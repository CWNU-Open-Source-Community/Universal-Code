package sandbox

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"cyberagent-workbench/internal/rootfs"
)

const (
	LocalReadinessProtocolVersion = "local_sandbox_readiness.v1"
	LocalExecutionProtocolVersion = "local_sandbox_execution.v1"
	LocalBackendName              = "windows_appcontainer"
	LocalReadinessTTL             = 30 * time.Second

	LocalReadinessReady       = "ready"
	LocalReadinessDisabled    = "disabled"
	LocalReadinessUnavailable = "unavailable"

	LocalReasonNone                    = "none"
	LocalReasonFeatureDisabled         = "feature_disabled"
	LocalReasonPlatformUnsupported     = "platform_unsupported"
	LocalReasonArchitectureUnsupported = "architecture_unsupported"
	LocalReasonAppContainerUnavailable = "appcontainer_unavailable"
	LocalReasonFilesystemUnavailable   = "filesystem_isolation_unavailable"
	LocalReasonProcessUnavailable      = "process_isolation_unavailable"
	LocalReasonNetworkUnavailable      = "network_isolation_unavailable"
	LocalReasonCredentialUnavailable   = "credential_isolation_unavailable"
	LocalReasonProbeFailed             = "conformance_probe_failed"

	LocalRemediationNone             = "none"
	LocalRemediationEnableFeature    = "enable_workspace_sandbox"
	LocalRemediationUseSupportedHost = "use_windows_10_11_x64"
	LocalRemediationRepairIsolation  = "repair_windows_appcontainer"
	LocalRemediationUseNTFS          = "move_drydock_to_acl_volume"
	LocalRemediationRetryProbe       = "retry_local_sandbox_readiness"

	LocalExecutionCompleted = "completed"
	LocalExecutionFailed    = "failed"
	LocalExecutionCancelled = "cancelled"
	LocalExecutionTimedOut  = "timed_out"

	DefaultLocalDiskWriteLimit int64 = 2 * 1024 * 1024 * 1024
	MaxLocalDiskWriteLimit     int64 = 8 * 1024 * 1024 * 1024
	MaxLocalToolchainInputs          = 16
	LocalAttachmentVirtualRoot       = "/attachments"
	LocalAttachmentEnvironment       = "TRAVERSE_ATTACHMENTS_DIR"
)

var (
	ErrLocalSandboxBoundary    = errors.New("local sandbox boundary is invalid")
	ErrLocalSandboxUnavailable = errors.New("local sandbox backend is unavailable")
	ErrLocalSandboxOutputLimit = errors.New("local sandbox output limit exceeded")
	ErrLocalSandboxWriteLimit  = errors.New("local sandbox write limit exceeded")
)

// A rejection before process creation has no command tree to reap. This is
// control-flow proof, not a complete execution receipt or an isolation grant.
func localNoProcessResult() LocalExecutionResult {
	return LocalExecutionResult{ExitCode: 125, TreeReaped: true}
}

// LocalRuntimeCapabilities are process-local startup grants. They are not Run
// authority and must never be inferred from a selected Profile or permission.
type LocalRuntimeCapabilities struct {
	Enabled bool
}

