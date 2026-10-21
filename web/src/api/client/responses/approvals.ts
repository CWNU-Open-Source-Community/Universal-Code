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
  validWorkspaceRelativePath,
} from "../validation";
import type {
  ApprovalDecisionControlRequestView,
  ApprovalDecisionControlView,
  ApprovalQueueView,
  ControlledCommandProposalView,
  HostCommandProposalView,
} from "../../types";

export function parseApprovalQueue(value: unknown, expectedRunID: string): ApprovalQueueView {
  if (!hasExactKeys(value, ["capability_grant", "items", "process_execution_enabled",
    "protocol_version", "run_id", "session_grant_created", "truncated"]) ||
    value.protocol_version !== "approval_queue.v1" || value.run_id !== expectedRunID ||
    !boundedIdentity(value.run_id) || !Array.isArray(value.items) || value.items.length > 100 ||
    typeof value.truncated !== "boolean" || value.process_execution_enabled !== false ||
    value.session_grant_created !== false || value.capability_grant !== false) {
    throw new APIRequestError("Approval queue response is invalid", "INVALID_RESPONSE", 502);
  }
  const identities = new Set<string>();
  for (const item of value.items) {
    const itemID = isRecord(item) ? boundedIdentity(item.id) : "";
    const requiredKeys = ["action_class", "allowed_actions", "capability_grant", "created_at",
      "id", "mode", "process_execution_enabled", "proposal_id", "run_id", "session_id",
      "status", "tool_name", "updated_at", "version", "workspace_id"];
    const optionalKeys = ["canonical_url", "exact_target"];
    if (!isRecord(item) || !hasOnlyKeys(item, [...requiredKeys, ...optionalKeys]) ||
      requiredKeys.some((key) => !Object.prototype.hasOwnProperty.call(item, key)) ||
      item.run_id !== expectedRunID ||
      !["pending", "approved", "denied"].includes(String(item.status)) || !itemID ||
      !boundedIdentity(item.proposal_id) || !boundedIdentity(item.run_id) ||
      !boundedIdentity(item.session_id) ||
      (item.workspace_id !== "" && !boundedIdentity(item.workspace_id)) ||
      !boundedText(item.tool_name, 128) || !boundedText(item.action_class, 128) ||
      !boundedText(item.mode, 64) || !Array.isArray(item.allowed_actions) ||
      item.allowed_actions.length > 3 ||
      !item.allowed_actions.every((action) => action === "approve_once" ||
        action === "approve_for_thread" || action === "approve_for_run" || action === "approve_for_run_app" || action === "deny") ||
      new Set(item.allowed_actions).size !== item.allowed_actions.length ||
      !safePositiveInteger(item.version) || !validDate(item.created_at) ||
      !validDate(item.updated_at) || item.process_execution_enabled !== false ||
      item.capability_grant !== false || identities.has(itemID)) {
      throw new APIRequestError("Approval queue item is invalid", "INVALID_RESPONSE", 502);
    }
    if (item.tool_name === "replace_file" && item.allowed_actions.includes("approve_once")) {
      throw new APIRequestError("File approval exposed write authority", "INVALID_RESPONSE", 502);
    }
    const webFetch = item.tool_name === "web_fetch";
    const recovering = item.status === "approved" || item.status === "denied";
    const validWebFetchActions = item.status === "pending"
      ? item.allowed_actions.length === 3 &&
        item.allowed_actions.includes("approve_once") &&
        item.allowed_actions.includes("approve_for_thread") &&
        item.allowed_actions.includes("deny")
      : item.allowed_actions.length === 1 && (item.status === "approved"
        ? ["approve_once", "approve_for_thread"].includes(String(item.allowed_actions[0]))
        : item.allowed_actions[0] === "deny");
    if (webFetch ? item.action_class !== "public_https_fetch" ||
      !boundedText(item.canonical_url, 4_096) || !boundedText(item.exact_target, 253) ||
      !item.canonical_url.startsWith("https://") ||
      !validWebFetchActions
      : item.canonical_url !== undefined || item.exact_target !== undefined ||
        item.allowed_actions.includes("approve_for_thread") || recovering) {
      throw new APIRequestError("Web fetch approval projection is invalid", "INVALID_RESPONSE", 502);
    }
    if (item.allowed_actions.includes("approve_for_run") && (item.tool_name !== "command_runtime" || item.status !== "pending")) {
      throw new APIRequestError("Bounded approval belongs to another operation", "INVALID_RESPONSE", 502);
    }
    if (item.allowed_actions.includes("approve_for_run_app") &&
      (item.tool_name !== "mcp_tool_call" || item.action_class !== "mcp_server_and_tool" || item.status !== "pending")) {
      throw new APIRequestError("Application approval belongs to another operation", "INVALID_RESPONSE", 502);
    }
    identities.add(itemID);
  }
  return value as unknown as ApprovalQueueView;
}

