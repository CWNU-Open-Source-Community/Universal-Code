import { APIRequestError } from "../protocol";
import {
  boundedIdentity,
  hasExactKeys,
  hasOnlyKeys,
  isRecord,
  safeBoundedCount,
  safePositiveInteger,
  validDate,
} from "../validation";
import type {
  ModelCancellationRequestView,
  ModelCancellationView,
  PublicModelStreamSnapshot,
  SpecialistModelCancellationView,
} from "../../types";

export function isModelCancellationStatus(value: unknown): boolean {
  return value === "pending" || value === "observed" || value === "resolved";
}

export function parseModelCancellation(value: unknown, expectedRunID: string,
  request: ModelCancellationRequestView): ModelCancellationView {
  if (!hasExactKeys(value, ["attempt_id", "id", "model_attempt", "replayed",
    "requested_at", "run_id", "status"]) ||
    !boundedIdentity(value.id) || value.run_id !== expectedRunID ||
    !boundedIdentity(value.run_id) || value.attempt_id !== request.attempt_id ||
    !boundedIdentity(value.attempt_id) ||
    !safePositiveInteger(value.model_attempt) ||
    value.model_attempt !== request.model_attempt ||
    typeof value.replayed !== "boolean" || !validDate(value.requested_at) ||
    !isModelCancellationStatus(value.status)) {
    throw new APIRequestError("Model cancellation response is invalid", "INVALID_RESPONSE", 502);
  }
  return value as unknown as ModelCancellationView;
}

export function parsePublicModelStream(value: unknown,
  expectedRunID: string): PublicModelStreamSnapshot {
  const callKeys = ["attempt_id", "cancel_requested", "max_attempts", "model",
    "model_attempt", "protocol_repair", "provider", "run_id", "session_id",
    "started_at", "stream_bytes", "stream_chunks", "tool_round", "transport_attempt"];
  const required = ["call", "content_kind", "event_sequence", "items", "message_complete",
    "provisional", "revision", "text", "updated_at", "version"];
  if (!isRecord(value) || !hasOnlyKeys(value, [...required, "response_id"]) ||
    required.some((key) => !Object.prototype.hasOwnProperty.call(value, key)) ||
    value.version !== "model_public_stream.v3" || value.provisional !== true ||
    !["root_message", "tool_commentary"].includes(String(value.content_kind)) ||
    typeof value.message_complete !== "boolean" || !safePositiveInteger(value.revision) ||
    typeof value.text !== "string" ||
    new TextEncoder().encode(value.text).byteLength > 64 * 1024 ||
    !safeBoundedCount(value.event_sequence, 16 * 1024) || !Array.isArray(value.items) ||
    value.items.length > 64 || !validDate(value.updated_at) ||
    !hasExactKeys(value.call, callKeys)) {
    throw new APIRequestError("Public model stream response is invalid", "INVALID_RESPONSE", 502);
  }
  const responseID = value.response_id === undefined ? "" : boundedIdentity(value.response_id);
  const itemIDs = new Set(value.items.map((item) => isRecord(item) && typeof item.id === "string"
    ? item.id : ""));
  if ((value.response_id !== undefined && !responseID) ||
    (!responseID && (value.event_sequence !== 0 || value.items.length !== 0)) ||
    (responseID && !safePositiveInteger(value.event_sequence)) ||
    itemIDs.size !== value.items.length ||
    value.items.some((item) => !validPublicOutputItem(item, responseID))) {
    throw new APIRequestError("Public model stream item projection is invalid",
      "INVALID_RESPONSE", 502);
  }
  const call = value.call;
  if (call.run_id !== expectedRunID || !boundedIdentity(call.run_id) ||
    !boundedIdentity(call.session_id) || !boundedIdentity(call.attempt_id) ||
    !boundedIdentity(call.provider) || !boundedIdentity(call.model) ||
    !safePositiveInteger(call.model_attempt) || !safePositiveInteger(call.transport_attempt) ||
    !safePositiveInteger(call.max_attempts) || call.transport_attempt > call.max_attempts ||
    !safeBoundedCount(call.protocol_repair, 1) || !safeBoundedCount(call.tool_round, 4) ||
    !safeBoundedCount(call.stream_chunks, 64 * 1024) ||
    !safeBoundedCount(call.stream_bytes, 64 * 1024) ||
    typeof call.cancel_requested !== "boolean" || !validDate(call.started_at)) {
    throw new APIRequestError("Public model stream binding is invalid", "INVALID_RESPONSE", 502);
  }
  return value as unknown as PublicModelStreamSnapshot;
}

