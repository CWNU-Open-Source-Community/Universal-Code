package application_test

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"cyberagent-workbench/internal/application"
	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/events"
	"cyberagent-workbench/internal/llm"
	"cyberagent-workbench/internal/policy"
	"cyberagent-workbench/internal/session"
	"cyberagent-workbench/internal/store"
)

func TestSessionRunChatQueuesConcurrentOperatorInputAtSafeBoundary(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "cyberagent.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	ctx := context.Background()
	service := application.NewRunService(st)
	_, run, err := service.Create(ctx, application.CreateRunRequest{
		Goal: "concurrent operator steering", Profile: "review",
		ModelRoute: "lifecycle-test/model", Interactive: true,
		Budget: domain.Budget{MaxTurns: 4},
	})
	if err != nil {
		t.Fatal(err)
	}
	started := make(chan struct{})
	release := make(chan struct{})
	provider := &lifecycleProvider{responses: []string{
		rootActionResponse(domain.RootActionFinish, "first result", "first done", ""),
		rootActionResponse(domain.RootActionFinish, "second result", "all done", ""),
	}, afterResponse: func(index int) {
		if index == 0 {
			close(started)
			<-release
		}
	}}
	router := llm.NewRouter(llm.ModelRef{Provider: provider.Name(), Model: "model"})
	router.RegisterProvider(provider)
	sess, err := st.GetSession(ctx, run.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	manager := session.NewManager(st, router, policy.NewDefaultChecker()).WithRunChatExecutor(
		application.NewSessionRunChatExecutor(st, router, policy.NewDefaultChecker()),
	)
	firstDone := make(chan struct {
		result session.SendResult
		err    error
	}, 1)
	go func() {
		result, sendErr := manager.Send(ctx, sess.ID, "finish the initial request")
		firstDone <- struct {
			result session.SendResult
			err    error
		}{result: result, err: sendErr}
	}()
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("first model call did not reach its blocking boundary")
	}
	queued, err := manager.Send(ctx, sess.ID, "also verify the queued requirement")
	if err != nil || !queued.Queued || queued.RunAction != "queued" ||
		queued.SteeringID == "" || queued.SteeringSequence != 1 || queued.Compacted ||
		queued.UserMessage.ID != 0 || queued.ReplyMessage.ID != 0 {
		t.Fatalf("concurrent Session input was not queued cleanly: result=%#v err=%v", queued, err)
	}
	history, err := st.ListSessionMessages(ctx, sess.ID, true)
	if err != nil || len(history) != 0 {
		t.Fatalf("queued input was committed before a safe boundary: history=%#v err=%v", history, err)
	}
	close(release)
	var first struct {
		result session.SendResult
		err    error
	}
	select {
	case first = <-firstDone:
	case <-time.After(5 * time.Second):
		t.Fatal("first Session turn did not finish after release")
	}
	if first.err != nil || first.result.RunAction != string(domain.RootActionContinue) ||
		first.result.RunStatus != string(domain.RunRunning) {
		t.Fatalf("queued input did not defer the first finish action: result=%#v err=%v",
			first.result, first.err)
	}
	step, err := application.NewAgentRunner(st, router, policy.NewDefaultChecker()).Step(ctx, run.ID)
	if err != nil || step.Action.Kind != domain.RootActionContinue || step.RunStatus != domain.RunRunning ||
		step.UserMessage.Content != "also verify the queued requirement" {
		t.Fatalf("queued input was not delivered by the next Supervisor step: result=%#v err=%v",
			step, err)
	}
	history, err = st.ListSessionMessages(ctx, sess.ID, true)
	if err != nil || len(history) != 4 || history[0].Content != "finish the initial request" ||
		history[2].Content != "also verify the queued requirement" {
		t.Fatalf("concurrent Session history is not exactly-once and ordered: %#v err=%v",
			history, err)
	}
}

