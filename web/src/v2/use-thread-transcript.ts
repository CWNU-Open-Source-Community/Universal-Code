import { useInfiniteQuery, useQuery, useQueryClient, replaceEqualDeep, skipToken, type InfiniteData } from "@tanstack/react-query";
import { useCallback, useMemo } from "react";
import { APIRequestError, type APIClient } from "../api/client";
import type { PageResult, ThreadDetailView, ThreadTranscriptItemView } from "../api/types";
import { v2QueryKeys } from "./query-keys";

type TranscriptPage = PageResult<ThreadTranscriptItemView>;
type TranscriptData = InfiniteData<TranscriptPage, string>;
type TranscriptHead = TranscriptData & {
  mutableCursors?: Record<string, string>;
  hiddenMutable?: ThreadTranscriptItemView[];
  suppressedIDs?: string[];
  headCursor?: string;
  latestMutableIDs?: string[];
  settledRuns?: string[];
};
type TranscriptOptions = { refetchInterval?: number | false };
type TranscriptRefetchOptions = { refreshMutable?: boolean; throwOnError?: boolean };
type Observation = { cursor: string; page: TranscriptPage };

const identity = (item: ThreadTranscriptItemView) => item.id || `${item.run_id}:${item.sequence}:${item.canonical_id}`;
const compare = (left: ThreadTranscriptItemView, right: ThreadTranscriptItemView) =>
  left.run_ordinal - right.run_ordinal || left.sequence - right.sequence || (left.position ?? 0) - (right.position ?? 0);
const terminalRunStates = new Set(["completed", "failed", "cancelled"]);
const mutableCommandStates = new Set(["pending", "prepared", "running", "stopping"]);

// These DTOs include current database projections. Approval/event status rows
// describe immutable events, so a historical "pending" event alone is not mutable.
function isMutable(item: ThreadTranscriptItemView, terminalRuns: Set<string>) {
  return item.kind === "peer_message" && item.peer_message?.status === "queued" ||
    item.source === "operator" && item.kind === "operator_input" &&
    (item.status === "pending" || !item.status && !terminalRuns.has(item.run_id)) ||
    item.sequence === 0 && !terminalRunStates.has(item.status ?? "") ||
    item.tool_name === "command_runtime" && mutableCommandStates.has(item.activity_summary?.status ?? "");
}

function terminalRuns(items: ThreadTranscriptItemView[]) {
  return new Set(items.filter((item) => item.sequence === 0 && terminalRunStates.has(item.status ?? "")).map((item) => item.run_id));
}

function mergeItems(previous: ThreadTranscriptItemView[], incoming: ThreadTranscriptItemView[]) {
  const items = new Map(previous.map((item) => [identity(item), item]));
  let changed = false;
  for (const item of incoming) {
    const key = identity(item);
    const current = items.get(key);
    const next = current ? replaceEqualDeep(current, item) : item;
    if (next !== current) { items.set(key, next); changed = true; }
  }
  return changed ? [...items.values()].sort(compare) : previous;
}

function mergedPages(pages: TranscriptPage[]) {
  return mergeItems([], pages.slice().reverse().flatMap((page) => page.items));
}

function updateItems(items: ThreadTranscriptItemView[], fresh: Map<string, ThreadTranscriptItemView>, removed: Set<string>) {
  let changed = false;
  const next: ThreadTranscriptItemView[] = [];
  for (const item of items) {
    const key = identity(item);
    if (removed.has(key)) { changed = true; continue; }
    const replacement = fresh.get(key);
    const value = replacement ? replaceEqualDeep(item, replacement) : item;
    if (value !== item) changed = true;
    next.push(value);
  }
  return changed ? next : items;
}

