import { APIRequestError } from "../protocol";
import {
  hasExactKeys,
  isSHA256,
  safeBoundedCount,
  validDate,
  validWorkspaceRelativePath,
} from "../validation";
import type {
  RepositoryCommitComparisonView,
  RepositoryCommitDetailView,
  RepositoryCommitFilePreviewView,
  RepositoryDiffView,
  RepositoryFileHistoryView,
  RepositoryHistoryView,
  RepositoryStateView,
} from "../../types";

export function parseRepositoryState(value: unknown, workspaceID: string): RepositoryStateView {
  const keys = ["available", "branch", "changes", "clean", "conflicted_count",
    "content_included", "detached", "head", "hooks_executed", "kind", "network_used",
    "process_started", "protocol_version", "read_only", "redaction_count",
    "remote_config_included", "root_path_exposed", "staged_count", "truncated",
    "untracked_count", "workspace_id", "worktree_count"];
  if (!hasExactKeys(value, keys) || value.protocol_version !== "repository_state.v1" ||
    value.workspace_id !== workspaceID || !["none", "git"].includes(String(value.kind)) ||
    typeof value.available !== "boolean" || typeof value.clean !== "boolean" ||
    typeof value.detached !== "boolean" || typeof value.truncated !== "boolean" ||
    value.read_only !== true || value.root_path_exposed !== false ||
    value.content_included !== false || value.remote_config_included !== false ||
    value.process_started !== false || value.network_used !== false ||
    value.hooks_executed !== false || typeof value.branch !== "string" ||
    value.branch.length > 255 || /[\u0000-\u001f\u007f]/u.test(value.branch) ||
    typeof value.head !== "string" || !/^(?:[0-9a-f]{12})?$/u.test(value.head) ||
    !safeBoundedCount(value.staged_count, 10_000) ||
    !safeBoundedCount(value.worktree_count, 10_000) ||
    !safeBoundedCount(value.untracked_count, 10_000) ||
    !safeBoundedCount(value.conflicted_count, 10_000) ||
    !safeBoundedCount(value.redaction_count, 10_000) || !Array.isArray(value.changes) ||
    value.changes.length > 200) {
    throw new APIRequestError("Repository state violated its read-only bounded contract",
      "INVALID_RESPONSE", 502);
  }
  const changes = value.changes.map((change) => {
    if (!hasExactKeys(change, ["path", "staging", "worktree"]) ||
      !validWorkspaceRelativePath(change.path) || change.path === "." ||
      !["unmodified", "untracked", "modified", "added", "deleted", "renamed", "copied",
        "conflicted"].includes(String(change.staging)) ||
      !["unmodified", "untracked", "modified", "added", "deleted", "renamed", "copied",
        "conflicted"].includes(String(change.worktree)) ||
      (change.staging === "unmodified" && change.worktree === "unmodified")) {
      throw new APIRequestError("Repository change widened path or status authority",
        "INVALID_RESPONSE", 502);
    }
    return change;
  });
  const total = value.staged_count + value.worktree_count + value.untracked_count;
  if ((value.available !== (value.kind === "git")) ||
    (!value.available && (value.clean || value.detached || value.branch !== "" ||
      value.head !== "" || changes.length !== 0 || total !== 0 ||
      value.conflicted_count !== 0 || value.redaction_count !== 0 || value.truncated)) ||
    (value.available && value.clean !== (total === 0)) ||
    value.conflicted_count > value.staged_count + value.worktree_count) {
    throw new APIRequestError("Repository state contains inconsistent status facts",
      "INVALID_RESPONSE", 502);
  }
  return { ...value, changes } as unknown as RepositoryStateView;
}

