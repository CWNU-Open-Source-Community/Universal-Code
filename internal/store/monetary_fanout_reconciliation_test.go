package store

import (
	"context"
	"errors"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"cyberagent-workbench/internal/application"
	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/events"
	"cyberagent-workbench/internal/llm"
	"cyberagent-workbench/internal/policy"
	"cyberagent-workbench/internal/pricing"
)

type fanoutFailedSettlementStore struct {
	*SQLiteStore
	calls atomic.Int32
}

func (s *fanoutFailedSettlementStore) SettleModelCost(context.Context, domain.MonetarySettleRequest) (domain.MonetaryUsage, bool, error) {
	s.calls.Add(1)
	return domain.MonetaryUsage{}, false, errors.New("fixture interrupted monetary settlement")
}

type fanoutResultBarrierProvider struct {
	fanoutPricedProvider
	ready   chan struct{}
	fail    bool
	arrived atomic.Int32
}

func (p *fanoutResultBarrierProvider) Chat(ctx context.Context, request llm.ChatRequest) (*llm.ChatResponse, error) {
	response, err := p.fanoutPricedProvider.Chat(ctx, request)
	if err != nil {
		return response, err
	}
	// Both reservations must finish before either terminal receipt exists.
	// That leaves the later budget read as the first reconciliation opportunity.
	if p.arrived.Add(1) == 2 {
		close(p.ready)
	}
	select {
	case <-p.ready:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	if p.fail {
		return response, errors.New("fixture provider failure with billable usage")
	}
	return response, nil
}

func TestFanoutMonetaryReconcilesDurableOutcomesAfterReopen(t *testing.T) {
	for _, failedResponse := range []bool{false, true} {
		name := "completed_but_settlement_interrupted"
		if failedResponse {
			name = "failed_response_with_known_usage"
		}
		t.Run(name, func(t *testing.T) {
			const databaseName = "fanout-settlement.db"
			st, run, _ := createReadOnlyFanoutFixture(t, databaseName, 2)
			ctx := t.Context()
			if _, err := st.db.ExecContext(ctx, `UPDATE runs SET budget_json=json_set(budget_json,'$.max_cost_usd',1) WHERE id=?`, run.ID); err != nil {
				t.Fatal(err)
			}
			plan := createReadOnlyFanoutExecutionPlan(t, ctx, st, run.ID, "2", "fanout-settlement-plan")
			provider := &fanoutResultBarrierProvider{ready: make(chan struct{}), fail: failedResponse}
			router := llm.NewDefaultRouter()
			router.RegisterProvider(provider)
			snapshot := monetaryTestSnapshot(t, time.Now().UTC())
			snapshot.Entries[0].Model = router.Resolve(run.Config.ModelRoute).Model
			snapshot.Fingerprint = pricing.Fingerprint(snapshot)
			if _, _, err := st.ImportPriceSnapshot(ctx, snapshot); err != nil {
				t.Fatal(err)
			}
			interrupted := &fanoutFailedSettlementStore{SQLiteStore: st}
			service := application.NewReadOnlyFanoutExecutionService(st, router, policy.NewDefaultChecker()).
				WithMonetaryBudget(application.NewMonetaryBudgetService(interrupted))
			result, executeErr := service.Execute(ctx, application.ExecuteReadOnlyFanoutRequest{
				PlanID: plan.ID, OperationKey: "fanout-settlement-execute", RequestedBy: "operator", MaxOutputTokensPerShard: 512,
			})
			if provider.calls.Load() != 2 {
				t.Fatalf("expected two billable requests, got %d: %v", provider.calls.Load(), executeErr)
			}
			wantStatus := domain.ReadOnlyFanoutExecutionCompleted
			wantSettlements := int32(2)
			if failedResponse {
				wantStatus, wantSettlements = domain.ReadOnlyFanoutExecutionFailed, 0
			}
			if (executeErr != nil) != failedResponse || result.Execution.Status != wantStatus || interrupted.calls.Load() != wantSettlements {
				t.Fatalf("durable outcome did not reach the intended boundary: status=%s settlements=%d err=%v", result.Execution.Status, interrupted.calls.Load(), executeErr)
			}
			var initialReserved, initialSettled, initialReleased int64
			if err := st.db.QueryRowContext(ctx, `SELECT reserved_micros,settled_micros,released_micros FROM run_monetary_usage WHERE run_id=?`, run.ID).
				Scan(&initialReserved, &initialSettled, &initialReleased); err != nil {
				t.Fatal(err)
			}
			if initialReserved <= 16 || initialSettled != 0 || initialReleased != 0 {
				t.Fatalf("fixture did not leave open monetary reservations: reserved=%d settled=%d released=%d", initialReserved, initialSettled, initialReleased)
			}
			path := filepath.Join(st.home, databaseName)
			if err := st.Close(); err != nil {
				t.Fatal(err)
			}
			reopened, err := Open(path)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = reopened.Close() })
			for range 2 {
				usage, err := reopened.GetMonetaryUsage(ctx, run.ID)
				if err != nil || usage.SettledMicros != 16 || usage.ReservedMicros != initialReserved ||
					usage.ReleasedMicros != initialReserved-16 || usage.RemainingMicros != 999984 || usage.UnpricedCalls != 0 {
					t.Fatalf("durable fan-out usage was not priced exactly once: %+v err=%v", usage, err)
				}
			}
			var settledEvents int
			if err := reopened.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM run_events WHERE run_id=? AND type=?`, run.ID, events.MonetaryBudgetSettledEvent).Scan(&settledEvents); err != nil || settledEvents != 2 {
				t.Fatalf("reconciliation duplicated settlements: events=%d err=%v", settledEvents, err)
			}
		})
	}
}
