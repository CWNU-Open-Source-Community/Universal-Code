package sandbox

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode/utf8"
)

const SBXBackendName = "docker_sandboxes"
const SBXPolicyVersion = "sbx-command-runtime.v1"
const SBXReadinessProtocolVersion = "sbx-readiness.v1"

// A fixed product namespace prevents use of the user's everyday daemon,
// template cache and credential store. The user installs/logs in themselves.
// Contract: docker/sbx-kits-contrib/scripts/test-kit-e2e.sh, lines 10-38.
const SBXAppName = "traverse-runtime"

var (
	ErrSBXBoundary    = errors.New("Docker Sandboxes boundary is invalid")
	ErrSBXUnavailable = errors.New("Docker Sandboxes is not ready")
	ErrSBXCLI         = errors.New("Docker Sandboxes CLI operation failed")
	ErrSBXOutputLimit = errors.New("Docker Sandboxes output limit exceeded")
	ErrSBXOwnership   = errors.New("Docker Sandboxes ownership cannot be confirmed")
	ErrSBXCleanup     = errors.New("Docker Sandboxes VM removal cannot be confirmed")
	ErrSBXReplay      = errors.New("Docker Sandboxes operation was already dispatched; inspect its existing Job")
)

var sbxTemplate = regexp.MustCompile(`^[a-z0-9][a-z0-9._:/-]*@sha256:[0-9a-f]{64}$`)

// sbx v0.47 accepts an app-name suffix of at most 20 ASCII letters, digits,
// hyphens or underscores. Empty selects the user's default namespace, so it
// cannot provide this adapter's fixed isolation boundary.
var sbxAppName = regexp.MustCompile(`^[A-Za-z0-9_-]{1,20}$`)

func ValidSBXTemplateReference(value string) bool {
	return len(value) <= 512 && sbxTemplate.MatchString(value) &&
		!strings.Contains(value, "://") && !strings.Contains(value, "..") &&
		!strings.Contains(value, "//") && strings.Count(value, "@") == 1
}

type SBXBackendConfig struct {
	Enabled           bool
	ExecutablePath    string
	HelperExecutable  string
	TemplateReference string
	JournalRoot       string
}

func (config SBXBackendConfig) validate(appName string) error {
	if !sbxAppName.MatchString(appName) ||
		(config.TemplateReference != "" && !ValidSBXTemplateReference(config.TemplateReference)) {
		return ErrSBXBoundary
	}
	return nil
}

type SBXBackendOption func(*SBXBackend)

func WithSBXProcessTransport(transport SBXProcessTransport) SBXBackendOption {
	return func(b *SBXBackend) {
		if transport != nil {
			b.transport = transport
		}
	}
}

func WithSBXDaemonTransport(transport SBXDaemonTransport) SBXBackendOption {
	return func(b *SBXBackend) {
		if transport != nil {
			b.daemon = transport
		}
	}
}

type SBXReadiness struct {
	ProtocolVersion           string    `json:"protocol_version"`
	Status                    string    `json:"status"`
	ReasonCode                string    `json:"reason_code"`
	RemediationCode           string    `json:"remediation_code"`
	Ready                     bool      `json:"ready"`
	FeatureEnabled            bool      `json:"feature_enabled"`
	CLIInstalled              bool      `json:"cli_installed"`
	TemplateConfigured        bool      `json:"template_configured"`
	DaemonReachable           bool      `json:"daemon_reachable"`
	CredentialIsolationProven bool      `json:"credential_isolation_proven"`
	MCPIsolationProven        bool      `json:"mcp_isolation_proven"`
	Generation                string    `json:"generation"`
	EvidenceFingerprint       string    `json:"evidence_fingerprint"`
	CheckedAt                 time.Time `json:"checked_at"`
	ExpiresAt                 time.Time `json:"expires_at"`
}

