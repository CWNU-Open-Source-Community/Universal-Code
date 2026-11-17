package store

import (
	"context"
	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/events"
)

func (s *SQLiteStore) approvalToolBoundaryContextCalls(ctx context.Context, checkpoint domain.SupervisorCheckpoint) ([]domain.SupervisorToolCall, bool, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, false, err
	}
	defer func() { _ = tx.Rollback() }()
	value, found, err := approvalContinuationForCheckpointTx(ctx, tx, checkpoint)
	if err != nil || !found {
		return nil, found, err
	}
	rows, err := tx.QueryContext(ctx, `SELECT call.run_id,call.turn,call.attempt_id,call.round,call.position,
	 call.model_attempt,call.call_id,call.stream_response_id,call.stream_item_id,call.stream_call_id,
	 call.tool_name,call.payload_json,call.authority_json,call.status,call.result_json,call.error_code,call.created_at,call.completed_at
	 FROM run_events event JOIN run_supervisor_tool_calls call ON call.run_id=event.run_id AND call.attempt_id=event.subject_id
	 AND call.turn=json_extract(event.payload_json,'$.turn')
	 WHERE event.run_id=? AND event.type=? AND event.source='run_supervisor' AND call.turn<? AND call.status!='pending'
	 AND json_extract(event.payload_json,'$.tool_round_boundary')=1 AND json_extract(event.payload_json,'$.approval_continuation_handoff_id')=?
	 AND json_extract(event.payload_json,'$.operator_message_id')=?
	 ORDER BY call.turn DESC,call.round DESC,call.position LIMIT ?`, checkpoint.RunID, events.AgentTurnCompletedEvent, checkpoint.NextTurn, value.Handoff.Operation.ID, value.Handoff.Items[0].MessageID, domain.MaxSupervisorBoundaryContextCalls)
	if err != nil {
		return nil, true, err
	}
	defer rows.Close()
	var calls []domain.SupervisorToolCall
	for rows.Next() {
		call, err := scanSupervisorToolCall(rows)
		if err != nil {
			return nil, true, err
		}
		calls = append(calls, call)
	}
	return calls, true, rows.Err()
}
