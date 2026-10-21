import { APIRequestError } from "../protocol";
import {
  boundedIdentity,
  boundedText,
  isRecord,
  isSHA256,
  safeBoundedCount,
  safePositiveInteger,
  validDate,
} from "../validation";
import type {
  BatchDeliveriesListView,
  BatchDeliveryCancelView,
  BatchDeliveryMergeControlView,
  BatchDeliveryReconcileView,
  BatchDeliveryReviewControlView,
  BatchDeliverySnapshotView,
  BatchWorkbenchView,
} from "../../types";

export const batchPlanStatuses = ["preparing", "active", "reviewing", "merging",
  "completed", "blocked", "aborted"];

export const batchWorkspaceStatuses = ["preparing", "dispatched", "acknowledged", "working",
  "question", "ready_for_review", "changes_requested", "accepted", "merged",
  "cancelled", "failed", "orphaned"];

export const batchMergeStatuses = ["prepared", "running", "blocked", "completed", "aborted"];

export const batchMailboxKinds = ["dispatch", "ack", "progress", "question", "evidence",
  "ready_for_review", "changes_requested", "accepted", "aborted"];

export const batchValidationKinds = ["git_diff_check", "go_test", "npm_test"];

export const forbiddenBatchProjectionFields = new Set([
  "integration_root", "operation_digest", "owner_token", "owner_token_digest",
  "request_fingerprint", "tool_profile_fingerprint", "validation_json", "worktree_root",
]);

export function batchProjectionContainsPrivateField(value: unknown, depth = 0): boolean {
  if (value === null || typeof value !== "object") return false;
  if (depth > 12) return true;
  if (Array.isArray(value)) {
    return value.some((item) => batchProjectionContainsPrivateField(item, depth + 1));
  }
  return Object.entries(value).some(([key, child]) =>
    forbiddenBatchProjectionFields.has(key) ||
    batchProjectionContainsPrivateField(child, depth + 1));
}

export function parseBatchDeliveries(value: unknown, runID: string): BatchDeliveriesListView {
  if (!isRecord(value) || value.protocol_version !== "batch-deliveries-list.v1" ||
    !Array.isArray(value.items) || value.items.length > 64 ||
    batchProjectionContainsPrivateField(value)) {
    throw new APIRequestError("Batch delivery list is invalid", "INVALID_RESPONSE", 502);
  }
  for (const plan of value.items) parseBatchDeliveryPlan(plan, runID);
  return value as unknown as BatchDeliveriesListView;
}

export function parseBatchWorkbench(value: unknown, runID: string, planID = ""): BatchWorkbenchView {
  if (!isRecord(value) || value.protocol_version !== "batch-delivery-workbench.v1" ||
    typeof value.worker_available !== "boolean" || typeof value.replayed !== "boolean" ||
    !Array.isArray(value.children) || batchProjectionContainsPrivateField(value) ||
    !isRecord(value.snapshot) || !isRecord(value.snapshot.plan) || !boundedIdentity(value.snapshot.plan.id)) {
    throw new APIRequestError("Batch workbench response is invalid", "INVALID_RESPONSE", 502);
  }
  const snapshot = parseBatchDeliverySnapshot(value.snapshot, runID, planID || String(value.snapshot.plan.id));
  if (value.children.length !== snapshot.children.length || value.children.some((child, index) =>
    !isRecord(child) || child.ordinal !== snapshot.children[index].workspace.ordinal ||
    child.generation !== snapshot.children[index].workspace.generation ||
    typeof child.owner_available !== "boolean" || typeof child.executing !== "boolean" ||
    typeof child.outcome_unresolved !== "boolean")) {
    throw new APIRequestError("Batch workbench child binding is invalid", "INVALID_RESPONSE", 502);
  }
  return value as unknown as BatchWorkbenchView;
}

export function isGitObjectID(value: unknown): value is string {
  return typeof value === "string" && /^(?:[0-9a-f]{40}|[0-9a-f]{64})$/u.test(value);
}

export function isBatchPath(value: unknown): value is string {
  return boundedText(value, 4_096) && !value.startsWith("/") && !value.startsWith("../") &&
    !value.includes("\\") && !value.includes("\0") && value !== "..";
}

