package llm

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func boundaryProvider(t *testing.T, adapter, endpoint string, client *http.Client) Provider {
	t.Helper()
	var provider Provider
	var err error
	switch adapter {
	case "anthropic":
		provider, err = NewAnthropicCompatibleProvider(AnthropicCompatibleConfig{
			Name: adapter, BaseURL: endpoint, APIKey: "fixture-only", DefaultModel: "model", HTTPClient: client})
	case "openai":
		provider, err = NewOpenAICompatibleProvider(OpenAICompatibleConfig{
			Name: adapter, BaseURL: endpoint, APIKey: "fixture-only", DefaultModel: "model", HTTPClient: client})
	case "responses":
		provider, err = NewOpenAIResponsesProvider(OpenAIResponsesConfig{
			Name: adapter, BaseURL: endpoint, APIKey: "fixture-only", DefaultModel: "model", HTTPClient: client})
	case "ollama":
		provider, err = NewOllamaProvider(OllamaConfig{Name: adapter, BaseURL: endpoint, HTTPClient: client})
	default:
		t.Fatalf("unknown adapter %q", adapter)
	}
	if err != nil {
		t.Fatal(err)
	}
	return provider
}

func boundaryPartialStream(adapter string) string {
	switch adapter {
	case "anthropic":
		return "data: {\"type\":\"message_start\",\"message\":{\"model\":\"model\",\"usage\":{\"input_tokens\":2,\"output_tokens\":0}}}\n\n" +
			"data: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"text\",\"text\":\"\"}}\n\n" +
			"data: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"partial\"}}\n\n"
	case "openai":
		return "data: {\"id\":\"chat_boundary\",\"model\":\"model\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"content\":\"partial\"},\"finish_reason\":null}]}\n\n"
	case "responses":
		return "data: {\"type\":\"response.created\",\"response\":{\"id\":\"resp_boundary\",\"object\":\"response\",\"status\":\"in_progress\",\"model\":\"model\"}}\n\n" +
			"data: {\"type\":\"response.output_item.added\",\"item\":{\"id\":\"msg_boundary\",\"type\":\"message\",\"status\":\"in_progress\",\"role\":\"assistant\"}}\n\n" +
			"data: {\"type\":\"response.output_text.delta\",\"item_id\":\"msg_boundary\",\"delta\":\"partial\"}\n\n"
	case "ollama":
		return "{\"model\":\"model\",\"message\":{\"role\":\"assistant\",\"content\":\"partial\"},\"done\":false}\n"
	}
	return ""
}

func boundaryRequest() ChatRequest {
	return ChatRequest{Model: "model", Messages: []Message{{Role: "user", Content: "hello"}}}
}

func boundaryStreamFailure(t *testing.T, ctx context.Context, provider Provider) (*ProviderError, string) {
	t.Helper()
	chunks, err := provider.StreamChat(ctx, boundaryRequest())
	if err != nil {
		return NormalizeProviderError(provider.Name(), err), ""
	}
	var failure *ProviderError
	var text strings.Builder
	for chunk := range chunks {
		text.WriteString(chunk.Text)
		if chunk.Err != nil {
			failure = NormalizeProviderError(provider.Name(), chunk.Err)
		}
		if chunk.Done && chunk.Err == nil || len(chunk.ToolCalls) != 0 || chunk.Usage != nil {
			t.Fatal("incomplete stream yielded an accepted terminal, tool call or usage")
		}
	}
	return failure, text.String()
}

func TestProviderHTTPTimeoutUsesCallerContextAtIOBoundary(t *testing.T) {
	for _, adapter := range []string{"anthropic", "openai", "responses", "ollama"} {
		for _, phase := range []string{"chat_header", "stream_header", "chat_body", "chat_error_body", "stream_error_body", "stream_body", "stream_partial_frame"} {
			t.Run(adapter+"/"+phase, func(t *testing.T) {
				release := make(chan struct{})
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					_, _ = io.Copy(io.Discard, r.Body)
					if !strings.HasSuffix(phase, "header") {
						w.Header().Set("Content-Type", "text/event-stream")
						if strings.Contains(phase, "error_body") {
							w.WriteHeader(http.StatusServiceUnavailable)
						}
						if phase == "stream_body" || phase == "stream_partial_frame" {
							_, _ = io.WriteString(w, boundaryPartialStream(adapter))
							if phase == "stream_partial_frame" {
								prefix := "data: "
								if adapter == "ollama" {
									prefix = ""
								}
								_, _ = io.WriteString(w, prefix+`{"unfinished":`)
							}
						} else {
							_, _ = io.WriteString(w, `{"unfinished":`)
						}
						w.(http.Flusher).Flush()
					}
					select {
					case <-r.Context().Done():
					case <-release:
					}
				}))
				t.Cleanup(server.Close)
				t.Cleanup(func() { close(release) })
				provider := boundaryProvider(t, adapter, server.URL, &http.Client{Timeout: 400 * time.Millisecond})
				var failure *ProviderError
				ctx := t.Context()
				if strings.HasPrefix(phase, "chat_") {
					response, err := provider.Chat(ctx, boundaryRequest())
					if response != nil {
						t.Fatal("failed HTTP call yielded a response")
					}
					failure = NormalizeProviderError(provider.Name(), err)
				} else {
					var text string
					failure, text = boundaryStreamFailure(t, ctx, provider)
					if (phase == "stream_body" || phase == "stream_partial_frame") && text != "partial" {
						t.Errorf("fixture did not exercise an already started stream: text=%q", text)
					}
				}
				if ctx.Err() != nil || failure == nil || failure.Kind != OutcomeRetryable || failure.Reason != ProviderFailureNetwork ||
					errors.Is(failure, context.Canceled) || errors.Is(failure, context.DeadlineExceeded) {
					t.Fatalf("client timeout was confused with caller cancellation or protocol failure: %+v ctx=%v", failure, ctx.Err())
				}
			})
		}
	}
}

