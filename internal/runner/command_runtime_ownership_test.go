package runner

import (
	"context"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"cyberagent-workbench/internal/apperror"
)

func TestCommandRuntimeOwnershipFailureInterruptsProcess(t *testing.T) {
	for _, tc := range []struct {
		phase   string
		discard bool
	}{{"authority", false}, {"owner_renew", false}, {"authority", true}, {"owner_renew", true}} {
		name := tc.phase
		if tc.discard {
			name += "/digest_only"
		}
		t.Run(name, func(t *testing.T) {
			state := newCommandRuntimeMemoryStore()
			starter := &commandRuntimeFakeStarter{}
			manager, err := NewCommandRuntimeManager(state, starter, "ownership-failure-fixture")
			if err != nil {
				t.Fatal(err)
			}
			manager.ownerRenewEvery = 10 * time.Millisecond
			manager.ownerRenewTimeout = 100 * time.Millisecond
			var revoked atomic.Bool
			request := commandRuntimeTestRequest(manager, 2000)
			request.DiscardOutput = tc.discard
			request.DispatchCheck = func(context.Context, CommandRuntimeResolvedSpec) error {
				if revoked.Load() {
					return apperror.Wrap(apperror.CodePolicyDenied, "authorization revoked", errors.New("token=secret-value-1234567890"))
				}
				return nil
			}
			request.OwnershipCheck = func(ctx context.Context) error { return request.DispatchCheck(ctx, request.Spec) }
			job, _, err := manager.Start(t.Context(), request)
			if err != nil {
				t.Fatal(err)
			}
			if tc.phase == "authority" {
				revoked.Store(true)
			} else {
				state.mu.Lock()
				state.failActive = true
				state.mu.Unlock()
			}
			terminal, page := waitCommandRuntimeTerminal(t, manager, job.ID)
			if terminal.State != CommandRuntimeJobInterrupted || terminal.ExitCode == nil || *terminal.ExitCode != 125 {
				t.Fatalf("did not stop revoked process: %+v", terminal)
			}
			stored, err := state.GetCommandRuntimeJob(t.Context(), job.ID)
			if err != nil {
				t.Fatal(err)
			}
			if tc.discard {
				if stored.Stderr != "" || len(page.Frames) != 0 || stored.OutputFramesJSON != "[]" {
					t.Fatalf("digest-only interruption retained diagnostic: %+v", stored)
				}
				return
			}
			want := "command runtime owner renewal failed: injected owner-renewal persistence failure"
			if tc.phase == "authority" {
				want = "command runtime authority recheck failed: authorization revoked"
				if !strings.Contains(stored.Stderr, "[REDACTED:secret]") {
					t.Fatalf("authority diagnostic was not redacted: %q", stored.Stderr)
				}
			}
			if !strings.Contains(stored.Stderr, want) || strings.Contains(stored.Stderr, "secret-value") ||
				stored.StderrSHA256 != commandRuntimeStringSHA256(stored.Stderr) || stored.StderrObservedBytes < int64(len(stored.Stderr)) {
				t.Fatalf("interruption diagnostic missing from durable output: %q", stored.Stderr)
			}
		})
	}
}

