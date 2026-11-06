package httpapi

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/toolgateway"
)

func TestSupervisorToolRoundViewRedactsRecordPayloadAndResult(t *testing.T) {
	completed := time.Now().UTC()
	round := domain.SupervisorToolRound{Calls: []domain.SupervisorToolCall{{
		CallID: "call-command", ToolName: string(toolgateway.CommandRuntimeTool),
		PayloadJSON: `{"stdin":"operator-input","environment":{"TOKEN":"private-env-value"}}`,
		Status:      domain.SupervisorToolCompleted,
		ResultJSON:  `{"stdout":"another-secret-value","stderr":"session-value"}`,
		CreatedAt:   completed.Add(-time.Second), CompletedAt: &completed,
	}}}
	detail := ThreadActivityToolDetailView{Name: string(toolgateway.CommandRuntimeTool),
		Label: "运行命令", AgentID: "agent-root", AgentRole: "root", AgentLabel: "Root Agent",
		Status: "completed", StartedAt: completed.Add(-time.Second), CompletedAt: &completed,
		Detail: ThreadActivityTypedDetailView{Kind: "command",
			Command: &ThreadActivityCommandGroupView{Commands: []ThreadActivityCommandDetailView{{
				Command: "Write-Output safe", WorkingDirectory: ".", Status: "completed",
				ExecutionEnvironment: "Workspace Sandbox", Network: "disabled",
			}}}}}
	view := supervisorToolRoundView(round, "thread-1",
		map[string]*ThreadActivityToolDetailView{"call-command": &detail})
	if len(view.Calls) != 1 {
		t.Fatalf("calls=%d", len(view.Calls))
	}
	encoded, err := json.Marshal(view.Calls[0])
	if err != nil {
		t.Fatal(err)
	}
	public := string(encoded)
	for _, forbidden := range []string{"private-env-value", "operator-input",
		"another-secret-value", "session-value", "private message"} {
		if strings.Contains(public, forbidden) {
			t.Fatalf("Record projection exposed %q: %s", forbidden, public)
		}
	}
	for _, required := range []string{"Write-Output safe", `"kind":"command"`,
		`"detail_available":true`, `"agent_id":"agent-root"`} {
		if !strings.Contains(public, required) {
			t.Fatalf("Record projection omitted %q: %s", required, public)
		}
	}
	if strings.Contains(public, `"payload"`) || strings.Contains(public, `"result"`) {
		t.Fatalf("Record public call retained generic payload/result fields: %s", public)
	}
}