export function parseApprovalDecision(value: unknown, expectedRunID: string, expectedApprovalID: string,
  request: ApprovalDecisionControlRequestView): ApprovalDecisionControlView {
  const expectedStatus = request.action === "deny" ? "denied" : "approved";
  const required = ["action", "approval_id", "capability_grant",
    "docker_execution_enabled", "execution_resumed", "process_execution_enabled", "proposal_id",
    "replayed", "retry_completed", "retry_scheduled", "run_id", "session_grant_created",
    "shell_execution_enabled", "status", "tool_name", "version", "workspace_write_applied"];
  if (!isRecord(value) || !hasOnlyKeys(value, [...required, "continuation", "bounded_grant"]) ||
    !required.every((key) => Object.hasOwn(value, key)) ||
    value.version !== "approval_control.v1" ||
    value.run_id !== expectedRunID || value.approval_id !== expectedApprovalID ||
    value.action !== request.action || value.status !== expectedStatus ||
    !boundedIdentity(value.run_id) || !boundedIdentity(value.approval_id) ||
    !boundedIdentity(value.proposal_id) || !boundedText(value.tool_name, 128) ||
    typeof value.replayed !== "boolean" || typeof value.execution_resumed !== "boolean" ||
    typeof value.retry_completed !== "boolean" || typeof value.retry_scheduled !== "boolean" ||
    value.execution_resumed !== false || value.retry_completed !== false ||
    (value.tool_name !== "web_fetch" && value.retry_scheduled !== false) ||
    value.process_execution_enabled !== false ||
    value.shell_execution_enabled !== false || value.docker_execution_enabled !== false ||
    value.workspace_write_applied !== false || typeof value.session_grant_created !== "boolean" ||
    value.capability_grant !== false) {
    throw new APIRequestError("Approval decision violated its closed authority contract",
      "INVALID_RESPONSE", 502);
  }
  if (request.action === "approve_for_run") {
    const grant = value.bounded_grant;
    if (value.tool_name !== "command_runtime" || !hasExactKeys(grant, ["id", "scope_fingerprint", "ttl_seconds", "max_uses", "uses_remaining", "use_ordinal", "expires_at", "each_command_requires_review"]) ||
      !boundedIdentity(grant.id) || !isSHA256(grant.scope_fingerprint) || grant.ttl_seconds !== request.grant_ttl_seconds || grant.max_uses !== request.grant_max_uses ||
      !safePositiveInteger(grant.use_ordinal) || grant.use_ordinal > request.grant_max_uses! || !safeBoundedCount(grant.uses_remaining, request.grant_max_uses!) ||
      grant.use_ordinal > request.grant_max_uses! - grant.uses_remaining || !validDate(grant.expires_at) || grant.each_command_requires_review !== true ||
      (value.replayed && value.session_grant_created)) {
      throw new APIRequestError("Bounded command decision is invalid", "INVALID_RESPONSE", 502);
    }
  } else if (request.action === "approve_for_run_app") {
    if (value.tool_name !== "mcp_tool_call" || value.bounded_grant !== undefined) {
      throw new APIRequestError("Application approval decision is invalid", "INVALID_RESPONSE", 502);
    }
  } else if (value.bounded_grant !== undefined || value.session_grant_created !== false) {
    throw new APIRequestError("One-call decision unexpectedly created a grant", "INVALID_RESPONSE", 502);
  }
  if (value.continuation !== undefined) {
    if (!["agent_browser_sensitive", "mcp_tool_call", "command_runtime"].includes(String(value.tool_name))) {
      throw new APIRequestError("Approval continuation belongs to another operation", "INVALID_RESPONSE", 502);
    }
    validateApprovalContinuation(value.continuation, true);
  }
  return value as unknown as ApprovalDecisionControlView;
}

