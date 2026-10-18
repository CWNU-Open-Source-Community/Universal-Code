package application_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"cyberagent-workbench/internal/application"
	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/llm"
	"cyberagent-workbench/internal/mcp"
	"cyberagent-workbench/internal/policy"
	"cyberagent-workbench/internal/store"
)

type workerMCPObservedClient struct {
	manager *mcp.Manager
	observe func(context.Context, mcp.InvokeRequest)
	closed  []string
}

func (c *workerMCPObservedClient) Capabilities(ctx context.Context, run, workspace string) (mcp.ScopedCapabilities, error) {
	return c.manager.Capabilities(ctx, run, workspace)
}
func (c *workerMCPObservedClient) RefreshCapabilities(ctx context.Context, run, workspace string, check func(context.Context) error, revoked <-chan struct{}) (mcp.ScopedCapabilities, error) {
	return c.manager.RefreshCapabilities(ctx, run, workspace, check, revoked)
}
func (c *workerMCPObservedClient) RefreshWorkerCapabilities(ctx context.Context, run, workspace string, check func(context.Context) error, revoked <-chan struct{}, owned *mcp.InvocationWorkspace) (mcp.ScopedCapabilities, error) {
	return c.manager.RefreshWorkerCapabilities(ctx, run, workspace, check, revoked, owned)
}
func (c *workerMCPObservedClient) Invoke(ctx context.Context, r mcp.InvokeRequest) (mcp.ClientCallResult, error) {
	c.observe(ctx, r)
	return c.manager.Invoke(ctx, r)
}

func (c *workerMCPObservedClient) CloseWorkspaceScope(runID, scopeID string) {
	c.closed = append(c.closed, scopeID)
	c.manager.CloseWorkspaceScope(runID, scopeID)
}

