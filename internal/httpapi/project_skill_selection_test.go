package httpapi

import (
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"cyberagent-workbench/internal/application"
	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/skills"
)

func TestProjectSkillSelectionExactPinsReplayAndFailurePreserveCreatedRun(t *testing.T) {
	f := newAPIFixture(t)
	registry, err := skills.BuiltinRegistry()
	if err != nil {
		t.Fatal(err)
	}
	manifest, _ := registry.Get("code")
	ref := "code@" + manifest.Version
	filename := filepath.Join(f.workspace.RootPath, ".prayu", "config.yaml")
	if err := os.MkdirAll(filepath.Dir(filename), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filename, []byte("protocol: project_config.v1\nskill_suggestions: ["+ref+", absent-package@1.0.0]\n"), 0600); err != nil {
		t.Fatal(err)
	}
	var created ThreadCreationControlView
	decodeDataStatus(t, performControlPathRequest(t, f.api, ThreadCollectionPath, "project-skill-create-0001", strings.NewReader(`{"version":"thread_creation.v1","workspace_id":"`+f.workspace.ID+`","goal":"select project skills"}`)), http.StatusAccepted, &created)
	path := "/api/v1/runs/" + created.Run.ID + "/project-skills/select"
	// The live file is irrelevant after creation, including an unavailable new version.
	if err := os.WriteFile(filename, []byte("protocol: project_config.v1\nskill_suggestions: [code@99.0.0]\n"), 0600); err != nil {
		t.Fatal(err)
	}
	assertAPIError(t, performControlPathRequest(t, f.api, path, "project-skill-bad-ref-0001", strings.NewReader(`{"refs":["code@99.0.0"],"confirm_untrusted_context":true}`)), http.StatusBadRequest, "INVALID_ARGUMENT")
	assertAPIError(t, performControlPathRequest(t, f.api, path, "project-skill-missing-0001", strings.NewReader(`{"refs":["absent-package@1.0.0"],"confirm_untrusted_context":false}`)), http.StatusPreconditionFailed, "FAILED_PRECONDITION")
	assertAPIError(t, performControlPathRequest(t, f.api, path, "project-skill-missing-0002", strings.NewReader(`{"refs":["absent-package@1.0.0"],"confirm_untrusted_context":true}`)), http.StatusNotFound, "NOT_FOUND")
	var selected application.ProjectSkillSelectionResult
	body := `{"refs":["` + ref + `"],"confirm_untrusted_context":false}`
	decodeData(t, performControlPathRequest(t, f.api, path, "project-skill-select-0001", strings.NewReader(body)), &selected)
	if selected.CapabilityGrant || selected.BuiltinSelectionID == "" || selected.ExternalSelectionID != "" || selected.Replayed {
		t.Fatalf("unexpected selection %#v", selected)
	}
	var replay application.ProjectSkillSelectionResult
	decodeData(t, performControlPathRequest(t, f.api, path, "project-skill-select-0001", strings.NewReader(body)), &replay)
	if !replay.Replayed || replay.BuiltinSelectionID != selected.BuiltinSelectionID {
		t.Fatalf("selection replay drift %#v", replay)
	}
	run, err := f.store.GetRun(t.Context(), created.Run.ID)
	if err != nil || run.Status != domain.RunCreated {
		t.Fatalf("selection started/lost Run: %s %v", run.Status, err)
	}
}
