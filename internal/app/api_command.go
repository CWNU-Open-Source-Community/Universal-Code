package app

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"

	"cyberagent-workbench/internal/apperror"
	"cyberagent-workbench/internal/application"
	"cyberagent-workbench/internal/browserruntime"
	"cyberagent-workbench/internal/commandruntimeadapter"
	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/httpapi"
	"cyberagent-workbench/internal/idgen"
	"cyberagent-workbench/internal/plugins"
	"cyberagent-workbench/internal/repository"
	"cyberagent-workbench/internal/runner"
	"cyberagent-workbench/internal/sandbox"
	"cyberagent-workbench/internal/scheduler"
	"cyberagent-workbench/internal/skills"
	"cyberagent-workbench/internal/webevidence"
	"cyberagent-workbench/internal/webui"
)

const apiTokenEnvironment = "CYBERAGENT_API_TOKEN"

const apiControlTokenEnvironment = "CYBERAGENT_API_CONTROL_TOKEN"

func (a *App) apiCommand(ctx context.Context, args []string) error {
	if len(args) == 0 {
		return errors.New("API subcommand is required")
	}
	switch args[0] {
	case "serve":
		return a.apiServeCommand(ctx, args[1:])
	case "openapi":
		return a.apiOpenAPICommand(args[1:])
	default:
		return fmt.Errorf("unknown API subcommand %q", args[0])
	}
}

