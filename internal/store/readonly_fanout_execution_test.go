package store

import (
	"context"
	"errors"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"cyberagent-workbench/internal/apperror"
	"cyberagent-workbench/internal/application"
	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/events"
	"cyberagent-workbench/internal/llm"
	"cyberagent-workbench/internal/policy"
	"cyberagent-workbench/internal/runmutation"
)

func TestReadOnlyFanoutExecutionCompletesConcurrentlyAndReplaysExactlyOnce(t *testing.T) {
	st, run, root := createReadOnlyFanoutFixture(t, "readonly-execution.db", 8)
	ctx := context.Background()
	plan := createReadOnlyFanoutExecutionPlan(t, ctx, st, run.ID, "6",
		"readonly-execution-plan")
	service := application.NewReadOnlyFanoutExecutionService(st,
		llm.NewDefaultRouter(), policy.NewDefaultChecker()).
		WithRunExecutionLeaseOwner("fanout-test-worker")
	request := application.ExecuteReadOnlyFanoutRequest{
		PlanID: plan.ID, OperationKey: "readonly-execution-0001",
		RequestedBy: "operator", MaxOutputTokensPerShard: 512,
	}
	result, err := service.Execute(ctx, request)
	if err != nil {
		t.Fatal(err)
	}
	if result.Replayed || result.Execution.Status != domain.ReadOnlyFanoutExecutionCompleted ||
		len(result.Execution.Shards) != 6 || result.UsageAfter.ReadOnlyFanoutTokens <= 0 {
		t.Fatalf("unexpected read-only execution result: %#v", result)
	}
	for _, shard := range result.Execution.Shards {
		if shard.Status != domain.ReadOnlyFanoutExecutionShardCompleted ||
			shard.AttemptCount != 1 || shard.ReportDigest == "" ||
			strings.Contains(shard.ReportJSON, "tool_calls") {
			t.Fatalf("unexpected completed shard: %#v", shard)
		}
	}
	var calls int
	if err := st.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM readonly_fanout_model_calls
		WHERE execution_id = ?`, result.Execution.ID).Scan(&calls); err != nil || calls != 6 {
		t.Fatalf("unexpected model-call count: %d err=%v", calls, err)
	}
	timeline, err := st.ListRunEvents(ctx, run.ID)
	if err != nil || countRunEventType(timeline,
		events.ReadOnlyFanoutExecutionCompletedEvent) != 1 ||
		countRunEventType(timeline, events.ReadOnlyFanoutShardCompletedEvent) != 6 {
		t.Fatalf("execution event stream is incomplete: err=%v", err)
	}
	for _, event := range timeline {
		if !strings.HasPrefix(event.Type, "readonly_fanout.") {
			continue
		}
		if strings.Contains(event.PayloadJSON, root) ||
			strings.Contains(event.PayloadJSON, "audit independent source modules") ||
			strings.Contains(event.PayloadJSON, "module-a.go") ||
			strings.Contains(event.PayloadJSON, "Mock read-only audit") {
			t.Fatalf("read-only fan-out event leaked model or workspace content: %s",
				event.PayloadJSON)
		}
	}
	replayed, err := service.Execute(ctx, request)
	if err != nil || !replayed.Replayed || replayed.Execution.ID != result.Execution.ID {
		t.Fatalf("execution replay failed: %#v err=%v", replayed, err)
	}
	if err := st.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM readonly_fanout_model_calls
		WHERE execution_id = ?`, result.Execution.ID).Scan(&calls); err != nil || calls != 6 {
		t.Fatalf("replay duplicated model calls: %d err=%v", calls, err)
	}
	var rawKeyCount int
	if err := st.db.QueryRowContext(ctx, `SELECT COUNT(*)
		FROM readonly_fanout_execution_operations
		WHERE operation_key_digest = ? OR request_fingerprint = ?`, request.OperationKey,
		request.OperationKey).Scan(&rawKeyCount); err != nil || rawKeyCount != 0 {
		t.Fatalf("raw execution operation key was stored: count=%d err=%v",
			rawKeyCount, err)
	}
	if _, err := st.db.ExecContext(ctx, `UPDATE readonly_fanout_execution_shards
		SET report_json = '{}' WHERE execution_id = ? AND ordinal = 1`,
		result.Execution.ID); err == nil {
		t.Fatal("direct terminal shard mutation succeeded")
	}
}

