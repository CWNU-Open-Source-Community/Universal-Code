package httpapi

import (
	"net/http"
	"strings"

	"cyberagent-workbench/internal/apperror"
	"cyberagent-workbench/internal/application"
)

const InstalledSkillSelectionPathTemplate = "/api/v1/runs/{run_id}/external-skills/select"

type InstalledSkillSelectionRequestView struct {
	Refs                    []string `json:"refs"`
	ConfirmUntrustedContext bool     `json:"confirm_untrusted_context"`
}

// matchInstalledSkillSelectionPath keeps the operator selection entry separate
// from the read-only external Skill projection on the parent path.
func matchInstalledSkillSelectionPath(path string) (string, bool) {
	parts := strings.Split(strings.TrimPrefix(path, "/api/v1/"), "/")
	if len(parts) == 4 && parts[0] == "runs" && parts[1] != "" &&
		parts[2] == "external-skills" && parts[3] == "select" {
		return parts[1], true
	}
	return "", false
}

func (a *API) serveInstalledSkillSelection(w http.ResponseWriter, r *http.Request,
	requestID, runID string,
) {
	const label = "Installed Skill selection"
	if !a.authorizeThreadControl(w, r, requestID) {
		return
	}
	if r.Method != http.MethodPost {
		a.writeError(w, requestID, apperror.New(apperror.CodeInvalidArgument,
			"installed Skill selection requires POST"), http.StatusMethodNotAllowed)
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
	var body InstalledSkillSelectionRequestView
	if !a.decodeThreadControlBody(w, r, requestID, label, &body) {
		return
	}
	store, ok := a.store.(application.ProjectSkillSelectionStore)
	if !ok {
		a.writeError(w, requestID, apperror.New(apperror.CodeFailedPrecondition,
			"installed Skill selection is unavailable"), 0)
		return
	}
	result, err := application.SelectInstalledSkills(r.Context(), store,
		application.ProjectSkillSelectionRequest{
			RunID: runID, Refs: body.Refs, OperationKey: key,
			RequestedBy:             "http_installed_skill_operator",
			ConfirmUntrustedContext: body.ConfirmUntrustedContext})
	if err != nil {
		a.writeError(w, requestID, err, 0)
		return
	}
	a.writeSuccess(w, requestID, result, nil)
}
