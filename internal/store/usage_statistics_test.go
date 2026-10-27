package store

import (
	"database/sql"
	"errors"
	"fmt"
	"math"
	"strings"
	"testing"
	"time"

	"cyberagent-workbench/internal/apperror"
	"cyberagent-workbench/internal/application"
	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/events"
	"cyberagent-workbench/internal/llm"
)

func TestThreadUsageStatisticsAcrossRunsFiltersReceiptsAndCosts(t *testing.T) {
	st := openWorkItemTestStore(t)
	ctx := t.Context()
	runs := application.NewRunService(st)
	create := func(goal string) domain.Run {
		t.Helper()
		_, run, err := runs.Create(ctx, application.CreateRunRequest{Goal: goal, Profile: "review", Interactive: true, Budget: domain.Budget{MaxTurns: 10}})
		if err != nil {
			t.Fatal(err)
		}
		return run
	}
	first, other := create("usage A"), create("usage B")
	if _, err := runs.Start(ctx, first.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := runs.Fail(ctx, first.ID, "continue usage fixture"); err != nil {
		t.Fatal(err)
	}
	threadID := domain.InitialThreadID(first.ID)
	next, err := application.NewThreadService(st).Submit(ctx, application.SubmitThreadMessageRequest{
		Version: domain.ThreadMessageProtocolVersion, ThreadID: threadID, Content: "next usage Run",
		OperationKey: "usage-successor-0001", RequestedBy: "test_operator"})
	if err != nil {
		t.Fatal(err)
	}
	bindings, err := st.ListThreadRuns(ctx, threadID)
	if err != nil || len(bindings) != 2 || bindings[1].RunID != next.Run.ID || bindings[1].PredecessorRunID != first.ID {
		t.Fatalf("real Thread continuation did not bind both Runs: %+v %v", bindings, err)
	}
	now := time.Now().UTC()
	old := now.AddDate(0, 0, -40)
	const key = int64(1<<61 + 1)
	for _, check := range []struct {
		run                   domain.Run
		source, kind, subject string
		tokens                int
		at                    time.Time
	}{
		{first, "model_gateway", events.ModelCompletedEvent, "first", 12, old},
		{next.Run, "model_gateway", events.ModelFailedEvent, "next", 24, now},
		{next.Run, "specialist_model_gateway", events.ModelCompletedEvent, "child", 3, now},
		{next.Run, "thread_context_compaction", "context_compaction.model_completed", "compact", 4, now},
		{next.Run, "readonly_fanout", events.ReadOnlyFanoutShardCompletedEvent, "fanout", 5, now},
		{other, "model_gateway", events.ModelCompletedEvent, "other-root", 1000, now},
		{other, "specialist_model_gateway", events.ModelCompletedEvent, "other-child", 1000, now},
		{other, "thread_context_compaction", "context_compaction.model_completed", "other-compact", 1000, now},
		{other, "readonly_fanout", events.ReadOnlyFanoutShardCompletedEvent, "other-fanout", 1000, now},
	} {
		usageTestEvent(t, st, check.run, check.source, check.kind, check.subject, check.at, map[string]any{
			"provider": "p", "model": "m", "monetary_attempt_number": key, "execution_id": "fanout", "shard": 1, "attempt": 1,
			"usage": llm.Usage{InputTokens: check.tokens, TotalTokens: check.tokens, CacheReadTokens: 1, CacheUsageReported: true},
		})
	}
	usageTestReservation(t, st, first, "first-cost", domain.MonetaryScopeRoot, key, old, old, 10, domain.MonetaryPricingPriced)
	usageTestReservation(t, st, next.Run, "next-cost", domain.MonetaryScopeRoot, key, now, now, 900, domain.MonetaryPricingUsageUnknown)
	usageTestReservation(t, st, next.Run, "child-cost", domain.MonetaryScopeSpecialist, key, now, now, 20, domain.MonetaryPricingPriced)
	usageTestReservation(t, st, other, "other-cost", domain.MonetaryScopeRoot, key, now, now, 800, domain.MonetaryPricingPriced)
	usageTestReservation(t, st, other, "other-child-cost", domain.MonetaryScopeSpecialist, key, now, now, 900, domain.MonetaryPricingUsageUnknown)
	for _, check := range []struct {
		period                      string
		calls, tokens, cost, priced int64
	}{
		{"all", 5, 48, 30, 2}, {"30d", 4, 36, 20, 1},
	} {
		got, err := st.GetThreadUsageStatistics(ctx, threadID, check.period, 0, now)
		if err != nil || got.Totals.Calls != check.calls || got.Totals.TotalTokens != check.tokens || got.Totals.CacheReadTokens != check.calls ||
			got.Cost.KnownCostMicros != check.cost || got.Cost.PricedCalls != check.priced || got.Cost.UnpricedCalls != 1 || got.Cost.UntrackedCalls != 2 {
			t.Fatalf("scoped %s totals/cost changed or leaked: %+v %v", check.period, got, err)
		}
		if got.UnassignedCalls != 0 || len(got.Models) != 1 || got.Models[0].Usage != got.Totals ||
			len(got.RecentSessions) != 1 || got.RecentSessions[0].ThreadID != threadID || got.RecentSessions[0].Usage != got.Totals {
			t.Fatalf("scoped breakdown leaked another Thread: %+v", got)
		}
		var daily, activity int64
		for _, day := range got.Days {
			daily += day.Usage.TotalTokens
		}
		for _, day := range got.ActivityDays {
			activity += day.TotalTokens
		}
		if daily != check.tokens || activity != 48 {
			t.Fatalf("day or independent activity range leaked: days=%d activity=%d", daily, activity)
		}
	}
	global, err := st.GetUsageStatistics(ctx, "all", 0, now)
	if err != nil || global.Totals.TotalTokens != 4048 || global.Cost.KnownCostMicros != 830 || global.Cost.UnpricedCalls != 2 || len(global.RecentSessions) != 2 {
		t.Fatalf("original global call changed: %+v %v", global, err)
	}
	otherStats, err := st.GetThreadUsageStatistics(ctx, domain.InitialThreadID(other.ID), "all", 0, now)
	if err != nil || otherStats.Totals.TotalTokens != 4000 || otherStats.Cost.KnownCostMicros != 800 || otherStats.Cost.UnpricedCalls != 1 {
		t.Fatalf("other scope reused first Thread: %+v %v", otherStats, err)
	}
	thread, err := st.GetThread(ctx, threadID)
	if err != nil {
		t.Fatal(err)
	}
	archived, err := application.NewThreadService(st).Transition(ctx, threadID, domain.ThreadArchive, thread.Version, "test_operator", "usage-archive-0001")
	if err != nil {
		t.Fatal(err)
	}
	if got, err := st.GetThreadUsageStatistics(ctx, threadID, "all", 0, now); err != nil || got.Totals.TotalTokens != 48 {
		t.Fatalf("archived usage lost: %+v %v", got, err)
	}
	if _, err := application.NewThreadService(st).Transition(ctx, threadID, domain.ThreadDelete, archived.Version, "test_operator", "usage-delete-0001"); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{threadID, "missing-thread"} {
		if _, err := st.GetThreadUsageStatistics(ctx, id, "all", 0, now); !errors.Is(err, sql.ErrNoRows) {
			t.Fatalf("missing/deleted Thread became usage: %q %v", id, err)
		}
	}
	if _, err := st.GetThreadUsageStatistics(ctx, "", "all", 0, now); apperror.CodeOf(err) != apperror.CodeInvalidArgument {
		t.Fatalf("empty Thread became global usage: %v", err)
	}
}

func usageTestEvent(t *testing.T, st *SQLiteStore, run domain.Run, source, kind, subject string, at time.Time, payload map[string]any) {
	t.Helper()
	event, err := events.New(run.ID, run.MissionID, kind, source, subject, payload)
	if err != nil {
		t.Fatal(err)
	}
	event.CreatedAt = at
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

func TestUsageStatisticsCalendarWindowsSourcesAndCoverage(t *testing.T) {
	st, run := newMonetaryTestStore(t)
	now := time.Date(2026, 10, 19, 17, 30, 0, 0, time.UTC)
	boundary := time.Date(2026, 10, 19, 16, 0, 0, 0, time.UTC)
	known := func(input, output int) llm.Usage {
		return llm.Usage{InputTokens: input, OutputTokens: output, TotalTokens: input + output}
	}
	root := known(100, 20)
	root.CacheReadTokens, root.CacheCreationTokens, root.CacheUsageReported = 40, 20, true
	prior := known(10, 2)
	prior.CacheReadTokens, prior.CacheCreationTokens, prior.CacheUsageReported = 3, 2, true
	for _, r := range []struct {
		source, kind, subject string
		at                    time.Time
		payload               map[string]any
	}{
		{"model_gateway", events.ModelCompletedEvent, "prior", boundary.Add(-time.Second), map[string]any{"provider": "p", "model": "m", "usage": prior}},
		{"model_gateway", events.ModelStartedEvent, "root", boundary, map[string]any{"provider": "p", "model": "m"}},
		{"model_gateway", events.ModelCompletedEvent, "root", boundary, map[string]any{"provider": "p", "model": "m", "usage": root}},
		{"model_gateway", events.ModelCompletedEvent, "root", boundary.Add(time.Second), map[string]any{"provider": "p", "model": "m", "usage": root}},
		{"specialist_model_gateway", events.ModelFailedEvent, "child", boundary, map[string]any{"provider": "p", "model": "child", "usage": known(5, 1)}},
		{"thread_context_compaction", "context_compaction.model_failed", "compact", boundary, map[string]any{"provider": "p", "model": "m", "usage": known(7, 1)}},
		{"readonly_fanout", events.ReadOnlyFanoutShardCompletedEvent, "shard", boundary, map[string]any{"provider": "p", "model": "m", "execution_id": "fanout", "shard": 1, "attempt": 1, "usage": known(2, 1)}},
		{"readonly_fanout", events.ReadOnlyFanoutShardFailedEvent, "shard", boundary, map[string]any{"provider": "p", "model": "m", "execution_id": "fanout", "shard": 1, "attempt": 2, "usage": known(3, 1)}},
		{"readonly_fanout", events.ReadOnlyFanoutShardFailedEvent, "not-sent-shard", boundary, map[string]any{"attempt": 1, "error_code": "request_not_dispatched", "usage": llm.Usage{}}},
		{"model_gateway", events.ModelFailedEvent, "not-sent", boundary, map[string]any{"dispatch": "not_sent", "usage": llm.Usage{}}},
		{"model_gateway", events.ModelFailedEvent, "missing", boundary, map[string]any{"usage_unknown": true}},
		{"model_gateway", events.ModelCompletedEvent, "missing-model", boundary, map[string]any{"usage": known(1, 1)}},
		{"model_gateway", events.ModelCompletedEvent, "old", boundary.AddDate(0, 0, -30), map[string]any{"usage": known(7, 1)}},
		{"model_gateway", events.ModelCompletedEvent, "ancient", time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC), map[string]any{"usage": known(10, 1)}},
		{"model_gateway", events.ModelCompletedEvent, "future", now.Add(time.Hour), map[string]any{"usage": known(1000, 1000)}},
	} {
		usageTestEvent(t, st, run, r.source, r.kind, r.subject, r.at, r.payload)
	}
	// Historical unbound Runs are valid local history; keep them in totals
	// without inventing a Thread or modifying any immutable Thread binding.
	historical := run
	historical.ID = "unbound-usage-history"
	_, err := st.db.Exec(`INSERT INTO runs(id,mission_id,status,config_json,budget_json,created_at,updated_at)
		SELECT ?,mission_id,status,config_json,budget_json,created_at,updated_at FROM runs WHERE id=?`, historical.ID, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	usageTestEvent(t, st, historical, "model_gateway", events.ModelFailedEvent, "historical", boundary, map[string]any{"usage": known(4, 1)})
	for _, check := range []struct {
		period, start string
		calls, tokens int64
	}{
		{"today", "2026-10-20", 8, 148},
		{"30d", "2026-09-21", 9, 160},
		{"all", "2024-01-01", 11, 179},
	} {
		t.Run(check.period, func(t *testing.T) {
			result, err := st.GetUsageStatistics(t.Context(), check.period, 480, now)
			if err != nil {
				t.Fatal(err)
			}
			if result.Totals.Calls != check.calls || result.Totals.TotalTokens != check.tokens || result.StartDate != check.start || result.EndDate != "2026-10-20" {
				t.Fatalf("wrong calendar totals: %+v", result)
			}
			if result.Totals.UsageReportedCalls != check.calls-1 || result.UnassignedCalls != 1 || len(result.RecentSessions) != 1 || result.RecentSessions[0].Usage.Calls != check.calls-1 {
				t.Fatalf("coverage or Thread attribution changed: %+v", result)
			}
			if result.Cost.UntrackedCalls != check.calls || result.Cost.KnownCostMicros != 0 || result.Cost.PricedCalls != 0 || result.Cost.UnpricedCalls != 0 {
				t.Fatalf("untracked calls became priced: %+v", result.Cost)
			}
			var activityTokens int64
			for _, day := range result.ActivityDays {
				activityTokens += day.TotalTokens
			}
			if activityTokens != 168 || result.ActivityEndDate != "2026-10-20" || result.ActivityStartDate != "2026-04-22" {
				t.Fatalf("activity window follows selected period: %+v", result)
			}
			if result.Models[0].Provider != "p" || result.Models[0].Model != "m" || result.Models[0].Usage.TotalTokens < 100 {
				t.Fatalf("model identities/order changed: %+v", result.Models)
			}
		})
	}
	utc, err := st.GetUsageStatistics(t.Context(), "today", 0, now)
	if err != nil || utc.EndDate != "2026-10-19" || utc.Totals.TotalTokens != 160 {
		t.Fatalf("fixed UTC offset ignored: %+v %v", utc, err)
	}
}

func usageTestReservation(t *testing.T, st *SQLiteStore, run domain.Run, id, scope string, number int64, created, settled time.Time, amount int64, status string) {
	t.Helper()
	state := "settled"
	var settledValue any = ts(settled)
	if settled.IsZero() {
		state, settledValue = "reserved", nil
	}
	_, err := st.db.Exec(`INSERT INTO run_monetary_reservations
		(id,run_id,scope,provider,model,attempt_number,reserved_micros,settled_micros,released_micros,status,estimate_source,price_fingerprint,created_at,settled_at)
		VALUES (?, ?, ?, 'p', 'm', ?, 900, ?, ?, ?, 'test', ?, ?, ?)`, id, run.ID, scope, number,
		map[bool]int64{true: amount, false: 0}[state == "settled"], map[bool]int64{true: 900 - amount, false: 0}[state == "settled"], state,
		strings.Repeat("a", 64), ts(created), settledValue)
	if err != nil {
		t.Fatal(err)
	}
	usageTestEvent(t, st, run, "monetary_budget", events.MonetaryBudgetReservedEvent, id, created, map[string]any{"scope": scope})
	if state == "settled" {
		actual := amount
		if status != domain.MonetaryPricingPriced {
			actual = math.MaxInt64
		}
		usageTestEvent(t, st, run, "monetary_budget", events.MonetaryBudgetSettledEvent, id, settled, map[string]any{"scope": scope, "actual_micros": actual, "pricing_status": status})
	}
}

func TestUsageStatisticsCostsUseSettlementDatesAndActualPrices(t *testing.T) {
	st, run := newMonetaryTestStore(t)
	now := time.Date(2026, 10, 19, 17, 30, 0, 0, time.UTC)
	boundary := time.Date(2026, 10, 19, 16, 0, 0, 0, time.UTC)
	for i, check := range []struct {
		key             int64
		receipt, settle time.Time
		amount          int64
		status          string
	}{
		{1<<61 + 1, boundary.Add(-time.Minute), boundary.Add(time.Minute), 123, domain.MonetaryPricingPriced},
		{1<<61 + 2, boundary.Add(time.Minute), boundary.Add(2 * time.Minute), 900, domain.MonetaryPricingUsageUnknown},
		{1<<61 + 3, boundary.Add(time.Minute), time.Time{}, 0, ""},
		{1, boundary.Add(3 * time.Minute), boundary.Add(4 * time.Minute), 20, domain.MonetaryPricingPriced},
	} {
		id := fmt.Sprintf("cost-%d", i)
		usageTestReservation(t, st, run, id, domain.MonetaryScopeRoot, check.key, boundary.Add(-time.Hour), check.settle, check.amount, check.status)
		payload := map[string]any{"provider": "p", "model": "m", "model_attempt": check.key, "usage": llm.Usage{InputTokens: 10, OutputTokens: 2, TotalTokens: 12}}
		if check.key >= 1<<61 {
			payload["monetary_attempt_number"] = check.key
		} else {
			usageTestEvent(t, st, run, "model_gateway", events.ModelStartedEvent, id, boundary, payload)
		}
		usageTestEvent(t, st, run, "model_gateway", events.ModelFailedEvent, id, check.receipt, payload)
	}
	result, err := st.GetUsageStatistics(t.Context(), "today", 480, now)
	if err != nil {
		t.Fatal(err)
	}
	if result.Totals.Calls != 3 || result.Cost.KnownCostMicros != 143 || result.Cost.PricedCalls != 2 || result.Cost.UnpricedCalls != 1 || result.Cost.UntrackedCalls != 1 {
		t.Fatalf("receipt dates, reserved amounts, or coverage treated as actual settlement: %+v", result)
	}
	all, err := st.GetUsageStatistics(t.Context(), "all", 480, now)
	if err != nil || all.Totals.Calls != 4 || all.Cost.UntrackedCalls != 1 {
		t.Fatalf("cross-day receipt lost its settlement: %+v %v", all, err)
	}
}

func TestUsageStatisticsEmptyReadOnlyAndValidation(t *testing.T) {
	st, run := newMonetaryTestStore(t)
	st.db.SetMaxOpenConns(1)
	var before, after int64
	if err := st.db.QueryRow(`SELECT total_changes()`).Scan(&before); err != nil {
		t.Fatal(err)
	}
	if _, err := st.db.Exec(`PRAGMA query_only=ON`); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 10, 19, 12, 0, 0, 0, time.UTC)
	for _, period := range []string{"", "today", "30d", "all"} {
		result, err := st.GetUsageStatistics(t.Context(), period, -300, now)
		if err != nil || result.Totals.Calls != 0 || result.Models == nil || result.Days == nil || result.ActivityDays == nil || result.RecentSessions == nil {
			t.Fatalf("empty read-only usage: %+v %v", result, err)
		}
		result, err = st.GetThreadUsageStatistics(t.Context(), domain.InitialThreadID(run.ID), period, -300, now)
		if err != nil || result.Totals.Calls != 0 || result.Models == nil || result.Days == nil || result.ActivityDays == nil || result.RecentSessions == nil {
			t.Fatalf("empty read-only Thread usage: %+v %v", result, err)
		}
	}
	if err := st.db.QueryRow(`SELECT total_changes()`).Scan(&after); err != nil || after != before {
		t.Fatalf("usage query mutated the database: %d/%d %v", before, after, err)
	}
	for _, query := range []struct {
		period string
		offset int
	}{{"week", 0}, {"today", 841}, {"today", -841}} {
		_, err := st.GetUsageStatistics(t.Context(), query.period, query.offset, now)
		if apperror.CodeOf(err) != apperror.CodeInvalidArgument {
			t.Fatalf("invalid query accepted: %+v %v", query, err)
		}
	}
}

func TestUsageStatisticsLegacyFanoutAndPendingCancellation(t *testing.T) {
	st, run := newMonetaryTestStore(t)
	now := time.Now().UTC()
	usageTestEvent(t, st, run, "readonly_fanout", events.ReadOnlyFanoutShardCompletedEvent, "legacy-completed", now,
		map[string]any{"attempt": 1, "input_tokens": 4, "output_tokens": 2, "total_tokens": 6})
	usageTestEvent(t, st, run, "readonly_fanout", events.ReadOnlyFanoutShardFailedEvent, "legacy-failed", now,
		map[string]any{"attempt": 1, "total_tokens": 5})
	usageTestEvent(t, st, run, "readonly_fanout", events.ReadOnlyFanoutShardCancelledEvent, "pending", now,
		map[string]any{"attempt": 0, "provider": "", "model": "", "error_code": "operator_cancelled"})
	result, err := st.GetUsageStatistics(t.Context(), "all", 0, now)
	if err != nil || result.Totals.Calls != 2 || result.Totals.TotalTokens != 11 || result.Totals.InputTokens != 4 || result.Totals.OutputTokens != 2 || result.Totals.UsageReportedCalls != 1 || result.Totals.CacheUsageReportedCalls != 0 {
		t.Fatalf("legacy tokens lost, partial usage marked complete, or pending shard counted: %+v %v", result, err)
	}
}

func TestUsageStatisticsAuxiliarySettlementIdentities(t *testing.T) {
	st, run := newMonetaryTestStore(t)
	now := time.Now().UTC()
	thread, err := st.GetThreadByRun(t.Context(), run.ID)
	if err != nil {
		t.Fatal(err)
	}
	_, err = st.db.Exec(`INSERT INTO thread_context_compactions
		(id,operation_id,thread_id,run_id,session_id,key_digest,request_fingerprint,source_sha256,status,provider,model,created_at)
		VALUES (91,'compact-usage',?,?,?,'key','request','source','generated','p','m',?)`, thread.ID, run.ID, run.SessionID, ts(now))
	if err != nil {
		t.Fatal(err)
	}
	for i, check := range []struct {
		source, scope, subject, kind string
		key                          int64
		extra                        map[string]any
	}{
		{"specialist_model_gateway", domain.MonetaryScopeSpecialist, "specialist", events.ModelFailedEvent, 1<<61 + 7, map[string]any{"monetary_attempt_number": int64(1<<61 + 7)}},
		{"thread_context_compaction", domain.MonetaryScopeContextCompaction, "compact-usage", "context_compaction.model_completed", 91, map[string]any{}},
		{"readonly_fanout", domain.MonetaryScopeReadOnlyFanout, "fanout-shard", events.ReadOnlyFanoutShardFailedEvent, int64(llm.ReadOnlyFanoutMonetaryAttemptNumber("fanout-cost", 1, 1)), map[string]any{"execution_id": "fanout-cost", "shard": 1, "attempt": 1}},
	} {
		usageTestReservation(t, st, run, fmt.Sprintf("aux-%d", i), check.scope, check.key, now.Add(-time.Minute), now, 10, domain.MonetaryPricingPriced)
		payload := check.extra
		payload["provider"], payload["model"], payload["usage"] = "p", "m", llm.Usage{InputTokens: 2, OutputTokens: 1, TotalTokens: 3}
		usageTestEvent(t, st, run, check.source, check.kind, check.subject, now, payload)
	}
	result, err := st.GetUsageStatistics(t.Context(), "all", 0, now)
	if err != nil || result.Totals.Calls != 3 || result.Cost.UntrackedCalls != 0 || result.Cost.PricedCalls != 3 || result.Cost.KnownCostMicros != 30 {
		t.Fatalf("auxiliary receipts did not match their settled costs: %+v %v", result, err)
	}
}
