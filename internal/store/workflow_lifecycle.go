package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	"cyberagent-workbench/internal/apperror"
	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/events"
)

// The single time parameter is supplied by the candidate/claim caller. Paused
// workflows become claimable only for terminal Run settlement or deadline work.
const workflowLifecycleDueSQL = `EXISTS(SELECT 1 FROM runs WHERE runs.id=workflow_executions.run_id AND runs.status IN ('completed','failed','cancelled')) OR julianday(?) >= julianday(created_at)+(SELECT json_extract(spec_json,'$.limits.timeout_millis') FROM workflow_definitions WHERE workflow_definitions.id=workflow_executions.definition_id)/86400000.0`

func workflowLifecycleClaimTx(ctx context.Context, tx *sql.Tx, id, owner string, revision int64) (domain.WorkflowExecution, domain.Run, string, error) {
	var e domain.WorkflowExecution
	var run domain.Run
	now := time.Now().UTC()
	result, err := tx.ExecContext(ctx, `UPDATE workflow_executions SET updated_at=updated_at WHERE id=? AND version=? AND claim_owner=? AND claim_until>? AND status IN ('running','waiting','waiting_approval','paused')`, id, revision, owner, ts(now))
	if err != nil {
		return e, run, "", err
	}
	if err = requireSingleLeaseUpdate(result, "workflow lifecycle lost its claim or revision"); err != nil {
		return e, run, "", err
	}
	e, err = scanWorkflowExecution(tx.QueryRowContext(ctx, workflowExecutionSelect+` WHERE id=?`, id))
	if err != nil {
		return e, run, "", err
	}
	run, _, err = getCoordinatorRunTx(ctx, tx, e.RunID)
	if err != nil {
		return e, run, "", err
	}
	if run.Terminal() {
		return e, run, "Run ended as " + string(run.Status), nil
	}
	var raw string
	if err = tx.QueryRowContext(ctx, `SELECT spec_json FROM workflow_definitions WHERE id=?`, e.DefinitionID).Scan(&raw); err != nil {
		return e, run, "", err
	}
	spec, err := domain.DecodeWorkflowSpec([]byte(raw))
	if err != nil {
		return e, run, "", err
	}
	if now.Before(e.CreatedAt.Add(time.Duration(spec.Limits.TimeoutMillis) * time.Millisecond)) {
		return e, run, "", apperror.New(apperror.CodeConflict, "workflow lifecycle requires its deadline or a terminal Run")
	}
	return e, run, "workflow deadline exceeded", nil
}

func workflowPendingStepsTx(ctx context.Context, tx *sql.Tx, id string) ([]domain.WorkflowStepExecution, error) {
	rows, err := tx.QueryContext(ctx, workflowStepSelect+` WHERE execution_id=? AND status='pending' ORDER BY rowid`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var steps []domain.WorkflowStepExecution
	for rows.Next() {
		step, err := scanWorkflowStep(rows)
		if err != nil {
			return nil, err
		}
		steps = append(steps, step)
	}
	return steps, rows.Err()
}

func (s *SQLiteStore) ListWorkflowLifecycleSteps(ctx context.Context, id, owner string, revision int64) ([]domain.WorkflowStepExecution, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	if _, _, _, err = workflowLifecycleClaimTx(ctx, tx, id, owner, revision); err != nil {
		return nil, err
	}
	return workflowPendingStepsTx(ctx, tx, id)
}

// This read precedes external resource closure; settlement repeats the check in
// its write transaction. A replacement Run lease fences the captured attempt.
func (s *SQLiteStore) ValidateWorkflowLifecycleAttempt(ctx context.Context, id, owner string, revision int64, lease domain.RunExecutionLease, a domain.AgentAttempt) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	e, _, _, err := workflowLifecycleClaimTx(ctx, tx, id, owner, revision)
	if err != nil {
		return err
	}
	var owned int
	err = tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM workflow_steps w JOIN agent_nodes n ON n.id=w.agent_id AND n.session_id=w.session_id JOIN agent_nodes p ON p.id=n.parent_id AND p.run_id=n.run_id AND p.role='root' JOIN runs r ON r.id=n.run_id JOIN missions m ON m.id=r.mission_id JOIN sessions s ON s.id=n.session_id AND s.workspace_id=m.workspace_id WHERE w.execution_id=? AND w.status='pending' AND n.run_id=? AND n.id=? AND n.role='specialist' AND n.active_attempt_id=? AND n.status='running'`, e.ID, e.RunID, a.AgentID, a.ID).Scan(&owned)
	if err != nil {
		return err
	}
	if owned != 1 {
		return apperror.New(apperror.CodeConflict, "workflow cleanup attempt lost its child ownership")
	}
	_, err = workflowLifecycleAttemptTx(ctx, tx, e.RunID, lease, a)
	return err
}

func workflowLifecycleAttemptTx(ctx context.Context, tx *sql.Tx, runID string, lease domain.RunExecutionLease, a domain.AgentAttempt) (bool, error) {
	if lease.RunID != runID || a.RunID != runID || a.LeaseID == lease.LeaseID && a.LeaseGeneration == lease.Generation {
		return false, apperror.New(apperror.CodeConflict, "workflow cleanup requires a replaced attempt lease")
	}
	if err := requireRunExecutionLeaseTx(ctx, tx, runID, lease.LeaseID, lease.Generation); err != nil {
		return false, err
	}
	var waiting bool
	err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM specialist_tool_calls c WHERE c.agent_attempt_id=a.id AND c.status='pending' AND c.started_at IS NULL AND c.approval_id<>'') AND NOT EXISTS(SELECT 1 FROM specialist_tool_calls c WHERE c.agent_attempt_id=a.id AND c.status='pending' AND c.started_at IS NOT NULL) FROM agent_attempts a JOIN agent_nodes n ON n.id=a.agent_id AND n.active_attempt_id=a.id AND n.status='running' WHERE a.id=? AND a.run_id=? AND a.agent_id=? AND a.parent_agent_id=? AND a.status='running' AND a.lease_id=? AND a.lease_generation=?`, a.ID, runID, a.AgentID, a.ParentAgentID, a.LeaseID, a.LeaseGeneration).Scan(&waiting)
	if errors.Is(err, sql.ErrNoRows) {
		err = apperror.New(apperror.CodeConflict, "workflow cleanup attempt identity or lease changed")
	}
	return waiting, err
}

