package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"unicode/utf8"

	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/toolgateway"
)

// toolCatalog is the fixed v1 typed-action set. Every entry forwards into
// the existing Tool Gateway; Policy, Approval, budgets, and redaction stay
// authoritative. Unimplemented tools are never published.
func (s *Server) toolCatalog() []ToolDefinition {
	return []ToolDefinition{
		{Name: "read_file", Description: "Read one bounded workspace file through the existing Tool Gateway (Policy/redaction/budgets stay authoritative).",
			InputSchema: json.RawMessage(`{"type":"object","additionalProperties":false,"required":["path"],"properties":{"path":{"type":"string","minLength":1,"maxLength":2048}}}`)},
		{Name: "list_workspace", Description: "List one bounded workspace directory through the existing Tool Gateway.",
			InputSchema: json.RawMessage(`{"type":"object","additionalProperties":false,"properties":{"path":{"type":"string","minLength":1,"maxLength":2048}}}`)},
	}
}

func (s *Server) serveToolsList(_ context.Context, request Request, output io.Writer) {
	result := ListToolsResult{Tools: s.toolCatalog()}
	_ = s.write(output, Envelope{JSONRPC: "2.0", ID: request.ID, Result: serverResult(request, result)})
}

func (s *Server) serveToolsCall(ctx context.Context, request Request, output io.Writer) {
	params, err := DecodeCallToolParams(request.Params)
	if err != nil {
		_ = s.write(output, Envelope{JSONRPC: "2.0", ID: request.ID,
			Error: &RPCError{Code: CodeInvalidParams, Message: err.Error()}})
		return
	}
	name := toolgateway.ToolName(params.Name)
	if name != toolgateway.ReadFileTool && name != toolgateway.ListWorkspaceTool {
		_ = s.write(output, Envelope{JSONRPC: "2.0", ID: request.ID,
			Error: &RPCError{Code: CodeMethodNotFound, Message: "tool not found in this server"}})
		return
	}
	// The client can only supply the closed tool payload; an arbitrary
	// executable, path, credential, or permission tier has no field to ride on.
	arguments, err := decodeWorkspaceReadArguments(name, params.Arguments)
	if err != nil {
		_ = s.write(output, Envelope{JSONRPC: "2.0", ID: request.ID,
			Error: &RPCError{Code: CodeInvalidParams, Message: err.Error()}})
		return
	}
	// Workspace reads use the Gateway's Arguments contract. The Gateway owns
	// invocation identity and budget charging; a transport request id is not one.
	clientName := request.clientName
	if !request.modern {
		s.mu.Lock()
		clientName = s.clientName
		s.mu.Unlock()
	}
	call := toolgateway.ToolCall{Name: name, Arguments: arguments,
		RunID: s.runID, SessionID: "", WorkspaceID: s.workspaceID,
		RequestedBy: "mcp:" + clientName}
	run, err := s.store.GetRun(ctx, s.runID)
	if err != nil {
		_ = s.write(output, Envelope{JSONRPC: "2.0", ID: request.ID,
			Error: &RPCError{Code: CodeInternalError, Message: "Run scope unavailable"}})
		return
	}
	call.SessionID = run.SessionID
	outcome, err := s.tools.Invoke(ctx, call)
	if err != nil {
		reason := "invocation_failed"
		message := "tool invocation failed"
		if errors.Is(err, context.DeadlineExceeded) {
			reason = "timed_out"
			message = "tool call timed out"
		} else if errors.Is(err, context.Canceled) {
			reason = "cancelled"
			message = "tool call cancelled"
		}
		_ = s.write(output, Envelope{JSONRPC: "2.0", ID: request.ID,
			Error: &RPCError{Code: CodeInternalError, Message: message}})
		_ = s.store.RecordMCPAudit(context.WithoutCancel(ctx), s.runID, "mcp.tool_denied", map[string]any{
			"tool": params.Name, "reason": reason})
		return
	}
	if outcome.Result == nil {
		_ = s.write(output, Envelope{JSONRPC: "2.0", ID: request.ID,
			Error: &RPCError{Code: CodeInternalError, Message: outcome.Decision.Reason}})
		_ = s.store.RecordMCPAudit(ctx, s.runID, "mcp.tool_denied", map[string]any{
			"tool": params.Name, "reason": outcome.Decision.Reason})
		return
	}
	// Only the bounded redacted status and metadata travel back; raw tool
	// output bodies stay inside the gateway redaction boundary.
	text := fmt.Sprintf(`{"status":%q,"exit_code":%d,"summary":%q}`,
		outcome.Result.Status, outcome.Result.ExitCode, outcome.Result.Metadata["summary"])
	if len(outcome.Result.Metadata) > 0 {
		encoded, err := json.Marshal(outcome.Result.Metadata)
		if err == nil && len(encoded) <= 8192 {
			text = fmt.Sprintf(`{"status":%q,"exit_code":%d,"metadata":%s}`,
				outcome.Result.Status, outcome.Result.ExitCode, encoded)
		}
	}
	_ = s.write(output, Envelope{JSONRPC: "2.0", ID: request.ID,
		Result: serverResult(request, CallToolResult{Content: []TextContent{{Type: "text", Text: text}},
			IsError: outcome.Result.Status != toolgateway.StatusCompleted})})
	_ = s.store.RecordMCPAudit(context.WithoutCancel(ctx), s.runID, "mcp.tool_completed", map[string]any{
		"tool": params.Name, "status": outcome.Result.Status})
}

func decodeWorkspaceReadArguments(name toolgateway.ToolName, raw json.RawMessage) (map[string]string, error) {
	var arguments map[string]string
	if err := json.Unmarshal(raw, &arguments); err != nil || arguments == nil {
		return nil, errors.New("workspace read arguments must be a JSON object of strings")
	}
	for key, value := range arguments {
		if key != "path" {
			return nil, errors.New("workspace read arguments only accept path")
		}
		if value == "" || utf8.RuneCountInString(value) > 2048 {
			return nil, errors.New("workspace read path must contain 1 to 2048 characters")
		}
	}
	if name == toolgateway.ReadFileTool && arguments["path"] == "" {
		return nil, errors.New("read_file requires path")
	}
	return arguments, nil
}

var _ = domain.ValidAgentID
var _ = fmt.Sprintf
