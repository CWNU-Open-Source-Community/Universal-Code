import { APIRequestError } from "../protocol";
import {
  boundedIdentity,
  boundedText,
  hasExactKeys,
  hasOnlyKeys,
  isRecord,
  isSHA256,
  safeBoundedCount,
  safePositiveInteger,
  validDate,
} from "../validation";
import { parseOperationReceipt } from "./operations";
import type {
  RunWakeControlView,
  RunWakeExecutionView,
  RunWakeStateView,
  ScheduledJobControlView,
  ScheduledJobDetailView,
  ScheduledJobListView,
  ScheduledJobView,
} from "../../types";

export function parseRunWakeIntent(value: unknown, runID: string): NonNullable<RunWakeStateView["intent"]> {
  if (!isRecord(value) || !hasOnlyKeys(value, ["attempt_count", "background_loop_enabled",
    "base_backoff_seconds", "cancelled_at", "created_at", "deadline_at", "execution_enabled",
    "id", "initial_delay_seconds", "max_attempts", "max_backoff_seconds",
    "max_elapsed_seconds", "next_wake_at", "protocol_version", "run_id", "session_id",
    "status", "updated_at"]) || value.protocol_version !== "run_wake_intent.v1" ||
    value.run_id !== runID || !boundedIdentity(value.id) || !boundedIdentity(value.session_id) ||
    !["queued", "leased", "completed", "cancelled", "exhausted"].includes(String(value.status)) ||
    !safeBoundedCount(value.attempt_count, 8) || !safePositiveInteger(value.max_attempts) ||
    Number(value.attempt_count) > Number(value.max_attempts) ||
    !safeBoundedCount(value.initial_delay_seconds, 3600) ||
    !safeBoundedCount(value.base_backoff_seconds, 21_600) ||
    !safeBoundedCount(value.max_backoff_seconds, 21_600) ||
    !safeBoundedCount(value.max_elapsed_seconds, 86_400) || value.execution_enabled !== false ||
    value.background_loop_enabled !== false || !validDate(value.next_wake_at) ||
    !validDate(value.deadline_at) || !validDate(value.created_at) || !validDate(value.updated_at) ||
    (value.cancelled_at !== undefined && !validDate(value.cancelled_at))) {
    throw new APIRequestError("Run wake intent violated its closed authority contract",
      "INVALID_RESPONSE", 502);
  }
  return value as unknown as NonNullable<RunWakeStateView["intent"]>;
}

export function parseRunWakeState(value: unknown, runID: string): RunWakeStateView {
  if (!isRecord(value) || !hasOnlyKeys(value, ["found", "intent", "protocol_version", "run_id"]) ||
    value.protocol_version !== "run_wake_intent.v1" || value.run_id !== runID ||
    typeof value.found !== "boolean" || (value.found !== (value.intent !== undefined))) {
    throw new APIRequestError("Run wake state response is invalid", "INVALID_RESPONSE", 502);
  }
  return value.found
    ? { ...value, intent: parseRunWakeIntent(value.intent, runID) } as unknown as RunWakeStateView
    : value as unknown as RunWakeStateView;
}

export function parseRunWakeControl(value: unknown, runID: string,
  expectedAction: "cancel" | "schedule"): RunWakeControlView {
  if (!hasExactKeys(value, ["action", "execution_started", "intent", "model_called",
    "protocol_version", "replayed", "tool_called"]) ||
    value.protocol_version !== "run_wake_control.v1" || value.action !== expectedAction ||
    typeof value.replayed !== "boolean" || value.execution_started !== false ||
    value.model_called !== false || value.tool_called !== false) {
    throw new APIRequestError("Run wake response widened execution authority",
      "INVALID_RESPONSE", 502);
  }
  return { ...value, intent: parseRunWakeIntent(value.intent, runID) } as unknown as RunWakeControlView;
}

