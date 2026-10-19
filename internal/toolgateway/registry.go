package toolgateway

import (
	"context"
	"encoding/json"
	"fmt"
)

type ToolDefinition struct {
	Name        ToolName        `json:"name"`
	Description string          `json:"description"`
	Class       ActionClass     `json:"action_class"`
	Approval    ApprovalMode    `json:"approval"`
	InputSchema json.RawMessage `json:"input_schema"`
}

type toolRegistration struct {
	definition     ToolDefinition
	valid          bool
	supervisor     bool
	typedAction    bool
	defaultCatalog bool
	planCatalog    bool
	normalize      func(ToolName, json.RawMessage) (json.RawMessage, error)
	preflight      func(*Gateway, ToolCall) error
	invoke         func(*Gateway, context.Context, ToolCall) (Outcome, error)
}

type toolRegistry struct {
	ordered []toolRegistration
	byName  map[ToolName]int
}

var registeredTools toolRegistry

func init() {
	registeredTools = buildToolRegistry()
}

func buildToolRegistry() toolRegistry {
	registry := toolRegistry{byName: make(map[ToolName]int)}
	add := func(registration toolRegistration) {
		name := registration.definition.Name
		if _, exists := registry.byName[name]; exists {
			panic(fmt.Sprintf("duplicate tool registration %q", name))
		}
		registry.byName[name] = len(registry.ordered)
		registry.ordered = append(registry.ordered, registration)
	}
	addSupervisor := func(definitions []ToolDefinition,
		normalize func(ToolName, json.RawMessage) (json.RawMessage, error),
		invoke func(*Gateway, context.Context, ToolCall) (Outcome, error),
		preflight func(*Gateway, ToolCall) error,
		typedAction, defaultCatalog bool,
	) {
		for _, definition := range definitions {
			add(toolRegistration{definition: definition, valid: true, supervisor: true,
				typedAction: typedAction, defaultCatalog: defaultCatalog,
				planCatalog: defaultCatalog && definition.Name != SkillCandidateProposeTool,
				normalize:   normalize, preflight: preflight, invoke: invoke})
		}
	}

	// Child calls use an internal gateway envelope, outside the Supervisor catalog.
	add(toolRegistration{definition: specialistResultReadDefinition})
	add(toolRegistration{definition: SpecialistToolDefinition(false), valid: true,
		normalize: func(_ ToolName, payload json.RawMessage) (json.RawMessage, error) {
			_, canonical, err := NormalizeSpecialistToolPayload(payload)
			return canonical, err
		},
		preflight: requireExecutor("Specialist tool", func(g *Gateway, _ ToolCall) bool { return g.specialist != nil }), invoke: (*Gateway).invokeSpecialist})
	// This order is the existing model catalog order. Dynamic capability
	// projections narrow or replace these definitions at the application boundary.
	addSupervisor(StructuredMemoryToolDefinitions(), NormalizeStructuredMemoryPayload,
		(*Gateway).invokeStructuredMemory,
		requireExecutor("structured memory", func(g *Gateway, _ ToolCall) bool { return g.structuredMemory != nil }), true, true)
	addSupervisor(HistoryRecallToolDefinitions(), NormalizeHistoryRecallPayload,
		(*Gateway).invokeHistoryRecall, nil, true, true)
	addSupervisor(ThreadPeerMessageToolDefinitions(), NormalizeThreadPeerMessagePayload,
		(*Gateway).invokeThreadPeerMessages,
		requireExecutor("thread messaging", func(g *Gateway, _ ToolCall) bool { return g.threadPeerMessages != nil }), false, true)
	addSupervisor([]ToolDefinition{contextPinProposeDefinition},
		func(_ ToolName, payload json.RawMessage) (json.RawMessage, error) {
			_, canonical, err := normalizeContextPinPayload(payload)
			return canonical, err
		}, (*Gateway).invokeContextPin,
		requireExecutor("context pin proposal", func(g *Gateway, _ ToolCall) bool { return g.contextPins != nil }), true, true)
	addSupervisor([]ToolDefinition{SkillReadToolDefinition(nil)},
		func(_ ToolName, payload json.RawMessage) (json.RawMessage, error) {
			_, canonical, err := NormalizeSkillReadPayload(payload)
			return canonical, err
		}, (*Gateway).invokeSkillRead, nil, true, true)
	addSupervisor([]ToolDefinition{specialistDelegationDefinition},
		func(_ ToolName, payload json.RawMessage) (json.RawMessage, error) {
			_, canonical, err := normalizeSpecialistDelegationPayload(payload)
			return canonical, err
		}, (*Gateway).invokeSpecialistDelegation,
		requireExecutor("specialist delegation proposal", func(g *Gateway, _ ToolCall) bool { return g.delegationProposals != nil }), true, true)
	addSupervisor([]ToolDefinition{childTaskProposeDefinition},
		func(_ ToolName, payload json.RawMessage) (json.RawMessage, error) {
			_, canonical, err := normalizeChildTaskProposalPayload(payload)
			return canonical, err
		}, (*Gateway).invokeChildTaskProposal,
		requireExecutor("child task proposal", func(g *Gateway, _ ToolCall) bool { return g.childTaskProposals != nil }), true, true)
	addSupervisor([]ToolDefinition{dockerSandboxProposalDefinition},
		func(_ ToolName, payload json.RawMessage) (json.RawMessage, error) {
			_, canonical, err := normalizeDockerSandboxProposalPayload(payload)
			return canonical, err
		}, (*Gateway).invokeDockerSandboxProposal,
		requireExecutor("Docker Sandbox proposal", func(g *Gateway, _ ToolCall) bool { return g.dockerSandboxProposals != nil }), true, true)
	addSupervisor([]ToolDefinition{skillCandidateProposalDefinition},
		func(_ ToolName, payload json.RawMessage) (json.RawMessage, error) {
			_, canonical, err := normalizeSkillCandidatePayload(payload)
			return canonical, err
		}, (*Gateway).invokeSkillCandidate,
		requireExecutor("Skill candidate proposal", func(g *Gateway, _ ToolCall) bool { return g.skillCandidates != nil }), false, true)
	addSupervisor([]ToolDefinition{debugTerminalDefinition},
		func(_ ToolName, payload json.RawMessage) (json.RawMessage, error) {
			_, canonical, err := normalizeDebugTerminalPayload(payload)
			return canonical, err
		}, (*Gateway).invokeDebugTerminal,
		requireExecutor("debug terminal", func(g *Gateway, _ ToolCall) bool { return g.debugTerminal != nil }), false, true)
	addSupervisor([]ToolDefinition{commandRuntimeDefinition},
		func(_ ToolName, payload json.RawMessage) (json.RawMessage, error) {
			_, canonical, err := normalizeCommandRuntimePayload(payload)
			return canonical, err
		}, (*Gateway).invokeCommandRuntime,
		requireExecutor("command runtime", func(g *Gateway, _ ToolCall) bool { return g.commandRuntime != nil }), false, true)
	addSupervisor([]ToolDefinition{runTestsDefinition},
		func(_ ToolName, payload json.RawMessage) (json.RawMessage, error) {
			_, canonical, err := NormalizeRunTestsPayload(payload)
			return canonical, err
		}, (*Gateway).invokeRunTests,
		requireExecutor("project tests", func(g *Gateway, _ ToolCall) bool { _, ok := g.commandRuntime.(ProjectTestPreparer); return ok }), false, true)
	addSupervisor([]ToolDefinition{mcpSearchDefinition},
		func(_ ToolName, payload json.RawMessage) (json.RawMessage, error) {
			_, canonical, err := NormalizeMCPSearchPayload(payload)
			return canonical, err
		}, (*Gateway).invokeMCPSearch,
		requireExecutor("MCP catalog", func(g *Gateway, _ ToolCall) bool { return g.mcpSearch != nil }), true, true)
	addSupervisor([]ToolDefinition{MCPToolDefinition()},
		func(_ ToolName, payload json.RawMessage) (json.RawMessage, error) {
			_, canonical, err := NormalizeMCPToolPayload(payload)
			return canonical, err
		}, (*Gateway).invokeMCP,
		requireExecutor("MCP client", func(g *Gateway, _ ToolCall) bool { return g.mcp != nil }), true, true)
	addSupervisor(WebEvidenceToolDefinitions(), NormalizeWebEvidencePayload,
		(*Gateway).invokeWebEvidence,
		requireExecutor("web evidence", func(g *Gateway, _ ToolCall) bool { return g.webEvidence != nil }), true, true)
	browserInvoke := func(g *Gateway, ctx context.Context, call ToolCall) (Outcome, error) {
		if IsAgentBrowserPayload(call.Payload) {
			return g.invokeAgentBrowser(ctx, call)
		}
		return g.invokeBrowserAction(ctx, call)
	}
	browserPreflight := requireExecutor("browser action", func(g *Gateway, call ToolCall) bool {
		if IsAgentBrowserPayload(call.Payload) {
			return g.agentBrowser != nil
		}
		return g.browserActions != nil
	})
	addSupervisor(BrowserActionToolDefinitions(), NormalizeBrowserActionPayload,
		browserInvoke, browserPreflight, true, true)
	add(toolRegistration{definition: planDeliveryDefinition, valid: true, supervisor: true,
		typedAction: true, planCatalog: true,
		normalize: func(_ ToolName, payload json.RawMessage) (json.RawMessage, error) {
			_, canonical, err := normalizePlanDeliveryPayload(payload)
			return canonical, err
		}, preflight: requireExecutor("Plan/Delivery proposal", func(g *Gateway, _ ToolCall) bool { return g.planDeliveryProposals != nil }),
		invoke: (*Gateway).invokePlanDelivery})
	addSupervisor(AgentCodeToolDefinitions(), NormalizeAgentCodePayload,
		(*Gateway).invokeAgentCode,
		requireExecutor("agent code tool", func(g *Gateway, _ ToolCall) bool { return g.agentCode != nil }), true, false)
	addSupervisor(CodeIntelToolDefinitions(),
		func(name ToolName, payload json.RawMessage) (json.RawMessage, error) {
			_, canonical, err := NormalizeCodeIntelPayload(name, payload)
			return canonical, err
		}, (*Gateway).invokeCodeIntel,
		requireExecutor("code-intel tool", func(g *Gateway, _ ToolCall) bool { return g.codeIntel != nil }), true, false)
	for _, name := range []ToolName{BrowserScrollTool, BrowserKeyTool} {
		definition, found := AgentBrowserToolDefinition(name)
		if !found {
			panic(fmt.Sprintf("missing Agent Browser definition %q", name))
		}
		addSupervisor([]ToolDefinition{definition}, NormalizeBrowserActionPayload,
			browserInvoke, browserPreflight, true, false)
	}

	// Operator tools are valid gateway names, but are absent from model catalogs.
	for _, definition := range []ToolDefinition{
		{Name: ReadFileTool, Class: ClassWorkspaceRead, Approval: ApprovalAutomatic},
		{Name: ListWorkspaceTool, Class: ClassWorkspaceRead, Approval: ApprovalAutomatic},
		{Name: ShellTool, Class: ClassShell, Approval: ApprovalPerCall},
		{Name: ReplaceFileTool, Class: ClassWorkspaceWrite, Approval: ApprovalPerCall},
		{Name: ScriptProcessTool, Class: ClassProcess, Approval: ApprovalPerCall},
	} {
		registration := toolRegistration{definition: definition, valid: true,
			typedAction: definition.Name != ShellTool}
		switch definition.Name {
		case ReadFileTool, ListWorkspaceTool:
			registration.invoke = (*Gateway).invokeWorkspaceRead
		case ShellTool:
			registration.invoke = (*Gateway).invokeShellProposal
		case ReplaceFileTool:
			registration.invoke = (*Gateway).invokeFileEditProposal
		}
		// ScriptProcess retains its separate ProposeScriptProcess/Review path.
		add(registration)
	}
	// Historical proposals retain their class for reading existing records.
	// Registration grants them no validity, typed-action or invocation capability.
	for _, name := range []ToolName{ControlledCommandProposeTool,
		HostCommandProposeTool, OneShotCommandProposeTool} {
		add(toolRegistration{definition: ToolDefinition{Name: name, Class: ClassAgentProposal}})
	}
	return registry
}

