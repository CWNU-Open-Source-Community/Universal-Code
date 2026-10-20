package browserruntime

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"sync"
	"time"

	"cyberagent-workbench/internal/domain"
)

const (
	BrowserStartSpecProtocolVersion   = "browser_start_spec.v1"
	BrowserProcessExitProtocolVersion = "browser_process_exit.v1"
	WindowsBrowserProcessAdapterName  = "windows_browser_job.v2"
	MaxBrowserProcessCount            = 32
	MaxBrowserJobMemoryBytes          = 2 * 1024 * 1024 * 1024
)

var (
	ErrBrowserRuntimeBoundary              = errors.New("browser runtime boundary is invalid")
	ErrBrowserRuntimeUnavailable           = errors.New("browser runtime is unavailable")
	ErrBrowserStandardUserTokenUnavailable = errors.New(
		"browser standard-user launch token is unavailable")
)

type BrowserStartSpec struct {
	ProtocolVersion               string    `json:"protocol_version"`
	AuthorizationFingerprint      string    `json:"authorization_fingerprint"`
	ExecutableIdentityFingerprint string    `json:"executable_identity_fingerprint"`
	ExecutablePath                string    `json:"executable_path"`
	ExecutableSHA256              string    `json:"executable_sha256"`
	ProfileOwnershipFingerprint   string    `json:"profile_ownership_fingerprint"`
	ProfileLeaseFingerprint       string    `json:"profile_lease_fingerprint"`
	NetworkContainmentFingerprint string    `json:"network_containment_fingerprint"`
	ProfilePath                   string    `json:"profile_path"`
	Arguments                     []string  `json:"arguments"`
	InitialURL                    string    `json:"initial_url"`
	RemoteDebuggingAddress        string    `json:"remote_debugging_address"`
	RemoteDebuggingPort           int       `json:"remote_debugging_port"`
	ActiveProcessLimit            int       `json:"active_process_limit"`
	JobMemoryLimitBytes           uint64    `json:"job_memory_limit_bytes"`
	LoopbackNavigationRequired    bool      `json:"loopback_navigation_required"`
	HostNameResolutionDisabled    bool      `json:"host_name_resolution_disabled"`
	NetworkDefaultDeny            bool      `json:"network_default_deny"`
	FullCDPUsed                   bool      `json:"full_cdp_used"`
	ShellUsed                     bool      `json:"shell_used"`
	PersonalProfileUsed           bool      `json:"personal_profile_used"`
	CreatedAt                     time.Time `json:"created_at"`
	RuntimeDeadline               time.Time `json:"runtime_deadline"`
	Fingerprint                   string    `json:"fingerprint"`
}

type BrowserProcessExit struct {
	ProtocolVersion      string    `json:"protocol_version"`
	Adapter              string    `json:"adapter"`
	StartSpecFingerprint string    `json:"start_spec_fingerprint"`
	ExitCode             int       `json:"exit_code"`
	TreeReaped           bool      `json:"tree_reaped"`
	ProcessScope         string    `json:"process_scope,omitempty"`
	ProcessScopeVerified bool      `json:"process_scope_verified,omitempty"`
	TimedOut             bool      `json:"timed_out"`
	Cancelled            bool      `json:"cancelled"`
	StartedAt            time.Time `json:"started_at"`
	CompletedAt          time.Time `json:"completed_at"`
	RawOutputIncluded    bool      `json:"raw_output_included"`
	Fingerprint          string    `json:"fingerprint"`
}

type browserPlatformProcess interface {
	PID() int
	Done() <-chan struct{}
	Exit() (BrowserProcessExit, bool)
	Stop(context.Context, bool) error
}

type browserProcessStarter interface {
	Name() string
	Available() bool
	Start(context.Context, BrowserStartSpec) (browserPlatformProcess, error)
}

type browserAcceptanceRevalidator func(BrowserExecutableIdentity,
	BrowserAcceptanceCandidate) error

type BrowserProcessController struct {
	starter     browserProcessStarter
	revalidate  browserAcceptanceRevalidator
	containment browserNetworkContainmentFactory
}

