import { useCallback, useEffect, useLayoutEffect, useMemo, useRef, useState, type MutableRefObject, type RefObject, type UIEvent } from "react";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { useConnectionStore } from "../state/connection";

export const MAX_SCROLL_MEMORY_THREADS = 150;
export const SCROLL_FOLLOW_THRESHOLD_PX = 48;
export const SCROLL_TO_BOTTOM_PILL_THRESHOLD_PX = SCROLL_FOLLOW_THRESHOLD_PX;
export const UNREAD_OVERFLOW_VIEWPORT_RATIO = 0.7;

export interface V2ThreadScrollMemory {
  top: number;
  following: boolean;
  anchorKey: string | null;
  anchorOffset: number;
  seenEntryCount: number;
  lastSeenTailID?: string | null;
  updatedAt: number;
}

export interface ScrollMemoryEntryLike {
  id: string;
  kind: string;
}

export function scrollMemoryStorageKey(scope: string): string {
  return `v2:${scope}:scroll-memory`;
}

export function scrollMemoryQueryKey(scope: string, threadID: string) {
  return ["v2", "scroll-memory", scope || "memory", threadID] as const;
}

function sanitizeScrollMemoryEntry(raw: unknown): V2ThreadScrollMemory | null {
  if (!raw || typeof raw !== "object" || Array.isArray(raw)) return null;
  const candidate = raw as Record<string, unknown>;
  if (typeof candidate.top !== "number" || !Number.isFinite(candidate.top) || candidate.top < 0) return null;
  if (typeof candidate.following !== "boolean") return null;
  if (candidate.anchorKey !== null && typeof candidate.anchorKey !== "string") return null;
  if (typeof candidate.anchorOffset !== "number" || !Number.isFinite(candidate.anchorOffset)) return null;
  if (typeof candidate.seenEntryCount !== "number" || !Number.isFinite(candidate.seenEntryCount) || candidate.seenEntryCount < 0) return null;
  if (candidate.lastSeenTailID !== undefined && candidate.lastSeenTailID !== null && typeof candidate.lastSeenTailID !== "string") return null;
  if (typeof candidate.updatedAt !== "number" || !Number.isFinite(candidate.updatedAt)) return null;
  const lastSeenTailID = typeof candidate.lastSeenTailID === "string" && candidate.lastSeenTailID
    ? candidate.lastSeenTailID
    : null;
  return {
    top: candidate.top,
    following: candidate.following,
    anchorKey: candidate.anchorKey,
    anchorOffset: candidate.anchorOffset,
    seenEntryCount: Math.floor(candidate.seenEntryCount),
    lastSeenTailID,
    updatedAt: candidate.updatedAt,
  };
}

export function readPersistedScrollMemoryMap(scope: string): Record<string, V2ThreadScrollMemory> {
  const normalized = scope.trim();
  if (!normalized || typeof window === "undefined") return {};
  try {
    const raw = window.localStorage.getItem(scrollMemoryStorageKey(normalized));
    if (!raw) return {};
    const parsed: unknown = JSON.parse(raw);
    if (!parsed || typeof parsed !== "object" || Array.isArray(parsed)) return {};
    const source = "threads" in parsed && parsed.threads && typeof parsed.threads === "object" && !Array.isArray(parsed.threads)
      ? parsed.threads as Record<string, unknown>
      : parsed as Record<string, unknown>;
    const result: Record<string, V2ThreadScrollMemory> = {};
    for (const [threadID, value] of Object.entries(source)) {
      if (!threadID) continue;
      const clean = sanitizeScrollMemoryEntry(value);
      if (clean) result[threadID] = clean;
    }
    return result;
  } catch {
    return {};
  }
}

export function readPersistedThreadScrollMemory(scope: string, threadID: string): V2ThreadScrollMemory | null {
  if (!threadID) return null;
  const map = readPersistedScrollMemoryMap(scope);
  return Object.hasOwn(map, threadID) ? map[threadID] : null;
}

export function writePersistedThreadScrollMemory(
  scope: string,
  threadID: string,
  entry: V2ThreadScrollMemory,
  maxThreads = MAX_SCROLL_MEMORY_THREADS,
): void {
  const normalized = scope.trim();
  if (!normalized || !threadID || typeof window === "undefined") return;
  try {
    const existing = readPersistedScrollMemoryMap(normalized);
    const merged: Record<string, V2ThreadScrollMemory> = { ...existing, [threadID]: entry };
    const bounded = Object.entries(merged)
      .sort((left, right) => {
        if (left[0] === threadID) return -1;
        if (right[0] === threadID) return 1;
        return right[1].updatedAt - left[1].updatedAt;
      })
      .slice(0, Math.max(1, maxThreads));
    window.localStorage.setItem(scrollMemoryStorageKey(normalized), JSON.stringify(Object.fromEntries(bounded)));
  } catch {
    // Quota or storage restrictions degrade cleanly to the in-memory QueryClient cache.
  }
}

