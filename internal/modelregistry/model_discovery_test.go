package modelregistry

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"cyberagent-workbench/internal/llm"
)

func TestModelDiscoveryAuthenticatedCatalogPaginationAndNoSideEffects(t *testing.T) {
	var calls atomic.Int32
	const secret = "ordinary-private-key"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.Method != "GET" || r.URL.Path != "/v1/models" || r.ContentLength > 0 || r.Header.Get("x-api-key") != secret || r.Header.Get("anthropic-version") != "2023-06-01" {
			t.Errorf("unexpected catalog request %s %s", r.Method, r.URL.Path)
		}
		if r.Header.Get("x-project") != "lab" {
			t.Error("validated draft header not applied")
		}
		if r.URL.Query().Get("after_id") == "" {
			fmt.Fprint(w, `{"data":[{"id":"first","display_name":"First model","max_input_tokens":200000,"max_tokens":64000}],"has_more":true,"last_id":"first"}`)
		} else {
			fmt.Fprint(w, `{"data":[{"id":"first"},{"id":"second"}],"has_more":false}`)
		}
	}))
	defer srv.Close()
	result, err := DiscoverProviderModels(t.Context(), ModelDiscoveryOptions{ProviderID: "draft-models", EndpointURL: srv.URL + "/v1/messages", Transport: llm.HarnessTransportAnthropicMessages, Secret: secret, AdvancedConfig: json.RawMessage(`{"request_headers":{"x-project":"lab"},"request_body":{"vendor_option":true}}`)})
	if err != nil || len(result.Models) != 2 || calls.Load() != 2 || result.Truncated || result.Models[0].InputTokenLimit != 200000 || result.Models[0].OutputTokenLimit != 64000 {
		t.Fatalf("catalog=%#v err=%v calls=%d", result, err, calls.Load())
	}
}

func TestModelDiscoveryErrorsNeverFallbackOrLeakSecrets(t *testing.T) {
	const secret = "short-ordinary-key"
	for _, status := range []int{http.StatusUnauthorized, http.StatusTooManyRequests, http.StatusFound} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			var redirected atomic.Int32
			target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { redirected.Add(1) }))
			defer target.Close()
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Location", target.URL)
				w.WriteHeader(status)
				fmt.Fprint(w, secret)
			}))
			defer srv.Close()
			result, err := DiscoverProviderModels(t.Context(), ModelDiscoveryOptions{ProviderID: "draft-models", EndpointURL: srv.URL + "/v1/responses", Transport: llm.HarnessTransportOpenAIResponses, Secret: secret})
			if err == nil || len(result.Models) != 0 || strings.Contains(err.Error(), secret) || redirected.Load() != 0 {
				t.Fatalf("unsafe fallback/redirect/error: %#v %v", result, err)
			}
		})
	}

}

func TestModelDiscoveryCancellationAndBoundedCatalog(t *testing.T) {
	entered := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { close(entered); <-r.Context().Done() }))
	defer srv.Close()
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() {
		_, err := DiscoverProviderModels(ctx, ModelDiscoveryOptions{ProviderID: "draft-models", EndpointURL: srv.URL + "/v1/chat/completions", Transport: llm.HarnessTransportOpenAIChatCompletions})
		done <- err
	}()
	<-entered
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("cancel returned %v", err)
	}
	large := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		data := []map[string]string{}
		for i := 0; i < 513; i++ {
			data = append(data, map[string]string{"id": fmt.Sprintf("model-%d", i)})
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"data": data})
	}))
	defer large.Close()
	result, err := DiscoverProviderModels(t.Context(), ModelDiscoveryOptions{ProviderID: "draft-models", EndpointURL: large.URL + "/v1/responses", Transport: llm.HarnessTransportOpenAIResponses})
	if err != nil || !result.Truncated || len(result.Models) != 512 {
		t.Fatalf("catalog bound=%#v err=%v", result, err)
	}
}
