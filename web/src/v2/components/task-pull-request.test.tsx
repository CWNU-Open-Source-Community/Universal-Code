import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act, cleanup, render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import type { APIClient } from "../../api/client";
import { V2RecoveryProvider, useV2RecoveryStore, type V2RecoveryStore } from "../recovery-storage";
import { githubURL, TaskPullRequest } from "./task-pull-request";

const head = "a".repeat(40), base = "b".repeat(40);
const connection = { id: "connection-1", credential: { name: "fixture", kind: "fine_grained_pat" },
  repository: { host: "github.com", owner: "test", name: "repo", full_name: "test/repo", private: false },
  network: { write_enabled: true, allowed_log_hosts: [] }, generation: 1, enabled: true };
const context = { thread_id: "thread-1", run_id: "run-1", session_id: "session-1", source_workspace_id: "workspace-1", workspace_id: "workspace-1",
  repository_root: "D:/acceptance/repo", head_oid: head, branch: "feature/test", binding_fingerprint: "c".repeat(64), remotes: [] };
const preview = { version: "thread_pull_request.v1", thread_id: "thread-1", run_id: "run-1", connection_id: "connection-1", operation_id: "op-pr-1",
  draft: { repository: connection.repository, head_branch: "feature/test", base_branch: "main", head_sha: head, base_sha: base,
    title: "修复入口", body: "实际验证了入口返回", credential: connection.credential }, draft_only: true };
const approval = { ID: "approval-1", Status: "pending", RunID: "run-1" };
const pr = { number: 12, url: "https://github.com/test/repo/pull/12", title: { text: "修复入口" }, head_sha: head,
  base_sha: base, head_branch: "feature/test", base_branch: "main", state: "open", draft: true };
const discovery = { version: "thread_pull_request.v1", context, connection_id: "connection-1", repository: connection.repository,
  base_branch: "main", base_sha: base, remote_head_sha: head, head_published: true, pull_requests: [], write_enabled: true,
  write_permission_verified: false, diagnostics: [], checked_at: "2026-09-11T00:00:00Z" };

function fixture() {
  let state = "not_received";
  let currentApproval: typeof approval | undefined = approval;
  const get = vi.fn(async (path: string, _query?: unknown, _signal?: AbortSignal): Promise<unknown> => path.endsWith("/pr-followups") ? { version: "pr-followup.v1", thread_id: path.split("/")[2], worker_state: "running", items: [] }
    : path.endsWith("/git") ? { version: "thread_git.v1", ...context, branches: ["main", "feature/test"], changes: [], truncated: false, can_execute: true }
    : path.endsWith("/request") ? { version: "thread_pull_request.v1", thread_id: "thread-1", state,
      ...(state === "proposed" ? { preview, approval: currentApproval } : {}), ...(state === "created" ? { pull_request: pr, head_matches_reviewed: true } : {}) }
      : discovery);
  const postControl = vi.fn(async (path: string, _body?: unknown, _key?: string): Promise<unknown> => {
    if (path.endsWith("/preview")) { state = "proposed"; currentApproval = approval; return { version: "thread_pull_request.v1", preview, approval, existing_pull_requests: [] }; }
    if (path.endsWith("/create")) { state = "created"; return { version: "thread_pull_request.v1", thread_id: "thread-1", state, pull_request: pr, head_matches_reviewed: true }; }
    return {};
  });
  const client = { baseURL: "http://127.0.0.1:19991/api/v1", hasGitHubReviewControl: true, hasApprovalControl: true,
    githubReviewConnections: vi.fn().mockResolvedValue([{ connection, credential: { configured: true } }]),
    githubReviewProjection: vi.fn().mockResolvedValue({ snapshots: [] }),
    decideApproval: vi.fn().mockResolvedValue({}), configureGitHubReview: vi.fn().mockResolvedValue({ connection }), get, postControl };
  return { client, setState: (value: string) => { state = value; }, setApproval: (value: typeof approval | undefined) => { currentApproval = value; } };
}
function show(client: ReturnType<typeof fixture>["client"], onFeedback = vi.fn(), options: { threadID?: string; seed?: (store: V2RecoveryStore) => void } = {}) {
  let store!: V2RecoveryStore, seeded = false;
  function Probe() { store = useV2RecoveryStore()!; if (!seeded) { seeded = true; options.seed?.(store); } return null; }
  const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false }, mutations: { retry: false } } });
  const tree = (threadID: string) => <QueryClientProvider client={queryClient}><V2RecoveryProvider client={client} scopeID="acceptance-store"><Probe />
    <TaskPullRequest client={client as unknown as APIClient} threadID={threadID} working={false} onFeedback={onFeedback} onGit={vi.fn()} />
  </V2RecoveryProvider></QueryClientProvider>;
  const ui = render(tree(options.threadID ?? "thread-1"));
  return { ...ui, queryClient, store: () => store, selectThread: (id: string) => ui.rerender(tree(id)) };
}
beforeEach(() => localStorage.clear());
afterEach(() => { cleanup(); vi.restoreAllMocks(); });

