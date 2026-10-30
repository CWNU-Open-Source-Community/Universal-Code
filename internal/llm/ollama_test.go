package llm

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func newTestOllamaProvider(t *testing.T, server *httptest.Server) *OllamaProvider {
	t.Helper()
	provider, err := NewOllamaProvider(OllamaConfig{
		Name: OllamaDefaultName, BaseURL: server.URL,
	})
	if err != nil {
		t.Fatal(err)
	}
	return provider
}

func ollamaJSON(value any) string {
	raw, err := json.Marshal(value)
	if err != nil {
		panic(err)
	}
	return string(raw)
}

func TestOllamaProviderConstructorRejectsUnsafeConfiguration(t *testing.T) {
	tests := []struct {
		name    string
		baseURL string
	}{
		{name: "https scheme", baseURL: "https://127.0.0.1:11434"},
		{name: "non-loopback host", baseURL: "http://192.168.1.10:11434"},
		{name: "hostname alias", baseURL: "http://ollama.internal:11434"},
		{name: "path prefix", baseURL: "http://127.0.0.1:11434/v1"},
		{name: "userinfo", baseURL: "http://user:pass@127.0.0.1:11434"},
		{name: "query", baseURL: "http://127.0.0.1:11434?token=secret"},
		{name: "fragment", baseURL: "http://127.0.0.1:11434#frag"},
		{name: "empty", baseURL: ""},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := NewOllamaProvider(OllamaConfig{BaseURL: test.baseURL}); err == nil {
				t.Fatalf("unsafe base url was accepted: %q", test.baseURL)
			}
		})
	}
	t.Run("loopback variants accepted", func(t *testing.T) {
		for _, baseURL := range []string{
			"http://127.0.0.1:11434", "http://127.0.0.1:11434/",
			"http://localhost:11434", "http://[::1]:11434",
		} {
			provider, err := NewOllamaProvider(OllamaConfig{BaseURL: baseURL})
			if err != nil {
				t.Fatalf("loopback base url %q rejected: %v", baseURL, err)
			}
			if provider.baseURL != strings.TrimRight(baseURL, "/") {
				t.Fatalf("base url was not normalized: %q", provider.baseURL)
			}
		}
	})
	t.Run("proxy-bearing transport rejected", func(t *testing.T) {
		proxyClient := &http.Client{Transport: &customRoundTripper{}}
		if _, err := NewOllamaProvider(OllamaConfig{
			BaseURL: "http://127.0.0.1:11434", HTTPClient: proxyClient}); err == nil {
			t.Fatal("custom proxy-capable transport was accepted")
		}
	})
}

type customRoundTripper struct{}

func (customRoundTripper) RoundTrip(*http.Request) (*http.Response, error) {
	return nil, errors.New("unexpected request")
}

func TestOllamaProviderNeverUsesProxyAndRejectsRedirects(t *testing.T) {
	provider, err := NewOllamaProvider(OllamaConfig{BaseURL: "http://127.0.0.1:11434"})
	if err != nil {
		t.Fatal(err)
	}
	transport, ok := provider.client.Transport.(*http.Transport)
	if !ok {
		t.Fatalf("unexpected transport type %T", provider.client.Transport)
	}
	if transport.Proxy != nil {
		t.Fatal("Ollama transport must never consult an environment proxy")
	}
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		http.Redirect(writer, request, "http://127.0.0.1:9/elsewhere", http.StatusFound)
	}))
	defer server.Close()
	redirected := newTestOllamaProvider(t, server)
	_, err = redirected.ListModels(context.Background())
	if err == nil || ProviderErrorReason(err) != ProviderFailureProtocolIncompatible {
		t.Fatalf("redirect was followed or misclassified: %v", err)
	}
}

