package store

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"cyberagent-workbench/internal/apperror"
	"cyberagent-workbench/internal/application"
	"cyberagent-workbench/internal/events"
	"cyberagent-workbench/internal/runmutation"
	"cyberagent-workbench/internal/sandbox"
)

func TestSandboxExecutionLeaseFencesTakeoverAndLifecycleRowsAreImmutable(t *testing.T) {
	ctx := context.Background()
	st, run, _ := openSandboxManifestStore(t, ctx)
	lifecycle := createSandboxLifecycleStoreFixture(t, ctx, st, run.ID)
	first, err := st.AcquireSandboxExecutionLease(ctx, lifecycle.Execution.ID,
		"sandbox_worker_one", "", sandbox.MinExecutionLeaseTTL)
	if err != nil || first.Replayed || first.TookOver || first.Lease.Generation != 2 {
		t.Fatalf("first post-preparation lease is invalid: %#v err=%v", first, err)
	}
	if _, err := st.AcquireSandboxExecutionLease(ctx, lifecycle.Execution.ID,
		"sandbox_worker_two", "", time.Minute); apperror.CodeOf(err) != apperror.CodeConflict {
		t.Fatalf("active sandbox lease admitted a second owner: %v", err)
	}
	replayed, err := st.AcquireSandboxExecutionLease(ctx, lifecycle.Execution.ID,
		first.Lease.OwnerID, first.Lease.LeaseID, time.Minute)
	if err != nil || !replayed.Replayed || replayed.Lease.Generation != first.Lease.Generation {
		t.Fatalf("same-owner sandbox lease renewal did not replay: %#v err=%v", replayed, err)
	}
	// Renewing above extended the lease, so release and acquire a short generation for takeover.
	if _, _, err := st.ReleaseSandboxExecutionLease(ctx, replayed.Lease); err != nil {
		t.Fatal(err)
	}
	short, err := st.AcquireSandboxExecutionLease(ctx, lifecycle.Execution.ID,
		"sandbox_worker_short", "", sandbox.MinExecutionLeaseTTL)
	if err != nil || short.Lease.Generation != 3 {
		t.Fatalf("short sandbox lease acquisition failed: %#v err=%v", short, err)
	}
	time.Sleep(sandbox.MinExecutionLeaseTTL + 150*time.Millisecond)
	taken, err := st.AcquireSandboxExecutionLease(ctx, lifecycle.Execution.ID,
		"sandbox_worker_takeover", "", time.Minute)
	if err != nil || !taken.TookOver || taken.Lease.Generation != 4 {
		t.Fatalf("expired sandbox lease was not fenced by a new generation: %#v err=%v", taken, err)
	}
	if _, _, err := st.ReleaseSandboxExecutionLease(ctx, short.Lease); apperror.CodeOf(err) != apperror.CodeConflict {
		t.Fatalf("stale sandbox worker released successor lease: %v", err)
	}
	now := time.Now().UTC()
	staleResult := sandbox.CleanupResult{
		ID: "cleanup-stale-lease", ExecutionID: lifecycle.Execution.ID,
		RunID: run.ID, ProtocolVersion: sandbox.CleanupProtocolVersion,
		LeaseID: short.Lease.LeaseID, LeaseGeneration: short.Lease.Generation,
		InputArtifactsVerified: true, CleanupComplete: true, Outcome: "backend_disabled",
		ReconciledBy: "sandbox_worker_short", CompletedAt: now,
	}
	staleOperation := sandbox.CleanupOperation{
		KeyDigest: runmutation.Fingerprint("test-sandbox-cleanup-key", lifecycle.Execution.ID),
		CleanupID: staleResult.ID, ExecutionID: lifecycle.Execution.ID,
		RunID: run.ID, ReconciledBy: staleResult.ReconciledBy, CreatedAt: now,
	}
	staleOperation.RequestFingerprint = sandbox.CleanupRequestFingerprint(
		lifecycle.Execution.ID, false, staleResult.ReconciledBy)
	if _, _, err := st.CompleteSandboxCleanup(ctx, staleResult, staleOperation,
		short.Lease); apperror.CodeOf(err) != apperror.CodeConflict {
		t.Fatalf("stale sandbox worker committed cleanup: %v", err)
	}
	if _, err := st.db.ExecContext(ctx, `UPDATE sandbox_disabled_executions
		SET backend_started = 1 WHERE id = ?`, lifecycle.Execution.ID); err == nil ||
		!strings.Contains(err.Error(), "cannot be updated") {
		t.Fatalf("sandbox execution root was mutable: %v", err)
	}
	if _, err := st.db.ExecContext(ctx, `DELETE FROM sandbox_execution_operations
		WHERE execution_id = ?`, lifecycle.Execution.ID); err == nil ||
		!strings.Contains(err.Error(), "cannot be deleted") {
		t.Fatalf("sandbox execution operation was deletable: %v", err)
	}
	if _, _, err := st.ReleaseSandboxExecutionLease(ctx, taken.Lease); err != nil {
		t.Fatal(err)
	}
	timeline, err := st.ListRunEvents(ctx, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if countRunEventType(timeline, events.SandboxExecutionLeaseTakenOverEvent) != 1 {
		t.Fatalf("sandbox lease takeover audit count is invalid: %#v", timeline)
	}
}

func TestSchemaV49UpgradeAddsDisabledSandboxLifecycleWithoutLosingCandidate(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "v49.db")
	st, run, _ := openSandboxManifestStoreAt(t, ctx, filepath.Join(t.TempDir(), "seed.db"), 177)
	restoreLegacyContext := addEmptyCurrentContextForLegacySeed(t, st)
	service := newSandboxFixtureService(t, st)
	prepared, err := service.Prepare(ctx, application.PrepareSandboxManifestRequest{
		RunID: run.ID, Manifest: sandboxStoreTestManifest(),
		OperationKey: "schema-v49-lifecycle-prepare", RequestedBy: "schema_upgrade_test",
	})
	if err != nil {
		t.Fatal(err)
	}
	candidate, err := service.ValidateExecutionCandidate(ctx,
		application.ValidateSandboxExecutionCandidateRequest{
			PreparationID: prepared.Preparation.ID, Manifest: sandboxStoreTestManifest(),
			OperationKey: "schema-v49-lifecycle-candidate", RequestedBy: "schema_upgrade_test",
		})
	if err != nil {
		t.Fatal(err)
	}
	restoreLegacyContext()
	st = historicalTestDatabaseFromSeed(t, st, path, 49)
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	st, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	if version, err := st.SchemaVersion(ctx); err != nil || version != LatestSchemaVersion {
		t.Fatalf("schema v49 did not upgrade to the latest schema: version=%d err=%v", version, err)
	}
	loaded, err := st.GetSandboxExecutionCandidate(ctx, candidate.Candidate.ID)
	if err != nil || loaded.Candidate.ID != candidate.Candidate.ID {
		t.Fatalf("schema v49 candidate was not preserved: %#v err=%v", loaded, err)
	}
	var table string
	if err := st.db.QueryRowContext(ctx, `SELECT name FROM sqlite_master
		WHERE type = 'table' AND name = 'sandbox_disabled_executions'`).Scan(&table); err != nil ||
		table != "sandbox_disabled_executions" {
		t.Fatalf("schema v50 lifecycle ledger is missing: %q err=%v", table, err)
	}
}