func requireExecutor(label string, available func(*Gateway, ToolCall) bool) func(*Gateway, ToolCall) error {
	return func(g *Gateway, call ToolCall) error {
		if !available(g, call) {
			return fmt.Errorf("%s executor is required", label)
		}
		return nil
	}
}

func lookupTool(name ToolName) (toolRegistration, bool) {
	index, found := registeredTools.byName[name]
	if !found {
		return toolRegistration{}, false
	}
	return registeredTools.ordered[index], true
}

func copyToolDefinition(definition ToolDefinition) ToolDefinition {
	definition.InputSchema = append(json.RawMessage(nil), definition.InputSchema...)
	return definition
}

// IsSupervisorTool recognizes the static model-tool catalog. Availability and
// execution authority are decided from the current Run capability snapshot.
func IsSupervisorTool(name ToolName) bool {
	registration, found := lookupTool(name)
	return found && registration.supervisor
}

func SupervisorToolDefinitions() []ToolDefinition {
	definitions := make([]ToolDefinition, 0)
	for _, registration := range registeredTools.ordered {
		if registration.defaultCatalog {
			definitions = append(definitions, copyToolDefinition(registration.definition))
		}
	}
	return definitions
}

func PlanPhaseSupervisorToolDefinitions() []ToolDefinition {
	definitions := make([]ToolDefinition, 0)
	for _, registration := range registeredTools.ordered {
		if registration.planCatalog {
			definitions = append(definitions, copyToolDefinition(registration.definition))
		}
	}
	return definitions
}

