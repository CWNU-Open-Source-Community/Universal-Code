package application

import (
	"context"
	"strings"
	"time"

	"cyberagent-workbench/internal/apperror"
	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/idgen"
	"cyberagent-workbench/internal/runmutation"
)

type ThreadPlanControlRequest struct {
	Version, ThreadID, RunID, Action   string
	ProposalID                         string
	Direction                          int
	ManualAcceptance                   domain.PlanDeliveryManualAcceptance
	Content, OperationKey, RequestedBy string
}

type threadPlanCommitBinding struct{ proposalID, selectionKey string }

type ThreadPlanControlResult struct {
	ThreadID, RunID, Action, State            string
	ProposalID, SelectionID                   string
	Direction                                 int
	ManualAcceptance                          domain.PlanDeliveryManualAcceptance
	AppliedMode, CurrentMode                  *domain.RunModeSnapshot
	TurnRequest                               *domain.ThreadRequestObservation
	ExecutionStarted, ModelCalled, ToolCalled bool
}

type threadPlanControlStore interface {
	PlanDeliveryControlStore
	GetThread(context.Context, string) (domain.Thread, error)
	ListThreadRuns(context.Context, string) ([]domain.ThreadRun, error)
	InspectThreadTurnRequest(context.Context, string, string, string) (domain.ThreadRequestObservation, error)
	ThreadPlanConfirmationStale(context.Context, string, string, string) (bool, error)
	GetThreadPlanInitialMode(context.Context, string) (domain.RunModeSnapshot, error)
}

func threadPlanStepKey(threadID, operationKey, step string) string {
	if step != "turn" {
		step = "control"
	}
	return "thread-plan-" + runmutation.Fingerprint(PlanDeliveryControlProtocolVersion, "thread", threadID, operationKey, step)
}

func threadPlanOperationDigest(threadID, key string) string {
	return runmutation.Fingerprint(PlanDeliveryControlProtocolVersion, "thread-operation", threadID, key)
}

func (s *ThreadTurnService) planControlStore() (threadPlanControlStore, error) {
	if s == nil || s.threads == nil {
		return nil, apperror.New(apperror.CodeFailedPrecondition, "Thread Plan control is unavailable")
	}
	st, ok := s.threads.store.(threadPlanControlStore)
	if !ok {
		return nil, apperror.New(apperror.CodeFailedPrecondition, "Thread Plan control persistence is unavailable")
	}
	return st, nil
}

func normalizeThreadPlanRequest(request ThreadPlanControlRequest, mutation bool) (ThreadPlanControlRequest, error) {
	if request.Version != PlanDeliveryControlProtocolVersion ||
		!domain.ValidAgentID(request.ThreadID) || !domain.ValidAgentID(request.RunID) || !domain.ValidAgentID(request.RequestedBy) ||
		(request.Action != "enter_plan" && request.Action != "enter_deliver" && request.Action != "confirm") {
		return request, apperror.New(apperror.CodeInvalidArgument, "Thread Plan control identity or action is invalid")
	}
	key, err := domain.NormalizeAgentOperationKey(request.OperationKey)
	if err != nil || key != request.OperationKey || containsSpaceOrControl(key) {
		return request, apperror.New(apperror.CodeInvalidArgument, "Thread Plan control idempotency key is invalid")
	}
	if !mutation {
		return request, nil
	}
	if request.Action != "confirm" {
		if request.ProposalID != "" || request.Direction != 0 || request.ManualAcceptance != "" || request.Content != "" {
			return request, apperror.New(apperror.CodeInvalidArgument, "Entering Plan cannot carry confirmation fields")
		}
		return request, nil
	}
	acceptance, err := domain.NormalizePlanDeliveryManualAcceptance(request.ManualAcceptance)
	if err != nil || request.ManualAcceptance == "" || !domain.ValidAgentID(request.ProposalID) || request.Direction < 1 || request.Direction > domain.PlanDeliveryDirectionCount {
		return request, apperror.New(apperror.CodeInvalidArgument, "Plan confirmation requires an exact proposal, direction and manual acceptance policy")
	}
	content, err := domain.NormalizeThreadMessageContent(request.Content, nil)
	if err != nil || content == "" {
		return request, apperror.New(apperror.CodeInvalidArgument, "Plan confirmation requires the explicit operator message")
	}
	request.Content, request.ManualAcceptance = content, acceptance
	return request, nil
}

