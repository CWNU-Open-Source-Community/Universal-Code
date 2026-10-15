package httpapi

import (
	"net/http"
	"strings"
	"testing"

	"cyberagent-workbench/internal/application"
	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/skills"
)

const installedSkillRef = "api-projection-review@1.0.0"

// newInstalledSkillSelectionRun creates one Created Run. An empty profile keeps the
// product default; installed external fixtures need an explicitly matching profile.
func newInstalledSkillSelectionRun(t *testing.T, f *apiFixture, key, profile string) string {
	t.Helper()
	identity := `"workspace_id":"` + f.workspace.ID + `"`
	if profile != "" {
		identity += `,"profile":"` + profile + `"`
	}
	var created ThreadCreationControlView
	decodeDataStatus(t, performControlPathRequest(t, f.api, ThreadCollectionPath, key,
		strings.NewReader(`{"version":"thread_creation.v1",`+identity+
			`,"goal":"select an installed skill"}`)),
		http.StatusAccepted, &created)
	return created.Run.ID
}

func TestInstalledSkillSelectionAdmitsReplaysAndPreservesCreatedRun(t *testing.T) {
	f := newAPIFixture(t)
	runID := newInstalledSkillSelectionRun(t, f, "installed-skill-create-0001", "review")
	path := "/api/v1/runs/" + runID + "/external-skills/select"

	// The read bearer cannot reach a control route, and the route is POST-only.
	assertAPIError(t, performSessionMessageRequest(t, f.api, http.MethodPost, path,
		testAccessToken, "installed-skill-read-0001", "application/json",
		strings.NewReader(`{"refs":["`+installedSkillRef+`"],"confirm_untrusted_context":true}`)),
		http.StatusUnauthorized, "POLICY_DENIED")
	assertAPIError(t, performControlMethodPathRequest(t, f.api, http.MethodGet, path,
		"installed-skill-get-0001", strings.NewReader(`{}`)),
		http.StatusMethodNotAllowed, "INVALID_ARGUMENT")
	assertAPIError(t, performSessionMessageRequest(t, f.api, http.MethodPost, path+"?extra=1",
		testControlToken, "installed-skill-query-0001", "application/json",
		strings.NewReader(`{"refs":["`+installedSkillRef+`"],"confirm_untrusted_context":true}`)),
		http.StatusBadRequest, "INVALID_ARGUMENT")

	// Nothing is installed for the absent ref, and external context needs confirmation.
	assertAPIError(t, performControlPathRequest(t, f.api, path, "installed-skill-unconfirmed-0001",
		strings.NewReader(`{"refs":["`+installedSkillRef+`"],"confirm_untrusted_context":false}`)),
		http.StatusPreconditionFailed, "FAILED_PRECONDITION")
	assertAPIError(t, performControlPathRequest(t, f.api, path, "installed-skill-absent-0001",
		strings.NewReader(`{"refs":["absent-package@1.0.0"],"confirm_untrusted_context":true}`)),
		http.StatusNotFound, "NOT_FOUND")
	// Duplicate identities are refused before any selection is written.
	assertAPIError(t, performControlPathRequest(t, f.api, path, "installed-skill-duplicate-0001",
		strings.NewReader(`{"refs":["`+installedSkillRef+`","`+installedSkillRef+
			`"],"confirm_untrusted_context":true}`)),
		http.StatusBadRequest, "INVALID_ARGUMENT")
	assertAPIError(t, performControlPathRequest(t, f.api, path, "installed-skill-empty-0001",
		strings.NewReader(`{"refs":[],"confirm_untrusted_context":true}`)),
		http.StatusBadRequest, "INVALID_ARGUMENT")
	assertAPIError(t, performControlPathRequest(t, f.api, path, "installed-skill-malformed-0001",
		strings.NewReader(`{"refs":["no-version"],"confirm_untrusted_context":true}`)),
		http.StatusBadRequest, "INVALID_ARGUMENT")
	// The project-pinned entry keeps its narrower admission rule for the same ref.
	assertAPIError(t, performControlPathRequest(t, f.api,
		"/api/v1/runs/"+runID+"/project-skills/select", "installed-skill-pin-0001",
		strings.NewReader(`{"refs":["`+installedSkillRef+`"],"confirm_untrusted_context":true}`)),
		http.StatusPreconditionFailed, "FAILED_PRECONDITION")

	var selected application.ProjectSkillSelectionResult
	body := `{"refs":["` + installedSkillRef + `"],"confirm_untrusted_context":true}`
	decodeData(t, performControlPathRequest(t, f.api, path, "installed-skill-select-0001",
		strings.NewReader(body)), &selected)
	if selected.RunID != runID || selected.CapabilityGrant || selected.Replayed ||
		selected.BuiltinSelectionID != "" || selected.ExternalSelectionID == "" ||
		strings.Join(selected.Refs, ",") != installedSkillRef {
		t.Fatalf("unexpected installed selection %#v", selected)
	}

	var replay application.ProjectSkillSelectionResult
	decodeData(t, performControlPathRequest(t, f.api, path, "installed-skill-select-0001",
		strings.NewReader(body)), &replay)
	if !replay.Replayed || replay.ExternalSelectionID != selected.ExternalSelectionID {
		t.Fatalf("selection replay drift %#v", replay)
	}
	assertAPIError(t, performControlPathRequest(t, f.api, path, "installed-skill-select-0001",
		strings.NewReader(`{"refs":["code@1.0.0"],"confirm_untrusted_context":false}`)),
		http.StatusConflict, "CONFLICT")

	run, err := f.store.GetRun(t.Context(), runID)
	if err != nil || run.Status != domain.RunCreated {
		t.Fatalf("selection started/lost Run: %s %v", run.Status, err)
	}
	// Selection is metadata-only: the projection still exposes no package content.
	projection := new(ExternalSkillProjectionView)
	decodeData(t, f.get(t, "/api/v1/runs/"+runID+"/external-skills"), projection)
	if projection.ItemCount != 1 || projection.Items[0].Name != "api-projection-review" ||
		projection.ToolCapabilityGrant {
		t.Fatalf("unexpected projection %#v", projection)
	}
}

