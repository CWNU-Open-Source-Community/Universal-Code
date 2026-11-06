package application

import (
	"context"
	"errors"
	"testing"
	"time"

	"cyberagent-workbench/internal/approval"
	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/fileedit"
	"cyberagent-workbench/internal/session"
)

type fileEditReviewMemoryStore struct {
	run       domain.Run
	mission   domain.Mission
	edits     map[string]fileedit.Edit
	approvals map[string]approval.Record
}

func (s *fileEditReviewMemoryStore) GetApprovalByProposal(_ context.Context,
	proposalID string,
) (approval.Record, error) {
	record, found := s.approvals[proposalID]
	if !found {
		return approval.Record{}, errors.New("not found")
	}
	return record, nil
}

func (s *fileEditReviewMemoryStore) DecideApproval(_ context.Context,
	request approval.DecisionRequest,
) (approval.DecisionResult, error) {
	record, found := s.approvals[request.ProposalID]
	if !found {
		return approval.DecisionResult{}, errors.New("not found")
	}
	wanted, ok := request.Action.Status()
	if !ok {
		return approval.DecisionResult{}, errors.New("invalid action")
	}
	if record.Status == wanted {
		return approval.DecisionResult{Approval: record, Replayed: true}, nil
	}
	if record.Status != approval.StatusPending {
		return approval.DecisionResult{}, errors.New("conflicting decision")
	}
	now := time.Now().UTC()
	record.Status = wanted
	record.DecisionReason = request.Reason
	record.ReviewedBy = request.ReviewedBy
	record.Version++
	record.UpdatedAt = now
	record.DecidedAt = &now
	s.approvals[request.ProposalID] = record
	return approval.DecisionResult{Approval: record}, nil
}

func (s *fileEditReviewMemoryStore) CommitFileEditReview(ctx context.Context, expected fileedit.Edit, observed approval.Record, request approval.DecisionRequest) (fileedit.Edit, bool, error) {
	current, found := s.edits[expected.ID]
	if !found || current != expected || s.approvals[expected.ID].Version != observed.Version {
		return fileedit.Edit{}, false, errors.New("review changed")
	}
	prepared, err := fileedit.PrepareReview(current, request.Action == approval.ActionApprove, request.Reason, time.Now().UTC())
	if err != nil {
		return fileedit.Edit{}, false, err
	}
	decision, err := s.DecideApproval(ctx, request)
	if err != nil {
		return fileedit.Edit{}, false, err
	}
	s.edits[prepared.ID] = prepared
	return prepared, decision.Replayed, nil
}

func fileEditReviewApproval(run domain.Run, mission domain.Mission,
	edit fileedit.Edit,
) approval.Record {
	return approval.Record{ID: "approval-review", ProposalID: edit.ID,
		IdempotencyKey: approval.ProposalIdempotencyKey("replace_file", edit.ID),
		RunID:          run.ID, SessionID: run.SessionID, WorkspaceID: mission.WorkspaceID,
		ToolName: "replace_file", ActionClass: "workspace_write", Mode: "per_call",
		Status: approval.StatusPending,
		RequestFingerprint: approval.FileEditFingerprint(edit.SessionID,
			edit.WorkspaceID, edit.Path, edit.ProposedHash),
		RequestedBy: "tool_gateway", Version: 1, CreatedAt: edit.CreatedAt,
		UpdatedAt: edit.UpdatedAt}
}

func (s *fileEditReviewMemoryStore) GetRun(context.Context, string) (domain.Run, error) {
	return s.run, nil
}

func (s *fileEditReviewMemoryStore) GetMission(context.Context, string) (domain.Mission, error) {
	return s.mission, nil
}

func (s *fileEditReviewMemoryStore) GetSession(context.Context, string) (session.Session, error) {
	return session.Session{ID: s.run.SessionID, WorkspaceID: s.mission.WorkspaceID,
		Status: session.StatusActive}, nil
}

func (s *fileEditReviewMemoryStore) GetWorkspaceInfo(context.Context, string) (session.WorkspaceInfo, error) {
	return session.WorkspaceInfo{ID: s.mission.WorkspaceID, RootPath: "review-memory-workspace"}, nil
}

func (s *fileEditReviewMemoryStore) SaveFileEdit(_ context.Context,
	edit fileedit.Edit,
) (fileedit.Edit, error) {
	s.edits[edit.ID] = edit
	return edit, nil
}

func (s *fileEditReviewMemoryStore) GetFileEdit(_ context.Context,
	id string,
) (fileedit.Edit, error) {
	edit, found := s.edits[id]
	if !found {
		return fileedit.Edit{}, errors.New("not found")
	}
	return edit, nil
}

func (s *fileEditReviewMemoryStore) ListFileEdits(context.Context,
	fileedit.ListFilter,
) ([]fileedit.Edit, error) {
	return nil, nil
}

