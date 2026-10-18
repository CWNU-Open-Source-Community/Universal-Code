package store

import (
	"bytes"
	"context"
	"database/sql"
	"slices"
	"time"

	"cyberagent-workbench/internal/apperror"
	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/events"
)

const rootInboxDeliverySelect = `SELECT run_id, root_agent_id, supervisor_attempt_id,
	turn_number, message_id, ordinal, status, prepared_at, resolved_at
	FROM root_inbox_deliveries`

const rootInboxMessageSelect = `SELECT message.id, message.run_id, message.sender_agent_id,
	message.recipient_agent_id, message.sequence, message.kind, message.semantic,
	message.payload_json, message.status, message.created_at, message.consumed_at
	FROM agent_messages message`

func (s *SQLiteStore) PrepareRootInboxContext(ctx context.Context,
	checkpoint domain.SupervisorCheckpoint,
) (domain.RootInboxContextBatch, error) {
	if err := checkpoint.Validate(); err != nil {
		return domain.RootInboxContextBatch{}, err
	}
	if checkpoint.Phase != domain.SupervisorTurnStarted {
		return domain.RootInboxContextBatch{}, apperror.New(apperror.CodeFailedPrecondition,
			"only a started Supervisor turn can prepare root inbox context")
	}
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{})
	if err != nil {
		return domain.RootInboxContextBatch{}, err
	}
	defer func() { _ = tx.Rollback() }()
	run, current, err := requireActiveSupervisorAttemptTx(ctx, tx, checkpoint)
	if err != nil {
		return domain.RootInboxContextBatch{}, err
	}
	root, found, err := getRootAgentTx(ctx, tx, run.ID)
	if err != nil {
		return domain.RootInboxContextBatch{}, err
	}
	if !found || root.Role != domain.AgentRoleRoot || root.Status != domain.AgentRunning ||
		root.ActiveAttemptID != current.AttemptID {
		return domain.RootInboxContextBatch{}, apperror.New(apperror.CodeConflict,
			"root Agent is not bound to the preparing Supervisor attempt")
	}
	batch, found, err := loadPreparedRootInboxBatchTx(ctx, tx, current, root)
	if err != nil {
		return domain.RootInboxContextBatch{}, err
	}
	if found {
		batch.Recovered = true
		if err := tx.Commit(); err != nil {
			return domain.RootInboxContextBatch{}, err
		}
		return batch, nil
	}
	now := time.Now().UTC()
	if _, err := supersedeRootInboxDeliveriesTx(ctx, tx, run, root.ID, current.AttemptID,
		now); err != nil {
		return domain.RootInboxContextBatch{}, err
	}
	messages, err := listEligibleRootInboxMessagesTx(ctx, tx, run.ID, root.ID,
		domain.MaxRootInboxContextMessages)
	if err != nil {
		return domain.RootInboxContextBatch{}, err
	}
	batch = domain.RootInboxContextBatch{
		RunID: run.ID, RootAgentID: root.ID, SupervisorAttemptID: current.AttemptID,
		Turn: current.NextTurn, Messages: messages, PreparedAt: now,
	}
	if err := batch.Validate(); err != nil {
		return domain.RootInboxContextBatch{}, err
	}
	if len(messages) == 0 {
		if err := tx.Commit(); err != nil {
			return domain.RootInboxContextBatch{}, err
		}
		return batch, nil
	}
	payloadBytes := 0
	for index, message := range messages {
		if err := validateEligibleRootInboxMessageTx(ctx, tx, run.ID, root.ID, message); err != nil {
			return domain.RootInboxContextBatch{}, err
		}
		delivery := domain.RootInboxDelivery{
			RunID: run.ID, RootAgentID: root.ID, SupervisorAttemptID: current.AttemptID,
			Turn: current.NextTurn, MessageID: message.ID, Ordinal: index + 1,
			Status: domain.RootInboxDeliveryPrepared, PreparedAt: now,
		}
		if err := delivery.Validate(); err != nil {
			return domain.RootInboxContextBatch{}, err
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO root_inbox_deliveries
			(run_id, root_agent_id, supervisor_attempt_id, turn_number, message_id, ordinal,
			status, prepared_at, resolved_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, NULL)`,
			delivery.RunID, delivery.RootAgentID, delivery.SupervisorAttemptID, delivery.Turn,
			delivery.MessageID, delivery.Ordinal, delivery.Status, ts(delivery.PreparedAt)); err != nil {
			return domain.RootInboxContextBatch{}, err
		}
		payloadBytes += len([]byte(message.PayloadJSON))
	}
	if err := appendSupervisorEventTx(ctx, tx, run, events.AgentInboxContextPreparedEvent,
		"agent_coordinator", current.AttemptID, map[string]any{
			"agent_id": root.ID, "turn": current.NextTurn,
			"message_count": len(messages), "payload_bytes": payloadBytes,
		}); err != nil {
		return domain.RootInboxContextBatch{}, err
	}
	if _, err := createAgentGraphSnapshotTx(ctx, tx, run); err != nil {
		return domain.RootInboxContextBatch{}, err
	}
	if err := pruneSupersededRootInboxDeliveriesTx(ctx, tx, run.ID); err != nil {
		return domain.RootInboxContextBatch{}, err
	}
	if err := tx.Commit(); err != nil {
		return domain.RootInboxContextBatch{}, err
	}
	return batch, nil
}

func loadPreparedRootInboxBatchTx(ctx context.Context, tx *sql.Tx,
	checkpoint domain.SupervisorCheckpoint, root domain.AgentNode,
) (domain.RootInboxContextBatch, bool, error) {
	deliveries, err := listRootInboxDeliveriesTx(ctx, tx, rootInboxDeliverySelect+
		` WHERE run_id = ? AND root_agent_id = ? AND supervisor_attempt_id = ? AND status = ?
		ORDER BY ordinal`, checkpoint.RunID, root.ID, checkpoint.AttemptID,
		domain.RootInboxDeliveryPrepared)
	if err != nil {
		return domain.RootInboxContextBatch{}, false, err
	}
	if len(deliveries) == 0 {
		return domain.RootInboxContextBatch{}, false, nil
	}
	messages := make([]domain.AgentMessage, 0, len(deliveries))
	for index, delivery := range deliveries {
		if delivery.Turn != checkpoint.NextTurn || delivery.Ordinal != index+1 {
			return domain.RootInboxContextBatch{}, false, apperror.New(
				apperror.CodeFailedPrecondition, "root inbox delivery order does not match its Supervisor turn")
		}
		message, err := scanAgentMessage(tx.QueryRowContext(ctx,
			agentMessageSelect+` WHERE id = ?`, delivery.MessageID))
		if err != nil {
			return domain.RootInboxContextBatch{}, false, err
		}
		if err := validateEligibleRootInboxMessageTx(ctx, tx, checkpoint.RunID, root.ID,
			message); err != nil {
			return domain.RootInboxContextBatch{}, false, err
		}
		messages = append(messages, message)
	}
	batch := domain.RootInboxContextBatch{
		RunID: checkpoint.RunID, RootAgentID: root.ID,
		SupervisorAttemptID: checkpoint.AttemptID, Turn: checkpoint.NextTurn,
		Messages: messages, PreparedAt: deliveries[0].PreparedAt,
	}
	return batch, true, batch.Validate()
}

func listEligibleRootInboxMessagesTx(ctx context.Context, tx *sql.Tx, runID string,
	rootAgentID string, limit int,
) ([]domain.AgentMessage, error) {
	rows, err := tx.QueryContext(ctx, rootInboxMessageSelect+
		` JOIN agent_nodes sender
			ON sender.run_id = message.run_id AND sender.id = message.sender_agent_id
		WHERE message.run_id = ? AND message.recipient_agent_id = ?
			AND message.status = ? AND sender.role = ? AND sender.parent_id = ?
			AND (
				(message.semantic = ? AND message.kind = ?)
				OR (message.semantic = ? AND message.kind = ? AND EXISTS (
					SELECT 1 FROM agent_completion_reports report
					WHERE report.message_id = message.id AND report.agent_id = sender.id
				))
				OR (message.semantic = ? AND message.kind = ? AND EXISTS (
					SELECT 1 FROM agent_attempts attempt
					WHERE attempt.notification_message_id = message.id
						AND attempt.agent_id = sender.id AND attempt.status = ?
				))
			)
		ORDER BY message.sequence LIMIT ?`, runID, rootAgentID, domain.AgentMessagePending,
		domain.AgentRoleSpecialist, rootAgentID,
		domain.AgentMessageSemanticDependency, domain.AgentMessageNotification,
		domain.AgentMessageSemanticMessage, domain.AgentMessageResult,
		domain.AgentMessageSemanticMessage, domain.AgentMessageNotification,
		domain.AgentAttemptCrashed, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanAgentMessages(rows)
}

func validateEligibleRootInboxMessageTx(ctx context.Context, tx *sql.Tx, runID string,
	rootAgentID string, message domain.AgentMessage,
) error {
	if err := message.Validate(); err != nil {
		return err
	}
	if message.RunID != runID || message.RecipientAgentID != rootAgentID ||
		message.SenderAgentID == "" || message.Status != domain.AgentMessagePending ||
		!domain.EligibleRootInboxMessage(message) {
		return apperror.New(apperror.CodeFailedPrecondition,
			"root inbox context message is no longer eligible")
	}
	sender, err := scanAgentNode(tx.QueryRowContext(ctx, agentLifecycleSelect+` WHERE id = ?`,
		message.SenderAgentID))
	if err != nil {
		return err
	}
	if sender.RunID != runID || sender.Role != domain.AgentRoleSpecialist ||
		sender.ParentID != rootAgentID {
		return apperror.New(apperror.CodeFailedPrecondition,
			"root inbox context sender is not a direct Specialist child")
	}
	switch {
	case message.Semantic == domain.AgentMessageSemanticDependency:
		_, err = domain.DecodeAgentDependencyPayload(message.PayloadJSON)
		return err
	case message.Kind == domain.AgentMessageResult:
		payload, err := domain.DecodeAgentCompletionInboxPayload(message.PayloadJSON)
		if err != nil || payload.AgentID != sender.ID {
			return apperror.Wrap(apperror.CodeFailedPrecondition,
				"root inbox completion payload does not match its sender", err)
		}
		completion, err := scanAgentCompletion(tx.QueryRowContext(ctx,
			agentCompletionSelect+` WHERE completion.message_id = ?`, message.ID))
		if err != nil {
			return err
		}
		if completion.ID != payload.CompletionReportID || completion.AgentID != sender.ID ||
			!sameCompletionReport(completion.Report, payload.Report) {
			return apperror.New(apperror.CodeFailedPrecondition,
				"root inbox completion payload does not match its durable report")
		}
		return nil
	case message.Kind == domain.AgentMessageNotification:
		payload, err := domain.DecodeAgentAttemptFailurePayload(message.PayloadJSON)
		if err != nil || payload.AgentID != sender.ID {
			return apperror.Wrap(apperror.CodeFailedPrecondition,
				"root inbox failure payload does not match its sender", err)
		}
		attempt, err := scanAgentAttempt(tx.QueryRowContext(ctx,
			agentAttemptSelect+` WHERE notification_message_id = ?`, message.ID))
		if err != nil {
			return err
		}
		if attempt.ID != payload.AttemptID || attempt.AgentID != sender.ID ||
			attempt.Status != domain.AgentAttemptCrashed ||
			attempt.Failure.Code != payload.FailureCode || attempt.Failure.Reason != payload.Reason {
			return apperror.New(apperror.CodeFailedPrecondition,
				"root inbox failure payload does not match its durable attempt")
		}
		return nil
	default:
		return apperror.New(apperror.CodeFailedPrecondition,
			"root inbox context message protocol is unsupported")
	}
}

func sameCompletionReport(left domain.CompletionReport, right domain.CompletionReport) bool {
	return left.Version == right.Version && left.Outcome == right.Outcome &&
		left.Summary == right.Summary && slices.Equal(left.WorkItemIDs, right.WorkItemIDs) &&
		slices.Equal(left.NoteIDs, right.NoteIDs) && bytes.Equal(left.Data, right.Data)
}

func commitRootInboxContextTx(ctx context.Context, tx *sql.Tx, run domain.Run,
	root domain.AgentNode, checkpoint domain.SupervisorCheckpoint, committedAt time.Time,
) (int, error) {
	deliveries, err := listRootInboxDeliveriesTx(ctx, tx, rootInboxDeliverySelect+
		` WHERE run_id = ? AND root_agent_id = ? AND supervisor_attempt_id = ? AND status = ?
		ORDER BY ordinal`, run.ID, root.ID, checkpoint.AttemptID,
		domain.RootInboxDeliveryPrepared)
	if err != nil {
		return 0, err
	}
	for index, delivery := range deliveries {
		if delivery.Turn != checkpoint.NextTurn || delivery.Ordinal != index+1 {
			return 0, apperror.New(apperror.CodeFailedPrecondition,
				"root inbox delivery does not match the completing Supervisor turn")
		}
		message, err := scanAgentMessage(tx.QueryRowContext(ctx,
			agentMessageSelect+` WHERE id = ?`, delivery.MessageID))
		if err != nil {
			return 0, err
		}
		if err := validateEligibleRootInboxMessageTx(ctx, tx, run.ID, root.ID, message); err != nil {
			return 0, err
		}
		result, err := tx.ExecContext(ctx, `UPDATE root_inbox_deliveries
			SET status = ?, resolved_at = ?
			WHERE supervisor_attempt_id = ? AND message_id = ? AND status = ?`,
			domain.RootInboxDeliveryCommitted, ts(committedAt), checkpoint.AttemptID,
			message.ID, domain.RootInboxDeliveryPrepared)
		if err != nil {
			return 0, err
		}
		if err := requireSingleRootInboxUpdate(result,
			"root inbox delivery changed before turn commit"); err != nil {
			return 0, err
		}
		result, err = tx.ExecContext(ctx, `UPDATE agent_messages SET status = ?, consumed_at = ?
			WHERE id = ? AND status = ?`, domain.AgentMessageConsumed, ts(committedAt),
			message.ID, domain.AgentMessagePending)
		if err != nil {
			return 0, err
		}
		if err := requireSingleRootInboxUpdate(result,
			"root inbox message changed before turn commit"); err != nil {
			return 0, err
		}
		if err := appendSupervisorEventTx(ctx, tx, run, events.AgentMessageConsumedEvent,
			"run_supervisor", message.ID, map[string]any{
				"recipient_agent_id": root.ID, "sequence": message.Sequence,
				"kind": message.Kind, "semantic": message.Semantic,
				"supervisor_attempt_id": checkpoint.AttemptID,
			}); err != nil {
			return 0, err
		}
	}
	if len(deliveries) > 0 {
		if err := appendSupervisorEventTx(ctx, tx, run, events.AgentInboxContextCommittedEvent,
			"run_supervisor", checkpoint.AttemptID, map[string]any{
				"agent_id": root.ID, "turn": checkpoint.NextTurn,
				"message_count": len(deliveries),
			}); err != nil {
			return 0, err
		}
	}
	return len(deliveries), nil
}

func supersedeRootInboxDeliveriesTx(ctx context.Context, tx *sql.Tx, run domain.Run,
	rootAgentID string, exceptAttemptID string, resolvedAt time.Time,
) (int, error) {
	query := `SELECT supervisor_attempt_id, COUNT(*) FROM root_inbox_deliveries
		WHERE run_id = ? AND root_agent_id = ? AND status = ?`
	args := []any{run.ID, rootAgentID, domain.RootInboxDeliveryPrepared}
	if exceptAttemptID != "" {
		query += ` AND supervisor_attempt_id <> ?`
		args = append(args, exceptAttemptID)
	}
	query += ` GROUP BY supervisor_attempt_id ORDER BY supervisor_attempt_id`
	rows, err := tx.QueryContext(ctx, query, args...)
	if err != nil {
		return 0, err
	}
	type staleBatch struct {
		attemptID string
		count     int
	}
	stale := make([]staleBatch, 0)
	for rows.Next() {
		var item staleBatch
		if err := rows.Scan(&item.attemptID, &item.count); err != nil {
			_ = rows.Close()
			return 0, err
		}
		stale = append(stale, item)
	}
	if err := rows.Close(); err != nil {
		return 0, err
	}
	if err := rows.Err(); err != nil {
		return 0, err
	}
	total := 0
	for _, item := range stale {
		result, err := tx.ExecContext(ctx, `UPDATE root_inbox_deliveries
			SET status = ?, resolved_at = ?
			WHERE run_id = ? AND root_agent_id = ? AND supervisor_attempt_id = ? AND status = ?`,
			domain.RootInboxDeliverySuperseded, ts(resolvedAt), run.ID, rootAgentID,
			item.attemptID, domain.RootInboxDeliveryPrepared)
		if err != nil {
			return 0, err
		}
		rowsAffected, err := result.RowsAffected()
		if err != nil {
			return 0, err
		}
		if int(rowsAffected) != item.count {
			return 0, apperror.New(apperror.CodeConflict,
				"root inbox delivery changed before supersession")
		}
		total += item.count
		if err := appendSupervisorEventTx(ctx, tx, run,
			events.AgentInboxContextSupersededEvent, "run_supervisor", item.attemptID,
			map[string]any{"agent_id": rootAgentID, "message_count": item.count}); err != nil {
			return 0, err
		}
	}
	return total, nil
}

func pruneSupersededRootInboxDeliveriesTx(ctx context.Context, tx *sql.Tx,
	runID string,
) error {
	_, err := tx.ExecContext(ctx, `DELETE FROM root_inbox_deliveries
		WHERE rowid IN (
			SELECT rowid FROM root_inbox_deliveries
			WHERE run_id = ? AND status = ?
			ORDER BY resolved_at DESC, supervisor_attempt_id DESC, ordinal DESC
			LIMIT -1 OFFSET ?
		)`, runID, domain.RootInboxDeliverySuperseded, domain.MaxRootInboxDeliveryHistory)
	return err
}

func listRootInboxDeliveriesTx(ctx context.Context, tx *sql.Tx, query string,
	args ...any,
) ([]domain.RootInboxDelivery, error) {
	rows, err := tx.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := make([]domain.RootInboxDelivery, 0)
	for rows.Next() {
		item, err := scanRootInboxDelivery(rows)
		if err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

func scanRootInboxDelivery(row scanner) (domain.RootInboxDelivery, error) {
	var item domain.RootInboxDelivery
	var status, preparedAt string
	var resolvedAt sql.NullString
	if err := row.Scan(&item.RunID, &item.RootAgentID, &item.SupervisorAttemptID,
		&item.Turn, &item.MessageID, &item.Ordinal, &status, &preparedAt,
		&resolvedAt); err != nil {
		return domain.RootInboxDelivery{}, err
	}
	item.Status = domain.RootInboxDeliveryStatus(status)
	item.PreparedAt = parseTS(preparedAt)
	item.ResolvedAt = parseNullableTS(resolvedAt)
	return item, item.Validate()
}

func requireSingleRootInboxUpdate(result sql.Result, message string) error {
	rows, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if rows != 1 {
		return apperror.New(apperror.CodeConflict, message)
	}
	return nil
}

func validateRootInboxDeliveryProjectionTx(ctx context.Context, tx *sql.Tx,
	nodes []domain.AgentNode,
) error {
	for _, node := range nodes {
		if node.Role != domain.AgentRoleRoot {
			continue
		}
		deliveries, err := listRootInboxDeliveriesTx(ctx, tx, rootInboxDeliverySelect+
			` WHERE run_id = ? AND root_agent_id = ? AND status = ? ORDER BY ordinal`,
			node.RunID, node.ID, domain.RootInboxDeliveryPrepared)
		if err != nil {
			return err
		}
		if node.Status != domain.AgentRunning {
			if len(deliveries) != 0 {
				return apperror.New(apperror.CodeFailedPrecondition,
					"non-running root Agent unexpectedly has prepared inbox delivery")
			}
			continue
		}
		if len(deliveries) > domain.MaxRootInboxContextMessages {
			return apperror.New(apperror.CodeFailedPrecondition,
				"root Agent prepared inbox delivery exceeds its context bound")
		}
		for index, delivery := range deliveries {
			if delivery.SupervisorAttemptID != node.ActiveAttemptID ||
				delivery.Ordinal != index+1 {
				return apperror.New(apperror.CodeFailedPrecondition,
					"root Agent prepared inbox delivery does not match its active attempt")
			}
			message, err := scanAgentMessage(tx.QueryRowContext(ctx,
				agentMessageSelect+` WHERE id = ?`, delivery.MessageID))
			if err != nil {
				return err
			}
			if err := validateEligibleRootInboxMessageTx(ctx, tx, node.RunID, node.ID,
				message); err != nil {
				return err
			}
		}
	}
	return nil
}
