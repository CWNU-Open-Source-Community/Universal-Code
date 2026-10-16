package mcp

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
	"time"

	"cyberagent-workbench/internal/apperror"
	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/executionauth"
	"cyberagent-workbench/internal/toolcontract"
	"github.com/modelcontextprotocol/go-sdk/jsonrpc"
	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestTaskCatalogSDKNotificationsAndStaleCalls(t *testing.T) {
	for _, version := range []string{legacyClientProtocolVersion, preferredClientProtocolVersion} {
		t.Run(version, func(t *testing.T) {
			var calls, connects atomic.Int32
			var fail atomic.Bool
			var streamMode atomic.Int32
			server := sdk.NewServer(&sdk.Implementation{Name: "catalog", Version: "1"}, &sdk.ServerOptions{Capabilities: &sdk.ServerCapabilities{Tools: &sdk.ToolCapabilities{ListChanged: true}, Resources: &sdk.ResourceCapabilities{ListChanged: true}, Prompts: &sdk.PromptCapabilities{ListChanged: true}}})
			handler := func(context.Context, *sdk.CallToolRequest) (*sdk.CallToolResult, error) {
				calls.Add(1)
				return &sdk.CallToolResult{Content: []sdk.Content{&sdk.TextContent{Text: "ok"}}}, nil
			}
			server.AddTool(&sdk.Tool{Name: "lookup", InputSchema: json.RawMessage(`{"type":"object"}`)}, handler)
			server.AddReceivingMiddleware(func(next sdk.MethodHandler) sdk.MethodHandler {
				return func(ctx context.Context, method string, request sdk.Request) (sdk.Result, error) {
					if method == "initialize" || method == "server/discover" {
						connects.Add(1)
					}
					if method == "tools/list" && fail.Load() {
						return nil, errors.New("fixture discovery failed")
					}
					result, err := next(ctx, method, request)
					if err != nil {
						t.Logf("fixture %s: %v", method, err)
					}
					return result, err
				}
			})
			sdkHandler := sdk.NewStreamableHTTPHandler(func(*http.Request) *sdk.Server { return server }, &sdk.StreamableHTTPOptions{Stateless: version == preferredClientProtocolVersion})
			peer := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method == http.MethodGet {
					switch streamMode.Load() {
					case 1:
						w.Header().Set("Location", "https://redirect.invalid/private")
						w.WriteHeader(http.StatusTemporaryRedirect)
						return
					case 2:
						w.Header().Set("Content-Type", "text/event-stream")
						w.WriteHeader(http.StatusOK)
						w.(http.Flusher).Flush()
						return
					}
				}
				sdkHandler.ServeHTTP(w, r)
			}))
			defer peer.Close()
			store := &clientStoreFake{}
			manager, err := NewClientManager(store, nil, ManagerOptions{HTTPClient: peer.Client()})
			if err != nil {
				t.Fatal(err)
			}
			defer manager.Close()
			d := ServerDescriptor{ProtocolVersion: ClientProtocolVersion, ID: "catalog", Name: "Catalog", Transport: TransportStreamableHTTP, Target: peer.URL,
				DeclaredCapabilities: []CapabilityKind{CapabilityTools, CapabilityResources, CapabilityPrompts}, Scope: ScopeRun, RunID: "run-1", WorkspaceID: "workspace-1",
				Source: Source{Kind: "manual", URI: "operator"}, CallTimeoutMillis: 5000, MaxResultBytes: 8192}
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
			// Select the reviewed protocol explicitly so both SDK notification lifecycles run.
			if record.Capabilities.ProtocolVersion != version {
				record.Capabilities.ProtocolVersion = version
				record.Capabilities.Fingerprint = capabilityFingerprint(record.Capabilities)
				before := record.Generation
				record.Generation++
				record, err = store.UpdateMCPClientServer(t.Context(), record, before)
				if err != nil {
					t.Fatal(err)
				}
			}
			record, err = manager.Review(t.Context(), d.ID, ReviewRequest{Action: ReviewEnableCapabilities, ExpectedDescriptorFingerprint: record.DescriptorFingerprint, ExpectedCapabilityFingerprint: record.Capabilities.Fingerprint, ReviewedBy: "operator"})
			if err != nil {
				t.Fatal(err)
			}
			revoked := make(chan struct{})
			var oldLeaseExpired atomic.Bool
			check := func(context.Context) error {
				if oldLeaseExpired.Load() {
					return errors.New("previous execution lease ended while awaiting approval")
				}
				select {
				case <-revoked:
					return errors.New("revoked")
				default:
					return nil
				}
			}
			caps, err := manager.RefreshCapabilities(t.Context(), "run-1", "workspace-1", check, revoked)
			if err != nil {
				t.Fatalf("refresh: %v cause: %v", err, errors.Unwrap(err))
			}
			old := caps.Servers[0].CapabilityFingerprint
			baseline := connects.Load()
			if _, err = manager.RefreshCapabilities(t.Context(), "run-1", "workspace-1", check, revoked); err != nil {
				t.Fatal(err)
			}
			if connects.Load() != baseline {
				t.Fatal("task catalog reconnected")
			}
			oldLeaseExpired.Store(true)
			var renewedChecks atomic.Int32
			check = func(context.Context) error {
				renewedChecks.Add(1)
				select {
				case <-revoked:
					return errors.New("revoked")
				default:
					return nil
				}
			}
			if _, err = manager.RefreshCapabilities(t.Context(), "run-1", "workspace-1", check, revoked); err != nil {
				t.Fatal(err)
			}
			if connects.Load() != baseline || renewedChecks.Load() == 0 {
				t.Fatal("approval lease handover lost the stateful peer or its current authority check")
			}
			// A new runtime fence must not inherit the old peer even when both
			// check closures still succeed (for example two live runtime instances).
			revoked = make(chan struct{})
			if _, err = manager.RefreshCapabilities(t.Context(), "run-1", "workspace-1", check, revoked); err != nil {
				t.Fatal(err)
			}
			if connects.Load() <= baseline {
				t.Fatal("a different runtime fence retained the previous connection")
			}
			baseline = connects.Load()
			wait := func(predicate func(ServerRecord) bool) ServerRecord {
				t.Helper()
				deadline := time.Now().Add(5 * time.Second)
				for time.Now().Before(deadline) {
					r, _ := store.GetMCPClientServer(t.Context(), d.ID)
					if predicate(r) {
						return r
					}
					time.Sleep(10 * time.Millisecond)
				}
				r, _ := store.GetMCPClientServer(t.Context(), d.ID)
				t.Fatalf("catalog notification not applied: %+v", r)
				return r
			}
			server.AddTool(&sdk.Tool{Name: "added", InputSchema: json.RawMessage(`{"type":"object"}`)}, handler)
			record = wait(func(r ServerRecord) bool { return len(r.Capabilities.Tools) == 2 })
			if record.State != TrustEnabled || record.ApprovedCapabilityFingerprint != record.Capabilities.Fingerprint {
				t.Fatal("notification disabled server")
			}
			request := InvokeRequest{RunID: "run-1", WorkspaceID: "workspace-1", ServerID: d.ID, ToolName: "lookup", Arguments: json.RawMessage(`{}`), CapabilityFingerprint: old, OperationID: "call-1",
				Subject: executionauth.SubjectRef{RunID: "run-1", ActorID: "agent-1"}, Authorizer: executionauth.NewPolicyAuthorizer(func(ctx context.Context, _ executionauth.SubjectRef, _ toolcontract.Operation, _ string) (executionauth.OperationAuthority, error) {
					return executionauth.OperationAuthority{Mode: domain.ExecutionApprovalFull, BindingFingerprint: strings.Repeat("a", 64), RuntimeAvailable: true, FullActivated: true}, ctx.Err()
				})}
			_, err = manager.Invoke(t.Context(), request)
			if apperror.CodeOf(err) != apperror.CodeConflict || calls.Load() != 0 {
				t.Fatalf("old call dispatched: %v calls=%d", err, calls.Load())
			}
			server.AddResource(&sdk.Resource{URI: "fixture://readme", Name: "Readme"}, func(context.Context, *sdk.ReadResourceRequest) (*sdk.ReadResourceResult, error) {
				return &sdk.ReadResourceResult{}, nil
			})
			record = wait(func(r ServerRecord) bool { return len(r.Capabilities.Resources) == 1 })
			server.AddPrompt(&sdk.Prompt{Name: "review"}, func(context.Context, *sdk.GetPromptRequest) (*sdk.GetPromptResult, error) {
				return &sdk.GetPromptResult{}, nil
			})
			record = wait(func(r ServerRecord) bool { return len(r.Capabilities.Prompts) == 1 })
			request.CapabilityFingerprint = record.Capabilities.Fingerprint
			if _, err = manager.Invoke(t.Context(), request); err != nil {
				t.Fatalf("new call: %v (%v)", err, errors.Unwrap(err))
			}
			if calls.Load() != 1 || connects.Load() != baseline {
				t.Fatalf("call reuse calls=%d connects=%d baseline=%d", calls.Load(), connects.Load(), baseline)
			}
			retained := record.Capabilities.Fingerprint
			fail.Store(true)
			server.AddTool(&sdk.Tool{Name: "failure", InputSchema: json.RawMessage(`{"type":"object"}`)}, handler)
			record = wait(func(r ServerRecord) bool { return r.Health == HealthUnavailable })
			if record.State != TrustEnabled || record.Capabilities.Fingerprint != retained || record.HealthMessage == "" {
				t.Fatal("refresh failure lost previous catalog/error")
			}
			fail.Store(false)
			if version == legacyClientProtocolVersion {
				streamMode.Store(1)
				if redirected, refreshErr := manager.RefreshCapabilities(t.Context(), "run-1", "workspace-1", check, revoked); refreshErr != nil || len(redirected.Servers) != 0 {
					t.Fatalf("redirect failure was not isolated: %+v %v", redirected, refreshErr)
				}
				before, _ := store.GetMCPClientServer(t.Context(), d.ID)
				streamMode.Store(2)
				_, _ = manager.RefreshCapabilities(t.Context(), "run-1", "workspace-1", check, revoked)
				wait(func(r ServerRecord) bool { return r.Health == HealthUnavailable && r.Generation > before.Generation })
				streamMode.Store(0)
			}
			if _, err = manager.RefreshCapabilities(t.Context(), "run-1", "workspace-1", check, revoked); err != nil {
				t.Fatalf("future catalog did not recover: %v", err)
			}
			session, _ := manager.session(catalogSessionKey{"run-1", "workspace-1", d.ID})
			close(revoked)
			select {
			case <-session.done:
			case <-time.After(3 * time.Second):
				t.Fatal("Run revocation did not close active connection")
			}
			fresh := make(chan struct{})
			if _, err = manager.RefreshCapabilities(t.Context(), "run-1", "workspace-1", func(context.Context) error { return nil }, fresh); err != nil {
				t.Fatal(err)
			}
			session, _ = manager.session(catalogSessionKey{"run-1", "workspace-1", d.ID})
			if _, err = manager.Review(t.Context(), d.ID, ReviewRequest{Action: ReviewDisable, ExpectedDescriptorFingerprint: d.Fingerprint(), ReviewedBy: "operator"}); err != nil {
				t.Fatal(err)
			}
			select {
			case <-session.done:
			default:
				t.Fatal("disable retained connection")
			}
			manager.CloseRun("run-1")
			if calls.Load() != 1 {
				t.Fatal("notification retried a tool call")
			}
		})
	}
}