export function parseRunWakeExecution(value: unknown, runID: string): RunWakeExecutionView {
  if (!isRecord(value) || !hasOnlyKeys(value, ["background_loop_enabled", "consumption_status",
    "execution_started", "intent", "model_called", "protocol_version", "receipt", "replayed", "run_id",
    "stop_reason", "tool_called"]) || value.protocol_version !== "run_wake_consumer.v1" ||
    value.run_id !== runID || value.consumption_status !== "completed" ||
    value.execution_started !== true || typeof value.model_called !== "boolean" ||
    typeof value.tool_called !== "boolean" || value.background_loop_enabled !== false ||
    typeof value.replayed !== "boolean" ||
    (value.stop_reason !== undefined && !boundedText(value.stop_reason, 64))) {
    throw new APIRequestError("Foreground Run wake response violated its bounded contract",
      "INVALID_RESPONSE", 502);
  }
  const intent = parseRunWakeIntent(value.intent, runID);
  if (intent.status !== "completed") {
    throw new APIRequestError("Foreground Run wake did not settle its exact intent",
      "INVALID_RESPONSE", 502);
  }
  const receipt = parseOperationReceipt(value.receipt, "run_wake_consume", "completed",
    value.replayed);
  return { ...value, intent, receipt } as unknown as RunWakeExecutionView;
}

export const scheduledJobStatuses = ["active", "paused", "completed", "failed", "cancelled",
  "exhausted"];

export function validScheduledExecutionTarget(value: unknown): boolean {
  if (!isRecord(value)) return false;
  if (value.kind === "thread") {
    return hasExactKeys(value, ["kind", "thread_id", "prompt"]) &&
      Boolean(boundedIdentity(value.thread_id)) && typeof value.prompt === "string" &&
      value.prompt.trim().length > 0 && value.prompt.length <= 65_536;
  }
  return value.kind === "workflow" && hasOnlyKeys(value, ["kind", "workflow_definition_id", "inputs"]) &&
    Boolean(boundedIdentity(value.workflow_definition_id));
}

export function parseScheduledJob(value: unknown): ScheduledJobView {
  const required = ["active_lease_generation", "consecutive_unchanged", "created_at",
    "created_by", "id", "last_event_sequence", "model_calls", "owner_root_agent_id",
    "owner_run_id", "revision", "rounds_completed", "spec", "status", "updated_at"];
  const allowed = [...required, "active_lease_expires_at", "completed_at", "last_error_code",
    "last_observation_sha256", "last_result", "next_wake_at", "pending_occurrence_at",
    "stop_reason", "observation_consent_version", "execution_consent_version"];
  if (!isRecord(value) || !hasOnlyKeys(value, allowed) ||
    required.some((key) => !Object.prototype.hasOwnProperty.call(value, key)) ||
    !boundedIdentity(value.id) || !boundedIdentity(value.owner_run_id) ||
    !boundedIdentity(value.owner_root_agent_id) || !boundedText(value.created_by, 256) ||
    !scheduledJobStatuses.includes(String(value.status)) || !validDate(value.created_at) ||
    !validDate(value.updated_at) || !safeBoundedCount(value.active_lease_generation,
      Number.MAX_SAFE_INTEGER) || !safeBoundedCount(value.revision, Number.MAX_SAFE_INTEGER) ||
    !safeBoundedCount(value.rounds_completed, 10_000) ||
    !safeBoundedCount(value.consecutive_unchanged, 10_000) ||
    !safeBoundedCount(value.model_calls, 10_000) ||
    !safeBoundedCount(value.last_event_sequence, Number.MAX_SAFE_INTEGER) ||
    (value.observation_consent_version !== undefined &&
      value.observation_consent_version !== 0 && value.observation_consent_version !== 1) ||
    (value.execution_consent_version !== undefined &&
      value.execution_consent_version !== 0 && value.execution_consent_version !== 1)) {
    throw new APIRequestError("Scheduled job response is invalid", "INVALID_RESPONSE", 502);
  }
  for (const field of ["active_lease_expires_at", "completed_at", "next_wake_at",
    "pending_occurrence_at"]) {
    if (value[field] !== undefined && !validDate(value[field])) {
      throw new APIRequestError("Scheduled job timestamp is invalid", "INVALID_RESPONSE", 502);
    }
  }
  if ((value.last_observation_sha256 !== undefined &&
      !isSHA256(value.last_observation_sha256)) ||
    (value.last_result !== undefined && typeof value.last_result !== "string") ||
    (value.last_error_code !== undefined && !boundedText(value.last_error_code, 128)) ||
    (value.stop_reason !== undefined && typeof value.stop_reason !== "string")) {
    throw new APIRequestError("Scheduled job result metadata is invalid", "INVALID_RESPONSE", 502);
  }
  const spec = value.spec;
  if (!hasExactKeys(spec, ["deadline_at", "execution_mode", "max_elapsed_seconds",
    "max_model_calls", "max_rounds", "notification", "retry", "schedule",
    "stop_on_target_terminal", "target_run_id", "version",
    ...(isRecord(spec) && "execution_target" in spec ? ["execution_target"] : [])]) ||
    spec.version !== "scheduled-job.v1" || !validDate(spec.deadline_at) ||
    !["read_only", "approved_repair", "automatic"].includes(String(spec.execution_mode)) ||
    !["silent", "on_change", "on_failure", "all"].includes(String(spec.notification)) ||
    typeof spec.stop_on_target_terminal !== "boolean" ||
    boundedIdentity(spec.target_run_id) !== value.owner_run_id ||
    !safePositiveInteger(spec.max_rounds) || !safeBoundedCount(spec.max_model_calls, 10_000) ||
    !safePositiveInteger(spec.max_elapsed_seconds)) {
    throw new APIRequestError("Scheduled job specification is invalid", "INVALID_RESPONSE", 502);
  }
  if (value.observation_consent_version === 1 &&
    (spec.execution_mode !== "read_only" || spec.max_model_calls !== 0)) {
    throw new APIRequestError("Scheduled observation widened authority", "INVALID_RESPONSE", 502);
  }
  if ((spec.execution_mode === "automatic" &&
      (!validScheduledExecutionTarget(spec.execution_target) || value.execution_consent_version !== 1 ||
        spec.max_model_calls !== 0 || spec.stop_on_target_terminal !== false)) ||
    (spec.execution_mode !== "automatic" && (spec.execution_target !== undefined || value.execution_consent_version === 1))) {
    throw new APIRequestError("Scheduled execution target or consent is invalid", "INVALID_RESPONSE", 502);
  }
  const retry = spec.retry;
  if (!hasExactKeys(retry, ["initial_backoff_seconds", "max_attempts",
    "max_backoff_seconds"]) || !safePositiveInteger(retry.max_attempts) ||
    !safePositiveInteger(retry.initial_backoff_seconds) ||
    !safePositiveInteger(retry.max_backoff_seconds) ||
    retry.max_backoff_seconds < retry.initial_backoff_seconds) {
    throw new APIRequestError("Scheduled job retry policy is invalid", "INVALID_RESPONSE", 502);
  }
  const schedule = spec.schedule;
  if (!isRecord(schedule) || !hasOnlyKeys(schedule, ["anchor_at", "interval_seconds", "kind",
    "misfire_policy", "timezone"]) || !validDate(schedule.anchor_at) ||
    !["once", "periodic"].includes(String(schedule.kind)) ||
    !["run_once", "skip"].includes(String(schedule.misfire_policy)) ||
    !boundedText(schedule.timezone, 128) ||
    (schedule.kind === "once" && schedule.interval_seconds !== undefined) ||
    (schedule.kind === "periodic" && !safePositiveInteger(schedule.interval_seconds))) {
    throw new APIRequestError("Scheduled job schedule is invalid", "INVALID_RESPONSE", 502);
  }
  return { ...value, observation_consent_version: value.observation_consent_version ?? 0,
    execution_consent_version: value.execution_consent_version ?? 0 } as unknown as ScheduledJobView;
}

