import { creationBudgetMatches } from "../../task-configuration";
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
import {
  normalizeRequestedNetworkAuthority,
  normalizeRequestedWebAccess,
  scopeMatchesRequestedNetworkAuthority,
  scopeMatchesRequestedWebAccess,
} from "./permissions";
import type {
  ChildTaskProposalView,
  ChildTaskProposalsListView,
  FanoutExecutionView,
  FanoutExecutionsListView,
  RunCreationControlRequestView,
  RunCreationControlView,
  RunEventPollView,
  RunEventStreamView,
  RunExecutionControlRequestView,
  RunExecutionControlView,
  RunLifecycleControlRequestView,
  RunLifecycleControlView,
} from "../../types";

export function parseStreamFrame(value: unknown, expectedRunID: string, expectedRequestID = ""): RunEventStreamView {
  if (typeof value !== "object" || value === null) {
    throw new Error("SSE frame is not an object");
  }
  const frame = value as Partial<RunEventStreamView>;
  if (frame.version !== "run-events.v1" || frame.run_id !== expectedRunID ||
    typeof frame.request_id !== "string" || frame.request_id === "" ||
    (expectedRequestID !== "" && frame.request_id !== expectedRequestID) ||
    typeof frame.cursor !== "string" || frame.cursor === "" || frame.cursor.length > 512 ||
    typeof frame.sequence !== "number" || !Number.isSafeInteger(frame.sequence) || frame.sequence <= 0 ||
    typeof frame.event !== "object" || frame.event === null ||
    frame.event.version !== "v1" || frame.event.run_id !== expectedRunID ||
    frame.event.sequence !== frame.sequence || typeof frame.event.event_id !== "string" ||
    frame.event.event_id === "" || typeof frame.event.mission_id !== "string" ||
    typeof frame.event.type !== "string" || frame.event.type === "" ||
    typeof frame.event.source !== "string" || frame.event.source === "" ||
    typeof frame.event.created_at !== "string") {
    throw new Error("SSE frame does not match run-events.v1");
  }
  return frame as RunEventStreamView;
}

export function parseEventPoll(value: unknown, expectedRunID: string, requestID: string): RunEventPollView {
  if (typeof value !== "object" || value === null) {
    throw new Error("Event poll response is not an object");
  }
  const poll = value as Partial<RunEventPollView>;
  if (poll.version !== "run-event-poll.v1" || poll.run_id !== expectedRunID ||
    typeof poll.cursor !== "string" || poll.cursor === "" || poll.cursor.length > 512 ||
    !Array.isArray(poll.frames) || typeof poll.has_more !== "boolean" ||
    (poll.has_more && poll.frames.length === 0) || poll.frames.length > 100) {
    throw new Error("Event poll response does not match run-event-poll.v1");
  }
  const frames = poll.frames.map((frame) => parseStreamFrame(frame, expectedRunID, requestID));
  for (let index = 1; index < frames.length; index++) {
    if (frames[index]!.sequence !== frames[index - 1]!.sequence + 1) {
      throw new Error("Event poll response contains a sequence gap");
    }
  }
  if (frames.length > 0 && frames[frames.length - 1]!.cursor !== poll.cursor) {
    throw new Error("Event poll response cursor does not match its final frame");
  }
  return { ...poll, frames } as RunEventPollView;
}

