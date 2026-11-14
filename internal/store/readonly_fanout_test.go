package store

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"cyberagent-workbench/internal/apperror"
	"cyberagent-workbench/internal/application"
	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/events"
	"cyberagent-workbench/internal/policy"
	"cyberagent-workbench/internal/rootfs"
	"cyberagent-workbench/internal/tools"
)

func TestReadOnlyFanoutPlanIsImmutableIdempotentAndDoesNotCreateAgents(t *testing.T) {
	st, run, root := createReadOnlyFanoutFixture(t, "readonly-plan.db", 8)
	ctx := context.Background()
	service := application.NewReadOnlyFanoutPlanService(st, policy.NewDefaultChecker())
	request := application.CreateReadOnlyFanoutPlanRequest{
		RunID: run.ID, Goal: "audit independent source modules", ScopePath: ".",
		Tier: "6", OperationKey: "readonly-fanout-plan-0001", RequestedBy: "operator",
	}
	result, err := service.Create(ctx, request)
	if err != nil {
		t.Fatal(err)
	}
	if result.Replayed || result.Plan.EffectiveParallelism != 6 ||
		result.Plan.ShardCount != 6 || result.Plan.FileCount != 8 ||
		result.Plan.Status != domain.ReadOnlyFanoutPlanned {
		t.Fatalf("unexpected plan result: %#v", result)
	}
	if result.Plan.Goal != request.Goal || result.Plan.ScopePath != "." ||
		result.Plan.CapabilityFingerprint == "" || result.Plan.SnapshotDigest == "" {
		t.Fatalf("plan metadata is incomplete: %#v", result.Plan)
	}
	loaded, err := st.GetReadOnlyFanoutPlan(ctx, result.Plan.ID)
	if err != nil || loaded.SnapshotDigest != result.Plan.SnapshotDigest {
		t.Fatalf("stored plan drifted: %#v err=%v", loaded, err)
	}
	listed, err := st.ListReadOnlyFanoutPlans(ctx, run.ID, 10)
	if err != nil || len(listed) != 1 || listed[0].ID != result.Plan.ID {
		t.Fatalf("plan list drifted: %#v err=%v", listed, err)
	}
	summaries, err := st.ListReadOnlyFanoutPlanSummariesPage(ctx, run.ID, 0, 1)
	if err != nil || len(summaries) != 1 || summaries[0].ID != result.Plan.ID ||
		summaries[0].FileCount != result.Plan.FileCount ||
		summaries[0].EffectiveParallelism != result.Plan.EffectiveParallelism {
		t.Fatalf("plan summary page drifted: %#v err=%v", summaries, err)
	}
	emptySummaries, err := st.ListReadOnlyFanoutPlanSummariesPage(ctx, run.ID, 1, 1)
	if err != nil || len(emptySummaries) != 0 {
		t.Fatalf("plan summary offset page drifted: %#v err=%v", emptySummaries, err)
	}
	if execution, found, err := st.GetLatestReadOnlyFanoutExecutionSummary(ctx,
		result.Plan.ID); err != nil || found || execution.ID != "" {
		t.Fatalf("planning synthesized an execution summary: found=%t value=%#v err=%v",
			found, execution, err)
	}
	replayed, err := service.Create(ctx, request)
	if err != nil || !replayed.Replayed || replayed.Plan.ID != result.Plan.ID {
		t.Fatalf("plan replay failed: %#v err=%v", replayed, err)
	}
	changed := request
	changed.Goal = "different audit intent"
	if _, err := service.Create(ctx, changed); apperror.CodeOf(err) != apperror.CodeConflict {
		t.Fatalf("changed intent reused operation key: code=%s err=%v",
			apperror.CodeOf(err), err)
	}
	nodes, err := st.ListAgentNodes(ctx, run.ID)
	if err != nil || len(nodes) != 1 || nodes[0].Role != domain.AgentRoleRoot {
		t.Fatalf("read-only plan changed Agent graph: %#v err=%v", nodes, err)
	}
	var attempts, schedules int
	if err := st.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM agent_attempts WHERE run_id = ?`, run.ID).Scan(&attempts); err != nil {
		t.Fatal(err)
	}
	if err := st.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM specialist_schedules WHERE run_id = ?`, run.ID).Scan(&schedules); err != nil {
		t.Fatal(err)
	}
	if attempts != 0 || schedules != 0 {
		t.Fatalf("read-only planning started execution: attempts=%d schedules=%d",
			attempts, schedules)
	}
	timeline, err := st.ListRunEvents(ctx, run.ID)
	if err != nil || countRunEventType(timeline, events.ReadOnlyFanoutPlannedEvent) != 1 {
		t.Fatalf("planned event is missing: %#v err=%v", timeline, err)
	}
	for _, event := range timeline {
		if event.Type == events.ReadOnlyFanoutPlannedEvent &&
			(strings.Contains(event.PayloadJSON, request.Goal) ||
				strings.Contains(event.PayloadJSON, root)) {
			t.Fatalf("planned event leaked goal or workspace root: %s", event.PayloadJSON)
		}
	}
	var rawKeyCount int
	if err := st.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM readonly_fanout_operations
		WHERE operation_key_digest = ? OR request_fingerprint = ?`, request.OperationKey,
		request.OperationKey).Scan(&rawKeyCount); err != nil {
		t.Fatal(err)
	}
	if rawKeyCount != 0 {
		t.Fatal("raw fan-out operation key was persisted")
	}
	if _, err := st.db.ExecContext(ctx, `UPDATE readonly_fanout_plans
		SET goal = 'tampered' WHERE id = ?`, result.Plan.ID); err == nil {
		t.Fatal("direct plan mutation succeeded")
	}
	if _, err := st.db.ExecContext(ctx, `DELETE FROM readonly_fanout_files
		WHERE plan_id = ? AND ordinal = 1`, result.Plan.ID); err == nil {
		t.Fatal("direct manifest deletion succeeded")
	}
}

func TestReadOnlyFanoutPlanConvergesAcrossStores(t *testing.T) {
	path := filepath.Join(t.TempDir(), "readonly-concurrent.db")
	st, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	other, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	ctx := context.Background()
	root := t.TempDir()
	workspaceID := "ws-readonly-concurrent"
	if err := st.SaveWorkspace(ctx, WorkspaceRecord{
		ID: workspaceID, Name: "readonly-concurrent", RootPath: root,
		CreatedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatal(err)
	}
	for index := range 12 {
		writeReadOnlyFanoutFile(t, root, filepath.Join("src", "file-"+string(rune('a'+index))+".go"),
			"package source\n")
	}
	_, run, err := application.NewRunService(st).Create(ctx, application.CreateRunRequest{
		Goal: "concurrent read-only planning", Profile: "review", WorkspaceID: workspaceID,
		Budget: domain.Budget{MaxTurns: 20, MaxTokens: 20_000},
	})
	if err != nil {
		t.Fatal(err)
	}
	if run, err = application.NewRunService(st).Start(ctx, run.ID); err != nil {
		t.Fatal(err)
	}
	services := []*application.ReadOnlyFanoutPlanService{
		application.NewReadOnlyFanoutPlanService(st, policy.NewDefaultChecker()),
		application.NewReadOnlyFanoutPlanService(other, policy.NewDefaultChecker()),
	}
	request := application.CreateReadOnlyFanoutPlanRequest{
		RunID: run.ID, Goal: "fan out immutable review", ScopePath: ".", Tier: "6",
		OperationKey: "readonly-fanout-concurrent", RequestedBy: "operator",
	}
	const workers = 8
	type workerResult struct {
		result application.CreateReadOnlyFanoutPlanResult
		err    error
	}
	results := make(chan workerResult, workers)
	var wg sync.WaitGroup
	for index := range workers {
		wg.Add(1)
		go func(service *application.ReadOnlyFanoutPlanService) {
			defer wg.Done()
			result, err := service.Create(ctx, request)
			results <- workerResult{result: result, err: err}
		}(services[index%len(services)])
	}
	wg.Wait()
	close(results)
	ids := map[string]bool{}
	for current := range results {
		if current.err != nil {
			t.Fatal(current.err)
		}
		ids[current.result.Plan.ID] = true
	}
	if len(ids) != 1 {
		t.Fatalf("concurrent plan ids diverged: %#v", ids)
	}
	plans, err := st.ListReadOnlyFanoutPlans(ctx, run.ID, 10)
	if err != nil || len(plans) != 1 || plans[0].ShardCount != 6 {
		t.Fatalf("concurrent plans did not converge: %#v err=%v", plans, err)
	}
	timeline, err := st.ListRunEvents(ctx, run.ID)
	if err != nil || countRunEventType(timeline, events.ReadOnlyFanoutPlannedEvent) != 1 {
		t.Fatalf("concurrent event count drifted: %#v err=%v", timeline, err)
	}
}

func TestReadOnlyFanoutPolicyDenialAndSchemaV32Upgrade(t *testing.T) {
	st, run, _ := createReadOnlyFanoutFixture(t, "readonly-denial.db", 2)
	ctx := context.Background()
	service := application.NewReadOnlyFanoutPlanService(st, denyingReadOnlyFanoutChecker{})
	_, err := service.Create(ctx, application.CreateReadOnlyFanoutPlanRequest{
		RunID: run.ID, Goal: "blocked local review", Tier: "2",
		OperationKey: "readonly-fanout-denied", RequestedBy: "operator",
	})
	if apperror.CodeOf(err) != apperror.CodePolicyDenied {
		t.Fatalf("Policy denial was not enforced: code=%s err=%v", apperror.CodeOf(err), err)
	}
	plans, err := st.ListReadOnlyFanoutPlans(ctx, run.ID, 10)
	if err != nil || len(plans) != 0 {
		t.Fatalf("Policy denial created a plan: %#v err=%v", plans, err)
	}

	path := filepath.Join(t.TempDir(), "readonly-upgrade.db")
	upgrade := openHistoricalTestDatabase(t, filepath.Join(t.TempDir(), "seed.db"), 177)

	_, existingRun := createWorkItemTestRun(t, ctx, upgrade, "schema v32 preserved Run")
	upgrade = historicalTestDatabaseFromSeed(t, upgrade, path, 32)
	if err := upgrade.Close(); err != nil {
		t.Fatal(err)
	}
	upgrade, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer upgrade.Close()
	if version, err := upgrade.SchemaVersion(ctx); err != nil || version != LatestSchemaVersion {
		t.Fatalf("schema v32 did not upgrade: version=%d err=%v", version, err)
	}
	if _, err := upgrade.GetRun(ctx, existingRun.ID); err != nil {
		t.Fatalf("schema v32 Run was not preserved: %v", err)
	}
	var count int
	if err := upgrade.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM readonly_fanout_plans`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("migration synthesized plans: count=%d err=%v", count, err)
	}
}

