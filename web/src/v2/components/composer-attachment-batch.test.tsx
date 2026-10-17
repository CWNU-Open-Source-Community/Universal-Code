import { File as NodeFile } from "node:buffer";
import { webcrypto } from "node:crypto";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act, cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import type { APIClient } from "../../api/client";
import type { WorkspaceFileAttachment } from "../../api/file-attachments";
import type { WorkspaceImageAttachment } from "../../api/image-attachments";
import { V2RecoveryProvider } from "../recovery-storage";
import { V2Composer } from "./composer";

const workspaceID = "workspace-batch-paste";
const imageReceipt: WorkspaceImageAttachment = { id: "image-batch", workspace_id: workspaceID, name: "画面.png",
  mime_type: "image/png", sha256: "a".repeat(64), byte_size: 18, width: 64, height: 32 };
async function fileReceipt(file: File): Promise<WorkspaceFileAttachment> {
  const sha256 = [...new Uint8Array(await crypto.subtle.digest("SHA-256", await file.arrayBuffer()))]
    .map((byte) => byte.toString(16).padStart(2, "0")).join("");
  return { id: `file-${sha256.slice(0, 12)}`, workspace_id: workspaceID, name: file.name, mime_type: file.type,
    sha256, byte_size: file.size, readability: "text", text_bytes: file.size, redacted: false };
}
function mount(selectedWorkspaceID = workspaceID) {
  const client = { baseURL: "/api/v1", hasThreadControl: true, hasModelControl: true,
    availableModelRoutes: vi.fn(async () => ({ routes: [{ provider_id: "fixture", model: "vision", selectable: true, default_for_routes: ["code"],
      vision_capability: { state: "supported", source: "operator_declared" } }] })),
    uploadWorkspaceImage: vi.fn(async () => imageReceipt),
    downloadWorkspaceImage: vi.fn(async () => new Blob(["verified fixture"], { type: "image/png" })),
    uploadWorkspaceFile: vi.fn(async (_workspace: string, file: File, _key: string) => fileReceipt(file)),
    inspectWorkspaceFileUpload: vi.fn(),
  };
  const queries = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  const submit = vi.fn(async () => {});
  render(<QueryClientProvider client={queries}><V2RecoveryProvider client={client} scopeID="batch-paste-store">
    <V2Composer client={client as unknown as APIClient} workspaceID={selectedWorkspaceID} threadID="" workspaces={[]}
      onWorkspaceChange={() => {}} onSubmit={submit} />
  </V2RecoveryProvider></QueryClientProvider>);
  return { client, queries, submit };
}
function paste(file: File, text = "") {
  const event = new Event("paste", { bubbles: true, cancelable: true });
  Object.defineProperty(event, "clipboardData", { value: { files: [file], items: [],
    types: text ? ["Files", "text/plain"] : ["Files"], getData: (type: string) => type === "text/plain" ? text : "" } });
  fireEvent(screen.getByRole("textbox", { name: "开始新对话" }), event);
  return event;
}

beforeEach(() => {
  localStorage.clear(); vi.stubGlobal("File", NodeFile); vi.stubGlobal("crypto", webcrypto);
  vi.stubGlobal("URL", Object.assign(URL, { createObjectURL: vi.fn(() => "blob:batch-image"), revokeObjectURL: vi.fn() }));
});
afterEach(() => { cleanup(); vi.restoreAllMocks(); vi.unstubAllGlobals(); });

it("explains an unselected project without uploading or suppressing accompanying pasted text", () => {
  const { client, queries, submit } = mount("");
  const input = screen.getByRole("textbox", { name: "开始新对话" });
  fireEvent.change(input, { target: { value: "保留正文" } });
  const event = paste(new File(["requirements"], "需求.txt", { type: "text/plain" }), "伴随文本");
  expect(event.defaultPrevented).toBe(false);
  expect(screen.getByRole("alert")).toHaveTextContent("先选择项目");
  expect(screen.getByRole("alert")).toHaveTextContent("尚未加入");
  expect(client.uploadWorkspaceFile).not.toHaveBeenCalled();
  expect(client.uploadWorkspaceImage).not.toHaveBeenCalled();
  expect(input).toHaveValue("保留正文");
  expect(submit).not.toHaveBeenCalled(); queries.clear();
});

