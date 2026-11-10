package application_test

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/llm"
	"cyberagent-workbench/internal/store"
)

func TestWorkflowClaimExpiryDuringLongDrainDoesNotReplayAttempt(t *testing.T) {
	t.Parallel()
	base := &specialistTestProvider{}
	raw, run, _, path := newWorkflowFixture(t, base)
	gate := make(chan struct{})
	close(gate)
	failure := errors.New("claim renewal failed before long drain")
	st := &workflowClaimProbeStore{SQLiteStore: raw, renewalStarted: make(chan struct{}), renewalGate: gate, renewalFailure: failure}
	p := &workflowClaimProvider{specialistTestProvider: base, entered: make(chan struct{}), release: make(chan struct{}), cancelled: make(chan struct{}), drain: make(chan struct{})}
	svc := workflowClaimService(st, p)
	e := startHostWorkflow(t, svc, run.ID, "claim-long-drain-review", workflowSpec(workflowWorker("one")))
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan struct{})
	var oldErr error
	go func() { _, oldErr = svc.Advance(ctx, e.ID); close(done) }()
	var drainOnce sync.Once
	t.Cleanup(func() {
		drainOnce.Do(func() { close(p.drain) })
		cancel()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Error("original interpreter did not drain")
		}
	})
	waitWorkflowClaimSignal(t, p.entered, "original model call")
	waitWorkflowClaimSignal(t, p.cancelled, "model cancellation after renewal failure")
	started := time.Now()
	// Keep the cancelled provider inside its real runner for longer than the
	// complete lease TTL. No stored timestamps or lease generations are changed.
	select {
	case <-time.After(35 * time.Second):
	case <-done:
		t.Fatal("original interpreter exited before its drain gate opened")
	}
	lease, found, err := raw.GetRunExecutionLease(t.Context(), run.ID)
	if err != nil || !found || lease.ActiveAt(time.Now()) {
		t.Fatalf("expected expired original Run lease: lease=%+v found=%t err=%v", lease, found, err)
	}
	second, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()
	replacement := &specialistTestProvider{responses: []llm.ChatResponse{workflowResponse(t, domain.CompletionSucceeded)}}
	secondService := workflowClaimService(second, replacement)
	secondCtx, stopSecond := context.WithTimeout(t.Context(), 8*time.Second)
	defer stopSecond()
	after, err := secondService.Advance(secondCtx, e.ID)
	if err != nil || after.Status != "failed" || after.State.Jobs != 0 || replacement.calls != 0 {
		t.Fatalf("replacement replayed unknown work: execution=%+v model_calls=%d err=%v", after, replacement.calls, err)
	}
	select {
	case <-done:
		t.Fatal("original interpreter exited while its drain gate remained closed")
	default:
	}
	var stepIDs []string
	for _, group := range after.State.Maps {
		for _, item := range group.Items {
			stepIDs = append(stepIDs, item.StepExecutionID)
		}
	}
	if len(stepIDs) != 1 {
		t.Fatalf("frozen step identity changed: %v", stepIDs)
	}
	step, found, err := raw.GetWorkflowStep(t.Context(), stepIDs[0])
	if err != nil || !found || step.Status != "failed" || !strings.Contains(step.Reason, "worker_lost") {
		t.Fatalf("unknown step was not fenced: step=%+v found=%t err=%v", step, found, err)
	}
	attempts, err := raw.ListAgentAttempts(t.Context(), step.AgentID)
	if err != nil || len(attempts) != 1 || attempts[0].Status != domain.AgentAttemptCrashed || attempts[0].Failure.Code != "worker_lost" || attempts[0].Turn != 1 {
		t.Fatalf("unknown attempt was replayed or not fenced: attempts=%+v err=%v", attempts, err)
	}
	drainOnce.Do(func() { close(p.drain) })
	select {
	case <-done:
		if !errors.Is(oldErr, failure) {
			t.Fatalf("original renewal failure was lost: %v", oldErr)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("original interpreter did not finish after drain")
	}
	settled, err := raw.GetWorkflowExecution(t.Context(), e.ID)
	if err != nil || settled.Status != after.Status || settled.Version != after.Version || settled.State.Jobs != after.State.Jobs || base.calls != 1 || replacement.calls != 0 {
		t.Fatalf("old owner changed the replacement checkpoint: after=%+v settled=%+v calls=%d/%d err=%v", after, settled, base.calls, replacement.calls, err)
	}
	t.Logf("actual cancelled drain=%s; replacement model_calls=0; one crashed/worker_lost attempt; old owner retained replacement checkpoint version=%d", time.Since(started).Round(time.Millisecond), settled.Version)
}
