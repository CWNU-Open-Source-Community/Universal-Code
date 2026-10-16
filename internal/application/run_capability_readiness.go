package application

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"cyberagent-workbench/internal/apperror"
	"cyberagent-workbench/internal/commandruntimeadapter"
	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/runworktree"
	"cyberagent-workbench/internal/sandbox"
	"cyberagent-workbench/internal/toolgateway"
)

const RunCapabilityReadinessProtocolVersion = "run_capability_readiness.v1"

type CapabilityReadinessBlocker string

const (
	CapabilityBlockerRunNotQuiescent         CapabilityReadinessBlocker = "run_not_quiescent"
	CapabilityBlockerExecutionLeaseActive    CapabilityReadinessBlocker = "execution_lease_active"
	CapabilityBlockerStartupGateClosed       CapabilityReadinessBlocker = "startup_gate_closed"
	CapabilityBlockerCapabilityUnimplemented CapabilityReadinessBlocker = "capability_not_implemented"
	CapabilityBlockerBackendNotReady         CapabilityReadinessBlocker = "backend_not_ready"
	CapabilityBlockerSurfaceMismatch         CapabilityReadinessBlocker = "surface_mismatch"
	CapabilityBlockerProfileMismatch         CapabilityReadinessBlocker = "profile_mismatch"
	CapabilityBlockerPermissionMismatch      CapabilityReadinessBlocker = "permission_mismatch"
	CapabilityBlockerWorkspaceUntrusted      CapabilityReadinessBlocker = "workspace_untrusted"
	CapabilityBlockerSandboxUnproven         CapabilityReadinessBlocker = "sandbox_unproven"
	CapabilityBlockerDockerUnavailable       CapabilityReadinessBlocker = "docker_unavailable"
	CapabilityBlockerSBXUnavailable          CapabilityReadinessBlocker = "sbx_unavailable"
)

type CapabilityReadinessRemediation string

const (
	CapabilityRemediationPauseRun                 CapabilityReadinessRemediation = "pause_run"
	CapabilityRemediationCreateNewRun             CapabilityReadinessRemediation = "create_new_run"
	CapabilityRemediationWaitForExecutionLease    CapabilityReadinessRemediation = "wait_for_execution_lease"
	CapabilityRemediationRestartWithStartupGate   CapabilityReadinessRemediation = "restart_with_startup_gate"
	CapabilityRemediationUpgradeApplication       CapabilityReadinessRemediation = "upgrade_application"
	CapabilityRemediationRetryBackendReadiness    CapabilityReadinessRemediation = "retry_backend_readiness"
	CapabilityRemediationSelectRequiredSurface    CapabilityReadinessRemediation = "select_required_surface"
	CapabilityRemediationSelectRequiredProfile    CapabilityReadinessRemediation = "select_required_profile"
	CapabilityRemediationSelectRequiredPermission CapabilityReadinessRemediation = "select_required_permission"
	CapabilityRemediationTrustWorkspace           CapabilityReadinessRemediation = "trust_workspace"
	CapabilityRemediationVerifySandbox            CapabilityReadinessRemediation = "verify_sandbox"
	CapabilityRemediationInstallOrStartDocker     CapabilityReadinessRemediation = "install_or_start_docker"
	CapabilityRemediationInstallOrStartSBX        CapabilityReadinessRemediation = "install_or_start_sbx"
)

const StandardCodePresetValue = "standard_code"

var capabilityBlockerOrder = map[CapabilityReadinessBlocker]int{
	CapabilityBlockerRunNotQuiescent: 0, CapabilityBlockerExecutionLeaseActive: 1,
	CapabilityBlockerStartupGateClosed: 2, CapabilityBlockerCapabilityUnimplemented: 3,
	CapabilityBlockerSurfaceMismatch: 4, CapabilityBlockerProfileMismatch: 5,
	CapabilityBlockerPermissionMismatch: 6, CapabilityBlockerWorkspaceUntrusted: 7,
	CapabilityBlockerSandboxUnproven: 8, CapabilityBlockerDockerUnavailable: 9,
	CapabilityBlockerBackendNotReady: 10, CapabilityBlockerSBXUnavailable: 11,
}

var capabilityRemediationOrder = map[CapabilityReadinessRemediation]int{
	CapabilityRemediationPauseRun: 0, CapabilityRemediationCreateNewRun: 1,
	CapabilityRemediationWaitForExecutionLease:    2,
	CapabilityRemediationRestartWithStartupGate:   3,
	CapabilityRemediationUpgradeApplication:       4,
	CapabilityRemediationSelectRequiredSurface:    5,
	CapabilityRemediationSelectRequiredProfile:    6,
	CapabilityRemediationSelectRequiredPermission: 7,
	CapabilityRemediationTrustWorkspace:           8, CapabilityRemediationVerifySandbox: 9,
	CapabilityRemediationInstallOrStartDocker:  10,
	CapabilityRemediationRetryBackendReadiness: 11, CapabilityRemediationInstallOrStartSBX: 12,
}

var capabilityRemediationsByBlocker = map[CapabilityReadinessBlocker][]CapabilityReadinessRemediation{
	CapabilityBlockerRunNotQuiescent:         {CapabilityRemediationPauseRun, CapabilityRemediationCreateNewRun},
	CapabilityBlockerExecutionLeaseActive:    {CapabilityRemediationWaitForExecutionLease},
	CapabilityBlockerStartupGateClosed:       {CapabilityRemediationRestartWithStartupGate},
	CapabilityBlockerCapabilityUnimplemented: {CapabilityRemediationUpgradeApplication},
	CapabilityBlockerSurfaceMismatch:         {CapabilityRemediationSelectRequiredSurface},
	CapabilityBlockerProfileMismatch:         {CapabilityRemediationSelectRequiredProfile},
	CapabilityBlockerPermissionMismatch:      {CapabilityRemediationSelectRequiredPermission},
	CapabilityBlockerWorkspaceUntrusted:      {CapabilityRemediationTrustWorkspace},
	CapabilityBlockerSandboxUnproven:         {CapabilityRemediationVerifySandbox},
	CapabilityBlockerDockerUnavailable:       {CapabilityRemediationInstallOrStartDocker},
	CapabilityBlockerSBXUnavailable:          {CapabilityRemediationInstallOrStartSBX},
	CapabilityBlockerBackendNotReady:         {CapabilityRemediationRetryBackendReadiness},
}

