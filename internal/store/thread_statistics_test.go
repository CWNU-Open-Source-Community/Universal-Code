package store

import (
	"fmt"
	"testing"
	"time"

	"cyberagent-workbench/internal/application"
	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/events"
	"cyberagent-workbench/internal/llm"
	"cyberagent-workbench/internal/policy"
)

func TestThreadStatisticsAggregatesRootAcrossRunsAndExcludesRetriesAndAuxiliaries(t *testing.T) {
	st, old := newMonetaryTestStore(t)
	ctx := t.Context()
	old, err := application.NewRunService(st).Cancel(ctx, old.ID)
	if err != nil {
		t.Fatal(err)
	}
	tx, next, _ := newThreadPlanCandidateTx(t, ctx, st, old)
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	threadID := domain.InitialThreadID(old.ID)
	appendEvent := func(run domain.Run, kind, source, subject string, payload map[string]any) {
		t.Helper()
		event, err := events.New(run.ID, run.MissionID, kind, source, subject, payload)
		if err != nil {
			t.Fatal(err)
		}
		tx, err := st.db.BeginTx(ctx, nil)
		if err != nil {
			t.Fatal(err)
		}
		defer tx.Rollback()
		if _, err := insertRunEventTx(ctx, tx, event); err != nil {
			t.Fatal(err)
		}
		if err := tx.Commit(); err != nil {
			t.Fatal(err)
		}
	}
	appendEvent(old, events.ModelFailedEvent, "model_gateway", "retry-1", map[string]any{"attempt_id": "a", "turn": 1, "tool_round": 0, "model_attempt": 1})
	appendEvent(old, events.ModelCompletedEvent, "model_gateway", "retry-2", map[string]any{"attempt_id": "a", "turn": 1, "tool_round": 0, "model_attempt": 2, "decode_millis": 200,
		"usage": llm.Usage{InputTokens: 100, OutputTokens: 20, TotalTokens: 120, CacheReadTokens: 80, CacheUsageReported: true}})
	appendEvent(old, events.ModelCompletedEvent, "model_gateway", "tools", map[string]any{"attempt_id": "a", "turn": 1, "tool_round": 1,
		"usage": llm.Usage{InputTokens: 50, OutputTokens: 10, TotalTokens: 60}})
	appendEvent(old, events.ModelCompletedEvent, "model_gateway", "repair", map[string]any{"attempt_id": "a", "turn": 1, "tool_round": 1, "protocol_repair": 1,
		"usage": llm.Usage{InputTokens: 5, OutputTokens: 2, TotalTokens: 7}})
	for _, excluded := range []struct{ source, purpose, dispatch string }{
		{"specialist_model_gateway", "", ""}, {"model_gateway", llm.ModelPurposeContextCompaction, ""}, {"model_gateway", "", "not_sent"},
	} {
		appendEvent(old, events.ModelFailedEvent, excluded.source, "excluded-"+excluded.source+excluded.purpose+excluded.dispatch,
			map[string]any{"purpose": excluded.purpose, "dispatch": excluded.dispatch, "usage": llm.Usage{InputTokens: 1000, OutputTokens: 1000, TotalTokens: 2000}})
	}
	appendEvent(next, events.ModelStartedEvent, "model_gateway", "latest", map[string]any{"attempt_id": "a", "turn": 1,
		"input_estimate": 64000, "context_window_tokens": 1000000, "context_window_source": "provider_override"})
	appendEvent(next, events.ModelCompletedEvent, "model_gateway", "latest", map[string]any{"attempt_id": "a", "turn": 1, "decode_millis": 300,
		"usage": llm.Usage{InputTokens: 30, OutputTokens: 6, TotalTokens: 36, CacheReadTokens: 20, CacheUsageReported: true}})
	if _, err := st.EnqueueOperatorSteering(ctx, domain.EnqueueOperatorSteeringRequest{RunID: next.ID, SessionID: next.SessionID,
		Content: "next user turn", OperationKey: "statistics-user-turn-0001", RequestedBy: "operator"}); err != nil {
		t.Fatal(err)
	}
	var changesBefore, changesAfter int64
	if err := st.db.QueryRowContext(ctx, "SELECT total_changes()").Scan(&changesBefore); err != nil {
		t.Fatal(err)
	}
	if _, err := st.db.ExecContext(ctx, "PRAGMA query_only=ON"); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		stats, err := st.GetThreadStatistics(ctx, threadID)
		if err != nil {
			t.Fatal(err)
		}
		if stats.Turns != 1 || stats.Steps != 3 || stats.InputTokens != 185 || stats.OutputTokens != 38 || stats.TotalTokens != 223 || stats.CacheReadTokens != 100 ||
			stats.UsageCalls != 4 || stats.CacheReportedCalls != 2 || stats.TimedOutputTokens != 26 || stats.DecodeMillis != 500 {
			t.Fatalf("wrong root cumulative statistics: %+v", stats)
		}
		if stats.Context == nil || stats.Context.InputTokens != 30 || stats.Context.WindowTokens != 1000000 || stats.Context.Estimated || stats.Context.WindowSource != "provider_override" {
			t.Fatalf("wrong saved request window: %+v", stats.Context)
		}
	}
	if err := st.db.QueryRowContext(ctx, "SELECT total_changes()").Scan(&changesAfter); err != nil || changesAfter != changesBefore {
		t.Fatalf("statistics query wrote state: before=%d after=%d err=%v", changesBefore, changesAfter, err)
	}
	if _, err := st.db.ExecContext(ctx, "PRAGMA query_only=OFF"); err != nil {
		t.Fatal(err)
	}
	// More terminal records than the transcript page size still contribute.
	for i := 0; i < 105; i++ {
		appendEvent(next, events.ModelCompletedEvent, "model_gateway", fmt.Sprintf("history-%d", i), map[string]any{"attempt_id": fmt.Sprintf("history-%d", i), "usage": llm.Usage{InputTokens: 1, TotalTokens: 1}})
	}
	stats, err := st.GetThreadStatistics(ctx, threadID)
	if err != nil || stats.Steps != 108 || stats.TotalTokens != 328 || stats.DecodeMillis != 500 {
		t.Fatalf("history was bounded or untimed history inferred: %+v %v", stats, err)
	}
}

