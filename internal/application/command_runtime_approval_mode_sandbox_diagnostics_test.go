package application

import (
	"context"
	"encoding/json"
	"io"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/idgen"
	"cyberagent-workbench/internal/policy"
	"cyberagent-workbench/internal/runner"
	"cyberagent-workbench/internal/sandbox"
	"cyberagent-workbench/internal/store"
	"cyberagent-workbench/internal/toolgateway"
)

// B11 diagnostics retain B10's foreground assertions unchanged. The longer
// start lifetime below diagnoses the distinct background protocol; it does not
// extend the foreground limit or constitute a foreground acceptance result.
type sandboxApprovalTiming struct {
	Name         string  `json:"name"`
	AtSeconds    float64 `json:"at_seconds"`
	TotalSeconds float64 `json:"total_seconds"`
	FenceSeconds float64 `json:"fence_seconds,omitempty"`
}

type sandboxApprovalProbe struct {
	origin   time.Time
	mu       sync.Mutex
	timings  []sandboxApprovalTiming
	samples  map[string]int
	finished chan struct{}
}

func (p *sandboxApprovalProbe) record(name string, start time.Time, fence time.Duration) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.timings = append(p.timings, sandboxApprovalTiming{Name: name, AtSeconds: start.Sub(p.origin).Seconds(), TotalSeconds: time.Since(start).Seconds(), FenceSeconds: fence.Seconds()})
}

func (p *sandboxApprovalProbe) sample(stop <-chan struct{}, done chan<- struct{}) {
	defer close(done)
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	buffer := make([]byte, 2<<20)
	for {
		select {
		case <-stop:
			return
		case <-ticker.C:
			n := runtime.Stack(buffer, true)
			for _, stack := range strings.Split(string(buffer[:n]), "\n\n") {
				if !strings.Contains(stack, "(*DockerSandboxCommandRuntimeExecutor).ExecuteSandboxCommand") {
					continue
				}
				var frames []string
				for _, line := range strings.Split(stack, "\n") {
					if strings.HasPrefix(line, "cyberagent-workbench/internal/repository.") || strings.HasPrefix(line, "cyberagent-workbench/internal/application.") {
						if index := strings.LastIndex(line, "("); index >= 0 {
							line = line[:index]
						}
						frames = append(frames, line)
						if len(frames) == 4 {
							break
						}
					}
				}
				p.mu.Lock()
				p.samples[strings.Join(frames, " -> ")]++
				p.mu.Unlock()
			}
		}
	}
}

type sandboxApprovalObservedExecutor struct {
	runner.CommandRuntimeSandboxExecutor
	probe *sandboxApprovalProbe
}

// Preserve the production adapter's guest namespace and image/path pins while
// observing timing. The wrapper must not fall back to native host resolution.
func (e *sandboxApprovalObservedExecutor) NormalizeCommandRuntimeSpec(spec runner.CommandRuntimeSpec, root string) (runner.CommandRuntimeResolvedSpec, error) {
	return e.CommandRuntimeSandboxExecutor.(interface {
		NormalizeCommandRuntimeSpec(runner.CommandRuntimeSpec, string) (runner.CommandRuntimeResolvedSpec, error)
	}).NormalizeCommandRuntimeSpec(spec, root)
}

func (e *sandboxApprovalObservedExecutor) Ready(ctx context.Context, runID string) (bool, error) {
	start := time.Now()
	defer e.probe.record("adapter_ready", start, 0)
	return e.CommandRuntimeSandboxExecutor.(commandRuntimeSandboxReadiness).Ready(ctx, runID)
}

func (e *sandboxApprovalObservedExecutor) OwnsWorkspaceCheckpoint() bool {
	return e.CommandRuntimeSandboxExecutor.(commandRuntimeSandboxCheckpointOwner).OwnsWorkspaceCheckpoint()
}