func TestReadOnlyFanoutExecutionBudgetDenialMakesZeroModelCalls(t *testing.T) {
	st, run, _ := createReadOnlyFanoutFixture(t, "readonly-budget.db", 8)
	ctx := context.Background()
	plan := createReadOnlyFanoutExecutionPlan(t, ctx, st, run.ID, "6",
		"readonly-budget-plan")
	provider := &fanoutCountingProvider{}
	router := llm.NewRouter(llm.ModelRef{Provider: provider.Name(), Model: "audit"})
	router.RegisterProvider(provider)
	router.SetRoute("review", llm.ModelRef{Provider: provider.Name(), Model: "audit"})
	service := application.NewReadOnlyFanoutExecutionService(st, router,
		policy.NewDefaultChecker()).WithRunExecutionLeaseOwner("fanout-budget-worker")
	_, err := service.Execute(ctx, application.ExecuteReadOnlyFanoutRequest{
		PlanID: plan.ID, OperationKey: "readonly-budget-0001",
		RequestedBy: "operator", MaxOutputTokensPerShard: 4096,
	})
	if apperror.CodeOf(err) != apperror.CodeResourceExhausted {
		t.Fatalf("budget denial code=%s err=%v", apperror.CodeOf(err), err)
	}
	if provider.calls.Load() != 0 {
		t.Fatalf("budget denial made %d model calls", provider.calls.Load())
	}
	var executions, calls int
	if err := st.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM readonly_fanout_executions
		WHERE plan_id = ?`, plan.ID).Scan(&executions); err != nil {
		t.Fatal(err)
	}
	if err := st.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM readonly_fanout_model_calls
		WHERE run_id = ?`, run.ID).Scan(&calls); err != nil {
		t.Fatal(err)
	}
	if executions != 0 || calls != 0 {
		t.Fatalf("budget denial persisted execution=%d calls=%d", executions, calls)
	}
}

