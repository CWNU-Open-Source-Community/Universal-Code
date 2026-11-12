package hooks

import (
	"encoding/json"
	"strings"
	"unicode/utf8"
)

// UserPromptSubmitPayload is the bounded Go-owned payload delivered to hooks
// at the user_prompt_submit boundary. The operator's text is untrusted data:
// hooks can inspect it to deny or annotate, and it never gains authority from
// being inspected.
type UserPromptSubmitPayload struct {
	ThreadID  string `json:"thread_id,omitempty"`
	SessionID string `json:"session_id,omitempty"`
	RunID     string `json:"run_id,omitempty"`
	Content   string `json:"content"`
	Truncated bool   `json:"truncated,omitempty"`
}

// BuildUserPromptSubmitPayload encodes the prompt-submission payload and
// clips the operator text on a UTF-8 boundary when the encoding would exceed
// the hook input limit, so an oversized prompt fails no boundary. Content
// with invalid UTF-8 bytes is sanitized once, because the hook sees the
// JSON-encoded form either way.
func BuildUserPromptSubmitPayload(payload UserPromptSubmitPayload) json.RawMessage {
	if !utf8.ValidString(payload.Content) {
		payload.Content = strings.ToValidUTF8(payload.Content, "")
	}
	content := payload.Content
	limit := len(content)
	for {
		clipped := content[:limit]
		for len(clipped) > 0 && !utf8.ValidString(clipped) {
			clipped = clipped[:len(clipped)-1]
		}
		payload.Content = clipped
		payload.Truncated = len(clipped) < len(content)
		raw, err := json.Marshal(payload)
		if err == nil && len(raw) <= MaxPayloadBytes {
			return raw
		}
		if limit == 0 {
			payload.Content = ""
			payload.Truncated = len(content) > 0
			raw, _ = json.Marshal(payload)
			return raw
		}
		limit = utf8.RuneCountInString(clipped) / 2
	}
}
