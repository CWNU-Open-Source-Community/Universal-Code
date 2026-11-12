import { expect, test } from "@playwright/test";

for (const mode of ["single", "mixed"] as const) {
  test(`new conversation image recovery notices: ${mode}`, async ({ page }, info) => {
    const origin = process.env.WORKBENCH_JOURNEY_URL!;
    const metadata = JSON.parse(process.env.WORKBENCH_JOURNEY_METADATA!);
    const connect = async () => {
      await page.getByLabel(/查看令牌|Read access token/u).fill(metadata.read_token);
      await page.getByLabel(/操作令牌|Control access token/u).fill(metadata.control_token);
      await page.getByRole("button", { name: "连接", exact: true }).click();
    };
    await page.goto(origin); await connect();
    await page.getByRole("button", { name: "新对话", exact: true }).click();
    await page.getByRole("button", { name: "接入项目", exact: true }).click();
    await page.getByRole("button", { name: "添加", exact: true }).click();
    await page.getByRole("textbox", { name: "项目文件夹路径", exact: true }).fill(metadata.workspace);
    await page.getByRole("button", { name: "添加", exact: true }).click();
    await page.getByRole("button", { name: "创建项目", exact: true }).click();
    await page.getByRole("button", { name: "切换为纯文本编辑", exact: true }).click();
    const composer = page.getByRole("textbox", { name: "开始新对话", exact: true });
    const text = "Keep this draft while discarding only the damaged image";
    await composer.fill(text);
    const workspaces = await page.request.get(`${origin}/api/v1/workspaces`, { headers: { Authorization: `Bearer ${metadata.read_token}` } });
    const workspaceID = (await workspaces.json()).data[0].id;
    const record = await page.evaluate(({ workspaceID, mixed }) => {
      const existing = Object.keys(localStorage).find((key) => key.startsWith("v2_recovery.v1:"))!;
      const prefix = existing.slice(0, existing.lastIndexOf(":") + 1);
      const imageKey = prefix + encodeURIComponent(`image-upload:${JSON.stringify([workspaceID, ""])}:v2-image-upload-notice`);
      const image = JSON.stringify({ version: "v2_recovery.v1", value: { version: "v2_image_upload.v1", operationKey: "v2-image-upload-notice",
        name: "damaged-test-image.png", sha256: "damaged", byteSize: 135, mimeType: "image/png" } });
      const creationKey = prefix + encodeURIComponent("creation:invalid-notice-fixture");
      const creation = JSON.stringify({ version: "v2_recovery.v1", value: { damaged: true } });
      localStorage.setItem(imageKey, image);
      if (mixed) localStorage.setItem(creationKey, creation);
      return { imageKey, image, creationKey, creation };
    }, { workspaceID, mixed: mode === "mixed" });
    for (const theme of ["light", "dark"] as const) {
      await page.evaluate((value) => localStorage.setItem("prayu.theme", value), theme);
      await page.reload(); await connect();
      await expect(page.getByRole("button", { name: "丢弃记录", exact: true })).toBeVisible();
      await expect(page.getByRole("button", { name: "发送消息", exact: true })).toBeDisabled();
      for (const [width, height] of [[1280, 800], [390, 844]]) {
        await page.setViewportSize({ width, height });
        if (width === 390) await expect(page.getByRole("navigation", { name: "对话导航", exact: true })).toBeHidden();
        await page.waitForTimeout(400);
        await page.screenshot({ path: info.outputPath(`${mode}-${theme}-${width}.png`) });
      }
    }
    await expect(page.getByRole("alert")).toHaveCount(mode === "single" ? 0 : 1);
    if (mode === "mixed") await expect(page.getByRole("alert")).toContainText("原创建请求无法可靠核对");
    await page.getByRole("button", { name: "丢弃记录", exact: true }).click();
    await expect(composer).toHaveValue(text);
    expect(await page.evaluate((key) => localStorage.getItem(key), record.imageKey)).toBeNull();
    if (mode === "single") {
      await expect(page.getByRole("alert")).toHaveCount(0);
      await expect(page.getByRole("button", { name: "发送消息", exact: true })).toBeEnabled();
    } else {
      await expect(page.getByRole("alert")).toContainText("原创建请求无法可靠核对");
      let creations = 0;
      page.on("request", (request) => { if (request.method() === "POST" && new URL(request.url()).pathname === "/api/v1/threads") creations++; });
      await page.getByRole("button", { name: "发送消息", exact: true }).click();
      await expect(page.locator(".v2-composer-error")).toContainText("原创建请求无法可靠核对");
      expect(creations).toBe(0);
      expect(await page.evaluate((key) => localStorage.getItem(key), record.creationKey)).toBe(record.creation);
      await expect(composer).toHaveValue(text);
    }
  });
}
