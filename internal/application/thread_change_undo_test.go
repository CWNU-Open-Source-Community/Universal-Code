package application_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"cyberagent-workbench/internal/apperror"
	"cyberagent-workbench/internal/application"
	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/store"
	"cyberagent-workbench/internal/workspacecheckpoint"
)

func undoThreadReply(t *testing.T, st application.ThreadChangeUndoStore, run domain.Run, eventID string, confirm bool) application.ThreadChangeUndoResult {
	t.Helper()
	r, err := application.NewThreadChangeUndoService(st, domain.ExecutionPermissionRuntimeCapabilities{OperatorApprovalEnabled: true}).Undo(t.Context(), domain.InitialThreadID(run.ID), application.ThreadChangeUndoRequest{Version: application.ThreadChangeUndoVersion, SourceEventID: eventID, Confirm: confirm})
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func TestThreadChangeUndoRestoresReplyIntervalAndDurablyReplays(t *testing.T) {
	root := t.TempDir()
	st, run := threadReviewFixture(t, root)
	for path, text := range map[string]string{"app.txt": "before\n", "old.txt": "move me\n", "deleted.txt": "deleted\n", "outside.txt": "untouched\n"} {
		if err := os.WriteFile(filepath.Join(root, path), []byte(text), 0600); err != nil {
			t.Fatal(err)
		}
	}
	applyThreadChange(t, st, run, root, "app.txt", "replace", "", "middle\n")
	applyThreadChange(t, st, run, root, "app.txt", "replace", "", "after\n")
	applyThreadChange(t, st, run, root, "old.txt", "move", "intermediate.txt", "")
	applyThreadChange(t, st, run, root, "intermediate.txt", "move", "new.txt", "")
	applyThreadChange(t, st, run, root, "new.txt", "replace", "", "changed after move\n")
	deleteBefore, err := workspacecheckpoint.Capture(t.Context(), workspacecheckpoint.CaptureRequest{ID: "delete-before", RunID: run.ID, MissionID: run.MissionID, SessionID: run.SessionID,
		WorkspaceID: "review-workspace", WorkspaceRoot: root, Trigger: workspacecheckpoint.TriggerFileTool, Phase: workspacecheckpoint.PhaseBefore, TriggerReceiptID: "pending-edit", CreatedAt: time.Now().UTC()})
	if err != nil {
		t.Fatal(err)
	}
	deleted := applyThreadChange(t, st, run, root, "deleted.txt", "delete", "", "")
	deleteBefore.Checkpoint.TriggerReceiptID = deleted.ID
	if _, _, err := st.CreateWorkspaceCheckpoint(t.Context(), deleteBefore); err != nil {
		t.Fatal(err)
	}
	applyThreadChange(t, st, run, root, "created.txt", "create", "", "new\n")
	reply := saveThreadChangeReply(t, st, run, "done")
	if err := os.WriteFile(filepath.Join(root, "outside.txt"), []byte("external change survives\n"), 0600); err != nil {
		t.Fatal(err)
	}
	preview := undoThreadReply(t, st, run, reply.EventID, false)
	if preview.State != "available" {
		t.Fatalf("preview: %+v", preview)
	}
	if text, _ := os.ReadFile(filepath.Join(root, "app.txt")); string(text) != "after\n" {
		t.Fatal("preview wrote files")
	}
	result := undoThreadReply(t, st, run, reply.EventID, true)
	if result.State != "completed" || result.Replayed || len(result.RemainingPaths) != 0 {
		t.Fatalf("undo: %+v", result)
	}
	for path, want := range map[string]string{"app.txt": "before\n", "old.txt": "move me\n", "deleted.txt": "deleted\n", "outside.txt": "external change survives\n"} {
		text, err := os.ReadFile(filepath.Join(root, path))
		if err != nil || string(text) != want {
			t.Fatalf("%s: %q %v", path, text, err)
		}
	}
	for _, path := range []string{"new.txt", "intermediate.txt", "created.txt"} {
		if _, err := os.Stat(filepath.Join(root, path)); !os.IsNotExist(err) {
			t.Fatalf("%s still exists: %v", path, err)
		}
	}
	if err := os.WriteFile(filepath.Join(root, "app.txt"), []byte("later user work\n"), 0600); err != nil {
		t.Fatal(err)
	}
	replay := undoThreadReply(t, st, run, reply.EventID, true)
	if replay.State != "completed" || !replay.Replayed {
		t.Fatalf("replay: %+v", replay)
	}
	if text, _ := os.ReadFile(filepath.Join(root, "app.txt")); string(text) != "later user work\n" {
		t.Fatal("replay overwrote later work")
	}
	if checkpoint, found, err := st.GetWorkspaceCheckpointRunState(t.Context(), run.ID); err != nil || found {
		t.Fatalf("reply undo changed whole workspace cursor: %+v %v %v", checkpoint, found, err)
	}
	checkpoints, err := application.NewWorkspaceCheckpointService(st, domain.ExecutionPermissionRuntimeCapabilities{OperatorApprovalEnabled: true})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = checkpoints.Reconcile(t.Context()); err != nil {
		t.Fatal(err)
	}
	timeline, err := checkpoints.Timeline(t.Context(), run.ID, 100)
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range timeline.Checkpoints {
		if workspacecheckpoint.IsThreadUndoCheckpoint(c) {
			t.Fatal("scoped undo snapshot leaked into whole-workspace timeline")
		}
	}
	transactions, err := st.ListWorkspaceCheckpointTransactions(t.Context(), run.ID, 100)
	if err != nil {
		t.Fatal(err)
	}
	for _, transaction := range transactions {
		if workspacecheckpoint.IsThreadUndo(transaction) {
			_, err = checkpoints.Restore(t.Context(), application.WorkspaceRestoreRequest{RunID: run.ID, TargetCheckpointID: transaction.TargetCheckpointID, Kind: workspacecheckpoint.TransactionRewind})
			if apperror.CodeOf(err) != apperror.CodeFailedPrecondition {
				t.Fatalf("scoped target accepted by whole restore: %v", err)
			}
		}
	}
}

func TestThreadChangeUndoRejectsLaterWritesAndBrokenHistory(t *testing.T) {
	root := t.TempDir()
	st, run := threadReviewFixture(t, root)
	if err := os.WriteFile(filepath.Join(root, "app.txt"), []byte("before\n"), 0600); err != nil {
		t.Fatal(err)
	}
	applyThreadChange(t, st, run, root, "app.txt", "replace", "", "after\n")
	reply := saveThreadChangeReply(t, st, run, "first")
	applyThreadChange(t, st, run, root, "app.txt", "replace", "", "next turn\n")
	saveThreadChangeReply(t, st, run, "second")
	r := undoThreadReply(t, st, run, reply.EventID, true)
	if r.State != "conflict" || len(r.Conflicts) != 1 || r.Conflicts[0].Path != "app.txt" {
		t.Fatalf("conflict: %+v", r)
	}
	if text, _ := os.ReadFile(filepath.Join(root, "app.txt")); string(text) != "next turn\n" {
		t.Fatal("later reply was overwritten")
	}
	if err := os.WriteFile(filepath.Join(root, "app.txt"), []byte("external gap\n"), 0600); err != nil {
		t.Fatal(err)
	}
	applyThreadChange(t, st, run, root, "app.txt", "replace", "", "after gap\n")
	if err := os.WriteFile(filepath.Join(root, "app.txt"), []byte("second gap\n"), 0600); err != nil {
		t.Fatal(err)
	}
	applyThreadChange(t, st, run, root, "app.txt", "replace", "", "last\n")
	gap := saveThreadChangeReply(t, st, run, "gap")
	if r := undoThreadReply(t, st, run, gap.EventID, true); r.State != "unavailable" || r.Conflicts[0].Reason != "incomplete_history" {
		t.Fatalf("gap: %+v", r)
	}
}

type threadUndoConcurrentWriter struct {
	*store.SQLiteStore
	root        string
	transaction workspacecheckpoint.Transaction
}

func (s *threadUndoConcurrentWriter) ClaimThreadChangeUndo(ctx context.Context, t workspacecheckpoint.Transaction) (workspacecheckpoint.Transaction, bool, error) {
	result, claimed, err := s.SQLiteStore.ClaimThreadChangeUndo(ctx, t)
	if err == nil && claimed {
		s.transaction = result
		err = os.WriteFile(filepath.Join(s.root, "b.txt"), []byte("concurrent user content\n"), 0600)
	}
	return result, claimed, err
}

func TestThreadChangeUndoPersistsPartialResultWithoutReapplying(t *testing.T) {
	root := t.TempDir()
	st, run := threadReviewFixture(t, root)
	for _, path := range []string{"a.txt", "b.txt"} {
		if err := os.WriteFile(filepath.Join(root, path), []byte("before\n"), 0600); err != nil {
			t.Fatal(err)
		}
		applyThreadChange(t, st, run, root, path, "replace", "", "after\n")
	}
	reply := saveThreadChangeReply(t, st, run, "done")
	wrapped := &threadUndoConcurrentWriter{SQLiteStore: st, root: root}
	r := undoThreadReply(t, wrapped, run, reply.EventID, true)
	if r.State != "partial" || len(r.RestoredPaths) != 1 || r.RestoredPaths[0] != "a.txt" || len(r.RemainingPaths) != 1 || r.RemainingPaths[0] != "b.txt" {
		t.Fatalf("partial: %+v", r)
	}
	if err := os.WriteFile(filepath.Join(root, "a.txt"), []byte("later edit\n"), 0600); err != nil {
		t.Fatal(err)
	}
	replay := undoThreadReply(t, st, run, reply.EventID, true)
	if replay.State != "partial" || !replay.Replayed {
		t.Fatalf("partial replay: %+v", replay)
	}
	if text, _ := os.ReadFile(filepath.Join(root, "a.txt")); string(text) != "later edit\n" {
		t.Fatal("partial receipt reapplied files")
	}
	if err := st.CheckThreadChangeUndoIdle(t.Context(), domain.InitialThreadID(run.ID), run.ID, ""); err != nil {
		t.Fatalf("terminal partial retained reservation: %v", err)
	}
}

func TestThreadChangeUndoExecutionLeaseBlocksMutation(t *testing.T) {
	root := t.TempDir()
	st, run := threadReviewFixture(t, root)
	applyThreadChange(t, st, run, root, "app.txt", "create", "", "after\n")
	reply := saveThreadChangeReply(t, st, run, "done")
	_, err := st.AcquireRunExecutionLease(t.Context(), domain.AcquireRunExecutionLeaseRequest{RunID: run.ID, OwnerID: "active-model", TTL: time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	if r := undoThreadReply(t, st, run, reply.EventID, true); r.State != "unavailable" || r.Conflicts[0].Reason != "run_busy" {
		t.Fatalf("active lease: %+v", r)
	}
}

func TestThreadChangeUndoRejectsDeletedFileWithoutOriginalMode(t *testing.T) {
	root := t.TempDir()
	st, run := threadReviewFixture(t, root)
	if err := os.WriteFile(filepath.Join(root, "deleted.txt"), []byte("original\n"), 0600); err != nil {
		t.Fatal(err)
	}
	applyThreadChange(t, st, run, root, "deleted.txt", "delete", "", "")
	reply := saveThreadChangeReply(t, st, run, "deleted")
	r := undoThreadReply(t, st, run, reply.EventID, true)
	if r.State != "unavailable" || r.Conflicts[0].Reason != "incomplete_history" {
		t.Fatalf("unknown original mode: %+v", r)
	}
	if _, err := os.Stat(filepath.Join(root, "deleted.txt")); !os.IsNotExist(err) {
		t.Fatal("unknown mode was guessed")
	}
}
