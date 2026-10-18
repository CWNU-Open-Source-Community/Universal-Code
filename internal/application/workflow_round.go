package application

import (
	"context"
	"time"

	"cyberagent-workbench/internal/apperror"
	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/waitgraph"
)

type workflowRoundItem struct {
	job      domain.WorkflowStepExecution
	request  ExecuteSpecialistRequest
	deadline time.Time
}

// One workflow wave borrows the existing child runner and one Run lease. Each
// child has its own runner/operation identity; child errors do not cancel peers.
func (s *WorkflowService) executeWorkflowRound(ctx context.Context, e domain.WorkflowExecution, owner string, bounds domain.WorkflowLimits, items []workflowRoundItem) ([]specialistRoundOutcome, error) {
	base := NewSubagentRunner(s.workers.store, s.workers.router, s.workers.checker)
	outcomes := []specialistRoundOutcome{}
	err := withRunExecutionLease(ctx, s.store, e.RunID, base.leaseOwner, base.leasePolicy, func(leaseCtx context.Context, lease domain.RunExecutionLease) error {
		if _, err := s.store.RecoverSpecialistAttempts(leaseCtx, lease); err != nil {
			return err
		}
		ids := make([]string, len(items))
		for i, item := range items {
			ids[i] = item.job.ID
		}
		if err := s.store.ValidateWorkflowDispatch(leaseCtx, e.ID, owner, e.Version, ids); err != nil {
			return err
		}
		run, err := s.store.GetRun(leaseCtx, e.RunID)
		if err != nil {
			return err
		}
		usage, err := s.store.GetRunAgentUsage(leaseCtx, e.RunID)
		if err != nil {
			return err
		}
		nodes, err := s.store.ListAgentNodes(leaseCtx, e.RunID)
		if err != nil {
			return err
		}
		remainingTokens := bounds.MaxTokens - (usage.TotalTokens - e.UsageBefore.TotalTokens)
		if run.Budget.MaxTokens > 0 {
			remainingTokens = min(remainingTokens, run.Budget.MaxTokens-usage.TotalTokens)
		}
		remainingTurns := bounds.MaxTurns - (workflowTurns(nodes) - e.TurnsBefore)
		children := make([]domain.AgentNode, 0, len(items))
		selected := make([]workflowRoundItem, 0, len(items))
		newTurns := int64(0)
		for _, item := range items {
			child, err := s.store.GetAgentNode(leaseCtx, item.job.AgentID)
			if err != nil {
				return err
			}
			if child.Terminal() {
				continue
			}
			if child.ActiveAttemptID == "" {
				newTurns++
			}
			children = append(children, child)
			selected = append(selected, item)
		}
		if len(selected) == 0 {
			return nil
		}
		if newTurns > remainingTurns || remainingTokens < int64(len(selected)) {
			return apperror.New(apperror.CodeResourceExhausted, "workflow round exhausted its cumulative token or turn budget")
		}
		budget := run.Budget
		budget.MaxTokens = usage.TotalTokens + remainingTokens
		limits, scheduled, reason := specialistRoundLimits(budget, usage, children)
		if reason != "" {
			return apperror.New(apperror.CodeResourceExhausted, string(reason))
		}
		allowed := map[string]bool{}
		for _, child := range scheduled {
			allowed[child.ID] = true
		}
		ch := make(chan specialistRoundOutcome, len(selected))
		started := 0
		for i, item := range selected {
			child := children[i]
			if !allowed[child.ID] {
				continue
			}
			started++
			go func(index int, item workflowRoundItem, child domain.AgentNode) {
				childCtx, cancel := context.WithDeadline(leaseCtx, item.deadline)
				defer cancel()
				childCtx, release, err := waitgraph.Enter(childCtx, waitgraph.Default(), waitgraph.Agent(child.ParentID), waitgraph.Agent(child.ID))
				if err != nil {
					ch <- specialistRoundOutcome{index: index, agentID: child.ID, err: apperror.New(apperror.CodeConflict, err.Error())}
					return
				}
				defer release()
				runner := NewSubagentRunner(s.workers.store, s.workers.router, s.workers.checker).WithRunWorktree(s.workers.drydocks).WithWorkerRuntime(s.workers.workerRuntime)
				runner.operationKey = item.request.OperationKey
				if item.request.WorkspaceTools {
					runner.WithWorkspaceTools()
				}
				if st, ok := s.workers.store.(MonetaryBudgetStore); ok {
					runner.WithMonetaryBudget(NewMonetaryBudgetService(st))
				}
				result := SpecialistTurnResult{RunID: child.RunID, AgentID: child.ID, ParentAgentID: child.ParentID, SessionID: child.SessionID}
				replayed, err := runner.operationReplay(childCtx, &result)
				if !replayed && err == nil {
					result, err = NewSpecialistScheduler(runner).runSpecialistChild(childCtx, lease, child, limits[child.ID])
				}
				ch <- specialistRoundOutcome{index: index, agentID: child.ID, result: result, err: err}
			}(i, item, child)
		}
		for i := 0; i < started; i++ {
			outcomes = append(outcomes, <-ch)
		}
		return nil
	})
	return outcomes, err
}
