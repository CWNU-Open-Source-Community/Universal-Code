package toolgateway

import (
	"bytes"
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/toolbudget"
)

func TestToolRegistryKeepsModelCatalogOrderAndPhaseSets(t *testing.T) {
	wantDefault := []ToolName{
		WorkItemCreateTool, NoteCreateTool, HistorySearchTool, HistoryReadTool, ContextPinProposeTool,
		SkillReadTool, SpecialistDelegationProposeTool, ChildTaskProposeTool,
		DockerSandboxRunProposeTool, SkillCandidateProposeTool, DebugTerminalTool,
		CommandRuntimeTool, RunTestsTool, MCPToolSearchTool, MCPToolCallTool, WebSearchTool, SourceSearchTool,
		WebFetchTool, WebCitationTool, BrowserStatusTool, BrowserNavigateTool,
		BrowserSnapshotTool, BrowserClickTool, BrowserTypeTool, BrowserScreenshotTool,
	}
	names := func(definitions []ToolDefinition) []ToolName {
		result := make([]ToolName, 0, len(definitions))
		for _, definition := range definitions {
			result = append(result, definition.Name)
		}
		return result
	}
	if got := names(SupervisorToolDefinitions()); !reflect.DeepEqual(got, wantDefault) {
		t.Fatalf("default model catalog order changed: %v", got)
	}
	wantPlan := make([]ToolName, 0, len(wantDefault))
	for _, name := range wantDefault {
		if name != SkillCandidateProposeTool {
			wantPlan = append(wantPlan, name)
		}
	}
	wantPlan = append(wantPlan, PlanDeliveryProposeTool)
	if got := names(PlanPhaseSupervisorToolDefinitions()); !reflect.DeepEqual(got, wantPlan) {
		t.Fatalf("Plan model catalog order changed: %v", got)
	}
}

func TestToolRegistryCatalogAndLookupAgreeWithoutSharingSchemas(t *testing.T) {
	seen := make(map[ToolName]bool)
	for _, definition := range AllSupervisorToolDefinitions() {
		if seen[definition.Name] {
			t.Fatalf("duplicate catalog tool %q", definition.Name)
		}
		seen[definition.Name] = true
		lookup, found := SupervisorToolDefinition(definition.Name)
		if !found || !reflect.DeepEqual(lookup, definition) || !IsSupervisorTool(definition.Name) ||
			!definition.Name.Valid() || !json.Valid(definition.InputSchema) {
			t.Fatalf("catalog/lookup mismatch for %q", definition.Name)
		}
		class, found := ClassForTool(definition.Name)
		if !found || class != definition.Class {
			t.Fatalf("catalog/class mismatch for %q", definition.Name)
		}
		registration, _ := lookupTool(definition.Name)
		if registration.normalize == nil || registration.invoke == nil {
			t.Fatalf("catalog tool %q has no canonicalizer or gateway handler", definition.Name)
		}
		definition.InputSchema[0] = '!'
		repeated, _ := SupervisorToolDefinition(definition.Name)
		if !bytes.Equal(repeated.InputSchema, lookup.InputSchema) {
			t.Fatalf("catalog mutation changed registered schema for %q", definition.Name)
		}
	}
	for _, name := range append(codeIntelCatalogNames(), BrowserScrollTool, BrowserKeyTool) {
		if !seen[name] {
			t.Fatalf("dynamic capability tool %q is missing from the static catalog", name)
		}
	}
}

func codeIntelCatalogNames() []ToolName {
	names := make([]ToolName, 0)
	for _, definition := range CodeIntelToolDefinitions() {
		names = append(names, definition.Name)
	}
	return names
}

func TestToolRegistryDoesNotExpandTypedActionsOrRetiredExecution(t *testing.T) {
	typedActions := TypedActionIDs()
	if !SpecialistTool.Valid() || IsSupervisorTool(SpecialistTool) {
		t.Fatal("child tool envelope must stay internal to the gateway")
	}
	if _, found := SupervisorToolDefinition(SpecialistTool); found {
		t.Fatal("child tool envelope was exposed as a Supervisor tool")
	}
	if _, err := NormalizeSupervisorToolPayload(SpecialistTool, json.RawMessage(`{}`)); err == nil {
		t.Fatal("Supervisor accepted the internal child tool envelope")
	}
	for _, name := range []ToolName{ReadFileTool, ListWorkspaceTool, ReplaceFileTool, ScriptProcessTool} {
		if _, found := typedActions[string(name)]; !found || !name.Valid() || IsSupervisorTool(name) {
			t.Fatalf("operator tool %q lost its existing configuration boundary", name)
		}
	}
	for _, name := range []ToolName{ShellTool, DebugTerminalTool, CommandRuntimeTool, SkillCandidateProposeTool, SpecialistTool} {
		if _, found := typedActions[string(name)]; found {
			t.Fatalf("registry expanded typed actions to %q", name)
		}
	}
	for _, name := range []ToolName{ControlledCommandProposeTool, HostCommandProposeTool, OneShotCommandProposeTool} {
		class, found := ClassForTool(name)
		if !found || class != ClassAgentProposal {
			t.Fatalf("historical proposal %q lost its readable action class", name)
		}
		if _, found := typedActions[string(name)]; found || name.Valid() || IsSupervisorTool(name) {
			t.Fatalf("historical proposal %q gained execution capability", name)
		}
		if _, found := SupervisorToolDefinition(name); found {
			t.Fatalf("historical proposal %q gained a model definition", name)
		}
		if _, err := NormalizeSupervisorToolPayload(name, json.RawMessage(`{}`)); err == nil {
			t.Fatalf("historical proposal %q gained a canonicalizer", name)
		}
	}
	if _, err := New(nil, nil).Invoke(t.Context(), ToolCall{Name: ScriptProcessTool}); err == nil ||
		!strings.Contains(err.Error(), "unsupported tool") {
		t.Fatalf("script_process escaped its separate proposal path: %v", err)
	}
}

