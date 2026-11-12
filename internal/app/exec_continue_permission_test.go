package app

import (
	"testing"

	"cyberagent-workbench/internal/application"
	"cyberagent-workbench/internal/domain"
)

func TestExecContinueConfirmedFullRebindsSuccessor(t *testing.T) {
	a, provider, st := newExecContinueTestApp(t)
	_, run, err := application.NewRunService(st).Create(t.Context(), application.CreateRunRequest{
		Goal: "Full successor", Profile: "review", WorkspaceID: "exec-ws",
		ModelRoute: provider.Name() + "/model", Interactive: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	threadID := domain.InitialThreadID(run.ID)
	_, err = application.NewThreadExecutionPermissionService(st, cliExecutionPermissionCapabilities(true, true)).Change(t.Context(), application.ChangeThreadExecutionPermissionRequest{
		ThreadID: threadID, Mode: "full", ConfirmFull: true,
		OperationKey: "exec-confirmed-full-test", RequestedBy: "cli_exec_operator", Reason: "test confirmed continuation",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := application.NewRunService(st).Cancel(t.Context(), run.ID); err != nil {
		t.Fatal(err)
	}
	capabilities := cliExecutionPermissionCapabilities(true, true)
	release, err := a.activateExecInvocationFull(t.Context(), threadID, run.ID, capabilities, true, true)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	turns := application.NewThreadTurnServiceWithExecutionCapabilities(st, application.NewRunLifecycleControlService(st),
		application.NewRunExecutionHandoffWithRuntime(st, a.router, a.checker, application.RunRuntimeDependencies{ExecutionCapabilities: capabilities}), capabilities)
	result, err := application.NewOneShotService(st, turns).Run(t.Context(), application.OneShotRequest{
		Version: domain.ThreadMessageProtocolVersion, ThreadID: threadID,
		Content: "Continue Full task", OperationKey: "exec-full-successor-message", RequestedBy: "cli_exec_operator",
	})
	if err != nil {
		t.Fatal(err)
	}
	permission, err := st.GetRunExecutionPermission(t.Context(), result.RunID)
	if err != nil {
		t.Fatal(err)
	}
	if result.RunID == run.ID || !capabilities.AllowsSnapshot(permission) {
		t.Fatalf("confirmed invocation did not authorize its successor: previous=%s successor=%s mode=%s", run.ID, result.RunID, permission.Mode)
	}
	release()
	if capabilities.AllowsSnapshot(permission) {
		t.Fatal("successor Full grant survived invocation cleanup")
	}
}