func AllSupervisorToolDefinitions() []ToolDefinition {
	definitions := make([]ToolDefinition, 0)
	for _, registration := range registeredTools.ordered {
		if registration.supervisor {
			definitions = append(definitions, copyToolDefinition(registration.definition))
		}
	}
	return definitions
}

func SupervisorToolDefinition(name ToolName) (ToolDefinition, bool) {
	registration, found := lookupTool(name)
	if !found || !registration.supervisor {
		return ToolDefinition{}, false
	}
	return copyToolDefinition(registration.definition), true
}

func NormalizeSupervisorToolPayload(name ToolName, payload json.RawMessage) (json.RawMessage, error) {
	registration, found := lookupTool(name)
	if !found || !registration.supervisor {
		return nil, fmt.Errorf("unsupported supervisor tool %q", name)
	}
	return registration.normalize(name, payload)
}

// TypedActionIDs returns only names approved for project-config typed actions.
// Model visibility and gateway validity do not expand this configuration set.
func TypedActionIDs() map[string]struct{} {
	ids := make(map[string]struct{})
	for id := range projectCommandPresets {
		ids[id] = struct{}{}
	}
	for _, registration := range registeredTools.ordered {
		if registration.typedAction {
			ids[string(registration.definition.Name)] = struct{}{}
		}
	}
	return ids
}

func (n ToolName) Valid() bool {
	registration, found := lookupTool(n)
	return found && registration.valid
}

func ClassForTool(name ToolName) (ActionClass, bool) {
	registration, found := lookupTool(name)
	if !found {
		return "", false
	}
	return registration.definition.Class, true
}
