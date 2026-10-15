package application

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"cyberagent-workbench/internal/apperror"
	"cyberagent-workbench/internal/codeintel"
	"cyberagent-workbench/internal/commandruntimeadapter"
	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/llm"
	"cyberagent-workbench/internal/mcp"
	"cyberagent-workbench/internal/redact"
	"cyberagent-workbench/internal/runmutation"
	"cyberagent-workbench/internal/toolcontract"
	"cyberagent-workbench/internal/toolgateway"
	"cyberagent-workbench/internal/webevidence"
	"cyberagent-workbench/internal/workspace"
)

const supervisorToolResultVersion = "supervisor_tool_result.v1"

const (
	supervisorToolCallTimeout          = 30 * time.Second
	supervisorWebSearchToolCallTimeout = 60 * time.Second
)

var errSupervisorWaitingApproval = errors.New("Supervisor tool is waiting for operator approval")

func commandRuntimeAuthorityCurrent(
	capabilities domain.ExecutionPermissionRuntimeCapabilities,
	authority commandruntimeadapter.Authority,
	permission domain.RunExecutionPermissionSnapshot,
) bool {
	return authority.ProtocolVersion == commandruntimeadapter.OperationAuthorityVersion && authority.PermissionMode == permission.Mode && authority.PermissionRevision == permission.Revision && agentCodeRuntimeCurrent(capabilities, permission, authority.PermissionSnapshotID, authority.PermissionGeneration, authority.PermissionRuntimeEpoch, authority.RunAuthorizationFence)
}

type supervisorAgentCodeTools struct {
	Capabilities toolgateway.AgentCodeCapabilitySnapshot
	Authority    json.RawMessage
}

type supervisorCodeIntelTools struct {
	Capabilities toolgateway.CodeIntelCapabilitySnapshot
	Authority    json.RawMessage
}

type supervisorCommandRuntimeTools struct {
	Adapter   commandruntimeadapter.Identity
	Authority json.RawMessage
}

type supervisorMCPTools struct {
	Capabilities mcp.ScopedCapabilities
	Authority    json.RawMessage
}

type supervisorWebEvidenceTools struct {
	Capabilities toolgateway.WebEvidenceCapabilities
	Authority    json.RawMessage
}

type supervisorBrowserActionTools struct {
	Capabilities toolgateway.BrowserActionCapabilities
	Authority    json.RawMessage
}

type supervisorToolOptions struct {
	HistoryRecall      bool
	BuiltinSkills      []toolgateway.BuiltinSkillDescriptor
	OwnedFileWorkspace bool
	CommandRuntime     supervisorCommandRuntimeTools
	AgentCode          supervisorAgentCodeTools
	CodeIntel          supervisorCodeIntelTools
	MCP                supervisorMCPTools
	WebEvidence        supervisorWebEvidenceTools
	BrowserActions     supervisorBrowserActionTools
}

func supervisorDebugTerminalAvailable(surface domain.ExecutionSurface, phase domain.ExecutionPhase,
	permission domain.RunExecutionPermissionMode, enabled bool,
) bool {
	return enabled && surface == domain.ExecutionSurfaceCode &&
		phase == domain.ExecutionPhaseDeliver && permission == domain.RunExecutionPermissionFull
}

func supervisorCommandRuntimeAvailable(surface domain.ExecutionSurface, phase domain.ExecutionPhase,
	permission domain.RunExecutionPermissionMode, adapter commandruntimeadapter.Identity,
) bool {
	return adapter.Executable() && surface == domain.ExecutionSurfaceCode &&
		phase == domain.ExecutionPhaseDeliver && permission.IsApprovalMode() && adapter.AllowsPermission(permission)
}

func supervisorWebEvidenceAvailable(name toolgateway.ToolName, capabilities toolgateway.WebEvidenceCapabilities) bool {
	if !capabilities.Available {
		return false
	}
	switch name {
	case toolgateway.WebSearchTool:
		return capabilities.SearchAvailable
	case toolgateway.SourceSearchTool:
		return capabilities.SourceSearchAvailable
	default:
		return capabilities.FetchAvailable
	}
}

type supervisorToolResultEnvelope struct {
	Version   string            `json:"version"`
	Tool      string            `json:"tool"`
	Status    string            `json:"status"`
	Metadata  map[string]string `json:"metadata,omitempty"`
	Code      string            `json:"code,omitempty"`
	Message   string            `json:"message,omitempty"`
	Stdout    string            `json:"stdout,omitempty"`
	Stderr    string            `json:"stderr,omitempty"`
	Truncated bool              `json:"truncated,omitempty"`
}

func marshalSupervisorToolResultEnvelope(value supervisorToolResultEnvelope) ([]byte, error) {
	var buffer bytes.Buffer
	encoder := json.NewEncoder(&buffer)
	// Web results are durable JSON and are never embedded as HTML. Keeping
	// literal '<', '>', and '&' avoids a sixfold expansion of bounded evidence;
	// all existing non-Web result encodings retain their prior canonical form.
	if toolgateway.IsWebEvidenceTool(toolgateway.ToolName(value.Tool)) ||
		toolgateway.IsBrowserActionTool(toolgateway.ToolName(value.Tool)) {
		encoder.SetEscapeHTML(false)
	}
	if err := encoder.Encode(value); err != nil {
		return nil, err
	}
	encoded := bytes.TrimSuffix(buffer.Bytes(), []byte("\n"))
	if !json.Valid(encoded) || len(encoded) > domain.MaxSupervisorToolResultBytes {
		return nil, errors.New("supervisor tool result envelope exceeds its durable JSON limit")
	}
	return encoded, nil
}

func supervisorStructuredToolSpecs(surface domain.ExecutionSurface,
	phase domain.ExecutionPhase,
	permissionMode domain.RunExecutionPermissionMode,
	skillCandidateEnabled bool,
	debugTerminalEnabled bool,
	options ...supervisorToolOptions,
) []llm.ToolSpec {
	configured := supervisorToolOptions{}
	if len(options) > 0 {
		configured = options[0]
	}
	agentCode := toolgateway.AgentCodeCapabilitySnapshot{}
	if len(options) > 0 {
		agentCode = configured.AgentCode.Capabilities
	}
	definitions := toolgateway.SupervisorToolDefinitions()
	if phase == domain.ExecutionPhasePlan {
		definitions = toolgateway.PlanPhaseSupervisorToolDefinitions()
	}
	if configured.BrowserActions.Capabilities.Available && configured.BrowserActions.Capabilities.ProtocolVersion == toolgateway.AgentBrowserAuthorityVersion {
		for _, name := range []toolgateway.ToolName{toolgateway.BrowserScrollTool, toolgateway.BrowserKeyTool} {
			d, _ := toolgateway.AgentBrowserToolDefinition(name)
			definitions = append(definitions, d)
		}
	}
	out := make([]llm.ToolSpec, 0, len(definitions))
	for _, definition := range definitions {
		if definition.Name == toolgateway.SkillReadTool {
			if len(configured.BuiltinSkills) == 0 {
				continue
			}
			definition = toolgateway.SkillReadToolDefinition(configured.BuiltinSkills)
		}
		if toolgateway.IsHistoryRecallTool(definition.Name) && !configured.HistoryRecall {
			continue
		}
		if definition.Name == toolgateway.SkillCandidateProposeTool &&
			!skillCandidateEnabled {
			continue
		}
		if definition.Name == toolgateway.DebugTerminalTool &&
			!supervisorDebugTerminalAvailable(surface, phase, permissionMode, debugTerminalEnabled) {
			continue
		}
		if definition.Name == toolgateway.CommandRuntimeTool &&
			!supervisorCommandRuntimeAvailable(surface, phase, permissionMode, configured.CommandRuntime.Adapter) {
			continue
		}
		if definition.Name == toolgateway.CommandRuntimeTool {
			definition = toolgateway.CommandRuntimeDefinitionForAdapter(
				configured.CommandRuntime.Adapter)
		}
		if definition.Name == toolgateway.MCPToolCallTool || definition.Name == toolgateway.MCPToolSearchTool {
			if len(configured.MCP.Capabilities.Servers) == 0 ||
				len(configured.MCP.Authority) == 0 {
				continue
			}
		}
		if toolgateway.IsWebEvidenceTool(definition.Name) &&
			!supervisorWebEvidenceAvailable(definition.Name, configured.WebEvidence.Capabilities) {
			continue
		}
		if toolgateway.IsBrowserActionTool(definition.Name) &&
			!configured.BrowserActions.Capabilities.Available {
			continue
		}
		if toolgateway.IsBrowserActionTool(definition.Name) && configured.BrowserActions.Capabilities.ProtocolVersion == toolgateway.AgentBrowserAuthorityVersion {
			definition, _ = toolgateway.AgentBrowserToolDefinition(definition.Name)
		}
		out = append(out, llm.ToolSpec{
			Name: string(definition.Name), Description: definition.Description,
			Parameters: append(json.RawMessage(nil), definition.InputSchema...),
		})
	}
	for _, definition := range agentCode.VisibleDefinitions() {
		out = append(out, llm.ToolSpec{Name: string(definition.Name),
			Description: definition.Description,
			Parameters:  append(json.RawMessage(nil), definition.InputSchema...)})
	}
	if surface == domain.ExecutionSurfaceCode &&
		(phase == domain.ExecutionPhasePlan || phase == domain.ExecutionPhaseDeliver) {
		for _, definition := range configured.CodeIntel.Capabilities.VisibleDefinitions() {
			out = append(out, llm.ToolSpec{Name: string(definition.Name),
				Description: definition.Description,
				Parameters:  append(json.RawMessage(nil), definition.InputSchema...)})
		}
	}
	return out
}

