import { expect, test } from "@playwright/test";
import { mkdir, writeFile } from "node:fs/promises";
import { resolve } from "node:path";

test("native Agent Browser uses the production API and visible workbench", async ({ page }) => {
  test.skip(process.env.WORKBENCH_JOURNEY_BROWSER !== "1", "explicit native browser acceptance");
  test.setTimeout(120_000);
  const metadata = JSON.parse(process.env.WORKBENCH_JOURNEY_METADATA!) as {
    workspace: string; read_token: string; control_token: string;
  };
  const before = process.env.WORKBENCH_JOURNEY_BROWSER_BEFORE === "1";
  const phase = before ? "before" : "after";
  const evidence = process.env.WORKBENCH_JOURNEY_BROWSER_EVIDENCE;
  expect(evidence, "native evidence output is required").toBeTruthy();
  await mkdir(evidence!, { recursive: true });
  let statusURL = "";
  page.on("response", (response) => {
    if (/\/api\/v1\/runs\/[^/]+\/agent-browser$/u.test(new URL(response.url()).pathname)) statusURL = response.url();
  });
  const readStatus = async () => {
    expect(statusURL, "the actual browser card must read its status").toBeTruthy();
    const response = await page.request.get(statusURL, { headers: { Authorization: `Bearer ${metadata.read_token}` } });
    expect(response.ok()).toBe(true);
    return (await response.json()).data;
  };
  await page.goto(process.env.WORKBENCH_JOURNEY_URL!);
  await page.getByLabel(/查看令牌|Read access token/u).fill(metadata.read_token);
  await page.getByLabel(/操作令牌|Control access token/u).fill(metadata.control_token);
  await page.getByRole("button", { name: "连接", exact: true }).click();
  await page.getByRole("button", { name: "新对话", exact: true }).click();
  await page.getByRole("button", { name: "切换为纯文本编辑" }).click();
  await page.getByRole("button", { name: "接入项目", exact: true }).click();
  const dialog = page.getByRole("dialog", { name: "创建项目" });
  await dialog.getByRole("button", { name: "添加", exact: true }).click();
  await dialog.getByRole("textbox", { name: "项目文件夹路径" }).fill(metadata.workspace);
  await dialog.getByRole("button", { name: "添加", exact: true }).click();
  await dialog.getByRole("button", { name: "创建项目", exact: true }).click();
  await expect(dialog).toBeHidden();
  await page.getByRole("textbox", { name: "开始新对话" }).fill("JOURNEY_BROWSER_SETUP: Read README.md and wait for the operator to confirm browser access.");
  await page.getByRole("button", { name: "发送消息", exact: true }).click();
  await expect(page.getByText(/JOURNEY_BROWSER_SETUP_READY/u)).toBeVisible();
  await page.getByRole("button", { name: "请求批准", exact: true }).click();
  await page.getByRole("menuitemradio", { name: "完全访问权限", exact: true }).click();
  await page.getByRole("button", { name: "确认启用", exact: true }).click();
  await expect(page.getByRole("button", { name: /完全访问权限 · 已激活/u })).toBeVisible();
  await page.getByRole("textbox", { name: "继续对话" }).fill("JOURNEY_BROWSER: Use the ordinary browser tools on the owned local test page; enter a query, apply it locally, and capture the actual page.");
  await page.getByRole("button", { name: "发送消息", exact: true }).click();
  const card = page.getByRole("region", { name: "Agent 浏览器", exact: true });
  await expect(card).toBeVisible();
  if (before) {
    await expect(page.getByText(/JOURNEY_BROWSER_UNAVAILABLE/u)).toBeVisible();
    await expect.poll(async () => (await readStatus()).unavailable_code).toBe("platform_unsupported");
    await expect(card.getByRole("button", { name: "停止浏览器" })).toHaveCount(0);
  } else {
    await expect.poll(async () => (await readStatus()).last_action, { timeout: 60_000 }).toBe("browser_screenshot");
    const image = card.getByRole("img", { name: /Agent 浏览器页面：Owned Agent Browser check/u });
    await expect(image).toBeVisible();
    expect(await image.evaluate((node) => (node as HTMLImageElement).naturalWidth)).toBeGreaterThan(0);
  }
  const observed = await readStatus();
  await card.scrollIntoViewIfNeeded();
  await page.screenshot({ path: resolve(evidence!, `uc-agent-browser-${phase}.png`) });
  let closed: unknown;
  if (!before) {
    await card.getByRole("button", { name: "停止浏览器", exact: true }).click();
    await expect.poll(async () => (await readStatus()).state).toBe("closed");
    closed = await readStatus();
    expect(closed).toMatchObject(process.platform === "darwin"
      ? { tree_reaped: false, process_scope: "darwin_group_and_scoped_children.v1",
        process_scope_verified: true, profile_removed: true, cleanup_pending: false }
      : { tree_reaped: true, profile_removed: true, cleanup_pending: false });
  }
  await writeFile(resolve(evidence!, `uc-agent-browser-${phase}.json`), JSON.stringify({
    phase, platform: process.platform, product_revision: process.env.WORKBENCH_JOURNEY_PRODUCT_REVISION,
    harness_revision: process.env.WORKBENCH_JOURNEY_HARNESS_REVISION,
    backend: "production app.ExecuteContext", frontend: "production Vite build", provider: "controlled offline fixture",
    provider_reply_held_for_capture: !before,
    full_permission_confirmed_in_ui: true, viewport: page.viewportSize(), observed, closed,
  }, null, 2));
});
