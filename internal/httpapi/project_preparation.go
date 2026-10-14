package httpapi

import (
	"context"
	"net/http"
	"strings"

	"cyberagent-workbench/internal/apperror"
	"cyberagent-workbench/internal/application"
)

const (
	ProjectPreparationPathTemplate          = "/api/v1/runs/{run_id}/project-preparation"
	ProjectPreparationOperationPathTemplate = ProjectPreparationPathTemplate + "/operations/{operation_id}"
	ProjectPreparationCancelPathTemplate    = ProjectPreparationOperationPathTemplate + "/cancel"
)

type ProjectPreparationView = application.ProjectPreparationView
type ProjectPreparationOperationView = application.ProjectPreparationOperationView
type ProjectPreparationRequestView struct {
	Version             string   `json:"version"`
	Target              string   `json:"target"`
	ExpectedFingerprint string   `json:"expected_fingerprint"`
	AllowedSources      []string `json:"allowed_sources"`
	ConfirmExecution    bool     `json:"confirm_execution"`
	RunInstallScripts   bool     `json:"run_install_scripts"`
}
type ProjectPreparationCancelRequestView struct {
	Version string `json:"version"`
}

type ProjectPreparationController interface {
	Inspect(context.Context, string, string) (application.ProjectPreparationView, error)
	Prepare(context.Context, application.PrepareProjectRequest) (application.ProjectPreparationView, error)
	GetOperation(context.Context, string, string) (application.ProjectPreparationOperationView, error)
	Cancel(context.Context, string, string) (application.ProjectPreparationOperationView, error)
}

func matchProjectPreparationPath(path string) (runID, operationID string, cancel, matched bool) {
	parts := strings.Split(strings.TrimPrefix(path, "/api/v1/"), "/")
	if len(parts) < 3 || parts[0] != "runs" || parts[1] == "" || parts[2] != "project-preparation" {
		return
	}
	if len(parts) == 3 {
		return parts[1], "", false, true
	}
	if len(parts) == 5 && parts[3] == "operations" && parts[4] != "" {
		return parts[1], parts[4], false, true
	}
	if len(parts) == 6 && parts[3] == "operations" && parts[4] != "" && parts[5] == "cancel" {
		return parts[1], parts[4], true, true
	}
	return
}

func (a *API) serveProjectPreparation(w http.ResponseWriter, r *http.Request, requestID, runID, operationID string, cancel bool) {
	if a.projectPreparationController == nil {
		a.writeError(w, requestID, apperror.New(apperror.CodeNotFound, "project preparation is unavailable"), 0)
		return
	}
	mutation := r.Method == http.MethodPost
	token := a.tokenHash
	if mutation {
		token = a.controlTokenHash
	}
	if !a.authorized(r, token) {
		w.Header().Set("WWW-Authenticate", `Bearer realm="CyberAgent API"`)
		a.writeError(w, requestID, apperror.New(apperror.CodePolicyDenied, "valid bearer authorization is required"), http.StatusUnauthorized)
		return
	}
	if (cancel && !mutation) || (!cancel && operationID != "" && r.Method != http.MethodGet) || (operationID == "" && r.Method != http.MethodGet && !mutation) {
		w.Header().Set("Allow", http.MethodGet)
		a.writeError(w, requestID, apperror.New(apperror.CodeInvalidArgument, "project preparation method is invalid"), http.StatusMethodNotAllowed)
		return
	}
	if err := validatePathIdentity(runID); err != nil {
		a.writeError(w, requestID, err, 0)
		return
	}
	if operationID != "" {
		if err := validatePathIdentity(operationID); err != nil {
			a.writeError(w, requestID, err, 0)
			return
		}
	}
	if !mutation {
		if r.ContentLength != 0 || len(r.TransferEncoding) > 0 {
			a.writeError(w, requestID, apperror.New(apperror.CodeInvalidArgument, "preparation reads cannot contain a body"), 0)
			return
		}
		var value any
		var err error
		if operationID != "" {
			if err = rejectQuery(r.URL.Query()); err == nil {
				var view application.ProjectPreparationOperationView
				view, err = a.projectPreparationController.GetOperation(r.Context(), runID, operationID)
				if !a.runExecutionEnabled {
					view.CanCancel = false
				}
				value = view
			}
		} else {
			query := r.URL.Query()
			target := "."
			for key, values := range query {
				if key != "target" || len(values) != 1 || values[0] == "" {
					err = apperror.New(apperror.CodeInvalidArgument, "project preparation accepts one target directory")
					break
				}
				target = values[0]
			}
			if err == nil {
				var view application.ProjectPreparationView
				view, err = a.projectPreparationController.Inspect(r.Context(), runID, target)
				if !a.runExecutionEnabled {
					view.Available = false
					view.Reason = "project preparation execution is disabled"
					if view.Latest != nil {
						view.Latest.CanCancel = false
					}
				}
				value = view
			}
		}
		if err != nil {
			a.writeError(w, requestID, err, 0)
			return
		}
		a.writeSuccess(w, requestID, value, nil)
		return
	}
	if !a.runExecutionEnabled {
		a.writeError(w, requestID, apperror.New(apperror.CodeNotFound, "project preparation execution is disabled"), 0)
		return
	}
	if err := rejectQuery(r.URL.Query()); err != nil {
		a.writeError(w, requestID, err, 0)
		return
	}
	key, err := sessionControlIdempotencyKey(r.Header, "project preparation")
	if err != nil {
		a.writeError(w, requestID, err, 0)
		return
	}
	body, err := readStrictControlBody(r, "project preparation")
	if err != nil {
		a.writeError(w, requestID, err, 0)
		return
	}
	if cancel {
		var request ProjectPreparationCancelRequestView
		if err := decodeStrictRunOperation(body, &request, "preparation cancellation"); err != nil {
			a.writeError(w, requestID, err, 0)
			return
		}
		if request.Version != application.ProjectPreparationProtocolVersion {
			a.writeError(w, requestID, apperror.New(apperror.CodeInvalidArgument, "preparation version is invalid"), 0)
			return
		}
		value, err := a.projectPreparationController.Cancel(r.Context(), runID, operationID)
		if err != nil {
			a.writeError(w, requestID, err, 0)
			return
		}
		a.writeSuccess(w, requestID, value, nil)
		return
	}
	var request ProjectPreparationRequestView
	if err := decodeStrictRunOperation(body, &request, "project preparation"); err != nil {
		a.writeError(w, requestID, err, 0)
		return
	}
	if request.Version != application.ProjectPreparationProtocolVersion {
		a.writeError(w, requestID, apperror.New(apperror.CodeInvalidArgument, "preparation version is invalid"), 0)
		return
	}
	value, err := a.projectPreparationController.Prepare(r.Context(), application.PrepareProjectRequest{RunID: runID, OperationKey: key, RequestedBy: "web_operator", Target: request.Target, ExpectedFingerprint: request.ExpectedFingerprint, AllowedSources: request.AllowedSources, ConfirmExecution: request.ConfirmExecution, RunInstallScripts: request.RunInstallScripts})
	if err != nil {
		a.writeError(w, requestID, err, 0)
		return
	}
	a.writeSuccessStatus(w, requestID, value, nil, http.StatusAccepted)
}
