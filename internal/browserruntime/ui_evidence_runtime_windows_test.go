//go:build windows

package browserruntime

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"image/png"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"unsafe"

	"cyberagent-workbench/internal/uievidence"
	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"
)

const uiEvidenceRuntimeSmokeEnvironment = "CYBERAGENT_UI_EVIDENCE_SMOKE"

type uiEvidenceSmokeArtifact struct {
	File        string                 `json:"file"`
	MIME        string                 `json:"mime"`
	SHA256      string                 `json:"sha256"`
	Bytes       int                    `json:"bytes"`
	Width       int                    `json:"width"`
	Height      int                    `json:"height"`
	Matrix      string                 `json:"matrix"`
	Environment uievidence.Environment `json:"environment"`
}

type uiEvidenceSmokeReceipt struct {
	ProtocolVersion   string                    `json:"protocol_version"`
	SourceCommit      string                    `json:"source_commit"`
	DirtyDigest       string                    `json:"dirty_digest"`
	CleanCheckout     bool                      `json:"clean_checkout"`
	BrowserProduct    BrowserProduct            `json:"browser_product"`
	BrowserChannel    BrowserChannel            `json:"browser_channel"`
	BrowserVersion    string                    `json:"browser_version"`
	ExecutableSHA256  string                    `json:"executable_sha256"`
	DriverProtocol    string                    `json:"driver_protocol"`
	OriginScope       string                    `json:"origin_scope"`
	TemporaryProfile  bool                      `json:"temporary_profile"`
	Headless          bool                      `json:"headless"`
	RetentionDays     int                       `json:"retention_days"`
	Routes            []string                  `json:"routes"`
	RegressionCaught  bool                      `json:"regression_caught"`
	BrowserTreeReaped bool                      `json:"browser_tree_reaped"`
	BrowserPortFreed  bool                      `json:"browser_port_released"`
	ProfileRemoved    bool                      `json:"profile_removed"`
	FixtureReaped     bool                      `json:"fixture_server_reaped"`
	UntrustedEvidence bool                      `json:"untrusted_evidence"`
	CapturedAt        time.Time                 `json:"captured_at"`
	Artifacts         []uiEvidenceSmokeArtifact `json:"artifacts"`
}

type uiEvidenceSmokeLaunchReceipt struct {
	ProtocolVersion  string         `json:"protocol_version"`
	SourceCommit     string         `json:"source_commit"`
	DirtyDigest      string         `json:"dirty_digest"`
	CleanCheckout    bool           `json:"clean_checkout"`
	BrowserProduct   BrowserProduct `json:"browser_product"`
	BrowserChannel   BrowserChannel `json:"browser_channel"`
	BrowserVersion   string         `json:"browser_version"`
	ExecutableSHA256 string         `json:"executable_sha256"`
	ProcessAdapter   string         `json:"process_adapter"`
	TemporaryProfile bool           `json:"temporary_profile"`
	Headless         bool           `json:"headless"`
	CapturedAt       time.Time      `json:"captured_at"`
}

type uiEvidenceSmokeStartupDiagnostic struct {
	ProtocolVersion         string            `json:"protocol_version"`
	Reason                  string            `json:"reason"`
	ProcessAdapter          string            `json:"process_adapter"`
	PID                     int               `json:"pid"`
	MainProcessActive       bool              `json:"main_process_active"`
	MainProcessExitCode     uint32            `json:"main_process_exit_code"`
	JobTotalProcesses       uint32            `json:"job_total_processes"`
	JobActiveProcesses      uint32            `json:"job_active_processes"`
	JobTerminatedProcesses  uint32            `json:"job_terminated_processes"`
	ProcessQueryError       string            `json:"process_query_error,omitempty"`
	JobQueryError           string            `json:"job_query_error,omitempty"`
	ProfileEntries          []string          `json:"profile_entries"`
	ProfileReadError        string            `json:"profile_read_error,omitempty"`
	RemoteDebuggingPolicies map[string]string `json:"remote_debugging_policies"`
	CapturedAt              time.Time         `json:"captured_at"`
}

