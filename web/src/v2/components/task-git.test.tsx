import type { ReactElement } from "react";
import type { RenderOptions } from "@testing-library/react";
import { LocaleProvider } from "../../lib/locale";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act, cleanup, render as renderComponent, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import type { APIClient } from "../../api/client";
import type { WorkspaceView } from "../../api/types";
import type { ThreadGitExecuteRequest, ThreadGitPreview, ThreadGitResult, ThreadGitSpec, ThreadGitState } from "../../api/task-delivery";
import { V2RecoveryProvider, useV2RecoveryStore, type V2RecoveryStore } from "../recovery-storage";
import { TaskGit } from "./task-git";
import type { GitFormState } from "./git-form-state";

const scopeID = "task-git-fixture-db";
const fingerprint = "a".repeat(64);
const when = "2026-09-11T00:00:00Z";
const attemptKey = (thread: string) => `thread:${thread}:git-attempt`;
type SavedAttempt = { threadID: string; runID: string; key: string; operation: string };

function gitState(threadID: string): ThreadGitState {
  return { version: "thread_git.v1", thread_id: threadID, run_id: `${threadID}-run`, session_id: `${threadID}-session`,
    workspace_id: `${threadID}-physical`, source_workspace_id: `${threadID}-source`, repository_root: `D:/fixture/${threadID}`,
    branch: "feature/current", head_oid: "b".repeat(40), binding_fingerprint: "c".repeat(64),
    branches: ["feature/current", "main"], remotes: [{ name: "origin", url: "https://github.com/fixture/repo.git" }],
    changes: [{ path: "chosen.txt", staging: "unmodified", worktree: "modified" },
      { path: "user-staged.txt", staging: "modified", worktree: "unmodified" }], can_execute: true, truncated: false };
}
function gitPreview(threadID: string, spec: ThreadGitSpec): ThreadGitPreview {
  return { ...gitState(threadID), spec, diff: "--- a/chosen.txt\n+++ b/chosen.txt\n@@ -1 +1 @@\n-old\n+selected",
    can_execute: true, preview_fingerprint: fingerprint };
}
function gitResult(threadID: string, state = "unknown", extra: Partial<ThreadGitResult> = {}): ThreadGitResult {
  return { version: "thread_git.v1", thread_id: threadID, run_id: `${threadID}-run`, state,
    observed: state === "completed", receipt_saved: false, replayed: true, ...extra };
}
function fixture() {
  const observed = new Map<string, ThreadGitResult>();
  const hooks: {
    state?: (threadID: string) => ThreadGitState | Promise<ThreadGitState>;
    preview?: (threadID: string, spec: ThreadGitSpec) => Promise<ThreadGitPreview>;
    execute?: (threadID: string, input: ThreadGitExecuteRequest) => Promise<ThreadGitResult>;
  } = {};
  const get = vi.fn(async (path: string, _query?: unknown, _signal?: AbortSignal) => {
    const match = /^\/threads\/([^/]+)\/git(?:\/requests\/([^/]+))?$/u.exec(path);
    if (!match) throw new Error(`unexpected fixture GET ${path}`);
    const threadID = decodeURIComponent(match[1]);
    return match[2] ? observed.get(`${threadID}:${decodeURIComponent(match[2])}`) ?? gitResult(threadID, "not_received") : hooks.state?.(threadID) ?? gitState(threadID);
  });
  const postControl = vi.fn(async (path: string, body: unknown, key: string) => {
    const match = /^\/threads\/([^/]+)\/git\/(preview|execute)$/u.exec(path);
    if (!match) throw new Error(`unexpected fixture POST ${path}`);
    const threadID = decodeURIComponent(match[1]);
    if (match[2] === "preview") {
      const request = body as { version: string; run_id: string; spec: ThreadGitSpec };
      return hooks.preview ? hooks.preview(threadID, request.spec) : gitPreview(threadID, request.spec);
    }
    const request = body as ThreadGitExecuteRequest;
    expect(key).toBe(request.operation_key);
    if (hooks.execute) return hooks.execute(threadID, request);
    const result = gitResult(threadID, "completed", { spec: request.spec, commit_oid: "d".repeat(40), receipt_saved: true, completed_at: when });
    observed.set(`${threadID}:${key}`, result);
    return result;
  });
  const workspace: WorkspaceView = { id: "imported-worktree", name: "New isolated work", created_at: when };
  const importWorkspace = vi.fn().mockResolvedValue({ workspace });
  const client = { baseURL: "/api/v1", hasControl: true, hasWorkspaceImport: true, hasGitHubReviewControl: false,
    get, postControl, importWorkspace } as unknown as APIClient;
  return { client, get, postControl, importWorkspace, workspace, observed, hooks,
    previews: () => postControl.mock.calls.filter(([path]) => path.endsWith("/preview")),
    executions: () => postControl.mock.calls.filter(([path]) => path.endsWith("/execute")) };
}