async function selectConnection(user: ReturnType<typeof userEvent.setup>) {
  await user.click(screen.getByText("选择或接入 GitHub"));
  await screen.findByRole("option", { name: /test\/repo/ });
  await user.selectOptions(screen.getByRole("combobox", { name: "GitHub 连接" }), "connection-1");
}
async function openDraft(user: ReturnType<typeof userEvent.setup>) {
  await user.click(screen.getByText("创建草稿 PR"));
}

it("previews first and only approves and creates after explicit confirmation", async () => {
  const { client } = fixture(), user = userEvent.setup();
  show(client);
  await selectConnection(user);
  await screen.findByText(/本地提交/);
  await openDraft(user);
  await user.type(screen.getByRole("textbox", { name: "标题" }), "修复入口");
  await user.type(screen.getByRole("textbox", { name: "说明" }), "实际验证了入口返回");
  await user.click(screen.getByRole("button", { name: "预览草稿 PR" }));
  const confirm = await screen.findByRole("button", { name: "批准并创建这份草稿 PR" });
  await waitFor(() => expect(confirm).toBeEnabled());
  expect(client.decideApproval).not.toHaveBeenCalled();
  expect(client.postControl.mock.calls.filter(([path]) => path.endsWith("/create"))).toHaveLength(0);
  await user.click(confirm);
  await waitFor(() => expect(client.decideApproval).toHaveBeenCalledOnce());
  const approved = client.decideApproval.mock.calls[0] as unknown as unknown[];
  expect(approved.slice(0, 3)).toEqual(["run-1", "approval-1", { version: "approval_control.v1", action: "approve_once" }]);
  await waitFor(() => expect(client.postControl.mock.calls.filter(([path]) => path.endsWith("/create"))).toHaveLength(1));
  expect(client.decideApproval.mock.invocationCallOrder[0]).toBeLessThan(client.postControl.mock.invocationCallOrder[1]);
});

it("reopens an unknown creation by observing the original key without approval or another POST", async () => {
  const { client, setState } = fixture(), user = userEvent.setup();
  const first = show(client);
  await selectConnection(user);
  await screen.findByText(/本地提交/);
  await openDraft(user);
  await user.type(screen.getByRole("textbox", { name: "标题" }), "修复入口");
  await user.type(screen.getByRole("textbox", { name: "说明" }), "实际验证了入口返回");
  await user.click(screen.getByRole("button", { name: "预览草稿 PR" }));
  await waitFor(() => expect(screen.getByRole("button", { name: "批准并创建这份草稿 PR" })).toBeEnabled());
  client.postControl.mockImplementation(async (path) => { if (path.endsWith("/create")) { setState("unknown"); throw new Error("response lost"); } return {}; });
  await user.click(screen.getByRole("button", { name: "批准并创建这份草稿 PR" }));
  await screen.findByText(/创建结果未知/);
  const requestCall = client.get.mock.calls.find(([path]) => path.endsWith("/request"));
  expect(requestCall).toBeTruthy();
  first.unmount(); first.queryClient.clear();
  client.get.mockClear(); client.postControl.mockClear(); client.decideApproval.mockClear();
  show(client);
  await screen.findByText(/创建结果未知/);
  expect(screen.getByRole("region", { name: "原 PR 创建请求" }).closest("details")).toBeNull();
  expect(client.get.mock.calls.some(([path]) => path.endsWith("/request"))).toBe(true);
  expect(client.postControl).not.toHaveBeenCalled(); expect(client.decideApproval).not.toHaveBeenCalled();
  expect(screen.queryByRole("button", { name: "批准并创建这份草稿 PR" })).not.toBeInTheDocument();
});

