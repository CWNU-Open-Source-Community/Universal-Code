package application

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"cyberagent-workbench/internal/apperror"
	"cyberagent-workbench/internal/fileedit"
	"cyberagent-workbench/internal/idgen"
	"cyberagent-workbench/internal/policy"
	"cyberagent-workbench/internal/runner"
	"cyberagent-workbench/internal/toolgateway"
	"cyberagent-workbench/internal/workspacecheckpoint"
)

func TestSharedCommandServiceSurvivesEditsAndKeepsCheckpointCursor(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("Node.js is required for the real service integration test")
	}
	ctx := t.Context()
	state, run, root, lease, caps := newCommandRuntimeTestRuntime(t, ctx)
	manager, err := runner.NewPlatformCommandRuntimeManager(state, idgen.New("shared-service-owner"))
	if err != nil {
		t.Skipf("platform command runtime: %v", err)
	}
	t.Cleanup(func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := manager.Shutdown(cleanup); err != nil {
			t.Errorf("service cleanup: %v", err)
		}
	})
	service, err := NewCommandRuntimeService(state, manager, caps)
	if err != nil {
		t.Fatal(err)
	}
	workspace, err := state.GetWorkspaceByID(ctx, "workspace-command-runtime-app")
	if err != nil {
		t.Fatal(err)
	}
	for name, content := range map[string]string{
		"target.txt": "before\n",
		"server.cjs": `const fs = require('node:fs'); const http = require('node:http');
const server = http.createServer((req, res) => {
  if (req.url === '/mutate') fs.writeFileSync('target.txt', 'service changed\n');
  res.end(fs.readFileSync('target.txt'));
});
server.listen(0, '127.0.0.1', () => console.log('http://127.0.0.1:' + server.address().port + '/'));`,
	} {
		if err := os.WriteFile(filepath.Join(workspace.RootPath, name), []byte(content), 0600); err != nil {
			t.Fatal(err)
		}
	}
	start := toolgateway.CommandRuntimeInput{Version: toolgateway.CommandRuntimeToolProtocolVersion,
		Action: toolgateway.CommandRuntimeActionStart, Commands: []runner.CommandRuntimeSpec{{
			Version: runner.CommandRuntimeProtocolVersion, WorkspaceMode: runner.CommandRuntimeWorkspaceSharedService,
			Profile: runner.CommandRuntimeProcess, Executable: node, Arguments: []string{"server.cjs"},
			WorkingDirectory: ".", Environment: []runner.CommandRuntimeEnvironment{},
			StdinPolicy: runner.CommandRuntimeStdinClosed, CloseInitialStdin: true,
			TimeoutMilliseconds: 60_000, Output: runner.CommandRuntimeOutputPolicy{InlineBytes: 4096, ArtifactBytes: 4096},
			Network: runner.CommandRuntimeNetworkHost, Credentials: runner.CommandRuntimeCredentialsNone,
			Purpose: "keep the development server running during file edits",
		}}}
	scope := commandRuntimeTestScope(t, ctx, state, service, run, root, lease, "shared-service")
	f := commandFixtureForScope(t, state, service, scope)
	// Leave the exact started call pending to exercise recovery after process
	// launch but before the Supervisor commits its tool-result acknowledgement.
	launchScope := f.startScope(t, start, 1)
	started, err := service.ExecuteCommandRuntime(ctx, launchScope, start)
	if err != nil || len(started.Jobs) != 1 || started.Jobs[0].State.Terminal() {
		t.Fatalf("start service: %+v %v", started, err)
	}
	jobID, pid := started.Jobs[0].ID, started.Jobs[0].PID
	record, err := state.GetCommandRuntimeJob(ctx, jobID)
	if err != nil {
		t.Fatal(err)
	}
	launchScope.InvocationID = record.InvocationID
	var url string
	for deadline := time.Now().Add(8 * time.Second); time.Now().Before(deadline) && url == ""; {
		job, page, waitErr := manager.Wait(ctx, jobID, 100*time.Millisecond, 0, 4096)
		if waitErr != nil || job.State.Terminal() {
			t.Fatalf("service readiness: %+v %v", job, waitErr)
		}
		for _, frame := range page.Frames {
			if strings.HasPrefix(strings.TrimSpace(frame.Text), "http://127.0.0.1:") {
				url = strings.TrimSpace(frame.Text)
			}
		}
	}
	if url == "" {
		t.Fatal("service did not report its listening URL")
	}
	client := &http.Client{Timeout: time.Second, Transport: &http.Transport{Proxy: nil}}
	t.Cleanup(client.CloseIdleConnections)
	read := func(path string) string {
		t.Helper()
		response, err := client.Get(url + path)
		if err != nil {
			t.Fatal(err)
		}
		defer response.Body.Close()
		body, err := io.ReadAll(response.Body)
		if err != nil || response.StatusCode != http.StatusOK {
			t.Fatalf("read service: status=%d %v", response.StatusCode, err)
		}
		return string(body)
	}
	if got := read(""); got != "before\n" {
		t.Fatalf("initial page: %q", got)
	}
	if open, err := state.ListOpenWorkspaceCheckpointTransactions(ctx, 10); err != nil || len(open) != 0 {
		t.Fatalf("running service retained an exclusive mutation boundary: %+v %v", open, err)
	}
	apply := NewFileEditApplyService(state, policy.NewDefaultChecker(), service.checkpoints).WithExecutionPermissionCapabilities(caps)
	propose := func(before, after string) fileedit.Edit {
		t.Helper()
		edit, err := fileedit.NewManager(state).Propose(ctx, fileedit.Proposal{
			SessionID: run.SessionID, WorkspaceID: workspace.ID, WorkspaceRoot: workspace.RootPath,
			Path: "target.txt", ProposedText: after, ExpectedOriginalHash: fileedit.HashText(before),
		})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := NewFileEditReviewService(state).Review(ctx, ReviewFileEditRequest{
			Version: FileEditReviewProtocolVersion, RunID: run.ID, EditID: edit.ID, Action: FileEditApproveIntent,
		}); err != nil {
			t.Fatal(err)
		}
		return edit
	}
	applyEdit := func(edit fileedit.Edit) error {
		_, err := apply.Apply(ctx, ApplyFileEditRequest{Version: fileedit.FileEditApplyProtocolVersion,
			RunID: run.ID, EditID: edit.ID, OperationKey: "shared-edit-" + edit.ID, AppliedBy: "test_operator",
			LeaseID: lease.LeaseID, LeaseGeneration: lease.Generation})
		return err
	}
	before := "before\n"
	for index := 1; index <= 2; index++ {
		after := fmt.Sprintf("edit %d\n", index)
		if err := applyEdit(propose(before, after)); err != nil {
			t.Fatalf("edit while service remains running: %v", err)
		}
		if got := read(""); got != after {
			t.Fatalf("same service did not observe edit: %q", got)
		}
		before = after
	}
	stale := propose(before, "stale overwrite\n")
	if got := read("mutate"); got != "service changed\n" {
		t.Fatalf("service write fixture: %q", got)
	}
	if err := applyEdit(stale); apperror.CodeOf(err) != apperror.CodeConflict {
		t.Fatalf("service write bypassed file hash conflict: %v", err)
	}
	if got := read(""); got != "service changed\n" {
		t.Fatalf("rejected edit overwrote service data: %q", got)
	}
	cursor, found, err := state.GetWorkspaceCheckpointRunState(ctx, run.ID)
	if err != nil || !found {
		t.Fatalf("checkpoint cursor: %t %v", found, err)
	}
	checkpoint, err := state.GetWorkspaceCheckpoint(ctx, cursor.CurrentCheckpointID)
	if err != nil || !slices.Contains(checkpoint.IncompleteReasons, sharedCommandServiceCheckpointReason) {
		t.Fatalf("concurrent writes lack attribution warning: %+v %v", checkpoint, err)
	}
	if err := requireSharedCommandServicesStopped(ctx, state, workspace.RootPath); apperror.CodeOf(err) != apperror.CodeConflict || !strings.Contains(err.Error(), jobID) {
		t.Fatalf("restore accepted running shared service: %v", err)
	}
	if _, err := service.checkpoints.Restore(ctx, WorkspaceRestoreRequest{
		RunID: run.ID, TargetCheckpointID: cursor.CurrentCheckpointID, ExpectedCurrentCheckpointID: cursor.CurrentCheckpointID,
		OperationKey: "restore-with-service", RequestedBy: "api_operator", Kind: workspacecheckpoint.TransactionRewind, Confirm: true,
	}); apperror.CodeOf(err) != apperror.CodeConflict || !strings.Contains(err.Error(), jobID) {
		t.Fatalf("restore endpoint bypassed the service guard: %v", err)
	}
	// Re-enter the durable launch, exercising a strict Drydock-style replay
	// cursor too. The original Job must survive and no launch may repeat.
	service.checkpoints.strictMutationReplay = true
	replayed, err := service.ExecuteCommandRuntime(ctx, launchScope, start)
	if err != nil || !replayed.Replayed || len(replayed.Jobs) != 1 || replayed.Jobs[0].ID != jobID || replayed.Jobs[0].PID != pid {
		t.Fatalf("replayed service after edits: %+v %v", replayed, err)
	}
	if waiting, err := f.resume(t); err != nil || waiting {
		t.Fatalf("recover the pending launch acknowledgement: waiting=%t %v", waiting, err)
	}
	wait := 1000
	stopped, err := f.execute(t, ctx, toolgateway.CommandRuntimeInput{Version: toolgateway.CommandRuntimeToolProtocolVersion,
		Action: toolgateway.CommandRuntimeActionCancel, JobID: jobID, WaitMilliseconds: &wait}, 2)
	if err != nil || len(stopped.Jobs) != 1 {
		t.Fatalf("cancel exact service: %+v %v", stopped, err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for round := 3; (!stopped.Jobs[0].State.Terminal() || !stopped.Jobs[0].TreeReaped) && time.Now().Before(deadline); round++ {
		outputCursor, maxBytes := uint64(0), 4096
		stopped, err = f.execute(t, ctx, toolgateway.CommandRuntimeInput{
			Version: toolgateway.CommandRuntimeToolProtocolVersion, Action: toolgateway.CommandRuntimeActionWait,
			JobID: jobID, Cursor: &outputCursor, MaxBytes: &maxBytes, WaitMilliseconds: &wait,
		}, round)
		if err != nil || len(stopped.Jobs) != 1 || stopped.Jobs[0].ID != jobID {
			t.Fatalf("wait for exact service cleanup: %+v %v", stopped, err)
		}
	}
	if !stopped.Jobs[0].State.Terminal() || !stopped.Jobs[0].TreeReaped {
		t.Fatalf("service was not reaped after cancellation: %+v", stopped)
	}
	afterStop, found, err := state.GetWorkspaceCheckpointRunState(ctx, run.ID)
	if err != nil || !found || afterStop.CurrentCheckpointID != cursor.CurrentCheckpointID {
		t.Fatalf("service exit moved the editing cursor: %+v %v", afterStop, err)
	}
	if err := requireSharedCommandServicesStopped(ctx, state, workspace.RootPath); err != nil {
		t.Fatalf("reaped service still blocks restore: %v", err)
	}
	t.Logf("same Node Job %s pid=%d served two edits; service write conflicted; replay kept PID; stop preserved checkpoint", jobID, pid)
}

type sharedServiceListFixture struct {
	jobs []runner.CommandRuntimeJob
}

func (f sharedServiceListFixture) ListCommandRuntimeJobs(_ context.Context, _ runner.CommandRuntimeListFilter) ([]runner.CommandRuntimeJob, error) {
	return f.jobs, nil
}

func TestSharedServiceRestoreGuardUsesPhysicalRootAndCleanupProof(t *testing.T) {
	root := t.TempDir()
	sha, err := runner.CommandRuntimeWorkspaceRootSHA256(root)
	if err != nil {
		t.Fatal(err)
	}
	base := runner.CommandRuntimeJob{ID: "service-in-another-run", RunID: "another-run", WorkspaceRootSHA256: sha,
		IntentJSON: `{"workspace_mode":"shared_service"}`, State: runner.CommandRuntimeJobInterrupted, TreeReaped: false}
	for _, tc := range []struct {
		name    string
		mutate  func(*runner.CommandRuntimeJob)
		blocked bool
	}{
		{"terminal but unconfirmed tree", func(*runner.CommandRuntimeJob) {}, true},
		{"running in another Run", func(j *runner.CommandRuntimeJob) { j.State = runner.CommandRuntimeJobRunning }, true},
		{"reaped service", func(j *runner.CommandRuntimeJob) { j.TreeReaped = true }, false},
		{"different physical root", func(j *runner.CommandRuntimeJob) { j.WorkspaceRootSHA256 = strings.Repeat("0", 64) }, false},
		{"ordinary command", func(j *runner.CommandRuntimeJob) { j.IntentJSON = `{}` }, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			job := base
			tc.mutate(&job)
			err := requireSharedCommandServicesStopped(t.Context(), sharedServiceListFixture{[]runner.CommandRuntimeJob{job}}, root)
			if (apperror.CodeOf(err) == apperror.CodeConflict) != tc.blocked {
				t.Fatalf("blocked=%t, err=%v", tc.blocked, err)
			}
		})
	}
}
