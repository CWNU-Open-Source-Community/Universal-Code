package app

import (
	"context"

	"cyberagent-workbench/internal/apperror"
	"cyberagent-workbench/internal/domain"
)

func (a *App) activateExecInvocationFull(ctx context.Context, threadID, runID string,
	capabilities domain.ExecutionPermissionRuntimeCapabilities, confirmFull, continuing bool,
) (func(), error) {
	releaseRun, err := a.activateCLIInvocationFull(ctx, runID, capabilities, confirmFull)
	if err != nil || !confirmFull || !continuing {
		return releaseRun, err
	}
	thread, err := a.store.GetThreadExecutionPermission(ctx, threadID)
	if err != nil {
		releaseRun()
		return nil, apperror.Normalize(err)
	}
	run, err := a.store.GetRunExecutionPermission(ctx, runID)
	if err != nil {
		releaseRun()
		return nil, apperror.Normalize(err)
	}
	// A continued Thread can materialize a successor during submission. Bind
	// this invocation's explicit confirmation to that Thread, so its existing
	// Full preference can follow the successor within this process only.
	if _, err := capabilities.RuntimeAuthority.ActivateThreadFullAccess(thread, &run); err != nil {
		releaseRun()
		return nil, apperror.Wrap(apperror.CodePolicyDenied, "exec --confirm-full requires the Thread's current Full preference", err)
	}
	return func() { capabilities.RuntimeAuthority.RevokeThread(threadID); releaseRun() }, nil
}