func TestOllamaListModelsCapabilityAware(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/api/tags" || request.Method != http.MethodGet {
			t.Errorf("unexpected request %s %s", request.Method, request.URL.Path)
		}
		_ = json.NewEncoder(writer).Encode(map[string]any{
			"models": []map[string]any{
				{"name": "tooler:latest", "model": "tooler:latest",
					"capabilities": []string{"completion", "tools"}},
				{"name": "visioner:latest", "model": "visioner:latest",
					"capabilities": []string{"completion", "vision"}},
				{"name": "legacy:7b", "model": "legacy:7b"},
			},
		})
	}))
	defer server.Close()
	provider := newTestOllamaProvider(t, server)
	models, err := provider.ListModels(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(models) != 3 {
		t.Fatalf("unexpected model count %d", len(models))
	}
	if !provider.SupportsTools("tooler:latest") || provider.SupportsVision("tooler:latest") {
		t.Fatal("tooler capabilities were misreported")
	}
	if provider.SupportsTools("visioner:latest") || !provider.SupportsVision("visioner:latest") {
		t.Fatal("visioner capabilities were misreported")
	}
	// The legacy model's capabilities field is absent: everything stays
	// unknown and therefore unsupported.
	if provider.SupportsTools("legacy:7b") || provider.SupportsVision("legacy:7b") ||
		provider.SupportsJSONMode("legacy:7b") {
		t.Fatal("unprobed model reported capabilities")
	}
	if !provider.SupportsJSONMode("tooler:latest") {
		t.Fatal("probed model must support the wire-level JSON format")
	}
	harness := provider.DescribeModelHarness("legacy:7b")
	if harness.ToolStrategy != HarnessToolStrategyNone ||
		harness.JSONStrategy != HarnessJSONStrategyNone ||
		harness.TransportProtocol != HarnessTransportOllamaChat {
		t.Fatalf("no-tool harness is wrong: %#v", harness)
	}
}

func TestOllamaProbeModelRecordsCapabilitiesAndContext(t *testing.T) {
	var showCalls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path == "/api/tags" {
			_ = json.NewEncoder(writer).Encode(map[string]any{
				"models": []map[string]any{{
					"name": "probe-me:7b", "model": "probe-me:7b",
					"capabilities": []string{"completion"},
				}},
			})
			return
		}
		if request.URL.Path == "/api/show" {
			showCalls.Add(1)
			var body map[string]any
			_ = json.NewDecoder(request.Body).Decode(&body)
			if body["model"] != "probe-me:7b" || body["verbose"] != false {
				t.Errorf("unexpected probe body: %v", body)
			}
			_ = json.NewEncoder(writer).Encode(map[string]any{
				"capabilities": []string{"completion", "tools"},
				"model_info": map[string]any{
					"general.architecture": "llama",
					"llama.context_length":  8192.0,
					"llama.block_count":     32.0,
				},
			})
			return
		}
		t.Errorf("unexpected path %s", request.URL.Path)
	}))
	defer server.Close()
	provider := newTestOllamaProvider(t, server)
	probe, err := provider.ProbeModel(context.Background(), "probe-me:7b")
	if err != nil {
		t.Fatal(err)
	}
	if !probe.Known || !probe.Tools || probe.Vision || probe.ContextLength != 8192 {
		t.Fatalf("probe facts are wrong: %#v", probe)
	}
	if !provider.SupportsTools("probe-me:7b") ||
		provider.ContextLength("probe-me:7b") != 8192 {
		t.Fatal("probe did not update the capability cache")
	}
	if showCalls.Load() != 1 {
		t.Fatalf("unexpected show call count %d", showCalls.Load())
	}
}