it("does not retain a credential in storage or mutation variables", async () => {
  const { client } = fixture(), user = userEvent.setup();
  const ui = show(client);
  await selectConnection(user);
  await user.click(screen.getByText("接入 GitHub 或保存凭据"));
  await user.type(screen.getByLabelText("GitHub 访问令牌"), "fixture-only-token-293");
  await user.click(screen.getByRole("button", { name: "保存 GitHub 连接" }));
  await screen.findByText(/连接已保存/);
  expect(screen.getByLabelText("GitHub 访问令牌")).toHaveValue("");
  expect(JSON.stringify(localStorage)).not.toContain("fixture-only-token-293");
  expect(JSON.stringify(ui.queryClient.getMutationCache().getAll().map((entry) => entry.state))).not.toContain("fixture-only-token-293");
});

it("only links to credential-free GitHub HTTPS pages", () => {
  expect(githubURL("https://github.com/test/repo/pull/12")).toBe("https://github.com/test/repo/pull/12");
  expect(githubURL("javascript:alert(1)")).toBeUndefined();
  expect(githubURL("https://github.com.evil.example/test")).toBeUndefined();
  expect(githubURL("https://token@github.com/test")).toBeUndefined();
});

const originalAttempt = { threadID: "thread-1", key: "task-pr-original-request" };
function seedAttempt(store: V2RecoveryStore) {
  store.write("thread:thread-1:pr-attempt", originalAttempt);
  store.write("thread:thread-1:pr-connection", "connection-1");
  store.write("thread:thread-1:pr-title", "后来编辑的标题");
  store.write("thread:thread-1:pr-body", "后来编辑的说明");
  store.write("thread:thread-1:pr-base", "later-base");
}

it("reopens a preview without approval with GET only and explicitly repairs the exact original intent", async () => {
  const f = fixture(), user = userEvent.setup();
  f.setState("proposed"); f.setApproval(undefined);
  const view = show(f.client, vi.fn(), { seed: seedAttempt });
  await waitFor(() => expect(screen.getByRole("button", { name: "补齐原预览的审批" })).toBeEnabled());
  expect(f.client.postControl).not.toHaveBeenCalled();
  expect(f.client.decideApproval).not.toHaveBeenCalled();
  expect(screen.queryByRole("button", { name: /放弃/ })).not.toBeInTheDocument();
  await user.click(screen.getByRole("button", { name: "补齐原预览的审批" }));
  await waitFor(() => expect(screen.getByRole("button", { name: "批准并创建这份草稿 PR" })).toBeEnabled());
  expect(f.client.postControl).toHaveBeenCalledExactlyOnceWith("/threads/thread-1/pull-request/preview", {
    version: "thread_pull_request.v1", connection_id: preview.connection_id, base_branch: "main",
    title: preview.draft.title, body: preview.draft.body, expected_run_id: "run-1", expected_head_sha: head,
    operation_key: originalAttempt.key,
  }, originalAttempt.key);
  expect(f.client.decideApproval).not.toHaveBeenCalled();
  expect(view.store().read("thread:thread-1:pr-title", "")).toBe("后来编辑的标题");
  expect(view.store().read("thread:thread-1:pr-base", "")).toBe("later-base");
});

