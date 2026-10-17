package store

import (
	"context"
	"database/sql"
	"errors"

	"cyberagent-workbench/internal/apperror"
	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/events"
	"cyberagent-workbench/internal/llm"
)

type projectHookReader interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

func enrichProjectHookRounds(ctx context.Context, reader projectHookReader, rounds []domain.SupervisorToolRound) error {
	var exists int
	if err := reader.QueryRowContext(ctx, `SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='project_hook_calls'`).Scan(&exists); err != nil || exists == 0 {
		return err
	}
	for i := range rounds {
		for j := range rounds[i].Calls {
			call := &rounds[i].Calls[j]
			var origin domain.ProjectHookCallOrigin
			var source string
			err := reader.QueryRowContext(ctx, `SELECT r.origin,COALESCE(h.trigger_key,''),COALESCE(h.slot,''),COALESCE(h.target,''),COALESCE(h.config_fingerprint,''),COALESCE(h.workspace_version,'')
 FROM run_supervisor_tool_rounds r LEFT JOIN project_hook_calls h ON h.run_id=r.run_id AND h.turn=r.turn AND h.attempt_id=r.attempt_id AND h.call_id=?
 WHERE r.run_id=? AND r.turn=? AND r.attempt_id=? AND r.round=?`, call.CallID, call.RunID, call.Turn, call.AttemptID, call.Round).
				Scan(&source, &origin.TriggerKey, &origin.Slot, &origin.Target, &origin.ConfigFingerprint, &origin.WorkspaceVersion)
			if err != nil {
				return err
			}
			if source == "model" && origin.TriggerKey == "" {
				continue
			}
			if source != "project_hook" || origin.TriggerKey == "" {
				return apperror.New(apperror.CodeFailedPrecondition, "project hook source binding is incomplete")
			}

			if err := origin.Validate(); err != nil {
				return err
			}
			call.ProjectHook = &origin
		}
	}
	return nil
}

func (s *SQLiteStore) GetProjectHookCall(ctx context.Context, runID, key string) (domain.SupervisorToolCall, bool, error) {
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return domain.SupervisorToolCall{}, false, err
	}
	defer tx.Rollback()
	var cp domain.SupervisorCheckpoint
	var callID string
	err = tx.QueryRowContext(ctx, `SELECT run_id,turn,attempt_id,call_id FROM project_hook_calls WHERE run_id=? AND trigger_key=?`, runID, key).Scan(&cp.RunID, &cp.NextTurn, &cp.AttemptID, &callID)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.SupervisorToolCall{}, false, nil
	}
	if err != nil {
		return domain.SupervisorToolCall{}, false, err
	}
	call, err := getSupervisorToolCallTx(ctx, tx, cp, callID)
	if err != nil {
		return call, false, err
	}
	rounds := []domain.SupervisorToolRound{{Calls: []domain.SupervisorToolCall{call}}}
	if err := enrichProjectHookRounds(ctx, tx, rounds); err != nil {
		return call, false, err
	}
	return rounds[0].Calls[0], true, tx.Commit()
}

// RecordProjectHookCall creates no model response or usage. ModelAttempt on the
// execution ledger identifies the real preceding output and its steering fence.
func (s *SQLiteStore) RecordProjectHookCall(ctx context.Context, cp domain.SupervisorCheckpoint,
	call llm.ToolCall, origin domain.ProjectHookCallOrigin,
) error {
	if err := origin.Validate(); err != nil {
		return err
	}
	if call.Name != "command_runtime" && call.Name != "run_tests" {
		return apperror.New(apperror.CodeInvalidArgument, "project hook must use a project command")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	run, current, err := requireActiveSupervisorAttemptTx(ctx, tx, cp)
	if err != nil {
		return err
	}
	var exists int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM project_hook_calls WHERE run_id=? AND trigger_key=?`, cp.RunID, origin.TriggerKey).Scan(&exists); err != nil {
		return err
	}
	if exists != 0 {
		return tx.Commit()
	}
	number, kind, err := latestSupervisorPrimaryModelTx(ctx, tx, current)
	if err != nil {
		return err
	}
	if number <= 0 || kind != events.ModelCompletedEvent {
		return apperror.New(apperror.CodeFailedPrecondition, "project hook requires a completed model boundary")
	}
	var previous int
	if err := tx.QueryRowContext(ctx, `SELECT COALESCE(MAX(round),0) FROM run_supervisor_tool_rounds WHERE run_id=? AND turn=? AND attempt_id=?`, cp.RunID, cp.NextTurn, cp.AttemptID).Scan(&previous); err != nil {
		return err
	}
	if err := insertSupervisorToolRoundSourceTx(ctx, tx, run, current, llm.ModelAttempt{Number: number, ToolRound: previous}, []llm.ToolCall{call}, nil, "project_hook"); err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO project_hook_calls(run_id,turn,attempt_id,call_id,trigger_key,slot,target,config_fingerprint,workspace_version) VALUES(?,?,?,?,?,?,?,?,?)`, cp.RunID, cp.NextTurn, cp.AttemptID, call.ID, origin.TriggerKey, origin.Slot, origin.Target, origin.ConfigFingerprint, origin.WorkspaceVersion)
	if err != nil {
		return err
	}
	return tx.Commit()
}

// Older schemas contain only native rounds. New schemas retain their original
// native budget independently of the physical execution sequence.
func supervisorNativeToolRoundCountTx(ctx context.Context, tx *sql.Tx, cp domain.SupervisorCheckpoint, completed bool) (int, error) {
	var hasOrigin int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM pragma_table_info('run_supervisor_tool_rounds') WHERE name='origin'`).Scan(&hasOrigin); err != nil {
		return 0, err
	}
	query := `SELECT COUNT(*) FROM run_supervisor_tool_rounds WHERE run_id=? AND turn=? AND attempt_id=?`
	if hasOrigin != 0 {
		query += ` AND origin='model'`
	}
	if completed {
		query += ` AND completed_at IS NOT NULL`
	}
	var count int
	err := tx.QueryRowContext(ctx, query, cp.RunID, cp.NextTurn, cp.AttemptID).Scan(&count)
	return count, err
}