type registryChargingStore struct {
	*memoryStore
}

func TestGatewayMissingRequiredExecutorDoesNotChargeBudget(t *testing.T) {
	capability := testBrowserActionCapabilityContext()
	browserCall := ToolCall{Name: BrowserStatusTool, OperationKey: "browser-operation",
		RunID: "run-1", SessionID: "session-1", AgentID: "agent-1",
		Surface: capability.Surface, Phase: capability.Phase, Role: capability.Role,
		Profile: capability.Profile, PermissionMode: capability.PermissionMode,
		ModeRevision: capability.ModeRevision, PermissionRevision: capability.PermissionRevision,
		CapabilityGeneration: BrowserActionCapabilitySnapshot(capability).Generation,
		RequestedBy:          "run_supervisor", LeaseID: "lease-1", LeaseGeneration: 1}
	legacyCall, agentCall := browserCall, browserCall
	legacyCall.Payload = json.RawMessage(`{"version":"browser_status.v1"}`)
	agentCall.Payload = json.RawMessage(`{"version":"browser_status.v2"}`)
	for _, test := range []struct {
		name      string
		call      ToolCall
		configure func(*Gateway) *Gateway
		wantError string
	}{
		{name: "command runtime", call: commandRuntimeToolCall(commandRuntimeValidPayload("Write-Output ok")),
			wantError: "command runtime executor is required"},
		{name: "Docker proposal", call: validDockerSandboxToolCall(dockerSandboxProposalPayload),
			wantError: "Docker Sandbox proposal executor is required"},
		{name: "legacy browser with only Agent Browser", call: legacyCall,
			configure: func(g *Gateway) *Gateway { return g.WithAgentBrowserExecutor(&agentBrowserGatewayProbe{}) },
			wantError: "browser action executor is required"},
		{name: "Agent Browser with only legacy browser", call: agentCall,
			configure: func(g *Gateway) *Gateway { return g.WithBrowserActionExecutor(&browserActionExecutorStub{}) },
			wantError: "browser action executor is required"},
	} {
		t.Run(test.name, func(t *testing.T) {
			store := newTrackedStructuredStore()
			gateway := New(store, nil)
			if test.configure != nil {
				gateway = test.configure(gateway)
			}
			_, err := gateway.Invoke(t.Context(), test.call)
			if err == nil || err.Error() != test.wantError || store.chargeCount() != 0 {
				t.Fatalf("missing executor consumed a tool call: err=%v charges=%d", err, store.chargeCount())
			}
		})
	}
}

func (s *registryChargingStore) ChargeToolCall(_ context.Context, request toolbudget.ChargeRequest) (toolbudget.Usage, error) {
	return toolbudget.Usage{Tracked: true, RunID: request.RunID,
		LastCharge: "tool-charge-" + request.ToolName}, nil
}

type registryEvidenceExecutor struct {
	names []ToolName
}

func (e *registryEvidenceExecutor) ExecuteAgentCode(_ context.Context, scope AgentCodeExecutionScope,
	name ToolName, _ json.RawMessage,
) (AgentCodeExecutionResult, error) {
	if err := scope.Validate(); err != nil {
		return AgentCodeExecutionResult{}, err
	}
	e.names = append(e.names, name)
	return AgentCodeExecutionResult{JSON: `{"evidence":[]}`}, nil
}

func TestGatewayDispatchesAdvertisedGitHubReviewEvidenceTools(t *testing.T) {
	executor := &registryEvidenceExecutor{}
	gateway := New(&registryChargingStore{newMemoryStore()}, nil).WithAgentCodeExecutor(executor)
	call := ToolCall{RunID: "run-1", MissionID: "mission-1", AgentID: "agent-1",
		SessionID: "session-1", WorkspaceID: "workspace-1", WorkspaceRoot: t.TempDir(),
		RootFingerprint: strings.Repeat("b", 64), CapabilityGeneration: strings.Repeat("c", 64),
		Surface: domain.ExecutionSurfaceCode, Phase: domain.ExecutionPhasePlan,
		Role: domain.AgentRoleRoot, Profile: domain.ProfileCode,
		PermissionMode: domain.RunExecutionPermissionApproval, ModeRevision: 1, PermissionRevision: 1,
		LeaseID: "lease-1", LeaseGeneration: 1, RequestedBy: "run_supervisor"}
	for _, test := range []struct {
		name    ToolName
		payload string
	}{
		{GitHubEvidenceListTool, `{"version":"agent-code-tools.v1","limit":10}`},
		{GitHubEvidenceReadTool, `{"version":"agent-code-tools.v1","evidence_id":"evidence-1"}`},
	} {
		call.Name, call.OperationKey, call.Payload = test.name, "evidence-"+string(test.name), json.RawMessage(test.payload)
		outcome, err := gateway.Invoke(t.Context(), call)
		if err != nil || outcome.Result == nil || outcome.Result.Status != StatusCompleted {
			t.Fatalf("advertised evidence tool %q failed dispatch: %#v err=%v", test.name, outcome, err)
		}
	}
	if !reflect.DeepEqual(executor.names, []ToolName{GitHubEvidenceListTool, GitHubEvidenceReadTool}) {
		t.Fatalf("evidence tools did not reach the existing Agent Code executor: %v", executor.names)
	}
	call.PermissionRevision = 0
	if _, err := gateway.Invoke(t.Context(), call); err == nil || len(executor.names) != 2 {
		t.Fatalf("evidence dispatch bypassed capability validation: %v", err)
	}
}