export function parseRepositoryDiff(value: unknown, workspaceID: string): RepositoryDiffView {
  const keys = ["authority_granted", "available", "base_head", "hooks_executed",
    "instruction_authorized", "items", "kind", "mutation_supported", "network_used",
    "omitted_count", "patch_content_included", "process_started", "protocol_version",
    "raw_content_included", "read_only", "redaction_count", "remote_config_included",
    "returned_count", "root_path_exposed", "total_patch_bytes", "truncated", "workspace_id"];
  if (!hasExactKeys(value, keys) || value.protocol_version !== "repository_diff.v1" ||
    value.workspace_id !== workspaceID || !["none", "git"].includes(String(value.kind)) ||
    typeof value.available !== "boolean" || typeof value.truncated !== "boolean" ||
    value.read_only !== true || value.instruction_authorized !== false ||
    value.mutation_supported !== false || value.authority_granted !== false ||
    value.root_path_exposed !== false || value.raw_content_included !== false ||
    value.patch_content_included !== value.available || value.remote_config_included !== false ||
    value.process_started !== false || value.network_used !== false || value.hooks_executed !== false ||
    typeof value.base_head !== "string" || !/^(?:[0-9a-f]{12})?$/u.test(value.base_head) ||
    !Array.isArray(value.items) || value.items.length > 50 ||
    !safeBoundedCount(value.returned_count, 50) || value.returned_count !== value.items.length ||
    !safeBoundedCount(value.omitted_count, 10_000) ||
    !safeBoundedCount(value.redaction_count, 10_000) ||
    !safeBoundedCount(value.total_patch_bytes, 512 * 1024) ||
    value.available !== (value.kind === "git")) {
    throw new APIRequestError("Repository diff violated its bounded read-only contract",
      "INVALID_RESPONSE", 502);
  }
  const status = ["unmodified", "untracked", "modified", "added", "deleted", "renamed",
    "copied", "conflicted"];
  const contentStates = ["text", "binary_or_unsupported", "size_limited", "linked", "unavailable"];
  let totalBytes = 0;
  let redactedItems = 0;
  const paths = new Set<string>();
  const items = value.items.map((item) => {
    if (!hasExactKeys(item, ["added_lines", "content_state", "deleted_lines", "patch",
      "patch_bytes", "path", "redacted", "staging", "truncated", "worktree"]) ||
      !validWorkspaceRelativePath(item.path) || item.path === "." || paths.has(String(item.path)) ||
      !status.includes(String(item.staging)) || !status.includes(String(item.worktree)) ||
      !contentStates.includes(String(item.content_state)) || typeof item.patch !== "string" ||
      !safeBoundedCount(item.patch_bytes, 64 * 1024) ||
      new TextEncoder().encode(item.patch).length !== item.patch_bytes ||
      !safeBoundedCount(item.added_lines, 64 * 1024) ||
      !safeBoundedCount(item.deleted_lines, 64 * 1024) ||
      typeof item.redacted !== "boolean" || typeof item.truncated !== "boolean" ||
      (item.content_state !== "text" &&
        (item.patch !== "" || item.patch_bytes !== 0 || item.added_lines !== 0 ||
          item.deleted_lines !== 0 || item.truncated))) {
      throw new APIRequestError("Repository diff item widened content or path authority",
        "INVALID_RESPONSE", 502);
    }
    paths.add(String(item.path));
    totalBytes += Number(item.patch_bytes);
    redactedItems += item.redacted ? 1 : 0;
    return item;
  });
  if (totalBytes !== value.total_patch_bytes || redactedItems > value.redaction_count ||
    ((items.some((item) => item.truncated) || value.omitted_count > 0) && !value.truncated) ||
    (!value.available && (items.length !== 0 || value.base_head !== "" ||
      value.total_patch_bytes !== 0 || value.omitted_count !== 0 || value.redaction_count !== 0))) {
    throw new APIRequestError("Repository diff contains inconsistent bounded facts",
      "INVALID_RESPONSE", 502);
  }
  return { ...value, items } as unknown as RepositoryDiffView;
}

