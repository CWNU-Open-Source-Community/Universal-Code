package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"cyberagent-workbench/internal/apperror"
	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/events"
	"cyberagent-workbench/internal/session"
)

func (s *SQLiteStore) GetThreadTurnFailure(ctx context.Context, runID, messageID string) (domain.ThreadTurnFailure, bool, error) {
	return getThreadTurnFailure(ctx, s.db, runID, messageID)
}

func (s *SQLiteStore) GetLatestThreadTurnFailure(ctx context.Context, threadID string) (domain.ThreadTurnFailure, bool, error) {
	var runID, messageID string
	err := s.db.QueryRowContext(ctx, `SELECT event.run_id,event.subject_id FROM run_events event
		JOIN threads thread ON thread.active_run_id=event.run_id
		WHERE thread.id=? AND event.type=? AND event.source='thread_turn'
		AND json_extract(event.payload_json,'$.handoff_operation_id')=(SELECT id FROM run_execution_handoff_operations WHERE run_id=event.run_id ORDER BY event_sequence DESC LIMIT 1)
		ORDER BY event.sequence DESC LIMIT 1`, threadID, events.ThreadTurnFailedEvent).Scan(&runID, &messageID)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.ThreadTurnFailure{}, false, nil
	}
	if err != nil {
		return domain.ThreadTurnFailure{}, false, err
	}
	return s.GetThreadTurnFailure(ctx, runID, messageID)
}

func getThreadTurnFailure(ctx context.Context, q interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}, runID, messageID string) (domain.ThreadTurnFailure, bool, error) {
	var value domain.ThreadTurnFailure
	var payload string
	var sequence int64
	err := q.QueryRowContext(ctx, `SELECT payload_json, sequence FROM run_events WHERE run_id=? AND type=? AND source='thread_turn' AND subject_id=? ORDER BY sequence`, runID, events.ThreadTurnFailedEvent, messageID).Scan(&payload, &sequence)
	if errors.Is(err, sql.ErrNoRows) {
		return value, false, nil
	}
	if err != nil {
		return value, false, err
	}
	if err := json.Unmarshal([]byte(payload), &value); err != nil {
		return value, false, err
	}
	if value.RunID != runID || value.MessageID != messageID || value.Turn <= 0 || value.UserMessageID <= 0 || value.OutcomeMessageID <= 0 || !domain.ValidAgentID(value.ThreadID) || !domain.ValidAgentID(value.HandoffOperationID) || !domain.ValidAgentID(value.AttemptID) {
		return value, false, apperror.New(apperror.CodeFailedPrecondition, "persisted Thread failure binding is invalid")
	}
	var exact int
	inputProvenance := `AND original.role='user' AND original.source_kind='operator_message' AND original.instruction_authorized=1`
	if exists, err := storeTableExists(ctx, q, "thread_peer_messages"); err != nil {
		return value, false, err
	} else if exists {
		inputProvenance = `AND ((original.role='user' AND original.source_kind='operator_message' AND original.instruction_authorized=1
		 AND NOT EXISTS(SELECT 1 FROM thread_peer_messages peer WHERE peer.steering_message_id=message.id))
		 OR (original.role='tool' AND original.source_kind='peer_message' AND original.instruction_authorized=0
		 AND EXISTS(SELECT 1 FROM thread_peer_messages peer WHERE peer.steering_message_id=message.id
		 AND original.source_ref='thread:'||peer.source_thread_id||'/peer-message:'||peer.id)))`
	}
	err = q.QueryRowContext(ctx, `SELECT COUNT(*) FROM operator_steering_messages message
		JOIN operator_steering_deliveries delivery ON delivery.message_id=message.id
		JOIN session_messages original ON original.id=message.session_message_id
		JOIN session_messages outcome ON outcome.id=?
		JOIN run_execution_handoff_items item ON item.message_id=message.id
		JOIN run_execution_handoff_results result ON result.operation_id=item.operation_id
		JOIN run_execution_handoff_operations operation ON operation.id=item.operation_id
		JOIN thread_runs binding ON binding.run_id=message.run_id AND binding.session_id=message.session_id
		WHERE message.id=? AND message.run_id=? AND message.status='committed'
		AND original.id=? AND original.session_id=message.session_id
		AND original.content=message.content
		`+inputProvenance+`
		AND original.content_sha256=message.content_sha256
		AND outcome.session_id=message.session_id AND outcome.role='tool'
		AND outcome.source_kind='tool_result' AND outcome.source_ref=item.operation_id AND outcome.instruction_authorized=0
		AND delivery.status='committed' AND delivery.attempt_id=? AND delivery.turn=?
		AND delivery.run_id=message.run_id AND operation.run_id=message.run_id AND operation.session_id=message.session_id
		AND item.operation_id=? AND result.status='failed' AND UPPER(result.error_code)=? AND binding.thread_id=?`, value.OutcomeMessageID, messageID, runID, value.UserMessageID, value.AttemptID, value.Turn, value.HandoffOperationID, value.ErrorCode, value.ThreadID).Scan(&exact)
	if err != nil {
		return value, false, err
	}
	if exact != 1 {
		return value, false, apperror.New(apperror.CodeFailedPrecondition, "Thread failed turn no longer matches its exact consumed message")
	}
	if value.FailureStage != "" {
		stage, err := recordedThreadContextFailureStage(ctx, q, value.HandoffOperationID)
		if err == nil && stage == "" {
			stage, err = recordedThreadModelFailureStage(ctx, q, runID, value.AttemptID)
		}
		if err != nil {
			return value, false, err
		}
		if stage != value.FailureStage {
			return value, false, apperror.New(apperror.CodeFailedPrecondition, "Thread failure stage does not match its recorded model failure")
		}
	}
	value.EventSequence = sequence
	return value, true, nil
}

