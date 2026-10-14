package runner

import (
	"context"
	"errors"
	"io"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"cyberagent-workbench/internal/commandruntimeadapter"
	"cyberagent-workbench/internal/domain"
)

func TestSandboxCleanupUnknownKeepsDurableJobStopping(t *testing.T) {
	for _, cleanupErr := range []error{nil, errors.New("owned VM removal was not confirmed")} {
		name := "missing-proof"
		if cleanupErr != nil {
			name = "cleanup-error"
		}
		t.Run(name, func(t *testing.T) {
			store, manager, executor, request := newSandboxCleanupFixture(t,
				CommandRuntimeSandboxResult{ExitCode: 0, TreeReaped: false}, cleanupErr)
			started, replayed, err := manager.Start(t.Context(), request)
			if err != nil || replayed {
				t.Fatalf("start: %+v replayed=%t err=%v", started, replayed, err)
			}
			entry := manager.entry(started.ID)
			if entry == nil {
				t.Fatal("started Job has no live owner")
			}
			executor.finish()
			awaitSandboxCleanupEntry(t, entry)

			stopping, page, err := manager.Wait(t.Context(), started.ID, 0, 0, MaxCommandRuntimeOutputRead)
			if !errors.Is(err, ErrCommandRuntimeUncertain) || stopping.State != CommandRuntimeJobStopping ||
				stopping.TreeReaped || stopping.ExitCode != nil || stopping.CompletedAt != nil ||
				page.State != CommandRuntimeJobStopping || page.ExitCode != nil {
				t.Fatalf("cleanup uncertainty became completion: job=%+v page=%+v err=%v", stopping, page, err)
			}
			persisted, err := store.GetCommandRuntimeJob(t.Context(), started.ID)
			if err != nil || persisted.Validate() != nil || persisted.State != CommandRuntimeJobStopping ||
				persisted.TreeReaped || persisted.ExitCode != nil || persisted.CompletedAt != nil {
				t.Fatalf("durable Job lost unresolved cleanup: %+v err=%v", persisted, err)
			}
			// A renewal already queued behind wait's persistence lock must not
			// write again after execution has finished, even while cleanup remains
			// unresolved and the ledger intentionally retains a stopping Job.
			if err := manager.renewOwnership(t.Context(), entry); err != nil {
				t.Fatalf("finished execution renewal: %v", err)
			}
			afterRenewal, err := store.GetCommandRuntimeJob(t.Context(), started.ID)
			if err != nil || afterRenewal.Version != persisted.Version ||
				!afterRenewal.OwnerExpiresAt.Equal(persisted.OwnerExpiresAt) ||
				!afterRenewal.OwnerRenewedAt.Equal(persisted.OwnerRenewedAt) {
				t.Fatalf("finished execution renewed its unresolved owner: before=%+v after=%+v err=%v", persisted, afterRenewal, err)
			}
			active, err := store.ListCommandRuntimeJobs(t.Context(), CommandRuntimeListFilter{ActiveOnly: true})
			if err != nil || len(active) != 1 || active[0].ID != started.ID || manager.entry(started.ID) != entry {
				t.Fatalf("unresolved Job lost active ownership: active=%+v err=%v", active, err)
			}
			_, replayed, err = manager.Start(t.Context(), request)
			if !replayed || !errors.Is(err, ErrCommandRuntimeUncertain) || executor.calls.Load() != 1 {
				t.Fatalf("uncertain replay was hidden or relaunched: replayed=%t calls=%d err=%v", replayed, executor.calls.Load(), err)
			}
			shutdown, cancel := context.WithTimeout(t.Context(), time.Second)
			defer cancel()
			if err := manager.Shutdown(shutdown); !errors.Is(err, ErrCommandRuntimeUncertain) {
				t.Fatalf("shutdown hid unconfirmed removal: %v", err)
			}
			persisted, err = store.GetCommandRuntimeJob(t.Context(), started.ID)
			if err != nil || persisted.State != CommandRuntimeJobStopping || persisted.TreeReaped || persisted.CompletedAt != nil {
				t.Fatalf("shutdown fabricated terminal evidence: %+v err=%v", persisted, err)
			}

			restarted := executor.identity
			restarted.Generation = strings.Repeat("e", 64)
			if count, err := ReconcileSandboxCommandRuntimeStartup(t.Context(), store, restarted); err != nil || count != 0 {
				t.Fatalf("recovery adopted a still-live Job owner: count=%d err=%v", count, err)
			}
			// This fixture advances past the old owner lease deterministically.
			// Backend removal itself is verified by the composition root before
			// invoking the recovery-only ledger API; this test does not attest a VM.
			store.mu.Lock()
			expired := store.jobs[started.ID]
			past := time.Now().UTC().Add(-time.Minute)
			expired.CreatedAt = past.Add(-time.Minute)
			expired.StartedAt = &expired.CreatedAt
			expired.OwnerRenewedAt = expired.CreatedAt
			expired.OwnerExpiresAt = past
			expired.UpdatedAt = past
			store.jobs[started.ID] = expired
			store.mu.Unlock()
			if count, err := ReconcileSandboxCommandRuntimeStartup(t.Context(), store, restarted); err != nil || count != 1 {
				t.Fatalf("confirmed cleanup did not settle expired Job: count=%d err=%v", count, err)
			}
			recovered, err := store.GetCommandRuntimeJob(t.Context(), started.ID)
			if err != nil || recovered.Validate() != nil || recovered.State != CommandRuntimeJobInterrupted ||
				!recovered.TreeReaped || recovered.CompletedAt == nil || recovered.ExitCode == nil ||
				*recovered.ExitCode != 125 || recovered.Adapter != executor.identity || executor.calls.Load() != 1 ||
				recovered.RequestFingerprint != persisted.RequestFingerprint || recovered.SpecFingerprint != persisted.SpecFingerprint {
				t.Fatalf("recovery lost historical identity or replayed execution: %+v calls=%d err=%v", recovered, executor.calls.Load(), err)
			}
			if count, err := ReconcileSandboxCommandRuntimeStartup(t.Context(), store, restarted); err != nil || count != 0 {
				t.Fatalf("settled recovery was not idempotent: count=%d err=%v", count, err)
			}
		})
	}
}

