package application_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"reflect"
	"sync/atomic"
	"testing"
	"time"

	"cyberagent-workbench/internal/apperror"
	"cyberagent-workbench/internal/application"
	"cyberagent-workbench/internal/coordinator"
	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/events"
	"cyberagent-workbench/internal/llm"
	"cyberagent-workbench/internal/policy"
	"cyberagent-workbench/internal/pricing"
	"cyberagent-workbench/internal/store"
)

func newHTTPMoneySpecialist(t *testing.T, provider llm.Provider, capUSD float64) (string, *store.SQLiteStore, domain.Run, domain.AgentNode, *application.SubagentRunner, *llm.Router) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "specialist-money.db")
	st, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	run, _, router := newQualifiedHTTPProviderSupervisor(t, st, provider, domain.Budget{MaxTurns: 10, MaxCostUSD: capUSD})
	importSupervisorPriceSnapshot(t, t.Context(), st)
	window := llm.DefaultContextWindow()
	window.DefaultOutputTokens, window.MaxOutputTokens = 256, 512
	if err := router.SetContextWindow(llm.ModelRef{Provider: provider.Name(), Model: "model"}, window); err != nil {
		t.Fatal(err)
	}
	root, found, err := st.GetRootAgent(t.Context(), run.ID)
	if err != nil || !found {
		t.Fatalf("missing root: %t %v", found, err)
	}
	coord, err := coordinator.NewWithSpecialistAdmission(st, coordinator.SpecialistAdmissionPolicy{
		MaxChildren: 2, MaxTurnsPerChild: 3, MaxTokensPerChild: 256,
	})
	if err != nil {
		t.Fatal(err)
	}
	admitted, err := coord.AdmitSpecialist(t.Context(), coordinator.AdmitSpecialistRequest{
		RunID: run.ID, ParentAgentID: root.ID, Title: "bounded HTTP Specialist", Skills: []string{"model.chat"},
		TurnLimit: 3, TokenLimit: 256, IdempotencyKey: "http-specialist-admit-0001",
	})
	if err != nil {
		t.Fatal(err)
	}
	runner := application.NewSubagentRunner(st, router, policy.NewDefaultChecker()).
		WithMonetaryBudget(application.NewMonetaryBudgetService(st)).
		WithModelRetryPolicy(application.ModelRetryPolicy{MaxAttempts: 2})
	return path, st, run, admitted.Agent, runner, router
}

func TestSpecialistHTTPTimeoutRetainsUnknownCostBeforeRetry(t *testing.T) {
	runCase := func(t *testing.T, capUSD float64) int64 {
		t.Helper()
		var calls atomic.Int64
		var st *store.SQLiteStore
		var run domain.Run
		firstMoney := make(chan domain.MonetaryUsage, 1)
		release := make(chan struct{})
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			_, _ = io.Copy(io.Discard, r.Body)
			if calls.Add(1) == 1 {
				money, err := st.GetMonetaryUsage(t.Context(), run.ID)
				if err != nil {
					t.Error(err)
				}
				firstMoney <- money
				select {
				case <-r.Context().Done():
				case <-release:
				}
				return
			}
			action, _ := json.Marshal(domain.SpecialistAction{Version: domain.SpecialistLifecycleVersion,
				Kind: domain.SpecialistActionContinue, Message: "bounded Specialist recovered"})
			w.Header().Set("Content-Type", "text/event-stream")
			boundaryAnthropicText(w, string(action))
			boundaryAnthropicEvent(w, map[string]any{"type": "message_delta", "delta": map[string]any{"stop_reason": "end_turn"},
				"usage": map[string]any{"output_tokens": 3}})
			boundaryAnthropicEvent(w, map[string]any{"type": "message_stop"})
			w.(http.Flusher).Flush()
		}))
		t.Cleanup(server.Close)
		t.Cleanup(func() { close(release) })
		provider := boundaryHTTPAnthropic(t, server.URL, 400*time.Millisecond)
		var path string
		var child domain.AgentNode
		var runner *application.SubagentRunner
		path, st, run, child, runner, _ = newHTTPMoneySpecialist(t, provider, capUSD)
		result, err := runner.Step(t.Context(), run.ID, child.ID)
		if calls.Load() == 0 {
			t.Fatalf("fixture cap did not allow its first request: %+v err=%v", result, err)
		}
		initial := <-firstMoney
		t.Logf("first_reservation=%d cap=%d actual_http_calls=%d", initial.ReservedMicros, initial.CapMicros, calls.Load())
		money := readOrdinaryMoneyUsage(t, st, run.ID)
		if capUSD == 1 {
			if err != nil || calls.Load() != 2 || money.SettledMicros != initial.ReservedMicros+8 {
				t.Fatalf("unknown sent Specialist request was released to finance retry: initial=%+v final=%+v calls=%d err=%v", initial, money, calls.Load(), err)
			}
		} else if apperror.CodeOf(err) != apperror.CodeResourceExhausted || calls.Load() != 1 ||
			money.SettledMicros != initial.ReservedMicros || money.ReleasedMicros != 0 {
			t.Fatalf("one-reservation cap permitted another sent request: initial=%+v final=%+v calls=%d err=%v", initial, money, calls.Load(), err)
		}
		if err := st.Close(); err != nil {
			t.Fatal(err)
		}
		reopened, err := store.Open(path)
		if err != nil {
			t.Fatal(err)
		}
		defer reopened.Close()
		if recovered := readOrdinaryMoneyUsage(t, reopened, run.ID); !reflect.DeepEqual(recovered, money) {
			t.Fatalf("Specialist accounting changed on reopen: initial=%+v recovered=%+v", money, recovered)
		}
		return initial.ReservedMicros
	}
	// Measure the actual first reservation through the ample-budget HTTP case.
	// Run it outside the subtest so filtering to one_reservation still calibrates
	// against the current prompt and price snapshot instead of a fixed dollar cap.
	firstReservation := runCase(t, 1)
	t.Run("one_reservation", func(t *testing.T) {
		if reserved := runCase(t, float64(firstReservation)/float64(pricing.MicrosPerUSD)); reserved != firstReservation {
			t.Fatalf("restricted request reservation changed after calibration: actual=%d calibrated=%d", reserved, firstReservation)
		}
	})
}

