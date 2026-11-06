package store

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"cyberagent-workbench/internal/apperror"
	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/events"
	"cyberagent-workbench/internal/runmutation"
)

// Notification claims are independent of interpreter claims: an abandoned
// interpreter cannot delay a completed execution's saved results.
func (s *SQLiteStore) ListWorkflowSummaryCandidates(ctx context.Context, now time.Time, limit int) ([]domain.WorkflowExecution, error) {
	if now.IsZero() || limit < 1 || limit > 128 {
		return nil, apperror.New(apperror.CodeInvalidArgument, "workflow summary time or page bound is invalid")
	}
	rows, err := s.db.QueryContext(ctx, workflowExecutionSelect+` WHERE id IN (
	 SELECT n.execution_id FROM workflow_completion_notifications n
	 JOIN workflow_executions e ON e.id=n.execution_id JOIN runs r ON r.id=e.run_id
	 WHERE n.status='pending' AND n.next_attempt_at<=? AND (n.claim_owner='' OR n.claim_until<=?)
	 AND (r.status IN ('completed','failed','cancelled') OR (r.status='running' AND NOT EXISTS (
	 SELECT 1 FROM run_execution_leases l WHERE l.run_id=r.id AND l.status='active' AND l.expires_at>?)))
	 ORDER BY n.next_attempt_at,n.execution_id LIMIT ?)
	 ORDER BY (SELECT next_attempt_at FROM workflow_completion_notifications WHERE execution_id=id),id`, ts(now), ts(now), ts(now), limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var executions []domain.WorkflowExecution
	for rows.Next() {
		e, err := scanWorkflowExecution(rows)
		if err != nil {
			return nil, err
		}
		executions = append(executions, e)
	}
	return executions, rows.Err()
}

func (s *SQLiteStore) ClaimWorkflowSummary(ctx context.Context, executionID, owner string, now time.Time) (int64, bool, error) {
	if !domain.ValidAgentID(owner) || now.IsZero() {
		return 0, false, apperror.New(apperror.CodeInvalidArgument, "workflow summary requires an owner and current time")
	}
	var generation int64
	err := s.db.QueryRowContext(ctx, `UPDATE workflow_completion_notifications SET claim_owner=?,claim_until=?
     WHERE execution_id=? AND status='pending' AND next_attempt_at<=? AND (claim_owner='' OR claim_until<=?)
     RETURNING wake_generation`, owner, ts(now.Add(15*time.Minute)), executionID, ts(now), ts(now)).Scan(&generation)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, false, nil
	}
	return generation, err == nil, err
}

// Only an exhausted wake which never created an actual handoff can be replaced.
// Inspect operation digests, since a crash can precede the consumption's binding
// of handoff_operation_id even after execution has started. The claim and the
// generation CAS fence this decision in the same SQLite write transaction.
func (s *SQLiteStore) AdvanceWorkflowSummaryWake(ctx context.Context, executionID, owner, wakeID string, generation int64) (bool, error) {
	result, err := s.db.ExecContext(ctx, `UPDATE workflow_completion_notifications SET wake_generation=wake_generation+1
     WHERE execution_id=? AND status='pending' AND claim_owner=? AND claim_until>? AND wake_generation=?
     AND EXISTS (SELECT 1 FROM workflow_executions e JOIN runs r ON r.id=e.run_id
      JOIN run_wake_intents w ON w.run_id=e.run_id AND w.session_id=r.session_id
      WHERE e.id=execution_id AND e.status='completed' AND r.status='running' AND w.id=? AND w.status='exhausted')
     AND NOT EXISTS (SELECT 1 FROM run_wake_consumptions c
      JOIN run_execution_handoff_operations o ON o.operation_key_digest=c.handoff_operation_key_digest
      WHERE c.intent_id=?)`, executionID, owner, ts(time.Now().UTC()), generation, wakeID, wakeID)
	if err != nil {
		return false, err
	}
	n, err := result.RowsAffected()
	return n == 1, err
}

// A cancelled obligation retains the completed workflow output. In particular,
// ending the parent Run never grants authority to restart it for a summary.
func (s *SQLiteStore) FinishWorkflowSummary(ctx context.Context, executionID, owner, status, reason string, next time.Time) error {
	if status != "pending" && status != "delivered" && status != "cancelled" {
		return apperror.New(apperror.CodeInvalidArgument, "invalid workflow summary disposition")
	}
	result, err := s.db.ExecContext(ctx, `UPDATE workflow_completion_notifications
	 SET status=?,reason=?,next_attempt_at=?,claim_owner='',claim_until=''
	 WHERE execution_id=? AND status='pending' AND claim_owner=? AND claim_until>?`, status, reason, ts(next), executionID, owner, ts(time.Now().UTC()))
	if err != nil {
		return err
	}
	n, err := result.RowsAffected()
	if err == nil && n != 1 {
		return apperror.New(apperror.CodeConflict, "workflow summary claim was superseded")
	}
	return err
}

// Inbox commitment occurs before model execution. The exact completion event
// and assistant message commit together; a handoff acknowledgement may lag them.
func (s *SQLiteStore) WorkflowSummaryDelivered(ctx context.Context, executionID string) (bool, error) {
	var runID string
	if err := s.db.QueryRowContext(ctx, `SELECT run_id FROM workflow_executions WHERE id=?`, executionID).Scan(&runID); err != nil {
		return false, err
	}
	key := "specialist-summary-" + runmutation.Fingerprint("workflow-summary.v1", executionID)
	operationDigest := runmutation.Fingerprint("operator_steering_operation.v1", runID, key)
	var count int
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM operator_steering_operations o
     JOIN operator_steering_messages m ON m.id=o.message_id AND m.run_id=o.run_id
     JOIN operator_steering_deliveries d ON d.message_id=m.id AND d.run_id=m.run_id AND d.status='committed'
     JOIN run_events event ON event.run_id=d.run_id AND event.subject_id=d.attempt_id
     WHERE o.operation_key_digest=? AND o.requested_by='specialist_completion' AND m.status='committed'
     AND event.type=? AND event.source='run_supervisor'
     AND json_extract(event.payload_json,'$.attempt_id')=d.attempt_id
     AND json_extract(event.payload_json,'$.turn')=d.turn
     AND json_extract(event.payload_json,'$.user_message_id')=m.session_message_id
     AND json_type(event.payload_json,'$.assistant_message_id')='integer'`, operationDigest, events.AgentTurnCompletedEvent).Scan(&count)
	if err == nil && count > 1 {
		return false, apperror.New(apperror.CodeConflict, "workflow summary has ambiguous completion events")
	}
	return count == 1, err
}
