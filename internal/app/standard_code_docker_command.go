package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"cyberagent-workbench/internal/application"
	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/repository"
	"cyberagent-workbench/internal/sandbox"
	"cyberagent-workbench/internal/standardcode"
	"cyberagent-workbench/internal/workspace"
)

const standardCodeDockerImageEnvironment = "CYBERAGENT_STANDARD_CODE_DOCKER_IMAGE_DIGEST"

func (a *App) runStandardCode(ctx context.Context, args []string) error {
	if len(args) == 0 {
		return errors.New("usage: cyberagent run standard-code preset|pause-and-configure|docker-readiness|docker-prepare|docker-execute|docker-cancel|docker-recover")
	}
	switch args[0] {
	case "preset", "pause-and-configure":
		return a.runStandardCodePreset(ctx, args[0], args[1:])
	case "docker-readiness", "docker-prepare", "docker-execute":
		return a.runStandardCodeBoundCommand(ctx, args[0], args[1:])
	case "docker-cancel":
		return a.runStandardCodeCancel(ctx, args[1:])
	case "docker-recover":
		return a.runStandardCodeRecover(ctx, args[1:])
	default:
		return fmt.Errorf("unknown Standard Code subcommand %q", args[0])
	}
}

func (a *App) runStandardCodePreset(ctx context.Context, command string,
	args []string,
) error {
	fs := newFlagSet("run standard-code "+command, a.errOut)
	workspaceName := fs.String("workspace", "", "registered source Workspace name")
	goal := fs.String("goal", "", "goal for a newly created Code Run")
	backendIntent := fs.String("backend", "auto", "backend intent: auto, local, or docker")
	operationKey := fs.String("operation-key", "", "stable operator-owned operation key")
	requestedBy := fs.String("operator", "cli_operator", "operator identity")
	confirmTrust := fs.Bool("confirm-workspace-trust", false,
		"confirm the exact reviewed source Workspace state")
	expectedTrust := fs.String("expected-trust-digest", "",
		"exact trust digest returned by the review call")
	permissionControl := fs.Bool("enable-permission-control", false,
		"enable Workspace Access and Approval evaluation")
	workspaceSandbox := fs.Bool("enable-workspace-sandbox", false,
		"enable and probe the process-local Workspace Sandbox gate")
	dockerExecution := fs.Bool("enable-docker-execution", false,
		"probe the fixed Standard Code Docker backend")
	jsonOutput := fs.Bool("json", false, "emit the strict preset result as JSON")
	if err := fs.Parse(reorderFlags(args, map[string]bool{
		"workspace": true, "goal": true, "backend": true,
		"operation-key": true, "operator": true,
		"confirm-workspace-trust": false, "expected-trust-digest": true,
		"enable-permission-control": false, "enable-workspace-sandbox": false,
		"enable-docker-execution": false, "json": false,
	})); err != nil {
		return err
	}
	targetInvalid := fs.NArg() > 1 ||
		(command == "pause-and-configure" && fs.NArg() != 1) ||
		(command == "preset" && ((fs.NArg() == 0 &&
			(strings.TrimSpace(*workspaceName) == "" || strings.TrimSpace(*goal) == "")) ||
			(fs.NArg() == 1 && (strings.TrimSpace(*workspaceName) != "" ||
				strings.TrimSpace(*goal) != ""))))
	if targetInvalid || strings.TrimSpace(*operationKey) == "" ||
		!*permissionControl || !*workspaceSandbox {
		return errors.New("usage: cyberagent run standard-code " + command +
			" [<run-id>] --operation-key <key> [--workspace <name> --goal <text>] [--backend auto|local|docker] --enable-permission-control --enable-workspace-sandbox [--enable-docker-execution] [--confirm-workspace-trust --expected-trust-digest <sha256>] [--json]")
	}
	runID := ""
	if fs.NArg() == 1 {
		runID = fs.Arg(0)
	}
	workspaceID := ""
	if strings.TrimSpace(*workspaceName) != "" {
		record, err := a.store.GetWorkspaceByName(ctx,
			workspace.Slug(strings.TrimSpace(*workspaceName)))
		if err != nil {
			return err
		}
		workspaceID = record.ID
	}
	capabilities := domain.ExecutionPermissionRuntimeCapabilities{
		WorkspaceSandboxEnabled: true, OperatorApprovalEnabled: true,
	}
	runtime := application.CapabilityReadinessRuntime{
		RunControlEnabled: true, RunExecutionEnabled: true,
		ExecutionPermissionControlEnabled: true, StandardCodePresetEnabled: true,
		ExecutionPermissionCapabilities: capabilities,
		DockerStartupGateEnabled:        *dockerExecution,
		DockerAvailable:                 *dockerExecution,
	}
	localBackend, localReadiness, err := openLocalSandbox(ctx, true)
	if err != nil {
		return err
	}
	if localBackend != nil {
		defer localBackend.Close()
		runtime, err = runtime.WithLocalSandboxReadiness(localReadiness)
		if err != nil {
			return err
		}
		if localReadiness.Ready {
			localExecutor, executorErr :=
				application.NewLocalSandboxCommandRuntimeExecutor(a.store,
					localBackend, localReadiness)
			if executorErr != nil {
				return executorErr
			}
			runtime.CommandRuntimeAdapters = append(runtime.CommandRuntimeAdapters,
				localExecutor.Identity())
		}
	}
	if readiness, found, readinessErr := a.standardCodeCapabilityDockerReadiness(ctx,
		*dockerExecution); readinessErr != nil {
		return readinessErr
	} else if found {
		runtime.DockerReadiness = &readiness
		runtime.DockerAvailable = readiness.DaemonReachable
		runtime.DockerBackendReady = readiness.Ready
		if standardDocker, serviceErr := a.newStandardCodeDockerService(
			*dockerExecution, true, true); serviceErr != nil {
			return serviceErr
		} else if standardDocker != nil {
			dockerExecutor, executorErr :=
				application.NewDockerSandboxCommandRuntimeExecutor(standardDocker)
			if executorErr != nil {
				return executorErr
			}
			runtime.CommandRuntimeAdapters = append(runtime.CommandRuntimeAdapters,
				dockerExecutor.Identity())
		}
	}
	drydockExecutor, err := repository.NewRunWorktreeExecutor(
		filepath.Join(a.home, "drydocks"))
	if err != nil {
		return err
	}
	drydocks, err := application.NewRunWorktreeService(a.store, drydockExecutor)
	if err != nil {
		return err
	}
	service, err := application.NewStandardCodePresetService(a.store, drydocks, runtime)
	if err != nil {
		return err
	}
	action := string(domain.StandardCodePresetConfigure)
	if command == "pause-and-configure" {
		action = string(domain.StandardCodePresetPauseAndConfigure)
	}
	result, err := service.Configure(ctx, application.ConfigureStandardCodeRequest{
		Version: domain.StandardCodePresetProtocolVersion, RunID: runID,
		WorkspaceID: workspaceID, Goal: *goal, BackendIntent: *backendIntent,
		Action: action, OperationKey: *operationKey, RequestedBy: *requestedBy,
		ConfirmWorkspaceTrust: *confirmTrust, ExpectedTrustDigest: *expectedTrust,
	})
	if err != nil {
		return err
	}
	if *jsonOutput {
		return json.NewEncoder(a.out).Encode(standardCodePresetCLIView(result))
	}
	fmt.Fprintf(a.out,
		"protocol: %s\nstatus: %s\nrun: %s\nworkspace: %s\naction: %s\nbackend_intent: %s\nselected_backend: %s\nselection_reason: %s\ndrydock_ready: %t\nnetwork: %s\ncredentials: %s\ntrust_required: %t\ntrust_digest: %s\nblocked_by: %s\nnext_steps: %s\nreplayed: %t\ncapability_grant: false\n",
		result.ProtocolVersion, result.Status, result.RunID, result.WorkspaceID,
		result.Action, result.BackendIntent, result.SelectedBackend,
		result.SelectionReason, result.DrydockReady, result.Network,
		result.Credentials, result.TrustRequired, result.TrustDigest,
		joinStandardCodeBlockers(result.BlockedBy), joinStandardCodeNextSteps(result.NextSteps),
		result.Replayed)
	if result.Mode != nil && result.Profile != nil && result.Interaction != nil &&
		result.Permission != nil && result.BrowserCDP != nil {
		fmt.Fprintf(a.out,
			"surface: %s\nphase: %s\nprofile: %s\ninteraction: %s\npermission: %s\nbrowser_cdp: %s\n",
			result.Mode.Surface, result.Mode.Phase, result.Profile.Profile,
			result.Interaction.Mode, result.Permission.Mode, result.BrowserCDP.Mode)
	}
	return nil
}