var capabilityRuntimeFailureBlockers = map[CapabilityReadinessBlocker]struct{}{
	CapabilityBlockerCapabilityUnimplemented: {}, CapabilityBlockerSurfaceMismatch: {},
	CapabilityBlockerProfileMismatch: {}, CapabilityBlockerPermissionMismatch: {},
	CapabilityBlockerWorkspaceUntrusted: {}, CapabilityBlockerSandboxUnproven: {},
	CapabilityBlockerDockerUnavailable: {}, CapabilityBlockerBackendNotReady: {},
	CapabilityBlockerSBXUnavailable: {},
}

type CapabilityReadinessOption struct {
	Value            string                           `json:"value"`
	Selected         bool                             `json:"selected"`
	Selectable       bool                             `json:"selectable"`
	RuntimeAvailable bool                             `json:"runtime_available"`
	BlockedBy        []CapabilityReadinessBlocker     `json:"blocked_by"`
	Remediation      []CapabilityReadinessRemediation `json:"remediation"`
	RestartRequired  bool                             `json:"restart_required"`
}

type RunCapabilityReadiness struct {
	ProtocolVersion       string                       `json:"protocol_version"`
	RunID                 string                       `json:"run_id"`
	Permissions           []CapabilityReadinessOption  `json:"permissions"`
	Profiles              []CapabilityReadinessOption  `json:"profiles"`
	Interactions          []CapabilityReadinessOption  `json:"interactions"`
	BrowserCDPPermissions []CapabilityReadinessOption  `json:"browser_cdp_permissions"`
	Presets               []CapabilityReadinessOption  `json:"presets"`
	CommandRuntime        CommandRuntimeReadiness      `json:"command_runtime"`
	UIEvidence            VerificationFeatureReadiness `json:"ui_evidence"`
	BatchValidation       VerificationFeatureReadiness `json:"batch_validation"`
	CapabilityGrant       bool                         `json:"capability_grant"`
}

// CommandRuntimeReadiness separates a compiled protocol, a process-installed
// backend, live backend readiness, and the non-authorizing fact that the
// current Run would receive the tool under its present durable snapshots.
type CommandRuntimeReadiness struct {
	ProtocolAvailable bool   `json:"protocol_available"`
	AdapterInstalled  bool   `json:"adapter_installed"`
	AdapterReady      bool   `json:"adapter_ready"`
	CurrentRunGranted bool   `json:"current_run_granted"`
	AdapterKind       string `json:"adapter_kind,omitempty"`
	Backend           string `json:"backend,omitempty"`
}

type CapabilityReadinessRuntime struct {
	RunControlEnabled                  bool
	RunExecutionEnabled                bool
	ExecutionPermissionControlEnabled  bool
	BrowserCDPPermissionControlEnabled bool
	StandardCodePresetEnabled          bool
	ExecutionPermissionCapabilities    domain.ExecutionPermissionRuntimeCapabilities
	BrowserCDPPermissionCapabilities   domain.BrowserCDPPermissionRuntimeCapabilities
	LocalSandboxInstalled              bool
	LocalSandboxProven                 bool
	LocalBackendReady                  bool
	DockerStartupGateEnabled           bool
	DockerAvailable                    bool
	DockerBackendReady                 bool
	DockerReadiness                    *sandbox.DockerReadiness
	SBXStartupGateEnabled              bool
	SBXAvailable                       bool
	SBXBackendReady                    bool
	BrowserBackendReady                bool
	CommandRuntimeAdapters             []commandruntimeadapter.Identity
	UIEvidenceInstalled                bool
	BatchValidationInstalled           bool
	VerificationSettingsControlEnabled bool
	VerificationSettingsReader         DesktopVerificationSettingsReader
}

// WithLocalSandboxReadiness consumes a validated, non-authorizing Local backend
// attestation. A current proof may open the generic Workspace Sandbox gate; an
// unavailable Local backend does not close a gate already opened explicitly for
// an independent fixed backend.
func (r CapabilityReadinessRuntime) WithLocalSandboxReadiness(
	readiness sandbox.LocalReadiness,
) (CapabilityReadinessRuntime, error) {
	if err := readiness.Validate(); err != nil {
		return CapabilityReadinessRuntime{}, fmt.Errorf(
			"local sandbox readiness: %w", err)
	}
	now := time.Now().UTC()
	current := !readiness.CheckedAt.After(now) && now.Before(readiness.ExpiresAt)
	ready := readiness.FeatureEnabled && readiness.Ready &&
		readiness.Status == sandbox.LocalReadinessReady && current
	// A fixed Docker backend may already supply the generic Workspace Sandbox
	// startup gate. A failed Local proof closes Local only; it must not erase an
	// independently configured Docker gate.
	r.ExecutionPermissionCapabilities.WorkspaceSandboxEnabled =
		r.ExecutionPermissionCapabilities.WorkspaceSandboxEnabled || ready
	r.LocalSandboxInstalled = ready || (readiness.FeatureEnabled &&
		readiness.ReasonCode != sandbox.LocalReasonPlatformUnsupported &&
		readiness.ReasonCode != sandbox.LocalReasonArchitectureUnsupported)
	r.LocalSandboxProven = ready
	r.LocalBackendReady = ready
	return r, nil
}

