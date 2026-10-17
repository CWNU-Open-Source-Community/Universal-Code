package application

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"cyberagent-workbench/internal/apperror"
	"cyberagent-workbench/internal/fileedit"
	"cyberagent-workbench/internal/idgen"
	"cyberagent-workbench/internal/policy"
	"cyberagent-workbench/internal/runmutation"
	"cyberagent-workbench/internal/runner"
	"cyberagent-workbench/internal/store"
	"cyberagent-workbench/internal/toolgateway"
	"cyberagent-workbench/internal/workspacecheckpoint"
)

type workspaceBoundaryJobReader struct {
	WorkspaceCheckpointStore
	job runner.CommandRuntimeJob
	err error
}

func (s workspaceBoundaryJobReader) GetCommandRuntimeJob(context.Context, string) (runner.CommandRuntimeJob, error) {
	return s.job, s.err
}

type workspaceBoundaryAfterPrecheckStore struct {
	*store.SQLiteStore
	reads int
}

func (s *workspaceBoundaryAfterPrecheckStore) ListOpenWorkspaceCheckpointTransactions(ctx context.Context, limit int) ([]workspacecheckpoint.Transaction, error) {
	s.reads++
	if s.reads == 1 {
		return nil, nil
	}
	return s.SQLiteStore.ListOpenWorkspaceCheckpointTransactions(ctx, limit)
}

