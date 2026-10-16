package application

import (
	"context"
	"io"
	"sync"
	"testing"
	"time"

	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/idgen"
	"cyberagent-workbench/internal/policy"
	"cyberagent-workbench/internal/runner"
	"cyberagent-workbench/internal/sandbox"
	"cyberagent-workbench/internal/toolgateway"
)

type dockerAdmissionBarrier struct {
	*DockerSandboxCommandRuntimeExecutor
	entered chan struct{}
	release chan struct{}
}

func (x *dockerAdmissionWaitBarrier) Start(ctx context.Context, request sandbox.DockerContainerLifecycleRequest,
	fence sandbox.DockerContainerLifecycleFence,
) (sandbox.DockerContainerLifecycleObservation, bool, error) {
	close(x.startEntered)
	select {
	case <-x.startRelease:
	case <-ctx.Done():
		return sandbox.DockerContainerLifecycleObservation{}, false, ctx.Err()
	}
	return x.dockerLifecycleSupervisorTestTransport.Start(ctx, request, fence)
}

func (e *dockerAdmissionBarrier) ExecuteSandboxCommand(ctx context.Context, scope runner.CommandRuntimeScope,
	spec runner.CommandRuntimeResolvedSpec, stdin io.ReadCloser,
) (runner.CommandRuntimeSandboxResult, error) {
	close(e.entered)
	select {
	case <-e.release:
	case <-ctx.Done():
		return runner.CommandRuntimeSandboxResult{ExitCode: 125, TreeReaped: true}, ctx.Err()
	}
	return e.DockerSandboxCommandRuntimeExecutor.ExecuteSandboxCommand(ctx, scope, spec, stdin)
}

type dockerAdmissionWaitBarrier struct {
	*dockerLifecycleSupervisorTestTransport
	entered      chan struct{}
	release      chan struct{}
	startEntered chan struct{}
	startRelease chan struct{}
}

func (x *dockerAdmissionWaitBarrier) Wait(ctx context.Context, request sandbox.DockerContainerLifecycleRequest,
	fence sandbox.DockerContainerLifecycleFence,
) (sandbox.DockerContainerLifecycleObservation, error) {
	close(x.entered)
	select {
	case <-x.release:
	case <-ctx.Done():
		return sandbox.DockerContainerLifecycleObservation{}, ctx.Err()
	}
	return x.dockerLifecycleSupervisorTestTransport.Wait(ctx, request, fence)
}

