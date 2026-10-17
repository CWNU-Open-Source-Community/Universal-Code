package application

import (
	"context"
	"testing"
	"time"

	"cyberagent-workbench/internal/approval"
	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/runner"
	"cyberagent-workbench/internal/toolgateway"
)

func TestWorkerRevokeApprovalWaitStopsOnlyOwnedCommand(t *testing.T) {
	st, run, root, lease, caps := newCommandRuntimeTestRuntime(t, t.Context())
	manager, err := runner.NewPlatformCommandRuntimeManager(st, "worker-revoke-cleanup")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = manager.Shutdown(ctx)
	})
	commands, err := NewCommandRuntimeService(st, manager, caps)
	if err != nil {
		t.Fatal(err)
	}
	a := newWorkerCommandTestActor(t, st, commands, run, root, lease, &domain.WorkerProfile{})
	b := newWorkerCommandTestActor(t, st, commands, run, root, lease, &domain.WorkerProfile{})
	input := commandApprovalNativeInput(t, true)
	input.Commands[0].Arguments = []string{"-e", "setInterval(()=>{},1000)"}
	input.Commands[0].TimeoutMilliseconds = 60000
	start := func(actor *workerCommandTestActor) string {
		call := actor.prepare(t, input)
		result, err := commands.ExecuteCommandRuntime(t.Context(), actor.charge(t, call, true), input)
		if err != nil || len(result.Jobs) != 1 {
			t.Fatalf("start owned command: %+v %v", result, err)
		}
		actor.complete(t, call)
		return result.Jobs[0].ID
	}
	ownedJob, siblingJob := start(a), start(b)
	call := a.prepare(t, toolgateway.CommandRuntimeInput{Version: toolgateway.CommandRuntimeToolProtocolVersion, Action: "list"})
	binding, err := st.GetSpecialistToolBinding(t.Context(), a.ref)
	if err != nil {
		t.Fatal(err)
	}
	p, err := st.EnsureApproval(t.Context(), approval.Proposal{IdempotencyKey: "worker-revoke-cleanup-approval", ProposalID: call.ApprovalSourceID, SessionID: a.child.SessionID, WorkspaceID: binding.WorkspaceID, ToolName: call.Name, ActionClass: "command_process", Mode: "per_call", Status: approval.StatusPending, RequestFingerprint: domain.SpecialistToolApprovalFingerprint(call), RequestedBy: "specialist_runner"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = st.SetSpecialistToolApproval(t.Context(), a.ref, call.CallID, p.ID); err != nil {
		t.Fatal(err)
	}
	if _, _, err = st.ReleaseRunExecutionLease(t.Context(), lease); err != nil {
		t.Fatal(err)
	}
	service := NewSpecialistExecutionService(st, nil, nil).WithWorkerRuntime(&AgentRunner{commandRuntime: commands})
	node, err := service.RevokeSpecialistReadTools(t.Context(), run.ID, a.child.ID)
	if err != nil || !node.Terminal() {
		t.Fatalf("revoke wait: %+v %v", node, err)
	}
	job, err := st.GetCommandRuntimeJob(t.Context(), ownedJob)
	// Stop requests OS termination; the process reaper persists its terminal
	// receipt asynchronously after the cleanup call returns.
	deadline := time.Now().Add(5 * time.Second)
	for err == nil && !job.State.Terminal() && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
		job, err = st.GetCommandRuntimeJob(t.Context(), ownedJob)
	}
	if err != nil || !job.State.Terminal() {
		t.Fatalf("revoked worker process survived: %s %v", job.State, err)
	}
	job, err = st.GetCommandRuntimeJob(t.Context(), siblingJob)
	if err != nil || job.State.Terminal() {
		t.Fatalf("sibling process was stopped: %+v %v", job, err)
	}
}
