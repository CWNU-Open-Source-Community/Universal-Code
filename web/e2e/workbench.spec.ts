import { expect, test, type Locator, type Page, type TestInfo } from "@playwright/test";
import { readFile, writeFile } from "node:fs/promises";
import { join } from "node:path";

type Metadata = { workspace: string; read_token: string; control_token: string };
const metadata = () => JSON.parse(process.env.WORKBENCH_JOURNEY_METADATA!) as Metadata;
const origin = () => process.env.WORKBENCH_JOURNEY_URL!;
type Timing = { operation: string; elapsed_ms: number; hang_budget_ms: number; settled: boolean };

async function measure(timings: Timing[], operation: string, action: () => Promise<void>) {
  const started = performance.now();
  let settled = false;
  try {
    await action();
    settled = true;
    // A generous regression bound catches hangs; this is not a responsiveness SLA.
    expect(performance.now() - started, `${operation} did not settle within the hang budget`).toBeLessThan(30_000);
  } finally {
    timings.push({ operation, elapsed_ms: Math.round(performance.now() - started), hang_budget_ms: 30_000, settled });
  }
}

async function record(timings: Timing[], info: TestInfo) {
  const path = info.outputPath("interaction-timings.json");
  await writeFile(path, JSON.stringify({ purpose: "Observed user-operation durations; budgets only detect hangs", timings }, null, 2));
  await info.attach("interaction-timings", { path, contentType: "application/json" });
}

async function connect(page: Page) {
  await page.goto(origin());
  await reconnect(page);
  await page.getByRole("button", { name: "新对话", exact: true }).click();
  await expect(page.getByRole("textbox", { name: "开始新对话" })).toBeVisible();
  // These command journeys use exact plain-text sentinels for their offline
  // provider. Exercise the supported source mode; rich editing has its own tests.
  await page.getByRole("button", { name: "切换为纯文本编辑" }).click();
}

async function reconnect(page: Page) {
  await page.getByLabel(/查看令牌|Read access token/u).fill(metadata().read_token);
  await page.getByLabel(/操作令牌|Control access token/u).fill(metadata().control_token);
  await page.getByRole("button", { name: "连接", exact: true }).click();
}

async function importProject(page: Page) {
  const trigger = page.getByRole("button", { name: "接入项目", exact: true });
  await trigger.focus();
  await page.keyboard.press("Enter");
  const dialog = page.getByRole("dialog", { name: "接入已有项目" });
  const input = dialog.getByRole("textbox", { name: "项目文件夹路径" });
  await expect(input).toBeFocused();
  await page.keyboard.press("Escape");
  await expect(dialog).toBeHidden();
  await expect(trigger).toBeFocused();
  await page.keyboard.press("Enter");
  await input.fill(metadata().workspace);
  await page.keyboard.press("Shift+Tab");
  await expect(dialog.getByRole("button", { name: "关闭项目接入" })).toBeFocused();
  await page.keyboard.press("Shift+Tab");
  await expect(dialog.getByRole("button", { name: "接入此目录" })).toBeFocused();
  await dialog.getByRole("button", { name: "接入此目录" }).click();
  await expect(dialog).toBeHidden();
  await expect(page.getByRole("combobox", { name: "选择工作区" })).not.toHaveValue("");
}

async function submitRead(page: Page) {
  await page.getByRole("textbox", { name: "开始新对话" }).fill(
    "JOURNEY_READ: Read README.md using the file tool and report the exact journey proof.");
  await page.getByRole("button", { name: "发送消息", exact: true }).click();
  await expect(page.getByRole("textbox", { name: "继续对话" })).toBeVisible();
  await expect(page.getByText("Read README.md: JOURNEY_FILE_PROOF_2026", { exact: true })).toBeVisible();
}

async function withinViewport(locator: Locator, page: Page) {
  const bounds = await locator.boundingBox();
  expect(bounds).not.toBeNull();
  const viewport = page.viewportSize()!;
  expect(bounds!.x).toBeGreaterThanOrEqual(-1);
  expect(bounds!.x + bounds!.width).toBeLessThanOrEqual(viewport.width + 1);
}

