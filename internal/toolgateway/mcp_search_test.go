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

type mcpSearchExecutorStub struct {
	mcpExecutorStub
	searches int
	scope    MCPExecutionScope
}

func (s *mcpSearchExecutorStub) SearchMCP(_ context.Context, scope MCPExecutionScope, _ MCPSearchPayload) (json.RawMessage, error) {
	s.searches++
	s.scope = scope
	return json.RawMessage(`{"operations":[]}`), nil
}

func TestMCPSearchPayloadBoundsAndRevision(t *testing.T) {
	for _, payload := range []string{
		`{"query":"文档","extra":true}`, `{"query":"x"} {}`, `{"query":"x","offset":1}`,
		`{"query":"x","limit":21}`, `{"query":"x","offset":-1}`, `{"query":"x","server_id":"bad/server"}`,
	} {
		if _, _, err := NormalizeMCPSearchPayload(json.RawMessage(payload)); err == nil {
			t.Fatal("invalid search payload accepted", payload)
		}
	}
	input, _, err := NormalizeMCPSearchPayload(json.RawMessage(`{"query":"  文档  "}`))
	if err != nil || input.Query != "文档" || input.Limit != 5 {
		t.Fatalf("query was not normalized: %+v %v", input, err)
	}
}

func TestMCPSearchGatewayUsesFencedScopeAndHonorsHookDenial(t *testing.T) {
	executor := &mcpSearchExecutorStub{}
	call := ToolCall{Name: MCPToolSearchTool, Payload: json.RawMessage(`{"query":"docs"}`),
		RunID: "run-1", MissionID: "mission-1", AgentID: "agent-1", AgentAttemptID: "attempt-1", SessionID: "session-1",
		WorkspaceID: "workspace-1", OperationKey: strings.Repeat("a", 64),
		Surface: domain.ExecutionSurfaceCode, Phase: domain.ExecutionPhaseDeliver, Role: domain.AgentRoleRoot,
		PermissionMode: domain.RunExecutionPermissionAsk, PermissionSnapshotID: "permission-1", PermissionRevision: 1,
		LeaseID: "lease-1", LeaseGeneration: 1, RequestedBy: "run_supervisor"}
	gateway := New(nil, policy.NewDefaultChecker()).WithMCPExecutor(executor)
	result, err := gateway.Invoke(t.Context(), call)
	if err != nil || executor.searches != 1 || executor.calls != 0 || executor.scope.RunID != call.RunID ||
		executor.scope.AgentAttemptID != call.AgentAttemptID || result.Result.Metadata["capability_grant"] != "false" {
		t.Fatalf("catalog did not use its host read scope: %+v %v", result, err)
	}
	engine := hooks.NewEngine(nil)
	if err := engine.Replace([]hooks.Registration{{PluginID: "catalog-guard", PluginFingerprint: strings.Repeat("b", 64),
		Declaration: hooks.Declaration{ProtocolVersion: hooks.ProtocolVersion, ID: "deny-search", Event: hooks.PreTool,
			Action: hooks.ActionDeny, FailurePolicy: hooks.FailureDeny, TimeoutMillis: 100,
			ToolNames: []string{string(MCPToolSearchTool)}, Message: "catalog disabled"}}}); err != nil {
		t.Fatal(err)
	}
	result, err = gateway.WithLifecycleHooks(engine).Invoke(t.Context(), call)
	if err != nil || result.Decision.Allowed || executor.searches != 1 {
		t.Fatalf("denied search reached catalog: %+v %v", result, err)
	}
}
