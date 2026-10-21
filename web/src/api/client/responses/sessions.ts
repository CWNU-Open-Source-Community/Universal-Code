import { APIRequestError } from "../protocol";
import {
  boundedIdentity,
  boundedText,
  hasExactKeys,
  hasOnlyKeys,
  isRecord,
  safeBoundedCount,
  safePositiveInteger,
  validDate,
} from "../validation";
import { parseRunCreationControl } from "./runs";
import type {
  SessionArchiveControlView,
  SessionMessageControlView,
  SessionSteeringCancellationView,
  ThreadCreationControlRequestView,
  ThreadCreationControlView,
  ThreadExecutionPermissionControlView,
  ThreadLifecycleControlView,
  ThreadMessageControlView,
  ThreadRunRecoveryControlRequestView,
  ThreadRunRecoveryControlView,
  ThreadView,
} from "../../types";

export function parseThreadApprovalPreference(value: unknown, threadID: string): ThreadExecutionPermissionControlView {
  const fail = () => { throw new APIRequestError("Thread approval preference is invalid", "INVALID_RESPONSE", 502); };
  if (!isRecord(value) || !isRecord(value.execution_permission)) return fail();
  const permission = value.execution_permission;
  const legacyModes = ["conservative", "workspace_access", "approval", "full_access", "debug"];
  const modes = ["ask", "auto", "full"];
  const legacy = permission.protocol_version === "thread_execution_permission.v1";
  if ((!legacy && permission.protocol_version !== "thread_execution_permission.v2") ||
    permission.thread_id !== threadID || typeof permission.mode !== "string" ||
    !(legacy ? legacyModes : modes).includes(permission.mode) ||
    permission.policy_version !== (legacy ? "execution_permission_policy.v1" : "execution_permission_policy.v2") ||
    !Number.isSafeInteger(permission.revision) || Number(permission.revision) < 1 ||
    permission.process_enabled !== false || permission.execution_authorized !== false || permission.capability_grant !== false ||
    typeof permission.runtime_gate_available !== "boolean" || !isRecord(permission.runtime) ||
    typeof value.replayed !== "boolean" || typeof value.current_run_synchronized !== "boolean") return fail();
  const expectedMode = legacy ? ["full_access", "debug"].includes(permission.mode) ? "full" : "ask" : permission.mode;
  // A legacy reader may supply no new presentation fields. Its startup flags
  // cannot prove current-process Full activation, so the projection stays cold.
  const approvalMode = permission.approval_mode ?? (legacy ? expectedMode : undefined);
  const activation = permission.full_activation ?? (legacy ? "inactive" : undefined);
  if (approvalMode !== expectedMode || typeof activation !== "string" ||
    !["inactive", "active", "unavailable"].includes(activation) ||
    (activation === "active" && (legacy || approvalMode !== "full" || !permission.runtime_gate_available)) ||
    (permission.full_unavailable_reason !== undefined && typeof permission.full_unavailable_reason !== "string")) return fail();
  return { ...value, execution_permission: { ...permission, approval_mode: approvalMode,
    full_activation: activation } } as ThreadExecutionPermissionControlView;
}

