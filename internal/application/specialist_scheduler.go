package application

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"cyberagent-workbench/internal/apperror"
	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/hooks"
	"cyberagent-workbench/internal/idgen"
	"cyberagent-workbench/internal/waitgraph"
)

const MaxSpecialistScheduleRounds = domain.MaxSpecialistScheduleRounds

type SpecialistScheduleStopReason string

const (
	SpecialistScheduleAllTerminal     SpecialistScheduleStopReason = "all_terminal"
	SpecialistScheduleNoReadyChild    SpecialistScheduleStopReason = "no_ready_child"
	SpecialistScheduleRoundLimit      SpecialistScheduleStopReason = "round_limit"
	SpecialistScheduleTokenBudget     SpecialistScheduleStopReason = "token_budget_exhausted"
	SpecialistScheduleExecutionBudget SpecialistScheduleStopReason = "execution_budget_exhausted"
	SpecialistScheduleChildError      SpecialistScheduleStopReason = "child_error"
	SpecialistScheduleCancelled       SpecialistScheduleStopReason = "cancelled"
	SpecialistScheduleWaitingApproval SpecialistScheduleStopReason = "waiting_approval"
)

type SpecialistScheduleRequest struct {
	RunID              string
	AgentIDs           []string
	MaxRounds          int
	OperatorRequestID  string
	ContinuationDigest string
}

type SpecialistScheduleFailure struct {
	AgentID string
	Code    apperror.Code
	Reason  string
}

type SpecialistScheduleResult struct {
	WaitingApproval    bool
	ScheduleID         string
	OperatorRequestID  string
	ContinuationDigest string
	Replayed           bool
	RunID              string
	AgentIDs           []string
	RoundsCompleted    int
	TurnsStarted       int
	RecoveredAttempts  int
	RecoveredSchedule  bool
	StopReason         SpecialistScheduleStopReason
	UsageBefore        domain.RunAgentUsage
	UsageAfter         domain.RunAgentUsage
	Turns              []SpecialistTurnResult
	Failures           []SpecialistScheduleFailure
}

type SpecialistScheduleStore interface {
	StartSpecialistSchedule(ctx context.Context,
		start domain.SpecialistScheduleStart) (domain.SpecialistScheduleStartResult, error)
	FinishSpecialistSchedule(ctx context.Context,
		finish domain.SpecialistScheduleFinish) (domain.SpecialistSchedule, error)
}

// SpecialistScheduler is the Run-level owner for child turns. Direct callers
// remain internal; the operator CLI reaches it only through an immutable,
// application-bound schedule request. Models and ordinary tools have no path.
type SpecialistScheduler struct {
	runner         *SubagentRunner
	waitGraph      *waitgraph.Graph
	lifecycleHooks *hooks.Engine
}

func (s *SpecialistScheduler) WithLifecycleHooks(
	engine *hooks.Engine,
) *SpecialistScheduler {
	if s != nil {
		s.lifecycleHooks = engine
	}
	return s
}

func NewSpecialistScheduler(runner *SubagentRunner) *SpecialistScheduler {
	return &SpecialistScheduler{runner: runner, waitGraph: waitgraph.Default()}
}

func (s *SpecialistScheduler) WithWaitGraph(graph *waitgraph.Graph) *SpecialistScheduler {
	if s != nil && graph != nil {
		s.waitGraph = graph
	}
	return s
}

func (s *SpecialistScheduler) Execute(ctx context.Context,
	request SpecialistScheduleRequest,
) (SpecialistScheduleResult, error) {
	result, err := normalizeSpecialistScheduleRequest(request)
	if err != nil {
		return result, err
	}
	if s == nil || s.runner == nil || s.runner.store == nil || s.runner.router == nil ||
		s.runner.checker == nil || s.waitGraph == nil {
		return result, apperror.New(apperror.CodeFailedPrecondition,
			"Specialist scheduler dependencies are required")
	}
	scheduleStore, ok := s.runner.store.(SpecialistScheduleStore)
	if !ok {
		return result, apperror.New(apperror.CodeFailedPrecondition,
			"Specialist scheduler requires a durable schedule store")
	}
	run, err := s.runner.store.GetRun(ctx, result.RunID)
	if err != nil {
		return result, apperror.Normalize(err)
	}
	if run.Status != domain.RunRunning {
		return result, apperror.New(apperror.CodeFailedPrecondition,
			fmt.Sprintf("run %s is %s; Specialist scheduling requires running", run.ID, run.Status))
	}
	mission, err := s.runner.store.GetMission(ctx, run.MissionID)
	if err != nil {
		return result, apperror.Normalize(err)
	}
	err = withRunExecutionLease(ctx, s.runner.store, run.ID, s.runner.leaseOwner,
		s.runner.leasePolicy, func(leaseCtx context.Context, lease domain.RunExecutionLease) error {
			if err := executeLifecycleBoundary(leaseCtx, s.lifecycleHooks, hooks.Subagent,
				run.ID, mission.WorkspaceID, map[string]any{
					"agent_count": len(result.AgentIDs), "max_rounds": request.MaxRounds,
					"operator_request": strings.TrimSpace(request.OperatorRequestID) != "",
					"source":           "specialist_scheduler",
				}); err != nil {
				return err
			}
			return s.executeWithLease(leaseCtx, lease, request.MaxRounds, scheduleStore, &result)
		})
	if err != nil && result.StopReason == "" &&
		(ctx.Err() != nil || errors.Is(err, context.Canceled)) {
		result.StopReason = SpecialistScheduleCancelled
	}
	return result, apperror.Normalize(err)
}

