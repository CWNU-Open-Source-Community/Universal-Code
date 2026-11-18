package application_test

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"cyberagent-workbench/internal/apperror"
	"cyberagent-workbench/internal/application"
	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/events"
	"cyberagent-workbench/internal/llm"
	"cyberagent-workbench/internal/policy"
	"cyberagent-workbench/internal/session"
	"cyberagent-workbench/internal/store"
)

// Exercise the root scheduling branch for a capacity below the model window.
// The durable recovery ceiling itself is tested with SQLite/reopen separately.
type preparedCapacityStore struct {
	*store.SQLiteStore
	inputCap int
}

func (s preparedCapacityStore) SupervisorContextRecoveryInputLimit(context.Context, domain.SupervisorCheckpoint, int, int) (int, bool, error) {
	return s.inputCap, true, nil
}

func TestSupervisorPreparedCapacityCompactsHistoryBeforeDispatch(t *testing.T) {
	st, run, _, request := toolBoundaryFixture(t, domain.Budget{MaxTurns: 3})
	for i := 0; i < 6; i++ {
		if _, err := st.SaveSessionMessage(t.Context(), session.NewMessage(run.SessionID, "user", strings.Repeat("Prior observation remains recallable. ", 150))); err != nil {
			t.Fatal(err)
		}
	}
	provider := &scriptedToolProvider{respond: func(req llm.ChatRequest, index int) (*llm.ChatResponse, error) {
		if index == 0 {
			if req.Metadata["purpose"] != "context_compaction" {
				t.Fatal("narrow capacity skipped durable history compaction")
			}
			return textResponse(`{"version":"generated_handoff.v1","summary":"Past observations can be recalled through exact history sources."}`), nil
		}
		if index != 1 {
			t.Fatalf("unexpected extra request: %d", index)
		}
		input, _ := strconv.Atoi(req.Metadata["context_input_estimate"])
		if input > 16000 || req.Metadata["context_summary_id"] == "" || req.Metadata["context_history_omitted"] != "0" {
			t.Fatalf("lossy/unbounded request dispatched: %v", req.Metadata)
		}
		return textResponse(rootActionResponse(domain.RootActionFinish, "Reply completed.", "reply", "")), nil
	}}
	wrapped := preparedCapacityStore{SQLiteStore: st, inputCap: 16000}
	router := llm.NewRouter(llm.ModelRef{Provider: provider.Name(), Model: "model"})
	router.RegisterProvider(provider)
	service := application.NewThreadTurnService(st, application.NewRunLifecycleControlService(wrapped), application.NewRunExecutionHandoffService(wrapped, router, policy.NewDefaultChecker()))
	if _, err := service.Execute(t.Context(), request); err != nil {
		t.Fatal(err)
	}
	if len(provider.Requests()) != 2 {
		t.Fatal("capacity created a retry instead of local compaction")
	}
}

func TestSupervisorPreparedZeroCapacityDeniesBeforeAnyProviderCall(t *testing.T) {
	st, _, _, request := toolBoundaryFixture(t, domain.Budget{MaxTurns: 3})
	provider := &scriptedToolProvider{respond: func(llm.ChatRequest, int) (*llm.ChatResponse, error) {
		t.Fatal("zero prepared input capacity was dispatched")
		return nil, nil
	}}
	wrapped := preparedCapacityStore{SQLiteStore: st, inputCap: 0}
	_, err := toolBoundaryService(wrapped, st, provider).Execute(t.Context(), request)
	if apperror.CodeOf(err) != apperror.CodeResourceExhausted || len(provider.Requests()) != 0 {
		t.Fatalf("budget not enforced before dispatch: %v", err)
	}
}

type receiptRetryUsageProvider struct {
	scriptedToolProvider
	charged     int64
	priorTokens int64
}

func (p *receiptRetryUsageProvider) StreamChat(ctx context.Context, request llm.ChatRequest) (<-chan llm.ChatChunk, error) {
	if request.Metadata["context_segment_receipted_rounds"] == "" && request.Metadata["context_boundary_receipts"] == "" {
		return p.scriptedToolProvider.StreamChat(ctx, request)
	}
	if _, err := p.Chat(ctx, request); err != nil {
		return nil, err
	}
	input, _ := strconv.Atoi(request.Metadata["context_input_estimate"])
	prior := p.priorTokens
	if prior == 0 {
		prior = 8
	}
	p.charged = 60000 - prior - int64(input) - int64(request.MaxTokens) + 200
	usage := llm.Usage{InputTokens: int(p.charged), TotalTokens: int(p.charged)}
	chunks := make(chan llm.ChatChunk, 2)
	chunks <- llm.ChatChunk{Done: true, Usage: &usage, Err: &llm.ProviderError{Kind: llm.OutcomeRetryable, Reason: llm.ProviderFailureNetwork, Provider: p.Name(), Message: "charged transport failure"}}
	close(chunks)
	return chunks, nil
}