export function parseControlledCommandProposalReceipt(value: unknown): boolean {
  if (!hasExactKeys(value, ["active_process_limit", "backend", "cancelled", "completed_at",
    "environment_inherited", "exit_code", "job_assigned_at_creation", "kill_on_job_close",
    "low_integrity_token", "network_requested", "output_limit_exceeded", "persistent_process",
    "process_memory_limit", "product_execution_enabled", "request_id", "restricted_token",
    "started_at", "stderr_captured_bytes", "stderr_observed_bytes", "stderr_prefix_sha256",
    "stderr_truncated", "stdin_closed", "stdout_captured_bytes", "stdout_observed_bytes",
    "stdout_prefix_sha256", "stdout_truncated", "timed_out", "tree_reaped"]) ||
    !boundedIdentity(value.request_id) || value.backend !== "windows-controlled-v1" ||
    typeof value.exit_code !== "number" || !Number.isSafeInteger(value.exit_code) ||
    !safeBoundedCount(value.stdout_observed_bytes, 64 * 1024 * 1024) ||
    !safeBoundedCount(value.stdout_captured_bytes, 64 * 1024) ||
    !safeBoundedCount(value.stderr_observed_bytes, 64 * 1024 * 1024) ||
    !safeBoundedCount(value.stderr_captured_bytes, 64 * 1024) ||
    value.stdout_captured_bytes > value.stdout_observed_bytes ||
    value.stderr_captured_bytes > value.stderr_observed_bytes ||
    !isSHA256(value.stdout_prefix_sha256) || !isSHA256(value.stderr_prefix_sha256) ||
    typeof value.stdout_truncated !== "boolean" ||
    typeof value.stderr_truncated !== "boolean" ||
    !validDate(value.started_at) || !validDate(value.completed_at) ||
    Date.parse(value.completed_at) < Date.parse(value.started_at) ||
    typeof value.timed_out !== "boolean" || typeof value.cancelled !== "boolean" ||
    typeof value.output_limit_exceeded !== "boolean" || value.tree_reaped !== true ||
    value.restricted_token !== true || value.low_integrity_token !== true ||
    value.job_assigned_at_creation !== true || value.kill_on_job_close !== true ||
    value.active_process_limit !== 1 || value.process_memory_limit !== 512 * 1024 * 1024 ||
    value.stdin_closed !== true || value.environment_inherited !== false ||
    value.network_requested !== false || value.persistent_process !== false ||
    value.product_execution_enabled !== true) {
    return false;
  }
  return true;
}

