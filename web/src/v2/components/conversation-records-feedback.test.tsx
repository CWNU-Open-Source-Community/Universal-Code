import type { ReactElement } from "react";
import type { RenderOptions } from "@testing-library/react";
import { LocaleProvider } from "../../lib/locale";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act, fireEvent, render as renderComponent, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { useState } from "react";
import type { APIClient } from "../../api/client";
import type { ThreadReview } from "../../api/task-delivery";
import type { ThreadDetailView, ThreadTranscriptItemView, WorkspaceView } from "../../api/types";
import { FileEditDraftProvider, useFileEditLeaveEntry } from "../../components/file-edit-drafts";
import { getThreadWorkspaceState } from "../workspace-tabs";
import { V2Conversation } from "./conversation";

vi.mock("../../hooks/use-run-event-stream", () => ({ useRunEventStream: () => ({ error: null, frames: [] }) }));
vi.mock("../../hooks/use-public-model-stream", () => ({ usePublicModelStream: () => ({ error: null, snapshot: null, status: "waiting" }) }));
vi.mock("./permission-control", () => ({ V2PermissionControl: () => null }));
vi.mock("./run-network-authority-control", () => ({ V2RunNetworkAuthorityControl: () => null }));
vi.mock("./model-route-control", () => ({ V2ModelRouteControl: () => null }));
vi.mock("../../lib/desktop-bridge", async (original) => ({
  ...await original<typeof import("../../lib/desktop-bridge")>(),
  desktopUserTerminalEnabled: () => true,
}));
vi.mock("../../components/user-terminal-panel", () => ({
  UserTerminalPanel: ({ onQuote, onReturnToDraft }: { onQuote?: (text: string) => string; onReturnToDraft?: () => void }) => <div>
    <button onClick={() => onQuote?.("来源执行：source-run\n来源终端：terminal-source\n```text\n  failed build\n```")}>引用选中日志</button>
    <button onClick={onReturnToDraft}>返回聊天草稿</button>
  </div>,
}));

