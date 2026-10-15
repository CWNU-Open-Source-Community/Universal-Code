package application

import (
	"context"
	"encoding/json"
	"errors"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"cyberagent-workbench/internal/apperror"
	"cyberagent-workbench/internal/commandruntimeadapter"
	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/idgen"
	"cyberagent-workbench/internal/policy"
	"cyberagent-workbench/internal/runner"
	"cyberagent-workbench/internal/runworktree"
	"cyberagent-workbench/internal/sandbox"
	"cyberagent-workbench/internal/store"
	"cyberagent-workbench/internal/toolgateway"
)

// These acceptance tests exercise the production Docker adapter, routing,
// operation authorizer, SQLite ledger and real Git Drydock. Only the existing
// Docker readiness/observation/lifecycle/IO transports are controlled doubles;
// these tests do not establish OS isolation or require a Docker daemon.
type commandApprovalSandboxFixture struct {
	*commandApprovalFixture
	drydockFixture drydockApplicationFixture
	owned          runworktree.Workspace
	mux            *CommandRuntimeMultiplexer
	host           *CommandRuntimeService
	hostTripwire   *commandApprovalSandboxHostTripwire
	recorder       *dockerLifecycleTestRecorder
}

// A misrouted host job must fail before any process can start. Both manager
// persistence entrypoints are trapped, so a failed assertion cannot launch a
// host process while the test still records whether dispatch was attempted.
type commandApprovalSandboxHostTripwire struct {
	*store.SQLiteStore
	attempts atomic.Int32
}

func (s *commandApprovalSandboxHostTripwire) PrepareCommandRuntimeJob(context.Context, runner.CommandRuntimeJob) (runner.CommandRuntimeJob, bool, error) {
	s.attempts.Add(1)
	return runner.CommandRuntimeJob{}, false, errors.New("test rejected an unexpected host process dispatch")
}

func (s *commandApprovalSandboxHostTripwire) PrepareCommandRuntimeJobForAgent(context.Context, runner.CommandRuntimeJob, domain.AgentAttribution) (runner.CommandRuntimeJob, bool, error) {
	s.attempts.Add(1)
	return runner.CommandRuntimeJob{}, false, errors.New("test rejected an unexpected attributed host process dispatch")
}