// TestInstalledEdgeUIEvidenceHeadlessMatrixAndRegression is opt-in because it
// starts the real, fixed-location Edge binary. Unit tests cover every
// authorization/lifecycle boundary; this test deliberately exercises the same
// closed CDP transport against a real engine and a deterministic loopback page.
func TestInstalledEdgeUIEvidenceHeadlessMatrixAndRegression(t *testing.T) {
	if os.Getenv(uiEvidenceRuntimeSmokeEnvironment) != "1" {
		t.Skip("set CYBERAGENT_UI_EVIDENCE_SMOKE=1 to exercise real Edge UI evidence")
	}

	commit, dirtyDigest, clean := uiEvidenceSmokeSourceBinding(t)
	identity := installedStableEdge(t)
	artifactDirectory := uiEvidenceSmokeArtifactDirectory(t)
	writeUIEvidenceSmokeLaunchReceipt(t, artifactDirectory, uiEvidenceSmokeLaunchReceipt{
		ProtocolVersion: "ui-evidence-ci-launch.v1", SourceCommit: commit,
		DirtyDigest: dirtyDigest, CleanCheckout: clean,
		BrowserProduct: identity.Product, BrowserChannel: identity.Channel,
		BrowserVersion: identity.Version, ExecutableSHA256: identity.ExecutableSHA256,
		ProcessAdapter:   WindowsBrowserProcessAdapterName,
		TemporaryProfile: true, Headless: true, CapturedAt: time.Now().UTC(),
	})
	t.Logf("real Edge launch: commit=%s version=%s executable_sha256=%s adapter=%s",
		commit, identity.Version, identity.ExecutableSHA256,
		WindowsBrowserProcessAdapterName)
	origin, closeFixture := startUIEvidenceSmokeFixture(t)
	defer closeFixture()

	scope, err := NewTargetScope(ProfileSafeWeb, []string{origin})
	if err != nil {
		t.Fatal(err)
	}
	profileRoot := t.TempDir()
	profilePath := filepath.Join(profileRoot, "edge-profile")
	if !pathWithinRoot(profileRoot, profilePath) {
		t.Fatal("smoke browser profile escaped its temporary root")
	}
	if err := os.Mkdir(profilePath, 0o700); err != nil {
		t.Fatal(err)
	}

	process := startUIEvidenceSmokeEdge(t, identity, profilePath)
	defer process.Stop(t)

	endpoint := waitForUIEvidenceSmokeEndpoint(t, profilePath, process,
		artifactDirectory)
	// Connect and initialize under the same bounded startup deadline.
	operationContext, cancel := context.WithTimeout(t.Context(), 45*time.Second)
	defer cancel()
	connection, err := dialRestrictedCDP(operationContext, endpoint)
	if err != nil {
		t.Fatal(err)
	}
	client := &restrictedCDPClient{conn: connection, scope: scope,
		maxRequests: 500, uiEvidence: true}
	if err := client.initialize(operationContext); err != nil {
		_ = connection.Close()
		t.Fatalf("initialize real Edge restricted CDP: %v", err)
	}
	clientClosed := false
	defer func() {
		if !clientClosed {
			_ = client.close(context.Background())
		}
	}()

	receipt := uiEvidenceSmokeReceipt{
		ProtocolVersion: "ui-evidence-ci-smoke.v1", SourceCommit: commit,
		DirtyDigest: dirtyDigest, CleanCheckout: clean, BrowserProduct: identity.Product,
		BrowserChannel: identity.Channel, BrowserVersion: identity.Version,
		ExecutableSHA256: identity.ExecutableSHA256,
		DriverProtocol:   uievidence.DriverProtocolVersion, OriginScope: origin,
		TemporaryProfile: true, Headless: true, RetentionDays: 5,
		Routes:            []string{"/fixed", "/regression"},
		UntrustedEvidence: true,
		CapturedAt:        time.Now().UTC(),
	}

	matrix := []struct {
		name        string
		environment uievidence.Environment
	}{
		{name: "desktop-light-en", environment: uievidence.Environment{
			Viewport: uievidence.Viewport{Width: 1440, Height: 900, DPR: 1},
			Locale:   "en-US", Theme: uievidence.ThemeLight}},
		{name: "mobile-dark-zh-reduced", environment: uievidence.Environment{
			Viewport: uievidence.Viewport{Width: 390, Height: 844, DPR: 2},
			Locale:   "zh-CN", Theme: uievidence.ThemeDark, ReducedMotion: true}},
	}
	for _, item := range matrix {
		t.Run(item.name, func(t *testing.T) {
			configureUIEvidenceSmoke(t, client, item.environment)
			navigateUIEvidenceSmoke(t, client, origin+"/fixed")
			assertUIEvidenceSmokeSelector(t, client,
				fmt.Sprintf(`body[data-theme=%q]`, item.environment.Theme), true)
			motion := "full"
			if item.environment.ReducedMotion {
				motion = "reduced"
			}
			assertUIEvidenceSmokeSelector(t, client,
				fmt.Sprintf(`body[data-motion=%q]`, motion), true)
			assertUIEvidenceSmokeSelector(t, client,
				fmt.Sprintf(`body[data-locale=%q]`, item.environment.Locale), true)

			clickUIEvidenceSmoke(t, client, "#repair")
			waitForUIEvidenceSmokeSelector(t, client, `body[data-state="fixed"]`, true)
			typeUIEvidenceSmoke(t, client, "#fixture-input", "fixture input")
			waitForUIEvidenceSmokeSelector(t, client, `body[data-typed="true"]`, true)
			verifyUIEvidenceSmokeTextCaptures(t, client)

			screenshot, err := client.captureScreenshot(t.Context(), "ci-smoke")
			if err != nil {
				t.Fatal(err)
			}
			configuration, err := png.DecodeConfig(bytes.NewReader(screenshot.PNG))
			if err != nil {
				t.Fatalf("decode real Edge screenshot: %v", err)
			}
			wantWidth := int(float64(item.environment.Viewport.Width) *
				item.environment.Viewport.DPR)
			wantHeight := int(float64(item.environment.Viewport.Height) *
				item.environment.Viewport.DPR)
			if configuration.Width != wantWidth || configuration.Height != wantHeight {
				t.Fatalf("screenshot size = %dx%d, want %dx%d", configuration.Width,
					configuration.Height, wantWidth, wantHeight)
			}
			fileName := item.name + ".png"
			writeUIEvidenceSmokeArtifact(t, artifactDirectory, fileName, screenshot.PNG)
			receipt.Artifacts = append(receipt.Artifacts, uiEvidenceSmokeArtifact{
				File: fileName, MIME: "image/png", SHA256: screenshot.SHA256,
				Bytes: len(screenshot.PNG), Width: configuration.Width,
				Height: configuration.Height, Matrix: item.name, Environment: item.environment,
			})
		})
	}

	// This route differs only in real page behavior: its button lacks the event
	// handler. Source/build checks cannot make the post-click selector appear.
	configureUIEvidenceSmoke(t, client, matrix[0].environment)
	navigateUIEvidenceSmoke(t, client, origin+"/regression")
	clickUIEvidenceSmoke(t, client, "#repair")
	receipt.RegressionCaught = !uiEvidenceSmokeSelectorEventually(
		t.Context(), client, `body[data-state="fixed"]`, true, 750*time.Millisecond)
	if !receipt.RegressionCaught {
		t.Fatal("real page assertion did not catch the deliberate interaction regression")
	}
	screenshot, err := client.captureScreenshot(t.Context(), "ci-smoke")
	if err != nil {
		t.Fatal(err)
	}
	configuration, err := png.DecodeConfig(bytes.NewReader(screenshot.PNG))
	if err != nil {
		t.Fatal(err)
	}
	writeUIEvidenceSmokeArtifact(t, artifactDirectory, "regression-detected.png",
		screenshot.PNG)
	receipt.Artifacts = append(receipt.Artifacts, uiEvidenceSmokeArtifact{
		File: "regression-detected.png", MIME: "image/png", SHA256: screenshot.SHA256,
		Bytes: len(screenshot.PNG), Width: configuration.Width,
		Height: configuration.Height, Matrix: "regression-desktop",
		Environment: matrix[0].environment,
	})

	diagnosticContext, diagnosticCancel := context.WithTimeout(t.Context(), 5*time.Second)
	if err := client.drainUIEvidenceEventsUntilIdle(diagnosticContext); err != nil {
		diagnosticCancel()
		t.Fatalf("settle real Edge diagnostics: %v", err)
	}
	diagnosticCancel()
	diagnostics := client.diagnostics()
	if diagnostics.Summary.ConsoleErrors != 0 || diagnostics.Summary.PageErrors != 0 ||
		diagnostics.Summary.FailedRequests != 0 || diagnostics.Summary.HTTPFailures != 0 ||
		diagnostics.Summary.BlockedRequests != 0 {
		t.Fatalf("real Edge diagnostics are not clean: %+v", diagnostics.Summary)
	}
	for _, request := range diagnostics.Network {
		if request.URL == "" {
			continue
		}
		parsed, parseErr := url.Parse(request.URL)
		if parseErr != nil || parsed.Scheme+"://"+parsed.Host != origin {
			t.Fatalf("real Edge request escaped the exact loopback origin: %q", request.URL)
		}
	}
	closeContext, closeCancel := context.WithTimeout(context.Background(), 2*time.Second)
	if err := client.close(closeContext); err != nil {
		closeCancel()
		t.Fatalf("close real Edge restricted CDP: %v", err)
	}
	clientClosed = true
	closeCancel()
	process.Stop(t)
	receipt.BrowserTreeReaped = process.reaped
	receipt.BrowserPortFreed = uiEvidenceSmokePortReleased(t, endpoint)
	if err := removeProfileTreeBounded(profilePath, profileCleanupRetryTimeout,
		os.RemoveAll); err != nil {
		t.Fatalf("remove dedicated UI evidence profile: %v", err)
	}
	if _, err := os.Lstat(profilePath); !os.IsNotExist(err) {
		t.Fatalf("dedicated UI evidence profile remained after cleanup: %v", err)
	}
	receipt.ProfileRemoved = true
	closeFixture()
	receipt.FixtureReaped = true
	if !receipt.BrowserTreeReaped || !receipt.BrowserPortFreed {
		t.Fatal("real Edge cleanup receipt is incomplete")
	}
	writeUIEvidenceSmokeReceipt(t, artifactDirectory, receipt)
	t.Logf("real Edge UI evidence: commit=%s version=%s executable_sha256=%s artifacts=%d regression_caught=%t",
		commit, identity.Version, identity.ExecutableSHA256, len(receipt.Artifacts),
		receipt.RegressionCaught)
}

