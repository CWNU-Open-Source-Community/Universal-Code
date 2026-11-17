package application

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"image"
	"image/color"
	"image/png"
	"math"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"cyberagent-workbench/internal/apperror"
	"cyberagent-workbench/internal/browserruntime"
	"cyberagent-workbench/internal/commandruntimeadapter"
	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/idgen"
	"cyberagent-workbench/internal/runner"
	"cyberagent-workbench/internal/session"
	"cyberagent-workbench/internal/toolgateway"
	"cyberagent-workbench/internal/uievidence"
)

func TestUIEvidenceCommandScopePassesNativeBindingsAndCannotReviveAfterReconfirmation(t *testing.T) {
	ctx := t.Context()
	state, run, _, _, capabilities := newCommandRuntimeTestRuntime(t, ctx)
	mission, err := state.GetMission(ctx, run.MissionID)
	if err != nil {
		t.Fatal(err)
	}
	workspace, err := state.GetWorkspaceByID(ctx, mission.WorkspaceID)
	if err != nil {
		t.Fatal(err)
	}
	workspace.RootPath = newUIEvidenceGitWorkspace(t)
	if err = state.SaveWorkspace(ctx, workspace); err != nil {
		t.Fatal(err)
	}
	manager, err := runner.NewPlatformCommandRuntimeManager(state, idgen.New("ui-evidence-binding-test"))
	if err != nil {
		t.Skipf("platform command manager unavailable: %v", err)
	}
	t.Cleanup(func() { _ = manager.Shutdown(context.Background()) })
	commands, err := NewCommandRuntimeService(state, manager, capabilities)
	if err != nil {
		t.Fatal(err)
	}
	browsers := &fakeUIEvidenceBrowsers{driver: &fakeUIEvidenceDriver{}}
	service, err := NewUIEvidenceService(state, commands, browsers, filepath.Join(t.TempDir(), "profiles"), capabilities)
	if err != nil {
		t.Fatal(err)
	}
	request := validUIEvidenceServiceRequest(t, reserveUIEvidencePort(t))
	request.RunID = run.ID
	prepared, _, err := service.prepare(ctx, request)
	if err != nil {
		t.Fatal(err)
	}
	scope := service.commandScope(prepared, "application-start")
	if _, err = commands.loadAuthorizedBindings(ctx, scope, false); err != nil {
		t.Fatalf("modern UI evidence scope cannot enter native command boundary: %v", err)
	}
	permission, err := state.GetRunExecutionPermission(ctx, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	capabilities.RuntimeAuthority.RevokeRun(run.ID)
	if _, err = commands.loadAuthorizedBindings(ctx, scope, false); err == nil {
		t.Fatal("revoked UI evidence retained command authority")
	}
	if _, err = capabilities.RuntimeAuthority.ActivateRunFullAccess(permission); err != nil {
		t.Fatal(err)
	}
	for _, suffix := range []string{"build", "application-start", "application-read", "wait"} {
		if _, err = commands.loadAuthorizedBindings(ctx, service.commandScope(prepared, suffix), false); err == nil {
			t.Fatalf("reconfirmation revived old %s scope", suffix)
		}
	}
	request.OperationKey += "-reconfirmed"
	fresh, _, err := service.prepare(ctx, request)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = commands.loadAuthorizedBindings(ctx, service.commandScope(fresh, "application-start"), false); err != nil {
		t.Fatal(err)
	}
	cold := capabilities
	cold.RuntimeAuthority = domain.NewExecutionPermissionRuntimeAuthority()
	coldService, err := NewUIEvidenceService(state, commands, browsers, filepath.Join(t.TempDir(), "cold-profiles"), cold)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err = coldService.prepare(ctx, request); err == nil {
		t.Fatal("cold process recreated UI evidence authority")
	}
	jobs, err := state.ListCommandRuntimeJobs(ctx, runner.CommandRuntimeListFilter{RunID: run.ID, Limit: 10})
	if err != nil || len(jobs) != 0 {
		t.Fatalf("binding validation launched a process: %v %v", jobs, err)
	}
}

func TestUIEvidenceServiceRunsRealLoopbackReadinessAndCleansEveryResource(t *testing.T) {
	for _, test := range []struct {
		name        string
		diagnostics uievidence.DiagnosticsSummary
		wantStatus  uievidence.Status
		wantStage   uievidence.FailureStage
	}{
		{name: "pass", wantStatus: uievidence.StatusPassed,
			wantStage: uievidence.FailureNone},
		{name: "console failure",
			diagnostics: uievidence.DiagnosticsSummary{ConsoleErrors: 1},
			wantStatus:  uievidence.StatusFailed, wantStage: uievidence.FailureConsole},
		{name: "page failure",
			diagnostics: uievidence.DiagnosticsSummary{PageErrors: 1},
			wantStatus:  uievidence.StatusFailed, wantStage: uievidence.FailureConsole},
		{name: "request failure",
			diagnostics: uievidence.DiagnosticsSummary{FailedRequests: 1},
			wantStatus:  uievidence.StatusFailed, wantStage: uievidence.FailureNetwork},
		{name: "HTTP status failure",
			diagnostics: uievidence.DiagnosticsSummary{HTTPFailures: 1},
			wantStatus:  uievidence.StatusFailed, wantStage: uievidence.FailureNetwork},
		{name: "blocked request",
			diagnostics: uievidence.DiagnosticsSummary{BlockedRequests: 1},
			wantStatus:  uievidence.StatusFailed, wantStage: uievidence.FailureNetwork},
	} {
		t.Run(test.name, func(t *testing.T) {
			root := newUIEvidenceGitWorkspace(t)
			port := reserveUIEvidencePort(t)
			state := newFakeUIEvidenceStore(t, root)
			commands := &fakeUIEvidenceCommands{port: port}
			driver := &fakeUIEvidenceDriver{diagnostics: test.diagnostics}
			browsers := &fakeUIEvidenceBrowsers{driver: driver}
			service, err := NewUIEvidenceService(state, commands, browsers,
				filepath.Join(t.TempDir(), "profiles"), state.capabilities)
			if err != nil {
				t.Fatal(err)
			}
			service.now = monotonicUIEvidenceClock(time.Date(2026, 8, 20, 1, 0, 0, 0, time.UTC))
			request := validUIEvidenceServiceRequest(t, port)
			attempt, err := service.Run(t.Context(), request)
			if err != nil {
				t.Fatal(err)
			}
			if attempt.Status != test.wantStatus || attempt.FailureStage != test.wantStage ||
				!attempt.Cleanup.Complete() || !commands.killed || !browsers.closed {
				t.Fatalf("attempt=%+v killed=%t browser_closed=%t",
					attempt, commands.killed, browsers.closed)
			}
			if test.wantStatus == uievidence.StatusPassed && !attempt.Status.Passed() {
				t.Fatal("clean real-readiness run did not produce pass")
			}
			if len(state.steps) != len(request.Steps) || len(state.artifacts) < 4 {
				t.Fatalf("steps=%d artifacts=%d", len(state.steps), len(state.artifacts))
			}
			if driver.diagnosticCalls != 1 {
				t.Fatalf("diagnostic snapshots=%d, want exactly one", driver.diagnosticCalls)
			}
			if connection, dialErr := net.DialTimeout("tcp",
				net.JoinHostPort("127.0.0.1", fmtInt(port)), 100*time.Millisecond); dialErr == nil {
				_ = connection.Close()
				t.Fatal("application port remained open")
			}
		})
	}
}

func TestUIEvidenceRequestRejectsNetworkClientAndSecretInput(t *testing.T) {
	request := validUIEvidenceServiceRequest(t, reserveUIEvidencePort(t))
	request.Start.Script = "curl https://example.invalid"
	if request.Validate() == nil {
		t.Fatal("network-client recipe was accepted")
	}
	request = validUIEvidenceServiceRequest(t, reserveUIEvidencePort(t))
	value := "token=abcdefghijklmnopqrstuvwxyz1234567890"
	digest := sha256.Sum256([]byte(value))
	request.Steps = append(request.Steps, UIEvidenceRuntimeStep{Step: uievidence.Step{
		ID: "type-secret", Kind: uievidence.StepType, Selector: "input",
		InputSHA256: hex.EncodeToString(digest[:])}, Input: value})
	if request.Validate() == nil {
		t.Fatal("secret-like fixture input was accepted")
	}
	request = validUIEvidenceServiceRequest(t, reserveUIEvidencePort(t))
	request.Capture.Accessibility = false
	if request.Validate() == nil {
		t.Fatal("incomplete screenshot/DOM/a11y/diagnostic evidence policy was accepted")
	}
}

func TestUIEvidenceCommandScopeUsesOperatorRootAuthorityWithoutFabricatingAttempt(t *testing.T) {
	rootPath := newUIEvidenceGitWorkspace(t)
	port := reserveUIEvidencePort(t)
	state := newFakeUIEvidenceStore(t, rootPath)
	state.root.Status = domain.AgentReady
	commands := &fakeUIEvidenceCommands{port: port}
	service, err := NewUIEvidenceService(state, commands,
		&fakeUIEvidenceBrowsers{driver: &fakeUIEvidenceDriver{}},
		filepath.Join(t.TempDir(), "profiles"), state.capabilities)
	if err != nil {
		t.Fatal(err)
	}
	prepared, existing, err := service.prepare(t.Context(),
		validUIEvidenceServiceRequest(t, port))
	if err != nil || existing.Status != "" {
		t.Fatalf("prepare existing=%+v err=%v", existing, err)
	}
	scope := service.commandScope(prepared, "application-start")
	if scope.AgentID != state.root.ID || scope.AgentAttemptID != "" ||
		scope.RootAgentID != state.root.ID ||
		scope.RequestedBy != toolgateway.CommandRuntimeRequestedByUIEvidenceOperator ||
		scope.Attribution() != (domain.AgentAttribution{AgentID: state.root.ID,
			Source: domain.AgentAttributionOperatorRoot}) || scope.Validate() != nil {
		t.Fatalf("command scope fabricated an Agent attempt or lost root authority: %+v", scope)
	}
	if scope.PermissionMode != domain.RunExecutionPermissionFull || scope.PermissionSnapshotID != state.permission.ID || scope.PermissionRevision != state.permission.Revision || scope.PermissionGeneration == 0 || scope.PermissionRuntimeEpoch == "" || scope.RunAuthorizationFence == 0 || scope.ModeRevision != 1 {
		t.Fatalf("UI evidence lost modern authority: %+v", scope)
	}
	state.capabilities.RuntimeAuthority.RevokeRun(state.run.ID)
	if _, err := state.capabilities.RuntimeAuthority.ActivateRunFullAccess(state.permission); err != nil {
		t.Fatal(err)
	}
	for _, suffix := range []string{"build", "application-start", "application-read", "wait"} {
		reused := service.commandScope(prepared, suffix)
		if reused.PermissionGeneration != scope.PermissionGeneration || reused.RunAuthorizationFence != scope.RunAuthorizationFence || agentCodeRuntimeCurrent(state.capabilities, state.permission, reused.PermissionSnapshotID, reused.PermissionGeneration, reused.PermissionRuntimeEpoch, reused.RunAuthorizationFence) {
			t.Fatalf("a new confirmation revived prior UI evidence authority: %+v", reused)
		}
	}
	scope.AgentAttemptID = "attempt-fabricated-by-operator-path"
	if scope.Validate() == nil {
		t.Fatal("operator UI evidence scope accepted a fabricated Agent attempt")
	}
}

func TestUIEvidenceUsesBoundedAttemptIdentityForMaximumOperationKey(t *testing.T) {
	root := newUIEvidenceGitWorkspace(t)
	port := reserveUIEvidencePort(t)
	state := newFakeUIEvidenceStore(t, root)
	commands := &fakeUIEvidenceCommands{port: port}
	browsers := &fakeUIEvidenceBrowsers{driver: &fakeUIEvidenceDriver{}}
	service, err := NewUIEvidenceService(state, commands, browsers,
		filepath.Join(t.TempDir(), "profiles"), state.capabilities)
	if err != nil {
		t.Fatal(err)
	}
	request := validUIEvidenceServiceRequest(t, port)
	request.OperationKey = strings.Repeat("x", 1024)
	if err := request.Validate(); err != nil {
		t.Fatalf("maximum UI evidence operation key rejected: %v", err)
	}
	attempt, err := service.Run(t.Context(), request)
	if err != nil || attempt.Status != uievidence.StatusPassed ||
		!attempt.Cleanup.Complete() {
		t.Fatalf("maximum operation-key attempt=%+v err=%v", attempt, err)
	}
}

func TestUIEvidenceStepReceiptFailureRemainsAValidFailClosedTerminalOutcome(t *testing.T) {
	root := newUIEvidenceGitWorkspace(t)
	port := reserveUIEvidencePort(t)
	state := newFakeUIEvidenceStore(t, root)
	state.stepErr = errors.New("step ledger unavailable")
	commands := &fakeUIEvidenceCommands{port: port}
	browsers := &fakeUIEvidenceBrowsers{driver: &fakeUIEvidenceDriver{}}
	service, err := NewUIEvidenceService(state, commands, browsers,
		filepath.Join(t.TempDir(), "profiles"), state.capabilities)
	if err != nil {
		t.Fatal(err)
	}
	attempt, err := service.Run(t.Context(), validUIEvidenceServiceRequest(t, port))
	if err != nil || attempt.Status != uievidence.StatusFailed ||
		attempt.FailureStage != uievidence.FailureCapture ||
		attempt.FailureCode != "step_receipt_failed" || !attempt.Cleanup.Complete() {
		t.Fatalf("receipt failure attempt=%+v err=%v", attempt, err)
	}
}

func TestUIEvidenceReadServiceKeepsHistoryVisibleWithoutExecutionAuthority(t *testing.T) {
	root := newUIEvidenceGitWorkspace(t)
	port := reserveUIEvidencePort(t)
	state := newFakeUIEvidenceStore(t, root)
	commands := &fakeUIEvidenceCommands{port: port}
	browsers := &fakeUIEvidenceBrowsers{driver: &fakeUIEvidenceDriver{}}
	execution, err := NewUIEvidenceService(state, commands, browsers,
		filepath.Join(t.TempDir(), "profiles"), state.capabilities)
	if err != nil {
		t.Fatal(err)
	}
	attempt, err := execution.Run(t.Context(), validUIEvidenceServiceRequest(t, port))
	if err != nil || attempt.Status != uievidence.StatusPassed {
		t.Fatalf("execution attempt=%+v err=%v", attempt, err)
	}

	readOnly, err := NewUIEvidenceReadService(state)
	if err != nil {
		t.Fatal(err)
	}
	values, err := readOnly.List(t.Context(), uievidence.ListFilter{
		RunID: attempt.Manifest.RunID, Limit: 10})
	if err != nil || len(values) != 1 || values[0].Manifest.Fingerprint !=
		attempt.Manifest.Fingerprint {
		t.Fatalf("read-only list=%+v err=%v", values, err)
	}
	bundle, err := readOnly.Get(t.Context(), attempt.Manifest.AttemptID)
	if err != nil || len(bundle.Steps) == 0 || len(bundle.Artifacts) == 0 {
		t.Fatalf("read-only bundle=%+v err=%v", bundle, err)
	}
	artifact, err := readOnly.Artifact(t.Context(), attempt.Manifest.AttemptID,
		bundle.Artifacts[0].ID)
	if err != nil || artifact.Validate() != nil || !artifact.Metadata.Untrusted {
		t.Fatalf("read-only artifact=%+v err=%v", artifact.Metadata, err)
	}
	if _, err := readOnly.Start(t.Context(), validUIEvidenceServiceRequest(t,
		reserveUIEvidencePort(t))); apperror.CodeOf(err) != apperror.CodeFailedPrecondition {
		t.Fatalf("read-only service started execution: %v", err)
	}
}

func TestUIEvidenceRejectsPreexistingServiceWithoutAdoptingOrStoppingIt(t *testing.T) {
	root := newUIEvidenceGitWorkspace(t)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	port := listener.Addr().(*net.TCPAddr).Port
	state := newFakeUIEvidenceStore(t, root)
	commands := &fakeUIEvidenceCommands{port: port}
	browsers := &fakeUIEvidenceBrowsers{driver: &fakeUIEvidenceDriver{}}
	service, err := NewUIEvidenceService(state, commands, browsers,
		filepath.Join(t.TempDir(), "profiles"), state.capabilities)
	if err != nil {
		t.Fatal(err)
	}
	attempt, err := service.Run(t.Context(), validUIEvidenceServiceRequest(t, port))
	if err != nil || attempt.Status != uievidence.StatusFailed ||
		attempt.FailureStage != uievidence.FailureLaunch ||
		attempt.FailureCode != "preexisting_service" || !attempt.Cleanup.Complete() {
		t.Fatalf("preexisting-service attempt=%+v err=%v", attempt, err)
	}
	if commands.killed || browsers.closed {
		t.Fatalf("foreign service was treated as owned: killed=%t browser_closed=%t",
			commands.killed, browsers.closed)
	}
	connection, err := net.DialTimeout("tcp", listener.Addr().String(),
		100*time.Millisecond)
	if err != nil {
		t.Fatalf("pre-existing listener was disturbed: %v", err)
	}
	_ = connection.Close()
}

func TestUIEvidenceAsyncCancelReapsOwnedResourcesAndClosesExecution(t *testing.T) {
	root := newUIEvidenceGitWorkspace(t)
	port := reserveUIEvidencePort(t)
	state := newFakeUIEvidenceStore(t, root)
	commands := &fakeUIEvidenceCommands{port: port}
	driver := &fakeUIEvidenceDriver{blockNavigation: true,
		navigationStarted: make(chan struct{})}
	browsers := &fakeUIEvidenceBrowsers{driver: driver}
	service, err := NewUIEvidenceService(state, commands, browsers,
		filepath.Join(t.TempDir(), "profiles"), state.capabilities)
	if err != nil {
		t.Fatal(err)
	}
	request := validUIEvidenceServiceRequest(t, port)
	created, err := service.Start(t.Context(), request)
	if err != nil || created.Status != uievidence.StatusNotRun {
		t.Fatalf("started=%+v err=%v", created, err)
	}
	select {
	case <-driver.navigationStarted:
	case <-time.After(5 * time.Second):
		t.Fatal("asynchronous UI evidence did not reach real navigation")
	}
	cancelContext, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	completed, err := service.Cancel(cancelContext, created.Manifest.AttemptID)
	if err != nil || completed.Status != uievidence.StatusCancelled ||
		completed.FailureStage != uievidence.FailureNavigation ||
		!completed.Cleanup.Complete() || !commands.killed || !browsers.closed {
		t.Fatalf("cancelled=%+v killed=%t browser_closed=%t err=%v", completed,
			commands.killed, browsers.closed, err)
	}
	if len(state.steps) != 1 || state.steps[0].Status != uievidence.StatusCancelled {
		t.Fatalf("cancelled step receipts=%+v", state.steps)
	}
	if err := service.Close(cancelContext); err != nil {
		t.Fatal(err)
	}
	request.OperationKey = "ui-evidence-after-close"
	if _, err := service.Start(t.Context(), request); apperror.CodeOf(err) !=
		apperror.CodeFailedPrecondition {
		t.Fatalf("closed UI evidence service accepted execution: %v", err)
	}
}

func TestUIEvidenceDeadlineReapsOwnedResourcesAndRecordsTimedOut(t *testing.T) {
	root := newUIEvidenceGitWorkspace(t)
	port := reserveUIEvidencePort(t)
	state := newFakeUIEvidenceStore(t, root)
	commands := &fakeUIEvidenceCommands{port: port}
	driver := &fakeUIEvidenceDriver{blockNavigation: true,
		navigationStarted: make(chan struct{})}
	browsers := &fakeUIEvidenceBrowsers{driver: driver}
	service, err := NewUIEvidenceService(state, commands, browsers,
		filepath.Join(t.TempDir(), "profiles"), state.capabilities)
	if err != nil {
		t.Fatal(err)
	}
	deadlineContext, cancel := context.WithTimeout(t.Context(), 2*time.Second)
	defer cancel()
	attempt, err := service.Run(deadlineContext,
		validUIEvidenceServiceRequest(t, port))
	if err != nil || attempt.Status != uievidence.StatusTimedOut ||
		attempt.FailureStage != uievidence.FailureNavigation ||
		!attempt.Cleanup.Complete() || !commands.killed || !browsers.closed {
		t.Fatalf("timed-out=%+v killed=%t browser_closed=%t err=%v", attempt,
			commands.killed, browsers.closed, err)
	}
	if len(state.steps) != 1 || state.steps[0].Status != uievidence.StatusTimedOut {
		t.Fatalf("timed-out step receipts=%+v", state.steps)
	}
}

func TestUIEvidenceDeadlineReapsBuildJobBeforeApplicationLaunch(t *testing.T) {
	root := newUIEvidenceGitWorkspace(t)
	port := reserveUIEvidencePort(t)
	state := newFakeUIEvidenceStore(t, root)
	commands := &blockingBuildUIEvidenceCommands{}
	browsers := &fakeUIEvidenceBrowsers{driver: &fakeUIEvidenceDriver{}}
	service, err := NewUIEvidenceService(state, commands, browsers,
		filepath.Join(t.TempDir(), "profiles"), state.capabilities)
	if err != nil {
		t.Fatal(err)
	}
	request := validUIEvidenceServiceRequest(t, port)
	build := request.Start
	build.Purpose = "build deterministic fixture"
	request.Build = &build
	// Source revalidation includes a real Git snapshot. Give slower Windows
	// filesystems enough time to reach the deliberately blocking build job so
	// this test measures build cleanup rather than pre-build hashing latency.
	deadlineContext, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	attempt, err := service.Run(deadlineContext, request)
	if err != nil || attempt.Status != uievidence.StatusTimedOut ||
		attempt.FailureStage != uievidence.FailureBuild ||
		attempt.FailureCode != "build_failed" || !attempt.Cleanup.Complete() ||
		!commands.Killed() || browsers.closed {
		t.Fatalf("build timeout=%+v killed=%t browser_closed=%t err=%v", attempt,
			commands.Killed(), browsers.closed, err)
	}
}

func TestUIEvidenceCloseCancelsAndWaitsForSynchronousRun(t *testing.T) {
	root := newUIEvidenceGitWorkspace(t)
	port := reserveUIEvidencePort(t)
	state := newFakeUIEvidenceStore(t, root)
	commands := &fakeUIEvidenceCommands{port: port}
	driver := &fakeUIEvidenceDriver{blockNavigation: true,
		navigationStarted: make(chan struct{})}
	browsers := &fakeUIEvidenceBrowsers{driver: driver}
	service, err := NewUIEvidenceService(state, commands, browsers,
		filepath.Join(t.TempDir(), "profiles"), state.capabilities)
	if err != nil {
		t.Fatal(err)
	}
	request := validUIEvidenceServiceRequest(t, port)
	result := make(chan uievidence.Attempt, 1)
	errors := make(chan error, 1)
	go func() {
		attempt, runErr := service.Run(context.Background(), request)
		result <- attempt
		errors <- runErr
	}()
	select {
	case <-driver.navigationStarted:
	case <-time.After(5 * time.Second):
		t.Fatal("synchronous UI evidence did not reach navigation")
	}
	closeContext, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	if err := service.Close(closeContext); err != nil {
		t.Fatal(err)
	}
	attempt := <-result
	if err := <-errors; err != nil || attempt.Status != uievidence.StatusCancelled ||
		!attempt.Cleanup.Complete() || !commands.killed || !browsers.closed {
		t.Fatalf("closed synchronous attempt=%+v err=%v", attempt, err)
	}
}

func TestSafeWebUIEvidenceBrowserPrepareHonorsCancelledContext(t *testing.T) {
	provider := &SafeWebUIEvidenceBrowserProvider{runtime: &BrowserRuntimeService{}}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := provider.Prepare(ctx, UIEvidenceBrowserSelection{
		Product: browserruntime.BrowserProductEdge, Channel: browserruntime.BrowserChannelStable})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Prepare error = %v, want context cancellation", err)
	}
}

