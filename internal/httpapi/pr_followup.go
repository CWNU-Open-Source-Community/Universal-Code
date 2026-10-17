package httpapi

import (
	"context"
	"net/http"
	"strings"

	"cyberagent-workbench/internal/apperror"
	"cyberagent-workbench/internal/application"
	"cyberagent-workbench/internal/domain"
)

type PRFollowupController interface {
	List(context.Context, string) (application.PRFollowupList, error)
	Create(context.Context, string, string, application.PRFollowupCreateRequest) (domain.PRFollowup, error)
	Transition(context.Context, string, string, string, application.PRFollowupTransitionRequest) (domain.PRFollowup, error)
}

type prFollowupRoute struct{ threadID, id, action string }

func matchPRFollowupPath(path string) (prFollowupRoute, bool) {
	p := strings.Split(strings.TrimPrefix(path, "/api/v1/"), "/")
	if (len(p) != 3 && len(p) != 5) || p[0] != "threads" || p[2] != "pr-followups" {
		return prFollowupRoute{}, false
	}
	route := prFollowupRoute{threadID: p[1]}
	if len(p) == 3 {
		return route, true
	}
	route.id, route.action = p[3], p[4]
	switch route.action {
	case "pause", "resume", "cancel":
		return route, true
	default:
		return prFollowupRoute{}, false
	}
}

func (a *API) servePRFollowup(w http.ResponseWriter, r *http.Request, requestID string, route prFollowupRoute) {
	if a.prFollowupController == nil || !a.githubReviewControlEnabled {
		a.writeError(w, requestID, apperror.New(apperror.CodeNotFound, "HTTP API endpoint was not found"), http.StatusNotFound)
		return
	}
	if err := validatePathIdentity(route.threadID); err != nil {
		a.writeError(w, requestID, err, 0)
		return
	}
	if route.action != "" {
		if err := validatePathIdentity(route.id); err != nil {
			a.writeError(w, requestID, err, 0)
			return
		}
	}
	read := route.action == "" && r.Method == http.MethodGet
	token := a.controlTokenHash
	if read {
		token = a.tokenHash
	}
	if !a.authorized(r, token) {
		w.Header().Set("WWW-Authenticate", `Bearer realm="CyberAgent API"`)
		a.writeError(w, requestID, apperror.New(apperror.CodePolicyDenied, "valid bearer authorization is required"), http.StatusUnauthorized)
		return
	}
	if !read && r.Method != http.MethodPost {
		allow := http.MethodPost
		if route.action == "" {
			allow = http.MethodGet + ", " + http.MethodPost
		}
		w.Header().Set("Allow", allow)
		a.writeError(w, requestID, apperror.New(apperror.CodeInvalidArgument, "unsupported PR followup method"), http.StatusMethodNotAllowed)
		return
	}
	var result any
	var err error
	if read {
		if err = rejectQuery(r.URL.Query()); err != nil {
			a.writeError(w, requestID, err, 0)
			return
		}
		if r.ContentLength != 0 || len(r.TransferEncoding) != 0 {
			a.writeError(w, requestID, apperror.New(apperror.CodeInvalidArgument, "PR followup reads cannot contain a body"), 0)
			return
		}
		result, err = a.prFollowupController.List(r.Context(), route.threadID)
	} else if route.action == "" {
		key, keyErr := sessionControlIdempotencyKey(r.Header, "PR followup creation")
		if keyErr != nil {
			a.writeError(w, requestID, keyErr, 0)
			return
		}
		var body application.PRFollowupCreateRequest
		if !a.decodeThreadControlBody(w, r, requestID, "PR followup creation", &body) {
			return
		}
		result, err = a.prFollowupController.Create(r.Context(), route.threadID, key, body)
	} else {
		var body application.PRFollowupTransitionRequest
		if !a.decodeThreadControlBody(w, r, requestID, "PR followup transition", &body) {
			return
		}
		result, err = a.prFollowupController.Transition(r.Context(), route.threadID, route.id, route.action, body)
	}
	if err != nil {
		a.writeError(w, requestID, err, 0)
		return
	}
	a.writeSuccess(w, requestID, result, nil)
}