it("denies the pending original approval before clearing the local preview", async () => {
  const f = fixture(), user = userEvent.setup();
  f.setState("proposed");
  const view = show(f.client, vi.fn(), { seed: seedAttempt });
  await waitFor(() => expect(screen.getByRole("button", { name: "拒绝原审批并放弃预览" })).toBeEnabled());
  let resolve!: (value: unknown) => void;
  f.client.decideApproval.mockImplementation(() => new Promise((done) => { resolve = done; }));
  await user.click(screen.getByRole("button", { name: "拒绝原审批并放弃预览" }));
  expect(f.client.decideApproval).toHaveBeenCalledExactlyOnceWith("run-1", "approval-1", { version: "approval_control.v1", action: "deny" }, `${originalAttempt.key}-deny`);
  expect(view.store().read("thread:thread-1:pr-attempt", null)).toEqual(originalAttempt);
  f.setApproval({ ...approval, Status: "denied" });
  await act(async () => resolve({ status: "denied" }));
  await waitFor(() => expect(view.store().read("thread:thread-1:pr-attempt", null)).toBeNull());
  expect(screen.queryByRole("region", { name: "原 PR 创建请求" })).not.toBeInTheDocument();
  expect(f.client.postControl).not.toHaveBeenCalled();
});

it("keeps the original intent after a lost denial response and only reads its result on reopen", async () => {
  const f = fixture(), user = userEvent.setup();
  f.setState("proposed");
  f.client.decideApproval.mockImplementation(async () => { f.setApproval({ ...approval, Status: "denied" }); throw new Error("fixture denial response lost"); });
  const first = show(f.client, vi.fn(), { seed: seedAttempt });
  await waitFor(() => expect(screen.getByRole("button", { name: "拒绝原审批并放弃预览" })).toBeEnabled());
  await user.click(screen.getByRole("button", { name: "拒绝原审批并放弃预览" }));
  await screen.findByText("原审批已拒绝，不能用于创建 PR。");
  expect(first.store().read("thread:thread-1:pr-attempt", null)).toEqual(originalAttempt);
  first.unmount(); first.queryClient.clear(); f.client.decideApproval.mockClear();
  const reopened = show(f.client);
  await waitFor(() => expect(screen.getByRole("button", { name: "放弃此预览并重新填写" })).toBeEnabled());
  expect(f.client.postControl).not.toHaveBeenCalled(); expect(f.client.decideApproval).not.toHaveBeenCalled();
  await user.click(screen.getByRole("button", { name: "放弃此预览并重新填写" }));
  expect(reopened.store().read("thread:thread-1:pr-attempt", null)).toBeNull();
});

it("does not pretend local deletion revokes an already approved original preview", async () => {
  const f = fixture(); f.setState("proposed"); f.setApproval({ ...approval, Status: "approved" });
  const view = show(f.client, vi.fn(), { seed: seedAttempt });
  await screen.findByText("原审批已批准；清除本机记录不能撤销它。请继续核对或执行原请求。");
  expect(screen.queryByRole("button", { name: /放弃|确认结果并继续/ })).not.toBeInTheDocument();
  expect(view.store().read("thread:thread-1:pr-attempt", null)).toEqual(originalAttempt);
  expect(f.client.postControl).not.toHaveBeenCalled(); expect(f.client.decideApproval).not.toHaveBeenCalled();
});

it("distinguishes a remotely confirmed PR without a local receipt and a changed target commit", async () => {
  const f = fixture(); const originalGet = f.client.get.getMockImplementation()!;
  f.client.get.mockImplementation(async (path, query, signal) => path.endsWith("/request") ? {
    version: "thread_pull_request.v1", thread_id: "thread-1", state: "created", receipt_saved: false,
    pull_request: { ...pr, base_sha: "d".repeat(40) }, preview, head_matches_reviewed: true,
  } : originalGet(path, query, signal));
  show(f.client, vi.fn(), { seed: seedAttempt });
  await screen.findByText("已在远端核实原 PR；本地完成收据尚未保存。");
  expect(screen.getByText(/目标分支提交此后已有变化/)).toBeInTheDocument();
  expect(screen.queryByText(/远端源提交此后已有变化/)).not.toBeInTheDocument();
  expect(f.client.postControl).not.toHaveBeenCalled(); expect(f.client.decideApproval).not.toHaveBeenCalled();
});

