package store

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"cyberagent-workbench/internal/application"
	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/events"
	"cyberagent-workbench/internal/llm"
	"cyberagent-workbench/internal/policy"
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

func createFindingReportSourceExecution(t *testing.T, ctx context.Context,
	st *SQLiteStore, runID string, planKey string, executionKey string,
) domain.ReadOnlyFanoutExecution {
	t.Helper()
	plan := createReadOnlyFanoutExecutionPlan(t, ctx, st, runID, "1", planKey)
	provider := &findingReportProvider{}
	router := llm.NewRouter(llm.ModelRef{Provider: provider.Name(), Model: "audit"})
	router.RegisterProvider(provider)
	router.SetRoute("review", llm.ModelRef{Provider: provider.Name(), Model: "audit"})
	result, err := application.NewReadOnlyFanoutExecutionService(st, router,
		policy.NewDefaultChecker()).WithRunExecutionLeaseOwner("finding-report-worker").
		Execute(ctx, application.ExecuteReadOnlyFanoutRequest{
			PlanID: plan.ID, OperationKey: executionKey,
			RequestedBy: "operator", MaxOutputTokensPerShard: 512,
		})
	if err != nil {
		t.Fatal(err)
	}
	return result.Execution
}

type findingReportProvider struct{}

func (*findingReportProvider) Name() string { return "finding-report" }
func (*findingReportProvider) ListModels(context.Context) ([]llm.ModelInfo, error) {
	return []llm.ModelInfo{{ID: "audit", Provider: "finding-report"}}, nil
}
func (*findingReportProvider) Chat(context.Context,
	llm.ChatRequest,
) (*llm.ChatResponse, error) {
	encoded, _ := json.Marshal(domain.ReadOnlyFanoutReport{
		Version: domain.ReadOnlyFanoutReportVersion,
		Summary: "Two duplicate assertions with conservative confidence.",
		Findings: []domain.ReadOnlyFanoutFinding{
			{
				Severity: domain.ReadOnlyFindingHigh, Category: "correctness",
				Title: "Unchecked boundary", Detail: "The input boundary is unchecked.",
				Path: "src/module-a.go", LineStart: 1, LineEnd: 2, Confidence: 85,
			},
			{
				Severity: domain.ReadOnlyFindingHigh, Category: "correctness",
				Title: "Unchecked boundary", Detail: "The input boundary is unchecked.",
				Path: "src/module-a.go", LineStart: 1, LineEnd: 2, Confidence: 40,
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
