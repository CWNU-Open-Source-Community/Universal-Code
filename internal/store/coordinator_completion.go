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
	"cyberagent-workbench/internal/redact"
	"cyberagent-workbench/internal/runmutation"
	"cyberagent-workbench/internal/session"
)

const agentCompletionSelect = `SELECT id, run_id, agent_id, parent_agent_id, attempt_id,
	protocol_version, outcome, summary, work_item_ids_json, note_ids_json, message_id, created_at
	FROM agent_completion_reports`

func (s *SQLiteStore) FinishSpecialist(ctx context.Context, completion domain.AgentCompletion,
	operationKey string,
) (domain.AgentCompletion, bool, error) {
	completion.ID = strings.TrimSpace(completion.ID)
	completion.RunID = strings.TrimSpace(completion.RunID)
	completion.AgentID = strings.TrimSpace(completion.AgentID)
	completion.ParentAgentID = strings.TrimSpace(completion.ParentAgentID)
	completion.AttemptID = strings.TrimSpace(completion.AttemptID)
	completion.MessageID = strings.TrimSpace(completion.MessageID)
	report, err := domain.NormalizeCompletionReport(completion.Report)
	if err != nil {
		return domain.AgentCompletion{}, false,
			apperror.Wrap(apperror.CodeInvalidArgument, "completion report is invalid", err)
	}
	report.Summary = redact.String(report.Summary)
	report, err = domain.NormalizeCompletionReport(report)
	if err != nil {
		return domain.AgentCompletion{}, false,
			apperror.Wrap(apperror.CodeInvalidArgument, "redacted completion report is invalid", err)
	}
	completion.Report = report
	if completion.CreatedAt.IsZero() {
		completion.CreatedAt = time.Now().UTC()
	} else {
		completion.CreatedAt = completion.CreatedAt.UTC()
	}
	if err := completion.Validate(); err != nil {
		return domain.AgentCompletion{}, false,
			apperror.Wrap(apperror.CodeInvalidArgument, "Agent completion is invalid", err)
	}
	normalizedOperationKey, err := domain.NormalizeAgentOperationKey(operationKey)
	if err != nil {
		return domain.AgentCompletion{}, false,
			apperror.Wrap(apperror.CodeInvalidArgument, "Agent completion idempotency key is invalid", err)
	}
	reportJSON, err := marshalRedactedJSON(completion.Report)
	if err != nil {
		return domain.AgentCompletion{}, false, err
	}
	workItemIDsJSON, err := marshalRedactedJSON(completion.Report.WorkItemIDs)
	if err != nil {
		return domain.AgentCompletion{}, false, err
	}
	noteIDsJSON, err := marshalRedactedJSON(completion.Report.NoteIDs)
	if err != nil {
		return domain.AgentCompletion{}, false, err
	}
	keyDigest := runmutation.Fingerprint("agent_completion_operation.v1", completion.RunID,
		normalizedOperationKey)
	requestFingerprint := runmutation.Fingerprint("agent_completion_request.v1", completion.RunID,
		completion.AgentID, completion.ParentAgentID, completion.AttemptID, reportJSON)

	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{})
	if err != nil {
		return domain.AgentCompletion{}, false, err
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx, `UPDATE agent_nodes SET updated_at = updated_at WHERE id = ?`,
		completion.AgentID); err != nil {
		return domain.AgentCompletion{}, false, err
	}
	storedFingerprint, storedReportID, found, err := getAgentCompletionOperationTx(ctx, tx, keyDigest)
	if err != nil {
		return domain.AgentCompletion{}, false, err
	}
	if found {
		if storedFingerprint != requestFingerprint {
			return domain.AgentCompletion{}, false, apperror.New(apperror.CodeConflict,
				"Agent completion idempotency key was already used for different intent")
		}
		existing, err := scanAgentCompletion(tx.QueryRowContext(ctx,
			agentCompletionSelect+` WHERE id = ?`, storedReportID))
		if err != nil {
			return domain.AgentCompletion{}, false, err
		}
		if err := tx.Commit(); err != nil {
			return domain.AgentCompletion{}, false, err
		}
		return existing, true, nil
	}

	run, _, err := getCoordinatorRunTx(ctx, tx, completion.RunID)
	if err != nil {
		return domain.AgentCompletion{}, false, err
	}
	if run.Status != domain.RunRunning {
		return domain.AgentCompletion{}, false, apperror.New(apperror.CodeFailedPrecondition,
			"Agent completion requires a running Run")
	}
	child, err := scanAgentNode(tx.QueryRowContext(ctx, agentNodeSelect+` WHERE id = ?`,
		completion.AgentID))
	if err != nil {
		return domain.AgentCompletion{}, false, err
	}
	if child.RunID != run.ID || child.Role != domain.AgentRoleSpecialist ||
		child.ParentID != completion.ParentAgentID {
		return domain.AgentCompletion{}, false, apperror.New(apperror.CodeInvalidArgument,
			"Agent completion must target a Specialist and its direct parent")
	}
	if child.Status != domain.AgentRunning || child.ActiveAttemptID != completion.AttemptID {
		return domain.AgentCompletion{}, false, apperror.New(apperror.CodeFailedPrecondition,
			"Agent completion does not match the active Specialist attempt")
	}
	attempt, attemptChild, attemptRun, err := loadActiveAgentAttemptTx(ctx, tx,
		domain.AgentAttemptRef{
			RunID: completion.RunID, AgentID: completion.AgentID, AttemptID: completion.AttemptID,
		})
	if err != nil {
		return domain.AgentCompletion{}, false, err
	}
	if attempt.UsageRecordedAt == nil {
		return domain.AgentCompletion{}, false, apperror.New(apperror.CodeFailedPrecondition,
			"Agent completion requires recorded model usage")
	}
	if err := requireSpecialistProtocolRepairResolvedTx(ctx, tx, attempt.ID); err != nil {
		return domain.AgentCompletion{}, false, err
	}
	if completion.CreatedAt.Before(*attempt.UsageRecordedAt) {
		return domain.AgentCompletion{}, false, apperror.New(apperror.CodeInvalidArgument,
			"Agent completion cannot predate recorded model usage")
	}
	child = attemptChild
	run = attemptRun
	parent, err := scanAgentNode(tx.QueryRowContext(ctx, agentLifecycleSelect+` WHERE id = ?`,
		completion.ParentAgentID))
	if err != nil {
		return domain.AgentCompletion{}, false, err
	}
	if parent.RunID != run.ID || parent.Role != domain.AgentRoleRoot || parent.Terminal() {
		return domain.AgentCompletion{}, false, apperror.New(apperror.CodeFailedPrecondition,
			"Agent completion parent must be the active Run root")
	}
	if err := validateCompletionReferencesTx(ctx, tx, child, completion.Report); err != nil {
		return domain.AgentCompletion{}, false, err
	}
	if _, err := commitSpecialistContextTx(ctx, tx, run, child, parent, attempt,
		completion.CreatedAt); err != nil {
		return domain.AgentCompletion{}, false, err
	}

	payloadJSON, err := marshalRedactedJSON(domain.AgentCompletionInboxPayload{
		CompletionReportID: completion.ID, AgentID: child.ID, Report: completion.Report,
	})
	if err != nil {
		return domain.AgentCompletion{}, false, err
	}
	if len([]byte(payloadJSON)) > domain.MaxAgentMessagePayloadBytes {
		return domain.AgentCompletion{}, false, apperror.New(apperror.CodeResourceExhausted,
			fmt.Sprintf("completion inbox payload exceeds %d bytes", domain.MaxAgentMessagePayloadBytes))
	}
	message := domain.AgentMessage{
		ID: completion.MessageID, RunID: run.ID, SenderAgentID: child.ID,
		RecipientAgentID: parent.ID, Kind: domain.AgentMessageResult,
		Semantic: domain.AgentMessageSemanticMessage, PayloadJSON: payloadJSON,
		Status: domain.AgentMessagePending, CreatedAt: completion.CreatedAt,
	}
	if err := insertBoundedAgentMessageTx(ctx, tx, &message); err != nil {
		return domain.AgentCompletion{}, false, err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO agent_completion_reports
		(id, run_id, agent_id, parent_agent_id, attempt_id, protocol_version, outcome, summary,
		work_item_ids_json, note_ids_json, message_id, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, completion.ID, completion.RunID,
		completion.AgentID, completion.ParentAgentID, completion.AttemptID, completion.Report.Version,
		completion.Report.Outcome, completion.Report.Summary, workItemIDsJSON, noteIDsJSON,
		completion.MessageID, ts(completion.CreatedAt)); err != nil {
		return domain.AgentCompletion{}, false, err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO agent_completion_operations
		(operation_key_digest, request_fingerprint, report_id, created_at) VALUES (?, ?, ?, ?)`,
		keyDigest, requestFingerprint, completion.ID, ts(completion.CreatedAt)); err != nil {
		return domain.AgentCompletion{}, false, err
	}

	updatedAttempt := attempt
	updatedAttempt.Status = domain.AgentAttemptFinished
	updatedAttempt.NotificationMessageID = message.ID
	updatedAttempt.UpdatedAt = completion.CreatedAt
	attemptFinished := completion.CreatedAt
	updatedAttempt.FinishedAt = &attemptFinished
	if err := updatedAttempt.Validate(); err != nil {
		return domain.AgentCompletion{}, false, err
	}
	result, err := tx.ExecContext(ctx, `UPDATE agent_attempts SET status = ?,
		notification_message_id = ?, updated_at = ?, finished_at = ?
		WHERE id = ? AND status = ? AND usage_recorded_at IS NOT NULL`,
		updatedAttempt.Status, updatedAttempt.NotificationMessageID, ts(updatedAttempt.UpdatedAt),
		ts(updatedAttempt.FinishedAt.UTC()), attempt.ID, domain.AgentAttemptRunning)
	if err != nil {
		return domain.AgentCompletion{}, false, err
	}
	if err := requireSingleAgentAttemptUpdate(result,
		"Specialist attempt changed during completion"); err != nil {
		return domain.AgentCompletion{}, false, err
	}

	updatedChild := child
	updatedChild.Status = domain.AgentCompleted
	updatedChild.ActiveAttemptID = ""
	updatedChild.StatusReason = "completion reported"
	if completion.Report.Outcome == domain.CompletionPartial {
		updatedChild.StatusReason = "partial completion reported"
	}
	updatedChild.Version++
	updatedChild.UpdatedAt = completion.CreatedAt
	finished := completion.CreatedAt
	updatedChild.FinishedAt = &finished
	if err := updatedChild.Validate(); err != nil {
		return domain.AgentCompletion{}, false, err
	}
	result, err = tx.ExecContext(ctx, `UPDATE agent_nodes SET status = ?, active_attempt_id = '',
		status_reason = ?, version = ?, updated_at = ?, finished_at = ?
		WHERE id = ? AND version = ? AND status = ? AND active_attempt_id = ?`,
		updatedChild.Status, updatedChild.StatusReason, updatedChild.Version, ts(updatedChild.UpdatedAt),
		ts(updatedChild.FinishedAt.UTC()), updatedChild.ID, child.Version, domain.AgentRunning,
		completion.AttemptID)
	if err != nil {
		return domain.AgentCompletion{}, false, err
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return domain.AgentCompletion{}, false, err
	}
	if rows != 1 {
		return domain.AgentCompletion{}, false,
			apperror.New(apperror.CodeConflict, "Specialist changed during completion")
	}
	result, err = tx.ExecContext(ctx, `UPDATE sessions SET status = ?, updated_at = ?
		WHERE id = ? AND status = ?`, session.StatusArchived, ts(completion.CreatedAt),
		child.SessionID, session.StatusActive)
	if err != nil {
		return domain.AgentCompletion{}, false, err
	}
	rows, err = result.RowsAffected()
	if err != nil {
		return domain.AgentCompletion{}, false, err
	}
	if rows != 1 {
		return domain.AgentCompletion{}, false,
			apperror.New(apperror.CodeConflict, "Specialist Session was not active during completion")
	}

	if err := appendSupervisorEventTx(ctx, tx, run, events.AgentTurnCompletedEvent,
		"agent_coordinator", attempt.ID, agentAttemptEventPayload(updatedAttempt, false)); err != nil {
		return domain.AgentCompletion{}, false, err
	}
	if err := appendSupervisorEventTx(ctx, tx, run, events.AgentCompletionReportedEvent,
		"agent_coordinator", completion.ID, map[string]any{
			"agent_id": child.ID, "parent_agent_id": parent.ID,
			"attempt_id": completion.AttemptID, "protocol_version": completion.Report.Version,
			"outcome": completion.Report.Outcome, "summary_bytes": len([]byte(completion.Report.Summary)),
			"work_item_count": len(completion.Report.WorkItemIDs),
			"note_count":      len(completion.Report.NoteIDs), "message_id": message.ID,
		}); err != nil {
		return domain.AgentCompletion{}, false, err
	}
	if err := appendSupervisorEventTx(ctx, tx, run, events.AgentMessageSentEvent,
		"agent_coordinator", message.ID, map[string]any{
			"sender_agent_id": child.ID, "recipient_agent_id": parent.ID,
			"sequence": message.Sequence, "kind": message.Kind, "semantic": message.Semantic,
			"payload_bytes": len([]byte(message.PayloadJSON)), "completion_report_id": completion.ID,
		}); err != nil {
		return domain.AgentCompletion{}, false, err
	}
	if err := appendSupervisorEventTx(ctx, tx, run, events.AgentStatusChangedEvent,
		"agent_coordinator", child.ID, map[string]any{
			"from": child.Status, "to": updatedChild.Status, "reason": updatedChild.StatusReason,
			"parent_agent_id": parent.ID, "completion_report_id": completion.ID,
			"version": updatedChild.Version,
		}); err != nil {
		return domain.AgentCompletion{}, false, err
	}
	if _, err := createAgentGraphSnapshotTx(ctx, tx, run); err != nil {
		return domain.AgentCompletion{}, false, err
	}
	if err := tx.Commit(); err != nil {
		return domain.AgentCompletion{}, false, err
	}
	return completion, false, nil
}

func (s *SQLiteStore) GetAgentCompletion(ctx context.Context,
	agentID string,
) (domain.AgentCompletion, bool, error) {
	completion, err := scanAgentCompletion(s.db.QueryRowContext(ctx,
		agentCompletionSelect+` WHERE agent_id = ?`, strings.TrimSpace(agentID)))
	if errors.Is(err, sql.ErrNoRows) {
		return domain.AgentCompletion{}, false, nil
	}
	if err != nil {
		return domain.AgentCompletion{}, false, err
	}
	return completion, true, nil
}

func getAgentCompletionOperationTx(ctx context.Context, tx *sql.Tx,
	keyDigest string,
) (string, string, bool, error) {
	var fingerprint, reportID string
	err := tx.QueryRowContext(ctx, `SELECT request_fingerprint, report_id
		FROM agent_completion_operations WHERE operation_key_digest = ?`, keyDigest).
		Scan(&fingerprint, &reportID)
	if errors.Is(err, sql.ErrNoRows) {
		return "", "", false, nil
	}
	if err != nil {
		return "", "", false, err
	}
	return fingerprint, reportID, true, nil
}

func scanAgentCompletion(row scanner) (domain.AgentCompletion, error) {
	var completion domain.AgentCompletion
	var protocolVersion, outcome, summary, workItemIDsJSON, noteIDsJSON, createdAt string
	if err := row.Scan(&completion.ID, &completion.RunID, &completion.AgentID,
		&completion.ParentAgentID, &completion.AttemptID, &protocolVersion, &outcome, &summary,
		&workItemIDsJSON, &noteIDsJSON, &completion.MessageID, &createdAt); err != nil {
		return domain.AgentCompletion{}, err
	}
	completion.Report = domain.CompletionReport{
		Version: protocolVersion, Outcome: domain.CompletionOutcome(outcome), Summary: summary,
	}
	if err := json.Unmarshal([]byte(workItemIDsJSON), &completion.Report.WorkItemIDs); err != nil {
		return domain.AgentCompletion{}, err
	}
	if err := json.Unmarshal([]byte(noteIDsJSON), &completion.Report.NoteIDs); err != nil {
		return domain.AgentCompletion{}, err
	}
	completion.CreatedAt = parseTS(createdAt)
	return completion, completion.Validate()
}

func validateCompletionReferencesTx(ctx context.Context, tx *sql.Tx, child domain.AgentNode,
	report domain.CompletionReport,
) error {
	providedWorkItems := make(map[string]struct{}, len(report.WorkItemIDs))
	for _, workItemID := range report.WorkItemIDs {
		var status string
		err := tx.QueryRowContext(ctx, `SELECT status FROM work_items
			WHERE run_id = ? AND id = ? AND owner_agent_id = ?`, child.RunID, workItemID, child.ID).
			Scan(&status)
		if errors.Is(err, sql.ErrNoRows) {
			return apperror.New(apperror.CodeInvalidArgument,
				"completion WorkItem reference is not owned by the Specialist")
		}
		if err != nil {
			return err
		}
		providedWorkItems[workItemID] = struct{}{}
	}
	rows, err := tx.QueryContext(ctx, `SELECT id FROM work_items
		WHERE run_id = ? AND owner_agent_id = ? AND status NOT IN (?, ?)
		ORDER BY id`, child.RunID, child.ID, domain.WorkItemCompleted, domain.WorkItemCancelled)
	if err != nil {
		return err
	}
	activeWorkItems := make([]string, 0)
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			_ = rows.Close()
			return err
		}
		activeWorkItems = append(activeWorkItems, id)
	}
	if err := rows.Close(); err != nil {
		return err
	}
	if err := rows.Err(); err != nil {
		return err
	}
	if report.Outcome == domain.CompletionSucceeded && len(activeWorkItems) > 0 {
		return apperror.New(apperror.CodeFailedPrecondition,
			"successful completion cannot leave active Specialist WorkItems")
	}
	if report.Outcome == domain.CompletionPartial {
		for _, workItemID := range activeWorkItems {
			if _, included := providedWorkItems[workItemID]; !included {
				return apperror.New(apperror.CodeFailedPrecondition,
					"partial completion must reference every active Specialist WorkItem")
			}
		}
	}
	for _, noteID := range report.NoteIDs {
		var status, visibility string
		err := tx.QueryRowContext(ctx, `SELECT status, visibility FROM notes
			WHERE run_id = ? AND id = ? AND owner_agent_id = ?`, child.RunID, noteID, child.ID).
			Scan(&status, &visibility)
		if errors.Is(err, sql.ErrNoRows) {
			return apperror.New(apperror.CodeInvalidArgument,
				"completion Note reference is not owned by the Specialist")
		}
		if err != nil {
			return err
		}
		if domain.NoteStatus(status) != domain.NoteActive ||
			(domain.NoteVisibility(visibility) != domain.NoteVisibilityRun &&
				domain.NoteVisibility(visibility) != domain.NoteVisibilityRoot) {
			return apperror.New(apperror.CodeFailedPrecondition,
				"completion Note reference must be active and visible to the parent")
		}
	}
	return nil
}

func validateAgentCompletionProjectionTx(ctx context.Context, tx *sql.Tx,
	nodes []domain.AgentNode,
) error {
	for _, node := range nodes {
		if node.Role != domain.AgentRoleSpecialist {
			continue
		}
		completion, err := scanAgentCompletion(tx.QueryRowContext(ctx,
			agentCompletionSelect+` WHERE agent_id = ?`, node.ID))
		found := err == nil
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		if node.Status == domain.AgentCompleted {
			if !found || completion.RunID != node.RunID || completion.ParentAgentID != node.ParentID {
				return apperror.New(apperror.CodeFailedPrecondition,
					"completed Specialist is missing its completion report")
			}
			attempt, attemptErr := scanAgentAttempt(tx.QueryRowContext(ctx,
				agentAttemptSelect+` WHERE id = ?`, completion.AttemptID))
			if attemptErr != nil && !errors.Is(attemptErr, sql.ErrNoRows) {
				return attemptErr
			}
			if attemptErr == nil && (attempt.RunID != node.RunID || attempt.AgentID != node.ID ||
				attempt.ParentAgentID != node.ParentID || attempt.Status != domain.AgentAttemptFinished ||
				attempt.NotificationMessageID != completion.MessageID) {
				return apperror.New(apperror.CodeFailedPrecondition,
					"completed Specialist does not match its durable Agent attempt")
			}
			continue
		}
		if found {
			return apperror.New(apperror.CodeFailedPrecondition,
				"non-completed Specialist unexpectedly has a completion report")
		}
	}
	return nil
}