func (r SBXReadiness) Validate() error {
	if r.ProtocolVersion != SBXReadinessProtocolVersion || !sbxSHA(r.Generation) || !sbxSHA(r.EvidenceFingerprint) ||
		r.CheckedAt.IsZero() || !r.ExpiresAt.After(r.CheckedAt) || r.ExpiresAt.Sub(r.CheckedAt) > 30*time.Second ||
		(r.Status != "ready" && r.Status != "unavailable") || r.ReasonCode == "" ||
		(r.Ready && (r.Status != "ready" || !r.FeatureEnabled || !r.CLIInstalled || !r.TemplateConfigured || !r.DaemonReachable || !r.CredentialIsolationProven || !r.MCPIsolationProven)) ||
		(!r.Ready && r.Status == "ready") {
		return ErrSBXBoundary
	}
	return nil
}
func (r SBXReadiness) ReadyAt(now time.Time) bool {
	return r.Validate() == nil && r.Ready && !now.Before(r.CheckedAt) && now.Before(r.ExpiresAt)
}

// Template cache contents are deliberately not inferred from undocumented CLI
// JSON. Creation uses --pull never, so a missing pinned template fails without
// downloading it. The read-only probe never creates, executes, pulls or logs in.
func ProbeSBXReadiness(ctx context.Context, enabled bool, executablePath, templateReference string) (SBXReadiness, error) {
	b, err := NewSBXBackend(SBXBackendConfig{Enabled: enabled,
		ExecutablePath: executablePath, TemplateReference: templateReference})
	if err != nil {
		return SBXReadiness{}, err
	}
	return b.Readiness(ctx)
}

type SBXBackend struct {
	config                    SBXBackendConfig
	transport                 SBXProcessTransport
	daemon                    SBXDaemonTransport
	generation, executableSHA string
	mu                        sync.Mutex
	lock                      *os.File
	namespaceLock             *sbxNamespaceLock
	ownedJournal              bool
	helperSHA, helperName     string
	helperPrepared            atomic.Bool
	versionGate               chan struct{}
	versionOutput             string
	closed                    bool
	lifetime                  context.Context
	cancel                    context.CancelFunc
}

func NewSBXBackend(config SBXBackendConfig, options ...SBXBackendOption) (*SBXBackend, error) {
	if err := config.validate(SBXAppName); err != nil {
		return nil, err
	}
	b := &SBXBackend{config: config, transport: sbxProcessTransport{}, daemon: newSBXDaemonTransport(), versionGate: make(chan struct{}, 1)}
	b.lifetime, b.cancel = context.WithCancel(context.Background())
	for _, option := range options {
		if option != nil {
			option(b)
		}
	}
	if config.ExecutablePath != "" {
		var err error
		resolved, resolveErr := filepath.EvalSymlinks(config.ExecutablePath)
		if resolveErr == nil {
			b.config.ExecutablePath = resolved
		}
		b.executableSHA, err = sbxFileDigest(b.config.ExecutablePath)
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return nil, ErrSBXBoundary
		}
	}
	if config.HelperExecutable != "" {
		resolved, err := filepath.EvalSymlinks(config.HelperExecutable)
		if err != nil {
			return nil, ErrSBXBoundary
		}
		b.config.HelperExecutable = resolved
		b.helperSHA, err = sbxFileDigest(resolved)
		if err != nil {
			return nil, ErrSBXBoundary
		}
		b.helperName = "traverse-empty-" + sbxDigest(resolved, b.helperSHA)[:32]
	}
	nonce := make([]byte, 32)
	if _, err := rand.Read(nonce); err != nil {
		return nil, err
	}
	b.generation = sbxDigest(SBXPolicyVersion, SBXAppName, b.executableSHA, b.helperSHA, b.helperName, config.TemplateReference, hex.EncodeToString(nonce))
	if config.JournalRoot != "" {
		if _, err := sbxCanonical(config.JournalRoot, true); err != nil {
			return nil, err
		}
		lockPath := filepath.Join(config.JournalRoot, ".sbx-owner.lock")
		if _, err := os.Lstat(lockPath); err == nil {
			if _, err := sbxCanonical(lockPath, false); err != nil {
				return nil, ErrSBXOwnership
			}
		} else if !errors.Is(err, os.ErrNotExist) {
			return nil, err
		}
		file, err := os.OpenFile(filepath.Join(config.JournalRoot, ".sbx-owner.lock"), os.O_CREATE|os.O_RDWR, 0600)
		if err != nil {
			return nil, err
		}
		if _, err := sbxCanonical(file.Name(), false); err != nil {
			_ = file.Close()
			return nil, ErrSBXOwnership
		}
		if err := sbxLock(file); err != nil {
			_ = file.Close()
			return nil, ErrSBXOwnership
		}
		b.lock = file
		b.ownedJournal = true
	}
	return b, nil
}

