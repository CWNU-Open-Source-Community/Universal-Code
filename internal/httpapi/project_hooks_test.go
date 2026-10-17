package httpapi

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"cyberagent-workbench/internal/apperror"
	"cyberagent-workbench/internal/application"
)

const projectHooksHTTPPath = "/api/v1/threads/thread-one/project-hooks"

var projectHooksHTTPFingerprint = strings.Repeat("a", 64)
var projectHooksHTTPBody = `{"version":"project-hooks.v1","expected_fingerprint":"` + projectHooksHTTPFingerprint + `","expected_revision":0}`

type projectHooksControllerStub struct {
	threadID, action string
	request          application.ProjectHooksReviewRequest
	calls            int
	err              error
}

func (s *projectHooksControllerStub) Get(_ context.Context, threadID string) (application.ProjectHooksView, error) {
	s.threadID, s.calls = threadID, s.calls+1
	return application.ProjectHooksView{Version: application.ProjectHooksVersion, ThreadID: threadID,
		WorkspaceID: "workspace-one", Fingerprint: projectHooksHTTPFingerprint, MatchesTask: true,
		State: "needs_review", Hooks: []application.ProjectHookView{{Event: "before_finish", CommandID: "test.go", Target: "."}}}, s.err
}

func (s *projectHooksControllerStub) Review(ctx context.Context, threadID, action string, request application.ProjectHooksReviewRequest) (application.ProjectHooksView, error) {
	s.action, s.request = action, request
	view, err := s.Get(ctx, threadID)
	view.Revision, view.Trusted, view.State = request.ExpectedRevision+1, action == "trust", "disabled"
	if view.Trusted {
		view.State = "trusted"
	}
	return view, err
}

func newProjectHooksHTTPFixture() (*API, *projectHooksControllerStub) {
	controller := &projectHooksControllerStub{}
	return &API{projectHooksController: controller, controlEnabled: true,
		tokenHash: sha256.Sum256([]byte(testAccessToken)), controlTokenHash: sha256.Sum256([]byte(testControlToken))}, controller
}

func projectHooksHTTPRequest(api *API, method, path, token, body string) *httptest.ResponseRecorder {
	request := httptest.NewRequest(method, "http://127.0.0.1"+path, strings.NewReader(body))
	request.RemoteAddr = "127.0.0.1:49152"
	if token != "" {
		request.Header.Set("Authorization", "Bearer "+token)
	}
	if method == http.MethodPost {
		request.Header.Set("Content-Type", "application/json")
	}
	response := httptest.NewRecorder()
	api.ServeHTTP(response, request)
	return response
}

func TestProjectHooksHTTPReadAndExactReview(t *testing.T) {
	api, controller := newProjectHooksHTTPFixture()
	response := projectHooksHTTPRequest(api, http.MethodGet, projectHooksHTTPPath, testAccessToken, "")
	if response.Code != http.StatusOK || controller.threadID != "thread-one" || !strings.Contains(response.Body.String(), `"state":"needs_review"`) {
		t.Fatalf("read: %d %s", response.Code, response.Body.String())
	}
	for _, action := range []string{"trust", "disable"} {
		response = projectHooksHTTPRequest(api, http.MethodPost, projectHooksHTTPPath+"/"+action, testControlToken, projectHooksHTTPBody)
		if response.Code != http.StatusOK || controller.action != action || controller.threadID != "thread-one" ||
			controller.request != (application.ProjectHooksReviewRequest{Version: application.ProjectHooksVersion, ExpectedFingerprint: projectHooksHTTPFingerprint}) {
			t.Fatalf("review did not preserve task and CAS: %#v %d %s", controller, response.Code, response.Body.String())
		}
	}
	controller.err = apperror.New(apperror.CodeConflict, "project hook definition changed; refresh before confirming")
	response = projectHooksHTTPRequest(api, http.MethodPost, projectHooksHTTPPath+"/trust", testControlToken, projectHooksHTTPBody)
	if response.Code != http.StatusConflict {
		t.Fatalf("stale review lost its conflict: %d %s", response.Code, response.Body.String())
	}
}

