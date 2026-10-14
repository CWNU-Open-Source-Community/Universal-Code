package application_test

import (
	"context"
	"errors"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"cyberagent-workbench/internal/apperror"
	"cyberagent-workbench/internal/application"
	"cyberagent-workbench/internal/coordinator"
	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/events"
	"cyberagent-workbench/internal/llm"
	"cyberagent-workbench/internal/policy"
	"cyberagent-workbench/internal/store"
	"cyberagent-workbench/internal/waitgraph"
)

func TestSpecialistSchedulerRunsTwoChildrenConcurrentlyWithinOneLease(t *testing.T) {
	provider := newSchedulerBarrierProvider(t, llm.Usage{
		InputTokens: 1, OutputTokens: 1, TotalTokens: 2,
	})
	st, run, children, scheduler := newSpecialistSchedulerFixture(t, provider,
		domain.Budget{MaxTurns: 20}, 4, 64)
	type outcome struct {
		result application.SpecialistScheduleResult
		err    error
	}
	done := make(chan outcome, 1)
	go func() {
		result, err := scheduler.Execute(context.Background(), application.SpecialistScheduleRequest{
			RunID: run.ID, AgentIDs: childIDs(children), MaxRounds: 1,
		})
		done <- outcome{result: result, err: err}
	}()
	waitForSchedulerStarts(t, provider.started, 2)
	close(provider.release)
	got := waitForScheduleOutcome(t, done)
	if got.err != nil || got.result.StopReason != application.SpecialistScheduleRoundLimit ||
		got.result.ScheduleID == "" || got.result.RecoveredSchedule ||
		got.result.RoundsCompleted != 1 || got.result.TurnsStarted != 2 ||
		len(got.result.Turns) != 2 || provider.maximumConcurrency() != 2 {
		t.Fatalf("bounded concurrent schedule failed: result=%#v max=%d err=%v",
			got.result, provider.maximumConcurrency(), got.err)
	}
	schedule, err := st.GetSpecialistSchedule(context.Background(), got.result.ScheduleID)
	if err != nil || schedule.Status != domain.SpecialistScheduleCompleted ||
		schedule.StopReason != string(application.SpecialistScheduleRoundLimit) ||
		schedule.RoundsCompleted != 1 || schedule.TurnsStarted != 2 ||
		len(schedule.AgentIDs) != 2 {
		t.Fatalf("durable schedule summary is invalid: schedule=%#v err=%v", schedule, err)
	}
	eventLog, err := st.ListRunEvents(context.Background(), run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if countEventType(eventLog, events.AgentScheduleStartedEvent) != 1 ||
		countEventType(eventLog, events.AgentScheduleStoppedEvent) != 1 {
		t.Fatalf("schedule lifecycle events are incomplete: %#v", eventLog)
	}
	for _, event := range eventLog {
		if (event.Type == events.AgentScheduleStartedEvent ||
			event.Type == events.AgentScheduleStoppedEvent) &&
			(strings.Contains(event.PayloadJSON, `"lease_id"`) ||
				strings.Contains(event.PayloadJSON, `"lease_generation"`)) {
			t.Fatalf("schedule event exposed internal fencing identity: %s", event.PayloadJSON)
		}
	}
	for index, turn := range got.result.Turns {
		if turn.AgentID != got.result.AgentIDs[index] ||
			turn.AttemptStatus != domain.AgentAttemptContinued {
			t.Fatalf("schedule results are not deterministic: %#v", got.result.Turns)
		}
	}
	lease, found, err := st.GetRunExecutionLease(context.Background(), run.ID)
	if err != nil || !found || lease.Status != domain.RunExecutionLeaseReleased {
		t.Fatalf("schedule lease was not released: lease=%#v found=%t err=%v",
			lease, found, err)
	}
	leaseIDs := map[string]struct{}{}
	for _, child := range children {
		attempts, err := st.ListAgentAttempts(context.Background(), child.ID)
		if err != nil || len(attempts) != 1 || attempts[0].LeaseGeneration != 1 {
			t.Fatalf("child did not use the shared first-generation lease: child=%s attempts=%#v err=%v",
				child.ID, attempts, err)
		}
		leaseIDs[attempts[0].LeaseID] = struct{}{}
	}
	if len(leaseIDs) != 1 {
		t.Fatalf("parallel child turns did not share one Run lease: %#v", leaseIDs)
	}
}

func TestSpecialistSchedulerStopsWhenAllChildrenFinish(t *testing.T) {
	report := domain.CompletionReport{
		Version: domain.CompletionReportVersion, Outcome: domain.CompletionSucceeded,
		Summary: "bounded Specialist work completed", WorkItemIDs: []string{}, NoteIDs: []string{},
	}
	provider := &specialistTestProvider{responses: []llm.ChatResponse{
		{
			Text: specialistResponse(t, domain.SpecialistAction{
				Version: domain.SpecialistLifecycleVersion, Kind: domain.SpecialistActionFinish,
				Message: "first child complete", Report: &report,
			}), Usage: llm.Usage{InputTokens: 1, OutputTokens: 2, TotalTokens: 3},
		},
		{
			Text: specialistResponse(t, domain.SpecialistAction{
				Version: domain.SpecialistLifecycleVersion, Kind: domain.SpecialistActionFinish,
				Message: "second child complete", Report: &report,
			}), Usage: llm.Usage{InputTokens: 1, OutputTokens: 2, TotalTokens: 3},
		},
	}}
	st, run, children, scheduler := newSpecialistSchedulerFixture(t, provider,
		domain.Budget{MaxTurns: 20}, 4, 64)
	result, err := scheduler.Execute(context.Background(), application.SpecialistScheduleRequest{
		RunID: run.ID, AgentIDs: childIDs(children), MaxRounds: 4,
	})
	if err != nil || result.StopReason != application.SpecialistScheduleAllTerminal ||
		result.RoundsCompleted != 1 || result.TurnsStarted != 2 {
		t.Fatalf("terminal schedule did not stop: result=%#v err=%v", result, err)
	}
	for _, child := range children {
		updated, err := st.GetAgentNode(context.Background(), child.ID)
		if err != nil || updated.Status != domain.AgentCompleted || updated.FinishedAt == nil {
			t.Fatalf("finished child is not terminal: child=%#v err=%v", updated, err)
		}
	}
}

func TestSpecialistSchedulerWaitsForDependencyBeforeCallingModel(t *testing.T) {
	report := domain.CompletionReport{Version: domain.CompletionReportVersion,
		Outcome: domain.CompletionSucceeded, Summary: "bounded work completed",
		WorkItemIDs: []string{}, NoteIDs: []string{}}
	response := llm.ChatResponse{Text: specialistResponse(t, domain.SpecialistAction{
		Version: domain.SpecialistLifecycleVersion, Kind: domain.SpecialistActionFinish,
		Message: "child finished", Report: &report,
	}), Usage: llm.Usage{InputTokens: 1, OutputTokens: 1, TotalTokens: 2}}
	provider := &specialistTestProvider{responses: []llm.ChatResponse{response, response}}
	st, run, children, scheduler := newSpecialistSchedulerFixture(t, provider,
		domain.Budget{MaxTurns: 20}, 4, 64)
	ctx := t.Context()
	now := time.Now().UTC()
	if _, _, err := st.RecordDependencyWait(ctx, domain.DependencyEdge{
		ID: "scheduler-dependency", RunID: run.ID, SourceKind: waitgraph.KindAgent, SourceID: children[1].ID,
		TargetKind: waitgraph.KindAgent, TargetID: children[0].ID, Reason: "consume the first result",
		State: domain.AgentDependencyWait, FailurePolicy: domain.DependencyPolicyFail, Generation: 1,
		CreatedAt: now, UpdatedAt: now, Deadline: now.Add(time.Minute),
	}, "scheduler-dependency-operation"); err != nil {
		t.Fatal(err)
	}
	result, err := scheduler.Execute(ctx, application.SpecialistScheduleRequest{
		RunID: run.ID, AgentIDs: childIDs(children), MaxRounds: 4,
	})
	if err != nil || result.StopReason != application.SpecialistScheduleAllTerminal ||
		result.RoundsCompleted != 2 || result.TurnsStarted != 2 || len(result.Turns) != 2 {
		t.Fatalf("dependency schedule failed: %#v err=%v", result, err)
	}
	if result.Turns[0].AgentID != children[0].ID || result.Turns[1].AgentID != children[1].ID {
		t.Fatalf("dependent child executed before its target: %#v", result.Turns)
	}
	target, err := st.GetAgentNode(ctx, children[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	attempts, err := st.ListAgentAttempts(ctx, children[1].ID)
	if err != nil || len(attempts) != 1 || target.FinishedAt == nil ||
		attempts[0].StartedAt.Before(*target.FinishedAt) {
		t.Fatalf("dependent child started before completion: target=%#v attempts=%#v err=%v", target, attempts, err)
	}
}

func TestSpecialistSchedulerParentCancellationFansOutToBothChildren(t *testing.T) {
	provider := newSchedulerBarrierProvider(t, llm.Usage{})
	st, run, children, scheduler := newSpecialistSchedulerFixture(t, provider,
		domain.Budget{MaxTurns: 20}, 4, 64)
	ctx, cancel := context.WithCancel(context.Background())
	type outcome struct {
		result application.SpecialistScheduleResult
		err    error
	}
	done := make(chan outcome, 1)
	go func() {
		result, err := scheduler.Execute(ctx, application.SpecialistScheduleRequest{
			RunID: run.ID, AgentIDs: childIDs(children), MaxRounds: 2,
		})
		done <- outcome{result: result, err: err}
	}()
	waitForSchedulerStarts(t, provider.started, 2)
	cancel()
	got := waitForScheduleOutcome(t, done)
	if apperror.CodeOf(got.err) != apperror.CodeCancelled ||
		got.result.StopReason != application.SpecialistScheduleCancelled ||
		got.result.TurnsStarted != 2 || len(got.result.Failures) != 2 {
		t.Fatalf("parent cancellation did not fan out: result=%#v code=%s err=%v",
			got.result, apperror.CodeOf(got.err), got.err)
	}
	assertCrashedScheduleAttempts(t, st, children, "cancelled")
}

func TestSpecialistSchedulerChildFailureCancelsSibling(t *testing.T) {
	provider := newSchedulerBarrierProvider(t, llm.Usage{})
	st, run, children, scheduler := newSpecialistSchedulerFixture(t, provider,
		domain.Budget{MaxTurns: 20}, 4, 64)
	sortedIDs := childIDs(children)
	provider.failAgent = sortedIDs[0]
	provider.blockSuccessful = true
	type outcome struct {
		result application.SpecialistScheduleResult
		err    error
	}
	done := make(chan outcome, 1)
	go func() {
		result, err := scheduler.Execute(context.Background(), application.SpecialistScheduleRequest{
			RunID: run.ID, AgentIDs: sortedIDs, MaxRounds: 2,
		})
		done <- outcome{result: result, err: err}
	}()
	waitForSchedulerStarts(t, provider.started, 2)
	close(provider.release)
	got := waitForScheduleOutcome(t, done)
	if got.err == nil || got.result.StopReason != application.SpecialistScheduleChildError ||
		got.result.TurnsStarted != 2 || len(got.result.Failures) != 2 {
		t.Fatalf("child failure did not cancel its sibling: result=%#v err=%v",
			got.result, got.err)
	}
	failureCodes := map[string]string{}
	for _, child := range children {
		attempts, err := st.ListAgentAttempts(context.Background(), child.ID)
		if err != nil || len(attempts) != 1 || attempts[0].Status != domain.AgentAttemptCrashed {
			t.Fatalf("child attempt was not crashed: child=%s attempts=%#v err=%v",
				child.ID, attempts, err)
		}
		failureCodes[child.ID] = attempts[0].Failure.Code
	}
	if failureCodes[sortedIDs[0]] == "cancelled" || failureCodes[sortedIDs[1]] != "cancelled" {
		t.Fatalf("failure initiator and cancelled sibling were not distinguished: %#v", failureCodes)
	}
}

func TestSpecialistSchedulerContainsChildPanicAndCancelsSibling(t *testing.T) {
	provider := newSchedulerBarrierProvider(t, llm.Usage{})
	st, run, children, scheduler := newSpecialistSchedulerFixture(t, provider,
		domain.Budget{MaxTurns: 20}, 4, 64)
	sortedIDs := childIDs(children)
	provider.panicAgent = sortedIDs[0]
	provider.blockSuccessful = true
	type outcome struct {
		result application.SpecialistScheduleResult
		err    error
	}
	done := make(chan outcome, 1)
	go func() {
		result, err := scheduler.Execute(context.Background(), application.SpecialistScheduleRequest{
			RunID: run.ID, AgentIDs: sortedIDs, MaxRounds: 2,
		})
		done <- outcome{result: result, err: err}
	}()
	waitForSchedulerStarts(t, provider.started, 2)
	close(provider.release)
	got := waitForScheduleOutcome(t, done)
	if apperror.CodeOf(got.err) != apperror.CodeInternal ||
		got.result.StopReason != application.SpecialistScheduleChildError ||
		got.result.TurnsStarted != 2 || len(got.result.Failures) != 2 {
		t.Fatalf("child panic escaped containment: result=%#v code=%s err=%v",
			got.result, apperror.CodeOf(got.err), got.err)
	}
	failureCodes := map[string]string{}
	for _, child := range children {
		attempts, err := st.ListAgentAttempts(context.Background(), child.ID)
		if err != nil || len(attempts) != 1 || attempts[0].Status != domain.AgentAttemptCrashed ||
			strings.Contains(attempts[0].Failure.Reason, "must-not-persist-panic-payload") {
			t.Fatalf("panic attempt was not safely closed: child=%s attempts=%#v err=%v",
				child.ID, attempts, err)
		}
		failureCodes[child.ID] = attempts[0].Failure.Code
	}
	if failureCodes[sortedIDs[0]] != "specialist_failure" ||
		failureCodes[sortedIDs[1]] != "cancelled" {
		t.Fatalf("panic and sibling cancellation were not distinguished: %#v", failureCodes)
	}
}

func TestSpecialistSchedulerContainsCoordinatorPanicAndFailsSummary(t *testing.T) {
	provider := newSchedulerBarrierProvider(t, llm.Usage{})
	st, run, children, _ := newSpecialistSchedulerFixture(t, provider,
		domain.Budget{MaxTurns: 20}, 4, 64)
	router := llm.NewRouter(llm.ModelRef{Provider: provider.Name(), Model: "model"})
	router.RegisterProvider(provider)
	runner := application.NewSubagentRunner(&panicAgentListStore{SQLiteStore: st},
		router, policy.NewDefaultChecker())
	scheduler := application.NewSpecialistScheduler(runner)
	result, err := scheduler.Execute(context.Background(), application.SpecialistScheduleRequest{
		RunID: run.ID, AgentIDs: childIDs(children), MaxRounds: 1,
	})
	if apperror.CodeOf(err) != apperror.CodeInternal ||
		result.StopReason != application.SpecialistScheduleChildError || result.ScheduleID == "" {
		t.Fatalf("scheduler panic escaped containment: result=%#v code=%s err=%v",
			result, apperror.CodeOf(err), err)
	}
	schedule, loadErr := st.GetSpecialistSchedule(context.Background(), result.ScheduleID)
	if loadErr != nil || schedule.Status != domain.SpecialistScheduleFailed ||
		schedule.StopReason != string(application.SpecialistScheduleChildError) ||
		schedule.ErrorCode != string(apperror.CodeInternal) {
		t.Fatalf("scheduler panic summary is invalid: schedule=%#v err=%v", schedule, loadErr)
	}
	eventLog, loadErr := st.ListRunEvents(context.Background(), run.ID)
	if loadErr != nil {
		t.Fatal(loadErr)
	}
	for _, event := range eventLog {
		if strings.Contains(event.PayloadJSON, "must-not-persist-scheduler-panic") {
			t.Fatalf("scheduler panic payload reached events: %s", event.PayloadJSON)
		}
	}
}

func TestSpecialistSchedulerRechecksAggregateTokenBudget(t *testing.T) {
	provider := newAggregateBudgetProvider(t)
	st, run, children, scheduler := newSpecialistSchedulerFixture(t, provider,
		domain.Budget{MaxTurns: 20, MaxTokens: 10}, 4, 4)
	router := llm.NewRouter(llm.ModelRef{Provider: provider.Name(), Model: "model"})
	router.RegisterProvider(provider)
	rootTurn, err := application.NewAgentRunner(st, router,
		policy.NewDefaultChecker()).Step(context.Background(), run.ID)
	if err != nil || rootTurn.Checkpoint.TotalTokens != 2 {
		t.Fatalf("root budget seed failed: turn=%#v err=%v", rootTurn, err)
	}
	result, err := scheduler.Execute(context.Background(), application.SpecialistScheduleRequest{
		RunID: run.ID, AgentIDs: childIDs(children), MaxRounds: 4,
	})
	if err != nil || result.StopReason != application.SpecialistScheduleTokenBudget ||
		result.RoundsCompleted != 1 || result.TurnsStarted != 2 ||
		result.UsageBefore.RootTokens != 2 || result.UsageBefore.TotalTokens != 2 ||
		result.UsageAfter.SpecialistTokens != 8 ||
		result.UsageAfter.TotalTokens != 10 {
		t.Fatalf("aggregate token budget was not rechecked: result=%#v err=%v", result, err)
	}
	requests := provider.childRequests()
	if len(requests) != 2 {
		t.Fatalf("expected two child model calls, got %d", len(requests))
	}
	for _, request := range requests {
		if request.MaxTokens != 4 {
			t.Fatalf("remaining token budget was not split fairly: request=%#v", request)
		}
	}
}

func TestSpecialistSchedulerSplitsAndSumsAggregateExecutionBudget(t *testing.T) {
	provider := newDeadlineBudgetProvider(t, 35*time.Millisecond)
	_, run, children, scheduler := newSpecialistSchedulerFixture(t, provider,
		domain.Budget{MaxTurns: 20, TimeoutSeconds: 1}, 4, 64)
	result, err := scheduler.Execute(context.Background(), application.SpecialistScheduleRequest{
		RunID: run.ID, AgentIDs: childIDs(children), MaxRounds: 1,
	})
	if err != nil || result.StopReason != application.SpecialistScheduleRoundLimit ||
		result.RoundsCompleted != 1 || result.TurnsStarted != 2 ||
		result.UsageAfter.SpecialistExecutionMillis < 50 ||
		result.UsageAfter.TotalExecutionMillis != result.UsageAfter.SpecialistExecutionMillis {
		t.Fatalf("aggregate execution time was not summed: result=%#v err=%v", result, err)
	}
	deadlines := provider.callDeadlines()
	if len(deadlines) != 2 {
		t.Fatalf("expected two child deadlines, got %#v", deadlines)
	}
	for _, remaining := range deadlines {
		if remaining < 300*time.Millisecond || remaining > 550*time.Millisecond {
			t.Fatalf("execution budget was not split between children: %s", remaining)
		}
	}
}

func TestSpecialistSchedulerStopsBeforeCallsWhenAggregateExecutionBudgetIsSpent(t *testing.T) {
	provider := newSchedulerBarrierProvider(t, llm.Usage{})
	st, run, children, scheduler := newSpecialistSchedulerFixture(t, provider,
		domain.Budget{MaxTurns: 20, TimeoutSeconds: 1}, 4, 64)
	ctx := context.Background()
	lease := acquireTestRunExecutionLease(t, ctx, st, run.ID)
	turn, err := st.BeginSupervisorTurn(ctx, lease, "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.FailSupervisorTurn(ctx, turn.Checkpoint,
		"deterministic spent execution budget", time.Second); err != nil {
		t.Fatal(err)
	}
	releaseTestRunExecutionLease(t, ctx, st, run.ID)
	if paused, err := st.GetRun(ctx, run.ID); err != nil || paused.Status != domain.RunPaused {
		t.Fatalf("spent failed turn must pause the Run: %#v %v", paused, err)
	}
	// Explicitly resume so the scheduler reaches its aggregate budget guard;
	// resuming must not reset the already charged execution time.
	if _, err := application.NewRunService(st).Resume(ctx, run.ID); err != nil {
		t.Fatal(err)
	}

	result, err := scheduler.Execute(ctx, application.SpecialistScheduleRequest{
		RunID: run.ID, AgentIDs: childIDs(children), MaxRounds: 2,
	})
	if err != nil || result.StopReason != application.SpecialistScheduleExecutionBudget ||
		result.RoundsCompleted != 0 || result.TurnsStarted != 0 ||
		result.UsageBefore.RootExecutionMillis != 1000 ||
		result.UsageAfter.TotalExecutionMillis != 1000 {
		t.Fatalf("spent aggregate execution budget reached a child: result=%#v err=%v",
			result, err)
	}
	select {
	case agentID := <-provider.started:
		t.Fatalf("Provider was called after execution budget exhaustion: %s", agentID)
	default:
	}
}

func TestSpecialistSchedulerRecoversStaleAttemptBeforeConcurrentRound(t *testing.T) {
	provider := &specialistTestProvider{responses: []llm.ChatResponse{
		{
			Text: specialistResponse(t, domain.SpecialistAction{
				Version: domain.SpecialistLifecycleVersion, Kind: domain.SpecialistActionContinue,
				Message: "resumed first child",
			}), Usage: llm.Usage{InputTokens: 1, OutputTokens: 1, TotalTokens: 2},
		},
		{
			Text: specialistResponse(t, domain.SpecialistAction{
				Version: domain.SpecialistLifecycleVersion, Kind: domain.SpecialistActionContinue,
				Message: "resumed second child",
			}), Usage: llm.Usage{InputTokens: 1, OutputTokens: 1, TotalTokens: 2},
		},
	}}
	st, run, children, scheduler := newSpecialistSchedulerFixture(t, provider,
		domain.Budget{MaxTurns: 20}, 4, 64)
	ctx := context.Background()
	oldLease, err := st.AcquireRunExecutionLease(ctx, domain.AcquireRunExecutionLeaseRequest{
		RunID: run.ID, OwnerID: "stale-specialist-scheduler", TTL: 500 * time.Millisecond,
	})
	if err != nil {
		t.Fatal(err)
	}
	usage, err := st.GetRunAgentUsage(ctx, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	oldScheduleID := "schedule-stale-worker-0001"
	if _, err := st.StartSpecialistSchedule(ctx, domain.SpecialistScheduleStart{
		ID: oldScheduleID, RunID: run.ID, AgentIDs: childIDs(children), MaxRounds: 2,
		Lease: oldLease.Lease, UsageBefore: usage, StartedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatal(err)
	}
	root, found, err := st.GetRootAgent(ctx, run.ID)
	if err != nil || !found {
		t.Fatalf("root Agent was not found: found=%t err=%v", found, err)
	}
	staleID := "attempt-stale-scheduler-0001"
	if _, _, err := st.BeginSpecialistAttempt(ctx, domain.AgentAttemptStart{
		AttemptID: staleID, RunID: run.ID, AgentID: children[0].ID,
		ParentAgentID: root.ID, Lease: oldLease.Lease, StartedAt: time.Now().UTC(),
	}, "stale-scheduler-attempt-start-0001"); err != nil {
		t.Fatal(err)
	}
	time.Sleep(time.Until(oldLease.Lease.ExpiresAt) + 30*time.Millisecond)

	result, err := scheduler.Execute(ctx, application.SpecialistScheduleRequest{
		RunID: run.ID, AgentIDs: childIDs(children), MaxRounds: 1,
	})
	if err != nil || result.RecoveredAttempts != 1 || !result.RecoveredSchedule ||
		result.RoundsCompleted != 1 ||
		result.TurnsStarted != 2 || result.StopReason != application.SpecialistScheduleRoundLimit {
		t.Fatalf("stale attempt recovery did not converge: result=%#v err=%v", result, err)
	}
	oldSchedule, err := st.GetSpecialistSchedule(ctx, oldScheduleID)
	if err != nil || oldSchedule.Status != domain.SpecialistScheduleAbandoned ||
		oldSchedule.StopReason != "worker_lost" || oldSchedule.TurnsStarted != 1 ||
		oldSchedule.RecoveredAttempts != 1 || oldSchedule.FinishedAt == nil {
		t.Fatalf("stale schedule was not durably abandoned: schedule=%#v err=%v",
			oldSchedule, err)
	}
	stale, found, err := st.GetAgentAttempt(ctx, staleID)
	if err != nil || !found || stale.Status != domain.AgentAttemptCrashed ||
		stale.Failure.Code != "worker_lost" {
		t.Fatalf("stale attempt was not fenced: attempt=%#v found=%t err=%v",
			stale, found, err)
	}
	lease, found, err := st.GetRunExecutionLease(ctx, run.ID)
	if err != nil || !found || lease.Generation != 2 ||
		lease.Status != domain.RunExecutionLeaseReleased {
		t.Fatalf("takeover lease generation is invalid: lease=%#v found=%t err=%v",
			lease, found, err)
	}
}

func TestSpecialistSchedulerValidatesBoundedTargets(t *testing.T) {
	tests := []application.SpecialistScheduleRequest{
		{RunID: "run-1", AgentIDs: []string{"a", "a"}, MaxRounds: 1},
		{RunID: "run-1", AgentIDs: []string{"a", "b", "c"}, MaxRounds: 1},
		{RunID: "run-1", AgentIDs: []string{"a"}, MaxRounds: 0},
		{RunID: "run-1", AgentIDs: []string{"a"}, MaxRounds: application.MaxSpecialistScheduleRounds + 1},
	}
	for _, request := range tests {
		_, err := application.NewSpecialistScheduler(nil).Execute(context.Background(), request)
		if apperror.CodeOf(err) != apperror.CodeInvalidArgument {
			t.Fatalf("invalid schedule was accepted: request=%#v code=%s err=%v",
				request, apperror.CodeOf(err), err)
		}
	}
}

func newSpecialistSchedulerFixture(t testing.TB, provider llm.Provider, budget domain.Budget,
	turnLimit int64, tokenLimit int64,
) (*store.SQLiteStore, domain.Run, []domain.AgentNode, *application.SpecialistScheduler) {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "cyberagent.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	ctx := context.Background()
	service := application.NewRunService(st)
	_, run, err := service.Create(ctx, application.CreateRunRequest{
		Goal: "coordinate two bounded Specialist reviews", Profile: "code",
		ModelRoute: provider.Name() + "/model", Budget: budget,
	})
	if err != nil {
		t.Fatal(err)
	}
	run, err = service.Start(ctx, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	root, found, err := st.GetRootAgent(ctx, run.ID)
	if err != nil || !found {
		t.Fatalf("root Agent was not created: found=%t err=%v", found, err)
	}
	coord, err := coordinator.NewWithSpecialistAdmission(st,
		coordinator.SpecialistAdmissionPolicy{
			MaxChildren: 2, MaxTurnsPerChild: turnLimit, MaxTokensPerChild: tokenLimit,
		})
	if err != nil {
		t.Fatal(err)
	}
	children := make([]domain.AgentNode, 0, 2)
	for index := 0; index < 2; index++ {
		admitted, err := coord.AdmitSpecialist(ctx, coordinator.AdmitSpecialistRequest{
			RunID: run.ID, ParentAgentID: root.ID,
			Title:  "internal bounded Specialist " + string(rune('A'+index)),
			Skills: []string{"model.chat"}, TurnLimit: turnLimit, TokenLimit: tokenLimit,
			IdempotencyKey: "specialist-scheduler-admit-000" + string(rune('1'+index)),
		})
		if err != nil {
			t.Fatal(err)
		}
		children = append(children, admitted.Agent)
	}
	router := llm.NewRouter(llm.ModelRef{Provider: provider.Name(), Model: "model"})
	router.RegisterProvider(provider)
	runner := application.NewSubagentRunner(st, router, policy.NewDefaultChecker())
	return st, run, children, application.NewSpecialistScheduler(runner)
}

func childIDs(children []domain.AgentNode) []string {
	ids := make([]string, 0, len(children))
	for _, child := range children {
		ids = append(ids, child.ID)
	}
	sort.Strings(ids)
	return ids
}

type schedulerBarrierProvider struct {
	mu              sync.Mutex
	response        llm.ChatResponse
	started         chan string
	release         chan struct{}
	active          int
	maxActive       int
	failAgent       string
	panicAgent      string
	blockSuccessful bool
}

type panicAgentListStore struct {
	*store.SQLiteStore
}

func (*panicAgentListStore) ListAgentNodes(context.Context,
	string,
) ([]domain.AgentNode, error) {
	panic("must-not-persist-scheduler-panic")
}

type aggregateBudgetProvider struct {
	mu                 sync.Mutex
	rootResponse       llm.ChatResponse
	specialistResponse llm.ChatResponse
	requests           []llm.ChatRequest
}

type deadlineBudgetProvider struct {
	mu        sync.Mutex
	response  llm.ChatResponse
	delay     time.Duration
	deadlines []time.Duration
}

func newDeadlineBudgetProvider(t testing.TB, delay time.Duration) *deadlineBudgetProvider {
	t.Helper()
	return &deadlineBudgetProvider{
		delay: delay,
		response: llm.ChatResponse{
			Text: specialistResponse(t, domain.SpecialistAction{
				Version: domain.SpecialistLifecycleVersion, Kind: domain.SpecialistActionContinue,
				Message: "continue within the execution slice",
			}), Usage: llm.Usage{InputTokens: 1, OutputTokens: 1, TotalTokens: 2},
		},
	}
}

func (*deadlineBudgetProvider) Name() string { return "deadline-budget-test" }

func (*deadlineBudgetProvider) ListModels(context.Context) ([]llm.ModelInfo, error) {
	return []llm.ModelInfo{{ID: "model", Provider: "deadline-budget-test"}}, nil
}

func (p *deadlineBudgetProvider) Chat(ctx context.Context,
	_ llm.ChatRequest,
) (*llm.ChatResponse, error) {
	deadline, ok := ctx.Deadline()
	if !ok {
		return nil, errors.New("scheduled model call omitted its deadline")
	}
	p.mu.Lock()
	p.deadlines = append(p.deadlines, time.Until(deadline))
	p.mu.Unlock()
	timer := time.NewTimer(p.delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-timer.C:
	}
	response := p.response
	response.Provider = p.Name()
	response.Model = "model"
	return &response, nil
}

func (p *deadlineBudgetProvider) StreamChat(ctx context.Context,
	request llm.ChatRequest,
) (<-chan llm.ChatChunk, error) {
	response, err := p.Chat(ctx, request)
	if err != nil {
		return nil, err
	}
	chunks := make(chan llm.ChatChunk, 2)
	chunks <- llm.ChatChunk{Text: response.Text}
	chunks <- llm.FinalChatChunk(response)
	close(chunks)
	return chunks, nil
}

func (*deadlineBudgetProvider) SupportsTools(string) bool    { return false }
func (*deadlineBudgetProvider) SupportsVision(string) bool   { return false }
func (*deadlineBudgetProvider) SupportsJSONMode(string) bool { return true }

func (p *deadlineBudgetProvider) callDeadlines() []time.Duration {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]time.Duration(nil), p.deadlines...)
}

func newAggregateBudgetProvider(t testing.TB) *aggregateBudgetProvider {
	t.Helper()
	return &aggregateBudgetProvider{
		rootResponse: llm.ChatResponse{
			Text: rootActionResponse(domain.RootActionContinue,
				"root reserved its coordination budget", "", ""),
			Usage: llm.Usage{InputTokens: 1, OutputTokens: 1, TotalTokens: 2},
		},
		specialistResponse: llm.ChatResponse{
			Text: specialistResponse(t, domain.SpecialistAction{
				Version: domain.SpecialistLifecycleVersion, Kind: domain.SpecialistActionContinue,
				Message: "bounded child budget consumed",
			}), Usage: llm.Usage{InputTokens: 2, OutputTokens: 2, TotalTokens: 4},
		},
	}
}

func (*aggregateBudgetProvider) Name() string { return "aggregate-budget-test" }

func (*aggregateBudgetProvider) ListModels(context.Context) ([]llm.ModelInfo, error) {
	return []llm.ModelInfo{{ID: "model", Provider: "aggregate-budget-test"}}, nil
}

func (p *aggregateBudgetProvider) Chat(ctx context.Context,
	request llm.ChatRequest,
) (*llm.ChatResponse, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	p.mu.Lock()
	p.requests = append(p.requests, request)
	p.mu.Unlock()
	response := p.specialistResponse
	if request.Metadata["response_schema"] == domain.RootLifecycleVersion {
		response = p.rootResponse
	}
	response.Provider = p.Name()
	response.Model = "model"
	return &response, nil
}

func (p *aggregateBudgetProvider) StreamChat(ctx context.Context,
	request llm.ChatRequest,
) (<-chan llm.ChatChunk, error) {
	response, err := p.Chat(ctx, request)
	if err != nil {
		return nil, err
	}
	chunks := make(chan llm.ChatChunk, 2)
	chunks <- llm.ChatChunk{Text: response.Text}
	chunks <- llm.FinalChatChunk(response)
	close(chunks)
	return chunks, nil
}

func (*aggregateBudgetProvider) SupportsTools(string) bool    { return false }
func (*aggregateBudgetProvider) SupportsVision(string) bool   { return false }
func (*aggregateBudgetProvider) SupportsJSONMode(string) bool { return true }

func (p *aggregateBudgetProvider) childRequests() []llm.ChatRequest {
	p.mu.Lock()
	defer p.mu.Unlock()
	requests := make([]llm.ChatRequest, 0, len(p.requests))
	for _, request := range p.requests {
		if request.Metadata["response_schema"] == domain.SpecialistLifecycleVersion {
			requests = append(requests, request)
		}
	}
	return requests
}

func newSchedulerBarrierProvider(t testing.TB, usage llm.Usage) *schedulerBarrierProvider {
	t.Helper()
	return &schedulerBarrierProvider{
		response: llm.ChatResponse{
			Text: specialistResponse(t, domain.SpecialistAction{
				Version: domain.SpecialistLifecycleVersion, Kind: domain.SpecialistActionContinue,
				Message: "continue bounded parallel work",
			}), Usage: usage,
		},
		started: make(chan string, 4), release: make(chan struct{}),
	}
}

func (*schedulerBarrierProvider) Name() string { return "scheduler-test" }

func (*schedulerBarrierProvider) ListModels(context.Context) ([]llm.ModelInfo, error) {
	return []llm.ModelInfo{{ID: "model", Provider: "scheduler-test"}}, nil
}

func (p *schedulerBarrierProvider) Chat(ctx context.Context,
	request llm.ChatRequest,
) (*llm.ChatResponse, error) {
	agentID := request.Metadata["agent_id"]
	p.mu.Lock()
	p.active++
	if p.active > p.maxActive {
		p.maxActive = p.active
	}
	p.mu.Unlock()
	defer func() {
		p.mu.Lock()
		p.active--
		p.mu.Unlock()
	}()
	p.started <- agentID
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-p.release:
	}
	if agentID == p.failAgent {
		return nil, llm.NewProviderError(llm.OutcomePermanent, p.Name(),
			"deterministic child failure", errors.New("test failure"))
	}
	if agentID == p.panicAgent {
		panic("must-not-persist-panic-payload")
	}
	if p.blockSuccessful {
		<-ctx.Done()
		return nil, ctx.Err()
	}
	response := p.response
	response.Provider = p.Name()
	response.Model = "model"
	return &response, nil
}

func (p *schedulerBarrierProvider) StreamChat(ctx context.Context,
	request llm.ChatRequest,
) (<-chan llm.ChatChunk, error) {
	response, err := p.Chat(ctx, request)
	if err != nil {
		return nil, err
	}
	chunks := make(chan llm.ChatChunk, 2)
	chunks <- llm.ChatChunk{Text: response.Text}
	chunks <- llm.FinalChatChunk(response)
	close(chunks)
	return chunks, nil
}

func (*schedulerBarrierProvider) SupportsTools(string) bool    { return false }
func (*schedulerBarrierProvider) SupportsVision(string) bool   { return false }
func (*schedulerBarrierProvider) SupportsJSONMode(string) bool { return true }

func (p *schedulerBarrierProvider) maximumConcurrency() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.maxActive
}

func waitForSchedulerStarts(t testing.TB, started <-chan string, count int) {
	t.Helper()
	seen := make(map[string]struct{}, count)
	deadline := time.NewTimer(5 * time.Second)
	defer deadline.Stop()
	for len(seen) < count {
		select {
		case agentID := <-started:
			seen[agentID] = struct{}{}
		case <-deadline.C:
			t.Fatalf("only %d/%d Specialist model calls started", len(seen), count)
		}
	}
}

func waitForScheduleOutcome[T any](t testing.TB, done <-chan T) T {
	t.Helper()
	select {
	case result := <-done:
		return result
	case <-time.After(8 * time.Second):
		t.Fatal("Specialist schedule did not return")
		var zero T
		return zero
	}
}

func assertCrashedScheduleAttempts(t testing.TB, st *store.SQLiteStore,
	children []domain.AgentNode, failureCode string,
) {
	t.Helper()
	for _, child := range children {
		attempts, err := st.ListAgentAttempts(context.Background(), child.ID)
		if err != nil || len(attempts) != 1 || attempts[0].Status != domain.AgentAttemptCrashed ||
			attempts[0].Failure.Code != failureCode {
			t.Fatalf("child cancellation was not durable: child=%s attempts=%#v err=%v",
				child.ID, attempts, err)
		}
		updated, err := st.GetAgentNode(context.Background(), child.ID)
		if err != nil || updated.Status != domain.AgentReady || updated.ActiveAttemptID != "" {
			t.Fatalf("cancelled child was left active: child=%#v err=%v", updated, err)
		}
	}
}
