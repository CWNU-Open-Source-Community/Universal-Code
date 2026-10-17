import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { cleanup, render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { APIRequestError, type APIClient } from "../../api/client";
import { PR_FOLLOWUP_VERSION, type PRFollowup, type PRFollowupCreateRequest } from "../../api/pr-followup";
import { useV2RecoveryStore, V2RecoveryProvider, type V2RecoveryStore } from "../recovery-storage";
import { TaskPRFollowup } from "./task-pr-followup";

function followup(overrides: Partial<PRFollowup> = {}): PRFollowup {
  const now = new Date().toISOString();
  return { id: "followup-1", thread_id: "thread-1", connection_id: "connection-1", repository: "test/repo", workspace_id: "workspace-1",
    root_path_sha256: "a".repeat(64), branch: "feature/test", pull_request: 12, interval_seconds: 300,
    deadline_at: new Date(Date.now() + 86_400_000).toISOString(), max_turns: 3, include_existing_comments: false,
    status: "active", revision: 7, turns_started: 1, next_check_at: now, created_at: now, updated_at: now, last_result: "等待新的 PR 反馈", ...overrides };
}
function fixture(initial: PRFollowup[] = []) {
  let items = initial;
  const get = vi.fn(async (path: string) => ({ version: PR_FOLLOWUP_VERSION, thread_id: decodeURIComponent(path.split("/")[2]), worker_state: "running", items }));
  const postControl = vi.fn(async (path: string, body: unknown, _key: string): Promise<PRFollowup> => {
    if (path.endsWith("/pr-followups")) {
      const request = body as PRFollowupCreateRequest;
      const value = followup({ ...request, revision: 1, turns_started: 0 });
      items = [value]; return value;
    }
    const status = path.endsWith("/pause") ? "paused" : path.endsWith("/resume") ? "active" : "cancelled";
    const value = { ...items[0], status, revision: items[0].revision + 1 } as PRFollowup;
    items = [value]; return value;
  });
  const client = { baseURL: "http://127.0.0.1:19991/api/v1", hasGitHubReviewControl: true, get, postControl };
  return { client, setItems: (next: PRFollowup[]) => { items = next; } };
}
function show(f: ReturnType<typeof fixture>, options: { pullRequest?: number; seed?: (store: V2RecoveryStore) => void } = {}) {
  let store!: V2RecoveryStore, seeded = false;
  function Probe() { store = useV2RecoveryStore()!; if (!seeded) { seeded = true; options.seed?.(store); } return null; }
  const queries = new QueryClient({ defaultOptions: { queries: { retry: false }, mutations: { retry: false } } });
  const tree = (connectionID: string, pullRequest: number) => <QueryClientProvider client={queries}>
    <V2RecoveryProvider client={f.client} scopeID="pr-followup-tests"><Probe />
      <TaskPRFollowup client={f.client as unknown as APIClient} threadID="thread-1" connectionID={connectionID} pullRequest={pullRequest} canStart />
    </V2RecoveryProvider></QueryClientProvider>;
  const view = render(tree("connection-1", options.pullRequest ?? 12));
  return { ...view, store: () => store, select: (connectionID: string, pullRequest: number) => view.rerender(tree(connectionID, pullRequest)) };
}
beforeEach(() => localStorage.clear());
afterEach(() => { cleanup(); vi.restoreAllMocks(); });

async function enable(user: ReturnType<typeof userEvent.setup>) {
  await user.click(await screen.findByText("设置持续跟进"));
  await waitFor(() => expect(screen.getByRole("button", { name: "启用持续跟进" })).toBeEnabled());
  await user.click(screen.getByRole("button", { name: "启用持续跟进" }));
}

it("creates with bounded defaults and clears the saved exact intent after a confirmed reply", async () => {
  const f = fixture(), user = userEvent.setup(), view = show(f);
  const before = Date.now();
  await enable(user);
  await screen.findByText("持续跟进中");
  expect(f.client.postControl).toHaveBeenCalledOnce();
  const [path, raw, key] = f.client.postControl.mock.calls[0];
  const body = raw as PRFollowupCreateRequest;
  expect(path).toBe("/threads/thread-1/pr-followups");
  expect(body).toMatchObject({ version: PR_FOLLOWUP_VERSION, connection_id: "connection-1", pull_request: 12, interval_seconds: 300, max_turns: 3, include_existing_comments: false });
  expect(Date.parse(body.deadline_at)).toBeGreaterThanOrEqual(before + 86_400_000);
  expect(Date.parse(body.deadline_at)).toBeLessThanOrEqual(Date.now() + 86_400_000);
  expect(key).toMatch(/^task-pr-followup-/u);
  expect(view.store().read("thread:thread-1:pr-followup-create", null)).toBeNull();
  expect(screen.getByText(/当前的 Full \/ Ask \/ Auto 权限与预算/)).toBeInTheDocument();
});

it("reopens an uncertain creation with GET only and replays the original key and body after selecting another PR", async () => {
  const f = fixture(), user = userEvent.setup();
  f.client.postControl.mockImplementationOnce(async (_path, raw) => {
    f.setItems([followup({ ...(raw as PRFollowupCreateRequest), revision: 1, turns_started: 0 })]);
    throw new Error("创建回复丢失");
  });
  const first = show(f);
  await enable(user);
  await screen.findByText("创建回复丢失");
  expect(screen.queryByRole("button", { name: "重新设置" })).not.toBeInTheDocument();
  const original = f.client.postControl.mock.calls[0];
  expect(first.store().read("thread:thread-1:pr-followup-create", null)).toMatchObject({ key: original[2], body: original[1] });
  first.unmount();
  const reopened = show(f, { pullRequest: 99 });
  await screen.findByText("test/repo #12");
  expect(f.client.postControl).toHaveBeenCalledOnce();
  expect(screen.queryByText("设置持续跟进")).not.toBeInTheDocument();
  await user.click(screen.getByRole("button", { name: "核对原创建请求" }));
  await waitFor(() => expect(f.client.postControl).toHaveBeenCalledTimes(2));
  expect(f.client.postControl.mock.calls[1]).toEqual(original);
  await waitFor(() => expect(reopened.store().read("thread:thread-1:pr-followup-create", null)).toBeNull());
});

it("controls the thread's unfinished follow-up despite a different PR selection", async () => {
  const f = fixture([followup()]), user = userEvent.setup(), view = show(f);
  await screen.findByText("持续跟进中");
  view.select("other-connection", 99);
  expect(screen.getByText("test/repo #12")).toBeInTheDocument();
  expect(screen.queryByRole("button", { name: "启用持续跟进" })).not.toBeInTheDocument();
  await user.click(screen.getByRole("button", { name: "暂停跟进" }));
  await screen.findByText("已暂停");
  expect(f.client.postControl).toHaveBeenNthCalledWith(1, "/threads/thread-1/pr-followups/followup-1/pause",
    { version: PR_FOLLOWUP_VERSION, expected_revision: 7 }, "pr-followup-followup-1-pause-7");
  await waitFor(() => expect(screen.getByRole("button", { name: "继续跟进" })).toBeEnabled());
  await user.click(screen.getByRole("button", { name: "继续跟进" }));
  await screen.findByText("持续跟进中");
  await waitFor(() => expect(screen.getByRole("button", { name: "停止跟进" })).toBeEnabled());
  await user.click(screen.getByRole("button", { name: "停止跟进" }));
  await screen.findByText("已停止");
  expect(screen.getByText("重新开始持续跟进")).toBeInTheDocument();
});

it.each(["blocked", "ready"] as const)("can resume a %s follow-up", async (status) => {
  const f = fixture([followup({ status, last_error: "等待当前任务处理审批" })]), user = userEvent.setup();
  show(f);
  const resume = await screen.findByRole("button", { name: "继续跟进" });
  await waitFor(() => expect(resume).toBeEnabled());
  expect(screen.getByText("等待当前任务处理审批")).toBeInTheDocument();
  await user.click(resume);
  expect(f.client.postControl).toHaveBeenCalledWith("/threads/thread-1/pr-followups/followup-1/resume",
    { version: PR_FOLLOWUP_VERSION, expected_revision: 7 }, "pr-followup-followup-1-resume-7");
});

it("does not submit when the creation intent cannot be saved", async () => {
  const f = fixture(), user = userEvent.setup();
  show(f);
  vi.spyOn(Storage.prototype, "setItem").mockImplementation(() => { throw new Error("storage unavailable"); });
  await enable(user);
  await screen.findByText(/无法保存本机恢复数据/);
  expect(f.client.postControl).not.toHaveBeenCalled();
});

it("allows correcting settings after an explicit server rejection", async () => {
  const f = fixture(), user = userEvent.setup(), view = show(f);
  f.client.postControl.mockRejectedValueOnce(new APIRequestError("请先核对本地分支", "FAILED_PRECONDITION", 412));
  await enable(user);
  await screen.findByText("请先核对本地分支");
  await user.click(screen.getByRole("button", { name: "重新设置" }));
  expect(view.store().read("thread:thread-1:pr-followup-create", null)).toBeNull();
  expect(screen.getByText("设置持续跟进")).toBeInTheDocument();
  expect(f.client.postControl).toHaveBeenCalledOnce();
});

it("preserves a malformed local intent and prevents a replacement creation", async () => {
  const f = fixture();
  show(f, { seed: (store) => store.write("thread:thread-1:pr-followup-create", { key: "unverified", body: {} }) });
  await screen.findByText(/本机持续跟进请求格式异常/);
  expect(screen.queryByText("设置持续跟进")).not.toBeInTheDocument();
  expect(f.client.postControl).not.toHaveBeenCalled();
});
