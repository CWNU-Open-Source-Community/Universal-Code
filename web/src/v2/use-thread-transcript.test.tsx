import { QueryClient, QueryClientProvider, useInfiniteQuery } from "@tanstack/react-query";
import { act, cleanup, renderHook, waitFor } from "@testing-library/react";
import type { ReactNode } from "react";
import { afterEach, expect, it, vi } from "vitest";
import type { APIClient } from "../api/client";
import type { PageResult, ThreadDetailView, ThreadTranscriptItemView } from "../api/types";
import { v2QueryKeys } from "./query-keys";
import { useV2ThreadTranscript } from "./use-thread-transcript";
import { peerMessageFixture } from "../test/thread-peer-message";

afterEach(() => { cleanup(); vi.useRealTimers(); });

function item(sequence: number, patch: Partial<ThreadTranscriptItemView> = {}): ThreadTranscriptItemView {
  return { version: "thread_transcript.v1", id: `event-${sequence}`, canonical_id: `canonical-${sequence}`,
    run_id: "run-a", run_ordinal: 1, sequence, created_at: new Date(sequence * 1_000).toISOString(),
    source: "harness", kind: "harness_status", activity_type: "message", stage: "result",
    title: `Record ${sequence}`, durable: true, provisional: false, instruction_authorized: false, verifiable: true,
    ...patch };
}
function page(items: ThreadTranscriptItemView[], next = ""): PageResult<ThreadTranscriptItemView> {
  return { items, page: { limit: 100, ...(next ? { next_cursor: next } : {}) }, requestID: "fixture" };
}
function deferred<T>() {
  let resolve!: (value: T) => void;
  let reject!: (reason: Error) => void;
  const promise = new Promise<T>((finish, fail) => { resolve = finish; reject = fail; });
  return { promise, resolve, reject };
}
function setup(total = 1_100) {
  let records = Array.from({ length: total }, (_, index) => item(index + 1));
  let hidden = new Set<number>();
  const getPage = vi.fn((_path: string, _query: unknown, cursor = "") => {
    const before = cursor ? Number(cursor.replace("before-", "")) : Infinity;
    const source = records.filter((entry) => entry.sequence < before).slice(-100);
    const more = records.some((entry) => entry.sequence < (source[0]?.sequence ?? 0));
    return Promise.resolve(page(source.filter((entry) => !hidden.has(entry.sequence)), more ? `before-${source[0].sequence}` : ""));
  });
  const client = { getPage } as unknown as APIClient;
  const queries = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  const wrapper = ({ children }: { children: ReactNode }) => <QueryClientProvider client={queries}>{children}</QueryClientProvider>;
  return { client, queries, wrapper, getPage,
    append: (count: number) => { const start = records.length; records = [...records, ...Array.from({ length: count }, (_, index) => item(start + index + 1))]; },
    update: (sequence: number, patch: Partial<ThreadTranscriptItemView>) => { records = records.map((entry) => entry.sequence === sequence ? { ...entry, ...patch } : entry); },
    hide: (sequences: number[]) => { hidden = new Set(sequences); },
  };
}

it("measures ten historical requests on the previous infinite-query refresh", async () => {
  const fixture = setup();
  const view = renderHook(() => useInfiniteQuery({ queryKey: ["old-transcript"],
    initialPageParam: "", getNextPageParam: (result: PageResult<ThreadTranscriptItemView>) => result.page.next_cursor || undefined,
    queryFn: ({ pageParam, signal }) => fixture.client.getPage<ThreadTranscriptItemView>("/threads/thread-a/transcript", { limit: 100 }, pageParam, signal),
  }), { wrapper: fixture.wrapper });
  await waitFor(() => expect(view.result.current.data?.pages).toHaveLength(1));
  for (let index = 1; index < 10; index++) await act(async () => { await view.result.current.fetchNextPage(); });
  expect(fixture.getPage).toHaveBeenCalledTimes(10);
  fixture.append(1);
  fixture.getPage.mockClear();
  await act(async () => { await view.result.current.refetch(); });
  expect(fixture.getPage).toHaveBeenCalledTimes(10);
  view.unmount(); fixture.queries.clear();
});