type LocalReadiness struct {
	ProtocolVersion          string    `json:"protocol_version"`
	PolicyVersion            string    `json:"policy_version"`
	Backend                  string    `json:"backend"`
	Status                   string    `json:"status"`
	ReasonCode               string    `json:"reason_code"`
	RemediationCode          string    `json:"remediation_code"`
	CheckedAt                time.Time `json:"checked_at"`
	ExpiresAt                time.Time `json:"expires_at"`
	EvidenceFingerprint      string    `json:"evidence_fingerprint"`
	RuntimeGeneration        string    `json:"runtime_generation"`
	FeatureEnabled           bool      `json:"feature_enabled"`
	WindowsHost              bool      `json:"windows_host"`
	X64Host                  bool      `json:"x64_host"`
	PersistentACLs           bool      `json:"persistent_acls"`
	AppContainerProfile      bool      `json:"appcontainer_profile"`
	AppContainerToken        bool      `json:"appcontainer_token"`
	RestrictedToken          bool      `json:"restricted_token"`
	LowIntegrityToken        bool      `json:"low_integrity_token"`
	ZeroNetworkCapabilities  bool      `json:"zero_network_capabilities"`
	WFPDefaultDeny           bool      `json:"wfp_default_deny"`
	CreationTimeJobBinding   bool      `json:"creation_time_job_binding"`
	KillOnJobClose           bool      `json:"kill_on_job_close"`
	BoundedHandleInheritance bool      `json:"bounded_handle_inheritance"`
	CleanEnvironment         bool      `json:"clean_environment"`
	EphemeralProfile         bool      `json:"ephemeral_profile"`
	FilesystemProven         bool      `json:"filesystem_proven"`
	ProcessProven            bool      `json:"process_proven"`
	NetworkProven            bool      `json:"network_proven"`
	CredentialProven         bool      `json:"credential_proven"`
	Ready                    bool      `json:"ready"`
	CapabilityGrant          bool      `json:"capability_grant"`
}

func (r LocalReadiness) Validate() error {
	if r.ProtocolVersion != LocalReadinessProtocolVersion ||
		r.PolicyVersion != LocalBackendPolicyVersion || r.Backend != LocalBackendName ||
		!validDigest(r.RuntimeGeneration) || !validDigest(r.EvidenceFingerprint) ||
		r.CheckedAt.IsZero() || !r.ExpiresAt.After(r.CheckedAt) ||
		r.ExpiresAt.Sub(r.CheckedAt) != LocalReadinessTTL || r.CapabilityGrant {
		return ErrLocalSandboxBoundary
	}
	if r.Status != LocalReadinessReady && r.Status != LocalReadinessDisabled &&
		r.Status != LocalReadinessUnavailable {
		return ErrLocalSandboxBoundary
	}
	if !validLocalReadinessDisposition(r) ||
		r.EvidenceFingerprint != localReadinessFingerprint(r) {
		return ErrLocalSandboxBoundary
	}
	return nil
}

func validLocalReadinessDisposition(r LocalReadiness) bool {
	if r.Status == LocalReadinessDisabled {
		return !r.FeatureEnabled && !r.Ready && r.ReasonCode == LocalReasonFeatureDisabled &&
			r.RemediationCode == LocalRemediationEnableFeature
	}
	if !r.FeatureEnabled {
		return false
	}
	proof := r.WindowsHost && r.X64Host && r.PersistentACLs && r.AppContainerProfile &&
		r.AppContainerToken && r.LowIntegrityToken &&
		r.ZeroNetworkCapabilities && r.WFPDefaultDeny && r.CreationTimeJobBinding &&
		r.KillOnJobClose && r.BoundedHandleInheritance && r.CleanEnvironment &&
		r.EphemeralProfile && r.FilesystemProven && r.ProcessProven &&
		r.NetworkProven && r.CredentialProven
	if r.Status == LocalReadinessReady {
		return r.Ready && proof && r.ReasonCode == LocalReasonNone &&
			r.RemediationCode == LocalRemediationNone
	}
	return !r.Ready && !proof && validLocalFailure(r.ReasonCode, r.RemediationCode)
}

func validLocalFailure(reason, remediation string) bool {
	switch reason {
	case LocalReasonPlatformUnsupported, LocalReasonArchitectureUnsupported:
		return remediation == LocalRemediationUseSupportedHost
	case LocalReasonFilesystemUnavailable:
		return remediation == LocalRemediationUseNTFS
	case LocalReasonAppContainerUnavailable, LocalReasonProcessUnavailable,
		LocalReasonNetworkUnavailable, LocalReasonCredentialUnavailable:
		return remediation == LocalRemediationRepairIsolation
	case LocalReasonProbeFailed:
		return remediation == LocalRemediationRetryProbe
	default:
		return false
	}
}

