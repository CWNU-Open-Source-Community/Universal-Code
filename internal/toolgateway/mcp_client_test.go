package toolgateway

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/hooks"
	"cyberagent-workbench/internal/policy"
)

type mcpExecutorStub struct {
	calls   int
	scope   MCPExecutionScope
	input   MCPToolCallPayload
	content string
}

func (s *mcpExecutorStub) ExecuteMCP(_ context.Context, scope MCPExecutionScope,
	payload MCPToolCallPayload,
) (MCPExecutionResult, error) {
	s.calls++
	s.scope = scope
	s.input = payload
	content := s.content
	if content == "" {
		content = `{"text":"AKIAABCDEFGHIJKLMNOP"}`
	}
	return MCPExecutionResult{Content: content,
		Metadata: map[string]string{"remote": "untrusted"}}, nil
}

func TestMCPGatewayRequiresExactFencedScopeAndRedactsOutput(t *testing.T) {
	executor := &mcpExecutorStub{}
	fingerprint := strings.Repeat("a", 64)
	payload := json.RawMessage(`{"version":"mcp-client.v1","server_id":"docs","tool_name":"lookup","capability_fingerprint":"` + fingerprint + `","arguments":{"query":"bounded"}}`)
	gateway := New(nil, policy.NewDefaultChecker()).WithMCPExecutor(executor)
	call := ToolCall{Name: MCPToolCallTool, Payload: payload, RunID: "run-1",
		MissionID: "mission-1", AgentID: "agent-1", SessionID: "session-1",
		WorkspaceID: "workspace-1", Surface: domain.ExecutionSurfaceCode,
		OperationKey: strings.Repeat("d", 64),
		Phase:        domain.ExecutionPhaseDeliver, Role: domain.AgentRoleRoot,
		PermissionMode:       domain.RunExecutionPermissionFull,
		PermissionSnapshotID: "permission-1", PermissionRevision: 1,
		LeaseID: "lease-1", LeaseGeneration: 1, RequestedBy: "run_supervisor"}
	outcome, err := gateway.Invoke(t.Context(), call)
	if err != nil {
		t.Fatal(err)
	}
	if executor.calls != 1 || executor.scope.RunID != call.RunID ||
		executor.input.CapabilityFingerprint != fingerprint || outcome.Result == nil ||
		strings.Contains(outcome.Result.Stdout, "AKIAABCDEFGHIJKLMNOP") ||
		!strings.Contains(outcome.Result.Stdout, "[REDACTED:aws-access-key]") ||
		outcome.Result.Metadata["untrusted_output"] != "true" {
		t.Fatalf("MCP gateway did not fence or sanitize the call: %#v %#v", executor, outcome)
	}
	call.PermissionMode = domain.RunExecutionPermissionAuto
	if _, err := gateway.Invoke(t.Context(), call); err != nil || executor.calls != 2 ||
		executor.scope.PermissionMode != domain.RunExecutionPermissionAuto {
		t.Fatalf("Auto did not reach the host operation authorizer: calls=%d scope=%+v err=%v",
			executor.calls, executor.scope, err)
	}
	call.PermissionMode = "unknown-permission"
	if _, err := gateway.Invoke(t.Context(), call); err == nil {
		t.Fatal("MCP gateway accepted an unknown permission")
	}
}

