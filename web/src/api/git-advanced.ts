import type {
  GitAdvancedExecuteResultView,
  GitAdvancedProjectionView,
  GitAdvancedReviewResultView,
  GitAdvancedSpecView,
} from "./types";

export const gitAdvancedProtocol = "git-advanced.v1" as const;

export const gitAdvancedOperations = [
  "hunk_stage", "hunk_unstage", "hunk_revert",
  "stash_create", "stash_apply", "stash_pop", "stash_drop",
  "rebase_start", "rebase_continue", "rebase_skip", "rebase_abort",
  "cherry_pick_start", "cherry_pick_continue", "cherry_pick_skip", "cherry_pick_abort",
  "bisect_start", "bisect_good", "bisect_bad", "bisect_skip", "bisect_run", "bisect_reset",
  "worktree_create", "worktree_lock", "worktree_unlock", "worktree_remove", "worktree_prune",
] as const;

export type GitAdvancedOperation = typeof gitAdvancedOperations[number];

type UnknownRecord = Record<string, unknown>;

const operationSet = new Set<string>(gitAdvancedOperations);
const digest = /^[0-9a-f]{64}$/u;
const objectID = /^(?:[0-9a-f]{40}|[0-9a-f]{64})$/u;
const failureCodes = new Set([
  "capability_disabled", "approval_required", "stale_preview", "repository_drift",
  "remote_drift", "permission_drift", "lease_drift", "branch_protected", "conflict",
  "unsafe_repository", "outside_managed_root", "unknown_worktree", "dirty_worktree",
  "budget_exceeded", "timeout", "cancelled", "interrupted", "git_failed",
]);

function record(value: unknown): value is UnknownRecord {
  return typeof value === "object" && value !== null && !Array.isArray(value);
}

function keys(value: UnknownRecord, required: string[], optional: string[] = []): boolean {
  const allowed = new Set([...required, ...optional]);
  return required.every((key) => Object.prototype.hasOwnProperty.call(value, key)) &&
    Object.keys(value).every((key) => allowed.has(key));
}

function identity(value: unknown): value is string {
  return typeof value === "string" && value.length > 0 && value.length <= 256 &&
    value.trim() === value && !/[\u0000-\u001f\u007f]/u.test(value);
}

function text(value: unknown, maximum: number, empty = false): value is string {
  return typeof value === "string" && value.length <= maximum && value.trim() === value &&
    (empty || value.length > 0) && !/[\u0000]/u.test(value);
}

function date(value: unknown): value is string {
  return typeof value === "string" && Number.isFinite(Date.parse(value));
}

function integer(value: unknown, minimum = 0, maximum = Number.MAX_SAFE_INTEGER): value is number {
  return typeof value === "number" && Number.isSafeInteger(value) &&
    value >= minimum && value <= maximum;
}

function relativePath(value: unknown): value is string {
  return typeof value === "string" && value.length > 0 && [...value].length <= 4_096 &&
    !value.includes("\\") && !value.includes(":") && !/\p{Cc}/u.test(value) &&
    value.split("/").every((part) => part !== "" && part !== "." && part !== ".." &&
      part.trim().replace(/[ .]+$/u, "").toLowerCase() !== ".git");
}

function worktreeName(value: unknown): value is string {
  return typeof value === "string" && /^[a-z0-9][a-z0-9_-]{0,79}$/u.test(value);
}

function branch(value: unknown): value is string {
  if (typeof value !== "string" || value === "" || value === "@" || value.length > 255 ||
    value.startsWith("-") || value.startsWith("/") || value.endsWith("/") ||
    value.endsWith(".") || /[\u0000-\u0020\u007f]/u.test(value) ||
    ["\\", "~", "^", ":", "?", "*", "[", "\""].some((item) => value.includes(item)) ||
    value.includes("..") || value.includes("//") || value.includes("@{")) return false;
  return value.split("/").every((part) => part !== "" && !part.startsWith(".") &&
    !part.toLowerCase().endsWith(".lock"));
}

function optionalString(value: UnknownRecord, key: string,
  validate: (candidate: unknown) => boolean): boolean {
  return value[key] === undefined || validate(value[key]);
}

