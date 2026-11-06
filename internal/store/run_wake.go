package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"cyberagent-workbench/internal/apperror"
	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/events"
)

func (s *SQLiteStore) GetRunWakeOperation(ctx context.Context,
	keyDigest string,
) (domain.RunWakeOperation, bool, error) {
	operation, err := scanRunWakeOperation(s.db.QueryRowContext(ctx, `SELECT
		operation_key_digest, request_fingerprint, protocol_version, action,
		intent_id, run_id, requested_by, event_sequence, created_at
		FROM run_wake_operations WHERE operation_key_digest = ?`, keyDigest))
	if errors.Is(err, sql.ErrNoRows) {
		return domain.RunWakeOperation{}, false, nil
	}
	return operation, err == nil, err
}

func (s *SQLiteStore) GetLatestRunWakeIntent(ctx context.Context,
	runID string,
) (domain.RunWakeIntent, bool, error) {
	intent, err := scanRunWakeIntent(s.db.QueryRowContext(ctx, runWakeIntentSelect+`
		WHERE run_id = ? ORDER BY created_at DESC, id DESC LIMIT 1`, runID))
	if errors.Is(err, sql.ErrNoRows) {
		return domain.RunWakeIntent{}, false, nil
	}
	return intent, err == nil, err
}

func (s *SQLiteStore) GetRunWakeIntent(ctx context.Context,
	id string,
) (domain.RunWakeIntent, error) {
	return scanRunWakeIntent(s.db.QueryRowContext(ctx, runWakeIntentSelect+`
		WHERE id = ?`, id))
}

func (s *SQLiteStore) ListDueRunWakeIntents(ctx context.Context, now time.Time,
	limit, maxSteps int,
) ([]domain.RunWakeIntent, error) {
	if now.IsZero() || limit < 1 || limit > 64 || maxSteps < 1 || maxSteps > domain.MaxRunExecutionHandoffSteps {
		return nil, errors.New("run wake due query is invalid")
	}
	// The worker consumes one candidate per tick. Apply admission before LIMIT so
	// a paused, busy, or drained Run cannot occupy that slot indefinitely. Durable
	// handoff results and exhausted, unprepared wakes still need settlement even
	// when their Run can no longer execute.
	// A prepared handoff retains its original step limit. A worker with a
	// different limit must skip it without changing that durable generation.
	rows, err := s.db.QueryContext(ctx, runWakeIntentSelect+`
		WHERE ((status = 'queued' AND julianday(next_wake_at) <= julianday(?1))
			OR (status = 'leased' AND EXISTS (SELECT 1 FROM run_wake_leases lease
				WHERE lease.id = run_wake_intents.active_lease_id AND lease.status = 'active'
					AND julianday(lease.expires_at) <= julianday(?1))))
			AND NOT EXISTS (SELECT 1 FROM run_wake_consumptions consumption
				WHERE consumption.intent_id = run_wake_intents.id
					AND consumption.generation = run_wake_intents.attempt_count
					AND consumption.status = 'prepared' AND consumption.max_steps <> ?3)
			AND (
				EXISTS (SELECT 1 FROM runs run JOIN sessions session_record
					ON session_record.id = run.session_id
					WHERE run.id = run_wake_intents.run_id
						AND run.session_id = run_wake_intents.session_id
						AND run.status = 'running' AND session_record.status = 'active'
						AND NOT EXISTS (SELECT 1 FROM run_execution_leases execution_lease
							WHERE execution_lease.run_id = run.id AND execution_lease.status = 'active'
								AND julianday(execution_lease.expires_at) > julianday(?1))
						AND (EXISTS (SELECT 1 FROM operator_steering_messages message
							WHERE message.run_id = run.id AND message.session_id = run.session_id
								AND message.status = 'pending')
							OR EXISTS (SELECT 1 FROM run_wake_consumptions consumption
								WHERE consumption.intent_id = run_wake_intents.id
									AND consumption.generation = run_wake_intents.attempt_count
									AND consumption.lease_id = run_wake_intents.active_lease_id
									AND consumption.status = 'prepared')))
				OR EXISTS (SELECT 1 FROM run_wake_consumptions consumption
					JOIN run_execution_handoff_operations handoff
						ON handoff.operation_key_digest = consumption.handoff_operation_key_digest
					JOIN run_execution_handoff_results result ON result.operation_id = handoff.id
					WHERE consumption.intent_id = run_wake_intents.id
						AND consumption.generation = run_wake_intents.attempt_count
						AND consumption.lease_id = run_wake_intents.active_lease_id
						AND consumption.status = 'prepared')
				OR ((attempt_count >= max_attempts OR julianday(deadline_at) <= julianday(?1))
					AND NOT EXISTS (SELECT 1 FROM run_wake_consumptions consumption
						WHERE consumption.intent_id = run_wake_intents.id
							AND consumption.generation = run_wake_intents.attempt_count
							AND consumption.status = 'prepared'))
			)
		ORDER BY next_wake_at, created_at, id LIMIT ?2`, ts(now), limit, maxSteps)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	values := make([]domain.RunWakeIntent, 0, limit)
	for rows.Next() {
		value, err := scanRunWakeIntent(rows)
		if err != nil {
			return nil, err
		}
		values = append(values, value)
	}
	return values, rows.Err()
}