func standardCodePresetCLIView(result application.StandardCodePresetResult) any {
	view := map[string]any{"protocol_version": result.ProtocolVersion,
		"status": result.Status, "run_id": result.RunID,
		"workspace_id": result.WorkspaceID, "action": result.Action,
		"backend_intent":   result.BackendIntent,
		"selected_backend": result.SelectedBackend,
		"selection_reason": result.SelectionReason,
		"local_readiness":  result.LocalReadiness,
		"docker_readiness": result.DockerReadiness,
		"blocked_by":       result.BlockedBy, "next_steps": result.NextSteps,
		"trust_required": result.TrustRequired, "trust_digest": result.TrustDigest,
		"drydock_ready": result.DrydockReady, "network": result.Network,
		"credentials": result.Credentials, "replayed": result.Replayed,
		"capability_grant": false}
	if result.Mode != nil && result.Profile != nil && result.Interaction != nil &&
		result.Permission != nil && result.BrowserCDP != nil {
		view["surface"], view["phase"] = result.Mode.Surface, result.Mode.Phase
		view["profile"], view["interaction"] = result.Profile.Profile,
			result.Interaction.Mode
		view["permission"], view["browser_cdp"] = result.Permission.Mode,
			result.BrowserCDP.Mode
	}
	return view
}