func (r CapabilityReadinessRuntime) Validate() error {
	if err := r.ExecutionPermissionCapabilities.Validate(); err != nil {
		return fmt.Errorf("execution permission capabilities: %w", err)
	}
	if err := r.BrowserCDPPermissionCapabilities.Validate(); err != nil {
		return fmt.Errorf("browser CDP capabilities: %w", err)
	}
	if r.BrowserCDPPermissionCapabilities.ControlEnabled !=
		r.BrowserCDPPermissionControlEnabled {
		return errors.New("browser CDP control and runtime capability must match")
	}
	if r.LocalSandboxProven && !r.LocalSandboxInstalled {
		return errors.New("a proven local sandbox must be installed")
	}
	if r.LocalBackendReady && (!r.LocalSandboxInstalled || !r.LocalSandboxProven ||
		!r.ExecutionPermissionCapabilities.WorkspaceSandboxEnabled) {
		return errors.New("a ready local backend requires the Workspace sandbox gate and proof")
	}
	if r.DockerBackendReady && (!r.DockerStartupGateEnabled || !r.DockerAvailable) {
		return errors.New("a ready Docker backend requires its startup gate and installation")
	}
	if r.SBXBackendReady && (!r.SBXStartupGateEnabled || !r.SBXAvailable ||
		!r.ExecutionPermissionCapabilities.WorkspaceSandboxEnabled) {
		return errors.New("a ready Docker Sandboxes backend requires its startup gate and proof")
	}
	if r.DockerReadiness != nil {
		if r.DockerReadiness.Validate() != nil ||
			r.DockerReadiness.FeatureEnabled != r.DockerStartupGateEnabled ||
			r.DockerAvailable != r.DockerReadiness.DaemonReachable ||
			r.DockerBackendReady != r.DockerReadiness.Ready {
			return errors.New("detailed Docker readiness does not match runtime facts")
		}
	}
	seenAdapters := make(map[commandruntimeadapter.Identity]struct{},
		len(r.CommandRuntimeAdapters))
	for _, adapter := range r.CommandRuntimeAdapters {
		if adapter.Validate() != nil || !adapter.Executable() ||
			!commandRuntimeStartupAvailable(adapter,
				r.ExecutionPermissionCapabilities) {
			return errors.New("Command Runtime installed adapter is invalid")
		}
		if _, duplicate := seenAdapters[adapter]; duplicate {
			return errors.New("Command Runtime installed adapter is duplicated")
		}
		seenAdapters[adapter] = struct{}{}
	}
	if r.BrowserBackendReady && !r.BrowserCDPPermissionControlEnabled {
		return errors.New("a ready browser backend requires restricted CDP control")
	}
	if r.StandardCodePresetEnabled && (!r.RunControlEnabled ||
		!r.ExecutionPermissionControlEnabled) {
		return errors.New("standard code preset control requires Run and permission controls")
	}
	return nil
}

type RunCapabilityReadinessStore interface {
	GetRun(context.Context, string) (domain.Run, error)
	GetRunMode(context.Context, string) (domain.RunModeSnapshot, error)
	GetRunExecutionProfile(context.Context, string) (domain.RunExecutionProfileSnapshot, error)
	GetRunExecutionPermission(context.Context, string) (domain.RunExecutionPermissionSnapshot, error)
	GetRunBrowserCDPPermission(context.Context, string) (domain.RunBrowserCDPPermissionSnapshot, error)
	GetRunExecutionInteraction(context.Context, string) (domain.RunExecutionInteractionSnapshot, error)
	GetRunExecutionLease(context.Context, string) (domain.RunExecutionLease, bool, error)
	GetDrydockByRun(context.Context, string) (runworktree.Workspace, bool, error)
}

type RunCapabilityReadinessService struct {
	store          RunCapabilityReadinessStore
	runtime        CapabilityReadinessRuntime
	commandRuntime toolgateway.CommandRuntimeAdvertiser
	now            func() time.Time
}

func NewRunCapabilityReadinessService(store RunCapabilityReadinessStore,
	runtime CapabilityReadinessRuntime,
	commandRuntime ...toolgateway.CommandRuntimeAdvertiser,
) *RunCapabilityReadinessService {
	service := &RunCapabilityReadinessService{store: store, runtime: runtime,
		now: func() time.Time { return time.Now().UTC() }}
	if len(commandRuntime) == 1 {
		service.commandRuntime = commandRuntime[0]
	}
	return service
}

