package application

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"cyberagent-workbench/internal/apperror"
	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/llm"
	"cyberagent-workbench/internal/mcp"
	"cyberagent-workbench/internal/toolgateway"
)

func TestMCPToolSearchFindsLateApprovedOperationAndExecutesThroughGateway(t *testing.T) {
	f := newMCPOperationApprovalFixture(t, domain.RunExecutionPermissionFull, false, false, "large_catalog")
	permission, err := f.st.GetRunExecutionPermission(t.Context(), f.call.RunID)
	if err != nil {
		t.Fatal(err)
	}
	advertisement, err := f.supervisor.supervisorMCPCapabilities(t.Context(), f.turn, permission)
	if err != nil || len(advertisement.Capabilities.Servers[0].Tools) <= 128 {
		t.Fatalf("full authorized catalog missing: %v", err)
	}
	options := supervisorToolOptions{MCP: advertisement}
	specs := supervisorStructuredToolSpecs(f.turn.Mode.Surface, f.turn.Mode.Phase, permission.Mode, false, false, options)
	foundSearch := false
	for _, spec := range specs {
		foundSearch = foundSearch || spec.Name == string(toolgateway.MCPToolSearchTool)
		if strings.Contains(string(spec.Parameters), "zz_read_archive") || strings.Contains(string(spec.Parameters), "note_129") {
			t.Fatal("upfront model catalog includes deferred remote schemas")
		}
	}
	if !foundSearch {
		t.Fatal("MCP search was not offered as an ordinary provider-neutral tool")
	}
	search, err := prepareSupervisorToolCalls([]llm.ToolCall{{ID: "search", Name: string(toolgateway.MCPToolSearchTool), Arguments: json.RawMessage(`{"query":"归档"}`)}},
		f.call.RunID, f.call.Turn, 1, f.turn.Mode.Surface, f.turn.Mode.Phase, permission.Mode, false, false, options)
	if err != nil {
		t.Fatal(err)
	}
	call := f.call
	call.CallID, call.ToolName, call.PayloadJSON, call.AuthorityJSON = search[0].ID, search[0].Name, string(search[0].Arguments), string(search[0].Authority)
	result, err := f.supervisor.invokeSupervisorTool(t.Context(), f.turn, call)
	if err != nil {
		t.Fatal(err)
	}
	var envelope supervisorToolResultEnvelope
	var page mcpSearchPage
	if json.Unmarshal([]byte(result.ResultJSON), &envelope) != nil || json.Unmarshal([]byte(envelope.Stdout), &page) != nil ||
		page.TotalMatches != 1 || len(page.Operations) != 1 || page.Operations[0].Name != "zz_read_archive" || f.requests.Load() != 0 {
		t.Fatalf("late relevant operation not found locally: %s, requests=%d", result.ResultJSON, f.requests.Load())
	}
	operation := page.Operations[0]
	if !strings.Contains(string(operation.InputSchema), `"integer"`) || operation.Invocation.CapabilityFingerprint != f.server.Capabilities.Fingerprint {
		t.Fatalf("discovery did not return exact schema and invocation identity: %+v", operation)
	}
	// The fixture's durable pending call targets the exact late operation. It
	// passes the actual operation authorizer and manager before reaching TLS.
	if waiting, err := f.resume(t); err != nil || waiting || f.calls.Load() != 1 {
		t.Fatalf("late approved call failed at the real gateway: waiting=%t calls=%d err=%v", waiting, f.calls.Load(), err)
	}
	// Search authority can never be reused to prepare a remote operation.
	forged := options
	forged.MCP.Authority = search[0].Authority
	if _, err := prepareSupervisorToolCalls([]llm.ToolCall{{ID: "forged", Name: string(toolgateway.MCPToolCallTool), Arguments: json.RawMessage(f.call.PayloadJSON)}},
		f.call.RunID, f.call.Turn, 2, f.turn.Mode.Surface, f.turn.Mode.Phase, permission.Mode, false, false, forged); err == nil {
		t.Fatal("catalog authority was accepted as invocation authority")
	}
	// Unknown operations still fail the authoritative preparation check.
	unknown := json.RawMessage(strings.Replace(f.call.PayloadJSON, "zz_read_archive", "not_authorized", 1))
	if _, err := prepareSupervisorToolCalls([]llm.ToolCall{{ID: "unknown", Name: string(toolgateway.MCPToolCallTool), Arguments: unknown}},
		f.call.RunID, f.call.Turn, 2, f.turn.Mode.Surface, f.turn.Mode.Phase, permission.Mode, false, false, options); err == nil {
		t.Fatal("unknown operation was accepted")
	}
	if _, err := f.manager.Review(t.Context(), f.server.Descriptor.ID, mcp.ReviewRequest{Action: mcp.ReviewDisable,
		ExpectedDescriptorFingerprint: f.server.DescriptorFingerprint, ReviewedBy: "operator"}); err != nil {
		t.Fatal(err)
	}
	result, err = f.supervisor.invokeSupervisorTool(t.Context(), f.turn, call)
	if err != nil || json.Unmarshal([]byte(result.ResultJSON), &envelope) != nil || json.Unmarshal([]byte(envelope.Stdout), &page) != nil || len(page.Operations) != 0 {
		t.Fatalf("disabled operations remained discoverable: %s err=%v", result.ResultJSON, err)
	}
	if stopped, err := f.supervisor.invokeSupervisorTool(t.Context(), f.turn, f.call); err != nil || stopped.Status == domain.SupervisorToolCompleted || f.calls.Load() != 1 {
		t.Fatalf("disabled operation was not stopped: status=%s calls=%d err=%v", stopped.Status, f.calls.Load(), err)
	}
	f.capabilities.RuntimeAuthority.RevokeRun(f.call.RunID)
	if _, err := f.supervisor.invokeSupervisorTool(t.Context(), f.turn, call); apperror.CodeOf(err) != apperror.CodeFailedPrecondition || f.calls.Load() != 1 {
		t.Fatalf("revoked discovery authority remained usable: %v", err)
	}
}