func TestThreadStatisticsCountsCommittedSteeringOnceAndPersistsRequestTiming(t *testing.T) {
	st := openWorkItemTestStore(t)
	ctx := t.Context()
	_, created := createWorkItemTestRun(t, ctx, st, "statistics committed input")
	run, err := application.NewRunService(st).Start(ctx, created.ID)
	if err != nil {
		t.Fatal(err)
	}
	request := domain.EnqueueOperatorSteeringRequest{RunID: run.ID, SessionID: run.SessionID, Content: "one input", OperationKey: "statistics-commit-input-0001", RequestedBy: "operator"}
	for range 2 {
		if _, err := st.EnqueueOperatorSteering(ctx, request); err != nil {
			t.Fatal(err)
		}
	}
	turn, err := st.BeginSupervisorTurn(ctx, acquireTestRunExecutionLease(t, ctx, st, run.ID), "")
	if err != nil {
		t.Fatal(err)
	}
	decode := int64(25)
	attempt := llm.ModelAttempt{Number: 1, MaxAttempts: 1, Provider: "test", Model: "model", InputEstimate: 200, ContextWindowTokens: 32768, ContextWindowSource: "fallback"}
	if _, err := st.RecordSupervisorModelStarted(ctx, turn.Checkpoint, attempt); err != nil {
		t.Fatal(err)
	}
	stats, err := st.GetThreadStatistics(ctx, domain.InitialThreadID(run.ID))
	if err != nil || stats.Context == nil || stats.Context.InputTokens != 200 || !stats.Context.Estimated {
		t.Fatalf("running request lost its estimate: %+v %v", stats.Context, err)
	}
	attempt.Outcome, attempt.Elapsed, attempt.DecodeMillis = llm.OutcomeSuccess, time.Second, &decode
	response := llm.ChatResponse{Text: "working", Usage: llm.Usage{InputTokens: 10, OutputTokens: 5, TotalTokens: 15}, Provider: "test", Model: "model"}
	checkpoint, err := st.RecordSupervisorModelCompleted(ctx, turn.Checkpoint, attempt, response)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := st.CompleteSupervisorTurn(ctx, checkpoint, response, domain.RootAction{Version: domain.RootLifecycleVersion, Kind: domain.RootActionContinue, Message: response.Text}, policy.Decision{Allowed: true}, 0); err != nil {
		t.Fatal(err)
	}
	stats, err = st.GetThreadStatistics(ctx, domain.InitialThreadID(run.ID))
	if err != nil || stats.Turns != 1 || stats.Steps != 1 || stats.TimedOutputTokens != 5 || stats.DecodeMillis != 25 || stats.Context == nil || stats.Context.WindowTokens != 32768 || stats.Context.InputTokens != 10 || stats.Context.Estimated {
		t.Fatalf("committed input duplicated or timing not persisted: %+v %v", stats, err)
	}
}