export function parseRunCreationControl(value: unknown,
  request: RunCreationControlRequestView, requestedModelRoute: string | null = ""): RunCreationControlView {
  if (!hasExactKeys(value, ["mission", "mode", "replayed", "run", "session"]) ||
    typeof value.replayed !== "boolean" || !isRecord(value.mission) ||
    !isRecord(value.mode) || !isRecord(value.run) || !isRecord(value.session) ||
    !isRecord(value.run.config) || !isRecord(value.run.budget) || !isRecord(value.mission.scope) ||
    !isRecord(value.mode.scope)) {
    throw new APIRequestError("Run creation response is invalid", "INVALID_RESPONSE", 502);
  }
  const missionID = boundedIdentity(value.mission.id);
  const runID = boundedIdentity(value.run.id);
  const sessionID = boundedIdentity(value.session.id);
  const workspaceID = boundedIdentity(value.mission.workspace_id);
  const expectedProfile = request.profile ?? "code";
  const expectedModelRoute = requestedModelRoute || expectedProfile;
  const resolvedModelRoute = value.run.config.model_route;
  // A Thread's default is resolved by Go's model registry. The client validates
  // its shape and Run/Session binding, not a second copy of registry policy.
  const defaultRouteValid = typeof resolvedModelRoute === "string" &&
    (resolvedModelRoute === expectedProfile ||
      (resolvedModelRoute.split("/").length === 2 && resolvedModelRoute.split("/")
        .every((part) => Boolean(boundedIdentity(part)) && part.trim() === part)));
  const expectedSurface = request.surface ?? "code";
  const expectedPhase = request.phase ?? "deliver";
  const expectedNetwork = normalizeRequestedNetworkAuthority(request);
  const expectedWeb = normalizeRequestedWebAccess(request);
  if (!missionID || !runID || !sessionID || !workspaceID ||
    workspaceID !== request.workspace_id || value.mission.goal !== request.goal ||
    value.run.mission_id !== missionID || value.run.session_id !== sessionID ||
    value.session.workspace_id !== workspaceID ||
    value.mission.scope.workspace_id !== workspaceID || value.mode.scope.workspace_id !== workspaceID ||
    value.run.status !== "created" || value.session.status !== "active" ||
    value.run.config.interactive !== true || value.mission.profile !== expectedProfile ||
    value.mode.profile !== expectedProfile ||
    (requestedModelRoute === null ? !defaultRouteValid : resolvedModelRoute !== expectedModelRoute) ||
    value.session.route !== resolvedModelRoute ||
    value.session.title !== value.mission.goal ||
    !scopeMatchesRequestedNetworkAuthority(value.mission.scope, expectedNetwork) ||
    !scopeMatchesRequestedNetworkAuthority(value.mode.scope, expectedNetwork) ||
    (expectedWeb !== null && (!scopeMatchesRequestedWebAccess(value.mission.scope, expectedWeb) ||
      !scopeMatchesRequestedWebAccess(value.mode.scope, expectedWeb))) ||
    !creationBudgetMatches(value.run.budget, value.run.config, request.budget) ||
    value.mode.capability_grant !== false || value.mode.protocol_version !== "run_mode.v1" ||
    value.mode.policy_version !== "mode_policy.v1" || value.mode.revision !== 1 ||
    value.mode.surface !== expectedSurface || value.mode.phase !== expectedPhase) {
    throw new APIRequestError("Run creation response violated its closed authority contract",
      "INVALID_RESPONSE", 502);
  }
  return value as unknown as RunCreationControlView;
}

export function parseRunLifecycleControl(value: unknown, expectedRunID: string,
  request: RunLifecycleControlRequestView): RunLifecycleControlView {
  if (!hasExactKeys(value, ["action", "applied_status", "capability_grant",
    "event_sequence_end", "event_sequence_start", "execution_started", "expected_status",
    "model_called", "replayed", "run", "tool_called", "version"]) ||
    value.version !== "run_lifecycle_control.v1" || value.action !== request.action ||
    !isRecord(value.run) || value.run.id !== expectedRunID || !boundedIdentity(value.run.id) ||
    typeof value.run.status !== "string" || typeof value.replayed !== "boolean" ||
    value.execution_started !== false || value.model_called !== false ||
    value.tool_called !== false || value.capability_grant !== false ||
    !safePositiveInteger(value.event_sequence_start) ||
    !safePositiveInteger(value.event_sequence_end)) {
    throw new APIRequestError("Run lifecycle response is invalid", "INVALID_RESPONSE", 502);
  }
  const transitions = {
    start: ["created", "running", 2],
    pause: ["running", "paused", 1],
    resume: ["paused", "running", 1],
  } as const;
  const transition = transitions[request.action];
  if (!transition || value.expected_status !== transition[0] ||
    value.applied_status !== transition[1] ||
    (!value.replayed && value.run.status !== transition[1]) ||
    (value.replayed && !isRunStatus(value.run.status)) ||
    value.event_sequence_end - value.event_sequence_start + 1 !== transition[2]) {
    throw new APIRequestError("Run lifecycle response violated its transition contract",
      "INVALID_RESPONSE", 502);
  }
  return value as unknown as RunLifecycleControlView;
}

export function isRunStatus(value: unknown): boolean {
  return typeof value === "string" && ["created", "preparing", "running",
    "waiting_approval", "paused", "completed", "failed", "cancelled"].includes(value);
}

export function parseRunExecutionControl(value: unknown, expectedRunID: string,
  request: RunExecutionControlRequestView): RunExecutionControlView {
  if (!isRecord(value) || !hasOnlyKeys(value, ["cancelled_count", "capability_grant",
    "committed_count", "completion_event_sequence", "error_code", "execution_started",
    "max_steps", "model_called", "operation_id", "pending_count", "prepared_count",
    "replayed", "run_id", "run_status", "selected_count", "session_id", "status",
    "steps_completed", "stop_reason", "tool_called", "version"]) ||
    value.version !== "run_execution_handoff.v1" || value.run_id !== expectedRunID ||
    !boundedIdentity(value.run_id) || !boundedIdentity(value.session_id) ||
    !boundedIdentity(value.operation_id) || value.max_steps !== request.max_steps ||
    !safeBoundedCount(value.selected_count, request.max_steps) ||
    !safeBoundedCount(value.steps_completed, value.selected_count) ||
    !safeBoundedCount(value.pending_count, value.selected_count) ||
    !safeBoundedCount(value.prepared_count, value.selected_count) ||
    !safeBoundedCount(value.committed_count, value.selected_count) ||
    !safeBoundedCount(value.cancelled_count, value.selected_count) ||
    value.pending_count + value.prepared_count + value.committed_count +
      value.cancelled_count !== value.selected_count ||
    !safePositiveInteger(value.completion_event_sequence) ||
    (value.status !== "completed" && value.status !== "failed") ||
    !isRunStatus(value.run_status) || typeof value.stop_reason !== "string" ||
    value.stop_reason.length === 0 || value.stop_reason.length > 64 ||
    typeof value.replayed !== "boolean" || typeof value.execution_started !== "boolean" ||
    typeof value.model_called !== "boolean" || typeof value.tool_called !== "boolean" ||
    value.capability_grant !== false || (value.tool_called && !value.model_called) ||
    value.execution_started !== (value.selected_count > 0) ||
    (value.status === "completed" && value.error_code !== undefined) ||
    (value.status === "failed" && (typeof value.error_code !== "string" ||
      value.error_code.length === 0 || value.error_code.length > 64))) {
    throw new APIRequestError("Run execution response is invalid", "INVALID_RESPONSE", 502);
  }
  return value as unknown as RunExecutionControlView;
}