export function parseControlledCommandProposal(value: unknown, expectedRunID: string,
  expectedProposalID = ""): ControlledCommandProposalView {
  const required = ["capability_grant", "created_at", "evidence_instruction_authorized",
    "execution_authorized", "fingerprint", "id", "instruction_authorized", "kind",
    "mission_id", "operator_review_required", "permission_mode", "permission_revision",
    "policy_version", "protocol_version", "purpose", "run_id", "session_id",
    "timeout_milliseconds", "workspace_id"];
  const optional = ["execution_replayed", "receipt", "relative_path", "result", "review",
    "review_replayed", "untrusted_evidence"];
  if (!isRecord(value) || !hasOnlyKeys(value, [...required, ...optional]) ||
    required.some((key) => !Object.prototype.hasOwnProperty.call(value, key)) ||
    value.protocol_version !== "controlled_command_proposal.v1" ||
    value.policy_version !== "controlled_command_proposal_policy.v1" ||
    value.run_id !== expectedRunID ||
    (expectedProposalID !== "" && value.id !== expectedProposalID) ||
    !boundedIdentity(value.id) || !boundedIdentity(value.run_id) ||
    !boundedIdentity(value.mission_id) || !boundedIdentity(value.session_id) ||
    !boundedIdentity(value.workspace_id) ||
    !["git-status", "git-diff-check", "go-version", "powershell-workspace-list"]
      .includes(String(value.kind)) ||
    !boundedText(value.purpose, 4_800) ||
    !["conservative", "approval", "full_access", "debug"]
      .includes(String(value.permission_mode)) ||
    !safePositiveInteger(value.permission_revision) ||
    !safePositiveInteger(value.timeout_milliseconds) ||
    value.timeout_milliseconds > 120_000 ||
    value.operator_review_required !== true || value.instruction_authorized !== false ||
    value.execution_authorized !== false || value.capability_grant !== false ||
    !isSHA256(value.fingerprint) || !validDate(value.created_at) ||
    value.evidence_instruction_authorized !== false ||
    (value.relative_path !== undefined &&
      (!boundedText(value.relative_path, 1024) ||
        !validWorkspaceRelativePath(value.relative_path))) ||
    (value.review_replayed !== undefined && typeof value.review_replayed !== "boolean") ||
    (value.execution_replayed !== undefined && typeof value.execution_replayed !== "boolean") ||
    (value.untrusted_evidence !== undefined &&
      (typeof value.untrusted_evidence !== "string" ||
        value.untrusted_evidence.length > 16 * 1024 ||
        !value.untrusted_evidence.startsWith("UNTRUSTED GO COMMAND RESULT")))) {
    throw new APIRequestError("Controlled command proposal response is invalid",
      "INVALID_RESPONSE", 502);
  }
  if (value.review !== undefined) {
    const review = value.review;
    if (!hasExactKeys(review, ["capability_grant", "created_at", "decision", "id", "reason",
      "reviewed_by", "single_use_execution_authorized"]) ||
      !boundedIdentity(review.id) || !boundedIdentity(review.reviewed_by) ||
      !boundedText(review.reason, 4_096) || !validDate(review.created_at) ||
      (review.decision !== "approve" && review.decision !== "deny") ||
      review.single_use_execution_authorized !== (review.decision === "approve") ||
      review.capability_grant !== false) {
      throw new APIRequestError("Controlled command review response is invalid",
        "INVALID_RESPONSE", 502);
    }
  }
  if (value.result !== undefined) {
    const result = value.result;
    if (!hasExactKeys(result, ["automatic_retry_allowed", "content_sha256", "created_at", "id",
      "instruction_authorized", "raw_output_persisted", "source_kind", "source_ref", "status"]) ||
      !boundedIdentity(result.id) || !boundedIdentity(result.source_ref) ||
      (result.status !== "completed" && result.status !== "failed") ||
      result.source_kind !== "go_command_result" || !isSHA256(result.content_sha256) ||
      result.instruction_authorized !== false || result.raw_output_persisted !== false ||
      result.automatic_retry_allowed !== false || !validDate(result.created_at)) {
      throw new APIRequestError("Controlled command result response is invalid",
        "INVALID_RESPONSE", 502);
    }
  }
  if ((value.result === undefined) !== (value.receipt === undefined) ||
    (value.receipt !== undefined && !parseControlledCommandProposalReceipt(value.receipt)) ||
    value.result !== undefined && value.review === undefined ||
    value.untrusted_evidence !== undefined && value.result === undefined) {
    throw new APIRequestError("Controlled command proposal response crossed its execution boundary",
      "INVALID_RESPONSE", 502);
  }
  return value as unknown as ControlledCommandProposalView;
}

export function parseHostCommandProposalReceipt(value: unknown): boolean {
  return hasExactKeys(value, ["active_process_limit", "backend", "cancelled", "completed_at",
    "environment_inherited", "exit_code", "job_assigned_at_creation", "job_memory_limit",
    "kill_on_job_close", "low_integrity_token", "network_requested", "non_sandboxed",
    "output_limit_exceeded", "persistent_process", "product_execution_enabled", "request_id",
    "restricted_token", "started_at", "stderr_captured_bytes", "stderr_observed_bytes",
    "stderr_prefix_sha256", "stderr_truncated", "stdin_closed", "stdout_captured_bytes",
    "stdout_observed_bytes", "stdout_prefix_sha256", "stdout_truncated", "timed_out",
    "tree_reaped"]) &&
    boundedIdentity(value.request_id) === value.request_id && value.backend === "windows-host-job-v1" &&
    typeof value.exit_code === "number" && Number.isSafeInteger(value.exit_code) &&
    safeBoundedCount(value.stdout_observed_bytes, 64 * 1024 * 1024) &&
    safeBoundedCount(value.stdout_captured_bytes, 64 * 1024) &&
    safeBoundedCount(value.stderr_observed_bytes, 64 * 1024 * 1024) &&
    safeBoundedCount(value.stderr_captured_bytes, 64 * 1024) &&
    value.stdout_captured_bytes <= value.stdout_observed_bytes &&
    value.stderr_captured_bytes <= value.stderr_observed_bytes &&
    isSHA256(value.stdout_prefix_sha256) && isSHA256(value.stderr_prefix_sha256) &&
    typeof value.stdout_truncated === "boolean" && typeof value.stderr_truncated === "boolean" &&
    validDate(value.started_at) && validDate(value.completed_at) &&
    Date.parse(value.completed_at) >= Date.parse(value.started_at) &&
    typeof value.timed_out === "boolean" && typeof value.cancelled === "boolean" &&
    typeof value.output_limit_exceeded === "boolean" && value.tree_reaped === true &&
    value.non_sandboxed === true && value.restricted_token === false &&
    value.low_integrity_token === false && value.job_assigned_at_creation === true &&
    value.kill_on_job_close === true && value.active_process_limit === 32 &&
    value.job_memory_limit === 2 * 1024 * 1024 * 1024 && value.stdin_closed === true &&
    value.environment_inherited === false && value.network_requested === true &&
    value.persistent_process === false && value.product_execution_enabled === true;
}

