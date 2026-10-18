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
	"cyberagent-workbench/internal/runmutation"
)

func (s *SQLiteStore) SaveWorkflowDefinition(ctx context.Context, spec domain.WorkflowSpec) (domain.WorkflowDefinition, error) {
	spec, err := domain.NormalizeWorkflowSpec(spec)
	if err != nil {
		return domain.WorkflowDefinition{}, apperror.Wrap(apperror.CodeInvalidArgument, "invalid workflow", err)
	}
	raw, err := json.Marshal(spec)
	if err != nil {
		return domain.WorkflowDefinition{}, err
	}
	digest := runmutation.Fingerprint(domain.WorkflowVersion, string(raw))
	id := "workflow-" + digest
	_, err = s.db.ExecContext(ctx, `INSERT INTO workflow_definitions(id,digest,spec_json,created_at) VALUES(?,?,?,?) ON CONFLICT(digest) DO NOTHING`, id, digest, string(raw), ts(time.Now().UTC()))
	if err != nil {
		return domain.WorkflowDefinition{}, err
	}
	return s.GetWorkflowDefinition(ctx, id)
}
func (s *SQLiteStore) GetWorkflowDefinition(ctx context.Context, id string) (domain.WorkflowDefinition, error) {
	var d domain.WorkflowDefinition
	var raw, created string
	err := s.db.QueryRowContext(ctx, `SELECT id,digest,spec_json,created_at FROM workflow_definitions WHERE id=?`, id).Scan(&d.ID, &d.Digest, &raw, &created)
	if errors.Is(err, sql.ErrNoRows) {
		return d, apperror.New(apperror.CodeNotFound, "workflow definition does not exist")
	}
	if err != nil {
		return d, err
	}
	d.CreatedAt = parseTS(created)
	d.Spec, err = domain.DecodeWorkflowSpec([]byte(raw))
	return d, err
}