func prepareSupervisorToolCalls(calls []llm.ToolCall, runID string, turn int, round int,
	surface domain.ExecutionSurface, phase domain.ExecutionPhase,
	permissionMode domain.RunExecutionPermissionMode,
	skillCandidateEnabled bool,
	debugTerminalEnabled bool,
	options ...supervisorToolOptions,
) ([]llm.ToolCall, error) {
	configured := supervisorToolOptions{}
	if len(options) > 0 {
		configured = options[0]
	}
	commandRuntimeAuthority := configured.CommandRuntime.Authority
	agentCode := toolgateway.AgentCodeCapabilitySnapshot{}
	var agentCodeAuthority json.RawMessage
	codeIntel := toolgateway.CodeIntelCapabilitySnapshot{}
	var codeIntelAuthority json.RawMessage
	webEvidence := toolgateway.WebEvidenceCapabilities{}
	var webEvidenceAuthority json.RawMessage
	browserActions := toolgateway.BrowserActionCapabilities{}
	var browserActionAuthority json.RawMessage
	if len(options) > 0 {
		agentCode = configured.AgentCode.Capabilities
		agentCodeAuthority = configured.AgentCode.Authority
		codeIntel = configured.CodeIntel.Capabilities
		codeIntelAuthority = configured.CodeIntel.Authority
		webEvidence = configured.WebEvidence.Capabilities
		webEvidenceAuthority = configured.WebEvidence.Authority
		browserActions = configured.BrowserActions.Capabilities
		browserActionAuthority = configured.BrowserActions.Authority
	}
	if len(calls) == 0 || len(calls) > domain.MaxSupervisorToolCallsPerRound {
		return nil, fmt.Errorf("supervisor tool batch must contain 1 to %d calls",
			domain.MaxSupervisorToolCallsPerRound)
	}
	normalized, err := llm.NormalizeToolCalls(calls)
	if err != nil {
		return nil, err
	}
	out := make([]llm.ToolCall, len(normalized))
	seen := make(map[string]struct{}, len(normalized))
	for index, call := range normalized {
		name := toolgateway.ToolName(call.Name)
		if !toolgateway.IsSupervisorTool(name) {
			return nil, fmt.Errorf("provider requested unsupported supervisor tool %q", call.Name)
		}
		if toolgateway.IsAgentCodeTool(name) {
			available := false
			for _, capability := range agentCode.Tools {
				if capability.Name == name && capability.Available {
					available = true
					break
				}
			}
			if !available || len(agentCodeAuthority) == 0 {
				return nil, fmt.Errorf("provider requested unavailable agent code tool %q", call.Name)
			}
		}
		if name == toolgateway.SkillReadTool {
			input, _, err := toolgateway.NormalizeSkillReadPayload(call.Arguments)
			// Installed discovery is paged. Its first summary page is not an
			// authority whitelist: the reader validates every exact enabled pin
			// against current installation, surface, Run and attempt state.
			available := len(configured.BuiltinSkills) > 0 && (input.Catalog || input.Portable())
			for _, item := range configured.BuiltinSkills {
				if item.SkillReadRequest == input.CatalogRequest() {
					available = true
					break
				}
			}
			if err != nil || !available {
				return nil, errors.New("provider requested an embedded Skill absent from the current catalog")
			}
		}
		if toolgateway.IsHistoryRecallTool(name) && !configured.HistoryRecall {
			return nil, fmt.Errorf("provider requested unavailable history recall tool %q", call.Name)
		}
		if toolgateway.IsCodeIntelTool(name) && (len(codeIntelAuthority) == 0 ||
			surface != domain.ExecutionSurfaceCode ||
			(phase != domain.ExecutionPhasePlan && phase != domain.ExecutionPhaseDeliver)) {
			return nil, fmt.Errorf("provider requested unavailable code-intel tool %q", call.Name)
		}
		if toolgateway.IsWebEvidenceTool(name) &&
			(!supervisorWebEvidenceAvailable(name, webEvidence) || len(webEvidenceAuthority) == 0) {
			return nil, fmt.Errorf("provider requested unavailable web evidence tool %q: %s",
				call.Name, supervisorWebEvidenceUnavailableReason(name, webEvidence))
		}
		if toolgateway.IsBrowserActionTool(name) && (!browserActions.Available ||
			len(browserActionAuthority) == 0) {
			return nil, fmt.Errorf("provider requested unavailable browser action %q", call.Name)
		}
		if name == toolgateway.PlanDeliveryProposeTool && phase != domain.ExecutionPhasePlan {
			return nil, errors.New("provider requested Plan/Delivery proposal outside Plan phase")
		}
		if name == toolgateway.SkillCandidateProposeTool && phase != domain.ExecutionPhaseDeliver {
			return nil, errors.New("provider requested Skill candidate proposal outside Deliver phase")
		}
		if name == toolgateway.SkillCandidateProposeTool && !skillCandidateEnabled {
			return nil, errors.New(
				"provider requested Skill candidate proposal without the explicit generator Skill")
		}
		if name == toolgateway.DebugTerminalTool &&
			!supervisorDebugTerminalAvailable(surface, phase, permissionMode, debugTerminalEnabled) {
			return nil, errors.New(
				"provider requested Debug terminal outside Code/Deliver with an active terminal input grant")
		}
		if name == toolgateway.CommandRuntimeTool &&
			!supervisorCommandRuntimeAvailable(surface, phase, permissionMode, configured.CommandRuntime.Adapter) {
			return nil, errors.New(
				"provider requested command runtime outside its advertised Code/Deliver adapter authority")
		}
		payload, err := toolgateway.NormalizeSupervisorToolPayload(name, call.Arguments)
		if err != nil {
			return nil, err
		}
		if toolgateway.IsCodeIntelTool(name) {
			input, _, _ := toolgateway.NormalizeCodeIntelPayload(name, payload)
			if !codeIntel.Available(name, input) {
				return nil, errors.New(
					"provider requested code-intel capability absent from the current reviewed snapshot")
			}
		}
		if name == toolgateway.MCPToolCallTool {
			request, _, _ := toolgateway.NormalizeMCPToolPayload(payload)
			if !supervisorMCPToolAvailable(configured.MCP.Capabilities, request) ||
				len(configured.MCP.Authority) == 0 {
				return nil, errors.New(
					"provider requested an MCP capability absent from the current reviewed snapshot")
			}
		}
		if name == toolgateway.MCPToolSearchTool &&
			(len(configured.MCP.Capabilities.Servers) == 0 || len(configured.MCP.Authority) == 0) {
			return nil, errors.New("provider requested MCP search without an authorized catalog")
		}
		operationKey := supervisorToolOperationKey(runID, turn, name, payload)
		callID, err := runmutation.SupervisorToolCallID(operationKey, round)
		if err != nil {
			return nil, err
		}
		if _, exists := seen[callID]; exists {
			return nil, errors.New("provider requested duplicate structured tool intent in one batch")
		}
		seen[callID] = struct{}{}
		out[index] = llm.ToolCall{ID: callID, Name: string(name), Arguments: payload,
			StreamResponseID: call.StreamResponseID, StreamItemID: call.StreamItemID,
			StreamCallID: call.StreamCallID}
		if toolgateway.IsAgentCodeTool(name) {
			out[index].Authority = append(json.RawMessage(nil), agentCodeAuthority...)
		}
		if toolgateway.IsCodeIntelTool(name) {
			out[index].Authority = append(json.RawMessage(nil), codeIntelAuthority...)
		}
		if name == toolgateway.CommandRuntimeTool {
			if authority, authorityErr := commandruntimeadapter.DecodeAuthority(
				commandRuntimeAuthority); authorityErr != nil ||
				authority.RunID != runID ||
				!authority.Adapter.SameBackend(configured.CommandRuntime.Adapter) {
				return nil, errors.New("command runtime advertisement authority is invalid")
			}
			out[index].Authority = append(json.RawMessage(nil), commandRuntimeAuthority...)
		}
		if name == toolgateway.MCPToolCallTool || name == toolgateway.MCPToolSearchTool {
			authority, err := mcp.DecodeSupervisorCallAuthority(configured.MCP.Authority)
			if err != nil || authority.RunID != runID ||
				(name == toolgateway.MCPToolCallTool && authority.Version == mcp.SupervisorCatalogAuthorityVersion) {
				return nil, errors.New("MCP advertisement authority is invalid")
			}
			if name == toolgateway.MCPToolSearchTool {
				authority.Version = mcp.SupervisorCatalogAuthorityVersion
				authority.ServerID, authority.DescriptorFingerprint, authority.ServerGeneration = "", "", 0
			} else if authority.Version == mcp.SupervisorOperationAuthorityVersion {
				request, _, _ := toolgateway.NormalizeMCPToolPayload(payload)
				for _, server := range configured.MCP.Capabilities.Servers {
					if server.ServerID == request.ServerID {
						authority.ServerID = server.ServerID
						authority.DescriptorFingerprint = server.DescriptorFingerprint
						authority.ServerGeneration = server.RegistrationGeneration
						break
					}
				}
			}
			out[index].Authority, err = mcp.EncodeSupervisorCallAuthority(authority)
			if err != nil {
				return nil, err
			}
		}
		if toolgateway.IsWebEvidenceTool(name) {
			authority, authorityErr := toolgateway.DecodeWebEvidenceCallAuthority(
				webEvidenceAuthority)
			if authorityErr != nil || authority.RunID != runID ||
				authority.Generation != webEvidence.Generation {
				return nil, errors.New("web evidence advertisement authority is invalid")
			}
			out[index].Authority = append(json.RawMessage(nil), webEvidenceAuthority...)
		}
		if toolgateway.IsBrowserActionTool(name) {
			if toolgateway.IsAgentBrowserPayload(out[index].Arguments) {
				a, e := toolgateway.DecodeAgentBrowserAuthority(browserActionAuthority)
				if e != nil || a.RunID != runID || a.Generation != browserActions.Generation || browserActions.ProtocolVersion != toolgateway.AgentBrowserAuthorityVersion {
					return nil, errors.New("Agent browser advertisement authority mismatch")
				}
			} else {
				a, e := toolgateway.DecodeBrowserActionCallAuthority(browserActionAuthority)
				if e != nil || a.RunID != runID || a.Generation != browserActions.Generation || a.FullCDPSessionID != browserActions.FullCDPSessionID {
					return nil, errors.New("browser action advertisement authority mismatch")
				}
			}
			out[index].Authority = append(json.RawMessage(nil), browserActionAuthority...)
		}
	}
	if err := validateSupervisorBrowserBatch(out); err != nil {
		return nil, err
	}
	return out, nil
}