func TestSandboxConfirmedCleanupKeepsNormalJobTerminals(t *testing.T) {
	for _, test := range []struct {
		name     string
		exitCode int
		err      error
		shutdown bool
		want     CommandRuntimeJobState
	}{
		{name: "completed", want: CommandRuntimeJobCompleted},
		{name: "guest-failed", exitCode: 1, err: errors.New("guest command failed"), want: CommandRuntimeJobFailed},
		{name: "shutdown-interrupted", shutdown: true, want: CommandRuntimeJobInterrupted},
	} {
		t.Run(test.name, func(t *testing.T) {
			store, manager, executor, request := newSandboxCleanupFixture(t,
				CommandRuntimeSandboxResult{ExitCode: test.exitCode, Stdout: []byte("bounded guest output\n"), TreeReaped: true}, test.err)
			started, _, err := manager.Start(t.Context(), request)
			if err != nil {
				t.Fatal(err)
			}
			if test.shutdown {
				shutdown, cancel := context.WithTimeout(t.Context(), time.Second)
				defer cancel()
				if err := manager.Shutdown(shutdown); err != nil {
					t.Fatalf("confirmed shutdown: %v", err)
				}
			} else {
				executor.finish()
			}
			terminal, _ := waitCommandRuntimeTerminal(t, manager, started.ID)
			persisted, err := store.GetCommandRuntimeJob(t.Context(), started.ID)
			if err != nil || persisted.Validate() != nil || terminal.State != test.want || persisted.State != test.want ||
				!terminal.TreeReaped || terminal.CompletedAt == nil || terminal.ExitCode == nil || executor.calls.Load() != 1 {
				t.Fatalf("confirmed cleanup changed normal terminal: snapshot=%+v persisted=%+v calls=%d err=%v", terminal, persisted, executor.calls.Load(), err)
			}
			if test.err != nil && persisted.Stderr != test.err.Error() {
				t.Fatalf("sandbox failure diagnostic was lost: %q", persisted.Stderr)
			}
		})
	}
}

