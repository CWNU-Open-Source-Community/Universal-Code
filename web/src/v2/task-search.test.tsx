import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act, cleanup, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, describe, expect, it, vi } from "vitest";
import type { APIClient } from "../api/client";
import type { ThreadView, WorkspaceView } from "../api/types";
import { useConnectionStore } from "../state/connection";
import { LocaleProvider } from "../lib/locale";
import { V2Workbench } from "./app";
import { v2QueryKeys } from "./query-keys";

vi.mock("./components/conversation", () => ({
  V2Conversation: ({ threadID, draft, onDraftChange }: { threadID: string; draft: string;
    onDraftChange: (value: string) => void }) => <div data-testid="search-current-task">{threadID}
    <textarea aria-label="当前任务草稿" value={draft} onChange={(event) => onDraftChange(event.target.value)} />
  </div>,
}));

const workspace: WorkspaceView = { id: "search-workspace", name: "Search project", created_at: "2026-10-07T00:00:00Z" };
const current: ThreadView = {
  id: "search-current", protocol_version: "thread.v1", title: "Current task", workspace_id: workspace.id,
  mission_id: "search-mission", active_run_id: "search-run", last_run_id: "search-run", status: "active",
  composer_state: "ready", version: 1, created_at: "2026-10-07T00:00:00Z", updated_at: "2026-10-07T00:00:00Z",
};
const clients: QueryClient[] = [];
const page = (items: ThreadView[] | WorkspaceView[], next_cursor = "") => ({
  items, page: { limit: 100, next_cursor }, requestID: "task-search-fixture",
});
function deferred<T>() {
  let resolve!: (value: T) => void;
  let reject!: (error: unknown) => void;
  const promise = new Promise<T>((fulfill, fail) => { resolve = fulfill; reject = fail; });
  return { promise, resolve, reject };
}
function start(getThreads: (query: { q?: string }, cursor: string, signal: AbortSignal) => unknown) {
  window.history.replaceState({}, "", `#/threads/${current.id}`);
  const getPage = vi.fn((path: string, query: { q?: string }, cursor: string, signal: AbortSignal) =>
    path === "/workspaces" ? Promise.resolve(page([workspace])) : Promise.resolve(getThreads(query, cursor, signal)));
  const client = { hasThreadControl: true, getPage, get: vi.fn(), createThread: vi.fn(),
    submitThreadTurn: vi.fn(), executeRun: vi.fn(), transitionThread: vi.fn() } as unknown as APIClient;
  const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  clients.push(queryClient);
  render(<QueryClientProvider client={queryClient}><LocaleProvider><V2Workbench client={client} /></LocaleProvider></QueryClientProvider>);
  const listCalls = () => getPage.mock.calls.filter(([path]) => path === "/threads");
  return { client, queryClient, listCalls };
}
async function openSearch() {
  await screen.findByRole("button", { name: current.title });
  await userEvent.setup().click(screen.getByRole("button", { name: "搜索" }));
  return screen.getByRole("searchbox", { name: "搜索对话" });
}

beforeEach(() => localStorage.setItem("prayu.locale.v1", "zh-CN"));
afterEach(() => {
  cleanup();
  clients.splice(0).forEach((client) => client.clear());
  useConnectionStore.getState().disconnect();
  vi.restoreAllMocks();
  vi.useRealTimers();
  window.history.replaceState({}, "", "/");
  window.localStorage.clear();
});

