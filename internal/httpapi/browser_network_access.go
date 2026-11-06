package httpapi

import (
	"context"
	"net/http"
	"strings"

	"cyberagent-workbench/internal/apperror"
	"cyberagent-workbench/internal/application"
	"cyberagent-workbench/internal/domain"
)

const RunBrowserNetworkAccessPathTemplate = "/api/v1/runs/{run_id}/browser-network"
const WorkspaceBrowserNetworkAccessPathTemplate = "/api/v1/workspaces/{workspace_id}/browser-network"

type BrowserNetworkAccessRequestView struct {
	Version              string                      `json:"version"`
	ExpectedRevision     int64                       `json:"expected_revision"`
	BrowserNetworkAccess domain.BrowserNetworkAccess `json:"browser_network_access"`
	BrowserOrigins       []string                    `json:"browser_origins,omitempty"`
	RememberForProject   bool                        `json:"remember_for_project,omitempty"`
}

type WorkspaceBrowserNetworkAccessRequestView struct {
	Version              string                      `json:"version"`
	ExpectedRevision     int64                       `json:"expected_revision"`
	BrowserNetworkAccess domain.BrowserNetworkAccess `json:"browser_network_access"`
	BrowserOrigins       []string                    `json:"browser_origins,omitempty"`
}

func matchBrowserNetworkAccessPath(path string) (runID, workspaceID string, matched bool) {
	for _, prefix := range []string{"/api/v1/runs/", "/api/v1/workspaces/"} {
		if !strings.HasPrefix(path, prefix) || !strings.HasSuffix(path, "/browser-network") {
			continue
		}
		id := strings.TrimSuffix(strings.TrimPrefix(path, prefix), "/browser-network")
		if id == "" || strings.Contains(id, "/") {
			return "", "", false
		}
		if prefix == "/api/v1/runs/" {
			return id, "", true
		}
		return "", id, true
	}
	return "", "", false
}

func (a *API) serveBrowserNetworkAccess(w http.ResponseWriter, r *http.Request, requestID, runID, workspaceID string) {
	if r.Method != http.MethodGet && r.Method != http.MethodPut {
		w.Header().Set("Allow", "GET, PUT")
		a.writeError(w, requestID, apperror.New(apperror.CodeInvalidArgument, "Unsupported browser network method"), http.StatusMethodNotAllowed)
		return
	}
	token := a.tokenHash
	if r.Method == http.MethodPut {
		if !a.controlEnabled {
			a.writeError(w, requestID, apperror.New(apperror.CodePolicyDenied, "Browser network control is disabled"), http.StatusForbidden)
			return
		}
		token = a.controlTokenHash
	}
	if !a.authorized(r, token) {
		a.writeError(w, requestID, apperror.New(apperror.CodePolicyDenied, "Valid bearer authorization is required"), http.StatusUnauthorized)
		return
	}
	id := runID
	if id == "" {
		id = workspaceID
	}
	if err := validatePathIdentity(id); err != nil {
		a.writeError(w, requestID, err, 0)
		return
	}
	if err := rejectQuery(r.URL.Query()); err != nil {
		a.writeError(w, requestID, err, 0)
		return
	}
	var v domain.BrowserNetworkAccessSelection
	var err error
	if r.Method == http.MethodGet {
		if r.ContentLength != 0 || len(r.TransferEncoding) > 0 {
			a.writeError(w, requestID, apperror.New(apperror.CodeInvalidArgument, "Browser network GET cannot contain a body"), 0)
			return
		}
		v, err = a.store.GetBrowserNetworkAccess(r.Context(), runID, workspaceID)
	} else {
		if err = validateJSONContentType(r.Header); err != nil {
			a.writeError(w, requestID, err, http.StatusUnsupportedMediaType)
			return
		}
		key, keyErr := sessionControlIdempotencyKey(r.Header, "Browser network access")
		if keyErr != nil {
			a.writeError(w, requestID, keyErr, 0)
			return
		}
		var input BrowserNetworkAccessRequestView
		if workspaceID != "" {
			var project WorkspaceBrowserNetworkAccessRequestView
			err = decodeFullCDPSessionRequest(r, "Project browser network access", &project)
			input = BrowserNetworkAccessRequestView{Version: project.Version, ExpectedRevision: project.ExpectedRevision, BrowserNetworkAccess: project.BrowserNetworkAccess, BrowserOrigins: project.BrowserOrigins}
		} else {
			err = decodeFullCDPSessionRequest(r, "Run browser network access", &input)
		}
		if err != nil {
			a.writeFullCDPSessionRequestError(w, requestID, err)
			return
		}
		var browsers interface {
			InvalidateNetworkAccess(context.Context, string) error
		}
		if controller, ok := a.agentBrowserController.(interface {
			InvalidateNetworkAccess(context.Context, string) error
		}); ok {
			browsers = controller
		}
		v, err = application.ChangeBrowserNetworkAccess(r.Context(), a.store, browsers, domain.ChangeBrowserNetworkAccessRequest{Version: input.Version, RunID: runID, WorkspaceID: workspaceID, ExpectedRevision: input.ExpectedRevision, BrowserNetworkAccess: input.BrowserNetworkAccess, BrowserOrigins: input.BrowserOrigins, RememberForProject: input.RememberForProject, OperationKey: key, RequestedBy: "http_control"})
	}
	if err != nil {
		a.writeError(w, requestID, err, 0)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	a.writeSuccessStatus(w, requestID, v, nil, http.StatusOK)
}
