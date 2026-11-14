import type { LocaleText } from "../../lib/locale";
import type { NarrativeEntry } from "./narrative";

// These facts come from the durable operator-input/handoff ledger. A model
// response, stream completion or internal tool-loop turn cannot end a task.
export interface NarrativeTaskSource {
  id: string;
  input_message_id: string;
  state: "running" | "completed" | "interrupted" | "failed";
  started_at?: string;
  finished_at?: string;
  final_message_id?: string;
  change_source_event_id?: string;
}
export interface NarrativeTask {
  id: string;
  inputMessageID: string;
  runId: string;
  state: NarrativeTaskSource["state"];
  startedAt?: string;
  finishedAt?: string;
  finalMessageID?: string;
  changeSourceEventID?: string;
}
export type TaskTranscriptItem = { run_id: string; sequence?: number; task?: NarrativeTaskSource };

export function narrativeTaskIndex(items: TaskTranscriptItem[]) {
  const tasks = new Map<string, NarrativeTask>();
  const sequences = new Map<string, number>();
  for (const item of items) {
    const source = item.task;
    if (!source?.id || !source.input_message_id || !item.run_id ||
      !["running", "completed", "interrupted", "failed"].includes(source.state)) continue;
    const previous = tasks.get(source.id);
    // A resumed input can legitimately return from interrupted/completed to
    // running. New lifecycle rows outrank metadata cached on older pages.
    const sequence = item.sequence ?? 0;
    if (previous && (previous.inputMessageID !== source.input_message_id ||
      sequence < (sequences.get(source.id) ?? 0))) continue;
    sequences.set(source.id, sequence);
    tasks.set(source.id, { id: source.id, inputMessageID: source.input_message_id, runId: item.run_id,
      state: source.state, startedAt: source.started_at, finishedAt: source.finished_at,
      finalMessageID: source.final_message_id, changeSourceEventID: source.change_source_event_id });
  }
  return tasks;
}

export function narrativeTaskFields(item: TaskTranscriptItem, tasks: Map<string, NarrativeTask>) {
  const task = item.task && tasks.get(item.task.id);
  return task ? { task } : {};
}

export function narrativeTaskSource(task: NarrativeTask): NarrativeTaskSource {
  return { id: task.id, input_message_id: task.inputMessageID, state: task.state,
    started_at: task.startedAt, finished_at: task.finishedAt, final_message_id: task.finalMessageID,
    change_source_event_id: task.changeSourceEventID };
}

export interface NarrativeTaskGroup {
  kind: "task";
  id: string;
  task: NarrativeTask;
  entries: NarrativeEntry[];
}
export function groupNarrativeTasks(entries: NarrativeEntry[]): Array<NarrativeEntry | NarrativeTaskGroup> {
  const groups = new Map<string, NarrativeTaskGroup>();
  const result: Array<NarrativeEntry | NarrativeTaskGroup> = [];
  for (const entry of entries) {
    if (!entry.task) { result.push(entry); continue; }
    let group = groups.get(entry.task.id);
    if (!group) {
      group = { kind: "task", id: entry.task.id, task: entry.task, entries: [] };
      groups.set(entry.task.id, group); result.push(group);
    }
    group.entries.push(entry);
  }
  return result;
}

export function isFinalTaskReply(entry: NarrativeEntry, task: NarrativeTask): entry is Extract<NarrativeEntry, { kind: "assistant" }> {
  return entry.kind === "assistant" && !entry.provisional && Boolean(task.finalMessageID &&
    (entry.messageIDs ?? [entry.id]).includes(task.finalMessageID));
}

export function taskDurationLabel(task: NarrativeTask, t: LocaleText): string | undefined {
  if (!task.startedAt || !task.finishedAt) return;
  const start = Date.parse(task.startedAt), end = Date.parse(task.finishedAt);
  if (!Number.isFinite(start) || !Number.isFinite(end) || end < start) return;
  const elapsed = Math.floor((end - start) / 1000);
  const hours = Math.floor(elapsed / 3600), minutes = Math.floor(elapsed % 3600 / 60), seconds = elapsed % 60;
  const chinese = `${hours ? `${hours}小时` : ""}${minutes ? `${minutes}分钟` : ""}${seconds || !hours && !minutes ? `${seconds}秒` : ""}`;
  const english = [hours && `${hours}h`, minutes && `${minutes}m`, (seconds || !hours && !minutes) && `${seconds}s`].filter(Boolean).join(" ");
  return t(`用时${chinese}`, `Worked for ${english}`);
}
