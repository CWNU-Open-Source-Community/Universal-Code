package httpapi

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"cyberagent-workbench/internal/apperror"
	"cyberagent-workbench/internal/application"
	"cyberagent-workbench/internal/redact"
)

const AgentBrowserStatusPathTemplate = "/api/v1/runs/{run_id}/agent-browser"
const AgentBrowserClosePathTemplate = AgentBrowserStatusPathTemplate + "/close"
const AgentBrowserScreenshotPathTemplate = AgentBrowserStatusPathTemplate + "/screenshot"
const AgentBrowserScreencastPathTemplate = AgentBrowserStatusPathTemplate + "/screencast"

const agentBrowserScreencastHeartbeat = 15 * time.Second

type AgentBrowserController interface {
	GetStatus(context.Context, string) (application.AgentBrowserView, error)
	Close(context.Context, string, string) (application.AgentBrowserView, error)
}

type agentBrowserScreenshotReader interface {
	ReadScreenshot(context.Context, string, string, string) ([]byte, string, error)
}

type AgentBrowserScreenshotView struct {
	Locator  string `json:"locator"`
	SHA256   string `json:"sha256"`
	ByteSize int    `json:"byte_size"`
	MIMEType string `json:"mime_type"`
}

// Public projection intentionally excludes runtime authority, endpoint/profile
// handles and raw backend errors. Reading it never launches a browser.
type AgentBrowserStatusView struct {
	Version              string                      `json:"version"`
	RunID                string                      `json:"run_id"`
	SessionID            string                      `json:"session_id,omitempty"`
	Generation           uint64                      `json:"generation"`
	State                string                      `json:"state"`
	Available            bool                        `json:"available"`
	CanStart             bool                        `json:"can_start"`
	Product              string                      `json:"product,omitempty"`
	Headless             bool                        `json:"headless"`
	URL                  string                      `json:"url,omitempty"`
	Title                string                      `json:"title,omitempty"`
	DocumentEpoch        uint64                      `json:"document_epoch"`
	LastAction           string                      `json:"last_action,omitempty"`
	UpdatedAt            time.Time                   `json:"updated_at"`
	Screenshot           *AgentBrowserScreenshotView `json:"screenshot,omitempty"`
	FailureCode          string                      `json:"failure_code,omitempty"`
	UnavailableCode      string                      `json:"unavailable_code,omitempty"`
	CleanupPending       bool                        `json:"cleanup_pending"`
	TreeReaped           bool                        `json:"tree_reaped"`
	ProfileRemoved       bool                        `json:"profile_removed"`
	ProcessScope         string                      `json:"process_scope,omitempty"`
	ProcessScopeVerified bool                        `json:"process_scope_verified"`
}

type AgentBrowserCloseRequestView struct {
	Version   string `json:"version"`
	SessionID string `json:"session_id"`
}

func matchAgentBrowserPath(path string) (runID string, closeSession bool, matched bool) {
	const prefix = "/api/v1/runs/"
	if !strings.HasPrefix(path, prefix) {
		return "", false, false
	}
	suffix := "/agent-browser"
	if strings.HasSuffix(path, suffix+"/close") {
		suffix += "/close"
		closeSession = true
	} else if !strings.HasSuffix(path, suffix) {
		return "", false, false
	}
	runID = strings.TrimSuffix(strings.TrimPrefix(path, prefix), suffix)
	return runID, closeSession, runID != "" && !strings.Contains(runID, "/")
}