it("refreshes only the newest page after loading ten pages and retains the historical frontier", async () => {
  const fixture = setup();
  const view = renderHook(() => useV2ThreadTranscript(fixture.client, "thread-a"), { wrapper: fixture.wrapper });
  await waitFor(() => expect(view.result.current.items).toHaveLength(100));
  for (let index = 1; index < 10; index++) await act(async () => { await view.result.current.fetchNextPage(); });
  await waitFor(() => expect(view.result.current.data?.pages).toHaveLength(10));
  expect(view.result.current.items).toHaveLength(1_000);
  const historyKey = [...v2QueryKeys.transcript("thread-a"), "history"];
  const history = fixture.queries.getQueryData(historyKey);
  fixture.append(1);
  fixture.getPage.mockClear();
  await act(async () => { await fixture.queries.invalidateQueries({ queryKey: v2QueryKeys.transcript("thread-a") }, { cancelRefetch: false }); });
  expect(fixture.getPage).toHaveBeenCalledTimes(1);
  expect(fixture.getPage).toHaveBeenCalledWith("/threads/thread-a/transcript", { limit: 100 }, "", expect.any(AbortSignal));
  expect(fixture.queries.getQueryData(historyKey)).toBe(history);
  await waitFor(() => expect(view.result.current.items).toHaveLength(1_001));
  expect(view.result.current.items.map((entry) => entry.sequence)).toEqual(Array.from({ length: 1_001 }, (_, index) => index + 101));
  expect(view.result.current.data?.pages).toHaveLength(10);
  fixture.getPage.mockClear();
  await act(async () => { await view.result.current.fetchNextPage(); });
  expect(fixture.getPage).toHaveBeenCalledWith("/threads/thread-a/transcript", { limit: 100 }, "before-101", expect.any(AbortSignal));
  await waitFor(() => expect(view.result.current.items).toHaveLength(1_101));
  expect(view.result.current.hasNextPage).toBe(false);
  view.unmount(); fixture.queries.clear();
});

it("fills a multi-page live gap before joining cached records without moving the older-history cursor", async () => {
  const fixture = setup(200);
  const view = renderHook(() => useV2ThreadTranscript(fixture.client, "thread-a"), { wrapper: fixture.wrapper });
  await waitFor(() => expect(view.result.current.items).toHaveLength(100));
  fixture.append(350);
  fixture.getPage.mockClear();
  await act(async () => { await view.result.current.refetch(); });
  expect(fixture.getPage.mock.calls.map((call) => call[2])).toEqual(["", "before-451", "before-351", "before-251"]);
  await waitFor(() => expect(view.result.current.items).toHaveLength(450));
  expect(view.result.current.items.map((entry) => entry.sequence)).toEqual(Array.from({ length: 450 }, (_, index) => index + 101));
  expect(new Set(view.result.current.items.map((entry) => entry.id)).size).toBe(450);
  await act(async () => { await view.result.current.fetchNextPage(); });
  expect(fixture.getPage.mock.calls.at(-1)?.[2]).toBe("before-101");
  await waitFor(() => expect(view.result.current.items).toHaveLength(550));
  view.unmount(); fixture.queries.clear();
});

it("bridges non-projected source pages instead of treating an empty page as the end of a live gap", async () => {
  const fixture = setup(200);
  const view = renderHook(() => useV2ThreadTranscript(fixture.client, "thread-a"), { wrapper: fixture.wrapper });
  await waitFor(() => expect(view.result.current.items).toHaveLength(100));
  fixture.append(500);
  fixture.hide(Array.from({ length: 400 }, (_, index) => index + 201));
  fixture.getPage.mockClear();
  await act(async () => { await view.result.current.refetch(); });
  expect(fixture.getPage).toHaveBeenCalledTimes(6);
  await waitFor(() => expect(view.result.current.items).toHaveLength(200));
  expect(view.result.current.items.map((entry) => entry.sequence)).toEqual([
    ...Array.from({ length: 100 }, (_, index) => index + 101), ...Array.from({ length: 100 }, (_, index) => index + 601),
  ]);
  await act(async () => { await view.result.current.fetchNextPage(); });
  await waitFor(() => expect(view.result.current.items).toHaveLength(300));
  view.unmount(); fixture.queries.clear();
});

it("replaces a same-identity projection and preserves its actual source and authority flags", async () => {
  const fixture = setup(200);
  fixture.update(190, { kind: "operator_input", source: "operator", source_ref: "queued-190", status: "pending", instruction_authorized: true });
  const view = renderHook(() => useV2ThreadTranscript(fixture.client, "thread-a"), { wrapper: fixture.wrapper });
  await waitFor(() => expect(view.result.current.items).toHaveLength(100));
  await act(async () => { await view.result.current.fetchNextPage(); });
  await waitFor(() => expect(view.result.current.items).toHaveLength(200));
  const unchanged = view.result.current.items.find((entry) => entry.sequence === 189);
  fixture.update(190, { status: "cancelled", stage: "blocked", instruction_authorized: false, promoted_to_message_id: "replacement-190" });
  await act(async () => { await view.result.current.refetch(); });
  await waitFor(() => expect(view.result.current.items.find((entry) => entry.sequence === 190)).toMatchObject({
    id: "event-190", canonical_id: "canonical-190", source_ref: "queued-190", source: "operator", kind: "operator_input",
    status: "cancelled", instruction_authorized: false, durable: true, provisional: false, promoted_to_message_id: "replacement-190",
  }));
  expect(view.result.current.items.find((entry) => entry.sequence === 189)).toBe(unchanged);
  expect(view.result.current.items).toHaveLength(200);
  view.unmount(); fixture.queries.clear();
});