func (s *SpecialistScheduler) executeWithLease(ctx context.Context,
	lease domain.RunExecutionLease, maxRounds int, scheduleStore SpecialistScheduleStore,
	result *SpecialistScheduleResult,
) (executeErr error) {
	run, err := s.runner.store.GetRun(ctx, result.RunID)
	if err != nil {
		return apperror.Normalize(err)
	}
	usage, err := s.runner.store.GetRunAgentUsage(ctx, run.ID)
	if err != nil {
		return apperror.Normalize(err)
	}
	result.UsageBefore = usage
	result.UsageAfter = usage
	scheduleID := idgen.New("schedule")
	started, err := scheduleStore.StartSpecialistSchedule(ctx,
		domain.SpecialistScheduleStart{
			ID: scheduleID, RunID: result.RunID,
			AgentIDs: append([]string(nil), result.AgentIDs...), MaxRounds: maxRounds,
			OperatorRequestID:  result.OperatorRequestID,
			ContinuationDigest: result.ContinuationDigest,
			Lease:              lease, UsageBefore: usage, StartedAt: time.Now().UTC(),
		})
	if err != nil {
		return apperror.Normalize(err)
	}
	result.ScheduleID = started.Schedule.ID
	if started.Schedule.Status.Terminal() {
		result.Replayed = true
		result.RoundsCompleted = started.Schedule.RoundsCompleted
		result.TurnsStarted = started.Schedule.TurnsStarted
		result.StopReason = SpecialistScheduleStopReason(started.Schedule.StopReason)
		result.WaitingApproval = result.StopReason == SpecialistScheduleWaitingApproval
		result.UsageBefore = started.Schedule.UsageBefore
		result.UsageAfter = started.Schedule.UsageAfter
		return specialistOperatorScheduleTerminalError(started.Schedule)
	}
	result.RecoveredSchedule = started.RecoveredSchedule
	defer func() {
		if recover() != nil {
			if result.StopReason == "" {
				result.StopReason = SpecialistScheduleChildError
			}
			executeErr = errors.Join(executeErr, apperror.New(apperror.CodeInternal,
				"Specialist scheduler runtime panicked"))
		}
		status, stopReason, errorCode := specialistScheduleTerminal(result, executeErr, ctx)
		finishCtx, cancelFinish := specialistEventContext(ctx)
		_, finishErr := scheduleStore.FinishSpecialistSchedule(finishCtx,
			domain.SpecialistScheduleFinish{
				ID: result.ScheduleID, Lease: lease, Status: status,
				StopReason: stopReason, RoundsCompleted: result.RoundsCompleted,
				TurnsStarted:      result.TurnsStarted,
				RecoveredAttempts: result.RecoveredAttempts,
				UsageAfter:        result.UsageAfter, ErrorCode: errorCode,
				FinishedAt: time.Now().UTC(),
			})
		cancelFinish()
		if finishErr != nil {
			executeErr = errors.Join(executeErr, apperror.Normalize(finishErr))
		}
	}()
	recovered, err := s.runner.store.RecoverSpecialistAttempts(ctx, lease)
	if err != nil {
		return apperror.Normalize(err)
	}
	result.RecoveredAttempts = len(recovered)
	if reason := specialistAggregateBudgetStop(run.Budget, usage); reason != "" {
		result.StopReason = reason
		return nil
	}

	for result.RoundsCompleted < maxRounds {
		ready, terminal, err := s.scheduleCandidates(ctx, run.ID, result.AgentIDs)
		if err != nil {
			return err
		}
		if len(ready) == 0 {
			if terminal == len(result.AgentIDs) {
				result.StopReason = SpecialistScheduleAllTerminal
			} else {
				result.StopReason = SpecialistScheduleNoReadyChild
			}
			return nil
		}

		limits, scheduled, reason := specialistRoundLimits(run.Budget, result.UsageAfter, ready)
		if reason != "" {
			result.StopReason = reason
			return nil
		}
		outcomes := s.runSpecialistRound(ctx, lease, scheduled, limits)
		result.RoundsCompleted++
		var roundErrors []specialistRoundOutcome
		for _, outcome := range outcomes {
			result.Turns = append(result.Turns, outcome.result)
			result.WaitingApproval = result.WaitingApproval || outcome.result.WaitingApproval
			if outcome.result.AttemptID != "" {
				result.TurnsStarted++
			}
			if outcome.err != nil {
				roundErrors = append(roundErrors, outcome)
				result.Failures = append(result.Failures, SpecialistScheduleFailure{
					AgentID: outcome.agentID,
					Code:    apperror.CodeOf(apperror.Normalize(outcome.err)),
					Reason:  boundedSpecialistFailure(outcome.err),
				})
			}
		}
		usageCtx, cancelUsage := specialistEventContext(ctx)
		usage, err = s.runner.store.GetRunAgentUsage(usageCtx, run.ID)
		cancelUsage()
		if err != nil {
			return apperror.Normalize(err)
		}
		result.UsageAfter = usage
		if result.WaitingApproval && len(roundErrors) == 0 {
			result.StopReason = SpecialistScheduleWaitingApproval
			return nil
		}
		if len(roundErrors) != 0 {
			if ctx.Err() != nil {
				result.StopReason = SpecialistScheduleCancelled
				return apperror.Normalize(ctx.Err())
			}
			if reason := specialistAggregateBudgetStop(run.Budget, usage); reason != "" {
				result.StopReason = reason
				return nil
			}
			result.StopReason = SpecialistScheduleChildError
			return selectSpecialistRoundError(roundErrors)
		}
		if reason := specialistAggregateBudgetStop(run.Budget, usage); reason != "" {
			result.StopReason = reason
			return nil
		}
	}
	result.StopReason = SpecialistScheduleRoundLimit
	return nil
}

