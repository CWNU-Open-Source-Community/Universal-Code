package application

import (
	"context"
	"fmt"
	"time"

	"cyberagent-workbench/internal/apperror"
	"cyberagent-workbench/internal/approval"
	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/toolbudget"
)

// ExecuteAutomatic continues authorized background work through the ordinary
// Thread executor. Only this Go entrypoint can select automatic admission.
func (s *ThreadTurnService) ExecuteAutomatic(ctx context.Context, request ExecuteThreadTurnRequest) (ExecuteThreadTurnResult, error) {
	request.automatic = true
	return s.Execute(ctx, request)
}

func (s *ThreadTurnService) automaticMessageID(ctx context.Context, request SubmitThreadMessageRequest) (string, error) {
	reader, ok := s.threads.store.(interface {
		InspectOperatorSteeringOperation(context.Context, string, string) (domain.OperatorSteeringMessage, bool, error)
	})
	if !ok {
		return "", apperror.New(apperror.CodeFailedPrecondition, "Automatic continuation requires durable input observation")
	}
	bindings, err := s.threads.store.ListThreadRuns(ctx, request.ThreadID)
	if err != nil {
		return "", apperror.Normalize(err)
	}
	for index := len(bindings) - 1; index >= 0; index-- {
		message, found, err := reader.InspectOperatorSteeringOperation(ctx, bindings[index].SessionID, request.OperationKey)
		if err != nil {
			return "", apperror.Normalize(err)
		}
		if found {
			return message.ID, nil
		}
	}
	return "", nil
}

// CheckAutomaticTurn reports why a new background turn cannot start. ExecuteAutomatic
// repeats admission under the live-owner lock; this observation grants no authority.
func (s *ThreadTurnService) CheckAutomaticTurn(ctx context.Context, threadID string) error {
	if s == nil {
		return apperror.New(apperror.CodeFailedPrecondition, "Automatic continuation is unavailable")
	}
	s.turnMu.Lock()
	defer s.turnMu.Unlock()
	if s.activeTurns[threadID] != nil {
		return apperror.New(apperror.CodeFailedPrecondition, "Automatic continuation requires an idle task")
	}
	return s.checkAutomaticTurn(ctx, threadID, "")
}

type automaticThreadStore interface {
	GetThreadExecutionFacts(context.Context, []string) (map[string]domain.ThreadExecutionFacts, error)
	ListApprovals(context.Context, approval.ListFilter) ([]approval.Record, error)
	GetThreadExecutionPermission(context.Context, string) (domain.ThreadExecutionPermissionSnapshot, error)
	GetRunExecutionPermission(context.Context, string) (domain.RunExecutionPermissionSnapshot, error)
	GetRunAgentUsage(context.Context, string) (domain.RunAgentUsage, error)
	GetToolCallUsage(context.Context, string) (toolbudget.Usage, error)
	GetMonetaryUsage(context.Context, string) (domain.MonetaryUsage, error)
	GetLatestThreadTurnFailure(context.Context, string) (domain.ThreadTurnFailure, bool, error)
}