it("refreshes delivery status after a queued peer message moves off the latest transcript page", async () => {
  const fixture = setup(200);
  const message = peerMessageFixture();
  fixture.update(190, { source: "peer", kind: "peer_message", peer_message: message });
  const view = renderHook(() => useV2ThreadTranscript(fixture.client, "thread-b"), { wrapper: fixture.wrapper });
  await waitFor(() => expect(view.result.current.items).toHaveLength(100));
  fixture.append(120);
  await act(async () => { await view.result.current.refetch(); });
  fixture.update(190, { peer_message: { ...message, status: "delivered", delivered_at: "2026-10-19T09:00:01Z" } });
  await act(async () => { await view.result.current.refetch(); });
  await waitFor(() => expect(view.result.current.items.find((entry) => entry.sequence === 190)?.peer_message?.status).toBe("delivered"));
  view.unmount(); fixture.queries.clear();
});

it("orders Run succession and tool positions while retaining separate source IDs with one canonical ID", async () => {
  const fixture = setup(0);
  fixture.getPage.mockResolvedValueOnce(page([
    item(900, { id: "older-run", canonical_id: "same-tool", run_ordinal: 1 }),
    item(1, { id: "tool-first", canonical_id: "same-tool", run_id: "run-b", run_ordinal: 2, position: 1 }),
    item(1, { id: "tool-second", canonical_id: "same-tool", run_id: "run-b", run_ordinal: 2, position: 2 }),
  ], "older"));
  const view = renderHook(() => useV2ThreadTranscript(fixture.client, "thread-a"), { wrapper: fixture.wrapper });
  await waitFor(() => expect(view.result.current.items).toHaveLength(3));
  fixture.getPage.mockResolvedValueOnce(page([item(899), item(900, { id: "older-run", canonical_id: "same-tool" })]));
  await act(async () => { await view.result.current.fetchNextPage(); });
  await waitFor(() => expect(view.result.current.items).toHaveLength(4));
  expect(view.result.current.items.map((entry) => entry.id)).toEqual(["event-899", "older-run", "tool-first", "tool-second"]);
  view.unmount(); fixture.queries.clear();
});

it("keeps historical pages across task navigation and syncs only the head when returning", async () => {
  const fixture = setup();
  const original = fixture.getPage.getMockImplementation()!;
  fixture.getPage.mockImplementation((path, query, cursor) => path.includes("thread-b")
    ? Promise.resolve(page([item(1, { id: "task-b", run_id: "run-b" })])) : original(path, query, cursor));
  const view = renderHook(({ threadID }) => useV2ThreadTranscript(fixture.client, threadID), {
    wrapper: fixture.wrapper, initialProps: { threadID: "thread-a" },
  });
  await waitFor(() => expect(view.result.current.items).toHaveLength(100));
  for (let index = 1; index < 10; index++) await act(async () => { await view.result.current.fetchNextPage(); });
  view.rerender({ threadID: "thread-b" });
  await waitFor(() => expect(view.result.current.items.map((entry) => entry.id)).toEqual(["task-b"]));
  fixture.append(1);
  fixture.getPage.mockClear();
  view.rerender({ threadID: "thread-a" });
  await waitFor(() => expect(view.result.current.items).toHaveLength(1_001));
  expect(fixture.getPage).toHaveBeenCalledTimes(1);
  expect(view.result.current.data?.pages).toHaveLength(10);
  view.unmount(); fixture.queries.clear();
});

it("retains cached records on a failed sync and permits read retry without discarding history", async () => {
  const fixture = setup(200);
  const view = renderHook(() => useV2ThreadTranscript(fixture.client, "thread-a"), { wrapper: fixture.wrapper });
  await waitFor(() => expect(view.result.current.items).toHaveLength(100));
  await act(async () => { await view.result.current.fetchNextPage(); });
  await waitFor(() => expect(view.result.current.items).toHaveLength(200));
  const records = view.result.current.items;
  fixture.getPage.mockRejectedValueOnce(new Error("connection interrupted"));
  await act(async () => { await view.result.current.refetch(); });
  await waitFor(() => expect(view.result.current.isError).toBe(true));
  expect(view.result.current.items).toBe(records);
  fixture.append(1);
  await act(async () => { await view.result.current.refetch(); });
  await waitFor(() => expect(view.result.current.isError).toBe(false));
  expect(view.result.current.items).toHaveLength(201);
  view.unmount(); fixture.queries.clear();
});

