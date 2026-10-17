package application

import (
	"context"
	"encoding/json"
	"errors"
	"time"
	"unicode/utf8"

	"cyberagent-workbench/internal/apperror"
	"cyberagent-workbench/internal/contextmgr"
	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/llm"
)

type ThreadContextCompactionStore interface {
	MonetaryBudgetStore
	BeginThreadContextCompaction(context.Context, domain.ThreadContextCompactionRequest) (domain.ThreadContextCompactionOperation, domain.ThreadContextCompactionSnapshot, error)
	PrepareThreadContextCompactionCall(context.Context, int64, string, string, int, string, int) error
	StartThreadContextCompactionCall(context.Context, int64) error
	CompleteThreadContextCompactionCall(context.Context, int64, string, *llm.Usage, time.Duration, error) (domain.ThreadContextCompactionOperation, error)
	CommitThreadContextCompaction(context.Context, int64, contextmgr.Result) (domain.ThreadContextCompactionResult, error)
	FailThreadContextCompaction(context.Context, int64, error) error
}

type ThreadContextCompactionService struct {
	store     ThreadContextCompactionStore
	router    *llm.Router
	execution interface {
		ExecutionState(context.Context, string) (ThreadExecutionState, error)
	}
}

func NewThreadContextCompactionService(store ThreadContextCompactionStore, router *llm.Router) *ThreadContextCompactionService {
	return &ThreadContextCompactionService{store: store, router: router}
}

func (s *ThreadContextCompactionService) WithExecutionState(observer interface {
	ExecutionState(context.Context, string) (ThreadExecutionState, error)
}) *ThreadContextCompactionService {
	s.execution = observer
	return s
}

func (s *ThreadContextCompactionService) Compact(ctx context.Context, request domain.ThreadContextCompactionRequest) (view domain.ThreadContextCompactionResult, resultErr error) {
	if s == nil || s.store == nil || s.router == nil {
		return view, apperror.New(apperror.CodeFailedPrecondition, "Thread context compaction dependencies are unavailable")
	}
	request, err := request.Normalize()
	if err != nil {
		return view, err
	}
	operation, snapshot, err := s.store.BeginThreadContextCompaction(ctx, request)
	if err != nil {
		return view, apperror.Normalize(err)
	}
	if operation.Status == "completed" || operation.Status == "noop" {
		return operation.Result, nil
	}
	defer func() {
		if resultErr != nil {
			persistCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
			defer cancel()
			resultErr = errors.Join(resultErr, s.store.FailThreadContextCompaction(persistCtx, operation.ID, resultErr))
		}
	}()
	if s.execution != nil {
		state, err := s.execution.ExecutionState(ctx, request.ThreadID)
		if err != nil {
			return view, err
		}
		if state.State != "idle" {
			return view, apperror.New(apperror.CodeConflict, "thread_context_busy: wait for the current task to finish")
		}
	}
	const preserveRecent = 4
	if len(snapshot.Messages) <= preserveRecent {
		return s.store.CommitThreadContextCompaction(ctx, operation.ID, contextmgr.Result{Summary: snapshot.Summary, Preserved: snapshot.Messages})
	}
	ref, err := supervisorModelRef(s.router, snapshot.Run.Config.ModelRoute)
	if err != nil {
		return view, err
	}
	if s.router.RequiresPrivateAssistantHistory(ref) {
		return view, apperror.New(apperror.CodeFailedPrecondition, "thread_context_unsupported: native private assistant history cannot be replaced by a context summary; select a compatible model for the next Thread Run")
	}
	generator := &threadContextSummaryGenerator{service: s, operation: operation, snapshot: snapshot, focus: request.Focus}
	config := contextmgr.DefaultConfig()
	config.PreserveRecentMessages = preserveRecent
	result, err := contextmgr.NewManager(nil, config).WithSummaryGenerator(generator, operation.SourceSHA256).PrepareCandidate(ctx, snapshot.Run.SessionID, snapshot.Mission.WorkspaceID, snapshot.Messages, snapshot.Summary, snapshot.HasSummary)
	if err != nil {
		return view, apperror.Normalize(err)
	}
	if !result.Generated {
		return view, apperror.New(apperror.CodeFailedPrecondition, "context summary generation failed; the original active history was retained")
	}
	if err = ctx.Err(); err != nil {
		return view, apperror.Normalize(err)
	}
	return s.store.CommitThreadContextCompaction(ctx, operation.ID, result)
}

type threadContextSummaryGenerator struct {
	service   *ThreadContextCompactionService
	operation domain.ThreadContextCompactionOperation
	snapshot  domain.ThreadContextCompactionSnapshot
	focus     string
}

