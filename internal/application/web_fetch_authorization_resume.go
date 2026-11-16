package application

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"cyberagent-workbench/internal/apperror"
	"cyberagent-workbench/internal/domain"
)

type webFetchAuthorizationResumeStore interface {
	GetWebFetchAuthorization(context.Context, string) (domain.WebFetchAuthorization, error)
	ListRecoverableWebFetchAuthorizations(context.Context, string, int) (
		[]domain.WebFetchAuthorization, error)
	ResumeWebFetchAuthorizationRun(context.Context, string) (domain.Run, bool, error)
}

type webFetchAuthorizationFailureStore interface {
	PrepareWebFetchAuthorizationHandoff(context.Context, string, string, domain.SupervisorPhase) (domain.RunExecutionHandoff, bool, error)
}

type webFetchContinuationModelStore interface {
	GetWebFetchContinuationModelAttempt(context.Context, domain.WebFetchAuthorization, string) (int, error)
}

// ResumeWebFetchAuthorization resumes the exact pending Supervisor call. It
// never creates a new Turn and therefore cannot silently resend user input.
func (s *RunExecutionHandoffService) ResumeWebFetchAuthorization(ctx context.Context,
	runID, authorizationID string,
) (LifecycleResult, bool, error) {
	if s == nil || s.store == nil || s.supervisor == nil {
		return LifecycleResult{}, false, apperror.New(apperror.CodeFailedPrecondition,
			"web fetch authorization resume dependencies are required")
	}
	runID, authorizationID = strings.TrimSpace(runID), strings.TrimSpace(authorizationID)
	if !domain.ValidAgentID(runID) || !domain.ValidAgentID(authorizationID) {
		return LifecycleResult{}, false, apperror.New(apperror.CodeInvalidArgument,
			"web fetch authorization resume identity is invalid")
	}
	store, ok := any(s.store).(webFetchAuthorizationResumeStore)
	if !ok {
		return LifecycleResult{}, false, apperror.New(apperror.CodeFailedPrecondition,
			"web fetch authorization resume store is unavailable")
	}
	value, err := store.GetWebFetchAuthorization(ctx, authorizationID)
	if err != nil {
		return LifecycleResult{}, false, apperror.Normalize(err)
	}
	if value.RunID != runID || value.Status == domain.WebFetchAuthorizationPending {
		return LifecycleResult{}, false, apperror.New(apperror.CodeFailedPrecondition,
			"web fetch authorization is not ready for this Run")
	}
	if err := resumeWebFetchAuthorizationRun(ctx, store, value.ID); err != nil {
		return LifecycleResult{}, false, apperror.Normalize(err)
	}
	resumable, err := s.webFetchAuthorizationTurnResumable(ctx, value)
	if err != nil || !resumable {
		return LifecycleResult{}, !resumable, err
	}
	// A cold Full selection must leave this exact pending tool batch resumable.
	// Recording a failed handoff here would try to seal an unsettled tool turn.
	permission, err := s.store.GetRunExecutionPermission(ctx, runID)
	if err == nil {
		err = requireSupervisorPermissionReady(s.supervisor.executionCapabilities, permission)
	}
	if err != nil {
		return LifecycleResult{}, false, apperror.Normalize(err)
	}
	var execution LifecycleResult
	var continuation domain.RunExecutionHandoff
	if recorder, ok := s.store.(webFetchAuthorizationFailureStore); ok {
		checkpoint, found, readErr := s.store.GetSupervisorCheckpoint(ctx, value.RunID)
		if readErr != nil || !found {
			return execution, false, apperror.Normalize(readErr)
		}
		continuation, _, err = recorder.PrepareWebFetchAuthorizationHandoff(ctx, value.ID,
			checkpoint.AttemptID, domain.SupervisorTurnStarted)
		if err != nil {
			return execution, false, apperror.Normalize(err)
		}
		if continuation.Result != nil {
			return execution, true, storedThreadTurnExecutionError(continuation)
		}
	}
	err = s.supervisor.withRunExecutionLease(ctx, value.RunID,
		func(leaseCtx context.Context, lease domain.RunExecutionLease) error {
			stillResumable, checkErr := s.webFetchAuthorizationTurnResumable(leaseCtx, value)
			if checkErr != nil || !stillResumable {
				return checkErr
			}
			var stepErr error
			execution, stepErr = s.supervisor.stepWithLease(leaseCtx, lease, "")
			if continuation.Operation.ID != "" {
				if execution.ToolCalls > 0 && execution.ModelAttempts == 0 {
					// Like ordinary recovered Supervisor results, handoff flags cover
					// the logical turn. A resumed stored tool batch may reach another
					// approval before any NEW model request. Require the exact original
					// model completion; do not infer one merely from ToolCalls > 0.
					provenance, ok := s.store.(webFetchContinuationModelStore)
					if !ok || execution.Handle.RunID != value.RunID || execution.Turn != value.SupervisorTurn {
						return errors.Join(stepErr, apperror.New(apperror.CodeFailedPrecondition,
							"Web fetch continuation model provenance is unavailable"))
					}
					modelAttempt, proofErr := provenance.GetWebFetchContinuationModelAttempt(leaseCtx, value, execution.AttemptID)
					if proofErr != nil {
						return errors.Join(stepErr, apperror.Normalize(proofErr))
					}
					if modelAttempt <= 0 {
						return errors.Join(stepErr, apperror.New(apperror.CodeFailedPrecondition,
							"Web fetch continuation has no matching completed model attempt"))
					}
					execution.ModelAttempts = modelAttempt
				}
				if recordErr := s.completeWebFetchContinuation(leaseCtx, lease, &continuation, execution, stepErr, ""); recordErr != nil {
					return errors.Join(stepErr, recordErr)
				}
			}
			return stepErr
		})
	if err != nil {
		if failureStore, ok := s.store.(threadTurnFailureStore); ok && continuation.Result != nil {
			if failure, closed, closeErr := closeFailedProductTurn(ctx, failureStore, value.ThreadID, continuation); closeErr != nil {
				return execution, false, closeErr
			} else if closed {
				return execution, false, failedProductTurnError(failure)
			}
		}
		return execution, false, apperror.Normalize(err)
	}
	if execution.Turn == 0 {
		return LifecycleResult{}, true, nil
	}
	return execution, false, nil
}

