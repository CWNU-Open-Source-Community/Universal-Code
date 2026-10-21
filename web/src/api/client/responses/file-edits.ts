import { APIRequestError } from "../protocol";
import {
  boundedIdentity,
  hasExactKeys,
  hasOnlyKeys,
  isRecord,
  isSHA256,
  safeBoundedCount,
  validDate,
  validWorkspaceRelativePath,
} from "../validation";
import { validateApprovalContinuation } from "./approvals";
import { parseOperationReceipt } from "./operations";
import type {
  FileEditApplyView,
  FileEditChangeSetView,
  FileEditPreviewView,
  FileEditProposalRecoveryView,
  FileEditProposalSourceView,
  FileEditProposalView,
  FileEditQueueView,
  FileEditRevertProposalView,
  FileEditReviewRequestView,
  FileEditReviewView,
} from "../../types";

export function parseFileEditProposalSource(value: unknown, runID: string,
  expectedPath: string): FileEditProposalSourceView {
  if (!hasExactKeys(value, ["content", "content_sha256", "editable", "expires_at",
    "file_write", "path", "protocol_version", "run_id", "source_handle", "workspace_id"]) ||
    value.protocol_version !== "file_edit_proposal.v1" || value.run_id !== runID ||
    value.path !== expectedPath || !boundedIdentity(value.run_id) ||
    !boundedIdentity(value.workspace_id) || !validWorkspaceRelativePath(value.path) ||
    typeof value.content !== "string" || new TextEncoder().encode(value.content).length > 131_072 ||
    !isSHA256(value.content_sha256) || typeof value.source_handle !== "string" ||
    !/^[A-Za-z0-9_-]{43}$/u.test(value.source_handle) || !validDate(value.expires_at) ||
    Date.parse(value.expires_at) <= Date.now() || value.editable !== true ||
    value.file_write !== false) {
    throw new APIRequestError("File edit source violated its exact no-write contract",
      "INVALID_RESPONSE", 502);
  }
  return value as unknown as FileEditProposalSourceView;
}

export function parseFileEditProposal(value: unknown, runID: string): FileEditProposalView {
  if (!hasExactKeys(value, ["approval_required", "edit", "file_written",
    "protocol_version", "replayed", "run_id"]) ||
    value.protocol_version !== "file_edit_proposal.v1" || value.run_id !== runID ||
    value.approval_required !== true || value.file_written !== false ||
    typeof value.replayed !== "boolean") {
    throw new APIRequestError("File edit proposal widened write authority", "INVALID_RESPONSE", 502);
  }
  const edit = parseFileEditPreview(value.edit);
  if (edit.status !== "proposed" || edit.apply_enabled !== false ||
    edit.allowed_actions.length > 2) {
    throw new APIRequestError("File edit proposal result is not pending review",
      "INVALID_RESPONSE", 502);
  }
  return { ...value, edit } as unknown as FileEditProposalView;
}

export function parseFileEditProposalRecovery(value: unknown, runID: string,
  editID: string): FileEditProposalRecoveryView {
  if (!hasExactKeys(value, ["current_content_sha256", "edit_id", "editable", "file_write",
    "original_content", "original_sha256", "path", "proposed_content", "proposed_sha256",
    "protocol_version", "review_required", "run_id", "stale", "status", "workspace_id"]) ||
    value.protocol_version !== "file_edit_proposal_recovery.v1" || value.run_id !== runID ||
    value.edit_id !== editID || !boundedIdentity(value.workspace_id) ||
    !validWorkspaceRelativePath(value.path) || typeof value.original_content !== "string" ||
    typeof value.proposed_content !== "string" ||
    new TextEncoder().encode(value.original_content).length > 256 * 1024 ||
    new TextEncoder().encode(value.proposed_content).length > 256 * 1024 ||
    !(isSHA256(value.original_sha256) ||
      (value.original_sha256 === "missing" && value.original_content === "")) ||
    !isSHA256(value.proposed_sha256) ||
    !(isSHA256(value.current_content_sha256) || value.current_content_sha256 === "missing") ||
    value.status !== "proposed" || value.stale !==
      (value.current_content_sha256 !== value.original_sha256) ||
    value.review_required !== true || value.editable !== false || value.file_write !== false) {
    throw new APIRequestError("File edit proposal recovery widened authority",
      "INVALID_RESPONSE", 502);
  }
  return value as unknown as FileEditProposalRecoveryView;
}

export function parseFileEditRevertProposal(value: unknown, runID: string, sourceEditID: string): FileEditRevertProposalView {
  if (!hasExactKeys(value, ["edit", "file_written", "protocol_version", "replayed", "run_id", "source_edit_id"]) ||
    value.protocol_version !== "file_edit_proposal.v1" || value.run_id !== runID ||
    value.source_edit_id !== sourceEditID || value.file_written !== false || typeof value.replayed !== "boolean") {
    throw new APIRequestError("Revert proposal response violated its no-write identity contract", "INVALID_RESPONSE", 502);
  }
  const edit = parseFileEditPreview(value.edit);
  if (edit.id === sourceEditID || edit.operation === "move" || edit.secrets_redacted ||
    (!value.replayed && (edit.status !== "proposed" || edit.apply_enabled))) {
    throw new APIRequestError("Revert proposal did not return a reviewable inverse edit", "INVALID_RESPONSE", 502);
  }
  return { ...value, edit } as unknown as FileEditRevertProposalView;
}