func TestUIEvidenceReadinessRejectsExpectedResponseFromTerminalApplicationJob(t *testing.T) {
	root := newUIEvidenceGitWorkspace(t)
	port := reserveUIEvidencePort(t)
	state := newFakeUIEvidenceStore(t, root)
	commands := &fakeUIEvidenceCommands{port: port, terminalOnRead: true}
	browsers := &fakeUIEvidenceBrowsers{driver: &fakeUIEvidenceDriver{}}
	service, err := NewUIEvidenceService(state, commands, browsers,
		filepath.Join(t.TempDir(), "profiles"), state.capabilities)
	if err != nil {
		t.Fatal(err)
	}
	attempt, err := service.Run(t.Context(), validUIEvidenceServiceRequest(t, port))
	if err != nil || attempt.Status != uievidence.StatusFailed ||
		attempt.FailureStage != uievidence.FailureReadiness ||
		attempt.FailureCode != "readiness_failed" || !attempt.Cleanup.Complete() {
		t.Fatalf("terminal-readiness attempt=%+v err=%v", attempt, err)
	}
	if browsers.closed {
		t.Fatal("browser was launched after the application Job became terminal")
	}
}

func TestUIEvidenceRefusesPassWhenCleanupCannotBeProven(t *testing.T) {
	root := newUIEvidenceGitWorkspace(t)
	port := reserveUIEvidencePort(t)
	state := newFakeUIEvidenceStore(t, root)
	commands := &fakeUIEvidenceCommands{port: port}
	browsers := &fakeUIEvidenceBrowsers{driver: &fakeUIEvidenceDriver{},
		incompleteCleanup: true}
	service, err := NewUIEvidenceService(state, commands, browsers,
		filepath.Join(t.TempDir(), "profiles"), state.capabilities)
	if err != nil {
		t.Fatal(err)
	}
	attempt, err := service.Run(t.Context(),
		validUIEvidenceServiceRequest(t, port))
	if err != nil || attempt.Status != uievidence.StatusFailed ||
		attempt.FailureStage != uievidence.FailureCleanup ||
		attempt.FailureCode != "cleanup_incomplete" || attempt.Status.Passed() {
		t.Fatalf("cleanup-failure attempt=%+v err=%v", attempt, err)
	}
}

