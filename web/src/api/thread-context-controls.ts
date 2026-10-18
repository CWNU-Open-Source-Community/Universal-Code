import type { APIClient } from "./client";

export type ThreadContextPin = {
  id: string; text: string; version: number; content_sha256: string;
  source_kind: string; source_message_id?: number; created_at: string; updated_at: string;
};
export type ThreadContextPinChoice = "pin_task" | "discussion_only" | "custom" | "dismiss";
export type ThreadContextPinProposal = {
  id: string; version: number; status: "pending" | "resolved" | "dismissed";
  text: string; question: string; run_id: string; source_message_id?: number;
  created_at: string; choice?: ThreadContextPinChoice; reply_text?: string; reply_operation_key?: string;
};
export type ThreadContextPins = {
  version: "thread_context_pins.v1"; thread_id: string; revision: number;
  pins: ThreadContextPin[]; proposals: ThreadContextPinProposal[];
  busy: boolean; effective_from: "next_request";
};
export type ThreadContextPinResolution = {
  state: ThreadContextPins; reply_text?: string; reply_operation_key?: string; replayed: boolean;
};
export type ThreadCompactionResult = {
  version: "thread_context_compaction.v1"; thread_id: string; run_id: string; session_id: string;
  status: "completed" | "noop"; summary_id: number; removed_messages: number; preserved_messages: number;
  source_sha256: string; generated: boolean; replayed: boolean;
  input_messages_omitted?: number; input_fingerprint?: string;
};
export type ThreadCompactionOperation = {
  operation_id: string; run_id: string; session_id: string;
  status: "prepared" | "started" | "generated" | "completed" | "noop" | "failed";
  source_sha256: string; provider?: string; model?: string; created_at: string;
  started_at?: string; terminal_at?: string; usage_known: boolean; error_message?: string;
  result?: ThreadCompactionResult;
};
export type ThreadCompactionOperations = {
  version: "thread_context_compaction_operations.v1"; thread_id: string; operations: ThreadCompactionOperation[];
};
const record = (v: unknown): v is Record<string, unknown> => Boolean(v) && typeof v === "object" && !Array.isArray(v);
const text = (v: unknown): v is string => typeof v === "string";
const identity = (v: unknown): v is string => text(v) && v.trim().length > 0;
const integer = (v: unknown): v is number => Number.isSafeInteger(v) && Number(v) >= 0;
const positive = (v: unknown): v is number => integer(v) && v > 0;
const date = (v: unknown) => text(v) && Number.isFinite(Date.parse(v));
const digest = (v: unknown) => text(v) && /^[a-f0-9]{64}$/u.test(v);
const choices = new Set(["pin_task", "discussion_only", "custom", "dismiss"]);
const invalid = () => new Error("任务上下文返回的内容或来源不匹配，请重新读取。");

export function parseThreadContextPins(value: unknown, threadID: string): ThreadContextPins {
  if (!record(value) || value.version !== "thread_context_pins.v1" || value.thread_id !== threadID ||
    !integer(value.revision) || typeof value.busy !== "boolean" || value.effective_from !== "next_request" ||
    !Array.isArray(value.pins) || !Array.isArray(value.proposals)) throw invalid();
  if (value.pins.some((pin) => !record(pin) || !identity(pin.id) || !identity(pin.text) || !positive(pin.version) ||
    !digest(pin.content_sha256) || !identity(pin.source_kind) || !date(pin.created_at) || !date(pin.updated_at) ||
    (pin.source_message_id !== undefined && !integer(pin.source_message_id)))) throw invalid();
  if (value.proposals.some((proposal) => !record(proposal) || !identity(proposal.id) || !positive(proposal.version) ||
    !["pending", "resolved", "dismissed"].includes(String(proposal.status)) || !identity(proposal.text) ||
    !text(proposal.question) || !identity(proposal.run_id) || !date(proposal.created_at) ||
    (proposal.source_message_id !== undefined && !integer(proposal.source_message_id)) ||
    (proposal.choice !== undefined && !choices.has(String(proposal.choice))) ||
    (proposal.reply_text !== undefined && !text(proposal.reply_text)) ||
    (proposal.reply_operation_key !== undefined && !text(proposal.reply_operation_key)))) throw invalid();
  return value as unknown as ThreadContextPins;
}

