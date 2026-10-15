package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"cyberagent-workbench/internal/apperror"
	"cyberagent-workbench/internal/application"
	"cyberagent-workbench/internal/domain"
)

type fullCDPSessionControllerStub struct {
	view         application.FullCDPSessionView
	openResult   application.FullCDPSessionResult
	closeResult  application.FullCDPSessionResult
	getRunID     string
	openRequest  application.OpenFullCDPSessionRequest
	closeRequest application.CloseFullCDPSessionRequest
}

func (*fullCDPSessionControllerStub) CaptureFullCDPPreview(context.Context, string, string) (application.FullCDPPreviewView, error) {
	return application.FullCDPPreviewView{}, apperror.New(apperror.CodeFailedPrecondition, "unused preview test seam")
}
func (*fullCDPSessionControllerStub) ReadFullCDPPreviewImage(context.Context, string, string, string) (application.FullCDPPreviewCapture, error) {
	return application.FullCDPPreviewCapture{}, apperror.New(apperror.CodeFailedPrecondition, "unused preview test seam")
}
func (*fullCDPSessionControllerStub) ActFullCDPPreview(context.Context, application.FullCDPPreviewActionRequest) (application.FullCDPPreviewView, error) {
	return application.FullCDPPreviewView{}, apperror.New(apperror.CodeFailedPrecondition, "unused preview test seam")
}

func (stub *fullCDPSessionControllerStub) GetFullCDPSession(
	_ context.Context, runID string,
) (application.FullCDPSessionView, error) {
	stub.getRunID = runID
	return stub.view, nil
}

func (stub *fullCDPSessionControllerStub) OpenFullCDPSession(
	_ context.Context, request application.OpenFullCDPSessionRequest,
) (application.FullCDPSessionResult, error) {
	stub.openRequest = request
	return stub.openResult, nil
}

func (stub *fullCDPSessionControllerStub) CloseFullCDPSession(
	_ context.Context, request application.CloseFullCDPSessionRequest,
) (application.FullCDPSessionResult, error) {
	stub.closeRequest = request
	return stub.closeResult, nil
}