// EndFailedThreadTurn closes only an observed failed product turn. It never
// retries a model/tool, abandons queued followups, or terminates the Run.
func (s *SQLiteStore) EndFailedThreadTurn(ctx context.Context, threadID, runID, handoffID string) (domain.ThreadTurnFailure, bool, error) {
	var empty domain.ThreadTurnFailure
	if !domain.ValidAgentID(threadID) || !domain.ValidAgentID(runID) || !domain.ValidAgentID(handoffID) {
		return empty, false, apperror.New(apperror.CodeInvalidArgument, "Thread failure identity is invalid")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return empty, false, err
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx, `UPDATE runs SET updated_at=updated_at WHERE id=?`, runID); err != nil {
		return empty, false, err
	}
	handoff, found, err := getRunExecutionHandoffByID(ctx, tx, handoffID)
	if err != nil {
		return empty, false, err
	}
	if !found || handoff.Operation.RunID != runID || handoff.Result == nil || handoff.Result.Status != domain.RunExecutionHandoffFailed {
		return empty, false, apperror.New(apperror.CodeConflict, "Thread failure handoff is not a failed outcome")
	}
	for _, item := range handoff.Items {
		value, sealed, err := getThreadTurnFailure(ctx, tx, runID, item.MessageID)
		if err != nil {
			return empty, false, err
		}
		if sealed && value.HandoffOperationID == handoffID && value.ThreadID == threadID {
			return value, true, tx.Commit()
		}
	}
	threadRecord, err := scanThread(tx.QueryRowContext(ctx, threadSelect+` WHERE id=?`, threadID))
	if err != nil {
		return empty, false, err
	}
	run, err := getRunControlRunTx(ctx, tx, runID)
	if err != nil {
		return empty, false, err
	}
	if threadRecord.Status != domain.ThreadActive || threadRecord.ActiveRunID != runID || threadRecord.MissionID != run.MissionID || run.Terminal() {
		return empty, false, apperror.New(apperror.CodeConflict, "Thread failed turn is no longer active")
	}
	if err := requireNoActiveRunControlLeaseTx(ctx, tx, runID, time.Now().UTC()); err != nil {
		return empty, false, err
	}
	checkpoint, found, err := getSupervisorCheckpointTx(ctx, tx, runID)
	if err != nil {
		return empty, false, err
	}
	if !found || (checkpoint.Phase != domain.SupervisorTurnFailed && checkpoint.Phase != domain.SupervisorTurnStarted) {
		return empty, false, nil
	}
	var messageID string
	err = tx.QueryRowContext(ctx, `SELECT delivery.message_id FROM operator_steering_deliveries delivery JOIN run_execution_handoff_items item ON item.message_id=delivery.message_id WHERE delivery.run_id=? AND delivery.attempt_id=? AND delivery.status='prepared' AND item.operation_id=?`, runID, checkpoint.AttemptID, handoffID).Scan(&messageID)
	if errors.Is(err, sql.ErrNoRows) {
		return empty, false, nil
	}
	if err != nil {
		return empty, false, err
	}
	message, err := getOperatorSteeringMessageTx(ctx, tx, messageID)
	if err != nil {
		return empty, false, err
	}
	if message.Status != domain.OperatorSteeringPending || message.Content != checkpoint.PendingInput {
		return empty, false, apperror.New(apperror.CodeConflict, "Thread failed input does not match the prepared turn")
	}
	var latest string
	if err := tx.QueryRowContext(ctx, `SELECT id FROM run_execution_handoff_operations WHERE run_id=? ORDER BY event_sequence DESC LIMIT 1`, runID).Scan(&latest); err != nil {
		return empty, false, err
	}
	if latest != handoffID {
		return empty, false, apperror.New(apperror.CodeConflict, "A later execution owns the Thread outcome")
	}
	if err := settleRejectedHostProposalFailureTx(ctx, tx, run, checkpoint, handoff); err != nil {
		return empty, false, err
	}
	if err := settleFileApplyBeforeBoundaryFailureTx(ctx, tx, run, checkpoint, handoff); err != nil {
		return empty, false, err
	}
	if err := requireThreadToolEffectsSettledTx(ctx, tx, runID); err != nil {
		return empty, false, err
	}
	outcome := fmt.Sprintf("System-recorded failed turn %d. Execution outcome: %s. Handoff: %s. Attempt: %s. The preceding input remains part of this conversation. A new request starts a new turn and must account for completed actions instead of replaying them. The tool summaries below are bounded previews, not complete output.\n", checkpoint.NextTurn, handoff.Result.ErrorCode, handoffID, checkpoint.AttemptID)
	if handoff.Result.StopReason == "observed_failed_web_fetch" {
		outcome += "The historical continuation did not retain its original machine-readable error code. FAILED_PRECONDITION labels this observed failed boundary, not the original model cause. No model or tool was rerun while recording this boundary. The original recorded error text follows.\n"
	}
	lastError := []rune(sanitizeSupervisorText(checkpoint.LastError))
	if len(lastError) > 512 {
		lastError = append(lastError[:512], []rune(" [error preview truncated]")...)
	}
	outcome += fmt.Sprintf("Stopped at %s. Last Supervisor error preview: %s\n", handoff.Result.StopReason, string(lastError))
	toolRows, err := tx.QueryContext(ctx, `SELECT call_id,tool_name,status,result_json,error_code FROM run_supervisor_tool_calls WHERE run_id=? AND attempt_id=? ORDER BY round,position`, runID, checkpoint.AttemptID)
	if err != nil {
		return empty, false, err
	}
	for toolRows.Next() {
		var id, name, status, result, errorCode string
		if err := toolRows.Scan(&id, &name, &status, &result, &errorCode); err != nil {
			_ = toolRows.Close()
			return empty, false, err
		}
		if len([]rune(outcome)) >= 8192 {
			outcome += "Additional tool summaries omitted; inspect the exact attempt and handoff records above for all results.\n"
			break
		}
		if name == "workspace_apply" {
			outcome += fileApplyBoundaryFailureEvidence(result)
		}
		call := domain.SupervisorToolCall{RunID: runID, Turn: checkpoint.NextTurn, AttemptID: checkpoint.AttemptID,
			CallID: id, ToolName: name, Status: domain.SupervisorToolCallStatus(status), ResultJSON: result, ErrorCode: errorCode}
		if effect := domain.ObservedSupervisorToolEffect(call); effect != nil {
			encoded, _ := json.Marshal(struct {
				Effect   *domain.SupervisorToolEffect `json:"observation"`
				Original domain.HistoryReadRequest    `json:"original_result"`
			}{effect, domain.SupervisorToolResultReference(call)})
			outcome += fmt.Sprintf("Recorded tool %s (%s): %s; historical effect only, no current authority or filesystem guarantee: %s\n", id, name, status, encoded)
			continue
		}
		preview := []rune(sanitizeSupervisorText(result))
		if len(preview) > 512 {
			preview = append(preview[:512], []rune(" [preview truncated]")...)
		}
		outcome += fmt.Sprintf("Recorded tool %s (%s): %s; result SHA256 %s; preview %s\n", id, name, status, session.ContentSHA256(result), string(preview))
	}
	if err := toolRows.Err(); err != nil {
		_ = toolRows.Close()
		return empty, false, err
	}
	_ = toolRows.Close()
	original, err := saveSupervisorOperatorInputTx(ctx, tx, run, checkpoint)
	if err != nil {
		return empty, false, err
	}
	if _, _, err := commitOperatorSteeringDeliveryTx(ctx, tx, run, checkpoint, original, time.Now().UTC()); err != nil {
		return empty, false, err
	}
	corrections, err := commitMidTurnSteeringTx(ctx, tx, run, checkpoint, true)
	if err != nil {
		return empty, false, err
	}
	if corrections > 0 {
		outcome += fmt.Sprintf("Accepted current-turn corrections: %d. This failed execution did not complete their requested work.\n", corrections)
	}
	control, err := saveSessionMessageTx(ctx, tx, session.NewEvidenceMessage(run.SessionID, session.SourceToolResult, handoffID, outcome))
	if err != nil {
		return empty, false, err
	}
	value := domain.ThreadTurnFailure{ThreadID: threadID, RunID: runID, HandoffOperationID: handoffID, MessageID: messageID, AttemptID: checkpoint.AttemptID, Turn: checkpoint.NextTurn, UserMessageID: original.ID, OutcomeMessageID: control.ID, ErrorCode: strings.ToUpper(handoff.Result.ErrorCode)}
	if value.ErrorCode == string(apperror.CodeResourceExhausted) && handoff.Result.StopReason == domain.ThreadFailureContextWindowExceeded {
		value.FailureStage = domain.ThreadFailureContextWindowExceeded
	}
	if value.ErrorCode == string(apperror.CodeFailedPrecondition) {
		value.FailureStage, err = recordedThreadModelFailureStage(ctx, tx, runID, checkpoint.AttemptID)
		if err != nil {
			return empty, false, err
		}
	}
	event, err := events.New(runID, run.MissionID, events.ThreadTurnFailedEvent, "thread_turn", messageID, value)
	if err != nil {
		return empty, false, err
	}
	event, err = insertRunEventTx(ctx, tx, event)
	if err != nil {
		return empty, false, err
	}
	value.EventSequence = event.Sequence
	if exists, err := storeTableExists(ctx, tx, "thread_peer_messages"); err != nil {
		return empty, false, err
	} else if exists {
		if _, err := tx.ExecContext(ctx, `UPDATE thread_peer_messages SET error=? WHERE steering_message_id=? AND error='' AND delivered_at IS NULL`, value.ErrorCode, messageID); err != nil {
			return empty, false, err
		}
	}
	checkpoint.NextTurn++
	checkpoint.Phase = domain.SupervisorIdle
	checkpoint.AttemptID, checkpoint.PendingInput, checkpoint.RepairReason, checkpoint.LastError = "", "", "", ""
	checkpoint.PendingImageCount = 0
	checkpoint.PendingAttachmentCount = 0
	checkpoint.PendingAttachmentCount = 0
	checkpoint.RepairPhase = domain.ProtocolRepairNone
	checkpoint.UpdatedAt = time.Now().UTC()
	if err := upsertSupervisorCheckpointTx(ctx, tx, checkpoint); err != nil {
		return empty, false, err
	}
	mission, err := scanMission(tx.QueryRowContext(ctx, `SELECT id,goal,profile,workspace_id,scope_json,created_at,updated_at FROM missions WHERE id=?`, run.MissionID))
	if err != nil {
		return empty, false, err
	}
	if _, _, changed, err := syncRootAgentTx(ctx, tx, run, mission, rootAgentProjection{Status: domain.AgentReady, TurnsUsed: int64(checkpoint.NextTurn - 1), TokensUsed: checkpoint.TotalTokens}, checkpoint.UpdatedAt); err != nil {
		return empty, false, err
	} else if changed {
		if _, err := createAgentGraphSnapshotTx(ctx, tx, run); err != nil {
			return empty, false, err
		}
	}
	return value, true, tx.Commit()
}

