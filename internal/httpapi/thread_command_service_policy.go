package httpapi

import (
	"bytes"
	"encoding/json"
	"net/http"
	"strings"

	"cyberagent-workbench/internal/apperror"
	"cyberagent-workbench/internal/domain"
)

const ThreadCommandServicePolicyPathTemplate = "/api/v1/threads/{thread_id}/command-service-policy"

type ThreadCommandServicePolicyRequest struct {
	Version          string                      `json:"version"`
	Policy           domain.CommandServicePolicy `json:"policy"`
	ExpectedRevision int64                       `json:"expected_revision"`
}

func matchThreadCommandServicePolicyPath(path string) (string, bool) {
	parts := strings.Split(strings.TrimPrefix(path, "/api/v1/"), "/")
	if len(parts) == 3 && parts[0] == "threads" && parts[1] != "" && parts[2] == "command-service-policy" {
		return parts[1], true
	}
	return "", false
}

func (a *API) serveThreadCommandServicePolicy(w http.ResponseWriter, r *http.Request, requestID, threadID string) {
	if a.threadCommandServicePolicy == nil {
		a.writeError(w, requestID, apperror.New(apperror.CodeNotFound, "service policy control is unavailable"), 0)
		return
	}
	token := a.tokenHash
	if r.Method == http.MethodPut {
		if !a.executionPermissionControlEnabled {
			a.writeError(w, requestID, apperror.New(apperror.CodeNotFound, "service policy control is unavailable"), http.StatusNotFound)
			return
		}
		token = a.controlTokenHash
	}
	if !a.authorized(r, token) {
		w.Header().Set("WWW-Authenticate", `Bearer realm="CyberAgent API"`)
		a.writeError(w, requestID, apperror.New(apperror.CodePolicyDenied, "valid bearer authorization is required"), http.StatusUnauthorized)
		return
	}
	if r.Method != http.MethodGet && r.Method != http.MethodPut {
		w.Header().Set("Allow", "GET, PUT")
		a.writeError(w, requestID, apperror.New(apperror.CodeInvalidArgument, "service policy method is invalid"), http.StatusMethodNotAllowed)
		return
	}
	if err := validatePathIdentity(threadID); err != nil {
		a.writeError(w, requestID, err, 0)
		return
	}
	if err := rejectQuery(r.URL.Query()); err != nil {
		a.writeError(w, requestID, err, 0)
		return
	}
	if r.Method == http.MethodGet {
		if r.ContentLength != 0 || len(r.TransferEncoding) != 0 {
			a.writeError(w, requestID, apperror.New(apperror.CodeInvalidArgument, "service policy read cannot contain a body"), 0)
			return
		}
		value, err := a.threadCommandServicePolicy.Get(r.Context(), threadID)
		if err != nil {
			a.writeError(w, requestID, err, 0)
			return
		}
		a.writeSuccess(w, requestID, value, nil)
		return
	}
	if err := validateJSONContentType(r.Header); err != nil {
		a.writeError(w, requestID, err, http.StatusUnsupportedMediaType)
		return
	}
	body, err := readBoundedRequestBody(r, 4096)
	if err == nil {
		err = rejectDuplicateJSONObjectFields(body, "Thread service policy")
	}
	if err != nil {
		a.writeError(w, requestID, err, 0)
		return
	}
	var input ThreadCommandServicePolicyRequest
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	if err = decoder.Decode(&input); err == nil {
		err = ensureJSONEOF(decoder)
	}
	if err != nil || input.Version != domain.ThreadCommandServicePolicyVersion || !input.Policy.Valid() || input.ExpectedRevision < 1 {
		a.writeError(w, requestID, apperror.New(apperror.CodeInvalidArgument, "service policy requires its version, policy, and current revision"), 0)
		return
	}
	value, err := a.threadCommandServicePolicy.Save(r.Context(), threadID, input.ExpectedRevision, input.Policy)
	if err != nil {
		a.writeError(w, requestID, err, 0)
		return
	}
	a.writeSuccess(w, requestID, value, nil)
}
