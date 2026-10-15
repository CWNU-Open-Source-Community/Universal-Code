package store

import (
	"encoding/json"
	"path/filepath"
	"testing"
	"time"

	"cyberagent-workbench/internal/mcp"
)

func TestSchemaV196PreservesMCPServersAndAdmitsRemoteTransports(t *testing.T) {
	state := openHistoricalTestDatabase(t, filepath.Join(t.TempDir(), "transports.db"), 195)
	defer state.Close()
	now := time.Now().UTC()
	if err := state.SaveWorkspace(t.Context(), WorkspaceRecord{ID: "transport-workspace", Name: "transports", RootPath: t.TempDir(), CreatedAt: now}); err != nil {
		t.Fatal(err)
	}
	d := mcp.ServerDescriptor{ProtocolVersion: mcp.ConfigClientProtocolVersion, ID: "historical-http", Name: "HTTP", Transport: mcp.TransportStreamableHTTP,
		Target: "https://example.org/mcp", Scope: mcp.ScopeWorkspace, WorkspaceID: "transport-workspace", Source: mcp.Source{Kind: "manual", URI: "operator"},
		DeclaredCapabilities: []mcp.CapabilityKind{mcp.CapabilityTools}, CallTimeoutMillis: 1000, MaxResultBytes: 4096}
	record := mcp.ServerRecord{ProtocolVersion: mcp.ServerRecordProtocolVersion, Descriptor: d, DescriptorFingerprint: d.Fingerprint(), State: mcp.TrustStaged, Health: mcp.HealthUnknown, Generation: 1, CreatedAt: now, UpdatedAt: now}
	if _, _, err := state.CreateMCPClientServer(t.Context(), record); err != nil {
		t.Fatal(err)
	}
	before, err := state.GetMCPClientServer(t.Context(), d.ID)
	if err != nil {
		t.Fatal(err)
	}
	var oldRow int64
	if err := state.db.QueryRow(`SELECT rowid FROM mcp_client_servers WHERE id=?`, d.ID).Scan(&oldRow); err != nil {
		t.Fatal(err)
	}
	if err := state.applyMigration(t.Context(), migrationPlan()[195]); err != nil {
		t.Fatal(err)
	}
	after, err := state.GetMCPClientServer(t.Context(), d.ID)
	if err != nil {
		t.Fatal(err)
	}
	first, _ := json.Marshal(before)
	second, _ := json.Marshal(after)
	var newRow int64
	if err := state.db.QueryRow(`SELECT rowid FROM mcp_client_servers WHERE id=?`, d.ID).Scan(&newRow); err != nil || oldRow != newRow || string(first) != string(second) {
		t.Fatal("historical server identity or bytes changed", err)
	}
	for _, kind := range []mcp.TransportKind{mcp.TransportSSE, mcp.TransportWebSocket} {
		next := record
		next.Descriptor.ID, next.Descriptor.Name, next.Descriptor.Transport = string(kind), string(kind), kind
		if kind == mcp.TransportWebSocket {
			next.Descriptor.Target = "wss://example.org/mcp"
		}
		next.DescriptorFingerprint = next.Descriptor.Fingerprint()
		if _, _, err := state.CreateMCPClientServer(t.Context(), next); err != nil {
			t.Fatal(err)
		}
	}
	for _, query := range []string{`UPDATE mcp_client_servers SET transport='sse', generation=generation+1 WHERE id='historical-http'`, `DELETE FROM mcp_client_servers WHERE id='historical-http'`, `UPDATE mcp_client_servers SET generation=generation+2 WHERE id='historical-http'`} {
		if _, err := state.db.Exec(query); err == nil {
			t.Fatal("retained server constraint missing", query)
		}
	}
	assertNoForeignKeyViolations(t, state.db)
}
