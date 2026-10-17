package application

import (
	"context"
	"encoding/json"
	"io"
	"path/filepath"
	"strings"
	"testing"

	"cyberagent-workbench/internal/apperror"
	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/fileedit"
	"cyberagent-workbench/internal/idgen"
	"cyberagent-workbench/internal/policy"
	"cyberagent-workbench/internal/runmutation"
	"cyberagent-workbench/internal/runner"
	"cyberagent-workbench/internal/toolgateway"
	"cyberagent-workbench/internal/workspacecheckpoint"
)

// Keep a real manager Job open without starting a Docker daemon. Git, SQLite,
// command authority, cancellation and the owned file apply path remain real.
type drydockBoundaryHeldCommand struct {
	*DockerSandboxCommandRuntimeExecutor
}

func (drydockBoundaryHeldCommand) OwnsWorkspaceCheckpoint() bool         { return false }
func (drydockBoundaryHeldCommand) CommandRuntimeAdmissionRequired() bool { return false }

type drydockBoundaryUnownedStore struct{ RunWorktreeStore }

func (drydockBoundaryUnownedStore) RunOwnsCurrentDrydock(context.Context, string, string) (bool, error) {
	return false, nil
}

func (e drydockBoundaryHeldCommand) ExecuteSandboxCommand(ctx context.Context, _ runner.CommandRuntimeScope,
	_ runner.CommandRuntimeResolvedSpec, _ io.ReadCloser,
) (runner.CommandRuntimeSandboxResult, error) {
	<-ctx.Done()
	return runner.CommandRuntimeSandboxResult{ExitCode: 125, TreeReaped: true}, ctx.Err()
}