func TestReadOnlyFanoutFailureCancelsAllConcurrentSiblings(t *testing.T) {
	st, run, _ := createReadOnlyFanoutFixture(t, "readonly-cancel.db", 8)
	ctx := context.Background()
	plan := createReadOnlyFanoutExecutionPlan(t, ctx, st, run.ID, "6",
		"readonly-cancel-plan")
	provider := newFanoutBarrierProvider(6)
	router := llm.NewRouter(llm.ModelRef{Provider: provider.Name(), Model: "audit"})
	router.RegisterProvider(provider)
	router.SetRoute("review", llm.ModelRef{Provider: provider.Name(), Model: "audit"})
	service := application.NewReadOnlyFanoutExecutionService(st, router,
		policy.NewDefaultChecker()).WithRunExecutionLeaseOwner("fanout-cancel-worker")
	result, err := service.Execute(ctx, application.ExecuteReadOnlyFanoutRequest{
		PlanID: plan.ID, OperationKey: "readonly-cancel-0001",
		RequestedBy: "operator", MaxOutputTokensPerShard: 512,
	})
	if err == nil || result.Execution.Status != domain.ReadOnlyFanoutExecutionFailed {
		t.Fatalf("fan-out failure did not finalize: result=%#v err=%v", result, err)
	}
	if provider.maxConcurrent.Load() < 4 || provider.calls.Load() != 6 {
		t.Fatalf("fan-out did not run at its bounded tier: calls=%d max=%d",
			provider.calls.Load(), provider.maxConcurrent.Load())
	}
	failed, cancelled := 0, 0
	for _, shard := range result.Execution.Shards {
		switch shard.Status {
		case domain.ReadOnlyFanoutExecutionShardFailed:
			failed++
		case domain.ReadOnlyFanoutExecutionShardCancelled:
			cancelled++
		default:
			t.Fatalf("non-terminal shard remained after cancellation: %#v", shard)
		}
	}
	if failed != 1 || cancelled != 5 {
		t.Fatalf("unexpected cancellation fan-out: failed=%d cancelled=%d",
			failed, cancelled)
	}
	var active int
	if err := st.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM readonly_fanout_model_calls
		WHERE execution_id = ? AND status = 'started'`, result.Execution.ID).
		Scan(&active); err != nil || active != 0 {
		t.Fatalf("active model calls remained: %d err=%v", active, err)
	}
	usage, err := st.GetRunAgentUsage(ctx, run.ID)
	if err != nil || usage.ReadOnlyFanoutTokens <= 0 || usage.TotalTokens > run.Budget.MaxTokens {
		t.Fatalf("cancelled reservation accounting is invalid: %#v err=%v", usage, err)
	}
}

func TestReadOnlyFanoutRecoveryFencesOldLeaseAndChargesUnknownCall(t *testing.T) {
	st, run, _ := createReadOnlyFanoutFixture(t, "readonly-recovery.db", 2)
	ctx := context.Background()
	plan := createReadOnlyFanoutExecutionPlan(t, ctx, st, run.ID, "2",
		"readonly-recovery-plan")
	oldLease := acquireTestRunExecutionLease(t, ctx, st, run.ID)
	now := time.Now().UTC()
	executionID := "fanout-execution-recovery"
	shards := make([]domain.ReadOnlyFanoutExecutionShard, len(plan.Shards))
	for index, planned := range plan.Shards {
		shards[index] = domain.ReadOnlyFanoutExecutionShard{
			ExecutionID: executionID, PlanID: plan.ID, Ordinal: planned.Ordinal,
			Status:      domain.ReadOnlyFanoutExecutionShardPending,
			InputDigest: planned.InputDigest, Version: 1,
			CreatedAt: now, UpdatedAt: now,
		}
	}
	execution := domain.ReadOnlyFanoutExecution{
		ID: executionID, PlanID: plan.ID, RunID: run.ID,
		WorkspaceID: plan.WorkspaceID, Status: domain.ReadOnlyFanoutExecutionRunning,
		Parallelism: plan.EffectiveParallelism, MaxOutputTokensPerShard: 512,
		SnapshotDigest: plan.SnapshotDigest, RequestedBy: plan.RequestedBy,
		Version: 1, StartedAt: now, UpdatedAt: now, Shards: shards,
	}
	operation := domain.ReadOnlyFanoutExecutionOperation{
		KeyDigest: runmutation.OperationKeyDigest("readonly_fanout_execution",
			plan.ID, "readonly-recovery-execution"),
		RequestFingerprint: runmutation.Fingerprint(
			"readonly_fanout_execution_request.v1", plan.ID, run.ID,
			plan.RequestedBy, "512"),
		ExecutionID: executionID, PlanID: plan.ID, RunID: run.ID,
		RequestedBy: plan.RequestedBy, CreatedAt: now,
	}
	if _, _, err := st.CreateReadOnlyFanoutExecution(ctx, oldLease, execution, operation,
		policy.Decision{Allowed: true, Reason: "allowed by recovery test"}); err != nil {
		t.Fatal(err)
	}
	alternate := execution
	alternate.ID = "fanout-execution-alternate"
	alternate.Shards = append([]domain.ReadOnlyFanoutExecutionShard(nil), execution.Shards...)
	for index := range alternate.Shards {
		alternate.Shards[index].ExecutionID = alternate.ID
	}
	alternateOperation := operation
	alternateOperation.ExecutionID = alternate.ID
	converged, replayed, err := st.CreateReadOnlyFanoutExecution(ctx, oldLease,
		alternate, alternateOperation,
		policy.Decision{Allowed: true, Reason: "allowed by recovery test"})
	if err != nil || !replayed || converged.ID != executionID {
		t.Fatalf("same-key execution creation did not converge: %#v replayed=%t err=%v",
			converged, replayed, err)
	}
	started, err := st.StartReadOnlyFanoutExecutionShard(ctx, oldLease, executionID, 1,
		"mock", "mock-cyber-agent", strings.Repeat("b", 64), 100, 512, 1000)
	if err != nil || started.Status != domain.ReadOnlyFanoutExecutionShardRunning {
		t.Fatalf("start failed: %#v err=%v", started, err)
	}
	if _, _, err := st.ReleaseRunExecutionLease(ctx, oldLease); err != nil {
		t.Fatal(err)
	}
	newLease := acquireTestRunExecutionLease(t, ctx, st, run.ID)
	recovered, changed, err := st.RecoverReadOnlyFanoutExecution(ctx, newLease,
		executionID)
	if err != nil || !changed || recovered.Shards[0].Status !=
		domain.ReadOnlyFanoutExecutionShardPending ||
		recovered.Shards[0].AttemptCount != 1 || recovered.Shards[0].CurrentAttempt != 0 {
		t.Fatalf("recovery failed: %#v changed=%t err=%v", recovered, changed, err)
	}
	summary, found, err := st.GetLatestReadOnlyFanoutExecutionSummary(ctx, plan.ID)
	if err != nil || !found || summary.Shards[0].AttemptCount != 1 ||
		summary.Shards[0].CurrentAttempt != 0 || summary.Validate() != nil {
		t.Fatalf("recovered pending summary is invalid: %#v found=%t err=%v", summary, found, err)
	}
	var callStatus string
	if err := st.db.QueryRowContext(ctx, `SELECT status FROM readonly_fanout_model_calls
		WHERE execution_id = ? AND shard_ordinal = 1 AND attempt_number = 1`,
		executionID).Scan(&callStatus); err != nil || callStatus != "abandoned" {
		t.Fatalf("old model call was not abandoned: %q err=%v", callStatus, err)
	}
	usage, err := st.GetRunAgentUsage(ctx, run.ID)
	if err != nil || usage.ReadOnlyFanoutTokens != 612 ||
		usage.ReadOnlyFanoutMillis != 1000 {
		t.Fatalf("unknown call reservation was not charged: %#v err=%v", usage, err)
	}
	if _, err := st.StartReadOnlyFanoutExecutionShard(ctx, oldLease, executionID, 1,
		"mock", "mock-cyber-agent", strings.Repeat("c", 64), 100, 512, 1000); apperror.CodeOf(err) != apperror.CodeConflict {
		t.Fatalf("stale lease was not fenced: code=%s err=%v", apperror.CodeOf(err), err)
	}
	current, err := st.CancelReadOnlyFanoutExecutionRemainder(ctx, newLease,
		executionID, "test_cleanup", "recovery test cleanup")
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := st.FinalizeReadOnlyFanoutExecution(ctx, newLease, current.ID,
		domain.ReadOnlyFanoutExecutionFailed, "test_cleanup"); err != nil {
		t.Fatal(err)
	}
	summary, found, err = st.GetLatestReadOnlyFanoutExecutionSummary(ctx, plan.ID)
	if err != nil || !found || summary.Shards[0].Status != domain.ReadOnlyFanoutExecutionShardCancelled ||
		summary.Shards[0].AttemptCount != 1 || summary.Shards[0].CurrentAttempt != 0 || summary.Validate() != nil {
		t.Fatalf("recovered cancelled summary is invalid: %#v found=%t err=%v", summary, found, err)
	}
	if _, _, err := st.ReleaseRunExecutionLease(ctx, newLease); err != nil {
		t.Fatal(err)
	}
}

func TestReadOnlyFanoutCancellationRecoversInterruptedExecution(t *testing.T) {
	st, run, _ := createReadOnlyFanoutFixture(t, "fanout-cancel-recovery.db", 2)
	ctx := t.Context()
	plan := createReadOnlyFanoutExecutionPlan(t, ctx, st, run.ID, "2", "cancel-recovery-plan")
	worker := application.NewReadOnlyFanoutExecutionService(&fanoutLostResultStore{st},
		llm.NewDefaultRouter(), policy.NewDefaultChecker())
	if _, err := worker.Execute(ctx, application.ExecuteReadOnlyFanoutRequest{
		PlanID: plan.ID, OperationKey: "cancel-recovery-execute", RequestedBy: "operator",
		MaxOutputTokensPerShard: 512,
	}); err == nil {
		t.Fatal("expected interrupted terminal persistence")
	}
	executions, err := st.ListReadOnlyFanoutExecutions(ctx, plan.ID, 5)
	if err != nil || len(executions) != 1 || executions[0].Status != domain.ReadOnlyFanoutExecutionRunning {
		t.Fatalf("interrupted execution missing: %#v err=%v", executions, err)
	}
	controller := application.NewReadOnlyFanoutExecutionService(st, llm.NewDefaultRouter(), policy.NewDefaultChecker())
	activeLease := acquireTestRunExecutionLease(t, ctx, st, run.ID)
	if _, err := controller.CancelReadOnlyFanoutExecution(ctx, executions[0].ID,
		"operator", "cancel-active-owner"); apperror.CodeOf(err) != apperror.CodeConflict {
		t.Fatalf("cancellation stole an active lease: %v", err)
	}
	if _, _, err := st.ReleaseRunExecutionLease(ctx, activeLease); err != nil {
		t.Fatal(err)
	}
	var version int64
	for _, key := range []string{"cancel-interrupted-worker", "cancel-interrupted-worker-replay"} {
		cancelled, err := controller.CancelReadOnlyFanoutExecution(ctx, executions[0].ID, "operator", key)
		if err != nil || cancelled.Status != domain.ReadOnlyFanoutExecutionCancelled || cancelled.Validate() != nil {
			t.Fatalf("interrupted execution did not cancel: %#v err=%v", cancelled, err)
		}
		if version != 0 && version != cancelled.Version {
			t.Fatal("repeated cancellation mutated the terminal execution")
		}
		version = cancelled.Version
		for _, shard := range cancelled.Shards {
			if shard.Status != domain.ReadOnlyFanoutExecutionShardCancelled || shard.AttemptCount != 1 || shard.CurrentAttempt != 0 {
				t.Fatalf("cancelled recovery lost attempt history: %#v", shard)
			}
		}
	}
	if _, found, err := st.GetLatestReadOnlyFanoutExecutionSummary(ctx, plan.ID); err != nil || !found {
		t.Fatalf("cancelled execution summary unavailable: found=%t err=%v", found, err)
	}
}

type fanoutLostFinalizationStore struct{ *SQLiteStore }

func (*fanoutLostFinalizationStore) FinalizeReadOnlyFanoutExecution(context.Context, domain.RunExecutionLease,
	string, domain.ReadOnlyFanoutExecutionStatus, string,
) (domain.ReadOnlyFanoutExecution, bool, error) {
	return domain.ReadOnlyFanoutExecution{}, false, errors.New("fixture lost execution finalization")
}

func TestReadOnlyFanoutCancellationPreservesCompletedShardResults(t *testing.T) {
	st, run, _ := createReadOnlyFanoutFixture(t, "fanout-completed-cancel.db", 2)
	ctx := t.Context()
	plan := createReadOnlyFanoutExecutionPlan(t, ctx, st, run.ID, "2", "completed-cancel-plan")
	worker := application.NewReadOnlyFanoutExecutionService(&fanoutLostFinalizationStore{st},
		llm.NewDefaultRouter(), policy.NewDefaultChecker())
	if _, err := worker.Execute(ctx, application.ExecuteReadOnlyFanoutRequest{
		PlanID: plan.ID, OperationKey: "completed-cancel-execute", RequestedBy: "operator",
		MaxOutputTokensPerShard: 512,
	}); err == nil {
		t.Fatal("expected lost execution finalization")
	}
	executions, err := st.ListReadOnlyFanoutExecutions(ctx, plan.ID, 5)
	if err != nil || len(executions) != 1 || executions[0].Status != domain.ReadOnlyFanoutExecutionRunning {
		t.Fatalf("interrupted execution missing: %#v err=%v", executions, err)
	}
	controller := application.NewReadOnlyFanoutExecutionService(st, llm.NewDefaultRouter(), policy.NewDefaultChecker())
	final, err := controller.CancelReadOnlyFanoutExecution(ctx, executions[0].ID, "operator", "completed-cancel")
	if err != nil || final.Status != domain.ReadOnlyFanoutExecutionCompleted || final.Validate() != nil {
		t.Fatalf("already completed shards were not finalized: %#v err=%v", final, err)
	}
	for index, shard := range final.Shards {
		if shard.ReportJSON != executions[0].Shards[index].ReportJSON ||
			shard.AttemptCount != 1 || shard.CurrentAttempt != 1 {
			t.Fatalf("cancellation changed a completed result: %#v", shard)
		}
	}
}

func TestSchemaV33PlanSurvivesReadOnlyExecutionMigration(t *testing.T) {
	databasePath := filepath.Join(t.TempDir(), "readonly-v33-upgrade.db")
	st, run, _ := createReadOnlyFanoutFixture(t, "seed.db", 4, 177)
	ctx := context.Background()
	plan := createReadOnlyFanoutExecutionPlan(t, ctx, st, run.ID, "4",
		"readonly-v33-upgrade-plan")
	st = historicalTestDatabaseFromSeed(t, st, databasePath, 33)
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	upgraded, err := Open(databasePath)
	if err != nil {
		t.Fatal(err)
	}
	defer upgraded.Close()
	if version, err := upgraded.SchemaVersion(ctx); err != nil ||
		version != LatestSchemaVersion {
		t.Fatalf("schema v33 did not upgrade: version=%d err=%v", version, err)
	}
	loaded, err := upgraded.GetReadOnlyFanoutPlan(ctx, plan.ID)
	if err != nil || loaded.SnapshotDigest != plan.SnapshotDigest {
		t.Fatalf("schema v33 plan was not preserved: %#v err=%v", loaded, err)
	}
	var executions int
	if err := upgraded.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM readonly_fanout_executions`).Scan(&executions); err != nil || executions != 0 {
		t.Fatalf("migration synthesized executions: count=%d err=%v", executions, err)
	}
}

