package application

import (
	"context"
	"time"

	"cyberagent-workbench/internal/apperror"
	"cyberagent-workbench/internal/domain"
)

// Lifecycle settlement never resumes a worker or recovers a lost attempt. A
// terminal child only needs its durable result projected into the pending step.
func (s *WorkflowService) finalizeWorkflowLifecycle(ctx context.Context, e domain.WorkflowExecution, owner string) (domain.WorkflowExecution, error) {
	steps, err := s.store.ListWorkflowLifecycleSteps(ctx, e.ID, owner, e.Version)
	if err != nil {
		return e, err
	}
	needsLease := false
	for _, job := range steps {
		child, err := s.store.GetAgentNode(ctx, job.AgentID)
		if apperror.CodeOf(apperror.Normalize(err)) == apperror.CodeNotFound {
			continue
		}
		if err != nil {
			return e, err
		}
		needsLease = needsLease || !child.Terminal()
	}
	settle := func(ctx context.Context, lease *domain.RunExecutionLease) (domain.WorkflowExecution, error) {
		var attempts []domain.AgentAttempt
		for _, job := range steps {
			child, err := s.store.GetAgentNode(ctx, job.AgentID)
			if apperror.CodeOf(apperror.Normalize(err)) == apperror.CodeNotFound {
				continue
			}
			if err != nil {
				return e, err
			}
			if child.Terminal() || child.ActiveAttemptID == "" {
				continue
			}
			if lease == nil {
				return e, apperror.New(apperror.CodeConflict, "workflow cleanup must wait for its active attempt")
			}
			ref := domain.AgentAttemptRef{RunID: e.RunID, AgentID: child.ID, AttemptID: child.ActiveAttemptID}
			attempt, found, err := s.store.GetAgentAttempt(ctx, ref.AttemptID)
			if err != nil {
				return e, err
			}
			if !found {
				return e, apperror.New(apperror.CodeConflict, "workflow cleanup attempt disappeared")
			}
			if err = s.store.ValidateWorkflowLifecycleAttempt(ctx, e.ID, owner, e.Version, *lease, attempt); err != nil {
				return e, err
			}
			if s.workers.workerRuntime == nil {
				if child.Worker != nil {
					return e, apperror.New(apperror.CodeUnavailable, "workflow cleanup runtime is unavailable")
				}
			} else if err = s.workers.workerRuntime.closeWorkerResources(ref); err != nil {
				return e, err
			}
			attempts = append(attempts, attempt)
		}
		return s.store.FinalizeWorkflowLifecycle(ctx, e.ID, owner, e.Version, lease, attempts)
	}
	run, err := s.store.GetRun(ctx, e.RunID)
	if err != nil {
		return e, err
	}
	if !needsLease || run.Terminal() {
		return settle(ctx, nil)
	}
	current, found, err := s.store.GetRunExecutionLease(ctx, e.RunID)
	if err != nil {
		return e, err
	}
	// Another live owner keeps its resources. The saved deadline remains due,
	// so the host will retry after that owner releases or loses its lease.
	if found && current.ActiveAt(time.Now().UTC()) {
		return e, nil
	}
	base := NewSubagentRunner(s.workers.store, s.workers.router, s.workers.checker)
	err = withRunExecutionLease(ctx, s.store, e.RunID, base.leaseOwner, base.leasePolicy, func(ctx context.Context, lease domain.RunExecutionLease) error {
		var err error
		e, err = settle(ctx, &lease)
		return err
	})
	return e, err
}
