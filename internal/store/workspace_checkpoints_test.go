package store

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"cyberagent-workbench/internal/apperror"
	"cyberagent-workbench/internal/application"
	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/rootfs"
	"cyberagent-workbench/internal/workspacecheckpoint"
)

func TestWorkspaceCheckpointStoreSealsContentAndReplaysSemanticIntent(t *testing.T) {
	ctx := context.Background()
	state, runRecord, mission, workspaceRoot := newWorkspaceCheckpointStoreFixture(t)
	defer state.Close()
	now := time.Date(2026, 8, 19, 4, 0, 0, 0, time.UTC)
	before := captureStoreCheckpoint(t, runRecord, mission, workspaceRoot,
		"checkpoint-before", "receipt-before", workspacecheckpoint.PhaseBefore, now)
	created, replayed, err := state.CreateWorkspaceCheckpoint(ctx, before)
	if err != nil || replayed || created.ID != before.Checkpoint.ID {
		t.Fatalf("create=%+v replayed=%t err=%v", created, replayed, err)
	}
	replayedCheckpoint, replayed, err := state.CreateWorkspaceCheckpoint(ctx, before)
	if err != nil || !replayed || replayedCheckpoint.ID != created.ID {
		t.Fatalf("checkpoint replay=%+v replayed=%t err=%v",
			replayedCheckpoint, replayed, err)
	}
	loaded, err := state.GetWorkspaceCheckpointSnapshot(ctx, created.ID)
	if err != nil || loaded.Checkpoint.ManifestSHA256 != before.Checkpoint.ManifestSHA256 ||
		len(loaded.Blobs) != len(before.Blobs) {
		t.Fatalf("loaded checkpoint=%+v err=%v", loaded.Checkpoint, err)
	}
	for _, blob := range loaded.Blobs {
		var references int
		if err := state.db.QueryRowContext(ctx, `SELECT reference_count
			FROM workspace_checkpoint_blobs WHERE sha256 = ?`, blob.SHA256).
			Scan(&references); err != nil || references < 1 {
			t.Fatalf("blob %s references=%d err=%v", blob.SHA256, references, err)
		}
	}
	if _, err := state.db.ExecContext(ctx, `UPDATE workspace_checkpoints
		SET trigger_receipt_id = 'tampered' WHERE id = ?`, created.ID); err == nil {
		t.Fatal("sealed checkpoint accepted an update")
	}
	if _, err := state.db.ExecContext(ctx, `INSERT INTO workspace_checkpoint_entries
		(checkpoint_id, path, kind, worktree_state, storage_policy, mode, size_bytes,
		 worktree_sha256, blob_sha256, index_oid, index_mode, tracked, staged,
		 binary, line_endings, recoverable, reason)
		VALUES (?, 'late.txt', 'file', 'missing', 'missing', 0, 0, 'missing', '', '', '',
		0, 0, 0, '', 1, '')`, created.ID); err == nil {
		t.Fatal("sealed checkpoint accepted a late manifest entry")
	}

	mustCheckpointStoreWrite(t, filepath.Join(workspaceRoot, "file.txt"), []byte("after\n"))
	after := captureStoreCheckpointWithParent(t, runRecord, mission, workspaceRoot,
		"checkpoint-after", "receipt-after", before.Checkpoint.ID,
		workspacecheckpoint.PhaseAfter, now.Add(time.Minute))
	if _, _, err := state.CreateWorkspaceCheckpoint(ctx, after); err != nil {
		t.Fatal(err)
	}

	transaction := workspacecheckpoint.Transaction{ID: "transaction-generated-a",
		ProtocolVersion:    workspacecheckpoint.ProtocolVersion,
		OperationKeyDigest: storeCheckpointDigest("operation"),
		RequestFingerprint: storeCheckpointDigest("request"), RunID: runRecord.ID,
		WorkspaceID: mission.WorkspaceID, Kind: workspacecheckpoint.TransactionFileTool,
		TriggerReceiptID: "receipt-file-tool", BeforeCheckpointID: before.Checkpoint.ID,
		ExpectedCurrentCheckpointID: before.Checkpoint.ID,
		TargetCheckpointID:          after.Checkpoint.ID,
		Status:                      workspacecheckpoint.TransactionPrepared,
		RecoveryLevel:               workspacecheckpoint.RecoveryComplete, ConflictJSON: "[]",
		CreatedAt: now.Add(2 * time.Minute), UpdatedAt: now.Add(2 * time.Minute)}
	stored, replayed, err := state.CreateWorkspaceCheckpointTransaction(ctx, transaction)
	if err != nil || replayed {
		t.Fatalf("transaction=%+v replayed=%t err=%v", stored, replayed, err)
	}
	semanticReplay := transaction
	semanticReplay.ID = "transaction-generated-b"
	semanticReplay.CreatedAt = semanticReplay.CreatedAt.Add(time.Hour)
	semanticReplay.UpdatedAt = semanticReplay.CreatedAt
	converged, replayed, err := state.CreateWorkspaceCheckpointTransaction(ctx, semanticReplay)
	if err != nil || !replayed || converged.ID != stored.ID {
		t.Fatalf("semantic replay=%+v replayed=%t err=%v", converged, replayed, err)
	}
	completedAt := now.Add(3 * time.Minute)
	stored.Status = workspacecheckpoint.TransactionCompleted
	stored.AfterCheckpointID = after.Checkpoint.ID
	stored.UpdatedAt = completedAt
	stored.CompletedAt = &completedAt
	completed, replayed, err := state.UpdateWorkspaceCheckpointTransaction(ctx, stored)
	if err != nil || replayed || completed.Status != workspacecheckpoint.TransactionCompleted {
		t.Fatalf("complete=%+v replayed=%t err=%v", completed, replayed, err)
	}
	terminalMutation := completed
	terminalMutation.Status = workspacecheckpoint.TransactionFailed
	terminalMutation.ErrorCode = "tampered"
	terminalMutation.AfterCheckpointID = ""
	terminalMutation.UpdatedAt = completedAt.Add(time.Minute)
	terminalMutation.CompletedAt = &terminalMutation.UpdatedAt
	if _, _, err := state.UpdateWorkspaceCheckpointTransaction(ctx,
		terminalMutation); apperror.CodeOf(err) != apperror.CodeConflict {
		t.Fatalf("terminal transaction mutation error=%v", err)
	}

	orphan := []byte("unreferenced checkpoint blob")
	orphanHash := sha256.Sum256(orphan)
	if _, err := state.db.ExecContext(ctx, `INSERT INTO workspace_checkpoint_blobs
		(sha256, size_bytes, content, reference_count, created_at) VALUES (?, ?, ?, 0, ?)`,
		hex.EncodeToString(orphanHash[:]), len(orphan), orphan, ts(now)); err != nil {
		t.Fatal(err)
	}
	removed, err := state.GarbageCollectWorkspaceCheckpointBlobs(ctx, 10)
	if err != nil || removed != 1 {
		t.Fatalf("GC removed=%d err=%v", removed, err)
	}
	if _, err := state.db.ExecContext(ctx,
		`DROP TRIGGER trg_workspace_checkpoint_transaction_quota`); err != nil {
		t.Fatal(err)
	}
	if _, err := state.db.ExecContext(ctx, `CREATE TRIGGER trg_workspace_checkpoint_transaction_quota
		BEFORE INSERT ON workspace_checkpoint_transactions
		WHEN (SELECT COUNT(*) FROM workspace_checkpoint_transactions) >= 1
		BEGIN SELECT RAISE(ABORT, 'workspace checkpoint transaction quota exceeded'); END;`); err != nil {
		t.Fatal(err)
	}
	blocked := transaction
	blocked.ID = "transaction-blocked-by-metadata-quota"
	blocked.OperationKeyDigest = storeCheckpointDigest("operation-blocked-by-quota")
	blocked.RequestFingerprint = storeCheckpointDigest("request-blocked-by-quota")
	blocked.CreatedAt, blocked.UpdatedAt = now.Add(4*time.Minute), now.Add(4*time.Minute)
	if _, _, err := state.CreateWorkspaceCheckpointTransaction(ctx, blocked); apperror.CodeOf(err) != apperror.CodeResourceExhausted {
		t.Fatalf("transaction quota error=%v", err)
	}
}

