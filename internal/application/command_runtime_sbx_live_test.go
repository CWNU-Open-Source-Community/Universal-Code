package application

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"cyberagent-workbench/internal/commandruntimeadapter"
	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/idgen"
	"cyberagent-workbench/internal/policy"
	"cyberagent-workbench/internal/repository"
	"cyberagent-workbench/internal/runner"
	"cyberagent-workbench/internal/runworktree"
	"cyberagent-workbench/internal/sandbox"
	"cyberagent-workbench/internal/store"
	"cyberagent-workbench/internal/toolgateway"
	"cyberagent-workbench/internal/workspacecheckpoint"
)

// This test creates real microVMs and registers the production zero-tool MCP
// helper. It is deliberately absent from ordinary test runs. To opt in, set:
//
// CYBERAGENT_SBX_LIVE_ACCEPTANCE=1
// CYBERAGENT_SBX_LIVE_EXECUTABLE=<absolute sbx executable>
// CYBERAGENT_SBX_LIVE_HELPER=<absolute built cyberagent executable>
// CYBERAGENT_SBX_LIVE_TEMPLATE=<already cached immutable OCI template reference>
// CYBERAGENT_SBX_LIVE_ARTIFACT_ROOT=<existing canonical directory owned by this test>
//
// Every fixture, database, journal and summary remains beneath a fresh directory
// in the explicit artifact root. No template is pulled, model is called, or host
// credential environment is forwarded to the guest. The production backend is
// responsible for CLI environment filtering, readiness and exact VM removal.
func TestSBXProductRealDaemonAcceptanceOptIn(t *testing.T) {
	if value := os.Getenv("CYBERAGENT_SBX_LIVE_ACCEPTANCE"); value != "1" {
		if value != "" {
			t.Fatal("CYBERAGENT_SBX_LIVE_ACCEPTANCE must be exactly 1")
		}
		t.Skip("real SBX product acceptance requires explicit opt-in")
	}
	config := sbxLiveProductConfig(t)
	artifactRoot, err := os.MkdirTemp(config.artifactRoot, "sbx-product-")
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("retained real SBX product evidence: %s", artifactRoot)
	t.Setenv("CYBERAGENT_SBX_LIVE_SECRET_SENTINEL", "owned-test-sentinel-must-not-reach-guest")
	// The backend owns the fixed product namespace until Close. Do not parallelize
	// these subtests or instantiate a second backend before the first has closed.
	for _, tc := range []struct {
		name   string
		exit   int
		cancel bool
	}{
		{name: "output-success", exit: 0},
		{name: "output-exit-seven", exit: 7},
		{name: "cancel-detached-child", cancel: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), 4*time.Minute)
			defer cancel()
			root := filepath.Join(artifactRoot, tc.name)
			if err := os.Mkdir(root, 0700); err != nil {
				t.Fatal(err)
			}
			input := sbxLiveProductInput()
			if err := input.Validate(); err != nil {
				t.Fatalf("SBX acceptance command contract before backend preparation: %v", err)
			}
			fixture, observed, workspace := newSBXLiveProductFixture(t, ctx, root, config, sbxLiveProductProgram(tc.exit, tc.cancel))
			// Reuse the real supervisor prepared-call ledger and operation-authority
			// binding, without making a provider request or substituting a backend.
			fixture.record(t, input, 1)
			if _, err := fixture.st.RecordSupervisorToolExecutionStarted(ctx, fixture.turn.Checkpoint, fixture.call.CallID); err != nil {
				t.Fatal(err)
			}
			scope, preparedInput := fixture.scope(t)
			scope.InvocationID = fixture.call.CallID
			if err := scope.Validate(); err != nil {
				t.Fatalf("SBX acceptance prepared scope contract: %v", err)
			}
			previousCheckpointID := sbxLiveProductCheckpointCursor(t, ctx, fixture.st, scope.RunID)
			started, err := fixture.service.ExecuteCommandRuntime(ctx, scope, preparedInput)
			if err != nil || started.Replayed || len(started.Jobs) != 1 {
				t.Fatalf("real SBX product start: jobs=%+v replay=%t error=%v", started.Jobs, started.Replayed, err)
			}
			jobID := started.Jobs[0].ID
			if tc.cancel {
				waitSBXLiveProductFile(t, ctx, fixture.manager, jobID, filepath.Join(workspace.Path, "detached-started.txt"), 1)
				waitSBXLiveProductFile(t, ctx, fixture.manager, jobID, filepath.Join(workspace.Path, "detached-heartbeat.txt"), 2)
				writeDrydockTestFile(t, filepath.Join(workspace.Path, "cancel-requested.txt"), "owned cancellation canary\n")
				if _, err := fixture.manager.Stop(ctx, jobID, false, 0); err != nil {
					t.Fatalf("cancel exact admitted SBX Job: %v", err)
				}
			}
			terminal := waitSBXLiveProductTerminal(t, ctx, fixture.manager, jobID)
			stored, err := fixture.st.GetCommandRuntimeJob(ctx, jobID)
			if err != nil || stored.Validate() != nil || !terminal.TreeReaped || !stored.TreeReaped ||
				terminal.ExitCode == nil || !stored.Adapter.SameBackend(fixture.service.adapter) ||
				stored.PID != 0 || stored.ProcessGroup != 0 || observed.calls.Load() != 1 {
				t.Fatalf("real durable SBX terminal/ownership proof: job=%+v calls=%d error=%v", terminal, observed.calls.Load(), err)
			}
			const wantStdout, wantStderr = "real SBX stdout 中文\n", "real SBX stderr 中文\n"
			if stored.Stdout != wantStdout || stored.Stderr != wantStderr || stored.StdoutSHA256 != sbxLiveProductSHA(wantStdout) ||
				stored.StderrSHA256 != sbxLiveProductSHA(wantStderr) || stored.StdoutObservedBytes != int64(len(wantStdout)) ||
				stored.StderrObservedBytes != int64(len(wantStderr)) || stored.TruncationReason != "" {
				t.Fatalf("real stdout/stderr were not retained exactly: stdout=%q stderr=%q truncation=%q", stored.Stdout, stored.Stderr, stored.TruncationReason)
			}
			if tc.cancel {
				if terminal.State != runner.CommandRuntimeJobCancelled {
					t.Fatalf("real SBX cancellation lost terminal state: %+v", terminal)
				}
				assertSBXLiveProductDetachedStopped(t, ctx, workspace.Path)
			} else {
				wantState := runner.CommandRuntimeJobCompleted
				if tc.exit != 0 {
					wantState = runner.CommandRuntimeJobFailed
				}
				if terminal.State != wantState || *terminal.ExitCode != tc.exit {
					t.Fatalf("real guest exit lost: state=%s exit=%d want=%s/%d", terminal.State, *terminal.ExitCode, wantState, tc.exit)
				}
			}
			if readDrydockTestFile(t, filepath.Join(workspace.Path, "acceptance-output.txt")) != "real SBX generated file\n" {
				t.Fatal("real SBX did not write its granted Drydock")
			}
			for _, name := range []string{"acceptance-output.txt", "detached-started.txt", "detached-heartbeat.txt", "post-cleanup.txt"} {
				if _, err := os.Lstat(filepath.Join(fixture.root, name)); !errors.Is(err, os.ErrNotExist) {
					t.Fatalf("source workspace received a guest write: %s error=%v", name, err)
				}
			}
			if readDrydockTestFile(t, filepath.Join(workspace.Path, ".git")) != readDrydockTestFile(t, filepath.Join(root, "git-pointer-before.txt")) {
				t.Fatal("guest changed the read-only Drydock Git pointer")
			}
			journal := readSBXLiveProductJournal(t, filepath.Join(root, "journal"), workspace.Path)
			actual := observed.result()
			if !actual.TreeReaped || actual.ReceiptFingerprint != sbxLiveProductPartsSHA(journal.OperationDigest, journal.RequestFingerprint, journal.ID, "removed") ||
				journal.OperationDigest != sbxLiveProductPartsSHA(scope.RunID+"/"+scope.OperationKey) {
				t.Fatalf("production backend did not produce exact removed-VM receipt: %+v", actual)
			}
			if err := fixture.service.completeCommandRuntimeJobBoundary(ctx, stored); err != nil {
				t.Fatalf("real SBX checkpoint completion: %v", err)
			}
			afterCheckpointID := assertSBXLiveProductCheckpoint(t, ctx, fixture, scope, stored, workspace, previousCheckpointID)
			replayed, err := fixture.service.ExecuteCommandRuntime(ctx, scope, preparedInput)
			if err != nil || !replayed.Replayed || len(replayed.Jobs) != 1 || replayed.Jobs[0].ID != jobID ||
				replayed.Jobs[0].State != terminal.State || replayed.Jobs[0].ExitCode == nil ||
				*replayed.Jobs[0].ExitCode != *terminal.ExitCode || !replayed.Jobs[0].TreeReaped || observed.calls.Load() != 1 {
				t.Fatalf("exact real SBX replay changed terminal result or dispatched again: %+v calls=%d error=%v", replayed, observed.calls.Load(), err)
			}
			afterReplay := readSBXLiveProductJournal(t, filepath.Join(root, "journal"), workspace.Path)
			if journal != afterReplay {
				t.Fatal("read-only Job replay changed the owned VM journal")
			}
			jobs, err := fixture.st.ListCommandRuntimeJobs(ctx, runner.CommandRuntimeListFilter{RunID: fixture.turn.Run.ID, Limit: 10})
			if err != nil || len(jobs) != 1 {
				t.Fatalf("exact replay duplicated the durable Job: jobs=%d error=%v", len(jobs), err)
			}
			summary, err := json.MarshalIndent(struct {
				Template, BackendGeneration, VMReceiptFingerprint, CheckpointID string
				Job                                                             runner.CommandRuntimeJobSnapshot
				Journal                                                         sbxLiveProductJournal
				BackendCalls                                                    int32
				ExactReplay, DetachedChildStopped                               bool
			}{config.template, fixture.service.adapter.Generation, actual.ReceiptFingerprint, afterCheckpointID,
				terminal, journal, observed.calls.Load(), true, tc.cancel}, "", "  ")
			if err != nil {
				t.Fatal("could not encode real SBX acceptance summary", err)
			}
			if err := os.WriteFile(filepath.Join(root, "acceptance-summary.json"), append(summary, '\n'), 0600); err != nil {
				t.Fatal("could not retain real SBX acceptance summary", err)
			}
			t.Logf("actual SBX state=%s exit=%d backend_calls=%d stdout_bytes=%d stderr_bytes=%d cleanup=true replay=true checkpoint=%s", terminal.State, *terminal.ExitCode, observed.calls.Load(), len(stored.Stdout), len(stored.Stderr), afterCheckpointID)
		})
	}
}

