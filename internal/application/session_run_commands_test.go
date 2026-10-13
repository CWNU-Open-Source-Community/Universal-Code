package application_test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"

	"cyberagent-workbench/internal/apperror"
	"cyberagent-workbench/internal/application"
	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/llm"
	"cyberagent-workbench/internal/policy"
	"cyberagent-workbench/internal/session"
	"cyberagent-workbench/internal/store"
)

func TestRunBoundSessionCompactPreservesKimiHistory(t *testing.T) {
	for _, withHistory := range []bool{false, true} {
		t.Run(fmt.Sprintf("existing_history_%t", withHistory), func(t *testing.T) {
			var calls atomic.Int32
			router := kimiSupervisorFixtureRouter(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				count := calls.Add(1)
				w.Header().Set("Content-Type", "text/event-stream")
				event, err := json.Marshal(map[string]any{
					"id": fmt.Sprintf("session-kimi-%d", count), "model": "kimi-k3",
					"choices": []any{map[string]any{"index": 0, "finish_reason": "stop", "delta": map[string]any{
						"role": "assistant", "reasoning_content": "private session reasoning",
						"content": rootActionResponse(domain.RootActionContinue, "Conversation is usable", "", ""),
					}}},
					"usage": map[string]int{"prompt_tokens": 2, "completion_tokens": 3, "total_tokens": 5},
				})
				if err != nil {
					t.Error(err)
					return
				}
				_, _ = fmt.Fprintf(w, "data: %s\n\ndata: [DONE]\n\n", event)
			}))
			st, run, manager := sessionCommandFixture(t, router, "kimi-root/model")
			if withHistory {
				if _, err := manager.Send(t.Context(), run.SessionID, "Start the task"); err != nil {
					t.Fatal(err)
				}
			}
			// A stale sessions.route must not bypass the actual Run's requirement.
			sess, err := st.GetSession(t.Context(), run.SessionID)
			if err != nil {
				t.Fatal(err)
			}
			sess.Route = "mock/mock-code"
			if err := st.SaveSession(t.Context(), sess); err != nil {
				t.Fatal(err)
			}
			before, err := st.ListSessionMessages(t.Context(), run.SessionID, true)
			if err != nil {
				t.Fatal(err)
			}
			beforeCalls := calls.Load()
			if _, err := manager.Send(t.Context(), run.SessionID, "/compact"); apperror.CodeOf(err) != apperror.CodeFailedPrecondition || !strings.Contains(err.Error(), "private assistant history") {
				t.Fatalf("private-history compaction was not refused: %v", err)
			}
			after, err := st.ListSessionMessages(t.Context(), run.SessionID, true)
			if err != nil || !reflect.DeepEqual(before, after) || calls.Load() != beforeCalls {
				t.Fatalf("refused compaction changed history or called the model: before=%v after=%v err=%v", before, after, err)
			}
			if _, found, err := st.LatestContextSummary(t.Context(), run.SessionID); err != nil || found {
				t.Fatalf("refused compaction persisted a summary: found=%t err=%v", found, err)
			}
			result, err := manager.Send(t.Context(), run.SessionID, "Continue the task")
			if err != nil || result.Text != "Conversation is usable" || calls.Load() != beforeCalls+1 {
				t.Fatalf("refused compaction damaged the next turn: result=%+v err=%v", result, err)
			}
		})
	}
}

