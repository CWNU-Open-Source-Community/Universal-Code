package mcp

import (
	"context"
	"encoding/json"
	"io"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

var serverProtocolVersions = []string{"2026-07-28", "2025-11-25", "2025-06-18", "2025-03-26", "2024-11-05"}

func prepareServerRequest(request *Request) *RPCError {
	var params struct {
		Meta map[string]json.RawMessage `json:"_meta"`
	}
	if len(request.Params) != 0 && json.Unmarshal(request.Params, &params) != nil {
		return &RPCError{Code: CodeInvalidParams, Message: "MCP request parameters must be an object"}
	}
	if raw, present := params.Meta[sdk.MetaKeyProtocolVersion]; present {
		var version string
		if json.Unmarshal(raw, &version) != nil {
			return &RPCError{Code: CodeInvalidParams, Message: "invalid MCP protocol metadata"}
		}
		if version != preferredClientProtocolVersion {
			return &RPCError{Code: sdk.CodeUnsupportedProtocolVersion, Message: "unsupported per-request MCP protocol version",
				Data: mustJSON(sdk.UnsupportedProtocolVersionData{Supported: serverProtocolVersions, Requested: version})}
		}
		if request.Method == "initialize" || request.Method == "ping" {
			return &RPCError{Code: CodeMethodNotFound, Message: "legacy lifecycle method is unavailable in this protocol"}
		}
		var capabilities map[string]json.RawMessage
		if json.Unmarshal(params.Meta[sdk.MetaKeyClientCapabilities], &capabilities) != nil || capabilities == nil {
			return &RPCError{Code: CodeInvalidParams, Message: "modern MCP requests require client capability metadata"}
		}
		request.modern = true
		var identity ClientInfo
		if raw := params.Meta[sdk.MetaKeyClientInfo]; len(raw) != 0 {
			if json.Unmarshal(raw, &identity) != nil || len(identity.Name) > 128 || len(identity.Version) > 128 {
				return &RPCError{Code: CodeInvalidParams, Message: "MCP client identity is invalid"}
			}
			request.clientName = identity.Name
		}
	}
	if request.Method == "server/discover" && !request.modern {
		return &RPCError{Code: CodeInvalidParams, Message: "server/discover requires per-request MCP protocol metadata"}
	}
	return nil
}

func negotiatedServerVersion(requested string) string {
	for _, version := range serverProtocolVersions[1:] {
		if requested == version {
			return requested
		}
	}
	return serverProtocolVersions[1]
}

func (s *Server) serveDiscover(ctx context.Context, request Request, output io.Writer) {
	result := map[string]any{
		"supportedVersions": serverProtocolVersions,
		"capabilities":      ServerCapabilities{Resources: &struct{}{}, Tools: &struct{}{}},
		"_meta":             map[string]any{sdk.MetaKeyServerInfo: ClientInfo{Name: ServerName, Version: ServerVersion}},
	}
	_ = s.write(output, Envelope{JSONRPC: "2.0", ID: request.ID, Result: serverResult(request, result)})
	_ = s.store.RecordMCPAudit(ctx, s.runID, "mcp.initialized", map[string]any{"client_name": request.clientName, "protocol_version": preferredClientProtocolVersion, "handshake": "server/discover"})
}

func serverResult(request Request, value any) json.RawMessage {
	raw := mustJSON(value)
	if !request.modern {
		return raw
	}
	var result map[string]json.RawMessage
	if json.Unmarshal(raw, &result) != nil || result == nil {
		return raw
	}
	result["resultType"] = json.RawMessage(`"complete"`)
	if request.Method != "tools/call" {
		result["ttlMs"] = json.RawMessage(`0`)
		result["cacheScope"] = json.RawMessage(`"private"`)
	}
	return mustJSON(result)
}
