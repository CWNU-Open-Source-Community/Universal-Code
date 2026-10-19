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
	"cyberagent-workbench/internal/idgen"
	"cyberagent-workbench/internal/runmutation"
	"cyberagent-workbench/internal/runner"
	"cyberagent-workbench/internal/session"
)

const approvalContinuationActor = "approval_continuation"

type approvalContinuationContext struct {
	ThreadID         string `json:"thread_id"`
	OriginTurn       int    `json:"origin_turn"`
	OriginAttemptID  string `json:"origin_attempt_id"`
	UserMessageID    int64  `json:"user_message_id"`
	Evidence         string `json:"evidence"`
	ScopeFingerprint string `json:"scope_fingerprint"`
	BatchStartTurn   int    `json:"batch_start_turn"`
}

// PrepareApprovalContinuation admits only a completed model wait whose exact
// proposals have all been decided. A manual proposal has no matching call and
// cannot start a model. The existing handoff is the at-most-once execution key.
func (s *SQLiteStore) PrepareApprovalContinuation(ctx context.Context, runID, kind, proposalID string) (domain.ApprovalContinuation, bool, error) {
	var empty domain.ApprovalContinuation
	if !domain.ValidAgentID(runID) || !domain.ValidAgentID(proposalID) || (kind != "file_edit" && kind != "host_command") {
		return empty, false, apperror.New(apperror.CodeInvalidArgument, "Approval continuation identity is invalid")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return empty, false, err
	}
	defer func() { _ = tx.Rollback() }()
	if err := lockRunControlTx(ctx, tx, runID); err != nil {
		return empty, false, err
	}
	tool, field := "workspace_change", "$.metadata.edit_id"
	if kind == "host_command" {
		tool, field = "host_command_propose", "$.metadata.proposal_id"
	}
	var origin approvalContinuationContext
	err = tx.QueryRowContext(ctx, `SELECT turn,attempt_id FROM run_supervisor_tool_calls
		WHERE run_id=? AND tool_name=? AND status='completed' AND json_extract(result_json,?)=?
		ORDER BY turn,round,position LIMIT 1`, runID, tool, field, proposalID).Scan(&origin.OriginTurn, &origin.OriginAttemptID)
	if errors.Is(err, sql.ErrNoRows) {
		return empty, false, nil
	}
	if err != nil {
		return empty, false, err
	}
	origin, foundOrigin, err := resolveApprovalWaitOriginTx(ctx, tx, runID, origin)
	if err != nil || !foundOrigin {
		return empty, false, err
	}
	key := runmutation.RunExecutionHandoffOperationDigest(runID, "approval-continuation-"+origin.OriginAttemptID)
	if handoff, found, err := getRunExecutionHandoffByKey(ctx, tx, key); err != nil {
		return empty, false, err
	} else if found {
		value, err := loadApprovalContinuationTx(ctx, tx, handoff)
		return value, true, err
	}
	run, err := getRunControlRunTx(ctx, tx, runID)
	if err != nil {
		return empty, false, err
	}
	checkpoint, found, err := getSupervisorCheckpointTx(ctx, tx, runID)
	if err != nil {
		return empty, false, err
	}
	if !found || run.Status != domain.RunPaused || checkpoint.Phase != domain.SupervisorWaiting ||
		checkpoint.NextTurn != origin.OriginTurn+1 || checkpoint.AttemptID != "" || !run.UpdatedAt.Equal(checkpoint.UpdatedAt) {
		return empty, false, nil
	}
	if err := requireNoActiveRunControlLeaseTx(ctx, tx, runID, time.Now().UTC()); err != nil {
		return empty, false, err
	}
	if err := requireNoPendingOperatorSteeringTx(ctx, tx, runID); err != nil {
		return empty, false, nil
	}
	if err := requireThreadToolEffectsSettledTx(ctx, tx, runID); err != nil {
		return empty, false, err
	}
	if err := requireApprovalEffectsSettledTx(ctx, tx, runID); err != nil {
		return empty, false, err
	}
	var messageID string
	err = tx.QueryRowContext(ctx, `SELECT thread.id,message.id,message.session_message_id
		FROM operator_steering_messages message
		JOIN threads thread ON thread.active_run_id=message.run_id AND thread.last_run_id=message.run_id
		JOIN run_events event ON event.run_id=message.run_id AND event.subject_id=?
		AND event.type=? AND event.source='run_supervisor'
		WHERE message.run_id=? AND json_extract(event.payload_json,'$.turn')=?
		AND message.status='committed' AND message.session_id=? AND thread.status='active'
		AND json_extract(event.payload_json,'$.requested_lifecycle_action')='wait'
		AND json_extract(event.payload_json,'$.lifecycle_action')='wait'
		AND json_extract(event.payload_json,'$.user_message_id')=message.session_message_id
		AND (EXISTS (SELECT 1 FROM operator_steering_deliveries delivery WHERE delivery.message_id=message.id
		  AND delivery.run_id=message.run_id AND delivery.attempt_id=event.subject_id AND delivery.turn=? AND delivery.status='committed')
		 OR EXISTS (SELECT 1 FROM run_execution_handoff_items item
		  JOIN run_execution_handoff_operations previous ON previous.id=item.operation_id
		  JOIN run_execution_handoff_results result ON result.operation_id=previous.id AND result.status='completed'
		  JOIN run_events prepared ON prepared.run_id=previous.run_id AND prepared.sequence=previous.event_sequence AND prepared.subject_id=previous.id
		  WHERE item.message_id=message.id AND previous.run_id=message.run_id AND previous.session_id=message.session_id
		  AND previous.requested_by='approval_continuation' AND (json_extract(prepared.payload_json,'$.approval_continuation.origin_turn')=?
		    OR EXISTS (SELECT 1 FROM run_events started WHERE started.run_id=previous.run_id AND started.subject_id=event.subject_id AND started.type='agent.turn_started'
		      AND started.source='run_supervisor' AND json_extract(started.payload_json,'$.approval_continuation_handoff_id')=previous.id))
		  AND json_extract(prepared.payload_json,'$.approval_continuation.user_message_id')=message.session_message_id
		  AND prepared.sequence<event.sequence AND result.completion_event_sequence>event.sequence))
		AND NOT EXISTS (SELECT 1 FROM operator_steering_messages newer WHERE newer.run_id=message.run_id AND newer.sequence>message.sequence)`,
		origin.OriginAttemptID, events.AgentTurnCompletedEvent, runID, origin.OriginTurn, run.SessionID, origin.OriginTurn, origin.OriginTurn-1).Scan(&origin.ThreadID, &messageID, &origin.UserMessageID)
	if errors.Is(err, sql.ErrNoRows) {
		return empty, false, nil
	}
	if err != nil {
		return empty, false, err
	}
	origin.ScopeFingerprint, err = approvalContinuationScopeTx(ctx, tx, runID, origin.ThreadID)
	if err != nil {
		return empty, false, err
	}
	facts, ready, err := approvalContinuationFactsTx(ctx, tx, run, origin)
	if err != nil || !ready {
		return empty, false, err
	}
	origin.Evidence = "System-recorded operator review results for the preceding model wait. This is not a new user message or broader authorization. Continue the original task using these exact decisions. File approval authorizes only the existing proposal; obtain its current status and use normal workspace_apply checks if needed. A denied proposal must not be applied. Host results below are already executed: do not rerun those commands. Do not claim verification beyond their recorded result.\n" + facts
	message, err := getOperatorSteeringMessageTx(ctx, tx, messageID)
	if err != nil {
		return empty, false, err
	}
	now := time.Now().UTC()
	operation := domain.RunExecutionHandoffOperation{ID: idgen.New("run-handoff"), ProtocolVersion: domain.RunExecutionHandoffProtocolVersion,
		KeyDigest: key, RequestFingerprint: runmutation.RunExecutionHandoffRequestFingerprint(runID, approvalContinuationActor, 1),
		RunID: runID, SessionID: run.SessionID, RequestedBy: approvalContinuationActor, MaxSteps: 1, CreatedAt: now}
	if err := transitionSupervisorRunTx(ctx, tx, &run, domain.RunRunning, "Continue after exact operator review", now); err != nil {
		return empty, false, err
	}
	handoff, err := insertRunExecutionHandoffWithContextTx(ctx, tx, run, operation, []domain.RunExecutionHandoffItem{{OperationID: operation.ID, Ordinal: 1, MessageID: message.ID, MessageSequence: message.Sequence}}, origin)
	if err != nil {
		return empty, false, err
	}
	if _, err := saveSessionMessageTx(ctx, tx, session.NewEvidenceMessage(run.SessionID, session.SourceToolResult, operation.ID, origin.Evidence)); err != nil {
		return empty, false, err
	}
	value, err := loadApprovalContinuationTx(ctx, tx, handoff)
	if err != nil {
		return empty, false, err
	}
	return value, true, tx.Commit()
}