// ControlPlan prepares the existing Plan selection and mode receipts, then uses
// the normal Thread turn owner, journal, lease, failure sealing and tool loop.
// No new plan state machine or detached execution is introduced.
func (s *ThreadTurnService) ControlPlan(ctx context.Context, request ThreadPlanControlRequest) (ThreadPlanControlResult, error) {
	request, err := normalizeThreadPlanRequest(request, true)
	if err != nil {
		return ThreadPlanControlResult{}, err
	}
	st, err := s.planControlStore()
	if err != nil {
		return ThreadPlanControlResult{}, err
	}
	ctx, finish, err := s.beginHostOperation(ctx)
	if err != nil {
		return ThreadPlanControlResult{}, err
	}
	defer finish()
	if err := requireThreadPlanRun(ctx, st, request.ThreadID, request.RunID); err != nil {
		return ThreadPlanControlResult{}, err
	}
	plans := NewPlanDeliveryControlService(st)
	if request.Action != "confirm" {
		err := s.prepareThreadPlanOnly(ctx, request, func(ctx context.Context) error {
			return s.enterThreadPlanMode(ctx, request)
		})
		if err != nil {
			return ThreadPlanControlResult{}, err
		}
		return s.InspectPlan(ctx, request)
	}
	selectionKey := threadPlanStepKey(request.ThreadID, request.OperationKey, "select")
	// Validate the original selection even when Execute would return a completed
	// turn immediately. Another key must never start the same chosen plan again.
	op, selected, err := st.GetPlanDeliverySelectionOperation(ctx, threadPlanOperationDigest(request.ThreadID, selectionKey))
	if err != nil {
		return ThreadPlanControlResult{}, apperror.Normalize(err)
	}
	if selected {
		fingerprint := domain.PlanDeliverySelectionRequestFingerprintForAcceptance(request.ProposalID, request.RunID, request.Direction, request.RequestedBy, request.ManualAcceptance)
		if op.RequestFingerprint != fingerprint || op.ProposalID != request.ProposalID || op.RequestedBy != request.RequestedBy || op.RunID != request.RunID {
			return ThreadPlanControlResult{}, apperror.New(apperror.CodeConflict, "Plan confirmation key belongs to a different reviewed plan")
		}
	} else {
		if _, found, err := st.GetPlanDeliverySelectionByRun(ctx, request.RunID); err != nil {
			return ThreadPlanControlResult{}, apperror.Normalize(err)
		} else if found {
			return ThreadPlanControlResult{}, apperror.New(apperror.CodeConflict, "This Plan was confirmed by another operation; inspect the original confirmation")
		}
	}
	turnKey := threadPlanStepKey(request.ThreadID, request.OperationKey, "turn")
	before, err := st.InspectThreadTurnRequest(ctx, request.ThreadID, turnKey, request.RequestedBy)
	if err != nil {
		return ThreadPlanControlResult{}, apperror.Normalize(err)
	}
	if before.MessageID != "" && !selected {
		return ThreadPlanControlResult{}, apperror.New(apperror.CodeConflict, "Plan confirmation turn has no matching selected plan")
	}
	if !selected && before.MessageID == "" {
		stale, err := st.ThreadPlanConfirmationStale(ctx, request.ThreadID, request.RunID, request.ProposalID)
		if err != nil {
			return ThreadPlanControlResult{}, apperror.Normalize(err)
		}
		if stale {
			return ThreadPlanControlResult{}, apperror.New(apperror.CodeConflict, "Plan confirmation was superseded; review an updated plan")
		}
	}
	var prepare func(context.Context) error
	if before.MessageID == "" {
		prepare = func(ctx context.Context) error {
			stale, err := st.ThreadPlanConfirmationStale(ctx, request.ThreadID, request.RunID, request.ProposalID)
			if err != nil {
				return apperror.Normalize(err)
			}
			if stale {
				return apperror.New(apperror.CodeConflict, "Plan confirmation was superseded by newer requirements or execution; replan in this Thread")
			}
			if _, err := plans.SelectDirection(ctx, ControlPlanDirectionRequest{Version: request.Version, ThreadID: request.ThreadID,
				RunID: request.RunID, ProposalID: request.ProposalID, Direction: request.Direction, ManualAcceptance: request.ManualAcceptance,
				OperationKey: selectionKey, RequestedBy: request.RequestedBy}); err != nil {
				return err
			}
			_, err = plans.EnterDelivery(ctx, ControlPlanDeliveryTransitionRequest{Version: request.Version,
				ThreadID: request.ThreadID, RunID: request.RunID, OperationKey: threadPlanStepKey(request.ThreadID, request.OperationKey, "deliver"), RequestedBy: request.RequestedBy})
			return err
		}
	}
	executed, executeErr := s.executeWithPreparation(ctx, ExecuteThreadTurnRequest{Version: domain.ThreadMessageProtocolVersion,
		ThreadID: request.ThreadID, Content: request.Content, OperationKey: turnKey, RequestedBy: request.RequestedBy,
		plan: &threadPlanCommitBinding{proposalID: request.ProposalID, selectionKey: selectionKey}}, prepare)
	if executeErr != nil {
		// A correction that wins after reservation permanently invalidates
		// this exact unqueued confirmation. Seal only that existing intent;
		// a concurrently accepted message wins the store CAS and is preserved.
		if apperror.CodeOf(executeErr) == apperror.CodeConflict {
			decisionCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Second)
			if stale, err := st.ThreadPlanConfirmationStale(decisionCtx, request.ThreadID, request.RunID, request.ProposalID); err == nil && stale {
				if intents, ok := s.threads.store.(threadMessageIntentStore); ok {
					_, _ = intents.RejectThreadMessageIntent(decisionCtx, domain.ThreadMessageIntentRequest{ThreadID: request.ThreadID, Content: request.Content, OperationKey: turnKey, RequestedBy: request.RequestedBy})
				}
			}
			cancel()
		}
		return ThreadPlanControlResult{}, executeErr
	}
	result, err := s.InspectPlan(ctx, request)
	result.ExecutionStarted, result.ModelCalled, result.ToolCalled = executed.ExecutionStarted, executed.ModelCalled, executed.ToolCalled
	return result, err
}