// supervisorWebEvidenceNotOpenReason is the generic reason for a Run whose web
// evidence tools are all closed. It names the operator path that opens them.
const supervisorWebEvidenceNotOpenReason = "web evidence tools are not open for the current Run and agent. " +
	"Configure an eligible search provider or enable approval for an exact public HTTPS fetch. " +
	"Do not retry this tool until it is opened."

// supervisorWebEvidenceUnavailableReason keeps the stable
// "provider requested unavailable web evidence tool %q" rejection prefix and
// appends why the requested tool is not open for this Run, the operator path
// that opens it, and an explicit do-not-retry instruction. The tool-request
// repair presents the composed diagnostic to the model; without the reason the
// model retried the same unavailable web tool every round and burned one model
// call plus one protocol repair per attempt.
func supervisorWebEvidenceUnavailableReason(name toolgateway.ToolName,
	capabilities toolgateway.WebEvidenceCapabilities,
) string {
	if !capabilities.Available {
		return supervisorWebEvidenceNotOpenReason
	}
	if name == toolgateway.WebSearchTool && !capabilities.SearchAvailable {
		return "web search is not opened for the current Run: no eligible search provider is configured. " +
			"Configure a search backend or a model route with supported native search. " +
			"Do not retry web_search until it is opened. Continue with the tools currently offered."
	}
	if name == toolgateway.SourceSearchTool && !capabilities.SourceSearchAvailable {
		return "platform source search is not opened for the current Run: no eligible public source connector is configured. Configure a supported connector with a public HTTPS endpoint. Do not retry source_search until it is opened."
	}
	if name != toolgateway.WebSearchTool && name != toolgateway.SourceSearchTool &&
		!capabilities.FetchAvailable {
		return "web fetch is not opened for the current Run: the target requires a domain grant or per-call approval. " +
			"Enable web fetch approval or authorize the exact public HTTPS host. " +
			"Do not retry this tool until it is opened."
	}
	return supervisorWebEvidenceNotOpenReason
}

func (s *AgentRunner) supervisorWebEvidenceCapabilities(
	ctx context.Context, turn domain.SupervisorTurn,
	permission domain.RunExecutionPermissionSnapshot,
) (toolgateway.WebEvidenceCapabilities, json.RawMessage, error) {
	if s == nil || s.webEvidence == nil || turn.Agent.Role != domain.AgentRoleRoot {
		return toolgateway.WebEvidenceCapabilities{}, nil, nil
	}
	networkAuthority := webNetworkAuthority(turn.Mode.Scope)
	searchAuthority := webSearchAuthority(turn.Mode.Scope)
	providerFingerprint := ""
	if turn.Mode.Scope.WebAccessMode != domain.WebAccessDisabled {
		providerFingerprint = s.webEvidence.SearchProviderFingerprintForScope(ctx,
			webevidence.ExecutionScope{RunID: turn.Run.ID, MissionID: turn.Mission.ID,
				WorkspaceID: turn.Mission.WorkspaceID,
				ModelRoute:  turn.Run.Config.ModelRoute, Authority: searchAuthority})
	}
	connectorFingerprint := s.webEvidence.SourceConnectorFingerprintFor(searchAuthority)
	context := toolgateway.WebEvidenceCapabilityContext{RunID: turn.Run.ID,
		MissionID: turn.Mission.ID, SessionID: turn.Run.SessionID,
		RootAgentID: turn.Agent.ID, WorkspaceID: turn.Mission.WorkspaceID,
		Surface: turn.Mode.Surface, Phase: turn.Mode.Phase, Role: turn.Agent.Role,
		Profile: turn.Mode.Profile, PermissionMode: permission.Mode,
		ModeRevision:                    turn.Mode.Revision,
		PermissionRevision:              permission.Revision,
		WebAccessMode:                   turn.Mode.Scope.WebAccessMode,
		NetworkMode:                     networkAuthority.Mode,
		AllowedTargets:                  append([]string(nil), networkAuthority.AllowedTargets...),
		ProviderAvailable:               providerFingerprint != "",
		ProviderFingerprint:             providerFingerprint,
		SourceConnectorAvailable:        connectorFingerprint != "",
		SourceConnectorFingerprint:      connectorFingerprint,
		InlineWebFetchApprovalAvailable: s.webFetchAuthorizationSchedulerEnabled}
	snapshot := toolgateway.WebEvidenceCapabilitySnapshot(context)
	if !snapshot.Available {
		return snapshot, nil, nil
	}
	authority, err := toolgateway.NewWebEvidenceCallAuthority(context)
	if err != nil {
		return toolgateway.WebEvidenceCapabilities{}, nil, err
	}
	encoded, err := toolgateway.EncodeWebEvidenceCallAuthority(authority)
	if err != nil {
		return toolgateway.WebEvidenceCapabilities{}, nil, err
	}
	return snapshot, encoded, nil
}

