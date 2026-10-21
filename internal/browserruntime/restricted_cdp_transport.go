package browserruntime

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"cyberagent-workbench/internal/domain"
	"github.com/gorilla/websocket"
)

const (
	RestrictedNavigationProtocolVersion  = "restricted_browser_navigation.v1"
	RestrictedDOMMetadataProtocolVersion = "restricted_browser_dom_metadata.v1"
	RestrictedScreenshotProtocolVersion  = "restricted_browser_screenshot.v1"
	DevToolsActivePortFileName           = "DevToolsActivePort"
	MaxDevToolsActivePortBytes           = 4 * 1024
	MaxRestrictedCDPWireBytes            = 12 * 1024 * 1024
	MaxRestrictedCDPTokenBytes           = 1024
	RestrictedCDPOperationTimeout        = 5 * time.Second
)

type restrictedCDPMethodScope uint8

const (
	restrictedCDPBrowserMethod restrictedCDPMethodScope = iota + 1
	restrictedCDPTargetMethod
)

var restrictedCDPMethods = map[string]restrictedCDPMethodScope{
	"Target.createBrowserContext":    restrictedCDPBrowserMethod,
	"Target.createTarget":            restrictedCDPBrowserMethod,
	"Target.attachToTarget":          restrictedCDPBrowserMethod,
	"Target.closeTarget":             restrictedCDPBrowserMethod,
	"Target.disposeBrowserContext":   restrictedCDPBrowserMethod,
	"Browser.setDownloadBehavior":    restrictedCDPBrowserMethod,
	"Page.enable":                    restrictedCDPTargetMethod,
	"DOM.enable":                     restrictedCDPTargetMethod,
	"Network.enable":                 restrictedCDPTargetMethod,
	"Network.setBlockedURLs":         restrictedCDPTargetMethod,
	"Network.setBypassServiceWorker": restrictedCDPTargetMethod,
	"Network.setCacheDisabled":       restrictedCDPTargetMethod,
	"Fetch.enable":                   restrictedCDPTargetMethod,
	"Fetch.continueRequest":          restrictedCDPTargetMethod,
	"Fetch.failRequest":              restrictedCDPTargetMethod,
	"Page.navigate":                  restrictedCDPTargetMethod,
	"DOM.getDocument":                restrictedCDPTargetMethod,
	"DOM.performSearch":              restrictedCDPTargetMethod,
	"DOM.discardSearchResults":       restrictedCDPTargetMethod,
	"Page.getLayoutMetrics":          restrictedCDPTargetMethod,
	"Page.captureScreenshot":         restrictedCDPTargetMethod,
}

// uiEvidenceCDPMethods is an additional closed set admitted only by an
// AuthorizeUIEvidenceCDP-derived session. In particular it contains no script
// evaluation, cookie, response-body, request mutation, or request replay API.
var uiEvidenceCDPMethods = map[string]restrictedCDPMethodScope{
	"Accessibility.enable":               restrictedCDPTargetMethod,
	"Accessibility.getFullAXTree":        restrictedCDPTargetMethod,
	"DOM.focus":                          restrictedCDPTargetMethod,
	"DOM.getBoxModel":                    restrictedCDPTargetMethod,
	"DOM.getOuterHTML":                   restrictedCDPTargetMethod,
	"DOM.querySelector":                  restrictedCDPTargetMethod,
	"DOM.scrollIntoViewIfNeeded":         restrictedCDPTargetMethod,
	"Emulation.setDeviceMetricsOverride": restrictedCDPTargetMethod,
	"Emulation.setEmulatedMedia":         restrictedCDPTargetMethod,
	"Emulation.setLocaleOverride":        restrictedCDPTargetMethod,
	"Input.dispatchMouseEvent":           restrictedCDPTargetMethod,
	"Input.insertText":                   restrictedCDPTargetMethod,
	"Log.enable":                         restrictedCDPTargetMethod,
	"Performance.enable":                 restrictedCDPTargetMethod,
	"Performance.getMetrics":             restrictedCDPTargetMethod,
	"Runtime.enable":                     restrictedCDPTargetMethod,
}

// fullCDPMethods is the additional, highly-sensitive CDP method set admitted
// only under a confirmed FullCDPAuthorization. It never includes methods that
// disable browser security.
var fullCDPMethods = map[string]restrictedCDPMethodScope{
	"Accessibility.enable":        restrictedCDPTargetMethod,
	"Accessibility.getFullAXTree": restrictedCDPTargetMethod,
	"DOM.describeNode":            restrictedCDPTargetMethod,
	"DOM.focus":                   restrictedCDPTargetMethod,
	"DOM.getBoxModel":             restrictedCDPTargetMethod,
	"DOM.getOuterHTML":            restrictedCDPTargetMethod,
	"DOM.querySelector":           restrictedCDPTargetMethod,
	"DOM.scrollIntoViewIfNeeded":  restrictedCDPTargetMethod,
	"Network.getCookies":          restrictedCDPTargetMethod,
	"Network.getAllCookies":       restrictedCDPTargetMethod,
	"Storage.getCookies":          restrictedCDPTargetMethod,
	"Fetch.fulfillRequest":        restrictedCDPTargetMethod,
	"Input.dispatchMouseEvent":    restrictedCDPTargetMethod,
	"Input.insertText":            restrictedCDPTargetMethod,
	"Page.getFrameTree":           restrictedCDPTargetMethod,
	"Runtime.enable":              restrictedCDPTargetMethod,
	"Runtime.evaluate":            restrictedCDPTargetMethod,
	"Log.enable":                  restrictedCDPTargetMethod,
}