function bool(value: UnknownRecord, key: string): boolean {
  return value[key] === undefined || typeof value[key] === "boolean";
}

function stringArray(value: unknown, maximum: number,
  validate: (candidate: unknown) => boolean): value is string[] {
  return Array.isArray(value) && value.length <= maximum && value.every(validate) &&
    new Set(value).size === value.length;
}

export function validGitAdvancedSpec(value: unknown): value is GitAdvancedSpecView {
  const required = ["operation", "protocol_version"];
  const optional = ["bad_commit", "branch", "commit", "commits", "expected_current",
    "good_commit", "hunk_ids", "include_untracked", "keep_index", "lock_reason", "message",
    "onto_oid", "paths", "recipe", "restore_index", "sequence_id", "stash_oid",
    "upstream_oid", "worktree_id", "worktree_name"];
  if (!record(value) || !keys(value, required, optional) ||
    value.protocol_version !== gitAdvancedProtocol ||
    typeof value.operation !== "string" || !operationSet.has(value.operation) ||
    !bool(value, "include_untracked") || !bool(value, "keep_index") ||
    !bool(value, "restore_index") ||
    !optionalString(value, "message", (item) => text(item, 4_096)) ||
    !optionalString(value, "lock_reason", (item) => text(item, 1_024)) ||
    !optionalString(value, "branch", branch) ||
    !optionalString(value, "worktree_name", worktreeName) ||
    !optionalString(value, "sequence_id", identity) ||
    !optionalString(value, "worktree_id", identity)) {
    return false;
  }
  for (const key of ["bad_commit", "commit", "expected_current", "good_commit", "onto_oid",
    "stash_oid", "upstream_oid"]) {
    if (!optionalString(value, key, (item) => typeof item === "string" && objectID.test(item))) {
      return false;
    }
  }
  if ((value.paths !== undefined &&
      !stringArray(value.paths, 200, (item) => relativePath(item) && !item.startsWith("-"))) ||
    (value.hunk_ids !== undefined && !stringArray(value.hunk_ids, 200,
      (item) => typeof item === "string" && digest.test(item))) ||
    (value.commits !== undefined && !stringArray(value.commits, 128,
      (item) => typeof item === "string" && objectID.test(item)))) {
    return false;
  }
  if (value.recipe !== undefined && (!record(value.recipe) ||
    !keys(value.recipe, ["max_steps", "name", "timeout_seconds"]) ||
    !["go_test", "npm_test"].includes(String(value.recipe.name)) ||
    !integer(value.recipe.max_steps, 1, 128) || !integer(value.recipe.timeout_seconds, 1, 900))) {
    return false;
  }
  const operation = value.operation as GitAdvancedOperation;
  const operationFields: Record<GitAdvancedOperation, readonly string[]> = {
    hunk_stage: ["paths", "hunk_ids"], hunk_unstage: ["paths", "hunk_ids"],
    hunk_revert: ["paths", "hunk_ids"],
    stash_create: ["message", "include_untracked", "keep_index"],
    stash_apply: ["stash_oid", "restore_index"], stash_pop: ["stash_oid", "restore_index"],
    stash_drop: ["stash_oid"],
    rebase_start: ["upstream_oid", "onto_oid"], rebase_continue: ["sequence_id"],
    rebase_skip: ["sequence_id"], rebase_abort: ["sequence_id"],
    cherry_pick_start: ["commits"], cherry_pick_continue: ["sequence_id"],
    cherry_pick_skip: ["sequence_id"], cherry_pick_abort: ["sequence_id"],
    bisect_start: ["good_commit", "bad_commit"],
    bisect_good: ["sequence_id", "expected_current"],
    bisect_bad: ["sequence_id", "expected_current"],
    bisect_skip: ["sequence_id", "expected_current"],
    bisect_run: ["sequence_id", "expected_current", "recipe"],
    bisect_reset: ["sequence_id"],
    worktree_create: ["worktree_name", "branch", "commit"],
    worktree_lock: ["worktree_id", "worktree_name", "lock_reason"],
    worktree_unlock: ["worktree_id", "worktree_name"],
    worktree_remove: ["worktree_id", "worktree_name"], worktree_prune: [],
  };
  const allowed = new Set(operationFields[operation]);
  if (optional.some((key) => Object.prototype.hasOwnProperty.call(value, key) && !allowed.has(key))) {
    return false;
  }
  switch (operation) {
    case "stash_create":
      if (typeof value.message !== "string" || value.message.trim() === "") return false;
      break;
    case "stash_apply": case "stash_pop": case "stash_drop":
      if (typeof value.stash_oid !== "string" || !objectID.test(value.stash_oid)) return false;
      break;
    case "rebase_start":
      if (typeof value.upstream_oid !== "string" || !objectID.test(value.upstream_oid) ||
        typeof value.onto_oid !== "string" || !objectID.test(value.onto_oid)) return false;
      break;
    case "rebase_continue": case "rebase_skip": case "rebase_abort":
    case "cherry_pick_continue": case "cherry_pick_skip": case "cherry_pick_abort":
    case "bisect_reset":
      if (!identity(value.sequence_id)) return false;
      break;
    case "cherry_pick_start":
      if (!Array.isArray(value.commits) || value.commits.length === 0) return false;
      break;
    case "bisect_start":
      if (typeof value.good_commit !== "string" || !objectID.test(value.good_commit) ||
        typeof value.bad_commit !== "string" || !objectID.test(value.bad_commit) ||
        value.good_commit === value.bad_commit) return false;
      break;
    case "bisect_good": case "bisect_bad": case "bisect_skip":
      if (!identity(value.sequence_id) || typeof value.expected_current !== "string" ||
        !objectID.test(value.expected_current)) return false;
      break;
    case "bisect_run":
      if (!identity(value.sequence_id) || typeof value.expected_current !== "string" ||
        !objectID.test(value.expected_current) || value.recipe === undefined) return false;
      break;
    case "worktree_create":
      if (!worktreeName(value.worktree_name) || !branch(value.branch) ||
        typeof value.commit !== "string" || !objectID.test(value.commit)) return false;
      break;
    case "worktree_lock": case "worktree_unlock": case "worktree_remove":
      if (!identity(value.worktree_id) || !worktreeName(value.worktree_name)) return false;
      break;
  }
  return JSON.stringify(value).length <= 65_536;
}

