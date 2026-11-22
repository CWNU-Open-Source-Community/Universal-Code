package application_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
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
)

func TestSupervisorRequestKeepsPrefixAcrossMeteredToolRounds(t *testing.T) {
	st, _, _, input := toolBoundaryFixture(t, domain.Budget{MaxTurns: 3, MaxToolCalls: 4, MaxTokens: 3000})
	input.Content = "Read the example file and report the result."
	provider := &boundaryJourneyProvider{}
	provider.respond = func(_ context.Context, _ llm.ChatRequest, index int) (*llm.ChatResponse, error) {
		if index <= 2 {
			return boundaryRead(fmt.Sprintf("read-%d", index), 1), nil
		}
		return textResponse(rootActionResponse(domain.RootActionFinish, "Read the example file.", "", "")), nil
	}
	if _, err := toolBoundaryService(st, st, provider).Execute(t.Context(), input); err != nil {
		t.Fatal(err)
	}
	requests := provider.Requests()
	if len(requests) != 3 {
		t.Fatalf("got %d model requests, want 3", len(requests))
	}
	for i, request := range requests {
		if request.MaxTokens != 3000-4*i {
			t.Fatalf("round %d: effective output limit %d", i, request.MaxTokens)
		}
		want := fmt.Sprintf("output limit of %d tokens", request.MaxTokens)
		firstCall, currentFacts, operator := -1, -1, -1
		for n, message := range request.Messages {
			if message.Role == "user" && strings.Contains(message.Content, input.Content) {
				operator = n
			}
			if len(message.ToolCalls) != 0 && firstCall == -1 {
				firstCall = n
			}
			if strings.Contains(message.Content, want) {
				currentFacts = n
			}
		}
		if operator < 0 || currentFacts <= operator || (i > 0 && (firstCall < 0 || currentFacts <= firstCall)) {
			t.Fatalf("round %d: current allowance is missing from the current input or tool-result tail", i)
		}
		if i == 2 {
			previous := requests[i-1]
			if !reflect.DeepEqual(previous.Messages[:firstCall+1], request.Messages[:firstCall+1]) ||
				!reflect.DeepEqual(previous.Messages[firstCall+1].ToolResults, request.Messages[firstCall+1].ToolResults) {
				t.Fatal("rebuilding the tool request changed the prefix through the first native call or its result payload")
			}
			if !reflect.DeepEqual(previous.Tools, request.Tools) {
				t.Fatal("unchanged capabilities produced different tool schemas")
			}
		}
	}
}

