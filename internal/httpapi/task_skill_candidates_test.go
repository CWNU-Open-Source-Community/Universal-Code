package httpapi

import (
	"encoding/base64"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"cyberagent-workbench/internal/application"
	"cyberagent-workbench/internal/plugins"
	"cyberagent-workbench/internal/skills"
)

func taskCandidatesURL(workspaceID, profile string) string {
	return TaskSkillCandidatesPath + "?workspace_id=" + workspaceID + "&surface=code&phase=deliver&profile=" + profile
}

func TestTaskSkillCandidatesHTTPUsesGUIImportEnableAndSelection(t *testing.T) {
	f, controller, _, _ := newExtensionOnboardingFixture(t)
	objects, _ := skills.NewLocalPackageObjectStore(filepath.Dir(f.dbPath))
	builtins, _ := skills.BuiltinRegistry()
	registry := application.NewSkillPackageRegistryService(f.store, objects, builtins)
	var err error
	f.api, err = New(f.store, Config{AccessToken: testAccessToken, ControlToken: testControlToken,
		RunCreationEnabled: true, SessionMessageEnabled: true,
		ExtensionControlEnabled: true, ExtensionController: controller,
		SkillInstallationEnabled: true, SkillInstallationController: registry})
	if err != nil {
		t.Fatal(err)
	}
	archive := buildOpenAPISkillPackage(t)
	installBody, _ := json.Marshal(SkillPackageInstallRequestView{Version: skills.PackageInstallationProtocolVersion,
		ArchiveBase64: base64.StdEncoding.EncodeToString(archive), Surface: "code", ConfirmUntrusted: true})
	var installed PluginSkillInstallView
	decodeDataStatus(t, performControlPathRequest(t, f.api, SkillPackageInstallPath, "task-gui-import-0001", strings.NewReader(string(installBody))), http.StatusAccepted, &installed)
	var before application.TaskSkillCandidatesView
	url := taskCandidatesURL(f.workspace.ID, "review")
	decodeData(t, f.get(t, url), &before)
	if len(before.Items) != 0 {
		t.Fatalf("unreviewed import was selectable: %+v", before)
	}
	for _, action := range []plugins.ReviewAction{plugins.ReviewApprove, plugins.ReviewEnable} {
		var value ExtensionPluginInstallationView
		decodeDataStatus(t, extensionOnboardingRequest(t, f.api, "/api/v1/extensions/plugins/"+installed.Installation.ID+"/review", ExtensionPluginReviewRequestView{
			Version: ExtensionControlProtocol, Action: action, ExpectedPackageFingerprint: installed.Installation.PackageFingerprint,
			ExpectedGeneration: installed.Installation.Generation, Capabilities: []plugins.Capability{plugins.CapabilitySkills}, ConfirmUntrusted: true,
		}), http.StatusAccepted, &value)
		installed.Installation = value
	}
	var candidates application.TaskSkillCandidatesView
	response := f.get(t, url)
	decodeData(t, response, &candidates)
	if candidates.WorkspaceID != f.workspace.ID || candidates.CapabilityGrant || len(candidates.Items) != 1 ||
		candidates.Items[0].Ref != "openapi-external-review@1.0.0" || candidates.UnavailableCount != 1 {
		t.Fatalf("enabled new Plugin candidate=%+v", candidates)
	}
	for _, private := range []string{f.workspace.RootPath, "Inspect workspace evidence only.", "archive_base64", "object_key"} {
		if strings.Contains(response.Body.String(), private) {
			t.Fatalf("candidate response exposed %q", private)
		}
	}
	runID := newInstalledSkillSelectionRun(t, f, "task-gui-create-0001", "review")
	var selected application.ProjectSkillSelectionResult
	decodeData(t, performControlPathRequest(t, f.api, "/api/v1/runs/"+runID+"/external-skills/select", "task-gui-select-0001",
		strings.NewReader(`{"refs":["`+candidates.Items[0].Ref+`"],"confirm_untrusted_context":true}`)), &selected)
	selection, found, err := f.store.GetExternalSkillSelectionByRun(t.Context(), runID)
	if err != nil || !found || selection.Items[0].Plugin == nil || selection.Items[0].InstallationID != installed.Installation.ID {
		t.Fatalf("GUI import selected no real Plugin binding: %+v %v", selection, err)
	}
	// A compatibility installation with a valid retained object remains available
	// beside the new Plugin source; the fixture's missing old object stays withheld.
	historical := newInventorySkillFixture(t)
	seedInventorySkill(t, f.store, f.dbPath, historical, true, objects)
	decodeData(t, f.get(t, url), &candidates)
	if len(candidates.Items) != 2 || candidates.Items[0].Ref != historical.ref || candidates.Items[1].Ref != "openapi-external-review@1.0.0" {
		t.Fatalf("historical/new sources did not coexist: %+v", candidates)
	}
}

