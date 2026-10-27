import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import type { APIClient } from "../api/client";
import type { ThreadReplyUpdatesView } from "../api/types";
import { useConnectionStore } from "../state/connection";
import { useV2PersistentState, useV2RecoveryStore } from "./recovery-storage";

const storageKey = "reply-read-state";
type ThreadReadState = { pending: string[]; seen: string[]; manualUnread: boolean };
type ReadState = { cursor: string; threads: Record<string, ThreadReadState> };
const emptyThread: ThreadReadState = { pending: [], seen: [], manualUnread: false };
const emptyState: ReadState = { cursor: "", threads: {} };
const object = (value: unknown): value is Record<string, unknown> => Boolean(value) && typeof value === "object" && !Array.isArray(value);
const identity = (value: unknown): value is string => typeof value === "string" && value.length > 0 &&
  value.length <= 4096 && value.trim() === value && !/[\u0000-\u001f\u007f]/u.test(value);
const identities = (value: unknown): value is string[] => Array.isArray(value) && value.every(identity) && new Set(value).size === value.length;

function validState(value: unknown): value is ReadState {
  return object(value) && Object.keys(value).length === 2 && (value.cursor === "" || identity(value.cursor)) && object(value.threads) &&
    Object.entries(value.threads).every(([id, thread]) => identity(id) && object(thread) && Object.keys(thread).length === 3 &&
      identities(thread.pending) && identities(thread.seen) && typeof thread.manualUnread === "boolean");
}

function validUpdates(value: unknown): value is ThreadReplyUpdatesView {
  return object(value) && value.version === "thread_reply_updates.v1" && identity(value.data_store_id) && identity(value.cursor) &&
    typeof value.has_more === "boolean" && Array.isArray(value.items) && value.items.length <= 100 && value.items.every((item) =>
      object(item) && identity(item.thread_id) && identity(item.message_id) &&
      (item.thread_status === "active" || item.thread_status === "archived") &&
      Number.isSafeInteger(item.run_ordinal) && Number(item.run_ordinal) > 0 &&
      Number.isSafeInteger(item.sequence) && Number(item.sequence) > 0 && typeof item.created_at === "string");
}

function threadState(state: ReadState, id: string): ThreadReadState {
  return Object.hasOwn(state.threads, id) ? state.threads[id] : emptyThread;
}

function changeThread(state: ReadState, id: string, change: (current: ThreadReadState) => ThreadReadState): ReadState {
  const current = threadState(state, id);
  const next = change(current);
  if (next === current) return state;
  const threads = { ...state.threads };
  if (!next.pending.length && !next.seen.length && !next.manualUnread) delete threads[id];
  else threads[id] = next;
  return { ...state, threads };
}

export type V2ReplyReading = {
  observe: (threadID: string, messageIDs: string[]) => void;
  prepare: (threadID: string) => Promise<string[] | null>;
  confirm: (threadID: string, messageIDs: string[]) => void;
};

