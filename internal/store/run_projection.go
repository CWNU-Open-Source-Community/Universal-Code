package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"cyberagent-workbench/internal/events"
	"cyberagent-workbench/internal/fileedit"
	"cyberagent-workbench/internal/policy"
	"cyberagent-workbench/internal/session"
	"cyberagent-workbench/internal/toolrun"
)

type runBinding struct {
	RunID       string
	MissionID   string
	WorkspaceID string
}

func runBindingForSessionTx(ctx context.Context, tx *sql.Tx, sessionID string) (runBinding, bool, error) {
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return runBinding{}, false, nil
	}
	var binding runBinding
	err := tx.QueryRowContext(ctx, `SELECT runs.id, runs.mission_id, missions.workspace_id
		FROM runs JOIN missions ON missions.id = runs.mission_id WHERE runs.session_id = ?`, sessionID).
		Scan(&binding.RunID, &binding.MissionID, &binding.WorkspaceID)
	if errors.Is(err, sql.ErrNoRows) {
		return runBinding{}, false, nil
	}
	if err != nil {
		return runBinding{}, false, err
	}
	return binding, true, nil
}

func requireRunWorkspaceForSessionTx(ctx context.Context, tx *sql.Tx, sessionID string, workspaceID string) error {
	binding, ok, err := runBindingForSessionTx(ctx, tx, sessionID)
	if err != nil || !ok {
		return err
	}
	if strings.TrimSpace(binding.WorkspaceID) != strings.TrimSpace(workspaceID) {
		return errors.New("activity workspace does not match the attached run")
	}
	return nil
}

func appendRunEventForSessionTx(ctx context.Context, tx *sql.Tx, sessionID string, eventType string, source string, subjectID string, payload any) error {
	binding, ok, err := runBindingForSessionTx(ctx, tx, sessionID)
	if err != nil || !ok {
		return err
	}
	event, err := events.New(binding.RunID, binding.MissionID, eventType, source, subjectID, payload)
	if err != nil {
		return err
	}
	_, err = insertRunEventTx(ctx, tx, event)
	return err
}

func projectSessionMessageTx(ctx context.Context, tx *sql.Tx, message session.Message) error {
	return appendRunEventForSessionTx(ctx, tx, message.SessionID, events.SessionMessageEvent, "session_store", fmt.Sprint(message.ID), map[string]any{
		"session_id":             message.SessionID,
		"message_id":             message.ID,
		"role":                   message.Role,
		"content":                message.Content,
		"provenance_version":     message.Provenance.Version,
		"source_kind":            message.Provenance.SourceKind,
		"source_ref":             message.Provenance.SourceRef,
		"content_sha256":         message.Provenance.ContentSHA256,
		"instruction_authorized": message.Provenance.InstructionAuthorized,
		"token_estimate":         message.TokenEstimate,
		"compacted":              message.Compacted,
	})
}

func projectToolRunTx(ctx context.Context, tx *sql.Tx, run toolrun.ToolRun, previousStatus string, existed bool) error {
	if existed && previousStatus == run.Status {
		return nil
	}
	if err := requireRunWorkspaceForSessionTx(ctx, tx, run.SessionID, run.WorkspaceID); err != nil {
		return err
	}
	if !existed {
		allowed := run.Status != toolrun.StatusDenied
		if err := appendRunEventForSessionTx(ctx, tx, run.SessionID, events.PolicyDecisionEvent, "policy", run.ID, map[string]any{
			"context":        "tool_run.shell",
			"allowed":        allowed,
			"needs_approval": allowed,
			"risk":           run.Risk,
			"reason":         run.PolicyReason,
		}); err != nil {
			return err
		}
	}
	eventType, ok := toolRunEventType(run.Status)
	if !ok {
		return fmt.Errorf("invalid tool run status %q", run.Status)
	}
	payload := map[string]any{
		"session_id":      run.SessionID,
		"workspace_id":    run.WorkspaceID,
		"tool_name":       run.ToolName,
		"command":         run.Command,
		"status":          run.Status,
		"previous_status": previousStatus,
		"risk":            run.Risk,
		"reason":          run.PolicyReason,
		"exit_code":       run.ExitCode,
	}
	if run.Status == toolrun.StatusCompleted || run.Status == toolrun.StatusFailed {
		payload["stdout"] = run.Stdout
		payload["stderr"] = run.Stderr
	}
	return appendRunEventForSessionTx(ctx, tx, run.SessionID, eventType, "toolrun_store", run.ID, payload)
}

