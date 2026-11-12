package application

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/toolgateway"
)

func TestThreadActivityToolKindCoversPublicNonCommandTools(t *testing.T) {
	tests := map[toolgateway.ToolName]string{
		toolgateway.WorkspaceListTool: "file_read", toolgateway.WorkspaceReadTool: "file_read",
		toolgateway.WorkspaceGlobTool: "file_read", toolgateway.WorkspaceGrepTool: "file_read",
		toolgateway.WorkspaceChangeTool: "file_edit", toolgateway.WorkspaceApplyTool: "file_edit",
		toolgateway.WorkspaceDeleteTool: "file_edit", toolgateway.WebSearchTool: "web_search",
		toolgateway.WebFetchTool: "web_fetch", toolgateway.WebCitationTool: "web_fetch",
		toolgateway.MCPToolCallTool: "mcp", toolgateway.CodeWorkspaceSymbolsTool: "verification",
		toolgateway.CodeDocumentSymbolsTool: "verification", toolgateway.CodeDefinitionTool: "verification",
		toolgateway.CodeReferencesTool: "verification", toolgateway.CodeImplementationTool: "verification",
		toolgateway.CodeHoverTool: "verification", toolgateway.CodeSignatureHelpTool: "verification",
		toolgateway.CodeDiagnosticsTool: "verification", toolgateway.CodeCallHierarchyTool: "verification",
		toolgateway.CodeTypeHierarchyTool: "verification", toolgateway.BrowserStatusTool: "browser",
		toolgateway.GitHubEvidenceListTool: "verification", toolgateway.GitHubEvidenceReadTool: "verification",
		toolgateway.BrowserNavigateTool: "browser", toolgateway.BrowserSnapshotTool: "browser",
		toolgateway.BrowserClickTool: "browser", toolgateway.BrowserTypeTool: "browser",
		toolgateway.BrowserScreenshotTool: "browser", toolgateway.BrowserMoveTool: "browser",
	}
	for name, want := range tests {
		if got := threadActivityToolKind(name); got != want || threadActivityToolLabel(name) == "" {
			t.Errorf("tool %q kind=%q label=%q, want %q", name, got,
				threadActivityToolLabel(name), want)
		}
	}
	if got := threadActivityToolKind(toolgateway.CommandRuntimeTool); got != "tool" {
		t.Fatalf("command kind=%q", got)
	}
}

func TestProjectThreadActivityWorkspaceEditHidesContentAndRawResult(t *testing.T) {
	payload, _ := json.Marshal(toolgateway.WorkspaceChangePayload{
		Version: toolgateway.AgentCodeRegistryVersion, Action: "create", Path: "src/session.ts",
		ExpectedSHA256: "missing", Content: "private replacement body",
	})
	call := threadActivityFactsCall(toolgateway.WorkspaceChangeTool, payload,
		map[string]string{"operation": "create", "status": "proposed",
			"credential": "secret-metadata-must-not-project"}, "private stdout must not project")
	facts, found, err := ProjectThreadActivityToolFacts(call)
	if err != nil || !found || facts.Kind != "file_edit" || facts.Target != "src/session.ts" ||
		factValue(facts.Parameters, "change") != "已提供（内容不显示）" ||
		factValue(facts.Result, "operation") != "create" ||
		factValue(facts.Result, "status") != "proposed" {
		t.Fatalf("facts=%#v found=%t err=%v", facts, found, err)
	}
	encoded, _ := json.Marshal(facts)
	for _, forbidden := range []string{"private replacement body", "private stdout",
		"secret-metadata"} {
		if strings.Contains(string(encoded), forbidden) {
			t.Fatalf("safe projection leaked %q: %s", forbidden, encoded)
		}
	}
}

func TestProjectThreadActivityMCPShowsOnlyServerToolAndArgumentKeys(t *testing.T) {
	payload, _ := json.Marshal(toolgateway.MCPToolCallPayload{
		Version: toolgateway.MCPClientToolProtocolVersion, ServerID: "issue-tracker",
		ToolName: "create_issue", CapabilityFingerprint: strings.Repeat("b", 64),
		Arguments: json.RawMessage(`{"title":"private issue title","body":"private issue body","nested":{"token":"not public"}}`),
	})
	call := threadActivityFactsCall(toolgateway.MCPToolCallTool, payload,
		map[string]string{"trust": "untrusted", "remote_payload": "private remote metadata"},
		"private MCP output")
	facts, found, err := ProjectThreadActivityToolFacts(call)
	if err != nil || !found || facts.Kind != "mcp" ||
		facts.Target != "issue-tracker / create_issue" ||
		factValue(facts.Parameters, "argument_keys") != "body, nested, title" ||
		factValue(facts.Result, "trust") != "外部不可信数据" || !facts.Untrusted {
		t.Fatalf("facts=%#v found=%t err=%v", facts, found, err)
	}
	encoded, _ := json.Marshal(facts)
	for _, forbidden := range []string{"private issue", "not public", "private MCP",
		"private remote", strings.Repeat("b", 64)} {
		if strings.Contains(string(encoded), forbidden) {
			t.Fatalf("MCP projection leaked %q: %s", forbidden, encoded)
		}
	}
}

