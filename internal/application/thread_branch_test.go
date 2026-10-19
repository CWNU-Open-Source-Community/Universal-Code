package application_test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"path/filepath"
	"strings"
	"testing"

	"cyberagent-workbench/internal/application"
	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/events"
	"cyberagent-workbench/internal/llm"
	"cyberagent-workbench/internal/policy"
	"cyberagent-workbench/internal/store"
)

func TestThreadBranchHistoricalCommentaryKeepsDeliveredInputBeforeEveryUpdate(t *testing.T) {
	first := toolResponse("branch-note-1", "note_create", `{"title":"First observation","content":"first fact"}`)
	first.Text = "first public update"
	second := toolResponse("branch-note-2", "note_create", `{"title":"Second observation","content":"second fact"}`)
	second.Text = "selected public update"
	provider := &scriptedToolProvider{responses: []*llm.ChatResponse{first, second, textResponse(rootActionResponse(domain.RootActionFinish, "FUTURE_FINAL_ANSWER", "done", ""))}}
	st, turns, input := threadControlFixture(t, provider)
	input.Content = "CURRENT_ATTEMPT_INPUT"
	finished, err := turns.Execute(t.Context(), input)
	if err != nil {
		t.Fatal(err)
	}
	eventList, err := st.ListRunEvents(t.Context(), finished.Submission.Run.ID)
	if err != nil {
		t.Fatal(err)
	}
	anchor := ""
	for _, event := range eventList {
		var p struct{ Text string }
		_ = json.Unmarshal([]byte(event.PayloadJSON), &p)
		if event.Type == events.ModelPublicCommentaryEvent && p.Text == second.Text {
			anchor = event.EventID
		}
	}
	if anchor == "" {
		t.Fatal("tool loop did not produce the selected durable update")
	}
	branch, err := application.NewThreadBranchService(st).Branch(t.Context(), domain.ThreadBranchRequest{Version: domain.ThreadBranchProtocolVersion, ThreadID: input.ThreadID, SourceEventID: anchor, OperationKey: "historical-commentary-branch", RequestedBy: "test_operator"})
	if err != nil {
		t.Fatal(err)
	}
	messages, err := st.ListSessionMessages(t.Context(), branch.Session.ID, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(messages) != 3 || messages[0].Content != input.Content || messages[1].Content != first.Text || messages[2].Content != second.Text {
		t.Fatalf("input was lost, duplicated or ordered after an update: %+v", messages)
	}
	if len(provider.Requests()) != 3 {
		t.Fatal("branch repeated original tools or provider work")
	}
}

func TestThreadBranchKimiContinuesWithHistoricalEvidenceAndFreshNativeReplay(t *testing.T) {
	calls := 0
	router := kimiSupervisorFixtureRouter(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Messages []map[string]json.RawMessage `json:"messages"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
			w.WriteHeader(400)
			return
		}
		calls++
		encoded, _ := json.Marshal(body.Messages)
		if calls >= 3 {
			if !strings.Contains(string(encoded), "BRANCH_ORIGINAL_INTENT") || !strings.Contains(string(encoded), "selected answer") || strings.Contains(string(encoded), "FUTURE_ANSWER_CANARY") || strings.Contains(string(encoded), "native-private-1") || strings.Contains(string(encoded), "native-private-2") {
				t.Error("branch model input lost selected history or inherited future/private source")
			}
			assistants := 0
			for _, message := range body.Messages {
				var role, reason string
				_ = json.Unmarshal(message["role"], &role)
				_ = json.Unmarshal(message["reasoning_content"], &reason)
				if role == "assistant" {
					assistants++
					if calls == 3 || reason != "native-private-3" {
						t.Error("imported assistant was sent without fresh native replay")
					}
				}
			}
			if calls == 4 && assistants != 1 {
				t.Error("second branch turn did not use its own native history")
			}
		}
		text := "selected answer"
		if calls == 2 {
			text = "FUTURE_ANSWER_CANARY"
		}
		if calls >= 3 {
			text = "continued branch answer"
		}
		w.Header().Set("Content-Type", "text/event-stream")
		emit := func(delta any, finish string) {
			event := map[string]any{"id": fmt.Sprintf("branch-kimi-%d", calls), "model": "kimi-k3", "choices": []any{map[string]any{"index": 0, "delta": delta, "finish_reason": finish}}}
			if finish != "" {
				event["usage"] = map[string]int{"prompt_tokens": 2, "completion_tokens": 3, "total_tokens": 5}
			}
			raw, _ := json.Marshal(event)
			_, _ = fmt.Fprintf(w, "data: %s\n\n", raw)
		}
		emit(map[string]any{"role": "assistant", "reasoning_content": fmt.Sprintf("native-private-%d", calls)}, "")
		emit(map[string]any{"content": rootActionResponse(domain.RootActionContinue, text, "", "")}, "stop")
		_, _ = fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	st, err := store.Open(filepath.Join(t.TempDir(), "branch-kimi.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	first := newStartedRunForProvider(t, st, "kimi-root", domain.Budget{MaxTurns: 8})
	agent := application.NewAgentRunner(st, router, policy.NewDefaultChecker()).WithModelRetryPolicy(application.ModelRetryPolicy{MaxAttempts: 1})
	if _, err := agent.StepWithInput(t.Context(), first.ID, "BRANCH_ORIGINAL_INTENT"); err != nil {
		t.Fatal(err)
	}
	sourceEvents, err := st.ListRunEvents(t.Context(), first.ID)
	if err != nil {
		t.Fatal(err)
	}
	anchor := ""
	for _, event := range sourceEvents {
		var p struct{ Role, Content string }
		_ = json.Unmarshal([]byte(event.PayloadJSON), &p)
		if event.Type == events.SessionMessageEvent && p.Role == "assistant" && p.Content == "selected answer" {
			anchor = event.EventID
		}
	}
	if anchor == "" {
		t.Fatal("source assistant event missing")
	}
	if _, err := agent.StepWithInput(t.Context(), first.ID, "later source input"); err != nil {
		t.Fatal(err)
	}
	branch, err := application.NewThreadBranchService(st).Branch(t.Context(), domain.ThreadBranchRequest{Version: domain.ThreadBranchProtocolVersion, ThreadID: domain.InitialThreadID(first.ID), SourceEventID: anchor, OperationKey: "kimi-selected-branch", RequestedBy: "test_operator"})
	if err != nil {
		t.Fatal(err)
	}
	if calls != 2 {
		t.Fatal("branch creation called the provider")
	}
	if _, err := application.NewRunService(st).Start(t.Context(), branch.Run.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := agent.StepWithInput(t.Context(), branch.Run.ID, "Continue the selected answer"); err != nil {
		t.Fatal("Kimi rejected imported historical context", err)
	}
	if _, err := agent.StepWithInput(t.Context(), branch.Run.ID, "Continue again"); err != nil {
		t.Fatal("Kimi rejected its fresh branch native history", err)
	}
	if calls != 4 {
		t.Fatalf("model calls=%d", calls)
	}
}
