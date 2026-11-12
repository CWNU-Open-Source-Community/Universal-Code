import { useEffect, useState } from "react";
import { APIRequestError, type APIClient } from "../api/client";
import type { RunEventStreamView } from "../api/types";
import { desktopRuntimeActive } from "../lib/desktop-bridge";
import { useConnectionStore } from "../state/connection";

export type StreamStatus = "connecting" | "live" | "reconnecting" | "stopped";

const reconnectDelayMs = 1_000;
const desktopCaughtUpDelayMs = 250;
const maxImmediateDesktopPages = 4;
const maxLiveFrames = 500;
const maxRememberedRuns = 16;
const emptyFrames: RunEventStreamView[] = [];

interface RememberedDesktopRun {
  runID: string;
  cursor: string;
  frames: RunEventStreamView[];
}

const rememberedDesktopRuns = new Map<string, RememberedDesktopRun>();
const clientCacheIDs = new WeakMap<APIClient, number>();
let nextClientCacheID = 0;

function desktopRunCacheKey(client: APIClient, dataStoreID: string, runID: string): string {
  // An unidentified store cannot safely share a cursor across mounts.
  if (!dataStoreID) return "";
  let clientID = clientCacheIDs.get(client);
  if (clientID === undefined) {
    clientID = ++nextClientCacheID;
    clientCacheIDs.set(client, clientID);
  }
  return JSON.stringify([clientID, dataStoreID, runID]);
}

export function clearDesktopRunEventMemory(runID = ""): void {
  if (runID) {
    for (const [key, remembered] of rememberedDesktopRuns) {
      if (remembered.runID === runID) rememberedDesktopRuns.delete(key);
    }
    return;
  }
  rememberedDesktopRuns.clear();
}

function rememberDesktopRun(key: string, runID: string, cursor: string, frames: RunEventStreamView[]): void {
  if (!key) return;
  const remembered = rememberedDesktopRuns.get(key);
  if (remembered?.cursor === cursor && remembered.frames === frames) return;
  rememberedDesktopRuns.delete(key);
  rememberedDesktopRuns.set(key, { runID, cursor, frames });
  while (rememberedDesktopRuns.size > maxRememberedRuns) {
    const oldest = rememberedDesktopRuns.keys().next().value as string | undefined;
    if (!oldest) {
      break;
    }
    rememberedDesktopRuns.delete(oldest);
  }
}

function mergeFrames(current: RunEventStreamView[], incoming: RunEventStreamView[]): RunEventStreamView[] {
  if (incoming.length === 0) return current;
  const sequences = new Set(current.map((frame) => frame.sequence));
  const oldestRetained = current.length === maxLiveFrames ? current[0]!.sequence : 0;
  const added: RunEventStreamView[] = [];
  for (const frame of incoming) {
    // Persisted event sequences are immutable. Replayed envelopes can carry a
    // different request ID or cursor without changing the visible event.
    if (frame.sequence < oldestRetained || sequences.has(frame.sequence)) continue;
    sequences.add(frame.sequence);
    added.push(frame);
  }
  if (added.length === 0) return current;
  const merged = [...current, ...added];
  if (current.length && added[0]!.sequence < current.at(-1)!.sequence) {
    merged.sort((left, right) => left.sequence - right.sequence);
  }
  return merged.slice(-maxLiveFrames);
}

function delay(signal: AbortSignal, delayMs = reconnectDelayMs): Promise<void> {
  if (signal.aborted) {
    return Promise.resolve();
  }
  return new Promise((resolve) => {
    const onAbort = () => {
      window.clearTimeout(timeout);
      resolve();
    };
    const timeout = window.setTimeout(() => {
      signal.removeEventListener("abort", onAbort);
      resolve();
    }, delayMs);
    signal.addEventListener("abort", onAbort, { once: true });
  });
}

