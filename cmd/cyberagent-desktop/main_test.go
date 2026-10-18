//go:build desktop

package main

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"cyberagent-workbench/internal/desktop"
	"cyberagent-workbench/internal/sandbox"

	"github.com/wailsapp/wails/v2/pkg/options"
	"github.com/wailsapp/wails/v2/pkg/runtime"
)

type testWindowRestorer struct {
	unminimised int
	shown       int
}

func (r *testWindowRestorer) Unminimise(context.Context) { r.unminimised++ }
func (r *testWindowRestorer) Show(context.Context)       { r.shown++ }

func newWailsRendererRequest(method, target string, body io.Reader) *http.Request {
	request := httptest.NewRequest(method, target, body)
	request.Host = trustedDesktopRendererHost()
	request.Header.Set("User-Agent", "PrayuDesktopTest/1.0 wails.io")
	request.URL.Scheme = ""
	request.URL.Host = ""
	request.RequestURI = request.URL.RequestURI()
	return request
}

func desktopTestScreen(current, primary bool, width, height int) runtime.Screen {
	screen := runtime.Screen{IsCurrent: current, IsPrimary: primary}
	screen.Size.Width = width
	screen.Size.Height = height
	return screen
}

func TestDesktopWindowMaximisesOnlyWhenTheDefaultSizeDoesNotFit(t *testing.T) {
	tests := []struct {
		name    string
		screens []runtime.Screen
		want    bool
	}{
		{name: "no screen data", want: false},
		{name: "large current screen", screens: []runtime.Screen{
			desktopTestScreen(true, true, 1706, 960)}, want: false},
		{name: "high DPI low resolution current screen", screens: []runtime.Screen{
			desktopTestScreen(true, true, 512, 384)}, want: true},
		{name: "small primary fallback", screens: []runtime.Screen{
			desktopTestScreen(false, true, 1024, 768)}, want: true},
	}
	for _, current := range tests {
		t.Run(current.name, func(t *testing.T) {
			if got := shouldMaximiseDesktopWindow(current.screens); got != current.want {
				t.Fatalf("shouldMaximiseDesktopWindow() = %t, want %t", got, current.want)
			}
		})
	}
}

func TestDesktopWorkspaceSandboxAvailabilityRequiresValidatedReadiness(t *testing.T) {
	requested := desktopOptions{workspaceSandbox: true}
	if desktopWorkspaceSandboxRuntimeAvailable(requested, nil) ||
		desktopWorkspaceSandboxRuntimeAvailable(requested,
			&sandbox.LocalReadiness{Ready: false}) {
		t.Fatal("a requested but unavailable Workspace Sandbox became runtime-available")
	}
	ready := &sandbox.LocalReadiness{Ready: true}
	if !desktopWorkspaceSandboxRuntimeAvailable(requested, ready) {
		t.Fatal("validated Workspace Sandbox readiness was not projected")
	}
	if desktopWorkspaceSandboxRuntimeAvailable(desktopOptions{}, ready) {
		t.Fatal("unrequested Workspace Sandbox readiness became runtime-available")
	}
}

