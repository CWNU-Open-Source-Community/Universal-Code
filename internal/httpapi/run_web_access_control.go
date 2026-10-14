package httpapi

import (
	"bytes"
	"encoding/json"
	"net/http"
	"strings"
	"unicode/utf8"

	"cyberagent-workbench/internal/apperror"
	"cyberagent-workbench/internal/application"
)

const (
	RunWebAccessControlPathTemplate = "/api/v1/runs/{run_id}/web-access"
	MaxRunWebAccessControlBodyBytes = 160 * 1024
)

type RunWebAccessControlRequestView struct {
	Version              string   `json:"version"`
	ExpectedModeRevision int64    `json:"expected_mode_revision"`
	WebAccessMode        string   `json:"web_access_mode"`
	WebAllowedTargets    []string `json:"web_allowed_targets,omitempty"`
	Reason               string   `json:"reason,omitempty"`
}

type RunWebAccessControlView struct {
	Version  string      `json:"version"`
	RunID    string      `json:"run_id"`
	Mode     RunModeView `json:"mode"`
	Replayed bool        `json:"replayed"`
}

func matchRunWebAccessControlPath(requestPath string) (string, bool) {
	return matchRunOperationControlPath(requestPath, "/web-access")
}

func (a *API) serveRunWebAccessControl(writer http.ResponseWriter,
	request *http.Request, requestID string, runID string,
) {
	if !a.authorizeRunOperation(writer, request, requestID,
		a.controlEnabled, "Run web access") {
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
	if err := validateJSONContentType(request.Header); err != nil {
		a.writeError(writer, requestID, err, http.StatusUnsupportedMediaType)
		return
	}
	operationKey, err := sessionControlIdempotencyKey(request.Header,
		"Run web access")
	if err != nil {
		a.writeError(writer, requestID, err, 0)
		return
	}
	body, err := readBoundedRequestBody(request, MaxRunWebAccessControlBodyBytes)
	if err != nil {
		a.writeError(writer, requestID, err, runOperationErrorStatus(err))
		return
	}
	if !utf8.Valid(body) {
		a.writeError(writer, requestID, apperror.New(apperror.CodeInvalidArgument,
			"Run web access body must be valid UTF-8 JSON"), 0)
		return
	}
	if err := rejectDuplicateJSONObjectFields(body, "Run web access"); err != nil {
		a.writeError(writer, requestID, err, 0)
		return
	}
	var view RunWebAccessControlRequestView
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&view); err != nil {
		a.writeError(writer, requestID, apperror.Wrap(apperror.CodeInvalidArgument,
			"Run web access body must be one JSON object", err), 0)
		return
	}
	if err := ensureJSONEOF(decoder); err != nil {
		a.writeError(writer, requestID, err, 0)
		return
	}
	service := application.NewRunWebAccessService(a.store).
		WithRuntimeAuthority(a.executionPermissionCapabilities.RuntimeAuthority)
	result, err := service.Change(request.Context(),
		application.ChangeRunWebAccessRequest{
			Version: view.Version, RunID: strings.TrimSpace(runID),
			ExpectedModeRevision: view.ExpectedModeRevision,
			WebAccessMode:        view.WebAccessMode, WebAllowedTargets: view.WebAllowedTargets,
			OperationKey: operationKey, RequestedBy: "http_control", Reason: view.Reason,
		})
	if err != nil {
		a.writeError(writer, requestID, err, 0)
		return
	}
	a.writeSuccessStatus(writer, requestID, RunWebAccessControlView{
		Version: application.RunWebAccessControlProtocolVersion,
		RunID:   result.Mode.RunID, Mode: runModeView(result.Mode),

		Replayed: result.Replayed,
	}, nil, http.StatusAccepted)
}
