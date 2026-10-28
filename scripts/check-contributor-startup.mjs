// Execute the actual CONTRIBUTING snippets against an isolated store and browser.
// Prerequisites: npm ci in web, go mod download, playwright install chromium.
import assert from "node:assert/strict";
import { spawn, spawnSync } from "node:child_process";
import { mkdtemp, readFile, rm } from "node:fs/promises";
import { createRequire } from "node:module";
import { createServer } from "node:net";
import { tmpdir } from "node:os";
import { dirname, join, resolve } from "node:path";
import { fileURLToPath } from "node:url";
import { setTimeout as delay } from "node:timers/promises";

const root = resolve(dirname(fileURLToPath(import.meta.url)), "..");
const require = createRequire(join(root, "web/package.json"));
const { chromium } = require("@playwright/test");
const windows = process.platform === "win32";
const markdown = await readFile(join(root, "CONTRIBUTING.md"), "utf8");
function snippet(marker) {
  const pieces = markdown.split(`<!-- ${marker} -->`);
  assert.equal(pieces.length, 2, `Missing or duplicated documentation snippet: ${marker}`);
  const match = pieces[1].match(/^\s*```\w+\n([\s\S]*?)\n```/);
  assert.ok(match, `Missing code fence: ${marker}`);
  return match[1];
}

// Never connect to or stop somebody else's development server.
for (const port of [8765, 5173]) {
  await new Promise((accept, reject) => {
    const probe = createServer();
    probe.once("error", reject);
    probe.listen(port, "127.0.0.1", () => probe.close(accept));
  });
}
const home = await mkdtemp(join(tmpdir(), "contributor-startup-"));
const children = [];
let browser;
function start(command) {
  const env = { ...process.env, CYBERAGENT_HOME: home, NO_COLOR: "1" };
  for (const key of ["CYBERAGENT_API_TARGET", "VITE_API_BASE_URL", "CYBERAGENT_API_TOKEN", "CYBERAGENT_API_CONTROL_TOKEN"]) {
    delete env[key];
  }
  // Windows PowerShell 5.1 also exercises legacy native-argument quoting.
  const child = spawn(windows ? "powershell.exe" : "bash",
    windows ? ["-NoLogo", "-NoProfile", "-NonInteractive", "-Command", "$ErrorActionPreference = 'Stop'\n" + command]
      : ["-e", "-o", "pipefail", "-c", command],
    { cwd: root, env, detached: !windows, stdio: ["ignore", "pipe", "pipe"] });
  const state = { child, output: "", error: null };
  // Keep token-bearing output private. Only sanitized diagnostics leave this process.
  child.stdout.on("data", data => { state.output += data.toString(); });
  child.stderr.on("data", data => { state.output += data.toString(); });
  child.on("error", error => { state.error = error; });
  children.push(state);
  return state;
}
function sanitized(text) {
  return text.replace(/(?:Read|Control) token:[^\r\n]*/g, "token: [redacted]")
    .replace(/\b[0-9a-f]{64}\b/g, "[redacted]").slice(-4000);
}
async function ready(state, predicate) {
  const deadline = Date.now() + 300_000;
  while (Date.now() < deadline) {
    if (state.error) throw state.error;
    if (state.child.exitCode !== null) throw new Error("Documented command exited: " + sanitized(state.output));
    if (await predicate()) return;
    await delay(200);
  }
  throw new Error("Documented startup timed out: " + sanitized(state.output));
}

try {
  const backend = start(snippet(`dev-backend:${windows ? "powershell" : "bash"}`));
  await ready(backend, () => backend.output.includes("api_url: http://127.0.0.1:8765/api/v1"));
  const read = backend.output.match(/^Read token: (\S+)\r?$/m)?.[1];
  const control = backend.output.match(/^Control token: (\S+)\r?$/m)?.[1];
  // Do not use assertions that print secret values on failure.
  assert.ok(/^[0-9a-f]{64}$/.test(read), "Read token must encode 32 random bytes");
  assert.ok(/^[0-9a-f]{64}$/.test(control), "Control token must encode 32 random bytes");
  assert.ok(read !== control, "Read and control tokens must differ");
  const frontend = start(snippet("dev-frontend"));
  await ready(frontend, async () => {
    try { return (await fetch("http://127.0.0.1:5173", { signal: AbortSignal.timeout(1000) })).ok; }
    catch { return false; }
  });
  for (const origin of ["http://127.0.0.1:8765", "http://127.0.0.1:5173"]) {
    for (const path of ["health", "capabilities"]) {
      const url = `${origin}/api/v1/${path}`;
      const options = token => ({ headers: token ? { Authorization: `Bearer ${token}` } : {}, signal: AbortSignal.timeout(5000) });
      assert.equal((await fetch(url, options())).status, 401, "Anonymous access must be rejected");
      assert.equal((await fetch(url, options(control))).status, 401, "Control token must not grant read access");
      assert.equal((await fetch(url, options(read))).status, 200, "Read access must work directly and through Vite");
    }
  }
  browser = await chromium.launch({ headless: true });
  const page = await browser.newPage({ locale: "zh-CN" });
  await page.goto("http://127.0.0.1:5173");
  await page.locator("#read-token").fill(read);
  await page.locator("#control-token").fill(control);
  const responses = ["health", "capabilities"].map(path => page.waitForResponse(
    response => response.url() === `http://127.0.0.1:5173/api/v1/${path}` && response.status() === 200));
  await page.locator(".connection-panel button[type=submit]").click();
  await Promise.all(responses);
  await page.locator(".connection-page").waitFor({ state: "detached" });
  await page.getByRole("button", { name: "新对话", exact: true }).waitFor({ state: "visible" });
  assert.ok(!page.url().includes(read) && !page.url().includes(control), "Tokens must not enter the browser URL");
  console.log(`contributor-startup: ${windows ? "PowerShell" : "Bash"} snippets, token boundaries, Go API, Vite proxy and browser connection passed`);
} catch (error) {
  console.error(sanitized(String(error.stack ?? error)));
  process.exitCode = 1;
} finally {
  await browser?.close();
  for (const { child } of children.reverse()) {
    if (!child.pid) continue;
    if (windows) spawnSync("taskkill.exe", ["/PID", String(child.pid), "/T", "/F"], { stdio: "ignore" });
    else {
      try { process.kill(-child.pid, "SIGTERM"); } catch (error) { if (error.code !== "ESRCH") throw error; }
    }
  }
  for (const { child } of children) {
    if (child.exitCode === null && child.signalCode === null) {
      await new Promise(accept => child.once("exit", accept));
    }
  }
  await rm(home, { recursive: true, force: true, maxRetries: 10, retryDelay: 200 });
}