export const hostCommandRiskKinds = ["network", "credential", "host_path", "policy_denial",
  "non_whitelisted_tool", "other_high_risk"] as const;

export const hostCommandRiskFields = ["state", "supervisor_turn", "supervisor_tool_call_id",
  "tool_invocation_id", "mode_snapshot_id", "mode_revision", "interaction_snapshot_id",
  "interaction_revision", "execution_profile_snapshot_id", "execution_profile_revision",
  "permission_snapshot_id", "workspace_root_fingerprint", "capability_generation",
  "scope_fingerprint", "risk_kinds", "network_targets", "network_purpose",
  "credential_kinds", "host_paths", "policy_code", "policy_reason", "requested_tool",
  "other_risk_reason", "max_output_bytes", "active_process_limit", "process_memory_bytes",
  "approval_id", "approval_status", "grant_id", "grant_generation", "grant_max_uses",
  "grant_uses_remaining", "grant_expires_at", "grant_consumption_id",
  "invalidation_reason"];

export function boundedRiskText(value: unknown, maximum: number): value is string {
  return boundedText(value, maximum) && !/[\u0000-\u001f\u007f]/u.test(value);
}

export function parseRiskList(value: unknown, maximum: number): string[] | null {
  if (value === undefined) return [];
  if (!Array.isArray(value) || value.length === 0 || value.length > maximum) return null;
  const result: string[] = [];
  for (const item of value) {
    if (!boundedRiskText(item, 512)) return null;
    result.push(item);
  }
  if (new Set(result).size !== result.length ||
    result.some((item, index) => index > 0 && result[index - 1]! > item)) return null;
  return result;
}

