import type { ReactElement } from "react";
import type { RenderOptions } from "@testing-library/react";
import { LocaleProvider } from "../../lib/locale";
import { act, fireEvent, render as renderComponent, renderHook, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import type { PropsWithChildren } from "react";
import { APIRequestError, type APIClient } from "../../api/client";
import { V2Composer } from "./composer";
import { useV2ThreadTurn, useV2ThreadSubmissions } from "../use-thread-turn";
import { v2FileReferenceKey, withV2FileReference } from "./file-context";
import type { WorkspaceExplorerView } from "../../api/types";

const digest = "a".repeat(64);
it("shares the four-file limit and same-path replacement across reference entry points", () => {
  const files = Array.from({ length: 4 }, (_, index) => ({ id: `ref-${index}`, path: `${index}.md`, digest,
    partial: false, redacted: false }));
  const incoming = { path: "new.md", provenance: { content_sha256: "b".repeat(64) }, truncated: true, redaction_count: 1 } as WorkspaceExplorerView;
  expect(() => withV2FileReference(files, incoming, "最多四个")).toThrow("最多四个");
  expect(files).toHaveLength(4);
  const replaced = withV2FileReference(files, { ...incoming, path: "2.md" }, "最多四个");
  expect(replaced).toHaveLength(4);
  expect(replaced.at(-1)).toMatchObject({ path: "2.md", digest: "b".repeat(64), partial: true, redacted: true });
  expect(replaced.at(-1)?.id).not.toBe("ref-2");
});

function setup() {
  const workspaceExplore = vi.fn(async (_workspace: string, path: string) => ({
    protocol_version: "workspace_explorer.v1", workspace_id: "workspace-1", path,
    kind: path === "." ? "directory" : "file", content: path === "." ? "" : "file context",
    entries: path === "." ? ["README.md", "notes.md"].map((name) => ({ name, path: name,
      kind: "file", size_bytes: 12, readable: true })) : [], redaction_count: 0, truncated: false,
    returned_bytes: 12, total_bytes: 12,
    provenance: { source_kind: "workspace_file", source_ref: path, content_sha256: digest,
      instruction_authorized: false },
  }));
  const client = { hasEvidenceAttachment: true, workspaceExplore,
    attachEvidence: vi.fn().mockResolvedValue({ replayed: false }),
    submitThreadTurn: vi.fn().mockResolvedValue({}),
  } as unknown as APIClient;
  const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false }, mutations: { retry: false } } });
  const wrapper = ({ children }: PropsWithChildren) => <QueryClientProvider client={queryClient}>{children}</QueryClientProvider>;
  return { client, queryClient, wrapper };
}

it("selects files before a Thread exists and keeps each project's references separate", async () => {
  const { client, wrapper } = setup();
  const user = userEvent.setup();
  const composer = (workspaceID: string) => <V2Composer client={client} threadID=""
    workspaceID={workspaceID} workspaces={[]} onSubmit={async () => undefined} onWorkspaceChange={() => undefined} />;
  const view = render(composer("workspace-1"), { wrapper });
  await user.click(screen.getByRole("button", { name: "添加附件" }));
  await user.click(screen.getByRole("menuitem", { name: "引用项目文件" }));
  await user.click(await screen.findByRole("button", { name: /README.md/ }));
  await user.click(await screen.findByRole("button", { name: "引用此文件" }));
  expect(client.workspaceExplore).toHaveBeenCalledWith("workspace-1", "README.md", expect.any(AbortSignal));
  view.rerender(composer("workspace-2"));
  expect(screen.queryByLabelText("待发送的文件引用")).not.toBeInTheDocument();
  view.rerender(composer("workspace-1"));
  expect(screen.getByRole("button", { name: "移除引用 README.md" })).toBeEnabled();
  expect(client.attachEvidence).not.toHaveBeenCalled();
  expect(client.submitThreadTurn).not.toHaveBeenCalled();
});