func (s *SQLiteStore) CreateRunWakeIntent(ctx context.Context,
	intent domain.RunWakeIntent, operation domain.RunWakeOperation,
) (domain.RunWakeIntent, domain.RunWakeOperation, bool, error) {
	if err := intent.Validate(); err != nil || intent.Status != domain.RunWakeQueued ||
		operation.EventSequence != 0 || operation.IntentID != intent.ID ||
		operation.RunID != intent.RunID || operation.Action != domain.RunWakeSchedule ||
		operation.CreatedAt != intent.CreatedAt {
		if err != nil {
			return domain.RunWakeIntent{}, domain.RunWakeOperation{}, false, err
		}
		return domain.RunWakeIntent{}, domain.RunWakeOperation{}, false,
			errors.New("run wake schedule operation binding is invalid")
	}
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{})
	if err != nil {
		return domain.RunWakeIntent{}, domain.RunWakeOperation{}, false, err
	}
	defer func() { _ = tx.Rollback() }()
	if existing, found, err := getRunWakeOperationTx(ctx, tx,
		operation.KeyDigest); err != nil {
		return domain.RunWakeIntent{}, domain.RunWakeOperation{}, false, err
	} else if found {
		stored, err := getRunWakeIntentTx(ctx, tx, existing.IntentID)
		if err != nil {
			return domain.RunWakeIntent{}, domain.RunWakeOperation{}, false, err
		}
		if !sameRunWakeOperationIntent(existing, operation) {
			return domain.RunWakeIntent{}, domain.RunWakeOperation{}, false,
				errors.New("run wake operation key was already used for different intent")
		}
		return stored, existing, true, nil
	}
	var activeCount int
	err = tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM run_wake_intents
		WHERE run_id = ? AND status IN ('queued', 'leased')`, intent.RunID).Scan(&activeCount)
	if err != nil {
		return domain.RunWakeIntent{}, domain.RunWakeOperation{}, false, err
	}
	if activeCount != 0 {
		return domain.RunWakeIntent{}, domain.RunWakeOperation{}, false,
			errors.New("run already has an active wake intent")
	}
	event, err := appendRunWakeEventTx(ctx, tx, intent.RunID,
		events.RunWakeScheduledEvent, "run_wake_control", intent.ID, map[string]any{
			"max_attempts": intent.MaxAttempts, "initial_delay_seconds": intent.InitialDelaySeconds,
			"base_backoff_seconds": intent.BaseBackoffSeconds,
			"max_backoff_seconds":  intent.MaxBackoffSeconds,
			"max_elapsed_seconds":  intent.MaxElapsedSeconds,
			"execution_enabled":    false, "background_loop_enabled": false,
		}, intent.CreatedAt)
	if err != nil {
		return domain.RunWakeIntent{}, domain.RunWakeOperation{}, false, err
	}
	if err := insertRunWakeIntentTx(ctx, tx, intent); err != nil {
		return domain.RunWakeIntent{}, domain.RunWakeOperation{}, false, err
	}
	operation.EventSequence = event.Sequence
	if err := operation.Validate(); err != nil {
		return domain.RunWakeIntent{}, domain.RunWakeOperation{}, false, err
	}
	if err := insertRunWakeOperationTx(ctx, tx, operation); err != nil {
		return domain.RunWakeIntent{}, domain.RunWakeOperation{}, false, err
	}
	if err := tx.Commit(); err != nil {
		return domain.RunWakeIntent{}, domain.RunWakeOperation{}, false, err
	}
	return intent, operation, false, nil
}

func (s *SQLiteStore) CancelRunWakeIntent(ctx context.Context,
	runID string, now time.Time, operation domain.RunWakeOperation,
) (domain.RunWakeIntent, domain.RunWakeOperation, bool, error) {
	if now.IsZero() || operation.EventSequence != 0 || operation.RunID != runID ||
		operation.Action != domain.RunWakeCancel || operation.CreatedAt != now {
		return domain.RunWakeIntent{}, domain.RunWakeOperation{}, false,
			errors.New("run wake cancellation operation binding is invalid")
	}
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{})
	if err != nil {
		return domain.RunWakeIntent{}, domain.RunWakeOperation{}, false, err
	}
	defer func() { _ = tx.Rollback() }()
	if existing, found, err := getRunWakeOperationTx(ctx, tx,
		operation.KeyDigest); err != nil {
		return domain.RunWakeIntent{}, domain.RunWakeOperation{}, false, err
	} else if found {
		stored, err := getRunWakeIntentTx(ctx, tx, existing.IntentID)
		if err != nil {
			return domain.RunWakeIntent{}, domain.RunWakeOperation{}, false, err
		}
		if !sameRunWakeOperationIntent(existing, operation) {
			return domain.RunWakeIntent{}, domain.RunWakeOperation{}, false,
				errors.New("run wake operation key was already used for different intent")
		}
		return stored, existing, true, nil
	}
	intent, err := scanRunWakeIntent(tx.QueryRowContext(ctx, runWakeIntentSelect+`
		WHERE run_id = ? AND status IN ('queued', 'leased')
		ORDER BY created_at DESC, id DESC LIMIT 1`, runID))
	if err != nil {
		return domain.RunWakeIntent{}, domain.RunWakeOperation{}, false, err
	}
	operation.IntentID = intent.ID
	if intent.Status == domain.RunWakeLeased {
		var prepared int
		if err := tx.QueryRowContext(ctx, `SELECT EXISTS(
			SELECT 1 FROM run_wake_consumptions
			WHERE intent_id = ? AND generation = ? AND lease_id = ? AND status = 'prepared'
		)`, intent.ID, intent.AttemptCount, intent.ActiveLeaseID).Scan(&prepared); err != nil {
			return domain.RunWakeIntent{}, domain.RunWakeOperation{}, false, err
		}
		if prepared == 1 {
			return domain.RunWakeIntent{}, domain.RunWakeOperation{}, false,
				apperror.New(apperror.CodeConflict,
					"prepared Run wake handoff must settle before cancellation")
		}
	}
	event, err := appendRunWakeEventTx(ctx, tx, intent.RunID,
		events.RunWakeCancelledEvent, "run_wake_control", intent.ID, map[string]any{
			"attempt_count": intent.AttemptCount, "execution_started": false,
			"model_called": false, "tool_called": false,
		}, now)
	if err != nil {
		return domain.RunWakeIntent{}, domain.RunWakeOperation{}, false, err
	}
	if intent.Status == domain.RunWakeLeased {
		if _, err := tx.ExecContext(ctx, `UPDATE run_wake_leases
			SET status = 'revoked', ended_at = ? WHERE id = ? AND status = 'active'`,
			ts(now), intent.ActiveLeaseID); err != nil {
			return domain.RunWakeIntent{}, domain.RunWakeOperation{}, false, err
		}
	}
	if _, err := tx.ExecContext(ctx, `UPDATE run_wake_intents SET status = 'cancelled',
		active_lease_id = NULL, updated_at = ?, cancelled_at = ? WHERE id = ?`,
		ts(now), ts(now), intent.ID); err != nil {
		return domain.RunWakeIntent{}, domain.RunWakeOperation{}, false, err
	}
	operation.EventSequence = event.Sequence
	if err := operation.Validate(); err != nil {
		return domain.RunWakeIntent{}, domain.RunWakeOperation{}, false, err
	}
	if err := insertRunWakeOperationTx(ctx, tx, operation); err != nil {
		return domain.RunWakeIntent{}, domain.RunWakeOperation{}, false, err
	}
	stored, err := getRunWakeIntentTx(ctx, tx, intent.ID)
	if err != nil {
		return domain.RunWakeIntent{}, domain.RunWakeOperation{}, false, err
	}
	if err := tx.Commit(); err != nil {
		return domain.RunWakeIntent{}, domain.RunWakeOperation{}, false, err
	}
	return stored, operation, false, nil
}

func (s *SQLiteStore) AcquireRunWake(ctx context.Context, intentID string,
	ownerID string, leaseID string, now time.Time,
) (domain.RunWakeIntent, domain.RunWakeLease, bool, error) {
	if !domain.ValidAgentID(intentID) || !domain.ValidAgentID(ownerID) ||
		!domain.ValidAgentID(leaseID) || now.IsZero() {
		return domain.RunWakeIntent{}, domain.RunWakeLease{}, false,
			errors.New("run wake acquisition metadata is invalid")
	}
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{})
	if err != nil {
		return domain.RunWakeIntent{}, domain.RunWakeLease{}, false, err
	}
	defer func() { _ = tx.Rollback() }()
	intent, err := getRunWakeIntentTx(ctx, tx, intentID)
	if err != nil {
		return domain.RunWakeIntent{}, domain.RunWakeLease{}, false, err
	}
	if intent.Status == domain.RunWakeLeased {
		intent, err = reconcileExpiredRunWakeTx(ctx, tx, intent, now)
		if err != nil {
			return domain.RunWakeIntent{}, domain.RunWakeLease{}, false, err
		}
	}
	if intent.Status != domain.RunWakeQueued || now.Before(intent.NextWakeAt) {
		if err := tx.Commit(); err != nil {
			return domain.RunWakeIntent{}, domain.RunWakeLease{}, false, err
		}
		return intent, domain.RunWakeLease{}, false, nil
	}
	if intent.AttemptCount >= intent.MaxAttempts || !now.Before(intent.DeadlineAt) {
		intent, err = exhaustRunWakeTx(ctx, tx, intent, now, "budget_exhausted")
		if err != nil {
			return domain.RunWakeIntent{}, domain.RunWakeLease{}, false, err
		}
		if err := tx.Commit(); err != nil {
			return domain.RunWakeIntent{}, domain.RunWakeLease{}, false, err
		}
		return intent, domain.RunWakeLease{}, false, nil
	}
	var eligible int
	err = tx.QueryRowContext(ctx, `SELECT EXISTS(
		SELECT 1 FROM runs run JOIN sessions session_record ON session_record.id = run.session_id
		WHERE run.id = ? AND run.session_id = ? AND run.status = 'running'
			AND session_record.status = 'active'
			AND EXISTS (SELECT 1 FROM operator_steering_messages message
				WHERE message.run_id = run.id AND message.session_id = run.session_id
					AND message.status = 'pending')
			AND NOT EXISTS (SELECT 1 FROM run_execution_leases execution_lease
				WHERE execution_lease.run_id = run.id AND execution_lease.status = 'active'
					AND julianday(execution_lease.expires_at) > julianday(?))
	)`, intent.RunID, intent.SessionID, ts(now)).Scan(&eligible)
	if err != nil {
		return domain.RunWakeIntent{}, domain.RunWakeLease{}, false, err
	}
	if eligible != 1 {
		return domain.RunWakeIntent{}, domain.RunWakeLease{}, false,
			errors.New("run wake intent is not currently eligible for ownership")
	}
	generation := intent.AttemptCount + 1
	expiresAt := now.Add(domain.RunWakeLeaseSeconds * time.Second)
	if expiresAt.After(intent.DeadlineAt) {
		expiresAt = intent.DeadlineAt
	}
	if !expiresAt.After(now) {
		return domain.RunWakeIntent{}, domain.RunWakeLease{}, false,
			errors.New("run wake total budget cannot admit another ownership lease")
	}
	event, err := appendRunWakeEventTx(ctx, tx, intent.RunID,
		events.RunWakeClaimedEvent, "run_wake_coordinator", intent.ID, map[string]any{
			"generation": generation, "attempt_count": generation,
			"execution_started": false, "model_called": false, "tool_called": false,
		}, now)
	if err != nil || event.Sequence <= 0 {
		return domain.RunWakeIntent{}, domain.RunWakeLease{}, false, err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE run_wake_intents SET status = 'leased',
		attempt_count = ?, active_lease_id = ?, updated_at = ? WHERE id = ?`,
		generation, leaseID, ts(now), intent.ID); err != nil {
		return domain.RunWakeIntent{}, domain.RunWakeLease{}, false, err
	}
	lease := domain.RunWakeLease{ID: leaseID, IntentID: intent.ID,
		Generation: generation, OwnerID: ownerID, Status: domain.RunWakeLeaseActive,
		AcquiredAt: now, ExpiresAt: expiresAt}
	if err := lease.Validate(); err != nil {
		return domain.RunWakeIntent{}, domain.RunWakeLease{}, false, err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO run_wake_leases
		(id, intent_id, generation, owner_id, status, acquired_at, expires_at, ended_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, NULL)`, lease.ID, lease.IntentID, lease.Generation,
		lease.OwnerID, lease.Status, ts(lease.AcquiredAt), ts(lease.ExpiresAt)); err != nil {
		return domain.RunWakeIntent{}, domain.RunWakeLease{}, false, err
	}
	stored, err := getRunWakeIntentTx(ctx, tx, intent.ID)
	if err != nil {
		return domain.RunWakeIntent{}, domain.RunWakeLease{}, false, err
	}
	if err := tx.Commit(); err != nil {
		return domain.RunWakeIntent{}, domain.RunWakeLease{}, false, err
	}
	return stored, lease, true, nil
}

func (s *SQLiteStore) ReleaseRunWakeForRetry(ctx context.Context,
	lease domain.RunWakeLease, now time.Time,
) (domain.RunWakeIntent, error) {
	if lease.Status != domain.RunWakeLeaseActive || now.IsZero() {
		return domain.RunWakeIntent{}, errors.New("active run wake lease is required")
	}
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{})
	if err != nil {
		return domain.RunWakeIntent{}, err
	}
	defer func() { _ = tx.Rollback() }()
	storedLease, err := getRunWakeLeaseTx(ctx, tx, lease.ID)
	if err != nil {
		return domain.RunWakeIntent{}, err
	}
	if storedLease.IntentID != lease.IntentID || storedLease.Generation != lease.Generation ||
		storedLease.OwnerID != lease.OwnerID || storedLease.Status != domain.RunWakeLeaseActive ||
		storedLease.ExpiresAt != lease.ExpiresAt || now.After(storedLease.ExpiresAt) {
		return domain.RunWakeIntent{}, errors.New("run wake lease ownership is stale")
	}
	intent, err := getRunWakeIntentTx(ctx, tx, storedLease.IntentID)
	if err != nil {
		return domain.RunWakeIntent{}, err
	}
	if intent.Status != domain.RunWakeLeased || intent.ActiveLeaseID != storedLease.ID ||
		intent.AttemptCount != storedLease.Generation {
		return domain.RunWakeIntent{}, errors.New("run wake intent no longer belongs to this owner")
	}
	var prepared bool
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM run_wake_consumptions
		WHERE intent_id = ? AND generation = ? AND lease_id = ? AND status = 'prepared')`,
		intent.ID, intent.AttemptCount, storedLease.ID).Scan(&prepared); err != nil {
		return domain.RunWakeIntent{}, err
	}
	if prepared {
		return domain.RunWakeIntent{}, apperror.New(apperror.CodeConflict,
			"prepared Run wake handoff must settle before retry")
	}
	delay := runWakeBackoff(intent.BaseBackoffSeconds, intent.MaxBackoffSeconds,
		storedLease.Generation)
	next := now.Add(time.Duration(delay) * time.Second)
	exhausted := intent.AttemptCount >= intent.MaxAttempts || next.After(intent.DeadlineAt)
	eventType := events.RunWakeRetriedEvent
	status := domain.RunWakeQueued
	if exhausted {
		eventType = events.RunWakeExhaustedEvent
		status = domain.RunWakeExhausted
		next = intent.DeadlineAt
	}
	event, err := appendRunWakeEventTx(ctx, tx, intent.RunID, eventType,
		"run_wake_coordinator", intent.ID, map[string]any{
			"generation": storedLease.Generation, "attempt_count": intent.AttemptCount,
			"backoff_seconds": delay, "execution_started": false,
			"model_called": false, "tool_called": false,
		}, now)
	if err != nil || event.Sequence <= 0 {
		return domain.RunWakeIntent{}, err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE run_wake_leases SET status = 'released',
		ended_at = ? WHERE id = ? AND status = 'active'`, ts(now), storedLease.ID); err != nil {
		return domain.RunWakeIntent{}, err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE run_wake_intents SET status = ?,
		next_wake_at = ?, active_lease_id = NULL, updated_at = ? WHERE id = ?`,
		status, ts(next), ts(now), intent.ID); err != nil {
		return domain.RunWakeIntent{}, err
	}
	stored, err := getRunWakeIntentTx(ctx, tx, intent.ID)
	if err != nil {
		return domain.RunWakeIntent{}, err
	}
	if err := tx.Commit(); err != nil {
		return domain.RunWakeIntent{}, err
	}
	return stored, nil
}

