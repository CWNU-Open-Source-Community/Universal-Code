import { validFileAttachments } from "../../file-attachments";
import { validImageAttachments } from "../../image-attachments";
import { validThreadPeerMessage } from "../../thread-peer-messages";
import { APIRequestError } from "../protocol";
import {
  boundedIdentity,
  boundedText,
  hasOnlyKeys,
  isRecord,
  isSHA256,
  safeBoundedCount,
  safePositiveInteger,
  validDate,
} from "../validation";
import { threadActivityCommandStatuses, validThreadActivityString } from "./activity";
import type { ThreadTranscriptItemView } from "../../types";

export const threadTranscriptTypes = ["message", "search", "read", "edit", "execute", "verify",
  "approval", "checkpoint", "delivery"];

export const threadTranscriptStages = ["started", "arguments_ready", "running", "result", "blocked"];

export const threadTranscriptKinds = ["harness_status", "model_update", "operator_input", "model_call",
  "tool_call", "approval", "file_change", "plan", "dependency", "browser", "peer_message"];

export const threadTranscriptSources = ["harness", "model", "operator", "peer"];

export function validWebEvidenceURL(value: unknown): value is string {
  if (typeof value !== "string" || value.length === 0 || value.length > 4_096 ||
    /[\u0000-\u001f\u007f]/u.test(value)) return false;
  try {
    const parsed = new URL(value);
    return parsed.protocol === "https:" && parsed.hostname.length > 0 &&
      parsed.username === "" && parsed.password === "" &&
      (parsed.port === "" || parsed.port === "443") && parsed.hash === "";
  } catch {
    return false;
  }
}

export function validThreadWebEvidence(value: unknown): boolean {
  const required = ["citeable", "digest", "fetched_at", "instruction_authorized", "partial",
    "snapshot_id", "source_id", "stale", "stale_at", "state", "untrusted", "url", "version"];
  const optional = ["citation_id", "title"];
  if (!isRecord(value) || !hasOnlyKeys(value, [...required, ...optional]) ||
    required.some((key) => !Object.prototype.hasOwnProperty.call(value, key)) ||
    value.version !== "web_evidence_presentation.v1" || !boundedIdentity(value.source_id) ||
    !boundedIdentity(value.snapshot_id) ||
    (value.citation_id !== undefined && !boundedIdentity(value.citation_id)) ||
    !validWebEvidenceURL(value.url) ||
    (value.title !== undefined && !boundedText(value.title, 1_024)) ||
    !isSHA256(value.digest) || !validDate(value.fetched_at) || !validDate(value.stale_at) ||
    Date.parse(String(value.stale_at)) < Date.parse(String(value.fetched_at)) ||
    typeof value.partial !== "boolean" || typeof value.stale !== "boolean" ||
    typeof value.citeable !== "boolean" || value.untrusted !== true ||
    value.instruction_authorized !== false) return false;
  switch (value.state) {
    case "fetched":
      return !value.partial && !value.stale && value.citeable;
    case "partial":
      return value.partial && !value.stale && value.citeable;
    case "stale":
      return value.stale && value.citeable;
    case "blocked":
    case "failed":
      return !value.partial && !value.stale && !value.citeable;
    default:
      return false;
  }
}

