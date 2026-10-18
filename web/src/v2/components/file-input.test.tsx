import type { ReactElement } from "react";
import type { RenderOptions } from "@testing-library/react";
import { LocaleProvider } from "../../lib/locale";
import { File as NodeFile } from "node:buffer";
import { webcrypto } from "node:crypto";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act, cleanup, fireEvent, render as renderComponent, screen, waitFor } from "@testing-library/react";
import { useState, type PropsWithChildren } from "react";
import type { APIClient } from "../../api/client";
import type { WorkspaceFileAttachment } from "../../api/file-attachments";
import { v2AttachmentReferenceKey } from "../attachment-keys";
import { getV2DraftDocument, v2DraftScope } from "../draft-context";
import { useV2RecoveryStore, V2RecoveryProvider } from "../recovery-storage";
import { V2Composer } from "./composer";
import { useV2FileInput, V2FileAttachments } from "./file-input";

const workspaceID = "workspace-upload-ui";
const fixtureFile = () => new File(["Read the attached requirements.\n"], "需求.txt", { type: "text/plain" });
const digest = async (file: File) => [...new Uint8Array(await crypto.subtle.digest("SHA-256", await file.arrayBuffer()))]
  .map((byte) => byte.toString(16).padStart(2, "0")).join("");
async function metadata(file: File, workspace = workspaceID): Promise<WorkspaceFileAttachment> {
  const sha256 = await digest(file);
  return { id: `file-${sha256.slice(0, 12)}`, workspace_id: workspace, name: file.name, sha256, byte_size: file.size,
    mime_type: file.type || "application/octet-stream", readability: file.type === "text/plain" ? "text" : "stored_only",
    text_bytes: file.type === "text/plain" ? file.size : 0, redacted: false };
}
function fixture() {
  return { baseURL: "/api/v1", hasThreadControl: true, hasModelControl: true,
    availableModelRoutes: vi.fn(async () => ({ routes: [{ provider_id: "fixture", model: "vision", selectable: true, default_for_routes: ["code"],
      vision_capability: { state: "supported", source: "operator_declared" } }] })),
    uploadWorkspaceFile: vi.fn(async (workspace: string, file: File, _operationKey: string) => metadata(file, workspace)),
    inspectWorkspaceFileUpload: vi.fn(), downloadWorkspaceFile: vi.fn(),
    uploadWorkspaceImage: vi.fn(async (workspace: string, file: File) => ({ id: "image-uploaded", workspace_id: workspace,
      name: file.name, mime_type: "image/png" as const, sha256: await digest(file), byte_size: file.size, width: 1, height: 1 })),
    downloadWorkspaceImage: vi.fn(async () => new Blob(["verified image"], { type: "image/png" })),
  };
}
function mount(client: ReturnType<typeof fixture>, persistent = false, selectedWorkspace = workspaceID) {
  const queries = new QueryClient({ defaultOptions: { queries: { retry: false }, mutations: { retry: false } } });
  const submit = vi.fn(async (..._args: unknown[]) => {});
  const wrapper = ({ children }: PropsWithChildren) => <QueryClientProvider client={queries}>
    <V2RecoveryProvider client={client} scopeID={persistent ? "file-input-ui" : undefined}>{children}</V2RecoveryProvider>
  </QueryClientProvider>;
  const component = (workspace: string, unavailableReason?: string) => <V2Composer client={client as unknown as APIClient} workspaceID={workspace}
    threadID="" workspaces={[]} onWorkspaceChange={() => {}} onSubmit={submit} fileReferenceUnavailableReason={unavailableReason} />;
  const page = render(component(selectedWorkspace), { wrapper });
  return { ...page, queries, submit, changeWorkspace: (workspace: string) => page.rerender(component(workspace)),
    changeAvailability: (reason?: string) => page.rerender(component(selectedWorkspace, reason)) };
}
const choose = (file: File) => fireEvent.change(screen.getByLabelText("选择图片或文件"), { target: { files: [file] } });

beforeEach(() => {
  localStorage.clear();
  vi.stubGlobal("File", NodeFile);
  vi.stubGlobal("crypto", webcrypto);
  vi.stubGlobal("URL", Object.assign(URL, { createObjectURL: vi.fn(() => "blob:file-ui"), revokeObjectURL: vi.fn() }));
});
afterEach(() => { cleanup(); vi.restoreAllMocks(); vi.unstubAllGlobals(); });

