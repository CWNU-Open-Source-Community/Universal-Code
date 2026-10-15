package application

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/mcp"
	"cyberagent-workbench/internal/toolcontract"
	"cyberagent-workbench/internal/toolgateway"
)

type mcpRuntimeCredentialFixture struct {
	mu    sync.Mutex
	value string
	onGet func()
}

func (c *mcpRuntimeCredentialFixture) Get(context.Context, string) (string, bool, error) {
	if c.onGet != nil {
		c.onGet()
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.value, c.value != "", nil
}
func (c *mcpRuntimeCredentialFixture) replace(value string) {
	c.mu.Lock()
	c.value = value
	c.mu.Unlock()
}

func TestMCPRuntimeCommonAuthorityAtRealTLSSends(t *testing.T) {
	for _, scenario := range []string{"native_result", "modern_result", "revoke_before_connect", "revoke_discovery", "disable_discovery", "lease_discovery", "credential_drift", "lost_response", "revoke_after_send", "disable_after_send", "credential_after_send", "remote_error"} {
		t.Run(scenario, func(t *testing.T) {
			state, run, root, lease, capabilities := newMCPApprovalModeRuntime(t, t.Context())
			root = ensureCommandRuntimeTestAgent(t, t.Context(), state, lease, root)
			authority := domain.NewExecutionPermissionRuntimeAuthority()
			capabilities.RuntimeAuthority = authority
			permission, err := state.GetRunExecutionPermission(t.Context(), run.ID)
			if err != nil {
				t.Fatal(err)
			}
			grant, err := authority.ActivateRunFullAccess(permission)
			if err != nil {
				t.Fatal(err)
			}
			fence, err := authority.IssueRunAuthorizationFence(run.ID)
			if err != nil {
				t.Fatal(err)
			}
			credential := &mcpRuntimeCredentialFixture{value: "fixture-runtime-credential"}
			var runtimeCall atomic.Bool
			credential.onGet = func() {
				if runtimeCall.Load() && scenario == "revoke_before_connect" {
					authority.RevokeRun(run.ID)
				}
			}
			var starts, lists, calls atomic.Int32
			var manager *mcp.Manager
			var reviewed mcp.ServerRecord
			peer := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method == http.MethodDelete {
					w.WriteHeader(http.StatusNoContent)
					return
				}
				var request struct {
					JSONRPC string          `json:"jsonrpc"`
					ID      json.RawMessage `json:"id"`
					Method  string          `json:"method"`
					Params  json.RawMessage `json:"params"`
				}
				if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
					w.WriteHeader(400)
					return
				}
				if len(request.ID) == 0 {
					w.WriteHeader(202)
					return
				}
				var body json.RawMessage
				switch request.Method {
				case "server/discover":
					if scenario != "modern_result" {
						w.Header().Set("Content-Type", "application/json")
						_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": request.ID, "error": map[string]any{"code": -32601, "message": "legacy only"}})
						return
					}
					if runtimeCall.Load() {
						starts.Add(1)
					}
					body = json.RawMessage(`{"supportedVersions":["2026-07-28"],"capabilities":{"tools":{}},"_meta":{"io.modelcontextprotocol/serverInfo":{"name":"runtime-fixture","version":"1.0.0"}},"resultType":"complete","ttlMs":0,"cacheScope":"private"}`)
				case "initialize":
					if runtimeCall.Load() {
						starts.Add(1)
						var params struct{ ProtocolVersion string }
						if scenario == "modern_result" || json.Unmarshal(request.Params, &params) != nil || params.ProtocolVersion != "2024-11-05" {
							t.Error("runtime did not bind the reviewed protocol")
						}
					}
					body = json.RawMessage(`{"protocolVersion":"2024-11-05","capabilities":{"tools":{}},"serverInfo":{"name":"runtime-fixture","version":"1.0.0"}}`)
					if scenario == "modern_result" {
						// The compatibility review handshake learns this peer's
						// modern profile; runtime must use that exact reviewed value.
						body = json.RawMessage(strings.Replace(string(body), "2024-11-05", "2026-07-28", 1))
					}
				case "tools/list":
					body = json.RawMessage(`{"tools":[{"name":"lookup","description":"Fixture lookup","inputSchema":{"type":"object","additionalProperties":false,"properties":{"value":{"type":"integer"}}}}]}`)
					if runtimeCall.Load() {
						lists.Add(1)
						switch scenario {
						case "revoke_discovery":
							authority.RevokeRun(run.ID)
							body = json.RawMessage(`{"tools":[],"nextCursor":"must-not-send"}`)
						case "disable_discovery":
							if _, err := manager.Review(t.Context(), reviewed.Descriptor.ID, mcp.ReviewRequest{Action: mcp.ReviewDisable, ExpectedDescriptorFingerprint: reviewed.DescriptorFingerprint, ReviewedBy: "operator"}); err != nil {
								t.Error(err)
							}
						case "lease_discovery":
							if _, _, err := state.ReleaseRunExecutionLease(t.Context(), lease); err != nil {
								t.Error(err)
							}
						case "credential_drift":
							credential.replace("replacement-runtime-credential")
						}
					}
				case "tools/call":
					calls.Add(1)
					if !strings.Contains(string(request.Params), "9007199254740993") {
						t.Error("native input number lost precision")
					}
					if r.Header.Get("Authorization") != "Bearer fixture-runtime-credential" {
						t.Error("host credential not bound at actual send")
					}
					if scenario == "lost_response" {
						w.WriteHeader(500)
						return
					}
					if scenario == "revoke_after_send" {
						authority.RevokeRun(run.ID)
					}
					if scenario == "disable_after_send" {
						if _, err := manager.Review(t.Context(), reviewed.Descriptor.ID, mcp.ReviewRequest{Action: mcp.ReviewDisable, ExpectedDescriptorFingerprint: reviewed.DescriptorFingerprint, ReviewedBy: "operator"}); err != nil {
							t.Error(err)
						}
					}
					if scenario == "credential_after_send" {
						credential.replace("replacement-runtime-credential")
					}
					body = json.RawMessage(`{"content":[{"type":"text","text":"fixture-runtime-credential received"}],"structuredContent":{"value":9007199254740993},"_meta":{"vendor":{"number":9007199254740993}},"resultType":"complete"}`)
					if scenario == "remote_error" {
						body = json.RawMessage(`{"isError":true,"content":[{"type":"text","text":"remote failed after partial work"}]}`)
					}
				default:
					t.Errorf("unexpected MCP method %s", request.Method)
					w.WriteHeader(400)
					return
				}
				w.Header().Set("Content-Type", "application/json")
				_ = json.NewEncoder(w).Encode(struct {
					JSONRPC string          `json:"jsonrpc"`
					ID      json.RawMessage `json:"id"`
					Result  json.RawMessage `json:"result"`
				}{"2.0", request.ID, body})
			}))
			defer peer.Close()
			manager, err = mcp.NewClientManager(state, credential, mcp.ManagerOptions{HTTPClient: peer.Client()})
			if err != nil {
				t.Fatal(err)
			}
			descriptor := mcp.ServerDescriptor{ProtocolVersion: mcp.ClientProtocolVersion, ID: "runtime-docs", Name: "runtime-docs",
				Transport: mcp.TransportStreamableHTTP, Target: peer.URL, CredentialRef: "runtime-token", DeclaredCapabilities: []mcp.CapabilityKind{mcp.CapabilityTools},
				Scope: mcp.ScopeRun, RunID: run.ID, WorkspaceID: "workspace-command-runtime-app", Source: mcp.Source{Kind: "manual", URI: "operator://runtime-test"}, CallTimeoutMillis: 3000, MaxResultBytes: 8192}
			reviewed, _, err = manager.Stage(t.Context(), descriptor)
			if err != nil {
				t.Fatal(err)
			}
			reviewed, err = manager.Review(t.Context(), descriptor.ID, mcp.ReviewRequest{Action: mcp.ReviewApproveDiscovery, ExpectedDescriptorFingerprint: reviewed.DescriptorFingerprint, ReviewedBy: "operator"})
			if err != nil {
				t.Fatal(err)
			}
			reviewed, err = manager.Refresh(t.Context(), descriptor.ID)
			if err != nil {
				t.Fatal(err)
			}
			reviewed, err = manager.Review(t.Context(), descriptor.ID, mcp.ReviewRequest{Action: mcp.ReviewEnableCapabilities, ExpectedDescriptorFingerprint: reviewed.DescriptorFingerprint, ExpectedCapabilityFingerprint: reviewed.Capabilities.Fingerprint, ReviewedBy: "operator"})
			if err != nil {
				t.Fatal(err)
			}
			executor, err := NewMCPClientToolExecutor(manager, state, capabilities)
			if err != nil {
				t.Fatal(err)
			}
			scope := exactPermissionMCPScope(run, lease, permission)
			scope.AgentID, scope.AgentAttemptID = root.ID, root.ActiveAttemptID
			scope.PermissionGeneration, scope.RunAuthorizationFence, scope.PermissionRuntimeEpoch = grant.Generation, fence, authority.RuntimeEpoch()
			runtimeCall.Store(true)
			result, callErr := executor.ExecuteMCP(t.Context(), scope, toolgateway.MCPToolCallPayload{Version: toolgateway.MCPClientToolProtocolVersion,
				ServerID: descriptor.ID, ToolName: "lookup", CapabilityFingerprint: reviewed.ApprovedCapabilityFingerprint, Arguments: json.RawMessage(`{"value":9007199254740993}`)})
			if scenario == "native_result" || scenario == "modern_result" {
				if callErr != nil || calls.Load() != 1 || result.IsError || result.Truncated || !json.Valid([]byte(result.Content)) ||
					!strings.Contains(result.Content, `"_meta"`) || strings.Count(result.Content, "9007199254740993") != 2 ||
					strings.Contains(result.Content, "fixture-runtime-credential") || result.Metadata["execution_receipt"] != "result_received" {
					t.Fatalf("native result not retained/redacted: %+v calls=%d err=%v cause=%v", result, calls.Load(), callErr, errors.Unwrap(callErr))
				}
			} else if scenario == "remote_error" {
				if callErr != nil || calls.Load() != 1 || !result.IsError || result.Metadata["execution_receipt"] != "result_received" {
					t.Fatalf("received remote error was not retained: %+v err=%v", result, callErr)
				}
			} else {
				receipt, found := mcp.InvocationReceipt(callErr)
				wantState, wantCalls := toolcontract.ReceiptNotDispatched, int32(0)
				if scenario == "lost_response" || scenario == "disable_after_send" {
					wantState, wantCalls = toolcontract.ReceiptOutcomeUnknown, 1
				}
				if scenario == "revoke_after_send" || scenario == "credential_after_send" {
					wantState, wantCalls = toolcontract.ReceiptResultReceived, 1
				}
				if callErr == nil || !found || receipt.State != wantState || calls.Load() != wantCalls || result.Content != "" {
					t.Fatalf("dispatch/revocation receipt: %+v calls=%d want=%d err=%v", receipt, calls.Load(), wantCalls, callErr)
				}
				if scenario == "revoke_discovery" && lists.Load() != 1 {
					t.Fatal("revocation did not stop the next discovery page", lists.Load())
				}
				if scenario == "revoke_before_connect" && (starts.Load() != 0 || lists.Load() != 0) {
					t.Fatal("revoked authority reached the peer", starts.Load(), lists.Load())
				}
				if scenario == "disable_discovery" || scenario == "disable_after_send" {
					current, err := state.GetMCPClientServer(t.Context(), descriptor.ID)
					if err != nil || current.State != mcp.TrustDisabled {
						t.Fatal("runtime error overwrote operator disable", current.State, err)
					}
				}
				if scenario == "revoke_discovery" || scenario == "lease_discovery" || scenario == "credential_drift" {
					current, err := state.GetMCPClientServer(t.Context(), descriptor.ID)
					if err != nil || current.State != mcp.TrustEnabled || current.Health != mcp.HealthHealthy || current.Generation != reviewed.Generation {
						t.Fatal("Run-local authority loss changed the server review", current.State, current.Health, err)
					}
				}
			}
			audits, err := state.ListMCPClientCalls(t.Context(), run.ID, 10)
			if err != nil || len(audits) != 1 {
				t.Fatal("missing existing MCP audit", len(audits), err)
			}
			if scenario == "lost_response" && audits[0].ErrorCode != "outcome_unknown" {
				t.Fatal("audit lost uncertain dispatch", audits[0])
			}
			if scenario == "remote_error" && (audits[0].Status != "failed" || audits[0].ErrorCode != "remote_tool_error") {
				t.Fatal("received remote error became audit success", audits[0])
			}
		})
	}
}