func specialistScheduleTerminal(result *SpecialistScheduleResult, err error,
	ctx context.Context,
) (domain.SpecialistScheduleStatus, string, string) {
	stopReason := strings.TrimSpace(string(result.StopReason))
	if err == nil {
		if stopReason == "" {
			stopReason = string(SpecialistScheduleNoReadyChild)
		}
		return domain.SpecialistScheduleCompleted, stopReason, ""
	}
	normalized := apperror.Normalize(err)
	code := string(apperror.CodeOf(normalized))
	if ctx.Err() != nil || apperror.CodeOf(normalized) == apperror.CodeCancelled {
		if stopReason == "" {
			stopReason = string(SpecialistScheduleCancelled)
		}
		return domain.SpecialistScheduleCancelled, stopReason, code
	}
	if stopReason == "" {
		stopReason = string(SpecialistScheduleChildError)
	}
	return domain.SpecialistScheduleFailed, stopReason, code
}

func (s *SpecialistScheduler) scheduleCandidates(ctx context.Context, runID string,
	agentIDs []string,
) ([]domain.AgentNode, int, error) {
	if _, err := s.runner.store.ReconcileDependencyEdges(ctx, runID); err != nil {
		return nil, 0, apperror.Normalize(err)
	}
	nodes, err := s.runner.store.ListAgentNodes(ctx, runID)
	if err != nil {
		return nil, 0, apperror.Normalize(err)
	}
	byID := make(map[string]domain.AgentNode, len(nodes))
	var root domain.AgentNode
	for _, node := range nodes {
		byID[node.ID] = node
		if node.Role == domain.AgentRoleRoot {
			if root.ID != "" {
				return nil, 0, apperror.New(apperror.CodeConflict,
					"Specialist schedule found multiple root Agents")
			}
			root = node
		}
	}
	if root.ID == "" || root.Terminal() || root.Status == domain.AgentRunning {
		return nil, 0, apperror.New(apperror.CodeFailedPrecondition,
			"Specialist schedule requires one non-running active root Agent")
	}
	ready := make([]domain.AgentNode, 0, len(agentIDs))
	terminal := 0
	for _, agentID := range agentIDs {
		child, found := byID[agentID]
		if !found || child.Role != domain.AgentRoleSpecialist || child.ParentID != root.ID ||
			child.RunID != runID {
			return nil, 0, apperror.New(apperror.CodeFailedPrecondition,
				fmt.Sprintf("Agent %s is not a direct Specialist child of this Run", agentID))
		}
		switch {
		case child.Terminal():
			terminal++
		case child.Status == domain.AgentReady:
			ready = append(ready, child)
		case child.Status == domain.AgentWaiting:
			// Waiting children remain dormant until an authenticated wake/message transition.
		case child.Status == domain.AgentRunning:
			if child.Worker != nil {
				if state, ok := s.runner.store.(interface {
					SpecialistToolApprovalState(context.Context, domain.AgentAttemptRef) (bool, bool, error)
				}); ok {
					waiting, _, err := state.SpecialistToolApprovalState(ctx, domain.AgentAttemptRef{RunID: runID, AgentID: child.ID, AttemptID: child.ActiveAttemptID})
					if err != nil {
						return nil, 0, err
					}
					if waiting {
						ready = append(ready, child)
						continue
					}
				}
			}
			return nil, 0, apperror.New(apperror.CodeConflict,
				fmt.Sprintf("Specialist %s remained running after lease recovery", child.ID))
		default:
			return nil, 0, apperror.New(apperror.CodeFailedPrecondition,
				fmt.Sprintf("Specialist %s has unsupported scheduling status %s", child.ID, child.Status))
		}
	}
	return ready, terminal, nil
}

