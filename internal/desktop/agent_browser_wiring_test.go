package desktop

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"runtime"
	"sync"
	"testing"

	"cyberagent-workbench/internal/browserruntime"
	"cyberagent-workbench/internal/credential"
	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/httpapi"
	"cyberagent-workbench/internal/modelregistry"
)

func TestControlPlaneWiresOrdinaryAgentBrowserIntoModelRequests(t *testing.T) {
	const model = "agent-browser-wiring-model"
	const providerSecret = "desktop-agent-browser-wiring-secret"
	qualificationFixture := &anthropicHarnessFixture{}

	var requestMu sync.Mutex
	modelRequests := make([]desktopSourceWiringRequest, 0, 2)
	provider := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/v1/messages" || request.Method != http.MethodPost {
			t.Errorf("unexpected Provider request %s %s", request.Method, request.URL.Path)
			writer.WriteHeader(http.StatusNotFound)
			return
		}
		if request.Header.Get("x-api-key") != providerSecret ||
			request.Header.Get("Accept") != "text/event-stream" {
			t.Errorf("Provider request omitted its credential or streaming contract")
			writer.WriteHeader(http.StatusUnauthorized)
			return
		}
		raw, err := io.ReadAll(io.LimitReader(request.Body, 256*1024))
		if err != nil {
			t.Errorf("read Provider request: %v", err)
			writer.WriteHeader(http.StatusBadRequest)
			return
		}
		var captured desktopSourceWiringRequest
		if err := json.Unmarshal(raw, &captured); err != nil || !captured.Stream || captured.Model != model {
			t.Errorf("invalid Provider request body: %s err=%v", string(raw), err)
			writer.WriteHeader(http.StatusBadRequest)
			return
		}
		phase, nonce, err := qualificationFixture.phase(raw)
		if err != nil {
			t.Error(err)
			writer.WriteHeader(http.StatusBadRequest)
			return
		}
		switch phase {
		case "tool_result":
			writeAnthropicTextSSE(t, writer, model, fmt.Sprintf(
				`{"version":"model_harness_probe.v1","status":"ok","nonce":"%s"}`, nonce))
		case "tool_call":
			writeAnthropicToolSSE(t, writer, model, nonce)
		default:
			requestMu.Lock()
			modelRequests = append(modelRequests, captured)
			requestMu.Unlock()
			writeAnthropicTextSSE(t, writer, model,
				`{"version":"root_lifecycle.v1","action":"finish","message":"browser wiring observed","summary":"operator request completed"}`)
		}
	}))
	defer provider.Close()

	t.Setenv("CYBERAGENT_ANTHROPIC_API_KEY", providerSecret)
	t.Setenv("CYBERAGENT_ANTHROPIC_BASE_URL", provider.URL)
	t.Setenv("CYBERAGENT_ANTHROPIC_MODEL", model)
	permissionCapabilities := domain.ExecutionPermissionRuntimeCapabilities{
		OperatorApprovalEnabled: true,
		DangerFullAccessEnabled: true,
		RuntimeAuthority:        domain.NewExecutionPermissionRuntimeAuthority(),
	}
	planeConfig := ControlPlaneConfig{
		DatabasePath: filepath.Join(t.TempDir(), "agent-browser-wiring.db"),
		ReadToken:    desktopControlPlaneTestToken, ControlToken: desktopControlPlaneControlToken,
		RunCreationEnabled: true, RunControlEnabled: true, SessionMessageEnabled: true,
		RunLifecycleEnabled: true, RunExecutionEnabled: true, ModelControlEnabled: true,
		ExecutionPermissionControlEnabled: true,
		ExecutionPermissionCapabilities:   permissionCapabilities,
		CredentialStore:                   credential.NewMemoryStore(),
		AppVersion:                        "desktop-agent-browser-wiring-test",
	}
	plane, err := OpenControlPlane(planeConfig)
	if err != nil {
		t.Fatal(err)
	}
	defer plane.Close()

	workspace, err := plane.RegisterWorkspaceDirectory(t.Context(), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	harness := desktopControlRequest(plane.Handler(), http.MethodPost,
		httpapi.ModelHarnessQualificationPath, "desktop-agent-browser-harness-0001",
		fmt.Sprintf(`{"version":%q,"provider":"anthropic","model":%q,"confirm_qualification":true}`,
			modelregistry.HarnessQualificationProtocolVersion, model))
	if harness.Code != http.StatusAccepted {
		t.Fatalf("Harness qualification status=%d body=%s", harness.Code, harness.Body.String())
	}
	var qualification httpapi.ModelHarnessQualificationView
	decodeDesktopControlData(t, harness, &qualification)
	if qualification.Status != modelregistry.HarnessDiagnosticQualified || !qualification.Harness.RootEligible || qualification.ModelCalls != 2 || qualification.SyntheticToolCalls != 1 {
		t.Fatalf("Harness qualification did not verify exactly one paired tool exchange: %#v", qualification)
	}
	route := desktopControlRequest(plane.Handler(), http.MethodPost,
		"/api/v1/models/routes/code", "desktop-agent-browser-route-0001",
		fmt.Sprintf(`{"version":%q,"provider":"anthropic","model":%q}`,
			modelregistry.RouteControlProtocolVersion, model))
	if route.Code != http.StatusAccepted {
		t.Fatalf("route selection status=%d body=%s", route.Code, route.Body.String())
	}

	fullThread := createDesktopSourceWiringThread(t, plane, workspace.ID,
		"Full ordinary browser wiring", "desktop-agent-browser-full-thread-0001")
	permission := desktopControlRequest(plane.Handler(), http.MethodPost,
		"/api/v1/threads/"+fullThread.Thread.ID+"/execution-permission",
		"desktop-agent-browser-full-permission-0001",
		`{"mode":"full","reason":"inspect ordinary browser tools","confirm_full":true}`)
	if permission.Code != http.StatusAccepted {
		t.Fatalf("Full selection status=%d body=%s", permission.Code, permission.Body.String())
	}
	assertDesktopCurrentApproval(t, plane, permissionCapabilities, fullThread.Run.ID, domain.RunExecutionPermissionFull)
	network := desktopControlRequest(plane.Handler(), http.MethodPut,
		"/api/v1/runs/"+fullThread.Run.ID+"/browser-network", "desktop-agent-browser-network-0001",
		`{"version":"browser_network_access.v1","expected_revision":1,"browser_network_access":"internet"}`)
	if network.Code != http.StatusOK {
		t.Fatalf("browser network selection status=%d body=%s", network.Code, network.Body.String())
	}
	completeDesktopSourceWiringTurn(t, plane, fullThread,
		"Inspect the ordinary browser tools available to this Run",
		"desktop-agent-browser-full-turn-0001")

	restrictedThread := createDesktopSourceWiringThread(t, plane, workspace.ID,
		"Ask ordinary browser boundary", "desktop-agent-browser-restricted-thread-0001")
	assertDesktopCurrentApproval(t, plane, permissionCapabilities, restrictedThread.Run.ID, domain.RunExecutionPermissionAsk)
	completeDesktopSourceWiringTurn(t, plane, restrictedThread,
		"Inspect tools without elevated execution permission",
		"desktop-agent-browser-restricted-turn-0001")
	profile, err := plane.stateStore.GetRunExecutionProfile(t.Context(), restrictedThread.Run.ID)
	if err != nil || profile.Profile != domain.RunExecutionProfilePreview {
		t.Fatalf("ordinary chat default execution profile=%+v err=%v", profile, err)
	}
	// Match the live desktop sequence: finish the first Ask reply, then select
	// Full in the existing chat. The running Ask epoch stays immutable, while
	// the explicit confirmation must be ready for the next user input.
	path := "/api/v1/threads/" + restrictedThread.Thread.ID + "/execution-permission"
	confirmed := desktopControlRequest(plane.Handler(), http.MethodPost, path,
		"desktop-agent-browser-after-reply-full",
		`{"mode":"full","reason":"explicit Full after the first reply","confirm_full":true}`)
	if confirmed.Code != http.StatusAccepted {
		t.Fatalf("post-reply Full selection status=%d body=%s", confirmed.Code, confirmed.Body.String())
	}
	var pending httpapi.ThreadExecutionPermissionControlView
	decodeDesktopControlData(t, confirmed, &pending)
	if pending.ExecutionPermission.FullActivation != "active" || pending.CurrentRunEffect != "deferred" || pending.CurrentRunSynchronized || pending.CurrentRunMode != "ask" {
		t.Fatalf("explicit Full was not ready for the next input: %+v", pending)
	}
	assertDesktopCurrentApproval(t, plane, permissionCapabilities, restrictedThread.Run.ID, domain.RunExecutionPermissionAsk)
	var refreshed httpapi.ThreadExecutionPermissionControlView
	decodeDesktopControlData(t, desktopAPIRequest(plane.Handler(), path), &refreshed)
	if refreshed.ExecutionPermission.FullActivation != "active" || refreshed.CurrentRunSynchronized {
		t.Fatalf("permission refresh lost the explicit future activation: %+v", refreshed)
	}
	nextResponse := desktopControlRequest(plane.Handler(), http.MethodPost,
		"/api/v1/threads/"+restrictedThread.Thread.ID+"/turns", "desktop-agent-browser-after-full-turn",
		`{"version":"thread_message_submission.v1","content":"Continue with the explicitly confirmed Full preference"}`)
	if nextResponse.Code != http.StatusAccepted {
		t.Fatalf("post-confirmation turn status=%d body=%s", nextResponse.Code, nextResponse.Body.String())
	}
	var next httpapi.ThreadMessageControlView
	decodeDesktopControlData(t, nextResponse, &next)
	if next.RunID == restrictedThread.Run.ID || !next.ModelCalled || !next.ExecutionStarted {
		t.Fatalf("Full successor did not execute the next real desktop input: %+v", next)
	}
	assertDesktopCurrentApproval(t, plane, permissionCapabilities, next.RunID, domain.RunExecutionPermissionFull)
	profile, err = plane.stateStore.GetRunExecutionProfile(t.Context(), next.RunID)
	if err != nil || profile.Profile != domain.RunExecutionProfilePreview {
		t.Fatalf("Full confirmation changed the default execution profile: %+v err=%v", profile, err)
	}
	var bound httpapi.ThreadExecutionPermissionControlView
	decodeDesktopControlData(t, desktopAPIRequest(plane.Handler(), path), &bound)
	if bound.ExecutionPermission.FullActivation != "active" || !bound.CurrentRunSynchronized || bound.CurrentRunMode != "full" {
		t.Fatalf("Full successor did not bind the same desktop authority: %+v", bound)
	}

	requestMu.Lock()
	captured := append([]desktopSourceWiringRequest(nil), modelRequests...)
	requestMu.Unlock()
	if len(captured) != 3 {
		t.Fatalf("Supervisor model request count=%d, want Full, Ask and confirmed Full successor", len(captured))
	}
	fullTools := desktopSourceWiringToolsByName(captured[0].Tools)
	restrictedTools := desktopSourceWiringToolsByName(captured[1].Tools)
	successorTools := desktopSourceWiringToolsByName(captured[2].Tools)
	for _, name := range []string{"workspace_read", "workspace_list", "workspace_grep", "workspace_change", "workspace_apply"} {
		if _, found := successorTools[name]; !found {
			t.Fatalf("confirmed Full successor omitted %s: %v", name, desktopSourceWiringToolNames(captured[2].Tools))
		}
	}
	for _, name := range []string{"command_runtime", "browser_navigate"} {
		if _, found := successorTools[name]; found {
			t.Fatalf("default preview/browser-off configuration unexpectedly advertised %s", name)
		}
	}
	for _, name := range []string{"browser_navigate", "browser_scroll", "browser_key", "browser_upload"} {
		_, fullFound := fullTools[name]
		_, restrictedFound := restrictedTools[name]
		if browserruntime.AgentBrowserPlatformAvailable() && !fullFound {
			t.Fatalf("native Full Provider tools omitted %s: %v", name,
				desktopSourceWiringToolNames(captured[0].Tools))
		}
		if !browserruntime.AgentBrowserPlatformAvailable() && fullFound {
			t.Fatalf("host without an adapter advertised ordinary browser tool %s", name)
		}
		if restrictedFound {
			t.Fatalf("Ask Run received ordinary browser tool %s: %v", name,
				desktopSourceWiringToolNames(captured[1].Tools))
		}
	}
	if !browserruntime.AgentBrowserPlatformAvailable() {
		return
	}

	statusResponse := desktopAPIRequest(plane.Handler(),
		"/api/v1/runs/"+fullThread.Run.ID+"/agent-browser")
	if statusResponse.Code != http.StatusOK {
		t.Fatalf("Agent browser status=%d body=%s", statusResponse.Code, statusResponse.Body.String())
	}
	var status httpapi.AgentBrowserStatusView
	decodeDesktopControlData(t, statusResponse, &status)
	if status.SessionID == "" || status.State != "idle" {
		t.Fatalf("model request did not create an observable idle Agent browser session: %#v", status)
	}
	closedResponse := desktopControlRequest(plane.Handler(), http.MethodPost,
		"/api/v1/runs/"+fullThread.Run.ID+"/agent-browser/close",
		"desktop-agent-browser-close-0001",
		fmt.Sprintf(`{"version":"agent_browser_close.v1","session_id":%q}`, status.SessionID))
	if closedResponse.Code != http.StatusOK {
		t.Fatalf("Agent browser close status=%d body=%s", closedResponse.Code, closedResponse.Body.String())
	}
	var closed httpapi.AgentBrowserStatusView
	decodeDesktopControlData(t, closedResponse, &closed)
	processesStopped := closed.TreeReaped
	if runtime.GOOS == "darwin" {
		processesStopped = !closed.TreeReaped && closed.ProcessScope == "no_owned_process_resources" && closed.ProcessScopeVerified
	}
	if closed.State != "closed" || !processesStopped || !closed.ProfileRemoved || closed.CleanupPending {
		t.Fatalf("idle Agent browser session did not close cleanly: %#v", closed)
	}
	if err := plane.Close(); err != nil {
		t.Fatal(err)
	}
	planeConfig.ExecutionPermissionCapabilities.RuntimeAuthority = domain.NewExecutionPermissionRuntimeAuthority()
	reopened, err := OpenControlPlane(planeConfig)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	var cold httpapi.ThreadExecutionPermissionControlView
	decodeDesktopControlData(t, desktopAPIRequest(reopened.Handler(), path), &cold)
	if cold.ExecutionPermission.FullActivation != "inactive" {
		t.Fatal("reopened desktop restored Full without explicit confirmation")
	}
	confirmed = desktopControlRequest(reopened.Handler(), http.MethodPost, path,
		"desktop-agent-browser-cold-full-reconfirmation",
		`{"mode":"full","reason":"explicit Full after desktop restart","confirm_full":true}`)
	if confirmed.Code != http.StatusAccepted {
		t.Fatalf("reopened Full confirmation status=%d body=%s", confirmed.Code, confirmed.Body.String())
	}
	decodeDesktopControlData(t, confirmed, &refreshed)
	if refreshed.ExecutionPermission.FullActivation != "active" || !refreshed.CurrentRunSynchronized {
		t.Fatalf("reopened Full did not require and accept reconfirmation: %+v", refreshed)
	}
	nextResponse = desktopControlRequest(reopened.Handler(), http.MethodPost,
		"/api/v1/threads/"+restrictedThread.Thread.ID+"/turns", "desktop-agent-browser-cold-full-turn",
		`{"version":"thread_message_submission.v1","content":"Continue after the explicit cold-process confirmation"}`)
	if nextResponse.Code != http.StatusAccepted {
		t.Fatalf("reopened Full turn status=%d body=%s", nextResponse.Code, nextResponse.Body.String())
	}
	decodeDesktopControlData(t, nextResponse, &next)
	if !next.ModelCalled {
		t.Fatal("reconfirmed desktop never reached the model")
	}
	assertDesktopCurrentApproval(t, reopened, planeConfig.ExecutionPermissionCapabilities, next.RunID, domain.RunExecutionPermissionFull)
}