func (s *ThreadTurnService) checkAutomaticTurn(ctx context.Context, threadID, ownMessageID string) error {
	if s == nil || s.threads == nil || s.threads.store == nil || s.execution == nil || s.execution.supervisor == nil {
		return apperror.New(apperror.CodeFailedPrecondition, "Automatic continuation dependencies are unavailable")
	}
	if !validControlIdentity(threadID) {
		return apperror.New(apperror.CodeInvalidArgument, "Automatic continuation Thread id is invalid")
	}
	st, ok := s.threads.store.(automaticThreadStore)
	if !ok {
		return apperror.New(apperror.CodeFailedPrecondition, "Automatic continuation requires durable permission and budget observations")
	}
	thread, err := s.threads.store.GetThread(ctx, threadID)
	if err != nil {
		return apperror.Normalize(err)
	}
	if thread.Status != domain.ThreadActive {
		return apperror.New(apperror.CodeFailedPrecondition, "Automatic continuation requires an active task")
	}
	runID := thread.ActiveRunID
	if runID == "" {
		runID = thread.LastRunID
	}
	run, err := s.threads.store.GetRun(ctx, runID)
	if err != nil {
		return apperror.Normalize(err)
	}
	switch run.Status {
	case domain.RunCreated, domain.RunRunning, domain.RunCompleted:
	default:
		return apperror.New(apperror.CodeFailedPrecondition, fmt.Sprintf("Automatic continuation cannot start while the Run is %s", run.Status))
	}
	runner := s.execution.supervisor
	cp, found, err := runner.store.GetSupervisorCheckpoint(ctx, run.ID)
	if err != nil {
		return apperror.Normalize(err)
	}
	if found && cp.NextTurn > run.Budget.MaxTurns {
		return apperror.New(apperror.CodeResourceExhausted, "Automatic continuation requires remaining Run turn budget; continue explicitly to start a new budget")
	}
	usage, err := st.GetRunAgentUsage(ctx, run.ID)
	if err != nil {
		return apperror.Normalize(err)
	}
	tools, err := st.GetToolCallUsage(ctx, run.ID)
	if err != nil {
		return apperror.Normalize(err)
	}
	if err := requireSandboxCandidateBudget(run.Budget, usage, tools.Consumed); err != nil {
		return apperror.Wrap(apperror.CodeOf(err), "Automatic continuation requires remaining Run budget", err)
	}
	if run.Budget.MaxCostUSD > 0 {
		money, err := st.GetMonetaryUsage(ctx, run.ID)
		if err != nil {
			return apperror.Normalize(err)
		}
		if money.RemainingMicros <= 0 {
			return apperror.New(apperror.CodeResourceExhausted, "Automatic continuation requires remaining Run monetary budget")
		}
	}
	approvals, err := st.ListApprovals(ctx, approval.ListFilter{RunID: run.ID, Status: approval.StatusPending, Limit: 1})
	if err != nil {
		return apperror.Normalize(err)
	}
	if len(approvals) > 0 {
		return apperror.New(apperror.CodeFailedPrecondition, "The task is waiting for approval; resume it through the existing approval")
	}
	ownPending := false
	if ownMessageID != "" {
		message, err := s.execution.store.GetOperatorSteering(ctx, ownMessageID)
		if err != nil {
			return apperror.Normalize(err)
		}
		ownPending = message.RunID == run.ID && message.Status == domain.OperatorSteeringPending
	}
	if found && ((cp.HasPendingInput() || cp.AttemptID != "") && !ownPending || cp.Phase == domain.SupervisorWaiting || cp.Phase == domain.SupervisorTurnFailed || cp.Phase == domain.SupervisorRunFailed) {
		return apperror.New(apperror.CodeFailedPrecondition, "The previous turn is waiting or needs recovery; automatic continuation cannot replace its input or approval")
	}
	if found && ownPending && cp.AttemptID != "" {
		rounds, err := runner.store.ListSupervisorToolRounds(ctx, cp)
		if err != nil {
			return apperror.Normalize(err)
		}
		for _, round := range rounds {
			for _, call := range round.Calls {
				if !call.Status.Terminal() {
					return apperror.New(apperror.CodeFailedPrecondition, "The previous tool operation needs approval or recovery before automatic continuation")
				}
			}
		}
	}
	if _, failed, err := st.GetLatestThreadTurnFailure(ctx, threadID); err != nil {
		return apperror.Normalize(err)
	} else if failed {
		return apperror.New(apperror.CodeFailedPrecondition, "The previous task turn failed or was stopped; continue explicitly before automatic work can resume")
	}
	if s.recovery != nil {
		if _, failed, err := s.recovery.store.GetThreadRunRecovery(ctx, threadID); err != nil {
			return apperror.Normalize(err)
		} else if failed {
			return apperror.New(apperror.CodeFailedPrecondition, "The previous task execution requires recovery before automatic continuation")
		}
	}
	if lease, found, err := runner.store.GetRunExecutionLease(ctx, run.ID); err != nil {
		return apperror.Normalize(err)
	} else if found && lease.ActiveAt(time.Now().UTC()) {
		return apperror.New(apperror.CodeFailedPrecondition, "Automatic continuation requires an idle Run without an active execution lease")
	}
	queue, err := runner.store.GetOperatorSteeringQueueSummary(ctx, run.ID)
	if err != nil {
		return apperror.Normalize(err)
	}
	if queue.Prepared+queue.Pending > 0 && (!ownPending || queue.Prepared+queue.Pending != 1) {
		return apperror.New(apperror.CodeFailedPrecondition, "Automatic continuation must wait for existing task input and approvals")
	}
	if found && cp.AttemptID != "" && queue.Prepared != 1 {
		return apperror.New(apperror.CodeFailedPrecondition, "The unfinished attempt does not own this automatic input")
	}
	preference, err := st.GetThreadExecutionPermission(ctx, threadID)
	if err != nil {
		return apperror.Normalize(err)
	}
	permission, err := st.GetRunExecutionPermission(ctx, run.ID)
	if err != nil {
		return apperror.Normalize(err)
	}
	if preference.Mode == domain.RunExecutionPermissionFull || (thread.ActiveRunID != "" && permission.Mode == domain.RunExecutionPermissionFull) {
		var current *domain.RunExecutionPermissionSnapshot
		if thread.ActiveRunID != "" {
			current = &permission
		}
		if !s.runtimeAuthority.AllowsThreadFullAccess(preference, current) {
			return apperror.New(apperror.CodeFailedPrecondition, "Full Access is not active in this process; explicitly reactivate Full Access before automatic continuation")
		}
	}
	if run.Status != domain.RunCompleted {
		if config, ok := s.threads.store.(threadEpochTransitionStore); ok {
			if pending, err := s.pendingEpochConfiguration(ctx, config, thread, run); err != nil {
				return err
			} else if pending {
				return apperror.New(apperror.CodeFailedPrecondition, "Task configuration changed; continue explicitly before automatic work can resume")
			}
		}
	}
	return nil
}

