package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"cyberagent-workbench/internal/apperror"
	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/events"
	"cyberagent-workbench/internal/idgen"
	"cyberagent-workbench/internal/redact"
	"cyberagent-workbench/internal/runmutation"
)

const agentNodeColumns = `SELECT id, run_id, parent_id, session_id, role, profile, skills_json, status,
	depth, child_limit, turn_limit, token_limit, turns_used, tokens_used, active_attempt_id,
	status_reason, version, created_at, updated_at, finished_at`

// Identity/lifecycle consumers and immutable graph snapshots do not consume child authority
// or cumulative usage. Keep their projection independent of the child ledgers.
const agentLifecycleSelect = agentNodeColumns + `, '[]', 0, 0, 0, 0 FROM agent_nodes`

const agentNodeSelect = agentNodeColumns + `, allowed_read_paths_json, tool_call_limit, cost_limit_micros,
 (SELECT COUNT(*) FROM specialist_tool_calls c JOIN agent_attempts a ON a.id=c.agent_attempt_id WHERE a.agent_id=agent_nodes.id AND c.charge_id<>''),
 (SELECT COALESCE(SUM(reserved_micros-released_micros),0) FROM run_monetary_reservations WHERE agent_id=agent_nodes.id AND run_id=agent_nodes.run_id) FROM agent_nodes`

const agentMessageSelect = `SELECT id, run_id, sender_agent_id, recipient_agent_id, sequence, kind,
	semantic, payload_json, status, created_at, consumed_at FROM agent_messages`

type rootAgentProjection struct {
	Status          domain.AgentStatus
	ActiveAttemptID string
	StatusReason    string
	TurnsUsed       int64
	TokensUsed      int64
}

func (s *SQLiteStore) RegisterRootAgent(ctx context.Context, runID string) (domain.AgentNode, bool, error) {
	runID = strings.TrimSpace(runID)
	if runID == "" {
		return domain.AgentNode{}, false, apperror.New(apperror.CodeInvalidArgument, "agent run id is required")
	}
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{})
	if err != nil {
		return domain.AgentNode{}, false, err
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx, `UPDATE runs SET updated_at = updated_at WHERE id = ?`, runID); err != nil {
		return domain.AgentNode{}, false, err
	}
	run, mission, err := getCoordinatorRunTx(ctx, tx, runID)
	if err != nil {
		return domain.AgentNode{}, false, err
	}
	if current, found, err := getRootAgentTx(ctx, tx, run.ID); err != nil {
		return domain.AgentNode{}, false, err
	} else if found {
		if current.SessionID != run.SessionID || current.Profile != mission.Profile {
			return domain.AgentNode{}, false, apperror.New(apperror.CodeConflict,
				"root agent identity no longer matches its run")
		}
		if _, snapshotFound, err := latestAgentGraphSnapshotTx(ctx, tx, run.ID); err != nil {
			return domain.AgentNode{}, false, err
		} else if !snapshotFound {
			if _, err := createAgentGraphSnapshotTx(ctx, tx, run); err != nil {
				return domain.AgentNode{}, false, err
			}
		}
		if err := tx.Commit(); err != nil {
			return domain.AgentNode{}, false, err
		}
		return current, false, nil
	}
	projection, err := rootAgentProjectionForRunTx(ctx, tx, run)
	if err != nil {
		return domain.AgentNode{}, false, err
	}
	node, created, changed, err := syncRootAgentTx(ctx, tx, run, mission, projection, time.Now().UTC())
	if err != nil {
		return domain.AgentNode{}, false, err
	}
	if created || changed {
		if _, err := createAgentGraphSnapshotTx(ctx, tx, run); err != nil {
			return domain.AgentNode{}, false, err
		}
	}
	if err := tx.Commit(); err != nil {
		return domain.AgentNode{}, false, err
	}
	return node, created, nil
}

func (s *SQLiteStore) GetAgentNode(ctx context.Context, id string) (domain.AgentNode, error) {
	return scanAgentNode(s.db.QueryRowContext(ctx, agentNodeSelect+` WHERE id = ?`, strings.TrimSpace(id)))
}

func (s *SQLiteStore) GetRootAgent(ctx context.Context, runID string) (domain.AgentNode, bool, error) {
	node, err := scanAgentNode(s.db.QueryRowContext(ctx, agentLifecycleSelect+` WHERE run_id = ? AND parent_id IS NULL`,
		strings.TrimSpace(runID)))
	if errors.Is(err, sql.ErrNoRows) {
		return domain.AgentNode{}, false, nil
	}
	if err != nil {
		return domain.AgentNode{}, false, err
	}
	return node, true, nil
}

func (s *SQLiteStore) ListAgentNodes(ctx context.Context, runID string) ([]domain.AgentNode, error) {
	return listAgentNodes(ctx, s.db, strings.TrimSpace(runID))
}