it("persists the base draft without querying per keystroke and requires explicit lookup before preview", async () => {
  const f = fixture(), user = userEvent.setup(); const view = show(f.client);
  await selectConnection(user);
  await waitFor(() => expect(screen.getByRole("button", { name: "查找当前分支的 PR" })).toBeEnabled());
  const calls = () => f.client.get.mock.calls.filter(([path]) => path === "/threads/thread-1/pull-request");
  const before = calls().length;
  await user.click(screen.getByText("目标分支与远端详情"));
  await openDraft(user);
  await user.type(screen.getByRole("textbox", { name: "目标分支" }), "release/next");
  expect(calls()).toHaveLength(before);
  expect(view.store().read("thread:thread-1:pr-base", "")).toBe("release/next");
  expect(screen.getByRole("button", { name: "预览草稿 PR" })).toBeDisabled();
  await user.click(screen.getByRole("button", { name: "查找当前分支的 PR" }));
  await waitFor(() => expect(calls()).toHaveLength(before + 1));
  expect(calls().at(-1)?.[1]).toEqual({ connection_id: "connection-1", base_branch: "release/next" });
  expect(f.client.postControl).not.toHaveBeenCalled();
});

it.each(["same-thread-new-key", "different-thread"])("binds a late creation to the original cache without changing %s", async (mode) => {
  const f = fixture(), user = userEvent.setup(); f.setState("proposed");
  let resolve!: (value: unknown) => void;
  f.client.postControl.mockImplementation(() => new Promise((done) => { resolve = done; }));
  const originalGet = f.client.get.getMockImplementation()!;
  f.client.get.mockImplementation(async (path, query, signal) => {
    const threadID = path.includes("thread-2") ? "thread-2" : "thread-1";
    if (path.endsWith("/request") && (query as { operation_key: string }).operation_key === "task-pr-new-request") return { version: "thread_pull_request.v1", thread_id: threadID, state: "unknown" };
    if (path.includes("thread-2") && path.endsWith("/git")) return { version: "thread_git.v1", ...context, thread_id: threadID, changes: [], branches: [], truncated: false };
    if (path.includes("thread-2") && path.endsWith("/pull-request")) return { ...discovery, context: { ...context, thread_id: threadID } };
    return originalGet(path, query, signal);
  });
  const view = show(f.client, vi.fn(), { seed: seedAttempt });
  await waitFor(() => expect(screen.getByRole("button", { name: "批准并创建这份草稿 PR" })).toBeEnabled());
  await user.click(screen.getByRole("button", { name: "批准并创建这份草稿 PR" }));
  await waitFor(() => expect(f.client.postControl).toHaveBeenCalledOnce());
  const nextThread = mode === "different-thread" ? "thread-2" : "thread-1";
  const nextAttempt = { threadID: nextThread, key: "task-pr-new-request" };
  act(() => {
    view.store().write(`thread:${nextThread}:pr-attempt`, nextAttempt);
    view.store().write(`thread:${nextThread}:pr-number`, 73);
    view.store().write(`thread:${nextThread}:pr-title`, "新标题不能被旧响应覆盖");
  });
  if (mode === "different-thread") view.selectThread(nextThread);
  const created = { version: "thread_pull_request.v1", thread_id: "thread-1", state: "created", pull_request: pr, receipt_saved: true, head_matches_reviewed: true };
  f.setState("created");
  await act(async () => resolve(created));
  await waitFor(() => expect(view.queryClient.getQueryData(["thread", "thread-1", "pr-request", originalAttempt.key])).toMatchObject({ state: "created" }));
  expect(view.queryClient.getQueryData(["thread", nextThread, "pr-request", nextAttempt.key])).toMatchObject({ state: "unknown" });
  expect(view.store().read(`thread:${nextThread}:pr-attempt`, null)).toEqual(nextAttempt);
  expect(view.store().read(`thread:${nextThread}:pr-number`, 0)).toBe(73);
  expect(view.store().read(`thread:${nextThread}:pr-title`, "")).toBe("新标题不能被旧响应覆盖");
  expect(f.client.postControl).toHaveBeenCalledExactlyOnceWith("/threads/thread-1/pull-request/create", {
    version: "thread_pull_request.v1", operation_id: preview.operation_id, approval_id: approval.ID,
  }, originalAttempt.key);
});