it("selects a file with its digest, restores the selection across views and keeps late selections", async () => {
  const user = userEvent.setup();
  const { client, wrapper } = setup();
  let finish!: () => void;
  const submit = vi.fn(() => new Promise<void>((resolve) => { finish = resolve; }));
  const component = <V2Composer client={client} threadID="thread-1" runID="run-1"
    workspaceID="workspace-1" workspaces={[]} onSubmit={submit} onWorkspaceChange={() => undefined} />;
  const view = render(component, { wrapper });
  const choose = async (name: string) => {
    await user.click(screen.getByRole("button", { name: "添加附件" }));
    await user.click(screen.getByRole("menuitem", { name: "引用项目文件" }));
    await user.click(await screen.findByRole("button", { name: new RegExp(name) }));
    await user.click(await screen.findByRole("button", { name: "引用此文件" }));
  };
  await choose("README.md");
  expect(screen.getByRole("button", { name: "移除引用 README.md" })).toBeEnabled();
  view.rerender(<p>设置</p>);
  view.rerender(component);
  expect(screen.getByRole("button", { name: "移除引用 README.md" })).toBeInTheDocument();
  fireEvent.change(screen.getByRole("textbox", { name: "继续对话" }), { target: { value: "按说明修改" } });
  await user.click(screen.getByRole("button", { name: "发送消息" }));
  expect(submit).toHaveBeenCalledWith("按说明修改", [expect.objectContaining({ path: "README.md", digest })]);
  expect(screen.getByRole("button", { name: "移除引用 README.md" })).toBeDisabled();
  await choose("notes.md");
  fireEvent.change(screen.getByRole("textbox", { name: "继续对话" }), { target: { value: "下一条草稿" } });
  await act(async () => finish());
  expect(screen.queryByRole("button", { name: "移除引用 README.md" })).not.toBeInTheDocument();
  expect(screen.getByRole("button", { name: "移除引用 notes.md" })).toBeEnabled();
  expect(screen.getByRole("textbox", { name: "继续对话" })).toHaveValue("下一条草稿");
  await user.click(screen.getByRole("button", { name: "移除引用 notes.md" }));
  expect(screen.queryByLabelText("待发送的文件引用")).not.toBeInTheDocument();
});

it("submits references in one turn request and retries the same intent without separately attaching or queueing", async () => {
  const { client, wrapper } = setup();
  const submit = vi.mocked(client.submitThreadTurn).mockRejectedValueOnce(new Error("response lost"));
  const { result } = renderHook(() => useV2ThreadTurn(client), { wrapper });
  const input = { threadID: "thread-1", workspaceID: "workspace-1", operationKey: "turn-1", content: "检查文件", createdAt: new Date().toISOString(),
    files: [{ id: "reference-1", path: "README.md", digest, partial: false, redacted: false }] };
  await act(async () => { await result.current.mutateAsync(input); });
  expect(submit).toHaveBeenCalledTimes(2);
  expect(submit.mock.calls[0]).toEqual(submit.mock.calls[1]);
  expect(client.attachEvidence).not.toHaveBeenCalled();
  await waitFor(() => expect(client.submitThreadTurn).toHaveBeenCalledWith("thread-1",
    { version: "thread_message_submission.v1", content: "检查文件",
      files: [{ source_kind: "workspace_file", path: "README.md", expected_sha256: digest }] }, "turn-1"));
});

it("keeps a selected file and draft recoverable when the old execution can no longer accept evidence", async () => {
  const user = userEvent.setup();
  const { client, wrapper } = setup();
  const submit = vi.fn(async () => undefined);
  const component = (unavailableReason?: string) => <V2Composer client={client} threadID="thread-1" runID="run-1"
    workspaceID="workspace-1" workspaces={[]} onSubmit={submit} onWorkspaceChange={() => undefined}
    fileReferenceUnavailableReason={unavailableReason} />;
  const view = render(component(), { wrapper });
  await user.click(screen.getByRole("button", { name: "添加附件" }));
  await user.click(screen.getByRole("menuitem", { name: "引用项目文件" }));
  await user.click(await screen.findByRole("button", { name: /README.md/ }));
  await user.click(await screen.findByRole("button", { name: "引用此文件" }));
  fireEvent.change(screen.getByRole("textbox", { name: "继续对话" }), { target: { value: "继续修改这个项目" } });
  // A lifecycle update can arrive while the picker is open or with files already selected.
  await user.click(screen.getByRole("button", { name: "添加附件" }));
  await user.click(screen.getByRole("menuitem", { name: "引用项目文件" }));
  view.rerender(component("当前正在执行，文件引用需等执行结束后发送；不带引用的补充消息仍可排队。"));
  expect(screen.queryByRole("dialog", { name: "选择项目文件" })).not.toBeInTheDocument();
  await user.click(screen.getByRole("button", { name: "添加附件" }));
  expect(screen.getByRole("menuitem", { name: "引用项目文件" })).toBeDisabled();
  await user.keyboard("{Escape}");
  expect(screen.getByRole("button", { name: "移除引用 README.md" })).toBeEnabled();
  expect(screen.getByRole("textbox", { name: "继续对话" })).toHaveValue("继续修改这个项目");
  expect(screen.getByRole("button", { name: "发送消息" })).toBeDisabled();
  expect(screen.getByRole("status")).toHaveTextContent("先移除项目文件引用");
  expect(submit).not.toHaveBeenCalled();
  await user.click(screen.getByRole("button", { name: "移除引用 README.md" }));
  await user.click(screen.getByRole("button", { name: "发送消息" }));
  expect(submit).toHaveBeenCalledWith("继续修改这个项目");
  view.rerender(component());
  await user.click(screen.getByRole("button", { name: "添加附件" }));
  expect(screen.getByRole("menuitem", { name: "引用项目文件" })).toBeEnabled();
});