it("pastes mixed image/file items exactly once and submits a file-only message after removing the image", async () => {
  const client = fixture();
  const page = mount(client);
  const file = fixtureFile();
  const image = new File([new Uint8Array([0x89, 80, 78, 71, 13, 10, 26, 10, 0])], "截图.png", { type: "" });
  const expected = await metadata(file);
  fireEvent.paste(screen.getByRole("textbox", { name: "开始新对话" }), { clipboardData: { files: [], items: [
    { kind: "file", getAsFile: () => image }, { kind: "string", getAsFile: () => null }, { kind: "file", getAsFile: () => file },
  ] } });
  await screen.findByRole("button", { name: "移除文件 需求.txt" });
  expect(client.uploadWorkspaceImage).toHaveBeenCalledTimes(1);
  expect(client.uploadWorkspaceFile).toHaveBeenCalledTimes(1);
  expect(client.uploadWorkspaceFile).toHaveBeenCalledWith(workspaceID, file, expect.stringMatching(/^v2-file-upload-/));
  expect(client.uploadWorkspaceImage.mock.calls[0][1].type).toBe("image/png");
  expect(new Uint8Array(await client.uploadWorkspaceImage.mock.calls[0][1].arrayBuffer())).toEqual(new Uint8Array(await image.arrayBuffer()));
  fireEvent.click(screen.getByRole("button", { name: "移除图片 截图.png" }));
  await waitFor(() => expect(screen.getByRole("button", { name: "发送消息" })).toBeEnabled());
  fireEvent.click(screen.getByRole("button", { name: "发送消息" }));
  expect(page.submit).toHaveBeenCalledWith("", [], [], undefined, [expected]);
  await waitFor(() => expect(screen.queryByRole("button", { name: "移除文件 需求.txt" })).not.toBeInTheDocument());
  page.queries.clear();
});

it("restores an unknown upload after remount, inspects its exact original key without POST, and retains the recovered draft", async () => {
  const client = fixture();
  const file = fixtureFile();
  const expected = await metadata(file);
  client.uploadWorkspaceFile.mockRejectedValueOnce(new Error("upload response lost"));
  const first = mount(client, true);
  choose(file);
  await screen.findByRole("button", { name: "核对原上传" });
  await waitFor(() => expect(screen.getByRole("button", { name: "核对原上传" })).toBeEnabled());
  const key = client.uploadWorkspaceFile.mock.calls[0][2];
  expect(screen.getByRole("button", { name: "发送消息" })).toBeDisabled();
  first.unmount(); first.queries.clear();
  client.inspectWorkspaceFileUpload.mockResolvedValue({ state: "stored", attachment: expected });
  const reopened = mount(client, true);
  expect(client.uploadWorkspaceFile).toHaveBeenCalledTimes(1);
  expect(client.inspectWorkspaceFileUpload).not.toHaveBeenCalled();
  fireEvent.click(await screen.findByRole("button", { name: "核对原上传" }));
  await screen.findByRole("button", { name: "移除文件 需求.txt" });
  expect(client.inspectWorkspaceFileUpload).toHaveBeenCalledWith(workspaceID, key);
  expect(client.uploadWorkspaceFile).toHaveBeenCalledTimes(1);
  expect(screen.queryByRole("button", { name: "核对原上传" })).not.toBeInTheDocument();
  reopened.unmount(); reopened.queries.clear();
  const third = mount(client, true);
  await screen.findByRole("button", { name: "移除文件 需求.txt" });
  expect(screen.getByText(`${file.size} B`)).toBeInTheDocument();
  expect(client.uploadWorkspaceFile).toHaveBeenCalledTimes(1);
  third.queries.clear();
});

it("does not resurrect an upload after its original page closes and the recovered pending item is explicitly dismissed", async () => {
  const client = fixture();
  const file = fixtureFile();
  const expected = await metadata(file);
  let complete!: (attachment: WorkspaceFileAttachment) => void;
  client.uploadWorkspaceFile.mockImplementationOnce(() => new Promise<WorkspaceFileAttachment>((resolve) => { complete = resolve; }));
  const first = mount(client, true);
  choose(file);
  await waitFor(() => expect(client.uploadWorkspaceFile).toHaveBeenCalledTimes(1));
  first.unmount(); first.queries.clear();
  const reopened = mount(client, true);
  fireEvent.click(await screen.findByRole("button", { name: "不加入本条消息" }));
  expect(screen.queryByRole("button", { name: "核对原上传" })).not.toBeInTheDocument();
  await act(async () => complete(expected));
  reopened.unmount(); reopened.queries.clear();
  const third = mount(client, true);
  expect(screen.queryByRole("button", { name: "移除文件 需求.txt" })).not.toBeInTheDocument();
  expect(screen.queryByRole("button", { name: "核对原上传" })).not.toBeInTheDocument();
  expect(client.uploadWorkspaceFile).toHaveBeenCalledTimes(1);
  expect(third.submit).not.toHaveBeenCalled();
  third.queries.clear();
});