export function parseBatchDeliverySpec(value: unknown): number {
  if (!isRecord(value) || value.version !== "batch-delivery.v1" ||
    !Array.isArray(value.tasks) || value.tasks.length < 1 || value.tasks.length > 2 ||
    !isRecord(value.contract) || value.contract.require_clean !== true ||
    value.contract.require_independent_review !== true ||
    value.contract.require_all_validations !== true ||
    !safeBoundedCount(value.contract.max_changed_files, 512) ||
    value.contract.max_changed_files === 0 ||
    !safeBoundedCount(value.contract.max_diff_bytes, 16 * 1024 * 1024) ||
    value.contract.max_diff_bytes === 0) {
    throw new APIRequestError("Batch delivery specification is invalid", "INVALID_RESPONSE", 502);
  }
  const taskCount = value.tasks.length;
  const dependencyGraph = new Map<number, number[]>();
  for (const [index, task] of value.tasks.entries()) {
    if (!isRecord(task) || task.ordinal !== index + 1 ||
      !Array.isArray(task.ownership_hints) || task.ownership_hints.length < 1 ||
      task.ownership_hints.length > 32 || !Array.isArray(task.dependency_ordinals) ||
      task.dependency_ordinals.length > value.tasks.length - 1 || !isRecord(task.budget) ||
      !safePositiveInteger(task.budget.turn_limit) ||
      !safePositiveInteger(task.budget.token_limit) ||
      !safeBoundedCount(task.budget.timeout_millis, 1_800_000) ||
      task.budget.timeout_millis === 0 || !Array.isArray(task.validations) ||
      task.validations.length < 1 || task.validations.length > 16 ||
      !Array.isArray(task.expected_artifacts) || task.expected_artifacts.length > 8) {
      throw new APIRequestError("Batch delivery task is invalid", "INVALID_RESPONSE", 502);
    }
    const ownership = new Set<string>();
    for (const hint of task.ownership_hints) {
      if (!isRecord(hint) || !isBatchPath(hint.path) ||
        (hint.kind !== "file" && hint.kind !== "directory") ||
        ownership.has(`${hint.kind}:${hint.path}`)) {
        throw new APIRequestError("Batch delivery ownership is invalid", "INVALID_RESPONSE", 502);
      }
      ownership.add(`${hint.kind}:${hint.path}`);
    }
    const dependencies = task.dependency_ordinals;
    if (dependencies.some((ordinal) => !safePositiveInteger(ordinal) ||
      ordinal > taskCount || ordinal === task.ordinal) ||
      new Set(dependencies).size !== dependencies.length) {
      throw new APIRequestError("Batch delivery dependencies are invalid", "INVALID_RESPONSE", 502);
    }
    dependencyGraph.set(task.ordinal, dependencies);
    let hasDiffCheck = false;
    const validationIDs = new Set<string>();
    for (const validation of task.validations) {
      if (!isRecord(validation) || !boundedText(validation.id, 96) ||
        !batchValidationKinds.includes(String(validation.kind)) ||
        !isBatchPath(validation.scope) || validationIDs.has(validation.id)) {
        throw new APIRequestError("Batch delivery validation is invalid", "INVALID_RESPONSE", 502);
      }
      validationIDs.add(validation.id);
      hasDiffCheck ||= validation.kind === "git_diff_check";
    }
    if (!hasDiffCheck) {
      throw new APIRequestError("Batch delivery validation is incomplete", "INVALID_RESPONSE", 502);
    }
    for (const artifact of task.expected_artifacts) {
      if (!isRecord(artifact) || !isBatchPath(artifact.path_hint) ||
        !boundedText(artifact.kind, 64)) {
        throw new APIRequestError("Batch delivery artifact is invalid", "INVALID_RESPONSE", 502);
      }
    }
  }
  const visiting = new Set<number>();
  const visited = new Set<number>();
  const visit = (ordinal: number): void => {
    if (visiting.has(ordinal)) {
      throw new APIRequestError("Batch delivery dependency cycle is invalid", "INVALID_RESPONSE", 502);
    }
    if (visited.has(ordinal)) return;
    visiting.add(ordinal);
    for (const dependency of dependencyGraph.get(ordinal) ?? []) visit(dependency);
    visiting.delete(ordinal);
    visited.add(ordinal);
  };
  for (const ordinal of dependencyGraph.keys()) visit(ordinal);
  return taskCount;
}

