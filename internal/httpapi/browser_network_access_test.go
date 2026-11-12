package httpapi

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"cyberagent-workbench/internal/application"
	"cyberagent-workbench/internal/domain"
)

func performBrowserNetworkRequest(t *testing.T, api *API, method, path, token string, body io.Reader) *httptest.ResponseRecorder {
	return performRequest(t, api, method, path, token, "127.0.0.1:8765", "127.0.0.1:45000", body)
}

func TestBrowserNetworkAccessHTTPSelectionPersistenceAndIsolation(t *testing.T) {
	f := newAPIFixture(t)
	path := "/api/v1/runs/" + f.run.ID + "/browser-network"
	projectPath := "/api/v1/workspaces/" + f.workspace.ID + "/browser-network"
	var initial domain.BrowserNetworkAccessSelection
	decodeDataStatus(t, performBrowserNetworkRequest(t, f.api, http.MethodGet, path, testAccessToken, nil), http.StatusOK, &initial)
	if initial.BrowserNetworkAccess != domain.BrowserNetworkOff || initial.Revision != 1 || initial.ProjectPreference.Revision != 0 {
		t.Fatalf("initial=%+v", initial)
	}
	permission, err := f.store.GetRunExecutionPermission(t.Context(), f.run.ID)
	if err != nil {
		t.Fatal(err)
	}
	mode, err := f.store.GetRunMode(t.Context(), f.run.ID)
	if err != nil {
		t.Fatal(err)
	}
	put := func(body, key string) domain.BrowserNetworkAccessSelection {
		t.Helper()
		var selected domain.BrowserNetworkAccessSelection
		decodeDataStatus(t, performControlMethodPathRequest(t, f.api, http.MethodPut, path, key, strings.NewReader(body)), http.StatusOK, &selected)
		return selected
	}
	body := `{"version":"browser_network_access.v1","expected_revision":1,"browser_network_access":"internet","remember_for_project":true}`
	selected := put(body, "browser-access-remember-0001")
	if selected.Revision != 2 || selected.BrowserNetworkAccess != domain.BrowserNetworkInternet || selected.ProjectPreference.Revision != 1 || selected.ProjectPreference.BrowserNetworkAccess != domain.BrowserNetworkInternet {
		t.Fatalf("selected=%+v", selected)
	}
	if replay := put(body, "browser-access-remember-0001"); !replay.Replayed || replay.Revision != 2 {
		t.Fatalf("replay=%+v", replay)
	}
	assertAPIError(t, performControlMethodPathRequest(t, f.api, http.MethodPut, path, "browser-access-stale-0002", strings.NewReader(body)), http.StatusConflict, "CONFLICT")
	afterPermission, _ := f.store.GetRunExecutionPermission(t.Context(), f.run.ID)
	afterMode, _ := f.store.GetRunMode(t.Context(), f.run.ID)
	if afterPermission.ID != permission.ID || afterMode.ID != mode.ID {
		t.Fatal("browser access changed execution or command network policy")
	}
	// Disabling retains an inactive restriction; enabling it cannot broaden
	// the next chat to unrestricted Internet access.
	selected = put(`{"version":"browser_network_access.v1","expected_revision":2,"browser_network_access":"off","browser_origins":["https://Example.org/path"],"remember_for_project":true}`, "browser-access-off-0003")
	if selected.BrowserNetworkAccess != domain.BrowserNetworkOff || len(selected.BrowserOrigins) != 1 || selected.BrowserOrigins[0] != "https://example.org:443" {
		t.Fatalf("off=%+v", selected)
	}
	create := fmt.Sprintf(`{"version":"run_creation.v1","goal":"Inherited browser choice","workspace_id":%q}`, f.workspace.ID)
	var created RunCreationControlView
	decodeDataStatus(t, performControlPathRequest(t, f.api, RunCreationControlPath, "browser-new-chat-0004", strings.NewReader(create)), http.StatusAccepted, &created)
	stored, err := f.store.GetRun(t.Context(), created.Run.ID)
	if err != nil || stored.Config.BrowserNetworkAccess != domain.BrowserNetworkOff || len(stored.Config.BrowserOrigins) != 1 {
		t.Fatalf("inherited=%+v %v", stored.Config, err)
	}
	var project domain.BrowserNetworkAccessSelection
	decodeDataStatus(t, performBrowserNetworkRequest(t, f.api, http.MethodGet, projectPath, testAccessToken, nil), http.StatusOK, &project)
	if project.Revision != 2 || project.BrowserNetworkAccess != domain.BrowserNetworkOff || len(project.BrowserOrigins) != 1 {
		t.Fatalf("project=%+v", project)
	}
	selected = put(`{"version":"browser_network_access.v1","expected_revision":3,"browser_network_access":"restricted","browser_origins":["https://example.org"]}`, "browser-access-reenable-0005")
	if selected.BrowserNetworkAccess != domain.BrowserNetworkRestricted || selected.ProjectPreference.BrowserNetworkAccess != domain.BrowserNetworkOff {
		t.Fatalf("run-only=%+v", selected)
	}
	for _, payload := range []string{
		`{"version":"browser_network_access.v1","expected_revision":4,"browser_network_access":"internet","browser_origins":["https://example.org"]}`,
		`{"version":"browser_network_access.v1","expected_revision":4,"browser_network_access":"restricted"}`,
		`{"version":"browser_network_access.v1","expected_revision":4,"browser_network_access":"off","browser_network_access":"internet"}`,
	} {
		assertAPIError(t, performControlMethodPathRequest(t, f.api, http.MethodPut, path, "browser-access-invalid-0006", strings.NewReader(payload)), http.StatusBadRequest, "INVALID_ARGUMENT")
	}
}