func TestCommandRuntimeOwnershipDiagnosticSurvivesConcurrentNaturalExit(t *testing.T) {
	state := &commandRuntimeTerminalGateStore{
		commandRuntimeMemoryStore: newCommandRuntimeMemoryStore(),
		terminalStarted:           make(chan struct{}, 1),
		terminalRelease:           make(chan struct{}),
	}
	starter := &commandRuntimeFakeStarter{}
	manager, err := NewCommandRuntimeManager(state, starter, "ownership-exit-fixture")
	if err != nil {
		t.Fatal(err)
	}
	manager.ownerRenewEvery = time.Millisecond
	diagnostic := &commandRuntimeGatedError{entered: make(chan struct{}), release: make(chan struct{})}
	releaseDiagnostic := sync.OnceFunc(func() { close(diagnostic.release) })
	releaseTerminal := sync.OnceFunc(func() { close(state.terminalRelease) })
	t.Cleanup(func() {
		releaseDiagnostic()
		releaseTerminal()
		_ = manager.Shutdown(context.Background())
	})
	request := commandRuntimeTestRequest(manager, 2000)
	request.OwnershipCheck = func(context.Context) error { return diagnostic }
	job, _, err := manager.Start(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-diagnostic.entered:
	case <-time.After(time.Second):
		t.Fatal("ownership failure did not reach diagnostic formatting")
	}
	entry := manager.entry(job.ID)
	// setDesired has signalled already. The next notification is the diagnostic.
	select {
	case <-entry.notify:
	default:
	}
	starter.last().finish(0)
	<-entry.stdoutDone
	<-entry.stderrDone
	// Let wait reach terminal persistence while diagnostic formatting is paused.
	// The fence must hold it until the diagnostic has joined the output snapshot.
	select {
	case <-state.terminalStarted:
	case <-time.After(100 * time.Millisecond):
	}
	releaseDiagnostic()
	select {
	case <-entry.notify:
	case <-time.After(time.Second):
		t.Fatal("ownership diagnostic did not reach live output")
	}
	_, live, err := manager.Wait(t.Context(), job.ID, 0, 0, MaxCommandRuntimeOutputRead)
	if err != nil {
		t.Fatal(err)
	}
	releaseTerminal()
	select {
	case <-entry.done:
	case <-time.After(time.Second):
		t.Fatal("natural exit did not finish after diagnostic persistence")
	}
	stored, err := state.GetCommandRuntimeJob(t.Context(), job.ID)
	if err != nil {
		t.Fatal(err)
	}
	reader, err := NewCommandRuntimeManager(state, starter, "ownership-exit-reader")
	if err != nil {
		t.Fatal(err)
	}
	terminal, page, err := reader.Wait(t.Context(), job.ID, 0, 0, MaxCommandRuntimeOutputRead)
	if err != nil {
		t.Fatal(err)
	}
	var stderr strings.Builder
	for _, frame := range page.Frames {
		if frame.Stream != CommandRuntimeStderr {
			t.Fatalf("unexpected diagnostic stream: %+v", frame)
		}
		stderr.WriteString(frame.Text)
	}
	if terminal.State != CommandRuntimeJobInterrupted || terminal.ExitCode == nil || *terminal.ExitCode != 0 ||
		!strings.Contains(stored.Stderr, "command runtime authority recheck failed: ownership revoked") ||
		stored.Stderr != stderr.String() || stored.StderrObservedBytes != int64(len(stored.Stderr)) ||
		stored.StderrSHA256 != commandRuntimeStringSHA256(stderr.String()) ||
		stored.OutputCursor != page.EndCursor || page.EndCursor != live.EndCursor || len(page.Frames) != len(live.Frames) {
		t.Fatalf("natural exit lost live diagnostic: state=%s stderr=%q observed=%d hash=%s cursor=%d live=%+v final=%+v",
			stored.State, stored.Stderr, stored.StderrObservedBytes, stored.StderrSHA256, stored.OutputCursor, live, page)
	}
}

type commandRuntimeGatedError struct {
	entered chan struct{}
	release chan struct{}
}

func (e *commandRuntimeGatedError) Error() string {
	close(e.entered)
	<-e.release
	return "ownership revoked"
}

type commandRuntimeTerminalGateStore struct {
	*commandRuntimeMemoryStore
	terminalStarted chan struct{}
	terminalRelease chan struct{}
}

func (s *commandRuntimeTerminalGateStore) UpdateCommandRuntimeJob(ctx context.Context,
	job CommandRuntimeJob, expectedVersion int64,
) (CommandRuntimeJob, error) {
	if job.State.Terminal() {
		s.terminalStarted <- struct{}{}
		<-s.terminalRelease
	}
	return s.commandRuntimeMemoryStore.UpdateCommandRuntimeJob(ctx, job, expectedVersion)
}