function mount(f: ReturnType<typeof fixture>, threadID = "task-a", identity = scopeID) {
  let store!: V2RecoveryStore;
  const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false, staleTime: 5000 }, mutations: { retry: false } } });
  const onOpenWorktree = vi.fn(), onFeedback = vi.fn(), onPullRequest = vi.fn();
  function Probe() { store = useV2RecoveryStore()!; return null; }
  let currentThread = threadID;
  const tree = (id: string, visible = true) => <QueryClientProvider client={queryClient}>
    <V2RecoveryProvider client={f.client} scopeID={identity}><Probe />
      {/* The actual review owner remounts its task body on Run/tab changes. */}
      {visible && <TaskGit key={id} client={f.client} threadID={id} working={false} onFeedback={onFeedback}
        onPullRequest={onPullRequest} onOpenWorktree={onOpenWorktree} />}
    </V2RecoveryProvider>
  </QueryClientProvider>;
  const view = render(tree(threadID));
  return { ...view, store: () => store, queryClient, onOpenWorktree, onFeedback, onPullRequest,
    selectThread: (id: string) => { currentThread = id; view.rerender(tree(id)); },
    showGit: (visible: boolean) => view.rerender(tree(currentThread, visible)) };
}
async function ready(threadID = "task-a") {
  await screen.findByText(`D:/fixture/${threadID}`);
  await waitFor(() => expect(screen.getByRole("button", { name: "刷新仓库" })).toBeEnabled());
}
async function selectCommit(message = "only the selected change") {
  const user = userEvent.setup();
  await user.click(screen.getByRole("checkbox", { name: /^chosen\.txt/u }));
  await user.type(screen.getByRole("textbox", { name: "提交说明" }), message);
  await user.click(screen.getByRole("button", { name: "预览本次操作" }));
  await screen.findByRole("region", { name: "Git 操作确认" });
  return user;
}
beforeEach(() => window.localStorage.clear());
afterEach(() => { cleanup(); vi.restoreAllMocks(); });

it("requires nonempty selection, exact preview and a separate confirmation before executing", async () => {
  const f = fixture();
  const view = mount(f);
  await ready();
  expect(screen.getByRole("button", { name: "预览本次操作" })).toBeDisabled();
  expect(f.postControl).not.toHaveBeenCalled();
  const user = await selectCommit();
  expect(f.previews()).toHaveLength(1);
  expect(f.previews()[0][1]).toEqual({ version: "thread_git.v1", run_id: "task-a-run",
    spec: { operation: "commit", paths: ["chosen.txt"], message: "only the selected change" } });
  expect(f.executions()).toEqual([]);
  // Changing the draft invalidates the old preview instead of applying the old text.
  await user.clear(screen.getByRole("textbox", { name: "提交说明" }));
  await user.type(screen.getByRole("textbox", { name: "提交说明" }), "corrected message");
  expect(screen.queryByRole("region", { name: "Git 操作确认" })).not.toBeInTheDocument();
  await user.click(screen.getByRole("button", { name: "预览本次操作" }));
  await screen.findByRole("region", { name: "Git 操作确认" });
  f.hooks.execute = async (thread, request) => {
    expect(view.store().read<SavedAttempt | null>(attemptKey(thread), null)).toEqual({ threadID: thread, runID: request.run_id,
      key: request.operation_key, operation: "commit" });
    return gitResult(thread, "completed", { spec: request.spec, receipt_saved: true });
  };
  await user.click(screen.getByRole("button", { name: "确认提交所选文件" }));
  await waitFor(() => expect(f.executions()).toHaveLength(1));
  const body = f.executions()[0][1] as ThreadGitExecuteRequest;
  expect(body).toEqual({ version: "thread_git.v1", run_id: "task-a-run", operation_key: expect.stringMatching(/^task-git-/u),
    expected_preview_fingerprint: fingerprint, requested_by: "local-operator",
    spec: { operation: "commit", paths: ["chosen.txt"], message: "corrected message" } });
  expect(JSON.stringify(body)).not.toContain("user-staged.txt");
  await screen.findByText("已确认提交所选文件完成");
});

it("restores a lost response with the original persisted key using GET only", async () => {
  const f = fixture();
  f.hooks.execute = async (thread, input) => {
    f.observed.set(`${thread}:${input.operation_key}`, gitResult(thread));
    throw new Error("fixture response lost");
  };
  const first = mount(f);
  await ready();
  const user = await selectCommit();
  await user.click(screen.getByRole("button", { name: "确认提交所选文件" }));
  await screen.findByText(/操作响应未能确认，已保留原请求/u);
  const saved = first.store().read<SavedAttempt | null>(attemptKey("task-a"), null)!;
  expect(saved.key).toMatch(/^task-git-/u);
  expect(f.executions()).toHaveLength(1);
  first.unmount();
  f.get.mockClear(); f.postControl.mockClear();
  const reopened = mount(f);
  await ready();
  await waitFor(() => expect(f.get).toHaveBeenCalledWith(`/threads/task-a/git/requests/${saved.key}`, {}, expect.any(AbortSignal)));
  await screen.findByText("结果尚未确认，暂不能开始新操作。");
  expect(f.postControl).not.toHaveBeenCalled();
  expect(reopened.store().read(attemptKey("task-a"), null)).toEqual(saved);
  await userEvent.setup().click(screen.getByRole("button", { name: "只读核对原操作" }));
  await waitFor(() => expect(f.get.mock.calls.filter(([path]) => path.includes("/requests/"))).toHaveLength(2));
  expect(f.postControl).not.toHaveBeenCalled();
  expect(screen.queryByRole("button", { name: "确认结果并继续" })).not.toBeInTheDocument();
});

