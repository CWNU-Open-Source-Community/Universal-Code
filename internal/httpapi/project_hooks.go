package httpapi

import (
	"context"
	"encoding/hex"
	"net/http"
	"strings"

	"cyberagent-workbench/internal/apperror"
	"cyberagent-workbench/internal/application"
)

const ProjectHooksPathTemplate = "/api/v1/threads/{thread_id}/project-hooks"

type ProjectHooksController interface {
	Get(context.Context, string) (application.ProjectHooksView, error)
	Review(context.Context, string, string, application.ProjectHooksReviewRequest) (application.ProjectHooksView, error)
}

func matchProjectHooksPath(path string) (threadID, action string, matched bool) {
	parts := strings.Split(strings.TrimPrefix(path, "/api/v1/"), "/")
	if (len(parts) != 3 && len(parts) != 4) || parts[0] != "threads" || parts[2] != "project-hooks" {
		return
	}
	if len(parts) == 3 {
		return parts[1], "", true
	}
	if parts[3] == "trust" || parts[3] == "disable" {
		return parts[1], parts[3], true
	}
	return
}

func (a *API) serveProjectHooks(w http.ResponseWriter, r *http.Request, requestID, threadID, action string) {
	if a.projectHooksController == nil || (action != "" && !a.controlEnabled) {
		a.writeError(w, requestID, apperror.New(apperror.CodeNotFound, "project hook review is unavailable"), http.StatusNotFound)
		return
	}
	method, token := http.MethodGet, a.tokenHash
	if action != "" {
		method, token = http.MethodPost, a.controlTokenHash
	}
	if !a.authorized(r, token) {
		w.Header().Set("WWW-Authenticate", `Bearer realm="CyberAgent API"`)
		a.writeError(w, requestID, apperror.New(apperror.CodePolicyDenied, "valid bearer authorization is required"), http.StatusUnauthorized)
		return
	}
	if r.Method != method {
		w.Header().Set("Allow", method)
		a.writeError(w, requestID, apperror.New(apperror.CodeInvalidArgument, "project hook review method is invalid"), http.StatusMethodNotAllowed)
		return
	}
	if err := validatePathIdentity(threadID); err != nil {
		a.writeError(w, requestID, err, 0)
		return
	}
	var view application.ProjectHooksView
	var err error
	if action == "" {
		if err = rejectQuery(r.URL.Query()); err != nil {
			a.writeError(w, requestID, err, 0)
			return
		}
		if r.ContentLength != 0 || len(r.TransferEncoding) != 0 {
			a.writeError(w, requestID, apperror.New(apperror.CodeInvalidArgument, "project hook reads cannot contain a body"), 0)
			return
		}
		view, err = a.projectHooksController.Get(r.Context(), threadID)
	} else {
		var body struct {
			Version             string `json:"version"`
			ExpectedFingerprint string `json:"expected_fingerprint"`
			ExpectedRevision    *int64 `json:"expected_revision"`
		}
		if !a.decodeThreadControlBody(w, r, requestID, "project hook review", &body) {
			return
		}
		fingerprint, fingerprintErr := hex.DecodeString(body.ExpectedFingerprint)
		if body.Version != application.ProjectHooksVersion || body.ExpectedRevision == nil || *body.ExpectedRevision < 0 ||
			fingerprintErr != nil || len(fingerprint) != 32 || strings.ToLower(body.ExpectedFingerprint) != body.ExpectedFingerprint {
			a.writeError(w, requestID, apperror.New(apperror.CodeInvalidArgument, "project hook review requires its exact version, fingerprint and nonnegative revision"), 0)
			return
		}
		view, err = a.projectHooksController.Review(r.Context(), threadID, action, application.ProjectHooksReviewRequest{
			Version: body.Version, ExpectedFingerprint: body.ExpectedFingerprint, ExpectedRevision: *body.ExpectedRevision,
		})
	}
	if err != nil {
		a.writeError(w, requestID, err, 0)
		return
	}
	a.writeSuccess(w, requestID, view, nil)
}