func joinStandardCodeBlockers(values []application.CapabilityReadinessBlocker) string {
	parts := make([]string, len(values))
	for index, value := range values {
		parts[index] = string(value)
	}
	return strings.Join(parts, ",")
}

func joinStandardCodeNextSteps(values []application.StandardCodeNextStep) string {
	parts := make([]string, len(values))
	for index, value := range values {
		parts[index] = string(value)
	}
	return strings.Join(parts, ",")
}

func (a *App) runStandardCodeBoundCommand(ctx context.Context, action string,
	args []string,
) error {
	fs := newFlagSet("run standard-code "+action, a.errOut)
	generation := fs.Int64("generation", 0, "exact current Drydock generation")
	checkpoint := fs.String("checkpoint", "", "exact current Drydock Checkpoint identity")
	toolchain := fs.String("toolchain", "", "fixed offline toolchain: go, node, python, or rust")
	workingDirectory := fs.String("cwd", ".", "normalized path relative to the Drydock root")
	timeout := fs.Duration("timeout", 10*time.Minute, "bounded command timeout")
	purpose := fs.String("purpose", "", "operator-visible command purpose")
	operationKey := fs.String("operation-key", "", "stable operation key")
	requestedBy := fs.String("operator", "cli_operator", "operator identity")
	preparationID := fs.String("preparation", "", "exact prepared Standard Code intent")
	approvalID := fs.String("approval", "", "approved exact per-call approval")
	dockerEnabled := fs.Bool("enable-docker-execution", false,
		"enable the process-local fixed Docker execution capability")
	permissionControl := fs.Bool("enable-permission-control", false,
		"enable operator approval control")
	workspaceSandbox := fs.Bool("enable-workspace-sandbox", false,
		"enable the process-local Workspace sandbox capability")
	var arguments multiStringFlag
	fs.Var(&arguments, "arg", "command argument (repeatable; never interpreted by a shell)")
	shape := map[string]bool{"generation": true, "checkpoint": true,
		"toolchain": true, "cwd": true, "timeout": true, "purpose": true,
		"operation-key": true, "operator": true, "preparation": true,
		"approval": true, "arg": true, "enable-docker-execution": false,
		"enable-permission-control": false, "enable-workspace-sandbox": false}
	if err := fs.Parse(reorderFlags(args, shape)); err != nil {
		return err
	}
	if fs.NArg() != 1 || *generation < 1 || strings.TrimSpace(*checkpoint) == "" ||
		strings.TrimSpace(*toolchain) == "" || strings.TrimSpace(*purpose) == "" ||
		*timeout < time.Second || *timeout%time.Second != 0 ||
		!*permissionControl || !*workspaceSandbox ||
		(action == "docker-execute" && !*dockerEnabled) {
		return errors.New("usage: cyberagent run standard-code " + action +
			" <run-id> --generation <n> --checkpoint <id> --toolchain go|node|python|rust --purpose <text> [--arg <value> ...] [--cwd <relative-path>] [--timeout <duration>] --enable-permission-control --enable-workspace-sandbox [--enable-docker-execution]")
	}
	command := standardcode.Command{ProtocolVersion: standardcode.CommandProtocolVersion,
		Toolchain: strings.TrimSpace(*toolchain), Arguments: arguments.values,
		WorkingDirectory: strings.TrimSpace(*workingDirectory),
		TimeoutSeconds:   int(timeout.Seconds()), Purpose: strings.TrimSpace(*purpose)}
	service, err := a.newStandardCodeDockerService(*dockerEnabled,
		*permissionControl, *workspaceSandbox)
	if err != nil {
		return err
	}
	encoder := json.NewEncoder(a.out)
	encoder.SetIndent("", "  ")
	switch action {
	case "docker-readiness":
		value, err := service.Readiness(ctx, application.StandardCodeDockerReadinessRequest{
			RunID: fs.Arg(0), ExpectedGeneration: *generation,
			ExpectedCheckpoint: *checkpoint, Command: command})
		if err != nil {
			return err
		}
		return encoder.Encode(value)
	case "docker-prepare":
		if strings.TrimSpace(*operationKey) == "" {
			return errors.New("docker-prepare requires --operation-key")
		}
		value, err := service.Prepare(ctx, application.StandardCodeDockerPrepareRequest{
			RunID: fs.Arg(0), ExpectedGeneration: *generation,
			ExpectedCheckpoint: *checkpoint, OperationKey: *operationKey,
			RequestedBy: *requestedBy, Command: command})
		if err != nil {
			return err
		}
		return encoder.Encode(value)
	case "docker-execute":
		if strings.TrimSpace(*operationKey) == "" ||
			strings.TrimSpace(*preparationID) == "" || strings.TrimSpace(*approvalID) == "" {
			return errors.New("docker-execute requires --preparation, --approval, and --operation-key")
		}
		if _, err := service.RecoverStartup(ctx); err != nil {
			return err
		}
		value, err := service.Execute(ctx, application.StandardCodeDockerExecuteRequest{
			RunID: fs.Arg(0), ExpectedGeneration: *generation,
			ExpectedCheckpoint: *checkpoint, PreparationID: *preparationID,
			ApprovalID: *approvalID, OperationKey: *operationKey,
			RequestedBy: *requestedBy, Command: command})
		if err != nil {
			return err
		}
		return encoder.Encode(value)
	default:
		return errors.New("unsupported Standard Code action")
	}
}

