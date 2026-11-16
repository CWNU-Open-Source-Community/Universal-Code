package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"cyberagent-workbench/internal/contextmgr"
	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/events"
	"cyberagent-workbench/internal/session"
)

const continuityNodeSelect = `SELECT id, protocol_version, kind, session_id, run_id,
	workspace_id, coalesce(parent_id, ''), coalesce(source_node_id, ''), title, summary,
	snapshot_json, context_sha256, project_config_fingerprint,
	project_instructions_fingerprint, git_branch, git_head, created_by, created_at
	FROM session_continuity_nodes`

func (s *SQLiteStore) CreateSessionContinuityNode(ctx context.Context,
	node contextmgr.ContinuityNode,
) error {
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{})
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if err := insertSessionContinuityNodeTx(ctx, tx, node); err != nil {
		return err
	}
	mission, err := getRunMissionBindingTx(ctx, tx, node.RunID)
	if err != nil {
		return err
	}
	event, err := events.New(node.RunID, mission.ID, events.SessionContinuityNodeCreatedEvent,
		node.CreatedBy, node.ID, map[string]any{
			"node_id": node.ID, "kind": node.Kind, "session_id": node.SessionID,
			"parent_id": node.ParentID, "source_node_id": node.SourceNodeID,
			"context_sha256": node.ContextSHA256,
		})
	if err != nil {
		return err
	}
	if _, err := insertRunEventTx(ctx, tx, event); err != nil {
		return err
	}
	return tx.Commit()
}

func insertSessionContinuityNodeTx(ctx context.Context, tx *sql.Tx,
	node contextmgr.ContinuityNode,
) error {
	if err := node.Validate(); err != nil {
		return fmt.Errorf("session continuity node: %w", err)
	}
	raw, err := json.Marshal(node.Snapshot)
	if err != nil {
		return err
	}
	if len(raw) > maxStoreJSONPayloadBytes {
		return errors.New("session continuity snapshot is too large")
	}
	var parent, source any
	if node.ParentID != "" {
		parent = node.ParentID
	}
	if node.SourceNodeID != "" {
		source = node.SourceNodeID
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO session_continuity_nodes
		(id, protocol_version, kind, session_id, run_id, workspace_id, parent_id,
		source_node_id, title, summary, snapshot_json, context_sha256,
		project_config_fingerprint, project_instructions_fingerprint, git_branch,
		git_head, created_by, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		node.ID, node.ProtocolVersion, node.Kind, node.SessionID, node.RunID,
		node.WorkspaceID, parent, source, node.Title, node.Summary, string(raw),
		node.ContextSHA256, node.ProjectConfigFingerprint,
		node.ProjectInstructionsFingerprint, node.GitBranch, node.GitHead,
		node.CreatedBy, ts(node.CreatedAt))
	return err
}

func (s *SQLiteStore) GetSessionContinuityNode(ctx context.Context,
	id string,
) (contextmgr.ContinuityNode, error) {
	id = strings.TrimSpace(id)
	if id == "" {
		return contextmgr.ContinuityNode{}, errors.New("session continuity node id is required")
	}
	return scanSessionContinuityNode(s.db.QueryRowContext(ctx,
		continuityNodeSelect+` WHERE id = ?`, id))
}

func (s *SQLiteStore) ListSessionContinuityNodes(ctx context.Context,
	sessionID string, limit int,
) ([]contextmgr.ContinuityNode, error) {
	return s.listSessionContinuityNodes(ctx, "session_id", sessionID, limit)
}

func (s *SQLiteStore) ListWorkspaceContinuityNodes(ctx context.Context,
	workspaceID string, limit int,
) ([]contextmgr.ContinuityNode, error) {
	return s.listSessionContinuityNodes(ctx, "workspace_id", workspaceID, limit)
}

func (s *SQLiteStore) listSessionContinuityNodes(ctx context.Context, column,
	identity string, limit int,
) ([]contextmgr.ContinuityNode, error) {
	identity = strings.TrimSpace(identity)
	if identity == "" || (column != "session_id" && column != "workspace_id") {
		return nil, errors.New("session continuity list identity is required")
	}
	if limit == 0 {
		limit = 500
	}
	if limit < 1 || limit > 2000 {
		return nil, errors.New("session continuity list limit must be between 1 and 2000")
	}
	rows, err := s.db.QueryContext(ctx, continuityNodeSelect+` WHERE `+column+
		` = ? ORDER BY created_at, id LIMIT ?`, identity, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	values := make([]contextmgr.ContinuityNode, 0)
	for rows.Next() {
		node, err := scanSessionContinuityNode(rows)
		if err != nil {
			return nil, err
		}
		values = append(values, node)
	}
	return values, rows.Err()
}

// CreateMissionRunWithContinuity keeps Run creation and the selected
// Fork/Resume source binding in one transaction. createMissionRunTx also
// creates a local root node; the branch marker remains separately visible.
func (s *SQLiteStore) CreateMissionRunWithContinuity(ctx context.Context,
	mission domain.Mission, run domain.Run, mode domain.RunModeSnapshot,
	linkedSession session.Session, createSession bool, initialEvents []events.Event,
	node contextmgr.ContinuityNode,
) error {
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{})
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if err := createMissionRunWithContinuityTx(ctx, tx, mission, run, mode,
		linkedSession, createSession, initialEvents, node); err != nil {
		return err
	}
	return tx.Commit()
}

// CreateWorkspaceMissionRunWithContinuity atomically registers an independent
// Workspace and its authority-reset fork Run. Filesystem worktree creation is
// verified before this transaction begins.
func (s *SQLiteStore) CreateWorkspaceMissionRunWithContinuity(ctx context.Context,
	workspace session.WorkspaceRecord, mission domain.Mission, run domain.Run,
	mode domain.RunModeSnapshot, linkedSession session.Session, createSession bool,
	initialEvents []events.Event, node contextmgr.ContinuityNode,
) error {
	workspace.ID, workspace.Name = strings.TrimSpace(workspace.ID), strings.TrimSpace(workspace.Name)
	if workspace.ID == "" || workspace.Name == "" || workspace.RootPath == "" ||
		strings.ContainsRune(workspace.ID+workspace.Name+workspace.RootPath, 0) {
		return errors.New("fork Workspace registration is invalid")
	}
	if workspace.CreatedAt.IsZero() {
		workspace.CreatedAt = time.Now().UTC()
	}
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{})
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx, `INSERT INTO workspaces
		(id, name, root_path, created_at) VALUES (?, ?, ?, ?)`, workspace.ID,
		workspace.Name, workspace.RootPath, ts(workspace.CreatedAt)); err != nil {
		return err
	}
	if err := createMissionRunWithContinuityTx(ctx, tx, mission, run, mode,
		linkedSession, createSession, initialEvents, node); err != nil {
		return err
	}
	return tx.Commit()
}