func workerIsolationFullMode(t *testing.T, st *store.SQLiteStore, runID string) domain.ExecutionPermissionRuntimeCapabilities {
	t.Helper()
	capabilities := domain.ExecutionPermissionRuntimeCapabilities{OperatorApprovalEnabled: true, DangerFullAccessEnabled: true, RuntimeAuthority: domain.NewExecutionPermissionRuntimeAuthority()}
	if _, err := application.NewRunService(st).Pause(t.Context(), runID); err != nil {
		t.Fatal(err)
	}
	if _, err := application.NewRunExecutionPermissionService(st, capabilities).Change(t.Context(), application.ChangeRunExecutionPermissionRequest{RunID: runID, Mode: "full", OperationKey: "worker-isolation-full-0001", RequestedBy: "operator", ConfirmFull: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := application.NewRunService(st).Resume(t.Context(), runID); err != nil {
		t.Fatal(err)
	}
	return capabilities
}

func workerIsolationSibling(t *testing.T, st *store.SQLiteStore, run domain.Run, first domain.AgentNode) domain.AgentNode {
	t.Helper()
	child, _, err := st.AdmitSpecialist(t.Context(), domain.SpecialistAdmission{AgentID: "agent-isolation-sibling", SessionID: "session-isolation-sibling", RunID: run.ID, ParentAgentID: first.ParentID, Title: "Sibling worker", Skills: []string{"model.chat"}, Worker: &domain.WorkerProfile{}, TurnLimit: 4, TokenLimit: 1000, ToolCallLimit: 12, MaxChildren: 2, CreatedAt: time.Now().UTC()}, "worker-isolation-sibling-0001")
	if err != nil {
		t.Fatal(err)
	}
	payload, _ := json.Marshal(domain.AgentInstructionPayload{Version: domain.SpecialistInstructionVersion, Instruction: "Use only your own worker resources."})
	if _, _, err = st.SendAgentMessage(t.Context(), domain.AgentMessage{ID: "message-isolation-sibling", RunID: run.ID, SenderAgentID: first.ParentID, RecipientAgentID: child.ID, Kind: domain.AgentMessageInstruction, Semantic: domain.AgentMessageSemanticMessage, PayloadJSON: string(payload)}, "worker-isolation-message-0001"); err != nil {
		t.Fatal(err)
	}
	return child
}

func workerIsolationFinish(t *testing.T) llm.ChatResponse {
	return llm.ChatResponse{Text: specialistResponse(t, domain.SpecialistAction{Version: domain.SpecialistLifecycleVersion, Kind: domain.SpecialistActionFinish, Message: "Worker isolation verified", Report: &domain.CompletionReport{Version: domain.CompletionReportVersion, Outcome: domain.CompletionSucceeded, Summary: "Used independently owned tools.", WorkItemIDs: []string{}, NoteIDs: []string{}}}), Usage: llm.Usage{InputTokens: 1, OutputTokens: 1, TotalTokens: 2}}
}

func TestExecutionWorkersMCPRealTransportUsesOwnedSessionsAndRejectsForgedSources(t *testing.T) {
	testExecutionWorkersMCPTransport(t, false)
}

func TestExecutionWorkersMCPRemoteErrorReachesModel(t *testing.T) {
	testExecutionWorkersMCPTransport(t, true)
}

func TestExecutionWorkerMCPRealTransportSurvivesContinueAndClosesOnFinish(t *testing.T) {
	testExecutionWorkersMCPTransport(t, false, true)
}

func testExecutionWorkersMCPTransport(t *testing.T, remoteError bool, resume ...bool) {
	continued := len(resume) > 0 && resume[0]
	rootPath := t.TempDir()
	provider := &specialistTestProvider{}
	st, run, first, _ := newScopedSubagentFixture(t, provider, domain.Budget{MaxTurns: 20, MaxTokens: 10000, MaxToolCalls: 30}, 4, 1000, false, domain.SpecialistAdmission{Worker: &domain.WorkerProfile{}, ToolCallLimit: 12, MaxChildren: 2}, "", rootPath)
	caps := workerIsolationFullMode(t, st, run.ID)
	sibling := workerIsolationSibling(t, st, run, first)
	var mu sync.Mutex
	var initialized int
	var peerSessions []string
	peer := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		var q struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
		}
		if err := json.NewDecoder(r.Body).Decode(&q); err != nil {
			w.WriteHeader(400)
			return
		}
		if len(q.ID) == 0 {
			w.WriteHeader(202)
			return
		}
		var body string
		switch q.Method {
		case "initialize":
			mu.Lock()
			initialized++
			id := fmt.Sprintf("worker-connection-%d", initialized)
			mu.Unlock()
			w.Header().Set("Mcp-Session-Id", id)
			body = `{"protocolVersion":"2025-06-18","capabilities":{"tools":{}},"serverInfo":{"name":"worker-peer","version":"1"}}`
		case "tools/list":
			body = `{"tools":[{"name":"lookup","inputSchema":{"type":"object","properties":{},"additionalProperties":false},"annotations":{"readOnlyHint":true,"destructiveHint":false}}]}`
		case "tools/call":
			mu.Lock()
			peerSessions = append(peerSessions, r.Header.Get("Mcp-Session-Id"))
			mu.Unlock()
			body = `{"content":[{"type":"text","text":"worker transport verified"}]}`
			if remoteError {
				body = `{"isError":true,"content":[{"type":"text","text":"worker transport verified"}]}`
			}
		default:
			t.Errorf("unexpected MCP method %s", q.Method)
			w.WriteHeader(400)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": q.ID, "result": json.RawMessage(body)})
	}))
	defer peer.Close()
	manager, err := mcp.NewClientManager(st, nil, mcp.ManagerOptions{HTTPClient: peer.Client()})
	if err != nil {
		t.Fatal(err)
	}
	defer manager.Close()
	mission, err := st.GetMission(t.Context(), run.MissionID)
	if err != nil {
		t.Fatal(err)
	}
	descriptor := mcp.ServerDescriptor{ProtocolVersion: mcp.ClientProtocolVersion, ID: "worker-peer", Name: "Worker peer", Transport: mcp.TransportStreamableHTTP, Target: peer.URL, DeclaredCapabilities: []mcp.CapabilityKind{mcp.CapabilityTools}, Scope: mcp.ScopeRun, RunID: run.ID, WorkspaceID: mission.WorkspaceID, Source: mcp.Source{Kind: "manual", URI: "operator://worker-isolation"}, CallTimeoutMillis: 3000, MaxResultBytes: 8192}
	record, _, err := manager.Stage(t.Context(), descriptor)
	if err != nil {
		t.Fatal(err)
	}
	record, err = manager.Review(t.Context(), descriptor.ID, mcp.ReviewRequest{Action: mcp.ReviewApproveDiscovery, ExpectedDescriptorFingerprint: record.DescriptorFingerprint, ReviewedBy: "operator"})
	if err != nil {
		t.Fatal(err)
	}
	record, err = manager.Refresh(t.Context(), descriptor.ID)
	if err != nil {
		t.Fatal(err)
	}
	record, err = manager.Review(t.Context(), descriptor.ID, mcp.ReviewRequest{Action: mcp.ReviewEnableCapabilities, ExpectedDescriptorFingerprint: record.DescriptorFingerprint, ExpectedCapabilityFingerprint: record.Capabilities.Fingerprint, ReviewedBy: "operator"})
	if err != nil {
		t.Fatal(err)
	}
	current := first
	scopes := map[string]string{}
	client := &workerMCPObservedClient{manager: manager, observe: func(ctx context.Context, r mcp.InvokeRequest) {
		if r.Subject.ActorID != current.ID || r.Subject.RunID != run.ID || r.Workspace == nil || r.Workspace.ScopeID == "" {
			t.Fatalf("MCP borrowed parent actor or workspace: %+v", r)
		}
		if owner := scopes[r.Workspace.ScopeID]; owner != "" && owner != current.ID {
			t.Fatal("siblings shared MCP connection scope")
		}
		scopes[r.Workspace.ScopeID] = current.ID
		owned, err := r.Workspace.Resolve(ctx)
		if err != nil || filepath.Clean(owned.Path) != filepath.Clean(rootPath) || owned.Fingerprint == "" {
			t.Fatalf("worker root binding: %+v %v", owned, err)
		}
		active, err := st.GetAgentNode(ctx, current.ID)
		if err != nil {
			t.Fatal(err)
		}
		ref := domain.AgentAttemptRef{RunID: run.ID, AgentID: current.ID, AttemptID: active.ActiveAttemptID}
		rounds, err := st.ListSpecialistToolRounds(ctx, ref)
		if err != nil || len(rounds) != 1 || len(rounds[0].Calls) != 1 {
			t.Fatalf("worker intent missing: %+v %v", rounds, err)
		}
		c := rounds[0].Calls[0]
		attempt, found, err := st.GetAgentAttempt(ctx, ref.AttemptID)
		if err != nil || !found {
			t.Fatal(err)
		}
		source := domain.SpecialistToolSource{Ref: ref, CallID: c.CallID, SessionID: current.SessionID, WorkspaceID: mission.WorkspaceID, Name: c.Name, PayloadJSON: c.PayloadJSON, AuthorityJSON: c.AuthorityJSON, ChargeID: c.ChargeID, LeaseID: attempt.LeaseID, LeaseGeneration: attempt.LeaseGeneration, RequireStarted: true}
		if _, err := st.ValidateSpecialistToolSource(ctx, source); err != nil {
			t.Fatalf("real worker source rejected: %v", err)
		}
		for _, change := range []func(*domain.SpecialistToolSource){func(s *domain.SpecialistToolSource) { s.SessionID = run.SessionID }, func(s *domain.SpecialistToolSource) { s.Ref.AgentID = current.ParentID }, func(s *domain.SpecialistToolSource) { s.PayloadJSON = `{"forged":true}` }, func(s *domain.SpecialistToolSource) { s.Name = "browser_status" }} {
			forged := source
			change(&forged)
			if _, err := st.ValidateSpecialistToolSource(ctx, forged); err == nil {
				t.Fatalf("forged child source accepted: %+v", forged)
			}
		}
	}}
	provider.respond = func(index int, request llm.ChatRequest) (llm.ChatResponse, error) {
		if index%2 == 0 {
			payload, _ := json.Marshal(map[string]any{"version": "mcp-client.v1", "server_id": descriptor.ID, "tool_name": "lookup", "capability_fingerprint": record.ApprovedCapabilityFingerprint, "arguments": map[string]any{}})
			return llm.ChatResponse{ToolCalls: []llm.ToolCall{{ID: "lookup", Name: "mcp_tool_call", Arguments: payload}}, Usage: llm.Usage{InputTokens: 1, OutputTokens: 1, TotalTokens: 2}}, nil
		}
		messages := request.Messages
		if len(messages) == 0 || len(messages[len(messages)-1].ToolResults) != 1 || !strings.Contains(messages[len(messages)-1].ToolResults[0].Content, "worker transport verified") {
			t.Fatalf("worker did not receive real MCP response: %+v", messages)
		}
		if received := messages[len(messages)-1].ToolResults[0]; received.IsError != remoteError {
			t.Fatalf("remote error state changed before the model: %+v", received)
		}
		if continued && index == 1 {
			return llm.ChatResponse{Text: specialistResponse(t, domain.SpecialistAction{Version: domain.SpecialistLifecycleVersion, Kind: domain.SpecialistActionContinue, Message: "Continue using this MCP session."}), Usage: llm.Usage{InputTokens: 1, OutputTokens: 1, TotalTokens: 2}}, nil
		}
		return workerIsolationFinish(t), nil
	}
	router := llm.NewRouter(llm.ModelRef{Provider: provider.Name(), Model: "model"})
	router.RegisterProvider(provider)
	runtime := application.NewAgentRunner(st, router, policy.NewDefaultChecker()).WithExecutionPermissionCapabilities(caps).WithMCPClient(client)
	service := application.NewSpecialistExecutionService(st, router, policy.NewDefaultChecker()).WithWorkerRuntime(runtime)
	children := []domain.AgentNode{first, sibling}
	if continued {
		children = []domain.AgentNode{first, first, sibling}
	}
	for index, child := range children {
		current = child
		result, err := service.ExecuteSpecialist(t.Context(), application.ExecuteSpecialistRequest{RunID: run.ID, AgentID: child.ID, OperationKey: fmt.Sprintf("worker-mcp-%s-%d", child.ID, index)})
		if err != nil || result.WaitingApproval || result.ToolCalls != 1 {
			t.Fatalf("MCP worker execution: %+v %v", result, err)
		}
		if continued {
			wantStatus, wantClosed := domain.AgentAttemptFinished, index
			if index == 0 {
				wantStatus = domain.AgentAttemptContinued
			}
			if result.AttemptStatus != wantStatus || len(client.closed) != wantClosed {
				t.Fatalf("MCP lifetime after turn %d: status=%s closed=%v", index, result.AttemptStatus, client.closed)
			}
		}
		if remoteError {
			rounds, err := st.ListSpecialistToolRounds(t.Context(), domain.AgentAttemptRef{RunID: run.ID, AgentID: child.ID, AttemptID: result.AttemptID})
			if err != nil || rounds[0].Calls[0].Status != domain.SupervisorToolFailed || rounds[0].Calls[0].ErrorCode != "remote_tool_error" {
				t.Fatalf("remote error was lost from the durable worker receipt: %+v %v", rounds, err)
			}
		}
	}
	mu.Lock()
	defer mu.Unlock()
	if continued {
		if len(peerSessions) != 3 || peerSessions[0] == "" || peerSessions[0] != peerSessions[1] || peerSessions[1] == peerSessions[2] {
			t.Fatalf("continuation did not preserve only its owned MCP connection: %v", peerSessions)
		}
		return
	}
	if len(peerSessions) != 2 || peerSessions[0] == "" || peerSessions[0] == peerSessions[1] {
		t.Fatalf("siblings reused transport connection: %v", peerSessions)
	}
	t.Logf("two native workers used distinct MCP sessions %v and owned workspace scope IDs", peerSessions)
}