type LocalExecutionBinding struct {
	RunID                     string
	MissionID                 string
	SessionID                 string
	WorkspaceID               string
	DrydockID                 string
	DrydockRoot               string
	DrydockPathSHA256         string
	DrydockRootFingerprint    string
	DrydockBindingFingerprint string
	DrydockGeneration         int64
	PermissionSnapshotID      string
	PermissionRevision        int64
	ProfileSnapshotID         string
	ProfileRevision           int64
	InteractionSnapshotID     string
	InteractionRevision       int64
	CapabilityGeneration      string
	LeaseID                   string
	LeaseGeneration           int64
	OperationKeySHA256        string
	RuntimeGeneration         string
}

func (b LocalExecutionBinding) Validate() error {
	for _, value := range []string{b.RunID, b.MissionID, b.SessionID, b.WorkspaceID,
		b.DrydockID, b.PermissionSnapshotID, b.ProfileSnapshotID,
		b.InteractionSnapshotID, b.LeaseID} {
		if !validLocalIdentity(value) {
			return ErrLocalSandboxBoundary
		}
	}
	if !validLocalHostRoot(b.DrydockRoot) ||
		localHostPathDigest(b.DrydockRoot) != b.DrydockPathSHA256 {
		return ErrLocalSandboxBoundary
	}
	for _, digest := range []string{b.DrydockPathSHA256, b.DrydockRootFingerprint,
		b.DrydockBindingFingerprint, b.CapabilityGeneration, b.OperationKeySHA256,
		b.RuntimeGeneration} {
		if !validDigest(digest) {
			return ErrLocalSandboxBoundary
		}
	}
	if b.DrydockGeneration < 1 || b.PermissionRevision < 1 || b.ProfileRevision < 1 ||
		b.InteractionRevision < 1 || b.LeaseGeneration < 1 {
		return ErrLocalSandboxBoundary
	}
	return nil
}

type LocalToolchainInput struct {
	ID          string
	Root        string
	VirtualRoot string
	RootSHA256  string
	// DataOnly reuses the pinned read-only input boundary for sent attachments.
	// It cannot supply the launch executable or participate in PATH lookup.
	DataOnly bool
}

func (i LocalToolchainInput) Validate() error {
	return validateLocalToolchainInput(i)
}

func validateLocalToolchainInput(i LocalToolchainInput) error {
	if !validLocalIdentity(i.ID) || !validLocalHostRoot(i.Root) ||
		localHostPathDigest(i.Root) != i.RootSHA256 ||
		validateLocalVirtualRoot(i.VirtualRoot) != nil || i.VirtualRoot == "/workspace" ||
		strings.HasPrefix(i.VirtualRoot, "/workspace/") ||
		(i.DataOnly && i.VirtualRoot != LocalAttachmentVirtualRoot) {
		return ErrLocalSandboxBoundary
	}
	return nil
}

type LocalRunRequest struct {
	Manifest          Manifest
	Binding           LocalExecutionBinding
	ToolchainInputs   []LocalToolchainInput
	MaxDiskWriteBytes int64
	StdinPipe         bool
	// Instrumentation requests the system lpacInstrumentation capability for
	// a trusted PowerShell execution profile. It is not a general capability
	// list, a network grant, or a guarantee of access limited to ETW registration.
	Instrumentation bool
}

