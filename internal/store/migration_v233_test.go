package store

import (
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"cyberagent-workbench/internal/fileedit"
)

func TestSchemaV233PreservesFileEditApplyJournal(t *testing.T) {
	ctx := t.Context()
	path := filepath.Join(t.TempDir(), "file-edits-v232.db")
	state := openHistoricalTestDatabase(t, path, 232)
	run, source := newV153SourceRun(t, state, "v233-journal")
	edit := v153Propose(t, state, run, source.ID, source.RootPath, "existing edit", time.Now().UTC())
	edit = v153Approved(t, state, run, edit, nil)
	operation, _, _, err := state.PrepareFileEditApply(ctx, v153ApplyOperation(run, edit, "v233-existing-apply"))
	if err != nil {
		t.Fatal(err)
	}
	edit.Status = fileedit.StatusApplied
	if _, err := state.SaveFileEdit(ctx, edit); err != nil {
		t.Fatal(err)
	}
	result, _, err := state.CompleteFileEditApply(ctx, fileedit.ApplyResult{
		OperationKeyDigest: operation.KeyDigest, Status: fileedit.ApplyCompleted, CompletedAt: time.Now().UTC(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := state.Close(); err != nil {
		t.Fatal(err)
	}
	upgraded, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = upgraded.Close() })
	stored, receipt, found, err := upgraded.GetFileEditApplyOperation(ctx, operation.KeyDigest)
	if err != nil || !found || !reflect.DeepEqual(stored, operation) || receipt == nil || !reflect.DeepEqual(*receipt, result) {
		t.Fatalf("apply history changed during migration: %+v, %+v, %v, %v", stored, receipt, found, err)
	}
	if _, err := upgraded.db.ExecContext(ctx, "UPDATE file_edit_apply_operations SET path = ? WHERE operation_key_digest = ?",
		" different.txt ", operation.KeyDigest); err == nil {
		t.Fatal("migrated journal accepted an update")
	}

	const relative = " notes.txt "
	next, err := fileedit.NewManager(upgraded).PrepareProposal(ctx, fileedit.Proposal{
		SessionID: run.SessionID, WorkspaceID: source.ID, WorkspaceRoot: source.RootPath,
		Operation: fileedit.OperationCreate, Path: relative, ExpectedOriginalHash: "missing", ProposedText: "new note\n",
	})
	if err != nil {
		t.Fatal(err)
	}
	next, err = upgraded.SaveFileEdit(ctx, next)
	if err != nil {
		t.Fatal(err)
	}
	next = v153Approved(t, upgraded, run, next, nil)
	newOperation, _, _, err := upgraded.PrepareFileEditApply(ctx, v153ApplyOperation(run, next, "v233-exact-apply"))
	if err != nil {
		t.Fatal(err)
	}
	reloaded, _, found, err := upgraded.GetFileEditApplyOperation(ctx, newOperation.KeyDigest)
	if err != nil || !found || reloaded.Path != relative {
		t.Fatalf("migrated journal lost the exact path: %q, %v, %v", reloaded.Path, found, err)
	}
	assertNoForeignKeyViolations(t, upgraded.db)
}