func (b *SBXBackend) Generation() string {
	if b == nil {
		return ""
	}
	return b.generation
}

func (b *SBXBackend) Available() bool {
	return b != nil && b.config.Enabled && b.executableSHA != "" && b.ownedJournal &&
		ValidSBXTemplateReference(b.config.TemplateReference) && b.lifetime.Err() == nil
}
func (b *SBXBackend) TemplateReference() string {
	if b == nil {
		return ""
	}
	return b.config.TemplateReference
}

func (b *SBXBackend) Readiness(ctx context.Context) (SBXReadiness, error) {
	if b == nil || ctx == nil {
		return SBXReadiness{}, ErrSBXBoundary
	}
	bounded, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	r := SBXReadiness{ProtocolVersion: SBXReadinessProtocolVersion, Status: "unavailable",
		FeatureEnabled: b.config.Enabled, TemplateConfigured: ValidSBXTemplateReference(b.config.TemplateReference), Generation: b.generation,
		CheckedAt: time.Now().UTC()}
	r.ExpiresAt = r.CheckedAt.Add(30 * time.Second)
	finish := func(reason, remediation string) (SBXReadiness, error) {
		r.ReasonCode, r.RemediationCode = reason, remediation
		r.EvidenceFingerprint = sbxDigest(r.Generation, reason, remediation)
		return r, nil
	}
	if b.lifetime.Err() != nil {
		return finish("backend_closed", "restart_application")
	}
	if b.executableSHA == "" {
		return finish("cli_missing", "install_sbx")
	}
	r.CLIInstalled = true
	version, versionErr := b.compatibleCLI(bounded)
	if versionErr != nil || version == "" {
		return finish("cli_unavailable", "check_sbx_installation")
	}
	if !sbxCompatibleVersion(version) {
		return finish("cli_version_unsupported", "install_supported_sbx_version")
	}
	if !r.FeatureEnabled {
		return finish("disabled", "enable_sbx")
	}
	if !r.TemplateConfigured {
		return finish("template_missing", "configure_pinned_template")
	}
	if err := b.daemon.Check(bounded); err != nil {
		return finish("daemon_protocol_unavailable", "start_supported_sbx_daemon")
	}
	if _, err := b.inventory(bounded); err != nil {
		return finish("daemon_unavailable", "start_and_sign_in_sbx")
	}
	r.DaemonReachable = true
	ssh, sshErr := b.isolationSetting(bounded, "ssh.agentForwardingEnabled")
	if sshErr != nil || ssh {
		return finish("ssh_forwarding_not_disabled", "disable_sbx_ssh_forwarding_and_restart_daemon")
	}
	if !b.helperPrepared.Load() {
		return finish("mcp_helper_unavailable", "restart_application_to_prepare_sbx_helper")
	}
	if err := b.verifyMCPHelper(bounded); err != nil {
		return finish("mcp_helper_unavailable", "restart_application_to_prepare_sbx_helper")
	}
	gateway, gatewayErr := b.isolationSetting(bounded, "mcp.forceLocalGateway")
	if gatewayErr != nil || !gateway {
		return finish("mcp_local_gateway_required", "enable_sbx_local_gateway_and_restart_daemon")
	}
	r.MCPIsolationProven, r.CredentialIsolationProven = true, true
	r.Ready, r.Status = true, "ready"
	r.ReasonCode = "ready"
	r.EvidenceFingerprint = sbxDigest(b.generation, version, "ssh.agentForwardingEnabled=false", "mcp.forceLocalGateway=true", b.helperSHA, b.helperName, b.config.TemplateReference)
	return r, nil
}