export function parseFileEditPreview(value: unknown): FileEditPreviewView {
  if (!isRecord(value) || !hasOnlyKeys(value, ["allowed_actions", "apply_enabled", "created_at",
    "destination_original_hash", "destination_path", "destination_proposed_hash", "diff", "id",
    "operation", "original_hash", "path", "proposed_hash", "reason", "secrets_redacted",
    "session_id", "status", "updated_at", "workspace_id"]) ||
    !["proposed", "approved", "applied", "denied", "failed"].includes(String(value.status)) ||
    !["replace", "create", "move", "delete"].includes(String(value.operation)) ||
    !boundedIdentity(value.id) || !boundedIdentity(value.session_id) ||
    !boundedIdentity(value.workspace_id) || !validWorkspaceRelativePath(value.path) ||
    value.path === "." ||
    typeof value.diff !== "string" || value.diff.length > 1_100_000 ||
    !(isSHA256(value.original_hash) || value.original_hash === "missing") ||
    !(isSHA256(value.proposed_hash) || value.proposed_hash === "missing") ||
    typeof value.secrets_redacted !== "boolean" || typeof value.apply_enabled !== "boolean" ||
    !Array.isArray(value.allowed_actions) || value.allowed_actions.length > 2 ||
    !value.allowed_actions.every((action) => action === "approve_intent" || action === "deny") ||
    !validDate(value.created_at) || !validDate(value.updated_at) ||
    (value.reason !== undefined && typeof value.reason !== "string") ||
    (value.operation === "move" ?
      (!validWorkspaceRelativePath(value.destination_path) || value.destination_path === "." ||
        value.destination_path === value.path || value.destination_original_hash !== "missing" ||
        !isSHA256(value.destination_proposed_hash)) :
      (value.destination_path !== undefined || value.destination_original_hash !== undefined ||
        value.destination_proposed_hash !== undefined)) ||
    (value.apply_enabled === true &&
      (value.status !== "approved" || value.allowed_actions.length !== 0))) {
    throw new APIRequestError("File edit preview violated its metadata-only contract",
      "INVALID_RESPONSE", 502);
  }
  return value as unknown as FileEditPreviewView;
}

export function parseFileEditQueue(value: unknown, runID: string): FileEditQueueView {
  if (!hasExactKeys(value, ["apply_enabled", "items", "protocol_version", "run_id", "truncated"]) ||
    value.protocol_version !== "file_edit_review.v1" || value.run_id !== runID ||
    typeof value.apply_enabled !== "boolean" || typeof value.truncated !== "boolean" ||
    !Array.isArray(value.items) || value.items.length > 100) {
    throw new APIRequestError("File edit queue response is invalid", "INVALID_RESPONSE", 502);
  }
  const items = value.items.map(parseFileEditPreview);
  if (!value.apply_enabled && items.some((item) => item.apply_enabled)) {
    throw new APIRequestError("File edit queue widened apply authority", "INVALID_RESPONSE", 502);
  }
  return { ...value, items } as unknown as FileEditQueueView;
}