const runWakeIntentSelect = `SELECT id, protocol_version, run_id, session_id,
	CASE WHEN EXISTS (SELECT 1 FROM run_wake_consumptions consumption
		WHERE consumption.intent_id = run_wake_intents.id
			AND consumption.status = 'completed') THEN 'completed' ELSE status END,
	max_attempts, attempt_count, initial_delay_seconds, base_backoff_seconds,
	max_backoff_seconds, max_elapsed_seconds, next_wake_at, deadline_at,
	active_lease_id, execution_enabled, background_loop_enabled,
	created_at, updated_at, cancelled_at FROM run_wake_intents `

func scanRunWakeIntent(row scanner) (domain.RunWakeIntent, error) {
	var intent domain.RunWakeIntent
	var status string
	var activeLease sql.NullString
	var executionEnabled, backgroundLoopEnabled int
	var createdAt, updatedAt, nextWakeAt, deadlineAt string
	var cancelledAt sql.NullString
	err := row.Scan(&intent.ID, &intent.ProtocolVersion, &intent.RunID, &intent.SessionID,
		&status, &intent.MaxAttempts, &intent.AttemptCount, &intent.InitialDelaySeconds,
		&intent.BaseBackoffSeconds, &intent.MaxBackoffSeconds, &intent.MaxElapsedSeconds,
		&nextWakeAt, &deadlineAt, &activeLease, &executionEnabled, &backgroundLoopEnabled,
		&createdAt, &updatedAt, &cancelledAt)
	if err != nil {
		return domain.RunWakeIntent{}, err
	}
	intent.Status = domain.RunWakeStatus(status)
	intent.ActiveLeaseID = activeLease.String
	intent.ExecutionEnabled = executionEnabled != 0
	intent.BackgroundLoopEnabled = backgroundLoopEnabled != 0
	intent.NextWakeAt = parseTS(nextWakeAt)
	intent.DeadlineAt = parseTS(deadlineAt)
	intent.CreatedAt = parseTS(createdAt)
	intent.UpdatedAt = parseTS(updatedAt)
	intent.CancelledAt = parseNullableTS(cancelledAt)
	if err := intent.Validate(); err != nil {
		return domain.RunWakeIntent{}, fmt.Errorf("stored run wake intent is invalid: %w", err)
	}
	return intent, nil
}