func (s *AgentRunner) supervisorBrowserActionCapabilities(ctx context.Context,
	turn domain.SupervisorTurn, permission domain.RunExecutionPermissionSnapshot,
) (toolgateway.BrowserActionCapabilities, json.RawMessage, error) {
	// An operator-opened session owns this Run's browser target even after it
	// closes or loses authority. Never reinterpret its refs as ordinary browser
	// refs, or silently move a rejected action to another browser backend.
	if s != nil && s.agentBrowser != nil && turn.Agent.Role == domain.AgentRoleRoot &&
		(s.browserActions == nil || !s.browserActions.hasBrowserActionSession(turn.Run.ID)) {
		return s.agentBrowserCapabilities(ctx, turn)
	}
	if s == nil || s.browserActions == nil || turn.Agent.Role != domain.AgentRoleRoot ||
		permission.Mode != domain.RunExecutionPermissionFull {
		return toolgateway.BrowserActionCapabilities{}, nil, nil
	}
	binding, available, err := s.browserActions.browserActionBinding(ctx, turn.Run.ID)
	if err != nil {
		return toolgateway.BrowserActionCapabilities{}, nil, err
	}
	if !available || binding.executionPermission.ID != permission.ID ||
		binding.executionPermission.Revision != permission.Revision ||
		binding.executionPermission.Mode != permission.Mode {
		return toolgateway.BrowserActionCapabilities{}, nil, nil
	}
	scope := toolgateway.BrowserActionCapabilityContext{RunID: turn.Run.ID,
		MissionID: turn.Mission.ID, SessionID: turn.Run.SessionID,
		RootAgentID: turn.Agent.ID, WorkspaceID: turn.Mission.WorkspaceID,
		Surface: turn.Mode.Surface, Phase: turn.Mode.Phase, Role: turn.Agent.Role,
		Profile: turn.Mode.Profile, PermissionMode: permission.Mode,
		ModeRevision:         turn.Mode.Revision,
		PermissionSnapshotID: permission.ID, PermissionRevision: permission.Revision,
		PermissionActivation:        binding.executionActivation,
		RunAuthorizationFence:       binding.executionFence,
		FullCDPSessionID:            binding.view.SessionID,
		BrowserPermissionSnapshotID: binding.browserPermissionID,
		BrowserPermissionRevision:   binding.browserPermissionRevision,
		TargetOrigin:                binding.view.TargetOrigin, Ready: binding.view.State == FullCDPSessionReady,
		RuntimeAvailable: binding.view.RuntimeAvailable}
	snapshot := toolgateway.BrowserActionCapabilitySnapshot(scope)
	if !snapshot.Available {
		return snapshot, nil, nil
	}
	authority, err := toolgateway.NewBrowserActionCallAuthority(scope)
	if err != nil {
		return toolgateway.BrowserActionCapabilities{}, nil, err
	}
	encoded, err := toolgateway.EncodeBrowserActionCallAuthority(authority)
	if err != nil {
		return toolgateway.BrowserActionCapabilities{}, nil, err
	}
	return snapshot, encoded, nil
}

func (s *AgentRunner) supervisorMCPCapabilities(ctx context.Context,
	turn domain.SupervisorTurn, permission domain.RunExecutionPermissionSnapshot,
) (supervisorMCPTools, error) {
	if s.mcpClient == nil ||
		strings.TrimSpace(turn.Mission.WorkspaceID) == "" {
		return supervisorMCPTools{}, nil
	}
	generation, live := s.executionCapabilities.FullAccessGeneration(permission)
	if !live {
		return supervisorMCPTools{}, nil
	}
	fence := uint64(0)
	runtimeEpoch := ""
	if s.executionCapabilities.RuntimeAuthority != nil {
		runtimeEpoch = s.executionCapabilities.RuntimeAuthority.RuntimeEpoch()
		if runtimeEpoch == "" {
			return supervisorMCPTools{}, apperror.New(apperror.CodeFailedPrecondition,
				"MCP runtime identity is unavailable")
		}
		issuedFence, fenceErr := s.executionCapabilities.RuntimeAuthority.
			IssueRunAuthorizationFence(turn.Run.ID)
		if fenceErr != nil {
			return supervisorMCPTools{}, fenceErr
		}
		fence = issuedFence
	}
	capabilities, err := s.mcpClient.Capabilities(ctx, turn.Run.ID, turn.Mission.WorkspaceID)
	if err != nil {
		return supervisorMCPTools{}, apperror.Normalize(err)
	}
	if capabilities.ProtocolVersion != mcp.ClientProtocolVersion || len(capabilities.Servers) == 0 {
		return supervisorMCPTools{}, nil
	}
	authority, err := mcp.EncodeSupervisorCallAuthority(mcp.SupervisorCallAuthority{
		Version: mcp.SupervisorOperationAuthorityVersion,
		RunID:   turn.Run.ID, MissionID: turn.Mission.ID,
		WorkspaceID:          turn.Mission.WorkspaceID,
		PermissionSnapshotID: permission.ID, PermissionRevision: permission.Revision,
		PermissionMode: permission.Mode, PermissionGeneration: generation,
		RunAuthorizationFence: fence, PermissionRuntimeEpoch: runtimeEpoch,
		ServerID: capabilities.Servers[0].ServerID, DescriptorFingerprint: capabilities.Servers[0].DescriptorFingerprint,
		ServerGeneration: capabilities.Servers[0].RegistrationGeneration,
	})
	if err != nil {
		return supervisorMCPTools{}, err
	}
	return supervisorMCPTools{Capabilities: capabilities, Authority: authority}, nil
}

func runAuthorizationFenceCurrent(
	capabilities domain.ExecutionPermissionRuntimeCapabilities,
	runID string, fence uint64,
) bool {
	if capabilities.RuntimeAuthority == nil {
		return fence == 0
	}
	return capabilities.RuntimeAuthority.AllowsRunAuthorizationFence(runID, fence)
}

func supervisorMCPToolAvailable(capabilities mcp.ScopedCapabilities,
	request toolgateway.MCPToolCallPayload,
) bool {
	for _, server := range capabilities.Servers {
		if server.ServerID != request.ServerID ||
			server.CapabilityFingerprint != request.CapabilityFingerprint {
			continue
		}
		snapshot := mcp.CapabilitySnapshot{Tools: server.Tools, Resources: server.Resources, ResourceTemplates: server.ResourceTemplates, Prompts: server.Prompts}
		if _, err := mcp.ValidateInvocation(snapshot, request.Method, request.ToolName, request.Arguments); err == nil {
			return true
		}
	}
	return false
}

func (s *AgentRunner) supervisorAgentCodeCapabilities(ctx context.Context,
	turn domain.SupervisorTurn, permission domain.RunExecutionPermissionSnapshot,
) (toolgateway.AgentCodeCapabilitySnapshot, json.RawMessage, error) {
	store, ok := s.store.(AgentCodeToolStore)
	if !ok || turn.Mode.Surface != domain.ExecutionSurfaceCode ||
		strings.TrimSpace(turn.Mission.WorkspaceID) == "" {
		return toolgateway.AgentCodeCapabilitySnapshot{}, nil, nil
	}
	files, err := ResolveRunFileWorkspace(ctx, store, turn.Run, turn.Mission, s.drydocks)
	if err != nil {
		return toolgateway.AgentCodeCapabilitySnapshot{}, nil, apperror.Normalize(err)
	}
	rootFingerprint, err := workspace.AgentCodeRootFingerprint(files.Workspace.RootPath)
	if err != nil {
		return toolgateway.AgentCodeCapabilitySnapshot{}, nil, apperror.Normalize(err)
	}
	permissionSnapshotID, permissionGeneration, permissionRuntimeEpoch, runFence, live := bindAgentCodeRuntime(s.executionCapabilities, permission)
	if !live {
		return toolgateway.AgentCodeCapabilitySnapshot{}, nil, nil
	}
	scope := toolgateway.AgentCodeCapabilityContext{RunID: turn.Run.ID,
		MissionID: turn.Mission.ID, RootAgentID: turn.Agent.ID,
		WorkspaceID: turn.Mission.WorkspaceID, RootFingerprint: rootFingerprint,
		Surface: turn.Mode.Surface, Phase: turn.Mode.Phase, Role: turn.Agent.Role,
		Profile: turn.Mode.Profile, PermissionMode: permission.Mode,
		PermissionSnapshotID:   permissionSnapshotID,
		PermissionGeneration:   permissionGeneration,
		PermissionRuntimeEpoch: permissionRuntimeEpoch,
		RunAuthorizationFence:  runFence,
		ModeRevision:           turn.Mode.Revision, PermissionRevision: permission.Revision}
	snapshot := toolgateway.AgentCodeCapabilities(scope)
	authority, err := toolgateway.NewAgentCodeCallAuthority(scope, turn.Run.SessionID)
	if err != nil {
		return toolgateway.AgentCodeCapabilitySnapshot{}, nil, err
	}
	encoded, err := toolgateway.EncodeAgentCodeCallAuthority(authority)
	if err != nil {
		return toolgateway.AgentCodeCapabilitySnapshot{}, nil, err
	}
	return snapshot, encoded, nil
}