func TestMCPToolSearchPagesFullCatalogAndFiltersUnicodeContent(t *testing.T) {
	catalog := mcp.ScopedCapabilities{ProtocolVersion: mcp.ClientProtocolVersion, Generation: strings.Repeat("a", 64),
		Servers: []mcp.ScopedServerCapability{{ServerID: "docs", Name: "文档", CapabilityFingerprint: strings.Repeat("b", 64)}}}
	server := &catalog.Servers[0]
	for i := 0; i < 137; i++ {
		server.Tools = append(server.Tools, mcp.RemoteTool{Name: fmt.Sprintf("read_%03d", i), InputSchema: json.RawMessage(`{"type":"object"}`)})
	}
	server.Resources = []mcp.RemoteResource{{Name: "架构说明", Description: "系统结构资料", URI: "docs://architecture"}}
	server.ResourceTemplates = []mcp.RemoteResourceTemplate{{Name: "问题记录", URITemplate: "docs://issues/{id}"}}
	server.Prompts = []mcp.RemotePrompt{{Name: "代码审阅", Arguments: []mcp.RemotePromptArgument{{Name: "language", Required: true}}}}
	request := toolgateway.MCPSearchPayload{Limit: 20}
	seen := map[string]bool{}
	for {
		raw, err := searchMCPCatalog(catalog, request)
		if err != nil {
			t.Fatal(err)
		}
		var page mcpSearchPage
		if json.Unmarshal(raw, &page) != nil || page.TotalMatches != 140 || len(raw) > toolgateway.MaxResultStdoutBytes {
			t.Fatalf("invalid catalog page: %s", raw)
		}
		for _, operation := range page.Operations {
			if seen[operation.Name] {
				t.Fatal("paging repeated an operation", operation.Name)
			}
			seen[operation.Name] = true
		}
		if page.NextRequest == nil {
			break
		}
		request = *page.NextRequest
	}
	if len(seen) != 140 || !seen["read_136"] || !seen["代码审阅"] {
		t.Fatal("paging lost later operations", len(seen))
	}
	for _, query := range []string{"架构", "结构资料", "代码审阅", "问题记录"} {
		raw, err := searchMCPCatalog(catalog, toolgateway.MCPSearchPayload{Query: query, Limit: 5})
		var page mcpSearchPage
		if err != nil || json.Unmarshal(raw, &page) != nil || len(page.Operations) != 1 {
			t.Fatalf("Unicode content search %q failed: %s %v", query, raw, err)
		}
	}
	for _, query := range []string{"unrelated", "???"} {
		raw, err := searchMCPCatalog(catalog, toolgateway.MCPSearchPayload{Query: query, Limit: 5})
		var page mcpSearchPage
		if err != nil || json.Unmarshal(raw, &page) != nil || len(page.Operations) != 0 {
			t.Fatalf("unrelated operations returned: %s %v", raw, err)
		}
	}
	if _, err := searchMCPCatalog(catalog, toolgateway.MCPSearchPayload{Limit: 5, Offset: 20, CatalogRevision: strings.Repeat("c", 64)}); apperror.CodeOf(err) != apperror.CodeConflict {
		t.Fatalf("stale continuation accepted: %v", err)
	}
}

