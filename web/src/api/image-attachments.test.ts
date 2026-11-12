import { webcrypto } from "node:crypto";
import { APIClient } from "./client";
import type { WorkspaceImageAttachment } from "./image-attachments";

const bytes = new Uint8Array([137, 80, 78, 71, 1, 2, 3]);
const digest = async (value: Uint8Array) => [...new Uint8Array(await webcrypto.subtle.digest("SHA-256", new Uint8Array(value)))]
  .map((byte) => byte.toString(16).padStart(2, "0")).join("");
let metadata: WorkspaceImageAttachment;
beforeEach(async () => {
  vi.stubGlobal("crypto", webcrypto);
  metadata = { id: "image-1", workspace_id: "workspace-1", sha256: await digest(bytes), mime_type: "image/png",
    byte_size: bytes.length, width: 1, height: 1, name: "截图.png" };
});
afterEach(() => vi.unstubAllGlobals());

it("uploads original bytes with control authority and validates the immutable receipt", async () => {
  const fetcher = vi.fn(async () => new Response(JSON.stringify({ version: "api.v1", data: { image: metadata }, request_id: "image-upload" }),
    { status: 200, headers: { "Content-Type": "application/json", "X-CyberAgent-API-Version": "api.v1" } }));
  vi.stubGlobal("fetch", fetcher);
  const file = new File([bytes], "截图.png", { type: "image/png" });
  Object.defineProperty(file, "arrayBuffer", { value: async () => bytes.buffer });
  expect(await new APIClient("read", "/api/v1", "write").uploadWorkspaceImage("workspace-1", file, "original-image-key")).toEqual(metadata);
  const [url, init] = fetcher.mock.calls[0] as unknown as [string, RequestInit];
  const body = JSON.parse(init.body as string);
  expect(url).toContain("/workspaces/workspace-1/image-attachments");
  expect(body.data_base64).toBe(btoa(String.fromCharCode(...bytes)));
  expect(new Headers(init.headers).get("Authorization")).toBe("Bearer write");
  expect(new Headers(init.headers).get("Idempotency-Key")).toBe("original-image-key");
});

it("downloads with read authority and refuses corrupted bytes even when headers match", async () => {
  const headers = { "content-type": metadata.mime_type, "content-length": String(metadata.byte_size),
    etag: `"${metadata.sha256}"`, "x-cyberagent-content-sha256": metadata.sha256 };
  const fetcher = vi.fn().mockResolvedValueOnce(new Response(bytes, { headers }))
    .mockResolvedValueOnce(new Response(new Uint8Array(bytes.length), { headers }));
  vi.stubGlobal("fetch", fetcher);
  const client = new APIClient("read", "/api/v1", "write");
  expect((await client.downloadWorkspaceImage(metadata)).size).toBe(bytes.length);
  expect(new Headers(fetcher.mock.calls[0][1].headers).get("Authorization")).toBe("Bearer read");
  await expect(client.downloadWorkspaceImage(metadata)).rejects.toThrow("附件内容验证失败");
});

it("rejects an upload receipt from another workspace instead of showing its image", async () => {
  vi.stubGlobal("fetch", vi.fn(async () => new Response(JSON.stringify({ version: "api.v1", data: { image: { ...metadata, workspace_id: "other-workspace" } }, request_id: "wrong-image" }),
    { headers: { "Content-Type": "application/json", "X-CyberAgent-API-Version": "api.v1" } })));
  const file = new File([bytes], "截图.png", { type: "image/png" });
  Object.defineProperty(file, "arrayBuffer", { value: async () => bytes.buffer });
  await expect(new APIClient("read", "/api/v1", "write").uploadWorkspaceImage("workspace-1", file, "original-upload-key"))
    .rejects.toThrow("图片上传结果与原图不一致");
});

it("observes an original image upload with read authority and rejects foreign or malformed receipts", async () => {
  const response = (data: unknown) => new Response(JSON.stringify({ version: "api.v1", data, request_id: "image-observation" }),
    { headers: { "Content-Type": "application/json", "X-CyberAgent-API-Version": "api.v1" } });
  const fetcher = vi.fn().mockResolvedValueOnce(response({ state: "stored", image: metadata }))
    .mockResolvedValueOnce(response({ state: "not_received" }))
    .mockResolvedValueOnce(response({ state: "stored", image: { ...metadata, workspace_id: "other-workspace" } }))
    .mockResolvedValueOnce(response({ state: "not_received", image: metadata }));
  vi.stubGlobal("fetch", fetcher);
  const client = new APIClient("read", "/api/v1", "write");
  expect(await client.inspectWorkspaceImageUpload("workspace-1", "original-image-upload-key")).toEqual({ state: "stored", image: metadata });
  expect(await client.inspectWorkspaceImageUpload("workspace-1", "original-image-upload-key")).toEqual({ state: "not_received" });
  for (let index = 0; index < 2; index++) await expect(client.inspectWorkspaceImageUpload("workspace-1", "original-image-upload-key")).rejects.toThrow("无法核对");
  const [url, init] = fetcher.mock.calls[0] as [string, RequestInit];
  expect(url).toContain("/workspaces/workspace-1/image-attachments/request");
  expect(init.method).toBe("GET");
  expect(new Headers(init.headers).get("Authorization")).toBe("Bearer read");
  expect(new Headers(init.headers).get("Idempotency-Key")).toBe("original-image-upload-key");
});