type sbxLiveProductSettings struct{ executable, helper, template, artifactRoot string }

func sbxLiveProductInput() toolgateway.CommandRuntimeInput {
	return toolgateway.CommandRuntimeInput{Version: toolgateway.CommandRuntimeToolProtocolVersion,
		Action: toolgateway.CommandRuntimeActionStart,
		Commands: []runner.CommandRuntimeSpec{{Version: runner.CommandRuntimeProtocolVersion, Profile: runner.CommandRuntimeProcess,
			Executable: "/usr/bin/python3", Arguments: []string{"-I", "acceptance.py"}, WorkingDirectory: ".",
			Environment: []runner.CommandRuntimeEnvironment{}, StdinPolicy: runner.CommandRuntimeStdinClosed, CloseInitialStdin: true,
			TimeoutMilliseconds: 90000, Output: runner.CommandRuntimeOutputPolicy{InlineBytes: 4096, ArtifactBytes: 64 * 1024},
			Network: runner.CommandRuntimeNetworkDisabled, Credentials: runner.CommandRuntimeCredentialsNone,
			Purpose: "real SBX product output, exit, detached-child cancellation and exact replay acceptance"}},
	}
}

func sbxLiveProductConfig(t *testing.T) sbxLiveProductSettings {
	t.Helper()
	value := sbxLiveProductSettings{os.Getenv("CYBERAGENT_SBX_LIVE_EXECUTABLE"), os.Getenv("CYBERAGENT_SBX_LIVE_HELPER"),
		os.Getenv("CYBERAGENT_SBX_LIVE_TEMPLATE"), os.Getenv("CYBERAGENT_SBX_LIVE_ARTIFACT_ROOT")}
	if !sandbox.ValidSBXTemplateReference(value.template) {
		t.Fatal("explicit immutable cached CYBERAGENT_SBX_LIVE_TEMPLATE required")
	}
	for _, entry := range []struct {
		name, path string
		directory  bool
	}{{"CYBERAGENT_SBX_LIVE_EXECUTABLE", value.executable, false}, {"CYBERAGENT_SBX_LIVE_HELPER", value.helper, false}, {"CYBERAGENT_SBX_LIVE_ARTIFACT_ROOT", value.artifactRoot, true}} {
		canonical, err := filepath.EvalSymlinks(entry.path)
		info, statErr := os.Stat(entry.path)
		if !filepath.IsAbs(entry.path) || filepath.Clean(entry.path) != entry.path || canonical != entry.path || err != nil || statErr != nil ||
			info.IsDir() != entry.directory || (!entry.directory && !info.Mode().IsRegular()) {
			t.Fatalf("%s must name an existing canonical absolute %s", entry.name, map[bool]string{true: "directory owned by this test", false: "executable file"}[entry.directory])
		}
	}
	return value
}