func newCommandApprovalSandboxFixture(t *testing.T, mode domain.RunExecutionPermissionMode, profile domain.RunExecutionProfile, owned bool, idleRoot ...bool) *commandApprovalSandboxFixture {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Fatal("real Git fixture is required; do not silently skip", err)
	}
	base := newDrydockApplicationFixture(t, "approval sandbox "+string(mode))
	f := &commandApprovalSandboxFixture{drydockFixture: base}
	if owned {
		f.owned = mustCreateDrydock(t, base)
	}
	if _, err := NewRunExecutionProfileService(base.state).Change(t.Context(), ChangeRunExecutionProfileRequest{
		RunID: base.run.ID, Profile: string(profile), OperationKey: "sandbox-approval-profile", RequestedBy: "operator",
	}); err != nil {
		t.Fatal(err)
	}
	if profile == domain.RunExecutionProfileSBX {
		if _, err := NewRunExecutionInteractionService(base.state).Change(t.Context(), ChangeRunExecutionInteractionRequest{
			RunID: base.run.ID, Mode: string(domain.RunExecutionInteractionControlled), Trust: string(domain.WorkspaceTrustTrusted),
			OperationKey: "sandbox-approval-sbx-interaction", RequestedBy: "operator", ConfirmWorkspaceTrust: true,
		}); err != nil {
			t.Fatal(err)
		}
	}
	caps := domain.ExecutionPermissionRuntimeCapabilities{
		WorkspaceSandboxEnabled: true, OperatorApprovalEnabled: true, DangerFullAccessEnabled: true,
		RuntimeAuthority: domain.NewExecutionPermissionRuntimeAuthority(),
	}
	if mode != domain.RunExecutionPermissionAsk {
		if _, err := NewRunExecutionPermissionService(base.state, caps).Change(t.Context(), ChangeRunExecutionPermissionRequest{
			RunID: base.run.ID, Mode: string(mode), ConfirmFull: mode == domain.RunExecutionPermissionFull,
			OperationKey: "sandbox-approval-mode", RequestedBy: "operator",
		}); err != nil {
			t.Fatal(err)
		}
	}
	checker := policy.NewDefaultChecker()
	imageDigest := "sha256:" + strings.Repeat("7", 64)
	endpoint, err := sandbox.NewDockerObservationEndpoint(sandbox.DockerObservationEndpointLocalUnix)
	if err != nil {
		t.Fatal(err)
	}
	readiness, err := sandbox.NewDockerReadinessProbe(&dockerSandboxReadinessTransport{endpoint: endpoint, image: imageDigest})
	if err != nil {
		t.Fatal(err)
	}
	manifests := NewSandboxManifestService(base.state, checker).WithStandardCodeDrydock(base.service).
		WithDockerContainerTransactionHarness(sandbox.NewInMemoryDockerWriteTransaction()).
		WithDockerProductionObserver(sandbox.NewReadOnlyDockerProductionObserver(applicationDockerObservationTransport{imageDigest: imageDigest}))
	f.recorder = &dockerLifecycleTestRecorder{}
	lifecycle := newDockerLifecycleSupervisorTransport(t, f.recorder, sandbox.DockerContainerLifecycleStateAbsent, "")
	ioTransport := &fakeDockerContainerIOTransport{attachBody: dockerLogFramePayload(1, "controlled sandbox output\n")}
	dockerService, err := NewDockerSandboxService(base.state, readiness, checker, sandbox.DockerRuntimeCapabilities{Enabled: true}, caps,
		WithDockerSandboxExecution(lifecycle, ioTransport, t.TempDir(), time.Minute), WithDockerStandardCode(base.service, imageDigest))
	if err != nil {
		t.Fatal(err)
	}
	standard, err := NewStandardCodeDockerService(base.state, base.service, manifests, dockerService, imageDigest)
	if err != nil {
		t.Fatal(err)
	}
	executor, err := NewDockerSandboxCommandRuntimeExecutor(standard)
	if err != nil {
		t.Fatal(err)
	}
	manager, err := runner.NewSandboxCommandRuntimeManager(base.state, executor, idgen.New("sandbox-approval-owner"))
	if err != nil {
		t.Fatal(err)
	}
	service, err := NewSandboxedCommandRuntimeService(base.state, manager, executor, caps, base.service)
	if err != nil {
		t.Fatal(err)
	}
	f.hostTripwire = &commandApprovalSandboxHostTripwire{SQLiteStore: base.state}
	hostManager, err := runner.NewPlatformCommandRuntimeManager(f.hostTripwire, idgen.New("sandbox-host-tripwire"))
	if err != nil {
		t.Fatal(err)
	}
	f.host, err = NewCommandRuntimeService(base.state, hostManager, caps)
	if err != nil {
		t.Fatal(err)
	}
	f.mux, err = NewCommandRuntimeMultiplexer(f.host, service)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := f.mux.Shutdown(ctx); err != nil {
			t.Error(err)
		}
	})
	if adapters := f.mux.InstalledCommandRuntimeAdapters(); len(adapters) != 2 {
		t.Fatalf("test requires both installed candidates, got %+v", adapters)
	}
	if _, err := NewRunService(base.state).Start(t.Context(), base.run.ID); err != nil {
		t.Fatal(err)
	}
	lease, err := base.state.AcquireRunExecutionLease(t.Context(), domain.AcquireRunExecutionLeaseRequest{
		RunID: base.run.ID, OwnerID: "sandbox-approval-worker", TTL: 3 * time.Minute,
	})
	if err != nil {
		t.Fatal(err)
	}
	var turn domain.SupervisorTurn
	if len(idleRoot) != 0 && idleRoot[0] {
		turn.Run, err = base.state.GetRun(t.Context(), base.run.ID)
		if err != nil {
			t.Fatal(err)
		}
		turn.Mission, err = base.state.GetMission(t.Context(), turn.Run.MissionID)
		if err != nil {
			t.Fatal(err)
		}
		turn.Agent, _, err = base.state.RegisterRootAgent(t.Context(), base.run.ID)
		if err != nil {
			t.Fatal(err)
		}
	} else {
		turn, err = base.state.BeginSupervisorTurn(t.Context(), lease.Lease, "")
		if err != nil {
			t.Fatal(err)
		}
	}
	f.commandApprovalFixture = &commandApprovalFixture{
		st: base.state, service: service, turn: turn, caps: caps, root: base.sourceRoot, path: base.databasePath, manager: manager,
		supervisor: NewAgentRunner(base.state, nil, checker).WithExecutionPermissionCapabilities(caps).WithCommandRuntime(f.mux),
	}
	return f
}

