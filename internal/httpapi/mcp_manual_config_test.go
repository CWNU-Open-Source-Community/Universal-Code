package httpapi

import (
	"net/http"
	"path/filepath"
	"testing"

	"cyberagent-workbench/internal/mcp"
)

func TestManualMCPConfigurationHTTPRegistrationPreservesReviewedFields(t *testing.T) {
	f, _, _, _ := newExtensionOnboardingFixture(t)
	request := extensionRegistrationRequest(f)
	request.Descriptor.ProtocolVersion = mcp.ConfigClientProtocolVersion
	request.Descriptor.Transport, request.Descriptor.Target = mcp.TransportStdio, filepath.Join(t.TempDir(), "server.exe")
	request.Descriptor.Cwd = t.TempDir()
	request.Descriptor.Env = map[string]string{"MCP_MODE": "selected"}
	request.Descriptor.EnvCredentialRefs = map[string]string{"API_KEY": "manual-config-secret"}
	var result ExtensionMCPRegistrationView
	decodeDataStatus(t, extensionOnboardingRequest(t, f.api, ExtensionMCPRegistrationPath, request), http.StatusAccepted, &result)
	if result.Server.State != "staged" || result.Server.Cwd != request.Descriptor.Cwd || result.Server.Env["MCP_MODE"] != "selected" ||
		result.Server.EnvCredentialRefs["API_KEY"] != "manual-config-secret" {
		t.Fatal("registration dropped configuration or bypassed review")
	}
	request.Descriptor.Env["MCP_MODE"] = "changed"
	if response := extensionOnboardingRequest(t, f.api, ExtensionMCPRegistrationPath, request); response.Code != http.StatusConflict {
		t.Fatal("configuration change reused reviewed identity", response.Code)
	}
	request.Descriptor.ID = "manual-http-config"
	request.Descriptor.Transport, request.Descriptor.Target, request.Descriptor.Cwd = mcp.TransportStreamableHTTP, "http://127.0.0.1:8099/mcp", ""
	request.Descriptor.Env, request.Descriptor.EnvCredentialRefs = nil, nil
	request.Descriptor.Headers = map[string]string{"X-Project": "fixture"}
	decodeDataStatus(t, extensionOnboardingRequest(t, f.api, ExtensionMCPRegistrationPath, request), http.StatusAccepted, &result)
	if result.Server.Headers["X-Project"] != "fixture" || result.Server.Target != request.Descriptor.Target {
		t.Fatal("local HTTP header configuration was lost")
	}
	request.Descriptor.ID = "bad-manual-config"
	request.Descriptor.Headers["Authorization"] = "synthetic-inline-secret"
	if response := extensionOnboardingRequest(t, f.api, ExtensionMCPRegistrationPath, request); response.Code != http.StatusBadRequest {
		t.Fatal("inline HTTP credential accepted", response.Code)
	}
}