// Observation only: every lifecycle and guest I/O effect uses SBXBackend's
// production transport and its production authority checks without alteration.
type sbxLiveProductBackend struct {
	SBXCommandRuntimeBackend
	calls          atomic.Int32
	mu             sync.Mutex
	last           sandbox.SBXExecutionResult
	readiness      sandbox.SBXReadiness
	readinessErr   error
	readinessCalls int
}

func (b *sbxLiveProductBackend) Readiness(ctx context.Context) (sandbox.SBXReadiness, error) {
	value, err := b.SBXCommandRuntimeBackend.Readiness(ctx)
	b.mu.Lock()
	b.readiness, b.readinessErr = value, err
	b.readinessCalls++
	b.mu.Unlock()
	return value, err
}

func (b *sbxLiveProductBackend) Run(ctx context.Context, request sandbox.SBXRunRequest, stdin io.Reader) (sandbox.SBXExecutionResult, error) {
	b.calls.Add(1)
	value, err := b.SBXCommandRuntimeBackend.Run(ctx, request, stdin)
	b.mu.Lock()
	b.last = value
	b.mu.Unlock()
	return value, err
}

func (b *sbxLiveProductBackend) result() sandbox.SBXExecutionResult {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.last
}

func newSBXLiveProductFixture(t *testing.T, ctx context.Context, root string, config sbxLiveProductSettings, program string) (*commandApprovalFixture, *sbxLiveProductBackend, runworktree.Workspace) {
	t.Helper()
	return newSBXLiveProductFixtureWithBackend(t, ctx, root, program, func(journal string) SBXCommandRuntimeBackend {
		backend, err := sandbox.NewSBXBackend(sandbox.SBXBackendConfig{Enabled: true, ExecutablePath: config.executable, HelperExecutable: config.helper, TemplateReference: config.template, JournalRoot: journal})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			if err := backend.Close(); err != nil {
				t.Error(err)
			}
		})
		if err := backend.Prepare(ctx); err != nil {
			t.Fatalf("production SBX helper preparation: %v", err)
		}
		return backend
	})
}