func scanRunWakeOperation(row scanner) (domain.RunWakeOperation, error) {
	var operation domain.RunWakeOperation
	var action, createdAt string
	err := row.Scan(&operation.KeyDigest, &operation.RequestFingerprint,
		&operation.ProtocolVersion, &action, &operation.IntentID, &operation.RunID,
		&operation.RequestedBy, &operation.EventSequence, &createdAt)
	if err != nil {
		return domain.RunWakeOperation{}, err
	}
	operation.Action = domain.RunWakeAction(action)
	operation.CreatedAt = parseTS(createdAt)
	if err := operation.Validate(); err != nil {
		return domain.RunWakeOperation{}, fmt.Errorf("stored run wake operation is invalid: %w", err)
	}
	return operation, nil
}

func getRunWakeOperationTx(ctx context.Context, tx *sql.Tx,
	keyDigest string,
) (domain.RunWakeOperation, bool, error) {
	operation, err := scanRunWakeOperation(tx.QueryRowContext(ctx, `SELECT
		operation_key_digest, request_fingerprint, protocol_version, action,
		intent_id, run_id, requested_by, event_sequence, created_at
		FROM run_wake_operations WHERE operation_key_digest = ?`, keyDigest))
	if errors.Is(err, sql.ErrNoRows) {
		return domain.RunWakeOperation{}, false, nil
	}
	return operation, err == nil, err
}