func TestCatalogSubscriptionBindsOneListenAndCancellation(t *testing.T) {
	id, _ := jsonrpc.MakeID("catalog-stream")
	sends := 0
	bound := &catalogSubscription{discovery: &resolvedDiscovery{guard: func(_ context.Context, send toolcontract.DiscoverySend) error {
		if send.Method != "subscriptions/listen" || send.Bytes != 80 {
			t.Fatal("wrong subscription budget")
		}
		sends++
		return nil
	}}}
	listen := &jsonrpc.Request{ID: id, Method: "subscriptions/listen", Params: json.RawMessage(`{"notifications":{"toolsListChanged":true}}`)}
	if err := bound.listen(t.Context(), strings.Repeat("a", 64), listen, 80); err != nil {
		t.Fatal(err)
	}
	if bound.listen(t.Context(), strings.Repeat("a", 64), listen, 80) == nil || sends != 1 {
		t.Fatal("listen permit replayed")
	}
	cancel := &jsonrpc.Request{Method: "notifications/cancelled", Params: json.RawMessage(`{"requestId":"different"}`)}
	if bound.cancel(cancel) == nil {
		t.Fatal("unbound cancel accepted")
	}
	cancel.Params = json.RawMessage(`{"requestId":"catalog-stream"}`)
	if bound.cancel(cancel) != nil || bound.cancel(cancel) == nil {
		t.Fatal("cancel not exactly once")
	}
	extra := &catalogSubscription{discovery: &resolvedDiscovery{guard: func(context.Context, toolcontract.DiscoverySend) error {
		t.Fatal("invalid subscription reached guard")
		return nil
	}}}
	listen.Params = json.RawMessage(`{"notifications":{"resourceSubscriptions":["file:///private"]}}`)
	if extra.listen(t.Context(), strings.Repeat("a", 64), listen, 80) == nil {
		t.Fatal("resource subscription accepted")
	}
}