const originalDraft = "  保留我的原草稿。\n";
const when = "2026-09-11T00:00:00Z";
const workspaces = [{ id: "workspace-source", name: "Project" }] as WorkspaceView[];
function thread(threadID: string): ThreadDetailView {
  const run = { id: `run-${threadID}`, session_id: `session-${threadID}`, status: "completed" };
  return { thread: { id: threadID, title: `Task ${threadID}`, workspace_id: "workspace-source", status: "active", composer_state: "ready" },
    last_run: run, runs: [{ ordinal: 1, run }], mission: {} } as unknown as ThreadDetailView;
}
function review(threadID: string): ThreadReview {
  return { thread_id: threadID, thread_version: 1, current_run_id: `run-${threadID}`, observed_at: when,
    change_scope: "recorded_file_edits", total_runs: 1,
    runs: [{ run_id: `run-${threadID}`, session_id: `session-${threadID}`, ordinal: 1, source_event_sequence: 1, handoff_url: `/runs/run-${threadID}/code-handoff` }],
    target: { state: "available", source_workspace_id: "workspace-source", workspace_id: "workspace-source", kind: "source", root_path: "D:/fixture/project" },
    revision: { state: "available", repository_kind: "git", head: "d".repeat(40), branch: "feature", dirty: true, revision_sha256: "e".repeat(64), reasons: [] },
    applied_changes: [{ run_id: `run-${threadID}`, session_id: `session-${threadID}`, workspace_id: "workspace-source", edit_id: "edit-original",
      operation: "replace", path: "src/review.ts", status: "applied", original_sha256: "a".repeat(64), proposed_sha256: "b".repeat(64),
      current_sha256: "b".repeat(64), current_match: "matches", diff: "--- a/src/review.ts\n+++ b/src/review.ts\n@@ -1 +1 @@\n-old\n+new",
      diff_truncated: false, redacted: false, updated_at: when }], unapplied_changes: [], checks: [], partial: false, reasons: [] };
}
function setup(recordsRequested = false, dirtyFile = false) {
  const submit = vi.fn(); const post = vi.fn(); const lifecycle = vi.fn();
  const openPreview = vi.fn(); const onOpenTool = vi.fn();
  const client = { hasThreadControl: true, hasFullCDPSessionControl: true,
    get: vi.fn((path: string) => {
      const id = path.split("/")[2];
      if (path.endsWith("/review")) return Promise.resolve(review(id));
      if (path.startsWith("/threads/")) return Promise.resolve(thread(id));
      if (path.startsWith("/runs/")) return Promise.resolve({ run: { id, status: "completed" } });
      return Promise.reject(new Error(`Unexpected read: ${path}`));
    }),
    getPage: vi.fn().mockResolvedValue({ items: [
      { version: "thread_transcript.v1", id: "saved-reply", canonical_id: "saved-reply", run_id: "run-thread-a", run_ordinal: 1,
        sequence: 1, source: "model", kind: "model_update", stage: "result", activity_type: "message", title: "Assistant reply",
        detail: "Saved assistant reply", durable: true, provisional: false, created_at: when, verifiable: true, instruction_authorized: false },
      { version: "thread_transcript.v1", id: "saved-tool", canonical_id: "saved-tool", run_id: "run-thread-a", run_ordinal: 1,
        sequence: 2, source: "harness", kind: "tool_call", stage: "result", activity_type: "read", title: "Read file", tool_name: "workspace_read",
        detail: "Saved tool result", durable: true, provisional: false, created_at: when, verifiable: true, instruction_authorized: false },
    ] as ThreadTranscriptItemView[], page: { limit: 100 }, requestID: "fixture" }),
    getFullCDPSession: vi.fn().mockResolvedValue({ session: { state: "closed" } }),
    submitThreadTurn: submit, postControl: post, controlRunLifecycle: lifecycle, openFullCDPSession: openPreview,
  } as unknown as APIClient;
  const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false, staleTime: Infinity }, mutations: { retry: false } } });
  function DirtyFile() {
    useFileEditLeaveEntry({ path: "README.md", dirty: dirtyFile, busy: false, canSave: false,
      save: async () => undefined, keep: vi.fn(), discard: vi.fn() });
    return null;
  }
  function Harness() {
    const [threadID, setThreadID] = useState("thread-a");
    const [drafts, setDrafts] = useState<Record<string, string>>({ "thread-a": originalDraft, "thread-b": "另一个任务的草稿" });
    return <QueryClientProvider client={queryClient}><FileEditDraftProvider><DirtyFile />
      <button type="button" onClick={() => setThreadID("thread-b")}>切到另一个任务</button>
      <V2Conversation client={client} threadID={threadID} workspaces={workspaces} recordsRequested={recordsRequested}
        onArchive={vi.fn()} onManageModels={vi.fn()}
        onOpenTool={onOpenTool}
        draft={drafts[threadID]} onDraftChange={(next) => setDrafts((current) => ({ ...current, [threadID]: next }))} />
    </FileEditDraftProvider></QueryClientProvider>;
  }
  return { ...render(<Harness />), queryClient, client, onOpenTool,
    expectNoWrites: () => { expect(submit).not.toHaveBeenCalled(); expect(post).not.toHaveBeenCalled();
      expect(lifecycle).not.toHaveBeenCalled(); expect(openPreview).not.toHaveBeenCalled(); } };
}

it("appends exact review context and focuses the actual task composer without sending", async () => {
  const { container, expectNoWrites } = setup();
  const user = userEvent.setup();
  await screen.findByText("Task thread-a");
  const editor = container.querySelector<HTMLTextAreaElement>(".v2-shared-composer textarea")!;
  expect(editor).toHaveValue(originalDraft);
  await user.click(screen.getByRole("button", { name: "审阅改动" }));
  const dialog = await screen.findByRole("dialog", { name: "审阅任务改动" }, { timeout: 5_000 });
  await user.click(await within(dialog).findByRole("button", { name: "引用文件" }, { timeout: 5_000 }));
  await waitFor(() => expect(editor).toHaveFocus());
  expect(editor).toBeVisible();
  expect(screen.queryByRole("dialog", { name: "审阅任务改动" })).not.toBeInTheDocument();
  expect(editor.value).toMatch(new RegExp(`^${originalDraft}\\n\\n`));
  for (const context of ["文件：src/review.ts", "来源执行：run-thread-a", "编辑记录：edit-original", "任务：thread-a", "b".repeat(64)]) {
    expect(editor.value).toContain(context);
  }
  expectNoWrites();
});