func TestFullCDPSessionHTTPReadOpenAndClose(t *testing.T) {
	fixture := newAPIFixture(t)
	startedAt := time.Date(2026, time.August, 30, 1, 2, 3, 0, time.UTC)
	expiresAt := startedAt.Add(15 * time.Minute)
	ready := application.FullCDPSessionView{
		Version:   application.FullCDPSessionProtocolVersion,
		SessionID: "full_cdp_session-http-0001", RunID: fixture.run.ID,
		State:        application.FullCDPSessionReady,
		Browser:      application.FullCDPBrowserSelection{Product: "chrome", Channel: "stable"},
		TargetOrigin: "http://127.0.0.1:18080", RuntimeAvailable: true,
		StartedAt: &startedAt, ExpiresAt: &expiresAt,
	}
	closedAt := startedAt.Add(time.Minute)
	closed := ready
	closed.State = application.FullCDPSessionClosed
	closed.CompletedAt = &closedAt
	closed.CloseReason = application.FullCDPCloseOperator
	closed.CDPClosed = true
	closed.ProcessTreeQuiescent = true
	closed.ProfileReleased = true
	closed.ProfileCleaned = true
	stub := &fullCDPSessionControllerStub{view: ready,
		openResult:  application.FullCDPSessionResult{Session: ready},
		closeResult: application.FullCDPSessionResult{Session: closed, Replayed: true}}
	api, err := New(fixture.store, Config{AccessToken: testAccessToken,
		ControlToken: testControlToken, FullCDPSessionControlEnabled: true,
		ExecutionPermissionControlEnabled:  true,
		BrowserCDPPermissionControlEnabled: true,
		BrowserCDPPermissionCapabilities: domain.BrowserCDPPermissionRuntimeCapabilities{
			ControlEnabled: true, FullDebugEnabled: true},
		ExecutionPermissionCapabilities: domain.ExecutionPermissionRuntimeCapabilities{
			OperatorApprovalEnabled: true, DangerFullAccessEnabled: true},
		FullCDPSessionController: stub})
	if err != nil {
		t.Fatal(err)
	}
	path := strings.ReplaceAll(FullCDPSessionControlPathTemplate,
		"{run_id}", fixture.run.ID)
	readResponse := performRequest(t, api, http.MethodGet, path, testAccessToken,
		"127.0.0.1:8765", "127.0.0.1:45000", nil)
	var read FullCDPSessionControlView
	decodeDataStatus(t, readResponse, http.StatusOK, &read)
	if stub.getRunID != fixture.run.ID || read.Replayed ||
		read.Session.SessionID != ready.SessionID || read.Session.TargetOrigin != ready.TargetOrigin {
		t.Fatalf("unexpected Full CDP read projection: %+v run_id=%q", read, stub.getRunID)
	}
	assertFullCDPHTTPProjectionRedacted(t, readResponse)

	openBody := `{"version":"full_cdp_session.v1","target":"http://127.0.0.1:18080/path","browser":{"product":"chrome","channel":"stable"},` +
		`"expected_execution_permission_revision":7,"expected_browser_cdp_permission_revision":9,` +
		`"confirm_full_cdp":true,"reason":"authorized CTF debugging"}`
	openResponse := performControlPathRequest(t, api, path,
		"http-full-cdp-open-0001", strings.NewReader(openBody))
	var opened FullCDPSessionControlView
	decodeDataStatus(t, openResponse, http.StatusCreated, &opened)
	wantOpen := application.OpenFullCDPSessionRequest{RunID: fixture.run.ID,
		Target:                               "http://127.0.0.1:18080/path",
		Browser:                              application.FullCDPBrowserSelection{Product: "chrome", Channel: "stable"},
		ExpectedExecutionPermissionRevision:  7,
		ExpectedBrowserCDPPermissionRevision: 9,
		ConfirmFullCDP:                       true, Reason: "authorized CTF debugging",
		OperationKey: "http-full-cdp-open-0001"}
	if !reflect.DeepEqual(stub.openRequest, wantOpen) ||
		opened.Session.SessionID != ready.SessionID || opened.Replayed {
		t.Fatalf("unexpected Full CDP open: request=%+v view=%+v", stub.openRequest, opened)
	}
	assertFullCDPHTTPProjectionRedacted(t, openResponse)

	closePath := strings.ReplaceAll(FullCDPSessionCloseControlPathTemplate,
		"{run_id}", fixture.run.ID)
	closeBody := `{"version":"full_cdp_session_close.v1",` +
		`"expected_session_id":"full_cdp_session-http-0001","reason":"operator finished"}`
	closeResponse := performControlPathRequest(t, api, closePath,
		"http-full-cdp-close-0001", strings.NewReader(closeBody))
	var closeView FullCDPSessionControlView
	decodeDataStatus(t, closeResponse, http.StatusOK, &closeView)
	wantClose := application.CloseFullCDPSessionRequest{RunID: fixture.run.ID,
		ExpectedSessionID: ready.SessionID,
		OperationKey:      "http-full-cdp-close-0001", Reason: "operator finished"}
	if !reflect.DeepEqual(stub.closeRequest, wantClose) || !closeView.Replayed ||
		closeView.Session.State != string(application.FullCDPSessionClosed) ||
		!closeView.Session.CDPClosed || !closeView.Session.ProcessTreeQuiescent ||
		!closeView.Session.ProfileReleased || !closeView.Session.ProfileCleaned {
		t.Fatalf("unexpected Full CDP close: request=%+v view=%+v", stub.closeRequest, closeView)
	}
	assertFullCDPHTTPProjectionRedacted(t, closeResponse)
}

