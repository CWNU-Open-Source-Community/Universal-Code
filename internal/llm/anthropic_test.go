package llm

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestAnthropicCompatibleProviderChat(t *testing.T) {
	var captured struct {
		Path             string
		APIKey           string
		AnthropicVersion string
		Body             anthropicMessageRequest
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		captured.Path = r.URL.Path
		captured.APIKey = r.Header.Get("x-api-key")
		captured.AnthropicVersion = r.Header.Get("anthropic-version")
		if err := json.NewDecoder(r.Body).Decode(&captured.Body); err != nil {
			t.Fatal(err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"id":"msg_test",
			"type":"message",
			"role":"assistant",
			"model":"test-model",
			"content":[{"type":"text","text":"hello from provider"}],
			"usage":{"input_tokens":7,"output_tokens":3}
		}`))
	}))
	defer server.Close()

	provider, err := NewAnthropicCompatibleProvider(AnthropicCompatibleConfig{
		Name:         "test",
		BaseURL:      server.URL,
		APIKey:       "secret",
		DefaultModel: "test-model",
	})
	if err != nil {
		t.Fatal(err)
	}
	resp, err := provider.Chat(context.Background(), ChatRequest{
		Messages: []Message{
			{Role: "system", Content: "be concise"},
			{Role: "user", Content: "say hello"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if captured.Path != "/v1/messages" {
		t.Fatalf("unexpected path: %s", captured.Path)
	}
	if captured.APIKey != "secret" {
		t.Fatalf("missing api key header")
	}
	if captured.AnthropicVersion != AnthropicVersion {
		t.Fatalf("unexpected anthropic version: %s", captured.AnthropicVersion)
	}
	if captured.Body.System != "be concise" {
		t.Fatalf("system prompt not lifted: %#v", captured.Body)
	}
	if captured.Body.Thinking != nil {
		t.Fatalf("generic Anthropic request unexpectedly changed thinking mode: %#v", captured.Body.Thinking)
	}
	if len(captured.Body.Messages) != 1 || captured.Body.Messages[0].Role != "user" || captured.Body.Messages[0].Content != "say hello" {
		t.Fatalf("unexpected messages: %#v", captured.Body.Messages)
	}
	if resp.Text != "hello from provider" || resp.Usage.TotalTokens != 10 {
		t.Fatalf("unexpected response: %#v", resp)
	}
}

func TestAnthropicCompatibleProviderCanDisableThinkingPerProvider(t *testing.T) {
	var captured anthropicMessageRequest
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&captured); err != nil {
			t.Fatal(err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"id":"msg_no_thinking","type":"message","role":"assistant",
			"model":"test-model","content":[{"type":"text","text":"ok"}],
			"usage":{"input_tokens":2,"output_tokens":1}
		}`))
	}))
	defer server.Close()
	provider, err := NewAnthropicCompatibleProvider(AnthropicCompatibleConfig{
		Name: "deepseek", BaseURL: server.URL, APIKey: "secret",
		DefaultModel: "test-model", DisableThinking: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := provider.Chat(t.Context(), ChatRequest{
		Messages: []Message{{Role: "user", Content: "ping"}}, MaxTokens: 8,
	}); err != nil {
		t.Fatal(err)
	}
	if captured.Thinking == nil || captured.Thinking.Type != "disabled" {
		t.Fatalf("provider-specific thinking override missing: %#v", captured.Thinking)
	}
}

func TestAnthropicCompatibleProviderDoesNotProjectThinkingBlocksAsText(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"id":"msg_thinking",
			"type":"message",
			"role":"assistant",
			"model":"thinking-model",
			"content":[
				{"type":"thinking","thinking":"private reasoning must stay private"},
				{"type":"text","text":"public result"}
			],
			"usage":{"input_tokens":5,"output_tokens":4}
		}`))
	}))
	defer server.Close()
	provider, err := NewAnthropicCompatibleProvider(AnthropicCompatibleConfig{
		Name: "test", BaseURL: server.URL, APIKey: "secret", DefaultModel: "thinking-model",
	})
	if err != nil {
		t.Fatal(err)
	}
	response, err := provider.Chat(t.Context(),
		ChatRequest{Messages: []Message{{Role: "user", Content: "inspect"}}})
	if err != nil {
		t.Fatal(err)
	}
	if response.Text != "public result" || strings.Contains(response.Text, "private reasoning") {
		t.Fatalf("provider thinking entered public text: %q", response.Text)
	}
}

func TestAnthropicCompatibleProviderStreamsSSEWithFinalUsage(t *testing.T) {
	var captured anthropicMessageRequest
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&captured); err != nil {
			t.Fatal(err)
		}
		if r.Header.Get("Accept") != "text/event-stream" {
			t.Errorf("missing streaming accept header: %q", r.Header.Get("Accept"))
		}
		w.Header().Set("Content-Type", "text/event-stream")
		flusher, ok := w.(http.Flusher)
		if !ok {
			t.Fatal("test server does not support flushing")
		}
		for _, payload := range []string{
			`{"type":"message_start","message":{"model":"stream-model","usage":{"input_tokens":7,"output_tokens":0}}}`,
			`{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`,
			`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"hello "}}`,
			`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"world"}}`,
			`{"type":"content_block_stop","index":0}`,
			`{"type":"message_delta","usage":{"output_tokens":2}}`,
			`{"type":"message_stop"}`,
		} {
			_, _ = fmt.Fprintf(w, "data: %s\n\n", payload)
			flusher.Flush()
		}
	}))
	defer server.Close()
	provider, err := NewAnthropicCompatibleProvider(AnthropicCompatibleConfig{
		Name: "test", BaseURL: server.URL, APIKey: "secret", DefaultModel: "fallback-model",
	})
	if err != nil {
		t.Fatal(err)
	}
	chunks, err := provider.StreamChat(context.Background(), ChatRequest{Messages: []Message{{Role: "user", Content: "hello"}}})
	if err != nil {
		t.Fatal(err)
	}
	var text strings.Builder
	var final ChatChunk
	for chunk := range chunks {
		if chunk.Err != nil {
			t.Fatal(chunk.Err)
		}
		text.WriteString(chunk.Text)
		if chunk.Done {
			final = chunk
		}
	}
	if !captured.Stream || text.String() != "hello world" || !final.Done || final.Usage == nil ||
		final.Usage.InputTokens != 7 || final.Usage.OutputTokens != 2 || final.Usage.TotalTokens != 9 ||
		final.Provider != "test" || final.Model != "stream-model" {
		t.Fatalf("unexpected stream captured=%#v text=%q final=%#v", captured, text.String(), final)
	}
}

func TestAnthropicCompatibleProviderDoesNotStreamThinkingDeltasAsText(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		flusher := w.(http.Flusher)
		for _, payload := range []string{
			`{"type":"message_start","message":{"model":"thinking-stream","usage":{"input_tokens":5,"output_tokens":0}}}`,
			`{"type":"content_block_start","index":0,"content_block":{"type":"thinking","thinking":"private"}}`,
			`{"type":"content_block_delta","index":0,"delta":{"type":"thinking_delta","thinking":"hidden trace"}}`,
			`{"type":"content_block_stop","index":0}`,
			`{"type":"content_block_start","index":1,"content_block":{"type":"text","text":""}}`,
			`{"type":"content_block_delta","index":1,"delta":{"type":"text_delta","text":"public update"}}`,
			`{"type":"content_block_stop","index":1}`,
			`{"type":"message_delta","usage":{"output_tokens":3}}`,
			`{"type":"message_stop"}`,
		} {
			_, _ = fmt.Fprintf(w, "data: %s\n\n", payload)
			flusher.Flush()
		}
	}))
	defer server.Close()
	provider, err := NewAnthropicCompatibleProvider(AnthropicCompatibleConfig{
		Name: "test", BaseURL: server.URL, APIKey: "secret", DefaultModel: "thinking-stream",
	})
	if err != nil {
		t.Fatal(err)
	}
	chunks, err := provider.StreamChat(t.Context(),
		ChatRequest{Messages: []Message{{Role: "user", Content: "inspect"}}})
	if err != nil {
		t.Fatal(err)
	}
	var text strings.Builder
	for chunk := range chunks {
		if chunk.Err != nil {
			t.Fatal(chunk.Err)
		}
		text.WriteString(chunk.Text)
	}
	if text.String() != "public update" || strings.Contains(text.String(), "hidden trace") {
		t.Fatalf("provider thinking delta entered public text: %q", text.String())
	}
}

func TestAnthropicCompatibleProviderMapsToolsAndToolResults(t *testing.T) {
	var captured map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&captured); err != nil {
			t.Fatal(err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"id":"msg_tool","type":"message","role":"assistant","model":"tool-model",
			"content":[{"type":"tool_use","id":"provider-tool-2","name":"note_create",
				"input":{"title":"Decision","content":"Use strict JSON"}}],
			"usage":{"input_tokens":9,"output_tokens":4}
		}`))
	}))
	defer server.Close()
	provider, err := NewAnthropicCompatibleProvider(AnthropicCompatibleConfig{
		Name: "test", BaseURL: server.URL, APIKey: "secret", DefaultModel: "tool-model",
	})
	if err != nil {
		t.Fatal(err)
	}
	response, err := provider.Chat(t.Context(), ChatRequest{
		Messages: []Message{
			{Role: "user", Content: "plan"},
			{Role: "assistant", ToolCalls: []ToolCall{{
				ID: "toolu_0123456789abcdef01234567", Name: "work_item_create",
				Arguments: json.RawMessage(`{"title":"Inspect parser"}`),
			}}},
			{Role: "user", Content: "Continue after the tool result.", ToolResults: []ToolResult{{
				ToolCallID: "toolu_0123456789abcdef01234567", Content: `{"status":"completed"}`,
			}}},
		},
		Tools: []ToolSpec{{
			Name: "note_create", Description: "Create a Note",
			Parameters: json.RawMessage(`{"type":"object","required":["title","content"]}`),
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	tools, ok := captured["tools"].([]any)
	messages, messagesOK := captured["messages"].([]any)
	if !ok || len(tools) != 1 || !messagesOK || len(messages) != 3 || !provider.SupportsTools("tool-model") {
		t.Fatalf("Anthropic tool request was not encoded: %#v", captured)
	}
	resultMessage, ok := messages[2].(map[string]any)
	if !ok {
		t.Fatalf("Anthropic tool result message was not an object: %#v", messages[2])
	}
	blocks, ok := resultMessage["content"].([]any)
	if !ok || len(blocks) != 2 {
		t.Fatalf("Anthropic tool result content was not encoded: %#v", resultMessage["content"])
	}
	firstBlock, firstOK := blocks[0].(map[string]any)
	secondBlock, secondOK := blocks[1].(map[string]any)
	if !firstOK || !secondOK || firstBlock["type"] != "tool_result" || secondBlock["type"] != "text" {
		t.Fatalf("tool_result blocks must precede text: %#v", blocks)
	}
	if len(response.ToolCalls) != 1 || response.ToolCalls[0].ID != "provider-tool-2" ||
		response.ToolCalls[0].Name != "note_create" || !json.Valid(response.ToolCalls[0].Arguments) ||
		response.Usage.TotalTokens != 13 || response.Text != "" {
		t.Fatalf("Anthropic tool response was not decoded: %#v", response)
	}
}

func TestAnthropicCompatibleProviderStreamsToolUseJSONDeltas(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		flusher := w.(http.Flusher)
		for _, payload := range []string{
			`{"type":"message_start","message":{"model":"tool-stream","usage":{"input_tokens":6,"output_tokens":0}}}`,
			`{"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"provider-stream-1","name":"work_item_create","input":{}}}`,
			`{"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"{\"title\":"}}`,
			`{"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"\"Stream plan\"}"}}`,
			`{"type":"content_block_stop","index":0}`,
			`{"type":"message_delta","usage":{"output_tokens":3}}`,
			`{"type":"message_stop"}`,
		} {
			_, _ = fmt.Fprintf(w, "data: %s\n\n", payload)
			flusher.Flush()
		}
	}))
	defer server.Close()
	provider, err := NewAnthropicCompatibleProvider(AnthropicCompatibleConfig{
		Name: "test", BaseURL: server.URL, APIKey: "secret", DefaultModel: "tool-stream",
	})
	if err != nil {
		t.Fatal(err)
	}
	chunks, err := provider.StreamChat(t.Context(), ChatRequest{Messages: []Message{{Role: "user", Content: "plan"}}})
	if err != nil {
		t.Fatal(err)
	}
	var final ChatChunk
	var eventTypes []StreamEventType
	for chunk := range chunks {
		if chunk.Err != nil {
			t.Fatal(chunk.Err)
		}
		if chunk.Done {
			final = chunk
		}
		for _, event := range chunk.Events {
			eventTypes = append(eventTypes, event.Type)
		}
	}
	if !final.Done || final.Usage == nil || final.Usage.TotalTokens != 9 || len(final.ToolCalls) != 1 ||
		final.ToolCalls[0].ID != "provider-stream-1" ||
		string(final.ToolCalls[0].Arguments) != `{"title":"Stream plan"}` {
		t.Fatalf("unexpected streamed tool call: %#v", final)
	}
	if want := providerToolDeltaEventVector(2); !reflect.DeepEqual(eventTypes, want) {
		t.Fatalf("Anthropic tool event vector = %v, want %v", eventTypes, want)
	}
}