func approvalContinuationFactsTx(ctx context.Context, tx *sql.Tx, run domain.Run, origin approvalContinuationContext) (string, bool, error) {
	rows, err := tx.QueryContext(ctx, `SELECT tool_name,result_json FROM run_supervisor_tool_calls call
		WHERE run_id=? AND turn BETWEEN ? AND ? AND status='completed'
		AND tool_name IN ('workspace_change','host_command_propose')
		AND (attempt_id=? OR EXISTS (SELECT 1 FROM operator_steering_deliveries delivery JOIN operator_steering_messages message ON message.id=delivery.message_id
		 WHERE delivery.run_id=call.run_id AND delivery.attempt_id=call.attempt_id AND delivery.turn=call.turn AND delivery.status IN ('committed','superseded') AND message.session_message_id=?)
		 OR EXISTS (SELECT 1 FROM run_events started JOIN run_events final ON final.run_id=started.run_id AND final.subject_id=? AND final.type='agent.turn_started' AND final.source='run_supervisor'
		 WHERE started.run_id=call.run_id AND started.subject_id=call.attempt_id AND started.type='agent.turn_started' AND started.source='run_supervisor'
		 AND COALESCE(json_extract(started.payload_json,'$.approval_continuation_handoff_id'),'')!=''
		 AND json_extract(started.payload_json,'$.approval_continuation_handoff_id')=json_extract(final.payload_json,'$.approval_continuation_handoff_id')))
		 ORDER BY turn,round,position LIMIT 33`, run.ID, origin.BatchStartTurn, origin.OriginTurn, origin.OriginAttemptID, origin.UserMessageID, origin.OriginAttemptID)
	if err != nil {
		return "", false, err
	}
	type item struct{ tool, body string }
	var items []item
	for rows.Next() {
		var v item
		if err := rows.Scan(&v.tool, &v.body); err != nil {
			_ = rows.Close()
			return "", false, err
		}
		items = append(items, v)
	}
	err = rows.Err()
	_ = rows.Close()
	if err != nil {
		return "", false, err
	}
	if len(items) == 0 || len(items) > 32 {
		return "", false, nil
	}
	var facts strings.Builder
	for _, item := range items {
		var result struct {
			Metadata map[string]string `json:"metadata"`
		}
		if err := json.Unmarshal([]byte(item.body), &result); err != nil {
			return "", false, err
		}
		if item.tool == "workspace_change" {
			id := result.Metadata["edit_id"]
			if !domain.ValidAgentID(id) {
				return "", false, nil
			}
			var status, approvalStatus, path, original, proposed, workspaceID string
			err := tx.QueryRowContext(ctx, `SELECT edit.status,approval.status,edit.path,edit.original_hash,edit.proposed_hash,edit.workspace_id
				FROM file_edits edit JOIN tool_approvals approval ON approval.proposal_id=edit.id
				WHERE edit.id=? AND edit.session_id=? AND approval.run_id=? AND approval.session_id=edit.session_id
				AND approval.workspace_id=edit.workspace_id AND approval.action_class='workspace_write'`, id, run.SessionID, run.ID).
				Scan(&status, &approvalStatus, &path, &original, &proposed, &workspaceID)
			if err != nil {
				return "", false, err
			}
			if ((status != "approved" && status != "applied" && status != "failed") || approvalStatus != "approved") && (status != "denied" || approvalStatus != "denied") {
				return "", false, nil
			}
			if err := requireFileEditWorkspaceTx(ctx, tx, run.SessionID, workspaceID, false); err != nil {
				return "", false, err
			}
			fmt.Fprintf(&facts, "File proposal %s: %s; path %q; original SHA256 %s; proposed SHA256 %s. No file write is implied by approval; applied/failed records must not be blindly repeated.\n", id, status, path, original, proposed)
		} else {
			id := result.Metadata["proposal_id"]
			if !domain.ValidAgentID(id) {
				return "", false, nil
			}
			proposal, err := getHostCommandProposal(ctx, tx, id)
			if err != nil {
				return "", false, err
			}
			if proposal.RunID != run.ID || proposal.SessionID != run.SessionID {
				return "", false, apperror.New(apperror.CodeConflict, "Host continuation crossed its Run")
			}
			review, found, err := getHostCommandReviewByProposal(ctx, tx, id)
			if err != nil || !found {
				return "", false, err
			}
			if review.Decision == runner.HostCommandReviewDeny {
				fmt.Fprintf(&facts, "Host proposal %s: denied, not executed.\n", id)
				continue
			}
			value, receipt, found, err := getHostCommandProposalResult(ctx, tx, id)
			if err != nil || !found {
				return "", false, err
			}
			if value.RunID != run.ID || value.SessionID != run.SessionID || value.ProposalID != id || value.ReviewID != review.ID {
				return "", false, apperror.New(apperror.CodeConflict, "Host continuation result binding is invalid")
			}
			fmt.Fprintf(&facts, "Host proposal %s: result %s; receipt %s; exit %d; timed_out %t; cancelled %t; stdout_truncated %t; stderr_truncated %t. Its saved output is already in this Session.\n", id, value.ID, receipt.RequestID, receipt.ExitCode, receipt.TimedOut, receipt.Cancelled, receipt.StdoutTruncated, receipt.StderrTruncated)
		}
	}
	return facts.String(), true, nil
}

