package toolgateway

import (
	"encoding/json"
	"testing"
)

func TestAgentBrowserTargetStrictSnapshotAndExclusiveChoice(t *testing.T) {
	for _, raw := range []string{
		`{"version":"browser_click.v2","snapshot_id":"current","target":{"name":"加入队列","role":"button"}}`,
		`{"version":"browser_click.v2","snapshot_id":"current","element_ref":"current-ref"}`,
	} {
		if _, err := NormalizeAgentBrowserPayload(BrowserClickTool, json.RawMessage(raw)); err != nil {
			t.Fatalf("valid current target: %v", err)
		}
	}
	for _, raw := range []string{
		`{"version":"browser_click.v2","target":{"name":"加入队列","role":"button"}}`,
		`{"version":"browser_click.v2","snapshot_id":"current","target":{"name":"加入队列","role":"button"},"element_ref":"another"}`,
		`{"version":"browser_click.v2","snapshot_id":"current","target":{"name":"加入队列","role":"button"},"element_ref":""}`,
		`{"version":"browser_click.v2","snapshot_id":"current","target":null,"element_ref":"another"}`,
		`{"version":"browser_click.v2","snapshot_id":"current","target":{"name":"","role":"button"}}`,
		`{"version":"browser_click.v2","snapshot_id":"current","target":{"name":"加入队列","role":"button","selector":"#submit"}}`,
		`{"version":"browser_click.v2","snapshot_id":"current","target":{"name":"token=synthetic-secret-value","role":"button"}}`,
		`{"version":"browser_click.v2","snapshot_id":"current","target":{"name":"token=[REDACTED:secret]","role":"button"}}`,
	} {
		if _, err := NormalizeAgentBrowserPayload(BrowserClickTool, json.RawMessage(raw)); err == nil {
			t.Fatalf("invalid target accepted: %s", raw)
		}
	}
	if _, err := NormalizeAgentBrowserPayload(BrowserSnapshotTool, json.RawMessage(`{"version":"browser_snapshot.v2","target":{"name":"加入队列","role":"button"}}`)); err == nil {
		t.Fatal("target leaked beyond input actions")
	}
	definition, ok := AgentBrowserToolDefinition(BrowserClickTool)
	if !ok {
		t.Fatal("definition missing")
	}
	var schema map[string]any
	if json.Unmarshal(definition.InputSchema, &schema) != nil || len(schema["oneOf"].([]any)) != 3 {
		t.Fatal("schema lost exclusive ref, target or coordinate choices")
	}
}