export async function readThreadContextPins(client: APIClient, threadID: string, signal?: AbortSignal) {
  return parseThreadContextPins(await client.get<unknown>(`/threads/${encodeURIComponent(threadID)}/context/pins`, {}, signal), threadID);
}

export function parseThreadPinResolution(value: unknown, threadID: string): ThreadContextPinResolution {
  if (!record(value) || typeof value.replayed !== "boolean" ||
    (value.reply_text !== undefined && !text(value.reply_text)) ||
    (value.reply_operation_key !== undefined && !text(value.reply_operation_key)) ||
    Boolean(value.reply_text) !== Boolean(value.reply_operation_key)) throw invalid();
  return { ...value, state: parseThreadContextPins(value.state, threadID) } as ThreadContextPinResolution;
}

export function parseThreadCompaction(value: unknown, threadID: string, runID: string): ThreadCompactionResult {
  if (!record(value) || value.version !== "thread_context_compaction.v1" || value.thread_id !== threadID ||
    value.run_id !== runID || !identity(value.session_id) || !["completed", "noop"].includes(String(value.status)) ||
    !integer(value.summary_id) || !integer(value.removed_messages) || !integer(value.preserved_messages) ||
    !digest(value.source_sha256) || typeof value.generated !== "boolean" || typeof value.replayed !== "boolean" ||
    (value.input_messages_omitted !== undefined && !integer(value.input_messages_omitted)) ||
    (value.input_fingerprint !== undefined && value.input_fingerprint !== "" && !digest(value.input_fingerprint))) throw invalid();
  return value as unknown as ThreadCompactionResult;
}

export function parseThreadCompactionOperation(value: unknown, threadID: string, operationID?: string): ThreadCompactionOperation {
  if (!record(value) || !identity(value.operation_id) || (operationID !== undefined && value.operation_id !== operationID) ||
    !identity(value.run_id) || !identity(value.session_id) ||
    !["prepared", "started", "generated", "completed", "noop", "failed"].includes(String(value.status)) ||
    !digest(value.source_sha256) || !date(value.created_at) || typeof value.usage_known !== "boolean" ||
    (value.started_at !== undefined && !date(value.started_at)) || (value.terminal_at !== undefined && !date(value.terminal_at)) ||
    (value.provider !== undefined && !text(value.provider)) || (value.model !== undefined && !text(value.model)) ||
    (value.error_message !== undefined && !text(value.error_message))) throw invalid();
  if (value.result !== undefined) {
    const result = parseThreadCompaction(value.result, threadID, value.run_id);
    if (result.session_id !== value.session_id || result.source_sha256 !== value.source_sha256 || result.status !== value.status) throw invalid();
  }
  return value as unknown as ThreadCompactionOperation;
}

export function parseThreadCompactionOperations(value: unknown, threadID: string): ThreadCompactionOperations {
  if (!record(value) || value.version !== "thread_context_compaction_operations.v1" || value.thread_id !== threadID ||
    !Array.isArray(value.operations)) throw invalid();
  const operations = value.operations.map((operation) => parseThreadCompactionOperation(operation, threadID));
  if (new Set(operations.map((operation) => operation.operation_id)).size !== operations.length) throw invalid();
  return { version: "thread_context_compaction_operations.v1", thread_id: threadID, operations };
}

export async function readThreadCompactionOperations(client: APIClient, threadID: string, signal?: AbortSignal) {
  return parseThreadCompactionOperations(await client.get<unknown>(`/threads/${encodeURIComponent(threadID)}/context/compactions`, {}, signal), threadID);
}

export async function recoverThreadCompaction(client: APIClient, threadID: string, operation: ThreadCompactionOperation, action: "resume" | "discard") {
  const result = parseThreadCompactionOperation(await client.postControl<unknown>(
    `/threads/${encodeURIComponent(threadID)}/context/compactions/${encodeURIComponent(operation.operation_id)}/recover`,
    { version: "thread_context_compaction_recovery.v1", expected_run_id: operation.run_id, action },
    `thread-context-recover-${operation.operation_id}-${action}`), threadID, operation.operation_id);
  if (result.run_id !== operation.run_id || result.session_id !== operation.session_id || result.source_sha256 !== operation.source_sha256) throw invalid();
  return result;
}
