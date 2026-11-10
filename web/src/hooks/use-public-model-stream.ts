import { useEffect, useState } from "react";
import { APIRequestError, type APIClient } from "../api/client";
import type { PublicModelStreamSnapshot } from "../api/types";
import { useConnectionStore } from "../state/connection";

export type PublicModelStreamStatus = "waiting" | "live" | "finalizing" |
  "reconnecting" | "stopped";

export interface PublicModelStreamState {
  error: string;
  snapshot: PublicModelStreamSnapshot | null;
  status: PublicModelStreamStatus;
}

const livePollDelayMs = 150;
const idlePollDelayMs = 1_000;
const reconnectDelayMs = 500;
const maxReconnectDelayMs = 8_000;
const finalizingMissLimit = 2;

function delay(milliseconds: number, signal: AbortSignal): Promise<void> {
  if (signal.aborted) {
    return Promise.resolve();
  }
  return new Promise((resolve) => {
    const finish = () => {
      window.clearTimeout(timeout);
      signal.removeEventListener("abort", finish);
      resolve();
    };
    const timeout = window.setTimeout(finish, milliseconds);
    signal.addEventListener("abort", finish, { once: true });
  });
}

function callIdentity(snapshot: PublicModelStreamSnapshot): string {
  return `${snapshot.call.attempt_id}:${snapshot.call.model_attempt}`;
}

export function usePublicModelStream(client: APIClient, runID: string,
  enabled: boolean): PublicModelStreamState {
  const dataStoreID = useConnectionStore((state) => state.health?.data_store_id ?? "");
  const [snapshotState, setSnapshotState] = useState<{
    client: APIClient; runID: string; enabled: boolean; dataStoreID: string; snapshot: PublicModelStreamSnapshot | null;
  }>(() => ({ client, runID, enabled, dataStoreID, snapshot: null }));
  const [status, setStatus] = useState<PublicModelStreamStatus>("stopped");
  const [error, setError] = useState("");
  const sameOwner = snapshotState.client === client && snapshotState.runID === runID &&
    snapshotState.enabled === enabled && snapshotState.dataStoreID === dataStoreID;

  useEffect(() => {
    const controller = new AbortController();
    const setSnapshot = (snapshot: PublicModelStreamSnapshot | null) =>
      setSnapshotState((previous) => previous.client === client && previous.runID === runID &&
        previous.enabled === enabled && previous.dataStoreID === dataStoreID && previous.snapshot === snapshot
        ? previous : { client, runID, enabled, dataStoreID, snapshot });
    setSnapshot(null);
    setError("");
    if (!enabled || !runID) {
      setStatus("stopped");
      return () => controller.abort();
    }

    let current: PublicModelStreamSnapshot | null = null;
    let finalizingMisses = 0;
    let retryDelay = reconnectDelayMs;
    const poll = async () => {
      setStatus("waiting");
      while (!controller.signal.aborted) {
        let wait = livePollDelayMs;
        try {
          const next = await client.pollPublicModelStream(runID, controller.signal);
          if (controller.signal.aborted) return;
          retryDelay = reconnectDelayMs;
          if (next === null) {
            if (current) {
              finalizingMisses++;
              if (finalizingMisses >= finalizingMissLimit) {
                current = null;
                setSnapshot(null);
                setStatus("waiting");
              } else {
                setStatus("finalizing");
              }
            } else {
              setStatus("waiting");
            }
            setError("");
            wait = idlePollDelayMs;
            await delay(wait, controller.signal);
            continue;
          }
          const replace = current === null || callIdentity(next) !== callIdentity(current) ||
            next.revision > current.revision;
          if (replace) {
            current = next;
            setSnapshot(next);
          }
          finalizingMisses = 0;
          setStatus("live");
          setError("");
        } catch (caught) {
          if (controller.signal.aborted) return;
          if (caught instanceof APIRequestError && caught.status === 404) {
            retryDelay = reconnectDelayMs;
            if (current) {
              finalizingMisses++;
              if (finalizingMisses >= finalizingMissLimit) {
                current = null;
                setSnapshot(null);
                setStatus("waiting");
              } else {
                setStatus("finalizing");
              }
            } else {
              setStatus("waiting");
            }
            setError("");
            wait = reconnectDelayMs;
          } else if (caught instanceof APIRequestError &&
            [400, 401, 403].includes(caught.status)) {
            setStatus("stopped");
            setError(caught.message);
            return;
          } else {
            setStatus("reconnecting");
            setError(caught instanceof Error ? caught.message : "Model stream disconnected");
            wait = retryDelay;
            retryDelay = Math.min(retryDelay * 2, maxReconnectDelayMs);
          }
        }
        await delay(wait, controller.signal);
      }
    };
    void poll();
    return () => controller.abort();
  }, [client, dataStoreID, enabled, runID]);

  return sameOwner ? { error, snapshot: snapshotState.snapshot, status }
    : { error: "", snapshot: null, status: enabled && runID ? "waiting" : "stopped" };
}
