package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"cyberagent-workbench/internal/apperror"
	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/events"
	"cyberagent-workbench/internal/idgen"
)

const agentGraphSnapshotSelect = `SELECT id, run_id, version, protocol_version, root_agent_id,
	node_count, pending_message_count, state_json, created_at FROM agent_graph_snapshots`

type agentGraphNodeSnapshot struct {
	ID              string             `json:"id"`
	ParentID        string             `json:"parent_id,omitempty"`
	SessionID       string             `json:"session_id"`
	Role            domain.AgentRole   `json:"role"`
	Profile         domain.Profile     `json:"profile"`
	Skills          []string           `json:"skills"`
	Status          domain.AgentStatus `json:"status"`
	Depth           int                `json:"depth"`
	ChildLimit      int                `json:"child_limit"`
	TurnLimit       int64              `json:"turn_limit"`
	TokenLimit      int64              `json:"token_limit"`
	TurnsUsed       int64              `json:"turns_used"`
	TokensUsed      int64              `json:"tokens_used"`
	ActiveAttemptID string             `json:"active_attempt_id,omitempty"`
	StatusReason    string             `json:"status_reason,omitempty"`
	Version         int64              `json:"version"`
	CreatedAt       string             `json:"created_at"`
	UpdatedAt       string             `json:"updated_at"`
	FinishedAt      string             `json:"finished_at,omitempty"`
}

type agentGraphMessageSnapshot struct {
	ID               string                      `json:"id"`
	SenderAgentID    string                      `json:"sender_agent_id,omitempty"`
	RecipientAgentID string                      `json:"recipient_agent_id"`
	Sequence         int64                       `json:"sequence"`
	Kind             domain.AgentMessageKind     `json:"kind"`
	Semantic         domain.AgentMessageSemantic `json:"semantic,omitempty"`
	PayloadSHA256    string                      `json:"payload_sha256"`
	CreatedAt        string                      `json:"created_at"`
}

type agentGraphDeliverySnapshot struct {
	RootAgentID         string `json:"root_agent_id"`
	SupervisorAttemptID string `json:"supervisor_attempt_id"`
	Turn                int    `json:"turn"`
	MessageID           string `json:"message_id"`
	Ordinal             int    `json:"ordinal"`
}

type agentGraphSpecialistDeliverySnapshot struct {
	AgentID        string `json:"agent_id"`
	ParentAgentID  string `json:"parent_agent_id"`
	AgentAttemptID string `json:"agent_attempt_id"`
	Turn           int64  `json:"turn"`
	MessageID      string `json:"message_id"`
	Ordinal        int    `json:"ordinal"`
}

type agentGraphSnapshotState struct {
	ProtocolVersion           string                                 `json:"protocol_version"`
	RootAgentID               string                                 `json:"root_agent_id"`
	Nodes                     []agentGraphNodeSnapshot               `json:"nodes"`
	PendingMessages           []agentGraphMessageSnapshot            `json:"pending_messages"`
	PreparedInbox             []agentGraphDeliverySnapshot           `json:"prepared_inbox,omitempty"`
	PreparedSpecialistContext []agentGraphSpecialistDeliverySnapshot `json:"prepared_specialist_context,omitempty"`
}