func specialistHTTPResponse(t *testing.T, w http.ResponseWriter, text string) {
	t.Helper()
	w.Header().Set("Content-Type", "text/event-stream")
	boundaryAnthropicText(w, text)
	boundaryAnthropicEvent(w, map[string]any{"type": "message_delta", "delta": map[string]any{"stop_reason": "end_turn"},
		"usage": map[string]any{"output_tokens": 3}})
	boundaryAnthropicEvent(w, map[string]any{"type": "message_stop"})
	w.(http.Flusher).Flush()
}

func TestSpecialistHTTPMonetaryIdentitySeparatesChildrenAndTurns(t *testing.T) {
	var calls atomic.Int64
	action := specialistResponse(t, domain.SpecialistAction{Version: domain.SpecialistLifecycleVersion,
		Kind: domain.SpecialistActionContinue, Message: "source bound continuation"})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		calls.Add(1)
		specialistHTTPResponse(t, w, action)
	}))
	t.Cleanup(server.Close)
	_, st, run, child, runner, _ := newHTTPMoneySpecialist(t, boundaryHTTPAnthropic(t, server.URL, time.Second), 1)
	coord, err := coordinator.NewWithSpecialistAdmission(st, coordinator.SpecialistAdmissionPolicy{
		MaxChildren: 2, MaxTurnsPerChild: 3, MaxTokensPerChild: 256,
	})
	if err != nil {
		t.Fatal(err)
	}
	other, err := coord.AdmitSpecialist(t.Context(), coordinator.AdmitSpecialistRequest{
		RunID: run.ID, ParentAgentID: child.ParentID, Title: "second bounded child", Skills: []string{"model.chat"},
		TurnLimit: 3, TokenLimit: 256, IdempotencyKey: "http-specialist-admit-0002",
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{child.ID, other.Agent.ID, child.ID} {
		result, err := runner.Step(t.Context(), run.ID, id)
		if err != nil || result.ModelAttempts != 1 || result.Usage.TotalTokens != 5 {
			t.Fatalf("bounded child turn failed: %+v %v", result, err)
		}
	}
	timeline, err := st.ListRunEvents(t.Context(), run.ID)
	if err != nil {
		t.Fatal(err)
	}
	keys := map[int64]bool{}
	for _, event := range timeline {
		if event.Type != events.ModelStartedEvent || event.Source != "specialist_model_gateway" {
			continue
		}
		var payload struct {
			Number int   `json:"model_attempt"`
			Key    int64 `json:"monetary_attempt_number"`
		}
		if err := json.Unmarshal([]byte(event.PayloadJSON), &payload); err != nil {
			t.Fatal(err)
		}
		if payload.Number != 1 || payload.Key < 1<<61 || keys[payload.Key] {
			t.Fatalf("another child or turn reused a reservation: %+v", payload)
		}
		keys[payload.Key] = true
	}
	money := readOrdinaryMoneyUsage(t, st, run.ID)
	if calls.Load() != 3 || len(keys) != 3 || money.SettledMicros != 24 {
		t.Fatalf("three independently sent calls did not have three charges: calls=%d keys=%v money=%+v", calls.Load(), keys, money)
	}
}

func TestSpecialistHTTPProtocolRepairSettlesEachResponseOnce(t *testing.T) {
	var calls atomic.Int64
	action := specialistResponse(t, domain.SpecialistAction{Version: domain.SpecialistLifecycleVersion,
		Kind: domain.SpecialistActionContinue, Message: "continue after one repair"})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		if calls.Add(1) == 1 {
			specialistHTTPResponse(t, w, "not a lifecycle response")
			return
		}
		specialistHTTPResponse(t, w, action)
	}))
	t.Cleanup(server.Close)
	_, st, run, child, runner, _ := newHTTPMoneySpecialist(t, boundaryHTTPAnthropic(t, server.URL, time.Second), 1)
	result, err := runner.Step(t.Context(), run.ID, child.ID)
	money := readOrdinaryMoneyUsage(t, st, run.ID)
	if err != nil || result.ModelAttempts != 2 || result.ProtocolRepairs != 1 || result.Usage.TotalTokens != 10 ||
		calls.Load() != 2 || money.SettledMicros != 16 {
		t.Fatalf("protocol repair reused cumulative usage for one charge: result=%+v money=%+v calls=%d err=%v", result, money, calls.Load(), err)
	}
}