it("requires explicit acknowledgement of not_received before a fresh preview and never reposts the original", async () => {
  const f = fixture(), view = mount(f);
  await ready();
  const saved: SavedAttempt = { threadID: "task-a", runID: "task-a-run", key: "task-git-original", operation: "commit" };
  act(() => view.store().write(attemptKey("task-a"), saved));
  const acknowledge = await screen.findByRole("button", { name: "确认结果并继续" });
  expect(view.store().read(attemptKey("task-a"), null)).toEqual(saved);
  expect(screen.getByRole("button", { name: "预览本次操作" })).toBeDisabled();
  expect(f.postControl).not.toHaveBeenCalled();
  await userEvent.setup().click(acknowledge);
  await waitFor(() => expect(view.store().read(attemptKey("task-a"), null)).toBeNull());
  await ready();
  const user = await selectCommit("newly reviewed request");
  expect(f.executions()).toEqual([]);
  await user.click(screen.getByRole("button", { name: "确认提交所选文件" }));
  await waitFor(() => expect(f.executions()).toHaveLength(1));
  const body = f.executions()[0][1] as ThreadGitExecuteRequest;
  expect(body.operation_key).not.toBe(saved.key);
  expect(body.spec.message).toBe("newly reviewed request");
});

it("keeps new drafts and another task's unknown request when an older task finishes late", async () => {
  const f = fixture();
  let finish!: (value: ThreadGitResult) => void;
  f.hooks.execute = () => new Promise((resolve) => { finish = resolve; });
  const view = mount(f);
  await ready();
  const user = await selectCommit();
  await user.click(screen.getByRole("button", { name: "确认提交所选文件" }));
  await waitFor(() => expect(f.executions()).toHaveLength(1));
  const original = view.store().read<SavedAttempt | null>(attemptKey("task-a"), null)!;
  const other: SavedAttempt = { threadID: "task-b", runID: "task-b-run", key: "task-git-other", operation: "stage" };
  const draftA = "A 的新草稿，不能因旧提交而清除", draftB = "B 的独立新草稿";
  act(() => {
    view.store().write("draft:task-a", draftA);
    view.store().write("draft:task-b", draftB);
    view.store().write(attemptKey("task-b"), other);
  });
  f.observed.set(`task-b:${other.key}`, gitResult("task-b"));
  view.selectThread("task-b");
  await ready("task-b");
  await screen.findByText("核对上次暂存所选文件");
  await act(async () => finish(gitResult("task-a", "completed", { spec: { operation: "commit" }, receipt_saved: true })));
  expect(view.store().read(attemptKey("task-b"), null)).toEqual(other);
  expect(view.store().read("draft:task-a", null)).toBe(draftA);
  expect(view.store().read("draft:task-b", null)).toBe(draftB);
  expect(f.postControl.mock.calls.filter(([path]) => path.startsWith("/threads/task-b/"))).toEqual([]);
  expect(f.get.mock.calls.filter(([path]) => path === `/threads/task-b/git/requests/${original.key}`)).toEqual([]);
  expect(screen.getByText("D:/fixture/task-b")).toBeInTheDocument();
  expect(screen.queryByText("已确认提交所选文件完成")).not.toBeInTheDocument();
});

it("does not observe or execute a damaged cross-task identity stored under this task's key", async () => {
  const f = fixture(), first = mount(f);
  await ready();
  const bad: SavedAttempt = { threadID: "other-task", runID: "other-run", key: "task-git-foreign", operation: "commit" };
  act(() => first.store().write(attemptKey("task-a"), bad));
  await screen.findByText("本机 Git 操作记录异常，已保留原记录；未执行新操作。");
  expect(f.get.mock.calls.filter(([path]) => path.includes("/requests/"))).toEqual([]);
  expect(f.postControl).not.toHaveBeenCalled();
  expect(first.store().read(attemptKey("task-a"), null)).toEqual(bad);
  expect(screen.getByRole("button", { name: "预览本次操作" })).toBeDisabled();
  first.unmount();
  f.get.mockClear();
  const otherDB = mount(f, "task-a", "different-data-store");
  await ready();
  expect(otherDB.store().read(attemptKey("task-a"), null)).toBeNull();
  expect(f.get.mock.calls.filter(([path]) => path.includes("/requests/"))).toEqual([]);
});

it("does not execute when the write-ahead identity cannot be persisted", async () => {
  const f = fixture();
  mount(f);
  await ready();
  const user = await selectCommit();
  vi.spyOn(Storage.prototype, "setItem").mockImplementation(() => { throw new DOMException("quota", "QuotaExceededError"); });
  await user.click(screen.getByRole("button", { name: "确认提交所选文件" }));
  await screen.findByRole("alert");
  expect(f.executions()).toEqual([]);
});

