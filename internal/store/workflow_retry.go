package store

import (
	"context"
	"encoding/json"
	"strings"
	"time"

	"cyberagent-workbench/internal/apperror"
	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/runmutation"
)

// This transaction is the dispatch boundary for one host-owned round. A wave
// accepted before a pause may settle, but an old claim cannot accept a new one.
func (s *SQLiteStore) ValidateWorkflowDispatch(ctx context.Context, executionID, owner string, revision int64, stepIDs []string) error {
	return s.validateWorkflowSteps(ctx, executionID, owner, revision, stepIDs, true)
}

func (s *SQLiteStore) ValidateWorkflowSettlement(ctx context.Context, executionID, owner string, revision int64, stepIDs []string) error {
	return s.validateWorkflowSteps(ctx, executionID, owner, revision, stepIDs, false)
}

func (s *SQLiteStore) validateWorkflowSteps(ctx context.Context, executionID, owner string, revision int64, stepIDs []string, running bool) error {
	if len(stepIDs) < 1 || len(stepIDs) > 2 {
		return apperror.New(apperror.CodeInvalidArgument, "workflow round requires one or two durable steps")
	}
	args := []any{executionID, owner, revision, ts(time.Now().UTC())}
	marks := make([]string, len(stepIDs))
	for i, id := range stepIDs {
		marks[i] = "?"
		args = append(args, id)
	}
	var count int
	condition := ""
	if running {
		condition = " AND r.status='running'"
	}
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM workflow_steps w JOIN workflow_executions e ON e.id=w.execution_id JOIN runs r ON r.id=e.run_id
 WHERE e.id=? AND e.claim_owner=? AND e.version=? AND e.claim_until>? AND e.status IN ('running','waiting','waiting_approval')`+condition+` AND w.id IN (`+strings.Join(marks, ",")+`)`, args...).Scan(&count)
	if err != nil {
		return err
	}
	if count != len(stepIDs) {
		return apperror.New(apperror.CodeConflict, "workflow dispatch lost its revision, claim or running Run")
	}
	return nil
}

func (s *SQLiteStore) RetryFailedWorkflowExecution(ctx context.Context, id string, revision int64, operationKey string) (domain.WorkflowExecution, error) {
	var e domain.WorkflowExecution
	key, err := domain.NormalizeAgentOperationKey(operationKey)
	if err != nil {
		return e, apperror.Wrap(apperror.CodeInvalidArgument, "workflow retry requires an operation key", err)
	}
	if revision < 1 {
		return e, apperror.New(apperror.CodeInvalidArgument, "workflow retry requires its observed revision")
	}
	digest := runmutation.Fingerprint("workflow_retry.v1", id, key)
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return e, err
	}
	defer tx.Rollback()
	// Acquire the same SQLite write lock as other execution controls before reading.
	if _, err = tx.ExecContext(ctx, `UPDATE workflow_executions SET updated_at=updated_at WHERE id=?`, id); err != nil {
		return e, err
	}
	e, err = scanWorkflowExecution(tx.QueryRowContext(ctx, workflowExecutionSelect+` WHERE id=?`, id))
	if err != nil {
		return e, err
	}
	if prior, ok := e.State.RetryOperations[digest]; ok {
		if prior.Revision != revision {
			return e, apperror.New(apperror.CodeConflict, "workflow retry key was used for another revision")
		}
		return e, tx.Commit()
	}
	if e.Version != revision || e.Status != "failed" || e.State.Blocked == nil || !e.State.Blocked.Retryable {
		return e, apperror.New(apperror.CodeConflict, "workflow has no retryable failed items at this revision")
	}
	now := time.Now().UTC()
	var active int
	err = tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM workflow_executions WHERE id=? AND claim_owner<>'' AND claim_until>?`, id, ts(now)).Scan(&active)
	if err != nil {
		return e, err
	}
	if active != 0 {
		return e, apperror.New(apperror.CodeConflict, "workflow retry must wait for its interpreter claim")
	}
	lease, found, err := getRunExecutionLeaseTx(ctx, tx, e.RunID)
	if err != nil {
		return e, err
	}
	if found && lease.ActiveAt(now) {
		return e, apperror.New(apperror.CodeConflict, "workflow retry must wait for the Run lease")
	}
	run, _, err := getCoordinatorRunTx(ctx, tx, e.RunID)
	if err != nil {
		return e, err
	}
	if run.Status != domain.RunRunning {
		return e, apperror.New(apperror.CodeFailedPrecondition, "workflow retry requires a running Run")
	}
	var raw string
	if err = tx.QueryRowContext(ctx, `SELECT spec_json FROM workflow_definitions WHERE id=?`, e.DefinitionID).Scan(&raw); err != nil {
		return e, err
	}
	spec, err := domain.DecodeWorkflowSpec([]byte(raw))
	if err != nil {
		return e, err
	}
	group, ok := e.State.Maps[e.State.Blocked.Key]
	if !ok || group.StepID != e.State.Blocked.StepID {
		return e, apperror.New(apperror.CodeFailedPrecondition, "workflow retry has no frozen task group")
	}
	step, ok := spec.Step(group.StepID)
	if !ok || step.Task == nil {
		return e, apperror.New(apperror.CodeFailedPrecondition, "workflow retry task is absent")
	}
	if !now.Before(e.CreatedAt.Add(time.Duration(spec.Limits.TimeoutMillis) * time.Millisecond)) {
		return e, apperror.New(apperror.CodeFailedPrecondition, "workflow deadline exceeded")
	}
	failed := 0
	for _, item := range group.Items {
		if item.Status == "pending" {
			return e, apperror.New(apperror.CodeConflict, "workflow pending items and approvals retain their original attempt")
		}
		if item.Status != "failed" {
			continue
		}
		failed++
		job, err := scanWorkflowStep(tx.QueryRowContext(ctx, workflowStepSelect+` WHERE id=?`, item.StepExecutionID))
		if err != nil {
			return e, err
		}
		if job.ExecutionID != e.ID || job.StepID != step.ID || job.Status != "failed" || item.Generation >= step.AttemptLimit() {
			return e, apperror.New(apperror.CodeFailedPrecondition, "workflow failed attempt is not retryable")
		}
	}
	usage, err := getRunAgentUsageTx(ctx, tx, e.RunID)
	if err != nil {
		return e, err
	}
	var turns int64
	if err = tx.QueryRowContext(ctx, `SELECT COALESCE(SUM(turns_used),0) FROM agent_nodes WHERE run_id=?`, e.RunID).Scan(&turns); err != nil {
		return e, err
	}
	if failed == 0 || max(e.State.AllocatedJobs, e.State.Jobs)+failed > spec.Limits.MaxJobs || int64(failed)*step.Task.TokenLimit > spec.Limits.MaxTokens-(usage.TotalTokens-e.UsageBefore.TotalTokens) || int64(failed)*step.Task.TurnLimit > spec.Limits.MaxTurns-(turns-e.TurnsBefore) {
		return e, apperror.New(apperror.CodeResourceExhausted, "workflow retry exceeds its cumulative job, token or turn budget")
	}
	if run.Budget.MaxTokens > 0 && usage.TotalTokens+int64(failed)*step.Task.TokenLimit >= run.Budget.MaxTokens || turns+int64(failed)*step.Task.TurnLimit >= int64(run.Budget.MaxTurns) {
		return e, apperror.New(apperror.CodeResourceExhausted, "workflow retry exceeds its remaining Run budget")
	}
	if run.Budget.TimeoutSeconds > 0 && usage.TotalExecutionMillis >= run.Budget.TimeoutSeconds*1000 {
		return e, apperror.New(apperror.CodeResourceExhausted, "workflow retry exceeds its remaining Run execution time")
	}
	if len(e.State.RetryOperations) >= domain.MaxWorkflowJobs {
		return e, apperror.New(apperror.CodeResourceExhausted, "workflow retry operation bound exhausted")
	}
	for i := range group.Items {
		item := &group.Items[i]
		if item.Status != "failed" {
			continue
		}
		item.Generation++
		item.StepExecutionID = domain.WorkflowItemStepID(e.State.Blocked.Key, group.Kind, item.Index, item.Generation)
		item.Status, item.Reason = "pending", ""
	}
	e.State.Maps[e.State.Blocked.Key] = group
	e.State.AllocatedJobs = max(e.State.AllocatedJobs, e.State.Jobs) + failed
	if e.State.RetryOperations == nil {
		e.State.RetryOperations = map[string]domain.WorkflowRetryOperation{}
	}
	e.State.RetryOperations[digest] = domain.WorkflowRetryOperation{Revision: revision, Key: e.State.Blocked.Key}
	e.State.Blocked = nil
	e.State.WaitFingerprint = ""
	e.Status, e.Reason = "running", ""
	state, err := json.Marshal(e.State)
	if err != nil {
		return e, err
	}
	if len(state) > 2*1024*1024 {
		return e, apperror.New(apperror.CodeResourceExhausted, "workflow checkpoint exceeds 2 MiB")
	}
	_, err = tx.ExecContext(ctx, `UPDATE workflow_executions SET status='running',reason='',state_json=?,version=version+1,claim_owner='',claim_until='',updated_at=? WHERE id=? AND version=?`, string(state), ts(now), e.ID, revision)
	if err != nil {
		return e, err
	}
	e, err = scanWorkflowExecution(tx.QueryRowContext(ctx, workflowExecutionSelect+` WHERE id=?`, e.ID))
	if err != nil {
		return e, err
	}
	return e, tx.Commit()
}
