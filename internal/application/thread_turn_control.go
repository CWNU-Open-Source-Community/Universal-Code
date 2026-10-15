package application

import (
	"context"
	"strings"
	"time"

	"cyberagent-workbench/internal/apperror"
	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/idgen"
	"cyberagent-workbench/internal/llm"
)

const ThreadExecutionProtocolVersion = "thread_execution.v1"

// ThreadExecutionState describes this service's live request ownership. It is
// not a Run status, lease, durable worker, or authority that survives a restart.
type ThreadExecutionState struct {
	Version             string `json:"version"`
	ThreadID            string `json:"thread_id"`
	ExecutionID         string `json:"execution_id,omitempty"`
	State               string `json:"state"`
	QueuedMessages      int    `json:"queued_messages"`
	CapabilityGrant     bool   `json:"capability_grant"`
	LastTurnInterrupted bool   `json:"last_turn_interrupted,omitempty"`
}

type queuedThreadTurn struct {
	request SubmitThreadMessageRequest
	result  ExecuteThreadTurnResult
}

type activeThreadTurn struct {
	preparingPlan bool
	id            string
	operationKey  string
	cancel        context.CancelFunc
	stopping      bool
	queue         []queuedThreadTurn
	approvals     []ApprovalContinuationRequest
}

// Execute retains the caller's lifetime. A second explicit message is durably
// queued and consumed at the next turn boundary by that same request owner.
// There is no detached goroutine, reconstructed process authority, or second
// Supervisor; all work still crosses the existing execution lease and handoff.
func (s *ThreadTurnService) Execute(ctx context.Context,
	request ExecuteThreadTurnRequest,
) (result ExecuteThreadTurnResult, resultErr error) {
	return s.executeWithPreparation(ctx, request, nil)
}

// SubmitCurrentSteering shares the live Thread stop lock with Interrupt.
// Admission wins before stopping or is rejected; it cannot slip between the
// stopping flag and the durable Session-message enqueue.
func (s *ThreadTurnService) SubmitCurrentSteering(ctx context.Context,
	request SubmitSessionMessageRequest,
) (SubmitSessionMessageResult, error) {
	if s == nil || s.threads == nil || s.threads.store == nil {
		return SubmitSessionMessageResult{}, apperror.New(apperror.CodeFailedPrecondition,
			"Thread turn control is required for current-task correction")
	}
	reader, ok := s.threads.store.(interface {
		GetThreadBySession(context.Context, string) (domain.Thread, error)
	})
	if !ok {
		return SubmitSessionMessageResult{}, apperror.New(apperror.CodeFailedPrecondition,
			"Thread lookup is required for current-task correction")
	}
	submissions, ok := s.threads.store.(SessionMessageSubmissionStore)
	if !ok {
		return SubmitSessionMessageResult{}, apperror.New(apperror.CodeFailedPrecondition,
			"Session message store is required for current-task correction")
	}
	thread, err := reader.GetThreadBySession(ctx, request.SessionID)
	if err != nil {
		return SubmitSessionMessageResult{}, apperror.Normalize(err)
	}
	s.turnMu.Lock()
	defer s.turnMu.Unlock()
	if active := s.activeTurns[thread.ID]; active != nil && active.stopping {
		observer, ok := s.threads.store.(interface {
			InspectOperatorSteeringOperation(context.Context, string, string) (domain.OperatorSteeringMessage, bool, error)
		})
		if !ok {
			return SubmitSessionMessageResult{}, apperror.New(apperror.CodeFailedPrecondition,
				"The current task is stopping; keep this draft and send it after stopping completes")
		}
		_, found, err := observer.InspectOperatorSteeringOperation(ctx, request.SessionID, request.OperationKey)
		if err != nil {
			return SubmitSessionMessageResult{}, err
		}
		if !found {
			return SubmitSessionMessageResult{}, apperror.New(apperror.CodeFailedPrecondition,
				"The current task is stopping; keep this draft and send it after stopping completes")
		}
	}
	return NewSessionMessageSubmissionService(submissions).Submit(ctx, request)
}