func TestProjectHooksHTTPAuthorityAndStrictInput(t *testing.T) {
	for _, tc := range []struct {
		name, method, path, token, body string
		disabled, missingController     bool
		status                          int
	}{
		{name: "read bearer required", method: http.MethodGet, path: projectHooksHTTPPath, status: http.StatusUnauthorized},
		{name: "read remains available with control disabled", method: http.MethodGet, path: projectHooksHTTPPath, token: testAccessToken, disabled: true, status: http.StatusOK},
		{name: "read cannot trust", method: http.MethodPost, path: projectHooksHTTPPath + "/trust", token: testAccessToken, body: projectHooksHTTPBody, status: http.StatusUnauthorized},
		{name: "read cannot disable", method: http.MethodPost, path: projectHooksHTTPPath + "/disable", token: testAccessToken, body: projectHooksHTTPBody, status: http.StatusUnauthorized},
		{name: "control disabled", method: http.MethodPost, path: projectHooksHTTPPath + "/trust", token: testControlToken, body: projectHooksHTTPBody, disabled: true, status: http.StatusNotFound},
		{name: "controller absent", method: http.MethodGet, path: projectHooksHTTPPath, token: testAccessToken, missingController: true, status: http.StatusNotFound},
		{name: "unknown action", method: http.MethodGet, path: projectHooksHTTPPath + "/execute", token: testAccessToken, status: http.StatusNotFound},
		{name: "wrong read method", method: http.MethodPost, path: projectHooksHTTPPath, token: testAccessToken, status: http.StatusMethodNotAllowed},
		{name: "wrong review method", method: http.MethodGet, path: projectHooksHTTPPath + "/trust", token: testControlToken, status: http.StatusMethodNotAllowed},
		{name: "read body", method: http.MethodGet, path: projectHooksHTTPPath, token: testAccessToken, body: `{}`, status: http.StatusBadRequest},
		{name: "read query", method: http.MethodGet, path: projectHooksHTTPPath + "?workspace_id=other", token: testAccessToken, status: http.StatusBadRequest},
		{name: "review query", method: http.MethodPost, path: projectHooksHTTPPath + "/trust?force=true", token: testControlToken, body: projectHooksHTTPBody, status: http.StatusBadRequest},
		{name: "task identity", method: http.MethodPost, path: "/api/v1/threads/%20thread-one/project-hooks/trust", token: testControlToken, body: projectHooksHTTPBody, status: http.StatusBadRequest},
		{name: "unknown body field", body: strings.TrimSuffix(projectHooksHTTPBody, "}") + `,"command":"arbitrary"}`, status: http.StatusBadRequest},
		{name: "duplicate fingerprint", body: strings.TrimSuffix(projectHooksHTTPBody, "}") + `,"expected_fingerprint":"` + projectHooksHTTPFingerprint + `"}`, status: http.StatusBadRequest},
		{name: "null object", body: `null`, status: http.StatusBadRequest},
		{name: "missing revision", body: `{"version":"project-hooks.v1","expected_fingerprint":"` + projectHooksHTTPFingerprint + `"}`, status: http.StatusBadRequest},
		{name: "null revision", body: strings.Replace(projectHooksHTTPBody, `"expected_revision":0`, `"expected_revision":null`, 1), status: http.StatusBadRequest},
		{name: "negative revision", body: strings.Replace(projectHooksHTTPBody, `"expected_revision":0`, `"expected_revision":-1`, 1), status: http.StatusBadRequest},
		{name: "version", body: strings.Replace(projectHooksHTTPBody, "project-hooks.v1", "project-hooks.v2", 1), status: http.StatusBadRequest},
		{name: "fingerprint", body: strings.Replace(projectHooksHTTPBody, projectHooksHTTPFingerprint, "unknown", 1), status: http.StatusBadRequest},
		{name: "trailing data", body: projectHooksHTTPBody + `{}`, status: http.StatusBadRequest},
	} {
		t.Run(tc.name, func(t *testing.T) {
			api, controller := newProjectHooksHTTPFixture()
			api.controlEnabled = !tc.disabled
			if tc.missingController {
				api.projectHooksController = nil
			}
			if tc.method == "" {
				tc.method, tc.path, tc.token = http.MethodPost, projectHooksHTTPPath+"/trust", testControlToken
			}
			response := projectHooksHTTPRequest(api, tc.method, tc.path, tc.token, tc.body)
			if response.Code != tc.status {
				t.Fatalf("status=%d want=%d body=%s", response.Code, tc.status, response.Body.String())
			}
			if tc.status != http.StatusOK && controller.calls != 0 {
				t.Fatal("rejected request reached controller")
			}
		})
	}
}

