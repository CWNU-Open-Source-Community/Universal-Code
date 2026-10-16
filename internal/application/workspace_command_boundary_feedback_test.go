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
	"cyberagent-workbench/internal/runner"
	"cyberagent-workbench/internal/toolgateway"
)

type workspaceBoundaryJobReader struct {
	WorkspaceCheckpointStore
	job runner.CommandRuntimeJob
	err error
}

func (s workspaceBoundaryJobReader) GetCommandRuntimeJob(context.Context, string) (runner.CommandRuntimeJob, error) {
	return s.job, s.err
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
	apply := NewFileEditApplyService(state, policy.NewDefaultChecker(), service.checkpoints).
		WithExecutionPermissionCapabilities(capabilities)
	request := ApplyFileEditRequest{Version: fileedit.FileEditApplyProtocolVersion,
		RunID: run.ID, EditID: edit.ID, OperationKey: "boundary-feedback-apply", AppliedBy: "test_operator",
		LeaseID: lease.LeaseID, LeaseGeneration: lease.Generation}
	_, err = apply.Apply(ctx, request)
	if apperror.CodeOf(err) != apperror.CodeConflict || !strings.Contains(err.Error(), jobID) ||
		!strings.Contains(err.Error(), `"action":"cancel"`) || !strings.Contains(err.Error(), `"wait_milliseconds":1000`) ||
		!strings.Contains(err.Error(), "original apply_arguments") || !strings.Contains(err.Error(), "restart") {
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
	open, err := state.ListOpenWorkspaceCheckpointTransactions(ctx, 10)
	if err != nil || len(open) != 1 || open[0].TriggerReceiptID != jobID {
		t.Fatalf("blocked edit changed the open command boundary: %+v %v", open, err)
	}
	job, err := state.GetCommandRuntimeJob(ctx, jobID)
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
			_, err := apply.Apply(ctx, request)
			if apperror.CodeOf(err) != apperror.CodeConflict || strings.Contains(err.Error(), jobID) || strings.Contains(err.Error(), `"action":"cancel"`) {
				t.Fatalf("unverified receipt produced a Job recovery instruction: %v", err)
			}
		})
	}
	service.checkpoints.store = state
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
	cancelInput.JobID = restarted.Jobs[0].ID
	cancelAndWait(cancelInput)
}