func TestSupervisorToolRoundViewUsesTypedMCPDetail(t *testing.T) {
	payload, err := json.Marshal(toolgateway.MCPToolCallPayload{
		Version: toolgateway.MCPClientToolProtocolVersion, ServerID: "issue-tracker",
		ToolName: "create_issue", CapabilityFingerprint: strings.Repeat("b", 64),
		Arguments: json.RawMessage(`{"title":"private title","body":"private body"}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	completed := time.Now().UTC()
	round := domain.SupervisorToolRound{Calls: []domain.SupervisorToolCall{{
		CallID: "call-mcp", ToolName: string(toolgateway.MCPToolCallTool), PayloadJSON: string(payload),
		Status: domain.SupervisorToolCompleted,
		ResultJSON: `{"version":"supervisor_tool_result.v1","tool":"mcp_tool_call",` +
			`"status":"completed","metadata":{"remote":"private metadata"},` +
			`"stdout":"private MCP output"}`,
		CreatedAt: completed.Add(-time.Second), CompletedAt: &completed,
	}}}
	detail := ThreadActivityToolDetailView{Name: string(toolgateway.MCPToolCallTool),
		Label: "MCP 调用", AgentID: "agent-root", AgentRole: "root", AgentLabel: "Root Agent",
		Status: "completed", StartedAt: completed.Add(-time.Second), CompletedAt: &completed,
		Detail: ThreadActivityTypedDetailView{Kind: "mcp", MCP: &ThreadActivityMCPDetailView{
			Operation: "MCP 调用", Server: "issue-tracker", Tool: "create_issue",
			Arguments: []ThreadActivityJSONFieldSummaryView{{Name: "body", Type: "string", Summary: "[value]"},
				{Name: "title", Type: "string", Summary: "[value]"}},
			Result:   ThreadActivityJSONSummaryView{Type: "object", Count: 1, Summary: "1 field"},
			Boundary: ThreadActivityBoundaryView{Authorization: "policy_checked", Untrusted: true},
		}}}
	encoded, err := json.Marshal(supervisorToolRoundView(round, "thread-1",
		map[string]*ThreadActivityToolDetailView{"call-mcp": &detail}))
	if err != nil {
		t.Fatal(err)
	}
	public := string(encoded)
	for _, required := range []string{`"kind":"mcp"`, `"server":"issue-tracker"`,
		`"tool":"create_issue"`, `"name":"body"`, `"name":"title"`} {
		if !strings.Contains(public, required) {
			t.Fatalf("typed MCP projection omitted %q: %s", required, public)
		}
	}
	for _, forbidden := range []string{"private title", "private body", "private metadata",
		"private MCP output", strings.Repeat("b", 64)} {
		if strings.Contains(public, forbidden) {
			t.Fatalf("typed MCP projection leaked %q: %s", forbidden, public)
		}
	}
	if strings.Contains(public, `"payload"`) || strings.Contains(public, `"result":[`) {
		t.Fatalf("typed MCP Record response retained a generic fact bag: %s", public)
	}
}

func TestSupervisorToolRoundViewFailsClosedForLegacyUntypedTool(t *testing.T) {
	completed := time.Now().UTC()
	round := domain.SupervisorToolRound{Calls: []domain.SupervisorToolCall{{
		ToolName: "work_item_create", PayloadJSON: `{"title":"private work item"}`,
		Status:     domain.SupervisorToolCompleted,
		ResultJSON: `{"status":"completed","message":"private result"}`,
		CreatedAt:  completed.Add(-time.Second), CompletedAt: &completed,
	}}}
	encoded, err := json.Marshal(supervisorToolRoundView(round, "thread-1", nil))
	if err != nil {
		t.Fatal(err)
	}
	public := string(encoded)
	if strings.Contains(public, "private work item") || strings.Contains(public, "private result") ||
		!strings.Contains(public, `"detail_available":false`) || strings.Contains(public, `"detail":`) {
		t.Fatalf("untyped Record tool did not fail closed: %s", public)
	}
}

func TestPublicSupervisorToolJSONFailsClosed(t *testing.T) {
	if got := string(publicSupervisorToolJSON(`{"stdin":`)); got != `{"redacted":true,"unavailable":true}` {
		t.Fatalf("invalid projection=%s", got)
	}
}

func TestPublicSupervisorToolJSONRedactsCustomProviderHeadersAndPrefixedSecrets(t *testing.T) {
	raw := `{"request_headers":{"x-api-key":"unstructured-secret-value","Accept":"application/json"},` +
		`"github_token":"another-unstructured-value","nested":{"database_password":"third-value"},` +
		`"safe":"visible"}`
	projected := string(publicSupervisorToolJSON(raw))
	for _, forbidden := range []string{"unstructured-secret-value", "another-unstructured-value",
		"third-value", "application/json"} {
		if strings.Contains(projected, forbidden) {
			t.Fatalf("Record projection leaked %q: %s", forbidden, projected)
		}
	}
	if !strings.Contains(projected, `"safe":"visible"`) ||
		!strings.Contains(projected, recordRedactedValue) {
		t.Fatalf("Record projection did not preserve safe structure: %s", projected)
	}
}

func TestPublicSupervisorToolJSONOmitsSecretMaterialUsedAsAFieldName(t *testing.T) {
	const secretKey = "sk-proj-abcdefghijklmnopqrstuvwxyz123456"
	projected := string(publicSupervisorToolJSON(`{"` + secretKey + `":"value",` +
		`"nested":{"safe":"visible"}}`))
	if strings.Contains(projected, secretKey) || strings.Contains(projected, "value") {
		t.Fatalf("Record projection leaked a secret-bearing JSON field name: %s", projected)
	}
	if !strings.Contains(projected, `"safe":"visible"`) {
		t.Fatalf("Record projection removed unrelated safe structure: %s", projected)
	}
}
