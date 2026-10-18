package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"cyberagent-workbench/internal/apperror"
	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/events"
	"cyberagent-workbench/internal/redact"
	"cyberagent-workbench/internal/runmutation"
	"cyberagent-workbench/internal/session"
)

func (s *SQLiteStore) AdmitSpecialist(ctx context.Context, admission domain.SpecialistAdmission,
	operationKey string,
) (domain.AgentNode, bool, error) {
	return s.admitSpecialist(ctx, admission, operationKey, nil)
}

func (s *SQLiteStore) admitSpecialist(ctx context.Context, admission domain.SpecialistAdmission,
	operationKey string, binding *childTaskAdmissionBinding,
) (domain.AgentNode, bool, error) {
	admission.AgentID = strings.TrimSpace(admission.AgentID)
	admission.SessionID = strings.TrimSpace(admission.SessionID)
	admission.RunID = strings.TrimSpace(admission.RunID)
	admission.ParentAgentID = strings.TrimSpace(admission.ParentAgentID)
	admission.Title = redact.String(strings.TrimSpace(admission.Title))
	skills, err := domain.NormalizeAgentSkills(admission.Skills)
	if err != nil {
		return domain.AgentNode{}, false,
			apperror.Wrap(apperror.CodeInvalidArgument, "specialist admission skills are invalid", err)
	}
	admission.Skills = skills
	for _, skill := range admission.Skills {
		if !domain.DelegableAgentSkill(skill) {
			return domain.AgentNode{}, false, apperror.New(apperror.CodeInvalidArgument,
				"specialist admission includes a non-delegable control capability")
		}
	}
	if admission.CreatedAt.IsZero() {
		admission.CreatedAt = time.Now().UTC()
	} else {
		admission.CreatedAt = admission.CreatedAt.UTC()
	}
	if err := admission.Validate(); err != nil {
		return domain.AgentNode{}, false,
			apperror.Wrap(apperror.CodeInvalidArgument, "specialist admission is invalid", err)
	}
	workerJSON, err := json.Marshal(admission.Worker)
	if err != nil {
		return domain.AgentNode{}, false, err
	}
	normalizedOperationKey, err := domain.NormalizeAgentOperationKey(operationKey)
	if err != nil {
		return domain.AgentNode{}, false,
			apperror.Wrap(apperror.CodeInvalidArgument, "specialist admission idempotency key is invalid", err)
	}
	skillsJSON, err := marshalRedactedJSON(admission.Skills)
	if err != nil {
		return domain.AgentNode{}, false, err
	}
	pathsJSON, err := json.Marshal(admission.AllowedReadPaths)
	if err != nil {
		return domain.AgentNode{}, false, err
	}
	keyDigest := runmutation.Fingerprint("agent_admission_operation.v1", admission.RunID,
		normalizedOperationKey)
	requestFingerprint := runmutation.Fingerprint("agent_admission_request.v1", admission.RunID,
		admission.ParentAgentID, admission.Title, skillsJSON, strconv.FormatInt(admission.TurnLimit, 10),
		strconv.FormatInt(admission.TokenLimit, 10), strconv.Itoa(admission.MaxChildren))
	if len(admission.AllowedReadPaths) > 0 || admission.ToolCallLimit > 0 || admission.CostLimitMicros > 0 {
		requestFingerprint = runmutation.Fingerprint(requestFingerprint, string(pathsJSON), strconv.FormatInt(admission.ToolCallLimit, 10), strconv.FormatInt(admission.CostLimitMicros, 10))
	}

	if admission.Worker != nil {
		requestFingerprint = runmutation.Fingerprint(requestFingerprint, string(workerJSON))
	}
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{})
	if err != nil {
		return domain.AgentNode{}, false, err
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx, `UPDATE agent_nodes SET updated_at = updated_at WHERE id = ?`,
		admission.ParentAgentID); err != nil {
		return domain.AgentNode{}, false, err
	}
	storedFingerprint, storedAgentID, found, err := getAgentAdmissionOperationTx(ctx, tx, keyDigest)
	if err != nil {
		return domain.AgentNode{}, false, err
	}
	if found {
		if storedFingerprint != requestFingerprint {
			return domain.AgentNode{}, false, apperror.New(apperror.CodeConflict,
				"specialist admission idempotency key was already used for different intent")
		}
		existing, err := scanAgentNode(tx.QueryRowContext(ctx, agentNodeSelect+` WHERE id = ?`, storedAgentID))
		if err != nil {
			return domain.AgentNode{}, false, err
		}
		if err := bindChildTaskAdmissionTx(ctx, tx, binding, existing.ID); err != nil {
			return domain.AgentNode{}, false, err
		}
		if err := tx.Commit(); err != nil {
			return domain.AgentNode{}, false, err
		}
		return existing, true, nil
	}
	if err := requireSpecialistDelegationApplicationAdmissionTx(ctx, tx, admission.RunID,
		keyDigest); err != nil {
		return domain.AgentNode{}, false, err
	}

	run, mission, err := getCoordinatorRunTx(ctx, tx, admission.RunID)
	if err != nil {
		return domain.AgentNode{}, false, err
	}
	if run.Status != domain.RunRunning {
		return domain.AgentNode{}, false,
			apperror.New(apperror.CodeFailedPrecondition, "specialist admission requires a running Run")
	}

	parent, err := scanAgentNode(tx.QueryRowContext(ctx, agentLifecycleSelect+` WHERE id = ?`,
		admission.ParentAgentID))
	if err != nil {
		return domain.AgentNode{}, false, err
	}
	if parent.RunID != run.ID || parent.Role != domain.AgentRoleRoot || parent.ParentID != "" || parent.Depth != 0 {
		return domain.AgentNode{}, false,
			apperror.New(apperror.CodeInvalidArgument, "specialist parent must be the Run root Agent")
	}
	if parent.Status != domain.AgentReady {
		return domain.AgentNode{}, false, apperror.New(apperror.CodeFailedPrecondition,
			"specialist admission requires an idle ready root Agent")
	}
	if parent.ChildLimit != 0 && parent.ChildLimit != admission.MaxChildren {
		return domain.AgentNode{}, false, apperror.New(apperror.CodeConflict,
			"root Agent already uses a different child capacity")
	}
	var childCount int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM agent_nodes WHERE run_id = ? AND parent_id = ? AND status NOT IN ('completed','failed','cancelled')`,
		run.ID, parent.ID).Scan(&childCount); err != nil {
		return domain.AgentNode{}, false, err
	}
	if childCount >= admission.MaxChildren || childCount >= domain.MaxAgentChildren {
		return domain.AgentNode{}, false,
			apperror.New(apperror.CodeResourceExhausted, "root Agent child capacity is exhausted")
	}
	var historyCount int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM agent_nodes WHERE run_id=?`, run.ID).Scan(&historyCount); err != nil {
		return domain.AgentNode{}, false, err
	}
	if historyCount >= domain.MaxAgentHistoryNodesPerRun {
		return domain.AgentNode{}, false, apperror.New(apperror.CodeResourceExhausted, "Run worker history bound exhausted")
	}
	parentSkills := make(map[string]struct{}, len(parent.Skills))
	for _, skill := range parent.Skills {
		parentSkills[skill] = struct{}{}
	}
	for _, skill := range admission.Skills {
		if _, allowed := parentSkills[skill]; !allowed {
			return domain.AgentNode{}, false, apperror.New(apperror.CodeInvalidArgument,
				"specialist skills must be a subset of its parent skills")
		}
	}
	reservedTurns, reservedTokens, err := specialistReservationsTx(ctx, tx, run.ID, parent.ID)
	if err != nil {
		return domain.AgentNode{}, false, err
	}
	remainingTurns := int64(run.Budget.MaxTurns) - parent.TurnsUsed - reservedTurns - admission.TurnLimit
	if remainingTurns < 1 {
		return domain.AgentNode{}, false, apperror.New(apperror.CodeResourceExhausted,
			"specialist turn reservation must leave one root turn available")
	}
	if run.Budget.MaxTokens > 0 {
		remainingTokens := run.Budget.MaxTokens - parent.TokensUsed - reservedTokens - admission.TokenLimit
		if remainingTokens < 1 {
			return domain.AgentNode{}, false, apperror.New(apperror.CodeResourceExhausted,
				"specialist token reservation must leave root token capacity available")
		}
	}
	parentSession, err := scanSession(tx.QueryRowContext(ctx, `SELECT id, workspace_id, title, route, status,
		created_at, updated_at FROM sessions WHERE id = ?`, parent.SessionID))
	if err != nil {
		return domain.AgentNode{}, false, err
	}
	if parentSession.Status != session.StatusActive {
		return domain.AgentNode{}, false,
			apperror.New(apperror.CodeFailedPrecondition, "root Agent Session must be active")
	}

	now := admission.CreatedAt
	childSession := session.Session{
		ID: admission.SessionID, WorkspaceID: mission.WorkspaceID, Title: admission.Title,
		Route: parentSession.Route, Status: session.StatusActive, CreatedAt: now, UpdatedAt: now,
	}
	if err := childSession.Validate(); err != nil {
		return domain.AgentNode{}, false, err
	}
	child := domain.AgentNode{
		ID: admission.AgentID, RunID: run.ID, ParentID: parent.ID, SessionID: childSession.ID,
		Role: domain.AgentRoleSpecialist, Profile: parent.Profile, Skills: admission.Skills, Worker: admission.Worker,
		Status: domain.AgentReady, Depth: parent.Depth + 1, ChildLimit: 0,
		TurnLimit: admission.TurnLimit, TokenLimit: admission.TokenLimit,
		AllowedReadPaths: admission.AllowedReadPaths, ToolCallLimit: admission.ToolCallLimit, CostLimitMicros: admission.CostLimitMicros,
		StatusReason: "awaiting internal scheduling", Version: 1, CreatedAt: now, UpdatedAt: now,
	}
	if binding != nil && len(binding.task.DependencyOrdinals) != 0 {
		child.Status = domain.AgentWaiting
		child.StatusReason = "child task dependencies"
	}
	if err := child.Validate(); err != nil {
		return domain.AgentNode{}, false, err
	}
	updatedParent := parent
	updatedParent.ChildLimit = admission.MaxChildren
	updatedParent.TurnLimit = int64(run.Budget.MaxTurns) - reservedTurns - child.TurnLimit
	if run.Budget.MaxTokens > 0 {
		updatedParent.TokenLimit = run.Budget.MaxTokens - reservedTokens - child.TokenLimit
	}
	updatedParent.Version++
	updatedParent.UpdatedAt = now
	if err := updatedParent.Validate(); err != nil {
		return domain.AgentNode{}, false, err
	}
	result, err := tx.ExecContext(ctx, `UPDATE agent_nodes SET child_limit = ?, turn_limit = ?, token_limit = ?,
		version = ?, updated_at = ? WHERE id = ? AND version = ? AND status = ?`,
		updatedParent.ChildLimit, updatedParent.TurnLimit, updatedParent.TokenLimit, updatedParent.Version,
		ts(updatedParent.UpdatedAt), updatedParent.ID, parent.Version, domain.AgentReady)
	if err != nil {
		return domain.AgentNode{}, false, err
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return domain.AgentNode{}, false, err
	}
	if rows != 1 {
		return domain.AgentNode{}, false,
			apperror.New(apperror.CodeConflict, "root Agent changed during specialist admission")
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO sessions
		(id, workspace_id, title, route, status, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?)`,
		childSession.ID, childSession.WorkspaceID, childSession.Title, childSession.Route,
		childSession.Status, ts(childSession.CreatedAt), ts(childSession.UpdatedAt)); err != nil {
		return domain.AgentNode{}, false, err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO agent_nodes
		(id, run_id, parent_id, session_id, role, profile, skills_json, status, depth, child_limit,
		turn_limit, token_limit, turns_used, tokens_used, active_attempt_id, status_reason, version,
		created_at, updated_at, finished_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 0, 0, '', ?, ?, ?, ?, NULL)`,
		child.ID, child.RunID, child.ParentID, child.SessionID, child.Role, child.Profile, skillsJSON,
		child.Status, child.Depth, child.ChildLimit, child.TurnLimit, child.TokenLimit, child.StatusReason,
		child.Version, ts(child.CreatedAt), ts(child.UpdatedAt)); err != nil {
		return domain.AgentNode{}, false, err
	}
	if admission.Worker != nil {
		if _, err := tx.ExecContext(ctx, `UPDATE agent_nodes SET worker_profile_json=? WHERE id=?`, string(workerJSON), child.ID); err != nil {
			return domain.AgentNode{}, false, err
		}
		if err := s.captureWorkerParentHistoryTx(ctx, tx, child); err != nil {
			return domain.AgentNode{}, false, err
		}
	}
	// The inserted node already has the no-tool defaults. Only an explicit child
	// grant needs an authority update, in the same admission transaction.
	if len(child.AllowedReadPaths) > 0 || child.ToolCallLimit > 0 || child.CostLimitMicros > 0 {
		if _, err := tx.ExecContext(ctx, `UPDATE agent_nodes SET allowed_read_paths_json=?,tool_call_limit=?,cost_limit_micros=? WHERE id=?`, string(pathsJSON), child.ToolCallLimit, child.CostLimitMicros, child.ID); err != nil {
			return domain.AgentNode{}, false, err
		}
	}
	if err := bindChildTaskAdmissionTx(ctx, tx, binding, child.ID); err != nil {
		return domain.AgentNode{}, false, err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO agent_admission_operations
		(operation_key_digest, request_fingerprint, agent_id, created_at) VALUES (?, ?, ?, ?)`,
		keyDigest, requestFingerprint, child.ID, ts(now)); err != nil {
		return domain.AgentNode{}, false, err
	}
	if err := appendSupervisorEventTx(ctx, tx, run, events.AgentCapacityReservedEvent,
		"agent_coordinator", parent.ID, map[string]any{
			"agent_id": child.ID, "child_count": childCount + 1, "child_limit": updatedParent.ChildLimit,
			"reserved_turns": child.TurnLimit, "reserved_tokens": child.TokenLimit,
			"root_turn_limit": updatedParent.TurnLimit, "root_token_limit": updatedParent.TokenLimit,
			"root_version": updatedParent.Version,
		}); err != nil {
		return domain.AgentNode{}, false, err
	}
	if err := appendSupervisorEventTx(ctx, tx, run, events.AgentRegisteredEvent,
		"agent_coordinator", child.ID, map[string]any{
			"role": child.Role, "parent_agent_id": child.ParentID, "session_id": child.SessionID,
			"profile": child.Profile, "skills": child.Skills, "status": child.Status,
			"depth": child.Depth, "child_limit": child.ChildLimit,
			"turn_limit": child.TurnLimit, "token_limit": child.TokenLimit, "version": child.Version,
		}); err != nil {
		return domain.AgentNode{}, false, err
	}
	if _, err := createAgentGraphSnapshotTx(ctx, tx, run); err != nil {
		return domain.AgentNode{}, false, err
	}
	if err := tx.Commit(); err != nil {
		return domain.AgentNode{}, false, err
	}
	return child, false, nil
}

func getAgentAdmissionOperationTx(ctx context.Context, tx *sql.Tx,
	keyDigest string,
) (string, string, bool, error) {
	var fingerprint, agentID string
	err := tx.QueryRowContext(ctx, `SELECT request_fingerprint, agent_id
		FROM agent_admission_operations WHERE operation_key_digest = ?`, keyDigest).
		Scan(&fingerprint, &agentID)
	if errors.Is(err, sql.ErrNoRows) {
		return "", "", false, nil
	}
	if err != nil {
		return "", "", false, err
	}
	return fingerprint, agentID, true, nil
}

func specialistReservationsTx(ctx context.Context, queryer interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}, runID string, parentID string) (int64, int64, error) {
	var turns, tokens sql.NullInt64
	err := queryer.QueryRowContext(ctx, `SELECT SUM(CASE WHEN status IN ('completed','failed','cancelled') THEN turns_used ELSE MAX(turn_limit,turns_used) END), SUM(CASE WHEN status IN ('completed','failed','cancelled') THEN tokens_used ELSE MAX(token_limit,tokens_used) END) FROM agent_nodes
		WHERE run_id = ? AND parent_id = ? AND role = ?`, runID, parentID, domain.AgentRoleSpecialist).
		Scan(&turns, &tokens)
	if err != nil {
		return 0, 0, err
	}
	if turns.Int64 < 0 || tokens.Int64 < 0 {
		return 0, 0, fmt.Errorf("specialist budget reservation is invalid")
	}
	return turns.Int64, tokens.Int64, nil
}

func effectiveRootBudgetTx(ctx context.Context, tx *sql.Tx, run domain.Run,
	rootID string,
) (domain.Budget, error) {
	turns, tokens, err := specialistReservationsTx(ctx, tx, run.ID, rootID)
	if err != nil {
		return domain.Budget{}, err
	}
	effective := run.Budget
	if turns >= int64(effective.MaxTurns) {
		return domain.Budget{}, apperror.New(apperror.CodeConflict,
			"specialist reservations exhausted the root turn budget")
	}
	effective.MaxTurns -= int(turns)
	if effective.MaxTokens > 0 {
		if tokens >= effective.MaxTokens {
			return domain.Budget{}, apperror.New(apperror.CodeConflict,
				"specialist reservations exhausted the root token budget")
		}
		effective.MaxTokens -= tokens
	}
	return effective, effective.Validate()
}

func syncSpecialistLifecycleTx(ctx context.Context, tx *sql.Tx, run domain.Run,
	rootStatus domain.AgentStatus, at time.Time,
) (bool, error) {
	nodes, err := listAgentNodesWithSelect(ctx, tx, run.ID, agentLifecycleSelect)
	if err != nil {
		return false, err
	}
	if at.IsZero() {
		at = time.Now().UTC()
	} else {
		at = at.UTC()
	}
	changed := false
	for _, current := range nodes {
		if current.Role != domain.AgentRoleSpecialist || current.Terminal() {
			continue
		}
		target := domain.AgentStatus("")
		reason := ""
		switch run.Status {
		case domain.RunPaused:
			if current.Status == domain.AgentReady || current.Status == domain.AgentRunning {
				target, reason = domain.AgentWaiting, "run paused"
			}
		case domain.RunWaitingApproval:
			if current.Status == domain.AgentReady || current.Status == domain.AgentRunning {
				target, reason = domain.AgentWaiting, "waiting for approval"
			}
		case domain.RunRunning:
			if rootStatus == domain.AgentReady && current.Status == domain.AgentWaiting &&
				(current.StatusReason == "run paused" || current.StatusReason == "waiting for approval") {
				target = domain.AgentReady
			}
		case domain.RunCompleted:
			target, reason = domain.AgentCancelled, "run completed"
		case domain.RunFailed:
			target, reason = domain.AgentFailed, "run failed"
		case domain.RunCancelled:
			target, reason = domain.AgentCancelled, "run cancelled"
		}
		if target == "" || target == current.Status {
			continue
		}
		if !current.CanTransition(target) {
			return false, apperror.New(apperror.CodeConflict,
				fmt.Sprintf("specialist Agent cannot transition from %s to %s", current.Status, target))
		}
		if current.Status == domain.AgentRunning {
			failureCode := specialistLifecycleFailureCode(run.Status)
			if _, err := interruptAgentAttemptTx(ctx, tx, run, current, failureCode, reason, at); err != nil {
				return false, err
			}
		}
		updated := current
		updated.Status = target
		updated.ActiveAttemptID = ""
		updated.StatusReason = normalizeAgentStatusReason(reason)
		updated.Version++
		updated.UpdatedAt = at
		if updated.Terminal() {
			finished := at
			updated.FinishedAt = &finished
		} else {
			updated.FinishedAt = nil
		}
		if err := updated.Validate(); err != nil {
			return false, err
		}
		result, err := tx.ExecContext(ctx, `UPDATE agent_nodes SET status = ?, active_attempt_id = '',
			status_reason = ?, version = ?, updated_at = ?, finished_at = ?
			WHERE id = ? AND version = ? AND status = ?`, updated.Status, updated.StatusReason,
			updated.Version, ts(updated.UpdatedAt), nullableTS(updated.FinishedAt), updated.ID,
			current.Version, current.Status)
		if err != nil {
			return false, err
		}
		rows, err := result.RowsAffected()
		if err != nil {
			return false, err
		}
		if rows != 1 {
			return false, apperror.New(apperror.CodeConflict,
				"specialist Agent changed during Run lifecycle projection")
		}
		if updated.Terminal() {
			if _, err := tx.ExecContext(ctx, `UPDATE sessions SET status = ?, updated_at = ?
				WHERE id = ? AND status = ?`, session.StatusArchived, ts(at), updated.SessionID,
				session.StatusActive); err != nil {
				return false, err
			}
		}
		if err := appendSupervisorEventTx(ctx, tx, run, events.AgentStatusChangedEvent,
			"agent_coordinator", updated.ID, map[string]any{
				"from": current.Status, "to": updated.Status, "reason": updated.StatusReason,
				"parent_agent_id": updated.ParentID, "version": updated.Version,
			}); err != nil {
			return false, err
		}
		changed = true
	}
	return changed, nil
}

func specialistLifecycleFailureCode(status domain.RunStatus) string {
	switch status {
	case domain.RunPaused:
		return "run_paused"
	case domain.RunWaitingApproval:
		return "waiting_approval"
	case domain.RunCompleted:
		return "run_completed"
	case domain.RunFailed:
		return "run_failed"
	case domain.RunCancelled:
		return "run_cancelled"
	default:
		return "run_state_changed"
	}
}