function validBinding(value: unknown): boolean {
  if (!record(value) || !keys(value, ["branch", "captured_at", "common_dir_sha256", "detached",
    "head", "index_sha256", "object_format", "protocol_version", "repository_sha256",
    "sequence_sha256", "stash_sha256", "status_sha256", "worktree_sha256"],
  ["upstream_oid", "upstream_ref"])) return false;
  const expectedObjectLength = value.object_format === "sha256" ? 64 : 40;
  return value.protocol_version === gitAdvancedProtocol &&
    [value.common_dir_sha256, value.index_sha256, value.repository_sha256,
      value.sequence_sha256, value.stash_sha256, value.status_sha256,
      value.worktree_sha256].every((item) => typeof item === "string" && digest.test(item)) &&
    (value.object_format === "sha1" || value.object_format === "sha256") &&
    typeof value.head === "string" && (value.head === "unborn" ||
      (objectID.test(value.head) && value.head.length === expectedObjectLength)) &&
    text(value.branch, 255, true) && typeof value.detached === "boolean" && date(value.captured_at) &&
    optionalString(value, "upstream_ref", (item) => text(item, 1_024)) &&
    optionalString(value, "upstream_oid", (item) => typeof item === "string" &&
      objectID.test(item) && item.length === expectedObjectLength) &&
    (value.upstream_oid === undefined || value.upstream_ref !== undefined);
}

function validCapability(value: unknown): boolean {
  return record(value) && keys(value, ["captured_at", "enabled", "generation",
    "managed_root_sha256", "max_commits", "max_hunks", "max_paths", "operations",
    "protocol_version"]) && value.protocol_version === "git-advanced-capability.v1" &&
    typeof value.enabled === "boolean" && digest.test(String(value.generation)) &&
    digest.test(String(value.managed_root_sha256)) && value.max_commits === 128 &&
    value.max_hunks === 200 && value.max_paths === 200 && date(value.captured_at) &&
    stringArray(value.operations, gitAdvancedOperations.length,
      (item) => typeof item === "string" && operationSet.has(item)) &&
    (value.enabled ? value.operations.length === gitAdvancedOperations.length :
      value.operations.length === 0);
}

