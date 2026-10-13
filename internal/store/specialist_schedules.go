package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"slices"
	"strings"

	"cyberagent-workbench/internal/apperror"
	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/events"
)

const specialistScheduleSelect = `SELECT id, run_id, lease_id, lease_generation,
	max_rounds, status, stop_reason, rounds_completed, turns_started, recovered_attempts,
	before_root_tokens, before_specialist_tokens, before_readonly_tokens, before_total_tokens,
	before_root_execution_millis, before_specialist_execution_millis,
	before_readonly_execution_millis, before_total_execution_millis,
	after_root_tokens, after_specialist_tokens, after_readonly_tokens,
	after_total_tokens, after_root_execution_millis, after_specialist_execution_millis,
	after_readonly_execution_millis, after_total_execution_millis,
	error_code, started_at, finished_at, continuation_digest
	FROM specialist_schedules`

type specialistScheduleRecord struct {
	Schedule        domain.SpecialistSchedule
	LeaseID         string
	LeaseGeneration int64
}

func (s *SQLiteStore) StartSpecialistSchedule(ctx context.Context,
	start domain.SpecialistScheduleStart,
) (domain.SpecialistScheduleStartResult, error) {
	start, err := start.Normalize()
	if err != nil {
		return domain.SpecialistScheduleStartResult{},
			apperror.Wrap(apperror.CodeInvalidArgument, "Specialist schedule start is invalid", err)
	}
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{})
	if err != nil {
		return domain.SpecialistScheduleStartResult{}, err
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx, `UPDATE runs SET updated_at = updated_at WHERE id = ?`,
		start.RunID); err != nil {
		return domain.SpecialistScheduleStartResult{}, err
	}
	if err := requireRunExecutionLeaseTx(ctx, tx, start.RunID, start.Lease.LeaseID,
		start.Lease.Generation); err != nil {
		return domain.SpecialistScheduleStartResult{}, err
	}
	run, err := scanRun(tx.QueryRowContext(ctx, `SELECT id, mission_id, session_id, status,
		config_json, budget_json, started_at, finished_at, created_at, updated_at
		FROM runs WHERE id = ?`, start.RunID))
	if err != nil {
		return domain.SpecialistScheduleStartResult{}, err
	}
	if run.Status != domain.RunRunning {
		return domain.SpecialistScheduleStartResult{}, apperror.New(apperror.CodeFailedPrecondition,
			"Specialist schedule requires a running Run")
	}
	if err := requireNoApplyingSpecialistDelegationApplicationTx(ctx, tx, run.ID); err != nil {
		return domain.SpecialistScheduleStartResult{}, err
	}
	if start.ContinuationDigest != "" {
		var existingID string
		err := tx.QueryRowContext(ctx, `SELECT id FROM specialist_schedules WHERE continuation_digest = ?
			ORDER BY started_at DESC, id DESC LIMIT 1`, start.ContinuationDigest).Scan(&existingID)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return domain.SpecialistScheduleStartResult{}, err
		}
		if err == nil {
			existing, err := getSpecialistScheduleTx(ctx, tx, existingID)
			if err != nil {
				return domain.SpecialistScheduleStartResult{}, err
			}
			if existing.Schedule.RunID != start.RunID || existing.Schedule.MaxRounds != start.MaxRounds || !slices.Equal(existing.Schedule.AgentIDs, start.AgentIDs) {
				return domain.SpecialistScheduleStartResult{}, apperror.New(apperror.CodeConflict, "Specialist continuation intent changed")
			}
			if existing.Schedule.Status.Terminal() && existing.Schedule.Status != domain.SpecialistScheduleAbandoned {
				return domain.SpecialistScheduleStartResult{Schedule: existing.Schedule}, tx.Commit()
			}
		}
	}
	recovered := false
	previous, found, err := getRunningSpecialistScheduleTx(ctx, tx, run.ID)
	if err != nil {
		return domain.SpecialistScheduleStartResult{}, err
	}
	if found {
		if previous.Schedule.ContinuationDigest != start.ContinuationDigest {
			return domain.SpecialistScheduleStartResult{}, apperror.New(apperror.CodeConflict, "active Specialist schedule belongs to another continuation")
		}
		previousRequestID, operatorControlled, err :=
			getSpecialistOperatorScheduleRequestIDForScheduleTx(ctx, tx,
				previous.Schedule.ID)
		if err != nil {
			return domain.SpecialistScheduleStartResult{}, err
		}
		if start.OperatorRequestID != "" {
			if !operatorControlled || previousRequestID != start.OperatorRequestID {
				return domain.SpecialistScheduleStartResult{}, apperror.New(
					apperror.CodeConflict,
					"active Specialist schedule belongs to another execution request")
			}
		} else if operatorControlled {
			return domain.SpecialistScheduleStartResult{}, apperror.New(
				apperror.CodeConflict,
				"operator-controlled Specialist schedule requires operator recovery")
		}
		if previous.LeaseGeneration >= start.Lease.Generation {
			return domain.SpecialistScheduleStartResult{}, apperror.New(apperror.CodeConflict,
				"Run already has an active Specialist schedule")
		}
		if !usageNotBefore(start.UsageBefore, previous.Schedule.UsageBefore) {
			return domain.SpecialistScheduleStartResult{}, apperror.New(apperror.CodeConflict,
				"stale Specialist schedule usage is not monotonic")
		}
		var priorTurns, recoverableAttempts int
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*),
			COALESCE(SUM(CASE WHEN status = 'running' THEN 1 ELSE 0 END), 0)
			FROM agent_attempts WHERE run_id = ? AND lease_id = ? AND lease_generation = ?`,
			run.ID, previous.LeaseID, previous.LeaseGeneration).
			Scan(&priorTurns, &recoverableAttempts); err != nil {
			return domain.SpecialistScheduleStartResult{}, err
		}
		finishedAt := start.StartedAt
		if finishedAt.Before(previous.Schedule.StartedAt) {
			finishedAt = previous.Schedule.StartedAt
		}
		updateResult, err := tx.ExecContext(ctx, `UPDATE specialist_schedules SET status = ?,
			stop_reason = 'worker_lost', turns_started = ?, recovered_attempts = ?,
			after_root_tokens = ?, after_specialist_tokens = ?,
			after_readonly_tokens = ?, after_total_tokens = ?, after_root_execution_millis = ?,
			after_specialist_execution_millis = ?, after_total_execution_millis = ?,
			after_readonly_execution_millis = ?,
			error_code = 'UNAVAILABLE', finished_at = ? WHERE id = ? AND status = ?`,
			domain.SpecialistScheduleAbandoned, priorTurns, recoverableAttempts,
			start.UsageBefore.RootTokens,
			start.UsageBefore.SpecialistTokens, start.UsageBefore.ReadOnlyFanoutTokens,
			start.UsageBefore.RootTokens+start.UsageBefore.SpecialistTokens,
			start.UsageBefore.RootExecutionMillis, start.UsageBefore.SpecialistExecutionMillis,
			start.UsageBefore.RootExecutionMillis+start.UsageBefore.SpecialistExecutionMillis,
			start.UsageBefore.ReadOnlyFanoutMillis, ts(finishedAt), previous.Schedule.ID,
			domain.SpecialistScheduleRunning)
		if err != nil {
			return domain.SpecialistScheduleStartResult{}, err
		}
		if err := requireSingleAgentAttemptUpdate(updateResult,
			"stale Specialist schedule changed before recovery"); err != nil {
			return domain.SpecialistScheduleStartResult{}, err
		}
		previous.Schedule.Status = domain.SpecialistScheduleAbandoned
		previous.Schedule.StopReason = "worker_lost"
		previous.Schedule.ErrorCode = "UNAVAILABLE"
		previous.Schedule.TurnsStarted = priorTurns
		previous.Schedule.RecoveredAttempts = recoverableAttempts
		previous.Schedule.UsageAfter = start.UsageBefore
		previous.Schedule.FinishedAt = &finishedAt
		if err := appendSpecialistScheduleStoppedEventTx(ctx, tx, run,
			previous.Schedule); err != nil {
			return domain.SpecialistScheduleStartResult{}, err
		}
		recovered = true
	}
	if err := requireSpecialistScheduleTargetsTx(ctx, tx, run.ID, start.AgentIDs,
		start.OperatorRequestID); err != nil {
		return domain.SpecialistScheduleStartResult{}, err
	}
	if err := requireSpecialistOperatorScheduleStartTx(ctx, tx, start); err != nil {
		return domain.SpecialistScheduleStartResult{}, err
	}
	usage := start.UsageBefore
	if _, err := tx.ExecContext(ctx, `INSERT INTO specialist_schedules
		(id, run_id, lease_id, lease_generation, max_rounds, status,
		 before_root_tokens, before_specialist_tokens, before_readonly_tokens,
		 before_total_tokens,
		 before_root_execution_millis, before_specialist_execution_millis,
		 before_readonly_execution_millis, before_total_execution_millis,
		 after_root_tokens, after_specialist_tokens, after_readonly_tokens,
		 after_total_tokens, after_root_execution_millis, after_specialist_execution_millis,
		 after_readonly_execution_millis,
		 after_total_execution_millis, started_at, continuation_digest)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		start.ID, run.ID, start.Lease.LeaseID, start.Lease.Generation, start.MaxRounds,
		domain.SpecialistScheduleRunning, usage.RootTokens, usage.SpecialistTokens,
		usage.ReadOnlyFanoutTokens, usage.RootTokens+usage.SpecialistTokens,
		usage.RootExecutionMillis, usage.SpecialistExecutionMillis,
		usage.ReadOnlyFanoutMillis,
		usage.RootExecutionMillis+usage.SpecialistExecutionMillis,
		usage.RootTokens, usage.SpecialistTokens, usage.ReadOnlyFanoutTokens,
		usage.RootTokens+usage.SpecialistTokens, usage.RootExecutionMillis,
		usage.SpecialistExecutionMillis, usage.ReadOnlyFanoutMillis,
		usage.RootExecutionMillis+usage.SpecialistExecutionMillis,
		ts(start.StartedAt), start.ContinuationDigest); err != nil {
		return domain.SpecialistScheduleStartResult{}, err
	}
	for index, agentID := range start.AgentIDs {
		if _, err := tx.ExecContext(ctx, `INSERT INTO specialist_schedule_agents
			(schedule_id, run_id, agent_id, ordinal) VALUES (?, ?, ?, ?)`, start.ID,
			run.ID, agentID, index+1); err != nil {
			return domain.SpecialistScheduleStartResult{}, err
		}
	}
	if start.OperatorRequestID != "" {
		var attemptOrdinal int
		if err := tx.QueryRowContext(ctx, `SELECT 1 + COUNT(*)
			FROM specialist_operator_schedule_attempts WHERE request_id = ?`,
			start.OperatorRequestID).Scan(&attemptOrdinal); err != nil {
			return domain.SpecialistScheduleStartResult{}, err
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO specialist_operator_schedule_attempts
			(request_id, schedule_id, ordinal, created_at) VALUES (?, ?, ?, ?)`,
			start.OperatorRequestID, start.ID, attemptOrdinal,
			ts(start.StartedAt)); err != nil {
			return domain.SpecialistScheduleStartResult{},
				normalizeSpecialistOperatorScheduleError(err)
		}
	}
	schedule := domain.SpecialistSchedule{
		ContinuationDigest: start.ContinuationDigest,
		ID:                 start.ID, RunID: run.ID, AgentIDs: append([]string(nil), start.AgentIDs...),
		MaxRounds: start.MaxRounds, Status: domain.SpecialistScheduleRunning,
		UsageBefore: usage, UsageAfter: usage, StartedAt: start.StartedAt,
	}
	if err := schedule.Validate(); err != nil {
		return domain.SpecialistScheduleStartResult{}, err
	}
	payload := map[string]any{
		"schedule_id": schedule.ID, "agent_ids": schedule.AgentIDs,
		"child_count": len(schedule.AgentIDs), "max_rounds": schedule.MaxRounds,
		"usage_before":        schedule.UsageBefore,
		"operator_controlled": start.OperatorRequestID != "",
	}
	if start.OperatorRequestID != "" {
		payload["operator_schedule_request_id"] = start.OperatorRequestID
	}
	if err := appendSupervisorEventTx(ctx, tx, run, events.AgentScheduleStartedEvent,
		"specialist_scheduler", schedule.ID, payload); err != nil {
		return domain.SpecialistScheduleStartResult{}, err
	}
	if err := tx.Commit(); err != nil {
		return domain.SpecialistScheduleStartResult{}, err
	}
	return domain.SpecialistScheduleStartResult{
		Schedule: schedule, RecoveredSchedule: recovered,
	}, nil
}

func (s *SQLiteStore) FinishSpecialistSchedule(ctx context.Context,
	finish domain.SpecialistScheduleFinish,
) (domain.SpecialistSchedule, error) {
	finish, err := finish.Normalize()
	if err != nil {
		return domain.SpecialistSchedule{},
			apperror.Wrap(apperror.CodeInvalidArgument, "Specialist schedule finish is invalid", err)
	}
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{})
	if err != nil {
		return domain.SpecialistSchedule{}, err
	}
	defer func() { _ = tx.Rollback() }()
	record, err := getSpecialistScheduleTx(ctx, tx, finish.ID)
	if err != nil {
		return domain.SpecialistSchedule{}, err
	}
	if record.Schedule.Status != domain.SpecialistScheduleRunning {
		if !sameSpecialistScheduleFinish(record.Schedule, finish) {
			return domain.SpecialistSchedule{}, apperror.New(apperror.CodeConflict,
				"Specialist schedule terminal replay differs from its durable summary")
		}
		if err := tx.Commit(); err != nil {
			return domain.SpecialistSchedule{}, err
		}
		return record.Schedule, nil
	}
	if record.Schedule.RunID != finish.Lease.RunID || record.LeaseID != finish.Lease.LeaseID ||
		record.LeaseGeneration != finish.Lease.Generation {
		return domain.SpecialistSchedule{}, apperror.New(apperror.CodeConflict,
			"Specialist schedule finish does not match its execution lease")
	}
	if err := requireRunExecutionLeaseTx(ctx, tx, record.Schedule.RunID,
		finish.Lease.LeaseID, finish.Lease.Generation); err != nil {
		return domain.SpecialistSchedule{}, err
	}
	if finish.RoundsCompleted > record.Schedule.MaxRounds ||
		!usageNotBefore(finish.UsageAfter, record.Schedule.UsageBefore) {
		return domain.SpecialistSchedule{}, apperror.New(apperror.CodeConflict,
			"Specialist schedule terminal summary is not monotonic")
	}
	finishedAt := finish.FinishedAt
	if finishedAt.Before(record.Schedule.StartedAt) {
		finishedAt = record.Schedule.StartedAt
	}
	result, err := tx.ExecContext(ctx, `UPDATE specialist_schedules SET status = ?,
		stop_reason = ?, rounds_completed = ?, turns_started = ?, recovered_attempts = ?,
		after_root_tokens = ?, after_specialist_tokens = ?, after_total_tokens = ?,
		after_readonly_tokens = ?,
		after_root_execution_millis = ?, after_specialist_execution_millis = ?,
		after_readonly_execution_millis = ?, after_total_execution_millis = ?,
		error_code = ?, finished_at = ?
		WHERE id = ? AND status = ?`, finish.Status, finish.StopReason,
		finish.RoundsCompleted, finish.TurnsStarted, finish.RecoveredAttempts,
		finish.UsageAfter.RootTokens, finish.UsageAfter.SpecialistTokens,
		finish.UsageAfter.RootTokens+finish.UsageAfter.SpecialistTokens,
		finish.UsageAfter.ReadOnlyFanoutTokens, finish.UsageAfter.RootExecutionMillis,
		finish.UsageAfter.SpecialistExecutionMillis, finish.UsageAfter.ReadOnlyFanoutMillis,
		finish.UsageAfter.RootExecutionMillis+finish.UsageAfter.SpecialistExecutionMillis,
		finish.ErrorCode, ts(finishedAt), finish.ID, domain.SpecialistScheduleRunning)
	if err != nil {
		return domain.SpecialistSchedule{}, err
	}
	if err := requireSingleAgentAttemptUpdate(result,
		"Specialist schedule changed before terminal commit"); err != nil {
		return domain.SpecialistSchedule{}, err
	}
	record.Schedule.Status = finish.Status
	record.Schedule.StopReason = finish.StopReason
	record.Schedule.RoundsCompleted = finish.RoundsCompleted
	record.Schedule.TurnsStarted = finish.TurnsStarted
	record.Schedule.RecoveredAttempts = finish.RecoveredAttempts
	record.Schedule.UsageAfter = finish.UsageAfter
	record.Schedule.ErrorCode = finish.ErrorCode
	record.Schedule.FinishedAt = &finishedAt
	if err := record.Schedule.Validate(); err != nil {
		return domain.SpecialistSchedule{}, err
	}
	run, err := scanRun(tx.QueryRowContext(ctx, `SELECT id, mission_id, session_id, status,
		config_json, budget_json, started_at, finished_at, created_at, updated_at
		FROM runs WHERE id = ?`, record.Schedule.RunID))
	if err != nil {
		return domain.SpecialistSchedule{}, err
	}
	if err := appendSpecialistScheduleStoppedEventTx(ctx, tx, run,
		record.Schedule); err != nil {
		return domain.SpecialistSchedule{}, err
	}
	if err := tx.Commit(); err != nil {
		return domain.SpecialistSchedule{}, err
	}
	return record.Schedule, nil
}

func (s *SQLiteStore) GetSpecialistSchedule(ctx context.Context,
	id string,
) (domain.SpecialistSchedule, error) {
	id = strings.TrimSpace(id)
	if id == "" || len([]rune(id)) > domain.MaxModelCancellationIdentityRunes {
		return domain.SpecialistSchedule{}, apperror.New(apperror.CodeInvalidArgument,
			"Specialist schedule id is required and bounded")
	}
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return domain.SpecialistSchedule{}, err
	}
	defer func() { _ = tx.Rollback() }()
	record, err := getSpecialistScheduleTx(ctx, tx, id)
	if err != nil {
		return domain.SpecialistSchedule{}, err
	}
	if err := tx.Commit(); err != nil {
		return domain.SpecialistSchedule{}, err
	}
	return record.Schedule, nil
}

func getRunningSpecialistScheduleTx(ctx context.Context, tx *sql.Tx,
	runID string,
) (specialistScheduleRecord, bool, error) {
	record, err := scanSpecialistSchedule(tx.QueryRowContext(ctx,
		specialistScheduleSelect+` WHERE run_id = ? AND status = ?`, runID,
		domain.SpecialistScheduleRunning))
	if errors.Is(err, sql.ErrNoRows) {
		return specialistScheduleRecord{}, false, nil
	}
	if err != nil {
		return specialistScheduleRecord{}, false, err
	}
	agentIDs, err := listSpecialistScheduleAgentsTx(ctx, tx, record.Schedule.ID)
	if err != nil {
		return specialistScheduleRecord{}, false, err
	}
	record.Schedule.AgentIDs = agentIDs
	if err := record.Schedule.Validate(); err != nil {
		return specialistScheduleRecord{}, false,
			apperror.Wrap(apperror.CodeFailedPrecondition, "invalid persisted Specialist schedule", err)
	}
	return record, true, nil
}

func getSpecialistScheduleTx(ctx context.Context, tx *sql.Tx,
	id string,
) (specialistScheduleRecord, error) {
	record, err := scanSpecialistSchedule(tx.QueryRowContext(ctx,
		specialistScheduleSelect+` WHERE id = ?`, id))
	if err != nil {
		return specialistScheduleRecord{}, err
	}
	record.Schedule.AgentIDs, err = listSpecialistScheduleAgentsTx(ctx, tx, id)
	if err != nil {
		return specialistScheduleRecord{}, err
	}
	if err := record.Schedule.Validate(); err != nil {
		return specialistScheduleRecord{},
			apperror.Wrap(apperror.CodeFailedPrecondition, "invalid persisted Specialist schedule", err)
	}
	return record, nil
}

func scanSpecialistSchedule(row scanner) (specialistScheduleRecord, error) {
	var record specialistScheduleRecord
	var beforeTokenSubtotal, beforeExecutionSubtotal int64
	var afterTokenSubtotal, afterExecutionSubtotal int64
	var startedAt string
	var finishedAt sql.NullString
	schedule := &record.Schedule
	if err := row.Scan(&schedule.ID, &schedule.RunID, &record.LeaseID,
		&record.LeaseGeneration, &schedule.MaxRounds, &schedule.Status,
		&schedule.StopReason, &schedule.RoundsCompleted, &schedule.TurnsStarted,
		&schedule.RecoveredAttempts, &schedule.UsageBefore.RootTokens,
		&schedule.UsageBefore.SpecialistTokens, &schedule.UsageBefore.ReadOnlyFanoutTokens,
		&beforeTokenSubtotal,
		&schedule.UsageBefore.RootExecutionMillis,
		&schedule.UsageBefore.SpecialistExecutionMillis,
		&schedule.UsageBefore.ReadOnlyFanoutMillis, &beforeExecutionSubtotal,
		&schedule.UsageAfter.RootTokens, &schedule.UsageAfter.SpecialistTokens,
		&schedule.UsageAfter.ReadOnlyFanoutTokens, &afterTokenSubtotal,
		&schedule.UsageAfter.RootExecutionMillis,
		&schedule.UsageAfter.SpecialistExecutionMillis,
		&schedule.UsageAfter.ReadOnlyFanoutMillis, &afterExecutionSubtotal,
		&schedule.ErrorCode, &startedAt,
		&finishedAt, &schedule.ContinuationDigest); err != nil {
		return specialistScheduleRecord{}, err
	}
	schedule.UsageBefore.RunID = schedule.RunID
	schedule.UsageAfter.RunID = schedule.RunID
	if beforeTokenSubtotal != schedule.UsageBefore.RootTokens+
		schedule.UsageBefore.SpecialistTokens ||
		beforeExecutionSubtotal != schedule.UsageBefore.RootExecutionMillis+
			schedule.UsageBefore.SpecialistExecutionMillis ||
		afterTokenSubtotal != schedule.UsageAfter.RootTokens+
			schedule.UsageAfter.SpecialistTokens ||
		afterExecutionSubtotal != schedule.UsageAfter.RootExecutionMillis+
			schedule.UsageAfter.SpecialistExecutionMillis {
		return specialistScheduleRecord{}, apperror.New(apperror.CodeConflict,
			"Specialist schedule legacy usage subtotal is inconsistent")
	}
	schedule.UsageBefore.TotalTokens = beforeTokenSubtotal +
		schedule.UsageBefore.ReadOnlyFanoutTokens
	schedule.UsageBefore.TotalExecutionMillis = beforeExecutionSubtotal +
		schedule.UsageBefore.ReadOnlyFanoutMillis
	schedule.UsageAfter.TotalTokens = afterTokenSubtotal +
		schedule.UsageAfter.ReadOnlyFanoutTokens
	schedule.UsageAfter.TotalExecutionMillis = afterExecutionSubtotal +
		schedule.UsageAfter.ReadOnlyFanoutMillis
	schedule.StartedAt = parseTS(startedAt)
	schedule.FinishedAt = parseNullableTS(finishedAt)
	return record, nil
}

func (s *SQLiteStore) GetSpecialistContinuationSchedule(ctx context.Context, digest string) (domain.SpecialistSchedule, bool, error) {
	var id string
	err := s.db.QueryRowContext(ctx, `SELECT id FROM specialist_schedules WHERE continuation_digest = ?
		ORDER BY started_at DESC, id DESC LIMIT 1`, digest).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.SpecialistSchedule{}, false, nil
	}
	if err != nil {
		return domain.SpecialistSchedule{}, false, err
	}
	schedule, err := s.GetSpecialistSchedule(ctx, id)
	return schedule, err == nil, err
}

func (s *SQLiteStore) ListSpecialistContinuationAttemptIDs(ctx context.Context, digest string) ([]string, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT attempt.id FROM agent_attempts attempt
		JOIN specialist_schedules schedule ON schedule.run_id = attempt.run_id
			AND schedule.lease_id = attempt.lease_id AND schedule.lease_generation = attempt.lease_generation
		JOIN specialist_schedule_agents target ON target.schedule_id = schedule.id AND target.agent_id = attempt.agent_id
		WHERE schedule.continuation_digest = ? ORDER BY attempt.turn_number, attempt.id`, digest)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	ids := []string{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

func listSpecialistScheduleAgentsTx(ctx context.Context, tx *sql.Tx,
	scheduleID string,
) ([]string, error) {
	rows, err := tx.QueryContext(ctx, `SELECT agent_id FROM specialist_schedule_agents
		WHERE schedule_id = ? ORDER BY ordinal`, scheduleID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []string
	for rows.Next() {
		var agentID string
		if err := rows.Scan(&agentID); err != nil {
			return nil, err
		}
		result = append(result, agentID)
	}
	return result, rows.Err()
}

func requireSpecialistScheduleTargetsTx(ctx context.Context, tx *sql.Tx,
	runID string, agentIDs []string, operatorRequestID string,
) error {
	var rootID string
	var rootCount int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*), COALESCE(MIN(id), '')
		FROM agent_nodes WHERE run_id = ? AND role = 'root'`, runID).
		Scan(&rootCount, &rootID); err != nil {
		return err
	}
	if rootCount != 1 {
		return apperror.New(apperror.CodeFailedPrecondition,
			"Specialist schedule requires exactly one root Agent")
	}
	for _, agentID := range agentIDs {
		var count int
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM agent_nodes
			WHERE run_id = ? AND id = ? AND role = 'specialist' AND parent_id = ?`,
			runID, agentID, rootID).Scan(&count); err != nil {
			return err
		}
		if count != 1 {
			return apperror.New(apperror.CodeFailedPrecondition,
				fmt.Sprintf("Agent %s is not a direct Specialist child of this Run", agentID))
		}
		reserved, err := hasPendingSpecialistOperatorScheduleReservationTx(ctx, tx,
			runID, agentID, operatorRequestID)
		if err != nil {
			return err
		}
		if reserved {
			return apperror.New(apperror.CodeConflict,
				fmt.Sprintf("Specialist %s is reserved by an operator schedule", agentID))
		}
	}
	return nil
}