func TestUIEvidenceFailsWhenSourceChangesDuringRealPageEvidence(t *testing.T) {
	root := newUIEvidenceGitWorkspace(t)
	port := reserveUIEvidencePort(t)
	state := newFakeUIEvidenceStore(t, root)
	commands := &fakeUIEvidenceCommands{port: port}
	driver := &fakeUIEvidenceDriver{onNavigate: func() {
		if err := os.WriteFile(filepath.Join(root, "fixture.txt"),
			[]byte("changed during UI evidence\n"), 0o600); err != nil {
			t.Error(err)
		}
	}}
	browsers := &fakeUIEvidenceBrowsers{driver: driver}
	service, err := NewUIEvidenceService(state, commands, browsers,
		filepath.Join(t.TempDir(), "profiles"), state.capabilities)
	if err != nil {
		t.Fatal(err)
	}
	attempt, err := service.Run(t.Context(), validUIEvidenceServiceRequest(t, port))
	if err != nil || attempt.Status != uievidence.StatusFailed ||
		attempt.FailureStage != uievidence.FailureAssertion ||
		attempt.FailureCode != "source_changed_during_evidence" ||
		!attempt.Cleanup.Complete() {
		t.Fatalf("source-change attempt=%+v err=%v", attempt, err)
	}
}