func (s *AgentRunner) supervisorCodeIntelCapabilities(ctx context.Context,
	turn domain.SupervisorTurn,
) (toolgateway.CodeIntelCapabilitySnapshot, error) {
	result := toolgateway.CodeIntelCapabilitySnapshot{ProtocolVersion: codeintel.ProtocolVersion,
		Servers: []toolgateway.CodeIntelServerCapability{}, Refusals: map[string]string{}}
	available, _ := toolgateway.CodeIntelScopeEligibility(toolgateway.AgentCodeCapabilityContext{
		Surface: turn.Mode.Surface, Phase: turn.Mode.Phase, Role: turn.Agent.Role,
		Profile: turn.Mode.Profile})
	if s.codeIntel == nil || !available || strings.TrimSpace(turn.Mission.WorkspaceID) == "" {
		return result, nil
	}
	store, ok := s.store.(AgentCodeToolStore)
	if !ok {
		return result, nil
	}
	files, err := ResolveRunFileWorkspace(ctx, store, turn.Run, turn.Mission, s.drydocks)
	if err != nil {
		return result, apperror.Normalize(err)
	}
	for _, snapshot := range s.codeIntel.Capabilities(ctx, files.Workspace.ID, files.Workspace.RootPath) {
		if snapshot.Health != codeintel.HealthHealthy {
			reason := string(snapshot.Health)
			if snapshot.LastError != "" {
				reason += ": " + snapshot.LastError
			}
			result.Refusals[snapshot.ServerID] = reason
			continue
		}
		server := toolgateway.CodeIntelServerCapability{ServerID: snapshot.ServerID,
			ServerName: snapshot.ServerName, Languages: append([]string(nil), snapshot.Languages...),
			Generation:            snapshot.Generation,
			CapabilityFingerprint: snapshot.CapabilityFingerprint}
		for _, name := range snapshot.ModelVisibleTools {
			tool := toolgateway.ToolName(name)
			if toolgateway.IsCodeIntelTool(tool) {
				server.Tools = append(server.Tools, tool)
			}
		}
		result.Servers = append(result.Servers, server)
	}
	return result, nil
}

func supervisorToolOperationKey(runID string, turn int, name toolgateway.ToolName,
	payload json.RawMessage,
) string {
	return runmutation.SupervisorToolOperationKey(runID, turn, string(name), string(payload))
}

func (s *AgentRunner) resumeSupervisorTools(ctx context.Context, turn domain.SupervisorTurn,
	rounds []domain.SupervisorToolRound, standardCode ...*standardCodeSupervisorTurn,
) ([]domain.SupervisorToolRound, bool, error) {
	var completion *standardCodeSupervisorTurn
	if len(standardCode) > 0 {
		completion = standardCode[0]
	}
	for _, round := range rounds {
		for _, call := range round.Calls {
			if call.Status != domain.SupervisorToolPending {
				if completion != nil {
					if err := completion.ObserveCall(ctx, call); err != nil {
						return rounds, false, apperror.Normalize(err)
					}
				}
				continue
			}
			if fence, ok := s.store.(interface {
				SupersedeSupervisorToolIfSteeringChanged(context.Context, domain.SupervisorCheckpoint, string) (bool, error)
			}); ok {
				superseded, err := fence.SupersedeSupervisorToolIfSteeringChanged(ctx, turn.Checkpoint, call.CallID)
				if err != nil {
					return rounds, false, apperror.Normalize(err)
				}
				if superseded {
					continue
				}
			}
			decision := standardCodeCallDecision{Allowed: true}
			var err error
			if completion != nil {
				decision, err = completion.Authorize(ctx, call)
				if err != nil {
					return rounds, false, apperror.Normalize(err)
				}
			}
			agentBrowserCall := toolgateway.IsBrowserActionTool(toolgateway.ToolName(call.ToolName)) && toolgateway.IsAgentBrowserPayload(json.RawMessage(call.PayloadJSON))
			preflightStopped := false
			if agentBrowserCall && decision.Allowed {
				waiting, denial, preflightErr := s.preflightAgentBrowserApproval(ctx, call)
				if preflightErr != nil {
					return rounds, false, preflightErr
				}
				if waiting {
					return rounds, true, nil
				}
				if denial != nil {
					decision.Allowed = false
					decision.Result = denial
					preflightStopped = true
				}
			}
			if toolgateway.ToolName(call.ToolName) == toolgateway.MCPToolCallTool && decision.Allowed {
				waiting, denial, preflightErr := s.preflightMCPApproval(ctx, call)
				if preflightErr != nil {
					return rounds, false, preflightErr
				}
				if waiting {
					return rounds, true, nil
				}
				if denial != nil {
					decision.Allowed, decision.Result, preflightStopped = false, denial, true
				}
			}
			if toolgateway.ToolName(call.ToolName) == toolgateway.CommandRuntimeTool && decision.Allowed {
				waiting, denial, preflightErr := s.preflightCommandApproval(ctx, call)
				if preflightErr != nil {
					return rounds, false, preflightErr
				}
				if waiting {
					return rounds, true, nil
				}
				if denial != nil {
					decision.Allowed, decision.Result, preflightStopped = false, denial, true
				}
			}
			fresh := true
			if !preflightStopped {
				var startedErr error
				if fence, ok := s.store.(interface {
					RecordSupervisorToolExecutionStartedWithSteering(context.Context, domain.SupervisorCheckpoint, string) (bool, bool, error)
				}); ok {
					var superseded bool
					fresh, superseded, startedErr = fence.RecordSupervisorToolExecutionStartedWithSteering(ctx, turn.Checkpoint, call.CallID)
					if superseded {
						continue
					}
				} else {
					fresh, startedErr = s.store.RecordSupervisorToolExecutionStarted(ctx, turn.Checkpoint, call.CallID)
				}
				if startedErr != nil {
					return rounds, false, apperror.Normalize(startedErr)
				}
			}
			var result domain.SupervisorToolResult
			if agentBrowserCall && !fresh {
				result = agentBrowserStoppedResult(call, "outcome_unknown", "A prior browser dispatch started without a completed receipt. Do not automatically repeat the action.", domain.SupervisorToolFailed)
			} else if toolgateway.ToolName(call.ToolName) == toolgateway.MCPToolCallTool && !fresh {
				result = mcpStoppedResult(call, "outcome_unknown", "A prior MCP dispatch started without a completed receipt. Inspect existing call evidence; do not automatically repeat the action.", nil)
			} else if commandCallOutcomeUnknown(call) && !fresh {
				result = commandPreflightResult(call, "outcome_unknown", "A prior command dispatch started without a completed receipt. Inspect the existing Run-owned jobs; do not repeat the command or stdin action.", domain.SupervisorToolFailed)
			} else if decision.Allowed {
				result, err = s.invokeSupervisorTool(ctx, turn, call)
				if err != nil {
					if errors.Is(err, errSupervisorWaitingApproval) {
						return rounds, true, nil
					}
					return rounds, false, err
				}
			} else if decision.Result != nil {
				result = *decision.Result
			} else {
				return rounds, false, apperror.New(apperror.CodeFailedPrecondition,
					"Standard Code Supervisor denial omitted its durable result")
			}
			if agentBrowserCall && result.Status == domain.SupervisorToolCompleted {
				a, checkErr := toolgateway.DecodeAgentBrowserAuthority(json.RawMessage(call.AuthorityJSON))
				if checkErr == nil {
					checkErr = s.agentBrowser.check(ctx, a)
				}
				if checkErr != nil {
					result = agentBrowserStoppedResult(call, "authority_changed", "Browser authority changed before persistence; no success is reported.", domain.SupervisorToolFailed)
				}
			}
			stored, _, err := s.store.RecordSupervisorToolResult(ctx, turn.Checkpoint, result)
			if err != nil {
				return rounds, false, apperror.Normalize(err)
			}
			if completion != nil {
				if err := completion.ObserveCall(ctx, stored); err != nil {
					return rounds, false, apperror.Normalize(err)
				}
			}
		}
	}
	stored, err := s.store.ListSupervisorToolRounds(ctx, turn.Checkpoint)
	if err != nil {
		return rounds, false, err
	}
	if completion != nil {
		for _, round := range stored {
			if err := completion.ObserveRound(ctx, round); err != nil {
				return stored, false, apperror.Normalize(err)
			}
		}
	}
	return stored, false, nil
}