it("collects two inline comments in the task draft while keeping review open, then returns without sending", async () => {
  const { container, expectNoWrites } = setup();
  const user = userEvent.setup();
  await user.click(await screen.findByRole("button", { name: "审阅改动" }));
  const dialog = await screen.findByRole("dialog", { name: "审阅任务改动" });
  await user.click(await within(dialog).findByText("查看这次编辑的差异"));
  await user.click(within(dialog).getByRole("button", { name: "评论旧第 1 行" }));
  const composing = within(dialog).getByRole("textbox");
  fireEvent.change(composing, { target: { value: "正在输入的中文意见" } });
  fireEvent.keyDown(composing, { key: "Escape", isComposing: true });
  expect(dialog).toBeVisible();
  expect(composing).toHaveValue("正在输入的中文意见");
  await user.keyboard("{Escape}");
  expect(dialog).toBeVisible();
  expect(within(dialog).queryByRole("textbox")).not.toBeInTheDocument();
  for (const [side, message] of [["旧", "保留旧逻辑的边界检查"], ["新", "增加空输入处理"]]) {
    await user.click(within(dialog).getByRole("button", { name: `评论${side}第 1 行` }));
    await user.type(within(dialog).getByRole("textbox"), message);
    await user.keyboard("{Control>}{Enter}{/Control}");
    expect(dialog).toBeVisible();
    expect(within(dialog).getByRole("status")).toHaveTextContent("尚未发送");
  }
  const editor = container.querySelector<HTMLTextAreaElement>(".v2-shared-composer textarea")!;
  expect(editor.value.startsWith(`${originalDraft}\n\n`)).toBe(true);
  for (const part of ["保留旧逻辑的边界检查", "增加空输入处理", "任务：thread-a", "编辑记录：edit-original", "b".repeat(64)]) expect(editor.value).toContain(part);
  expect(editor.value.match(/审阅意见：/gu)).toHaveLength(2);
  expectNoWrites();
  await user.click(within(dialog).getByRole("button", { name: "返回聊天草稿" }));
  await waitFor(() => expect(editor).toHaveFocus());
  expect(editor).toBeVisible();
  expect(dialog).not.toBeInTheDocument();
  await user.click(screen.getByRole("button", { name: "切到另一个任务" }));
  await screen.findByText("Task thread-b");
  expect(container.querySelector(".v2-shared-composer textarea")).toHaveValue("另一个任务的草稿");
  expectNoWrites();
});

it("quotes the viewed worktree's source into the task draft and returns without sending", async () => {
  const { container, client, expectNoWrites } = setup();
  client.fileEditChangeSet = vi.fn().mockResolvedValue({ workspace_id: "isolated-worktree" });
  client.workspaceExplore = vi.fn().mockResolvedValue({ protocol_version: "workspace_explorer.v1", workspace_id: "isolated-worktree",
    path: "src/example.ts", kind: "file", entries: [], content: "first line\n  selected source\nlast line", returned_bytes: 41,
    total_bytes: 41, truncated: false, redaction_count: 0, root_path_exposed: false,
    provenance: { version: "context_provenance.v1", source_kind: "workspace_file", source_ref: "src/example.ts",
      content_sha256: "c".repeat(64), instruction_authorized: false } });
  const user = userEvent.setup();
  await user.click(await screen.findByRole("button", { name: "工作区文件" }));
  const dialog = await screen.findByRole("dialog", { name: "工作区文件" });
  await within(dialog).findByText("selected source");
  for (const line of [2, 3]) {
    act(() => {
      const range = document.createRange();
      range.selectNodeContents(dialog.querySelector(`[data-line="${line}"] .explorer-line-content`)!.firstChild!);
      document.getSelection()!.removeAllRanges();
      document.getSelection()!.addRange(range);
      document.dispatchEvent(new Event("selectionchange"));
    });
    await user.click(within(dialog).getByRole("button", { name: "引用选中行" }));
    expect(dialog).toBeVisible();
    expect(within(within(dialog).getByRole("region", { name: "引用文件片段" })).getByRole("status")).toHaveTextContent("尚未发送");
  }
  const editor = container.querySelector<HTMLTextAreaElement>(".v2-shared-composer textarea")!;
  expect(editor.value.startsWith(`${originalDraft}\n\n`)).toBe(true);
  for (const part of ["任务：thread-a", "来源工作区：isolated-worktree", "来源执行：run-thread-a", "c".repeat(64),
    "预览第 2–2 行", "预览第 3–3 行", "  selected source", "last line"]) expect(editor.value).toContain(part);
  expect(editor.value).not.toContain("workspace-source");
  expect(client.workspaceExplore).toHaveBeenCalledWith("isolated-worktree", ".", expect.any(AbortSignal));
  await user.click(within(dialog).getByRole("button", { name: "返回聊天草稿" }));
  await waitFor(() => expect(editor).toHaveFocus());
  expect(editor).toBeVisible();
  expect(dialog).not.toBeInTheDocument();
  expectNoWrites();
});

