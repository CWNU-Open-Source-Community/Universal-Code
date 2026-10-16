package httpapi

import (
	"net/http"
	"testing"

	"cyberagent-workbench/internal/application"
	"cyberagent-workbench/internal/skills"
)

func TestExtensionOnboardingCapabilitiesFollowActualControllerAndGate(t *testing.T) {
	f, _, _, _ := newExtensionOnboardingFixture(t)
	assertCapabilities := func(wantMCP, wantPlugin, wantLSP bool) {
		t.Helper()
		var view ExtensionInventoryView
		decodeDataStatus(t, f.get(t, ExtensionInventoryPath), http.StatusOK, &view)
		if view.Onboarding == nil || view.Onboarding.MCPRegistration != wantMCP ||
			view.Onboarding.PluginImport != wantPlugin || view.Onboarding.LSPConfiguration != wantLSP {
			t.Fatalf("onboarding availability does not match this backend: %+v", view.Onboarding)
		}
	}
	assertCapabilities(true, true, false)
	f.api.codeIntelController = newCodeIntelOpenAPITestController(t, f)
	assertCapabilities(true, true, true)
	f.api.extensionControlEnabled = false
	assertCapabilities(false, false, false)
	f.api.extensionControlEnabled = true
	f.api.extensionController = &extensionControllerStub{}
	assertCapabilities(false, false, true)
	response := f.get(t, ExtensionInventoryPath+"?workspace_id="+f.workspace.ID)
	assertAPIError(t, response, http.StatusServiceUnavailable, "UNAVAILABLE")
}

type uploadOnlyExtensionOnboarding struct {
	ExtensionController
	ExtensionOnboardingController
}

func TestExtensionOnboardingSourceAndDirectoryCapabilitiesFollowAvailableControllers(t *testing.T) {
	f, controller, _, _ := newExtensionOnboardingFixture(t)
	capabilities := f.api.extensionOnboardingCapabilities()
	if !capabilities.PluginSourceImport || capabilities.NativeSkillDirectoryImport {
		t.Fatalf("initial acquisition capabilities: %+v", capabilities)
	}
	// An old adapter still supports registration and ZIP upload, without the
	// separately published source-acquisition method.
	f.api.extensionController = uploadOnlyExtensionOnboarding{controller, controller}
	capabilities = f.api.extensionOnboardingCapabilities()
	if !capabilities.MCPRegistration || !capabilities.PluginImport || capabilities.PluginSourceImport {
		t.Fatalf("old upload adapter capability drift: %+v", capabilities)
	}
	request := extensionPluginImportRequest(extensionPluginArchive(t))
	request.ArchiveBase64 = ""
	request.Source = &ExtensionPluginImportSourceView{Kind: "https", URL: "https://example.invalid/plugin.zip"}
	assertAPIError(t, extensionOnboardingRequest(t, f.api, ExtensionPluginImportPath, request), http.StatusNotFound, "NOT_FOUND")
	request.Source = nil
	request = extensionPluginImportRequest(extensionPluginArchive(t))
	var uploaded ExtensionPluginImportView
	decodeDataStatus(t, extensionOnboardingRequest(t, f.api, ExtensionPluginImportPath, request), http.StatusAccepted, &uploaded)
	if uploaded.Acquisition != nil || uploaded.Installation.State != "staged" {
		t.Fatal("old adapter no longer accepts ZIP upload", uploaded)
	}
	f.api.skillInstallationEnabled = true
	if f.api.extensionOnboardingCapabilities().NativeSkillDirectoryImport {
		t.Fatal("directory import advertised without an installation controller")
	}
	objects, err := skills.NewLocalPackageObjectStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	builtins, err := skills.BuiltinRegistry()
	if err != nil {
		t.Fatal(err)
	}
	f.api.skillInstallationController = application.NewSkillPackageRegistryService(f.store, objects, builtins)
	if !f.api.extensionOnboardingCapabilities().NativeSkillDirectoryImport {
		t.Fatal("available directory import was not advertised")
	}
	f.api.skillInstallationEnabled = false
	f.api.extensionControlEnabled = false
	capabilities = f.api.extensionOnboardingCapabilities()
	if capabilities.PluginSourceImport || capabilities.NativeSkillDirectoryImport {
		t.Fatalf("disabled acquisition controls were advertised: %+v", capabilities)
	}
}

func TestExtensionOnboardingInventoryRejectsAmbiguousQueries(t *testing.T) {
	f, _, _, _ := newExtensionOnboardingFixture(t)
	for _, query := range []string{
		"?workspace_id=a&workspace_id=b", "?run_id=a&run_id=b", "?unknown=value",
		"?workspace_id=", "?run_id=",
	} {
		assertAPIError(t, f.get(t, ExtensionInventoryPath+query), http.StatusBadRequest, "INVALID_ARGUMENT")
	}
}
