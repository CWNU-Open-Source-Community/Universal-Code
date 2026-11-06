import { expect, test } from "@playwright/test";
import { writeFile } from "node:fs/promises";

test("a damaged image upload record can be left out without deleting another draft", async ({ page }, info) => {
  const address = process.env.WORKBENCH_JOURNEY_URL!;
  const metadata = JSON.parse(process.env.WORKBENCH_JOURNEY_METADATA!);
  const reconnect = async () => {
    await page.getByLabel(/查看令牌|Read access token/u).fill(metadata.read_token);
    await page.getByLabel(/操作令牌|Control access token/u).fill(metadata.control_token);
    await page.getByRole("button", { name: "连接", exact: true }).click();
  };
  await page.goto(address); await reconnect();
  await page.getByRole("button", { name: "新对话", exact: true }).click();
  await page.getByRole("button", { name: "接入项目", exact: true }).click();
  await page.getByRole("button", { name: "添加", exact: true }).click();
  await page.getByRole("textbox", { name: "项目文件夹路径", exact: true }).fill(metadata.workspace);
  await page.getByRole("button", { name: "添加", exact: true }).click();
  await page.getByRole("button", { name: "创建项目", exact: true }).click();
  await page.getByRole("button", { name: "切换为纯文本编辑", exact: true }).click();
  const composer = page.getByRole("textbox", { name: "开始新对话", exact: true });
  const text = "Keep this text draft after leaving out the damaged image record";
  await composer.fill(text);
  await expect(page.getByRole("button", { name: "发送消息", exact: true })).toBeEnabled();
  const workspaces = await page.request.get(`${address}/api/v1/workspaces`, { headers: { Authorization: `Bearer ${metadata.read_token}` } });
  const workspaceID = (await workspaces.json()).data[0].id;
  const injected = await page.evaluate(({ workspaceID }) => {
    const existing = Object.keys(localStorage).find((key) => key.startsWith("v2_recovery.v1:"))!;
    const storagePrefix = existing.slice(0, existing.lastIndexOf(":") + 1);
    const key = storagePrefix + encodeURIComponent(`image-upload:${JSON.stringify([workspaceID, ""])}:v2-image-upload-damaged`);
    const goodKey = storagePrefix + encodeURIComponent(`image-upload:${JSON.stringify([workspaceID, "other-thread"])}:v2-image-upload-untouched`);
    const good = JSON.stringify({ version: "v2_recovery.v1", value: { version: "v2_image_upload.v1", operationKey: "v2-image-upload-untouched",
      name: "other-draft.png", sha256: "a".repeat(64), byteSize: 135, mimeType: "image/png" } });
    const broken = JSON.stringify({ version: "v2_recovery.v1", value: { version: "v2_image_upload.v1", operationKey: "v2-image-upload-damaged",
      name: "damaged.png", sha256: "broken", byteSize: 135, mimeType: "image/png" } });
    localStorage.setItem(goodKey, good); localStorage.setItem(key, broken);
    return { key, broken, goodKey, good };
  }, { workspaceID });
  await page.reload(); await reconnect();
  await expect(composer).toHaveValue(text);
  await expect(page.getByText(/图片上传记录无法读取/u)).toBeVisible();
  await expect(page.getByRole("button", { name: "发送消息", exact: true })).toBeDisabled();
  for (const theme of ["light", "dark"] as const) {
    await page.evaluate((value) => localStorage.setItem("prayu.theme", value), theme);
    await page.reload(); await reconnect();
    for (const [width, height] of [[1280, 800], [390, 844]]) {
      await page.setViewportSize({ width, height });
      await expect(composer).toHaveValue(text);
      if (width === 390) {
        await expect(page.getByRole("button", { name: /^(?:显示|隐藏)侧栏$/u })).toHaveAttribute("aria-pressed", "false");
        await expect(page.getByRole("navigation", { name: "对话导航", exact: true })).toBeHidden();
      }
      await page.waitForTimeout(400);
      const discard = page.getByRole("button", { name: "丢弃记录", exact: true });
      await expect(discard).toBeVisible();
      const bounds = (await discard.boundingBox())!;
      expect(bounds.y).toBeGreaterThanOrEqual(0);
      expect(bounds.y + bounds.height).toBeLessThanOrEqual(height);
      expect(await discard.evaluate((button) => {
        const rect = button.getBoundingClientRect();
        return button.contains(document.elementFromPoint(rect.x + rect.width / 2, rect.y + rect.height / 2));
      })).toBe(true);
      await page.screenshot({ path: info.outputPath(`damaged-${theme}-${width}.png`) });
    }
  }
  await writeFile(info.outputPath("damaged-storage.json"), JSON.stringify(injected, null, 2));
  await expect(page.getByRole("button", { name: "丢弃记录", exact: true })).toBeVisible();
  await page.getByRole("button", { name: "丢弃记录", exact: true }).click();
  await expect(page.getByRole("button", { name: "发送消息", exact: true })).toBeEnabled();
  await expect(page.getByRole("alert")).toHaveCount(0);
  expect(await page.evaluate((key) => localStorage.getItem(key), injected.key)).toBeNull();
  expect(await page.evaluate((key) => localStorage.getItem(key), injected.goodKey)).toBe(injected.good);
  await expect(composer).toHaveValue(text);
  await page.screenshot({ path: info.outputPath("discarded-text-draft.png") });
  await page.reload(); await reconnect();
  await expect(composer).toHaveValue(text);
  await expect(page.getByRole("button", { name: "发送消息", exact: true })).toBeEnabled();
  expect(await page.evaluate((key) => localStorage.getItem(key), injected.goodKey)).toBe(injected.good);
  await composer.fill("JOURNEY_READ: Continue after leaving out only the damaged image record.");
  await page.getByRole("button", { name: "发送消息", exact: true }).click();
  await expect(page.getByText("Read README.md: JOURNEY_FILE_PROOF_2026", { exact: true })).toBeVisible();
});
