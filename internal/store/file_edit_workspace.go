package store

import (
	"context"
	"database/sql"
	"errors"
	"strings"

	"cyberagent-workbench/internal/apperror"
)

// FileEditWorkspaceBelongsToRun checks durable ownership, including historical
// source edits and cleaned Drydocks. It is not authority for a new mutation.
func (s *SQLiteStore) FileEditWorkspaceBelongsToRun(ctx context.Context,
	runID, sessionID, workspaceID string,
) (bool, error) {
	ownerTable := "drydock_workspaces"
	if present, err := hasRunFileDrydockBindings(ctx, s.db); err != nil {
		return false, err
	} else if present {
		ownerTable = "run_file_drydock_bindings"
	}
	var owned bool
	err := s.db.QueryRowContext(ctx, `SELECT EXISTS (
		SELECT 1 FROM runs r JOIN missions m ON m.id = r.mission_id
		JOIN sessions se ON se.id = r.session_id
		WHERE r.id = ? AND (r.session_id = ? OR EXISTS(SELECT 1 FROM agent_nodes child WHERE child.run_id=r.id AND child.role='specialist' AND child.session_id=?)) AND se.workspace_id = m.workspace_id
		AND (m.workspace_id = ? OR EXISTS (SELECT 1 FROM `+ownerTable+` d
			WHERE d.run_id = r.id AND d.mission_id = m.id AND d.session_id = se.id
			AND d.source_workspace_id = m.workspace_id AND d.workspace_id = ?)))`,
		runID, sessionID, sessionID, workspaceID, workspaceID).Scan(&owned)
	return owned, err
}

func requireFileEditWorkspaceTx(ctx context.Context, tx *sql.Tx,
	sessionID, workspaceID string, current bool,
) error {
	binding, bound, err := runBindingForSessionTx(ctx, tx, sessionID)
	if err != nil {
		return err
	}
	if !bound {
		_, rootSession, child, err := specialistFileEditBindingTx(ctx, tx, sessionID, current)
		if err != nil {
			return err
		}
		if child {
			return requireFileEditWorkspaceTx(ctx, tx, rootSession, workspaceID, current)
		}
		return nil
	}
	workspaceID = strings.TrimSpace(workspaceID)
	if !current && binding.WorkspaceID == workspaceID {
		return nil
	}
	// Historical migration fixtures legitimately use the store before Drydock
	// exists. Preserve that source-only contract rather than querying future tables.
	var hasDrydocks bool
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM sqlite_master
		WHERE type='table' AND name='drydock_workspaces')`).Scan(&hasDrydocks); err != nil {
		return err
	}
	if !hasDrydocks {
		if workspaceID == binding.WorkspaceID {
			return nil
		}
		return errors.New("file edit workspace does not match the attached Run")
	}
	hasBindings, err := hasRunFileDrydockBindings(ctx, tx)
	if err != nil {
		return err
	}
	if current && hasBindings {
		if err := requireCurrentRunDrydockTx(ctx, tx, binding.RunID); err != nil {
			return err
		}
	}
	var target, state string
	targetQuery := `SELECT d.workspace_id,d.state FROM drydock_workspaces d JOIN sessions se ON se.id=d.session_id WHERE d.run_id=? AND d.mission_id=? AND d.session_id=? AND d.source_workspace_id=? AND se.workspace_id=d.source_workspace_id`
	if hasBindings {
		targetQuery = `SELECT owner.workspace_id,d.state FROM run_file_drydock_bindings owner JOIN drydock_workspaces d ON d.id=owner.drydock_id JOIN sessions se ON se.id=owner.session_id WHERE owner.run_id=? AND owner.mission_id=? AND owner.session_id=? AND owner.source_workspace_id=? AND se.workspace_id=owner.source_workspace_id`
	}
	err = tx.QueryRowContext(ctx, targetQuery, binding.RunID, binding.MissionID, sessionID, binding.WorkspaceID).Scan(&target, &state)
	if err == nil {
		if target == workspaceID && (!current || state == "ready" || state == "delivered") {
			return nil
		}
		return apperror.New(apperror.CodeFailedPrecondition,
			"file edit does not target this Run's available Drydock")
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	var ownsDrydock bool
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM drydock_workspaces
		WHERE run_id=?)`, binding.RunID).Scan(&ownsDrydock); err != nil {
		return err
	}
	if ownsDrydock {
		return apperror.New(apperror.CodeFailedPrecondition,
			"file edit Run Drydock identity is invalid")
	}
	var configured bool
	var hasPresets bool
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM sqlite_master
		WHERE type='table' AND name='standard_code_preset_operations')`).Scan(&hasPresets); err != nil {
		return err
	}
	if hasPresets {
		if err := tx.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM standard_code_preset_operations
			WHERE run_id=? AND status='configured')`, binding.RunID).Scan(&configured); err != nil {
			return err
		}
	}
	if !configured && workspaceID == binding.WorkspaceID {
		return nil
	}
	return apperror.New(apperror.CodeFailedPrecondition,
		"file edit Run workspace binding is unavailable")
}

func requireFileEditApprovalWorkspaceTx(ctx context.Context, tx *sql.Tx,
	sessionID, workspaceID, proposalID, toolName, actionClass string,
) error {
	var actualTool string
	err := tx.QueryRowContext(ctx, `SELECT CASE operation_kind
		WHEN 'create' THEN 'create_file' WHEN 'move' THEN 'move_file'
		WHEN 'delete' THEN 'delete_file' ELSE 'replace_file' END
		FROM file_edits WHERE id=? AND session_id=? AND workspace_id=?`,
		proposalID, sessionID, workspaceID).Scan(&actualTool)
	if err != nil || actualTool != toolName || actionClass != "workspace_write" {
		return errors.New("approval workspace does not match an owned file edit")
	}
	return requireFileEditWorkspaceTx(ctx, tx, sessionID, workspaceID, false)
}