func newSBXLiveProductFixtureWithBackend(t *testing.T, ctx context.Context, root, program string, makeBackend func(string) SBXCommandRuntimeBackend) (*commandApprovalFixture, *sbxLiveProductBackend, runworktree.Workspace) {
	t.Helper()
	source := filepath.Join(root, "source")
	journal := filepath.Join(root, "journal")
	for _, directory := range []string{source, journal, filepath.Join(root, "empty-home"), filepath.Join(root, "empty-hooks")} {
		if err := os.Mkdir(directory, 0700); err != nil {
			t.Fatal(err)
		}
	}
	writeDrydockTestFile(t, filepath.Join(source, "acceptance.py"), program)
	sbxLiveProductGit(t, root, source, "init", "-q", "-b", "main")
	sbxLiveProductGit(t, root, source, "config", "user.name", "SBX Product Acceptance")
	sbxLiveProductGit(t, root, source, "config", "user.email", "sbx-product@example.invalid")
	sbxLiveProductGit(t, root, source, "add", "--", "acceptance.py")
	sbxLiveProductGit(t, root, source, "commit", "-q", "-m", "owned real SBX fixture")
	state, err := store.Open(filepath.Join(root, "product.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := state.Close(); err != nil {
			t.Error(err)
		}
	})
	workspaceRecord := store.WorkspaceRecord{ID: "sbx-live-workspace", Name: "owned real SBX source", RootPath: source}
	if err := state.SaveWorkspace(ctx, workspaceRecord); err != nil {
		t.Fatal(err)
	}
	_, run, err := NewRunService(state).Create(ctx, CreateRunRequest{Goal: "verify real SBX product execution", Profile: "code", Surface: "code", Phase: "deliver",
		WorkspaceID: workspaceRecord.ID, Budget: domain.Budget{MaxTurns: 4, MaxTokens: 2000, MaxToolCalls: 12}})
	if err != nil {
		t.Fatal(err)
	}
	request := ConfigureStandardCodeRequest{Version: domain.StandardCodePresetProtocolVersion, RunID: run.ID, BackendIntent: "sbx", Action: "configure", OperationKey: "real-sbx-preset-0001", RequestedBy: "operator"}
	if _, _, _, err := normalizeStandardCodePresetRequest(request); err != nil {
		t.Fatalf("SBX acceptance preset contract before backend preparation: %v", err)
	}
	deliverRequest := ChangeRunPhaseRequest{RunID: run.ID, Phase: "deliver", OperationKey: "real-sbx-deliver-0001", RequestedBy: "operator", Reason: "execute the explicitly selected SBX acceptance fixture"}
	if _, _, err := normalizeChangeRunPhaseRequest(deliverRequest); err != nil {
		t.Fatalf("SBX acceptance Deliver contract before backend preparation: %v", err)
	}
	worktreeExecutor, err := repository.NewRunWorktreeExecutor(filepath.Join(root, "drydocks"))
	if err != nil {
		t.Fatal(err)
	}
	drydocks, err := NewRunWorktreeService(state, worktreeExecutor)
	if err != nil {
		t.Fatal(err)
	}
	backend := makeBackend(journal)
	observed := &sbxLiveProductBackend{SBXCommandRuntimeBackend: backend}
	t.Cleanup(func() {
		if t.Failed() {
			observed.mu.Lock()
			defer observed.mu.Unlock()
			t.Logf("last SBX readiness observation: calls=%d proof=%+v error=%v", observed.readinessCalls, observed.readiness, observed.readinessErr)
		}
	})
	readiness, err := observed.Readiness(ctx)
	if err != nil || !readiness.ReadyAt(time.Now().UTC()) || readiness.Generation != backend.Generation() || !readiness.CredentialIsolationProven || !readiness.MCPIsolationProven {
		t.Fatalf("real production SBX readiness: %+v error=%v", readiness, err)
	}
	executor, err := NewSBXCommandRuntimeExecutor(state, observed)
	if err != nil {
		t.Fatal(err)
	}
	caps := domain.ExecutionPermissionRuntimeCapabilities{WorkspaceSandboxEnabled: true, OperatorApprovalEnabled: true, RuntimeAuthority: domain.NewExecutionPermissionRuntimeAuthority()}
	preset, err := NewStandardCodePresetService(state, drydocks, CapabilityReadinessRuntime{RunControlEnabled: true, RunExecutionEnabled: true,
		ExecutionPermissionControlEnabled: true, StandardCodePresetEnabled: true, ExecutionPermissionCapabilities: caps,
		SBXStartupGateEnabled: true, SBXAvailable: backend.Available(), SBXBackendReady: readiness.Ready,
		CommandRuntimeAdapters: []commandruntimeadapter.Identity{executor.Identity()}})
	if err != nil {
		t.Fatal(err)
	}
	preview, err := preset.Configure(ctx, request)
	if err != nil || !preview.TrustRequired || preview.TrustDigest == "" {
		t.Fatalf("real SBX workspace trust preview: %+v error=%v", preview, err)
	}
	request.ConfirmWorkspaceTrust, request.ExpectedTrustDigest = true, preview.TrustDigest
	configured, err := preset.Configure(ctx, request)
	if err != nil || configured.Status != StandardCodeResultConfigured || configured.SelectedBackend != domain.StandardCodeSelectedSBX ||
		configured.Profile == nil || configured.Profile.Validate() != nil || configured.Profile.Profile != domain.RunExecutionProfileSBX ||
		configured.Permission == nil || configured.Permission.Validate() != nil || configured.Permission.Mode != domain.RunExecutionPermissionAsk ||
		configured.Interaction == nil || configured.Interaction.Validate() != nil || configured.Interaction.RequiredGate != domain.ExecutionInteractionGateSBXMicroVM ||
		configured.Mode == nil || configured.Mode.Surface != domain.ExecutionSurfaceCode || configured.Mode.Phase != domain.ExecutionPhasePlan || !configured.DrydockReady || configured.CapabilityGrant {
		t.Fatalf("real SBX product preset: %+v error=%v", configured, err)
	}
	// Standard Code deliberately starts in Plan. Use the public operator phase
	// transition before Run start and lease acquisition to authorize Code/Deliver.
	deliver, err := NewRunService(state).ChangePhase(ctx, deliverRequest)
	if err != nil || deliver.Replayed || deliver.Mode.Surface != domain.ExecutionSurfaceCode || deliver.Mode.Phase != domain.ExecutionPhaseDeliver {
		t.Fatalf("real SBX fixture operator Deliver transition: %+v error=%v", deliver, err)
	}
	workspace, found, err := state.GetDrydockByRun(ctx, run.ID)
	if err != nil || !found || workspace.State != runworktree.StateReady || workspace.Path == source {
		t.Fatalf("owned real SBX Drydock: %+v found=%t error=%v", workspace, found, err)
	}
	writeDrydockTestFile(t, filepath.Join(root, "git-pointer-before.txt"), readDrydockTestFile(t, filepath.Join(workspace.Path, ".git")))
	manager, err := runner.NewSandboxCommandRuntimeManager(state, executor, idgen.New("sbx-live-product-owner"))
	if err != nil {
		t.Fatal(err)
	}
	service, err := NewSandboxedCommandRuntimeService(state, manager, executor, caps, drydocks)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 45*time.Second)
		defer cancel()
		if err := service.Shutdown(cleanup); err != nil {
			t.Error(err)
		}
	})
	if _, err := NewRunService(state).Start(ctx, run.ID); err != nil {
		t.Fatal(err)
	}
	acquired, err := state.AcquireRunExecutionLease(ctx, domain.AcquireRunExecutionLeaseRequest{RunID: run.ID, OwnerID: "sbx-live-product-worker", TTL: 5 * time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		if _, _, err := state.ReleaseRunExecutionLease(cleanup, acquired.Lease); err != nil {
			t.Error(err)
		}
	})
	turn, err := state.BeginSupervisorTurn(ctx, acquired.Lease, "explicit real SBX product acceptance; no provider calls")
	if err != nil {
		t.Fatal(err)
	}
	checker := policy.NewDefaultChecker()
	return &commandApprovalFixture{st: state, service: service, turn: turn, caps: caps, root: source, path: filepath.Join(root, "product.db"), manager: manager,
		supervisor: NewAgentRunner(state, nil, checker).WithExecutionPermissionCapabilities(caps).WithCommandRuntime(service)}, observed, workspace
}