func installedStableEdge(t *testing.T) BrowserExecutableIdentity {
	t.Helper()
	identities, err := DiscoverInstalledBrowsers()
	if err != nil {
		t.Fatal(err)
	}
	for _, identity := range identities {
		if identity.Product == BrowserProductEdge && identity.Channel == BrowserChannelStable &&
			identity.VersionVerified && identity.Version != "" {
			return identity
		}
	}
	t.Fatal("fixed-location stable Edge with a verified version is required")
	return BrowserExecutableIdentity{}
}

func uiEvidenceSmokeSourceBinding(t *testing.T) (string, string, bool) {
	t.Helper()
	rootCommand := exec.Command("git", "rev-parse", "--show-toplevel")
	rootCommand.Stderr = os.Stderr
	rootRaw, err := rootCommand.Output()
	if err != nil {
		t.Fatalf("resolve UI evidence source root: %v", err)
	}
	root := strings.TrimSpace(string(rootRaw))
	commitCommand := exec.Command("git", "-C", root, "rev-parse", "HEAD")
	commitCommand.Stderr = os.Stderr
	commitRaw, err := commitCommand.Output()
	if err != nil {
		t.Fatalf("resolve UI evidence source commit: %v", err)
	}
	commit := strings.ToLower(strings.TrimSpace(string(commitRaw)))
	decoded, err := hex.DecodeString(commit)
	if err != nil || len(decoded) != 20 {
		t.Fatalf("invalid UI evidence source commit %q", commit)
	}
	statusCommand := exec.Command("git", "-C", root, "status", "--porcelain=v1",
		"--untracked-files=all")
	statusCommand.Stderr = os.Stderr
	statusRaw, err := statusCommand.Output()
	if err != nil {
		t.Fatalf("inspect UI evidence source status: %v", err)
	}
	clean := len(bytes.TrimSpace(statusRaw)) == 0
	if os.Getenv("GITHUB_ACTIONS") == "true" && !clean {
		t.Fatalf("real UI evidence CI must run from a clean fixed commit: %s", statusRaw)
	}
	digest := sha256.Sum256(statusRaw)
	return commit, hex.EncodeToString(digest[:]), clean
}