export function parseThreadView(value: unknown, expectedThreadID = ""): ThreadView {
  const required = ["composer_state", "created_at", "id", "last_run_id", "mission_id",
    "protocol_version", "status", "title", "updated_at", "version"];
  const optional = ["active_run_id", "archived_at", "deleted_at", "workspace_id", "execution_state", "project_workspace_id", "project_assignment_version", "pinned"];
  if (!isRecord(value) || !hasOnlyKeys(value, [...required, ...optional]) ||
    required.some((key) => !Object.prototype.hasOwnProperty.call(value, key)) ||
    value.protocol_version !== "thread.v1" || !boundedIdentity(value.id) ||
    (expectedThreadID !== "" && value.id !== expectedThreadID) ||
    !boundedIdentity(value.mission_id) || !boundedIdentity(value.last_run_id) ||
    !boundedText(value.title, 4_096) || !safePositiveInteger(value.version) ||
    (value.pinned !== undefined && typeof value.pinned !== "boolean") ||
    !validDate(value.created_at) || !validDate(value.updated_at) ||
    (value.workspace_id !== undefined && !boundedIdentity(value.workspace_id)) ||
    (value.project_workspace_id !== undefined && value.project_workspace_id !== "" && !boundedIdentity(value.project_workspace_id)) ||
    (value.project_assignment_version !== undefined && !safePositiveInteger(value.project_assignment_version)) ||
    ((value.project_workspace_id !== undefined) !== (value.project_assignment_version !== undefined)) ||
    (value.active_run_id !== undefined && !boundedIdentity(value.active_run_id)) ||
    (value.archived_at !== undefined && !validDate(value.archived_at)) ||
    (value.deleted_at !== undefined && !validDate(value.deleted_at)) ||
    !["active", "archived", "deleted"].includes(String(value.status)) ||
    !["ready", "waiting_approval", "successor_required", "unavailable"]
      .includes(String(value.composer_state)) ||
    (value.execution_state !== undefined && (typeof value.execution_state !== "string" || !["idle", "running", "stopping", "stop_failed",
      "waiting_approval", "paused", "completed", "failed", "cancelled", "unknown"]
      .includes(value.execution_state))) ||
    Date.parse(String(value.updated_at)) < Date.parse(String(value.created_at))) {
    throw new APIRequestError("Thread response is invalid", "INVALID_RESPONSE", 502);
  }
  if ((value.active_run_id !== undefined && value.active_run_id !== value.last_run_id) ||
    (value.status === "active" && (value.archived_at !== undefined ||
      value.deleted_at !== undefined ||
      (value.active_run_id === undefined && value.composer_state !== "successor_required") ||
      (value.active_run_id !== undefined && value.composer_state !== "ready" &&
        value.composer_state !== "waiting_approval"))) ||
    (value.status === "archived" && (!validDate(value.archived_at) ||
      value.deleted_at !== undefined || value.composer_state !== "unavailable")) ||
    (value.status === "deleted" && (!validDate(value.archived_at) ||
      !validDate(value.deleted_at) || value.composer_state !== "unavailable"))) {
    throw new APIRequestError("Thread response violated its lifecycle projection",
      "INVALID_RESPONSE", 502);
  }
  return value as unknown as ThreadView;
}

export function parseThreadCreationControl(value: unknown,
  request: ThreadCreationControlRequestView): ThreadCreationControlView {
  if (!hasExactKeys(value, ["mission", "mode", "replayed", "run", "session", "thread"])) {
    throw new APIRequestError("Thread creation response is invalid", "INVALID_RESPONSE", 502);
  }
  const requestedModelRoute = request.provider && request.model
    ? `${request.provider}/${request.model}` : null;
  const thread = parseThreadView(value.thread);
  const workspaceID = request.workspace_id || thread.workspace_id;
  if (typeof workspaceID !== "string" || !boundedIdentity(workspaceID) || (!request.workspace_id && thread.project_workspace_id !== "")) {
    throw new APIRequestError("Thread creation workspace binding is invalid", "INVALID_RESPONSE", 502);
  }
  const runCreation = parseRunCreationControl({ mission: value.mission, mode: value.mode,
    replayed: value.replayed, run: value.run, session: value.session }, {
    version: "run_creation.v1", goal: request.goal, workspace_id: workspaceID,
    profile: request.profile, surface: request.surface, phase: request.phase,
    network_mode: request.network_mode, allowed_targets: request.allowed_targets, budget: request.budget,
    web_access_mode: request.web_access_mode, web_allowed_targets: request.web_allowed_targets,
  }, requestedModelRoute);
  if (thread.status !== "active" || thread.workspace_id !== workspaceID ||
    thread.mission_id !== runCreation.mission.id || thread.title !== request.goal ||
    thread.active_run_id !== runCreation.run.id || thread.last_run_id !== runCreation.run.id ||
    thread.composer_state !== "ready") {
    throw new APIRequestError("Thread creation response violated its identity binding",
      "INVALID_RESPONSE", 502);
  }
  return { ...runCreation, thread } as ThreadCreationControlView;
}

export function isValidOperatorSteeringMessage(value: unknown): value is Record<string, unknown> {
  if (!isRecord(value) || !hasOnlyKeys(value,
    ["cancelled_at", "committed_at", "created_at", "id", "prepared", "sequence", "status"]) ||
    !boundedIdentity(value.id) || typeof value.prepared !== "boolean" ||
    !safePositiveInteger(value.sequence) || !validDate(value.created_at) ||
    !["pending", "committed", "cancelled"].includes(String(value.status)) ||
    (value.prepared && value.status !== "pending")) {
    return false;
  }
  const committedAt = value.committed_at;
  const cancelledAt = value.cancelled_at;
  return (committedAt === undefined || validDate(committedAt)) &&
    (cancelledAt === undefined || validDate(cancelledAt)) &&
    (value.status !== "pending" || (committedAt === undefined && cancelledAt === undefined)) &&
    (value.status !== "committed" || (committedAt !== undefined && cancelledAt === undefined)) &&
    (value.status !== "cancelled" || (cancelledAt !== undefined && committedAt === undefined));
}