func (a *App) runStandardCodeCancel(ctx context.Context, args []string) error {
	fs := newFlagSet("run standard-code docker-cancel", a.errOut)
	operationKey := fs.String("operation-key", "", "stable cancellation operation key")
	requestedBy := fs.String("operator", "cli_operator", "operator identity")
	if err := fs.Parse(reorderFlags(args, map[string]bool{
		"operation-key": true, "operator": true})); err != nil {
		return err
	}
	if fs.NArg() != 1 || strings.TrimSpace(*operationKey) == "" {
		return errors.New("usage: cyberagent run standard-code docker-cancel <admission-id> --operation-key <key> [--operator <id>]")
	}
	service, err := a.newStandardCodeDockerService(false, false, false)
	if err != nil {
		return err
	}
	value, err := service.Cancel(ctx, application.StandardCodeDockerCancelRequest{
		AdmissionID: fs.Arg(0), OperationKey: *operationKey, RequestedBy: *requestedBy})
	if err != nil {
		return err
	}
	encoder := json.NewEncoder(a.out)
	encoder.SetIndent("", "  ")
	return encoder.Encode(value)
}

func (a *App) runStandardCodeRecover(ctx context.Context, args []string) error {
	fs := newFlagSet("run standard-code docker-recover", a.errOut)
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		return errors.New("usage: cyberagent run standard-code docker-recover")
	}
	service, err := a.newStandardCodeDockerService(false, false, false)
	if err != nil {
		return err
	}
	values, err := service.RecoverStartup(ctx)
	if err != nil {
		return err
	}
	encoder := json.NewEncoder(a.out)
	encoder.SetIndent("", "  ")
	return encoder.Encode(values)
}

