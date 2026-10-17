package store

import (
	"context"
	"database/sql"
	"errors"

	"cyberagent-workbench/internal/apperror"
	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/events"
	"cyberagent-workbench/internal/idgen"
	"cyberagent-workbench/internal/session"
)

// A tool boundary is an internal segment of the same accepted input. Its
// original delivery stays in the journal; the next segment prepares that same
// message atomically, so cancellation/restart always has an exact owner.
func prepareSupervisorToolBoundaryTx(ctx context.Context, tx *sql.Tx, run domain.Run,
	checkpoint domain.SupervisorCheckpoint,
) (domain.OperatorSteeringMessage, string, error) {
	var messageID, handoffID string
	// Ask can complete the original handoff with root_wait while its input is
	// still prepared. Approval resumes that input under a new execution lease;
	// it must remain eligible for further tool segments until finally committed.
	err := tx.QueryRowContext(ctx, `SELECT delivery.message_id,operation.id
		FROM operator_steering_deliveries delivery
		JOIN operator_steering_messages message ON message.id=delivery.message_id
		JOIN run_execution_handoff_items item ON item.message_id=message.id
		JOIN run_execution_handoff_operations operation ON operation.id=item.operation_id
		WHERE delivery.run_id=? AND delivery.attempt_id=? AND delivery.turn=? AND delivery.status='prepared'
		AND message.status='pending' AND message.session_id=? AND message.content=?
		AND operation.run_id=message.run_id AND operation.session_id=message.session_id
		AND (NOT EXISTS (SELECT 1 FROM run_execution_handoff_results result WHERE result.operation_id=operation.id)
			OR EXISTS (SELECT 1 FROM run_execution_handoff_results result WHERE result.operation_id=operation.id
				AND result.status='completed' AND result.stop_reason='root_wait' AND result.error_code=''))
		ORDER BY operation.event_sequence DESC LIMIT 1`, run.ID, checkpoint.AttemptID, checkpoint.NextTurn,
		run.SessionID, checkpoint.PendingInput).Scan(&messageID, &handoffID)
	if errors.Is(err, sql.ErrNoRows) {
		continuation, found, lookupErr := approvalContinuationForCheckpointTx(ctx, tx, checkpoint)
		if lookupErr != nil || !found {
			return domain.OperatorSteeringMessage{}, "", lookupErr
		}
		messageID, handoffID = continuation.Handoff.Items[0].MessageID, continuation.Handoff.Operation.ID
		err = nil
	}
	if err != nil {
		return domain.OperatorSteeringMessage{}, "", err
	}
	rounds, err := supervisorNativeToolRoundCountTx(ctx, tx, checkpoint, true)
	if err != nil {
		return domain.OperatorSteeringMessage{}, "", err
	}
	if rounds != domain.MaxSupervisorToolRounds {
		return domain.OperatorSteeringMessage{}, "", apperror.New(apperror.CodeFailedPrecondition,
			"Internal tool continuation requires a completed tool-round boundary")
	}
	root, found, err := getRootAgentTx(ctx, tx, run.ID)
	if err != nil || !found {
		return domain.OperatorSteeringMessage{}, "", err
	}
	budget, err := effectiveRootBudgetTx(ctx, tx, run, root.ID)
	if err != nil {
		return domain.OperatorSteeringMessage{}, "", err
	}
	if checkpoint.NextTurn >= budget.MaxTurns || (budget.MaxTokens > 0 && checkpoint.TotalTokens >= budget.MaxTokens) {
		return domain.OperatorSteeringMessage{}, "", apperror.New(apperror.CodeResourceExhausted,
			"Run budget is exhausted before the next tool segment")
	}
	if budget.TimeoutSeconds > 0 && checkpoint.ExecutionMillis >= budget.TimeoutSeconds*1000 {
		return domain.OperatorSteeringMessage{}, "", apperror.New(apperror.CodeDeadlineExceeded,
			"Run execution timeout is exhausted before the next tool segment")
	}
	if budget.MaxToolCalls > 0 {
		var consumed int64
		if err := tx.QueryRowContext(ctx, `SELECT COALESCE((SELECT consumed FROM run_tool_usage WHERE run_id=?),0)`, run.ID).Scan(&consumed); err != nil {
			return domain.OperatorSteeringMessage{}, "", err
		}
		if consumed >= budget.MaxToolCalls {
			return domain.OperatorSteeringMessage{}, "", apperror.New(apperror.CodeResourceExhausted,
				"Run tool-call budget is exhausted before the next tool segment")
		}
	}
	message, err := getOperatorSteeringMessageTx(ctx, tx, messageID)
	return message, handoffID, err
}