func TestUIEvidenceFailsWhenSourceChangesDuringCleanupBeforeCompletion(t *testing.T) {
	root := newUIEvidenceGitWorkspace(t)
	port := reserveUIEvidencePort(t)
	state := newFakeUIEvidenceStore(t, root)
	commands := &fakeUIEvidenceCommands{port: port}
	browsers := &fakeUIEvidenceBrowsers{driver: &fakeUIEvidenceDriver{},
		onClose: func() {
			if err := os.WriteFile(filepath.Join(root, "fixture.txt"),
				[]byte("changed while owned resources were closing\n"), 0o600); err != nil {
				t.Error(err)
			}
		}}
	service, err := NewUIEvidenceService(state, commands, browsers,
		filepath.Join(t.TempDir(), "profiles"), state.capabilities)
	if err != nil {
		t.Fatal(err)
	}
	attempt, err := service.Run(t.Context(), validUIEvidenceServiceRequest(t, port))
	if err != nil || attempt.Status != uievidence.StatusFailed ||
		attempt.FailureStage != uievidence.FailureAssertion ||
		attempt.FailureCode != "source_changed_before_completion" ||
		!attempt.Cleanup.Complete() {
		t.Fatalf("cleanup source-change attempt=%+v err=%v", attempt, err)
	}
}

