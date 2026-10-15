package llm

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
)

type chatGPTTestRuntime struct {
	token        string
	unsafeHeader string
}

func (r *chatGPTTestRuntime) ResolveCredential(context.Context) (string, error) { return r.token, nil }
func (r *chatGPTTestRuntime) MapModel(model string) (string, error)             { return model, nil }
func (r *chatGPTTestRuntime) Apply(secret string, headers http.Header, _ map[string]any) error {
	if headers != nil && r.unsafeHeader != "" {
		headers.Set(r.unsafeHeader, secret)
	}
	return nil
}
func (*chatGPTTestRuntime) BindingDigest() string { return strings.Repeat("1", 64) }
func (*chatGPTTestRuntime) AllowsCredentialEndpoint(endpoint string) bool {
	return endpoint == ChatGPTResponsesEndpoint
}

type chatGPTTestRoundTrip func(*http.Request) (*http.Response, error)

func (f chatGPTTestRoundTrip) RoundTrip(request *http.Request) (*http.Response, error) {
	return f(request)
}

func newChatGPTTestProvider(t *testing.T, runtime *chatGPTTestRuntime,
	transport func(*http.Request) (*http.Response, error),
) *ChatGPTProvider {
	t.Helper()
	provider, err := NewChatGPTProvider(ChatGPTProviderConfig{Name: "chatgpt-fixture",
		DefaultModel: "gpt-fixture", Runtime: runtime,
		Models:     []ModelInfo{{ID: "gpt-fixture", DisplayName: "Fixture", Provider: "chatgpt-fixture"}},
		HTTPClient: &http.Client{Transport: chatGPTTestRoundTrip(transport)}})
	if err != nil {
		t.Fatal(err)
	}
	return provider
}

func chatGPTTestSSE(items ...map[string]any) string {
	var stream strings.Builder
	write := func(event map[string]any) {
		raw, err := json.Marshal(event)
		if err != nil {
			panic(err)
		}
		fmt.Fprintf(&stream, "data: %s\n\n", raw)
	}
	write(map[string]any{"type": "response.created", "response": map[string]any{
		"id": "response-fixture", "object": "response", "status": "in_progress", "model": "gpt-fixture"}})
	for _, item := range items {
		started := make(map[string]any)
		for _, key := range []string{"id", "type", "role", "call_id", "name", "namespace"} {
			if value, found := item[key]; found {
				started[key] = value
			}
		}
		started["status"] = "in_progress"
		write(map[string]any{"type": "response.output_item.added", "item": started})
		if item["type"] == "message" {
			content := item["content"].([]map[string]any)
			for _, part := range content {
				write(map[string]any{"type": "response.output_text.delta", "item_id": item["id"], "delta": part["text"]})
			}
		}
		write(map[string]any{"type": "response.output_item.done", "item": item})
	}
	write(map[string]any{"type": "response.completed", "response": map[string]any{
		"id": "response-fixture", "object": "response", "status": "completed", "model": "gpt-fixture",
		"output": items, "usage": map[string]any{"input_tokens": 4, "output_tokens": 2, "total_tokens": 6}}})
	return stream.String()
}

func chatGPTTestText(text string) map[string]any {
	return map[string]any{"id": "message-fixture", "type": "message", "status": "completed", "role": "assistant",
		"content": []map[string]any{{"type": "output_text", "text": text}}}
}

func chatGPTTestHTTPResponse(request *http.Request, body string) *http.Response {
	return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": {"text/event-stream"}},
		Body: io.NopCloser(strings.NewReader(body)), Request: request}
}

func TestChatGPTProviderStreamsBufferedCallsAndReplaysNamespacedTools(t *testing.T) {
	runtime := &chatGPTTestRuntime{token: "private-oauth-fixture"}
	var bodies []map[string]any
	provider := newChatGPTTestProvider(t, runtime, func(request *http.Request) (*http.Response, error) {
		var body map[string]any
		if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		bodies = append(bodies, body)
		if request.URL.String() != ChatGPTResponsesEndpoint || request.Method != http.MethodPost ||
			request.Header.Get("Authorization") != "Bearer private-oauth-fixture" ||
			request.Header.Get("Accept") != "text/event-stream" || body["stream"] != true || body["store"] != false ||
			body["max_output_tokens"] != nil || body["temperature"] != nil ||
			body["instructions"] != "Local policy" {
			t.Fatalf("ChatGPT HTTP policy = %s %#v", request.URL, body)
		}
		if len(bodies) == 1 {
			return chatGPTTestHTTPResponse(request, chatGPTTestSSE(
				map[string]any{"id": "reasoning-fixture", "type": "reasoning", "summary": []any{}, "encrypted_content": "opaque-private-state"},
				map[string]any{"id": "function-fixture", "type": "function_call", "status": "completed",
					"namespace": ChatGPTToolNamespace, "call_id": "call-fixture", "name": "read_file", "arguments": `{"path":"main.go"}`})), nil
		}
		return chatGPTTestHTTPResponse(request, chatGPTTestSSE(chatGPTTestText("Reviewed"))), nil
	})
	request := ChatRequest{Messages: []Message{{Role: "system", Content: "Local policy"}, {Role: "user", Content: "Review"}},
		MaxTokens: 128, Temperature: 0.8, Tools: []ToolSpec{{Name: "read_file", Parameters: json.RawMessage(`{"type":"object"}`)}}}
	first, err := provider.Chat(t.Context(), request)
	if err != nil || first == nil || len(first.ToolCalls) != 1 || first.Replay == nil || first.Usage.TotalTokens != 6 {
		t.Fatalf("native tool response = %#v, %v", first, err)
	}
	request.Messages = append(request.Messages, Message{Role: "assistant", ToolCalls: first.ToolCalls, Replay: first.Replay},
		Message{Role: "user", ToolResults: []ToolResult{{ToolCallID: first.ToolCalls[0].ID, Content: "package main"}}})
	final, err := provider.Chat(t.Context(), request)
	if err != nil || final == nil || final.Text != "Reviewed" || len(final.Items) != 1 || len(bodies) != 2 {
		t.Fatalf("completed tool loop = %#v, %v", final, err)
	}
	for _, body := range bodies {
		namespace := body["tools"].([]any)[0].(map[string]any)
		if namespace["type"] != "namespace" || namespace["name"] != ChatGPTToolNamespace ||
			namespace["tools"].([]any)[0].(map[string]any)["name"] != "read_file" {
			t.Fatalf("tool namespace missing: %#v", namespace)
		}
	}
	replayed, _ := json.Marshal(bodies[1]["input"])
	for _, required := range []string{`"namespace":"universal_code"`, `"encrypted_content":"opaque-private-state"`, `"type":"function_call_output"`, "package main"} {
		if !strings.Contains(string(replayed), required) {
			t.Fatalf("native tool replay omitted %s: %s", required, replayed)
		}
	}
}

