package application

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"cyberagent-workbench/internal/apperror"
	"cyberagent-workbench/internal/approval"
	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/llm"
	"cyberagent-workbench/internal/mcp"
	"cyberagent-workbench/internal/policy"
	"cyberagent-workbench/internal/store"
	"cyberagent-workbench/internal/toolgateway"
)

// Only the remote peer and model are controlled. Creation, review, dispatch,
// durable approval, Run leases and same-turn continuation use public services.
// Keeping the database path and TLS client here permits a real close/reopen
// without changing another lane's fixtures or retaining a closed store handle.
type mcpRecoveryAcceptanceFixture struct {
	path        string
	st          *store.SQLiteStore
	run         domain.Run
	pending     domain.SupervisorCheckpoint
	calls       []domain.SupervisorToolCall
	caps        domain.ExecutionPermissionRuntimeCapabilities
	peer        *httptest.Server
	manager     *mcp.Manager
	supervisor  *AgentRunner
	turns       *ThreadTurnService
	controller  *ApprovalControlService
	provider    *mcpRecoveryAcceptanceProvider
	payloads    []json.RawMessage
	runtime     atomic.Bool
	requests    atomic.Int32
	wireCalls   atomic.Int32
	wireMu      sync.Mutex
	wireArgs    []string
	entered     chan struct{}
	release     chan struct{}
	enterOnce   sync.Once
	releaseOnce sync.Once
}

type mcpRecoveryAcceptanceProvider struct {
	llm.MockProvider
	payloads []json.RawMessage
	mu       sync.Mutex
	requests int
	results  []llm.ToolResult
}

func (*mcpRecoveryAcceptanceProvider) Name() string              { return "mcp-recovery-acceptance" }
func (*mcpRecoveryAcceptanceProvider) SupportsTools(string) bool { return true }
func (p *mcpRecoveryAcceptanceProvider) Chat(_ context.Context, request llm.ChatRequest) (*llm.ChatResponse, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.requests++
	var results []llm.ToolResult
	for _, message := range request.Messages {
		results = append(results, message.ToolResults...)
	}
	response := &llm.ChatResponse{Provider: p.Name(), Model: "fixture", Usage: llm.Usage{InputTokens: 2, OutputTokens: 2, TotalTokens: 4}}
	if len(results) > 0 {
		p.results = append([]llm.ToolResult(nil), results...)
		raw, err := json.Marshal(domain.RootAction{Version: domain.RootLifecycleVersion, Kind: domain.RootActionContinue, Message: "Observed the exact durable MCP outcomes."})
		response.Text = string(raw)
		return response, err
	}
	if p.requests != 1 {
		return nil, fmt.Errorf("continuation lost its durable tool results at model request %d", p.requests)
	}
	found := false
	for _, spec := range request.Tools {
		found = found || spec.Name == mcp.OperationApprovalTool
	}
	if !found {
		return nil, fmt.Errorf("reviewed MCP tool was not advertised")
	}
	for i, payload := range p.payloads {
		response.ToolCalls = append(response.ToolCalls, llm.ToolCall{ID: fmt.Sprintf("consent-call-%d", i), Name: mcp.OperationApprovalTool, Arguments: payload})
	}
	return response, nil
}

func (p *mcpRecoveryAcceptanceProvider) StreamChat(ctx context.Context, request llm.ChatRequest) (<-chan llm.ChatChunk, error) {
	response, err := p.Chat(ctx, request)
	if err != nil {
		return nil, err
	}
	chunks := make(chan llm.ChatChunk, 2)
	if response.Text != "" {
		chunks <- llm.ChatChunk{Text: response.Text}
	}
	chunks <- llm.FinalChatChunk(response)
	close(chunks)
	return chunks, nil
}