func TestAnthropicCompatibleProviderReportsMalformedStreamEvent(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: not-json\n\n"))
	}))
	defer server.Close()
	provider, err := NewAnthropicCompatibleProvider(AnthropicCompatibleConfig{
		Name: "test", BaseURL: server.URL, APIKey: "secret", DefaultModel: "model",
	})
	if err != nil {
		t.Fatal(err)
	}
	chunks, err := provider.StreamChat(context.Background(), ChatRequest{Messages: []Message{{Role: "user", Content: "hello"}}})
	if err != nil {
		t.Fatal(err)
	}
	chunk, ok := <-chunks
	if !ok || ProviderErrorKind(chunk.Err) != OutcomeInvalidResponse {
		t.Fatalf("malformed SSE was not typed: ok=%t chunk=%#v", ok, chunk)
	}
}

func TestAnthropicStreamRejectsTextBlockLifecycleGaps(t *testing.T) {
	newState := func(t *testing.T) *anthropicStreamState {
		t.Helper()
		state := &anthropicStreamState{model: "requested",
			events: newProviderStreamEvents("test", "requested", "wire",
				StreamGranularityDelta)}
		if _, _, err := state.consume([]byte(
			`{"type":"message_start","message":{"model":"actual","usage":{"input_tokens":1,"output_tokens":0}}}`),
			"test"); err != nil {
			t.Fatal(err)
		}
		return state
	}

	t.Run("delta without start", func(t *testing.T) {
		state := newState(t)
		if _, _, err := state.consume([]byte(
			`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"unsafe"}}`),
			"test"); ProviderErrorKind(err) != OutcomeInvalidResponse {
			t.Fatalf("unknown text block error = %v", err)
		}
	})

	t.Run("stop with open block", func(t *testing.T) {
		state := newState(t)
		if _, _, err := state.consume([]byte(
			`{"type":"content_block_start","index":0,"content_block":{"type":"text","text":"partial"}}`),
			"test"); err != nil {
			t.Fatal(err)
		}
		if _, _, err := state.consume([]byte(`{"type":"message_stop"}`), "test"); ProviderErrorKind(err) != OutcomeInvalidResponse {
			t.Fatalf("unfinished text block error = %v", err)
		}
	})
}