func TestProviderCallerCancellationPreservesStandardCause(t *testing.T) {
	for _, adapter := range []string{"anthropic", "openai", "responses", "ollama"} {
		for _, phase := range []string{"header", "body"} {
			for _, deadline := range []bool{false, true} {
				t.Run(fmt.Sprintf("%s/%s/deadline=%t", adapter, phase, deadline), func(t *testing.T) {
					started, release := make(chan struct{}), make(chan struct{})
					server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
						_, _ = io.Copy(io.Discard, r.Body)
						if phase == "body" {
							_, _ = io.WriteString(w, `{"unfinished":`)
							w.(http.Flusher).Flush()
						}
						close(started)
						select {
						case <-r.Context().Done():
						case <-release:
						}
					}))
					t.Cleanup(server.Close)
					t.Cleanup(func() { close(release) })
					provider := boundaryProvider(t, adapter, server.URL, &http.Client{Timeout: time.Second})
					var ctx context.Context
					var cancel context.CancelFunc
					if deadline {
						ctx, cancel = context.WithTimeout(t.Context(), 300*time.Millisecond)
					} else {
						var cancelCause context.CancelCauseFunc
						ctx, cancelCause = context.WithCancelCause(t.Context())
						cancel = func() { cancelCause(errors.New("private-cancel-cause-canary")) }
					}
					defer cancel()
					if !deadline {
						go func() {
							select {
							case <-started:
								cancel()
							case <-ctx.Done():
							}
						}()
					}
					_, err := provider.Chat(ctx, boundaryRequest())
					failure := NormalizeProviderError(provider.Name(), err)
					if failure == nil || failure.Kind != OutcomeCancelled || !errors.Is(failure, ctx.Err()) || failure.Cause != ctx.Err() {
						t.Fatalf("caller stop lost its safe standard context identity: %+v ctx=%v", failure, ctx.Err())
					}
					if strings.Contains(failure.Error(), "private-cancel-cause-canary") || strings.Contains(fmt.Sprint(failure.Cause), "private-cancel-cause-canary") {
						t.Fatal("private cancellation cause escaped")
					}
				})
			}
		}
	}
}

type boundaryErrorReader struct{ err error }

func (r boundaryErrorReader) Read([]byte) (int, error) { return 0, r.err }
func (boundaryErrorReader) Close() error               { return nil }

func TestProviderStreamProtocolFailuresRemainNonRetryable(t *testing.T) {
	for _, adapter := range []string{"anthropic", "openai", "responses", "ollama"} {
		for _, control := range []string{"malformed", "missing_terminal", "oversize", "reader_error"} {
			t.Run(adapter+"/"+control, func(t *testing.T) {
				provider := boundaryProvider(t, adapter, "http://127.0.0.1:1", &http.Client{})
				var body io.ReadCloser
				if control == "reader_error" {
					body = boundaryErrorReader{errors.New("fixture reader failed")}
				} else {
					payload := boundaryPartialStream(adapter)
					if control != "missing_terminal" {
						prefix := "data: "
						if adapter == "ollama" {
							prefix = ""
						}
						payload += prefix
						if control == "malformed" {
							payload += "{invalid}\n\n"
						} else {
							payload += strings.Repeat("x", 2<<20) + "\n\n"
						}
					}
					body = io.NopCloser(strings.NewReader(payload))
				}
				chunks := make(chan ChatChunk, 32)
				switch p := provider.(type) {
				case *AnthropicCompatibleProvider:
					p.readStream(t.Context(), body, "model", chunks)
				case *OpenAICompatibleProvider:
					p.readStream(t.Context(), body, "model", chunks)
				case *OpenAIResponsesProvider:
					p.readStream(t.Context(), body, "model", chunks)
				case *OllamaProvider:
					p.readStream(t.Context(), body, "model", 0, chunks)
				}
				var failure *ProviderError
				for chunk := range chunks {
					if chunk.Err != nil {
						failure = NormalizeProviderError(provider.Name(), chunk.Err)
					}
					if chunk.Done && chunk.Err == nil {
						t.Fatal("invalid stream was accepted")
					}
				}
				if failure == nil || failure.Kind != OutcomeInvalidResponse || failure.Reason != ProviderFailureProtocolIncompatible {
					t.Fatalf("protocol failure became a retryable transport failure: %+v", failure)
				}
			})
		}
	}
}

func TestOpenAIStreamAcceptsTerminalAtCleanEOF(t *testing.T) {
	provider := boundaryProvider(t, "openai", "http://127.0.0.1:1", &http.Client{}).(*OpenAICompatibleProvider)
	payload := boundaryPartialStream("openai") +
		"data: {\"model\":\"model\",\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}]}\n\n" +
		"data: {\"model\":\"model\",\"choices\":[],\"usage\":{\"prompt_tokens\":2,\"completion_tokens\":1,\"total_tokens\":3}}\n\n" +
		"data: [DONE]"
	chunks := make(chan ChatChunk, 32)
	provider.readStream(t.Context(), io.NopCloser(strings.NewReader(payload)), "model", chunks)
	finals := 0
	for chunk := range chunks {
		if chunk.Err != nil {
			t.Fatal(chunk.Err)
		}
		if chunk.Done {
			finals++
			if chunk.Usage == nil || chunk.Usage.TotalTokens != 3 {
				t.Fatalf("terminal usage lost: %+v", chunk)
			}
		}
	}
	if finals != 1 {
		t.Fatalf("terminal at clean EOF was not accepted exactly once: %d", finals)
	}
}