func TestMCPGatewayRestrictedHookCanDenyBeforeTransport(t *testing.T) {
	executor := &mcpExecutorStub{}
	engine := hooks.NewEngine(nil)
	if err := engine.Replace([]hooks.Registration{{PluginID: "guard",
		PluginFingerprint: strings.Repeat("b", 64), Declaration: hooks.Declaration{
			ProtocolVersion: hooks.ProtocolVersion, ID: "deny-mcp", Event: hooks.PreTool,
			Action: hooks.ActionDeny, FailurePolicy: hooks.FailureDeny, TimeoutMillis: 100,
			ToolNames: []string{string(MCPToolCallTool)}, Message: "MCP disabled by policy",
		}}}); err != nil {
		t.Fatal(err)
	}
	payload := json.RawMessage(`{"version":"mcp-client.v1","server_id":"docs","tool_name":"lookup","capability_fingerprint":"` + strings.Repeat("a", 64) + `","arguments":{}}`)
	outcome, err := New(nil, policy.NewDefaultChecker()).WithMCPExecutor(executor).
		WithLifecycleHooks(engine).Invoke(t.Context(), ToolCall{Name: MCPToolCallTool,
		Payload: payload, RunID: "run-1", MissionID: "mission-1",
		AgentID: "agent-1", SessionID: "session-1", WorkspaceID: "workspace-1",
		OperationKey: strings.Repeat("d", 64),
		Surface:      domain.ExecutionSurfaceCode, Phase: domain.ExecutionPhaseDeliver,
		Role: domain.AgentRoleRoot, PermissionMode: domain.RunExecutionPermissionFullAccess,
		PermissionSnapshotID: "permission-1", PermissionRevision: 1,
		LeaseID: "lease-1", LeaseGeneration: 1, RequestedBy: "run_supervisor"})
	if err != nil || outcome.Decision.Allowed || executor.calls != 0 ||
		strings.Contains(outcome.Decision.Reason, "MCP disabled by policy") {
		t.Fatalf("restricted pre-tool hook did not deny before MCP transport: %#v err=%v", outcome, err)
	}
}

func TestMCPPayloadRejectsSecretMaterial(t *testing.T) {
	payload := json.RawMessage(`{"version":"mcp-client.v1","server_id":"docs","tool_name":"lookup","capability_fingerprint":"` + strings.Repeat("a", 64) + `","arguments":{"api_key":"sk-abcdefghijklmnopqrstuvwxyz"}}`)
	if _, _, err := NormalizeMCPToolPayload(payload); err == nil {
		t.Fatal("secret-like material was accepted in MCP arguments")
	}
}

func TestMCPPayloadRejectsSensitiveArgumentNamesRecursively(t *testing.T) {
	fingerprint := strings.Repeat("a", 64)
	for name, arguments := range map[string]string{
		"password":        `{"password":"ordinary-value"}`,
		"x-api-key":       `{"X-API-Key":"ordinary-value"}`,
		"github token":    `{"github_token":"ordinary-value"}`,
		"authorization":   `{"headers":{"Authorization":"ordinary-value"}}`,
		"authentication":  `{"authentication":"ordinary-value"}`,
		"auth header":     `{"auth_header":"ordinary-value"}`,
		"access key":      `{"accessKey":"ordinary-value"}`,
		"signing key":     `{"signing_key":"ordinary-value"}`,
		"credentials":     `{"credentials":"ordinary-value"}`,
		"nested array":    `{"items":[{"client_secret":"ordinary-value"}]}`,
		"secret key name": `{"sk-proj-abcdefghijklmnopqrstuvwxyz123456":"ordinary-value"}`,
	} {
		t.Run(name, func(t *testing.T) {
			payload := json.RawMessage(`{"version":"mcp-client.v1","server_id":"docs",` +
				`"tool_name":"lookup","capability_fingerprint":"` + fingerprint +
				`","arguments":` + arguments + `}`)
			if _, _, err := NormalizeMCPToolPayload(payload); err == nil ||
				!strings.Contains(err.Error(), "approved credential reference") {
				t.Fatalf("sensitive MCP argument was accepted: %v", err)
			}
		})
	}
}

func TestMCPPayloadAllowsNonSecretTokenMetadataAndCredentialReference(t *testing.T) {
	payload := json.RawMessage(`{"version":"mcp-client.v1","server_id":"docs",` +
		`"tool_name":"lookup","capability_fingerprint":"` + strings.Repeat("a", 64) +
		`","arguments":{"max_tokens":128,"token_count":4,` +
		`"credential_ref":"credential-docs-production",` +
		`"token_id":"provider-session-token","secret_name":"docs-signing-secret",` +
		`"api_key_reference":"docs-provider-key"}}`)
	value, canonical, err := NormalizeMCPToolPayload(payload)
	if err != nil || !json.Valid(canonical) ||
		!strings.Contains(string(value.Arguments), `"credential_ref"`) ||
		!strings.Contains(string(value.Arguments), `"token_id"`) ||
		!strings.Contains(string(value.Arguments), `"secret_name"`) ||
		!strings.Contains(string(value.Arguments), `"api_key_reference"`) {
		t.Fatalf("safe MCP metadata was rejected: value=%#v canonical=%s err=%v",
			value, canonical, err)
	}
}