describe("V2 task title search", () => {
  it("finds a title older than the first 100 tasks without preloading history or querying each task", async () => {
    const newest = [current, ...Array.from({ length: 99 }, (_, index) => ({ ...current,
      id: `newer-${index}`, title: `Newer task ${index}` }))];
    const older = { ...current, id: "older-101", title: "Older needle task" };
    const { client, listCalls } = start((query) => query.q ? page([older]) : page(newest, "older-page"));
    const input = await openSearch();
    fireEvent.change(input, { target: { value: "needle" } });
    expect(await screen.findByRole("button", { name: older.title })).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: current.title })).not.toBeInTheDocument();
    expect(screen.getByRole("status", { name: "对话列表状态" })).toHaveTextContent("搜索结果：已加载 1 条匹配对话；全部匹配结果已加载");
    expect(listCalls()).toHaveLength(2);
    expect(listCalls()[1]).toEqual(["/threads", { limit: 100, status: "active", q: "needle" }, "", expect.any(AbortSignal)]);
    expect(client.get).not.toHaveBeenCalled();
    expect(screen.getByTestId("search-current-task")).toHaveTextContent(current.id);
    expect(window.location.hash).toBe(`#/threads/${current.id}`);
  });

  it("preserves the task route and draft when results include or exclude it and when search clears or closes", async () => {
    const other = { ...current, id: "other-task", title: "Different task" };
    const { client, listCalls } = start((query) => page(query.q === "Different" ? [other] : [current]));
    const input = await openSearch();
    fireEvent.change(screen.getByRole("textbox", { name: "当前任务草稿" }), { target: { value: "尚未发送的草稿" } });
    const checkCurrent = () => {
      expect(screen.getByRole("textbox", { name: "当前任务草稿" })).toHaveValue("尚未发送的草稿");
      expect(screen.getByTestId("search-current-task")).toHaveTextContent(current.id);
      expect(window.location.hash).toBe(`#/threads/${current.id}`);
    };
    fireEvent.change(input, { target: { value: "Current" } });
    await waitFor(() => expect(listCalls().at(-1)?.[1]).toHaveProperty("q", "Current"));
    await screen.findByRole("button", { name: current.title });
    checkCurrent();
    fireEvent.change(input, { target: { value: "Different" } });
    await screen.findByRole("button", { name: other.title });
    expect(screen.queryByRole("button", { name: current.title })).not.toBeInTheDocument();
    checkCurrent();
    await userEvent.setup().click(screen.getByRole("button", { name: "清除对话搜索" }));
    expect(input).toHaveValue("");
    await screen.findByRole("button", { name: current.title });
    expect(listCalls().at(-1)?.[1]).toEqual({ limit: 100, status: "active" });
    checkCurrent();
    fireEvent.change(input, { target: { value: "Different" } });
    await screen.findByRole("button", { name: other.title });
    await userEvent.setup().click(screen.getByRole("button", { name: "搜索" }));
    expect(screen.queryByRole("searchbox")).not.toBeInTheDocument();
    await screen.findByRole("button", { name: current.title });
    checkCurrent();
    for (const method of [client.createThread, client.submitThreadTurn, client.executeRun, client.transitionThread]) {
      expect(method).not.toHaveBeenCalled();
    }
  });

  it("debounces rapid input and waits for IME composition, preserving Unicode and literal search characters", async () => {
    const { listCalls } = start(() => page([current]));
    const input = await openSearch();
    vi.useFakeTimers();
    fireEvent.change(input, { target: { value: "a" } });
    await act(async () => { vi.advanceTimersByTime(100); });
    fireEvent.change(input, { target: { value: "alpha" } });
    await act(async () => { vi.advanceTimersByTime(249); });
    expect(listCalls()).toHaveLength(1);
    await act(async () => { vi.advanceTimersByTime(1); });
    expect(listCalls().at(-1)?.[1]).toHaveProperty("q", "alpha");
    fireEvent.compositionStart(input);
    fireEvent.change(input, { target: { value: "中" } });
    await act(async () => { vi.advanceTimersByTime(500); });
    expect(listCalls()).toHaveLength(2);
    fireEvent.change(input, { target: { value: "  中文Ä%_  " } });
    fireEvent.compositionEnd(input);
    await act(async () => { vi.advanceTimersByTime(249); });
    expect(listCalls()).toHaveLength(2);
    await act(async () => { vi.advanceTimersByTime(1); });
    expect(listCalls().at(-1)?.[1]).toHaveProperty("q", "中文Ä%_");
  });

  it("does not let a late search response replace a newer query", async () => {
    const oldRequest = deferred<ReturnType<typeof page>>();
    const old = { ...current, id: "old-search", title: "Alpha old result" };
    const next = { ...current, id: "new-search", title: "Beta current result" };
    const { listCalls } = start((query) => query.q === "alpha" ? oldRequest.promise
      : page(query.q ? [next] : [current]));
    const input = await openSearch();
    fireEvent.change(input, { target: { value: "alpha" } });
    await waitFor(() => expect(listCalls()).toHaveLength(2));
    expect(screen.getByRole("status", { name: "对话列表状态" })).toHaveTextContent("正在搜索对话");
    fireEvent.change(input, { target: { value: "beta" } });
    await screen.findByRole("button", { name: next.title });
    expect(listCalls()[1][3].aborted).toBe(true);
    await act(async () => { oldRequest.resolve(page([old])); });
    expect(screen.getByRole("button", { name: next.title })).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: old.title })).not.toBeInTheDocument();
  });

  it("validates 256 Unicode codepoints without cutting emoji and lets an invalid search clear", async () => {
    const { listCalls } = start(() => page([current]));
    const input = await openSearch();
    vi.useFakeTimers();
    const valid = "😀".repeat(256);
    fireEvent.change(input, { target: { value: valid } });
    await act(async () => { vi.advanceTimersByTime(250); });
    expect(listCalls().at(-1)?.[1]).toHaveProperty("q", valid);
    expect(input).toHaveValue(valid);
    expect(input).not.toHaveAttribute("aria-invalid", "true");
    fireEvent.change(input, { target: { value: "😀".repeat(257) } });
    await act(async () => { vi.advanceTimersByTime(500); });
    expect(listCalls()).toHaveLength(2);
    expect(input).toHaveAttribute("aria-invalid", "true");
    expect(screen.getByRole("alert")).toHaveTextContent("标题搜索最多支持 256 个字符");
    fireEvent.click(screen.getByRole("button", { name: "清除对话搜索" }));
    await act(async () => { vi.advanceTimersByTime(1); });
    expect(input).toHaveValue("");
    expect(input).not.toHaveAttribute("aria-invalid", "true");
    expect(screen.queryByText("标题搜索最多支持 256 个字符，请缩短搜索词。")).not.toBeInTheDocument();
    expect(listCalls().at(-1)?.[1]).toEqual({ limit: 100, status: "active" });
  });

  it("pages matching results on demand, deduplicates them, and isolates late load-more responses after changing the query", async () => {
    const first = { ...current, id: "alpha-1", title: "Alpha first" };
    const older = { ...current, id: "alpha-2", title: "Alpha older" };
    const next = { ...current, id: "beta-1", title: "Beta result" };
    const late = deferred<ReturnType<typeof page>>();
    const { listCalls } = start((query, cursor) => {
      if (query.q === "alpha") return cursor === "last-alpha" ? late.promise
        : cursor ? page([first, older], "last-alpha") : page([first], "more-alpha");
      return page(query.q ? [next] : [current]);
    });
    const input = await openSearch();
    fireEvent.change(input, { target: { value: "alpha" } });
    await screen.findByRole("button", { name: first.title });
    expect(listCalls()).toHaveLength(2);
    await userEvent.setup().click(screen.getByRole("button", { name: "加载更多匹配对话" }));
    await screen.findByRole("button", { name: older.title });
    expect(screen.getAllByRole("button", { name: first.title })).toHaveLength(1);
    expect(screen.getByRole("status", { name: "对话列表状态" })).toHaveTextContent("已加载 2 条匹配对话；还有更多匹配结果");
    expect(listCalls().at(-1)?.slice(0, 3)).toEqual(["/threads", { limit: 100, status: "active", q: "alpha" }, "more-alpha"]);
    await userEvent.setup().click(screen.getByRole("button", { name: "加载更多匹配对话" }));
    await screen.findByRole("button", { name: "正在加载更多匹配对话…" });
    fireEvent.change(input, { target: { value: "beta" } });
    await screen.findByRole("button", { name: next.title });
    expect(listCalls().find((call) => call[2] === "last-alpha")?.[3].aborted).toBe(true);
    await act(async () => { late.resolve(page([{ ...older, id: "late-alpha", title: "Late alpha" }])); });
    expect(screen.queryByRole("button", { name: "Late alpha" })).not.toBeInTheDocument();
    expect(screen.getByRole("button", { name: next.title })).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "加载更多匹配对话" })).not.toBeInTheDocument();
  });

  it("retries a failed search page and reports empty results only after a successful search", async () => {
    let failed = false;
    const older = { ...current, id: "retry-result", title: "Needle older" };
    const { listCalls } = start((query, cursor) => {
      if (query.q === "needle" && cursor) {
        if (!failed) { failed = true; return Promise.reject(new Error("search page unavailable")); }
        return page([older]);
      }
      return page(query.q === "empty" ? [] : [current], query.q === "needle" ? "retry-cursor" : "");
    });
    const input = await openSearch();
    fireEvent.change(input, { target: { value: "needle" } });
    await screen.findByRole("button", { name: "加载更多匹配对话" });
    await userEvent.setup().click(screen.getByRole("button", { name: "加载更多匹配对话" }));
    await screen.findByRole("button", { name: "重试加载对话" });
    expect(screen.getByRole("status", { name: "对话列表状态" })).toHaveTextContent("更多记录读取失败");
    expect(screen.queryByText("没有匹配的未归档对话标题")).not.toBeInTheDocument();
    await userEvent.setup().click(screen.getByRole("button", { name: "重试加载对话" }));
    await screen.findByRole("button", { name: older.title });
    expect(listCalls().at(-1)?.[2]).toBe("retry-cursor");
    expect(screen.getByRole("status", { name: "对话列表状态" })).toHaveTextContent("全部匹配结果已加载");
    fireEvent.change(input, { target: { value: "empty" } });
    expect(await screen.findByText("没有匹配的未归档对话标题")).toBeInTheDocument();
    expect(screen.getByRole("status", { name: "对话列表状态" })).toHaveTextContent("已加载 0 条匹配对话；全部匹配结果已加载");
  });

  it("keeps an initial search failure distinct from an empty result and retries the same title", async () => {
    let fail = true;
    const result = { ...current, id: "recovered-search", title: "Recovered needle" };
    const { listCalls } = start((query) => {
      if (query.q && fail) { fail = false; return Promise.reject(new Error("search unavailable")); }
      return page(query.q ? [result] : [current]);
    });
    const input = await openSearch();
    fireEvent.change(input, { target: { value: "needle" } });
    await screen.findByRole("button", { name: "重试加载对话" });
    expect(within(screen.getByRole("complementary")).getByRole("alert")).toHaveTextContent("对话列表加载失败");
    expect(screen.queryByText("没有匹配的未归档对话标题")).not.toBeInTheDocument();
    expect(screen.queryByRole("button", { name: current.title })).not.toBeInTheDocument();
    expect(screen.getByTestId("search-current-task")).toHaveTextContent(current.id);
    await userEvent.setup().click(screen.getByRole("button", { name: "重试加载对话" }));
    await screen.findByRole("button", { name: result.title });
    expect(listCalls().at(-1)?.slice(0, 3)).toEqual(["/threads", { limit: 100, status: "active", q: "needle" }, ""]);
  });

  it("retries first-page refresh failures rather than fetching later pages and hides stale idle status", async () => {
    let failRefresh = false;
    const idle = { ...current, execution_state: "idle" } as ThreadView;
    const { listCalls } = start((_query, cursor) => {
      if (failRefresh) { failRefresh = false; return Promise.reject(new Error("list refresh unavailable")); }
      return page(cursor ? [] : [idle], "more-history");
    });
    await screen.findByRole("button", { name: current.title });
    expect(screen.getByRole("button", { name: current.title })).toHaveAccessibleDescription("空闲");
    failRefresh = true;
    await userEvent.setup().click(screen.getByRole("button", { name: "刷新对话列表" }));
    await screen.findByRole("button", { name: "重试加载对话" });
    expect(screen.getByRole("button", { name: current.title })).toHaveAccessibleDescription("本次列表读取失败，执行状态未知");
    expect(screen.getByRole("button", { name: current.title })).not.toHaveTextContent("空闲");
    await userEvent.setup().click(screen.getByRole("button", { name: "重试加载对话" }));
    await waitFor(() => expect(screen.getByRole("button", { name: current.title })).toHaveAccessibleDescription("空闲"));
    expect(listCalls().at(-1)?.[2]).toBe("");
    expect(screen.getByTestId("search-current-task")).toHaveTextContent(current.id);
  });

  it("invalidates every active title query through the existing list prefix while preserving archive caches", async () => {
    const { queryClient } = start(() => page([current]));
    await screen.findByRole("button", { name: current.title });
    const activeAlpha = v2QueryKeys.threadSearch("active", "alpha");
    const activeBeta = v2QueryKeys.threadSearch("active", "beta");
    const archived = v2QueryKeys.threads("archived");
    queryClient.setQueryData(activeAlpha, { pages: [page([current])], pageParams: [""] });
    queryClient.setQueryData(activeBeta, { pages: [page([current])], pageParams: [""] });
    queryClient.setQueryData(archived, { pages: [page([])], pageParams: [""] });
    await act(async () => { await queryClient.invalidateQueries({ queryKey: v2QueryKeys.threads("active") }); });
    expect(queryClient.getQueryState(activeAlpha)?.isInvalidated).toBe(true);
    expect(queryClient.getQueryState(activeBeta)?.isInvalidated).toBe(true);
    expect(queryClient.getQueryState(archived)?.isInvalidated).toBe(false);
  });

  it.each(["", "task"])("pauses polling across five history pages for query %j while preserving explicit refreshes", async (title) => {
    vi.useFakeTimers();
    let execution_state: ThreadView["execution_state"] = "running";
    const tasks = [current, ...Array.from({ length: 4 }, (_, index) => ({
      ...current, id: `history-${index + 1}`, title: `History task ${index + 1}`,
    }))];
    const { client, queryClient, listCalls } = start((_query, cursor) => {
      const index = cursor ? Number(cursor.slice("history-".length)) : 0;
      return page([{ ...tasks[index], execution_state }], index < tasks.length - 1 ? `history-${index + 1}` : "");
    });
    await act(async () => { await vi.advanceTimersByTimeAsync(1); });
    fireEvent.change(screen.getByRole("textbox", { name: "当前任务草稿" }), { target: { value: "历史浏览中的草稿" } });
    if (title) {
      fireEvent.click(screen.getByRole("button", { name: "搜索" }));
      fireEvent.change(screen.getByRole("searchbox", { name: "搜索对话" }), { target: { value: title } });
      await act(async () => { await vi.advanceTimersByTimeAsync(251); });
      await act(async () => { await vi.advanceTimersByTimeAsync(1); });
    }
    for (let index = 1; index < tasks.length; index++) {
      fireEvent.click(screen.getByRole("button", { name: title ? "加载更多匹配对话" : "加载更早对话" }));
      await act(async () => { await vi.advanceTimersByTimeAsync(1); });
      expect(screen.getByRole("button", { name: tasks[index].title })).toBeInTheDocument();
    }
    const loadedCalls = listCalls().length;
    expect(loadedCalls).toBe(tasks.length + (title ? 1 : 0));
    execution_state = "completed";
    await act(async () => { await vi.advanceTimersByTimeAsync(45_000); });
    expect(listCalls()).toHaveLength(loadedCalls);
    expect(screen.getByRole("button", { name: current.title })).toHaveAccessibleDescription("执行中");

    fireEvent.click(screen.getByRole("button", { name: "刷新对话列表" }));
    await act(async () => { await vi.advanceTimersByTimeAsync(1); });
    const cursors = ["", "history-1", "history-2", "history-3", "history-4"];
    expect(listCalls().slice(loadedCalls).map((call) => call[2])).toEqual(cursors);
    for (const task of tasks) {
      expect(screen.getByRole("button", { name: task.title })).toHaveAccessibleDescription("已完成");
    }
    execution_state = "paused";
    await act(async () => {
      await queryClient.invalidateQueries({ queryKey: v2QueryKeys.threads("active") });
      await vi.advanceTimersByTimeAsync(1);
    });
    expect(listCalls().slice(loadedCalls + tasks.length).map((call) => call[2])).toEqual(cursors);
    for (const task of tasks) {
      expect(screen.getByRole("button", { name: task.title })).toHaveAccessibleDescription("已暂停");
    }
    await act(async () => { await vi.advanceTimersByTimeAsync(45_000); });
    expect(listCalls()).toHaveLength(loadedCalls + tasks.length * 2);
    expect(client.get).not.toHaveBeenCalled();
    expect(screen.getByRole("textbox", { name: "当前任务草稿" })).toHaveValue("历史浏览中的草稿");
    expect(screen.getByTestId("search-current-task")).toHaveTextContent(current.id);
    expect(window.location.hash).toBe(`#/threads/${current.id}`);
  });

  it("resumes single-page search polling and pauses again when restoring cached history", async () => {
    vi.useFakeTimers();
    let execution_state: ThreadView["execution_state"] = "running";
    const older = { ...current, id: "older-history", title: "Older history" };
    const match = { ...current, id: "single-match", title: "Single needle" };
    const { client, listCalls } = start((query, cursor) => query.q
      ? page([{ ...match, execution_state }])
      : page([{ ...(cursor ? older : current), execution_state }], cursor ? "" : "older-page"));
    await act(async () => { await vi.advanceTimersByTimeAsync(1); });
    fireEvent.click(screen.getByRole("button", { name: "加载更早对话" }));
    await act(async () => { await vi.advanceTimersByTimeAsync(45_001); });
    expect(listCalls()).toHaveLength(2);

    fireEvent.click(screen.getByRole("button", { name: "搜索" }));
    fireEvent.change(screen.getByRole("searchbox", { name: "搜索对话" }), { target: { value: "needle" } });
    await act(async () => { await vi.advanceTimersByTimeAsync(251); });
    await act(async () => { await vi.advanceTimersByTimeAsync(1); });
    expect(listCalls()).toHaveLength(3);
    expect(screen.getByRole("button", { name: match.title })).toHaveAccessibleDescription("执行中");
    execution_state = "completed";
    await act(async () => { await vi.advanceTimersByTimeAsync(15_000); });
    expect(listCalls()).toHaveLength(4);
    expect(listCalls().at(-1)?.slice(0, 3)).toEqual(["/threads", { limit: 100, status: "active", q: "needle" }, ""]);
    expect(screen.getByRole("button", { name: match.title })).toHaveAccessibleDescription("已完成");

    fireEvent.click(screen.getByRole("button", { name: "清除对话搜索" }));
    await act(async () => { await vi.advanceTimersByTimeAsync(1); });
    expect(listCalls().slice(4).map((call) => call[2])).toEqual(["", "older-page"]);
    expect(screen.getByRole("button", { name: older.title })).toBeInTheDocument();
    await act(async () => { await vi.advanceTimersByTimeAsync(45_000); });
    expect(listCalls()).toHaveLength(6);
    expect(client.get).not.toHaveBeenCalled();
    expect(screen.getByTestId("search-current-task")).toHaveTextContent(current.id);
  });

  it("refreshes all visible rows with one list request and stops periodic reads when the sidebar is hidden", async () => {
    vi.useFakeTimers();
    let execution_state: ThreadView["execution_state"] = "running";
    const { client, listCalls } = start(() => page([{ ...current, execution_state }, ...Array.from({ length: 20 }, (_, index) => ({
      ...current, id: `state-${index}`, title: `State ${index}`,
    }))]));
    await act(async () => { await vi.advanceTimersByTimeAsync(1); });
    expect(screen.getByRole("button", { name: current.title })).toHaveAccessibleDescription("执行中");
    execution_state = "completed";
    await act(async () => { await vi.advanceTimersByTimeAsync(15_000); });
    expect(listCalls()).toHaveLength(2);
    expect(screen.getByRole("button", { name: current.title })).toHaveAccessibleDescription("已完成");
    expect(client.get).not.toHaveBeenCalled();
    fireEvent.click(screen.getByRole("button", { name: "隐藏侧栏" }));
    await act(async () => { await vi.advanceTimersByTimeAsync(45_000); });
    expect(listCalls()).toHaveLength(2);
    expect(client.get).not.toHaveBeenCalled();
  });
});