func (s *AgentRunner) invokeSupervisorTool(ctx context.Context, turn domain.SupervisorTurn,
	call domain.SupervisorToolCall,
) (domain.SupervisorToolResult, error) {
	if call.AgentAttribution == domain.AgentAttributionLegacyUnknown ||
		call.AgentID == "" || call.AgentID != turn.Agent.ID ||
		call.AgentAttemptID == "" ||
		call.AgentAttemptID != turn.Checkpoint.AttemptID {
		return domain.SupervisorToolResult{}, apperror.New(
			apperror.CodeFailedPrecondition,
			"durable Supervisor tool Agent attribution does not match the active turn")
	}
	name := toolgateway.ToolName(call.ToolName)
	operationKey := supervisorToolOperationKey(call.RunID, call.Turn, name, json.RawMessage(call.PayloadJSON))
	if name == toolgateway.CommandRuntimeTool {
		operationKey = runmutation.SupervisorToolExecutionKey(call.RunID, call.Turn, call.ToolName, call.PayloadJSON, call.CallID)
	}
	if name == toolgateway.BrowserScreenshotTool {
		operationKey = supervisorBrowserScreenshotOperationKey(call)
	}
	toolCall := toolgateway.ToolCall{
		Name: name, Payload: json.RawMessage(call.PayloadJSON), OperationKey: operationKey,
		RunID: call.RunID, AgentID: call.AgentID,
		AgentAttemptID: call.AgentAttemptID, SessionID: turn.Run.SessionID,
		WorkspaceID: turn.Mission.WorkspaceID,
		LeaseID:     turn.Checkpoint.LeaseID, LeaseGeneration: turn.Checkpoint.LeaseGeneration,
		RequestedBy:    "run_supervisor",
		SupervisorTurn: call.Turn, SupervisorToolCallID: call.CallID,
	}
	if name == toolgateway.HostCommandProposeTool {
		return s.recoverHistoricalHostTool(ctx, turn, call)
	}
	if toolgateway.IsAgentCodeTool(name) || toolgateway.IsCodeIntelTool(name) {
		authority, authorityErr := toolgateway.DecodeAgentCodeCallAuthority(
			json.RawMessage(call.AuthorityJSON))
		permission, permissionErr := s.store.GetRunExecutionPermission(ctx, turn.Run.ID)
		live := permissionErr == nil && agentCodeRuntimeCurrent(s.executionCapabilities, permission, authority.PermissionSnapshotID,
			authority.PermissionGeneration, authority.PermissionRuntimeEpoch, authority.RunAuthorizationFence)
		if authorityErr != nil || authority.RunID != call.RunID ||
			permissionErr != nil || !live || permission.Mode != authority.PermissionMode ||
			permission.Revision != authority.PermissionRevision ||
			authority.RootAgentID != turn.Agent.ID || authority.SessionID != turn.Run.SessionID ||
			authority.MissionID != turn.Mission.ID ||
			authority.WorkspaceID != turn.Mission.WorkspaceID {
			return domain.SupervisorToolResult{}, apperror.New(apperror.CodeFailedPrecondition,
				"durable agent code tool authority does not match the active Supervisor turn")
		}
		toolCall.MissionID = authority.MissionID
		toolCall.RootFingerprint = authority.RootFingerprint
		toolCall.Surface = authority.Surface
		toolCall.Phase = authority.Phase
		toolCall.Role = authority.Role
		toolCall.Profile = authority.Profile
		toolCall.PermissionMode = authority.PermissionMode
		toolCall.PermissionSnapshotID = authority.PermissionSnapshotID
		toolCall.PermissionGeneration = authority.PermissionGeneration
		toolCall.PermissionRuntimeEpoch = authority.PermissionRuntimeEpoch
		toolCall.RunAuthorizationFence = authority.RunAuthorizationFence
		toolCall.ModeRevision = authority.ModeRevision
		toolCall.PermissionRevision = authority.PermissionRevision
		toolCall.CapabilityGeneration = authority.CapabilityGeneration
	}
	if name == toolgateway.CommandRuntimeTool {
		authority, authorityErr := commandruntimeadapter.DecodeAuthority(
			json.RawMessage(call.AuthorityJSON))
		permission, permissionErr := s.store.GetRunExecutionPermission(ctx, turn.Run.ID)
		if authorityErr != nil || permissionErr != nil || authority.RunID != call.RunID ||
			!authority.Adapter.AllowsPermission(permission.Mode) ||
			!commandRuntimeAuthorityCurrent(s.executionCapabilities,
				authority, permission) {
			return domain.SupervisorToolResult{}, apperror.New(apperror.CodeFailedPrecondition,
				"durable command runtime adapter authority does not match the active Supervisor turn")
		}
		toolCall.MissionID = turn.Mission.ID
		toolCall.Surface = turn.Mode.Surface
		toolCall.Phase = turn.Mode.Phase
		toolCall.Role = turn.Agent.Role
		toolCall.Profile = turn.Mode.Profile
		toolCall.PermissionMode = permission.Mode
		toolCall.ModeRevision = turn.Mode.Revision
		toolCall.PermissionRevision = permission.Revision
		toolCall.CapabilityGeneration = authority.Adapter.Generation
		toolCall.CommandRuntimeAdapter = authority.Adapter
		toolCall.PermissionSnapshotID = authority.PermissionSnapshotID
		toolCall.PermissionGeneration = authority.PermissionGeneration
		toolCall.PermissionRuntimeEpoch = authority.PermissionRuntimeEpoch
		toolCall.RunAuthorizationFence = authority.RunAuthorizationFence
		toolCall.SupervisorToolCallID = call.CallID
		toolCall.SupervisorTurn = call.Turn
	}
	if name == toolgateway.MCPToolCallTool || name == toolgateway.MCPToolSearchTool {
		authority, authorityErr := mcp.DecodeSupervisorCallAuthority(
			json.RawMessage(call.AuthorityJSON))
		permission, permissionErr := s.store.GetRunExecutionPermission(ctx, turn.Run.ID)
		generation, live := s.executionCapabilities.FullAccessGeneration(permission)
		fenceLive := authorityErr == nil &&
			mcpRuntimeAuthorityCurrent(s.executionCapabilities,
				turn.Run.ID, authority.RunAuthorizationFence, authority.PermissionRuntimeEpoch)
		validKind := name == toolgateway.MCPToolSearchTool && authority.Version == mcp.SupervisorCatalogAuthorityVersion ||
			name == toolgateway.MCPToolCallTool && authority.Version != mcp.SupervisorCatalogAuthorityVersion
		if authorityErr != nil || !validKind || permissionErr != nil ||
			authority.RunID != turn.Run.ID || authority.MissionID != turn.Mission.ID ||
			authority.WorkspaceID != turn.Mission.WorkspaceID ||
			authority.PermissionSnapshotID != permission.ID ||
			authority.PermissionRevision != permission.Revision ||
			authority.PermissionMode != permission.Mode || !live || !fenceLive ||
			authority.PermissionGeneration != generation {
			return domain.SupervisorToolResult{}, apperror.New(
				apperror.CodeFailedPrecondition,
				"durable MCP authority does not match the exact live execution permission")
		}
		toolCall.MissionID = authority.MissionID
		toolCall.Surface = turn.Mode.Surface
		toolCall.Phase = turn.Mode.Phase
		toolCall.Role = turn.Agent.Role
		toolCall.Profile = turn.Mode.Profile
		toolCall.PermissionMode = permission.Mode
		toolCall.PermissionSnapshotID = permission.ID
		toolCall.PermissionRevision = permission.Revision
		toolCall.PermissionGeneration = generation
		toolCall.PermissionRuntimeEpoch = authority.PermissionRuntimeEpoch
		toolCall.RunAuthorizationFence = authority.RunAuthorizationFence
	}
	if toolgateway.IsWebEvidenceTool(name) {
		authority, authorityErr := toolgateway.DecodeWebEvidenceCallAuthority(
			json.RawMessage(call.AuthorityJSON))
		permission, permissionErr := s.store.GetRunExecutionPermission(ctx, turn.Run.ID)
		if authorityErr != nil || authority.RunID != call.RunID ||
			permissionErr != nil || permission.Mode != authority.PermissionMode ||
			permission.Revision != authority.PermissionRevision ||
			authority.RootAgentID != turn.Agent.ID || authority.SessionID != turn.Run.SessionID ||
			authority.MissionID != turn.Mission.ID ||
			authority.WorkspaceID != turn.Mission.WorkspaceID {
			return domain.SupervisorToolResult{}, apperror.New(apperror.CodeFailedPrecondition,
				"durable web evidence authority does not match the active Supervisor turn")
		}
		toolCall.MissionID = authority.MissionID
		toolCall.Surface = authority.Surface
		toolCall.Phase = authority.Phase
		toolCall.Role = authority.Role
		toolCall.Profile = authority.Profile
		toolCall.PermissionMode = authority.PermissionMode
		toolCall.ModeRevision = authority.ModeRevision
		toolCall.PermissionRevision = authority.PermissionRevision
		toolCall.CapabilityGeneration = authority.Generation
		toolCall.ProviderFingerprint = authority.ProviderFingerprint
		toolCall.ConnectorFingerprint = authority.SourceConnectorFingerprint
	}
	if toolgateway.IsBrowserActionTool(name) && toolgateway.IsAgentBrowserPayload(json.RawMessage(call.PayloadJSON)) {
		a, e := toolgateway.DecodeAgentBrowserAuthority(json.RawMessage(call.AuthorityJSON))
		if e != nil || s.agentBrowser == nil || a.RootAgentID != turn.Agent.ID || a.RunID != turn.Run.ID || a.MissionID != turn.Mission.ID || a.SessionID != turn.Run.SessionID || a.WorkspaceID != turn.Mission.WorkspaceID || a.Surface != turn.Mode.Surface || a.Phase != turn.Mode.Phase || a.Profile != turn.Mode.Profile || a.ModeRevision != turn.Mode.Revision {
			return domain.SupervisorToolResult{}, agentBrowserUnavailable("durable Agent browser authority mismatches active turn")
		}
		if e = s.agentBrowser.check(ctx, a); e != nil {
			return domain.SupervisorToolResult{}, e
		}
		toolCall.AgentBrowserAuthority = json.RawMessage(call.AuthorityJSON)
		toolCall.MissionID = a.MissionID
		toolCall.Surface = a.Surface
		toolCall.Phase = a.Phase
		toolCall.Role = a.Role
		toolCall.Profile = a.Profile
		toolCall.PermissionMode = a.PermissionMode
		toolCall.ModeRevision = a.ModeRevision
		toolCall.PermissionSnapshotID = a.PermissionSnapshotID
		toolCall.PermissionRevision = a.PermissionRevision
		toolCall.PermissionGeneration = a.PermissionActivation
		toolCall.RunAuthorizationFence = a.RunAuthorizationFence
		toolCall.CapabilityGeneration = a.Generation
	} else if toolgateway.IsBrowserActionTool(name) {
		if s.browserActions == nil {
			return domain.SupervisorToolResult{}, apperror.New(
				apperror.CodeFailedPrecondition,
				"browser actions are not connected to the active Supervisor")
		}
		authority, authorityErr := toolgateway.DecodeBrowserActionCallAuthority(
			json.RawMessage(call.AuthorityJSON))
		permission, permissionErr := s.store.GetRunExecutionPermission(ctx, turn.Run.ID)
		binding, bindingAvailable, bindingErr := s.browserActions.browserActionBinding(
			ctx, turn.Run.ID)
		if authorityErr != nil || permissionErr != nil || bindingErr != nil ||
			!bindingAvailable || authority.RunID != call.RunID ||
			authority.RunID != turn.Run.ID || authority.MissionID != turn.Mission.ID ||
			authority.SessionID != turn.Run.SessionID ||
			authority.RootAgentID != turn.Agent.ID ||
			authority.WorkspaceID != turn.Mission.WorkspaceID ||
			authority.Surface != turn.Mode.Surface || authority.Phase != turn.Mode.Phase ||
			authority.Profile != turn.Mode.Profile || authority.Role != turn.Agent.Role ||
			authority.ModeRevision != turn.Mode.Revision ||
			authority.PermissionSnapshotID != permission.ID ||
			authority.PermissionRevision != permission.Revision ||
			authority.PermissionMode != permission.Mode ||
			authority.FullCDPSessionID != binding.view.SessionID ||
			authority.BrowserPermissionSnapshotID != binding.browserPermissionID ||
			authority.BrowserPermissionRevision != binding.browserPermissionRevision ||
			authority.PermissionActivation != binding.executionActivation ||
			authority.RunAuthorizationFence != binding.executionFence ||
			authority.TargetOrigin != binding.view.TargetOrigin {
			return domain.SupervisorToolResult{}, apperror.New(
				apperror.CodeFailedPrecondition,
				"durable browser action authority no longer matches the ready Full CDP session")
		}
		toolCall.MissionID = authority.MissionID
		toolCall.Surface = authority.Surface
		toolCall.Phase = authority.Phase
		toolCall.Role = authority.Role
		toolCall.Profile = authority.Profile
		toolCall.PermissionMode = authority.PermissionMode
		toolCall.ModeRevision = authority.ModeRevision
		toolCall.PermissionSnapshotID = authority.PermissionSnapshotID
		toolCall.PermissionRevision = authority.PermissionRevision
		toolCall.PermissionGeneration = authority.PermissionActivation
		toolCall.RunAuthorizationFence = authority.RunAuthorizationFence
		toolCall.CapabilityGeneration = authority.Generation
		toolCall.BrowserActionSessionID = authority.FullCDPSessionID
		toolCall.BrowserPermissionSnapshotID = authority.BrowserPermissionSnapshotID
		toolCall.BrowserPermissionRevision = authority.BrowserPermissionRevision
	}
	toolCall.Payload = supervisorAcceptedToolExecutionPayload(name, call.PayloadJSON)
	toolTimeout := supervisorToolExecutionTimeout(name)
	toolCtx, cancelTool := context.WithTimeout(ctx, toolTimeout)
	outcome, err := s.tools.Invoke(toolCtx, toolCall)
	toolContextErr := toolCtx.Err()
	cancelTool()
	if ctx.Err() != nil {
		return domain.SupervisorToolResult{}, apperror.Normalize(ctx.Err())
	}
	if errors.Is(toolContextErr, context.DeadlineExceeded) {
		if _, hasReceipt := mcp.InvocationReceipt(err); !hasReceipt {
			err = apperror.New(apperror.CodeDeadlineExceeded,
				fmt.Sprintf("structured supervisor tool exceeded its %s execution limit", toolTimeout))
		}
	}
	completedAt := time.Now().UTC()
	if err != nil {
		if errors.Is(err, errWebFetchWaitingApproval) {
			return domain.SupervisorToolResult{}, errSupervisorWaitingApproval
		}
		if receipt, found := mcp.InvocationReceipt(err); name == toolgateway.MCPToolCallTool && found {
			code := string(apperror.CodeOf(apperror.Normalize(err)))
			if receipt.State == toolcontract.ReceiptOutcomeUnknown {
				code = string(receipt.State)
			}
			return mcpStoppedResult(call, code, err.Error(), &receipt), nil
		}
		code := apperror.CodeOf(apperror.Normalize(err))
		if !recoverableSupervisorToolError(name, code) {
			if name == toolgateway.WorkspaceApplyTool && code == apperror.CodeInternal {
				// Retain the actual failure text without claiming uncertain file
				// effects settled. M examines the apply/checkpoint journals later.
				if _, recordErr := s.store.FailSupervisorTurn(ctx, turn.Checkpoint, boundedSupervisorToolMessage(err.Error()), 0); recordErr != nil {
					return domain.SupervisorToolResult{}, errors.Join(apperror.Normalize(err), recordErr)
				}
			}
			return domain.SupervisorToolResult{}, apperror.Normalize(err)
		}
		encoded, encodeErr := marshalSupervisorToolResultEnvelope(supervisorToolResultEnvelope{
			Version: supervisorToolResultVersion, Tool: call.ToolName, Status: string(domain.SupervisorToolFailed),
			Code: string(code), Message: boundedSupervisorToolMessage(err.Error()),
		})
		if encodeErr != nil {
			return domain.SupervisorToolResult{}, encodeErr
		}
		return domain.SupervisorToolResult{
			CallID: call.CallID, Status: domain.SupervisorToolFailed, ResultJSON: string(encoded),
			ErrorCode: string(code), CompletedAt: completedAt,
		}, nil
	}

	if outcome.Result == nil {
		return domain.SupervisorToolResult{}, apperror.New(apperror.CodeInternal,
			"structured supervisor tool returned no result")
	}
	metadata := make(map[string]string, len(outcome.Result.Metadata))
	for key, value := range outcome.Result.Metadata {
		// Replay is an execution detail that can differ when two supervisors
		// recover the same pending call concurrently. Keep the durable/provider
		// result deterministic for the semantic operation.
		if key == "replayed" {
			continue
		}
		metadata[key] = redact.String(value)
	}
	status := domain.SupervisorToolCompleted
	code := ""
	message := ""
	if !outcome.Decision.Allowed || outcome.Result.Status == toolgateway.StatusDenied {
		status = domain.SupervisorToolDenied
		code = string(apperror.CodePolicyDenied)
		message = boundedSupervisorToolMessage(outcome.Decision.Reason)
	} else if name == toolgateway.MCPToolCallTool && outcome.Result.Status == toolgateway.StatusFailed {
		status, code, message = domain.SupervisorToolFailed, "remote_tool_error", "Remote MCP tool reported an error; partial effects may have occurred."
	}
	envelope := supervisorToolResultEnvelope{
		Version: supervisorToolResultVersion, Tool: call.ToolName, Status: string(status),
		Metadata: metadata, Code: code, Message: message,
	}
	if name == toolgateway.DebugTerminalTool || name == toolgateway.CommandRuntimeTool ||
		toolgateway.IsHistoryRecallTool(name) || name == toolgateway.SkillReadTool ||
		name == toolgateway.MCPToolCallTool || name == toolgateway.MCPToolSearchTool ||
		toolgateway.IsAgentCodeTool(name) || toolgateway.IsCodeIntelTool(name) ||
		toolgateway.IsWebEvidenceTool(name) || toolgateway.IsBrowserActionTool(name) {
		if toolgateway.IsHistoryRecallTool(name) {
			// The store has already redacted and hashed the original source.
			// Redacting serialized JSON or a partial byte page changes its text.
			// Persistence rechecks the page against that source before publication.
			envelope.Stdout = outcome.Result.Stdout
		} else {
			envelope.Stdout = redact.String(outcome.Result.Stdout)
		}
		envelope.Stderr = redact.String(outcome.Result.Stderr)
		envelope.Truncated = outcome.Result.Truncated
	}
	encoded, err := marshalSupervisorToolResultEnvelope(envelope)
	if err != nil {
		return domain.SupervisorToolResult{}, err
	}
	return domain.SupervisorToolResult{
		CallID: call.CallID, Status: status, ResultJSON: string(encoded), ErrorCode: code,
		CompletedAt: completedAt,
	}, nil
}