type RestrictedNavigationResult struct {
	ProtocolVersion    string    `json:"protocol_version"`
	Authorization      string    `json:"authorization_fingerprint"`
	CanonicalURL       string    `json:"canonical_url"`
	AllowedRequests    int       `json:"allowed_requests"`
	BlockedRequests    int       `json:"blocked_requests"`
	ScopeValidated     bool      `json:"scope_validated"`
	RedirectsValidated bool      `json:"redirects_validated"`
	UntrustedEvidence  bool      `json:"untrusted_evidence"`
	CompletedAt        time.Time `json:"completed_at"`
	Fingerprint        string    `json:"fingerprint"`
}

type RestrictedDOMMetadata struct {
	ProtocolVersion   string    `json:"protocol_version"`
	Authorization     string    `json:"authorization_fingerprint"`
	CanonicalURL      string    `json:"canonical_url"`
	RootNodeName      string    `json:"root_node_name"`
	RootChildCount    int       `json:"root_child_count"`
	ElementCount      int       `json:"element_count"`
	FormCount         int       `json:"form_count"`
	ScriptCount       int       `json:"script_count"`
	ViewportWidth     int       `json:"viewport_width"`
	ViewportHeight    int       `json:"viewport_height"`
	TextIncluded      bool      `json:"text_included"`
	InstructionsUsed  bool      `json:"instructions_used"`
	UntrustedEvidence bool      `json:"untrusted_evidence"`
	CompletedAt       time.Time `json:"completed_at"`
	Fingerprint       string    `json:"fingerprint"`
}

type RestrictedScreenshot struct {
	ProtocolVersion   string    `json:"protocol_version"`
	Authorization     string    `json:"authorization_fingerprint"`
	CanonicalURL      string    `json:"canonical_url"`
	MediaType         string    `json:"media_type"`
	Bytes             int       `json:"bytes"`
	SHA256            string    `json:"sha256"`
	PNG               []byte    `json:"-"`
	UntrustedEvidence bool      `json:"untrusted_evidence"`
	CompletedAt       time.Time `json:"completed_at"`
	Fingerprint       string    `json:"fingerprint"`
}

type RestrictedBrowserSession struct {
	authorization RestrictedCDPAuthorization
	start         BrowserStartAuthorization
	session       SessionPlan
	permission    domain.RunBrowserCDPPermissionSnapshot
	ownership     ProfileOwnershipPlan
	profileLease  ProfileRuntimeLease
	process       *BrowserProcess
	client        *restrictedCDPClient
	operation     chan struct{}
	closed        chan struct{}
}

type restrictedCDPClient struct {
	conn             *websocket.Conn
	scope            TargetScope
	maxRequests      int
	nextID           int64
	browserContextID string
	targetID         string
	sessionID        string
	pageLoaded       bool
	allowedRequests  int
	blockedRequests  int
	blockedDocument  bool
	budgetErr        error
	capturedRequests []capturedRequestMetadata
	consoleEntries   []UIEvidenceConsoleEntry
	pageErrors       []UIEvidencePageError
	networkEntries   []UIEvidenceNetworkEntry
	networkPending   map[string]struct{}
	fullCDP          bool
	uiEvidence       bool
}

// capturedRequestMetadata is bounded request metadata only. It never retains
// headers, cookies, bodies, or any secret-bearing field.
type capturedRequestMetadata struct {
	URL          string `json:"url"`
	Method       string `json:"method"`
	ResourceType string `json:"resource_type"`
}

type cdpWireMessage struct {
	ID        int64           `json:"id,omitempty"`
	Method    string          `json:"method,omitempty"`
	SessionID string          `json:"sessionId,omitempty"`
	Params    json.RawMessage `json:"params,omitempty"`
	Result    json.RawMessage `json:"result,omitempty"`
	Error     *cdpWireError   `json:"error,omitempty"`
}

type cdpWireError struct {
	Code int `json:"code"`
}