func (s *ThreadTurnService) executeWithPreparation(ctx context.Context, request ExecuteThreadTurnRequest,
	prepare func(context.Context) error,
) (result ExecuteThreadTurnResult, resultErr error) {
	if s == nil || s.threads == nil || s.threads.store == nil || s.lifecycle == nil ||
		s.execution == nil || s.execution.store == nil || s.execution.supervisor == nil {
		return ExecuteThreadTurnResult{}, apperror.New(apperror.CodeFailedPrecondition,
			"Thread turn dependencies are required")
	}
	normalized, err := normalizeSubmitThreadMessageRequest(SubmitThreadMessageRequest{
		Version: request.Version, ThreadID: request.ThreadID, Content: request.Content,
		OperationKey: request.OperationKey, RequestedBy: request.RequestedBy,
		Files: request.Files, Images: request.Images, Attachments: request.Attachments, MCPReferences: request.MCPReferences, plan: request.plan,
	})
	if err != nil {
		return ExecuteThreadTurnResult{}, err
	}
	intent, err := s.threads.reserveMessage(ctx, normalized)
	if err != nil {
		return ExecuteThreadTurnResult{}, err
	}
	// Confirm an already sealed outcome even while another request owns the
	// Thread. Queue admission must not relabel a committed failed turn as success.
	if intent.MessageID != "" {
		if replay, found, err := s.findCompletedReplay(ctx, normalized); found || err != nil {
			return replay, err
		}
	}
	canReject := true
	if len(normalized.Files) > 0 || len(normalized.Images) > 0 || len(normalized.Attachments) > 0 || len(normalized.MCPReferences) > 0 {
		// A retry of the same still-preparing request must not reject its owner.
		// Only genuinely separate submissions may receive a durable rejection.
		defer func() {
			if resultErr == nil || !canReject {
				return
			}
			store, ok := s.threads.store.(threadMessageIntentStore)
			if !ok {
				return
			}
			decisionCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Second)
			defer cancel()
			if rejected, err := store.RejectThreadMessageIntent(decisionCtx, threadMessageIntentRequest(normalized)); err == nil && rejected {
				resultErr = &ThreadMessageNotQueuedError{Cause: resultErr}
			}
		}()

	}
	request.ThreadID = normalized.ThreadID
	s.turnMu.Lock()
	if active := s.activeTurns[request.ThreadID]; active != nil {
		defer s.turnMu.Unlock()
		if (len(normalized.Files) > 0 || len(normalized.Images) > 0 || len(normalized.Attachments) > 0 || len(normalized.MCPReferences) > 0) &&
			active.operationKey == normalized.OperationKey && intent.MessageID == "" {
			canReject = false
			return ExecuteThreadTurnResult{}, apperror.New(apperror.CodeUnavailable,
				"This Thread submission is still preparing; retry the same operation to confirm its result")
		}
		if prepare != nil || active.preparingPlan {
			return ExecuteThreadTurnResult{}, apperror.New(apperror.CodeFailedPrecondition, "Plan preparation requires an idle task; keep this input and retry after preparation")
		}
		if len(normalized.Files) > 0 || len(normalized.MCPReferences) > 0 {
			if intent.MessageID != "" {
				submission, _, err := s.threads.prepareMessage(ctx, normalized, intent)
				return ExecuteThreadTurnResult{Submission: submission, Replayed: true}, err
			}
			return ExecuteThreadTurnResult{}, apperror.New(apperror.CodeFailedPrecondition,
				"Thread project file and MCP references require an idle task. Wait for this execution or remove the references")
		}
		if active.stopping {
			return ExecuteThreadTurnResult{}, apperror.New(apperror.CodeFailedPrecondition,
				"The current turn is stopping; keep this draft and send it after stopping completes")
		}
		if len(active.queue) >= domain.MaxPendingOperatorSteering {
			return ExecuteThreadTurnResult{}, apperror.New(apperror.CodeResourceExhausted,
				"The pending Thread message queue is full")
		}
		var submission SubmitThreadMessageResult
		if len(normalized.Images) > 0 || len(normalized.Attachments) > 0 {
			submission, intent, err = s.threads.prepareMessage(ctx, normalized, intent)
			if err == nil && submission.Message.ID == "" && len(normalized.Images) > 0 {
				var refErr error
				var ref = llm.ModelRef{}
				ref, refErr = supervisorModelRef(s.execution.supervisor.router,
					submission.Run.Config.ModelRoute)
				if refErr != nil {
					err = refErr
				} else if capability := s.execution.supervisor.router.DescribeVision(ref); capability.State != llm.VisionSupported {
					err = apperror.New(apperror.CodeFailedPrecondition,
						"Selected model image capability is "+string(capability.State)+"; select a confirmed vision model before sending images")
				}
			}
			if err == nil && submission.Message.ID == "" {
				submission, err = s.threads.commitMessage(ctx, normalized, submission, nil)
			}
		} else {
			submission, err = s.threads.Submit(ctx, normalized)
		}
		if err != nil {
			return ExecuteThreadTurnResult{}, err
		}
		result := ExecuteThreadTurnResult{Submission: submission, Replayed: submission.Replayed}
		if !submission.Replayed && submission.Message.Status == domain.OperatorSteeringPending {
			active.queue = append(active.queue, queuedThreadTurn{request: normalized, result: result})
		}
		return result, nil
	}
	turnCtx, cancel := context.WithCancel(ctx)
	active := &activeThreadTurn{id: idgen.New("thread-execution"), operationKey: normalized.OperationKey, cancel: cancel, preparingPlan: prepare != nil}
	if s.activeTurns == nil {
		s.activeTurns = make(map[string]*activeThreadTurn)
	}
	s.activeTurns[request.ThreadID] = active
	s.turnMu.Unlock()
	defer func() {
		cancel()
		s.turnMu.Lock()
		if s.activeTurns[request.ThreadID] == active {
			active.stopping = true
			delete(s.activeTurns, request.ThreadID)
		}
		s.turnMu.Unlock()
	}()
	if prepare != nil {
		if err := prepare(turnCtx); err != nil {
			return result, err
		}
		s.turnMu.Lock()
		stopped := active.stopping || turnCtx.Err() != nil
		s.turnMu.Unlock()
		if stopped {
			return result, apperror.New(apperror.CodeCancelled, "Plan preparation stopped before execution")
		}
	}

	var executionErr error
	result, executionErr = s.execute(turnCtx, request)
	for {
		s.turnMu.Lock()
		if executionErr == nil && turnCtx.Err() == nil && len(active.queue) == 0 && len(active.approvals) > 0 {
			next := active.approvals[0]
			active.approvals = active.approvals[1:]
			s.turnMu.Unlock()
			if resumed := s.resumeApprovalWithOwner(turnCtx, next); resumed.State == "failed" {
				s.turnMu.Lock()
				active.stopping = true
				s.turnMu.Unlock()
				return result, executionErr
			}
			continue
		}
		if executionErr != nil || turnCtx.Err() != nil || len(active.queue) == 0 {
			// Accepted inputs belong to the Thread, not this request owner.
			// Stop draining on failure/cancellation, retaining unconsumed messages.
			active.stopping = true
			s.turnMu.Unlock()
			return result, executionErr
		}
		next := active.queue[0]
		active.queue = active.queue[1:]
		s.turnMu.Unlock()
		// The queued input keeps its accepted Run binding even if a preference
		// changes while the previous turn is in flight.
		next.result.Submission.Message, executionErr = s.execution.store.GetOperatorSteering(
			turnCtx, next.result.Submission.Message.ID)
		if executionErr == nil && next.result.Submission.Message.Status != domain.OperatorSteeringPending {
			continue
		}
		if executionErr == nil {
			next.result.Submission.Run, executionErr = s.threads.store.GetRun(turnCtx,
				next.result.Submission.Run.ID)
		}
		if executionErr == nil {
			_, executionErr = s.executeSubmission(turnCtx, next.request, next.result)
		}
		if executionErr != nil {
			s.turnMu.Lock()
			active.queue = append([]queuedThreadTurn{next}, active.queue...)
			s.turnMu.Unlock()
		}
	}
}

