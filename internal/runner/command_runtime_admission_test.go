package runner

import (
	"context"
	"errors"
	"io"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"cyberagent-workbench/internal/commandruntimeadapter"
	"cyberagent-workbench/internal/domain"
)

type admissionSandboxExecutor struct {
	*commandRuntimeSandboxExecutorFake
	execute func(context.Context, CommandRuntimeResolvedSpec, io.ReadCloser) (CommandRuntimeSandboxResult, error)
}

type admissionReplayStore struct {
	*commandRuntimeMemoryStore
	replayed chan struct{}
}

func (s *admissionReplayStore) PrepareCommandRuntimeJob(ctx context.Context, job CommandRuntimeJob) (CommandRuntimeJob, bool, error) {
	stored, replayed, err := s.commandRuntimeMemoryStore.PrepareCommandRuntimeJob(ctx, job)
	if replayed {
		s.replayed <- struct{}{}
	}
	return stored, replayed, err
}

func (*admissionSandboxExecutor) CommandRuntimeAdmissionRequired() bool { return true }
func (e *admissionSandboxExecutor) ExecuteSandboxCommand(ctx context.Context, _ CommandRuntimeScope, spec CommandRuntimeResolvedSpec, stdin io.ReadCloser) (CommandRuntimeSandboxResult, error) {
	return e.execute(ctx, spec, stdin)
}

func admissionTestManager(t *testing.T, execute func(context.Context, CommandRuntimeResolvedSpec, io.ReadCloser) (CommandRuntimeSandboxResult, error)) (*CommandRuntimeManager, CommandRuntimeStartRequest) {
	t.Helper()
	e := &admissionSandboxExecutor{commandRuntimeSandboxExecutorFake: &commandRuntimeSandboxExecutorFake{
		identity: commandruntimeadapter.SandboxedWorkspace("docker_standard_code", "sandbox-admission-fixture-policy", strings.Repeat("e", 64))}, execute: execute}
	m, err := NewSandboxCommandRuntimeManager(newCommandRuntimeMemoryStore(), e, "admission-owner")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		if err := m.Shutdown(ctx); err != nil {
			t.Error(err)
		}
	})
	r := commandRuntimeTestRequest(m, 2000)
	r.Scope.PermissionMode = domain.RunExecutionPermissionWorkspaceAccess
	r.DispatchCheck = func(ctx context.Context, _ CommandRuntimeResolvedSpec) error { return ctx.Err() }
	r.OwnershipCheck = func(ctx context.Context) error { return ctx.Err() }
	return m, r
}