func (r LocalRunRequest) Validate() error {
	manifest, err := NormalizeManifest(r.Manifest)
	if err != nil || manifest.Backend != BackendLocal || r.Binding.Validate() != nil ||
		len(r.ToolchainInputs) == 0 || len(r.ToolchainInputs) > MaxLocalToolchainInputs ||
		len(manifest.Mounts) != 1 ||
		manifest.Network.Mode != "disabled" || len(manifest.Network.AllowedTargets) != 0 ||
		manifest.SecretReferenceCount() != 0 || len(manifest.InputArtifactIDs) != 0 ||
		manifest.WritableMountCount() != 1 ||
		r.MaxDiskWriteBytes < manifest.Resources.MaxOutputBytes ||
		r.MaxDiskWriteBytes > MaxLocalDiskWriteLimit {
		return ErrLocalSandboxBoundary
	}
	workspaceMount := -1
	for index, mount := range manifest.Mounts {
		if mount.Access == MountReadWrite {
			workspaceMount = index
		}
	}
	if workspaceMount < 0 || manifest.Mounts[workspaceMount].Source != "." ||
		manifest.Mounts[workspaceMount].Target != "/workspace" ||
		!pathWithin(manifest.Command.WorkingDirectory, "/workspace") ||
		rootfs.CheckExternalDirectory(manifest.Command.WorkingDirectory) != nil {
		return ErrLocalSandboxBoundary
	}
	seenIDs := make(map[string]struct{}, len(r.ToolchainInputs))
	seenRoots := make(map[string]struct{}, len(r.ToolchainInputs))
	seenTargets := make(map[string]struct{}, len(r.ToolchainInputs))
	commandCovered := false
	for _, input := range r.ToolchainInputs {
		if input.Validate() != nil {
			return ErrLocalSandboxBoundary
		}
		rootKey := strings.ToLower(filepath.Clean(input.Root))
		if _, ok := seenIDs[input.ID]; ok {
			return ErrLocalSandboxBoundary
		}
		if _, ok := seenRoots[rootKey]; ok {
			return ErrLocalSandboxBoundary
		}
		for target := range seenTargets {
			if pathWithin(input.VirtualRoot, target) ||
				pathWithin(target, input.VirtualRoot) {
				return ErrLocalSandboxBoundary
			}
		}
		seenIDs[input.ID], seenRoots[rootKey], seenTargets[input.VirtualRoot] =
			struct{}{}, struct{}{}, struct{}{}
		if localRootsOverlapPortable(input.Root, r.Binding.DrydockRoot) {
			return ErrLocalSandboxBoundary
		}
		for otherRoot := range seenRoots {
			if otherRoot != rootKey && localRootsOverlapPortable(input.Root, otherRoot) {
				return ErrLocalSandboxBoundary
			}
		}
		if !input.DataOnly && pathWithin(manifest.Command.Executable, input.VirtualRoot) {
			commandCovered = true
		}
	}
	if !commandCovered {
		return ErrLocalSandboxBoundary
	}
	for _, output := range manifest.Output.Paths {
		if !pathWithin(output, "/workspace") {
			return ErrLocalSandboxBoundary
		}
	}
	for _, binding := range manifest.Environment {
		if binding.Source != EnvironmentLiteral || localSensitiveEnvironmentName(binding.Name) {
			return ErrLocalSandboxBoundary
		}
	}
	return nil
}

func NormalizeLocalRunRequest(request LocalRunRequest) (LocalRunRequest, error) {
	normalized, err := NormalizeManifest(request.Manifest)
	if err != nil {
		return LocalRunRequest{}, err
	}
	request.Manifest = normalized
	request.ToolchainInputs = append([]LocalToolchainInput(nil), request.ToolchainInputs...)
	sort.Slice(request.ToolchainInputs, func(i, j int) bool {
		return request.ToolchainInputs[i].ID < request.ToolchainInputs[j].ID
	})
	if request.MaxDiskWriteBytes == 0 {
		request.MaxDiskWriteBytes = DefaultLocalDiskWriteLimit
	}
	if err := request.Validate(); err != nil {
		return LocalRunRequest{}, err
	}
	return request, nil
}

type LocalCapturedOutput struct {
	Data          []byte `json:"-"`
	ObservedBytes int64  `json:"observed_bytes"`
	CapturedBytes int    `json:"captured_bytes"`
	SHA256        string `json:"sha256"`
	Truncated     bool   `json:"truncated"`
}

func (o LocalCapturedOutput) Validate(maximum int64) error {
	if maximum < 1 || o.ObservedBytes < 0 || o.ObservedBytes > maximum+1 ||
		o.CapturedBytes != len(o.Data) || int64(o.CapturedBytes) > o.ObservedBytes ||
		!validDigest(o.SHA256) || o.Truncated != (o.ObservedBytes > int64(o.CapturedBytes)) {
		return ErrLocalSandboxBoundary
	}
	digest := sha256.Sum256(o.Data)
	if hex.EncodeToString(digest[:]) != o.SHA256 {
		return ErrLocalSandboxBoundary
	}
	return nil
}