export function parseHostCommandProposal(value: unknown, expectedRunID: string,
  expectedProposalID = ""): HostCommandProposalView {
  const required = ["argv", "automatic_retry_allowed", "capability_grant", "created_at",
    "environment_keys", "environment_policy", "environment_sha256",
    "evidence_instruction_authorized", "executable_path", "executable_sha256",
    "execution_authorized", "fingerprint", "id", "instruction_authorized", "mission_id",
    "network_intent", "non_sandboxed", "operator_review_required", "permission_mode",
    "permission_revision", "policy_version", "protocol_version", "purpose", "run_id",
    "session_id", "spec_fingerprint", "timeout_milliseconds", "working_directory",
    "workspace_id"];
  const optional = ["continuation", "execution_replayed", "receipt", "result", "review", "review_replayed",
    "untrusted_evidence", "saved_output", "uncertain", ...hostCommandRiskFields];
  if (!isRecord(value) || !hasOnlyKeys(value, [...required, ...optional]) ||
    required.some((key) => !Object.prototype.hasOwnProperty.call(value, key)) ||
    (value.protocol_version !== "host_command_proposal.v1" &&
      value.protocol_version !== "risk_escalation.v1") || value.run_id !== expectedRunID ||
    (expectedProposalID !== "" && value.id !== expectedProposalID) ||
    boundedIdentity(value.id) !== value.id || boundedIdentity(value.run_id) !== value.run_id ||
    boundedIdentity(value.mission_id) !== value.mission_id ||
    boundedIdentity(value.session_id) !== value.session_id ||
    boundedIdentity(value.workspace_id) !== value.workspace_id ||
    !boundedText(value.executable_path, 32_768) || !isSHA256(value.executable_sha256) ||
    !Array.isArray(value.argv) || value.argv.length > 64 ||
    !value.argv.every((argument) => boundedText(argument, 16 * 1024)) ||
    !boundedText(value.working_directory, 32_768) ||
    value.environment_policy !== "sanitized_host_environment.v1" ||
    !Array.isArray(value.environment_keys) || value.environment_keys.length > 48 ||
    !value.environment_keys.every((key) => boundedText(key, 256) && !key.includes("=")) ||
    !isSHA256(value.environment_sha256) || value.network_intent !== "host" ||
    !safePositiveInteger(value.timeout_milliseconds) || value.timeout_milliseconds > 600_000 ||
    !boundedText(value.purpose, 4_800) || !isSHA256(value.spec_fingerprint) ||
    !safePositiveInteger(value.permission_revision) ||
    typeof value.operator_review_required !== "boolean" || value.non_sandboxed !== true ||
    value.automatic_retry_allowed !== false || value.instruction_authorized !== false ||
    typeof value.execution_authorized !== "boolean" ||
    typeof value.capability_grant !== "boolean" || !isSHA256(value.fingerprint) ||
    !validDate(value.created_at) || value.evidence_instruction_authorized !== false ||
    (value.review_replayed !== undefined && typeof value.review_replayed !== "boolean") ||
    (value.execution_replayed !== undefined && typeof value.execution_replayed !== "boolean") ||
    (value.uncertain !== undefined && typeof value.uncertain !== "boolean") ||
    (value.untrusted_evidence !== undefined &&
      (typeof value.untrusted_evidence !== "string" || value.untrusted_evidence.length > 16 * 1024))) {
    throw new APIRequestError("Host command proposal response is invalid", "INVALID_RESPONSE", 502);
  }

  const risk = value.protocol_version === "risk_escalation.v1";
  if (!risk) {
    if (value.policy_version !== "host_command_policy.v1" || value.permission_mode !== "approval" ||
      value.operator_review_required !== true || value.execution_authorized !== false ||
      value.capability_grant !== false || hostCommandRiskFields.some((field) =>
        Object.prototype.hasOwnProperty.call(value, field)) ||
      (value.untrusted_evidence !== undefined &&
        !value.untrusted_evidence.startsWith("UNTRUSTED HOST COMMAND RESULT"))) {
      throw new APIRequestError("Legacy host command proposal widened its authority",
        "INVALID_RESPONSE", 502);
    }
  } else {
    validateRiskEscalationProposal(value);
  }

  if (value.review !== undefined) {
    const review = value.review;
    if (!hasExactKeys(review, ["capability_grant", "created_at", "decision", "id", "reason",
      "reviewed_by", "single_use_execution_authorized"]) ||
      boundedIdentity(review.id) !== review.id ||
      boundedIdentity(review.reviewed_by) !== review.reviewed_by ||
      !boundedText(review.reason, 4_096) || !validDate(review.created_at) ||
      (review.decision !== "approve" && review.decision !== "deny") ||
      (risk
        ? (review.decision === "deny"
          ? review.single_use_execution_authorized !== false || review.capability_grant !== false
          : review.single_use_execution_authorized !== !value.capability_grant ||
            review.capability_grant !== value.capability_grant)
        : review.single_use_execution_authorized !== (review.decision === "approve") ||
          review.capability_grant !== false) ||
      (risk && review.id !== value.approval_id)) {
      throw new APIRequestError("Host command review response is invalid", "INVALID_RESPONSE", 502);
    }
  }
  if (value.result !== undefined) {
    const result = value.result;
    if (!hasExactKeys(result, ["automatic_retry_allowed", "content_sha256", "created_at", "id",
      "instruction_authorized", "raw_output_persisted", "source_kind", "source_ref", "status"]) ||
      boundedIdentity(result.id) !== result.id ||
      boundedIdentity(result.source_ref) !== result.source_ref ||
      (result.status !== "completed" && result.status !== "failed") ||
      result.source_kind !== "go_command_result" || !isSHA256(result.content_sha256) ||
      result.instruction_authorized !== false || result.raw_output_persisted !== false ||
      result.automatic_retry_allowed !== false || !validDate(result.created_at)) {
      throw new APIRequestError("Host command result response is invalid", "INVALID_RESPONSE", 502);
    }
  }
  if ((value.result === undefined) !== (value.receipt === undefined) ||
    (value.receipt !== undefined && !parseHostCommandProposalReceipt(value.receipt)) ||
    value.result !== undefined && value.review === undefined ||
    value.untrusted_evidence !== undefined && value.result === undefined ||
    (risk && ((value.approval_status === "pending") !== (value.review === undefined) ||
      value.result !== undefined && value.state !==
        (isRecord(value.result) ? value.result.status : undefined) ||
      value.untrusted_evidence !== undefined &&
        !value.untrusted_evidence.startsWith("UNTRUSTED APPROVED RISK ESCALATION RESULT")))) {
    throw new APIRequestError("Host command proposal response crossed its execution boundary",
      "INVALID_RESPONSE", 502);
  }
  if (value.continuation !== undefined) {
    validateApprovalContinuation(value.continuation);
    if (value.review === undefined) {
      throw new APIRequestError("Approval continuation has no saved review", "INVALID_RESPONSE", 502);
    }
  }
  if (value.saved_output !== undefined) {
    validateHostCommandSavedOutput(value.saved_output, value);
  }
  return value as unknown as HostCommandProposalView;
}

