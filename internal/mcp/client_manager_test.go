package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"cyberagent-workbench/internal/apperror"
)

type clientStoreFake struct {
	mu      sync.Mutex
	record  ServerRecord
	audits  []CallAudit
	created bool
}

func (s *clientStoreFake) CreateMCPClientServer(_ context.Context, record ServerRecord) (ServerRecord, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.created {
		return s.record, true, nil
	}
	s.created, s.record = true, record
	return record, false, nil
}

func (s *clientStoreFake) GetMCPClientServer(_ context.Context, id string) (ServerRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.created || s.record.Descriptor.ID != id {
		return ServerRecord{}, errors.New("not found")
	}
	return s.record, nil
}

func (s *clientStoreFake) ListMCPClientServers(_ context.Context, runID, workspaceID string,
	_ int,
) ([]ServerRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.created || !scopeMatches(s.record.Descriptor, runID, workspaceID) {
		return nil, nil
	}
	return []ServerRecord{s.record}, nil
}

func (s *clientStoreFake) ListRecoverableMCPClientServers(_ context.Context,
	_ int,
) ([]ServerRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.created && s.record.Health == HealthConnecting {
		return []ServerRecord{s.record}, nil
	}
	return []ServerRecord{}, nil
}

func (s *clientStoreFake) UpdateMCPClientServer(_ context.Context, record ServerRecord,
	expected int64,
) (ServerRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.created || s.record.Generation != expected || record.Generation != expected+1 {
		return ServerRecord{}, errors.New("generation conflict")
	}
	s.record = record
	return record, nil
}

func (s *clientStoreFake) RecordMCPClientCall(_ context.Context, audit CallAudit) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.audits = append(s.audits, audit)
	return nil
}

func (s *clientStoreFake) ListMCPClientCalls(_ context.Context, _ string, _ int) ([]CallAudit, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]CallAudit(nil), s.audits...), nil
}

type clientTransportScript struct {
	tools       func() []RemoteTool
	onToolsList func()
	onToolCall  func()
	failMethod  string
}

// The Manager fixture uses the same SDK session and real HTTP exchange as
// production; no fake Client/Envelope dispatcher remains.
func (script *clientTransportScript) client(t *testing.T) *Client {
	t.Helper()
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		var request Envelope
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Error(err)
			w.WriteHeader(400)
			return
		}
		if request.Method == "notifications/initialized" {
			w.WriteHeader(http.StatusAccepted)
			return
		}
		if request.Method == script.failMethod {
			http.Error(w, "fixture transport disconnected", http.StatusInternalServerError)
			return
		}
		var result any
		switch request.Method {
		case "initialize":
			result = map[string]any{"protocolVersion": ProtocolVersion,
				"capabilities": map[string]any{"tools": map[string]any{}},
				"serverInfo":   map[string]string{"name": "fixture", "version": "1.0.0"}}
		case "tools/list":
			if script.onToolsList != nil {
				script.onToolsList()
			}
			items := make([]map[string]any, 0)
			for _, tool := range script.tools() {
				items = append(items, map[string]any{"name": tool.Name,
					"description": tool.Description, "inputSchema": tool.InputSchema})
			}
			result = map[string]any{"tools": items}
		case "tools/call":
			if script.onToolCall != nil {
				script.onToolCall()
			}
			result = map[string]any{"content": []map[string]string{{
				"type": "text", "text": "AKIAABCDEFGHIJKLMNOP credential-value"}}}
		default:
			http.Error(w, "unexpected method "+request.Method, http.StatusBadRequest)
			return
		}

		raw, err := json.Marshal(result)
		if err != nil {
			t.Error(err)
			w.WriteHeader(500)
			return
		}
		sdkWriteResponse(w, request.ID, string(raw))
	}))
	t.Cleanup(server.Close)
	client, err := newRemoteClientTransport(clientTransportDescriptor(TransportStreamableHTTP, server.URL, nil), "", server.Client())
	if err != nil {
		t.Fatal(err)
	}
	if script.failMethod == "tools/call" {
		// The original fake disconnected before dispatch. Keep that failure
		// phase; post-dispatch uncertainty has separate real transport tests.
		client.beforeCall = func(context.Context) error { return errors.New("fixture transport disconnected") }
	}
	t.Cleanup(func() { _ = client.Close() })
	return client
}

