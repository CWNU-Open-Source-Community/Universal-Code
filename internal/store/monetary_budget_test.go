package store

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"cyberagent-workbench/internal/apperror"
	"cyberagent-workbench/internal/application"
	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/events"
	"cyberagent-workbench/internal/pricing"
)

func monetaryTestSnapshot(t *testing.T, now time.Time) pricing.Snapshot {
	t.Helper()
	snapshot := pricing.Snapshot{
		ProtocolVersion: pricing.ProtocolVersion, ID: "test-prices",
		Source: pricing.SourceOperatorImport, Currency: pricing.CurrencyUSD,
		ImportedBy: "cli_operator", ImportedAt: now,
		ValidFrom: now.Add(-time.Hour), ValidUntil: now.Add(24 * time.Hour),
		Entries: []pricing.Entry{{
			Provider: "mock", Model: "mock-code",
			InputPerMillionMicros: 1000000, OutputPerMillionMicros: 2000000,
		}},
	}
	snapshot.Fingerprint = pricing.Fingerprint(snapshot)
	return snapshot
}

func TestMonetaryFreshRunProjectionDoesNotCreateLedger(t *testing.T) {
	st, run := newMonetaryTestStore(t)
	ctx := context.Background()
	st.db.SetMaxOpenConns(1)
	var beforeChanges, beforeEvents int64
	if err := st.db.QueryRowContext(ctx, `SELECT total_changes()`).Scan(&beforeChanges); err != nil {
		t.Fatal(err)
	}
	if err := st.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM run_events`).Scan(&beforeEvents); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		if i == 1 {
			if _, err := st.db.ExecContext(ctx, `PRAGMA query_only=ON`); err != nil {
				t.Fatal(err)
			}
		}
		usage, err := st.GetMonetaryUsage(ctx, run.ID)
		if err != nil {
			t.Fatalf("fresh tracked Run cannot read its budget: %v", err)
		}
		if err := usage.Validate(); err != nil {
			t.Fatal(err)
		}
		if usage.RunID != run.ID || !usage.Tracked || usage.Currency != "USD" || usage.CapMicros != 2_000_000 || usage.RemainingMicros != usage.CapMicros || usage.ReservedMicros != 0 || usage.SettledMicros != 0 || usage.ReleasedMicros != 0 || usage.ExhaustedAt != nil || !usage.UpdatedAt.Equal(run.CreatedAt) {
			t.Fatalf("fresh Run projection is not its unspent budget: %#v", usage)
		}
	}
	if _, err := st.GetMonetaryUsage(ctx, "run-does-not-exist"); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("unknown Run was projected as an empty budget: %v", err)
	}
	var changes, rows, reservations, eventsAfter int64
	if err := st.db.QueryRowContext(ctx, `SELECT total_changes(),(SELECT COUNT(*) FROM run_monetary_usage),(SELECT COUNT(*) FROM run_monetary_reservations),(SELECT COUNT(*) FROM run_events)`).Scan(&changes, &rows, &reservations, &eventsAfter); err != nil {
		t.Fatal(err)
	}
	if changes != beforeChanges || eventsAfter != beforeEvents || rows != 0 || reservations != 0 {
		t.Fatalf("budget query wrote state: changes %d/%d events %d/%d ledger %d reservations %d", beforeChanges, changes, beforeEvents, eventsAfter, rows, reservations)
	}
}

func newMonetaryTestStore(t *testing.T) (*SQLiteStore, domain.Run) {
	t.Helper()
	st, err := Open(filepath.Join(t.TempDir(), "monetary.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	ctx := context.Background()
	_, run, err := application.NewRunService(st).Create(ctx, application.CreateRunRequest{
		Goal: "monetary test", Profile: "code", WorkspaceID: "ws-monetary",
		Budget: domain.Budget{MaxTurns: 10, MaxToolCalls: 100, MaxCostUSD: 2.0},
	})
	if err != nil {
		t.Fatal(err)
	}
	return st, run
}

func monetaryTestInsertModelEvent(t *testing.T, st *SQLiteStore, run domain.Run,
	eventType string, payload map[string]any,
) {
	t.Helper()
	ctx := context.Background()
	event, err := events.New(run.ID, run.MissionID, eventType, "model_gateway", run.ID, payload)
	if err != nil {
		t.Fatal(err)
	}
	tx, err := st.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := insertRunEventTx(ctx, tx, event); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
}

func TestMonetaryBudgetReserveSettleReleaseAndOversell(t *testing.T) {
	st, run := newMonetaryTestStore(t)
	ctx := context.Background()
	if _, _, err := st.ImportPriceSnapshot(ctx, monetaryTestSnapshot(t, time.Now().UTC())); err != nil {
		t.Fatal(err)
	}
	snapshot, found, err := st.ActivePriceSnapshot(ctx)
	if err != nil || !found || snapshot.ID != "test-prices" {
		t.Fatalf("active snapshot missing: %#v %t %v", snapshot, found, err)
	}
	// The cap is 2.00 USD = 2,000,000 micros.
	reserve := domain.MonetaryReserveRequest{RunID: run.ID,
		Scope: domain.MonetaryScopeRoot, Provider: "mock", Model: "mock-code",
		AttemptNumber: 1, ReservedMicros: 1_200_000,
		PriceFingerprint: snapshot.Fingerprint, EstimateSource: "price_snapshot.v1/test-prices",
	}
	usage, replayed, err := st.ReserveModelCost(ctx, reserve)
	if err != nil {
		t.Fatal(err)
	}
	if replayed || !usage.Tracked || usage.ReservedMicros != 1_200_000 ||
		usage.RemainingMicros != 800_000 {
		t.Fatalf("unexpected reserve usage: %#v replayed=%t", usage, replayed)
	}
	// Exact replay of the same attempt is idempotent.
	usage, replayed, err = st.ReserveModelCost(ctx, reserve)
	if err != nil || !replayed || usage.ReservedMicros != 1_200_000 {
		t.Fatalf("replay failed: %#v %t %v", usage, replayed, err)
	}
	// A different amount for the same attempt conflicts.
	tampered := reserve
	tampered.ReservedMicros = 999
	if _, _, err := st.ReserveModelCost(ctx, tampered); apperror.CodeOf(err) != apperror.CodeConflict {
		t.Fatalf("tampered replay was not rejected: %v", err)
	}
	// A second attempt that would oversell the cap is refused.
	over := reserve
	over.AttemptNumber = 2
	over.ReservedMicros = 900_000
	if _, _, err := st.ReserveModelCost(ctx, over); apperror.CodeOf(err) != apperror.CodeResourceExhausted {
		t.Fatalf("oversell was not rejected: %v", err)
	}
	// Settle the first reservation with actual usage below the reserve.
	settled, replayed, err := st.SettleModelCost(ctx, domain.MonetarySettleRequest{
		RunID: run.ID, Scope: domain.MonetaryScopeRoot, AttemptNumber: 1,
		ActualMicros: 1_000_000,
	})
	if err != nil || replayed {
		t.Fatalf("settle failed: %#v %t %v", settled, replayed, err)
	}
	if settled.SettledMicros != 1_000_000 || settled.ReleasedMicros != 200_000 ||
		settled.RemainingMicros != 1_000_000 {
		t.Fatalf("unexpected settle usage: %#v", settled)
	}
	// Settle replay is idempotent.
	if _, replayed, err := st.SettleModelCost(ctx, domain.MonetarySettleRequest{
		RunID: run.ID, Scope: domain.MonetaryScopeRoot, AttemptNumber: 1,
		ActualMicros: 1_000_000,
	}); err != nil || !replayed {
		t.Fatalf("settle replay failed: replayed=%t err=%v", replayed, err)
	}
	// After settling, the second attempt now fits.
	if _, _, err := st.ReserveModelCost(ctx, over); err != nil {
		t.Fatalf("post-settle reserve failed: %v", err)
	}
	third := over
	third.AttemptNumber = 3
	third.ReservedMicros = 100_001
	if _, _, err := st.ReserveModelCost(ctx, third); apperror.CodeOf(err) != apperror.CodeResourceExhausted {
		t.Fatalf("spent plus open reservations did not enforce the cumulative cap: %v", err)
	}
}

func TestMonetaryBudgetUntrackedRunsSkipTheLedger(t *testing.T) {
	st, err := Open(filepath.Join(t.TempDir(), "monetary-untracked.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	ctx := context.Background()
	_, run, err := application.NewRunService(st).Create(ctx, application.CreateRunRequest{
		Goal: "untracked", Profile: "code", WorkspaceID: "ws-untracked",
		Budget: domain.Budget{MaxTurns: 10, MaxToolCalls: 100},
	})
	if err != nil {
		t.Fatal(err)
	}
	usage, replayed, err := st.ReserveModelCost(ctx, domain.MonetaryReserveRequest{
		RunID: run.ID, Scope: domain.MonetaryScopeRoot, Provider: "mock",
		Model: "mock-code", AttemptNumber: 1, ReservedMicros: 1000,
		PriceFingerprint: strings.Repeat("a", 64), EstimateSource: "test",
	})
	if err != nil || replayed || usage.Tracked {
		t.Fatalf("untracked run touched the ledger: %#v %t %v", usage, replayed, err)
	}
}

func TestMonetaryPriceImportIsIdempotentAndRotates(t *testing.T) {
	st, _ := newMonetaryTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC()
	snapshot := monetaryTestSnapshot(t, now)
	if _, replayed, err := st.ImportPriceSnapshot(ctx, snapshot); err != nil || replayed {
		t.Fatalf("first import failed: replayed=%t err=%v", replayed, err)
	}
	if _, replayed, err := st.ImportPriceSnapshot(ctx, snapshot); err != nil || !replayed {
		t.Fatalf("replay import failed: replayed=%t err=%v", replayed, err)
	}
	list, err := st.ListPriceSnapshots(ctx, 8)
	if err != nil || len(list) != 1 {
		t.Fatalf("list failed: %#v %v", list, err)
	}

	extended := snapshot
	extended.ValidUntil = snapshot.ValidUntil.Add(time.Hour)
	extended.Fingerprint = pricing.Fingerprint(extended)
	if extended.Fingerprint != snapshot.Fingerprint {
		t.Fatal("test fixture unexpectedly changed the fingerprint")
	}
	if _, replayed, err := st.ImportPriceSnapshot(ctx, extended); apperror.CodeOf(err) != apperror.CodeConflict || replayed {
		t.Fatalf("changed validity window was accepted: replayed=%t err=%v", replayed, err)
	}
	active, found, err := st.ActivePriceSnapshot(ctx)
	if err != nil || !found || active.ID != snapshot.ID ||
		!active.ValidUntil.Equal(snapshot.ValidUntil) ||
		active.Fingerprint != snapshot.Fingerprint {
		t.Fatalf("rejected replay mutated the active snapshot: %#v %t %v", active, found, err)
	}

	second := snapshot
	second.ID = "test-prices-v2"
	second.Fingerprint = pricing.Fingerprint(second)
	if _, _, err := st.ImportPriceSnapshot(ctx, second); err != nil {
		t.Fatal(err)
	}
	active, found, err = st.ActivePriceSnapshot(ctx)
	if err != nil || !found || active.ID != "test-prices-v2" {
		t.Fatalf("active snapshot did not rotate: %#v %t %v", active, found, err)
	}
}

func TestMonetaryReservationsReleaseOnTerminalRun(t *testing.T) {
	st, run := newMonetaryTestStore(t)
	ctx := context.Background()
	if _, _, err := st.ImportPriceSnapshot(ctx, monetaryTestSnapshot(t, time.Now().UTC())); err != nil {
		t.Fatal(err)
	}
	snapshot, _, _ := st.ActivePriceSnapshot(ctx)
	if _, _, err := st.ReserveModelCost(ctx, domain.MonetaryReserveRequest{
		RunID: run.ID, Scope: domain.MonetaryScopeRoot, Provider: "mock",
		Model: "mock-code", AttemptNumber: 1, ReservedMicros: 500_000,
		PriceFingerprint: snapshot.Fingerprint, EstimateSource: "test",
	}); err != nil {
		t.Fatal(err)
	}
	released, err := st.ReleaseOpenMonetaryReservations(ctx, run.ID)
	if err != nil || released != 1 {
		t.Fatalf("release failed: %d %v", released, err)
	}
	usage, err := st.GetMonetaryUsage(ctx, run.ID)
	if err != nil || usage.ReleasedMicros != 500_000 {
		t.Fatalf("unexpected post-release usage: %#v %v", usage, err)
	}
}

func TestMonetaryReconciliationClosesTerminalAttempts(t *testing.T) {
	st, run := newMonetaryTestStore(t)
	ctx := context.Background()
	if _, _, err := st.ImportPriceSnapshot(ctx, monetaryTestSnapshot(t, time.Now().UTC())); err != nil {
		t.Fatal(err)
	}
	snapshot, _, _ := st.ActivePriceSnapshot(ctx)
	if _, _, err := st.ReserveModelCost(ctx, domain.MonetaryReserveRequest{
		RunID: run.ID, Scope: domain.MonetaryScopeRoot, Provider: "mock",
		Model: "mock-code", AttemptNumber: 1, ReservedMicros: 500_000,
		PriceFingerprint: snapshot.Fingerprint, EstimateSource: "test",
	}); err != nil {
		t.Fatal(err)
	}
	monetaryTestInsertModelEvent(t, st, run, events.ModelCompletedEvent, map[string]any{
		"model_attempt": 1, "provider": "mock", "model": "mock-code",
		"tool_call_count": 0,
		"usage":           map[string]any{"input_tokens": 100, "output_tokens": 50},
	})
	usage, err := st.GetMonetaryUsage(ctx, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	// 100 tokens at 1 USD/M + 50 tokens at 2 USD/M = 200 micros settled.
	if usage.SettledMicros != 200 || usage.ReleasedMicros != 499_800 {
		t.Fatalf("reconciliation settled incorrectly: %#v", usage)
	}
}