func TestDesktopOptionsDefaultToSafeProductAndKeepGranularCapabilitiesExplicit(t *testing.T) {
	defaults, err := parseDesktopOptions(nil)
	if err != nil {
		t.Fatal(err)
	}
	if !defaults.profileControl || !defaults.permissionControl ||
		!defaults.workspaceSandbox || !defaults.browserCDPControl ||
		!defaults.dangerFullAccess || !defaults.fullCDPDebug ||
		!defaults.runCreation || !defaults.runExecution || !defaults.modelControl ||
		!defaults.providerCredentials || !defaults.gitAdvanced || !defaults.riskProfileRestart ||
		!defaults.githubReview || !defaults.scheduledJobControl ||
		!defaults.scheduledJobWorker || !defaults.scheduledJobConfirmedOnly {
		t.Fatalf("default safe product capability bundle is incomplete: %#v", defaults)
	}
	if defaults.operatorPreview || defaults.safeView ||
		defaults.userTerminal ||
		defaults.dockerExecution || defaults.batchValidation || defaults.runWakeWorker {
		t.Fatalf("default product launch silently enabled high-risk authority: %#v", defaults)
	}
	readOnly, err := parseDesktopOptions([]string{"--safe-view"})
	if err != nil {
		t.Fatal(err)
	}
	if readOnly != (desktopOptions{safeView: true}) {
		t.Fatalf("explicit Safe View is not read-only: %#v", readOnly)
	}
	if readOnly.riskProfileRestart {
		t.Fatal("explicit Safe View exposed the high-risk restart protocol")
	}
	if _, err := parseDesktopOptions([]string{
		"--safe-view", "--enable-run-creation",
	}); err == nil {
		t.Fatal("Safe View was combined with a control capability")
	}
	for _, current := range []struct {
		flag string
		want desktopOptions
	}{
		{flag: "--enable-profile-control", want: desktopOptions{profileControl: true}},
		{flag: "--enable-permission-control", want: desktopOptions{permissionControl: true}},
		{flag: "--enable-browser-cdp-control", want: desktopOptions{browserCDPControl: true}},
		{flag: "--enable-run-creation", want: desktopOptions{runCreation: true}},
		{flag: "--enable-session-messages", want: desktopOptions{sessionMessages: true}},
		{flag: "--enable-session-steering-control", want: desktopOptions{sessionSteeringControl: true}},
		{flag: "--enable-run-lifecycle", want: desktopOptions{runLifecycle: true}},
		{flag: "--enable-run-execution", want: desktopOptions{runExecution: true}},
		{flag: "--enable-plan-delivery", want: desktopOptions{planDeliveryControl: true}},
		{flag: "--enable-approvals", want: desktopOptions{approvalControl: true}},
		{flag: "--enable-model-control", want: desktopOptions{modelControl: true}},
		{flag: "--enable-provider-credentials", want: desktopOptions{providerCredentials: true}},
		{flag: "--enable-file-edit-review", want: desktopOptions{fileEditReview: true}},
		{flag: "--enable-file-edit-proposals", want: desktopOptions{fileEditProposals: true}},
		{flag: "--enable-run-wake", want: desktopOptions{runWakeControl: true}},
		{flag: "--enable-file-edit-apply", want: desktopOptions{fileEditApply: true}},
		{flag: "--enable-run-wake-execution", want: desktopOptions{runWakeExecution: true}},
		{flag: "--enable-wake-worker", want: desktopOptions{runWakeWorker: true}},
		{flag: "--enable-scheduled-jobs", want: desktopOptions{scheduledJobControl: true}},
		{flag: "--enable-skill-installation", want: desktopOptions{skillInstallation: true}},
		{flag: "--enable-evidence-attachments", want: desktopOptions{evidenceAttachment: true}},
		{flag: "--enable-verification-evidence", want: desktopOptions{verificationEvidence: true}},
		{flag: "--enable-batch-delivery-control", want: desktopOptions{batchDeliveryControl: true}},
	} {
		parsed, err := parseDesktopOptions([]string{current.flag})
		if err != nil {
			t.Fatal(err)
		}
		if parsed != current.want {
			t.Fatalf("%s was not independently explicit: %#v", current.flag, parsed)
		}
		if parsed.riskProfileRestart {
			t.Fatalf("%s exposed the high-risk restart protocol", current.flag)
		}
	}
	if _, err := parseDesktopOptions([]string{"unexpected"}); err == nil {
		t.Fatal("desktop positional argument was accepted")
	}
	if _, err := parseDesktopOptions([]string{
		"--enable-danger-full-access",
	}); err == nil {
		t.Fatal("danger-full-access without permission control was accepted")
	}
	if _, err := parseDesktopOptions([]string{
		"--enable-permission-control", "--enable-danger-full-access",
		"--enable-debug-maximum-access",
	}); err == nil {
		t.Fatal("retired Debug execution flag was accepted")
	}
	if _, err := parseDesktopOptions([]string{
		"--enable-full-cdp-debug",
	}); err == nil {
		t.Fatal("full CDP debug without its parent gates was accepted")
	}
	if _, err := parseDesktopOptions([]string{
		"--enable-host-command-proposals",
	}); err == nil {
		t.Fatal("host command proposals without permission control were accepted")
	}
	if _, err := parseDesktopOptions([]string{
		"--enable-workspace-sandbox",
	}); err == nil || !strings.Contains(err.Error(), "--enable-permission-control") {
		t.Fatalf("Workspace Sandbox without permission control was accepted: %v", err)
	}
	workspaceSandbox, err := parseDesktopOptions([]string{
		"--enable-permission-control", "--enable-workspace-sandbox",
	})
	if err != nil || !workspaceSandbox.permissionControl ||
		!workspaceSandbox.workspaceSandbox {
		t.Fatalf("Workspace Sandbox capability set is incomplete: %+v err=%v",
			workspaceSandbox, err)
	}
	if _, err := parseDesktopOptions([]string{
		"--enable-batch-validation-execution",
	}); err == nil {
		t.Fatal("batch host validation without permission/full access was accepted")
	}
	if _, err := parseDesktopOptions([]string{"--enable-git-advanced"}); err == nil ||
		!strings.Contains(err.Error(), "--enable-permission-control") {
		t.Fatalf("Git advanced control without permission control was accepted: %v", err)
	}
	gitAdvanced, err := parseDesktopOptions([]string{
		"--enable-permission-control", "--enable-git-advanced",
		"--git-worktree-root", `D:\PrayuManagedWorktrees`,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !gitAdvanced.permissionControl || !gitAdvanced.gitAdvanced ||
		gitAdvanced.gitWorktreeRoot != `D:\PrayuManagedWorktrees` {
		t.Fatalf("Git advanced capability set is incomplete: %+v", gitAdvanced)
	}
	if _, err := parseDesktopOptions([]string{
		"--enable-scheduled-job-worker",
	}); err == nil || !strings.Contains(err.Error(), "--enable-scheduled-jobs") {
		t.Fatalf("scheduled worker without its control capability was accepted: %v", err)
	}
	scheduled, err := parseDesktopOptions([]string{
		"--enable-scheduled-jobs", "--enable-scheduled-job-worker",
	})
	if err != nil {
		t.Fatal(err)
	}
	if !scheduled.scheduledJobControl || !scheduled.scheduledJobWorker || scheduled.scheduledJobConfirmedOnly {
		t.Fatalf("scheduled job capability set is incomplete: %+v", scheduled)
	}
	batchValidation, err := parseDesktopOptions([]string{
		"--enable-permission-control", "--enable-danger-full-access",
		"--enable-batch-delivery-control",
		"--enable-batch-validation-execution",
	})
	if err != nil {
		t.Fatal(err)
	}
	if !batchValidation.permissionControl || !batchValidation.dangerFullAccess ||
		!batchValidation.batchDeliveryControl || !batchValidation.batchValidation {
		t.Fatalf("batch host validation capability set is incomplete: %+v", batchValidation)
	}

	maximum, err := parseDesktopOptions([]string{
		"--enable-permission-control", "--enable-danger-full-access",
		"--enable-user-terminal",
		"--enable-browser-cdp-control", "--enable-full-cdp-debug",
	})
	if err != nil {
		t.Fatal(err)
	}
	if !maximum.permissionControl || !maximum.dangerFullAccess ||
		!maximum.userTerminal ||
		!maximum.browserCDPControl || !maximum.fullCDPDebug {
		t.Fatalf("maximum debug capability set is incomplete: %+v", maximum)
	}
}

func TestDesktopOperatorPreviewEnablesTheSafeProductBundleOnly(t *testing.T) {
	preview, err := parseDesktopOptions([]string{"--operator-preview"})
	if err != nil {
		t.Fatal(err)
	}
	if !preview.operatorPreview || !preview.profileControl || !preview.permissionControl ||
		!preview.workspaceSandbox ||
		!preview.browserCDPControl || !preview.dangerFullAccess || !preview.fullCDPDebug ||
		!preview.runCreation || !preview.sessionMessages ||
		!preview.sessionSteeringControl || !preview.runLifecycle || !preview.runExecution ||
		!preview.planDeliveryControl || !preview.approvalControl ||
		!preview.modelControl ||
		!preview.providerCredentials || !preview.fileEditReview ||
		!preview.fileEditProposals || !preview.runWakeControl || !preview.fileEditApply ||
		!preview.runWakeExecution || !preview.skillInstallation ||
		!preview.scheduledJobControl || !preview.scheduledJobWorker ||
		!preview.evidenceAttachment || !preview.verificationEvidence ||
		!preview.embeddedAnalyzer || !preview.batchDeliveryControl || !preview.gitAdvanced ||
		!preview.githubReview {
		t.Fatalf("operator preview capability bundle is incomplete: %+v", preview)
	}
	if preview.scheduledJobConfirmedOnly ||
		preview.runWakeWorker || preview.userTerminal || preview.dockerExecution ||
		preview.riskProfileRestart ||
		preview.batchValidation {
		t.Fatalf("operator preview silently enabled a high-risk capability: %+v", preview)
	}
}

func TestDesktopDockerExecutionRequiresAnExplicitPermissionGate(t *testing.T) {
	if _, err := parseDesktopOptions([]string{"--enable-docker-execution"}); err == nil ||
		!strings.Contains(err.Error(), "--enable-permission-control") {
		t.Fatalf("Docker execution without permission control was accepted: %v", err)
	}
	options, err := parseDesktopOptions([]string{
		"--enable-permission-control", "--enable-docker-execution",
	})
	if err != nil {
		t.Fatal(err)
	}
	if !options.permissionControl || !options.dockerExecution ||
		options.dangerFullAccess || options.userTerminal {
		t.Fatalf("Docker execution widened an unrelated capability: %+v", options)
	}
	preview, err := parseDesktopOptions([]string{
		"--operator-preview", "--enable-docker-execution",
	})
	if err != nil {
		t.Fatal(err)
	}
	if !preview.operatorPreview || !preview.permissionControl || !preview.dockerExecution {
		t.Fatalf("explicit Docker opt-in was not retained with preview: %+v", preview)
	}
}

func TestPackagedSecurityModeIsExactAndCannotInheritDesktopAuthority(t *testing.T) {
	options, err := parseDesktopOptions([]string{
		"--standard-code-attack-matrix",
		"--attack-matrix-root", `D:\harness\standard-code-attack-181`,
		"--candidate-archive", `D:\candidate\TraverseBoard.zip`,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !options.securityMatrix || options.securityMatrixRoot == "" ||
		options.securityCandidate == "" || options.profileControl ||
		options.permissionControl || options.workspaceSandbox || options.runExecution ||
		options.dangerFullAccess || options.userTerminal ||
		options.dockerExecution {
		t.Fatalf("packaged security mode inherited product authority: %+v", options)
	}
	for _, arguments := range [][]string{
		{"--standard-code-attack-matrix"},
		{"--attack-matrix-root", `D:\harness\standard-code-attack-181`},
		{"--standard-code-attack-matrix", "--attack-matrix-root",
			`D:\harness\standard-code-attack-181`, "--candidate-archive",
			`D:\candidate\TraverseBoard.zip`, "--enable-danger-full-access"},
	} {
		if _, err := parseDesktopOptions(arguments); err == nil {
			t.Fatalf("incomplete or widened packaged security arguments were accepted: %v",
				arguments)
		}
	}
}

func TestPackagedRecoveryWorkerModeIsExactAndInternalOnly(t *testing.T) {
	arguments := []string{
		"--standard-code-attack-recovery-worker",
		"--recovery-worker-root", `D:\harness\standard-code-attack-181\runtime\recovery\recovery-recovery_force_termination-local`,
		"--recovery-worker-case", "recovery_force_termination",
		"--recovery-worker-backend", "local",
		"--recovery-worker-phase", "prepare",
	}
	options, err := parseDesktopOptions(arguments)
	if err != nil {
		t.Fatal(err)
	}
	if !options.securityRecoveryWorker || options.securityRecoveryRoot == "" ||
		options.securityRecoveryCase != "recovery_force_termination" ||
		options.securityRecoveryBackend != "local" ||
		options.securityRecoveryPhase != "prepare" || options.securityMatrix ||
		options.permissionControl || options.workspaceSandbox || options.runExecution ||
		options.dangerFullAccess || options.dockerExecution {
		t.Fatalf("packaged recovery worker inherited Desktop authority: %+v", options)
	}
	for _, invalid := range [][]string{
		arguments[:1],
		append(append([]string(nil), arguments...), "--enable-danger-full-access"),
		{"--recovery-worker-root", arguments[2]},
	} {
		if _, err := parseDesktopOptions(invalid); err == nil {
			t.Fatalf("incomplete or widened recovery worker arguments were accepted: %v", invalid)
		}
	}
}

func TestSecondInstanceHandlerIgnoresArgumentsAndRecoversAfterStartup(t *testing.T) {
	restorer := &testWindowRestorer{}
	lifecycle := desktop.NewLifecycle(restorer)
	handler := secondInstanceHandler(lifecycle)
	handler(options.SecondInstanceData{
		Args: []string{"--secret", `C:\PRIVATE\workspace`}, WorkingDirectory: `C:\PRIVATE`,
	})
	if restorer.unminimised != 0 || restorer.shown != 0 {
		t.Fatal("second instance restored the window before startup")
	}
	lifecycle.Start(context.Background())
	if restorer.unminimised != 1 || restorer.shown != 1 {
		t.Fatalf("second instance recovery count = %d/%d", restorer.unminimised, restorer.shown)
	}
	lifecycle.Stop()
}

func TestInProcessAPIHandlerPinsLoopbackBoundaryWithoutMutatingRequest(t *testing.T) {
	var receivedHost string
	var receivedRemote string
	var receivedPath string
	var receivedScheme string
	var receivedURLHost string
	next := http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		receivedHost = request.Host
		receivedRemote = request.RemoteAddr
		receivedPath = request.URL.Path
		receivedScheme = request.URL.Scheme
		receivedURLHost = request.URL.Host
		writer.WriteHeader(http.StatusNoContent)
	})
	handler := inProcessAPIHandler{next: next}
	request := newWailsRendererRequest(http.MethodGet, "http://wails.localhost/api/v1/health", nil)
	request.RemoteAddr = "203.0.113.10:443"
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)

	if response.Code != http.StatusNoContent || receivedHost != "127.0.0.1" ||
		receivedRemote != "127.0.0.1:0" || receivedPath != "/api/v1/health" ||
		receivedScheme != "" || receivedURLHost != "" {
		t.Fatalf("unexpected in-process projection: status=%d host=%q remote=%q path=%q",
			response.Code, receivedHost, receivedRemote, receivedPath)
	}
	if request.Host != trustedDesktopRendererHost() || request.RemoteAddr != "203.0.113.10:443" ||
		request.URL.Scheme != "" || request.URL.Host != "" {
		t.Fatalf("original request was mutated: host=%q remote=%q", request.Host, request.RemoteAddr)
	}
}

func TestInProcessAPIHandlerAcceptsWailsWebViewRequestShape(t *testing.T) {
	called := false
	handler := inProcessAPIHandler{next: http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		called = true
		writer.WriteHeader(http.StatusNoContent)
	})}
	request := newWailsRendererRequest(http.MethodGet,
		"http://wails.localhost/api/v1/desktop/bootstrap", nil)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusNoContent || !called {
		t.Fatalf("real Wails request shape was rejected: status=%d called=%t", response.Code, called)
	}
}