export function parsePublicModelStreamPoll(value: unknown,
  expectedRunID: string): PublicModelStreamSnapshot | null {
  if (!isRecord(value) || !hasOnlyKeys(value, ["active", "snapshot", "version"]) ||
    value.version !== "model_public_stream_poll.v1" || typeof value.active !== "boolean") {
    throw new APIRequestError("Public model stream poll response is invalid",
      "INVALID_RESPONSE", 502);
  }
  if (!value.active) {
    if (value.snapshot !== undefined) {
      throw new APIRequestError("Inactive public model stream poll returned a snapshot",
        "INVALID_RESPONSE", 502);
    }
    return null;
  }
  if (value.snapshot === undefined) {
    throw new APIRequestError("Active public model stream poll omitted its snapshot",
      "INVALID_RESPONSE", 502);
  }
  return parsePublicModelStream(value.snapshot, expectedRunID);
}

export function validPublicOutputItem(value: unknown, responseID: string): boolean {
  const required = ["durable", "id", "provisional", "response_id", "status", "type"];
  const allowed = [...required, "argument_bytes", "call_id", "durable_call_id", "tool_name"];
  if (!isRecord(value) || !hasOnlyKeys(value, allowed) ||
    required.some((key) => !Object.prototype.hasOwnProperty.call(value, key)) ||
    value.response_id !== responseID || !boundedIdentity(value.response_id) ||
    !boundedIdentity(value.id) || value.provisional !== true || value.durable !== false ||
    !["message", "tool_call"].includes(String(value.type)) ||
    !["in_progress", "ready_for_validation", "completed", "failed", "cancelled"]
      .includes(String(value.status))) {
    return false;
  }
  if (value.type === "message") {
    return value.call_id === undefined && value.durable_call_id === undefined &&
      value.tool_name === undefined && value.argument_bytes === undefined &&
      value.status !== "ready_for_validation";
  }
  return Boolean(boundedIdentity(value.call_id) && boundedIdentity(value.tool_name)) &&
    value.durable_call_id === undefined &&
    (value.argument_bytes === undefined || safeBoundedCount(value.argument_bytes, 256 * 1024));
}

export function parseSpecialistModelCancellation(value: unknown, expectedRunID: string,
  expectedAgentID: string, request: ModelCancellationRequestView): SpecialistModelCancellationView {
  if (!hasExactKeys(value, ["agent_id", "attempt_id", "id", "model_attempt", "replayed",
    "requested_at", "run_id", "status"]) ||
    !boundedIdentity(value.id) || value.run_id !== expectedRunID ||
    !boundedIdentity(value.run_id) || value.agent_id !== expectedAgentID ||
    !boundedIdentity(value.agent_id) || value.attempt_id !== request.attempt_id ||
    !boundedIdentity(value.attempt_id) ||
    !safePositiveInteger(value.model_attempt) ||
    value.model_attempt !== request.model_attempt ||
    typeof value.replayed !== "boolean" || !validDate(value.requested_at) ||
    !isModelCancellationStatus(value.status)) {
    throw new APIRequestError("Specialist model cancellation response is invalid",
      "INVALID_RESPONSE", 502);
  }
  return value as unknown as SpecialistModelCancellationView;
}