export function findRawConversationEntryElement(scroller: HTMLElement, key: string): HTMLElement | null {
  if (!key) return null;
  const topLevel = scroller.querySelectorAll<HTMLElement>(".v2-conv-entries > [data-conv-entry-key]");
  for (const node of topLevel) {
    if (node.dataset.convEntryKey === key) return node;
  }
  const all = scroller.querySelectorAll<HTMLElement>("[data-conv-entry-key]");
  for (const node of all) {
    if (node.dataset.convEntryKey === key) return node;
  }
  return null;
}

export function findConversationEntryElement(scroller: HTMLElement, key: string): HTMLElement | null {
  const node = findRawConversationEntryElement(scroller, key);
  if (!node) return null;
  if (node.closest("[hidden]")) {
    return node.closest<HTMLElement>(".v2-task-turn[data-conv-entry-key]") ?? node;
  }
  return node;
}

export function computeUnreadTail(
  entries: ReadonlyArray<ScrollMemoryEntryLike>,
  seenEntryCount: number,
  lastSeenTailID: string | null | undefined,
): { firstUnreadIndex: number; unreadCount: number; resolvedSeenCount: number; resolvedTailID: string | null } {
  if (entries.length === 0) {
    return { firstUnreadIndex: 0, unreadCount: 0, resolvedSeenCount: 0, resolvedTailID: null };
  }
  if (lastSeenTailID) {
    for (let index = entries.length - 1; index >= 0; index--) {
      if (entries[index]!.id === lastSeenTailID) {
        const firstUnreadIndex = index + 1;
        const unreadCount = Math.max(0, entries.length - firstUnreadIndex);
        return {
          firstUnreadIndex,
          unreadCount,
          resolvedSeenCount: firstUnreadIndex,
          resolvedTailID: lastSeenTailID,
        };
      }
    }
    // If a provisional tail ID was replaced in-place by its durable counterpart without
    // growing past the previously seen count, keep unreadCount at 0 and adopt the new tail ID.
    if (entries.length <= seenEntryCount) {
      const currentTail = entries[entries.length - 1]!.id;
      return {
        firstUnreadIndex: entries.length,
        unreadCount: 0,
        resolvedSeenCount: entries.length,
        resolvedTailID: currentTail,
      };
    }
  }
  const clampedSeen = Math.max(0, Math.min(entries.length, seenEntryCount));
  const unreadCount = Math.max(0, entries.length - clampedSeen);
  const resolvedTailID = clampedSeen > 0 ? (entries[clampedSeen - 1]?.id ?? null) : null;
  return {
    firstUnreadIndex: clampedSeen,
    unreadCount,
    resolvedSeenCount: clampedSeen,
    resolvedTailID,
  };
}

function hasMeasuredGeometry(scroller: HTMLElement, target: HTMLElement): boolean {
  const rect = target.getBoundingClientRect();
  const scrollerRect = scroller.getBoundingClientRect();
  return rect.height > 0 || rect.top !== scrollerRect.top || target.offsetHeight > 0 || target.offsetTop > 0;
}

function elementScrollCoordinateTop(scroller: HTMLElement, target: HTMLElement): number {
  const rect = target.getBoundingClientRect();
  const scrollerRect = scroller.getBoundingClientRect();
  if (rect.height > 0 || rect.top !== scrollerRect.top) {
    return scroller.scrollTop + (rect.top - scrollerRect.top);
  }
  return target.offsetTop;
}

export function captureScrollAnchor(scroller: HTMLElement): { anchorKey: string | null; anchorOffset: number } {
  const scrollerRect = scroller.getBoundingClientRect();
  const candidates = scroller.querySelectorAll<HTMLElement>(".v2-conv-entries > [data-conv-entry-key]");
  const list = candidates.length > 0 ? candidates : scroller.querySelectorAll<HTMLElement>("[data-conv-entry-key]");
  for (const node of list) {
    if (node.closest("[hidden]")) continue;
    const rect = node.getBoundingClientRect();
    if (rect.height > 0) {
      if (rect.bottom > scrollerRect.top + 2) {
        return { anchorKey: node.dataset.convEntryKey ?? null, anchorOffset: rect.top - scrollerRect.top };
      }
      continue;
    }
    if (node.offsetHeight > 0 && node.offsetTop + node.offsetHeight > scroller.scrollTop + 2) {
      return { anchorKey: node.dataset.convEntryKey ?? null, anchorOffset: node.offsetTop - scroller.scrollTop };
    }
  }
  return { anchorKey: null, anchorOffset: 0 };
}

