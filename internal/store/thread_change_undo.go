package store

import (
	"context"
	"database/sql"
	"strings"
	"time"

	"cyberagent-workbench/internal/apperror"
	"cyberagent-workbench/internal/workspacecheckpoint"
)

func (s *SQLiteStore) ClaimThreadChangeUndo(ctx context.Context, t workspacecheckpoint.Transaction) (workspacecheckpoint.Transaction, bool, error) {
	if !workspacecheckpoint.IsThreadUndo(t) || t.Status != workspacecheckpoint.TransactionPrepared {
		return t, false, apperror.New(apperror.CodeConflict, "Thread undo claim is invalid")
	}
	now := time.Now().UTC()
	result, err := s.db.ExecContext(ctx, `UPDATE workspace_checkpoint_transactions SET status='applying',updated_at=?
	 WHERE id=? AND operation_key_digest=? AND request_fingerprint=? AND status='prepared' AND updated_at=?`, ts(now), t.ID, t.OperationKeyDigest, t.RequestFingerprint, ts(t.UpdatedAt))
	if err != nil {
		return t, false, err
	}
	n, err := result.RowsAffected()
	if err != nil {
		return t, false, err
	}
	current, _, err := s.GetWorkspaceCheckpointTransaction(ctx, t.ID)
	return current, n == 1, err
}

func (s *SQLiteStore) CheckThreadChangeUndoIdle(ctx context.Context, threadID, runID, ownTransaction string) error {
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return err
	}
	defer tx.Rollback()
	return requireThreadChangeUndoIdleTx(ctx, tx, threadID, runID, ownTransaction)
}

// The same check is repeated by journal insertion in its write transaction.
// Reservations fence execution on every Run sharing the physical Workspace.
func requireThreadChangeUndoIdleTx(ctx context.Context, tx *sql.Tx, threadID, runID, ownTransaction string) error {
	var workspaceID string
	if err := tx.QueryRowContext(ctx, `SELECT COALESCE(owner.workspace_id,mission.workspace_id)
	 FROM threads thread JOIN thread_runs binding ON binding.thread_id=thread.id JOIN runs run ON run.id=binding.run_id
	 JOIN missions mission ON mission.id=run.mission_id
	 LEFT JOIN run_file_drydock_bindings owner ON owner.run_id=run.id
	 WHERE thread.id=? AND binding.run_id=? AND thread.status='active'`, threadID, runID).Scan(&workspaceID); err != nil {
		return err
	}
	var busy bool
	if err := tx.QueryRowContext(ctx, `WITH shared AS (
	 SELECT run.id FROM runs run JOIN missions mission ON mission.id=run.mission_id
	 LEFT JOIN run_file_drydock_bindings owner ON owner.run_id=run.id
	 WHERE COALESCE(owner.workspace_id,mission.workspace_id)=?
	 ) SELECT
	 EXISTS(SELECT 1 FROM run_execution_leases lease JOIN shared ON shared.id=lease.run_id
	  WHERE lease.status='active' AND julianday(lease.expires_at)>julianday(?))
	 OR EXISTS(SELECT 1 FROM workspace_checkpoint_transactions WHERE workspace_id=? AND id<>? AND status IN ('prepared','applying'))
	 OR EXISTS(SELECT 1 FROM command_runtime_jobs job JOIN shared ON shared.id=job.run_id WHERE job.state IN ('prepared','running','stopping') OR job.tree_reaped<>1)
	 OR EXISTS(SELECT 1 FROM run_supervisor_tool_calls tool JOIN shared ON shared.id=tool.run_id WHERE tool.status IN ('pending','running'))
	 OR EXISTS(SELECT 1 FROM terminal_sessions terminal JOIN shared ON shared.id=terminal.run_id WHERE terminal.state IN ('starting','running'))
	 OR EXISTS(SELECT 1 FROM operator_steering_deliveries input JOIN shared ON shared.id=input.run_id WHERE input.status='prepared')
	 OR EXISTS(SELECT 1 FROM drydock_cleanup_operations cleanup JOIN drydock_workspaces d ON d.id=cleanup.drydock_id WHERE d.workspace_id=? AND cleanup.status='prepared')`,
		workspaceID, ts(time.Now().UTC()), workspaceID, ownTransaction, workspaceID).Scan(&busy); err != nil {
		return err
	}
	if busy {
		return apperror.New(apperror.CodeConflict, "Thread change undo requires an idle working directory")
	}
	return requireQuiescentRunPauseTx(ctx, tx, runID)
}

func requireThreadChangeUndoJournalTx(ctx context.Context, tx *sql.Tx, value workspacecheckpoint.Transaction) error {
	var threadID string
	sourceID := strings.TrimPrefix(value.TriggerReceiptID, workspacecheckpoint.ThreadUndoReceiptPrefix)
	if err := tx.QueryRowContext(ctx, `SELECT thread.id FROM threads thread
	 JOIN thread_runs binding ON binding.thread_id=thread.id
	 JOIN run_events event ON event.run_id=binding.run_id
	 WHERE EXISTS(SELECT 1 FROM thread_runs target JOIN runs target_run ON target_run.id=target.run_id JOIN runs source_run ON source_run.id=event.run_id WHERE target.thread_id=thread.id AND target.run_id=? AND target_run.mission_id=source_run.mission_id) AND event.event_id=? AND event.type='session.message_created'
	 AND event.source='session_store' AND json_extract(event.payload_json,'$.role')='assistant'
	 AND json_extract(event.payload_json,'$.source_kind')='model_response'`, value.RunID, sourceID).Scan(&threadID); err != nil {
		return err
	}
	return requireThreadChangeUndoIdleTx(ctx, tx, threadID, value.RunID, "")
}
