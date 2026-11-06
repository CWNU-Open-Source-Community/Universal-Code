//go:build desktop

package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	goruntime "runtime"
	"strings"

	"cyberagent-workbench/internal/app"
	"cyberagent-workbench/internal/apperror"
	"cyberagent-workbench/internal/application"
	"cyberagent-workbench/internal/browserruntime"
	"cyberagent-workbench/internal/computeruse"
	"cyberagent-workbench/internal/desktop"
	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/httpapi"
	"cyberagent-workbench/internal/packagede2e"
	"cyberagent-workbench/internal/sandbox"
	"cyberagent-workbench/internal/sbxmcp"
	"cyberagent-workbench/internal/webui"
	webassets "cyberagent-workbench/web"

	"github.com/wailsapp/wails/v2"
	wailsassetserver "github.com/wailsapp/wails/v2/pkg/assetserver"
	"github.com/wailsapp/wails/v2/pkg/options"
	"github.com/wailsapp/wails/v2/pkg/options/assetserver"
	"github.com/wailsapp/wails/v2/pkg/runtime"
)

const (
	desktopSingleInstanceID            = "e3305a58-3d1e-4e2f-b4ca-d1032a737b96"
	defaultDesktopWidth                = 1440
	defaultDesktopHeight               = 900
	standardCodeDockerImageEnvironment = "CYBERAGENT_STANDARD_CODE_DOCKER_IMAGE_DIGEST"
	webSearchEndpointEnvironment       = "CYBERAGENT_WEB_SEARCH_ENDPOINT"
)

type desktopOptions struct {
	operatorPreview           bool
	safeView                  bool
	profileControl            bool
	permissionControl         bool
	workspaceSandbox          bool
	dangerFullAccess          bool
	browserCDPControl         bool
	fullCDPDebug              bool
	runCreation               bool
	sessionMessages           bool
	sessionSteeringControl    bool
	runLifecycle              bool
	runExecution              bool
	planDeliveryControl       bool
	approvalControl           bool
	modelControl              bool
	providerCredentials       bool
	fileEditReview            bool
	fileEditProposals         bool
	runWakeControl            bool
	fileEditApply             bool
	runWakeExecution          bool
	runWakeWorker             bool
	scheduledJobControl       bool
	scheduledJobWorker        bool
	scheduledJobConfirmedOnly bool
	skillInstallation         bool
	evidenceAttachment        bool
	verificationEvidence      bool
	embeddedAnalyzer          bool
	batchDeliveryControl      bool
	batchValidation           bool
	uiEvidence                bool
	userTerminal              bool
	dockerExecution           bool
	codeIntelConfig           string
	gitAdvanced               bool
	githubReview              bool
	gitWorktreeRoot           string
	securityMatrix            bool
	securityMatrixRoot        string
	securityCandidate         string
	securityRecoveryWorker    bool
	securityRecoveryRoot      string
	securityRecoveryCase      string
	securityRecoveryBackend   string
	securityRecoveryPhase     string
	riskProfileRestart        bool
	sandboxSettings           bool
	version                   bool
}

type nativeSkillPackagePicker struct{}

type nativeWorkspaceDirectoryPicker struct{}

type wailsWindowRestorer struct{}

func (wailsWindowRestorer) Unminimise(ctx context.Context) { runtime.WindowUnminimise(ctx) }
func (wailsWindowRestorer) Show(ctx context.Context)       { runtime.WindowShow(ctx) }

func secondInstanceHandler(lifecycle *desktop.Lifecycle) func(options.SecondInstanceData) {
	return func(_ options.SecondInstanceData) {
		lifecycle.RequestRestore()
	}
}

func shouldMaximiseDesktopWindow(screens []runtime.Screen) bool {
	var selected *runtime.Screen
	for index := range screens {
		if screens[index].IsCurrent {
			selected = &screens[index]
			break
		}
		if selected == nil && screens[index].IsPrimary {
			selected = &screens[index]
		}
	}
	return selected != nil && (selected.Size.Width < defaultDesktopWidth ||
		selected.Size.Height < defaultDesktopHeight)
}

func (nativeSkillPackagePicker) OpenSkillPackage(ctx context.Context) (string, error) {
	if ctx == nil {
		return "", errors.New("desktop lifecycle is unavailable")
	}
	return runtime.OpenFileDialog(ctx, runtime.OpenDialogOptions{
		Title: "Select SKILL.md, plugin.json or a legacy Skill archive",
		Filters: []runtime.FileFilter{
			{DisplayName: "Skills and Agent Plugins", Pattern: "SKILL.md;plugin.json;*.zip"},
		},
		ShowHiddenFiles:      false,
		CanCreateDirectories: false,
		ResolvesAliases:      false,
	})
}

func (nativeWorkspaceDirectoryPicker) OpenWorkspaceDirectory(ctx context.Context) (string, error) {
	if ctx == nil {
		return "", errors.New("desktop lifecycle is unavailable")
	}
	return runtime.OpenDirectoryDialog(ctx, runtime.OpenDialogOptions{
		Title:                "Select Universal Code workspace folder",
		ShowHiddenFiles:      true,
		CanCreateDirectories: false,
		ResolvesAliases:      true,
	})
}

type inProcessAPIHandler struct {
	next http.Handler
}