type credentialReaderFake struct{ value string }

func (c credentialReaderFake) Get(context.Context, string) (string, bool, error) {
	return c.value, c.value != "", nil
}

func TestServerDescriptorRejectsPersistedStdioSecrets(t *testing.T) {
	descriptor := ServerDescriptor{ProtocolVersion: ClientProtocolVersion,
		ID: "stdio-fixture", Name: "stdio-fixture", Transport: TransportStdio,
		Target: filepath.Join(t.TempDir(), "fixture"), Arguments: []string{"--api-key=plaintext"},
		DeclaredCapabilities: []CapabilityKind{CapabilityTools}, Scope: ScopeWorkspace,
		WorkspaceID: "workspace-1", Source: Source{Kind: "manual", URI: "operator"},
		CallTimeoutMillis: 1_000, MaxResultBytes: 4_096}
	if err := descriptor.Validate(); err == nil {
		t.Fatal("stdio descriptor persisted a secret-bearing argument")
	}
	descriptor.Arguments = []string{"--token-budget=1000", "--config", "safe.json"}
	if err := descriptor.Validate(); err != nil {
		t.Fatalf("safe stdio arguments were rejected: %v", err)
	}
}

func TestClientManagerRequiresInitialReviewsAndUpdatesEnabledCatalog(t *testing.T) {
	store := &clientStoreFake{}
	toolSet := []RemoteTool{{Name: "lookup", Description: "Look up credential-value item.",
		InputSchema: json.RawMessage(`{"type":"object","description":"credential-value","additionalProperties":false,"required":["query"],"properties":{"query":{"type":"string"}}}`)}}
	bearerSeen := ""
	manager, err := NewClientManager(store, credentialReaderFake{value: "credential-value"},
		ManagerOptions{Now: func() time.Time { return time.Date(2026, 8, 20, 1, 2, 3, 0, time.UTC) },
			TransportFactory: func(_ context.Context, _ ServerDescriptor, bearer string) (clientTransport, error) {
				bearerSeen = bearer
				return (&clientTransportScript{tools: func() []RemoteTool {
					return append([]RemoteTool(nil), toolSet...)
				}}).client(t), nil
			}})
	if err != nil {
		t.Fatal(err)
	}
	descriptor := ServerDescriptor{ProtocolVersion: ClientProtocolVersion,
		ID: "docs", Name: "Documentation", Transport: TransportStreamableHTTP,
		Target: "https://mcp.example.invalid/rpc", CredentialRef: "docs-token",
		DeclaredCapabilities: []CapabilityKind{CapabilityTools}, Scope: ScopeRun,
		RunID: "run-1", WorkspaceID: "workspace-1",
		Source:            Source{Kind: "manual", URI: "operator://fixture"},
		CallTimeoutMillis: 1_000, MaxResultBytes: 8_192}
	record, replayed, err := manager.Stage(t.Context(), descriptor)
	if err != nil || replayed || record.State != TrustStaged {
		t.Fatalf("stage=%#v replayed=%t err=%v", record, replayed, err)
	}
	if _, err := manager.Review(t.Context(), descriptor.ID, ReviewRequest{
		Action: ReviewEnableCapabilities, ExpectedDescriptorFingerprint: record.DescriptorFingerprint,
		ReviewedBy: "operator"}); err == nil {
		t.Fatal("capabilities were enabled before discovery review")
	}
	record, err = manager.Review(t.Context(), descriptor.ID, ReviewRequest{
		Action: ReviewApproveDiscovery, ExpectedDescriptorFingerprint: record.DescriptorFingerprint,
		ReviewedBy: "operator"})
	if err != nil {
		t.Fatal(err)
	}
	record, err = manager.Refresh(t.Context(), descriptor.ID)
	if err != nil || record.State != TrustCapabilitiesPending ||
		record.Capabilities.Fingerprint == "" || bearerSeen != "credential-value" ||
		strings.Contains(record.Capabilities.Tools[0].Description, "credential-value") ||
		bytes.Contains(record.Capabilities.Tools[0].InputSchema, []byte("credential-value")) {
		t.Fatalf("discovery=%#v bearer=%q err=%v", record, bearerSeen, err)
	}
	record, err = manager.Review(t.Context(), descriptor.ID, ReviewRequest{
		Action: ReviewEnableCapabilities, ExpectedDescriptorFingerprint: record.DescriptorFingerprint,
		ExpectedCapabilityFingerprint: record.Capabilities.Fingerprint, ReviewedBy: "operator"})
	if err != nil || record.State != TrustEnabled {
		t.Fatalf("enable=%#v err=%v", record, err)
	}
	capabilities, err := manager.Capabilities(t.Context(), "run-1", "workspace-1")
	if err != nil || len(capabilities.Servers) != 1 || len(capabilities.Servers[0].Tools) != 1 {
		t.Fatalf("capabilities=%#v err=%v", capabilities, err)
	}
	result, err := manager.Invoke(t.Context(), InvokeRequest{RunID: "run-1",
		WorkspaceID: "workspace-1", ServerID: "docs", ToolName: "lookup",
		CapabilityFingerprint: record.Capabilities.Fingerprint,
		Arguments:             json.RawMessage(`{"query":"bounded"}`)})
	if err != nil || strings.Contains(result.Content, "AKIAABCDEFGHIJKLMNOP") ||
		strings.Contains(result.Content, "credential-value") ||
		!strings.Contains(result.Content, "[REDACTED:aws-access-key]") ||
		!strings.Contains(result.Content, "[REDACTED:credential]") {
		t.Fatalf("invoke=%#v err=%v", result, err)
	}
	if len(store.audits) != 1 || store.audits[0].Status != "completed" ||
		store.audits[0].ArgumentsSHA256 == "" {
		t.Fatalf("metadata-only call audit missing: %#v", store.audits)
	}
	toolSet = []RemoteTool{{Name: "lookup", Description: "Changed capability.",
		InputSchema: json.RawMessage(`{"type":"object"}`)}}
	_, err = manager.Invoke(t.Context(), InvokeRequest{RunID: "run-1",
		WorkspaceID: "workspace-1", ServerID: "docs", ToolName: "lookup",
		CapabilityFingerprint: record.Capabilities.Fingerprint,
		Arguments:             json.RawMessage(`{"query":"bounded"}`)})
	if err == nil || apperror.CodeOf(apperror.Normalize(err)) != apperror.CodeConflict {
		t.Fatalf("capability drift was not rejected: %v", err)
	}
	quarantined, _ := store.GetMCPClientServer(t.Context(), "docs")
	if quarantined.State != TrustEnabled || quarantined.Health != HealthHealthy || quarantined.ApprovedCapabilityFingerprint != quarantined.Capabilities.Fingerprint {
		t.Fatalf("drift did not atomically update the enabled server: %#v", quarantined)
	}
}

