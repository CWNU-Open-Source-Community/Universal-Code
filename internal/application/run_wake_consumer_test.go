package application_test

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"cyberagent-workbench/internal/apperror"
	"cyberagent-workbench/internal/application"
	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/llm"
	"cyberagent-workbench/internal/operationreceipt"
	"cyberagent-workbench/internal/policy"
	"cyberagent-workbench/internal/store"
)

type pendingWakeHandoff struct{}

type failingWakeSettlementStore struct{ *store.SQLiteStore }

func (s failingWakeSettlementStore) CompleteRunWakeConsumption(context.Context,
	string, domain.RunExecutionHandoff, time.Time,
) (domain.RunWakeConsumption, domain.RunWakeIntent, bool, error) {
	return domain.RunWakeConsumption{}, domain.RunWakeIntent{}, false,
		errors.New("injected wake settlement failure")
}

func (pendingWakeHandoff) Execute(context.Context,
	application.ExecuteRunHandoffRequest,
) (application.ExecuteRunHandoffResult, error) {
	return application.ExecuteRunHandoffResult{Handoff: domain.RunExecutionHandoff{
		Operation: domain.RunExecutionHandoffOperation{ID: "run-handoff-still-pending"},
	}}, apperror.New(apperror.CodeConflict, "Run execution lease is active")
}