func (s *SQLiteStore) ListWorkflowDefinitions(ctx context.Context) ([]domain.WorkflowDefinition, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id,digest,spec_json,created_at FROM workflow_definitions ORDER BY created_at DESC,id LIMIT 128`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := []domain.WorkflowDefinition{}
	for rows.Next() {
		var d domain.WorkflowDefinition
		var raw, created string
		if err := rows.Scan(&d.ID, &d.Digest, &raw, &created); err != nil {
			return nil, err
		}
		d.CreatedAt = parseTS(created)
		d.Spec, err = domain.DecodeWorkflowSpec([]byte(raw))
		if err != nil {
			return nil, err
		}
		items = append(items, d)
	}
	return items, rows.Err()
}
func (s *SQLiteStore) StartWorkflowExecution(ctx context.Context, e domain.WorkflowExecution) (domain.WorkflowExecution, error) {
	if !domain.ValidAgentID(e.ID) || !domain.ValidAgentID(e.RunID) || len(e.Inputs) > 32*1024 || !json.Valid(e.Inputs) {
		return e, apperror.New(apperror.CodeInvalidArgument, "workflow execution requires identities and bounded JSON inputs")
	}
	e.State = domain.WorkflowState{Stack: []domain.WorkflowFrame{{}}, Outputs: map[string]json.RawMessage{}, Iterations: map[string]int{}}
	state, _ := json.Marshal(e.State)
	usage, _ := json.Marshal(e.UsageBefore)
	now := ts(time.Now().UTC())
	_, err := s.db.ExecContext(ctx, `INSERT INTO workflow_executions(id,definition_id,run_id,inputs_json,status,state_json,usage_before_json,turns_before,created_at,updated_at) VALUES(?,?,?,?,'running',?,?,?,?,?) ON CONFLICT(id) DO NOTHING`, e.ID, e.DefinitionID, e.RunID, string(e.Inputs), string(state), string(usage), e.TurnsBefore, now, now)
	if err != nil {
		return e, err
	}
	stored, err := s.GetWorkflowExecution(ctx, e.ID)
	if err != nil {
		return e, err
	}
	if stored.DefinitionID != e.DefinitionID || stored.RunID != e.RunID || string(stored.Inputs) != string(e.Inputs) {
		return e, apperror.New(apperror.CodeConflict, "workflow start key was used for different inputs")
	}
	return stored, nil
}

const workflowExecutionSelect = `SELECT id,definition_id,run_id,inputs_json,status,reason,state_json,usage_before_json,version,created_at,updated_at,turns_before FROM workflow_executions`

func scanWorkflowExecution(row scanner) (domain.WorkflowExecution, error) {
	var e domain.WorkflowExecution
	var input, state, usage, created, updated string
	if err := row.Scan(&e.ID, &e.DefinitionID, &e.RunID, &input, &e.Status, &e.Reason, &state, &usage, &e.Version, &created, &updated, &e.TurnsBefore); err != nil {
		return e, err
	}
	e.Inputs = json.RawMessage(input)
	e.CreatedAt = parseTS(created)
	e.UpdatedAt = parseTS(updated)
	if err := json.Unmarshal([]byte(state), &e.State); err != nil {
		return e, err
	}
	err := json.Unmarshal([]byte(usage), &e.UsageBefore)
	return e, err
}
func (s *SQLiteStore) GetWorkflowExecution(ctx context.Context, id string) (domain.WorkflowExecution, error) {
	e, err := scanWorkflowExecution(s.db.QueryRowContext(ctx, workflowExecutionSelect+` WHERE id=?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return e, apperror.New(apperror.CodeNotFound, "workflow execution does not exist")
	}
	return e, err
}
func (s *SQLiteStore) ListWorkflowExecutions(ctx context.Context, runID string) ([]domain.WorkflowExecution, error) {
	rows, err := s.db.QueryContext(ctx, workflowExecutionSelect+` WHERE run_id=? ORDER BY created_at DESC,id LIMIT 128`, runID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []domain.WorkflowExecution{}
	for rows.Next() {
		e, err := scanWorkflowExecution(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// Claims serialize interpreter calls across processes, independently of the Run
// lease acquired by the real worker runner. One Advance is bounded to 10 minutes.
func (s *SQLiteStore) ClaimWorkflowExecution(ctx context.Context, id, owner string) (domain.WorkflowExecution, bool, error) {
	now := time.Now().UTC()
	result, err := s.db.ExecContext(ctx, `UPDATE workflow_executions SET claim_owner=?,claim_until=? WHERE id=? AND status IN ('running','waiting','waiting_approval') AND (claim_owner='' OR claim_until<=?)`, owner, ts(now.Add(15*time.Minute)), id, ts(now))
	if err != nil {
		return domain.WorkflowExecution{}, false, err
	}
	n, err := result.RowsAffected()
	if err != nil {
		return domain.WorkflowExecution{}, false, err
	}
	e, err := s.GetWorkflowExecution(ctx, id)
	return e, n == 1, err
}
func (s *SQLiteStore) ReleaseWorkflowExecution(ctx context.Context, id, owner string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE workflow_executions SET claim_owner='',claim_until='' WHERE id=? AND claim_owner=?`, id, owner)
	return err
}
func (s *SQLiteStore) CheckpointWorkflowExecution(ctx context.Context, e domain.WorkflowExecution, owner string) (domain.WorkflowExecution, error) {
	state, err := json.Marshal(e.State)
	if err != nil {
		return e, err
	}
	if len(state) > 2*1024*1024 {
		return e, apperror.New(apperror.CodeResourceExhausted, "workflow checkpoint exceeds 2 MiB")
	}
	result, err := s.db.ExecContext(ctx, `UPDATE workflow_executions SET status=?,reason=?,state_json=?,version=version+1,updated_at=? WHERE id=? AND version=? AND claim_owner=? AND claim_until>? AND status IN ('running','waiting','waiting_approval')`, e.Status, e.Reason, string(state), ts(time.Now().UTC()), e.ID, e.Version, owner, ts(time.Now().UTC()))
	if err != nil {
		return e, err
	}
	n, err := result.RowsAffected()
	if err != nil {
		return e, err
	}
	if n != 1 {
		return e, apperror.New(apperror.CodeConflict, "workflow checkpoint was superseded or paused")
	}
	return s.GetWorkflowExecution(ctx, e.ID)
}
func (s *SQLiteStore) ControlWorkflowExecution(ctx context.Context, id, action string, version int64) (domain.WorkflowExecution, error) {
	var query string
	switch action {
	case "pause":
		query = `UPDATE workflow_executions SET status='paused',reason='operator pause',version=version+1,updated_at=? WHERE id=? AND version=? AND status IN ('running','waiting','waiting_approval')`
	case "resume":
		query = `UPDATE workflow_executions SET status='running',reason='',version=version+1,updated_at=? WHERE id=? AND version=? AND status IN ('paused','waiting')`
	default:
		return domain.WorkflowExecution{}, apperror.New(apperror.CodeInvalidArgument, "workflow control must be pause or resume")
	}
	result, err := s.db.ExecContext(ctx, query, ts(time.Now().UTC()), id, version)
	if err != nil {
		return domain.WorkflowExecution{}, err
	}
	n, err := result.RowsAffected()
	if err != nil {
		return domain.WorkflowExecution{}, err
	}
	if n != 1 {
		return domain.WorkflowExecution{}, apperror.New(apperror.CodeConflict, "workflow state changed before control")
	}
	return s.GetWorkflowExecution(ctx, id)
}

const workflowStepSelect = `SELECT id,execution_id,step_id,agent_id,session_id,inputs_json,status,output_json,reason FROM workflow_steps`

func scanWorkflowStep(row scanner) (domain.WorkflowStepExecution, error) {
	var step domain.WorkflowStepExecution
	var input, output string
	err := row.Scan(&step.ID, &step.ExecutionID, &step.StepID, &step.AgentID, &step.SessionID, &input, &step.Status, &output, &step.Reason)
	step.Inputs = json.RawMessage(input)
	if output != "null" {
		step.Output = json.RawMessage(output)
	}
	return step, err
}
func (s *SQLiteStore) GetWorkflowStep(ctx context.Context, id string) (domain.WorkflowStepExecution, bool, error) {
	step, err := scanWorkflowStep(s.db.QueryRowContext(ctx, workflowStepSelect+` WHERE id=?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return step, false, nil
	}
	return step, err == nil, err
}

// ReadWorkflowVerificationClaim is a host-only read bound to the interpreter's
// live claim. It grants no ordinary Agent access to another child's history.
func (s *SQLiteStore) ReadWorkflowVerificationClaim(ctx context.Context, executionID, stepID, owner string, revision int64) (domain.WorkflowExecution, domain.WorkflowStepExecution, error) {
	var e domain.WorkflowExecution
	var step domain.WorkflowStepExecution
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return e, step, err
	}
	defer tx.Rollback()
	var valid int
	err = tx.QueryRowContext(ctx, `SELECT 1 FROM workflow_executions e
	 JOIN workflow_steps w ON w.execution_id=e.id
	 JOIN agent_nodes a ON a.id=w.agent_id AND a.run_id=e.run_id AND a.session_id=w.session_id AND a.role='specialist'
	 JOIN agent_nodes p ON p.id=a.parent_id AND p.run_id=e.run_id AND p.role='root'
	 JOIN runs r ON r.id=e.run_id
	 JOIN missions m ON m.id=r.mission_id
	 JOIN sessions s ON s.id=w.session_id AND s.workspace_id=m.workspace_id
	 WHERE e.id=? AND w.id=? AND e.version=? AND e.claim_owner=? AND e.claim_until>?
	 AND e.status IN ('running','waiting','waiting_approval')`, executionID, stepID, revision, owner, ts(time.Now().UTC())).Scan(&valid)
	if errors.Is(err, sql.ErrNoRows) {
		err = apperror.New(apperror.CodeConflict, "workflow verification lost its claim, revision or child ownership")
	}
	if err != nil {
		return e, step, err
	}
	e, err = scanWorkflowExecution(tx.QueryRowContext(ctx, workflowExecutionSelect+` WHERE id=?`, executionID))
	if err != nil {
		return e, step, err
	}
	step, err = scanWorkflowStep(tx.QueryRowContext(ctx, workflowStepSelect+` WHERE id=?`, stepID))
	return e, step, err
}

func (s *SQLiteStore) ListWorkflowVerificationSteps(ctx context.Context, executionID, owner string, revision int64) ([]domain.WorkflowStepExecution, error) {
	rows, err := s.db.QueryContext(ctx, workflowStepSelect+` WHERE execution_id=? AND EXISTS(
	 SELECT 1 FROM workflow_executions e WHERE e.id=? AND e.version=? AND e.claim_owner=? AND e.claim_until>?
	 AND e.status IN ('running','waiting','waiting_approval')) ORDER BY rowid`, executionID, executionID, revision, owner, ts(time.Now().UTC()))
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
func (s *SQLiteStore) PrepareWorkflowStep(ctx context.Context, step domain.WorkflowStepExecution, owner string) (domain.WorkflowStepExecution, error) {
	if !json.Valid(step.Inputs) || len(step.Inputs) > 32*1024 {
		return step, apperror.New(apperror.CodeInvalidArgument, "workflow step inputs exceed their bound")
	}
	result, err := s.db.ExecContext(ctx, `INSERT INTO workflow_steps(id,execution_id,step_id,agent_id,session_id,inputs_json,status) SELECT ?,?,?,?,?,?,'pending' WHERE EXISTS(SELECT 1 FROM workflow_executions WHERE id=? AND claim_owner=? AND claim_until>? AND status IN ('running','waiting','waiting_approval')) ON CONFLICT(id) DO NOTHING`, step.ID, step.ExecutionID, step.StepID, step.AgentID, step.SessionID, string(step.Inputs), step.ExecutionID, owner, ts(time.Now().UTC()))
	if err != nil {
		return step, err
	}
	_ = result
	stored, found, err := s.GetWorkflowStep(ctx, step.ID)
	if err != nil {
		return step, err
	}
	if !found {
		return step, apperror.New(apperror.CodeConflict, "workflow was paused before dispatch")
	}
	if stored.ExecutionID != step.ExecutionID || stored.StepID != step.StepID || stored.AgentID != step.AgentID || stored.SessionID != step.SessionID || string(stored.Inputs) != string(step.Inputs) {
		return step, apperror.New(apperror.CodeConflict, "workflow step identity was reused")
	}
	return stored, nil
}
func (s *SQLiteStore) CompleteWorkflowStep(ctx context.Context, step domain.WorkflowStepExecution, owner string) error {
	return s.completeWorkflowStep(ctx, step, owner, 0, nil, nil)
}

// Only the host holding the current Run lease may retire a suspended attempt.
// Its captured previous lease can belong to an earlier approval-wait round.
func (s *SQLiteStore) FailWorkflowStepWithLease(ctx context.Context, step domain.WorkflowStepExecution, owner string, revision int64, lease domain.RunExecutionLease, expected domain.AgentAttempt) error {
	if step.Status != "failed" || revision < 1 {
		return apperror.New(apperror.CodeInvalidArgument, "workflow lease cleanup requires a failed step")
	}
	return s.completeWorkflowStep(ctx, step, owner, revision, &lease, &expected)
}

func (s *SQLiteStore) completeWorkflowStep(ctx context.Context, step domain.WorkflowStepExecution, owner string, revision int64, lease *domain.RunExecutionLease, expected *domain.AgentAttempt) error {
	if (step.Status != "completed" && step.Status != "failed") || !json.Valid(step.Output) || len(step.Output) > 32*1024 {
		return apperror.New(apperror.CodeInvalidArgument, "workflow result must be bounded JSON")
	}
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{})
	if err != nil {
		return err
	}
	defer tx.Rollback()
	result, err := tx.ExecContext(ctx, `UPDATE workflow_steps SET status=?,output_json=?,reason=? WHERE id=? AND execution_id=? AND agent_id=? AND session_id=? AND status='pending' AND EXISTS(SELECT 1 FROM workflow_executions WHERE id=? AND claim_owner=? AND claim_until>? AND (?=0 OR version=?) AND (status IN ('running','waiting','waiting_approval') OR (?='completed' AND status='paused')))`, step.Status, string(step.Output), step.Reason, step.ID, step.ExecutionID, step.AgentID, step.SessionID, step.ExecutionID, owner, ts(time.Now().UTC()), revision, revision, step.Status)
	if err != nil {
		return err
	}
	n, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if n != 1 {
		return apperror.New(apperror.CodeConflict, "workflow result lost its execution claim")
	}
	if step.Status == "failed" {
		// A failed workflow relinquishes idle children instead of retaining their
		// reservations forever. Only the current Run lease holder can also end
		// the exact suspended attempt captured before resource cleanup.
		if lease != nil {
			var runID string
			if err = tx.QueryRowContext(ctx, `SELECT run_id FROM workflow_executions WHERE id=?`, step.ExecutionID).Scan(&runID); err != nil {
				return err
			}
			if lease.RunID != runID {
				return apperror.New(apperror.CodeConflict, "workflow cleanup lease belongs to another Run")
			}
			if err = requireRunExecutionLeaseTx(ctx, tx, runID, lease.LeaseID, lease.Generation); err != nil {
				return err
			}
			var active string
			if err = tx.QueryRowContext(ctx, `SELECT active_attempt_id FROM agent_nodes WHERE id=? AND session_id=? AND run_id=?`, step.AgentID, step.SessionID, runID).Scan(&active); err != nil {
				return err
			}
			if expected == nil || active != expected.ID {
				return apperror.New(apperror.CodeConflict, "workflow active attempt changed before cleanup")
			}
		}
		var suspended string
		if expected != nil && expected.ID != "" {
			err = tx.QueryRowContext(ctx, `SELECT n.id FROM agent_nodes n JOIN agent_nodes p ON p.id=n.parent_id AND p.role='root' AND p.run_id=n.run_id JOIN agent_attempts a ON a.id=n.active_attempt_id AND a.status='running' WHERE n.id=? AND n.session_id=? AND n.run_id=(SELECT run_id FROM workflow_executions WHERE id=?) AND n.role='specialist' AND n.status='running' AND a.id=? AND a.lease_id=? AND a.lease_generation=? AND EXISTS(SELECT 1 FROM specialist_tool_calls c WHERE c.agent_attempt_id=a.id AND c.status='pending' AND c.started_at IS NULL AND c.approval_id<>'')`, step.AgentID, step.SessionID, step.ExecutionID, expected.ID, expected.LeaseID, expected.LeaseGeneration).Scan(&suspended)
			if errors.Is(err, sql.ErrNoRows) {
				return apperror.New(apperror.CodeConflict, "workflow approval attempt ownership changed before cleanup")
			}
			if err != nil {
				return err
			}
		}
		if suspended != "" {
			child, err := scanAgentNode(tx.QueryRowContext(ctx, agentNodeSelect+` WHERE id=?`, suspended))
			if err != nil {
				return err
			}
			run, _, err := getCoordinatorRunTx(ctx, tx, child.RunID)
			if err != nil {
				return err
			}
			if _, err = interruptAgentAttemptTx(ctx, tx, run, child, "workflow_failed", step.Reason, time.Now().UTC()); err != nil {
				return err
			}
			if _, err = tx.ExecContext(ctx, `UPDATE agent_nodes SET status='waiting',active_attempt_id='' WHERE id=?`, child.ID); err != nil {
				return err
			}
		}
		now := ts(time.Now().UTC())
		cancelled, err := tx.ExecContext(ctx, `UPDATE agent_nodes SET status='cancelled',status_reason='workflow step failed',finished_at=?,updated_at=?,version=version+1 WHERE id=(SELECT agent_id FROM workflow_steps WHERE id=?) AND status IN ('ready','waiting') AND active_attempt_id=''`, now, now, step.ID)
		if err != nil {
			return err
		}
		if _, err = tx.ExecContext(ctx, `UPDATE sessions SET status='archived',updated_at=? WHERE id=(SELECT session_id FROM agent_nodes WHERE id=(SELECT agent_id FROM workflow_steps WHERE id=?) AND status='cancelled')`, now, step.ID); err != nil {
			return err
		}
		changed, err := cancelled.RowsAffected()
		if err != nil {
			return err
		}
		if changed > 0 {
			child, err := scanAgentNode(tx.QueryRowContext(ctx, agentNodeSelect+` WHERE id=(SELECT agent_id FROM workflow_steps WHERE id=?)`, step.ID))
			if err != nil {
				return err
			}
			run, _, err := getCoordinatorRunTx(ctx, tx, child.RunID)
			if err != nil {
				return err
			}
			if err := appendSupervisorEventTx(ctx, tx, run, events.AgentStatusChangedEvent, "workflow", child.ID, map[string]any{"to": child.Status, "reason": child.StatusReason, "version": child.Version}); err != nil {
				return err
			}
			if _, err := createAgentGraphSnapshotTx(ctx, tx, run); err != nil {
				return err
			}
		}
	}
	return tx.Commit()
}