export function validateHostCommandSavedOutput(output: unknown, proposal: Record<string, unknown>): void {
  const validStream = (value: unknown): value is Record<string, unknown> => {
    if (!hasExactKeys(value, ["text", "utf8_bytes", "truncated", "redacted"]) ||
      typeof value.text !== "string" || value.redacted !== true ||
      typeof value.truncated !== "boolean" || typeof value.utf8_bytes !== "number" ||
      !Number.isSafeInteger(value.utf8_bytes) || value.utf8_bytes < 0) return false;
    return new TextEncoder().encode(value.text).byteLength === value.utf8_bytes &&
      value.utf8_bytes <= 16 * 1024;
  };
  if (proposal.protocol_version !== "host_command_proposal.v1" ||
    !isRecord(proposal.result) || !isRecord(proposal.receipt) ||
    !hasExactKeys(output, ["result_id", "request_id", "stdout", "stderr"]) ||
    output.result_id !== proposal.result.id || output.request_id !== proposal.receipt.request_id ||
    !validStream(output.stdout) || !validStream(output.stderr) ||
    Number(output.stdout.utf8_bytes) + Number(output.stderr.utf8_bytes) > 16 * 1024 ||
    (proposal.receipt.stdout_truncated === true && output.stdout.truncated !== true) ||
    (proposal.receipt.stderr_truncated === true && output.stderr.truncated !== true)) {
    throw new APIRequestError("Host command saved output does not match its result", "INVALID_RESPONSE", 502);
  }
}