// AutomaticThreadTurnObservation is a read-only result for one durable input.
// A committed message alone is insufficient: failed turns also consume messages.
type AutomaticThreadTurnObservation struct {
	State  string `json:"state"`
	Reason string `json:"reason,omitempty"`
}

func (s *ThreadTurnService) ObserveAutomaticTurn(ctx context.Context, threadID, runID, messageID string) (AutomaticThreadTurnObservation, error) {
	observation := func(state, reason string) (AutomaticThreadTurnObservation, error) {
		return AutomaticThreadTurnObservation{State: state, Reason: reason}, nil
	}
	if s == nil || s.threads == nil || s.execution == nil || s.execution.supervisor == nil {
		return AutomaticThreadTurnObservation{}, apperror.New(apperror.CodeFailedPrecondition, "Automatic continuation observation is unavailable")
	}
	if !validControlIdentity(threadID) || !validControlIdentity(runID) || !validControlIdentity(messageID) {
		return AutomaticThreadTurnObservation{}, apperror.New(apperror.CodeInvalidArgument, "Automatic continuation observation identity is invalid")
	}
	st, ok := s.threads.store.(automaticThreadStore)
	if !ok {
		return AutomaticThreadTurnObservation{}, apperror.New(apperror.CodeFailedPrecondition, "Automatic continuation observation store is unavailable")
	}
	thread, err := s.threads.store.GetThread(ctx, threadID)
	if err != nil {
		return AutomaticThreadTurnObservation{}, apperror.Normalize(err)
	}
	bindings, err := s.threads.store.ListThreadRuns(ctx, threadID)
	if err != nil {
		return AutomaticThreadTurnObservation{}, apperror.Normalize(err)
	}
	message, err := s.execution.store.GetOperatorSteering(ctx, messageID)
	if err != nil {
		return AutomaticThreadTurnObservation{}, apperror.Normalize(err)
	}
	bound := false
	for _, binding := range bindings {
		if binding.RunID == runID && binding.SessionID == message.SessionID && message.RunID == runID {
			bound = true
			break
		}
	}
	if !bound {
		return AutomaticThreadTurnObservation{}, apperror.New(apperror.CodeConflict, "Automatic continuation input does not belong to this task Run")
	}
	failureStore, ok := s.threads.store.(threadTurnFailureStore)
	if !ok {
		return AutomaticThreadTurnObservation{}, apperror.New(apperror.CodeFailedPrecondition, "Automatic continuation outcome reader is unavailable")
	}
	if _, failed, err := failureStore.GetThreadTurnFailure(ctx, runID, messageID); err != nil {
		return AutomaticThreadTurnObservation{}, apperror.Normalize(err)
	} else if failed {
		return observation("blocked", "The original task turn failed or was stopped")
	}
	run, err := s.threads.store.GetRun(ctx, runID)
	if err != nil {
		return AutomaticThreadTurnObservation{}, apperror.Normalize(err)
	}
	if thread.Status != domain.ThreadActive || message.Status == domain.OperatorSteeringCancelled || run.Status == domain.RunFailed || run.Status == domain.RunCancelled {
		return observation("blocked", "The task or its original input was paused, stopped, or failed")
	}
	approvals, err := st.ListApprovals(ctx, approval.ListFilter{RunID: runID, Status: approval.StatusPending, Limit: 1})
	if err != nil {
		return AutomaticThreadTurnObservation{}, apperror.Normalize(err)
	}
	if len(approvals) > 0 || run.Status == domain.RunWaitingApproval {
		return observation("waiting_approval", "Continue through the existing approval")
	}
	if run.Status == domain.RunPaused {
		return observation("blocked", "The original task execution is paused")
	}
	runner := s.execution.supervisor
	cp, found, err := runner.store.GetSupervisorCheckpoint(ctx, runID)
	if err != nil {
		return AutomaticThreadTurnObservation{}, apperror.Normalize(err)
	}
	if found && (cp.Phase == domain.SupervisorTurnFailed || cp.Phase == domain.SupervisorRunFailed) {
		return observation("blocked", "The original execution requires recovery")
	}
	if found && cp.Phase == domain.SupervisorWaiting {
		return observation("blocked", "The task is waiting at its existing turn boundary")
	}
	s.turnMu.Lock()
	active := s.activeTurns[threadID] != nil
	s.turnMu.Unlock()
	lease, leased, err := runner.store.GetRunExecutionLease(ctx, runID)
	if err != nil {
		return AutomaticThreadTurnObservation{}, apperror.Normalize(err)
	}
	if active || leased && lease.ActiveAt(time.Now().UTC()) {
		return observation("running", "The task execution is active")
	}
	if found && cp.AttemptID != "" {
		rounds, err := runner.store.ListSupervisorToolRounds(ctx, cp)
		if err != nil {
			return AutomaticThreadTurnObservation{}, apperror.Normalize(err)
		}
		for _, round := range rounds {
			for _, call := range round.Calls {
				if !call.Status.Terminal() {
					return observation("blocked", "An unfinished tool operation needs approval or recovery")
				}
			}
		}
	}
	if message.Status == domain.OperatorSteeringPending || found && (cp.AttemptID != "" || cp.HasPendingInput() || cp.Phase == domain.SupervisorTurnStarted) {
		return observation("running", "The original input has not reached a completed turn")
	}
	if s.recovery != nil {
		recovery, failed, err := s.recovery.store.GetThreadRunRecovery(ctx, threadID)
		if err != nil {
			return AutomaticThreadTurnObservation{}, apperror.Normalize(err)
		}
		if failed && recovery.RunID == runID {
			return observation("blocked", "The task execution requires recovery")
		}
	}
	facts, err := st.GetThreadExecutionFacts(ctx, []string{threadID})
	if err != nil {
		return AutomaticThreadTurnObservation{}, apperror.Normalize(err)
	}
	if fact := facts[threadID]; fact.RunID == runID && fact.Unsettled {
		return observation("running", "The durable task execution has not settled")
	}
	if message.Status != domain.OperatorSteeringCommitted || !found {
		return observation("blocked", "The original turn has no confirmed completion")
	}
	_, _, err = s.execution.store.GetCommittedOperatorSteeringLifecycleActions(ctx, messageID)
	if err != nil {
		if apperror.CodeOf(err) == apperror.CodeFailedPrecondition || apperror.CodeOf(err) == apperror.CodeConflict {
			return observation("blocked", "The original turn has no unambiguous completion")
		}
		return AutomaticThreadTurnObservation{}, apperror.Normalize(err)
	}
	return observation("completed", "")
}