export function parseRepositoryHistory(value: unknown, workspaceID: string): RepositoryHistoryView {
  const keys = ["author_identity_included", "available", "branches", "commit_body_included",
    "commits", "detached", "first_parent_only", "head", "hooks_executed", "kind",
    "network_used", "omitted_branch_count", "process_started", "protocol_version",
    "read_only", "redaction_count", "remote_config_included", "returned_branch_count",
    "returned_commit_count", "root_path_exposed", "truncated", "workspace_id"];
  if (!hasExactKeys(value, keys) || value.protocol_version !== "repository_history.v1" ||
    value.workspace_id !== workspaceID || !["none", "git"].includes(String(value.kind)) ||
    typeof value.available !== "boolean" || value.available !== (value.kind === "git") ||
    typeof value.detached !== "boolean" || typeof value.truncated !== "boolean" ||
    value.first_parent_only !== true || value.read_only !== true ||
    value.root_path_exposed !== false || value.author_identity_included !== false ||
    value.commit_body_included !== false || value.remote_config_included !== false ||
    value.process_started !== false || value.network_used !== false ||
    value.hooks_executed !== false || typeof value.head !== "string" ||
    !/^(?:[0-9a-f]{12})?$/u.test(value.head) || !Array.isArray(value.commits) ||
    !Array.isArray(value.branches) || value.commits.length > 50 || value.branches.length > 64 ||
    !safeBoundedCount(value.returned_commit_count, 50) ||
    value.returned_commit_count !== value.commits.length ||
    !safeBoundedCount(value.returned_branch_count, 64) ||
    value.returned_branch_count !== value.branches.length ||
    !safeBoundedCount(value.omitted_branch_count, 1024) ||
    !safeBoundedCount(value.redaction_count, 10_000)) {
    throw new APIRequestError("Repository history violated its bounded local contract",
      "INVALID_RESPONSE", 502);
  }
  const hashes = new Set<string>();
  const objectIDs = new Set<string>();
  const commits = value.commits.map((commit) => {
    if (!hasExactKeys(commit, ["committed_at", "hash", "object_id", "parent_count", "redacted",
      "subject", "subject_bounded"]) || typeof commit.hash !== "string" ||
      !/^[0-9a-f]{12}$/u.test(commit.hash) || hashes.has(commit.hash) ||
      typeof commit.object_id !== "string" || !/^[0-9a-f]{40}$/u.test(commit.object_id) ||
      !commit.object_id.startsWith(commit.hash) || objectIDs.has(commit.object_id) ||
      typeof commit.subject !== "string" || commit.subject.length === 0 ||
      [...commit.subject].length > 512 || /[\u0000-\u001f\u007f]/u.test(commit.subject) ||
      !safeBoundedCount(commit.parent_count, 10_000) || !validDate(commit.committed_at) ||
      typeof commit.redacted !== "boolean" || typeof commit.subject_bounded !== "boolean") {
      throw new APIRequestError("Repository commit widened history or privacy authority",
        "INVALID_RESPONSE", 502);
    }
    hashes.add(commit.hash);
    objectIDs.add(commit.object_id);
    return commit;
  });
  const branchNames = new Set<string>();
  let currentBranches = 0;
  const branches = value.branches.map((branch) => {
    if (!hasExactKeys(branch, ["current", "head", "name"]) ||
      typeof branch.name !== "string" || branch.name.length === 0 ||
      [...branch.name].length > 255 || /[\u0000-\u001f\u007f]/u.test(branch.name) ||
      branchNames.has(branch.name) || typeof branch.head !== "string" ||
      !/^[0-9a-f]{12}$/u.test(branch.head) || typeof branch.current !== "boolean") {
      throw new APIRequestError("Repository branch widened local metadata authority",
        "INVALID_RESPONSE", 502);
    }
    branchNames.add(branch.name);
    currentBranches += branch.current ? 1 : 0;
    return branch;
  });
  if (currentBranches > 1 ||
    (!value.available && (value.head !== "" || value.detached || commits.length !== 0 ||
      branches.length !== 0 || value.omitted_branch_count !== 0 ||
      value.redaction_count !== 0 || value.truncated)) ||
    (value.detached && currentBranches !== 0) ||
    (value.head === "" && commits.length !== 0)) {
    throw new APIRequestError("Repository history contains inconsistent local facts",
      "INVALID_RESPONSE", 502);
  }
  return { ...value, commits, branches } as unknown as RepositoryHistoryView;
}

