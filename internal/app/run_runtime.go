package app

import (
	"context"
	"errors"
	"sync"
	"time"

	"cyberagent-workbench/internal/apperror"
	"cyberagent-workbench/internal/application"
	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/runner"
)

func (a *App) runRuntimeDependencies() application.RunRuntimeDependencies {
	d := application.RunRuntimeDependencies{
		ActiveCalls: a.calls, LifecycleHooks: a.newLifecycleHookEngine(),
		CodeIntel: a.codeIntel, WebEvidence: a.newWebEvidenceService(),
		WebFetchAuthorizationScheduler: true,
	}
	if executor := a.newDockerSandboxProposalExecutor(); executor != nil {
		d.DockerSandbox = executor
	}
	if client := a.newMCPClientManager(); client != nil {
		d.MCPClient = client
	}
	return d
}

// Full is a durable preference, but its grant belongs only to this invocation.
// Every adapter receives the same authority; returning never persists a grant.
func (a *App) activateCLIInvocationFull(ctx context.Context, runID string,
	capabilities domain.ExecutionPermissionRuntimeCapabilities, confirmFull bool,
) (func(), error) {
	if err := capabilities.Validate(); err != nil {
		return nil, apperror.Wrap(apperror.CodeInvalidArgument, "invalid CLI runtime capabilities", err)
	}
	if !confirmFull {
		return func() {}, nil
	}
	if !capabilities.OperatorApprovalEnabled || !capabilities.DangerFullAccessEnabled ||
		capabilities.RuntimeAuthority == nil {
		return nil, apperror.New(apperror.CodePolicyDenied,
			"--confirm-full requires --enable-permission-control and --enable-danger-full-access")
	}
	run, err := a.store.GetRun(ctx, runID)
	if err != nil {
		return nil, apperror.Normalize(err)
	}
	permission, err := a.store.GetRunExecutionPermission(ctx, run.ID)
	if err != nil {
		return nil, apperror.Normalize(err)
	}
	if permission.Mode != domain.RunExecutionPermissionFull ||
		permission.RunID != run.ID || permission.MissionID != run.MissionID {
		return nil, apperror.New(apperror.CodePolicyDenied,
			"--confirm-full requires the Run's current Full preference")
	}
	if _, err := capabilities.RuntimeAuthority.ActivateRunFullAccess(permission); err != nil {
		return nil, apperror.Wrap(apperror.CodeFailedPrecondition,
			"CLI Full could not be activated", err)
	}
	var released sync.Once
	return func() { released.Do(func() { capabilities.RuntimeAuthority.RevokeRun(run.ID) }) }, nil
}

// A foreground CLI invocation owns its adapters until it returns. The same
// capability value (including its revocation fence) reaches every sibling.
type cliExecutionRuntime struct {
	capabilities domain.ExecutionPermissionRuntimeCapabilities
	commands     *application.CommandRuntimeService
	browser      *application.AgentBrowserService
	manager      *runner.CommandRuntimeManager
	stop         func() error
	closeOnce    sync.Once
	closeErr     error
}

func (a *App) newCLIExecutionRuntime(ctx context.Context,
	capabilities domain.ExecutionPermissionRuntimeCapabilities, withCommands bool,
) (*cliExecutionRuntime, error) {
	if err := capabilities.Validate(); err != nil {
		return nil, apperror.Wrap(apperror.CodeInvalidArgument, "invalid CLI runtime capabilities", err)
	}
	r := &cliExecutionRuntime{capabilities: capabilities, stop: func() error { return nil }}
	if withCommands {
		var err error
		r.manager, r.commands, err = a.newCLICommandRuntimeWithCapabilities(ctx, capabilities)
		if err != nil {
			return nil, err
		}
		r.stop = a.startCLICommandRuntimeReconciler(ctx, r.commands)
	}
	if capabilities.DangerFullAccessEnabled {
		r.browser = application.NewAgentBrowserService(a.store, application.AgentBrowserOptions{
			HomePath: a.home, Capabilities: capabilities, Headless: true, ServiceReadiness: r.commands,
		})
	}
	return r, nil
}

func (r *cliExecutionRuntime) dependencies(a *App) application.RunRuntimeDependencies {
	d := a.runRuntimeDependencies()
	d.ExecutionCapabilities = r.capabilities
	if r.commands != nil {
		d.CommandRuntime = r.commands
	}
	d.AgentBrowser = r.browser
	return d
}

func (r *cliExecutionRuntime) close() error {
	if r == nil {
		return nil
	}
	r.closeOnce.Do(func() {
		r.closeErr = r.stop()
		if r.browser != nil {
			ctx, cancel := context.WithTimeout(context.Background(), 35*time.Second)
			r.closeErr = errors.Join(r.closeErr, r.browser.Shutdown(ctx))
			cancel()
		}
		r.closeErr = errors.Join(r.closeErr, shutdownCLICommandRuntime(r.manager))
	})
	return r.closeErr
}