func (e *sandboxApprovalObservedExecutor) CommandRuntimeAdmissionRequired() bool {
	adapter, ok := e.CommandRuntimeSandboxExecutor.(interface{ CommandRuntimeAdmissionRequired() bool })
	return ok && adapter.CommandRuntimeAdmissionRequired()
}

func (e *sandboxApprovalObservedExecutor) ExecuteSandboxCommand(ctx context.Context, scope runner.CommandRuntimeScope, spec runner.CommandRuntimeResolvedSpec, stdin io.ReadCloser) (runner.CommandRuntimeSandboxResult, error) {
	start := time.Now()
	stop, sampled := make(chan struct{}), make(chan struct{})
	go e.probe.sample(stop, sampled)
	defer func() {
		close(stop)
		<-sampled
		e.probe.record("native_executor", start, 0)
		close(e.probe.finished)
	}()
	return e.CommandRuntimeSandboxExecutor.ExecuteSandboxCommand(ctx, scope, spec, stdin)
}

type sandboxApprovalObservedLifecycle struct {
	*dockerLifecycleSupervisorTestTransport
	probe *sandboxApprovalProbe
}

func sandboxApprovalTimedFence(fence sandbox.DockerContainerLifecycleFence, elapsed *time.Duration) sandbox.DockerContainerLifecycleFence {
	return func(ctx context.Context, action sandbox.DockerContainerLifecycleActionKind) error {
		start := time.Now()
		defer func() { *elapsed += time.Since(start) }()
		return fence(ctx, action)
	}
}

func (x *sandboxApprovalObservedLifecycle) StageOwned(ctx context.Context, request sandbox.DockerContainerWriteRequest, ownership sandbox.DockerContainerLifecycleOwnership, fence sandbox.DockerContainerLifecycleFence) (sandbox.DockerContainerStageResult, error) {
	start, elapsed := time.Now(), time.Duration(0)
	defer func() { x.probe.record("double_stage", start, elapsed) }()
	return x.dockerLifecycleSupervisorTestTransport.StageOwned(ctx, request, ownership, sandboxApprovalTimedFence(fence, &elapsed))
}

func (x *sandboxApprovalObservedLifecycle) Start(ctx context.Context, request sandbox.DockerContainerLifecycleRequest, fence sandbox.DockerContainerLifecycleFence) (sandbox.DockerContainerLifecycleObservation, bool, error) {
	start, elapsed := time.Now(), time.Duration(0)
	defer func() { x.probe.record("double_start", start, elapsed) }()
	return x.dockerLifecycleSupervisorTestTransport.Start(ctx, request, sandboxApprovalTimedFence(fence, &elapsed))
}

func (x *sandboxApprovalObservedLifecycle) Wait(ctx context.Context, request sandbox.DockerContainerLifecycleRequest, fence sandbox.DockerContainerLifecycleFence) (sandbox.DockerContainerLifecycleObservation, error) {
	start, elapsed := time.Now(), time.Duration(0)
	defer func() { x.probe.record("double_wait", start, elapsed) }()
	return x.dockerLifecycleSupervisorTestTransport.Wait(ctx, request, sandboxApprovalTimedFence(fence, &elapsed))
}

func (x *sandboxApprovalObservedLifecycle) Cleanup(ctx context.Context, request sandbox.DockerContainerLifecycleRequest, fence sandbox.DockerContainerLifecycleFence) (sandbox.DockerContainerLifecycleCleanupResult, error) {
	start, elapsed := time.Now(), time.Duration(0)
	defer func() { x.probe.record("double_cleanup", start, elapsed) }()
	return x.dockerLifecycleSupervisorTestTransport.Cleanup(ctx, request, sandboxApprovalTimedFence(fence, &elapsed))
}

type sandboxApprovalObservedIO struct {
	*fakeDockerContainerIOTransport
	probe *sandboxApprovalProbe
}

func (x *sandboxApprovalObservedIO) AttachOwnedLogs(ctx context.Context, request sandbox.DockerContainerLifecycleRequest, plan sandbox.DockerLogCapturePlan) (io.ReadCloser, error) {
	start := time.Now()
	defer x.probe.record("double_attach_logs", start, 0)
	return x.fakeDockerContainerIOTransport.AttachOwnedLogs(ctx, request, plan)
}