type specialistRoundOutcome struct {
	index   int
	agentID string
	result  SpecialistTurnResult
	err     error
}

func (s *SpecialistScheduler) runSpecialistRound(ctx context.Context,
	lease domain.RunExecutionLease, children []domain.AgentNode,
	limits map[string]specialistTurnLimits,
) []specialistRoundOutcome {
	roundCtx, cancelRound := context.WithCancel(ctx)
	defer cancelRound()
	outcomeCh := make(chan specialistRoundOutcome, len(children))
	for index, child := range children {
		index, child := index, child
		go func() {
			childCtx, releaseWait, waitErr := waitgraph.Enter(roundCtx, s.waitGraph,
				waitgraph.Agent(child.ParentID), waitgraph.Agent(child.ID))
			if waitErr != nil {
				outcomeCh <- specialistRoundOutcome{index: index, agentID: child.ID,
					err: apperror.New(apperror.CodeConflict,
						"Specialist synchronous dependency was rejected: "+waitErr.Error())}
				return
			}
			defer releaseWait()
			result, err := s.runSpecialistChild(childCtx, lease, child, limits[child.ID])
			outcomeCh <- specialistRoundOutcome{
				index: index, agentID: child.ID, result: result, err: err,
			}
		}()
	}
	outcomes := make([]specialistRoundOutcome, 0, len(children))
	for range children {
		outcome := <-outcomeCh
		outcomes = append(outcomes, outcome)
		if outcome.err != nil {
			cancelRound()
		}
	}
	sort.Slice(outcomes, func(i, j int) bool { return outcomes[i].index < outcomes[j].index })
	return outcomes
}

func (s *SpecialistScheduler) runSpecialistChild(ctx context.Context,
	lease domain.RunExecutionLease, child domain.AgentNode,
	limits specialistTurnLimits,
) (result SpecialistTurnResult, err error) {
	result = SpecialistTurnResult{
		RunID: child.RunID, AgentID: child.ID, ParentAgentID: child.ParentID,
		SessionID: child.SessionID,
	}
	defer func() {
		if recover() == nil {
			return
		}
		cause := apperror.New(apperror.CodeInternal,
			"Specialist child runtime panicked")
		if result.AttemptID == "" || result.AttemptStatus != domain.AgentAttemptRunning {
			err = cause
			return
		}
		err = s.runner.failAttempt(ctx, &result, domain.AgentAttemptRef{
			RunID: result.RunID, AgentID: result.AgentID, AttemptID: result.AttemptID,
		}, cause)
	}()
	err = s.runner.stepReadyWithLease(ctx, lease, &result, limits)
	return result, err
}

