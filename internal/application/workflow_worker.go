package application

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"cyberagent-workbench/internal/apperror"
	"cyberagent-workbench/internal/idgen"
	"cyberagent-workbench/internal/scheduler"
)

// Run owns the host lifetime, independently of the request that saved or resumed
// an execution. The existing scheduler supplies polling and serial dispatch;
// workflow claims and the worker's Run lease provide cross-host exclusion.
func (s *WorkflowService) Run(ctx context.Context) error {
	if s == nil || s.store == nil || s.workers == nil {
		return apperror.New(apperror.CodeFailedPrecondition, "workflow requires its worker execution service")
	}
	worker, err := scheduler.NewWorker(s, scheduler.WorkerConfig{OnError: func(err error) {
		slog.Error("workflow worker could not advance an execution", "error", err)
	}})
	if err != nil {
		return err
	}
	return worker.Run(ctx)
}

// RunDue stops after one successful advancement. Keyset paging skips dormant
// waits without letting a page of them hide later runnable work. Checkpoints
// update UpdatedAt, so successive executions receive turns in oldest-first order.
func (s *WorkflowService) RunDue(ctx context.Context, owner string, now time.Time) (bool, error) {
	if s == nil || s.store == nil || s.workers == nil {
		return false, apperror.New(apperror.CodeFailedPrecondition, "workflow requires its worker execution service")
	}
	if !validControlIdentity(owner) || now.IsZero() {
		return false, apperror.New(apperror.CodeInvalidArgument, "workflow worker requires an owner and current time")
	}
	var afterTime time.Time
	var afterID string
	var attempted bool
	var failures error
	for {
		candidates, err := s.store.ListWorkflowCandidates(ctx, afterTime, afterID, now, 32)
		if err != nil || len(candidates) == 0 {
			return attempted, errors.Join(failures, err)
		}
		for _, execution := range candidates {
			afterTime, afterID = execution.UpdatedAt, execution.ID
			definition, err := s.store.GetWorkflowDefinition(ctx, execution.DefinitionID)
			if err != nil {
				failures = errors.Join(failures, err)
				continue
			}
			expired := !now.Before(execution.CreatedAt.Add(time.Duration(definition.Spec.Limits.TimeoutMillis) * time.Millisecond))
			if !expired {
				lease, found, err := s.store.GetRunExecutionLease(ctx, execution.RunID)
				if err != nil {
					failures = errors.Join(failures, err)
					continue
				}
				if found && lease.ActiveAt(now) {
					continue
				}
			}
			if execution.Status != "running" && execution.State.WaitFingerprint != "" {
				if !expired {
					wake, err := s.store.WorkflowWakeFingerprint(ctx, execution.RunID, now)
					if err != nil {
						failures = errors.Join(failures, err)
						continue
					}
					if wake == execution.State.WaitFingerprint {
						continue
					}
				}
			}
			// Each acquisition has a fresh fencing identity, even when the host's
			// scheduler owner stays the same after an earlier claim expires.
			claimOwner := owner + "-" + idgen.New("workflowclaim")
			claimed, acquired, err := s.store.ClaimWorkflowExecution(ctx, execution.ID, claimOwner)
			if err != nil {
				failures = errors.Join(failures, err)
				continue
			}
			if !acquired {
				continue
			}
			_, err = s.runClaimed(ctx, claimed, claimOwner, execution.Version)
			attempted = true
			if err == nil || ctx.Err() != nil {
				return true, errors.Join(failures, err)
			}
			// A storage failure has no durable wake signal. Leave the execution
			// retryable and give later candidates a chance during this same tick.
			failures = errors.Join(failures, err)
		}
	}
}