function validFileImpact(value: unknown): boolean {
  return record(value) && keys(value, ["change", "destructive", "path"],
    ["after_sha256", "before_sha256"]) && relativePath(value.path) &&
    text(value.change, 128) && typeof value.destructive === "boolean" &&
    optionalString(value, "before_sha256", (item) => digest.test(String(item)) ||
      objectID.test(String(item))) && optionalString(value, "after_sha256",
    (item) => digest.test(String(item)) || objectID.test(String(item)));
}

function validConflict(value: unknown): boolean {
  if (!record(value) || !keys(value, ["active", "can_abort", "can_continue", "can_skip", "files"],
    ["kind"]) || typeof value.active !== "boolean" || typeof value.can_abort !== "boolean" ||
    typeof value.can_continue !== "boolean" || typeof value.can_skip !== "boolean" ||
    !Array.isArray(value.files) || value.files.length > 200 ||
    !optionalString(value, "kind", (item) => text(item, 64))) return false;
  return value.files.every((item) => record(item) && keys(item, ["path"],
    ["base_oid", "ours_oid", "theirs_oid"]) && relativePath(item.path) &&
    ["base_oid", "ours_oid", "theirs_oid"].every((key) => optionalString(item, key,
      (candidate) => typeof candidate === "string" && objectID.test(candidate))));
}

function validHunk(value: unknown): boolean {
  return record(value) && keys(value, ["context_sha256", "destructive", "id", "new_lines",
    "new_start", "old_lines", "old_start", "patch", "patch_sha256", "path"],
  ["base_blob", "index_blob", "worktree_sha256"]) && digest.test(String(value.id)) &&
    digest.test(String(value.context_sha256)) && digest.test(String(value.patch_sha256)) &&
    relativePath(value.path) && typeof value.patch === "string" && value.patch.length <= 1_048_576 &&
    typeof value.destructive === "boolean" && integer(value.new_lines, 0) &&
    integer(value.new_start, 0) && integer(value.old_lines, 0) && integer(value.old_start, 0) &&
    optionalString(value, "base_blob", (item) => objectID.test(String(item))) &&
    optionalString(value, "index_blob", (item) => objectID.test(String(item))) &&
    optionalString(value, "worktree_sha256", (item) => digest.test(String(item)));
}

function validRecovery(value: unknown): boolean {
  return record(value) && keys(value, ["incomplete_reasons", "required"],
    ["checkpoint_id", "checkpoint_level", "restore_action"]) &&
    typeof value.required === "boolean" && stringArray(value.incomplete_reasons, 32,
      (item) => text(item, 1_024)) && optionalString(value, "checkpoint_id", identity) &&
    optionalString(value, "checkpoint_level", (item) => text(item, 64)) &&
    optionalString(value, "restore_action", (item) => text(item, 256));
}

function validPreview(value: unknown): boolean {
  if (!record(value) || !keys(value, ["approval_fingerprint", "binding", "blocked_reasons",
    "capability", "conflict", "created_at", "files", "hunks", "id", "operation",
    "protocol_version", "recovery", "spec", "summary"], ["lease_generation",
    "permission_revision", "permission_snapshot_id", "target"]) ||
    value.protocol_version !== "git-advanced-preview.v1" || !identity(value.id) ||
    !digest.test(String(value.approval_fingerprint)) ||
    typeof value.operation !== "string" || !operationSet.has(value.operation) || !date(value.created_at) ||
    !validBinding(value.binding) || !validCapability(value.capability) ||
    !validConflict(value.conflict) || !validRecovery(value.recovery) ||
    !validGitAdvancedSpec(value.spec) || value.spec.operation !== value.operation ||
    !Array.isArray(value.hunks) || value.hunks.length > 200 || !value.hunks.every(validHunk) ||
    !Array.isArray(value.files) || value.files.length > 200 || !value.files.every(validFileImpact) ||
    !stringArray(value.blocked_reasons, 64, (item) => text(item, 1_024)) ||
    !text(value.summary, 8_192)) return false;
  return optionalString(value, "target", (item) => text(item, 4_096)) &&
    (value.lease_generation === undefined || integer(value.lease_generation, 1)) &&
    (value.permission_revision === undefined || integer(value.permission_revision, 1)) &&
    optionalString(value, "permission_snapshot_id", identity);
}