func commandApprovalSandboxInput(t *testing.T) toolgateway.CommandRuntimeInput {
	t.Helper()
	executable, err := exec.LookPath("go")
	if err != nil {
		t.Fatal("Go toolchain fixture is required", err)
	}
	n := 4096
	return toolgateway.CommandRuntimeInput{Version: toolgateway.CommandRuntimeToolProtocolVersion, Action: toolgateway.CommandRuntimeActionRun,
		FailurePolicy: toolgateway.CommandRuntimeFailFast, MaxBytes: &n,
		Commands: []runner.CommandRuntimeSpec{{Version: runner.CommandRuntimeProtocolVersion, Profile: runner.CommandRuntimeProcess,
			Executable: executable, Arguments: []string{"version"}, WorkingDirectory: ".", Environment: []runner.CommandRuntimeEnvironment{},
			StdinPolicy: runner.CommandRuntimeStdinClosed, CloseInitialStdin: true, TimeoutMilliseconds: 20000,
			Output:  runner.CommandRuntimeOutputPolicy{InlineBytes: 4096, ArtifactBytes: 4096},
			Network: runner.CommandRuntimeNetworkDisabled, Credentials: runner.CommandRuntimeCredentialsNone,
			Purpose: "verify owned sandbox routing with controlled Docker transports"}},
	}
}

func (f *commandApprovalSandboxFixture) assertDispatchCounts(t *testing.T, jobs, starts int) {
	t.Helper()
	stored, err := f.st.ListCommandRuntimeJobs(t.Context(), runner.CommandRuntimeListFilter{RunID: f.turn.Run.ID, Limit: 20})
	if err != nil || len(stored) != jobs {
		t.Fatalf("unexpected durable jobs: %+v err=%v; want %d", stored, err, jobs)
	}
	for _, job := range stored {
		if !job.Adapter.SameBackend(f.service.adapter) {
			t.Fatalf("job escaped selected sandbox: %+v", job)
		}
	}
	actualStarts := 0
	for _, event := range f.recorder.snapshot() {
		if event == "mutate:start" {
			actualStarts++
		}
		if starts == 0 && strings.HasPrefix(event, "mutate:") {
			t.Fatalf("rejected command reached Docker mutation: %s", event)
		}
	}
	if actualStarts != starts || f.hostTripwire.attempts.Load() != 0 {
		t.Fatalf("unexpected dispatch: sandbox starts=%d want=%d, host attempts=%d", actualStarts, starts, f.hostTripwire.attempts.Load())
	}
}

func (f *commandApprovalSandboxFixture) requireRecovery(t *testing.T) {
	t.Helper()
	before := f.owned
	after := before
	after.State, after.RecoveryReason = runworktree.StateRecoveryRequired, "approval_sandbox_test_recovery"
	after.Generation++
	after.UpdatedAt = time.Now().UTC()
	receipt := runworktree.Receipt{ID: idgen.New("approval-sandbox-recovery"), ProtocolVersion: runworktree.ReceiptProtocolVersion,
		OperationKeySHA256: runworktree.Fingerprint("approval-sandbox-recovery-operation", before.ID),
		RequestFingerprint: runworktree.Fingerprint("approval-sandbox-recovery-request", before.ID),
		DrydockID:          before.ID, RunID: f.turn.Run.ID, Operation: runworktree.OperationUse, Outcome: runworktree.OutcomeFailed,
		GenerationBefore: before.Generation, GenerationAfter: after.Generation, SourceIdentitySHA256: before.Source.Fingerprint(),
		RootFingerprint: before.RootFingerprint, BindingBeforeSHA256: before.ExpectedBindingFingerprint,
		BindingAfterSHA256: before.ExpectedBindingFingerprint, ReasonCode: after.RecoveryReason,
		Summary: "Controlled durable recovery state; no filesystem or process authority is granted", CreatedAt: after.UpdatedAt}
	stored, replayed, err := f.st.AdvanceDrydock(t.Context(), after, before.Generation, receipt)
	if err != nil || replayed || stored.State != runworktree.StateRecoveryRequired || stored.Generation != after.Generation {
		t.Fatalf("failed to establish real invalid Drydock state: %+v replayed=%t err=%v", stored, replayed, err)
	}
	f.owned = stored
}