export function resolveAnchorScrollTop(
  scroller: HTMLElement,
  anchorKey: string | null,
  anchorOffset: number,
  fallbackTop: number,
): number {
  if (anchorKey) {
    const target = findConversationEntryElement(scroller, anchorKey);
    if (target && hasMeasuredGeometry(scroller, target)) {
      return Math.max(0, elementScrollCoordinateTop(scroller, target) - anchorOffset);
    }
  }
  return Math.max(0, fallbackTop);
}

export function resolveUnreadStartAnchor(
  scroller: HTMLElement,
  entries: ReadonlyArray<ScrollMemoryEntryLike>,
  saved: V2ThreadScrollMemory,
): {
  shouldAnchor: boolean;
  anchorKey: string | null;
  targetTop: number;
  unreadCount: number;
  resolvedSeenCount: number;
  resolvedTailID: string | null;
} {
  const tail = computeUnreadTail(entries, saved.seenEntryCount, saved.lastSeenTailID);
  if (tail.unreadCount <= 0) {
    return {
      shouldAnchor: false,
      anchorKey: null,
      targetTop: saved.top,
      unreadCount: 0,
      resolvedSeenCount: tail.resolvedSeenCount,
      resolvedTailID: tail.resolvedTailID,
    };
  }
  const { unreadCount, firstUnreadIndex, resolvedSeenCount, resolvedTailID } = tail;
  const firstUnread = entries[firstUnreadIndex];
  if (!firstUnread || scroller.clientHeight <= 0) {
    return {
      shouldAnchor: false,
      anchorKey: null,
      targetTop: saved.top,
      unreadCount,
      resolvedSeenCount,
      resolvedTailID,
    };
  }
  const precedingPrompt = firstUnreadIndex > 0 && firstUnread.kind !== "user" && entries[firstUnreadIndex - 1]?.kind === "user"
    ? entries[firstUnreadIndex - 1]
    : undefined;
  const anchorEntry = precedingPrompt ?? firstUnread;

  const firstUnreadEl = findConversationEntryElement(scroller, firstUnread.id)
    ?? findConversationEntryElement(scroller, `task:${firstUnread.id}`);
  const anchorEl = findConversationEntryElement(scroller, anchorEntry.id)
    ?? findConversationEntryElement(scroller, `task:${anchorEntry.id}`)
    ?? firstUnreadEl;

  let firstUnreadTop = 0;
  let anchorTop = 0;
  let unreadHeight = 0;

  if (firstUnreadEl && hasMeasuredGeometry(scroller, firstUnreadEl)) {
    firstUnreadTop = elementScrollCoordinateTop(scroller, firstUnreadEl);
    anchorTop = anchorEl && hasMeasuredGeometry(scroller, anchorEl)
      ? elementScrollCoordinateTop(scroller, anchorEl)
      : firstUnreadTop;
    let measuredSum = 0;
    for (let index = firstUnreadIndex; index < entries.length; index++) {
      const node = findConversationEntryElement(scroller, entries[index]!.id);
      if (node) {
        const rect = node.getBoundingClientRect();
        measuredSum += rect.height > 0 ? rect.height : node.offsetHeight;
      }
    }
    unreadHeight = Math.max(scroller.scrollHeight - firstUnreadTop, measuredSum);
  } else if (scroller.scrollHeight > scroller.clientHeight) {
    firstUnreadTop = resolvedSeenCount > 0
      ? Math.round((resolvedSeenCount / entries.length) * scroller.scrollHeight)
      : 0;
    anchorTop = precedingPrompt
      ? Math.round(((firstUnreadIndex - 1) / entries.length) * scroller.scrollHeight)
      : firstUnreadTop;
    unreadHeight = scroller.scrollHeight - firstUnreadTop;
  }

  const shouldAnchor = unreadHeight > scroller.clientHeight * UNREAD_OVERFLOW_VIEWPORT_RATIO;
  return {
    shouldAnchor,
    anchorKey: anchorEntry.id,
    targetTop: Math.max(0, anchorTop),
    unreadCount,
    resolvedSeenCount,
    resolvedTailID,
  };
}

interface RestoreGuardState {
  threadID: string;
  targetFollowing: boolean;
  targetTop: number;
  anchorKey: string | null;
  anchorOffset: number;
  lastProgrammaticTop: number;
}

export interface UseV2ScrollMemoryOptions {
  threadID: string;
  scrollRef: RefObject<HTMLDivElement | null>;
  entries: ReadonlyArray<ScrollMemoryEntryLike>;
  ready: boolean;
  detailReady?: boolean;
  revision?: unknown;
  scope?: string;
  readingHistoryWindow?: boolean;
  isPaginatingOlder?: () => boolean;
  onAfterScrollStateChange?: () => void;
}

