package httpapi

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"cyberagent-workbench/internal/application"
	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/llm"
	"cyberagent-workbench/internal/policy"
)

// Route-catalog tests use inert controllers; the focused test below uses the
// real store/service to verify authorization, CAS and replay.
func (c openAPIThreadPlanController) InspectContextPins(_ context.Context, threadID string) (domain.ThreadContextPins, error) {
	return domain.ThreadContextPins{Version: domain.ThreadContextPinsVersion, ThreadID: threadID, Pins: []domain.ThreadContextPin{}, Proposals: []domain.ContextPinProposal{}, EffectiveFrom: "next_request"}, nil
}

func (c openAPIThreadPlanController) ControlContextPin(ctx context.Context, request domain.ThreadContextPinControl) (domain.ThreadContextPinResult, error) {
	state, err := c.InspectContextPins(ctx, request.ThreadID)
	return domain.ThreadContextPinResult{State: state}, err
}

func (c openAPIThreadPlanController) ResolveContextPinProposal(ctx context.Context, request domain.ResolveContextPinProposal) (domain.ThreadContextPinResult, error) {
	state, err := c.InspectContextPins(ctx, request.ThreadID)
	return domain.ThreadContextPinResult{State: state}, err
}

func TestThreadContextPinsHTTPRequireControlAndCAS(t *testing.T) {
	fixture := newAPIFixture(t)
	_, run, err := application.NewRunService(fixture.store).Create(t.Context(), application.CreateRunRequest{Goal: "fixed Thread requirements through HTTP", Profile: "review", Interactive: true, Budget: domain.Budget{MaxTurns: 4}})
	if err != nil {
		t.Fatal(err)
	}
	router := llm.NewRouter(llm.ModelRef{Provider: "http-plan", Model: "model"})
	router.RegisterProvider(&httpPlanProvider{})
	lifecycle := application.NewRunLifecycleControlService(fixture.store)
	execution := application.NewRunExecutionHandoffService(fixture.store, router, policy.NewDefaultChecker())
	turns := application.NewThreadTurnService(fixture.store, lifecycle, execution)
	api, err := New(fixture.store, Config{AccessToken: testAccessToken, ControlToken: testControlToken, RunCreationEnabled: true, SessionMessageEnabled: true, RunLifecycleEnabled: true, RunExecutionEnabled: true, RunLifecycleController: lifecycle, RunExecutionController: execution, ThreadTurnController: turns})
	if err != nil {
		t.Fatal(err)
	}
	path := "/api/v1/threads/" + domain.InitialThreadID(run.ID) + "/context/pins"
	body := `{"version":"thread_context_pins.v1","action":"add","expected_revision":0,"text":"始终保留中文与旧接口。"}`
	unauthorized := performRequest(t, api, http.MethodPost, path, testAccessToken, "127.0.0.1:8765", "127.0.0.1:45000", strings.NewReader(body))
	assertAPIError(t, unauthorized, http.StatusUnauthorized, "POLICY_DENIED")
	response := performControlPathRequest(t, api, path, "http-context-pin-add", strings.NewReader(body))
	var added domain.ThreadContextPinResult
	decodeDataStatus(t, response, http.StatusOK, &added)
	if len(added.State.Pins) != 1 || added.State.Pins[0].Text != "始终保留中文与旧接口。" {
		t.Fatalf("HTTP pin=%+v", added)
	}
	replay := performControlPathRequest(t, api, path, "http-context-pin-add", strings.NewReader(body))
	var repeated domain.ThreadContextPinResult
	decodeDataStatus(t, replay, http.StatusOK, &repeated)
	if !repeated.Replayed || len(repeated.State.Pins) != 1 {
		t.Fatalf("HTTP pin replay=%+v", repeated)
	}
	stale := performControlPathRequest(t, api, path, "http-context-pin-stale", strings.NewReader(body))
	assertAPIError(t, stale, http.StatusConflict, "CONFLICT")
	read := performRequest(t, api, http.MethodGet, path, testAccessToken, "127.0.0.1:8765", "127.0.0.1:45000", nil)
	var state domain.ThreadContextPins
	decodeDataStatus(t, read, http.StatusOK, &state)
	if state.Revision != 1 || state.Busy || state.ThreadID != domain.InitialThreadID(run.ID) {
		t.Fatalf("GET context pins=%+v", state)
	}
	forged := performControlPathRequest(t, api, path, "http-context-pin-forged-actor", strings.NewReader(strings.Replace(body, `"action":"add"`, `"requested_by":"run_supervisor","action":"add"`, 1)))
	assertAPIError(t, forged, http.StatusBadRequest, "INVALID_ARGUMENT")
}