export function parseRepositoryFileHistory(value: unknown, workspaceID: string,
  path: string): RepositoryFileHistoryView {
  const keys = ["author_identity_included", "authority_granted", "available",
    "checkout_performed", "commit_body_included", "entries", "file_content_included",
    "first_parent_only", "head", "hooks_executed", "kind", "metadata_only", "network_used",
    "observed", "patch_included", "path", "process_started", "protocol_version", "read_only",
    "redaction_count", "reference_updated", "remote_config_included", "rename_inferred",
    "returned_entry_count", "root_path_exposed", "scanned_commit_count", "truncated",
    "workspace_id"];
  if (!hasExactKeys(value, keys) || value.protocol_version !== "repository_file_history.v1" ||
    value.workspace_id !== workspaceID || value.path !== path ||
    !validWorkspaceRelativePath(value.path) || value.path === "." ||
    !["none", "git"].includes(String(value.kind)) ||
    typeof value.available !== "boolean" || value.available !== (value.kind === "git") ||
    typeof value.head !== "string" || !/^(?:[0-9a-f]{12})?$/u.test(value.head) ||
    !Array.isArray(value.entries) || value.entries.length > 50 ||
    !safeBoundedCount(value.scanned_commit_count, 512) ||
    !safeBoundedCount(value.returned_entry_count, 50) ||
    value.returned_entry_count !== value.entries.length ||
    !safeBoundedCount(value.redaction_count, 512 * 240) ||
    typeof value.observed !== "boolean" || value.observed !== (value.entries.length > 0) ||
    typeof value.truncated !== "boolean" || value.first_parent_only !== true ||
    value.rename_inferred !== false || value.metadata_only !== true || value.read_only !== true ||
    value.authority_granted !== false || value.root_path_exposed !== false ||
    value.author_identity_included !== false || value.commit_body_included !== false ||
    value.file_content_included !== false || value.patch_included !== false ||
    value.remote_config_included !== false || value.checkout_performed !== false ||
    value.reference_updated !== false || value.process_started !== false ||
    value.network_used !== false || value.hooks_executed !== false) {
    throw new APIRequestError("Repository file history violated its exact metadata contract",
      "INVALID_RESPONSE", 502);
  }
  const objectIDs = new Set<string>();
  const kinds = ["", "regular", "executable", "symlink", "submodule"];
  let redactedEntries = 0;
  const entries = value.entries.map((entry) => {
    if (!hasExactKeys(entry, ["change", "committed_at", "content_changed", "current_kind",
      "hash", "mode_changed", "object_id", "previous_kind", "redacted", "subject",
      "subject_bounded"]) || typeof entry.object_id !== "string" ||
      !/^[0-9a-f]{40}$/u.test(entry.object_id) || objectIDs.has(entry.object_id) ||
      typeof entry.hash !== "string" || !/^[0-9a-f]{12}$/u.test(entry.hash) ||
      !entry.object_id.startsWith(entry.hash) || typeof entry.subject !== "string" ||
      entry.subject.length === 0 || [...entry.subject].length > 512 ||
      /[\u0000-\u001f\u007f]/u.test(entry.subject) || !validDate(entry.committed_at) ||
      !["added", "modified", "deleted"].includes(String(entry.change)) ||
      !kinds.includes(String(entry.previous_kind)) || !kinds.includes(String(entry.current_kind)) ||
      typeof entry.content_changed !== "boolean" || typeof entry.mode_changed !== "boolean" ||
      (!entry.content_changed && !entry.mode_changed) || typeof entry.redacted !== "boolean" ||
      typeof entry.subject_bounded !== "boolean" ||
      (entry.change === "added" && (entry.previous_kind !== "" || entry.current_kind === "" ||
        !entry.content_changed || !entry.mode_changed)) ||
      (entry.change === "deleted" && (entry.previous_kind === "" || entry.current_kind !== "" ||
        !entry.content_changed || !entry.mode_changed)) ||
      (entry.change === "modified" &&
        (entry.previous_kind === "" || entry.current_kind === ""))) {
      throw new APIRequestError("Repository file history entry widened content authority",
        "INVALID_RESPONSE", 502);
    }
    objectIDs.add(entry.object_id);
    redactedEntries += entry.redacted ? 1 : 0;
    return entry;
  });
  if (redactedEntries > value.redaction_count ||
    (!value.available && (value.head !== "" || value.scanned_commit_count !== 0 ||
      entries.length !== 0 || value.redaction_count !== 0 || value.observed || value.truncated)) ||
    (value.head === "" && (value.scanned_commit_count !== 0 || entries.length !== 0)) ||
    (entries.length > value.scanned_commit_count)) {
    throw new APIRequestError("Repository file history contains inconsistent bounded facts",
      "INVALID_RESPONSE", 502);
  }
  return { ...value, entries } as unknown as RepositoryFileHistoryView;
}

