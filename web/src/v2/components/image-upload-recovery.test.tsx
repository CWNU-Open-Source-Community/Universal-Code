import { File as NodeFile } from "node:buffer";
import { webcrypto } from "node:crypto";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act, cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import type { APIClient } from "../../api/client";
import type { WorkspaceImageAttachment } from "../../api/image-attachments";
import { LocaleProvider } from "../../lib/locale";
import { V2RecoveryProvider } from "../recovery-storage";
import { V2Composer } from "./composer";

const workspaceID = "workspace-image-recovery";
const file = () => new File(["self-made image fixture"], "blue.png", { type: "image/png" });
async function fixture() {
  const imageFile = file();
  const sha256 = [...new Uint8Array(await crypto.subtle.digest("SHA-256", await imageFile.arrayBuffer()))].map((byte) => byte.toString(16).padStart(2, "0")).join("");
  const image: WorkspaceImageAttachment = { id: "image-original", workspace_id: workspaceID, sha256,
    byte_size: imageFile.size, mime_type: "image/png", width: 64, height: 32, name: imageFile.name };
  const client = { baseURL: "/api/v1", hasThreadControl: true, hasModelControl: true,
    availableModelRoutes: vi.fn(async () => ({ routes: [{ provider_id: "fixture", model: "vision", selectable: true, default_for_routes: ["code"],
      vision_capability: { state: "supported", source: "operator_declared" } }] })),
    uploadWorkspaceImage: vi.fn(async (_workspace: string, _file: File, _key: string): Promise<WorkspaceImageAttachment> => { throw new Error("response lost"); }),
    inspectWorkspaceImageUpload: vi.fn(async (_workspace: string, _key: string): Promise<{ state: "not_received" | "stored"; image?: WorkspaceImageAttachment }> => ({ state: "stored", image })),
    downloadWorkspaceImage: vi.fn(async () => new Blob(["fixture"], { type: "image/png" })),
  };
  const mount = (selected = workspaceID) => {
    const queries = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    const submit = vi.fn(async () => {});
    const view = (workspace: string) => <LocaleProvider><QueryClientProvider client={queries}>
      <V2RecoveryProvider client={client} scopeID="image-upload-recovery">
        <V2Composer client={client as unknown as APIClient} workspaceID={workspace} threadID="" workspaces={[]}
          onWorkspaceChange={() => {}} onSubmit={submit} />
      </V2RecoveryProvider></QueryClientProvider></LocaleProvider>;
    return { ...render(view(selected)), queries, submit, view };
  };
  return { client, image, imageFile, mount };
}
const choose = (image: File) => fireEvent.change(screen.getByLabelText("选择图片或文件"), { target: { files: [image] } });
const input = () => screen.getByRole("textbox", { name: "开始新对话" });
beforeEach(() => {
  localStorage.clear(); localStorage.setItem("prayu.locale.v1", "zh-CN"); localStorage.setItem("universal-code.composer-plain-text", "true");
  vi.stubGlobal("File", NodeFile); vi.stubGlobal("crypto", webcrypto);
  vi.stubGlobal("URL", Object.assign(URL, { createObjectURL: vi.fn(() => "blob:image"), revokeObjectURL: vi.fn() }));
});
afterEach(() => { cleanup(); vi.restoreAllMocks(); vi.unstubAllGlobals(); });

it("recovers the exact original upload on remount without another POST and keeps text edited during lookup", async () => {
  const f = await fixture(); const first = f.mount();
  fireEvent.change(input(), { target: { value: "Original draft" } }); choose(f.imageFile);
  await screen.findByText("response lost");
  const key = f.client.uploadWorkspaceImage.mock.calls[0][2];
  expect(screen.getByRole("button", { name: "发送消息" })).toBeDisabled();
  first.unmount(); first.queries.clear();
  let resolve!: (value: { state: "stored"; image: WorkspaceImageAttachment }) => void;
  f.client.inspectWorkspaceImageUpload.mockImplementationOnce(() => new Promise((accept) => { resolve = accept; }));
  const second = f.mount();
  await waitFor(() => expect(f.client.inspectWorkspaceImageUpload).toHaveBeenCalledWith(workspaceID, key));
  fireEvent.change(input(), { target: { value: "Edited during original lookup" } });
  await act(async () => resolve({ state: "stored", image: f.image }));
  await screen.findByRole("button", { name: "移除图片 blue.png" });
  expect(input()).toHaveValue("Edited during original lookup");
  expect(f.client.uploadWorkspaceImage).toHaveBeenCalledTimes(1);
  second.unmount(); second.queries.clear();
  const third = f.mount();
  await screen.findByRole("button", { name: "移除图片 blue.png" });
  expect(f.client.inspectWorkspaceImageUpload).toHaveBeenCalledTimes(1);
  expect(input()).toHaveValue("Edited during original lookup"); third.queries.clear();
});

