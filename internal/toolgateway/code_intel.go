package toolgateway

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"time"
	"unicode/utf8"

	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/policy"
	"cyberagent-workbench/internal/redact"
	"cyberagent-workbench/internal/tools"
)

const (
	CodeIntelProtocolVersion = "code-intel-lsp.v1"
	maxCodeIntelResultItems  = 200

	CodeWorkspaceSymbolsTool ToolName = "code_workspace_symbols"
	CodeDocumentSymbolsTool  ToolName = "code_document_symbols"
	CodeDefinitionTool       ToolName = "code_definition"
	CodeReferencesTool       ToolName = "code_references"
	CodeImplementationTool   ToolName = "code_implementation"
	CodeHoverTool            ToolName = "code_hover"
	CodeSignatureHelpTool    ToolName = "code_signature_help"
	CodeDiagnosticsTool      ToolName = "code_diagnostics"
	CodeCallHierarchyTool    ToolName = "code_call_hierarchy"
	CodeTypeHierarchyTool    ToolName = "code_type_hierarchy"
)

type CodeIntelServerCapability struct {
	ServerID              string     `json:"server_id"`
	ServerName            string     `json:"server_name"`
	Languages             []string   `json:"languages"`
	Generation            string     `json:"generation"`
	CapabilityFingerprint string     `json:"capability_fingerprint"`
	Tools                 []ToolName `json:"tools"`
}

type CodeIntelCapabilitySnapshot struct {
	ProtocolVersion string                      `json:"protocol_version"`
	Servers         []CodeIntelServerCapability `json:"servers"`
	Refusals        map[string]string           `json:"refusals,omitempty"`
}

func (s CodeIntelCapabilitySnapshot) VisibleDefinitions() []ToolDefinition {
	definitions := make([]ToolDefinition, 0)
	for _, definition := range codeIntelDefinitions {
		choices := make([]CodeIntelServerCapability, 0)
		for _, server := range s.Servers {
			for _, tool := range server.Tools {
				if tool == definition.Name {
					choices = append(choices, server)
					break
				}
			}
		}
		if len(choices) == 0 {
			continue
		}
		copyDefinition := definition
		copyDefinition.InputSchema = codeIntelSchema(definition.Name, choices)
		definitions = append(definitions, copyDefinition)
	}
	return definitions
}

func (s CodeIntelCapabilitySnapshot) Available(name ToolName, payload CodeIntelPayload) bool {
	for _, server := range s.Servers {
		if server.ServerID != payload.ServerID || server.Generation != payload.ServerGeneration ||
			server.CapabilityFingerprint != payload.CapabilityFingerprint {
			continue
		}
		for _, tool := range server.Tools {
			if tool == name {
				return true
			}
		}
	}
	return false
}

var codeIntelDefinitions = []ToolDefinition{
	{Name: CodeWorkspaceSymbolsTool, Class: ClassWorkspaceRead, Approval: ApprovalAutomatic,
		Description: "Query reviewed language-server workspace symbols with commit, dirty-state, server-generation, and paged semantic provenance."},
	{Name: CodeDocumentSymbolsTool, Class: ClassWorkspaceRead, Approval: ApprovalAutomatic,
		Description: "Query hierarchical symbols for one exact Workspace document through a reviewed read-only language server."},
	{Name: CodeDefinitionTool, Class: ClassWorkspaceRead, Approval: ApprovalAutomatic,
		Description: "Resolve definitions at a zero-based LSP position; every returned file URI is revalidated against the Workspace."},
	{Name: CodeReferencesTool, Class: ClassWorkspaceRead, Approval: ApprovalAutomatic,
		Description: "Resolve references at a zero-based LSP position with explicit declaration inclusion and stale-evidence bindings."},
	{Name: CodeImplementationTool, Class: ClassWorkspaceRead, Approval: ApprovalAutomatic,
		Description: "Resolve implementations at a zero-based LSP position using a reviewed local language server."},
	{Name: CodeHoverTool, Class: ClassWorkspaceRead, Approval: ApprovalAutomatic,
		Description: "Read bounded hover evidence; Markdown, links, control characters, and secret-shaped output are sanitized."},
	{Name: CodeSignatureHelpTool, Class: ClassWorkspaceRead, Approval: ApprovalAutomatic,
		Description: "Read bounded signature-help evidence at a zero-based LSP position."},
	{Name: CodeDiagnosticsTool, Class: ClassWorkspaceRead, Approval: ApprovalAutomatic,
		Description: "Read bounded pull or published diagnostics for one exact file and document version."},
	{Name: CodeCallHierarchyTool, Class: ClassWorkspaceRead, Approval: ApprovalAutomatic,
		Description: "Build a bounded stable incoming/outgoing call graph with source provenance and evidence status."},
	{Name: CodeTypeHierarchyTool, Class: ClassWorkspaceRead, Approval: ApprovalAutomatic,
		Description: "Build a bounded stable supertype/subtype graph with source provenance and evidence status."},
}

