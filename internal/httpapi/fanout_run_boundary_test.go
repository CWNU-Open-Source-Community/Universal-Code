package httpapi

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"cyberagent-workbench/internal/application"
	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/llm"
	"cyberagent-workbench/internal/policy"
	"cyberagent-workbench/internal/runmutation"
	"cyberagent-workbench/internal/store"
)

const (
	auditFanoutExecutionID = "fanout-execution-audit-boundary"
	auditFanoutOwnerPath   = "workspace-fanout-owner"
)

// fanoutBoundaryFixture owns a dedicated Workspace and Run that carry one
// read-only fan-out plan plus a running execution. Every fan-out route below is
// addressed through the unrelated apiFixture Run so that only the Run boundary
// check can decide the outcome.
func fanoutBoundaryFixture(t *testing.T, fixture *apiFixture) (domain.Run, string) {
	t.Helper()
	ctx := t.Context()
	ownerWorkspace := store.WorkspaceRecord{ID: auditFanoutOwnerPath,
		Name: "fanout-owner", RootPath: t.TempDir(), CreatedAt: time.Now().UTC()}
	for _, name := range []string{"module-a.go", "module-b.go"} {
		if err := os.WriteFile(filepath.Join(ownerWorkspace.RootPath, name),
			[]byte("package source\n// bounded fixture\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := fixture.store.SaveWorkspace(ctx, ownerWorkspace); err != nil {
		t.Fatal(err)
	}
	runs := application.NewRunService(fixture.store)
	_, created, err := runs.Create(ctx, application.CreateRunRequest{
		Goal: "own the read-only fan-out execution", Profile: "review",
		ModelRoute: "review", WorkspaceID: ownerWorkspace.ID,
		Budget: domain.Budget{MaxTurns: 8, MaxTokens: 20_000},
	})
	if err != nil {
		t.Fatal(err)
	}
	ownerRun, err := runs.Start(ctx, created.ID)
	if err != nil {
		t.Fatal(err)
	}
	planned, err := application.NewReadOnlyFanoutPlanService(fixture.store,
		policy.NewDefaultChecker()).Create(ctx, application.CreateReadOnlyFanoutPlanRequest{
		RunID: ownerRun.ID, Goal: "audit independent source modules", ScopePath: ".",
		Tier: "2", OperationKey: "audit-fanout-boundary-plan", RequestedBy: "operator",
	})
	if err != nil {
		t.Fatal(err)
	}
	plan := planned.Plan

	now := time.Now().UTC()
	shards := make([]domain.ReadOnlyFanoutExecutionShard, len(plan.Shards))
	for index, item := range plan.Shards {
		shards[index] = domain.ReadOnlyFanoutExecutionShard{
			ExecutionID: auditFanoutExecutionID, PlanID: plan.ID, Ordinal: item.Ordinal,
			Status:      domain.ReadOnlyFanoutExecutionShardPending,
			InputDigest: item.InputDigest, Version: 1,
			CreatedAt: now, UpdatedAt: now,
		}
	}
	execution := domain.ReadOnlyFanoutExecution{
		ID: auditFanoutExecutionID, PlanID: plan.ID, RunID: ownerRun.ID,
		WorkspaceID: plan.WorkspaceID, Status: domain.ReadOnlyFanoutExecutionRunning,
		Parallelism: plan.EffectiveParallelism, MaxOutputTokensPerShard: 512,
		SnapshotDigest: plan.SnapshotDigest, RequestedBy: plan.RequestedBy,
		Version: 1, StartedAt: now, UpdatedAt: now, Shards: shards,
	}
	operation := domain.ReadOnlyFanoutExecutionOperation{
		KeyDigest: runmutation.OperationKeyDigest("readonly_fanout_execution",
			plan.ID, "audit-fanout-boundary-execution"),
		RequestFingerprint: runmutation.Fingerprint(
			"readonly_fanout_execution_request.v1", plan.ID, ownerRun.ID,
			plan.RequestedBy, "512"),
		ExecutionID: auditFanoutExecutionID, PlanID: plan.ID, RunID: ownerRun.ID,
		RequestedBy: plan.RequestedBy, CreatedAt: now,
	}
	lease, err := fixture.store.AcquireRunExecutionLease(ctx,
		domain.AcquireRunExecutionLeaseRequest{RunID: ownerRun.ID,
			OwnerID: "audit-fanout-worker", TTL: time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := fixture.store.CreateReadOnlyFanoutExecution(ctx, lease.Lease,
		execution, operation,
		policy.Decision{Allowed: true, Reason: "allowed by boundary test"}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := fixture.store.ReleaseRunExecutionLease(ctx, lease.Lease); err != nil {
		t.Fatal(err)
	}
	fixture.api.fanoutExecutionController = application.NewReadOnlyFanoutExecutionService(
		fixture.store, llm.NewDefaultRouter(), policy.NewDefaultChecker()).
		WithRunExecutionLeaseOwner("audit-fanout-worker")
	return ownerRun, plan.ID
}

func cancelFanoutExecution(fixture *apiFixture, runID string) *httptest.ResponseRecorder {
	request := httptest.NewRequest(http.MethodPost, "http://127.0.0.1"+
		"/api/v1/runs/"+runID+"/fanout-executions/"+auditFanoutExecutionID+"/cancel",
		strings.NewReader(`{"version":"readonly_fanout_cancel.v1","confirm_cancel":true}`))
	request.Host = "127.0.0.1:8765"
	request.RemoteAddr = "127.0.0.1:45000"
	request.Header.Set("Authorization", "Bearer "+testControlToken)
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	fixture.api.ServeHTTP(response, request)
	return response
}

// The documented route /api/v1/runs/{run_id}/fanout-executions/{execution_id}/cancel
// checks the owning Run only AFTER ReadOnlyFanoutExecutionService.Cancel has
// recovered, cancelled and finalized the execution. A cancellation addressed to
// an unrelated Run therefore destroys the other Run's execution while the caller
// is told 404 "fan-out execution is not part of the run".
func TestFanoutExecutionCancelEnforcesRunBoundaryBeforeMutating(t *testing.T) {
	fixture := newAPIFixture(t)
	ownerRun, _ := fanoutBoundaryFixture(t, fixture)

	response := cancelFanoutExecution(fixture, fixture.run.ID)
	if response.Code != http.StatusNotFound {
		t.Fatalf("cross-run cancellation status=%d body=%s", response.Code, response.Body.String())
	}
	current, err := fixture.store.GetReadOnlyFanoutExecution(t.Context(), auditFanoutExecutionID)
	if err != nil {
		t.Fatalf("owner Run execution disappeared: %v", err)
	}
	if current.Status != domain.ReadOnlyFanoutExecutionRunning {
		t.Fatalf("cancellation crossed the documented Run boundary before the identity check: "+
			"execution %q owned by %q became %q while the caller addressed %q and received %d",
			current.ID, ownerRun.ID, current.Status, fixture.run.ID, response.Code)
	}
}

// The same missing pre-check on the read route returns another Run's execution
// history as a 200 body for this Run's collection.
func TestFanoutExecutionListEnforcesRunBoundary(t *testing.T) {
	fixture := newAPIFixture(t)
	_, planID := fanoutBoundaryFixture(t, fixture)

	response := fixture.get(t, "/api/v1/runs/"+fixture.run.ID+"/fanout-executions?plan_id="+planID)
	if response.Code != http.StatusNotFound ||
		strings.Contains(response.Body.String(), auditFanoutExecutionID) {
		t.Fatalf("run-scoped collection served execution history owned by another Run "+
			"through plan %q: %d %s", planID, response.Code, response.Body.String())
	}
}

// The owner Run keeps both documented routes: the plan lookup must not reject a
// matching binding, and cancellation must stay idempotent for its own Run.
func TestFanoutExecutionRoutesServeTheirOwnRun(t *testing.T) {
	fixture := newAPIFixture(t)
	ownerRun, planID := fanoutBoundaryFixture(t, fixture)

	listed := fixture.get(t, "/api/v1/runs/"+ownerRun.ID+"/fanout-executions?plan_id="+planID)
	if listed.Code != http.StatusOK || !strings.Contains(listed.Body.String(), auditFanoutExecutionID) {
		t.Fatalf("own-run list regressed: %d %s", listed.Code, listed.Body.String())
	}
	cancelled := cancelFanoutExecution(fixture, ownerRun.ID)
	if cancelled.Code != http.StatusOK {
		t.Fatalf("own-run cancellation regressed: %d %s", cancelled.Code, cancelled.Body.String())
	}
	current, err := fixture.store.GetReadOnlyFanoutExecution(t.Context(), auditFanoutExecutionID)
	if err != nil || current.Status != domain.ReadOnlyFanoutExecutionCancelled {
		t.Fatalf("own-run execution did not reach its cancelled state: %#v err=%v", current, err)
	}
	repeat := cancelFanoutExecution(fixture, ownerRun.ID)
	if repeat.Code != http.StatusOK {
		t.Fatalf("terminal cancellation is no longer idempotent: %d %s", repeat.Code, repeat.Body.String())
	}
}
