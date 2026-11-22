package httpapi

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"cyberagent-workbench/internal/application"
	"cyberagent-workbench/internal/credential"
	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/githubreview"
	"cyberagent-workbench/internal/policy"
	"cyberagent-workbench/internal/repository"
	"cyberagent-workbench/internal/store"
	"cyberagent-workbench/internal/toolgateway"
)

type threadPullRequestReadBodyController struct {
	ThreadPullRequestController
	called int
}

func (c *threadPullRequestReadBodyController) Discover(context.Context, string, string, string) (application.ThreadPullRequestDiscovery, error) {
	c.called++
	return application.ThreadPullRequestDiscovery{}, nil
}

func (c *threadPullRequestReadBodyController) Observe(context.Context, string, string) (application.ThreadPullRequestResult, error) {
	c.called++
	return application.ThreadPullRequestResult{}, nil
}

func TestThreadPullRequestHTTPReadBodyRejectedBeforeController(t *testing.T) {
	const token = "read-only-pull-request-body-fixture-token"
	for _, route := range []string{"", "/request?operation_key=original-pr-key"} {
		for _, body := range []struct {
			name    string
			length  int64
			chunked bool
			status  int
		}{
			{name: "empty", length: 0, status: http.StatusOK},
			{name: "positive_content_length", length: 2, status: http.StatusBadRequest},
			{name: "unknown_content_length", length: -1, status: http.StatusBadRequest},
			{name: "transfer_encoding", length: 0, chunked: true, status: http.StatusBadRequest},
		} {
			t.Run(route+"/"+body.name, func(t *testing.T) {
				controller := &threadPullRequestReadBodyController{}
				api := &API{threadPullRequestController: controller, githubReviewControlEnabled: true, tokenHash: sha256.Sum256([]byte(token))}
				request := httptest.NewRequest(http.MethodGet, "http://127.0.0.1/api/v1/threads/thread-body/pull-request"+route, nil)
				request.RemoteAddr = "127.0.0.1:49152"
				request.Header.Set("Authorization", "Bearer "+token)
				request.ContentLength = body.length
				if body.chunked {
					request.TransferEncoding = []string{"chunked"}
				}
				response := httptest.NewRecorder()
				api.ServeHTTP(response, request)
				if response.Code != body.status {
					t.Fatalf("status=%d want=%d body=%s", response.Code, body.status, response.Body.String())
				}
				wantCalled := 0
				if body.status == http.StatusOK {
					wantCalled = 1
				}
				if controller.called != wantCalled {
					t.Fatalf("controller calls=%d want=%d", controller.called, wantCalled)
				}
			})
		}
	}
}