func TestAnthropicCompatibleProviderClassifiesHTTPFailures(t *testing.T) {
	tests := []struct {
		name       string
		statusCode int
		retryAfter string
		want       Outcome
		wantReason ProviderFailureReason
	}{
		{name: "rate limit", statusCode: http.StatusTooManyRequests, retryAfter: "7",
			want: OutcomeRateLimited, wantReason: ProviderFailureRateLimit},
		{name: "unavailable", statusCode: http.StatusServiceUnavailable,
			want: OutcomeRetryable, wantReason: ProviderFailureCapacity},
		{name: "overloaded", statusCode: 529,
			want: OutcomeRetryable, wantReason: ProviderFailureCapacity},
		{name: "auth", statusCode: http.StatusUnauthorized,
			want: OutcomePermanent, wantReason: ProviderFailureAuthentication},
		{name: "ambiguous not found", statusCode: http.StatusNotFound,
			want: OutcomePermanent, wantReason: ProviderFailureProtocolIncompatible},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				if test.retryAfter != "" {
					w.Header().Set("Retry-After", test.retryAfter)
				}
				w.WriteHeader(test.statusCode)
				_, _ = w.Write([]byte(`{"error":"temporary"}`))
			}))
			defer server.Close()
			provider, err := NewAnthropicCompatibleProvider(AnthropicCompatibleConfig{
				Name: "test", BaseURL: server.URL, APIKey: "secret", DefaultModel: "model",
			})
			if err != nil {
				t.Fatal(err)
			}
			_, err = provider.Chat(context.Background(), ChatRequest{Messages: []Message{{Role: "user", Content: "hello"}}})
			var providerErr *ProviderError
			if !errors.As(err, &providerErr) || providerErr.Kind != test.want ||
				providerErr.Reason != test.wantReason || providerErr.StatusCode != test.statusCode {
				t.Fatalf("unexpected provider error: %#v err=%v", providerErr, err)
			}
			if test.retryAfter != "" && providerErr.RetryAfter != 7*time.Second {
				t.Fatalf("retry after = %s, want 7s", providerErr.RetryAfter)
			}
		})
	}
}

