package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"io"

	"cyberagent-workbench/internal/runactivity"
)

// resourceCatalog is the fixed v1 catalog. Every declared resource is
// implemented; nothing beyond this list is ever published.
func (s *Server) resourceCatalog() []Resource {
	return []Resource{
		{URI: "cyberagent://run/summary", Name: "Run summary", Description: "Bounded Run/Mission metadata projection", MIMEType: "application/json"},
		{URI: "cyberagent://run/activity", Name: "Run activity", Description: "Display-only public activity projection; never model private reasoning", MIMEType: "application/json"},
	}
}

func (s *Server) serveResourcesList(ctx context.Context, request Request, output io.Writer) {
	_ = ctx
	result := ListResourcesResult{Resources: s.resourceCatalog()}
	_ = s.write(output, Envelope{JSONRPC: "2.0", ID: request.ID, Result: serverResult(request, result)})
}

func (s *Server) serveResourcesRead(ctx context.Context, request Request, output io.Writer) {
	params, err := DecodeReadResourceParams(request.Params)
	if err != nil {
		_ = s.write(output, Envelope{JSONRPC: "2.0", ID: request.ID,
			Error: &RPCError{Code: CodeInvalidParams, Message: err.Error()}})
		return
	}
	switch params.URI {
	case "cyberagent://run/summary":
		s.readRunSummary(ctx, request, output)
	case "cyberagent://run/activity":
		s.readRunActivity(ctx, request, output)
	default:
		_ = s.write(output, Envelope{JSONRPC: "2.0", ID: request.ID,
			Error: &RPCError{Code: CodeInvalidParams, Message: "resource not found"}})
	}
}

func (s *Server) readRunSummary(ctx context.Context, request Request, output io.Writer) {
	run, err := s.store.GetRun(ctx, s.runID)
	if err != nil {
		_ = s.write(output, Envelope{JSONRPC: "2.0", ID: request.ID,
			Error: &RPCError{Code: CodeInternalError, Message: "Run scope unavailable"}})
		return
	}
	summary := map[string]any{"run_id": run.ID, "status": run.Status,
		"profile": run.Config.ModelRoute, "workspace_id": s.workspaceID}
	_ = s.write(output, Envelope{JSONRPC: "2.0", ID: request.ID,
		Result: serverResult(request, ReadResourceResult{Contents: []ResourceContent{{URI: "cyberagent://run/summary",
			MIMEType: "application/json", Text: string(mustJSON(summary))}}})})
	_ = s.store.RecordMCPAudit(ctx, s.runID, "mcp.resource_read", map[string]any{"uri": "cyberagent://run/summary"})
}

func (s *Server) readRunActivity(ctx context.Context, request Request, output io.Writer) {
	projection, err := s.activityResource(ctx)
	if err != nil {
		_ = s.write(output, Envelope{JSONRPC: "2.0", ID: request.ID,
			Error: &RPCError{Code: CodeInternalError, Message: "Run activity unavailable"}})
		return
	}
	raw, err := json.Marshal(projection)
	if err != nil {
		_ = s.write(output, Envelope{JSONRPC: "2.0", ID: request.ID,
			Error: &RPCError{Code: CodeInternalError, Message: "Run activity projection failed"}})
		return
	}
	_ = s.write(output, Envelope{JSONRPC: "2.0", ID: request.ID,
		Result: serverResult(request, ReadResourceResult{Contents: []ResourceContent{{URI: "cyberagent://run/activity",
			MIMEType: "application/json", Text: string(raw)}}})})
	_ = s.store.RecordMCPAudit(ctx, s.runID, "mcp.resource_read", map[string]any{"uri": "cyberagent://run/activity"})
}

var _ = runactivity.ProtocolVersion
var _ = fmt.Sprintf