func TestProjectHooksHTTPRejectsFormAndMissingBrowserBearer(t *testing.T) {
	api, controller := newProjectHooksHTTPFixture()
	for _, token := range []string{"", testControlToken} {
		request := httptest.NewRequest(http.MethodPost, "http://127.0.0.1"+projectHooksHTTPPath+"/trust", strings.NewReader(projectHooksHTTPBody))
		request.RemoteAddr = "127.0.0.1:49152"
		request.Header.Set("Origin", "https://example.com")
		request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		if token != "" {
			request.Header.Set("Authorization", "Bearer "+token)
		}
		response := httptest.NewRecorder()
		api.ServeHTTP(response, request)
		if response.Code != http.StatusUnauthorized && response.Code != http.StatusUnsupportedMediaType {
			t.Fatalf("browser form accepted: %d %s", response.Code, response.Body.String())
		}
	}
	if controller.calls != 0 {
		t.Fatal("browser form reached controller")
	}
}

func TestProjectHooksHTTPConfigurationAndOpenAPI(t *testing.T) {
	fixture := newAPIFixture(t)
	controller := &projectHooksControllerStub{}
	api, err := New(fixture.store, Config{AccessToken: testAccessToken, ControlToken: testControlToken, RunControlEnabled: true, ProjectHooksController: controller})
	if err != nil {
		t.Fatal(err)
	}
	response := projectHooksHTTPRequest(api, http.MethodPost, projectHooksHTTPPath+"/trust", testControlToken, projectHooksHTTPBody)
	if response.Code != http.StatusOK || controller.action != "trust" {
		t.Fatalf("configured controller not used: %d %s", response.Code, response.Body.String())
	}
	generated, err := GenerateOpenAPI()
	if err != nil {
		t.Fatal(err)
	}
	var document openAPIDocument
	if err = json.Unmarshal(generated, &document); err != nil {
		t.Fatal(err)
	}
	if document.Paths[ProjectHooksPathTemplate].Get == nil || document.Paths[ProjectHooksPathTemplate+"/trust"].Post == nil || document.Paths[ProjectHooksPathTemplate+"/disable"].Post == nil {
		t.Fatal("project hook routes missing from OpenAPI")
	}
	for _, field := range []string{"version", "expected_fingerprint", "expected_revision"} {
		assertProjectHooksSchemaRequired(t, document.Components.Schemas, "ProjectHooksReviewRequest", field)
	}
	for _, field := range []string{"version", "thread_id", "workspace_id", "fingerprint", "trusted", "revision", "matches_task", "hooks", "state", "message"} {
		assertProjectHooksSchemaRequired(t, document.Components.Schemas, "ProjectHooksView", field)
	}
}

func assertProjectHooksSchemaRequired(t *testing.T, schemas map[string]map[string]any, name, field string) {
	t.Helper()
	required, _ := schemas[name]["required"].([]any)
	for _, value := range required {
		if value == field {
			return
		}
	}
	t.Fatalf("%s must require %s", name, field)
}
