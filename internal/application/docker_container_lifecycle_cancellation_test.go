package application

import (
	"context"
	"errors"
	"testing"
	"time"

	"cyberagent-workbench/internal/sandbox"
)

// The real transport returns the pre-start observation after the daemon accepts
// Start. These hooks reproduce cancellation before its following confirmation.
type dockerLifecycleInterruptedConfirmationTransport struct {
	*dockerLifecycleSupervisorTestTransport
	afterStart func()
	afterWait  func()
	startErr   error
	cleanupErr error
}

func (transport *dockerLifecycleInterruptedConfirmationTransport) Start(ctx context.Context,
	request sandbox.DockerContainerLifecycleRequest, fence sandbox.DockerContainerLifecycleFence,
) (sandbox.DockerContainerLifecycleObservation, bool, error) {
	observation, started, err := transport.dockerLifecycleSupervisorTestTransport.Start(ctx, request, fence)
	if err != nil || !started {
		return observation, started, err
	}
	if transport.afterStart != nil {
		transport.afterStart()
	}
	return transport.observation(request, sandbox.DockerContainerLifecycleStateCreated), true, transport.startErr
}

func (transport *dockerLifecycleInterruptedConfirmationTransport) Observe(ctx context.Context,
	request sandbox.DockerContainerLifecycleRequest,
) (sandbox.DockerContainerLifecycleObservation, error) {
	if err := ctx.Err(); err != nil {
		return sandbox.DockerContainerLifecycleObservation{}, err
	}
	return transport.dockerLifecycleSupervisorTestTransport.Observe(ctx, request)
}

func (transport *dockerLifecycleInterruptedConfirmationTransport) Wait(ctx context.Context,
	request sandbox.DockerContainerLifecycleRequest, fence sandbox.DockerContainerLifecycleFence,
) (sandbox.DockerContainerLifecycleObservation, error) {
	if err := ctx.Err(); err != nil {
		return sandbox.DockerContainerLifecycleObservation{}, err
	}
	observation, err := transport.dockerLifecycleSupervisorTestTransport.Wait(ctx, request, fence)
	if err == nil && transport.afterWait != nil {
		transport.afterWait()
	}
	return observation, err
}

func (transport *dockerLifecycleInterruptedConfirmationTransport) Cleanup(ctx context.Context,
	request sandbox.DockerContainerLifecycleRequest, fence sandbox.DockerContainerLifecycleFence,
) (sandbox.DockerContainerLifecycleCleanupResult, error) {
	if transport.cleanupErr != nil {
		return sandbox.DockerContainerLifecycleCleanupResult{}, transport.cleanupErr
	}
	return transport.dockerLifecycleSupervisorTestTransport.Cleanup(ctx, request, fence)
}

type dockerLifecycleContextCheckingStore struct {
	*dockerLifecycleSupervisorTestStore
}

func (store dockerLifecycleContextCheckingStore) AppendDockerContainerLifecycleTransition(ctx context.Context,
	transition sandbox.DockerContainerLifecycleTransition, lease sandbox.DockerContainerLifecycleLease,
) (sandbox.DockerContainerLifecycleRecord, bool, error) {
	if err := ctx.Err(); err != nil {
		return sandbox.DockerContainerLifecycleRecord{}, false, err
	}
	return store.dockerLifecycleSupervisorTestStore.AppendDockerContainerLifecycleTransition(ctx, transition, lease)
}