func (x *sandboxApprovalObservedIO) ExportOwnedOutputs(ctx context.Context, request sandbox.DockerContainerLifecycleRequest, plan sandbox.DockerOutputExportPlan) (io.ReadCloser, error) {
	start := time.Now()
	defer x.probe.record("double_export", start, 0)
	return x.fakeDockerContainerIOTransport.ExportOwnedOutputs(ctx, request, plan)
}

// This barrier is after the real transaction commits. It neither edits the
// candidate's budget snapshot nor intercepts normal Supervisor accounting.
type sandboxApprovalCandidateBarrier struct {
	*store.SQLiteStore
	captured chan sandbox.ExecutionCandidate
	release  chan struct{}
}

func (s *sandboxApprovalCandidateBarrier) CreateSandboxExecutionCandidate(ctx context.Context, candidate sandbox.ExecutionCandidate, operation sandbox.CandidateOperation) (sandbox.ValidatedExecutionCandidate, bool, error) {
	value, replayed, err := s.SQLiteStore.CreateSandboxExecutionCandidate(ctx, candidate, operation)
	if err != nil || replayed {
		return value, replayed, err
	}
	s.captured <- value.Candidate
	select {
	case <-s.release:
		return value, replayed, nil
	case <-ctx.Done():
		return value, replayed, ctx.Err()
	}
}

func newSandboxApprovalObservedFixture(t *testing.T, mode domain.RunExecutionPermissionMode) (*commandApprovalSandboxFixture, *sandboxApprovalProbe, *StandardCodeDockerService) {
	t.Helper()
	f := newCommandApprovalSandboxFixture(t, mode, domain.RunExecutionProfileDocker, true)
	probe := &sandboxApprovalProbe{origin: time.Now(), samples: make(map[string]int), finished: make(chan struct{})}
	native := f.service.sandbox.(*DockerSandboxCommandRuntimeExecutor)
	standard := native.service
	standard.docker.lifecycleTransport = &sandboxApprovalObservedLifecycle{dockerLifecycleSupervisorTestTransport: standard.docker.lifecycleTransport.(*dockerLifecycleSupervisorTestTransport), probe: probe}
	standard.docker.ioService.transport = &sandboxApprovalObservedIO{fakeDockerContainerIOTransport: standard.docker.ioService.transport.(*fakeDockerContainerIOTransport), probe: probe}
	if err := f.manager.Shutdown(t.Context()); err != nil {
		t.Fatal(err)
	}
	executor := &sandboxApprovalObservedExecutor{CommandRuntimeSandboxExecutor: native, probe: probe}
	manager, err := runner.NewSandboxCommandRuntimeManager(f.st, executor, idgen.New("sandbox-observed-owner"))
	if err != nil {
		t.Fatal(err)
	}
	f.service, err = NewSandboxedCommandRuntimeService(f.st, manager, executor, f.caps, f.drydockFixture.service)
	if err != nil {
		t.Fatal(err)
	}
	f.manager = manager
	f.mux, err = NewCommandRuntimeMultiplexer(f.host, f.service)
	if err != nil {
		t.Fatal(err)
	}
	f.supervisor = NewAgentRunner(f.st, nil, policy.NewDefaultChecker()).WithExecutionPermissionCapabilities(f.caps).WithCommandRuntime(f.mux)
	return f, probe, standard
}

func sandboxApprovalObservedStart(t *testing.T, f *commandApprovalSandboxFixture) string {
	t.Helper()
	input := commandApprovalSandboxInput(t)
	input.Action, input.FailurePolicy, input.MaxBytes = toolgateway.CommandRuntimeActionStart, "", nil
	input.Commands[0].TimeoutMilliseconds = 60000
	f.record(t, input, 1)
	if waiting, err := f.resume(t); err != nil || waiting {
		t.Fatalf("normal Supervisor start failed: waiting=%t err=%v", waiting, err)
	}
	jobs, err := f.st.ListCommandRuntimeJobs(t.Context(), runner.CommandRuntimeListFilter{RunID: f.turn.Run.ID, Limit: 20})
	if err != nil || len(jobs) != 1 {
		t.Fatalf("start did not create exactly one job: %+v err=%v", jobs, err)
	}
	return jobs[0].ID
}

