package httpapi

import (
	"bytes"
	"context"
	"encoding/base64"
	"image"
	"image/png"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"cyberagent-workbench/internal/apperror"
	"cyberagent-workbench/internal/application"
	"cyberagent-workbench/internal/webevidence"
)

type agentBrowserHTTPStub struct {
	view                   application.AgentBrowserView
	reads, closes, images  int
	closeRun, closeSession string
	png                    []byte
	digest                 string
}

func (s *agentBrowserHTTPStub) GetStatus(context.Context, string) (application.AgentBrowserView, error) {
	s.reads++
	return s.view, nil
}
func (s *agentBrowserHTTPStub) Close(_ context.Context, runID, sessionID string) (application.AgentBrowserView, error) {
	s.closes++
	s.closeRun, s.closeSession = runID, sessionID
	if sessionID != s.view.SessionID {
		return application.AgentBrowserView{}, apperror.New(apperror.CodeConflict, "different session")
	}
	s.view.State = "closed"
	return s.view, nil
}
func (s *agentBrowserHTTPStub) ReadScreenshot(_ context.Context, runID, sessionID, locator string) ([]byte, string, error) {
	s.images++
	if runID != s.view.RunID || sessionID != s.view.SessionID || locator != s.view.ArtifactLocator {
		return nil, "", apperror.New(apperror.CodeConflict, "different screenshot")
	}
	return s.png, s.digest, nil
}

func TestAgentBrowserHTTPReadCloseAndExactScreenshot(t *testing.T) {
	fixture := newAPIFixture(t)
	var buffer bytes.Buffer
	if err := png.Encode(&buffer, image.NewRGBA(image.Rect(0, 0, 1, 1))); err != nil {
		t.Fatal(err)
	}
	stub := &agentBrowserHTTPStub{png: buffer.Bytes(), digest: webevidence.DigestBytes(buffer.Bytes())}
	stub.view = application.AgentBrowserView{Version: "agent_browser_status.v1", RunID: fixture.run.ID, SessionID: "browser-http-session", Generation: 1,
		State: "ready", Headless: true, CanonicalURL: "https://docs.example.com/report", Title: "Actual page", LastAction: "browser_snapshot", UpdatedAt: time.Now().UTC(),
		ArtifactLocator: "browser-shot.png", ScreenshotSHA256: stub.digest, ScreenshotBytes: len(stub.png), FailureReason: "PRIVATE_BACKEND_PROFILE_PATH",
		Capabilities: application.AgentBrowserCapabilities{Available: true, CanStart: true, RefusalReason: "PRIVATE_REFUSAL"}}
	api, err := New(fixture.store, Config{AccessToken: testAccessToken, ControlToken: testControlToken, AgentBrowserController: stub})
	if err != nil {
		t.Fatal(err)
	}
	path := "/api/v1/runs/" + fixture.run.ID + "/agent-browser"
	response := performRequest(t, api, http.MethodGet, path, testAccessToken, "127.0.0.1:8765", "127.0.0.1:45000", nil)
	var view AgentBrowserStatusView
	decodeDataStatus(t, response, http.StatusOK, &view)
	if stub.reads != 1 || stub.closes != 0 || view.SessionID != stub.view.SessionID || view.Screenshot == nil || view.Screenshot.SHA256 != stub.digest || strings.Contains(response.Body.String(), "PRIVATE_") {
		t.Fatal("status read performed mutation or leaked runtime state")
	}
	query := url.Values{"session_id": {stub.view.SessionID}, "artifact_locator": {stub.view.ArtifactLocator}, "sha256": {stub.digest}}
	imageResponse := performRequest(t, api, http.MethodGet, path+"/screenshot?"+query.Encode(), testAccessToken, "127.0.0.1:8765", "127.0.0.1:45000", nil)
	if imageResponse.Code != http.StatusOK || !bytes.Equal(imageResponse.Body.Bytes(), stub.png) || imageResponse.Header().Get("X-CyberAgent-Content-SHA256") != stub.digest {
		t.Fatal("exact screenshot failed", imageResponse.Code, imageResponse.Body.String())
	}
	query.Set("session_id", "other-session")
	wrong := performRequest(t, api, http.MethodGet, path+"/screenshot?"+query.Encode(), testAccessToken, "127.0.0.1:8765", "127.0.0.1:45000", nil)
	if wrong.Code == http.StatusOK {
		t.Fatal("read screenshot from another session")
	}
	closeBody := `{"version":"agent_browser_close.v1","session_id":"browser-http-session"}`
	denied := performRequest(t, api, http.MethodPost, path+"/close", testAccessToken, "127.0.0.1:8765", "127.0.0.1:45000", strings.NewReader(closeBody))
	if denied.Code != http.StatusUnauthorized || stub.closes != 0 {
		t.Fatal("read token closed browser")
	}
	for i := 0; i < 2; i++ {
		closed := performControlPathRequest(t, api, path+"/close", "close-exact-browser", strings.NewReader(closeBody))
		decodeDataStatus(t, closed, http.StatusOK, &view)
		if view.State != "closed" || stub.closeSession != "browser-http-session" || stub.closeRun != fixture.run.ID {
			t.Fatal("close targeted a different session")
		}
	}
	stub.view.RunID = "another-run"
	wrongRun := performRequest(t, api, http.MethodGet, path, testAccessToken, "127.0.0.1:8765", "127.0.0.1:45000", nil)
	if wrongRun.Code != http.StatusConflict {
		t.Fatal("displayed status from another Run")
	}
}