func getRunWakeIntentTx(ctx context.Context, tx *sql.Tx,
	id string,
) (domain.RunWakeIntent, error) {
	return scanRunWakeIntent(tx.QueryRowContext(ctx, runWakeIntentSelect+`WHERE id = ?`, id))
}

func getRunWakeLeaseTx(ctx context.Context, tx *sql.Tx,
	id string,
) (domain.RunWakeLease, error) {
	var lease domain.RunWakeLease
	var status, acquiredAt, expiresAt string
	var endedAt sql.NullString
	err := tx.QueryRowContext(ctx, `SELECT id, intent_id, generation, owner_id,
		status, acquired_at, expires_at, ended_at FROM run_wake_leases WHERE id = ?`, id).
		Scan(&lease.ID, &lease.IntentID, &lease.Generation, &lease.OwnerID, &status,
			&acquiredAt, &expiresAt, &endedAt)
	if err != nil {
		return domain.RunWakeLease{}, err
	}
	lease.Status = domain.RunWakeLeaseStatus(status)
	lease.AcquiredAt = parseTS(acquiredAt)
	lease.ExpiresAt = parseTS(expiresAt)
	lease.EndedAt = parseNullableTS(endedAt)
	if err := lease.Validate(); err != nil {
		return domain.RunWakeLease{}, fmt.Errorf("stored run wake lease is invalid: %w", err)
	}
	return lease, nil
}