func TestOllamaChatResponseUsageAndToolCalls(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path == "/api/tags" {
			_ = json.NewEncoder(writer).Encode(map[string]any{
				"models": []map[string]any{{"name": "tooler:latest", "model": "tooler:latest",
					"capabilities": []string{"completion", "tools"}}},
			})
			return
		}
		if request.URL.Path != "/api/chat" {
			t.Errorf("unexpected path %s", request.URL.Path)
		}
		_ = json.NewEncoder(writer).Encode(map[string]any{
			"model": "tooler:latest", "done": true, "done_reason": "stop",
			"prompt_eval_count": 12, "eval_count": 7,
			"message": map[string]any{
				"role":    "assistant",
				"content": "calling",
				"tool_calls": []map[string]any{{
					"function": map[string]any{
						"name":      "workspace_list",
						"arguments": map[string]any{"path": "."},
					},
				}},
			},
		})
	}))
	defer server.Close()
	provider := newTestOllamaProvider(t, server)
	if _, err := provider.ListModels(context.Background()); err != nil {
		t.Fatal(err)
	}
	response, err := provider.Chat(context.Background(), ChatRequest{
		Model: "tooler:latest",
		Messages: []Message{{Role: "user", Content: "list the workspace"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if response.Text != "calling" || len(response.ToolCalls) != 1 ||
		response.ToolCalls[0].Name != "workspace_list" ||
		response.ToolCalls[0].ID == "" ||
		string(response.ToolCalls[0].Arguments) != `{"path":"."}` {
		t.Fatalf("chat response is wrong: %#v", response)
	}
	if response.Usage.InputTokens != 12 || response.Usage.OutputTokens != 7 ||
		response.Usage.TotalTokens != 19 {
		t.Fatalf("usage counts were not adopted: %#v", response.Usage)
	}
}

func TestOllamaChatEstimatesUsageWhenCountsAreAbsent(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path == "/api/tags" {
			_ = json.NewEncoder(writer).Encode(map[string]any{
				"models": []map[string]any{{"name": "tooler:latest", "model": "tooler:latest",
					"capabilities": []string{"completion", "tools"}}},
			})
			return
		}
		_ = json.NewEncoder(writer).Encode(map[string]any{
			"model": "tooler:latest", "done": true,
			"message": map[string]any{"role": "assistant",
				"content": strings.Repeat("x", 400)},
		})
	}))
	defer server.Close()
	provider := newTestOllamaProvider(t, server)
	if _, err := provider.ListModels(context.Background()); err != nil {
		t.Fatal(err)
	}
	response, err := provider.Chat(context.Background(), ChatRequest{
		Model: "tooler:latest",
		Messages: []Message{{Role: "user", Content: "hello"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if response.Usage.InputTokens < 1 || response.Usage.OutputTokens < 100 ||
		response.Usage.TotalTokens != response.Usage.InputTokens+response.Usage.OutputTokens {
		t.Fatalf("usage estimation is wrong: %#v", response.Usage)
	}
}

func TestOllamaChatErrorsClassifyStably(t *testing.T) {
	tests := []struct {
		name       string
		statusCode int
		body       string
		wantReason ProviderFailureReason
		wantKind   Outcome
	}{
		{name: "model not found", statusCode: http.StatusNotFound,
			body: ollamaJSON(map[string]any{"error": "model 'ghost:7b' not found, try pulling it first"}),
			wantReason: ProviderFailureModelNotFound, wantKind: OutcomePermanent},
		{name: "plain 404 stays protocol", statusCode: http.StatusNotFound,
			body: `{}`, wantReason: ProviderFailureProtocolIncompatible, wantKind: OutcomePermanent},
		{name: "out of memory", statusCode: http.StatusInternalServerError,
			body:       ollamaJSON(map[string]any{"error": "out of memory"}),
			wantReason: ProviderFailureCapacity, wantKind: OutcomeRetryable},
		{name: "service unavailable", statusCode: http.StatusServiceUnavailable,
			body: `{}`, wantReason: ProviderFailureCapacity, wantKind: OutcomeRetryable},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
				writer.WriteHeader(test.statusCode)
				_, _ = io.WriteString(writer, test.body)
			}))
			defer server.Close()
			provider := newTestOllamaProvider(t, server)
			_, err := provider.Chat(context.Background(), ChatRequest{
				Model: "ghost:7b",
				Messages: []Message{{Role: "user", Content: "hello"}},
			})
			if err == nil {
				t.Fatal("expected an error")
			}
			if ProviderErrorReason(err) != test.wantReason || ProviderErrorKind(err) != test.wantKind {
				t.Fatalf("error classification: reason=%s kind=%s err=%v",
					ProviderErrorReason(err), ProviderErrorKind(err), err)
			}
		})
	}
}