func TestSandboxAdmissionCancellationStopsOwnedJobAndClosesPipe(t *testing.T) {
	entered := make(chan struct{})
	m, request := admissionTestManager(t, func(ctx context.Context, _ CommandRuntimeResolvedSpec, stdin io.ReadCloser) (CommandRuntimeSandboxResult, error) {
		close(entered)
		<-ctx.Done()
		_, err := io.ReadAll(stdin)
		return CommandRuntimeSandboxResult{ExitCode: 125, TreeReaped: true}, errors.Join(ctx.Err(), err)
	})
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	result := make(chan error, 1)
	go func() { _, _, err := m.Start(ctx, request); result <- err }()
	<-entered
	cancel()
	select {
	case err := <-result:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("start ignored pre-admission cancellation: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("admission did not observe cancellation")
	}
	_, id := CommandRuntimeOperationIdentity(request.Scope.RunID, request.Scope.OperationKey)
	terminal, _ := waitCommandRuntimeTerminal(t, m, id)
	if terminal.State != CommandRuntimeJobKilled || !terminal.TreeReaped || !terminal.StdinClosed {
		t.Fatalf("unadmitted job leaked: %+v", terminal)
	}
}

func TestSandboxAdmissionJobTimeoutRunsWhileStartWaits(t *testing.T) {
	m, request := admissionTestManager(t, func(ctx context.Context, _ CommandRuntimeResolvedSpec, _ io.ReadCloser) (CommandRuntimeSandboxResult, error) {
		<-ctx.Done()
		return CommandRuntimeSandboxResult{ExitCode: 125, TreeReaped: true}, ctx.Err()
	})
	request.Spec.Spec.TimeoutMilliseconds = 50
	job, _, err := m.Start(t.Context(), request)
	if err == nil {
		t.Fatalf("start returned before any native admission: %+v", job)
	}
	terminal, _ := waitCommandRuntimeTerminal(t, m, job.ID)
	if terminal.State != CommandRuntimeJobTimedOut || !terminal.TreeReaped {
		t.Fatalf("pending admission bypassed job timeout: %+v", terminal)
	}
}

func TestSandboxAdmissionShutdownCanStopPendingStart(t *testing.T) {
	entered := make(chan struct{})
	m, request := admissionTestManager(t, func(ctx context.Context, _ CommandRuntimeResolvedSpec, _ io.ReadCloser) (CommandRuntimeSandboxResult, error) {
		close(entered)
		<-ctx.Done()
		return CommandRuntimeSandboxResult{ExitCode: 125, TreeReaped: true}, ctx.Err()
	})
	started := make(chan error, 1)
	go func() { _, _, err := m.Start(t.Context(), request); started <- err }()
	<-entered
	stopped := make(chan error, 1)
	go func() {
		ctx, cancel := context.WithTimeout(t.Context(), time.Second)
		defer cancel()
		stopped <- m.Shutdown(ctx)
	}()
	select {
	case err := <-stopped:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(1500 * time.Millisecond):
		t.Fatal("pending admission held the start lock against Shutdown")
	}
	select {
	case err := <-started:
		if err == nil {
			t.Fatal("shutdown admitted a new native dispatch")
		}
	case <-time.After(time.Second):
		t.Fatal("Shutdown did not release pending Start")
	}
}

func TestSandboxAdmissionFailureRetainsActualErrorAndTerminalReceipt(t *testing.T) {
	cause := errors.New("native admission denied")
	m, request := admissionTestManager(t, func(context.Context, CommandRuntimeResolvedSpec, io.ReadCloser) (CommandRuntimeSandboxResult, error) {
		return CommandRuntimeSandboxResult{ExitCode: 125, TreeReaped: true}, cause
	})
	job, _, err := m.Start(t.Context(), request)
	stored, readErr := m.store.GetCommandRuntimeJob(t.Context(), job.ID)
	if !errors.Is(err, cause) || readErr != nil || job.State != CommandRuntimeJobFailed || !job.TreeReaped || !strings.Contains(stored.Stderr, cause.Error()) {
		t.Fatalf("pre-admission failure lost its receipt: job=%+v err=%v", job, err)
	}
}

func TestSandboxAdmissionInitialStdinDoesNotDeadlockHandshake(t *testing.T) {
	m, request := admissionTestManager(t, func(ctx context.Context, spec CommandRuntimeResolvedSpec, stdin io.ReadCloser) (CommandRuntimeSandboxResult, error) {
		data, err := io.ReadAll(stdin)
		if err == nil {
			err = ConfirmCommandRuntimeDispatch(ctx, spec)
		}
		return CommandRuntimeSandboxResult{ExitCode: 0, Stdout: data, TreeReaped: true}, err
	})
	request.Spec.Spec.InitialStdin, request.Spec.Spec.CloseInitialStdin = "initial bytes before admission\n", true
	job, _, err := m.Start(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	terminal, page := waitCommandRuntimeTerminal(t, m, job.ID)
	var stdout strings.Builder
	for _, frame := range page.Frames {
		if frame.Stream == CommandRuntimeStdout {
			stdout.WriteString(frame.Text)
		}
	}
	if terminal.State != CommandRuntimeJobCompleted || stdout.String() != request.Spec.Spec.InitialStdin {
		t.Fatalf("initial stdin blocked admission: job=%+v stdout=%q", terminal, stdout.String())
	}
}

func TestSandboxAdmissionDoesNotSkipFinalAuthorityCheck(t *testing.T) {
	var revoked, dispatched atomic.Bool
	denied := errors.New("authority revoked before final native check")
	m, request := admissionTestManager(t, func(ctx context.Context, spec CommandRuntimeResolvedSpec, _ io.ReadCloser) (CommandRuntimeSandboxResult, error) {
		revoked.Store(true)
		if err := ConfirmCommandRuntimeDispatch(ctx, spec); err != nil {
			return CommandRuntimeSandboxResult{ExitCode: 125, TreeReaped: true}, err
		}
		dispatched.Store(true)
		return CommandRuntimeSandboxResult{ExitCode: 0, TreeReaped: true}, nil
	})
	request.DispatchCheck = func(ctx context.Context, _ CommandRuntimeResolvedSpec) error {
		if revoked.Load() {
			return denied
		}
		return ctx.Err()
	}
	job, _, err := m.Start(t.Context(), request)
	if !errors.Is(err, denied) || dispatched.Load() || job.State != CommandRuntimeJobFailed {
		t.Fatalf("handshake bypassed authority: job=%+v dispatched=%t err=%v", job, dispatched.Load(), err)
	}
}

func TestSandboxAdmissionConcurrentReplaySharesOwner(t *testing.T) {
	for _, outcome := range []string{"admit", "cancel_replay", "shutdown"} {
		t.Run(outcome, func(t *testing.T) {
			entered, release, finish := make(chan struct{}), make(chan struct{}), make(chan struct{})
			var starts atomic.Int32
			m, request := admissionTestManager(t, func(ctx context.Context, spec CommandRuntimeResolvedSpec, _ io.ReadCloser) (CommandRuntimeSandboxResult, error) {
				if starts.Add(1) == 1 {
					close(entered)
				}
				select {
				case <-release:
				case <-ctx.Done():
					return CommandRuntimeSandboxResult{ExitCode: 125, TreeReaped: true}, ctx.Err()
				}
				if err := ConfirmCommandRuntimeDispatch(ctx, spec); err != nil {
					return CommandRuntimeSandboxResult{ExitCode: 125, TreeReaped: true}, err
				}
				select {
				case <-finish:
					return CommandRuntimeSandboxResult{ExitCode: 0, TreeReaped: true}, nil
				case <-ctx.Done():
					return CommandRuntimeSandboxResult{ExitCode: 125, TreeReaped: true}, ctx.Err()
				}
			})
			store := &admissionReplayStore{commandRuntimeMemoryStore: m.store.(*commandRuntimeMemoryStore), replayed: make(chan struct{}, 1)}
			m.store = store
			type startResult struct {
				job      CommandRuntimeJobSnapshot
				replayed bool
				err      error
			}
			start := func(ctx context.Context) <-chan startResult {
				result := make(chan startResult, 1)
				go func() {
					job, replayed, err := m.Start(ctx, request)
					result <- startResult{job, replayed, err}
				}()
				return result
			}
			owner := start(t.Context())
			select {
			case <-entered:
			case <-time.After(time.Second):
				t.Fatal("owner did not reach the native admission barrier")
			}
			replayCtx, cancelReplay := context.WithCancel(t.Context())
			defer cancelReplay()
			replay := start(replayCtx)
			select {
			case <-store.replayed:
			case <-time.After(time.Second):
				t.Fatal("concurrent request did not reach the existing Job")
			}
			select {
			case result := <-owner:
				t.Fatalf("owner returned before admission: %+v", result)
			case result := <-replay:
				t.Fatalf("replay returned before the owner's admission: %+v", result)
			case <-time.After(50 * time.Millisecond):
			}
			_, jobID := CommandRuntimeOperationIdentity(request.Scope.RunID, request.Scope.OperationKey)
			if outcome == "shutdown" {
				shutdownCtx, cancel := context.WithTimeout(t.Context(), time.Second)
				defer cancel()
				if err := m.Shutdown(shutdownCtx); err != nil {
					t.Fatalf("pending replay blocked Shutdown: %v", err)
				}
				for _, pending := range []<-chan startResult{owner, replay} {
					select {
					case result := <-pending:
						if result.err == nil || result.job.ID != jobID || result.job.State != CommandRuntimeJobInterrupted {
							t.Fatalf("Shutdown lost pending admission: %+v", result)
						}
					case <-time.After(time.Second):
						t.Fatal("Shutdown did not release a pending start")
					}
				}
			} else {
				if outcome == "cancel_replay" {
					cancelReplay()
					select {
					case result := <-replay:
						if !result.replayed || !errors.Is(result.err, context.Canceled) || result.job.ID != jobID || result.job.State != CommandRuntimeJobRunning {
							t.Fatalf("replay cancellation changed the owner's Job: %+v", result)
						}
					case <-time.After(time.Second):
						t.Fatal("replay did not observe its own cancellation")
					}
				}
				close(release)
				pendingStarts := []<-chan startResult{owner}
				if outcome == "admit" {
					pendingStarts = append(pendingStarts, replay)
				}
				for index, pending := range pendingStarts {
					select {
					case result := <-pending:
						if result.err != nil || result.replayed != (index == 1) || result.job.ID != jobID || result.job.State != CommandRuntimeJobRunning {
							t.Fatalf("shared admission returned the wrong Job: %+v", result)
						}
					case <-time.After(time.Second):
						t.Fatal("shared admission did not release a pending start")
					}
				}
				close(finish)
				terminal, _ := waitCommandRuntimeTerminal(t, m, jobID)
				if terminal.State != CommandRuntimeJobCompleted || !terminal.TreeReaped {
					t.Fatalf("original Job did not complete: %+v", terminal)
				}
			}
			jobs, err := store.ListCommandRuntimeJobs(t.Context(), CommandRuntimeListFilter{RunID: request.Scope.RunID})
			if err != nil || len(jobs) != 1 || starts.Load() != 1 {
				t.Fatalf("replay duplicated execution: starts=%d jobs=%d err=%v", starts.Load(), len(jobs), err)
			}
		})
	}
}