func TestMCPPayloadRejectsSecretShapedValueBehindCredentialReference(t *testing.T) {
	payload := json.RawMessage(`{"version":"mcp-client.v1","server_id":"docs",` +
		`"tool_name":"lookup","capability_fingerprint":"` + strings.Repeat("a", 64) +
		`","arguments":{"token_id":"sk-abcdefghijklmnopqrstuvwxyz123456"}}`)
	if _, _, err := NormalizeMCPToolPayload(payload); err == nil {
		t.Fatal("secret-shaped value was accepted merely because its field was a reference")
	}
}

func TestMCPGatewayRedactsSensitiveJSONFieldsBeforeDurableOutcome(t *testing.T) {
	argumentCanary := "MCP_RESULT_PASSWORD_CANARY_83C1"
	tokenCanary := "MCP_RESULT_TOKEN_CANARY_593A"
	executor := &mcpExecutorStub{content: `{"status":"created","password":"` +
		argumentCanary + `","nested":[{"github_token":"` + tokenCanary +
		`"}],"safe":"visible"}`}
	payload := json.RawMessage(`{"version":"mcp-client.v1","server_id":"docs",` +
		`"tool_name":"lookup","capability_fingerprint":"` + strings.Repeat("a", 64) +
		`","arguments":{"query":"bounded"}}`)
	call := ToolCall{Name: MCPToolCallTool, Payload: payload, RunID: "run-1",
		MissionID: "mission-1", AgentID: "agent-1", SessionID: "session-1",
		WorkspaceID: "workspace-1", Surface: domain.ExecutionSurfaceCode,
		OperationKey: strings.Repeat("d", 64), Phase: domain.ExecutionPhaseDeliver,
		Role: domain.AgentRoleRoot, PermissionMode: domain.RunExecutionPermissionFullAccess,
		PermissionSnapshotID: "permission-1", PermissionRevision: 1,
		LeaseID: "lease-1", LeaseGeneration: 1, RequestedBy: "run_supervisor"}
	outcome, err := New(nil, policy.NewDefaultChecker()).WithMCPExecutor(executor).
		Invoke(t.Context(), call)
	if err != nil || outcome.Result == nil {
		t.Fatalf("MCP result failed: %#v err=%v", outcome, err)
	}
	if strings.Contains(outcome.Result.Stdout, argumentCanary) ||
		strings.Contains(outcome.Result.Stdout, tokenCanary) ||
		!strings.Contains(outcome.Result.Stdout, mcpSensitiveFieldPlaceholder) ||
		!strings.Contains(outcome.Result.Stdout, `"safe":"visible"`) {
		t.Fatalf("MCP result was not safely projected before persistence: %s",
			outcome.Result.Stdout)
	}
}

func TestMCPContentPayloadVersionSeparatesMethods(t *testing.T) {
	old := `{"version":"mcp-client.v1","server_id":"docs","tool_name":"lookup","capability_fingerprint":"` + strings.Repeat("a", 64) + `","arguments":{}}`
	_, canonical, err := NormalizeMCPToolPayload(json.RawMessage(old))
	if err != nil || string(canonical) != old {
		t.Fatalf("old canonical payload changed: %s %v", canonical, err)
	}
	for _, method := range []string{"resources/read", "prompts/get"} {
		name := "resources.read"
		if method == "prompts/get" {
			name = "prompts.get"
		}
		raw := `{"version":"mcp-client.v2","method":"` + method + `","server_id":"docs","tool_name":"` + name + `","capability_fingerprint":"` + strings.Repeat("a", 64) + `","arguments":{}}`
		p, _, err := NormalizeMCPToolPayload(json.RawMessage(raw))
		if err != nil || p.Method != method {
			t.Fatalf("content normalization: %+v %v", p, err)
		}
		if _, _, err := NormalizeMCPToolPayload(json.RawMessage(strings.Replace(raw, "mcp-client.v2", "mcp-client.v1", 1))); err == nil {
			t.Fatal("v1 admitted a content method")
		}
	}
	if _, _, err := NormalizeMCPToolPayload(json.RawMessage(strings.Replace(old, "mcp-client.v1", "mcp-client.v2", 1))); err == nil {
		t.Fatal("v2 omitted its content method")
	}
}