func TestProviderHTTPClientBoundsTimeoutAndDisablesRedirects(t *testing.T) {
	source := &http.Client{Timeout: 2 * time.Minute}
	client, err := providerHTTPClient(source)
	if err != nil {
		t.Fatal(err)
	}
	if client == source || client.Timeout != 2*time.Minute || source.Timeout != 2*time.Minute {
		t.Fatalf("HTTP client timeout was not safely copied and bounded: source=%s client=%s",
			source.Timeout, client.Timeout)
	}
	request := httptest.NewRequest(http.MethodGet, "https://provider.invalid/next", nil)
	if err := client.CheckRedirect(request, nil); !errors.Is(err, http.ErrUseLastResponse) {
		t.Fatalf("redirect policy error = %v", err)
	}
	short, err := providerHTTPClient(&http.Client{Timeout: 5 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	if short.Timeout != 5*time.Second {
		t.Fatalf("short caller timeout was widened: %s", short.Timeout)
	}
}

func TestAnthropicCompatibleProviderRejectsMalformedAndEmptyResponses(t *testing.T) {
	for _, body := range []string{`not-json`, `{"model":"model","content":[]}`} {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write([]byte(body))
		}))
		provider, err := NewAnthropicCompatibleProvider(AnthropicCompatibleConfig{
			Name: "test", BaseURL: server.URL, APIKey: "secret", DefaultModel: "model",
		})
		if err != nil {
			server.Close()
			t.Fatal(err)
		}
		_, err = provider.Chat(context.Background(), ChatRequest{Messages: []Message{{Role: "user", Content: "hello"}}})
		server.Close()
		if ProviderErrorKind(err) != OutcomeInvalidResponse {
			t.Fatalf("body %q outcome=%s err=%v", body, ProviderErrorKind(err), err)
		}
	}
}

