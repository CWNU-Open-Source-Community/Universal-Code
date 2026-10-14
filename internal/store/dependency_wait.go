package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"time"

	"cyberagent-workbench/internal/apperror"
	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/events"
	"cyberagent-workbench/internal/idgen"
	"cyberagent-workbench/internal/runmutation"
	"cyberagent-workbench/internal/waitgraph"
)

const dependencyEdgeSelect = `SELECT id, run_id, source_kind, source_id, target_kind, target_id,
	reason, state, failure_policy, generation, deadline, created_at, updated_at, resolved_at
	FROM agent_dependency_edges`

// RecordDependencyWait validates and persists one structured wait edge. The
// same operation key replays idempotently; cycles, reverse runtime→Agent
// waits, unknown endpoints, cross-Mission targets, and polling livelock are
// rejected before anything is written.
func (s *SQLiteStore) RecordDependencyWait(ctx context.Context,
	edge domain.DependencyEdge, operationKey string,
) (domain.DependencyEdge, bool, error) {
	edge = edge.Normalize()
	if err := edge.Validate(); err != nil {
		return domain.DependencyEdge{}, false, apperror.Wrap(apperror.CodeInvalidArgument,
			"dependency wait edge is invalid", err)
	}
	if edge.State != domain.AgentDependencyWait {
		return domain.DependencyEdge{}, false, apperror.New(apperror.CodeInvalidArgument,
			"a new dependency edge must start waiting")
	}
	normalizedKey, err := domain.NormalizeAgentOperationKey(operationKey)
	if err != nil {
		return domain.DependencyEdge{}, false, apperror.Wrap(apperror.CodeInvalidArgument,
			"dependency operation key is invalid", err)
	}
	keyDigest := runmutation.Fingerprint("agent_dependency_edge_operation.v1",
		edge.RunID, normalizedKey)
	requestFingerprint := runmutation.Fingerprint("agent_dependency_edge_request.v1",
		edge.RunID, string(edge.SourceKind), edge.SourceID, string(edge.TargetKind),
		edge.TargetID, edge.Reason, string(edge.FailurePolicy), strconv.FormatInt(edge.Generation, 10),
		edge.Deadline.UTC().Format(time.RFC3339))
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{})
	if err != nil {
		return domain.DependencyEdge{}, false, err
	}
	defer func() { _ = tx.Rollback() }()
	_, status, missionID, err := loadMonetaryRunTx(ctx, tx, edge.RunID)
	if err != nil {
		return domain.DependencyEdge{}, false, err
	}
	if status == domain.RunCancelled || status == domain.RunCompleted || status == domain.RunFailed {
		return domain.DependencyEdge{}, false, apperror.New(apperror.CodeFailedPrecondition,
			"terminal runs cannot record new dependency waits")
	}
	var storedFingerprint, storedEdgeID string
	err = tx.QueryRowContext(ctx, `SELECT request_fingerprint, edge_id
		FROM agent_dependency_edge_operations WHERE operation_key_digest = ?`, keyDigest).
		Scan(&storedFingerprint, &storedEdgeID)
	if err == nil {
		if storedFingerprint != requestFingerprint {
			return domain.DependencyEdge{}, false, apperror.New(apperror.CodeConflict,
				"dependency operation key was already used for different intent")
		}
		existing, scanErr := scanDependencyEdge(tx.QueryRowContext(ctx,
			dependencyEdgeSelect+` WHERE id = ?`, storedEdgeID))
		if scanErr != nil {
			return domain.DependencyEdge{}, false, scanErr
		}
		if err := tx.Commit(); err != nil {
			return domain.DependencyEdge{}, false, err
		}
		return existing, true, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return domain.DependencyEdge{}, false, err
	}
	if edge.SourceKind != waitgraph.KindAgent || edge.TargetKind != waitgraph.KindAgent {
		return domain.DependencyEdge{}, false, apperror.New(apperror.CodePolicyDenied,
			"durable dependency edges currently require Agent endpoints")
	}
	if err := requireRunAgentNodeTx(ctx, tx, edge.RunID, edge.SourceID); err != nil {
		return domain.DependencyEdge{}, false, err
	}
	if err := requireRunAgentNodeTx(ctx, tx, edge.RunID, edge.TargetID); err != nil {
		return domain.DependencyEdge{}, false, err
	}
	var openCount int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM agent_dependency_edges
		WHERE run_id = ? AND state = 'wait'`, edge.RunID).Scan(&openCount); err != nil {
		return domain.DependencyEdge{}, false, err
	}
	if openCount >= domain.MaxDependencyEdgesPerRun {
		return domain.DependencyEdge{}, false, apperror.New(apperror.CodeResourceExhausted,
			"run dependency edge capacity was exhausted")
	}
	dag, err := loadOpenDependencyDAGTx(ctx, tx, edge.RunID)
	if err != nil {
		return domain.DependencyEdge{}, false, err
	}
	if err := dag.ValidateInsert(waitgraph.DurableEdge{
		Source: waitgraph.Node{Kind: edge.SourceKind, ID: edge.SourceID},
		Target: waitgraph.Node{Kind: edge.TargetKind, ID: edge.TargetID},
	}); err != nil {
		return domain.DependencyEdge{}, false, dependencyValidationError(err)
	}
	// Polling livelock: a source that wakes and re-waits beyond the declared
	// bound is diagnosed instead of silently looping.
	var wakeCount int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM agent_dependency_wakes wake
		JOIN agent_dependency_edges edge ON edge.id = wake.edge_id
		WHERE edge.run_id = ? AND edge.source_kind = ? AND edge.source_id = ?`,
		edge.RunID, string(edge.SourceKind), edge.SourceID).Scan(&wakeCount); err != nil {
		return domain.DependencyEdge{}, false, err
	}
	if wakeCount+1 > domain.DependencyPollingLivelockLimit {
		_ = appendSupervisorEventTx(ctx, tx, domain.Run{ID: edge.RunID, MissionID: missionID},
			events.DependencyLivelockDetectedEvent, "agent_dependency", edge.SourceID, map[string]any{
				"source_kind": string(edge.SourceKind), "source_id": edge.SourceID,
				"wake_count": wakeCount + 1,
			})
		if err := tx.Commit(); err != nil {
			return domain.DependencyEdge{}, false, err
		}
		return domain.DependencyEdge{}, false, apperror.New(apperror.CodeLivelock,
			"agent is polling the same dependency instead of progressing")
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO agent_dependency_edges
		(id, run_id, source_kind, source_id, target_kind, target_id, reason, state,
		failure_policy, generation, deadline, created_at, updated_at, resolved_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, NULL)`,
		edge.ID, edge.RunID, string(edge.SourceKind), edge.SourceID, string(edge.TargetKind),
		edge.TargetID, edge.Reason, string(edge.State), string(edge.FailurePolicy),
		edge.Generation, ts(edge.Deadline), ts(edge.CreatedAt), ts(edge.UpdatedAt)); err != nil {
		return domain.DependencyEdge{}, false, err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO agent_dependency_edge_operations
		(operation_key_digest, request_fingerprint, edge_id, created_at) VALUES (?, ?, ?, ?)`,
		keyDigest, requestFingerprint, edge.ID, ts(time.Now().UTC())); err != nil {
		return domain.DependencyEdge{}, false, err
	}
	if err := waitReadyDependencySourcesTx(ctx, tx, edge.RunID); err != nil {
		return domain.DependencyEdge{}, false, err
	}
	_ = appendSupervisorEventTx(ctx, tx, domain.Run{ID: edge.RunID, MissionID: missionID},
		events.DependencyWaitRecordedEvent, "agent_dependency", edge.ID, map[string]any{
			"source_kind": string(edge.SourceKind), "source_id": edge.SourceID,
			"target_kind": string(edge.TargetKind), "target_id": edge.TargetID,
			"reason": edge.Reason, "generation": edge.Generation,
			"failure_policy": string(edge.FailurePolicy),
		})
	if err := tx.Commit(); err != nil {
		return domain.DependencyEdge{}, false, err
	}
	return edge, false, nil
}