func TestBrowserNetworkAccessProjectControlAndAuthorization(t *testing.T) {
	f := newAPIFixture(t)
	path := "/api/v1/workspaces/" + f.workspace.ID + "/browser-network"
	body := `{"version":"browser_network_access.v1","expected_revision":0,"browser_network_access":"internet"}`
	assertAPIError(t, performBrowserNetworkRequest(t, f.api, http.MethodPut, path, testAccessToken, strings.NewReader(body)), http.StatusUnauthorized, "POLICY_DENIED")
	var selected domain.BrowserNetworkAccessSelection
	decodeDataStatus(t, performControlMethodPathRequest(t, f.api, http.MethodPut, path, "browser-project-0001", strings.NewReader(body)), http.StatusOK, &selected)
	if selected.Revision != 1 || selected.BrowserNetworkAccess != domain.BrowserNetworkInternet {
		t.Fatalf("project=%+v", selected)
	}
	var unchanged domain.BrowserNetworkAccessSelection
	decodeDataStatus(t, performBrowserNetworkRequest(t, f.api, http.MethodGet, "/api/v1/runs/"+f.run.ID+"/browser-network", testAccessToken, nil), http.StatusOK, &unchanged)
	if unchanged.BrowserNetworkAccess != domain.BrowserNetworkOff {
		t.Fatalf("project change affected existing Run: %+v", unchanged)
	}
}

func TestBrowserNetworkAccessTerminalSelectionAppliesToSuccessor(t *testing.T) {
	f := newAPIFixture(t)
	runs := application.NewRunService(f.store)
	_, run, err := runs.Create(t.Context(), application.CreateRunRequest{Goal: "complete then enable browser", WorkspaceID: f.workspace.ID, Profile: "review"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = runs.Start(t.Context(), run.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = runs.Complete(t.Context(), run.ID); err != nil {
		t.Fatal(err)
	}
	path := "/api/v1/runs/" + run.ID + "/browser-network"
	var next domain.BrowserNetworkAccessSelection
	decodeDataStatus(t, performControlMethodPathRequest(t, f.api, http.MethodPut, path, "browser-terminal-enable-0001", strings.NewReader(`{"version":"browser_network_access.v1","expected_revision":1,"browser_network_access":"internet","remember_for_project":true}`)), http.StatusOK, &next)
	if !next.ForNextRun || next.BrowserNetworkAccess != domain.BrowserNetworkInternet || next.Revision != 2 {
		t.Fatalf("next=%+v", next)
	}
	historical, err := f.store.GetRun(t.Context(), run.ID)
	if err != nil || historical.Config.BrowserNetworkAccess != domain.BrowserNetworkOff || historical.Config.BrowserNetworkRevision != 1 {
		t.Fatalf("finished config changed: %+v %v", historical.Config, err)
	}
	thread, err := f.store.GetThreadByRun(t.Context(), run.ID)
	if err != nil {
		t.Fatal(err)
	}
	continued, err := application.NewThreadService(f.store).Submit(t.Context(), application.SubmitThreadMessageRequest{Version: domain.ThreadMessageProtocolVersion, ThreadID: thread.ID, Content: "open the website", OperationKey: "browser-terminal-successor-0002", RequestedBy: "operator"})
	if err != nil || !continued.SuccessorCreated || continued.Run.Config.BrowserNetworkAccess != domain.BrowserNetworkInternet || continued.Run.Config.BrowserNetworkRevision != 1 {
		t.Fatalf("successor=%+v %v", continued, err)
	}
	assertAPIError(t, performControlMethodPathRequest(t, f.api, http.MethodPut, path, "browser-terminal-stale-0003", strings.NewReader(`{"version":"browser_network_access.v1","expected_revision":2,"browser_network_access":"off"}`)), http.StatusConflict, "CONFLICT")
}