func (s *ThreadTurnService) ExecutionState(ctx context.Context, threadID string) (ThreadExecutionState, error) {
	if _, err := s.threads.Get(ctx, threadID); err != nil {
		return ThreadExecutionState{}, err
	}
	s.turnMu.Lock()
	defer s.turnMu.Unlock()
	state := s.executionStateLocked(threadID)
	if state.State == "idle" && s.recovery != nil {
		// Reuse the durable failed handoff. The transient request registry must
		// not make an interrupted turn appear completed after reconnecting.
		recovery, found, err := s.recovery.store.GetThreadRunRecovery(ctx, threadID)
		if err != nil {
			return ThreadExecutionState{}, err
		}
		state.LastTurnInterrupted = found && recovery.Quiescent &&
			recovery.ErrorCode == strings.ToLower(string(apperror.CodeCancelled))
		if !found {
			if reader, ok := s.threads.store.(interface {
				GetLatestThreadTurnFailure(context.Context, string) (domain.ThreadTurnFailure, bool, error)
			}); ok {
				failure, closed, err := reader.GetLatestThreadTurnFailure(ctx, threadID)
				if err != nil {
					return ThreadExecutionState{}, err
				}
				state.LastTurnInterrupted = closed && failure.ErrorCode == string(apperror.CodeCancelled)
			}
		}
	}
	return state, nil
}