func OpenRestrictedBrowserSession(ctx context.Context,
	authorization RestrictedCDPAuthorization, start BrowserStartAuthorization,
	session SessionPlan, identity BrowserExecutableIdentity,
	acceptance BrowserAcceptanceCandidate, ownership ProfileOwnershipPlan,
	attempt BrowserLaunchAttempt, launchLease BrowserLaunchLease,
	review BrowserLaunchReview,
	networkEvidence BrowserNetworkContainmentEvidence,
	networkReview BrowserNetworkContainmentReview,
	networkPlan BrowserNetworkContainmentPlan,
	permission domain.RunBrowserCDPPermissionSnapshot,
	profileLease ProfileRuntimeLease, process *BrowserProcess,
) (*RestrictedBrowserSession, error) {
	if err := validateRestrictedCDPSessionAuthorization(authorization, start, session,
		permission); err != nil {
		return nil, err
	}
	if err := ValidateBrowserStartAuthorization(start, session, identity, acceptance,
		ownership, attempt, launchLease, review, networkEvidence, networkReview,
		networkPlan, permission); err != nil {
		return nil, err
	}
	if err := ValidateProfileRuntimeLease(profileLease, start, ownership); err != nil {
		return nil, err
	}
	if process == nil || process.PID() <= 0 {
		return nil, errors.New("restricted CDP requires the exact live browser process")
	}
	if _, exited := process.Exit(); exited {
		return nil, errors.New("restricted CDP browser process already exited")
	}
	spec := process.StartSpec()
	if err := validateBrowserStartSpec(spec, start, identity, ownership,
		profileLease, networkPlan, spec.NetworkContainmentFingerprint); err != nil {
		return nil, err
	}
	marker, err := readProfileOwnerMarker(ownership.DirectoryPath)
	if err != nil || !markerMatchesOwnership(marker, ownership) ||
		marker.State != ProfileMarkerActive || marker.Fingerprint != profileLease.MarkerFingerprint {
		return nil, errors.New("restricted CDP Profile is not the exact active owner")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	openContext, cancel := boundedRestrictedCDPContext(ctx, authorization.ExpiresAt)
	defer cancel()
	endpoint, err := waitForDevToolsEndpoint(openContext, profileLease.DirectoryPath, process)
	if err != nil {
		return nil, err
	}
	connection, err := dialRestrictedCDP(openContext, endpoint)
	if err != nil {
		return nil, err
	}
	client := &restrictedCDPClient{conn: connection, scope: session.Scope,
		maxRequests: attempt.MaxRequests, uiEvidence: authorization.UIEvidenceAuthorized}
	if err := client.initialize(openContext); err != nil {
		_ = connection.Close()
		return nil, err
	}
	runtime := &RestrictedBrowserSession{
		authorization: authorization, start: start, session: session,
		permission: permission, ownership: ownership, profileLease: profileLease,
		process: process, client: client, operation: make(chan struct{}, 1),
		closed: make(chan struct{}),
	}
	runtime.operation <- struct{}{}
	go runtime.closeWhenProcessExits()
	return runtime, nil
}

func (runtime *RestrictedBrowserSession) Navigate(ctx context.Context,
	rawURL string,
) (RestrictedNavigationResult, error) {
	if runtime == nil || !runtime.authorization.NavigateAuthorized {
		return RestrictedNavigationResult{}, ErrBrowserRuntimeBoundary
	}
	decision := runtime.session.Scope.AuthorizeNavigation(rawURL)
	if !decision.Allowed {
		return RestrictedNavigationResult{}, errors.New("restricted navigation target is outside the exact scope")
	}
	release, operationContext, cancel, err := runtime.beginOperation(ctx)
	if err != nil {
		return RestrictedNavigationResult{}, err
	}
	defer release()
	defer cancel()
	allowedBefore, blockedBefore := runtime.client.allowedRequests,
		runtime.client.blockedRequests
	runtime.client.pageLoaded = false
	runtime.client.blockedDocument = false
	var navigation struct {
		FrameID   string `json:"frameId"`
		ErrorText string `json:"errorText"`
	}
	if err := runtime.client.call(operationContext, runtime.client.sessionID,
		"Page.navigate", map[string]any{"url": decision.CanonicalURL}, &navigation); err != nil {
		return RestrictedNavigationResult{}, err
	}
	if navigation.ErrorText != "" || runtime.client.blockedDocument {
		return RestrictedNavigationResult{}, errors.New("restricted navigation was blocked")
	}
	if err := runtime.client.waitForPageLoad(operationContext); err != nil {
		return RestrictedNavigationResult{}, err
	}
	finalURL, _, _, err := runtime.client.documentIdentity(operationContext)
	if err != nil {
		return RestrictedNavigationResult{}, err
	}
	result := RestrictedNavigationResult{
		ProtocolVersion: RestrictedNavigationProtocolVersion,
		Authorization:   runtime.authorization.Fingerprint, CanonicalURL: finalURL,
		AllowedRequests: runtime.client.allowedRequests - allowedBefore,
		BlockedRequests: runtime.client.blockedRequests - blockedBefore,
		ScopeValidated:  true, RedirectsValidated: true, UntrustedEvidence: true,
		CompletedAt: time.Now().UTC(),
	}
	result.Fingerprint = browserRuntimeFingerprint(result)
	return result, nil
}

func (runtime *RestrictedBrowserSession) DOMMetadata(ctx context.Context,
) (RestrictedDOMMetadata, error) {
	if runtime == nil || !runtime.authorization.DOMMetadataAuthorized {
		return RestrictedDOMMetadata{}, ErrBrowserRuntimeBoundary
	}
	release, operationContext, cancel, err := runtime.beginOperation(ctx)
	if err != nil {
		return RestrictedDOMMetadata{}, err
	}
	defer release()
	defer cancel()
	canonicalURL, rootName, childCount, err := runtime.client.documentIdentity(operationContext)
	if err != nil {
		return RestrictedDOMMetadata{}, err
	}
	elements, err := runtime.client.searchCount(operationContext, "*")
	if err != nil {
		return RestrictedDOMMetadata{}, err
	}
	forms, err := runtime.client.searchCount(operationContext, "form")
	if err != nil {
		return RestrictedDOMMetadata{}, err
	}
	scripts, err := runtime.client.searchCount(operationContext, "script")
	if err != nil {
		return RestrictedDOMMetadata{}, err
	}
	var metrics struct {
		CSSLayoutViewport struct {
			ClientWidth  float64 `json:"clientWidth"`
			ClientHeight float64 `json:"clientHeight"`
		} `json:"cssLayoutViewport"`
	}
	if err := runtime.client.call(operationContext, runtime.client.sessionID,
		"Page.getLayoutMetrics", map[string]any{}, &metrics); err != nil {
		return RestrictedDOMMetadata{}, err
	}
	width, height, err := boundedViewport(metrics.CSSLayoutViewport.ClientWidth,
		metrics.CSSLayoutViewport.ClientHeight)
	if err != nil {
		return RestrictedDOMMetadata{}, err
	}
	metadata := RestrictedDOMMetadata{
		ProtocolVersion: RestrictedDOMMetadataProtocolVersion,
		Authorization:   runtime.authorization.Fingerprint, CanonicalURL: canonicalURL,
		RootNodeName: rootName, RootChildCount: childCount, ElementCount: elements,
		FormCount: forms, ScriptCount: scripts, ViewportWidth: width,
		ViewportHeight: height, UntrustedEvidence: true, CompletedAt: time.Now().UTC(),
	}
	metadata.Fingerprint = browserRuntimeFingerprint(metadata)
	return metadata, nil
}

func (runtime *RestrictedBrowserSession) Screenshot(ctx context.Context,
) (RestrictedScreenshot, error) {
	if runtime == nil || !runtime.authorization.ScreenshotAuthorized {
		return RestrictedScreenshot{}, ErrBrowserRuntimeBoundary
	}
	release, operationContext, cancel, err := runtime.beginOperation(ctx)
	if err != nil {
		return RestrictedScreenshot{}, err
	}
	defer release()
	defer cancel()
	canonicalURL, _, _, err := runtime.client.documentIdentity(operationContext)
	if err != nil {
		return RestrictedScreenshot{}, err
	}
	var capture struct {
		Data string `json:"data"`
	}
	if err := runtime.client.call(operationContext, runtime.client.sessionID,
		"Page.captureScreenshot", map[string]any{"format": "png", "fromSurface": true,
			"captureBeyondViewport": false}, &capture); err != nil {
		return RestrictedScreenshot{}, err
	}
	png, err := decodeRestrictedScreenshot(capture.Data,
		minInt(MaxScreenshotBytes, runtime.session.Limits.MaxResponseBytes))
	if err != nil {
		return RestrictedScreenshot{}, err
	}
	digest := sha256.Sum256(png)
	result := RestrictedScreenshot{
		ProtocolVersion: RestrictedScreenshotProtocolVersion,
		Authorization:   runtime.authorization.Fingerprint, CanonicalURL: canonicalURL,
		MediaType: "image/png", Bytes: len(png), SHA256: hex.EncodeToString(digest[:]),
		PNG: append([]byte(nil), png...), UntrustedEvidence: true,
		CompletedAt: time.Now().UTC(),
	}
	result.Fingerprint = browserRuntimeFingerprint(result)
	return result, nil
}

func (runtime *RestrictedBrowserSession) Close(ctx context.Context) error {
	if runtime == nil {
		return nil
	}
	select {
	case <-runtime.operation:
	case <-runtime.closed:
		return nil
	case <-contextDone(ctx):
		return contextError(ctx)
	}
	close(runtime.closed)
	closeContext, cancel := boundedRestrictedCDPContext(ctx,
		time.Now().Add(time.Second))
	defer cancel()
	return runtime.client.close(closeContext)
}

func (runtime *RestrictedBrowserSession) beginOperation(ctx context.Context,
) (func(), context.Context, context.CancelFunc, error) {
	if err := validateRestrictedCDPSessionAuthorization(runtime.authorization, runtime.start,
		runtime.session, runtime.permission); err != nil {
		return nil, nil, nil, err
	}
	if !time.Now().UTC().Before(runtime.authorization.ExpiresAt) {
		return nil, nil, nil, errors.New("restricted CDP authorization expired")
	}
	if _, exited := runtime.process.Exit(); exited {
		return nil, nil, nil, errors.New("restricted browser process exited")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	select {
	case <-ctx.Done():
		return nil, nil, nil, ctx.Err()
	case <-runtime.closed:
		return nil, nil, nil, errors.New("restricted browser session is closed")
	case <-runtime.operation:
	}
	operationContext, cancel := boundedRestrictedCDPContext(ctx,
		runtime.authorization.ExpiresAt)
	release := func() {
		select {
		case runtime.operation <- struct{}{}:
		case <-runtime.closed:
		}
	}
	return release, operationContext, cancel, nil
}

func validateRestrictedCDPSessionAuthorization(authorization RestrictedCDPAuthorization,
	start BrowserStartAuthorization, session SessionPlan,
	permission domain.RunBrowserCDPPermissionSnapshot,
) error {
	if authorization.UIEvidenceAuthorized {
		return ValidateUIEvidenceCDPAuthorization(authorization, start, session, permission)
	}
	return ValidateRestrictedCDPAuthorization(authorization, start, session, permission)
}

func (runtime *RestrictedBrowserSession) closeWhenProcessExits() {
	select {
	case <-runtime.process.Done():
		_ = runtime.Close(context.Background())
	case <-runtime.closed:
	}
}

func (client *restrictedCDPClient) initialize(ctx context.Context) error {
	var contextResult struct {
		BrowserContextID string `json:"browserContextId"`
	}
	if err := client.call(ctx, "", "Target.createBrowserContext",
		map[string]any{"disposeOnDetach": true}, &contextResult); err != nil {
		return err
	}
	if !validRestrictedCDPToken(contextResult.BrowserContextID) {
		return errors.New("chromium returned an invalid browser context identity")
	}
	client.browserContextID = contextResult.BrowserContextID
	if err := client.call(ctx, "", "Browser.setDownloadBehavior", map[string]any{
		"behavior": "deny", "browserContextId": client.browserContextID,
	}, &struct{}{}); err != nil {
		return err
	}
	var targetResult struct {
		TargetID string `json:"targetId"`
	}
	if err := client.call(ctx, "", "Target.createTarget", map[string]any{
		"url": "about:blank", "browserContextId": client.browserContextID,
	}, &targetResult); err != nil {
		return err
	}
	if !validRestrictedCDPToken(targetResult.TargetID) {
		return errors.New("chromium returned an invalid target identity")
	}
	client.targetID = targetResult.TargetID
	var attachResult struct {
		SessionID string `json:"sessionId"`
	}
	if err := client.call(ctx, "", "Target.attachToTarget", map[string]any{
		"targetId": client.targetID, "flatten": true,
	}, &attachResult); err != nil {
		return err
	}
	if !validRestrictedCDPToken(attachResult.SessionID) {
		return errors.New("chromium returned an invalid target session identity")
	}
	client.sessionID = attachResult.SessionID
	commands := []struct {
		method string
		params map[string]any
	}{
		{"Page.enable", map[string]any{}},
		{"DOM.enable", map[string]any{}},
		{"Network.enable", map[string]any{}},
		{"Network.setBlockedURLs", map[string]any{"urls": []string{
			"ws://*", "wss://*", "file://*", "ftp://*"}}},
		{"Network.setBypassServiceWorker", map[string]any{"bypass": true}},
		{"Network.setCacheDisabled", map[string]any{"cacheDisabled": true}},
		{"Fetch.enable", map[string]any{"patterns": []map[string]any{{
			"urlPattern": "*", "requestStage": "Request"}}, "handleAuthRequests": false}},
	}
	for _, command := range commands {
		if err := client.call(ctx, client.sessionID, command.method,
			command.params, &struct{}{}); err != nil {
			return err
		}
	}
	if client.uiEvidence {
		for _, method := range []string{"Runtime.enable", "Log.enable",
			"Performance.enable", "Accessibility.enable"} {
			if err := client.call(ctx, client.sessionID, method, map[string]any{},
				&struct{}{}); err != nil {
				return err
			}
		}
	}
	if client.fullCDP {
		if err := client.call(ctx, client.sessionID, "Accessibility.enable",
			map[string]any{}, &struct{}{}); err != nil {
			return err
		}
	}
	return nil
}

func (client *restrictedCDPClient) call(ctx context.Context, sessionID string,
	method string, params map[string]any, result any,
) error {
	if !client.methodSessionAllowed(method, sessionID) {
		return ErrBrowserRuntimeBoundary
	}
	id, err := client.writeCommand(ctx, sessionID, method, params)
	if err != nil {
		return err
	}
	for {
		message, err := client.readMessage(ctx)
		if err != nil {
			return err
		}
		if message.Method != "" {
			if err := client.handleEvent(ctx, message); err != nil {
				return err
			}
			continue
		}
		if message.ID != id {
			continue
		}
		if message.Error != nil {
			return fmt.Errorf("chromium rejected fixed CDP method %s with code %d",
				method, message.Error.Code)
		}
		if result == nil || len(message.Result) == 0 {
			return nil
		}
		if err := json.Unmarshal(message.Result, result); err != nil {
			return errors.New("chromium returned a malformed fixed-method result")
		}
		return nil
	}
}

func (client *restrictedCDPClient) writeCommand(ctx context.Context,
	sessionID string, method string, params map[string]any,
) (int64, error) {
	if !client.methodSessionAllowed(method, sessionID) {
		return 0, ErrBrowserRuntimeBoundary
	}
	client.nextID++
	message := map[string]any{"id": client.nextID, "method": method,
		"params": params}
	if sessionID != "" {
		message["sessionId"] = sessionID
	}
	if deadline, ok := ctx.Deadline(); ok {
		_ = client.conn.SetWriteDeadline(deadline)
	}
	if err := client.conn.WriteJSON(message); err != nil {
		return 0, errors.New("restricted CDP write failed")
	}
	return client.nextID, nil
}

func (client *restrictedCDPClient) methodSessionAllowed(method string,
	sessionID string,
) bool {
	scope, ok := restrictedCDPMethods[method]
	if !ok && client.uiEvidence {
		scope, ok = uiEvidenceCDPMethods[method]
	}
	if !ok {
		if client.fullCDP {
			scope, ok = fullCDPMethods[method]
		}
		if !ok {
			return false
		}
	}
	if scope == restrictedCDPBrowserMethod {
		return sessionID == ""
	}
	return scope == restrictedCDPTargetMethod && client.sessionID != "" &&
		sessionID == client.sessionID
}

func (client *restrictedCDPClient) readMessage(ctx context.Context) (cdpWireMessage, error) {
	if deadline, ok := ctx.Deadline(); ok {
		_ = client.conn.SetReadDeadline(deadline)
	}
	messageType, raw, err := client.conn.ReadMessage()
	if err != nil {
		if ctx.Err() != nil {
			return cdpWireMessage{}, ctx.Err()
		}
		return cdpWireMessage{}, errors.New("restricted CDP read failed")
	}
	if messageType != websocket.TextMessage || len(raw) == 0 ||
		len(raw) > MaxRestrictedCDPWireBytes {
		return cdpWireMessage{}, errors.New("restricted CDP message type or size is invalid")
	}
	var message cdpWireMessage
	if err := json.Unmarshal(raw, &message); err != nil ||
		(message.ID == 0) == (message.Method == "") {
		return cdpWireMessage{}, errors.New("restricted CDP message envelope is invalid")
	}
	return message, nil
}

func (client *restrictedCDPClient) handleEvent(ctx context.Context,
	message cdpWireMessage,
) error {
	if message.SessionID != client.sessionID {
		return nil
	}
	switch message.Method {
	case "Page.loadEventFired":
		client.pageLoaded = true
		return nil
	case "Fetch.requestPaused":
		var paused struct {
			RequestID    string `json:"requestId"`
			ResourceType string `json:"resourceType"`
			Request      struct {
				URL    string `json:"url"`
				Method string `json:"method"`
			} `json:"request"`
		}
		if err := json.Unmarshal(message.Params, &paused); err != nil ||
			!validRestrictedCDPToken(paused.RequestID) {
			return errors.New("restricted CDP request event is malformed")
		}
		if len(client.capturedRequests) < client.maxRequests {
			client.capturedRequests = append(client.capturedRequests, capturedRequestMetadata{
				URL: paused.Request.URL, Method: paused.Request.Method,
				ResourceType: paused.ResourceType,
			})
		}
		decision := client.scope.AuthorizeNavigation(paused.Request.URL)
		if decision.Allowed && client.allowedRequests+client.blockedRequests < client.maxRequests {
			client.allowedRequests++
			_, err := client.writeCommand(ctx, client.sessionID,
				"Fetch.continueRequest", map[string]any{"requestId": paused.RequestID})
			return err
		}
		client.blockedRequests++
		if paused.ResourceType == "Document" {
			client.blockedDocument = true
		}
		if client.allowedRequests+client.blockedRequests > client.maxRequests {
			client.budgetErr = errors.New("restricted CDP request budget exhausted")
		}
		_, err := client.writeCommand(ctx, client.sessionID, "Fetch.failRequest",
			map[string]any{"requestId": paused.RequestID, "errorReason": "BlockedByClient"})
		return err
	case "Runtime.consoleAPICalled":
		return client.captureConsoleAPICalled(message.Params)
	case "Runtime.exceptionThrown":
		return client.capturePageException(message.Params)
	case "Log.entryAdded":
		return client.captureLogEntry(message.Params)
	case "Network.requestWillBeSent":
		return client.captureNetworkRequest(message.Params)
	case "Network.responseReceived":
		return client.captureNetworkResponse(message.Params)
	case "Network.loadingFailed":
		return client.captureNetworkFailure(message.Params)
	case "Network.loadingFinished":
		return client.captureNetworkFinished(message.Params)
	default:
		return nil
	}
}

func (client *restrictedCDPClient) waitForPageLoad(ctx context.Context) error {
	for !client.pageLoaded {
		if client.budgetErr != nil {
			return client.budgetErr
		}
		message, err := client.readMessage(ctx)
		if err != nil {
			return err
		}
		if message.Method != "" {
			if err := client.handleEvent(ctx, message); err != nil {
				return err
			}
		}
	}
	return client.budgetErr
}

func (client *restrictedCDPClient) documentIdentity(ctx context.Context,
) (string, string, int, error) {
	var document struct {
		Root struct {
			NodeID         int64  `json:"nodeId"`
			NodeName       string `json:"nodeName"`
			ChildNodeCount int    `json:"childNodeCount"`
			DocumentURL    string `json:"documentURL"`
		} `json:"root"`
	}
	if err := client.call(ctx, client.sessionID, "DOM.getDocument",
		map[string]any{"depth": 1, "pierce": false}, &document); err != nil {
		return "", "", 0, err
	}
	decision := client.scope.AuthorizeNavigation(document.Root.DocumentURL)
	if !decision.Allowed || document.Root.NodeID <= 0 ||
		!validDOMNodeName(document.Root.NodeName) || document.Root.ChildNodeCount < 0 ||
		document.Root.ChildNodeCount > 1_000_000 {
		return "", "", 0, errors.New("restricted DOM identity is outside scope or malformed")
	}
	return decision.CanonicalURL, document.Root.NodeName,
		document.Root.ChildNodeCount, nil
}

func (client *restrictedCDPClient) searchCount(ctx context.Context,
	query string,
) (int, error) {
	if query != "*" && query != "form" && query != "script" {
		return 0, ErrBrowserRuntimeBoundary
	}
	var search struct {
		SearchID    string `json:"searchId"`
		ResultCount int    `json:"resultCount"`
	}
	if err := client.call(ctx, client.sessionID, "DOM.performSearch",
		map[string]any{"query": query, "includeUserAgentShadowDOM": false}, &search); err != nil {
		return 0, err
	}
	if !validRestrictedCDPToken(search.SearchID) || search.ResultCount < 0 ||
		search.ResultCount > 1_000_000 {
		return 0, errors.New("restricted DOM search metadata is malformed")
	}
	if err := client.call(ctx, client.sessionID, "DOM.discardSearchResults",
		map[string]any{"searchId": search.SearchID}, &struct{}{}); err != nil {
		return 0, err
	}
	return search.ResultCount, nil
}

func (client *restrictedCDPClient) close(ctx context.Context) error {
	var closeErr error
	if client.targetID != "" {
		var result struct {
			Success bool `json:"success"`
		}
		if err := client.call(ctx, "", "Target.closeTarget",
			map[string]any{"targetId": client.targetID}, &result); err != nil {
			closeErr = err
		}
	}
	if client.browserContextID != "" {
		if err := client.call(ctx, "", "Target.disposeBrowserContext",
			map[string]any{"browserContextId": client.browserContextID},
			&struct{}{}); err != nil && closeErr == nil {
			closeErr = err
		}
	}
	if err := client.conn.Close(); err != nil && closeErr == nil {
		closeErr = err
	}
	return closeErr
}

func waitForDevToolsEndpoint(ctx context.Context, profilePath string,
	process interface{ Done() <-chan struct{} },
) (*url.URL, error) {
	ticker := time.NewTicker(20 * time.Millisecond)
	defer ticker.Stop()
	for {
		endpoint, pending, err := readDevToolsEndpoint(profilePath)
		if err != nil {
			return nil, err
		}
		if !pending {
			return endpoint, nil
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-process.Done():
			return nil, errors.New("browser exited before publishing its DevTools endpoint")
		case <-ticker.C:
		}
	}
}

func readDevToolsEndpoint(profilePath string) (*url.URL, bool, error) {
	path := filepath.Join(profilePath, DevToolsActivePortFileName)
	if !pathWithinRoot(profilePath, path) {
		return nil, false, ErrBrowserRuntimeBoundary
	}
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, true, nil
	}
	if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 ||
		info.Size() < 0 || info.Size() > MaxDevToolsActivePortBytes ||
		!profilePathHasNoIndirection(path) {
		return nil, false, errors.New("DevTools endpoint file is unavailable or indirect")
	}
	// Chromium creates this direct regular file before publishing its two-line
	// payload. Keep the bounded startup wait pending during that zero-length
	// window; indirection, oversized data, malformed content, process exit, and
	// the caller's deadline remain fail-closed.
	if info.Size() == 0 {
		return nil, true, nil
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, false, err
	}
	raw, readErr := io.ReadAll(io.LimitReader(file, MaxDevToolsActivePortBytes+1))
	closeErr := file.Close()
	if readErr != nil || closeErr != nil ||
		len(raw) > MaxDevToolsActivePortBytes || !utf8.Valid(raw) {
		return nil, false, errors.New("DevTools endpoint file is malformed")
	}
	if len(raw) == 0 {
		return nil, true, nil
	}
	lines := strings.Split(strings.TrimSuffix(string(raw), "\n"), "\n")
	if len(lines) != 2 || strings.HasSuffix(lines[0], "\r") ||
		strings.HasSuffix(lines[1], "\r") {
		return nil, false, errors.New("DevTools endpoint file has an invalid shape")
	}
	port, err := strconv.Atoi(lines[0])
	if err != nil || port < 1 || port > 65535 ||
		!validDevToolsBrowserPath(lines[1]) {
		return nil, false, errors.New("DevTools endpoint file has an invalid address")
	}
	return &url.URL{Scheme: "ws", Host: net.JoinHostPort("127.0.0.1",
		strconv.Itoa(port)), Path: lines[1]}, false, nil
}

func dialRestrictedCDP(ctx context.Context, endpoint *url.URL) (*websocket.Conn, error) {
	if endpoint == nil || endpoint.Scheme != "ws" || endpoint.Hostname() != "127.0.0.1" ||
		!validDevToolsBrowserPath(endpoint.Path) || endpoint.RawQuery != "" ||
		endpoint.Fragment != "" {
		return nil, ErrBrowserRuntimeBoundary
	}
	expectedAddress := endpoint.Host
	// Production callers already bound startup to the session/authority
	// deadline. Do not shorten a cold browser's handshake to the operation
	// budget; callers without a deadline still get the usual five-second cap.
	if _, bounded := ctx.Deadline(); !bounded {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, RestrictedCDPOperationTimeout)
		defer cancel()
	}
	var stopHandshakeCancel func() bool
	dialer := websocket.Dialer{
		NetDialContext: func(ctx context.Context, network string,
			address string,
		) (net.Conn, error) {
			if network != "tcp" || address != expectedAddress {
				return nil, ErrBrowserRuntimeBoundary
			}
			connection, err := (&net.Dialer{}).DialContext(ctx, network, address)
			if err == nil {
				// Gorilla applies the deadline to the HTTP upgrade, but context
				// cancellation alone does not interrupt that socket read.
				stopHandshakeCancel = context.AfterFunc(ctx, func() { _ = connection.Close() })
			}
			return connection, err
		},
	}
	connection, response, err := dialer.DialContext(ctx, endpoint.String(), nil)
	if stopHandshakeCancel != nil {
		stopHandshakeCancel()
	}
	if ctx.Err() != nil {
		if connection != nil {
			_ = connection.Close()
		}
		err = ctx.Err()
	}
	if err != nil {
		if response != nil && response.Body != nil {
			_ = response.Body.Close()
		}
		return nil, fmt.Errorf("connect to restricted chromium DevTools endpoint failed: %w", err)
	}
	remote, ok := connection.UnderlyingConn().RemoteAddr().(*net.TCPAddr)
	if !ok || remote == nil || !remote.IP.IsLoopback() ||
		strconv.Itoa(remote.Port) != endpoint.Port() {
		_ = connection.Close()
		return nil, errors.New("chromium DevTools transport escaped loopback")
	}
	connection.SetReadLimit(MaxRestrictedCDPWireBytes)
	return connection, nil
}