it("retains one mixed batch through a slow image upload and more than eight subsequent draft revisions", async () => {
  const { client, queries, submit } = mount();
  const input = screen.getByRole("textbox", { name: "开始新对话" });
  fireEvent.change(input, { target: { value: "最初的要求" } });
  const image = new File(["image fixture data"], "画面.png", { type: "image/png" });
  const file = new File(["project requirements"], "需求.txt", { type: "text/plain" });
  let finish!: (image: WorkspaceImageAttachment) => void;
  client.uploadWorkspaceImage.mockImplementationOnce(() => new Promise((resolve) => { finish = resolve; }));
  fireEvent.change(screen.getByLabelText("选择图片或文件"), { target: { files: [image, file] } });
  await waitFor(() => expect(client.uploadWorkspaceImage).toHaveBeenCalledTimes(1));
  expect(client.uploadWorkspaceFile).not.toHaveBeenCalled();
  for (let revision = 1; revision <= 18; revision++) {
    fireEvent.change(input, { target: { value: `等待上传时继续纠正要求 ${revision}` } });
  }
  expect(input).toHaveValue("等待上传时继续纠正要求 18");
  await act(async () => finish(imageReceipt));
  await screen.findByRole("button", { name: "移除文件 需求.txt" });
  expect(screen.getByRole("button", { name: "移除图片 画面.png" })).toBeInTheDocument();
  expect(input).toHaveValue("等待上传时继续纠正要求 18");
  await waitFor(() => expect(screen.getByRole("button", { name: "添加附件" })).toBeEnabled());
  expect(screen.getByRole("button", { name: "发送消息" })).toBeEnabled();
  expect(client.uploadWorkspaceFile).toHaveBeenCalledTimes(1);
  expect(client.uploadWorkspaceFile).toHaveBeenCalledWith(workspaceID, file, expect.stringMatching(/^v2-file-upload-/));
  expect(submit).not.toHaveBeenCalled(); queries.clear();
});

it("leaves native textarea text insertion enabled when the same paste also contains a file", async () => {
  const { client, queries, submit } = mount();
  const input = screen.getByRole("textbox", { name: "开始新对话" });
  fireEvent.change(input, { target: { value: "已有正文" } });
  const file = new File(["requirements"], "需求.txt", { type: "text/plain" });
  const event = paste(file, "同时粘贴的纠正要求");
  expect(event.defaultPrevented).toBe(false);
  await screen.findByRole("button", { name: "移除文件 需求.txt" });
  expect(client.uploadWorkspaceFile).toHaveBeenCalledTimes(1);
  expect(input).toHaveValue("已有正文"); // jsdom does not emulate the browser's native paste edit.
  expect(submit).not.toHaveBeenCalled(); queries.clear();
});

it("keeps both the attachment and formatted text when pasting into the rich editor", async () => {
  localStorage.removeItem("universal-code.composer-plain-text");
  // jsdom has no caret geometry for ProseMirror's scroll-to-selection.
  Object.defineProperties(Range.prototype, {
    getClientRects: { configurable: true, value: () => [] },
    getBoundingClientRect: { configurable: true, value: () => new DOMRect() },
  });
  const { client, queries, submit } = mount();
  const file = new File(["requirements"], "需求.txt", { type: "text/plain" });
  paste(file, "**同时保留的要求**");
  await screen.findByRole("button", { name: "移除文件 需求.txt" });
  expect(client.uploadWorkspaceFile).toHaveBeenCalledTimes(1);
  expect(screen.getByRole("textbox").querySelector("strong")).toHaveTextContent("同时保留的要求");
  fireEvent.click(screen.getByRole("button", { name: "发送消息" }));
  await waitFor(() => expect(submit).toHaveBeenCalledOnce());
  expect(submit.mock.calls[0]).toEqual(expect.arrayContaining([
    "**同时保留的要求**", expect.arrayContaining([expect.objectContaining({ name: "需求.txt" })]),
  ]));
  queries.clear();
});

it("reports a second paste while busy and preserves the first batch plus continued typing", async () => {
  const { client, queries, submit } = mount();
  const first = new File(["first file"], "第一批.txt", { type: "text/plain" });
  const second = new File(["second file"], "第二批.txt", { type: "text/plain" });
  const receipt = await fileReceipt(first);
  let finish!: (file: WorkspaceFileAttachment) => void;
  client.uploadWorkspaceFile.mockImplementationOnce(() => new Promise((resolve) => { finish = resolve; }));
  paste(first);
  await waitFor(() => expect(client.uploadWorkspaceFile).toHaveBeenCalledTimes(1));
  expect(screen.queryByRole("alert")).not.toBeInTheDocument();
  fireEvent.change(screen.getByRole("textbox", { name: "开始新对话" }), { target: { value: "上传期间继续输入" } });
  paste(second);
  expect(await screen.findByRole("alert")).toHaveTextContent(/附件|上传/);
  expect(screen.getByRole("alert")).toHaveTextContent(/稍后|等待|完成|正在|处理中/);
  expect(client.uploadWorkspaceFile).toHaveBeenCalledTimes(1);
  await act(async () => finish(receipt));
  await screen.findByRole("button", { name: "移除文件 第一批.txt" });
  expect(screen.queryByRole("button", { name: "移除文件 第二批.txt" })).not.toBeInTheDocument();
  expect(screen.getByRole("textbox", { name: "开始新对话" })).toHaveValue("上传期间继续输入");
  expect(client.uploadWorkspaceFile).toHaveBeenCalledTimes(1);
  expect(submit).not.toHaveBeenCalled(); queries.clear();
});

// Retain textarea coverage through the supported plain-text editing mode.
beforeEach(() => localStorage.setItem("universal-code.composer-plain-text", "true"));
