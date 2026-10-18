package application

import (
	"context"
	"encoding/json"
	"fmt"
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
	"cyberagent-workbench/internal/runmutation"
	"cyberagent-workbench/internal/runner"
	"cyberagent-workbench/internal/sandbox"
	"cyberagent-workbench/internal/toolgateway"
)

type dockerSharedServiceJobStore struct {
	commandRuntimeDockerServiceStore
	job runner.CommandRuntimeJob
}

func (s dockerSharedServiceJobStore) GetCommandRuntimeJob(context.Context, string) (runner.CommandRuntimeJob, error) {
	return s.job, nil
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

func TestDockerServiceLookupAndOrdinaryCleanupIgnoreHistoricalJobWindow(t *testing.T) {
	ctx := t.Context()
	f := newCommandApprovalSandboxFixture(t, domain.RunExecutionPermissionAuto, domain.RunExecutionProfileDocker, true)
	native := f.service.sandbox.(*DockerSandboxCommandRuntimeExecutor)
	input := commandApprovalSandboxInput(t)
	input.Action, input.FailurePolicy, input.MaxBytes = toolgateway.CommandRuntimeActionStart, "", nil
	input.Commands[0].WorkspaceMode = runner.CommandRuntimeWorkspaceSharedService
	input.Commands[0].TimeoutMilliseconds = 120000
	started, err := f.execute(t, ctx, input, 1)
	if err != nil || len(started.Jobs) != 1 {
		t.Fatalf("start original service: %+v %v", started, err)
	}
	live := started.Jobs[0]
	for deadline := time.Now().Add(15 * time.Second); !live.State.Terminal() && time.Now().Before(deadline); {
		live, _, err = f.manager.Wait(ctx, live.ID, time.Second, math.MaxUint64, 4096)
		if err != nil {
			t.Fatal(err)
		}
	}
	if live.State != runner.CommandRuntimeJobCompleted || !live.TreeReaped {
		t.Fatalf("original service cleanup: %+v", live)
	}
	job, err := f.st.GetCommandRuntimeJob(ctx, live.ID)
	if err != nil {
		t.Fatal(err)
	}
	actor, err := f.st.GetCommandRuntimeJobAttribution(ctx, job.ID)
	if err != nil {
		t.Fatal(err)
	}
	admission, found, err := f.st.GetDockerSandboxAdmissionByOperation(ctx, commandRuntimeDockerServiceAdmissionDigest(job))
	if err != nil || !found {
		t.Fatalf("original service admission: %t %v", found, err)
	}
	// Put the original service beyond the former newest-500 lookup window using
	// real terminal Job records. These rows own no native process or container.
	for i := 0; i < 500; i++ {
		historical := job
		historical.OperationDigest, historical.ID = runner.CommandRuntimeOperationIdentity(job.RunID, fmt.Sprintf("later-history-%d", i))
		historical.InvocationID = fmt.Sprintf("later-history-%d", i)
		historical.RequestFingerprint = runmutation.Fingerprint("docker-service-test-history", historical.ID)
		now := job.CreatedAt.Add(time.Duration(i+1) * time.Millisecond)
		historical.CreatedAt, historical.UpdatedAt, historical.OwnerRenewedAt = now, now, now
		historical.OwnerExpiresAt = now.Add(time.Minute)
		historical.State, historical.Version = runner.CommandRuntimeJobPrepared, 1
		historical.StartedAt, historical.CompletedAt, historical.ExitCode = nil, nil, nil
		historical.TreeReaped, historical.JobAssignedAtCreation = false, false
		historical, replayed, err := f.st.PrepareCommandRuntimeJobForAgent(ctx, historical, actor)
		if err != nil || replayed {
			t.Fatalf("prepare historical Job %d: replayed=%t %v", i, replayed, err)
		}
		historical.State, historical.Version = runner.CommandRuntimeJobRunning, 2
		historical.StartedAt, historical.JobAssignedAtCreation = &now, true
		historical, err = f.st.UpdateCommandRuntimeJob(ctx, historical, 1)
		if err != nil {
			t.Fatalf("start historical Job %d: %v", i, err)
		}
		exit := 0
		historical.State, historical.Version = runner.CommandRuntimeJobCompleted, 3
		historical.StartedAt, historical.CompletedAt, historical.ExitCode = &now, &now, &exit
		historical.TreeReaped, historical.JobAssignedAtCreation = true, true
		if _, err := f.st.UpdateCommandRuntimeJob(ctx, historical, 2); err != nil {
			t.Fatalf("complete historical Job %d: %v", i, err)
		}
	}
	recent, err := f.st.ListCommandRuntimeJobs(ctx, runner.CommandRuntimeListFilter{RunID: job.RunID, Limit: 500})
	if err != nil || len(recent) != 500 {
		t.Fatalf("historical lookup window: %d %v", len(recent), err)
	}
	for _, newer := range recent {
		if newer.ID == job.ID {
			t.Fatal("test did not place the original service outside the historical window")
		}
	}
	bound, err := commandRuntimeDockerServiceForAdmission(ctx, f.st, admission)
	if err != nil || bound == nil || bound.job.ID != job.ID || bound.actor != actor {
		t.Fatalf("old service lost its exact durable binding: %+v %v", bound, err)
	}
	manifest, err := sandbox.DecodeManifest([]byte(admission.ManifestJSON))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := native.service.docker.loadCurrentDockerSandboxAuthority(ctx, admission.PlanID, manifest, admission.RequestedBy); apperror.CodeOf(err) != apperror.CodePolicyDenied {
		t.Fatalf("durable service marker authorized a fresh start without its private proof: %v", err)
	}
	forged := admission
	forged.RequestedBy = commandRuntimeDockerServiceRequester(recent[0])
	forged.AdmissionFingerprint = domain.DockerSandboxAdmissionFingerprint(forged)
	if bound, err := commandRuntimeDockerServiceForAdmission(ctx, f.st, forged); err == nil || bound != nil {
		t.Fatalf("another Job's marker replaced the exact admission binding: %+v %v", bound, err)
	}
	ordinary, err := f.execute(t, ctx, commandApprovalSandboxInput(t), 2)
	if err != nil || len(ordinary.Jobs) != 1 || ordinary.Jobs[0].State != runner.CommandRuntimeJobCompleted || !ordinary.Jobs[0].TreeReaped {
		t.Fatalf("historical Jobs blocked ordinary execution or container cleanup: %+v %v", ordinary, err)
	}
	workspace, shared, err := native.service.docker.sharedServiceCleanupWorkspace(ctx, admission)
	if err != nil || !shared || workspace.RootPath != f.owned.Path {
		t.Fatalf("old service cleanup lost its original physical root: %+v %t %v", workspace, shared, err)
	}
}