func TestSchemaV117UpgradeAddsWorkspaceCheckpointLedgerWithoutRewritingRuns(t *testing.T) {
	ctx := context.Background()
	databasePath := filepath.Join(t.TempDir(), "workspace-checkpoint-v116.db")
	state := openHistoricalTestDatabase(t, databasePath+".seed.db", 177)

	workspaceRoot := newWorkspaceCheckpointGitRepository(t)
	workspace := WorkspaceRecord{ID: "workspace-migration-117", Name: "migration-117",
		RootPath: workspaceRoot}
	if err := state.SaveWorkspace(ctx, workspace); err != nil {
		t.Fatal(err)
	}
	_, runRecord, err := newMigrationFixtureRunService(t, state).Create(ctx,
		application.CreateRunRequest{Goal: "preserve Run across v117 migration",
			Profile: "code", WorkspaceID: workspace.ID,
			Budget: domain.Budget{MaxTurns: 2, MaxTokens: 500, MaxToolCalls: 4}})
	if err != nil {
		t.Fatal(err)
	}
	historical := historicalTestDatabaseFromSeed(t, state, databasePath, 116)
	if err := state.Close(); err != nil {
		t.Fatal(err)
	}
	state = historical
	if err := state.Close(); err != nil {
		t.Fatal(err)
	}
	upgraded, err := Open(databasePath)
	if err != nil {
		t.Fatal(err)
	}
	defer upgraded.Close()
	loadedRun, err := upgraded.GetRun(ctx, runRecord.ID)
	if err != nil || loadedRun.ID != runRecord.ID {
		t.Fatalf("Run after v117 migration=%+v err=%v", loadedRun, err)
	}
	var version int
	if err := upgraded.db.QueryRowContext(ctx, `SELECT MAX(version) FROM schema_migrations`).
		Scan(&version); err != nil || version != LatestSchemaVersion {
		t.Fatalf("schema version=%d err=%v", version, err)
	}
	for _, table := range []string{"workspace_checkpoint_blobs", "workspace_checkpoints",
		"workspace_checkpoint_entries", "workspace_checkpoint_transactions",
		"workspace_checkpoint_run_state"} {
		var count int
		if err := upgraded.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM sqlite_master
			WHERE type = 'table' AND name = ?`, table).Scan(&count); err != nil || count != 1 {
			t.Fatalf("table %s count=%d err=%v", table, count, err)
		}
	}
	var quotaTriggerSQL string
	if err := upgraded.db.QueryRowContext(ctx, `SELECT sql FROM sqlite_master
		WHERE type = 'trigger' AND name = 'trg_workspace_checkpoint_blob_store_quota'`).
		Scan(&quotaTriggerSQL); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(quotaTriggerSQL, "2147483648") ||
		!strings.Contains(quotaTriggerSQL, "SUM(size_bytes)") ||
		!strings.Contains(quotaTriggerSQL, "sha256 = NEW.sha256") {
		t.Fatalf("unexpected workspace checkpoint blob quota trigger: %s", quotaTriggerSQL)
	}
	for name, fragments := range map[string][]string{
		"trg_workspace_checkpoint_insert_unsealed":   {"NEW.sealed != 0"},
		"trg_workspace_checkpoint_metadata_quota":    {"10000", "2000000", "SUM(entry_count)"},
		"trg_workspace_checkpoint_transaction_quota": {"20000", "COUNT(*)"},
	} {
		var triggerSQL string
		if err := upgraded.db.QueryRowContext(ctx, `SELECT sql FROM sqlite_master
			WHERE type = 'trigger' AND name = ?`, name).Scan(&triggerSQL); err != nil {
			t.Fatal(err)
		}
		for _, fragment := range fragments {
			if !strings.Contains(triggerSQL, fragment) {
				t.Fatalf("trigger %s does not contain %q: %s", name, fragment, triggerSQL)
			}
		}
	}
}

func TestSchemaV232PreservesCheckpointReferencesAndExactPaths(t *testing.T) {
	state, run, mission, root := newWorkspaceCheckpointStoreFixture(t)
	t.Cleanup(func() { _ = state.Close() })
	ctx := t.Context()
	now := time.Now().UTC()
	before := captureStoreCheckpoint(t, run, mission, root, "checkpoint-v231", "receipt-v231",
		workspacecheckpoint.PhaseBefore, now)
	if _, _, err := state.CreateWorkspaceCheckpoint(ctx, before); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "checkpoint-v231.db")
	historical := historicalTestDatabaseFromSeed(t, state, path, 231)
	if err := historical.Close(); err != nil {
		t.Fatal(err)
	}
	upgraded, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = upgraded.Close() })
	loaded, err := upgraded.GetWorkspaceCheckpointSnapshot(ctx, before.Checkpoint.ID)
	if err != nil || loaded.Checkpoint.ManifestSHA256 != before.Checkpoint.ManifestSHA256 {
		t.Fatalf("existing checkpoint was not preserved: %+v, %v", loaded.Checkpoint, err)
	}
	for _, blob := range loaded.Blobs {
		var refs int
		err := upgraded.db.QueryRowContext(ctx, "SELECT reference_count FROM workspace_checkpoint_blobs WHERE sha256 = ?", blob.SHA256).Scan(&refs)
		if err != nil || refs != 1 {
			t.Fatalf("historical blob reference count = %d, %v", refs, err)
		}
	}
	const relative = " notes.txt "
	exact, err := rootfs.ExactAbsolutePath(filepath.Join(root, relative))
	if err != nil {
		t.Fatal(err)
	}
	mustCheckpointStoreWrite(t, exact, []byte("exact checkpoint content\n"))
	t.Cleanup(func() { _ = os.Remove(exact) })
	after := captureStoreCheckpointWithParent(t, run, mission, root, "checkpoint-v232", "receipt-v232",
		before.Checkpoint.ID, workspacecheckpoint.PhaseAfter, now.Add(time.Minute))
	if _, _, err := upgraded.CreateWorkspaceCheckpoint(ctx, after); err != nil {
		t.Fatal(err)
	}
	loaded, err = upgraded.GetWorkspaceCheckpointSnapshot(ctx, after.Checkpoint.ID)
	if err != nil || len(loaded.Entries) != 2 || loaded.Entries[0].Path != relative || !loaded.Entries[0].Recoverable {
		t.Fatalf("checkpoint lost exact path: %+v, %v", loaded.Entries, err)
	}
	var refs int
	err = upgraded.db.QueryRowContext(ctx, "SELECT reference_count FROM workspace_checkpoint_blobs WHERE sha256 = ?",
		loaded.Entries[0].BlobSHA256).Scan(&refs)
	if err != nil || refs != 1 {
		t.Fatalf("new blob reference count = %d, %v", refs, err)
	}
}

func TestWorkspaceCheckpointMetadataQuotaRollsBackCandidateBlobs(t *testing.T) {
	ctx := t.Context()
	state, runRecord, mission, workspaceRoot := newWorkspaceCheckpointStoreFixture(t)
	defer state.Close()
	candidate := captureStoreCheckpoint(t, runRecord, mission, workspaceRoot,
		"checkpoint-after-metadata-quota", "receipt-after-metadata-quota",
		workspacecheckpoint.PhaseStandalone, time.Now().UTC())
	c := candidate.Checkpoint
	_, err := state.db.ExecContext(ctx, `WITH RECURSIVE counter(value) AS (
		SELECT 1 UNION ALL SELECT value + 1 FROM counter WHERE value < ?
	) INSERT INTO workspace_checkpoints
		(id, protocol_version, run_id, mission_id, session_id, workspace_id, attempt_id,
		 capability_generation, trigger_kind, phase, trigger_receipt_id, requested_by,
		 title, parent_checkpoint_id, root_fingerprint, root_path_sha256, base_commit,
		 branch, index_sha256, index_blob_sha256, manifest_sha256, recovery_level,
		 incomplete_reasons_json, entry_count, stored_bytes, sealed, created_at)
	SELECT 'quota-checkpoint-' || value, ?, ?, ?, ?, ?, '', '', 'manual', 'standalone',
		'quota-receipt-' || value, '', '', '', ?, ?, ?, ?, ?, '', ?, 'complete',
		'[]', 0, 0, 0, ? FROM counter`, workspacecheckpoint.MaxStoreCheckpoints,
		c.ProtocolVersion, c.RunID, c.MissionID, c.SessionID, c.WorkspaceID,
		c.RootFingerprint, c.RootPathSHA256, c.BaseCommit, c.Branch, c.IndexSHA256,
		c.ManifestSHA256, ts(c.CreatedAt))
	if err != nil {
		t.Fatal(err)
	}
	var blobsBefore int
	if err := state.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM workspace_checkpoint_blobs`).Scan(&blobsBefore); err != nil {
		t.Fatal(err)
	}
	if _, _, err = state.CreateWorkspaceCheckpoint(ctx, candidate); apperror.CodeOf(err) != apperror.CodeResourceExhausted {
		t.Fatalf("metadata quota error=%v", err)
	}
	var blobsAfter int
	if err := state.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM workspace_checkpoint_blobs`).Scan(&blobsAfter); err != nil {
		t.Fatal(err)
	}
	if blobsAfter != blobsBefore {
		t.Fatalf("metadata quota left candidate blobs: before=%d after=%d", blobsBefore, blobsAfter)
	}
}

func newWorkspaceCheckpointStoreFixture(t *testing.T) (*SQLiteStore, domain.Run,
	domain.Mission, string,
) {
	t.Helper()
	ctx := context.Background()
	state, err := Open(filepath.Join(t.TempDir(), "workspace-checkpoints.db"))
	if err != nil {
		t.Fatal(err)
	}
	workspaceRoot := newWorkspaceCheckpointGitRepository(t)
	workspace := WorkspaceRecord{ID: "workspace-checkpoint", Name: "checkpoint",
		RootPath: workspaceRoot}
	if err := state.SaveWorkspace(ctx, workspace); err != nil {
		state.Close()
		t.Fatal(err)
	}
	mission, runRecord, err := application.NewRunService(state).Create(ctx,
		application.CreateRunRequest{Goal: "test reversible Workspace checkpoints",
			Profile: "code", WorkspaceID: workspace.ID,
			Budget: domain.Budget{MaxTurns: 4, MaxTokens: 1000, MaxToolCalls: 8}})
	if err != nil {
		state.Close()
		t.Fatal(err)
	}
	return state, runRecord, mission, workspaceRoot
}

func captureStoreCheckpoint(t *testing.T, runRecord domain.Run, mission domain.Mission,
	root, id, receipt string, phase workspacecheckpoint.Phase, at time.Time,
) workspacecheckpoint.Snapshot {
	t.Helper()
	return captureStoreCheckpointWithParent(t, runRecord, mission, root, id, receipt, "",
		phase, at)
}

func captureStoreCheckpointWithParent(t *testing.T, runRecord domain.Run,
	mission domain.Mission, root, id, receipt, parent string,
	phase workspacecheckpoint.Phase, at time.Time,
) workspacecheckpoint.Snapshot {
	t.Helper()
	snapshot, err := workspacecheckpoint.Capture(t.Context(), workspacecheckpoint.CaptureRequest{
		ID: id, RunID: runRecord.ID, MissionID: mission.ID, SessionID: runRecord.SessionID,
		WorkspaceID: mission.WorkspaceID, WorkspaceRoot: root, Trigger: workspacecheckpoint.TriggerFileTool,
		Phase: phase, TriggerReceiptID: receipt, ParentCheckpointID: parent, CreatedAt: at})
	if err != nil {
		t.Fatal(err)
	}
	return snapshot
}

func newWorkspaceCheckpointGitRepository(t *testing.T) string {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is unavailable")
	}
	root := t.TempDir()
	runWorkspaceCheckpointGit(t, root, "init", "-q")
	runWorkspaceCheckpointGit(t, root, "config", "user.email", "checkpoint@example.invalid")
	runWorkspaceCheckpointGit(t, root, "config", "user.name", "Checkpoint Test")
	mustCheckpointStoreWrite(t, filepath.Join(root, "file.txt"), []byte("before\n"))
	runWorkspaceCheckpointGit(t, root, "add", ".")
	runWorkspaceCheckpointGit(t, root, "commit", "-m", "baseline")
	return root
}

func runWorkspaceCheckpointGit(t *testing.T, root string, args ...string) string {
	t.Helper()
	command := exec.Command("git", append([]string{"-C", root}, args...)...)
	command.Env = append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1", "GIT_TERMINAL_PROMPT=0")
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v: %s", args, err, output)
	}
	return strings.TrimSpace(string(output))
}

func mustCheckpointStoreWrite(t *testing.T, path string, content []byte) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, content, 0o644); err != nil {
		t.Fatal(err)
	}
}

func storeCheckpointDigest(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}