function validStash(value: unknown): boolean {
  return record(value) && keys(value, ["base_commit", "files", "index_commit", "oid", "subject"],
    ["untracked_commit"]) && [value.base_commit, value.index_commit, value.oid].every((item) =>
    typeof item === "string" && objectID.test(item)) && text(value.subject, 4_096, true) &&
    optionalString(value, "untracked_commit", (item) => objectID.test(String(item))) &&
    Array.isArray(value.files) && value.files.length <= 600 && value.files.every(validFileImpact);
}

function validSequence(value: unknown): boolean {
  return record(value) && keys(value, ["created_at", "current_head", "generation", "id", "kind",
    "last_operation_id", "original_branch", "original_head", "protocol_version",
    "repository_sha256", "run_id", "sequencer_sha256", "started_operation_id", "status",
    "updated_at", "workspace_id"], ["completed_at"]) &&
    value.protocol_version === "git-advanced-sequence.v1" && identity(value.id) &&
    identity(value.run_id) && identity(value.workspace_id) && identity(value.started_operation_id) &&
    identity(value.last_operation_id) && ["rebase", "cherry_pick", "bisect"].includes(String(value.kind)) &&
    ["active", "conflicted", "completed", "aborted", "failed"].includes(String(value.status)) &&
    digest.test(String(value.repository_sha256)) && digest.test(String(value.sequencer_sha256)) &&
    objectID.test(String(value.original_head)) && objectID.test(String(value.current_head)) &&
    text(value.original_branch, 255, true) && integer(value.generation, 1) && date(value.created_at) &&
    date(value.updated_at) && optionalString(value, "completed_at", date);
}

function validWorktree(value: unknown): boolean {
  return record(value) && keys(value, ["branch", "common_dir_sha256", "created_at",
    "created_operation_id", "generation", "head", "id", "last_operation_id", "locked", "name",
    "path_sha256", "present", "protocol_version", "repository_sha256", "run_id", "updated_at",
    "workspace_id"], ["lock_reason", "removed_at"]) &&
    value.protocol_version === "git-managed-worktree.v1" && identity(value.id) && identity(value.run_id) &&
    identity(value.workspace_id) && identity(value.created_operation_id) && identity(value.last_operation_id) &&
    digest.test(String(value.common_dir_sha256)) && digest.test(String(value.repository_sha256)) &&
    digest.test(String(value.path_sha256)) && objectID.test(String(value.head)) &&
    text(value.branch, 255) && text(value.name, 128) && typeof value.locked === "boolean" &&
    typeof value.present === "boolean" && integer(value.generation, 1) && date(value.created_at) &&
    date(value.updated_at) && optionalString(value, "lock_reason", (item) => text(item, 1_024)) &&
    optionalString(value, "removed_at", date);
}

function validEmptyBinding(value: unknown): boolean {
  return record(value) && keys(value, ["branch", "captured_at", "common_dir_sha256", "detached",
    "head", "index_sha256", "object_format", "protocol_version", "repository_sha256",
    "sequence_sha256", "stash_sha256", "status_sha256", "worktree_sha256"]) &&
    value.protocol_version === "" && value.repository_sha256 === "" &&
    value.common_dir_sha256 === "" && value.head === "" && value.branch === "" &&
    value.index_sha256 === "" && value.worktree_sha256 === "" && value.status_sha256 === "" &&
    value.stash_sha256 === "" && value.sequence_sha256 === "" && value.object_format === "" &&
    value.detached === false && value.captured_at === "0001-01-01T00:00:00Z";
}