func TestMCPToolSearchByteBoundContinuesWithoutDroppingEntries(t *testing.T) {
	catalog := mcp.ScopedCapabilities{ProtocolVersion: mcp.ClientProtocolVersion, Generation: strings.Repeat("a", 64),
		Servers: []mcp.ScopedServerCapability{{ServerID: "docs", Name: "Docs", CapabilityFingerprint: strings.Repeat("b", 64)}}}
	for i := 0; i < 3; i++ {
		catalog.Servers[0].Tools = append(catalog.Servers[0].Tools, mcp.RemoteTool{Name: fmt.Sprintf("large_%d", i),
			InputSchema: json.RawMessage(`{"type":"object","description":"` + strings.Repeat("x", 60000) + `"}`)})
	}
	raw, err := searchMCPCatalog(catalog, toolgateway.MCPSearchPayload{Limit: 20})
	var page mcpSearchPage
	if err != nil || json.Unmarshal(raw, &page) != nil || len(page.Operations) != 2 || page.NextRequest == nil || page.NextRequest.Offset != 2 {
		t.Fatalf("byte bound lost continuation: bytes=%d %v", len(raw), err)
	}
	raw, err = searchMCPCatalog(catalog, *page.NextRequest)
	if err != nil || json.Unmarshal(raw, &page) != nil || len(page.Operations) != 1 || page.Operations[0].Name != "large_2" {
		t.Fatalf("byte continuation skipped the remaining operation: %v", err)
	}
	catalog.Servers[0].Tools[0].Description = strings.Repeat("x", toolgateway.MaxResultStdoutBytes)
	if _, err := searchMCPCatalog(catalog, toolgateway.MCPSearchPayload{Limit: 5}); apperror.CodeOf(err) != apperror.CodeResourceExhausted || !strings.Contains(err.Error(), "server must reduce") {
		t.Fatalf("oversized single entry not reported explicitly: %v", err)
	}
}

func TestMCPToolSearchHasTypedThreadActivityDetail(t *testing.T) {
	call := threadActivityFactsCall(toolgateway.MCPToolSearchTool, []byte(`{"query":"归档","limit":5}`),
		nil, `{"catalog_revision":"`+strings.Repeat("a", 64)+`","total_matches":1,"operations":[]}`)
	detail, found, err := ProjectThreadActivityTypedDetail(call)
	if err != nil || !found || detail.Kind != "mcp" || detail.MCP == nil ||
		detail.MCP.Tool != "mcp_tool_search" || !detail.MCP.Boundary.Untrusted {
		t.Fatalf("catalog activity lacks typed detail: %+v found=%t err=%v", detail, found, err)
	}
}