func TestClientManagerValidatesArgumentsBeforeOpeningTransport(t *testing.T) {
	// Argument schema validation is also covered through the end-to-end manager
	// flow above; keep this assertion explicit because transport-before-validation
	// could otherwise disclose malformed or over-broad input to an external server.
	schema := json.RawMessage(`{"type":"object","additionalProperties":false,"required":["count"],"properties":{"count":{"type":"integer","minimum":1}}}`)
	if err := validateArgumentsAgainstSchema(schema, json.RawMessage(`{"count":2}`)); err != nil {
		t.Fatal(err)
	}
	if err := validateArgumentsAgainstSchema(schema, json.RawMessage(`{"count":0,"secret":"x"}`)); err == nil {
		t.Fatal("invalid MCP arguments passed approved JSON Schema validation")
	}
	localReference := json.RawMessage(`{"type":"object","properties":{"query":{"$ref":"#/$defs/query"}},"$defs":{"query":{"type":"string"}}}`)
	if err := (RemoteTool{Name: "local-ref", InputSchema: localReference}).Validate(); err != nil {
		t.Fatalf("document-local MCP schema reference was rejected: %v", err)
	}
	if err := validateArgumentsAgainstSchema(localReference,
		json.RawMessage(`{"query":"bounded"}`)); err != nil {
		t.Fatalf("document-local MCP schema reference did not validate: %v", err)
	}
	external := json.RawMessage(`{"$ref":"file:///host-secret.json"}`)
	if err := (RemoteTool{Name: "external-ref", InputSchema: external}).Validate(); err == nil ||
		!strings.Contains(err.Error(), "external MCP schema resources are forbidden") {
		t.Fatalf("external MCP schema resource passed discovery validation: %v", err)
	}
	if err := validateArgumentsAgainstSchema(external, json.RawMessage(`{}`)); err == nil ||
		!strings.Contains(err.Error(), "external MCP schema resources are forbidden") {
		t.Fatalf("external MCP schema resource was not rejected: %v", err)
	}
}