func createReadOnlyFanoutFixture(t *testing.T, databaseName string,
	fileCount int, historicalVersion ...int,
) (*SQLiteStore, domain.Run, string) {
	t.Helper()
	var st *SQLiteStore
	var err error
	if len(historicalVersion) == 0 {
		st, err = Open(filepath.Join(t.TempDir(), databaseName))
	} else {
		st = openHistoricalTestDatabase(t, filepath.Join(t.TempDir(), databaseName), historicalVersion[0])
	}
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	ctx := context.Background()
	root := t.TempDir()
	workspaceID := "ws-" + strings.TrimSuffix(databaseName, filepath.Ext(databaseName))
	if err := st.SaveWorkspace(ctx, WorkspaceRecord{
		ID: workspaceID, Name: strings.TrimPrefix(workspaceID, "ws-"), RootPath: root,
		CreatedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatal(err)
	}
	for index := range fileCount {
		name := filepath.Join("src", "module-"+string(rune('a'+index))+".go")
		writeReadOnlyFanoutFile(t, root, name, "package source\n// bounded fixture\n")
	}
	_, run, err := newMigrationFixtureRunService(t, st).Create(ctx, application.CreateRunRequest{
		Goal: "read-only fan-out fixture", Profile: "review", WorkspaceID: workspaceID,
		Budget: domain.Budget{MaxTurns: 20, MaxTokens: 20_000},
	})
	if err != nil {
		t.Fatal(err)
	}
	run, err = application.NewRunService(st).Start(ctx, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	return st, run, root
}

func writeReadOnlyFanoutFile(t *testing.T, root, relative, content string) {
	t.Helper()
	full, err := rootfs.ExactAbsolutePath(filepath.Join(root, relative))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

type denyingReadOnlyFanoutChecker struct{}

func (denyingReadOnlyFanoutChecker) CheckText(context, text string) policy.Decision {
	return policy.Decision{Allowed: false, Risk: "high", Reason: "denied by test Policy"}
}

func (denyingReadOnlyFanoutChecker) CheckToolCall(call tools.Call) policy.Decision {
	return policy.Decision{Allowed: false, Risk: "high", Reason: "denied by test Policy"}
}