func TestSupervisorReceiptTransportRetryRechecksChargedUsage(t *testing.T) {
	st, run, root, request := toolBoundaryFixture(t, domain.Budget{MaxTurns: 3, MaxToolCalls: 4, MaxTokens: 60000})
	if err := os.WriteFile(filepath.Join(root, "README.md"), []byte(strings.Repeat("Large local observation remains untrusted evidence. ", 300)), 0600); err != nil {
		t.Fatal(err)
	}
	provider := &receiptRetryUsageProvider{}
	provider.respond = func(req llm.ChatRequest, index int) (*llm.ChatResponse, error) {
		switch index {
		case 0:
			return toolResponse("small", "work_item_create", `{"title":"Observe file","priority":"high"}`), nil
		case 1:
			return boundaryRead("read-once", 1), nil
		case 2:
			return nil, &llm.ProviderError{Kind: llm.OutcomePermanent, Reason: llm.ProviderFailureContextLimit, Provider: provider.Name(), Message: "input limit"}
		case 3:
			if req.Metadata["context_segment_receipted_rounds"] == "" {
				t.Fatal("failure was not on a receipt request")
			}
			return textResponse("unused charged stream response"), nil
		default:
			t.Fatal("receipt was retried beyond its remaining total token budget")
			return nil, nil
		}
	}
	_, err := toolBoundaryService(st, st, provider).Execute(t.Context(), request)
	if apperror.CodeOf(err) != apperror.CodeResourceExhausted || len(provider.Requests()) != 4 {
		t.Fatalf("receipt retry budget not fenced: requests=%d err=%v", len(provider.Requests()), err)
	}
	cp, found, checkpointErr := st.GetSupervisorCheckpoint(t.Context(), run.ID)
	if checkpointErr != nil || !found || cp.TotalTokens != provider.charged+8 {
		t.Fatalf("failure usage lost or double counted: %d err=%v", cp.TotalTokens, checkpointErr)
	}
	rounds, err := st.ListRunSupervisorToolRoundsPage(t.Context(), run.ID, 0, 20)
	if err != nil || len(rounds) != 2 {
		t.Fatal("retry repeated a completed tool", err)
	}
}

func TestSupervisorExhaustedRecoveryRestartRejectsBeforeSummary(t *testing.T) {
	filename := filepath.Join(t.TempDir(), "exhausted-restart.db")
	st, err := store.Open(filename)
	if err != nil {
		t.Fatal(err)
	}
	run := newStartedRunForProvider(t, st, "tool-loop", domain.Budget{MaxTurns: 3})
	turn, err := st.BeginSupervisorTurn(t.Context(), acquireTestRunExecutionLease(t, t.Context(), st, run.ID), "continue")
	if err != nil {
		t.Fatal(err)
	}
	cp := turn.Checkpoint
	attempt := llm.ModelAttempt{SupervisorAttemptID: cp.AttemptID, Number: 1, TransportAttempt: 1, MaxAttempts: 3, Provider: "tool-loop", Model: "model", InputEstimate: 16000}
	if _, err := st.RecordSupervisorModelStarted(t.Context(), cp, attempt); err != nil {
		t.Fatal(err)
	}
	attempt.Outcome, attempt.FailureReason, attempt.ErrorText = llm.OutcomePermanent, llm.ProviderFailureContextLimit, "input limit"
	cp, err = st.RecordSupervisorModelFailed(t.Context(), cp, attempt)
	if err != nil {
		t.Fatal(err)
	}
	if claimed, err := st.ClaimSupervisorContextRecovery(t.Context(), cp, attempt); !claimed || err != nil {
		t.Fatal(err)
	}
	attempt.Number, attempt.InputEstimate, attempt.Outcome, attempt.FailureReason, attempt.ErrorText = 2, 15999, "", "", ""
	if _, err := st.RecordSupervisorModelStarted(t.Context(), cp, attempt); err != nil {
		t.Fatal(err)
	}
	attempt.Outcome, attempt.FailureReason, attempt.ErrorText = llm.OutcomePermanent, llm.ProviderFailureContextLimit, "second input limit"
	cp, err = st.RecordSupervisorModelFailed(t.Context(), cp, attempt)
	if err != nil {
		t.Fatal(err)
	}
	// More than the normal history threshold: automatic compaction itself
	// would issue a summary unless exhausted recovery is checked first.
	for i := 0; i < 24; i++ {
		if _, err := st.SaveSessionMessage(t.Context(), session.NewMessage(run.SessionID, "user", strings.Repeat("Late historical observation. ", 180))); err != nil {
			t.Fatal(err)
		}
	}
	releaseTestRunExecutionLease(t, t.Context(), st, run.ID)
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	st, err = store.Open(filename)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	provider := &scriptedToolProvider{respond: func(llm.ChatRequest, int) (*llm.ChatResponse, error) {
		t.Fatal("exhausted recovery dispatched root or auxiliary summary")
		return nil, nil
	}}
	_, err = newToolLoopSupervisor(st, provider).WithGeneratedContextCompaction(true).Step(t.Context(), run.ID)
	if apperror.CodeOf(err) != apperror.CodeResourceExhausted || len(provider.Requests()) != 0 {
		t.Fatal("exhausted recovery did not fail before summary", err)
	}
	log, err := st.ListRunEvents(t.Context(), run.ID)
	if err != nil || countEventType(log, events.ModelStartedEvent) != 2 {
		t.Fatal("restart inserted new model work", err)
	}
}