// An inactive Supervisor lease does not prove asynchronous tool effects have
// ended. Preserve uncertainty until the existing execution journals settle.
func requireThreadToolEffectsSettledTx(ctx context.Context, tx *sql.Tx, runID string) error {
	for _, query := range []string{
		`SELECT COUNT(*) FROM run_supervisor_tool_calls WHERE run_id=? AND status='pending'`,
		`SELECT COUNT(*) FROM command_runtime_jobs WHERE run_id=? AND (state NOT IN ('completed','failed','timed_out','cancelled','killed','interrupted') OR tree_reaped<>1 OR started_at IS NULL OR completed_at IS NULL OR exit_code IS NULL)`,
		`SELECT COUNT(*) FROM workspace_checkpoint_transactions WHERE run_id=? AND status IN ('prepared','applying')`,
		`SELECT COUNT(*) FROM terminal_sessions WHERE run_id=? AND state IN ('starting','running')`,
	} {
		var unresolved int
		if err := tx.QueryRowContext(ctx, query, runID).Scan(&unresolved); err != nil {
			return err
		}
		if unresolved != 0 {
			return apperror.New(apperror.CodeFailedPrecondition, "The earlier turn still has unresolved tool work; its recorded results must settle before a new turn")
		}
	}
	return nil
}
