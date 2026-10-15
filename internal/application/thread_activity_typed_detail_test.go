package application

import (
	"encoding/json"
	"strings"
	"testing"

	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/toolgateway"
	"cyberagent-workbench/internal/webevidence"
)

func TestProjectThreadActivityTypedWebSearchProjectsRankedSafeSources(t *testing.T) {
	payload, _ := json.Marshal(toolgateway.WebSearchPayload{Version: "web_search.v1",
		Query: "runtime architecture", Limit: 2})
	stdout, _ := json.Marshal(webevidence.SearchResult{
		ProtocolVersion: webevidence.SearchProtocolVersion,
		Query:           "runtime architecture",
		Provider:        "provider-native",
		SearchPolicy:    webevidence.SearchPolicyProviderNative,
		SelectionReason: "configured provider supports grounded search",
		Sources: []webevidence.SearchStub{
			{Rank: 1, CanonicalURL: "https://docs.example.com/runtime?page=2&token=hidden#section",
				Title: "Runtime guide", Provider: "provider-native", Citeable: true,
				Snippet: "private search snippet", Fetched: true},
			{Rank: 2, CanonicalURL: "https://example.org/design", Title: "Design notes",
				Provider: "provider-native"},
		},
	})
	call := threadActivityFactsCall(toolgateway.WebSearchTool, payload,
		map[string]string{"provider": "provider-native", "search_policy": "provider_native",
			"selection_reason": "configured provider supports grounded search",
			"source_count":     "2", "citeable": "true"}, string(stdout))

	detail, found, err := ProjectThreadActivityTypedDetail(call)
	if err != nil || !found || detail.Kind != "web_search" || detail.WebSearch == nil {
		t.Fatalf("detail=%#v found=%t err=%v", detail, found, err)
	}
	search := detail.WebSearch
	if search.Query != "runtime architecture" || search.Provider != "provider-native" ||
		search.SourceCount != 2 || !search.Citeable || len(search.Sources) != 2 ||
		search.Sources[0].Rank != 1 || search.Sources[0].Title != "Runtime guide" ||
		search.Sources[0].URL != "https://docs.example.com/runtime?page=2" ||
		search.Sources[0].State != string(webevidence.SourceFetched) ||
		!search.Sources[0].Citeable {
		t.Fatalf("unexpected search detail: %#v", search)
	}
	encoded, _ := json.Marshal(detail)
	for _, forbidden := range []string{"token=hidden", "#section", "private search snippet"} {
		if strings.Contains(string(encoded), forbidden) {
			t.Fatalf("search detail exposed %q: %s", forbidden, encoded)
		}
	}
}

func TestSafeThreadActivityURLPreservesSemanticQueryAndDropsCredentials(t *testing.T) {
	got := safeThreadActivityURL("https://operator:private-canary@Docs.Example.com/guide" +
		"?page=2&lang=zh&auth=private-auth-canary&signature=private-signature-canary#part")
	if got != "https://docs.example.com/guide?lang=zh&page=2" {
		t.Fatalf("safe URL=%q", got)
	}
	for _, forbidden := range []string{"operator", "private-canary", "auth=", "signature=", "#part"} {
		if strings.Contains(got, forbidden) {
			t.Fatalf("safe URL exposed %q: %q", forbidden, got)
		}
	}
}

