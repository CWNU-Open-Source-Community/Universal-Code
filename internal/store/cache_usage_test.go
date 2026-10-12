package store

import (
	"context"
	"testing"
	"time"

	"cyberagent-workbench/internal/apperror"
	"cyberagent-workbench/internal/application"
	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/events"
	"cyberagent-workbench/internal/llm"
	"cyberagent-workbench/internal/pricing"
)

func cachePrice(value int64) *int64 { return &value }

func TestCacheMonetaryReconciliationUsesPinnedPricesAfterReopen(t *testing.T) {
	st, run := newMonetaryTestStore(t)
	ctx := t.Context()
	prices := monetaryTestSnapshot(t, time.Now().UTC())
	prices.Entries[0].CacheReadPerMillionMicros = cachePrice(250_000)
	prices.Entries[0].CacheCreation5mPerMillionMicros = cachePrice(1_250_000)
	prices.Entries[0].CacheCreation1hPerMillionMicros = cachePrice(2_000_000)
	prices.Fingerprint = pricing.Fingerprint(prices)
	if _, _, err := st.ImportPriceSnapshot(ctx, prices); err != nil {
		t.Fatal(err)
	}
	request := domain.MonetaryReserveRequest{RunID: run.ID, Scope: domain.MonetaryScopeRoot, AttemptNumber: 1,
		Provider: "mock", Model: "mock-code", ReservedMicros: 1000, PriceFingerprint: prices.Fingerprint, EstimateSource: "cache-test"}
	if _, _, err := st.ReserveModelCost(ctx, request); err != nil {
		t.Fatal(err)
	}
	u := llm.Usage{InputTokens: 100, OutputTokens: 10, TotalTokens: 110, CacheReadTokens: 40,
		CacheCreationTokens: 40, CacheCreation5mTokens: 20, CacheCreation1hTokens: 20,
		CacheCreationBreakdownKnown: true, CacheUsageReported: true}
	monetaryTestInsertModelEvent(t, st, run, events.ModelCompletedEvent, map[string]any{
		"model_attempt": 1, "provider": "mock", "model": "mock-code", "usage": u, "tool_call_count": 0})
	rotated := prices
	rotated.ID = "rotated-cache-prices"
	rotated.Entries = append([]pricing.Entry(nil), prices.Entries...)
	rotated.Entries[0].CacheReadPerMillionMicros = cachePrice(999_000_000)
	rotated.Fingerprint = pricing.Fingerprint(rotated)
	if _, _, err := st.ImportPriceSnapshot(ctx, rotated); err != nil {
		t.Fatal(err)
	}
	var seq int
	var name, path string
	if err := st.db.QueryRowContext(ctx, "PRAGMA database_list").Scan(&seq, &name, &path); err != nil {
		t.Fatal(err)
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	for range 2 {
		money, err := reopened.GetMonetaryUsage(ctx, run.ID)
		// Ordinary 20 + read 10 + creation5m 25 + creation1h 40 + output 20.
		if err != nil || money.SettledMicros != 115 || money.ReleasedMicros != 885 || money.RemainingMicros != money.CapMicros-115 || money.UnpricedCalls != 0 {
			t.Fatalf("pinned cache settlement=%+v err=%v", money, err)
		}
	}
	if _, err := application.NewMonetaryBudgetService(reopened).SettleModelCall(ctx, run.ID, domain.MonetaryScopeRoot,
		llm.ModelAttempt{Number: 1, Provider: "mock", Model: "mock-code"}, u, 0); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := reopened.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM run_events WHERE run_id=? AND type=?`, run.ID, events.MonetaryBudgetSettledEvent).Scan(&count); err != nil || count != 1 {
		t.Fatalf("settlement duplicated: %d %v", count, err)
	}
	usage, err := reopened.GetModelUsage(ctx, run.ID)
	if err != nil || usage.CacheReadTokens != 40 || usage.CacheCreationTokens != 40 || usage.InputTokens != 100 || usage.Calls != 1 || usage.CacheUsageReportedCalls != 1 {
		t.Fatalf("cache receipt after reopen=%+v err=%v", usage, err)
	}
}

func TestCacheUsageProjectionReadsLegacyAndFailedReceiptsWithoutCountingUnknownCancellation(t *testing.T) {
	st, run := newMonetaryTestStore(t)
	ctx := context.Background()
	tx, err := st.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	known := llm.Usage{InputTokens: 6, OutputTokens: 2, TotalTokens: 8, CacheReadTokens: 2, CacheUsageReported: true}
	for index, receipt := range []struct {
		source, kind string
		usage        any
		purpose      string
	}{
		{"model_gateway", events.ModelCompletedEvent, map[string]any{"input_tokens": 3, "output_tokens": 2}, ""},
		{"model_gateway", events.ModelFailedEvent, known, ""},
		{"model_gateway", events.ModelCompletedEvent, known, llm.ModelPurposeContextCompaction},
		{"readonly_fanout", events.ReadOnlyFanoutShardFailedEvent, known, ""},
		{"readonly_fanout", events.ReadOnlyFanoutShardCancelledEvent, nil, ""},
	} {
		event, err := events.New(run.ID, run.MissionID, receipt.kind, receipt.source, string(rune('a'+index)), map[string]any{"usage": receipt.usage, "purpose": receipt.purpose})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := insertRunEventTx(ctx, tx, event); err != nil {
			t.Fatal(err)
		}
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	u, err := st.GetModelUsage(ctx, run.ID)
	if err != nil || u.InputTokens != 21 || u.OutputTokens != 8 || u.TotalTokens != 29 || u.CacheReadTokens != 6 || u.Calls != 4 || u.CacheUsageReportedCalls != 3 {
		t.Fatalf("legacy/failed/compaction/cancellation usage=%+v err=%v", u, err)
	}
}

func TestCacheSpecialistUsagePersistsAndMissingPricesRemainExplicit(t *testing.T) {
	st, run, ref, a, path := newSpecialistMonetaryLedger(t)
	ctx := t.Context()
	u := llm.Usage{InputTokens: 8, OutputTokens: 2, TotalTokens: 10, CacheReadTokens: 3, CacheCreationTokens: 2,
		CacheCreation5mTokens: 1, CacheCreation1hTokens: 1, CacheCreationBreakdownKnown: true, CacheUsageReported: true}
	a.Outcome, a.ErrorText = llm.OutcomePermanent, "model refused this request"
	for range 2 {
		if _, err := st.RecordSpecialistModelFailed(ctx, ref, a, &u); err != nil {
			t.Fatal(err)
		}
	}
	drift := u
	drift.CacheReadTokens++
	if _, err := st.RecordSpecialistModelFailed(ctx, ref, a, &drift); apperror.CodeOf(err) != apperror.CodeConflict {
		t.Fatalf("changed cached usage replay accepted: %v", err)
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	for range 2 {
		money, err := reopened.GetMonetaryUsage(ctx, run.ID)
		if err != nil || money.SettledMicros != 1000 || money.ReleasedMicros != 0 || money.UnpricedCalls != 1 {
			t.Fatalf("unpriced cache usage lost: %+v %v", money, err)
		}
		usage, err := reopened.GetModelUsage(ctx, run.ID)
		if err != nil || usage.Calls != 1 || usage.CacheReadTokens != 3 || usage.CacheCreationTokens != 2 || usage.CacheCreation5mTokens != 1 || usage.CacheCreation1hTokens != 1 {
			t.Fatalf("Specialist cache receipt lost or duplicated: %+v %v", usage, err)
		}
	}
}
