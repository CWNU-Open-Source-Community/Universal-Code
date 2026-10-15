package mcp

import (
	"bytes"
	"encoding/json"
)

// BoundResultJSON keeps a received result parseable at the persistence boundary.
// The caller must redact before calling it. Large media is omitted explicitly;
// an oversized remainder becomes a bounded preview, never half a JSON object.
func BoundResultJSON(raw json.RawMessage, limit int) (string, bool) {
	if len(raw) <= limit {
		return string(raw), false
	}
	var value map[string]any
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	if decoder.Decode(&value) == nil && value != nil {
		omit := func(value map[string]any, key string) {
			if _, found := value[key]; found {
				delete(value, key)
				value["host_notice"] = "Binary content omitted: MCP result exceeds the configured byte limit."
			}
		}
		block := func(item any) {
			if item, ok := item.(map[string]any); ok {
				if item["type"] == "image" || item["type"] == "audio" {
					omit(item, "data")
				}
				if resource, ok := item["resource"].(map[string]any); ok && item["type"] == "resource" {
					omit(resource, "blob")
				}
			}
		}
		if content, ok := value["content"].([]any); ok {
			for _, item := range content {
				block(item)
			}
		}
		if contents, ok := value["contents"].([]any); ok {
			for _, item := range contents {
				if item, ok := item.(map[string]any); ok {
					omit(item, "blob")
				}
			}
		}
		if messages, ok := value["messages"].([]any); ok {
			for _, item := range messages {
				if item, ok := item.(map[string]any); ok {
					block(item["content"])
				}
			}
		}
		raw, _ = json.Marshal(value)
		if len(raw) <= limit {
			return string(raw), true
		}
	}
	// JSON escaping can expand a preview, so size the encoded response itself.
	preview := func(n int) []byte {
		result, _ := json.Marshal(map[string]any{"truncated": true, "host_notice": "MCP result exceeded the configured byte limit; preview only.", "preview": truncateClientUTF8(string(raw), n)})
		return result
	}
	lo, hi := 0, len(raw)
	result := preview(0)
	if len(result) > limit {
		if limit >= 2 {
			return "{}", true
		}
		return "0", true
	}
	for lo <= hi {
		mid := lo + (hi-lo)/2
		candidate := preview(mid)
		if len(candidate) <= limit {
			result, lo = candidate, mid+1
		} else {
			hi = mid - 1
		}
	}
	return string(result), true
}