func TestProjectThreadActivityTypedMCPKeepsSafeScalarsAndRedactsSecrets(t *testing.T) {
	payload, _ := json.Marshal(toolgateway.MCPToolCallPayload{
		Version: toolgateway.MCPClientToolProtocolVersion, ServerID: "issue-tracker",
		ToolName: "create_issue", CapabilityFingerprint: strings.Repeat("a", 64),
		Arguments: json.RawMessage(`{
			"title":"Fix durable loop","priority":2,"dry_run":true,
			"password":"plain-canary-password","clientSecret":"nested-key-canary",
			"authorization":"Bearer abcdefghijklmnopqrstuvwxyz123456",
			"sk-proj-abcdefghijklmnopqrstuvwxyz123456":"secret-key-name-value",
			"nested":{"token":"nested-token-canary"}}`),
	})
	stdout := `{
		"message":"Issue created","count":1,"ok":true,
		"sk-proj-zyxwvutsrqponmlkjihgfedcba":"secret-result-key-name-value",
		"api_key":"sk-abcdefghijklmnopqrstuvwxyz123456",
		"cookie":"session-cookie-canary","nested":{"password":"nested-canary"}}`
	call := threadActivityFactsCall(toolgateway.MCPToolCallTool, payload,
		map[string]string{"untrusted": "true"}, stdout)

	detail, found, err := ProjectThreadActivityTypedDetail(call)
	if err != nil || !found || detail.Kind != "mcp" || detail.MCP == nil {
		t.Fatalf("detail=%#v found=%t err=%v", detail, found, err)
	}
	mcp := detail.MCP
	if mcp.Server != "issue-tracker" || mcp.Tool != "create_issue" ||
		jsonFieldSummary(mcp.Arguments, "title") != "Fix durable loop" ||
		jsonFieldSummary(mcp.Arguments, "priority") != "2" ||
		jsonFieldSummary(mcp.Arguments, "dry_run") != "true" ||
		jsonFieldSummary(mcp.Arguments, "password") != "[已脱敏]" ||
		jsonFieldSummary(mcp.Arguments, "clientSecret") != "[已脱敏]" ||
		jsonFieldSummary(mcp.Result.Fields, "message") != "Issue created" ||
		jsonFieldSummary(mcp.Result.Fields, "api_key") != "[已脱敏]" {
		t.Fatalf("unexpected MCP detail: %#v", mcp)
	}
	encoded, _ := json.Marshal(detail)
	for _, forbidden := range []string{"plain-canary-password", "nested-key-canary",
		"abcdefghijklmnopqrstuvwxyz123456", "nested-token-canary", "session-cookie-canary",
		"zyxwvutsrqponmlkjihgfedcba", "secret-key-name-value", "secret-result-key-name-value",
		"nested-canary", strings.Repeat("a", 64)} {
		if strings.Contains(string(encoded), forbidden) {
			t.Fatalf("MCP detail exposed %q: %s", forbidden, encoded)
		}
	}
}

func TestProjectThreadActivityMCPContentOperations(t *testing.T) {
	for _, method := range []string{"resources/read", "prompts/get"} {
		payload, _ := json.Marshal(toolgateway.MCPToolCallPayload{Version: toolgateway.MCPContentToolProtocolVersion,
			Method: method, ToolName: strings.ReplaceAll(method, "/", "."), ServerID: "content-server",
			CapabilityFingerprint: strings.Repeat("a", 64), Arguments: json.RawMessage(`{"name":"summary"}`)})
		call := threadActivityFactsCall(toolgateway.MCPToolCallTool, payload, nil, `{"content":[{"type":"text","text":"visible resource"}]}`)
		detail, found, err := ProjectThreadActivityTypedDetail(call)
		if err != nil || !found || detail.MCP == nil || detail.MCP.Tool != strings.ReplaceAll(method, "/", ".") {
			t.Fatalf("content operation activity disappeared: %#v found=%t err=%v", detail, found, err)
		}
	}
}

func TestProjectThreadActivityTypedFileEditProjectsSafeDiffReferenceAndStatistics(t *testing.T) {
	payload, _ := json.Marshal(toolgateway.WorkspaceChangePayload{
		Version: toolgateway.AgentCodeRegistryVersion, Action: "propose_patch", Path: "src/session.ts",
		ExpectedSHA256: strings.Repeat("b", 64),
		Replacements: []toolgateway.WorkspaceReplacement{{OldText: "old private content",
			NewText: "new private content", ExpectedOccurrences: 1}},
	})
	stdout, _ := json.Marshal(map[string]any{
		"version": toolgateway.AgentCodeRegistryVersion, "edit_id": "edit-safe-reference",
		"operation": "propose_patch",
		"path":      "src/session.ts", "status": "proposed", "file_written": false,
		"diff": "@@ -1,2 +1,3 @@\n-old secret=sk-abcdefghijklmnopqrstuvwxyz\n+new secret=sk-zyxwvutsrqponmlkjihgfedcba\n+safe line\n",
	})
	call := threadActivityFactsCall(toolgateway.WorkspaceChangeTool, payload,
		map[string]string{"operation": "replace", "status": "proposed",
			"edit_id": "edit-safe-reference"}, string(stdout))

	detail, found, err := ProjectThreadActivityTypedDetail(call)
	if err != nil || !found || detail.Kind != "file_edit" || detail.FileEdit == nil {
		t.Fatalf("detail=%#v found=%t err=%v", detail, found, err)
	}
	edit := detail.FileEdit
	if edit.Action != "propose_patch" || edit.Path != "src/session.ts" ||
		edit.EditID != "edit-safe-reference" || edit.DiffAvailable ||
		edit.ApplyStatus != "proposed" || edit.Diff.AddedLines != 2 ||
		edit.Diff.RemovedLines != 1 || edit.Diff.Hunks != 1 ||
		!strings.Contains(edit.Diff.Summary, "+2 −1") {
		t.Fatalf("unexpected file-edit detail: %#v", edit)
	}
	encoded, _ := json.Marshal(detail)
	for _, forbidden := range []string{"old private content", "new private content",
		"abcdefghijklmnopqrstuvwxyz", "zyxwvutsrqponmlkjihgfedcba", strings.Repeat("b", 64)} {
		if strings.Contains(string(encoded), forbidden) {
			t.Fatalf("file-edit detail exposed %q: %s", forbidden, encoded)
		}
	}
}

