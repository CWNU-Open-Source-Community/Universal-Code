package app

import (
	"context"
	"encoding/json"
	"time"

	"cyberagent-workbench/internal/apperror"
	"cyberagent-workbench/internal/application"
	"cyberagent-workbench/internal/commandruntimeadapter"
	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/idgen"
	"cyberagent-workbench/internal/runner"
)

func (a *App) runProjectAction(ctx context.Context, args []string) error {
	fs := newFlagSet("run project-action", a.errOut)
	target := fs.String("path", "", "Exact relative Go file for formatting or project directory for tests")
	key := fs.String("operation-key", "", "Stable command operation key")
	operator := fs.String("operator", "cli_operator", "Operator identity")
	prepare := fs.Bool("prepare", false, "Print the typed intent without execution")
	confirm := fs.Bool("confirm-execution", false, "Confirm execution on a Created or Paused Run")
	confirmHost := fs.Bool("confirm-non-sandboxed-host-execution", false, "Acknowledge native host execution")
	confirmFull := fs.Bool("confirm-full", false, "Activate existing Full Access selection")
	enablePermission := fs.Bool("enable-permission-control", false, "Enable the existing permission controller")
	enableFull := fs.Bool("enable-danger-full-access", false, "Enable the existing Full Access runtime")
	enableWorkspace := fs.Bool("enable-workspace-sandbox", false, "Enable the selected existing workspace sandbox")
	enableDocker := fs.Bool("enable-docker-execution", false, "Enable the fixed Docker execution adapter")
	if err := fs.Parse(reorderFlags(args, map[string]bool{"path": true, "operation-key": true, "operator": true,
		"prepare": false, "confirm-execution": false, "confirm-non-sandboxed-host-execution": false,
		"confirm-full": false, "enable-permission-control": false, "enable-danger-full-access": false,
		"enable-workspace-sandbox": false, "enable-docker-execution": false})); err != nil {
		return err
	}
	if fs.NArg() != 2 || !domain.ValidAgentID(fs.Arg(0)) || (fs.Arg(1) != "test" && fs.Arg(1) != "format") {
		return apperror.New(apperror.CodeInvalidArgument, "usage: cyberagent run project-action <run-id> test|format [--path <relative-file-or-package>] [--prepare] [--operation-key <key> --confirm-non-sandboxed-host-execution --enable-permission-control --enable-danger-full-access --confirm-execution --confirm-full]")
	}
	run, err := a.store.GetRun(ctx, fs.Arg(0))
	if err != nil {
		return err
	}
	mission, err := a.store.GetMission(ctx, run.MissionID)
	if err != nil {
		return err
	}
	drydocks, err := a.newRunFileDrydockService(ctx, run.ID)
	if err != nil {
		return err
	}
	files, err := application.ResolveRunFileWorkspace(ctx, a.store, run, mission, drydocks)
	if err != nil {
		return err
	}
	adapter := commandruntimeadapter.HostUnsandboxed("project-action-preparation")
	if files.Drydock != nil {
		profile, err := a.store.GetRunExecutionProfile(ctx, run.ID)
		if err != nil {
			return err
		}
		switch profile.Profile {
		case domain.RunExecutionProfileLocal:
			adapter = commandruntimeadapter.SandboxedWorkspace(application.CommandRuntimeLocalSandboxBackend, "local-preparation", "preparation-only")
		case domain.RunExecutionProfileDocker:
			adapter = commandruntimeadapter.SandboxedWorkspace(application.CommandRuntimeDockerSandboxBackend, "docker-standard-code-guest.v1", "preparation-only")
		default:
			return apperror.New(apperror.CodeFailedPrecondition, "project actions do not support the selected owned workspace backend")
		}
	}
	spec, err := application.PrepareProjectCommandForAdapter(run, fs.Arg(1), *target, adapter, files.Workspace.RootPath)
	if err != nil {
		return err
	}
	if *prepare {
		return json.NewEncoder(a.out).Encode(spec)
	}
	if !domain.ValidAgentID(*key) || !domain.ValidAgentID(*operator) {
		return apperror.New(apperror.CodeInvalidArgument, "project execution requires a bounded operation key and operator identity")
	}
	request := application.OperatorCommandRequest{RunID: run.ID, OperationKey: *key, RequestedBy: *operator, Command: spec, ConfirmExecution: *confirm}
	if receipt, found, err := application.ReadOperatorCommand(ctx, a.store, request); found || err != nil {
		return writeOperatorCommandResult(a.out, receipt, err)
	}
	if files.Drydock == nil && !*confirmHost {
		return apperror.New(apperror.CodeInvalidArgument, "project actions require explicit host-execution acknowledgement")
	}
	capabilities := cliExecutionPermissionCapabilities(*enablePermission, *enableFull)
	capabilities.WorkspaceSandboxEnabled = *enableWorkspace
	permission, err := a.store.GetRunExecutionPermission(ctx, run.ID)
	if err != nil {
		return err
	}
	if permission.Mode == domain.RunExecutionPermissionFull && *confirmFull {
		if _, err := capabilities.RuntimeAuthority.ActivateRunFullAccess(permission); err != nil {
			return err
		}
	}
	if files.Drydock != nil {
		if !*enablePermission || !*enableWorkspace {
			return apperror.New(apperror.CodeFailedPrecondition, "sandbox project actions require --enable-permission-control and --enable-workspace-sandbox")
		}
		var executor runner.CommandRuntimeSandboxExecutor
		if adapter.Backend == application.CommandRuntimeLocalSandboxBackend {
			backend, readiness, err := openLocalSandbox(ctx, true)
			if err != nil {
				return err
			}
			defer backend.Close()
			executor, err = application.NewLocalSandboxCommandRuntimeExecutor(a.store, backend, readiness)
			if err != nil {
				return err
			}
		} else {
			if !*enableDocker {
				return apperror.New(apperror.CodeFailedPrecondition, "Docker project actions require --enable-docker-execution")
			}
			standard, err := a.newStandardCodeDockerService(true, true, true, capabilities)
			if err != nil {
				return err
			}
			executor, err = application.NewDockerSandboxCommandRuntimeExecutor(standard)
			if err != nil {
				return err
			}
		}
		manager, err := runner.NewSandboxCommandRuntimeManager(a.store, executor, idgen.New("project-action-cli"))
		if err != nil {
			return err
		}
		defer func() {
			shutdown, cancel := context.WithTimeout(context.Background(), 7*time.Second)
			defer cancel()
			_ = manager.Shutdown(shutdown)
		}()
		if _, err := manager.ReconcileStartup(ctx); err != nil {
			return err
		}
		commands, err := application.NewSandboxedCommandRuntimeService(a.store, manager, executor, capabilities, drydocks)
		if err != nil {
			return err
		}
		commands.SetCommandRuntimePolicy(a.checker)
		view, err := application.NewProjectActionsService(a.store, commands, drydocks).List(ctx, run.ID)
		if err != nil {
			return err
		}
		for _, action := range view.Actions {
			if action.Slot == fs.Arg(1) && !action.Available {
				return apperror.New(apperror.CodeFailedPrecondition, action.Reason)
			}
		}
		result, err := commands.RunOperatorCommand(ctx, request)
		return writeOperatorCommandResult(a.out, result, err)
	}
	manager, commands, err := a.newCLICommandRuntimeWithCapabilities(ctx, capabilities)
	if err != nil {
		return err
	}
	if commands == nil {
		return apperror.New(apperror.CodeFailedPrecondition, "project command runtime startup gates are disabled")
	}
	defer func() { _ = shutdownCLICommandRuntime(manager) }()
	commands.SetCommandRuntimePolicy(a.checker)
	stop := a.startCLICommandRuntimeReconciler(ctx, commands)
	defer func() { _ = stop() }()
	result, err := commands.RunOperatorCommand(ctx, request)
	return writeOperatorCommandResult(a.out, result, err)
}