it("deduplicates fallback and event sync while allowing terminal synchronization after polling stops", async () => {
  const fixture = setup(200);
  const view = renderHook(({ interval }) => useV2ThreadTranscript(fixture.client, "thread-a", { refetchInterval: interval }), {
    wrapper: fixture.wrapper, initialProps: { interval: false as number | false },
  });
  await waitFor(() => expect(view.result.current.items).toHaveLength(100));
  const pending = deferred<PageResult<ThreadTranscriptItemView>>();
  fixture.getPage.mockResolvedValueOnce(page([item(201)], "before-201"));
  fixture.getPage.mockImplementationOnce(() => pending.promise);
  fixture.append(1);
  fixture.getPage.mockClear();
  vi.useFakeTimers();
  view.rerender({ interval: 1_000 });
  await act(async () => { await vi.advanceTimersByTimeAsync(1_000); });
  expect(fixture.getPage).toHaveBeenCalledTimes(2);
  let eventRefresh!: Promise<unknown>;
  let explicitRefresh!: Promise<unknown>;
  act(() => {
    eventRefresh = fixture.queries.invalidateQueries({ queryKey: v2QueryKeys.transcript("thread-a") }, { cancelRefetch: false });
    explicitRefresh = view.result.current.refetch();
  });
  await act(async () => { await vi.advanceTimersByTimeAsync(3_000); });
  expect(fixture.getPage).toHaveBeenCalledTimes(2);
  view.rerender({ interval: false });
  await act(async () => {
    pending.resolve(page([item(200)], "before-200"));
    await Promise.all([eventRefresh, explicitRefresh]);
    await vi.advanceTimersByTimeAsync(0);
  });
  expect(view.result.current.items.at(-1)?.sequence).toBe(201);
  fixture.append(1);
  fixture.update(202, { title: "Run completed", status: "completed" });
  fixture.getPage.mockClear();
  await act(async () => {
    await fixture.queries.invalidateQueries({ queryKey: v2QueryKeys.transcript("thread-a") }, { cancelRefetch: false });
    await vi.advanceTimersByTimeAsync(0);
  });
  expect(fixture.getPage).toHaveBeenCalledTimes(1);
  expect(view.result.current.items.at(-1)).toMatchObject({ sequence: 202, status: "completed", title: "Run completed", durable: true });
  view.unmount(); fixture.queries.clear();
});

it("revalidates only the mutable historical source window on broad invalidation", async () => {
  const fixture = setup();
  fixture.update(150, { source: "operator", kind: "operator_input", source_ref: "queued-150",
    status: "pending", instruction_authorized: true, detail: "old queued content" });
  fixture.update(160, { tool_name: "command_runtime", activity_summary: {
    version: "thread_activity_summary.v1", activity_ref: "command-160", command: "sleep 60", command_count: 1,
    status: "running", duration_milliseconds: 100,
  } });
  fixture.update(250, { kind: "approval", source: "harness", status: "pending" });
  fixture.update(450, { tool_name: "command_runtime", status: "completed", activity_summary: undefined });
  const view = renderHook(() => useV2ThreadTranscript(fixture.client, "thread-a"), { wrapper: fixture.wrapper });
  await waitFor(() => expect(view.result.current.items).toHaveLength(100));
  for (let index = 1; index < 10; index++) await act(async () => { await view.result.current.fetchNextPage(); });
  await waitFor(() => expect(view.result.current.items).toHaveLength(1_000));
  const stable = view.result.current.items.find((entry) => entry.sequence === 149);
  const approval = view.result.current.items.find((entry) => entry.sequence === 250);
  const refetch = view.result.current.refetch;
  fixture.update(150, { status: "cancelled", stage: "blocked", detail: "corrected content",
    instruction_authorized: false, promoted_to_message_id: "replacement-150" });
  fixture.update(160, { activity_summary: {
    version: "thread_activity_summary.v1", activity_ref: "command-160", command: "sleep 60", command_count: 1,
    status: "completed", duration_milliseconds: 600, exit_code: 0,
  } });
  fixture.getPage.mockClear();
  await act(async () => { await fixture.queries.invalidateQueries({ queryKey: v2QueryKeys.thread("thread-a") }, { cancelRefetch: false }); });
  expect(fixture.getPage.mock.calls.map((call) => call[2])).toEqual(["", "before-201"]);
  await waitFor(() => expect(view.result.current.items.find((entry) => entry.sequence === 150)?.status).toBe("cancelled"));
  expect(view.result.current.items.find((entry) => entry.sequence === 150)).toMatchObject({
    source_ref: "queued-150", instruction_authorized: false, verifiable: true, detail: "corrected content",
    promoted_to_message_id: "replacement-150",
  });
  expect(view.result.current.items.find((entry) => entry.sequence === 160)?.activity_summary)
    .toMatchObject({ status: "completed", duration_milliseconds: 600, exit_code: 0 });
  expect(view.result.current.items.find((entry) => entry.sequence === 149)).toBe(stable);
  expect(view.result.current.items.find((entry) => entry.sequence === 250)).toBe(approval);
  expect(view.result.current.refetch).toBe(refetch);
  fixture.getPage.mockClear();
  await act(async () => { await view.result.current.refetch(); });
  expect(fixture.getPage).toHaveBeenCalledTimes(1);
  view.unmount(); fixture.queries.clear();
});