func TestInstalledSkillSelectionRefusesStartedRunAndUnknownRun(t *testing.T) {
	f := newAPIFixture(t)
	runID := newInstalledSkillSelectionRun(t, f, "installed-skill-create-0002", "review")
	body := `{"refs":["` + installedSkillRef + `"],"confirm_untrusted_context":true}`
	selected := application.ProjectSkillSelectionResult{}
	decodeData(t, performControlPathRequest(t, f.api,
		"/api/v1/runs/"+runID+"/external-skills/select", "installed-skill-select-0002",
		strings.NewReader(body)), &selected)

	run, err := application.NewRunService(f.store).Start(t.Context(), runID)
	if err != nil {
		t.Fatal(err)
	}
	if run.Status == domain.RunCreated {
		t.Fatal("run did not leave Created")
	}
	assertAPIError(t, performControlPathRequest(t, f.api,
		"/api/v1/runs/"+runID+"/external-skills/select", "installed-skill-select-0003",
		strings.NewReader(body)), http.StatusPreconditionFailed, "FAILED_PRECONDITION")
	assertAPIError(t, performControlPathRequest(t, f.api,
		"/api/v1/runs/absent-run-000000/external-skills/select", "installed-skill-select-0004",
		strings.NewReader(body)), http.StatusNotFound, "NOT_FOUND")
}

func TestInstalledSkillSelectionKeepsBuiltInVersionRule(t *testing.T) {
	f := newAPIFixture(t)
	runID := newInstalledSkillSelectionRun(t, f, "installed-skill-create-0003", "")
	path := "/api/v1/runs/" + runID + "/external-skills/select"
	assertAPIError(t, performControlPathRequest(t, f.api, path, "installed-skill-builtin-0001",
		strings.NewReader(`{"refs":["code@99.0.0"],"confirm_untrusted_context":false}`)),
		http.StatusConflict, "CONFLICT")

	registry, err := skills.BuiltinRegistry()
	if err != nil {
		t.Fatal(err)
	}
	manifest, _ := registry.Get("code")
	var selected application.ProjectSkillSelectionResult
	decodeData(t, performControlPathRequest(t, f.api, path, "installed-skill-builtin-0002",
		strings.NewReader(`{"refs":["code@`+manifest.Version+`"],"confirm_untrusted_context":false}`)),
		&selected)
	if selected.BuiltinSelectionID == "" || selected.ExternalSelectionID != "" ||
		selected.CapabilityGrant {
		t.Fatalf("unexpected built-in selection through installed route %#v", selected)
	}
}