it("keeps a later PR selection even when the original creation key is unchanged", async () => {
  const f = fixture(), user = userEvent.setup(); f.setState("proposed");
  let resolve!: (value: unknown) => void;
  f.client.postControl.mockImplementation(() => new Promise((done) => { resolve = done; }));
  const view = show(f.client, vi.fn(), { seed: seedAttempt });
  await waitFor(() => expect(screen.getByRole("button", { name: "批准并创建这份草稿 PR" })).toBeEnabled());
  await user.click(screen.getByRole("button", { name: "批准并创建这份草稿 PR" }));
  await waitFor(() => expect(f.client.postControl).toHaveBeenCalledOnce());
  act(() => view.store().write("thread:thread-1:pr-number", 73));
  f.setState("created");
  await act(async () => resolve({ version: "thread_pull_request.v1", thread_id: "thread-1", state: "created", pull_request: pr, receipt_saved: true, head_matches_reviewed: true }));
  await screen.findByRole("link", { name: "打开原 PR #12" });
  expect(view.store().read("thread:thread-1:pr-number", 0)).toBe(73);
  expect(view.store().read("thread:thread-1:pr-attempt", null)).toEqual(originalAttempt);
});

it("explains that matching remote CI does not validate uncommitted worktree changes", async () => {
  const f = fixture(), originalGet = f.client.get.getMockImplementation()!;
  f.client.get.mockImplementation(async (path, query, signal) => path.endsWith("/git") ? {
    ...(await originalGet(path, query, signal) as object), changes: [{ path: "later-edit.ts", staging: "unmodified", worktree: "modified" }],
  } : originalGet(path, query, signal));
  f.client.githubReviewProjection.mockResolvedValue({ snapshots: [{ id: "saved-ci-1", fetched_at: "2026-09-11T00:00:00Z",
    identity: { number: pr.number, head_sha: head }, omissions: [], check_runs: [], jobs: [], threads: [], loose_comments: [], reviews: [] }] });
  show(f.client, vi.fn(), { seed: (store) => { store.write("thread:thread-1:pr-connection", "connection-1"); store.write("thread:thread-1:pr-number", pr.number); } });
  await screen.findByText("工作目录还有未提交的改动，以下 CI 对应上方提交。请先核对本次 PR 的范围，选择相关文件提交并推送，再核对新提交的检查结果。");
  expect(screen.queryByText("远端记录与当前本地版本不同。请核对本地提交与远端提交，推送后刷新检查结果。")).not.toBeInTheDocument();
  expect(f.client.postControl).not.toHaveBeenCalled();
});