it("keeps a mismatched receipt pending until the user leaves it out", async () => {
  const f = await fixture(); const first = f.mount(); choose(f.imageFile);
  await screen.findByText("response lost"); first.unmount(); first.queries.clear();
  f.client.inspectWorkspaceImageUpload.mockResolvedValue({ state: "stored", image: { ...f.image, sha256: "b".repeat(64) } });
  const second = f.mount();
  await screen.findByText("原上传结果与图片不符，未加入草稿。");
  expect(screen.queryByRole("button", { name: "移除图片 blue.png" })).not.toBeInTheDocument();
  fireEvent.change(input(), { target: { value: "Send text only" } });
  expect(screen.getByRole("button", { name: "发送消息" })).toBeDisabled();
  fireEvent.click(screen.getByRole("button", { name: "不加入本条消息" }));
  await waitFor(() => expect(screen.getByRole("button", { name: "发送消息" })).toBeEnabled());
  expect(f.client.uploadWorkspaceImage).toHaveBeenCalledTimes(1); second.queries.clear();
});

it("does not attach a late recovered receipt to the newly selected workspace", async () => {
  const f = await fixture(); const first = f.mount(); choose(f.imageFile);
  await screen.findByText("response lost"); first.unmount(); first.queries.clear();
  let resolve!: (value: { state: "stored"; image: WorkspaceImageAttachment }) => void;
  f.client.inspectWorkspaceImageUpload.mockImplementationOnce(() => new Promise((accept) => { resolve = accept; }));
  const second = f.mount();
  await waitFor(() => expect(f.client.inspectWorkspaceImageUpload).toHaveBeenCalledTimes(1));
  second.rerender(second.view("other-workspace"));
  fireEvent.change(input(), { target: { value: "Other workspace draft" } });
  await act(async () => resolve({ state: "stored", image: f.image }));
  expect(input()).toHaveValue("Other workspace draft");
  expect(screen.queryByRole("button", { name: "移除图片 blue.png" })).not.toBeInTheDocument();
  second.rerender(second.view(workspaceID));
  await screen.findByRole("button", { name: "移除图片 blue.png" });
  expect(input()).toHaveValue(""); second.queries.clear();
});

it("keeps an absent original result pending without polling and retries only its original read", async () => {
  const f = await fixture(); const first = f.mount(); choose(f.imageFile);
  await screen.findByText("response lost"); first.unmount(); first.queries.clear();
  f.client.inspectWorkspaceImageUpload.mockResolvedValueOnce({ state: "not_received" });
  const second = f.mount();
  await screen.findByText(/尚未找到原图片上传结果/u);
  fireEvent.change(input(), { target: { value: "Keep the original upload pending" } });
  expect(screen.getByRole("button", { name: "发送消息" })).toBeDisabled();
  expect(f.client.inspectWorkspaceImageUpload).toHaveBeenCalledTimes(1);
  choose(f.imageFile);
  await screen.findByText("这张图片的原上传结果尚待核对，请先核对原图片上传。");
  expect(f.client.uploadWorkspaceImage).toHaveBeenCalledTimes(1);
  fireEvent.click(screen.getByRole("button", { name: "核对原图片上传" }));
  await screen.findByRole("button", { name: "移除图片 blue.png" });
  expect(f.client.inspectWorkspaceImageUpload).toHaveBeenCalledTimes(2);
  expect(f.client.uploadWorkspaceImage).toHaveBeenCalledTimes(1); second.queries.clear();
});

it("blocks sending when the original upload journal cannot be read", async () => {
  const f = await fixture(); const first = f.mount(); choose(f.imageFile);
  await screen.findByText("response lost"); first.unmount(); first.queries.clear();
  const key = Object.keys(localStorage).find((key) => decodeURIComponent(key).includes("image-upload:"))!;
  const record = JSON.parse(localStorage.getItem(key)!); record.value.sha256 = "malformed";
  localStorage.setItem(key, JSON.stringify(record));
  const second = f.mount();
  await screen.findByText(/原图片上传记录无法读取/u);
  fireEvent.change(input(), { target: { value: "Keep this draft until the original record is available" } });
  expect(screen.getByRole("button", { name: "发送消息" })).toBeDisabled();
  expect(f.client.inspectWorkspaceImageUpload).not.toHaveBeenCalled();
  expect(localStorage.getItem(key)).toBe(JSON.stringify(record)); second.queries.clear();
});