func TestFullCDPSessionHTTPStrictBoundariesAndTokens(t *testing.T) {
	fixture := newAPIFixture(t)
	stub := &fullCDPSessionControllerStub{view: application.FullCDPSessionView{
		Version: application.FullCDPSessionProtocolVersion, RunID: fixture.run.ID,
		State: application.FullCDPSessionNone}}
	api, err := New(fixture.store, Config{AccessToken: testAccessToken,
		ControlToken: testControlToken, FullCDPSessionControlEnabled: true,
		ExecutionPermissionControlEnabled:  true,
		BrowserCDPPermissionControlEnabled: true,
		BrowserCDPPermissionCapabilities: domain.BrowserCDPPermissionRuntimeCapabilities{
			ControlEnabled: true, FullDebugEnabled: true},
		ExecutionPermissionCapabilities: domain.ExecutionPermissionRuntimeCapabilities{
			OperatorApprovalEnabled: true, DangerFullAccessEnabled: true},
		FullCDPSessionController: stub})
	if err != nil {
		t.Fatal(err)
	}
	path := strings.ReplaceAll(FullCDPSessionControlPathTemplate,
		"{run_id}", fixture.run.ID)
	noneResponse := performRequest(t, api, http.MethodGet, path, testAccessToken,
		"127.0.0.1:8765", "127.0.0.1:45000", nil)
	var noneEnvelope struct {
		Data struct {
			Session map[string]json.RawMessage `json:"session"`
		} `json:"data"`
	}
	if noneResponse.Code != http.StatusOK ||
		json.Unmarshal(noneResponse.Body.Bytes(), &noneEnvelope) != nil {
		t.Fatalf("unexpected empty Full CDP session response: %d %s",
			noneResponse.Code, noneResponse.Body.String())
	}
	if _, present := noneEnvelope.Data.Session["browser"]; present {
		t.Fatalf("empty Full CDP state exposed a synthetic browser selection: %s",
			noneResponse.Body.String())
	}
	validBody := `{"version":"full_cdp_session.v1","target":"http://127.0.0.1:18080","browser":{"product":"edge","channel":"beta"},` +
		`"expected_execution_permission_revision":1,"expected_browser_cdp_permission_revision":1,` +
		`"confirm_full_cdp":true}`

	assertAPIError(t, performRequest(t, api, http.MethodGet, path, testControlToken,
		"127.0.0.1:8765", "127.0.0.1:45000", nil),
		http.StatusUnauthorized, "POLICY_DENIED")
	assertAPIError(t, performRequest(t, api, http.MethodPost, path, testAccessToken,
		"127.0.0.1:8765", "127.0.0.1:45000", strings.NewReader(validBody)),
		http.StatusUnauthorized, "POLICY_DENIED")
	assertAPIError(t, performRequest(t, api, http.MethodGet, path+"?refresh=true",
		testAccessToken, "127.0.0.1:8765", "127.0.0.1:45000", nil),
		http.StatusBadRequest, "INVALID_ARGUMENT")
	assertAPIError(t, performRequest(t, api, http.MethodGet, path, testAccessToken,
		"127.0.0.1:8765", "127.0.0.1:45000", strings.NewReader(`{}`)),
		http.StatusBadRequest, "INVALID_ARGUMENT")

	tests := []struct {
		name        string
		path        string
		key         string
		contentType string
		body        string
		status      int
		code        string
	}{
		{name: "query", path: path + "?force=true", key: "http-full-cdp-query-0001", contentType: "application/json", body: validBody, status: http.StatusBadRequest, code: "INVALID_ARGUMENT"},
		{name: "missing version", path: path, key: "http-full-cdp-missing-version-0001", contentType: "application/json", body: strings.Replace(validBody, `"version":"full_cdp_session.v1",`, "", 1), status: http.StatusBadRequest, code: "INVALID_ARGUMENT"},
		{name: "wrong version", path: path, key: "http-full-cdp-wrong-version-0001", contentType: "application/json", body: strings.Replace(validBody, "full_cdp_session.v1", "full_cdp_session.v0", 1), status: http.StatusBadRequest, code: "INVALID_ARGUMENT"},
		{name: "missing key", path: path, contentType: "application/json", body: validBody, status: http.StatusBadRequest, code: "INVALID_ARGUMENT"},
		{name: "content type", path: path, key: "http-full-cdp-content-0001", contentType: "application/json; charset=utf-8", body: validBody, status: http.StatusUnsupportedMediaType, code: "INVALID_ARGUMENT"},
		{name: "unknown field", path: path, key: "http-full-cdp-unknown-0001", contentType: "application/json", body: strings.TrimSuffix(validBody, "}") + `,"devtools_endpoint":"ws://secret"}`, status: http.StatusBadRequest, code: "INVALID_ARGUMENT"},
		{name: "duplicate field", path: path, key: "http-full-cdp-duplicate-0001", contentType: "application/json", body: strings.TrimSuffix(validBody, "}") + `,"confirm_full_cdp":true}`, status: http.StatusBadRequest, code: "INVALID_ARGUMENT"},
		{name: "nested duplicate browser field", path: path, key: "http-full-cdp-nested-duplicate-0001", contentType: "application/json", body: strings.Replace(validBody, `"browser":{"product":"edge","channel":"beta"}`, `"browser":{"product":"edge","product":"chrome","channel":"beta"}`, 1), status: http.StatusBadRequest, code: "INVALID_ARGUMENT"},
		{name: "trailing value", path: path, key: "http-full-cdp-trailing-0001", contentType: "application/json", body: validBody + `{}`, status: http.StatusBadRequest, code: "INVALID_ARGUMENT"},
		{name: "oversized", path: path, key: "http-full-cdp-oversized-0001", contentType: "application/json", body: strings.Repeat("x", MaxControlRequestBodyBytes+1), status: http.StatusRequestEntityTooLarge, code: "RESOURCE_EXHAUSTED"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodPost,
				"http://127.0.0.1"+test.path, strings.NewReader(test.body))
			request.Host = "127.0.0.1:8765"
			request.RemoteAddr = "127.0.0.1:45000"
			request.Header.Set("Authorization", "Bearer "+testControlToken)
			if test.key != "" {
				request.Header.Set("Idempotency-Key", test.key)
			}
			if test.contentType != "" {
				request.Header.Set("Content-Type", test.contentType)
			}
			response := httptest.NewRecorder()
			api.ServeHTTP(response, request)
			assertAPIError(t, response, test.status, test.code)
		})
	}
	duplicateKeyRequest := httptest.NewRequest(http.MethodPost,
		"http://127.0.0.1"+path, strings.NewReader(validBody))
	duplicateKeyRequest.Host = "127.0.0.1:8765"
	duplicateKeyRequest.RemoteAddr = "127.0.0.1:45000"
	duplicateKeyRequest.Header.Set("Authorization", "Bearer "+testControlToken)
	duplicateKeyRequest.Header.Set("Content-Type", "application/json")
	duplicateKeyRequest.Header.Add("Idempotency-Key", "http-full-cdp-key-0001")
	duplicateKeyRequest.Header.Add("Idempotency-Key", "http-full-cdp-key-0002")
	duplicateKeyResponse := httptest.NewRecorder()
	api.ServeHTTP(duplicateKeyResponse, duplicateKeyRequest)
	assertAPIError(t, duplicateKeyResponse, http.StatusBadRequest, "INVALID_ARGUMENT")

	closePath := strings.ReplaceAll(FullCDPSessionCloseControlPathTemplate,
		"{run_id}", fixture.run.ID)
	for name, body := range map[string]string{
		"missing version": `{"expected_session_id":"full_cdp_session-http-0001"}`,
		"wrong version":   `{"version":"full_cdp_session_close.v0","expected_session_id":"full_cdp_session-http-0001"}`,
		"old confirmation": `{"version":"full_cdp_session_close.v1",` +
			`"expected_session_id":"full_cdp_session-http-0001","confirm":true}`,
	} {
		t.Run("close "+name, func(t *testing.T) {
			response := performControlPathRequest(t, api, closePath,
				"http-full-cdp-close-invalid-0001", strings.NewReader(body))
			assertAPIError(t, response, http.StatusBadRequest, "INVALID_ARGUMENT")
		})
	}
	wrongMethod := performRequest(t, api, http.MethodGet, closePath, testAccessToken,
		"127.0.0.1:8765", "127.0.0.1:45000", nil)
	assertAPIError(t, wrongMethod, http.StatusMethodNotAllowed, "INVALID_ARGUMENT")
	if allow := wrongMethod.Header().Get("Allow"); allow != http.MethodPost {
		t.Fatalf("Full CDP close Allow=%q", allow)
	}
}