func createSandboxLifecycleStoreFixture(t *testing.T, ctx context.Context,
	st *SQLiteStore, runID string,
) sandbox.Lifecycle {
	t.Helper()
	service := newSandboxFixtureService(t, st)
	manifest := sandboxStoreTestManifest()
	prepared, err := service.Prepare(ctx, application.PrepareSandboxManifestRequest{
		RunID: runID, Manifest: manifest, OperationKey: "store-lifecycle-prepare",
		RequestedBy: "lifecycle_store_test",
	})
	if err != nil {
		t.Fatal(err)
	}
	candidate, err := service.ValidateExecutionCandidate(ctx,
		application.ValidateSandboxExecutionCandidateRequest{
			PreparationID: prepared.Preparation.ID, Manifest: manifest,
			OperationKey: "store-lifecycle-candidate", RequestedBy: "lifecycle_store_test",
		})
	if err != nil {
		t.Fatal(err)
	}
	lifecycle, err := service.BeginDisabledExecution(ctx, application.BeginSandboxExecutionRequest{
		CandidateID: candidate.Candidate.ID, Manifest: manifest,
		OperationKey: "store-lifecycle-begin-operation", RequestedBy: "lifecycle_store_test",
	})
	if err != nil {
		t.Fatal(err)
	}
	return lifecycle
}
