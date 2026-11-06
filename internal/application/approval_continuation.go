package application

import (
	"context"
	"errors"
	"strings"
	"time"

	"cyberagent-workbench/internal/apperror"
	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/idgen"
)

type ApprovalContinuationRequest struct {
	RunID      string
	Kind       string
	ProposalID string
}

// The review is already durable when this result is returned. Failed refers
// only to its subsequent model continuation, never to the operator decision.
type ApprovalContinuationResult struct {
	State       string `json:"state"`
	HandoffID   string `json:"handoff_id,omitempty"`
	Replayed    bool   `json:"replayed"`
	ModelCalled bool   `json:"model_called"`
	ToolCalled  bool   `json:"tool_called"`
	ErrorCode   string `json:"error_code,omitempty"`
	Message     string `json:"message,omitempty"`
}

type approvalContinuationStore interface {
	PrepareApprovalContinuation(context.Context, string, string, string) (domain.ApprovalContinuation, bool, error)
	BeginSupervisorApprovalContinuation(context.Context, domain.RunExecutionLease, string) (domain.SupervisorTurn, error)
	CloseFailedApprovalContinuation(context.Context, string) error
}

// ResumeApproval shares the Thread's live owner and stop button. It never
// approves or executes a proposal itself, nor manufactures a user message.
func (s *ThreadTurnService) ResumeApproval(ctx context.Context, request ApprovalContinuationRequest) ApprovalContinuationResult {
	if s == nil || s.threads == nil || s.execution == nil {
		return approvalContinuationFailed(apperror.New(apperror.CodeUnavailable, "Approval continuation is unavailable"))
	}
	if !validControlIdentity(request.RunID) || !validControlIdentity(request.ProposalID) || (request.Kind != "file_edit" && request.Kind != "host_command" && request.Kind != "agent_browser" && request.Kind != "mcp" && request.Kind != "command_runtime") {
		return approvalContinuationFailed(apperror.New(apperror.CodeInvalidArgument, "Approval continuation identity is invalid"))
	}
	thread, err := s.threads.store.GetThreadByRun(ctx, request.RunID)
	if err != nil {
		return approvalContinuationFailed(err)
	}
	if thread.Status != domain.ThreadActive || thread.ActiveRunID != request.RunID {
		return ApprovalContinuationResult{State: "not_started"}
	}
	s.turnMu.Lock()
	if s.hostContext != nil && s.hostContext.Err() != nil {
		s.turnMu.Unlock()
		return approvalContinuationFailed(apperror.New(apperror.CodeUnavailable, "Thread execution host is shutting down"))
	}
	if active := s.activeTurns[thread.ID]; active != nil {
		defer s.turnMu.Unlock()
		if active.stopping {
			return ApprovalContinuationResult{State: "not_started"}
		}
		for _, pending := range active.approvals {
			if pending == request {
				return ApprovalContinuationResult{State: "queued", Replayed: true}
			}
		}
		if len(active.approvals) >= 32 {
			return approvalContinuationFailed(apperror.New(apperror.CodeResourceExhausted, "Pending review continuation queue is full"))
		}
		active.approvals = append(active.approvals, request)
		return ApprovalContinuationResult{State: "queued"}
	}
	turnCtx, cancel := context.WithCancel(ctx)
	active := &activeThreadTurn{id: idgen.New("thread-execution"), operationKey: "approval:" + request.Kind + ":" + request.ProposalID, cancel: cancel, done: make(chan struct{})}
	if s.activeTurns == nil {
		s.activeTurns = make(map[string]*activeThreadTurn)
	}
	s.activeTurns[thread.ID] = active
	s.turnMu.Unlock()
	defer func() {
		cancel()
		s.turnMu.Lock()
		if s.activeTurns[thread.ID] == active {
			active.stopping = true
			delete(s.activeTurns, thread.ID)
		}
		close(active.done)
		s.turnMu.Unlock()
	}()
	result := s.resumeApprovalWithOwner(turnCtx, request)
	// Accepted new messages retain their ordinary ownership and priority. They
	// are consumed only after this approval continuation reaches its boundary.
	for result.State != "failed" && turnCtx.Err() == nil {
		s.turnMu.Lock()
		if len(active.queue) > 0 {
			next := active.queue[0]
			active.queue = active.queue[1:]
			s.turnMu.Unlock()
			var err error
			next.result.Submission.Message, err = s.execution.store.GetOperatorSteering(turnCtx, next.result.Submission.Message.ID)
			if err == nil && next.result.Submission.Message.Status != domain.OperatorSteeringPending {
				continue
			}
			if err == nil {
				next.result.Submission.Run, err = s.threads.store.GetRun(turnCtx, next.result.Submission.Run.ID)
			}
			if err == nil {
				_, err = s.executeSubmission(turnCtx, next.request, next.result)
			}
			if err != nil {
				break
			}
			continue
		}
		if len(active.approvals) == 0 {
			active.stopping = true
			if s.activeTurns[thread.ID] == active {
				delete(s.activeTurns, thread.ID)
			}
			s.turnMu.Unlock()
			break
		}
		next := active.approvals[0]
		active.approvals = active.approvals[1:]
		s.turnMu.Unlock()
		if resumed := s.resumeApprovalWithOwner(turnCtx, next); resumed.State == "failed" {
			break
		}
	}
	return result
}

