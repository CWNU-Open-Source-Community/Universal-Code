package store

import (
	"math"
	"testing"
	"time"

	"cyberagent-workbench/internal/apperror"
	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/events"
)

func TestMonetaryKnownOverageSurvivesSettlementAndReconciliation(t *testing.T) {
	for _, reconcile := range []bool{false, true} {
		name := "settlement"
		if reconcile {
			name = "reconciliation"
		}
		t.Run(name, func(t *testing.T) {
			st, run := newMonetaryTestStore(t)
			ctx := t.Context()
			snapshot := monetaryTestSnapshot(t, time.Now().UTC())
			if _, _, err := st.ImportPriceSnapshot(ctx, snapshot); err != nil {
				t.Fatal(err)
			}
			reserve := domain.MonetaryReserveRequest{RunID: run.ID, Scope: domain.MonetaryScopeRoot,
				Provider: "mock", Model: "mock-code", AttemptNumber: 1, ReservedMicros: 100,
				PriceFingerprint: snapshot.Fingerprint, EstimateSource: "test"}
			if _, _, err := st.ReserveModelCost(ctx, reserve); err != nil {
				t.Fatal(err)
			}
			const actual = 2_500_000
			settle := domain.MonetarySettleRequest{RunID: run.ID, Scope: reserve.Scope,
				AttemptNumber: reserve.AttemptNumber, ActualMicros: actual, PricingStatus: domain.MonetaryPricingPriced}
			if reconcile {
				monetaryTestInsertModelEvent(t, st, run, events.ModelCompletedEvent, map[string]any{
					"model_attempt": 1, "provider": "mock", "model": "mock-code", "tool_call_count": 0,
					"usage": map[string]any{"input_tokens": actual, "output_tokens": 0, "total_tokens": actual},
				})
			} else if _, _, err := st.SettleModelCost(ctx, settle); err != nil {
				t.Fatal(err)
			}
			usage, err := st.GetMonetaryUsage(ctx, run.ID)
			if err != nil || usage.SettledMicros != actual || usage.RemainingMicros != 0 ||
				usage.ReservedMicros != actual || usage.ReleasedMicros != 0 {
				t.Fatalf("known overage was lost: %+v err=%v", usage, err)
			}
			if replay, replayed, err := st.ReserveModelCost(ctx, reserve); err != nil || !replayed || replay.SettledMicros != actual {
				t.Fatalf("original reservation no longer replays: %+v replayed=%t err=%v", replay, replayed, err)
			}
			if replay, replayed, err := st.SettleModelCost(ctx, settle); err != nil || !replayed || replay.SettledMicros != actual {
				t.Fatalf("settlement replay charged twice: %+v replayed=%t err=%v", replay, replayed, err)
			}
			reserve.AttemptNumber++
			if _, _, err := st.ReserveModelCost(ctx, reserve); apperror.CodeOf(err) != apperror.CodeResourceExhausted {
				t.Fatalf("known overspend permitted another request: %v", err)
			}
		})
	}
}

func TestMonetaryUnpricedSettlementRetainsEstimate(t *testing.T) {
	for _, status := range []string{domain.MonetaryPricingUsageUnknown, domain.MonetaryPricingPriceMissing, domain.MonetaryPricingCachePriceMissing} {
		t.Run(status, func(t *testing.T) {
			st, run := newMonetaryTestStore(t)
			ctx := t.Context()
			snapshot := monetaryTestSnapshot(t, time.Now().UTC())
			if _, _, err := st.ImportPriceSnapshot(ctx, snapshot); err != nil {
				t.Fatal(err)
			}
			if _, _, err := st.ReserveModelCost(ctx, domain.MonetaryReserveRequest{RunID: run.ID,
				Scope: domain.MonetaryScopeRoot, Provider: "mock", Model: "mock-code", AttemptNumber: 1,
				ReservedMicros: 100, PriceFingerprint: snapshot.Fingerprint, EstimateSource: "test"}); err != nil {
				t.Fatal(err)
			}
			usage, _, err := st.SettleModelCost(ctx, domain.MonetarySettleRequest{RunID: run.ID,
				Scope: domain.MonetaryScopeRoot, AttemptNumber: 1, ActualMicros: math.MaxInt64, PricingStatus: status})
			if err != nil || usage.SettledMicros != 100 || usage.ReservedMicros != 100 || usage.UnpricedCalls != 1 {
				t.Fatalf("unknown charge did not retain its estimate: %+v err=%v", usage, err)
			}
		})
	}
}
