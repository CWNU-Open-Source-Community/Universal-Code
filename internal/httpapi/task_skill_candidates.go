package httpapi

import (
	"net/http"

	"cyberagent-workbench/internal/apperror"
	"cyberagent-workbench/internal/application"
	"cyberagent-workbench/internal/domain"
)

const TaskSkillCandidatesPath = "/api/v1/task-configuration/skills"

func (a *API) serveTaskSkillCandidates(w http.ResponseWriter, r *http.Request, requestID string) {
	if !a.authorized(r, a.tokenHash) {
		w.Header().Set("WWW-Authenticate", `Bearer realm="CyberAgent API"`)
		a.writeError(w, requestID, apperror.New(apperror.CodePolicyDenied, "valid read bearer authorization is required"), http.StatusUnauthorized)
		return
	}
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", http.MethodGet)
		a.writeError(w, requestID, apperror.New(apperror.CodeInvalidArgument, "task Skill candidates require GET"), http.StatusMethodNotAllowed)
		return
	}
	query := r.URL.Query()
	if err := validateSingleQueryValues(query, "workspace_id", "surface", "phase", "profile"); err != nil {
		a.writeError(w, requestID, err, 0)
		return
	}
	for _, key := range []string{"workspace_id", "surface", "phase", "profile"} {
		if len(query[key]) != 1 || query.Get(key) == "" {
			a.writeError(w, requestID, apperror.New(apperror.CodeInvalidArgument, "task Skill scope parameters must appear exactly once"), 0)
			return
		}
	}
	if r.ContentLength != 0 || len(r.TransferEncoding) > 0 {
		a.writeError(w, requestID, apperror.New(apperror.CodeInvalidArgument, "task Skill candidate reads cannot contain a body"), 0)
		return
	}
	store, ok := a.store.(application.TaskSkillCandidatesStore)
	if !ok {
		a.writeError(w, requestID, apperror.New(apperror.CodeFailedPrecondition, "task Skill candidate inventory is unavailable"), 0)
		return
	}
	view, err := application.ListTaskSkillCandidates(r.Context(), store, application.TaskSkillCandidatesRequest{
		WorkspaceID: query.Get("workspace_id"), Surface: domain.ExecutionSurface(query.Get("surface")),
		Phase: domain.ExecutionPhase(query.Get("phase")), Profile: domain.Profile(query.Get("profile")),
	})
	if err != nil {
		a.writeError(w, requestID, err, 0)
		return
	}
	a.writeSuccess(w, requestID, view, nil)
}