func TestFileApplyBlockedByCommandJobOffersSameJobRecovery(t *testing.T) {
	ctx := t.Context()
	state, run, root, lease, capabilities := newCommandRuntimeTestRuntime(t, ctx)
	manager, err := runner.NewPlatformCommandRuntimeManager(state, idgen.New("workspace-boundary-feedback-owner"))
	if err != nil {
		t.Skipf("platform command runtime unavailable: %v", err)
	}
	t.Cleanup(func() {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := manager.Shutdown(shutdownCtx); err != nil {
			t.Errorf("shutdown command runtime: %v", err)
		}
	})
	service, err := NewCommandRuntimeService(state, manager, capabilities)
	if err != nil {
		t.Fatal(err)
	}
	workspace, err := state.GetWorkspaceByID(ctx, "workspace-command-runtime-app")
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(workspace.RootPath, "target.txt")
	if err := os.WriteFile(path, []byte("before\n"), 0600); err != nil {
		t.Fatal(err)
	}
	edit, err := fileedit.NewManager(state).Propose(ctx, fileedit.Proposal{
		SessionID: run.SessionID, WorkspaceID: workspace.ID, WorkspaceRoot: workspace.RootPath,
		Path: "target.txt", ProposedText: "after\n", ExpectedOriginalHash: fileedit.HashText("before\n"),
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := NewFileEditReviewService(state).Review(ctx, ReviewFileEditRequest{
		Version: FileEditReviewProtocolVersion, RunID: run.ID, EditID: edit.ID, Action: FileEditApproveIntent,
	}); err != nil {
		t.Fatal(err)
	}
	profile, script := runner.CommandRuntimeBash, "sleep 30"
	if runtime.GOOS == "windows" {
		profile, script = runner.CommandRuntimePowerShell, "Start-Sleep -Seconds 30"
	}
	start := toolgateway.CommandRuntimeInput{Version: toolgateway.CommandRuntimeToolProtocolVersion,
		Action: toolgateway.CommandRuntimeActionStart, Commands: []runner.CommandRuntimeSpec{{
			Version: runner.CommandRuntimeProtocolVersion, Profile: profile, Script: script,
			WorkingDirectory: ".", Environment: []runner.CommandRuntimeEnvironment{},
			StdinPolicy: runner.CommandRuntimeStdinClosed, CloseInitialStdin: true,
			TimeoutMilliseconds: 30_000, Output: runner.CommandRuntimeOutputPolicy{InlineBytes: 4096, ArtifactBytes: 4096},
			Network: runner.CommandRuntimeNetworkDisabled, Credentials: runner.CommandRuntimeCredentialsNone,
			Purpose: "hold this Run's command mutation boundary while a file edit is attempted",
		}}}
	scope := commandRuntimeTestScope(t, ctx, state, service, run, root, lease, "boundary-feedback")
	f := commandFixtureForScope(t, state, service, scope)
	started, err := f.execute(t, ctx, start, 1)
	if errors.Is(err, runner.ErrCommandRuntimeUnavailable) {
		t.Skipf("%s unavailable: %v", profile, err)
	}
	if err != nil || len(started.Jobs) != 1 || started.Jobs[0].State.Terminal() {
		t.Fatalf("background start: %+v %v", started, err)
	}
	jobID := started.Jobs[0].ID
	applyArguments, err := json.Marshal(toolgateway.WorkspaceApplyPayload{
		Version: toolgateway.AgentCodeRegistryVersion, EditID: edit.ID, ExpectedAction: "replace",
		ExpectedOriginalSHA256: edit.OriginalHash, ExpectedProposedSHA256: edit.ProposedHash,
	})
	if err != nil {
		t.Fatal(err)
	}
	applyKey := func() string {
		return runmutation.SupervisorToolOperationKey(run.ID, f.turn.Checkpoint.NextTurn,
			string(toolgateway.WorkspaceApplyTool), string(applyArguments))
	}
	apply := NewFileEditApplyService(state, policy.NewDefaultChecker(), service.checkpoints).
		WithExecutionPermissionCapabilities(capabilities)
	request := ApplyFileEditRequest{Version: fileedit.FileEditApplyProtocolVersion,
		RunID: run.ID, EditID: edit.ID, OperationKey: applyKey(), AppliedBy: "test_operator",
		LeaseID: lease.LeaseID, LeaseGeneration: lease.Generation}
	_, err = apply.Apply(ctx, request)
	if apperror.CodeOf(err) != apperror.CodeConflict || !strings.Contains(err.Error(), jobID) ||
		!strings.Contains(err.Error(), `"action":"cancel"`) || !strings.Contains(err.Error(), `"wait_milliseconds":1000`) ||
		!strings.Contains(err.Error(), "original apply_arguments") ||
		!strings.Contains(err.Error(), "only if this Job was the dev service needed for the task") {
		t.Fatalf("file edit did not explain same-Job recovery: %v", err)
	}
	conflict := err.Error()
	jsonStart, jsonEnd := strings.Index(conflict, "{"), strings.Index(conflict, "}")
	var cancelInput toolgateway.CommandRuntimeInput
	if jsonStart < 0 || jsonEnd < jsonStart || json.Unmarshal([]byte(conflict[jsonStart:jsonEnd+1]), &cancelInput) != nil ||
		cancelInput.Validate() != nil || cancelInput.JobID != jobID {
		t.Fatalf("recovery instruction is not a valid same-Job command: %s", conflict)
	}
	if data, err := os.ReadFile(path); err != nil || string(data) != "before\n" {
		t.Fatalf("blocked edit wrote the file: %q %v", data, err)
	}
	if _, _, found, err := state.GetFileEditApplyOperation(ctx,
		runmutation.FileEditApplyOperationDigest(run.ID, edit.ID, request.OperationKey)); err != nil || found {
		t.Fatalf("confirmed command conflict retained an unstarted file intent: found=%t err=%v", found, err)
	}
	open, err := state.ListOpenWorkspaceCheckpointTransactions(ctx, 10)
	if err != nil || len(open) != 1 || open[0].TriggerReceiptID != jobID {
		t.Fatalf("blocked edit changed the open command boundary: %+v %v", open, err)
	}
	job, err := state.GetCommandRuntimeJob(ctx, jobID)
	if err != nil {
		t.Fatal(err)
	}
	binding, err := service.checkpoints.loadBinding(ctx, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name      string
		change    func(*runner.CommandRuntimeJob)
		lookupErr error
	}{
		{name: "missing job", lookupErr: errors.New("receipt unavailable")},
		{name: "different Run", change: func(j *runner.CommandRuntimeJob) { j.RunID = "other-run" }},
		{name: "different workspace", change: func(j *runner.CommandRuntimeJob) { j.WorkspaceID = "other-workspace" }},
		{name: "different operation", change: func(j *runner.CommandRuntimeJob) { j.OperationDigest = strings.Repeat("a", 64) }},
	} {
		t.Run(test.name, func(t *testing.T) {
			observed := job
			if test.change != nil {
				test.change(&observed)
			}
			service.checkpoints.store = workspaceBoundaryJobReader{WorkspaceCheckpointStore: state, job: observed, err: test.lookupErr}
			err := service.checkpoints.workspaceMutationBoundaryConflict(ctx, binding, workspacecheckpoint.TransactionFileTool, open[0])
			if apperror.CodeOf(err) != apperror.CodeConflict || strings.Contains(err.Error(), jobID) || strings.Contains(err.Error(), `"action":"cancel"`) {
				t.Fatalf("unverified receipt produced a Job recovery instruction: %v", err)
			}
		})
	}
	service.checkpoints.store = state
	t.Run("command boundary appears after precheck", func(t *testing.T) {
		lateEdit, err := fileedit.NewManager(state).Propose(ctx, fileedit.Proposal{
			SessionID: run.SessionID, WorkspaceID: workspace.ID, WorkspaceRoot: workspace.RootPath,
			Path: "target.txt", ProposedText: "late\n", ExpectedOriginalHash: edit.OriginalHash,
		})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := NewFileEditReviewService(state).Review(ctx, ReviewFileEditRequest{
			Version: FileEditReviewProtocolVersion, RunID: run.ID, EditID: lateEdit.ID, Action: FileEditApproveIntent,
		}); err != nil {
			t.Fatal(err)
		}
		lateStore := &workspaceBoundaryAfterPrecheckStore{SQLiteStore: state}
		service.checkpoints.store = lateStore
		defer func() { service.checkpoints.store = state }()
		lateRequest := request
		lateRequest.EditID, lateRequest.OperationKey = lateEdit.ID, "late-command-boundary-apply"
		if _, err := apply.Apply(ctx, lateRequest); apperror.CodeOf(err) != apperror.CodeConflict ||
			!strings.Contains(err.Error(), jobID) || lateStore.reads != 2 {
			t.Fatalf("final boundary check was bypassed: reads=%d err=%v", lateStore.reads, err)
		}
		if data, err := os.ReadFile(path); err != nil || string(data) != "before\n" {
			t.Fatalf("late command conflict wrote the file: %q %v", data, err)
		}
	})
	round := 1
	cancelAndWait := func(input toolgateway.CommandRuntimeInput) {
		round++
		stopped, err := f.execute(t, ctx, input, round)
		if err != nil || len(stopped.Jobs) != 1 {
			t.Fatalf("same-Job cancellation: %+v %v", stopped, err)
		}
		deadline := time.Now().Add(5 * time.Second)
		for !stopped.Jobs[0].State.Terminal() && time.Now().Before(deadline) {
			round++
			cursor, maxBytes, wait := uint64(0), 4096, 1000
			stopped, err = f.execute(t, ctx, toolgateway.CommandRuntimeInput{
				Version: toolgateway.CommandRuntimeToolProtocolVersion, Action: toolgateway.CommandRuntimeActionWait,
				JobID: input.JobID, Cursor: &cursor, MaxBytes: &maxBytes, WaitMilliseconds: &wait,
			}, round)
			if err != nil || len(stopped.Jobs) != 1 {
				t.Fatalf("waiting for the same Job: %+v %v", stopped, err)
			}
		}
		if !stopped.Jobs[0].State.Terminal() || !stopped.Jobs[0].TreeReaped {
			t.Fatalf("same-Job cancellation did not finish: %+v", stopped)
		}
	}
	cancelAndWait(cancelInput)
	f.nextTurn(t, lease)
	previousKey := request.OperationKey
	request.OperationKey = applyKey()
	if request.OperationKey == previousKey {
		t.Fatal("same apply arguments did not receive a new Supervisor turn key")
	}
	applied, err := apply.Apply(ctx, request)
	if err != nil || !applied.FileWritten || applied.Edit.Status != fileedit.StatusApplied {
		t.Fatalf("original file apply did not recover after command completion: %+v %v", applied, err)
	}
	if data, err := os.ReadFile(path); err != nil || string(data) != "after\n" {
		t.Fatalf("recovered edit bytes: %q %v", data, err)
	}
	f.nextTurn(t, lease)
	round = 0
	round++
	restarted, err := f.execute(t, ctx, start, round)
	if err != nil || len(restarted.Jobs) != 1 || restarted.Jobs[0].ID == jobID || restarted.Jobs[0].State.Terminal() {
		t.Fatalf("service restart after the edit reused the old Job: %+v %v", restarted, err)
	}
	replayed, err := apply.Apply(ctx, request)
	if err != nil || !replayed.Replayed || replayed.FileWritten || replayed.Result.EventSequence != applied.Result.EventSequence {
		t.Fatalf("new command boundary blocked or rewrote an already completed edit: %+v %v", replayed, err)
	}
	cancelInput.JobID = restarted.Jobs[0].ID
	cancelAndWait(cancelInput)
}