func (s *SQLiteStore) SendAgentMessage(ctx context.Context, message domain.AgentMessage,
	operationKey string,
) (domain.AgentMessage, bool, error) {
	message.ID = strings.TrimSpace(message.ID)
	message.RunID = strings.TrimSpace(message.RunID)
	message.SenderAgentID = strings.TrimSpace(message.SenderAgentID)
	message.RecipientAgentID = strings.TrimSpace(message.RecipientAgentID)
	message.PayloadJSON = strings.TrimSpace(message.PayloadJSON)
	if message.Semantic == "" {
		message.Semantic = domain.AgentMessageSemanticMessage
	}
	message.Status = domain.AgentMessagePending
	message.Sequence = 0
	message.ConsumedAt = nil
	if message.ID == "" || message.RunID == "" || message.RecipientAgentID == "" {
		return domain.AgentMessage{}, false, apperror.New(apperror.CodeInvalidArgument,
			"agent message id, run id, and recipient are required")
	}
	if !domain.ValidAgentMessageKind(message.Kind) {
		return domain.AgentMessage{}, false,
			apperror.New(apperror.CodeInvalidArgument, "agent message kind is invalid")
	}
	if !domain.ValidAgentMessageSemantic(message.Semantic) {
		return domain.AgentMessage{}, false,
			apperror.New(apperror.CodeInvalidArgument, "agent message semantic is invalid")
	}
	normalizedOperationKey, err := domain.NormalizeAgentOperationKey(operationKey)
	if err != nil {
		return domain.AgentMessage{}, false,
			apperror.Wrap(apperror.CodeInvalidArgument, "agent message idempotency key is invalid", err)
	}
	safePayload, err := redactJSONPayload(message.PayloadJSON)
	if err != nil {
		return domain.AgentMessage{}, false,
			apperror.Wrap(apperror.CodeInvalidArgument, "invalid agent message payload", err)
	}
	if len([]byte(safePayload)) == 0 || len([]byte(safePayload)) > domain.MaxAgentMessagePayloadBytes {
		return domain.AgentMessage{}, false, apperror.New(apperror.CodeResourceExhausted,
			fmt.Sprintf("agent message payload exceeds %d bytes", domain.MaxAgentMessagePayloadBytes))
	}
	var object map[string]any
	if err := json.Unmarshal([]byte(safePayload), &object); err != nil || object == nil {
		return domain.AgentMessage{}, false, apperror.New(apperror.CodeInvalidArgument,
			"agent message payload must be a JSON object")
	}
	if err := validateAgentMessageJSON(object, 0); err != nil {
		return domain.AgentMessage{}, false, apperror.Wrap(apperror.CodeInvalidArgument,
			"agent message payload has an invalid structure", err)
	}
	message.PayloadJSON = safePayload
	if message.CreatedAt.IsZero() {
		message.CreatedAt = time.Now().UTC()
	} else {
		message.CreatedAt = message.CreatedAt.UTC()
	}
	validationMessage := message
	validationMessage.Sequence = 1
	if err := validationMessage.Validate(); err != nil {
		return domain.AgentMessage{}, false,
			apperror.Wrap(apperror.CodeInvalidArgument, "invalid agent message", err)
	}
	keyDigest := runmutation.Fingerprint("agent_message_operation.v1", message.RunID,
		normalizedOperationKey)
	requestFingerprint := runmutation.Fingerprint("agent_message_request.v1", message.RunID,
		message.SenderAgentID, message.RecipientAgentID, string(message.Kind), string(message.Semantic),
		message.PayloadJSON)
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{})
	if err != nil {
		return domain.AgentMessage{}, false, err
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx, `UPDATE agent_nodes SET updated_at = updated_at WHERE id = ?`,
		message.RecipientAgentID); err != nil {
		return domain.AgentMessage{}, false, err
	}
	storedFingerprint, storedMessageID, found, err := getAgentMessageOperationTx(ctx, tx, keyDigest)
	if err != nil {
		return domain.AgentMessage{}, false, err
	}
	if found {
		if storedFingerprint != requestFingerprint {
			return domain.AgentMessage{}, false, apperror.New(apperror.CodeConflict,
				"agent message idempotency key was already used for different intent")
		}
		existing, err := scanAgentMessage(tx.QueryRowContext(ctx,
			agentMessageSelect+` WHERE id = ?`, storedMessageID))
		if err != nil {
			return domain.AgentMessage{}, false, err
		}
		if err := tx.Commit(); err != nil {
			return domain.AgentMessage{}, false, err
		}
		return existing, true, nil
	}
	if err := requireSpecialistDelegationApplicationMessageTx(ctx, tx, message.RunID,
		keyDigest, message.RecipientAgentID); err != nil {
		return domain.AgentMessage{}, false, err
	}
	recipient, err := scanAgentNode(tx.QueryRowContext(ctx, agentLifecycleSelect+` WHERE id = ?`, message.RecipientAgentID))
	if err != nil {
		return domain.AgentMessage{}, false, err
	}
	if recipient.RunID != message.RunID {
		return domain.AgentMessage{}, false, apperror.New(apperror.CodeInvalidArgument,
			"agent message recipient belongs to another run")
	}
	if recipient.Terminal() {
		return domain.AgentMessage{}, false, apperror.New(apperror.CodeFailedPrecondition,
			"terminal agent cannot receive inbox messages")
	}
	if message.SenderAgentID != "" {
		sender, err := scanAgentNode(tx.QueryRowContext(ctx, agentLifecycleSelect+` WHERE id = ?`, message.SenderAgentID))
		if err != nil {
			return domain.AgentMessage{}, false, err
		}
		if sender.RunID != message.RunID {
			return domain.AgentMessage{}, false, apperror.New(apperror.CodeInvalidArgument,
				"agent message sender belongs to another run")
		}
		if sender.ID == recipient.ID {
			return domain.AgentMessage{}, false, apperror.New(apperror.CodeInvalidArgument,
				"agent cannot send a message to itself")
		}
	}
	if message.Semantic == domain.AgentMessageSemanticDependency && message.SenderAgentID == "" {
		return domain.AgentMessage{}, false, apperror.New(apperror.CodeInvalidArgument,
			"dependency notification requires an agent sender")
	}
	run, err := scanRun(tx.QueryRowContext(ctx, `SELECT id, mission_id, session_id, status, config_json, budget_json,
		started_at, finished_at, created_at, updated_at FROM runs WHERE id = ?`, message.RunID))
	if err != nil {
		return domain.AgentMessage{}, false, err
	}
	var wakePayload domain.AgentWakePayload
	if message.Semantic == domain.AgentMessageSemanticWake {
		if run.Status != domain.RunRunning {
			return domain.AgentMessage{}, false, apperror.New(apperror.CodeFailedPrecondition,
				"wake requires a running Run")
		}
		if recipient.Role != domain.AgentRoleSpecialist || recipient.Status != domain.AgentWaiting {
			return domain.AgentMessage{}, false, apperror.New(apperror.CodeFailedPrecondition,
				"wake requires a waiting Specialist recipient")
		}
		wakePayload, err = domain.DecodeAgentWakePayload(message.PayloadJSON)
		if err != nil {
			return domain.AgentMessage{}, false,
				apperror.Wrap(apperror.CodeInvalidArgument, "invalid wake payload", err)
		}
	}
	var pending int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM agent_messages
		WHERE recipient_agent_id = ? AND status = ?`, recipient.ID, domain.AgentMessagePending).Scan(&pending); err != nil {
		return domain.AgentMessage{}, false, err
	}
	if pending >= domain.MaxAgentInboxMessages {
		return domain.AgentMessage{}, false,
			apperror.New(apperror.CodeResourceExhausted, "agent inbox is full")
	}
	var history int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM agent_messages
		WHERE recipient_agent_id = ?`, recipient.ID).Scan(&history); err != nil {
		return domain.AgentMessage{}, false, err
	}
	if history >= domain.MaxAgentMessageHistory {
		return domain.AgentMessage{}, false, apperror.New(apperror.CodeResourceExhausted,
			"agent message history is full")
	}
	if err := tx.QueryRowContext(ctx, `SELECT COALESCE(MAX(sequence), 0) + 1 FROM agent_messages
		WHERE recipient_agent_id = ?`, recipient.ID).Scan(&message.Sequence); err != nil {
		return domain.AgentMessage{}, false, err
	}
	if err := message.Validate(); err != nil {
		return domain.AgentMessage{}, false,
			apperror.Wrap(apperror.CodeInvalidArgument, "invalid agent message", err)
	}
	if err := validateSpecialistInstructionSendTx(ctx, tx, message, recipient); err != nil {
		return domain.AgentMessage{}, false, err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO agent_messages
		(id, run_id, sender_agent_id, recipient_agent_id, sequence, kind, semantic, payload_json, status, created_at, consumed_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, NULL)`, message.ID, message.RunID,
		nullableAgentID(message.SenderAgentID), message.RecipientAgentID, message.Sequence, message.Kind,
		message.Semantic, message.PayloadJSON, message.Status, ts(message.CreatedAt)); err != nil {
		return domain.AgentMessage{}, false, err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO agent_message_operations
		(operation_key_digest, request_fingerprint, message_id, created_at) VALUES (?, ?, ?, ?)`,
		keyDigest, requestFingerprint, message.ID, ts(message.CreatedAt)); err != nil {
		return domain.AgentMessage{}, false, err
	}
	if err := appendSupervisorEventTx(ctx, tx, run, events.AgentMessageSentEvent, "agent_coordinator", message.ID, map[string]any{
		"sender_agent_id": message.SenderAgentID, "recipient_agent_id": message.RecipientAgentID,
		"sequence": message.Sequence, "kind": message.Kind, "semantic": message.Semantic,
		"payload_bytes": len([]byte(message.PayloadJSON)),
	}); err != nil {
		return domain.AgentMessage{}, false, err
	}
	if message.Semantic == domain.AgentMessageSemanticWake {
		updated := recipient
		updated.Status = domain.AgentReady
		updated.ActiveAttemptID = ""
		updated.StatusReason = normalizeAgentStatusReason(wakePayload.Reason)
		updated.Version++
		updated.UpdatedAt = time.Now().UTC()
		if err := updated.Validate(); err != nil {
			return domain.AgentMessage{}, false, err
		}
		result, err := tx.ExecContext(ctx, `UPDATE agent_nodes SET status = ?, active_attempt_id = '',
			status_reason = ?, version = ?, updated_at = ? WHERE id = ? AND version = ? AND status = ?`,
			updated.Status, updated.StatusReason, updated.Version, ts(updated.UpdatedAt), updated.ID,
			recipient.Version, domain.AgentWaiting)
		if err != nil {
			return domain.AgentMessage{}, false, err
		}
		rows, err := result.RowsAffected()
		if err != nil {
			return domain.AgentMessage{}, false, err
		}
		if rows != 1 {
			return domain.AgentMessage{}, false,
				apperror.New(apperror.CodeConflict, "wake recipient changed concurrently")
		}
		if err := appendSupervisorEventTx(ctx, tx, run, events.AgentStatusChangedEvent,
			"agent_coordinator", updated.ID, map[string]any{
				"from": recipient.Status, "to": updated.Status, "reason": updated.StatusReason,
				"message_id": message.ID, "version": updated.Version,
			}); err != nil {
			return domain.AgentMessage{}, false, err
		}
	}
	if _, err := createAgentGraphSnapshotTx(ctx, tx, run); err != nil {
		return domain.AgentMessage{}, false, err
	}
	if err := tx.Commit(); err != nil {
		return domain.AgentMessage{}, false, err
	}
	return message, false, nil
}