func (s *RunCapabilityReadinessService) Project(ctx context.Context,
	runID string,
) (RunCapabilityReadiness, error) {
	if s == nil || s.store == nil || s.now == nil {
		return RunCapabilityReadiness{}, apperror.New(apperror.CodeFailedPrecondition,
			"Run capability readiness service is unavailable")
	}
	if err := s.runtime.Validate(); err != nil {
		return RunCapabilityReadiness{}, apperror.Wrap(apperror.CodeFailedPrecondition,
			"Run capability readiness runtime is invalid", err)
	}
	runID = strings.TrimSpace(runID)
	if !domain.ValidAgentID(runID) || strings.ContainsRune(runID, 0) {
		return RunCapabilityReadiness{}, apperror.New(apperror.CodeInvalidArgument,
			"Run capability readiness Run id is invalid")
	}
	run, err := s.store.GetRun(ctx, runID)
	if err != nil {
		return RunCapabilityReadiness{}, apperror.Normalize(err)
	}
	mode, err := s.store.GetRunMode(ctx, run.ID)
	if err != nil {
		return RunCapabilityReadiness{}, apperror.Normalize(err)
	}
	profile, err := s.store.GetRunExecutionProfile(ctx, run.ID)
	if err != nil {
		return RunCapabilityReadiness{}, apperror.Normalize(err)
	}
	permission, err := s.store.GetRunExecutionPermission(ctx, run.ID)
	if err != nil {
		return RunCapabilityReadiness{}, apperror.Normalize(err)
	}
	cdp, err := s.store.GetRunBrowserCDPPermission(ctx, run.ID)
	if err != nil {
		return RunCapabilityReadiness{}, apperror.Normalize(err)
	}
	interaction, err := s.store.GetRunExecutionInteraction(ctx, run.ID)
	if err != nil {
		return RunCapabilityReadiness{}, apperror.Normalize(err)
	}
	lease, found, err := s.store.GetRunExecutionLease(ctx, run.ID)
	if err != nil {
		return RunCapabilityReadiness{}, apperror.Normalize(err)
	}
	if err := validateCapabilityReadinessInputs(run, mode, profile, permission, cdp,
		interaction, lease, found); err != nil {
		return RunCapabilityReadiness{}, apperror.Wrap(apperror.CodeInternal,
			"Run capability readiness source facts are invalid", err)
	}
	drydockWorkspace, drydockFound, err := readRunFileDrydock(ctx, s.store, run.ID)
	if err != nil {
		return RunCapabilityReadiness{}, apperror.Normalize(err)
	}
	drydockReady := drydockFound && requireCurrentRunFileDrydock(ctx, s.store, run.ID, drydockWorkspace) == nil &&
		(drydockWorkspace.State == runworktree.StateReady ||
			drydockWorkspace.State == runworktree.StateDelivered)
	advertisedAdapter := commandruntimeadapter.Identity{}
	advertised := false
	if s.commandRuntime != nil {
		advertisedAdapter, advertised, err = s.commandRuntime.
			AdvertisedCommandRuntimeAdapter(ctx, run.ID, permission.Mode)
		if err != nil {
			return RunCapabilityReadiness{}, apperror.Normalize(err)
		}
		if advertised && !containsCommandRuntimeAdapter(
			s.runtime.CommandRuntimeAdapters, advertisedAdapter) {
			return RunCapabilityReadiness{}, apperror.New(apperror.CodeInternal,
				"advertised Command Runtime adapter is not installed")
		}
	}
	activeLease := found && lease.ActiveAt(s.now())
	verificationSettings := DesktopVerificationSettings{UIEvidenceEnabled: s.runtime.UIEvidenceInstalled, BatchValidationEnabled: s.runtime.BatchValidationInstalled}
	if s.runtime.VerificationSettingsReader != nil {
		snapshot, err := s.runtime.VerificationSettingsReader.Load(ctx)
		if err != nil {
			return RunCapabilityReadiness{}, err
		}
		if snapshot.Revision < 1 {
			return RunCapabilityReadiness{}, apperror.New(apperror.CodeFailedPrecondition, "verification settings revision is invalid")
		}
		verificationSettings = snapshot.Settings
	}
	value := capabilityReadinessProjection{run: run, mode: mode, profile: profile,
		permission: permission, cdp: cdp, interaction: interaction,
		activeLease: activeLease, drydockReady: drydockReady, runtime: s.runtime,
		advertisedAdapter: advertisedAdapter, advertised: advertised,
		verificationSettings:       verificationSettings,
		advertisementAuthoritative: s.commandRuntime != nil}.project()
	if err := value.Validate(); err != nil {
		return RunCapabilityReadiness{}, apperror.Wrap(apperror.CodeInternal,
			"Run capability readiness projection is invalid", err)
	}
	return value, nil
}

func validateCapabilityReadinessInputs(run domain.Run, mode domain.RunModeSnapshot,
	profile domain.RunExecutionProfileSnapshot,
	permission domain.RunExecutionPermissionSnapshot,
	cdp domain.RunBrowserCDPPermissionSnapshot,
	interaction domain.RunExecutionInteractionSnapshot,
	lease domain.RunExecutionLease, leaseFound bool,
) error {
	if err := run.Validate(); err != nil {
		return err
	}
	for _, value := range []struct {
		runID, missionID string
		err              error
	}{
		{mode.RunID, mode.MissionID, mode.Validate()},
		{profile.RunID, profile.MissionID, profile.Validate()},
		{permission.RunID, permission.MissionID, permission.Validate()},
		{cdp.RunID, cdp.MissionID, cdp.Validate()},
		{interaction.RunID, interaction.MissionID, interaction.Validate()},
	} {
		if value.err != nil || value.runID != run.ID || value.missionID != run.MissionID {
			return errors.New("Run capability readiness snapshots do not match the Run")
		}
	}
	if leaseFound && (lease.Validate() != nil || lease.RunID != run.ID) {
		return errors.New("Run capability readiness lease does not match the Run")
	}
	return nil
}

type capabilityReadinessProjection struct {
	run                        domain.Run
	mode                       domain.RunModeSnapshot
	profile                    domain.RunExecutionProfileSnapshot
	permission                 domain.RunExecutionPermissionSnapshot
	cdp                        domain.RunBrowserCDPPermissionSnapshot
	interaction                domain.RunExecutionInteractionSnapshot
	activeLease                bool
	drydockReady               bool
	runtime                    CapabilityReadinessRuntime
	advertisedAdapter          commandruntimeadapter.Identity
	advertised                 bool
	advertisementAuthoritative bool
	verificationSettings       DesktopVerificationSettings
}

func (p capabilityReadinessProjection) project() RunCapabilityReadiness {
	return RunCapabilityReadiness{
		ProtocolVersion: RunCapabilityReadinessProtocolVersion, RunID: p.run.ID,
		Permissions: p.permissionOptions(), Profiles: p.profileOptions(),
		Interactions:          p.interactionOptions(),
		BrowserCDPPermissions: p.browserCDPOptions(), Presets: p.presetOptions(),
		CommandRuntime:  p.commandRuntimeReadiness(),
		UIEvidence:      BuildVerificationFeatureReadiness(p.runtime.UIEvidenceInstalled, p.verificationSettings.UIEvidenceEnabled, p.runtime.VerificationSettingsControlEnabled),
		BatchValidation: BuildVerificationFeatureReadiness(p.runtime.BatchValidationInstalled, p.verificationSettings.BatchValidationEnabled, p.runtime.VerificationSettingsControlEnabled),
		CapabilityGrant: false,
	}
}