func TestInProcessAPIHandlerRejectsNonRendererOrigins(t *testing.T) {
	for _, target := range []string{
		"https://wails.localhost/api/v1/health",
		"http://wails.localhost:80/api/v1/health",
		"http://user@wails.localhost/api/v1/health",
		"http://untrusted.example/api/v1/health",
	} {
		t.Run(target, func(t *testing.T) {
			called := false
			handler := inProcessAPIHandler{next: http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
				called = true
			})}
			response := httptest.NewRecorder()
			request := httptest.NewRequest(http.MethodGet, target, nil)
			request.Header.Set("User-Agent", "PrayuDesktopTest/1.0 wails.io")
			handler.ServeHTTP(response, request)
			if response.Code != http.StatusForbidden || called {
				t.Fatalf("origin %q reached API: status=%d called=%t", target, response.Code, called)
			}
		})
	}
}

func TestInProcessAPIHandlerRejectsNonCanonicalRendererURL(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*http.Request)
	}{
		{name: "fragment", mutate: func(request *http.Request) { request.URL.Fragment = "fragment" }},
		{name: "opaque", mutate: func(request *http.Request) {
			request.URL.Opaque = "//wails.localhost/api/v1/health"
		}},
	}
	for _, current := range tests {
		t.Run(current.name, func(t *testing.T) {
			called := false
			handler := inProcessAPIHandler{next: http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
				called = true
			})}
			request := newWailsRendererRequest(http.MethodGet,
				"http://wails.localhost/api/v1/health", nil)
			current.mutate(request)
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request)
			if response.Code != http.StatusForbidden || called {
				t.Fatalf("non-canonical renderer URL reached API: status=%d called=%t",
					response.Code, called)
			}
		})
	}
}

