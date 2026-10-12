package application_test

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"

	"cyberagent-workbench/internal/apperror"
	"cyberagent-workbench/internal/application"
	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/llm"
	"cyberagent-workbench/internal/policy"
	"cyberagent-workbench/internal/store"
)

func TestSpecialistContinuationCompletesAcrossTurnsAndSummarizesInMainSessionOnce(t *testing.T) {
	usage := llm.Usage{InputTokens: 2, OutputTokens: 2, TotalTokens: 4}
	report := domain.CompletionReport{Version: domain.CompletionReportVersion, Outcome: domain.CompletionSucceeded,
		Summary: "Child verified the assigned evidence", WorkItemIDs: []string{}, NoteIDs: []string{}}
	provider := &specialistTestProvider{responses: []llm.ChatResponse{
		{Text: specialistResponse(t, domain.SpecialistAction{Version: domain.SpecialistLifecycleVersion, Kind: domain.SpecialistActionContinue, Message: "First pass complete; continue the assigned review"}), Usage: usage},
		{Text: specialistResponse(t, domain.SpecialistAction{Version: domain.SpecialistLifecycleVersion, Kind: domain.SpecialistActionFinish, Message: "Review complete", Report: &report}), Usage: usage},
		{Text: rootActionResponse(domain.RootActionContinue, "子代理已验证指定证据，主会话汇总已完成。", "", ""), Usage: usage},
	}}
	st, run, child, _ := newSubagentRunnerFixtureWithExternal(t, provider, domain.Budget{MaxTurns: 10}, 4, 4096, false, t.TempDir())
	router := llm.NewRouter(llm.ModelRef{Provider: provider.Name(), Model: "model"})
	router.RegisterProvider(provider)
	handoff := application.NewRunExecutionHandoffService(st, router, policy.NewDefaultChecker())
	service := application.NewSpecialistExecutionService(st, router, policy.NewDefaultChecker()).WithCompletionSummary(handoff)
	request := application.ContinueSpecialistRequest{RunID: run.ID, AgentID: child.ID, OperationKey: "continuous-child-summary-0001"}
	result, err := service.ContinueSpecialist(t.Context(), request)
	if err != nil || result.Schedule.RoundsCompleted != 2 || result.Schedule.StopReason != "all_terminal" || result.SummaryStatus != "completed" || provider.calls != 3 || len(result.AttemptIDs) != 2 {
		t.Fatalf("continuous completion=%+v calls=%d err=%v", result, provider.calls, err)
	}
	parentInput := provider.requests[2]
	var inbox, notification bool
	for _, message := range parentInput.Messages {
		inbox = inbox || (strings.Contains(message.Content, domain.RootInboxContextVersion) && strings.Contains(message.Content, report.Summary))
		notification = notification || strings.Contains(message.Content, "[Authorized child completion notification]")
	}
	if !inbox || !notification {
		t.Fatalf("main Agent did not receive the actual completion and attributed request: %+v", parentInput)
	}
	messages, err := st.ListSessionMessages(t.Context(), run.SessionID, true)
	if err != nil {
		t.Fatal(err)
	}
	var summaryCount int
	for _, message := range messages {
		if string(message.Role) == "assistant" && message.Content == "子代理已验证指定证据，主会话汇总已完成。" {
			summaryCount++
		}
	}
	if summaryCount != 1 {
		t.Fatalf("main conversation has %d summary messages: %+v", summaryCount, messages)
	}

	// A newer wake must remain untouched when the earlier child request replays.
	if _, err := st.EnqueueOperatorSteering(t.Context(), domain.EnqueueOperatorSteeringRequest{RunID: run.ID, SessionID: run.SessionID,
		Content: "A later operator turn", OperationKey: "continuous-later-input-0001", RequestedBy: "operator"}); err != nil {
		t.Fatal(err)
	}
	later, err := application.NewRunWakeControlService(st).Schedule(t.Context(), application.ScheduleRunWakeRequest{
		Version: domain.RunWakeControlProtocolVersion, RunID: run.ID, OperationKey: "continuous-later-wake-0001", RequestedBy: "operator",
		MaxAttempts: 2, BaseBackoffSeconds: 5, MaxBackoffSeconds: 30, MaxElapsedSeconds: 120,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := application.NewRunService(st).Pause(t.Context(), run.ID); err != nil {
		t.Fatal(err)
	}
	replay, err := service.ContinueSpecialist(t.Context(), request)
	if err != nil || !replay.Replayed || replay.Schedule.ID != result.Schedule.ID || replay.WakeID != result.WakeID || replay.SummaryStatus != "completed" || provider.calls != 3 {
		t.Fatalf("replay executed or lost the original summary: %+v calls=%d err=%v", replay, provider.calls, err)
	}
	latest, err := st.GetRunWakeIntent(t.Context(), later.Intent.ID)
	if err != nil || latest.Status != domain.RunWakeQueued {
		t.Fatalf("replay consumed a later wake: %+v %v", latest, err)
	}
}

func TestSpecialistContinuationDeliversSummaryAfterEarlierOperatorMessages(t *testing.T) {
	for _, earlier := range []int{1, 2} {
		t.Run(fmt.Sprintf("earlier-%d", earlier), func(t *testing.T) {
			report := domain.CompletionReport{Version: domain.CompletionReportVersion, Outcome: domain.CompletionSucceeded, Summary: "Durable child evidence for the queued summary", WorkItemIDs: []string{}, NoteIDs: []string{}}
			provider := &specialistTestProvider{responses: []llm.ChatResponse{{Text: specialistResponse(t, domain.SpecialistAction{Version: domain.SpecialistLifecycleVersion, Kind: domain.SpecialistActionFinish, Message: "Done", Report: &report})}}}
			for i := 0; i < earlier; i++ {
				provider.responses = append(provider.responses, llm.ChatResponse{Text: rootActionResponse(domain.RootActionContinue, fmt.Sprintf("Earlier request %d handled", i), "", "")})
			}
			provider.responses = append(provider.responses, llm.ChatResponse{Text: rootActionResponse(domain.RootActionContinue, "Queued child summary delivered", "", "")})
			st, run, child, _ := newSubagentRunnerFixtureWithExternal(t, provider, domain.Budget{MaxTurns: 20}, 4, 4096, false, t.TempDir())
			for i := 0; i < earlier; i++ {
				if _, err := st.EnqueueOperatorSteering(t.Context(), domain.EnqueueOperatorSteeringRequest{RunID: run.ID, SessionID: run.SessionID,
					Content: fmt.Sprintf("Earlier operator request %d", i), OperationKey: fmt.Sprintf("earlier-summary-input-%04d", i), RequestedBy: "operator"}); err != nil {
					t.Fatal(err)
				}
			}
			router := llm.NewRouter(llm.ModelRef{Provider: provider.Name(), Model: "model"})
			router.RegisterProvider(provider)
			service := application.NewSpecialistExecutionService(st, router, policy.NewDefaultChecker()).WithCompletionSummary(application.NewRunExecutionHandoffService(st, router, policy.NewDefaultChecker()))
			request := application.ContinueSpecialistRequest{RunID: run.ID, AgentID: child.ID, OperationKey: "summary-after-earlier-input-0001"}
			result, err := service.ContinueSpecialist(t.Context(), request)
			if err != nil || result.SummaryStatus != "completed" || provider.calls != earlier+2 {
				t.Fatalf("summary stranded behind older input: %+v calls=%d err=%v", result, provider.calls, err)
			}
			var reportRetained bool
			for _, message := range provider.requests[len(provider.requests)-1].Messages {
				reportRetained = reportRetained || strings.Contains(message.Content, report.Summary)
			}
			if !reportRetained {
				t.Fatal("the earlier operator turn consumed the child report and the summary lost its evidence")
			}
			messages, err := st.ListSessionMessages(t.Context(), run.SessionID, true)
			if err != nil {
				t.Fatal(err)
			}
			var summaryFound bool
			for _, message := range messages {
				summaryFound = summaryFound || (string(message.Role) == "assistant" && message.Content == "Queued child summary delivered")
			}
			queue, _ := st.GetOperatorSteeringQueueSummary(t.Context(), run.ID)
			if !summaryFound || queue.Pending != 0 {
				t.Fatalf("main summary was not delivered: messages=%+v queue=%+v", messages, queue)
			}
			replay, err := service.ContinueSpecialist(t.Context(), request)
			if err != nil || replay.SummaryStatus != "completed" || !replay.Replayed || provider.calls != earlier+2 {
				t.Fatalf("FIFO replay reexecuted earlier work: %+v calls=%d err=%v", replay, provider.calls, err)
			}
		})
	}
}

type specialistSummaryHandoffFunc func(context.Context, application.ExecuteRunHandoffRequest) (application.ExecuteRunHandoffResult, error)

func (f specialistSummaryHandoffFunc) Execute(ctx context.Context, request application.ExecuteRunHandoffRequest) (application.ExecuteRunHandoffResult, error) {
	return f(ctx, request)
}

func TestSpecialistContinuationRetriesSummaryWakeWithoutStrandingNotification(t *testing.T) {
	report := domain.CompletionReport{Version: domain.CompletionReportVersion, Outcome: domain.CompletionSucceeded,
		Summary: "Child evidence survives a wake retry", WorkItemIDs: []string{}, NoteIDs: []string{}}
	provider := &specialistTestProvider{responses: []llm.ChatResponse{
		{Text: specialistResponse(t, domain.SpecialistAction{Version: domain.SpecialistLifecycleVersion, Kind: domain.SpecialistActionFinish, Message: "Done", Report: &report})},
		{Text: rootActionResponse(domain.RootActionContinue, "Summary delivered after wake retry", "", "")},
	}}
	st, run, child, _ := newSubagentRunnerFixtureWithExternal(t, provider, domain.Budget{MaxTurns: 10}, 4, 4096, false, t.TempDir())
	router := llm.NewRouter(llm.ModelRef{Provider: provider.Name(), Model: "model"})
	router.RegisterProvider(provider)
	handoff := application.NewRunExecutionHandoffService(st, router, policy.NewDefaultChecker())
	var attempts int
	service := application.NewSpecialistExecutionService(st, router, policy.NewDefaultChecker()).WithCompletionSummary(
		specialistSummaryHandoffFunc(func(ctx context.Context, request application.ExecuteRunHandoffRequest) (application.ExecuteRunHandoffResult, error) {
			attempts++
			if attempts == 1 {
				return application.ExecuteRunHandoffResult{}, apperror.New(apperror.CodeUnavailable, "temporary handoff failure")
			}
			return handoff.Execute(ctx, request)
		}))
	result, err := service.ContinueSpecialist(t.Context(), application.ContinueSpecialistRequest{
		RunID: run.ID, AgentID: child.ID, OperationKey: "summary-wake-retry-0001",
	})
	queue, _ := st.GetOperatorSteeringQueueSummary(t.Context(), run.ID)
	if err != nil || result.SummaryStatus != "completed" || attempts != 2 || provider.calls != 2 || queue.Pending != 0 {
		t.Fatalf("summary wake retry stranded work: %+v attempts=%d calls=%d queue=%+v err=%v", result, attempts, provider.calls, queue, err)
	}
}

func TestSpecialistContinuationStopsAtParentPauseAndResumesSummaryWithSameKey(t *testing.T) {
	report := domain.CompletionReport{Version: domain.CompletionReportVersion, Outcome: domain.CompletionSucceeded, Summary: "Child completed before the parent's pause", WorkItemIDs: []string{}, NoteIDs: []string{}}
	provider := &specialistTestProvider{responses: []llm.ChatResponse{
		{Text: specialistResponse(t, domain.SpecialistAction{Version: domain.SpecialistLifecycleVersion, Kind: domain.SpecialistActionFinish, Message: "Done", Report: &report})},
		{Text: rootActionResponse(domain.RootActionContinue, "Earlier request handled before operator pauses", "", "")},
		{Text: rootActionResponse(domain.RootActionContinue, "Summary delivered after explicit resume", "", "")},
	}}
	st, run, child, _ := newSubagentRunnerFixtureWithExternal(t, provider, domain.Budget{MaxTurns: 12}, 4, 4096, false, t.TempDir())
	if _, err := st.EnqueueOperatorSteering(t.Context(), domain.EnqueueOperatorSteeringRequest{RunID: run.ID, SessionID: run.SessionID, Content: "Pause before proceeding", OperationKey: "earlier-summary-pause-0001", RequestedBy: "operator"}); err != nil {
		t.Fatal(err)
	}
	router := llm.NewRouter(llm.ModelRef{Provider: provider.Name(), Model: "model"})
	router.RegisterProvider(provider)
	handoff := application.NewRunExecutionHandoffService(st, router, policy.NewDefaultChecker())
	var pause sync.Once
	service := application.NewSpecialistExecutionService(st, router, policy.NewDefaultChecker()).WithCompletionSummary(
		specialistSummaryHandoffFunc(func(ctx context.Context, request application.ExecuteRunHandoffRequest) (application.ExecuteRunHandoffResult, error) {
			result, err := handoff.Execute(ctx, request)
			if err == nil {
				pause.Do(func() { _, err = application.NewRunService(st).Pause(ctx, run.ID) })
			}
			return result, err
		}))
	request := application.ContinueSpecialistRequest{RunID: run.ID, AgentID: child.ID, OperationKey: "summary-stops-at-parent-pause-0001"}
	result, err := service.ContinueSpecialist(t.Context(), request)
	paused, _ := st.GetRun(t.Context(), run.ID)
	if err != nil || result.SummaryStatus != "failed" || paused.Status != domain.RunPaused || provider.calls != 2 {
		t.Fatalf("summary crossed a parent pause: %+v Run=%+v calls=%d err=%v", result, paused, provider.calls, err)
	}
	if _, err := application.NewRunService(st).Resume(t.Context(), run.ID); err != nil {
		t.Fatal(err)
	}
	replay, err := service.ContinueSpecialist(t.Context(), request)
	if err != nil || replay.SummaryStatus != "completed" || !replay.Replayed || provider.calls != 3 {
		t.Fatalf("same-key recovery did not deliver summary: %+v calls=%d err=%v", replay, provider.calls, err)
	}
}

type busySpecialistSummaryProvider struct {
	*specialistTestProvider
	rootID           string
	once             sync.Once
	started, release chan struct{}
}

func (p *busySpecialistSummaryProvider) Chat(ctx context.Context, request llm.ChatRequest) (*llm.ChatResponse, error) {
	if request.Metadata["agent_id"] == p.rootID {
		p.once.Do(func() {
			close(p.started)
			select {
			case <-p.release:
			case <-ctx.Done():
			}
		})
	}
	return p.specialistTestProvider.Chat(ctx, request)
}

func (p *busySpecialistSummaryProvider) StreamChat(ctx context.Context, request llm.ChatRequest) (<-chan llm.ChatChunk, error) {
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

type busySpecialistSummaryStore struct {
	*store.SQLiteStore
	handoff          application.RunWakeExecutionHandoff
	provider         *busySpecialistSummaryProvider
	once, queuedOnce sync.Once
	queued           chan struct{}
	parentDone       chan error
}

func (s *busySpecialistSummaryStore) EnqueueOperatorSteering(ctx context.Context, request domain.EnqueueOperatorSteeringRequest) (domain.OperatorSteeringEnqueueResult, error) {
	if request.RequestedBy == "specialist_completion" {
		s.once.Do(func() {
			go func() {
				_, err := s.handoff.Execute(ctx, application.ExecuteRunHandoffRequest{Version: domain.RunExecutionHandoffProtocolVersion,
					RunID: request.RunID, MaxSteps: 1, OperationKey: "earlier-live-parent-turn-0001", RequestedBy: "operator"})
				s.parentDone <- err
			}()
			select {
			case <-s.provider.started:
			case <-ctx.Done():
			}
		})
	}
	result, err := s.SQLiteStore.EnqueueOperatorSteering(ctx, request)
	if request.RequestedBy == "specialist_completion" {
		s.queuedOnce.Do(func() { close(s.queued) })
	}
	return result, err
}

func TestSpecialistContinuationWaitsForFrozenParentHandoffWithoutActiveWake(t *testing.T) {
	report := domain.CompletionReport{Version: domain.CompletionReportVersion, Outcome: domain.CompletionSucceeded, Summary: "Child report retained while parent was busy", WorkItemIDs: []string{}, NoteIDs: []string{}}
	provider := &busySpecialistSummaryProvider{specialistTestProvider: &specialistTestProvider{responses: []llm.ChatResponse{
		{Text: specialistResponse(t, domain.SpecialistAction{Version: domain.SpecialistLifecycleVersion, Kind: domain.SpecialistActionFinish, Message: "Done", Report: &report})},
		{Text: rootActionResponse(domain.RootActionContinue, "Earlier live user turn completed", "", "")},
		{Text: rootActionResponse(domain.RootActionContinue, "Summary delivered after parent released its lease", "", "")},
	}}, started: make(chan struct{}), release: make(chan struct{})}
	base, run, child, _ := newSubagentRunnerFixtureWithExternal(t, provider, domain.Budget{MaxTurns: 12}, 4, 4096, false, t.TempDir())
	provider.rootID = child.ParentID
	if _, err := base.EnqueueOperatorSteering(t.Context(), domain.EnqueueOperatorSteeringRequest{RunID: run.ID, SessionID: run.SessionID, Content: "An earlier live user turn", OperationKey: "earlier-busy-parent-input-0001", RequestedBy: "operator"}); err != nil {
		t.Fatal(err)
	}
	router := llm.NewRouter(llm.ModelRef{Provider: provider.Name(), Model: "model"})
	router.RegisterProvider(provider)
	handoff := application.NewRunExecutionHandoffService(base, router, policy.NewDefaultChecker())
	st := &busySpecialistSummaryStore{SQLiteStore: base, handoff: handoff, provider: provider, queued: make(chan struct{}), parentDone: make(chan error, 1)}
	service := application.NewSpecialistExecutionService(st, router, policy.NewDefaultChecker()).WithCompletionSummary(handoff)
	request := application.ContinueSpecialistRequest{RunID: run.ID, AgentID: child.ID, OperationKey: "continuous-waits-for-parent-0001"}
	type outcome struct {
		result application.SpecialistContinuationResult
		err    error
	}
	done := make(chan outcome, 1)
	go func() { result, err := service.ContinueSpecialist(t.Context(), request); done <- outcome{result, err} }()
	<-st.queued
	select {
	case got := <-done:
		t.Fatalf("returned without delivering behind the frozen parent: %+v", got)
	default:
	}
	close(provider.release)
	got := <-done
	if err := <-st.parentDone; err != nil {
		t.Fatal(err)
	}
	if got.err != nil || got.result.SummaryStatus != "completed" || provider.calls != 3 {
		t.Fatalf("busy parent stranded summary: %+v calls=%d err=%v", got, provider.calls, got.err)
	}
	queue, _ := st.GetOperatorSteeringQueueSummary(t.Context(), run.ID)
	if queue.Pending != 0 {
		t.Fatalf("busy parent left summary pending: %+v", queue)
	}
}

func TestSpecialistContinuationRespectsTurnBudgetAndExplicitRunPause(t *testing.T) {
	provider := &specialistTestProvider{responses: []llm.ChatResponse{{
		Text:  specialistResponse(t, domain.SpecialistAction{Version: domain.SpecialistLifecycleVersion, Kind: domain.SpecialistActionContinue, Message: "More work remains"}),
		Usage: llm.Usage{InputTokens: 2, OutputTokens: 2, TotalTokens: 4},
	}}}
	st, run, child, _ := newSubagentRunnerFixtureWithExternal(t, provider, domain.Budget{MaxTurns: 10}, 1, 4096, false, t.TempDir())
	router := llm.NewRouter(llm.ModelRef{Provider: provider.Name(), Model: "model"})
	router.RegisterProvider(provider)
	service := application.NewSpecialistExecutionService(st, router, policy.NewDefaultChecker()).WithCompletionSummary(application.NewRunExecutionHandoffService(st, router, policy.NewDefaultChecker()))
	request := application.ContinueSpecialistRequest{RunID: run.ID, AgentID: child.ID, OperationKey: "continuous-budget-stop-0001"}
	if _, err := application.NewRunService(st).Pause(t.Context(), run.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := service.ContinueSpecialist(t.Context(), request); apperror.CodeOf(err) != apperror.CodeFailedPrecondition || provider.calls != 0 {
		t.Fatalf("paused Run dispatched: %v calls=%d", err, provider.calls)
	}
	if _, err := application.NewRunService(st).Resume(t.Context(), run.ID); err != nil {
		t.Fatal(err)
	}
	result, err := service.ContinueSpecialist(t.Context(), request)
	if err != nil || result.Schedule.Status != domain.SpecialistScheduleFailed || result.SummaryStatus != "not_requested" || provider.calls != 1 {
		t.Fatalf("turn budget did not stop child before summary: %+v calls=%d err=%v", result, provider.calls, err)
	}
	queue, _ := st.GetOperatorSteeringQueueSummary(t.Context(), run.ID)
	if queue.Pending != 0 {
		t.Fatalf("unfinished child queued summary: %+v", queue)
	}
}

func TestSpecialistContinuationCancellationStopsWithoutSummaryAndReplays(t *testing.T) {
	provider := &specialistTestProvider{block: true, started: make(chan struct{})}
	st, run, child, _ := newSubagentRunnerFixtureWithExternal(t, provider, domain.Budget{MaxTurns: 10}, 4, 4096, false, t.TempDir())
	router := llm.NewRouter(llm.ModelRef{Provider: provider.Name(), Model: "model"})
	router.RegisterProvider(provider)
	service := application.NewSpecialistExecutionService(st, router, policy.NewDefaultChecker()).WithCompletionSummary(application.NewRunExecutionHandoffService(st, router, policy.NewDefaultChecker()))
	request := application.ContinueSpecialistRequest{RunID: run.ID, AgentID: child.ID, OperationKey: "continuous-cancellation-0001"}
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() { _, err := service.ContinueSpecialist(ctx, request); done <- err }()
	<-provider.started
	cancel()
	if err := <-done; apperror.CodeOf(err) != apperror.CodeCancelled {
		t.Fatalf("cancellation: %v", err)
	}
	replay, err := service.ContinueSpecialist(t.Context(), request)
	if err != nil || !replay.Replayed || replay.Schedule.Status != domain.SpecialistScheduleCancelled || replay.SummaryStatus != "cancelled" || provider.calls != 1 {
		t.Fatalf("cancelled child replayed work: %+v calls=%d err=%v", replay, provider.calls, err)
	}
	queue, _ := st.GetOperatorSteeringQueueSummary(t.Context(), run.ID)
	if queue.Pending != 0 {
		t.Fatalf("cancelled child queued a summary: %+v", queue)
	}
}