export function validateRiskEscalationProposal(value: Record<string, unknown>): void {
  const riskRequired = ["state", "supervisor_turn", "supervisor_tool_call_id",
    "tool_invocation_id", "mode_snapshot_id", "mode_revision", "interaction_snapshot_id",
    "interaction_revision", "execution_profile_snapshot_id", "execution_profile_revision",
    "permission_snapshot_id", "workspace_root_fingerprint", "capability_generation",
    "scope_fingerprint", "risk_kinds", "max_output_bytes", "active_process_limit",
    "process_memory_bytes", "approval_id", "approval_status"];
  const kinds = parseRiskList(value.risk_kinds, 6);
  const networkTargets = parseRiskList(value.network_targets, 16);
  const credentialKinds = parseRiskList(value.credential_kinds, 16);
  const hostPaths = parseRiskList(value.host_paths, 16);
  const validStates = ["waiting_approval", "approved", "denied", "completed", "failed",
    "invalidated"];
  const validApprovalStatuses = ["pending", "approved", "denied"];
  const grantPresent = value.grant_id !== undefined;
  if (riskRequired.some((key) => !Object.prototype.hasOwnProperty.call(value, key)) ||
    value.policy_version !== "risk_escalation_policy.v1" ||
    value.permission_mode !== "workspace_access" || !validStates.includes(String(value.state)) ||
    !safePositiveInteger(value.supervisor_turn) ||
    boundedIdentity(value.supervisor_tool_call_id) !== value.supervisor_tool_call_id ||
    boundedIdentity(value.tool_invocation_id) !== value.tool_invocation_id ||
    boundedIdentity(value.mode_snapshot_id) !== value.mode_snapshot_id ||
    !safePositiveInteger(value.mode_revision) ||
    boundedIdentity(value.interaction_snapshot_id) !== value.interaction_snapshot_id ||
    !safePositiveInteger(value.interaction_revision) ||
    boundedIdentity(value.execution_profile_snapshot_id) !== value.execution_profile_snapshot_id ||
    !safePositiveInteger(value.execution_profile_revision) ||
    boundedIdentity(value.permission_snapshot_id) !== value.permission_snapshot_id ||
    !isSHA256(value.workspace_root_fingerprint) || !isSHA256(value.capability_generation) ||
    !isSHA256(value.scope_fingerprint) || kinds === null || kinds.length === 0 ||
    !kinds.every((kind) => hostCommandRiskKinds.includes(
      kind as typeof hostCommandRiskKinds[number])) || networkTargets === null ||
    credentialKinds === null || hostPaths === null ||
    value.max_output_bytes !== 64 * 1024 * 1024 || value.active_process_limit !== 32 ||
    value.process_memory_bytes !== 2 * 1024 * 1024 * 1024 ||
    boundedIdentity(value.approval_id) !== value.approval_id ||
    !validApprovalStatuses.includes(String(value.approval_status)) ||
    value.operator_review_required !== (value.approval_status === "pending") ||
    value.execution_authorized !== (value.approval_status === "approved") ||
    value.capability_grant !== grantPresent ||
    kinds.includes("network") !==
      (networkTargets.length > 0 && boundedRiskText(value.network_purpose, 1_200)) ||
    kinds.includes("credential") !== (credentialKinds.length > 0) ||
    kinds.includes("host_path") !== (hostPaths.length > 0) ||
    kinds.includes("policy_denial") !==
      (boundedRiskText(value.policy_code, 512) && boundedRiskText(value.policy_reason, 1_200)) ||
    kinds.includes("non_whitelisted_tool") !== boundedRiskText(value.requested_tool, 512) ||
    kinds.includes("other_high_risk") !== boundedRiskText(value.other_risk_reason, 1_200) ||
    (value.invalidation_reason !== undefined &&
      (!boundedRiskText(value.invalidation_reason, 512) || value.state !== "invalidated")) ||
    (value.state === "invalidated" && value.invalidation_reason === undefined) ||
    (value.uncertain !== undefined && typeof value.uncertain !== "boolean") ||
    (value.uncertain === true && value.state !== "invalidated" && value.state !== "failed") ||
    (value.state === "waiting_approval" && value.approval_status !== "pending") ||
    (value.state === "denied" && value.approval_status !== "denied") ||
    (["approved", "completed", "failed"].includes(String(value.state)) &&
      value.approval_status !== "approved")) {
    throw new APIRequestError("Risk escalation proposal response is invalid",
      "INVALID_RESPONSE", 502);
  }
  if (grantPresent) {
    if (boundedIdentity(value.grant_id) !== value.grant_id ||
      !safePositiveInteger(value.grant_generation) ||
      !safePositiveInteger(value.grant_max_uses) || value.grant_max_uses > 8 ||
      !safeBoundedCount(value.grant_uses_remaining, Number(value.grant_max_uses)) ||
      !validDate(value.grant_expires_at) ||
      boundedIdentity(value.grant_consumption_id) !== value.grant_consumption_id) {
      throw new APIRequestError("Bounded risk escalation grant response is invalid",
        "INVALID_RESPONSE", 502);
    }
  } else if (["grant_generation", "grant_max_uses", "grant_uses_remaining",
    "grant_expires_at", "grant_consumption_id"].some((field) =>
    Object.prototype.hasOwnProperty.call(value, field))) {
    throw new APIRequestError("Risk escalation response contains detached grant metadata",
      "INVALID_RESPONSE", 502);
  }
}

export function validateApprovalContinuation(value: unknown, pendingBrowserTool = false): void {
  if (!isRecord(value) || !hasOnlyKeys(value, ["state", "replayed", "model_called", "tool_called",
    "handoff_id", "error_code", "message"]) ||
    typeof value.state !== "string" || !["not_started", "queued", "completed", "failed", ...(pendingBrowserTool ? ["waiting_approval"] : [])].includes(value.state) ||
    typeof value.replayed !== "boolean" || typeof value.model_called !== "boolean" ||
    typeof value.tool_called !== "boolean" ||
    (value.handoff_id !== undefined && boundedIdentity(value.handoff_id) !== value.handoff_id) ||
    (value.error_code !== undefined && boundedIdentity(value.error_code) !== value.error_code) ||
    (value.message !== undefined && !boundedText(value.message, 4_096)) ||
    (pendingBrowserTool && value.handoff_id !== undefined) ||
    (value.state === "completed" && ((!pendingBrowserTool && !boundedIdentity(value.handoff_id)) || value.error_code !== undefined)) ||
    ((value.state === "not_started" || value.state === "queued") &&
      (value.model_called || value.tool_called || value.handoff_id !== undefined || value.error_code !== undefined))) {
    throw new APIRequestError("Approval continuation response is invalid", "INVALID_RESPONSE", 502);
  }
}