it("prioritizes PR checks and comments while keeping exact stale evidence accessible", async () => {
  const f = fixture(), user = userEvent.setup(), onFeedback = vi.fn();
  const originalGet = f.client.get.getMockImplementation()!;
  const snapshotHead = "d".repeat(40);
  f.client.get.mockImplementation(async (path, query, signal) => path === "/threads/thread-1/pull-request"
    ? { ...discovery, pull_requests: [pr] } : originalGet(path, query, signal));
  f.client.githubReviewProjection.mockResolvedValue({ snapshots: [{ id: "exact-snapshot-73", fetched_at: "2026-09-11T01:00:00Z",
    identity: { number: pr.number, head_sha: snapshotHead }, omissions: [], jobs: [], threads: [], reviews: [],
    check_runs: [{ id: 31, name: "typecheck", status: "completed", conclusion: "failure", head_sha: snapshotHead,
      summary: { text: "泛型边界未通过" }, text: { text: "需要核对当前实现" } }],
    loose_comments: [{ node_id: "comment-73", url: "https://github.com/test/repo/pull/12#issuecomment-73", author: "reviewer",
      body: { text: "请补充空值处理" } }],
  }] });
  show(f.client, onFeedback, { seed: (store) => store.write("thread:thread-1:pr-connection", "connection-1") });
  await screen.findByRole("link", { name: "#12 修复入口" });
  await screen.findByText("typecheck");
  expect(screen.getByText("请补充空值处理")).toBeVisible();
  expect(screen.getByText("远端记录与当前本地版本不同。请核对本地提交与远端提交，推送后刷新检查结果。")).toBeVisible();
  expect(screen.getByRole("combobox", { name: "GitHub 连接" })).not.toBeVisible();
  expect(screen.getByRole("textbox", { name: "目标分支" })).not.toBeVisible();
  expect(screen.getByText("快照与完整提交身份").closest("details")).not.toHaveAttribute("open");
  expect(screen.getByText(snapshotHead)).not.toBeVisible();
  expect(f.client.postControl).not.toHaveBeenCalled(); expect(f.client.decideApproval).not.toHaveBeenCalled();
  await user.click(screen.getByRole("button", { name: "引用检查并修复" }));
  expect(onFeedback).toHaveBeenCalledExactlyOnceWith(expect.stringContaining(`来源快照：exact-snapshot-73\n远端提交：${snapshotHead}`));
  expect(onFeedback.mock.calls[0][0]).toContain("泛型边界未通过\n需要核对当前实现");
  await user.click(screen.getByText("快照与完整提交身份"));
  expect(screen.getByText(snapshotHead)).toBeVisible();
  expect(f.client.postControl).not.toHaveBeenCalled();
  f.client.postControl.mockRejectedValueOnce(new Error("fixture refresh unavailable"));
  await user.click(screen.getByRole("button", { name: "刷新远端 CI 和评论" }));
  await screen.findByText("本次刷新失败，以下保留上次读取的记录。请重试刷新远端 CI 和评论。");
  expect(screen.getByText("typecheck")).toBeVisible();
  expect(screen.getByText("请补充空值处理")).toBeVisible();
  expect(f.client.postControl).toHaveBeenCalledTimes(1);
  expect(f.client.decideApproval).not.toHaveBeenCalled();
});

it("preserves the unsent draft and transient credential when their disclosure is closed", async () => {
  const f = fixture(), user = userEvent.setup(), view = show(f.client);
  await selectConnection(user);
  await screen.findByText("当前分支尚无开放的 PR");
  expect(screen.getByText("创建草稿 PR").closest("details")).not.toHaveAttribute("open");
  await openDraft(user);
  await user.type(screen.getByRole("textbox", { name: "标题" }), "待审阅的标题");
  await user.type(screen.getByRole("textbox", { name: "说明" }), "保留我的验证说明");
  await user.click(screen.getByText("创建草稿 PR"));
  expect(screen.getByRole("textbox", { name: "标题" })).not.toBeVisible();
  await openDraft(user);
  expect(screen.getByRole("textbox", { name: "标题" })).toHaveValue("待审阅的标题");
  expect(screen.getByRole("textbox", { name: "说明" })).toHaveValue("保留我的验证说明");
  await user.click(screen.getByText("接入 GitHub 或保存凭据"));
  await user.type(screen.getByLabelText("GitHub 访问令牌"), "transient-disclosure-fixture-token");
  await user.click(screen.getByText("连接设置"));
  expect(screen.getByLabelText("GitHub 访问令牌")).not.toBeVisible();
  await user.click(screen.getByText("连接设置"));
  expect(screen.getByLabelText("GitHub 访问令牌")).toHaveValue("transient-disclosure-fixture-token");
  expect(view.store().read("thread:thread-1:pr-title", "")).toBe("待审阅的标题");
  expect(view.store().read("thread:thread-1:pr-body", "")).toBe("保留我的验证说明");
  expect(JSON.stringify(localStorage)).not.toContain("transient-disclosure-fixture-token");
  expect(view.store().read("thread:thread-1:pr-attempt", null)).toBeNull();
  expect(f.client.postControl).not.toHaveBeenCalled(); expect(f.client.decideApproval).not.toHaveBeenCalled();
});