func TestDockerContainerLifecycleInterruptedConfirmationCleansOnlyOwnedResource(t *testing.T) {
	for _, test := range []struct {
		name         string
		startErr     error
		afterWait    bool
		exited       bool
		cleanupFails bool
		staleFence   bool
		wantOutcome  string
	}{
		{name: "cancel-after-start", wantOutcome: sandbox.DockerContainerLifecycleOutcomeCancelled},
		{name: "cancelled-start-response", startErr: context.Canceled, wantOutcome: sandbox.DockerContainerLifecycleOutcomeCancelled},
		{name: "timed-out-start-response", startErr: context.DeadlineExceeded, wantOutcome: sandbox.DockerContainerLifecycleOutcomeTimedOut},
		{name: "timed-out-start-already-exited", startErr: context.DeadlineExceeded, exited: true, wantOutcome: sandbox.DockerContainerLifecycleOutcomeTimedOut},
		{name: "cancel-after-wait", afterWait: true, wantOutcome: sandbox.DockerContainerLifecycleOutcomeCancelled},
		{name: "cleanup-unconfirmed", cleanupFails: true},
		{name: "cleanup-fence-changed", staleFence: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			plan, request := newDockerLifecycleSupervisorPlan(t, "interrupt-"+test.name)
			recorder := &dockerLifecycleTestRecorder{}
			now := time.Now().UTC()
			stored := &dockerLifecycleSupervisorTestStore{now: now, recorder: recorder}
			transport := &dockerLifecycleInterruptedConfirmationTransport{
				dockerLifecycleSupervisorTestTransport: newDockerLifecycleSupervisorTransport(t, recorder,
					sandbox.DockerContainerLifecycleStateAbsent, ""),
				startErr: test.startErr,
			}
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			interrupted := false
			interrupt := func() {
				interrupted = true
				if test.exited {
					transport.state = sandbox.DockerContainerLifecycleStateExited
				}
				if !errors.Is(test.startErr, context.DeadlineExceeded) {
					cancel()
				}
				if test.staleFence {
					stored.mu.Lock()
					stored.fenceErr = errors.New("owned lifecycle lease changed")
					stored.mu.Unlock()
				}
			}
			if test.afterWait {
				transport.afterWait = interrupt
			} else {
				transport.afterStart = interrupt
			}
			if test.cleanupFails {
				transport.cleanupErr = errors.New("owned cleanup not confirmed")
			}
			supervisor, err := NewDockerContainerLifecycleSupervisor(dockerLifecycleContextCheckingStore{stored}, transport,
				dockerLifecycleSupervisorTestAuthority{plan: plan, request: request}, "interrupted-owner", time.Minute)
			if err != nil {
				t.Fatal(err)
			}
			supervisor.now = func() time.Time { return now }
			postExitCalls := 0
			supervisor.WithDockerContainerLifecyclePostExit(DockerContainerLifecyclePostExitFunc(func(context.Context,
				DockerContainerLifecyclePostExitRequest,
			) error {
				postExitCalls++
				return nil
			}))
			authorityCalls := 0
			supervisor.WithDockerContainerLifecycleStartAuthority(DockerContainerLifecycleStartAuthorityFunc(func(context.Context,
				DockerContainerLifecycleStartAuthorityRequest,
			) error {
				authorityCalls++
				if interrupted {
					t.Error("cleanup requested fresh execution authority")
					return dockerContainerLifecycleStartAuthorityError()
				}
				return nil
			}))
			result, err := supervisor.BeginAndRun(ctx, plan, request, plan.RequestedBy, "interrupted-confirmation")
			if transport.creates != 1 || transport.starts != 1 || authorityCalls != 2 {
				t.Fatalf("cancellation changed create/start authority: creates=%d starts=%d checks=%d", transport.creates, transport.starts, authorityCalls)
			}
			if test.cleanupFails || test.staleFence {
				if err == nil || result.Receipt != nil || stored.record.Receipt != nil || transport.deletes != 0 {
					t.Fatalf("unconfirmed/fenced cleanup became terminal: receipt=%+v deletes=%d error=%v", result.Receipt, transport.deletes, err)
				}
				if test.cleanupFails && transport.terms != 1 {
					t.Fatal("interrupted confirmation never attempted owned termination")
				}
				return
			}
			wantTerms := 1
			if test.afterWait || test.exited {
				wantTerms = 0
			}
			if err != nil || result.Receipt == nil || result.Receipt.Outcome != test.wantOutcome ||
				!result.Receipt.ContainerRemovedNow || transport.terms != wantTerms || transport.deletes != 1 || postExitCalls != 1 {
				t.Fatalf("interrupted confirmation lost owned cleanup: receipt=%+v terms=%d deletes=%d error=%v", result.Receipt, transport.terms, transport.deletes, err)
			}
			assertDockerLifecycleEventOrder(t, recorder.snapshot(), "mutate:start", "transition:started", "transition:cleaning", "mutate:delete", "receipt")
		})
	}
}