type BrowserProcess struct {
	spec                BrowserStartSpec
	platform            browserPlatformProcess
	guard               browserNetworkContainmentGuard
	stopMu              sync.Mutex
	containmentOnce     sync.Once
	containmentDone     chan struct{}
	containmentMu       sync.Mutex
	containmentErr      error
	containmentVerified bool
}

func NewPlatformBrowserProcessController() (*BrowserProcessController, error) {
	return newBrowserProcessController(newPlatformBrowserProcessStarter(),
		revalidateAcceptedBrowserExecutable,
		newPlatformBrowserNetworkContainmentFactory())
}

func newBrowserProcessController(starter browserProcessStarter,
	revalidator browserAcceptanceRevalidator,
	containment browserNetworkContainmentFactory,
) (*BrowserProcessController, error) {
	if starter == nil || revalidator == nil || containment == nil {
		return nil, errors.New("browser process starter, executable revalidator, and network containment are required")
	}
	return &BrowserProcessController{
		starter: starter, revalidate: revalidator, containment: containment,
	}, nil
}

func (controller *BrowserProcessController) Available() bool {
	return controller != nil && controller.starter != nil && controller.containment != nil &&
		controller.starter.Available() && controller.containment.Available()
}

// FullCDPAvailable reports only the process adapter required by the independent
// Full CDP path. Safe Web network containment is deliberately not inherited.
func (controller *BrowserProcessController) FullCDPAvailable() bool {
	return controller != nil && controller.starter != nil &&
		controller.starter.Available()
}