export function useV2ThreadTranscript(client: APIClient, threadID: string, { refetchInterval = false }: TranscriptOptions = {}) {
  const queryClient = useQueryClient();
  const queryKey = v2QueryKeys.transcript(threadID);
  const historyKey = [...queryKey, "history"];
  const demandKey = [...queryKey, "sync-demand"];
  // This observer keeps a demand that arrives during the initial read without
  // manufacturing successful head data or changing the loading state.
  useQuery({ queryKey: demandKey, queryFn: skipToken, initialData: 0 });
  const head = useQuery({
    queryKey,
    enabled: Boolean(threadID),
    refetchInterval,
    queryFn: async ({ signal }): Promise<TranscriptHead> => {
      let previous = queryClient.getQueryData<TranscriptHead>(queryKey);
      let completed = 0;
      const observations: Observation[] = [];
      const removed = new Set(previous?.suppressedIDs);
      let resetEmptyHistory = false;
      let result!: TranscriptHead;

      do {
        const requested = queryClient.getQueryData<number>(demandKey) ?? 0;
        const history: TranscriptData | undefined = resetEmptyHistory ? undefined : queryClient.getQueryData<TranscriptData>(historyKey);
        const cached: ThreadTranscriptItemView[] = mergedPages([...(previous?.pages ?? []), ...(history?.pages ?? [])])
          .filter((item) => !removed.has(identity(item)));
        const newest: ThreadTranscriptItemView | undefined = cached.at(-1);
        // Seeing a terminal Run in another source window does not prove that an
        // old queued/Session window has had its final read. Retire those windows
        // only after this round completes successfully.
        const finishedRuns = new Set(previous?.settledRuns);
        const detail = queryClient.getQueryData<ThreadDetailView>(v2QueryKeys.thread(threadID));
        const newlyFinishedRuns = new Set([
          ...terminalRuns(cached),
          ...(detail ? [...(detail.runs ?? []).map((binding) => binding.run), detail.active_run, detail.last_run]
            .filter((run) => run && terminalRunStates.has(run.status)).map((run) => run!.id) : []),
        ]);
        const mutable = mergeItems(cached.filter((item) => isMutable(item, finishedRuns)),
          (previous?.hiddenMutable ?? []).filter((item) => !finishedRuns.has(item.run_id)));
        const mutableCursors = { ...previous?.mutableCursors };
        // Explicit history cursors already pin stable source windows. A mutable
        // record first seen in the moving head acquires a stable cursor the first
        // time a bridge/targeted read finds it outside the newest source page.
        for (const data of [previous, history]) data?.pages.forEach((page, index) => {
          for (const item of page.items) if (!removed.has(identity(item)) && isMutable(item, finishedRuns) &&
            !mutableCursors[identity(item)] && data.pageParams[index]) {
            mutableCursors[identity(item)] = data.pageParams[index];
          }
        });
        const round: Observation[] = [];
        const cursors = new Set<string>();
        let cursor = "";
        let joined = !newest;
        let unlocated = mutable.filter((item) => !mutableCursors[identity(item)]);
        const covered = new Set<string>();
        while (true) {
          const page = await client.getPage<ThreadTranscriptItemView>(
            `/threads/${encodeURIComponent(threadID)}/transcript`, { limit: 100 }, cursor, signal);
          round.push({ cursor, page });
          for (const item of page.items) {
            if (isMutable(item, finishedRuns) && cursor) mutableCursors[identity(item)] = cursor;
            covered.add(identity(item));
          }
          const oldest = page.items[0];
          if (oldest) unlocated = unlocated.filter((item) => {
            if (compare(item, oldest) < 0) return true;
            covered.add(identity(item));
            return false;
          });
          if (page.items.some((item) => newest && compare(item, newest) <= 0)) joined = true;
          const next = page.page.next_cursor;
          if (cursor === "" && previous && next === previous.headCursor) {
            joined = true;
            // The moving head has exactly the same source frontier. Missing
            // unpinned projections were in that same window, even if it is empty.
            const latest = new Set(previous.latestMutableIDs);
            unlocated = unlocated.filter((item) => {
              if (!latest.has(identity(item))) return true;
              covered.add(identity(item)); return false;
            });
          }
          if (!next) {
            if (page.page.truncated) throw new APIRequestError("最新工作记录存在未读取的间隔，请重试同步。", "INVALID_RESPONSE", 502);
            for (const item of unlocated) covered.add(identity(item));
            break;
          }
          // With no visible cached records there is no visible join point. Treat
          // this as the first page and rebase empty history to its new frontier;
          // newly visible older records remain reachable via manual pagination.
          if (!previous || joined && unlocated.length === 0) break;
          if (cursors.has(next)) throw new APIRequestError("工作记录分页游标未推进，已有记录已保留。", "INVALID_RESPONSE", 502);
          cursors.add(next);
          cursor = next;
        }

        const observedIDs = new Set(round.flatMap(({ page }) => page.items.map(identity)));
        const targets = new Map<string, ThreadTranscriptItemView[]>();
        for (const item of mutable) {
          const key = identity(item);
          const pinned = mutableCursors[key];
          if (observedIDs.has(key) || !pinned || covered.has(key)) continue;
          const items = targets.get(pinned) ?? [];
          items.push(item); targets.set(pinned, items);
        }
        for (const [pinned, items] of targets) {
          let observation = round.find((entry) => entry.cursor === pinned);
          if (!observation) {
            observation = { cursor: pinned, page: await client.getPage<ThreadTranscriptItemView>(
              `/threads/${encodeURIComponent(threadID)}/transcript`, { limit: 100 }, pinned, signal) };
            round.push(observation);
          }
          for (const item of items) covered.add(identity(item));
        }
        const incoming = mergedPages(round.map(({ page }) => page));
        const incomingIDs = new Set(incoming.map(identity));
        for (const id of incomingIDs) removed.delete(id);
        for (const item of mutable) {
          const key = identity(item);
          if (covered.has(key) && !incomingIDs.has(key)) removed.add(key);
          else if (incomingIDs.has(key)) removed.delete(key);
        }
        const fresh = new Map(incoming.map((item) => [identity(item), item]));
        const first = previous?.pages[0];
        const emptyStart: boolean = Boolean(previous && !newest && mutable.length === 0);
        resetEmptyHistory ||= emptyStart;
        const items = mergeItems(updateItems(first?.items ?? [], fresh, removed), incoming);
        const firstPage = first && items === first.items && !emptyStart ? first : {
          ...(emptyStart ? round[0].page : first ?? round[0].page), items,
        };
        const remaining = emptyStart ? [] : (previous?.pages.slice(1) ?? []).map((page) => {
          const items = updateItems(page.items, fresh, removed);
          return items === page.items ? page : { ...page, items };
        });
        const finalCached = mergedPages([firstPage, ...remaining, ...(history?.pages ?? [])])
          .filter((item) => !removed.has(identity(item)));
        const finalFinishedRuns = new Set([...finishedRuns, ...newlyFinishedRuns, ...terminalRuns(finalCached)]);
        // A tool segment can temporarily suppress queued input, then bind it
        // back to this exact original identity at final commit. Keep tracking its
        // source window while hidden; never infer a replacement from equal text.
        const hiddenMutable = mutable.filter((item) => removed.has(identity(item)) && item.source === "operator" &&
          item.kind === "operator_input" && item.status === "pending" && !finalFinishedRuns.has(item.run_id));
        const finalMutable = [...finalCached.filter((item) => isMutable(fresh.get(identity(item)) ?? item, finalFinishedRuns)), ...hiddenMutable];
        result = {
          pages: firstPage === first && remaining.every((page, index) => page === previous!.pages[index + 1])
            ? previous!.pages : [firstPage, ...remaining],
          pageParams: emptyStart ? [""] : previous?.pageParams ?? [""],
          mutableCursors: Object.fromEntries(finalMutable.map((item) => [identity(item), mutableCursors[identity(item)] ?? ""])),
          hiddenMutable,
          suppressedIDs: [...removed],
          headCursor: round[0].page.page.next_cursor,
          latestMutableIDs: [...new Set([
            ...round[0].page.items.filter((item) => isMutable(item, finalFinishedRuns)).map(identity),
            ...(previous?.headCursor === round[0].page.page.next_cursor ? previous?.latestMutableIDs ?? [] : []),
          ])].filter((id) => finalMutable.some((item) => identity(item) === id)),
          settledRuns: [...finalFinishedRuns],
        };
        observations.push(...round);
        completed = requested;
        previous = result;
        // A terminal/queue action arriving during a request needs a subsequent
        // observation. Consume it serially inside the same React Query fetch.
      } while ((queryClient.getQueryData<number>(demandKey) ?? 0) > completed);

      if (resetEmptyHistory) queryClient.removeQueries({ queryKey: historyKey, exact: true });
      else queryClient.setQueryData<TranscriptData>(historyKey, (current) => {
        if (!current) return current;
        const fresh = new Map(observations.flatMap(({ page }) => page.items).map((item) => [identity(item), item]));
        const byCursor = new Map(observations.map((entry) => [entry.cursor, entry]));
        const pages = current.pages.map((page, index) => {
          // A direct source-window read is authoritative, including a projection
          // that disappeared after queued input was consumed/suppressed.
          const direct = byCursor.get(current.pageParams[index]);
          const items = direct ? replaceEqualDeep(page.items, direct.page.items) : updateItems(page.items, fresh, removed);
          return items === page.items ? page : { ...page, items };
        });
        return pages.every((page, index) => page === current.pages[index]) ? current : { ...current, pages };
      });
      return result;
    },
  });
  const older = useInfiniteQuery({
    queryKey: historyKey,
    // Automatic invalidation reads the head and mutable windows, never every
    // loaded immutable page. fetchNextPage retains its original source frontier.
    enabled: false,
    initialPageParam: head.data?.pages.at(-1)?.page.next_cursor ?? "",
    getNextPageParam: (page: TranscriptPage) => page.page.next_cursor || undefined,
    queryFn: async ({ pageParam, signal }) => {
      let requested = queryClient.getQueryData<number>(demandKey) ?? 0;
      let settled = new Set(queryClient.getQueryData<TranscriptHead>(queryKey)?.settledRuns);
      const checkedSuppressed = new Set<string>();
      let page = await client.getPage<ThreadTranscriptItemView>(
        `/threads/${encodeURIComponent(threadID)}/transcript`, { limit: 100 }, pageParam, signal);
      while (true) {
        const current = queryClient.getQueryData<TranscriptHead>(queryKey);
        const latestDemand = queryClient.getQueryData<number>(demandKey) ?? 0;
        const latestSettled = new Set(current?.settledRuns);
        const suppressed = new Set(current?.suppressedIDs);
        const crossedTerminal = page.items.some((item) => item.source === "operator" && item.kind === "operator_input" &&
          latestSettled.has(item.run_id) && !settled.has(item.run_id));
        const unchecked = page.items.filter((item) => suppressed.has(identity(item)) && !checkedSuppressed.has(identity(item)));
        if (requested === latestDemand && !crossedTerminal && unchecked.length === 0) break;
        for (const item of unchecked) checkedSuppressed.add(identity(item));
        requested = latestDemand;
        settled = latestSettled;
        // An old pagination snapshot may introduce a Session identity the head
        // has never seen. Re-read only this window after an intervening terminal
        // observation, rather than trusting an unseen pre-commit projection.
        page = await client.getPage<ThreadTranscriptItemView>(
          `/threads/${encodeURIComponent(threadID)}/transcript`, { limit: 100 }, pageParam, signal);
      }
      const fresh = new Map(page.items.map((item) => [identity(item), item]));
      queryClient.setQueryData<TranscriptHead>(queryKey, (current) => {
        if (!current) return current;
        const pages = current.pages.map((cached) => {
          const items = updateItems(cached.items, fresh, new Set());
          return items === cached.items ? cached : { ...cached, items };
        });
        const suppressedIDs = current.suppressedIDs?.filter((id) => !fresh.has(id));
        const hiddenMutable = current.hiddenMutable?.filter((item) => !fresh.has(identity(item)));
        return pages.every((page, index) => page === current.pages[index]) &&
          suppressedIDs?.length === current.suppressedIDs?.length && hiddenMutable?.length === current.hiddenMutable?.length
          ? current : { ...current, pages, suppressedIDs, hiddenMutable };
      });
      return page;
    },
  });
  const data = useMemo(() => head.data ? {
    pages: [...head.data.pages, ...(older.data?.pages ?? [])],
    pageParams: [...head.data.pageParams, ...(older.data?.pageParams ?? [])],
  } : undefined, [head.data?.pages, head.data?.pageParams, older.data]);
  const items = useMemo(() => {
    const suppressed = new Set(head.data?.suppressedIDs);
    return mergedPages(data?.pages ?? []).filter((item) => !suppressed.has(identity(item)));
  }, [data?.pages, head.data?.suppressedIDs]);
  const hasNextPage = older.data ? older.hasNextPage : Boolean(head.data?.pages.at(-1)?.page.next_cursor);
  const refetch = useCallback(({ refreshMutable = false, throwOnError = false }: TranscriptRefetchOptions = {}) => {
    const target = v2QueryKeys.transcript(threadID);
    if (refreshMutable) queryClient.setQueryData<number>([...target, "sync-demand"], (current) => (current ?? 0) + 1);
    return queryClient.refetchQueries({ queryKey: target, exact: true }, { cancelRefetch: false, throwOnError });
  }, [queryClient, threadID]);
  return {
    data, items,
    isLoading: head.isLoading,
    isError: head.isError || older.isError,
    error: older.error ?? head.error,
    hasNextPage,
    isFetchingNextPage: older.isFetchingNextPage,
    isFetchNextPageError: older.isError,
    fetchNextPage: () => hasNextPage ? older.fetchNextPage({ cancelRefetch: false }) : Promise.resolve(),
    refetch,
  };
}