type specialistReserveDriftStore struct {
	*store.SQLiteStore
	drift func() error
}

func (s *specialistReserveDriftStore) ReserveModelCost(ctx context.Context, req domain.MonetaryReserveRequest) (domain.MonetaryUsage, bool, error) {
	money, replayed, err := s.SQLiteStore.ReserveModelCost(ctx, req)
	if err == nil && !replayed {
		err = s.drift()
	}
	return money, replayed, err
}

func TestSpecialistPreparedDriftReleasesOnlyProvenUnsentReservation(t *testing.T) {
	var calls atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusInternalServerError)
	}))
	t.Cleanup(server.Close)
	provider := boundaryHTTPAnthropic(t, server.URL, time.Second)
	path, st, run, child, runner, router := newHTTPMoneySpecialist(t, provider, 1)
	drifting := &specialistReserveDriftStore{SQLiteStore: st, drift: func() error {
		window := router.ContextWindow(llm.ModelRef{Provider: provider.Name(), Model: "model"})
		return router.SetContextWindow(llm.ModelRef{Provider: provider.Name(), Model: "model"}, window)
	}}
	runner.WithMonetaryBudget(application.NewMonetaryBudgetService(drifting))
	result, err := runner.Step(t.Context(), run.ID, child.ID)
	money := readOrdinaryMoneyUsage(t, st, run.ID)
	if err == nil || calls.Load() != 0 || result.ModelAttempts != 1 || money.ReservedMicros <= 0 ||
		money.SettledMicros != 0 || money.ReleasedMicros != money.ReservedMicros {
		t.Fatalf("prepared rejection sent a request or retained an unsent charge: result=%+v money=%+v calls=%d err=%v", result, money, calls.Load(), err)
	}
	timeline, listErr := st.ListRunEvents(t.Context(), run.ID)
	if listErr != nil {
		t.Fatal(listErr)
	}
	proofs := 0
	for _, event := range timeline {
		if event.Type == events.ModelFailedEvent && event.Source == "specialist_model_gateway" {
			var payload struct {
				Dispatch string `json:"dispatch"`
			}
			if err := json.Unmarshal([]byte(event.PayloadJSON), &payload); err != nil {
				t.Fatal(err)
			}
			if payload.Dispatch == "not_sent" {
				proofs++
			}
		}
	}
	if proofs != 1 {
		t.Fatalf("missing exact not-dispatched durable receipt: %d", proofs)
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	if recovered := readOrdinaryMoneyUsage(t, reopened, run.ID); !reflect.DeepEqual(recovered, money) {
		t.Fatalf("prepared rejection changed monetary state on reopen: %+v vs %+v", money, recovered)
	}
}