func (a *App) apiServeCommand(ctx context.Context, args []string) (resultErr error) {
	fs := newFlagSet("api serve", a.errOut)
	listenAddress := fs.String("listen", httpapi.DefaultListenAddress, "loopback listen address")
	uiDirectory := fs.String("ui-dir", "", "optional built Web UI directory")
	workspaceImport := fs.Bool("enable-workspace-import", false,
		"allow the control-token operator to register an existing server-side directory")
	fileEditProposals := fs.Bool("enable-file-edit-proposals", false,
		"enable Go-issued interactive FileEdit proposal sources")
	providerCredentials := fs.Bool("enable-provider-credentials", false,
		"enable OS-owned Provider credential changes")
	wakeWorker := fs.Bool("enable-wake-worker", false,
		"enable the bounded single-owner Run wake worker")
	scheduledJobWorker := fs.Bool("enable-scheduled-job-worker", false,
		"enable the process-local single-concurrency scheduled job worker")
	permissionControl := fs.Bool("enable-permission-control", false,
		"enable operator-selected Run execution permissions")
	workspaceSandbox := fs.Bool("enable-workspace-sandbox", false,
		"probe and enable the verified process-local Workspace Sandbox")
	dangerFullAccess := fs.Bool("enable-danger-full-access", false,
		"enable danger-full-access permission selection")
	browserCDPControl := fs.Bool("enable-browser-cdp-control", false,
		"enable operator-selected browser CDP permissions")
	fullCDPDebug := fs.Bool("enable-full-cdp-debug", false,
		"enable highly sensitive complete CDP debugging selection")
	dockerExecution := fs.Bool("enable-docker-execution", false,
		"enable the process-local Docker Sandbox execution capability")
	batchValidationExecution := fs.Bool("enable-batch-validation-execution", false,
		"enable fixed offline go/npm checks for confirmed batch deliveries")
	codeIntelConfig := fs.String("code-intel-config", "",
		"absolute operator-reviewed code-intel config")
	gitAdvancedEnabled := fs.Bool("enable-git-advanced", false,
		"enable approval-gated git-advanced.v1 mutations")
	githubReviewEnabled := fs.Bool("enable-github-review", false,
		"enable the approval-gated GitHub Review Provider")
	gitWorktreeRoot := fs.String("git-worktree-root", "",
		"product-managed worktree root; defaults below CYBERAGENT_HOME")
	if err := fs.Parse(reorderFlags(args, map[string]bool{"listen": true, "ui-dir": true,
		"enable-workspace-import":    false,
		"code-intel-config":          true,
		"enable-file-edit-proposals": false, "enable-provider-credentials": false,
		"enable-wake-worker": false, "enable-scheduled-job-worker": false,
		"enable-permission-control": false,
		"enable-workspace-sandbox":  false,
		"enable-danger-full-access": false, "enable-browser-cdp-control": false, "enable-full-cdp-debug": false,
		"enable-docker-execution": false, "enable-batch-validation-execution": false,
		"enable-git-advanced": false, "enable-github-review": false,
		"git-worktree-root": true})); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		return errors.New("usage: cyberagent api serve [--listen <loopback-host:port>] [--ui-dir <built-web-directory>] [explicit capability flags]")
	}
	if strings.TrimSpace(*codeIntelConfig) != "" {
		if a.codeIntelConfigLoaded || a.codeIntel != nil {
			return apperror.New(apperror.CodeFailedPrecondition,
				"code-intel config was already initialized")
		}
		absolute, err := filepath.Abs(strings.TrimSpace(*codeIntelConfig))
		if err != nil {
			return err
		}
		a.codeIntelConfigPath = filepath.Clean(absolute)
	}

	accessToken := os.Getenv(apiTokenEnvironment)
	controlToken := os.Getenv(apiControlTokenEnvironment)
	if *workspaceImport && controlToken == "" {
		return apperror.New(apperror.CodeInvalidArgument,
			"--enable-workspace-import requires CYBERAGENT_API_CONTROL_TOKEN")
	}
	permissionCapabilities := newAPIExecutionPermissionCapabilities(
		*permissionControl, *dangerFullAccess)
	if *workspaceSandbox && !*permissionControl {
		return apperror.New(apperror.CodeInvalidArgument,
			"--enable-workspace-sandbox requires --enable-permission-control")
	}
	var localReadinessRuntime *application.CapabilityReadinessRuntime
	var localSandboxBackend sandbox.LocalBackend
	var localSandboxReadiness *sandbox.LocalReadiness
	if *workspaceSandbox {
		backend, readiness, err := openLocalSandbox(ctx, true)
		if err != nil {
			return err
		}
		localSandboxBackend = backend
		localSandboxReadiness = &readiness
		defer localSandboxBackend.Close()
		runtime := application.CapabilityReadinessRuntime{
			RunControlEnabled:                  controlToken != "",
			ExecutionPermissionControlEnabled:  controlToken != "" && *permissionControl,
			BrowserCDPPermissionControlEnabled: controlToken != "" && *browserCDPControl,
			ExecutionPermissionCapabilities:    permissionCapabilities,
			BrowserCDPPermissionCapabilities: domain.BrowserCDPPermissionRuntimeCapabilities{
				ControlEnabled: *browserCDPControl, FullDebugEnabled: *fullCDPDebug,
			},
			DockerStartupGateEnabled: *dockerExecution,
			DockerAvailable:          *dockerExecution,
		}
		runtime, err = runtime.WithLocalSandboxReadiness(readiness)
		if err != nil {
			return err
		}
		permissionCapabilities = runtime.ExecutionPermissionCapabilities
		localReadinessRuntime = &runtime
	}
	// Local is a platform-specific backend. A current, fixed Docker image may
	// independently supply the requested generic Workspace Sandbox startup gate.
	if *workspaceSandbox && *dockerExecution {
		proof, found, err := a.standardCodeCapabilityDockerReadiness(ctx, true)
		if err != nil {
			return err
		}
		if found && proof.Validate() == nil && proof.ReadyAt(time.Now().UTC()) {
			permissionCapabilities.WorkspaceSandboxEnabled = true
			if localReadinessRuntime != nil {
				localReadinessRuntime.ExecutionPermissionCapabilities = permissionCapabilities
			}
		}
	}
	if err := permissionCapabilities.Validate(); err != nil {
		return apperror.Wrap(apperror.CodeInvalidArgument,
			err.Error(), err)
	}
	browserCDPCapabilities := domain.BrowserCDPPermissionRuntimeCapabilities{
		ControlEnabled: *browserCDPControl, FullDebugEnabled: *fullCDPDebug,
	}
	if err := browserCDPCapabilities.Validate(); err != nil {
		return apperror.Wrap(apperror.CodeInvalidArgument, err.Error(), err)
	}
	if browserCDPCapabilities.FullDebugEnabled &&
		!permissionCapabilities.DangerFullAccessEnabled {
		return apperror.New(apperror.CodeInvalidArgument,
			"full CDP requires danger Full Access execution capability")
	}

	if *dockerExecution && !permissionCapabilities.OperatorApprovalEnabled {
		return apperror.New(apperror.CodeInvalidArgument,
			"--enable-docker-execution requires --enable-permission-control")
	}
	if *gitAdvancedEnabled && !permissionCapabilities.OperatorApprovalEnabled {
		return apperror.New(apperror.CodeInvalidArgument,
			"--enable-git-advanced requires --enable-permission-control")
	}
	if *githubReviewEnabled && !permissionCapabilities.OperatorApprovalEnabled {
		return apperror.New(apperror.CodeInvalidArgument,
			"--enable-github-review requires --enable-permission-control")
	}
	if strings.TrimSpace(*gitWorktreeRoot) != "" &&
		!*gitAdvancedEnabled && !*githubReviewEnabled {
		return apperror.New(apperror.CodeInvalidArgument,
			"--git-worktree-root requires --enable-git-advanced or --enable-github-review")
	}
	if *batchValidationExecution &&
		(!permissionCapabilities.OperatorApprovalEnabled ||
			!permissionCapabilities.DangerFullAccessEnabled) {
		return apperror.New(apperror.CodeInvalidArgument,
			"--enable-batch-validation-execution requires permission control and danger-full-access")
	}
	if (*fileEditProposals || *providerCredentials || *wakeWorker || *scheduledJobWorker ||
		*permissionControl || *browserCDPControl ||
		*dockerExecution || *batchValidationExecution || *gitAdvancedEnabled ||
		*githubReviewEnabled) && controlToken == "" {
		return apperror.New(apperror.CodeInvalidArgument,
			"interactive proposals, Provider credentials, the wake worker, and execution permission control require CYBERAGENT_API_CONTROL_TOKEN")
	}
	generated := accessToken == ""
	if generated {
		var err error
		accessToken, err = httpapi.GenerateAccessToken()
		if err != nil {
			return err
		}
	}
	var uiBundle *webui.Bundle
	if strings.TrimSpace(*uiDirectory) != "" {
		var err error
		uiBundle, err = webui.LoadDirectory(*uiDirectory)
		if err != nil {
			return apperror.Wrap(apperror.CodeInvalidArgument, "invalid Web UI directory", err)
		}
	}
	if err := a.ensureStore(); err != nil {
		return err
	}
	mcpClient := a.newMCPClientManager()
	if mcpClient == nil {
		return apperror.New(apperror.CodeInternal,
			"initialize MCP Client control service")
	}
	if _, err := mcpClient.ReconcileStartup(ctx); err != nil {
		return apperror.Wrap(apperror.CodeUnavailable,
			"MCP Client startup reconciliation failed", err)
	}
	pluginService, err := plugins.NewService(a.store)
	if err != nil {
		return err
	}
	extensionControl, err := application.NewExtensionControlService(a.store,
		mcpClient, pluginService)
	if err != nil {
		return err
	}
	extensionControl.WithCredentials(a.credentials)
	var codeIntelControl httpapi.CodeIntelController
	if strings.TrimSpace(a.codeIntelConfigPath) == "" {
		service, err := application.OpenCodeIntelControlService(a.store, a.codeIntel,
			filepath.Join(a.home, "code-intel.json"))
		if err != nil {
			return apperror.Wrap(apperror.CodeFailedPrecondition,
				"load managed code-intel configuration", err)
		}
		a.codeIntel = service.Manager()
		codeIntelControl = service
	}
	hookEngine := a.newLifecycleHookEngine()
	workspaceCheckpoints, err := application.NewWorkspaceCheckpointService(a.store,
		permissionCapabilities)
	if err != nil {
		return err
	}
	workspaceCheckpoints.WithLifecycleHooks(hookEngine)
	if _, err := workspaceCheckpoints.Reconcile(ctx); err != nil {
		return apperror.Wrap(apperror.CodeUnavailable,
			"Workspace checkpoint startup reconciliation failed", err)
	}
	var gitAdvancedService *application.GitAdvancedService
	if *gitAdvancedEnabled {
		managedRoot := strings.TrimSpace(*gitWorktreeRoot)
		if managedRoot == "" {
			managedRoot = filepath.Join(a.home, "worktrees")
		}
		gitExecutor, executorErr := repository.NewAdvancedExecutor(managedRoot, true)
		if executorErr != nil {
			return executorErr
		}
		gitAdvancedService, err = application.NewGitAdvancedService(a.store,
			gitExecutor, permissionCapabilities, workspaceCheckpoints)
		if err != nil {
			return err
		}
		if _, err := gitAdvancedService.ReconcileStartup(ctx, 500); err != nil {
			return apperror.Wrap(apperror.CodeUnavailable,
				"Git advanced startup reconciliation failed", err)
		}
	}
	var githubReviewService *application.GitHubReviewService
	if *githubReviewEnabled {
		managedRoot := strings.TrimSpace(*gitWorktreeRoot)
		if managedRoot == "" {
			managedRoot = filepath.Join(a.home, "worktrees")
		}
		gitExecutor, executorErr := repository.NewAdvancedExecutor(managedRoot, true)
		if executorErr != nil {
			return executorErr
		}
		githubReviewService, err = application.NewGitHubReviewService(a.store,
			a.credentials, gitExecutor, permissionCapabilities)
		if err != nil {
			return err
		}
		if _, err := githubReviewService.ReconcileStartup(ctx, 500); err != nil {
			return apperror.Wrap(apperror.CodeUnavailable,
				"GitHub review startup reconciliation failed", err)
		}
	}
	batchDelivery := application.NewBatchDeliveryService(a.store).
		WithHostValidationExecution(*batchValidationExecution, permissionCapabilities)
	var commandRuntimeDrydocks *application.RunWorktreeService
	if controlToken != "" && permissionCapabilities.WorkspaceSandboxEnabled {
		drydockExecutor, executorErr := repository.NewRunWorktreeExecutor(
			filepath.Join(a.home, "drydocks"))
		if executorErr != nil {
			return executorErr
		}
		commandRuntimeDrydocks, err = application.NewRunWorktreeService(a.store,
			drydockExecutor)
		if err != nil {
			return err
		}
		commandRuntimeDrydocks.WithCheckpointService(workspaceCheckpoints)
	}
	var standardCodeDelivery *application.StandardCodeDeliveryService
	var standardCodeDeliveryController httpapi.StandardCodeDeliveryController
	if commandRuntimeDrydocks != nil {
		standardCodeDelivery, err = application.NewStandardCodeDeliveryService(
			a.store, commandRuntimeDrydocks)
		if err != nil {
			return err
		}
		if githubReviewService != nil {
			githubReviewService.WithStandardCodeDelivery(standardCodeDelivery)
		}
	}
	dockerSandbox, standardCodeRuntime, err :=
		a.newDockerSandboxServiceWithStandardCode(*dockerExecution,
			permissionCapabilities, commandRuntimeDrydocks)
	if err != nil {
		return err
	}
	if _, err := dockerSandbox.RecoverStartup(ctx); err != nil {
		return apperror.Wrap(apperror.CodeUnavailable,
			"Docker Sandbox startup recovery failed", err)
	}
	webClient := webevidence.NewSafeHTTPClient()
	providerSearchClient := webevidence.NewProviderSearchHTTPClient()
	var webSearchProvider webevidence.SearchProvider
	if endpoint := strings.TrimSpace(os.Getenv(webSearchEndpointEnvironment)); endpoint != "" {
		webSearchProvider, err = webevidence.NewSearXNGProvider(webClient, endpoint)
		if err != nil {
			return apperror.Wrap(apperror.CodeInvalidArgument,
				"invalid Web search endpoint", err)
		}
	}
	providerSearchResolver, err := application.NewProviderSearchResolver(a.models,
		a.store, a.credentials, webSearchProvider, providerSearchClient)
	if err != nil {
		return apperror.Wrap(apperror.CodeFailedPrecondition,
			"Provider search resolver is unavailable", err)
	}
	webEvidence := webevidence.NewService(a.store, webSearchProvider,
		webevidence.NewFetcher(webClient)).
		WithSourceConnectors(webevidence.NewDefaultSourceConnectors(webClient)...).
		WithSearchProviderResolver(providerSearchResolver)
	lifecycleControl := application.NewRunLifecycleControlService(a.store).
		WithLifecycleHooks(hookEngine)
	webFetchAuthorizationSchedulerEnabled := controlToken != "" &&
		*permissionControl && permissionCapabilities.OperatorApprovalEnabled
	projectHooks := application.NewProjectHooksService(a.store).WithRunWorktree(commandRuntimeDrydocks)
	runtimeDependencies := application.RunRuntimeDependencies{
		ActiveCalls: a.calls, Drydocks: commandRuntimeDrydocks, MCPClient: mcpClient,
		ExecutionCapabilities: permissionCapabilities, LifecycleHooks: hookEngine,
		ProjectHooks: projectHooks,
		WebEvidence:  webEvidence, WebFetchAuthorizationScheduler: webFetchAuthorizationSchedulerEnabled,
		StandardCodeDelivery: standardCodeDelivery, CodeIntel: a.codeIntel,
	}
	if standardCodeDelivery != nil {
		standardCodeDeliveryController = standardCodeDelivery
	}
	commandManager, err := runner.NewPlatformCommandRuntimeManager(a.store,
		idgen.New("command-runtime-owner"))
	if err != nil {
		return err
	}
	commandOptions := application.CommandRuntimeSetOptions{
		HostEnabled:  controlToken != "" && permissionCapabilities.DangerFullAccessEnabled,
		Capabilities: permissionCapabilities, Drydocks: commandRuntimeDrydocks,
		StartupShutdownTimeout: 7 * time.Second,
	}
	if controlToken != "" && localSandboxBackend != nil &&
		localSandboxReadiness != nil && localSandboxReadiness.Ready &&
		commandRuntimeDrydocks != nil {
		commandOptions.LocalBackend = localSandboxBackend
		commandOptions.LocalReadiness = localSandboxReadiness
	}
	if controlToken != "" && standardCodeRuntime != nil &&
		commandRuntimeDrydocks != nil {
		commandOptions.StandardCodeDockerRuntime = standardCodeRuntime
	}
	commandSet, err := application.OpenCommandRuntimeSet(ctx, a.store, commandManager,
		commandOptions)
	if err != nil {
		return err
	}
	defer func() {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 7*time.Second)
		defer cancel()
		_ = commandSet.Shutdown(shutdownCtx)
	}()
	commandRuntime := commandSet.Runtime
	batchDelivery.WithValidationCommandRuntime(commandRuntime)
	gitAdvancedService.WithBisectCommandRuntime(commandRuntime)
	// Reap owned backend processes before Batch reconciliation can remove an
	// expired child/integration worktree that their cleanup intent still binds.
	if _, err := batchDelivery.ReconcileStartup(ctx, 256); err != nil {
		return apperror.Wrap(apperror.CodeUnavailable,
			"batch delivery startup reconciliation failed", err)
	}
	if commandRuntime != nil {
		runtimeDependencies.CommandRuntime = commandRuntime
	}
	var fullCDPSessions *application.FullCDPProductionService
	if controlToken != "" && *fullCDPDebug && *browserCDPControl &&
		*permissionControl && permissionCapabilities.DangerFullAccessEnabled {
		controller, controllerErr := browserruntime.NewPlatformBrowserProcessController()
		if controllerErr != nil {
			return controllerErr
		}
		if controller.FullCDPAvailable() {
			fullCDPSessions, err = application.NewHomeFullCDPProductionService(a.store,
				controller, browserruntime.FullCDPRuntimeCapabilities{StartEnabled: true,
					DisposableProfileEnabled: true, TransportEnabled: true},
				browserCDPCapabilities, permissionCapabilities, a.home)
			if err != nil {
				return err
			}
			runtimeDependencies.BrowserActions = fullCDPSessions
			defer func() {
				shutdownCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
				defer cancel()
				if err := fullCDPSessions.Close(shutdownCtx); err != nil {
					fmt.Fprintln(a.errOut, "Full CDP shutdown:", err)
				}
			}()
		}
	}
	if executor := a.newDockerSandboxProposalExecutor(); executor != nil {
		runtimeDependencies.DockerSandbox = executor
	}
	var agentBrowserController httpapi.AgentBrowserController
	if controlToken != "" {
		agentBrowser := application.NewAgentBrowserService(a.store, application.AgentBrowserOptions{
			HomePath: a.home, Capabilities: permissionCapabilities, Headless: true, ServiceReadiness: commandRuntime,
		})
		runtimeDependencies.AgentBrowser = agentBrowser
		agentBrowserController = agentBrowser
		defer func() {
			shutdownCtx, cancel := context.WithTimeout(context.Background(), 35*time.Second)
			defer cancel()
			resultErr = errors.Join(resultErr, agentBrowser.Shutdown(shutdownCtx))
		}()
	}
	executionControl := application.NewRunExecutionHandoffWithRuntime(a.store, a.router,
		a.checker, runtimeDependencies)
	specialistExecution := application.NewSpecialistExecutionService(a.store, a.router, a.checker).
		WithRunWorktree(commandRuntimeDrydocks).WithCompletionSummary(executionControl).
		WithExecutionRuntime(executionControl)
	batchWorkbench := application.NewBatchDeliveryWorkbenchService(batchDelivery).
		WithWorker(application.NewBatchDeliveryModelWorker(batchDelivery, a.store, a.router, a.checker).WithNativeTools().WithExecutionRuntime(executionControl))
	planDeliveryControl := application.NewPlanDeliveryControlService(a.store)
	approvalControl := application.NewApprovalControlService(a.store,
		a.newToolGateway().WithAgentCodeWorkspaceResolver(
			application.NewAgentCodeWorkspaceResolver(a.store, commandRuntimeDrydocks)), a.checker)
	modelControl := application.NewModelControlService(a.models, a.store)
	threadModelRoutes := application.NewThreadModelRouteService(a.store, a.models)
	providerSearchReadiness := application.NewProviderSearchReadinessService(
		a.store, providerSearchResolver)
	providerDefinitionControl, err := application.NewProviderDefinitionService(a.store, a.models)
	if err != nil {
		return err
	}
	providerDefinitionControl.WithModelDiscoveryCredentials(a.credentials)
	providerCredentialControl := application.NewProviderCredentialService(a.credentials).
		WithRegistryReload(a.models, a.store)
	fileEditReview := application.NewFileEditReviewService(a.store).WithRunWorktree(commandRuntimeDrydocks)
	fileEditProposal := application.NewFileEditProposalService(a.store, a.checker).WithRunWorktree(commandRuntimeDrydocks)
	fileEditApply := application.NewFileEditApplyService(a.store, a.checker,
		workspaceCheckpoints).WithRunWorktree(commandRuntimeDrydocks)
	runWakeControl := application.NewRunWakeControlService(a.store)
	runWakeExecution := application.NewForegroundRunWakeConsumer(a.store,
		executionControl)
	var worker *application.RunWakeWorker
	if *wakeWorker {
		createdWorker, workerErr := application.NewRunWakeWorker(
			application.NewRunWakeCoordinator(a.store), runWakeExecution,
			application.RunWakeWorkerConfig{OnError: func(runErr error) {
				fmt.Fprintln(a.errOut, "wake-worker:", runErr)
			}})
		if workerErr != nil {
			return workerErr
		}
		worker = createdWorker
	}
	var workerHealth httpapi.RunWakeWorkerHealthSource
	if worker != nil {
		workerHealth = worker
	}
	scheduledJobs := application.NewScheduledJobService(a.store)
	var scheduleWorker *scheduler.Worker
	if *scheduledJobWorker {
		scheduleWorker, err = scheduler.NewWorker(scheduledJobs, scheduler.WorkerConfig{
			OnError: func(runErr error) {
				fmt.Fprintln(a.errOut, "scheduled-job-worker:", runErr)
			},
		})
		if err != nil {
			return err
		}
	}
	var scheduleWorkerHealth httpapi.ScheduledJobWorkerHealthSource
	if scheduleWorker != nil {
		scheduleWorkerHealth = scheduledAPIWorkerHealth{scheduleWorker}
	}
	builtinSkills, err := skills.BuiltinRegistry()
	if err != nil {
		return err
	}
	skillObjects, err := skills.NewLocalPackageObjectStore(a.home)
	if err != nil {
		return err
	}
	skillInstallation := application.NewSkillPackageRegistryService(a.store,
		skillObjects, builtinSkills)
	controlledCommandProposals := application.NewControlledCommandHistory(a.store)
	projectPreparation, err := application.NewProjectPreparationService(a.store, commandRuntime, commandRuntimeDrydocks, a.home)
	if err != nil {
		return err
	}
	defer projectPreparation.Close()
	hostCommandProposalControl := application.NewHostCommandHistory(a.store)
	embeddedAnalyzerExecution := application.NewEmbeddedAnalyzerExecutionService(a.store)
	installedCommandRuntimeAdapters := []commandruntimeadapter.Identity{}
	if commandRuntime != nil {
		installedCommandRuntimeAdapters = commandRuntime.InstalledCommandRuntimeAdapters()
	}
	readinessRuntime := application.CapabilityReadinessRuntime{
		RunControlEnabled:                  controlToken != "",
		RunExecutionEnabled:                controlToken != "",
		ExecutionPermissionControlEnabled:  controlToken != "" && *permissionControl,
		BrowserCDPPermissionControlEnabled: controlToken != "" && *browserCDPControl,
		ExecutionPermissionCapabilities:    permissionCapabilities,
		BrowserCDPPermissionCapabilities:   browserCDPCapabilities,
		LocalSandboxInstalled:              localSandboxReadiness != nil && localSandboxReadiness.Ready,
		DockerStartupGateEnabled:           *dockerExecution,
		DockerAvailable:                    *dockerExecution,
		CommandRuntimeAdapters:             installedCommandRuntimeAdapters,
	}
	if localReadinessRuntime != nil {
		readinessRuntime = *localReadinessRuntime
		readinessRuntime.RunExecutionEnabled = controlToken != ""
		readinessRuntime.CommandRuntimeAdapters = installedCommandRuntimeAdapters
	}
	if standardCodeRuntime != nil {
		if dockerReadiness, found, readinessErr :=
			a.standardCodeCapabilityDockerReadiness(ctx, *dockerExecution); readinessErr != nil {
			return readinessErr
		} else if found {
			readinessRuntime.DockerReadiness = &dockerReadiness
			readinessRuntime.DockerAvailable = dockerReadiness.DaemonReachable
			readinessRuntime.DockerBackendReady = dockerReadiness.Ready
		}
	}
	var standardCodePreset *application.StandardCodePresetService
	if controlToken != "" && *permissionControl && commandRuntimeDrydocks != nil {
		readinessRuntime.StandardCodePresetEnabled = true
		standardCodePreset, err = application.NewStandardCodePresetService(a.store,
			commandRuntimeDrydocks, readinessRuntime)
		if err != nil {
			return err
		}
	}
	localReadinessRuntime = &readinessRuntime
	threadTurnControl := application.NewThreadTurnServiceWithExecutionCapabilities(
		a.store, lifecycleControl, executionControl, permissionCapabilities).
		WithModelRouteRegistry(a.models).
		WithLifecycleHooks(hookEngine)
	var workspaceImporter httpapi.WorkspaceImporter
	if *workspaceImport {
		manager, err := a.workspaceManager()
		if err != nil {
			return err
		}
		workspaceImporter = manager
	}
	var threadGit *application.ThreadGitService
	if *gitAdvancedEnabled || *githubReviewEnabled {
		localGit, gitErr := repository.NewMutationExecutor()
		if gitErr != nil {
			return gitErr
		}
		remoteGit, gitErr := repository.NewRemoteExecutor(a.credentials)
		if gitErr != nil {
			return gitErr
		}
		threadGit = application.NewThreadGitService(a.store, localGit, remoteGit,
			workspaceCheckpoints, commandRuntimeDrydocks, permissionCapabilities).WithAdvanced(gitAdvancedService)
	}
	var prFollowupWorker *application.PRFollowupService
	workflowControl := application.NewWorkflowService(a.store, specialistExecution).WithBatchDelivery(batchWorkbench)
	var prFollowupController httpapi.PRFollowupController
	if *githubReviewEnabled && controlToken != "" && *scheduledJobWorker {
		prFollowupWorker, err = application.NewPRFollowupService(a.store, githubReviewService, threadGit, threadTurnControl)
		if err != nil {
			return err
		}
		prFollowupController = prFollowupWorker
	}
	api, err := httpapi.New(a.store, httpapi.Config{
		AccessToken: accessToken, ControlToken: controlToken,
		WorkspaceImportEnabled: *workspaceImport, WorkspaceImporter: workspaceImporter,
		RunControlEnabled: controlToken != "", RunCreationEnabled: controlToken != "",
		StandardCodePresetEnabled:             standardCodePreset != nil,
		SessionMessageEnabled:                 controlToken != "",
		SessionSteeringControlEnabled:         controlToken != "",
		RunLifecycleEnabled:                   controlToken != "",
		RunExecutionEnabled:                   controlToken != "",
		PlanDeliveryControlEnabled:            controlToken != "",
		ApprovalControlEnabled:                controlToken != "",
		WebFetchAuthorizationSchedulerEnabled: webFetchAuthorizationSchedulerEnabled,
		ModelControlEnabled:                   controlToken != "",
		ProviderDefinitionEnabled:             controlToken != "",
		ProviderCredentialEnabled:             *providerCredentials,
		FileEditReviewEnabled:                 controlToken != "",
		FileEditProposalEnabled:               *fileEditProposals,
		RunWakeControlEnabled:                 controlToken != "",
		FileEditApplyEnabled:                  controlToken != "",
		RunWakeExecutionEnabled:               controlToken != "",
		RunWakeWorkerEnabled:                  *wakeWorker,
		ScheduledJobControlEnabled:            controlToken != "",
		ScheduledJobWorkerEnabled:             *scheduledJobWorker,
		ExecutionPermissionControlEnabled:     *permissionControl,
		ExecutionPermissionCapabilities:       permissionCapabilities,
		BrowserCDPPermissionControlEnabled:    *browserCDPControl,
		BrowserCDPPermissionCapabilities:      browserCDPCapabilities,
		FullCDPSessionControlEnabled:          fullCDPSessions != nil,
		FullCDPSessionController:              fullCDPSessions,
		CapabilityReadinessRuntime:            localReadinessRuntime,
		CommandRuntimeAdapters:                installedCommandRuntimeAdapters,
		CommandRuntimeAdvertiser:              commandRuntime,
		ProjectActionsController:              application.NewProjectActionsService(a.store, commandRuntime, commandRuntimeDrydocks),
		ProjectHooksController:                projectHooks,
		ProjectPreparationController:          projectPreparation,
		SkillInstallationEnabled:              controlToken != "",
		SkillCatalogControlEnabled:            controlToken != "",
		EvidenceAttachmentEnabled:             controlToken != "",
		VerificationEvidenceEnabled:           controlToken != "",
		EmbeddedAnalyzerExecutionEnabled:      controlToken != "",
		WorkspaceCheckpointControlEnabled:     controlToken != "",
		GitAdvancedControlEnabled:             *gitAdvancedEnabled,
		GitHubReviewControlEnabled:            *githubReviewEnabled,
		BatchDeliveryControlEnabled:           controlToken != "",
		BatchDeliveryHostValidationEnabled:    *batchValidationExecution,
		ExtensionControlEnabled:               controlToken != "",
		LifecycleHooks:                        hookEngine,
		RunLifecycleController:                lifecycleControl,
		ThreadTurnController:                  threadTurnControl,
		ThreadContextCompactionController:     application.NewThreadContextCompactionService(a.store, a.router).WithExecutionState(threadTurnControl),
		StandardCodePresetController:          standardCodePreset,
		StandardCodeDeliveryController:        standardCodeDeliveryController,
		RunExecutionController:                executionControl,
		PublicModelStreamSource:               executionControl,
		AgentBrowserController:                agentBrowserController,
		PlanDeliveryController:                planDeliveryControl,
		ApprovalController:                    approvalControl,
		ControlledCommandProposalController:   controlledCommandProposals,
		HostCommandProposalController:         hostCommandProposalControl,
		ModelControlController:                modelControl,
		ThreadModelRouteController:            threadModelRoutes,
		ProviderSearchReadinessController:     providerSearchReadiness,
		ProviderDefinitionController:          providerDefinitionControl,
		PriceSnapshotController:               a.store,
		FanoutExecutionController: application.NewReadOnlyFanoutExecutionService(
			a.store, a.router, a.checker),
		ChildTaskControlController:    application.NewChildTaskControlService(a.store),
		SpecialistExecutionController: specialistExecution,
		WorkflowController:            workflowControl,
		ProviderCredentialController:  providerCredentialControl,
		FileEditReviewController:      fileEditReview,
		FileEditProposalController:    fileEditProposal,
		FileWorkspaceDrydocks:         commandRuntimeDrydocks,
		ThreadReviewReader: application.NewThreadReviewService(a.store).WithRunWorktree(commandRuntimeDrydocks).
			WithCodeHandoff(application.NewCodeHandoffService(a.store).WithStandardCodeDelivery(standardCodeDelivery)),
		RunWakeController:                   runWakeControl,
		FileEditApplyController:             fileEditApply,
		RunWakeExecutionController:          runWakeExecution,
		RunWakeWorkerHealthSource:           workerHealth,
		ScheduledJobController:              scheduledJobs,
		ScheduledJobWorkerHealthSource:      scheduleWorkerHealth,
		SkillInstallationController:         skillInstallation,
		SkillCatalogController:              application.NewSkillCatalogService(a.store, skillInstallation),
		EmbeddedAnalyzerExecutionController: embeddedAnalyzerExecution,
		WorkspaceCheckpointController:       workspaceCheckpoints,
		GitAdvancedController:               gitAdvancedService,
		ThreadGitController:                 threadGit,
		ThreadPullRequestController:         application.NewThreadPullRequestService(a.store, githubReviewService, threadGit),
		PRFollowupController:                prFollowupController,
		GitHubReviewController:              githubReviewService,
		BatchDeliveryController:             batchWorkbench,
		ExtensionController:                 extensionControl,
		CodeIntelSource:                     a.codeIntel,
		CodeIntelController:                 codeIntelControl,
		DockerSandboxController:             dockerSandbox,
		DockerEnvironmentController: httpapi.NewDockerEnvironmentController(application.NewDockerEnvironmentService(
			*dockerExecution, os.Getenv(standardCodeDockerImageEnvironment), commandOptions.StandardCodeDockerRuntime != nil)),
		ModelRegistry: a.models,
		AppVersion:    Version,
		UIHandler:     uiBundle,
	})
	if err != nil {
		return err
	}
	listener, err := httpapi.ListenLoopback(ctx, *listenAddress)
	if err != nil {
		return err
	}
	server, err := httpapi.NewServer(api, log.New(a.errOut, "api: ", log.LstdFlags))
	if err != nil {
		_ = listener.Close()
		return err
	}
	reconcileCtx, reconcileCancel := context.WithCancel(ctx)
	reconcileDone := make(chan struct{})
	defer func() {
		reconcileCancel()
		<-reconcileDone
	}()
	if webFetchAuthorizationSchedulerEnabled {
		webFetchReconcileCtx, cancelWebFetchReconcile := context.WithCancel(ctx)
		webFetchReconcileDone := make(chan struct{})
		go func() {
			defer close(webFetchReconcileDone)
			for {
				_, _ = executionControl.ReconcileWebFetchAuthorizations(
					webFetchReconcileCtx, "", application.MaxApprovalQueueItems)
				timer := time.NewTimer(2 * time.Second)
				select {
				case <-webFetchReconcileCtx.Done():
					timer.Stop()
					return
				case <-timer.C:
				}
			}
		}()
		defer func() {
			cancelWebFetchReconcile()
			<-webFetchReconcileDone
		}()
	}
	go func() {
		defer close(reconcileDone)
		if commandRuntime != nil {
			if reconcileErr := commandRuntime.RunReconciler(reconcileCtx,
				500*time.Millisecond); reconcileErr != nil && reconcileCtx.Err() == nil {
				fmt.Fprintln(a.errOut, "command-runtime-reconciler:", reconcileErr)
			}
		} else {
			runCommandRuntimeStartupReconciler(reconcileCtx, commandManager, a.errOut)
		}
	}()
	origin := "http://" + listener.Addr().String()
	baseURL := origin + "/api/v1"
	fmt.Fprintf(a.out, "api_url: %s\napi_version: %s\napi_token_generated: %t\napi_control_enabled: %t\n",
		baseURL, httpapi.Version, generated, controlToken != "")
	if uiBundle != nil {
		fmt.Fprintf(a.out, "ui_url: %s/\nui_source: %s\nui_assets: %d\nui_digest: %s\n",
			origin, uiBundle.Source(), uiBundle.AssetCount(), uiBundle.Digest())
	}
	if generated {
		fmt.Fprintf(a.out, "api_token: %s\n", accessToken)
	} else {
		fmt.Fprintf(a.out, "api_token_source: %s\n", apiTokenEnvironment)
	}
	if controlToken != "" {
		fmt.Fprintf(a.out, "api_control_token_source: %s\n", apiControlTokenEnvironment)
	}
	var workerCancel context.CancelFunc
	var workerDone chan struct{}
	if worker != nil {
		workerCtx, cancel := context.WithCancel(ctx)
		workerCancel = cancel
		workerDone = make(chan struct{})
		go func() {
			defer close(workerDone)
			_ = worker.Run(workerCtx)
		}()
	}
	if workerCancel != nil {
		defer func() {
			workerCancel()
			<-workerDone
		}()
	}
	var scheduleWorkerCancel context.CancelFunc
	var scheduleWorkerDone chan struct{}
	if scheduleWorker != nil {
		workerCtx, cancel := context.WithCancel(ctx)
		scheduleWorkerCancel = cancel
		scheduleWorkerDone = make(chan struct{})
		go func() {
			defer close(scheduleWorkerDone)
			_ = scheduleWorker.Run(workerCtx)
		}()
	}
	if scheduleWorkerCancel != nil {
		defer func() {
			scheduleWorkerCancel()
			<-scheduleWorkerDone
		}()
	}
	if prFollowupWorker != nil {
		followupCtx, cancel := context.WithCancel(ctx)
		done := make(chan struct{})
		go func() { defer close(done); _ = prFollowupWorker.Run(followupCtx) }()
		defer func() { cancel(); <-done }()
	}
	if controlToken != "" {
		workflowCtx, cancel := context.WithCancel(ctx)
		done := make(chan struct{})
		go func() { defer close(done); _ = workflowControl.Run(workflowCtx) }()
		defer func() { cancel(); <-done }()
	}
	fmt.Fprintf(a.out, "file_edit_proposals_enabled: %t\nprovider_credentials_enabled: %t\nwake_worker_enabled: %t\nwake_worker_concurrency: %d\nwake_worker_max_steps: %d\n",
		*fileEditProposals, *providerCredentials, *wakeWorker,
		application.RunWakeWorkerConcurrency, application.RunWakeWorkerMaxSteps)
	fmt.Fprintf(a.out, "scheduled_job_control_enabled: %t\nscheduled_job_worker_enabled: %t\nscheduled_job_worker_concurrency: %d\nscheduled_job_persistent_service: false\n",
		controlToken != "", *scheduledJobWorker, scheduler.WorkerConcurrency)
	fmt.Fprintf(a.out, "execution_permission_control_enabled: %t\nworkspace_sandbox_enabled: %t\noperator_approval_enabled: %t\ndanger_full_access_enabled: %t\ncommand_runtime_enabled: %t\n",
		*permissionControl, permissionCapabilities.WorkspaceSandboxEnabled,
		permissionCapabilities.OperatorApprovalEnabled,
		permissionCapabilities.DangerFullAccessEnabled,
		commandRuntime != nil)
	fmt.Fprintf(a.out, "browser_cdp_permission_control_enabled: %t\nfull_cdp_debug_enabled: %t\n",
		*browserCDPControl, browserCDPCapabilities.FullDebugEnabled)
	fmt.Fprintf(a.out, "docker_execution_enabled: %t\n", *dockerExecution)
	fmt.Fprintf(a.out, "batch_delivery_host_validation_enabled: %t\n",
		*batchValidationExecution)
	fmt.Fprintf(a.out, "code_intel_enabled: %t\n", a.codeIntel != nil)
	fmt.Fprintf(a.out, "workspace_import_enabled: %t\n", *workspaceImport)
	fmt.Fprintf(a.out, "git_advanced_control_enabled: %t\n", *gitAdvancedEnabled)
	fmt.Fprintf(a.out, "github_review_control_enabled: %t\n", *githubReviewEnabled)
	fmt.Fprintln(a.out, "note: the API is loopback-only; control is separately authorized and tokens are not persisted")
	return server.Serve(ctx, listener)
}

