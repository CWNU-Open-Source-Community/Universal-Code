package application

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/events"
	"cyberagent-workbench/internal/fileedit"
	"cyberagent-workbench/internal/session"
	"cyberagent-workbench/internal/store"
	"cyberagent-workbench/internal/workspacecheckpoint"
)

type slowAfterCheckpointStore struct {
	*store.SQLiteStore
	updated   workspacecheckpoint.Transaction
	updateHit bool
}

func (s *slowAfterCheckpointStore) CreateWorkspaceCheckpoint(ctx context.Context,
	snapshot workspacecheckpoint.Snapshot,
) (workspacecheckpoint.Checkpoint, bool, error) {
	if snapshot.Checkpoint.Phase == workspacecheckpoint.PhaseAfter {
		// Exhaust the finish budget on the after-checkpoint persistence so a
		// terminal write sharing the capture budget would run on an expired
		// context.
		time.Sleep(2 * threadUndoFinishBudget)
		return workspacecheckpoint.Checkpoint{}, false, errors.New("checkpoint store unavailable")
	}
	return s.SQLiteStore.CreateWorkspaceCheckpoint(ctx, snapshot)
}

func (s *slowAfterCheckpointStore) UpdateWorkspaceCheckpointTransaction(ctx context.Context,
	value workspacecheckpoint.Transaction,
) (workspacecheckpoint.Transaction, bool, error) {
	result, replayed, err := s.SQLiteStore.UpdateWorkspaceCheckpointTransaction(ctx, value)
	if err == nil {
		s.updated, s.updateHit = result, true
	}
	return result, replayed, err
}

func TestThreadChangeUndoTerminalWriteSurvivesAnExhaustedCaptureBudget(t *testing.T) {
	orig := threadUndoFinishBudget
	threadUndoFinishBudget = 100 * time.Millisecond
	t.Cleanup(func() { threadUndoFinishBudget = orig })

	root := t.TempDir()
	st, err := store.Open(filepath.Join(t.TempDir(), "undo-budget.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	ctx := context.Background()
	if err = st.SaveWorkspace(ctx, store.WorkspaceRecord{ID: "review-workspace",
		Name: "Review fixture", RootPath: root, CreatedAt: time.Now().UTC()}); err != nil {
		t.Fatal(err)
	}
	_, run, err := NewRunService(st).Create(ctx, CreateRunRequest{Goal: "undo terminal budget",
		Profile: "code", Phase: "deliver", WorkspaceID: "review-workspace",
		ModelRoute: "fixture/model", Interactive: true})
	if err != nil {
		t.Fatal(err)
	}
	mission, err := st.GetMission(ctx, run.MissionID)
	if err != nil {
		t.Fatal(err)
	}
	manager := fileedit.NewManager(st)
	edit, err := manager.Propose(ctx, fileedit.Proposal{SessionID: run.SessionID,
		WorkspaceID: mission.WorkspaceID, WorkspaceRoot: root, Path: "app.txt",
		Operation: fileedit.OperationCreate, ProposedText: "after\n", ExpectedOriginalHash: "missing"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = NewFileEditReviewService(st).Review(ctx, ReviewFileEditRequest{
		Version: FileEditReviewProtocolVersion, RunID: run.ID, EditID: edit.ID,
		Action: FileEditApproveIntent}); err != nil {
		t.Fatal(err)
	}
	if _, err = manager.Approve(ctx, edit.ID, root); err != nil {
		t.Fatal(err)
	}
	message, err := st.SaveSessionMessage(ctx, session.NewMessage(run.SessionID, "assistant", "done"))
	if err != nil {
		t.Fatal(err)
	}
	list, err := st.ListRunEvents(ctx, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	var replyID string
	for _, event := range list {
		if event.Type == events.SessionMessageEvent && event.SubjectID == fmt.Sprint(message.ID) {
			replyID = event.EventID
			break
		}
	}
	if replyID == "" {
		t.Fatal("reply event was not projected")
	}

	wrapped := &slowAfterCheckpointStore{SQLiteStore: st}
	r, undoErr := NewThreadChangeUndoService(wrapped,
		domain.ExecutionPermissionRuntimeCapabilities{OperatorApprovalEnabled: true}).Undo(ctx,
		domain.InitialThreadID(run.ID), ThreadChangeUndoRequest{
			Version: ThreadChangeUndoVersion, SourceEventID: replyID, Confirm: true})
	if undoErr == nil {
		t.Fatal("exhausted capture budget did not surface")
	}
	if r.State != "partial" || len(r.Conflicts) == 0 || r.Conflicts[len(r.Conflicts)-1].Reason != "restore_failed" {
		t.Fatalf("interrupted result: %+v", r)
	}
	if !wrapped.updateHit || wrapped.updated.Status != workspacecheckpoint.TransactionInterrupted ||
		wrapped.updated.CompletedAt == nil {
		t.Fatalf("terminal write did not survive the exhausted capture budget: hit=%t row=%+v",
			wrapped.updateHit, wrapped.updated)
	}
	if err = st.CheckThreadChangeUndoIdle(ctx, domain.InitialThreadID(run.ID), run.ID, ""); err != nil {
		t.Fatalf("workspace stayed fenced after the exhausted budget: %v", err)
	}
}