func boundedRestrictedCDPContext(parent context.Context,
	authorizationExpiry time.Time,
) (context.Context, context.CancelFunc) {
	if parent == nil {
		parent = context.Background()
	}
	deadline := time.Now().Add(RestrictedCDPOperationTimeout)
	if !authorizationExpiry.IsZero() && authorizationExpiry.Before(deadline) {
		deadline = authorizationExpiry
	}
	if parentDeadline, ok := parent.Deadline(); ok && parentDeadline.Before(deadline) {
		deadline = parentDeadline
	}
	return context.WithDeadline(parent, deadline)
}

func validDevToolsBrowserPath(path string) bool {
	const prefix = "/devtools/browser/"
	if !strings.HasPrefix(path, prefix) || len(path) > 512 ||
		path != filepath.ToSlash(path) || strings.ContainsAny(path, "?#\\\x00\r\n") {
		return false
	}
	token := strings.TrimPrefix(path, prefix)
	if !validRestrictedCDPToken(token) {
		return false
	}
	for _, character := range token {
		if (character < 'a' || character > 'z') &&
			(character < 'A' || character > 'Z') &&
			(character < '0' || character > '9') && character != '-' && character != '_' {
			return false
		}
	}
	return true
}

func validRestrictedCDPToken(value string) bool {
	return value != "" && len(value) <= MaxRestrictedCDPTokenBytes && utf8.ValidString(value) &&
		strings.TrimSpace(value) == value && !strings.ContainsAny(value, "\x00\r\n")
}