func TestInProcessAPIHandlerRejectsOriginlessRequestsWithoutWailsAuthority(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*http.Request)
	}{
		{name: "missing user agent", mutate: func(request *http.Request) {
			request.Header.Del("User-Agent")
		}},
		{name: "substring user agent", mutate: func(request *http.Request) {
			request.Header.Set("User-Agent", "not-wails.io-client")
		}},
		{name: "missing host", mutate: func(request *http.Request) { request.Host = "" }},
		{name: "wrong host", mutate: func(request *http.Request) { request.Host = "untrusted.example" }},
		{name: "host port", mutate: func(request *http.Request) { request.Host = "wails.localhost:80" }},
		{name: "host whitespace", mutate: func(request *http.Request) { request.Host = " wails.localhost" }},
		{name: "partial authority", mutate: func(request *http.Request) { request.URL.Scheme = "http" }},
	}
	for _, current := range tests {
		t.Run(current.name, func(t *testing.T) {
			called := false
			handler := inProcessAPIHandler{next: http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
				called = true
			})}
			request := newWailsRendererRequest(http.MethodGet,
				"http://wails.localhost/api/v1/health", nil)
			current.mutate(request)
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request)
			if response.Code != http.StatusForbidden || called {
				t.Fatalf("untrusted originless request reached API: status=%d called=%t",
					response.Code, called)
			}
		})
	}
}