it("collects terminal excerpts in the task draft, then returns without sending or changing the next task's draft", async () => {
  const { container, expectNoWrites } = setup();
  const user = userEvent.setup();
  await user.click(await screen.findByRole("button", { name: "终端" }));
  const dialog = await screen.findByRole("dialog", { name: "任务终端" });
  for (let count = 0; count < 2; count++) await user.click(within(dialog).getByRole("button", { name: "引用选中日志" }));
  const editor = container.querySelector<HTMLTextAreaElement>(".v2-shared-composer textarea")!;
  expect(dialog).toBeVisible();
  expect(editor.value.startsWith(`${originalDraft}\n\n`)).toBe(true);
  expect(editor.value.match(/来源终端：terminal-source/gu)).toHaveLength(2);
  expect(editor.value).toContain("任务：thread-a\n来源执行：source-run");
  expect(editor.value).toContain("\n  failed build\n");
  await user.click(within(dialog).getByRole("button", { name: "返回聊天草稿" }));
  await waitFor(() => expect(editor).toHaveFocus());
  expect(editor).toBeVisible();
  expect(dialog).not.toBeInTheDocument();
  await user.click(screen.getByRole("button", { name: "切到另一个任务" }));
  await screen.findByText("Task thread-b");
  expect(container.querySelector(".v2-shared-composer textarea")).toHaveValue("另一个任务的草稿");
  expectNoWrites();
});

it("reveals the task draft when requesting an application startup, without starting or sending", async () => {
  const { container, client, expectNoWrites } = setup();
  const user = userEvent.setup();
  await user.click(await screen.findByRole("button", { name: "应用预览" }));
  const dialog = await screen.findByRole("dialog", { name: "应用预览" });
  await waitFor(() => expect(client.getFullCDPSession).toHaveBeenCalledWith("run-thread-a", expect.any(AbortSignal)));
  await user.click(within(dialog).getByRole("button", { name: "把启动要求放回草稿" }));
  const editor = container.querySelector<HTMLTextAreaElement>(".v2-shared-composer textarea")!;
  await waitFor(() => expect(editor).toHaveFocus());
  expect(editor).toBeVisible();
  expect(editor.value.startsWith(`${originalDraft}\n\n请检查当前项目的启动方式`)).toBe(true);
  expect(editor.value).toContain("如果启动失败，请报告实际错误。");
  expect(screen.queryByRole("dialog", { name: "应用预览" })).not.toBeInTheDocument();
  expectNoWrites();
});

it("keeps chat and draft visible beside records and reuses the loaded transcript", async () => {
  vi.spyOn(HTMLElement.prototype, "clientWidth", "get").mockReturnValue(1200);
  const { container, client, expectNoWrites } = setup(true);
  const user = userEvent.setup();
  const panel = await screen.findByRole("complementary", { name: "执行记录" });
  await within(panel).findByRole("region", { name: "执行记录" });
  const editor = container.querySelector<HTMLTextAreaElement>(".v2-shared-composer textarea")!;
  expect(editor).toBeVisible(); expect(editor).toHaveValue(originalDraft);
  expect(container.querySelector(".v2-conversation-scroll")).toHaveTextContent("Saved assistant reply");
  expect(screen.queryByRole("button", { name: "补充消息" })).not.toBeInTheDocument();
  const reads = vi.mocked(client.getPage).mock.calls.length;
  await user.click(within(panel).getByRole("button", { name: /workspace_read/ }));
  expect(await within(panel).findByRole("button", { name: "关闭记录详情" })).toHaveFocus();
  await user.keyboard("{Escape}");
  expect(panel).toBeVisible();
  expect(within(panel).getByRole("button", { name: /workspace_read/ })).toHaveFocus();
  await user.click(within(panel).getByRole("button", { name: "收起执行记录" }));
  await user.click(screen.getByRole("button", { name: "执行记录" }));
  expect(await screen.findByRole("complementary", { name: "执行记录" })).toBeVisible();
  expect(vi.mocked(client.getPage).mock.calls.length).toBe(reads);
  expect(editor).toHaveValue(originalDraft); expectNoWrites();
});