func (p capabilityReadinessProjection) commandRuntimeReadiness() CommandRuntimeReadiness {
	result := CommandRuntimeReadiness{ProtocolAvailable: true,
		AdapterInstalled: len(p.runtime.CommandRuntimeAdapters) > 0}
	selected := false
	selectedReady := false
	for _, adapter := range p.runtime.CommandRuntimeAdapters {
		ready := true
		switch adapter.Backend {
		case CommandRuntimeLocalSandboxBackend:
			ready = p.runtime.LocalBackendReady
		case CommandRuntimeDockerSandboxBackend:
			ready = p.runtime.DockerBackendReady
		case "docker_sandboxes":
			ready = p.runtime.SBXBackendReady
		}
		result.AdapterReady = result.AdapterReady || ready
		if !adapter.AllowsPermission(p.permission.Mode) ||
			commandRuntimeExecutionProfile(adapter) != p.profile.Profile {
			continue
		}
		if selected {
			// Ambiguous selection fails closed without hiding the process-level
			// fact that adapters are installed.
			result.AdapterKind, result.Backend = "", ""
			selectedReady = false
			continue
		}
		selected = true
		result.AdapterKind = string(adapter.Kind)
		result.Backend = adapter.Backend
		selectedReady = ready
		if adapter.Kind == commandruntimeadapter.KindSandboxedWorkspace {
			selectedReady = selectedReady && p.drydockReady
		}
	}
	if p.advertised {
		result.AdapterReady = true
		result.AdapterKind = string(p.advertisedAdapter.Kind)
		result.Backend = p.advertisedAdapter.Backend
		selected = true
		selectedReady = true
	}
	grantReady := selected && selectedReady
	if p.advertisementAuthoritative {
		grantReady = p.advertised
	}
	result.CurrentRunGranted = p.runtime.RunExecutionEnabled &&
		grantReady && p.activeLease &&
		p.runtime.ExecutionPermissionCapabilities.AllowsSnapshot(p.permission) &&
		p.run.Status == domain.RunRunning &&
		p.mode.Surface == domain.ExecutionSurfaceCode &&
		p.mode.Phase == domain.ExecutionPhaseDeliver
	return result
}

func containsCommandRuntimeAdapter(installed []commandruntimeadapter.Identity,
	want commandruntimeadapter.Identity,
) bool {
	for _, adapter := range installed {
		if adapter.SameBackend(want) {
			return true
		}
	}
	return false
}

func (p capabilityReadinessProjection) permissionOptions() []CapabilityReadinessOption {
	modes := []domain.RunExecutionPermissionMode{domain.RunExecutionPermissionAsk, domain.RunExecutionPermissionAuto, domain.RunExecutionPermissionFull}
	options := make([]CapabilityReadinessOption, 0, len(modes))
	for _, target := range modes {
		builder := newReadinessOption(string(target), p.permission.Mode.ApprovalPreference() == domain.ExecutionApprovalMode(target))
		selectable := p.runQuiescent() || domain.PermissionTransitionRevokes(p.permission.Mode, target)
		if !selectable {
			p.addRunStateBlocker(builder)
		}
		available := p.runtime.ExecutionPermissionCapabilities.Allows(target)
		if !p.runtime.ExecutionPermissionControlEnabled || !available {
			selectable = false
			builder.add(CapabilityBlockerStartupGateClosed, CapabilityRemediationRestartWithStartupGate)
		}
		runtimeAvailable := available
		if target == domain.RunExecutionPermissionFull {
			runtimeAvailable = available && p.permission.Mode == target && p.runtime.ExecutionPermissionCapabilities.AllowsSnapshot(p.permission)
			if available && p.permission.Mode == target && !runtimeAvailable {
				builder.add(CapabilityBlockerPermissionMismatch, CapabilityRemediationSelectRequiredPermission)
			}
		}
		options = append(options, builder.finish(selectable, runtimeAvailable))
	}
	return options
}

func (p capabilityReadinessProjection) profileOptions() []CapabilityReadinessOption {
	profiles := []domain.RunExecutionProfile{domain.RunExecutionProfilePreview,
		domain.RunExecutionProfileDocker, domain.RunExecutionProfileLocal, domain.RunExecutionProfileSBX}
	options := make([]CapabilityReadinessOption, 0, len(profiles))
	for _, target := range profiles {
		builder := newReadinessOption(string(target), p.profile.Profile == target)
		selectable := p.runQuiescent() && !p.activeLease && p.runtime.RunControlEnabled
		p.addRunStateBlocker(builder)
		if p.activeLease {
			builder.add(CapabilityBlockerExecutionLeaseActive,
				CapabilityRemediationWaitForExecutionLease)
		}
		if !p.runtime.RunControlEnabled {
			builder.add(CapabilityBlockerStartupGateClosed,
				CapabilityRemediationRestartWithStartupGate)
		}
		runtimeAvailable := true
		switch target {
		case domain.RunExecutionProfileLocal:
			runtimeAvailable = p.addLocalBackendBlockers(builder)
		case domain.RunExecutionProfileDocker:
			runtimeAvailable = p.addDockerBackendBlockers(builder)
		case domain.RunExecutionProfileSBX:
			runtimeAvailable = p.addSBXBackendBlockers(builder)
		}
		if target != domain.RunExecutionProfilePreview &&
			p.interaction.WorkspaceTrust != domain.WorkspaceTrustTrusted {
			runtimeAvailable = false
			builder.add(CapabilityBlockerWorkspaceUntrusted,
				CapabilityRemediationTrustWorkspace)
		}
		options = append(options, builder.finish(selectable, runtimeAvailable))
	}
	return options
}