export function parseRepositoryCommitDetail(value: unknown, workspaceID: string,
  objectID: string): RepositoryCommitDetailView {
  const keys = ["author_identity_included", "available", "changed_file_count", "changes",
    "checkout_performed", "commit_body_included", "committed_at", "file_content_included",
    "first_parent_only", "hash", "hooks_executed", "kind", "network_used", "object_id",
    "omitted_change_count", "parent_count", "patch_included", "process_started",
    "protocol_version", "read_only", "redaction_count", "reference_updated",
    "remote_config_included", "returned_change_count", "root_path_exposed", "subject",
    "truncated", "workspace_id"];
  if (!hasExactKeys(value, keys) || value.protocol_version !== "repository_commit_detail.v1" ||
    value.workspace_id !== workspaceID || value.object_id !== objectID ||
    !/^[0-9a-f]{40}$/u.test(String(value.object_id)) ||
    !["none", "git"].includes(String(value.kind)) ||
    typeof value.available !== "boolean" || value.available !== (value.kind === "git") ||
    value.first_parent_only !== true || value.read_only !== true ||
    value.root_path_exposed !== false || value.author_identity_included !== false ||
    value.commit_body_included !== false || value.file_content_included !== false ||
    value.patch_included !== false || value.remote_config_included !== false ||
    value.checkout_performed !== false || value.reference_updated !== false ||
    value.process_started !== false || value.network_used !== false ||
    value.hooks_executed !== false || typeof value.truncated !== "boolean" ||
    !Array.isArray(value.changes) || value.changes.length > 200 ||
    !safeBoundedCount(value.changed_file_count, 40_000) ||
    !safeBoundedCount(value.returned_change_count, 200) ||
    value.returned_change_count !== value.changes.length ||
    !safeBoundedCount(value.omitted_change_count, 40_000) ||
    value.changed_file_count !== value.returned_change_count + value.omitted_change_count ||
    !safeBoundedCount(value.redaction_count, 40_000) ||
    !safeBoundedCount(value.parent_count, 1024)) {
    throw new APIRequestError("Repository commit detail violated its exact read-only contract",
      "INVALID_RESPONSE", 502);
  }
  const paths = new Set<string>();
  const kinds = ["", "regular", "executable", "symlink", "submodule"];
  const changes = value.changes.map((change) => {
    if (!hasExactKeys(change, ["change", "content_changed", "current_kind", "mode_changed",
      "path", "previous_kind"]) || !validWorkspaceRelativePath(change.path) ||
      change.path === "." || paths.has(String(change.path)) ||
      !["added", "modified", "deleted"].includes(String(change.change)) ||
      !kinds.includes(String(change.previous_kind)) || !kinds.includes(String(change.current_kind)) ||
      typeof change.content_changed !== "boolean" || typeof change.mode_changed !== "boolean" ||
      (!change.content_changed && !change.mode_changed) ||
      (change.change === "added" && (change.previous_kind !== "" || change.current_kind === "")) ||
      (change.change === "deleted" && (change.previous_kind === "" || change.current_kind !== "")) ||
      (change.change === "modified" &&
        (change.previous_kind === "" || change.current_kind === ""))) {
      throw new APIRequestError("Repository commit file metadata widened content authority",
        "INVALID_RESPONSE", 502);
    }
    paths.add(String(change.path));
    return change;
  });
  if ((!value.available && (value.hash !== "" || value.subject !== "" ||
    value.committed_at !== "0001-01-01T00:00:00Z" || value.parent_count !== 0 ||
    value.changed_file_count !== 0 || value.omitted_change_count !== 0 ||
    value.redaction_count !== 0 || value.truncated)) ||
    (value.available && (typeof value.hash !== "string" || !/^[0-9a-f]{12}$/u.test(value.hash) ||
      !objectID.startsWith(value.hash) || typeof value.subject !== "string" ||
      value.subject.length === 0 || [...value.subject].length > 512 ||
      /[\u0000-\u001f\u007f]/u.test(value.subject) || !validDate(value.committed_at))) ||
    (value.omitted_change_count > 0 && !value.truncated)) {
    throw new APIRequestError("Repository commit detail contains inconsistent bounded facts",
      "INVALID_RESPONSE", 502);
  }
  return { ...value, changes } as unknown as RepositoryCommitDetailView;
}