func sbxLiveProductGit(t *testing.T, root, source string, arguments ...string) {
	t.Helper()
	git, err := exec.LookPath("git")
	if err != nil {
		t.Fatal("real Git is required for opted-in SBX acceptance", err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, git, append([]string{"-C", source, "-c", "core.hooksPath=" + filepath.Join(root, "empty-hooks"), "-c", "core.fsmonitor=false"}, arguments...)...)
	command.Env = []string{"HOME=" + filepath.Join(root, "empty-home"), "USERPROFILE=" + filepath.Join(root, "empty-home"), "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=" + filepath.Join(root, "empty-git-config"), "GIT_TERMINAL_PROMPT=0"}
	for _, name := range []string{"PATH", "PATHEXT", "SystemRoot", "WINDIR", "SystemDrive", "TEMP", "TMP"} {
		if value, exists := os.LookupEnv(name); exists {
			command.Env = append(command.Env, name+"="+value)
		}
	}
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("owned Git fixture %v: %v: %s", arguments, err, output)
	}
}

func sbxLiveProductProgram(exit int, cancel bool) string {
	program := `import os, pathlib, subprocess, sys, time
for name in ("CYBERAGENT_SBX_LIVE_SECRET_SENTINEL", "SSH_AUTH_SOCK", "DOCKER_HOST", "AWS_ACCESS_KEY_ID", "AWS_SECRET_ACCESS_KEY", "OPENAI_API_KEY", "ANTHROPIC_API_KEY", "GITHUB_TOKEN"):
    if os.environ.get(name):
        raise RuntimeError("host environment leaked: " + name)
if os.environ.get("HOME") != "/home/agent" or os.environ.get("GIT_ASKPASS") != "/bin/false":
    raise RuntimeError("guest environment was not explicitly normalized")
for name in ("/var/run/docker.sock", "/root/.ssh", "/host"):
    if pathlib.Path(name).exists():
        raise RuntimeError("ungranted host path visible: " + name)
pointer = pathlib.Path(".git")
try:
    with pointer.open("wb") as handle:
        handle.write(b"unexpected git metadata write")
except OSError:
    pass
else:
    raise RuntimeError("Drydock git pointer was writable")
pathlib.Path("acceptance-output.txt").write_text("real SBX generated file\n", encoding="utf-8")
sys.stdout.write("real SBX stdout 中文\n")
sys.stdout.flush()
sys.stderr.write("real SBX stderr 中文\n")
sys.stderr.flush()
`
	if !cancel {
		return program + fmt.Sprintf("sys.exit(%d)\n", exit)
	}
	return program + `child = r'''import os, pathlib, time
pathlib.Path("detached-started.txt").write_text(str(os.getpid()), encoding="utf-8")
deadline = time.monotonic() + 180
while time.monotonic() < deadline and not pathlib.Path("cancel-requested.txt").exists():
    with pathlib.Path("detached-heartbeat.txt").open("ab") as handle:
        handle.write(b"1")
    time.sleep(0.1)
time.sleep(20)
pathlib.Path("post-cleanup.txt").write_text("detached child survived cleanup\n", encoding="utf-8")
'''
subprocess.Popen([sys.executable, "-I", "-c", child], start_new_session=True, stdin=subprocess.DEVNULL, stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
while True:
    time.sleep(1)
`
}

