package llm

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestOpenAICacheUsageNormalization(t *testing.T) {
	for _, test := range []struct {
		name, details     string
		read, creation    int
		reported, invalid bool
	}{
		{"legacy", "", 0, 0, false, false},
		{"null", "null", 0, 0, false, false},
		{"empty", `{}`, 0, 0, false, false},
		{"zero", `{"cached_tokens":0}`, 0, 0, true, false},
		{"read", `{"cached_tokens":6}`, 6, 0, true, false},
		{"write", `{"cached_tokens":6,"cache_write_tokens":3}`, 6, 3, true, false},
		{"negative", `{"cached_tokens":-1}`, 0, 0, false, true},
		{"too_many_read", `{"cached_tokens":11}`, 0, 0, false, true},
		{"overlapping_input", `{"cached_tokens":6,"cache_write_tokens":5}`, 0, 0, false, true},
		{"invalid_type", `{"cached_tokens":"6"}`, 0, 0, false, true},
	} {
		for _, responses := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/responses_%t", test.name, responses), func(t *testing.T) {
				raw := `{"prompt_tokens":10,"completion_tokens":3,"total_tokens":13`
				field := "prompt_tokens_details"
				if responses {
					raw = `{"input_tokens":10,"output_tokens":3,"total_tokens":13`
					field = "input_tokens_details"
				}
				if test.details != "" {
					raw += `,"` + field + `":` + test.details
				}
				raw += "}"
				var usage Usage
				var err error
				if responses {
					var wire openAIResponsesUsage
					err = json.Unmarshal([]byte(raw), &wire)
					if err == nil {
						usage, err = normalizeResponsesUsage(&wire)
					}
				} else {
					var wire openAIUsage
					err = json.Unmarshal([]byte(raw), &wire)
					if err == nil {
						usage, err = normalizeOpenAIUsage(wire)
					}
				}
				if test.invalid {
					if err == nil {
						t.Fatalf("invalid cache usage accepted: %+v", usage)
					}
					return
				}
				if err != nil {
					t.Fatal(err)
				}
				if usage.InputTokens != 10 || usage.OutputTokens != 3 || usage.TotalTokens != 13 || usage.CacheReadTokens != test.read || usage.CacheCreationTokens != test.creation || usage.CacheUsageReported != test.reported || usage.CacheCreationBreakdownKnown {
					t.Fatalf("wrong normalization: %+v", usage)
				}
			})
		}
	}
}