func TestSandboxShutdownRetryDrainsPendingCleanupBeforeReturning(t *testing.T) {
	store, manager, executor, request := newSandboxCleanupFixture(t,
		CommandRuntimeSandboxResult{ExitCode: 125, TreeReaped: true}, nil)
	executor.waitAfterCancel = true
	started, _, err := manager.Start(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-executor.started:
	case <-time.After(time.Second):
		t.Fatal("sandbox execution did not enter its owned cleanup lifecycle")
	}
	entry := manager.entry(started.ID)
	first, cancelFirst := context.WithTimeout(t.Context(), 10*time.Millisecond)
	defer cancelFirst()
	if err := manager.Shutdown(first); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("first shutdown did not report pending cleanup: %v", err)
	}
	select {
	case <-entry.done:
		t.Fatal("pending independent cleanup became a completed Job")
	default:
	}

	second, cancelSecond := context.WithTimeout(t.Context(), time.Second)
	defer cancelSecond()
	observed := &sandboxShutdownObservedContext{Context: second, checked: make(chan struct{})}
	returned := make(chan error, 1)
	go func() { returned <- manager.Shutdown(observed) }()
	// Observing Done proves the repeated Shutdown actually reached its pending
	// entry wait. This catches an early closed-manager return without a sleep.
	select {
	case err := <-returned:
		t.Fatalf("repeated shutdown returned before pending cleanup: %v", err)
	case <-observed.checked:
	case <-second.Done():
		t.Fatal("repeated shutdown did not reach the cleanup drain")
	}
	executor.finish()
	select {
	case err := <-returned:
		if err != nil {
			t.Fatalf("repeated shutdown lost confirmed cleanup: %v", err)
		}
	case <-second.Done():
		t.Fatal("repeated shutdown did not wait for confirmed cleanup")
	}
	select {
	case <-entry.done:
	default:
		t.Fatal("repeated shutdown returned before durable/output completion")
	}
	terminal, _, err := manager.Wait(t.Context(), started.ID, 0, 0, MaxCommandRuntimeOutputRead)
	persisted, storedErr := store.GetCommandRuntimeJob(t.Context(), started.ID)
	if err != nil || storedErr != nil || persisted.Validate() != nil ||
		terminal.State != CommandRuntimeJobInterrupted || persisted.State != CommandRuntimeJobInterrupted ||
		!terminal.TreeReaped || terminal.ExitCode == nil || terminal.CompletedAt == nil || executor.calls.Load() != 1 {
		t.Fatalf("retry drain did not preserve the interrupted terminal: job=%+v stored=%+v calls=%d err=%v storedErr=%v", terminal, persisted, executor.calls.Load(), err, storedErr)
	}
}

type sandboxShutdownObservedContext struct {
	context.Context
	checked chan struct{}
	once    sync.Once
}

func (c *sandboxShutdownObservedContext) Done() <-chan struct{} {
	c.once.Do(func() { close(c.checked) })
	return c.Context.Done()
}

type sandboxCleanupExecutor struct {
	identity        commandruntimeadapter.Identity
	result          CommandRuntimeSandboxResult
	err             error
	release         chan struct{}
	started         chan struct{}
	waitAfterCancel bool
	once            sync.Once
	calls           atomic.Int64
}

func (e *sandboxCleanupExecutor) Identity() commandruntimeadapter.Identity { return e.identity }
func (*sandboxCleanupExecutor) Available() bool                            { return true }
func (e *sandboxCleanupExecutor) finish()                                  { e.once.Do(func() { close(e.release) }) }

func (e *sandboxCleanupExecutor) ExecuteSandboxCommand(ctx context.Context, _ CommandRuntimeScope,
	_ CommandRuntimeResolvedSpec, _ io.ReadCloser,
) (CommandRuntimeSandboxResult, error) {
	e.calls.Add(1)
	close(e.started)
	select {
	case <-e.release:
		return e.result, e.err
	case <-ctx.Done():
		if e.waitAfterCancel {
			<-e.release
		}
		result := e.result
		result.ExitCode = 125
		return result, ctx.Err()
	}
}

func newSandboxCleanupFixture(t *testing.T, result CommandRuntimeSandboxResult, executeErr error) (
	*commandRuntimeMemoryStore, *CommandRuntimeManager, *sandboxCleanupExecutor, CommandRuntimeStartRequest,
) {
	t.Helper()
	store := newCommandRuntimeMemoryStore()
	executor := &sandboxCleanupExecutor{
		identity: commandruntimeadapter.SandboxedWorkspace("docker_sandboxes", "sbx-cleanup-fixture-policy", strings.Repeat("d", 64)),
		result:   result, err: executeErr, release: make(chan struct{}), started: make(chan struct{}),
	}
	manager, err := NewSandboxCommandRuntimeManager(store, executor, "sbx-cleanup-owner")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		executor.finish()
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = manager.Shutdown(ctx)
	})
	request := commandRuntimeTestRequest(manager, 60_000)
	request.Scope.PermissionMode = domain.RunExecutionPermissionAsk
	request.Spec.Spec.StdinPolicy, request.Spec.Spec.CloseInitialStdin = CommandRuntimeStdinClosed, true
	request.Spec.ExecutableIdentityKind = CommandRuntimeExecutableTemplatePathSHA256
	return store, manager, executor, request
}

func awaitSandboxCleanupEntry(t *testing.T, entry *commandRuntimeEntry) {
	t.Helper()
	select {
	case <-entry.done:
	case <-time.After(2 * time.Second):
		t.Fatal("returned sandbox execution did not persist its cleanup outcome")
	}
}