export function parseThreadExecution(value: unknown, threadID: string): import("../../types").ThreadExecutionView {
  if (!isRecord(value) || !hasOnlyKeys(value, ["version", "thread_id", "execution_id", "state",
    "queued_messages", "capability_grant", "last_turn_interrupted"]) || value.version !== "thread_execution.v1" ||
    value.thread_id !== threadID || value.capability_grant !== false ||
    !["idle", "running", "stopping", "stop_failed"].includes(String(value.state)) ||
    !safeBoundedCount(value.queued_messages, Number.MAX_SAFE_INTEGER) ||
    (value.last_turn_interrupted !== undefined && typeof value.last_turn_interrupted !== "boolean") ||
    (value.state !== "idle" && value.last_turn_interrupted === true) ||
    (value.state === "idle" ? value.execution_id !== undefined || value.queued_messages !== 0
      : !boundedIdentity(value.execution_id))) {
    throw new APIRequestError("Thread execution response is invalid", "INVALID_RESPONSE", 502);
  }
  return value as unknown as import("../../types").ThreadExecutionView;
}

export function parseThreadMessageControl(value: unknown, expectedThreadID: string,
  executionAllowed = false): ThreadMessageControlView {
  const required = ["capability_grant", "execution_started", "model_called", "replayed",
    "run_id", "session_id", "steering", "successor_created", "thread", "tool_called", "version"];
  if (!isRecord(value) || !hasOnlyKeys(value, [...required, "predecessor_run_id"]) ||
    required.some((key) => !Object.prototype.hasOwnProperty.call(value, key)) ||
    value.version !== "thread_message_submission.v1" || !boundedIdentity(value.run_id) ||
    !boundedIdentity(value.session_id) || typeof value.successor_created !== "boolean" ||
    typeof value.replayed !== "boolean" || typeof value.execution_started !== "boolean" ||
    typeof value.model_called !== "boolean" || typeof value.tool_called !== "boolean" ||
    value.capability_grant !== false ||
    !isValidOperatorSteeringMessage(value.steering) ||
    (value.predecessor_run_id !== undefined && !boundedIdentity(value.predecessor_run_id))) {
    throw new APIRequestError("Thread message response is invalid", "INVALID_RESPONSE", 502);
  }
  if ((!executionAllowed && (value.execution_started || value.model_called || value.tool_called)) ||
    ((value.model_called || value.tool_called) && !value.execution_started)) {
    throw new APIRequestError("Thread message response is invalid at its execution boundary",
      "INVALID_RESPONSE", 502);
  }
  const thread = parseThreadView(value.thread, expectedThreadID);
  if (thread.status !== "active" || thread.last_run_id !== value.run_id ||
    (thread.active_run_id !== undefined && thread.active_run_id !== value.run_id) ||
    (value.successor_created && (value.predecessor_run_id === undefined ||
      value.predecessor_run_id === value.run_id)) ||
    (!value.successor_created && value.predecessor_run_id !== undefined)) {
    throw new APIRequestError("Thread message response violated its continuation contract",
      "INVALID_RESPONSE", 502);
  }
  return { ...value, thread } as unknown as ThreadMessageControlView;
}

export function parseThreadLifecycleControl(value: unknown, expectedThreadID: string,
  action: "archive" | "restore" | "delete", expectedVersion: number): ThreadLifecycleControlView {
  if (!hasExactKeys(value, ["capability_grant", "thread", "version"]) ||
    value.version !== "thread_lifecycle.v1" || value.capability_grant !== false) {
    throw new APIRequestError("Thread lifecycle response is invalid", "INVALID_RESPONSE", 502);
  }
  const thread = parseThreadView(value.thread, expectedThreadID);
  const expectedStatus = action === "archive" ? "archived" : action === "restore" ? "active" : "deleted";
  if (thread.status !== expectedStatus || thread.version < expectedVersion) {
    throw new APIRequestError("Thread lifecycle response violated its transition contract",
      "INVALID_RESPONSE", 502);
  }
  return { ...value, thread } as unknown as ThreadLifecycleControlView;
}