export function useRunEventStream(client: APIClient, runID: string) {
  const dataStoreID = useConnectionStore((state) => state.health?.data_store_id ?? "");
  const [frameState, setFrameState] = useState(() => ({ client, dataStoreID, runID, frames: emptyFrames }));
  const sameOwner = frameState.client === client && frameState.dataStoreID === dataStoreID && frameState.runID === runID;
  const frames = sameOwner ? frameState.frames : emptyFrames;
  const [status, setStatus] = useState<StreamStatus>("stopped");
  const [error, setError] = useState("");

  useEffect(() => {
    const setFrames = (update: RunEventStreamView[] | ((current: RunEventStreamView[]) => RunEventStreamView[])) => {
      setFrameState((previous) => {
        const sameOwner = previous.client === client && previous.dataStoreID === dataStoreID && previous.runID === runID;
        const current = sameOwner ? previous.frames : emptyFrames;
        const next = typeof update === "function" ? update(current) : update;
        return sameOwner && next === current ? previous : { client, dataStoreID, runID, frames: next };
      });
    };
    setError("");
    if (!runID) {
      setFrames((current) => current.length ? [] : current);
      setStatus("stopped");
      return;
    }

    const controller = new AbortController();
    let connectionStatus: StreamStatus | undefined;
    let connectionError = "";
    const publishConnection = (nextStatus: StreamStatus, nextError = "") => {
      if (connectionStatus !== nextStatus) {
        connectionStatus = nextStatus;
        setStatus(nextStatus);
      }
      if (connectionError !== nextError) {
        connectionError = nextError;
        setError(nextError);
      }
    };
    if (desktopRuntimeActive()) {
      const cacheKey = desktopRunCacheKey(client, dataStoreID, runID);
      const remembered = cacheKey ? rememberedDesktopRuns.get(cacheKey) : undefined;
      let cursor = remembered?.cursor ?? "";
      let currentFrames = remembered?.frames ?? [];
      let cursorResetUsed = false;
      setFrames(currentFrames);
      const poll = async () => {
        publishConnection("connecting");
        let immediatePages = 0;
        while (!controller.signal.aborted) {
          try {
            const page = await client.pollRunEvents(
              runID,
              cursor,
              100,
              controller.signal,
            );
            if (controller.signal.aborted) {
              return;
            }
            cursor = page.cursor;
            if (page.frames.length) {
              const merged = mergeFrames(currentFrames, page.frames);
              if (merged !== currentFrames) {
                currentFrames = merged;
                setFrames(currentFrames);
              }
            }
            rememberDesktopRun(cacheKey, runID, cursor, currentFrames);
            publishConnection("live");
            if (page.has_more) {
              immediatePages++;
              if (immediatePages < maxImmediateDesktopPages) {
                continue;
              }
            }
            immediatePages = 0;
          } catch (caught) {
            if (controller.signal.aborted) {
              return;
            }
            if (caught instanceof APIRequestError && caught.status === 400 && cursor !== "" &&
              !cursorResetUsed) {
              cursorResetUsed = true;
              cursor = "";
              currentFrames = [];
              if (cacheKey) rememberedDesktopRuns.delete(cacheKey);
              setFrames([]);
              continue;
            }
            const message = caught instanceof Error ? caught.message : "Event polling disconnected";
            if (caught instanceof APIRequestError && [400, 401, 403, 404].includes(caught.status)) {
              publishConnection("stopped", message);
              return;
            }
            publishConnection("reconnecting", message);
            await delay(controller.signal);
            continue;
          }
          await delay(controller.signal, desktopCaughtUpDelayMs);
        }
      };
      void poll();
      return () => {
        controller.abort();
      };
    }
    let currentFrames: RunEventStreamView[] = [];
    setFrames((current) => current.length ? [] : current);
    let cursor = "";
    let cursorResetUsed = false;
    const run = async () => {
      publishConnection("connecting");
      while (!controller.signal.aborted) {
        try {
          await client.streamRunEvents(runID, {
            cursor,
            signal: controller.signal,
            onOpen: () => {
              if (!controller.signal.aborted) publishConnection("live");
            },
            onFrame: (frame) => {
              if (controller.signal.aborted) return;
              cursor = frame.cursor;
              publishConnection("live");
              const merged = mergeFrames(currentFrames, [frame]);
              if (merged !== currentFrames) {
                currentFrames = merged;
                setFrames(currentFrames);
              }
            },
          });
          if (!controller.signal.aborted) {
            publishConnection("reconnecting");
            await delay(controller.signal);
          }
        } catch (caught) {
          if (controller.signal.aborted) {
            return;
          }
          if (caught instanceof APIRequestError && caught.status === 400 && cursor !== "" && !cursorResetUsed) {
            // As in Desktop polling, a rejected cursor may be stale after the
            // server changes. Replay once, then surface any further failure.
            cursorResetUsed = true;
            cursor = "";
            currentFrames = [];
            setFrames([]);
            continue;
          }
          const message = caught instanceof Error ? caught.message : "Event stream disconnected";
          if (caught instanceof APIRequestError && [400, 401, 403, 404].includes(caught.status)) {
            publishConnection("stopped", message);
            return;
          }
          publishConnection("reconnecting", message);
          await delay(controller.signal);
        }
      }
    };
    void run();

    return () => {
      controller.abort();
    };
  }, [client, dataStoreID, runID]);

  const visibleStatus: StreamStatus = sameOwner ? status : runID ? "connecting" : "stopped";
  return { frames, status: visibleStatus, error: sameOwner ? error : "" };
}