func loadApprovalContinuationTx(ctx context.Context, q hostCommandProposalQueryer, handoff domain.RunExecutionHandoff) (domain.ApprovalContinuation, error) {
	value := domain.ApprovalContinuation{Handoff: handoff}
	var payload string
	if err := q.QueryRowContext(ctx, `SELECT payload_json FROM run_events WHERE run_id=? AND sequence=? AND subject_id=? AND type=?`, handoff.Operation.RunID, handoff.Operation.EventSequence, handoff.Operation.ID, events.RunExecutionHandoffRequestedEvent).Scan(&payload); err != nil {
		return value, err
	}
	var event struct {
		Context approvalContinuationContext `json:"approval_continuation"`
	}
	if err := json.Unmarshal([]byte(payload), &event); err != nil {
		return value, err
	}
	origin := event.Context
	if handoff.Operation.RequestedBy != approvalContinuationActor || len(handoff.Items) != 1 || origin.OriginTurn <= 0 || !domain.ValidAgentID(origin.ThreadID) || !domain.ValidAgentID(origin.OriginAttemptID) || origin.UserMessageID <= 0 || origin.Evidence == "" {
		return value, apperror.New(apperror.CodeConflict, "Approval continuation lost its sealed origin")
	}
	var original session.Message
	var err error
	original, err = scanSessionMessage(q.QueryRowContext(ctx, `SELECT id,session_id,role,content,provenance_version,source_kind,source_ref,content_sha256,instruction_authorized,token_estimate,compacted,created_at FROM session_messages WHERE id=?`, origin.UserMessageID))
	if err != nil {
		return value, err
	}
	var exact int
	if err := q.QueryRowContext(ctx, `SELECT COUNT(*) FROM operator_steering_messages WHERE id=? AND run_id=? AND session_id=? AND session_message_id=? AND sequence=? AND status='committed'`,
		handoff.Items[0].MessageID, handoff.Operation.RunID, handoff.Operation.SessionID, original.ID, handoff.Items[0].MessageSequence).Scan(&exact); err != nil {
		return value, err
	}
	if exact != 1 || session.ValidateStoredMessage(original) != nil || original.SessionID != handoff.Operation.SessionID || !validQueuedInputProvenanceTx(ctx, q, handoff.Items[0].MessageID, original) {
		return value, apperror.New(apperror.CodeConflict, "Approval continuation original input is invalid")
	}
	value.ThreadID, value.OriginTurn, value.OriginAttemptID, value.UserMessageID, value.Input = origin.ThreadID, origin.OriginTurn, origin.OriginAttemptID, origin.UserMessageID, original.Content
	if err := q.QueryRowContext(ctx, `SELECT image_count,attachment_count FROM operator_steering_messages WHERE id=? AND run_id=?`, handoff.Items[0].MessageID, handoff.Operation.RunID).Scan(&value.ImageCount, &value.AttachmentCount); err != nil {
		return value, err
	}
	return value, nil
}

