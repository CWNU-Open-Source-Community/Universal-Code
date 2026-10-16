package httpapi

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"cyberagent-workbench/internal/application"
	"cyberagent-workbench/internal/commandruntimeadapter"
	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/store"
)

func TestRunCapabilityReadinessHTTPProjectsSameStableGoFacts(t *testing.T) {
	fixture := newAPIFixture(t)
	path := "/api/v1/runs/" + fixture.run.ID + "/capability-readiness"
	response := performSessionMessageRequest(t, fixture.api, http.MethodGet,
		path, testAccessToken, "", "", nil)
	var view RunCapabilityReadinessView
	decodeDataStatus(t, response, http.StatusOK, &view)
	if view.ProtocolVersion != application.RunCapabilityReadinessProtocolVersion ||
		view.RunID != fixture.run.ID || view.CapabilityGrant || len(view.Permissions) != 3 ||
		len(view.Profiles) != 4 || len(view.Interactions) != 4 ||
		len(view.BrowserCDPPermissions) != 2 || len(view.Presets) != 1 ||
		!view.CommandRuntime.ProtocolAvailable ||
		view.CommandRuntime.AdapterInstalled || view.CommandRuntime.AdapterReady ||
		view.CommandRuntime.CurrentRunGranted || view.CommandRuntime.AdapterKind != "" ||
		view.CommandRuntime.Backend != "" {
		t.Fatalf("unexpected HTTP readiness projection: %#v", view)
	}
	preview := readinessHTTPOption(t, view.Profiles, "preview")
	if !preview.Selected || preview.Selectable || !preview.RuntimeAvailable ||
		!containsString(preview.BlockedBy, string(application.CapabilityBlockerRunNotQuiescent)) ||
		!containsString(preview.BlockedBy, string(application.CapabilityBlockerExecutionLeaseActive)) {
		t.Fatalf("HTTP readiness conflated selected and unavailable: %#v", preview)
	}
	local := readinessHTTPOption(t, view.Profiles, "local")
	if local.RuntimeAvailable ||
		!containsString(local.BlockedBy, string(application.CapabilityBlockerSandboxUnproven)) {
		t.Fatalf("HTTP readiness claimed an unproven Local runtime: %#v", local)
	}
	raw := strings.ToLower(response.Body.String())
	for _, forbidden := range []string{"root_path", "docker_socket", "endpoint_fingerprint",
		"profile_path", "credential", "lease_id", "owner_id", "operation_key"} {
		if strings.Contains(raw, forbidden) {
			t.Fatalf("readiness projection exposed private field %q: %s", forbidden, raw)
		}
	}
	assertAPIError(t, performSessionMessageRequest(t, fixture.api, http.MethodGet,
		path+"?debug=true", testAccessToken, "", "", nil),
		http.StatusBadRequest, "INVALID_ARGUMENT")
}

