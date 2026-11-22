package httpapi

import (
	"encoding/json"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"cyberagent-workbench/internal/application"
	"cyberagent-workbench/internal/commandruntimeadapter"
	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/fileedit"
	"cyberagent-workbench/internal/policy"
	"cyberagent-workbench/internal/repository"
	"cyberagent-workbench/internal/store"
)

func TestFileEditHTTPKeepsConfiguredTargetAndSourceHistoryDistinct(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git unavailable")
	}
	ctx := t.Context()
	root := t.TempDir()
	git := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", root}, args...)...)
		hideSubprocessWindow(cmd)
		cmd.Env = append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1", "GIT_TERMINAL_PROMPT=0")
		if output, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v %s", args, err, output)
		}
	}
	git("init", "-q", "-b", "main")
	git("config", "user.email", "file-scope@example.invalid")
	git("config", "user.name", "File Scope Test")
	git("config", "core.autocrlf", "false")
	if err := os.WriteFile(filepath.Join(root, "same.txt"), []byte("committed target\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	git("add", "same.txt")
	git("commit", "-qm", "baseline")
	state, err := store.Open(filepath.Join(t.TempDir(), "scope.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = state.Close() })
	workspace := store.WorkspaceRecord{ID: "source-file-scope", Name: "source", RootPath: root}
	if err := state.SaveWorkspace(ctx, workspace); err != nil {
		t.Fatal(err)
	}
	runs := application.NewRunService(state)
	_, run, err := runs.Create(ctx, application.CreateRunRequest{Goal: "read precise file scopes", Profile: "code", WorkspaceID: workspace.ID,
		Budget: domain.Budget{MaxTurns: 4, MaxToolCalls: 8}})
	if err != nil {
		t.Fatal(err)
	}
	run, err = runs.Start(ctx, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	run, err = runs.Pause(ctx, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "same.txt"), []byte("user source\r\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	manager := fileedit.NewManager(state)
	sourceEdit, err := manager.Propose(ctx, fileedit.Proposal{SessionID: run.SessionID, WorkspaceID: workspace.ID,
		WorkspaceRoot: root, Path: "same.txt", ProposedText: "old source proposal\r\n"})
	if err != nil {
		t.Fatal(err)
	}
	executor, err := repository.NewRunWorktreeExecutor(filepath.Join(t.TempDir(), "managed"))
	if err != nil {
		t.Fatal(err)
	}
	drydocks, err := application.NewRunWorktreeService(state, executor)
	if err != nil {
		t.Fatal(err)
	}
	// Startup readiness is a test fixture; this test never executes a command.
	preset, err := application.NewStandardCodePresetService(state, drydocks, application.CapabilityReadinessRuntime{
		RunControlEnabled: true, RunExecutionEnabled: true, ExecutionPermissionControlEnabled: true, StandardCodePresetEnabled: true,
		ExecutionPermissionCapabilities: domain.ExecutionPermissionRuntimeCapabilities{WorkspaceSandboxEnabled: true, OperatorApprovalEnabled: true},
		LocalSandboxInstalled:           true, LocalSandboxProven: true, LocalBackendReady: true,
		CommandRuntimeAdapters: []commandruntimeadapter.Identity{commandruntimeadapter.SandboxedWorkspace(
			application.CommandRuntimeLocalSandboxBackend, "http-file-scope", "http-file-scope-generation")}})
	if err != nil {
		t.Fatal(err)
	}
	request := application.ConfigureStandardCodeRequest{Version: domain.StandardCodePresetProtocolVersion, RunID: run.ID,
		Action: "configure", BackendIntent: "local", OperationKey: "http-file-scope-configure", RequestedBy: "test_operator"}
	preview, err := preset.Configure(ctx, request)
	if err != nil || !preview.TrustRequired {
		t.Fatalf("trust: %+v %v", preview, err)
	}
	request.ConfirmWorkspaceTrust, request.ExpectedTrustDigest = true, preview.TrustDigest
	configured, err := preset.Configure(ctx, request)
	if err != nil || configured.Status != application.StandardCodeResultConfigured {
		t.Fatalf("configure: %+v %v", configured, err)
	}
	owned, found, err := state.GetDrydockByRun(ctx, run.ID)
	if err != nil || !found {
		t.Fatalf("Drydock: %t %v", found, err)
	}
	targetEdit, err := manager.Propose(ctx, fileedit.Proposal{SessionID: run.SessionID, WorkspaceID: owned.WorkspaceID,
		WorkspaceRoot: owned.Path, Path: "same.txt", ProposedText: "new target proposal\n"})
	if err != nil {
		t.Fatal(err)
	}
	api, err := New(state, Config{AccessToken: testAccessToken, ControlToken: testControlToken, FileWorkspaceDrydocks: drydocks,
		FileEditReviewEnabled: true, FileEditReviewController: application.NewFileEditReviewService(state).WithRunWorktree(drydocks),
		FileEditApplyEnabled: true, FileEditApplyController: application.NewFileEditApplyService(state, policy.NewDefaultChecker()).WithRunWorktree(drydocks)})
	if err != nil {
		t.Fatal(err)
	}
	get := func(path string, output any) {
		t.Helper()
		response := performRequest(t, api, http.MethodGet, "/api/v1/runs/"+run.ID+path, testAccessToken, "127.0.0.1:8765", "127.0.0.1:45000", nil)
		if response.Code != http.StatusOK {
			t.Fatalf("GET %s: %d %s", path, response.Code, response.Body.String())
		}
		var envelope struct {
			Data json.RawMessage `json:"data"`
		}
		if err := json.Unmarshal(response.Body.Bytes(), &envelope); err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal(envelope.Data, output); err != nil {
			t.Fatal(err)
		}
		if strings.Contains(response.Body.String(), root) || strings.Contains(response.Body.String(), owned.Path) {
			t.Fatal("file projection leaked an absolute root")
		}
	}
	var queue FileEditQueueView
	get("/file-edits", &queue)
	if len(queue.Items) != 2 {
		t.Fatalf("history missing: %+v", queue)
	}
	var sourceView, targetView FileEditPreviewView
	get("/file-edits/"+sourceEdit.ID, &sourceView)
	get("/file-edits/"+targetEdit.ID, &targetView)
	if sourceView.WorkspaceID != workspace.ID || len(sourceView.AllowedActions) != 0 || sourceView.ApplyEnabled ||
		targetView.WorkspaceID != owned.WorkspaceID || !strings.Contains(targetView.Diff, "-committed target") ||
		!strings.Contains(sourceView.Diff, "-user source") || len(targetView.AllowedActions) != 2 {
		t.Fatalf("source/target were reinterpreted: source=%+v target=%+v", sourceView, targetView)
	}
	var summary FileEditChangeSetView
	get("/file-edit-change-set", &summary)
	if summary.WorkspaceID != owned.WorkspaceID || summary.ReturnedCount != 1 || summary.Items[0].ID != targetEdit.ID {
		t.Fatalf("change set merged source history: %+v", summary)
	}
	for _, edit := range []fileedit.Edit{sourceEdit, targetEdit} {
		approval, err := state.GetApprovalByProposal(ctx, edit.ID)
		if err != nil {
			t.Fatal(err)
		}
		var approvalView ApprovalPreviewView
		get("/approvals/"+approval.ID+"/preview", &approvalView)
		if approvalView.WorkspaceID != edit.WorkspaceID || approvalView.SourceCurrent != (edit.ID == targetEdit.ID) {
			t.Fatalf("approval scope: %+v", approvalView)
		}
	}
	// A lost process-owned resolver must not erase historical records or grant
	// controls. Read-only projection never falls back to the source target.
	api.fileWorkspaceDrydocks = nil
	get("/file-edits/"+targetEdit.ID, &targetView)
	get("/file-edits", &queue)
	if len(queue.Items) != 2 || len(targetView.AllowedActions) != 0 || targetView.ApplyEnabled {
		t.Fatal("unavailable target lost history or advertised mutation")
	}
	response := performRequest(t, api, http.MethodGet, "/api/v1/runs/"+run.ID+"/file-edit-change-set", testAccessToken, "127.0.0.1:8765", "127.0.0.1:45000", nil)
	assertAPIError(t, response, http.StatusPreconditionFailed, "FAILED_PRECONDITION")
	for path, want := range map[string]string{filepath.Join(root, "same.txt"): "user source\r\n", filepath.Join(owned.Path, "same.txt"): "committed target\n"} {
		body, err := os.ReadFile(path)
		if err != nil || string(body) != want {
			t.Fatalf("read projection wrote %s: %q %v", path, body, err)
		}
	}

	t.Run("continued Run lists its own pending inverse", func(t *testing.T) {
		// Reuse the real configured SQLite/Git fixture, apply the old proposal,
		// then publish the ordinary Thread successor. The physical directory's
		// creator Session remains old; the new edit must belong to the new one.
		api.fileWorkspaceDrydocks = drydocks
		checkpoints, err := application.NewWorkspaceCheckpointService(state,
			domain.ExecutionPermissionRuntimeCapabilities{WorkspaceSandboxEnabled: true, OperatorApprovalEnabled: true})
		if err != nil {
			t.Fatal(err)
		}
		drydocks.WithCheckpointService(checkpoints)
		if _, err := runs.Resume(ctx, run.ID); err != nil {
			t.Fatal(err)
		}
		if _, err := application.NewFileEditReviewService(state).WithRunWorktree(drydocks).Review(ctx,
			application.ReviewFileEditRequest{Version: application.FileEditReviewProtocolVersion,
				RunID: run.ID, EditID: targetEdit.ID, Action: application.FileEditApproveIntent}); err != nil {
			t.Fatal(err)
		}
		if result, err := application.NewFileEditApplyService(state, policy.NewDefaultChecker()).WithRunWorktree(drydocks).Apply(ctx,
			application.ApplyFileEditRequest{Version: fileedit.FileEditApplyProtocolVersion,
				RunID: run.ID, EditID: targetEdit.ID, OperationKey: "http-owned-source-apply", AppliedBy: "test_operator"}); err != nil || !result.FileWritten {
			t.Fatalf("apply source: %+v %v", result, err)
		}
		if _, err := runs.Fail(ctx, run.ID, "end this execution before the next operator request"); err != nil {
			t.Fatal(err)
		}
		thread, err := state.GetThreadByRun(ctx, run.ID)
		if err != nil {
			t.Fatal(err)
		}
		next, err := application.NewThreadServiceWithExecutionCapabilities(state,
			domain.ExecutionPermissionRuntimeCapabilities{WorkspaceSandboxEnabled: true, OperatorApprovalEnabled: true}).
			WithRunWorktree(drydocks).Submit(ctx, application.SubmitThreadMessageRequest{
			Version: domain.ThreadMessageProtocolVersion, ThreadID: thread.ID,
			Content: "Propose reversing the earlier edit; wait for my new approval", OperationKey: "http-owned-successor", RequestedBy: "test_operator"})
		if err != nil || !next.SuccessorCreated {
			t.Fatalf("successor: %+v %v", next, err)
		}
		if _, err := runs.Start(ctx, next.Run.ID); err != nil {
			t.Fatal(err)
		}
		inverse, err := application.NewFileEditProposalService(state, policy.NewDefaultChecker()).WithRunWorktree(drydocks).ProposeRevert(ctx,
			application.CreateFileEditRevertProposalRequest{Version: application.FileEditProposalProtocolVersion,
				RunID: next.Run.ID, SourceRunID: run.ID, SourceEditID: targetEdit.ID, Path: targetEdit.Path,
				ExpectedSHA256: targetEdit.ProposedHash, OperationKey: "http-owned-inverse"})
		if err != nil || inverse.Edit.SessionID != next.Run.SessionID || inverse.Edit.WorkspaceID != owned.WorkspaceID || inverse.Edit.Status != fileedit.StatusProposed {
			t.Fatalf("inverse: %+v %v", inverse, err)
		}
		readQueue := func(runID string) FileEditQueueView {
			t.Helper()
			var result FileEditQueueView
			response := performRequest(t, api, http.MethodGet, "/api/v1/runs/"+runID+"/file-edits", testAccessToken, "127.0.0.1:8765", "127.0.0.1:45000", nil)
			decodeDataStatus(t, response, http.StatusOK, &result)
			return result
		}
		current := readQueue(next.Run.ID)
		if len(current.Items) != 1 || current.Items[0].ID != inverse.Edit.ID || current.Items[0].SessionID != next.Run.SessionID ||
			current.Items[0].Status != fileedit.StatusProposed || len(current.Items[0].AllowedActions) != 2 || current.Items[0].ApplyEnabled {
			t.Fatalf("successor pending inverse missing or misrepresented: %+v", current)
		}
		page, err := state.ListRunFileEditPreviewsPage(ctx, next.Run.ID, 0, 1)
		if err != nil || len(page) != 1 || page[0].ID != inverse.Edit.ID {
			t.Fatalf("scoped page: %+v %v", page, err)
		}
		if extra, err := state.ListRunFileEditPreviewsPage(ctx, next.Run.ID, 1, 1); err != nil || len(extra) != 0 {
			t.Fatalf("predecessor edits entered successor pagination: %+v %v", extra, err)
		}
		old := readQueue(run.ID)
		if len(old.Items) != 2 {
			t.Fatalf("old history disappeared or included inverse: %+v", old)
		}
		for _, item := range old.Items {
			if item.ID == inverse.Edit.ID || item.SessionID != run.SessionID || len(item.AllowedActions) != 0 || item.ApplyEnabled {
				t.Fatalf("historical Run claimed current inverse or controls: %+v", item)
			}
		}
		_, other, err := runs.Create(ctx, application.CreateRunRequest{Goal: "different Thread with same source", Profile: "code", WorkspaceID: workspace.ID})
		if err != nil {
			t.Fatal(err)
		}
		if otherQueue := readQueue(other.ID); len(otherQueue.Items) != 0 {
			t.Fatalf("another Thread received shared-source edits: %+v", otherQueue)
		}
		for _, mismatchedRun := range []string{run.ID, other.ID} {
			response := performRequest(t, api, http.MethodGet, "/api/v1/runs/"+mismatchedRun+"/file-edits/"+inverse.Edit.ID,
				testAccessToken, "127.0.0.1:8765", "127.0.0.1:45000", nil)
			assertAPIError(t, response, http.StatusNotFound, "NOT_FOUND")
		}
		var detail FileEditPreviewView
		decodeDataStatus(t, performRequest(t, api, http.MethodGet, "/api/v1/runs/"+next.Run.ID+"/file-edits/"+inverse.Edit.ID,
			testAccessToken, "127.0.0.1:8765", "127.0.0.1:45000", nil), http.StatusOK, &detail)
		if detail.ID != current.Items[0].ID || detail.SessionID != current.Items[0].SessionID || detail.Diff != current.Items[0].Diff {
			t.Fatalf("list/detail diverged: %+v %+v", current, detail)
		}
		api.fileWorkspaceDrydocks = nil
		unavailable := readQueue(next.Run.ID)
		if len(unavailable.Items) != 1 || unavailable.Items[0].ID != inverse.Edit.ID || len(unavailable.Items[0].AllowedActions) != 0 || unavailable.Items[0].ApplyEnabled {
			t.Fatalf("unavailable resolver erased historical data or granted controls: %+v", unavailable)
		}
		for path, want := range map[string]string{filepath.Join(root, "same.txt"): "user source\r\n", filepath.Join(owned.Path, "same.txt"): "new target proposal\n"} {
			body, err := os.ReadFile(path)
			if err != nil || string(body) != want {
				t.Fatalf("inverse proposal or reads wrote %s: %q %v", path, body, err)
			}
		}
	})
}