it("retains failed references and clears only the accepted snapshot after a corrected submission", async () => {
  const { client, wrapper, queryClient } = setup();
  vi.mocked(client.submitThreadTurn).mockRejectedValueOnce(new APIRequestError("file changed", "CONFLICT", 409, "rejected", false));
  const { result } = renderHook(() => ({ turn: useV2ThreadTurn(client), submissions: useV2ThreadSubmissions("thread-1") }), { wrapper });
  const input = { threadID: "thread-1", workspaceID: "workspace-1", operationKey: "failed-reference", content: "检查文件", createdAt: new Date().toISOString(),
    files: [{ id: "reference-1", path: "README.md", digest, partial: false, redacted: false }] };
  const key = v2FileReferenceKey("workspace-1", "thread-1");
  queryClient.setQueryData(key, input.files);
  await act(async () => { await expect(result.current.turn.mutateAsync(input)).rejects.toThrow("file changed"); });
  expect(queryClient.getQueryData(key)).toEqual(input.files);
  await waitFor(() => expect(result.current.submissions).toHaveLength(1));
  const corrected = { ...input.files[0]!, id: "reference-2", digest: "b".repeat(64) };
  const late = { ...corrected, id: "reference-3", path: "notes.md" };
  queryClient.setQueryData(key, [corrected, late]);
  await act(async () => { await result.current.turn.mutateAsync({ ...input, operationKey: "corrected-reference",
    replacesOperationKey: input.operationKey, files: [corrected] }); });
  expect(queryClient.getQueryData(key)).toEqual([late]);
  // Only the explicit correction of a known rejection clears its old notice.
  await waitFor(() => expect(result.current.submissions).toHaveLength(0));
  expect(client.attachEvidence).not.toHaveBeenCalled();
});

it("retains an unknown first-turn identity beyond the default cache lifetime and removes it after confirmation", async () => {
  vi.useFakeTimers();
  const { client, queryClient, wrapper } = setup();
  try {
    vi.mocked(client.submitThreadTurn).mockRejectedValueOnce(new Error("response lost"))
      .mockRejectedValueOnce(new Error("response lost"));
    const input = { threadID: "new-thread", workspaceID: "workspace-1", operationKey: "first-turn-original",
      content: "首条文件消息", createdAt: new Date().toISOString(),
      files: [{ id: "first-file", path: "README.md", digest, partial: false, redacted: false }] };
    const first = renderHook(() => useV2ThreadTurn(client), { wrapper });
    await act(async () => {
      const rejected = expect(first.result.current.mutateAsync(input)).rejects.toThrow("response lost");
      await vi.advanceTimersByTimeAsync(401);
      await rejected;
    });
    first.unmount();
    await act(async () => { await vi.advanceTimersByTimeAsync(6 * 60 * 1000); });
    const restored = renderHook(() => ({ turn: useV2ThreadTurn(client), submissions: useV2ThreadSubmissions("new-thread") }), { wrapper });
    expect(restored.result.current.submissions).toHaveLength(1);
    const recovered = restored.result.current.submissions[0]!.input;
    expect(recovered.operationKey).toBe(input.operationKey);
    expect(recovered.files).toEqual(input.files);
    await act(async () => { await restored.result.current.turn.mutateAsync(recovered); });
    expect(vi.mocked(client.submitThreadTurn)).toHaveBeenCalledTimes(3);
    expect(vi.mocked(client.submitThreadTurn).mock.calls[2]).toEqual(vi.mocked(client.submitThreadTurn).mock.calls[0]);
    expect(queryClient.getMutationCache().findAll({ mutationKey: ["v2", "submit-turn"] })).toHaveLength(0);
    restored.unmount();
  } finally {
    queryClient.clear();
    vi.useRealTimers();
  }
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