export function parseFileEditChangeSet(value: unknown, runID: string): FileEditChangeSetView {
  const keys = ["applied_count", "apply_independent", "approved_count", "atomic_apply",
    "batch_mutation_supported", "denied_count", "diff_content_included", "failed_count",
    "items", "partial_apply_visible", "proposed_count", "protocol_version",
    "returned_count", "review_independent", "run_id", "session_id", "total_diff_bytes",
    "truncated", "workspace_id"];
  if (!hasExactKeys(value, keys) || value.protocol_version !== "file_edit_change_set.v1" ||
    value.run_id !== runID || !boundedIdentity(value.session_id) ||
    !boundedIdentity(value.workspace_id) || !Array.isArray(value.items) ||
    value.items.length > 100 || !safeBoundedCount(value.proposed_count, 100) ||
    !safeBoundedCount(value.approved_count, 100) ||
    !safeBoundedCount(value.applied_count, 100) ||
    !safeBoundedCount(value.denied_count, 100) ||
    !safeBoundedCount(value.failed_count, 100) ||
    !safeBoundedCount(value.returned_count, 100) ||
    !safeBoundedCount(value.total_diff_bytes, 100 * 1_064_960) ||
    typeof value.truncated !== "boolean" || value.review_independent !== true ||
    value.apply_independent !== true || value.atomic_apply !== false ||
    value.batch_mutation_supported !== false || value.partial_apply_visible !== true ||
    value.diff_content_included !== false) {
    throw new APIRequestError("File edit change set widened batch mutation authority",
      "INVALID_RESPONSE", 502);
  }
  const items = value.items.map((item) => {
    if (!hasOnlyKeys(item, ["allowed_actions", "apply_enabled", "destination_path", "diff_bytes",
      "id", "operation", "path", "secrets_redacted", "status", "updated_at"]) ||
      !boundedIdentity(item.id) ||
      !validWorkspaceRelativePath(item.path) || item.path === "." ||
      !["replace", "create", "move", "delete"].includes(String(item.operation)) ||
      (item.operation === "move" ?
        (!validWorkspaceRelativePath(item.destination_path) || item.destination_path === "." ||
          item.destination_path === item.path) : item.destination_path !== undefined) ||
      !["proposed", "approved", "applied", "denied", "failed"].includes(String(item.status)) ||
      !safeBoundedCount(item.diff_bytes, 1_064_960) ||
      typeof item.secrets_redacted !== "boolean" || typeof item.apply_enabled !== "boolean" ||
      !Array.isArray(item.allowed_actions) || item.allowed_actions.length > 2 ||
      !item.allowed_actions.every((action: unknown) =>
        action === "approve_intent" || action === "deny") ||
      !validDate(item.updated_at) ||
      (item.status === "proposed" ?
        !(item.allowed_actions.length === 0 ||
          (item.allowed_actions.length === 2 &&
            item.allowed_actions.includes("approve_intent") &&
            item.allowed_actions.includes("deny"))) : item.allowed_actions.length !== 0) ||
      (item.apply_enabled && item.status !== "approved")) {
      throw new APIRequestError("File edit change set item violated per-file authority",
        "INVALID_RESPONSE", 502);
    }
    return item;
  });
  const counts = new Map<string, number>([
    ["proposed", value.proposed_count], ["approved", value.approved_count],
    ["applied", value.applied_count], ["denied", value.denied_count],
    ["failed", value.failed_count],
  ]);
  if (value.returned_count !== items.length ||
    [...counts.values()].reduce((sum, count) => sum + count, 0) !== items.length ||
    items.some((item) => counts.get(String(item.status)) === undefined) ||
    value.total_diff_bytes !== items.reduce((sum, item) => sum + Number(item.diff_bytes), 0)) {
    throw new APIRequestError("File edit change set contains inconsistent partial state",
      "INVALID_RESPONSE", 502);
  }
  for (const status of counts.keys()) {
    if (items.filter((item) => item.status === status).length !== counts.get(status)) {
      throw new APIRequestError("File edit change set contains inconsistent partial state",
        "INVALID_RESPONSE", 502);
    }
  }
  return { ...value, items } as unknown as FileEditChangeSetView;
}

export function parseFileEditReview(value: unknown, runID: string, editID: string,
  request: FileEditReviewRequestView): FileEditReviewView {
  const required = ["action", "edit", "file_written", "protocol_version", "replayed", "run_id"];
  if (!isRecord(value) || !hasOnlyKeys(value, [...required, "continuation"]) ||
    required.some((key) => !Object.prototype.hasOwnProperty.call(value, key)) ||
    value.protocol_version !== "file_edit_review.v1" || value.run_id !== runID ||
    value.action !== request.action || value.file_written !== false || typeof value.replayed !== "boolean") {
    throw new APIRequestError("File edit review response violated its no-write contract",
      "INVALID_RESPONSE", 502);
  }
  const edit = parseFileEditPreview(value.edit);
  const expected = request.action === "approve_intent" ? "approved" : "denied";
  if (edit.id !== editID || edit.status !== expected || edit.apply_enabled !== false) {
    throw new APIRequestError("File edit review result does not match the requested decision",
      "INVALID_RESPONSE", 502);
  }
  if (value.continuation !== undefined) validateApprovalContinuation(value.continuation);
  return { ...value, edit } as unknown as FileEditReviewView;
}

export function parseFileEditApply(value: unknown, runID: string, editID: string): FileEditApplyView {
  if (!hasExactKeys(value, ["edit", "file_written", "policy_rechecked", "protocol_version",
    "receipt", "replayed", "run_id", "status"]) || value.protocol_version !== "file_edit_apply.v1" ||
    value.run_id !== runID || (value.status !== "applied" && value.status !== "failed") ||
    typeof value.replayed !== "boolean" || typeof value.file_written !== "boolean" ||
    value.policy_rechecked !== true) {
    throw new APIRequestError("File edit apply response violated its audited contract",
      "INVALID_RESPONSE", 502);
  }
  const edit = parseFileEditPreview(value.edit);
  if (edit.id !== editID || edit.apply_enabled !== false ||
    (value.status === "applied" && edit.status !== "applied") ||
    (value.status === "failed" && edit.status !== "failed")) {
    throw new APIRequestError("File edit apply result does not match the requested edit",
      "INVALID_RESPONSE", 502);
  }
  const receipt = parseOperationReceipt(value.receipt, "file_edit_apply", value.status,
    value.replayed);
  return { ...value, edit, receipt } as unknown as FileEditApplyView;
}