func supervisorToolExecutionTimeout(name toolgateway.ToolName) time.Duration {
	if name == "mcp_tool_call" {
		return mcp.InvocationTimeout + time.Minute
	}
	if name == toolgateway.WebSearchTool || name == toolgateway.SourceSearchTool {
		return supervisorWebSearchToolCallTimeout
	}
	return supervisorToolCallTimeout
}

func recoverableSupervisorToolError(name toolgateway.ToolName,
	code apperror.Code,
) bool {
	switch code {
	case apperror.CodeInvalidArgument, apperror.CodeConflict,
		apperror.CodeResourceExhausted, apperror.CodeDeadlineExceeded:
		return true
	case apperror.CodeFailedPrecondition, apperror.CodeNotFound, apperror.CodePolicyDenied:
		return name == toolgateway.DebugTerminalTool || name == toolgateway.CommandRuntimeTool ||
			toolgateway.IsHistoryRecallTool(name) || name == toolgateway.SkillReadTool ||
			name == toolgateway.MCPToolCallTool || name == toolgateway.MCPToolSearchTool || toolgateway.IsAgentCodeTool(name) ||
			toolgateway.IsWebEvidenceTool(name) || toolgateway.IsBrowserActionTool(name) ||
			toolgateway.IsCodeIntelTool(name)
	case apperror.CodeUnavailable:
		return toolgateway.IsCodeIntelTool(name) || toolgateway.IsWebEvidenceTool(name) ||
			toolgateway.IsBrowserActionTool(name)
	default:
		return false
	}
}

