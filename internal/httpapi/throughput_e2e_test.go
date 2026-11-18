package httpapi_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"cyberagent-workbench/internal/application"
	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/fileedit"
	"cyberagent-workbench/internal/llm"
	"cyberagent-workbench/internal/policy"
	"cyberagent-workbench/internal/store"
	"cyberagent-workbench/internal/toolgateway"
	"cyberagent-workbench/internal/tools"
	"cyberagent-workbench/internal/httpapi"
)

const (
	e2eAccessToken  = "http-e2e-access-token-0123456789-abcdef"
	e2eControlToken = "http-e2e-control-token-0123456789-abcde"
)

type e2eHTTPProvider struct {
	mu       sync.Mutex
	requests []llm.ChatRequest
	respond  func(context.Context, llm.ChatRequest, int) (*llm.ChatResponse, error)
}

func (*e2eHTTPProvider) Name() string { return "http-e2e" }

func (*e2eHTTPProvider) ListModels(context.Context) ([]llm.ModelInfo, error) {
	return []llm.ModelInfo{{ID: "model", Provider: "http-e2e", Capabilities: []string{"chat", "tools"}}}, nil
}

func (p *e2eHTTPProvider) Chat(ctx context.Context, req llm.ChatRequest) (*llm.ChatResponse, error) {
	p.mu.Lock()
	p.requests = append(p.requests, req)
	idx := len(p.requests) - 1
	respond := p.respond
	p.mu.Unlock()
	if respond == nil {
		return nil, errors.New("e2eHTTPProvider respond is nil")
	}
	return respond(ctx, req, idx)
}

func (p *e2eHTTPProvider) StreamChat(ctx context.Context, req llm.ChatRequest) (<-chan llm.ChatChunk, error) {
	resp, err := p.Chat(ctx, req)
	if err != nil {
		return nil, err
	}
	ch := make(chan llm.ChatChunk, 2)
	if resp.Text != "" {
		ch <- llm.ChatChunk{Text: resp.Text}
	}
	ch <- llm.FinalChatChunk(resp)
	close(ch)
	return ch, nil
}

func (*e2eHTTPProvider) SupportsTools(string) bool    { return true }
func (*e2eHTTPProvider) SupportsVision(string) bool   { return false }
func (*e2eHTTPProvider) SupportsJSONMode(string) bool { return true }

type e2eHTTPChecker struct {
	base           policy.Checker
	needsReview    func(tools.Call) bool
	barrierTarget  int
	barrierTimeout time.Duration

	mu             sync.Mutex
	inFlight       int
	maxInFlight    int
	barrierArrived int
	barrierReady   chan struct{}
}

func newE2EHTTPChecker(barrierTarget int) *e2eHTTPChecker {
	c := &e2eHTTPChecker{
		base:           policy.NewDefaultChecker(),
		barrierTarget:  barrierTarget,
		barrierTimeout: 300 * time.Millisecond,
	}
	if barrierTarget > 1 {
		c.barrierReady = make(chan struct{})
	}
	return c
}

func (c *e2eHTTPChecker) CheckText(ctx, text string) policy.Decision {
	return c.base.CheckText(ctx, text)
}

func (c *e2eHTTPChecker) CheckToolCall(call tools.Call) policy.Decision {
	isRead := call.Name == "workspace_read" || call.Name == "workspace_list" ||
		call.Name == "workspace_glob" || call.Name == "workspace_grep"
	c.mu.Lock()
	c.inFlight++
	if c.inFlight > c.maxInFlight {
		c.maxInFlight = c.inFlight
	}
	var waitCh chan struct{}
	if c.barrierTarget > 1 && isRead && c.barrierReady != nil {
		c.barrierArrived++
		if c.barrierArrived >= c.barrierTarget {
			select {
			case <-c.barrierReady:
			default:
				close(c.barrierReady)
			}
		}
		waitCh = c.barrierReady
	}
	c.mu.Unlock()
	if waitCh != nil {
		select {
		case <-waitCh:
		case <-time.After(c.barrierTimeout):
		}
	}
	defer func() {
		c.mu.Lock()
		c.inFlight--
		c.mu.Unlock()
	}()
	d := c.base.CheckToolCall(call)
	if c.needsReview != nil && c.needsReview(call) {
		d.NeedsApproval = true
	}
	return d
}