export function parseBatchDeliveryPlan(value: unknown, runID: string, planID = ""): number {
  if (!isRecord(value) || !boundedIdentity(String(value.id)) || value.run_id !== runID ||
    (planID !== "" && value.id !== planID) || !boundedIdentity(String(value.proposal_id)) ||
    !boundedIdentity(String(value.root_agent_id)) || !boundedIdentity(String(value.workspace_id)) ||
    !batchPlanStatuses.includes(String(value.status)) ||
    !isGitObjectID(value.base_commit) ||
    !boundedText(value.source_branch, 256) || !boundedText(value.created_by, 256) ||
    !validDate(value.created_at) || !validDate(value.updated_at) || !isRecord(value.spec) ||
    value.spec.version !== "batch-delivery.v1") {
    throw new APIRequestError("Batch delivery plan is invalid", "INVALID_RESPONSE", 502);
  }
  return parseBatchDeliverySpec(value.spec);
}

export function parseBatchDeliverySnapshot(value: unknown, runID: string,
  planID: string): BatchDeliverySnapshotView {
  if (!isRecord(value) || value.protocol_version !== "batch-delivery.v1" ||
    !isRecord(value.plan) || !Array.isArray(value.children) || value.children.length > 2 ||
    !Array.isArray(value.merge_steps) || value.merge_steps.length > 2 ||
    batchProjectionContainsPrivateField(value)) {
    throw new APIRequestError("Batch delivery snapshot is invalid", "INVALID_RESPONSE", 502);
  }
  const taskCount = parseBatchDeliveryPlan(value.plan, runID, planID);
  if (value.children.length !== taskCount) {
    throw new APIRequestError("Batch delivery child count is invalid", "INVALID_RESPONSE", 502);
  }
  const ordinals = new Set<number>();
  for (const child of value.children) {
    if (!isRecord(child) || !isRecord(child.workspace) || !Array.isArray(child.mailbox)) {
      throw new APIRequestError("Batch delivery child is invalid", "INVALID_RESPONSE", 502);
    }
    const workspace = child.workspace;
    if (workspace.plan_id !== planID || !safePositiveInteger(workspace.ordinal) ||
      workspace.ordinal > 2 || ordinals.has(workspace.ordinal) ||
      !boundedIdentity(String(workspace.agent_id)) || !safePositiveInteger(workspace.generation) ||
      !batchWorkspaceStatuses.includes(String(workspace.status)) ||
      !boundedText(workspace.branch, 256) ||
      !isGitObjectID(workspace.base_commit) ||
      (workspace.head_commit !== undefined && workspace.head_commit !== "" &&
        !isGitObjectID(workspace.head_commit)) ||
      !validDate(workspace.lease_expires_at) || !validDate(workspace.last_heartbeat_at) ||
      !validDate(workspace.created_at) || !validDate(workspace.updated_at) ||
      !isClosedBatchToolProfile(workspace.tool_profile) || child.mailbox.length > 512) {
      throw new APIRequestError("Batch delivery workspace is invalid", "INVALID_RESPONSE", 502);
    }
    ordinals.add(workspace.ordinal);
    let priorSequence = 0;
    for (const message of child.mailbox) {
      if (!isRecord(message) || message.ordinal !== workspace.ordinal ||
        !safePositiveInteger(message.generation) || message.generation > workspace.generation ||
        !safePositiveInteger(message.sequence) || message.sequence <= priorSequence ||
        !boundedIdentity(String(message.id)) || !boundedText(message.actor, 256) ||
        !boundedText(message.summary, 4096) || !Array.isArray(message.evidence_refs) ||
        message.evidence_refs.length > 32 ||
        !message.evidence_refs.every((reference) => boundedText(reference, 2_048)) ||
        !batchMailboxKinds.includes(String(message.kind)) || !validDate(message.created_at)) {
        throw new APIRequestError("Batch delivery mailbox is invalid", "INVALID_RESPONSE", 502);
      }
      priorSequence = message.sequence;
    }
    if (child.receipt !== undefined) parseBatchDeliveryReceipt(child.receipt, workspace.ordinal);
    if (child.review !== undefined) parseBatchDeliveryReview(child.review, workspace.ordinal);
  }
  if (value.merge_queue !== undefined) parseBatchDeliveryMergeQueue(value.merge_queue, planID);
  for (const step of value.merge_steps) parseBatchDeliveryMergeStep(step);
  return value as unknown as BatchDeliverySnapshotView;
}

