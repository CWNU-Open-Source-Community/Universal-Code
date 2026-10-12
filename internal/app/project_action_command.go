package app

import (
	"context"
	"encoding/json"

	"cyberagent-workbench/internal/apperror"
	"cyberagent-workbench/internal/application"
	"cyberagent-workbench/internal/domain"
)

func (a *App) runProjectAction(ctx context.Context, args []string) error {
	fs := newFlagSet("run project-action", a.errOut)
	target := fs.String("path", "", "Exact relative Go file for formatting or package directory for npm tests")
	key := fs.String("operation-key", "", "Stable command operation key")
	operator := fs.String("operator", "cli_operator", "Operator identity")
	prepare := fs.Bool("prepare", false, "Print the typed intent without execution")
	confirm := fs.Bool("confirm-execution", false, "Confirm execution on a Created or Paused Run")
	confirmHost := fs.Bool("confirm-non-sandboxed-host-execution", false, "Acknowledge native host execution")
	confirmFull := fs.Bool("confirm-full", false, "Activate existing Full Access selection")
	enablePermission := fs.Bool("enable-permission-control", false, "Enable the existing permission controller")
	enableFull := fs.Bool("enable-danger-full-access", false, "Enable the existing Full Access runtime")
	if err := fs.Parse(reorderFlags(args, map[string]bool{"path": true, "operation-key": true, "operator": true,
		"prepare": false, "confirm-execution": false, "confirm-non-sandboxed-host-execution": false,
		"confirm-full": false, "enable-permission-control": false, "enable-danger-full-access": false})); err != nil {
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
	spec, err := application.PrepareProjectCommand(run, fs.Arg(1), *target, files.Workspace.RootPath)
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
	if !*confirmHost {
		return apperror.New(apperror.CodeInvalidArgument, "project actions require explicit host-execution acknowledgement")
	}
	capabilities := cliExecutionPermissionCapabilities(*enablePermission, *enableFull)
	permission, err := a.store.GetRunExecutionPermission(ctx, run.ID)
	if err != nil {
		return err
	}
	if permission.Mode == domain.RunExecutionPermissionFull && *confirmFull {
		if _, err := capabilities.RuntimeAuthority.ActivateRunFullAccess(permission); err != nil {
			return err
		}
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
