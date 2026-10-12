package llm

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestOpenAIIndependentRefusalNonStreaming(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = fmt.Fprint(w, `{"model":"upstream-snapshot","choices":[{"index":0,"message":{"role":"assistant","content":null,"refusal":"private refusal canary"},"finish_reason":"stop"}],"usage":{"prompt_tokens":10,"completion_tokens":3,"total_tokens":13,"prompt_tokens_details":{"cached_tokens":6}}}`)
	}))
	defer server.Close()
	response, err := newTestOpenAIProvider(t, server.URL).Chat(t.Context(), ChatRequest{Messages: []Message{{Role: "user", Content: "inspect"}}})
	assertLimitedResponse(t, response, err, FinishReasonRefusal, ProviderFailureRefusal, 13)
	if response.Provider != "openai" || response.Model != "gpt-test" || response.Text != "" || response.Replay != nil ||
		response.Usage.CacheReadTokens != 6 || !response.Usage.CacheUsageReported || strings.Contains(err.Error(), "canary") {
		t.Fatalf("refusal lost identity/cache usage or exposed private output: response=%+v err=%v", response, err)
	}
}

func TestOpenAIIndependentRefusalStreamingUsageForms(t *testing.T) {
	for _, attached := range []bool{false, true} {
		t.Run(fmt.Sprintf("attached_%t", attached), func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "text/event-stream")
				_, _ = fmt.Fprint(w, "data: {\"model\":\"snapshot\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"refusal\":\"private \"}}]}\n\n")
				_, _ = fmt.Fprint(w, "data: {\"model\":\"snapshot\",\"choices\":[{\"index\":0,\"delta\":{\"refusal\":\"refusal canary\"}}]}\n\n")
				usage := `"usage":{"prompt_tokens":10,"completion_tokens":3,"total_tokens":13,"prompt_tokens_details":{"cached_tokens":6}}`
				finish := `{"model":"snapshot","choices":[{"index":0,"delta":{},"finish_reason":"stop"}]`
				if attached {
					finish += "," + usage
				}
				_, _ = fmt.Fprintf(w, "data: %s}\n\n", finish)
				if !attached {
					_, _ = fmt.Fprintf(w, "data: {\"model\":\"snapshot\",\"choices\":[],%s}\n\n", usage)
				}
				_, _ = fmt.Fprint(w, "data: [DONE]\n\n")
			}))
			defer server.Close()
			chunks, err := newTestOpenAIProvider(t, server.URL).StreamChat(t.Context(), ChatRequest{Messages: []Message{{Role: "user", Content: "inspect"}}})
			if err != nil {
				t.Fatal(err)
			}
			accumulator, _ := NewItemStreamAccumulator("refusal-fixture", "openai", "gpt-test")
			failures, terminals := 0, 0
			for chunk := range chunks {
				if _, _, err := accumulator.Consume(chunk); err != nil {
					t.Fatal(err)
				}
				if chunk.Text != "" || chunk.Done || len(chunk.ToolCalls) != 0 || chunk.Replay != nil {
					t.Fatalf("refusal exposed successful output: %+v", chunk)
				}
				for _, event := range chunk.Events {
					if event.Type == StreamResponseCompleted {
						t.Fatal("refusal published success")
					}
					if event.Type == StreamResponseFailed {
						terminals++
					}
				}
				if chunk.Err != nil {
					failures++
					if ProviderErrorReason(chunk.Err) != ProviderFailureRefusal || chunk.FinishReason != FinishReasonRefusal ||
						chunk.Usage == nil || chunk.Usage.TotalTokens != 13 || chunk.Usage.CacheReadTokens != 6 ||
						chunk.Model != "gpt-test" || chunk.Provider != "openai" || strings.Contains(chunk.Err.Error(), "canary") {
						t.Fatalf("refusal lost classification or usage: %+v", chunk)
					}
				}
			}
			if failures != 1 || terminals != 1 {
				t.Fatalf("failures=%d terminals=%d", failures, terminals)
			}
		})
	}
}