export function isClosedBatchToolProfile(value: unknown): boolean {
  if (!isRecord(value) || value.version !== "batch-delivery-tools.v1") return false;
  for (const key of ["workspace_list", "workspace_read", "workspace_search",
    "workspace_change", "workspace_apply", "git_status", "git_diff", "git_commit"]) {
    if (value[key] !== true) return false;
  }
  for (const key of ["workspace_delete", "shell", "process", "network", "credentials",
    "debug_terminal", "approvals", "spawn_children"]) {
    if (value[key] !== false) return false;
  }
  return true;
}

export function parseBatchDeliveryReceipt(value: unknown, ordinal: number): void {
  if (!isRecord(value) || value.ordinal !== ordinal || !boundedIdentity(String(value.id)) ||
    !safePositiveInteger(value.generation) || value.protocol_version !== "batch-delivery-receipt.v1" ||
    !isGitObjectID(value.base_commit) || !isGitObjectID(value.head_commit) ||
    !isSHA256(value.diff_sha256) || !isSHA256(value.call_chain_sha256) ||
    !safePositiveInteger(value.diff_bytes) || value.diff_bytes > 16 * 1024 * 1024 ||
    !boundedText(value.diff_stat, 4_096) ||
    !Array.isArray(value.changed_files) || value.changed_files.length < 1 ||
    value.changed_files.length > 512 || !value.changed_files.every(isBatchPath) ||
    new Set(value.changed_files).size !== value.changed_files.length ||
    !Array.isArray(value.test_receipts) || value.test_receipts.length < 1 ||
    value.test_receipts.length > 32 || !Array.isArray(value.evidence_refs) ||
    value.evidence_refs.length > 32 || !Array.isArray(value.limitations) ||
    value.limitations.length > 32 ||
    !value.evidence_refs.every((reference) => boundedText(reference, 2_048)) ||
    !value.limitations.every((limitation) => boundedText(limitation, 1_024)) ||
    !validDate(value.created_at)) {
    throw new APIRequestError("Batch delivery receipt is invalid", "INVALID_RESPONSE", 502);
  }
  for (const receipt of value.test_receipts) {
    if (!isRecord(receipt) || !boundedText(receipt.requirement_id, 96) ||
      !batchValidationKinds.includes(String(receipt.kind)) || !isBatchPath(receipt.scope) ||
      receipt.exit_code !== 0 || !isSHA256(receipt.output_sha256) ||
      !safeBoundedCount(receipt.duration_millis, 10 * 60 * 1_000) ||
      !validDate(receipt.completed_at)) {
      throw new APIRequestError("Batch delivery test receipt is invalid", "INVALID_RESPONSE", 502);
    }
  }
}

export function parseBatchDeliveryReview(value: unknown, ordinal: number): void {
  if (!isRecord(value) || value.ordinal !== ordinal || !boundedIdentity(String(value.id)) ||
    !safePositiveInteger(value.generation) || value.protocol_version !== "batch-delivery-review.v1" ||
    !boundedIdentity(String(value.receipt_id)) || !boundedText(value.reviewer, 256) ||
    !boundedText(value.summary, 4096) ||
    (value.verdict !== "accepted" && value.verdict !== "changes_requested") ||
    !isGitObjectID(value.base_commit) || !isGitObjectID(value.head_commit) ||
    !isSHA256(value.diff_sha256) || !isSHA256(value.call_chain_sha256) ||
    value.full_diff_reviewed !== true || value.call_chain_reviewed !== true ||
    value.tests_reviewed !== true || !validDate(value.created_at)) {
    throw new APIRequestError("Batch delivery review is invalid", "INVALID_RESPONSE", 502);
  }
}