func TestThreadActivityTypedFileEditRejectsAvailableDiffWithoutReference(t *testing.T) {
	value := ThreadActivityTypedDetail{Kind: "file_edit", FileEdit: &ThreadActivityFileEditDetail{
		Operation: "应用文件修改", DiffAvailable: true,
		Boundary: ThreadActivityBoundary{Authorization: "policy_checked"},
	}}
	if err := value.Validate(); err == nil {
		t.Fatal("available file-edit diff without an opaque reference unexpectedly validated")
	}
}

func TestProjectThreadActivityTypedRevertConflictRequiresReview(t *testing.T) {
	for _, test := range []struct {
		action string
		code   string
		want   string
	}{
		{"propose_revert", "CONFLICT", "撤销请求与当前编辑状态不一致。请先核对来源编辑和当前文件，保留后续修改。"},
		{"propose_revert", "POLICY_DENIED", "当前执行边界未授权此操作"},
		{"propose_patch", "CONFLICT", "执行上下文已变化，请重试"},
	} {
		t.Run(test.action+"/"+test.code, func(t *testing.T) {
			input := toolgateway.WorkspaceChangePayload{Version: toolgateway.AgentCodeRegistryVersion,
				Action: test.action, Path: "review.txt", ExpectedSHA256: strings.Repeat("b", 64)}
			if test.action == "propose_revert" {
				input.SourceRunID, input.SourceEditID = "run-source", "edit-source"
			} else {
				input.Replacements = []toolgateway.WorkspaceReplacement{{OldText: "old", NewText: "new", ExpectedOccurrences: 1}}
			}
			payload, _ := json.Marshal(input)
			result, _ := json.Marshal(map[string]any{"version": supervisorToolResultVersion,
				"tool": toolgateway.WorkspaceChangeTool, "status": "failed", "code": test.code,
				"stderr": "private failure details must not be displayed"})
			call := domain.SupervisorToolCall{ToolName: string(toolgateway.WorkspaceChangeTool),
				PayloadJSON: string(payload), Status: domain.SupervisorToolFailed,
				ErrorCode: test.code, ResultJSON: string(result)}
			detail, found, err := ProjectThreadActivityTypedDetail(call)
			if err != nil || !found || detail.FileEdit == nil {
				t.Fatalf("detail=%+v found=%t err=%v", detail, found, err)
			}
			edit := detail.FileEdit
			if edit.Action != test.action || edit.Boundary.ErrorCode != test.code ||
				edit.Boundary.FailureReason != test.want || edit.Applied || edit.FileWritten {
				t.Fatalf("incorrect failure presentation: %+v", edit)
			}
			encoded, _ := json.Marshal(detail)
			if strings.Contains(string(encoded), "private failure details") {
				t.Fatalf("raw failure exposed: %s", encoded)
			}
		})
	}
}

