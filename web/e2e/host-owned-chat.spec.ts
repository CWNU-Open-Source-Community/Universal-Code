import { expect, test, type Page } from "@playwright/test";
import { readFile, writeFile } from "node:fs/promises";
import { join } from "node:path";

type Metadata = { workspace: string; temp_root: string; read_token: string; control_token: string };
type Execution = { state: string; execution_id: string; last_turn_interrupted?: boolean };
type Observation = { state: string; settled: boolean; message_id: string };
type Message = { role: string; content: string };
const metadata = () => JSON.parse(process.env.WORKBENCH_JOURNEY_METADATA!) as Metadata;
const origin = () => process.env.WORKBENCH_JOURNEY_URL!;

async function reconnect(page: Page) {
  await page.getByLabel(/查看令牌|Read access token/u).fill(metadata().read_token);
  await page.getByLabel(/操作令牌|Control access token/u).fill(metadata().control_token);
  await page.getByRole("button", { name: "连接", exact: true }).click();
}

async function readMarker(token: string, suffix: string) {
  try { return await readFile(join(metadata().temp_root, "reconnect", `${token}.${suffix}`), "utf8"); }
  catch (error) { if ((error as NodeJS.ErrnoException).code === "ENOENT") return ""; throw error; }
}

async function readAPI<T>(page: Page, path: string, key?: string) {
  const response = await page.request.get(`${origin()}/api/v1${path}`, { headers: {
    Authorization: `Bearer ${metadata().read_token}`, ...(key ? { "Idempotency-Key": key } : {}),
  } });
  expect(response.ok(), `${path}: ${await response.text()}`).toBe(true);
  return (await response.json()).data as T;
}

for (const boundary of ["complete", "stop"] as const) {
  test(`accepted chat survives page reload before ${boundary}`, async ({ page }, info) => {
    test.skip(process.env.WORKBENCH_JOURNEY_RECONNECT !== "1", "explicit held-response reconnect acceptance");
    const token = `reload-${boundary}`;
    const content = `JOURNEY_RECONNECT: ${token} Keep this exact accepted request across a page refresh.`;
    await page.goto(origin());
    await reconnect(page);
    await page.getByRole("button", { name: "新对话", exact: true }).click();
    await page.getByRole("button", { name: "切换为纯文本编辑" }).click();
    await page.getByRole("button", { name: "接入项目", exact: true }).click();
    const dialog = page.getByRole("dialog", { name: "创建项目" });
    await dialog.getByRole("button", { name: "添加", exact: true }).click();
    await dialog.getByRole("textbox", { name: "项目文件夹路径" }).fill(metadata().workspace);
    await dialog.getByRole("button", { name: "添加", exact: true }).click();
    await dialog.getByRole("button", { name: "创建项目", exact: true }).click();
    await expect(dialog).toBeHidden();
    await page.getByRole("textbox", { name: "开始新对话" }).fill(content);
    const submitted = page.waitForRequest((request) => request.method() === "POST" &&
      /\/api\/v1\/threads\/[^/]+\/turns$/u.test(new URL(request.url()).pathname));
    await page.getByRole("button", { name: "发送消息", exact: true }).click();
    const post = await submitted;
    const threadID = new URL(post.url()).pathname.split("/").at(-2)!;
    const operationKey = post.headers()["idempotency-key"]!;
    expect(operationKey).toBeTruthy();
    await expect.poll(() => readMarker(token, "requests")).toBe("started\n");
    const before = await readAPI<Execution>(page, `/threads/${threadID}/execution`);
    expect(before.state).toBe("running");
    await page.reload();
    await reconnect(page);
    await expect(page.getByRole("button", { name: "停止当前执行", exact: true })).toBeVisible();
    const reconnected = await readAPI<Execution>(page, `/threads/${threadID}/execution`);
    expect(reconnected).toMatchObject({ state: "running", execution_id: before.execution_id });
    const runningScreenshot = info.outputPath("reconnected-running.png");
    await page.screenshot({ path: runningScreenshot });
    await info.attach("reconnected-running", { path: runningScreenshot, contentType: "image/png" });
    if (boundary === "complete") {
      await writeFile(join(metadata().temp_root, "reconnect", `${token}.release`), "release\n");
      await expect(page.getByText(`JOURNEY_RECONNECT_COMPLETE ${token}`, { exact: true })).toBeVisible();
      await expect.poll(async () => (await readAPI<Observation>(page, `/threads/${threadID}/turn-request`, operationKey)).state).toBe("completed");
    } else {
      await page.getByRole("button", { name: "停止当前执行", exact: true }).click();
      await expect.poll(() => readMarker(token, "cancelled")).toBe("cancelled\n");
      await expect(page.getByText(/上次执行已中断/u)).toBeVisible();
      await expect.poll(async () => (await readAPI<Observation>(page, `/threads/${threadID}/turn-request`, operationKey)).state).toBe("failed");
    }
    await expect.poll(async () => (await readAPI<Execution>(page, `/threads/${threadID}/execution`)).state).toBe("idle");
    await expect(page.getByRole("button", { name: "停止当前执行", exact: true })).toBeHidden();
    await expect(page.getByText("正在工作", { exact: true })).toBeHidden();
    await expect(page.getByRole("button", { name: "待处理 0 条 · 正在处理 1 条", exact: true })).toBeHidden();
    const messages = await readAPI<Message[]>(page, `/threads/${threadID}/messages?limit=100`);
    expect(messages.filter((message) => message.role === "user" && message.content === content)).toHaveLength(1);
    expect(await readMarker(token, "requests")).toBe("started\n");
    const observed = await readAPI<Observation>(page, `/threads/${threadID}/turn-request`, operationKey);
    expect(observed.settled).toBe(true);
    const outcomeScreenshot = info.outputPath(`reloaded-${boundary}.png`);
    await page.screenshot({ path: outcomeScreenshot });
    await info.attach(`reloaded-${boundary}`, { path: outcomeScreenshot, contentType: "image/png" });
    const outcomeFile = info.outputPath("accepted-turn-outcome.json");
    await writeFile(outcomeFile, JSON.stringify({ threadID, before, reconnected, observed,
      model_requests: 1, original_user_messages: 1, boundary }, null, 2));
    await info.attach("accepted-turn-outcome", { path: outcomeFile, contentType: "application/json" });
  });
}