type LocalExecutionResult struct {
	ProtocolVersion          string              `json:"protocol_version"`
	PolicyVersion            string              `json:"policy_version"`
	Instrumentation          bool                `json:"instrumentation"`
	Backend                  string              `json:"backend"`
	Status                   string              `json:"status"`
	ExitCode                 int                 `json:"exit_code"`
	Stdout                   LocalCapturedOutput `json:"stdout"`
	Stderr                   LocalCapturedOutput `json:"stderr"`
	StartedAt                time.Time           `json:"started_at"`
	CompletedAt              time.Time           `json:"completed_at"`
	EvidenceFingerprint      string              `json:"evidence_fingerprint"`
	RuntimeGeneration        string              `json:"runtime_generation"`
	BindingFingerprint       string              `json:"binding_fingerprint"`
	ManifestFingerprint      string              `json:"manifest_fingerprint"`
	ProfileFingerprint       string              `json:"profile_fingerprint"`
	TimedOut                 bool                `json:"timed_out"`
	Cancelled                bool                `json:"cancelled"`
	OutputLimitExceeded      bool                `json:"output_limit_exceeded"`
	WriteLimitExceeded       bool                `json:"write_limit_exceeded"`
	TreeReaped               bool                `json:"tree_reaped"`
	DrydockReadWrite         bool                `json:"drydock_read_write"`
	ToolchainsReadOnly       bool                `json:"toolchains_read_only"`
	ReparseBoundary          bool                `json:"reparse_boundary"`
	DiskWritesBound          bool                `json:"disk_writes_bound"`
	OwnerRecoveryBound       bool                `json:"owner_recovery_bound"`
	StdinClosed              bool                `json:"stdin_closed"`
	CPUQuotaBound            bool                `json:"cpu_quota_bound"`
	MemoryBound              bool                `json:"memory_bound"`
	ProcessCountBound        bool                `json:"process_count_bound"`
	RuntimeBound             bool                `json:"runtime_bound"`
	OutputBound              bool                `json:"output_bound"`
	ArtifactBound            bool                `json:"artifact_bound"`
	AppContainerToken        bool                `json:"appcontainer_token"`
	RestrictedToken          bool                `json:"restricted_token"`
	LowIntegrityToken        bool                `json:"low_integrity_token"`
	ZeroNetworkCapabilities  bool                `json:"zero_network_capabilities"`
	WFPDefaultDeny           bool                `json:"wfp_default_deny"`
	CreationTimeJobBinding   bool                `json:"creation_time_job_binding"`
	KillOnJobClose           bool                `json:"kill_on_job_close"`
	BoundedHandleInheritance bool                `json:"bounded_handle_inheritance"`
	EnvironmentInherited     bool                `json:"environment_inherited"`
	CredentialMaterialized   bool                `json:"credential_materialized"`
	ProfileDeleted           bool                `json:"profile_deleted"`
	ACLsRestored             bool                `json:"acls_restored"`
	CapabilityGrant          bool                `json:"capability_grant"`
}

