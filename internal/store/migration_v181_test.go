package store

import (
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"cyberagent-workbench/internal/domain"
)

// This starts with a genuine v177 database, writes historical v1 permission
// under its original constraints, upgrades to v180 and records a native Docker
// admission with lifecycle children. It never removes a ledger row or trigger.
func TestSchemaV181PreservesDockerAdmissionRowsChildrenAndRecovery(t *testing.T) {
	ctx := t.Context()
	path, root := filepath.Join(t.TempDir(), "docker-v180.db"), t.TempDir()
	state := openHistoricalTestDatabase(t, path, 177)
	if err := state.SaveWorkspace(ctx, WorkspaceRecord{ID: "ws-docker-history", Name: "docker-history", RootPath: root}); err != nil {
		t.Fatal(err)
	}
	run := seedLegacyStructuredToolRun(t, state, "ws-docker-history", domain.ExecutionPhaseDeliver, domain.RunExecutionPermissionApproval)
	for _, step := range migrationPlan()[177:180] {
		if err := state.applyMigration(ctx, step); err != nil {
			t.Fatal(err)
		}
	}
	permission, err := state.GetRunExecutionPermission(ctx, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	restoreLegacyContext := addEmptyCurrentContextForLegacySeed(t, state)
	fixture := newDockerSandboxStoreFixture(t, ctx, state, run, root, "v180-docker", permission)
	// A non-default rowid catches a rebuild that silently renumbers rows.
	// Insert the frozen v180 columns under the real old constraints/triggers.
	insertV180DockerAdmission(t, state, fixture.Admission, 73)
	if _, replayed, err := state.CreateDockerSandboxAdmission(ctx, fixture.Admission); err != nil || !replayed {
		t.Fatalf("old native admission read/replay failed: %t %v", replayed, err)
	}
	beginDockerSandboxStoreStart(t, ctx, state, fixture)
	if _, _, err := state.BeginDockerContainerLifecycle(ctx, fixture.Intent, "historical-owner", time.Minute); err != nil {
		t.Fatal(err)
	}
	launch := newDockerSandboxStoreLaunch(t, fixture)
	if _, _, err := state.BindDockerSandboxLaunch(ctx, launch); err != nil {
		t.Fatal(err)
	}
	restoreLegacyContext()
	tables := []string{"sandbox_docker_product_admissions", "sandbox_docker_product_start_requests", "sandbox_docker_product_launches", "sandbox_docker_lifecycle_intents", "sandbox_docker_lifecycle_transitions", "run_execution_permission_snapshots"}
	before := make(map[string][][]any)
	for _, table := range tables {
		before[table] = dockerMigrationRows(t, state, table)
	}
	var originalRowID int64
	if err := state.db.QueryRowContext(ctx, `SELECT rowid FROM sandbox_docker_product_admissions WHERE id=?`, fixture.Admission.ID).Scan(&originalRowID); err != nil {
		t.Fatal(err)
	}
	ledger, err := state.loadAppliedMigrations(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := state.Close(); err != nil {
		t.Fatal(err)
	}
	state, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer state.Close()
	for _, table := range tables {
		if !reflect.DeepEqual(before[table], dockerMigrationRows(t, state, table)) {
			t.Fatalf("historical rows changed in %s", table)
		}
	}
	var rowID int64
	if err := state.db.QueryRowContext(ctx, `SELECT rowid FROM sandbox_docker_product_admissions WHERE id=?`, fixture.Admission.ID).Scan(&rowID); err != nil || rowID != originalRowID {
		t.Fatalf("historical rowid changed: %d -> %d, %v", originalRowID, rowID, err)
	}
	currentLedger, err := state.loadAppliedMigrations(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for version, record := range ledger {
		if currentLedger[version] != record {
			t.Fatalf("historical migration %d changed", version)
		}
	}
	recoverable, err := state.ListRecoverableDockerSandboxes(ctx, 10)
	if err != nil || len(recoverable) != 1 || recoverable[0].Admission.ID != fixture.Admission.ID {
		t.Fatalf("lost historical recovery: %#v, %v", recoverable, err)
	}
	if _, replayed, err := state.BindDockerSandboxLaunch(ctx, launch); err != nil || !replayed {
		t.Fatalf("historical launch replay changed: %t, %v", replayed, err)
	}
	for _, query := range []string{`UPDATE sandbox_docker_product_admissions SET permission_mode='full' WHERE id=?`, `DELETE FROM sandbox_docker_product_admissions WHERE id=?`} {
		if _, err := state.db.ExecContext(ctx, query, fixture.Admission.ID); err == nil {
			t.Fatal("historical admission became mutable", query)
		}
	}
	assertNoForeignKeyViolations(t, state.db)
	assertLatestMigrationLedger(t, state, migrationPlan())
}

// Several immutable lifecycle children are WITHOUT ROWID; compare their actual
// columns in key order. The rebuilt admission's original rowid is checked above.
func dockerMigrationRows(t testing.TB, state *SQLiteStore, table string) [][]any {
	t.Helper()
	rows, err := state.db.QueryContext(t.Context(), "SELECT * FROM "+table+" ORDER BY 1,2")
	if err != nil {
		t.Fatal(table, err)
	}
	defer rows.Close()
	columns, err := rows.Columns()
	if err != nil {
		t.Fatal(err)
	}
	var result [][]any
	for rows.Next() {
		values, pointers := make([]any, len(columns)), make([]any, len(columns))
		for i := range values {
			pointers[i] = &values[i]
		}
		if err := rows.Scan(pointers...); err != nil {
			t.Fatal(err)
		}
		result = append(result, values)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return result
}

// Frozen v180 native admission writer columns, with an explicit historical rowid.
func insertV180DockerAdmission(t testing.TB, state *SQLiteStore, value domain.DockerSandboxAdmission, rowID int64) {
	t.Helper()
	_, err := state.db.ExecContext(t.Context(), `INSERT INTO sandbox_docker_product_admissions
		(rowid, id, protocol_version, operation_key_digest, request_fingerprint,
		lifecycle_operation_digest, run_id, mission_id, workspace_id, plan_id,
		candidate_id, preparation_id, manifest_json, manifest_fingerprint,
		plan_fingerprint, spec_fingerprint, authority_fingerprint,
		readiness_fingerprint, readiness_expires_at, runtime_epoch_fingerprint,
		profile_snapshot_id, profile_revision, permission_snapshot_id,
		permission_revision, permission_mode, approval_id, approval_version,
		policy_fingerprint, network_mode, network_target_count, cpu_quota_millis,
		memory_bytes, pids, disk_bytes, wall_clock_seconds, log_bytes, log_lines,
		tool_calls_remaining, decision, reason_code, remediation_code,
		product_entry_enabled, execution_authorized, artifact_commit_authorized,
		requested_by, created_at, admission_fingerprint)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?,
			?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		rowID, value.ID, value.ProtocolVersion, value.OperationKeyDigest,
		value.RequestFingerprint, value.LifecycleOperationDigest, value.RunID,
		value.MissionID, value.WorkspaceID, value.PlanID, value.CandidateID,
		value.PreparationID, value.ManifestJSON, value.ManifestFingerprint,
		value.PlanFingerprint, value.SpecFingerprint, value.AuthorityFingerprint,
		value.ReadinessFingerprint, ts(value.ReadinessExpiresAt),
		value.RuntimeEpochFingerprint, value.ProfileSnapshotID, value.ProfileRevision,
		value.PermissionSnapshotID, value.PermissionRevision, value.PermissionMode,
		value.ApprovalID, value.ApprovalVersion, value.PolicyFingerprint,
		value.NetworkMode, value.NetworkTargetCount, value.CPUQuotaMillis,
		value.MemoryBytes, value.PIDs, value.DiskBytes, value.WallClockSeconds,
		value.LogBytes, value.LogLines, value.ToolCallsRemaining, value.Decision,
		value.ReasonCode, value.RemediationCode, boolInt(value.ProductEntryEnabled),
		boolInt(value.ExecutionAuthorized), boolInt(value.ArtifactCommitAuthorized),
		value.RequestedBy, ts(value.CreatedAt), value.AdmissionFingerprint)
	if err != nil {
		t.Fatal(err)
	}
}