func TestOllamaChatNoToolSafePath(t *testing.T) {
	var receivedTools atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path == "/api/tags" {
			_ = json.NewEncoder(writer).Encode(map[string]any{
				"models": []map[string]any{{
					"name": "plain:7b", "model": "plain:7b",
					"capabilities": []string{"completion"},
				}},
			})
			return
		}
		if request.URL.Path == "/api/chat" {
			var body struct {
				Tools []json.RawMessage `json:"tools"`
			}
			_ = json.NewDecoder(request.Body).Decode(&body)
			receivedTools.Store(int32(len(body.Tools)))
			_ = json.NewEncoder(writer).Encode(map[string]any{
				"model": "plain:7b", "done": true,
				"message": map[string]any{"role": "assistant", "content": "no tools here"},
			})
			return
		}
		t.Errorf("unexpected path %s", request.URL.Path)
	}))
	defer server.Close()
	provider := newTestOllamaProvider(t, server)
	if _, err := provider.ListModels(context.Background()); err != nil {
		t.Fatal(err)
	}
	tools := []ToolSpec{{Name: "workspace_list", Description: "list",
		Parameters: json.RawMessage(`{"type":"object"}`)}}
	// The no-tool safe path: a completion-only model must never receive tool
	// schemas, and the provider refuses rather than faking a tool call.
	_, err := provider.Chat(context.Background(), ChatRequest{
		Model: "plain:7b", Tools: tools,
		Messages: []Message{{Role: "user", Content: "hello"}},
	})
	if err == nil || !strings.Contains(err.Error(), "does not support tool calling") {
		t.Fatalf("no-tool request was not refused: %v", err)
	}
	if receivedTools.Load() != 0 {
		t.Fatal("tool schemas reached a no-tool model")
	}
	response, err := provider.Chat(context.Background(), ChatRequest{
		Model: "plain:7b",
		Messages: []Message{{Role: "user", Content: "hello"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if response.Text != "no tools here" {
		t.Fatalf("plain chat failed: %#v", response)
	}
}

func TestOllamaStreamChatTextUsageAndToolCalls(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path == "/api/tags" {
			_ = json.NewEncoder(writer).Encode(map[string]any{
				"models": []map[string]any{{"name": "tooler:latest", "model": "tooler:latest",
					"capabilities": []string{"completion", "tools"}}},
			})
			return
		}
		if request.URL.Path != "/api/chat" {
			t.Errorf("unexpected path %s", request.URL.Path)
		}
		writer.Header().Set("Content-Type", "application/x-ndjson")
		for _, event := range []map[string]any{
			{"model": "tooler:latest", "done": false,
				"message": map[string]any{"role": "assistant", "content": "Hello "}},
			{"model": "tooler:latest", "done": false,
				"message": map[string]any{"role": "assistant", "content": "world"}},
			{"model": "tooler:latest", "done": false,
				"message": map[string]any{"role": "assistant", "tool_calls": []map[string]any{{
					"function": map[string]any{"name": "workspace_list",
						"arguments": map[string]any{"path": "."}},
				}}}},
			{"model": "tooler:latest", "done": true, "done_reason": "stop",
				"prompt_eval_count": 9, "eval_count": 5},
		} {
			_ = json.NewEncoder(writer).Encode(event)
		}
	}))
	defer server.Close()
	provider := newTestOllamaProvider(t, server)
	if _, err := provider.ListModels(context.Background()); err != nil {
		t.Fatal(err)
	}
	chunks, err := provider.StreamChat(context.Background(), ChatRequest{
		Model: "tooler:latest",
		Messages: []Message{{Role: "user", Content: "hi"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	var text strings.Builder
	var final *ChatChunk
	for chunk := range chunks {
		if chunk.Err != nil {
			t.Fatalf("stream error: %v", chunk.Err)
		}
		text.WriteString(chunk.Text)
		if chunk.Done {
			copy := chunk
			final = &copy
		}
	}
	if text.String() != "Hello world" {
		t.Fatalf("streamed text is wrong: %q", text.String())
	}
	if final == nil || len(final.ToolCalls) != 1 ||
		final.ToolCalls[0].Name != "workspace_list" ||
		final.Usage == nil || final.Usage.InputTokens != 9 || final.Usage.OutputTokens != 5 {
		t.Fatalf("final chunk is wrong: %#v", final)
	}
}

func TestOllamaStreamProtocolAndCancellation(t *testing.T) {
	t.Run("event after done is rejected", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path == "/api/tags" {
			_ = json.NewEncoder(writer).Encode(map[string]any{
				"models": []map[string]any{{"name": "tooler:latest", "model": "tooler:latest",
					"capabilities": []string{"completion", "tools"}}},
			})
			return
		}
			for _, event := range []map[string]any{
				{"model": "tooler:latest", "done": false,
					"message": map[string]any{"role": "assistant", "content": "a"}},
				{"model": "tooler:latest", "done": true, "done_reason": "stop"},
				{"model": "tooler:latest", "done": false,
					"message": map[string]any{"role": "assistant", "content": "b"}},
			} {
				_ = json.NewEncoder(writer).Encode(event)
			}
		}))
		defer server.Close()
		provider := newTestOllamaProvider(t, server)
		if _, err := provider.ListModels(context.Background()); err != nil {
			t.Fatal(err)
		}
		chunks, err := provider.StreamChat(context.Background(), ChatRequest{
			Model: "tooler:latest",
			Messages: []Message{{Role: "user", Content: "hi"}},
		})
		if err != nil {
			t.Fatal(err)
		}
		var sawError bool
		for chunk := range chunks {
			if chunk.Err != nil {
				sawError = true
			}
		}
		if !sawError {
			t.Fatal("post-terminal event was not rejected")
		}
	})
	t.Run("truncated stream is rejected", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path == "/api/tags" {
			_ = json.NewEncoder(writer).Encode(map[string]any{
				"models": []map[string]any{{"name": "tooler:latest", "model": "tooler:latest",
					"capabilities": []string{"completion", "tools"}}},
			})
			return
		}
			_ = json.NewEncoder(writer).Encode(map[string]any{
				"model": "tooler:latest", "done": false,
				"message": map[string]any{"role": "assistant", "content": "partial"},
			})
		}))
		defer server.Close()
		provider := newTestOllamaProvider(t, server)
		if _, err := provider.ListModels(context.Background()); err != nil {
			t.Fatal(err)
		}
		chunks, err := provider.StreamChat(context.Background(), ChatRequest{
			Model: "tooler:latest",
			Messages: []Message{{Role: "user", Content: "hi"}},
		})
		if err != nil {
			t.Fatal(err)
		}
		var sawError bool
		for chunk := range chunks {
			if chunk.Err != nil {
				sawError = true
			}
		}
		if !sawError {
			t.Fatal("truncated stream was not rejected")
		}
	})
	t.Run("cancellation stops the stream", func(t *testing.T) {
		release := make(chan struct{})
		server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path == "/api/tags" {
			_ = json.NewEncoder(writer).Encode(map[string]any{
				"models": []map[string]any{{"name": "tooler:latest", "model": "tooler:latest",
					"capabilities": []string{"completion", "tools"}}},
			})
			return
		}
			writer.Header().Set("Content-Type", "application/x-ndjson")
			flusher, _ := writer.(http.Flusher)
			for index := 0; index < 100; index++ {
				_ = json.NewEncoder(writer).Encode(map[string]any{
					"model": "tooler:latest", "done": false,
					"message": map[string]any{"role": "assistant", "content": "token"},
				})
				if flusher != nil {
					flusher.Flush()
				}
			}
			close(release)
		}))
		defer server.Close()
		provider := newTestOllamaProvider(t, server)
		if _, err := provider.ListModels(context.Background()); err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithCancel(context.Background())
		chunks, err := provider.StreamChat(ctx, ChatRequest{
			Model: "tooler:latest",
			Messages: []Message{{Role: "user", Content: "hi"}},
		})
		if err != nil {
			t.Fatal(err)
		}
		first := <-chunks
		if first.Err != nil || first.Text == "" {
			t.Fatalf("first chunk is wrong: %#v", first)
		}
		cancel()
		for range chunks {
		}
	})
}