async function narrowConversation(page: Page) {
  // Wait for the reconnected workbench to mount before changing its viewport.
  await expect(page.getByRole("textbox", { name: "继续对话" })).toBeVisible();
  await page.setViewportSize({ width: 390, height: 844 });
  const toggle = page.getByRole("button", { name: /^(?:显示|隐藏)侧栏$/u });
  await expect(toggle).toBeVisible();
  if (await toggle.getAttribute("aria-pressed") === "true") await toggle.click();
  await expect(toggle).toHaveAttribute("aria-pressed", "false");
  await expect(page.getByRole("navigation", { name: "对话导航", exact: true })).toBeHidden();
}

test("normal entry reads a real project file and retains the task after reload", async ({ page }, info) => {
  const timings: Timing[] = [];
  try {
    await measure(timings, "connect-to-workbench", () => connect(page));
    await measure(timings, "keyboard-import-project", () => importProject(page));
    await measure(timings, "send-task-to-visible-file-result", () => submitRead(page));
    const taskURL = page.url();
    expect(taskURL).toMatch(/#\/threads\//u);
    const before = await readFile(join(metadata().workspace, "README.md"), "utf8");
    expect(before).toContain("JOURNEY_FILE_PROOF_2026");
    await measure(timings, "reload-and-reopen-persisted-task", async () => {
      await page.reload();
      await reconnect(page);
      await expect(page.getByText("Read README.md: JOURNEY_FILE_PROOF_2026", { exact: true })).toBeVisible();
      expect(page.url()).toBe(taskURL);
    });
    await narrowConversation(page);
    await withinViewport(page.getByRole("textbox", { name: "继续对话" }), page);
    await withinViewport(page.getByRole("button", { name: "发送消息", exact: true }), page);
    expect(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth + 1)).toBe(true);
    await info.attach("narrow-workbench", { body: await page.screenshot(), contentType: "image/png" });
  } finally { await record(timings, info); }
});

async function openFiles(page: Page) {
  await page.getByRole("button", { name: "工作区文件", exact: true }).click();
  await expect(page.getByRole("button", { name: "关闭文件面板", exact: true })).toBeVisible();
}

async function editReadme(page: Page) {
  const files = page.locator(".v2-file-drawer");
  await files.getByRole("button", { name: /^README\.md/u }).click();
  await files.getByRole("button", { name: "编辑提案", exact: true }).click();
  const editor = page.getByRole("region", { name: "编辑 README.md 的提案", exact: true });
  await expect(editor.locator(".monaco-editor")).toBeVisible();
  // Monaco's native EditContext is a zero-size keyboard target on modern Chromium.
  await expect(editor.getByRole("textbox", { name: "Editor content", exact: true })).toBeAttached();
  return editor;
}

async function keepDraft(page: Page) {
  const dialog = page.getByRole("dialog", { name: "处理未提交的文件修改", exact: true });
  await expect(dialog).toBeVisible();
  await expect(dialog.getByRole("button", { name: "继续编辑", exact: true })).toBeFocused();
  await dialog.getByRole("button", { name: "保留草稿", exact: true }).click();
  await expect(dialog).toBeHidden();
}