func (s *SQLiteStore) SnapshotAgentGraph(ctx context.Context, runID string) (domain.AgentGraphSnapshot, error) {
	runID = strings.TrimSpace(runID)
	if runID == "" {
		return domain.AgentGraphSnapshot{}, apperror.New(apperror.CodeInvalidArgument, "agent graph run id is required")
	}
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{})
	if err != nil {
		return domain.AgentGraphSnapshot{}, err
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx, `UPDATE runs SET updated_at = updated_at WHERE id = ?`, runID); err != nil {
		return domain.AgentGraphSnapshot{}, err
	}
	run, err := scanRun(tx.QueryRowContext(ctx, `SELECT id, mission_id, session_id, status, config_json, budget_json,
		started_at, finished_at, created_at, updated_at FROM runs WHERE id = ?`, runID))
	if err != nil {
		return domain.AgentGraphSnapshot{}, err
	}
	snapshot, err := createAgentGraphSnapshotTx(ctx, tx, run)
	if err != nil {
		return domain.AgentGraphSnapshot{}, err
	}
	if err := appendSupervisorEventTx(ctx, tx, run, events.AgentGraphSnapshottedEvent,
		"agent_coordinator", snapshot.ID, map[string]any{
			"version": snapshot.Version, "protocol_version": snapshot.ProtocolVersion,
			"root_agent_id": snapshot.RootAgentID, "node_count": snapshot.NodeCount,
			"pending_message_count": snapshot.PendingMessageCount,
		}); err != nil {
		return domain.AgentGraphSnapshot{}, err
	}
	if err := tx.Commit(); err != nil {
		return domain.AgentGraphSnapshot{}, err
	}
	return snapshot, nil
}

func (s *SQLiteStore) GetLatestAgentGraphSnapshot(ctx context.Context,
	runID string,
) (domain.AgentGraphSnapshot, bool, error) {
	snapshot, err := scanAgentGraphSnapshot(s.db.QueryRowContext(ctx,
		agentGraphSnapshotSelect+` WHERE run_id = ? ORDER BY version DESC LIMIT 1`, strings.TrimSpace(runID)))
	if errors.Is(err, sql.ErrNoRows) {
		return domain.AgentGraphSnapshot{}, false, nil
	}
	if err != nil {
		return domain.AgentGraphSnapshot{}, false, err
	}
	return snapshot, true, nil
}