export function parseScheduledJobList(value: unknown): ScheduledJobListView {
  if (!hasExactKeys(value, ["items", "protocol_version"]) ||
    value.protocol_version !== "scheduled-job.v1" || !Array.isArray(value.items) ||
    value.items.length > 100) {
    throw new APIRequestError("Scheduled job list is invalid", "INVALID_RESPONSE", 502);
  }
  return { ...value, items: value.items.map(parseScheduledJob) } as ScheduledJobListView;
}

export function parseScheduledJobControl(value: unknown, runID: string, jobID: string,
  action: "create" | "pause" | "resume" | "cancel" | "enable-observation"): ScheduledJobControlView {
  if (!hasExactKeys(value, ["action", "authority_bypass", "execution_started", "job",
    "protocol_version", "replayed"]) ||
    value.protocol_version !== "scheduled-job-control.v1" || value.action !== action ||
    value.authority_bypass !== false || value.execution_started !== false ||
    typeof value.replayed !== "boolean") {
    throw new APIRequestError("Scheduled job control response widened authority",
      "INVALID_RESPONSE", 502);
  }
  const job = parseScheduledJob(value.job);
  if (job.owner_run_id !== runID || (jobID !== "" && job.id !== jobID)) {
    throw new APIRequestError("Scheduled job control response changed identity",
      "INVALID_RESPONSE", 502);
  }
  return { ...value, job } as ScheduledJobControlView;
}