func insertRunWakeIntentTx(ctx context.Context, tx *sql.Tx,
	intent domain.RunWakeIntent,
) error {
	_, err := tx.ExecContext(ctx, `INSERT INTO run_wake_intents
		(id, protocol_version, run_id, session_id, status, max_attempts, attempt_count,
		 initial_delay_seconds, base_backoff_seconds, max_backoff_seconds,
		 max_elapsed_seconds, next_wake_at, deadline_at, active_lease_id,
		 execution_enabled, background_loop_enabled, created_at, updated_at, cancelled_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, NULL, 0, 0, ?, ?, NULL)`,
		intent.ID, intent.ProtocolVersion, intent.RunID, intent.SessionID, intent.Status,
		intent.MaxAttempts, intent.AttemptCount, intent.InitialDelaySeconds,
		intent.BaseBackoffSeconds, intent.MaxBackoffSeconds, intent.MaxElapsedSeconds,
		ts(intent.NextWakeAt), ts(intent.DeadlineAt), ts(intent.CreatedAt), ts(intent.UpdatedAt))
	return err
}

func insertRunWakeOperationTx(ctx context.Context, tx *sql.Tx,
	operation domain.RunWakeOperation,
) error {
	_, err := tx.ExecContext(ctx, `INSERT INTO run_wake_operations
		(operation_key_digest, request_fingerprint, protocol_version, action,
		 intent_id, run_id, requested_by, event_sequence, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`, operation.KeyDigest,
		operation.RequestFingerprint, operation.ProtocolVersion, operation.Action,
		operation.IntentID, operation.RunID, operation.RequestedBy,
		operation.EventSequence, ts(operation.CreatedAt))
	return err
}