func (s *SQLiteStore) ListAgentMessages(ctx context.Context, agentID string, pendingOnly bool,
	limit int,
) ([]domain.AgentMessage, error) {
	agentID = strings.TrimSpace(agentID)
	if limit <= 0 || limit > domain.MaxAgentInboxMessages {
		limit = domain.MaxAgentInboxMessages
	}
	query := agentMessageSelect + ` WHERE recipient_agent_id = ?`
	args := []any{agentID}
	if pendingOnly {
		query += ` AND status = ?`
		args = append(args, domain.AgentMessagePending)
	}
	query += ` ORDER BY sequence LIMIT ?`
	args = append(args, limit)
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanAgentMessages(rows)
}

func (s *SQLiteStore) ConsumeAgentMessages(ctx context.Context, agentID string, limit int) ([]domain.AgentMessage, error) {
	agentID = strings.TrimSpace(agentID)
	if agentID == "" {
		return nil, apperror.New(apperror.CodeInvalidArgument, "agent id is required")
	}
	if limit <= 0 {
		limit = domain.MaxAgentMessageBatch
	}
	if limit > domain.MaxAgentMessageBatch {
		return nil, apperror.New(apperror.CodeInvalidArgument,
			fmt.Sprintf("agent inbox batch exceeds %d messages", domain.MaxAgentMessageBatch))
	}
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{})
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx, `UPDATE agent_nodes SET updated_at = updated_at WHERE id = ?`, agentID); err != nil {
		return nil, err
	}
	node, err := scanAgentNode(tx.QueryRowContext(ctx, agentNodeSelect+` WHERE id = ?`, agentID))
	if err != nil {
		return nil, err
	}
	if node.Role == domain.AgentRoleRoot && node.Status == domain.AgentRunning {
		return nil, apperror.New(apperror.CodeFailedPrecondition,
			"running root Agent inbox is reserved for Supervisor context delivery")
	}
	if node.Role == domain.AgentRoleSpecialist && node.Status == domain.AgentRunning {
		return nil, apperror.New(apperror.CodeFailedPrecondition,
			"running Specialist inbox is reserved for Agent attempt context delivery")
	}
	rows, err := tx.QueryContext(ctx, agentMessageSelect+` WHERE recipient_agent_id = ? AND status = ?
		ORDER BY sequence LIMIT ?`, node.ID, domain.AgentMessagePending, limit)
	if err != nil {
		return nil, err
	}
	messages, err := scanAgentMessages(rows)
	closeErr := rows.Close()
	if err != nil {
		return nil, err
	}
	if closeErr != nil {
		return nil, closeErr
	}
	if len(messages) == 0 {
		if err := tx.Commit(); err != nil {
			return nil, err
		}
		return []domain.AgentMessage{}, nil
	}
	run, err := scanRun(tx.QueryRowContext(ctx, `SELECT id, mission_id, session_id, status, config_json, budget_json,
		started_at, finished_at, created_at, updated_at FROM runs WHERE id = ?`, node.RunID))
	if err != nil {
		return nil, err
	}
	consumedAt := time.Now().UTC()
	for index := range messages {
		result, err := tx.ExecContext(ctx, `UPDATE agent_messages SET status = ?, consumed_at = ?
			WHERE id = ? AND status = ?`, domain.AgentMessageConsumed, ts(consumedAt), messages[index].ID,
			domain.AgentMessagePending)
		if err != nil {
			return nil, err
		}
		rowsAffected, err := result.RowsAffected()
		if err != nil {
			return nil, err
		}
		if rowsAffected != 1 {
			return nil, apperror.New(apperror.CodeConflict, "agent inbox changed before consumption")
		}
		messages[index].Status = domain.AgentMessageConsumed
		messages[index].ConsumedAt = &consumedAt
		if err := messages[index].Validate(); err != nil {
			return nil, err
		}
		if err := appendSupervisorEventTx(ctx, tx, run, events.AgentMessageConsumedEvent,
			"agent_coordinator", messages[index].ID, map[string]any{
				"recipient_agent_id": node.ID, "sequence": messages[index].Sequence,
				"kind": messages[index].Kind, "semantic": messages[index].Semantic,
			}); err != nil {
			return nil, err
		}
	}
	if _, err := createAgentGraphSnapshotTx(ctx, tx, run); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return messages, nil
}