export function parseScheduledJobDetail(value: unknown, jobID: string): ScheduledJobDetailView {
  if (!hasExactKeys(value, ["protocol_version", "snapshot"]) ||
    value.protocol_version !== "scheduled-job.v1" || !isRecord(value.snapshot) ||
    !hasOnlyKeys(value.snapshot, ["authorization", "job", "notifications", "rounds", "executions"]) ||
    !Array.isArray(value.snapshot.notifications) || !Array.isArray(value.snapshot.rounds) ||
    value.snapshot.notifications.length > 100 || value.snapshot.rounds.length > 100) {
    throw new APIRequestError("Scheduled job detail is invalid", "INVALID_RESPONSE", 502);
  }
  const job = parseScheduledJob(value.snapshot.job);
  const authorization = value.snapshot.authorization;
  if (authorization !== undefined &&
    (!hasExactKeys(authorization, ["approval_bypass", "authorized_at", "authorized_by",
      "execution_bypass", "expires_at", "job_id", "mode_revision", "mode_snapshot_id",
      "network_bypass", "permission_revision", "permission_snapshot_id", "protocol_version",
      "run_id"]) || authorization.protocol_version !== "scheduled-job-authorization.v1" ||
      authorization.job_id !== jobID || authorization.run_id !== job.owner_run_id ||
      authorization.execution_bypass !== false || authorization.network_bypass !== false ||
      authorization.approval_bypass !== false || !validDate(authorization.authorized_at) ||
      !validDate(authorization.expires_at))) {
    throw new APIRequestError("Scheduled job authorization exposed an invalid field",
      "INVALID_RESPONSE", 502);
  }
  const roundRequired = ["attempt", "changed", "claim_generation", "event_sequence", "job_id",
    "model_called", "occurrence_at", "ordinal", "protocol_version", "started_at", "status",
    "tool_called"];
  const roundAllowed = [...roundRequired, "completed_at", "error_code", "observation_sha256",
    "result"];
  const invalidRound = value.snapshot.rounds.some((round) => !isRecord(round) ||
    !hasOnlyKeys(round, roundAllowed) ||
    roundRequired.some((key) => !Object.prototype.hasOwnProperty.call(round, key)) ||
    round.protocol_version !== "scheduled-job-round.v1" || round.job_id !== jobID ||
    !["claimed", "retry_wait", "unchanged", "completed", "failed", "skipped"]
      .includes(String(round.status)) || typeof round.changed !== "boolean" ||
    typeof round.model_called !== "boolean" || typeof round.tool_called !== "boolean" ||
    (round.tool_called && !round.model_called) || !validDate(round.occurrence_at) ||
    !validDate(round.started_at) ||
    (round.completed_at !== undefined && !validDate(round.completed_at)) ||
    (round.observation_sha256 !== undefined && !isSHA256(round.observation_sha256)));
  const invalidNotice = value.snapshot.notifications.some((notice) =>
    !hasExactKeys(notice, ["created_at", "id", "job_id", "kind", "summary"]) ||
    notice.job_id !== jobID || !boundedIdentity(notice.id) || !validDate(notice.created_at) ||
    !["change", "failure", "recovery", "completed"].includes(String(notice.kind)) ||
    !boundedText(notice.summary, 1024));
  const executions = value.snapshot.executions ?? [];
  const invalidExecution = !Array.isArray(executions) || executions.length > 100 || executions.some((item) =>
    !isRecord(item) || !hasOnlyKeys(item, ["job_id", "occurrence_at", "run_id", "message_id",
      "workflow_execution_id", "state", "result", "error_code", "created_at", "updated_at"]) ||
    item.job_id !== jobID || !validDate(item.occurrence_at) || !validDate(item.created_at) ||
    !validDate(item.updated_at) || !["queued", "running", "waiting_approval", "completed", "failed", "blocked"].includes(String(item.state)) ||
    ["run_id", "message_id", "workflow_execution_id"].some((key) => item[key] !== undefined && !boundedIdentity(item[key])) ||
    (item.result !== undefined && typeof item.result !== "string") ||
    (item.error_code !== undefined && !boundedText(item.error_code, 128)));
  if (job.id !== jobID || invalidRound || invalidNotice || invalidExecution) {
    throw new APIRequestError("Scheduled job detail changed identity or exposed payload",
      "INVALID_RESPONSE", 502);
  }
  return { ...value, snapshot: { ...value.snapshot, job, executions } } as ScheduledJobDetailView;
}
