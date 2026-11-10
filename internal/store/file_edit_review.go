package store

import (
	"context"
	"time"

	"cyberagent-workbench/internal/apperror"
	"cyberagent-workbench/internal/approval"
	"cyberagent-workbench/internal/fileedit"
)

// CommitFileEditReview publishes the exact operator decision and file review
// together, so a workflow cannot consume the decision before the edit is ready.
func (s *SQLiteStore) CommitFileEditReview(ctx context.Context, expected fileedit.Edit, expectedApproval approval.Record, request approval.DecisionRequest) (fileedit.Edit, bool, error) {
	request, err := request.Normalize()
	if err != nil {
		return fileedit.Edit{}, false, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fileedit.Edit{}, false, err
	}
	defer tx.Rollback()
	conflict := func() (fileedit.Edit, bool, error) {
		return fileedit.Edit{}, false, apperror.New(apperror.CodeConflict, "file edit review proposal or approval changed")
	}
	current, err := scanFileEdit(tx.QueryRowContext(ctx, `SELECT id,session_id,workspace_id,path,operation_kind,destination_path,status,original_text,proposed_text,diff_text,original_hash,proposed_hash,destination_original_hash,destination_proposed_hash,reason,secrets_redacted,created_at,updated_at FROM file_edits WHERE id=?`, expected.ID))
	if err != nil {
		return fileedit.Edit{}, false, err
	}
	immutable := current
	immutable.Status, immutable.Reason, immutable.UpdatedAt = expected.Status, expected.Reason, expected.UpdatedAt
	immutable.CreatedAt = expected.CreatedAt
	if !current.CreatedAt.Equal(expected.CreatedAt) || immutable != expected || request.ProposalID != current.ID || request.IdempotencyKey != approval.ReviewIdempotencyKey(fileedit.ApprovalToolName(current), current.ID, request.Action) || request.ReviewedBy != "file_edit_review" {
		return conflict()
	}
	record, err := getApprovalTx(ctx, tx, "", current.ID)
	if err != nil {
		return fileedit.Edit{}, false, err
	}
	if approvalIdentityMatches(record, expectedApproval) != nil || record.ID != expectedApproval.ID || record.RunID != expectedApproval.RunID || record.RunID == "" || record.ProposalID != current.ID || record.SessionID != current.SessionID || record.WorkspaceID != current.WorkspaceID || record.ToolName != fileedit.ApprovalToolName(current) || record.ActionClass != "workspace_write" || record.RequestFingerprint != fileedit.ApprovalFingerprint(current.SessionID, current.WorkspaceID, current) {
		return conflict()
	}
	desired, _ := request.Action.Status()
	if snapshot, found, err := getFileEditReviewSnapshot(ctx, tx, record.RunID, current.ID); err != nil {
		return fileedit.Edit{}, false, err
	} else if found {
		if record.Status != desired || (snapshot.Status == fileedit.StatusApproved) != (desired == approval.StatusApproved) {
			return conflict()
		}
		return snapshot, true, nil
	}
	if current.Status != fileedit.StatusProposed || expected.Status != fileedit.StatusProposed || !current.UpdatedAt.Equal(expected.UpdatedAt) || record.Version != expectedApproval.Version || record.Status != expectedApproval.Status || (record.Status != approval.StatusPending && record.Status != desired) {
		return conflict()
	}
	run, _, err := getCoordinatorRunTx(ctx, tx, record.RunID)
	if err != nil {
		return fileedit.Edit{}, false, err
	}
	if record.Status == approval.StatusPending && run.Terminal() {
		return fileedit.Edit{}, false, apperror.New(apperror.CodeFailedPrecondition, "terminal Run file edits cannot be changed")
	}
	prepared, err := fileedit.PrepareReview(current, request.Action == approval.ActionApprove, request.Reason, time.Now().UTC())
	if err != nil {
		return fileedit.Edit{}, false, err
	}
	decision, err := decideApprovalTx(ctx, tx, request)
	if err != nil {
		return fileedit.Edit{}, false, err
	}
	result, err := tx.ExecContext(ctx, `UPDATE file_edits SET status=?,reason=?,updated_at=? WHERE id=? AND status='proposed' AND updated_at=?`, prepared.Status, prepared.Reason, ts(prepared.UpdatedAt), current.ID, ts(current.UpdatedAt))
	if err != nil {
		return fileedit.Edit{}, false, err
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return fileedit.Edit{}, false, err
	}
	if changed != 1 {
		return conflict()
	}
	if err = projectFileEditTx(ctx, tx, prepared, current.Status, true); err != nil {
		return fileedit.Edit{}, false, err
	}
	if err = syncFileEditApprovalTx(ctx, tx, prepared, current.Status, true); err != nil {
		return fileedit.Edit{}, false, err
	}
	if err = tx.Commit(); err != nil {
		return fileedit.Edit{}, false, err
	}
	return prepared, decision.Replayed, nil
}