func requireSpecialistOperatorScheduleStartTx(ctx context.Context, tx *sql.Tx,
	start domain.SpecialistScheduleStart,
) error {
	if start.OperatorRequestID == "" {
		return nil
	}
	request, err := getSpecialistOperatorScheduleRequest(ctx, tx,
		start.OperatorRequestID)
	if err != nil {
		return err
	}
	if request.RunID != start.RunID || request.MaxRounds != start.MaxRounds ||
		!slices.Equal(request.AgentIDs, start.AgentIDs) {
		return apperror.New(apperror.CodeFailedPrecondition,
			"Specialist schedule does not match its operator request")
	}
	latest, found, err := getLatestSpecialistOperatorScheduleAttempt(ctx, tx,
		request.ID)
	if err != nil {
		return err
	}
	if !found {
		return nil
	}
	previous, err := getSpecialistScheduleTx(ctx, tx, latest.ScheduleID)
	if err != nil {
		return err
	}
	if previous.Schedule.Status != domain.SpecialistScheduleAbandoned {
		return apperror.New(apperror.CodeConflict,
			"specialist operator schedule request already has a terminal or active attempt")
	}
	return nil
}

func getSpecialistOperatorScheduleRequestIDForScheduleTx(ctx context.Context,
	tx *sql.Tx, scheduleID string,
) (string, bool, error) {
	var requestID string
	err := tx.QueryRowContext(ctx, `SELECT request_id
		FROM specialist_operator_schedule_attempts WHERE schedule_id = ?`,
		scheduleID).Scan(&requestID)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	return requestID, err == nil, err
}