func TestSessionRunChatKeepsCommittedQueueFactWhenPausedRunAlreadyHasGuidance(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "cyberagent.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	ctx := context.Background()
	runs := application.NewRunService(st)
	_, created, err := runs.Create(ctx, application.CreateRunRequest{
		Goal: "paused queue ordering", Profile: "review", Budget: domain.Budget{MaxTurns: 4},
	})
	if err != nil {
		t.Fatal(err)
	}
	run, err := runs.Start(ctx, created.ID)
	if err != nil {
		t.Fatal(err)
	}
	run, err = runs.Pause(ctx, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.EnqueueOperatorSteering(ctx, domain.EnqueueOperatorSteeringRequest{
		RunID: run.ID, SessionID: run.SessionID, Content: "first queued guidance",
		OperationKey: "paused-session-steering-first-0001", RequestedBy: "operator",
	}); err != nil {
		t.Fatal(err)
	}
	sess, err := st.GetSession(ctx, run.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	manager := session.NewManager(st, llm.NewDefaultRouter(), policy.NewDefaultChecker()).
		WithRunChatExecutor(application.NewSessionRunChatExecutor(st,
			llm.NewDefaultRouter(), policy.NewDefaultChecker()))
	result, err := manager.Send(ctx, sess.ID, "second queued guidance")
	if err != nil || !result.Queued || result.RunStatus != string(domain.RunPaused) ||
		result.SteeringSequence != 2 {
		t.Fatalf("paused Session did not report its committed queue fact: result=%#v err=%v",
			result, err)
	}
	persisted, err := st.GetRun(ctx, run.ID)
	if err != nil || persisted.Status != domain.RunPaused {
		t.Fatalf("queueing changed paused Run lifecycle: run=%#v err=%v", persisted, err)
	}
	summary, err := st.GetOperatorSteeringQueueSummary(ctx, run.ID)
	if err != nil || summary.Pending != 2 || summary.Prepared != 0 {
		t.Fatalf("paused queue ordering was not durable: %#v err=%v", summary, err)
	}
}

func TestSessionRunChatAutoStartsAndCommitsOneMessagePair(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "cyberagent.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	ctx := context.Background()
	service := application.NewRunService(st)
	_, run, err := service.Create(ctx, application.CreateRunRequest{
		Goal: "interactive review", Profile: "review", Budget: domain.Budget{MaxTurns: 4},
	})
	if err != nil {
		t.Fatal(err)
	}
	sess, err := st.GetSession(ctx, run.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	router := llm.NewDefaultRouter()
	manager := session.NewManager(st, router, policy.NewDefaultChecker()).WithRunChatExecutor(
		application.NewSessionRunChatExecutor(st, router, policy.NewDefaultChecker()),
	)
	result, err := manager.Send(ctx, sess.ID, "review this exact input")
	if err != nil {
		t.Fatal(err)
	}
	if result.RunID != run.ID || result.RunAction != string(domain.RootActionContinue) || result.RunStatus != string(domain.RunRunning) {
		t.Fatalf("unexpected run chat result: %#v", result)
	}
	if result.UserMessage.Content != "review this exact input" || result.UserMessage.ID == 0 || result.ReplyMessage.ID == 0 {
		t.Fatalf("unexpected committed messages: %#v", result)
	}
	messages, err := st.ListSessionMessages(ctx, sess.ID, true)
	if err != nil {
		t.Fatal(err)
	}
	if len(messages) != 2 || messages[0].Content != "review this exact input" || messages[0].ID != result.UserMessage.ID || messages[1].ID != result.ReplyMessage.ID {
		t.Fatalf("run chat duplicated or changed messages: %#v", messages)
	}
	persisted, err := st.GetRun(ctx, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	checkpoint, ok, err := st.GetSupervisorCheckpoint(ctx, run.ID)
	if err != nil || !ok {
		t.Fatalf("checkpoint lookup ok=%t err=%v", ok, err)
	}
	if persisted.Status != domain.RunRunning || checkpoint.NextTurn != 2 || checkpoint.Phase != domain.SupervisorIdle || checkpoint.PendingInput != "" {
		t.Fatalf("unexpected persisted run chat state: run=%#v checkpoint=%#v", persisted, checkpoint)
	}
	items, err := st.ListRunEvents(ctx, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if countEventType(items, events.AgentTurnCompletedEvent) != 1 || countEventType(items, events.SupervisorActionEvent) != 1 || countEventType(items, events.SessionMessageEvent) != 2 {
		t.Fatalf("unexpected run chat events: %#v", items)
	}
}

func TestSessionRunChatKeepsLegacyUnboundSessionCompatible(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "cyberagent.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	ctx := context.Background()
	router := llm.NewDefaultRouter()
	manager := session.NewManager(st, router, policy.NewDefaultChecker()).WithRunChatExecutor(
		application.NewSessionRunChatExecutor(st, router, policy.NewDefaultChecker()),
	)
	sess, err := manager.Create(ctx, "", "legacy session", "learn")
	if err != nil {
		t.Fatal(err)
	}
	result, err := manager.Send(ctx, sess.ID, "legacy chat")
	if err != nil {
		t.Fatal(err)
	}
	if result.RunID != "" || result.RunAction != "" || result.UserMessage.Content != "legacy chat" {
		t.Fatalf("legacy session unexpectedly used a run: %#v", result)
	}
	messages, err := st.ListSessionMessages(ctx, sess.ID, true)
	if err != nil || len(messages) != 2 {
		t.Fatalf("legacy message persistence len=%d err=%v", len(messages), err)
	}
}

func TestSessionRunChatWaitResumesWithNewInputAcrossRestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "cyberagent.db")
	st, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.Close() }()
	ctx := context.Background()
	service := application.NewRunService(st)
	_, run, err := service.Create(ctx, application.CreateRunRequest{
		Goal: "interactive wait", Profile: "learn", ModelRoute: "lifecycle-test/model", Budget: domain.Budget{MaxTurns: 4},
	})
	if err != nil {
		t.Fatal(err)
	}
	provider := &lifecycleProvider{responses: []string{
		rootActionResponse(domain.RootActionWait, "Please provide a choice.", "", "user choice required"),
		rootActionResponse(domain.RootActionContinue, "Choice received.", "", ""),
	}}
	router := llm.NewRouter(llm.ModelRef{Provider: provider.Name(), Model: "model"})
	router.RegisterProvider(provider)
	sess, err := st.GetSession(ctx, run.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	manager := session.NewManager(st, router, policy.NewDefaultChecker()).WithRunChatExecutor(
		application.NewSessionRunChatExecutor(st, router, policy.NewDefaultChecker()),
	)
	first, err := manager.Send(ctx, sess.ID, "Which path should I take?")
	if err != nil {
		t.Fatal(err)
	}
	if first.RunAction != string(domain.RootActionWait) || first.RunStatus != string(domain.RunPaused) {
		t.Fatalf("unexpected wait result: %#v", first)
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	st, err = store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	manager = session.NewManager(st, router, policy.NewDefaultChecker()).WithRunChatExecutor(
		application.NewSessionRunChatExecutor(st, router, policy.NewDefaultChecker()),
	)
	second, err := manager.Send(ctx, sess.ID, "Take the safe path.")
	if err != nil {
		t.Fatal(err)
	}
	if second.RunAction != string(domain.RootActionContinue) || second.RunStatus != string(domain.RunRunning) {
		t.Fatalf("unexpected resumed result: %#v", second)
	}
	messages, err := st.ListSessionMessages(ctx, sess.ID, true)
	if err != nil {
		t.Fatal(err)
	}
	if len(messages) != 4 || messages[0].Content != "Which path should I take?" || messages[2].Content != "Take the safe path." {
		t.Fatalf("unexpected resumed history: %#v", messages)
	}
	checkpoint, ok, err := st.GetSupervisorCheckpoint(ctx, run.ID)
	if err != nil || !ok || checkpoint.NextTurn != 3 || checkpoint.Phase != domain.SupervisorIdle || checkpoint.PendingInput != "" {
		t.Fatalf("unexpected resumed checkpoint ok=%t checkpoint=%#v err=%v", ok, checkpoint, err)
	}
}

func TestSessionRunChatTreatsPlainRepliesAndFinishAsNonTerminal(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "cyberagent.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	ctx := context.Background()
	runs := application.NewRunService(st)
	_, created, err := runs.Create(ctx, application.CreateRunRequest{
		Goal: "terminal session", Profile: "review", ModelRoute: "lifecycle-test/model",
		Interactive: true, Budget: domain.Budget{MaxTurns: 3},
	})
	if err != nil {
		t.Fatal(err)
	}
	run, err := runs.Start(ctx, created.ID)
	if err != nil {
		t.Fatal(err)
	}
	provider := &lifecycleProvider{responses: []string{
		"你好，我是普通公开回复，不是生命周期 JSON。",
		rootActionResponse(domain.RootActionFinish, "Finished this turn.", "session complete", ""),
	}}
	router := llm.NewRouter(llm.ModelRef{Provider: provider.Name(), Model: "model"})
	router.RegisterProvider(provider)
	handoff := application.NewRunExecutionHandoffService(st, router, policy.NewDefaultChecker())
	queue := func(content, operationKey string) {
		t.Helper()
		if _, queueErr := st.EnqueueOperatorSteering(ctx, domain.EnqueueOperatorSteeringRequest{
			RunID: run.ID, SessionID: run.SessionID, Content: content,
			OperationKey: operationKey, RequestedBy: "desktop_operator",
		}); queueErr != nil {
			t.Fatal(queueErr)
		}
	}
	execute := func(operationKey string) application.LifecycleResult {
		t.Helper()
		result, executeErr := handoff.Execute(ctx, application.ExecuteRunHandoffRequest{
			Version: domain.RunExecutionHandoffProtocolVersion, RunID: run.ID,
			MaxSteps: 1, OperationKey: operationKey, RequestedBy: "desktop_operator",
		})
		if executeErr != nil {
			t.Fatal(executeErr)
		}
		if len(result.Execution.Steps) != 1 {
			t.Fatalf("handoff steps = %d, want 1: %#v", len(result.Execution.Steps), result)
		}
		return result.Execution.Steps[0]
	}
	queue("first message", "plain-reply-steering-0001")
	first := execute("plain-reply-handoff-0001")
	if first.Action.Kind != domain.RootActionContinue ||
		first.RunStatus != domain.RunRunning ||
		first.ReplyMessage.Content != "你好，我是普通公开回复，不是生命周期 JSON。" {
		t.Fatalf("plain reply did not remain interactive: %#v", first)
	}
	queue("second message", "finish-reply-steering-0002")
	second := execute("finish-reply-handoff-0002")
	if second.Action.Kind != domain.RootActionContinue ||
		second.RunStatus != domain.RunRunning ||
		second.ReplyMessage.Content != "Finished this turn." {
		t.Fatalf("interactive finish was not deferred: %#v", second)
	}
	messages, err := st.ListSessionMessages(ctx, run.SessionID, true)
	if err != nil {
		t.Fatal(err)
	}
	if len(messages) != 4 {
		t.Fatalf("multi-turn history len=%d err=%v", len(messages), err)
	}
	eventItems, err := st.ListRunEvents(ctx, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	foundRequestedFinish := false
	for _, item := range eventItems {
		if item.Type == events.AgentTurnCompletedEvent &&
			strings.Contains(item.PayloadJSON, `"requested_lifecycle_action":"finish"`) &&
			strings.Contains(item.PayloadJSON, `"lifecycle_action":"continue"`) {
			foundRequestedFinish = true
			break
		}
	}
	if !foundRequestedFinish {
		t.Fatalf("interactive finish deferral was not audited: %#v", eventItems)
	}
}

// Keep the ordinary reply queue separate from the auxiliary model purpose.
// This fixture exercises a declared generation failure and the real extractive
// fallback; generated-summary success has a dedicated protocol fixture.
type extractiveSessionProvider struct {
	lifecycleProvider
	summaryCalls int
}

func (p *extractiveSessionProvider) Chat(ctx context.Context, request llm.ChatRequest) (*llm.ChatResponse, error) {
	if request.Metadata["purpose"] == "context_compaction" {
		p.summaryCalls++
		return nil, llm.NewProviderError(llm.OutcomePermanent, p.Name(), "fixture summary generation unavailable", nil)
	}
	return p.lifecycleProvider.Chat(ctx, request)
}

func TestSessionRunChatFeedsCompactedSummaryBackToSupervisor(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "cyberagent.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	ctx := context.Background()
	service := application.NewRunService(st)
	_, run, err := service.Create(ctx, application.CreateRunRequest{
		Goal: "long interactive session", Profile: "learn", ModelRoute: "lifecycle-test/model", Budget: domain.Budget{MaxTurns: 16},
	})
	if err != nil {
		t.Fatal(err)
	}
	responses := make([]string, 12)
	for i := range responses {
		responses[i] = rootActionResponse(domain.RootActionContinue, compactionFixtureObservation()+strings.Repeat("Extra observation. ", 73), "", "")
	}
	provider := &extractiveSessionProvider{lifecycleProvider: lifecycleProvider{responses: responses}}
	router := llm.NewRouter(llm.ModelRef{Provider: provider.Name(), Model: "model"})
	router.RegisterProvider(provider)
	sess, err := st.GetSession(ctx, run.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	manager := session.NewManager(st, router, policy.NewDefaultChecker()).WithRunChatExecutor(
		application.NewSessionRunChatExecutor(st, router, policy.NewDefaultChecker()),
	)
	var compacted session.SendResult
	for i := 1; i <= 12; i++ {
		result, err := manager.Send(ctx, sess.ID, fmt.Sprintf("message %d", i))
		if err != nil {
			t.Fatal(err)
		}
		if result.Compacted {
			compacted = result
		}
	}
	if !compacted.Compacted || compacted.SummaryID == 0 {
		t.Fatalf("executor-owned compaction was not reported: last request input=%s, limit=%s", provider.requests[11].Metadata["context_input_estimate"], provider.requests[11].Metadata["context_input_limit"])
	}
	if len(provider.requests) != 12 {
		t.Fatalf("provider request count = %d, want 12", len(provider.requests))
	}
	if provider.summaryCalls != 1 {
		t.Fatalf("expected one explicit auxiliary failure before extractive fallback, got %d", provider.summaryCalls)
	}
	saved, found, err := st.LatestContextSummary(ctx, run.SessionID)
	if err != nil || !found || saved.ID != compacted.SummaryID {
		t.Fatalf("committed summary ID = %d, want %d, found=%v err=%v", saved.ID, compacted.SummaryID, found, err)
	}
	ref := fmt.Sprintf("summary:%d (SHA-256 %s)", saved.ID, saved.ContentSHA256)
	transcripts := 0
	for _, message := range provider.requests[11].Messages {
		if strings.Contains(message.Content, ref) {
			if message.Role != "assistant" || !strings.Contains(message.Content, "Recorded compatibility observation.") {
				t.Fatalf("summary %d did not carry the historical assistant observation in an assistant message (role=%s)", saved.ID, message.Role)
			}
			transcripts++
		}
	}
	if transcripts != 1 {
		t.Fatalf("last Supervisor request contained %d copies of committed summary %d, want one", transcripts, saved.ID)
	}
}