func TestAnthropicHTTPErrorDoesNotProjectResponseBody(t *testing.T) {
	token := "t" + "p-" + strings.Repeat("b", 40)
	marker := "remote-marker-that-is-not-secret-shaped"
	err := anthropicHTTPError("mimo", http.StatusTooManyRequests, "",
		[]byte("MIMO_API_KEY="+token+" prompt="+marker))
	if strings.Contains(err.Error(), token[:12]) || strings.Contains(err.Error(), marker) ||
		err.Reason != ProviderFailureRateLimit {
		t.Fatalf("HTTP error leaked response secret: %q", err.Error())
	}
}

func TestParseRetryAfterSupportsSecondsAndHTTPDate(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	if got := parseRetryAfter("4", now); got != 4*time.Second {
		t.Fatalf("seconds retry-after = %s", got)
	}
	when := now.Add(5 * time.Second).Format(http.TimeFormat)
	if got := parseRetryAfter(when, now); got != 5*time.Second {
		t.Fatalf("date retry-after = %s", got)
	}
	if got := parseRetryAfter("9223372036854775807", now); got <= 0 {
		t.Fatalf("overflow retry-after = %s", got)
	}
}

func TestAnthropicCompatibleProviderListModelsEndpoint(t *testing.T) {
	for _, test := range []struct {
		name, basePath, modelsPath string
	}{
		{"base URL", "", "/v1/models"},
		{"version base URL", "/v1", "/v1/models"},
		{"messages endpoint", "/v1/messages", "/v1/models"},
		{"prefixed messages endpoint", "/gateway/v1/messages", "/gateway/v1/models"},
	} {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodGet || r.URL.Path != test.modelsPath {
					t.Errorf("model-list request = %s %s, want GET %s", r.Method, r.URL.Path, test.modelsPath)
					http.NotFound(w, r)
					return
				}
				if got := r.Header.Get("x-api-key"); got != "test-secret" {
					t.Errorf("API key = %q", got)
				}
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`{"data":[{"id":"listed-model","display_name":"Listed Model"}]}`))
			}))
			defer server.Close()

			provider, err := NewAnthropicCompatibleProvider(AnthropicCompatibleConfig{
				Name: "test", BaseURL: server.URL + test.basePath, APIKey: "test-secret",
				DefaultModel: "fallback-model",
			})
			if err != nil {
				t.Fatal(err)
			}
			models, err := provider.ListModels(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			if len(models) != 1 || models[0].ID != "listed-model" || models[0].DisplayName != "Listed Model" {
				t.Fatalf("unexpected model list: %#v", models)
			}
		})
	}
}