func (h inProcessAPIHandler) ServeHTTP(writer http.ResponseWriter, request *http.Request) {
	if h.next == nil || request == nil || request.URL == nil {
		http.Error(writer, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
		return
	}
	if !trustedDesktopRendererOrigin(request) {
		http.Error(writer, http.StatusText(http.StatusForbidden), http.StatusForbidden)
		return
	}
	trusted := request.Clone(request.Context())
	trusted.Host = "127.0.0.1"
	trusted.RemoteAddr = "127.0.0.1:0"
	trusted.URL.Scheme = ""
	trusted.URL.Host = ""
	if trusted.URL != nil && trusted.URL.Path == "" {
		trusted.URL.Path = "/"
	}
	trusted.RequestURI = trusted.URL.RequestURI()
	if trusted.RequestURI == "" {
		trusted.RequestURI = "/"
	}
	if (trusted.Method == http.MethodGet || trusted.Method == http.MethodHead) &&
		trusted.ContentLength == -1 && len(trusted.TransferEncoding) == 0 && trusted.Body == http.NoBody &&
		trusted.Header.Get("Content-Length") == "" {
		trusted.ContentLength = 0
	}
	h.next.ServeHTTP(desktopAPIResponseWriter{ResponseWriter: writer}, trusted)
}

// The embedded WebView transport derives the reason phrase from http.StatusText.
// Cancellation's nonstandard 499 has no phrase and can leave native fetch pending.
// Keep the CANCELLED envelope and its durable failure receipt intact; only the
// desktop transport uses a standard error status. The standalone API keeps 499.
type desktopAPIResponseWriter struct {
	http.ResponseWriter
}

func (w desktopAPIResponseWriter) WriteHeader(status int) {
	if status == apperror.HTTPStatus(context.Canceled) {
		status = http.StatusConflict
	}
	w.ResponseWriter.WriteHeader(status)
}

func (w desktopAPIResponseWriter) Unwrap() http.ResponseWriter {
	return w.ResponseWriter
}

func trustedDesktopRendererOrigin(request *http.Request) bool {
	if request == nil || request.URL == nil || request.URL.User != nil || request.URL.Fragment != "" ||
		request.URL.RawFragment != "" || request.URL.Opaque != "" {
		return false
	}
	// The Wails AssetServer enforces the platform webview host itself before
	// the request reaches this handler; trustedDesktopRendererHost pins the
	// exact per-platform authority (wails.localhost on Windows, wails on
	// macOS where the custom wails:// scheme owns the renderer).
	if !strings.EqualFold(request.Host, trustedDesktopRendererHost()) ||
		!containsUserAgentToken(request.UserAgent(), wailsassetserver.WailsUserAgentValue) {
		return false
	}
	if request.URL.Scheme == "" || request.URL.Host == "" {
		// Wails converts the intercepted WebView request into server form before
		// invoking the custom AssetServer handler, leaving authority in Request.Host.
		return request.URL.Scheme == "" && request.URL.Host == ""
	}
	return strings.EqualFold(request.URL.Scheme, "http") &&
		strings.EqualFold(request.URL.Hostname(), "wails.localhost") && request.URL.Port() == ""
}

func containsUserAgentToken(value, expected string) bool {
	for _, token := range strings.Fields(value) {
		if strings.EqualFold(token, expected) {
			return true
		}
	}
	return false
}

func formatDesktopBindingError(err error) any {
	// Wails constructs Error(message.error), so an object loses its message.
	// Use the normalized public Message field without falling back to causes.
	var normalized *apperror.Error
	if errors.As(apperror.Normalize(err), &normalized) && normalized != nil && strings.TrimSpace(normalized.Message) != "" {
		return normalized.Message
	}
	return "Desktop operation failed"
}

func main() {
	if handled, code := computeruse.Execute(os.Args[1:], os.Stdin, os.Stdout); handled {
		os.Exit(code)
	}
	if handled, code := sbxmcp.Execute(os.Args[1:], os.Stdin, os.Stdout); handled {
		os.Exit(code)
	}
	restart, restartMode, restartErr := parseRiskRestartHelperOptions(os.Args[1:])
	if restartErr != nil {
		reportDesktopStartupFailure(restartErr)
		os.Exit(2)
	}
	if restartMode {
		if err := completeRiskRestartHelperHandshake(restart); err != nil {
			reportDesktopStartupFailure(err)
			os.Exit(1)
		}
		config, err := desktopOptionsForRiskProfile(restart.profile)
		if err != nil {
			reportDesktopStartupFailure(err)
			os.Exit(2)
		}
		if err := runDesktop(config); err != nil {
			reportDesktopStartupFailure(err)
			os.Exit(1)
		}
		return
	}
	config, err := parseDesktopOptions(os.Args[1:])
	if err != nil {
		reportDesktopStartupFailure(err)
		os.Exit(2)
	}
	if config.version {
		fmt.Fprintf(os.Stdout, "%s desktop %s\n", app.Name, app.Version)
		return
	}
	if config.securityRecoveryWorker {
		if workerErr := desktop.RunStandardCodeSecurityRecoveryWorker(
			context.Background(), desktop.StandardCodeSecurityRecoveryWorkerConfig{
				Root: config.securityRecoveryRoot, CaseID: config.securityRecoveryCase,
				Backend: config.securityRecoveryBackend, Phase: config.securityRecoveryPhase,
			}); workerErr != nil {
			fmt.Fprintf(os.Stderr, "packaged security recovery failed: %v\n", workerErr)
			os.Exit(1)
		}
		return
	}
	if config.securityMatrix {
		report, runErr := packagede2e.RunStandardCodeSecurityMatrix(
			context.Background(), packagede2e.SecurityMatrixOptions{
				HarnessRoot:      config.securityMatrixRoot,
				CandidateArchive: config.securityCandidate,
			}, desktop.NewStandardCodeSecurityDriver(strings.TrimSpace(
				os.Getenv(standardCodeDockerImageEnvironment))))
		if runErr != nil {
			fmt.Fprintf(os.Stderr, "packaged security matrix failed: %v\n", runErr)
			os.Exit(1)
		}
		fmt.Fprintf(os.Stdout, "%s status=%s report_sha256=%s runs=%d\n",
			report.ProtocolVersion, report.Status, report.ReportSHA256,
			report.Summary.RequiredBackendRuns)
		return
	}
	if err := runDesktop(config); err != nil {
		reportDesktopStartupFailure(err)
		os.Exit(1)
	}
}

func parseDesktopOptions(args []string) (desktopOptions, error) {
	fs := flag.NewFlagSet("cyberagent-desktop", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	operatorPreview := fs.Bool("operator-preview", false,
		"compatibility mode for the legacy safe Desktop capability bundle")
	safeView := fs.Bool("safe-view", false,
		"launch the explicit read-only Desktop without control capabilities")
	profileControl := fs.Bool("enable-profile-control", false,
		"enable only the non-authorizing Run execution-profile control")
	permissionControl := fs.Bool("enable-permission-control", false,
		"enable operator selection of execution permission modes")
	workspaceSandbox := fs.Bool("enable-workspace-sandbox", false,
		"probe and enable the verified Windows Workspace Sandbox")
	dangerFullAccess := fs.Bool("enable-danger-full-access", false,
		"enable unsandboxed one-shot host execution permission selection")
	browserCDPControl := fs.Bool("enable-browser-cdp-control", false,
		"enable browser CDP permission selection")
	fullCDPDebug := fs.Bool("enable-full-cdp-debug", false,
		"enable highly sensitive complete CDP debugging selection")
	runCreation := fs.Bool("enable-run-creation", false,
		"enable idempotent workspace-bound Run creation")
	sessionMessages := fs.Bool("enable-session-messages", false,
		"enable idempotent Run-bound Session message submission")
	sessionSteeringControl := fs.Bool("enable-session-steering-control", false,
		"enable pending-only Run-bound Session steering cancellation")
	runLifecycle := fs.Bool("enable-run-lifecycle", false,
		"enable idempotent Run start, pause, and resume control")
	runExecution := fs.Bool("enable-run-execution", false,
		"enable bounded queued Run execution through the Go Supervisor")
	planDeliveryControl := fs.Bool("enable-plan-delivery", false,
		"enable operator Plan direction selection and explicit Deliver transition")
	approvalControl := fs.Bool("enable-approvals", false,
		"enable bounded approve-once and deny decisions for durable approvals")
	modelControl := fs.Bool("enable-model-control", false,
		"enable persisted model route selection and explicit connectivity diagnostics")
	providerCredentials := fs.Bool("enable-provider-credentials", false,
		"enable OS-owned Provider credential changes")
	fileEditReview := fs.Bool("enable-file-edit-review", false,
		"enable review-only file edit approval or denial without applying files")
	fileEditProposals := fs.Bool("enable-file-edit-proposals", false,
		"enable Go-issued interactive FileEdit proposal sources")
	runWakeControl := fs.Bool("enable-run-wake", false,
		"enable durable bounded Run wake intent scheduling and cancellation")
	fileEditApply := fs.Bool("enable-file-edit-apply", false,
		"enable independently authorized approved FileEdit application")
	runWakeExecution := fs.Bool("enable-run-wake-execution", false,
		"enable explicitly launched foreground wake execution")
	runWakeWorker := fs.Bool("enable-wake-worker", false,
		"enable the bounded single-owner Run wake worker")
	scheduledJobControl := fs.Bool("enable-scheduled-jobs", false,
		"enable durable scheduled job creation and lifecycle control")
	scheduledJobWorker := fs.Bool("enable-scheduled-job-worker", false,
		"enable the process-local single-concurrency scheduled job worker")
	skillInstallation := fs.Bool("enable-skill-installation", false,
		"enable confirmed inert Skill package installation")
	evidenceAttachment := fs.Bool("enable-evidence-attachments", false,
		"enable idempotent non-authorizing Workspace evidence attachment")
	verificationEvidence := fs.Bool("enable-verification-evidence", false,
		"enable immutable operator verification evidence recording")
	embeddedAnalyzer := fs.Bool("enable-embedded-analyzer", false,
		"enable the fixed bounded embedded Rust/WASI analyzer")
	batchDeliveryControl := fs.Bool("enable-batch-delivery-control", false,
		"enable confirmed batch delivery preparation, review, merge, cancellation, and recovery")
	batchValidation := fs.Bool("enable-batch-validation-execution", false,
		"enable fixed offline go/npm checks for confirmed batch deliveries")
	uiEvidence := fs.Bool("enable-ui-evidence", false,
		"enable source-bound real-browser UI evidence for explicitly authorized Runs")
	userTerminal := fs.Bool("enable-user-terminal", false,
		"enable the user-owned Debug ConPTY terminal")
	dockerExecution := fs.Bool("enable-docker-execution", false,
		"enable product Docker Sandbox admission and execution on the fixed local daemon")
	codeIntelConfig := fs.String("code-intel-config", "",
		"absolute operator-reviewed code-intel config")
	gitAdvanced := fs.Bool("enable-git-advanced", false,
		"enable approval-gated git-advanced.v1 mutations")
	githubReview := fs.Bool("enable-github-review", false,
		"enable GitHub App review evidence and approval-gated write-back")
	gitWorktreeRoot := fs.String("git-worktree-root", "",
		"product-managed Git worktree root; defaults below CYBERAGENT_HOME")
	securityMatrix := fs.Bool("standard-code-attack-matrix", false,
		"execute only the fixed packaged Standard Code security matrix")
	securityMatrixRoot := fs.String("attack-matrix-root", "",
		"new harness-owned standard-code-attack-* evidence directory")
	securityCandidate := fs.String("candidate-archive", "",
		"portable candidate ZIP containing this exact executable")
	securityRecoveryWorker := fs.Bool("standard-code-attack-recovery-worker", false,
		"execute one fixed internal packaged recovery lifecycle")
	securityRecoveryRoot := fs.String("recovery-worker-root", "",
		"harness-owned fixed recovery directory")
	securityRecoveryCase := fs.String("recovery-worker-case", "",
		"frozen Standard Code recovery case")
	securityRecoveryBackend := fs.String("recovery-worker-backend", "",
		"frozen Local or Docker recovery backend")
	securityRecoveryPhase := fs.String("recovery-worker-phase", "",
		"fixed prepare or recover phase")
	version := fs.Bool("version", false, "print version and exit")
	if err := fs.Parse(args); err != nil {
		return desktopOptions{}, err
	}
	if fs.NArg() != 0 {
		return desktopOptions{}, errors.New("cyberagent-desktop accepts no positional arguments")
	}
	visited := make(map[string]struct{})
	fs.Visit(func(current *flag.Flag) { visited[current.Name] = struct{}{} })
	if *safeView && len(visited) != 1 {
		return desktopOptions{}, errors.New("--safe-view cannot be combined with other Desktop options")
	}
	_, matrixFlag := visited["standard-code-attack-matrix"]
	_, matrixRootFlag := visited["attack-matrix-root"]
	_, candidateFlag := visited["candidate-archive"]
	if matrixFlag || matrixRootFlag || candidateFlag {
		if !*securityMatrix || !matrixRootFlag || !candidateFlag || len(visited) != 3 ||
			strings.TrimSpace(*securityMatrixRoot) == "" ||
			strings.TrimSpace(*securityCandidate) == "" {
			return desktopOptions{}, errors.New(
				"packaged security mode requires exactly --standard-code-attack-matrix, --attack-matrix-root, and --candidate-archive")
		}
	}
	workerNames := []string{"standard-code-attack-recovery-worker",
		"recovery-worker-root", "recovery-worker-case", "recovery-worker-backend",
		"recovery-worker-phase"}
	workerFlags := 0
	for _, name := range workerNames {
		if _, ok := visited[name]; ok {
			workerFlags++
		}
	}
	if workerFlags != 0 {
		if workerFlags != len(workerNames) || len(visited) != len(workerNames) ||
			!*securityRecoveryWorker || strings.TrimSpace(*securityRecoveryRoot) == "" ||
			strings.TrimSpace(*securityRecoveryCase) == "" ||
			strings.TrimSpace(*securityRecoveryBackend) == "" ||
			strings.TrimSpace(*securityRecoveryPhase) == "" {
			return desktopOptions{}, errors.New(
				"packaged recovery worker requires its exact five fixed arguments")
		}
	}
	config := desktopOptions{operatorPreview: *operatorPreview, safeView: *safeView,
		profileControl: *profileControl, runCreation: *runCreation,
		permissionControl: *permissionControl, dangerFullAccess: *dangerFullAccess,
		workspaceSandbox:        *workspaceSandbox,
		browserCDPControl:       *browserCDPControl,
		fullCDPDebug:            *fullCDPDebug,
		sessionMessages:         *sessionMessages,
		sessionSteeringControl:  *sessionSteeringControl,
		runLifecycle:            *runLifecycle,
		runExecution:            *runExecution,
		planDeliveryControl:     *planDeliveryControl,
		approvalControl:         *approvalControl,
		modelControl:            *modelControl,
		providerCredentials:     *providerCredentials,
		fileEditReview:          *fileEditReview,
		fileEditProposals:       *fileEditProposals,
		runWakeControl:          *runWakeControl,
		fileEditApply:           *fileEditApply,
		runWakeExecution:        *runWakeExecution,
		runWakeWorker:           *runWakeWorker,
		scheduledJobControl:     *scheduledJobControl,
		scheduledJobWorker:      *scheduledJobWorker,
		skillInstallation:       *skillInstallation,
		evidenceAttachment:      *evidenceAttachment,
		verificationEvidence:    *verificationEvidence,
		embeddedAnalyzer:        *embeddedAnalyzer,
		batchDeliveryControl:    *batchDeliveryControl,
		batchValidation:         *batchValidation,
		uiEvidence:              *uiEvidence,
		userTerminal:            *userTerminal,
		dockerExecution:         *dockerExecution,
		codeIntelConfig:         strings.TrimSpace(*codeIntelConfig),
		gitAdvanced:             *gitAdvanced,
		githubReview:            *githubReview,
		gitWorktreeRoot:         strings.TrimSpace(*gitWorktreeRoot),
		securityMatrix:          *securityMatrix,
		securityMatrixRoot:      strings.TrimSpace(*securityMatrixRoot),
		securityCandidate:       strings.TrimSpace(*securityCandidate),
		securityRecoveryWorker:  *securityRecoveryWorker,
		securityRecoveryRoot:    strings.TrimSpace(*securityRecoveryRoot),
		securityRecoveryCase:    strings.TrimSpace(*securityRecoveryCase),
		securityRecoveryBackend: strings.TrimSpace(*securityRecoveryBackend),
		securityRecoveryPhase:   strings.TrimSpace(*securityRecoveryPhase),
		version:                 *version}
	// A normal product launch has no arguments. Keep every explicit --enable-*
	// invocation in the legacy granular mode so a single high-risk switch never
	// inherits prerequisite authority from the product defaults.
	if (len(visited) == 0 || config.operatorPreview) && !config.securityMatrix &&
		!config.securityRecoveryWorker {
		enableSafeDesktopProductBundle(&config)
	}
	if len(visited) == 0 {
		config.riskProfileRestart = true
		config.sandboxSettings = true
	}
	if config.operatorPreview {
		// Preserve the preview launcher's explicitly requested whole-job worker.
		config.scheduledJobWorker = true
		config.scheduledJobConfirmedOnly = false
	}
	capabilities := domain.ExecutionPermissionRuntimeCapabilities{
		OperatorApprovalEnabled: config.permissionControl,
		DangerFullAccessEnabled: config.dangerFullAccess,
	}
	if err := capabilities.Validate(); err != nil {
		return desktopOptions{}, err
	}

	if config.workspaceSandbox && !config.permissionControl {
		return desktopOptions{}, errors.New(
			"Workspace Sandbox requires --enable-permission-control")
	}
	if config.dockerExecution && !config.permissionControl {
		return desktopOptions{}, errors.New(
			"Docker execution requires --enable-permission-control")
	}
	if config.gitAdvanced && !config.permissionControl {
		return desktopOptions{}, errors.New(
			"Git advanced control requires --enable-permission-control")
	}
	if config.githubReview && !config.permissionControl {
		return desktopOptions{}, errors.New(
			"GitHub review control requires --enable-permission-control")
	}
	if config.gitWorktreeRoot != "" && !config.gitAdvanced && !config.githubReview {
		return desktopOptions{}, errors.New(
			"--git-worktree-root requires --enable-git-advanced or --enable-github-review")
	}
	if config.batchValidation && (!config.batchDeliveryControl || !config.permissionControl || !config.dangerFullAccess) {
		return desktopOptions{}, errors.New(
			"batch validation execution requires --enable-batch-delivery-control, --enable-permission-control, and --enable-danger-full-access")
	}
	if config.fullCDPDebug && (!config.browserCDPControl || !config.dangerFullAccess) {
		return desktopOptions{}, errors.New(
			"full CDP requires --enable-browser-cdp-control and --enable-danger-full-access")
	}
	if config.scheduledJobWorker && !config.scheduledJobControl {
		return desktopOptions{}, errors.New(
			"scheduled job worker requires --enable-scheduled-jobs")
	}
	if config.uiEvidence && (!config.runExecution || !config.dangerFullAccess || !config.browserCDPControl) {
		return desktopOptions{}, errors.New(
			"UI evidence requires --enable-run-execution, --enable-danger-full-access, and --enable-browser-cdp-control")
	}
	return config, nil
}

func enableSafeDesktopProductBundle(config *desktopOptions) {
	config.profileControl = true
	config.permissionControl = true
	config.workspaceSandbox = true
	config.browserCDPControl = true
	// Install high-risk adapters in the normal process without granting them.
	// The process-local authority below starts empty and is activated only by an
	// explicitly confirmed current Thread Full Access selection.
	config.dangerFullAccess = true
	config.fullCDPDebug = true
	config.runCreation = true
	config.sessionMessages = true
	config.sessionSteeringControl = true
	config.runLifecycle = true
	config.runExecution = true
	config.planDeliveryControl = true
	config.approvalControl = true
	config.modelControl = true
	config.providerCredentials = true
	config.fileEditReview = true
	config.fileEditProposals = true
	config.runWakeControl = true
	config.fileEditApply = true
	config.runWakeExecution = true
	config.scheduledJobControl = true
	config.scheduledJobWorker = true
	config.scheduledJobConfirmedOnly = true
	config.skillInstallation = true
	config.evidenceAttachment = true
	config.verificationEvidence = true
	config.embeddedAnalyzer = true
	config.batchDeliveryControl = true
	config.gitAdvanced = true
	config.githubReview = true
}

func desktopWorkspaceSandboxRuntimeAvailable(config desktopOptions,
	readiness *sandbox.LocalReadiness,
	dockerReadiness ...*sandbox.DockerReadiness,
) bool {
	var docker *sandbox.DockerReadiness
	if len(dockerReadiness) == 1 {
		docker = dockerReadiness[0]
	}
	return desktop.WorkspaceSandboxRuntimeAvailable(config.workspaceSandbox, config.dockerExecution, readiness, docker)
}

func runDesktop(config desktopOptions) error {
	if err := checkDesktopPrerequisites(); err != nil {
		return err
	}
	homePath := app.DefaultHome()
	config, verificationSettingsStore, err := loadDesktopVerificationPreferences(context.Background(), config, homePath)
	if err != nil {
		return err
	}
	var terminalSettingsStore application.DesktopTerminalSettingsStore
	if config.riskProfileRestart {
		terminalSettingsStore, err = application.NewFileDesktopTerminalSettingsStore(homePath)
		if err != nil {
			return err
		}
		if _, err = terminalSettingsStore.Load(context.Background()); err != nil {
			return err
		}
	}
	config, environmentSettings, err := loadDesktopSandboxSettings(context.Background(), config, homePath)
	if err != nil {
		return err
	}
	var sbxBackend *sandbox.SBXBackend
	var sbxReadiness *sandbox.SBXReadiness
	if environmentSettings != nil {
		sbxBackend, err = desktop.NewDesktopSBXBackend(homePath, *environmentSettings)
		if err != nil {
			return err
		}
		defer sbxBackend.Close()
	}
	var localReadiness *sandbox.LocalReadiness
	var localBackend sandbox.LocalBackend
	if config.workspaceSandbox {
		backend, err := sandbox.NewPlatformLocalBackend()
		if err != nil {
			return err
		}
		localBackend = backend
		defer localBackend.Close()
		readiness, err := backend.Readiness(context.Background(),
			sandbox.LocalRuntimeCapabilities{Enabled: true})
		if err != nil {
			return err
		}
		localReadiness = &readiness
	}
	imageDigest := strings.TrimSpace(os.Getenv(standardCodeDockerImageEnvironment))
	if environmentSettings != nil {
		imageDigest = environmentSettings.DockerImageDigest
	}
	var dockerReadiness *sandbox.DockerReadiness
	if config.workspaceSandbox && config.dockerExecution && sandbox.ValidOCIImageDigest(imageDigest) {
		readiness, err := desktop.ProbeStandardCodeDockerReadiness(context.Background(), true, imageDigest)
		if err != nil {
			return err
		}
		dockerReadiness = &readiness
	}
	workspaceSandboxAvailable := desktopWorkspaceSandboxRuntimeAvailable(config,
		localReadiness, dockerReadiness)
	if sbxBackend != nil {
		// Native startup owns the opt-in registration. Settings GET remains a
		// read-only observation; an unavailable daemon is surfaced by readiness.
		if config.workspaceSandbox && environmentSettings.SBXEnabled {
			_ = sbxBackend.Prepare(context.Background())
		}
		readiness, err := sbxBackend.Readiness(context.Background())
		if err != nil {
			return err
		}
		sbxReadiness = &readiness
		workspaceSandboxAvailable = workspaceSandboxAvailable || (config.workspaceSandbox && readiness.Ready)
	}
	var executionRuntimeAuthority *domain.ExecutionPermissionRuntimeAuthority
	if config.dangerFullAccess {
		executionRuntimeAuthority = domain.NewExecutionPermissionRuntimeAuthority()
	}
	executionPermissionCapabilities := domain.ExecutionPermissionRuntimeCapabilities{
		WorkspaceSandboxEnabled: workspaceSandboxAvailable,
		OperatorApprovalEnabled: config.permissionControl,
		DangerFullAccessEnabled: config.dangerFullAccess,
		RuntimeAuthority:        executionRuntimeAuthority,
	}
	if err := executionPermissionCapabilities.Validate(); err != nil {
		return err
	}
	bundle, err := webui.LoadEmbeddedFS(webassets.Files, "dist")
	if err != nil {
		return apperror.Wrap(apperror.CodeFailedPrecondition,
			"embedded Desktop UI validation failed", err)
	}
	readToken, err := httpapi.GenerateAccessToken()
	if err != nil {
		return err
	}
	controlToken := ""
	// Docker product control uses the same short-lived Desktop control token;
	// it never receives an independent renderer authority channel.
	if config.profileControl || config.permissionControl || config.browserCDPControl ||
		config.runCreation || config.sessionMessages || config.sessionSteeringControl ||
		config.runLifecycle || config.runExecution || config.planDeliveryControl ||
		config.approvalControl || config.modelControl || config.providerCredentials ||
		config.fileEditReview || config.fileEditProposals || config.runWakeControl ||
		config.fileEditApply || config.runWakeExecution || config.runWakeWorker ||
		config.scheduledJobControl || config.scheduledJobWorker ||
		config.skillInstallation || config.evidenceAttachment ||
		config.verificationEvidence || config.embeddedAnalyzer || config.userTerminal ||
		config.dockerExecution || config.batchDeliveryControl || config.batchValidation ||
		config.uiEvidence || config.gitAdvanced || config.githubReview || config.sandboxSettings {
		controlToken, err = httpapi.GenerateAccessToken()
		if err != nil {
			return err
		}
	}

	databasePath := filepath.Join(homePath, "cyberagent.db")
	var computerUseExecutable string
	if goruntime.GOOS == "windows" && config.runExecution {
		computerUseExecutable, err = os.Executable()
		if err != nil {
			return err
		}
	}
	controlPlane, err := desktop.OpenControlPlane(desktop.ControlPlaneConfig{
		TerminalSettingsStore: terminalSettingsStore,
		DatabasePath:          databasePath, HomePath: homePath, ReadToken: readToken,
		ControlToken:          controlToken,
		ComputerUseExecutable: computerUseExecutable,
		RunControlEnabled:     config.profileControl, RunCreationEnabled: config.runCreation,
		ExecutionPermissionControlEnabled:  config.permissionControl,
		ExecutionPermissionCapabilities:    executionPermissionCapabilities,
		LocalSandboxReadiness:              localReadiness,
		LocalSandboxBackend:                localBackend,
		StandardCodeDockerReadiness:        dockerReadiness,
		StandardCodeDockerImageDigest:      imageDigest,
		SBXBackend:                         sbxBackend,
		SBXReadiness:                       sbxReadiness,
		SandboxEnvironmentControlEnabled:   config.sandboxSettings,
		SandboxEnvironmentSettings:         environmentSettings,
		WebSearchEndpoint:                  strings.TrimSpace(os.Getenv(webSearchEndpointEnvironment)),
		BrowserCDPPermissionControlEnabled: config.browserCDPControl,
		BrowserCDPPermissionCapabilities: domain.BrowserCDPPermissionRuntimeCapabilities{
			ControlEnabled:   config.browserCDPControl,
			FullDebugEnabled: config.fullCDPDebug,
		},
		FullCDPSessionControlEnabled: config.fullCDPDebug,
		FullCDPRuntimeCapabilities: browserruntime.FullCDPRuntimeCapabilities{
			StartEnabled:             config.fullCDPDebug,
			DisposableProfileEnabled: config.fullCDPDebug,
			TransportEnabled:         config.fullCDPDebug,
		},
		SessionMessageEnabled:              config.sessionMessages,
		SessionSteeringControlEnabled:      config.sessionSteeringControl,
		RunLifecycleEnabled:                config.runLifecycle,
		RunExecutionEnabled:                config.runExecution,
		PlanDeliveryControlEnabled:         config.planDeliveryControl,
		ApprovalControlEnabled:             config.approvalControl,
		ModelControlEnabled:                config.modelControl,
		ProviderCredentialEnabled:          config.providerCredentials,
		FileEditReviewEnabled:              config.fileEditReview,
		FileEditProposalEnabled:            config.fileEditProposals,
		RunWakeControlEnabled:              config.runWakeControl,
		FileEditApplyEnabled:               config.fileEditApply,
		RunWakeExecutionEnabled:            config.runWakeExecution,
		RunWakeWorkerEnabled:               config.runWakeWorker,
		ScheduledJobControlEnabled:         config.scheduledJobControl,
		ScheduledJobWorkerEnabled:          config.scheduledJobWorker,
		ScheduledJobConfirmedOnly:          config.scheduledJobConfirmedOnly,
		SkillInstallationEnabled:           config.skillInstallation,
		EvidenceAttachmentEnabled:          config.evidenceAttachment,
		VerificationEvidenceEnabled:        config.verificationEvidence,
		EmbeddedAnalyzerExecutionEnabled:   config.embeddedAnalyzer,
		BatchDeliveryControlEnabled:        config.batchDeliveryControl,
		BatchDeliveryHostValidationEnabled: config.batchValidation,
		UIEvidenceControlEnabled:           config.uiEvidence,
		VerificationSettingsReader:         verificationSettingsStore,
		VerificationSettingsControlEnabled: config.riskProfileRestart,
		BrowserRuntimeCapabilities: browserruntime.ProductionRuntimeCapabilities{
			SafeWebStartEnabled: true, DisposableProfileEnabled: true,
			NetworkContainmentEnabled: true, RestrictedCDPEnabled: true,
		},
		UserTerminalEnabled:        config.userTerminal,
		DockerExecutionEnabled:     config.dockerExecution,
		CodeIntelConfigPath:        config.codeIntelConfig,
		GitAdvancedControlEnabled:  config.gitAdvanced,
		GitHubReviewControlEnabled: config.githubReview,
		GitManagedWorktreeRoot:     config.gitWorktreeRoot,
		AppVersion:                 app.Version, UIHandler: bundle,
		OnWakeWorkerError: func(runErr error) {
			fmt.Fprintln(os.Stderr, "wake-worker:", runErr)
		},
		OnScheduledJobWorkerError: func(runErr error) {
			fmt.Fprintln(os.Stderr, "scheduled-job-worker:", runErr)
		},
	})
	if err != nil {
		return apperror.Wrap(apperror.CodeFailedPrecondition,
			"desktop data store validation failed", err)
	}
	defer controlPlane.Close()
	dockerExecutionEnabled, err := controlPlane.DockerExecutionEnabled()
	if err != nil {
		return apperror.Wrap(apperror.CodeFailedPrecondition,
			"desktop Docker capability projection failed", err)
	}
	if err := controlPlane.StartWakeWorker(context.Background()); err != nil {
		return apperror.Wrap(apperror.CodeFailedPrecondition,
			"desktop wake worker could not start", err)
	}
	if err := controlPlane.StartTerminalBoundaryMonitor(context.Background()); err != nil {
		return apperror.Wrap(apperror.CodeFailedPrecondition,
			"desktop terminal boundary monitor could not start", err)
	}

	lifecycle := desktop.NewLifecycle(wailsWindowRestorer{})
	selector, preview := desktop.NewSkillPackagePreviewBoundary()
	commandRuntimeAdapterInstalled, commandRuntimeAdapterReady :=
		controlPlane.CommandRuntimeProcessStatus()
	bridge, err := desktop.NewDesktopBridge(desktop.DesktopBridgeConfig{
		TerminalSettingsStore: terminalSettingsStore,
		TerminalSettingsConfirmer: func() desktop.DesktopTerminalSettingsConfirmer {
			if terminalSettingsStore == nil {
				return nil
			}
			return nativeTerminalSettingsConfirmer{}
		}(),
		ContextProvider: lifecycle.Context, FilePicker: nativeSkillPackagePicker{},
		ReadToken: readToken, ControlToken: controlToken, APIVersion: httpapi.Version,
		RunControlEnabled: config.profileControl, RunCreationEnabled: config.runCreation,
		StandardCodePresetEnabled:          controlPlane.StandardCodePresetEnabled(),
		ExecutionPermissionControlEnabled:  config.permissionControl,
		WorkspaceSandboxEnabled:            workspaceSandboxAvailable,
		BrowserCDPPermissionControlEnabled: config.browserCDPControl,
		FullCDPDebugEnabled:                config.fullCDPDebug,
		FullCDPSessionControlEnabled:       controlPlane.FullCDPSessionControlEnabled(),
		OperatorApprovalEnabled:            config.permissionControl,
		DangerFullAccessEnabled:            config.dangerFullAccess,
		CommandRuntimeAdapterInstalled:     commandRuntimeAdapterInstalled,
		CommandRuntimeAdapterReady:         commandRuntimeAdapterReady,
		SessionMessageEnabled:              config.sessionMessages,
		SessionSteeringControlEnabled:      config.sessionSteeringControl,
		RunLifecycleEnabled:                config.runLifecycle,
		RunExecutionEnabled:                config.runExecution,
		PlanDeliveryControlEnabled:         config.planDeliveryControl,
		ApprovalControlEnabled:             config.approvalControl,
		ModelControlEnabled:                config.modelControl,
		ProviderCredentialEnabled:          config.providerCredentials,
		FileEditReviewEnabled:              config.fileEditReview,
		FileEditProposalEnabled:            config.fileEditProposals,
		RunWakeControlEnabled:              config.runWakeControl,
		FileEditApplyEnabled:               config.fileEditApply,
		RunWakeExecutionEnabled:            config.runWakeExecution,
		RunWakeWorkerEnabled:               config.runWakeWorker,
		ScheduledJobControlEnabled:         config.scheduledJobControl,
		ScheduledJobWorkerEnabled:          config.scheduledJobWorker,
		SkillInstallationEnabled:           config.skillInstallation,
		EvidenceAttachmentEnabled:          config.evidenceAttachment,
		VerificationEvidenceEnabled:        config.verificationEvidence,
		EmbeddedAnalyzerExecutionEnabled:   config.embeddedAnalyzer,
		BatchDeliveryControlEnabled:        config.batchDeliveryControl,
		BatchDeliveryHostValidationEnabled: config.batchValidation,
		UIEvidenceControlEnabled:           config.uiEvidence,
		UserTerminalEnabled:                config.userTerminal,
		DockerExecutionEnabled:             dockerExecutionEnabled,
		CodeIntelEnabled:                   controlPlane.CodeIntelEnabled(),
		GitAdvancedControlEnabled:          config.gitAdvanced,
		GitHubReviewControlEnabled:         config.githubReview,
		AppVersion:                         app.Version, UIDigest: bundle.Digest(), Selector: selector,
		PreviewBridge: preview, SkillInstaller: controlPlane.SkillInstaller(),
		SkillRegistry:     controlPlane.SkillRegistry(),
		WorkspaceResolver: controlPlane, WorkspaceLauncher: newNativeWorkspaceLauncher(),
		WorkspaceDirectoryPicker:          nativeWorkspaceDirectoryPicker{},
		WorkspaceRegistrar:                controlPlane,
		ClipboardReader:                   desktop.NewNativeClipboardFileReader(),
		ClipboardFiles:                    controlPlane.ClipboardFileImporter(),
		ClipboardImages:                   controlPlane.ClipboardImageImporter(),
		ResourceReader:                    controlPlane,
		ResourceHost:                      nativeChatResourceHost{},
		UserTerminalController:            controlPlane.UserTerminalController(),
		DebugTerminalAgentInputController: controlPlane.DebugTerminalAgentInputController(),
		RiskProfileRestartEnabled:         config.riskProfileRestart,
		VerificationSettingsStore:         verificationSettingsStore,
		VerificationSettingsConfirmer: func() desktop.DesktopVerificationSettingsConfirmer {
			if verificationSettingsStore == nil {
				return nil
			}
			return nativeVerificationSettingsConfirmer{}
		}(),
		RiskProfileRestarter: func() desktop.DesktopRiskProfileRestarter {
			if !config.riskProfileRestart {
				return nil
			}
			return newNativeRiskProfileRestarter()
		}(),
	})
	if err != nil {
		return err
	}

	appOptions := &options.App{
		Title: app.Name, Width: defaultDesktopWidth, Height: defaultDesktopHeight,
		MinWidth: 480, MinHeight: 600,
		Frameless:        true,
		WindowStartState: options.Normal,
		BackgroundColour: options.NewRGBA(0, 0, 0, 0),
		AssetServer: &assetserver.Options{
			Handler: inProcessAPIHandler{next: controlPlane.Handler()},
		},
		OnStartup: func(ctx context.Context) {
			lifecycle.Start(ctx)
			if screens, screenErr := runtime.ScreenGetAll(ctx); screenErr == nil &&
				shouldMaximiseDesktopWindow(screens) {
				runtime.WindowMaximise(ctx)
			}
		},
		OnShutdown: func(context.Context) {
			lifecycle.Stop()
		},
		Bind: []interface{}{bridge},
		// Keep Wails' production context-menu filter: editable controls and
		// selected text get native edit actions; ordinary page surfaces do not.
		// Developer tools remain controlled by the separate production setting.
		EnableDefaultContextMenu: true,
		// Keep OS anti-phishing cloud submission disabled for a local-first app.
		EnableFraudulentWebsiteDetection: false,
		BindingsAllowedOrigins:           "",
		DragAndDrop: &options.DragAndDrop{
			EnableFileDrop: false, DisableWebViewDrop: true,
		},
		SingleInstanceLock: &options.SingleInstanceLock{
			UniqueId:               desktopSingleInstanceID,
			OnSecondInstanceLaunch: secondInstanceHandler(lifecycle),
		},
		Debug:          options.Debug{OpenInspectorOnStartup: false},
		ErrorFormatter: formatDesktopBindingError,
	}
	applyDesktopPlatformOptions(appOptions)
	return wails.Run(appOptions)
}