func getCoordinatorRunTx(ctx context.Context, tx *sql.Tx, runID string) (domain.Run, domain.Mission, error) {
	run, err := scanRun(tx.QueryRowContext(ctx, `SELECT id, mission_id, session_id, status, config_json, budget_json,
		started_at, finished_at, created_at, updated_at FROM runs WHERE id = ?`, runID))
	if err != nil {
		return domain.Run{}, domain.Mission{}, err
	}
	mission, err := scanMission(tx.QueryRowContext(ctx, `SELECT id, goal, profile, workspace_id, scope_json,
		created_at, updated_at FROM missions WHERE id = ?`, run.MissionID))
	if err != nil {
		return domain.Run{}, domain.Mission{}, err
	}
	return run, mission, nil
}

func rootAgentProjectionForRunTx(ctx context.Context, tx *sql.Tx, run domain.Run) (rootAgentProjection, error) {
	projection := rootAgentProjection{Status: domain.AgentReady}
	checkpoint, found, err := getSupervisorCheckpointTx(ctx, tx, run.ID)
	if err != nil {
		return rootAgentProjection{}, err
	}
	if found {
		projection.TurnsUsed = int64(checkpoint.NextTurn - 1)
		projection.TokensUsed = checkpoint.TotalTokens
		projection.StatusReason = checkpoint.LastError
	}
	switch run.Status {
	case domain.RunCreated, domain.RunPreparing:
		projection.Status = domain.AgentReady
	case domain.RunRunning:
		projection.Status = domain.AgentReady
		if found && checkpoint.Phase == domain.SupervisorTurnStarted {
			projection.Status = domain.AgentRunning
			projection.ActiveAttemptID = checkpoint.AttemptID
			projection.StatusReason = ""
		}
	case domain.RunWaitingApproval:
		projection.Status = domain.AgentWaiting
		projection.StatusReason = "waiting for approval"
	case domain.RunPaused:
		projection.Status = domain.AgentWaiting
		if projection.StatusReason == "" {
			projection.StatusReason = "run paused"
		}
	case domain.RunCompleted:
		projection.Status = domain.AgentCompleted
		projection.StatusReason = "run completed"
	case domain.RunFailed:
		projection.Status = domain.AgentFailed
		if projection.StatusReason == "" {
			projection.StatusReason = "run failed"
		}
	case domain.RunCancelled:
		projection.Status = domain.AgentCancelled
		projection.StatusReason = "run cancelled"
	default:
		return rootAgentProjection{}, fmt.Errorf("unsupported run status %q for root agent", run.Status)
	}
	return projection, nil
}