func TestAnthropicCompatibleProviderListModelsReturnsHTTPFailure(t *testing.T) {
	server := httptest.NewServer(http.NotFoundHandler())
	defer server.Close()

	provider, err := NewAnthropicCompatibleProvider(AnthropicCompatibleConfig{
		Name:         "test",
		BaseURL:      server.URL,
		APIKey:       "secret",
		DefaultModel: "fallback-model",
	})
	if err != nil {
		t.Fatal(err)
	}
	models, err := provider.ListModels(context.Background())
	var providerErr *ProviderError
	if !errors.As(err, &providerErr) || providerErr.StatusCode != http.StatusNotFound || len(models) != 0 {
		t.Fatalf("model-list failure became fallback data: %#v %v", models, err)
	}
}

func TestAnthropicCompatibleProviderRejectsUnsafeConfiguration(t *testing.T) {
	for _, baseURL := range []string{
		"ftp://example.com/anthropic",
		"http://example.com/anthropic",
		"https://user:password@example.com/anthropic",
		"https://example.com/anthropic?route=unsafe",
		"https://example.com/anthropic#fragment",
	} {
		if _, err := NewAnthropicCompatibleProvider(AnthropicCompatibleConfig{
			Name: "unsafe", BaseURL: baseURL, APIKey: "secret",
		}); err == nil {
			t.Fatalf("unsafe Provider base URL was accepted: %s", baseURL)
		}
	}
	for _, apiKey := range []string{"", " secret", "secret\nvalue", strings.Repeat("x", maxProviderAPIKeyBytes+1)} {
		if _, err := NewAnthropicCompatibleProvider(AnthropicCompatibleConfig{
			Name: "unsafe", BaseURL: "https://example.com/anthropic", APIKey: apiKey,
		}); err == nil {
			t.Fatal("unsafe Provider API key was accepted")
		}
	}
}

func TestAnthropicCompatibleProviderAllowsLoopbackHTTP(t *testing.T) {
	for _, baseURL := range []string{
		"http://localhost:8080/anthropic",
		"http://127.0.0.1:8080/anthropic",
		"http://[::1]:8080/anthropic",
	} {
		if _, err := NewAnthropicCompatibleProvider(AnthropicCompatibleConfig{
			Name: "local", BaseURL: baseURL, APIKey: "secret",
		}); err != nil {
			t.Fatalf("loopback Provider URL %s was rejected: %v", baseURL, err)
		}
	}
}

func TestAnthropicCompatibleProviderDoesNotForwardCredentialsAcrossRedirects(t *testing.T) {
	received := false
	target := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		received = true
	}))
	defer target.Close()
	redirect := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		http.Redirect(writer, request, target.URL+"/v1/messages", http.StatusTemporaryRedirect)
	}))
	defer redirect.Close()
	provider, err := NewAnthropicCompatibleProvider(AnthropicCompatibleConfig{
		Name: "redirect-test", BaseURL: redirect.URL, APIKey: "secret", DefaultModel: "model",
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = provider.Chat(t.Context(), ChatRequest{
		Messages: []Message{{Role: "user", Content: "do not redirect credentials"}},
	})
	var providerErr *ProviderError
	if !errors.As(err, &providerErr) || providerErr.StatusCode != http.StatusTemporaryRedirect {
		t.Fatalf("Provider redirect did not fail as HTTP 307: %v", err)
	}
	if received {
		t.Fatal("Provider followed a redirect and exposed credential headers")
	}
}