func (a *API) serveAgentBrowser(writer http.ResponseWriter, request *http.Request, requestID, runID string, closeSession bool) {
	if a.agentBrowserController == nil {
		a.writeError(writer, requestID, apperror.New(apperror.CodeNotFound, "Agent browser is unavailable"), http.StatusNotFound)
		return
	}
	method := http.MethodGet
	token := a.tokenHash
	if closeSession {
		method = http.MethodPost
		token = a.controlTokenHash
	}
	if request.Method != method {
		writer.Header().Set("Allow", method)
		a.writeError(writer, requestID, apperror.New(apperror.CodeInvalidArgument, "unsupported Agent browser method"), http.StatusMethodNotAllowed)
		return
	}
	if !a.authorized(request, token) {
		a.writeError(writer, requestID, apperror.New(apperror.CodePolicyDenied, "valid bearer authorization is required"), http.StatusUnauthorized)
		return
	}
	if err := validatePathIdentity(runID); err != nil {
		a.writeError(writer, requestID, err, 0)
		return
	}
	if err := rejectQuery(request.URL.Query()); err != nil {
		a.writeError(writer, requestID, err, 0)
		return
	}
	var value application.AgentBrowserView
	var err error
	if closeSession {
		if err = validateJSONContentType(request.Header); err != nil {
			a.writeError(writer, requestID, err, http.StatusUnsupportedMediaType)
			return
		}
		var input AgentBrowserCloseRequestView
		if err = decodeFullCDPSessionRequest(request, "Agent browser close", &input); err != nil {
			a.writeFullCDPSessionRequestError(writer, requestID, err)
			return
		}
		if input.Version != "agent_browser_close.v1" || validatePathIdentity(input.SessionID) != nil {
			a.writeError(writer, requestID, apperror.New(apperror.CodeInvalidArgument, "Agent browser close requires the exact session"), 0)
			return
		}
		value, err = a.agentBrowserController.Close(request.Context(), runID, input.SessionID)
		if err == nil && value.SessionID != input.SessionID {
			err = apperror.New(apperror.CodeConflict, "Agent browser close result belongs to another session")
		}
	} else {
		if request.ContentLength != 0 || len(request.TransferEncoding) != 0 {
			a.writeError(writer, requestID, apperror.New(apperror.CodeInvalidArgument, "Agent browser GET cannot contain a body"), 0)
			return
		}
		value, err = a.agentBrowserController.GetStatus(request.Context(), runID)
	}
	if err != nil {
		a.writeError(writer, requestID, err, 0)
		return
	}
	view, err := publicAgentBrowserStatus(value, runID)
	if err != nil {
		a.writeError(writer, requestID, err, 0)
		return
	}
	writer.Header().Set("Cache-Control", "no-store")
	a.writeSuccessStatus(writer, requestID, view, nil, http.StatusOK)
}

func publicAgentBrowserStatus(value application.AgentBrowserView, runID string) (AgentBrowserStatusView, error) {
	if value.Version != "agent_browser_status.v1" || value.RunID != runID {
		return AgentBrowserStatusView{}, apperror.New(apperror.CodeConflict, "Agent browser status belongs to another execution")
	}
	view := AgentBrowserStatusView{Version: value.Version, RunID: runID, SessionID: value.SessionID, Generation: value.Generation,
		State: value.State, Available: value.Capabilities.Available, CanStart: value.Capabilities.CanStart, Product: string(value.Product), Headless: value.Headless,
		Title: redact.String(value.Title), DocumentEpoch: value.DocumentEpoch, LastAction: value.LastAction, UpdatedAt: value.UpdatedAt}
	if parsed, err := url.Parse(value.CanonicalURL); err == nil && parsed.User == nil && parsed.Host != "" && (parsed.Scheme == "https" || parsed.Scheme == "http") && redact.String(value.CanonicalURL) == value.CanonicalURL {
		view.URL = value.CanonicalURL
	}
	if value.FailureReason != "" {
		view.FailureCode = "action_failed"
	}
	if !value.Capabilities.Available {
		view.FailureCode = "browser_unavailable"
		switch value.Capabilities.UnavailableCode {
		case "platform_unsupported", "full_access_required", "runtime_unavailable", "run_unavailable", "session_unavailable":
			view.UnavailableCode = value.Capabilities.UnavailableCode
		}
	}
	if value.Cleanup != nil {
		view.CleanupPending = value.Cleanup.CleanupPending
		view.TreeReaped = value.Cleanup.TreeReaped
		view.ProfileRemoved = value.Cleanup.ProfileRemoved
		view.ProcessScope = value.Cleanup.ProcessScope
		view.ProcessScopeVerified = value.Cleanup.ProcessScopeVerified
	}
	if value.ArtifactLocator != "" && isLowerHexSHA256(value.ScreenshotSHA256) && value.ScreenshotBytes > 0 && value.ScreenshotBytes <= 20*1024*1024 {
		view.Screenshot = &AgentBrowserScreenshotView{Locator: value.ArtifactLocator, SHA256: value.ScreenshotSHA256, ByteSize: value.ScreenshotBytes, MIMEType: "image/png"}
	}
	return view, nil
}