func TestAgentBrowserHTTPRejectsAmbiguousControl(t *testing.T) {
	fixture := newAPIFixture(t)
	stub := &agentBrowserHTTPStub{view: application.AgentBrowserView{Version: "agent_browser_status.v1", RunID: fixture.run.ID, SessionID: "browser-http-session", State: "idle"}}
	if _, err := New(fixture.store, Config{AccessToken: testAccessToken, AgentBrowserController: stub}); err == nil {
		t.Fatal("browser control configured without separate control token")
	}
	api, err := New(fixture.store, Config{AccessToken: testAccessToken, ControlToken: testControlToken, AgentBrowserController: stub})
	if err != nil {
		t.Fatal(err)
	}
	path := "/api/v1/runs/" + fixture.run.ID + "/agent-browser"
	for _, body := range []string{`{"version":"agent_browser_close.v1","session_id":"one","session_id":"two"}`, `{"version":"agent_browser_close.v1","session_id":"one","confirmed":true}`} {
		response := performControlPathRequest(t, api, path+"/close", "close-invalid-browser", strings.NewReader(body))
		if response.Code != http.StatusBadRequest {
			t.Fatal("ambiguous close accepted", response.Code, response.Body.String())
		}
	}
	if stub.closes != 0 {
		t.Fatal("invalid payload reached controller")
	}
}

type agentBrowserScreencastStub struct {
	agentBrowserHTTPStub
	streams   int
	lastRunID string
	frames    []application.AgentBrowserScreencastFrameView
}

// flushableRecorder lets the SSE handler flush through httptest.ResponseRecorder.
type flushableRecorder struct {
	*httptest.ResponseRecorder
}

func (f *flushableRecorder) Flush() {}

