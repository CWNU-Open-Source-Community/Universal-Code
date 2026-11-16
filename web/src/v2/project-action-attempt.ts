import { useCallback, useEffect, useRef, useState } from "react";
import type { ProjectActionSlot } from "../api/project-actions";
import { updateBrowserStorageRecord, type BrowserStorageRecordResult } from "../lib/browser-storage-record";
import { decodeRecoveryRecord } from "./recovery-storage";

export const projectActionAttemptsDatabase = "v2_project_action_recovery.v1";
const changedEvent = "universal-code:project-action-attempt";

export interface ProjectActionAttempt {
  runID: string;
  slot: ProjectActionSlot;
  target: string;
  key: string;
  submissionID?: string;
  jobID?: string;
}

function validAttempt(value: unknown, runID: string): value is ProjectActionAttempt {
  if (!value || typeof value !== "object") return false;
  const item = value as Partial<ProjectActionAttempt>;
  return item.runID === runID && (item.slot === "test" || item.slot === "format") &&
    typeof item.target === "string" && typeof item.key === "string" &&
    /^project-action-[\w-]+$/u.test(item.key) &&
    (item.submissionID === undefined || typeof item.submissionID === "string") &&
    (item.jobID === undefined || typeof item.jobID === "string");
}

type AttemptChange = (current: ProjectActionAttempt | null) => ProjectActionAttempt | null | undefined;
type AttemptSnapshot = {
  attempt: ProjectActionAttempt | null;
  ready: boolean;
  error?: "invalid" | "unavailable";
};

let attemptChannel: BroadcastChannel | undefined;
function getAttemptChannel() {
  if (!attemptChannel) {
    try { attemptChannel = new window.BroadcastChannel(projectActionAttemptsDatabase); }
    catch { /* Focus and page restore also read the current record. */ }
  }
  return attemptChannel;
}

export function useProjectActionAttempt(runID: string, recordKey?: string) {
  const [snapshot, setSnapshot] = useState<AttemptSnapshot>({ attempt: null, ready: !recordKey });
  const memory = useRef<ProjectActionAttempt | null>(null);
  const sequence = useRef(0);
  const mounted = useRef(false);

  const update = useCallback(async (change?: AttemptChange): Promise<ProjectActionAttempt | null> => {
    if (!recordKey) {
      const next = change?.(memory.current);
      if (next !== undefined) memory.current = next;
      if (mounted.current) setSnapshot({ attempt: memory.current, ready: true });
      return memory.current;
    }
    const request = ++sequence.current;
    let damaged = false;
    const result = await new Promise<BrowserStorageRecordResult>((finish) => {
      updateBrowserStorageRecord(projectActionAttemptsDatabase, (stored) => {
        // Copy legacy localStorage once. Keeping a null record prevents a
        // cleared attempt from being restored again on the next launch.
        const legacy = stored === null ? localStorage.getItem(recordKey) : null;
        let current: ProjectActionAttempt | null;
        try {
          const value: unknown = stored !== null ? JSON.parse(stored) : legacy === null ? null : decodeRecoveryRecord(legacy);
          if (value !== null && !validAttempt(value, runID)) throw new Error("Invalid project action attempt");
          current = value;
        } catch (error) {
          damaged = true;
          throw error;
        }
        const next = change?.(current);
        return next !== undefined ? JSON.stringify(next) : stored === null ? JSON.stringify(current) : undefined;
      }, finish, recordKey);
    });
    const latest = mounted.current && request === sequence.current;
    if (!result.ok) {
      if (latest && (!change || damaged)) {
        setSnapshot((previous) => ({ ...previous, ready: false, error: damaged ? "invalid" : "unavailable" }));
      }
      throw new Error("Project action recovery record could not be saved or read");
    }
    const attempt = JSON.parse(result.value ?? "null") as ProjectActionAttempt | null;
    if (latest) setSnapshot({ attempt, ready: true });
    if (result.written) {
      window.dispatchEvent(new CustomEvent(changedEvent, { detail: recordKey }));
      getAttemptChannel()?.postMessage(recordKey);
    }
    return attempt;
  }, [recordKey, runID]);

  const refresh = useCallback(() => { void update().catch(() => {}); }, [update]);
  useEffect(() => {
    mounted.current = true;
    const onRemote = (event: MessageEvent<unknown>) => {
      if (event.data === recordKey) refresh();
    };
    const onLocal = (event: Event) => {
      if ((event as CustomEvent).detail === recordKey) refresh();
    };
    const channel = recordKey ? getAttemptChannel() : undefined;
    if (recordKey) {
      channel?.addEventListener("message", onRemote);
      window.addEventListener(changedEvent, onLocal);
      window.addEventListener("focus", refresh);
      window.addEventListener("pageshow", refresh);
      refresh();
    }
    return () => {
      mounted.current = false;
      sequence.current++;
      channel?.removeEventListener("message", onRemote);
      window.removeEventListener(changedEvent, onLocal);
      window.removeEventListener("focus", refresh);
      window.removeEventListener("pageshow", refresh);
    };
  }, [recordKey, refresh]);

  return { ...snapshot, update, refresh };
}
