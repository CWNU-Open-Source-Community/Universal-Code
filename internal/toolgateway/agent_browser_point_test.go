package toolgateway

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/santhosh-tekuri/jsonschema/v5"
)

func TestAgentBrowserCoordinatePayloadAndSchemaAgree(t *testing.T) {
	point := `"point":{"x":40,"y":30,"document_epoch":2,"viewport":{"width":800,"height":600}}`
	cases := []struct {
		name    ToolName
		payload string
		valid   bool
	}{
		{BrowserClickTool, `{"version":"browser_click.v2",` + point + `}`, true},
		{BrowserMoveTool, `{"version":"browser_move.v2",` + point + `}`, true},
		{BrowserClickTool, `{"version":"browser_click.v2","snapshot_id":"fresh","element_ref":"ref"}`, true},
		{BrowserClickTool, `{"version":"browser_click.v2",` + point + `,"element_ref":"ref"}`, false},
		{BrowserClickTool, `{"version":"browser_click.v2",` + point + `,"snapshot_id":"fresh","target":{"name":"Submit","role":"button"}}`, false},
		{BrowserClickTool, `{"version":"browser_click.v2","point":null}`, false},
		{BrowserMoveTool, `{"version":"browser_move.v2"}`, false},
		{BrowserMoveTool, `{"version":"browser_move.v2","point":{"y":30,"document_epoch":2,"viewport":{"width":800,"height":600}}}`, false},
		{BrowserClickTool, `{"version":"browser_click.v2","point":{"x":-1,"y":30,"document_epoch":2,"viewport":{"width":800,"height":600}}}`, false},
		{BrowserClickTool, `{"version":"browser_click.v2","point":{"x":1,"y":30,"document_epoch":0,"viewport":{"width":800,"height":600}}}`, false},
	}
	for _, tc := range cases {
		t.Run(string(tc.name)+tc.payload, func(t *testing.T) {
			canonical, err := NormalizeAgentBrowserPayload(tc.name, json.RawMessage(tc.payload))
			if (err == nil) != tc.valid {
				t.Fatalf("normalizer=%s err=%v valid=%t", canonical, err, tc.valid)
			}
			definition, ok := AgentBrowserToolDefinition(tc.name)
			if !ok {
				t.Fatal("definition missing")
			}
			compiler := jsonschema.NewCompiler()
			if err := compiler.AddResource("browser.json", bytes.NewReader(definition.InputSchema)); err != nil {
				t.Fatal(err)
			}
			schema, err := compiler.Compile("browser.json")
			if err != nil {
				t.Fatal(err)
			}
			var value any
			if err := json.Unmarshal([]byte(tc.payload), &value); err != nil {
				t.Fatal(err)
			}
			if accepted := schema.Validate(value) == nil; accepted != tc.valid {
				t.Fatalf("schema accepted=%t valid=%t payload=%s", accepted, tc.valid, tc.payload)
			}
		})
	}
	// Cross-field viewport bounds are enforced by the normalizer and runtime;
	// standard JSON Schema cannot compare x directly with point.viewport.width.
	if _, err := NormalizeAgentBrowserPayload(BrowserClickTool, json.RawMessage(`{"version":"browser_click.v2","point":{"x":800,"y":30,"document_epoch":2,"viewport":{"width":800,"height":600}}}`)); err == nil {
		t.Fatal("coordinate outside observed viewport accepted")
	}
	move, ok := AgentBrowserToolDefinition(BrowserMoveTool)
	if !ok || !IsBrowserActionTool(BrowserMoveTool) || !strings.Contains(move.Description, "THIS SAME browser page") || !strings.Contains(move.Description, "CSS viewport pixels") {
		t.Fatal("coordinate API is undiscoverable or separate from browser")
	}
}
