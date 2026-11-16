import {
  gitAdvancedOperations,
  isGitAdvancedProjection,
  validGitAdvancedSpec,
} from "./git-advanced";

const sha = "a".repeat(64);
const oid = "1".repeat(40);
const now = "2026-08-20T10:00:00Z";

function projection() {
  return {
    protocol_version: "git-advanced-api.v1", run_id: "run-1", workspace_id: "workspace-1",
    authority: { protocol_version: "git-advanced-authority.v1", executable: true,
      lease_active: true, lease_expires_at: "2026-08-20T11:00:00Z",
      permission_revision: 1, permission_snapshot_id: "permission-1",
      scope: { capability_generation: sha, lease_generation: 2 } },
    capability: { protocol_version: "git-advanced-capability.v1", enabled: true,
      generation: sha, managed_root_sha256: sha, max_commits: 128, max_hunks: 200,
      max_paths: 200, operations: [...gitAdvancedOperations], captured_at: now },
    binding: { protocol_version: "git-advanced.v1", repository_sha256: sha,
      common_dir_sha256: sha, head: oid, branch: "main", index_sha256: sha,
      worktree_sha256: sha, status_sha256: sha, stash_sha256: sha,
      sequence_sha256: sha, detached: false, object_format: "sha1", captured_at: now },
    conflict: { active: false, can_abort: false, can_continue: false, can_skip: false,
      files: [] },
    stashes: [], worktrees: [], operations: [],
  };
}

function operationView() {
  const base = projection();
  const preview = {
    protocol_version: "git-advanced-preview.v1", id: "preview-1", operation: "stash_create",
    spec: { protocol_version: "git-advanced.v1", operation: "stash_create",
      message: "audited stash" },
    binding: base.binding, capability: base.capability, hunks: [], files: [],
    conflict: base.conflict,
    recovery: { required: true, incomplete_reasons: [],
      restore_action: "workspace_checkpoint_rewind" },
    summary: "stash one exact change", blocked_reasons: [], approval_fingerprint: sha,
    permission_snapshot_id: "permission-1", permission_revision: 1, lease_generation: 2,
    created_at: now,
  };
  return { id: "operation-1", protocol_version: "git-advanced.v1",
    operation_key_sha256: sha, request_fingerprint: sha, preview_id: preview.id,
    approval_fingerprint: sha, operation: "stash_create", preview,
    repository_sha256: sha, common_dir_sha256: sha,
    permission_snapshot_id: "permission-1", permission_revision: 1,
    capability_generation: sha, lease_generation: 2, status: "proposed", created_at: now };
}

describe("Git advanced browser contract", () => {
  it("accepts only the complete closed operation capability", () => {
    expect(isGitAdvancedProjection(projection(), "run-1")).toBe(true);
    const incomplete = projection();
    incomplete.capability.operations = ["hunk_stage"] as typeof incomplete.capability.operations;
    expect(isGitAdvancedProjection(incomplete, "run-1")).toBe(false);
  });

  it("rejects internal lease identities and raw managed host paths", () => {
    const leakedLease = projection() as Record<string, unknown>;
    (leakedLease.authority as Record<string, unknown>).scope = {
      capability_generation: sha, lease_generation: 2, lease_id: "secret-lease",
    };
    expect(isGitAdvancedProjection(leakedLease, "run-1")).toBe(false);

    const leakedPath = projection();
    leakedPath.worktrees = [{ id: "worktree-1", protocol_version: "git-managed-worktree.v1",
      run_id: "run-1", workspace_id: "workspace-1", repository_sha256: sha,
      common_dir_sha256: sha, name: "review", path_sha256: sha, branch: "review/branch",
      head: oid, locked: false, present: true, generation: 1,
      created_operation_id: "operation-1", last_operation_id: "operation-1",
      created_at: now, updated_at: now, path: "C:\\private" }] as typeof leakedPath.worktrees;
    expect(isGitAdvancedProjection(leakedPath, "run-1")).toBe(false);
  });

  it("accepts parsed audit operations and typed failures without a post-state", () => {
    const withOperation = projection();
    const operation = operationView();
    withOperation.operations = [operation] as typeof withOperation.operations;
    expect(isGitAdvancedProjection(withOperation, "run-1")).toBe(true);

    operation.status = "failed";
    Object.assign(operation, { approval_id: "approval-1", started_at: now, completed_at: now,
      error_code: "interrupted", receipt: {
        protocol_version: "git-advanced-receipt.v1", id: "receipt-1", preview_id: "preview-1",
        operation: "stash_create", status: "failed", pre_binding: withOperation.binding,
        post_binding: { protocol_version: "", repository_sha256: "", common_dir_sha256: "",
          head: "", branch: "", index_sha256: "", worktree_sha256: "", status_sha256: "",
          stash_sha256: "", sequence_sha256: "", detached: false, object_format: "",
          captured_at: "0001-01-01T00:00:00Z" },
        conflict: withOperation.conflict, observed_bytes: 0, error_code: "interrupted",
        error_summary: "operation stopped before post-state capture", started_at: now,
        completed_at: now,
      } });
    expect(isGitAdvancedProjection(withOperation, "run-1")).toBe(true);
  });

  it("rejects traversal, raw argv fields, unknown operations, and unbounded recipes", () => {
    expect(validGitAdvancedSpec({ protocol_version: "git-advanced.v1", operation: "hunk_stage",
      paths: ["../secret"] })).toBe(false);
    expect(validGitAdvancedSpec({ protocol_version: "git-advanced.v1", operation: "hunk_stage",
      argv: ["reset", "--hard"] })).toBe(false);
    expect(validGitAdvancedSpec({ protocol_version: "git-advanced.v1", operation: "force_push" }))
      .toBe(false);
    expect(validGitAdvancedSpec({ protocol_version: "git-advanced.v1", operation: "bisect_run",
      sequence_id: "sequence-1", expected_current: oid,
      recipe: { name: "go_test", max_steps: 129, timeout_seconds: 120 } })).toBe(false);
    expect(validGitAdvancedSpec({ protocol_version: "git-advanced.v1", operation: "hunk_stage",
      paths: ["folder/.Git/config"] })).toBe(false);
    expect(validGitAdvancedSpec({ protocol_version: "git-advanced.v1", operation: "hunk_stage",
      paths: ["folder/ .git. /config"] })).toBe(false);
    expect(validGitAdvancedSpec({ protocol_version: "git-advanced.v1", operation: "stash_drop",
      stash_oid: oid, restore_index: true })).toBe(false);
    expect(validGitAdvancedSpec({ protocol_version: "git-advanced.v1", operation: "worktree_create",
      worktree_name: "Review", branch: "review/topic", commit: oid })).toBe(false);
  });

  it("keeps spaced Git paths distinct in input and evidence", () => {
    const paths = ["notes.txt", " notes.txt", "notes.txt ", "dir / file,name.txt "];
    expect(validGitAdvancedSpec({ protocol_version: "git-advanced.v1", operation: "hunk_stage", paths }))
      .toBe(true);
    const withPaths = projection();
    withPaths.stashes = [{ oid, base_commit: oid, index_commit: oid, subject: "Fixture",
      files: paths.map((path) => ({ path, change: "index", destructive: false })) }] as typeof withPaths.stashes;
    expect(isGitAdvancedProjection(withPaths, "run-1")).toBe(true);
  });
});
