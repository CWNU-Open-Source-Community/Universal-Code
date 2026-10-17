package store

import (
	"context"
	"database/sql"

	"cyberagent-workbench/internal/apperror"
	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/fileedit"
	"cyberagent-workbench/internal/session"
)

// CreateFileEditIfAbsent is the insert-only boundary for a prepared interactive
// proposal. A retry must never rewrite a decision, file body, or apply status.
func (s *SQLiteStore) CreateFileEditIfAbsent(ctx context.Context, edit fileedit.Edit) (fileedit.Edit, bool, error) {
	edit, err := normalizeStoredFileEdit(edit)
	if err != nil {
		return fileedit.Edit{}, false, err
	}
	if edit.Status != fileedit.StatusProposed {
		return fileedit.Edit{}, false, apperror.New(apperror.CodeInvalidArgument,
			"new file edit must be proposed")
	}
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{})
	if err != nil {
		return fileedit.Edit{}, false, err
	}
	defer func() { _ = tx.Rollback() }()
	// INSERT acquires the SQLite writer fence before reading a possible replay.
	result, err := tx.ExecContext(ctx, `INSERT INTO file_edits
		(id, session_id, workspace_id, path, operation_kind, destination_path, status,
		 original_text, proposed_text, diff_text, original_hash, proposed_hash,
		 destination_original_hash, destination_proposed_hash,
		 reason, secrets_redacted, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(id) DO NOTHING`,
		edit.ID, edit.SessionID, edit.WorkspaceID, edit.Path, edit.Operation,
		edit.DestinationPath, edit.Status, edit.OriginalText, edit.ProposedText,
		edit.Diff, edit.OriginalHash, edit.ProposedHash, edit.DestinationOriginalHash,
		edit.DestinationProposedHash, edit.Reason, boolInt(edit.SecretsRedacted),
		ts(edit.CreatedAt), ts(edit.UpdatedAt))
	if err != nil {
		return fileedit.Edit{}, false, err
	}
	inserted, err := result.RowsAffected()
	if err != nil {
		return fileedit.Edit{}, false, err
	}
	if inserted == 0 {
		existing, readErr := scanFileEdit(tx.QueryRowContext(ctx, `SELECT id, session_id,
			workspace_id, path, operation_kind, destination_path, status, original_text,
			proposed_text, diff_text, original_hash, proposed_hash, destination_original_hash,
			destination_proposed_hash, reason, secrets_redacted, created_at, updated_at
			FROM file_edits WHERE id = ?`, edit.ID))
		if readErr != nil {
			return fileedit.Edit{}, false, readErr
		}
		if !fileedit.SameProposalContent(existing, edit) {
			return fileedit.Edit{}, false, apperror.New(apperror.CodeConflict,
				"file edit proposal identity already belongs to different content")
		}
		return existing, true, tx.Commit()
	}
	// A new interactive proposal must still be bound to a live Run in this
	// transaction. Existing records above remain readable after that Run ends.
	var runStatus, sessionStatus string
	runSessionID := edit.SessionID
	if _, rootSession, child, err := specialistFileEditBindingTx(ctx, tx, edit.SessionID, true); err != nil {
		return fileedit.Edit{}, false, err
	} else if child {
		runSessionID = rootSession
	}
	if err := tx.QueryRowContext(ctx, `SELECT r.status, s.status
		FROM runs r JOIN sessions s ON s.id = ?
		JOIN missions m ON m.id = r.mission_id
		WHERE r.session_id = ? AND s.workspace_id = m.workspace_id`, edit.SessionID, runSessionID).
		Scan(&runStatus, &sessionStatus); err != nil {
		return fileedit.Edit{}, false, err
	}
	if runStatus != string(domain.RunRunning) || sessionStatus != string(session.StatusActive) {
		return fileedit.Edit{}, false, apperror.New(apperror.CodeFailedPrecondition,
			"new file edit proposal requires a running Run and active Session")
	}
	if err := requireFileEditWorkspaceTx(ctx, tx, edit.SessionID, edit.WorkspaceID, true); err != nil {
		return fileedit.Edit{}, false, err
	}
	if err := projectFileEditTx(ctx, tx, edit, "", false); err != nil {
		return fileedit.Edit{}, false, err
	}
	if err := syncFileEditApprovalTx(ctx, tx, edit, "", false); err != nil {
		return fileedit.Edit{}, false, err
	}
	return edit, false, tx.Commit()
}
