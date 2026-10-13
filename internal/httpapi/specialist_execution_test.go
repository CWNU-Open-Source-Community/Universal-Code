package httpapi

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"cyberagent-workbench/internal/application"
	"cyberagent-workbench/internal/domain"
)

type specialistExecutionCapture struct {
	requests      []application.ExecuteSpecialistRequest
	continuations []application.ContinueSpecialistRequest
}

func (c *specialistExecutionCapture) ContinueSpecialist(_ context.Context, r application.ContinueSpecialistRequest) (application.SpecialistContinuationResult, error) {
	c.continuations = append(c.continuations, r)
	return application.SpecialistContinuationResult{RunID: r.RunID, AgentID: r.AgentID, ThreadID: "thread-main",
		Schedule:  domain.SpecialistSchedule{ID: "schedule-child", Status: domain.SpecialistScheduleCompleted, StopReason: "all_terminal", RoundsCompleted: 2, TurnsStarted: 2},
		AttemptID: "last-attempt", AttemptIDs: []string{"first-attempt", "last-attempt"}, SummaryStatus: "completed"}, nil
}

func (c *specialistExecutionCapture) ExecuteSpecialist(_ context.Context, r application.ExecuteSpecialistRequest) (application.SpecialistTurnResult, error) {
	c.requests = append(c.requests, r)
	return application.SpecialistTurnResult{RunID: r.RunID, AgentID: r.AgentID, AttemptID: "sealed-child-attempt", AttemptStatus: domain.AgentAttemptContinued, ModelAttempts: 2, ToolCalls: 1, ToolRounds: 1}, nil
}

func TestSpecialistContinuationHTTPRequiresExplicitConfirmedModelControl(t *testing.T) {
	f := newAPIFixture(t)
	capture := &specialistExecutionCapture{}
	f.api.specialistExecutionController = capture
	f.api.modelControlEnabled = true
	path := "/api/v1/runs/" + f.run.ID + "/agents/admitted-child/continue"
	body := `{"version":"specialist_continuation.v1","operation_key":"continuous-child-http-0001","confirm_execute":true}`
	response := performControlPathRequest(t, f.api, path, "continuous-child-header-0001", strings.NewReader(strings.Replace(body, `"confirm_execute":true`, `"confirm_execute":false`, 1)))
	if response.Code != http.StatusBadRequest || len(capture.continuations) != 0 {
		t.Fatalf("missing confirmation dispatched: %d %s", response.Code, response.Body.String())
	}
	response = performControlPathRequest(t, f.api, path, "continuous-child-header-0001", strings.NewReader(strings.Replace(body, `"confirm_execute":true`, `"confirm_execute":true,"max_rounds":1000`, 1)))
	if response.Code != http.StatusBadRequest || len(capture.continuations) != 0 {
		t.Fatalf("renderer widened continuation: %d %s", response.Code, response.Body.String())
	}
	f.api.modelControlEnabled = false
	response = performControlPathRequest(t, f.api, path, "continuous-child-header-0001", strings.NewReader(body))
	if response.Code != http.StatusNotFound || len(capture.continuations) != 0 {
		t.Fatalf("disabled model control dispatched: %d %s", response.Code, response.Body.String())
	}
	f.api.modelControlEnabled = true
	response = performControlPathRequest(t, f.api, path, "continuous-child-header-0001", strings.NewReader(body))
	var view SpecialistContinuationView
	decodeDataStatus(t, response, http.StatusOK, &view)
	if len(capture.continuations) != 1 || capture.continuations[0].OperationKey != "continuous-child-http-0001" || view.SummaryStatus != "completed" || view.ThreadID != "thread-main" || view.RoundsCompleted != 2 || len(view.AttemptIDs) != 2 {
		t.Fatalf("continuation intent/result changed: requests=%+v view=%+v", capture.continuations, view)
	}
}

func TestSpecialistExecutionHTTPRequiresControlConfirmationAndStableIntent(t *testing.T) {
	f := newAPIFixture(t)
	capture := &specialistExecutionCapture{}
	f.api.specialistExecutionController = capture
	f.api.modelControlEnabled = true
	path := "/api/v1/runs/" + f.run.ID + "/agents/admitted-child/execute"
	body := `{"version":"specialist_tools.v1","operation_key":"child-http-operation-0001","workspace_tools":true,"confirm_execute":true}`
	request := httptest.NewRequest(http.MethodPost, "http://127.0.0.1"+path, strings.NewReader(body))
	request.Host = "127.0.0.1:8765"
	request.RemoteAddr = "127.0.0.1:45000"
	request.Header.Set("Authorization", "Bearer "+testAccessToken)
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Idempotency-Key", "child-http-header-0001")
	response := httptest.NewRecorder()
	f.api.ServeHTTP(response, request)
	if response.Code != http.StatusUnauthorized && response.Code != http.StatusForbidden {
		t.Fatalf("read token authorized execution: %d %s", response.Code, response.Body.String())
	}
	if len(capture.requests) != 0 {
		t.Fatal("unauthorized request dispatched a child")
	}
	response = performControlPathRequest(t, f.api, path, "child-http-header-0001", strings.NewReader(strings.Replace(body, `"confirm_execute":true`, `"confirm_execute":false`, 1)))
	if response.Code != http.StatusBadRequest || len(capture.requests) != 0 {
		t.Fatalf("missing confirmation dispatched child: %d %s", response.Code, response.Body.String())
	}
	response = performControlPathRequest(t, f.api, path, "child-http-header-0001", strings.NewReader(body))
	var view SpecialistExecutionView
	decodeDataStatus(t, response, http.StatusOK, &view)
	if len(capture.requests) != 1 || !capture.requests[0].WorkspaceTools || capture.requests[0].OperationKey != "child-http-operation-0001" || capture.requests[0].RunID != f.run.ID || capture.requests[0].AgentID != "admitted-child" || view.ModelAttempts != 2 || view.Status != "continued" {
		t.Fatalf("child intent/result changed: requests=%+v view=%+v", capture.requests, view)
	}
	response = f.get(t, "/api/v1/runs/"+f.run.ID+"/agents/admitted-child/attempts/foreign-attempt/tool-receipts")
	if response.Code != http.StatusNotFound {
		t.Fatalf("foreign attempt receipts leaked: %d %s", response.Code, response.Body.String())
	}
}