func TestProjectThreadActivityTypedOtherBranches(t *testing.T) {
	readPayload, _ := json.Marshal(toolgateway.WorkspaceReadPayload{
		Version: toolgateway.AgentCodeRegistryVersion, Path: "src/main.go", StartLine: 5,
		EndLine: 15})
	fetchPayload, _ := json.Marshal(toolgateway.WebFetchPayload{Version: "web_fetch.v1",
		URL: "https://docs.example.com/guide?view=compact"})
	codePayload, _ := json.Marshal(toolgateway.CodeIntelPayload{
		Version: toolgateway.CodeIntelProtocolVersion, ServerID: "gopls",
		ServerGeneration: strings.Repeat("c", 64), CapabilityFingerprint: strings.Repeat("d", 64),
		Path: "src/main.go", Line: 4, Character: 2, Limit: 10})
	browserPayload, _ := json.Marshal(toolgateway.BrowserActionPayload{
		Version: "browser_type.v1", Selector: "#search", Value: "private browser input"})

	tests := []struct {
		name string
		call domain.SupervisorToolCall
		kind string
		ok   func(ThreadActivityTypedDetail) bool
	}{
		{name: "file read", kind: "file_read",
			call: threadActivityFactsCall(toolgateway.WorkspaceReadTool, readPayload,
				map[string]string{"result_count": "11", "truncated": "true"}, "hidden file"),
			ok: func(value ThreadActivityTypedDetail) bool {
				return value.FileRead != nil && value.FileRead.Path == "src/main.go" &&
					value.FileRead.StartLine == 5 && value.FileRead.EndLine == 15 &&
					value.FileRead.ResultCount == 11 && value.FileRead.Truncated
			}},
		{name: "web fetch", kind: "web_fetch",
			call: threadActivityFactsCall(toolgateway.WebFetchTool, fetchPayload,
				map[string]string{"state": "partial", "robots": "bypassed_disallow",
					"robots_policy": "audit_only", "redirects": "2", "partial": "true",
					"citeable": "true"},
				`{"protocol_version":"web_fetch.v1","snapshot":{"url":"https://docs.example.com/guide?view=compact","state":"partial","http_status":206,"robots":"bypassed_disallow","redirects":2,"citeable":true}}`),
			ok: func(value ThreadActivityTypedDetail) bool {
				return value.WebFetch != nil && value.WebFetch.URL == "https://docs.example.com/guide?view=compact" &&
					value.WebFetch.State == "partial" && value.WebFetch.HTTPStatus == 206 &&
					value.WebFetch.RobotsPolicy == "audit_only" && value.WebFetch.Redirects == 2
			}},
		{name: "verification", kind: "verification",
			call: threadActivityFactsCall(toolgateway.CodeHoverTool, codePayload,
				map[string]string{"result_count": "1"}, "hidden hover"),
			ok: func(value ThreadActivityTypedDetail) bool {
				return value.Verification != nil && value.Verification.Tool == "code_hover" &&
					value.Verification.Path == "src/main.go" && value.Verification.Position == "5:3"
			}},
		{name: "browser", kind: "browser",
			call: threadActivityFactsCall(toolgateway.BrowserTypeTool, browserPayload,
				map[string]string{"artifact_bytes": "42"}, "hidden browser result"),
			ok: func(value ThreadActivityTypedDetail) bool {
				return value.Browser != nil && value.Browser.Action == "browser_type" &&
					value.Browser.Selector == "#search" && value.Browser.InputLength == 21 &&
					value.Browser.ArtifactBytes == 42
			}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			value, found, err := ProjectThreadActivityTypedDetail(test.call)
			if err != nil || !found || value.Kind != test.kind || !test.ok(value) {
				t.Fatalf("detail=%#v found=%t err=%v", value, found, err)
			}
			encoded, _ := json.Marshal(value)
			for _, forbidden := range []string{"hidden file", "hidden hover",
				"private browser input", "hidden browser result", strings.Repeat("c", 64),
				strings.Repeat("d", 64)} {
				if strings.Contains(string(encoded), forbidden) {
					t.Fatalf("typed detail exposed %q: %s", forbidden, encoded)
				}
			}
		})
	}
}

func TestThreadActivityTypedDetailRejectsAmbiguousOrMismatchedBranches(t *testing.T) {
	command := &ThreadActivityCommandGroup{Commands: []ThreadActivityCommandDetail{}}
	web := &ThreadActivityWebFetchDetail{Operation: "抓取网页",
		Boundary: ThreadActivityBoundary{Authorization: "policy_checked"}}
	if err := (ThreadActivityTypedDetail{Kind: "command", Command: command,
		WebFetch: web}).Validate(); err == nil {
		t.Fatal("ambiguous detail unexpectedly validated")
	}
	if err := (ThreadActivityTypedDetail{Kind: "web_fetch", Command: command}).Validate(); err == nil {
		t.Fatal("mismatched detail unexpectedly validated")
	}
}

func jsonFieldSummary(values []ThreadActivityJSONFieldSummary, name string) string {
	for _, value := range values {
		if value.Name == name {
			return value.Summary
		}
	}
	return ""
}