type SBXRunRequest struct {
	OperationKey, RequestFingerprint, RuntimeGeneration string
	DrydockRoot                                         string
	SourceRepository                                    bool     // Native source command; .git remains a separate read-only mount.
	Arguments                                           []string // guest executable followed by literal argv; no CLI flags
	Environment                                         []string // complete explicit guest environment, used with env -i
	WorkingDirectory                                    string   // relative to the primary workspace
	Timeout                                             time.Duration
	OutputLimit                                         int
	AuthorityCheck                                      func(context.Context) error
	// BeforeExec completes a process-local manager admission handshake only
	// after all VM identity and ownership checks. It is never a wire input.
	BeforeExec func(context.Context) error `json:"-"`
}

type SBXExecutionResult struct {
	ExitCode           int
	Stdout, Stderr     []byte
	TreeReaped         bool
	ReceiptFingerprint string
}

func (b *SBXBackend) Run(ctx context.Context, request SBXRunRequest, stdin io.Reader) (SBXExecutionResult, error) {
	// Until create is dispatched, no VM exists for this invocation.
	result := SBXExecutionResult{ExitCode: 125, TreeReaped: true}
	if b == nil || ctx == nil || b.validateRequest(request) != nil {
		return result, ErrSBXBoundary
	}
	runCtx, cancelRun := context.WithCancel(ctx)
	stopLifetime := context.AfterFunc(b.lifetime, cancelRun)
	defer func() { stopLifetime(); cancelRun() }()
	ctx = runCtx
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed || b.lock == nil {
		return result, ErrSBXUnavailable
	}
	if err := b.acquireNamespace(); err != nil {
		return result, err
	}
	if err := request.AuthorityCheck(ctx); err != nil {
		return result, err
	}
	r, err := b.Readiness(ctx)
	if err != nil || !r.Ready {
		return result, errors.Join(err, ErrSBXUnavailable)
	}
	key := sbxDigest(request.OperationKey)
	if _, err := os.Lstat(filepath.Join(b.config.JournalRoot, key+".json")); err == nil {
		result.TreeReaped = false // the prior dispatch's owned VM is unknown here
		return result, ErrSBXReplay
	} else if !errors.Is(err, os.ErrNotExist) {
		return result, ErrSBXOwnership
	}
	nonce := make([]byte, 16)
	if _, err := rand.Read(nonce); err != nil {
		return result, err
	}
	record := sbxRecord{AppName: SBXAppName, Version: SBXPolicyVersion, OperationDigest: key, RequestFingerprint: request.RequestFingerprint,
		Name: "traverse-sbx-" + hex.EncodeToString(nonce), Workspace: request.DrydockRoot, Phase: "reserved"}
	all, err := b.inventory(ctx)
	if err != nil {
		return result, err
	}
	if _, found, err := sbxFind(all, record.Name); err != nil || found {
		return result, ErrSBXOwnership
	}
	if err := b.save(record); err != nil {
		return result, err
	}
	cleanup := func(cause error) (SBXExecutionResult, error) {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		cleanupErr := b.removeOwned(cleanupCtx, &record)
		result.TreeReaped = cleanupErr == nil && record.ID != ""
		if result.TreeReaped {
			result.ReceiptFingerprint = sbxDigest(record.OperationDigest, record.RequestFingerprint, record.ID, "removed")
		}
		return result, errors.Join(cause, cleanupErr)
	}
	if err := request.AuthorityCheck(ctx); err != nil {
		record.Phase = "unused"
		_ = b.save(record)
		return result, err
	}
	// SBX shares the actual host workspace. An existing hard link could alias
	// an inode outside the granted workspace even when path checks succeed.
	// Check after the last authority callback, immediately before VM creation.
	if err := sbxValidateWorkspaceForSource(ctx, record.Workspace, request.SourceRepository); err != nil {
		record.Phase = "unused"
		_ = b.save(record)
		return result, err
	}
	if err := b.verifyMCPHelper(ctx); err != nil {
		record.Phase = "unused"
		_ = b.save(record)
		return result, err
	}
	createCtx, cancelCreate := context.WithTimeout(ctx, 2*time.Minute)
	result.TreeReaped = false
	created, createErr := b.call(createCtx, []string{"create", "--name", record.Name, "--template", b.config.TemplateReference,
		"--pull", "never", "--skills", "off", "--static-mcp", b.helperName, "--deny-network", "**", "--cpus", "2", "--memory", "2g",
		"shell", record.Workspace, filepath.Join(record.Workspace, ".git") + ":ro"}, nil, 64*1024)
	cancelCreate()
	// A failed/lost create reply never grants execution. Without a successfully
	// journaled immutable ID, cleanup stays unresolved rather than adopting a VM.
	if createErr != nil || created.ExitCode != 0 {
		return cleanup(errors.Join(createErr, ErrSBXCLI))
	}
	identityCtx, cancelIdentity := context.WithTimeout(context.Background(), 8*time.Second)
	all, err = b.inventory(identityCtx)
	cancelIdentity()
	if err != nil {
		return cleanup(err)
	}
	entry, found, err := sbxFind(all, record.Name)
	if err != nil || !found || !sbxMountsMatch(entry, record) {
		return cleanup(ErrSBXOwnership)
	}
	record.ID, record.Phase = entry.ID, "created"
	if err := b.save(record); err != nil {
		return cleanup(err)
	}
	// No env/credential/kit flags are forwarded. env -i removes template
	// sentinels as well as keys. Readiness must independently seal MCP access.
	// The successful create invocation includes the per-sandbox deny-all rule.
	// Re-adding the same rule would rely on undocumented duplicate-rule behavior.
	if err := request.AuthorityCheck(ctx); err != nil {
		return cleanup(err)
	}
	// The namespace owner serializes all cooperating product instances. Check
	// the saved identity once more immediately before the name-only exec API.
	all, err = b.inventory(ctx)
	if err != nil {
		return cleanup(err)
	}
	entry, found, err = sbxFind(all, record.Name)
	if err != nil || !found || entry.ID != record.ID || !sbxMountsMatch(entry, record) {
		return cleanup(ErrSBXOwnership)
	}
	args := []string{"exec"}
	if stdin != nil {
		args = append(args, "--interactive")
	}
	args = append(args, record.Name, "/bin/bash", "--noprofile", "--norc", "-p", "-c",
		sbxGuestEnvironmentGuard, "traverse-sbx-env")
	args = append(args, request.Environment...)
	args = append(args, "/bin/bash", "--noprofile", "--norc", "-c",
		sbxGuestWorkspaceCommand, "traverse-sbx", request.WorkingDirectory)
	args = append(args, request.Arguments...)
	record.Phase = "dispatching"
	if err := b.save(record); err != nil {
		return cleanup(err)
	}
	executionCtx, cancel := context.WithTimeout(ctx, request.Timeout)
	if request.BeforeExec != nil {
		if err := request.BeforeExec(executionCtx); err != nil {
			cancel()
			return cleanup(err)
		}
	}
	executed, runErr := b.call(executionCtx, args, stdin, request.OutputLimit)
	cancel()
	result.ExitCode, result.Stdout, result.Stderr = executed.ExitCode, executed.Stdout, executed.Stderr
	return cleanup(runErr)
}

