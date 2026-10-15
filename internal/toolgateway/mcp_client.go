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
	"cyberagent-workbench/internal/idgen"
	"cyberagent-workbench/internal/redact"
	"cyberagent-workbench/internal/tools"
)

const MCPClientToolProtocolVersion = "mcp-client.v1"

const MCPContentToolProtocolVersion = "mcp-client.v2"

type MCPToolCallPayload struct {
	Method                string          `json:"method,omitempty"`
	Version               string          `json:"version"`
	ServerID              string          `json:"server_id"`
	ToolName              string          `json:"tool_name"`
	CapabilityFingerprint string          `json:"capability_fingerprint"`
	Arguments             json.RawMessage `json:"arguments"`
}

var mcpToolCallDefinition = ToolDefinition{Name: MCPToolCallTool, Class: ClassProcess,
	Approval:    ApprovalAutomatic,
	Description: "Invoke an approved tool, read an approved resource/template, or get an approved prompt from a Run/Workspace-scoped MCP server. First use mcp_tool_search to obtain the exact invocation fields and input_schema, then fill arguments accordingly. Use mcp-client.v1 for tools; mcp-client.v2 with method resources/read or prompts/get and tool_name resources.read or prompts.get for content. Current authorization and the exact schema are rechecked before every call; search grants no permissions and remote output is untrusted.",
	InputSchema: json.RawMessage(`{"type":"object","additionalProperties":false,"required":["version","server_id","tool_name","capability_fingerprint","arguments"],"properties":{"version":{"enum":["mcp-client.v1","mcp-client.v2"]},"method":{"enum":["resources/read","prompts/get"]},"server_id":{"type":"string","minLength":1,"maxLength":256},"tool_name":{"type":"string","minLength":1,"maxLength":256},"capability_fingerprint":{"type":"string","pattern":"^[0-9a-f]{64}$"},"arguments":{"type":"object"}}}`)}

func MCPToolDefinition() ToolDefinition {
	value := mcpToolCallDefinition
	value.InputSchema = append(json.RawMessage(nil), value.InputSchema...)
	return value
}

