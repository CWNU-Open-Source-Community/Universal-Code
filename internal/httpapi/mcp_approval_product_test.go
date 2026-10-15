package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"cyberagent-workbench/internal/application"
	"cyberagent-workbench/internal/approval"
	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/llm"
	"cyberagent-workbench/internal/mcp"
	"cyberagent-workbench/internal/plugins"
	"cyberagent-workbench/internal/policy"
	"cyberagent-workbench/internal/store"
	"cyberagent-workbench/internal/toolgateway"
)

type mcpApprovalProductProvider struct {
	llm.MockProvider
	payload        json.RawMessage
	requests       int
	observedResult bool
}

func (p *mcpApprovalProductProvider) Name() string            { return "mcp-approval-product" }
func (*mcpApprovalProductProvider) SupportsTools(string) bool { return true }
func (p *mcpApprovalProductProvider) Chat(_ context.Context, request llm.ChatRequest) (*llm.ChatResponse, error) {
	p.requests++
	response := &llm.ChatResponse{Provider: p.Name(), Model: "fixture", Usage: llm.Usage{InputTokens: 4, OutputTokens: 4, TotalTokens: 8}}
	if p.requests == 1 {
		found := false
		for _, tool := range request.Tools {
			if tool.Name == mcp.OperationApprovalTool {
				found = true
			}
		}
		if !found {
			return nil, errors.New("MCP tool missing from ordinary Cyber/Plan model request")
		}
		response.ToolCalls = []llm.ToolCall{{ID: "http-mcp-call", Name: mcp.OperationApprovalTool, Arguments: p.payload}}
	} else if p.requests == 2 {
		for _, message := range request.Messages {
			for _, result := range message.ToolResults {
				if strings.Contains(result.Content, "exact MCP fixture response") {
					p.observedResult = true
				}
			}
		}
		if !p.observedResult {
			return nil, errors.New("same-turn continuation omitted approved MCP result")
		}
		raw, _ := json.Marshal(domain.RootAction{Version: domain.RootLifecycleVersion, Kind: domain.RootActionContinue, Message: "Observed the approved local MCP result."})
		response.Text = string(raw)
	} else {
		return nil, errors.New("unexpected repeated model continuation")
	}
	return response, nil
}
func (p *mcpApprovalProductProvider) StreamChat(ctx context.Context, request llm.ChatRequest) (<-chan llm.ChatChunk, error) {
	response, err := p.Chat(ctx, request)
	if err != nil {
		return nil, err
	}
	out := make(chan llm.ChatChunk, 2)
	if response.Text != "" {
		out <- llm.ChatChunk{Text: response.Text}
	}
	out <- llm.FinalChatChunk(response)
	close(out)
	return out, nil
}

type mcpApprovalNoCredentials struct{}

func (mcpApprovalNoCredentials) Get(context.Context, string) (string, bool, error) {
	return "", false, nil
}

