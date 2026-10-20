package browserruntime

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"path/filepath"
	"reflect"
	"strings"
	"time"
)

const agentBrowserProcessVersion = "agent_browser_process.v1"

func agentBrowserToken() string {
	var b [24]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic(err)
	}
	return hex.EncodeToString(b[:])
}

func validateAgentBrowserRequest(request AgentBrowserStartRequest) error {
	a := request.Authority
	for _, id := range []string{a.RunID, a.ManagerBootID, a.SessionID, a.PermissionSnapshotID} {
		if id == "" || strings.TrimSpace(id) != id || len(id) > 256 || strings.ContainsAny(id, "\x00\r\n") {
			return ErrBrowserRuntimeBoundary
		}
	}
	if request.CheckAuthority == nil || a.Generation == 0 || a.PermissionRevision < 1 ||
		a.PermissionActivation == 0 || a.RunAuthorizationFence == 0 ||
		a.PermissionMode != "full" ||
		request.RuntimeDeadline.IsZero() || !request.RuntimeDeadline.After(time.Now()) ||
		request.RuntimeDeadline.After(time.Now().Add(24*time.Hour)) {
		return ErrBrowserRuntimeBoundary
	}
	return nil
}

func fixedAgentBrowserArguments(profile string, headless bool) []string {
	args := []string{"--remote-debugging-address=127.0.0.1", "--remote-debugging-port=0", "--user-data-dir=" + profile,
		"--no-first-run", "--no-default-browser-check", "--disable-extensions", "--disable-sync", "--disable-component-update",
		"--disable-background-networking", "--disable-breakpad", "--disable-crash-reporter", "--password-store=basic", "--window-size=1280,900"}
	if headless {
		args = append(args, "--headless=new")
	}
	return append(args, "about:blank")
}

func validateAgentBrowserProcessSpec(spec BrowserStartSpec) error {
	args := fixedAgentBrowserArguments(spec.ProfilePath, false)
	if !reflect.DeepEqual(spec.Arguments, args) && !reflect.DeepEqual(spec.Arguments, fixedAgentBrowserArguments(spec.ProfilePath, true)) {
		return ErrBrowserRuntimeBoundary
	}
	if spec.ProtocolVersion != agentBrowserProcessVersion || !validSHA256(spec.AuthorizationFingerprint) ||
		!validSHA256(spec.ExecutableIdentityFingerprint) || !validSHA256(spec.ExecutableSHA256) ||
		!validSHA256(spec.ProfileOwnershipFingerprint) || !validSHA256(spec.ProfileLeaseFingerprint) ||
		!filepath.IsAbs(spec.ProfilePath) || !filepath.IsAbs(spec.ExecutablePath) ||
		spec.NetworkContainmentFingerprint != "" || spec.FullCDPUsed || spec.LoopbackNavigationRequired ||
		spec.HostNameResolutionDisabled || spec.NetworkDefaultDeny || spec.ShellUsed || spec.PersonalProfileUsed ||
		spec.InitialURL != "about:blank" || spec.RemoteDebuggingAddress != "127.0.0.1" || spec.RemoteDebuggingPort != 0 ||
		spec.ActiveProcessLimit != MaxBrowserProcessCount || spec.JobMemoryLimitBytes != MaxBrowserJobMemoryBytes ||
		spec.CreatedAt.IsZero() || !spec.RuntimeDeadline.After(spec.CreatedAt) || spec.Fingerprint != browserRuntimeFingerprint(spec) {
		return ErrBrowserRuntimeBoundary
	}
	return nil
}