func (s *agentBrowserScreencastStub) StreamScreencast(ctx context.Context, runID string,
	emit func(application.AgentBrowserScreencastFrameView) error,
) error {
	s.streams++
	s.lastRunID = runID
	frames := s.frames
	if len(frames) == 0 {
		frames = []application.AgentBrowserScreencastFrameView{{Version: application.AgentBrowserScreencastVersion,
			RunID: s.view.RunID, SessionID: s.view.SessionID, Generation: s.view.Generation,
			MediaType: "image/jpeg", Width: 1280, Height: 900,
			Data: base64.StdEncoding.EncodeToString([]byte("live-frame")), CapturedAt: time.Now().UTC()}}
	}
	for _, frame := range frames {
		if err := emit(frame); err != nil {
			return err
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
	}
	return nil
}

func TestAgentBrowserScreencastStreamsRunBoundFrames(t *testing.T) {
	fixture := newAPIFixture(t)
	stub := &agentBrowserScreencastStub{}
	stub.view = application.AgentBrowserView{Version: "agent_browser_status.v1", RunID: fixture.run.ID,
		SessionID: "browser-live-session", Generation: 3, State: "ready", Headless: true,
		UpdatedAt: time.Now().UTC(), Capabilities: application.AgentBrowserCapabilities{Available: true}}
	now := time.Now().UTC()
	stub.frames = []application.AgentBrowserScreencastFrameView{
		{Version: application.AgentBrowserScreencastVersion, RunID: fixture.run.ID, SessionID: "browser-live-session",
			Generation: 3, MediaType: "image/jpeg", Width: 1280, Height: 900, Data: "aGVsbG8=", CapturedAt: now},
		{Version: application.AgentBrowserScreencastVersion, RunID: "another-run", SessionID: "browser-live-session",
			Generation: 3, MediaType: "image/jpeg", Width: 1280, Height: 900, Data: "cHJpdmF0ZQ==", CapturedAt: now},
	}
	api, err := New(fixture.store, Config{AccessToken: testAccessToken, ControlToken: testControlToken, AgentBrowserController: stub})
	if err != nil {
		t.Fatal(err)
	}
	path := "/api/v1/runs/" + fixture.run.ID + "/agent-browser/screencast"
	denied := performRequest(t, api, http.MethodGet, path, "not-the-read-token", "127.0.0.1:8765", "127.0.0.1:45000", nil)
	if denied.Code != http.StatusUnauthorized || stub.streams != 0 {
		t.Fatal("live view streamed without a valid read token")
	}
	withQuery := performRequest(t, api, http.MethodGet, path+"?fps=30", testAccessToken, "127.0.0.1:8765", "127.0.0.1:45000", nil)
	if withQuery.Code != http.StatusBadRequest {
		t.Fatal("live view accepted query parameters", withQuery.Code)
	}
	request := httptest.NewRequest(http.MethodGet, "http://127.0.0.1"+path, nil)
	request.Host = "127.0.0.1:8765"
	request.RemoteAddr = "127.0.0.1:45000"
	request.Header.Set("Authorization", "Bearer "+testAccessToken)
	response := &flushableRecorder{ResponseRecorder: httptest.NewRecorder()}
	api.ServeHTTP(response, request)
	if response.Code != http.StatusOK ||
		!strings.HasPrefix(response.Header().Get("Content-Type"), "text/event-stream") {
		t.Fatal("live view did not answer as an event stream", response.Code, response.Header().Get("Content-Type"), response.Body.String())
	}
	body := response.Body.String()
	if strings.Count(body, "event: agent_browser.frame") != 1 || !strings.Contains(body, `"data":"aGVsbG8="`) ||
		strings.Contains(body, "cHJpdmF0ZQ==") || strings.Contains(body, "another-run") {
		t.Fatal("live view leaked a foreign-run frame or dropped the owned one", body)
	}
	if !strings.Contains(body, "event: agent_browser.closed") {
		t.Fatal("live view did not report a terminal close after the foreign frame was rejected", body)
	}
	if stub.streams != 1 || stub.lastRunID != fixture.run.ID {
		t.Fatal("live view reached the controller with the wrong identity")
	}
}

func TestAgentBrowserScreencastUnavailableWithoutStreamer(t *testing.T) {
	fixture := newAPIFixture(t)
	stub := &agentBrowserHTTPStub{view: application.AgentBrowserView{Version: "agent_browser_status.v1",
		RunID: fixture.run.ID, SessionID: "browser-http-session", State: "ready",
		Capabilities: application.AgentBrowserCapabilities{Available: true}}}
	api, err := New(fixture.store, Config{AccessToken: testAccessToken, ControlToken: testControlToken, AgentBrowserController: stub})
	if err != nil {
		t.Fatal(err)
	}
	path := "/api/v1/runs/" + fixture.run.ID + "/agent-browser/screencast"
	response := performRequest(t, api, http.MethodGet, path, testAccessToken, "127.0.0.1:8765", "127.0.0.1:45000", nil)
	if response.Code != http.StatusNotFound {
		t.Fatal("live view offered without a streaming controller", response.Code)
	}
}