it("keeps a late upload in its original workspace while the same composer edits another workspace", async () => {
  const client = fixture();
  const file = fixtureFile();
  const expected = await metadata(file);
  let complete!: (attachment: WorkspaceFileAttachment) => void;
  client.uploadWorkspaceFile.mockImplementationOnce(() => new Promise<WorkspaceFileAttachment>((resolve) => { complete = resolve; }));
  const page = mount(client, true);
  choose(file);
  await waitFor(() => expect(client.uploadWorkspaceFile).toHaveBeenCalledTimes(1));
  page.changeWorkspace("workspace-other");
  fireEvent.change(screen.getByRole("textbox", { name: "开始新对话" }), { target: { value: "另一个项目的新要求" } });
  await act(async () => complete(expected));
  expect(screen.queryByRole("button", { name: "移除文件 需求.txt" })).not.toBeInTheDocument();
  expect(screen.getByRole("textbox", { name: "开始新对话" })).toHaveValue("另一个项目的新要求");
  page.changeWorkspace(workspaceID);
  await screen.findByRole("button", { name: "移除文件 需求.txt" });
  expect(screen.getByRole("textbox", { name: "开始新对话" })).toHaveValue("");
  expect(client.uploadWorkspaceFile).toHaveBeenCalledTimes(1);
  expect(page.submit).not.toHaveBeenCalled();
  page.queries.clear();
});

it("treats an existing draft with no attachments field as empty instead of importing stale query cache files", async () => {
  const client = fixture();
  const expected = await metadata(fixtureFile());
  const queries = new QueryClient();
  queries.setQueryData(v2AttachmentReferenceKey(workspaceID, "thread-old-draft"), [expected]);
  function Probe() {
    const store = useV2RecoveryStore()!;
    useState(() => getV2DraftDocument(store).update(v2DraftScope(workspaceID, "thread-old-draft"), { text: "旧版本草稿", files: [], images: [] }, null));
    return <Selected />;
  }
  function Selected() {
    const selected = useV2FileInput({ client: client as unknown as APIClient, workspaceID, threadID: "thread-old-draft", disabled: false });
    return <V2FileAttachments client={client as unknown as APIClient} attachments={selected.attachments} onRemove={() => {}} />;
  }
  render(<QueryClientProvider client={queries}><V2RecoveryProvider client={client} scopeID="old-draft-files"><Probe /></V2RecoveryProvider></QueryClientProvider>);
  expect(screen.queryByRole("button", { name: "移除文件 需求.txt" })).not.toBeInTheDocument();
  expect(client.uploadWorkspaceFile).not.toHaveBeenCalled();
  queries.clear();
});

it("shows file identity without interpreting receipt status and keeps exact download, removal, and pending controls", async () => {
  const client = fixture();
  const base = await metadata(fixtureFile());
  const text: WorkspaceFileAttachment = { ...base, id: "file-text", name: "需求.txt", byte_size: 0, text_bytes: 0 };
  const partial: WorkspaceFileAttachment = { ...base, id: "file-partial", name: "日志.txt", byte_size: 2048,
    readability: "partial_text", redacted: true };
  const archive: WorkspaceFileAttachment = { ...base, id: "file-archive", name: "项目资料与截图备份归档文件.zip",
    byte_size: 1572864, mime_type: "application/zip", readability: "stored_only", text_bytes: 0,
    reason: "internal extraction receipt" };
  const original = new Blob(["original archive bytes"], { type: "application/octet-stream" });
  let complete!: (blob: Blob) => void;
  client.downloadWorkspaceFile.mockImplementationOnce(() => new Promise<Blob>((resolve) => { complete = resolve; }));
  const saved: { name: string; href: string }[] = [];
  vi.spyOn(HTMLAnchorElement.prototype, "click").mockImplementation(function (this: HTMLAnchorElement) {
    saved.push({ name: this.download, href: this.href });
  });
  const remove = vi.fn();
  render(<V2FileAttachments client={client as unknown as APIClient} attachments={[text, partial, archive]}
    onRemove={remove} pendingIDs={[text.id]} />);
  for (const file of [text, partial, archive]) expect(screen.getByTitle(file.name)).toHaveTextContent(file.name);
  for (const size of ["0 B", "2.0 KiB", "1.5 MiB"]) expect(screen.getByText(size)).toBeInTheDocument();
  expect(screen.queryByText(/解析|文本可读取|模型可见|尚不能读取|internal extraction receipt/)).not.toBeInTheDocument();
  expect(screen.getByText("随原消息提交")).toBeInTheDocument();
  const lockedRemove = screen.getByRole("button", { name: `移除文件 ${text.name}` });
  expect(lockedRemove).toBeDisabled();
  fireEvent.click(lockedRemove);
  expect(remove).not.toHaveBeenCalled();
  const download = screen.getByRole("button", { name: `下载文件 ${archive.name}` });
  fireEvent.click(download);
  expect(download).toBeDisabled();
  expect(client.downloadWorkspaceFile).toHaveBeenCalledExactlyOnceWith(archive);
  await act(async () => complete(original));
  expect(URL.createObjectURL).toHaveBeenCalledWith(original);
  expect(saved).toEqual([{ name: archive.name, href: "blob:file-ui" }]);
  expect(download).toBeEnabled();
  fireEvent.click(screen.getByRole("button", { name: `移除文件 ${archive.name}` }));
  expect(remove).toHaveBeenCalledExactlyOnceWith(archive.id);
});