export function parseRepositoryCommitComparison(value: unknown, workspaceID: string,
  baseObjectID: string, headObjectID: string): RepositoryCommitComparisonView {
  const keys = ["ancestor_required", "author_identity_included", "authority_granted",
    "available", "base_committed_at", "base_hash", "base_object_id", "base_redacted",
    "base_subject", "base_subject_bounded", "changed_file_count", "changes",
    "checkout_performed", "commit_body_included", "file_content_included", "head_committed_at",
    "head_hash", "head_object_id", "head_redacted", "head_subject", "head_subject_bounded",
    "hooks_executed", "kind", "metadata_only", "network_used", "omitted_change_count",
    "patch_included", "process_started", "protocol_version", "read_only", "redaction_count",
    "reference_updated", "remote_config_included", "rename_inferred", "returned_change_count",
    "root_path_exposed", "same_object", "truncated", "workspace_id"];
  if (!hasExactKeys(value, keys) ||
    value.protocol_version !== "repository_commit_comparison.v1" ||
    value.workspace_id !== workspaceID || value.base_object_id !== baseObjectID ||
    value.head_object_id !== headObjectID || !/^[0-9a-f]{40}$/u.test(baseObjectID) ||
    !/^[0-9a-f]{40}$/u.test(headObjectID) || !["none", "git"].includes(String(value.kind)) ||
    typeof value.available !== "boolean" || value.available !== (value.kind === "git") ||
    value.same_object !== (baseObjectID === headObjectID) || value.metadata_only !== true ||
    value.read_only !== true || value.rename_inferred !== false ||
    value.ancestor_required !== false || value.authority_granted !== false ||
    value.root_path_exposed !== false || value.author_identity_included !== false ||
    value.commit_body_included !== false || value.file_content_included !== false ||
    value.patch_included !== false || value.remote_config_included !== false ||
    value.checkout_performed !== false || value.reference_updated !== false ||
    value.process_started !== false || value.network_used !== false ||
    value.hooks_executed !== false || typeof value.truncated !== "boolean" ||
    typeof value.base_redacted !== "boolean" || typeof value.base_subject_bounded !== "boolean" ||
    typeof value.head_redacted !== "boolean" || typeof value.head_subject_bounded !== "boolean" ||
    !Array.isArray(value.changes) || value.changes.length > 200 ||
    !safeBoundedCount(value.changed_file_count, 40_000) ||
    !safeBoundedCount(value.returned_change_count, 200) ||
    value.returned_change_count !== value.changes.length ||
    !safeBoundedCount(value.omitted_change_count, 40_000) ||
    value.changed_file_count !== value.returned_change_count + value.omitted_change_count ||
    !safeBoundedCount(value.redaction_count, 40_000)) {
    throw new APIRequestError("Repository commit comparison violated its metadata contract",
      "INVALID_RESPONSE", 502);
  }
  const paths = new Set<string>();
  const kinds = ["", "regular", "executable", "symlink", "submodule"];
  const changes = value.changes.map((change) => {
    if (!hasExactKeys(change, ["change", "content_changed", "current_kind", "mode_changed",
      "path", "previous_kind"]) || !validWorkspaceRelativePath(change.path) ||
      change.path === "." || paths.has(String(change.path)) ||
      !["added", "modified", "deleted"].includes(String(change.change)) ||
      !kinds.includes(String(change.previous_kind)) || !kinds.includes(String(change.current_kind)) ||
      typeof change.content_changed !== "boolean" || typeof change.mode_changed !== "boolean" ||
      (!change.content_changed && !change.mode_changed) ||
      (change.change === "added" && (change.previous_kind !== "" || change.current_kind === "")) ||
      (change.change === "deleted" && (change.previous_kind === "" || change.current_kind !== "")) ||
      (change.change === "modified" &&
        (change.previous_kind === "" || change.current_kind === ""))) {
      throw new APIRequestError("Repository comparison file metadata widened content authority",
        "INVALID_RESPONSE", 502);
    }
    paths.add(String(change.path));
    return change;
  });
  const redactedSubjects = Number(value.base_redacted) + Number(value.head_redacted);
  if ((!value.available && (value.base_hash !== "" || value.head_hash !== "" ||
      value.base_subject !== "" || value.head_subject !== "" ||
      value.base_committed_at !== "0001-01-01T00:00:00Z" ||
      value.head_committed_at !== "0001-01-01T00:00:00Z" || value.changed_file_count !== 0 ||
      value.redaction_count !== 0 || value.truncated)) ||
    (value.available && (typeof value.base_hash !== "string" ||
      !/^[0-9a-f]{12}$/u.test(value.base_hash) || !baseObjectID.startsWith(value.base_hash) ||
      typeof value.head_hash !== "string" || !/^[0-9a-f]{12}$/u.test(value.head_hash) ||
      !headObjectID.startsWith(value.head_hash) || typeof value.base_subject !== "string" ||
      value.base_subject.length === 0 || [...value.base_subject].length > 512 ||
      /[\u0000-\u001f\u007f]/u.test(value.base_subject) ||
      typeof value.head_subject !== "string" || value.head_subject.length === 0 ||
      [...value.head_subject].length > 512 || /[\u0000-\u001f\u007f]/u.test(value.head_subject) ||
      !validDate(value.base_committed_at) || !validDate(value.head_committed_at))) ||
    redactedSubjects > value.redaction_count ||
    ((value.base_subject_bounded || value.head_subject_bounded ||
      value.omitted_change_count > 0) && !value.truncated) ||
    (value.same_object && (value.changed_file_count !== 0 || changes.length !== 0))) {
    throw new APIRequestError("Repository comparison contains inconsistent exact facts",
      "INVALID_RESPONSE", 502);
  }
  return { ...value, changes } as unknown as RepositoryCommitComparisonView;
}

