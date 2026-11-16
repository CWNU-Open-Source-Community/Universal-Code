import { APIRequestError, type APIClient } from "../api/client";
import type { ThreadTurnFailureReferenceView } from "../api/types";

export interface ThreadRequestObservation {
  kind: "creation" | "turn";
  state: "not_received" | "received" | "completed" | "failed" | "rejected" | "cancelled";
  settled: boolean;
  workspace_id: string;
  thread_id?: string;
  run_id?: string;
  session_id?: string;
  message_id?: string;
  message_status?: "pending" | "committed" | "cancelled";
  request_fingerprint?: string;
  turn_failure?: ThreadTurnFailureReferenceView;
  error_code?: string;
  failure_stage?: string;
}

const identity = (value: unknown): value is string => typeof value === "string" && value.length > 0 &&
  value.length <= 256 && value.trim() === value && !/[\u0000-\u001f\u007f]/u.test(value);
const record = (value: unknown): value is Record<string, unknown> =>
  !!value && typeof value === "object" && !Array.isArray(value);

function parseObservation(value: unknown, kind: "creation" | "turn", scope: string): ThreadRequestObservation {
  const invalid = (): never => { throw new APIRequestError("The original request status could not be verified", "INVALID_RESPONSE", 502); };
  if (!record(value) || value.kind !== kind || !identity(value.workspace_id) ||
    (kind === "creation" ? value.workspace_id !== scope : value.thread_id !== scope) ||
    !["not_received", "received", "completed", "failed", "rejected", "cancelled"].includes(String(value.state)) ||
    typeof value.settled !== "boolean") return invalid();
  const allowed = new Set(["kind", "state", "settled", "workspace_id", "thread_id", "run_id", "session_id",
    "message_id", "message_status", "request_fingerprint", "turn_failure", "error_code", "failure_stage"]);
  if (Object.keys(value).some((key) => !allowed.has(key))) return invalid();
  for (const field of ["thread_id", "run_id", "session_id", "message_id"]) {
    if (value[field] !== undefined && !identity(value[field])) return invalid();
  }
  if (value.request_fingerprint !== undefined && (typeof value.request_fingerprint !== "string" ||
    !/^[0-9a-f]{64}$/u.test(value.request_fingerprint))) return invalid();
  if (value.settled !== !["not_received", "received"].includes(String(value.state))) return invalid();
  if (value.state === "not_received" && ["run_id", "session_id", "message_id", "message_status", "request_fingerprint"].some((key) => value[key] !== undefined)) return invalid();
  if (kind === "creation") {
    if (!["not_received", "completed"].includes(String(value.state)) || value.message_id !== undefined || value.message_status !== undefined) return invalid();
    if (value.state === "completed" && (!identity(value.thread_id) || !identity(value.run_id) || !identity(value.session_id) || !value.request_fingerprint)) return invalid();
  } else {
    if (value.message_id !== undefined && (!identity(value.run_id) || !identity(value.session_id) ||
      !["pending", "committed", "cancelled"].includes(String(value.message_status)))) return invalid();
    if (["completed", "failed"].includes(String(value.state)) && (!identity(value.message_id) || value.message_status !== "committed")) return invalid();
    if (value.state === "cancelled" && (!identity(value.message_id) || value.message_status !== "cancelled")) return invalid();
  }
  if (value.state === "failed") {
    const ref = value.turn_failure;
    if (kind !== "turn" || !record(ref) || Object.keys(ref).length !== 4 ||
      ref.thread_id !== value.thread_id || ref.run_id !== value.run_id || ref.message_id !== value.message_id ||
      !Number.isSafeInteger(ref.event_sequence) || Number(ref.event_sequence) < 1 || value.error_code !== "turn_failed") return invalid();
  } else if (value.turn_failure !== undefined || value.error_code !== undefined || value.failure_stage !== undefined) return invalid();
  if (value.failure_stage !== undefined && !["tool_request_rejected", "empty_model_response", "invalid_model_response", "context_window_exceeded", "full_permission_not_ready"].includes(String(value.failure_stage))) return invalid();
  return value as unknown as ThreadRequestObservation;
}

export async function inspectV2TurnRequest(client: APIClient,
  input: { threadID: string; operationKey: string; signal?: AbortSignal }): Promise<ThreadRequestObservation> {
  return parseObservation(await client.inspectThreadTurnRequest(input.threadID, input.operationKey, input.signal), "turn", input.threadID);
}

export async function inspectV2SteeringRequest(client: APIClient,
  input: { sessionID?: string; operationKey: string; signal?: AbortSignal }): Promise<{
    state: "not_received" | "received"; message_id?: string; message_status?: "pending" | "committed" | "cancelled";
  }> {
  if (!identity(input.sessionID)) throw new APIRequestError("The original Session could not be verified", "INVALID_RESPONSE", 502);
  const value = await client.inspectSessionMessageOperation(input.sessionID, input.operationKey, input.signal);
  if (!record(value) || value.version !== "session_message_submission.v1" || value.session_id !== input.sessionID ||
    !["not_received", "received"].includes(String(value.state)) ||
    Object.keys(value).some((key) => !["version", "session_id", "state", "message_id", "message_status", "delivery_mode"].includes(key)) ||
    (value.state === "received" && (!identity(value.message_id) ||
      !["pending", "committed", "cancelled"].includes(String(value.message_status)) || value.delivery_mode !== "steer")) ||
    (value.state === "not_received" && (value.message_id !== undefined || value.message_status !== undefined ||
      value.delivery_mode !== undefined))) {
    throw new APIRequestError("The original Session message status could not be verified", "INVALID_RESPONSE", 502);
  }
  return value as { state: "not_received" | "received"; message_id?: string;
    message_status?: "pending" | "committed" | "cancelled" };
}

export async function inspectV2CreationRequest(client: APIClient,
  input: { operationKey: string; workspaceID: string; signal?: AbortSignal }): Promise<ThreadRequestObservation> {
  return parseObservation(await client.inspectThreadCreationRequest(input.workspaceID, input.operationKey, input.signal), "creation", input.workspaceID);
}