it("uses the returned managed worktree path only after confirmation and opens it through workspace import", async () => {
  const f = fixture(), view = mount(f);
  const returnedPath = "D:/managed/worktrees/exact-created-worktree";
  f.hooks.execute = async (thread, input) => gitResult(thread, "completed", { spec: input.spec, worktree_path: returnedPath, receipt_saved: true });
  await ready();
  const user = userEvent.setup();
  await user.click(screen.getByText("分支、独立目录与暂存"));
  await user.click(screen.getByRole("button", { name: "创建独立工作目录" }));
  await user.type(screen.getByRole("textbox", { name: "新分支名称" }), "feature/isolated");
  await user.type(screen.getByRole("textbox", { name: "独立目录名称" }), "isolated");
  await user.click(screen.getByRole("button", { name: "预览本次操作" }));
  await screen.findByRole("region", { name: "Git 操作确认" });
  expect(f.previews()[0][1]).toEqual({ version: "thread_git.v1", run_id: "task-a-run",
    spec: { operation: "worktree_create", branch: "feature/isolated", worktree_name: "isolated" } });
  expect(f.executions()).toEqual([]);
  expect(f.importWorkspace).not.toHaveBeenCalled();
  await user.click(screen.getByRole("button", { name: "确认创建独立工作目录" }));
  const open = await screen.findByRole("button", { name: "在此目录开始新任务" });
  expect(screen.getByText(`独立目录：${returnedPath}`)).toBeInTheDocument();
  expect(screen.getByText(/原任务和未提交修改保留在原目录/u)).toBeInTheDocument();
  expect(f.importWorkspace).not.toHaveBeenCalled();
  expect(view.onOpenWorktree).not.toHaveBeenCalled();
  await user.click(open);
  await waitFor(() => expect(f.importWorkspace).toHaveBeenCalledExactlyOnceWith(returnedPath));
  expect(view.onOpenWorktree).toHaveBeenCalledExactlyOnceWith(f.workspace);
  expect(screen.getByText("D:/fixture/task-a")).toBeInTheDocument();
});

it("keeps a blocked preview reviewable without allowing its execution", async () => {
  const f = fixture();
  f.hooks.preview = async (thread, spec) => ({ ...gitPreview(thread, spec), can_execute: false, blocked_reason: "fixture changed since review" });
  mount(f);
  await ready();
  await selectCommit();
  expect(screen.getByText("fixture changed since review")).toBeInTheDocument();
  expect(screen.getByRole("button", { name: "确认提交所选文件" })).toBeDisabled();
  expect(f.executions()).toEqual([]);
});

it.each([
  { sameSource: true, label: "项目目录" },
  { sameSource: false, label: "隔离目录" },
])("labels $label from actual workspace binding without guessing from a worktree-looking path", async ({ sameSource, label }) => {
  const f = fixture();
  const path = "D:\\projects\\worktrees\\a-very-specific-project";
  f.hooks.state = (thread) => ({ ...gitState(thread), repository_root: path,
    source_workspace_id: sameSource ? `${thread}-physical` : `${thread}-source` });
  mount(f);
  await screen.findByText("a-very-specific-project");
  const location = screen.getByLabelText("当前 Git 目录");
  expect(within(location).getByText(label)).toBeVisible();
  expect(within(location).getByText("feature/current")).toBeVisible();
  expect(within(location).getByText("b".repeat(8))).toBeVisible();
  expect(within(location).queryByText("独立 worktree")).not.toBeInTheDocument();
  expect(within(location).getByText(path)).not.toBeVisible();
  await userEvent.setup().click(within(location).getByText("完整位置与版本"));
  expect(within(location).getByText(path)).toBeVisible();
  expect(within(location).getByText("b".repeat(40))).toBeVisible();
  expect(f.postControl).not.toHaveBeenCalled();
});

it("keeps the author and commit effects visible while project requirements and exact evidence remain expandable", async () => {
  const f = fixture();
  f.hooks.preview = async (thread, spec) => ({ ...gitPreview(thread, spec), commit_author: { name: "Local Author", email: "local@example.invalid" } });
  mount(f);
  await ready();
  expect(screen.getByRole("button", { name: "提交", pressed: true })).toBeVisible();
  expect(screen.queryByRole("combobox", { name: "操作" })).not.toBeInTheDocument();
  expect(screen.getByRole("button", { name: "切换已有分支" })).not.toBeVisible();
  const user = await selectCommit();
  const confirmation = screen.getByRole("region", { name: "Git 操作确认" });
  await user.click(screen.getByRole("button", { name: "提交", pressed: true }));
  expect(confirmation).toBeInTheDocument();
  expect(within(confirmation).getByText("提交作者：Local Author <local@example.invalid>")).toBeVisible();
  expect(within(confirmation).getByText("本次提交会跳过本地 hooks，生成无签名提交。请确认这种提交方式符合项目要求。")).toBeVisible();
  const projectRequirements = screen.getByText(/内置 Git 操作跳过本地 hooks/u);
  expect(projectRequirements).not.toBeVisible();
  await user.click(screen.getByText("项目检查与提交签名"));
  expect(projectRequirements).toBeVisible();
  expect(projectRequirements).toHaveTextContent("生成无签名提交");
  expect(within(confirmation).getByText(/提交说明末尾会附加 Traverse-Operation/u)).toBeVisible();
  expect(within(confirmation).getByText("本次所选文件（1）")).toBeVisible();
  expect(within(confirmation).getByText(fingerprint)).not.toBeVisible();
  await user.click(within(confirmation).getByText("完整位置与预览记录"));
  expect(within(confirmation).getByText(fingerprint)).toBeVisible();
  expect(within(confirmation).getByText("D:/fixture/task-a")).toBeVisible();
  expect(f.executions()).toEqual([]);
});