func approvalContinuationForCheckpointTx(ctx context.Context, tx *sql.Tx, checkpoint domain.SupervisorCheckpoint) (domain.ApprovalContinuation, bool, error) {
	var key string
	err := tx.QueryRowContext(ctx, `SELECT operation.operation_key_digest FROM run_execution_handoff_operations operation
		JOIN run_events event ON event.run_id=operation.run_id AND event.sequence=operation.event_sequence AND event.subject_id=operation.id
		WHERE operation.run_id=? AND operation.requested_by=? AND (json_extract(event.payload_json,'$.approval_continuation.origin_turn')=?
		 OR EXISTS (SELECT 1 FROM run_events started WHERE started.run_id=operation.run_id AND started.subject_id=? AND started.type='agent.turn_started' AND started.source='run_supervisor'
		 AND json_extract(started.payload_json,'$.turn')=? AND json_extract(started.payload_json,'$.approval_continuation_handoff_id')=operation.id))
		AND NOT EXISTS (SELECT 1 FROM run_execution_handoff_results result WHERE result.operation_id=operation.id)
		ORDER BY operation.event_sequence DESC LIMIT 1`, checkpoint.RunID, approvalContinuationActor, checkpoint.NextTurn-1, checkpoint.AttemptID, checkpoint.NextTurn).Scan(&key)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.ApprovalContinuation{}, false, nil
	}
	if err != nil {
		return domain.ApprovalContinuation{}, false, err
	}
	handoff, found, err := getRunExecutionHandoffByKey(ctx, tx, key)
	if err != nil || !found {
		return domain.ApprovalContinuation{}, false, err
	}
	value, err := loadApprovalContinuationTx(ctx, tx, handoff)
	if err == nil && value.Input != checkpoint.PendingInput {
		err = apperror.New(apperror.CodeConflict, "Approval continuation input changed")
	}
	return value, err == nil, err
}