func toolRunEventType(status string) (string, bool) {
	switch status {
	case toolrun.StatusProposed:
		return events.ToolProposedEvent, true
	case toolrun.StatusApproved:
		return events.ToolApprovedEvent, true
	case toolrun.StatusDenied:
		return events.ToolDeniedEvent, true
	case toolrun.StatusRunning:
		return events.ToolStartedEvent, true
	case toolrun.StatusCompleted:
		return events.ToolCompletedEvent, true
	case toolrun.StatusFailed:
		return events.ToolFailedEvent, true
	default:
		return "", false
	}
}

func projectFileEditTx(ctx context.Context, tx *sql.Tx, edit fileedit.Edit, previousStatus string, existed bool) error {
	return projectFileEditWithAuthorizationSourceTx(ctx, tx, edit, previousStatus, existed, "")
}

func projectFileEditWithAuthorizationSourceTx(ctx context.Context, tx *sql.Tx,
	edit fileedit.Edit, previousStatus string, existed bool, authorizationSource string,
) error {
	if existed && previousStatus == edit.Status {
		return nil
	}
	if err := requireFileEditWorkspaceTx(ctx, tx, edit.SessionID, edit.WorkspaceID,
		!existed || edit.Status == fileedit.StatusApproved || edit.Status == fileedit.StatusApplied); err != nil {
		return err
	}
	eventType, ok := fileEditEventType(edit.Status)
	if !ok {
		return fmt.Errorf("invalid file edit status %q", edit.Status)
	}
	payload := map[string]any{
		"session_id":       edit.SessionID,
		"workspace_id":     edit.WorkspaceID,
		"path":             edit.Path,
		"operation":        edit.Operation,
		"destination_path": edit.DestinationPath,
		"status":           edit.Status,
		"previous_status":  previousStatus,
		"reason":           edit.Reason,
		"secrets_redacted": edit.SecretsRedacted,
	}
	if edit.Status == fileedit.StatusApproved || edit.Status == fileedit.StatusDenied {
		// Bodies remain in the immutable proposal; seal only the exact review
		// projection so later application cannot turn approval replay into a
		// different decision or a newly fabricated current-state snapshot.
		snapshot := edit
		snapshot.OriginalText, snapshot.ProposedText, snapshot.Diff = "", "", ""
		payload["review_snapshot"] = snapshot
	}
	if authorizationSource != "" {
		payload["authorization_source"] = authorizationSource
	}
	if edit.Status == fileedit.StatusProposed {
		payload["diff"] = edit.Diff
		payload["original_hash"] = edit.OriginalHash
		payload["proposed_hash"] = edit.ProposedHash
		payload["destination_original_hash"] = edit.DestinationOriginalHash
		payload["destination_proposed_hash"] = edit.DestinationProposedHash
	}
	return appendFileEditRunEventForSessionTx(ctx, tx, edit.SessionID, eventType, "fileedit_store", edit.ID, payload)
}

func fileEditEventType(status string) (string, bool) {
	switch status {
	case fileedit.StatusProposed:
		return events.FileEditProposedEvent, true
	case fileedit.StatusApproved:
		return events.FileEditApprovedEvent, true
	case fileedit.StatusApplied:
		return events.FileEditAppliedEvent, true
	case fileedit.StatusDenied:
		return events.FileEditDeniedEvent, true
	case fileedit.StatusFailed:
		return events.FileEditFailedEvent, true
	default:
		return "", false
	}
}

func (s *SQLiteStore) RecordPolicyDecision(ctx context.Context, record policy.DecisionRecord) error {
	if err := record.Validate(); err != nil {
		return err
	}
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{})
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if err := appendRunEventForSessionTx(ctx, tx, record.SessionID, events.PolicyDecisionEvent, "policy", record.SubjectID, map[string]any{
		"context":        record.Context,
		"allowed":        record.Decision.Allowed,
		"needs_approval": record.Decision.NeedsApproval,
		"risk":           record.Decision.Risk,
		"reason":         record.Decision.Reason,
	}); err != nil {
		return err
	}
	return tx.Commit()
}
