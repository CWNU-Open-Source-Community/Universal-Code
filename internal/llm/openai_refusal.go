package llm

import (
	"bytes"
	"encoding/json"
	"errors"
	"unicode/utf8"
)

// Refusal text remains private. Only its presence and bounded byte count are
// retained while streaming; public outcomes use the existing fixed diagnostic.
func decodeOpenAIRefusal(raw json.RawMessage) (string, error) {
	if len(raw) == 0 || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return "", nil
	}
	if !utf8.Valid(raw) || !anthropicReplayJSONUnicode(raw) {
		return "", errors.New("refusal is not valid Unicode")
	}
	var refusal string
	if json.Unmarshal(raw, &refusal) != nil || len(refusal) > MaxModelOutputBytes {
		return "", errors.New("refusal is not a bounded string")
	}
	return refusal, nil
}