export function parseBatchDeliveryMergeQueue(value: unknown, planID: string): void {
  if (!isRecord(value) || value.plan_id !== planID || !boundedIdentity(String(value.id)) ||
    value.protocol_version !== "batch-delivery-merge-queue.v1" ||
    !batchMergeStatuses.includes(String(value.status)) ||
    !isGitObjectID(value.base_commit) || !isGitObjectID(value.latest_base_commit) ||
    !boundedText(value.integration_branch, 256) ||
    (value.integration_head !== undefined && value.integration_head !== "" &&
      !isGitObjectID(value.integration_head)) ||
    !Array.isArray(value.ordered_ordinals) || value.ordered_ordinals.length < 1 ||
    value.ordered_ordinals.length > 2 ||
    value.ordered_ordinals.some((ordinal) => !safePositiveInteger(ordinal) || ordinal > 2) ||
    new Set(value.ordered_ordinals).size !== value.ordered_ordinals.length ||
    !safeBoundedCount(value.next_index, value.ordered_ordinals.length) ||
    (value.failure_code !== undefined && value.failure_code !== "" &&
      !boundedText(value.failure_code, 256)) ||
    (value.failure_summary !== undefined && value.failure_summary !== "" &&
      !boundedText(value.failure_summary, 4_096)) || !validDate(value.created_at) ||
    !validDate(value.updated_at)) {
    throw new APIRequestError("Batch delivery merge queue is invalid", "INVALID_RESPONSE", 502);
  }
}

export function parseBatchDeliveryMergeStep(value: unknown): void {
  if (!isRecord(value) || !safeBoundedCount(value.step_index, 2) ||
    !safePositiveInteger(value.ordinal) || value.ordinal > 2 ||
    !isGitObjectID(value.input_head) || !isGitObjectID(value.pre_merge_head) ||
    (value.post_merge_head !== undefined && value.post_merge_head !== "" &&
      !isGitObjectID(value.post_merge_head)) ||
    !batchMergeStatuses.includes(String(value.status)) ||
    (value.failure_code !== undefined && value.failure_code !== "" &&
      !boundedText(value.failure_code, 256)) || !validDate(value.created_at) ||
    (value.completed_at !== undefined && !validDate(value.completed_at))) {
    throw new APIRequestError("Batch delivery merge step is invalid", "INVALID_RESPONSE", 502);
  }
}

export function parseBatchDeliveryReviewControl(value: unknown, planID: string,
  ordinal: number): BatchDeliveryReviewControlView {
  if (!isRecord(value) || typeof value.replayed !== "boolean" || !isRecord(value.review) ||
    value.review.plan_id !== undefined && value.review.plan_id !== planID) {
    throw new APIRequestError("Batch delivery review response is invalid", "INVALID_RESPONSE", 502);
  }
  parseBatchDeliveryReview(value.review, ordinal);
  return value as unknown as BatchDeliveryReviewControlView;
}

export function parseBatchDeliveryMergeControl(value: unknown,
  planID: string): BatchDeliveryMergeControlView {
  if (!isRecord(value) || typeof value.base_drifted !== "boolean" ||
    typeof value.replayed !== "boolean" || !isRecord(value.queue) ||
    !Array.isArray(value.steps) || value.steps.length > 2 ||
    batchProjectionContainsPrivateField(value)) {
    throw new APIRequestError("Batch delivery merge response is invalid", "INVALID_RESPONSE", 502);
  }
  parseBatchDeliveryMergeQueue(value.queue, planID);
  for (const step of value.steps) parseBatchDeliveryMergeStep(step);
  return value as unknown as BatchDeliveryMergeControlView;
}

export function parseBatchDeliveryCancel(value: unknown, runID: string,
  planID: string): BatchDeliveryCancelView {
  if (!isRecord(value) || !isRecord(value.snapshot) ||
    !Array.isArray(value.preserved_ordinals) || value.preserved_ordinals.length > 2 ||
    typeof value.integration_preserved !== "boolean" || typeof value.replayed !== "boolean") {
    throw new APIRequestError("Batch delivery cancellation response is invalid", "INVALID_RESPONSE", 502);
  }
  parseBatchDeliverySnapshot(value.snapshot, runID, planID);
  return value as unknown as BatchDeliveryCancelView;
}

export function parseBatchDeliveryReconcile(value: unknown,
  planID: string): BatchDeliveryReconcileView {
  if (!isRecord(value) || value.protocol_version !== "batch-delivery.v1" ||
    value.plan_id !== planID || !safeBoundedCount(value.materialized_worktrees, 2) ||
    !safeBoundedCount(value.recovered_worktrees, 2) || typeof value.expired !== "boolean" ||
    typeof value.merge_resumed !== "boolean" || typeof value.merge_completed !== "boolean" ||
    typeof value.needs_operator_attention !== "boolean") {
    throw new APIRequestError("Batch delivery reconciliation response is invalid",
      "INVALID_RESPONSE", 502);
  }
  return value as unknown as BatchDeliveryReconcileView;
}