func sandboxApprovalObservedWait(t *testing.T, f *commandApprovalSandboxFixture, jobID string, round int) domain.SupervisorToolCall {
	t.Helper()
	cursor, maxBytes, waitMilliseconds := uint64(0), 4096, 1000
	f.record(t, toolgateway.CommandRuntimeInput{Version: toolgateway.CommandRuntimeToolProtocolVersion, Action: toolgateway.CommandRuntimeActionWait,
		JobID: jobID, Cursor: &cursor, MaxBytes: &maxBytes, WaitMilliseconds: &waitMilliseconds}, round)
	if waiting, err := f.resume(t); err != nil || waiting {
		t.Fatalf("normal Supervisor wait failed: waiting=%t err=%v", waiting, err)
	}
	call, started, err := f.st.GetSupervisorApprovalCall(t.Context(), f.call.RunID, f.call.CallID)
	if err != nil || !started || call.Status != domain.SupervisorToolCompleted {
		t.Fatalf("wait has no normal accounted receipt: %+v started=%t err=%v", call, started, err)
	}
	return call
}

func sandboxApprovalObservedTerminal(t *testing.T, f *commandApprovalSandboxFixture, probe *sandboxApprovalProbe, jobID string) runner.CommandRuntimeJob {
	t.Helper()
	select {
	case <-probe.finished:
	case <-time.After(75 * time.Second):
		t.Fatal("native diagnostic exceeded its bounded observation window")
	}
	// Observing the real terminal transaction only synchronizes this test; the
	// result is still consumed through the normal Supervisor wait below. No
	// manager.Wait, bypassed call, fabricated receipt or budget update is used.
	deadline := time.NewTimer(3 * time.Second)
	defer deadline.Stop()
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		job, err := f.st.GetCommandRuntimeJob(t.Context(), jobID)
		if err != nil {
			t.Fatal(err)
		}
		if job.State.Terminal() {
			return job
		}
		select {
		case <-deadline.C:
			t.Fatal("native completion did not become a durable terminal job")
		case <-ticker.C:
		}
	}
}

func sandboxApprovalDiagnosticReport(t *testing.T, f *commandApprovalSandboxFixture, probe *sandboxApprovalProbe, job runner.CommandRuntimeJob) {
	t.Helper()
	events, err := f.st.ListRunEvents(t.Context(), f.turn.Run.ID)
	if err != nil {
		t.Fatal(err)
	}
	var timeline []map[string]any
	for _, event := range events {
		if event.CreatedAt.Before(probe.origin) {
			continue
		}
		timeline = append(timeline, map[string]any{"type": event.Type, "at_seconds": event.CreatedAt.Sub(probe.origin).Seconds()})
	}
	usage, err := f.st.GetToolCallUsage(t.Context(), f.turn.Run.ID)
	if err != nil {
		t.Fatal(err)
	}
	probe.mu.Lock()
	report, err := json.Marshal(map[string]any{"job_state": job.State, "exit_code": job.ExitCode, "job_output_frames": job.OutputFramesJSON,
		"tool_calls_consumed": usage.Consumed, "budget_tool_calls": f.turn.Run.Budget.MaxToolCalls,
		"timings": probe.timings, "stack_samples_100ms": probe.samples, "ledger_timeline": timeline})
	probe.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("B11_DIAGNOSTIC %s", report)
}