func (p capabilityReadinessProjection) interactionOptions() []CapabilityReadinessOption {
	modes := []domain.RunExecutionInteractionMode{domain.RunExecutionInteractionPreview,
		domain.RunExecutionInteractionControlled, domain.RunExecutionInteractionDebug,
		domain.RunExecutionInteractionCyber}
	options := make([]CapabilityReadinessOption, 0, len(modes))
	for _, target := range modes {
		builder := newReadinessOption(string(target), p.interaction.Mode == target)
		selectable := p.runQuiescent() && !p.activeLease && p.runtime.RunControlEnabled
		p.addRunStateBlocker(builder)
		if p.activeLease {
			builder.add(CapabilityBlockerExecutionLeaseActive,
				CapabilityRemediationWaitForExecutionLease)
		}
		if !p.runtime.RunControlEnabled {
			builder.add(CapabilityBlockerStartupGateClosed,
				CapabilityRemediationRestartWithStartupGate)
		}
		runtimeAvailable := true
		if target != domain.RunExecutionInteractionPreview {
			expectedSurface, expectedProfile := domain.ExecutionSurfaceCode,
				p.profile.Profile
			if target == domain.RunExecutionInteractionCyber {
				expectedSurface, expectedProfile = domain.ExecutionSurfaceCyber,
					domain.RunExecutionProfileDocker
			} else if target == domain.RunExecutionInteractionDebug {
				expectedProfile = domain.RunExecutionProfileLocal
			} else if expectedProfile != domain.RunExecutionProfileLocal &&
				expectedProfile != domain.RunExecutionProfileDocker && expectedProfile != domain.RunExecutionProfileSBX {
				expectedProfile = domain.RunExecutionProfileLocal
			}
			if p.mode.Surface != expectedSurface {
				selectable, runtimeAvailable = false, false
				builder.add(CapabilityBlockerSurfaceMismatch,
					CapabilityRemediationSelectRequiredSurface)
			}
			if p.profile.Profile != expectedProfile {
				selectable, runtimeAvailable = false, false
				builder.add(CapabilityBlockerProfileMismatch,
					CapabilityRemediationSelectRequiredProfile)
			}
			if p.interaction.WorkspaceTrust != domain.WorkspaceTrustTrusted {
				runtimeAvailable = false
				builder.add(CapabilityBlockerWorkspaceUntrusted,
					CapabilityRemediationTrustWorkspace)
			}
			if target == domain.RunExecutionInteractionCyber ||
				(target == domain.RunExecutionInteractionControlled &&
					p.profile.Profile == domain.RunExecutionProfileDocker) {
				if !p.addDockerBackendBlockers(builder) {
					runtimeAvailable = false
				}
			} else if target == domain.RunExecutionInteractionControlled && p.profile.Profile == domain.RunExecutionProfileSBX {
				if !p.addSBXBackendBlockers(builder) {
					runtimeAvailable = false
				}
			} else if !p.addLocalBackendBlockers(builder) {
				runtimeAvailable = false
			}
		}
		if target == domain.RunExecutionInteractionDebug {
			if _, live := p.runtime.ExecutionPermissionCapabilities.FullAccessGeneration(p.permission); p.permission.Mode != domain.RunExecutionPermissionFull || !live {
				runtimeAvailable = false
				builder.add(CapabilityBlockerPermissionMismatch,
					CapabilityRemediationSelectRequiredPermission)
			}
			if !p.runtime.ExecutionPermissionCapabilities.DangerFullAccessEnabled {
				runtimeAvailable = false
				builder.add(CapabilityBlockerStartupGateClosed,
					CapabilityRemediationRestartWithStartupGate)
			}
		}
		options = append(options, builder.finish(selectable, runtimeAvailable))
	}
	return options
}

func (p capabilityReadinessProjection) browserCDPOptions() []CapabilityReadinessOption {
	modes := []domain.RunBrowserCDPPermissionMode{
		domain.RunBrowserCDPPermissionRestricted, domain.RunBrowserCDPPermissionFullDebug}
	options := make([]CapabilityReadinessOption, 0, len(modes))
	for _, target := range modes {
		builder := newReadinessOption(string(target), p.cdp.Mode == target)
		stateAllowsChange := p.runQuiescent() ||
			(target == domain.RunBrowserCDPPermissionRestricted && !p.run.Terminal())
		leaseBlocksChange := target == domain.RunBrowserCDPPermissionFullDebug &&
			p.activeLease
		selectable := stateAllowsChange && !leaseBlocksChange &&
			p.runtime.BrowserCDPPermissionCapabilities.Allows(target)
		if !stateAllowsChange {
			p.addRunStateBlocker(builder)
		}
		if leaseBlocksChange {
			builder.add(CapabilityBlockerExecutionLeaseActive,
				CapabilityRemediationWaitForExecutionLease)
		}
		gateAvailable := p.runtime.BrowserCDPPermissionControlEnabled &&
			p.runtime.BrowserCDPPermissionCapabilities.Allows(target)
		if !gateAvailable {
			selectable = false
			builder.add(CapabilityBlockerStartupGateClosed,
				CapabilityRemediationRestartWithStartupGate)
		}
		runtimeAvailable := gateAvailable && p.runtime.BrowserBackendReady
		if gateAvailable && !p.runtime.BrowserBackendReady {
			builder.add(CapabilityBlockerBackendNotReady,
				CapabilityRemediationRetryBackendReadiness)
		}
		if target == domain.RunBrowserCDPPermissionFullDebug {
			executionCeilingSelected :=
				p.permission.Mode == domain.RunExecutionPermissionFull
			executionLive := executionCeilingSelected &&
				p.runtime.ExecutionPermissionCapabilities.AllowsSnapshot(p.permission)
			if !executionLive {
				selectable, runtimeAvailable = false, false
				builder.add(CapabilityBlockerPermissionMismatch,
					CapabilityRemediationSelectRequiredPermission)
			}
		}
		options = append(options, builder.finish(selectable, runtimeAvailable))
	}
	return options
}

