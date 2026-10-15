package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"cyberagent-workbench/internal/apperror"
	"cyberagent-workbench/internal/application"
)

type providerSearchReadinessControllerFake struct {
	value application.ProviderSearchReadiness
	calls int
}

func (f *providerSearchReadinessControllerFake) Check(context.Context, string) (application.SearchDiagnostics, error) {
	return application.SearchDiagnostics{}, apperror.New(apperror.CodeFailedPrecondition, "unused diagnostics test seam")
}

func (f *providerSearchReadinessControllerFake) Get(_ context.Context,
	threadID string,
) (application.ProviderSearchReadiness, error) {
	f.calls++
	value := f.value
	value.ThreadID = threadID
	return value, nil
}

func TestProviderSearchReadinessAPIProjectsStableFailClosedState(t *testing.T) {
	fixture := newAPIFixture(t)
	controller := &providerSearchReadinessControllerFake{value: application.ProviderSearchReadiness{
		ProtocolVersion: application.ProviderSearchReadinessProtocolVersion,
		RunID:           "run-search-readiness",
		ModelRoute:      "official-deepseek/deepseek-v4-flash",
		Provider:        "official-deepseek", Model: "deepseek-v4-flash",
		SearchPolicy:   modelSearchPolicyProviderNativeForTest,
		State:          application.ProviderSearchStateProviderUnqualified,
		Reason:         application.ProviderSearchReasonQualificationRequired,
		Remediation:    application.ProviderSearchRemediationQualifyProvider,
		RequiredTarget: "api.deepseek.com", NetworkMode: "allowlist",
		ModeRevision: 2,
	}}
	api, err := New(fixture.store, Config{AccessToken: testAccessToken,
		ProviderSearchReadinessController: controller,
		AppVersion:                        "provider-search-readiness-test"})
	if err != nil {
		t.Fatal(err)
	}
	path := "/api/v1/threads/thread-search-readiness/search-readiness"
	response := performRequest(t, api, http.MethodGet, path, testAccessToken,
		"127.0.0.1:8765", "127.0.0.1:45000", nil)
	if response.Code != http.StatusOK || controller.calls != 1 {
		t.Fatalf("status=%d calls=%d body=%s", response.Code, controller.calls,
			response.Body.String())
	}
	var envelope struct {
		Data ProviderSearchReadinessView `json:"data"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &envelope); err != nil {
		t.Fatal(err)
	}
	view := envelope.Data
	if view.ProtocolVersion != application.ProviderSearchReadinessProtocolVersion ||
		view.ThreadID != "thread-search-readiness" ||
		view.State != application.ProviderSearchStateProviderUnqualified ||
		view.Reason != application.ProviderSearchReasonQualificationRequired ||
		view.RequiredTarget != "api.deepseek.com" || view.RuntimeReady ||
		view.CapabilityGrant {
		t.Fatalf("view=%+v body=%s", view, response.Body.String())
	}

	unauthorized := performRequest(t, api, http.MethodGet, path, "",
		"127.0.0.1:8765", "127.0.0.1:45000", nil)
	if unauthorized.Code != http.StatusUnauthorized || controller.calls != 1 {
		t.Fatalf("unauthorized status=%d calls=%d", unauthorized.Code, controller.calls)
	}
	method := performRequest(t, api, http.MethodPost, path, testAccessToken,
		"127.0.0.1:8765", "127.0.0.1:45000", strings.NewReader(`{}`))
	if method.Code != http.StatusMethodNotAllowed || controller.calls != 1 {
		t.Fatalf("method status=%d calls=%d body=%s", method.Code, controller.calls,
			method.Body.String())
	}
}

const modelSearchPolicyProviderNativeForTest = "provider_native"