export function parseThreadRunRecoveryControl(value: unknown, expectedThreadID: string,
  request: ThreadRunRecoveryControlRequestView): ThreadRunRecoveryControlView {
  if (!hasExactKeys(value, ["failed_run", "replayed", "successor_required", "thread", "version"]) ||
    value.version !== "thread_run_recovery.v1" || typeof value.replayed !== "boolean" ||
    value.successor_required !== true || !isRecord(value.failed_run) ||
    !hasOnlyKeys(value.failed_run, ["budget", "config", "created_at", "finished_at", "id",
      "mission_id", "session_id", "started_at", "status", "updated_at"]) ||
    value.failed_run.id !== request.run_id || !boundedIdentity(value.failed_run.id) ||
    !boundedIdentity(value.failed_run.mission_id) ||
    (value.failed_run.session_id !== undefined && !boundedIdentity(value.failed_run.session_id)) ||
    value.failed_run.status !== "failed" || !validDate(value.failed_run.created_at) ||
    !validDate(value.failed_run.updated_at) || !validDate(value.failed_run.finished_at) ||
    !isRecord(value.failed_run.config) || !isRecord(value.failed_run.budget)) {
    throw new APIRequestError("Thread Run recovery response is invalid", "INVALID_RESPONSE", 502);
  }
  const thread = parseThreadView(value.thread, expectedThreadID);
  if (thread.status !== "active" || thread.active_run_id !== undefined ||
    thread.last_run_id !== request.run_id || thread.composer_state !== "successor_required") {
    throw new APIRequestError("Thread Run recovery widened its continuation contract",
      "INVALID_RESPONSE", 502);
  }
  return { ...value, thread } as unknown as ThreadRunRecoveryControlView;
}

export function parseSessionMessageControl(value: unknown,
  expectedSessionID: string): SessionMessageControlView {
  if (!hasExactKeys(value, ["capability_grant", "execution_started", "model_called", "replayed",
    "run_id", "session_id", "steering", "tool_called", "version"]) ||
    value.version !== "session_message_submission.v1" ||
    value.session_id !== expectedSessionID || !boundedIdentity(value.run_id) ||
    !boundedIdentity(value.session_id) || typeof value.replayed !== "boolean" ||
    value.execution_started !== false || value.model_called !== false ||
    value.tool_called !== false || value.capability_grant !== false ||
    !isValidOperatorSteeringMessage(value.steering)) {
    throw new APIRequestError("Run message response is invalid", "INVALID_RESPONSE", 502);
  }
  return value as unknown as SessionMessageControlView;
}

export function parseSessionArchiveControl(value: unknown,
  expectedSessionID: string): SessionArchiveControlView {
  if (!hasExactKeys(value, ["replayed", "session_id", "status", "version"]) ||
    value.version !== "session_archive.v1" || value.session_id !== expectedSessionID ||
    !boundedIdentity(value.session_id) || value.status !== "archived" ||
    typeof value.replayed !== "boolean") {
    throw new APIRequestError("Run-local Session archive response is invalid", "INVALID_RESPONSE", 502);
  }
  return value as unknown as SessionArchiveControlView;
}

export function parseSessionSteeringCancellation(value: unknown,
  expectedSessionID: string, expectedMessageID: string): SessionSteeringCancellationView {
  if (!hasExactKeys(value, ["cancellation_id", "cancellation_kind", "capability_grant",
    "execution_started", "model_called", "replayed", "run_id", "session_id", "steering",
    "tool_called", "version"]) ||
    value.version !== "session_steering_cancellation.v1" ||
    value.session_id !== expectedSessionID || !boundedIdentity(value.run_id) ||
    !boundedIdentity(value.session_id) || !boundedIdentity(value.cancellation_id) ||
    value.cancellation_kind !== "operator" || typeof value.replayed !== "boolean" ||
    value.execution_started !== false || value.model_called !== false ||
    value.tool_called !== false || value.capability_grant !== false ||
    !isRecord(value.steering) || !hasOnlyKeys(value.steering,
      ["cancelled_at", "committed_at", "created_at", "id", "prepared", "sequence", "status"]) ||
    value.steering.id !== expectedMessageID || value.steering.status !== "cancelled" ||
    value.steering.prepared !== false ||
    typeof value.steering.sequence !== "number" ||
    !Number.isSafeInteger(value.steering.sequence) || value.steering.sequence <= 0 ||
    typeof value.steering.created_at !== "string" ||
    !Number.isFinite(Date.parse(value.steering.created_at)) ||
    typeof value.steering.cancelled_at !== "string" ||
    !Number.isFinite(Date.parse(value.steering.cancelled_at)) ||
    value.steering.committed_at !== undefined) {
    throw new APIRequestError("Run-local Session steering cancellation response is invalid",
      "INVALID_RESPONSE", 502);
  }
  return value as unknown as SessionSteeringCancellationView;
}
