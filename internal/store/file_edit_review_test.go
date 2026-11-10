package store

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"cyberagent-workbench/internal/apperror"
	"cyberagent-workbench/internal/application"
	"cyberagent-workbench/internal/approval"
	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/fileedit"
	"cyberagent-workbench/internal/policy"
)

func atomicFileReviewFixture(t *testing.T) (*SQLiteStore, domain.Run, fileedit.Edit, approval.Record, approval.DecisionRequest) {
	t.Helper()
	st, err := Open(filepath.Join(t.TempDir(), "review.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	root := t.TempDir()
	if err := st.SaveWorkspace(t.Context(), WorkspaceRecord{ID: "atomic-review-workspace", Name: "review", RootPath: root}); err != nil {
		t.Fatal(err)
	}
	runs := application.NewRunService(st)
	_, run, err := runs.Create(t.Context(), application.CreateRunRequest{Goal: "review exact file intent", Profile: "code", WorkspaceID: "atomic-review-workspace", Budget: domain.Budget{MaxTurns: 10, MaxToolCalls: 10}})
	if err != nil {
		t.Fatal(err)
	}
	run, err = runs.Start(t.Context(), run.ID)
	if err != nil {
		t.Fatal(err)
	}
	edit, err := fileedit.NewManager(st).Propose(t.Context(), fileedit.Proposal{SessionID: run.SessionID, WorkspaceID: "atomic-review-workspace", WorkspaceRoot: root, Path: "output.txt", Operation: fileedit.OperationCreate, ExpectedOriginalHash: "missing", ProposedText: "reviewed output\n"})
	if err != nil {
		t.Fatal(err)
	}
	record, err := st.GetApprovalByProposal(t.Context(), edit.ID)
	if err != nil {
		t.Fatal(err)
	}
	request := approval.DecisionRequest{ProposalID: edit.ID, IdempotencyKey: approval.ReviewIdempotencyKey(fileedit.ApprovalToolName(edit), edit.ID, approval.ActionApprove), Action: approval.ActionApprove, ReviewedBy: "file_edit_review"}
	return st, run, edit, record, request
}

func TestCommitFileEditReviewRollsBackDecisionAndEventsOnEditWriteFailure(t *testing.T) {
	st, run, edit, observed, request := atomicFileReviewFixture(t)
	before, err := st.ListRunEvents(t.Context(), run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.db.ExecContext(t.Context(), `CREATE TRIGGER review_write_failure BEFORE UPDATE OF status ON file_edits BEGIN SELECT RAISE(ABORT,'injected review write failure'); END`); err != nil {
		t.Fatal(err)
	}
	if _, _, err := st.CommitFileEditReview(t.Context(), edit, observed, request); err == nil || !strings.Contains(err.Error(), "injected review write failure") {
		t.Fatalf("write fault was not reached after deciding: %v", err)
	}
	current, err := st.GetFileEdit(t.Context(), edit.ID)
	if err != nil || current.Status != fileedit.StatusProposed {
		t.Fatalf("failed transaction changed edit: %+v err=%v", current, err)
	}
	record, err := st.GetApprovalByProposal(t.Context(), edit.ID)
	if err != nil || record.Status != approval.StatusPending || record.Version != observed.Version {
		t.Fatalf("failed transaction published decision: %+v err=%v", record, err)
	}
	var operations int
	if err := st.db.QueryRowContext(t.Context(), `SELECT count(*) FROM approval_operations WHERE approval_id=?`, record.ID).Scan(&operations); err != nil || operations != 0 {
		t.Fatalf("failed transaction retained decision receipt: count=%d err=%v", operations, err)
	}
	after, err := st.ListRunEvents(t.Context(), run.ID)
	if err != nil || len(after) != len(before) {
		t.Fatalf("failed transaction retained events: before=%d after=%d err=%v", len(before), len(after), err)
	}
	if _, err := st.db.ExecContext(t.Context(), `DROP TRIGGER review_write_failure`); err != nil {
		t.Fatal(err)
	}
	if _, _, err := st.CommitFileEditReview(t.Context(), edit, observed, request); err != nil {
		t.Fatalf("same operation could not retry after rollback: %v", err)
	}
}

func TestCommitFileEditReviewFencesChangedProposalAndApproval(t *testing.T) {
	st, _, edit, observed, request := atomicFileReviewFixture(t)
	for _, kind := range []string{"proposal", "approval_version", "approval_id"} {
		t.Run(kind, func(t *testing.T) {
			changedEdit, changedApproval := edit, observed
			switch kind {
			case "proposal":
				changedEdit.ProposedText = "different bytes"
			case "approval_version":
				changedApproval.Version++
			case "approval_id":
				changedApproval.ID = "approval-other"
			}
			if _, _, err := st.CommitFileEditReview(t.Context(), changedEdit, changedApproval, request); apperror.CodeOf(err) != apperror.CodeConflict {
				t.Fatalf("changed %s was accepted: %v", kind, err)
			}
		})
	}
}

func TestCommitFileEditReviewReplaysOriginalSnapshotAfterApply(t *testing.T) {
	st, run, edit, observed, request := atomicFileReviewFixture(t)
	approved, replayed, err := st.CommitFileEditReview(t.Context(), edit, observed, request)
	if err != nil || replayed || approved.Status != fileedit.StatusApproved {
		t.Fatalf("first review: %+v replayed=%t err=%v", approved, replayed, err)
	}
	result, err := application.NewFileEditApplyService(st, policy.NewDefaultChecker()).Apply(t.Context(), application.ApplyFileEditRequest{Version: fileedit.FileEditApplyProtocolVersion, RunID: run.ID, EditID: edit.ID, OperationKey: "apply-after-atomic-review", AppliedBy: "operator"})
	if err != nil || result.Edit.Status != fileedit.StatusApplied {
		t.Fatalf("real file apply: %+v err=%v", result, err)
	}
	before, err := st.ListRunEvents(t.Context(), run.ID)
	if err != nil {
		t.Fatal(err)
	}
	for range 2 {
		review, replayed, err := st.CommitFileEditReview(t.Context(), edit, observed, request)
		if err != nil || !replayed || review != approved {
			t.Fatalf("review snapshot changed after apply: %+v replayed=%t err=%v", review, replayed, err)
		}
	}
	request.Action, request.Reason = approval.ActionDeny, "denied by operator"
	request.IdempotencyKey = approval.ReviewIdempotencyKey(fileedit.ApprovalToolName(edit), edit.ID, request.Action)
	if _, _, err := st.CommitFileEditReview(t.Context(), edit, observed, request); apperror.CodeOf(err) != apperror.CodeConflict {
		t.Fatalf("reverse decision was accepted: %v", err)
	}
	current, err := st.GetFileEdit(t.Context(), edit.ID)
	if err != nil || current.Status != fileedit.StatusApplied {
		t.Fatalf("replay changed applied state: %+v err=%v", current, err)
	}
	after, err := st.ListRunEvents(t.Context(), run.ID)
	if err != nil || len(after) != len(before) {
		t.Fatalf("replay or rejected reverse decision added events: before=%d after=%d err=%v", len(before), len(after), err)
	}
}

func TestCommitFileEditReviewRepairsPreviouslyCommittedDecision(t *testing.T) {
	for _, action := range []approval.Action{approval.ActionApprove, approval.ActionDeny} {
		t.Run(string(action), func(t *testing.T) {
			st, run, edit, _, request := atomicFileReviewFixture(t)
			request.Action = action
			request.IdempotencyKey = approval.ReviewIdempotencyKey(fileedit.ApprovalToolName(edit), edit.ID, action)
			serviceAction := application.FileEditApproveIntent
			if action == approval.ActionDeny {
				request.Reason = "denied by operator"
				serviceAction = application.FileEditDeny
			}
			if _, err := st.DecideApproval(context.Background(), request); err != nil {
				t.Fatal(err)
			}
			result, err := application.NewFileEditReviewService(st).Review(t.Context(), application.ReviewFileEditRequest{Version: application.FileEditReviewProtocolVersion, RunID: run.ID, EditID: edit.ID, Action: serviceAction})
			if err != nil || !result.Replayed || (result.Edit.Status == fileedit.StatusApproved) != (action == approval.ActionApprove) {
				t.Fatalf("existing decision did not settle through the same review: %+v err=%v", result, err)
			}
		})
	}
}