func createReadOnlyFanoutExecutionPlan(t *testing.T, ctx context.Context,
	st *SQLiteStore, runID string, tier string, operationKey string,
) domain.ReadOnlyFanoutPlan {
	t.Helper()
	result, err := application.NewReadOnlyFanoutPlanService(st,
		policy.NewDefaultChecker()).Create(ctx, application.CreateReadOnlyFanoutPlanRequest{
		RunID: runID, Goal: "audit independent source modules", ScopePath: ".",
		Tier: tier, OperationKey: operationKey, RequestedBy: "operator",
	})
	if err != nil {
		t.Fatal(err)
	}
	return result.Plan
}

type fanoutCountingProvider struct {
	calls atomic.Int64
}

func (*fanoutCountingProvider) Name() string { return "fanout-counting" }
func (*fanoutCountingProvider) ListModels(context.Context) ([]llm.ModelInfo, error) {
	return []llm.ModelInfo{{ID: "audit", Provider: "fanout-counting"}}, nil
}
func (p *fanoutCountingProvider) Chat(context.Context, llm.ChatRequest) (*llm.ChatResponse, error) {
	p.calls.Add(1)
	return nil, errors.New("counting provider should not be called")
}
func (p *fanoutCountingProvider) StreamChat(ctx context.Context,
	req llm.ChatRequest,
) (<-chan llm.ChatChunk, error) {
	response, err := p.Chat(ctx, req)
	if err != nil {
		return nil, err
	}
	ch := make(chan llm.ChatChunk, 1)
	ch <- llm.FinalChatChunk(response)
	close(ch)
	return ch, nil
}
func (*fanoutCountingProvider) SupportsTools(string) bool    { return false }
func (*fanoutCountingProvider) SupportsVision(string) bool   { return false }
func (*fanoutCountingProvider) SupportsJSONMode(string) bool { return true }