func (p capabilityReadinessProjection) presetOptions() []CapabilityReadinessOption {
	selected := p.mode.Surface == domain.ExecutionSurfaceCode &&
		p.mode.Phase == domain.ExecutionPhasePlan &&
		(p.profile.Profile == domain.RunExecutionProfileLocal ||
			p.profile.Profile == domain.RunExecutionProfileDocker || p.profile.Profile == domain.RunExecutionProfileSBX) &&
		p.interaction.Mode == domain.RunExecutionInteractionControlled &&
		p.interaction.ExecutionProfile == p.profile.Profile &&
		p.interaction.ExecutionProfileRevision == p.profile.Revision &&
		p.permission.Mode.IsApprovalMode() &&
		p.cdp.Mode == domain.RunBrowserCDPPermissionRestricted && p.drydockReady
	builder := newReadinessOption(StandardCodePresetValue, selected)
	selectable := p.runtime.StandardCodePresetEnabled && p.runQuiescent() && !p.activeLease
	p.addRunStateBlocker(builder)
	if p.activeLease {
		builder.add(CapabilityBlockerExecutionLeaseActive,
			CapabilityRemediationWaitForExecutionLease)
	}
	if !p.runtime.StandardCodePresetEnabled {
		builder.add(CapabilityBlockerCapabilityUnimplemented,
			CapabilityRemediationUpgradeApplication)
	}
	runtimeAvailable := p.runtime.StandardCodePresetEnabled
	backendReady := false
	if selected && p.profile.Profile == domain.RunExecutionProfileDocker {
		backendReady = p.addDockerBackendBlockers(builder)
	} else if selected && p.profile.Profile == domain.RunExecutionProfileSBX {
		backendReady = p.addSBXBackendBlockers(builder)
	} else {
		backendReady = p.addLocalBackendBlockers(builder)
	}
	if !backendReady {
		runtimeAvailable = false
	}
	if p.interaction.WorkspaceTrust != domain.WorkspaceTrustTrusted || !p.drydockReady {
		runtimeAvailable = false
		builder.add(CapabilityBlockerWorkspaceUntrusted,
			CapabilityRemediationTrustWorkspace)
	}
	return []CapabilityReadinessOption{builder.finish(selectable, runtimeAvailable)}
}

func (p capabilityReadinessProjection) addSBXBackendBlockers(builder *capabilityReadinessOptionBuilder) bool {
	ready := true
	if !p.runtime.SBXStartupGateEnabled || !p.runtime.ExecutionPermissionCapabilities.WorkspaceSandboxEnabled {
		ready = false
		builder.add(CapabilityBlockerStartupGateClosed, CapabilityRemediationRestartWithStartupGate)
	}
	if !p.runtime.SBXAvailable {
		ready = false
		builder.add(CapabilityBlockerSBXUnavailable, CapabilityRemediationInstallOrStartSBX)
	} else if !p.runtime.SBXBackendReady {
		ready = false
		builder.add(CapabilityBlockerBackendNotReady, CapabilityRemediationRetryBackendReadiness)
	}
	return ready
}

func (p capabilityReadinessProjection) addLocalBackendBlockers(
	builder *capabilityReadinessOptionBuilder,
) bool {
	ready := true
	if !p.runtime.ExecutionPermissionCapabilities.WorkspaceSandboxEnabled {
		ready = false
		builder.add(CapabilityBlockerStartupGateClosed,
			CapabilityRemediationRestartWithStartupGate)
	}
	if !p.runtime.LocalSandboxInstalled || !p.runtime.LocalSandboxProven {
		ready = false
		builder.add(CapabilityBlockerSandboxUnproven,
			CapabilityRemediationVerifySandbox)
	} else if !p.runtime.LocalBackendReady {
		ready = false
		builder.add(CapabilityBlockerBackendNotReady,
			CapabilityRemediationRetryBackendReadiness)
	}
	return ready
}

func (p capabilityReadinessProjection) addDockerBackendBlockers(
	builder *capabilityReadinessOptionBuilder,
) bool {
	if p.runtime.DockerReadiness != nil {
		switch p.runtime.DockerReadiness.ReasonCode {
		case sandbox.DockerReadinessReasonNone:
			return true
		case sandbox.DockerReadinessReasonFeatureDisabled:
			builder.add(CapabilityBlockerStartupGateClosed,
				CapabilityRemediationRestartWithStartupGate)
		case sandbox.DockerReadinessReasonDaemonUnreachable:
			builder.add(CapabilityBlockerDockerUnavailable,
				CapabilityRemediationInstallOrStartDocker)
		default:
			builder.add(CapabilityBlockerBackendNotReady,
				CapabilityRemediationRetryBackendReadiness)
		}
		return false
	}
	ready := true
	if !p.runtime.DockerStartupGateEnabled {
		ready = false
		builder.add(CapabilityBlockerStartupGateClosed,
			CapabilityRemediationRestartWithStartupGate)
	}
	if !p.runtime.DockerAvailable {
		ready = false
		builder.add(CapabilityBlockerDockerUnavailable,
			CapabilityRemediationInstallOrStartDocker)
	} else if !p.runtime.DockerBackendReady {
		ready = false
		builder.add(CapabilityBlockerBackendNotReady,
			CapabilityRemediationRetryBackendReadiness)
	}
	return ready
}

func (p capabilityReadinessProjection) anyWorkspaceSandboxReady() bool {
	return p.runtime.LocalBackendReady || p.runtime.DockerBackendReady
}

func (p capabilityReadinessProjection) runQuiescent() bool {
	return p.run.Status == domain.RunCreated || p.run.Status == domain.RunPaused
}

func (p capabilityReadinessProjection) addRunStateBlocker(
	builder *capabilityReadinessOptionBuilder,
) {
	if p.runQuiescent() {
		return
	}
	remediation := CapabilityRemediationPauseRun
	if p.run.Terminal() || p.run.Status == domain.RunPreparing {
		remediation = CapabilityRemediationCreateNewRun
	}
	builder.add(CapabilityBlockerRunNotQuiescent, remediation)
}

type capabilityReadinessOptionBuilder struct {
	value       string
	selected    bool
	blockedBy   map[CapabilityReadinessBlocker]struct{}
	remediation map[CapabilityReadinessRemediation]struct{}
}