func TestInProcessAPIHandlerFailsClosedWithoutTarget(t *testing.T) {
	request := httptest.NewRequest(http.MethodGet, "http://wails.localhost/", nil)
	response := httptest.NewRecorder()
	inProcessAPIHandler{}.ServeHTTP(response, request)
	if response.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want %d", response.Code, http.StatusInternalServerError)
	}
}

func TestInProcessAPIHandlerFailsClosedWithoutURL(t *testing.T) {
	request := httptest.NewRequest(http.MethodGet, "http://wails.localhost/", nil)
	request.URL = nil
	response := httptest.NewRecorder()
	inProcessAPIHandler{next: http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Fatal("nil URL request reached the API")
	})}.ServeHTTP(response, request)
	if response.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want %d", response.Code, http.StatusInternalServerError)
	}
}

func TestInProcessAPIHandlerRebuildsMissingRequestURIFromURL(t *testing.T) {
	var requestURI string
	handler := inProcessAPIHandler{next: http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		requestURI = request.RequestURI
		writer.WriteHeader(http.StatusNoContent)
	})}
	request := newWailsRendererRequest(http.MethodGet,
		"http://wails.localhost/api/v1/health?probe=one", nil)
	request.RequestURI = ""
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusNoContent || requestURI != "/api/v1/health?probe=one" {
		t.Fatalf("status=%d request_uri=%q", response.Code, requestURI)
	}
	if request.RequestURI != "" {
		t.Fatal("source request URI was mutated")
	}
}

