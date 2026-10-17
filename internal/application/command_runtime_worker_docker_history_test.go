package application

import (
	"errors"
	"testing"
	"time"

	"cyberagent-workbench/internal/approval"
	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/policy"
	"cyberagent-workbench/internal/sandbox"
)

func TestWorkerDockerColdCleanupSurvivesParentPermissionDowngrade(t *testing.T) {
	ctx := t.Context()
	f := newCommandApprovalSandboxFixture(t, domain.RunExecutionPermissionFull, domain.RunExecutionProfileDocker, true, true)
	executor := f.service.sandbox.(*DockerSandboxCommandRuntimeExecutor)
	old := executor.service.docker
	transport := old.lifecycleTransport.(*dockerLifecycleSupervisorTestTransport)
	// A lost response leaves a real product/lifecycle WAL and an owned running
	// resource in the controlled daemon, without granting a terminal receipt.
	transport.waitErr = errors.New("controlled worker daemon connection lost")
	lease, found, err := f.st.GetRunExecutionLease(ctx, f.turn.Run.ID)
	if err != nil || !found {
		t.Fatalf("worker Run lease: %+v %v", lease, err)
	}
	worker := newWorkerCommandTestActor(t, f.st, f.service, f.turn.Run, f.turn.Agent, lease, &domain.WorkerProfile{})
	input := commandApprovalSandboxInput(t)
	call := worker.prepare(t, input)
	scope := worker.charge(t, call, true)
	bindings, err := f.service.loadAuthorizedBindings(ctx, scope, false)
	if err != nil {
		t.Fatal(err)
	}
	resolved, err := f.service.normalizeCommandRuntimeSpec(input.Commands[0], bindings.rootPath)
	if err != nil {
		t.Fatal(err)
	}
	command, err := commandRuntimeDockerCommand(resolved)
	if err != nil {
		t.Fatal(err)
	}
	liveCtx := f.service.withWorkerCommandRuntime(ctx, scope, bindings)
	liveCtx, err = workerCommandDockerContext(liveCtx, f.service.runnerScope(scope, bindings, scope.OperationKey), resolved, command)
	if err != nil {
		t.Fatal(err)
	}
	requester := workerCommandDockerRequester(workerCommandRuntimeFromContext(liveCtx))
	prepared, err := executor.service.prepareCommandRuntime(liveCtx, StandardCodeDockerPrepareRequest{RunID: worker.run.ID,
		ExpectedGeneration: f.owned.Generation, ExpectedCheckpoint: f.owned.LastCheckpointID, Command: command,
		RequestedBy: requester, OperationKey: "cold-worker-prepare"}, sandbox.DockerStandardCodeStdinClosed)
	if err != nil || prepared.Preparation == nil || prepared.Approval == nil {
		t.Fatalf("worker Docker preparation: %+v %v", prepared, err)
	}
	if _, err := executor.service.manifests.ReviewApproval(liveCtx, prepared.Preparation.Preparation.ID,
		approval.ActionApprove, "cold-worker-approve", "operator", ""); err != nil {
		t.Fatal(err)
	}
	_, err = executor.service.executeCommandRuntime(liveCtx, StandardCodeDockerExecuteRequest{RunID: worker.run.ID,
		ExpectedGeneration: f.owned.Generation, ExpectedCheckpoint: f.owned.LastCheckpointID,
		PreparationID: prepared.Preparation.Preparation.ID, ApprovalID: prepared.Approval.ID,
		Command: command, RequestedBy: requester, OperationKey: "cold-worker-execute"}, lease,
		sandbox.DockerStandardCodeStdinClosed, nil)
	if err == nil {
		t.Fatal("lost worker process outcome was accepted as success")
	}
	records, err := f.st.ListRecoverableDockerSandboxes(ctx, 10)
	if err != nil || len(records) != 1 || records[0].Launch == nil || transport.state != sandbox.DockerContainerLifecycleStateRunning {
		t.Fatalf("recoverable worker container: %+v state=%s err=%v", records, transport.state, err)
	}
	lifecycle, err := f.st.GetDockerContainerLifecycle(ctx, records[0].Launch.LifecycleIntentID)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := f.st.ReleaseDockerContainerLifecycleLease(ctx, lifecycle.Lease); err != nil {
		t.Fatal(err)
	}
	if _, err := NewRunExecutionPermissionService(f.st, f.caps).Change(ctx, ChangeRunExecutionPermissionRequest{
		RunID: worker.run.ID, Mode: "ask", RequestedBy: "operator", OperationKey: "cold-worker-lower-permission"}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := f.st.GetSpecialistApprovalCall(ctx, worker.run.ID, call.ApprovalSourceID); err == nil {
		t.Fatal("old worker authority remained available for current review")
	}
	transport.waitErr = nil
	restarted, err := NewDockerSandboxService(f.st, old.readiness, policy.NewDefaultChecker(), old.dockerCapabilities, f.caps,
		WithDockerSandboxExecution(transport, old.ioService.transport, old.stagingRoot, time.Minute),
		WithDockerStandardCode(f.drydockFixture.service, executor.service.imageDigest))
	if err != nil {
		t.Fatal(err)
	}
	if recovered, err := restarted.RecoverStartup(ctx); err != nil || len(recovered) != 1 {
		t.Fatalf("worker cleanup after permission downgrade: %+v %v", recovered, err)
	}
	if transport.creates != 1 || transport.starts != 1 || transport.terms != 1 || transport.deletes != 1 || transport.state != sandbox.DockerContainerLifecycleStateAbsent {
		t.Fatalf("worker cold recovery failed to reap only its original resource: %+v", transport)
	}
	record, err := f.st.GetDockerSandboxRecord(ctx, records[0].Admission.ID)
	if err != nil || record.Receipt == nil || !record.Receipt.CleanupComplete || record.Receipt.Outcome == domain.DockerSandboxOutcomeSucceeded {
		t.Fatalf("worker cleanup restored execution success: %+v %v", record.Receipt, err)
	}
	if _, err := restarted.loadCurrentDockerSandboxAuthority(liveCtx, record.Admission.PlanID, manifestFromAdmission(record.Admission), requester); err == nil {
		t.Fatal("historical cleanup source restored a revoked worker start grant")
	}
	if _, err := f.service.ExecuteCommandRuntime(ctx, scope, input); err == nil {
		t.Fatal("permission downgrade allowed another native command dispatch")
	}
	if transport.creates != 1 || transport.starts != 1 {
		t.Fatal("cleanup or stale command dispatch launched another container")
	}
}