test("manual file draft survives file switches, panel close and a browser reload", async ({ page }, info) => {
  const timings: Timing[] = [];
  try {
    await connect(page);
    await importProject(page);
    await submitRead(page);
    const original = await readFile(join(metadata().workspace, "README.md"), "utf8");
    const marker = "Journey unsent local edit survives navigation";
    await measure(timings, "open-file-editor", async () => { await openFiles(page); await editReadme(page); });
    const editor = page.getByRole("region", { name: "编辑 README.md 的提案", exact: true });
    await editor.getByRole("textbox", { name: "Editor content", exact: true }).focus();
    await expect(editor.getByRole("textbox", { name: "Editor content", exact: true })).toBeFocused();
    await page.keyboard.press("ControlOrMeta+A");
    await page.keyboard.insertText(`${original}\n${marker}\n`);
    await expect(editor.locator(".view-lines")).toContainText(marker);
    await measure(timings, "switch-file-with-draft", async () => {
      await page.getByRole("button", { name: "打开上级目录", exact: true }).click();
      await keepDraft(page);
      await page.locator(".v2-file-drawer").getByRole("button", { name: /^notes\.txt/u }).click();
      await expect(page.locator(".explorer-file-lines")).toContainText("JOURNEY_CHILD_PROOF_2026");
      await page.getByRole("button", { name: "打开上级目录", exact: true }).click();
      await editReadme(page);
      await expect(editor.locator(".view-lines")).toContainText(marker);
    });
    await measure(timings, "close-and-reopen-file-draft", async () => {
      await page.getByRole("button", { name: "关闭文件面板", exact: true }).click();
      await keepDraft(page);
      await openFiles(page);
      await editReadme(page);
      await expect(editor.locator(".view-lines")).toContainText(marker);
    });
    await page.getByRole("button", { name: "关闭文件面板", exact: true }).click();
    await keepDraft(page);
    await page.reload();
    await reconnect(page);
    await narrowConversation(page);
    await measure(timings, "restore-file-draft-after-reload", async () => {
      await openFiles(page);
      await editReadme(page);
      await expect(editor.locator(".view-lines")).toContainText(marker);
    });
    expect(await readFile(join(metadata().workspace, "README.md"), "utf8")).toBe(original);
    await withinViewport(page.locator(".v2-file-drawer"), page);
    await withinViewport(page.getByRole("button", { name: "关闭文件面板", exact: true }), page);
    // Settle the explicit leave guard before closing the page.
    await page.getByRole("button", { name: "关闭文件面板", exact: true }).click();
    await keepDraft(page);
  } finally { await record(timings, info); }
});

test("an approved child runs continuously and the main conversation receives its summary", async ({ page }, info) => {
  const timings: Timing[] = [];
  try {
    await connect(page);
    await importProject(page);
    await page.getByRole("textbox", { name: "开始新对话" }).fill(
      "JOURNEY_CHILD: Delegate reading notes.txt to a child, run it continuously, and summarize the finished report.");
    await page.getByRole("button", { name: "发送消息", exact: true }).click();
    await expect(page.getByRole("textbox", { name: "继续对话" })).toBeVisible();
    const conversationURL = page.url();
    await page.getByRole("button", { name: "观察执行", exact: true }).click();
    await page.getByRole("button", { name: "运行与工具", exact: true }).click();
    await page.getByRole("button", { name: "高级诊断", exact: true }).click();
    await page.getByRole("tab", { name: "子任务与交付", exact: true }).click();
    await page.getByRole("button", { name: "批准", exact: true }).click();
    await page.getByRole("button", { name: "准入子任务", exact: true }).click();
    await expect(page.getByText(/^admitted(?: · .*)?$/u)).toBeVisible();
    await page.getByRole("tab", { name: "子智能体", exact: true }).click();
    await measure(timings, "child-continuation-to-parent-summary", async () => {
      await page.getByRole("button", { name: "连续运行并汇总", exact: true }).click();
      await expect(page.getByText("主会话已汇总", { exact: true })).toBeVisible({ timeout: 30_000 });
    });
    await expect(page.getByText("子代理已完成", { exact: true })).toBeVisible();
    await expect(page.getByText("2 轮子代理执行", { exact: true })).toBeVisible();
    await page.getByRole("link", { name: "查看主会话", exact: true }).click();
    await expect(page).toHaveURL(conversationURL);
    await expect(page.getByText("Child completed; main conversation summary: JOURNEY_CHILD_PROOF_2026", { exact: true })).toBeVisible();
    await expect(page.getByRole("textbox", { name: "继续对话" })).toBeEnabled();
    await info.attach("main-conversation-child-summary", { body: await page.screenshot(), contentType: "image/png" });
  } finally { await record(timings, info); }
});