func TestDockerBackgroundStartWaitsForActualAdmissionBeforeNextTurn(t *testing.T) {
	f := newCommandApprovalSandboxFixture(t, domain.RunExecutionPermissionAuto, domain.RunExecutionProfileDocker, true)
	lease, found, err := f.st.GetRunExecutionLease(t.Context(), f.turn.Run.ID)
	if err != nil || !found {
		t.Fatalf("initial turn lease: found=%t err=%v", found, err)
	}
	f.turn, err = f.st.BeginSupervisorTurn(t.Context(), lease, "start and continue one background sandbox command")
	if err != nil {
		t.Fatal(err)
	}
	native := f.service.sandbox.(*DockerSandboxCommandRuntimeExecutor)
	barrier := &dockerAdmissionBarrier{DockerSandboxCommandRuntimeExecutor: native, entered: make(chan struct{}), release: make(chan struct{})}
	wait := &dockerAdmissionWaitBarrier{dockerLifecycleSupervisorTestTransport: native.service.docker.lifecycleTransport.(*dockerLifecycleSupervisorTestTransport),
		entered: make(chan struct{}), release: make(chan struct{}), startEntered: make(chan struct{}), startRelease: make(chan struct{})}
	native.service.docker.lifecycleTransport = wait
	var releaseOnce, startOnce, finishOnce sync.Once
	release := func() { releaseOnce.Do(func() { close(barrier.release) }) }
	start := func() { startOnce.Do(func() { close(wait.startRelease) }) }
	finish := func() { finishOnce.Do(func() { close(wait.release) }) }
	t.Cleanup(finish)
	t.Cleanup(start)
	t.Cleanup(release)
	if err := f.manager.Shutdown(t.Context()); err != nil {
		t.Fatal(err)
	}
	manager, err := runner.NewSandboxCommandRuntimeManager(f.st, barrier, idgen.New("docker-admission-owner"))
	if err != nil {
		t.Fatal(err)
	}
	service, err := NewSandboxedCommandRuntimeService(f.st, manager, barrier, f.caps, f.drydockFixture.service)
	if err != nil {
		t.Fatal(err)
	}
	f.manager, f.service = manager, service
	f.mux, err = NewCommandRuntimeMultiplexer(f.host, service)
	if err != nil {
		t.Fatal(err)
	}
	f.supervisor = NewAgentRunner(f.st, nil, policy.NewDefaultChecker()).WithExecutionPermissionCapabilities(f.caps).WithCommandRuntime(f.mux)
	input := commandApprovalSandboxInput(t)
	input.Action, input.FailurePolicy, input.MaxBytes = toolgateway.CommandRuntimeActionStart, "", nil
	input.Commands[0].TimeoutMilliseconds = 120000
	f.record(t, input, 1)
	result := make(chan error, 1)
	go func() { _, err := f.resume(t); result <- err }()
	select {
	case <-barrier.entered:
	case err := <-result:
		t.Fatalf("start failed before asynchronous admission: %v", err)
	case <-time.After(15 * time.Second):
		t.Fatal("asynchronous executor was not reached")
	}
	select {
	case err := <-result:
		t.Fatalf("start returned before actual native admission, allowing its parent attempt to end: %v", err)
	case <-time.After(100 * time.Millisecond):
	}
	release()
	select {
	case <-wait.startEntered:
	case err := <-result:
		t.Fatalf("start completed before the container's final start fence: %v", err)
	case <-time.After(30 * time.Second):
		t.Fatal("container did not reach its final start fence")
	}
	select {
	case err := <-result:
		t.Fatalf("start returned before the downstream final authority/lease check: %v", err)
	case <-time.After(100 * time.Millisecond):
	}
	start()
	select {
	case err := <-result:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(30 * time.Second):
		t.Fatal("start did not finish after real Docker dispatch admission")
	}
	select {
	case <-wait.entered:
	case <-time.After(15 * time.Second):
		t.Fatal("admitted command did not reach native lifecycle wait")
	}
	jobs, err := f.st.ListCommandRuntimeJobs(t.Context(), runner.CommandRuntimeListFilter{RunID: f.turn.Run.ID, Limit: 10})
	if err != nil || len(jobs) != 1 || jobs[0].State != runner.CommandRuntimeJobRunning {
		t.Fatalf("jobs=%+v err=%v", jobs, err)
	}
	oldAttempt := f.turn.Checkpoint.AttemptID
	f.nextTurnWithBackgroundJob(t, f.manager, jobs[0].ID)
	if f.turn.Checkpoint.AttemptID == oldAttempt {
		t.Fatal("fixture did not advance the model attempt")
	}
	finish()
	cursor, maximum, millis := uint64(0), 4096, 1000
	f.record(t, toolgateway.CommandRuntimeInput{Version: toolgateway.CommandRuntimeToolProtocolVersion, Action: toolgateway.CommandRuntimeActionWait,
		JobID: jobs[0].ID, Cursor: &cursor, MaxBytes: &maximum, WaitMilliseconds: &millis}, 1)
	if waiting, err := f.resume(t); err != nil || waiting {
		t.Fatalf("next-turn wait failed: waiting=%t err=%v", waiting, err)
	}
	deadline := time.NewTimer(15 * time.Second)
	defer deadline.Stop()
	for {
		job, err := f.st.GetCommandRuntimeJob(t.Context(), jobs[0].ID)
		if err != nil {
			t.Fatal(err)
		}
		if job.State.Terminal() {
			if job.State != runner.CommandRuntimeJobCompleted || job.ExitCode == nil || *job.ExitCode != 0 || !job.TreeReaped {
				t.Fatalf("admitted original job lost its authority after normal turn advance: %+v", job)
			}
			break
		}
		select {
		case <-deadline.C:
			t.Fatal("admitted command did not finish")
		case <-time.After(10 * time.Millisecond):
		}
	}
	f.assertDispatchCounts(t, 1, 1)
}