it("locates a mutable head record once, then refreshes its pinned cursor without walking old immutable pages", async () => {
  const fixture = setup(200);
  fixture.update(190, { source: "operator", kind: "operator_input", status: "pending", source_ref: "queued-190", instruction_authorized: true });
  const view = renderHook(() => useV2ThreadTranscript(fixture.client, "thread-a"), { wrapper: fixture.wrapper });
  await waitFor(() => expect(view.result.current.items).toHaveLength(100));
  fixture.append(150);
  fixture.getPage.mockClear();
  await act(async () => { await view.result.current.refetch(); });
  expect(fixture.getPage.mock.calls.map((call) => call[2])).toEqual(["", "before-251"]);
  await waitFor(() => expect(view.result.current.items).toHaveLength(250));
  await act(async () => { await view.result.current.fetchNextPage(); });
  fixture.append(1);
  fixture.update(190, { detail: "edited while pending" });
  fixture.getPage.mockClear();
  await act(async () => { await view.result.current.refetch(); });
  expect(fixture.getPage.mock.calls.map((call) => call[2])).toEqual(["", "before-251"]);
  await waitFor(() => expect(view.result.current.items.find((entry) => entry.sequence === 190)?.detail).toBe("edited while pending"));
  fixture.hide([190]);
  fixture.getPage.mockClear();
  await act(async () => { await view.result.current.refetch({ refreshMutable: true }); });
  expect(fixture.getPage.mock.calls.map((call) => call[2])).toEqual(["", "before-251"]);
  await waitFor(() => expect(view.result.current.items.some((entry) => entry.id === "event-190")).toBe(false));
  expect(view.result.current.items).toHaveLength(350);
  fixture.getPage.mockClear();
  await act(async () => { await view.result.current.refetch(); });
  expect(fixture.getPage.mock.calls.map((call) => call[2])).toEqual(["", "before-251"]);
  view.unmount(); fixture.queries.clear();
});

it("removes consumed old input from both historical pages and an overlapping head copy", async () => {
  const fixture = setup(300);
  fixture.update(150, { source: "operator", kind: "operator_input", status: "pending", source_ref: "queued-150", instruction_authorized: true });
  const view = renderHook(() => useV2ThreadTranscript(fixture.client, "thread-a"), { wrapper: fixture.wrapper });
  await waitFor(() => expect(view.result.current.items).toHaveLength(100));
  await act(async () => { await view.result.current.fetchNextPage(); });
  await waitFor(() => expect(view.result.current.items).toHaveLength(200));
  // The durable head may already overlap a previously loaded source page.
  fixture.queries.setQueryData(v2QueryKeys.transcript("thread-a"), {
    pages: [page([item(150, { source: "operator", kind: "operator_input", status: "pending", source_ref: "queued-150", instruction_authorized: true }),
      ...Array.from({ length: 100 }, (_, index) => item(index + 201))], "before-201")], pageParams: [""],
  });
  fixture.hide([150]);
  fixture.getPage.mockClear();
  await act(async () => { await view.result.current.refetch(); });
  expect(fixture.getPage.mock.calls.map((call) => call[2])).toEqual(["", "before-201"]);
  await waitFor(() => expect(view.result.current.items.some((entry) => entry.id === "event-150")).toBe(false));
  expect(view.result.current.items).toHaveLength(199);
  expect(view.result.current.data?.pages).toHaveLength(2);
  view.unmount(); fixture.queries.clear();
});

it.each(["completed", "cancelled"])("updates an old Run boundary to %s from its original source cursor without changing authority", async (status) => {
  const fixture = setup(0);
  const boundary = item(0, { id: "run-boundary:run-a", canonical_id: "run:run-a", activity_type: "checkpoint", status: "running", stage: "started" });
  fixture.getPage.mockResolvedValueOnce(page([item(200)], "old-run"));
  fixture.getPage.mockResolvedValueOnce(page([boundary, item(1)], "older"));
  const view = renderHook(() => useV2ThreadTranscript(fixture.client, "thread-a"), { wrapper: fixture.wrapper });
  await waitFor(() => expect(view.result.current.items).toHaveLength(1));
  await act(async () => { await view.result.current.fetchNextPage(); });
  await waitFor(() => expect(view.result.current.items).toHaveLength(3));
  fixture.getPage.mockClear();
  fixture.getPage.mockResolvedValueOnce(page([item(200)], "old-run"));
  fixture.getPage.mockResolvedValueOnce(page([{ ...boundary, status, stage: "result", detail: `当前 Run 状态：${status}` }, item(1)], "older"));
  await act(async () => { await view.result.current.refetch({ refreshMutable: true }); });
  expect(fixture.getPage.mock.calls.map((call) => call[2])).toEqual(["", "old-run"]);
  await waitFor(() => expect(view.result.current.items[0].status).toBe(status));
  expect(view.result.current.items[0]).toMatchObject({ id: "run-boundary:run-a", canonical_id: "run:run-a", sequence: 0,
    durable: true, source: "harness", verifiable: true, instruction_authorized: false });
  view.unmount(); fixture.queries.clear();
});

