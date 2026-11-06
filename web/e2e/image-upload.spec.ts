import { expect, test } from "@playwright/test";
import { createServer, request, type Server } from "node:http";
import type { AddressInfo } from "node:net";

// Self-created 64 x 32 solid blue PNG; no user image or external media.
const pixels = Buffer.from("iVBORw0KGgoAAAANSUhEUgAAAEAAAAAgCAIAAAAt/+nTAAAATklEQVR4nO3PUQkAIBTAwBfJaEY0miH8OITBAtxm7fN1wwUNaEEDWtCAFjSgBQ1oQQNa0IAWNKAFDWhBA1rQgBY0oAUNaEEDWtCAFjx2AV6dAKZPpj0uAAAAAElFTkSuQmCC", "base64");

test("a stored image upload recovers its original receipt after response loss and reload", async ({ page }, info) => {
  const upstream = new URL(process.env.WORKBENCH_JOURNEY_URL!);
  const metadata = JSON.parse(process.env.WORKBENCH_JOURNEY_METADATA!);
  let uploads = 0;
  const observations: string[] = [], downloads: string[] = [];
  let stored: { key: string; image: { id: string; workspace_id: string; sha256: string } } | undefined;
  const proxy: Server = createServer((incoming, outgoing) => {
    if (incoming.method === "GET" && /\/image-attachments\/request$/u.test(incoming.url ?? "")) observations.push(String(incoming.headers["idempotency-key"]));
    if (incoming.method === "GET" && /\/image-attachments\/image-[^/]+\/content$/u.test(incoming.url ?? "")) downloads.push(incoming.url!);
    const drop = incoming.method === "POST" && /\/image-attachments$/u.test(incoming.url ?? "");
    const forwarded = request({ hostname: upstream.hostname, port: upstream.port, path: incoming.url,
      method: incoming.method, headers: { ...incoming.headers, host: upstream.host } }, (response) => {
      if (!drop) { outgoing.writeHead(response.statusCode!, response.headers); response.pipe(outgoing); return; }
      uploads++;
      const chunks: Buffer[] = [];
      response.on("data", (chunk: Buffer) => chunks.push(chunk));
      response.on("end", () => {
        const body = Buffer.concat(chunks);
        stored = { key: String(incoming.headers["idempotency-key"]), image: JSON.parse(body.toString()).data.image };
        // The Go handler already committed and returned this real receipt.
        // Truncate its actual bytes in transit, after sending real HTTP headers.
        outgoing.writeHead(response.statusCode!, { ...response.headers, "content-length": body.length });
        outgoing.flushHeaders(); outgoing.write(body.subarray(0, 16));
        setTimeout(() => outgoing.destroy(), 20);
      });
    });
    forwarded.on("error", () => outgoing.destroy());
    incoming.pipe(forwarded);
  });
  await new Promise<void>((resolve) => proxy.listen(0, "127.0.0.1", resolve));
  const address = `http://127.0.0.1:${(proxy.address() as AddressInfo).port}`;
  const reconnect = async () => {
    await page.getByLabel(/查看令牌|Read access token/u).fill(metadata.read_token);
    await page.getByLabel(/操作令牌|Control access token/u).fill(metadata.control_token);
    await page.getByRole("button", { name: "连接", exact: true }).click();
  };
  try {
    await page.goto(address); await reconnect();
    await page.getByRole("button", { name: "接入项目", exact: true }).click();
    await page.getByRole("button", { name: "添加", exact: true }).click();
    await page.getByRole("textbox", { name: "项目文件夹路径", exact: true }).fill(metadata.workspace);
    await page.getByRole("button", { name: "添加", exact: true }).click();
    await page.getByRole("button", { name: "创建项目", exact: true }).click();
    await page.getByRole("button", { name: "切换为纯文本编辑", exact: true }).click();
    const composer = page.getByRole("textbox", { name: "开始新对话", exact: true });
    await composer.fill("Keep this draft while recovering the uploaded image");
    await page.getByLabel("选择图片或文件", { exact: true }).setInputFiles({ name: "self-made-blue.png", mimeType: "image/png", buffer: pixels });
    await expect(page.getByRole("alert")).toBeVisible();
    expect(stored?.image.id).toMatch(/^image-/u);
    const original = stored!;
    const saved = await page.request.get(`${address}/api/v1/workspaces/${original.image.workspace_id}/image-attachments/${original.image.id}`,
      { headers: { Authorization: `Bearer ${metadata.read_token}` } });
    expect(saved.ok()).toBe(true);
    expect((await saved.json()).data.image).toEqual(original.image);
    await page.reload(); await reconnect();
    await expect(composer).toHaveValue("Keep this draft while recovering the uploaded image");
    await expect(page.getByRole("button", { name: "预览图片 self-made-blue.png", exact: true })).toBeEnabled();
    await expect(page.getByRole("button", { name: "核对原图片上传", exact: true })).toBeHidden();
    expect(uploads).toBe(1);
    expect(observations).toEqual([original.key]);
    expect(downloads).toContain(`/api/v1/workspaces/${original.image.workspace_id}/image-attachments/${original.image.id}/content`);
    await info.attach("recovered-original-image", { body: await page.screenshot(), contentType: "image/png" });
    await page.reload(); await reconnect();
    await expect(composer).toHaveValue("Keep this draft while recovering the uploaded image");
    await expect(page.getByRole("button", { name: "预览图片 self-made-blue.png", exact: true })).toBeEnabled();
    expect(uploads).toBe(1);
  } finally {
    proxy.closeAllConnections();
    await new Promise<void>((resolve, reject) => proxy.close((error) => error ? reject(error) : resolve()));
  }
});