func TestCommandRuntimeApprovalModeSandboxDispatch(t *testing.T) {
	for _, mode := range []domain.RunExecutionPermissionMode{domain.RunExecutionPermissionAsk, domain.RunExecutionPermissionAuto, domain.RunExecutionPermissionFull} {
		t.Run(string(mode), func(t *testing.T) {
			f := newCommandApprovalSandboxFixture(t, mode, domain.RunExecutionProfileDocker, true)
			selected, available, err := f.mux.AdvertisedCommandRuntimeAdapter(t.Context(), f.turn.Run.ID, mode)
			if err != nil || !available || !selected.SameBackend(f.service.adapter) {
				t.Fatalf("owned sandbox was not selected: %+v available=%t err=%v", selected, available, err)
			}
			f.record(t, commandApprovalSandboxInput(t), 1)
			authority, err := commandruntimeadapter.DecodeAuthority(json.RawMessage(f.call.AuthorityJSON))
			if err != nil || authority.PermissionMode != mode || !authority.Adapter.SameBackend(selected) || authority.ScopeFingerprint == "" || len(authority.CommandFingerprints) != 1 {
				t.Fatalf("prepared call lost adapter or input pins: %+v err=%v", authority, err)
			}
			if waiting, err := f.resume(t); err != nil || waiting {
				t.Fatalf("verified routine sandbox command did not execute: waiting=%t err=%v", waiting, err)
			}
			call, started, err := f.st.GetSupervisorApprovalCall(t.Context(), f.call.RunID, f.call.CallID)
			if err != nil || !started || call.Status != domain.SupervisorToolCompleted || !strings.Contains(call.ResultJSON, "controlled sandbox output") {
				t.Fatalf("missing successful sandbox ledger receipt: %+v started=%t err=%v", call, started, err)
			}
			jobs, err := f.st.ListCommandRuntimeJobs(t.Context(), runner.CommandRuntimeListFilter{RunID: f.turn.Run.ID, Limit: 20})
			if err != nil || len(jobs) != 1 {
				t.Fatalf("expected exactly one durable sandbox job: %+v err=%v", jobs, err)
			}
			ownedHash, err := runner.CommandRuntimeWorkspaceRootSHA256(f.owned.Path)
			if err != nil {
				t.Fatal(err)
			}
			sourceHash, err := runner.CommandRuntimeWorkspaceRootSHA256(f.root)
			if err != nil {
				t.Fatal(err)
			}
			job := jobs[0]
			if job.State != runner.CommandRuntimeJobCompleted || job.PermissionMode != mode || job.WorkspaceRootSHA256 != ownedHash || ownedHash == sourceHash ||
				job.RunAuthorizationFence == 0 || job.PermissionRuntimeEpoch != f.caps.RuntimeAuthority.RuntimeEpoch() {
				t.Fatalf("job lost owned root or runtime authority: %+v", job)
			}
			if got := readDrydockTestFile(t, filepath.Join(f.root, "tracked.txt")); got != "base\n" {
				t.Fatalf("source repository changed: %q", got)
			}
			if waiting, err := f.resume(t); err != nil || waiting {
				t.Fatalf("completed replay failed: waiting=%t err=%v", waiting, err)
			}
			f.assertDispatchCounts(t, 1, 1)
		})
	}
}