it("closes selected details before the compact records workspace and restores focus", async () => {
  const { container, expectNoWrites } = setup(true);
  const user = userEvent.setup();
  const panel = await screen.findByRole("dialog", { name: "执行记录" });
  const row = await within(panel).findByRole("button", { name: /workspace_read/ });
  await user.click(row);
  expect(within(panel).getByRole("button", { name: "关闭记录详情" })).toHaveFocus();
  fireEvent.keyDown(document.activeElement!, { key: "Escape", isComposing: true });
  expect(panel).toBeVisible();
  expect(within(panel).getByLabelText("选中记录详情")).toBeVisible();
  await user.keyboard("{Escape}");
  expect(panel).toBeVisible(); expect(row).toHaveFocus();
  expect(within(panel).queryByLabelText("选中记录详情")).not.toBeInTheDocument();
  await user.keyboard("{Escape}");
  expect(panel).not.toBeInTheDocument();
  expect(screen.getByRole("button", { name: "执行记录" })).toHaveFocus();
  expect(container.querySelector(".v2-shared-composer textarea")).toHaveValue(originalDraft);
  expectNoWrites();
});

it("keeps exact run and UI evidence links in the records header menu", async () => {
  const { onOpenTool, expectNoWrites } = setup(true);
  const user = userEvent.setup();
  const panel = await screen.findByRole("dialog", { name: "执行记录" });
  expect(within(panel).queryByRole("navigation", { name: "执行观察工具" })).not.toBeInTheDocument();
  expect(within(panel).queryByRole("button", { name: "全部运行与会话" })).not.toBeInTheDocument();
  for (const label of ["运行与工具", "界面观察证据"]) {
    await user.click(within(panel).getByRole("button", { name: "执行记录选项" }));
    await user.click(within(panel).getByRole("menuitem", { name: label }));
    expect(within(panel).queryByRole("menu")).not.toBeInTheDocument();
  }
  await user.keyboard("{Escape}");
  for (const label of ["工作区恢复", "续接与记忆", "会话原始记录"]) {
    await user.click(screen.getByRole("button", { name: "查看上下文" }));
    await user.click(await screen.findByRole("button", { name: label }));
  }
  expect(onOpenTool.mock.calls).toEqual([["run", "run-thread-a"], ["run", "run-thread-a", "ui-evidence"],
    ["run", "run-thread-a", "checkpoints"], ["run", "run-thread-a", "context"], ["session", "session-thread-a"]]);
  expectNoWrites();
});

it.each([false, true])("opens records options by keyboard and closes only the menu (docked: %s)", async (docked) => {
  if (docked) vi.spyOn(HTMLElement.prototype, "clientWidth", "get").mockReturnValue(1200);
  const { expectNoWrites } = setup(true);
  const user = userEvent.setup();
  const panel = await screen.findByRole(docked ? "complementary" : "dialog", { name: "执行记录" });
  const row = await within(panel).findByRole("button", { name: /workspace_read/ });
  await user.click(row);
  const trigger = within(panel).getByRole("button", { name: "执行记录选项" });
  trigger.focus();
  await user.keyboard("{Enter}");
  expect(trigger).toHaveAttribute("aria-expanded", "true");
  expect(within(panel).getByRole("menuitem", { name: "运行与工具" })).toHaveFocus();
  await user.keyboard("{ArrowDown}");
  expect(within(panel).getByRole("menuitem", { name: "界面观察证据" })).toHaveFocus();
  await user.keyboard("{Escape}");
  expect(panel).toBeVisible();
  expect(within(panel).getByLabelText("选中记录详情")).toBeInTheDocument();
  expect(within(panel).queryByRole("menu")).not.toBeInTheDocument();
  expect(trigger).toHaveFocus();
  await user.keyboard("{ArrowUp}");
  expect(within(panel).getByRole("menuitem", { name: "界面观察证据" })).toHaveFocus();
  const search = within(panel).getByLabelText("在已加载记录中查找");
  await user.click(search);
  expect(within(panel).queryByRole("menu")).not.toBeInTheDocument();
  expect(search).toHaveFocus();
  const add = within(panel).getByRole("button", { name: "新建或切换工作区标签" });
  await user.click(add);
  trigger.focus();
  await user.keyboard("{ArrowDown}");
  expect(add).toHaveAttribute("aria-expanded", "false");
  expect(within(panel).getAllByRole("menu")).toHaveLength(1);
  await user.keyboard("{Escape}");
  expect(within(panel).queryByRole("menu")).not.toBeInTheDocument();
  expect(panel).toBeVisible();
  expect(trigger).toHaveFocus();
  expectNoWrites();
});