func TestOllamaServiceUnreachableDiagnostic(t *testing.T) {
	// A closed loopback port simulates a not-running Ollama service.
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	url := server.URL
	server.Close()
	provider, err := NewOllamaProvider(OllamaConfig{BaseURL: url})
	if err != nil {
		t.Fatal(err)
	}
	_, err = provider.ListModels(context.Background())
	if err == nil {
		t.Fatal("expected an unreachable-service error")
	}
	if ProviderErrorReason(err) != ProviderFailureNetwork ||
		ProviderErrorKind(err) != OutcomeRetryable {
		t.Fatalf("unreachable service was misclassified: %v", err)
	}
	if !strings.Contains(err.Error(), "unreachable") {
		t.Fatalf("diagnostic message is missing: %v", err)
	}
}

func TestOllamaChatValidatesRequests(t *testing.T) {
	provider, err := NewOllamaProvider(OllamaConfig{BaseURL: "http://127.0.0.1:11434"})
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name    string
		request ChatRequest
	}{
		{name: "empty model", request: ChatRequest{
			Messages: []Message{{Role: "user", Content: "hi"}}}},
		{name: "bad temperature", request: ChatRequest{Model: "model:7b", Temperature: 3,
			Messages: []Message{{Role: "user", Content: "hi"}}}},
		{name: "unsupported role", request: ChatRequest{Model: "model:7b",
			Messages: []Message{{Role: "alien", Content: "hi"}}}},
		{name: "json mode unprobed", request: ChatRequest{Model: "model:7b", JSONMode: true,
			Messages: []Message{{Role: "user", Content: "hi"}}}},
		{name: "tools for unprobed model", request: ChatRequest{Model: "model:7b",
			Tools: []ToolSpec{{Name: "workspace_list", Description: "list",
				Parameters: json.RawMessage(`{"type":"object"}`)}},
			Messages: []Message{{Role: "user", Content: "hi"}}}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := provider.Chat(context.Background(), test.request); err == nil {
				t.Fatal("invalid request was accepted")
			}
		})
	}
}