func (r LocalExecutionResult) Validate(request LocalRunRequest) error {
	normalized, err := NormalizeLocalRunRequest(request)
	if err != nil {
		return err
	}
	manifestFingerprint, _ := normalized.Manifest.Fingerprint()
	if r.ProtocolVersion != LocalExecutionProtocolVersion ||
		r.PolicyVersion != LocalBackendPolicyVersion || r.Backend != LocalBackendName ||
		r.Instrumentation != normalized.Instrumentation ||
		!validDigest(r.RuntimeGeneration) || r.RuntimeGeneration != normalized.Binding.RuntimeGeneration ||
		!validDigest(r.BindingFingerprint) ||
		r.BindingFingerprint != localExecutionBindingFingerprint(normalized) ||
		r.ManifestFingerprint != manifestFingerprint || !validDigest(r.ProfileFingerprint) ||
		!validDigest(r.EvidenceFingerprint) || r.StartedAt.IsZero() ||
		r.CompletedAt.Before(r.StartedAt) || r.CapabilityGrant ||
		!r.TreeReaped || !r.DrydockReadWrite || !r.ToolchainsReadOnly ||
		!r.ReparseBoundary || !r.DiskWritesBound || !r.OwnerRecoveryBound ||
		!r.StdinClosed || !r.CPUQuotaBound || !r.MemoryBound ||
		!r.ProcessCountBound || !r.RuntimeBound || !r.OutputBound ||
		!r.ArtifactBound || !r.AppContainerToken ||
		!r.LowIntegrityToken || !r.ZeroNetworkCapabilities || !r.WFPDefaultDeny ||
		!r.CreationTimeJobBinding || !r.KillOnJobClose || !r.BoundedHandleInheritance ||
		r.EnvironmentInherited || r.CredentialMaterialized || !r.ProfileDeleted ||
		!r.ACLsRestored || (r.TimedOut && r.Cancelled) {
		return ErrLocalSandboxBoundary
	}
	if r.Stdout.Validate(normalized.Manifest.Resources.MaxOutputBytes) != nil ||
		r.Stderr.Validate(normalized.Manifest.Resources.MaxOutputBytes) != nil {
		return ErrLocalSandboxBoundary
	}
	observed := r.Stdout.ObservedBytes + r.Stderr.ObservedBytes
	if observed < 0 || observed > normalized.Manifest.Resources.MaxOutputBytes+1 ||
		r.OutputLimitExceeded != (observed > normalized.Manifest.Resources.MaxOutputBytes) {
		return ErrLocalSandboxBoundary
	}
	expectedStatus := LocalExecutionCompleted
	if r.TimedOut {
		expectedStatus = LocalExecutionTimedOut
	} else if r.Cancelled {
		expectedStatus = LocalExecutionCancelled
	} else if r.ExitCode != 0 || r.OutputLimitExceeded || r.WriteLimitExceeded {
		expectedStatus = LocalExecutionFailed
	}
	if r.Status != expectedStatus || r.EvidenceFingerprint != localExecutionFingerprint(r) {
		return ErrLocalSandboxBoundary
	}
	return nil
}

type LocalBackend interface {
	Generation() string
	Readiness(context.Context, LocalRuntimeCapabilities) (LocalReadiness, error)
	Run(context.Context, LocalRunRequest) (LocalExecutionResult, error)
	RunWithStdin(context.Context, LocalRunRequest, io.ReadCloser) (LocalExecutionResult, error)
	Close() error
}

type localBackendConfig struct {
	ownerRoot string
}

type LocalBackendOption func(*localBackendConfig) error

// WithLocalOwnerRoot overrides the private recovery journal directory. It is
// intended for tests and portable installations; the directory is never
// exposed through readiness or execution evidence.
func WithLocalOwnerRoot(root string) LocalBackendOption {
	return func(config *localBackendConfig) error {
		if config == nil || !validLocalHostRoot(root) {
			return ErrLocalSandboxBoundary
		}
		config.ownerRoot = root
		return nil
	}
}

func NewPlatformLocalBackend(options ...LocalBackendOption) (LocalBackend, error) {
	config := localBackendConfig{}
	for _, option := range options {
		if option == nil {
			return nil, ErrLocalSandboxBoundary
		}
		if err := option(&config); err != nil {
			return nil, err
		}
	}
	return newPlatformLocalBackend(config)
}

func localExecutionBindingFingerprint(request LocalRunRequest) string {
	b := request.Binding
	parts := []string{LocalExecutionProtocolVersion, b.RunID, b.MissionID, b.SessionID,
		b.WorkspaceID, b.DrydockID, b.DrydockPathSHA256, b.DrydockRootFingerprint,
		b.DrydockBindingFingerprint, fmt.Sprint(b.DrydockGeneration), b.PermissionSnapshotID,
		fmt.Sprint(b.PermissionRevision), b.ProfileSnapshotID, fmt.Sprint(b.ProfileRevision),
		b.InteractionSnapshotID, fmt.Sprint(b.InteractionRevision), b.CapabilityGeneration,
		b.LeaseID, fmt.Sprint(b.LeaseGeneration), b.OperationKeySHA256, b.RuntimeGeneration,
		fmt.Sprint(request.MaxDiskWriteBytes), fmt.Sprint(request.StdinPipe),
		LocalBackendPolicyVersion, fmt.Sprint(request.Instrumentation)}
	for _, input := range request.ToolchainInputs {
		parts = append(parts, input.ID, input.RootSHA256, input.VirtualRoot)
		if input.DataOnly {
			parts = append(parts, "data_only=true")
		}
	}
	return localFingerprint(parts...)
}