func TestOpenAIRefusalFieldCompatibilityAndValidation(t *testing.T) {
	for _, test := range []struct {
		name, refusal, content, finish string
		tools                          bool
		want                           ProviderFailureReason
	}{
		{"omitted_text", "", `"ok"`, "stop", false, ProviderFailureNone},
		{"null_text", "null", `"ok"`, "stop", false, ProviderFailureNone},
		{"empty_text", `""`, `"ok"`, "stop", false, ProviderFailureNone},
		{"empty_stop", `""`, "null", "stop", false, ProviderFailureProtocolIncompatible},
		{"null_stop", "null", "null", "stop", false, ProviderFailureProtocolIncompatible},
		{"number", "1", "null", "stop", false, ProviderFailureProtocolIncompatible},
		{"object", `{}`, "null", "stop", false, ProviderFailureProtocolIncompatible},
		{"invalid_surrogate", `"\ud800"`, "null", "stop", false, ProviderFailureProtocolIncompatible},
		{"too_large", fmt.Sprintf("%q", strings.Repeat("x", MaxModelOutputBytes+1)), "null", "stop", false, ProviderFailureProtocolIncompatible},
		{"mixed_text", `"refused"`, `"ok"`, "stop", false, ProviderFailureProtocolIncompatible},
		{"mixed_tool", `"refused"`, "null", "tool_calls", true, ProviderFailureProtocolIncompatible},
		{"incompatible_finish", `"refused"`, "null", "length", false, ProviderFailureProtocolIncompatible},
		{"content_filter", "", "null", "content_filter", false, ProviderFailureRefusal},
	} {
		t.Run(test.name, func(t *testing.T) {
			message := `{"role":"assistant","content":` + test.content
			if test.refusal != "" {
				message += `,"refusal":` + test.refusal
			}
			if test.tools {
				message += `,"tool_calls":[{"id":"call","type":"function","function":{"name":"read_file","arguments":"{}"}}]`
			}
			message += "}"
			var response openAIChatResponse
			if err := json.Unmarshal([]byte(`{"model":"snapshot","choices":[{"index":0,"message":`+message+`,"finish_reason":"`+test.finish+`"}],"usage":{"prompt_tokens":2,"completion_tokens":3,"total_tokens":5}}`), &response); err != nil {
				t.Fatal(err)
			}
			provider := &OpenAICompatibleProvider{name: "test"}
			result, err := provider.normalizeResponse("selected", response)
			if ProviderErrorReason(err) != test.want {
				t.Fatalf("result=%+v err=%v", result, err)
			}
			if test.want != ProviderFailureNone && result != nil && (len(result.ToolCalls) != 0 || result.Text != "") {
				t.Fatal("failure exposed mixed output")
			}
		})
	}
	if _, err := decodeOpenAIRefusal(json.RawMessage{'"', 0xff, '"'}); err == nil {
		t.Fatal("invalid UTF-8 refusal accepted")
	}
}

