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
	"cyberagent-workbench/internal/domain"
)

const prFollowupHTTPPath = "/api/v1/threads/thread-one/pr-followups"
const prFollowupCreateBody = `{"version":"pr-followup.v1","connection_id":"github-one","pull_request":42,"interval_seconds":60,"deadline_at":"2026-10-20T12:00:00Z","max_turns":3,"include_existing_comments":true}`
const prFollowupTransitionBody = `{"version":"pr-followup.v1","expected_revision":7}`

type prFollowupControllerStub struct {
	threadID, id, key, action string
	expectedThreadID          string
	create                    application.PRFollowupCreateRequest
	transition                application.PRFollowupTransitionRequest
	calls                     int
}

func (s *prFollowupControllerStub) List(_ context.Context, threadID string) (application.PRFollowupList, error) {
	s.threadID = threadID
	s.calls++
	return application.PRFollowupList{Version: application.PRFollowupVersion, ThreadID: threadID, WorkerState: "idle", Items: []domain.PRFollowup{}}, nil
}

func (s *prFollowupControllerStub) Create(_ context.Context, threadID, key string, request application.PRFollowupCreateRequest) (domain.PRFollowup, error) {
	s.threadID, s.key, s.create = threadID, key, request
	s.calls++
	return domain.PRFollowup{ID: "followup-one", ThreadID: threadID, PullRequest: request.PullRequest, Status: domain.PRFollowupActive, Revision: 1}, nil
}

func (s *prFollowupControllerStub) Transition(_ context.Context, threadID, id, action string, request application.PRFollowupTransitionRequest) (domain.PRFollowup, error) {
	s.threadID, s.id, s.action, s.transition = threadID, id, action, request
	s.calls++
	expectedThreadID := s.expectedThreadID
	if expectedThreadID == "" {
		expectedThreadID = "thread-one"
	}
	if threadID != expectedThreadID {
		return domain.PRFollowup{}, apperror.New(apperror.CodeNotFound, "PR followup was not found in this task")
	}
	if request.ExpectedRevision != 7 {
		return domain.PRFollowup{}, apperror.New(apperror.CodeConflict, "PR followup revision changed")
	}
	return domain.PRFollowup{ID: id, ThreadID: threadID, Revision: 8}, nil
}

func newPRFollowupHTTPFixture() (*API, *prFollowupControllerStub) {
	controller := &prFollowupControllerStub{}
	return &API{prFollowupController: controller, githubReviewControlEnabled: true,
		tokenHash: sha256.Sum256([]byte(testAccessToken)), controlTokenHash: sha256.Sum256([]byte(testControlToken))}, controller
}

func prFollowupHTTPRequest(api *API, method, path, token, key, body string) *httptest.ResponseRecorder {
	request := httptest.NewRequest(method, "http://127.0.0.1"+path, strings.NewReader(body))
	request.RemoteAddr = "127.0.0.1:49152"
	if token != "" {
		request.Header.Set("Authorization", "Bearer "+token)
	}
	if key != "" {
		request.Header.Set("Idempotency-Key", key)
	}
	if method == http.MethodPost {
		request.Header.Set("Content-Type", "application/json")
	}
	response := httptest.NewRecorder()
	api.ServeHTTP(response, request)
	return response
}

func TestPRFollowupHTTPAuthorityAndEnablement(t *testing.T) {
	for _, tc := range []struct {
		name, method, path, token, body string
		disabled, missingController     bool
		status                          int
	}{
		{name: "read requires bearer", method: http.MethodGet, path: prFollowupHTTPPath, status: http.StatusUnauthorized},
		{name: "read token lists", method: http.MethodGet, path: prFollowupHTTPPath, token: testAccessToken, status: http.StatusOK},
		{name: "read token cannot create", method: http.MethodPost, path: prFollowupHTTPPath, token: testAccessToken, body: prFollowupCreateBody, status: http.StatusUnauthorized},
		{name: "control token creates", method: http.MethodPost, path: prFollowupHTTPPath, token: testControlToken, body: prFollowupCreateBody, status: http.StatusOK},
		{name: "read token cannot pause", method: http.MethodPost, path: prFollowupHTTPPath + "/followup-one/pause", token: testAccessToken, body: prFollowupTransitionBody, status: http.StatusUnauthorized},
		{name: "disabled", method: http.MethodGet, path: prFollowupHTTPPath, token: testAccessToken, disabled: true, status: http.StatusNotFound},
		{name: "missing controller", method: http.MethodPost, path: prFollowupHTTPPath, token: testControlToken, body: prFollowupCreateBody, missingController: true, status: http.StatusNotFound},
	} {
		t.Run(tc.name, func(t *testing.T) {
			api, controller := newPRFollowupHTTPFixture()
			api.githubReviewControlEnabled = !tc.disabled
			if tc.missingController {
				api.prFollowupController = nil
			}
			response := prFollowupHTTPRequest(api, tc.method, tc.path, tc.token, "pr-followup-create-http-operation", tc.body)
			if response.Code != tc.status {
				t.Fatalf("status=%d want=%d body=%s", response.Code, tc.status, response.Body.String())
			}
			if tc.status != http.StatusOK && controller.calls != 0 {
				t.Fatal("rejected request reached controller")
			}
		})
	}
}

