package application_test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"cyberagent-workbench/internal/application"
	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/llm"
	"cyberagent-workbench/internal/policy"
	"cyberagent-workbench/internal/store"
)

// Real HTTP adapter -> stream aggregator -> SQLite -> tool -> next HTTP request.
// No provider credentials or external model calls are used by this test.
func TestSupervisorResponsesPreservesPrivateReplayAndWireCallPair(t *testing.T) {
	verifySupervisorResponsesReplay(t, "")
}

func TestSupervisorResponsesFailedTerminalRetainsKnownUsage(t *testing.T) {
	for _, mode := range []string{"failed", "length", "refusal"} {
		t.Run(mode, func(t *testing.T) { verifySupervisorResponsesReplay(t, mode) })
	}
}

func verifySupervisorResponsesReplay(t *testing.T, finalMode string) {
	finalFails := finalMode != ""
	t.Helper()
	const opaque = "opaque-local-fixture-state"
	const wireCall = "original_provider_call_123"
	var requests atomic.Int32
	firstInput := make(chan []map[string]any, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Input           []map[string]any `json:"input"`
			MaxOutputTokens int              `json:"max_output_tokens"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
			w.WriteHeader(400)
			return
		}
		n := requests.Add(1)
		if n == 1 {
			firstInput <- body.Input
			if body.MaxOutputTokens != 500 {
				t.Errorf("initial output allowance = %d, want 500", body.MaxOutputTokens)
			}
		}
		if n == 2 {
			previous := <-firstInput
			throughUser := 0
			for i, item := range previous {
				if item["role"] == "user" {
					throughUser = i + 1
				}
			}
			if body.MaxOutputTokens != 486 || throughUser == 0 || len(body.Input) < throughUser ||
				!reflect.DeepEqual(previous[:throughUser], body.Input[:throughUser]) {
				t.Error("settling the first call rewrote the Responses prefix through the original user input")
			}
			var reason, call, result bool
			for _, item := range body.Input {
				switch item["type"] {
				case "reasoning":
					reason = item["encrypted_content"] == opaque
				case "function_call":
					call = item["call_id"] == wireCall
				case "function_call_output":
					result = item["call_id"] == wireCall
				}
			}
			if !reason || !call || !result {
				t.Errorf("wire continuation lost state/pair: reasoning=%t call=%t result=%t", reason, call, result)
			}
		}
		if n > 2 {
			t.Error("unexpected extra model request")
			w.WriteHeader(400)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		emit := func(value any) {
			raw, err := json.Marshal(value)
			if err != nil {
				t.Error(err)
				return
			}
			_, _ = fmt.Fprintf(w, "data: %s\n\n", raw)
		}
		responseID := fmt.Sprintf("response_%d", n)
		emit(map[string]any{"type": "response.created", "response": map[string]any{"id": responseID, "object": "response", "status": "in_progress", "model": "model"}})
		if n == 2 && finalFails {
			terminal := map[string]any{"id": responseID, "object": "response", "status": "failed", "model": "model", "usage": map[string]int{"input_tokens": 10, "output_tokens": 4, "total_tokens": 14}}
			switch finalMode {
			case "failed":
				terminal["error"] = map[string]any{"type": "server_error", "code": "server_error", "message": "local failure fixture"}
				emit(map[string]any{"type": "response.failed", "response": terminal})
			case "length", "refusal":
				item := map[string]any{"id": "message_failed", "type": "message", "status": "in_progress", "role": "assistant"}
				emit(map[string]any{"type": "response.output_item.added", "item": item})
				if finalMode == "length" {
					item["status"] = "incomplete"
					item["content"] = []any{map[string]any{"type": "output_text", "text": ""}}
					terminal["status"] = "incomplete"
					terminal["incomplete_details"] = map[string]any{"reason": "max_output_tokens"}
					emit(map[string]any{"type": "response.output_item.done", "item": item})
					emit(map[string]any{"type": "response.incomplete", "response": terminal})
				} else {
					item["status"] = "completed"
					item["content"] = []any{map[string]any{"type": "refusal", "refusal": "local fixture refusal"}}
					terminal["status"] = "completed"
					terminal["output"] = []any{item}
					emit(map[string]any{"type": "response.output_item.done", "item": item})
					emit(map[string]any{"type": "response.completed", "response": terminal})
				}
			}
			return
		}
		var outputs []map[string]any
		if n == 1 {
			emit(map[string]any{"type": "response.output_item.added", "item": map[string]any{"id": "reason_1", "type": "reasoning", "status": "in_progress"}})
			reason := map[string]any{"id": "reason_1", "type": "reasoning", "status": "completed", "summary": []any{}, "encrypted_content": opaque}
			emit(map[string]any{"type": "response.output_item.done", "item": reason})
			call := map[string]any{"id": "function_1", "type": "function_call", "status": "in_progress", "call_id": wireCall, "name": "work_item_create", "arguments": ""}
			emit(map[string]any{"type": "response.output_item.added", "item": call})
			call["status"] = "completed"
			call["arguments"] = `{"title":"Inspect parser","priority":"high"}`
			emit(map[string]any{"type": "response.function_call_arguments.done", "item_id": "function_1", "arguments": call["arguments"]})
			emit(map[string]any{"type": "response.output_item.done", "item": call})
			outputs = []map[string]any{reason, call}
		} else {
			content := rootActionResponse(domain.RootActionContinue, "One work item recorded", "", "")
			item := map[string]any{"id": "message_2", "type": "message", "status": "in_progress", "role": "assistant"}
			emit(map[string]any{"type": "response.output_item.added", "item": item})
			emit(map[string]any{"type": "response.output_text.delta", "item_id": "message_2", "delta": content})
			item["status"] = "completed"
			item["content"] = []any{map[string]any{"type": "output_text", "text": content}}
			emit(map[string]any{"type": "response.output_item.done", "item": item})
			outputs = []map[string]any{item}
		}
		emit(map[string]any{"type": "response.completed", "response": map[string]any{"id": responseID, "object": "response", "status": "completed", "model": "model", "output": outputs, "usage": map[string]int{"input_tokens": 10, "output_tokens": 4, "total_tokens": 14}}})
	}))
	defer server.Close()
	p, err := llm.NewOpenAIResponsesProvider(llm.OpenAIResponsesConfig{Name: "responses-test", BaseURL: server.URL, APIKey: "local-fixture", DefaultModel: "model"})
	if err != nil {
		t.Fatal(err)
	}
	ref := llm.ModelRef{Provider: p.Name(), Model: "model"}
	router := llm.NewRouter(ref)
	router.RegisterProvider(p)
	profile, err := router.HarnessProfile(ref)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	if err := router.SetHarnessQualification(ref, llm.HarnessQualification{ProtocolVersion: llm.ModelHarnessProtocolVersion, BindingDigest: profile.BindingDigest, ToolCallsQualified: true, ToolResultsQualified: true, StrictJSONQualified: true, StreamingQualified: true, QualifiedAt: now, ExpiresAt: now.Add(time.Hour)}); err != nil {
		t.Fatal(err)
	}
	st, err := store.Open(filepath.Join(t.TempDir(), "responses-replay.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	run := newStartedRunForProvider(t, st, p.Name(), domain.Budget{MaxTurns: 3, MaxToolCalls: 3, MaxTokens: 500})
	result, err := application.NewAgentRunner(st, router, policy.NewDefaultChecker()).WithModelRetryPolicy(application.ModelRetryPolicy{MaxAttempts: 1}).Step(t.Context(), run.ID)
	if (err != nil) != finalFails {
		t.Fatal(err)
	}
	if result.Checkpoint.TotalTokens != 28 {
		t.Fatalf("lost successful or failed terminal usage: %d", result.Checkpoint.TotalTokens)
	}
	if requests.Load() != 2 || result.ToolCalls != 1 {
		t.Fatalf("requests=%d toolCalls=%d", requests.Load(), result.ToolCalls)
	}
	events, err := st.ListRunEvents(t.Context(), run.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, event := range events {
		if strings.Contains(event.PayloadJSON, opaque) {
			t.Fatal("private replay leaked into public events")
		}
	}
	if finalFails {
		completed, failed := 0, 0
		for _, event := range events {
			if event.Type == "model.completed" {
				completed++
			}
			if event.Type == "model.failed" {
				failed++
				var payload struct {
					FailureReason llm.ProviderFailureReason `json:"failure_reason"`
					Outcome       llm.Outcome               `json:"outcome"`
				}
				if err := json.Unmarshal([]byte(event.PayloadJSON), &payload); err != nil {
					t.Fatal(err)
				}
				if finalMode == "length" && payload.FailureReason != llm.ProviderFailureOutputLimit || finalMode == "refusal" && payload.FailureReason != llm.ProviderFailureRefusal || payload.Outcome == llm.OutcomeInvalidResponse {
					t.Fatalf("typed failure changed into protocol error: mode=%s reason=%s outcome=%s", finalMode, payload.FailureReason, payload.Outcome)
				}
			}
		}
		if completed != 1 || failed != 1 {
			t.Fatalf("wrong terminal events completed=%d failed=%d", completed, failed)
		}
	}
	messages, err := st.ListSessionMessages(t.Context(), run.SessionID, true)
	if err != nil {
		t.Fatal(err)
	}
	for _, message := range messages {
		if strings.Contains(message.Content, opaque) {
			t.Fatal("private replay leaked into conversation history")
		}
	}
}