func CodeIntelToolDefinitions() []ToolDefinition {
	result := make([]ToolDefinition, len(codeIntelDefinitions))
	for index, definition := range codeIntelDefinitions {
		definition.InputSchema = codeIntelSchema(definition.Name, nil)
		result[index] = definition
	}
	return result
}

func CodeIntelToolDefinition(name ToolName) (ToolDefinition, bool) {
	for _, definition := range codeIntelDefinitions {
		if definition.Name == name {
			definition.InputSchema = codeIntelSchema(name, nil)
			return definition, true
		}
	}
	return ToolDefinition{}, false
}

func IsCodeIntelTool(name ToolName) bool {
	_, found := CodeIntelToolDefinition(name)
	return found
}

// CodeIntelScopeEligibility deliberately reuses the Agent Code read boundary.
// Semantic evidence never creates a second Surface, phase, role, or profile
// authorization path.
func CodeIntelScopeEligibility(scope AgentCodeCapabilityContext) (bool, string) {
	for _, tool := range AgentCodeCapabilities(scope).Tools {
		if tool.Name == WorkspaceListTool {
			return tool.Available, tool.Refusal
		}
	}
	return false, "agent code read capability is unavailable"
}

type CodeIntelPayload struct {
	Version               string `json:"version"`
	ServerID              string `json:"server_id"`
	ServerGeneration      string `json:"server_generation"`
	CapabilityFingerprint string `json:"capability_fingerprint"`
	Path                  string `json:"path,omitempty"`
	Query                 string `json:"query,omitempty"`
	Line                  int    `json:"line,omitempty"`
	Character             int    `json:"character,omitempty"`
	Direction             string `json:"direction,omitempty"`
	IncludeDeclaration    bool   `json:"include_declaration,omitempty"`
	Limit                 int    `json:"limit"`
	Cursor                string `json:"cursor,omitempty"`
}

func NormalizeCodeIntelPayload(name ToolName, raw json.RawMessage) (
	CodeIntelPayload, json.RawMessage, error,
) {
	if !IsCodeIntelTool(name) || len(raw) == 0 || len(raw) > MaxAgentCodePayloadBytes ||
		!utf8.Valid(raw) {
		return CodeIntelPayload{}, nil, errors.New(
			"code-intel payload must be bounded UTF-8 JSON for a semantic tool")
	}
	var value CodeIntelPayload
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&value); err != nil {
		return CodeIntelPayload{}, nil, errors.New("code-intel payload does not match its schema")
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return CodeIntelPayload{}, nil, errors.New("code-intel payload contains trailing data")
	}
	if value.Version != CodeIntelProtocolVersion || !validAgentCodeIdentity(value.ServerID) ||
		!validAgentCodeDigest(value.ServerGeneration, false) ||
		!validAgentCodeDigest(value.CapabilityFingerprint, false) ||
		value.Limit < 1 || value.Limit > maxCodeIntelResultItems ||
		!validAgentCodeText(value.Cursor, 8192, true) ||
		!validAgentCodeText(value.Query, 256, true) {
		return CodeIntelPayload{}, nil, errors.New("code-intel payload binding or page is invalid")
	}
	documentTool := name != CodeWorkspaceSymbolsTool
	if documentTool != (value.Path != "") ||
		(value.Path != "" && !validAgentCodeText(value.Path, 512, false)) {
		return CodeIntelPayload{}, nil, errors.New("code-intel document path is invalid")
	}
	positionTool := name == CodeDefinitionTool || name == CodeReferencesTool ||
		name == CodeImplementationTool || name == CodeHoverTool ||
		name == CodeSignatureHelpTool || name == CodeCallHierarchyTool ||
		name == CodeTypeHierarchyTool
	if positionTool {
		if value.Line < 0 || value.Line > 10_000_000 || value.Character < 0 ||
			value.Character > 10_000_000 {
			return CodeIntelPayload{}, nil, errors.New("code-intel position is invalid")
		}
	} else if value.Line != 0 || value.Character != 0 {
		return CodeIntelPayload{}, nil, errors.New("code-intel tool does not accept a position")
	}
	switch name {
	case CodeCallHierarchyTool:
		if value.Direction != "incoming" && value.Direction != "outgoing" &&
			value.Direction != "both" {
			return CodeIntelPayload{}, nil, errors.New("call hierarchy direction is invalid")
		}
	case CodeTypeHierarchyTool:
		if value.Direction != "supertypes" && value.Direction != "subtypes" &&
			value.Direction != "both" {
			return CodeIntelPayload{}, nil, errors.New("type hierarchy direction is invalid")
		}
	default:
		if value.Direction != "" {
			return CodeIntelPayload{}, nil, errors.New("code-intel tool does not accept direction")
		}
	}
	if name != CodeReferencesTool && value.IncludeDeclaration {
		return CodeIntelPayload{}, nil, errors.New(
			"only code references accepts include_declaration")
	}
	canonical, err := json.Marshal(value)
	if err != nil {
		return CodeIntelPayload{}, nil, err
	}
	if redact.String(string(canonical)) != string(canonical) {
		return CodeIntelPayload{}, nil, errors.New("code-intel payload contains secret-like material")
	}
	return value, canonical, nil
}

