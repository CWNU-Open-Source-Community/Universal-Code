import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act, cleanup, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, describe, expect, it, vi } from "vitest";
import type { APIClient } from "../api/client";
import type { ThreadSearchResultView, ThreadView, WorkspaceView } from "../api/types";
import { useConnectionStore } from "../state/connection";
import { LocaleProvider } from "../lib/locale";
import { V2Workbench } from "./app";
import { v2QueryKeys } from "./query-keys";

vi.mock("./components/conversation", () => ({
  V2Conversation: ({ threadID, messageID, draft, onDraftChange }: { threadID: string; messageID?: string; draft: string;
    onDraftChange: (value: string) => void }) => <div data-testid="search-current-task" data-message-id={messageID}>{threadID}
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
const page = (items: ThreadView[] | WorkspaceView[] | ThreadSearchResultView[], next_cursor = "") => ({
  items, page: { limit: 100, next_cursor }, requestID: "task-search-fixture",
});
function deferred<T>() {
  let resolve!: (value: T) => void;
  let reject!: (error: unknown) => void;
  const promise = new Promise<T>((fulfill, fail) => { resolve = fulfill; reject = fail; });
  return { promise, resolve, reject };
}
type SearchQuery = { q?: string; include_archived?: boolean; thread_id?: string };
function start(getThreads: (query: SearchQuery, cursor: string, signal: AbortSignal) => unknown) {
  window.history.replaceState({}, "", `#/threads/${current.id}`);
  const getPage = vi.fn(async (path: string, query: SearchQuery & { pinned?: boolean }, cursor: string, signal: AbortSignal) => {
    if (path === "/workspaces") return page([workspace]);
    if (path === "/threads" && query.pinned) return page([]);
    const result = await getThreads(query, cursor, signal) as ReturnType<typeof page>;
    if (path !== "/threads/search") return result;
    return { ...result, items: result.items.map((value) => {
      if ("thread_id" in value) return value;
      const thread = value as ThreadView;
      return { id: thread.id, thread_id: thread.id, thread_title: thread.title, thread_status: thread.status,
        workspace_id: thread.workspace_id ?? "", role: "title", snippet: thread.execution_state ?? thread.title,
        created_at: thread.created_at } satisfies ThreadSearchResultView;
    }) };
  });
  const client = { hasThreadControl: true, getPage, get: vi.fn(), createThread: vi.fn(),
    submitThreadTurn: vi.fn(), executeRun: vi.fn(), transitionThread: vi.fn() } as unknown as APIClient;
  const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  clients.push(queryClient);
  render(<QueryClientProvider client={queryClient}><LocaleProvider><V2Workbench client={client} /></LocaleProvider></QueryClientProvider>);
  const listCalls = () => getPage.mock.calls.filter(([path, query]) =>
    (path === "/threads" && !query.pinned) || path === "/threads/search");
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
  vi.unstubAllGlobals();
  vi.useRealTimers();
  window.history.replaceState({}, "", "/");
  window.localStorage.clear();
});

describe("V2 saved conversation search", () => {
  it("limits search to the current chat and locates an older saved message without changing the draft", async () => {
    const result: ThreadSearchResultView = { id: "current-old-message", message_id: "current-old-message", thread_id: current.id,
      thread_title: current.title, workspace_id: workspace.id, thread_status: "active", role: "assistant",
      snippet: "The earlier run saved the needle here.", created_at: "2026-10-01T04:00:00Z" };
    const other = { ...current, id: "other-chat", title: "Other matching chat" };
    const { client, listCalls } = start((query) => page(query.q ? query.thread_id ? [result] : [other] : [current]));
    const input = await openSearch();
    fireEvent.change(screen.getByRole("textbox", { name: "当前任务草稿" }), { target: { value: "未发送的草稿" } });
    fireEvent.change(input, { target: { value: "needle" } });
    await screen.findByRole("button", { name: other.title });
    await userEvent.click(screen.getByRole("button", { name: "当前聊天" }));
    await screen.findByRole("button", { name: current.title });
    expect(screen.queryByRole("button", { name: other.title })).not.toBeInTheDocument();
    expect(input).toHaveAccessibleDescription(/包括尚未加载的历史/);
    expect(listCalls().at(-1)?.slice(0, 3)).toEqual(["/threads/search", {
      q: "needle", include_archived: false, thread_id: current.id, limit: 50 }, ""]);
    await userEvent.click(screen.getByRole("checkbox", { name: "包含已归档对话" }));
    await waitFor(() => expect(listCalls().at(-1)?.[1]).toMatchObject({ thread_id: current.id, include_archived: true }));
    await userEvent.click(await screen.findByRole("button", { name: current.title }));
    expect(window.location.hash).toBe(`#/threads/${current.id}/messages/${result.message_id}`);
    expect(screen.getByTestId("search-current-task")).toHaveAttribute("data-message-id", result.message_id);
    expect(screen.getByRole("textbox", { name: "当前任务草稿" })).toHaveValue("未发送的草稿");
    for (const method of [client.createThread, client.submitThreadTurn, client.executeRun, client.transitionThread]) expect(method).not.toHaveBeenCalled();
  });

  it("isolates late search and load-more responses when changing the scope or current chat", async () => {
    const global = deferred<ReturnType<typeof page>>();
    const older = deferred<ReturnType<typeof page>>();
    const other = { ...current, id: "other-chat", title: "Other chat result" };
    const late = { ...current, id: "late-page", title: "Late result from previous chat" };
    const { listCalls } = start((query, cursor) => !query.q ? page([current, other]) : !query.thread_id ? global.promise
      : query.thread_id === current.id ? cursor ? older.promise : page([current], "older-current") : page([other]));
    const input = await openSearch();
    fireEvent.change(input, { target: { value: "needle" } });
    await waitFor(() => expect(listCalls()).toHaveLength(2));
    await userEvent.click(screen.getByRole("button", { name: "当前聊天" }));
    await screen.findByRole("button", { name: current.title });
    expect(listCalls()[1][3].aborted).toBe(true);
    await act(async () => { global.resolve(page([other])); });
    expect(screen.queryByRole("button", { name: other.title })).not.toBeInTheDocument();
    await userEvent.click(screen.getByRole("button", { name: "加载更多匹配记录" }));
    await screen.findByRole("button", { name: "正在加载更多匹配记录…" });
    act(() => { window.location.hash = `#/threads/${other.id}`; window.dispatchEvent(new HashChangeEvent("hashchange")); });
    await screen.findByRole("button", { name: other.title });
    expect(listCalls().at(-1)?.slice(0, 3)).toEqual(["/threads/search", {
      q: "needle", include_archived: false, thread_id: other.id, limit: 50 }, ""]);
    expect(listCalls().find((call) => call[2] === "older-current")?.[3].aborted).toBe(true);
    await act(async () => { older.resolve(page([late])); });
    expect(screen.queryByRole("button", { name: late.title })).not.toBeInTheDocument();
    expect(screen.queryByRole("button", { name: current.title })).not.toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "加载更多匹配记录" })).not.toBeInTheDocument();
    await userEvent.click(screen.getByRole("button", { name: "所有聊天" }));
    await waitFor(() => expect(listCalls().at(-1)?.[1]).not.toHaveProperty("thread_id"));
    expect(listCalls().at(-1)?.[2]).toBe("");
  });

  it("opens current-chat search with Ctrl or Cmd F, retains global search, and leaves native find available without a chat", async () => {
    const { listCalls } = start(() => page([current]));
    await screen.findByRole("button", { name: current.title });
    fireEvent.keyDown(window, { key: "f", code: "KeyF", ctrlKey: true });
    const input = await screen.findByRole("searchbox", { name: "搜索对话" });
    expect(screen.getByRole("button", { name: "当前聊天" })).toHaveAttribute("aria-pressed", "true");
    fireEvent.change(input, { target: { value: "needle" } });
    await waitFor(() => expect(listCalls().at(-1)?.[1]).toHaveProperty("thread_id", current.id));
    fireEvent.keyDown(window, { key: "k", code: "KeyK", ctrlKey: true });
    await waitFor(() => expect(listCalls().at(-1)?.[1]).not.toHaveProperty("thread_id"));
    expect(screen.getByRole("button", { name: "所有聊天" })).toHaveAttribute("aria-pressed", "true");
    fireEvent.keyDown(window, { key: "f", code: "KeyF", metaKey: true });
    await waitFor(() => expect(input).toHaveFocus());
    expect(screen.getByRole("button", { name: "当前聊天" })).toHaveAttribute("aria-pressed", "true");
    await userEvent.click(screen.getByRole("button", { name: "新对话" }));
    const nativeFind = new KeyboardEvent("keydown", { key: "f", code: "KeyF", ctrlKey: true, cancelable: true });
    fireEvent(window, nativeFind);
    expect(nativeFind.defaultPrevented).toBe(false);
    expect(screen.getByRole("button", { name: "当前聊天" })).toBeDisabled();
  });

  it("focuses the saved query after reopening an inert narrow sidebar", async () => {
    vi.stubGlobal("matchMedia", vi.fn((query: string) => ({
      matches: query === "(max-width: 720px)", media: query,
      addEventListener: vi.fn(), removeEventListener: vi.fn(),
    })));
    start(() => page([current]));
    await screen.findByTestId("search-current-task");
    fireEvent.keyDown(window, { key: "f", code: "KeyF", ctrlKey: true });
    const input = await screen.findByRole("searchbox", { name: "搜索对话" });
    fireEvent.change(input, { target: { value: "needle" } });
    await userEvent.click(screen.getByRole("button", { name: "关闭侧栏" }));
    expect(input.closest(".v2-sidebar-slot")).toHaveAttribute("inert");
    const focus = vi.spyOn(input, "focus").mockImplementation(() => {
      expect(input.closest(".v2-sidebar-slot")).not.toHaveAttribute("inert");
      HTMLElement.prototype.focus.call(input);
    });
    fireEvent.keyDown(window, { key: "f", code: "KeyF", ctrlKey: true });
    await waitFor(() => expect(input).toHaveFocus());
    expect(focus).toHaveBeenCalled();
    expect(input).toHaveValue("needle");
    expect(screen.getByRole("button", { name: "当前聊天" })).toHaveAttribute("aria-pressed", "true");
  });

  it("shows saved message snippets and time, restarts archive pagination, and opens the exact archived message without a write", async () => {
    const result: ThreadSearchResultView = { id: "older-message", message_id: "older-message", thread_id: "archived-thread",
      thread_title: "Archived conversation", workspace_id: workspace.id, thread_status: "archived", role: "assistant",
      snippet: "The saved answer contains the needle.", created_at: "2026-10-01T04:00:00Z" };
    const { client, listCalls } = start((query) => query.q
      ? page(query.include_archived ? [result] : [], query.include_archived ? "more-archived" : "") : page([current]));
    const input = await openSearch();
    fireEvent.change(input, { target: { value: "needle" } });
    await screen.findByText("没有匹配的标题或消息");
    await userEvent.click(screen.getByRole("checkbox", { name: "包含已归档对话" }));
    const match = await screen.findByRole("button", { name: result.thread_title });
    expect(match).toHaveAccessibleDescription(result.snippet);
    expect(within(match).getByText("已归档 · 助手")).toBeInTheDocument();
    expect(match.querySelector("time")).toHaveAttribute("datetime", result.created_at);
    expect(listCalls().at(-1)?.slice(0, 3)).toEqual(["/threads/search", { q: "needle", include_archived: true, limit: 50 }, ""]);
    await userEvent.click(match);
    expect(window.location.hash).toBe("#/threads/archived-thread/messages/older-message");
    expect(screen.getByTestId("search-current-task")).toHaveAttribute("data-message-id", "older-message");
    for (const method of [client.createThread, client.submitThreadTurn, client.executeRun, client.transitionThread]) expect(method).not.toHaveBeenCalled();
  });
  it("finds a title older than the first 100 tasks without preloading history or querying each task", async () => {
    const newest = [current, ...Array.from({ length: 99 }, (_, index) => ({ ...current,
      id: `newer-${index}`, title: `Newer task ${index}` }))];
    const older = { ...current, id: "older-101", title: "Older needle task" };
    const { client, listCalls } = start((query) => query.q ? page([older]) : page(newest, "older-page"));
    const input = await openSearch();
    fireEvent.change(input, { target: { value: "needle" } });
    expect(await screen.findByRole("button", { name: older.title })).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: current.title })).not.toBeInTheDocument();
    expect(screen.getByRole("status", { name: "对话列表状态" })).toHaveTextContent("搜索结果：已加载 1 条匹配记录；全部匹配结果已加载");
    expect(listCalls()).toHaveLength(2);
    expect(listCalls()[1]).toEqual(["/threads/search", { limit: 50, include_archived: false, q: "needle" }, "", expect.any(AbortSignal)]);
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
    expect(screen.getByRole("region", { name: `项目 ${workspace.name}` })).toContainElement(
      screen.getByRole("button", { name: current.title }));
    expect(screen.queryByText(/搜索全部已保存/)).not.toBeInTheDocument();
    expect(screen.queryByRole("checkbox", { name: "包含已归档对话" })).not.toBeInTheDocument();
    expect(screen.queryByRole("button", { name: other.title })).not.toBeInTheDocument();
    expect(screen.getByRole("status", { name: "对话列表状态" })).not.toHaveTextContent("搜索结果");
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
    expect(screen.getByRole("alert")).toHaveTextContent("搜索最多支持 256 个字符");
    fireEvent.click(screen.getByRole("button", { name: "清除对话搜索" }));
    await act(async () => { vi.advanceTimersByTime(1); });
    expect(input).toHaveValue("");
    expect(input).not.toHaveAttribute("aria-invalid", "true");
    expect(screen.queryByText("搜索最多支持 256 个字符，请缩短搜索词。")).not.toBeInTheDocument();
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
    await userEvent.setup().click(screen.getByRole("button", { name: "加载更多匹配记录" }));
    await screen.findByRole("button", { name: older.title });
    expect(screen.getAllByRole("button", { name: first.title })).toHaveLength(1);
    expect(screen.getByRole("status", { name: "对话列表状态" })).toHaveTextContent("已加载 2 条匹配记录；还有更多匹配结果");
    expect(listCalls().at(-1)?.slice(0, 3)).toEqual(["/threads/search", { limit: 50, include_archived: false, q: "alpha" }, "more-alpha"]);
    await userEvent.setup().click(screen.getByRole("button", { name: "加载更多匹配记录" }));
    await screen.findByRole("button", { name: "正在加载更多匹配记录…" });
    fireEvent.change(input, { target: { value: "beta" } });
    await screen.findByRole("button", { name: next.title });
    expect(listCalls().find((call) => call[2] === "last-alpha")?.[3].aborted).toBe(true);
    await act(async () => { late.resolve(page([{ ...older, id: "late-alpha", title: "Late alpha" }])); });
    expect(screen.queryByRole("button", { name: "Late alpha" })).not.toBeInTheDocument();
    expect(screen.getByRole("button", { name: next.title })).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "加载更多匹配记录" })).not.toBeInTheDocument();
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
    await screen.findByRole("button", { name: "加载更多匹配记录" });
    await userEvent.setup().click(screen.getByRole("button", { name: "加载更多匹配记录" }));
    await screen.findByRole("button", { name: "重试加载对话" });
    expect(screen.getByRole("status", { name: "对话列表状态" })).toHaveTextContent("更多记录读取失败");
    expect(screen.queryByText("没有匹配的标题或消息")).not.toBeInTheDocument();
    await userEvent.setup().click(screen.getByRole("button", { name: "重试加载对话" }));
    await screen.findByRole("button", { name: older.title });
    expect(listCalls().at(-1)?.[2]).toBe("retry-cursor");
    expect(screen.getByRole("status", { name: "对话列表状态" })).toHaveTextContent("全部匹配结果已加载");
    fireEvent.change(input, { target: { value: "empty" } });
    expect(await screen.findByText("没有匹配的标题或消息")).toBeInTheDocument();
    expect(screen.getByRole("status", { name: "对话列表状态" })).toHaveTextContent("已加载 0 条匹配记录；全部匹配结果已加载");
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
    expect(screen.queryByText("没有匹配的标题或消息")).not.toBeInTheDocument();
    expect(screen.queryByRole("button", { name: current.title })).not.toBeInTheDocument();
    expect(screen.getByTestId("search-current-task")).toHaveTextContent(current.id);
    await userEvent.setup().click(screen.getByRole("button", { name: "重试加载对话" }));
    await screen.findByRole("button", { name: result.title });
    expect(listCalls().at(-1)?.slice(0, 3)).toEqual(["/threads/search", { limit: 50, include_archived: false, q: "needle" }, ""]);
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
    const activeAlpha = ["v2", "threads", "active", "messages", { q: "alpha", include_archived: false, thread_id: "" }];
    const activeBeta = ["v2", "threads", "active", "messages", { q: "beta", include_archived: false, thread_id: current.id }];
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
      fireEvent.click(screen.getByRole("button", { name: title ? "加载更多匹配记录" : "加载更早对话" }));
      await act(async () => { await vi.advanceTimersByTimeAsync(1); });
      expect(screen.getByRole("button", { name: tasks[index].title })).toBeInTheDocument();
    }
    const loadedCalls = listCalls().length;
    expect(loadedCalls).toBe(tasks.length + (title ? 1 : 0));
    execution_state = "completed";
    await act(async () => { await vi.advanceTimersByTimeAsync(45_000); });
    expect(listCalls()).toHaveLength(loadedCalls);
    expect(screen.getByRole("button", { name: current.title })).toHaveAccessibleDescription(title ? "running" : "执行中");

    fireEvent.click(screen.getByRole("button", { name: "刷新对话列表" }));
    await act(async () => { await vi.advanceTimersByTimeAsync(1); });
    const cursors = ["", "history-1", "history-2", "history-3", "history-4"];
    expect(listCalls().slice(loadedCalls).map((call) => call[2])).toEqual(cursors);
    for (const task of tasks) {
      expect(screen.getByRole("button", { name: task.title })).toHaveAccessibleDescription(title ? "completed" : "已完成");
    }
    execution_state = "paused";
    await act(async () => {
      await queryClient.invalidateQueries({ queryKey: v2QueryKeys.threads("active") });
      await vi.advanceTimersByTimeAsync(1);
    });
    expect(listCalls().slice(loadedCalls + tasks.length).map((call) => call[2])).toEqual(cursors);
    for (const task of tasks) {
      expect(screen.getByRole("button", { name: task.title })).toHaveAccessibleDescription(title ? "paused" : "已暂停");
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
    expect(screen.getByRole("button", { name: match.title })).toHaveAccessibleDescription("running");
    execution_state = "completed";
    await act(async () => { await vi.advanceTimersByTimeAsync(15_000); });
    expect(listCalls()).toHaveLength(4);
    expect(listCalls().at(-1)?.slice(0, 3)).toEqual(["/threads/search", { limit: 50, include_archived: false, q: "needle" }, ""]);
    expect(screen.getByRole("button", { name: match.title })).toHaveAccessibleDescription("completed");

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