func TestForegroundRunWakeConsumerExecutesOnceAndSettlesCompleted(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "wake-consumer.db")
	state, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	provider := &lifecycleProvider{responses: []string{
		rootActionResponse(domain.RootActionContinue, "wake delivered", "", ""),
	}}
	_, created, err := application.NewRunService(state).Create(ctx,
		application.CreateRunRequest{Goal: "foreground wake", Profile: "code",
			ModelRoute: provider.Name() + "/model", Budget: domain.Budget{MaxTurns: 4}})
	if err != nil {
		t.Fatal(err)
	}
	run, err := application.NewRunService(state).Start(ctx, created.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := state.EnqueueOperatorSteering(ctx, domain.EnqueueOperatorSteeringRequest{
		RunID: run.ID, SessionID: run.SessionID, Content: "execute from wake",
		OperationKey: "wake-consumer-steering-0001", RequestedBy: "operator",
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := application.NewRunWakeControlService(state).Schedule(ctx,
		application.ScheduleRunWakeRequest{
			Version: domain.RunWakeControlProtocolVersion, RunID: run.ID,
			OperationKey: "wake-consumer-schedule-0001", RequestedBy: "operator",
			MaxAttempts: 3, InitialDelaySeconds: 0, BaseBackoffSeconds: 5,
			MaxBackoffSeconds: 30, MaxElapsedSeconds: 120,
		}); err != nil {
		t.Fatal(err)
	}
	router := llm.NewRouter(llm.ModelRef{Provider: provider.Name(), Model: "model"})
	router.RegisterProvider(provider)
	handoff := application.NewRunExecutionHandoffService(state, router,
		policy.NewDefaultChecker())
	consumer := application.NewForegroundRunWakeConsumer(state, handoff)
	request := application.ConsumeRunWakeRequest{
		Version: domain.RunWakeConsumerProtocolVersion, RunID: run.ID,
		OwnerID: "foreground_test", MaxSteps: 1,
	}
	result, err := consumer.Consume(ctx, request)
	if err != nil || result.Intent.Status != domain.RunWakeCompleted ||
		result.Consumption.Status != domain.RunWakeConsumptionCompleted ||
		result.Handoff.Result == nil || provider.calls != 1 {
		t.Fatalf("consume result=%#v calls=%d err=%v", result, provider.calls, err)
	}
	records, err := state.ListTerminalOperationRecords(ctx, run.ID, 2)
	if err != nil || len(records) != 1 ||
		records[0].Kind != operationreceipt.KindRunWakeConsume ||
		records[0].Outcome != "completed" {
		t.Fatalf("Run wake terminal receipt source=%#v err=%v", records, err)
	}
	if err := state.Close(); err != nil {
		t.Fatal(err)
	}
	state, err = store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer state.Close()
	consumer = application.NewForegroundRunWakeConsumer(state,
		application.NewRunExecutionHandoffService(state, router,
			policy.NewDefaultChecker()))
	replay, err := consumer.Consume(ctx, request)
	if err != nil || !replay.Replayed || replay.Intent.Status != domain.RunWakeCompleted ||
		replay.Handoff.Result == nil || !replay.Handoff.Result.ModelCalled ||
		replay.Handoff.Operation.ID != replay.Consumption.HandoffOperationID || provider.calls != 1 {
		t.Fatalf("restart replay=%#v calls=%d err=%v", replay, provider.calls, err)
	}
}

func TestForegroundRunWakeConsumerPreservesPreparedHandoffForLaterReplay(t *testing.T) {
	ctx := context.Background()
	state, err := store.Open(filepath.Join(t.TempDir(), "wake-pending-replay.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer state.Close()
	provider := &lifecycleProvider{responses: []string{
		rootActionResponse(domain.RootActionContinue, "wake replayed", "", ""),
	}}
	_, created, err := application.NewRunService(state).Create(ctx,
		application.CreateRunRequest{Goal: "resume pending foreground wake", Profile: "code",
			ModelRoute: provider.Name() + "/model", Budget: domain.Budget{MaxTurns: 4}})
	if err != nil {
		t.Fatal(err)
	}
	run, err := application.NewRunService(state).Start(ctx, created.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := state.EnqueueOperatorSteering(ctx, domain.EnqueueOperatorSteeringRequest{
		RunID: run.ID, SessionID: run.SessionID, Content: "resume this wake",
		OperationKey: "wake-pending-steering-0001", RequestedBy: "operator",
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := application.NewRunWakeControlService(state).Schedule(ctx,
		application.ScheduleRunWakeRequest{
			Version: domain.RunWakeControlProtocolVersion, RunID: run.ID,
			OperationKey: "wake-pending-schedule-0001", RequestedBy: "operator",
			MaxAttempts: 3, BaseBackoffSeconds: 5, MaxBackoffSeconds: 30,
			MaxElapsedSeconds: 120,
		}); err != nil {
		t.Fatal(err)
	}
	request := application.ConsumeRunWakeRequest{
		Version: domain.RunWakeConsumerProtocolVersion, RunID: run.ID,
		OwnerID: "pending_replay_test", MaxSteps: 1,
	}
	pending, err := application.NewForegroundRunWakeConsumer(state,
		pendingWakeHandoff{}).Consume(ctx, request)
	if apperror.CodeOf(err) != apperror.CodeConflict ||
		pending.Consumption.Status != domain.RunWakeConsumptionPrepared ||
		pending.Intent.Status != domain.RunWakeLeased || provider.calls != 0 {
		t.Fatalf("pending consume=%#v calls=%d err=%v", pending, provider.calls, err)
	}
	stored, found, err := state.GetRunWakeConsumption(ctx, pending.Intent.ID,
		pending.Intent.AttemptCount)
	if err != nil || !found || stored.Status != domain.RunWakeConsumptionPrepared {
		t.Fatalf("prepared consumption was not preserved: %#v found=%t err=%v",
			stored, found, err)
	}
	router := llm.NewRouter(llm.ModelRef{Provider: provider.Name(), Model: "model"})
	router.RegisterProvider(provider)
	resumed, err := application.NewForegroundRunWakeConsumer(state,
		application.NewRunExecutionHandoffService(state, router,
			policy.NewDefaultChecker())).Consume(ctx, request)
	if err != nil || resumed.Intent.Status != domain.RunWakeCompleted ||
		resumed.Consumption.Status != domain.RunWakeConsumptionCompleted ||
		provider.calls != 1 {
		t.Fatalf("resumed consume=%#v calls=%d err=%v", resumed, provider.calls, err)
	}
}

func TestRunWakeWorkerSkipsPausedRunAfterRestart(t *testing.T) {
	ctx := t.Context()
	path := filepath.Join(t.TempDir(), "wake-paused-restart.db")
	state, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	provider := &lifecycleProvider{responses: []string{
		rootActionResponse(domain.RootActionContinue, "healthy wake delivered", "", ""),
		rootActionResponse(domain.RootActionContinue, "resumed wake delivered", "", ""),
	}}
	paused := createScheduledWakeTestRun(t, state, provider.Name()+"/model", "paused")
	before, found, err := state.GetLatestRunWakeIntent(ctx, paused.ID)
	if err != nil || !found {
		t.Fatalf("paused wake not scheduled: found=%t err=%v", found, err)
	}
	if _, err := application.NewRunService(state).Pause(ctx, paused.ID); err != nil {
		t.Fatal(err)
	}
	healthy := createScheduledWakeTestRun(t, state, provider.Name()+"/model", "healthy")
	if err := state.Close(); err != nil {
		t.Fatal(err)
	}
	state, err = store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer state.Close()
	router := llm.NewRouter(llm.ModelRef{Provider: provider.Name(), Model: "model"})
	router.RegisterProvider(provider)
	worker, err := application.NewRunWakeWorker(application.NewRunWakeCoordinator(state),
		application.NewForegroundRunWakeConsumer(state,
			application.NewRunExecutionHandoffService(state, router, policy.NewDefaultChecker())),
		application.RunWakeWorkerConfig{OwnerID: "restart-wake-worker"})
	if err != nil {
		t.Fatal(err)
	}
	worked, err := worker.RunOnce(ctx)
	if err != nil || !worked || provider.calls != 1 {
		t.Fatalf("paused Run blocked healthy wake: worked=%t model_calls=%d err=%v", worked, provider.calls, err)
	}
	wake, found, err := state.GetLatestRunWakeIntent(ctx, healthy.ID)
	if err != nil || !found || wake.Status != domain.RunWakeCompleted {
		t.Fatalf("healthy wake=%#v found=%t err=%v", wake, found, err)
	}
	wake, found, err = state.GetLatestRunWakeIntent(ctx, paused.ID)
	if err != nil || !found || wake.ID != before.ID || wake.Status != domain.RunWakeQueued || wake.AttemptCount != 0 {
		t.Fatalf("paused wake changed: %#v found=%t err=%v", wake, found, err)
	}
	if worked, err := worker.RunOnce(ctx); err != nil || worked || provider.calls != 1 {
		t.Fatalf("paused Run was executed: worked=%t model_calls=%d err=%v", worked, provider.calls, err)
	}
	if _, err := application.NewRunService(state).Resume(ctx, paused.ID); err != nil {
		t.Fatal(err)
	}
	if worked, err := worker.RunOnce(ctx); err != nil || !worked || provider.calls != 2 {
		t.Fatalf("resumed wake did not execute: worked=%t model_calls=%d err=%v", worked, provider.calls, err)
	}
	wake, found, err = state.GetLatestRunWakeIntent(ctx, paused.ID)
	if err != nil || !found || wake.ID != before.ID || wake.Status != domain.RunWakeCompleted || wake.AttemptCount != 1 {
		t.Fatalf("resume changed wake identity or attempts: %#v found=%t err=%v", wake, found, err)
	}
}

func TestRunWakeDueRetainsStoredHandoffForPausedRunAfterRestart(t *testing.T) {
	ctx := t.Context()
	path := filepath.Join(t.TempDir(), "wake-settlement-restart.db")
	state, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	provider := &lifecycleProvider{responses: []string{
		rootActionResponse(domain.RootActionContinue, "wake already delivered", "", ""),
	}}
	run := createScheduledWakeTestRun(t, state, provider.Name()+"/model", "settlement")
	router := llm.NewRouter(llm.ModelRef{Provider: provider.Name(), Model: "model"})
	router.RegisterProvider(provider)
	request := application.ConsumeRunWakeRequest{
		Version: domain.RunWakeConsumerProtocolVersion, RunID: run.ID,
		OwnerID: "settlement-worker", MaxSteps: 1,
	}
	if _, err := application.NewForegroundRunWakeConsumer(failingWakeSettlementStore{state},
		application.NewRunExecutionHandoffService(state, router, policy.NewDefaultChecker())).Consume(ctx, request); err == nil || provider.calls != 1 {
		t.Fatalf("completion fault did not preserve executed handoff: model_calls=%d err=%v", provider.calls, err)
	}
	before, found, err := state.GetLatestRunWakeIntent(ctx, run.ID)
	if err != nil || !found || before.Status != domain.RunWakeLeased || before.AttemptCount != 1 {
		t.Fatalf("wake was not left for settlement: %#v found=%t err=%v", before, found, err)
	}
	queue, err := state.GetOperatorSteeringQueueSummary(ctx, run.ID)
	if err != nil || queue.Pending != 0 || queue.Committed != 1 {
		t.Fatalf("handoff input was not committed: %#v err=%v", queue, err)
	}
	if _, err := application.NewRunService(state).Pause(ctx, run.ID); err != nil {
		t.Fatal(err)
	}
	if err := state.Close(); err != nil {
		t.Fatal(err)
	}
	state, err = store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer state.Close()
	// Advance only the due projection's clock beyond lease and total deadline.
	// The real handoff and model response above are already committed in SQLite.
	due, err := state.ListDueRunWakeIntents(ctx, before.DeadlineAt.Add(time.Second), 1, 1)
	if err != nil || len(due) != 1 || due[0].ID != before.ID {
		t.Fatalf("paused Run hid completed handoff from settlement: due=%#v err=%v", due, err)
	}
	result, err := application.NewForegroundRunWakeConsumer(state,
		application.NewRunExecutionHandoffService(state, router, policy.NewDefaultChecker())).Consume(ctx, request)
	if err != nil || result.Intent.ID != before.ID || result.Intent.Status != domain.RunWakeCompleted ||
		result.Intent.AttemptCount != 1 || provider.calls != 1 {
		t.Fatalf("stored handoff did not settle once: result=%#v model_calls=%d err=%v", result, provider.calls, err)
	}
}

func createScheduledWakeTestRun(t *testing.T, state *store.SQLiteStore, modelRoute, name string) domain.Run {
	t.Helper()
	ctx := t.Context()
	_, created, err := application.NewRunService(state).Create(ctx,
		application.CreateRunRequest{Goal: name, Profile: "code",
			ModelRoute: modelRoute, Budget: domain.Budget{MaxTurns: 4}})
	if err != nil {
		t.Fatal(err)
	}
	run, err := application.NewRunService(state).Start(ctx, created.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := state.EnqueueOperatorSteering(ctx, domain.EnqueueOperatorSteeringRequest{
		RunID: run.ID, SessionID: run.SessionID, Content: name,
		OperationKey: "wake-steering-" + name, RequestedBy: "operator",
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := application.NewRunWakeControlService(state).Schedule(ctx,
		application.ScheduleRunWakeRequest{
			Version: domain.RunWakeControlProtocolVersion, RunID: run.ID,
			OperationKey: "wake-schedule-" + name, RequestedBy: "operator",
			MaxAttempts: 3, BaseBackoffSeconds: 5, MaxBackoffSeconds: 30,
			MaxElapsedSeconds: 120,
		}); err != nil {
		t.Fatal(err)
	}
	return run
}