// This fixture uses the real HTTP routes, SQLite, Git, approval service and
// authenticated GitHub client. Only the external github.com server is fixed.
func TestThreadPullRequestHTTPApprovalLostReplyAndReadOnlyRestart(t *testing.T) {
	ctx := t.Context()
	dbPath := filepath.Join(t.TempDir(), "pr.db")
	st, err := store.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.Close() }()
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
	git("config", "user.email", "fixture@example.invalid")
	git("config", "user.name", "PR fixture")
	git("config", "core.autocrlf", "false")
	if err = os.WriteFile(filepath.Join(root, "README.md"), []byte("Explicit test fixture\n"), 0600); err != nil {
		t.Fatal(err)
	}
	git("add", "README.md")
	git("commit", "-qm", "baseline")
	base := git("rev-parse", "HEAD")
	git("branch", "-M", "main")
	git("checkout", "-qb", "feature/pr")
	if err = os.WriteFile(filepath.Join(root, "change.txt"), []byte("reviewed fixture change\n"), 0600); err != nil {
		t.Fatal(err)
	}
	git("add", "change.txt")
	git("commit", "-qm", "reviewed change")
	head := git("rev-parse", "HEAD")
	git("remote", "add", "origin", "https://github.com/acme/widget.git")
	workspace := store.WorkspaceRecord{ID: "workspace-http-pr", Name: "HTTP PR fixture", RootPath: root, CreatedAt: time.Now().UTC()}
	if err = st.SaveWorkspace(ctx, workspace); err != nil {
		t.Fatal(err)
	}
	runs := application.NewRunService(st)
	_, run, err := runs.Create(ctx, application.CreateRunRequest{Goal: "Explicit GitHub protocol fixture", Profile: "code", WorkspaceID: workspace.ID, Budget: domain.Budget{MaxTurns: 8, MaxToolCalls: 20, MaxTokens: 4000}})
	if err != nil {
		t.Fatal(err)
	}
	_, err = application.NewRunExecutionProfileService(st).Change(ctx, application.ChangeRunExecutionProfileRequest{RunID: run.ID, Profile: "local", OperationKey: "http-pr-local-profile", RequestedBy: "fixture", Reason: "isolated Git fixture"})
	if err != nil {
		t.Fatal(err)
	}
	caps := domain.ExecutionPermissionRuntimeCapabilities{OperatorApprovalEnabled: true, RuntimeAuthority: domain.NewExecutionPermissionRuntimeAuthority()}
	for _, mode := range []domain.RunExecutionPermissionMode{domain.RunExecutionPermissionAuto, domain.RunExecutionPermissionAsk} {
		selected, err := application.NewRunExecutionPermissionService(st, caps).Change(ctx, application.ChangeRunExecutionPermissionRequest{
			RunID: run.ID, Mode: string(mode), OperationKey: "http-pr-permission-" + string(mode),
			RequestedBy: "fixture", Reason: "require exact approval for the selected remote operation",
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
	var mu sync.Mutex
	posts := 0
	remoteBody := ""
	remoteCreated := false
	remotePR := func() map[string]any {
		return map[string]any{"number": 17, "node_id": "PR17", "state": "open", "title": "HTTP draft", "body": remoteBody, "draft": true, "merged": false, "updated_at": time.Date(2026, 9, 11, 5, 0, 0, 0, time.UTC), "base": map[string]any{"ref": "main", "sha": base, "repo": map[string]any{"full_name": "acme/widget", "node_id": "R_widget"}}, "head": map[string]any{"ref": "feature/pr", "sha": head, "repo": map[string]any{"full_name": "acme/widget"}}}
	}
	remote := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		if r.Header.Get("Authorization") != "Bearer fixture-http-pr-token" {
			t.Error("missing exact fixture credential")
		}
		var v any
		switch r.URL.Path {
		case "/user":
			v = map[string]any{"login": "fixture-user"}
		case "/repos/acme/widget":
			v = map[string]any{"full_name": "acme/widget", "node_id": "R_widget", "default_branch": "main", "permissions": map[string]bool{"push": true}}
		case "/repos/acme/widget/branches/main":
			v = map[string]any{"name": "main", "commit": map[string]string{"sha": base}}
		case "/repos/acme/widget/branches/feature/pr":
			v = map[string]any{"name": "feature/pr", "commit": map[string]string{"sha": head}}
		case "/repos/acme/widget/pulls":
			if r.Method == "POST" {
				posts++
				var payload map[string]any
				_ = json.NewDecoder(r.Body).Decode(&payload)
				if payload["draft"] != true {
					t.Error("not a native draft")
				}
				remoteBody, _ = payload["body"].(string)
				remoteCreated = true
				c, _, e := w.(http.Hijacker).Hijack()
				if e != nil {
					t.Error(e)
					return
				}
				_ = c.Close()
				return
			}
			v = []any{}
			if remoteCreated {
				v = []any{remotePR()}
			}
		case "/repos/acme/widget/pulls/17":
			v = remotePR()
		default:
			http.NotFound(w, r)
			return
		}
		_ = json.NewEncoder(w).Encode(v)
	}))
	defer remote.Close()
	credentials := credential.NewMemoryStore()
	_ = credentials.Put(ctx, "http-pr-token", "fixture-http-pr-token")
	advanced, err := repository.NewAdvancedExecutor(filepath.Join(t.TempDir(), "managed"), true)
	if err != nil {
		t.Fatal(err)
	}
	local, err := repository.NewMutationExecutor()
	if err != nil {
		t.Fatal(err)
	}
	buildAPI := func() (*API, *application.GitHubReviewService, *application.ThreadGitService) {
		t.Helper()
		review, e := application.NewGitHubReviewServiceForTest(st, credentials, advanced, caps, remote.URL, remote.Client())
		if e != nil {
			t.Fatal(e)
		}
		gitSvc := application.NewThreadGitService(st, local, nil, nil, nil, caps)
		pr := application.NewThreadPullRequestService(st, review, gitSvc)
		approvalSvc := application.NewApprovalControlService(st, toolgateway.New(st, policy.NewDefaultChecker()), policy.NewDefaultChecker())
		a, e := New(st, Config{AccessToken: testAccessToken, ControlToken: testControlToken, RunControlEnabled: true, ExecutionPermissionControlEnabled: true, ExecutionPermissionCapabilities: caps, ApprovalControlEnabled: true, ApprovalController: approvalSvc, GitHubReviewControlEnabled: true, GitHubReviewController: review, ThreadPullRequestController: pr, AppVersion: "http-pr-fixture"})
		if e != nil {
			t.Fatal(e)
		}
		return a, review, gitSvc
	}
	api, review, gitSvc := buildAPI()
	repo, _ := githubreview.ParseRepository("acme/widget")
	configured, err := review.Configure(ctx, application.GitHubReviewConfigureRequest{ProtocolVersion: application.GitHubReviewAPIProtocolVersion, Repository: repo, Credential: githubreview.CredentialReference{Name: "http-pr-token", Kind: githubreview.AuthFineGrainedPAT}, AllowedLogHosts: []string{}, WriteEnabled: true, Enabled: true, RequestedBy: "fixture"})
	if err != nil {
		t.Fatal(err)
	}
	bound, err := gitSvc.CaptureThreadGitContext(ctx, thread.ID)
	if err != nil {
		t.Fatal(err)
	}
	request := func(method, path, token, key string, body any) *httptest.ResponseRecorder {
		t.Helper()
		raw, _ := json.Marshal(body)
		r := httptest.NewRequest(method, "http://127.0.0.1"+path, bytes.NewReader(raw))
		if method == "GET" {
			r = httptest.NewRequest(method, "http://127.0.0.1"+path, nil)
		}
		r.RemoteAddr = "127.0.0.1:12345"
		r.Header.Set("Authorization", "Bearer "+token)
		if method == "POST" {
			r.Header.Set("Content-Type", "application/json")
		}
		if key != "" {
			r.Header.Set("Idempotency-Key", key)
		}
		w := httptest.NewRecorder()
		api.ServeHTTP(w, r)
		return w
	}
	path := "/api/v1/threads/" + thread.ID + "/pull-request"
	previewBody := application.ThreadPullRequestPreviewRequest{Version: application.ThreadPullRequestVersion, ConnectionID: configured.Connection.ID, BaseBranch: "main", Title: "HTTP draft", Body: "Actual local fixture", ExpectedRunID: run.ID, ExpectedHeadSHA: bound.HeadSHA, OperationKey: "http-pr-original-key"}
	if denied := request("POST", path+"/preview", testAccessToken, "", previewBody); denied.Code != 401 {
		t.Fatalf("read token wrote: %s", denied.Body.String())
	}
	pResponse := request("POST", path+"/preview", testControlToken, "", previewBody)
	var p struct {
		Data application.ThreadPullRequestPreviewResult
	}
	_ = json.Unmarshal(pResponse.Body.Bytes(), &p)
	if pResponse.Code != 200 || p.Data.Preview == nil || p.Data.Approval == nil {
		t.Fatalf("preview%d %s", pResponse.Code, pResponse.Body.String())
	}
	approvalPath := "/api/v1/runs/" + run.ID + "/approvals/" + p.Data.Approval.ID + "/decision"
	approve := request("POST", approvalPath, testControlToken, "http-pr-original-approve", map[string]string{"version": "approval_control.v1", "action": "approve_once"})
	if approve.Code != 202 || !strings.Contains(approve.Body.String(), `"execution_resumed":false`) || !strings.Contains(approve.Body.String(), `"retry_scheduled":false`) {
		t.Fatalf("approve%d %s", approve.Code, approve.Body.String())
	}
	created := request("POST", path+"/create", testControlToken, "", application.ThreadPullRequestCreateRequest{Version: application.ThreadPullRequestVersion, OperationID: p.Data.Preview.OperationID, ApprovalID: p.Data.Approval.ID})
	if created.Code != 200 || !strings.Contains(created.Body.String(), `"state":"unknown"`) {
		t.Fatalf("lost reply%d %s", created.Code, created.Body.String())
	}
	before, _, err := st.GetRemoteOperation(ctx, p.Data.Preview.OperationID)
	if err != nil || before.StartedAt == nil || before.CompletedAt != nil {
		t.Fatalf("unknown ledger %#v %v", before, err)
	}
	eventsBefore, _ := st.ListRunEvents(ctx, run.ID)
	if err = st.Close(); err != nil {
		t.Fatal(err)
	}
	st, err = store.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	api, _, _ = buildAPI()
	for i := 0; i < 2; i++ {
		observed := request("GET", path+"/request?operation_key=http-pr-original-key", testAccessToken, "", nil)
		if observed.Code != 200 || !strings.Contains(observed.Body.String(), `"state":"created"`) || !strings.Contains(observed.Body.String(), `"receipt_saved":false`) {
			t.Fatalf("observe%d %s", observed.Code, observed.Body.String())
		}
	}
	missing := request("GET", path+"/request?operation_key=never-arrived-key", testAccessToken, "", nil)
	if missing.Code != 200 || !strings.Contains(missing.Body.String(), `"state":"not_received"`) {
		t.Fatal(missing.Body.String())
	}
	after, _, _ := st.GetRemoteOperation(ctx, p.Data.Preview.OperationID)
	eventsAfter, _ := st.ListRunEvents(ctx, run.ID)
	mu.Lock()
	defer mu.Unlock()
	if posts != 1 || before.SpecJSON != after.SpecJSON || after.CompletedAt != nil || len(eventsBefore) != len(eventsAfter) {
		t.Fatalf("observer mutated: posts%d", posts)
	}
	if current, _ := st.GetRun(ctx, run.ID); current.Status != domain.RunPaused {
		t.Fatal("approval or recovery resumed the model")
	}
}