func syncRootAgentTx(ctx context.Context, tx *sql.Tx, run domain.Run, mission domain.Mission,
	projection rootAgentProjection, at time.Time,
) (domain.AgentNode, bool, bool, error) {
	current, found, err := getRootAgentTx(ctx, tx, run.ID)
	if err != nil {
		return domain.AgentNode{}, false, false, err
	}
	if !found {
		node, err := insertRootAgentTx(ctx, tx, run, mission, projection, at)
		if err != nil {
			return domain.AgentNode{}, false, false, err
		}
		if _, err := syncSpecialistLifecycleTx(ctx, tx, run, projection.Status, at); err != nil {
			return domain.AgentNode{}, false, false, err
		}
		return node, true, true, nil
	}
	if current.SessionID != run.SessionID || current.Profile != mission.Profile {
		return domain.AgentNode{}, false, false, apperror.New(apperror.CodeConflict,
			"root agent identity no longer matches its run")
	}
	projection.StatusReason = normalizeAgentStatusReason(projection.StatusReason)
	projection.ActiveAttemptID = strings.TrimSpace(projection.ActiveAttemptID)
	if projection.TurnsUsed < current.TurnsUsed || projection.TokensUsed < current.TokensUsed {
		return domain.AgentNode{}, false, false, apperror.New(apperror.CodeConflict,
			"root agent usage projection cannot move backwards")
	}
	if !current.CanTransition(projection.Status) {
		return domain.AgentNode{}, false, false, apperror.New(apperror.CodeConflict,
			fmt.Sprintf("root agent cannot transition from %s to %s", current.Status, projection.Status))
	}
	if current.Status == domain.AgentRunning && projection.Status == domain.AgentRunning &&
		current.ActiveAttemptID != projection.ActiveAttemptID {
		return domain.AgentNode{}, false, false, apperror.New(apperror.CodeConflict,
			"root agent is bound to another active attempt")
	}
	if current.Status == domain.AgentRunning && projection.Status != domain.AgentRunning {
		if _, err := supersedeRootInboxDeliveriesTx(ctx, tx, run, current.ID, "", at.UTC()); err != nil {
			return domain.AgentNode{}, false, false, err
		}
	}
	updated := current
	if !slices.Contains(updated.Skills, domain.AgentSkillSpecialistDelegation) {
		updated.Skills, err = domain.NormalizeAgentSkills(append(
			append([]string(nil), updated.Skills...), domain.AgentSkillSpecialistDelegation))
		if err != nil {
			return domain.AgentNode{}, false, false, err
		}
	}
	updated.Status = projection.Status
	updated.ActiveAttemptID = projection.ActiveAttemptID
	updated.StatusReason = projection.StatusReason
	effectiveBudget, err := effectiveRootBudgetTx(ctx, tx, run, current.ID)
	if err != nil {
		return domain.AgentNode{}, false, false, err
	}
	updated.TurnLimit = int64(effectiveBudget.MaxTurns)
	updated.TokenLimit = effectiveBudget.MaxTokens
	updated.TurnsUsed = projection.TurnsUsed
	updated.TokensUsed = projection.TokensUsed
	if updated.Status != domain.AgentRunning {
		updated.ActiveAttemptID = ""
	}
	if updated.Status == domain.AgentRunning {
		updated.StatusReason = ""
	}
	if updated.Terminal() {
		if current.FinishedAt == nil {
			finished := at.UTC()
			updated.FinishedAt = &finished
		}
	} else {
		updated.FinishedAt = nil
	}
	changed := updated.Status != current.Status || updated.ActiveAttemptID != current.ActiveAttemptID ||
		updated.StatusReason != current.StatusReason || updated.TurnLimit != current.TurnLimit ||
		updated.TokenLimit != current.TokenLimit || updated.TurnsUsed != current.TurnsUsed ||
		updated.TokensUsed != current.TokensUsed || !slices.Equal(updated.Skills, current.Skills) ||
		!sameTimePointer(updated.FinishedAt, current.FinishedAt)
	if !changed {
		specialistChanged, err := syncSpecialistLifecycleTx(ctx, tx, run, projection.Status, at)
		return current, false, specialistChanged, err
	}
	updated.Version = current.Version + 1
	updated.UpdatedAt = at.UTC()
	if err := updated.Validate(); err != nil {
		return domain.AgentNode{}, false, false, err
	}
	skillsJSON, err := marshalRedactedJSON(updated.Skills)
	if err != nil {
		return domain.AgentNode{}, false, false, err
	}
	result, err := tx.ExecContext(ctx, `UPDATE agent_nodes SET skills_json = ?, status = ?, child_limit = ?,
		turn_limit = ?, token_limit = ?, turns_used = ?, tokens_used = ?, active_attempt_id = ?,
		status_reason = ?, version = ?, updated_at = ?, finished_at = ? WHERE id = ? AND version = ?`,
		skillsJSON, updated.Status, updated.ChildLimit, updated.TurnLimit, updated.TokenLimit,
		updated.TurnsUsed, updated.TokensUsed, updated.ActiveAttemptID, updated.StatusReason, updated.Version,
		ts(updated.UpdatedAt), nullableTS(updated.FinishedAt), updated.ID, current.Version)
	if err != nil {
		return domain.AgentNode{}, false, false, err
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return domain.AgentNode{}, false, false, err
	}
	if rows != 1 {
		return domain.AgentNode{}, false, false, apperror.New(apperror.CodeConflict,
			"root agent changed concurrently")
	}
	if err := appendSupervisorEventTx(ctx, tx, run, events.AgentStatusChangedEvent, "agent_coordinator", updated.ID,
		map[string]any{
			"from": current.Status, "to": updated.Status, "active_attempt_id": updated.ActiveAttemptID,
			"turns_used": updated.TurnsUsed, "tokens_used": updated.TokensUsed, "version": updated.Version,
		}); err != nil {
		return domain.AgentNode{}, false, false, err
	}
	if _, err := syncSpecialistLifecycleTx(ctx, tx, run, projection.Status, at); err != nil {
		return domain.AgentNode{}, false, false, err
	}
	return updated, false, true, nil
}

