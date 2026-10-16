package store

import (
	"context"
	"database/sql"
	"time"

	"cyberagent-workbench/internal/apperror"
	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/events"
)

const specialistContextDeliverySelect = `SELECT run_id, agent_id, parent_agent_id,
	agent_attempt_id, turn_number, message_id, ordinal, status, prepared_at, resolved_at
	FROM specialist_context_deliveries`

func (s *SQLiteStore) PrepareSpecialistContext(ctx context.Context,
	ref domain.AgentAttemptRef,
) (domain.SpecialistContextBatch, error) {
	ref = normalizeAgentAttemptRef(ref)
	if err := ref.Validate(); err != nil {
		return domain.SpecialistContextBatch{}, apperror.Wrap(apperror.CodeInvalidArgument,
			"Specialist context reference is invalid", err)
	}
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{})
	if err != nil {
		return domain.SpecialistContextBatch{}, err
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx, `UPDATE agent_nodes SET updated_at = updated_at WHERE id = ?`,
		ref.AgentID); err != nil {
		return domain.SpecialistContextBatch{}, err
	}
	attempt, child, run, err := loadActiveAgentAttemptTx(ctx, tx, ref)
	if err != nil {
		return domain.SpecialistContextBatch{}, err
	}
	if attempt.UsageRecordedAt != nil {
		return domain.SpecialistContextBatch{}, apperror.New(apperror.CodeFailedPrecondition,
			"Specialist context must be prepared before model usage is recorded")
	}
	parent, err := scanAgentNode(tx.QueryRowContext(ctx, agentLifecycleSelect+` WHERE id = ?`,
		attempt.ParentAgentID))
	if err != nil {
		return domain.SpecialistContextBatch{}, err
	}
	if parent.RunID != run.ID || parent.Role != domain.AgentRoleRoot || parent.Terminal() ||
		child.ParentID != parent.ID {
		return domain.SpecialistContextBatch{}, apperror.New(apperror.CodeFailedPrecondition,
			"Specialist context parent is not the active Run root")
	}
	batch, found, err := loadPreparedSpecialistContextBatchTx(ctx, tx, attempt, child, parent)
	if err != nil {
		return domain.SpecialistContextBatch{}, err
	}
	brief, briefFound, err := loadSpecialistTaskBriefTx(ctx, tx, attempt)
	if err != nil {
		return domain.SpecialistContextBatch{}, err
	}
	if found && !briefFound {
		return domain.SpecialistContextBatch{}, apperror.New(apperror.CodeFailedPrecondition,
			"legacy prepared Specialist context must recover through a fresh attempt")
	}
	if briefFound {
		if !found {
			var preparedAt string
			if err := tx.QueryRowContext(ctx, `SELECT prepared_at FROM specialist_task_briefs WHERE agent_attempt_id=?`, attempt.ID).Scan(&preparedAt); err != nil {
				return domain.SpecialistContextBatch{}, err
			}
			batch = domain.SpecialistContextBatch{RunID: run.ID, AgentID: child.ID, ParentAgentID: parent.ID,
				AgentAttemptID: attempt.ID, Turn: attempt.Turn, Messages: []domain.AgentMessage{}, PreparedAt: parseTS(preparedAt)}
		}
		batch.TaskBrief = brief
		batch.Recovered = true
		if err := tx.Commit(); err != nil {
			return domain.SpecialistContextBatch{}, err
		}
		return batch, nil
	}
	now := time.Now().UTC()
	superseded, err := supersedeSpecialistContextDeliveriesTx(ctx, tx, run, child.ID,
		attempt.ID, now)
	if err != nil {
		return domain.SpecialistContextBatch{}, err
	}
	messages, err := listEligibleSpecialistContextMessagesTx(ctx, tx, run.ID, child.ID,
		parent.ID, domain.MaxSpecialistContextMessages)
	if err != nil {
		return domain.SpecialistContextBatch{}, err
	}
	batch = domain.SpecialistContextBatch{
		RunID: run.ID, AgentID: child.ID, ParentAgentID: parent.ID,
		AgentAttemptID: attempt.ID, Turn: attempt.Turn, Messages: messages, PreparedAt: now,
	}
	for _, message := range messages {
		if err := validateEligibleSpecialistContextMessageTx(ctx, tx, run.ID, child, parent, message); err != nil {
			return domain.SpecialistContextBatch{}, err
		}
	}
	batch.TaskBrief, err = prepareSpecialistTaskBriefTx(ctx, tx, attempt, messages, now)
	if err != nil {
		return domain.SpecialistContextBatch{}, err
	}
	if err := batch.Validate(); err != nil {
		return domain.SpecialistContextBatch{}, err
	}
	payloadBytes := 0
	for index, message := range messages {
		if err := validateEligibleSpecialistContextMessageTx(ctx, tx, run.ID, child, parent,
			message); err != nil {
			return domain.SpecialistContextBatch{}, err
		}
		delivery := domain.SpecialistContextDelivery{
			RunID: run.ID, AgentID: child.ID, ParentAgentID: parent.ID,
			AgentAttemptID: attempt.ID, Turn: attempt.Turn, MessageID: message.ID,
			Ordinal: index + 1, Status: domain.RootInboxDeliveryPrepared, PreparedAt: now,
		}
		if err := delivery.Validate(); err != nil {
			return domain.SpecialistContextBatch{}, err
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO specialist_context_deliveries
			(run_id, agent_id, parent_agent_id, agent_attempt_id, turn_number, message_id,
			ordinal, status, prepared_at, resolved_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, NULL)`, delivery.RunID, delivery.AgentID,
			delivery.ParentAgentID, delivery.AgentAttemptID, delivery.Turn, delivery.MessageID,
			delivery.Ordinal, delivery.Status, ts(delivery.PreparedAt)); err != nil {
			return domain.SpecialistContextBatch{}, err
		}
		payloadBytes += len([]byte(message.PayloadJSON))
	}
	if len(messages) > 0 {
		if err := appendSupervisorEventTx(ctx, tx, run, events.AgentInboxContextPreparedEvent,
			"specialist_runner", attempt.ID, map[string]any{
				"agent_id": child.ID, "parent_agent_id": parent.ID, "turn": attempt.Turn,
				"message_count": len(messages), "payload_bytes": payloadBytes,
			}); err != nil {
			return domain.SpecialistContextBatch{}, err
		}
	}
	if len(messages) > 0 || superseded > 0 {
		if _, err := createAgentGraphSnapshotTx(ctx, tx, run); err != nil {
			return domain.SpecialistContextBatch{}, err
		}
	}
	if err := pruneSupersededSpecialistContextDeliveriesTx(ctx, tx, run.ID); err != nil {
		return domain.SpecialistContextBatch{}, err
	}
	if err := tx.Commit(); err != nil {
		return domain.SpecialistContextBatch{}, err
	}
	return batch, nil
}

func loadPreparedSpecialistContextBatchTx(ctx context.Context, tx *sql.Tx,
	attempt domain.AgentAttempt, child domain.AgentNode, parent domain.AgentNode,
) (domain.SpecialistContextBatch, bool, error) {
	deliveries, err := listSpecialistContextDeliveriesTx(ctx, tx,
		specialistContextDeliverySelect+` WHERE run_id = ? AND agent_id = ?
		AND agent_attempt_id = ? AND status = ? ORDER BY ordinal`, attempt.RunID, child.ID,
		attempt.ID, domain.RootInboxDeliveryPrepared)
	if err != nil {
		return domain.SpecialistContextBatch{}, false, err
	}
	if len(deliveries) == 0 {
		return domain.SpecialistContextBatch{}, false, nil
	}
	messages := make([]domain.AgentMessage, 0, len(deliveries))
	for index, delivery := range deliveries {
		if delivery.Turn != attempt.Turn || delivery.ParentAgentID != parent.ID ||
			delivery.Ordinal != index+1 {
			return domain.SpecialistContextBatch{}, false, apperror.New(
				apperror.CodeFailedPrecondition,
				"Specialist context delivery order does not match its Agent attempt")
		}
		message, err := scanAgentMessage(tx.QueryRowContext(ctx,
			agentMessageSelect+` WHERE id = ?`, delivery.MessageID))
		if err != nil {
			return domain.SpecialistContextBatch{}, false, err
		}
		if err := validateEligibleSpecialistContextMessageTx(ctx, tx, attempt.RunID, child,
			parent, message); err != nil {
			return domain.SpecialistContextBatch{}, false, err
		}
		messages = append(messages, message)
	}
	batch := domain.SpecialistContextBatch{
		RunID: attempt.RunID, AgentID: child.ID, ParentAgentID: parent.ID,
		AgentAttemptID: attempt.ID, Turn: attempt.Turn, Messages: messages,
		PreparedAt: deliveries[0].PreparedAt,
	}
	return batch, true, batch.Validate()
}

func listEligibleSpecialistContextMessagesTx(ctx context.Context, tx *sql.Tx, runID string,
	agentID string, parentAgentID string, limit int,
) ([]domain.AgentMessage, error) {
	rows, err := tx.QueryContext(ctx, rootInboxMessageSelect+`
		JOIN agent_nodes child
			ON child.run_id = message.run_id AND child.id = message.recipient_agent_id
		JOIN agent_nodes parent
			ON parent.run_id = message.run_id AND parent.id = message.sender_agent_id
		WHERE message.run_id = ? AND message.recipient_agent_id = ?
			AND message.sender_agent_id = ? AND message.status = ?
			AND message.kind = ? AND message.semantic = ?
			AND child.role = ? AND child.parent_id = parent.id
			AND parent.role = ? AND parent.status IN (?, ?, ?)
		ORDER BY message.sequence LIMIT ?`, runID, agentID, parentAgentID,
		domain.AgentMessagePending, domain.AgentMessageInstruction,
		domain.AgentMessageSemanticMessage, domain.AgentRoleSpecialist, domain.AgentRoleRoot,
		domain.AgentReady, domain.AgentRunning, domain.AgentWaiting, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanAgentMessages(rows)
}

func validateEligibleSpecialistContextMessageTx(ctx context.Context, tx *sql.Tx, runID string,
	child domain.AgentNode, parent domain.AgentNode, message domain.AgentMessage,
) error {
	if err := message.Validate(); err != nil {
		return err
	}
	if message.RunID != runID || message.RecipientAgentID != child.ID ||
		message.SenderAgentID != parent.ID || message.Status != domain.AgentMessagePending ||
		!domain.EligibleSpecialistContextMessage(message) {
		return apperror.New(apperror.CodeFailedPrecondition,
			"Specialist context instruction is no longer eligible")
	}
	storedChild, err := scanAgentNode(tx.QueryRowContext(ctx, agentLifecycleSelect+` WHERE id = ?`,
		child.ID))
	if err != nil {
		return err
	}
	storedParent, err := scanAgentNode(tx.QueryRowContext(ctx, agentLifecycleSelect+` WHERE id = ?`,
		parent.ID))
	if err != nil {
		return err
	}
	if storedChild.RunID != runID || storedChild.Role != domain.AgentRoleSpecialist ||
		storedChild.ParentID != storedParent.ID || storedParent.RunID != runID ||
		storedParent.Role != domain.AgentRoleRoot || storedParent.Terminal() {
		return apperror.New(apperror.CodeFailedPrecondition,
			"Specialist context instruction is not from its direct root parent")
	}
	_, err = domain.DecodeAgentInstructionPayload(message.PayloadJSON)
	return err
}

func commitSpecialistContextTx(ctx context.Context, tx *sql.Tx, run domain.Run,
	child domain.AgentNode, parent domain.AgentNode, attempt domain.AgentAttempt,
	committedAt time.Time,
) (int, error) {
	deliveries, err := listSpecialistContextDeliveriesTx(ctx, tx,
		specialistContextDeliverySelect+` WHERE run_id = ? AND agent_id = ?
		AND agent_attempt_id = ? AND status = ? ORDER BY ordinal`, run.ID, child.ID,
		attempt.ID, domain.RootInboxDeliveryPrepared)
	if err != nil {
		return 0, err
	}
	for index, delivery := range deliveries {
		if delivery.Turn != attempt.Turn || delivery.ParentAgentID != parent.ID ||
			delivery.Ordinal != index+1 {
			return 0, apperror.New(apperror.CodeFailedPrecondition,
				"Specialist context delivery does not match the completing Agent attempt")
		}
		message, err := scanAgentMessage(tx.QueryRowContext(ctx,
			agentMessageSelect+` WHERE id = ?`, delivery.MessageID))
		if err != nil {
			return 0, err
		}
		if err := validateEligibleSpecialistContextMessageTx(ctx, tx, run.ID, child, parent,
			message); err != nil {
			return 0, err
		}
		result, err := tx.ExecContext(ctx, `UPDATE specialist_context_deliveries
			SET status = ?, resolved_at = ?
			WHERE agent_attempt_id = ? AND message_id = ? AND status = ?`,
			domain.RootInboxDeliveryCommitted, ts(committedAt), attempt.ID, message.ID,
			domain.RootInboxDeliveryPrepared)
		if err != nil {
			return 0, err
		}
		if err := requireSingleSpecialistContextUpdate(result,
			"Specialist context delivery changed before attempt commit"); err != nil {
			return 0, err
		}
		result, err = tx.ExecContext(ctx, `UPDATE agent_messages SET status = ?, consumed_at = ?
			WHERE id = ? AND status = ?`, domain.AgentMessageConsumed, ts(committedAt),
			message.ID, domain.AgentMessagePending)
		if err != nil {
			return 0, err
		}
		if err := requireSingleSpecialistContextUpdate(result,
			"Specialist instruction changed before attempt commit"); err != nil {
			return 0, err
		}
		if err := appendSupervisorEventTx(ctx, tx, run, events.AgentMessageConsumedEvent,
			"specialist_runner", message.ID, map[string]any{
				"recipient_agent_id": child.ID, "sender_agent_id": parent.ID,
				"sequence": message.Sequence, "kind": message.Kind,
				"semantic": message.Semantic, "agent_attempt_id": attempt.ID,
			}); err != nil {
			return 0, err
		}
	}
	if len(deliveries) > 0 {
		if err := appendSupervisorEventTx(ctx, tx, run, events.AgentInboxContextCommittedEvent,
			"specialist_runner", attempt.ID, map[string]any{
				"agent_id": child.ID, "parent_agent_id": parent.ID, "turn": attempt.Turn,
				"message_count": len(deliveries),
			}); err != nil {
			return 0, err
		}
	}
	return len(deliveries), nil
}

func supersedeSpecialistContextDeliveriesTx(ctx context.Context, tx *sql.Tx, run domain.Run,
	agentID string, exceptAttemptID string, resolvedAt time.Time,
) (int, error) {
	query := `SELECT agent_attempt_id, COUNT(*) FROM specialist_context_deliveries
		WHERE run_id = ? AND agent_id = ? AND status = ?`
	args := []any{run.ID, agentID, domain.RootInboxDeliveryPrepared}
	if exceptAttemptID != "" {
		query += ` AND agent_attempt_id <> ?`
		args = append(args, exceptAttemptID)
	}
	query += ` GROUP BY agent_attempt_id ORDER BY agent_attempt_id`
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
		result, err := tx.ExecContext(ctx, `UPDATE specialist_context_deliveries
			SET status = ?, resolved_at = ?
			WHERE run_id = ? AND agent_id = ? AND agent_attempt_id = ? AND status = ?`,
			domain.RootInboxDeliverySuperseded, ts(resolvedAt), run.ID, agentID,
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
				"Specialist context delivery changed before supersession")
		}
		total += item.count
		if err := appendSupervisorEventTx(ctx, tx, run,
			events.AgentInboxContextSupersededEvent, "specialist_runner", item.attemptID,
			map[string]any{"agent_id": agentID, "message_count": item.count}); err != nil {
			return 0, err
		}
	}
	return total, nil
}

func pruneSupersededSpecialistContextDeliveriesTx(ctx context.Context, tx *sql.Tx,
	runID string,
) error {
	_, err := tx.ExecContext(ctx, `DELETE FROM specialist_context_deliveries
		WHERE rowid IN (
			SELECT rowid FROM specialist_context_deliveries
			WHERE run_id = ? AND status = ?
			ORDER BY resolved_at DESC, agent_attempt_id DESC, ordinal DESC
			LIMIT -1 OFFSET ?
		)`, runID, domain.RootInboxDeliverySuperseded, domain.MaxSpecialistDeliveryHistory)
	return err
}

func listSpecialistContextDeliveriesTx(ctx context.Context, tx *sql.Tx, query string,
	args ...any,
) ([]domain.SpecialistContextDelivery, error) {
	rows, err := tx.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := make([]domain.SpecialistContextDelivery, 0)
	for rows.Next() {
		item, err := scanSpecialistContextDelivery(rows)
		if err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

func scanSpecialistContextDelivery(row scanner) (domain.SpecialistContextDelivery, error) {
	var item domain.SpecialistContextDelivery
	var status, preparedAt string
	var resolvedAt sql.NullString
	if err := row.Scan(&item.RunID, &item.AgentID, &item.ParentAgentID,
		&item.AgentAttemptID, &item.Turn, &item.MessageID, &item.Ordinal, &status,
		&preparedAt, &resolvedAt); err != nil {
		return domain.SpecialistContextDelivery{}, err
	}
	item.Status = domain.RootInboxDeliveryStatus(status)
	item.PreparedAt = parseTS(preparedAt)
	item.ResolvedAt = parseNullableTS(resolvedAt)
	return item, item.Validate()
}

func requireSingleSpecialistContextUpdate(result sql.Result, message string) error {
	rows, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if rows != 1 {
		return apperror.New(apperror.CodeConflict, message)
	}
	return nil
}

func validateSpecialistContextDeliveryProjectionTx(ctx context.Context, tx *sql.Tx,
	nodes []domain.AgentNode,
) error {
	for _, node := range nodes {
		if node.Role != domain.AgentRoleSpecialist {
			continue
		}
		deliveries, err := listSpecialistContextDeliveriesTx(ctx, tx,
			specialistContextDeliverySelect+` WHERE run_id = ? AND agent_id = ? AND status = ?
			ORDER BY agent_attempt_id, ordinal`, node.RunID, node.ID,
			domain.RootInboxDeliveryPrepared)
		if err != nil {
			return err
		}
		if node.Status != domain.AgentRunning {
			if len(deliveries) != 0 {
				return apperror.New(apperror.CodeFailedPrecondition,
					"non-running Specialist unexpectedly has prepared context delivery")
			}
			continue
		}
		if len(deliveries) > domain.MaxSpecialistContextMessages {
			return apperror.New(apperror.CodeFailedPrecondition,
				"Specialist prepared context delivery exceeds its context bound")
		}
		parent, err := scanAgentNode(tx.QueryRowContext(ctx, agentLifecycleSelect+` WHERE id = ?`,
			node.ParentID))
		if err != nil {
			return err
		}
		attempt, err := scanAgentAttempt(tx.QueryRowContext(ctx,
			agentAttemptSelect+` WHERE id = ?`, node.ActiveAttemptID))
		if err != nil {
			return err
		}
		for index, delivery := range deliveries {
			if delivery.AgentAttemptID != node.ActiveAttemptID ||
				delivery.ParentAgentID != parent.ID || delivery.Turn != attempt.Turn ||
				delivery.Ordinal != index+1 {
				return apperror.New(apperror.CodeFailedPrecondition,
					"Specialist prepared context delivery does not match its active attempt")
			}
			message, err := scanAgentMessage(tx.QueryRowContext(ctx,
				agentMessageSelect+` WHERE id = ?`, delivery.MessageID))
			if err != nil {
				return err
			}
			if err := validateEligibleSpecialistContextMessageTx(ctx, tx, node.RunID, node,
				parent, message); err != nil {
				return err
			}
		}
	}
	return nil
}