type fakeUIEvidenceStore struct {
	mu           sync.Mutex
	run          domain.Run
	mission      domain.Mission
	workspace    session.WorkspaceRecord
	root         domain.AgentNode
	lease        domain.RunExecutionLease
	permission   domain.RunExecutionPermissionSnapshot
	capabilities domain.ExecutionPermissionRuntimeCapabilities
	attempts     map[string]uievidence.Attempt
	operations   map[string]string
	steps        []uievidence.StepReceipt
	artifacts    []uievidence.Artifact
	stepErr      error
}

func newFakeUIEvidenceStore(t *testing.T, rootPath string) *fakeUIEvidenceStore {
	t.Helper()
	now := time.Now().UTC()
	state := &fakeUIEvidenceStore{
		run: domain.Run{ID: "run-ui-evidence", MissionID: "mission-ui-evidence",
			SessionID: "session-ui-evidence", Status: domain.RunRunning},
		mission: domain.Mission{ID: "mission-ui-evidence", WorkspaceID: "workspace-ui-evidence"},
		workspace: session.WorkspaceRecord{ID: "workspace-ui-evidence", Name: "ui",
			RootPath: rootPath, CreatedAt: now},
		root: domain.AgentNode{ID: "agent-ui-root", RunID: "run-ui-evidence",
			SessionID: "session-ui-evidence", Role: domain.AgentRoleRoot},
		lease: domain.RunExecutionLease{RunID: "run-ui-evidence", LeaseID: "lease-ui-evidence",
			OwnerID: "agent-ui-root", Generation: 1, Status: domain.RunExecutionLeaseActive,
			AcquiredAt: now, RenewedAt: now, ExpiresAt: time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC)},
		attempts: make(map[string]uievidence.Attempt), operations: make(map[string]string)}
	initial, err := domain.NewInitialRunExecutionPermissionSnapshot("permission-ui-ask", state.run, state.mission, "operator", now)
	if err != nil {
		t.Fatal(err)
	}
	state.permission, err = initial.Next("permission-ui-full", domain.RunExecutionPermissionFull, true, "operator", "UI evidence fixture confirmation", now)
	if err != nil {
		t.Fatal(err)
	}
	state.capabilities = domain.ExecutionPermissionRuntimeCapabilities{OperatorApprovalEnabled: true, DangerFullAccessEnabled: true, RuntimeAuthority: domain.NewExecutionPermissionRuntimeAuthority()}
	if _, err = state.capabilities.RuntimeAuthority.ActivateRunFullAccess(state.permission); err != nil {
		t.Fatal(err)
	}
	return state
}