func TestRunCapabilityReadinessHTTPCurrentFixtureMatchesGoProjection(t *testing.T) {
	state, err := store.Open(filepath.Join(t.TempDir(), "readiness-contract.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = state.Close() })
	runs := application.NewRunService(state)
	_, run, err := runs.Create(t.Context(), application.CreateRunRequest{
		Goal: "read paused Plan readiness", Profile: "code", Surface: "code",
		Phase: "plan", Budget: domain.Budget{MaxTurns: 2},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := application.NewRunExecutionProfileService(state).Change(t.Context(),
		application.ChangeRunExecutionProfileRequest{RunID: run.ID, Profile: "local",
			OperationKey: "readiness-contract-local-profile", RequestedBy: "test_operator",
			Reason: "project selected installed adapter without execution"}); err != nil {
		t.Fatal(err)
	}
	if _, err := runs.Start(t.Context(), run.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := runs.Pause(t.Context(), run.ID); err != nil {
		t.Fatal(err)
	}
	capabilities := domain.ExecutionPermissionRuntimeCapabilities{
		OperatorApprovalEnabled: true, WorkspaceSandboxEnabled: true,
	}
	adapters := []commandruntimeadapter.Identity{commandruntimeadapter.SandboxedWorkspace(
		application.CommandRuntimeLocalSandboxBackend, "readiness-fixture-local", strings.Repeat("a", 64))}
	runtime := application.CapabilityReadinessRuntime{
		RunControlEnabled: true, ExecutionPermissionControlEnabled: true,
		ExecutionPermissionCapabilities: capabilities,
		LocalSandboxInstalled:           true, LocalSandboxProven: true, LocalBackendReady: true,
		CommandRuntimeAdapters: adapters,
	}
	api, err := New(state, Config{
		AccessToken: testAccessToken, ControlToken: testControlToken,
		RunControlEnabled: true, ExecutionPermissionControlEnabled: true,
		ExecutionPermissionCapabilities: capabilities, CommandRuntimeAdapters: adapters,
		CapabilityReadinessRuntime: &runtime,
	})
	if err != nil {
		t.Fatal(err)
	}
	response := performSessionMessageRequest(t, api, http.MethodGet,
		"/api/v1/runs/"+run.ID+"/capability-readiness", testAccessToken, "", "", nil)
	var view RunCapabilityReadinessView
	decodeDataStatus(t, response, http.StatusOK, &view)
	if len(view.Permissions) != 3 || view.Permissions[0].Value != "ask" ||
		view.Permissions[1].Value != "auto" || view.Permissions[2].Value != "full" ||
		!view.Permissions[0].Selected || view.CapabilityGrant ||
		!view.CommandRuntime.AdapterInstalled || !view.CommandRuntime.AdapterReady ||
		view.CommandRuntime.CurrentRunGranted || view.CommandRuntime.AdapterKind != "sandboxed_workspace" ||
		view.CommandRuntime.Backend != application.CommandRuntimeLocalSandboxBackend {
		t.Fatalf("current readiness contract changed: %#v", view)
	}

	// Capture the real HTTP envelope, normalizing only generated request/Run IDs.
	// Compare every public field in ordinary Go runs so a manually maintained TS
	// specimen cannot hide a backend contract change. The optional export follows
	// the existing TRAVERSE_TEST_*_OUTPUT cross-language acceptance convention.
	var captured map[string]any
	if err := json.Unmarshal(response.Body.Bytes(), &captured); err != nil {
		t.Fatal(err)
	}
	captured["request_id"] = "req-readiness-current"
	captured["data"].(map[string]any)["run_id"] = "run-readiness-current"
	data, err := json.MarshalIndent(captured, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if output := os.Getenv("TRAVERSE_TEST_READINESS_OUTPUT"); output != "" {
		if err := os.WriteFile(output, append(data, '\n'), 0600); err != nil {
			t.Fatal(err)
		}
	}
	fixturePath := filepath.Join("..", "..", "web", "src", "test", "fixtures", "readiness-paused-plan-current.json")
	fixture, err := os.ReadFile(fixturePath)
	if err != nil {
		t.Fatal(err)
	}
	var expected map[string]any
	if err := json.Unmarshal(fixture, &expected); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(captured, expected) {
		t.Fatalf("current readiness fixture drifted from the Go HTTP response; regenerate with TRAVERSE_TEST_READINESS_OUTPUT=%s\n%s", fixturePath, data)
	}
}

func readinessHTTPOption(t *testing.T, options []CapabilityReadinessOptionView,
	value string,
) CapabilityReadinessOptionView {
	t.Helper()
	for _, option := range options {
		if option.Value == value {
			return option
		}
	}
	t.Fatalf("HTTP readiness option %q is missing", value)
	return CapabilityReadinessOptionView{}
}

func containsString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

func TestVerificationStartupReadinessProjectsNativePreferencesWithoutHTTPWrites(t *testing.T) {
	f := newAPIFixture(t)
	preferences, err := application.NewFileDesktopVerificationSettingsStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	runtime := application.CapabilityReadinessRuntime{VerificationSettingsReader: preferences, VerificationSettingsControlEnabled: true}
	f.api.capabilityReadiness = application.NewRunCapabilityReadinessService(f.store, runtime)
	path := "/api/v1/runs/" + f.run.ID + "/capability-readiness"
	read := func() RunCapabilityReadinessView {
		t.Helper()
		response := performSessionMessageRequest(t, f.api, http.MethodGet, path, testAccessToken, "", "", nil)
		var view RunCapabilityReadinessView
		decodeDataStatus(t, response, http.StatusOK, &view)
		return view
	}
	initial := read()
	if initial.UIEvidence.Installed || initial.BatchValidation.Installed || initial.CapabilityGrant ||
		!reflect.DeepEqual(initial.UIEvidence.Remediation, []string{"enable_and_restart"}) ||
		!reflect.DeepEqual(initial.BatchValidation.Remediation, []string{"enable_and_restart"}) {
		t.Fatalf("disabled runtime lacks native enable guidance: %+v", initial)
	}
	// Model a preference already saved by the native-confirmed path. HTTP has
	// access only to Load through the narrow reader interface.
	settings := application.DesktopVerificationSettings{UIEvidenceEnabled: true}
	if _, _, err := preferences.Save(t.Context(), 1, settings); err != nil {
		t.Fatal(err)
	}
	pending := read()
	if pending.UIEvidence.Installed || !reflect.DeepEqual(pending.UIEvidence.Remediation, []string{"restart_application"}) ||
		!reflect.DeepEqual(pending.BatchValidation.Remediation, []string{"enable_and_restart"}) || pending.CapabilityGrant {
		t.Fatalf("readiness did not separate saved preference and active service: %+v", pending)
	}
	body := strings.NewReader(`{"settings":{"ui_evidence_enabled":false,"batch_validation_enabled":true},"expected_revision":2}`)
	response := performSessionMessageRequest(t, f.api, http.MethodPost, path, testAccessToken, "", "", body)
	if response.Code != http.StatusMethodNotAllowed {
		t.Fatalf("readiness exposed an HTTP write: status=%d body=%s", response.Code, response.Body.String())
	}
	unchanged, err := preferences.Load(t.Context())
	if err != nil || unchanged.Revision != 2 || unchanged.Settings != settings {
		t.Fatalf("HTTP changed native preferences: %+v err=%v", unchanged, err)
	}
	runtime.UIEvidenceInstalled = true
	f.api.capabilityReadiness = application.NewRunCapabilityReadinessService(f.store, runtime)
	active := read()
	if !active.UIEvidence.Installed || len(active.UIEvidence.BlockedBy) != 0 || len(active.UIEvidence.Remediation) != 0 ||
		active.BatchValidation.Installed || active.CapabilityGrant || active.CommandRuntime.CurrentRunGranted {
		t.Fatalf("installed startup services became an execution grant: %+v", active)
	}
}