func appendRunWakeEventTx(ctx context.Context, tx *sql.Tx, runID string,
	eventType string, source string, subjectID string, payload any,
	createdAt time.Time,
) (events.Event, error) {
	var missionID string
	if err := tx.QueryRowContext(ctx, `SELECT mission_id FROM runs WHERE id = ?`, runID).
		Scan(&missionID); err != nil {
		return events.Event{}, err
	}
	event, err := events.New(runID, missionID, eventType, source, subjectID, payload)
	if err != nil {
		return events.Event{}, err
	}
	event.CreatedAt = createdAt
	return insertRunEventTx(ctx, tx, event)
}

func sameRunWakeOperationIntent(stored domain.RunWakeOperation,
	requested domain.RunWakeOperation,
) bool {
	return stored.ProtocolVersion == requested.ProtocolVersion &&
		stored.KeyDigest == requested.KeyDigest &&
		stored.RequestFingerprint == requested.RequestFingerprint &&
		stored.Action == requested.Action && stored.RunID == requested.RunID &&
		stored.RequestedBy == requested.RequestedBy &&
		(requested.IntentID == "" || stored.IntentID == requested.IntentID)
}

func reconcileExpiredRunWakeTx(ctx context.Context, tx *sql.Tx,
	intent domain.RunWakeIntent, now time.Time,
) (domain.RunWakeIntent, error) {
	lease, err := getRunWakeLeaseTx(ctx, tx, intent.ActiveLeaseID)
	if err != nil {
		return domain.RunWakeIntent{}, err
	}
	if lease.Status != domain.RunWakeLeaseActive || now.Before(lease.ExpiresAt) {
		return intent, nil
	}
	var prepared int
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS(
		SELECT 1 FROM run_wake_consumptions
		WHERE intent_id = ? AND generation = ? AND lease_id = ? AND status = 'prepared'
	)`, intent.ID, intent.AttemptCount, lease.ID).Scan(&prepared); err != nil {
		return domain.RunWakeIntent{}, err
	}
	if prepared == 1 {
		// A prepared consumption owns this generation durably. Lease expiry cannot
		// manufacture a second generation while its exact handoff is recoverable.
		return intent, nil
	}
	if intent.AttemptCount >= intent.MaxAttempts || !now.Before(intent.DeadlineAt) {
		exhausted, err := exhaustRunWakeTx(ctx, tx, intent, now,
			"lease_expired_budget_exhausted")
		if err != nil {
			return domain.RunWakeIntent{}, err
		}
		if _, err := tx.ExecContext(ctx, `UPDATE run_wake_leases SET status = 'expired',
			ended_at = ? WHERE id = ? AND status = 'active'`, ts(now), lease.ID); err != nil {
			return domain.RunWakeIntent{}, err
		}
		return exhausted, nil
	}
	event, err := appendRunWakeEventTx(ctx, tx, intent.RunID,
		events.RunWakeRetriedEvent, "run_wake_coordinator", intent.ID, map[string]any{
			"generation": intent.AttemptCount, "attempt_count": intent.AttemptCount,
			"backoff_seconds": 0, "reason": "lease_expired",
			"execution_started": false, "model_called": false, "tool_called": false,
		}, now)
	if err != nil || event.Sequence <= 0 {
		return domain.RunWakeIntent{}, err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE run_wake_leases SET status = 'expired',
		ended_at = ? WHERE id = ? AND status = 'active'`, ts(now), lease.ID); err != nil {
		return domain.RunWakeIntent{}, err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE run_wake_intents SET status = 'queued',
		next_wake_at = ?, active_lease_id = NULL, updated_at = ? WHERE id = ?`,
		ts(now), ts(now), intent.ID); err != nil {
		return domain.RunWakeIntent{}, err
	}
	return getRunWakeIntentTx(ctx, tx, intent.ID)
}

func exhaustRunWakeTx(ctx context.Context, tx *sql.Tx, intent domain.RunWakeIntent,
	now time.Time, reason string,
) (domain.RunWakeIntent, error) {
	event, err := appendRunWakeEventTx(ctx, tx, intent.RunID,
		events.RunWakeExhaustedEvent, "run_wake_coordinator", intent.ID, map[string]any{
			"attempt_count": intent.AttemptCount, "reason": reason,
			"execution_started": false, "model_called": false, "tool_called": false,
		}, now)
	if err != nil || event.Sequence <= 0 {
		return domain.RunWakeIntent{}, err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE run_wake_intents SET status = 'exhausted',
		next_wake_at = deadline_at, active_lease_id = NULL, updated_at = ? WHERE id = ?`,
		ts(now), intent.ID); err != nil {
		return domain.RunWakeIntent{}, err
	}
	return getRunWakeIntentTx(ctx, tx, intent.ID)
}

func runWakeBackoff(base int, maximum int, generation int) int {
	delay := base
	for current := 1; current < generation && delay < maximum; current++ {
		if delay > maximum/2 {
			return maximum
		}
		delay *= 2
	}
	if delay > maximum {
		return maximum
	}
	return delay
}