func (s *ThreadTurnService) prepareThreadPlanOnly(ctx context.Context, request ThreadPlanControlRequest, prepare func(context.Context) error) error {
	s.turnMu.Lock()
	if s.hostContext != nil && s.hostContext.Err() != nil {
		s.turnMu.Unlock()
		return apperror.New(apperror.CodeUnavailable, "Thread execution host is shutting down")
	}
	if s.activeTurns[request.ThreadID] != nil {
		s.turnMu.Unlock()
		return apperror.New(apperror.CodeFailedPrecondition, "Plan control requires an idle task")
	}
	prepareCtx, cancel := context.WithCancel(ctx)
	active := &activeThreadTurn{id: idgen.New("thread-execution"), operationKey: request.OperationKey, cancel: cancel, preparingPlan: true}
	if s.activeTurns == nil {
		s.activeTurns = make(map[string]*activeThreadTurn)
	}
	s.activeTurns[request.ThreadID] = active
	s.turnMu.Unlock()
	defer func() {
		cancel()
		s.turnMu.Lock()
		if s.activeTurns[request.ThreadID] == active {
			delete(s.activeTurns, request.ThreadID)
		}
		s.turnMu.Unlock()
	}()
	return prepare(prepareCtx)
}

func requireThreadPlanRun(ctx context.Context, st threadPlanControlStore, threadID, runID string) error {
	bindings, err := st.ListThreadRuns(ctx, threadID)
	if err != nil {
		return apperror.Normalize(err)
	}
	for _, binding := range bindings {
		if binding.RunID == runID {
			return nil
		}
	}
	return apperror.New(apperror.CodeConflict, "Plan execution record belongs to a different Thread")
}