func localReadinessFingerprint(r LocalReadiness) string {
	return localFingerprint(LocalReadinessProtocolVersion, r.PolicyVersion, r.Backend,
		r.Status, r.ReasonCode, r.RemediationCode, r.CheckedAt.Format(time.RFC3339Nano),
		r.ExpiresAt.Format(time.RFC3339Nano), r.RuntimeGeneration,
		fmt.Sprint(r.FeatureEnabled), fmt.Sprint(r.WindowsHost), fmt.Sprint(r.X64Host),
		fmt.Sprint(r.PersistentACLs), fmt.Sprint(r.AppContainerProfile),
		fmt.Sprint(r.AppContainerToken), fmt.Sprint(r.RestrictedToken),
		fmt.Sprint(r.LowIntegrityToken), fmt.Sprint(r.ZeroNetworkCapabilities),
		fmt.Sprint(r.WFPDefaultDeny), fmt.Sprint(r.CreationTimeJobBinding),
		fmt.Sprint(r.KillOnJobClose), fmt.Sprint(r.BoundedHandleInheritance),
		fmt.Sprint(r.CleanEnvironment), fmt.Sprint(r.EphemeralProfile),
		fmt.Sprint(r.FilesystemProven), fmt.Sprint(r.ProcessProven),
		fmt.Sprint(r.NetworkProven), fmt.Sprint(r.CredentialProven),
		fmt.Sprint(r.Ready), "capability_grant=false")
}

func localExecutionFingerprint(r LocalExecutionResult) string {
	return localFingerprint(LocalExecutionProtocolVersion, r.PolicyVersion, r.Backend,
		fmt.Sprint(r.Instrumentation),
		r.Status, fmt.Sprint(r.ExitCode), r.Stdout.SHA256,
		fmt.Sprint(r.Stdout.ObservedBytes), fmt.Sprint(r.Stdout.CapturedBytes),
		fmt.Sprint(r.Stdout.Truncated), r.Stderr.SHA256,
		fmt.Sprint(r.Stderr.ObservedBytes), fmt.Sprint(r.Stderr.CapturedBytes),
		fmt.Sprint(r.Stderr.Truncated), r.StartedAt.Format(time.RFC3339Nano),
		r.CompletedAt.Format(time.RFC3339Nano), r.RuntimeGeneration,
		r.BindingFingerprint, r.ManifestFingerprint, r.ProfileFingerprint,
		fmt.Sprint(r.TimedOut), fmt.Sprint(r.Cancelled),
		fmt.Sprint(r.OutputLimitExceeded), fmt.Sprint(r.WriteLimitExceeded),
		fmt.Sprint(r.TreeReaped), fmt.Sprint(r.DrydockReadWrite),
		fmt.Sprint(r.ToolchainsReadOnly), fmt.Sprint(r.ReparseBoundary),
		fmt.Sprint(r.DiskWritesBound), fmt.Sprint(r.OwnerRecoveryBound),
		fmt.Sprint(r.StdinClosed), fmt.Sprint(r.CPUQuotaBound),
		fmt.Sprint(r.MemoryBound), fmt.Sprint(r.ProcessCountBound),
		fmt.Sprint(r.RuntimeBound), fmt.Sprint(r.OutputBound),
		fmt.Sprint(r.ArtifactBound), fmt.Sprint(r.AppContainerToken),
		fmt.Sprint(r.RestrictedToken), fmt.Sprint(r.LowIntegrityToken),
		fmt.Sprint(r.ZeroNetworkCapabilities), fmt.Sprint(r.WFPDefaultDeny),
		fmt.Sprint(r.CreationTimeJobBinding), fmt.Sprint(r.KillOnJobClose),
		fmt.Sprint(r.BoundedHandleInheritance), fmt.Sprint(r.EnvironmentInherited),
		fmt.Sprint(r.CredentialMaterialized), fmt.Sprint(r.ProfileDeleted),
		fmt.Sprint(r.ACLsRestored), "capability_grant=false")
}