func newReadinessOption(value string, selected bool) *capabilityReadinessOptionBuilder {
	return &capabilityReadinessOptionBuilder{value: value, selected: selected,
		blockedBy:   make(map[CapabilityReadinessBlocker]struct{}),
		remediation: make(map[CapabilityReadinessRemediation]struct{})}
}

func (b *capabilityReadinessOptionBuilder) add(blocker CapabilityReadinessBlocker,
	remediation CapabilityReadinessRemediation,
) {
	b.blockedBy[blocker] = struct{}{}
	b.remediation[remediation] = struct{}{}
}

func (b *capabilityReadinessOptionBuilder) finish(selectable, runtimeAvailable bool) CapabilityReadinessOption {
	blockedBy := make([]CapabilityReadinessBlocker, 0, len(b.blockedBy))
	for value := range b.blockedBy {
		blockedBy = append(blockedBy, value)
	}
	sort.Slice(blockedBy, func(i, j int) bool {
		return capabilityBlockerOrder[blockedBy[i]] < capabilityBlockerOrder[blockedBy[j]]
	})
	remediation := make([]CapabilityReadinessRemediation, 0, len(b.remediation))
	for value := range b.remediation {
		remediation = append(remediation, value)
	}
	sort.Slice(remediation, func(i, j int) bool {
		return capabilityRemediationOrder[remediation[i]] <
			capabilityRemediationOrder[remediation[j]]
	})
	restartRequired := false
	for _, blocker := range blockedBy {
		if blocker == CapabilityBlockerStartupGateClosed {
			restartRequired = true
			break
		}
	}
	return CapabilityReadinessOption{Value: b.value, Selected: b.selected,
		Selectable: selectable, RuntimeAvailable: runtimeAvailable,
		BlockedBy: blockedBy, Remediation: remediation,
		RestartRequired: restartRequired}
}

func (r RunCapabilityReadiness) Validate() error {
	if r.ProtocolVersion != RunCapabilityReadinessProtocolVersion ||
		!domain.ValidAgentID(r.RunID) || r.CapabilityGrant {
		return errors.New("Run capability readiness envelope is invalid")
	}
	if !r.CommandRuntime.ProtocolAvailable ||
		r.CommandRuntime.AdapterReady && !r.CommandRuntime.AdapterInstalled ||
		r.CommandRuntime.CurrentRunGranted && !r.CommandRuntime.AdapterReady ||
		((r.CommandRuntime.AdapterKind == "") != (r.CommandRuntime.Backend == "")) {
		return errors.New("Run Command Runtime readiness is invalid")
	}
	groups := []struct {
		name        string
		options     []CapabilityReadinessOption
		expected    []string
		maxSelected int
	}{
		{"permissions", r.Permissions, []string{"ask", "auto", "full"}, 1},
		{"profiles", r.Profiles, []string{"preview", "docker", "local", "sbx"}, 1},
		{"interactions", r.Interactions, []string{"preview", "controlled", "debug", "cyber"}, 1},
		{"browser CDP permissions", r.BrowserCDPPermissions, []string{"restricted", "full_debug"}, 1},
		{"presets", r.Presets, []string{StandardCodePresetValue}, 1},
	}
	for _, group := range groups {
		if len(group.options) != len(group.expected) {
			return fmt.Errorf("Run capability readiness %s are incomplete", group.name)
		}
		selected := 0
		for index, option := range group.options {
			if option.Value != group.expected[index] || option.Validate() != nil {
				return fmt.Errorf("Run capability readiness %s option is invalid", group.name)
			}
			if option.Selected {
				selected++
			}
		}
		if selected > group.maxSelected || group.name != "presets" && selected != 1 {
			return fmt.Errorf("Run capability readiness %s selection is invalid", group.name)
		}
	}
	return nil
}

func (o CapabilityReadinessOption) Validate() error {
	if strings.TrimSpace(o.Value) == "" || len(o.BlockedBy) > len(capabilityBlockerOrder) ||
		len(o.Remediation) > len(capabilityRemediationOrder) {
		return errors.New("capability readiness option is invalid")
	}
	last := -1
	for _, value := range o.BlockedBy {
		order, ok := capabilityBlockerOrder[value]
		if !ok || order <= last {
			return errors.New("capability readiness blockers are invalid")
		}
		last = order
	}
	last = -1
	for _, value := range o.Remediation {
		order, ok := capabilityRemediationOrder[value]
		if !ok || order <= last {
			return errors.New("capability readiness remediation is invalid")
		}
		last = order
	}
	hasStartupBlocker := false
	for _, value := range o.BlockedBy {
		hasStartupBlocker = hasStartupBlocker || value == CapabilityBlockerStartupGateClosed
		if o.RuntimeAvailable {
			if _, runtimeFailure := capabilityRuntimeFailureBlockers[value]; runtimeFailure {
				return errors.New("capability readiness runtime disposition is invalid")
			}
		}
		if !containsReadinessRemediation(o.Remediation,
			capabilityRemediationsByBlocker[value]...) {
			return errors.New("capability readiness remediation is incomplete")
		}
	}
	for _, remediation := range o.Remediation {
		matched := false
		for _, blocker := range o.BlockedBy {
			if containsReadinessRemediation([]CapabilityReadinessRemediation{remediation},
				capabilityRemediationsByBlocker[blocker]...) {
				matched = true
				break
			}
		}
		if !matched {
			return errors.New("capability readiness remediation is unrelated")
		}
	}
	if o.RestartRequired != hasStartupBlocker || len(o.BlockedBy) == 0 != (len(o.Remediation) == 0) {
		return errors.New("capability readiness disposition is invalid")
	}
	return nil
}

func containsReadinessRemediation(values []CapabilityReadinessRemediation,
	want ...CapabilityReadinessRemediation,
) bool {
	for _, value := range values {
		for _, candidate := range want {
			if value == candidate {
				return true
			}
		}
	}
	return false
}