it("switches to push without executing a stale commit preview and confirms the exact branch and remote separately", async () => {
  const f = fixture();
  mount(f);
  await ready();
  const user = await selectCommit();
  await user.click(screen.getByRole("button", { name: "推送" }));
  expect(screen.queryByRole("region", { name: "Git 操作确认" })).not.toBeInTheDocument();
  expect(screen.getByRole("button", { name: "推送", pressed: true })).toBeVisible();
  expect(f.executions()).toEqual([]);
  await user.click(screen.getByRole("button", { name: "预览本次操作" }));
  const confirmation = await screen.findByRole("region", { name: "Git 操作确认" });
  expect(within(confirmation).getByText("目标分支：feature/current")).toBeVisible();
  expect(within(confirmation).getByText("目标远端：https://github.com/fixture/repo.git")).toBeVisible();
  expect(f.executions()).toEqual([]);
  await user.click(screen.getByRole("button", { name: "确认推送当前分支" }));
  await waitFor(() => expect(f.executions()).toHaveLength(1));
  expect((f.executions()[0][1] as ThreadGitExecuteRequest).spec).toEqual({ operation: "push_branch", branch: "feature/current",
    remote_url: "https://github.com/fixture/repo.git", credential_name: "" });
});

it("restores selected files and input after remount while awaiting a fresh GET and requiring a new preview", async () => {
  const f = fixture(), view = mount(f);
  await ready();
  await selectCommit("preserved exact selection");
  view.showGit(false);
  const reads = f.get.mock.calls.length;
  let refreshed!: (value: ThreadGitState) => void;
  f.hooks.state = () => new Promise((resolve) => { refreshed = resolve; });
  view.showGit(true);
  await waitFor(() => expect(f.get.mock.calls.length).toBeGreaterThan(reads));
  expect(screen.getByRole("checkbox", { name: /^chosen\.txt/u })).toBeChecked();
  expect(screen.getByRole("checkbox", { name: /^chosen\.txt/u })).toBeDisabled();
  expect(screen.getByRole("textbox", { name: "提交说明" })).toHaveValue("preserved exact selection");
  expect(screen.queryByRole("region", { name: "Git 操作确认" })).not.toBeInTheDocument();
  expect(screen.getByRole("button", { name: "预览本次操作" })).toBeDisabled();
  await act(async () => { refreshed(gitState("task-a")); });
  await ready();
  expect(screen.getByText(/上次操作表单已保留/u)).toBeVisible();
  expect(f.previews()).toHaveLength(1);
  expect(f.executions()).toEqual([]);
  f.hooks.state = undefined;
  await userEvent.setup().click(screen.getByRole("button", { name: "预览本次操作" }));
  await screen.findByRole("region", { name: "Git 操作确认" });
  expect(f.previews()).toHaveLength(2);
  expect((f.previews()[1][1] as { spec: ThreadGitSpec }).spec.paths).toEqual(["chosen.txt"]);
  expect(f.executions()).toEqual([]);
});

