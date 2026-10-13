package application

import (
	"context"
	"errors"
	"strings"

	"cyberagent-workbench/internal/apperror"
	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/llm"
	"cyberagent-workbench/internal/policy"
	"cyberagent-workbench/internal/runmutation"
)

// SpecialistExecutionService is an operator entry point for an already admitted
// child, including child_task admissions. Models cannot construct this service.
type SpecialistExecutionService struct {
	store          SubagentRunnerStore
	router         *llm.Router
	checker        policy.Checker
	drydocks       *RunWorktreeService
	summaryHandoff RunWakeExecutionHandoff
}

func (s *SpecialistExecutionService) WithCompletionSummary(handoff RunWakeExecutionHandoff) *SpecialistExecutionService {
	s.summaryHandoff = handoff
	return s
}

func NewSpecialistExecutionService(st SubagentRunnerStore, router *llm.Router, checker policy.Checker) *SpecialistExecutionService {
	return &SpecialistExecutionService{store: st, router: router, checker: checker}
}

func (s *SpecialistExecutionService) WithRunWorktree(drydocks *RunWorktreeService) *SpecialistExecutionService {
	s.drydocks = drydocks
	return s
}

type ExecuteSpecialistRequest struct {
	RunID, AgentID, OperationKey string
	WorkspaceTools               bool
}

func (s *SpecialistExecutionService) ExecuteSpecialist(ctx context.Context, request ExecuteSpecialistRequest) (SpecialistTurnResult, error) {
	key, err := domain.NormalizeAgentOperationKey(request.OperationKey)
	if err != nil {
		return SpecialistTurnResult{}, apperror.Wrap(apperror.CodeInvalidArgument, "a stable child execution operation key is required", err)
	}
	runner := NewSubagentRunner(s.store, s.router, s.checker)
	runner.WithRunWorktree(s.drydocks)
	runner.operationKey = key
	if request.WorkspaceTools {
		runner.WithWorkspaceTools()
	}
	if st, ok := s.store.(MonetaryBudgetStore); ok {
		runner.WithMonetaryBudget(NewMonetaryBudgetService(st))
	}
	return runner.Step(ctx, request.RunID, request.AgentID)
}

func (r *SubagentRunner) operationReplay(ctx context.Context, result *SpecialistTurnResult) (bool, error) {
	if r.operationKey == "" {
		return false, nil
	}
	st, ok := r.store.(interface {
		GetAgentAttempt(context.Context, string) (domain.AgentAttempt, bool, error)
	})
	if !ok {
		return false, errors.New("operator child execution requires durable attempt lookup")
	}
	id := "attempt-" + runmutation.Fingerprint("specialist_operator_step.v1", result.RunID, result.AgentID, r.operationKey)
	attempt, found, err := st.GetAgentAttempt(ctx, id)
	if err != nil || !found || !attempt.Status.Terminal() {
		return false, err
	}
	if attempt.RunID != result.RunID || attempt.AgentID != result.AgentID || attempt.ParentAgentID != result.ParentAgentID {
		return false, errors.New("operator child attempt identity changed")
	}
	// The same operation key cannot switch between the no-tool and tool modes.
	toolMode := false
	if st, ok := r.store.(interface {
		GetSpecialistToolBinding(context.Context, domain.AgentAttemptRef) (domain.SpecialistToolBinding, error)
	}); ok {
		binding, e := st.GetSpecialistToolBinding(ctx, specialistAttemptRef(attempt))
		toolMode = e == nil && binding.Mode != ""
	}
	if toolMode != r.workspaceTools && attempt.UsageRecordedAt != nil {
		return false, apperror.New(apperror.CodeConflict, "child operation key was used with a different tool profile")
	}
	result.AttemptID = attempt.ID
	result.Turn = attempt.Turn
	result.AttemptStatus = attempt.Status
	result.Usage = attempt.Usage
	result.Replayed = true
	if counter, ok := r.store.(interface {
		CountSpecialistModelAttempts(context.Context, domain.AgentAttemptRef) (int, error)
	}); ok {
		count, e := counter.CountSpecialistModelAttempts(ctx, specialistAttemptRef(attempt))
		if e != nil {
			return true, e
		}
		result.ModelAttempts = count
	}
	if toolMode {
		if st, ok := r.store.(interface {
			ListSpecialistToolRounds(context.Context, domain.AgentAttemptRef) ([]domain.SpecialistToolRound, error)
		}); ok {
			rounds, e := st.ListSpecialistToolRounds(ctx, specialistAttemptRef(attempt))
			if e != nil {
				return true, e
			}
			result.ToolRounds = len(rounds)
			for _, round := range rounds {
				result.ToolCalls += len(round.Calls)
			}
		}
	}
	if attempt.Status == domain.AgentAttemptCrashed || attempt.Status == domain.AgentAttemptInterrupted {
		return true, apperror.New(apperror.CodeFailedPrecondition, "the recorded child execution ended: "+strings.TrimSpace(attempt.Failure.Code))
	}
	return true, nil
}
