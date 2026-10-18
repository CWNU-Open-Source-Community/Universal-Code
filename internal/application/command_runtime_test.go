package application

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"cyberagent-workbench/internal/apperror"
	"cyberagent-workbench/internal/commandruntimeadapter"
	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/idgen"
	"cyberagent-workbench/internal/runner"
	"cyberagent-workbench/internal/store"
	"cyberagent-workbench/internal/toolgateway"
)

func TestCommandRuntimeMultiplexerRejectsDuplicateBackendAcrossGenerations(t *testing.T) {
	state, err := store.Open(filepath.Join(t.TempDir(), "command-runtime-multiplexer.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer state.Close()
	capabilities := domain.ExecutionPermissionRuntimeCapabilities{
		OperatorApprovalEnabled: true, DangerFullAccessEnabled: true}
	managers := make([]*runner.CommandRuntimeManager, 0, 2)
	for _, owner := range []string{"duplicate-backend-owner-a", "duplicate-backend-owner-b"} {
		manager, err := runner.NewPlatformCommandRuntimeManager(state, idgen.New(owner))
		if err != nil {
			t.Skipf("platform host command runtime is unavailable: %v", err)
		}
		managers = append(managers, manager)
	}
	defer func() {
		for _, manager := range managers {
			shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			_ = manager.Shutdown(shutdownCtx)
			cancel()
		}
	}()
	services := make([]*CommandRuntimeService, 0, len(managers))
	for _, manager := range managers {
		service, err := NewCommandRuntimeService(state, manager, capabilities)
		if err != nil {
			t.Fatal(err)
		}
		services = append(services, service)
	}
	if services[0].adapter.Generation == services[1].adapter.Generation {
		t.Fatal("test managers unexpectedly share an adapter generation")
	}
	if value, err := NewCommandRuntimeMultiplexer(services...); value != nil ||
		apperror.CodeOf(err) != apperror.CodeConflict {
		t.Fatalf("duplicate backend generations were accepted: value=%v err=%v", value, err)
	}
}

func TestCommandRuntimeDockerCommandMapsOnlyFixedToolchains(t *testing.T) {
	for _, test := range []struct {
		executable string
		want       string
	}{
		{"go", "go"}, {"node", "node"}, {"python3", "python"}, {"cargo", "rust"},
	} {
		t.Run(test.executable, func(t *testing.T) {
			resolved := runner.CommandRuntimeResolvedSpec{
				Spec: runner.CommandRuntimeSpec{Profile: runner.CommandRuntimeProcess,
					WorkingDirectory: "src", TimeoutMilliseconds: 1501,
					Purpose: "exercise a fixed Docker toolchain"},
				ExecutablePath: filepath.Join("opt", "toolchains", test.executable),
				CanonicalArgv:  []string{"version"},
			}
			command, err := commandRuntimeDockerCommand(resolved)
			if err != nil || command.Validate() != nil || command.Toolchain != test.want ||
				command.TimeoutSeconds != 2 || command.WorkingDirectory != "src" ||
				len(command.Arguments) != 1 || command.Arguments[0] != "version" {
				t.Fatalf("Docker command=%#v err=%v", command, err)
			}
		})
	}
	for _, executable := range []string{"bash", "powershell.exe", "curl", "docker"} {
		resolved := runner.CommandRuntimeResolvedSpec{
			Spec: runner.CommandRuntimeSpec{Profile: runner.CommandRuntimeProcess,
				WorkingDirectory: ".", TimeoutMilliseconds: 1000,
				Purpose: "reject an arbitrary Docker executable"},
			ExecutablePath: filepath.Join("opt", "toolchains", executable),
			CanonicalArgv:  []string{},
		}
		if command, err := commandRuntimeDockerCommand(resolved); err == nil {
			t.Fatalf("arbitrary executable %q mapped to %#v", executable, command)
		}
	}
}

func TestCommandRuntimeAdapterReceiptsCannotMasqueradeAcrossIsolationGrades(t *testing.T) {
	host := commandruntimeadapter.HostUnsandboxed(strings.Repeat("a", 64))
	sandboxed := commandruntimeadapter.SandboxedWorkspace(
		CommandRuntimeLocalSandboxBackend, "windows-local-sandbox.v1",
		strings.Repeat("b", 64))
	forged := host
	forged.Kind = commandruntimeadapter.KindSandboxedWorkspace
	if host.SameBackend(sandboxed) || forged.Validate() == nil || forged.Executable() {
		t.Fatalf("adapter identities crossed isolation grades: host=%#v sandbox=%#v forged=%#v",
			host, sandboxed, forged)
	}
}

func TestCommandRuntimeDoesNotMapWorkspaceAccessToHostExecution(t *testing.T) {
	state, err := store.Open(filepath.Join(t.TempDir(), "workspace-access-host-runtime.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer state.Close()
	manager, err := runner.NewPlatformCommandRuntimeManager(state,
		idgen.New("workspace-access-host-owner"))
	if err != nil {
		t.Skipf("platform host command runtime is unavailable: %v", err)
	}
	defer func() {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = manager.Shutdown(shutdownCtx)
	}()
	service, err := NewCommandRuntimeService(state, manager,
		domain.ExecutionPermissionRuntimeCapabilities{WorkspaceSandboxEnabled: true})
	if service != nil || apperror.CodeOf(err) != apperror.CodeFailedPrecondition ||
		!strings.Contains(err.Error(), "danger-full-access") {
		t.Fatalf("Workspace Access enabled the host command runtime: service=%v err=%v",
			service, err)
	}
}

func TestCommandRuntimeAdvertisementRequiresCurrentRunLease(t *testing.T) {
	ctx := context.Background()
	state, runRecord, _, lease, capabilities := newCommandRuntimeTestRuntime(t, ctx)
	manager, err := runner.NewPlatformCommandRuntimeManager(state,
		idgen.New("command-runtime-advertisement-owner"))
	if err != nil {
		t.Skipf("platform host command runtime is unavailable: %v", err)
	}
	defer func() {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = manager.Shutdown(shutdownCtx)
	}()
	service, err := NewCommandRuntimeService(state, manager, capabilities)
	if err != nil {
		t.Fatal(err)
	}
	advertised, available, err := service.AdvertisedCommandRuntimeAdapter(ctx,
		runRecord.ID, domain.RunExecutionPermissionFull)
	if err != nil || !available || !advertised.SameBackend(service.adapter) {
		t.Fatalf("active Run advertisement=%#v available=%t err=%v",
			advertised, available, err)
	}
	authority := domain.NewExecutionPermissionRuntimeAuthority()
	dynamicCapabilities := capabilities
	dynamicCapabilities.RuntimeAuthority = authority
	dynamicService, err := NewCommandRuntimeService(state, manager, dynamicCapabilities)
	if err != nil {
		t.Fatal(err)
	}
	if advertised, available, err := dynamicService.AdvertisedCommandRuntimeAdapter(ctx,
		runRecord.ID, domain.RunExecutionPermissionFull); err != nil || available ||
		advertised != (commandruntimeadapter.Identity{}) {
		t.Fatalf("cold persisted Full Access was advertised: adapter=%+v available=%t err=%v",
			advertised, available, err)
	}
	permission, err := state.GetRunExecutionPermission(ctx, runRecord.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := authority.ActivateRunFullAccess(permission); err != nil {
		t.Fatal(err)
	}
	if advertised, available, err := dynamicService.AdvertisedCommandRuntimeAdapter(ctx,
		runRecord.ID, domain.RunExecutionPermissionFull); err != nil || !available ||
		!advertised.SameBackend(dynamicService.adapter) {
		t.Fatalf("live exact Full Access was not advertised: adapter=%+v available=%t err=%v",
			advertised, available, err)
	}
	if _, _, err := state.ReleaseRunExecutionLease(ctx, lease); err != nil {
		t.Fatal(err)
	}
	advertised, available, err = service.AdvertisedCommandRuntimeAdapter(ctx,
		runRecord.ID, domain.RunExecutionPermissionFull)
	if err != nil || available || advertised != (commandruntimeadapter.Identity{}) {
		t.Fatalf("released lease retained advertisement=%#v available=%t err=%v",
			advertised, available, err)
	}
}

func TestCommandRuntimeForegroundBatchHonorsOrderedFailurePolicy(t *testing.T) {
	ctx := context.Background()
	state, runRecord, root, lease, capabilities := newCommandRuntimeTestRuntime(t, ctx)
	manager, err := runner.NewPlatformCommandRuntimeManager(state,
		idgen.New("command-runtime-batch-owner"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := manager.Shutdown(shutdownCtx); err != nil {
			t.Errorf("shutdown command runtime: %v", err)
		}
	}()
	service, err := NewCommandRuntimeService(state, manager, capabilities)
	if err != nil {
		t.Fatal(err)
	}
	profile := runner.CommandRuntimeBash
	scripts := []string{
		"printf 'batch-first\\n'",
		"printf 'batch-failed\\n'; exit 7",
		"printf 'batch-third\\n'",
	}
	if runtime.GOOS == "windows" {
		profile = runner.CommandRuntimePowerShell
		scripts = []string{
			"[Console]::Out.WriteLine('batch-first')",
			"[Console]::Out.WriteLine('batch-failed'); exit 7",
			"[Console]::Out.WriteLine('batch-third')",
		}
	}
	for _, testCase := range []struct {
		policy   string
		wantJobs int
	}{
		{policy: toolgateway.CommandRuntimeFailFast, wantJobs: 2},
		{policy: toolgateway.CommandRuntimeContinue, wantJobs: 3},
	} {
		t.Run(testCase.policy, func(t *testing.T) {
			commands := make([]runner.CommandRuntimeSpec, 0, len(scripts))
			for index, script := range scripts {
				commands = append(commands, runner.CommandRuntimeSpec{
					Version: runner.CommandRuntimeProtocolVersion, Profile: profile,
					Script: script, WorkingDirectory: ".",
					Environment: []runner.CommandRuntimeEnvironment{},
					StdinPolicy: runner.CommandRuntimeStdinClosed, CloseInitialStdin: true,
					TimeoutMilliseconds: 3000,
					Output: runner.CommandRuntimeOutputPolicy{InlineBytes: 4096,
						ArtifactBytes: 4096},
					Network:     runner.CommandRuntimeNetworkDisabled,
					Credentials: runner.CommandRuntimeCredentialsNone,
					Purpose:     "ordered batch command " + string(rune('1'+index)),
				})
			}
			maxBytes := 4096
			scope := commandRuntimeTestScope(t, ctx, state, service, runRecord, root, lease, "batch")
			f := commandFixtureForScope(t, state, service, scope)
			result, err := f.execute(t, ctx,
				toolgateway.CommandRuntimeInput{
					Version:       toolgateway.CommandRuntimeToolProtocolVersion,
					Action:        toolgateway.CommandRuntimeActionRun,
					FailurePolicy: testCase.policy, MaxBytes: &maxBytes,
					Commands: commands,
				}, 1)
			if errors.Is(err, runner.ErrCommandRuntimeUnavailable) {
				t.Skipf("%s is unavailable: %v", profile, err)
			}
			if err != nil || len(result.Jobs) != testCase.wantJobs ||
				len(result.Artifacts) != testCase.wantJobs {
				t.Fatalf("batch result=%#v err=%v", result, err)
			}
			if result.Jobs[0].State != runner.CommandRuntimeJobCompleted ||
				result.Jobs[1].State != runner.CommandRuntimeJobFailed ||
				result.Jobs[1].ExitCode == nil || *result.Jobs[1].ExitCode != 7 {
				t.Fatalf("batch order/status is wrong: %#v", result.Jobs)
			}
			if testCase.wantJobs == 3 &&
				result.Jobs[2].State != runner.CommandRuntimeJobCompleted {
				t.Fatalf("continue policy skipped the third command: %#v", result.Jobs)
			}
			f.nextTurn(t, lease)
		})
	}
}

func TestCommandRuntimeForegroundBatchPreflightsEveryCommand(t *testing.T) {
	ctx := context.Background()
	state, runRecord, root, lease, capabilities := newCommandRuntimeTestRuntime(t, ctx)
	manager, err := runner.NewPlatformCommandRuntimeManager(state,
		idgen.New("command-runtime-preflight-owner"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := manager.Shutdown(shutdownCtx); err != nil {
			t.Errorf("shutdown command runtime: %v", err)
		}
	}()
	service, err := NewCommandRuntimeService(state, manager, capabilities)
	if err != nil {
		t.Fatal(err)
	}
	profile := runner.CommandRuntimeBash
	script := "printf ran > command-runtime-preflight-marker"
	if runtime.GOOS == "windows" {
		profile = runner.CommandRuntimePowerShell
		script = "[IO.File]::WriteAllText('command-runtime-preflight-marker', 'ran')"
	}
	command := runner.CommandRuntimeSpec{
		Version: runner.CommandRuntimeProtocolVersion, Profile: profile, Script: script,
		WorkingDirectory: ".", Environment: []runner.CommandRuntimeEnvironment{},
		StdinPolicy: runner.CommandRuntimeStdinClosed, CloseInitialStdin: true,
		TimeoutMilliseconds: 3000,
		Output:              runner.CommandRuntimeOutputPolicy{InlineBytes: 4096, ArtifactBytes: 4096},
		Network:             runner.CommandRuntimeNetworkDisabled,
		Credentials:         runner.CommandRuntimeCredentialsNone,
		Purpose:             "prove batch preflight prevents partial execution",
	}
	invalid := command
	invalid.WorkingDirectory = "missing-directory"
	invalid.Purpose = "invalid second command must fail before the first starts"
	maxBytes := 4096
	scope := commandRuntimeTestScope(t, ctx, state, service, runRecord, root, lease, "preflight")
	f := commandFixtureForScope(t, state, service, scope)
	permission, err := state.GetRunExecutionPermission(ctx, runRecord.ID)
	if err != nil {
		t.Fatal(err)
	}
	advertised, err := f.supervisor.supervisorCommandRuntimeTools(ctx, runRecord.ID, permission.Mode)
	if err != nil {
		t.Fatal(err)
	}
	payload, err := json.Marshal(toolgateway.CommandRuntimeInput{Version: toolgateway.CommandRuntimeToolProtocolVersion, Action: toolgateway.CommandRuntimeActionRun, FailurePolicy: toolgateway.CommandRuntimeFailFast, MaxBytes: &maxBytes, Commands: []runner.CommandRuntimeSpec{command, invalid}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = service.BindCommandRuntimeAuthority(ctx, advertised.Authority, payload); err == nil {
		t.Fatal("invalid second command passed preparation")
	}
	jobs, err := state.ListCommandRuntimeJobs(ctx, runner.CommandRuntimeListFilter{RunID: runRecord.ID, Limit: 10})
	if err != nil || len(jobs) != 0 {
		t.Fatalf("invalid batch created jobs: %v %v", jobs, err)
	}
	workspace, err := state.GetWorkspaceByID(ctx, "workspace-command-runtime-app")
	if err != nil {
		t.Fatal(err)
	}
	if _, statErr := os.Stat(filepath.Join(workspace.RootPath,
		"command-runtime-preflight-marker")); !os.IsNotExist(statErr) {
		t.Fatalf("first command ran before later-command preflight: %v", statErr)
	}
}

func TestCommandRuntimeForegroundCancellationReapsProcessTree(t *testing.T) {
	ctx := context.Background()
	state, runRecord, root, lease, capabilities := newCommandRuntimeTestRuntime(t, ctx)
	manager, err := runner.NewPlatformCommandRuntimeManager(state,
		idgen.New("command-runtime-cancel-owner"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := manager.Shutdown(shutdownCtx); err != nil {
			t.Errorf("shutdown command runtime: %v", err)
		}
	}()
	service, err := NewCommandRuntimeService(state, manager, capabilities)
	if err != nil {
		t.Fatal(err)
	}
	profile := runner.CommandRuntimeBash
	script := "sleep 30"
	if runtime.GOOS == "windows" {
		profile = runner.CommandRuntimePowerShell
		script = "Start-Sleep -Seconds 30"
	}
	maxBytes := 4096
	input := toolgateway.CommandRuntimeInput{
		Version:       toolgateway.CommandRuntimeToolProtocolVersion,
		Action:        toolgateway.CommandRuntimeActionRun,
		FailurePolicy: toolgateway.CommandRuntimeFailFast, MaxBytes: &maxBytes,
		Commands: []runner.CommandRuntimeSpec{{
			Version: runner.CommandRuntimeProtocolVersion, Profile: profile, Script: script,
			WorkingDirectory: ".", Environment: []runner.CommandRuntimeEnvironment{},
			StdinPolicy: runner.CommandRuntimeStdinClosed, CloseInitialStdin: true,
			TimeoutMilliseconds: 20_000,
			Output:              runner.CommandRuntimeOutputPolicy{InlineBytes: 4096, ArtifactBytes: 4096},
			Network:             runner.CommandRuntimeNetworkDisabled,
			Credentials:         runner.CommandRuntimeCredentialsNone,
			Purpose:             "prove foreground cancellation reaps the process tree",
		}},
	}
	scope := commandRuntimeTestScope(t, ctx, state, service, runRecord, root, lease, "cancel-foreground")
	f := commandFixtureForScope(t, state, service, scope)
	scope = f.startScope(t, input, 1)
	callCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	executionErr := make(chan error, 1)
	go func() {
		_, executeErr := service.ExecuteCommandRuntime(callCtx, scope, input)
		executionErr <- executeErr
	}()
	startupDeadline := time.Now().Add(10 * time.Second)
	for {
		jobs, listErr := state.ListCommandRuntimeJobs(ctx,
			runner.CommandRuntimeListFilter{RunID: runRecord.ID, Limit: 10})
		if listErr != nil {
			t.Fatal(listErr)
		}
		// The durable running row is visible before Start installs its
		// process-local entry. Require both signals so cancellation exercises
		// foreground cleanup instead of racing the startup commit.
		if len(jobs) == 1 && jobs[0].State == runner.CommandRuntimeJobRunning &&
			manager.OwnsActiveJob(jobs[0]) {
			break
		}
		select {
		case executeErr := <-executionErr:
			if errors.Is(executeErr, runner.ErrCommandRuntimeUnavailable) {
				t.Skipf("%s is unavailable: %v", profile, executeErr)
			}
			t.Fatalf("foreground command returned before durable startup: jobs=%#v err=%v",
				jobs, executeErr)
		default:
		}
		if time.Now().After(startupDeadline) {
			t.Fatalf("foreground command did not reach durable owned running state: jobs=%#v",
				jobs)
		}
		time.Sleep(10 * time.Millisecond)
	}
	cancel()
	var cancellationErr error
	select {
	case cancellationErr = <-executionErr:
		if !errors.Is(cancellationErr, context.Canceled) {
			t.Fatalf("foreground cancellation error=%v", cancellationErr)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("foreground cancellation did not return after durable startup")
	}
	durableDeadline := time.Now().Add(10 * time.Second)
	for {
		jobs, listErr := state.ListCommandRuntimeJobs(ctx,
			runner.CommandRuntimeListFilter{RunID: runRecord.ID, Limit: 10})
		if listErr != nil {
			t.Fatal(listErr)
		}
		if len(jobs) == 1 && jobs[0].State == runner.CommandRuntimeJobCancelled &&
			jobs[0].TreeReaped {
			break
		}
		if time.Now().After(durableDeadline) {
			t.Fatalf("foreground cancellation was not durably reaped: jobs=%#v execution_err=%v",
				jobs, cancellationErr)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestCommandRuntimeBindingBecomesStaleWhenRunningDowngradeCommits(t *testing.T) {
	ctx := context.Background()
	state, runRecord, root, _, _ := newCommandRuntimeTestRuntime(t, ctx)
	permission, err := state.GetRunExecutionPermission(ctx, runRecord.ID)
	if err != nil {
		t.Fatal(err)
	}
	authority := domain.NewExecutionPermissionRuntimeAuthority()
	if _, err := authority.ActivateRunFullAccess(permission); err != nil {
		t.Fatal(err)
	}
	capabilities := domain.ExecutionPermissionRuntimeCapabilities{
		OperatorApprovalEnabled: true, DangerFullAccessEnabled: true,
		RuntimeAuthority: authority,
	}
	manager, err := runner.NewPlatformCommandRuntimeManager(state,
		idgen.New("command-runtime-revocation-owner"))
	if err != nil {
		t.Skipf("platform host command runtime is unavailable: %v", err)
	}
	defer func() {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := manager.Shutdown(shutdownCtx); err != nil {
			t.Errorf("shutdown command runtime: %v", err)
		}
	}()
	service, err := NewCommandRuntimeService(state, manager, capabilities)
	if err != nil {
		t.Fatal(err)
	}
	mission, err := state.GetMission(ctx, runRecord.MissionID)
	if err != nil {
		t.Fatal(err)
	}
	workspace, err := state.GetWorkspaceByID(ctx, mission.WorkspaceID)
	if err != nil {
		t.Fatal(err)
	}
	rootSHA256, err := runner.CommandRuntimeWorkspaceRootSHA256(workspace.RootPath)
	if err != nil {
		t.Fatal(err)
	}
	mode, err := state.GetRunMode(ctx, runRecord.ID)
	if err != nil {
		t.Fatal(err)
	}
	profile, err := state.GetRunExecutionProfile(ctx, runRecord.ID)
	if err != nil {
		t.Fatal(err)
	}
	job := runner.CommandRuntimeJob{
		RunID: runRecord.ID, MissionID: runRecord.MissionID,
		SessionID: runRecord.SessionID, WorkspaceID: workspace.ID,
		RootAgentID: root.ID, WorkspaceRootSHA256: rootSHA256,
		ModeSnapshotID: mode.ID, ModeRevision: mode.Revision,
		ProfileSnapshotID: profile.ID, ProfileRevision: profile.Revision,
		PermissionSnapshotID: permission.ID, PermissionRevision: permission.Revision,
		PermissionMode: permission.Mode, Adapter: service.adapter,
	}
	job.PermissionGeneration, _ = capabilities.FullAccessGeneration(permission)
	job.PermissionRuntimeEpoch = authority.RuntimeEpoch()
	job.RunAuthorizationFence, err = authority.IssueRunAuthorizationFence(runRecord.ID)
	if err != nil {
		t.Fatal(err)
	}
	if current, err := service.commandRuntimeJobBindingsCurrent(ctx, job); err != nil || !current {
		t.Fatalf("live Full binding current=%t err=%v", current, err)
	}
	transition, transitionErr := NewRunExecutionPermissionService(state, capabilities).Change(ctx,
		ChangeRunExecutionPermissionRequest{RunID: runRecord.ID,
			Mode:         string(domain.RunExecutionPermissionAsk),
			OperationKey: "command-runtime-failed-revoke-0001", RequestedBy: "test_operator",
			Reason: "immediately lower permission while the Run is active"})
	if transitionErr != nil ||
		transition.Permission.Mode != domain.RunExecutionPermissionAsk {
		t.Fatalf("running permission downgrade=%+v err=%v", transition, transitionErr)
	}
	durable, err := state.GetRunExecutionPermission(ctx, runRecord.ID)
	if err != nil || durable.ID == permission.ID ||
		durable.Mode != domain.RunExecutionPermissionAsk {
		t.Fatalf("running downgrade was not durable: %+v err=%v", durable, err)
	}
	if current, err := service.commandRuntimeJobBindingsCurrent(ctx, job); err != nil || current {
		t.Fatalf("revoked Full binding remained current=%t err=%v", current, err)
	}
}

func TestCommandRuntimeBindingBecomesStaleAcrossThreadFullReconfirmation(t *testing.T) {
	ctx := context.Background()
	state, runRecord, root, lease, _ := newCommandRuntimeTestRuntime(t, ctx)
	if _, _, err := state.ReleaseRunExecutionLease(ctx, lease); err != nil {
		t.Fatal(err)
	}
	var err error
	runRecord, err = NewRunService(state).Pause(ctx, runRecord.ID)
	if err != nil {
		t.Fatal(err)
	}
	threadRecord, err := state.GetThreadByRun(ctx, runRecord.ID)
	if err != nil {
		t.Fatal(err)
	}
	authority := domain.NewExecutionPermissionRuntimeAuthority()
	capabilities := domain.ExecutionPermissionRuntimeCapabilities{
		OperatorApprovalEnabled: true, DangerFullAccessEnabled: true,
		RuntimeAuthority: authority,
	}
	threadPermissions := NewThreadExecutionPermissionService(state, capabilities)
	_, err = threadPermissions.Change(ctx, ChangeThreadExecutionPermissionRequest{
		ThreadID: threadRecord.ID, Mode: string(domain.RunExecutionPermissionFull),
		OperationKey: "command-runtime-thread-full-first-0001",
		RequestedBy:  "test_operator", Reason: "bind Full Access to the current task",
		ConfirmFull: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	runRecord, err = NewRunService(state).Resume(ctx, runRecord.ID)
	if err != nil {
		t.Fatal(err)
	}
	permission, err := state.GetRunExecutionPermission(ctx, runRecord.ID)
	if err != nil || !capabilities.AllowsSnapshot(permission) {
		t.Fatalf("initial Thread Full snapshot is not live: %+v err=%v", permission, err)
	}
	browserPermission, err := state.GetRunBrowserCDPPermission(ctx, runRecord.ID)
	if err != nil {
		t.Fatal(err)
	}
	manager, err := runner.NewPlatformCommandRuntimeManager(state,
		idgen.New("command-runtime-thread-reconfirmation-owner"))
	if err != nil {
		t.Skipf("platform host command runtime is unavailable: %v", err)
	}
	defer func() {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := manager.Shutdown(shutdownCtx); err != nil {
			t.Errorf("shutdown command runtime: %v", err)
		}
	}()
	service, err := NewCommandRuntimeService(state, manager, capabilities)
	if err != nil {
		t.Fatal(err)
	}
	mission, err := state.GetMission(ctx, runRecord.MissionID)
	if err != nil {
		t.Fatal(err)
	}
	workspace, err := state.GetWorkspaceByID(ctx, mission.WorkspaceID)
	if err != nil {
		t.Fatal(err)
	}
	rootSHA256, err := runner.CommandRuntimeWorkspaceRootSHA256(workspace.RootPath)
	if err != nil {
		t.Fatal(err)
	}
	mode, err := state.GetRunMode(ctx, runRecord.ID)
	if err != nil {
		t.Fatal(err)
	}
	profile, err := state.GetRunExecutionProfile(ctx, runRecord.ID)
	if err != nil {
		t.Fatal(err)
	}
	oldJob := runner.CommandRuntimeJob{
		ID: "command-runtime-thread-full-old-job", State: runner.CommandRuntimeJobRunning,
		RunID: runRecord.ID, MissionID: runRecord.MissionID,
		SessionID: runRecord.SessionID, WorkspaceID: workspace.ID,
		RootAgentID: root.ID, WorkspaceRootSHA256: rootSHA256,
		ModeSnapshotID: mode.ID, ModeRevision: mode.Revision,
		ProfileSnapshotID: profile.ID, ProfileRevision: profile.Revision,
		PermissionSnapshotID: permission.ID, PermissionRevision: permission.Revision,
		PermissionMode: permission.Mode, Adapter: service.adapter,
	}
	oldJob.PermissionGeneration, _ = capabilities.FullAccessGeneration(permission)
	oldJob.PermissionRuntimeEpoch = authority.RuntimeEpoch()
	oldJob.RunAuthorizationFence, err = authority.IssueRunAuthorizationFence(runRecord.ID)
	if err != nil {
		t.Fatal(err)
	}
	if current, err := service.commandRuntimeJobBindingsCurrent(ctx, oldJob); err != nil || !current {
		t.Fatalf("old Full job binding current=%t err=%v", current, err)
	}
	if _, err := NewRunService(state).Pause(ctx, runRecord.ID); err != nil {
		t.Fatal(err)
	}

	reconfirmed, err := threadPermissions.Change(ctx,
		ChangeThreadExecutionPermissionRequest{
			ThreadID: threadRecord.ID, Mode: string(domain.RunExecutionPermissionFull),
			OperationKey: "command-runtime-thread-full-reconfirm-0001",
			RequestedBy:  "test_operator", Reason: "reconfirm Full Access for the current task",
			ConfirmFull: true,
		})
	if err != nil {
		t.Fatal(err)
	}
	currentPermission, err := state.GetRunExecutionPermission(ctx, runRecord.ID)
	if err != nil || currentPermission.ID == permission.ID ||
		currentPermission.Revision <= permission.Revision ||
		!capabilities.AllowsSnapshot(currentPermission) {
		t.Fatalf("same-mode Full did not rotate and reactivate the Run snapshot: old=%+v current=%+v err=%v",
			permission, currentPermission, err)
	}
	currentBrowserPermission, err := state.GetRunBrowserCDPPermission(ctx, runRecord.ID)
	if err != nil || currentBrowserPermission.ID != browserPermission.ID ||
		currentBrowserPermission.Revision != browserPermission.Revision ||
		currentBrowserPermission.Mode != browserPermission.Mode {
		t.Fatalf("same-mode Full changed the independent CDP sub-permission: old=%+v current=%+v err=%v",
			browserPermission, currentBrowserPermission, err)
	}
	if reconfirmed.CurrentRunEffect == domain.ThreadExecutionPermissionPausedAndApplied {
		runRecord, err = NewRunService(state).Resume(ctx, runRecord.ID)
		if err != nil {
			t.Fatal(err)
		}
	}
	if current, err := service.commandRuntimeJobBindingsCurrent(ctx, oldJob); err != nil || current {
		t.Fatalf("old Full job binding survived same-mode reconfirmation: current=%t err=%v",
			current, err)
	}
}

type commandRuntimeTerminalTransitionStore struct {
	CommandRuntimeStore
	running  runner.CommandRuntimeJob
	terminal runner.CommandRuntimeJob
	calls    int
}

func (s *commandRuntimeTerminalTransitionStore) GetCommandRuntimeJob(
	context.Context, string,
) (runner.CommandRuntimeJob, error) {
	s.calls++
	if s.calls == 1 {
		return s.running, nil
	}
	return s.terminal, nil
}

func TestCommandRuntimeReadableAuthorizationAllowsTerminalOwnershipTransition(t *testing.T) {
	running := runner.CommandRuntimeJob{ID: "command-runtime-terminal-transition",
		RunID: "run-terminal-transition", MissionID: "mission-terminal-transition",
		SessionID: "session-terminal-transition", WorkspaceID: "workspace-terminal-transition",
		RootAgentID: "agent-terminal-transition", State: runner.CommandRuntimeJobRunning}
	terminal := running
	terminal.State = runner.CommandRuntimeJobCompleted
	state := &commandRuntimeTerminalTransitionStore{running: running, terminal: terminal}
	service := &CommandRuntimeService{store: state}
	root := domain.AgentNode{ID: running.RootAgentID, SessionID: running.SessionID, Role: domain.AgentRoleRoot}
	bindings := commandRuntimeBindings{
		run:       domain.Run{ID: running.RunID, SessionID: running.SessionID},
		mission:   domain.Mission{ID: running.MissionID},
		workspace: store.WorkspaceRecord{ID: running.WorkspaceID},
		root:      root,
		actor:     root,
	}
	result, err := service.authorizeReadableJob(context.Background(), running.ID, bindings)
	if err != nil || result.State != runner.CommandRuntimeJobCompleted || state.calls != 3 {
		t.Fatalf("terminal transition authorization=%#v calls=%d err=%v", result, state.calls, err)
	}
}

func TestCommandRuntimeUIEvidenceCleanupReapsExactJobAfterLeaseRelease(t *testing.T) {
	ctx := context.Background()
	state, runRecord, _, lease, capabilities := newCommandRuntimeTestRuntime(t, ctx)
	manager, err := runner.NewPlatformCommandRuntimeManager(state,
		idgen.New("command-runtime-ui-cleanup-owner"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := manager.Shutdown(shutdownCtx); err != nil {
			t.Errorf("shutdown command runtime: %v", err)
		}
	}()
	service, err := NewCommandRuntimeService(state, manager, capabilities)
	if err != nil {
		t.Fatal(err)
	}
	profile := runner.CommandRuntimeBash
	script := `IFS= read -r line; printf 'unexpected:%s\n' "$line"`
	if runtime.GOOS == "windows" {
		profile = runner.CommandRuntimePowerShell
		script = `$line = [Console]::In.ReadLine(); [Console]::Out.WriteLine("unexpected:$line")`
	}
	workspace, err := state.GetWorkspaceByID(ctx, "workspace-command-runtime-app")
	if err != nil {
		t.Fatal(err)
	}
	workspace.RootPath = newUIEvidenceGitWorkspace(t)
	if err := state.SaveWorkspace(ctx, workspace); err != nil {
		t.Fatal(err)
	}
	ui, err := NewUIEvidenceService(state, service, &fakeUIEvidenceBrowsers{driver: &fakeUIEvidenceDriver{}}, filepath.Join(t.TempDir(), "profiles"), capabilities)
	if err != nil {
		t.Fatal(err)
	}
	request := validUIEvidenceServiceRequest(t, reserveUIEvidencePort(t))
	request.RunID = runRecord.ID
	request.Start = runner.CommandRuntimeSpec{
		Version: runner.CommandRuntimeProtocolVersion, Profile: profile, Script: script,
		WorkingDirectory: ".", Environment: []runner.CommandRuntimeEnvironment{},
		StdinPolicy: runner.CommandRuntimeStdinPipe, CloseInitialStdin: false,
		TimeoutMilliseconds: 10000,
		Output:              runner.CommandRuntimeOutputPolicy{InlineBytes: 4096, ArtifactBytes: 4096},
		Network:             runner.CommandRuntimeNetworkDisabled, Credentials: runner.CommandRuntimeCredentialsNone,
		Purpose: "prove exact UI evidence cleanup survives lease release and revocation",
	}
	prepared, _, err := ui.prepare(ctx, request)
	if err != nil {
		t.Fatal(err)
	}
	scope := ui.commandScope(prepared, "application-start")
	if scope.RequestedBy != toolgateway.CommandRuntimeRequestedByUIEvidenceOperator || scope.AgentAttemptID != "" {
		t.Fatal("UI evidence fabricated Supervisor authority")
	}
	started, err := ui.startCommand(ctx, prepared, request.Start, "application-start")
	if errors.Is(err, runner.ErrCommandRuntimeUnavailable) {
		t.Skipf("%s is unavailable: %v", profile, err)
	}
	if err != nil || started.State != runner.CommandRuntimeJobRunning {
		t.Fatalf("UI cleanup Job start=%#v err=%v", started, err)
	}
	jobID := started.ID
	binding := ui.commandCleanupBinding(prepared, "application-start", jobID)
	wrong := binding
	wrong.OperationKey += "-other"
	if _, err := service.cleanupUIEvidenceJob(ctx, wrong); err == nil {
		t.Fatal("cleanup-only authority accepted a different operation identity")
	}
	active, err := manager.Get(ctx, jobID)
	if err != nil || active.State != runner.CommandRuntimeJobRunning {
		t.Fatalf("mismatched cleanup disturbed Job=%#v err=%v", active, err)
	}
	if _, _, err := state.ReleaseRunExecutionLease(ctx, lease); err != nil {
		t.Fatal(err)
	}
	if _, err := service.ExecuteCommandRuntime(ctx, scope,
		toolgateway.CommandRuntimeInput{Version: toolgateway.CommandRuntimeToolProtocolVersion,
			Action: toolgateway.CommandRuntimeActionKill, JobID: jobID}); err == nil {
		t.Fatal("ordinary command authority killed a Job after its Run lease was released")
	}
	capabilities.RuntimeAuthority.RevokeRun(runRecord.ID)
	cleaned, err := service.cleanupUIEvidenceJob(ctx, binding)
	if err != nil || (cleaned.State != runner.CommandRuntimeJobKilled && cleaned.State != runner.CommandRuntimeJobInterrupted) ||
		!cleaned.TreeReaped {
		t.Fatalf("exact UI cleanup Job=%#v err=%v", cleaned, err)
	}
}

func commandRuntimeTestScope(t *testing.T, ctx context.Context, state *store.SQLiteStore,
	service *CommandRuntimeService,
	runRecord domain.Run, root domain.AgentNode,
	lease domain.RunExecutionLease, operationKey string,
) toolgateway.CommandRuntimeContext {
	t.Helper()
	root = ensureCommandRuntimeTestAgent(t, ctx, state, lease, root)
	value := toolgateway.CommandRuntimeContext{
		InvocationID: "command-runtime-invocation-" + operationKey,
		OperationKey: operationKey, RunID: runRecord.ID,
		MissionID: runRecord.MissionID, RootAgentID: root.ID,
		AgentID: root.ID, AgentAttemptID: root.ActiveAttemptID,
		SessionID: runRecord.SessionID, WorkspaceID: "workspace-command-runtime-app",
		CapabilityGeneration: service.adapter.Generation,
		LeaseID:              lease.LeaseID, LeaseGeneration: lease.Generation,
		RequestedBy: "run_supervisor", PolicyDecision: toolgateway.Decision{
			Allowed: true, Approval: toolgateway.ApprovalAutomatic,
			Risk: "high", Reason: "test"},
		Adapter: service.adapter,
	}
	return bindCurrentCommandRuntimeTestScope(t, ctx, state, service, value)
}

func ensureCommandRuntimeTestAgent(t *testing.T, ctx context.Context,
	state *store.SQLiteStore, lease domain.RunExecutionLease, root domain.AgentNode,
) domain.AgentNode {
	t.Helper()
	current, found, err := state.GetRootAgent(ctx, root.RunID)
	if err != nil || !found {
		t.Fatalf("root agent found=%t err=%v", found, err)
	}
	if current.ActiveAttemptID != "" {
		return current
	}
	turn, err := state.BeginSupervisorTurn(ctx, lease,
		"exercise the attributed Command Runtime")
	if err != nil {
		t.Fatal(err)
	}
	return turn.Agent
}

func newCommandRuntimeTestRuntime(t *testing.T, ctx context.Context, turnBudget ...int) (
	*store.SQLiteStore, domain.Run, domain.AgentNode, domain.RunExecutionLease,
	domain.ExecutionPermissionRuntimeCapabilities,
) {
	t.Helper()
	databasePath := filepath.Join(t.TempDir(), "command-runtime-application.db")
	state, err := store.Open(databasePath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = state.Close() })
	workspaceRoot, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	workspace := store.WorkspaceRecord{ID: "workspace-command-runtime-app",
		Name: "command-runtime-app", RootPath: workspaceRoot}
	if err := state.SaveWorkspace(ctx, workspace); err != nil {
		t.Fatal(err)
	}
	runs := NewRunService(state)
	maxTurns := 4
	if len(turnBudget) > 0 {
		maxTurns = turnBudget[0]
	}
	_, runRecord, err := runs.Create(ctx, CreateRunRequest{
		Goal: "execute an owned command", Profile: "code", WorkspaceID: workspace.ID,
		Budget: domain.Budget{MaxTurns: maxTurns, MaxTokens: 1000, MaxToolCalls: 8},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := NewRunExecutionProfileService(state).Change(ctx,
		ChangeRunExecutionProfileRequest{RunID: runRecord.ID, Profile: "local",
			OperationKey: "command-runtime-profile-app-0001", RequestedBy: "test_operator",
			Reason: "exercise local command runtime"}); err != nil {
		t.Fatal(err)
	}
	capabilities := domain.ExecutionPermissionRuntimeCapabilities{
		OperatorApprovalEnabled: true, DangerFullAccessEnabled: true,
	}
	capabilities.RuntimeAuthority = domain.NewExecutionPermissionRuntimeAuthority()
	if _, err := NewRunExecutionPermissionService(state, capabilities).Change(ctx, ChangeRunExecutionPermissionRequest{
		RunID: runRecord.ID, Mode: string(domain.RunExecutionPermissionFull), OperationKey: "command-runtime-current-full", RequestedBy: "operator", Reason: "current host command regression", ConfirmFull: true,
	}); err != nil {
		t.Fatal(err)
	}
	runRecord, err = runs.Start(ctx, runRecord.ID)
	if err != nil {
		t.Fatal(err)
	}
	root, found, err := state.GetRootAgent(ctx, runRecord.ID)
	if err != nil || !found {
		t.Fatalf("root agent found=%t err=%v", found, err)
	}
	acquired, err := state.AcquireRunExecutionLease(ctx,
		domain.AcquireRunExecutionLeaseRequest{RunID: runRecord.ID,
			OwnerID: "command-runtime-test-worker", TTL: time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	return state, runRecord, root, acquired.Lease, capabilities
}

func bindCurrentCommandRuntimeTestScope(t *testing.T, ctx context.Context, state *store.SQLiteStore, service *CommandRuntimeService, value toolgateway.CommandRuntimeContext) toolgateway.CommandRuntimeContext {
	t.Helper()
	permission, err := state.GetRunExecutionPermission(ctx, value.RunID)
	if err != nil {
		t.Fatal(err)
	}
	mode, err := state.GetRunMode(ctx, value.RunID)
	if err != nil {
		t.Fatal(err)
	}
	var live bool
	value.PermissionSnapshotID, value.PermissionGeneration, value.PermissionRuntimeEpoch, value.RunAuthorizationFence, live = bindAgentCodeRuntime(service.capabilities, permission)
	if !live {
		t.Fatal("fixture has no current command authority")
	}
	value.PermissionMode, value.PermissionRevision = permission.Mode, permission.Revision
	value.Surface, value.Phase, value.Profile, value.ModeRevision, value.Role = mode.Surface, mode.Phase, mode.Profile, mode.Revision, domain.AgentRoleRoot
	return value
}