export async function parseRepositoryCommitFilePreview(value: unknown, workspaceID: string,
  objectID: string, path: string): Promise<RepositoryCommitFilePreviewView> {
  const keys = ["authority_granted", "checkout_performed", "content", "hash",
    "hooks_executed", "kind", "mutation_supported", "network_used", "object_id", "path",
    "process_started", "protocol_version", "provenance", "raw_blob_included", "read_only",
    "redacted", "redacted_content_included", "redaction_count", "reference_updated",
    "remote_config_included", "returned_bytes", "root_path_exposed", "total_bytes",
    "workspace_id"];
  if (!hasExactKeys(value, keys) ||
    value.protocol_version !== "repository_commit_file_preview.v1" ||
    value.workspace_id !== workspaceID || value.object_id !== objectID || value.path !== path ||
    !/^[0-9a-f]{40}$/u.test(String(value.object_id)) ||
    typeof value.hash !== "string" || !/^[0-9a-f]{12}$/u.test(value.hash) ||
    !objectID.startsWith(value.hash) || !validWorkspaceRelativePath(value.path) ||
    value.path === "." || !["regular", "executable"].includes(String(value.kind)) ||
    typeof value.content !== "string" ||
    /[\u0000-\u0008\u000b\u000c\u000e-\u001f\u007f]/u.test(value.content) ||
    !safeBoundedCount(value.total_bytes, 64 * 1024) ||
    !safeBoundedCount(value.returned_bytes, 128 * 1024) ||
    !safeBoundedCount(value.redaction_count, 64 * 1024) ||
    typeof value.redacted !== "boolean" || value.redacted !== (value.redaction_count > 0) ||
    value.read_only !== true || value.mutation_supported !== false ||
    value.authority_granted !== false || value.root_path_exposed !== false ||
    value.raw_blob_included !== false || value.redacted_content_included !== true ||
    value.remote_config_included !== false || value.checkout_performed !== false ||
    value.reference_updated !== false || value.process_started !== false ||
    value.network_used !== false || value.hooks_executed !== false ||
    !hasExactKeys(value.provenance, ["content_sha256", "instruction_authorized", "source_kind",
      "source_ref", "version"]) || value.provenance.version !== "context_provenance.v1" ||
    value.provenance.source_kind !== "repository_commit_file" ||
    value.provenance.source_ref !== path || !isSHA256(value.provenance.content_sha256) ||
    value.provenance.instruction_authorized !== false) {
    throw new APIRequestError("Repository commit preview violated its redacted evidence contract",
      "INVALID_RESPONSE", 502);
  }
  const encoded = new TextEncoder().encode(value.content);
  if (encoded.length !== value.returned_bytes) {
    throw new APIRequestError("Repository commit preview byte count is inconsistent",
      "INVALID_RESPONSE", 502);
  }
  const digest = new Uint8Array(await globalThis.crypto.subtle.digest("SHA-256", encoded));
  const digestHex = [...digest].map((byte) => byte.toString(16).padStart(2, "0")).join("");
  if (digestHex !== value.provenance.content_sha256) {
    throw new APIRequestError("Repository commit preview digest verification failed",
      "INVALID_RESPONSE", 502);
  }
  return value as unknown as RepositoryCommitFilePreviewView;
}