export function useV2ThreadUnread(client: APIClient, scopeID: string | undefined) {
  const recovery = useV2RecoveryStore();
  const [stored, setStored] = useV2PersistentState<unknown>(storageKey, emptyState);
  const valid = validState(stored);
  const state = valid ? stored : emptyState;
  const current = useRef(state);
  current.current = state;
  const [error, setError] = useState(false);
  const syncRef = useRef<() => Promise<boolean>>(() => Promise.resolve(false));
  useEffect(() => { if (!valid) recovery?.reject(storageKey); }, [valid, recovery]);
  const update = useCallback((change: (state: ReadState) => ReadState) => {
    const next = change(current.current);
    if (next === current.current) return;
    current.current = next;
    // The checkpoint and pending identities share one storage write. A failed
    // save leaves the previous complete checkpoint available for a replay.
    setStored(next);
  }, [setStored]);

  useEffect(() => {
    if (!scopeID) return;
    const controller = new AbortController();
    let active = true;
    let inFlight: Promise<boolean> | null = null;
    let timer: number | undefined;
    const refreshScope = async () => {
      const health = await client.health(controller.signal);
      if (active && health.data_store_id && health.data_store_id !== scopeID) {
        useConnectionStore.getState().setHealth(health);
        return true;
      }
      return false;
    };
    const scan = async (): Promise<boolean> => {
      try {
        while (active) {
          const before = current.current;
          const response: unknown = await client.get("/threads/reply-updates", {
            limit: 100, ...(before.cursor ? { cursor: before.cursor } : {}),
          }, controller.signal);
          if (!active) return false;
          if (!validUpdates(response)) throw new Error("Invalid reply updates");
          if (response.data_store_id !== scopeID) {
            if (!await refreshScope()) setError(true);
            return false;
          }
          if (current.current.cursor !== before.cursor) continue;
          if (response.has_more && response.cursor === before.cursor) throw new Error("Reply cursor did not advance");
          update((latest) => {
            if (latest.cursor !== before.cursor) return latest;
            let next = latest;
            for (const item of response.items) next = changeThread(next, item.thread_id, (thread) => {
              if (thread.seen.includes(item.message_id)) return { ...thread, seen: thread.seen.filter((id) => id !== item.message_id) };
              return thread.pending.includes(item.message_id) ? thread : { ...thread, pending: [...thread.pending, item.message_id] };
            });
            if (!response.has_more) for (const [id, previous] of Object.entries(before.threads)) {
              if (previous.seen.length) next = changeThread(next, id, (thread) => {
                const seen = thread.seen.filter((messageID) => !previous.seen.includes(messageID));
                return seen.length === thread.seen.length ? thread : { ...thread, seen };
              });
            }
            return next.cursor === response.cursor ? next : { ...next, cursor: response.cursor };
          });
          setError(false);
          if (!response.has_more) return true;
        }
      } catch {
        if (active) {
          // A replaced database can reject its old cursor before returning a
          // response body. Refresh health without discarding the old namespace.
          try { if (await refreshScope()) return false; } catch { /* Retry the unchanged checkpoint. */ }
          if (active) setError(true);
        }
      }
      return false;
    };
    const sync = () => {
      if (inFlight) return inFlight;
      window.clearTimeout(timer);
      inFlight = scan().finally(() => {
        inFlight = null;
        if (active) timer = window.setTimeout(() => { void sync(); }, 5000);
      });
      return inFlight;
    };
    syncRef.current = sync;
    const focus = () => { void sync(); };
    window.addEventListener("focus", focus);
    void sync();
    return () => {
      active = false;
      controller.abort();
      window.clearTimeout(timer);
      window.removeEventListener("focus", focus);
      syncRef.current = () => Promise.resolve(false);
    };
  }, [client, scopeID, update]);

  const sync = useCallback(() => syncRef.current(), []);
  const setRead = useCallback((id: string, read: boolean) => update((value) => changeThread(value, id, (thread) => {
    if (read) return !thread.pending.length && !thread.manualUnread ? thread : { ...thread, pending: [], manualUnread: false };
    return thread.manualUnread ? thread : { ...thread, manualUnread: true };
  })), [update]);
  const open = useCallback((id: string) => update((value) => changeThread(value, id, (thread) =>
    thread.manualUnread ? { ...thread, manualUnread: false } : thread)), [update]);
  const observe = useCallback((id: string, messageIDs: string[]) => {
    if (!messageIDs.length) return;
    update((value) => changeThread(value, id, (thread) => {
      const read = new Set(messageIDs);
      const pending = thread.pending.filter((messageID) => !read.has(messageID));
      const seen = [...thread.seen, ...messageIDs.filter((messageID) => !thread.pending.includes(messageID) && !thread.seen.includes(messageID))];
      return pending.length === thread.pending.length && seen.length === thread.seen.length ? thread : { ...thread, pending, seen };
    }));
  }, [update]);
  const prepare = useCallback(async (id: string) => await sync() ? [...threadState(current.current, id).pending] : null, [sync]);
  const confirm = useCallback((id: string, messageIDs: string[]) => update((value) => changeThread(value, id, (thread) => {
    const read = new Set(messageIDs);
    const pending = thread.pending.filter((messageID) => !read.has(messageID));
    return pending.length === thread.pending.length ? thread : { ...thread, pending };
  })), [update]);
  const reading = useMemo<V2ReplyReading>(() => ({ observe, prepare, confirm }), [observe, prepare, confirm]);
  const unreadThreadIDs = useMemo(() => new Set(Object.entries(state.threads)
    .filter(([, thread]) => thread.manualUnread || thread.pending.length > 0).map(([id]) => id)), [state.threads]);
  return { unreadThreadIDs, setRead, open, reading, error, retry: sync };
}