func waitSBXLiveProductFile(t *testing.T, ctx context.Context, manager *runner.CommandRuntimeManager, jobID, path string, minimum int64) {
	t.Helper()
	for {
		if info, err := os.Stat(path); err == nil && info.Size() >= minimum {
			return
		}
		job, err := manager.Get(ctx, jobID)
		if err != nil || job.State.Terminal() {
			t.Fatalf("real SBX Job ended before detached-child marker %s: state=%s error=%v", filepath.Base(path), job.State, err)
		}
		select {
		case <-ctx.Done():
			t.Fatalf("real guest marker did not arrive: %s: %v", filepath.Base(path), ctx.Err())
		case <-time.After(20 * time.Millisecond):
		}
	}
}

func waitSBXLiveProductTerminal(t *testing.T, ctx context.Context, manager *runner.CommandRuntimeManager, jobID string) runner.CommandRuntimeJobSnapshot {
	t.Helper()
	for {
		job, _, err := manager.Wait(ctx, jobID, time.Second, 0, 64*1024)
		if err != nil {
			t.Fatalf("real SBX Job wait: %v", err)
		}
		if job.State.Terminal() {
			return job
		}
		select {
		case <-ctx.Done():
			t.Fatal("real SBX Job did not converge to terminal cleanup", ctx.Err())
		case <-time.After(20 * time.Millisecond):
		}
	}
}

func assertSBXLiveProductDetachedStopped(t *testing.T, ctx context.Context, root string) {
	t.Helper()
	heartbeat := readDrydockTestFile(t, filepath.Join(root, "detached-heartbeat.txt"))
	// The child scheduled a write twenty seconds after the owned cancellation
	// canary. Observe longer than that delay after confirmed VM cleanup.
	timer := time.NewTimer(21 * time.Second)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		t.Fatal("post-cleanup detached-child observation incomplete", ctx.Err())
	case <-timer.C:
	}
	if readDrydockTestFile(t, filepath.Join(root, "detached-heartbeat.txt")) != heartbeat {
		t.Fatal("detached guest child wrote a heartbeat after confirmed VM cleanup")
	}
	if _, err := os.Lstat(filepath.Join(root, "post-cleanup.txt")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("detached guest child produced its delayed post-cleanup write: %v", err)
	}
}

type sbxLiveProductJournal struct {
	AppName            string `json:"app_name"`
	Version            string `json:"version"`
	OperationDigest    string `json:"operation_digest"`
	RequestFingerprint string `json:"request_fingerprint"`
	Name               string `json:"name"`
	ID                 string `json:"id"`
	Workspace          string `json:"workspace"`
	Phase              string `json:"phase"`
	Removed            bool   `json:"removed"`
}

func readSBXLiveProductJournal(t *testing.T, root, workspace string) sbxLiveProductJournal {
	t.Helper()
	paths, err := filepath.Glob(filepath.Join(root, "*.json"))
	if err != nil || len(paths) != 1 {
		t.Fatalf("expected one durable VM dispatch journal: journals=%d error=%v", len(paths), err)
	}
	data, err := os.ReadFile(paths[0])
	if err != nil {
		t.Fatal(err)
	}
	var value sbxLiveProductJournal
	decoder := json.NewDecoder(strings.NewReader(string(data)))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&value) != nil || value.AppName != sandbox.SBXAppName || value.Version != sandbox.SBXPolicyVersion ||
		value.Phase != "removed" || !value.Removed || value.ID == "" || !strings.HasPrefix(value.Name, "traverse-sbx-") ||
		value.Workspace != workspace || len(value.OperationDigest) != 64 || len(value.RequestFingerprint) != 64 || filepath.Base(paths[0]) != value.OperationDigest+".json" {
		t.Fatalf("durable exact VM cleanup receipt absent: %+v", value)
	}
	return value
}

func sbxLiveProductSHA(value string) string {
	digest := sha256.Sum256([]byte(value))
	return hex.EncodeToString(digest[:])
}

func sbxLiveProductPartsSHA(parts ...string) string {
	data, _ := json.Marshal(parts)
	return sbxLiveProductSHA(string(data))
}

func sbxLiveProductCheckpointCursor(t *testing.T, ctx context.Context, state *store.SQLiteStore, runID string) string {
	t.Helper()
	cursor, found, err := state.GetWorkspaceCheckpointRunState(ctx, runID)
	if err != nil {
		t.Fatal("SBX product checkpoint cursor", err)
	}
	if !found {
		return ""
	}
	return cursor.CurrentCheckpointID
}