func startUIEvidenceSmokeFixture(t *testing.T) (string, func()) {
	t.Helper()
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/fixed", func(response http.ResponseWriter, _ *http.Request) {
		response.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = io.WriteString(response, uiEvidenceSmokeHTML(true))
	})
	mux.HandleFunc("/regression", func(response http.ResponseWriter, _ *http.Request) {
		response.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = io.WriteString(response, uiEvidenceSmokeHTML(false))
	})
	server := &http.Server{Handler: mux, ReadHeaderTimeout: time.Second}
	serveDone := make(chan struct{})
	go func() {
		_ = server.Serve(listener)
		close(serveDone)
	}()
	origin := "http://" + listener.Addr().String()
	return origin, func() {
		shutdownContext, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = server.Shutdown(shutdownContext)
		<-serveDone
	}
}

func uiEvidenceSmokeHTML(fixed bool) string {
	handler := ""
	if fixed {
		handler = `repair.addEventListener("click",()=>document.body.dataset.state="fixed");`
	}
	return `<!doctype html><html lang="en"><head><meta charset="utf-8">
<meta name="viewport" content="width=device-width,initial-scale=1">
<link rel="icon" href="data:,"><title>UI evidence fixture</title>
<style>body{font:16px system-ui;margin:24px;background:#fff;color:#111}
@media(prefers-color-scheme:dark){body{background:#111;color:#eee}}
button,input{appearance:none;font:inherit;padding:10px;margin:6px;color:#111;background:#eee;
border:2px solid #777;border-radius:2px;outline:none;box-shadow:none;transition:none}
button:hover,button:focus,input:focus{color:#111;background:#eee;border-color:#777;
outline:none;box-shadow:none}input{caret-color:transparent}</style></head>
<body data-state="initial"><main><h1>Runtime evidence</h1>
<button id="repair" type="button">Apply fix</button>
<label>Fixture <input id="fixture-input" autocomplete="off"></label>
<div id="dynamic-mask">synthetic changing content</div></main><script>
document.body.dataset.theme=matchMedia("(prefers-color-scheme: dark)").matches?"dark":"light";
document.body.dataset.motion=matchMedia("(prefers-reduced-motion: reduce)").matches?"reduced":"full";
document.body.dataset.locale=new Intl.DateTimeFormat().resolvedOptions().locale;
const repair=document.querySelector("#repair");` + handler + `
document.querySelector("#fixture-input").addEventListener("input",event=>{
 if(event.target.value==="fixture input") document.body.dataset.typed="true";
});
console.info("ui-evidence-ready");</script></body></html>`
}