// A failed peer must not prevent another enabled server from being advertised.
type multiCatalogStore struct {
	clientStoreFake
	recordsMu  sync.Mutex
	records    map[string]ServerRecord
	failUpdate error
}

func (s *multiCatalogStore) GetMCPClientServer(_ context.Context, id string) (ServerRecord, error) {
	s.recordsMu.Lock()
	defer s.recordsMu.Unlock()
	return s.records[id], nil
}
func (s *multiCatalogStore) ListMCPClientServers(context.Context, string, string, int) ([]ServerRecord, error) {
	s.recordsMu.Lock()
	defer s.recordsMu.Unlock()
	return []ServerRecord{s.records["a"], s.records["b"]}, nil
}
func (s *multiCatalogStore) UpdateMCPClientServer(_ context.Context, r ServerRecord, expected int64) (ServerRecord, error) {
	s.recordsMu.Lock()
	defer s.recordsMu.Unlock()
	if s.failUpdate != nil {
		err := s.failUpdate
		s.failUpdate = nil
		return ServerRecord{}, err
	}
	if s.records[r.Descriptor.ID].Generation != expected {
		return ServerRecord{}, errors.New("generation conflict")
	}
	s.records[r.Descriptor.ID] = r
	return r, nil
}
func TestTaskCatalogIsolatesUnavailableServer(t *testing.T) {
	peer := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/a" {
			w.WriteHeader(503)
			return
		}
		var request Envelope
		if json.NewDecoder(r.Body).Decode(&request) != nil {
			w.WriteHeader(400)
			return
		}
		if len(request.ID) == 0 {
			w.WriteHeader(202)
			return
		}
		if request.Method == "initialize" {
			sdkWriteResponse(w, request.ID, `{"protocolVersion":"2025-06-18","capabilities":{"tools":{}},"serverInfo":{"name":"healthy","version":"1"}}`)
			return
		}
		sdkWriteResponse(w, request.ID, `{"tools":[{"name":"healthy","inputSchema":{"type":"object"}}]}`)
	}))
	defer peer.Close()
	snapshot, err := NewCapabilitySnapshot("old", "1", []CapabilityKind{CapabilityTools}, []RemoteTool{{Name: "old", InputSchema: json.RawMessage(`{"type":"object"}`)}}, nil, nil, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	st := &multiCatalogStore{records: map[string]ServerRecord{}}
	now := time.Now().UTC()
	for _, id := range []string{"a", "b"} {
		d := ServerDescriptor{ProtocolVersion: ClientProtocolVersion, ID: id, Name: id, Transport: TransportStreamableHTTP, Target: peer.URL + "/" + id, DeclaredCapabilities: []CapabilityKind{CapabilityTools}, Scope: ScopeRun, RunID: "run-1", WorkspaceID: "workspace-1", Source: Source{Kind: "manual", URI: "operator"}, CallTimeoutMillis: 1000, MaxResultBytes: 8192}
		st.records[id] = ServerRecord{ProtocolVersion: ServerRecordProtocolVersion, Descriptor: d, DescriptorFingerprint: d.Fingerprint(), State: TrustEnabled, Health: HealthHealthy, Generation: 1, Capabilities: snapshot, ApprovedCapabilityFingerprint: snapshot.Fingerprint, CreatedAt: now, UpdatedAt: now, ReviewedBy: "operator", ReviewedAt: &now}
	}
	manager, err := NewClientManager(st, nil, ManagerOptions{HTTPClient: peer.Client()})
	if err != nil {
		t.Fatal(err)
	}
	defer manager.Close()
	result, err := manager.RefreshCapabilities(t.Context(), "run-1", "workspace-1", func(context.Context) error { return nil }, nil)
	if err != nil || len(result.Servers) != 1 || result.Servers[0].ServerID != "b" || result.Servers[0].Tools[0].Name != "healthy" {
		t.Fatalf("healthy catalog blocked by failed peer: %+v %v", result, err)
	}
	failed, _ := st.GetMCPClientServer(t.Context(), "a")
	if failed.Health != HealthUnavailable || failed.HealthMessage == "" || failed.Capabilities.Fingerprint != snapshot.Fingerprint {
		t.Fatal("failed peer lost its error or previous catalog")
	}
	storeFailure := errors.New("catalog storage unavailable")
	st.recordsMu.Lock()
	st.failUpdate = storeFailure
	st.recordsMu.Unlock()
	if _, err = manager.RefreshCapabilities(t.Context(), "run-1", "workspace-1", func(context.Context) error { return nil }, nil); !errors.Is(err, storeFailure) {
		t.Fatalf("store failure was hidden as peer outage: %v", err)
	}
	if _, err = manager.RefreshCapabilities(t.Context(), "run-1", "workspace-1", func(context.Context) error { return errors.New("Run revoked") }, nil); err == nil {
		t.Fatal("task authority failure was hidden")
	}
}