func assertSBXLiveProductCheckpoint(t *testing.T, ctx context.Context, fixture *commandApprovalFixture, scope toolgateway.CommandRuntimeContext,
	job runner.CommandRuntimeJob, workspace runworktree.Workspace, previousID string,
) string {
	t.Helper()
	digest := workspaceBoundaryOperationDigest(job.RunID, workspacecheckpoint.TransactionCommandBatch, job.OperationDigest)
	boundary, found, err := fixture.st.GetWorkspaceCheckpointTransactionByOperation(ctx, digest)
	wantStatus := workspacecheckpoint.TransactionCompleted
	if job.State != runner.CommandRuntimeJobCompleted {
		wantStatus = workspacecheckpoint.TransactionFailed
	}
	if err != nil || !found || boundary.Validate() != nil || boundary.RunID != scope.RunID || boundary.WorkspaceID != workspace.WorkspaceID ||
		boundary.OperationKeyDigest != digest || boundary.Kind != workspacecheckpoint.TransactionCommandBatch || boundary.Status != wantStatus ||
		boundary.TriggerReceiptID != job.ID || boundary.BeforeCheckpointID == "" || boundary.AfterCheckpointID == "" ||
		boundary.AfterCheckpointID == boundary.BeforeCheckpointID || boundary.ExpectedCurrentCheckpointID != previousID {
		t.Fatalf("SBX product exact command checkpoint boundary: %+v found=%t error=%v", boundary, found, err)
	}
	cursor, found, err := fixture.st.GetWorkspaceCheckpointRunState(ctx, scope.RunID)
	if err != nil || !found || cursor.Validate() != nil || cursor.RunID != scope.RunID || cursor.WorkspaceID != workspace.WorkspaceID ||
		cursor.CurrentCheckpointID != boundary.AfterCheckpointID || cursor.LastTransactionID != boundary.ID {
		t.Fatalf("SBX product Run checkpoint cursor did not advance to its owned command boundary: %+v found=%t error=%v", cursor, found, err)
	}
	before, err := fixture.st.GetWorkspaceCheckpoint(ctx, boundary.BeforeCheckpointID)
	if err != nil || before.Validate() != nil || before.ParentCheckpointID != previousID {
		t.Fatalf("SBX product before checkpoint parent: %+v error=%v", before, err)
	}
	after, err := fixture.st.GetWorkspaceCheckpoint(ctx, boundary.AfterCheckpointID)
	if err != nil || after.Validate() != nil || after.ParentCheckpointID != before.ID {
		t.Fatalf("SBX product after checkpoint parent: %+v error=%v", after, err)
	}
	for _, checkpoint := range []workspacecheckpoint.Checkpoint{before, after} {
		wantPhase := workspacecheckpoint.PhaseBefore
		if checkpoint.ID == after.ID {
			wantPhase = workspacecheckpoint.PhaseAfter
		}
		if checkpoint.RunID != scope.RunID || checkpoint.MissionID != scope.MissionID || checkpoint.SessionID != scope.SessionID ||
			checkpoint.WorkspaceID != workspace.WorkspaceID || checkpoint.Phase != wantPhase ||
			checkpoint.Trigger != workspacecheckpoint.TriggerCommandBatch || checkpoint.TriggerReceiptID != job.ID ||
			checkpoint.CapabilityGeneration != scope.CapabilityGeneration {
			t.Fatalf("SBX product checkpoint scope/phase differs from its command boundary: %+v", checkpoint)
		}
	}
	return after.ID
}

// This verifies the complete product Job flow without creating a production
// backend, taking the SBX namespace lock, or starting a guest process.
// Its audited readiness double is setup evidence only, never live isolation
// evidence. All SQLite, Git, preset, lease, supervisor and operation checks use
// the same fixture and production services as the opt-in test above.
func TestSBXProductFixtureSetupContracts(t *testing.T) {
	for _, tc := range []struct {
		name   string
		exit   int
		cancel bool
	}{{"output-success", 0, false}, {"output-exit-seven", 7, false}, {"cancel-running", 0, true}} {
		t.Run(tc.name, func(t *testing.T) { testSBXProductFixtureJobFlow(t, tc.exit, tc.cancel) })
	}
}

