package httpapi

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"cyberagent-workbench/internal/application"
	"cyberagent-workbench/internal/domain"
)

func prepareOpenAPIWorkflowTarget(t *testing.T, f *apiFixture) (*application.WorkflowService, domain.Run, domain.WorkflowDefinition, domain.WorkflowExecution) {
	t.Helper()
	service := application.NewWorkflowService(f.store, application.NewSpecialistExecutionService(f.store, nil, nil))
	f.api.workflowController = service
	_, created, err := application.NewRunService(f.store).Create(t.Context(), application.CreateRunRequest{
		Goal: "OpenAPI workflow target", Profile: "code", Budget: domain.Budget{MaxTurns: 4},
	})
	if err != nil {
		t.Fatal(err)
	}
	run, err := application.NewRunService(f.store).Start(t.Context(), created.ID)
	if err != nil {
		t.Fatal(err)
	}
	definition, err := service.Save(t.Context(), domain.WorkflowSpec{
		Version: domain.WorkflowVersion, Name: "OpenAPI workflow",
		Limits: domain.WorkflowLimits{MaxSteps: 8, MaxJobs: 2, MaxTokens: 1000, MaxTurns: 4, TimeoutMillis: 60000},
		Steps: []domain.WorkflowStep{{ID: "inspect", Kind: "worker", Task: &domain.ChildTask{
			Title: "inspect", Goal: "inspect a file", Skills: []string{"model.chat"}, TurnLimit: 1, TokenLimit: 100, TimeoutMillis: 10000,
		}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	execution, err := service.Start(t.Context(), application.StartWorkflowRequest{
		RunID: run.ID, DefinitionID: definition.ID, OperationKey: "openapi-workflow-initial-target",
	})
	if err != nil {
		t.Fatal(err)
	}
	return service, run, definition, execution
}

func TestWorkflowHTTPPersistsScopesAndControlsExecution(t *testing.T) {
	f := newAPIFixture(t)
	f.api.modelControlEnabled = true
	f.api.workflowController = application.NewWorkflowService(f.store, application.NewSpecialistExecutionService(f.store, nil, nil))
	spec := domain.WorkflowSpec{Version: domain.WorkflowVersion, Name: "HTTP workflow", Limits: domain.WorkflowLimits{MaxSteps: 8, MaxJobs: 2, MaxTokens: 1000, MaxTurns: 4, TimeoutMillis: 60000}, Steps: []domain.WorkflowStep{{ID: "inspect", Kind: "worker", Task: &domain.ChildTask{Title: "inspect", Goal: "inspect a file", Skills: []string{"model.chat"}, TurnLimit: 1, TokenLimit: 100, TimeoutMillis: 10000}}}}
	raw, _ := json.Marshal(spec)
	saved := performControlPathRequest(t, f.api, "/api/v1/workflows", "workflow-http-save-first", strings.NewReader(string(raw)))
	var definition domain.WorkflowDefinition
	decodeDataStatus(t, saved, 200, &definition)
	var listed WorkflowDefinitionsView
	decodeDataStatus(t, f.get(t, "/api/v1/workflows"), 200, &listed)
	if len(listed.Items) != 1 || listed.Items[0].ID != definition.ID {
		t.Fatalf("definitions: %#v", listed)
	}
	startPath := "/api/v1/runs/" + f.run.ID + "/workflows"
	body := `{"version":"workflow_start.v1","definition_id":"` + definition.ID + `","inputs":{"subject":"test"},"confirm_start":true}`
	started := performControlPathRequest(t, f.api, startPath, "workflow-http-start-first", strings.NewReader(body))
	var e domain.WorkflowExecution
	decodeDataStatus(t, started, 200, &e)
	replayed := performControlPathRequest(t, f.api, startPath, "workflow-http-start-first", strings.NewReader(body))
	var replay domain.WorkflowExecution
	decodeDataStatus(t, replayed, 200, &replay)
	if replay.ID != e.ID {
		t.Fatal("HTTP start replay created another execution")
	}
	if got := performControlPathRequest(t, f.api, startPath, "workflow-http-no-confirm", strings.NewReader(strings.Replace(body, `"confirm_start":true`, `"confirm_start":false`, 1))); got.Code != 400 {
		t.Fatalf("start without consent: %d %s", got.Code, got.Body.String())
	}
	_, other, err := application.NewRunService(f.store).Create(t.Context(), application.CreateRunRequest{Goal: "other workflow Run", Profile: "code", Budget: domain.Budget{MaxTurns: 8}})
	if err != nil {
		t.Fatal(err)
	}
	foreignPath := "/api/v1/runs/" + other.ID + "/workflows/" + e.ID
	if got := f.get(t, foreignPath); got.Code != 404 {
		t.Fatalf("cross-Run read: %d %s", got.Code, got.Body.String())
	}
	if got := performControlPathRequest(t, f.api, foreignPath+"/pause", "workflow-http-foreign", strings.NewReader(`{"version":"workflow_control.v1","revision":1}`)); got.Code != 404 {
		t.Fatalf("cross-Run control: %d %s", got.Code, got.Body.String())
	}
	path := startPath + "/" + e.ID
	var paused domain.WorkflowExecution
	decodeDataStatus(t, performControlPathRequest(t, f.api, path+"/pause", "workflow-http-pause", strings.NewReader(`{"version":"workflow_control.v1","revision":1}`)), 200, &paused)
	if paused.Status != "paused" {
		t.Fatalf("pause failed: %#v", paused)
	}
	if got := performControlPathRequest(t, f.api, path+"/resume", "workflow-http-stale-resume", strings.NewReader(`{"version":"workflow_control.v1","revision":1}`)); got.Code != 409 {
		t.Fatalf("stale revision: %d %s", got.Code, got.Body.String())
	}
	var observed domain.WorkflowExecution
	decodeDataStatus(t, performControlPathRequest(t, f.api, path+"/advance", "workflow-http-paused-advance", strings.NewReader(`{"version":"workflow_control.v1","revision":2}`)), 200, &observed)
	if observed.Status != "paused" {
		t.Fatal("paused advance executed")
	}
	var rerun domain.WorkflowExecution
	decodeDataStatus(t, performControlPathRequest(t, f.api, path+"/rerun", "workflow-http-rerun-new", strings.NewReader(`{"version":"workflow_control.v1","revision":2,"confirm_start":true}`)), 200, &rerun)
	if rerun.ID == e.ID || string(rerun.Inputs) != string(e.Inputs) {
		t.Fatalf("rerun not fresh: %#v", rerun)
	}
	if got := f.request(t, http.MethodGet, path, "", "127.0.0.1:8765", "127.0.0.1:45000", nil); got.Code != 401 {
		t.Fatalf("read without token: %d", got.Code)
	}
}

func TestWorkflowOpenAPIIncludesBoundedProgramAndControls(t *testing.T) {
	raw, err := GenerateOpenAPI()
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]any
	if err = json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	paths := doc["paths"].(map[string]any)
	for _, path := range []string{"/api/v1/workflows", "/api/v1/runs/{run_id}/workflows", "/api/v1/runs/{run_id}/workflows/{execution_id}/advance", "/api/v1/runs/{run_id}/workflows/{execution_id}/rerun", "/api/v1/runs/{run_id}/workflows/{execution_id}/retry_failed"} {
		if paths[path] == nil {
			t.Fatalf("missing workflow path %s", path)
		}
	}
	schemas := doc["components"].(map[string]any)["schemas"].(map[string]any)
	for _, name := range []string{"WorkflowSpec", "WorkflowStep", "WorkflowCompletion", "WorkflowVerification", "WorkflowExecution", "WorkflowControlView", "WorkflowMapState", "WorkflowMapItem", "WorkflowBlocked"} {
		if schemas[name] == nil {
			t.Fatalf("missing workflow schema %s", name)
		}
	}
	completion := schemas["WorkflowCompletion"].(map[string]any)["properties"].(map[string]any)
	mode := completion["mode"].(map[string]any)
	if values, ok := mode["enum"].([]any); !ok || len(values) != 2 || values[0] != "report" || values[1] != "verified_code" {
		t.Fatalf("completion modes missing from schema: %#v", mode)
	}
	state := schemas["WorkflowState"].(map[string]any)["properties"].(map[string]any)
	if state["verifications"] == nil {
		t.Fatal("workflow state omits dated verification results")
	}
	if state["maps"] == nil || state["blocked"] == nil || state["allocated_jobs"] == nil {
		t.Fatal("workflow state omits durable item outcomes and retry bounds")
	}
}

// Persist the same claim-owned failed records that the runner leaves behind.
// HTTP tests then exercise the real retry transaction without invoking a model.
func prepareWorkflowRetryTarget(t *testing.T, f *apiFixture, execution domain.WorkflowExecution) domain.WorkflowExecution {
	t.Helper()
	ctx := t.Context()
	owner := "http-retry-fixture"
	e, claimed, err := f.store.ClaimWorkflowExecution(ctx, execution.ID, owner)
	if err != nil || !claimed {
		t.Fatalf("claim retry fixture: %t %v", claimed, err)
	}
	key := "retry-" + e.ID
	id := domain.WorkflowItemStepID(key, "worker", 0, 1)
	step, err := f.store.PrepareWorkflowStep(ctx, domain.WorkflowStepExecution{
		ID: id, ExecutionID: e.ID, StepID: "inspect", AgentID: "agent-" + key,
		SessionID: "session-" + key, Inputs: json.RawMessage(`{}`),
	}, owner)
	if err != nil {
		t.Fatal(err)
	}
	step.Status, step.Reason, step.Output = "failed", "provider unavailable", json.RawMessage(`{}`)
	if err = f.store.CompleteWorkflowStep(ctx, step, owner); err != nil {
		t.Fatal(err)
	}
	e.Status, e.Reason = "failed", step.Reason
	e.State.AllocatedJobs = 1
	e.State.Maps = map[string]domain.WorkflowMapState{key: {
		StepID: step.StepID, Kind: "worker", Inputs: step.Inputs,
		Items: []domain.WorkflowMapItem{{Index: 0, Generation: 1, StepExecutionID: id, Status: "failed", Reason: step.Reason}},
	}}
	e.State.Blocked = &domain.WorkflowBlocked{Key: key, StepID: step.StepID, Retryable: true}
	e, err = f.store.CheckpointWorkflowExecution(ctx, e, owner)
	if err != nil {
		t.Fatal(err)
	}
	if err = f.store.ReleaseWorkflowExecution(ctx, e.ID, owner); err != nil {
		t.Fatal(err)
	}
	return e
}

func TestWorkflowHTTPRetriesFailedItemsWithAuthorityRevisionAndIdempotency(t *testing.T) {
	f := newAPIFixture(t)
	f.api.modelControlEnabled = true
	_, run, _, execution := prepareOpenAPIWorkflowTarget(t, f)
	execution = prepareWorkflowRetryTarget(t, f, execution)
	path := "/api/v1/runs/" + run.ID + "/workflows/" + execution.ID + "/retry_failed"
	body := fmt.Sprintf(`{"version":"workflow_control.v1","revision":%d,"confirm_start":true}`, execution.Version)
	key := "workflow-http-retry-failed"
	if got := performControlPathRequest(t, f.api, path, key, strings.NewReader(strings.Replace(body, `"confirm_start":true`, `"confirm_start":false`, 1))); got.Code != 400 {
		t.Fatalf("retry without consent: %d %s", got.Code, got.Body.String())
	}
	if got := f.request(t, http.MethodPost, path, testAccessToken, "127.0.0.1:8765", "127.0.0.1:45000", strings.NewReader(body)); got.Code != http.StatusUnauthorized {
		t.Fatalf("read token retry: %d %s", got.Code, got.Body.String())
	}
	foreign := "/api/v1/runs/" + f.run.ID + "/workflows/" + execution.ID + "/retry_failed"
	if got := performControlPathRequest(t, f.api, foreign, key, strings.NewReader(body)); got.Code != http.StatusNotFound {
		t.Fatalf("foreign Run retry: %d %s", got.Code, got.Body.String())
	}
	var retried, replay domain.WorkflowExecution
	decodeDataStatus(t, performControlPathRequest(t, f.api, path, key, strings.NewReader(body)), 200, &retried)
	decodeDataStatus(t, performControlPathRequest(t, f.api, path, key, strings.NewReader(body)), 200, &replay)
	if retried.ID != execution.ID || retried.Status != "running" || retried.State.AllocatedJobs != 2 ||
		replay.Version != retried.Version || !retried.CreatedAt.Equal(execution.CreatedAt) || string(retried.Inputs) != string(execution.Inputs) {
		t.Fatalf("retry reset state or replayed twice: retried=%+v replay=%+v", retried, replay)
	}
	group := retried.State.Maps[execution.State.Blocked.Key]
	if len(group.Items) != 1 || group.Items[0].Status != "pending" || group.Items[0].Generation != 2 || group.Items[0].StepExecutionID == execution.State.Maps[execution.State.Blocked.Key].Items[0].StepExecutionID {
		t.Fatalf("failed item did not receive a new attempt: %+v", group)
	}
	if got := performControlPathRequest(t, f.api, path, "workflow-http-retry-stale", strings.NewReader(body)); got.Code != http.StatusConflict {
		t.Fatalf("new key with stale revision: %d %s", got.Code, got.Body.String())
	}
}
