package store

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"cyberagent-workbench/internal/application"
	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/events"
	"cyberagent-workbench/internal/llm"
	"cyberagent-workbench/internal/policy"
	"cyberagent-workbench/internal/rootfs"
)

func TestFindingReportProjectionConvergesAcrossStores(t *testing.T) {
	st, run, _ := createReadOnlyFanoutFixture(t, "finding-report.db", 1)
	ctx := context.Background()
	execution := createFindingReportSourceExecution(t, ctx, st, run.ID,
		"finding-report-plan-0001", "finding-report-execution-0001")
	invalidTx, err := st.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := invalidTx.ExecContext(ctx, `INSERT INTO finding_reports
		(id, run_id, source_kind, source_id, protocol_version, status, title,
		version, created_at) VALUES (?, ?, ?, ?, ?, 'building', ?, 1, ?)`,
		"report-invalid-direct-write", execution.RunID,
		domain.FindingReportSourceReadOnlyFanoutExecution, execution.ID,
		domain.FindingReportProtocolVersion, "Invalid direct report",
		ts(*execution.FinishedAt)); err != nil {
		_ = invalidTx.Rollback()
		t.Fatal(err)
	}
	if _, err := invalidTx.ExecContext(ctx, `UPDATE finding_reports
		SET status = 'generated', projection_digest = ?, finding_count = 1,
		evidence_count = 2, high_count = 1, version = 2
		WHERE id = 'report-invalid-direct-write'`, strings.Repeat("f", 64)); err == nil {
		_ = invalidTx.Rollback()
		t.Fatal("direct incomplete report generation succeeded")
	}
	if err := invalidTx.Rollback(); err != nil {
		t.Fatal(err)
	}
	var sequence int
	var databaseName, databasePath string
	if err := st.db.QueryRowContext(ctx, `PRAGMA database_list`).Scan(&sequence,
		&databaseName, &databasePath); err != nil {
		t.Fatal(err)
	}
	second, err := Open(databasePath)
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()

	type outcome struct {
		report   domain.FindingReport
		replayed bool
		err      error
	}
	start := make(chan struct{})
	results := make(chan outcome, 8)
	var workers sync.WaitGroup
	stores := []*SQLiteStore{st, second}
	for index := range 8 {
		workers.Add(1)
		go func(current int) {
			defer workers.Done()
			<-start
			report, replayed, err := stores[current%len(stores)].
				EnsureReadOnlyFanoutFindingReport(ctx, execution.ID)
			results <- outcome{report: report, replayed: replayed, err: err}
		}(index)
	}
	close(start)
	workers.Wait()
	close(results)
	created := 0
	var expected domain.FindingReport
	for result := range results {
		if result.err != nil {
			t.Fatal(result.err)
		}
		if !result.replayed {
			created++
		}
		if expected.ID == "" {
			expected = result.report
		} else if result.report.ID != expected.ID ||
			result.report.ProjectionDigest != expected.ProjectionDigest {
			t.Fatalf("concurrent report projection drifted: %#v", result.report)
		}
	}
	if created != 1 || expected.FindingCount != 1 || expected.EvidenceCount != 2 ||
		expected.Severity.High != 1 || len(expected.Findings) != 1 ||
		expected.Findings[0].Status != domain.FindingStatusDraft ||
		expected.Findings[0].Severity != domain.FindingSeverityHigh ||
		expected.Findings[0].Confidence != 40 ||
		len(expected.Findings[0].Evidence) != 2 {
		t.Fatalf("unexpected converged report: created=%d report=%#v", created, expected)
	}
	summaries, err := st.ListFindingReportSummariesPage(ctx, run.ID, 0, 1)
	if err != nil || len(summaries) != 1 || summaries[0].ID != expected.ID ||
		summaries[0].FindingCount != expected.FindingCount ||
		summaries[0].Severity != expected.Severity {
		t.Fatalf("finding report summary page drifted: %#v err=%v", summaries, err)
	}
	emptySummaries, err := st.ListFindingReportSummariesPage(ctx, run.ID, 1, 1)
	if err != nil || len(emptySummaries) != 0 {
		t.Fatalf("finding report summary offset page drifted: %#v err=%v", emptySummaries, err)
	}
	executionSummary, found, err := st.GetLatestReadOnlyFanoutExecutionSummary(ctx,
		execution.PlanID)
	if err != nil || !found || executionSummary.ID != execution.ID ||
		executionSummary.Status != domain.ReadOnlyFanoutExecutionCompleted ||
		len(executionSummary.Shards) != 1 || executionSummary.Shards[0].FindingCount != 2 {
		t.Fatalf("fan-out execution summary drifted: found=%t value=%#v err=%v",
			found, executionSummary, err)
	}
	for table, want := range map[string]int{
		"finding_reports": 1, "findings": 1, "finding_evidence": 2,
	} {
		var count int
		if err := st.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM `+table).
			Scan(&count); err != nil || count != want {
			t.Fatalf("unexpected %s count=%d want=%d err=%v", table, count, want, err)
		}
	}
	timeline, err := st.ListRunEvents(ctx, run.ID)
	if err != nil || countRunEventType(timeline, events.FindingReportGeneratedEvent) != 1 {
		t.Fatalf("report event count drifted: err=%v", err)
	}
	for _, event := range timeline {
		if event.Type != events.FindingReportGeneratedEvent {
			continue
		}
		if strings.Contains(event.PayloadJSON, "Unchecked boundary") ||
			strings.Contains(event.PayloadJSON, "src/module-a.go") ||
			strings.Contains(event.PayloadJSON, "input boundary") {
			t.Fatalf("report event leaked finding content: %s", event.PayloadJSON)
		}
	}
	if _, err := st.db.ExecContext(ctx, `UPDATE findings SET severity = 'critical'
		WHERE report_id = ?`, expected.ID); err == nil {
		t.Fatal("terminal finding mutation succeeded")
	}
	if _, err := st.db.ExecContext(ctx, `DELETE FROM finding_reports WHERE id = ?`,
		expected.ID); err == nil {
		t.Fatal("terminal report deletion succeeded")
	}
	loaded, err := second.GetFindingReport(ctx, expected.ID)
	if err != nil || loaded.ProjectionDigest != expected.ProjectionDigest {
		t.Fatalf("cross-store report read drifted: %#v err=%v", loaded, err)
	}
}

func TestSchemaV34ExecutionSurvivesFindingReportMigration(t *testing.T) {
	databasePath := filepath.Join(t.TempDir(), "finding-report-v34.db")
	st, run, _ := createReadOnlyFanoutFixture(t, "seed.db", 1, 177)
	restoreLegacyContext := addEmptyCurrentContextForLegacySeed(t, st)
	ctx := context.Background()
	execution := createFindingReportSourceExecution(t, ctx, st, run.ID,
		"finding-report-v34-plan", "finding-report-v34-execution")
	restoreLegacyContext()
	st = historicalTestDatabaseFromSeed(t, st, databasePath, 34)
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
		t.Fatalf("schema v34 did not upgrade: version=%d err=%v", version, err)
	}
	loaded, err := upgraded.GetReadOnlyFanoutExecution(ctx, execution.ID)
	if err != nil || loaded.Status != domain.ReadOnlyFanoutExecutionCompleted {
		t.Fatalf("schema v34 execution was not preserved: %#v err=%v", loaded, err)
	}
	var reports int
	if err := upgraded.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM finding_reports`).
		Scan(&reports); err != nil || reports != 0 {
		t.Fatalf("migration synthesized reports: count=%d err=%v", reports, err)
	}
}