func (s *RunExecutionHandoffService) completeWebFetchContinuation(ctx context.Context, lease domain.RunExecutionLease,
	handoff *domain.RunExecutionHandoff, execution LifecycleResult, cause error, stopReason string,
) error {
	settleCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Second)
	defer cancel()
	status, errorCode := domain.RunExecutionHandoffCompleted, ""
	if cause != nil {
		status, errorCode = domain.RunExecutionHandoffFailed, strings.ToLower(string(apperror.CodeOf(apperror.Normalize(cause))))
	}
	if stopReason == "" {
		stopReason = "web_fetch_continuation"
		if cause != nil {
			stopReason = errorCode
		}
	}
	steps := 0
	if execution.Turn > 0 {
		steps = 1
	}
	result, _, err := s.store.CompleteRunExecutionHandoff(settleCtx, handoff.Operation.ID, lease,
		status, stopReason, errorCode, steps, execution.ModelAttempts > 0, execution.ToolCalls > 0)
	if err == nil {
		handoff.Result = &result
	}
	return apperror.Normalize(err)
}

func resumeWebFetchAuthorizationRun(ctx context.Context,
	store webFetchAuthorizationResumeStore, authorizationID string,
) error {
	deadline := time.Now().Add(2 * time.Second)
	for {
		_, _, err := store.ResumeWebFetchAuthorizationRun(ctx, authorizationID)
		if err == nil {
			return nil
		}
		code := apperror.CodeOf(apperror.Normalize(err))
		if (code != apperror.CodeFailedPrecondition && code != apperror.CodeConflict) ||
			!strings.Contains(strings.ToLower(err.Error()), "active execution lease") ||
			time.Now().After(deadline) {
			return err
		}
		timer := time.NewTimer(25 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
}

// ReconcileWebFetchAuthorizations replays durable decided-but-unobserved
// approvals after a process interruption. It never invents a call: the store
// returns only exact pending Supervisor calls still bound to their checkpoint.
func (s *RunExecutionHandoffService) ReconcileWebFetchAuthorizations(ctx context.Context,
	runID string, limit int,
) (int, error) {
	if s == nil || s.store == nil {
		return 0, apperror.New(apperror.CodeFailedPrecondition,
			"web fetch authorization reconciliation is unavailable")
	}
	store, ok := any(s.store).(webFetchAuthorizationResumeStore)
	if !ok {
		return 0, apperror.New(apperror.CodeFailedPrecondition,
			"web fetch authorization reconciliation is unavailable")
	}
	values, err := store.ListRecoverableWebFetchAuthorizations(ctx,
		strings.TrimSpace(runID), limit)
	if err != nil {
		return 0, apperror.Normalize(err)
	}
	completed := 0
	var joined error
	for _, value := range values {
		if _, _, resumeErr := s.ResumeWebFetchAuthorization(ctx, value.RunID, value.ID); resumeErr != nil {
			joined = errors.Join(joined, fmt.Errorf("authorization %s: %w", value.ID,
				resumeErr))
			continue
		}
		completed++
	}
	return completed, joined
}

func (s *RunExecutionHandoffService) webFetchAuthorizationTurnResumable(ctx context.Context,
	value domain.WebFetchAuthorization,
) (bool, error) {
	run, err := s.store.GetRun(ctx, value.RunID)
	if err != nil {
		return false, apperror.Normalize(err)
	}
	if run.Status != domain.RunRunning {
		return false, nil
	}
	checkpoint, found, err := s.store.GetSupervisorCheckpoint(ctx, value.RunID)
	if err != nil {
		return false, apperror.Normalize(err)
	}
	if !found || checkpoint.Phase != domain.SupervisorTurnStarted ||
		checkpoint.NextTurn != value.SupervisorTurn {
		return false, nil
	}
	rounds, err := s.store.ListSupervisorToolRounds(ctx, checkpoint)
	if err != nil {
		return false, apperror.Normalize(err)
	}
	for _, round := range rounds {
		for _, call := range round.Calls {
			if call.CallID != value.SupervisorToolCallID || call.ToolName != "web_fetch" {
				continue
			}
			// Pending means the approved fetch itself still needs execution.
			// A terminal call remains resumable until the Turn checkpoint crosses
			// its boundary: a crash may have happened after persisting the result
			// but before the following provider/model step was scheduled.
			switch call.Status {
			case domain.SupervisorToolPending, domain.SupervisorToolCompleted,
				domain.SupervisorToolDenied, domain.SupervisorToolFailed:
				return true, nil
			}
		}
	}
	return false, apperror.New(apperror.CodeConflict,
		"web fetch authorization lost its exact Supervisor continuation")
}