func TestCommandRuntimeApprovalModeSandboxRecoveryRejectsBeforeDispatch(t *testing.T) {
	for _, mode := range []domain.RunExecutionPermissionMode{domain.RunExecutionPermissionAsk, domain.RunExecutionPermissionAuto, domain.RunExecutionPermissionFull} {
		t.Run(string(mode), func(t *testing.T) {
			f := newCommandApprovalSandboxFixture(t, mode, domain.RunExecutionProfileDocker, true)
			f.record(t, commandApprovalSandboxInput(t), 1)
			scope, input := f.scope(t)
			f.requireRecovery(t)
			if selected, available, err := f.mux.AdvertisedCommandRuntimeAdapter(t.Context(), f.turn.Run.ID, mode); available || selected.Executable() {
				t.Fatalf("invalid Drydock advertised an executable adapter: %+v available=%t err=%v", selected, available, err)
			}
			if _, err := f.mux.ExecuteCommandRuntime(t.Context(), scope, input); apperror.CodeOf(err) != apperror.CodeConflict || !strings.Contains(err.Error(), "Drydock binding is stale") {
				t.Fatalf("stale sandbox dispatch did not reject its Drydock binding: %v", err)
			}
			// Even a caller substituting an installed host identity cannot escape
			// the owned workspace gate; refusal precedes any process or job.
			scope.Adapter, scope.CapabilityGeneration = f.host.adapter, f.host.adapter.Generation
			if _, err := f.mux.ExecuteCommandRuntime(t.Context(), scope, input); apperror.CodeOf(err) != apperror.CodeConflict || !strings.Contains(err.Error(), "owned command workspace requires its sandbox adapter") {
				t.Fatalf("host substitution bypassed the owned workspace gate: %v", err)
			}
			for attempt := 0; attempt < 2; attempt++ {
				if waiting, err := f.resume(t); err != nil || waiting {
					t.Fatalf("invalid authority did not produce a terminal refusal: waiting=%t err=%v", waiting, err)
				}
			}
			call, started, err := f.st.GetSupervisorApprovalCall(t.Context(), f.call.RunID, f.call.CallID)
			if err != nil || started || call.Status != domain.SupervisorToolFailed || call.ErrorCode != "command_authority_expired" || !strings.Contains(call.ResultJSON, `"command_preflight":"not_dispatched"`) {
				t.Fatalf("invalid Drydock lost its explicit non-dispatch receipt: %+v started=%t err=%v", call, started, err)
			}
			f.assertDispatchCounts(t, 0, 0)
		})
	}
}

func TestCommandRuntimeApprovalModeSandboxMissingLocalBackendNeverFallsBackToHost(t *testing.T) {
	for _, mode := range []domain.RunExecutionPermissionMode{domain.RunExecutionPermissionAsk, domain.RunExecutionPermissionAuto, domain.RunExecutionPermissionFull} {
		t.Run(string(mode), func(t *testing.T) {
			// The host really is enabled for this exact profile and permission.
			// A Docker-only profile would otherwise hide a broken fallback gate.
			control := newCommandApprovalSandboxFixture(t, mode, domain.RunExecutionProfileLocal, false)
			selected, available, err := control.mux.AdvertisedCommandRuntimeAdapter(t.Context(), control.turn.Run.ID, mode)
			if err != nil || !available || !selected.SameBackend(control.host.adapter) {
				t.Fatalf("positive host-advertisement control failed: %+v available=%t err=%v", selected, available, err)
			}
			control.assertDispatchCounts(t, 0, 0)
			f := newCommandApprovalSandboxFixture(t, mode, domain.RunExecutionProfileLocal, true)
			for _, state := range []string{"ready", "recovery_required"} {
				if state == "recovery_required" {
					f.requireRecovery(t)
				}
				selected, available, err = f.mux.AdvertisedCommandRuntimeAdapter(t.Context(), f.turn.Run.ID, mode)
				if err != nil || available || selected.Executable() {
					t.Fatalf("%s owned Local workspace fell back to host without a Local sandbox: %+v available=%t err=%v", state, selected, available, err)
				}
			}
			f.assertDispatchCounts(t, 0, 0)
		})
	}
}