func TestSchemaV232PreservesExistingReportsAndExactFanoutPaths(t *testing.T) {
	st, run, root := createReadOnlyFanoutFixture(t, "finding-report-v231.db", 1, 231)
	ctx := t.Context()
	execution := createFindingReportSourceExecution(t, ctx, st, run.ID,
		"historical-finding-plan", "historical-finding-execution")
	original, _, err := st.EnsureReadOnlyFanoutFindingReport(ctx, execution.ID)
	if err != nil {
		t.Fatal(err)
	}
	var databasePath string
	if err := st.db.QueryRowContext(ctx, "SELECT file FROM pragma_database_list WHERE name = 'main'").
		Scan(&databasePath); err != nil {
		t.Fatal(err)
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	upgraded, err := Open(databasePath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = upgraded.Close() })
	loaded, err := upgraded.GetFindingReport(ctx, original.ID)
	if err != nil || loaded.ProjectionDigest != original.ProjectionDigest || len(loaded.Findings) != 1 ||
		loaded.Findings[0].RelativePath != "src/module-a.go" || len(loaded.Findings[0].Evidence) != 2 {
		t.Fatalf("historical report was not preserved: %#v, %v", loaded, err)
	}

	const scope = " src "
	const relative = scope + "/module-a.go "
	dir, err := rootfs.ExactAbsolutePath(filepath.Join(root, scope))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.RemoveAll(dir); err != nil {
			t.Error(err)
		}
	})
	writeReadOnlyFanoutFile(t, root, relative, "package source\n// exact path\n")
	planned, err := application.NewReadOnlyFanoutPlanService(upgraded, policy.NewDefaultChecker()).
		Create(ctx, application.CreateReadOnlyFanoutPlanRequest{
			RunID: run.ID, Goal: "review source", ScopePath: scope, Tier: "1",
			OperationKey: "exact-finding-plan", RequestedBy: "operator",
		})
	if err != nil {
		t.Fatal(err)
	}
	plan, err := upgraded.GetReadOnlyFanoutPlan(ctx, planned.Plan.ID)
	if err != nil || plan.ScopePath != scope || len(plan.Files) != 1 ||
		plan.Files[0].RelativePath != relative {
		t.Fatalf("plan did not preserve the exact path: %#v, %v", plan, err)
	}
	execution = executeFindingReportSource(t, ctx, upgraded, plan.ID, "exact-finding-execution", relative)
	if execution.Status != domain.ReadOnlyFanoutExecutionCompleted {
		t.Fatalf("execution for exact path did not complete: %#v", execution)
	}
	report, _, err := upgraded.EnsureReadOnlyFanoutFindingReport(ctx, execution.ID)
	if err != nil || len(report.Findings) != 1 || report.Findings[0].RelativePath != relative ||
		len(report.Findings[0].Evidence) != 2 || report.Findings[0].Evidence[0].RelativePath != relative ||
		report.Findings[0].Evidence[1].RelativePath != relative {
		t.Fatalf("report did not preserve the exact path: %#v, %v", report, err)
	}
}