func TestOpenAIStreamRefusalBoundsAndMixedOutput(t *testing.T) {
	for _, test := range []struct {
		name                string
		deltas              []string
		finish, usage, done string
		want                ProviderFailureReason
	}{
		{"over_bound", []string{`{"refusal":` + fmt.Sprintf("%q", strings.Repeat("x", MaxModelOutputBytes)) + `}`, `{"refusal":"x"}`}, "stop", `{"prompt_tokens":2,"completion_tokens":3,"total_tokens":5}`, "[DONE]", ProviderFailureProtocolIncompatible},
		{"mixed_text", []string{`{"refusal":"private"}`, `{"content":"ok"}`}, "stop", `{"prompt_tokens":2,"completion_tokens":3,"total_tokens":5}`, "[DONE]", ProviderFailureProtocolIncompatible},
		{"mixed_tools", []string{`{"tool_calls":[{"index":0,"id":"call","type":"function","function":{"name":"read_file","arguments":"{}"}}]}`, `{"refusal":"private"}`}, "tool_calls", `{"prompt_tokens":2,"completion_tokens":3,"total_tokens":5}`, "[DONE]", ProviderFailureProtocolIncompatible},
		{"missing_usage", []string{`{"refusal":"private"}`}, "stop", "", "[DONE]", ProviderFailureProtocolIncompatible},
		{"missing_finish", []string{`{"refusal":"private"}`}, "", `{"prompt_tokens":2,"completion_tokens":3,"total_tokens":5}`, "[DONE]", ProviderFailureProtocolIncompatible},
		{"missing_done", []string{`{"refusal":"private"}`}, "stop", `{"prompt_tokens":2,"completion_tokens":3,"total_tokens":5}`, "", ProviderFailureProtocolIncompatible},
	} {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "text/event-stream")
				for _, delta := range test.deltas {
					_, _ = fmt.Fprintf(w, "data: {\"model\":\"snapshot\",\"choices\":[{\"index\":0,\"delta\":%s}]}\n\n", delta)
				}
				if test.finish != "" {
					_, _ = fmt.Fprintf(w, "data: {\"model\":\"snapshot\",\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":%q}]}\n\n", test.finish)
				}
				if test.usage != "" {
					_, _ = fmt.Fprintf(w, "data: {\"model\":\"snapshot\",\"choices\":[],\"usage\":%s}\n\n", test.usage)
				}
				if test.done != "" {
					_, _ = fmt.Fprintf(w, "data: %s\n\n", test.done)
				}
			}))
			defer server.Close()
			chunks, err := newTestOpenAIProvider(t, server.URL).StreamChat(t.Context(), ChatRequest{Messages: []Message{{Role: "user", Content: "inspect"}}})
			if err != nil {
				t.Fatal(err)
			}
			failures := 0
			for chunk := range chunks {
				if chunk.Done || len(chunk.ToolCalls) > 0 || chunk.Replay != nil {
					t.Fatal("invalid refusal stream accepted output")
				}
				if chunk.Err != nil {
					failures++
					if ProviderErrorReason(chunk.Err) != test.want {
						t.Fatal(chunk.Err)
					}
				}
			}
			if failures != 1 {
				t.Fatalf("failure count %d", failures)
			}
		})
	}
}

func TestOpenAIStreamNullableRefusalPreservesOrdinaryOutput(t *testing.T) {
	for _, refusal := range []string{"", "null", `""`} {
		t.Run("field_"+refusal, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "text/event-stream")
				delta := `{"content":"ok"`
				if refusal != "" {
					delta += `,"refusal":` + refusal
				}
				delta += "}"
				_, _ = fmt.Fprintf(w, "data: {\"model\":\"snapshot\",\"choices\":[{\"index\":0,\"delta\":%s}]}\n\n", delta)
				_, _ = fmt.Fprint(w, "data: {\"model\":\"snapshot\",\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}],\"usage\":{\"prompt_tokens\":2,\"completion_tokens\":3,\"total_tokens\":5}}\n\ndata: [DONE]\n\n")
			}))
			defer server.Close()
			chunks, err := newTestOpenAIProvider(t, server.URL).StreamChat(t.Context(), ChatRequest{Messages: []Message{{Role: "user", Content: "inspect"}}})
			if err != nil {
				t.Fatal(err)
			}
			text, completed := "", false
			for chunk := range chunks {
				if chunk.Err != nil {
					t.Fatal(chunk.Err)
				}
				text += chunk.Text
				if chunk.Done {
					completed = true
					if chunk.FinishReason != FinishReasonStop || chunk.Usage == nil || chunk.Usage.TotalTokens != 5 {
						t.Fatalf("ordinary completion changed: %+v", chunk)
					}
				}
			}
			if !completed || text != "ok" {
				t.Fatalf("ordinary stream lost output: completed=%t text=%q", completed, text)
			}
		})
	}
}