func createMissionRunWithContinuityTx(ctx context.Context, tx *sql.Tx,
	mission domain.Mission, run domain.Run, mode domain.RunModeSnapshot,
	linkedSession session.Session, createSession bool, initialEvents []events.Event,
	node contextmgr.ContinuityNode,
) error {
	if err := createMissionRunTx(ctx, tx, mission, run, mode, linkedSession,
		createSession, initialEvents); err != nil {
		return err
	}
	if node.RunID != run.ID || node.SessionID != run.SessionID ||
		node.WorkspaceID != mission.WorkspaceID ||
		(node.Kind != contextmgr.ContinuityNodeFork && node.Kind != contextmgr.ContinuityNodeResume) {
		return errors.New("continuity branch node does not match the new Run")
	}
	if err := insertSessionContinuityNodeTx(ctx, tx, node); err != nil {
		return err
	}
	event, err := events.New(run.ID, mission.ID, events.SessionContinuityNodeCreatedEvent,
		node.CreatedBy, node.ID, map[string]any{
			"node_id": node.ID, "kind": node.Kind, "session_id": node.SessionID,
			"source_node_id": node.SourceNodeID, "context_sha256": node.ContextSHA256,
		})
	if err != nil {
		return err
	}
	if _, err := insertRunEventTx(ctx, tx, event); err != nil {
		return err
	}
	return nil
}

type continuityScanner interface {
	Scan(...any) error
}

func scanSessionContinuityNode(scanner continuityScanner) (contextmgr.ContinuityNode, error) {
	var node contextmgr.ContinuityNode
	var snapshotRaw, created string
	if err := scanner.Scan(&node.ID, &node.ProtocolVersion, &node.Kind, &node.SessionID,
		&node.RunID, &node.WorkspaceID, &node.ParentID, &node.SourceNodeID,
		&node.Title, &node.Summary, &snapshotRaw, &node.ContextSHA256,
		&node.ProjectConfigFingerprint, &node.ProjectInstructionsFingerprint,
		&node.GitBranch, &node.GitHead, &node.CreatedBy, &created); err != nil {
		return contextmgr.ContinuityNode{}, err
	}
	if err := json.Unmarshal([]byte(snapshotRaw), &node.Snapshot); err != nil {
		return contextmgr.ContinuityNode{}, err
	}
	node.CreatedAt = parseTS(created)
	if err := node.Validate(); err != nil {
		return contextmgr.ContinuityNode{}, fmt.Errorf("stored session continuity node: %w", err)
	}
	return node, nil
}

func getRunMissionBindingTx(ctx context.Context, tx *sql.Tx,
	runID string,
) (domain.Mission, error) {
	row := tx.QueryRowContext(ctx, `SELECT m.id, m.goal, m.profile, m.workspace_id,
		m.scope_json, m.created_at, m.updated_at FROM missions m
		JOIN runs r ON r.mission_id = m.id WHERE r.id = ?`, runID)
	var mission domain.Mission
	var scopeRaw, created, updated string
	if err := row.Scan(&mission.ID, &mission.Goal, &mission.Profile, &mission.WorkspaceID,
		&scopeRaw, &created, &updated); err != nil {
		return domain.Mission{}, err
	}
	if err := json.Unmarshal([]byte(scopeRaw), &mission.Scope); err != nil {
		return domain.Mission{}, err
	}
	mission.CreatedAt, mission.UpdatedAt = parseTS(created), parseTS(updated)
	return mission, mission.Validate()
}