it("retains operation, explicit remote, credential name and branch/worktree inputs without leaking them into another task", async () => {
  const f = fixture();
  const backup = "https://github.com/fixture/backup.git";
  f.hooks.state = (thread) => ({ ...gitState(thread), remotes: [...gitState(thread).remotes, { name: "backup", url: backup }] });
  const view = mount(f);
  await ready();
  const user = userEvent.setup();
  await user.click(screen.getByRole("checkbox", { name: /^chosen\.txt/u }));
  await user.type(screen.getByRole("textbox", { name: "提交说明" }), "任务 A 提交说明");
  await user.click(screen.getByText("分支、独立目录与暂存"));
  await user.click(screen.getByRole("button", { name: "创建独立工作目录" }));
  await user.type(screen.getByRole("textbox", { name: "新分支名称" }), "feature/preserved");
  await user.type(screen.getByRole("textbox", { name: "独立目录名称" }), "preserved-worktree");
  await user.click(screen.getByRole("button", { name: "推送" }));
  await user.selectOptions(screen.getByRole("combobox", { name: "推送目标" }), backup);
  await user.type(screen.getByRole("combobox", { name: "凭据名称" }), "saved-connection-name");
  view.selectThread("task-b");
  await ready("task-b");
  expect(screen.getByRole("button", { name: "提交", pressed: true })).toBeVisible();
  expect(screen.getByRole("checkbox", { name: /^chosen\.txt/u })).not.toBeChecked();
  expect(screen.getByRole("textbox", { name: "提交说明" })).toHaveValue("");
  view.selectThread("task-a");
  await ready();
  expect(screen.getByRole("button", { name: "推送", pressed: true })).toBeVisible();
  expect(screen.getByRole("combobox", { name: "推送目标" })).toHaveValue(backup);
  expect(screen.getByRole("combobox", { name: "凭据名称" })).toHaveValue("saved-connection-name");
  await user.click(screen.getByText("分支、独立目录与暂存"));
  await user.click(screen.getByRole("button", { name: "创建独立工作目录" }));
  expect(screen.getByRole("textbox", { name: "新分支名称" })).toHaveValue("feature/preserved");
  expect(screen.getByRole("textbox", { name: "独立目录名称" })).toHaveValue("preserved-worktree");
  await user.click(screen.getByRole("button", { name: "提交" }));
  expect(screen.getByRole("checkbox", { name: /^chosen\.txt/u })).toBeChecked();
  expect(screen.getByRole("textbox", { name: "提交说明" })).toHaveValue("任务 A 提交说明");
  expect(f.postControl).not.toHaveBeenCalled();
});

it("keeps separate forms for the actual directory binding even when paths have identical names", async () => {
  const f = fixture(), view = mount(f);
  await ready();
  const user = userEvent.setup();
  await user.click(screen.getByRole("checkbox", { name: /^chosen\.txt/u }));
  await user.type(screen.getByRole("textbox", { name: "提交说明" }), "only original directory");
  view.showGit(false);
  f.hooks.state = (thread) => ({ ...gitState(thread), workspace_id: "different-physical", repository_root: "D:/fixture/another-root" });
  view.showGit(true);
  await screen.findByText("D:/fixture/another-root");
  await waitFor(() => expect(screen.getByRole("button", { name: "刷新仓库" })).toBeEnabled());
  expect(screen.getByRole("checkbox", { name: /^chosen\.txt/u })).not.toBeChecked();
  expect(screen.getByRole("textbox", { name: "提交说明" })).toHaveValue("");
  expect(screen.getByText(/实际操作目录已变化/u)).toBeVisible();
  view.showGit(false); f.hooks.state = undefined; view.showGit(true);
  await ready();
  expect(screen.getByRole("checkbox", { name: /^chosen\.txt/u })).toBeChecked();
  expect(screen.getByRole("textbox", { name: "提交说明" })).toHaveValue("only original directory");
  expect(f.postControl).not.toHaveBeenCalled();
});

it("retains but explicitly blocks unavailable selected files after a fresh repository revision", async () => {
  const f = fixture(), view = mount(f);
  await ready(); await selectCommit();
  view.showGit(false);
  f.hooks.state = (thread) => ({ ...gitState(thread), head_oid: "e".repeat(40), binding_fingerprint: "f".repeat(64),
    changes: gitState(thread).changes.filter(({ path }) => path !== "chosen.txt") });
  view.showGit(true);
  await ready();
  expect(screen.getByText(/仓库版本已变化/u)).toBeVisible();
  expect(screen.getByRole("alert")).toHaveTextContent("chosen.txt");
  expect(screen.queryByRole("region", { name: "Git 操作确认" })).not.toBeInTheDocument();
  expect(screen.getByRole("button", { name: "预览本次操作" })).toBeDisabled();
  expect(f.previews()).toHaveLength(1);
  expect(f.executions()).toEqual([]);
  await userEvent.setup().click(screen.getByRole("button", { name: "移除这些不可用选择" }));
  expect(screen.queryByRole("alert")).not.toBeInTheDocument();
  expect(screen.getByText("选择文件（0）")).toBeInTheDocument();
});

it("rechecks before confirmation and rejects a changed HEAD without recording an execution attempt", async () => {
  const f = fixture(), view = mount(f);
  await ready(); const user = await selectCommit();
  f.hooks.state = (thread) => ({ ...gitState(thread), head_oid: "e".repeat(40), binding_fingerprint: "f".repeat(64) });
  const reads = f.get.mock.calls.length;
  await user.click(screen.getByRole("button", { name: "确认提交所选文件" }));
  await screen.findByText(/重新核对未完成，尚未执行/u);
  expect(f.get.mock.calls.length).toBeGreaterThan(reads);
  expect(f.executions()).toEqual([]);
  expect(view.store().read(attemptKey("task-a"), null)).toBeNull();
  expect(screen.queryByRole("region", { name: "Git 操作确认" })).not.toBeInTheDocument();
  expect(screen.getByRole("checkbox", { name: /^chosen\.txt/u })).toBeChecked();
});