func TestDrydockCommandBoundaryDoesNotReserveCrossTurnFileIntent(t *testing.T) {
	ctx := t.Context()
	f := newCommandApprovalSandboxFixture(t, domain.RunExecutionPermissionFull, domain.RunExecutionProfileDocker, true, true)
	lease, found, err := f.st.GetRunExecutionLease(ctx, f.turn.Run.ID)
	if err != nil || !found {
		t.Fatalf("initial lease: %t %v", found, err)
	}
	f.turn, err = f.st.BeginSupervisorTurn(ctx, lease, "edit the owned workspace after ending its command Job")
	if err != nil {
		t.Fatal(err)
	}
	if err := f.manager.Shutdown(ctx); err != nil {
		t.Fatal(err)
	}
	held := drydockBoundaryHeldCommand{f.service.sandbox.(*DockerSandboxCommandRuntimeExecutor)}
	manager, err := runner.NewSandboxCommandRuntimeManager(f.st, held, idgen.New("drydock-file-boundary-owner"))
	if err != nil {
		t.Fatal(err)
	}
	service, err := NewSandboxedCommandRuntimeService(f.st, manager, held, f.caps, f.drydockFixture.service)
	if err != nil {
		t.Fatal(err)
	}
	f.manager, f.service = manager, service
	f.mux, err = NewCommandRuntimeMultiplexer(f.host, service)
	if err != nil {
		t.Fatal(err)
	}
	f.supervisor = NewAgentRunner(f.st, nil, policy.NewDefaultChecker()).WithExecutionPermissionCapabilities(f.caps).WithCommandRuntime(f.mux)
	input := commandApprovalSandboxInput(t)
	input.Action, input.FailurePolicy, input.MaxBytes = toolgateway.CommandRuntimeActionStart, "", nil
	started, err := f.execute(t, ctx, input, 1)
	if err != nil || len(started.Jobs) != 1 || started.Jobs[0].State.Terminal() {
		call, _, _ := f.st.GetSupervisorApprovalCall(ctx, f.call.RunID, f.call.CallID)
		t.Fatalf("start: %+v %v; receipt=%s", started, err, call.ResultJSON)
	}
	jobID := started.Jobs[0].ID
	job, err := f.st.GetCommandRuntimeJob(ctx, jobID)
	if err != nil {
		t.Fatal(err)
	}
	ownedSHA, err := runner.CommandRuntimeWorkspaceRootSHA256(f.owned.Path)
	if err != nil || job.WorkspaceID != f.turn.Mission.WorkspaceID || job.WorkspaceID == f.owned.WorkspaceID || job.WorkspaceRootSHA256 != ownedSHA {
		t.Fatalf("fixture did not preserve source identity and owned root: %+v %v", job, err)
	}
	edit, err := fileedit.NewManager(f.st).Propose(ctx, fileedit.Proposal{SessionID: f.turn.Run.SessionID,
		WorkspaceID: f.owned.WorkspaceID, WorkspaceRoot: f.owned.Path, Path: "tracked.txt",
		ExpectedOriginalHash: fileedit.HashText("base\n"), ProposedText: "owned edit after cancellation\n"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := NewFileEditReviewService(f.st).WithRunWorktree(f.drydockFixture.service).Review(ctx,
		ReviewFileEditRequest{Version: FileEditReviewProtocolVersion, RunID: f.turn.Run.ID, EditID: edit.ID, Action: FileEditApproveIntent}); err != nil {
		t.Fatal(err)
	}
	arguments, _ := json.Marshal(toolgateway.WorkspaceApplyPayload{Version: toolgateway.AgentCodeRegistryVersion,
		EditID: edit.ID, ExpectedAction: "replace", ExpectedOriginalSHA256: edit.OriginalHash, ExpectedProposedSHA256: edit.ProposedHash})
	key := func() string {
		return runmutation.SupervisorToolOperationKey(f.turn.Run.ID, f.turn.Checkpoint.NextTurn, string(toolgateway.WorkspaceApplyTool), string(arguments))
	}
	lease, found, err = f.st.GetRunExecutionLease(ctx, f.turn.Run.ID)
	if err != nil || !found {
		t.Fatalf("lease: %t %v", found, err)
	}
	request := ApplyFileEditRequest{Version: fileedit.FileEditApplyProtocolVersion, RunID: f.turn.Run.ID,
		EditID: edit.ID, AppliedBy: "operator", OperationKey: key(), LeaseID: lease.LeaseID, LeaseGeneration: lease.Generation}
	apply := NewFileEditApplyService(f.st, policy.NewDefaultChecker()).WithRunWorktree(f.drydockFixture.service).WithExecutionPermissionCapabilities(f.caps)
	_, err = apply.Apply(ctx, request)
	if apperror.CodeOf(err) != apperror.CodeConflict {
		t.Fatalf("owned command did not block file apply: %v", err)
	}
	if _, _, found, lookupErr := f.st.GetFileEditApplyOperation(ctx, runmutation.FileEditApplyOperationDigest(request.RunID, edit.ID, request.OperationKey)); lookupErr != nil || found {
		t.Fatalf("Drydock command conflict retained an unstarted intent: found=%t err=%v; apply=%v", found, lookupErr, err)
	}
	if !strings.Contains(err.Error(), jobID) {
		t.Fatalf("Drydock conflict omitted its exact Job: %v", err)
	}
	boundaries := f.drydockFixture.service.FileEditCheckpointService()
	binding, err := boundaries.loadBinding(ctx, f.turn.Run.ID)
	if err != nil {
		t.Fatal(err)
	}
	open, err := f.st.ListOpenWorkspaceCheckpointTransactions(ctx, 10)
	if err != nil || len(open) != 1 || open[0].WorkspaceID != f.owned.WorkspaceID {
		t.Fatalf("owned command boundary: %+v %v", open, err)
	}
	for _, tc := range []struct {
		name   string
		change func(*runner.CommandRuntimeJob, *workspaceCheckpointBinding)
	}{
		{"wrong source", func(j *runner.CommandRuntimeJob, _ *workspaceCheckpointBinding) { j.WorkspaceID = "other-source" }},
		{"owned ID substituted for source", func(j *runner.CommandRuntimeJob, _ *workspaceCheckpointBinding) { j.WorkspaceID = f.owned.WorkspaceID }},
		{"wrong owned root", func(j *runner.CommandRuntimeJob, _ *workspaceCheckpointBinding) {
			j.WorkspaceRootSHA256 = strings.Repeat("a", 64)
		}},
		{"wrong owned workspace", func(_ *runner.CommandRuntimeJob, b *workspaceCheckpointBinding) { b.workspace.ID = "other-owned" }},
		{"wrong owned path", func(_ *runner.CommandRuntimeJob, b *workspaceCheckpointBinding) { b.workspace.RootPath = f.root }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			observed, target := job, binding
			tc.change(&observed, &target)
			wrapped := &drydockFileBoundaryStore{WorkspaceCheckpointStore: workspaceBoundaryJobReader{WorkspaceCheckpointStore: f.st, job: observed}, owner: f.drydockFixture.service}
			checkpoints := &WorkspaceCheckpointService{store: wrapped}
			err := checkpoints.workspaceMutationBoundaryConflict(ctx, target, workspacecheckpoint.TransactionFileTool, open[0])
			if apperror.CodeOf(err) != apperror.CodeConflict || strings.Contains(err.Error(), jobID) {
				t.Fatalf("unverified source/owned binding produced cancellation advice: %v", err)
			}
		})
	}
	t.Run("current logical owner changed", func(t *testing.T) {
		owner := *f.drydockFixture.service
		owner.store = drydockBoundaryUnownedStore{owner.store}
		wrapped := &drydockFileBoundaryStore{WorkspaceCheckpointStore: f.st, owner: &owner}
		err := (&WorkspaceCheckpointService{store: wrapped}).workspaceMutationBoundaryConflict(ctx, binding, workspacecheckpoint.TransactionFileTool, open[0])
		if apperror.CodeOf(err) != apperror.CodeConflict || strings.Contains(err.Error(), jobID) {
			t.Fatalf("former logical holder produced cancellation advice: %v", err)
		}
	})
	t.Run("underlying reader unavailable", func(t *testing.T) {
		wrapped := &drydockFileBoundaryStore{WorkspaceCheckpointStore: struct{ WorkspaceCheckpointStore }{f.st}, owner: f.drydockFixture.service}
		err := (&WorkspaceCheckpointService{store: wrapped}).workspaceMutationBoundaryConflict(ctx, binding, workspacecheckpoint.TransactionFileTool, open[0])
		if apperror.CodeOf(err) != apperror.CodeConflict || strings.Contains(err.Error(), jobID) {
			t.Fatalf("missing reader bypassed generic refusal: %v", err)
		}
	})
	wait, cursor, maximum := 1000, uint64(0), 4096
	stopped, err := f.execute(t, ctx, toolgateway.CommandRuntimeInput{Version: toolgateway.CommandRuntimeToolProtocolVersion,
		Action: toolgateway.CommandRuntimeActionCancel, JobID: jobID, WaitMilliseconds: &wait}, 2)
	if err != nil || len(stopped.Jobs) != 1 {
		t.Fatalf("cancel: %+v %v", stopped, err)
	}
	if !stopped.Jobs[0].State.Terminal() {
		stopped, err = f.execute(t, ctx, toolgateway.CommandRuntimeInput{Version: toolgateway.CommandRuntimeToolProtocolVersion,
			Action: toolgateway.CommandRuntimeActionWait, JobID: jobID, WaitMilliseconds: &wait, Cursor: &cursor, MaxBytes: &maximum}, 3)
	}
	if err != nil || len(stopped.Jobs) != 1 || !stopped.Jobs[0].State.Terminal() || !stopped.Jobs[0].TreeReaped {
		t.Fatalf("terminal wait: %+v %v", stopped, err)
	}
	f.nextTurn(t, lease)
	previousKey := request.OperationKey
	request.OperationKey = key()
	if previousKey == request.OperationKey {
		t.Fatal("Supervisor turn did not change")
	}
	applied, err := apply.Apply(ctx, request)
	if err != nil || !applied.FileWritten {
		t.Fatalf("same proposal after cancellation/new turn: %+v %v", applied, err)
	}
	if readDrydockTestFile(t, filepath.Join(f.owned.Path, "tracked.txt")) != "owned edit after cancellation\n" || readDrydockTestFile(t, filepath.Join(f.root, "tracked.txt")) != "base\n" {
		t.Fatal("apply crossed source/owned roots")
	}
	if f.hostTripwire.attempts.Load() != 0 {
		t.Fatal("sandbox test dispatched a host process")
	}
}
