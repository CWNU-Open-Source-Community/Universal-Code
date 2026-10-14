package store

import (
	"context"
	"database/sql"
	"errors"
	"strconv"
	"strings"
	"time"

	"cyberagent-workbench/internal/apperror"
	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/events"
	"cyberagent-workbench/internal/idgen"
	"cyberagent-workbench/internal/redact"
	"cyberagent-workbench/internal/runmutation"
	"cyberagent-workbench/internal/session"
)

const agentAttemptSelect = `SELECT id, run_id, agent_id, parent_agent_id, lease_id,
	lease_generation, turn_number, status, input_tokens, output_tokens, total_tokens,
	execution_millis, usage_recorded_at, failure_code, failure_reason,
	notification_message_id, started_at, updated_at, finished_at FROM agent_attempts`

func (s *SQLiteStore) BeginSpecialistAttempt(ctx context.Context, start domain.AgentAttemptStart,
	operationKey string,
) (domain.AgentAttempt, bool, error) {
	start.AttemptID = strings.TrimSpace(start.AttemptID)
	start.RunID = strings.TrimSpace(start.RunID)
	start.AgentID = strings.TrimSpace(start.AgentID)
	start.ParentAgentID = strings.TrimSpace(start.ParentAgentID)
	if start.StartedAt.IsZero() {
		start.StartedAt = time.Now().UTC()
	} else {
		start.StartedAt = start.StartedAt.UTC()
	}
	if err := start.Validate(); err != nil {
		return domain.AgentAttempt{}, false,
			apperror.Wrap(apperror.CodeInvalidArgument, "Specialist attempt start is invalid", err)
	}
	normalizedOperationKey, err := domain.NormalizeAgentOperationKey(operationKey)
	if err != nil {
		return domain.AgentAttempt{}, false,
			apperror.Wrap(apperror.CodeInvalidArgument, "Specialist attempt idempotency key is invalid", err)
	}
	keyDigest := runmutation.Fingerprint("agent_attempt_operation.v1", start.RunID,
		normalizedOperationKey)
	requestFingerprint := runmutation.Fingerprint("agent_attempt_start.v1", start.RunID,
		start.AgentID, start.ParentAgentID, start.Lease.LeaseID,
		strconv.FormatInt(start.Lease.Generation, 10))

	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{})
	if err != nil {
		return domain.AgentAttempt{}, false, err
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx, `UPDATE agent_nodes SET updated_at = updated_at WHERE id = ?`,
		start.AgentID); err != nil {
		return domain.AgentAttempt{}, false, err
	}
	storedFingerprint, storedAttemptID, storedKind, found, err :=
		getAgentAttemptMutationTx(ctx, tx, keyDigest)
	if err != nil {
		return domain.AgentAttempt{}, false, err
	}
	if found {
		if storedKind != "start" || storedFingerprint != requestFingerprint {
			return domain.AgentAttempt{}, false, apperror.New(apperror.CodeConflict,
				"Specialist attempt idempotency key was already used for different intent")
		}
		existing, err := scanAgentAttempt(tx.QueryRowContext(ctx,
			agentAttemptSelect+` WHERE id = ?`, storedAttemptID))
		if err != nil {
			return domain.AgentAttempt{}, false, err
		}
		if err := tx.Commit(); err != nil {
			return domain.AgentAttempt{}, false, err
		}
		return existing, true, nil
	}
	if err := requireRunExecutionLeaseTx(ctx, tx, start.RunID, start.Lease.LeaseID,
		start.Lease.Generation); err != nil {
		return domain.AgentAttempt{}, false, err
	}
	run, _, err := getCoordinatorRunTx(ctx, tx, start.RunID)
	if err != nil {
		return domain.AgentAttempt{}, false, err
	}
	if run.Status != domain.RunRunning {
		return domain.AgentAttempt{}, false,
			apperror.New(apperror.CodeFailedPrecondition, "Specialist scheduling requires a running Run")
	}
	if err := requireNoApplyingSpecialistDelegationApplicationTx(ctx, tx, run.ID); err != nil {
		return domain.AgentAttempt{}, false, err
	}
	child, err := scanAgentNode(tx.QueryRowContext(ctx, agentNodeSelect+` WHERE id = ?`, start.AgentID))
	if err != nil {
		return domain.AgentAttempt{}, false, err
	}
	if child.RunID != run.ID || child.Role != domain.AgentRoleSpecialist ||
		child.ParentID != start.ParentAgentID || child.Status != domain.AgentReady {
		return domain.AgentAttempt{}, false, apperror.New(apperror.CodeFailedPrecondition,
			"Specialist scheduling requires the ready child and its direct parent")
	}
	blocked, err := specialistDependenciesBlockedTx(ctx, tx, child.RunID, child.ID)
	if err != nil {
		return domain.AgentAttempt{}, false, err
	}
	if blocked {
		return domain.AgentAttempt{}, false, apperror.New(apperror.CodeFailedPrecondition,
			"Specialist scheduling requires satisfied dependencies")
	}
	parent, err := scanAgentNode(tx.QueryRowContext(ctx, agentNodeSelect+` WHERE id = ?`,
		start.ParentAgentID))
	if err != nil {
		return domain.AgentAttempt{}, false, err
	}
	if parent.RunID != run.ID || parent.Role != domain.AgentRoleRoot || parent.Terminal() {
		return domain.AgentAttempt{}, false,
			apperror.New(apperror.CodeFailedPrecondition, "Specialist parent must be the active Run root")
	}
	if child.TurnsUsed >= child.TurnLimit {
		return domain.AgentAttempt{}, false,
			apperror.New(apperror.CodeResourceExhausted, "Specialist turn budget is exhausted")
	}
	if child.TokenLimit > 0 && child.TokensUsed >= child.TokenLimit {
		return domain.AgentAttempt{}, false,
			apperror.New(apperror.CodeResourceExhausted, "Specialist token budget is exhausted")
	}
	attempt := domain.AgentAttempt{
		ID: start.AttemptID, RunID: run.ID, AgentID: child.ID, ParentAgentID: parent.ID,
		LeaseID: start.Lease.LeaseID, LeaseGeneration: start.Lease.Generation,
		Turn: child.TurnsUsed + 1, Status: domain.AgentAttemptRunning,
		StartedAt: start.StartedAt, UpdatedAt: start.StartedAt,
	}
	if err := attempt.Validate(); err != nil {
		return domain.AgentAttempt{}, false, err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO agent_attempts
		(id, run_id, agent_id, parent_agent_id, lease_id, lease_generation, turn_number, status,
		input_tokens, output_tokens, total_tokens, execution_millis, usage_recorded_at,
		failure_code, failure_reason, notification_message_id, started_at, updated_at, finished_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, 0, 0, 0, 0, NULL, '', '', '', ?, ?, NULL)`,
		attempt.ID, attempt.RunID, attempt.AgentID, attempt.ParentAgentID, attempt.LeaseID,
		attempt.LeaseGeneration, attempt.Turn, attempt.Status, ts(attempt.StartedAt),
		ts(attempt.UpdatedAt)); err != nil {
		return domain.AgentAttempt{}, false, err
	}
	if err := insertAgentAttemptMutationTx(ctx, tx, keyDigest, requestFingerprint, attempt.ID,
		"start", attempt.StartedAt); err != nil {
		return domain.AgentAttempt{}, false, err
	}
	updatedChild := child
	updatedChild.Status = domain.AgentRunning
	updatedChild.ActiveAttemptID = attempt.ID
	updatedChild.StatusReason = ""
	updatedChild.TurnsUsed = attempt.Turn
	updatedChild.Version++
	updatedChild.UpdatedAt = attempt.StartedAt
	if err := updatedChild.Validate(); err != nil {
		return domain.AgentAttempt{}, false, err
	}
	result, err := tx.ExecContext(ctx, `UPDATE agent_nodes SET status = ?, active_attempt_id = ?,
		status_reason = '', turns_used = ?, version = ?, updated_at = ?
		WHERE id = ? AND version = ? AND status = ? AND active_attempt_id = ''`,
		updatedChild.Status, updatedChild.ActiveAttemptID, updatedChild.TurnsUsed, updatedChild.Version,
		ts(updatedChild.UpdatedAt), updatedChild.ID, child.Version, domain.AgentReady)
	if err != nil {
		return domain.AgentAttempt{}, false, err
	}
	if err := requireSingleAgentAttemptUpdate(result, "Specialist changed during scheduling"); err != nil {
		return domain.AgentAttempt{}, false, err
	}
	if err := appendSupervisorEventTx(ctx, tx, run, events.AgentTurnStartedEvent,
		"agent_coordinator", attempt.ID, map[string]any{
			"agent_id": child.ID, "parent_agent_id": parent.ID, "turn": attempt.Turn,
			"lease_generation": attempt.LeaseGeneration,
		}); err != nil {
		return domain.AgentAttempt{}, false, err
	}
	if err := appendSupervisorEventTx(ctx, tx, run, events.AgentStatusChangedEvent,
		"agent_coordinator", child.ID, map[string]any{
			"from": child.Status, "to": updatedChild.Status, "attempt_id": attempt.ID,
			"turns_used": updatedChild.TurnsUsed, "version": updatedChild.Version,
		}); err != nil {
		return domain.AgentAttempt{}, false, err
	}
	if _, err := createAgentGraphSnapshotTx(ctx, tx, run); err != nil {
		return domain.AgentAttempt{}, false, err
	}
	if err := tx.Commit(); err != nil {
		return domain.AgentAttempt{}, false, err
	}
	return attempt, false, nil
}

func (s *SQLiteStore) RecordSpecialistAttemptUsage(ctx context.Context, ref domain.AgentAttemptRef,
	usage domain.AgentAttemptUsage, operationKey string,
) (domain.AgentAttempt, bool, error) {
	ref = normalizeAgentAttemptRef(ref)
	if err := ref.Validate(); err != nil {
		return domain.AgentAttempt{}, false,
			apperror.Wrap(apperror.CodeInvalidArgument, "Specialist usage reference is invalid", err)
	}
	if err := usage.Validate(); err != nil {
		return domain.AgentAttempt{}, false,
			apperror.Wrap(apperror.CodeInvalidArgument, "Specialist usage is invalid", err)
	}
	return s.mutateSpecialistUsage(ctx, ref, usage, operationKey)
}

func (s *SQLiteStore) ContinueSpecialistAttempt(ctx context.Context, ref domain.AgentAttemptRef,
	operationKey string,
) (domain.AgentAttempt, bool, error) {
	ref = normalizeAgentAttemptRef(ref)
	if err := ref.Validate(); err != nil {
		return domain.AgentAttempt{}, false,
			apperror.Wrap(apperror.CodeInvalidArgument, "Specialist continuation reference is invalid", err)
	}
	normalizedOperationKey, err := domain.NormalizeAgentOperationKey(operationKey)
	if err != nil {
		return domain.AgentAttempt{}, false,
			apperror.Wrap(apperror.CodeInvalidArgument, "Specialist continuation key is invalid", err)
	}
	keyDigest := runmutation.Fingerprint("agent_attempt_operation.v1", ref.RunID,
		normalizedOperationKey)
	requestFingerprint := runmutation.Fingerprint("agent_attempt_continue.v1", ref.RunID,
		ref.AgentID, ref.AttemptID)
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{})
	if err != nil {
		return domain.AgentAttempt{}, false, err
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx, `UPDATE agent_nodes SET updated_at = updated_at WHERE id = ?`,
		ref.AgentID); err != nil {
		return domain.AgentAttempt{}, false, err
	}
	if replayed, attempt, err := replayAgentAttemptMutationTx(ctx, tx, keyDigest,
		requestFingerprint, "continue"); err != nil || replayed {
		if err == nil {
			err = tx.Commit()
		}
		return attempt, replayed, err
	}
	attempt, child, run, err := loadActiveAgentAttemptTx(ctx, tx, ref)
	if err != nil {
		return domain.AgentAttempt{}, false, err
	}
	if err := requireSpecialistProtocolRepairResolvedTx(ctx, tx, attempt.ID); err != nil {
		return domain.AgentAttempt{}, false, err
	}
	if attempt.UsageRecordedAt == nil {
		return domain.AgentAttempt{}, false, apperror.New(apperror.CodeFailedPrecondition,
			"Specialist continuation requires recorded model usage")
	}
	if child.TurnsUsed >= child.TurnLimit ||
		(child.TokenLimit > 0 && child.TokensUsed >= child.TokenLimit) {
		return domain.AgentAttempt{}, false,
			apperror.New(apperror.CodeResourceExhausted, "Specialist has no budget for another turn")
	}
	parent, err := scanAgentNode(tx.QueryRowContext(ctx, agentNodeSelect+` WHERE id = ?`,
		attempt.ParentAgentID))
	if err != nil {
		return domain.AgentAttempt{}, false, err
	}
	now := time.Now().UTC()
	if _, err := commitSpecialistContextTx(ctx, tx, run, child, parent, attempt, now); err != nil {
		return domain.AgentAttempt{}, false, err
	}
	updatedAttempt := attempt
	updatedAttempt.Status = domain.AgentAttemptContinued
	updatedAttempt.UpdatedAt = now
	updatedAttempt.FinishedAt = &now
	if err := updatedAttempt.Validate(); err != nil {
		return domain.AgentAttempt{}, false, err
	}
	result, err := tx.ExecContext(ctx, `UPDATE agent_attempts SET status = ?, updated_at = ?, finished_at = ?
		WHERE id = ? AND status = ?`, updatedAttempt.Status, ts(now), ts(now), attempt.ID,
		domain.AgentAttemptRunning)
	if err != nil {
		return domain.AgentAttempt{}, false, err
	}
	if err := requireSingleAgentAttemptUpdate(result, "Specialist attempt changed before continuation"); err != nil {
		return domain.AgentAttempt{}, false, err
	}
	updatedChild := child
	updatedChild.Status = domain.AgentReady
	updatedChild.ActiveAttemptID = ""
	updatedChild.StatusReason = "continuation requested"
	updatedChild.Version++
	updatedChild.UpdatedAt = now
	if err := updatedChild.Validate(); err != nil {
		return domain.AgentAttempt{}, false, err
	}
	result, err = tx.ExecContext(ctx, `UPDATE agent_nodes SET status = ?, active_attempt_id = '',
		status_reason = ?, version = ?, updated_at = ? WHERE id = ? AND version = ?
		AND status = ? AND active_attempt_id = ?`, updatedChild.Status, updatedChild.StatusReason,
		updatedChild.Version, ts(now), child.ID, child.Version, domain.AgentRunning, attempt.ID)
	if err != nil {
		return domain.AgentAttempt{}, false, err
	}
	if err := requireSingleAgentAttemptUpdate(result, "Specialist changed before continuation"); err != nil {
		return domain.AgentAttempt{}, false, err
	}
	if err := insertAgentAttemptMutationTx(ctx, tx, keyDigest, requestFingerprint, attempt.ID,
		"continue", now); err != nil {
		return domain.AgentAttempt{}, false, err
	}
	if err := appendSupervisorEventTx(ctx, tx, run, events.AgentTurnCompletedEvent,
		"agent_coordinator", attempt.ID, agentAttemptEventPayload(updatedAttempt, false)); err != nil {
		return domain.AgentAttempt{}, false, err
	}
	if err := appendAgentAttemptStatusEventTx(ctx, tx, run, child, updatedChild,
		attempt.ID); err != nil {
		return domain.AgentAttempt{}, false, err
	}
	if _, err := createAgentGraphSnapshotTx(ctx, tx, run); err != nil {
		return domain.AgentAttempt{}, false, err
	}
	if err := tx.Commit(); err != nil {
		return domain.AgentAttempt{}, false, err
	}
	return updatedAttempt, false, nil
}

func (s *SQLiteStore) CrashSpecialistAttempt(ctx context.Context,
	request domain.AgentAttemptFailureRequest, operationKey string,
) (domain.AgentAttempt, bool, error) {
	request.Ref = normalizeAgentAttemptRef(request.Ref)
	request.NotificationMessageID = strings.TrimSpace(request.NotificationMessageID)
	if request.FailedAt.IsZero() {
		request.FailedAt = time.Now().UTC()
	} else {
		request.FailedAt = request.FailedAt.UTC()
	}
	failure, err := domain.NormalizeAgentAttemptFailure(request.Failure)
	if err != nil {
		return domain.AgentAttempt{}, false,
			apperror.Wrap(apperror.CodeInvalidArgument, "Specialist failure is invalid", err)
	}
	failure.Reason = redact.String(failure.Reason)
	failure, err = domain.NormalizeAgentAttemptFailure(failure)
	if err != nil {
		return domain.AgentAttempt{}, false,
			apperror.Wrap(apperror.CodeInvalidArgument, "redacted Specialist failure is invalid", err)
	}
	request.Failure = failure
	if err := request.Validate(); err != nil {
		return domain.AgentAttempt{}, false,
			apperror.Wrap(apperror.CodeInvalidArgument, "Specialist failure request is invalid", err)
	}
	normalizedOperationKey, err := domain.NormalizeAgentOperationKey(operationKey)
	if err != nil {
		return domain.AgentAttempt{}, false,
			apperror.Wrap(apperror.CodeInvalidArgument, "Specialist failure key is invalid", err)
	}
	keyDigest := runmutation.Fingerprint("agent_attempt_operation.v1", request.Ref.RunID,
		normalizedOperationKey)
	requestFingerprint := runmutation.Fingerprint("agent_attempt_crash.v1", request.Ref.RunID,
		request.Ref.AgentID, request.Ref.AttemptID, failure.Code, failure.Reason)
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{})
	if err != nil {
		return domain.AgentAttempt{}, false, err
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx, `UPDATE agent_nodes SET updated_at = updated_at WHERE id = ?`,
		request.Ref.AgentID); err != nil {
		return domain.AgentAttempt{}, false, err
	}
	if replayed, attempt, err := replayAgentAttemptMutationTx(ctx, tx, keyDigest,
		requestFingerprint, "crash"); err != nil || replayed {
		if err == nil {
			err = tx.Commit()
		}
		return attempt, replayed, err
	}
	attempt, child, run, err := loadActiveAgentAttemptTx(ctx, tx, request.Ref)
	if err != nil {
		return domain.AgentAttempt{}, false, err
	}
	parent, err := scanAgentNode(tx.QueryRowContext(ctx, agentNodeSelect+` WHERE id = ?`, child.ParentID))
	if err != nil {
		return domain.AgentAttempt{}, false, err
	}
	updated, _, err := crashAgentAttemptTx(ctx, tx, run, child, parent, attempt, failure,
		request.NotificationMessageID, request.FailedAt, false)
	if err != nil {
		return domain.AgentAttempt{}, false, err
	}
	if err := insertAgentAttemptMutationTx(ctx, tx, keyDigest, requestFingerprint, attempt.ID,
		"crash", request.FailedAt); err != nil {
		return domain.AgentAttempt{}, false, err
	}
	if _, err := createAgentGraphSnapshotTx(ctx, tx, run); err != nil {
		return domain.AgentAttempt{}, false, err
	}
	if err := tx.Commit(); err != nil {
		return domain.AgentAttempt{}, false, err
	}
	return updated, false, nil
}

func (s *SQLiteStore) RecoverSpecialistAttempts(ctx context.Context,
	lease domain.RunExecutionLease,
) ([]domain.AgentAttempt, error) {
	if err := lease.Validate(); err != nil || lease.Status != domain.RunExecutionLeaseActive {
		return nil, apperror.Wrap(apperror.CodeInvalidArgument,
			"Specialist recovery requires an active Run execution lease", err)
	}
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{})
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx, `UPDATE runs SET updated_at = updated_at WHERE id = ?`,
		lease.RunID); err != nil {
		return nil, err
	}
	if err := requireRunExecutionLeaseTx(ctx, tx, lease.RunID, lease.LeaseID,
		lease.Generation); err != nil {
		return nil, err
	}
	run, _, err := getCoordinatorRunTx(ctx, tx, lease.RunID)
	if err != nil {
		return nil, err
	}
	if run.Status != domain.RunRunning {
		return nil, apperror.New(apperror.CodeFailedPrecondition,
			"Specialist recovery requires a running Run")
	}
	rows, err := tx.QueryContext(ctx, agentAttemptSelect+` WHERE run_id = ? AND status = ?
		AND (lease_id <> ? OR lease_generation <> ?) ORDER BY started_at, id`, lease.RunID,
		domain.AgentAttemptRunning, lease.LeaseID, lease.Generation)
	if err != nil {
		return nil, err
	}
	stale, err := scanAgentAttempts(rows)
	closeErr := rows.Close()
	if err != nil {
		return nil, err
	}
	if closeErr != nil {
		return nil, closeErr
	}
	if len(stale) == 0 {
		if err := tx.Commit(); err != nil {
			return nil, err
		}
		return []domain.AgentAttempt{}, nil
	}
	recovered := make([]domain.AgentAttempt, 0, len(stale))
	now := time.Now().UTC()
	for _, attempt := range stale {
		child, err := scanAgentNode(tx.QueryRowContext(ctx, agentNodeSelect+` WHERE id = ?`,
			attempt.AgentID))
		if err != nil {
			return nil, err
		}
		parent, err := scanAgentNode(tx.QueryRowContext(ctx, agentNodeSelect+` WHERE id = ?`,
			attempt.ParentAgentID))
		if err != nil {
			return nil, err
		}
		updated, _, err := crashAgentAttemptTx(ctx, tx, run, child, parent, attempt,
			domain.AgentAttemptFailure{
				Code: "worker_lost", Reason: "previous worker lease was replaced before attempt completion",
			}, idgen.New("agentmsg"), now, true)
		if err != nil {
			return nil, err
		}
		recovered = append(recovered, updated)
	}
	if _, err := createAgentGraphSnapshotTx(ctx, tx, run); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return recovered, nil
}

func (s *SQLiteStore) GetAgentAttempt(ctx context.Context,
	attemptID string,
) (domain.AgentAttempt, bool, error) {
	attempt, err := scanAgentAttempt(s.db.QueryRowContext(ctx,
		agentAttemptSelect+` WHERE id = ?`, strings.TrimSpace(attemptID)))
	if errors.Is(err, sql.ErrNoRows) {
		return domain.AgentAttempt{}, false, nil
	}
	if err != nil {
		return domain.AgentAttempt{}, false, err
	}
	return attempt, true, nil
}

func (s *SQLiteStore) ListAgentAttempts(ctx context.Context,
	agentID string,
) ([]domain.AgentAttempt, error) {
	rows, err := s.db.QueryContext(ctx, agentAttemptSelect+` WHERE agent_id = ?
		ORDER BY turn_number, started_at`, strings.TrimSpace(agentID))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanAgentAttempts(rows)
}

func (s *SQLiteStore) mutateSpecialistUsage(ctx context.Context, ref domain.AgentAttemptRef,
	usage domain.AgentAttemptUsage, operationKey string,
) (domain.AgentAttempt, bool, error) {
	normalizedOperationKey, err := domain.NormalizeAgentOperationKey(operationKey)
	if err != nil {
		return domain.AgentAttempt{}, false,
			apperror.Wrap(apperror.CodeInvalidArgument, "Specialist usage key is invalid", err)
	}
	keyDigest := runmutation.Fingerprint("agent_attempt_operation.v1", ref.RunID,
		normalizedOperationKey)
	requestFingerprint := runmutation.Fingerprint("agent_attempt_usage.v1", ref.RunID,
		ref.AgentID, ref.AttemptID, strconv.FormatInt(usage.InputTokens, 10),
		strconv.FormatInt(usage.OutputTokens, 10), strconv.FormatInt(usage.TotalTokens, 10),
		strconv.FormatInt(usage.ExecutionMillis, 10))
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{})
	if err != nil {
		return domain.AgentAttempt{}, false, err
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx, `UPDATE agent_nodes SET updated_at = updated_at WHERE id = ?`,
		ref.AgentID); err != nil {
		return domain.AgentAttempt{}, false, err
	}
	if replayed, attempt, err := replayAgentAttemptMutationTx(ctx, tx, keyDigest,
		requestFingerprint, "usage"); err != nil || replayed {
		if err == nil {
			err = tx.Commit()
		}
		return attempt, replayed, err
	}
	attempt, child, run, err := loadActiveAgentAttemptTx(ctx, tx, ref)
	if err != nil {
		return domain.AgentAttempt{}, false, err
	}
	if attempt.UsageRecordedAt != nil {
		return domain.AgentAttempt{}, false,
			apperror.New(apperror.CodeConflict, "Specialist attempt usage was already recorded")
	}
	now := time.Now().UTC()
	updatedAttempt, _, err := applySpecialistUsageTx(ctx, tx, attempt, child, run, usage,
		keyDigest, requestFingerprint, now)
	if err != nil {
		return domain.AgentAttempt{}, false, err
	}
	if _, err := createAgentGraphSnapshotTx(ctx, tx, run); err != nil {
		return domain.AgentAttempt{}, false, err
	}
	if err := tx.Commit(); err != nil {
		return domain.AgentAttempt{}, false, err
	}
	return updatedAttempt, false, nil
}

func applySpecialistUsageTx(ctx context.Context, tx *sql.Tx, attempt domain.AgentAttempt,
	child domain.AgentNode, run domain.Run, usage domain.AgentAttemptUsage,
	keyDigest string, requestFingerprint string, now time.Time,
) (domain.AgentAttempt, domain.AgentNode, error) {
	if attempt.Status != domain.AgentAttemptRunning || attempt.UsageRecordedAt != nil ||
		child.Status != domain.AgentRunning || child.ActiveAttemptID != attempt.ID ||
		child.ID != attempt.AgentID || child.RunID != run.ID {
		return domain.AgentAttempt{}, domain.AgentNode{},
			apperror.New(apperror.CodeConflict, "Specialist usage target changed before commit")
	}
	if err := usage.Validate(); err != nil {
		return domain.AgentAttempt{}, domain.AgentNode{}, err
	}
	now = now.UTC()
	updatedAttempt := attempt
	updatedAttempt.Usage = usage
	updatedAttempt.UsageRecordedAt = &now
	updatedAttempt.UpdatedAt = now
	if err := updatedAttempt.Validate(); err != nil {
		return domain.AgentAttempt{}, domain.AgentNode{}, err
	}
	updatedTokens, err := supervisorAddCounter(child.TokensUsed, usage.TotalTokens,
		"Specialist token")
	if err != nil {
		return domain.AgentAttempt{}, domain.AgentNode{}, err
	}
	result, err := tx.ExecContext(ctx, `UPDATE agent_attempts SET input_tokens = ?, output_tokens = ?,
		total_tokens = ?, execution_millis = ?, usage_recorded_at = ?, updated_at = ?
		WHERE id = ? AND status = ? AND usage_recorded_at IS NULL`, usage.InputTokens,
		usage.OutputTokens, usage.TotalTokens, usage.ExecutionMillis, ts(now), ts(now), attempt.ID,
		domain.AgentAttemptRunning)
	if err != nil {
		return domain.AgentAttempt{}, domain.AgentNode{}, err
	}
	if err := requireSingleAgentAttemptUpdate(result, "Specialist usage changed concurrently"); err != nil {
		return domain.AgentAttempt{}, domain.AgentNode{}, err
	}
	updatedChild := child
	updatedChild.TokensUsed = updatedTokens
	updatedChild.Version++
	updatedChild.UpdatedAt = now
	if err := updatedChild.Validate(); err != nil {
		return domain.AgentAttempt{}, domain.AgentNode{}, err
	}
	result, err = tx.ExecContext(ctx, `UPDATE agent_nodes SET tokens_used = ?, version = ?, updated_at = ?
		WHERE id = ? AND version = ? AND status = ? AND active_attempt_id = ?`,
		updatedChild.TokensUsed, updatedChild.Version, ts(now), child.ID, child.Version,
		domain.AgentRunning, attempt.ID)
	if err != nil {
		return domain.AgentAttempt{}, domain.AgentNode{}, err
	}
	if err := requireSingleAgentAttemptUpdate(result, "Specialist changed during usage commit"); err != nil {
		return domain.AgentAttempt{}, domain.AgentNode{}, err
	}
	if err := insertAgentAttemptMutationTx(ctx, tx, keyDigest, requestFingerprint, attempt.ID,
		"usage", now); err != nil {
		return domain.AgentAttempt{}, domain.AgentNode{}, err
	}
	if err := appendSupervisorEventTx(ctx, tx, run, events.AgentAttemptUsageRecordedEvent,
		"agent_coordinator", attempt.ID, agentAttemptEventPayload(updatedAttempt, false)); err != nil {
		return domain.AgentAttempt{}, domain.AgentNode{}, err
	}
	return updatedAttempt, updatedChild, nil
}

func loadActiveAgentAttemptTx(ctx context.Context, tx *sql.Tx,
	ref domain.AgentAttemptRef,
) (domain.AgentAttempt, domain.AgentNode, domain.Run, error) {
	attempt, err := scanAgentAttempt(tx.QueryRowContext(ctx, agentAttemptSelect+` WHERE id = ?`,
		ref.AttemptID))
	if err != nil {
		return domain.AgentAttempt{}, domain.AgentNode{}, domain.Run{}, err
	}
	if attempt.RunID != ref.RunID || attempt.AgentID != ref.AgentID ||
		attempt.Status != domain.AgentAttemptRunning {
		return domain.AgentAttempt{}, domain.AgentNode{}, domain.Run{},
			apperror.New(apperror.CodeFailedPrecondition, "Specialist attempt is not the active target")
	}
	if err := requireRunExecutionLeaseTx(ctx, tx, attempt.RunID, attempt.LeaseID,
		attempt.LeaseGeneration); err != nil {
		return domain.AgentAttempt{}, domain.AgentNode{}, domain.Run{}, err
	}
	child, err := scanAgentNode(tx.QueryRowContext(ctx, agentNodeSelect+` WHERE id = ?`, attempt.AgentID))
	if err != nil {
		return domain.AgentAttempt{}, domain.AgentNode{}, domain.Run{}, err
	}
	if child.Status != domain.AgentRunning || child.ActiveAttemptID != attempt.ID ||
		child.ParentID != attempt.ParentAgentID {
		return domain.AgentAttempt{}, domain.AgentNode{}, domain.Run{},
			apperror.New(apperror.CodeFailedPrecondition, "Specialist projection does not match its attempt")
	}
	run, _, err := getCoordinatorRunTx(ctx, tx, attempt.RunID)
	if err != nil {
		return domain.AgentAttempt{}, domain.AgentNode{}, domain.Run{}, err
	}
	if run.Status != domain.RunRunning {
		return domain.AgentAttempt{}, domain.AgentNode{}, domain.Run{},
			apperror.New(apperror.CodeFailedPrecondition, "Specialist attempt Run is not running")
	}
	return attempt, child, run, nil
}

func crashAgentAttemptTx(ctx context.Context, tx *sql.Tx, run domain.Run,
	child domain.AgentNode, parent domain.AgentNode, attempt domain.AgentAttempt,
	failure domain.AgentAttemptFailure, messageID string, at time.Time,
	recovered bool,
) (domain.AgentAttempt, domain.AgentNode, error) {
	if child.Status != domain.AgentRunning || child.ActiveAttemptID != attempt.ID ||
		attempt.Status != domain.AgentAttemptRunning || parent.ID != attempt.ParentAgentID ||
		parent.RunID != run.ID || parent.Role != domain.AgentRoleRoot || parent.Terminal() {
		return domain.AgentAttempt{}, domain.AgentNode{},
			apperror.New(apperror.CodeFailedPrecondition, "crashed attempt projection is no longer active")
	}
	retry := child.TurnsUsed < child.TurnLimit &&
		(child.TokenLimit == 0 || child.TokensUsed < child.TokenLimit)
	payloadJSON, err := marshalRedactedJSON(domain.AgentAttemptFailurePayload{
		Version: domain.AgentAttemptFailureProtocolVersion, AgentID: child.ID, AttemptID: attempt.ID,
		FailureCode: failure.Code, Reason: failure.Reason, RetryScheduled: retry, Recovered: recovered,
	})
	if err != nil {
		return domain.AgentAttempt{}, domain.AgentNode{}, err
	}
	if len([]byte(payloadJSON)) > domain.MaxAgentMessagePayloadBytes {
		return domain.AgentAttempt{}, domain.AgentNode{},
			apperror.New(apperror.CodeResourceExhausted, "Specialist failure notification is too large")
	}
	message := domain.AgentMessage{
		ID: messageID, RunID: run.ID, SenderAgentID: child.ID, RecipientAgentID: parent.ID,
		Kind: domain.AgentMessageNotification, Semantic: domain.AgentMessageSemanticMessage,
		PayloadJSON: payloadJSON, Status: domain.AgentMessagePending, CreatedAt: at,
	}
	if err := insertBoundedAgentMessageTx(ctx, tx, &message); err != nil {
		return domain.AgentAttempt{}, domain.AgentNode{}, err
	}
	if err := abortSpecialistProtocolRepairTx(ctx, tx, run, attempt,
		failure.Reason, at); err != nil {
		return domain.AgentAttempt{}, domain.AgentNode{}, err
	}
	updatedAttempt := attempt
	updatedAttempt.Status = domain.AgentAttemptCrashed
	updatedAttempt.Failure = failure
	updatedAttempt.NotificationMessageID = message.ID
	updatedAttempt.UpdatedAt = at
	updatedAttempt.FinishedAt = &at
	if err := updatedAttempt.Validate(); err != nil {
		return domain.AgentAttempt{}, domain.AgentNode{}, err
	}
	result, err := tx.ExecContext(ctx, `UPDATE agent_attempts SET status = ?, failure_code = ?,
		failure_reason = ?, notification_message_id = ?, updated_at = ?, finished_at = ?
		WHERE id = ? AND status = ?`, updatedAttempt.Status, failure.Code, failure.Reason,
		message.ID, ts(at), ts(at), attempt.ID, domain.AgentAttemptRunning)
	if err != nil {
		return domain.AgentAttempt{}, domain.AgentNode{}, err
	}
	if err := requireSingleAgentAttemptUpdate(result, "Specialist attempt changed before crash commit"); err != nil {
		return domain.AgentAttempt{}, domain.AgentNode{}, err
	}
	cancellationResolution := "attempt_terminated"
	if recovered {
		cancellationResolution = "worker_lost"
	}
	if err := resolveTerminatedSpecialistModelCancellationsTx(ctx, tx, updatedAttempt,
		cancellationResolution, at); err != nil {
		return domain.AgentAttempt{}, domain.AgentNode{}, err
	}
	if _, err := supersedeSpecialistContextDeliveriesTx(ctx, tx, run, child.ID, "", at); err != nil {
		return domain.AgentAttempt{}, domain.AgentNode{}, err
	}
	if err := pruneSupersededSpecialistContextDeliveriesTx(ctx, tx, run.ID); err != nil {
		return domain.AgentAttempt{}, domain.AgentNode{}, err
	}
	updatedChild := child
	updatedChild.ActiveAttemptID = ""
	updatedChild.Version++
	updatedChild.UpdatedAt = at
	if retry {
		updatedChild.Status = domain.AgentReady
		updatedChild.StatusReason = "attempt crashed; retry available"
	} else {
		updatedChild.Status = domain.AgentFailed
		updatedChild.StatusReason = "attempt crashed; budget exhausted"
		finished := at
		updatedChild.FinishedAt = &finished
	}
	if err := updatedChild.Validate(); err != nil {
		return domain.AgentAttempt{}, domain.AgentNode{}, err
	}
	result, err = tx.ExecContext(ctx, `UPDATE agent_nodes SET status = ?, active_attempt_id = '',
		status_reason = ?, version = ?, updated_at = ?, finished_at = ?
		WHERE id = ? AND version = ? AND status = ? AND active_attempt_id = ?`,
		updatedChild.Status, updatedChild.StatusReason, updatedChild.Version, ts(at),
		nullableTS(updatedChild.FinishedAt), child.ID, child.Version, domain.AgentRunning, attempt.ID)
	if err != nil {
		return domain.AgentAttempt{}, domain.AgentNode{}, err
	}
	if err := requireSingleAgentAttemptUpdate(result, "Specialist changed before crash commit"); err != nil {
		return domain.AgentAttempt{}, domain.AgentNode{}, err
	}
	if updatedChild.Terminal() {
		sessionResult, err := tx.ExecContext(ctx, `UPDATE sessions SET status = ?, updated_at = ?
			WHERE id = ? AND status = ?`, session.StatusArchived, ts(at), child.SessionID,
			session.StatusActive)
		if err != nil {
			return domain.AgentAttempt{}, domain.AgentNode{}, err
		}
		if err := requireSingleAgentAttemptUpdate(sessionResult,
			"Specialist Session changed before terminal crash commit"); err != nil {
			return domain.AgentAttempt{}, domain.AgentNode{}, err
		}
	}
	if err := appendSupervisorEventTx(ctx, tx, run, events.AgentTurnFailedEvent,
		"agent_coordinator", attempt.ID, agentAttemptEventPayload(updatedAttempt, recovered)); err != nil {
		return domain.AgentAttempt{}, domain.AgentNode{}, err
	}
	if err := appendSupervisorEventTx(ctx, tx, run, events.AgentMessageSentEvent,
		"agent_coordinator", message.ID, map[string]any{
			"sender_agent_id": child.ID, "recipient_agent_id": parent.ID,
			"sequence": message.Sequence, "kind": message.Kind, "semantic": message.Semantic,
			"payload_bytes": len([]byte(message.PayloadJSON)), "attempt_id": attempt.ID,
		}); err != nil {
		return domain.AgentAttempt{}, domain.AgentNode{}, err
	}
	if err := appendAgentAttemptStatusEventTx(ctx, tx, run, child, updatedChild,
		attempt.ID); err != nil {
		return domain.AgentAttempt{}, domain.AgentNode{}, err
	}
	return updatedAttempt, updatedChild, nil
}

func interruptAgentAttemptTx(ctx context.Context, tx *sql.Tx, run domain.Run,
	child domain.AgentNode, code string, reason string, at time.Time,
) (domain.AgentAttempt, error) {
	attempt, err := scanAgentAttempt(tx.QueryRowContext(ctx, agentAttemptSelect+` WHERE id = ?`,
		child.ActiveAttemptID))
	if err != nil {
		return domain.AgentAttempt{}, err
	}
	if attempt.RunID != run.ID || attempt.AgentID != child.ID ||
		attempt.ParentAgentID != child.ParentID || attempt.Status != domain.AgentAttemptRunning {
		return domain.AgentAttempt{}, apperror.New(apperror.CodeFailedPrecondition,
			"Specialist projection does not match its interruptible attempt")
	}
	failure, err := domain.NormalizeAgentAttemptFailure(domain.AgentAttemptFailure{Code: code, Reason: reason})
	if err != nil {
		return domain.AgentAttempt{}, err
	}
	if err := abortSpecialistProtocolRepairTx(ctx, tx, run, attempt,
		failure.Reason, at); err != nil {
		return domain.AgentAttempt{}, err
	}
	updated := attempt
	updated.Status = domain.AgentAttemptInterrupted
	updated.Failure = failure
	updated.UpdatedAt = at
	updated.FinishedAt = &at
	if err := updated.Validate(); err != nil {
		return domain.AgentAttempt{}, err
	}
	result, err := tx.ExecContext(ctx, `UPDATE agent_attempts SET status = ?, failure_code = ?,
		failure_reason = ?, updated_at = ?, finished_at = ? WHERE id = ? AND status = ?`,
		updated.Status, failure.Code, failure.Reason, ts(at), ts(at), attempt.ID,
		domain.AgentAttemptRunning)
	if err != nil {
		return domain.AgentAttempt{}, err
	}
	if err := requireSingleAgentAttemptUpdate(result, "Specialist attempt changed before interruption"); err != nil {
		return domain.AgentAttempt{}, err
	}
	if err := resolveTerminatedSpecialistModelCancellationsTx(ctx, tx, updated,
		"attempt_terminated", at); err != nil {
		return domain.AgentAttempt{}, err
	}
	if _, err := supersedeSpecialistContextDeliveriesTx(ctx, tx, run, child.ID, "", at); err != nil {
		return domain.AgentAttempt{}, err
	}
	if err := pruneSupersededSpecialistContextDeliveriesTx(ctx, tx, run.ID); err != nil {
		return domain.AgentAttempt{}, err
	}
	if err := appendSupervisorEventTx(ctx, tx, run, events.AgentTurnFailedEvent,
		"agent_coordinator", attempt.ID, agentAttemptEventPayload(updated, false)); err != nil {
		return domain.AgentAttempt{}, err
	}
	return updated, nil
}

func validateAgentAttemptProjectionTx(ctx context.Context, tx *sql.Tx,
	nodes []domain.AgentNode,
) error {
	for _, node := range nodes {
		if node.Role != domain.AgentRoleSpecialist {
			continue
		}
		rows, err := tx.QueryContext(ctx, agentAttemptSelect+` WHERE agent_id = ?
			ORDER BY turn_number, started_at, id`, node.ID)
		if err != nil {
			return err
		}
		attempts, err := scanAgentAttempts(rows)
		closeErr := rows.Close()
		if err != nil {
			return err
		}
		if closeErr != nil {
			return closeErr
		}
		if len(attempts) == 0 {
			if node.Status == domain.AgentRunning {
				return apperror.New(apperror.CodeFailedPrecondition,
					"running Specialist is missing its durable Agent attempt")
			}
			continue
		}
		var tokensUsed int64
		runningCount := 0
		for index, attempt := range attempts {
			if attempt.RunID != node.RunID || attempt.AgentID != node.ID ||
				attempt.ParentAgentID != node.ParentID || attempt.Turn != int64(index+1) {
				return apperror.New(apperror.CodeFailedPrecondition,
					"Specialist attempt history contains an invalid relationship or turn sequence")
			}
			tokensUsed, err = supervisorAddCounter(tokensUsed, attempt.Usage.TotalTokens,
				"Specialist restored token")
			if err != nil {
				return err
			}
			if attempt.Status == domain.AgentAttemptRunning {
				runningCount++
			}
		}
		latest := attempts[len(attempts)-1]
		if node.TurnsUsed != latest.Turn || node.TokensUsed != tokensUsed {
			return apperror.New(apperror.CodeFailedPrecondition,
				"Specialist budget projection does not match its durable Agent attempts")
		}
		if node.Status == domain.AgentRunning {
			if runningCount != 1 || latest.Status != domain.AgentAttemptRunning ||
				latest.ID != node.ActiveAttemptID {
				return apperror.New(apperror.CodeFailedPrecondition,
					"running Specialist does not match one durable Agent attempt")
			}
		} else if runningCount != 0 {
			return apperror.New(apperror.CodeFailedPrecondition,
				"non-running Specialist unexpectedly has a running Agent attempt")
		}
		if node.Status == domain.AgentCompleted && latest.Status != domain.AgentAttemptFinished {
			return apperror.New(apperror.CodeFailedPrecondition,
				"completed Specialist does not end with a finished Agent attempt")
		}
	}
	return nil
}

func replayAgentAttemptMutationTx(ctx context.Context, tx *sql.Tx, keyDigest string,
	requestFingerprint string, kind string,
) (bool, domain.AgentAttempt, error) {
	storedFingerprint, attemptID, storedKind, found, err :=
		getAgentAttemptMutationTx(ctx, tx, keyDigest)
	if err != nil || !found {
		return false, domain.AgentAttempt{}, err
	}
	if storedKind != kind || storedFingerprint != requestFingerprint {
		return false, domain.AgentAttempt{}, apperror.New(apperror.CodeConflict,
			"Agent attempt idempotency key was already used for different intent")
	}
	attempt, err := scanAgentAttempt(tx.QueryRowContext(ctx, agentAttemptSelect+` WHERE id = ?`,
		attemptID))
	return true, attempt, err
}

func getAgentAttemptMutationTx(ctx context.Context, tx *sql.Tx,
	keyDigest string,
) (string, string, string, bool, error) {
	var fingerprint, attemptID, kind string
	err := tx.QueryRowContext(ctx, `SELECT request_fingerprint, attempt_id, kind
		FROM agent_attempt_mutations WHERE operation_key_digest = ?`, keyDigest).
		Scan(&fingerprint, &attemptID, &kind)
	if errors.Is(err, sql.ErrNoRows) {
		return "", "", "", false, nil
	}
	if err != nil {
		return "", "", "", false, err
	}
	return fingerprint, attemptID, kind, true, nil
}

func insertAgentAttemptMutationTx(ctx context.Context, tx *sql.Tx, keyDigest string,
	requestFingerprint string, attemptID string, kind string, at time.Time,
) error {
	_, err := tx.ExecContext(ctx, `INSERT INTO agent_attempt_mutations
		(operation_key_digest, request_fingerprint, attempt_id, kind, created_at)
		VALUES (?, ?, ?, ?, ?)`, keyDigest, requestFingerprint, attemptID, kind, ts(at))
	return err
}

func scanAgentAttempt(row scanner) (domain.AgentAttempt, error) {
	var attempt domain.AgentAttempt
	var status, usageRecordedAt, startedAt, updatedAt, finishedAt sql.NullString
	if err := row.Scan(&attempt.ID, &attempt.RunID, &attempt.AgentID, &attempt.ParentAgentID,
		&attempt.LeaseID, &attempt.LeaseGeneration, &attempt.Turn, &status,
		&attempt.Usage.InputTokens, &attempt.Usage.OutputTokens, &attempt.Usage.TotalTokens,
		&attempt.Usage.ExecutionMillis, &usageRecordedAt, &attempt.Failure.Code,
		&attempt.Failure.Reason, &attempt.NotificationMessageID, &startedAt, &updatedAt,
		&finishedAt); err != nil {
		return domain.AgentAttempt{}, err
	}
	attempt.Status = domain.AgentAttemptStatus(status.String)
	attempt.UsageRecordedAt = parseNullableTS(usageRecordedAt)
	attempt.StartedAt = parseTS(startedAt.String)
	attempt.UpdatedAt = parseTS(updatedAt.String)
	attempt.FinishedAt = parseNullableTS(finishedAt)
	return attempt, attempt.Validate()
}

func scanAgentAttempts(rows *sql.Rows) ([]domain.AgentAttempt, error) {
	out := make([]domain.AgentAttempt, 0)
	for rows.Next() {
		attempt, err := scanAgentAttempt(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, attempt)
	}
	return out, rows.Err()
}

func normalizeAgentAttemptRef(ref domain.AgentAttemptRef) domain.AgentAttemptRef {
	ref.RunID = strings.TrimSpace(ref.RunID)
	ref.AgentID = strings.TrimSpace(ref.AgentID)
	ref.AttemptID = strings.TrimSpace(ref.AttemptID)
	return ref
}

func agentAttemptEventPayload(attempt domain.AgentAttempt, recovered bool) map[string]any {
	payload := map[string]any{
		"agent_id": attempt.AgentID, "parent_agent_id": attempt.ParentAgentID,
		"turn": attempt.Turn, "status": attempt.Status,
		"input_tokens": attempt.Usage.InputTokens, "output_tokens": attempt.Usage.OutputTokens,
		"total_tokens": attempt.Usage.TotalTokens, "execution_millis": attempt.Usage.ExecutionMillis,
		"lease_generation": attempt.LeaseGeneration, "recovered": recovered,
	}
	if attempt.Failure.Code != "" {
		payload["failure_code"] = attempt.Failure.Code
		payload["failure_reason_bytes"] = len([]byte(attempt.Failure.Reason))
	}
	return payload
}

func appendAgentAttemptStatusEventTx(ctx context.Context, tx *sql.Tx, run domain.Run,
	before domain.AgentNode, after domain.AgentNode, attemptID string,
) error {
	return appendSupervisorEventTx(ctx, tx, run, events.AgentStatusChangedEvent,
		"agent_coordinator", after.ID, map[string]any{
			"from": before.Status, "to": after.Status, "reason": after.StatusReason,
			"parent_agent_id": after.ParentID, "attempt_id": attemptID,
			"turns_used": after.TurnsUsed, "tokens_used": after.TokensUsed,
			"version": after.Version,
		})
}

func requireSingleAgentAttemptUpdate(result sql.Result, message string) error {
	rows, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if rows != 1 {
		return apperror.New(apperror.CodeConflict, message)
	}
	return nil
}