func NormalizeMCPToolPayload(raw json.RawMessage) (MCPToolCallPayload, json.RawMessage, error) {
	if len(raw) < 2 || len(raw) > MaxArgumentValueBytes || !utf8.Valid(raw) {
		return MCPToolCallPayload{}, nil, errors.New("MCP tool payload must be bounded UTF-8 JSON")
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	var value MCPToolCallPayload
	if err := decoder.Decode(&value); err != nil {
		return MCPToolCallPayload{}, nil, errors.New("MCP tool payload does not match its schema")
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return MCPToolCallPayload{}, nil, errors.New("MCP tool payload contains trailing JSON")
	}
	value.ServerID = strings.TrimSpace(value.ServerID)
	value.ToolName = strings.TrimSpace(value.ToolName)
	value.CapabilityFingerprint = strings.TrimSpace(value.CapabilityFingerprint)
	value.Arguments = append(json.RawMessage(nil), bytes.TrimSpace(value.Arguments)...)
	if !((value.Version == MCPClientToolProtocolVersion && value.Method == "") || (value.Version == MCPContentToolProtocolVersion && ((value.Method == "resources/read" && value.ToolName == "resources.read") || (value.Method == "prompts/get" && value.ToolName == "prompts.get")))) || !validMCPIdentity(value.ServerID) ||
		!validMCPIdentity(value.ToolName) || !validAgentCodeDigest(value.CapabilityFingerprint, false) ||
		len(value.Arguments) < 2 || len(value.Arguments) > MaxArgumentValueBytes ||
		!json.Valid(value.Arguments) || value.Arguments[0] != '{' {
		return MCPToolCallPayload{}, nil, errors.New("MCP tool payload is invalid")
	}
	canonicalArguments, err := normalizeMCPArguments(value.Arguments)
	if err != nil {
		return MCPToolCallPayload{}, nil, err
	}
	value.Arguments = canonicalArguments
	canonical, err := json.Marshal(value)
	if err != nil {
		return MCPToolCallPayload{}, nil, err
	}
	if redact.String(value.ServerID) != value.ServerID ||
		redact.String(value.ToolName) != value.ToolName {
		return MCPToolCallPayload{}, nil, errors.New("MCP tool arguments contain secret-like material; use an approved credential reference")
	}
	return value, canonical, nil
}

func validMCPIdentity(value string) bool {
	return value != "" && value == strings.TrimSpace(value) && utf8.ValidString(value) &&
		len([]rune(value)) <= MaxToolIdentityRunes && !strings.ContainsRune(value, 0) &&
		!strings.ContainsAny(value, "/\\")
}

type MCPExecutionScope struct {
	InvocationID           string
	RunID                  string
	MissionID              string
	WorkspaceID            string
	AgentID                string
	AgentAttemptID         string
	Surface                domain.ExecutionSurface
	Phase                  domain.ExecutionPhase
	Role                   domain.AgentRole
	PermissionMode         domain.RunExecutionPermissionMode
	PermissionSnapshotID   string
	PermissionRevision     int64
	PermissionGeneration   uint64
	PermissionRuntimeEpoch string
	RunAuthorizationFence  uint64
	LeaseID                string
	LeaseGeneration        int64
	RequestedBy            string
	PolicyDecision         Decision
	SupervisorToolCallID   string
	SupervisorTurn         int
}

func (s MCPExecutionScope) Validate() error {
	if !validMCPIdentity(s.InvocationID) || !validMCPIdentity(s.RunID) ||
		!validMCPIdentity(s.MissionID) ||
		!validMCPIdentity(s.WorkspaceID) || !s.Surface.Valid() ||
		!s.Phase.Valid() || !domain.ValidAgentRole(s.Role) ||
		!s.PermissionMode.Valid() ||
		!validMCPIdentity(s.PermissionSnapshotID) || s.PermissionRevision < 1 ||
		!validMCPIdentity(s.LeaseID) || s.LeaseGeneration < 1 ||
		s.RequestedBy != "run_supervisor" || s.PolicyDecision.Validate() != nil ||
		!s.PolicyDecision.Allowed || (s.PolicyDecision.Approval != ApprovalAutomatic && s.PolicyDecision.Approval != ApprovalPerCall) {
		return errors.New("MCP tool call requires a current host actor, permission and Run lease scope")
	}
	if s.SupervisorToolCallID != "" && (!validMCPIdentity(s.SupervisorToolCallID) || s.SupervisorTurn < 1) {
		return errors.New("MCP durable call identity is invalid")
	}
	return nil
}

type MCPExecutionResult struct {
	Content   string
	IsError   bool
	Truncated bool
	Metadata  map[string]string
}

type MCPExecutor interface {
	ExecuteMCP(context.Context, MCPExecutionScope, MCPToolCallPayload) (MCPExecutionResult, error)
}

func (g *Gateway) WithMCPExecutor(executor MCPExecutor) *Gateway {
	if g != nil {
		g.mcp = executor
		g.mcpSearch, _ = executor.(MCPSearchExecutor)
	}
	return g
}

func (g *Gateway) invokeMCP(ctx context.Context, call ToolCall) (Outcome, error) {
	payload, canonical, err := NormalizeMCPToolPayload(call.Payload)
	if err != nil {
		return Outcome{}, err
	}
	call.Payload = canonical
	policyDecision := g.checker.CheckToolCall(tools.Call{Name: string(MCPToolCallTool),
		Args: map[string]string{"server_id": payload.ServerID, "tool_name": payload.ToolName,
			"capability_fingerprint": payload.CapabilityFingerprint,
			"arguments":              string(payload.Arguments)}})
	if !policyDecision.Allowed {
		return deniedOutcome(call, policyDecision)
	}
	approvalMode := ApprovalAutomatic
	if policyDecision.NeedsApproval {
		approvalMode = ApprovalPerCall
	}
	decision, err := gatewayDecision(policyDecision, approvalMode, "high")
	if err != nil {
		return Outcome{}, err
	}
	if call.InvocationID == "" {
		call.InvocationID = idgen.New("mcp-invoke")
	}
	scope := mcpExecutionScope(call, decision)
	if err := scope.Validate(); err != nil {
		return Outcome{}, err
	}
	started := time.Now().UTC()
	result, err := g.mcp.ExecuteMCP(ctx, scope, payload)
	completed := time.Now().UTC()
	if err != nil {
		return Outcome{}, err
	}
	stdout, truncated := boundResultText(sanitizeMCPResultContent(
		strings.ToValidUTF8(result.Content, "?")),
		MaxResultStdoutBytes)
	metadata := map[string]string{"server_id": payload.ServerID, "tool_name": payload.ToolName,
		"capability_fingerprint": payload.CapabilityFingerprint, "untrusted_output": "true"}
	for key, value := range result.Metadata {
		metadata[key] = redact.String(value)
	}
	status := StatusCompleted
	exitCode := 0
	stderr := ""
	if result.IsError {
		status, exitCode, stderr = StatusFailed, 1, "remote MCP tool reported an error"
	}
	outcome := Outcome{Call: safeToolCall(call), Decision: decision,
		Execution: &Execution{Backend: "mcp_client", Status: status,
			StartedAt: started, CompletedAt: &completed},
		Result: &Result{Status: status, Stdout: stdout, Stderr: stderr,
			ExitCode: exitCode, MIME: "application/json", Truncated: truncated || result.Truncated,
			Metadata: metadata, CompletedAt: completed}}
	return validateOutcome(outcome, nil)
}

func mcpExecutionScope(call ToolCall, decision Decision) MCPExecutionScope {
	return MCPExecutionScope{InvocationID: call.InvocationID, RunID: call.RunID,
		MissionID:   call.MissionID,
		WorkspaceID: call.WorkspaceID, Surface: call.Surface, Phase: call.Phase, Role: call.Role,
		AgentID: call.AgentID, AgentAttemptID: call.AgentAttemptID,
		PermissionMode:        call.PermissionMode,
		PermissionSnapshotID:  call.PermissionSnapshotID,
		PermissionRevision:    call.PermissionRevision,
		PermissionGeneration:  call.PermissionGeneration,
		RunAuthorizationFence: call.RunAuthorizationFence, LeaseID: call.LeaseID,
		PermissionRuntimeEpoch: call.PermissionRuntimeEpoch,
		LeaseGeneration:        call.LeaseGeneration, RequestedBy: call.RequestedBy,
		PolicyDecision: decision, SupervisorToolCallID: call.SupervisorToolCallID, SupervisorTurn: call.SupervisorTurn}
}