func codeIntelSchema(name ToolName, servers []CodeIntelServerCapability) json.RawMessage {
	baseProperties := map[string]any{
		"version":                map[string]any{"const": CodeIntelProtocolVersion},
		"server_id":              map[string]any{"type": "string", "minLength": 1, "maxLength": 256},
		"server_generation":      map[string]any{"type": "string", "minLength": 64, "maxLength": 64},
		"capability_fingerprint": map[string]any{"type": "string", "minLength": 64, "maxLength": 64},
		"limit":                  map[string]any{"type": "integer", "minimum": 1, "maximum": maxCodeIntelResultItems},
		"cursor":                 map[string]any{"type": "string", "maxLength": 8192},
	}
	required := []string{"version", "server_id", "server_generation",
		"capability_fingerprint", "limit"}
	if name == CodeWorkspaceSymbolsTool {
		baseProperties["query"] = map[string]any{"type": "string", "maxLength": 256}
		required = append(required, "query")
	} else {
		baseProperties["path"] = map[string]any{"type": "string", "minLength": 1,
			"maxLength": 512}
		required = append(required, "path")
	}
	positionTool := name == CodeDefinitionTool || name == CodeReferencesTool ||
		name == CodeImplementationTool || name == CodeHoverTool ||
		name == CodeSignatureHelpTool || name == CodeCallHierarchyTool ||
		name == CodeTypeHierarchyTool
	if positionTool {
		baseProperties["line"] = map[string]any{"type": "integer", "minimum": 0}
		baseProperties["character"] = map[string]any{"type": "integer", "minimum": 0}
		required = append(required, "line", "character")
	}
	if name == CodeReferencesTool {
		baseProperties["include_declaration"] = map[string]any{"type": "boolean"}
		required = append(required, "include_declaration")
	}
	if name == CodeCallHierarchyTool {
		baseProperties["direction"] = map[string]any{"enum": []string{"incoming", "outgoing", "both"}}
		required = append(required, "direction")
	}
	if name == CodeTypeHierarchyTool {
		baseProperties["direction"] = map[string]any{"enum": []string{"supertypes", "subtypes", "both"}}
		required = append(required, "direction")
	}
	makeSchema := func(server *CodeIntelServerCapability) map[string]any {
		properties := make(map[string]any, len(baseProperties))
		for key, value := range baseProperties {
			properties[key] = value
		}
		if server != nil {
			properties["server_id"] = map[string]any{"const": server.ServerID}
			properties["server_generation"] = map[string]any{"const": server.Generation}
			properties["capability_fingerprint"] = map[string]any{
				"const": server.CapabilityFingerprint}
		}
		return map[string]any{"type": "object", "additionalProperties": false,
			"required": required, "properties": properties}
	}
	var value any = makeSchema(nil)
	if len(servers) > 0 {
		choices := make([]map[string]any, 0, len(servers))
		for index := range servers {
			choices = append(choices, makeSchema(&servers[index]))
		}
		value = map[string]any{"type": "object", "oneOf": choices}
	}
	raw, _ := json.Marshal(value)
	return raw
}

type CodeIntelExecutionResult struct {
	JSON     string
	Metadata map[string]string
}

type CodeIntelExecutor interface {
	ExecuteCodeIntel(context.Context, AgentCodeExecutionScope, ToolName,
		json.RawMessage) (CodeIntelExecutionResult, error)
}

func (g *Gateway) WithCodeIntelExecutor(executor CodeIntelExecutor) *Gateway {
	if g != nil {
		g.codeIntel = executor
	}
	return g
}

