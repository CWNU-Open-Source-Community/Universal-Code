package application_test

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"cyberagent-workbench/internal/application"
	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/llm"
	"cyberagent-workbench/internal/policy"
	"cyberagent-workbench/internal/store"
)

type wakeReviewDueClock struct {
	*store.SQLiteStore
	now time.Time
}

func (s wakeReviewDueClock) ListDueRunWakeIntents(ctx context.Context, _ time.Time, limit, maxSteps int) ([]domain.RunWakeIntent, error) {
	return s.SQLiteStore.ListDueRunWakeIntents(ctx, s.now, limit, maxSteps)
}

func TestRunWakeWorkerSkipsPreparedStepLimitMismatch(t *testing.T) {
	state, err := store.Open(filepath.Join(t.TempDir(), "prepared-steps.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer state.Close()
	provider := &lifecycleProvider{responses: []string{rootActionResponse(domain.RootActionContinue, "healthy wake", "", "")}}
	blocked := createScheduledWakeTestRun(t, state, provider.Name()+"/model", "prepared-two-steps")
	pending, err := application.NewForegroundRunWakeConsumer(state, pendingWakeHandoff{}).Consume(t.Context(), application.ConsumeRunWakeRequest{
		Version: domain.RunWakeConsumerProtocolVersion, RunID: blocked.ID, OwnerID: "two-step-owner", MaxSteps: 2,
	})
	if err == nil || pending.Consumption.Status != domain.RunWakeConsumptionPrepared {
		t.Fatalf("prepare=%#v err=%v", pending, err)
	}
	healthy := createScheduledWakeTestRun(t, state, provider.Name()+"/model", "healthy-after-prepared")
	router := llm.NewRouter(llm.ModelRef{Provider: provider.Name(), Model: "model"})
	router.RegisterProvider(provider)
	worker, err := application.NewRunWakeWorker(application.NewRunWakeCoordinator(wakeReviewDueClock{state, pending.Intent.DeadlineAt.Add(time.Second)}),
		application.NewForegroundRunWakeConsumer(state, application.NewRunExecutionHandoffService(state, router, policy.NewDefaultChecker())),
		application.RunWakeWorkerConfig{OwnerID: "one-step-worker"})
	if err != nil {
		t.Fatal(err)
	}
	for attempt := 0; attempt < 2; attempt++ {
		worked, err := worker.RunOnce(t.Context())
		t.Logf("tick %d: worked=%t calls=%d err=%v", attempt, worked, provider.calls, err)
	}
	wake, found, err := state.GetLatestRunWakeIntent(t.Context(), healthy.ID)
	if err != nil || !found || wake.Status != domain.RunWakeCompleted || provider.calls != 1 {
		t.Fatalf("prepared step limit blocked healthy wake: status=%s calls=%d err=%v", wake.Status, provider.calls, err)
	}
	prepared, found, err := state.GetRunWakeConsumption(t.Context(), pending.Intent.ID, pending.Intent.AttemptCount)
	if err != nil || !found || prepared != pending.Consumption {
		t.Fatalf("worker changed the original prepared consumption: %#v err=%v", prepared, err)
	}
	due, err := state.ListDueRunWakeIntents(t.Context(), pending.Intent.DeadlineAt.Add(time.Second), 1, 2)
	if err != nil || len(due) != 1 || due[0].ID != pending.Intent.ID {
		t.Fatalf("original two-step replay is no longer selectable: %#v err=%v", due, err)
	}
}