func (s *SQLiteStore) RestoreAgentGraph(ctx context.Context, runID string) (domain.AgentGraph, error) {
	runID = strings.TrimSpace(runID)
	if runID == "" {
		return domain.AgentGraph{}, apperror.New(apperror.CodeInvalidArgument, "agent graph run id is required")
	}
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return domain.AgentGraph{}, err
	}
	defer func() { _ = tx.Rollback() }()
	run, err := scanRun(tx.QueryRowContext(ctx, `SELECT id, mission_id, session_id, status, config_json, budget_json,
		started_at, finished_at, created_at, updated_at FROM runs WHERE id = ?`, runID))
	if err != nil {
		return domain.AgentGraph{}, err
	}
	nodes, err := listAgentNodes(ctx, tx, runID)
	if err != nil {
		return domain.AgentGraph{}, err
	}
	if len(nodes) == 0 {
		return domain.AgentGraph{}, apperror.New(apperror.CodeFailedPrecondition,
			"run has no registered root agent")
	}
	rootID := ""
	for _, node := range nodes {
		if node.Role == domain.AgentRoleRoot {
			if rootID != "" {
				return domain.AgentGraph{}, apperror.New(apperror.CodeFailedPrecondition,
					"agent graph contains multiple roots")
			}
			rootID = node.ID
		}
	}
	if rootID == "" {
		return domain.AgentGraph{}, apperror.New(apperror.CodeFailedPrecondition, "agent graph root is missing")
	}
	if err := validateAgentAttemptProjectionTx(ctx, tx, nodes); err != nil {
		return domain.AgentGraph{}, err
	}
	if err := validateRootInboxDeliveryProjectionTx(ctx, tx, nodes); err != nil {
		return domain.AgentGraph{}, err
	}
	if err := validateSpecialistContextDeliveryProjectionTx(ctx, tx, nodes); err != nil {
		return domain.AgentGraph{}, err
	}
	if err := validateSpecialistBriefProjectionTx(ctx, tx, nodes); err != nil {
		return domain.AgentGraph{}, err
	}
	if err := validateAgentCompletionProjectionTx(ctx, tx, nodes); err != nil {
		return domain.AgentGraph{}, err
	}
	projection, err := rootAgentProjectionForRunTx(ctx, tx, run)
	if err != nil {
		return domain.AgentGraph{}, err
	}
	var root domain.AgentNode
	for _, node := range nodes {
		if node.ID == rootID {
			root = node
			break
		}
	}
	if root.Status != projection.Status || root.ActiveAttemptID != projection.ActiveAttemptID {
		return domain.AgentGraph{}, apperror.New(apperror.CodeFailedPrecondition,
			"root agent lifecycle does not match its Run and Supervisor checkpoint")
	}
	messages, err := listPendingAgentMessagesTx(ctx, tx, runID)
	if err != nil {
		return domain.AgentGraph{}, err
	}
	rootDeliveries, err := listRootInboxDeliveriesTx(ctx, tx, rootInboxDeliverySelect+
		` WHERE run_id = ? AND status = ?
		ORDER BY root_agent_id, supervisor_attempt_id, ordinal`, runID,
		domain.RootInboxDeliveryPrepared)
	if err != nil {
		return domain.AgentGraph{}, err
	}
	specialistDeliveries, err := listSpecialistContextDeliveriesTx(ctx, tx,
		specialistContextDeliverySelect+` WHERE run_id = ? AND status = ?
		ORDER BY agent_id, agent_attempt_id, ordinal`, runID,
		domain.RootInboxDeliveryPrepared)
	if err != nil {
		return domain.AgentGraph{}, err
	}
	snapshot, found, err := latestAgentGraphSnapshotTx(ctx, tx, runID)
	if err != nil {
		return domain.AgentGraph{}, err
	}
	if !found {
		return domain.AgentGraph{}, apperror.New(apperror.CodeFailedPrecondition,
			"agent graph has no recovery snapshot")
	}
	stateJSON, err := marshalAgentGraphSnapshotState(rootID, nodes, messages, rootDeliveries,
		specialistDeliveries)
	if err != nil {
		return domain.AgentGraph{}, err
	}
	if snapshot.StateJSON != stateJSON || snapshot.NodeCount != len(nodes) ||
		snapshot.PendingMessageCount != len(messages) || snapshot.RootAgentID != rootID {
		return domain.AgentGraph{}, apperror.New(apperror.CodeFailedPrecondition,
			"agent graph projection does not match its latest recovery snapshot")
	}
	graph := domain.AgentGraph{
		RunID: runID, RootAgentID: rootID, Nodes: nodes, PendingMessages: messages, LatestSnapshot: snapshot,
	}
	if err := graph.Validate(); err != nil {
		return domain.AgentGraph{}, apperror.Wrap(apperror.CodeFailedPrecondition, "invalid restored agent graph", err)
	}
	if err := tx.Commit(); err != nil {
		return domain.AgentGraph{}, err
	}
	return graph, nil
}