it("keeps focus in the composer when an outside click dismisses record options", async () => {
  vi.spyOn(HTMLElement.prototype, "clientWidth", "get").mockReturnValue(1200);
  const { expectNoWrites } = setup(true);
  const user = userEvent.setup();
  const panel = await screen.findByRole("complementary", { name: "执行记录" });
  await user.click(within(panel).getByRole("button", { name: "执行记录选项" }));
  expect(within(panel).getByRole("menu")).toBeVisible();
  const editor = screen.getByRole("textbox", { name: "继续对话" });
  await user.click(editor);
  expect(within(panel).queryByRole("menu")).not.toBeInTheDocument();
  expect(editor).toHaveFocus();
  expect(editor).toHaveValue(originalDraft);
  expectNoWrites();
});

it("returns compact records to the action menu when its trigger becomes hidden on resize", async () => {
  let width = 700;
  vi.spyOn(HTMLElement.prototype, "clientWidth", "get").mockImplementation(() => width);
  const resizes = new Set<() => void>();
  vi.stubGlobal("ResizeObserver", class {
    resize: () => void;
    constructor(callback: ResizeObserverCallback) {
      this.resize = () => callback([], this as unknown as ResizeObserver);
      resizes.add(this.resize);
    }
    observe() {}
    disconnect() { resizes.delete(this.resize); }
  });
  const { container, expectNoWrites } = setup();
  const user = userEvent.setup();
  await user.click(await screen.findByRole("button", { name: "执行记录" }));
  const records = await screen.findByRole("dialog", { name: "执行记录" });
  const workflow = container.querySelector<HTMLElement>(".v2-task-workflows")!;
  act(() => {
    width = 390;
    workflow.style.display = "none";
    for (const resize of [...resizes]) resize();
  });
  await user.click(within(records).getByRole("button", { name: "收起执行记录" }));
  expect(screen.getByRole("button", { name: "对话操作" })).toHaveFocus();
  expectNoWrites();
});

it("guards the dirty file editor before switching to execution records", async () => {
  vi.spyOn(HTMLElement.prototype, "clientWidth", "get").mockReturnValue(1200);
  const { expectNoWrites } = setup(false, true);
  const user = userEvent.setup();
  await user.click(await screen.findByRole("button", { name: "工作区文件" }));
  await user.click(screen.getByRole("button", { name: "执行记录" }));
  const guard = await screen.findByRole("dialog");
  expect(guard).toHaveTextContent("README.md");
  expect(getThreadWorkspaceState("thread-a").activeTabID).toBe("file:1");
  await user.click(within(guard).getByRole("button", { name: "继续编辑" }));
  expect(getThreadWorkspaceState("thread-a").activeTabID).toBe("file:1");
  await user.click(screen.getByRole("button", { name: "执行记录" }));
  await user.click(screen.getByRole("button", { name: "保留草稿" }));
  expect(await screen.findByRole("complementary", { name: "执行记录" })).toBeVisible();
  expect(getThreadWorkspaceState("thread-a").activeTabID).toBe("records");
  expectNoWrites();
});

afterEach(() => { vi.restoreAllMocks(); vi.unstubAllGlobals(); });

// Retain textarea coverage through the supported plain-text editing mode.
beforeEach(() => localStorage.setItem("universal-code.composer-plain-text", "true"));

function render(ui: ReactElement, options?: RenderOptions) {
  window.localStorage.setItem("prayu.locale.v1", "zh-CN");
  return renderComponent(ui, { wrapper: LocaleProvider, ...options });
}
