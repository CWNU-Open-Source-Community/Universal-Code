package httpapi

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"cyberagent-workbench/internal/application"
	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/policy"
	"cyberagent-workbench/internal/repository"
	"cyberagent-workbench/internal/store"
	"cyberagent-workbench/internal/toolgateway"
)

// Only temporary repositories are mutated. This crosses the actual HTTP,
// approval, SQLite operation and checkpoint boundaries with the real Git binary.
func TestThreadGitHTTPSelectedCommitAuthorizationAndReadOnlyObservation(t *testing.T) {
	ctx := t.Context()
	st, err := store.Open(filepath.Join(t.TempDir(), "git.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	root := t.TempDir()
	git := func(args ...string) string {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", root}, args...)...)
		hideSubprocessWindow(cmd)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %s %v", args, out, err)
		}
		return strings.TrimSpace(string(out))
	}
	git("init", "-q")
	git("config", "user.email", "http-git@example.invalid")
	git("config", "user.name", "HTTP Git fixture")
	git("config", "core.autocrlf", "false")
	if err = os.WriteFile(filepath.Join(root, "README.md"), []byte("baseline\n"), 0600); err != nil {
		t.Fatal(err)
	}
	git("add", "README.md")
	git("commit", "-qm", "baseline")
	git("remote", "add", "unsupported", "git@example.invalid:org/repo.git")
	w := store.WorkspaceRecord{ID: "workspace-http-git", Name: "HTTP Git fixture", RootPath: root, CreatedAt: time.Now().UTC()}
	if err = st.SaveWorkspace(ctx, w); err != nil {
		t.Fatal(err)
	}
	runs := application.NewRunService(st)
	_, run, err := runs.Create(ctx, application.CreateRunRequest{Goal: "Review selected Git files", Profile: "code", WorkspaceID: w.ID, Budget: domain.Budget{MaxTurns: 8, MaxToolCalls: 20, MaxTokens: 4000}})
	if err != nil {
		t.Fatal(err)
	}
	_, err = application.NewRunExecutionProfileService(st).Change(ctx, application.ChangeRunExecutionProfileRequest{RunID: run.ID, Profile: "local", OperationKey: "http-git-local-profile", RequestedBy: "fixture", Reason: "isolated repository"})
	if err != nil {
		t.Fatal(err)
	}
	caps := domain.ExecutionPermissionRuntimeCapabilities{OperatorApprovalEnabled: true, RuntimeAuthority: domain.NewExecutionPermissionRuntimeAuthority()}
	for _, mode := range []domain.RunExecutionPermissionMode{domain.RunExecutionPermissionAuto, domain.RunExecutionPermissionAsk} {
		selected, err := application.NewRunExecutionPermissionService(st, caps).Change(ctx, application.ChangeRunExecutionPermissionRequest{
			RunID: run.ID, Mode: string(mode), OperationKey: "http-git-permission-" + string(mode),
			RequestedBy: "fixture", Reason: "require exact approval for the selected Git operation",
		})
		if err != nil || selected.Permission.Mode != mode || selected.Permission.ExecutionAuthorized || selected.Permission.CapabilityGrant {
			t.Fatalf("current permission selection=%+v err=%v", selected, err)
		}
	}
	if _, err = runs.Start(ctx, run.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = runs.Pause(ctx, run.ID); err != nil {
		t.Fatal(err)
	}
	thread, err := st.GetThreadByRun(ctx, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	local, err := repository.NewMutationExecutor()
	if err != nil {
		t.Fatal(err)
	}
	cp, err := application.NewWorkspaceCheckpointService(st, caps)
	if err != nil {
		t.Fatal(err)
	}
	svc := application.NewThreadGitService(st, local, nil, cp, nil, caps)
	advancedExecutor, err := repository.NewAdvancedExecutor(filepath.Join(t.TempDir(), "managed"), true)
	if err != nil {
		t.Fatal(err)
	}
	advanced, err := application.NewGitAdvancedService(st, advancedExecutor, caps)
	if err != nil {
		t.Fatal(err)
	}
	svc.WithAdvanced(advanced)
	approvalSvc := application.NewApprovalControlService(st, toolgateway.New(st, policy.NewDefaultChecker()), policy.NewDefaultChecker())
	api, err := New(st, Config{AccessToken: testAccessToken, ControlToken: testControlToken, ExecutionPermissionControlEnabled: true, ExecutionPermissionCapabilities: caps, GitAdvancedControlEnabled: true, GitAdvancedController: advanced, ThreadGitController: svc, ApprovalControlEnabled: true, ApprovalController: approvalSvc, WorkspaceCheckpointControlEnabled: true, WorkspaceCheckpointController: cp, AppVersion: "git-http-fixture"})
	if err != nil {
		t.Fatal(err)
	}
	path := "/api/v1/threads/" + thread.ID + "/git"
	request := func(method, suffix, token string, raw []byte) *httptest.ResponseRecorder {
		t.Helper()
		r := httptest.NewRequest(method, "http://127.0.0.1"+path+suffix, bytes.NewReader(raw))
		r.RemoteAddr = "127.0.0.1:12345"
		r.Header.Set("Authorization", "Bearer "+token)
		if method == "POST" {
			r.Header.Set("Content-Type", "application/json")
		}
		out := httptest.NewRecorder()
		api.ServeHTTP(out, r)
		return out
	}
	decode := func(out *httptest.ResponseRecorder, target any) {
		t.Helper()
		if out.Code != 200 {
			t.Fatalf("HTTP %d: %s", out.Code, out.Body.String())
		}
		var envelope struct {
			Data json.RawMessage `json:"data"`
		}
		if err := json.Unmarshal(out.Body.Bytes(), &envelope); err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal(envelope.Data, target); err != nil {
			t.Fatal(err)
		}
	}
	if err = os.WriteFile(filepath.Join(root, "selected.txt"), []byte("exact selected content\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(root, "other.txt"), []byte("unrelated staging\n"), 0600); err != nil {
		t.Fatal(err)
	}
	git("add", "other.txt")
	var state application.ThreadGitState
	decode(request("GET", "", testAccessToken, nil), &state)
	if !state.CanExecute || len(state.RemoteURLs) != 1 || state.RemoteURLs[0].Name != "unsupported" || state.RemoteURLs[0].URL != "" || state.RemoteURLs[0].BlockedReason == "" {
		t.Fatalf("wrong state: %#v", state)
	}
	input := application.ThreadGitPreviewRequest{Version: application.ThreadGitProtocolVersion, RunID: run.ID, Spec: application.ThreadGitSpec{Operation: "commit", Paths: []string{"selected.txt"}, Message: "selected HTTP commit"}}
	raw, _ := json.Marshal(input)
	if out := request("POST", "/preview", testAccessToken, raw); out.Code != http.StatusUnauthorized {
		t.Fatalf("read bearer wrote: %d", out.Code)
	}
	if out := request("POST", "/preview", testControlToken, append(raw[:len(raw)-1], []byte(`,"unexpected":true}`)...)); out.Code != 400 {
		t.Fatalf("unknown field accepted: %d", out.Code)
	}
	var preview application.ThreadGitPreview
	raw, _ = json.Marshal(input)
	decode(request("POST", "/preview", testControlToken, raw), &preview)
	if !strings.Contains(preview.Diff, "+exact selected content") || strings.Contains(preview.Diff, "unrelated staging") {
		t.Fatalf("wrong review diff: %s", preview.Diff)
	}
	execute := application.ThreadGitExecuteRequest{Version: application.ThreadGitProtocolVersion, RunID: run.ID, Spec: input.Spec, OperationKey: "http-git-original", ExpectedPreviewFingerprint: preview.PreviewFingerprint, RequestedBy: "desktop-ui"}
	raw, _ = json.Marshal(execute)
	oldHead := git("rev-parse", "HEAD")
	if err = os.WriteFile(filepath.Join(root, "selected.txt"), []byte("newer unreviewed content\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if out := request("POST", "/execute", testControlToken, raw); out.Code != 409 {
		t.Fatalf("drifted content accepted: %d %s", out.Code, out.Body.String())
	}
	if got := git("rev-parse", "HEAD"); got != oldHead {
		t.Fatal("drifted operation changed HEAD")
	}
	if err = os.WriteFile(filepath.Join(root, "selected.txt"), []byte("exact selected content\n"), 0600); err != nil {
		t.Fatal(err)
	}
	previewRaw, _ := json.Marshal(input)
	decode(request("POST", "/preview", testControlToken, previewRaw), &preview)
	execute.ExpectedPreviewFingerprint = preview.PreviewFingerprint
	raw, _ = json.Marshal(execute)
	var result application.ThreadGitResult
	decode(request("POST", "/execute", testControlToken, raw), &result)
	if result.State != "completed" || !result.ReceiptSaved || result.CommitOID == "" {
		t.Fatalf("wrong result: %#v", result)
	}
	if strings.Contains(git("ls-tree", "--name-only", "HEAD"), "other.txt") || git("show", ":other.txt") != "unrelated staging" {
		t.Fatal("commit changed unrelated staging")
	}
	before, _ := st.ListRunEvents(ctx, run.ID)
	var observed application.ThreadGitResult
	decode(request("GET", "/requests/http-git-original", testAccessToken, nil), &observed)
	if observed.CommitOID != result.CommitOID || !observed.Replayed {
		t.Fatalf("wrong observation: %#v", observed)
	}
	decode(request("POST", "/execute", testControlToken, raw), &observed)
	after, _ := st.ListRunEvents(ctx, run.ID)
	if len(before) != len(after) {
		t.Fatal("GET or exact replay mutated events")
	}
	api.gitAdvancedControlEnabled = false
	decode(request("GET", "", testAccessToken, nil), &state)
	if state.CanExecute || state.BlockedReason == "" {
		t.Fatal("disabled API advertised writable Git")
	}
	if out := request("POST", "/execute", testControlToken, raw); out.Code != 404 {
		t.Fatalf("disabled control accepted write: %d", out.Code)
	}
}
