package httpapi

import (
	"net/http"
	"strconv"

	"cyberagent-workbench/internal/apperror"
	"cyberagent-workbench/internal/application"
	"cyberagent-workbench/internal/contextmgr"
)

type memoryCandidateConfirmRequestView struct {
	WorkspaceID           string `json:"workspace_id"`
	ExpectedVersion       int64  `json:"expected_version"`
	ExpectedFingerprint   string `json:"expected_fingerprint"`
	ExpectedSourceVersion int64  `json:"expected_source_version"`
	Confirm               bool   `json:"confirm"`
}

type memoryCandidateRejectRequestView struct {
	WorkspaceID           string `json:"workspace_id"`
	ExpectedVersion       int64  `json:"expected_version"`
	ExpectedFingerprint   string `json:"expected_fingerprint"`
	ExpectedSourceVersion int64  `json:"expected_source_version"`
}

func (a *API) contextMemoryCandidates(request *http.Request, segments []string) (any, *Page, error) {
	if len(segments) > 2 {
		return nil, nil, apperror.New(apperror.CodeNotFound, "memory candidate endpoint was not found")
	}
	store, ok := any(a.store).(application.ContextMemoryCandidateStore)
	if !ok {
		return nil, nil, apperror.New(apperror.CodeFailedPrecondition, "memory candidate store is unavailable")
	}
	service := application.NewContextMemoryCandidateService(store)
	values := request.URL.Query()
	if len(segments) == 2 {
		if err := validateSingleQueryValues(values, "workspace_id"); err != nil {
			return nil, nil, err
		}
		value, err := service.Get(request.Context(), segments[1], values.Get("workspace_id"))
		return value, nil, err
	}
	if err := validateSingleQueryValues(values, "workspace_id", "include_resolved", "limit"); err != nil {
		return nil, nil, err
	}
	includeResolved, err := parseOptionalBool(values.Get("include_resolved"))
	if err != nil {
		return nil, nil, err
	}
	limit := 0
	if raw := values.Get("limit"); raw != "" {
		limit, err = strconv.Atoi(raw)
		if err != nil || limit < 1 {
			return nil, nil, apperror.New(apperror.CodeInvalidArgument, "memory candidate limit must be a positive integer")
		}
	}
	items, err := service.List(request.Context(), contextmgr.MemoryCandidateFilter{WorkspaceID: values.Get("workspace_id"), IncludeResolved: includeResolved, Limit: limit})
	return items, nil, err
}

func (a *API) serveContextMemoryCandidateReview(writer http.ResponseWriter, request *http.Request, requestID, id, action string, body []byte) {
	if request.Method != http.MethodPost {
		writer.Header().Set("Allow", http.MethodPost)
		a.writeError(writer, requestID, apperror.New(apperror.CodeInvalidArgument, "memory candidate review only supports POST"), http.StatusMethodNotAllowed)
		return
	}
	store, ok := any(a.store).(application.ContextMemoryCandidateStore)
	if !ok {
		a.writeError(writer, requestID, apperror.New(apperror.CodeFailedPrecondition, "memory candidate store is unavailable"), 0)
		return
	}
	service := application.NewContextMemoryCandidateService(store)
	var result contextmgr.MemoryCandidateReviewResult
	var err error
	if action == "confirm" {
		var view memoryCandidateConfirmRequestView
		if err = decodeContextContinuityBody(body, &view); err == nil {
			result, err = service.Confirm(request.Context(), id, contextmgr.ReviewMemoryCandidateRequest{WorkspaceID: view.WorkspaceID, ExpectedVersion: view.ExpectedVersion, ExpectedFingerprint: view.ExpectedFingerprint, ExpectedSourceVersion: view.ExpectedSourceVersion, RequestedBy: "http_control", Confirm: view.Confirm})
		}
	} else {
		var view memoryCandidateRejectRequestView
		if err = decodeContextContinuityBody(body, &view); err == nil {
			result, err = service.Reject(request.Context(), id, contextmgr.ReviewMemoryCandidateRequest{WorkspaceID: view.WorkspaceID, ExpectedVersion: view.ExpectedVersion, ExpectedFingerprint: view.ExpectedFingerprint, ExpectedSourceVersion: view.ExpectedSourceVersion, RequestedBy: "http_control"})
		}
	}
	if err != nil {
		a.writeError(writer, requestID, err, 0)
		return
	}
	a.writeSuccess(writer, requestID, result, nil)
}