it("does not create an unknown attempt when the confirmation's fresh GET fails", async () => {
  const f = fixture(), view = mount(f);
  await ready(); const user = await selectCommit();
  f.hooks.state = () => Promise.reject(new Error("fixture fresh read unavailable"));
  await user.click(screen.getByRole("button", { name: "确认提交所选文件" }));
  await screen.findByText(/fixture fresh read unavailable/u);
  expect(f.executions()).toEqual([]);
  expect(view.store().read(attemptKey("task-a"), null)).toBeNull();
  expect(screen.getByRole("checkbox", { name: /^chosen\.txt/u })).toBeChecked();
});

it.each(["unmount", "edit"] as const)("does not continue a late confirmation after %s during its fresh GET", async (action) => {
  const f = fixture(), view = mount(f);
  await ready(); const user = await selectCommit();
  let finishRead!: (value: ThreadGitState) => void;
  f.hooks.state = () => new Promise((resolve) => { finishRead = resolve; });
  await user.click(screen.getByRole("button", { name: "确认提交所选文件" }));
  await waitFor(() => expect(finishRead).toBeDefined());
  const signal = f.get.mock.calls.at(-1)![2] as AbortSignal;
  if (action === "unmount") view.showGit(false);
  else await user.type(screen.getByRole("textbox", { name: "提交说明" }), " later correction");
  expect(signal.aborted).toBe(true);
  await act(async () => { finishRead(gitState("task-a")); });
  expect(f.executions()).toEqual([]);
  expect(view.store().read(attemptKey("task-a"), null)).toBeNull();
});

it("does not post a delayed preview after switching tasks while its fresh GET is pending", async () => {
  const f = fixture(), view = mount(f);
  await ready(); const user = userEvent.setup();
  await user.click(screen.getByRole("checkbox", { name: /^chosen\.txt/u }));
  await user.type(screen.getByRole("textbox", { name: "提交说明" }), "old task pending read");
  let finishRead!: (value: ThreadGitState) => void;
  f.hooks.state = (thread) => thread === "task-a" ? new Promise((resolve) => { finishRead = resolve; }) : gitState(thread);
  await user.click(screen.getByRole("button", { name: "预览本次操作" }));
  await waitFor(() => expect(finishRead).toBeDefined());
  view.selectThread("task-b");
  await ready("task-b");
  await act(async () => { finishRead(gitState("task-a")); });
  expect(f.postControl).not.toHaveBeenCalled();
  expect(view.store().read(attemptKey("task-a"), null)).toBeNull();
  expect(view.store().read(attemptKey("task-b"), null)).toBeNull();
});

it("retains an unavailable remote choice instead of silently changing the push target", async () => {
  const f = fixture();
  const backup = "https://github.com/fixture/backup.git";
  f.hooks.state = (thread) => ({ ...gitState(thread), remotes: [...gitState(thread).remotes, { name: "backup", url: backup }] });
  const view = mount(f);
  await ready(); const user = userEvent.setup();
  await user.click(screen.getByRole("button", { name: "推送" }));
  await user.selectOptions(screen.getByRole("combobox", { name: "推送目标" }), backup);
  view.showGit(false); f.hooks.state = undefined; view.showGit(true);
  await ready();
  expect(screen.getByRole("combobox", { name: "推送目标" })).toHaveValue(backup);
  expect(screen.getByRole("option", { name: `原推送目标已不可用：${backup}` })).toBeDisabled();
  expect(screen.getByRole("button", { name: "预览本次操作" })).toBeDisabled();
  expect(f.postControl).not.toHaveBeenCalled();
});

it("preserves the existing persisted text inputs when opening a new page cache", async () => {
  const f = fixture(), first = mount(f);
  await ready();
  act(() => {
    first.store().write("thread:task-a:git-message", "原本已保存的提交说明");
    first.store().write("thread:task-a:git-branch", "feature/previous");
    first.store().write("thread:task-a:git-worktree-name", "previous-worktree");
  });
  first.unmount();
  mount(f);
  await ready();
  expect(screen.getByRole("textbox", { name: "提交说明" })).toHaveValue("原本已保存的提交说明");
  const user = userEvent.setup();
  await user.click(screen.getByText("分支、独立目录与暂存"));
  await user.click(screen.getByRole("button", { name: "创建独立工作目录" }));
  expect(screen.getByRole("textbox", { name: "新分支名称" })).toHaveValue("feature/previous");
  expect(screen.getByRole("textbox", { name: "独立目录名称" })).toHaveValue("previous-worktree");
  expect(f.postControl).not.toHaveBeenCalled();
});

it("stops before the preview POST when its fresh read discovers content drift", async () => {
  const f = fixture(), view = mount(f);
  await ready(); const user = userEvent.setup();
  await user.click(screen.getByRole("checkbox", { name: /^chosen\.txt/u }));
  await user.type(screen.getByRole("textbox", { name: "提交说明" }), "preview only after reread");
  const updated = (thread: string) => ({ ...gitState(thread), binding_fingerprint: "e".repeat(64) });
  f.hooks.state = updated;
  await user.click(screen.getByRole("button", { name: "预览本次操作" }));
  await screen.findByText("仓库内容、分支或目录已变化。已保留原表单，请核对当前状态后重新预览。");
  expect(f.postControl).not.toHaveBeenCalled();
  expect(view.store().read(attemptKey("task-a"), null)).toBeNull();
  expect(screen.getByRole("checkbox", { name: /^chosen\.txt/u })).toBeChecked();
  f.hooks.preview = async (thread, spec) => ({ ...gitPreview(thread, spec), binding_fingerprint: "e".repeat(64) });
  await user.click(screen.getByRole("button", { name: "预览本次操作" }));
  await screen.findByRole("region", { name: "Git 操作确认" });
  expect(f.previews()).toHaveLength(1);
  expect(f.executions()).toEqual([]);
});