func (s *fakeUIEvidenceStore) GetRun(context.Context, string) (domain.Run, error) { return s.run, nil }
func (s *fakeUIEvidenceStore) GetRunMode(context.Context, string) (domain.RunModeSnapshot, error) {
	return domain.RunModeSnapshot{ID: "mode-ui-evidence", RunID: s.run.ID, MissionID: s.mission.ID, Surface: domain.ExecutionSurfaceCode, Phase: domain.ExecutionPhaseDeliver, Profile: "code", Revision: 1}, nil
}
func (s *fakeUIEvidenceStore) GetMission(context.Context, string) (domain.Mission, error) {
	return s.mission, nil
}
func (s *fakeUIEvidenceStore) GetWorkspaceByID(context.Context, string) (session.WorkspaceRecord, error) {
	return s.workspace, nil
}
func (s *fakeUIEvidenceStore) GetRootAgent(context.Context, string) (domain.AgentNode, bool, error) {
	return s.root, true, nil
}
func (s *fakeUIEvidenceStore) GetRunExecutionPermission(
	context.Context, string,
) (domain.RunExecutionPermissionSnapshot, error) {
	return s.permission, nil
}
func (s *fakeUIEvidenceStore) GetRunExecutionLease(context.Context, string) (domain.RunExecutionLease, bool, error) {
	return s.lease, true, nil
}
func (s *fakeUIEvidenceStore) CreateUIEvidenceAttempt(_ context.Context, attempt uievidence.Attempt) (uievidence.Attempt, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if id := s.operations[attempt.OperationDigest]; id != "" {
		existing := s.attempts[id]
		if existing.RequestFingerprint != attempt.RequestFingerprint {
			return uievidence.Attempt{}, false, errors.New("operation conflict")
		}
		return existing, true, nil
	}
	s.attempts[attempt.Manifest.AttemptID] = attempt
	s.operations[attempt.OperationDigest] = attempt.Manifest.AttemptID
	return attempt, false, nil
}
func (s *fakeUIEvidenceStore) UpdateUIEvidenceAttempt(_ context.Context, attempt uievidence.Attempt, version int64) (uievidence.Attempt, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.attempts[attempt.Manifest.AttemptID].Version != version {
		return uievidence.Attempt{}, errors.New("version conflict")
	}
	s.attempts[attempt.Manifest.AttemptID] = attempt
	return attempt, nil
}
func (s *fakeUIEvidenceStore) GetUIEvidenceAttempt(_ context.Context, id string) (uievidence.Attempt, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	value, ok := s.attempts[id]
	if !ok {
		return uievidence.Attempt{}, apperror.New(apperror.CodeNotFound, "not found")
	}
	return value, nil
}
func (s *fakeUIEvidenceStore) ListUIEvidenceAttempts(_ context.Context,
	filter uievidence.ListFilter,
) ([]uievidence.Attempt, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	values := make([]uievidence.Attempt, 0, len(s.attempts))
	for _, attempt := range s.attempts {
		if filter.RunID != "" && attempt.Manifest.RunID != filter.RunID {
			continue
		}
		if filter.Status != "" && attempt.Status != filter.Status {
			continue
		}
		values = append(values, attempt)
	}
	return values, nil
}
func (s *fakeUIEvidenceStore) AddUIEvidenceStep(ctx context.Context, value uievidence.StepReceipt) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.stepErr != nil {
		return s.stepErr
	}
	s.steps = append(s.steps, value)
	return nil
}
func (s *fakeUIEvidenceStore) ListUIEvidenceSteps(context.Context, string) ([]uievidence.StepReceipt, error) {
	return append([]uievidence.StepReceipt(nil), s.steps...), nil
}
func (s *fakeUIEvidenceStore) AddUIEvidenceArtifact(_ context.Context, value uievidence.Artifact) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.artifacts = append(s.artifacts, value)
	return nil
}
func (s *fakeUIEvidenceStore) ListUIEvidenceArtifacts(context.Context, string) ([]uievidence.ArtifactMetadata, error) {
	values := make([]uievidence.ArtifactMetadata, len(s.artifacts))
	for i := range s.artifacts {
		values[i] = s.artifacts[i].Metadata
	}
	return values, nil
}
func (s *fakeUIEvidenceStore) GetUIEvidenceArtifact(_ context.Context, _, id string) (uievidence.Artifact, error) {
	for _, value := range s.artifacts {
		if value.Metadata.ID == id {
			return value, nil
		}
	}
	return uievidence.Artifact{}, errors.New("not found")
}
func (s *fakeUIEvidenceStore) UIEvidenceArtifactTotals(context.Context, string) (int, int64, error) {
	var size int64
	for _, value := range s.artifacts {
		size += int64(len(value.Content))
	}
	return len(s.artifacts), size, nil
}
func (s *fakeUIEvidenceStore) ReconcileUIEvidenceAttempts(context.Context, time.Time) ([]uievidence.Attempt, error) {
	return nil, nil
}

type fakeUIEvidenceCommands struct {
	mu             sync.Mutex
	port           int
	server         *http.Server
	listener       net.Listener
	killed         bool
	terminalOnRead bool
}

type blockingBuildUIEvidenceCommands struct {
	mu     sync.Mutex
	killed bool
}

func uiEvidenceTestCommandRuntimeAdapter() commandruntimeadapter.Identity {
	return commandruntimeadapter.HostUnsandboxed(uiEvidenceTestDigest("command-runtime"))
}

func (*blockingBuildUIEvidenceCommands) AdvertisedCommandRuntimeAdapter(
	context.Context, string, domain.RunExecutionPermissionMode,
) (commandruntimeadapter.Identity, bool, error) {
	return uiEvidenceTestCommandRuntimeAdapter(), true, nil
}

func (*fakeUIEvidenceCommands) AdvertisedCommandRuntimeAdapter(
	context.Context, string, domain.RunExecutionPermissionMode,
) (commandruntimeadapter.Identity, bool, error) {
	return uiEvidenceTestCommandRuntimeAdapter(), true, nil
}

func (c *blockingBuildUIEvidenceCommands) ExecuteCommandRuntime(ctx context.Context,
	scope toolgateway.CommandRuntimeContext, input toolgateway.CommandRuntimeInput,
) (toolgateway.CommandRuntimeExecutionResult, error) {
	if err := scope.Validate(); err != nil {
		return toolgateway.CommandRuntimeExecutionResult{}, err
	}
	result := toolgateway.CommandRuntimeExecutionResult{Backend: "fake-build", Action: input.Action}
	job := runner.CommandRuntimeJobSnapshot{ID: "command-job-ui-build",
		State: runner.CommandRuntimeJobRunning, Profile: inputProfile(input),
		Network:     runner.CommandRuntimeNetworkDisabled,
		Credentials: runner.CommandRuntimeCredentialsNone, Version: 1,
		CreatedAt: time.Now().UTC()}
	switch input.Action {
	case toolgateway.CommandRuntimeActionStart:
		result.Jobs = []runner.CommandRuntimeJobSnapshot{job}
		return result, nil
	case toolgateway.CommandRuntimeActionWait:
		<-ctx.Done()
		return result, ctx.Err()
	case toolgateway.CommandRuntimeActionKill:
		c.mu.Lock()
		c.killed = true
		c.mu.Unlock()
		job.State, job.TreeReaped = runner.CommandRuntimeJobKilled, true
		result.Jobs = []runner.CommandRuntimeJobSnapshot{job}
		return result, nil
	default:
		return result, errors.New("unexpected build command action")
	}
}

