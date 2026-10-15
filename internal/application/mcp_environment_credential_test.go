package application

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"cyberagent-workbench/internal/apperror"
	"cyberagent-workbench/internal/credential"
	"cyberagent-workbench/internal/mcp"
)

func TestMCPEnvironmentCredentialUsesExactDescriptorReference(t *testing.T) {
	record := mcpCredentialRecord("environment")
	record.Descriptor.ProtocolVersion = mcp.ConfigClientProtocolVersion
	record.Descriptor.Transport, record.Descriptor.Target = mcp.TransportStdio, filepath.Join(t.TempDir(), "server.exe")
	record.Descriptor.CredentialRef = ""
	record.Descriptor.EnvCredentialRefs = map[string]string{"API_KEY": "environment-fixture"}
	record.DescriptorFingerprint = record.Descriptor.Fingerprint()
	registry := &mcpCredentialRegistryFake{[]mcp.ServerRecord{record}}
	owned := credential.NewMemoryStore()
	service := NewMCPCredentialService(registry, owned)
	binding := mcpCredentialBinding(record)
	binding.CredentialRef = "environment-fixture"
	request := mcpCredentialChange(t, service, binding)
	request.Secret = "synthetic-environment-value"
	status, err := service.Change(t.Context(), request)
	if err != nil || !status.Configured || status.CredentialRef != binding.CredentialRef || registry.records[0].State != mcp.TrustStaged {
		t.Fatal("environment credential lifecycle lost binding or reviewed server", err)
	}
	raw, _ := json.Marshal(status)
	if strings.Contains(string(raw), request.Secret) || status.PlaintextReturned {
		t.Fatal("environment secret entered status")
	}
	bad := binding
	bad.CredentialRef = "unregistered-reference"
	if _, err := service.Status(t.Context(), bad); apperror.CodeOf(err) != apperror.CodeConflict {
		t.Fatal("unregistered environment reference accepted", err)
	}
	request.Secret = "uc-mcp-oauth-v1:fixture-envelope"
	if _, err := service.Change(t.Context(), request); apperror.CodeOf(err) != apperror.CodeInvalidArgument {
		t.Fatal("reserved envelope accepted as raw environment secret", err)
	}
	request.Action, request.Secret = "delete", ""
	status, err = service.Change(t.Context(), request)
	if err != nil || status.Configured {
		t.Fatal("environment credential deletion did not verify absence", err)
	}
}

func TestMCPEnvironmentCredentialRejectsHTTPSharedReferenceMutation(t *testing.T) {
	remote := mcpCredentialRecord("remote")
	local := mcpCredentialRecord("local")
	local.Descriptor.ProtocolVersion = mcp.ConfigClientProtocolVersion
	local.Descriptor.Transport, local.Descriptor.Target = mcp.TransportStdio, filepath.Join(t.TempDir(), "server.exe")
	local.Descriptor.CredentialRef = ""
	local.Descriptor.EnvCredentialRefs = map[string]string{"API_KEY": remote.Descriptor.CredentialRef}
	local.DescriptorFingerprint = local.Descriptor.Fingerprint()
	owned := credential.NewMemoryStore()
	service := NewMCPCredentialService(&mcpCredentialRegistryFake{[]mcp.ServerRecord{local, remote}}, owned)
	binding := mcpCredentialBinding(local)
	binding.CredentialRef = remote.Descriptor.CredentialRef
	status, err := service.Status(t.Context(), binding)
	if err != nil || !status.EndpointConflict || status.RegistrationCount != 2 {
		t.Fatal("shared environment/HTTP reference was not reported", err)
	}
	if _, err := service.Change(t.Context(), ChangeMCPCredentialRequest{Version: MCPCredentialProtocolVersion, Binding: binding, Action: "set", Confirm: true,
		Secret: "synthetic-environment-value", ExpectedReferenceFingerprint: status.ReferenceFingerprint}); apperror.CodeOf(err) != apperror.CodeConflict {
		t.Fatal("cross-transport reference mutation accepted", err)
	}
	if configured, _ := owned.Configured(t.Context(), binding.CredentialRef); configured {
		t.Fatal("conflicting reference wrote a secret")
	}
}