it("rebases ten entirely hidden source pages to the new head without scanning unloaded history or losing newly visible pages", async () => {
  const fixture = setup(2_000);
  fixture.hide(Array.from({ length: 2_000 }, (_, index) => index + 1));
  const view = renderHook(() => useV2ThreadTranscript(fixture.client, "thread-a"), { wrapper: fixture.wrapper });
  await waitFor(() => expect(view.result.current.data?.pages).toHaveLength(1));
  for (let index = 1; index < 10; index++) await act(async () => { await view.result.current.fetchNextPage(); });
  await waitFor(() => expect(view.result.current.data?.pages).toHaveLength(10));
  expect(view.result.current.items).toHaveLength(0);
  fixture.append(300);
  fixture.getPage.mockClear();
  await act(async () => { await view.result.current.refetch(); });
  expect(fixture.getPage).toHaveBeenCalledTimes(1);
  await waitFor(() => expect(view.result.current.data?.pages).toHaveLength(1));
  expect(view.result.current.items).toHaveLength(100);
  expect(view.result.current.hasNextPage).toBe(true);
  await act(async () => { await view.result.current.fetchNextPage(); });
  await waitFor(() => expect(view.result.current.items).toHaveLength(200));
  await act(async () => { await view.result.current.fetchNextPage(); });
  await waitFor(() => expect(view.result.current.items).toHaveLength(300));
  expect(fixture.getPage.mock.calls.map((call) => call[2])).toEqual(["", "before-2201", "before-2101"]);
  expect(view.result.current.items.map((entry) => entry.sequence)).toEqual(Array.from({ length: 300 }, (_, index) => index + 2_001));
  view.unmount(); fixture.queries.clear();
});

it("does not swallow terminal demand arriving during an older head observation", async () => {
  const fixture = setup(200);
  const view = renderHook(() => useV2ThreadTranscript(fixture.client, "thread-a"), { wrapper: fixture.wrapper });
  await waitFor(() => expect(view.result.current.items).toHaveLength(100));
  const originalHead = deferred<PageResult<ThreadTranscriptItemView>>();
  fixture.getPage.mockImplementationOnce(() => originalHead.promise);
  fixture.getPage.mockClear();
  let first!: Promise<unknown>;
  let terminal!: Promise<unknown>;
  act(() => { first = view.result.current.refetch(); });
  expect(fixture.getPage).toHaveBeenCalledTimes(1);
  fixture.append(1);
  fixture.update(201, { status: "completed", title: "Durable completion" });
  act(() => { terminal = view.result.current.refetch({ refreshMutable: true }); });
  expect(fixture.getPage).toHaveBeenCalledTimes(1);
  await act(async () => {
    originalHead.resolve(page(Array.from({ length: 100 }, (_, index) => item(index + 101)), "before-101"));
    await Promise.all([first, terminal]);
  });
  expect(fixture.getPage).toHaveBeenCalledTimes(2);
  await waitFor(() => expect(view.result.current.items).toHaveLength(101));
  expect(view.result.current.items.at(-1)).toMatchObject({ sequence: 201, status: "completed", title: "Durable completion", durable: true });
  view.unmount(); fixture.queries.clear();
});

it("rejects strict read confirmation when the demanded fresh head fails, retaining the saved transcript", async () => {
  const fixture = setup(200);
  const view = renderHook(() => useV2ThreadTranscript(fixture.client, "thread-a"), { wrapper: fixture.wrapper });
  await waitFor(() => expect(view.result.current.items).toHaveLength(100));
  const originalHead = deferred<PageResult<ThreadTranscriptItemView>>();
  fixture.getPage.mockReturnValueOnce(originalHead.promise).mockRejectedValueOnce(new Error("fresh head failed"));
  let first!: Promise<void>;
  let strict!: Promise<void>;
  act(() => {
    first = view.result.current.refetch();
    strict = view.result.current.refetch({ refreshMutable: true, throwOnError: true });
  });
  const rejected = expect(strict).rejects.toThrow("fresh head failed");
  await act(async () => {
    originalHead.resolve(page(Array.from({ length: 100 }, (_, index) => item(index + 101)), "before-101"));
    await first; await rejected;
  });
  expect(view.result.current.items).toHaveLength(100);
  view.unmount(); fixture.queries.clear();
});

it("records terminal demand during the initial pending read while preserving the loading state", async () => {
  const fixture = setup(200);
  const initial = deferred<PageResult<ThreadTranscriptItemView>>();
  fixture.getPage.mockImplementationOnce(() => initial.promise);
  const view = renderHook(() => useV2ThreadTranscript(fixture.client, "thread-a"), { wrapper: fixture.wrapper });
  await waitFor(() => expect(fixture.getPage).toHaveBeenCalledTimes(1));
  fixture.append(1);
  fixture.update(201, { status: "completed", title: "Durable completion" });
  let terminal!: Promise<unknown>;
  act(() => { terminal = view.result.current.refetch({ refreshMutable: true }); });
  expect(view.result.current.isLoading).toBe(true);
  expect(view.result.current.data).toBeUndefined();
  expect(fixture.getPage).toHaveBeenCalledTimes(1);
  await act(async () => {
    initial.resolve(page(Array.from({ length: 100 }, (_, index) => item(index + 101)), "before-101"));
    await terminal;
  });
  expect(fixture.getPage).toHaveBeenCalledTimes(2);
  await waitFor(() => expect(view.result.current.items).toHaveLength(101));
  expect(view.result.current.items.at(-1)).toMatchObject({ sequence: 201, status: "completed", title: "Durable completion" });
  view.unmount(); fixture.queries.clear();
});