function validReceipt(value: unknown): boolean {
  if (!record(value)) return false;
  if (!keys(value, ["completed_at", "conflict", "id", "observed_bytes",
    "operation", "post_binding", "pre_binding", "preview_id", "protocol_version", "started_at",
    "status"], ["checkpoint_id", "error_code", "error_summary", "sequence_id", "target_oid",
    "worktree_id"]) || value.protocol_version !== "git-advanced-receipt.v1" || !identity(value.id) ||
    !identity(value.preview_id) || typeof value.operation !== "string" ||
    !operationSet.has(value.operation) ||
    !["succeeded", "conflicted", "failed"].includes(String(value.status)) ||
    !validBinding(value.pre_binding) || !validConflict(value.conflict) ||
    !integer(value.observed_bytes, 0) || !date(value.started_at) || !date(value.completed_at) ||
    !optionalString(value, "checkpoint_id", identity) ||
    !optionalString(value, "sequence_id", identity) ||
    !optionalString(value, "worktree_id", identity) || !optionalString(value, "target_oid",
      (item) => objectID.test(String(item))) ||
    !optionalString(value, "error_code", (item) => text(item, 64)) ||
    !optionalString(value, "error_summary", (item) => text(item, 4_096))) return false;
  if (value.status === "succeeded") {
    return validBinding(value.post_binding) && value.error_code === undefined &&
      value.error_summary === undefined;
  }
  if (value.status === "conflicted") {
    return validBinding(value.post_binding) && value.error_code === "conflict" &&
      record(value.conflict) && value.conflict.active === true &&
      typeof value.error_summary === "string" && value.error_summary.length > 0;
  }
  return typeof value.error_code === "string" && failureCodes.has(value.error_code) &&
    typeof value.error_summary === "string" && value.error_summary.length > 0 &&
    (validBinding(value.post_binding) || validEmptyBinding(value.post_binding));
}

function validOperationBase(value: unknown, parsed: boolean): boolean {
  const required = ["approval_fingerprint", "capability_generation", "created_at", "id",
    "common_dir_sha256", "lease_generation", "operation", "operation_key_sha256", "permission_revision",
    "permission_snapshot_id", "preview_id", "protocol_version", "repository_sha256",
    "request_fingerprint", "status"];
  if (!parsed) required.push("run_id", "session_id", "workspace_id");
  const optional = ["approval_id", "completed_at", "error_code", "started_at"];
  if (parsed) optional.push("preview", "receipt");
  if (!record(value) || !keys(value, required, optional) || value.protocol_version !== gitAdvancedProtocol ||
    !identity(value.id) || !identity(value.preview_id) || !identity(value.permission_snapshot_id) ||
    !digest.test(String(value.operation_key_sha256)) || !digest.test(String(value.request_fingerprint)) ||
    !digest.test(String(value.approval_fingerprint)) || !digest.test(String(value.capability_generation)) ||
    !digest.test(String(value.repository_sha256)) || !digest.test(String(value.common_dir_sha256)) ||
    typeof value.operation !== "string" ||
    !operationSet.has(value.operation) || !["proposed", "running", "succeeded", "conflicted", "failed"]
      .includes(String(value.status)) || !integer(value.permission_revision, 1) ||
    !integer(value.lease_generation, 1) || !date(value.created_at) ||
    !optionalString(value, "approval_id", identity) || !optionalString(value, "started_at", date) ||
    !optionalString(value, "completed_at", date) ||
    !optionalString(value, "error_code", (item) => text(item, 64))) return false;
  if (!parsed && (!identity(value.run_id) || !identity(value.session_id) || !identity(value.workspace_id))) {
    return false;
  }
  if (!parsed) return true;
  if (!validPreview(value.preview) || !record(value.preview) ||
    value.preview_id !== value.preview.id || value.operation !== value.preview.operation) return false;
  return value.receipt === undefined || (validReceipt(value.receipt) && record(value.receipt) &&
    value.receipt.preview_id === value.preview_id && value.receipt.operation === value.operation);
}

function validAuthority(value: unknown): boolean {
  return record(value) && keys(value, ["executable", "lease_active", "permission_revision",
    "permission_snapshot_id", "protocol_version", "scope"], ["lease_expires_at"]) &&
    value.protocol_version === "git-advanced-authority.v1" && typeof value.executable === "boolean" &&
    typeof value.lease_active === "boolean" && integer(value.permission_revision, 1) &&
    identity(value.permission_snapshot_id) && record(value.scope) &&
    keys(value.scope, ["capability_generation", "lease_generation"]) &&
    digest.test(String(value.scope.capability_generation)) && integer(value.scope.lease_generation, 0) &&
    optionalString(value, "lease_expires_at", date) && (!value.executable || value.lease_active);
}

