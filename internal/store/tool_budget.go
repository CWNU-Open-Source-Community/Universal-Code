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
	"cyberagent-workbench/internal/toolbudget"
)

func (s *SQLiteStore) ChargeToolCall(ctx context.Context, request toolbudget.ChargeRequest) (toolbudget.Usage, error) {
	normalized, err := request.Normalize()
	if err != nil {
		return toolbudget.Usage{}, err
	}
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{})
	if err != nil {
		return toolbudget.Usage{}, err
	}
	defer func() { _ = tx.Rollback() }()
	binding, tracked, err := resolveToolBudgetBindingTx(ctx, tx, normalized)
	if err != nil {
		return toolbudget.Usage{}, err
	}
	if normalized.LeaseID != "" {
		if !tracked {
			return toolbudget.Usage{}, apperror.New(apperror.CodeFailedPrecondition,
				"Run execution lease cannot fence an untracked tool call")
		}
		if err := requireRunExecutionLeaseTx(ctx, tx, binding.RunID, normalized.LeaseID,
			normalized.LeaseGeneration); err != nil {
			return toolbudget.Usage{}, err
		}
	}
	if normalized.RequestedBy == "specialist_runner" {
		var existing string
		if err := tx.QueryRowContext(ctx, `SELECT charge_id FROM specialist_tool_calls WHERE agent_attempt_id=? AND call_id=? AND status='pending'`, normalized.AgentAttemptID, normalized.ToolCallID).Scan(&existing); err != nil {
			return toolbudget.Usage{}, err
		}
		if existing != "" {
			var sequence int64
			var chargedAt string
			if err := tx.QueryRowContext(ctx, `SELECT sequence,created_at FROM run_tool_calls WHERE id=? AND run_id=?`, existing, binding.RunID).Scan(&sequence, &chargedAt); err != nil {
				return toolbudget.Usage{}, err
			}
			budget, _, err := loadRunBudgetTx(ctx, tx, binding.RunID)
			if err != nil {
				return toolbudget.Usage{}, err
			}
			if err := tx.Commit(); err != nil {
				return toolbudget.Usage{}, err
			}
			return usageFromValues(binding.RunID, sequence, budget.MaxToolCalls, existing, parseTS(chargedAt), nil), nil
		}
	}
	if normalized.RequestedBy == "specialist_runner" {
		var limit, used int64
		if err := tx.QueryRowContext(ctx, `SELECT n.tool_call_limit,(SELECT COUNT(*) FROM specialist_tool_calls c JOIN agent_attempts a ON a.id=c.agent_attempt_id WHERE a.agent_id=n.id AND c.charge_id<>'') FROM agent_nodes n WHERE n.id=? AND n.run_id=?`, normalized.AgentID, binding.RunID).Scan(&limit, &used); err != nil {
			return toolbudget.Usage{}, err
		}
		if limit > 0 && used >= limit {
			return toolbudget.Usage{}, apperror.New(apperror.CodeResourceExhausted, "child tool-call budget exhausted")
		}
	}
	if !tracked {
		if err := tx.Commit(); err != nil {
			return toolbudget.Usage{}, err
		}
		return toolbudget.Usage{Remaining: -1}, nil
	}
	budget, status, err := loadRunBudgetTx(ctx, tx, binding.RunID)
	if err != nil {
		return toolbudget.Usage{}, err
	}
	if status == domain.RunCompleted || status == domain.RunFailed || status == domain.RunCancelled {
		return toolbudget.Usage{}, apperror.New(apperror.CodeFailedPrecondition,
			fmt.Sprintf("run %s is terminal and cannot invoke tools", binding.RunID))
	}
	now := time.Now().UTC()
	if _, err := tx.ExecContext(ctx, `INSERT INTO run_tool_usage (run_id, consumed, updated_at)
		VALUES (?, 0, ?) ON CONFLICT(run_id) DO NOTHING`, binding.RunID, ts(now)); err != nil {
		return toolbudget.Usage{}, err
	}
	var consumed int64
	var previousUpdated string
	var exhaustedAt sql.NullString
	if err := tx.QueryRowContext(ctx, `SELECT consumed, updated_at, exhausted_at FROM run_tool_usage WHERE run_id = ?`, binding.RunID).
		Scan(&consumed, &previousUpdated, &exhaustedAt); err != nil {
		return toolbudget.Usage{}, err
	}
	if budget.MaxToolCalls > 0 && consumed >= budget.MaxToolCalls {
		if !exhaustedAt.Valid {
			if _, err := tx.ExecContext(ctx, `UPDATE run_tool_usage SET updated_at = ?, exhausted_at = ?
				WHERE run_id = ? AND exhausted_at IS NULL`, ts(now), ts(now), binding.RunID); err != nil {
				return toolbudget.Usage{}, err
			}
			event, err := events.New(binding.RunID, binding.MissionID, events.ToolBudgetExhaustedEvent,
				"tool_budget", binding.RunID, map[string]any{
					"consumed": consumed, "limit": budget.MaxToolCalls,
					"tool_name": normalized.ToolName, "action_class": normalized.ActionClass,
				})
			if err != nil {
				return toolbudget.Usage{}, err
			}
			if _, err := insertRunEventTx(ctx, tx, event); err != nil {
				return toolbudget.Usage{}, err
			}
		}
		if err := tx.Commit(); err != nil {
			return toolbudget.Usage{}, err
		}
		return toolbudget.Usage{}, apperror.New(apperror.CodeResourceExhausted,
			fmt.Sprintf("run %s exhausted its %d tool-call budget", binding.RunID, budget.MaxToolCalls))
	}
	if consumed == int64(^uint64(0)>>1) {
		return toolbudget.Usage{}, apperror.New(apperror.CodeResourceExhausted, "tool-call counter overflow")
	}
	chargeID := idgen.New("toolcall")
	sequence := consumed + 1
	result, err := tx.ExecContext(ctx, `UPDATE run_tool_usage SET consumed = ?, updated_at = ?
		WHERE run_id = ? AND consumed = ?`, sequence, ts(now), binding.RunID, consumed)
	if err != nil {
		return toolbudget.Usage{}, err
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return toolbudget.Usage{}, err
	}
	if rows != 1 {
		return toolbudget.Usage{}, errors.New("tool-call budget changed concurrently")
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO run_tool_calls
		(id, run_id, session_id, workspace_id, tool_name, action_class, sequence, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)`, chargeID, binding.RunID, normalized.SessionID,
		normalized.WorkspaceID, normalized.ToolName, normalized.ActionClass, sequence, ts(now)); err != nil {
		return toolbudget.Usage{}, err
	}
	if normalized.RequestedBy == "specialist_runner" {
		if _, err := tx.ExecContext(ctx, `UPDATE specialist_tool_calls SET charge_id=? WHERE agent_attempt_id=? AND call_id=? AND charge_id='' AND status='pending'`, chargeID, normalized.AgentAttemptID, normalized.ToolCallID); err != nil {
			return toolbudget.Usage{}, err
		}
	}
	event, err := events.New(binding.RunID, binding.MissionID, events.ToolBudgetChargedEvent,
		"tool_budget", chargeID, map[string]any{
			"charge_id": chargeID, "session_id": normalized.SessionID, "workspace_id": normalized.WorkspaceID,
			"tool_name": normalized.ToolName, "action_class": normalized.ActionClass,
			"consumed": sequence, "limit": budget.MaxToolCalls,
		})
	if err != nil {
		return toolbudget.Usage{}, err
	}
	if _, err := insertRunEventTx(ctx, tx, event); err != nil {
		return toolbudget.Usage{}, err
	}
	if err := tx.Commit(); err != nil {
		return toolbudget.Usage{}, err
	}
	return usageFromValues(binding.RunID, sequence, budget.MaxToolCalls, chargeID, now, nil), nil
}

func (s *SQLiteStore) GetToolCallUsage(ctx context.Context, runID string) (toolbudget.Usage, error) {
	runID = strings.TrimSpace(runID)
	if runID == "" {
		return toolbudget.Usage{}, errors.New("run id is required")
	}
	var budgetJSON string
	if err := s.db.QueryRowContext(ctx, `SELECT budget_json FROM runs WHERE id = ?`, runID).Scan(&budgetJSON); err != nil {
		return toolbudget.Usage{}, err
	}
	var budget domain.Budget
	if err := json.Unmarshal([]byte(budgetJSON), &budget); err != nil {
		return toolbudget.Usage{}, fmt.Errorf("decode run budget: %w", err)
	}
	var consumed int64
	var updated sql.NullString
	var exhausted sql.NullString
	err := s.db.QueryRowContext(ctx, `SELECT consumed, updated_at, exhausted_at FROM run_tool_usage WHERE run_id = ?`, runID).
		Scan(&consumed, &updated, &exhausted)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return toolbudget.Usage{}, err
	}
	var lastCharge sql.NullString
	if err := s.db.QueryRowContext(ctx, `SELECT id FROM run_tool_calls WHERE run_id = ? ORDER BY sequence DESC LIMIT 1`, runID).
		Scan(&lastCharge); err != nil && !errors.Is(err, sql.ErrNoRows) {
		return toolbudget.Usage{}, err
	}
	lastUpdated := time.Time{}
	if updated.Valid {
		lastUpdated = parseTS(updated.String)
	}
	var exhaustedAt *time.Time
	if exhausted.Valid {
		parsed := parseTS(exhausted.String)
		exhaustedAt = &parsed
	}
	return usageFromValues(runID, consumed, budget.MaxToolCalls, lastCharge.String, lastUpdated, exhaustedAt), nil
}

func resolveToolBudgetBindingTx(ctx context.Context, tx *sql.Tx, request toolbudget.ChargeRequest) (runBinding, bool, error) {
	if request.RequestedBy == "specialist_runner" {
		ref := domain.AgentAttemptRef{RunID: request.RunID, AgentID: request.AgentID, AttemptID: request.AgentAttemptID}
		attempt, child, run, err := loadActiveAgentAttemptTx(ctx, tx, ref)
		if err != nil {
			return runBinding{}, false, err
		}
		if child.SessionID != request.SessionID || attempt.LeaseID != request.LeaseID || attempt.LeaseGeneration != request.LeaseGeneration {
			return runBinding{}, false, errors.New("Specialist tool budget actor or lease differs")
		}
		var bound int
		err = tx.QueryRowContext(ctx, `SELECT count(*) FROM specialist_tool_bindings b JOIN specialist_tool_calls c ON c.agent_attempt_id=b.agent_attempt_id WHERE b.agent_attempt_id=? AND b.workspace_id=? AND c.call_id=? AND c.name=? AND c.status='pending'`, attempt.ID, request.WorkspaceID, request.ToolCallID, request.ToolName).Scan(&bound)
		if err != nil {
			return runBinding{}, false, err
		}
		if bound != 1 {
			return runBinding{}, false, errors.New("Specialist tool budget lacks its pending child call")
		}
		return runBinding{RunID: run.ID, MissionID: run.MissionID, WorkspaceID: request.WorkspaceID}, true, nil
	}
	if request.SessionID != "" {
		binding, bound, err := runBindingForSessionTx(ctx, tx, request.SessionID)
		if err != nil {
			return runBinding{}, false, err
		}
		if bound {
			if request.RunID != "" && request.RunID != binding.RunID {
				return runBinding{}, false, errors.New("tool call run does not match its session binding")
			}
			if request.WorkspaceID != binding.WorkspaceID {
				return runBinding{}, false, errors.New("tool call workspace does not match its Run")
			}
			return binding, true, nil
		}
		if request.RunID == "" {
			return runBinding{}, false, nil
		}
	}
	if request.RunID == "" {
		return runBinding{}, false, nil
	}
	var binding runBinding
	var sessionID string
	if err := tx.QueryRowContext(ctx, `SELECT runs.id, runs.mission_id, runs.session_id, missions.workspace_id
		FROM runs JOIN missions ON missions.id = runs.mission_id WHERE runs.id = ?`, request.RunID).
		Scan(&binding.RunID, &binding.MissionID, &sessionID, &binding.WorkspaceID); err != nil {
		return runBinding{}, false, err
	}
	if request.SessionID != "" && request.SessionID != sessionID {
		return runBinding{}, false, errors.New("tool call session does not match its Run")
	}
	if request.WorkspaceID != binding.WorkspaceID {
		return runBinding{}, false, errors.New("tool call workspace does not match its Run")
	}
	return binding, true, nil
}

func loadRunBudgetTx(ctx context.Context, tx *sql.Tx, runID string) (domain.Budget, domain.RunStatus, error) {
	var budgetJSON string
	var status domain.RunStatus
	if err := tx.QueryRowContext(ctx, `SELECT budget_json, status FROM runs WHERE id = ?`, runID).
		Scan(&budgetJSON, &status); err != nil {
		return domain.Budget{}, "", err
	}
	var budget domain.Budget
	if err := json.Unmarshal([]byte(budgetJSON), &budget); err != nil {
		return domain.Budget{}, "", fmt.Errorf("decode run budget: %w", err)
	}
	if err := budget.Validate(); err != nil {
		return domain.Budget{}, "", err
	}
	return budget, status, nil
}

func usageFromValues(runID string, consumed int64, limit int64, lastCharge string, updated time.Time, exhaustedAt *time.Time) toolbudget.Usage {
	remaining := int64(-1)
	if limit > 0 {
		remaining = max(0, limit-consumed)
	}
	return toolbudget.Usage{
		RunID: runID, Consumed: consumed, Limit: limit, Remaining: remaining,
		LastCharge: lastCharge, LastUpdated: updated, ExhaustedAt: exhaustedAt, Tracked: true,
	}
}
