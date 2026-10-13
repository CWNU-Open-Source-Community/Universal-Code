package application_test

import (
	"encoding/json"
	"io"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"cyberagent-workbench/internal/apperror"
	"cyberagent-workbench/internal/application"
	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/llm"
	"cyberagent-workbench/internal/pricing"
	"cyberagent-workbench/internal/store"
)

func TestMonetaryReservationIncludesKimiPrivateReplay(t *testing.T) {
	const reasoning = "private history "
	requests := 0
	provider, err := llm.NewOpenAICompatibleProvider(llm.OpenAICompatibleConfig{
		Name: "kimi", BaseURL: "https://api.moonshot.ai/v1", DefaultModel: "kimi-k3", APIKey: "fixture-only",
		HTTPClient: &http.Client{Transport: kimiSupervisorTransport(func(r *http.Request) (*http.Response, error) {
			requests++
			var body struct {
				Messages []struct {
					Reasoning string `json:"reasoning_content"`
				} `json:"messages"`
			}
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				return nil, err
			}
			if requests == 2 && body.Messages[0].Reasoning != strings.Repeat(reasoning, 1000) {
				t.Error("Kimi did not transmit the private replay")
			}
			response, err := json.Marshal(map[string]any{"id": "fixture-response", "model": "kimi-k3",
				"choices": []any{map[string]any{"index": 0, "finish_reason": "stop", "message": map[string]any{
					"role": "assistant", "content": "ok", "reasoning_content": strings.Repeat(reasoning, 1000)}}},
				"usage": map[string]any{"prompt_tokens": 10, "completion_tokens": 1, "total_tokens": 11}})
			return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(string(response)))}, err
		})},
	})
	if err != nil {
		t.Fatal(err)
	}
	first, err := provider.Chat(t.Context(), llm.ChatRequest{Model: "kimi-k3", MaxTokens: 1, Messages: []llm.Message{{Role: "user", Content: "go"}}})
	if err != nil {
		t.Fatal(err)
	}
	request := llm.ChatRequest{Model: "kimi-k3", MaxTokens: 1, Messages: []llm.Message{
		{Role: "assistant", Content: first.Text, Replay: first.Replay}, {Role: "user", Content: "continue"},
	}}
	if _, err := provider.Chat(t.Context(), request); err != nil {
		t.Fatal(err)
	}
	st, err := store.Open(filepath.Join(t.TempDir(), "private-replay.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	_, run, err := application.NewRunService(st).Create(t.Context(), application.CreateRunRequest{
		Goal: "private replay budget", Profile: "code", ModelRoute: "kimi/kimi-k3",
		Budget: domain.Budget{MaxTurns: 3, MaxCostUSD: 0.001},
	})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	snapshot := pricing.Snapshot{ProtocolVersion: pricing.ProtocolVersion, ID: "private-replay-prices",
		Source: pricing.SourceOperatorImport, Currency: pricing.CurrencyUSD, ImportedBy: "test", ImportedAt: now,
		ValidFrom: now.Add(-time.Minute), ValidUntil: now.Add(time.Hour), Entries: []pricing.Entry{
			{Provider: "kimi", Model: "kimi-k3", InputPerMillionMicros: 1_000_000},
		}}
	snapshot.Fingerprint = pricing.Fingerprint(snapshot)
	if _, _, err := st.ImportPriceSnapshot(t.Context(), snapshot); err != nil {
		t.Fatal(err)
	}
	money := application.NewMonetaryBudgetService(st)
	attempt := llm.ModelAttempt{Number: 1, Provider: "kimi", Model: "kimi-k3"}
	publicOnly := request
	publicOnly.Messages = append([]llm.Message(nil), request.Messages...)
	publicOnly.Messages[0].Replay = nil
	if _, err := money.ReserveModelCall(t.Context(), run, domain.MonetaryScopeRoot, attempt, publicOnly); err != nil {
		t.Fatalf("short public history should fit: %v", err)
	}
	attempt.Number++
	if _, err := money.ReserveModelCall(t.Context(), run, domain.MonetaryScopeRoot, attempt, request); apperror.CodeOf(err) != apperror.CodeResourceExhausted {
		t.Fatalf("private replay bypassed the monetary limit: %v", err)
	}
}