it("keeps a failed download visible on a message attachment without inventing a parsing result", async () => {
  const client = fixture();
  const file = await metadata(fixtureFile());
  client.downloadWorkspaceFile.mockRejectedValueOnce(new Error("附件下载连接中断"));
  render(<V2FileAttachments client={client as unknown as APIClient} attachments={[file]} />);
  expect(screen.queryByRole("button", { name: `移除文件 ${file.name}` })).not.toBeInTheDocument();
  fireEvent.click(screen.getByRole("button", { name: `下载文件 ${file.name}` }));
  expect(await screen.findByRole("alert")).toHaveTextContent("附件下载连接中断");
  expect(screen.getByTitle(file.name)).toBeInTheDocument();
  expect(screen.getByRole("button", { name: `下载文件 ${file.name}` })).toBeEnabled();
  expect(client.downloadWorkspaceFile).toHaveBeenCalledExactlyOnceWith(file);
  expect(URL.createObjectURL).not.toHaveBeenCalled();
});

it("queues uploaded attachments while project file references are unavailable, then permits the next text-only input", async () => {
  const client = fixture();
  const page = mount(client);
  const file = fixtureFile();
  const expected = await metadata(file);
  choose(file);
  await screen.findByRole("button", { name: `移除文件 ${file.name}` });
  const textbox = screen.getByRole("textbox", { name: "开始新对话" });
  const send = screen.getByRole("button", { name: "发送消息" });
  const busyReason = "当前正在执行，文件操作需等待本轮结束。";
  page.changeAvailability(busyReason);
  fireEvent.change(textbox, { target: { value: "请依据附件补充下一步" } });
  expect(textbox).toBeEnabled();
  expect(textbox).toHaveValue("请依据附件补充下一步");
  expect(screen.getByTitle(file.name)).toBeInTheDocument();
  expect(screen.queryByText(/当前也暂不能提交附件/)).not.toBeInTheDocument();
  expect(send).toBeEnabled();
  fireEvent.click(send);
  expect(page.submit).toHaveBeenCalledExactlyOnceWith("请依据附件补充下一步", [], [], undefined, [expected]);
  await waitFor(() => expect(screen.queryByTitle(file.name)).not.toBeInTheDocument());

  page.changeAvailability(busyReason);
  fireEvent.change(textbox, { target: { value: "补充一句：保留原始文件" } });
  expect(send).toBeEnabled();
  fireEvent.click(send);
  expect(page.submit).toHaveBeenNthCalledWith(2, "补充一句：保留原始文件");
  await waitFor(() => expect(textbox).toHaveValue(""));
  expect(client.uploadWorkspaceFile).toHaveBeenCalledTimes(1);
  page.queries.clear();
});

// Retain textarea coverage through the supported plain-text editing mode.
beforeEach(() => localStorage.setItem("universal-code.composer-plain-text", "true"));

function render(ui: ReactElement, options?: RenderOptions) {
  window.localStorage.setItem("prayu.locale.v1", "zh-CN");
  const Wrapper = options?.wrapper;
  return renderComponent(ui, { ...options, wrapper: ({ children }) => <LocaleProvider>
    {Wrapper ? <Wrapper>{children}</Wrapper> : children}
  </LocaleProvider> });
}