func TestMCPApprovalHTTPProductSameTurnAndReplay(t *testing.T) {
	for _, mode := range []string{"ask", "auto", "cancel"} {
		t.Run(mode, func(t *testing.T) {
			ctx := t.Context()
			st, err := store.Open(filepath.Join(t.TempDir(), "mcp-http.db"))
			if err != nil {
				t.Fatal(err)
			}
			defer st.Close()
			workspace := store.WorkspaceRecord{ID: "mcp-http-workspace", Name: "Owned fixture", RootPath: t.TempDir()}
			if err = st.SaveWorkspace(ctx, workspace); err != nil {
				t.Fatal(err)
			}
			provider := &mcpApprovalProductProvider{}
			_, run, err := application.NewRunService(st).Create(ctx, application.CreateRunRequest{Goal: "Read the exact reviewed MCP fixture", Profile: "review", Surface: "cyber", Phase: "plan", WorkspaceID: workspace.ID, Interactive: true, ModelRoute: provider.Name() + "/fixture", Budget: domain.Budget{MaxTurns: 8, MaxTokens: 50000, MaxToolCalls: 20}})
			if err != nil {
				t.Fatal(err)
			}
			caps := domain.ExecutionPermissionRuntimeCapabilities{OperatorApprovalEnabled: true, DangerFullAccessEnabled: true, RuntimeAuthority: domain.NewExecutionPermissionRuntimeAuthority()}
			if mode == "auto" {
				if _, err = application.NewRunExecutionPermissionService(st, caps).Change(ctx, application.ChangeRunExecutionPermissionRequest{RunID: run.ID, Mode: mode, OperationKey: "http-mcp-mode-0001", RequestedBy: "operator"}); err != nil {
					t.Fatal(err)
				}
			}
			var runtime atomic.Bool
			var requests, catalogRequests, calls, onboardingRequests atomic.Int32
			peer := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if runtime.Load() {
					requests.Add(1)
				}
				if r.Method == http.MethodDelete {
					w.WriteHeader(204)
					return
				}
				var request struct {
					ID     json.RawMessage `json:"id"`
					Method string          `json:"method"`
					Params json.RawMessage `json:"params"`
				}
				if json.NewDecoder(r.Body).Decode(&request) != nil {
					w.WriteHeader(400)
					return
				}
				onboardingRequests.Add(1)
				if runtime.Load() {
					switch request.Method {
					case "initialize", "notifications/initialized", "tools/list":
						catalogRequests.Add(1)
					}
				}
				if len(request.ID) == 0 {
					w.WriteHeader(202)
					return
				}
				w.Header().Set("Content-Type", "application/json")
				var body json.RawMessage
				switch request.Method {
				case "initialize":
					body = json.RawMessage(`{"protocolVersion":"2024-11-05","capabilities":{"tools":{}},"serverInfo":{"name":"http-approval-fixture","version":"1"}}`)
				case "tools/list":
					body = json.RawMessage(`{"tools":[{"name":"lookup","annotations":{"readOnlyHint":true},"inputSchema":{"type":"object","properties":{"query":{"type":"string"}}}}]}`)
				case "tools/call":
					calls.Add(1)
					body = json.RawMessage(`{"content":[{"type":"text","text":"exact MCP fixture response"}]}`)
				default:
					_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": request.ID, "error": map[string]any{"code": -32601, "message": "unsupported"}})
					return
				}
				_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": request.ID, "result": body})
			}))
			defer peer.Close()
			manager, err := mcp.NewClientManager(st, mcpApprovalNoCredentials{}, mcp.ManagerOptions{HTTPClient: peer.Client()})
			if err != nil {
				t.Fatal(err)
			}
			defer manager.Close()
			descriptor := mcp.ServerDescriptor{ProtocolVersion: mcp.ClientProtocolVersion, ID: "http-mcp-server", Name: "HTTP MCP fixture", Transport: mcp.TransportStreamableHTTP, Target: peer.URL, DeclaredCapabilities: []mcp.CapabilityKind{mcp.CapabilityTools}, Scope: mcp.ScopeRun, RunID: run.ID, WorkspaceID: workspace.ID, Source: mcp.Source{Kind: "manual", URI: "operator://http-mcp-fixture"}, CallTimeoutMillis: 3000, MaxResultBytes: 8192}
			pluginService, err := plugins.NewService(st)
			if err != nil {
				t.Fatal(err)
			}
			extensions, err := application.NewExtensionControlService(st, manager, pluginService)
			if err != nil {
				t.Fatal(err)
			}
			onboardingAPI, err := New(st, Config{AccessToken: testAccessToken, ControlToken: testControlToken,
				ExtensionControlEnabled: true, ExtensionController: extensions})
			if err != nil {
				t.Fatal(err)
			}
			registration := ExtensionMCPRegistrationRequestView{Version: ExtensionControlProtocol,
				Descriptor: ExtensionMCPRegistrationDescriptorView{ProtocolVersion: descriptor.ProtocolVersion,
					ID: descriptor.ID, Name: descriptor.Name, Transport: descriptor.Transport, Target: descriptor.Target,
					DeclaredCapabilities: descriptor.DeclaredCapabilities, Scope: descriptor.Scope, RunID: descriptor.RunID,
					WorkspaceID: descriptor.WorkspaceID, CallTimeoutMillis: descriptor.CallTimeoutMillis, MaxResultBytes: descriptor.MaxResultBytes}}
			var registered ExtensionMCPRegistrationView
			decodeDataStatus(t, extensionOnboardingRequest(t, onboardingAPI, ExtensionMCPRegistrationPath, registration), http.StatusAccepted, &registered)
			if registered.Server.State != "staged" || registered.NextStep != "approve_discovery" || onboardingRequests.Load() != 0 {
				t.Fatal("first registration contacted the peer or skipped review", registered, onboardingRequests.Load())
			}
			var before ExtensionInventoryView
			decodeData(t, performRequest(t, onboardingAPI, http.MethodGet, ExtensionInventoryPath+"?run_id="+run.ID, testAccessToken,
				"127.0.0.1:8765", "127.0.0.1:45000", nil), &before)
			if len(before.MCPServers) != 1 || len(before.MCPCalls) != 0 {
				t.Fatal("registered inventory invented a call receipt", before)
			}
			baseExtension := "/api/v1/extensions/mcp/" + descriptor.ID
			var reviewed ExtensionMCPServerView
			decodeDataStatus(t, extensionOnboardingRequest(t, onboardingAPI, baseExtension+"/review",
				ExtensionMCPReviewRequestView{Version: ExtensionControlProtocol, Action: mcp.ReviewApproveDiscovery,
					ExpectedDescriptorFingerprint: registered.Server.DescriptorFingerprint}), http.StatusAccepted, &reviewed)
			if onboardingRequests.Load() != 0 {
				t.Fatal("discovery approval itself contacted the peer")
			}
			decodeDataStatus(t, extensionOnboardingRequest(t, onboardingAPI, baseExtension+"/refresh",
				ExtensionRefreshRequestView{Version: ExtensionControlProtocol}), http.StatusAccepted, &reviewed)
			if reviewed.State != "capabilities_pending" || calls.Load() != 0 {
				t.Fatal("discovery skipped capability review or invoked a tool", reviewed)
			}
			decodeDataStatus(t, extensionOnboardingRequest(t, onboardingAPI, baseExtension+"/review",
				ExtensionMCPReviewRequestView{Version: ExtensionControlProtocol, Action: mcp.ReviewEnableCapabilities,
					ExpectedDescriptorFingerprint: reviewed.DescriptorFingerprint,
					ExpectedCapabilityFingerprint: reviewed.Capabilities.Fingerprint}), http.StatusAccepted, &reviewed)
			record, err := st.GetMCPClientServer(ctx, descriptor.ID)
			if err != nil {
				t.Fatal(err)
			}
			provider.payload, _ = json.Marshal(toolgateway.MCPToolCallPayload{Version: toolgateway.MCPClientToolProtocolVersion, ServerID: descriptor.ID, ToolName: "lookup", CapabilityFingerprint: record.ApprovedCapabilityFingerprint, Arguments: json.RawMessage(`{"query":"exact review intent"}`)})
			router := llm.NewRouter(llm.ModelRef{Provider: provider.Name(), Model: "fixture"})
			router.RegisterProvider(provider)
			checker := policy.NewDefaultChecker()
			dependencies := application.RunRuntimeDependencies{ExecutionCapabilities: caps, MCPClient: manager}
			supervisor := application.NewAgentRunnerWithRuntime(st, router, checker, dependencies)
			execution := application.NewRunExecutionHandoffWithRuntime(st, router, checker, dependencies)
			lifecycle := application.NewRunLifecycleControlService(st)
			threads := application.NewThreadTurnServiceWithExecutionCapabilities(st, lifecycle, execution, caps)
			controller := application.NewApprovalControlService(st, toolgateway.New(st, checker), checker)
			api, err := New(st, Config{AccessToken: testAccessToken, ControlToken: testControlToken, RunCreationEnabled: true, SessionMessageEnabled: true, RunLifecycleEnabled: true, RunLifecycleController: lifecycle, RunExecutionEnabled: true, RunExecutionController: execution, ThreadTurnController: threads, ApprovalControlEnabled: true, ApprovalController: controller, ExtensionControlEnabled: true, ExtensionController: extensions})
			if err != nil {
				t.Fatal(err)
			}
			if _, err = application.NewRunService(st).Start(ctx, run.ID); err != nil {
				t.Fatal(err)
			}
			runtime.Store(true)
			result, err := supervisor.Step(ctx, run.ID)
			// The enabled catalog refreshes before the model round. Only catalog
			// requests may precede approval; tools/call still requires approval.
			if err != nil || result.RunStatus != domain.RunWaitingApproval || catalogRequests.Load() == 0 || requests.Load() != catalogRequests.Load() || calls.Load() != 0 || provider.requests != 1 {
				t.Fatalf("preflight %+v err=%v peer=%d catalog=%d tools=%d model=%d", result, err, requests.Load(), catalogRequests.Load(), calls.Load(), provider.requests)
			}
			threadList, _ := readThreadList(t, api, "/api/v1/threads")
			if len(threadList) != 1 || threadList[0].ExecutionState != "waiting_approval" || threadList[0].ComposerState != "waiting_approval" {
				t.Fatalf("real MCP approval wait was not projected in Thread list: %+v", threadList)
			}
			if mode == "cancel" {
				if _, err := application.NewRunService(st).Cancel(ctx, run.ID); err != nil {
					t.Fatal(err)
				}
				threadList, _ = readThreadList(t, api, "/api/v1/threads")
				if len(threadList) != 1 || threadList[0].ExecutionState != "cancelled" || calls.Load() != 0 || provider.requests != 1 {
					t.Fatalf("retained MCP approval hid explicit cancellation: %+v", threadList)
				}
				return
			}
			approvals, err := st.ListApprovals(ctx, approval.ListFilter{RunID: run.ID, ToolName: mcp.OperationApprovalTool})
			if err != nil || len(approvals) != 1 {
				t.Fatalf("approvals %+v %v", approvals, err)
			}
			base := "/api/v1/runs/" + run.ID + "/approvals/" + approvals[0].ID
			queueResponse := performRequest(t, api, http.MethodGet, "/api/v1/runs/"+run.ID+"/approvals", testAccessToken, "127.0.0.1:8765", "127.0.0.1:45000", nil)
			var queue ApprovalQueueView
			decodeDataStatus(t, queueResponse, http.StatusOK, &queue)
			if len(queue.Items) != 1 || len(queue.Items[0].AllowedActions) != 2 {
				t.Fatalf("MCP pending queue omitted one-call review: %+v", queue)
			}
			beforePreviewRequests := requests.Load()
			previewResponse := performRequest(t, api, http.MethodGet, base+"/preview", testAccessToken, "127.0.0.1:8765", "127.0.0.1:45000", nil)
			var preview ApprovalPreviewView
			decodeDataStatus(t, previewResponse, http.StatusOK, &preview)
			if !preview.SourceCurrent || preview.Effect != "mcp_server_and_tool" || !strings.Contains(previewResponse.Body.String(), "exact review intent") || requests.Load() != beforePreviewRequests || calls.Load() != 0 {
				t.Fatalf("preview %+v", preview)
			}
			var decision ApprovalDecisionControlView
			for replay := 0; replay < 2; replay++ {
				response := performControlPathRequest(t, api, base+"/decision", "http-mcp-approve-once", strings.NewReader(`{"version":"approval_control.v1","action":"approve_once"}`))
				decodeDataStatus(t, response, http.StatusAccepted, &decision)
				if decision.Continuation == nil || decision.Continuation.State != "completed" || calls.Load() != 1 || provider.requests != 2 || !provider.observedResult {
					t.Fatalf("HTTP same-turn continuation %+v calls=%d models=%d", decision, calls.Load(), provider.requests)
				}
				if replay == 1 && (!decision.Replayed || !decision.Continuation.Replayed) {
					t.Fatal("review replay not idempotent")
				}
			}
			var after ExtensionInventoryView
			afterResponse := performRequest(t, api, http.MethodGet, ExtensionInventoryPath+"?run_id="+run.ID, testAccessToken,
				"127.0.0.1:8765", "127.0.0.1:45000", nil)
			decodeData(t, afterResponse, &after)
			if len(after.MCPCalls) != 1 || after.MCPCalls[0].ServerID != descriptor.ID || after.MCPCalls[0].ToolName != "lookup" ||
				after.MCPCalls[0].Status != "completed" || after.MCPCalls[0].ResultBytes == 0 || after.MCPCalls[0].CompletedAt == "" {
				t.Fatal("actual gated invocation did not produce its read-back receipt", after)
			}
			writeExtensionOnboardingEvidence(t, "extension-mcp-invocation-"+mode+".json", afterResponse)
			var registrationReplay ExtensionMCPRegistrationView
			decodeDataStatus(t, extensionOnboardingRequest(t, api, ExtensionMCPRegistrationPath, registration), http.StatusAccepted, &registrationReplay)
			if !registrationReplay.Replayed || registrationReplay.NextStep != "request_execution" || registrationReplay.Server.State != "enabled" {
				t.Fatal("registration replay lost current explicit reviews", registrationReplay)
			}
			// A retained review alone does not mean the peer is currently callable.
			// Simulate its persisted unavailable health without changing the review.
			record, err = st.GetMCPClientServer(ctx, descriptor.ID)
			if err != nil {
				t.Fatal(err)
			}
			previousGeneration := record.Generation
			record.Health, record.HealthMessage = mcp.HealthUnavailable, "fixture peer unavailable"
			record.Generation++
			record.UpdatedAt = time.Now().UTC()
			if _, err = st.UpdateMCPClientServer(ctx, record, previousGeneration); err != nil {
				t.Fatal(err)
			}
			decodeDataStatus(t, extensionOnboardingRequest(t, api, ExtensionMCPRegistrationPath, registration), http.StatusAccepted, &registrationReplay)
			available, err := manager.Capabilities(ctx, run.ID, workspace.ID)
			if err != nil || len(available.Servers) != 0 || registrationReplay.Server.State != "enabled" ||
				registrationReplay.Server.Health != "unavailable" || registrationReplay.NextStep != "refresh" {
				t.Fatal("registration replay confused a retained review with callable health", registrationReplay, available, err)
			}
			if output := os.Getenv("UC_MCP_HTTP_EVIDENCE"); output != "" {
				raw, _ := json.MarshalIndent(map[string]any{"queue": queue, "preview": preview, "decision": decision, "peer_tool_calls": calls.Load(), "model_calls": provider.requests}, "", "  ")
				if err = os.WriteFile(filepath.Join(output, "mcp-approval-http-"+mode+".json"), raw, 0600); err != nil {
					t.Fatal(err)
				}
			}
		})
	}
}