func (c *blockingBuildUIEvidenceCommands) cleanupUIEvidenceJob(_ context.Context,
	_ uiEvidenceCommandCleanupBinding,
) (runner.CommandRuntimeJobSnapshot, error) {
	c.mu.Lock()
	c.killed = true
	c.mu.Unlock()
	return runner.CommandRuntimeJobSnapshot{ID: "command-job-ui-build",
		State: runner.CommandRuntimeJobKilled, TreeReaped: true}, nil
}

func (c *blockingBuildUIEvidenceCommands) Killed() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.killed
}

func (c *fakeUIEvidenceCommands) ExecuteCommandRuntime(_ context.Context,
	scope toolgateway.CommandRuntimeContext, input toolgateway.CommandRuntimeInput,
) (toolgateway.CommandRuntimeExecutionResult, error) {
	if err := scope.Validate(); err != nil {
		return toolgateway.CommandRuntimeExecutionResult{}, err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	result := toolgateway.CommandRuntimeExecutionResult{Backend: "fake", Action: input.Action}
	job := runner.CommandRuntimeJobSnapshot{ID: "command-job-ui-application",
		State: runner.CommandRuntimeJobRunning, Profile: inputProfile(input),
		Network:     runner.CommandRuntimeNetworkDisabled,
		Credentials: runner.CommandRuntimeCredentialsNone, Version: 1,
		CreatedAt: time.Now().UTC()}
	switch input.Action {
	case toolgateway.CommandRuntimeActionStart:
		listener, err := net.Listen("tcp", net.JoinHostPort("127.0.0.1", fmtInt(c.port)))
		if err != nil {
			return result, err
		}
		c.listener = listener
		mux := http.NewServeMux()
		mux.HandleFunc("/health", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
		mux.HandleFunc("/", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("fixture")) })
		c.server = &http.Server{Handler: mux}
		go func() { _ = c.server.Serve(listener) }()
	case toolgateway.CommandRuntimeActionRead, toolgateway.CommandRuntimeActionWait:
		if input.Action == toolgateway.CommandRuntimeActionRead && c.terminalOnRead {
			job.State = runner.CommandRuntimeJobFailed
		}
	case toolgateway.CommandRuntimeActionKill:
		if c.server != nil {
			_ = c.server.Close()
		}
		if c.listener != nil {
			_ = c.listener.Close()
		}
		c.killed = true
		job.State, job.TreeReaped = runner.CommandRuntimeJobKilled, true
	default:
		return result, errors.New("unsupported fake command action")
	}
	result.Jobs = []runner.CommandRuntimeJobSnapshot{job}
	return result, nil
}

func (c *fakeUIEvidenceCommands) cleanupUIEvidenceJob(_ context.Context,
	_ uiEvidenceCommandCleanupBinding,
) (runner.CommandRuntimeJobSnapshot, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.server != nil {
		_ = c.server.Close()
	}
	if c.listener != nil {
		_ = c.listener.Close()
	}
	c.killed = true
	return runner.CommandRuntimeJobSnapshot{ID: "command-job-ui-application",
		State: runner.CommandRuntimeJobKilled, TreeReaped: true}, nil
}

func inputProfile(input toolgateway.CommandRuntimeInput) runner.CommandRuntimeProfile {
	if len(input.Commands) == 1 {
		return input.Commands[0].Profile
	}
	return runner.CommandRuntimeProcess
}

type fakeUIEvidenceBrowsers struct {
	driver            *fakeUIEvidenceDriver
	closed            bool
	incompleteCleanup bool
	onClose           func()
}

func (b *fakeUIEvidenceBrowsers) Prepare(context.Context, UIEvidenceBrowserSelection) (UIEvidenceBrowserPreparation, error) {
	return UIEvidenceBrowserPreparation{ManifestIdentity: uievidence.BrowserIdentity{
		Product: "edge", Version: "1.2.3", ExecutableSHA256: uiEvidenceTestDigest("browser"),
		DriverProtocol: uievidence.DriverProtocolVersion, Headless: true,
		TemporaryProfile: true}}, nil
}
func (b *fakeUIEvidenceBrowsers) Open(context.Context, UIEvidenceBrowserPreparation, BrowserRuntimeLaunchRequest) (*UIEvidenceBrowserRun, error) {
	return &UIEvidenceBrowserRun{Driver: b.driver}, nil
}
func (b *fakeUIEvidenceBrowsers) Close(context.Context, *UIEvidenceBrowserRun) (browserruntime.BrowserRuntimeReceipt, error) {
	b.closed = true
	if b.onClose != nil {
		b.onClose()
	}
	if b.incompleteCleanup {
		return browserruntime.BrowserRuntimeReceipt{ProcessTreeQuiescent: true,
			NetworkCleanupVerified: true, ProfileReleased: true}, nil
	}
	return browserruntime.BrowserRuntimeReceipt{ProcessTreeQuiescent: true, NetworkCleanupVerified: true, ProfileReleased: true, ProfileCleaned: true}, nil
}

type fakeUIEvidenceDriver struct {
	diagnostics       uievidence.DiagnosticsSummary
	diagnosticCalls   int
	environment       uievidence.Environment
	blockNavigation   bool
	navigationStarted chan struct{}
	navigationOnce    sync.Once
	onNavigate        func()
}

func (d *fakeUIEvidenceDriver) ConfigureUIEvidence(_ context.Context, environment uievidence.Environment) error {
	d.environment = environment
	return nil
}

