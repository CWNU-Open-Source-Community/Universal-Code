package runner

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

type commandRuntimeFailedHandoffStore struct {
	*commandRuntimeMemoryStore
	starter      *commandRuntimeFakeStarter
	entered      chan struct{}
	failure      error
	killFailures int
}

func (s *commandRuntimeFailedHandoffStore) UpdateCommandRuntimeJob(ctx context.Context, job CommandRuntimeJob, previous int64) (CommandRuntimeJob, error) {
	if previous == 1 && job.State == CommandRuntimeJobRunning {
		process := s.starter.last()
		process.mu.Lock()
		process.killFailures = s.killFailures
		process.mu.Unlock()
		close(s.entered)
		return CommandRuntimeJob{}, s.failure
	}
	return s.commandRuntimeMemoryStore.UpdateCommandRuntimeJob(ctx, job, previous)
}

func TestCommandRuntimeFailedHandoffRetainsStalledProcessWithoutBlockingShutdown(t *testing.T) {
	for _, slowCleanup := range []bool{false, true} {
		name := "kill_failure"
		if slowCleanup {
			name = "slow_cleanup"
		}
		t.Run(name, func(t *testing.T) { testCommandRuntimeFailedHandoffStalledCleanup(t, slowCleanup) })
	}
}

type commandRuntimeSlowHandoffStarter struct{ *commandRuntimeFakeStarter }

func (s commandRuntimeSlowHandoffStarter) Start(ctx context.Context, scope CommandRuntimeScope, spec CommandRuntimeResolvedSpec) (commandRuntimeProcess, error) {
	process, err := s.commandRuntimeFakeStarter.Start(ctx, scope, spec)
	if err != nil {
		return nil, err
	}
	return commandRuntimeSlowHandoffProcess{process.(*commandRuntimeFakeProcess)}, nil
}

type commandRuntimeSlowHandoffProcess struct{ *commandRuntimeFakeProcess }

// A sandbox can acknowledge cancellation before its independent cleanup completes.
func (commandRuntimeSlowHandoffProcess) Kill() error { return nil }

func testCommandRuntimeFailedHandoffStalledCleanup(t *testing.T, slowCleanup bool) {
	t.Helper()
	starter := &commandRuntimeFakeStarter{}
	failure := errors.New("injected running handoff failure")
	state := &commandRuntimeFailedHandoffStore{newCommandRuntimeMemoryStore(), starter, make(chan struct{}), failure, 100}
	processStarter := commandRuntimeStarter(starter)
	if slowCleanup {
		state.killFailures = 0
		processStarter = commandRuntimeSlowHandoffStarter{starter}
	}
	manager, err := NewCommandRuntimeManager(state, processStarter, "failed-handoff-owner")
	if err != nil {
		t.Fatal(err)
	}
	request := commandRuntimeTestRequest(manager, 60_000)
	startCtx, cancelStart := context.WithTimeout(t.Context(), 50*time.Millisecond)
	defer cancelStart()
	startDone := make(chan error, 1)
	go func() { _, _, err := manager.Start(startCtx, request); startDone <- err }()
	select {
	case <-state.entered:
	case <-time.After(time.Second):
		t.Fatal("handoff not reached")
	}
	process := starter.last()
	defer process.finish(125)
	var startErr error
	startReturned := false
	select {
	case startErr = <-startDone:
		startReturned = true
	case <-time.After(200 * time.Millisecond):
		t.Error("failed handoff left Start waiting past its cleanup deadline")
	}
	_, jobID := CommandRuntimeOperationIdentity(request.Scope.RunID, request.Scope.OperationKey)
	if startReturned {
		if !errors.Is(startErr, failure) || !slowCleanup && !strings.Contains(startErr.Error(), "kill failure") || slowCleanup && !errors.Is(startErr, context.DeadlineExceeded) {
			t.Errorf("cleanup lost the handoff or Kill failure: %v", startErr)
		}
		job, getErr := state.GetCommandRuntimeJob(t.Context(), jobID)
		if getErr != nil || !manager.OwnsActiveJob(job) {
			t.Errorf("failed handoff abandoned its live native process: %+v %v", job, getErr)
		}
	}
	shutdownCtx, cancelShutdown := context.WithTimeout(t.Context(), 50*time.Millisecond)
	defer cancelShutdown()
	shutdownDone := make(chan error, 1)
	go func() { shutdownDone <- manager.Shutdown(shutdownCtx) }()
	shutdownReturned := false
	select {
	case err := <-shutdownDone:
		shutdownReturned = true
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Errorf("live native process was reported drained: %v", err)
		}
	case <-time.After(200 * time.Millisecond):
		t.Error("failed handoff held startMu against the Shutdown deadline")
	}
	process.mu.Lock()
	process.killFailures = 0
	process.mu.Unlock()
	process.finish(125)
	if !startReturned {
		select {
		case <-startDone:
		case <-time.After(time.Second):
			t.Fatal("native exit did not release Start")
		}
	}
	if !shutdownReturned {
		select {
		case <-shutdownDone:
		case <-time.After(time.Second):
			t.Fatal("native exit did not release Shutdown")
		}
	}
	stopCtx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	if err := manager.Shutdown(stopCtx); err != nil {
		t.Fatal("reaped cleanup did not drain", err)
	}
	job, err := state.GetCommandRuntimeJob(t.Context(), jobID)
	if err != nil || job.State != CommandRuntimeJobFailed || !job.TreeReaped || manager.OwnsActiveJob(job) {
		t.Fatalf("reaped handoff did not save one failed receipt and release ownership: %+v %v", job, err)
	}
}