func (s *SQLiteStore) BeginSupervisorApprovalContinuation(ctx context.Context, lease domain.RunExecutionLease, handoffID string) (domain.SupervisorTurn, error) {
	return s.beginSupervisorTurn(ctx, lease, "", false, "", handoffID)
}

func requireApprovalContinuationStartTx(ctx context.Context, tx *sql.Tx, run domain.Run, checkpoint domain.SupervisorCheckpoint, handoffID string) (domain.ApprovalContinuation, error) {
	handoff, found, err := getRunExecutionHandoffByID(ctx, tx, handoffID)
	if err != nil {
		return domain.ApprovalContinuation{}, err
	}
	if !found || handoff.Result != nil || handoff.Operation.RunID != run.ID {
		return domain.ApprovalContinuation{}, apperror.New(apperror.CodeConflict, "Approval continuation is already settled")
	}
	value, err := loadApprovalContinuationTx(ctx, tx, handoff)
	if err != nil {
		return value, err
	}
	var active int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM threads WHERE id=? AND status='active' AND active_run_id=? AND last_run_id=?`, value.ThreadID, run.ID, run.ID).Scan(&active); err != nil {
		return value, err
	}
	ownedTurn, err := approvalContinuationOwnsTurnTx(ctx, tx, value, checkpoint)
	if err != nil {
		return value, err
	}
	if active != 1 || !ownedTurn || !run.UpdatedAt.Equal(handoff.Operation.CreatedAt) ||
		(checkpoint.Phase != domain.SupervisorWaiting && checkpoint.Phase != domain.SupervisorTurnStarted) {
		return value, apperror.New(apperror.CodeFailedPrecondition, "Approval continuation was superseded by a stop, input, or execution change")
	}
	if checkpoint.Phase == domain.SupervisorTurnStarted && checkpoint.PendingInput != value.Input {
		return value, apperror.New(apperror.CodeConflict, "Approval continuation has a different active input")
	}
	if err := requireNoPendingOperatorSteeringTx(ctx, tx, run.ID); err != nil {
		return value, err
	}
	if err := requireThreadToolEffectsSettledTx(ctx, tx, run.ID); err != nil {
		return value, err
	}
	if err := requireApprovalEffectsSettledTx(ctx, tx, run.ID); err != nil {
		return value, err
	}
	var saved string
	if err := tx.QueryRowContext(ctx, `SELECT json_extract(payload_json,'$.approval_continuation.scope_fingerprint') FROM run_events WHERE run_id=? AND sequence=? AND subject_id=?`, run.ID, handoff.Operation.EventSequence, handoffID).Scan(&saved); err != nil {
		return value, err
	}
	current, err := approvalContinuationScopeTx(ctx, tx, run.ID, value.ThreadID)
	if err != nil {
		return value, err
	}
	if saved == "" || saved != current {
		return value, apperror.New(apperror.CodeFailedPrecondition, "Approval continuation execution settings changed")
	}
	return value, nil
}

func approvalContinuationOwnsTurnTx(ctx context.Context, tx *sql.Tx, value domain.ApprovalContinuation, cp domain.SupervisorCheckpoint) (bool, error) {
	if cp.NextTurn == value.OriginTurn+1 {
		return true, nil
	}
	var count int
	err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM run_events WHERE run_id=? AND subject_id=? AND type='agent.turn_started' AND source='run_supervisor'
	 AND json_extract(payload_json,'$.turn')=? AND json_extract(payload_json,'$.approval_continuation_handoff_id')=? AND json_extract(payload_json,'$.operator_message_id')=?`,
		cp.RunID, cp.AttemptID, cp.NextTurn, value.Handoff.Operation.ID, value.Handoff.Items[0].MessageID).Scan(&count)
	return count == 1, err
}