func TestTaskSkillCandidatesHTTPNativePinAndReadBoundary(t *testing.T) {
	f := newAPIFixture(t)
	objects, _ := skills.NewLocalPackageObjectStore(filepath.Dir(f.dbPath))
	builtins, _ := skills.BuiltinRegistry()
	registry := application.NewSkillPackageRegistryService(f.store, objects, builtins)
	directory := filepath.Join(t.TempDir(), "http-native-task")
	if err := os.MkdirAll(directory, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(directory, "SKILL.md"), []byte("---\nname: http-native-task\ndescription: Select original guidance.\n---\nUse this exact native task evidence.\n"), 0600); err != nil {
		t.Fatal(err)
	}
	result, err := application.NewSkillCatalogService(f.store, registry).ImportFromDirectory(t.Context(), application.ImportSkillFromDirectoryRequest{
		Directory: directory, Surface: "code", OperationKey: "http-native-task-import", InstalledBy: "operator", ConfirmUntrusted: true})
	if err != nil || result.Portable == nil {
		t.Fatalf("native import=%+v %v", result, err)
	}
	url := taskCandidatesURL(f.workspace.ID, "code")
	var candidates application.TaskSkillCandidatesView
	decodeData(t, f.get(t, url), &candidates)
	if len(candidates.Items) != 1 || candidates.Items[0].Name != "http-native-task" || candidates.Items[0].Version != "" || !strings.HasPrefix(candidates.Items[0].Ref, `{"installation_id":`) {
		t.Fatalf("native exact-ref projection=%+v", candidates)
	}
	runID := newInstalledSkillSelectionRun(t, f, "http-native-task-create", "code")
	body, _ := json.Marshal(InstalledSkillSelectionRequestView{Refs: []string{candidates.Items[0].Ref}, ConfirmUntrustedContext: true})
	var selected application.ProjectSkillSelectionResult
	decodeData(t, performControlPathRequest(t, f.api, "/api/v1/runs/"+runID+"/external-skills/select", "http-native-task-select", strings.NewReader(string(body))), &selected)
	selection, found, err := f.store.GetExternalSkillSelectionByRun(t.Context(), runID)
	if err != nil || !found || !selection.Items[0].Plugin.Native || selection.Items[0].InstallationID != result.Portable.ID {
		t.Fatalf("native selection did not preserve installation: %+v %v", selection, err)
	}
	assertAPIError(t, performRequest(t, f.api, http.MethodGet, url, testControlToken, "127.0.0.1:8765", "127.0.0.1:45000", nil), http.StatusUnauthorized, "POLICY_DENIED")
	assertAPIError(t, performRequest(t, f.api, http.MethodPost, url, testAccessToken, "127.0.0.1:8765", "127.0.0.1:45000", strings.NewReader(`{}`)), http.StatusMethodNotAllowed, "INVALID_ARGUMENT")
	assertAPIError(t, performRequest(t, f.api, http.MethodGet, url, testAccessToken, "127.0.0.1:8765", "127.0.0.1:45000", strings.NewReader(`{}`)), http.StatusBadRequest, "INVALID_ARGUMENT")
	for _, query := range []string{
		"?workspace_id=" + f.workspace.ID, strings.TrimPrefix(url, TaskSkillCandidatesPath) + "&workspace_id=another",
		strings.TrimPrefix(url, TaskSkillCandidatesPath) + "&extra=yes", "?workspace_id=" + f.workspace.ID + "&surface=code&phase=other&profile=code",
	} {
		assertAPIError(t, f.get(t, TaskSkillCandidatesPath+query), http.StatusBadRequest, "INVALID_ARGUMENT")
	}
	assertAPIError(t, f.get(t, taskCandidatesURL("missing-workspace", "code")), http.StatusNotFound, "NOT_FOUND")
}