func (b *SBXBackend) validateRequest(r SBXRunRequest) error {
	if !b.config.Enabled || !ValidSBXTemplateReference(b.config.TemplateReference) || r.AuthorityCheck == nil ||
		r.RuntimeGeneration != b.generation || r.OperationKey == "" || len(r.OperationKey) > 256 ||
		!sbxSHA(r.RequestFingerprint) || r.Timeout <= 0 || r.Timeout > 30*time.Minute ||
		r.OutputLimit < 4096 || r.OutputLimit > 4*1024*1024 || len(r.Arguments) == 0 || len(r.Arguments) > 129 ||
		!strings.HasPrefix(r.Arguments[0], "/") || strings.HasPrefix(r.Arguments[0], "//") ||
		r.Environment == nil || len(r.Environment) > 96 || filepath.IsAbs(r.WorkingDirectory) ||
		r.WorkingDirectory == "" || strings.Contains(r.WorkingDirectory, `\`) ||
		r.WorkingDirectory == ".." || strings.HasPrefix(r.WorkingDirectory, "../") {
		return ErrSBXBoundary
	}
	if _, err := sbxCanonical(r.DrydockRoot, true); err != nil {
		return err
	}
	if sbxWithin(r.DrydockRoot, b.config.JournalRoot) || sbxWithin(r.DrydockRoot, b.config.ExecutablePath) ||
		sbxWithin(r.DrydockRoot, b.config.HelperExecutable) {
		return ErrSBXBoundary
	}
	info, err := os.Lstat(filepath.Join(r.DrydockRoot, ".git"))
	if err != nil || (!info.Mode().IsRegular() && !(r.SourceRepository && info.IsDir())) {
		return ErrSBXBoundary
	}
	for _, values := range [][]string{r.Arguments, r.Environment} {
		for _, value := range values {
			if !utf8.ValidString(value) || strings.ContainsRune(value, 0) || len(value) > 64*1024 {
				return ErrSBXBoundary
			}
		}
	}
	return nil
}

func sbxWithin(root, target string) bool {
	if target == "" {
		return false
	}
	relative, err := filepath.Rel(root, target)
	return err == nil && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator))
}

func (b *SBXBackend) call(ctx context.Context, args []string, stdin io.Reader, limit int) (SBXProcessResult, error) {
	if ctx == nil {
		return SBXProcessResult{ExitCode: -1}, ErrSBXBoundary
	}
	if ctx.Err() != nil {
		return SBXProcessResult{ExitCode: -1}, errors.Join(ErrSBXCLI, ctx.Err())
	}
	if len(args) == 0 {
		return SBXProcessResult{ExitCode: -1}, ErrSBXBoundary
	}
	if args[0] != "create" && args[0] != "exec" {
		bounded, cancel := context.WithTimeout(ctx, 8*time.Second)
		defer cancel()
		ctx = bounded
	}
	digest, err := sbxFileDigest(b.config.ExecutablePath)
	if err != nil || digest != b.executableSHA || digest == "" {
		return SBXProcessResult{ExitCode: -1}, ErrSBXBoundary
	}
	cliArgs := append([]string{"--app-name", SBXAppName}, args...)
	value, err := b.transport.Run(ctx, SBXProcessRequest{Executable: b.config.ExecutablePath, Arguments: cliArgs,
		Directory: filepath.Dir(b.config.ExecutablePath), Environment: sbxHostEnvironment(), Stdin: stdin, OutputLimit: limit})
	if len(value.Stdout)+len(value.Stderr) > limit {
		return SBXProcessResult{ExitCode: -1}, ErrSBXOutputLimit
	}
	return value, err
}

func sbxDigest(parts ...string) string {
	data, _ := json.Marshal(parts)
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}
func sbxSHA(s string) bool {
	value, err := hex.DecodeString(s)
	return err == nil && len(value) == sha256.Size && strings.ToLower(s) == s
}
func sbxCanonical(value string, directory bool) (string, error) {
	if value == "" || !filepath.IsAbs(value) || filepath.Clean(value) != value || strings.ContainsRune(value, 0) {
		return "", ErrSBXBoundary
	}
	resolved, err := filepath.EvalSymlinks(value)
	if err != nil {
		return "", err
	}
	if resolved != value {
		return "", ErrSBXBoundary
	}
	info, err := os.Lstat(value)
	if err != nil {
		return "", err
	}
	if info.Mode()&os.ModeSymlink != 0 || (directory && !info.IsDir()) || (!directory && !info.Mode().IsRegular()) {
		return "", ErrSBXBoundary
	}
	return value, nil
}
func sbxFileDigest(value string) (string, error) {
	if _, err := sbxCanonical(value, false); err != nil {
		return "", err
	}
	file, err := os.Open(value)
	if err != nil {
		return "", err
	}
	defer file.Close()
	hash := sha256.New()
	if _, err := io.Copy(hash, file); err != nil {
		return "", err
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}