export interface V2ScrollMemoryController {
  following: boolean;
  followLatestRef: MutableRefObject<boolean>;
  readingThreadRef: MutableRefObject<string>;
  unreadCount: number;
  isUnreadAnchored: boolean;
  showScrollToBottom: boolean;
  onScroll: (event: UIEvent<HTMLDivElement>) => void;
  jumpToBottom: () => void;
  recordProgrammaticScroll: (scrollTop: number, following: boolean, anchorKey?: string | null, anchorOffset?: number) => void;
  resetToLatest: () => void;
}

export function useV2ScrollMemory({
  threadID,
  scrollRef,
  entries,
  ready,
  detailReady = true,
  revision,
  scope,
  readingHistoryWindow = false,
  isPaginatingOlder,
  onAfterScrollStateChange,
}: UseV2ScrollMemoryOptions): V2ScrollMemoryController {
  const connectionScope = useConnectionStore((state) => state.health?.data_store_id ?? "");
  const resolvedScope = scope ?? connectionScope;
  const queryClient = useQueryClient();

  // Retain durable query cache entries across arbitrary tab switches (`gcTime: Infinity`)
  // without triggering redundant component re-renders when scroll writes update cache data.
  useQuery<V2ThreadScrollMemory | null>({
    queryKey: scrollMemoryQueryKey(resolvedScope, threadID),
    queryFn: () => readPersistedThreadScrollMemory(resolvedScope, threadID),
    initialData: () => readPersistedThreadScrollMemory(resolvedScope, threadID),
    enabled: Boolean(threadID),
    gcTime: Number.POSITIVE_INFINITY,
    staleTime: Number.POSITIVE_INFINITY,
    notifyOnChangeProps: [],
  });
  useQuery<{ top: number; following: boolean } | null>({
    queryKey: ["v2", "reading", threadID],
    queryFn: () => null,
    enabled: false,
    gcTime: Number.POSITIVE_INFINITY,
    staleTime: Number.POSITIVE_INFINITY,
    notifyOnChangeProps: [],
  });

  const readingThreadRef = useRef("");
  const followLatestRef = useRef(true);
  const seenEntryCountRef = useRef(0);
  const lastSeenTailIDRef = useRef<string | null>(null);
  const seenContentHeightRef = useRef(0);
  const scrollSizeRef = useRef<{ content: number; viewport: number } | null>(null);
  const restoringRef = useRef<RestoreGuardState | null>(null);
  const rafRef = useRef<number | null>(null);
  const entriesRef = useRef(entries);
  entriesRef.current = entries;

  const [following, setFollowing] = useState(true);
  const [unreadCount, setUnreadCount] = useState(0);
  const [hasContentGrowth, setHasContentGrowth] = useState(false);
  const [distanceFromBottom, setDistanceFromBottom] = useState(0);

  const cancelRestoreFrames = useCallback(() => {
    if (rafRef.current !== null && typeof cancelAnimationFrame === "function") {
      cancelAnimationFrame(rafRef.current);
      rafRef.current = null;
    }
  }, []);

  useEffect(() => () => cancelRestoreFrames(), [cancelRestoreFrames, threadID]);

  const persistMemory = useCallback((
    targetThreadID: string,
    next: V2ThreadScrollMemory,
    syncLegacyReading = true,
  ) => {
    if (!targetThreadID) return;
    const key = scrollMemoryQueryKey(resolvedScope, targetThreadID);
    const existing = queryClient.getQueryData<V2ThreadScrollMemory | null>(key);
    const unchanged = Boolean(
      existing &&
      existing.top === next.top &&
      existing.following === next.following &&
      existing.anchorKey === next.anchorKey &&
      existing.anchorOffset === next.anchorOffset &&
      existing.seenEntryCount === next.seenEntryCount &&
      (existing.lastSeenTailID ?? null) === (next.lastSeenTailID ?? null),
    );
    if (!unchanged) {
      queryClient.setQueryData<V2ThreadScrollMemory>(key, next);
      writePersistedThreadScrollMemory(resolvedScope, targetThreadID, next);
    }
    if (syncLegacyReading) {
      const prevReading = queryClient.getQueryData<{ top: number; following: boolean }>(["v2", "reading", targetThreadID]);
      if (!prevReading || prevReading.top !== next.top || prevReading.following !== next.following) {
        queryClient.setQueryData(["v2", "reading", targetThreadID], { top: next.top, following: next.following });
      }
    }
  }, [queryClient, resolvedScope]);

  const readSavedMemory = useCallback((targetThreadID: string): V2ThreadScrollMemory | null => {
    if (!targetThreadID) return null;
    const cached = queryClient.getQueryData<V2ThreadScrollMemory | null>(scrollMemoryQueryKey(resolvedScope, targetThreadID));
    if (cached) return cached;
    const persisted = readPersistedThreadScrollMemory(resolvedScope, targetThreadID);
    if (persisted) {
      queryClient.setQueryData<V2ThreadScrollMemory>(scrollMemoryQueryKey(resolvedScope, targetThreadID), persisted);
    }
    return persisted;
  }, [queryClient, resolvedScope]);

  const armProgrammaticRestore = useCallback((
    element: HTMLDivElement,
    guard: Omit<RestoreGuardState, "lastProgrammaticTop">,
  ) => {
    cancelRestoreFrames();
    const desiredTop = guard.targetFollowing
      ? element.scrollHeight
      : resolveAnchorScrollTop(element, guard.anchorKey, guard.anchorOffset, guard.targetTop);
    element.scrollTop = desiredTop;
    const state: RestoreGuardState = {
      ...guard,
      targetTop: desiredTop,
      lastProgrammaticTop: element.scrollTop,
    };
    restoringRef.current = state;

    if (typeof requestAnimationFrame !== "function") return;
    const settleFrame = (remaining: number) => {
      rafRef.current = requestAnimationFrame(() => {
        rafRef.current = null;
        const current = restoringRef.current;
        const scroller = scrollRef.current;
        if (!current || current.threadID !== guard.threadID || !scroller) return;
        if (!current.targetFollowing) {
          const resolved = resolveAnchorScrollTop(scroller, current.anchorKey, current.anchorOffset, current.targetTop);
          current.targetTop = resolved;
          scroller.scrollTop = resolved;
          current.lastProgrammaticTop = scroller.scrollTop;
          const maxScroll = Math.max(0, scroller.scrollHeight - scroller.clientHeight);
          scrollSizeRef.current = { content: scroller.scrollHeight, viewport: scroller.clientHeight };
          setDistanceFromBottom(Math.max(0, scroller.scrollHeight - scroller.clientHeight - scroller.scrollTop));
          if (remaining > 1) {
            settleFrame(remaining - 1);
            return;
          }
          // Release only when content height can actually accommodate targetTop;
          // if async content is still expanding, ResizeObserver finishes the restore.
          if (maxScroll >= current.targetTop) {
            restoringRef.current = null;
          }
        } else {
          scroller.scrollTop = scroller.scrollHeight;
          current.lastProgrammaticTop = scroller.scrollTop;
          scrollSizeRef.current = { content: scroller.scrollHeight, viewport: scroller.clientHeight };
          setDistanceFromBottom(0);
          if (remaining > 1) settleFrame(remaining - 1);
          else restoringRef.current = null;
        }
      });
    };
    settleFrame(2);
  }, [cancelRestoreFrames, scrollRef]);

  // Release programmatic restore guard immediately on physical user scroll gestures.
  useEffect(() => {
    const element = scrollRef.current;
    if (!element) return;
    const releaseOnUserGesture = () => {
      if (restoringRef.current?.threadID === threadID) {
        cancelRestoreFrames();
        restoringRef.current = null;
      }
    };
    element.addEventListener("wheel", releaseOnUserGesture, { passive: true });
    element.addEventListener("touchstart", releaseOnUserGesture, { passive: true });
    element.addEventListener("pointerdown", releaseOnUserGesture, { passive: true });
    return () => {
      element.removeEventListener("wheel", releaseOnUserGesture);
      element.removeEventListener("touchstart", releaseOnUserGesture);
      element.removeEventListener("pointerdown", releaseOnUserGesture);
    };
  }, [cancelRestoreFrames, detailReady, scrollRef, threadID]);

  useLayoutEffect(() => {
    const element = scrollRef.current;
    if (!element || !ready) return;

    if (readingThreadRef.current !== threadID) {
      readingThreadRef.current = threadID;
      if (readingHistoryWindow) {
        followLatestRef.current = false;
        setFollowing(false);
        setUnreadCount(0);
        setHasContentGrowth(false);
        seenContentHeightRef.current = element.scrollHeight;
        scrollSizeRef.current = { content: element.scrollHeight, viewport: element.clientHeight };
        setDistanceFromBottom(Math.max(0, element.scrollHeight - element.clientHeight - element.scrollTop));
        return;
      }

      const currentTailID = entries.length > 0 ? entries[entries.length - 1]!.id : null;
      const saved = readSavedMemory(threadID);
      if (!saved) {
        followLatestRef.current = true;
        seenEntryCountRef.current = entries.length;
        lastSeenTailIDRef.current = currentTailID;
        setFollowing(true);
        setUnreadCount(0);
        setHasContentGrowth(false);
        element.scrollTop = element.scrollHeight;
        seenContentHeightRef.current = element.scrollHeight;
        scrollSizeRef.current = { content: element.scrollHeight, viewport: element.clientHeight };
        setDistanceFromBottom(Math.max(0, element.scrollHeight - element.clientHeight - element.scrollTop));
        persistMemory(threadID, {
          top: element.scrollTop,
          following: true,
          anchorKey: null,
          anchorOffset: 0,
          seenEntryCount: entries.length,
          lastSeenTailID: currentTailID,
          updatedAt: Date.now(),
        }, false);
        return;
      }

      if (saved.following) {
        const unread = resolveUnreadStartAnchor(element, entries, saved);
        if (unread.shouldAnchor) {
          followLatestRef.current = false;
          seenEntryCountRef.current = unread.resolvedSeenCount;
          lastSeenTailIDRef.current = unread.resolvedTailID;
          setFollowing(false);
          setUnreadCount(unread.unreadCount);
          setHasContentGrowth(false);
          armProgrammaticRestore(element, {
            threadID,
            targetFollowing: false,
            targetTop: unread.targetTop,
            anchorKey: unread.anchorKey,
            anchorOffset: 0,
          });
          seenContentHeightRef.current = element.scrollHeight;
          scrollSizeRef.current = { content: element.scrollHeight, viewport: element.clientHeight };
          setDistanceFromBottom(Math.max(0, element.scrollHeight - element.clientHeight - element.scrollTop));
          persistMemory(threadID, {
            top: element.scrollTop,
            following: false,
            anchorKey: unread.anchorKey,
            anchorOffset: 0,
            seenEntryCount: unread.resolvedSeenCount,
            lastSeenTailID: unread.resolvedTailID,
            updatedAt: Date.now(),
          });
          return;
        }

        followLatestRef.current = true;
        seenEntryCountRef.current = entries.length;
        lastSeenTailIDRef.current = currentTailID;
        setFollowing(true);
        setUnreadCount(0);
        setHasContentGrowth(false);
        element.scrollTop = element.scrollHeight;
        seenContentHeightRef.current = element.scrollHeight;
        scrollSizeRef.current = { content: element.scrollHeight, viewport: element.clientHeight };
        setDistanceFromBottom(Math.max(0, element.scrollHeight - element.clientHeight - element.scrollTop));
        persistMemory(threadID, {
          top: element.scrollTop,
          following: true,
          anchorKey: null,
          anchorOffset: 0,
          seenEntryCount: entries.length,
          lastSeenTailID: currentTailID,
          updatedAt: Date.now(),
        }, false);
        return;
      }

      // saved.following === false: restore mid-thread reading position with clamp guard.
      const tail = computeUnreadTail(entries, saved.seenEntryCount, saved.lastSeenTailID);
      followLatestRef.current = false;
      seenEntryCountRef.current = tail.resolvedSeenCount;
      lastSeenTailIDRef.current = tail.resolvedTailID;
      setFollowing(false);
      setUnreadCount(tail.unreadCount);
      setHasContentGrowth(false);
      armProgrammaticRestore(element, {
        threadID,
        targetFollowing: false,
        targetTop: saved.top,
        anchorKey: saved.anchorKey,
        anchorOffset: saved.anchorOffset,
      });
      seenContentHeightRef.current = element.scrollHeight;
      scrollSizeRef.current = { content: element.scrollHeight, viewport: element.clientHeight };
      setDistanceFromBottom(Math.max(0, element.scrollHeight - element.clientHeight - element.scrollTop));
      return;
    }

    // Same thread: skip automatic scrolling while older transcript pagination owns the anchor.
    if (isPaginatingOlder?.()) return;
    if (readingHistoryWindow) return;

    const currentTailID = entries.length > 0 ? entries[entries.length - 1]!.id : null;
    if (followLatestRef.current) {
      element.scrollTop = element.scrollHeight;
      seenEntryCountRef.current = entries.length;
      lastSeenTailIDRef.current = currentTailID;
      setUnreadCount(0);
      setHasContentGrowth(false);
      seenContentHeightRef.current = element.scrollHeight;
      scrollSizeRef.current = { content: element.scrollHeight, viewport: element.clientHeight };
      setDistanceFromBottom(0);
      persistMemory(threadID, {
        top: element.scrollTop,
        following: true,
        anchorKey: null,
        anchorOffset: 0,
        seenEntryCount: entries.length,
        lastSeenTailID: currentTailID,
        updatedAt: Date.now(),
      }, false);
    } else {
      const tail = computeUnreadTail(entries, seenEntryCountRef.current, lastSeenTailIDRef.current);
      seenEntryCountRef.current = tail.resolvedSeenCount;
      lastSeenTailIDRef.current = tail.resolvedTailID;
      setUnreadCount(tail.unreadCount);
      if (element.scrollHeight > seenContentHeightRef.current + 8) {
        setHasContentGrowth(true);
      }
      setDistanceFromBottom(Math.max(0, element.scrollHeight - element.clientHeight - element.scrollTop));
    }
  }, [
    armProgrammaticRestore,
    entries,
    isPaginatingOlder,
    persistMemory,
    readSavedMemory,
    readingHistoryWindow,
    ready,
    revision,
    scrollRef,
    threadID,
  ]);

  useLayoutEffect(() => {
    const element = scrollRef.current;
    const content = element?.firstElementChild;
    if (!element || !content || typeof ResizeObserver === "undefined") return;
    const observer = new ResizeObserver(() => {
      if (readingThreadRef.current !== threadID) return;
      const guard = restoringRef.current;
      if (guard && guard.threadID === threadID && !guard.targetFollowing) {
        const resolved = resolveAnchorScrollTop(element, guard.anchorKey, guard.anchorOffset, guard.targetTop);
        guard.targetTop = resolved;
        element.scrollTop = resolved;
        guard.lastProgrammaticTop = element.scrollTop;
        scrollSizeRef.current = { content: element.scrollHeight, viewport: element.clientHeight };
        setDistanceFromBottom(Math.max(0, element.scrollHeight - element.clientHeight - element.scrollTop));
        if (Math.max(0, element.scrollHeight - element.clientHeight) >= resolved) {
          restoringRef.current = null;
        }
        return;
      }
      if (followLatestRef.current && !isPaginatingOlder?.() && !readingHistoryWindow) {
        element.scrollTop = element.scrollHeight;
      }
      scrollSizeRef.current = { content: element.scrollHeight, viewport: element.clientHeight };
      setDistanceFromBottom(Math.max(0, element.scrollHeight - element.clientHeight - element.scrollTop));
    });
    observer.observe(element);
    observer.observe(content);
    return () => observer.disconnect();
  }, [detailReady, isPaginatingOlder, readingHistoryWindow, scrollRef, threadID]);

  const onScroll = useCallback((event: UIEvent<HTMLDivElement>) => {
    const element = event.currentTarget;
    const size = scrollSizeRef.current;
    const paginating = Boolean(isPaginatingOlder?.());

    // Guard against transient layout clamps while restoring non-following scroll state.
    const guard = restoringRef.current;
    if (guard && guard.threadID === threadID && !guard.targetFollowing) {
      const maxScroll = Math.max(0, element.scrollHeight - element.clientHeight);
      if (element.scrollTop === guard.lastProgrammaticTop || maxScroll < guard.targetTop) {
        scrollSizeRef.current = { content: element.scrollHeight, viewport: element.clientHeight };
        setDistanceFromBottom(Math.max(0, element.scrollHeight - element.clientHeight - element.scrollTop));
        return;
      }
      cancelRestoreFrames();
      restoringRef.current = null;
    }

    // Replacing the editor can briefly expand the transcript, clamp scrollTop, then shrink it
    // before ResizeObserver runs. Preserve following latest across that transient viewport resize.
    if (followLatestRef.current && size && size.viewport > 0 && size.content === element.scrollHeight &&
      size.viewport !== element.clientHeight && !paginating && !readingHistoryWindow) {
      element.scrollTop = element.scrollHeight;
    }

    scrollSizeRef.current = { content: element.scrollHeight, viewport: element.clientHeight };
    const dist = Math.max(0, element.scrollHeight - element.clientHeight - element.scrollTop);
    const nextFollowing = dist <= SCROLL_FOLLOW_THRESHOLD_PX;
    followLatestRef.current = nextFollowing;
    setFollowing(nextFollowing);
    setDistanceFromBottom(dist);

    if (nextFollowing) {
      seenEntryCountRef.current = entriesRef.current.length;
      lastSeenTailIDRef.current = entriesRef.current.length > 0 ? entriesRef.current[entriesRef.current.length - 1]!.id : null;
      seenContentHeightRef.current = element.scrollHeight;
      setUnreadCount(0);
      setHasContentGrowth(false);
    } else if (seenContentHeightRef.current <= 0) {
      seenContentHeightRef.current = element.scrollHeight;
    }

    if (!readingHistoryWindow) {
      const anchor = nextFollowing ? { anchorKey: null, anchorOffset: 0 } : captureScrollAnchor(element);
      persistMemory(threadID, {
        top: element.scrollTop,
        following: nextFollowing,
        anchorKey: anchor.anchorKey,
        anchorOffset: anchor.anchorOffset,
        seenEntryCount: seenEntryCountRef.current,
        lastSeenTailID: lastSeenTailIDRef.current,
        updatedAt: Date.now(),
      });
    } else {
      queryClient.setQueryData(["v2", "reading", threadID], { top: element.scrollTop, following: nextFollowing });
    }
    onAfterScrollStateChange?.();
  }, [cancelRestoreFrames, isPaginatingOlder, onAfterScrollStateChange, persistMemory, queryClient, readingHistoryWindow, threadID]);

  const jumpToBottom = useCallback(() => {
    cancelRestoreFrames();
    restoringRef.current = null;
    followLatestRef.current = true;
    const latestEntries = entriesRef.current;
    seenEntryCountRef.current = latestEntries.length;
    lastSeenTailIDRef.current = latestEntries.length > 0 ? latestEntries[latestEntries.length - 1]!.id : null;
    setFollowing(true);
    setUnreadCount(0);
    setHasContentGrowth(false);
    setDistanceFromBottom(0);
    const element = scrollRef.current;
    if (element) {
      element.scrollTop = element.scrollHeight;
      seenContentHeightRef.current = element.scrollHeight;
      scrollSizeRef.current = { content: element.scrollHeight, viewport: element.clientHeight };
      persistMemory(threadID, {
        top: element.scrollTop,
        following: true,
        anchorKey: null,
        anchorOffset: 0,
        seenEntryCount: seenEntryCountRef.current,
        lastSeenTailID: lastSeenTailIDRef.current,
        updatedAt: Date.now(),
      });
    }
    onAfterScrollStateChange?.();
  }, [cancelRestoreFrames, onAfterScrollStateChange, persistMemory, scrollRef, threadID]);

  const recordProgrammaticScroll = useCallback((
    scrollTop: number,
    nextFollowing: boolean,
    explicitAnchorKey?: string | null,
    explicitAnchorOffset = 0,
  ) => {
    cancelRestoreFrames();
    restoringRef.current = null;
    followLatestRef.current = nextFollowing;
    setFollowing(nextFollowing);
    const element = scrollRef.current;
    const dist = element ? Math.max(0, element.scrollHeight - element.clientHeight - scrollTop) : 0;
    setDistanceFromBottom(dist);
    const latestEntries = entriesRef.current;
    if (nextFollowing) {
      seenEntryCountRef.current = latestEntries.length;
      lastSeenTailIDRef.current = latestEntries.length > 0 ? latestEntries[latestEntries.length - 1]!.id : null;
      if (element) seenContentHeightRef.current = element.scrollHeight;
      setUnreadCount(0);
      setHasContentGrowth(false);
    } else {
      // Re-anchor seen count via lastSeenTailID so older-page prepends never create false unread counts.
      const tail = computeUnreadTail(latestEntries, seenEntryCountRef.current, lastSeenTailIDRef.current);
      seenEntryCountRef.current = tail.resolvedSeenCount;
      lastSeenTailIDRef.current = tail.resolvedTailID;
      if (element) seenContentHeightRef.current = element.scrollHeight;
      setUnreadCount(tail.unreadCount);
    }
    const anchor = nextFollowing
      ? { anchorKey: null, anchorOffset: 0 }
      : explicitAnchorKey !== undefined
        ? { anchorKey: explicitAnchorKey, anchorOffset: explicitAnchorOffset }
        : element ? captureScrollAnchor(element) : { anchorKey: null, anchorOffset: 0 };
    persistMemory(threadID, {
      top: scrollTop,
      following: nextFollowing,
      anchorKey: anchor.anchorKey,
      anchorOffset: anchor.anchorOffset,
      seenEntryCount: seenEntryCountRef.current,
      lastSeenTailID: lastSeenTailIDRef.current,
      updatedAt: Date.now(),
    });
  }, [cancelRestoreFrames, persistMemory, scrollRef, threadID]);

  const resetToLatest = useCallback(() => {
    cancelRestoreFrames();
    restoringRef.current = null;
    followLatestRef.current = true;
    readingThreadRef.current = "";
    const latestEntries = entriesRef.current;
    seenEntryCountRef.current = latestEntries.length;
    lastSeenTailIDRef.current = latestEntries.length > 0 ? latestEntries[latestEntries.length - 1]!.id : null;
    setFollowing(true);
    setUnreadCount(0);
    setHasContentGrowth(false);
    setDistanceFromBottom(0);
    persistMemory(threadID, {
      top: 0,
      following: true,
      anchorKey: null,
      anchorOffset: 0,
      seenEntryCount: latestEntries.length,
      lastSeenTailID: lastSeenTailIDRef.current,
      updatedAt: Date.now(),
    });
  }, [cancelRestoreFrames, persistMemory, threadID]);

  const isUnreadAnchored = !following && (unreadCount > 0 || hasContentGrowth);
  const showScrollToBottom = !readingHistoryWindow && !following && (
    isUnreadAnchored || distanceFromBottom > SCROLL_TO_BOTTOM_PILL_THRESHOLD_PX
  );

  return useMemo(() => ({
    following,
    followLatestRef,
    readingThreadRef,
    unreadCount,
    isUnreadAnchored,
    showScrollToBottom,
    onScroll,
    jumpToBottom,
    recordProgrammaticScroll,
    resetToLatest,
  }), [
    following,
    isUnreadAnchored,
    jumpToBottom,
    onScroll,
    recordProgrammaticScroll,
    resetToLatest,
    showScrollToBottom,
    unreadCount,
  ]);
}