export function parseFanoutExecutions(value: unknown, runID: string,
  planID: string): FanoutExecutionsListView {
  if (!isRecord(value) ||
    value.protocol_version !== "readonly_fanout_executions.v1" ||
    value.plan_id !== planID || !Array.isArray(value.items) || value.items.length > 100) {
    throw new APIRequestError("Fan-out execution list is invalid", "INVALID_RESPONSE", 502);
  }
  return { protocol_version: value.protocol_version, plan_id: value.plan_id,
    items: value.items.map(parseFanoutExecution) } as FanoutExecutionsListView;
}

export function parseFanoutExecution(value: unknown): FanoutExecutionView {
  if (!isRecord(value) || !boundedIdentity(String(value.id)) ||
    !["running", "completed", "failed", "cancelled"].includes(String(value.status)) ||
    !safeBoundedCount(value.parallelism, 6) || !safeBoundedCount(value.max_output_tokens_per_shard, 4096) ||
    !boundedIdentity(String(value.requested_by)) || !validDate(value.started_at) ||
    !validDate(value.updated_at) || !Array.isArray(value.shards) || value.shards.length > 6) {
    throw new APIRequestError("Fan-out execution is invalid", "INVALID_RESPONSE", 502);
  }
  if (value.finished_at !== undefined && !validDate(value.finished_at)) {
    throw new APIRequestError("Fan-out execution finished_at is invalid", "INVALID_RESPONSE", 502);
  }
  const shards = value.shards.map((shard) => {
    if (!isRecord(shard) || !safeBoundedCount(shard.ordinal, 6) ||
      !["pending", "running", "completed", "failed", "cancelled"].includes(String(shard.status)) ||
      !safeBoundedCount(shard.attempt_count, 64) || !safeBoundedCount(shard.current_attempt, 64) ||
      !safeBoundedCount(shard.input_tokens, 1_000_000_000) ||
      !safeBoundedCount(shard.output_tokens, 1_000_000_000) ||
      !safeBoundedCount(shard.total_tokens, 1_000_000_000) ||
      !safeBoundedCount(shard.elapsed_millis, 1_000_000_000) ||
      !safeBoundedCount(shard.finding_count, 4096)) {
      throw new APIRequestError("Fan-out execution shard is invalid", "INVALID_RESPONSE", 502);
    }
    return shard;
  });
  return { ...value, shards } as FanoutExecutionView;
}

export function parseChildTaskProposals(value: unknown, runID: string): ChildTaskProposalsListView {
  if (!isRecord(value) || value.protocol_version !== "child_task_proposals.v1" ||
    !Array.isArray(value.items) || value.items.length > 50) {
    throw new APIRequestError("Child task proposal list is invalid", "INVALID_RESPONSE", 502);
  }
  return { protocol_version: value.protocol_version,
    items: value.items.map((item) => parseChildTaskProposal(item, runID)),
  } as ChildTaskProposalsListView;
}

export function parseChildTaskProposal(value: unknown, runID: string): ChildTaskProposalView {
  if (!isRecord(value) || !boundedIdentity(String(value.id)) || value.run_id !== runID ||
    !["proposed", "approved", "denied"].includes(String(value.status)) ||
    !["core", "readonly_fanout"].includes(String(value.surface)) ||
    !boundedIdentity(String(value.root_agent_id)) || !validDate(value.created_at) ||
    !Array.isArray(value.tasks) || value.tasks.length < 1 || value.tasks.length > 6) {
    throw new APIRequestError("Child task proposal is invalid", "INVALID_RESPONSE", 502);
  }
  for (const task of value.tasks) {
    if (!isRecord(task) || !safeBoundedCount(task.ordinal, 6) || !boundedText(task.title, 256) ||
      !boundedText(task.goal, 4096) || !safePositiveInteger(task.turn_limit) ||
      !safePositiveInteger(task.token_limit) || !safeBoundedCount(task.timeout_millis, 1_800_000)) {
      throw new APIRequestError("Child task proposal task is invalid", "INVALID_RESPONSE", 502);
    }
  }
  return value as unknown as ChildTaskProposalView;
}