// FinalizeWorkflowLifecycle is a host-only retirement transaction. It cannot
// admit work, recover an attempt or reset budgets. Every pending step and the
// execution become durable together, with the current claim and revision.
func (s *SQLiteStore) FinalizeWorkflowLifecycle(ctx context.Context, id, owner string, revision int64, lease *domain.RunExecutionLease, expected []domain.AgentAttempt) (domain.WorkflowExecution, error) {
	var e domain.WorkflowExecution
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return e, err
	}
	defer tx.Rollback()
	e, run, reason, err := workflowLifecycleClaimTx(ctx, tx, id, owner, revision)
	if err != nil {
		return e, err
	}
	if lease != nil {
		if lease.RunID != run.ID {
			return e, apperror.New(apperror.CodeConflict, "workflow cleanup lease belongs to another Run")
		}
		if err = requireRunExecutionLeaseTx(ctx, tx, run.ID, lease.LeaseID, lease.Generation); err != nil {
			return e, err
		}
	}
	var rawSpec string
	if err = tx.QueryRowContext(ctx, `SELECT spec_json FROM workflow_definitions WHERE id=?`, e.DefinitionID).Scan(&rawSpec); err != nil {
		return e, err
	}
	spec, err := domain.DecodeWorkflowSpec([]byte(rawSpec))
	if err != nil {
		return e, err
	}
	steps, err := workflowPendingStepsTx(ctx, tx, id)
	if err != nil {
		return e, err
	}
	attempts := map[string]domain.AgentAttempt{}
	for _, a := range expected {
		if _, exists := attempts[a.ID]; exists {
			return e, apperror.New(apperror.CodeInvalidArgument, "duplicate workflow cleanup attempt")
		}
		attempts[a.ID] = a
	}
	changed := false
	now := time.Now().UTC()
	for _, job := range steps {
		job.Status, job.Reason, job.Output = "failed", reason, json.RawMessage(`null`)
		child, err := scanAgentNode(tx.QueryRowContext(ctx, agentNodeSelect+` WHERE id=?`, job.AgentID))
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return e, err
		}
		if err == nil {
			var owned int
			err = tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM agent_nodes n JOIN agent_nodes p ON p.id=n.parent_id AND p.role='root' AND p.run_id=n.run_id JOIN runs r ON r.id=n.run_id JOIN missions m ON m.id=r.mission_id JOIN sessions s ON s.id=n.session_id AND s.workspace_id=m.workspace_id WHERE n.id=? AND n.run_id=? AND n.session_id=? AND n.role='specialist'`, child.ID, run.ID, job.SessionID).Scan(&owned)
			if err != nil {
				return e, err
			}
			if owned != 1 {
				return e, apperror.New(apperror.CodeConflict, "workflow cleanup child ownership changed")
			}
			if !child.Terminal() {
				if lease == nil {
					current, found, err := getRunExecutionLeaseTx(ctx, tx, run.ID)
					if err != nil {
						return e, err
					}
					if !run.Terminal() || found && current.ActiveAt(now) {
						return e, apperror.New(apperror.CodeConflict, "workflow cleanup requires exclusive Run ownership")
					}
				}
				if child.ActiveAttemptID != "" {
					a, ok := attempts[child.ActiveAttemptID]
					if !ok || lease == nil || a.RunID != run.ID || a.AgentID != child.ID || a.ParentAgentID != child.ParentID {
						return e, apperror.New(apperror.CodeConflict, "workflow cleanup active attempt changed")
					}
					waiting, err := workflowLifecycleAttemptTx(ctx, tx, run.ID, *lease, a)
					if err != nil {
						return e, err
					}
					code, detail := "workflow_failed", reason
					if !waiting {
						code, detail = "worker_lost", reason+"; previous worker stopped before its result was known"
					}
					if _, err = interruptAgentAttemptTx(ctx, tx, run, child, code, detail, now); err != nil {
						return e, err
					}
					delete(attempts, a.ID)
				} else if child.Status != domain.AgentReady && child.Status != domain.AgentWaiting {
					return e, apperror.New(apperror.CodeConflict, "workflow cleanup cannot retire an unknown running attempt")
				}
				if _, err = tx.ExecContext(ctx, `UPDATE agent_nodes SET status='cancelled',status_reason=?,active_attempt_id='',finished_at=?,updated_at=?,version=version+1 WHERE id=? AND version=?`, reason, ts(now), ts(now), child.ID, child.Version); err != nil {
					return e, err
				}
				if _, err = tx.ExecContext(ctx, `UPDATE sessions SET status='archived',updated_at=? WHERE id=?`, ts(now), child.SessionID); err != nil {
					return e, err
				}
				if err = appendSupervisorEventTx(ctx, tx, run, events.AgentStatusChangedEvent, "workflow", child.ID, map[string]any{"to": domain.AgentCancelled, "reason": reason, "version": child.Version + 1}); err != nil {
					return e, err
				}
				changed = true
			} else if child.ActiveAttemptID != "" {
				return e, apperror.New(apperror.CodeConflict, "terminal workflow child still owns an active attempt")
			}
			// Preserve a completed child's native report, including unsuccessful outcomes.
			completion, err := scanAgentCompletion(tx.QueryRowContext(ctx, agentCompletionSelect+` WHERE completion.agent_id=?`, child.ID))
			if err != nil && !errors.Is(err, sql.ErrNoRows) {
				return e, err
			}
			if err == nil {
				if !child.Terminal() || completion.RunID != run.ID || completion.ParentAgentID != child.ParentID {
					return e, apperror.New(apperror.CodeConflict, "workflow child completion ownership changed")
				}
				step, found := spec.Step(job.StepID)
				if !found {
					return e, apperror.New(apperror.CodeConflict, "workflow cleanup step definition changed")
				}
				if !step.RequiresCodeVerification() {
					job.Status, job.Reason = "completed", ""
				}
				job.Output, err = json.Marshal(completion.Report)
				if err != nil {
					return e, err
				}
			}
		}
		if _, err = tx.ExecContext(ctx, `UPDATE workflow_steps SET status=?,reason=?,output_json=? WHERE id=? AND execution_id=? AND status='pending'`, job.Status, job.Reason, string(job.Output), job.ID, e.ID); err != nil {
			return e, err
		}
	}

	if len(attempts) > 0 {
		return e, apperror.New(apperror.CodeConflict, "workflow cleanup attempt no longer belongs to a pending step")
	}
	if changed {
		if _, err = createAgentGraphSnapshotTx(ctx, tx, run); err != nil {
			return e, err
		}
	}
	// Frozen items that were never admitted have no child reservation to retire.
	// Also harvest rows committed just before an earlier checkpoint was lost.
	for key, group := range e.State.Maps {
		for i, item := range group.Items {
			if item.Status != "pending" {
				continue
			}
			job, readErr := scanWorkflowStep(tx.QueryRowContext(ctx, workflowStepSelect+` WHERE id=? AND execution_id=?`, item.StepExecutionID, e.ID))
			if readErr != nil && !errors.Is(readErr, sql.ErrNoRows) {
				return e, readErr
			}
			group.Items[i].Status, group.Items[i].Reason = "failed", reason
			if readErr == nil {
				group.Items[i].Status, group.Items[i].Reason = job.Status, job.Reason
				if job.Status == "completed" {
					e.State.Jobs++
				}
			}
		}
		e.State.Maps[key] = group
	}

	e.Status, e.Reason, e.State.WaitFingerprint = "failed", reason, ""
	e.State.Blocked = nil
	state, err := json.Marshal(e.State)
	if err != nil {
		return e, err
	}
	if len(state) > 2*1024*1024 {
		return e, apperror.New(apperror.CodeResourceExhausted, "workflow checkpoint exceeds 2 MiB")
	}
	result, err := tx.ExecContext(ctx, `UPDATE workflow_executions SET status='failed',reason=?,state_json=?,version=version+1,updated_at=? WHERE id=? AND version=? AND claim_owner=? AND claim_until>?`, reason, string(state), ts(now), id, revision, owner, ts(time.Now().UTC()))
	if err != nil {
		return e, err
	}
	if err = requireSingleLeaseUpdate(result, "workflow lifecycle lost its claim before settlement"); err != nil {
		return e, err
	}
	if err = tx.Commit(); err != nil {
		return e, err
	}
	return s.GetWorkflowExecution(ctx, id)
}