func approvalContinuationFailed(err error) ApprovalContinuationResult {
	return ApprovalContinuationResult{State: "failed", ErrorCode: string(apperror.CodeOf(apperror.Normalize(err))), Message: "审批已保存，后续执行未完成。请先查看执行记录；结果未确认的写入或命令不会自动重试。"}
}

func (s *ThreadTurnService) resumeApprovalWithOwner(ctx context.Context, request ApprovalContinuationRequest) ApprovalContinuationResult {
	if request.Kind == "agent_browser" || request.Kind == "mcp" || request.Kind == "command_runtime" {
		return s.resumePendingToolApproval(ctx, request)
	}
	store, ok := s.execution.store.(approvalContinuationStore)
	if !ok {
		return ApprovalContinuationResult{State: "not_started"}
	}
	thread, err := s.threads.store.GetThreadByRun(ctx, request.RunID)
	if err != nil {
		return approvalContinuationFailed(err)
	}
	run, err := s.threads.store.GetRun(ctx, request.RunID)
	if err != nil {
		return approvalContinuationFailed(err)
	}
	if configStore, ok := s.threads.store.(threadEpochTransitionStore); ok {
		pending, err := s.pendingEpochConfiguration(ctx, configStore, thread, run)
		if err != nil {
			return approvalContinuationFailed(err)
		}
		if pending {
			return ApprovalContinuationResult{State: "not_started"}
		}
	}
	value, found, err := store.PrepareApprovalContinuation(ctx, request.RunID, request.Kind, request.ProposalID)
	if err != nil {
		return approvalContinuationFailed(err)
	}
	if !found {
		return ApprovalContinuationResult{State: "not_started"}
	}
	if value.Handoff.Result != nil {
		return approvalContinuationProjection(value.Handoff, true)
	}
	var execution LifecycleResult
	err = s.execution.supervisor.withRunExecutionLease(ctx, request.RunID, func(leaseCtx context.Context, lease domain.RunExecutionLease) error {
		_, stepErr := store.BeginSupervisorApprovalContinuation(leaseCtx, lease, value.Handoff.Operation.ID)
		if stepErr == nil {
			execution, stepErr = s.execution.supervisor.stepWithLease(leaseCtx, lease, value.Input)
		}
		status, code, reason := domain.RunExecutionHandoffCompleted, "", "approval_continuation"
		if stepErr != nil {
			status = domain.RunExecutionHandoffFailed
			code = strings.ToLower(string(apperror.CodeOf(apperror.Normalize(stepErr))))
			reason = code
		}
		settleCtx, cancel := context.WithTimeout(context.WithoutCancel(leaseCtx), 2*time.Second)
		defer cancel()
		steps := 0
		if execution.Turn > 0 {
			steps = 1
		}
		result, _, recordErr := s.execution.store.CompleteRunExecutionHandoff(settleCtx, value.Handoff.Operation.ID, lease, status, reason, code, steps, execution.ModelAttempts > 0, execution.ToolCalls > 0)
		if recordErr == nil {
			value.Handoff.Result = &result
		}
		return errors.Join(stepErr, recordErr)
	})
	if err != nil {
		settleCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Second)
		defer cancel()
		if value.Handoff.Result != nil {
			err = errors.Join(err, store.CloseFailedApprovalContinuation(settleCtx, value.Handoff.Operation.ID))
		}
		result := approvalContinuationFailed(err)
		result.HandoffID = value.Handoff.Operation.ID
		result.ModelCalled = execution.ModelAttempts > 0
		result.ToolCalled = execution.ToolCalls > 0
		return result
	}
	return approvalContinuationProjection(value.Handoff, false)
}

func approvalContinuationProjection(handoff domain.RunExecutionHandoff, replayed bool) ApprovalContinuationResult {
	result := ApprovalContinuationResult{State: "completed", HandoffID: handoff.Operation.ID, Replayed: replayed}
	if handoff.Result == nil {
		return approvalContinuationFailed(apperror.New(apperror.CodeUnavailable, "Approval continuation outcome is unknown"))
	}
	result.ModelCalled, result.ToolCalled = handoff.Result.ModelCalled, handoff.Result.ToolCalled
	if handoff.Result.Status == domain.RunExecutionHandoffFailed {
		result.State = "failed"
		result.ErrorCode = strings.ToUpper(handoff.Result.ErrorCode)
		result.Message = "审批已保存，后续执行未完成。请先查看执行记录；结果未确认的写入或命令不会自动重试。"
	}
	return result
}