func TestAnthropicCacheUsageNormalization(t *testing.T) {
	for _, test := range []struct {
		name, cache                       string
		input, read, creation, five, hour int
		reported, known, invalid          bool
	}{
		{"legacy", "", 10, 0, 0, 0, 0, false, false, false},
		{"explicit_zero", `,"cache_read_input_tokens":0,"cache_creation_input_tokens":0`, 10, 0, 0, 0, 0, true, false, false},
		{"read", `,"cache_read_input_tokens":6`, 16, 6, 0, 0, 0, true, false, false},
		{"aggregate_creation", `,"cache_read_input_tokens":6,"cache_creation_input_tokens":5`, 21, 6, 5, 0, 0, true, false, false},
		{"mixed_ttl", `,"cache_read_input_tokens":6,"cache_creation_input_tokens":5,"cache_creation":{"ephemeral_5m_input_tokens":2,"ephemeral_1h_input_tokens":3}`, 21, 6, 5, 2, 3, true, true, false},
		{"detail_without_aggregate", `,"cache_creation":{"ephemeral_5m_input_tokens":2,"ephemeral_1h_input_tokens":3}`, 15, 0, 5, 2, 3, true, true, false},
		{"zero_breakdown", `,"cache_creation_input_tokens":0,"cache_creation":{"ephemeral_5m_input_tokens":0,"ephemeral_1h_input_tokens":0}`, 10, 0, 0, 0, 0, true, true, false},
		{"mismatched_aggregate", `,"cache_creation_input_tokens":6,"cache_creation":{"ephemeral_5m_input_tokens":2,"ephemeral_1h_input_tokens":3}`, 0, 0, 0, 0, 0, false, false, true},
		{"incomplete_breakdown", `,"cache_creation":{"ephemeral_5m_input_tokens":2}`, 0, 0, 0, 0, 0, false, false, true},
		{"negative_read", `,"cache_read_input_tokens":-1`, 0, 0, 0, 0, 0, false, false, true},
		{"negative_creation", `,"cache_creation_input_tokens":-1`, 0, 0, 0, 0, 0, false, false, true},
		{"negative_ttl", `,"cache_creation":{"ephemeral_5m_input_tokens":-1,"ephemeral_1h_input_tokens":3}`, 0, 0, 0, 0, 0, false, false, true},
		{"overflow", fmt.Sprintf(`,"cache_read_input_tokens":%d`, int(^uint(0)>>1)), 0, 0, 0, 0, 0, false, false, true},
	} {
		t.Run(test.name, func(t *testing.T) {
			var wire anthropicUsage
			if err := json.Unmarshal([]byte(`{"input_tokens":10,"output_tokens":3`+test.cache+`}`), &wire); err != nil {
				t.Fatal(err)
			}
			usage, err := wire.normalized()
			if test.invalid {
				if err == nil {
					t.Fatalf("invalid usage accepted %+v", usage)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if usage.InputTokens != test.input || usage.TotalTokens != test.input+3 || usage.CacheReadTokens != test.read || usage.CacheCreationTokens != test.creation || usage.CacheCreation5mTokens != test.five || usage.CacheCreation1hTokens != test.hour || usage.CacheUsageReported != test.reported || usage.CacheCreationBreakdownKnown != test.known {
				t.Fatalf("wrong normalization: %+v", usage)
			}
		})
	}
}

func TestOpenAILegacyDeepSeekCacheUsage(t *testing.T) {
	for _, test := range []struct {
		name, cache string
		invalid     bool
	}{
		{"legacy_hit_miss", `,"prompt_cache_hit_tokens":6,"prompt_cache_miss_tokens":4`, false},
		{"both_representations", `,"prompt_cache_hit_tokens":6,"prompt_cache_miss_tokens":4,"prompt_tokens_details":{"cached_tokens":6}`, false},
		{"hit_only", `,"prompt_cache_hit_tokens":6`, false},
		{"conflicting_hit", `,"prompt_cache_hit_tokens":6,"prompt_tokens_details":{"cached_tokens":5}`, true},
		{"inconsistent_miss", `,"prompt_cache_hit_tokens":6,"prompt_cache_miss_tokens":5`, true},
		{"negative_hit", `,"prompt_cache_hit_tokens":-1`, true},
		{"negative_miss", `,"prompt_cache_hit_tokens":6,"prompt_cache_miss_tokens":-1`, true},
		{"missing_hit", `,"prompt_cache_miss_tokens":4`, true},
	} {
		t.Run(test.name, func(t *testing.T) {
			var wire openAIUsage
			if err := json.Unmarshal([]byte(`{"prompt_tokens":10,"completion_tokens":3,"total_tokens":13`+test.cache+`}`), &wire); err != nil {
				t.Fatal(err)
			}
			usage, err := normalizeOpenAIUsage(wire)
			if test.invalid {
				if err == nil {
					t.Fatalf("invalid legacy cache usage accepted: %+v", usage)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if usage.InputTokens != 10 || usage.TotalTokens != 13 || usage.CacheReadTokens != 6 || !usage.CacheUsageReported {
				t.Fatalf("legacy cache hit was lost or counted twice: %+v", usage)
			}
		})
	}
}

func TestAnthropicCacheUsageCumulativeMerge(t *testing.T) {
	var wire anthropicUsage
	if err := json.Unmarshal([]byte(`{"input_tokens":0,"output_tokens":0,"cache_read_input_tokens":6,"cache_creation_input_tokens":5,"cache_creation":{"ephemeral_5m_input_tokens":2,"ephemeral_1h_input_tokens":3}}`), &wire); err != nil {
		t.Fatal(err)
	}
	for _, raw := range []string{`{"output_tokens":2}`, `{"input_tokens":4,"output_tokens":3,"cache_read_input_tokens":6}`, `{"output_tokens":3}`} {
		var update anthropicUsage
		_ = json.Unmarshal([]byte(raw), &update)
		var err error
		wire, err = wire.merge(update)
		if err != nil {
			t.Fatal(err)
		}
	}
	usage, err := wire.normalized()
	if err != nil {
		t.Fatal(err)
	}
	if usage.InputTokens != 15 || usage.OutputTokens != 3 || usage.TotalTokens != 18 || usage.CacheReadTokens != 6 || usage.CacheCreationTokens != 5 || usage.CacheCreation5mTokens != 2 || usage.CacheCreation1hTokens != 3 || !usage.CacheCreationBreakdownKnown {
		t.Fatalf("cumulative usage added duplicates or lost cache fields: %+v", usage)
	}
	for _, raw := range []string{`{"input_tokens":0}`, `{"output_tokens":2}`, `{"cache_read_input_tokens":0}`, `{"cache_creation_input_tokens":6}`, `{"cache_creation":{"ephemeral_5m_input_tokens":0}}`, `{"cache_creation_input_tokens":-1}`} {
		var update anthropicUsage
		_ = json.Unmarshal([]byte(raw), &update)
		if _, err := wire.merge(update); err == nil {
			t.Fatalf("invalid cumulative update accepted: %s", raw)
		}
	}
}

func TestAnthropicCacheUsageActualHTTPAndSSE(t *testing.T) {
	for _, stream := range []bool{false, true} {
		t.Run(fmt.Sprintf("stream_%t", stream), func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				if !stream {
					_, _ = fmt.Fprint(w, `{"id":"message","type":"message","role":"assistant","model":"model","content":[{"type":"text","text":"ok"}],"stop_reason":"end_turn","usage":{"input_tokens":4,"output_tokens":3,"cache_read_input_tokens":6,"cache_creation_input_tokens":5,"cache_creation":{"ephemeral_5m_input_tokens":2,"ephemeral_1h_input_tokens":3}}}`)
					return
				}
				w.Header().Set("Content-Type", "text/event-stream")
				for _, payload := range []string{
					`{"type":"message_start","message":{"id":"message","model":"model","usage":{"input_tokens":4,"output_tokens":0,"cache_read_input_tokens":6,"cache_creation_input_tokens":5,"cache_creation":{"ephemeral_5m_input_tokens":2,"ephemeral_1h_input_tokens":3}}}}`,
					`{"type":"content_block_start","index":0,"content_block":{"type":"text","text":"ok"}}`,
					`{"type":"content_block_stop","index":0}`,
					`{"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":3}}`,
					`{"type":"message_stop"}`,
				} {
					_, _ = fmt.Fprintf(w, "data: %s\n\n", payload)
				}
			}))
			defer server.Close()
			provider, err := NewAnthropicCompatibleProvider(AnthropicCompatibleConfig{Name: "anthropic-test", BaseURL: server.URL, APIKey: "test", DefaultModel: "model"})
			if err != nil {
				t.Fatal(err)
			}
			request := ChatRequest{Messages: []Message{{Role: "user", Content: "inspect"}}}
			var usage Usage
			if stream {
				chunks, err := provider.StreamChat(t.Context(), request)
				if err != nil {
					t.Fatal(err)
				}
				accumulator, _ := NewItemStreamAccumulator("cached-anthropic", "anthropic-test", "model")
				for chunk := range chunks {
					if chunk.Err != nil {
						t.Fatal(chunk.Err)
					}
					if _, _, err := accumulator.Consume(chunk); err != nil {
						t.Fatal(err)
					}
					if chunk.Done {
						usage = *chunk.Usage
					}
				}
			} else {
				response, err := provider.Chat(t.Context(), request)
				if err != nil {
					t.Fatal(err)
				}
				usage = response.Usage
			}
			if usage.InputTokens != 15 || usage.TotalTokens != 18 || usage.CacheReadTokens != 6 || usage.CacheCreationTokens != 5 || usage.CacheCreation5mTokens != 2 || usage.CacheCreation1hTokens != 3 || !usage.CacheCreationBreakdownKnown {
				t.Fatalf("HTTP cache usage lost or duplicated: %+v", usage)
			}
		})
	}
}

