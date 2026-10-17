package application_test

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"path/filepath"
	"sync/atomic"
	"testing"

	"cyberagent-workbench/internal/application"
	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/llm"
	"cyberagent-workbench/internal/policy"
	"cyberagent-workbench/internal/session"
	"cyberagent-workbench/internal/store"
)

func kimiCompactionFixture(t *testing.T, rejectAt int) (*llm.Router, *atomic.Int32) {
	t.Helper()
	requests := &atomic.Int32{}
	router := kimiSupervisorFixtureRouter(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request struct {
			Messages []map[string]json.RawMessage `json:"messages"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Error(err)
		}
		count := int(requests.Add(1))
		if count == rejectAt {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"error":{"type":"invalid_request_error","code":"context_length_exceeded","message":"maximum context length exceeded"}}`))
			return
		}
		assistants := 0
		for _, message := range request.Messages {
			if string(message["role"]) != `"assistant"` {
				continue
			}
			assistants++
			if string(message["reasoning_content"]) != `"r"` {
				t.Error("native reasoning was dropped")
			}
		}
		if assistants != count-1 {
			t.Errorf("native history: got %d assistants, want %d", assistants, count-1)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		body, _ := json.Marshal(map[string]any{
			"id": fmt.Sprintf("native-%d", count), "model": "kimi-k3",
			"choices": []any{map[string]any{"index": 0,
				"delta": map[string]any{"role": "assistant", "reasoning_content": "r",
					"content": rootActionResponse(domain.RootActionFinish, "Recorded.", "reply completed", "")},
				"finish_reason": "stop"}},
			"usage": map[string]int{"prompt_tokens": 2, "completion_tokens": 3, "total_tokens": 5},
		})
		fmt.Fprintf(w, "data: %s\n\ndata: [DONE]\n\n", body)
	}))
	if err := router.SetContextWindow(llm.ModelRef{Provider: "kimi-root", Model: "model"}, llm.ContextWindow{
		ProtocolVersion: llm.ContextWindowProtocolVersion, WindowTokens: 131072,
		SafetyMarginTokens: 128, DefaultOutputTokens: 512, MaxOutputTokens: 512, Source: "fixture_window",
	}); err != nil {
		t.Fatal(err)
	}
	return router, requests
}

func TestSupervisorKimiShortHistorySurvivesCountThresholdAndReopen(t *testing.T) {
	router, requests := kimiCompactionFixture(t, 0)
	dbPath := filepath.Join(t.TempDir(), "kimi-count.db")
	st, err := store.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.Close() }()
	_, run, err := application.NewRunService(st).Create(t.Context(), application.CreateRunRequest{
		Goal: "Review the code.", Profile: "review", ModelRoute: "kimi-root/model",
		Interactive: true, Budget: domain.Budget{MaxTurns: 30},
	})
	if err != nil {
		t.Fatal(err)
	}
	makeTurns := func() *application.ThreadTurnService {
		return application.NewThreadTurnService(st, application.NewRunLifecycleControlService(st),
			application.NewRunExecutionHandoffService(st, router, policy.NewDefaultChecker()))
	}
	turns := makeTurns()
	for turn := 1; turn <= 13; turn++ {
		if turn == 13 {
			if err := st.Close(); err != nil {
				t.Fatal(err)
			}
			st, err = store.Open(dbPath)
			if err != nil {
				t.Fatal(err)
			}
			turns = makeTurns()
		}
		_, err := turns.Execute(t.Context(), application.ExecuteThreadTurnRequest{
			Version: domain.ThreadMessageProtocolVersion, ThreadID: domain.InitialThreadID(run.ID), Content: "next",
			OperationKey: fmt.Sprintf("native-kimi-turn-%02d", turn), RequestedBy: "operator",
		})
		if err != nil {
			t.Fatalf("turn %d: %v", turn, err)
		}
	}
	if requests.Load() != 13 {
		t.Fatalf("provider requests=%d", requests.Load())
	}
	if _, found, err := st.LatestContextSummary(t.Context(), run.SessionID); err != nil || found {
		t.Fatalf("short native history should not require a summary: found=%v err=%v", found, err)
	}
}

func TestSupervisorKimiProviderContextLimitIsSealedWithoutLossyReplay(t *testing.T) {
	router, requests := kimiCompactionFixture(t, 2)
	st, err := store.Open(filepath.Join(t.TempDir(), "kimi-overflow.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	_, run, err := application.NewRunService(st).Create(t.Context(), application.CreateRunRequest{
		Goal: "Review the code.", Profile: "review", ModelRoute: "kimi-root/model",
		Interactive: true, Budget: domain.Budget{MaxTurns: 10},
	})
	if err != nil {
		t.Fatal(err)
	}
	turns := application.NewThreadTurnService(st, application.NewRunLifecycleControlService(st),
		application.NewRunExecutionHandoffService(st, router, policy.NewDefaultChecker()))
	request := application.ExecuteThreadTurnRequest{Version: domain.ThreadMessageProtocolVersion,
		ThreadID: domain.InitialThreadID(run.ID), Content: "next", OperationKey: "kimi-overflow-first-turn", RequestedBy: "operator"}
	if _, err := turns.Execute(t.Context(), request); err != nil {
		t.Fatal(err)
	}
	request.OperationKey = "kimi-overflow-second-turn"
	_, err = turns.Execute(t.Context(), request)
	var failed *application.ThreadTurnFailedError
	if !errors.As(err, &failed) || failed.Failure == nil || failed.Failure.FailureStage != domain.ThreadFailureContextWindowExceeded {
		t.Fatalf("provider overflow lost actionable failure classification: %v", err)
	}
	replay, err := turns.Execute(t.Context(), request)
	if err == nil || !replay.Replayed || requests.Load() != 2 {
		t.Fatalf("overflow retried the model: calls=%d err=%v", requests.Load(), err)
	}
	if _, found, err := st.LatestContextSummary(t.Context(), run.SessionID); err != nil || found {
		t.Fatalf("native history was summarized after provider refusal: %v", err)
	}
	history, err := st.ListSessionMessages(t.Context(), run.SessionID, false)
	if err != nil {
		t.Fatal(err)
	}
	users, assistants := 0, 0
	for _, message := range history {
		if message.Provenance.SourceKind == session.SourceOperatorMessage {
			users++
		}
		if message.Provenance.SourceKind == session.SourceModelResponse {
			assistants++
		}
		if message.Compacted {
			t.Fatal("private provider history was compacted")
		}
	}
	if users != 2 || assistants != 1 {
		t.Fatalf("accepted input or native history was lost: users=%d assistants=%d", users, assistants)
	}
}