// SettleDependencyTarget closes every open wait edge whose target matches
// with the given terminal outcome. Each edge receives at most one wake
// receipt, so replays and recovery can never wake a source twice.
func (s *SQLiteStore) SettleDependencyTarget(ctx context.Context, runID string,
	targetKind waitgraph.Kind, targetID string, outcome domain.AgentDependencyState, reason string,
) ([]domain.DependencyWake, error) {
	runID = strings.TrimSpace(runID)
	targetID = strings.TrimSpace(targetID)
	reason = strings.TrimSpace(reason)
	if !domain.ValidAgentDependencyStateForEdge(outcome) || outcome == domain.AgentDependencyWait {
		return nil, apperror.New(apperror.CodeInvalidArgument, "dependency settle outcome must be terminal")
	}
	if reason == "" || len([]byte(reason)) > 2048 || strings.ContainsRune(reason, 0) {
		return nil, apperror.New(apperror.CodeInvalidArgument, "dependency settle reason is invalid")
	}
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{})
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()
	_, status, missionID, err := loadMonetaryRunTx(ctx, tx, runID)
	if err != nil {
		return nil, err
	}
	_ = status
	rows, err := tx.QueryContext(ctx, dependencyEdgeSelect+` WHERE run_id = ? AND state = 'wait'
		AND target_kind = ? AND target_id = ? ORDER BY created_at`, runID,
		string(targetKind), targetID)
	if err != nil {
		return nil, err
	}
	edges := make([]domain.DependencyEdge, 0, 4)
	for rows.Next() {
		edge, scanErr := scanDependencyEdge(rows)
		if scanErr != nil {
			_ = rows.Close()
			return nil, scanErr
		}
		edges = append(edges, edge)
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	wakes := make([]domain.DependencyWake, 0, len(edges))
	for _, edge := range edges {
		wake, settled, settleErr := settleDependencyEdgeTx(ctx, tx,
			domain.Run{ID: runID, MissionID: missionID}, edge, outcome, reason)
		if settleErr != nil {
			return nil, settleErr
		}
		if settled {
			wakes = append(wakes, wake)
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return wakes, nil
}

// CancelDependencySource cancels every open wait edge originating from the
// given source (parent-cancel fan-down) and wakes each source exactly once.
func (s *SQLiteStore) CancelDependencySource(ctx context.Context, runID string,
	sourceKind waitgraph.Kind, sourceID, reason string,
) ([]domain.DependencyWake, error) {
	runID = strings.TrimSpace(runID)
	sourceID = strings.TrimSpace(sourceID)
	reason = strings.TrimSpace(reason)
	if reason == "" || len([]byte(reason)) > 2048 || strings.ContainsRune(reason, 0) {
		return nil, apperror.New(apperror.CodeInvalidArgument, "dependency cancel reason is invalid")
	}
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{})
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()
	_, status, missionID, err := loadMonetaryRunTx(ctx, tx, runID)
	if err != nil {
		return nil, err
	}
	_ = status
	rows, err := tx.QueryContext(ctx, dependencyEdgeSelect+` WHERE run_id = ? AND state = 'wait'
		AND source_kind = ? AND source_id = ? ORDER BY created_at`, runID,
		string(sourceKind), sourceID)
	if err != nil {
		return nil, err
	}
	edges := make([]domain.DependencyEdge, 0, 4)
	for rows.Next() {
		edge, scanErr := scanDependencyEdge(rows)
		if scanErr != nil {
			_ = rows.Close()
			return nil, scanErr
		}
		edges = append(edges, edge)
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	wakes := make([]domain.DependencyWake, 0, len(edges))
	for _, edge := range edges {
		wake, settled, settleErr := settleDependencyEdgeTx(ctx, tx,
			domain.Run{ID: runID, MissionID: missionID}, edge, domain.AgentDependencyCancelled, reason)
		if settleErr != nil {
			return nil, settleErr
		}
		if settled {
			wakes = append(wakes, wake)
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return wakes, nil
}

// ExpireOverdueDependencyEdges closes open waits whose no-progress deadline
// has passed and reports the deadlock diagnosis through a stable event.
func (s *SQLiteStore) ExpireOverdueDependencyEdges(ctx context.Context, runID string,
	now time.Time,
) ([]domain.DependencyWake, error) {
	runID = strings.TrimSpace(runID)
	now = now.UTC()
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{})
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()
	_, status, missionID, err := loadMonetaryRunTx(ctx, tx, runID)
	if err != nil {
		return nil, err
	}
	_ = status
	rows, err := tx.QueryContext(ctx, dependencyEdgeSelect+` WHERE run_id = ? AND state = 'wait'
		AND deadline <= ? ORDER BY deadline`, runID, ts(now))
	if err != nil {
		return nil, err
	}
	edges := make([]domain.DependencyEdge, 0, 4)
	for rows.Next() {
		edge, scanErr := scanDependencyEdge(rows)
		if scanErr != nil {
			_ = rows.Close()
			return nil, scanErr
		}
		edges = append(edges, edge)
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	if len(edges) > 0 {
		ids := make([]string, 0, len(edges))
		for _, edge := range edges {
			ids = append(ids, edge.ID)
		}
		_ = appendSupervisorEventTx(ctx, tx, domain.Run{ID: runID, MissionID: missionID},
			events.DependencyDeadlockDetectedEvent, "agent_dependency", runID, map[string]any{
				"edge_ids": ids, "edge_count": len(ids),
			})
	}
	wakes := make([]domain.DependencyWake, 0, len(edges))
	for _, edge := range edges {
		wake, settled, settleErr := settleDependencyEdgeTx(ctx, tx,
			domain.Run{ID: runID, MissionID: missionID}, edge, domain.AgentDependencyExpired,
			"dependency deadline passed without progress")
		if settleErr != nil {
			return nil, settleErr
		}
		if settled {
			wakes = append(wakes, wake)
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return wakes, nil
}

// ReconcileDependencyEdges settles open waits whose target already reached a
// terminal state, whose run became terminal, or whose deadline passed. The
// unique wake receipt makes the recovery idempotent across process crashes.
func (s *SQLiteStore) ReconcileDependencyEdges(ctx context.Context, runID string,
) ([]domain.DependencyWake, error) {
	runID = strings.TrimSpace(runID)
	now := time.Now().UTC()
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{})
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()
	_, status, missionID, err := loadMonetaryRunTx(ctx, tx, runID)
	if err != nil {
		return nil, err
	}
	if err := waitReadyDependencySourcesTx(ctx, tx, runID); err != nil {
		return nil, err
	}
	rows, err := tx.QueryContext(ctx, dependencyEdgeSelect+` WHERE run_id = ? AND state = 'wait'
		ORDER BY created_at`, runID)
	if err != nil {
		return nil, err
	}
	edges := make([]domain.DependencyEdge, 0, 8)
	for rows.Next() {
		edge, scanErr := scanDependencyEdge(rows)
		if scanErr != nil {
			_ = rows.Close()
			return nil, scanErr
		}
		edges = append(edges, edge)
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	wakes := make([]domain.DependencyWake, 0, len(edges))
	for _, edge := range edges {
		outcome, reason, settleNow := dependencyReconcileOutcome(ctx, tx, edge, status, now)
		if !settleNow {
			continue
		}
		wake, settled, settleErr := settleDependencyEdgeTx(ctx, tx,
			domain.Run{ID: runID, MissionID: missionID}, edge, outcome, reason)
		if settleErr != nil {
			return nil, settleErr
		}
		if settled {
			wakes = append(wakes, wake)
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return wakes, nil
}

// DetectDependencyStalls projects the stable deadlock/livelock diagnosis
// without mutating any row.
func (s *SQLiteStore) DetectDependencyStalls(ctx context.Context, runID string,
	now time.Time,
) (domain.DependencyStallDiagnosis, error) {
	runID = strings.TrimSpace(runID)
	now = now.UTC()
	diagnosis := domain.DependencyStallDiagnosis{RunID: runID, DetectedAt: now}
	rows, err := s.db.QueryContext(ctx, dependencyEdgeSelect+` WHERE run_id = ? AND state = 'wait'
		AND deadline <= ?`, runID, ts(now))
	if err != nil {
		return diagnosis, err
	}
	for rows.Next() {
		edge, scanErr := scanDependencyEdge(rows)
		if scanErr != nil {
			_ = rows.Close()
			return diagnosis, scanErr
		}
		diagnosis.DeadlockedEdgeIDs = append(diagnosis.DeadlockedEdgeIDs, edge.ID)
	}
	if err := rows.Close(); err != nil {
		return diagnosis, err
	}
	livelockRows, err := s.db.QueryContext(ctx, `SELECT edge.source_id, COUNT(*)
		FROM agent_dependency_wakes wake JOIN agent_dependency_edges edge ON edge.id = wake.edge_id
		WHERE edge.run_id = ? GROUP BY edge.source_kind, edge.source_id
		HAVING COUNT(*) > ?`, runID, domain.DependencyPollingLivelockLimit)
	if err != nil {
		return diagnosis, err
	}
	for livelockRows.Next() {
		var sourceID string
		var count int
		if err := livelockRows.Scan(&sourceID, &count); err != nil {
			_ = livelockRows.Close()
			return diagnosis, err
		}
		diagnosis.LivelockedSourceIDs = append(diagnosis.LivelockedSourceIDs, sourceID)
	}
	if err := livelockRows.Close(); err != nil {
		return diagnosis, err
	}
	return diagnosis, nil
}

func settleDependencyEdgeTx(ctx context.Context, tx *sql.Tx, run domain.Run,
	edge domain.DependencyEdge, outcome domain.AgentDependencyState, reason string,
) (domain.DependencyWake, bool, error) {
	now := time.Now().UTC()
	wake := domain.DependencyWake{
		ID: idgen.New("depwake"), RunID: run.ID, EdgeID: edge.ID,
		Outcome: outcome, Reason: reason, CreatedAt: now,
	}
	if err := wake.Validate(); err != nil {
		return domain.DependencyWake{}, false, apperror.Wrap(apperror.CodeInvalidArgument,
			"dependency wake is invalid", err)
	}
	// The unique wake receipt is the exactly-once anchor: a replay or a
	// concurrent settle returns the stored receipt instead of waking again.
	if _, err := tx.ExecContext(ctx, `INSERT INTO agent_dependency_wakes
		(id, run_id, edge_id, outcome, reason, created_at) VALUES (?, ?, ?, ?, ?, ?)`,
		wake.ID, wake.RunID, wake.EdgeID, string(wake.Outcome), wake.Reason, ts(wake.CreatedAt)); err != nil {
		if isUniqueViolation(err) {
			var storedID, storedOutcome, storedReason, storedCreated string
			if scanErr := tx.QueryRowContext(ctx, `SELECT id, outcome, reason, created_at
				FROM agent_dependency_wakes WHERE edge_id = ?`, edge.ID).
				Scan(&storedID, &storedOutcome, &storedReason, &storedCreated); scanErr != nil {
				return domain.DependencyWake{}, false, scanErr
			}
			existing := domain.DependencyWake{ID: storedID, RunID: run.ID, EdgeID: edge.ID,
				Outcome: domain.AgentDependencyState(storedOutcome), Reason: storedReason,
				CreatedAt: parseTS(storedCreated)}
			return existing, false, nil
		}
		return domain.DependencyWake{}, false, err
	}
	result, err := tx.ExecContext(ctx, `UPDATE agent_dependency_edges SET state = ?,
		resolved_at = ?, updated_at = ? WHERE id = ? AND state = 'wait'`,
		string(outcome), ts(now), ts(now), edge.ID)
	if err != nil {
		return domain.DependencyWake{}, false, err
	}
	if affected, err := result.RowsAffected(); err != nil || affected != 1 {
		return domain.DependencyWake{}, false, errors.New("dependency edge changed concurrently")
	}
	if edge.SourceKind == waitgraph.KindAgent {
		if err := deliverDependencySourceMessagesTx(ctx, tx, run, edge, outcome, reason, now); err != nil {
			return domain.DependencyWake{}, false, err
		}
	}
	if err := appendDependencyTerminalEventTx(ctx, tx, run, edge, outcome, reason); err != nil {
		return domain.DependencyWake{}, false, err
	}
	return wake, true, nil
}

// deliverDependencySourceMessagesTx hands the terminal outcome to the
// waiting source exactly once: a dependency notification lands in its inbox,
// and a waiting Agent source is additionally woken (or failed for the fail
// policy) through the existing wake transition.
func deliverDependencySourceMessagesTx(ctx context.Context, tx *sql.Tx, run domain.Run,
	edge domain.DependencyEdge, outcome domain.AgentDependencyState, reason string, now time.Time,
) error {
	payload, err := json.Marshal(domain.AgentDependencyPayload{
		DependencyID: edge.ID, State: outcome, Reason: reason,
	})
	if err != nil {
		return err
	}
	sender := ""
	if edge.TargetKind == waitgraph.KindAgent {
		sender = edge.TargetID
	}
	notification := &domain.AgentMessage{
		ID: idgen.New("agentmsg"), RunID: run.ID, SenderAgentID: sender,
		RecipientAgentID: edge.SourceID, Kind: domain.AgentMessageNotification,
		Semantic: domain.AgentMessageSemanticDependency, PayloadJSON: string(payload),
		Status: domain.AgentMessagePending, CreatedAt: now,
	}
	if err := insertBoundedAgentMessageTx(ctx, tx, notification); err != nil {
		return err
	}
	var nodeStatus domain.AgentStatus
	var nodeVersion int64
	if err := tx.QueryRowContext(ctx, `SELECT status, version FROM agent_nodes
		WHERE id = ? AND run_id = ?`, edge.SourceID, run.ID).Scan(&nodeStatus, &nodeVersion); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil
		}
		return err
	}
	if nodeStatus != domain.AgentWaiting {
		return nil
	}
	if outcome != domain.AgentDependencySatisfied && edge.FailurePolicy == domain.DependencyPolicyFail {
		_, err := tx.ExecContext(ctx, `UPDATE agent_nodes SET status = ?, status_reason = ?,
			finished_at = ?, version = version + 1, updated_at = ? WHERE id = ? AND run_id = ?
			AND version = ? AND status = ?`, domain.AgentFailed, "dependency_failed", ts(now),
			ts(now), edge.SourceID, run.ID, nodeVersion, domain.AgentWaiting)
		if err != nil {
			return err
		}
		return nil
	}
	blocked, err := specialistDependenciesBlockedTx(ctx, tx, run.ID, edge.SourceID)
	if err != nil || blocked {
		return err
	}
	wakePayload, err := json.Marshal(domain.AgentWakePayload{Reason: reason})
	if err != nil {
		return err
	}
	wakeMessage := &domain.AgentMessage{
		ID: idgen.New("agentmsg"), RunID: run.ID, RecipientAgentID: edge.SourceID,
		Kind: domain.AgentMessageControl, Semantic: domain.AgentMessageSemanticWake,
		PayloadJSON: string(wakePayload), Status: domain.AgentMessagePending, CreatedAt: now,
	}
	if err := insertBoundedAgentMessageTx(ctx, tx, wakeMessage); err != nil {
		return err
	}
	updated := nodeVersion + 1
	if _, err := tx.ExecContext(ctx, `UPDATE agent_nodes SET status = ?, active_attempt_id = '',
		status_reason = ?, version = ?, updated_at = ? WHERE id = ? AND run_id = ?
		AND version = ? AND status = ?`, domain.AgentReady, reason, updated, ts(now),
		edge.SourceID, run.ID, nodeVersion, domain.AgentWaiting); err != nil {
		return err
	}
	return nil
}

// Check both recorded waits and the assignment's declared dependencies: an
// interrupted admission may not have written its dependency edges yet.
const specialistDependenciesBlockedPredicate = `(EXISTS (SELECT 1 FROM agent_dependency_edges edge
	WHERE edge.run_id = agent_nodes.run_id AND edge.source_kind = 'agent' AND edge.source_id = agent_nodes.id
	AND (edge.state = 'wait' OR (edge.state <> 'satisfied' AND edge.failure_policy = 'fail')))
	OR EXISTS (SELECT 1 FROM child_task_assignments source
		JOIN json_each(source.dependency_ordinals_json) dependency
		LEFT JOIN child_task_assignments target ON target.proposal_id = source.proposal_id AND target.ordinal = dependency.value
		WHERE source.admitted_agent_id = agent_nodes.id AND source.surface = 'core'
		AND NOT EXISTS (SELECT 1 FROM agent_dependency_edges edge WHERE edge.run_id = agent_nodes.run_id
			AND edge.source_kind = 'agent' AND edge.source_id = agent_nodes.id
			AND edge.target_kind = 'agent' AND edge.target_id = target.admitted_agent_id
			AND edge.reason = 'child task dependency' AND edge.generation = 1
			AND edge.failure_policy = 'fail' AND edge.state = 'satisfied')))`

func specialistDependenciesBlockedTx(ctx context.Context, tx *sql.Tx, runID, agentID string) (bool, error) {
	var blocked bool
	err := tx.QueryRowContext(ctx, `SELECT `+specialistDependenciesBlockedPredicate+`
		FROM agent_nodes WHERE run_id = ? AND id = ?`, runID, agentID).Scan(&blocked)
	return blocked, err
}

func waitReadyDependencySourcesTx(ctx context.Context, tx *sql.Tx, runID string) error {
	_, err := tx.ExecContext(ctx, `UPDATE agent_nodes SET status = 'waiting',
		status_reason = 'dependency wait', version = version + 1, updated_at = ?
		WHERE run_id = ? AND role = 'specialist' AND status = 'ready'
		AND `+specialistDependenciesBlockedPredicate,
		ts(time.Now().UTC()), runID)
	return err
}

func appendDependencyTerminalEventTx(ctx context.Context, tx *sql.Tx, run domain.Run,
	edge domain.DependencyEdge, outcome domain.AgentDependencyState, reason string,
) error {
	eventType := events.DependencyExpiredEvent
	switch outcome {
	case domain.AgentDependencySatisfied:
		eventType = events.DependencySatisfiedEvent
	case domain.AgentDependencyFailed:
		eventType = events.DependencyFailedEvent
	case domain.AgentDependencyCancelled:
		eventType = events.DependencyCancelledEvent
	case domain.AgentDependencyExpired:
		eventType = events.DependencyExpiredEvent
	}
	return appendSupervisorEventTx(ctx, tx, run, eventType, "agent_dependency", edge.ID, map[string]any{
		"source_kind": string(edge.SourceKind), "source_id": edge.SourceID,
		"target_kind": string(edge.TargetKind), "target_id": edge.TargetID,
		"reason": reason, "generation": edge.Generation,
		"failure_policy": string(edge.FailurePolicy),
	})
}

// dependencyReconcileOutcome decides the terminal outcome for one open edge
// from the durable target state, the run state, and the no-progress deadline.
func dependencyReconcileOutcome(ctx context.Context, tx *sql.Tx, edge domain.DependencyEdge,
	runStatus domain.RunStatus, now time.Time,
) (domain.AgentDependencyState, string, bool) {
	if !edge.Deadline.After(now) {
		return domain.AgentDependencyExpired, "dependency deadline passed without progress", true
	}
	switch runStatus {
	case domain.RunCancelled, domain.RunCompleted, domain.RunFailed:
		return domain.AgentDependencyCancelled, "run terminal", true
	}
	if edge.TargetKind != waitgraph.KindAgent {
		return "", "", false
	}
	var targetStatus domain.AgentStatus
	err := tx.QueryRowContext(ctx, `SELECT status FROM agent_nodes WHERE id = ? AND run_id = ?`,
		edge.TargetID, edge.RunID).Scan(&targetStatus)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return domain.AgentDependencyFailed, "target agent missing", true
		}
		return "", "", false
	}
	switch targetStatus {
	case domain.AgentCompleted:
		return domain.AgentDependencySatisfied, "target completed", true
	case domain.AgentFailed:
		return domain.AgentDependencyFailed, "target failed", true
	case domain.AgentCancelled:
		return domain.AgentDependencyCancelled, "target cancelled", true
	default:
		return "", "", false
	}
}

func requireRunAgentNodeTx(ctx context.Context, tx *sql.Tx, runID, agentID string) error {
	var exists int
	if err := tx.QueryRowContext(ctx, `SELECT 1 FROM agent_nodes WHERE id = ? AND run_id = ?`,
		agentID, runID).Scan(&exists); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return apperror.New(apperror.CodeNotFound,
				"dependency endpoint agent is not part of the run")
		}
		return err
	}
	return nil
}

func loadOpenDependencyDAGTx(ctx context.Context, tx *sql.Tx, runID string) (*waitgraph.DAG, error) {
	rows, err := tx.QueryContext(ctx, `SELECT source_kind, source_id, target_kind, target_id
		FROM agent_dependency_edges WHERE run_id = ? AND state = 'wait'`, runID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	dag := waitgraph.NewDAG()
	for rows.Next() {
		var sourceKind, sourceID, targetKind, targetID string
		if err := rows.Scan(&sourceKind, &sourceID, &targetKind, &targetID); err != nil {
			return nil, err
		}
		dag.Add(waitgraph.DurableEdge{
			Source: waitgraph.Node{Kind: waitgraph.Kind(sourceKind), ID: sourceID},
			Target: waitgraph.Node{Kind: waitgraph.Kind(targetKind), ID: targetID},
		})
	}
	return dag, rows.Err()
}

func dependencyValidationError(err error) error {
	switch {
	case errors.Is(err, waitgraph.ErrDurableCycle), errors.Is(err, waitgraph.ErrDurableSelfLoop):
		return apperror.New(apperror.CodeConflict, "wait dependency would create a cycle")
	case errors.Is(err, waitgraph.ErrDurableReverseWait):
		return apperror.New(apperror.CodePolicyDenied,
			"lower runtime layers cannot durably wait on an Agent")
	case errors.Is(err, waitgraph.ErrDurableDepth), errors.Is(err, waitgraph.ErrDurableCapacity):
		return apperror.New(apperror.CodeResourceExhausted, "wait dependency graph capacity exceeded")
	default:
		return apperror.Wrap(apperror.CodeInvalidArgument, "wait dependency is invalid", err)
	}
}

func scanDependencyEdge(row scanner) (domain.DependencyEdge, error) {
	var edge domain.DependencyEdge
	var sourceKind, targetKind, state, policy, deadline, created, updated string
	var resolved sql.NullString
	if err := row.Scan(&edge.ID, &edge.RunID, &sourceKind, &edge.SourceID, &targetKind,
		&edge.TargetID, &edge.Reason, &state, &policy, &edge.Generation, &deadline,
		&created, &updated, &resolved); err != nil {
		return domain.DependencyEdge{}, err
	}
	edge.SourceKind = waitgraph.Kind(sourceKind)
	edge.TargetKind = waitgraph.Kind(targetKind)
	edge.State = domain.AgentDependencyState(state)
	edge.FailurePolicy = domain.DependencyFailurePolicy(policy)
	edge.Deadline = parseTS(deadline)
	edge.CreatedAt = parseTS(created)
	edge.UpdatedAt = parseTS(updated)
	if resolved.Valid {
		value := parseTS(resolved.String)
		edge.ResolvedAt = &value
	}
	return edge, nil
}

// ListDependencyEdges returns the run's dependency edges, newest first.
func (s *SQLiteStore) ListDependencyEdges(ctx context.Context, runID string,
	limit int,
) ([]domain.DependencyEdge, error) {
	runID = strings.TrimSpace(runID)
	if limit <= 0 || limit > 256 {
		return nil, apperror.New(apperror.CodeInvalidArgument, "dependency edge list limit is invalid")
	}
	rows, err := s.db.QueryContext(ctx, dependencyEdgeSelect+` WHERE run_id = ?
		ORDER BY created_at DESC, id DESC LIMIT ?`, runID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]domain.DependencyEdge, 0, 8)
	for rows.Next() {
		edge, err := scanDependencyEdge(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, edge)
	}
	return out, rows.Err()
}

// GetDependencyEdge returns one edge by id.
func (s *SQLiteStore) GetDependencyEdge(ctx context.Context, id string) (domain.DependencyEdge, bool, error) {
	id = strings.TrimSpace(id)
	edge, err := scanDependencyEdge(s.db.QueryRowContext(ctx, dependencyEdgeSelect+` WHERE id = ?`, id))
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return domain.DependencyEdge{}, false, nil
		}
		return domain.DependencyEdge{}, false, err
	}
	return edge, true, nil
}

func isUniqueViolation(err error) bool {
	return err != nil && strings.Contains(strings.ToLower(err.Error()), "unique constraint failed")
}