func TestFileEditReviewRecordsApprovalIntentWithoutApply(t *testing.T) {
	now := time.Now().UTC()
	edit := fileedit.Edit{ID: "edit-review", SessionID: "session-review",
		WorkspaceID: "workspace-review", Path: "README.md", Status: fileedit.StatusProposed,
		ProposedText: "new\n", ProposedHash: fileedit.HashText("new\n"),
		OriginalHash: "missing", CreatedAt: now, UpdatedAt: now}
	store := &fileEditReviewMemoryStore{
		run: domain.Run{ID: "run-review", MissionID: "mission-review",
			SessionID: "session-review", Status: domain.RunPaused},
		mission: domain.Mission{ID: "mission-review", WorkspaceID: "workspace-review"},
		edits:   map[string]fileedit.Edit{edit.ID: edit},
	}
	store.approvals = map[string]approval.Record{
		edit.ID: fileEditReviewApproval(store.run, store.mission, edit),
	}
	service := NewFileEditReviewService(store)
	result, err := service.Review(context.Background(), ReviewFileEditRequest{
		Version: FileEditReviewProtocolVersion, RunID: store.run.ID,
		EditID: edit.ID, Action: FileEditApproveIntent,
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Edit.Status != fileedit.StatusApproved || result.Replayed ||
		store.edits[edit.ID].Status != fileedit.StatusApproved {
		t.Fatalf("unexpected review result: %#v", result)
	}
	replayed, err := service.Review(context.Background(), ReviewFileEditRequest{
		Version: FileEditReviewProtocolVersion, RunID: store.run.ID,
		EditID: edit.ID, Action: FileEditApproveIntent,
	})
	if err != nil || !replayed.Replayed {
		t.Fatalf("review replay failed: %#v %v", replayed, err)
	}
}

func TestFileEditReviewRecoversApprovalCommittedBeforeEditState(t *testing.T) {
	now := time.Now().UTC()
	edit := fileedit.Edit{ID: "edit-recover", SessionID: "session-recover",
		WorkspaceID: "workspace-recover", Path: "README.md", Status: fileedit.StatusProposed,
		ProposedText: "new\n", ProposedHash: fileedit.HashText("new\n"),
		OriginalHash: "missing", CreatedAt: now, UpdatedAt: now}
	store := &fileEditReviewMemoryStore{
		run: domain.Run{ID: "run-recover", MissionID: "mission-recover",
			SessionID: "session-recover", Status: domain.RunPaused},
		mission: domain.Mission{ID: "mission-recover", WorkspaceID: "workspace-recover"},
		edits:   map[string]fileedit.Edit{edit.ID: edit},
	}
	record := fileEditReviewApproval(store.run, store.mission, edit)
	record.Status = approval.StatusApproved
	record.ReviewedBy = "file_edit_review"
	record.Version++
	record.UpdatedAt = now.Add(time.Second)
	record.DecidedAt = &record.UpdatedAt
	store.approvals = map[string]approval.Record{edit.ID: record}
	store.run.Status = domain.RunCompleted

	result, err := NewFileEditReviewService(store).Review(context.Background(),
		ReviewFileEditRequest{Version: FileEditReviewProtocolVersion,
			RunID: store.run.ID, EditID: edit.ID, Action: FileEditApproveIntent})
	if err != nil {
		t.Fatal(err)
	}
	if !result.Replayed || result.Edit.Status != fileedit.StatusApproved ||
		store.edits[edit.ID].Status != fileedit.StatusApproved {
		t.Fatalf("approval recovery failed: %#v", result)
	}
}

func TestFileEditReviewRejectsCrossRunAndTerminalMutation(t *testing.T) {
	now := time.Now().UTC()
	edit := fileedit.Edit{ID: "edit-review", SessionID: "other-session",
		WorkspaceID: "workspace-review", Path: "README.md", Status: fileedit.StatusProposed,
		ProposedText: "new\n", ProposedHash: fileedit.HashText("new\n"),
		CreatedAt: now, UpdatedAt: now}
	store := &fileEditReviewMemoryStore{
		run: domain.Run{ID: "run-review", MissionID: "mission-review",
			SessionID: "session-review", Status: domain.RunPaused},
		mission: domain.Mission{ID: "mission-review", WorkspaceID: "workspace-review"},
		edits:   map[string]fileedit.Edit{edit.ID: edit},
	}
	store.approvals = map[string]approval.Record{
		edit.ID: fileEditReviewApproval(store.run, store.mission, edit),
	}
	service := NewFileEditReviewService(store)
	request := ReviewFileEditRequest{Version: FileEditReviewProtocolVersion,
		RunID: store.run.ID, EditID: edit.ID, Action: FileEditDeny}
	if _, err := service.Review(context.Background(), request); err == nil {
		t.Fatal("cross-Run file edit was accepted")
	}
	edit.SessionID = store.run.SessionID
	store.edits[edit.ID] = edit
	store.run.Status = domain.RunCompleted
	if _, err := service.Review(context.Background(), request); err == nil {
		t.Fatal("terminal Run file edit mutation was accepted")
	}
}
