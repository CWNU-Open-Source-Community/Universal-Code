package httpapi

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"cyberagent-workbench/internal/apperror"
	"cyberagent-workbench/internal/application"
	"cyberagent-workbench/internal/hooks"
	"cyberagent-workbench/internal/mcp"
	"cyberagent-workbench/internal/plugins"
	"cyberagent-workbench/internal/store"
)

func newExtensionOnboardingFixture(t *testing.T) (*apiFixture, *application.ExtensionControlService, *mcp.Manager, *plugins.Service) {
	t.Helper()
	f := newAPIFixture(t)
	manager, err := mcp.NewClientManager(f.store, mcpApprovalNoCredentials{}, mcp.ManagerOptions{})
	if err != nil {
		t.Fatal(err)
	}
	pluginService, err := plugins.NewService(f.store)
	if err != nil {
		t.Fatal(err)
	}
	controller, err := application.NewExtensionControlService(f.store, manager, pluginService)
	if err != nil {
		t.Fatal(err)
	}
	f.api, err = New(f.store, Config{AccessToken: testAccessToken, ControlToken: testControlToken,
		ExtensionControlEnabled: true, ExtensionController: controller})
	if err != nil {
		t.Fatal(err)
	}
	return f, controller, manager, pluginService
}

func extensionRegistrationRequest(f *apiFixture) ExtensionMCPRegistrationRequestView {
	return ExtensionMCPRegistrationRequestView{Version: ExtensionControlProtocol,
		Descriptor: ExtensionMCPRegistrationDescriptorView{ProtocolVersion: mcp.ClientProtocolVersion,
			ID: "onboarding-server", Name: "Onboarding fixture", Transport: mcp.TransportStreamableHTTP,
			Target: "https://onboarding.invalid/mcp", DeclaredCapabilities: []mcp.CapabilityKind{mcp.CapabilityTools},
			Scope: mcp.ScopeRun, RunID: f.run.ID, WorkspaceID: f.workspace.ID,
			CallTimeoutMillis: 3000, MaxResultBytes: 8192}}
}

func extensionOnboardingRequest(t *testing.T, api *API, path string, value any) *httptest.ResponseRecorder {
	t.Helper()
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return performControlPathRequest(t, api, path, "", strings.NewReader(string(raw)))
}