type fanoutBarrierProvider struct {
	want          int64
	calls         atomic.Int64
	active        atomic.Int64
	maxConcurrent atomic.Int64
	ready         chan struct{}
	once          sync.Once
}

func newFanoutBarrierProvider(want int) *fanoutBarrierProvider {
	return &fanoutBarrierProvider{want: int64(want), ready: make(chan struct{})}
}

func (*fanoutBarrierProvider) Name() string { return "fanout-barrier" }
func (*fanoutBarrierProvider) ListModels(context.Context) ([]llm.ModelInfo, error) {
	return []llm.ModelInfo{{ID: "audit", Provider: "fanout-barrier"}}, nil
}
func (p *fanoutBarrierProvider) Chat(ctx context.Context,
	req llm.ChatRequest,
) (*llm.ChatResponse, error) {
	currentCalls := p.calls.Add(1)
	active := p.active.Add(1)
	defer p.active.Add(-1)
	for {
		maximum := p.maxConcurrent.Load()
		if active <= maximum || p.maxConcurrent.CompareAndSwap(maximum, active) {
			break
		}
	}
	if currentCalls == p.want {
		p.once.Do(func() { close(p.ready) })
	}
	select {
	case <-p.ready:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	ordinal, _ := strconv.Atoi(req.Metadata["fanout_shard"])
	if ordinal == 1 {
		return nil, llm.NewProviderError(llm.OutcomePermanent, p.Name(),
			"injected shard failure", nil)
	}
	<-ctx.Done()
	return nil, ctx.Err()
}
func (p *fanoutBarrierProvider) StreamChat(ctx context.Context,
	req llm.ChatRequest,
) (<-chan llm.ChatChunk, error) {
	response, err := p.Chat(ctx, req)
	if err != nil {
		return nil, err
	}
	ch := make(chan llm.ChatChunk, 1)
	ch <- llm.FinalChatChunk(response)
	close(ch)
	return ch, nil
}
func (*fanoutBarrierProvider) SupportsTools(string) bool    { return false }
func (*fanoutBarrierProvider) SupportsVision(string) bool   { return false }
func (*fanoutBarrierProvider) SupportsJSONMode(string) bool { return true }
