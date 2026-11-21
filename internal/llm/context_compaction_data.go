package llm

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"unicode/utf8"

	"cyberagent-workbench/internal/redact"
)

const maxContextCompactionDataBytes = redact.MaxModelDataBytes

// ContextCompactionDataMessage builds an inert user data message. Every string
// leaf (including JSON stored inside summary/continuity Content strings) is
// redacted before applying the private byte marker. The router honors it only
// for context_compaction requests; it does not grant instructions or tool access.
func ContextCompactionDataMessage(raw json.RawMessage) (Message, error) {
	if len(raw) == 0 || len(raw) > maxContextCompactionDataBytes || !utf8.Valid(raw) {
		return Message{}, errors.New("compaction data must be bounded UTF-8 JSON")
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return Message{}, err
	}
	if err := ensureModelJSONEOF(decoder); err != nil {
		return Message{}, err
	}
	if _, ok := value.(map[string]any); !ok {
		return Message{}, errors.New("compaction data must be a JSON object")
	}
	safe, err := redact.ModelJSONValue(value, true)
	if err != nil {
		return Message{}, err
	}
	content, err := json.Marshal(safe)
	if err != nil {
		return Message{}, err
	}
	if len(content) > maxContextCompactionDataBytes {
		return Message{}, errors.New("redacted compaction data exceeds its byte bound")
	}
	return Message{Role: "user", Content: string(content), contextCompactionContentSHA256: compactionDataDigest(string(content))}, nil
}

func (m Message) preservesContextCompactionData() bool {
	return m.Role == "user" && len(m.ToolCalls) == 0 && len(m.ToolResults) == 0 && len(m.Images) == 0 &&
		m.contextCompactionContentSHA256 != "" && m.contextCompactionContentSHA256 == compactionDataDigest(m.Content)
}

func compactionDataDigest(content string) string {
	digest := sha256.Sum256([]byte(content))
	return hex.EncodeToString(digest[:])
}