func launchPEAgentBrowser(ctx context.Context, request AgentBrowserStartRequest) (_ *AgentBrowserRuntime, launchErr error) {
	cleanupReceipt := AgentBrowserNoProcessCleanup(request.Authority.SessionID)
	defer func() {
		if launchErr != nil {
			cleanupReceipt.CleanupPending = !cleanupReceipt.ProcessesStopped() || !cleanupReceipt.ProfileRemoved
			launchErr = &AgentBrowserLaunchError{Err: launchErr, Cleanup: cleanupReceipt}
		}
	}()

	if ctx == nil {
		return nil, ErrBrowserRuntimeBoundary
	}
	if err := validateAgentBrowserRequest(request); err != nil {
		return nil, err
	}
	if err := request.CheckAuthority(ctx, request.Authority); err != nil {
		return nil, err
	}
	starter := newPlatformBrowserProcessStarter()
	if !starter.Available() {
		return nil, ErrBrowserRuntimeUnavailable
	}
	identities, err := DiscoverInstalledBrowsers()
	if err != nil {
		return nil, err
	}
	var identity BrowserExecutableIdentity
	var accepted BrowserAcceptanceCandidate
	for _, candidate := range identities {
		if request.Product != "" && candidate.Product != request.Product {
			continue
		}
		acceptance, err := BuildBrowserAcceptanceCandidate(candidate)
		if err == nil && acceptance.Decision == BrowserAcceptanceAccepted && acceptance.ReviewEligible {
			identity, accepted = candidate, acceptance
			break
		}
	}
	if identity.CanonicalPath == "" {
		return nil, errors.New("no accepted installed Chromium browser is available")
	}
	profile, err := prepareAgentBrowserProfile(request.HomePath, request.Authority)
	if err != nil {
		if profile.path != "" {
			cleanupErr := profile.cleanup()
			cleanupReceipt.ProfileRemoved = cleanupErr == nil
			err = errors.Join(err, cleanupErr)
		}
		return nil, err
	}
	cleanupReceipt.ProfileRemoved = false
	life, cancel := context.WithDeadline(context.Background(), request.RuntimeDeadline)
	r := &AgentBrowserRuntime{request: request, profile: profile, product: identity.Product, life: life, cancel: cancel,
		done: make(chan struct{}), operation: make(chan struct{}, 1), refs: map[string]agentBrowserRef{}, state: "starting", epoch: 1}
	r.operation <- struct{}{}
	defer func() {
		if launchErr != nil {
			cleanCtx, cleanCancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cleanCancel()
			var cleanupErr error
			cleanupReceipt, cleanupErr = r.Close(cleanCtx)
			launchErr = errors.Join(launchErr, cleanupErr)
		}
	}()
	if err := revalidateAcceptedBrowserExecutable(identity, accepted); err != nil {
		return nil, err
	}
	if err := request.CheckAuthority(ctx, request.Authority); err != nil {
		return nil, err
	}
	spec := BrowserStartSpec{ProtocolVersion: agentBrowserProcessVersion, AuthorizationFingerprint: browserRuntimeFingerprint(request.Authority),
		ExecutableIdentityFingerprint: identity.Fingerprint, ExecutablePath: identity.CanonicalPath, ExecutableSHA256: identity.ExecutableSHA256,
		ProfileOwnershipFingerprint: browserRuntimeFingerprint(string(profile.marker)), ProfileLeaseFingerprint: browserRuntimeFingerprint(profile.token),
		ProfilePath: profile.path, Arguments: fixedAgentBrowserArguments(profile.path, request.Headless), InitialURL: "about:blank",
		RemoteDebuggingAddress: "127.0.0.1", ActiveProcessLimit: MaxBrowserProcessCount, JobMemoryLimitBytes: MaxBrowserJobMemoryBytes,
		CreatedAt: time.Now().UTC(), RuntimeDeadline: request.RuntimeDeadline}
	spec.Fingerprint = browserRuntimeFingerprint(spec)
	if err := validateAgentBrowserProcessSpec(spec); err != nil {
		return nil, err
	}
	platform, err := starter.Start(ctx, spec)
	if err != nil {
		return nil, err
	}
	r.process = &BrowserProcess{spec: spec, platform: platform}
	if err := r.connect(ctx, profile.path); err != nil {
		return nil, err
	}
	return r, nil
}

func (r *AgentBrowserRuntime) connect(ctx context.Context, profilePath string) error {
	startCtx, startCancel := context.WithTimeout(ctx, 30*time.Second)
	defer startCancel()
	stopLife := context.AfterFunc(r.life, startCancel)
	defer stopLife()
	endpoint, err := waitForDevToolsEndpoint(startCtx, profilePath, r.process)
	if err != nil {
		if exit, ok := r.process.Exit(); ok {
			return fmt.Errorf("agent browser endpoint: %w (exit=%d tree_reaped=%t)", err, exit.ExitCode, exit.TreeReaped)
		}
		return fmt.Errorf("agent browser endpoint: %w", err)
	}
	conn, err := dialRestrictedCDP(startCtx, endpoint)
	if err != nil {
		return err
	}
	r.cdp = newAgentBrowserCDP(conn, r.onEvent)
	if err := r.initialize(startCtx); err != nil {
		return err
	}
	if err := r.request.CheckAuthority(startCtx, r.request.Authority); err != nil {
		return err
	}
	r.mu.Lock()
	r.state = "ready"
	r.mu.Unlock()
	go r.reapOnInvalidation()
	go r.monitor()
	return nil
}
