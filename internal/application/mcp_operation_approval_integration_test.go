package application

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/llm"
	"cyberagent-workbench/internal/mcp"
	"cyberagent-workbench/internal/policy"
	"cyberagent-workbench/internal/store"
	"cyberagent-workbench/internal/toolcontract"
	"cyberagent-workbench/internal/toolgateway"
	"cyberagent-workbench/internal/tools"
)

type mcpReviewChecker struct {
	policy.Checker
	require, deny bool
}

func (f *mcpApprovalFixture) dispatchScope(t *testing.T) (toolgateway.MCPExecutionScope, toolgateway.MCPToolCallPayload) {
	t.Helper()
	a, err := mcp.DecodeSupervisorCallAuthority(json.RawMessage(f.call.AuthorityJSON))
	if err != nil {
		t.Fatal(err)
	}
	p, _, err := toolgateway.NormalizeMCPToolPayload(json.RawMessage(f.call.PayloadJSON))
	if err != nil {
		t.Fatal(err)
	}
	return toolgateway.MCPExecutionScope{InvocationID: "exact-mcp-dispatch", RunID: a.RunID, MissionID: a.MissionID, WorkspaceID: a.WorkspaceID,
		AgentID: f.call.AgentID, AgentAttemptID: f.call.AgentAttemptID, Surface: f.turn.Mode.Surface, Phase: f.turn.Mode.Phase, Role: f.turn.Agent.Role,
		PermissionMode: a.PermissionMode, PermissionSnapshotID: a.PermissionSnapshotID, PermissionRevision: a.PermissionRevision,
		PermissionGeneration: a.PermissionGeneration, PermissionRuntimeEpoch: a.PermissionRuntimeEpoch, RunAuthorizationFence: a.RunAuthorizationFence,
		LeaseID: f.turn.Checkpoint.LeaseID, LeaseGeneration: f.turn.Checkpoint.LeaseGeneration, RequestedBy: "run_supervisor",
		SupervisorToolCallID: f.call.CallID, SupervisorTurn: f.call.Turn,
		PolicyDecision: toolgateway.Decision{Allowed: true, Approval: toolgateway.ApprovalPerCall, Risk: "high", Reason: "exact host review"}}, p
}

func TestMCPOperationApprovalActualGuardsRejectBypassAndDrift(t *testing.T) {
	for _, scenario := range []string{"pending_full", "denied_full", "payload_replaced", "cancelled_context", "revoke_discovery", "lease_discovery", "disable_discovery"} {
		t.Run(scenario, func(t *testing.T) {
			mode := domain.RunExecutionPermissionAsk
			if strings.HasSuffix(scenario, "_full") {
				mode = domain.RunExecutionPermissionFull
			}
			f := newMCPOperationApprovalFixture(t, mode, false, true)
			if waiting, err := f.resume(t); err != nil || !waiting {
				t.Fatal("missing review", err)
			}
			if scenario == "denied_full" {
				if err := f.decide(t, ApprovalControlDeny); err != nil {
					t.Fatal(err)
				}
			} else if scenario != "pending_full" {
				if err := f.decide(t, ApprovalControlApproveOnce); err != nil {
					t.Fatal(err)
				}
			}
			// Exercise the actual executor even if an upstream caller ignores the
			// preflight outcome. Neither Full nor a forged payload may bypass consent.
			if _, err := f.st.RecordSupervisorToolExecutionStarted(t.Context(), f.turn.Checkpoint, f.call.CallID); err != nil {
				t.Fatal(err)
			}
			scope, payload := f.dispatchScope(t)
			if scenario == "payload_replaced" {
				payload.Arguments = json.RawMessage(`{"value":1}`)
			}
			f.onRequest = func(method string) {
				if method != "tools/list" {
					return
				}
				switch scenario {
				case "revoke_discovery":
					f.capabilities.RuntimeAuthority.RevokeRun(f.call.RunID)
				case "lease_discovery":
					lease, found, err := f.st.GetRunExecutionLease(t.Context(), f.call.RunID)
					if err != nil || !found {
						t.Error("missing lease", err)
						return
					}
					if _, _, err = f.st.ReleaseRunExecutionLease(t.Context(), lease); err != nil {
						t.Error(err)
					}
				case "disable_discovery":
					if _, err := f.manager.Review(t.Context(), f.server.Descriptor.ID, mcp.ReviewRequest{Action: mcp.ReviewDisable, ExpectedDescriptorFingerprint: f.server.DescriptorFingerprint, ReviewedBy: "operator"}); err != nil {
						t.Error(err)
					}
				}
			}
			executor, err := NewMCPClientToolExecutor(f.manager, f.st, f.capabilities)
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			if scenario == "cancelled_context" {
				cancel()
			}
			_, err = executor.ExecuteMCP(ctx, scope, payload)
			if err == nil || f.calls.Load() != 0 {
				t.Fatalf("actual dispatch guard bypassed: calls=%d err=%v", f.calls.Load(), err)
			}
			if strings.HasSuffix(scenario, "_discovery") {
				receipt, found := mcp.InvocationReceipt(err)
				if !found || receipt.State != toolcontract.ReceiptNotDispatched || f.requests.Load() == 0 {
					t.Fatalf("actual pre-tool guard receipt=%+v found=%t err=%v", receipt, found, err)
				}
			} else if f.requests.Load() != 0 {
				t.Fatal("denied operation reached MCP peer", f.requests.Load())
			}
		})
	}
}