func TestCommandRuntimeApprovalModeSandboxSupervisorWaitAfterNativeCompletion(t *testing.T) {
	for _, mode := range []domain.RunExecutionPermissionMode{domain.RunExecutionPermissionAsk, domain.RunExecutionPermissionAuto, domain.RunExecutionPermissionFull} {
		t.Run(string(mode), func(t *testing.T) {
			f, probe, _ := newSandboxApprovalObservedFixture(t, mode)
			jobID := sandboxApprovalObservedStart(t, f)
			job := sandboxApprovalObservedTerminal(t, f, probe, jobID)
			call := sandboxApprovalObservedWait(t, f, jobID, 2)
			sandboxApprovalDiagnosticReport(t, f, probe, job)
			usage, err := f.st.GetToolCallUsage(t.Context(), f.turn.Run.ID)
			ownedHash, rootErr := runner.CommandRuntimeWorkspaceRootSHA256(f.owned.Path)
			if err != nil || rootErr != nil || usage.Consumed != 2 || job.State != runner.CommandRuntimeJobCompleted ||
				job.WorkspaceRootSHA256 != ownedHash || job.PermissionMode != mode || job.RunAuthorizationFence == 0 ||
				job.PermissionRuntimeEpoch != f.caps.RuntimeAuthority.RuntimeEpoch() || !strings.Contains(call.ResultJSON, "controlled sandbox output") {
				t.Fatalf("normal post-completion Supervisor wait lost completion, accounting, root or authority: state=%s calls=%d err=%v rootErr=%v receipt=%s", job.State, usage.Consumed, err, rootErr, call.ResultJSON)
			}
			if waiting, err := f.resume(t); err != nil || waiting {
				t.Fatalf("terminal wait replay failed: waiting=%t err=%v", waiting, err)
			}
			f.assertDispatchCounts(t, 1, 1)
		})
	}
}

func TestCommandRuntimeApprovalModeSandboxConcurrentSupervisorWait(t *testing.T) {
	for _, mode := range []domain.RunExecutionPermissionMode{domain.RunExecutionPermissionAsk, domain.RunExecutionPermissionAuto, domain.RunExecutionPermissionFull} {
		t.Run(string(mode), func(t *testing.T) {
			f, probe, standard := newSandboxApprovalObservedFixture(t, mode)
			barrier := &sandboxApprovalCandidateBarrier{SQLiteStore: f.st, captured: make(chan sandbox.ExecutionCandidate, 1), release: make(chan struct{})}
			standard.manifests.store = barrier
			var releaseOnce sync.Once
			release := func() { releaseOnce.Do(func() { close(barrier.release) }) }
			t.Cleanup(release)
			jobID := sandboxApprovalObservedStart(t, f)
			var candidate sandbox.ExecutionCandidate
			select {
			case candidate = <-barrier.captured:
			case <-time.After(45 * time.Second):
				t.Fatal("real native candidate was not persisted")
			}
			sandboxApprovalObservedWait(t, f, jobID, 2)
			usage, err := f.st.GetToolCallUsage(t.Context(), f.turn.Run.ID)
			if err != nil || candidate.ToolCallsUsed != 1 || usage.Consumed != 2 || usage.Consumed >= int64(f.turn.Run.Budget.MaxToolCalls) {
				t.Fatalf("normal wait accounting precondition was not established: candidate=%d current=%d budget=%d err=%v", candidate.ToolCallsUsed, usage.Consumed, f.turn.Run.Budget.MaxToolCalls, err)
			}
			release()
			job := sandboxApprovalObservedTerminal(t, f, probe, jobID)
			call := sandboxApprovalObservedWait(t, f, jobID, 3)
			sandboxApprovalDiagnosticReport(t, f, probe, job)
			// This remains red while an ordinary accounted wait poisons the
			// admission snapshot. Do not relax the budget guard to green it.
			if job.State != runner.CommandRuntimeJobCompleted || !strings.Contains(call.ResultJSON, "controlled sandbox output") {
				t.Fatalf("normal concurrent Supervisor wait invalidated its still-budgeted job: state=%s frames=%s", job.State, job.OutputFramesJSON)
			}
			f.assertDispatchCounts(t, 1, 1)
		})
	}
}