it("keeps a suppressed input removed when an older pagination request publishes its stale page snapshot", async () => {
  const fixture = setup(400);
  fixture.update(250, { source: "operator", kind: "operator_input", status: "pending", source_ref: "queued-250", instruction_authorized: true });
  const view = renderHook(() => useV2ThreadTranscript(fixture.client, "thread-a"), { wrapper: fixture.wrapper });
  await waitFor(() => expect(view.result.current.items).toHaveLength(100));
  await act(async () => { await view.result.current.fetchNextPage(); });
  await waitFor(() => expect(view.result.current.items).toHaveLength(200));
  const pendingHistory = deferred<PageResult<ThreadTranscriptItemView>>();
  fixture.getPage.mockImplementationOnce(() => pendingHistory.promise);
  let history!: Promise<unknown>;
  act(() => { history = view.result.current.fetchNextPage(); });
  await waitFor(() => expect(view.result.current.isFetchingNextPage).toBe(true));
  fixture.hide([250]);
  await act(async () => { await view.result.current.refetch({ refreshMutable: true }); });
  await waitFor(() => expect(view.result.current.items.some((entry) => entry.id === "event-250")).toBe(false));
  await act(async () => {
    pendingHistory.resolve(page(Array.from({ length: 100 }, (_, index) => item(index + 101)), "before-101"));
    await history;
  });
  await waitFor(() => expect(view.result.current.items).toHaveLength(299));
  expect(view.result.current.items.some((entry) => entry.id === "event-250")).toBe(false);
  expect(view.result.current.hasNextPage).toBe(true);
  view.unmount(); fixture.queries.clear();
});

it("restores the committed queued identity after a tool segment and removes its separately identified Session bubble", async () => {
  const fixture = setup(1_100);
  fixture.update(150, { source: "operator", kind: "operator_input", status: "pending", source_ref: "queued-150", instruction_authorized: true });
  const view = renderHook(() => useV2ThreadTranscript(fixture.client, "thread-a"), { wrapper: fixture.wrapper });
  await waitFor(() => expect(view.result.current.items).toHaveLength(100));
  for (let index = 1; index < 10; index++) await act(async () => { await view.result.current.fetchNextPage(); });
  await waitFor(() => expect(view.result.current.items).toHaveLength(1_000));
  // A non-final tool segment preserves the pending queue state, but shows the
  // exact Session input event instead of the old queued projection.
  fixture.hide([150]);
  fixture.append(1);
  fixture.update(1_101, { source: "operator", kind: "operator_input", status: undefined, source_ref: undefined,
    canonical_id: "session-event-1101", instruction_authorized: true, detail: "same input" });
  fixture.getPage.mockClear();
  await act(async () => { await view.result.current.refetch({ refreshMutable: true }); });
  await waitFor(() => expect(view.result.current.items.some((entry) => entry.id === "event-150")).toBe(false));
  await waitFor(() => expect(view.result.current.items.find((entry) => entry.sequence === 1_101)?.source).toBe("operator"));
  fixture.append(200);
  await act(async () => { await view.result.current.refetch(); });
  await waitFor(() => expect(view.result.current.items.at(-1)?.sequence).toBe(1_301));
  fixture.hide([1_101]);
  fixture.update(150, { status: "committed", stage: "result", detail: "committed original input" });
  fixture.getPage.mockClear();
  await act(async () => { await view.result.current.refetch({ refreshMutable: true }); });
  expect(fixture.getPage.mock.calls.map((call) => call[2])).toEqual(["", "before-201", "before-1102"]);
  await waitFor(() => expect(view.result.current.items.find((entry) => entry.id === "event-150")?.status).toBe("committed"));
  expect(view.result.current.items.some((entry) => entry.id === "event-1101")).toBe(false);
  expect(view.result.current.items.find((entry) => entry.id === "event-150")).toMatchObject({
    source_ref: "queued-150", canonical_id: "canonical-150", source: "operator", kind: "operator_input",
    instruction_authorized: true, verifiable: true, stage: "result", detail: "committed original input",
  });
  fixture.getPage.mockClear();
  await act(async () => { await view.result.current.refetch(); });
  expect(fixture.getPage).toHaveBeenCalledTimes(1);
  view.unmount(); fixture.queries.clear();
});

it("recovers an unpinned queued identity hidden in the same refresh that moves it out of the head", async () => {
  const fixture = setup(200);
  fixture.update(150, { source: "operator", kind: "operator_input", status: "pending", source_ref: "queued-150", instruction_authorized: true });
  const view = renderHook(() => useV2ThreadTranscript(fixture.client, "thread-a"), { wrapper: fixture.wrapper });
  await waitFor(() => expect(view.result.current.items).toHaveLength(100));
  fixture.append(250);
  fixture.hide([150]);
  await act(async () => { await view.result.current.refetch(); });
  await waitFor(() => expect(view.result.current.items.some((entry) => entry.id === "event-150")).toBe(false));
  expect(view.result.current.items.at(-1)?.sequence).toBe(450);
  fixture.hide([]);
  fixture.update(150, { status: "committed", stage: "result" });
  fixture.getPage.mockClear();
  // No newer source record moves the head cursor between hide and final commit.
  await act(async () => { await view.result.current.refetch({ refreshMutable: true }); });
  expect(fixture.getPage.mock.calls.map((call) => call[2])).toEqual(["", "before-351", "before-251", "before-151"]);
  await waitFor(() => expect(view.result.current.items.find((entry) => entry.id === "event-150")?.status).toBe("committed"));
  expect(view.result.current.items.find((entry) => entry.id === "event-150")).toMatchObject({
    source_ref: "queued-150", instruction_authorized: true, verifiable: true,
  });
  view.unmount(); fixture.queries.clear();
});

