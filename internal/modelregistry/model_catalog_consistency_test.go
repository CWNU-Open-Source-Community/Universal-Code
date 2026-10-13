package modelregistry

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"cyberagent-workbench/internal/llm"
)

func catalogTestRegistry(t *testing.T, definition ProviderDefinition, secret string) *Registry {
	t.Helper()
	registry, err := newRegistry(func(string) (string, bool) { return "", false },
		func(_ context.Context, id string) (string, bool, error) {
			return secret, id == definition.ID, nil
		})
	if err != nil {
		t.Fatal(err)
	}
	if err := registry.LoadRouteSettings(t.Context(), routeSettings{
		ProviderDefinitionsSettingKey: providerDefinitionSetting(t, definition, 1),
	}); err != nil {
		t.Fatal(err)
	}
	return registry
}

func TestModelCatalogDraftAndRegisteredListingShareProtocol(t *testing.T) {
	const secret = "catalog-private-key"
	for _, transport := range []string{ProviderTransportAnthropicMessages, ProviderTransportOpenAIChatCompletions, ProviderTransportOpenAIResponses} {
		t.Run(transport, func(t *testing.T) {
			var requests []string
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodGet || r.URL.Path != "/gateway/v1/models" || r.ContentLength > 0 || r.Header.Get("x-project") != "catalog-test" {
					t.Errorf("unexpected catalog request: %s %s", r.Method, r.URL)
				}
				cursor := r.URL.Query().Get("after")
				if transport == ProviderTransportAnthropicMessages {
					cursor = r.URL.Query().Get("after_id")
					if r.Header.Get("x-api-key") != secret || r.Header.Get("Authorization") != "Bearer "+secret || r.Header.Get("anthropic-version") != llm.AnthropicVersion {
						t.Error("Anthropic catalog authentication missing")
					}
				} else if r.Header.Get("Authorization") != "Bearer "+secret {
					t.Error("OpenAI catalog authentication missing")
				}
				requests = append(requests, r.URL.RequestURI())
				if cursor == "" {
					fmt.Fprint(w, `{"data":[{"id":"first","display_name":"First","max_input_tokens":200000}],"has_more":true,"last_id":"first"}`)
				} else {
					fmt.Fprint(w, `{"data":[{"id":"first"},{"id":"second","display_name":"Second"}],"has_more":false}`)
				}
			}))
			defer server.Close()
			definition := validCustomDefinition(server.URL + "/gateway/v1")
			definition.Transport = transport
			definition.AdvancedConfig = json.RawMessage(`{"request_headers":{"x-project":"catalog-test"},"request_body":{"vendor_option":true}}`)
			registry := catalogTestRegistry(t, definition, secret)
			draft, err := DiscoverProviderModels(t.Context(), ModelDiscoveryOptions{
				ProviderID: definition.ID, EndpointURL: definition.EndpointURL, Transport: transport,
				Secret: secret, AdvancedConfig: definition.AdvancedConfig,
			})
			if err != nil {
				t.Fatal(err)
			}
			listed, err := registry.Router().ListModels(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			var registered []DiscoveredModel
			for _, model := range listed {
				if model.Provider == definition.ID {
					registered = append(registered, DiscoveredModel{ID: model.ID, DisplayName: model.DisplayName})
					if len(model.Capabilities) != 0 {
						t.Fatal("catalog membership asserted unqualified capabilities")
					}
				}
			}
			if len(draft.Models) != 2 || draft.Models[0].InputTokenLimit != 200000 || draft.Source != "provider_api" || draft.Truncated {
				t.Fatalf("unexpected draft catalog: %#v", draft)
			}
			draft.Models[0].InputTokenLimit = 0
			if !reflect.DeepEqual(draft.Models, registered) || len(requests) != 4 || !reflect.DeepEqual(requests[:2], requests[2:]) {
				t.Fatalf("catalog entries or requests differ: draft=%#v listed=%#v requests=%v", draft.Models, registered, requests)
			}
		})
	}
}

type catalogRoundTripper func(*http.Request) (*http.Response, error)

func (f catalogRoundTripper) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestModelCatalogDeepSeekDraftAndRegisteredListingUseOfficialCatalog(t *testing.T) {
	// This sequential test intercepts every request; no external network is used.
	previous := http.DefaultTransport
	t.Cleanup(func() { http.DefaultTransport = previous })
	var requests []string
	http.DefaultTransport = catalogRoundTripper(func(r *http.Request) (*http.Response, error) {
		requests = append(requests, r.URL.String())
		if r.URL.String() != "https://api.deepseek.com/models" || r.Header.Get("Authorization") != "Bearer catalog-private-key" || r.Header.Get("x-api-key") != "" {
			t.Errorf("incorrect DeepSeek catalog request: %s", r.URL)
		}
		return &http.Response{StatusCode: http.StatusOK, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(`{"data":[{"id":"deepseek-v4-flash"}]}`))}, nil
	})
	for _, endpoint := range []string{"https://api.deepseek.com/anthropic", "https://api.deepseek.com/anthropic/v1/messages"} {
		definition := validCustomDefinition(endpoint)
		definition.Transport = ProviderTransportAnthropicMessages
		registry := catalogTestRegistry(t, definition, "catalog-private-key")
		if _, err := DiscoverProviderModels(t.Context(), ModelDiscoveryOptions{ProviderID: definition.ID,
			EndpointURL: endpoint, Transport: definition.Transport, Secret: "catalog-private-key"}); err != nil {
			t.Fatal(err)
		}
		if _, err := registry.Router().ListModels(t.Context()); err != nil {
			t.Fatal(err)
		}
	}
	if len(requests) != 4 {
		t.Fatalf("catalog requests=%v", requests)
	}
}

func TestModelCatalogDraftAndRegisteredListingReturnFailures(t *testing.T) {
	const secret = "catalog-private-key"
	for _, status := range []int{http.StatusUnauthorized, http.StatusTooManyRequests, http.StatusOK} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(status)
				fmt.Fprint(w, secret)
			}))
			defer server.Close()
			definition := validCustomDefinition(server.URL)
			definition.Transport = ProviderTransportAnthropicMessages
			registry := catalogTestRegistry(t, definition, secret)
			draft, draftErr := DiscoverProviderModels(t.Context(), ModelDiscoveryOptions{
				ProviderID: definition.ID, EndpointURL: definition.EndpointURL, Transport: definition.Transport, Secret: secret})
			listed, listedErr := registry.Router().ListModels(t.Context())
			var first, second *llm.ProviderError
			if !errors.As(draftErr, &first) || !errors.As(listedErr, &second) || len(draft.Models) != 0 || len(listed) != 0 ||
				first.Kind != second.Kind || first.StatusCode != second.StatusCode || strings.Contains(draftErr.Error(), secret) || strings.Contains(listedErr.Error(), secret) {
				t.Fatalf("failure differs or fell back: draft=%#v/%v listed=%#v/%v", draft, draftErr, listed, listedErr)
			}
		})
	}
}