type uiEvidenceSmokeEdgeProcess struct {
	platform browserPlatformProcess
	stopped  bool
	reaped   bool
}

func startUIEvidenceSmokeEdge(t *testing.T, identity BrowserExecutableIdentity,
	profilePath string,
) *uiEvidenceSmokeEdgeProcess {
	t.Helper()
	for _, name := range profileEnvironmentDirectoryNames {
		path := filepath.Join(profilePath, name)
		if !pathWithinRoot(profilePath, path) {
			t.Fatal("smoke browser environment directory escaped the temporary profile")
		}
		if err := os.Mkdir(path, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if err := validateBrowserEnvironmentDirectories(profilePath); err != nil {
		t.Fatal(err)
	}
	arguments := fixedRestrictedBrowserArguments(profilePath)
	now := time.Now().UTC()
	spec := BrowserStartSpec{
		ProtocolVersion:               BrowserStartSpecProtocolVersion,
		ExecutableIdentityFingerprint: identity.Fingerprint,
		ExecutablePath:                identity.CanonicalPath, ExecutableSHA256: identity.ExecutableSHA256,
		ProfilePath: profilePath, Arguments: arguments, InitialURL: "about:blank",
		RemoteDebuggingAddress: "127.0.0.1", RemoteDebuggingPort: 0,
		ActiveProcessLimit: MaxBrowserProcessCount, JobMemoryLimitBytes: MaxBrowserJobMemoryBytes,
		LoopbackNavigationRequired: true, HostNameResolutionDisabled: true,
		NetworkDefaultDeny: true, CreatedAt: now, RuntimeDeadline: now.Add(2 * time.Minute),
	}
	spec.Fingerprint = browserRuntimeFingerprint(spec)
	platform, err := (windowsBrowserProcessStarter{}).Start(t.Context(), spec)
	if err != nil {
		t.Fatalf("start fixed-location Edge through the production process adapter: %v", err)
	}
	return &uiEvidenceSmokeEdgeProcess{platform: platform}
}

func (process *uiEvidenceSmokeEdgeProcess) Active() bool {
	if process == nil || process.platform == nil || process.stopped {
		return false
	}
	select {
	case <-process.platform.Done():
		return false
	default:
		return true
	}
}

func (process *uiEvidenceSmokeEdgeProcess) Stop(t *testing.T) {
	t.Helper()
	if process == nil || process.platform == nil || process.stopped {
		return
	}
	process.stopped = true
	stopContext, stopCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer stopCancel()
	if err := process.platform.Stop(stopContext, false); err != nil {
		t.Errorf("stop exact UI evidence smoke Job Object: %v", err)
	}
	exit, ok := process.platform.Exit()
	process.reaped = ok && exit.TreeReaped
	if !ok || !process.reaped {
		t.Errorf("real Edge process tree did not exit after Job Object cleanup")
	}
}

func uiEvidenceSmokePortReleased(t *testing.T, endpoint *url.URL) bool {
	t.Helper()
	if endpoint == nil || endpoint.Port() == "" {
		t.Fatal("real Edge DevTools endpoint did not contain a port")
	}
	deadline := time.NewTimer(5 * time.Second)
	defer deadline.Stop()
	ticker := time.NewTicker(25 * time.Millisecond)
	defer ticker.Stop()
	address := net.JoinHostPort("127.0.0.1", endpoint.Port())
	for {
		connection, err := net.DialTimeout("tcp4", address, 100*time.Millisecond)
		if err != nil {
			return true
		}
		_ = connection.Close()
		select {
		case <-deadline.C:
			t.Fatalf("real Edge DevTools listener remained reachable after cleanup")
		case <-ticker.C:
		}
	}
}

func waitForUIEvidenceSmokeEndpoint(t *testing.T, profilePath string,
	process *uiEvidenceSmokeEdgeProcess, artifactDirectory string,
) *url.URL {
	t.Helper()
	deadline := time.NewTimer(45 * time.Second)
	defer deadline.Stop()
	ticker := time.NewTicker(25 * time.Millisecond)
	defer ticker.Stop()
	for {
		endpoint, pending, err := readDevToolsEndpoint(profilePath)
		if err != nil {
			t.Fatal(err)
		}
		if !pending {
			return endpoint
		}
		if !process.Active() {
			exit, _ := process.platform.Exit()
			t.Fatalf("real Edge Job Object exited before DevTools was ready: %+v", exit)
		}
		select {
		case <-deadline.C:
			diagnostic := collectUIEvidenceSmokeStartupDiagnostic(profilePath, process)
			raw, marshalErr := json.MarshalIndent(diagnostic, "", "  ")
			if marshalErr != nil {
				t.Fatalf("marshal real Edge startup diagnostic: %v", marshalErr)
			}
			writeUIEvidenceSmokeArtifact(t, artifactDirectory,
				"startup-failure.json", append(raw, '\n'))
			t.Fatal("timed out waiting for real Edge DevTools endpoint")
		case <-ticker.C:
		}
	}
}

func collectUIEvidenceSmokeStartupDiagnostic(profilePath string,
	process *uiEvidenceSmokeEdgeProcess,
) uiEvidenceSmokeStartupDiagnostic {
	diagnostic := uiEvidenceSmokeStartupDiagnostic{
		ProtocolVersion: "ui-evidence-ci-startup-diagnostic.v1",
		Reason:          "devtools_endpoint_timeout", ProcessAdapter: WindowsBrowserProcessAdapterName,
		RemoteDebuggingPolicies: readUIEvidenceSmokeRemoteDebuggingPolicies(),
		CapturedAt:              time.Now().UTC(),
	}
	if process != nil {
		diagnostic.PID = process.platform.PID()
		if platform, ok := process.platform.(*windowsBrowserProcess); ok {
			platform.mu.Lock()
			if platform.process != 0 {
				var exitCode uint32
				if err := windows.GetExitCodeProcess(platform.process, &exitCode); err != nil {
					diagnostic.ProcessQueryError = err.Error()
				} else {
					diagnostic.MainProcessExitCode = exitCode
					diagnostic.MainProcessActive = exitCode == 259
				}
			}
			if platform.job != 0 {
				accounting := struct {
					TotalUserTime             int64
					TotalKernelTime           int64
					ThisPeriodTotalUserTime   int64
					ThisPeriodTotalKernelTime int64
					TotalPageFaultCount       uint32
					TotalProcesses            uint32
					ActiveProcesses           uint32
					TotalTerminatedProcesses  uint32
				}{}
				if err := windows.QueryInformationJobObject(platform.job,
					windows.JobObjectBasicAccountingInformation,
					uintptr(unsafe.Pointer(&accounting)),
					uint32(unsafe.Sizeof(accounting)), nil); err != nil {
					diagnostic.JobQueryError = err.Error()
				} else {
					diagnostic.JobTotalProcesses = accounting.TotalProcesses
					diagnostic.JobActiveProcesses = accounting.ActiveProcesses
					diagnostic.JobTerminatedProcesses = accounting.TotalTerminatedProcesses
				}
			}
			platform.mu.Unlock()
		}
	}
	diagnostic.ProfileEntries, diagnostic.ProfileReadError =
		readUIEvidenceSmokeProfileEntries(profilePath)
	return diagnostic
}

func readUIEvidenceSmokeProfileEntries(profilePath string) ([]string, string) {
	entries, err := os.ReadDir(profilePath)
	if err != nil {
		return nil, err.Error()
	}
	result := make([]string, 0, 64)
	appendEntry := func(prefix string, entry os.DirEntry) {
		if len(result) >= 64 {
			return
		}
		kind := "file"
		if entry.IsDir() {
			kind = "directory"
		}
		size := int64(0)
		if info, infoErr := entry.Info(); infoErr == nil && !entry.IsDir() {
			size = info.Size()
		}
		result = append(result, fmt.Sprintf("%s:%s%s:%d", kind, prefix,
			entry.Name(), size))
	}
	for _, entry := range entries {
		appendEntry("", entry)
		if !entry.IsDir() || len(result) >= 64 {
			continue
		}
		children, childErr := os.ReadDir(filepath.Join(profilePath, entry.Name()))
		if childErr != nil {
			result = append(result, fmt.Sprintf("error:%s:%v", entry.Name(), childErr))
			continue
		}
		for _, child := range children {
			appendEntry(entry.Name()+"/", child)
		}
	}
	return result, ""
}

func readUIEvidenceSmokeRemoteDebuggingPolicies() map[string]string {
	result := map[string]string{}
	for _, item := range []struct {
		name string
		root registry.Key
		view uint32
	}{
		{name: "hklm_64", root: registry.LOCAL_MACHINE, view: registry.WOW64_64KEY},
		{name: "hklm_32", root: registry.LOCAL_MACHINE, view: registry.WOW64_32KEY},
		{name: "hkcu_64", root: registry.CURRENT_USER, view: registry.WOW64_64KEY},
		{name: "hkcu_32", root: registry.CURRENT_USER, view: registry.WOW64_32KEY},
	} {
		key, err := registry.OpenKey(item.root, `SOFTWARE\Policies\Microsoft\Edge`,
			registry.QUERY_VALUE|item.view)
		if err != nil {
			if errors.Is(err, windows.ERROR_FILE_NOT_FOUND) {
				result[item.name] = "unset"
			} else {
				result[item.name] = "unreadable:" + err.Error()
			}
			continue
		}
		value, _, valueErr := key.GetIntegerValue("RemoteDebuggingAllowed")
		_ = key.Close()
		if errors.Is(valueErr, windows.ERROR_FILE_NOT_FOUND) {
			result[item.name] = "unset"
		} else if valueErr != nil {
			result[item.name] = "unreadable:" + valueErr.Error()
		} else {
			result[item.name] = fmt.Sprintf("%d", value)
		}
	}
	return result
}

func configureUIEvidenceSmoke(t *testing.T, client *restrictedCDPClient,
	environment uievidence.Environment,
) {
	t.Helper()
	if err := environment.Validate(); err != nil {
		t.Fatal(err)
	}
	if err := client.call(t.Context(), client.sessionID,
		"Emulation.setDeviceMetricsOverride", map[string]any{
			"width": environment.Viewport.Width, "height": environment.Viewport.Height,
			"deviceScaleFactor": environment.Viewport.DPR, "mobile": false,
			"screenWidth":  environment.Viewport.Width,
			"screenHeight": environment.Viewport.Height,
		}, &struct{}{}); err != nil {
		t.Fatal(err)
	}
	if err := client.call(t.Context(), client.sessionID, "Emulation.setLocaleOverride",
		map[string]any{"locale": environment.Locale}, &struct{}{}); err != nil {
		t.Fatal(err)
	}
	motion := "no-preference"
	if environment.ReducedMotion {
		motion = "reduce"
	}
	features := []map[string]string{
		{"name": "prefers-color-scheme", "value": string(environment.Theme)},
		{"name": "prefers-reduced-motion", "value": motion},
	}
	if err := client.call(t.Context(), client.sessionID, "Emulation.setEmulatedMedia",
		map[string]any{"media": "screen", "features": features}, &struct{}{}); err != nil {
		t.Fatal(err)
	}
}

func navigateUIEvidenceSmoke(t *testing.T, client *restrictedCDPClient, target string) {
	t.Helper()
	decision := client.scope.AuthorizeNavigation(target)
	if !decision.Allowed {
		t.Fatalf("smoke target is outside exact scope: %s", target)
	}
	client.pageLoaded = false
	client.blockedDocument = false
	var navigation struct {
		FrameID   string `json:"frameId"`
		ErrorText string `json:"errorText"`
	}
	if err := client.call(t.Context(), client.sessionID, "Page.navigate",
		map[string]any{"url": decision.CanonicalURL}, &navigation); err != nil {
		t.Fatal(err)
	}
	if navigation.ErrorText != "" || client.blockedDocument {
		t.Fatalf("real Edge navigation was blocked: %s", navigation.ErrorText)
	}
	if err := client.waitForPageLoad(t.Context()); err != nil {
		t.Fatal(err)
	}
	finalURL, _, _, err := client.documentIdentity(t.Context())
	if err != nil || finalURL != decision.CanonicalURL {
		t.Fatalf("real Edge final URL = %q, want %q, err=%v", finalURL,
			decision.CanonicalURL, err)
	}
}

func clickUIEvidenceSmoke(t *testing.T, client *restrictedCDPClient, selector string) {
	t.Helper()
	nodeID, found, err := client.querySelector(t.Context(), selector)
	if err != nil || !found {
		t.Fatalf("real Edge click selector %q: found=%t err=%v", selector, found, err)
	}
	if err := client.call(t.Context(), client.sessionID, "DOM.scrollIntoViewIfNeeded",
		map[string]any{"nodeId": nodeID}, &struct{}{}); err != nil {
		t.Fatal(err)
	}
	rectangle, err := client.nodeRectangle(t.Context(), nodeID)
	if err != nil {
		t.Fatal(err)
	}
	x := float64(rectangle.Min.X+rectangle.Max.X) / 2
	y := float64(rectangle.Min.Y+rectangle.Max.Y) / 2
	for _, eventType := range []string{"mousePressed", "mouseReleased"} {
		if err := client.call(t.Context(), client.sessionID, "Input.dispatchMouseEvent",
			map[string]any{"type": eventType, "x": x, "y": y, "button": "left",
				"clickCount": 1}, &struct{}{}); err != nil {
			t.Fatal(err)
		}
	}
}

func typeUIEvidenceSmoke(t *testing.T, client *restrictedCDPClient,
	selector, value string,
) {
	t.Helper()
	nodeID, found, err := client.querySelector(t.Context(), selector)
	if err != nil || !found {
		t.Fatalf("real Edge type selector %q: found=%t err=%v", selector, found, err)
	}
	if err := client.call(t.Context(), client.sessionID, "DOM.focus",
		map[string]any{"nodeId": nodeID}, &struct{}{}); err != nil {
		t.Fatal(err)
	}
	if err := client.call(t.Context(), client.sessionID, "Input.insertText",
		map[string]any{"text": value}, &struct{}{}); err != nil {
		t.Fatal(err)
	}
}

func assertUIEvidenceSmokeSelector(t *testing.T, client *restrictedCDPClient,
	selector string, expected bool,
) {
	t.Helper()
	_, found, err := client.querySelector(t.Context(), selector)
	if err != nil || found != expected {
		t.Fatalf("real Edge selector %q: found=%t want=%t err=%v", selector, found,
			expected, err)
	}
}

func waitForUIEvidenceSmokeSelector(t *testing.T, client *restrictedCDPClient,
	selector string, expected bool,
) {
	t.Helper()
	if !uiEvidenceSmokeSelectorEventually(t.Context(), client, selector, expected,
		2*time.Second) {
		t.Fatalf("real Edge selector %q did not reach present=%t", selector, expected)
	}
}

func uiEvidenceSmokeSelectorEventually(ctx context.Context, client *restrictedCDPClient,
	selector string, expected bool, timeout time.Duration,
) bool {
	deadline := time.Now().Add(timeout)
	for {
		_, found, err := client.querySelector(ctx, selector)
		if err == nil && found == expected {
			return true
		}
		if time.Now().After(deadline) {
			return false
		}
		time.Sleep(25 * time.Millisecond)
	}
}

func verifyUIEvidenceSmokeTextCaptures(t *testing.T, client *restrictedCDPClient) {
	t.Helper()
	rootID, err := client.documentNodeID(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	var document struct {
		OuterHTML string `json:"outerHTML"`
	}
	if err := client.call(t.Context(), client.sessionID, "DOM.getOuterHTML",
		map[string]any{"nodeId": rootID}, &document); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(document.OuterHTML, `data-state="fixed"`) {
		t.Fatal("real Edge DOM capture missed the post-interaction state")
	}
	var accessibility struct {
		Nodes []json.RawMessage `json:"nodes"`
	}
	if err := client.call(t.Context(), client.sessionID,
		"Accessibility.getFullAXTree", map[string]any{"depth": 64},
		&accessibility); err != nil || len(accessibility.Nodes) == 0 {
		t.Fatalf("real Edge accessibility capture is empty: %v", err)
	}
	var performance struct {
		Metrics []json.RawMessage `json:"metrics"`
	}
	if err := client.call(t.Context(), client.sessionID, "Performance.getMetrics",
		map[string]any{}, &performance); err != nil || len(performance.Metrics) == 0 {
		t.Fatalf("real Edge performance capture is empty: %v", err)
	}
}

func uiEvidenceSmokeArtifactDirectory(t *testing.T) string {
	t.Helper()
	directory := strings.TrimSpace(os.Getenv("CYBERAGENT_UI_EVIDENCE_ARTIFACT_DIR"))
	if directory == "" {
		return t.TempDir()
	}
	if !filepath.IsAbs(directory) || filepath.Clean(directory) != directory {
		t.Fatalf("UI evidence artifact directory must be an absolute clean path")
	}
	if err := os.MkdirAll(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	return directory
}

func writeUIEvidenceSmokeArtifact(t *testing.T, directory, name string, content []byte) {
	t.Helper()
	path := filepath.Join(directory, name)
	if !pathWithinRoot(directory, path) {
		t.Fatal("UI evidence smoke artifact escaped its exact directory")
	}
	if err := os.WriteFile(path, content, 0o600); err != nil {
		t.Fatal(err)
	}
}

func writeUIEvidenceSmokeReceipt(t *testing.T, directory string,
	receipt uiEvidenceSmokeReceipt,
) {
	t.Helper()
	raw, err := json.MarshalIndent(receipt, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	raw = append(raw, '\n')
	writeUIEvidenceSmokeArtifact(t, directory, "receipt.json", raw)
	digest := sha256.Sum256(raw)
	t.Logf("UI evidence smoke receipt sha256=%s directory=%s",
		hex.EncodeToString(digest[:]), directory)
}

func writeUIEvidenceSmokeLaunchReceipt(t *testing.T, directory string,
	receipt uiEvidenceSmokeLaunchReceipt,
) {
	t.Helper()
	raw, err := json.MarshalIndent(receipt, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	raw = append(raw, '\n')
	writeUIEvidenceSmokeArtifact(t, directory, "launch.json", raw)
}