func (d *fakeUIEvidenceDriver) Navigate(ctx context.Context, _ string) (browserruntime.RestrictedNavigationResult, error) {
	if d.onNavigate != nil {
		d.onNavigate()
	}
	if d.blockNavigation {
		d.navigationOnce.Do(func() { close(d.navigationStarted) })
		<-ctx.Done()
		return browserruntime.RestrictedNavigationResult{}, ctx.Err()
	}
	return browserruntime.RestrictedNavigationResult{}, nil
}
func (*fakeUIEvidenceDriver) ClickUIEvidence(context.Context, string) error { return nil }
func (*fakeUIEvidenceDriver) TypeUIEvidence(context.Context, string, string, string) error {
	return nil
}
func (*fakeUIEvidenceDriver) AssertUIEvidenceSelector(context.Context, string, bool) error {
	return nil
}
func (*fakeUIEvidenceDriver) DOMUIEvidence(context.Context) (browserruntime.UIEvidenceTextCapture, error) {
	return fakeTextCapture("<main>fixture</main>"), nil
}
func (*fakeUIEvidenceDriver) AccessibilityUIEvidence(context.Context) (browserruntime.UIEvidenceTextCapture, error) {
	return fakeTextCapture(`{"nodes":[]}`), nil
}
func (*fakeUIEvidenceDriver) PerformanceUIEvidence(context.Context) (browserruntime.UIEvidenceTextCapture, error) {
	return fakeTextCapture(`{"metrics":[]}`), nil
}
func (d *fakeUIEvidenceDriver) DiagnosticsUIEvidence(context.Context) (browserruntime.UIEvidenceDiagnostics, browserruntime.UIEvidenceTextCapture, error) {
	d.diagnosticCalls++
	return browserruntime.UIEvidenceDiagnostics{Summary: d.diagnostics, UntrustedEvidence: true, CapturedAt: time.Now().UTC()}, fakeTextCapture(`{"console":[],"network":[]}`), nil
}
func (d *fakeUIEvidenceDriver) ScreenshotUIEvidence(context.Context, []string, float64) (browserruntime.RestrictedScreenshot, int, int, error) {
	value := uiEvidenceTestPNG()
	width := int(math.Round(float64(d.environment.Viewport.Width) * d.environment.Viewport.DPR))
	height := int(math.Round(float64(d.environment.Viewport.Height) * d.environment.Viewport.DPR))
	return browserruntime.RestrictedScreenshot{MediaType: "image/png", PNG: value, CompletedAt: time.Now().UTC()}, width, height, nil
}

func fakeTextCapture(value string) browserruntime.UIEvidenceTextCapture {
	return browserruntime.UIEvidenceTextCapture{MIME: "application/json", Content: []byte(value), Redacted: true, CapturedAt: time.Now().UTC()}
}

func validUIEvidenceServiceRequest(t *testing.T, port int) UIEvidenceStartRequest {
	t.Helper()
	spec := runner.CommandRuntimeSpec{Version: runner.CommandRuntimeProtocolVersion,
		WorkingDirectory: ".", Environment: []runner.CommandRuntimeEnvironment{},
		StdinPolicy: runner.CommandRuntimeStdinClosed, CloseInitialStdin: true,
		TimeoutMilliseconds: 60000, Output: runner.CommandRuntimeOutputPolicy{
			InlineBytes: 4096, ArtifactBytes: 4096},
		Network:     runner.CommandRuntimeNetworkDisabled,
		Credentials: runner.CommandRuntimeCredentialsNone, Purpose: "serve deterministic fixture"}
	if runtime.GOOS == "windows" {
		spec.Profile, spec.Script = runner.CommandRuntimePowerShell, "Write-Output fixture"
	} else {
		spec.Profile, spec.Script = runner.CommandRuntimeBash, "printf fixture"
	}
	base := "http://127.0.0.1:" + fmtInt(port)
	return UIEvidenceStartRequest{RunID: "run-ui-evidence",
		OperationKey: "ui-evidence-test-operation", Start: spec,
		Readiness: uievidence.Readiness{URL: base + "/health", Method: "GET",
			ExpectedStatus: []int{200}, TimeoutMilliseconds: 5000,
			IntervalMilliseconds: 25}, URL: base + "/", Route: "/",
		Browser: UIEvidenceBrowserSelection{Product: browserruntime.BrowserProductEdge,
			Channel: browserruntime.BrowserChannelStable},
		Environment: uievidence.Environment{Viewport: uievidence.Viewport{
			Width: 1280, Height: 720, DPR: 1}, Locale: "en-US",
			Theme: uievidence.ThemeLight, ReducedMotion: true},
		Fixture: uievidence.Fixture{Name: "deterministic fixture", Seed: "42",
			PageState: "ready", DataSHA256: uiEvidenceTestDigest("fixture"),
			Deterministic: true, Synthetic: true},
		Steps: []UIEvidenceRuntimeStep{{Step: uievidence.Step{ID: "navigate",
			Kind: uievidence.StepNavigate}}},
		Capture: uievidence.CapturePolicy{Screenshot: true, DOM: true,
			Accessibility: true, Console: true, Network: true, Performance: true,
			MaskSelectors: []string{}},
		FailurePolicy: uievidence.FailurePolicy{FailOnConsoleError: true,
			FailOnPageError: true, FailOnRequestError: true, FailOnHTTPStatus: true}}
}

func newUIEvidenceGitWorkspace(t *testing.T) string {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git unavailable")
	}
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "fixture.txt"), []byte("fixture\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"init", "-q"}, {"config", "user.email", "ui@example.invalid"},
		{"config", "user.name", "UI Test"}, {"add", "."}, {"commit", "-m", "fixture"}} {
		command := exec.Command("git", append([]string{"-C", root}, args...)...)
		hideSubprocessWindow(command)
		command.Env = append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1", "GIT_TERMINAL_PROMPT=0")
		if output, err := command.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, output)
		}
	}
	return root
}

func reserveUIEvidencePort(t *testing.T) int {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	_ = listener.Close()
	return port
}
func fmtInt(value int) string {
	const digits = "0123456789"
	if value == 0 {
		return "0"
	}
	buffer := make([]byte, 0, 8)
	for value > 0 {
		buffer = append(buffer, digits[value%10])
		value /= 10
	}
	for i, j := 0, len(buffer)-1; i < j; i, j = i+1, j-1 {
		buffer[i], buffer[j] = buffer[j], buffer[i]
	}
	return string(buffer)
}
func uiEvidenceTestDigest(value string) string {
	digest := sha256.Sum256([]byte(value))
	return hex.EncodeToString(digest[:])
}
func uiEvidenceTestPNG() []byte {
	canvas := image.NewRGBA(image.Rect(0, 0, 2, 2))
	for y := 0; y < 2; y++ {
		for x := 0; x < 2; x++ {
			canvas.Set(x, y, color.White)
		}
	}
	var output bytes.Buffer
	_ = png.Encode(&output, canvas)
	return output.Bytes()
}
func monotonicUIEvidenceClock(start time.Time) func() time.Time {
	var mu sync.Mutex
	current := start
	return func() time.Time {
		mu.Lock()
		defer mu.Unlock()
		current = current.Add(time.Millisecond)
		return current
	}
}

var _ UIEvidenceStore = (*fakeUIEvidenceStore)(nil)
var _ toolgateway.CommandRuntimeExecutor = (*fakeUIEvidenceCommands)(nil)
var _ UIEvidenceBrowserProvider = (*fakeUIEvidenceBrowsers)(nil)
var _ UIEvidenceBrowserDriver = (*fakeUIEvidenceDriver)(nil)