func TestPRFollowupHTTPForwardsTaskKeyAndExpectedRevision(t *testing.T) {
	api, controller := newPRFollowupHTTPFixture()
	response := prFollowupHTTPRequest(api, http.MethodPost, prFollowupHTTPPath, testControlToken, "pr-followup-original-operation-key", prFollowupCreateBody)
	if response.Code != http.StatusOK || controller.threadID != "thread-one" || controller.key != "pr-followup-original-operation-key" ||
		controller.create.ConnectionID != "github-one" || controller.create.PullRequest != 42 || controller.create.IntervalSeconds != 60 || controller.create.MaxTurns != 3 || !controller.create.IncludeExistingComments {
		t.Fatalf("create did not preserve request: controller=%#v status=%d body=%s", controller, response.Code, response.Body.String())
	}
	for _, action := range []string{"pause", "resume", "cancel"} {
		response = prFollowupHTTPRequest(api, http.MethodPost, prFollowupHTTPPath+"/followup-one/"+action, testControlToken, "", prFollowupTransitionBody)
		if response.Code != http.StatusOK || controller.threadID != "thread-one" || controller.id != "followup-one" || controller.action != action || controller.transition.ExpectedRevision != 7 || controller.transition.Version != application.PRFollowupVersion {
			t.Fatalf("transition did not preserve task and CAS: controller=%#v status=%d body=%s", controller, response.Code, response.Body.String())
		}
	}
	response = prFollowupHTTPRequest(api, http.MethodPost, prFollowupHTTPPath+"/followup-one/pause", testControlToken, "", `{"version":"pr-followup.v1","expected_revision":6}`)
	if response.Code != http.StatusConflict || controller.transition.ExpectedRevision != 6 {
		t.Fatalf("stale revision did not preserve conflict: %d %s", response.Code, response.Body.String())
	}
	response = prFollowupHTTPRequest(api, http.MethodPost, "/api/v1/threads/thread-other/pr-followups/followup-one/cancel", testControlToken, "", prFollowupTransitionBody)
	if response.Code != http.StatusNotFound || controller.threadID != "thread-other" || controller.id != "followup-one" {
		t.Fatalf("task scope not preserved: %d %s", response.Code, response.Body.String())
	}
}

func TestPRFollowupHTTPRejectsInvalidInputBeforeController(t *testing.T) {
	for _, tc := range []struct {
		name, method, path, key, body string
		status                        int
	}{
		{name: "missing create key", method: http.MethodPost, path: prFollowupHTTPPath, body: prFollowupCreateBody, status: http.StatusBadRequest},
		{name: "invalid create key", method: http.MethodPost, path: prFollowupHTTPPath, key: "key contains spaces", body: prFollowupCreateBody, status: http.StatusBadRequest},
		{name: "unknown field", method: http.MethodPost, path: prFollowupHTTPPath, key: "pr-followup-test-operation", body: `{"version":"pr-followup.v1","thread_id":"override"}`, status: http.StatusBadRequest},
		{name: "duplicate field", method: http.MethodPost, path: prFollowupHTTPPath, key: "pr-followup-test-operation", body: `{"version":"pr-followup.v1","version":"pr-followup.v1"}`, status: http.StatusBadRequest},
		{name: "null object", method: http.MethodPost, path: prFollowupHTTPPath, key: "pr-followup-test-operation", body: `null`, status: http.StatusBadRequest},
		{name: "trailing data", method: http.MethodPost, path: prFollowupHTTPPath, key: "pr-followup-test-operation", body: prFollowupCreateBody + `{}`, status: http.StatusBadRequest},
		{name: "transition unknown field", method: http.MethodPost, path: prFollowupHTTPPath + "/followup-one/pause", body: `{"version":"pr-followup.v1","expected_revision":7,"thread_id":"override"}`, status: http.StatusBadRequest},
		{name: "transition duplicate revision", method: http.MethodPost, path: prFollowupHTTPPath + "/followup-one/pause", body: `{"version":"pr-followup.v1","expected_revision":7,"expected_revision":6}`, status: http.StatusBadRequest},
		{name: "unexpected read query", method: http.MethodGet, path: prFollowupHTTPPath + "?limit=20", status: http.StatusBadRequest},
		{name: "unexpected mutation query", method: http.MethodPost, path: prFollowupHTTPPath + "/followup-one/cancel?force=true", body: prFollowupTransitionBody, status: http.StatusBadRequest},
		{name: "read body", method: http.MethodGet, path: prFollowupHTTPPath, body: `{}`, status: http.StatusBadRequest},
		{name: "invalid task path", method: http.MethodPost, path: "/api/v1/threads/%20thread-one/pr-followups", key: "pr-followup-test-operation", body: prFollowupCreateBody, status: http.StatusBadRequest},
		{name: "invalid followup path", method: http.MethodPost, path: prFollowupHTTPPath + "/followup%5Cone/pause", body: prFollowupTransitionBody, status: http.StatusBadRequest},
		{name: "unknown action", method: http.MethodGet, path: prFollowupHTTPPath + "/followup-one/delete", status: http.StatusNotFound},
		{name: "wrong collection method", method: http.MethodPut, path: prFollowupHTTPPath, status: http.StatusMethodNotAllowed},
		{name: "wrong transition method", method: http.MethodGet, path: prFollowupHTTPPath + "/followup-one/pause", status: http.StatusMethodNotAllowed},
	} {
		t.Run(tc.name, func(t *testing.T) {
			api, controller := newPRFollowupHTTPFixture()
			token := testControlToken
			if tc.method == http.MethodGet && !strings.HasSuffix(tc.path, "/pause") {
				token = testAccessToken
			}
			response := prFollowupHTTPRequest(api, tc.method, tc.path, token, tc.key, tc.body)
			if response.Code != tc.status || controller.calls != 0 {
				t.Fatalf("status=%d want=%d calls=%d body=%s", response.Code, tc.status, controller.calls, response.Body.String())
			}
		})
	}
	for _, change := range []string{"duplicate key", "content type"} {
		t.Run(change, func(t *testing.T) {
			api, controller := newPRFollowupHTTPFixture()
			request := httptest.NewRequest(http.MethodPost, "http://127.0.0.1"+prFollowupHTTPPath, strings.NewReader(prFollowupCreateBody))
			request.RemoteAddr = "127.0.0.1:49152"
			request.Header.Set("Authorization", "Bearer "+testControlToken)
			request.Header.Set("Content-Type", "application/json")
			request.Header.Set("Idempotency-Key", "pr-followup-test-operation")
			want := http.StatusBadRequest
			if change == "duplicate key" {
				request.Header.Add("Idempotency-Key", "another-pr-followup-operation")
			} else {
				request.Header.Set("Content-Type", "text/plain")
				want = http.StatusUnsupportedMediaType
			}
			response := httptest.NewRecorder()
			api.ServeHTTP(response, request)
			if response.Code != want || controller.calls != 0 {
				t.Fatalf("status=%d want=%d calls=%d body=%s", response.Code, want, controller.calls, response.Body.String())
			}
		})
	}
}

