package toolgateway

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"

	"cyberagent-workbench/internal/redact"
)

const mcpSensitiveFieldPlaceholder = redact.SensitiveJSONFieldPlaceholder

// normalizeMCPArguments makes the model-visible argument object deterministic
// and refuses inline credentials before they can reach either the transport or
// the durable Supervisor ledger. Credentials belong in the reviewed MCP server
// configuration and are referenced indirectly by the call.
func normalizeMCPArguments(raw json.RawMessage) (json.RawMessage, error) {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var value map[string]any
	if err := decoder.Decode(&value); err != nil || value == nil {
		return nil, errors.New("MCP tool arguments must be a JSON object")
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return nil, errors.New("MCP tool arguments contain trailing JSON")
	}
	if containsMCPInlineCredential(value) {
		return nil, errors.New("MCP tool arguments contain an inline credential; use an approved credential reference")
	}
	canonical, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	return canonical, nil
}

func containsMCPInlineCredential(value any) bool {
	switch typed := value.(type) {
	case map[string]any:
		for key, child := range typed {
			_, boolean := child.(bool)
			if redact.SensitiveJSONField(key, boolean) || redact.String(key) != key ||
				containsMCPInlineCredential(child) {
				return true
			}
		}
	case []any:
		for _, child := range typed {
			if containsMCPInlineCredential(child) {
				return true
			}
		}
	case string:
		return redact.String(typed) != typed
	}
	return false
}

// sanitizeMCPResultContent removes key-labelled credentials from JSON MCP
// output before it is copied into a durable tool result. Non-JSON output still
// receives the shared value-pattern redaction.
func sanitizeMCPResultContent(content string) string {
	canonical, err := redact.SanitizeSensitiveJSON([]byte(content))
	if err != nil {
		return redact.String(content)
	}
	return string(canonical)
}