func TestClientBoundsPreserveUTF8(t *testing.T) {
	value := strings.Repeat("界", 800)
	bounded := boundedClientMessage(value)
	if len([]byte(bounded)) > 2048 || !json.Valid([]byte(`{"value":"`+bounded+`"}`)) {
		t.Fatalf("bounded client message is not valid UTF-8: bytes=%d", len([]byte(bounded)))
	}
	if truncated := truncateClientUTF8("abc界def", 5); truncated != "abc" {
		t.Fatalf("UTF-8 truncation split a rune: %q", truncated)
	}
}

func TestClientManagerRechecksRevocationImmediatelyBeforeToolCall(t *testing.T) {
	now := time.Date(2026, 8, 20, 1, 2, 3, 0, time.UTC)
	tool := RemoteTool{Name: "lookup", InputSchema: json.RawMessage(`{"type":"object"}`)}
	descriptor := ServerDescriptor{ProtocolVersion: ClientProtocolVersion,
		ID: "revoked-server", Name: "Revoked Server", Transport: TransportStreamableHTTP,
		Target: "https://mcp.example.invalid/rpc", DeclaredCapabilities: []CapabilityKind{CapabilityTools},
		Scope: ScopeRun, RunID: "run-1", WorkspaceID: "workspace-1",
		Source: Source{Kind: "manual", URI: "operator"}, CallTimeoutMillis: 1_000,
		MaxResultBytes: 8_192}
	snapshot, err := NewCapabilitySnapshot("fixture", "1.0.0",
		[]CapabilityKind{CapabilityTools}, []RemoteTool{tool}, nil, nil, now)
	if err != nil {
		t.Fatal(err)
	}
	reviewed := now
	record := ServerRecord{ProtocolVersion: ServerRecordProtocolVersion,
		Descriptor: descriptor, DescriptorFingerprint: descriptor.Fingerprint(), State: TrustEnabled,
		Capabilities: snapshot, ApprovedCapabilityFingerprint: snapshot.Fingerprint,
		Health: HealthHealthy, Generation: 1, ReviewedBy: "operator", ReviewedAt: &reviewed,
		CreatedAt: now.Add(-time.Minute), UpdatedAt: now}
	if err := record.Validate(); err != nil {
		t.Fatal(err)
	}
	store := &clientStoreFake{record: record, created: true}
	toolCalls := 0
	manager, err := NewClientManager(store, credentialReaderFake{}, ManagerOptions{
		Now: func() time.Time { return now.Add(time.Second) },
		TransportFactory: func(context.Context, ServerDescriptor, string) (clientTransport, error) {
			return (&clientTransportScript{tools: func() []RemoteTool { return []RemoteTool{tool} },
				onToolsList: func() {
					store.mu.Lock()
					defer store.mu.Unlock()
					store.record.State = TrustDisabled
					store.record.Generation++
					store.record.UpdatedAt = now.Add(time.Second)
				}, onToolCall: func() { toolCalls++ }}).client(t), nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = manager.Invoke(t.Context(), InvokeRequest{RunID: "run-1", WorkspaceID: "workspace-1",
		ServerID: descriptor.ID, ToolName: tool.Name, CapabilityFingerprint: snapshot.Fingerprint,
		Arguments: json.RawMessage(`{}`)})
	if err == nil || apperror.CodeOf(apperror.Normalize(err)) != apperror.CodePolicyDenied ||
		toolCalls != 0 {
		t.Fatalf("revocation race reached remote tool call: calls=%d err=%v", toolCalls, err)
	}
}

func TestClientManagerRemovesDisconnectedServerFromHealthyCapabilities(t *testing.T) {
	now := time.Date(2026, 8, 20, 1, 2, 3, 0, time.UTC)
	tool := RemoteTool{Name: "lookup", InputSchema: json.RawMessage(`{"type":"object"}`)}
	descriptor := ServerDescriptor{ProtocolVersion: ClientProtocolVersion,
		ID: "disconnect-server", Name: "Disconnect Server", Transport: TransportStreamableHTTP,
		Target: "https://mcp.example.invalid/rpc", DeclaredCapabilities: []CapabilityKind{CapabilityTools},
		Scope: ScopeRun, RunID: "run-1", WorkspaceID: "workspace-1",
		Source: Source{Kind: "manual", URI: "operator"}, CallTimeoutMillis: 1_000,
		MaxResultBytes: 8_192}
	snapshot, err := NewCapabilitySnapshot("fixture", "1.0.0",
		[]CapabilityKind{CapabilityTools}, []RemoteTool{tool}, nil, nil, now)
	if err != nil {
		t.Fatal(err)
	}
	reviewed := now
	record := ServerRecord{ProtocolVersion: ServerRecordProtocolVersion,
		Descriptor: descriptor, DescriptorFingerprint: descriptor.Fingerprint(), State: TrustEnabled,
		Capabilities: snapshot, ApprovedCapabilityFingerprint: snapshot.Fingerprint,
		Health: HealthHealthy, Generation: 1, ReviewedBy: "operator", ReviewedAt: &reviewed,
		CreatedAt: now.Add(-time.Minute), UpdatedAt: now}
	store := &clientStoreFake{record: record, created: true}
	manager, err := NewClientManager(store, credentialReaderFake{}, ManagerOptions{
		Now: func() time.Time { return now.Add(time.Second) },
		TransportFactory: func(context.Context, ServerDescriptor, string) (clientTransport, error) {
			return (&clientTransportScript{tools: func() []RemoteTool { return []RemoteTool{tool} },
				failMethod: "tools/call"}).client(t), nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = manager.Invoke(t.Context(), InvokeRequest{RunID: "run-1", WorkspaceID: "workspace-1",
		ServerID: descriptor.ID, ToolName: tool.Name, CapabilityFingerprint: snapshot.Fingerprint,
		Arguments: json.RawMessage(`{}`)})
	if err == nil || !strings.Contains(err.Error(), "disconnected") {
		t.Fatalf("disconnected invocation error=%v", err)
	}
	stored, err := store.GetMCPClientServer(t.Context(), descriptor.ID)
	if err != nil || stored.State != TrustEnabled || stored.Health != HealthUnavailable ||
		stored.Generation != 2 || stored.ApprovedCapabilityFingerprint != snapshot.Fingerprint {
		t.Fatalf("disconnected server health=%#v err=%v", stored, err)
	}
	capabilities, err := manager.Capabilities(t.Context(), "run-1", "workspace-1")
	if err != nil || len(capabilities.Servers) != 0 {
		t.Fatalf("unavailable server remained model-visible: %#v err=%v", capabilities, err)
	}
}

func TestClientManagerStartupRecoveryPreservesInterruptedEnabledCatalog(t *testing.T) {
	now := time.Date(2026, 8, 20, 1, 2, 3, 0, time.UTC)
	descriptor := ServerDescriptor{ProtocolVersion: ClientProtocolVersion,
		ID: "restart-server", Name: "Restart Server", Transport: TransportStreamableHTTP,
		Target:               "https://mcp.example.invalid/rpc",
		DeclaredCapabilities: []CapabilityKind{CapabilityTools}, Scope: ScopeWorkspace,
		WorkspaceID: "workspace-1", Source: Source{Kind: "manual", URI: "operator"},
		CallTimeoutMillis: 1_000, MaxResultBytes: 8_192}
	snapshot, err := NewCapabilitySnapshot("restart-server", "1.0.0",
		[]CapabilityKind{CapabilityTools}, []RemoteTool{{Name: "lookup",
			InputSchema: json.RawMessage(`{"type":"object"}`)}}, nil, nil, now)
	if err != nil {
		t.Fatal(err)
	}
	reviewed := now
	leaseExpiresAt := now.Add(500 * time.Millisecond)
	record := ServerRecord{ProtocolVersion: ServerRecordProtocolVersion,
		Descriptor: descriptor, DescriptorFingerprint: descriptor.Fingerprint(),
		State: TrustEnabled, Capabilities: snapshot,
		ApprovedCapabilityFingerprint: snapshot.Fingerprint,
		Health:                        HealthConnecting, Generation: 4, ReviewedBy: "operator",
		ReviewedAt: &reviewed, DiscoveryLeaseID: "mcp-discovery-crashed",
		DiscoveryLeaseExpiresAt: &leaseExpiresAt,
		CreatedAt:               now.Add(-time.Minute), UpdatedAt: now}
	if err := record.Validate(); err != nil {
		t.Fatal(err)
	}
	store := &clientStoreFake{record: record, created: true}
	manager, err := NewClientManager(store, credentialReaderFake{}, ManagerOptions{
		Now: func() time.Time { return now.Add(time.Second) },
	})
	if err != nil {
		t.Fatal(err)
	}
	count, err := manager.ReconcileStartup(t.Context())
	if err != nil || count != 1 {
		t.Fatalf("startup reconciliation count=%d err=%v", count, err)
	}
	recovered, err := store.GetMCPClientServer(t.Context(), descriptor.ID)
	if err != nil || recovered.State != TrustEnabled ||
		recovered.Health != HealthUnavailable ||
		recovered.ApprovedCapabilityFingerprint != snapshot.Fingerprint || recovered.Generation != 5 {
		t.Fatalf("interrupted discovery was not fenced: %#v err=%v", recovered, err)
	}
}

func TestClientManagerStartupRecoveryPreservesAnotherProcessActiveDiscovery(t *testing.T) {
	now := time.Date(2026, 8, 20, 1, 2, 3, 0, time.UTC)
	leaseExpiresAt := now.Add(time.Minute)
	descriptor := ServerDescriptor{ProtocolVersion: ClientProtocolVersion,
		ID: "active-server", Name: "Active Server", Transport: TransportStreamableHTTP,
		Target: "https://mcp.example.invalid/rpc", DeclaredCapabilities: []CapabilityKind{CapabilityTools},
		Scope: ScopeWorkspace, WorkspaceID: "workspace-1", Source: Source{Kind: "manual", URI: "operator"},
		CallTimeoutMillis: 1_000, MaxResultBytes: 8_192}
	reviewed := now
	record := ServerRecord{ProtocolVersion: ServerRecordProtocolVersion,
		Descriptor: descriptor, DescriptorFingerprint: descriptor.Fingerprint(),
		State: TrustDiscoveryApproved, Health: HealthConnecting, Generation: 3,
		ReviewedBy: "operator", ReviewedAt: &reviewed,
		DiscoveryLeaseID: "mcp-discovery-active", DiscoveryLeaseExpiresAt: &leaseExpiresAt,
		CreatedAt: now.Add(-time.Minute), UpdatedAt: now}
	if err := record.Validate(); err != nil {
		t.Fatal(err)
	}
	store := &clientStoreFake{record: record, created: true}
	manager, err := NewClientManager(store, credentialReaderFake{}, ManagerOptions{
		Now: func() time.Time { return now.Add(time.Second) },
	})
	if err != nil {
		t.Fatal(err)
	}
	count, err := manager.ReconcileStartup(t.Context())
	if err != nil || count != 0 {
		t.Fatalf("active discovery reconciliation count=%d err=%v", count, err)
	}
	preserved, err := store.GetMCPClientServer(t.Context(), descriptor.ID)
	if err != nil || preserved.Health != HealthConnecting || preserved.Generation != 3 ||
		preserved.DiscoveryLeaseID != "mcp-discovery-active" {
		t.Fatalf("active discovery lease was changed: %#v err=%v", preserved, err)
	}
}
