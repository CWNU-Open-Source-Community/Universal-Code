package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"cyberagent-workbench/internal/application"
	"cyberagent-workbench/internal/approval"
	"cyberagent-workbench/internal/browserruntime"
	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/llm"
	"cyberagent-workbench/internal/policy"
	"cyberagent-workbench/internal/store"
	"cyberagent-workbench/internal/toolgateway"
)

type agentBrowserHTTPProductProvider struct {
	llm.MockProvider
	url      string
	requests []llm.ChatRequest
	pixels   bool
}

func (p *agentBrowserHTTPProductProvider) Name() string               { return "agent-browser-product" }
func (p *agentBrowserHTTPProductProvider) SupportsTools(string) bool  { return true }
func (p *agentBrowserHTTPProductProvider) SupportsVision(string) bool { return true }
func (p *agentBrowserHTTPProductProvider) DescribeVision(string) llm.VisionCapability {
	return llm.VisionCapability{State: llm.VisionSupported, Source: "operator_declared"}
}
func (p *agentBrowserHTTPProductProvider) Chat(_ context.Context, request llm.ChatRequest) (*llm.ChatResponse, error) {
	p.requests = append(p.requests, request)
	response := &llm.ChatResponse{Provider: p.Name(), Model: "fixture", Usage: llm.Usage{InputTokens: 4, OutputTokens: 4, TotalTokens: 8}}
	tool := func(name string, payload any) {
		b, _ := json.Marshal(payload)
		response.ToolCalls = []llm.ToolCall{{ID: fmt.Sprintf("provider-browser-%d", len(p.requests)), Name: name, Arguments: b}}
	}
	switch len(p.requests) {
	case 1:
		found := false
		for _, d := range request.Tools {
			if d.Name == "browser_navigate" && strings.Contains(string(d.Parameters), "browser_navigate.v2") {
				found = true
			}
		}
		if !found {
			return nil, errors.New("ordinary Supervisor did not advertise Agent browser v2")
		}
		tool("browser_navigate", map[string]any{"version": "browser_navigate.v2", "url": p.url})
	case 2:
		tool("browser_snapshot", map[string]any{"version": "browser_snapshot.v2"})
	case 3:
		var snapshot browserruntime.AgentBrowserSnapshot
		for _, m := range request.Messages {
			for _, result := range m.ToolResults {
				var envelope struct {
					Tool   string `json:"tool"`
					Stdout string `json:"stdout"`
				}
				if json.Unmarshal([]byte(result.Content), &envelope) == nil && envelope.Tool == "browser_snapshot" {
					_ = json.Unmarshal([]byte(envelope.Stdout), &snapshot)
				}
			}
		}
		ref := ""
		for _, element := range snapshot.Elements {
			if element.Name == "Publish fixture" {
				ref = element.Ref
			}
		}
		if snapshot.SnapshotID == "" || ref == "" {
			return nil, fmt.Errorf("real snapshot did not expose fixture button: %+v", snapshot)
		}
		tool("browser_click", map[string]any{"version": "browser_click.v2", "snapshot_id": snapshot.SnapshotID, "element_ref": ref, "sensitive_intent": map[string]any{"version": "browser_sensitive_intent.v1", "effect": "external_write", "target": snapshot.CanonicalURL, "description": "Publish one synthetic draft to the test-owned fixture", "document_epoch": snapshot.DocumentEpoch}})
		response.ToolCalls = append(response.ToolCalls, llm.ToolCall{ID: "provider-browser-shot", Name: "browser_screenshot", Arguments: json.RawMessage(`{"version":"browser_screenshot.v2"}`)})
	case 4:
		for _, m := range request.Messages {
			if len(m.Images) > 0 && len(m.ToolResults) > 0 {
				p.pixels = true
			}
		}
		if !p.pixels {
			return nil, errors.New("saved real PNG never entered model image input")
		}
		b, _ := json.Marshal(domain.RootAction{Version: domain.RootLifecycleVersion, Kind: domain.RootActionContinue, Message: "Observed the approved synthetic submission and its screenshot."})
		response.Text = string(b)
	default:
		return nil, errors.New("unexpected provider retry or repeated continuation")
	}
	return response, nil
}
func (p *agentBrowserHTTPProductProvider) StreamChat(ctx context.Context, r llm.ChatRequest) (<-chan llm.ChatChunk, error) {
	response, e := p.Chat(ctx, r)
	if e != nil {
		return nil, e
	}
	out := make(chan llm.ChatChunk, 2)
	if response.Text != "" {
		out <- llm.ChatChunk{Text: response.Text}
	}
	out <- llm.FinalChatChunk(response)
	close(out)
	return out, nil
}