func (controller *BrowserProcessController) Start(ctx context.Context,
	authorization BrowserStartAuthorization, session SessionPlan,
	identity BrowserExecutableIdentity, acceptance BrowserAcceptanceCandidate,
	ownership ProfileOwnershipPlan, attempt BrowserLaunchAttempt,
	launchLease BrowserLaunchLease, review BrowserLaunchReview,
	networkEvidence BrowserNetworkContainmentEvidence,
	networkReview BrowserNetworkContainmentReview,
	networkPlan BrowserNetworkContainmentPlan,
	permission domain.RunBrowserCDPPermissionSnapshot,
	profileLease ProfileRuntimeLease, now time.Time,
) (*BrowserProcess, error) {
	if controller == nil || controller.starter == nil || controller.containment == nil ||
		!controller.starter.Available() || !controller.containment.Available() {
		return nil, ErrBrowserRuntimeUnavailable
	}
	if ctx == nil {
		return nil, ErrBrowserRuntimeBoundary
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := ValidateBrowserStartAuthorization(authorization, session, identity,
		acceptance, ownership, attempt, launchLease, review, networkEvidence,
		networkReview, networkPlan, permission); err != nil {
		return nil, err
	}
	if err := ValidateProfileRuntimeLease(profileLease, authorization, ownership); err != nil {
		return nil, err
	}
	now = now.UTC()
	if now.IsZero() || now.Before(authorization.IssuedAt) ||
		!now.Before(authorization.StartDeadline) || !now.Before(authorization.RuntimeDeadline) {
		return nil, errors.New("browser process authorization expired before start")
	}
	marker, err := readProfileOwnerMarker(ownership.DirectoryPath)
	if err != nil || !markerMatchesOwnership(marker, ownership) ||
		marker.State != ProfileMarkerActive || marker.Fingerprint != profileLease.MarkerFingerprint {
		return nil, errors.New("browser process Profile marker is not the exact active owner")
	}
	if err := controller.revalidate(identity, acceptance); err != nil {
		return nil, fmt.Errorf("revalidate browser immediately before start: %w", err)
	}
	guard, err := controller.containment.Prepare(networkPlan)
	if err != nil {
		return nil, fmt.Errorf("prepare browser network containment: %w", err)
	}
	if guard == nil || guard.Adapter() != networkPlan.Adapter ||
		!validSHA256(guard.Fingerprint()) {
		if guard != nil {
			_ = guard.Close()
		}
		return nil, errors.New("browser network containment guard is invalid")
	}
	spec, err := buildBrowserStartSpec(authorization, identity, ownership,
		profileLease, networkPlan, guard.Fingerprint(), now)
	if err != nil {
		_ = guard.Close()
		return nil, err
	}
	platform, err := controller.starter.Start(ctx, spec)
	if err != nil {
		_ = guard.Close()
		return nil, err
	}
	process := &BrowserProcess{spec: spec, platform: platform, guard: guard,
		containmentDone: make(chan struct{})}
	go process.releaseContainmentWhenDone()
	go process.stopAtDeadline(authorization.RuntimeDeadline)
	return process, nil
}

// StartFullCDP starts one dedicated, Job-owned browser process for the
// independently authorized Full CDP channel. Unlike Start, it carries no Safe
// Web network-containment authority. The exact loopback target remains enforced
// by the CDP session scope and the process still uses a disposable Profile,
// fixed arguments, a standard-user token, and a bounded runtime.
func (controller *BrowserProcessController) StartFullCDP(ctx context.Context,
	authorization FullCDPStartAuthorization, session SessionPlan,
	identity BrowserExecutableIdentity, acceptance BrowserAcceptanceCandidate,
	ownership ProfileOwnershipPlan, attempt BrowserLaunchAttempt,
	launchLease BrowserLaunchLease, review BrowserLaunchReview,
	permission domain.RunBrowserCDPPermissionSnapshot,
	executionPermission domain.RunExecutionPermissionSnapshot,
	executionCapabilities domain.ExecutionPermissionRuntimeCapabilities,
	profileLease ProfileRuntimeLease, now time.Time,
) (*BrowserProcess, error) {
	if controller == nil || controller.starter == nil ||
		!controller.starter.Available() {
		return nil, ErrBrowserRuntimeUnavailable
	}
	if ctx == nil {
		return nil, ErrBrowserRuntimeBoundary
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := ValidateFullCDPStartAuthorization(authorization, session, identity,
		acceptance, ownership, attempt, launchLease, review, permission,
		executionPermission, executionCapabilities); err != nil {
		return nil, err
	}
	if err := ValidateFullCDPProfileRuntimeLease(profileLease, authorization,
		ownership); err != nil {
		return nil, err
	}
	now = now.UTC()
	if now.IsZero() || now.Before(authorization.IssuedAt) ||
		!now.Before(authorization.StartDeadline) ||
		!now.Before(authorization.RuntimeDeadline) {
		return nil, errors.New("full CDP process authorization expired before start")
	}
	marker, err := readProfileOwnerMarker(ownership.DirectoryPath)
	if err != nil || !markerMatchesOwnership(marker, ownership) ||
		marker.State != ProfileMarkerActive ||
		marker.Fingerprint != profileLease.MarkerFingerprint {
		return nil, errors.New("full CDP Profile marker is not the exact active owner")
	}
	if err := controller.revalidate(identity, acceptance); err != nil {
		return nil, fmt.Errorf("revalidate full CDP browser immediately before start: %w", err)
	}
	spec, err := buildFullCDPBrowserStartSpec(authorization, identity, ownership,
		profileLease, now)
	if err != nil {
		return nil, err
	}
	platform, err := controller.starter.Start(ctx, spec)
	if err != nil {
		return nil, err
	}
	process := &BrowserProcess{spec: spec, platform: platform,
		containmentDone: make(chan struct{})}
	go process.stopAtDeadline(authorization.RuntimeDeadline)
	return process, nil
}

func (process *BrowserProcess) PID() int {
	if process == nil || process.platform == nil {
		return 0
	}
	return process.platform.PID()
}

func (process *BrowserProcess) StartSpec() BrowserStartSpec {
	if process == nil {
		return BrowserStartSpec{}
	}
	copySpec := process.spec
	copySpec.Arguments = append([]string(nil), process.spec.Arguments...)
	return copySpec
}

func (process *BrowserProcess) Done() <-chan struct{} {
	if process == nil || process.platform == nil {
		closed := make(chan struct{})
		close(closed)
		return closed
	}
	return process.platform.Done()
}

func (process *BrowserProcess) Exit() (BrowserProcessExit, bool) {
	if process == nil || process.platform == nil {
		return BrowserProcessExit{}, false
	}
	return process.platform.Exit()
}

func (process *BrowserProcess) Stop(ctx context.Context) error {
	if process == nil || process.platform == nil {
		return ErrBrowserRuntimeBoundary
	}
	return process.requestStop(ctx, false)
}

func (process *BrowserProcess) requestStop(ctx context.Context, timedOut bool) error {
	process.stopMu.Lock()
	defer process.stopMu.Unlock()
	if _, exited := process.platform.Exit(); exited {
		return nil
	}
	// A failed or cancelled termination attempt must not permanently consume the
	// only stop opportunity. Serializing here preserves idempotency while
	// allowing a later bounded reaper to retry the same Job-owned process tree.
	return process.platform.Stop(ctx, timedOut)
}

func (process *BrowserProcess) WaitForContainmentCleanup(ctx context.Context) (bool, error) {
	if process == nil || process.platform == nil || process.guard == nil ||
		process.containmentDone == nil {
		return false, ErrBrowserRuntimeBoundary
	}
	if ctx == nil {
		ctx = context.Background()
	}
	select {
	case <-ctx.Done():
		return false, ctx.Err()
	case <-process.containmentDone:
		process.containmentMu.Lock()
		defer process.containmentMu.Unlock()
		return process.containmentVerified, process.containmentErr
	}
}

func (process *BrowserProcess) stopAtDeadline(deadline time.Time) {
	delay := time.Until(deadline)
	if delay <= 0 {
		_ = process.requestStop(context.Background(), true)
		return
	}
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-process.platform.Done():
	case <-timer.C:
		_ = process.requestStop(context.Background(), true)
	}
}

func (process *BrowserProcess) releaseContainmentWhenDone() {
	if process == nil || process.platform == nil || process.guard == nil {
		return
	}
	<-process.platform.Done()
	process.containmentOnce.Do(func() {
		err := process.guard.Close()
		verified := err == nil && process.guard.CleanupVerified()
		process.containmentMu.Lock()
		process.containmentErr = err
		process.containmentVerified = verified
		process.containmentMu.Unlock()
		close(process.containmentDone)
	})
}

func buildBrowserStartSpec(authorization BrowserStartAuthorization,
	identity BrowserExecutableIdentity, ownership ProfileOwnershipPlan,
	profileLease ProfileRuntimeLease, networkPlan BrowserNetworkContainmentPlan,
	containmentFingerprint string, now time.Time,
) (BrowserStartSpec, error) {
	arguments := fixedRestrictedBrowserArguments(ownership.DirectoryPath)
	spec := BrowserStartSpec{
		ProtocolVersion:               BrowserStartSpecProtocolVersion,
		AuthorizationFingerprint:      authorization.Fingerprint,
		ExecutableIdentityFingerprint: identity.Fingerprint,
		ExecutablePath:                identity.CanonicalPath,
		ExecutableSHA256:              identity.ExecutableSHA256,
		ProfileOwnershipFingerprint:   ownership.Fingerprint,
		ProfileLeaseFingerprint:       profileLease.Fingerprint,
		NetworkContainmentFingerprint: containmentFingerprint,
		ProfilePath:                   ownership.DirectoryPath,
		Arguments:                     arguments,
		InitialURL:                    "about:blank",
		RemoteDebuggingAddress:        "127.0.0.1",
		RemoteDebuggingPort:           0,
		ActiveProcessLimit:            MaxBrowserProcessCount,
		JobMemoryLimitBytes:           MaxBrowserJobMemoryBytes,
		LoopbackNavigationRequired:    true,
		HostNameResolutionDisabled:    true,
		NetworkDefaultDeny:            true,
		CreatedAt:                     now.UTC(), RuntimeDeadline: authorization.RuntimeDeadline,
	}
	spec.Fingerprint = browserRuntimeFingerprint(spec)
	if err := validateBrowserStartSpec(spec, authorization, identity, ownership,
		profileLease, networkPlan, containmentFingerprint); err != nil {
		return BrowserStartSpec{}, err
	}
	return spec, nil
}

func buildFullCDPBrowserStartSpec(authorization FullCDPStartAuthorization,
	identity BrowserExecutableIdentity, ownership ProfileOwnershipPlan,
	profileLease ProfileRuntimeLease, now time.Time,
) (BrowserStartSpec, error) {
	arguments := fixedFullCDPBrowserArguments(ownership.DirectoryPath)
	spec := BrowserStartSpec{
		ProtocolVersion:               BrowserStartSpecProtocolVersion,
		AuthorizationFingerprint:      authorization.Fingerprint,
		ExecutableIdentityFingerprint: identity.Fingerprint,
		ExecutablePath:                identity.CanonicalPath,
		ExecutableSHA256:              identity.ExecutableSHA256,
		ProfileOwnershipFingerprint:   ownership.Fingerprint,
		ProfileLeaseFingerprint:       profileLease.Fingerprint,
		ProfilePath:                   ownership.DirectoryPath,
		Arguments:                     arguments,
		InitialURL:                    "about:blank",
		RemoteDebuggingAddress:        "127.0.0.1",
		RemoteDebuggingPort:           0,
		ActiveProcessLimit:            MaxBrowserProcessCount,
		JobMemoryLimitBytes:           MaxBrowserJobMemoryBytes,
		FullCDPUsed:                   true,
		CreatedAt:                     now.UTC(),
		RuntimeDeadline:               authorization.RuntimeDeadline,
	}
	spec.Fingerprint = browserRuntimeFingerprint(spec)
	if err := validateFullCDPBrowserStartSpec(spec, authorization, identity,
		ownership, profileLease); err != nil {
		return BrowserStartSpec{}, err
	}
	return spec, nil
}

func validateBrowserStartSpec(spec BrowserStartSpec,
	authorization BrowserStartAuthorization, identity BrowserExecutableIdentity,
	ownership ProfileOwnershipPlan, profileLease ProfileRuntimeLease,
	networkPlan BrowserNetworkContainmentPlan, containmentFingerprint string,
) error {
	if spec.ProtocolVersion != BrowserStartSpecProtocolVersion ||
		spec.AuthorizationFingerprint != authorization.Fingerprint ||
		spec.ExecutableIdentityFingerprint != identity.Fingerprint ||
		spec.ExecutablePath != identity.CanonicalPath ||
		spec.ExecutableSHA256 != identity.ExecutableSHA256 ||
		spec.ProfileOwnershipFingerprint != ownership.Fingerprint ||
		spec.ProfileLeaseFingerprint != profileLease.Fingerprint ||
		spec.NetworkContainmentFingerprint != containmentFingerprint ||
		authorization.NetworkPlanFingerprint != networkPlan.Fingerprint ||
		networkPlan.ExecutablePath != spec.ExecutablePath ||
		spec.ProfilePath != ownership.DirectoryPath ||
		!reflect.DeepEqual(spec.Arguments, fixedRestrictedBrowserArguments(ownership.DirectoryPath)) ||
		spec.InitialURL != "about:blank" || spec.RemoteDebuggingAddress != "127.0.0.1" ||
		spec.RemoteDebuggingPort != 0 || spec.ActiveProcessLimit != MaxBrowserProcessCount ||
		spec.JobMemoryLimitBytes != MaxBrowserJobMemoryBytes ||
		!spec.LoopbackNavigationRequired || !spec.HostNameResolutionDisabled ||
		!spec.NetworkDefaultDeny || spec.FullCDPUsed || !validSHA256(containmentFingerprint) ||
		spec.ShellUsed || spec.PersonalProfileUsed ||
		spec.CreatedAt.IsZero() || !spec.RuntimeDeadline.Equal(authorization.RuntimeDeadline) ||
		!spec.RuntimeDeadline.After(spec.CreatedAt) ||
		spec.Fingerprint != browserRuntimeFingerprint(spec) {
		return ErrBrowserRuntimeBoundary
	}
	return nil
}

func validateFullCDPBrowserStartSpec(spec BrowserStartSpec,
	authorization FullCDPStartAuthorization, identity BrowserExecutableIdentity,
	ownership ProfileOwnershipPlan, profileLease ProfileRuntimeLease,
) error {
	if spec.ProtocolVersion != BrowserStartSpecProtocolVersion ||
		spec.AuthorizationFingerprint != authorization.Fingerprint ||
		spec.ExecutableIdentityFingerprint != identity.Fingerprint ||
		spec.ExecutablePath != identity.CanonicalPath ||
		spec.ExecutableSHA256 != identity.ExecutableSHA256 ||
		spec.ProfileOwnershipFingerprint != ownership.Fingerprint ||
		spec.ProfileLeaseFingerprint != profileLease.Fingerprint ||
		spec.NetworkContainmentFingerprint != "" ||
		spec.ProfilePath != ownership.DirectoryPath ||
		!reflect.DeepEqual(spec.Arguments,
			fixedFullCDPBrowserArguments(ownership.DirectoryPath)) ||
		spec.InitialURL != "about:blank" ||
		spec.RemoteDebuggingAddress != "127.0.0.1" ||
		spec.RemoteDebuggingPort != 0 ||
		spec.ActiveProcessLimit != MaxBrowserProcessCount ||
		spec.JobMemoryLimitBytes != MaxBrowserJobMemoryBytes ||
		spec.LoopbackNavigationRequired || spec.HostNameResolutionDisabled ||
		spec.NetworkDefaultDeny || !spec.FullCDPUsed || spec.ShellUsed ||
		spec.PersonalProfileUsed || spec.CreatedAt.IsZero() ||
		!spec.RuntimeDeadline.Equal(authorization.RuntimeDeadline) ||
		!spec.RuntimeDeadline.After(spec.CreatedAt) ||
		spec.Fingerprint != browserRuntimeFingerprint(spec) {
		return ErrBrowserRuntimeBoundary
	}
	return nil
}

func fixedRestrictedBrowserArguments(profilePath string) []string {
	return []string{
		"--headless=new",
		"--remote-debugging-address=127.0.0.1",
		"--remote-debugging-port=0",
		"--user-data-dir=" + profilePath,
		"--no-first-run",
		"--no-default-browser-check",
		"--disable-background-networking",
		"--disable-component-update",
		"--disable-default-apps",
		"--disable-extensions",
		"--disable-sync",
		"--disable-translate",
		"--disable-breakpad",
		"--disable-crash-reporter",
		"--metrics-recording-only",
		"--password-store=basic",
		"--no-proxy-server",
		"--host-resolver-rules=MAP * ~NOTFOUND, EXCLUDE 127.0.0.1, EXCLUDE ::1",
		"about:blank",
	}
}

func fixedFullCDPBrowserArguments(profilePath string) []string {
	return []string{
		"--headless=new",
		"--remote-debugging-address=127.0.0.1",
		"--remote-debugging-port=0",
		"--user-data-dir=" + profilePath,
		"--no-first-run",
		"--no-default-browser-check",
		"--disable-background-networking",
		"--disable-component-update",
		"--disable-default-apps",
		"--disable-extensions",
		"--disable-sync",
		"--disable-translate",
		"--disable-breakpad",
		"--disable-crash-reporter",
		"--metrics-recording-only",
		"--password-store=basic",
		"--no-proxy-server",
		"--host-resolver-rules=MAP * ~NOTFOUND, EXCLUDE 127.0.0.1, EXCLUDE ::1",
		"about:blank",
	}
}

func revalidateAcceptedBrowserExecutable(identity BrowserExecutableIdentity,
	expected BrowserAcceptanceCandidate,
) error {
	if err := RevalidateBrowserExecutableIdentity(identity); err != nil {
		return err
	}
	current, err := BuildBrowserAcceptanceCandidate(identity)
	if err != nil {
		return err
	}
	if !reflect.DeepEqual(current, expected) ||
		current.Decision != BrowserAcceptanceAccepted || !current.ReviewEligible {
		return errors.New("browser publisher acceptance changed before process start")
	}
	return nil
}

func newBrowserProcessExit(adapter string, spec BrowserStartSpec, exitCode int,
	treeReaped bool, timedOut bool, cancelled bool, startedAt time.Time,
	completedAt time.Time,
) BrowserProcessExit {
	exit := BrowserProcessExit{
		ProtocolVersion: BrowserProcessExitProtocolVersion,
		Adapter:         adapter, StartSpecFingerprint: spec.Fingerprint,
		ExitCode: exitCode, TreeReaped: treeReaped, TimedOut: timedOut,
		Cancelled: cancelled, StartedAt: startedAt.UTC(), CompletedAt: completedAt.UTC(),
	}
	exit.Fingerprint = browserRuntimeFingerprint(exit)
	return exit
}