func createAgentGraphSnapshotTx(ctx context.Context, tx *sql.Tx,
	run domain.Run,
) (domain.AgentGraphSnapshot, error) {
	nodes, err := listAgentNodesWithSelect(ctx, tx, run.ID, agentLifecycleSelect)
	if err != nil {
		return domain.AgentGraphSnapshot{}, err
	}
	if len(nodes) == 0 {
		return domain.AgentGraphSnapshot{}, apperror.New(apperror.CodeFailedPrecondition,
			"cannot snapshot an empty agent graph")
	}
	rootID := ""
	for _, node := range nodes {
		if node.Role == domain.AgentRoleRoot {
			if rootID != "" {
				return domain.AgentGraphSnapshot{}, apperror.New(apperror.CodeFailedPrecondition,
					"agent graph contains multiple roots")
			}
			rootID = node.ID
		}
	}
	if rootID == "" {
		return domain.AgentGraphSnapshot{}, apperror.New(apperror.CodeFailedPrecondition,
			"agent graph root is missing")
	}
	messages, err := listPendingAgentMessagesTx(ctx, tx, run.ID)
	if err != nil {
		return domain.AgentGraphSnapshot{}, err
	}
	rootDeliveries, err := listRootInboxDeliveriesTx(ctx, tx, rootInboxDeliverySelect+
		` WHERE run_id = ? AND status = ?
		ORDER BY root_agent_id, supervisor_attempt_id, ordinal`, run.ID,
		domain.RootInboxDeliveryPrepared)
	if err != nil {
		return domain.AgentGraphSnapshot{}, err
	}
	specialistDeliveries, err := listSpecialistContextDeliveriesTx(ctx, tx,
		specialistContextDeliverySelect+` WHERE run_id = ? AND status = ?
		ORDER BY agent_id, agent_attempt_id, ordinal`, run.ID,
		domain.RootInboxDeliveryPrepared)
	if err != nil {
		return domain.AgentGraphSnapshot{}, err
	}
	stateJSON, err := marshalAgentGraphSnapshotState(rootID, nodes, messages, rootDeliveries,
		specialistDeliveries)
	if err != nil {
		return domain.AgentGraphSnapshot{}, err
	}
	if len([]byte(stateJSON)) > domain.MaxAgentGraphSnapshotBytes {
		return domain.AgentGraphSnapshot{}, apperror.New(apperror.CodeResourceExhausted,
			fmt.Sprintf("agent graph snapshot exceeds %d bytes", domain.MaxAgentGraphSnapshotBytes))
	}
	var nextVersion int64
	if err := tx.QueryRowContext(ctx, `SELECT COALESCE(MAX(version), 0) + 1
		FROM agent_graph_snapshots WHERE run_id = ?`, run.ID).Scan(&nextVersion); err != nil {
		return domain.AgentGraphSnapshot{}, err
	}
	snapshot := domain.AgentGraphSnapshot{
		ID: idgen.New("graph"), RunID: run.ID, Version: nextVersion,
		ProtocolVersion: domain.AgentGraphProtocolVersion, RootAgentID: rootID,
		NodeCount: len(nodes), PendingMessageCount: len(messages), StateJSON: stateJSON,
		CreatedAt: time.Now().UTC(),
	}
	if err := snapshot.Validate(); err != nil {
		return domain.AgentGraphSnapshot{}, err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO agent_graph_snapshots
		(id, run_id, version, protocol_version, root_agent_id, node_count, pending_message_count,
		state_json, created_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`, snapshot.ID, snapshot.RunID,
		snapshot.Version, snapshot.ProtocolVersion, snapshot.RootAgentID, snapshot.NodeCount,
		snapshot.PendingMessageCount, snapshot.StateJSON, ts(snapshot.CreatedAt)); err != nil {
		return domain.AgentGraphSnapshot{}, err
	}
	if snapshot.Version > domain.MaxAgentGraphSnapshots {
		if _, err := tx.ExecContext(ctx, `DELETE FROM agent_graph_snapshots
			WHERE run_id = ? AND version <= ?`, run.ID, snapshot.Version-domain.MaxAgentGraphSnapshots); err != nil {
			return domain.AgentGraphSnapshot{}, err
		}
	}
	return snapshot, nil
}

func latestAgentGraphSnapshotTx(ctx context.Context, tx *sql.Tx,
	runID string,
) (domain.AgentGraphSnapshot, bool, error) {
	snapshot, err := scanAgentGraphSnapshot(tx.QueryRowContext(ctx,
		agentGraphSnapshotSelect+` WHERE run_id = ? ORDER BY version DESC LIMIT 1`, runID))
	if errors.Is(err, sql.ErrNoRows) {
		return domain.AgentGraphSnapshot{}, false, nil
	}
	if err != nil {
		return domain.AgentGraphSnapshot{}, false, err
	}
	return snapshot, true, nil
}

func scanAgentGraphSnapshot(row scanner) (domain.AgentGraphSnapshot, error) {
	var snapshot domain.AgentGraphSnapshot
	var createdAt string
	if err := row.Scan(&snapshot.ID, &snapshot.RunID, &snapshot.Version, &snapshot.ProtocolVersion,
		&snapshot.RootAgentID, &snapshot.NodeCount, &snapshot.PendingMessageCount, &snapshot.StateJSON,
		&createdAt); err != nil {
		return domain.AgentGraphSnapshot{}, err
	}
	snapshot.CreatedAt = parseTS(createdAt)
	return snapshot, snapshot.Validate()
}

func listPendingAgentMessagesTx(ctx context.Context, tx *sql.Tx,
	runID string,
) ([]domain.AgentMessage, error) {
	rows, err := tx.QueryContext(ctx, agentMessageSelect+` WHERE run_id = ? AND status = ?
		ORDER BY recipient_agent_id, sequence`, runID, domain.AgentMessagePending)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	messages, err := scanAgentMessages(rows)
	if err != nil {
		return nil, err
	}
	if len(messages) > domain.MaxAgentInboxMessages*domain.MaxAgentNodesPerRun {
		return nil, apperror.New(apperror.CodeResourceExhausted, "agent graph pending inbox limit was exceeded")
	}
	return messages, nil
}

func marshalAgentGraphSnapshotState(rootID string, nodes []domain.AgentNode,
	messages []domain.AgentMessage, rootDeliveries []domain.RootInboxDelivery,
	specialistDeliveries []domain.SpecialistContextDelivery,
) (string, error) {
	state := agentGraphSnapshotState{
		ProtocolVersion: domain.AgentGraphProtocolVersion,
		RootAgentID:     rootID,
		Nodes:           make([]agentGraphNodeSnapshot, 0, len(nodes)),
		PendingMessages: make([]agentGraphMessageSnapshot, 0, len(messages)),
	}
	for _, node := range nodes {
		finishedAt := ""
		if node.FinishedAt != nil {
			finishedAt = node.FinishedAt.UTC().Format(time.RFC3339Nano)
		}
		state.Nodes = append(state.Nodes, agentGraphNodeSnapshot{
			ID: node.ID, ParentID: node.ParentID, SessionID: node.SessionID, Role: node.Role,
			Profile: node.Profile, Skills: node.Skills, Status: node.Status, Depth: node.Depth,
			ChildLimit: node.ChildLimit, TurnLimit: node.TurnLimit, TokenLimit: node.TokenLimit,
			TurnsUsed: node.TurnsUsed, TokensUsed: node.TokensUsed,
			ActiveAttemptID: node.ActiveAttemptID, StatusReason: node.StatusReason, Version: node.Version,
			CreatedAt: node.CreatedAt.UTC().Format(time.RFC3339Nano),
			UpdatedAt: node.UpdatedAt.UTC().Format(time.RFC3339Nano), FinishedAt: finishedAt,
		})
	}
	for _, message := range messages {
		payloadHash := sha256.Sum256([]byte(message.PayloadJSON))
		semantic := message.Semantic
		if semantic == domain.AgentMessageSemanticMessage {
			semantic = ""
		}
		state.PendingMessages = append(state.PendingMessages, agentGraphMessageSnapshot{
			ID: message.ID, SenderAgentID: message.SenderAgentID,
			RecipientAgentID: message.RecipientAgentID, Sequence: message.Sequence, Kind: message.Kind,
			Semantic:      semantic,
			PayloadSHA256: fmt.Sprintf("%x", payloadHash[:]),
			CreatedAt:     message.CreatedAt.UTC().Format(time.RFC3339Nano),
		})
	}
	for _, delivery := range rootDeliveries {
		state.PreparedInbox = append(state.PreparedInbox, agentGraphDeliverySnapshot{
			RootAgentID:         delivery.RootAgentID,
			SupervisorAttemptID: delivery.SupervisorAttemptID, Turn: delivery.Turn,
			MessageID: delivery.MessageID, Ordinal: delivery.Ordinal,
		})
	}
	for _, delivery := range specialistDeliveries {
		state.PreparedSpecialistContext = append(state.PreparedSpecialistContext,
			agentGraphSpecialistDeliverySnapshot{
				AgentID: delivery.AgentID, ParentAgentID: delivery.ParentAgentID,
				AgentAttemptID: delivery.AgentAttemptID, Turn: delivery.Turn,
				MessageID: delivery.MessageID, Ordinal: delivery.Ordinal,
			})
	}
	return marshalRedactedJSON(state)
}
