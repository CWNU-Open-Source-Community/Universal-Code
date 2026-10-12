package httpapi

import (
	"context"
	"net/http"
	"strings"

	"cyberagent-workbench/internal/apperror"
	"cyberagent-workbench/internal/application"
)

const (
	ProjectActionsPathTemplate   = "/api/v1/runs/{run_id}/project-actions"
	ProjectActionRunPathTemplate = ProjectActionsPathTemplate + "/{slot}"
	ProjectActionJobPathTemplate = ProjectActionsPathTemplate + "/jobs/{job_id}"
)

type ProjectActionsView = application.ProjectActionsView
type ProjectActionView = application.ProjectActionView
type ProjectActionResultView = application.ProjectActionResultView
type ProjectActionJobView = application.ProjectActionJobView

type ProjectActionRequestView struct {
	Version          string `json:"version"`
	Target           string `json:"target"`
	ConfirmExecution bool   `json:"confirm_execution"`
}

type ProjectActionsController interface {
	List(context.Context, string) (application.ProjectActionsView, error)
	Run(context.Context, application.ProjectActionRequest) (application.ProjectActionResultView, error)
	GetJob(context.Context, string, string) (application.ProjectActionResultView, error)
}

func matchProjectActionsPath(path string) (runID, slot, jobID string, matched bool) {
	parts := strings.Split(strings.TrimPrefix(path, "/api/v1/"), "/")
	if len(parts) < 3 || parts[0] != "runs" || parts[1] == "" || parts[2] != "project-actions" {
		return
	}
	if len(parts) == 3 {
		return parts[1], "", "", true
	}
	if len(parts) == 4 && (parts[3] == "test" || parts[3] == "format") {
		return parts[1], parts[3], "", true
	}
	if len(parts) == 5 && parts[3] == "jobs" && parts[4] != "" {
		return parts[1], "", parts[4], true
	}
	return
}

func (a *API) serveProjectActions(w http.ResponseWriter, r *http.Request, requestID, runID, slot, jobID string) {
	if a.projectActionsController == nil || (slot != "" && !a.runExecutionEnabled) {
		a.writeError(w, requestID, apperror.New(apperror.CodeNotFound, "project actions are unavailable"), http.StatusNotFound)
		return
	}
	method, token := http.MethodGet, a.tokenHash
	if slot != "" {
		method, token = http.MethodPost, a.controlTokenHash
	}
	if !a.authorized(r, token) {
		w.Header().Set("WWW-Authenticate", `Bearer realm="CyberAgent API"`)
		a.writeError(w, requestID, apperror.New(apperror.CodePolicyDenied, "valid bearer authorization is required"), http.StatusUnauthorized)
		return
	}
	if r.Method != method {
		w.Header().Set("Allow", method)
		a.writeError(w, requestID, apperror.New(apperror.CodeInvalidArgument, "project action method is invalid"), http.StatusMethodNotAllowed)
		return
	}
	if err := validatePathIdentity(runID); err != nil {
		a.writeError(w, requestID, err, 0)
		return
	}
	if err := rejectQuery(r.URL.Query()); err != nil {
		a.writeError(w, requestID, err, 0)
		return
	}
	if slot == "" {
		if r.ContentLength != 0 || len(r.TransferEncoding) > 0 {
			a.writeError(w, requestID, apperror.New(apperror.CodeInvalidArgument, "project action reads cannot contain a body"), 0)
			return
		}
		var value any
		var err error
		if jobID == "" {
			view, readErr := a.projectActionsController.List(r.Context(), runID)
			if !a.runExecutionEnabled {
				for i := range view.Actions {
					view.Actions[i].Available = false
					view.Actions[i].Reason = "project action execution is disabled"
				}
			}
			value, err = view, readErr
		} else if err = validatePathIdentity(jobID); err == nil {
			view, readErr := a.projectActionsController.GetJob(r.Context(), runID, jobID)
			if !a.runExecutionEnabled {
				view.Job.CanStop = false
			}
			value, err = view, readErr
		}
		if err != nil {
			a.writeError(w, requestID, err, 0)
			return
		}
		a.writeSuccess(w, requestID, value, nil)
		return
	}
	key, err := sessionControlIdempotencyKey(r.Header, "project action")
	if err != nil {
		a.writeError(w, requestID, err, 0)
		return
	}
	body, err := readStrictControlBody(r, "project action")
	if err != nil {
		a.writeError(w, requestID, err, 0)
		return
	}
	var request ProjectActionRequestView
	if err := decodeStrictRunOperation(body, &request, "project action"); err != nil {
		a.writeError(w, requestID, err, 0)
		return
	}
	if request.Version != application.ProjectActionsProtocolVersion {
		a.writeError(w, requestID, apperror.New(apperror.CodeInvalidArgument, "project action version is invalid"), 0)
		return
	}
	value, err := a.projectActionsController.Run(r.Context(), application.ProjectActionRequest{RunID: runID, Slot: slot, Target: request.Target, OperationKey: key, RequestedBy: "web_operator", ConfirmExecution: request.ConfirmExecution})
	if err != nil && value.Job.ID == "" {
		a.writeError(w, requestID, err, 0)
		return
	}
	status := http.StatusAccepted
	if value.Job.State.Terminal() {
		status = http.StatusOK
	}
	a.writeSuccessStatus(w, requestID, value, nil, status)
}