func (f *mcpRecoveryAcceptanceFixture) serve(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodDelete {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	var request struct {
		ID     json.RawMessage `json:"id"`
		Method string          `json:"method"`
		Params json.RawMessage `json:"params"`
	}
	if json.NewDecoder(r.Body).Decode(&request) != nil {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	if f.runtime.Load() {
		// Enabled-server catalog refresh is authorized independently of the
		// pending tool consent. Keep invocation traffic separately observable.
		if request.Method != "initialize" && request.Method != "notifications/initialized" && request.Method != "tools/list" {
			f.requests.Add(1)
		}
	}
	if len(request.ID) == 0 {
		w.WriteHeader(http.StatusAccepted)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	var result json.RawMessage
	switch request.Method {
	case "initialize":
		result = json.RawMessage(`{"protocolVersion":"2024-11-05","capabilities":{"tools":{}},"serverInfo":{"name":"recovery-consent-peer","version":"1"}}`)
	case "tools/list":
		result = json.RawMessage(`{"tools":[{"name":"lookup","annotations":{"readOnlyHint":true},"inputSchema":{"type":"object","required":["query","value"],"properties":{"query":{"type":"string"},"value":{"type":"integer"}},"additionalProperties":false}}]}`)
	case "tools/call":
		f.wireCalls.Add(1)
		f.wireMu.Lock()
		f.wireArgs = append(f.wireArgs, string(request.Params))
		f.wireMu.Unlock()
		f.enterOnce.Do(func() { close(f.entered) })
		if f.release != nil {
			select {
			case <-f.release:
			case <-r.Context().Done():
				return
			}
		}
		result = json.RawMessage(`{"content":[{"type":"text","text":"exact approved recovery result"}]}`)
	default:
		_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": request.ID, "error": map[string]any{"code": -32601, "message": "unsupported"}})
		return
	}
	_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": request.ID, "result": result})
}

func (f *mcpRecoveryAcceptanceFixture) unblock() {
	if f.release != nil {
		f.releaseOnce.Do(func() { close(f.release) })
	}
}

func newMCPRecoveryAcceptanceRuntime(t *testing.T, mode domain.RunExecutionPermissionMode, callCount int, blocked bool) *mcpRecoveryAcceptanceFixture {
	t.Helper()
	f := &mcpRecoveryAcceptanceFixture{path: filepath.Join(t.TempDir(), "approval-recovery.db"), entered: make(chan struct{})}
	if blocked {
		f.release = make(chan struct{})
	}
	var err error
	f.st, err = store.Open(f.path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = f.st.Close() })
	f.peer = httptest.NewTLSServer(http.HandlerFunc(f.serve))
	t.Cleanup(f.peer.Close)
	t.Cleanup(f.unblock)
	workspace := store.WorkspaceRecord{ID: "recovery-consent-workspace", Name: "Owned MCP fixture", RootPath: t.TempDir()}
	if err = f.st.SaveWorkspace(t.Context(), workspace); err != nil {
		t.Fatal(err)
	}
	_, f.run, err = NewRunService(f.st).Create(t.Context(), CreateRunRequest{Goal: "Review exact MCP actions without repeating effects", Profile: "review", Surface: "cyber", Phase: "plan", WorkspaceID: workspace.ID, Interactive: true,
		ModelRoute: "mcp-recovery-acceptance/fixture", Budget: domain.Budget{MaxTurns: 8, MaxTokens: 50000, MaxToolCalls: 20}})
	if err != nil {
		t.Fatal(err)
	}
	// These Ask/Auto tests have no Full startup capability or activation.
	f.caps = domain.ExecutionPermissionRuntimeCapabilities{OperatorApprovalEnabled: true, RuntimeAuthority: domain.NewExecutionPermissionRuntimeAuthority()}
	if mode != domain.RunExecutionPermissionAsk {
		if _, err = NewRunExecutionPermissionService(f.st, f.caps).Change(t.Context(), ChangeRunExecutionPermissionRequest{RunID: f.run.ID, Mode: string(mode), OperationKey: "recovery-consent-mode-0001", RequestedBy: "operator"}); err != nil {
			t.Fatal(err)
		}
	}
	f.manager, err = mcp.NewClientManager(f.st, &mcpRuntimeCredentialFixture{}, mcp.ManagerOptions{HTTPClient: f.peer.Client()})
	if err != nil {
		t.Fatal(err)
	}
	descriptor := mcp.ServerDescriptor{ProtocolVersion: mcp.ClientProtocolVersion, ID: "recovery-consent-server", Name: "Recovery consent peer", Transport: mcp.TransportStreamableHTTP, Target: f.peer.URL,
		DeclaredCapabilities: []mcp.CapabilityKind{mcp.CapabilityTools}, Scope: mcp.ScopeRun, RunID: f.run.ID, WorkspaceID: workspace.ID, Source: mcp.Source{Kind: "manual", URI: "operator://recovery-acceptance"}, CallTimeoutMillis: 10000, MaxResultBytes: 8192}
	record, _, err := f.manager.Stage(t.Context(), descriptor)
	if err != nil {
		t.Fatal(err)
	}
	record, err = f.manager.Review(t.Context(), descriptor.ID, mcp.ReviewRequest{Action: mcp.ReviewApproveDiscovery, ExpectedDescriptorFingerprint: record.DescriptorFingerprint, ReviewedBy: "operator"})
	if err != nil {
		t.Fatal(err)
	}
	record, err = f.manager.Refresh(t.Context(), descriptor.ID)
	if err != nil {
		t.Fatal(err)
	}
	record, err = f.manager.Review(t.Context(), descriptor.ID, mcp.ReviewRequest{Action: mcp.ReviewEnableCapabilities, ExpectedDescriptorFingerprint: record.DescriptorFingerprint, ExpectedCapabilityFingerprint: record.Capabilities.Fingerprint, ReviewedBy: "operator"})
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < callCount; i++ {
		raw, err := json.Marshal(toolgateway.MCPToolCallPayload{Version: toolgateway.MCPClientToolProtocolVersion, ServerID: descriptor.ID, ToolName: "lookup", CapabilityFingerprint: record.ApprovedCapabilityFingerprint,
			Arguments: json.RawMessage(fmt.Sprintf(`{"query":"consent-%d","value":9007199254740993}`, i))})
		if err != nil {
			t.Fatal(err)
		}
		f.payloads = append(f.payloads, raw)
	}
	f.bindRuntime(t)
	if _, err = NewRunService(f.st).Start(t.Context(), f.run.ID); err != nil {
		t.Fatal(err)
	}
	f.runtime.Store(true)
	return f
}

func newMCPRecoveryAcceptanceFixture(t *testing.T, mode domain.RunExecutionPermissionMode, callCount int, blocked bool) *mcpRecoveryAcceptanceFixture {
	t.Helper()
	f := newMCPRecoveryAcceptanceRuntime(t, mode, callCount, blocked)
	result, err := f.supervisor.Step(t.Context(), f.run.ID)
	if err != nil || result.RunStatus != domain.RunWaitingApproval || f.requests.Load() != 0 {
		t.Fatalf("initial consent boundary: result=%+v requests=%d err=%v", result, f.requests.Load(), err)
	}
	var found bool
	f.pending, found, err = f.st.GetSupervisorCheckpoint(t.Context(), f.run.ID)
	if err != nil || !found || f.pending.Phase != domain.SupervisorTurnStarted {
		t.Fatalf("missing original pending turn: %+v %v", f.pending, err)
	}
	rounds, err := f.st.ListSupervisorToolRounds(t.Context(), f.pending)
	if err != nil || len(rounds) != 1 || len(rounds[0].Calls) != callCount {
		t.Fatalf("missing original durable intents: %+v %v", rounds, err)
	}
	f.calls = rounds[0].Calls
	for _, call := range f.calls {
		if call.Status != domain.SupervisorToolPending || call.Turn != f.pending.NextTurn || call.AttemptID != f.pending.AttemptID {
			t.Fatalf("unbound intent: %+v", call)
		}
	}
	f.assertApproval(t, 0, approval.StatusPending)
	return f
}

func (f *mcpRecoveryAcceptanceFixture) services(t *testing.T, st *store.SQLiteStore) (*AgentRunner, *ThreadTurnService, *ApprovalControlService) {
	t.Helper()
	manager, err := mcp.NewClientManager(st, &mcpRuntimeCredentialFixture{}, mcp.ManagerOptions{HTTPClient: f.peer.Client()})
	if err != nil {
		t.Fatal(err)
	}
	router := llm.NewRouter(llm.ModelRef{Provider: f.provider.Name(), Model: "fixture"})
	router.RegisterProvider(f.provider)
	checker := policy.NewDefaultChecker()
	deps := RunRuntimeDependencies{ExecutionCapabilities: f.caps, MCPClient: manager}
	supervisor := NewAgentRunnerWithRuntime(st, router, checker, deps)
	handoff := NewRunExecutionHandoffWithRuntime(st, router, checker, deps)
	turns := NewThreadTurnServiceWithExecutionCapabilities(st, NewRunLifecycleControlService(st), handoff, f.caps)
	return supervisor, turns, NewApprovalControlService(st, toolgateway.New(st, checker), checker)
}

func (f *mcpRecoveryAcceptanceFixture) bindRuntime(t *testing.T) {
	t.Helper()
	f.provider = &mcpRecoveryAcceptanceProvider{payloads: f.payloads}
	f.supervisor, f.turns, f.controller = f.services(t, f.st)
}

func (f *mcpRecoveryAcceptanceFixture) reopen(t *testing.T, cold bool) {
	t.Helper()
	oldStore, oldEpoch := f.st, f.caps.RuntimeAuthority.RuntimeEpoch()
	if err := oldStore.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := oldStore.GetRun(t.Context(), f.run.ID); err == nil {
		t.Fatal("old SQLite handle remained readable")
	}
	var err error
	f.st, err = store.Open(f.path)
	if err != nil {
		t.Fatal(err)
	}
	if cold {
		f.caps.RuntimeAuthority = domain.NewExecutionPermissionRuntimeAuthority()
	}
	f.bindRuntime(t)
	for _, before := range f.calls {
		after, started, err := f.st.GetSupervisorApprovalCall(t.Context(), f.run.ID, before.CallID)
		if err != nil || started || after.Status != domain.SupervisorToolPending || after.AuthorityJSON != before.AuthorityJSON || after.PayloadJSON != before.PayloadJSON {
			t.Fatalf("reopening changed pending consent: %+v started=%t err=%v", after, started, err)
		}
	}
	if (oldEpoch != f.caps.RuntimeAuthority.RuntimeEpoch()) != cold {
		t.Fatal("unexpected runtime epoch boundary")
	}
	t.Logf("real SQLite reopened: cold=%t original_turn=%d attempt=%s; persisted intents unchanged", cold, f.pending.NextTurn, f.pending.AttemptID)
}

func (f *mcpRecoveryAcceptanceFixture) assertApproval(t *testing.T, index int, status approval.Status) approval.Record {
	t.Helper()
	call := f.calls[index]
	r, err := f.st.GetApprovalByProposal(t.Context(), call.CallID)
	if err != nil || r.Status != status || r.RunID != f.run.ID || r.SessionID != f.run.SessionID || r.ProposalID != call.CallID || r.ToolName != mcp.OperationApprovalTool ||
		r.Mode != "per_call" || r.GrantID != "" || r.RequestFingerprint != mcp.OperationApprovalFingerprint(call) {
		t.Fatalf("consent is not bound to the exact stored operation: %+v err=%v", r, err)
	}
	return r
}

func (f *mcpRecoveryAcceptanceFixture) request(t *testing.T, index int, action ApprovalControlAction, key string) DecideApprovalControlRequest {
	t.Helper()
	record, err := f.st.GetApprovalByProposal(t.Context(), f.calls[index].CallID)
	if err != nil {
		t.Fatal(err)
	}
	return DecideApprovalControlRequest{Version: ApprovalControlProtocolVersion, RunID: f.run.ID, ApprovalID: record.ID, Action: action, OperationKey: key, ReviewedBy: "operator"}
}

func (f *mcpRecoveryAcceptanceFixture) decide(t *testing.T, request DecideApprovalControlRequest) DecideApprovalControlResult {
	t.Helper()
	result, err := f.controller.Decide(t.Context(), request)
	if err != nil {
		t.Fatalf("operator decision failed: %+v err=%v", request, err)
	}
	return result
}

func (f *mcpRecoveryAcceptanceFixture) resume(t *testing.T, index int) ApprovalContinuationResult {
	t.Helper()
	result := f.turns.ResumeApproval(t.Context(), ApprovalContinuationRequest{RunID: f.run.ID, Kind: "mcp", ProposalID: f.calls[index].CallID})
	t.Logf("public continuation: state=%s replayed=%t model=%t logical_tool=%t code=%s wire_calls=%d", result.State, result.Replayed, result.ModelCalled, result.ToolCalled, result.ErrorCode, f.wireCalls.Load())
	return result
}

func (f *mcpRecoveryAcceptanceFixture) assertCall(t *testing.T, index int, status domain.SupervisorToolCallStatus, code string, started bool) {
	t.Helper()
	before := f.calls[index]
	call, actualStarted, err := f.st.GetSupervisorApprovalCall(t.Context(), f.run.ID, before.CallID)
	if err != nil || actualStarted != started || call.Status != status || call.ErrorCode != code || call.Turn != f.pending.NextTurn || call.AttemptID != f.pending.AttemptID ||
		call.AuthorityJSON != before.AuthorityJSON || call.PayloadJSON != before.PayloadJSON {
		t.Fatalf("original intent outcome: %+v started=%t want=%t err=%v", call, actualStarted, started, err)
	}
	var envelope struct {
		Status, Code string
		Metadata     map[string]string
	}
	if err := json.Unmarshal([]byte(call.ResultJSON), &envelope); err != nil || envelope.Status != string(status) || envelope.Code != code {
		t.Fatalf("invalid terminal outcome %s err=%v", call.ResultJSON, err)
	}
	if !started && (envelope.Metadata["mcp_preflight"] != "not_dispatched" || envelope.Metadata["mcp_source_fingerprint"] != mcp.OperationApprovalFingerprint(before)) {
		t.Fatalf("missing explicit undispatched bound outcome: %s", call.ResultJSON)
	}
	t.Logf("original call settled: status=%s code=%s started=%t turn=%d", call.Status, call.ErrorCode, actualStarted, call.Turn)
}

func (f *mcpRecoveryAcceptanceFixture) assertTurnSettled(t *testing.T, wantCalls int32, wantResults int) {
	t.Helper()
	checkpoint, found, err := f.st.GetSupervisorCheckpoint(t.Context(), f.run.ID)
	if err != nil || !found || checkpoint.Phase != domain.SupervisorIdle || checkpoint.NextTurn != f.pending.NextTurn+1 || checkpoint.AttemptID != "" || checkpoint.HasPendingInput() {
		t.Fatalf("original turn did not settle exactly once: %+v err=%v", checkpoint, err)
	}
	run, err := f.st.GetRun(t.Context(), f.run.ID)
	if err != nil || run.Status != domain.RunRunning {
		t.Fatalf("Run stuck after consent: %+v err=%v", run, err)
	}
	if f.wireCalls.Load() != wantCalls || (wantCalls == 0 && f.requests.Load() != 0) {
		t.Fatalf("unexpected external effects: wire=%d requests=%d want=%d", f.wireCalls.Load(), f.requests.Load(), wantCalls)
	}
	f.provider.mu.Lock()
	results := append([]llm.ToolResult(nil), f.provider.results...)
	f.provider.mu.Unlock()
	if len(results) != wantResults {
		t.Fatalf("continuing model did not receive original outcomes: %+v", results)
	}
	expected := make(map[string]domain.SupervisorToolCall, len(f.calls))
	for _, original := range f.calls {
		call, _, err := f.st.GetSupervisorApprovalCall(t.Context(), f.run.ID, original.CallID)
		if err != nil {
			t.Fatal(err)
		}
		expected[original.CallID] = call
	}
	for _, result := range results {
		call, found := expected[result.ToolCallID]
		if !found || !json.Valid([]byte(result.Content)) || result.Content != call.ResultJSON ||
			result.IsError != (call.Status == domain.SupervisorToolDenied || call.Status == domain.SupervisorToolFailed) {
			t.Fatalf("model did not receive the exact bound durable outcome: %+v", result)
		}
		delete(expected, result.ToolCallID)
	}
	if len(expected) != 0 {
		t.Fatal("model continuation omitted an original operation outcome")
	}
	permission, err := f.st.GetRunExecutionPermission(t.Context(), f.run.ID)
	if err != nil || (permission.Mode != domain.RunExecutionPermissionAsk && permission.Mode != domain.RunExecutionPermissionAuto) || permission.ProcessEnabled || permission.ExecutionAuthorized || permission.CapabilityGrant {
		t.Fatalf("one-call consent became a broader permission: %+v err=%v", permission, err)
	}
	bound, err := mcp.DecodeSupervisorCallAuthority(json.RawMessage(f.calls[0].AuthorityJSON))
	if err != nil || permission.ID != bound.PermissionSnapshotID || permission.Revision != bound.PermissionRevision || permission.Mode != bound.PermissionMode {
		t.Fatalf("one-call consent changed the permission snapshot: %+v err=%v", permission, err)
	}
	t.Logf("same turn settled: original=%d next=%d model_results=%d wire_calls=%d", f.pending.NextTurn, checkpoint.NextTurn, len(results), f.wireCalls.Load())
}

func TestMCPApprovalRecoveryAcceptanceColdEpoch(t *testing.T) {
	for _, mode := range []domain.RunExecutionPermissionMode{domain.RunExecutionPermissionAsk, domain.RunExecutionPermissionAuto} {
		for _, when := range []string{"approved_before_restart", "approve_after_restart"} {
			t.Run(string(mode)+"/"+when, func(t *testing.T) {
				f := newMCPRecoveryAcceptanceFixture(t, mode, 1, false)
				req := f.request(t, 0, ApprovalControlApproveOnce, "cold-consent-approval-0001")
				if when == "approved_before_restart" {
					f.decide(t, req)
				}
				f.reopen(t, true)
				if when == "approve_after_restart" {
					f.decide(t, req)
				}
				f.assertApproval(t, 0, approval.StatusApproved)
				if result := f.resume(t, 0); result.State != "completed" {
					t.Fatalf("stale consent did not yield a recoverable outcome: %+v", result)
				}
				f.assertCall(t, 0, domain.SupervisorToolFailed, "mcp_authority_expired", false)
				f.assertTurnSettled(t, 0, 1)
				if replay := f.decide(t, req); !replay.Replayed {
					t.Fatal("same-key consent retry was not identified")
				}
				if replay := f.resume(t, 0); replay.State != "completed" || !replay.Replayed {
					t.Fatalf("terminal stale intent restarted: %+v", replay)
				}
				f.assertTurnSettled(t, 0, 1)
			})
		}
	}
}

func TestMCPApprovalRecoveryAcceptanceDecisionReplay(t *testing.T) {
	for _, mode := range []domain.RunExecutionPermissionMode{domain.RunExecutionPermissionAsk, domain.RunExecutionPermissionAuto} {
		t.Run(string(mode), func(t *testing.T) {
			f := newMCPRecoveryAcceptanceFixture(t, mode, 1, false)
			f.reopen(t, false) // durable DB restart while the host authority remains live
			req := f.request(t, 0, ApprovalControlApproveOnce, "repeat-consent-approval-0001")
			first := f.decide(t, req)
			if first.Replayed {
				t.Fatal("first decision reported replay")
			}
			for i := 0; i < 3; i++ {
				if i == 2 {
					req.OperationKey = "repeat-consent-approval-0002"
				}
				replay := f.decide(t, req)
				if !replay.Replayed || replay.Approval.Version != first.Approval.Version || !replay.Approval.UpdatedAt.Equal(first.Approval.UpdatedAt) {
					t.Fatalf("repeat changed consent: %+v", replay)
				}
			}
			if f.requests.Load() != 0 {
				t.Fatal("operator decision dispatched before continuation")
			}
			conflict := req
			conflict.Action = ApprovalControlDeny
			if _, err := f.controller.Decide(t.Context(), conflict); apperror.CodeOf(err) != apperror.CodeConflict {
				t.Fatalf("same decision key changed approved outcome: %v", err)
			}
			if result := f.resume(t, 0); result.State != "completed" {
				t.Fatalf("approved continuation failed: %+v", result)
			}
			f.assertCall(t, 0, domain.SupervisorToolCompleted, "", true)
			for i := 0; i < 2; i++ {
				if replay := f.decide(t, req); !replay.Replayed {
					t.Fatal("completed decision lost replay")
				}
				if replay := f.resume(t, 0); replay.State != "completed" || !replay.Replayed {
					t.Fatalf("completed call restarted: %+v", replay)
				}
			}
			f.assertTurnSettled(t, 1, 1)
			f.wireMu.Lock()
			args := append([]string(nil), f.wireArgs...)
			f.wireMu.Unlock()
			if len(args) != 1 || !strings.Contains(args[0], "consent-0") || !strings.Contains(args[0], "9007199254740993") {
				t.Fatalf("wire input differs from exact consent: %v", args)
			}
		})
	}
}

func TestMCPApprovalRecoveryAcceptanceDeniedTurn(t *testing.T) {
	for _, mode := range []domain.RunExecutionPermissionMode{domain.RunExecutionPermissionAsk, domain.RunExecutionPermissionAuto} {
		t.Run(string(mode), func(t *testing.T) {
			f := newMCPRecoveryAcceptanceFixture(t, mode, 1, false)
			f.reopen(t, false)
			req := f.request(t, 0, ApprovalControlDeny, "deny-consent-approval-0001")
			req.Reason = "operator declined this exact external operation"
			f.decide(t, req)
			if replay := f.decide(t, req); !replay.Replayed {
				t.Fatal("denial replay changed its identity")
			}
			record := f.assertApproval(t, 0, approval.StatusDenied)
			if record.DecisionReason != req.Reason {
				t.Fatal("durable operator reason was lost")
			}
			if result := f.resume(t, 0); result.State != "completed" {
				t.Fatalf("denied pending turn failed to settle: %+v", result)
			}
			f.assertCall(t, 0, domain.SupervisorToolDenied, "policy_denied", false)
			f.assertTurnSettled(t, 0, 1)
			if replay := f.resume(t, 0); replay.State != "completed" || !replay.Replayed {
				t.Fatalf("denied call restarted: %+v", replay)
			}
			f.assertTurnSettled(t, 0, 1)
		})
	}
}

func TestMCPApprovalRecoveryAcceptanceConsentIsolation(t *testing.T) {
	for _, mode := range []domain.RunExecutionPermissionMode{domain.RunExecutionPermissionAsk, domain.RunExecutionPermissionAuto} {
		t.Run(string(mode), func(t *testing.T) {
			f := newMCPRecoveryAcceptanceFixture(t, mode, 2, false)
			f.reopen(t, false)
			first := f.request(t, 0, ApprovalControlApproveOnce, "isolated-consent-approval-0001")
			f.decide(t, first)
			if result := f.resume(t, 0); result.State != "waiting_approval" {
				t.Fatalf("one consent authorized another intent: %+v", result)
			}
			f.assertCall(t, 0, domain.SupervisorToolCompleted, "", true)
			second := f.assertApproval(t, 1, approval.StatusPending)
			approved := f.assertApproval(t, 0, approval.StatusApproved)
			if approved.ID == second.ID || approved.RequestFingerprint == second.RequestFingerprint || f.wireCalls.Load() != 1 {
				t.Fatal("distinct intents shared consent or dispatch")
			}
			if _, err := f.controller.Decide(t.Context(), f.request(t, 1, ApprovalControlApproveOnce, first.OperationKey)); err == nil {
				t.Fatal("same key approved a different persisted operation")
			}
			f.assertApproval(t, 1, approval.StatusPending)
			if replay := f.resume(t, 0); replay.State != "completed" || !replay.Replayed || f.wireCalls.Load() != 1 {
				t.Fatalf("old review consumed the next pending operation: %+v", replay)
			}
			f.decide(t, f.request(t, 1, ApprovalControlDeny, "isolated-consent-denial-0002"))
			if result := f.resume(t, 1); result.State != "completed" {
				t.Fatalf("second denied intent blocked original turn: %+v", result)
			}
			f.assertCall(t, 1, domain.SupervisorToolDenied, "policy_denied", false)
			f.assertTurnSettled(t, 1, 2)
		})
	}
}

func TestMCPApprovalRecoveryAcceptanceConcurrentApproval(t *testing.T) {
	for _, mode := range []domain.RunExecutionPermissionMode{domain.RunExecutionPermissionAsk, domain.RunExecutionPermissionAuto} {
		for _, keys := range []string{"same_key", "distinct_keys"} {
			t.Run(string(mode)+"/"+keys, func(t *testing.T) {
				f := newMCPRecoveryAcceptanceFixture(t, mode, 1, true)
				f.reopen(t, false)
				secondStore, err := store.Open(f.path)
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { _ = secondStore.Close() })
				_, secondTurns, secondController := f.services(t, secondStore)
				request := f.request(t, 0, ApprovalControlApproveOnce, "concurrent-consent-0001")
				start := make(chan struct{})
				type decision struct {
					request DecideApprovalControlRequest
					result  DecideApprovalControlResult
					err     error
				}
				decisions := make(chan decision, 4)
				for i := 0; i < 4; i++ {
					req := request
					if keys == "distinct_keys" {
						req.OperationKey = fmt.Sprintf("concurrent-consent-%04d", i+1)
					}
					controller := f.controller
					if i%2 != 0 {
						controller = secondController
					}
					go func() { <-start; r, e := controller.Decide(t.Context(), req); decisions <- decision{req, r, e} }()
				}
				close(start)
				for i := 0; i < 4; i++ {
					select {
					case result := <-decisions:
						if result.err != nil {
							t.Errorf("concurrent decision: key=%s code=%s err=%v", result.request.OperationKey, apperror.CodeOf(result.err), result.err)
						}
						// A retry must be safe and usable even after contention.
						f.decide(t, result.request)
					case <-time.After(10 * time.Second):
						t.Fatal("concurrent decisions did not settle")
					}
				}
				f.assertApproval(t, 0, approval.StatusApproved)
				if f.requests.Load() != 0 {
					t.Fatal("approval itself dispatched externally")
				}
				resume := ApprovalContinuationRequest{RunID: f.run.ID, Kind: "mcp", ProposalID: f.calls[0].CallID}
				done := make(chan ApprovalContinuationResult, 1)
				go func() { done <- f.turns.ResumeApproval(t.Context(), resume) }()
				select {
				case <-f.entered:
				case <-time.After(10 * time.Second):
					t.Fatal("approved operation never reached TLS peer")
				}
				ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
				other := secondTurns.ResumeApproval(ctx, resume)
				cancel()
				if other.State != "failed" || other.ErrorCode != string(apperror.CodeConflict) {
					t.Errorf("contended independent owner did not report its live lease conflict: %+v", other)
				}
				if f.wireCalls.Load() != 1 {
					t.Error("concurrent owner repeated tools/call before response")
				}
				f.unblock()
				select {
				case result := <-done:
					if result.State != "completed" {
						t.Fatalf("winning continuation failed: %+v", result)
					}
				case <-time.After(10 * time.Second):
					t.Fatal("winning continuation did not settle")
				}
				if again := secondTurns.ResumeApproval(t.Context(), resume); again.State != "completed" || !again.Replayed {
					t.Fatalf("other owner could not replay completed outcome: %+v", again)
				}
				f.assertCall(t, 0, domain.SupervisorToolCompleted, "", true)
				f.assertTurnSettled(t, 1, 1)
				t.Log("four approval requests across two SQLite handles and two continuation owners: exactly one TLS tools/call")
			})
		}
	}
}