func startSupervisorToolBoundaryTx(ctx context.Context, tx *sql.Tx, run domain.Run,
	mission domain.Mission, checkpoint domain.SupervisorCheckpoint, message domain.OperatorSteeringMessage, approvalHandoffID string,
) (domain.SupervisorCheckpoint, error) {
	checkpoint.Phase = domain.SupervisorTurnStarted
	checkpoint.AttemptID = idgen.New("attempt")
	checkpoint.PendingInput = message.Content
	checkpoint.PendingImageCount = message.ImageCount
	checkpoint.PendingAttachmentCount = message.AttachmentCount
	checkpoint.PendingAttachmentCount = message.AttachmentCount
	if err := upsertSupervisorCheckpointTx(ctx, tx, checkpoint); err != nil {
		return checkpoint, err
	}
	if approvalHandoffID == "" {
		if _, err := prepareOperatorSteeringDeliveryTx(ctx, tx, run, checkpoint, message, checkpoint.UpdatedAt); err != nil {
			return checkpoint, err
		}
	} else if message.Status != domain.OperatorSteeringCommitted {
		return checkpoint, apperror.New(apperror.CodeConflict, "Approval tool segment lost its committed original input")
	}
	root, _, changed, err := syncRootAgentTx(ctx, tx, run, mission, rootAgentProjection{
		Status: domain.AgentRunning, ActiveAttemptID: checkpoint.AttemptID,
		TurnsUsed: int64(checkpoint.NextTurn - 1), TokensUsed: checkpoint.TotalTokens,
	}, checkpoint.UpdatedAt)
	if err != nil {
		return checkpoint, err
	}
	if changed {
		if _, err := createAgentGraphSnapshotTx(ctx, tx, run); err != nil {
			return checkpoint, err
		}
	}
	err = appendSupervisorEventTx(ctx, tx, run, events.AgentTurnStartedEvent, "run_supervisor", checkpoint.AttemptID,
		map[string]any{"agent_id": root.ID, "turn": checkpoint.NextTurn, "attempt_id": checkpoint.AttemptID,
			"recovered": false, "tool_boundary_continuation": true, "operator_message_id": message.ID, "approval_continuation_handoff_id": approvalHandoffID})
	return checkpoint, err
}

func supervisorToolBoundaryReplayTx(ctx context.Context, tx *sql.Tx, previous, current domain.SupervisorCheckpoint) (bool, error) {
	var count int
	err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM run_events event
		JOIN operator_steering_deliveries delivery ON delivery.message_id=json_extract(event.payload_json,'$.operator_message_id')
		WHERE event.run_id=? AND event.subject_id=? AND event.type=? AND event.source='run_supervisor'
		AND json_extract(event.payload_json,'$.tool_round_boundary')=1
		AND json_extract(event.payload_json,'$.turn')=?
		AND delivery.run_id=event.run_id AND delivery.attempt_id=? AND delivery.turn=? AND delivery.status='prepared'`,
		previous.RunID, previous.AttemptID, events.AgentTurnCompletedEvent, previous.NextTurn, current.AttemptID, current.NextTurn).Scan(&count)
	if err == nil && count == 0 {
		value, found, lookupErr := approvalContinuationForCheckpointTx(ctx, tx, current)
		if lookupErr != nil {
			return false, lookupErr
		}
		if found {
			err = tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM run_events WHERE run_id=? AND subject_id=? AND type=? AND source='run_supervisor'
			AND json_extract(payload_json,'$.turn')=? AND json_extract(payload_json,'$.tool_round_boundary')=1
			AND json_extract(payload_json,'$.approval_continuation_handoff_id')=? AND json_extract(payload_json,'$.operator_message_id')=?`,
				previous.RunID, previous.AttemptID, events.AgentTurnCompletedEvent, previous.NextTurn, value.Handoff.Operation.ID, value.Handoff.Items[0].MessageID).Scan(&count)
		}
	}
	return count == 1, err
}