func TestCommandRuntimeFailedHandoffShutdownRetriesFailedKill(t *testing.T) {
	starter := &commandRuntimeFakeStarter{}
	failure := errors.New("injected running handoff failure")
	state := &commandRuntimeFailedHandoffStore{newCommandRuntimeMemoryStore(), starter, make(chan struct{}), failure, 1}
	manager, err := NewCommandRuntimeManager(state, starter, "failed-handoff-retry-owner")
	if err != nil {
		t.Fatal(err)
	}
	request := commandRuntimeTestRequest(manager, 60_000)
	type startResult struct {
		job CommandRuntimeJobSnapshot
		err error
	}
	started := make(chan startResult, 1)
	go func() { job, _, err := manager.Start(t.Context(), request); started <- startResult{job, err} }()
	select {
	case <-state.entered:
	case <-time.After(time.Second):
		t.Fatal("handoff not reached")
	}
	defer starter.last().finish(125)
	var result startResult
	select {
	case result = <-started:
	case <-time.After(200 * time.Millisecond):
		t.Fatal("failed Kill blocked Start")
	}
	job, err := result.job, result.err
	if !errors.Is(err, failure) || job.State != CommandRuntimeJobStopping {
		t.Fatalf("initial failed Kill: %+v %v", job, err)
	}
	stopCtx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	if err := manager.Shutdown(stopCtx); err != nil {
		starter.last().finish(125)
		t.Fatal("shutdown did not retry failed Kill", err)
	}
	stored, err := state.GetCommandRuntimeJob(t.Context(), job.ID)
	if err != nil || stored.State != CommandRuntimeJobFailed || !stored.TreeReaped {
		t.Fatalf("retry lost failed handoff receipt: %+v %v", stored, err)
	}
}

func TestCommandRuntimeFailedHandoffDeadlineRetriesFailedKill(t *testing.T) {
	starter := &commandRuntimeFakeStarter{}
	failure := errors.New("injected running handoff failure")
	state := &commandRuntimeFailedHandoffStore{newCommandRuntimeMemoryStore(), starter, make(chan struct{}), failure, 1}
	manager, err := NewCommandRuntimeManager(state, starter, "failed-handoff-deadline-owner")
	if err != nil {
		t.Fatal(err)
	}
	request := commandRuntimeTestRequest(manager, 100)
	job, _, err := manager.Start(t.Context(), request)
	if !errors.Is(err, failure) || job.State != CommandRuntimeJobStopping {
		t.Fatalf("initial failed Kill: %+v %v", job, err)
	}
	t.Cleanup(func() {
		starter.last().finish(125)
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		if err := manager.Shutdown(ctx); err != nil {
			t.Error(err)
		}
	})
	// No Stop or Shutdown assists cleanup: only the original command deadline
	// may retry the transient Kill failure and release this owned process.
	terminal, _ := waitCommandRuntimeTerminal(t, manager, job.ID)
	stored, err := state.GetCommandRuntimeJob(t.Context(), job.ID)
	if err != nil || terminal.State != CommandRuntimeJobFailed || stored.State != CommandRuntimeJobFailed || !stored.TreeReaped {
		t.Fatalf("deadline retry lost failed handoff ownership or its final reason: terminal=%+v stored=%+v err=%v", terminal, stored, err)
	}
}
