package llm

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
)

func TestProviderTruncatedHTTPBodyIsRetryable(t *testing.T) {
	for _, adapter := range []string{"anthropic", "openai", "responses", "ollama"} {
		for _, phase := range []string{"chat", "stream", "stream_chunked", "stream_error"} {
			t.Run(fmt.Sprintf("%s/%s", adapter, phase), func(t *testing.T) {
				payload := `{"unfinished":`
				if phase == "stream" || phase == "stream_chunked" {
					payload = boundaryPartialStream(adapter)
				}
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					_, _ = io.Copy(io.Discard, r.Body)
					if phase == "stream_chunked" {
						conn, wire, err := w.(http.Hijacker).Hijack()
						if err != nil {
							t.Error(err)
							return
						}
						defer conn.Close()
						// Close after one chunk, without the final zero-length chunk.
						_, _ = fmt.Fprintf(wire, "HTTP/1.1 200 OK\r\nTransfer-Encoding: chunked\r\n\r\n%x\r\n%s\r\n", len(payload), payload)
						if err := wire.Flush(); err != nil {
							t.Error(err)
						}
						return
					}
					// The HTTP transport, not the provider's JSON decoder, detects
					// that the connection closed before the promised body arrived.
					w.Header().Set("Content-Length", strconv.Itoa(len(payload)+64))
					if phase == "stream_error" {
						w.WriteHeader(http.StatusServiceUnavailable)
					}
					_, _ = io.WriteString(w, payload)
				}))
				defer server.Close()
				provider := boundaryProvider(t, adapter, server.URL, server.Client())
				var failure *ProviderError
				if phase == "chat" {
					response, err := provider.Chat(t.Context(), boundaryRequest())
					if response != nil {
						t.Fatal("truncated response was accepted")
					}
					failure = NormalizeProviderError(provider.Name(), err)
				} else {
					var text string
					failure, text = boundaryStreamFailure(t, t.Context(), provider)
					if (phase == "stream" || phase == "stream_chunked") && text != "partial" {
						t.Fatalf("fixture did not reach partial output: %q", text)
					}
				}
				if failure == nil || failure.Kind != OutcomeRetryable || failure.Reason != ProviderFailureNetwork {
					t.Fatalf("HTTP body truncation must remain eligible for bounded transport retry: %#v", failure)
				}
			})
		}
	}
}