func TestProjectThreadActivityWebFactsPreserveSafeQueryAndWhitelistResult(t *testing.T) {
	payload, _ := json.Marshal(toolgateway.WebFetchPayload{Version: "web_fetch.v1",
		URL: "https://docs.example.com/report?page=2"})
	call := threadActivityFactsCall(toolgateway.WebFetchTool, payload,
		map[string]string{"state": "partial", "robots": "bypassed_disallow",
			"partial": "true", "citeable": "true", "url": "https://private.example/secret"},
		"private page body")
	facts, found, err := ProjectThreadActivityToolFacts(call)
	if err != nil || !found || facts.Kind != "web_fetch" ||
		facts.Target != "https://docs.example.com/report?page=2" ||
		factValue(facts.Result, "state") != "partial" ||
		factValue(facts.Result, "robots") != "bypassed_disallow" || !facts.Untrusted {
		t.Fatalf("facts=%#v found=%t err=%v", facts, found, err)
	}
	encoded, _ := json.Marshal(facts)
	for _, forbidden := range []string{"private page", "private.example"} {
		if strings.Contains(string(encoded), forbidden) {
			t.Fatalf("Web projection leaked %q: %s", forbidden, encoded)
		}
	}
}

func TestProjectThreadActivityFactsDeduplicatesTruncationSignals(t *testing.T) {
	payload, _ := json.Marshal(toolgateway.WorkspaceReadPayload{
		Version: toolgateway.AgentCodeRegistryVersion, Path: "large.log", StartLine: 1, EndLine: 20,
	})
	call := threadActivityFactsCall(toolgateway.WorkspaceReadTool, payload,
		map[string]string{"result_count": "20", "truncated": "true"}, "hidden output")
	var result map[string]any
	if err := json.Unmarshal([]byte(call.ResultJSON), &result); err != nil {
		t.Fatal(err)
	}
	result["truncated"] = true
	encoded, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	call.ResultJSON = string(encoded)
	facts, found, err := ProjectThreadActivityToolFacts(call)
	if err != nil || !found {
		t.Fatalf("facts=%#v found=%t err=%v", facts, found, err)
	}
	count := 0
	for _, fact := range facts.Result {
		if fact.Name == "truncated" {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("truncation facts=%#v", facts.Result)
	}
}

func TestProjectThreadActivityBrowserTypeHidesInputValue(t *testing.T) {
	payload, _ := json.Marshal(toolgateway.BrowserActionPayload{Version: "browser_type.v1",
		Selector: "#search", Value: "private browser input"})
	call := threadActivityFactsCall(toolgateway.BrowserTypeTool, payload, nil, "private page output")
	facts, found, err := ProjectThreadActivityToolFacts(call)
	if err != nil || !found || facts.Kind != "browser" ||
		factValue(facts.Parameters, "selector") != "#search" ||
		factValue(facts.Parameters, "input") != "已提供 21 个字符（内容不显示）" {
		t.Fatalf("facts=%#v found=%t err=%v", facts, found, err)
	}
	encoded, _ := json.Marshal(facts)
	if strings.Contains(string(encoded), "private browser input") ||
		strings.Contains(string(encoded), "private page output") {
		t.Fatalf("browser projection leaked content: %s", encoded)
	}
}

func TestProjectThreadActivityToolFactsRejectsInvalidPayloadAndSkipsCommand(t *testing.T) {
	call := domain.SupervisorToolCall{ToolName: string(toolgateway.WebSearchTool),
		PayloadJSON: `{"version":"web_search.v1","query":"x","limit":1,"unknown":true}`,
		Status:      domain.SupervisorToolPending}
	if _, found, err := ProjectThreadActivityToolFacts(call); !found || err == nil {
		t.Fatalf("invalid payload found=%t err=%v", found, err)
	}
	call.ToolName = string(toolgateway.CommandRuntimeTool)
	if _, found, err := ProjectThreadActivityToolFacts(call); found || err != nil {
		t.Fatalf("command projection found=%t err=%v", found, err)
	}
}

func threadActivityFactsCall(name toolgateway.ToolName, payload []byte,
	metadata map[string]string, stdout string,
) domain.SupervisorToolCall {
	result, _ := json.Marshal(map[string]any{"version": supervisorToolResultVersion,
		"tool": name, "status": "completed", "metadata": metadata,
		"stdout": stdout, "stderr": "private stderr"})
	completed := time.Date(2026, 9, 2, 12, 0, 1, 0, time.UTC)
	return domain.SupervisorToolCall{ToolName: string(name), PayloadJSON: string(payload),
		Status: domain.SupervisorToolCompleted, ResultJSON: string(result),
		CreatedAt: completed.Add(-time.Second), CompletedAt: &completed}
}

func factValue(fields []ThreadActivityFactField, name string) string {
	for _, field := range fields {
		if field.Name == name {
			return field.Value
		}
	}
	return ""
}

func TestThreadActivityPointerMovementUsesBrowserDetail(t *testing.T) {
	payload := []byte(`{"version":"browser_move.v2","point":{"x":40,"y":30,"document_epoch":2,"viewport":{"width":800,"height":600}}}`)
	call := threadActivityFactsCall(toolgateway.BrowserMoveTool, payload, map[string]string{}, `{"dispatched":true}`)
	detail, found, err := ProjectThreadActivityTypedDetail(call)
	if err != nil || !found || detail.Kind != "browser" || detail.Browser == nil || detail.Browser.Action != string(toolgateway.BrowserMoveTool) || detail.Browser.Operation != "移动页面指针" {
		t.Fatalf("pointer detail=%+v found=%t err=%v", detail, found, err)
	}
	if !SupportsThreadActivityDetail(string(toolgateway.BrowserMoveTool)) {
		t.Fatal("pointer detail is not registered")
	}
}
