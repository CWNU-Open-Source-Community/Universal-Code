package httpapi

import (
	"net/http"
	"strings"
	"testing"

	"cyberagent-workbench/internal/application"
	"cyberagent-workbench/internal/domain"
)

func TestThreadFullReactivationHTTPReportsActiveForIdleRunningChat(t *testing.T) {
	fixture := newAPIFixture(t)
	_, run, err := application.NewRunService(fixture.store).Create(t.Context(), application.CreateRunRequest{
		Goal: "reactivate Full after a chat reply", Profile: "code", Interactive: true, Budget: domain.Budget{MaxTurns: 4},
	})
	if err != nil {
		t.Fatal(err)
	}
	threadID := domain.InitialThreadID(run.ID)
	capabilities := domain.ExecutionPermissionRuntimeCapabilities{OperatorApprovalEnabled: true, DangerFullAccessEnabled: true,
		RuntimeAuthority: domain.NewExecutionPermissionRuntimeAuthority()}
	api, err := New(fixture.store, Config{AccessToken: testAccessToken, ControlToken: testControlToken,
		ExecutionPermissionControlEnabled: true, ExecutionPermissionCapabilities: capabilities})
	if err != nil {
		t.Fatal(err)
	}
	path := ThreadCollectionPath + "/" + threadID + "/execution-permission"
	body := `{"mode":"full","confirm_full":true,"reason":"explicit Full confirmation"}`
	var initial ThreadExecutionPermissionControlView
	decodeDataStatus(t, performControlPathRequest(t, api, path, "initial-full-before-chat", strings.NewReader(body)), http.StatusAccepted, &initial)
	if initial.ExecutionPermission.FullActivation != "active" {
		t.Fatalf("initial Full activation: %+v", initial)
	}
	if _, err = application.NewRunService(fixture.store).Start(t.Context(), run.ID); err != nil {
		t.Fatal(err)
	}
	capabilities.RuntimeAuthority.RevokeThread(threadID)
	var cold ThreadExecutionPermissionControlView
	decodeDataStatus(t, performRequest(t, api, http.MethodGet, path, testAccessToken, "127.0.0.1:8765", "127.0.0.1:45000", nil), http.StatusOK, &cold)
	if cold.ExecutionPermission.FullActivation != "inactive" {
		t.Fatalf("GET recreated Full authority: %+v", cold)
	}
	var reactivated ThreadExecutionPermissionControlView
	decodeDataStatus(t, performControlPathRequest(t, api, path, "reactivate-full-idle-chat", strings.NewReader(body)), http.StatusAccepted, &reactivated)
	if reactivated.ExecutionPermission.FullActivation != "active" || !reactivated.ExecutionPermission.RuntimeGateAvailable || !reactivated.CurrentRunSynchronized || reactivated.CurrentRunEffect != "applied" {
		t.Fatalf("reactivation response still reports inactive/deferred: %+v", reactivated)
	}
	var refreshed ThreadExecutionPermissionControlView
	decodeDataStatus(t, performRequest(t, api, http.MethodGet, path, testAccessToken, "127.0.0.1:8765", "127.0.0.1:45000", nil), http.StatusOK, &refreshed)
	if refreshed.ExecutionPermission.FullActivation != "active" || !refreshed.CurrentRunSynchronized {
		t.Fatalf("GET lost activation after successful POST: %+v", refreshed)
	}
}