func TestAgentBrowserHTTPProductApprovalAndScreenshot(t *testing.T) {
	if os.Getenv("AGENT_BROWSER_HTTP_TEST") != "1" {
		t.Skip("owned Chromium HTTP acceptance")
	}
	if !browserruntime.AgentBrowserPlatformAvailable() {
		t.Fatal("requested native Agent Browser HTTP runtime is unavailable")
	}
	testTimeout, browserLifetime := time.Minute, time.Minute
	if runtime.GOOS == "darwin" {
		// Include the native bundle signature check before exercising HTTP
		// approval, screenshots, and cleanup on slower Intel runners.
		testTimeout, browserLifetime = 3*time.Minute, 4*time.Minute
	}
	ctx, cancel := context.WithTimeout(t.Context(), testTimeout)
	defer cancel()
	var writes atomic.Int32
	site := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/publish" {
			if r.Method != http.MethodPost {
				t.Error("unexpected mutation method")
			}
			writes.Add(1)
			fmt.Fprint(w, "published")
			return
		}
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprint(w, `<!doctype html><title>HTTP browser fixture</title><h1>HTTP approved dynamic page</h1><button onclick="fetch('/publish',{method:'POST'}).then(()=>document.getElementById('result').textContent='Published exactly once')">Publish fixture</button><p id="result">Draft ready</p>`)
	}))
	defer site.Close()
	home, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	st, err := store.Open(filepath.Join(home, "store.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	workspace := store.WorkspaceRecord{ID: "browser-http-workspace", Name: "owned", RootPath: home}
	if err = st.SaveWorkspace(ctx, workspace); err != nil {
		t.Fatal(err)
	}
	_, run, err := application.NewRunService(st).Create(ctx, application.CreateRunRequest{Goal: "Inspect and approve the exact synthetic publish action", Profile: "review", Surface: "code", Phase: "deliver", WorkspaceID: workspace.ID, Interactive: true, ModelRoute: "agent-browser-product/fixture", Budget: domain.Budget{MaxTurns: 8, MaxTokens: 50000, MaxToolCalls: 20}})
	if err != nil {
		t.Fatal(err)
	}
	caps := domain.ExecutionPermissionRuntimeCapabilities{OperatorApprovalEnabled: true, DangerFullAccessEnabled: true, RuntimeAuthority: domain.NewExecutionPermissionRuntimeAuthority()}
	if _, err = application.NewRunExecutionPermissionService(st, caps).Change(ctx, application.ChangeRunExecutionPermissionRequest{RunID: run.ID, Mode: "full", OperationKey: "http-browser-full-access", RequestedBy: "test_operator", Reason: "owned local browser HTTP test", ConfirmFull: true}); err != nil {
		t.Fatal(err)
	}
	browser := application.NewAgentBrowserService(st, application.AgentBrowserOptions{HomePath: home, Capabilities: caps, Headless: true, SessionLifetime: browserLifetime})
	defer browser.Shutdown(context.Background())
	provider := &agentBrowserHTTPProductProvider{url: site.URL}
	router := llm.NewRouter(llm.ModelRef{Provider: provider.Name(), Model: "fixture"})
	router.RegisterProvider(provider)
	checker := policy.NewDefaultChecker()
	dependencies := application.RunRuntimeDependencies{ExecutionCapabilities: caps, AgentBrowser: browser}
	supervisor := application.NewAgentRunnerWithRuntime(st, router, checker, dependencies)
	execution := application.NewRunExecutionHandoffWithRuntime(st, router, checker, dependencies)
	lifecycle := application.NewRunLifecycleControlService(st)
	threads := application.NewThreadTurnServiceWithExecutionCapabilities(st, lifecycle, execution, caps)
	controller := application.NewApprovalControlService(st, toolgateway.New(st, checker), checker)
	api, err := New(st, Config{AccessToken: testAccessToken, ControlToken: testControlToken, RunCreationEnabled: true, SessionMessageEnabled: true, RunLifecycleEnabled: true, RunLifecycleController: lifecycle, RunExecutionEnabled: true, RunExecutionController: execution, ThreadTurnController: threads, ApprovalControlEnabled: true, ApprovalController: controller, AgentBrowserController: browser})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = application.NewRunService(st).Start(ctx, run.ID); err != nil {
		t.Fatal(err)
	}
	result, err := supervisor.Step(ctx, run.ID)
	if err != nil || result.RunStatus != domain.RunWaitingApproval || writes.Load() != 0 || len(provider.requests) != 3 {
		observed, statusErr := browser.GetStatus(ctx, run.ID)
		t.Logf("browser launch diagnostics home=%q state=%s reason=%q statusErr=%v", home, observed.State, observed.FailureReason, statusErr)
		for i, req := range provider.requests {
			for _, m := range req.Messages {
				for _, result := range m.ToolResults {
					t.Logf("fixture request %d tool %s: %s", i, result.ToolCallID, result.Content)
				}
			}
		}
		t.Fatalf("preflight %+v %v writes=%d model=%d", result, err, writes.Load(), len(provider.requests))
	}
	approvals, err := st.ListApprovals(ctx, approval.ListFilter{RunID: run.ID, ToolName: toolgateway.AgentBrowserApprovalTool})
	if err != nil || len(approvals) != 1 {
		t.Fatalf("approvals %v %+v", err, approvals)
	}
	record := approvals[0]
	base := "/api/v1/runs/" + run.ID
	previewResponse := performRequest(t, api, http.MethodGet, base+"/approvals/"+record.ID+"/preview", testAccessToken, "127.0.0.1:8765", "127.0.0.1:45000", nil)
	var preview ApprovalPreviewView
	decodeDataStatus(t, previewResponse, http.StatusOK, &preview)
	if !preview.SourceCurrent || preview.Effect != "browser_sensitive_action" || !strings.Contains(previewResponse.Body.String(), "synthetic draft") || writes.Load() != 0 {
		t.Fatalf("ordinary preview unusable %s", previewResponse.Body.String())
	}
	var decision ApprovalDecisionControlView
	for n := 0; n < 2; n++ {
		response := performControlPathRequest(t, api, base+"/approvals/"+record.ID+"/decision", "http-browser-approve-once", strings.NewReader(`{"version":"approval_control.v1","action":"approve_once"}`))
		decodeDataStatus(t, response, http.StatusAccepted, &decision)
		if decision.Continuation == nil || decision.Continuation.State != "completed" || writes.Load() != 1 || len(provider.requests) != 4 || !provider.pixels {
			t.Fatalf("HTTP continuation %+v writes=%d requests=%d pixels=%t", decision, writes.Load(), len(provider.requests), provider.pixels)
		}
		if n == 1 && (!decision.Replayed || !decision.Continuation.Replayed) {
			t.Fatal("decision replay was not observed")
		}
	}
	statusResponse := performRequest(t, api, http.MethodGet, base+"/agent-browser", testAccessToken, "127.0.0.1:8765", "127.0.0.1:45000", nil)
	var status AgentBrowserStatusView
	decodeDataStatus(t, statusResponse, http.StatusOK, &status)
	if status.Screenshot == nil {
		t.Fatalf("actual image not advertised %+v", status)
	}
	imagePath := base + "/agent-browser/screenshot?session_id=" + status.SessionID + "&artifact_locator=" + status.Screenshot.Locator + "&sha256=" + status.Screenshot.SHA256
	shot := performRequest(t, api, http.MethodGet, imagePath, testAccessToken, "127.0.0.1:8765", "127.0.0.1:45000", nil)
	if shot.Code != http.StatusOK || shot.Header().Get("X-CyberAgent-Content-SHA256") != status.Screenshot.SHA256 || len(shot.Body.Bytes()) != status.Screenshot.ByteSize {
		t.Fatalf("actual screenshot HTTP status=%d %s", shot.Code, shot.Body.String())
	}
	body, _ := json.Marshal(AgentBrowserCloseRequestView{Version: "agent_browser_close.v1", SessionID: status.SessionID})
	closedResponse := performControlPathRequest(t, api, base+"/agent-browser/close", "http-browser-close", strings.NewReader(string(body)))
	var closed AgentBrowserStatusView
	decodeDataStatus(t, closedResponse, http.StatusOK, &closed)
	processesStopped := closed.TreeReaped
	if runtime.GOOS == "darwin" {
		processesStopped = !closed.TreeReaped && closed.ProcessScope == browserruntime.DarwinAgentBrowserProcessScope && closed.ProcessScopeVerified
	}
	if !processesStopped || !closed.ProfileRemoved || closed.CleanupPending || writes.Load() != 1 {
		t.Fatalf("cleanup=%+v", closed)
	}
	if output := os.Getenv("AGENT_BROWSER_HTTP_EVIDENCE"); output != "" {
		report, _ := json.MarshalIndent(map[string]any{"run_id": run.ID, "preview": preview, "decision": decision, "status": status, "closed": closed, "actual_browser": true, "provider_kind": "controlled_fixture", "provider_requests": len(provider.requests), "writes": writes.Load(), "model_received_pixels": provider.pixels}, "", "  ")
		if err = os.WriteFile(filepath.Join(output, "agent-browser-http-product.json"), report, 0600); err != nil {
			t.Fatal(err)
		}
		if err = os.WriteFile(filepath.Join(output, "agent-browser-http-product.png"), shot.Body.Bytes(), 0600); err != nil {
			t.Fatal(err)
		}
	}
	t.Logf("HTTP preview -> decision -> same checkpoint continuation -> verified PNG -> close: %d provider requests, %d synthetic writes; tree/profile cleaned", len(provider.requests), writes.Load())
}