func approvalContinuationScopeTx(ctx context.Context, tx *sql.Tx, runID, threadID string) (string, error) {
	var values []string
	for _, table := range []string{"run_mode_snapshots", "run_execution_permission_snapshots", "run_execution_profile_snapshots", "run_execution_interaction_snapshots"} {
		var id string
		if err := tx.QueryRowContext(ctx, `SELECT id FROM `+table+` WHERE run_id=? ORDER BY revision DESC LIMIT 1`, runID).Scan(&id); err != nil {
			return "", err
		}
		values = append(values, id)
	}
	var permission, preference string
	if err := tx.QueryRowContext(ctx, `SELECT id FROM thread_execution_permission_snapshots WHERE thread_id=? ORDER BY revision DESC LIMIT 1`, threadID).Scan(&permission); err != nil {
		return "", err
	}
	if err := tx.QueryRowContext(ctx, `SELECT COALESCE((SELECT value FROM provider_setting WHERE key=?),'')`, threadModelRoutePreferenceKeyPrefix+threadID).Scan(&preference); err != nil {
		return "", err
	}
	values = append(values, permission, preference)
	encoded, err := json.Marshal(values)
	if err != nil {
		return "", err
	}
	return session.ContentSHA256(string(encoded)), nil
}

func requireApprovalEffectsSettledTx(ctx context.Context, tx *sql.Tx, runID string) error {
	for _, query := range []string{
		`SELECT COUNT(*) FROM host_command_proposal_execution_intents intent WHERE run_id=? AND NOT EXISTS (SELECT 1 FROM host_command_proposal_results result WHERE result.request_id=intent.request_id)`,
		`SELECT COUNT(*) FROM file_edit_apply_operations operation WHERE run_id=? AND NOT EXISTS (SELECT 1 FROM file_edit_apply_results result WHERE result.operation_key_digest=operation.operation_key_digest)`,
	} {
		var count int
		if err := tx.QueryRowContext(ctx, query, runID).Scan(&count); err != nil {
			return err
		}
		if count != 0 {
			return apperror.New(apperror.CodeFailedPrecondition, "An earlier approved operation has an unconfirmed result; automatic continuation is blocked")
		}
	}
	return nil
}

