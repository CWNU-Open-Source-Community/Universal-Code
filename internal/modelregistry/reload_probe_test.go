package modelregistry

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strings"
	"sync/atomic"
	"testing"

	"cyberagent-workbench/internal/llm"
)

func TestRegistryReloadReplacesQualificationProbePlan(t *testing.T) {
	for _, family := range []struct {
		name, endpoint, model string
		calls                 int
	}{
		{"kimi", "https://api.moonshot.ai/v1", "kimi-k3", 4},
		{"gemini", "https://generativelanguage.googleapis.com/v1beta/openai", "gemini-3.7-flash", 3},
	} {
		for _, change := range []string{"endpoint", "mapping", "add provider"} {
			t.Run(family.name+"/"+change, func(t *testing.T) {
				var calls atomic.Int32
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					calls.Add(1)
					var body struct {
						Model    string `json:"model"`
						Messages []struct {
							Content string `json:"content"`
						} `json:"messages"`
					}
					if err := json.NewDecoder(r.Body).Decode(&body); err != nil || len(body.Messages) < 2 {
						t.Errorf("invalid qualification request: %v", err)
						w.WriteHeader(http.StatusBadRequest)
						return
					}
					nonces := regexp.MustCompile(`[0-9a-f]{32}`).FindAllString(body.Messages[0].Content+body.Messages[1].Content, -1)
					if len(nonces) == 0 {
						t.Error("qualification nonce is missing")
						w.WriteHeader(http.StatusBadRequest)
						return
					}
					round := len(body.Messages) / 2
					sequential := strings.Contains(body.Messages[0].Content, "K3 model") || strings.Contains(body.Messages[0].Content, "Gemini model")
					delta := map[string]any{"role": "assistant"}
					if family.name == "kimi" {
						delta["reasoning_content"] = "private reload qualification reasoning"
					}
					finish := "stop"
					if round == 1 || sequential && round == 2 {
						args, _ := json.Marshal(map[string]string{"nonce": nonces[round-1]})
						tool := map[string]any{"index": 0, "id": fmt.Sprintf("reload-tool-%d", round), "type": "function",
							"function": map[string]string{"name": "prayu_harness_echo", "arguments": string(args)}}
						if family.name == "gemini" {
							tool["extra_content"] = map[string]any{"google": map[string]string{"thought_signature": "private reload signature"}}
						}
						delta["tool_calls"], finish = []any{tool}, "tool_calls"
					} else {
						nonce := nonces[0]
						if sequential && round == 4 {
							nonce = nonces[1]
						}
						ack, _ := json.Marshal(harnessProbeResponse{Version: HarnessProbeProtocolVersion, Status: "ok", Nonce: nonce})
						delta["content"] = string(ack)
					}
					payload, _ := json.Marshal(map[string]any{"id": fmt.Sprintf("reload-response-%d", round), "model": body.Model,
						"choices": []any{map[string]any{"index": 0, "delta": delta, "finish_reason": finish}},
						"usage":   map[string]int{"prompt_tokens": 2, "completion_tokens": 3, "total_tokens": 5}})
					w.Header().Set("Content-Type", "text/event-stream")
					_, _ = fmt.Fprintf(w, "data: %s\n\ndata: [DONE]\n\n", payload)
				}))
				defer server.Close()
				target, _ := url.Parse(server.URL)
				originalTransport := http.DefaultTransport
				http.DefaultTransport = geminiHarnessTransport(func(request *http.Request) (*http.Response, error) {
					copy := request.Clone(request.Context())
					copy.URL.Scheme, copy.URL.Host = target.Scheme, target.Host
					return originalTransport.RoundTrip(copy)
				})
				defer func() { http.DefaultTransport = originalTransport }()
				registry, err := newRegistry(nil, func(context.Context, string) (string, bool, error) { return "fixture", true, nil })
				if err != nil {
					t.Fatal(err)
				}
				definition := validCustomDefinition(family.endpoint)
				definition.AdvancedConfig, _ = json.Marshal(map[string]any{"model_mapping": map[string]string{"acme-code": family.model}})
				settings := routeSettings{}
				if change != "add provider" {
					settings[ProviderDefinitionsSettingKey] = providerDefinitionSetting(t, definition, 1)
				}
				if err := registry.LoadRouteSettings(t.Context(), settings); err != nil {
					t.Fatal(err)
				}
				wantCalls := 2
				switch change {
				case "endpoint":
					definition.EndpointURL = server.URL
				case "mapping":
					definition.AdvancedConfig = json.RawMessage(`{"model_mapping":{"acme-code":"ordinary-model"}}`)
				case "add provider":
					wantCalls = family.calls
				}
				settings[ProviderDefinitionsSettingKey] = providerDefinitionSetting(t, definition, 2)
				if _, err := registry.Reload(t.Context(), settings); err != nil {
					t.Fatal(err)
				}
				result, err := registry.QualifyHarness(t.Context(), settings, definition.ID, definition.DefaultModel)
				if err != nil || result.Status != HarnessDiagnosticQualified || result.ModelCalls != wantCalls || int(calls.Load()) != wantCalls {
					t.Fatalf("qualification used a stale probe plan: result=%+v calls=%d want=%d err=%v", result, calls.Load(), wantCalls, err)
				}
			})
		}
	}
}

func TestRegistryReloadUsesCurrentOllamaForCapabilities(t *testing.T) {
	for _, initialEndpoint := range []bool{false, true} {
		t.Run(fmt.Sprintf("initial_endpoint=%t", initialEndpoint), func(t *testing.T) {
			var oldCalls, newCalls atomic.Int32
			old := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				oldCalls.Add(1)
				w.WriteHeader(http.StatusInternalServerError)
			}))
			defer old.Close()
			current := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				newCalls.Add(1)
				_ = json.NewEncoder(w).Encode(map[string]any{
					"capabilities": []string{"completion", "tools"},
					"model_info":   map[string]any{"llama.context_length": 8192},
				})
			}))
			defer current.Close()
			values := map[string]string{}
			if initialEndpoint {
				values["CYBERAGENT_OLLAMA_BASE_URL"] = old.URL
				values["CYBERAGENT_OLLAMA_MODEL"] = "tooler:7b"
			}
			registry := New(func(key string) (string, bool) { value, found := values[key]; return value, found })
			values["CYBERAGENT_OLLAMA_BASE_URL"], values["CYBERAGENT_OLLAMA_MODEL"] = current.URL, "tooler:7b"
			if _, err := registry.Reload(t.Context(), routeSettings{}); err != nil {
				t.Fatal(err)
			}
			if _, err := registry.SelectRoute(t.Context(), routeSettings{}, "code", "ollama", "tooler:7b"); err != nil {
				t.Fatal(err)
			}
			ref := llm.ModelRef{Provider: "ollama", Model: "tooler:7b"}
			window := registry.Router().ContextWindow(ref)
			if oldCalls.Load() != 0 || newCalls.Load() == 0 || window.WindowTokens != 8192 || window.Source != "ollama_probe" {
				t.Fatalf("reload retained stale capability provider: old=%d new=%d window=%+v", oldCalls.Load(), newCalls.Load(), window)
			}
		})
	}
}