func TestChatGPTProviderRejectsForeignNamespaceAndIncompleteStream(t *testing.T) {
	for _, scenario := range []string{"foreign-namespace", "interrupted", "late-failure"} {
		t.Run(scenario, func(t *testing.T) {
			provider := newChatGPTTestProvider(t, &chatGPTTestRuntime{token: "private-access-fixture"}, func(request *http.Request) (*http.Response, error) {
				var body string
				switch scenario {
				case "foreign-namespace":
					body = chatGPTTestSSE(map[string]any{"id": "function", "type": "function_call", "status": "completed",
						"namespace": "another_app", "call_id": "call", "name": "read_file", "arguments": `{}`})
				default:
					body = chatGPTTestSSE(chatGPTTestText("partial"))
					terminal := strings.LastIndex(body, "data: ")
					body = body[:terminal]
					if scenario == "late-failure" {
						body += "data: {\"type\":\"response.failed\",\"response\":{\"id\":\"response-fixture\",\"object\":\"response\",\"status\":\"failed\",\"error\":{\"code\":\"subscription_sharing_usage_limit_exceeded\",\"message\":\"private-access-fixture\"}}}\n\n"
					}
				}
				return chatGPTTestHTTPResponse(request, body), nil
			})
			result, err := provider.Chat(t.Context(), ChatRequest{Messages: []Message{{Role: "user", Content: "Hello"}}})
			if err == nil || result != nil || strings.Contains(err.Error(), "private-access-fixture") {
				t.Fatalf("failed stream became completed output: %#v, %v", result, err)
			}
			if scenario == "late-failure" {
				failure := NormalizeProviderError(provider.Name(), err)
				if failure.Reason != ProviderFailureRateLimit || failure.Kind != OutcomeRateLimited {
					t.Fatalf("subscription usage limit lost its recovery classification: %#v", failure)
				}
			}
		})
	}
}

func TestChatGPTTransportContainsEndpointHeadersAndRedirects(t *testing.T) {
	for _, endpoint := range []string{"https://proxy.example.invalid/v1/responses", "https://api.openai.com/v1/responses?key=hidden",
		"https://api.openai.com/v1/chat/completions", "https://chatgpt.com/backend-api/codex/responses", "http://api.openai.com/v1/responses"} {
		request, err := http.NewRequestWithContext(t.Context(), http.MethodPost, endpoint, nil)
		if err != nil {
			t.Fatal(err)
		}
		var sent bool
		transport := chatGPTResponsesTransport{next: chatGPTTestRoundTrip(func(*http.Request) (*http.Response, error) {
			sent = true
			return nil, nil
		})}
		if _, err := transport.RoundTrip(request); err == nil || sent {
			t.Fatalf("credential escaped to %s: %v", endpoint, err)
		}
	}
	for _, header := range []string{"OpenAI-Organization", "OpenAI-Project", "ChatGPT-Account-ID", "Cookie", "Proxy-Authorization", "X-API-Key"} {
		t.Run(header, func(t *testing.T) {
			var sent bool
			provider := newChatGPTTestProvider(t, &chatGPTTestRuntime{token: "private-access-fixture", unsafeHeader: header}, func(*http.Request) (*http.Response, error) {
				sent = true
				return nil, nil
			})
			if _, err := provider.Chat(t.Context(), ChatRequest{}); err == nil || sent || strings.Contains(err.Error(), "private-access-fixture") {
				t.Fatalf("sensitive header was dispatched: %v", err)
			}
		})
	}
	var requests int
	provider := newChatGPTTestProvider(t, &chatGPTTestRuntime{token: "private-access-fixture"}, func(request *http.Request) (*http.Response, error) {
		requests++
		return &http.Response{StatusCode: http.StatusFound, Header: http.Header{"Location": {"https://proxy.example.invalid/collect"}},
			Body: io.NopCloser(strings.NewReader("")), Request: request}, nil
	})
	if _, err := provider.Chat(t.Context(), ChatRequest{}); err == nil || requests != 1 {
		t.Fatalf("OAuth inference followed a redirect: requests=%d, err=%v", requests, err)
	}
}