func createFindingReportSourceExecution(t *testing.T, ctx context.Context,
	st *SQLiteStore, runID string, planKey string, executionKey string,
) domain.ReadOnlyFanoutExecution {
	t.Helper()
	plan := createReadOnlyFanoutExecutionPlan(t, ctx, st, runID, "1", planKey)
	return executeFindingReportSource(t, ctx, st, plan.ID, executionKey, "src/module-a.go")
}

func executeFindingReportSource(t *testing.T, ctx context.Context,
	st *SQLiteStore, planID, executionKey, sourcePath string,
) domain.ReadOnlyFanoutExecution {
	t.Helper()
	provider := &findingReportProvider{sourcePath: sourcePath}
	router := llm.NewRouter(llm.ModelRef{Provider: provider.Name(), Model: "audit"})
	router.RegisterProvider(provider)
	router.SetRoute("review", llm.ModelRef{Provider: provider.Name(), Model: "audit"})
	result, err := application.NewReadOnlyFanoutExecutionService(st, router,
		policy.NewDefaultChecker()).WithRunExecutionLeaseOwner("finding-report-worker").
		Execute(ctx, application.ExecuteReadOnlyFanoutRequest{
			PlanID: planID, OperationKey: executionKey,
			RequestedBy: "operator", MaxOutputTokensPerShard: 512,
		})
	if err != nil {
		t.Fatal(err)
	}
	return result.Execution
}

type findingReportProvider struct {
	sourcePath string
}

func (*findingReportProvider) Name() string { return "finding-report" }
func (*findingReportProvider) ListModels(context.Context) ([]llm.ModelInfo, error) {
	return []llm.ModelInfo{{ID: "audit", Provider: "finding-report"}}, nil
}
func (p *findingReportProvider) Chat(context.Context,
	llm.ChatRequest,
) (*llm.ChatResponse, error) {
	encoded, _ := json.Marshal(domain.ReadOnlyFanoutReport{
		Version: domain.ReadOnlyFanoutReportVersion,
		Summary: "Two duplicate assertions with conservative confidence.",
		Findings: []domain.ReadOnlyFanoutFinding{
			{
				Severity: domain.ReadOnlyFindingHigh, Category: "correctness",
				Title: "Unchecked boundary", Detail: "The input boundary is unchecked.",
				Path: p.sourcePath, LineStart: 1, LineEnd: 2, Confidence: 85,
			},
			{
				Severity: domain.ReadOnlyFindingHigh, Category: "correctness",
				Title: "Unchecked boundary", Detail: "The input boundary is unchecked.",
				Path: p.sourcePath, LineStart: 1, LineEnd: 2, Confidence: 40,
			},
		},
	})
	return &llm.ChatResponse{
		Text: string(encoded), Provider: "finding-report", Model: "audit",
		Usage: llm.Usage{InputTokens: 10, OutputTokens: 20, TotalTokens: 30},
	}, nil
}
func (p *findingReportProvider) StreamChat(ctx context.Context,
	req llm.ChatRequest,
) (<-chan llm.ChatChunk, error) {
	response, err := p.Chat(ctx, req)
	if err != nil {
		return nil, err
	}
	result := make(chan llm.ChatChunk, 1)
	result <- llm.FinalChatChunk(response)
	close(result)
	return result, nil
}
func (*findingReportProvider) SupportsTools(string) bool    { return false }
func (*findingReportProvider) SupportsVision(string) bool   { return false }
func (*findingReportProvider) SupportsJSONMode(string) bool { return true }