func extensionPluginArchive(t *testing.T) []byte {
	t.Helper()
	manifest := extensionTestPlugin("fixture").Manifest
	raw, err := plugins.BuildUnsignedPackage(manifest, map[string][]byte{})
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func extensionPluginImportRequest(archive []byte) ExtensionPluginImportRequestView {
	digest := sha256.Sum256(archive)
	return ExtensionPluginImportRequestView{Version: ExtensionControlProtocol,
		ArchiveBase64: base64.StdEncoding.EncodeToString(archive), ArchiveSHA256: hex.EncodeToString(digest[:])}
}

func TestExtensionOnboardingRegistrationIsInertScopedAndReplayable(t *testing.T) {
	f, controller, manager, _ := newExtensionOnboardingFixture(t)
	request := extensionRegistrationRequest(f)
	var first ExtensionMCPRegistrationView
	firstResponse := extensionOnboardingRequest(t, f.api, ExtensionMCPRegistrationPath, request)
	decodeDataStatus(t, firstResponse, http.StatusAccepted, &first)
	writeExtensionOnboardingEvidence(t, "extension-mcp-registration.json", firstResponse)
	if first.ProtocolVersion != ExtensionOnboardingProtocol || first.Replayed || first.NextStep != "approve_discovery" ||
		first.Server.State != "staged" || first.Server.Health != "unknown" || len(first.Server.Capabilities.Tools) != 0 ||
		first.Server.Source.Kind != "manual" || first.Server.Source.URI != "http-upload" {
		t.Fatalf("registration implied reviewed or executed state: %+v", first)
	}
	var replay ExtensionMCPRegistrationView
	decodeDataStatus(t, extensionOnboardingRequest(t, f.api, ExtensionMCPRegistrationPath, request), http.StatusAccepted, &replay)
	if !replay.Replayed || replay.Server.ID != first.Server.ID || replay.Server.Generation != first.Server.Generation {
		t.Fatal("lost-response registration replay changed persisted identity", replay)
	}
	if _, err := manager.Refresh(t.Context(), first.Server.ID); err == nil {
		t.Fatal("unreviewed registration discovered remote capabilities")
	}
	capabilities, err := manager.Capabilities(t.Context(), f.run.ID, f.workspace.ID)
	if err != nil || len(capabilities.Servers) != 0 {
		t.Fatal("unreviewed registration became callable", capabilities, err)
	}
	inventory, err := controller.InventoryForScope(t.Context(), f.run.ID, f.workspace.ID)
	if err != nil || len(inventory.MCPServers) != 1 || len(inventory.MCPCalls) != 0 {
		t.Fatal("registration inventory lost its scope or invented a call", inventory, err)
	}
	workspaceInventory, err := controller.InventoryForScope(t.Context(), "", f.workspace.ID)
	if err != nil || workspaceInventory.WorkspaceID != f.workspace.ID || len(workspaceInventory.MCPServers) != 0 {
		t.Fatal("workspace-only inventory exposed a Run-scoped registration", workspaceInventory, err)
	}
	request.Descriptor.Target = "https://different.invalid/mcp"
	if response := extensionOnboardingRequest(t, f.api, ExtensionMCPRegistrationPath, request); response.Code != http.StatusConflict {
		t.Fatal("registration identity was rebound", response.Code, response.Body.String())
	}
	request.Descriptor.ID = "workspace-onboarding-server"
	request.Descriptor.Scope, request.Descriptor.RunID = mcp.ScopeWorkspace, ""
	decodeDataStatus(t, extensionOnboardingRequest(t, f.api, ExtensionMCPRegistrationPath, request), http.StatusAccepted, &replay)
	workspaceInventory, err = controller.InventoryForScope(t.Context(), "", f.workspace.ID)
	if err != nil || len(workspaceInventory.MCPServers) != 1 || len(workspaceInventory.MCPCalls) != 0 || workspaceInventory.RunID != "" {
		t.Fatal("first Workspace registration could not be read without a Run", workspaceInventory, err)
	}
	var httpInventory ExtensionInventoryView
	workspaceResponse := f.get(t, ExtensionInventoryPath+"?workspace_id="+f.workspace.ID)
	decodeData(t, workspaceResponse, &httpInventory)
	writeExtensionOnboardingEvidence(t, "extension-workspace-inventory.json", workspaceResponse)
	if httpInventory.WorkspaceID != f.workspace.ID || httpInventory.RunID != "" || len(httpInventory.MCPServers) != 1 || len(httpInventory.MCPCalls) != 0 {
		t.Fatal("Workspace HTTP inventory required an unrelated Run", httpInventory)
	}
	if _, err = controller.InventoryForScope(t.Context(), f.run.ID, "different-workspace"); err == nil {
		t.Fatal("inventory accepted mismatched Run and Workspace")
	}
	if _, err = controller.InventoryForScope(t.Context(), "", "missing-workspace"); err == nil {
		t.Fatal("inventory accepted a nonexistent Workspace")
	}
}

func TestExtensionOnboardingPluginImportRequiresSeparateReview(t *testing.T) {
	f, controller, _, pluginService := newExtensionOnboardingFixture(t)
	request := extensionPluginImportRequest(extensionPluginArchive(t))
	var first ExtensionPluginImportView
	response := extensionOnboardingRequest(t, f.api, ExtensionPluginImportPath, request)
	decodeDataStatus(t, response, http.StatusAccepted, &first)
	writeExtensionOnboardingEvidence(t, "extension-plugin-import.json", response)
	if first.Replayed || first.NextStep != "approve" || first.Installation.State != "staged" ||
		len(first.Installation.EnabledCapabilities) != 0 || first.Installation.Source.Kind != "upload" ||
		first.Installation.Source.URI != "sha256:"+request.ArchiveSHA256 {
		t.Fatal("ZIP import implied enabled capabilities", first)
	}
	for _, forbidden := range []string{"archive_base64", "publisher_public_key", "operation_key", "arguments\"", "hook-canary"} {
		if strings.Contains(response.Body.String(), forbidden) {
			t.Fatal("import response exposed private package content", forbidden)
		}
	}
	var replay ExtensionPluginImportView
	decodeDataStatus(t, extensionOnboardingRequest(t, f.api, ExtensionPluginImportPath, request), http.StatusAccepted, &replay)
	if !replay.Replayed || replay.Installation.ID != first.Installation.ID || replay.Installation.Generation != first.Installation.Generation {
		t.Fatal("same ZIP lost-response replay installed twice", replay)
	}
	hooks, err := pluginService.ActiveHooks(t.Context())
	if err != nil || len(hooks) != 0 {
		t.Fatal("unreviewed import activated Hooks", hooks, err)
	}
	engine := hooksEngineForOnboarding(f.store, pluginService)
	input := hookInputForOnboarding(f)
	result, err := engine.Execute(t.Context(), input)
	if err != nil || len(result.Executed) != 0 {
		t.Fatal("staged upload executed a restricted Hook", result, err)
	}
	review := ExtensionPluginReviewRequestView{Version: ExtensionControlProtocol, Action: plugins.ReviewEnable,
		ExpectedPackageFingerprint: first.Installation.PackageFingerprint, ExpectedGeneration: first.Installation.Generation,
		Capabilities: []plugins.Capability{plugins.CapabilityHooks}, ConfirmUntrusted: true}
	path := "/api/v1/extensions/plugins/" + first.Installation.ID + "/review"
	if response := extensionOnboardingRequest(t, f.api, path, review); response.Code != http.StatusPreconditionFailed {
		t.Fatal("staged import skipped approval", response.Code, response.Body.String())
	}
	review.Action, review.ConfirmUntrusted = plugins.ReviewApprove, false
	if response := extensionOnboardingRequest(t, f.api, path, review); response.Code != http.StatusForbidden {
		t.Fatal("unsigned import skipped untrusted confirmation", response.Code, response.Body.String())
	}
	review.ConfirmUntrusted = true
	var reviewed ExtensionPluginInstallationView
	decodeDataStatus(t, extensionOnboardingRequest(t, f.api, path, review), http.StatusAccepted, &reviewed)
	review.Action, review.ExpectedGeneration = plugins.ReviewEnable, reviewed.Generation
	decodeDataStatus(t, extensionOnboardingRequest(t, f.api, path, review), http.StatusAccepted, &reviewed)
	if reviewed.State != "enabled" || len(reviewed.EnabledCapabilities) != 1 || reviewed.EnabledCapabilities[0] != "hooks" {
		t.Fatal("explicit capability review was not persisted", reviewed)
	}
	result, err = engine.Execute(t.Context(), input)
	if err != nil || result.Denied || len(result.Executed) != 1 || result.Executed[0] != reviewed.Manifest.ID+"/record-test" {
		t.Fatal("reviewed uploaded contribution did not reach the existing restricted Hook handler", result, err)
	}
	audits, err := sql.Open("sqlite3", f.dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer audits.Close()
	var receipts int
	err = audits.QueryRowContext(t.Context(), `SELECT COUNT(*) FROM plugin_hook_audits
		WHERE plugin_id = ? AND hook_id = 'record-test' AND event = 'pre_tool'
		AND run_id = ? AND workspace_id = ? AND outcome = 'completed'`,
		reviewed.Manifest.ID, f.run.ID, f.workspace.ID).Scan(&receipts)
	if err != nil || receipts != 1 {
		t.Fatal("actual restricted Hook execution did not retain its receipt", receipts, err)
	}
	inventory, err := controller.Inventory(t.Context(), f.run.ID)
	if err != nil || len(inventory.Plugins) != 1 || len(inventory.MCPCalls) != 0 {
		t.Fatal("installation inventory invented invocation evidence", inventory, err)
	}
	writeExtensionOnboardingEvidence(t, "extension-inventory.json", f.get(t, ExtensionInventoryPath+"?run_id="+f.run.ID))
	decodeDataStatus(t, extensionOnboardingRequest(t, f.api, ExtensionPluginImportPath, request), http.StatusAccepted, &replay)
	if !replay.Replayed || replay.Installation.State != "enabled" || replay.NextStep != "select_contributions" || replay.Installation.Generation != reviewed.Generation {
		t.Fatal("replay reset the installed Plugin review", replay)
	}
}

func TestExtensionOnboardingControlBoundaries(t *testing.T) {
	f, controller, _, _ := newExtensionOnboardingFixture(t)
	for _, path := range []string{ExtensionMCPRegistrationPath, ExtensionPluginImportPath} {
		t.Run(path, func(t *testing.T) {
			for _, tc := range []struct {
				name   string
				config Config
				token  string
				method string
				status int
			}{
				{"disabled", Config{ExtensionController: controller}, testControlToken, http.MethodPost, http.StatusNotFound},
				{"access-is-not-control", Config{ExtensionControlEnabled: true, ExtensionController: controller}, testAccessToken, http.MethodPost, http.StatusUnauthorized},
				{"review-only", Config{ExtensionControlEnabled: true, ExtensionController: &extensionControllerStub{}}, testControlToken, http.MethodPost, http.StatusNotFound},
				{"method", Config{ExtensionControlEnabled: true, ExtensionController: controller}, testControlToken, http.MethodGet, http.StatusMethodNotAllowed},
			} {
				t.Run(tc.name, func(t *testing.T) {
					tc.config.AccessToken, tc.config.ControlToken = testAccessToken, testControlToken
					api, err := New(f.store, tc.config)
					if tc.name == "review-only" {
						if apperror.CodeOf(err) != apperror.CodeInvalidArgument {
							t.Fatalf("enabled partial controller accepted: %v", err)
						}
						return
					}
					if err != nil {
						t.Fatal(err)
					}
					response := performRequest(t, api, tc.method, path, tc.token, "127.0.0.1:8765", "127.0.0.1:45000", strings.NewReader(`{}`))
					if response.Code != tc.status {
						t.Fatal("control boundary changed", response.Code, response.Body.String())
					}
				})
			}
		})
	}
	f.api.extensionController = nil
	if response := extensionOnboardingRequest(t, f.api, ExtensionMCPRegistrationPath, extensionRegistrationRequest(f)); response.Code != http.StatusNotFound {
		t.Fatal("absent onboarding controller was not closed", response.Code, response.Body.String())
	}
}

func TestExtensionOnboardingRejectsInvalidAndOversizedInputs(t *testing.T) {
	f, _, _, _ := newExtensionOnboardingFixture(t)
	registration := extensionRegistrationRequest(f)
	for _, mutate := range []func(*ExtensionMCPRegistrationRequestView){
		func(v *ExtensionMCPRegistrationRequestView) { v.Version = "unknown" },
		func(v *ExtensionMCPRegistrationRequestView) {
			v.Descriptor.ProtocolVersion = mcp.NativeClientProtocolVersion
		},
		func(v *ExtensionMCPRegistrationRequestView) { v.Descriptor.Target = "http://insecure.invalid" },
		func(v *ExtensionMCPRegistrationRequestView) {
			v.Descriptor.Arguments = []string{"--api-key=secret-canary"}
		},
		func(v *ExtensionMCPRegistrationRequestView) { v.Descriptor.WorkspaceID = "missing-workspace" },
	} {
		candidate := registration
		mutate(&candidate)
		if response := extensionOnboardingRequest(t, f.api, ExtensionMCPRegistrationPath, candidate); response.Code < 400 {
			t.Fatal("invalid MCP registration was accepted", response.Body.String())
		}
	}
	for _, raw := range []string{
		`{"version":"extension-control.v1","version":"extension-control.v1","descriptor":{}}`,
		`{"version":"extension-control.v1","descriptor":{"source":{"kind":"plugin"}}}`,
		`{"version":"extension-control.v1","descriptor":{"env":{"TOKEN":"secret"}}}`,
	} {
		if response := performControlPathRequest(t, f.api, ExtensionMCPRegistrationPath, "", strings.NewReader(raw)); response.Code != http.StatusBadRequest {
			t.Fatal("untrusted descriptor fields were accepted", response.Code, response.Body.String())
		}
	}
	request := extensionPluginImportRequest(extensionPluginArchive(t))
	for _, candidate := range []ExtensionPluginImportRequestView{
		{Version: "unknown", ArchiveBase64: request.ArchiveBase64, ArchiveSHA256: request.ArchiveSHA256},
		{Version: ExtensionControlProtocol, ArchiveBase64: "!!!", ArchiveSHA256: request.ArchiveSHA256},
		{Version: ExtensionControlProtocol, ArchiveBase64: request.ArchiveBase64, ArchiveSHA256: strings.Repeat("0", 64)},
		extensionPluginImportRequest([]byte("not-a-zip")),
	} {
		if response := extensionOnboardingRequest(t, f.api, ExtensionPluginImportPath, candidate); response.Code != http.StatusBadRequest {
			t.Fatal("invalid Plugin archive was accepted", response.Code, response.Body.String())
		}
	}
	oversized := extensionPluginImportRequest(make([]byte, plugins.MaxArchiveBytes+1))
	if response := extensionOnboardingRequest(t, f.api, ExtensionPluginImportPath, oversized); response.Code != http.StatusRequestEntityTooLarge {
		t.Fatal("oversized decoded ZIP was accepted", response.Code, response.Body.String())
	}
	for _, path := range []string{ExtensionMCPRegistrationPath, ExtensionPluginImportPath} {
		req := httptest.NewRequest(http.MethodPost, "http://127.0.0.1:8765"+path, strings.NewReader(`{}`))
		req.RemoteAddr = "127.0.0.1:45000"
		req.Header.Set("Authorization", "Bearer "+testControlToken)
		req.Header.Set("Content-Type", "application/json")
		req.ContentLength = maxExtensionPluginImportBodyBytes + 1
		response := httptest.NewRecorder()
		f.api.ServeHTTP(response, req)
		if response.Code != http.StatusRequestEntityTooLarge {
			t.Fatal("oversized body was read", response.Code, response.Body.String())
		}
		if response := performControlPathRequest(t, f.api, path+"?path=C%3A%5Cprivate.zip", "", strings.NewReader(`{}`)); response.Code != http.StatusBadRequest {
			t.Fatal("onboarding accepted a path query", response.Code, response.Body.String())
		}
	}
	installations, err := f.store.ListPluginInstallations(t.Context(), "", 100)
	if err != nil || len(installations) != 0 {
		t.Fatal("rejected input left an installation", installations, err)
	}
}

func TestExtensionOnboardingUploadReplayOmitsPriorCLIHostPath(t *testing.T) {
	f, _, _, pluginService := newExtensionOnboardingFixture(t)
	archive := extensionPluginArchive(t)
	request := extensionPluginImportRequest(archive)
	prior, _, err := pluginService.Stage(t.Context(), archive, plugins.InstallSource{
		Kind: "local_file", URI: filepath.Join(t.TempDir(), "private-upload-canary.zip"), SHA256: request.ArchiveSHA256}, "", "cli_operator")
	if err != nil {
		t.Fatal(err)
	}
	var replay ExtensionPluginImportView
	response := extensionOnboardingRequest(t, f.api, ExtensionPluginImportPath, request)
	decodeDataStatus(t, response, http.StatusAccepted, &replay)
	if !replay.Replayed || replay.Installation.ID != prior.ID || replay.Installation.Source.Kind != "local_file" ||
		replay.Installation.Source.URI != "sha256:"+request.ArchiveSHA256 || strings.Contains(response.Body.String(), "private-upload-canary") {
		t.Fatal("upload replay leaked the CLI host path or rewrote provenance", response.Body.String())
	}
}

func hooksEngineForOnboarding(st *store.SQLiteStore, service *plugins.Service) *hooks.Engine {
	return hooks.NewEngine(st).WithLoader(service.ActiveHooks)
}

func hookInputForOnboarding(f *apiFixture) hooks.Input {
	return hooks.Input{Event: hooks.PreTool, RunID: f.run.ID, WorkspaceID: f.workspace.ID,
		ToolName: "list_workspace", Payload: json.RawMessage(`{}`)}
}

func writeExtensionOnboardingEvidence(t *testing.T, name string, response *httptest.ResponseRecorder) {
	t.Helper()
	if directory := os.Getenv("UC_EXTENSION_ONBOARDING_EVIDENCE"); directory != "" {
		if err := os.MkdirAll(directory, 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(directory, name), response.Body.Bytes(), 0600); err != nil {
			t.Fatal(err)
		}
	}
}

func TestExtensionOnboardingLostReplyRecoversAfterSQLiteReopen(t *testing.T) {
	f, _, _, _ := newExtensionOnboardingFixture(t)
	registration := extensionRegistrationRequest(f)
	plugin := extensionPluginImportRequest(extensionPluginArchive(t))
	var server ExtensionMCPRegistrationView
	var installation ExtensionPluginImportView
	decodeDataStatus(t, extensionOnboardingRequest(t, f.api, ExtensionMCPRegistrationPath, registration), http.StatusAccepted, &server)
	decodeDataStatus(t, extensionOnboardingRequest(t, f.api, ExtensionPluginImportPath, plugin), http.StatusAccepted, &installation)
	// Reopen the durable store as a new process would after either reply is lost.
	path := f.dbPath
	if err := f.store.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	manager, _ := mcp.NewClientManager(reopened, mcpApprovalNoCredentials{}, mcp.ManagerOptions{})
	service, _ := plugins.NewService(reopened)
	controller, _ := application.NewExtensionControlService(reopened, manager, service)
	api, err := New(reopened, Config{AccessToken: testAccessToken, ControlToken: testControlToken,
		ExtensionControlEnabled: true, ExtensionController: controller})
	if err != nil {
		t.Fatal(err)
	}
	var serverReplay ExtensionMCPRegistrationView
	var pluginReplay ExtensionPluginImportView
	decodeDataStatus(t, extensionOnboardingRequest(t, api, ExtensionMCPRegistrationPath, registration), http.StatusAccepted, &serverReplay)
	decodeDataStatus(t, extensionOnboardingRequest(t, api, ExtensionPluginImportPath, plugin), http.StatusAccepted, &pluginReplay)
	if !serverReplay.Replayed || !pluginReplay.Replayed || serverReplay.Server.ID != server.Server.ID ||
		pluginReplay.Installation.ID != installation.Installation.ID || serverReplay.Server.State != "staged" || pluginReplay.Installation.State != "staged" {
		t.Fatal("restart retried import or invented review", serverReplay, pluginReplay)
	}
}

func TestExtensionOnboardingCancelledRequestDoesNotStage(t *testing.T) {
	f, controller, _, _ := newExtensionOnboardingFixture(t)
	for _, candidate := range []struct {
		path string
		body any
	}{
		{ExtensionMCPRegistrationPath, extensionRegistrationRequest(f)},
		{ExtensionPluginImportPath, extensionPluginImportRequest(extensionPluginArchive(t))},
	} {
		raw, err := json.Marshal(candidate.body)
		if err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithCancel(t.Context())
		cancel()
		request := httptest.NewRequest(http.MethodPost, "http://127.0.0.1:8765"+candidate.path,
			strings.NewReader(string(raw))).WithContext(ctx)
		request.RemoteAddr = "127.0.0.1:45000"
		request.Header.Set("Authorization", "Bearer "+testControlToken)
		request.Header.Set("Content-Type", "application/json")
		response := httptest.NewRecorder()
		f.api.ServeHTTP(response, request)
		if response.Code < 400 {
			t.Fatal("cancelled request staged an extension", response.Body.String())
		}
	}
	inventory, err := controller.Inventory(t.Context(), f.run.ID)
	if err != nil || len(inventory.MCPServers) != 0 || len(inventory.Plugins) != 0 || len(inventory.MCPCalls) != 0 {
		t.Fatal("cancelled onboarding left durable extension state", inventory, err)
	}
}
