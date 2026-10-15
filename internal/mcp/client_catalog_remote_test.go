package mcp

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"cyberagent-workbench/internal/apperror"
	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/executionauth"
	"cyberagent-workbench/internal/toolcontract"
	"github.com/gorilla/websocket"
	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestTaskCatalogRemoteTransportLifecycle(t *testing.T) {
	for _, kind := range []TransportKind{TransportSSE, TransportWebSocket} {
		t.Run(string(kind), func(t *testing.T) {
			var calls, initializes, streams, active atomic.Int32
			server := sdk.NewServer(&sdk.Implementation{Name: "remote-catalog", Version: "1"}, &sdk.ServerOptions{Capabilities: &sdk.ServerCapabilities{Tools: &sdk.ToolCapabilities{ListChanged: true}}})
			handler := func(context.Context, *sdk.CallToolRequest) (*sdk.CallToolResult, error) {
				calls.Add(1)
				return &sdk.CallToolResult{Content: []sdk.Content{&sdk.TextContent{Text: strings.Repeat("catalog-result ", 1000)}}}, nil
			}
			server.AddTool(&sdk.Tool{Name: "lookup", InputSchema: json.RawMessage(`{"type":"object"}`)}, handler)
			server.AddReceivingMiddleware(func(next sdk.MethodHandler) sdk.MethodHandler {
				return func(ctx context.Context, method string, request sdk.Request) (sdk.Result, error) {
					if method == "initialize" {
						initializes.Add(1)
					}
					return next(ctx, method, request)
				}
			})
			var wire http.Handler = sdk.NewSSEHandler(func(*http.Request) *sdk.Server { return server }, nil)
			if kind == TransportWebSocket {
				wire = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					conn, err := (&websocket.Upgrader{}).Upgrade(w, r, nil)
					if err != nil {
						return
					}
					defer conn.Close()
					session, err := server.Connect(r.Context(), acceptedWebSocketTransport{&webSocketClientConnection{connection: conn}}, nil)
					if err != nil {
						t.Error(err)
						return
					}
					_ = session.Wait()
				})
			}
			peer := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Header.Get("Authorization") != "Bearer catalog-token" || r.Header.Get("X-Project") != "catalog" {
					t.Error("connection lost bound credential/header")
					w.WriteHeader(401)
					return
				}
				if r.Method == http.MethodGet {
					streams.Add(1)
					active.Add(1)
					defer active.Add(-1)
				}
				// A legacy SSE peer may emit this header; it must not trigger the
				// Streamable HTTP adapter's additional standalone notification GET.
				w.Header().Set("Mcp-Session-Id", "legacy-peer-session")
				wire.ServeHTTP(w, r)
			}))
			defer peer.Close()
			endpoint := peer.URL
			if kind == TransportWebSocket {
				endpoint = "wss" + strings.TrimPrefix(endpoint, "https")
			}
			store := &clientStoreFake{}
			manager, err := NewClientManager(store, credentialReaderFake{value: "catalog-token"}, ManagerOptions{HTTPClient: peer.Client()})
			if err != nil {
				t.Fatal(err)
			}
			defer manager.Close()
			d := ServerDescriptor{ProtocolVersion: ConfigClientProtocolVersion, ID: "remote", Name: "Remote", Transport: kind, Target: endpoint, Headers: map[string]string{"X-Project": "catalog"}, CredentialRef: "remote-token", DeclaredCapabilities: []CapabilityKind{CapabilityTools}, Scope: ScopeRun, RunID: "run-1", WorkspaceID: "workspace-1", Source: Source{Kind: "manual", URI: "operator"}, CallTimeoutMillis: 3000, MaxResultBytes: 1024}
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
			caps, err := manager.RefreshCapabilities(t.Context(), "run-1", "workspace-1", func(ctx context.Context) error { return ctx.Err() }, nil)
			if err != nil || len(caps.Servers) != 1 {
				t.Fatalf("catalog: %+v %v", caps, err)
			}
			first := caps.Servers[0].CapabilityFingerprint
			connects, gets := initializes.Load(), streams.Load()
			request := InvokeRequest{RunID: "run-1", WorkspaceID: "workspace-1", ServerID: d.ID, ToolName: "lookup", Arguments: json.RawMessage(`{}`), CapabilityFingerprint: first, OperationID: "remote-call-1", Subject: executionauth.SubjectRef{RunID: "run-1", ActorID: "agent-1"}, Authorizer: executionauth.NewPolicyAuthorizer(func(ctx context.Context, _ executionauth.SubjectRef, _ toolcontract.Operation, _ string) (executionauth.OperationAuthority, error) {
				return executionauth.OperationAuthority{Mode: domain.ExecutionApprovalFull, BindingFingerprint: strings.Repeat("a", 64), RuntimeAvailable: true, FullActivated: true}, ctx.Err()
			})}
			for _, id := range []string{"remote-call-1", "remote-call-2"} {
				request.OperationID = id
				result, err := manager.Invoke(t.Context(), request)
				if err != nil || !result.Truncated || len(result.FullContent) <= len(result.Content) {
					t.Fatalf("reused call lost full result: %+v %v", result, err)
				}
			}
			if initializes.Load() != connects || streams.Load() != gets || calls.Load() != 2 {
				t.Fatal("tool calls reopened connection or replayed")
			}
			server.AddTool(&sdk.Tool{Name: "added", InputSchema: json.RawMessage(`{"type":"object"}`)}, handler)
			deadline := time.Now().Add(3 * time.Second)
			for time.Now().Before(deadline) {
				record, _ = store.GetMCPClientServer(t.Context(), d.ID)
				if len(record.Capabilities.Tools) == 2 {
					break
				}
				time.Sleep(10 * time.Millisecond)
			}
			if len(record.Capabilities.Tools) != 2 || record.State != TrustEnabled || record.Health != HealthHealthy {
				t.Fatalf("idle notification not published: %+v", record)
			}
			if _, err := manager.Invoke(t.Context(), request); apperror.CodeOf(err) != apperror.CodeConflict || calls.Load() != 2 {
				t.Fatalf("old catalog call was replayed: %v", err)
			}
			if initializes.Load() != connects || streams.Load() != gets {
				t.Fatal("notification reopened connection")
			}
			session, _ := manager.session(catalogSessionKey{"run-1", "workspace-1", d.ID})
			if _, err := manager.Review(t.Context(), d.ID, ReviewRequest{Action: ReviewDisable, ExpectedDescriptorFingerprint: d.Fingerprint(), ReviewedBy: "operator"}); err != nil {
				t.Fatal(err)
			}
			select {
			case <-session.done:
			default:
				t.Fatal("disable left Manager connection open")
			}
			deadline = time.Now().Add(3 * time.Second)
			for active.Load() != 0 && time.Now().Before(deadline) {
				time.Sleep(10 * time.Millisecond)
			}
			if active.Load() != 0 {
				t.Fatal("disable left remote stream open")
			}
			if _, err := manager.Invoke(t.Context(), request); err == nil || calls.Load() != 2 {
				t.Fatal("disabled call reached server")
			}
		})
	}
}