func (g *Gateway) invokeCodeIntel(ctx context.Context, call ToolCall) (Outcome, error) {
	_, canonical, err := NormalizeCodeIntelPayload(call.Name, call.Payload)
	if err != nil {
		return Outcome{}, err
	}
	call.Payload = canonical
	workspaceID, root, err := g.bindAgentCodeWorkspace(ctx, call)
	if err != nil {
		return Outcome{}, err
	}
	call.WorkspaceRoot = root
	if !validAgentCodeDigest(call.RootFingerprint, false) {
		return Outcome{}, errors.New("code-intel tool is missing its Go-issued root fingerprint")
	}
	rootAgentID, catalogueRole := call.AgentID, call.Role
	if call.RequestedBy == "specialist_runner" {
		rootAgentID, catalogueRole = call.RootAgentID, domain.AgentRoleRoot
	}
	if available, reason := CodeIntelScopeEligibility(AgentCodeCapabilityContext{
		RunID: call.RunID, MissionID: call.MissionID, RootAgentID: rootAgentID,
		WorkspaceID: call.WorkspaceID, RootFingerprint: call.RootFingerprint,
		Surface: call.Surface, Phase: call.Phase, Role: catalogueRole, Profile: call.Profile,
		PermissionMode: call.PermissionMode, ModeRevision: call.ModeRevision,
		PermissionSnapshotID: call.PermissionSnapshotID, PermissionGeneration: call.PermissionGeneration,
		PermissionRuntimeEpoch: call.PermissionRuntimeEpoch, RunAuthorizationFence: call.RunAuthorizationFence,
		PermissionRevision: call.PermissionRevision,
	}); !available {
		return deniedOutcome(call, policy.Decision{Allowed: false, Reason: reason, Risk: "high"})
	}
	policyDecision := g.checker.CheckToolCall(tools.Call{Name: string(call.Name),
		Args: map[string]string{"payload": string(canonical)}, WorkingDir: root})
	if !policyDecision.Allowed {
		return deniedOutcome(call, policyDecision)
	}
	if policyDecision.NeedsApproval {
		policyDecision.Allowed = false
		policyDecision.Reason = "read-only code-intel tool required unsupported pre-approval: " +
			policyDecision.Reason
		return deniedOutcome(call, policyDecision)
	}
	decision, err := gatewayDecision(policyDecision, ApprovalAutomatic, "low")
	if err != nil {
		return Outcome{}, err
	}
	scope := AgentCodeExecutionScope{InvocationID: call.InvocationID, SupervisorToolCallID: call.SupervisorToolCallID,
		OperationKey: call.OperationKey, RunID: call.RunID, MissionID: call.MissionID,
		RootAgentID: rootAgentID, SessionID: call.SessionID, WorkspaceID: workspaceID,
		SourceWorkspaceID: call.WorkspaceID,
		WorkspaceRoot:     root, RootFingerprint: call.RootFingerprint,
		Surface: call.Surface, Phase: call.Phase, Role: call.Role, Profile: call.Profile,
		PermissionMode: call.PermissionMode, ModeRevision: call.ModeRevision,
		PermissionSnapshotID:   call.PermissionSnapshotID,
		PermissionGeneration:   call.PermissionGeneration,
		PermissionRuntimeEpoch: call.PermissionRuntimeEpoch,
		RunAuthorizationFence:  call.RunAuthorizationFence,
		PermissionRevision:     call.PermissionRevision,
		CapabilityGeneration:   call.CapabilityGeneration, LeaseID: call.LeaseID,
		LeaseGeneration: call.LeaseGeneration, RequestedBy: call.RequestedBy,
		PolicyDecision: decision}
	if call.RequestedBy == "specialist_runner" {
		scope.AgentID = call.AgentID
		scope.AgentAttemptID = call.AgentAttemptID
		scope.SpecialistToolCallID = call.SpecialistToolCallID
	}
	if err := scope.Validate(); err != nil {
		return Outcome{}, err
	}
	started := time.Now().UTC()
	result, err := g.codeIntel.ExecuteCodeIntel(ctx, scope, call.Name, canonical)
	completed := time.Now().UTC()
	if err != nil {
		return Outcome{}, err
	}
	stdout := redact.String(strings.ToValidUTF8(result.JSON, "?"))
	stdout, truncated := boundResultText(stdout, MaxResultStdoutBytes)
	metadata := make(map[string]string, len(result.Metadata)+4)
	for key, value := range result.Metadata {
		metadata[key] = redact.String(value)
	}
	metadata["registry"] = CodeIntelProtocolVersion
	metadata["capability_generation"] = call.CapabilityGeneration
	metadata["root_fingerprint"] = call.RootFingerprint
	artifactMetadata, captureErr := g.captureTerminalArtifacts(ctx, call, call.InvocationID,
		stdout, "", "application/json")
	for key, value := range artifactMetadata {
		metadata[key] = value
	}
	outcome := Outcome{Call: safeToolCall(call), Decision: decision,
		Execution: &Execution{Backend: "go_owned_lsp_readonly", Status: StatusCompleted,
			StartedAt: started, CompletedAt: &completed},
		Result: &Result{Status: StatusCompleted, Stdout: stdout, ExitCode: 0,
			MIME: "application/json", Truncated: truncated, Metadata: metadata,
			CompletedAt: completed}}
	return validateOutcome(outcome, captureErr)
}