// ExecutionStates snapshots only this service's live request ownership under
// one lock. Callers combine it with durable facts; absence here alone does not
// prove that another process has stopped executing a Run.
func (s *ThreadTurnService) ExecutionStates(ctx context.Context,
	threadIDs []string,
) (map[string]ThreadExecutionState, error) {
	if s == nil || s.threads == nil || s.threads.store == nil {
		return nil, apperror.New(apperror.CodeFailedPrecondition, "Thread execution observation is unavailable")
	}
	if err := ctx.Err(); err != nil {
		return nil, apperror.Normalize(err)
	}
	if len(threadIDs) > 1000 {
		return nil, apperror.New(apperror.CodeInvalidArgument, "Thread execution observation is limited to 1000 Threads")
	}
	for _, id := range threadIDs {
		if !domain.ValidAgentID(id) {
			return nil, apperror.New(apperror.CodeInvalidArgument, "Thread execution observation identity is invalid")
		}
	}
	s.turnMu.Lock()
	defer s.turnMu.Unlock()
	states := make(map[string]ThreadExecutionState, len(threadIDs))
	for _, id := range threadIDs {
		states[id] = s.executionStateLocked(id)
	}
	return states, nil
}

func (s *ThreadTurnService) Interrupt(ctx context.Context, threadID, executionID string) (ThreadExecutionState, error) {
	if _, err := s.threads.Get(ctx, threadID); err != nil {
		return ThreadExecutionState{}, err
	}
	if !domain.ValidAgentID(executionID) {
		return ThreadExecutionState{}, apperror.New(apperror.CodeInvalidArgument,
			"A current Thread execution identity is required")
	}
	s.turnMu.Lock()
	defer s.turnMu.Unlock()
	active := s.activeTurns[threadID]
	if active == nil {
		return s.executionStateLocked(threadID), nil
	}
	if active.id != executionID {
		return ThreadExecutionState{}, apperror.New(apperror.CodeConflict,
			"This stop request belongs to an earlier execution; refresh before stopping")
	}
	active.stopping = true
	active.cancel()
	return s.executionStateLocked(threadID), nil
}

func (s *ThreadTurnService) executionStateLocked(threadID string) ThreadExecutionState {
	state := ThreadExecutionState{Version: ThreadExecutionProtocolVersion, ThreadID: threadID, State: "idle"}
	if active := s.activeTurns[threadID]; active != nil {
		state.ExecutionID, state.State, state.QueuedMessages = active.id, "running", len(active.queue)
		if active.stopping {
			state.State = "stopping"
		}
	}
	return state
}