func TestOllamaUsageEstimateBounds(t *testing.T) {
	if usage := ollamaUsage(0, 0, "", 0); usage.InputTokens != 1 || usage.OutputTokens != 1 ||
		usage.TotalTokens != 2 {
		t.Fatalf("floor estimate is wrong: %#v", usage)
	}
	if usage := ollamaUsage(0, 0, "", 100); usage.InputTokens != 25 {
		t.Fatalf("input estimate is wrong: %#v", usage)
	}
	if usage := ollamaUsage(3, 2, "ignored", 0); usage.InputTokens != 3 ||
		usage.OutputTokens != 2 || usage.TotalTokens != 5 {
		t.Fatalf("daemon counts were not preferred: %#v", usage)
	}
}

func TestOllamaModelNameNormalization(t *testing.T) {
	valid := []string{"llama3.2:3b", "namespace/model:tag", "qwen2.5:0.5b", "mistral"}
	for _, name := range valid {
		if _, err := normalizeOllamaModel(name); err != nil {
			t.Fatalf("valid name %q rejected: %v", name, err)
		}
	}
	invalid := []string{"", " ", "has space", "has\ttab", "..", ".hidden",
		"/absolute", "trailing/", "a//b", ":tagless", "a::b", "a\nb"}
	for _, name := range invalid {
		if _, err := normalizeOllamaModel(name); err == nil {
			t.Fatalf("invalid name %q accepted", name)
		}
	}
}

func TestOllamaContextLengthSelection(t *testing.T) {
	modelInfo := map[string]any{
		"general.architecture":       1, // ignored: wrong key
		"llama.context_length":       4096.0,
		"qwen2.context_length":       8192.0,
		"broken.context_length":      -1.0,
		"huge.context_length":        1 << 30,
		"fractional.context_length":  4096.5,
		"string.context_length":      "8192", // ignored: not a number
	}
	if got := ollamaContextLength(modelInfo); got != 8192 {
		t.Fatalf("context length selection is wrong: %d", got)
	}
}

var _ = time.Second