it.each(["success", "acknowledged"] as const)("clears the consumed preview basis after %s and a refreshed Git binding while preserving other inputs", async (outcome) => {
  const f = fixture(), view = mount(f);
  await ready();
  const user = await selectCommit("keep this explanation for a later commit");
  await user.click(screen.getByText("分支、独立目录与暂存"));
  await user.click(screen.getByRole("button", { name: "暂存所选文件" }));
  await user.click(screen.getByRole("button", { name: "预览本次操作" }));
  await screen.findByRole("region", { name: "Git 操作确认" });
  f.hooks.execute = async (thread, request) => {
    f.hooks.state = (id) => ({ ...gitState(id), binding_fingerprint: "f".repeat(64),
      changes: gitState(id).changes.map((item) => item.path === "chosen.txt" ? { ...item, staging: "modified", worktree: "unmodified" } : item) });
    const completed = gitResult(thread, "completed", { spec: request.spec, receipt_saved: true });
    f.observed.set(`${thread}:${request.operation_key}`, completed);
    if (outcome === "acknowledged") throw new Error("fixture execute response lost after completion");
    return completed;
  };
  await user.click(screen.getByRole("button", { name: "确认暂存所选文件" }));
  await screen.findByText("已确认暂存所选文件完成");
  if (outcome === "acknowledged") await user.click(await screen.findByRole("button", { name: "确认结果并继续" }));
  await waitFor(() => expect(view.store().read(attemptKey("task-a"), null)).toBeNull());
  await ready();
  expect(view.queryClient.getQueryData<ThreadGitState>(["thread", "task-a", "git"])?.binding_fingerprint).toBe("f".repeat(64));
  expect(screen.getByText("选择文件（0）")).toBeInTheDocument();
  expect(screen.queryByText(/仓库版本已变化/u)).not.toBeInTheDocument();
  expect(screen.queryByText(/上次操作表单已保留/u)).not.toBeInTheDocument();
  expect(screen.queryByRole("region", { name: "Git 操作确认" })).not.toBeInTheDocument();
  expect(f.executions()).toHaveLength(1);
  await user.click(screen.getByRole("button", { name: "提交" }));
  expect(screen.getByRole("textbox", { name: "提交说明" })).toHaveValue("keep this explanation for a later commit");
  view.showGit(false); view.showGit(true);
  await ready();
  expect(screen.queryByText(/仓库版本已变化|上次操作表单已保留/u)).not.toBeInTheDocument();
  expect(f.executions()).toHaveLength(1);
});

it("does not consume a newer shared form when an earlier execution finishes late", async () => {
  const f = fixture(), view = mount(f);
  let finish!: (value: ThreadGitResult) => void;
  f.hooks.execute = () => new Promise((resolve) => { finish = resolve; });
  await ready(); const user = await selectCommit("original reviewed form");
  await user.click(screen.getByRole("button", { name: "确认提交所选文件" }));
  await waitFor(() => expect(finish).toBeDefined());
  // A second consumer of this page's repository form updates the cache while
  // the original request is pending; completion may consume only its own form.
  act(() => view.queryClient.setQueriesData<GitFormState>({ predicate: (query) =>
    query.queryKey[1] === "task-a" && query.queryKey[2] === "git-form" && Boolean(query.queryKey[4]) },
  (current) => current && { ...current, message: "later independent form", paths: ["user-staged.txt"] }));
  await act(async () => finish(gitResult("task-a", "completed", { spec: { operation: "commit" }, receipt_saved: true })));
  await waitFor(() => expect(view.store().read(attemptKey("task-a"), null)).toBeNull());
  await ready();
  expect(screen.getByRole("textbox", { name: "提交说明" })).toHaveValue("later independent form");
  expect(screen.getByRole("checkbox", { name: /^chosen\.txt/u })).not.toBeChecked();
  expect(screen.getByRole("checkbox", { name: /^user-staged\.txt/u })).toBeChecked();
  expect(screen.getByText(/上次操作表单已保留/u)).toBeVisible();
  expect(f.executions()).toHaveLength(1);
});

function render(ui: ReactElement, options?: RenderOptions) {
  localStorage.setItem("prayu.locale.v1", "zh-CN");
  const Wrapper = options?.wrapper;
  return renderComponent(ui, {...options, wrapper: ({children}) => <LocaleProvider>{Wrapper ? <Wrapper>{children}</Wrapper> : children}</LocaleProvider>});
}