it("performs the final queued and Session window reads even when a different page already shows the terminal Run", async () => {
  const fixture = setup(500);
  fixture.update(150, { source: "operator", kind: "operator_input", status: "pending", source_ref: "queued-150", instruction_authorized: true });
  fixture.update(350, { source: "operator", kind: "operator_input", status: undefined, source_ref: undefined, instruction_authorized: true });
  const view = renderHook(() => useV2ThreadTranscript(fixture.client, "thread-a"), { wrapper: fixture.wrapper });
  await waitFor(() => expect(view.result.current.items).toHaveLength(100));
  for (let index = 1; index < 4; index++) await act(async () => { await view.result.current.fetchNextPage(); });
  await waitFor(() => expect(view.result.current.items).toHaveLength(400));
  fixture.hide([150]);
  await act(async () => { await view.result.current.refetch(); });
  await waitFor(() => expect(view.result.current.items.some((entry) => entry.id === "event-150")).toBe(false));
  fixture.hide([350]);
  fixture.update(150, { status: "committed", stage: "result" });
  fixture.getPage.mockResolvedValueOnce(page([item(0, { id: "run-boundary:run-a", canonical_id: "run:run-a", status: "completed" }), item(1)]));
  await act(async () => { await view.result.current.fetchNextPage(); });
  await waitFor(() => expect(view.result.current.items[0].status).toBe("completed"));
  fixture.queries.setQueryData(v2QueryKeys.thread("thread-a"), {
    runs: [{ run: { id: "run-a", status: "completed" } }], last_run: { id: "run-a", status: "completed" },
  } as ThreadDetailView);
  fixture.getPage.mockClear();
  await act(async () => { await view.result.current.refetch({ refreshMutable: true }); });
  expect(fixture.getPage.mock.calls.map((call) => call[2])).toEqual(["", "before-201", "before-401"]);
  await waitFor(() => expect(view.result.current.items.find((entry) => entry.id === "event-150")?.status).toBe("committed"));
  expect(view.result.current.items.some((entry) => entry.id === "event-350")).toBe(false);
  fixture.getPage.mockClear();
  await act(async () => { await view.result.current.refetch(); });
  expect(fixture.getPage).toHaveBeenCalledTimes(1);
  view.unmount(); fixture.queries.clear();
});

it("rechecks only a newly loaded history window whose unseen Session snapshot crosses terminal synchronization", async () => {
  const fixture = setup(1_100);
  fixture.update(850, { source: "operator", kind: "operator_input", status: undefined, source_ref: undefined,
    instruction_authorized: true, detail: "old unbound Session input" });
  const view = renderHook(() => useV2ThreadTranscript(fixture.client, "thread-a"), { wrapper: fixture.wrapper });
  await waitFor(() => expect(view.result.current.items).toHaveLength(100));
  await act(async () => { await view.result.current.fetchNextPage(); });
  await waitFor(() => expect(view.result.current.items).toHaveLength(200));
  const history = deferred<PageResult<ThreadTranscriptItemView>>();
  fixture.getPage.mockImplementationOnce(() => history.promise);
  let pagination!: Promise<unknown>;
  act(() => { pagination = view.result.current.fetchNextPage(); });
  await waitFor(() => expect(view.result.current.isFetchingNextPage).toBe(true));
  fixture.hide([850]);
  fixture.queries.setQueryData(v2QueryKeys.thread("thread-a"), {
    runs: [{ run: { id: "run-a", status: "completed" } }], last_run: { id: "run-a", status: "completed" },
  } as ThreadDetailView);
  await act(async () => { await view.result.current.refetch({ refreshMutable: true }); });
  fixture.getPage.mockClear();
  await act(async () => {
    history.resolve(page(Array.from({ length: 100 }, (_, index) => item(index + 801, index + 801 === 850
      ? { source: "operator", kind: "operator_input", status: undefined, source_ref: undefined,
        instruction_authorized: true, detail: "old unbound Session input" } : {})), "before-801"));
    await pagination;
  });
  expect(fixture.getPage.mock.calls.map((call) => call[2])).toEqual(["before-901"]);
  await waitFor(() => expect(view.result.current.items).toHaveLength(299));
  expect(view.result.current.items.some((entry) => entry.id === "event-850")).toBe(false);
  expect(view.result.current.hasNextPage).toBe(true);
  view.unmount(); fixture.queries.clear();
});
