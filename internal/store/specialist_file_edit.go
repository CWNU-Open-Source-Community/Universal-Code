package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"

	"cyberagent-workbench/internal/approval"
	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/events"
	"cyberagent-workbench/internal/fileedit"
)

func (s *SQLiteStore) GetSpecialistFileEditOwner(ctx context.Context, runID, editID string) (domain.AgentNode, error) {
	return scanAgentNode(s.db.QueryRowContext(ctx, agentNodeSelect+` WHERE role='specialist' AND run_id=? AND session_id=(SELECT session_id FROM file_edits WHERE id=?)`, runID, editID))
}

func specialistFileEditBindingTx(ctx context.Context, tx *sql.Tx, sessionID string, current bool) (runBinding, string, bool, error) {
	var available bool
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM pragma_table_info('agent_nodes') WHERE name='worker_profile_json')`).Scan(&available); err != nil {
		return runBinding{}, "", false, err
	}
	if !available {
		return runBinding{}, "", false, nil
	}
	var b runBinding
	var rootSession string
	var active bool
	query := `SELECT r.id,r.mission_id,m.workspace_id,r.session_id,(child.status='running' AND child.worker_profile_json<>'null') FROM agent_nodes child JOIN runs r ON r.id=child.run_id JOIN missions m ON m.id=r.mission_id WHERE child.session_id=? AND child.role='specialist'`
	err := tx.QueryRowContext(ctx, query, sessionID).Scan(&b.RunID, &b.MissionID, &b.WorkspaceID, &rootSession, &active)
	if errors.Is(err, sql.ErrNoRows) {
		return b, "", false, nil
	}
	if err == nil && current && !active {
		return b, rootSession, true, errors.New("worker FileEdit owner is no longer active")
	}
	return b, rootSession, err == nil, err
}

func appendFileEditRunEventForSessionTx(ctx context.Context, tx *sql.Tx, sessionID, eventType, source, subjectID string, payload any) error {
	b, bound, err := runBindingForSessionTx(ctx, tx, sessionID)
	if err != nil {
		return err
	}
	if !bound {
		b, _, bound, err = specialistFileEditBindingTx(ctx, tx, sessionID, false)
	}
	if err != nil || !bound {
		return err
	}
	event, err := events.New(b.RunID, b.MissionID, eventType, source, subjectID, payload)
	if err != nil {
		return err
	}
	_, err = insertRunEventTx(ctx, tx, event)
	return err
}

func specialistFileEditApprovalMatchesTx(ctx context.Context, tx *sql.Tx, c domain.SpecialistToolCall, record approval.Record) error {
	if c.Name != "workspace_apply" && c.Name != "workspace_delete" {
		return errors.New("child tool has no file proposal approval")
	}
	var payload struct {
		EditID string `json:"edit_id"`
	}
	if json.Unmarshal([]byte(c.PayloadJSON), &payload) != nil || payload.EditID == "" || record.ProposalID != payload.EditID || record.ActionClass != "workspace_write" {
		return errors.New("child file review differs from its exact tool intent")
	}
	edit, err := scanFileEdit(tx.QueryRowContext(ctx, `SELECT id,session_id,workspace_id,path,operation_kind,destination_path,status,original_text,proposed_text,diff_text,original_hash,proposed_hash,destination_original_hash,destination_proposed_hash,reason,secrets_redacted,created_at,updated_at FROM file_edits WHERE id=?`, payload.EditID))
	if err != nil {
		return err
	}
	var childSession string
	if err = tx.QueryRowContext(ctx, `SELECT session_id FROM agent_nodes WHERE id=? AND run_id=? AND role='specialist'`, c.Ref.AgentID, c.Ref.RunID).Scan(&childSession); err != nil {
		return err
	}
	if record.RunID != c.Ref.RunID || record.SessionID != childSession || edit.SessionID != childSession || record.WorkspaceID != edit.WorkspaceID || record.ToolName != fileedit.ApprovalToolName(edit) || record.RequestFingerprint != fileedit.ApprovalFingerprint(edit.SessionID, edit.WorkspaceID, edit) {
		return errors.New("child file approval ownership changed")
	}
	return nil
}

func (s *SQLiteStore) SetSpecialistFileEditApproval(ctx context.Context, ref domain.AgentAttemptRef, callID, approvalID string) (domain.SpecialistToolCall, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return domain.SpecialistToolCall{}, err
	}
	defer tx.Rollback()
	if _, _, _, err = loadActiveAgentAttemptTx(ctx, tx, ref); err != nil {
		return domain.SpecialistToolCall{}, err
	}
	c, err := getSpecialistToolCallTx(ctx, tx, ref, callID)
	if err != nil {
		return c, err
	}
	if _, err = validateSpecialistWorkerBindingTx(ctx, tx, ref, c.Name); err != nil {
		return c, err
	}
	if c.Status != domain.SupervisorToolPending || c.StartedAt != nil || c.AuthorityJSON == "" || (c.ApprovalID != "" && c.ApprovalID != approvalID) {
		return c, errors.New("child file approval call is no longer pending")
	}
	record, err := getApprovalTx(ctx, tx, approvalID, "")
	if err != nil {
		return c, err
	}
	if err = specialistFileEditApprovalMatchesTx(ctx, tx, c, record); err != nil {
		return c, err
	}
	c.ApprovalID = approvalID
	if _, err = tx.ExecContext(ctx, `UPDATE specialist_tool_calls SET approval_id=? WHERE agent_attempt_id=? AND call_id=? AND status='pending' AND started_at IS NULL`, approvalID, ref.AttemptID, callID); err != nil {
		return c, err
	}
	return c, tx.Commit()
}
