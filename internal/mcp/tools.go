package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	iofs "io/fs"
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
		switch {
		case errors.Is(err, context.DeadlineExceeded):
			reason = "timed_out"
			message = "tool call timed out"
		case errors.Is(err, context.Canceled):
			reason = "cancelled"
			message = "tool call cancelled"
		case errors.Is(err, iofs.ErrNotExist):
			// Mirror the tools-layer wording without leaking the host's
			// absolute paths into external client sessions.
			kind := "file"
			if name == toolgateway.ListWorkspaceTool {
				kind = "directory"
			}
			message = fmt.Sprintf("%s does not exist in this workspace: %s", kind, arguments["path"])
		default:
			message = "tool invocation failed: " + err.Error()
		}
		// Tool execution failures are tool results with isError, not
		// protocol-level errors: the calling model must be able to observe
		// the failure and react (MCP tools specification).
		_ = s.write(output, Envelope{JSONRPC: "2.0", ID: request.ID,
			Result: serverResult(request, CallToolResult{Content: []TextContent{{Type: "text", Text: message}},
				IsError: true})})
		_ = s.store.RecordMCPAudit(context.WithoutCancel(ctx), s.runID, "mcp.tool_denied", map[string]any{
			"tool": params.Name, "reason": reason})
		return
	}
	if outcome.Result == nil {
		_ = s.write(output, Envelope{JSONRPC: "2.0", ID: request.ID,
			Result: serverResult(request, CallToolResult{Content: []TextContent{{Type: "text",
				Text: "tool call denied by policy: " + outcome.Decision.Reason}}, IsError: true})})
		_ = s.store.RecordMCPAudit(ctx, s.runID, "mcp.tool_denied", map[string]any{
			"tool": params.Name, "reason": outcome.Decision.Reason})
		return
	}
	// The bounded, redacted tool output travels back with the status: the
	// published read_file/list_workspace tools cannot fulfill their contract
	// for external MCP clients when the content is discarded. Redaction and
	// byte bounds applied inside the Gateway remain authoritative.
	payload := map[string]any{"status": outcome.Result.Status, "exit_code": outcome.Result.ExitCode}
	if outcome.Result.Stdout != "" {
		payload["stdout"] = outcome.Result.Stdout
	}
	if len(outcome.Result.Metadata) > 0 {
		payload["metadata"] = outcome.Result.Metadata
	}
	encoded, encodeErr := json.Marshal(payload)
	if encodeErr != nil {
		encoded = []byte(fmt.Sprintf(`{"status":%q,"exit_code":%d}`,
			outcome.Result.Status, outcome.Result.ExitCode))
	}
	_ = s.write(output, Envelope{JSONRPC: "2.0", ID: request.ID,
		Result: serverResult(request, CallToolResult{Content: []TextContent{{Type: "text", Text: string(encoded)}},
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