func insertRootAgentTx(ctx context.Context, tx *sql.Tx, run domain.Run, mission domain.Mission,
	projection rootAgentProjection, at time.Time,
) (domain.AgentNode, error) {
	skills, err := domain.NormalizeAgentSkills([]string{
		"model.chat", "note_create", domain.AgentSkillSpecialistDelegation, "work_item_create",
		"profile." + string(mission.Profile),
	})
	if err != nil {
		return domain.AgentNode{}, err
	}
	now := at.UTC()
	node := domain.AgentNode{
		ID: idgen.New("agent"), RunID: run.ID, SessionID: run.SessionID, Role: domain.AgentRoleRoot,
		Profile: mission.Profile, Skills: skills, Status: projection.Status, Depth: 0, ChildLimit: 0,
		TurnLimit: int64(run.Budget.MaxTurns), TokenLimit: run.Budget.MaxTokens,
		TurnsUsed: projection.TurnsUsed, TokensUsed: projection.TokensUsed,
		ActiveAttemptID: strings.TrimSpace(projection.ActiveAttemptID),
		StatusReason:    normalizeAgentStatusReason(projection.StatusReason), Version: 1,
		CreatedAt: now, UpdatedAt: now,
	}
	if node.Status != domain.AgentRunning {
		node.ActiveAttemptID = ""
	}
	if node.Status == domain.AgentRunning {
		node.StatusReason = ""
	}
	if node.Terminal() {
		finished := now
		node.FinishedAt = &finished
	}
	if err := node.Validate(); err != nil {
		return domain.AgentNode{}, err
	}
	skillsJSON, err := marshalRedactedJSON(node.Skills)
	if err != nil {
		return domain.AgentNode{}, err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO agent_nodes
		(id, run_id, parent_id, session_id, role, profile, skills_json, status, depth, child_limit,
		turn_limit, token_limit, turns_used, tokens_used, active_attempt_id, status_reason, version,
		created_at, updated_at, finished_at)
		VALUES (?, ?, NULL, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		node.ID, node.RunID, node.SessionID, node.Role, node.Profile, skillsJSON, node.Status,
		node.Depth, node.ChildLimit, node.TurnLimit, node.TokenLimit, node.TurnsUsed, node.TokensUsed,
		node.ActiveAttemptID, node.StatusReason, node.Version, ts(node.CreatedAt), ts(node.UpdatedAt),
		nullableTS(node.FinishedAt)); err != nil {
		return domain.AgentNode{}, err
	}
	if err := appendSupervisorEventTx(ctx, tx, run, events.AgentRegisteredEvent, "agent_coordinator", node.ID,
		map[string]any{
			"role": node.Role, "profile": node.Profile, "status": node.Status, "depth": node.Depth,
			"child_limit": node.ChildLimit, "turn_limit": node.TurnLimit, "token_limit": node.TokenLimit,
			"skills": node.Skills, "version": node.Version,
		}); err != nil {
		return domain.AgentNode{}, err
	}
	return node, nil
}

func getRootAgentTx(ctx context.Context, tx *sql.Tx, runID string) (domain.AgentNode, bool, error) {
	node, err := scanAgentNode(tx.QueryRowContext(ctx, agentLifecycleSelect+` WHERE run_id = ? AND parent_id IS NULL`, runID))
	if errors.Is(err, sql.ErrNoRows) {
		return domain.AgentNode{}, false, nil
	}
	if err != nil {
		return domain.AgentNode{}, false, err
	}
	return node, true, nil
}

func scanAgentNode(row scanner) (domain.AgentNode, error) {
	var node domain.AgentNode
	var pathsJSON string
	var parentID, finished sql.NullString
	var role, profile, status, skillsJSON, createdAt, updatedAt string
	if err := row.Scan(&node.ID, &node.RunID, &parentID, &node.SessionID, &role, &profile, &skillsJSON,
		&status, &node.Depth, &node.ChildLimit, &node.TurnLimit, &node.TokenLimit, &node.TurnsUsed,
		&node.TokensUsed, &node.ActiveAttemptID, &node.StatusReason, &node.Version, &createdAt,
		&updatedAt, &finished, &pathsJSON, &node.ToolCallLimit, &node.CostLimitMicros, &node.ToolCallsUsed, &node.CostUsedMicros); err != nil {
		return domain.AgentNode{}, err
	}
	if err := json.Unmarshal([]byte(pathsJSON), &node.AllowedReadPaths); err != nil {
		return domain.AgentNode{}, err
	}
	node.ParentID = parentID.String
	node.Role = domain.AgentRole(role)
	node.Profile = domain.Profile(profile)
	node.Status = domain.AgentStatus(status)
	if err := json.Unmarshal([]byte(skillsJSON), &node.Skills); err != nil {
		return domain.AgentNode{}, err
	}
	node.CreatedAt = parseTS(createdAt)
	node.UpdatedAt = parseTS(updatedAt)
	node.FinishedAt = parseNullableTS(finished)
	return node, node.Validate()
}

func listAgentNodes(ctx context.Context, queryer interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}, runID string) ([]domain.AgentNode, error) {
	return listAgentNodesWithSelect(ctx, queryer, runID, agentNodeSelect)
}