export function parseThreadTranscriptItem(value: unknown): ThreadTranscriptItemView {
  const required = ["activity_type", "canonical_id", "created_at", "durable", "id",
    "instruction_authorized", "kind", "provisional", "run_id",
    "run_ordinal", "sequence", "source", "stage", "title", "verifiable", "version"];
  const optional = ["activity_detail_ref", "activity_summary", "attempt_id", "boundary_reason", "detail",
    "detail_available", "delivery_mode", "durable_call_id", "promoted_to_message_id", "promoted_from_message_id",
    "model_attempt", "position", "source_ref", "status", "stream_call_id", "stream_item_id",
    "stream_response_id", "tool_name", "tool_round", "web_evidence", "images", "attachments", "peer_message"];
  if (!isRecord(value) || !hasOnlyKeys(value, [...required, ...optional]) ||
    required.some((key) => !Object.prototype.hasOwnProperty.call(value, key)) ||
    value.version !== "thread_transcript.v1" || !boundedIdentity(value.id) ||
    !boundedIdentity(value.canonical_id) || !boundedIdentity(value.run_id) ||
    !safePositiveInteger(value.run_ordinal) ||
    !safeBoundedCount(value.sequence, Number.MAX_SAFE_INTEGER) ||
    !threadTranscriptTypes.includes(String(value.activity_type)) ||
    !threadTranscriptStages.includes(String(value.stage)) ||
    !threadTranscriptKinds.includes(String(value.kind)) ||
    !threadTranscriptSources.includes(String(value.source)) ||
    !boundedText(value.title, 512) || !validDate(value.created_at) ||
    typeof value.verifiable !== "boolean" || typeof value.instruction_authorized !== "boolean" ||
    (value.detail_available !== undefined && value.detail_available !== true) ||
    value.provisional !== false || value.durable !== true) {
    throw new APIRequestError("Thread transcript item is invalid", "INVALID_RESPONSE", 502);
  }
  for (const field of ["activity_detail_ref", "attempt_id", "boundary_reason", "durable_call_id", "source_ref", "status",
    "stream_call_id", "stream_item_id", "stream_response_id", "tool_name", "promoted_to_message_id", "promoted_from_message_id"]) {
    if (value[field] !== undefined && !boundedIdentity(value[field])) {
      throw new APIRequestError("Thread transcript identity is invalid", "INVALID_RESPONSE", 502);
    }
  }
  if (value.detail !== undefined && (typeof value.detail !== "string" ||
    value.detail.length === 0 || value.detail.length > 16_384 || value.detail.includes("\0")) ||
    value.position !== undefined && !safePositiveInteger(value.position) ||
    value.model_attempt !== undefined && !safePositiveInteger(value.model_attempt) ||
    value.tool_round !== undefined && !safeBoundedCount(value.tool_round, 100)) {
    throw new APIRequestError("Thread transcript metadata is invalid", "INVALID_RESPONSE", 502);
  }
  if ((value.sequence === 0) !== (value.boundary_reason !== undefined) ||
    (value.stream_item_id !== undefined && value.canonical_id !== value.stream_item_id) ||
    ((value.detail_available === true) !== (value.activity_detail_ref !== undefined)) ||
    (value.detail_available === true && value.activity_detail_ref !== value.durable_call_id) ||
    (value.delivery_mode !== undefined &&
      (!["next_turn", "steer"].includes(String(value.delivery_mode)) || value.source !== "operator")) ||
    (value.promoted_to_message_id !== undefined && (value.source !== "operator" || value.status !== "cancelled" ||
      value.instruction_authorized !== false || value.delivery_mode !== "next_turn" || value.promoted_to_message_id === value.source_ref)) ||
    (value.promoted_from_message_id !== undefined && (value.source !== "operator" || value.delivery_mode !== "steer" ||
      value.promoted_from_message_id === value.source_ref || value.promoted_to_message_id !== undefined)) ||
    (value.source === "harness" && value.verifiable !== true) ||
    (value.source === "model" && value.verifiable !== false)) {
    throw new APIRequestError("Thread transcript provenance is invalid", "INVALID_RESPONSE", 502);
  }
  if (value.images !== undefined && (!validImageAttachments(value.images) || value.source !== "operator")) {
    throw new APIRequestError("消息图片引用无效。", "INVALID_RESPONSE", 502);
  }
  if (value.attachments !== undefined && (!validFileAttachments(value.attachments) || value.source !== "operator")) {
    throw new APIRequestError("消息附件引用无效。", "INVALID_RESPONSE", 502);
  }
  if ((value.source === "peer" || value.kind === "peer_message" || value.peer_message !== undefined) &&
    (value.source !== "peer" || value.kind !== "peer_message" || value.instruction_authorized !== false ||
      !validThreadPeerMessage(value.peer_message))) {
    throw new APIRequestError("The cross-chat message source is invalid.", "INVALID_RESPONSE", 502);
  }
  if (value.activity_summary !== undefined) {
    const summary = value.activity_summary;
    const requiredSummary = ["activity_ref", "command", "command_count",
      "duration_milliseconds", "status", "version"];
    if (value.tool_name !== "command_runtime" || !isRecord(summary) ||
      !hasOnlyKeys(summary, [...requiredSummary, "exit_code"]) ||
      requiredSummary.some((key) => !Object.prototype.hasOwnProperty.call(summary, key)) ||
      summary.version !== "thread_activity_summary.v1" ||
      summary.activity_ref !== value.activity_detail_ref ||
      !validThreadActivityString(summary.command, 4_096) ||
      !threadActivityCommandStatuses.includes(String(summary.status)) ||
      !safePositiveInteger(summary.command_count) || summary.command_count > 32 ||
      !safeBoundedCount(summary.duration_milliseconds, Number.MAX_SAFE_INTEGER) ||
      (summary.exit_code !== undefined && (typeof summary.exit_code !== "number" ||
        !Number.isSafeInteger(summary.exit_code)))) {
      throw new APIRequestError("Thread activity summary is invalid", "INVALID_RESPONSE", 502);
    }
  }
  if (value.web_evidence !== undefined && (!validThreadWebEvidence(value.web_evidence) ||
    value.kind !== "tool_call" || value.source !== "harness" || value.stage !== "result" ||
    value.verifiable !== true || value.instruction_authorized !== false ||
    !["web_fetch", "web_citation"].includes(String(value.tool_name)) ||
    (value.tool_name === "web_fetch" &&
      isRecord(value.web_evidence) && value.web_evidence.citation_id !== undefined) ||
    (value.tool_name === "web_citation" &&
      (!isRecord(value.web_evidence) || !boundedIdentity(value.web_evidence.citation_id))))) {
    throw new APIRequestError("Thread Web evidence is invalid", "INVALID_RESPONSE", 502);
  }
  return value as unknown as ThreadTranscriptItemView;
}

export function parseThreadTranscriptItems(values: unknown[]): ThreadTranscriptItemView[] {
  const items = values.map(parseThreadTranscriptItem);
  for (let index = 1; index < items.length; index++) {
    const left = items[index - 1];
    const right = items[index];
    if (right.run_ordinal < left.run_ordinal ||
      (right.run_ordinal === left.run_ordinal && right.sequence < left.sequence) ||
      (right.run_ordinal === left.run_ordinal && right.sequence === left.sequence &&
        (right.position ?? 0) < (left.position ?? 0))) {
      throw new APIRequestError("Thread transcript page is out of order", "INVALID_RESPONSE", 502);
    }
  }
  return items;
}
