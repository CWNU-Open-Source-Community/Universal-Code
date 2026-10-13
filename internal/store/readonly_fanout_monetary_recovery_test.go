package store

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"cyberagent-workbench/internal/apperror"
	"cyberagent-workbench/internal/application"
	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/llm"
	"cyberagent-workbench/internal/policy"
	"cyberagent-workbench/internal/pricing"
)

// Drop terminal writes after dispatch to reproduce a worker losing its result.
type fanoutLostResultStore struct{ *SQLiteStore }

func (*fanoutLostResultStore) CompleteReadOnlyFanoutExecutionShard(context.Context, domain.RunExecutionLease, string, int, int, string, string, llm.Usage, time.Duration, domain.ReadOnlyFanoutReport) (domain.ReadOnlyFanoutExecutionShard, error) {
	return domain.ReadOnlyFanoutExecutionShard{}, errors.New("fixture lost terminal persistence")
}
func (*fanoutLostResultStore) FailReadOnlyFanoutExecutionShard(context.Context, domain.RunExecutionLease, string, int, int, string, string, *llm.Usage, time.Duration, domain.ReadOnlyFanoutExecutionShardStatus, string, string) (domain.ReadOnlyFanoutExecutionShard, error) {
	return domain.ReadOnlyFanoutExecutionShard{}, errors.New("fixture lost terminal persistence")
}
func (*fanoutLostResultStore) CancelReadOnlyFanoutExecutionRemainder(context.Context, domain.RunExecutionLease, string, string, string) (domain.ReadOnlyFanoutExecution, error) {
	return domain.ReadOnlyFanoutExecution{}, errors.New("fixture lost terminal persistence")
}
func (*fanoutLostResultStore) FinalizeReadOnlyFanoutExecution(context.Context, domain.RunExecutionLease, string, domain.ReadOnlyFanoutExecutionStatus, string) (domain.ReadOnlyFanoutExecution, bool, error) {
	return domain.ReadOnlyFanoutExecution{}, false, errors.New("fixture lost terminal persistence")
}

type fanoutPricedProvider struct {
	llm.MockProvider
	calls atomic.Int32
}

func (p *fanoutPricedProvider) Chat(ctx context.Context, request llm.ChatRequest) (*llm.ChatResponse, error) {
	p.calls.Add(1)
	response, err := p.MockProvider.Chat(ctx, request)
	if response != nil {
		response.Usage = llm.Usage{InputTokens: 2, OutputTokens: 3, TotalTokens: 5}
	}
	return response, err
}

func TestReadOnlyFanoutRecoveryReservesEachBillableAttempt(t *testing.T) {
	for _, tightBudget := range []bool{false, true} {
		name := "retry_fits"
		if tightBudget {
			name = "retry_exhausted"
		}
		t.Run(name, func(t *testing.T) {
			st, run, _ := createReadOnlyFanoutFixture(t, "fanout-money.db", 2)
			ctx := t.Context()
			if _, err := st.db.ExecContext(ctx, `UPDATE runs SET budget_json=json_set(budget_json,'$.max_cost_usd',1) WHERE id=?`, run.ID); err != nil {
				t.Fatal(err)
			}
			plan := createReadOnlyFanoutExecutionPlan(t, ctx, st, run.ID, "2", "fanout-money-plan-0001")
			provider := &fanoutPricedProvider{}
			router := llm.NewDefaultRouter()
			router.RegisterProvider(provider)
			ref := router.Resolve(run.Config.ModelRoute)
			snapshot := monetaryTestSnapshot(t, time.Now().UTC())
			snapshot.Entries[0].Model = ref.Model
			snapshot.Fingerprint = pricing.Fingerprint(snapshot)
			if _, _, err := st.ImportPriceSnapshot(ctx, snapshot); err != nil {
				t.Fatal(err)
			}
			money := application.NewMonetaryBudgetService(st)
			request := application.ExecuteReadOnlyFanoutRequest{PlanID: plan.ID, OperationKey: "fanout-money-execute", RequestedBy: "operator", MaxOutputTokensPerShard: 512}
			firstService := application.NewReadOnlyFanoutExecutionService(&fanoutLostResultStore{st}, router, policy.NewDefaultChecker()).WithMonetaryBudget(money)
			if _, err := firstService.Execute(ctx, request); err == nil {
				t.Fatal("lost terminal writes unexpectedly succeeded")
			}
			before, err := money.Usage(ctx, run.ID)
			if err != nil || provider.calls.Load() == 0 || before.ReservedMicros == 0 || before.SettledMicros != 0 {
				t.Fatalf("first request was not left unresolved: %+v calls=%d err=%v", before, provider.calls.Load(), err)
			}
			firstCalls := provider.calls.Load()
			if tightBudget {
				var minimum int64
				if err := st.db.QueryRowContext(ctx, `SELECT MIN(reserved_micros) FROM run_monetary_reservations WHERE run_id=?`, run.ID).Scan(&minimum); err != nil {
					t.Fatal(err)
				}
				capUSD := float64(before.ReservedMicros+minimum-1) / 1_000_000
				if _, err := st.db.ExecContext(ctx, `UPDATE runs SET budget_json=json_set(budget_json,'$.max_cost_usd',?) WHERE id=?`, capUSD, run.ID); err != nil {
					t.Fatal(err)
				}
			}
			recovered := application.NewReadOnlyFanoutExecutionService(st, router, policy.NewDefaultChecker()).WithMonetaryBudget(money)
			result, err := recovered.Execute(ctx, request)
			if !result.Recovered {
				t.Fatalf("execution was not recovered: %+v err=%v", result, err)
			}
			if tightBudget {
				if apperror.CodeOf(err) != apperror.CodeResourceExhausted || provider.calls.Load() != firstCalls {
					t.Fatalf("retry bypassed remaining budget: calls=%d initial=%d err=%v", provider.calls.Load(), firstCalls, err)
				}
			} else if err != nil || result.Execution.Status != domain.ReadOnlyFanoutExecutionCompleted || provider.calls.Load() != firstCalls+2 {
				t.Fatalf("retry did not complete independently: calls=%d initial=%d result=%+v err=%v", provider.calls.Load(), firstCalls, result, err)
			}
			after, err := money.Usage(ctx, run.ID)
			if err != nil {
				t.Fatal(err)
			}
			known := int64(provider.calls.Load()-firstCalls) * 8
			if after.SettledMicros != known || after.ReservedMicros-after.ReleasedMicros != before.ReservedMicros+known {
				t.Fatalf("recovery erased the unknown charge: before=%+v after=%+v", before, after)
			}
			if _, err := st.ReleaseOpenMonetaryReservations(ctx, run.ID); err != nil {
				t.Fatal(err)
			}
			terminal, err := money.Usage(ctx, run.ID)
			if err != nil || terminal.ReservedMicros-terminal.ReleasedMicros != before.ReservedMicros+known {
				t.Fatalf("terminal cleanup erased the unknown charge: %+v err=%v", terminal, err)
			}
			calls := provider.calls.Load()
			if replay, err := recovered.Execute(ctx, request); err != nil || !replay.Replayed || provider.calls.Load() != calls {
				t.Fatalf("terminal replay invoked a model: %+v err=%v", replay, err)
			}
		})
	}
}