func TestInProcessAPIHandlerCanonicalizesMismatchedRequestURI(t *testing.T) {
	var requestURI string
	handler := inProcessAPIHandler{next: http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		requestURI = request.RequestURI
		writer.WriteHeader(http.StatusNoContent)
	})}
	request := newWailsRendererRequest(http.MethodGet,
		"http://wails.localhost/api/v1/health?probe=one", nil)
	request.RequestURI = "http://untrusted.example/private?secret=true"
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusNoContent || requestURI != "/api/v1/health?probe=one" {
		t.Fatalf("status=%d request_uri=%q", response.Code, requestURI)
	}
	if request.RequestURI != "http://untrusted.example/private?secret=true" {
		t.Fatal("source request URI was mutated")
	}
}

func TestInProcessAPIHandlerCanonicalizesOnlyTheWailsEmptyRoot(t *testing.T) {
	var path string
	var requestURI string
	var contentLength int64
	handler := inProcessAPIHandler{next: http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		path = request.URL.Path
		requestURI = request.RequestURI
		contentLength = request.ContentLength
		writer.WriteHeader(http.StatusNoContent)
	})}
	request := newWailsRendererRequest(http.MethodGet, "http://wails.localhost/", nil)
	request.URL.Path = ""
	request.RequestURI = ""
	request.ContentLength = -1
	request.Body = http.NoBody
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusNoContent || path != "/" || requestURI != "/" || contentLength != 0 {
		t.Fatalf("status=%d path=%q request_uri=%q content_length=%d",
			response.Code, path, requestURI, contentLength)
	}
	if request.URL.Path != "" || request.RequestURI != "" || request.ContentLength != -1 {
		t.Fatal("Wails source request was mutated")
	}
}

func TestInProcessAPIHandlerDoesNotEraseUnknownRequestBodies(t *testing.T) {
	var contentLength int64
	handler := inProcessAPIHandler{next: http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		contentLength = request.ContentLength
		writer.WriteHeader(http.StatusNoContent)
	})}
	request := newWailsRendererRequest(http.MethodGet,
		"http://wails.localhost/api/v1/health", nil)
	request.ContentLength = -1
	request.Body = io.NopCloser(strings.NewReader("unexpected"))
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusNoContent || contentLength != -1 {
		t.Fatalf("status=%d content_length=%d", response.Code, contentLength)
	}
}

func TestRetiredCommandExecutionFlagsAreRejected(t *testing.T) {
	for _, flag := range []string{"--enable-command-proposals", "--enable-host-command-proposals", "--enable-debug-maximum-access"} {
		if _, err := parseDesktopOptions([]string{flag}); err == nil {
			t.Fatalf("retired execution flag accepted: %s", flag)
		}
	}
}
