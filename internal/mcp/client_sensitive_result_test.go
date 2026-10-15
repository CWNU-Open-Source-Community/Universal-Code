package mcp

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

func TestClientSanitizesSensitiveResultFieldsBeforeTruncation(t *testing.T) {
	passwordCanary := "short phrase canary"
	authCanary := "ordinary auth canary"
	content, err := json.Marshal(map[string]any{
		"a_password":    passwordCanary,
		"b_auth_header": authCanary,
		"z_padding":     strings.Repeat("x", 512),
	})
	if err != nil {
		t.Fatal(err)
	}
	client, _ := sdkHTTPFixture(t, func(w http.ResponseWriter, r *http.Request, request Envelope) {
		result, err := json.Marshal(map[string]json.RawMessage{"content": json.RawMessage("[]"), "structuredContent": content})
		if err != nil {
			t.Error(err)
			return
		}
		sdkWriteResponse(w, request.ID, string(result))
	})
	result, err := client.CallTool(t.Context(), "lookup", json.RawMessage(`{}`), 128)
	if err != nil || !result.Truncated || strings.Contains(result.Content, passwordCanary) ||
		strings.Contains(result.Content, authCanary) ||
		strings.Contains(result.FullContent, passwordCanary) || strings.Contains(result.FullContent, authCanary) ||
		!strings.Contains(result.FullContent, "[REDACTED:sensitive-field]") || !json.Valid([]byte(result.Content)) {
		t.Fatalf("bounded MCP result was not sanitized before truncation: result=%#v err=%v",
			result, err)
	}
}