// Store an explicit user input only once even when several internal segments
// consume it. The final segment (or M failure closure) binds the same row.
func saveSupervisorOperatorInputTx(ctx context.Context, tx *sql.Tx, run domain.Run,
	checkpoint domain.SupervisorCheckpoint,
) (session.Message, error) {
	if continuation, found, err := approvalContinuationForCheckpointTx(ctx, tx, checkpoint); err != nil {
		return session.Message{}, err
	} else if found {
		return scanSessionMessage(tx.QueryRowContext(ctx, `SELECT id,session_id,role,content,provenance_version,
			source_kind,source_ref,content_sha256,instruction_authorized,token_estimate,compacted,created_at
			FROM session_messages WHERE id=?`, continuation.UserMessageID))
	}
	var originalID int64
	err := tx.QueryRowContext(ctx, `SELECT json_extract(event.payload_json,'$.user_message_id')
		FROM operator_steering_deliveries current
		JOIN run_events event ON event.run_id=current.run_id
		AND json_extract(event.payload_json,'$.operator_message_id')=current.message_id
		JOIN operator_steering_deliveries prior ON prior.message_id=current.message_id
		AND prior.attempt_id=event.subject_id AND prior.run_id=current.run_id AND prior.status='superseded'
		WHERE current.run_id=? AND current.attempt_id=? AND current.status='prepared'
		AND event.type=? AND event.source='run_supervisor'
		AND json_extract(event.payload_json,'$.tool_round_boundary')=1
		ORDER BY event.sequence LIMIT 1`, run.ID, checkpoint.AttemptID, events.AgentTurnCompletedEvent).Scan(&originalID)
	if errors.Is(err, sql.ErrNoRows) {
		return saveSessionMessageTx(ctx, tx, session.NewMessage(run.SessionID, "user", checkpoint.PendingInput))
	}
	if err != nil {
		return session.Message{}, err
	}
	message, err := scanSessionMessage(tx.QueryRowContext(ctx, `SELECT id,session_id,role,content,provenance_version,
		source_kind,source_ref,content_sha256,instruction_authorized,token_estimate,compacted,created_at
		FROM session_messages WHERE id=?`, originalID))
	if err != nil {
		return session.Message{}, err
	}
	if message.SessionID != run.SessionID || message.Role != "user" || message.Content != checkpoint.PendingInput ||
		message.Provenance.SourceKind != session.SourceOperatorMessage || !message.Provenance.InstructionAuthorized ||
		message.Provenance.ContentSHA256 != session.ContentSHA256(checkpoint.PendingInput) {
		return session.Message{}, apperror.New(apperror.CodeFailedPrecondition, "Internal tool continuation lost its exact original user input")
	}
	return message, nil
}

// ToolBoundaryContextCalls is a read-only projection of the previous segments
// of the exact currently prepared input, including terminal error results.
func (s *SQLiteStore) ToolBoundaryContextCalls(ctx context.Context, checkpoint domain.SupervisorCheckpoint) ([]domain.SupervisorToolCall, error) {
	if calls, found, err := s.approvalToolBoundaryContextCalls(ctx, checkpoint); err != nil || found {
		return calls, err
	}
	rows, err := s.db.QueryContext(ctx, `SELECT call.run_id,call.turn,call.attempt_id,call.round,call.position,
		call.model_attempt,call.call_id,call.stream_response_id,call.stream_item_id,call.stream_call_id,
		call.tool_name,call.payload_json,call.authority_json,call.status,call.result_json,call.error_code,call.created_at,call.completed_at
		FROM operator_steering_deliveries current
		JOIN run_events event ON event.run_id=current.run_id AND event.type=? AND event.source='run_supervisor'
		AND json_extract(event.payload_json,'$.tool_round_boundary')=1
		AND json_extract(event.payload_json,'$.operator_message_id')=current.message_id
		JOIN operator_steering_deliveries prior ON prior.message_id=current.message_id AND prior.run_id=current.run_id
		AND prior.attempt_id=event.subject_id AND prior.turn=json_extract(event.payload_json,'$.turn') AND prior.status='superseded'
		JOIN run_execution_handoff_items item ON item.message_id=current.message_id AND item.operation_id=json_extract(event.payload_json,'$.handoff_operation_id')
		JOIN run_supervisor_tool_calls call ON call.run_id=event.run_id AND call.attempt_id=event.subject_id AND call.turn=prior.turn
		WHERE current.run_id=? AND current.attempt_id=? AND current.turn=? AND current.status='prepared'
		AND call.status!='pending' ORDER BY call.turn DESC,call.round DESC,call.position LIMIT 32`,
		events.AgentTurnCompletedEvent, checkpoint.RunID, checkpoint.AttemptID, checkpoint.NextTurn)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var calls []domain.SupervisorToolCall
	for rows.Next() {
		call, err := scanSupervisorToolCall(rows)
		if err != nil {
			return nil, err
		}
		calls = append(calls, call)
	}
	return calls, rows.Err()
}