func TestSupervisorBoundaryOnlyRechecksRemainingTotalBudget(t *testing.T) {
	st, _, _, request := toolBoundaryFixture(t, domain.Budget{MaxTurns: 3, MaxToolCalls: int64(domain.MaxSupervisorToolRounds + 8), MaxTokens: 1000})
	provider := &scriptedToolProvider{respond: func(req llm.ChatRequest, index int) (*llm.ChatResponse, error) {
		if index < domain.MaxSupervisorToolRounds {
			return toolResponse(fmt.Sprint("item-", index), "work_item_create", fmt.Sprintf(`{"title":"Observed %d","priority":"high"}`, index)), nil
		}
		if index == domain.MaxSupervisorToolRounds {
			assertBoundaryPrompt(t, req)
			return textResponse(rootActionResponse(domain.RootActionContinue, "Continue the same accepted task.", "", "")), nil
		}
		t.Fatal("boundary-only request ignored remaining input plus output capacity")
		return nil, nil
	}}
	_, err := toolBoundaryService(st, st, provider).Execute(t.Context(), request)
	if apperror.CodeOf(err) != apperror.CodeResourceExhausted || len(provider.Requests()) != domain.MaxSupervisorToolRounds+1 {
		t.Fatalf("boundary budget: requests=%d err=%v", len(provider.Requests()), err)
	}
}

func TestSupervisorBoundaryTransportRetryRechecksChargedUsage(t *testing.T) {
	st, run, _, request := toolBoundaryFixture(t, domain.Budget{MaxTurns: 3, MaxToolCalls: int64(domain.MaxSupervisorToolRounds + 8), MaxTokens: 60000})
	provider := &receiptRetryUsageProvider{}
	provider.respond = func(req llm.ChatRequest, index int) (*llm.ChatResponse, error) {
		if index < domain.MaxSupervisorToolRounds {
			return toolResponse(fmt.Sprint("item-", index), "work_item_create", fmt.Sprintf(`{"title":"Observed %d","priority":"high"}`, index)), nil
		}
		if index == domain.MaxSupervisorToolRounds {
			assertBoundaryPrompt(t, req)
			return textResponse(rootActionResponse(domain.RootActionContinue, "Continue the same accepted task.", "", "")), nil
		}
		if index == domain.MaxSupervisorToolRounds+1 {
			if req.Metadata["context_boundary_receipts"] == "" || req.Metadata["context_segment_receipted_rounds"] != "" {
				t.Fatal("fixture was not boundary-only")
			}
			cp, found, err := st.GetSupervisorCheckpoint(t.Context(), run.ID)
			if err != nil || !found {
				t.Fatal(err)
			}
			provider.priorTokens = cp.TotalTokens
			return textResponse("unused charged stream response"), nil
		}
		t.Fatal("charged boundary request was retried beyond remaining input plus output allowance")
		return nil, nil
	}
	_, err := toolBoundaryService(st, st, provider).Execute(t.Context(), request)
	if apperror.CodeOf(err) != apperror.CodeResourceExhausted || len(provider.Requests()) != domain.MaxSupervisorToolRounds+2 {
		t.Fatalf("boundary retry budget: requests=%d err=%v", len(provider.Requests()), err)
	}
	cp, found, err := st.GetSupervisorCheckpoint(t.Context(), run.ID)
	if err != nil || !found || cp.TotalTokens != provider.priorTokens+provider.charged {
		t.Fatal("charged usage lost or duplicated", err)
	}
}