func newAPIExecutionPermissionCapabilities(approval, fullAccess bool) domain.ExecutionPermissionRuntimeCapabilities {
	return domain.ExecutionPermissionRuntimeCapabilities{
		OperatorApprovalEnabled: approval,
		DangerFullAccessEnabled: fullAccess,
		// All API services share this process-local revocation fence. Creating
		// it grants nothing. Like Desktop, Full requires explicit current Run
		// activation; a process restart never inherits that activation.
		RuntimeAuthority: domain.NewExecutionPermissionRuntimeAuthority(),
	}
}

func runCommandRuntimeStartupReconciler(ctx context.Context,
	manager *runner.CommandRuntimeManager, errOut interface{ Write([]byte) (int, error) },
) {
	if manager == nil {
		return
	}
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if _, err := manager.ReconcileStartup(ctx); err != nil && ctx.Err() == nil {
				fmt.Fprintln(errOut, "command-runtime-reconciler:", err)
				shutdownCtx, cancel := context.WithTimeout(context.Background(), 7*time.Second)
				_ = manager.Shutdown(shutdownCtx)
				cancel()
				return
			}
		}
	}
}

func (a *App) apiOpenAPICommand(args []string) error {
	fs := newFlagSet("api openapi", a.errOut)
	output := fs.String("output", "", "optional output file")
	if err := fs.Parse(reorderFlags(args, map[string]bool{"output": true})); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		return errors.New("usage: cyberagent api openapi [--output <path>]")
	}
	document, err := httpapi.GenerateOpenAPI()
	if err != nil {
		return err
	}
	if strings.TrimSpace(*output) == "" {
		_, err = a.out.Write(document)
		return err
	}
	path := filepath.Clean(strings.TrimSpace(*output))
	if err := os.WriteFile(path, document, 0o644); err != nil {
		return fmt.Errorf("write OpenAPI document: %w", err)
	}
	absolute, err := filepath.Abs(path)
	if err != nil {
		absolute = path
	}
	fmt.Fprintf(a.out, "openapi_written: %s\n", absolute)
	return nil
}

// The API worker runs the complete due-job service selected by this composition.
type scheduledAPIWorkerHealth struct{ *scheduler.Worker }

func (scheduledAPIWorkerHealth) SelectionScope() string { return "all_jobs" }
