package application_test

import (
	"path/filepath"
	"strings"
	"testing"

	"cyberagent-workbench/internal/application"
	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/llm"
	"cyberagent-workbench/internal/policy"
	"cyberagent-workbench/internal/store"
)

func oneShotFixture(t *testing.T, responses []string) (*store.SQLiteStore,
	*application.OneShotService, domain.Run) {
	t.Helper()
	base, err := store.Open(filepath.Join(t.TempDir(), "one-shot.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = base.Close() })
	provider := &lifecycleProvider{responses: responses}
	router := llm.NewRouter(llm.ModelRef{Provider: provider.Name(), Model: "model"})
	router.RegisterProvider(provider)
	_, run, err := application.NewRunService(base).Create(t.Context(),
		application.CreateRunRequest{Goal: "one-shot exec", Profile: "review",
			ModelRoute: "lifecycle-test/model", Interactive: true,
			Budget: domain.Budget{MaxTurns: 4}})
	if err != nil {
		t.Fatal(err)
	}
	turns := application.NewThreadTurnService(base,
		application.NewRunLifecycleControlService(base),
		application.NewRunExecutionHandoffService(base, router, policy.NewDefaultChecker()))
	return base, application.NewOneShotService(base, turns), run
}

func TestOneShotRunReturnsTerminalReply(t *testing.T) {
	_, service, run := oneShotFixture(t, []string{
		rootActionResponse(domain.RootActionFinish, "Delivery is ready.", "done", ""),
	})
	result, err := service.Run(t.Context(), application.OneShotRequest{
		Version:      domain.ThreadMessageProtocolVersion,
		ThreadID:     domain.InitialThreadID(run.ID),
		Content:      "Finish this delivered turn.",
		OperationKey: "one-shot-happy-key",
		RequestedBy:  "cli_exec_operator",
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.RunID != run.ID || result.ThreadID != domain.InitialThreadID(run.ID) {
		t.Fatalf("one-shot identity is wrong: %#v", result)
	}
	if result.TurnStatus != string(domain.RunExecutionHandoffCompleted) {
		t.Fatalf("one-shot turn status %q", result.TurnStatus)
	}
	if result.StopReason != "turn_finish" {
		t.Fatalf("one-shot stop reason %q", result.StopReason)
	}
	if strings.TrimSpace(result.Reply) != "Delivery is ready." {
		t.Fatalf("one-shot reply %q", result.Reply)
	}
}

func TestOneShotRunRejectsEmptyPrompt(t *testing.T) {
	_, service, _ := oneShotFixture(t, []string{
		rootActionResponse(domain.RootActionFinish, "unused", "done", ""),
	})
	if _, err := service.Run(t.Context(), application.OneShotRequest{
		Version:      domain.ThreadMessageProtocolVersion,
		ThreadID:     "thread-one",
		Content:      "   ",
		OperationKey: "one-shot-empty-key",
		RequestedBy:  "cli_exec_operator",
	}); err == nil {
		t.Fatal("empty prompt was accepted")
	}
}

func TestOneShotRunSurfacesTurnFailure(t *testing.T) {
	// No provider is registered on the router, so the model call fails and the
	// failed handoff surfaces as an error instead of a result.
	base, err := store.Open(filepath.Join(t.TempDir(), "one-shot-failure.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = base.Close() })
	emptyRouter := llm.NewRouter(llm.ModelRef{Provider: "none", Model: "model"})
	_, run, err := application.NewRunService(base).Create(t.Context(),
		application.CreateRunRequest{Goal: "one-shot failure", Profile: "review",
			ModelRoute: "none/model", Interactive: true,
			Budget: domain.Budget{MaxTurns: 4}})
	if err != nil {
		t.Fatal(err)
	}
	turns := application.NewThreadTurnService(base,
		application.NewRunLifecycleControlService(base),
		application.NewRunExecutionHandoffService(base, emptyRouter, policy.NewDefaultChecker()))
	service := application.NewOneShotService(base, turns)
	_, err = service.Run(t.Context(), application.OneShotRequest{
		Version:      domain.ThreadMessageProtocolVersion,
		ThreadID:     domain.InitialThreadID(run.ID),
		Content:      "Finish this delivered turn.",
		OperationKey: "one-shot-failure-key",
		RequestedBy:  "cli_exec_operator",
	})
	if err == nil {
		t.Fatal("failed handoff did not surface as an error")
	}
}

func TestOneShotRunDetectsSupervisorPause(t *testing.T) {
	// A provider response without a finish action keeps the supervisor in the
	// turn; the one-shot result must expose the missing completion instead of
	// reporting success.
	base, err := store.Open(filepath.Join(t.TempDir(), "one-shot-pause.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = base.Close() })
	provider := &lifecycleProvider{responses: []string{
		rootActionResponse(domain.RootActionContinue, "Working on it.", "", ""),
	}}
	router := llm.NewRouter(llm.ModelRef{Provider: provider.Name(), Model: "model"})
	router.RegisterProvider(provider)
	_, run, err := application.NewRunService(base).Create(t.Context(),
		application.CreateRunRequest{Goal: "one-shot pause", Profile: "review",
			ModelRoute: "lifecycle-test/model", Interactive: true,
			Budget: domain.Budget{MaxTurns: 1}})
	if err != nil {
		t.Fatal(err)
	}
	turns := application.NewThreadTurnService(base,
		application.NewRunLifecycleControlService(base),
		application.NewRunExecutionHandoffService(base, router, policy.NewDefaultChecker()))
	service := application.NewOneShotService(base, turns)
	result, err := service.Run(t.Context(), application.OneShotRequest{
		Version:      domain.ThreadMessageProtocolVersion,
		ThreadID:     domain.InitialThreadID(run.ID),
		Content:      "Keep working.",
		OperationKey: "one-shot-pause-key",
		RequestedBy:  "cli_exec_operator",
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.StopReason == "turn_finish" {
		t.Fatalf("unfinished supervisor turn reported finish: %#v", result)
	}
}

func TestOneShotRunContinuesExistingThread(t *testing.T) {
	_, service, run := oneShotFixture(t, []string{
		rootActionResponse(domain.RootActionFinish, "First task done.", "done", ""),
		rootActionResponse(domain.RootActionFinish, "Second task done.", "done", ""),
	})
	ctx := t.Context()
	// First turn executes to completion; the run stays running (Finish does
	// not transition to a terminal state).
	if _, err := service.Run(ctx, application.OneShotRequest{
		Version:      domain.ThreadMessageProtocolVersion,
		ThreadID:     domain.InitialThreadID(run.ID),
		Content:      "First prompt.",
		OperationKey: "one-shot-continue-1",
		RequestedBy:  "cli_exec_operator",
	}); err != nil {
		t.Fatal(err)
	}
	// Second turn continues the same thread — the turn machinery creates a
	// successor Run when the previous one is terminal.
	result, err := service.Run(ctx, application.OneShotRequest{
		Version:      domain.ThreadMessageProtocolVersion,
		ThreadID:     domain.InitialThreadID(run.ID),
		Content:      "Second prompt.",
		OperationKey: "one-shot-continue-2",
		RequestedBy:  "cli_exec_operator",
	})
	if err != nil {
		t.Fatal(err)
	}
	// The run stays running after a Finish action; the follow-up prompt is
	// executed in the same Run (no successor needed for a non-terminal run).
	if result.TurnStatus != string(domain.RunExecutionHandoffCompleted) {
		t.Fatalf("continued turn status %q", result.TurnStatus)
	}
	if strings.TrimSpace(result.Reply) != "Second task done." {
		t.Fatalf("continued reply %q", result.Reply)
	}
}

func TestOneShotRunContinueUnknownThreadFails(t *testing.T) {
	base, err := store.Open(filepath.Join(t.TempDir(), "one-shot-unknown.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = base.Close() })
	provider := &lifecycleProvider{responses: []string{
		rootActionResponse(domain.RootActionFinish, "unused", "done", ""),
	}}
	router := llm.NewRouter(llm.ModelRef{Provider: provider.Name(), Model: "model"})
	router.RegisterProvider(provider)
	turns := application.NewThreadTurnService(base,
		application.NewRunLifecycleControlService(base),
		application.NewRunExecutionHandoffService(base, router, policy.NewDefaultChecker()))
	service := application.NewOneShotService(base, turns)
	_, err = service.Run(t.Context(), application.OneShotRequest{
		Version:      domain.ThreadMessageProtocolVersion,
		ThreadID:     "thread-nonexistent-0001",
		Content:      "prompt",
		OperationKey: "one-shot-unknown-key",
		RequestedBy:  "cli_exec_operator",
	})
	if err == nil {
		t.Fatal("unknown thread was accepted")
	}
}