func (g *threadContextSummaryGenerator) Generate(ctx context.Context, input contextmgr.SummaryGenerationRequest) (contextmgr.SummaryGenerationResponse, error) {
	response, err := g.generate(ctx, input)
	if err != nil {
		return response, errors.Join(contextmgr.ErrSummaryGenerationAborted, err)
	}
	return response, nil
}
func (g *threadContextSummaryGenerator) generate(ctx context.Context, input contextmgr.SummaryGenerationRequest) (contextmgr.SummaryGenerationResponse, error) {
	var result contextmgr.SummaryGenerationResponse
	money := NewMonetaryBudgetService(g.service.store)
	o := g.operation
	attempt := llm.ModelAttempt{Number: int(o.ID), Provider: o.Provider, Model: o.Model}
	if o.Status == "generated" {
		if len(o.Usage) > 0 && string(o.Usage) != "null" {
			var usage llm.Usage
			if err := json.Unmarshal(o.Usage, &usage); err != nil {
				return result, err
			}
			_, err := money.SettleModelCall(ctx, o.RunID, domain.MonetaryScopeContextCompaction, attempt, usage, 0)
			if err != nil {
				return result, err
			}
		}
		return threadContextSummaryResponse(o), nil
	}
	ref, err := supervisorModelRef(g.service.router, g.snapshot.Run.Config.ModelRoute)
	if err != nil {
		return result, err
	}
	prepared, err := g.service.router.PrepareModelRequest(ref, llm.ChatRequest{Model: ref.Model})
	if err != nil {
		return result, err
	}
	window, _ := prepared.PreparedContextWindow()
	request, plan, inputFingerprint, inputOmitted, coverage, err := g.prepareSummaryRequest(ctx, prepared, window, input)
	if err != nil {
		return result, err
	}
	if err = g.service.router.ValidatePreparedRequest(ref, request); err != nil {
		return result, err
	}
	if err = g.service.store.PrepareThreadContextCompactionCall(ctx, o.ID, ref.Provider, ref.Model, plan.EstimatedInput+request.MaxTokens, inputFingerprint, inputOmitted); err != nil {
		return result, err
	}
	attempt.Provider, attempt.Model = ref.Provider, ref.Model
	if _, err = money.ReserveModelCall(ctx, g.snapshot.Run, domain.MonetaryScopeContextCompaction, attempt, request); err != nil {
		return result, err
	}
	if err = g.service.store.StartThreadContextCompactionCall(ctx, o.ID); err != nil {
		cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		_, releaseErr := money.ReleaseModelCall(cleanup, o.RunID, domain.MonetaryScopeContextCompaction, attempt)
		return result, errors.Join(err, releaseErr)
	}
	callCtx, cancel := context.WithTimeout(ctx, 90*time.Second)
	started := time.Now()
	received, callErr := g.service.router.ChatModelRef(callCtx, ref, request)
	cancelled := callCtx.Err()
	cancel()
	var text string
	var usage *llm.Usage
	if received != nil {
		if received.Usage.Validate() == nil && (received.Usage.InputTokens > 0 || received.Usage.OutputTokens > 0 || received.Usage.TotalTokens > 0) {
			copy := received.Usage
			if copy.TotalTokens == 0 {
				copy.TotalTokens = copy.InputTokens + copy.OutputTokens
			}
			usage = &copy
		}
		if callErr == nil {
			if completionErr := llm.CompletionError(ref.Provider, received.FinishReason); completionErr != nil {
				callErr = completionErr
			}
		}
		if callErr == nil && len(received.ToolCalls) > 0 {
			callErr = errors.New("context summary returned unexpected tool calls")
		}
		if callErr == nil {
			text, callErr = contextmgr.ParseSummaryGenerationText(received.Text)
			if callErr == nil && coverage != "" {
				if utf8.RuneCountInString(text)+utf8.RuneCountInString(coverage)+1 > contextmgr.MaxGeneratedSummaryChars {
					callErr = errors.New("generated summary exceeded the space reserved for its input coverage notice")
				} else {
					text += "\n" + coverage
				}
			}
		}
	} else if callErr == nil {
		callErr = errors.New("context summary provider returned no response")
	}
	persistCtx, persistCancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer persistCancel()
	completed, persistErr := g.service.store.CompleteThreadContextCompactionCall(persistCtx, o.ID, text, usage, time.Since(started), callErr)
	var accountingErr error
	if errors.Is(callErr, llm.ErrPreparedRequestChanged) {
		_, accountingErr = money.ReleaseModelCall(persistCtx, o.RunID, domain.MonetaryScopeContextCompaction, attempt)
	} else if usage != nil {
		_, accountingErr = money.SettleModelCall(persistCtx, o.RunID, domain.MonetaryScopeContextCompaction, attempt, *usage, 0)
	} else {
		_, accountingErr = money.SettleUnknownModelCall(persistCtx, o.RunID, domain.MonetaryScopeContextCompaction, attempt)
	}
	if err = errors.Join(callErr, persistErr, accountingErr, cancelled, ctx.Err()); err != nil {
		return result, err
	}
	return threadContextSummaryResponse(completed), nil
}
func threadContextSummaryResponse(o domain.ThreadContextCompactionOperation) contextmgr.SummaryGenerationResponse {
	return contextmgr.SummaryGenerationResponse{Text: o.ResponseText, InputFingerprint: o.InputFingerprint, InputMessagesOmitted: o.InputMessagesOmitted, Receipt: contextmgr.SummaryGenerationReceipt{RunID: o.RunID, AttemptID: o.OperationID, ModelAttempt: 1, CompletionSequence: o.CompletionSequence, Provider: o.Provider, Model: o.Model, SourceSHA256: o.SourceSHA256}}
}