func (a *API) serveAgentBrowserScreenshot(w http.ResponseWriter, r *http.Request, requestID, runID string) {
	reader, ok := a.agentBrowserController.(agentBrowserScreenshotReader)
	if !ok {
		a.writeError(w, requestID, apperror.New(apperror.CodeNotFound, "Agent browser image unavailable"), http.StatusNotFound)
		return
	}
	if !a.authorized(r, a.tokenHash) {
		a.writeError(w, requestID, apperror.New(apperror.CodePolicyDenied, "valid bearer authorization is required"), http.StatusUnauthorized)
		return
	}
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", http.MethodGet)
		a.writeError(w, requestID, apperror.New(apperror.CodeInvalidArgument, "Agent browser image requires GET"), http.StatusMethodNotAllowed)
		return
	}
	if validatePathIdentity(runID) != nil || r.ContentLength != 0 || len(r.TransferEncoding) != 0 {
		a.writeError(w, requestID, apperror.New(apperror.CodeInvalidArgument, "invalid Agent browser image request"), 0)
		return
	}
	query := r.URL.Query()
	if err := validateSingleQueryValues(query, "session_id", "artifact_locator", "sha256"); err != nil {
		a.writeError(w, requestID, err, 0)
		return
	}
	sessionID, locator, expected := query.Get("session_id"), query.Get("artifact_locator"), query.Get("sha256")
	if validatePathIdentity(sessionID) != nil || locator == "" || len(locator) > 1024 || redact.String(locator) != locator || !isLowerHexSHA256(expected) {
		a.writeError(w, requestID, apperror.New(apperror.CodeInvalidArgument, "exact browser screenshot identity is required"), 0)
		return
	}
	data, digest, err := reader.ReadScreenshot(r.Context(), runID, sessionID, locator)
	if err != nil {
		a.writeError(w, requestID, err, 0)
		return
	}
	actual := sha256.Sum256(data)
	if len(data) == 0 || len(data) > 20*1024*1024 || digest != expected || hex.EncodeToString(actual[:]) != expected || http.DetectContentType(data) != "image/png" {
		a.writeError(w, requestID, apperror.New(apperror.CodeConflict, "browser screenshot identity verification failed"), 0)
		return
	}
	w.Header().Set("Content-Type", "image/png")
	w.Header().Set("Content-Length", strconv.Itoa(len(data)))
	w.Header().Set("ETag", `"`+digest+`"`)
	w.Header().Set("X-CyberAgent-Content-SHA256", digest)
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write(data)
}

type agentBrowserScreencastStreamer interface {
	StreamScreencast(context.Context, string, func(application.AgentBrowserScreencastFrameView) error) error
}

// AgentBrowserScreencastFrameView is one operator live-view SSE frame. The
// projection re-validates every field against this Run before it leaves the
// Go process and carries no authority, URL, or evidence identity.
type AgentBrowserScreencastFrameView struct {
	Version    string    `json:"version"`
	RunID      string    `json:"run_id"`
	SessionID  string    `json:"session_id"`
	Generation uint64    `json:"generation"`
	MediaType  string    `json:"media_type"`
	Width      int       `json:"width"`
	Height     int       `json:"height"`
	Data       string    `json:"data"`
	CapturedAt time.Time `json:"captured_at"`
}

const maxAgentBrowserScreencastFrameData = 700 * 1024

func publicAgentBrowserScreencastFrame(value application.AgentBrowserScreencastFrameView,
	runID string,
) (AgentBrowserScreencastFrameView, error) {
	if value.Version != application.AgentBrowserScreencastVersion || value.RunID != runID ||
		value.MediaType != "image/jpeg" {
		return AgentBrowserScreencastFrameView{}, apperror.New(apperror.CodeConflict, "Agent browser live view belongs to another execution")
	}
	if validatePathIdentity(value.SessionID) != nil || value.Width < 0 || value.Height < 0 ||
		value.Width > 4096 || value.Height > 4096 || value.Data == "" ||
		len(value.Data) > maxAgentBrowserScreencastFrameData ||
		(value.CapturedAt.IsZero() || value.CapturedAt.After(time.Now().Add(time.Minute))) {
		return AgentBrowserScreencastFrameView{}, apperror.New(apperror.CodeConflict, "Agent browser live view frame is invalid")
	}
	return AgentBrowserScreencastFrameView{Version: value.Version, RunID: runID, SessionID: value.SessionID,
		Generation: value.Generation, MediaType: value.MediaType, Width: value.Width, Height: value.Height,
		Data: value.Data, CapturedAt: value.CapturedAt}, nil
}