func listAgentNodesWithSelect(ctx context.Context, queryer interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}, runID, selectSQL string) ([]domain.AgentNode, error) {
	rows, err := queryer.QueryContext(ctx, selectSQL+` WHERE run_id = ? ORDER BY depth, created_at, id`, runID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	nodes := make([]domain.AgentNode, 0, domain.MaxAgentNodesPerRun)
	for rows.Next() {
		node, err := scanAgentNode(rows)
		if err != nil {
			return nil, err
		}
		nodes = append(nodes, node)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(nodes) > domain.MaxAgentNodesPerRun {
		return nil, apperror.New(apperror.CodeResourceExhausted, "agent graph node limit was exceeded")
	}
	return nodes, nil
}

func scanAgentMessage(row scanner) (domain.AgentMessage, error) {
	var message domain.AgentMessage
	var senderID, consumedAt sql.NullString
	var kind, semantic, status, createdAt string
	if err := row.Scan(&message.ID, &message.RunID, &senderID, &message.RecipientAgentID,
		&message.Sequence, &kind, &semantic, &message.PayloadJSON, &status, &createdAt, &consumedAt); err != nil {
		return domain.AgentMessage{}, err
	}
	message.SenderAgentID = senderID.String
	message.Kind = domain.AgentMessageKind(kind)
	message.Semantic = domain.AgentMessageSemantic(semantic)
	message.Status = domain.AgentMessageStatus(status)
	message.CreatedAt = parseTS(createdAt)
	message.ConsumedAt = parseNullableTS(consumedAt)
	return message, message.Validate()
}

func getAgentMessageOperationTx(ctx context.Context, tx *sql.Tx,
	keyDigest string,
) (string, string, bool, error) {
	var fingerprint, messageID string
	err := tx.QueryRowContext(ctx, `SELECT request_fingerprint, message_id
		FROM agent_message_operations WHERE operation_key_digest = ?`, keyDigest).
		Scan(&fingerprint, &messageID)
	if errors.Is(err, sql.ErrNoRows) {
		return "", "", false, nil
	}
	if err != nil {
		return "", "", false, err
	}
	return fingerprint, messageID, true, nil
}

func scanAgentMessages(rows *sql.Rows) ([]domain.AgentMessage, error) {
	messages := make([]domain.AgentMessage, 0)
	for rows.Next() {
		message, err := scanAgentMessage(rows)
		if err != nil {
			return nil, err
		}
		messages = append(messages, message)
	}
	return messages, rows.Err()
}

func normalizeAgentStatusReason(reason string) string {
	reason = redact.String(strings.TrimSpace(reason))
	runes := []rune(reason)
	if len(runes) > domain.MaxAgentStatusReasonRunes {
		reason = string(runes[:domain.MaxAgentStatusReasonRunes])
	}
	return reason
}

func nullableAgentID(id string) any {
	if strings.TrimSpace(id) == "" {
		return nil
	}
	return strings.TrimSpace(id)
}

func sameTimePointer(left, right *time.Time) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return left.Equal(*right)
}

func validateAgentMessageJSON(value any, depth int) error {
	if depth > 32 {
		return errors.New("agent message payload exceeds depth 32")
	}
	switch current := value.(type) {
	case map[string]any:
		for key, item := range current {
			if key == "" || len(key) > 64 || redact.String(key) != key {
				return errors.New("agent message field name is empty, too long, or sensitive")
			}
			for _, char := range key {
				if (char < 'a' || char > 'z') && (char < 'A' || char > 'Z') &&
					(char < '0' || char > '9') && char != '_' && char != '-' && char != '.' {
					return errors.New("agent message field name contains an unsupported character")
				}
			}
			if err := validateAgentMessageJSON(item, depth+1); err != nil {
				return err
			}
		}
	case []any:
		for _, item := range current {
			if err := validateAgentMessageJSON(item, depth+1); err != nil {
				return err
			}
		}
	}
	return nil
}