func TestSupervisorAnthropicMessagesKeepsPreviousRequestAcrossUnmeteredReads(t *testing.T) {
	st, _, root, input := toolBoundaryFixture(t, domain.Budget{MaxTurns: 3, MaxToolCalls: 4})
	input.Content = "Read the first two lines of the example."
	if err := os.WriteFile(filepath.Join(root, "README.md"), []byte("first line\nsecond line\n"), 0600); err != nil {
		t.Fatal(err)
	}

	type wireRequest struct {
		System    json.RawMessage   `json:"system"`
		Tools     json.RawMessage   `json:"tools"`
		Messages  []json.RawMessage `json:"messages"`
		MaxTokens int               `json:"max_tokens"`
	}
	var calls atomic.Int32
	recorded := make(chan wireRequest, 4)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var wire wireRequest
		if err := json.NewDecoder(r.Body).Decode(&wire); err != nil {
			t.Error(err)
			http.Error(w, "invalid request", http.StatusBadRequest)
			return
		}
		n := calls.Add(1)
		if n > 3 {
			t.Error("unexpected extra model request")
			http.Error(w, "unexpected request", http.StatusBadRequest)
			return
		}
		recorded <- wire
		w.Header().Set("Content-Type", "text/event-stream")
		if n == 3 {
			boundaryAnthropicText(w, rootActionResponse(domain.RootActionFinish, "Read both lines.", "", ""))
			boundaryAnthropicEvent(w, map[string]any{"type": "message_delta",
				"delta": map[string]any{"stop_reason": "end_turn"}, "usage": map[string]any{"output_tokens": 2}})
			boundaryAnthropicEvent(w, map[string]any{"type": "message_stop"})
			return
		}
		boundaryAnthropicEvent(w, map[string]any{"type": "message_start", "message": map[string]any{
			"id": fmt.Sprintf("message-%d", n), "model": "model", "usage": map[string]any{"input_tokens": 2}}})
		boundaryAnthropicEvent(w, map[string]any{"type": "content_block_start", "index": 0,
			"content_block": map[string]any{"type": "tool_use", "id": fmt.Sprintf("read-%d", n), "name": "workspace_read", "input": map[string]any{}}})
		args := fmt.Sprintf(`{"version":"agent-code-tools.v1","path":"README.md","start_line":%d,"end_line":%d}`, n, n)
		boundaryAnthropicEvent(w, map[string]any{"type": "content_block_delta", "index": 0,
			"delta": map[string]any{"type": "input_json_delta", "partial_json": args}})
		boundaryAnthropicEvent(w, map[string]any{"type": "content_block_stop", "index": 0})
		boundaryAnthropicEvent(w, map[string]any{"type": "message_delta",
			"delta": map[string]any{"stop_reason": "tool_use"}, "usage": map[string]any{"output_tokens": 2}})
		boundaryAnthropicEvent(w, map[string]any{"type": "message_stop"})
	}))
	defer server.Close()

	provider, err := llm.NewAnthropicCompatibleProvider(llm.AnthropicCompatibleConfig{
		Name: "tool-loop", BaseURL: server.URL, APIKey: "fixture-only", DefaultModel: "model",
	})
	if err != nil {
		t.Fatal(err)
	}
	ref := llm.ModelRef{Provider: provider.Name(), Model: "model"}
	router := llm.NewRouter(ref)
	router.RegisterProvider(provider)
	profile, err := router.HarnessProfile(ref)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	if err := router.SetHarnessQualification(ref, llm.HarnessQualification{
		ProtocolVersion: llm.ModelHarnessProtocolVersion, BindingDigest: profile.BindingDigest,
		ToolCallsQualified: true, ToolResultsQualified: true, StrictJSONQualified: true, StreamingQualified: true,
		QualifiedAt: now, ExpiresAt: now.Add(time.Hour),
	}); err != nil {
		t.Fatal(err)
	}
	service := application.NewThreadTurnService(st, application.NewRunLifecycleControlService(st),
		application.NewRunExecutionHandoffService(st, router, policy.NewDefaultChecker()).WithGeneratedContextCompaction(false))
	if _, err := service.Execute(t.Context(), input); err != nil {
		t.Fatal(err)
	}
	if got := len(recorded); got != 3 {
		t.Fatalf("got %d Messages requests, want 3", got)
	}
	previous := <-recorded
	for round := 2; round <= 3; round++ {
		current := <-recorded
		if previous.MaxTokens != current.MaxTokens || !bytes.Equal(previous.System, current.System) || !bytes.Equal(previous.Tools, current.Tools) {
			t.Fatalf("round %d changed the fixed cap, instructions or unchanged tool schemas", round)
		}
		if len(current.Messages) != len(previous.Messages)+2 || !reflect.DeepEqual(previous.Messages, current.Messages[:len(previous.Messages)]) {
			t.Fatalf("round %d changed the previous Messages request prefix (%d then %d messages)", round, len(previous.Messages), len(current.Messages))
		}
		line := []string{"first line", "second line"}[round-2]
		if !bytes.Contains(current.Messages[len(current.Messages)-2], []byte(`"tool_use"`)) ||
			!bytes.Contains(current.Messages[len(current.Messages)-1], []byte(`"tool_result"`)) ||
			!bytes.Contains(current.Messages[len(current.Messages)-1], []byte(line)) {
			t.Fatalf("round %d did not append the native tool call and result", round)
		}
		previous = current
	}
}