func (a *App) newStandardCodeDockerService(dockerEnabled, permissionControl,
	workspaceSandbox bool, runtimeCapabilities ...domain.ExecutionPermissionRuntimeCapabilities,
) (*application.StandardCodeDockerService, error) {
	imageDigest := strings.TrimSpace(os.Getenv(standardCodeDockerImageEnvironment))
	if !sandbox.ValidOCIImageDigest(imageDigest) {
		return nil, fmt.Errorf("%s must name one pre-existing exact OCI sha256 image digest",
			standardCodeDockerImageEnvironment)
	}
	executor, err := repository.NewRunWorktreeExecutor(filepath.Join(a.home, "drydocks"))
	if err != nil {
		return nil, err
	}
	drydocks, err := application.NewRunWorktreeService(a.store, executor)
	if err != nil {
		return nil, err
	}
	manifests := application.NewSandboxManifestService(a.store, a.checker)
	if a.dockerObserver != nil {
		manifests.WithDockerProductionObserver(a.dockerObserver)
	}
	readiness := a.dockerReadinessProbe
	if readiness == nil {
		local, probeErr := sandbox.NewLocalDockerReadinessProbe()
		if probeErr != nil {
			return nil, probeErr
		}
		readiness = local
	}
	lifecycle := a.dockerLifecycle
	if lifecycle == nil {
		lifecycle = sandbox.NewLocalDockerContainerLifecycleTransport()
	}
	ioTransport := a.dockerIO
	if ioTransport == nil {
		ioTransport = sandbox.NewLocalDockerContainerIOTransport()
	}
	stagingRoot := filepath.Join(a.home, dockerSandboxStagingDirectory)
	if err := os.MkdirAll(stagingRoot, 0o700); err != nil {
		return nil, err
	}
	permissionCapabilities := domain.ExecutionPermissionRuntimeCapabilities{
		WorkspaceSandboxEnabled: workspaceSandbox,
		OperatorApprovalEnabled: permissionControl}
	if len(runtimeCapabilities) == 1 {
		permissionCapabilities = runtimeCapabilities[0]
	}
	docker, err := application.NewDockerSandboxService(a.store, readiness, a.checker,
		sandbox.DockerRuntimeCapabilities{Enabled: dockerEnabled}, permissionCapabilities,
		application.WithDockerSandboxExecution(lifecycle, ioTransport, stagingRoot,
			sandbox.DefaultDockerContainerLifecycleLeaseTTL),
		application.WithDockerStandardCode(drydocks, imageDigest))
	if err != nil {
		return nil, err
	}
	return application.NewStandardCodeDockerService(a.store, drydocks, manifests,
		docker, imageDigest)
}