// InspectPlan reads original receipts only. An absent request may still arrive;
// prepared/received is not a claim that any worker is currently running.
func (s *ThreadTurnService) InspectPlan(ctx context.Context, request ThreadPlanControlRequest) (ThreadPlanControlResult, error) {
	request, err := normalizeThreadPlanRequest(request, false)
	if err != nil {
		return ThreadPlanControlResult{}, err
	}
	st, err := s.planControlStore()
	if err != nil {
		return ThreadPlanControlResult{}, err
	}
	if err := requireThreadPlanRun(ctx, st, request.ThreadID, request.RunID); err != nil {
		return ThreadPlanControlResult{}, err
	}
	value := ThreadPlanControlResult{ThreadID: request.ThreadID, RunID: request.RunID, Action: request.Action, State: "not_received"}
	current, err := st.GetRunMode(ctx, request.RunID)
	if err != nil {
		return value, apperror.Normalize(err)
	}
	value.CurrentMode = &current
	step := request.Action
	if request.Action != "confirm" {
		if _, selected, err := st.GetPlanDeliverySelectionOperation(ctx, threadPlanOperationDigest(request.ThreadID, threadPlanStepKey(request.ThreadID, request.OperationKey, "control"))); err != nil {
			return value, apperror.Normalize(err)
		} else if selected {
			return value, apperror.New(apperror.CodeConflict, "Thread Plan key belongs to a confirmation")
		}
		reader, ok := s.threads.store.(threadPlanSuccessorStore)
		if !ok {
			return value, apperror.New(apperror.CodeFailedPrecondition, "Thread mode continuation reader is unavailable")
		}
		if successor, found, err := reader.GetThreadPlanSuccessor(ctx, request.ThreadID, request.RunID, threadPlanStepKey(request.ThreadID, request.OperationKey, "control"), request.RequestedBy, threadPlanTargetPhase(request.Action)); err != nil {
			return value, apperror.Normalize(err)
		} else if found {
			mode, err := st.GetRunMode(ctx, successor.ID)
			if err != nil {
				return value, apperror.Normalize(err)
			}
			initial, err := st.GetThreadPlanInitialMode(ctx, successor.ID)
			if err != nil {
				return value, apperror.Normalize(err)
			}
			// The immutable initial snapshot is the applied mode; current mode
			// may have changed since this original request was completed.
			value.AppliedMode, value.CurrentMode, value.State = &initial, &mode, "completed"
			return value, nil
		}
	}
	if request.Action == "confirm" {
		step = "deliver"
		key := threadPlanOperationDigest(request.ThreadID, threadPlanStepKey(request.ThreadID, request.OperationKey, "select"))
		op, found, err := st.GetPlanDeliverySelectionOperation(ctx, key)
		if err != nil {
			return value, apperror.Normalize(err)
		}
		if found {
			if op.RunID != request.RunID || op.RequestedBy != request.RequestedBy {
				return value, apperror.New(apperror.CodeConflict, "Plan selection request scope differs")
			}
			selection, err := st.GetPlanDeliverySelection(ctx, op.SelectionID)
			if err != nil {
				return value, apperror.Normalize(err)
			}
			value.SelectionID, value.ProposalID, value.Direction, value.ManualAcceptance = selection.ID, selection.ProposalID, selection.DirectionOrdinal, selection.EffectiveManualAcceptance()
			value.State = "prepared"
		}
	}
	modeKey := threadPlanOperationDigest(request.ThreadID, threadPlanStepKey(request.ThreadID, request.OperationKey, step))
	op, found, err := st.GetRunModeOperation(ctx, modeKey)
	if err != nil {
		return value, apperror.Normalize(err)
	}
	if found {
		if op.RunID != request.RunID || op.RequestedBy != request.RequestedBy {
			return value, apperror.New(apperror.CodeConflict, "Plan mode request scope differs")
		}
		mode, err := st.GetRunModeSnapshot(ctx, op.SnapshotID)
		if err != nil {
			return value, apperror.Normalize(err)
		}
		expected := threadPlanTargetPhase(request.Action)
		if mode.RunID != request.RunID || mode.Phase != expected || (request.Action == "confirm" && value.SelectionID == "") {
			return value, apperror.New(apperror.CodeConflict, "Thread Plan key belongs to a different action")
		}
		if request.Action != "confirm" {
			if _, selected, err := st.GetPlanDeliverySelectionOperation(ctx, modeKey); err != nil {
				return value, apperror.Normalize(err)
			} else if selected {
				return value, apperror.New(apperror.CodeConflict, "Thread Plan key belongs to a confirmation")
			}
		}
		value.AppliedMode, value.State = &mode, "completed"
		if request.Action == "confirm" {
			value.State = "prepared"
		}
	}
	if request.Action == "confirm" {
		turn, err := st.InspectThreadTurnRequest(ctx, request.ThreadID, threadPlanStepKey(request.ThreadID, request.OperationKey, "turn"), request.RequestedBy)
		if err != nil {
			return value, apperror.Normalize(err)
		}
		if turn.ThreadID != request.ThreadID {
			return value, apperror.New(apperror.CodeConflict, "Plan turn observation scope differs")
		}
		value.TurnRequest = &turn
		if turn.MessageID != "" || turn.State == "rejected" {
			value.State = strings.ToLower(turn.State)
		} else if value.ProposalID != "" {
			stale, err := st.ThreadPlanConfirmationStale(ctx, request.ThreadID, request.RunID, value.ProposalID)
			if err != nil {
				return value, apperror.Normalize(err)
			}
			if stale {
				value.State = "rejected"
			}
		} else if turn.State != "not_received" {
			value.State = "prepared"
		}
	}
	return value, nil
}