func normalizeSpecialistScheduleRequest(
	request SpecialistScheduleRequest,
) (SpecialistScheduleResult, error) {
	result := SpecialistScheduleResult{RunID: strings.TrimSpace(request.RunID)}
	result.OperatorRequestID = strings.TrimSpace(request.OperatorRequestID)
	result.ContinuationDigest = request.ContinuationDigest
	if result.RunID == "" {
		return result, apperror.New(apperror.CodeInvalidArgument,
			"Specialist schedule Run id is required")
	}
	if request.MaxRounds <= 0 || request.MaxRounds > MaxSpecialistScheduleRounds {
		return result, apperror.New(apperror.CodeInvalidArgument,
			fmt.Sprintf("Specialist schedule rounds must be between 1 and %d",
				MaxSpecialistScheduleRounds))
	}
	if result.OperatorRequestID != "" &&
		(!domain.ValidAgentID(result.OperatorRequestID) ||
			strings.ContainsRune(result.OperatorRequestID, 0)) {
		return result, apperror.New(apperror.CodeInvalidArgument,
			"Specialist operator schedule request id is invalid")
	}
	if len(request.AgentIDs) == 0 || len(request.AgentIDs) > domain.MaxAgentChildren {
		return result, apperror.New(apperror.CodeInvalidArgument,
			fmt.Sprintf("Specialist schedule requires between 1 and %d child Agents",
				domain.MaxAgentChildren))
	}
	seen := make(map[string]struct{}, len(request.AgentIDs))
	for _, value := range request.AgentIDs {
		agentID := strings.TrimSpace(value)
		if agentID == "" {
			return result, apperror.New(apperror.CodeInvalidArgument,
				"Specialist schedule Agent ids are required")
		}
		if _, exists := seen[agentID]; exists {
			return result, apperror.New(apperror.CodeInvalidArgument,
				"Specialist schedule Agent ids must be unique")
		}
		seen[agentID] = struct{}{}
		result.AgentIDs = append(result.AgentIDs, agentID)
	}
	sort.Strings(result.AgentIDs)
	return result, nil
}

func specialistAggregateBudgetStop(budget domain.Budget,
	usage domain.RunAgentUsage,
) SpecialistScheduleStopReason {
	if budget.MaxTokens > 0 && usage.TotalTokens >= budget.MaxTokens {
		return SpecialistScheduleTokenBudget
	}
	if budget.TimeoutSeconds > 0 &&
		usage.TotalExecutionMillis >= budget.TimeoutSeconds*1000 {
		return SpecialistScheduleExecutionBudget
	}
	return ""
}

func specialistRoundLimits(budget domain.Budget, usage domain.RunAgentUsage,
	ready []domain.AgentNode,
) (map[string]specialistTurnLimits, []domain.AgentNode, SpecialistScheduleStopReason) {
	scheduled := append([]domain.AgentNode(nil), ready...)
	if budget.MaxTokens > 0 {
		remaining := budget.MaxTokens - usage.TotalTokens
		if remaining <= 0 {
			return nil, nil, SpecialistScheduleTokenBudget
		}
		if int64(len(scheduled)) > remaining {
			scheduled = scheduled[:int(remaining)]
		}
	}
	if budget.TimeoutSeconds > 0 {
		remaining := budget.TimeoutSeconds*1000 - usage.TotalExecutionMillis
		if remaining <= 0 {
			return nil, nil, SpecialistScheduleExecutionBudget
		}
		if int64(len(scheduled)) > remaining {
			scheduled = scheduled[:int(remaining)]
		}
	}
	limits := make(map[string]specialistTurnLimits, len(scheduled))
	for _, child := range scheduled {
		limits[child.ID] = specialistTurnLimits{}
	}
	if budget.MaxTokens > 0 {
		share := (budget.MaxTokens - usage.TotalTokens) / int64(len(scheduled))
		for _, child := range scheduled {
			limit := limits[child.ID]
			limit.MaxTotalTokens = max(share, 1)
			limits[child.ID] = limit
		}
	}
	if budget.TimeoutSeconds > 0 {
		share := (budget.TimeoutSeconds*1000 - usage.TotalExecutionMillis) /
			int64(len(scheduled))
		for _, child := range scheduled {
			limit := limits[child.ID]
			limit.MaxExecutionMillis = max(share, 1)
			limits[child.ID] = limit
		}
	}
	return limits, scheduled, ""
}

func selectSpecialistRoundError(outcomes []specialistRoundOutcome) error {
	for _, outcome := range outcomes {
		if apperror.CodeOf(apperror.Normalize(outcome.err)) != apperror.CodeCancelled {
			return apperror.Normalize(outcome.err)
		}
	}
	return apperror.Normalize(outcomes[0].err)
}