func (c *e2eHTTPChecker) MaxInFlight() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.maxInFlight
}

type e2eHTTPServerFixture struct {
	st       *store.SQLiteStore
	root     string
	run      domain.Run
	threadID string
	provider *e2eHTTPProvider
	checker  *e2eHTTPChecker
	server   *httptest.Server
	client   *http.Client
}

func newE2EHTTPFixture(t *testing.T, mode domain.RunExecutionPermissionMode, checker *e2eHTTPChecker, provider *e2eHTTPProvider) *e2eHTTPServerFixture {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "http-e2e.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	wsID := "ws-http-e2e"
	if err := st.SaveWorkspace(t.Context(), store.WorkspaceRecord{
		ID: wsID, Name: "http-e2e", RootPath: root, CreatedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatal(err)
	}
	if provider == nil {
		provider = &e2eHTTPProvider{}
	}
	if checker == nil {
		checker = newE2EHTTPChecker(0)
	}
	caps := domain.ExecutionPermissionRuntimeCapabilities{
		OperatorApprovalEnabled: true,
		DangerFullAccessEnabled: true,
		RuntimeAuthority:        domain.NewExecutionPermissionRuntimeAuthority(),
	}
	_, run, err := application.NewRunService(st).Create(t.Context(), application.CreateRunRequest{
		Goal: "HTTP E2E throughput verification", Profile: "code", Surface: "code", Phase: "deliver",
		WorkspaceID: wsID, ModelRoute: provider.Name() + "/model", Interactive: true,
		Budget: domain.Budget{MaxTurns: 8, MaxToolCalls: 40},
	})
	if err != nil {
		t.Fatal(err)
	}
	if mode != "" && mode != domain.RunExecutionPermissionAsk {
		if _, err := application.NewThreadExecutionPermissionService(st, caps).Change(t.Context(), application.ChangeThreadExecutionPermissionRequest{
			ThreadID: domain.InitialThreadID(run.ID), Mode: string(mode), OperationKey: "http-e2e-perm-" + string(mode),
			RequestedBy: "test_operator", Reason: "http e2e mode setup",
			ConfirmFull: mode == domain.RunExecutionPermissionFull,
		}); err != nil {
			t.Fatal(err)
		}
	}
	router := llm.NewRouter(llm.ModelRef{Provider: provider.Name(), Model: "model"})
	router.RegisterProvider(provider)
	lifecycle := application.NewRunLifecycleControlService(st)
	execution := application.NewRunExecutionHandoffService(st, router, checker).
		WithExecutionPermissionCapabilities(caps).
		WithGeneratedContextCompaction(false)
	turns := application.NewThreadTurnServiceWithExecutionCapabilities(st, lifecycle, execution, caps)

	api, err := httpapi.New(st, httpapi.Config{
		AccessToken:                       e2eAccessToken,
		ControlToken:                      e2eControlToken,
		RunControlEnabled:                 true,
		ExecutionPermissionControlEnabled: true,
		RunCreationEnabled:                true,
		SessionMessageEnabled:             true,
		SessionSteeringControlEnabled:     true,
		RunLifecycleEnabled:               true,
		RunExecutionEnabled:               true,
		FileEditReviewEnabled:             true,
		FileEditApplyEnabled:              true,
		ExecutionPermissionCapabilities:   caps,
		RunLifecycleController:            lifecycle,
		RunExecutionController:            execution,
		ThreadTurnController:              turns,
		FileEditReviewController:          application.NewFileEditReviewService(st),
		FileEditApplyController:           application.NewFileEditApplyService(st, checker),
		AppVersion:                        "e2e-test",
	})
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(api.Handler())
	t.Cleanup(srv.Close)
	return &e2eHTTPServerFixture{
		st: st, root: root, run: run, threadID: domain.InitialThreadID(run.ID),
		provider: provider, checker: checker, server: srv,
		client: &http.Client{Timeout: 30 * time.Second},
	}
}

func (f *e2eHTTPServerFixture) doJSON(t *testing.T, method, path, token, idempotencyKey string, body any, wantStatus int, out any) []byte {
	t.Helper()
	var bodyReader io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		bodyReader = bytes.NewReader(raw)
	}
	req, err := http.NewRequestWithContext(t.Context(), method, f.server.URL+path, bodyReader)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if idempotencyKey != "" {
		req.Header.Set("Idempotency-Key", idempotencyKey)
	}
	resp, err := f.client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	rawResp, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != wantStatus {
		t.Fatalf("%s %s status=%d want=%d body=%s", method, path, resp.StatusCode, wantStatus, string(rawResp))
	}
	if out != nil {
		var env struct {
			Version string          `json:"version"`
			Data    json.RawMessage `json:"data"`
		}
		if err := json.Unmarshal(rawResp, &env); err != nil {
			t.Fatalf("decode envelope: %v body=%s", err, string(rawResp))
		}
		if env.Version != httpapi.Version {
			t.Fatalf("unexpected envelope version %q", env.Version)
		}
		if err := json.Unmarshal(env.Data, out); err != nil {
			t.Fatalf("decode data: %v data=%s", err, string(env.Data))
		}
	}
	return rawResp
}

