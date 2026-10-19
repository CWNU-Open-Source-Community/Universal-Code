import { File as NodeFile } from "node:buffer";
import { webcrypto } from "node:crypto";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act, cleanup, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import type { APIClient } from "../../api/client";
import type { WorkspaceFileAttachment } from "../../api/file-attachments";
import { LocaleProvider } from "../../lib/locale";
import { V2RecoveryProvider } from "../recovery-storage";
import { V2Composer } from "./composer";

const workspace = "text-attachment-project";
const original = `  # 审查报告\n\n${"保留完整内容和中文 UTF-8 字节。\n".repeat(450)}\n  `;
function fixture() {
  const files = new Map<string, File>();
  const client = {
    baseURL: "/api/v1", hasThreadControl: true,
    uploadWorkspaceFile: vi.fn(async (workspaceID: string, file: File, _key: string): Promise<WorkspaceFileAttachment> => {
      const sha256 = [...new Uint8Array(await crypto.subtle.digest("SHA-256", await file.arrayBuffer()))]
        .map((byte) => byte.toString(16).padStart(2, "0")).join("");
      files.set(sha256, file);
      return { id: `file-${sha256.slice(0, 12)}`, workspace_id: workspaceID, name: file.name, sha256,
        mime_type: file.type, byte_size: file.size, readability: "text", text_bytes: file.size, redacted: false };
    }),
    downloadWorkspaceFile: vi.fn(async (file: WorkspaceFileAttachment) => files.get(file.sha256)!),
    inspectWorkspaceFileUpload: vi.fn(),
  };
  return client;
}
function mount(client = fixture(), persistent = true) {
  const queries = new QueryClient({ defaultOptions: { queries: { retry: false }, mutations: { retry: false } } });
  const submit = vi.fn(async (..._args: unknown[]) => {});
  const ui = (workspaceID: string) => <QueryClientProvider client={queries}><LocaleProvider>
    <V2RecoveryProvider client={client} scopeID={persistent ? `ds1_${"d".repeat(64)}` : undefined}>
      <V2Composer client={client as unknown as APIClient} workspaceID={workspaceID} threadID="" workspaces={[]}
        onWorkspaceChange={() => {}} onSubmit={submit} />
    </V2RecoveryProvider>
  </LocaleProvider></QueryClientProvider>;
  const page = render(ui(workspace));
  return { ...page, client, queries, submit, changeWorkspace: (id: string) => page.rerender(ui(id)) };
}
const input = () => screen.getByRole("textbox", { name: "开始新对话" });
const convert = () => fireEvent.click(screen.getByRole("button", { name: "转为文本附件" }));
const preview = () => screen.findByRole("button", { name: "预览文件 粘贴的文本.txt" });

beforeEach(() => {
  localStorage.clear();
  localStorage.setItem("prayu.locale.v1", "zh-CN");
  localStorage.setItem("universal-code.composer-plain-text", "true");
  vi.stubGlobal("File", NodeFile); vi.stubGlobal("crypto", webcrypto);
});
afterEach(() => { cleanup(); vi.restoreAllMocks(); vi.unstubAllGlobals(); });

it("saves the exact full text, survives reopening, previews it and restores it beside new instructions", async () => {
  const first = mount();
  fireEvent.change(input(), { target: { value: original } });
  expect(screen.getByRole("button", { name: "发送消息" })).toBeDisabled();
  convert();
  await preview();
  await waitFor(() => expect(input()).toHaveValue(""));
  expect(await first.client.uploadWorkspaceFile.mock.calls[0][1].text()).toBe(original);
  expect(screen.getByRole("button", { name: "发送消息" })).toBeEnabled();
  first.unmount(); first.queries.clear();
  const reopened = mount(first.client);
  fireEvent.change(input(), { target: { value: "请重点检查登录流程" } });
  fireEvent.click(await preview());
  const dialog = await screen.findByRole("dialog");
  await waitFor(() => expect(dialog.querySelector("pre")?.textContent).toBe(original));
  fireEvent.click(within(dialog).getByRole("button", { name: "放回正文" }));
  expect(input()).toHaveValue(`请重点检查登录流程\n\n${original}`);
  expect(screen.queryByRole("button", { name: /^移除文件/ })).not.toBeInTheDocument();
  expect(screen.getByRole("button", { name: "发送消息" })).toBeDisabled();
  reopened.queries.clear();
});

