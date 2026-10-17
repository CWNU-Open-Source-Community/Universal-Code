package httpapi

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"cyberagent-workbench/internal/application"
	"cyberagent-workbench/internal/domain"
)

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
	for _, path := range []string{"/api/v1/workflows", "/api/v1/runs/{run_id}/workflows", "/api/v1/runs/{run_id}/workflows/{execution_id}/advance", "/api/v1/runs/{run_id}/workflows/{execution_id}/rerun"} {
		if paths[path] == nil {
			t.Fatalf("missing workflow path %s", path)
		}
	}
	schemas := doc["components"].(map[string]any)["schemas"].(map[string]any)
	for _, name := range []string{"WorkflowSpec", "WorkflowStep", "WorkflowExecution", "WorkflowControlView"} {
		if schemas[name] == nil {
			t.Fatalf("missing workflow schema %s", name)
		}
	}
}
