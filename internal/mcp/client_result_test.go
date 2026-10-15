package mcp

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestBoundResultJSONKeepsStructuredResultsAndExplainsMediaLimit(t *testing.T) {
	raw := json.RawMessage(`{"content":[{"type":"image","mimeType":"image/png","data":"` + strings.Repeat("a", 4096) + `"}],"structuredContent":{"count":9007199254740993}}`)
	result, truncated := BoundResultJSON(raw, 512)
	if !truncated || !json.Valid([]byte(result)) || len(result) > 512 || strings.Contains(result, strings.Repeat("a", 20)) ||
		!strings.Contains(result, "9007199254740993") || !strings.Contains(result, "Binary content omitted") {
		t.Fatalf("invalid bounded media result: %s", result)
	}
	result, truncated = BoundResultJSON([]byte(`{"content":[],"structuredContent":{"count":9007199254740993}}`), 512)
	if truncated || !strings.Contains(result, "9007199254740993") {
		t.Fatal("small structured result changed")
	}
}

func TestBoundResultJSONLimitsEncodedBytesAndPreservesUTF8(t *testing.T) {
	raw, _ := json.Marshal(map[string]any{"content": []map[string]string{{"type": "text", "text": strings.Repeat("中文\"\\<>", 1000)}}})
	for _, limit := range []int{1, 2, 64, 256, 1024} {
		result, truncated := BoundResultJSON(raw, limit)
		if !truncated || len(result) > limit || !json.Valid([]byte(result)) {
			t.Fatalf("limit %d produced %d invalid bytes: %s", limit, len(result), result)
		}
	}
}