func TestRunBoundSessionModelRejectsMutationAndReportsExecutionRoute(t *testing.T) {
	provider := &lifecycleProvider{responses: []string{rootActionResponse(domain.RootActionContinue, "Actual model replied", "", "")}}
	router := llm.NewRouter(llm.ModelRef{Provider: provider.Name(), Model: "model"})
	router.RegisterProvider(provider)
	st, run, manager := sessionCommandFixture(t, router, "lifecycle-test/model")
	sess, err := st.GetSession(t.Context(), run.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	sess.Route = "lifecycle-test/model-next"
	if err := st.SaveSession(t.Context(), sess); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Send(t.Context(), run.SessionID, "/model lifecycle-test/another-model"); apperror.CodeOf(err) != apperror.CodeFailedPrecondition || !strings.Contains(err.Error(), "Thread model control") {
		t.Fatalf("Run-bound model mutation was not refused: %v", err)
	}
	storedSession, err := st.GetSession(t.Context(), run.SessionID)
	if err != nil || storedSession.Route != sess.Route {
		t.Fatalf("rejected model command changed the Session: %+v %v", storedSession, err)
	}
	history, err := st.ListSessionMessages(t.Context(), run.SessionID, true)
	if err != nil || len(history) != 0 {
		t.Fatalf("rejected model command was persisted: %v %v", history, err)
	}
	query, err := manager.Send(t.Context(), run.SessionID, "/model")
	if err != nil || query.Session.Route != run.Config.ModelRoute || !strings.Contains(query.Text, "Session route: lifecycle-test/model -> lifecycle-test/model") {
		t.Fatalf("model query did not report the actual Run route: %+v %v", query, err)
	}
	if _, err := manager.Send(t.Context(), run.SessionID, "Continue with the reported model"); err != nil {
		t.Fatal(err)
	}
	storedRun, err := st.GetRun(t.Context(), run.ID)
	if err != nil || storedRun.Config.ModelRoute != run.Config.ModelRoute || len(provider.requests) != 1 || provider.requests[0].Model != "model" {
		t.Fatalf("model query and executed route disagree: run=%+v requests=%+v err=%v", storedRun, provider.requests, err)
	}
}

func TestRunBoundSessionCompactAllowsPublicHistory(t *testing.T) {
	provider := &lifecycleProvider{responses: []string{rootActionResponse(domain.RootActionContinue, "Continued with summary", "", "")}}
	router := llm.NewRouter(llm.ModelRef{Provider: provider.Name(), Model: "model"})
	router.RegisterProvider(provider)
	_, run, manager := sessionCommandFixture(t, router, "lifecycle-test/model")
	result, err := manager.Send(t.Context(), run.SessionID, "/compact")
	if err != nil || !result.Compacted || result.SummaryID == 0 {
		t.Fatalf("public-history compaction failed: %+v %v", result, err)
	}
	result, err = manager.Send(t.Context(), run.SessionID, "Continue the task")
	if err != nil || result.Text != "Continued with summary" {
		t.Fatalf("public-history Session could not continue: %+v %v", result, err)
	}
}

func TestUnboundSessionModelStillSwitchesWithRunExecutor(t *testing.T) {
	st, _, manager := sessionCommandFixture(t, llm.NewDefaultRouter(), "mock/mock-cyber-agent")
	sess, err := manager.Create(t.Context(), "", "Legacy route switch", "mock/mock-cyber-agent")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Send(t.Context(), sess.ID, "/model mock/mock-code"); err != nil {
		t.Fatal(err)
	}
	result, err := manager.Send(t.Context(), sess.ID, "Which model is selected?")
	if err != nil || !strings.Contains(result.Text, "[mock-code]") {
		t.Fatalf("unbound Session lost model switching: %+v %v", result, err)
	}
	stored, err := st.GetSession(t.Context(), sess.ID)
	if err != nil || stored.Route != "mock/mock-code" {
		t.Fatalf("unbound Session route was not saved: %+v %v", stored, err)
	}
}

func sessionCommandFixture(t *testing.T, router *llm.Router, route string) (*store.SQLiteStore, domain.Run, *session.Manager) {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "session-commands.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	_, run, err := application.NewRunService(st).Create(t.Context(), application.CreateRunRequest{
		Goal: "Keep Session commands consistent with execution", Profile: "review", ModelRoute: route,
		Interactive: true, Budget: domain.Budget{MaxTurns: 4},
	})
	if err != nil {
		t.Fatal(err)
	}
	manager := session.NewManager(st, router, policy.NewDefaultChecker()).WithRunChatExecutor(
		application.NewSessionRunChatExecutor(st, router, policy.NewDefaultChecker()))
	return st, run, manager
}