func TestFullCDPSessionHTTPProjectsPreReadyRecoveryState(t *testing.T) {
	fixture := newAPIFixture(t)
	stub := &fullCDPSessionControllerStub{view: application.FullCDPSessionView{
		Version:   application.FullCDPSessionProtocolVersion,
		SessionID: "full_cdp_session-http-recovery-0001", RunID: fixture.run.ID,
		State:            application.FullCDPSessionClosing,
		Browser:          application.FullCDPBrowserSelection{Product: "chrome", Channel: "stable"},
		RuntimeAvailable: true, CloseReason: application.FullCDPCloseOpenFailed,
		CDPClosed: true, FailureCode: "cleanup_recovery_required",
	}}
	api, err := New(fixture.store, Config{AccessToken: testAccessToken,
		ControlToken: testControlToken, FullCDPSessionControlEnabled: true,
		ExecutionPermissionControlEnabled:  true,
		BrowserCDPPermissionControlEnabled: true,
		BrowserCDPPermissionCapabilities: domain.BrowserCDPPermissionRuntimeCapabilities{
			ControlEnabled: true, FullDebugEnabled: true},
		ExecutionPermissionCapabilities: domain.ExecutionPermissionRuntimeCapabilities{
			OperatorApprovalEnabled: true, DangerFullAccessEnabled: true},
		FullCDPSessionController: stub})
	if err != nil {
		t.Fatal(err)
	}
	path := strings.ReplaceAll(FullCDPSessionControlPathTemplate,
		"{run_id}", fixture.run.ID)
	response := performRequest(t, api, http.MethodGet, path, testAccessToken,
		"127.0.0.1:8765", "127.0.0.1:45000", nil)
	var view FullCDPSessionControlView
	decodeDataStatus(t, response, http.StatusOK, &view)
	if view.Session.State != string(application.FullCDPSessionClosing) ||
		view.Session.CloseReason != application.FullCDPCloseOpenFailed ||
		!view.Session.CDPClosed || view.Session.StartedAt != nil ||
		view.Session.ExpiresAt != nil || view.Session.TargetOrigin != "" {
		t.Fatalf("unexpected pre-ready Full CDP recovery projection: %+v", view)
	}
	assertFullCDPHTTPProjectionRedacted(t, response)
}