func validDOMNodeName(value string) bool {
	return value != "" && len(value) <= 128 && utf8.ValidString(value) &&
		strings.TrimSpace(value) == value && !strings.ContainsAny(value, "\x00\r\n")
}

func boundedViewport(width float64, height float64) (int, int, error) {
	if math.IsNaN(width) || math.IsNaN(height) || math.IsInf(width, 0) ||
		math.IsInf(height, 0) || width < 0 || height < 0 ||
		width > 100_000 || height > 100_000 {
		return 0, 0, errors.New("restricted layout viewport is outside the fixed bound")
	}
	return int(width), int(height), nil
}

func decodeRestrictedScreenshot(encoded string, maximum int) ([]byte, error) {
	if encoded == "" || maximum <= 0 || len(encoded) > base64.StdEncoding.EncodedLen(maximum)+4 {
		return nil, errors.New("restricted screenshot exceeds its encoded bound")
	}
	decoded := make([]byte, base64.StdEncoding.DecodedLen(len(encoded)))
	written, err := base64.StdEncoding.Strict().Decode(decoded, []byte(encoded))
	if err != nil || written <= 0 || written > maximum {
		return nil, errors.New("restricted screenshot is malformed or oversized")
	}
	decoded = decoded[:written]
	if !bytes.HasPrefix(decoded, []byte("\x89PNG\r\n\x1a\n")) {
		return nil, errors.New("restricted screenshot is not PNG evidence")
	}
	return decoded, nil
}

func contextDone(ctx context.Context) <-chan struct{} {
	if ctx == nil {
		return nil
	}
	return ctx.Done()
}

func contextError(ctx context.Context) error {
	if ctx == nil || ctx.Err() == nil {
		return context.Canceled
	}
	return ctx.Err()
}
