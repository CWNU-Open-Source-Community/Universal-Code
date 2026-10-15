package httpapi

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"cyberagent-workbench/internal/application"
	"cyberagent-workbench/internal/hooks"
	"cyberagent-workbench/internal/mcp"
	"cyberagent-workbench/internal/plugins"
)

type extensionControllerStub struct {
	inventory application.ExtensionInventory
}

func (s *extensionControllerStub) PluginHistory(_ context.Context, id string) (plugins.History, error) {
	value := extensionTestPlugin(id)
	return plugins.History{InstallationID: id, PackageID: value.PackageID(), Installations: []plugins.Installation{value}, PublisherInstallationIDs: []string{}, TotalVersions: 1}, nil
}
func (s *extensionControllerStub) RollbackPlugin(_ context.Context, currentID, targetID string, request plugins.RollbackRequest) (plugins.Installation, plugins.Installation, error) {
	current, target := extensionTestPlugin(currentID), extensionTestPlugin(targetID)
	current.State, target.State = plugins.StateRolledBack, plugins.StateEnabled
	current.Generation, target.Generation = request.ExpectedCurrentGeneration+1, request.ExpectedTargetGeneration+1
	target.EnabledCapabilities = request.Capabilities
	return current, target, nil
}
func (s *extensionControllerStub) RevokePluginPublisher(_ context.Context, _, fingerprint string, generation int64, _ string) (plugins.PublisherTrust, error) {
	return plugins.PublisherTrust{Fingerprint: fingerprint, Publisher: "test-publisher", State: plugins.PublisherRevoked, Generation: generation + 1, ReviewedAt: time.Now().UTC()}, nil
}
func (s *extensionControllerStub) TrustPluginPublisher(_ context.Context, _, fingerprint string, generation int64, _ string) (plugins.PublisherTrust, error) {
	return plugins.PublisherTrust{Fingerprint: fingerprint, Publisher: "test-publisher", State: plugins.PublisherTrusted, Generation: generation + 1, ReviewedAt: time.Now().UTC()}, nil
}
func (s *extensionControllerStub) HookDiagnostics(_ context.Context, runID, workspaceID string) (application.HookDiagnostics, error) {
	return application.HookDiagnostics{RunID: runID, WorkspaceID: workspaceID, Declarations: []application.HookDeclaration{}, Observations: []hooks.AuditRecord{}}, nil
}

func (s *extensionControllerStub) Inventory(context.Context, string) (
	application.ExtensionInventory, error,
) {
	if s.inventory.ProtocolVersion == "" {
		return application.ExtensionInventory{
			ProtocolVersion: application.ExtensionInventoryProtocolVersion,
			MCPServers:      []mcp.ServerRecord{}, MCPCalls: []mcp.CallAudit{},
			Plugins: []plugins.Installation{},
		}, nil
	}
	return s.inventory, nil
}

func (s *extensionControllerStub) ReviewMCP(_ context.Context, id string,
	_ mcp.ReviewRequest,
) (mcp.ServerRecord, error) {
	return extensionTestMCPServer(id), nil
}

func (s *extensionControllerStub) RefreshMCP(_ context.Context,
	id string,
) (mcp.ServerRecord, error) {
	return extensionTestMCPServer(id), nil
}

func (s *extensionControllerStub) ReviewPlugin(_ context.Context, id string,
	_ plugins.ReviewRequest,
) (plugins.Installation, error) {
	return extensionTestPlugin(id), nil
}

func extensionTestMCPServer(id string) mcp.ServerRecord {
	now := time.Date(2026, 8, 20, 1, 2, 3, 0, time.UTC)
	descriptor := mcp.ServerDescriptor{ProtocolVersion: mcp.ClientProtocolVersion,
		ID: id, Name: "extension-test", Transport: mcp.TransportStreamableHTTP,
		Target: "https://mcp.invalid/v1", CredentialRef: "extension-test-token",
		DeclaredCapabilities: []mcp.CapabilityKind{mcp.CapabilityTools},
		Scope:                mcp.ScopeWorkspace, WorkspaceID: "workspace-extension-test",
		Source:            mcp.Source{Kind: "manual", URI: "operator"},
		CallTimeoutMillis: 1_000, MaxResultBytes: 4_096}
	return mcp.ServerRecord{ProtocolVersion: mcp.ServerRecordProtocolVersion,
		Descriptor: descriptor, DescriptorFingerprint: descriptor.Fingerprint(),
		State: mcp.TrustDisabled, Health: mcp.HealthUnknown, Generation: 2,
		CreatedAt: now, UpdatedAt: now}
}

func extensionTestPlugin(id string) plugins.Installation {
	now := time.Date(2026, 8, 20, 1, 2, 3, 0, time.UTC)
	digest := strings.Repeat("a", 64)
	return plugins.Installation{ProtocolVersion: plugins.InstallationProtocol,
		ID: id, Manifest: plugins.Manifest{ProtocolVersion: plugins.ProtocolVersion,
			ID: "extension-test", Name: "Extension Test", Version: "1.0.0",
			Publisher: "test-publisher", Description: "Inert test Plugin",
			Capabilities: []plugins.Capability{plugins.CapabilityHooks},
			Files:        []plugins.FileEntry{}, Hooks: []hooks.Declaration{{
				ProtocolVersion: hooks.ProtocolVersion, ID: "record-test",
				Event: hooks.PreTool, Action: hooks.ActionRecord,
				FailurePolicy: hooks.FailureContinue, TimeoutMillis: 100,
			}}},
		Source: plugins.InstallSource{Kind: "local_file", URI: `C:\extension-test.zip`,
			SHA256: digest},
		ArchiveSHA256: digest, PackageFingerprint: strings.Repeat("b", 64),
		ArchiveBytes: 128, State: plugins.StateDisabled,
		EnabledCapabilities: []plugins.Capability{}, Generation: 2,
		StagedBy:  "extension-test-operator",
		CreatedAt: now, UpdatedAt: now}
}

func TestExtensionProjectionOmitsSecretsAndExecutablePluginMaterial(t *testing.T) {
	server := extensionTestMCPServer("mcp-extension-projection")
	plugin := extensionTestPlugin("plugin-extension-projection")
	view := extensionMCPServerView(server)
	pluginView := ProjectPluginInstallation(plugin)
	if view.CredentialRef != "extension-test-token" || view.Target != server.Descriptor.Target {
		t.Fatalf("unexpected MCP projection: %#v", view)
	}
	encoded := extensionJSON(t, struct {
		MCP    ExtensionMCPServerView          `json:"mcp"`
		Plugin ExtensionPluginInstallationView `json:"plugin"`
	}{MCP: view, Plugin: pluginView})
	for _, forbidden := range []string{"publisher_public_key", "arguments\"", "raw", "archive_bytes"} {
		if strings.Contains(encoded, forbidden) {
			t.Fatalf("extension projection exposed %q: %s", forbidden, encoded)
		}
	}
}

func extensionJSON(t *testing.T, value any) string {
	t.Helper()
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return string(encoded)
}