func appendSpecialistScheduleStoppedEventTx(ctx context.Context, tx *sql.Tx,
	run domain.Run, schedule domain.SpecialistSchedule,
) error {
	return appendSupervisorEventTx(ctx, tx, run, events.AgentScheduleStoppedEvent,
		"specialist_scheduler", schedule.ID, map[string]any{
			"schedule_id": schedule.ID, "agent_ids": schedule.AgentIDs,
			"child_count": len(schedule.AgentIDs), "max_rounds": schedule.MaxRounds,
			"status": schedule.Status, "stop_reason": schedule.StopReason,
			"rounds_completed":   schedule.RoundsCompleted,
			"turns_started":      schedule.TurnsStarted,
			"recovered_attempts": schedule.RecoveredAttempts,
			"usage_before":       schedule.UsageBefore, "usage_after": schedule.UsageAfter,
			"error_code": schedule.ErrorCode,
		})
}

func sameSpecialistScheduleFinish(schedule domain.SpecialistSchedule,
	finish domain.SpecialistScheduleFinish,
) bool {
	return schedule.Status == finish.Status && schedule.StopReason == finish.StopReason &&
		schedule.RoundsCompleted == finish.RoundsCompleted &&
		schedule.TurnsStarted == finish.TurnsStarted &&
		schedule.RecoveredAttempts == finish.RecoveredAttempts &&
		schedule.UsageAfter == finish.UsageAfter && schedule.ErrorCode == finish.ErrorCode
}

func usageNotBefore(after domain.RunAgentUsage, before domain.RunAgentUsage) bool {
	return after.RunID == before.RunID && after.RootTokens >= before.RootTokens &&
		after.SpecialistTokens >= before.SpecialistTokens &&
		after.ReadOnlyFanoutTokens >= before.ReadOnlyFanoutTokens &&
		after.TotalTokens >= before.TotalTokens &&
		after.RootExecutionMillis >= before.RootExecutionMillis &&
		after.SpecialistExecutionMillis >= before.SpecialistExecutionMillis &&
		after.ReadOnlyFanoutMillis >= before.ReadOnlyFanoutMillis &&
		after.TotalExecutionMillis >= before.TotalExecutionMillis
}