func TestPRFollowupOpenAPIExposesOnlyPublicRecords(t *testing.T) {
	payload, err := GenerateOpenAPI()
	if err != nil {
		t.Fatal(err)
	}
	var document struct {
		Paths      map[string]map[string]json.RawMessage `json:"paths"`
		Components struct {
			Schemas map[string]json.RawMessage `json:"schemas"`
		} `json:"components"`
	}
	if err := json.Unmarshal(payload, &document); err != nil {
		t.Fatal(err)
	}
	path := "/api/v1/threads/{thread_id}/pr-followups"
	if document.Paths[path]["get"] == nil || document.Paths[path]["post"] == nil {
		t.Fatal("missing collection operations")
	}
	if !strings.Contains(string(document.Paths[path]["post"]), `"Idempotency-Key"`) || !strings.Contains(string(document.Paths[path]["post"]), `"ControlBearerAuth"`) {
		t.Fatal("create schema omitted its operation key or control authority")
	}
	for _, action := range []string{"pause", "resume", "cancel"} {
		if document.Paths[path+"/{followup_id}/"+action]["post"] == nil {
			t.Fatalf("missing %s operation", action)
		}
	}
	for name, schema := range document.Components.Schemas {
		if !strings.Contains(name, "PRFollowup") {
			continue
		}
		for _, forbidden := range []string{`"observed"`, `"pending"`, `"lease_token"`, `"lease_owner"`, `"content"`, `"operation_key"`} {
			if strings.Contains(string(schema), forbidden) {
				t.Fatalf("%s exposed private field %s", name, forbidden)
			}
		}
	}
}

func TestPRFollowupHTTPConfigurationRegistersController(t *testing.T) {
	fixture := newAPIFixture(t)
	controller := &prFollowupControllerStub{}
	api, err := New(fixture.store, Config{AccessToken: testAccessToken, ControlToken: testControlToken,
		ExecutionPermissionControlEnabled: true, ExecutionPermissionCapabilities: domain.ExecutionPermissionRuntimeCapabilities{OperatorApprovalEnabled: true},
		ApprovalControlEnabled: true, ApprovalController: application.NewApprovalControlService(fixture.store, nil, nil),
		GitHubReviewControlEnabled: true, GitHubReviewController: &githubReviewControllerStub{}, PRFollowupController: controller})
	if err != nil {
		t.Fatal(err)
	}
	response := prFollowupHTTPRequest(api, http.MethodGet, prFollowupHTTPPath, testAccessToken, "", "")
	if response.Code != http.StatusOK || controller.threadID != "thread-one" {
		t.Fatalf("configured controller was not used: %d %s", response.Code, response.Body.String())
	}
}