it("allows a converted attachment to be sent with a separate short instruction", async () => {
  const page = mount();
  fireEvent.change(input(), { target: { value: original } });
  convert(); await preview();
  await waitFor(() => expect(input()).toHaveValue(""));
  fireEvent.change(input(), { target: { value: "按附件中的报告修复问题" } });
  fireEvent.click(screen.getByRole("button", { name: "发送消息" }));
  await waitFor(() => expect(page.submit).toHaveBeenCalledOnce());
  expect(page.submit.mock.calls[0][0]).toBe("按附件中的报告修复问题");
  expect(page.submit.mock.calls[0][4]).toEqual([expect.objectContaining({ byte_size: new TextEncoder().encode(original).byteLength })]);
  page.queries.clear();
});

it("keeps the body when the upload result is unknown", async () => {
  const page = mount();
  page.client.uploadWorkspaceFile.mockRejectedValueOnce(new Error("网络连接中断"));
  fireEvent.change(input(), { target: { value: original } });
  convert();
  await screen.findByText(/网络连接中断/);
  expect(input()).toHaveValue(original);
  expect(screen.getByRole("button", { name: "核对原上传" })).toBeInTheDocument();
  expect(screen.getByRole("button", { name: "转为文本附件" })).toBeDisabled();
  page.queries.clear();
});

it("retains original text when saving the uploaded attachment to the draft fails", async () => {
  const page = mount();
  fireEvent.change(input(), { target: { value: original } });
  const write = Storage.prototype.setItem;
  vi.spyOn(Storage.prototype, "setItem").mockImplementation(function (this: Storage, key, value) {
    const record = JSON.parse(value);
    if (record.value?.snapshot?.attachments?.length) throw new DOMException("quota", "QuotaExceededError");
    return write.call(this, key, value);
  });
  convert();
  await screen.findByText(/粘贴的文本\.txt.*无法保存本机恢复数据/);
  expect(input()).toHaveValue(original);
  expect(screen.getByRole("button", { name: "发送消息" })).toBeDisabled();
  page.queries.clear();
});

it("keeps edits during upload and attaches to the original project after switching projects", async () => {
  const page = mount();
  const upload = page.client.uploadWorkspaceFile.getMockImplementation()!;
  let finish!: () => void;
  page.client.uploadWorkspaceFile.mockImplementationOnce(async (...args) => {
    await new Promise<void>((resolve) => { finish = resolve; });
    return upload(...args);
  });
  fireEvent.change(input(), { target: { value: original } });
  convert();
  await waitFor(() => expect(page.client.uploadWorkspaceFile).toHaveBeenCalledOnce());
  fireEvent.change(input(), { target: { value: "上传期间新的指示" } });
  page.changeWorkspace("another-project");
  fireEvent.change(input(), { target: { value: "另一个项目的草稿" } });
  await act(async () => finish());
  expect(input()).toHaveValue("另一个项目的草稿");
  expect(screen.queryByRole("button", { name: /^预览文件/ })).not.toBeInTheDocument();
  page.changeWorkspace(workspace);
  await preview();
  expect(input()).toHaveValue("上传期间新的指示");
  page.queries.clear();
});

it("converts the same Markdown source without changing formatting when rich editing is enabled", async () => {
  const page = mount();
  fireEvent.change(input(), { target: { value: original } });
  fireEvent.click(screen.getByRole("button", { name: "切换为 Markdown 编辑" }));
  expect(input().querySelector("h1")).toHaveTextContent("审查报告");
  convert(); await preview();
  await waitFor(() => expect(input().textContent).toBe(""));
  expect(await page.client.uploadWorkspaceFile.mock.calls[0][1].text()).toBe(original);
  page.queries.clear();
});