func testSBXProductFixtureJobFlow(t *testing.T, exit int, cancel bool) {
	t.Helper()
	ctx := t.Context()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	backend := &sbxLiveProductSetupBackend{exit: exit, cancel: cancel, started: make(chan struct{})}
	fixture, observed, workspace := newSBXLiveProductFixtureWithBackend(t, ctx, root, sbxLiveProductProgram(0, false),
		func(string) SBXCommandRuntimeBackend { return backend })
	input := sbxLiveProductInput()
	if err := input.Validate(); err != nil {
		t.Fatalf("live command contract: %v", err)
	}
	fixture.record(t, input, 1)
	if _, err := fixture.st.RecordSupervisorToolExecutionStarted(ctx, fixture.turn.Checkpoint, fixture.call.CallID); err != nil {
		t.Fatal(err)
	}
	scope, prepared := fixture.scope(t)
	scope.InvocationID = fixture.call.CallID
	if err := scope.Validate(); err != nil {
		t.Fatalf("live supervisor scope: %v", err)
	}
	previousID := sbxLiveProductCheckpointCursor(t, ctx, fixture.st, scope.RunID)
	started, err := fixture.service.ExecuteCommandRuntime(ctx, scope, prepared)
	if err != nil || started.Replayed || len(started.Jobs) != 1 {
		t.Fatalf("product ExecuteCommandRuntime start: jobs=%+v replay=%t error=%s", started.Jobs, started.Replayed, sbxLiveProductErrorChain(err))
	}
	jobID := started.Jobs[0].ID
	if cancel {
		select {
		case <-backend.started:
		case <-time.After(10 * time.Second):
			t.Fatal("setup backend did not start")
		}
		if _, err := fixture.manager.Stop(ctx, jobID, false, 0); err != nil {
			t.Fatal("setup Job cancellation", err)
		}
	}
	terminal := waitSBXLiveProductTerminal(t, ctx, fixture.manager, jobID)
	job, err := fixture.st.GetCommandRuntimeJob(ctx, jobID)
	wantState := runner.CommandRuntimeJobCompleted
	if cancel {
		wantState = runner.CommandRuntimeJobCancelled
	} else if exit != 0 {
		wantState = runner.CommandRuntimeJobFailed
	}
	if err != nil || job.Validate() != nil || job.State != wantState || terminal.State != wantState || !job.TreeReaped ||
		job.PID != 0 || job.ProcessGroup != 0 || job.ExitCode == nil || (!cancel && *job.ExitCode != exit) ||
		job.Stdout != "real SBX stdout 中文\n" || job.Stderr != "real SBX stderr 中文\n" ||
		job.StdoutSHA256 != sbxLiveProductSHA(job.Stdout) || job.StderrSHA256 != sbxLiveProductSHA(job.Stderr) {
		t.Fatalf("product durable terminal/output: %+v error=%v", job, err)
	}
	if err := fixture.service.completeCommandRuntimeJobBoundary(ctx, job); err != nil {
		t.Fatalf("live command checkpoint completion: %v", err)
	}
	afterID := assertSBXLiveProductCheckpoint(t, ctx, fixture, scope, job, workspace, previousID)
	replayed, err := fixture.service.ExecuteCommandRuntime(ctx, scope, prepared)
	if err != nil || !replayed.Replayed || len(replayed.Jobs) != 1 || replayed.Jobs[0].ID != jobID || replayed.Jobs[0].State != wantState ||
		!replayed.Jobs[0].TreeReaped || sbxLiveProductCheckpointCursor(t, ctx, fixture.st, scope.RunID) != afterID {
		t.Fatalf("product exact terminal replay: %+v error=%s", replayed, sbxLiveProductErrorChain(err))
	}
	jobs, err := fixture.st.ListCommandRuntimeJobs(ctx, runner.CommandRuntimeListFilter{RunID: scope.RunID, Limit: 10})
	if err != nil || len(jobs) != 1 || observed.calls.Load() != 1 || backend.calls.Load() != 1 {
		t.Fatalf("product exact replay dispatched again: jobs=%d backend_calls=%d error=%v", len(jobs), backend.calls.Load(), err)
	}
	t.Log("complete product ExecuteCommandRuntime/SQLite Job/terminal/checkpoint/replay passed with one audited fake backend call and no VM")
}

func sbxLiveProductErrorChain(err error) string {
	if err == nil {
		return "<nil>"
	}
	value := err.Error()
	if joined, ok := err.(interface{ Unwrap() []error }); ok {
		for _, cause := range joined.Unwrap() {
			value += " | " + sbxLiveProductErrorChain(cause)
		}
	} else if cause := errors.Unwrap(err); cause != nil {
		value += " | " + sbxLiveProductErrorChain(cause)
	}
	return value
}

type sbxLiveProductSetupBackend struct {
	calls   atomic.Int32
	exit    int
	cancel  bool
	started chan struct{}
}

func (*sbxLiveProductSetupBackend) Available() bool    { return true }
func (*sbxLiveProductSetupBackend) Generation() string { return strings.Repeat("a", 64) }
func (*sbxLiveProductSetupBackend) TemplateReference() string {
	return "example.invalid/setup@sha256:" + strings.Repeat("b", 64)
}
func (b *sbxLiveProductSetupBackend) Readiness(context.Context) (sandbox.SBXReadiness, error) {
	at := time.Now().UTC()
	return sandbox.SBXReadiness{ProtocolVersion: sandbox.SBXReadinessProtocolVersion, Status: "ready", ReasonCode: "ready", Ready: true,
		FeatureEnabled: true, CLIInstalled: true, TemplateConfigured: true, DaemonReachable: true, CredentialIsolationProven: true, MCPIsolationProven: true,
		Generation: b.Generation(), EvidenceFingerprint: strings.Repeat("c", 64), CheckedAt: at, ExpiresAt: at.Add(30 * time.Second)}, nil
}
func (b *sbxLiveProductSetupBackend) Run(ctx context.Context, request sandbox.SBXRunRequest, stdin io.Reader) (sandbox.SBXExecutionResult, error) {
	result := sandbox.SBXExecutionResult{ExitCode: b.exit, Stdout: []byte("real SBX stdout 中文\n"), Stderr: []byte("real SBX stderr 中文\n"), TreeReaped: true}
	if b.calls.Add(1) != 1 || stdin != nil || request.AuthorityCheck == nil {
		return result, errors.New("unexpected setup backend dispatch")
	}
	if err := request.AuthorityCheck(ctx); err != nil {
		return result, err
	}
	if request.BeforeExec != nil {
		if err := request.BeforeExec(ctx); err != nil {
			return result, err
		}
	}
	close(b.started)
	if b.cancel {
		<-ctx.Done()
		result.ExitCode = 137
		return result, ctx.Err()
	}
	return result, nil
}