func TestMCPOperationApprovalRealTLSLostReceiptNeverRepeats(t *testing.T) {
	f := newMCPOperationApprovalFixture(t, domain.RunExecutionPermissionAsk, false, false)
	if waiting, err := f.resume(t); err != nil || !waiting {
		t.Fatal("missing review", err)
	}
	if err := f.decide(t, ApprovalControlApproveOnce); err != nil {
		t.Fatal(err)
	}
	fault := &mcpReceiptFaultStore{SQLiteStore: f.st, fail: true}
	f.supervisor = NewAgentRunner(fault, nil, f.checker).WithExecutionPermissionCapabilities(f.capabilities).WithMCPClient(f.manager)
	if _, err := f.resume(t); err == nil || f.calls.Load() != 1 {
		t.Fatal("receipt fault did not follow the real send", err)
	}
	f.supervisor = NewAgentRunner(f.st, nil, f.checker).WithExecutionPermissionCapabilities(f.capabilities).WithMCPClient(f.manager)
	for attempt := 0; attempt < 2; attempt++ {
		if waiting, err := f.resume(t); err != nil || waiting || f.calls.Load() != 1 {
			t.Fatal("uncertain dispatch repeated", err)
		}
	}
	call, _, err := f.st.GetSupervisorApprovalCall(t.Context(), f.call.RunID, f.call.CallID)
	if err != nil || call.ErrorCode != "outcome_unknown" || !strings.Contains(call.ResultJSON, `"automatic_retry":"forbidden"`) {
		t.Fatalf("recovery lost uncertainty %+v %v", call, err)
	}
}

func (c *mcpReviewChecker) CheckToolCall(call tools.Call) policy.Decision {
	if call.Name == mcp.OperationApprovalTool {
		return policy.Decision{Allowed: !c.deny, NeedsApproval: c.require, Risk: "high", Reason: "fixture host rule"}
	}
	return c.Checker.CheckToolCall(call)
}

type mcpApprovalFixture struct {
	st              *store.SQLiteStore
	supervisor      *AgentRunner
	turn            domain.SupervisorTurn
	call            domain.SupervisorToolCall
	manager         *mcp.Manager
	server          mcp.ServerRecord
	capabilities    domain.ExecutionPermissionRuntimeCapabilities
	checker         *mcpReviewChecker
	requests, calls atomic.Int32
	response        atomic.Value
	runtime         atomic.Bool
	onRequest       func(string)
}