export function isGitAdvancedProjection(value: unknown,
  expectedRunID: string): value is GitAdvancedProjectionView {
  return record(value) && keys(value, ["authority", "binding", "capability", "conflict",
    "operations", "protocol_version", "run_id", "stashes", "workspace_id", "worktrees"],
  ["sequence"]) && value.protocol_version === "git-advanced-api.v1" && value.run_id === expectedRunID &&
    identity(value.run_id) && identity(value.workspace_id) && validAuthority(value.authority) &&
    validBinding(value.binding) && validCapability(value.capability) && validConflict(value.conflict) &&
    record(value.authority) && record(value.authority.scope) && record(value.capability) &&
    value.authority.scope.capability_generation === value.capability.generation &&
    Array.isArray(value.stashes) && value.stashes.length <= 100 && value.stashes.every(validStash) &&
    Array.isArray(value.worktrees) && value.worktrees.length <= 100 && value.worktrees.every(validWorktree) &&
    Array.isArray(value.operations) && value.operations.length <= 100 &&
    value.operations.every((item) => validOperationBase(item, true)) &&
    (value.sequence === undefined || validSequence(value.sequence));
}

function validApproval(value: unknown, runID: string, operationID: string): boolean {
  return record(value) && keys(value, ["ActionClass", "CreatedAt", "DecidedAt", "DecisionReason",
    "GrantID", "ID", "IdempotencyKey", "Mode", "ProposalID", "RequestFingerprint", "RequestedBy",
    "ReviewedBy", "RunID", "SessionID", "Status", "ToolName", "UpdatedAt", "Version", "WorkspaceID"]) &&
    identity(value.ID) && value.ProposalID === operationID && value.RunID === runID &&
    value.ToolName === "git.advanced" && value.ActionClass === "git_advanced_write" &&
    value.Mode === "per_call" && ["pending", "approved", "denied"].includes(String(value.Status)) &&
    value.GrantID === "" && digest.test(String(value.RequestFingerprint)) &&
    identity(value.SessionID) && identity(value.WorkspaceID) && integer(value.Version, 1) &&
    date(value.CreatedAt) && date(value.UpdatedAt);
}

export function isGitAdvancedReviewResult(value: unknown, expectedRunID: string,
  discovery: boolean): value is GitAdvancedReviewResultView {
  if (!record(value) || !keys(value, ["preview", "protocol_version", "replayed", "run_id",
    "workspace_id"], ["approval", "operation"]) || value.protocol_version !== "git-advanced-api.v1" ||
    value.run_id !== expectedRunID || !identity(value.workspace_id) || typeof value.replayed !== "boolean" ||
    !validPreview(value.preview)) return false;
  if (discovery) return value.operation === undefined && value.approval === undefined && !value.replayed;
  return (value.operation === undefined && value.approval === undefined) ||
    (validOperationBase(value.operation, false) && record(value.operation) && record(value.preview) &&
      value.operation.preview_id === value.preview.id &&
      validApproval(value.approval, expectedRunID, String(value.operation.id)));
}

export function isGitAdvancedExecuteResult(value: unknown,
  expectedOperationID: string): value is GitAdvancedExecuteResultView {
  return record(value) && keys(value, ["boundary", "operation", "protocol_version", "receipt", "replayed"],
    ["sequence", "worktree"]) && value.protocol_version === "git-advanced-api.v1" &&
    typeof value.replayed === "boolean" && validOperationBase(value.operation, false) &&
    record(value.operation) && value.operation.id === expectedOperationID && validReceipt(value.receipt) &&
    record(value.receipt) && value.receipt.preview_id === value.operation.preview_id &&
    value.receipt.operation === value.operation.operation && record(value.boundary) &&
    (value.sequence === undefined || validSequence(value.sequence)) &&
    (value.worktree === undefined || validWorktree(value.worktree));
}
