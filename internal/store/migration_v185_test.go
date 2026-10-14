package store

import (
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"cyberagent-workbench/internal/application"
	"cyberagent-workbench/internal/commandruntimeadapter"
	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/runner"
)

func TestSchemaV185PreservesRealV184CommandJobs(t *testing.T) {
	// Includes the v131 command-history repair; released v131 compatibility is tested separately.
	if got := migrationPlanDigest(migrationPlan()[:184]); got != "2a1d90c2bca6af524cf86646cc415b38b209b4bc3ec8a184fdfe0df61b58fc2d" {
		t.Fatalf("published v1-v184 prefix changed: %s", got)
	}
	ctx := t.Context()
	path := filepath.Join(t.TempDir(), "actual-v184.db")
	st := openHistoricalTestDatabase(t, path, 177)
	job := commandRuntimeMigrationJob(t, st, domain.RunExecutionPermissionFullAccess, commandruntimeadapter.HostUnsandboxed(strings.Repeat("a", 64)))
	insertV162CommandRuntimeJob(t, st, job, 73)
	oldIntents := seedV177FixedCommandHistory(t, st, job)
	for _, migration := range migrationPlan()[177:184] {
		if err := st.applyMigration(ctx, migration); err != nil {
			t.Fatal(err)
		}
	}
	before, err := st.GetCommandRuntimeJob(ctx, job.ID)
	if err != nil {
		t.Fatal(err)
	}
	actors := legacyFixtureRows(t, st, "command_runtime_job_agents")
	permissions := legacyFixtureRows(t, st, "run_execution_permission_snapshots")
	oldReceipts := legacyFixtureRows(t, st, "controlled_command_execution_receipts")
	ledger, err := st.loadAppliedMigrations(ctx)
	if err != nil || len(ledger) != 184 {
		t.Fatal("not a real v184 input", err)
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	st, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	after, err := st.GetCommandRuntimeJob(ctx, job.ID)
	if err != nil || !reflect.DeepEqual(before, after) {
		t.Fatal("historical Job changed", err)
	}
	var rowid, operator int
	if err := st.db.QueryRowContext(ctx, `SELECT rowid,operator_invocation FROM command_runtime_jobs WHERE id=?`, job.ID).Scan(&rowid, &operator); err != nil || rowid != 73 || operator != 0 {
		t.Fatal("old Job rowid or default provenance changed", rowid, operator, err)
	}
	if !reflect.DeepEqual(actors, legacyFixtureRows(t, st, "command_runtime_job_agents")) || !reflect.DeepEqual(permissions, legacyFixtureRows(t, st, "run_execution_permission_snapshots")) {
		t.Fatal("historical actors or permissions changed")
	}
	if !reflect.DeepEqual(oldReceipts, legacyFixtureRows(t, st, "controlled_command_execution_receipts")) {
		t.Fatal("old fixed command receipt changed")
	}
	for index, intent := range oldIntents {
		got, found, err := st.GetControlledExecutionIntentByPlanID(ctx, intent.PlanID)
		if err != nil || !found || got != intent {
			t.Fatal("old intent read changed", got, found, err)
		}
		_, found, err = st.GetControlledExecutionReceipt(ctx, intent.RequestID)
		if err != nil || found != (index == 0) {
			t.Fatal("unknown old intent acquired a receipt", found, err)
		}
	}
	current, err := st.loadAppliedMigrations(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for version, entry := range ledger {
		if current[version] != entry {
			t.Fatalf("migration %d changed", version)
		}
	}
	if _, err := st.db.ExecContext(ctx, `UPDATE command_runtime_jobs SET operator_invocation=1 WHERE id=?`, job.ID); err == nil {
		t.Fatal("old Job source changed")
	}
	// Actor input alone cannot produce process-local preparation provenance.
	forged := after
	forged.ID = "forged-operator-job"
	forged.OperationDigest = strings.Repeat("b", 64)
	if _, _, err := st.PrepareCommandRuntimeJobForAgent(ctx, forged, domain.AgentAttribution{AgentID: job.RootAgentID, Source: domain.AgentAttributionOperatorRoot}); err == nil || !strings.Contains(err.Error(), "live host provenance") {
		t.Fatal("public actor input forged operator invocation", err)
	}
	if _, err := st.db.ExecContext(ctx, `INSERT INTO command_runtime_job_agents (job_id,run_id,agent_id,agent_attempt_id,attribution_source,created_at) VALUES (?,?,?,NULL,'operator_root',?)`, job.ID, job.RunID, job.RootAgentID, ts(job.CreatedAt)); err == nil || !strings.Contains(err.Error(), "invocation source differs") {
		t.Fatal("actor source disagreed with stored provenance", err)
	}
	assertNoForeignKeyViolations(t, st.db)
	assertLatestMigrationLedger(t, st, migrationPlan())
	t.Log("immutable v1-v184 migration digest", migrationPlanDigest(migrationPlan()[:184]))
}

// Seed the original v177 tables before the real upgrade. No current permission
// is relabelled and no new execution is inferred for the unknown second intent.
func seedV177FixedCommandHistory(t *testing.T, st *SQLiteStore, job runner.CommandRuntimeJob) []runner.ControlledExecutionIntent {
	t.Helper()
	ctx := t.Context()
	if v, err := st.SchemaVersion(ctx); err != nil || v != 177 {
		t.Fatalf("historical fixed seed requires schema177: %d %v", v, err)
	}
	lease, found, err := st.GetRunExecutionLease(ctx, job.RunID)
	if err != nil || !found {
		t.Fatal("historical lease", err)
	}
	if _, _, err := st.ReleaseRunExecutionLease(ctx, lease); err != nil {
		t.Fatal(err)
	}
	if _, err := application.NewRunService(st).Pause(ctx, job.RunID); err != nil {
		t.Fatal(err)
	}
	interaction, err := application.NewRunExecutionInteractionService(st).Change(ctx, application.ChangeRunExecutionInteractionRequest{RunID: job.RunID, Mode: "controlled", Trust: "trusted", ConfirmWorkspaceTrust: true, OperationKey: "historical-controlled-interaction", RequestedBy: "operator"})
	if err != nil {
		t.Fatal(err)
	}
	profile, err := st.GetRunExecutionProfile(ctx, job.RunID)
	if err != nil {
		t.Fatal(err)
	}
	workspace, err := st.GetWorkspaceByID(ctx, job.WorkspaceID)
	if err != nil {
		t.Fatal(err)
	}
	intents := []runner.ControlledExecutionIntent{}
	for _, id := range []string{"historical-fixed-complete", "historical-fixed-unknown"} {
		plan, err := runner.PlanControlledCommand(runner.ControlledCommandPlanRequest{ID: id, WorkspaceID: workspace.ID, WorkspaceRoot: workspace.RootPath,
			Interaction: interaction.Interaction, CurrentProfile: profile, CurrentSurface: domain.ExecutionSurfaceCode, Kind: runner.ControlledCommandGoVersion})
		if err != nil {
			t.Fatal(err)
		}
		intent := runner.ControlledExecutionIntent{
			ProtocolVersion: runner.ControlledExecutionIntentProtocolVersion, PolicyVersion: runner.ControlledExecutionPolicyVersion,
			RequestID: runner.ControlledExecutionRequestID(plan), PlanID: plan.ID, PlanFingerprint: plan.Fingerprint,
			RunID: plan.RunID, WorkspaceID: plan.WorkspaceID, InteractionSnapshotID: plan.InteractionSnapshotID,
			InteractionRevision: plan.InteractionRevision, ExecutionProfileRevision: plan.ExecutionProfileRevision,
			Kind: plan.Kind, RequestedBy: "operator", CreatedAt: time.Now().UTC(),
		}
		if err := intent.Validate(); err != nil {
			t.Fatal(err)
		}
		if _, err := st.db.ExecContext(ctx, `INSERT INTO
		controlled_command_execution_intents
		(request_id, protocol_version, policy_version, plan_id, plan_fingerprint,
		run_id, workspace_id, interaction_snapshot_id, interaction_revision,
		execution_profile_revision, kind, requested_by, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			intent.RequestID, intent.ProtocolVersion, intent.PolicyVersion,
			intent.PlanID, intent.PlanFingerprint, intent.RunID, intent.WorkspaceID,
			intent.InteractionSnapshotID, intent.InteractionRevision,
			intent.ExecutionProfileRevision, intent.Kind, intent.RequestedBy,
			ts(intent.CreatedAt)); err != nil {
			t.Fatal(err)
		}
		intents = append(intents, intent)
		if id == "historical-fixed-unknown" {
			continue
		}
		receipt := runner.ControlledExecutionReceipt{
			RequestID: intent.RequestID, ProtocolVersion: runner.ControlledExecutionProtocolVersion,
			PolicyVersion: runner.ControlledExecutionPolicyVersion, Backend: "historical-fixture",
			StdoutPrefixSHA256: testCommandRuntimeDigest(""), StderrPrefixSHA256: testCommandRuntimeDigest(""),
			StartedAt: intent.CreatedAt, CompletedAt: intent.CreatedAt.Add(time.Second), TreeReaped: true,
			RestrictedToken: true, LowIntegrityToken: true, JobAssignedAtCreation: true, KillOnJobClose: true,
			ActiveProcessLimit: 1, ProcessMemoryLimit: runner.MaxControlledProcessMemoryBytes,
			StdinClosed: true, ProductExecutionEnabled: true,
		}
		if err := receipt.Validate(); err != nil {
			t.Fatal(err)
		}
		if _, err := st.db.ExecContext(ctx, `INSERT INTO
		controlled_command_execution_receipts
		(request_id, protocol_version, policy_version, backend, exit_code,
		stdout_observed_bytes, stdout_captured_bytes, stdout_prefix_sha256,
		stdout_truncated, stderr_observed_bytes, stderr_captured_bytes,
		stderr_prefix_sha256, stderr_truncated, started_at, completed_at,
		timed_out, cancelled, output_limit_exceeded, tree_reaped,
		restricted_token, low_integrity_token, job_assigned_at_creation,
		kill_on_job_close, active_process_limit, process_memory_limit,
		stdin_closed, environment_inherited, network_requested,
		persistent_process, product_execution_enabled)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?,
			?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			receipt.RequestID, receipt.ProtocolVersion, receipt.PolicyVersion,
			receipt.Backend, receipt.ExitCode, receipt.StdoutObservedBytes,
			receipt.StdoutCapturedBytes, receipt.StdoutPrefixSHA256,
			receipt.StdoutTruncated, receipt.StderrObservedBytes,
			receipt.StderrCapturedBytes, receipt.StderrPrefixSHA256,
			receipt.StderrTruncated, ts(receipt.StartedAt), ts(receipt.CompletedAt),
			receipt.TimedOut, receipt.Cancelled, receipt.OutputLimitExceeded,
			receipt.TreeReaped, receipt.RestrictedToken,
			receipt.LowIntegrityToken, receipt.JobAssignedAtCreation,
			receipt.KillOnJobClose, receipt.ActiveProcessLimit,
			receipt.ProcessMemoryLimit, receipt.StdinClosed,
			receipt.EnvironmentInherited, receipt.NetworkRequested,
			receipt.PersistentProcess, receipt.ProductExecutionEnabled); err != nil {
			t.Fatal(err)
		}
	}
	return intents
}
