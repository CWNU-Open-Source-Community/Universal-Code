import { spawn, type ChildProcess } from "node:child_process";
import { mkdir, unlink, writeFile } from "node:fs/promises";
import { dirname, resolve } from "node:path";
import { fileURLToPath } from "node:url";

const web = resolve(dirname(fileURLToPath(import.meta.url)), "..");
const repository = resolve(web, "..");
const artifacts = resolve(repository, "output/playwright/workbench-journeys");

async function command(executable: string, args: string[], cwd: string) {
  const child = spawn(executable, args, { cwd, stdio: "inherit", windowsHide: true });
  await new Promise<void>((accept, reject) => {
    child.once("error", reject);
    child.once("exit", (code) => code === 0 ? accept() : reject(new Error(`${executable} exited ${code}`)));
  });
}

async function stop(child: ChildProcess) {
  if (!child.pid || child.exitCode !== null || child.signalCode !== null) return;
  const exited = new Promise<void>((accept) => child.once("exit", () => accept()));
  // The harness owns its temporary directory and closes the real API on EOF.
  child.stdin?.end();
  const timer = setTimeout(() => child.kill("SIGKILL"), 10_000);
  try { await exited; } finally { clearTimeout(timer); }
}

export default async function setup() {
  await mkdir(artifacts, { recursive: true });
  if (process.env.WORKBENCH_JOURNEY_SKIP_BUILD !== "1") {
    await command(process.execPath, [resolve(web, "node_modules/typescript/bin/tsc"), "-b"], web);
    await command(process.execPath, [resolve(web, "node_modules/vite/bin/vite.js"), "build"], web);
  }
  const binary = resolve(artifacts, process.platform === "win32" ? "workbenchjourney.exe" : "workbenchjourney");
  await command("go", ["build", "-tags", "workbenchjourney", "-o", binary, "./cmd/workbenchjourney"], repository);
  const child = spawn(binary, ["--ui-dir", resolve(web, "dist")], {
    cwd: repository, stdio: ["pipe", "pipe", "pipe"], windowsHide: true,
  });
  let stdout = "";
  let stderr = "";
  child.stdout!.on("data", (chunk) => { stdout += chunk.toString(); });
  child.stderr!.on("data", (chunk) => { stderr += chunk.toString(); });
  const saveLog = () => writeFile(resolve(artifacts, "server.log"), `${stdout}\n${stderr}`);
  const shutdown = async () => {
    await stop(child);
    await saveLog();
    // Keep reports and traces, without uploading a large test-only executable.
    await unlink(binary);
  };
  try {
    await new Promise<void>((accept, reject) => {
      const deadline = setTimeout(() => finish(new Error(`Journey API startup timed out\n${stdout}\n${stderr}`)), 60_000);
      const poll = setInterval(() => {
        const address = stdout.match(/^api_url:\s*(\S+)/m)?.[1];
        const metadata = stdout.match(/^journey_metadata:\s*(\{.*\})/m)?.[1];
        if (!address || !metadata) return;
        process.env.WORKBENCH_JOURNEY_URL = address.replace(/\/api\/v1\/?$/, "");
        process.env.WORKBENCH_JOURNEY_METADATA = metadata;
        finish();
      }, 100);
      const finish = (error?: Error) => {
        clearTimeout(deadline); clearInterval(poll);
        child.off("error", onError); child.off("exit", onExit);
        error ? reject(error) : accept();
      };
      const onError = (error: Error) => finish(error);
      const onExit = (code: number | null) => finish(new Error(`Journey API exited ${code}\n${stdout}\n${stderr}`));
      child.once("error", onError); child.once("exit", onExit);
    });
  } catch (error) {
    await shutdown(); throw error;
  }
  return shutdown;
}
