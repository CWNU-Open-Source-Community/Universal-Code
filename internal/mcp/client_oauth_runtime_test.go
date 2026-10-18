package mcp

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"cyberagent-workbench/internal/credential"
	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/executionauth"
	"cyberagent-workbench/internal/mcpoauth"
	"cyberagent-workbench/internal/toolcontract"
)

func TestOAuthRuntimeRefreshFollowsCurrentAuthorityAndRedactsRotatedToken(t *testing.T) {
	for _, scenario := range []string{"refresh", "revoked_before_refresh", "server_rejected"} {
		t.Run(scenario, func(t *testing.T) {
			denied := scenario == "revoked_before_refresh"
			initial, rotated := strings.Repeat("oauth-initial-", 300), strings.Repeat("oauth-rotated-", 300)
			var refreshes, calls, checks atomic.Int32
			server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/token" {
					refreshes.Add(1)
					w.Header().Set("Content-Type", "application/json")
					json.NewEncoder(w).Encode(map[string]any{"access_token": rotated, "refresh_token": "oauth-rotated-refresh", "token_type": "Bearer", "expires_in": 3600})
					return
				}
				if r.Header.Get("X-Project") != "manual-config" {
					t.Error("manual HTTP configuration was lost")
				}
				var req Envelope
				if json.NewDecoder(r.Body).Decode(&req) != nil {
					w.WriteHeader(http.StatusBadRequest)
					return
				}
				if len(req.ID) == 0 {
					w.WriteHeader(http.StatusAccepted)
					return
				}
				var raw string
				switch req.Method {
				case "initialize":
					raw = `{"protocolVersion":"2025-06-18","capabilities":{"resources":{}},"serverInfo":{"name":"fixture","version":"1"}}`
				case "resources/list":
					raw = `{"resources":[{"uri":"fixture://readme","name":"Readme"}]}`
				case "resources/templates/list":
					raw = `{"resourceTemplates":[]}`
				case "resources/read":
					calls.Add(1)
					if r.Header.Get("Authorization") != "Bearer "+rotated {
						t.Error("call used the expired token")
					}
					if scenario == "server_rejected" {
						w.WriteHeader(http.StatusUnauthorized)
						return
					}
					raw = `{"contents":[{"uri":"fixture://readme","text":"` + rotated + `"}]}`
				default:
					t.Error("unexpected MCP method", req.Method)
					w.WriteHeader(http.StatusBadRequest)
					return
				}
				sdkWriteResponse(w, req.ID, raw)
			}))
			defer server.Close()
			owned := mcpoauth.Store{Dir: t.TempDir(), KeyStore: credential.NewMemoryStore()}
			st := &clientStoreFake{}
			manager, err := NewClientManager(st, owned, ManagerOptions{HTTPClient: server.Client()})
			if err != nil {
				t.Fatal(err)
			}
			d := ServerDescriptor{ProtocolVersion: ConfigClientProtocolVersion, ID: "oauth-runtime", Name: "OAuth runtime", Transport: TransportStreamableHTTP,
				Target: server.URL + "/mcp", CredentialRef: "oauth-runtime", Headers: map[string]string{"X-Project": "manual-config"},
				DeclaredCapabilities: []CapabilityKind{CapabilityResources}, Scope: ScopeRun, RunID: "run-1", WorkspaceID: "workspace-1",
				Source: Source{Kind: "manual", URI: "operator"}, CallTimeoutMillis: 5000, MaxResultBytes: 8192}
			storeToken := func(expiry time.Time) {
				raw, marshalErr := json.Marshal(map[string]any{"session": "fixture-login", "fingerprint": d.Fingerprint(), "target": d.Target, "resource": d.Target,
					"issuer": server.URL, "client_id": "public-client", "token_endpoint": server.URL + "/token",
					"token": map[string]any{"access_token": initial, "refresh_token": "oauth-initial-refresh", "token_type": "Bearer", "expiry": expiry}})
				if marshalErr != nil {
					t.Fatal(marshalErr)
				}
				if putErr := owned.Put(t.Context(), d.CredentialRef, "uc-mcp-oauth-v1:"+base64.RawURLEncoding.EncodeToString(raw)); putErr != nil {
					t.Fatal(putErr)
				}
			}
			storeToken(time.Now().Add(time.Hour))
			record, _, err := manager.Stage(t.Context(), d)
			if err != nil {
				t.Fatal(err)
			}
			record, err = manager.Review(t.Context(), d.ID, ReviewRequest{Action: ReviewApproveDiscovery, ExpectedDescriptorFingerprint: record.DescriptorFingerprint, ReviewedBy: "operator"})
			if err != nil {
				t.Fatal(err)
			}
			record, err = manager.Refresh(t.Context(), d.ID)
			if err != nil {
				t.Fatal(err)
			}
			record, err = manager.Review(t.Context(), d.ID, ReviewRequest{Action: ReviewEnableCapabilities, ExpectedDescriptorFingerprint: record.DescriptorFingerprint, ExpectedCapabilityFingerprint: record.Capabilities.Fingerprint, ReviewedBy: "operator"})
			if err != nil {
				t.Fatal(err)
			}
			authorizer := executionauth.NewPolicyAuthorizer(func(ctx context.Context, _ executionauth.SubjectRef, op toolcontract.Operation, _ string) (executionauth.OperationAuthority, error) {
				if op.Kind == toolcontract.OperationToolCall {
					if checks.Add(1) == 1 {
						storeToken(time.Now().Add(-time.Hour))
					} else if denied {
						return executionauth.OperationAuthority{}, errors.New("host authority revoked")
					}
				}
				return executionauth.OperationAuthority{Mode: domain.ExecutionApprovalFull, BindingFingerprint: strings.Repeat("a", 64), RuntimeAvailable: true, FullActivated: true}, ctx.Err()
			})
			result, err := manager.Invoke(t.Context(), InvokeRequest{RunID: "run-1", WorkspaceID: "workspace-1", ServerID: d.ID, Method: "resources/read", ToolName: "resources.read",
				Arguments: json.RawMessage(`{"uri":"fixture://readme"}`), CapabilityFingerprint: record.Capabilities.Fingerprint, OperationID: "oauth-resource", Subject: executionauth.SubjectRef{RunID: "run-1", ActorID: "agent-1"}, Authorizer: authorizer})
			if denied {
				receipt, found := InvocationReceipt(err)
				if err == nil || !found || receipt.State != toolcontract.ReceiptNotDispatched || calls.Load() != 0 || refreshes.Load() != 0 {
					t.Fatalf("revoked operation contacted peer/issuer: calls=%d refreshes=%d receipt=%+v err=%v", calls.Load(), refreshes.Load(), receipt, err)
				}
			} else if scenario == "server_rejected" {
				if err == nil || calls.Load() != 1 || refreshes.Load() != 1 {
					t.Fatal("server rejection retried OAuth or replayed the MCP operation")
				}
			} else if err != nil || calls.Load() != 1 || refreshes.Load() != 1 || strings.Contains(result.Content, rotated) || !strings.Contains(result.Content, "REDACTED") {
				t.Fatalf("refresh or redaction failed: calls=%d refreshes=%d err=%v", calls.Load(), refreshes.Load(), err)
			}
		})
	}
}