func boundedSupervisorToolMessage(value string) string {
	value = redact.String(strings.Join(strings.Fields(strings.TrimSpace(value)), " "))
	runes := []rune(value)
	if len(runes) > 1024 {
		value = string(runes[:1024])
	}
	if value == "" {
		return "structured tool call failed"
	}
	return value
}

// Both live native pairs and cross-segment evidence use the same model-bound
// projection. Re-reading raw pages at a boundary can otherwise advance a saved
// cursor past text the model never received.
func supervisorToolContextResult(call domain.SupervisorToolCall) (string, error) {
	switch toolgateway.ToolName(call.ToolName) {
	case toolgateway.WebFetchTool:
		return supervisorWebFetchContextResult(call)
	case toolgateway.WebSearchTool:
		return supervisorWebSearchContextResult(call)
	case toolgateway.SourceSearchTool:
		return supervisorSourceSearchContextResult(call)
	default:
		return call.ResultJSON, nil
	}
}

func supervisorRequestWithToolRounds(request llm.ChatRequest,
	rounds []domain.SupervisorToolRound,
) (llm.ChatRequest, error) {
	messages := append([]llm.Message(nil), request.Messages...)
	for _, round := range rounds {
		if err := round.Validate(); err != nil {
			return llm.ChatRequest{}, err
		}
		if !round.Complete() {
			return llm.ChatRequest{}, errors.New("cannot build model context with pending supervisor tools")
		}
		calls := make([]llm.ToolCall, 0, len(round.Calls))
		results := make([]llm.ToolResult, 0, len(round.Calls))
		for _, call := range round.Calls {
			calls = append(calls, llm.ToolCall{
				ID: call.CallID, Name: call.ToolName, Arguments: json.RawMessage(call.PayloadJSON),
			})
			content, err := supervisorToolContextResult(call)
			if err != nil {
				return llm.ChatRequest{}, err
			}
			result := llm.ToolResult{
				ToolCallID: call.CallID, Content: content,
				IsError: call.Status == domain.SupervisorToolDenied || call.Status == domain.SupervisorToolFailed,
			}
			if toolgateway.IsHistoryRecallTool(toolgateway.ToolName(call.ToolName)) {
				result = llm.StoredHistoryToolResult(result)
			}
			results = append(results, result)
		}
		messages = append(messages,
			llm.Message{Role: "assistant", ToolCalls: calls},
			llm.Message{Role: "user", ToolResults: results},
		)
	}
	request.Messages = messages
	metadata := make(map[string]string, len(request.Metadata)+1)
	for key, value := range request.Metadata {
		metadata[key] = value
	}
	metadata["tool_round"] = strconv.Itoa(len(rounds))
	request.Metadata = metadata
	return request, nil
}

func supervisorToolStats(rounds []domain.SupervisorToolRound) (int, int) {
	calls := 0
	for _, round := range rounds {
		calls += len(round.Calls)
	}
	return len(rounds), calls
}
