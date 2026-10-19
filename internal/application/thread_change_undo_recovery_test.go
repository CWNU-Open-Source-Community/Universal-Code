package application

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/events"
	"cyberagent-workbench/internal/fileedit"
	"cyberagent-workbench/internal/runmutation"
	"cyberagent-workbench/internal/session"
	"cyberagent-workbench/internal/workspacecheckpoint"
)

func TestThreadChangeUndoClaimAndDrydockRestartRecovery(t *testing.T) {
	f := newDrydockApplicationFixture(t, "reply undo recovery")
	checkpoints, err := NewWorkspaceCheckpointService(f.state, domain.ExecutionPermissionRuntimeCapabilities{OperatorApprovalEnabled: true})
	if err != nil {
		t.Fatal(err)
	}
	f.service.WithCheckpointService(checkpoints)
	w := mustCreateDrydock(t, f)
	writeDrydockTestFile(t, filepath.Join(w.Path, "tracked.txt"), "changed\n")
	if _, err := f.state.SaveSessionMessage(t.Context(), session.NewMessage(f.run.SessionID, "assistant", "done")); err != nil {
		t.Fatal(err)
	}
	facts, err := f.state.ListRunEvents(t.Context(), f.run.ID)
	if err != nil {
		t.Fatal(err)
	}
	sourceID := ""
	for _, fact := range facts {
		if fact.Type == events.SessionMessageEvent {
			sourceID = fact.EventID
		}
	}
	observed, err := workspacecheckpoint.Capture(t.Context(), workspacecheckpoint.CaptureRequest{ID: "undo-observed", RunID: f.run.ID, MissionID: f.run.MissionID, SessionID: f.run.SessionID,
		WorkspaceID: w.WorkspaceID, WorkspaceRoot: w.Path, Trigger: workspacecheckpoint.TriggerRewindPreflight, Phase: workspacecheckpoint.PhasePreflight, TriggerReceiptID: "reply-undo-test", CreatedAt: time.Now().UTC()})
	if err != nil {
		t.Fatal(err)
	}
	paths := map[string]threadUndoPath{"tracked.txt": {beforeHash: fileedit.HashText("base\n"), beforeText: "base\n", afterHash: fileedit.HashText("changed\n"), afterText: "changed\n", modePath: "tracked.txt"}}
	before := threadUndoSnapshot(observed, paths, false, "undo-before")
	target := threadUndoSnapshot(observed, paths, true, "undo-target")
	for _, snapshot := range []workspacecheckpoint.Snapshot{before, target} {
		if _, _, err := f.state.CreateWorkspaceCheckpoint(t.Context(), snapshot); err != nil {
			t.Fatal(err)
		}
	}
	now := time.Now().UTC()
	digest := threadUndoDigest(domain.InitialThreadID(f.run.ID), sourceID)
	tx := workspacecheckpoint.Transaction{ID: "undo-recovery-journal", ProtocolVersion: workspacecheckpoint.ProtocolVersion, OperationKeyDigest: digest,
		RequestFingerprint: runmutation.Fingerprint("reply-undo-recovery"), RunID: f.run.ID, WorkspaceID: w.WorkspaceID, Kind: workspacecheckpoint.TransactionUndo,
		TriggerReceiptID: workspacecheckpoint.ThreadUndoReceiptPrefix + sourceID, BeforeCheckpointID: before.Checkpoint.ID, TargetCheckpointID: target.Checkpoint.ID,
		Status: workspacecheckpoint.TransactionPrepared, RecoveryLevel: workspacecheckpoint.RecoveryComplete, ConflictJSON: "[]", CreatedAt: now, UpdatedAt: now}
	tx, _, err = f.state.CreateWorkspaceCheckpointTransaction(t.Context(), tx)
	if err != nil {
		t.Fatal(err)
	}
	if _, claimed, err := f.state.ClaimThreadChangeUndo(t.Context(), tx); err != nil || !claimed {
		t.Fatalf("initial claim: %v %v", claimed, err)
	}
	if _, claimed, err := f.state.ClaimThreadChangeUndo(t.Context(), tx); err != nil || claimed {
		t.Fatalf("stale prepared claim entered IO: %v %v", claimed, err)
	}
	if err := f.state.CheckThreadChangeUndoIdle(t.Context(), domain.InitialThreadID(f.run.ID), f.run.ID, ""); err == nil {
		t.Fatal("open undo did not reserve workspace")
	}
	// Recovery observes an interrupted process; user content is retained.
	writeDrydockTestFile(t, filepath.Join(w.Path, "tracked.txt"), "user work after crash\n")
	count, err := f.service.ReconcileWorkspaceCheckpoints(t.Context())
	if err != nil || count != 1 {
		t.Fatalf("Drydock recovery: count=%d err=%v", count, err)
	}
	journal, found, err := f.state.GetWorkspaceCheckpointTransaction(t.Context(), tx.ID)
	if err != nil || !found || journal.Status != workspacecheckpoint.TransactionInterrupted {
		t.Fatalf("journal: %+v %v", journal, err)
	}
	after, err := f.state.GetWorkspaceCheckpointSnapshot(t.Context(), journal.AfterCheckpointID)
	if err != nil || len(after.Entries) != 1 || after.Entries[0].WorktreeSHA256 != fileedit.HashText("user work after crash\n") {
		t.Fatalf("selected recovery state: %+v %v", after, err)
	}
	current, found, err := f.state.GetRunFileDrydock(t.Context(), f.run.ID)
	if err != nil || !found || current.LastCheckpointID != w.LastCheckpointID {
		t.Fatalf("whole Drydock cursor moved: %+v %v", current, err)
	}
	if err := f.state.CheckThreadChangeUndoIdle(t.Context(), domain.InitialThreadID(f.run.ID), f.run.ID, ""); err != nil {
		t.Fatalf("recovery retained reservation: %v", err)
	}
}

func TestThreadChangeUndoRejectsOmittedLivePath(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "unobserved.txt")
	if err := os.WriteFile(path, []byte("live\n"), 0600); err != nil {
		t.Fatal(err)
	}
	paths := map[string]threadUndoPath{"unobserved.txt": {}}
	if err := validateThreadUndoObservation(root, workspacecheckpoint.Snapshot{}, paths); err == nil {
		t.Fatal("truncated capture treated live path as missing")
	}
}