// A failed automatic continuation has no new operator delivery to consume. Its
// handoff remains failed; only a settled attempt is closed for a later message.
func (s *SQLiteStore) CloseFailedApprovalContinuation(ctx context.Context, handoffID string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	handoff, found, err := getRunExecutionHandoffByID(ctx, tx, handoffID)
	if err != nil {
		return err
	}
	if !found || handoff.Result == nil || handoff.Result.Status != domain.RunExecutionHandoffFailed {
		return nil
	}
	if err := lockRunControlTx(ctx, tx, handoff.Operation.RunID); err != nil {
		return err
	}
	value, err := loadApprovalContinuationTx(ctx, tx, handoff)
	if err != nil {
		return err
	}
	cp, found, err := getSupervisorCheckpointTx(ctx, tx, handoff.Operation.RunID)
	if err != nil || !found {
		return err
	}
	ownedTurn, err := approvalContinuationOwnsTurnTx(ctx, tx, value, cp)
	if err != nil {
		return err
	}
	if !ownedTurn || cp.AttemptID == "" {
		return nil
	}
	if cp.PendingInput != value.Input {
		return apperror.New(apperror.CodeConflict, "Failed approval continuation changed input")
	}
	if cp.LeaseID != handoff.Result.LeaseID || cp.LeaseGeneration != handoff.Result.LeaseGeneration {
		return apperror.New(apperror.CodeConflict, "Failed approval continuation execution lease changed")
	}
	if err := requireNoActiveRunControlLeaseTx(ctx, tx, cp.RunID, time.Now().UTC()); err != nil {
		return err
	}
	if err := requireThreadToolEffectsSettledTx(ctx, tx, cp.RunID); err != nil {
		return err
	}
	if err := requireApprovalEffectsSettledTx(ctx, tx, cp.RunID); err != nil {
		return err
	}
	run, err := getRunControlRunTx(ctx, tx, cp.RunID)
	if err != nil {
		return err
	}
	if run.Terminal() {
		return nil
	}
	corrections, err := commitMidTurnSteeringTx(ctx, tx, run, cp, true)
	if err != nil {
		return err
	}
	text := fmt.Sprintf("System-recorded approval continuation failure. Handoff %s, attempt %s, turn %d: %s. The original review remains saved. Completed tool records remain evidence; no unknown operation has been retried. Send a new message to continue.", handoffID, cp.AttemptID, cp.NextTurn, handoff.Result.ErrorCode)
	if corrections > 0 {
		text += fmt.Sprintf(" Accepted current-turn corrections: %d. This failed continuation did not complete their requested work.", corrections)
	}
	if _, err := saveSessionMessageTx(ctx, tx, session.NewEvidenceMessage(run.SessionID, session.SourceToolResult, handoffID, text)); err != nil {
		return err
	}
	cp.NextTurn++
	cp.Phase = domain.SupervisorIdle
	cp.AttemptID = ""
	cp.PendingInput = ""
	cp.PendingImageCount = 0
	cp.PendingAttachmentCount = 0
	cp.LastError = ""
	cp.RepairReason = ""
	cp.RepairPhase = domain.ProtocolRepairNone
	cp.UpdatedAt = time.Now().UTC()
	if err := upsertSupervisorCheckpointTx(ctx, tx, cp); err != nil {
		return err
	}
	mission, err := scanMission(tx.QueryRowContext(ctx, `SELECT id,goal,profile,workspace_id,scope_json,created_at,updated_at FROM missions WHERE id=?`, run.MissionID))
	if err != nil {
		return err
	}
	if _, _, changed, err := syncRootAgentTx(ctx, tx, run, mission, rootAgentProjection{Status: domain.AgentReady, TurnsUsed: int64(cp.NextTurn - 1), TokensUsed: cp.TotalTokens}, cp.UpdatedAt); err != nil {
		return err
	} else if changed {
		if _, err := createAgentGraphSnapshotTx(ctx, tx, run); err != nil {
			return err
		}
	}
	return tx.Commit()
}
