package application

import (
	"context"
	"encoding/json"
	"math"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"cyberagent-workbench/internal/apperror"
	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/fileedit"
	"cyberagent-workbench/internal/policy"
	"cyberagent-workbench/internal/runner"
	"cyberagent-workbench/internal/toolgateway"
)

type dockerSharedServiceJobStore struct {
	commandRuntimeDockerServiceStore
	job runner.CommandRuntimeJob
}

func (s dockerSharedServiceJobStore) ListCommandRuntimeJobs(context.Context, runner.CommandRuntimeListFilter) ([]runner.CommandRuntimeJob, error) {
	return []runner.CommandRuntimeJob{s.job}, nil
}

func TestDockerSharedServiceSurvivesFileEditAndPreservesItsCheckpoint(t *testing.T) {
	ctx := t.Context()
	f := newCommandApprovalSandboxFixture(t, domain.RunExecutionPermissionAuto, domain.RunExecutionProfileDocker, true)
	native := f.service.sandbox.(*DockerSandboxCommandRuntimeExecutor)
	native.service.authorityPoll = 5 * time.Millisecond
	transport := &dockerAdmissionWaitBarrier{
		dockerLifecycleSupervisorTestTransport: native.service.docker.lifecycleTransport.(*dockerLifecycleSupervisorTestTransport),
		entered:                                make(chan struct{}), release: make(chan struct{}), startEntered: make(chan struct{}), startRelease: make(chan struct{}),
	}
	close(transport.startRelease)
	native.service.docker.lifecycleTransport = transport
	var finishOnce sync.Once
	finish := func() { finishOnce.Do(func() { close(transport.release) }) }
	t.Cleanup(finish)
	input := commandApprovalSandboxInput(t)
	input.Action, input.FailurePolicy, input.MaxBytes = toolgateway.CommandRuntimeActionStart, "", nil
	input.Commands[0].WorkspaceMode = runner.CommandRuntimeWorkspaceSharedService
	input.Commands[0].TimeoutMilliseconds = 120000
	started, err := f.execute(t, ctx, input, 1)
	if err != nil || len(started.Jobs) != 1 || started.Jobs[0].State.Terminal() {
		t.Fatalf("start shared service: %+v %v", started, err)
	}
	select {
	case <-transport.entered:
	case <-time.After(10 * time.Second):
		t.Fatal("service did not reach its live container wait")
	}
	jobID := started.Jobs[0].ID
	job, err := f.st.GetCommandRuntimeJob(ctx, jobID)
	if err != nil || runner.CommandRuntimeJobWorkspaceMode(job) != runner.CommandRuntimeWorkspaceSharedService {
		t.Fatalf("durable service Job: %+v %v", job, err)
	}
	records, err := f.st.ListRecoverableDockerSandboxes(ctx, 10)
	if err != nil || len(records) != 1 {
		t.Fatalf("live service admission: %+v %v", records, err)
	}
	serviceBinding, err := commandRuntimeDockerServiceForAdmission(ctx, f.st, records[0].Admission)
	if err != nil || serviceBinding == nil || serviceBinding.actor.AgentID != f.turn.Agent.ID ||
		serviceBinding.actor.AgentAttemptID != f.turn.Checkpoint.AttemptID {
		t.Fatalf("service did not retain its exact Job/admission binding: %+v %v", serviceBinding, err)
	}
	executionScope := executionContextFromDockerBinding(serviceBinding.binding)
	edit, err := fileedit.NewManager(f.st).Propose(ctx, fileedit.Proposal{
		SessionID: f.turn.Run.SessionID, WorkspaceID: f.owned.WorkspaceID, WorkspaceRoot: f.owned.Path,
		Path: "tracked.txt", ExpectedOriginalHash: fileedit.HashText("base\n"), ProposedText: "edited while service runs\n",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := NewFileEditReviewService(f.st).WithRunWorktree(f.drydockFixture.service).Review(ctx,
		ReviewFileEditRequest{Version: FileEditReviewProtocolVersion, RunID: f.turn.Run.ID, EditID: edit.ID, Action: FileEditApproveIntent}); err != nil {
		t.Fatal(err)
	}
	lease, found, err := f.st.GetRunExecutionLease(ctx, f.turn.Run.ID)
	if err != nil || !found {
		t.Fatalf("file edit lease: %t %v", found, err)
	}
	apply := NewFileEditApplyService(f.st, policy.NewDefaultChecker()).WithRunWorktree(f.drydockFixture.service).WithExecutionPermissionCapabilities(f.caps)
	applied, err := apply.Apply(ctx, ApplyFileEditRequest{Version: fileedit.FileEditApplyProtocolVersion,
		RunID: f.turn.Run.ID, EditID: edit.ID, AppliedBy: "operator", OperationKey: "shared-service-edit",
		LeaseID: lease.LeaseID, LeaseGeneration: lease.Generation})
	if err != nil || !applied.FileWritten {
		t.Fatalf("edit while service runs: %+v %v", applied, err)
	}
	afterEdit, found, err := readRunFileDrydock(ctx, f.st, f.turn.Run.ID)
	if err != nil || !found || afterEdit.Generation <= f.owned.Generation || afterEdit.LastCheckpointID == f.owned.LastCheckpointID {
		t.Fatalf("file edit did not advance its Drydock: %+v %v", afterEdit, err)
	}
	if native.service.currentAuthorityMetadata(ctx, executionScope, nil) ||
		!native.service.currentAuthorityMetadata(ctx, executionScope, serviceBinding) {
		t.Fatal("only the explicitly bound shared service may survive a newer edit checkpoint")
	}
	resolved, err := native.NormalizeCommandRuntimeSpec(input.Commands[0], f.owned.Path)
	if err != nil {
		t.Fatal(err)
	}
	command, err := commandRuntimeDockerCommand(resolved)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := native.service.compileCurrent(ctx, job.RunID, executionScope.DrydockGeneration,
		executionScope.CheckpointID, command, string(job.StdinPolicy), true); err == nil {
		t.Fatal("shared service relaxed the original generation fence for a new start")
	}
	live, _, err := f.manager.Wait(ctx, jobID, 100*time.Millisecond, math.MaxUint64, 4096)
	if err != nil || live.State.Terminal() || live.State == runner.CommandRuntimeJobStopping {
		t.Fatalf("editing cancelled the live service: %+v %v", live, err)
	}
	finish()
	deadline := time.Now().Add(15 * time.Second)
	for !live.State.Terminal() && time.Now().Before(deadline) {
		live, _, err = f.manager.Wait(ctx, jobID, time.Second, math.MaxUint64, 4096)
		if err != nil {
			t.Fatal(err)
		}
	}
	if live.State != runner.CommandRuntimeJobCompleted || !live.TreeReaped {
		t.Fatalf("service failed to complete its owned container cleanup: %+v", live)
	}
	current, found, err := readRunFileDrydock(ctx, f.st, f.turn.Run.ID)
	if err != nil || !found || current.Generation != afterEdit.Generation || current.LastCheckpointID != afterEdit.LastCheckpointID {
		t.Fatalf("service completion replaced the file edit checkpoint: before=%+v after=%+v err=%v", afterEdit, current, err)
	}
	if readDrydockTestFile(t, filepath.Join(f.owned.Path, "tracked.txt")) != "edited while service runs\n" {
		t.Fatal("service completion changed the edited file")
	}
	if recovered, err := native.service.RecoverStartup(ctx); err != nil || len(recovered) != 0 {
		t.Fatalf("service recovery created a lifetime checkpoint: %+v %v", recovered, err)
	}
	current, _, err = readRunFileDrydock(ctx, f.st, f.turn.Run.ID)
	if err != nil || current.Generation != afterEdit.Generation || current.LastCheckpointID != afterEdit.LastCheckpointID {
		t.Fatalf("service recovery replaced the edit checkpoint: %+v %v", current, err)
	}
	terminal, err := f.st.GetDockerSandboxRecord(ctx, records[0].Admission.ID)
	if err != nil || terminal.Receipt == nil || !terminal.Receipt.CleanupComplete {
		t.Fatalf("service terminal cleanup receipt: %+v %v", terminal.Receipt, err)
	}
	if _, err := native.service.finalize(ctx, terminal, executionScope, terminal.Admission.RequestedBy); apperror.CodeOf(err) != apperror.CodePolicyDenied {
		t.Fatalf("direct service finalization did not refuse a lifetime checkpoint: %v", err)
	}
	replayed, err := native.service.executeCommandRuntime(ctx, StandardCodeDockerExecuteRequest{
		RunID: job.RunID, ExpectedGeneration: executionScope.DrydockGeneration, ExpectedCheckpoint: executionScope.CheckpointID,
		PreparationID: terminal.Admission.PreparationID, ApprovalID: terminal.Admission.ApprovalID,
		OperationKey: commandRuntimeDockerServiceKey(job) + "-execute", RequestedBy: terminal.Admission.RequestedBy, Command: command,
	}, lease, string(job.StdinPolicy), nil)
	if err != nil || !replayed.Executed || replayed.batchRecord == nil || !replayed.batchRecord.Replayed {
		t.Fatalf("terminal service replay failed after a newer edit checkpoint: %+v %v", replayed, err)
	}
	current, _, err = readRunFileDrydock(ctx, f.st, f.turn.Run.ID)
	if err != nil || current.Generation != afterEdit.Generation || current.LastCheckpointID != afterEdit.LastCheckpointID {
		t.Fatalf("terminal service replay replaced the edit checkpoint: %+v %v", current, err)
	}
	for _, test := range []struct {
		name   string
		change func(*runner.CommandRuntimeJob)
	}{
		{"different Run", func(j *runner.CommandRuntimeJob) { j.RunID = "other-run" }},
		{"different root", func(j *runner.CommandRuntimeJob) { j.RootAgentID = "other-root" }},
		{"different backend", func(j *runner.CommandRuntimeJob) { j.Adapter.Backend = "other-backend" }},
		{"different command", func(j *runner.CommandRuntimeJob) {
			var intent map[string]any
			if err := json.Unmarshal([]byte(j.IntentJSON), &intent); err != nil {
				t.Fatal(err)
			}
			intent["purpose"] = "a different command purpose"
			encoded, _ := json.Marshal(intent)
			j.IntentJSON = string(encoded)
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			changed := job
			test.change(&changed)
			bound, err := commandRuntimeDockerServiceForAdmission(ctx,
				dockerSharedServiceJobStore{commandRuntimeDockerServiceStore: f.st, job: changed}, records[0].Admission)
			if err == nil && bound != nil {
				t.Fatal("changed durable Job was accepted as the service's original binding")
			}
		})
	}
	wrongRoot := afterEdit
	wrongRoot.Path = f.root
	if serviceBinding.matchesWorkspace(wrongRoot) {
		t.Fatal("shared service accepted the source checkout as its executable root")
	}
	if transport.creates != 1 || transport.starts != 1 || transport.deletes != 1 || f.hostTripwire.attempts.Load() != 0 {
		t.Fatalf("service replay dispatched or cleaned another process: creates=%d starts=%d deletes=%d", transport.creates, transport.starts, transport.deletes)
	}
	if !strings.Contains(job.IntentJSON, `"workspace_mode":"shared_service"`) {
		t.Fatal("service semantics were not retained in the durable intent")
	}
}