// serveAgentBrowserScreencast streams the operator-only live view as SSE. It
// is visibility only: read-scoped, run-bound, launch-free, and it never
// persists frames or touches the hashed screenshot evidence chain.
func (a *API) serveAgentBrowserScreencast(w http.ResponseWriter, request *http.Request, requestID, runID string) {
	streamer, ok := a.agentBrowserController.(agentBrowserScreencastStreamer)
	if !ok {
		a.writeError(w, requestID, apperror.New(apperror.CodeNotFound, "Agent browser live view unavailable"), http.StatusNotFound)
		return
	}
	if !a.authorized(request, a.tokenHash) {
		a.writeError(w, requestID, apperror.New(apperror.CodePolicyDenied, "valid bearer authorization is required"), http.StatusUnauthorized)
		return
	}
	if request.Method != http.MethodGet {
		w.Header().Set("Allow", http.MethodGet)
		a.writeError(w, requestID, apperror.New(apperror.CodeInvalidArgument, "Agent browser live view requires GET"), http.StatusMethodNotAllowed)
		return
	}
	if validatePathIdentity(runID) != nil || request.ContentLength != 0 || len(request.TransferEncoding) != 0 {
		a.writeError(w, requestID, apperror.New(apperror.CodeInvalidArgument, "invalid Agent browser live view request"), 0)
		return
	}
	if err := rejectQuery(request.URL.Query()); err != nil {
		a.writeError(w, requestID, err, 0)
		return
	}
	if current, err := a.agentBrowserController.GetStatus(request.Context(), runID); err != nil {
		a.writeError(w, requestID, err, 0)
		return
	} else if !current.Capabilities.Available {
		a.writeError(w, requestID, apperror.New(apperror.CodeFailedPrecondition, "Agent browser session is not available"), http.StatusConflict)
		return
	}
	controller := http.NewResponseController(w)
	if err := controller.Flush(); err != nil {
		a.writeError(w, requestID, apperror.New(apperror.CodeInternal, "streaming is unsupported on this transport"), 0)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)

	var writeMu sync.Mutex
	flush := func() error { return controller.Flush() }
	emit := func(frame application.AgentBrowserScreencastFrameView) error {
		view, err := publicAgentBrowserScreencastFrame(frame, runID)
		if err != nil {
			return err
		}
		data, err := json.Marshal(view)
		if err != nil {
			return err
		}
		writeMu.Lock()
		defer writeMu.Unlock()
		if _, err := fmt.Fprintf(w, "event: agent_browser.frame\ndata: %s\n\n", data); err != nil {
			return err
		}
		_ = flush()
		return nil
	}
	done := make(chan error, 1)
	go func() { done <- streamer.StreamScreencast(request.Context(), runID, emit) }()
	heartbeat := time.NewTicker(agentBrowserScreencastHeartbeat)
	defer heartbeat.Stop()
	for {
		select {
		case err := <-done:
			if err != nil && !errors.Is(err, context.Canceled) && request.Context().Err() == nil {
				// A terminal but unclassified stream failure ends the SSE body
				// without leaking backend internals; the client reconnects and
				// re-reads the status projection.
				writeMu.Lock()
				_, _ = io.WriteString(w, "event: agent_browser.closed\ndata: {\"version\":\"agent_browser_screencast.v1\",\"reason\":\"ended\"}\n\n")
				_ = flush()
				writeMu.Unlock()
			}
			return
		case <-heartbeat.C:
			writeMu.Lock()
			_, _ = io.WriteString(w, ": keepalive\n\n")
			_ = flush()
			writeMu.Unlock()
		}
	}
}
