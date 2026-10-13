package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"cyberagent-workbench/internal/application"
	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/llm"
	"cyberagent-workbench/internal/policy"
)

type delayedRunExecution struct{ RunExecutionController }

func (c delayedRunExecution) Execute(ctx context.Context, request application.ExecuteRunHandoffRequest) (application.ExecuteRunHandoffResult, error) {
	select {
	case <-time.After(150 * time.Millisecond):
	case <-ctx.Done():
		return application.ExecuteRunHandoffResult{}, ctx.Err()
	}
	return c.RunExecutionController.Execute(ctx, request)
}

func TestRunExecutionHTTPRespondsAfterOrdinaryWriteTimeout(t *testing.T) {
	f := newAPIFixture(t)
	runs := application.NewRunService(f.store)
	_, created, err := runs.Create(t.Context(), application.CreateRunRequest{
		Goal: "long HTTP execution", Profile: "code", ModelRoute: "mock/mock-code",
		Budget: domain.Budget{MaxTurns: 4},
	})
	if err != nil {
		t.Fatal(err)
	}
	run, err := runs.Start(t.Context(), created.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.store.EnqueueOperatorSteering(t.Context(), domain.EnqueueOperatorSteeringRequest{
		RunID: run.ID, SessionID: run.SessionID, Content: "complete this queued input",
		OperationKey: "http-long-execution-queue-0001", RequestedBy: "test_operator",
	}); err != nil {
		t.Fatal(err)
	}
	f.api.runExecutionEnabled = true
	f.api.runExecutionController = delayedRunExecution{application.NewRunExecutionHandoffService(f.store, llm.NewDefaultRouter(), policy.NewDefaultChecker())}
	server := httptest.NewUnstartedServer(f.api)
	server.Config.WriteTimeout = 50 * time.Millisecond
	server.Start()
	defer server.Close()
	request, err := http.NewRequest(http.MethodPost, server.URL+"/api/v1/runs/"+run.ID+"/execute",
		strings.NewReader(`{"version":"run_execution_handoff.v1","max_steps":1}`))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Authorization", "Bearer "+testControlToken)
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Idempotency-Key", "http-long-execution-0001")
	response, err := server.Client().Do(request)
	if err != nil {
		t.Fatalf("lost execution response: %v", err)
	}
	defer response.Body.Close()
	var envelope struct {
		Data RunExecutionControlView `json:"data"`
	}
	if err := json.NewDecoder(response.Body).Decode(&envelope); err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != http.StatusAccepted || envelope.Data.RunID != run.ID || envelope.Data.CommittedCount != 1 {
		t.Fatalf("execution receipt: status=%d data=%+v", response.StatusCode, envelope.Data)
	}
	queue, err := f.store.GetOperatorSteeringQueueSummary(t.Context(), run.ID)
	if err != nil || queue.Committed != 1 {
		t.Fatalf("durable execution differs: queue=%+v err=%v", queue, err)
	}
	if server.Config.WriteTimeout != 50*time.Millisecond {
		t.Fatal("execution changed the global server timeout")
	}
}

type delayedSpecialistExecution struct{ specialistExecutionCapture }

func (c *delayedSpecialistExecution) ExecuteSpecialist(ctx context.Context, request application.ExecuteSpecialistRequest) (application.SpecialistTurnResult, error) {
	select {
	case <-time.After(150 * time.Millisecond):
	case <-ctx.Done():
		return application.SpecialistTurnResult{}, ctx.Err()
	}
	return c.specialistExecutionCapture.ExecuteSpecialist(ctx, request)
}

func TestSpecialistExecutionHTTPRespondsAfterOrdinaryWriteTimeout(t *testing.T) {
	f := newAPIFixture(t)
	f.api.modelControlEnabled = true
	f.api.specialistExecutionController = &delayedSpecialistExecution{}
	server := httptest.NewUnstartedServer(f.api)
	server.Config.WriteTimeout = 50 * time.Millisecond
	server.Start()
	defer server.Close()
	request, err := http.NewRequest(http.MethodPost, server.URL+"/api/v1/runs/"+f.run.ID+"/agents/child-timeout/execute",
		strings.NewReader(`{"version":"specialist_tools.v1","operation_key":"http-long-specialist-0001","confirm_execute":true}`))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Authorization", "Bearer "+testControlToken)
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Idempotency-Key", "http-long-specialist-header-0001")
	response, err := server.Client().Do(request)
	if err != nil {
		t.Fatalf("lost child execution response: %v", err)
	}
	defer response.Body.Close()
	var envelope struct {
		Data SpecialistExecutionView `json:"data"`
	}
	if err := json.NewDecoder(response.Body).Decode(&envelope); err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != http.StatusOK || envelope.Data.AttemptID != "sealed-child-attempt" || envelope.Data.AgentID != "child-timeout" {
		t.Fatalf("child execution receipt: status=%d data=%+v", response.StatusCode, envelope.Data)
	}
	if server.Config.WriteTimeout != 50*time.Millisecond {
		t.Fatal("child execution changed the global server timeout")
	}
}