func TestResponsesCacheUsageActualHTTPAndSSE(t *testing.T) {
	for _, stream := range []bool{false, true} {
		t.Run(fmt.Sprintf("stream_%t", stream), func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				response := `{"id":"response","object":"response","status":"completed","model":"model-local","output":[{"id":"message","type":"message","status":"completed","role":"assistant","content":[{"type":"output_text","text":"ok"}]}],"usage":{"input_tokens":10,"output_tokens":3,"total_tokens":13,"input_tokens_details":{"cached_tokens":6,"cache_write_tokens":2}}}`
				if !stream {
					_, _ = fmt.Fprint(w, response)
					return
				}
				w.Header().Set("Content-Type", "text/event-stream")
				for _, payload := range []string{
					`{"type":"response.created","response":{"id":"response","object":"response","status":"in_progress","model":"model-local"}}`,
					`{"type":"response.output_item.added","item":{"id":"message","type":"message","status":"in_progress","role":"assistant"}}`,
					`{"type":"response.output_item.done","item":{"id":"message","type":"message","status":"completed","role":"assistant","content":[{"type":"output_text","text":"ok"}]}}`,
					`{"type":"response.completed","response":` + response + `}`,
				} {
					_, _ = fmt.Fprintf(w, "data: %s\n\n", payload)
				}
			}))
			defer server.Close()
			provider := newTestResponsesProvider(t, server.URL)
			request := ChatRequest{Messages: []Message{{Role: "user", Content: "inspect"}}}
			var usage Usage
			if stream {
				chunks, err := provider.StreamChat(t.Context(), request)
				if err != nil {
					t.Fatal(err)
				}
				for chunk := range chunks {
					if chunk.Err != nil {
						t.Fatal(chunk.Err)
					}
					if chunk.Done {
						usage = *chunk.Usage
					}
				}
			} else {
				response, err := provider.Chat(t.Context(), request)
				if err != nil {
					t.Fatal(err)
				}
				usage = response.Usage
			}
			if usage.InputTokens != 10 || usage.TotalTokens != 13 || usage.CacheReadTokens != 6 || usage.CacheCreationTokens != 2 || !usage.CacheUsageReported || usage.CacheCreationBreakdownKnown {
				t.Fatalf("Responses cache usage lost: %+v", usage)
			}
		})
	}
}

func TestUsageCacheValidationAndLegacyJSON(t *testing.T) {
	var legacy Usage
	if err := json.Unmarshal([]byte(`{"input_tokens":10,"output_tokens":3,"total_tokens":13}`), &legacy); err != nil {
		t.Fatal(err)
	}
	if err := legacy.Validate(); err != nil {
		t.Fatal(err)
	}
	encoded, _ := json.Marshal(legacy)
	if strings.Contains(string(encoded), "cache_") {
		t.Fatalf("legacy zero cache fields changed JSON: %s", encoded)
	}
	for _, usage := range []Usage{
		{InputTokens: 10, CacheReadTokens: -1},
		{InputTokens: 10, CacheReadTokens: 6, CacheCreationTokens: 5},
		{InputTokens: 10, CacheCreationTokens: 5, CacheCreation5mTokens: 6},
		{InputTokens: 10, CacheCreationTokens: 5, CacheCreation5mTokens: 2, CacheCreation1hTokens: 2, CacheCreationBreakdownKnown: true},
	} {
		if err := usage.Validate(); err == nil {
			t.Fatalf("invalid usage accepted %+v", usage)
		}
	}
}