func TestThreadStatisticsContextTracksLatestCallAcrossRetriesAndRuns(t *testing.T) {
	st, old := newMonetaryTestStore(t)
	ctx := t.Context()
	old, err := application.NewRunService(st).Cancel(ctx, old.ID)
	if err != nil {
		t.Fatal(err)
	}
	tx, next, _ := newThreadPlanCandidateTx(t, ctx, st, old)
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	start := func(run domain.Run, subject string, estimate, window int) {
		t.Helper()
		threadStatisticsReceipt(t, st, run, events.ModelStartedEvent, "model_gateway", subject,
			map[string]any{"input_estimate": estimate, "context_window_tokens": window, "context_window_source": "fallback"})
	}
	complete := func(run domain.Run, subject string, input int) {
		t.Helper()
		threadStatisticsReceipt(t, st, run, events.ModelCompletedEvent, "model_gateway", subject,
			map[string]any{"usage": llm.Usage{InputTokens: input, OutputTokens: 20, TotalTokens: input + 20}})
	}
	check := func(input int64, estimated bool) {
		t.Helper()
		stats, err := st.GetThreadStatistics(ctx, domain.InitialThreadID(old.ID))
		if err != nil || stats.Context == nil || stats.Context.InputTokens != input || stats.Context.Estimated != estimated || stats.Context.WindowTokens != 32768 {
			t.Fatalf("wrong latest request context: %+v %v", stats.Context, err)
		}
	}
	start(old, "attempt/model/1", 700, 32768)
	complete(old, "attempt/model/1", 100)
	check(100, false)
	// A subject can recur in a different Run; its old receipt cannot fill the
	// new request. Run ordinal, rather than arrival time, defines chat order.
	start(next, "attempt/model/1", 800, 32768)
	check(800, true)
	complete(old, "old-late-call", 150)
	check(800, true)
	// The next transport attempt replaces the estimate. A completion from the
	// previous call that arrives afterward must not replace that newer value.
	start(next, "attempt/model/2", 900, 32768)
	complete(next, "attempt/model/1", 200)
	check(900, true)
	threadStatisticsReceipt(t, st, next, events.ModelCompletedEvent, "model_gateway", "attempt/model/2",
		map[string]any{"usage": llm.Usage{InputTokens: 600, OutputTokens: 20, TotalTokens: 620,
			CacheReadTokens: 400, CacheCreationTokens: 100, CacheUsageReported: true}})
	check(600, false)
	for _, excluded := range []struct{ source, purpose, dispatch string }{
		{"specialist_model_gateway", "", ""}, {"model_gateway", llm.ModelPurposeContextCompaction, ""}, {"model_gateway", "", "not_sent"},
	} {
		for _, kind := range []string{events.ModelStartedEvent, events.ModelCompletedEvent} {
			threadStatisticsReceipt(t, st, next, kind, excluded.source, "excluded-"+excluded.source+excluded.purpose+excluded.dispatch,
				map[string]any{"purpose": excluded.purpose, "dispatch": excluded.dispatch, "input_estimate": 5000, "context_window_tokens": 32768,
					"usage": llm.Usage{InputTokens: 4000, TotalTokens: 4000}})
		}
		check(600, false)
	}
	// An older server's next request may lack window metadata. Do not attach
	// its actual usage to the previous request's window.
	start(next, "attempt/model/3", 1000, 0)
	complete(next, "attempt/model/3", 650)
	stats, err := st.GetThreadStatistics(ctx, domain.InitialThreadID(old.ID))
	if err != nil || stats.Context != nil {
		t.Fatalf("reused an older request window: %+v %v", stats.Context, err)
	}
}

func TestThreadStatisticsContextKeepsEstimateWithoutReliableCompletedInput(t *testing.T) {
	for _, test := range []struct {
		name    string
		kind    string
		payload map[string]any
	}{
		{"missing", events.ModelCompletedEvent, map[string]any{}},
		{"zero_unknown", events.ModelCompletedEvent, map[string]any{"usage": llm.Usage{}}},
		{"output_only", events.ModelCompletedEvent, map[string]any{"usage": llm.Usage{OutputTokens: 20, TotalTokens: 20}}},
		{"total_only", events.ModelCompletedEvent, map[string]any{"usage": llm.Usage{InputTokens: 100, TotalTokens: 200}}},
		{"declared_unknown", events.ModelCompletedEvent, map[string]any{"usage": llm.Usage{InputTokens: 100, TotalTokens: 100}, "usage_unknown": true}},
		{"failed_with_usage", events.ModelFailedEvent, map[string]any{"usage": llm.Usage{InputTokens: 100, TotalTokens: 100}, "accounting_only": true}},
	} {
		t.Run(test.name, func(t *testing.T) {
			st, run := newMonetaryTestStore(t)
			threadStatisticsReceipt(t, st, run, events.ModelStartedEvent, "model_gateway", "request",
				map[string]any{"input_estimate": 700, "context_window_tokens": 32768})
			threadStatisticsReceipt(t, st, run, test.kind, "model_gateway", "request", test.payload)
			stats, err := st.GetThreadStatistics(t.Context(), domain.InitialThreadID(run.ID))
			if err != nil || stats.Context == nil || stats.Context.InputTokens != 700 || !stats.Context.Estimated {
				t.Fatalf("unreliable input replaced the estimate: %+v %v", stats.Context, err)
			}
		})
	}
}

func threadStatisticsReceipt(t *testing.T, st *SQLiteStore, run domain.Run, kind, source, subject string, payload map[string]any) {
	t.Helper()
	event, err := events.New(run.ID, run.MissionID, kind, source, subject, payload)
	if err != nil {
		t.Fatal(err)
	}
	tx, err := st.db.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if _, err := insertRunEventTx(t.Context(), tx, event); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
}