func newMCPOperationApprovalFixture(t *testing.T, mode domain.RunExecutionPermissionMode, cyberPlan bool, require bool, contentMethod ...string) *mcpApprovalFixture {
	t.Helper()
	ctx := t.Context()
	interactive := len(contentMethod) > 1 && contentMethod[1] == "interactive"
	method := ""
	if len(contentMethod) > 0 {
		method = contentMethod[0]
	}
	largeCatalog := method == "large_catalog"
	if largeCatalog {
		method = ""
	}
	f := &mcpApprovalFixture{checker: &mcpReviewChecker{Checker: policy.NewDefaultChecker(), require: require}}
	var err error
	f.st, err = store.Open(filepath.Join(t.TempDir(), "mcp-approval.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = f.st.Close() })
	workspace := store.WorkspaceRecord{ID: "mcp-approval-workspace", Name: "MCP fixture", RootPath: t.TempDir()}
	if err := f.st.SaveWorkspace(ctx, workspace); err != nil {
		t.Fatal(err)
	}
	request := CreateRunRequest{Goal: "Use the reviewed local fixture", Profile: "code", Surface: "code", Phase: "deliver", WorkspaceID: workspace.ID,
		Budget: domain.Budget{MaxTurns: 4, MaxTokens: 1000, MaxToolCalls: 8}}
	if cyberPlan {
		request.Profile, request.Surface, request.Phase = "review", "cyber", "plan"
	}
	runs := NewRunService(f.st)
	_, run, err := runs.Create(ctx, request)
	if err != nil {
		t.Fatal(err)
	}
	f.capabilities = domain.ExecutionPermissionRuntimeCapabilities{OperatorApprovalEnabled: true, DangerFullAccessEnabled: true,
		RuntimeAuthority: domain.NewExecutionPermissionRuntimeAuthority()}
	if mode != domain.RunExecutionPermissionAsk {
		if _, err = NewRunExecutionPermissionService(f.st, f.capabilities).Change(ctx, ChangeRunExecutionPermissionRequest{
			RunID: run.ID, Mode: string(mode), ConfirmFull: mode == domain.RunExecutionPermissionFull,
			OperationKey: "mcp-approval-mode-0001", RequestedBy: "test_operator", Reason: "exercise actual approval mode"}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err = runs.Start(ctx, run.ID); err != nil {
		t.Fatal(err)
	}
	lease, err := f.st.AcquireRunExecutionLease(ctx, domain.AcquireRunExecutionLeaseRequest{RunID: run.ID, OwnerID: "mcp-approval-worker", TTL: time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	f.turn, err = f.st.BeginSupervisorTurn(ctx, lease.Lease, "")
	if err != nil {
		t.Fatal(err)
	}
	peer := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
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
		if f.runtime.Load() {
			f.requests.Add(1)
			if f.onRequest != nil {
				f.onRequest(request.Method)
			}
		}
		if len(request.ID) == 0 {
			w.WriteHeader(202)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		var result json.RawMessage
		switch request.Method {
		case "server/discover":
			if interactive {
				result = json.RawMessage(`{"supportedVersions":["2026-07-28"],"capabilities":{"tools":{}},"_meta":{"io.modelcontextprotocol/serverInfo":{"name":"approval-fixture","version":"1"}},"resultType":"complete","ttlMs":0,"cacheScope":"private"}`)
			} else {
				w.WriteHeader(404)
				return
			}
		case "initialize":
			result = json.RawMessage(`{"protocolVersion":"2024-11-05","capabilities":{"tools":{}},"serverInfo":{"name":"approval-fixture","version":"1"}}`)
			if method != "" {
				result = json.RawMessage(`{"protocolVersion":"2024-11-05","capabilities":{"resources":{},"prompts":{}},"serverInfo":{"name":"approval-fixture","version":"1"}}`)
			}
			if interactive {
				result = json.RawMessage(strings.ReplaceAll(string(result), "2024-11-05", "2026-07-28"))
			}
		case "resources/list":
			result = json.RawMessage(`{"resources":[{"uri":"fixture://review","name":"Review"}]}`)
		case "resources/templates/list":
			result = json.RawMessage(`{"resourceTemplates":[]}`)
		case "prompts/list":
			result = json.RawMessage(`{"prompts":[{"name":"review","arguments":[{"name":"language","required":true}]}]}`)
		case "resources/read", "prompts/get":
			f.calls.Add(1)
			if request.Method == "resources/read" {
				result = json.RawMessage(`{"contents":[{"uri":"fixture://review","text":"reviewed resource"}]}`)
			} else {
				result = json.RawMessage(`{"messages":[{"role":"user","content":{"type":"text","text":"reviewed prompt"}}]}`)
			}
		case "tools/list":
			result = json.RawMessage(`{"tools":[{"name":"lookup","annotations":{"readOnlyHint":true,"destructiveHint":false},"inputSchema":{"type":"object","properties":{"value":{"type":"integer"}},"additionalProperties":false}}]}`)
			if largeCatalog {
				var catalog struct {
					Tools []map[string]any `json:"tools"`
				}
				_ = json.Unmarshal(result, &catalog)
				for i := 0; i < 130; i++ {
					catalog.Tools = append(catalog.Tools, map[string]any{"name": fmt.Sprintf("note_%03d", i), "description": "Read a note", "inputSchema": map[string]any{"type": "object"}})
				}
				catalog.Tools = append(catalog.Tools, map[string]any{"name": "zz_read_archive", "description": "检索归档文档", "inputSchema": map[string]any{"type": "object", "properties": map[string]any{"value": map[string]any{"type": "integer"}}, "additionalProperties": false}})
				result, _ = json.Marshal(catalog)
			}
		case "tools/call":
			f.calls.Add(1)
			if !strings.Contains(string(request.Params), "9007199254740993") {
				t.Error("exact native input was changed")
			}
			result = json.RawMessage(`{"content":[{"type":"text","text":"reviewed fixture result"}]}`)
			if response := f.response.Load(); response != nil {
				result = response.(json.RawMessage)
			}
		default:
			_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": request.ID, "error": map[string]any{"code": -32601, "message": "unsupported"}})
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": request.ID, "result": result})
	}))
	t.Cleanup(peer.Close)
	f.manager, err = mcp.NewClientManager(f.st, &mcpRuntimeCredentialFixture{}, mcp.ManagerOptions{HTTPClient: peer.Client(), InputBroker: mcp.NewInputBroker()})
	if err != nil {
		t.Fatal(err)
	}
	descriptor := mcp.ServerDescriptor{ProtocolVersion: mcp.ClientProtocolVersion, ID: "mcp-approval-server", Name: "Approval fixture",
		Transport: mcp.TransportStreamableHTTP, Target: peer.URL, DeclaredCapabilities: []mcp.CapabilityKind{mcp.CapabilityTools},
		Scope: mcp.ScopeRun, RunID: run.ID, WorkspaceID: workspace.ID, Source: mcp.Source{Kind: "manual", URI: "operator://mcp-approval-fixture"}, CallTimeoutMillis: 3000, MaxResultBytes: 8192}
	if method != "" {
		descriptor.DeclaredCapabilities = []mcp.CapabilityKind{mcp.CapabilityResources, mcp.CapabilityPrompts}
	}
	f.server, _, err = f.manager.Stage(ctx, descriptor)
	if err != nil {
		t.Fatal(err)
	}
	f.server, err = f.manager.Review(ctx, descriptor.ID, mcp.ReviewRequest{Action: mcp.ReviewApproveDiscovery, ExpectedDescriptorFingerprint: f.server.DescriptorFingerprint, ReviewedBy: "operator"})
	if err != nil {
		t.Fatal(err)
	}
	f.server, err = f.manager.Refresh(ctx, descriptor.ID)
	if err != nil {
		t.Fatal(err)
	}
	f.server, err = f.manager.Review(ctx, descriptor.ID, mcp.ReviewRequest{Action: mcp.ReviewEnableCapabilities, ExpectedDescriptorFingerprint: f.server.DescriptorFingerprint, ExpectedCapabilityFingerprint: f.server.Capabilities.Fingerprint, ReviewedBy: "operator"})
	if err != nil {
		t.Fatal(err)
	}
	f.supervisor = NewAgentRunner(f.st, nil, f.checker).WithExecutionPermissionCapabilities(f.capabilities).WithMCPClient(f.manager)
	permission, err := f.st.GetRunExecutionPermission(ctx, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	advertisement, err := f.supervisor.supervisorMCPCapabilities(ctx, f.turn, permission)
	if err != nil || len(advertisement.Authority) == 0 {
		t.Fatalf("advertisement missing: %v", err)
	}
	payload, _ := json.Marshal(toolgateway.MCPToolCallPayload{Version: toolgateway.MCPClientToolProtocolVersion, ServerID: descriptor.ID, ToolName: "lookup", CapabilityFingerprint: f.server.ApprovedCapabilityFingerprint, Arguments: json.RawMessage(`{"value":9007199254740993}`)})
	if largeCatalog {
		payload = json.RawMessage(strings.Replace(string(payload), `"tool_name":"lookup"`, `"tool_name":"zz_read_archive"`, 1))
	}
	if method != "" {
		name, args := "resources.read", `{"uri":"fixture://review"}`
		if method == "prompts/get" {
			name, args = "prompts.get", `{"name":"review","arguments":{"language":"Go"}}`
		}
		payload, _ = json.Marshal(toolgateway.MCPToolCallPayload{Version: toolgateway.MCPContentToolProtocolVersion, Method: method, ServerID: descriptor.ID, ToolName: name, CapabilityFingerprint: f.server.ApprovedCapabilityFingerprint, Arguments: json.RawMessage(args)})
	}
	prepared, err := prepareSupervisorToolCalls([]llm.ToolCall{{ID: "provider-call", Name: mcp.OperationApprovalTool, Arguments: payload}}, run.ID, f.turn.Checkpoint.NextTurn, 1, f.turn.Mode.Surface, f.turn.Mode.Phase, mode, false, false, supervisorToolOptions{MCP: advertisement})
	if err != nil {
		t.Fatal(err)
	}
	attempt := llm.ModelAttempt{Number: 1, TransportAttempt: 1, MaxAttempts: 1, Provider: "offline-fixture", Model: "fixture"}
	if _, err = f.st.RecordSupervisorModelStarted(ctx, f.turn.Checkpoint, attempt); err != nil {
		t.Fatal(err)
	}
	attempt.Outcome = llm.OutcomeSuccess
	f.turn.Checkpoint, err = f.st.RecordSupervisorModelCompleted(ctx, f.turn.Checkpoint, attempt, llm.ChatResponse{Provider: attempt.Provider, Model: attempt.Model, ToolCalls: prepared})
	if err != nil {
		t.Fatal(err)
	}
	rounds, err := f.st.ListSupervisorToolRounds(ctx, f.turn.Checkpoint)
	if err != nil || len(rounds) != 1 || len(rounds[0].Calls) != 1 {
		t.Fatalf("durable call missing %v", err)
	}
	f.call = rounds[0].Calls[0]
	f.runtime.Store(true)
	return f
}

func (f *mcpApprovalFixture) resume(t *testing.T) (bool, error) {
	t.Helper()
	rounds, err := f.st.ListSupervisorToolRounds(t.Context(), f.turn.Checkpoint)
	if err != nil {
		return false, err
	}
	_, waiting, err := f.supervisor.resumeSupervisorTools(t.Context(), f.turn, rounds)
	return waiting, err
}
func (f *mcpApprovalFixture) decide(t *testing.T, action ApprovalControlAction) error {
	t.Helper()
	record, err := f.st.GetApprovalByProposal(t.Context(), f.call.CallID)
	if err != nil {
		return err
	}
	_, err = NewApprovalControlService(f.st, toolgateway.New(nil, f.checker), f.checker).Decide(t.Context(), DecideApprovalControlRequest{
		Version: ApprovalControlProtocolVersion, RunID: f.call.RunID, ApprovalID: record.ID, Action: action, OperationKey: "mcp-decision-0001", ReviewedBy: "operator"})
	return err
}

func TestMCPOperationApprovalRealTLSThreeModes(t *testing.T) {
	for _, mode := range []domain.RunExecutionPermissionMode{domain.RunExecutionPermissionAsk, domain.RunExecutionPermissionAuto, domain.RunExecutionPermissionFull} {
		for _, cyber := range []bool{false, true} {
			name := string(mode) + "/code_deliver"
			if cyber {
				name = string(mode) + "/cyber_plan"
			}
			t.Run(name, func(t *testing.T) {
				f := newMCPOperationApprovalFixture(t, mode, cyber, false)
				waiting, err := f.resume(t)
				if mode != domain.RunExecutionPermissionFull {
					if err != nil || !waiting || f.requests.Load() != 0 {
						t.Fatalf("approval preflight dispatched: waiting=%t requests=%d err=%v", waiting, f.requests.Load(), err)
					}
					_, started, err := f.st.GetSupervisorApprovalCall(t.Context(), f.call.RunID, f.call.CallID)
					if err != nil || started {
						t.Fatal("waiting call was marked started", err)
					}
					if err = f.decide(t, ApprovalControlApproveOnce); err != nil {
						t.Fatal(err)
					}
					waiting, err = f.resume(t)
				} else if _, err := f.st.GetApprovalByProposal(t.Context(), f.call.CallID); !errors.Is(err, sql.ErrNoRows) {
					t.Fatal("Full fabricated review", err)
				}
				if err != nil || waiting || f.calls.Load() != 1 {
					t.Fatalf("exact approved invocation waiting=%t calls=%d err=%v", waiting, f.calls.Load(), err)
				}
				call, _, err := f.st.GetSupervisorApprovalCall(t.Context(), f.call.RunID, f.call.CallID)
				if err != nil || call.Status != domain.SupervisorToolCompleted {
					t.Fatalf("call result %+v %v", call, err)
				}
				if waiting, err = f.resume(t); err != nil || waiting || f.calls.Load() != 1 {
					t.Fatal("terminal call replayed", err)
				}
			})
		}
	}
}

func TestMCPOperationApprovalPendingDenialAndRevocationNeverSend(t *testing.T) {
	for _, scenario := range []string{"deny", "pending", "disabled", "reenabled", "changed_descriptor", "permission", "cold_epoch", "cancelled", "started_unknown", "full_policy_denied", "full_policy_pending"} {
		t.Run(scenario, func(t *testing.T) {
			mode := domain.RunExecutionPermissionAsk
			if scenario == "permission" {
				mode = domain.RunExecutionPermissionAuto
			}
			if strings.HasPrefix(scenario, "full_") {
				mode = domain.RunExecutionPermissionFull
			}
			f := newMCPOperationApprovalFixture(t, mode, false, true)
			if waiting, err := f.resume(t); err != nil || !waiting || f.requests.Load() != 0 {
				t.Fatal("initial review", waiting, err)
			}
			if scenario == "deny" || scenario == "full_policy_denied" {
				if err := f.decide(t, ApprovalControlDeny); err != nil {
					t.Fatal(err)
				}
			}
			switch scenario {
			case "disabled", "reenabled":
				updated, err := f.manager.Review(t.Context(), f.server.Descriptor.ID, mcp.ReviewRequest{Action: mcp.ReviewDisable, ExpectedDescriptorFingerprint: f.server.DescriptorFingerprint, ReviewedBy: "operator"})
				if err != nil {
					t.Fatal(err)
				}
				if scenario == "reenabled" {
					updated, err = f.manager.Review(t.Context(), updated.Descriptor.ID, mcp.ReviewRequest{Action: mcp.ReviewApproveDiscovery, ExpectedDescriptorFingerprint: updated.DescriptorFingerprint, ReviewedBy: "operator"})
					if err != nil {
						t.Fatal(err)
					}
					// Explicit registration review is separate from the pending Run call.
					f.runtime.Store(false)
					updated, err = f.manager.Refresh(t.Context(), updated.Descriptor.ID)
					f.runtime.Store(true)
					if err != nil {
						t.Fatal(err)
					}
					if _, err = f.manager.Review(t.Context(), updated.Descriptor.ID, mcp.ReviewRequest{Action: mcp.ReviewEnableCapabilities, ExpectedDescriptorFingerprint: updated.DescriptorFingerprint, ExpectedCapabilityFingerprint: updated.Capabilities.Fingerprint, ReviewedBy: "operator"}); err != nil {
						t.Fatal(err)
					}
				}
			case "changed_descriptor":
				descriptor := f.server.Descriptor
				descriptor.Name = "Replaced reviewed source"
				if _, _, err := f.manager.Stage(t.Context(), descriptor); err == nil {
					t.Fatal("same server identity silently replaced its reviewed descriptor")
				}
			case "permission":
				if _, err := NewRunExecutionPermissionService(f.st, f.capabilities).Change(t.Context(), ChangeRunExecutionPermissionRequest{RunID: f.call.RunID, Mode: "ask", OperationKey: "mcp-mode-revoke-0001", RequestedBy: "operator", Reason: "changed intent"}); err != nil {
					t.Fatal(err)
				}
				lease, err := f.st.AcquireRunExecutionLease(t.Context(), domain.AcquireRunExecutionLeaseRequest{RunID: f.call.RunID, OwnerID: "mcp-post-revoke-worker", TTL: time.Minute})
				if err != nil {
					t.Fatal(err)
				}
				f.turn, err = f.st.BeginSupervisorTurn(t.Context(), lease.Lease, "")
				if err != nil {
					t.Fatal(err)
				}
			case "cold_epoch":
				cold := f.capabilities
				cold.RuntimeAuthority = domain.NewExecutionPermissionRuntimeAuthority()
				f.supervisor = NewAgentRunner(f.st, nil, f.checker).WithExecutionPermissionCapabilities(cold).WithMCPClient(f.manager)
			case "cancelled":
				f.capabilities.RuntimeAuthority.RevokeRun(f.call.RunID)
			case "started_unknown":
				if _, err := f.st.RecordSupervisorToolExecutionStarted(t.Context(), f.turn.Checkpoint, f.call.CallID); err != nil {
					t.Fatal(err)
				}
			}
			if scenario == "disabled" || scenario == "reenabled" || scenario == "permission" {
				if err := f.decide(t, ApprovalControlApproveOnce); err == nil {
					t.Fatal("stale source was approved")
				}
			}
			waiting, err := f.resume(t)
			wantWaiting := scenario == "pending" || scenario == "full_policy_pending" || scenario == "changed_descriptor"
			if err != nil || waiting != wantWaiting || f.requests.Load() != 0 || f.calls.Load() != 0 {
				t.Fatalf("stale/denied call sent: waiting=%t requests=%d err=%v", waiting, f.requests.Load(), err)
			}
			call, started, err := f.st.GetSupervisorApprovalCall(t.Context(), f.call.RunID, f.call.CallID)
			if err != nil {
				t.Fatal(err)
			}
			if scenario == "started_unknown" {
				if !started || call.ErrorCode != "outcome_unknown" {
					t.Fatalf("lost uncertainty %+v", call)
				}
			} else if started {
				t.Fatal("preflight manufactured execution start")
			}
		})
	}
}
