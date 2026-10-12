package httpapi

import (
	"net/http"
	"strings"

	"cyberagent-workbench/internal/apperror"
	"cyberagent-workbench/internal/application"
)

const ProjectSkillSelectionPathTemplate = "/api/v1/runs/{run_id}/project-skills/select"

type ProjectSkillSelectionRequestView struct {
	Refs                    []string `json:"refs"`
	ConfirmUntrustedContext bool     `json:"confirm_untrusted_context"`
}

func matchProjectSkillSelectionPath(path string) (string, bool) {
	parts := strings.Split(strings.TrimPrefix(path, "/api/v1/"), "/")
	if len(parts) == 4 && parts[0] == "runs" && parts[1] != "" && parts[2] == "project-skills" && parts[3] == "select" {
		return parts[1], true
	}
	return "", false
}

func (a *API) serveProjectSkillSelection(w http.ResponseWriter, r *http.Request, requestID, runID string) {
	const label = "Project Skill selection"
	if !a.authorizeThreadControl(w, r, requestID) {
		return
	}
	if r.Method != http.MethodPost {
		a.writeError(w, requestID, apperror.New(apperror.CodeInvalidArgument, "project Skill selection requires POST"), http.StatusMethodNotAllowed)
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
	key, err := sessionControlIdempotencyKey(r.Header, label)
	if err != nil {
		a.writeError(w, requestID, err, 0)
		return
	}
	var body ProjectSkillSelectionRequestView
	if !a.decodeThreadControlBody(w, r, requestID, label, &body) {
		return
	}
	store, ok := a.store.(application.ProjectSkillSelectionStore)
	if !ok {
		a.writeError(w, requestID, apperror.New(apperror.CodeFailedPrecondition, "project Skill selection is unavailable"), 0)
		return
	}
	result, err := application.SelectProjectSkills(r.Context(), store, application.ProjectSkillSelectionRequest{
		RunID: runID, Refs: body.Refs, OperationKey: key, RequestedBy: "http_project_skill_operator", ConfirmUntrustedContext: body.ConfirmUntrustedContext})
	if err != nil {
		a.writeError(w, requestID, err, 0)
		return
	}
	a.writeSuccess(w, requestID, result, nil)
}