func httpWriteFile(t *testing.T, root, rel, content string) string {
	t.Helper()
	full := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(full, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return fileedit.HashText(content)
}

func httpToolCall(id, name string, payload any) llm.ToolCall {
	raw, _ := json.Marshal(payload)
	return llm.ToolCall{ID: id, Name: name, Arguments: raw}
}

func httpToolResp(calls ...llm.ToolCall) *llm.ChatResponse {
	return &llm.ChatResponse{
		Provider: "http-e2e", Model: "model",
		Usage:     llm.Usage{InputTokens: 4, OutputTokens: 4, TotalTokens: 8},
		ToolCalls: calls,
	}
}

func httpActionResp(kind domain.RootActionKind, msg, summary, reason string) *llm.ChatResponse {
	raw, _ := json.Marshal(map[string]any{
		"version": domain.RootLifecycleVersion,
		"action":  kind, "message": msg, "summary": summary, "reason": reason,
	})
	return &llm.ChatResponse{
		Text: string(raw), Provider: "http-e2e", Model: "model",
		Usage: llm.Usage{InputTokens: 4, OutputTokens: 4, TotalTokens: 8},
	}
}

// ============================================================================
// HTTP E2E TESTS (TIERS 1-4)
// ============================================================================

func TestThroughputE2E_Tier1_R3_HTTPRunCapabilitiesAndReviewDirectApply(t *testing.T) {
	checker := newE2EHTTPChecker(0)
	checker.needsReview = func(c tools.Call) bool { return c.Name == "replace_file" }
	p := &e2eHTTPProvider{}
	f := newE2EHTTPFixture(t, domain.RunExecutionPermissionAuto, checker, p)
	sha := httpWriteFile(t, f.root, "config.yaml", "mode: old\n")

	p.respond = func(_ context.Context, _ llm.ChatRequest, index int) (*llm.ChatResponse, error) {
		switch index {
		case 0:
			return httpToolResp(httpToolCall("cfg-1", "workspace_change", toolgateway.WorkspaceChangePayload{
				Version: toolgateway.AgentCodeRegistryVersion, Action: "replace",
				Path: "config.yaml", ExpectedSHA256: sha, Content: "mode: new\n",
			})), nil
		case 1:
			return httpActionResp(domain.RootActionWait, "Review config.yaml change", "", "operator review required"), nil
		case 2:
			return httpActionResp(domain.RootActionFinish, "Config applied via HTTP approval", "done", ""), nil
		default:
			return nil, fmt.Errorf("unexpected extra model call %d", index)
		}
	}

	var detail httpapi.RunDetailView
	f.doJSON(t, http.MethodGet, "/api/v1/runs/"+f.run.ID, e2eAccessToken, "", nil, http.StatusOK, &detail)
	if detail.AgentCodeTools.PermissionMode != string(domain.RunExecutionPermissionAuto) {
		t.Fatalf("unexpected permission mode in capabilities: %+v", detail.AgentCodeTools)
	}

	var turnView httpapi.ThreadMessageControlView
	f.doJSON(t, http.MethodPost, "/api/v1/threads/"+f.threadID+"/turns", e2eControlToken, "http-e2e-t1-turn-0001",
		httpapi.ThreadTurnControlRequestView{Version: domain.ThreadMessageProtocolVersion, Content: "Update config.yaml"},
		http.StatusAccepted, &turnView)

	var queue httpapi.FileEditQueueView
	f.doJSON(t, http.MethodGet, "/api/v1/runs/"+f.run.ID+"/file-edits", e2eAccessToken, "", nil, http.StatusOK, &queue)
	if len(queue.Items) != 1 || queue.Items[0].Status != fileedit.StatusProposed {
		t.Fatalf("expected 1 proposed file edit in queue, got %+v", queue)
	}
	editID := queue.Items[0].ID

	var reviewView httpapi.FileEditReviewView
	f.doJSON(t, http.MethodPost, "/api/v1/runs/"+f.run.ID+"/file-edits/"+editID+"/review", e2eControlToken, "",
		httpapi.FileEditReviewRequestView{Version: application.FileEditReviewProtocolVersion, Action: application.FileEditApproveIntent},
		http.StatusAccepted, &reviewView)

	if !reviewView.FileWritten || reviewView.Edit.Status != fileedit.StatusApplied {
		t.Fatalf("expected HTTP /review to apply directly (file_written=true, status=applied), got %+v", reviewView)
	}
	if reviewView.Continuation == nil || reviewView.Continuation.State != "completed" {
		t.Fatalf("expected completed continuation after HTTP review, got %+v", reviewView.Continuation)
	}
	if got, _ := os.ReadFile(filepath.Join(f.root, "config.yaml")); string(got) != "mode: new\n" {
		t.Fatalf("config.yaml=%q want=mode: new\\n", string(got))
	}
	rounds, _ := f.st.ListRunSupervisorToolRoundsPage(t.Context(), f.run.ID, 0, 10)
	for _, r := range rounds {
		for _, c := range r.Calls {
			if c.ToolName == "workspace_apply" {
				t.Fatalf("unexpected workspace_apply tool call: %#v", c)
			}
		}
	}
}

func TestThroughputE2E_Tier2_HTTPReviewCASConflictAndDeny(t *testing.T) {
	t.Run("http_review_deny", func(t *testing.T) {
		checker := newE2EHTTPChecker(0)
		checker.needsReview = func(c tools.Call) bool { return c.Name == "replace_file" }
		p := &e2eHTTPProvider{}
		f := newE2EHTTPFixture(t, domain.RunExecutionPermissionAuto, checker, p)
		sha := httpWriteFile(t, f.root, "deny_http.txt", "keep-me\n")

		p.respond = func(_ context.Context, _ llm.ChatRequest, index int) (*llm.ChatResponse, error) {
			switch index {
			case 0:
				return httpToolResp(httpToolCall("d1", "workspace_change", toolgateway.WorkspaceChangePayload{
					Version: toolgateway.AgentCodeRegistryVersion, Action: "replace",
					Path: "deny_http.txt", ExpectedSHA256: sha, Content: "drop-me\n",
				})), nil
			case 1:
				return httpActionResp(domain.RootActionWait, "Review deny_http.txt", "", "review"), nil
			default:
				return httpActionResp(domain.RootActionFinish, "Denied cleanly", "done", ""), nil
			}
		}
		f.doJSON(t, http.MethodPost, "/api/v1/threads/"+f.threadID+"/turns", e2eControlToken, "http-e2e-t2-deny-turn-0001",
			httpapi.ThreadTurnControlRequestView{Version: domain.ThreadMessageProtocolVersion, Content: "Edit deny_http.txt"},
			http.StatusAccepted, nil)

		var queue httpapi.FileEditQueueView
		f.doJSON(t, http.MethodGet, "/api/v1/runs/"+f.run.ID+"/file-edits", e2eAccessToken, "", nil, http.StatusOK, &queue)
		if len(queue.Items) != 1 {
			t.Fatalf("expected 1 edit in queue, got %+v", queue)
		}
		editID := queue.Items[0].ID

		var reviewView httpapi.FileEditReviewView
		f.doJSON(t, http.MethodPost, "/api/v1/runs/"+f.run.ID+"/file-edits/"+editID+"/review", e2eControlToken, "",
			httpapi.FileEditReviewRequestView{Version: application.FileEditReviewProtocolVersion, Action: application.FileEditDeny},
			http.StatusAccepted, &reviewView)
		if reviewView.FileWritten || reviewView.Edit.Status != fileedit.StatusDenied {
			t.Fatalf("expected denied non-written edit, got %+v", reviewView)
		}
		if got, _ := os.ReadFile(filepath.Join(f.root, "deny_http.txt")); string(got) != "keep-me\n" {
			t.Fatalf("denied HTTP edit changed disk: %q", string(got))
		}
	})

	t.Run("http_review_cas_conflict", func(t *testing.T) {
		checker := newE2EHTTPChecker(0)
		checker.needsReview = func(c tools.Call) bool { return c.Name == "replace_file" }
		p := &e2eHTTPProvider{}
		f := newE2EHTTPFixture(t, domain.RunExecutionPermissionAuto, checker, p)
		sha := httpWriteFile(t, f.root, "cas_http.txt", "v1\n")

		p.respond = func(_ context.Context, _ llm.ChatRequest, index int) (*llm.ChatResponse, error) {
			switch index {
			case 0:
				return httpToolResp(httpToolCall("c1", "workspace_change", toolgateway.WorkspaceChangePayload{
					Version: toolgateway.AgentCodeRegistryVersion, Action: "replace",
					Path: "cas_http.txt", ExpectedSHA256: sha, Content: "v2\n",
				})), nil
			case 1:
				return httpActionResp(domain.RootActionWait, "Review cas_http.txt", "", "review"), nil
			default:
				return httpActionResp(domain.RootActionFinish, "Conflict observed", "done", ""), nil
			}
		}
		f.doJSON(t, http.MethodPost, "/api/v1/threads/"+f.threadID+"/turns", e2eControlToken, "http-e2e-t2-cas-turn-0001",
			httpapi.ThreadTurnControlRequestView{Version: domain.ThreadMessageProtocolVersion, Content: "Edit cas_http.txt"},
			http.StatusAccepted, nil)

		var queue httpapi.FileEditQueueView
		f.doJSON(t, http.MethodGet, "/api/v1/runs/"+f.run.ID+"/file-edits", e2eAccessToken, "", nil, http.StatusOK, &queue)
		if len(queue.Items) != 1 {
			t.Fatalf("expected 1 edit in queue, got %+v", queue)
		}
		editID := queue.Items[0].ID

		// Modify disk file externally before HTTP approve_intent arrives.
		httpWriteFile(t, f.root, "cas_http.txt", "external-v9\n")

		reqBody, _ := json.Marshal(httpapi.FileEditReviewRequestView{
			Version: application.FileEditReviewProtocolVersion, Action: application.FileEditApproveIntent,
		})
		req, _ := http.NewRequestWithContext(t.Context(), http.MethodPost, f.server.URL+"/api/v1/runs/"+f.run.ID+"/file-edits/"+editID+"/review", bytes.NewReader(reqBody))
		req.Header.Set("Authorization", "Bearer "+e2eControlToken)
		req.Header.Set("Content-Type", "application/json")
		resp, err := f.client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		// Whether /review returns 409 CONFLICT or 202 with FileWritten=false + failed status,
		// the external file on disk must NEVER be overwritten!
		if got, _ := os.ReadFile(filepath.Join(f.root, "cas_http.txt")); string(got) != "external-v9\n" {
			t.Fatalf("CAS conflict overwrote external file: %q", string(got))
		}
	})
}

func TestThroughputE2E_Tier3_HTTPConcurrentReadAndApprovalReviewContinue(t *testing.T) {
	checker := newE2EHTTPChecker(8)
	checker.needsReview = func(c tools.Call) bool { return c.Name == "replace_file" }
	p := &e2eHTTPProvider{}
	f := newE2EHTTPFixture(t, domain.RunExecutionPermissionAuto, checker, p)
	for i := 1; i <= 8; i++ {
		httpWriteFile(t, f.root, fmt.Sprintf("h%d.txt", i), fmt.Sprintf("h%d\n", i))
	}
	targetSHA := httpWriteFile(t, f.root, "guarded.txt", "base\n")

	p.respond = func(_ context.Context, _ llm.ChatRequest, index int) (*llm.ChatResponse, error) {
		switch index {
		case 0:
			calls := make([]llm.ToolCall, 0, 8)
			for i := 1; i <= 8; i++ {
				calls = append(calls, httpToolCall(fmt.Sprintf("hr-%d", i), "workspace_read", toolgateway.WorkspaceReadPayload{
					Version: toolgateway.AgentCodeRegistryVersion, Path: fmt.Sprintf("h%d.txt", i), StartLine: 1, EndLine: 2,
				}))
			}
			return httpToolResp(calls...), nil
		case 1:
			// 1-step Auto create
			return httpToolResp(httpToolCall("hc-1", "workspace_change", toolgateway.WorkspaceChangePayload{
				Version: toolgateway.AgentCodeRegistryVersion, Action: "create", Path: "auto_note.txt", ExpectedSHA256: "missing", Content: "auto\n",
			})), nil
		case 2:
			// Policy-gated replace
			return httpToolResp(httpToolCall("hg-1", "workspace_change", toolgateway.WorkspaceChangePayload{
				Version: toolgateway.AgentCodeRegistryVersion, Action: "replace", Path: "guarded.txt", ExpectedSHA256: targetSHA, Content: "approved\n",
			})), nil
		case 3:
			return httpActionResp(domain.RootActionWait, "Approve guarded.txt", "", "review"), nil
		case 4:
			return httpActionResp(domain.RootActionFinish, "All HTTP phases done", "done", ""), nil
		default:
			return nil, fmt.Errorf("unexpected call %d", index)
		}
	}

	f.doJSON(t, http.MethodPost, "/api/v1/threads/"+f.threadID+"/turns", e2eControlToken, "http-e2e-t3-turn-0001",
		httpapi.ThreadTurnControlRequestView{Version: domain.ThreadMessageProtocolVersion, Content: "Read 8, auto-create, then review-edit"},
		http.StatusAccepted, nil)

	if checker.MaxInFlight() != 8 {
		t.Fatalf("expected 8 concurrent HTTP-triggered reads, got %d", checker.MaxInFlight())
	}
	if got, _ := os.ReadFile(filepath.Join(f.root, "auto_note.txt")); string(got) != "auto\n" {
		t.Fatalf("auto_note.txt not written in 1 step: %q", string(got))
	}
	var queue httpapi.FileEditQueueView
	f.doJSON(t, http.MethodGet, "/api/v1/runs/"+f.run.ID+"/file-edits", e2eAccessToken, "", nil, http.StatusOK, &queue)
	var pendingID string
	for _, item := range queue.Items {
		if item.Status == fileedit.StatusProposed {
			pendingID = item.ID
		}
	}
	if pendingID == "" {
		t.Fatalf("expected pending edit in queue: %+v", queue)
	}
	var reviewView httpapi.FileEditReviewView
	f.doJSON(t, http.MethodPost, "/api/v1/runs/"+f.run.ID+"/file-edits/"+pendingID+"/review", e2eControlToken, "",
		httpapi.FileEditReviewRequestView{Version: application.FileEditReviewProtocolVersion, Action: application.FileEditApproveIntent},
		http.StatusAccepted, &reviewView)
	if !reviewView.FileWritten || reviewView.Edit.Status != fileedit.StatusApplied {
		t.Fatalf("expected direct apply on review, got %+v", reviewView)
	}
	if got, _ := os.ReadFile(filepath.Join(f.root, "guarded.txt")); string(got) != "approved\n" {
		t.Fatalf("guarded.txt=%q", string(got))
	}
}

func TestThroughputE2E_Tier4_HTTPSensitiveEditApprovalWorkflow(t *testing.T) {
	checker := newE2EHTTPChecker(0)
	checker.needsReview = func(c tools.Call) bool { return c.Name == "replace_file" }
	p := &e2eHTTPProvider{}
	f := newE2EHTTPFixture(t, domain.RunExecutionPermissionAuto, checker, p)
	sha := httpWriteFile(t, f.root, "auth/policy.go", "package auth\nconst AllowAll = true\n")

	p.respond = func(_ context.Context, _ llm.ChatRequest, index int) (*llm.ChatResponse, error) {
		switch index {
		case 0:
			return httpToolResp(httpToolCall("sec-1", "workspace_change", toolgateway.WorkspaceChangePayload{
				Version: toolgateway.AgentCodeRegistryVersion, Action: "propose_patch", Path: "auth/policy.go", ExpectedSHA256: sha,
				Replacements: []toolgateway.WorkspaceReplacement{{OldText: "AllowAll = true", NewText: "AllowAll = false", ExpectedOccurrences: 1}},
			})), nil
		case 1:
			return httpActionResp(domain.RootActionWait, "Please approve security fix in auth/policy.go", "", "sensitive edit"), nil
		case 2:
			return httpActionResp(domain.RootActionFinish, "Security fix applied and verified", "done", ""), nil
		default:
			return nil, fmt.Errorf("unexpected call %d", index)
		}
	}

	f.doJSON(t, http.MethodPost, "/api/v1/threads/"+f.threadID+"/turns", e2eControlToken, "http-e2e-t4-turn-0001",
		httpapi.ThreadTurnControlRequestView{Version: domain.ThreadMessageProtocolVersion, Content: "Harden auth/policy.go"},
		http.StatusAccepted, nil)

	var queue httpapi.FileEditQueueView
	f.doJSON(t, http.MethodGet, "/api/v1/runs/"+f.run.ID+"/file-edits", e2eAccessToken, "", nil, http.StatusOK, &queue)
	if len(queue.Items) != 1 || !strings.Contains(queue.Items[0].Diff, "-const AllowAll = true") {
		t.Fatalf("unexpected diff queue: %+v", queue)
	}

	var reviewView httpapi.FileEditReviewView
	f.doJSON(t, http.MethodPost, "/api/v1/runs/"+f.run.ID+"/file-edits/"+queue.Items[0].ID+"/review", e2eControlToken, "",
		httpapi.FileEditReviewRequestView{Version: application.FileEditReviewProtocolVersion, Action: application.FileEditApproveIntent},
		http.StatusAccepted, &reviewView)
	if !reviewView.FileWritten || reviewView.Edit.Status != fileedit.StatusApplied ||
		reviewView.Continuation == nil || reviewView.Continuation.State != "completed" {
		t.Fatalf("HTTP sensitive review workflow failed: %+v", reviewView)
	}
	if got, _ := os.ReadFile(filepath.Join(f.root, "auth/policy.go")); !strings.Contains(string(got), "AllowAll = false") {
		t.Fatalf("auth/policy.go not updated: %q", string(got))
	}

	var transcript []map[string]any
	f.doJSON(t, http.MethodGet, "/api/v1/threads/"+f.threadID+"/transcript?limit=100", e2eAccessToken, "", nil, http.StatusOK, &transcript)
	operatorInputs := 0
	for _, item := range transcript {
		if item["kind"] == "operator_input" {
			operatorInputs++
		}
	}
	if operatorInputs != 1 {
		t.Fatalf("expected 1 operator_input in transcript, got %d (%v)", operatorInputs, transcript)
	}
}