func localHostPathDigest(value string) string {
	return localFingerprint("local-host-path.v1", strings.ToLower(filepath.Clean(value)))
}

func LocalHostPathDigest(value string) (string, error) {
	if !validLocalHostRoot(value) {
		return "", ErrLocalSandboxBoundary
	}
	return localHostPathDigest(value), nil
}

func localFingerprint(parts ...string) string {
	hash := sha256.New()
	for _, part := range parts {
		_, _ = fmt.Fprintf(hash, "%d:", len([]byte(part)))
		_, _ = hash.Write([]byte(part))
		_, _ = hash.Write([]byte{'|'})
	}
	return hex.EncodeToString(hash.Sum(nil))
}

func newLocalRuntimeGeneration(parts ...string) (string, error) {
	random := make([]byte, 32)
	if _, err := rand.Read(random); err != nil {
		return "", err
	}
	return localFingerprint(append(parts, hex.EncodeToString(random))...), nil
}

func validLocalIdentity(value string) bool {
	return value != "" && value == strings.TrimSpace(value) && utf8.ValidString(value) &&
		len([]rune(value)) <= 256 && !strings.ContainsRune(value, 0)
}

func validLocalHostRoot(value string) bool {
	if value == "" || value != strings.TrimSpace(value) || !utf8.ValidString(value) ||
		strings.ContainsRune(value, 0) || !filepath.IsAbs(value) || filepath.Clean(value) != value ||
		strings.HasPrefix(value, `\\`) || strings.HasPrefix(value, `\\?\`) ||
		strings.HasPrefix(value, `\\.\`) || len(value) < 3 || value[1] != ':' ||
		(value[2] != '\\' && value[2] != '/') || !unicode.IsLetter(rune(value[0])) {
		return false
	}
	return true
}

func validateLocalVirtualRoot(value string) error {
	if value == "" || value == "/" || !strings.HasPrefix(value, "/") ||
		strings.Contains(value, `\`) || path.Clean(value) != value ||
		!utf8.ValidString(value) || len(value) > 1024 {
		return ErrLocalSandboxBoundary
	}
	return nil
}

func localSensitiveEnvironmentName(value string) bool {
	upper := strings.ToUpper(strings.TrimSpace(value))
	if secretNamePattern.MatchString(value) {
		return true
	}
	switch upper {
	case LocalAttachmentEnvironment, "HOME", "USERPROFILE", "HOMEDRIVE", "HOMEPATH", "LOCALAPPDATA", "APPDATA",
		"PATH", "PATHEXT", "SYSTEMROOT", "WINDIR", "COMSPEC", "SYSTEMDRIVE",
		"TEMP", "TMP", "PROGRAMDATA", "USERNAME", "USERDOMAIN",
		"SSH_AUTH_SOCK", "SSH_AGENT_PID", "GIT_ASKPASS", "GCM_INTERACTIVE",
		"AWS_SHARED_CREDENTIALS_FILE", "AWS_CONFIG_FILE", "AZURE_CONFIG_DIR",
		"GOOGLE_APPLICATION_CREDENTIALS", "KUBECONFIG", "DOCKER_CONFIG",
		"NPM_TOKEN", "HTTP_PROXY", "HTTPS_PROXY", "ALL_PROXY", "NO_PROXY":
		return true
	default:
		return strings.Contains(upper, "CREDENTIAL") || strings.Contains(upper, "TOKEN") ||
			strings.Contains(upper, "SECRET") || strings.Contains(upper, "PASSWORD")
	}
}

func localRootsOverlapPortable(first, second string) bool {
	first = strings.TrimSuffix(strings.ToLower(filepath.Clean(first)), string(filepath.Separator))
	second = strings.TrimSuffix(strings.ToLower(filepath.Clean(second)), string(filepath.Separator))
	return first == second || strings.HasPrefix(first, second+string(filepath.Separator)) ||
		strings.HasPrefix(second, first+string(filepath.Separator))
}