func TestFullCDPSessionHTTPRequiresEnabledController(t *testing.T) {
	fixture := newAPIFixture(t)
	if _, err := New(fixture.store, Config{AccessToken: testAccessToken,
		ControlToken: testControlToken, FullCDPSessionControlEnabled: true}); err == nil {
		t.Fatal("enabled Full CDP HTTP API accepted a nil controller")
	}
	disabled, err := New(fixture.store, Config{AccessToken: testAccessToken,
		ControlToken:             testControlToken,
		FullCDPSessionController: &fullCDPSessionControllerStub{}})
	if err != nil {
		t.Fatal(err)
	}
	path := strings.ReplaceAll(FullCDPSessionControlPathTemplate,
		"{run_id}", fixture.run.ID)
	response := performRequest(t, disabled, http.MethodGet, path, testAccessToken,
		"127.0.0.1:8765", "127.0.0.1:45000", nil)
	assertAPIError(t, response, http.StatusNotFound, "NOT_FOUND")
}

func TestFullCDPSessionHTTPRequiresCoherentPermissionCapabilities(t *testing.T) {
	fixture := newAPIFixture(t)
	base := Config{AccessToken: testAccessToken, ControlToken: testControlToken,
		FullCDPSessionControlEnabled:      true,
		ExecutionPermissionControlEnabled: true,
		FullCDPSessionController:          &fullCDPSessionControllerStub{},
		ExecutionPermissionCapabilities: domain.ExecutionPermissionRuntimeCapabilities{
			OperatorApprovalEnabled: true}}
	for name, mutate := range map[string]func(*Config){
		"browser control": func(config *Config) {
			config.BrowserCDPPermissionCapabilities = domain.BrowserCDPPermissionRuntimeCapabilities{
				ControlEnabled: true, FullDebugEnabled: true}
			config.ExecutionPermissionCapabilities.DangerFullAccessEnabled = true
		},
		"full debug": func(config *Config) {
			config.BrowserCDPPermissionControlEnabled = true
			config.BrowserCDPPermissionCapabilities.ControlEnabled = true
			config.ExecutionPermissionCapabilities.DangerFullAccessEnabled = true
		},
		"full access": func(config *Config) {
			config.BrowserCDPPermissionControlEnabled = true
			config.BrowserCDPPermissionCapabilities = domain.BrowserCDPPermissionRuntimeCapabilities{
				ControlEnabled: true, FullDebugEnabled: true}
		},
	} {
		t.Run(name, func(t *testing.T) {
			config := base
			mutate(&config)
			if _, err := New(fixture.store, config); err == nil {
				t.Fatal("incoherent Full CDP HTTP capabilities were accepted")
			}
		})
	}
}

func assertFullCDPHTTPProjectionRedacted(t *testing.T,
	response *httptest.ResponseRecorder,
) {
	t.Helper()
	var envelope struct {
		Data map[string]json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &envelope); err != nil {
		t.Fatal(err)
	}
	var data map[string]json.RawMessage
	if err := json.Unmarshal(envelope.Data["session"], &data); err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"pid", "process_id", "executable_path",
		"profile_path", "profile_directory", "devtools_endpoint", "websocket_url",
		"token", "permission_snapshot_id", "runtime_fence",
		"authorization_fingerprint"} {
		if _, exists := data[forbidden]; exists {
			t.Fatalf("Full CDP HTTP projection exposed %q: %s", forbidden,
				response.Body.String())
		}
	}
}
