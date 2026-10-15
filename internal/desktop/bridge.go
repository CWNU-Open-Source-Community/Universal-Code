package desktop

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"strings"
	"sync/atomic"
	"unicode"
	"unicode/utf8"

	"cyberagent-workbench/internal/apperror"
	"cyberagent-workbench/internal/application"
	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/httpapi"
	"cyberagent-workbench/internal/operationreceipt"
	"cyberagent-workbench/internal/plugins"
	"cyberagent-workbench/internal/skills"
)

const (
	ConnectionBootstrapProtocolVersion   = "desktop_connection_bootstrap.v1"
	SkillPackageDialogProtocolVersion    = "desktop_skill_package_dialog.v1"
	SkillPackageInstallProtocolVersion   = "desktop_skill_package_install.v1"
	SkillPackageInventoryProtocolVersion = "desktop_skill_package_inventory.v1"
	SkillPackageRemovalProtocolVersion   = "desktop_skill_package_removal.v1"
	DesktopAPIBasePath                   = "/api/v1"

	desktopTokenMinBytes = 32
	desktopTokenMaxBytes = 512
)

// ConnectionBootstrap is delivered only through the same-origin native
// binding. Tokens stay in renderer memory and are never written to browser
// storage, SQLite, logs, command output, or the Windows registry.
type ConnectionBootstrap struct {
	ProtocolVersion                    string `json:"protocol_version"`
	APIBaseURL                         string `json:"api_base_url"`
	APIVersion                         string `json:"api_version"`
	AppVersion                         string `json:"app_version"`
	UIDigest                           string `json:"ui_digest"`
	ReadToken                          string `json:"read_token"`
	ControlToken                       string `json:"control_token"`
	ControlEnabled                     bool   `json:"control_enabled"`
	ExecutionPermissionControlEnabled  bool   `json:"execution_permission_control_enabled"`
	WorkspaceSandboxEnabled            bool   `json:"workspace_sandbox_enabled"`
	BrowserCDPPermissionControlEnabled bool   `json:"browser_cdp_permission_control_enabled"`
	FullCDPDebugEnabled                bool   `json:"full_cdp_debug_enabled"`
	FullCDPSessionControlEnabled       bool   `json:"full_cdp_session_control_enabled"`
	OperatorApprovalEnabled            bool   `json:"operator_approval_enabled"`
	DangerFullAccessEnabled            bool   `json:"danger_full_access_enabled"`
	CommandRuntimeEnabled              bool   `json:"command_runtime_enabled"`
	ThreadControlEnabled               bool   `json:"thread_control_enabled"`
	CommandRuntimeProtocolAvailable    bool   `json:"command_runtime_protocol_available"`
	CommandRuntimeAdapterInstalled     bool   `json:"command_runtime_adapter_installed"`
	CommandRuntimeAdapterReady         bool   `json:"command_runtime_adapter_ready"`
	RunCreationEnabled                 bool   `json:"run_creation_enabled"`
	StandardCodePresetEnabled          bool   `json:"standard_code_preset_enabled"`
	SessionMessageEnabled              bool   `json:"session_message_enabled"`
	SessionSteeringControlEnabled      bool   `json:"session_steering_control_enabled"`
	RunLifecycleEnabled                bool   `json:"run_lifecycle_enabled"`
	RunExecutionEnabled                bool   `json:"run_execution_enabled"`
	PlanDeliveryControlEnabled         bool   `json:"plan_delivery_control_enabled"`
	ApprovalControlEnabled             bool   `json:"approval_control_enabled"`
	ModelControlEnabled                bool   `json:"model_control_enabled"`
	ProviderCredentialEnabled          bool   `json:"provider_credential_enabled"`
	FileEditReviewEnabled              bool   `json:"file_edit_review_enabled"`
	FileEditProposalEnabled            bool   `json:"file_edit_proposal_enabled"`
	RunWakeControlEnabled              bool   `json:"run_wake_control_enabled"`
	FileEditApplyEnabled               bool   `json:"file_edit_apply_enabled"`
	RunWakeExecutionEnabled            bool   `json:"run_wake_execution_enabled"`
	RunWakeWorkerEnabled               bool   `json:"run_wake_worker_enabled"`
	ScheduledJobControlEnabled         bool   `json:"scheduled_job_control_enabled"`
	ScheduledJobWorkerEnabled          bool   `json:"scheduled_job_worker_enabled"`
	ReadOnlyDefault                    bool   `json:"read_only_default"`
	ProcessExecutionEnabled            bool   `json:"process_execution_enabled"`
	ShellExecutionEnabled              bool   `json:"shell_execution_enabled"`
	DockerExecutionEnabled             bool   `json:"docker_execution_enabled"`
	AgentCodeToolsEnabled              bool   `json:"agent_code_tools_enabled"`
	CodeIntelEnabled                   bool   `json:"code_intel_enabled"`
	SkillInstallationEnabled           bool   `json:"skill_installation_enabled"`
	EvidenceAttachmentEnabled          bool   `json:"evidence_attachment_enabled"`
	VerificationEvidenceEnabled        bool   `json:"verification_evidence_enabled"`
	EmbeddedAnalyzerExecutionEnabled   bool   `json:"embedded_analyzer_execution_enabled"`
	WorkspaceCheckpointControlEnabled  bool   `json:"workspace_checkpoint_control_enabled"`
	GitAdvancedControlEnabled          bool   `json:"git_advanced_control_enabled"`
	GitHubReviewControlEnabled         bool   `json:"github_review_control_enabled"`
	BatchDeliveryControlEnabled        bool   `json:"batch_delivery_control_enabled"`
	BatchDeliveryHostValidationEnabled bool   `json:"batch_delivery_host_validation_enabled"`
	UIEvidenceControlEnabled           bool   `json:"ui_evidence_control_enabled"`
	UserTerminalEnabled                bool   `json:"user_terminal_enabled"`
	AgentTerminalInputDefault          bool   `json:"agent_terminal_input_default"`
	WorkspaceOpenEnabled               bool   `json:"workspace_open_enabled"`
	WorkspaceImportEnabled             bool   `json:"workspace_import_enabled"`
	RendererPathInputSupported         bool   `json:"renderer_path_input_supported"`
	RiskProfileRestartEnabled          bool   `json:"risk_profile_restart_enabled"`
}

type SkillPackageDialogStatus string

const (
	SkillPackageDialogSelected  SkillPackageDialogStatus = "selected"
	SkillPackageDialogCancelled SkillPackageDialogStatus = "cancelled"
)

// SkillPackageDialogResult contains either an opaque selection handle or an
// explicit cancellation result. It cannot represent a local path.
type SkillPackageDialogResult struct {
	ProtocolVersion string                   `json:"protocol_version"`
	Status          SkillPackageDialogStatus `json:"status"`
	Selection       *SkillPackageSelection   `json:"selection"`
}

// SkillPackageFilePicker is implemented by the native shell. The selected
// path crosses this interface only inside Go and is immediately validated by
// the pathless preview boundary.
type SkillPackageFilePicker interface {
	OpenSkillPackage(context.Context) (string, error)
}

type SkillPackageInstaller interface {
	Import(context.Context, application.ImportSkillPackageRequest) (
		application.ImportSkillPackageResult, error)
}

// SkillPackageRegistry exposes the operator-facing historical Registry view.
// Inventory reports the live readback state of each retained object, and Remove
// writes the immutable tombstone. Neither grants selection or execution.
type SkillPackageRegistry interface {
	Inventory(context.Context, application.ListInstalledSkillPackagesRequest) (
		application.SkillPackageInventory, error)
	Remove(context.Context, application.RemoveSkillPackageRequest) (
		application.RemoveSkillPackageResult, error)
}

type SkillPackageInstallRequest struct {
	ProtocolVersion    string `json:"protocol_version"`
	ConfirmationHandle string `json:"confirmation_handle"`
	Surface            string `json:"surface"`
	OperationKey       string `json:"operation_key"`
	ConfirmUntrusted   bool   `json:"confirm_untrusted"`
}

type SkillPackageInstallResult struct {
	Installation               *plugins.Installation    `json:"-"`
	ProtocolVersion            string                   `json:"protocol_version"`
	Name                       string                   `json:"name"`
	Version                    string                   `json:"version"`
	Surface                    string                   `json:"surface"`
	TrustClass                 string                   `json:"trust_class"`
	ArchiveSHA256              string                   `json:"archive_sha256"`
	PackageFingerprint         string                   `json:"package_fingerprint"`
	Replayed                   bool                     `json:"replayed"`
	RecoveredPending           bool                     `json:"recovered_pending"`
	ImportCommandExecution     bool                     `json:"import_command_execution"`
	ImportNetworkAccess        bool                     `json:"import_network_access"`
	ImportProviderCalls        bool                     `json:"import_provider_calls"`
	ToolCapabilityGrant        bool                     `json:"tool_capability_grant"`
	RunSelectionAuthorized     bool                     `json:"run_selection_authorized"`
	ContextInjectionAuthorized bool                     `json:"context_injection_authorized"`
	Receipt                    operationreceipt.Receipt `json:"receipt"`
}

// MarshalJSON keeps legacy recovery responses unchanged. New installations
// expose the Plugin contract and never claim an old Registry receipt.
func (value SkillPackageInstallResult) MarshalJSON() ([]byte, error) {
	if value.Installation != nil {
		view := httpapi.ProjectPluginInstallation(*value.Installation)
		if view.Source.Kind == "local_directory" {
			view.Source.URI = ""
		} // Native paths remain inside Go.
		return json.Marshal(httpapi.PluginSkillInstallView{ProtocolVersion: plugins.PortableInstallationProtocol,
			Installation: view, Replayed: value.Replayed})
	}
	type legacy SkillPackageInstallResult
	return json.Marshal(legacy(value))
}

type DesktopBridgeConfig struct {
	ContextProvider                    func() context.Context
	FilePicker                         SkillPackageFilePicker
	ReadToken                          string
	ControlToken                       string
	RunControlEnabled                  bool
	ExecutionPermissionControlEnabled  bool
	WorkspaceSandboxEnabled            bool
	BrowserCDPPermissionControlEnabled bool
	FullCDPDebugEnabled                bool
	FullCDPSessionControlEnabled       bool
	OperatorApprovalEnabled            bool
	DangerFullAccessEnabled            bool
	CommandRuntimeAdapterInstalled     bool
	CommandRuntimeAdapterReady         bool
	RunCreationEnabled                 bool
	StandardCodePresetEnabled          bool
	SessionMessageEnabled              bool
	SessionSteeringControlEnabled      bool
	RunLifecycleEnabled                bool
	RunExecutionEnabled                bool
	PlanDeliveryControlEnabled         bool
	ApprovalControlEnabled             bool
	ModelControlEnabled                bool
	ProviderCredentialEnabled          bool
	FileEditReviewEnabled              bool
	FileEditProposalEnabled            bool
	RunWakeControlEnabled              bool
	FileEditApplyEnabled               bool
	RunWakeExecutionEnabled            bool
	RunWakeWorkerEnabled               bool
	ScheduledJobControlEnabled         bool
	ScheduledJobWorkerEnabled          bool
	SkillInstallationEnabled           bool
	EvidenceAttachmentEnabled          bool
	VerificationEvidenceEnabled        bool
	EmbeddedAnalyzerExecutionEnabled   bool
	BatchDeliveryControlEnabled        bool
	BatchDeliveryHostValidationEnabled bool
	UIEvidenceControlEnabled           bool
	UserTerminalEnabled                bool
	DockerExecutionEnabled             bool
	CodeIntelEnabled                   bool
	GitAdvancedControlEnabled          bool
	GitHubReviewControlEnabled         bool
	APIVersion                         string
	AppVersion                         string
	UIDigest                           string
	Selector                           NativeSkillPackageSelector
	PreviewBridge                      *SkillPackagePreviewBridge
	SkillInstaller                     SkillPackageInstaller
	SkillRegistry                      SkillPackageRegistry
	WorkspaceResolver                  WorkspaceResolver
	WorkspaceLauncher                  NativeWorkspaceLauncher
	WorkspaceDirectoryPicker           WorkspaceDirectoryPicker
	WorkspaceRegistrar                 WorkspaceDirectoryRegistrar
	ClipboardReader                    ClipboardFileReader
	ClipboardFiles                     ClipboardFileImporter
	ClipboardImages                    ClipboardImageImporter
	UserTerminalController             UserTerminalController
	DebugTerminalAgentInputController  application.DebugTerminalAgentInputController
	RiskProfileRestartEnabled          bool
	RiskProfileRestarter               DesktopRiskProfileRestarter
}

// DesktopBridge is the complete renderer binding surface for D0-A. Keep this
// type deliberately small: Wails binds every exported method.
type DesktopBridge struct {
	contextProvider          func() context.Context
	filePicker               SkillPackageFilePicker
	selector                 NativeSkillPackageSelector
	previewBridge            *SkillPackagePreviewBridge
	skillInstaller           SkillPackageInstaller
	skillRegistry            SkillPackageRegistry
	workspaceResolver        WorkspaceResolver
	workspaceLauncher        NativeWorkspaceLauncher
	workspaceDirectoryPicker WorkspaceDirectoryPicker
	workspaceRegistrar       WorkspaceDirectoryRegistrar
	clipboardReader          ClipboardFileReader
	clipboardFiles           ClipboardFileImporter
	clipboardImages          ClipboardImageImporter
	clipboardActive          atomic.Bool
	userTerminal             UserTerminalController
	debugTerminalAgentInput  application.DebugTerminalAgentInputController
	riskProfileRestarter     DesktopRiskProfileRestarter
	bootstrap                ConnectionBootstrap
	dialogActive             atomic.Bool
	workspaceOpenActive      atomic.Bool
	riskRestartActive        atomic.Bool
}

func NewDesktopBridge(config DesktopBridgeConfig) (*DesktopBridge, error) {
	if config.ContextProvider == nil || config.FilePicker == nil || config.Selector == nil ||
		config.PreviewBridge == nil {
		return nil, apperror.New(apperror.CodeInvalidArgument,
			"desktop bridge dependencies are required")
	}
	if !validDesktopToken(config.ReadToken) ||
		(config.ControlToken != "" && !validDesktopToken(config.ControlToken)) {
		return nil, apperror.New(apperror.CodeInvalidArgument,
			"desktop bridge tokens must be normalized bounded values")
	}
	controlEnabled := config.RunControlEnabled ||
		config.ExecutionPermissionControlEnabled ||
		config.BrowserCDPPermissionControlEnabled || config.FullCDPSessionControlEnabled ||
		config.RunCreationEnabled ||
		config.StandardCodePresetEnabled ||
		config.SessionMessageEnabled ||
		config.SessionSteeringControlEnabled || config.RunLifecycleEnabled ||
		config.RunExecutionEnabled || config.PlanDeliveryControlEnabled ||
		config.ApprovalControlEnabled || config.ModelControlEnabled ||
		config.ProviderCredentialEnabled || config.FileEditReviewEnabled ||
		config.FileEditProposalEnabled || config.RunWakeControlEnabled ||
		config.ScheduledJobControlEnabled
	controlEnabled = controlEnabled || config.FileEditApplyEnabled ||
		config.RunWakeExecutionEnabled || config.RunWakeWorkerEnabled ||
		config.ScheduledJobWorkerEnabled ||
		config.SkillInstallationEnabled ||
		config.EvidenceAttachmentEnabled || config.VerificationEvidenceEnabled ||
		config.EmbeddedAnalyzerExecutionEnabled ||
		config.BatchDeliveryControlEnabled || config.UIEvidenceControlEnabled ||
		config.ControlToken != "" ||
		config.UserTerminalEnabled || config.DockerExecutionEnabled ||
		config.GitAdvancedControlEnabled || config.GitHubReviewControlEnabled
	if controlEnabled && config.ControlToken == "" {
		return nil, apperror.New(apperror.CodeInvalidArgument,
			"desktop control capabilities require a control token")
	}
	if config.ControlToken != "" && !controlEnabled {
		return nil, apperror.New(apperror.CodeInvalidArgument,
			"desktop control token requires an enabled control capability")
	}
	if config.SkillInstallationEnabled && config.SkillInstaller == nil {
		return nil, apperror.New(apperror.CodeInvalidArgument,
			"desktop Skill installation requires the Go Registry installer")
	}
	if (config.WorkspaceResolver == nil) != (config.WorkspaceLauncher == nil) {
		return nil, apperror.New(apperror.CodeInvalidArgument,
			"desktop workspace opening requires paired resolver and launcher dependencies")
	}
	if (config.WorkspaceDirectoryPicker == nil) != (config.WorkspaceRegistrar == nil) {
		return nil, apperror.New(apperror.CodeInvalidArgument,
			"desktop workspace import requires paired picker and registrar dependencies")
	}
	if config.UserTerminalEnabled && config.UserTerminalController == nil {
		return nil, apperror.New(apperror.CodeInvalidArgument,
			"desktop user terminal requires the Go terminal controller")
	}
	if config.UserTerminalEnabled && config.DangerFullAccessEnabled &&
		config.DebugTerminalAgentInputController == nil {
		return nil, apperror.New(apperror.CodeInvalidArgument,
			"desktop Debug terminal Agent input requires the Go lease controller")
	}
	if config.RiskProfileRestartEnabled &&
		(config.RiskProfileRestarter == nil || config.ControlToken == "" ||
			!config.ExecutionPermissionControlEnabled || !config.OperatorApprovalEnabled) {
		return nil, apperror.New(apperror.CodeInvalidArgument,
			"desktop risk-profile restart requires native restart and permission control")
	}
	if !config.RiskProfileRestartEnabled && config.RiskProfileRestarter != nil {
		return nil, apperror.New(apperror.CodeInvalidArgument,
			"desktop risk-profile restarter requires its explicit capability")
	}
	permissionCapabilities := domain.ExecutionPermissionRuntimeCapabilities{
		WorkspaceSandboxEnabled: config.WorkspaceSandboxEnabled,
		OperatorApprovalEnabled: config.OperatorApprovalEnabled,
		DangerFullAccessEnabled: config.DangerFullAccessEnabled,
	}
	if err := permissionCapabilities.Validate(); err != nil {
		return nil, apperror.Wrap(apperror.CodeInvalidArgument,
			"desktop execution permission capabilities are invalid", err)
	}
	if (config.WorkspaceSandboxEnabled || config.OperatorApprovalEnabled || config.DangerFullAccessEnabled) && !config.ExecutionPermissionControlEnabled {
		return nil, apperror.New(apperror.CodeInvalidArgument,
			"desktop execution permission capabilities require permission control")
	}

	if config.DockerExecutionEnabled &&
		(!config.ExecutionPermissionControlEnabled || !config.OperatorApprovalEnabled) {
		return nil, apperror.New(apperror.CodeInvalidArgument,
			"desktop Docker execution requires operator approval permission control")
	}
	if config.GitAdvancedControlEnabled &&
		(!config.ExecutionPermissionControlEnabled || !config.OperatorApprovalEnabled) {
		return nil, apperror.New(apperror.CodeInvalidArgument,
			"desktop Git advanced control requires operator approval permission control")
	}
	if config.GitHubReviewControlEnabled &&
		(!config.ExecutionPermissionControlEnabled || !config.OperatorApprovalEnabled) {
		return nil, apperror.New(apperror.CodeInvalidArgument,
			"desktop GitHub review control requires operator approval permission control")
	}
	if config.BatchDeliveryHostValidationEnabled &&
		(!config.ExecutionPermissionControlEnabled || !config.OperatorApprovalEnabled ||
			!config.DangerFullAccessEnabled || !config.BatchDeliveryControlEnabled ||
			config.ControlToken == "") {
		return nil, apperror.New(apperror.CodeInvalidArgument,
			"desktop batch validation requires control, permission control, and danger-full-access")
	}
	if config.CommandRuntimeAdapterReady && !config.CommandRuntimeAdapterInstalled {
		return nil, apperror.New(apperror.CodeInvalidArgument,
			"desktop Command Runtime readiness requires an installed adapter")
	}
	commandRuntimeEnabled := config.RunExecutionEnabled &&
		config.CommandRuntimeAdapterReady
	browserCDPCapabilities := domain.BrowserCDPPermissionRuntimeCapabilities{
		ControlEnabled:   config.BrowserCDPPermissionControlEnabled,
		FullDebugEnabled: config.FullCDPDebugEnabled,
	}
	if err := browserCDPCapabilities.Validate(); err != nil {
		return nil, apperror.Wrap(apperror.CodeInvalidArgument,
			"desktop browser CDP permission capabilities are invalid", err)
	}
	if config.FullCDPDebugEnabled && !config.DangerFullAccessEnabled {
		return nil, apperror.New(apperror.CodeInvalidArgument,
			"desktop full CDP requires the Full Access execution adapter")
	}
	if config.FullCDPSessionControlEnabled &&
		(!config.FullCDPDebugEnabled || !config.BrowserCDPPermissionControlEnabled ||
			!config.DangerFullAccessEnabled) {
		return nil, apperror.New(apperror.CodeInvalidArgument,
			"desktop Full CDP session control requires the production Full CDP permission stack")
	}
	if config.UIEvidenceControlEnabled &&
		(!commandRuntimeEnabled || !config.BrowserCDPPermissionControlEnabled) {
		return nil, apperror.New(apperror.CodeInvalidArgument,
			"desktop UI evidence requires command runtime and restricted browser CDP control")
	}
	readHash := sha256.Sum256([]byte(config.ReadToken))
	controlHash := sha256.Sum256([]byte(config.ControlToken))
	if config.ControlToken != "" && subtle.ConstantTimeCompare(readHash[:], controlHash[:]) == 1 {
		return nil, apperror.New(apperror.CodeInvalidArgument,
			"desktop read and control tokens must be distinct")
	}
	apiVersion := strings.TrimSpace(config.APIVersion)
	appVersion := strings.TrimSpace(config.AppVersion)
	if apiVersion == "" || len(apiVersion) > 64 || appVersion == "" || len(appVersion) > 64 ||
		!validSHA256(config.UIDigest) {
		return nil, apperror.New(apperror.CodeInvalidArgument,
			"desktop bridge version and UI digest metadata are invalid")
	}
	return &DesktopBridge{
		contextProvider:          config.ContextProvider,
		filePicker:               config.FilePicker,
		selector:                 config.Selector,
		previewBridge:            config.PreviewBridge,
		skillInstaller:           config.SkillInstaller,
		skillRegistry:            config.SkillRegistry,
		workspaceResolver:        config.WorkspaceResolver,
		workspaceLauncher:        config.WorkspaceLauncher,
		workspaceDirectoryPicker: config.WorkspaceDirectoryPicker,
		workspaceRegistrar:       config.WorkspaceRegistrar,
		clipboardReader:          config.ClipboardReader,
		clipboardFiles:           config.ClipboardFiles,
		clipboardImages:          config.ClipboardImages,
		userTerminal:             config.UserTerminalController,
		debugTerminalAgentInput:  config.DebugTerminalAgentInputController,
		riskProfileRestarter:     config.RiskProfileRestarter,
		bootstrap: ConnectionBootstrap{
			ProtocolVersion: ConnectionBootstrapProtocolVersion,
			APIBaseURL:      DesktopAPIBasePath, APIVersion: apiVersion, AppVersion: appVersion,
			UIDigest: config.UIDigest, ReadToken: config.ReadToken, ControlToken: config.ControlToken,
			ControlEnabled:                     config.RunControlEnabled,
			ExecutionPermissionControlEnabled:  config.ExecutionPermissionControlEnabled,
			WorkspaceSandboxEnabled:            config.WorkspaceSandboxEnabled,
			BrowserCDPPermissionControlEnabled: config.BrowserCDPPermissionControlEnabled,
			FullCDPDebugEnabled:                config.FullCDPDebugEnabled,
			FullCDPSessionControlEnabled:       config.FullCDPSessionControlEnabled,
			OperatorApprovalEnabled:            config.OperatorApprovalEnabled,
			DangerFullAccessEnabled:            config.DangerFullAccessEnabled,
			CommandRuntimeEnabled:              commandRuntimeEnabled,
			ThreadControlEnabled:               config.RunCreationEnabled && config.SessionMessageEnabled,
			CommandRuntimeProtocolAvailable:    true,
			CommandRuntimeAdapterInstalled:     config.CommandRuntimeAdapterInstalled,
			CommandRuntimeAdapterReady:         config.CommandRuntimeAdapterReady,
			RunCreationEnabled:                 config.RunCreationEnabled,
			StandardCodePresetEnabled:          config.StandardCodePresetEnabled,
			SessionMessageEnabled:              config.SessionMessageEnabled,
			SessionSteeringControlEnabled:      config.SessionSteeringControlEnabled,
			RunLifecycleEnabled:                config.RunLifecycleEnabled,
			RunExecutionEnabled:                config.RunExecutionEnabled,
			PlanDeliveryControlEnabled:         config.PlanDeliveryControlEnabled,
			ApprovalControlEnabled:             config.ApprovalControlEnabled,
			ModelControlEnabled:                config.ModelControlEnabled,
			ProviderCredentialEnabled:          config.ProviderCredentialEnabled,
			FileEditReviewEnabled:              config.FileEditReviewEnabled,
			FileEditProposalEnabled:            config.FileEditProposalEnabled,
			RunWakeControlEnabled:              config.RunWakeControlEnabled,
			FileEditApplyEnabled:               config.FileEditApplyEnabled,
			RunWakeExecutionEnabled:            config.RunWakeExecutionEnabled,
			RunWakeWorkerEnabled:               config.RunWakeWorkerEnabled,
			ScheduledJobControlEnabled:         config.ScheduledJobControlEnabled,
			ScheduledJobWorkerEnabled:          config.ScheduledJobWorkerEnabled,
			ReadOnlyDefault:                    !controlEnabled,
			ProcessExecutionEnabled:            config.UserTerminalEnabled || commandRuntimeEnabled,
			ShellExecutionEnabled:              config.UserTerminalEnabled || commandRuntimeEnabled,
			DockerExecutionEnabled:             config.DockerExecutionEnabled,
			AgentCodeToolsEnabled:              true,
			CodeIntelEnabled:                   config.CodeIntelEnabled,
			SkillInstallationEnabled:           config.SkillInstallationEnabled,
			EvidenceAttachmentEnabled:          config.EvidenceAttachmentEnabled,
			VerificationEvidenceEnabled:        config.VerificationEvidenceEnabled,
			EmbeddedAnalyzerExecutionEnabled:   config.EmbeddedAnalyzerExecutionEnabled,
			WorkspaceCheckpointControlEnabled:  config.ControlToken != "",
			GitAdvancedControlEnabled:          config.GitAdvancedControlEnabled,
			GitHubReviewControlEnabled:         config.GitHubReviewControlEnabled,
			BatchDeliveryControlEnabled:        config.BatchDeliveryControlEnabled,
			BatchDeliveryHostValidationEnabled: config.BatchDeliveryHostValidationEnabled,
			UIEvidenceControlEnabled:           config.UIEvidenceControlEnabled,
			UserTerminalEnabled:                config.UserTerminalEnabled,
			AgentTerminalInputDefault:          false,
			WorkspaceOpenEnabled:               config.WorkspaceResolver != nil,
			WorkspaceImportEnabled:             config.RunCreationEnabled && config.WorkspaceRegistrar != nil,
			RendererPathInputSupported:         false,
			RiskProfileRestartEnabled:          config.RiskProfileRestartEnabled,
		},
	}, nil
}

func (b *DesktopBridge) StartUserTerminal(
	request DesktopTerminalStartRequest,
) (DesktopTerminalSession, error) {
	if b == nil || !b.bootstrap.UserTerminalEnabled || b.userTerminal == nil {
		return DesktopTerminalSession{}, apperror.New(apperror.CodeNotFound,
			"desktop user terminal is disabled")
	}
	ctx, err := b.lifecycleContext()
	if err != nil {
		return DesktopTerminalSession{}, err
	}
	return b.userTerminal.Start(ctx, request)
}

func (b *DesktopBridge) GetUserTerminal(
	sessionID string,
) (DesktopTerminalSession, error) {
	if b == nil || !b.bootstrap.UserTerminalEnabled || b.userTerminal == nil {
		return DesktopTerminalSession{}, apperror.New(apperror.CodeNotFound,
			"desktop user terminal is disabled")
	}
	ctx, err := b.lifecycleContext()
	if err != nil {
		return DesktopTerminalSession{}, err
	}
	return b.userTerminal.Get(ctx, sessionID)
}

func (b *DesktopBridge) ReadUserTerminal(
	request DesktopTerminalReadRequest,
) (DesktopTerminalOutput, error) {
	if b == nil || !b.bootstrap.UserTerminalEnabled || b.userTerminal == nil {
		return DesktopTerminalOutput{}, apperror.New(apperror.CodeNotFound,
			"desktop user terminal is disabled")
	}
	ctx, err := b.lifecycleContext()
	if err != nil {
		return DesktopTerminalOutput{}, err
	}
	return b.userTerminal.Read(ctx, request)
}

func (b *DesktopBridge) WriteUserTerminal(
	request DesktopTerminalWriteRequest,
) (DesktopTerminalWriteResult, error) {
	if b == nil || !b.bootstrap.UserTerminalEnabled || b.userTerminal == nil {
		return DesktopTerminalWriteResult{}, apperror.New(apperror.CodeNotFound,
			"desktop user terminal is disabled")
	}
	ctx, err := b.lifecycleContext()
	if err != nil {
		return DesktopTerminalWriteResult{}, err
	}
	return b.userTerminal.Write(ctx, request)
}

func (b *DesktopBridge) ResizeUserTerminal(
	request DesktopTerminalResizeRequest,
) error {
	if b == nil || !b.bootstrap.UserTerminalEnabled || b.userTerminal == nil {
		return apperror.New(apperror.CodeNotFound,
			"desktop user terminal is disabled")
	}
	ctx, err := b.lifecycleContext()
	if err != nil {
		return err
	}
	return b.userTerminal.Resize(ctx, request)
}

func (b *DesktopBridge) CloseUserTerminal(
	request DesktopTerminalCloseRequest,
) error {
	if b == nil || !b.bootstrap.UserTerminalEnabled || b.userTerminal == nil {
		return apperror.New(apperror.CodeNotFound,
			"desktop user terminal is disabled")
	}
	ctx, err := b.lifecycleContext()
	if err != nil {
		return err
	}
	return b.userTerminal.Close(ctx, request)
}

// Bootstrap returns same-origin in-memory connection material. It performs no
// file, process, network, model, Docker, or database operation.
func (b *DesktopBridge) Bootstrap() (ConnectionBootstrap, error) {
	if b == nil {
		return ConnectionBootstrap{}, apperror.New(apperror.CodeFailedPrecondition,
			"desktop bridge is unavailable")
	}
	return b.bootstrap, nil
}

// SelectSkillPackage opens the native dialog and returns only an opaque handle.
// Concurrent dialog requests fail closed rather than queuing more OS dialogs.
func (b *DesktopBridge) SelectSkillPackage() (SkillPackageDialogResult, error) {
	if b == nil || b.contextProvider == nil || b.filePicker == nil || b.selector == nil {
		return SkillPackageDialogResult{}, apperror.New(apperror.CodeFailedPrecondition,
			"desktop skill package dialog is unavailable")
	}
	if !b.dialogActive.CompareAndSwap(false, true) {
		return SkillPackageDialogResult{}, apperror.New(apperror.CodeResourceExhausted,
			"desktop skill package dialog is already active")
	}
	defer b.dialogActive.Store(false)

	ctx, err := b.lifecycleContext()
	if err != nil {
		return SkillPackageDialogResult{}, err
	}
	selectedPath, err := b.filePicker.OpenSkillPackage(ctx)
	if err != nil {
		return SkillPackageDialogResult{}, apperror.New(apperror.CodeUnavailable,
			"native skill package dialog failed")
	}
	if err := ctx.Err(); err != nil {
		return SkillPackageDialogResult{}, apperror.Normalize(err)
	}
	if selectedPath == "" {
		return SkillPackageDialogResult{
			ProtocolVersion: SkillPackageDialogProtocolVersion,
			Status:          SkillPackageDialogCancelled,
			Selection:       nil,
		}, nil
	}
	selection, err := b.selector(ctx, selectedPath)
	if err != nil {
		return SkillPackageDialogResult{}, apperror.Normalize(err)
	}
	return SkillPackageDialogResult{
		ProtocolVersion: SkillPackageDialogProtocolVersion,
		Status:          SkillPackageDialogSelected,
		Selection:       &selection,
	}, nil
}

// PreviewSkillPackage consumes a native-issued handle. No renderer-provided
// path, file bytes, URL, command, or installation request is accepted.
func (b *DesktopBridge) PreviewSkillPackage(handle string) (SkillPackagePreview, error) {
	if b == nil || b.previewBridge == nil {
		return SkillPackagePreview{}, apperror.New(apperror.CodeFailedPrecondition,
			"desktop skill package preview is unavailable")
	}
	ctx, err := b.lifecycleContext()
	if err != nil {
		return SkillPackagePreview{}, err
	}
	return b.previewBridge.Preview(ctx, handle)
}

// InstallSkillPackage consumes a short-lived preview confirmation. Package
// bytes remain in Go and flow through the existing Plugin installation
// lifecycle (or recovery of an already recorded legacy intent); this method cannot execute scripts, hooks, commands, tools,
// provider calls, or network requests.
func (b *DesktopBridge) InstallSkillPackage(
	request SkillPackageInstallRequest,
) (SkillPackageInstallResult, error) {
	if b == nil || !b.bootstrap.SkillInstallationEnabled || b.skillInstaller == nil ||
		b.previewBridge == nil {
		return SkillPackageInstallResult{}, apperror.New(apperror.CodeNotFound,
			"desktop Skill installation is disabled")
	}
	if request.ProtocolVersion != SkillPackageInstallProtocolVersion ||
		!request.ConfirmUntrusted {
		return SkillPackageInstallResult{}, apperror.New(apperror.CodeFailedPrecondition,
			"desktop Skill installation requires explicit untrusted-content confirmation")
	}
	surface, err := domain.ParseExecutionSurface(request.Surface)
	if err != nil {
		return SkillPackageInstallResult{}, apperror.Normalize(err)
	}
	ctx, err := b.lifecycleContext()
	if err != nil {
		return SkillPackageInstallResult{}, err
	}
	raw, preview, err := b.previewBridge.ConsumeInstall(ctx,
		request.ConfirmationHandle)
	if err != nil {
		return SkillPackageInstallResult{}, err
	}
	result, err := b.skillInstaller.Import(ctx, application.ImportSkillPackageRequest{
		Raw: raw, Snapshot: preview.snapshot, Source: preview.source, Surface: surface, OperationKey: request.OperationKey,
		InstalledBy: "desktop_operator", ConfirmUntrusted: true,
	})
	if err != nil {
		return SkillPackageInstallResult{}, apperror.Normalize(err)
	}
	if result.Installation != nil {
		installed := result.Installation
		if installed.Snapshot == nil || installed.ArchiveSHA256 != preview.ArchiveSHA256 ||
			installed.DisplayName() != preview.Name || installed.Snapshot.AuthorVersion != preview.Version ||
			installed.Source.Surface != string(surface) ||
			(installed.Snapshot.Legacy != nil && installed.Snapshot.Legacy.PackageFingerprint != preview.PackageFingerprint) {
			return SkillPackageInstallResult{}, apperror.New(apperror.CodeInternal, "Plugin installation violated its confirmed preview")
		}
		return SkillPackageInstallResult{ProtocolVersion: plugins.PortableInstallationProtocol, Installation: installed,
			Name: installed.DisplayName(), Version: installed.Snapshot.AuthorVersion, Surface: string(surface),
			ArchiveSHA256: installed.ArchiveSHA256, PackageFingerprint: installed.PackageFingerprint, Replayed: result.Replayed}, nil
	}
	installation := result.Package.Installation
	if installation.Name != preview.Name || installation.Version != preview.Version ||
		installation.ArchiveSHA256 != preview.ArchiveSHA256 ||
		installation.PackageFingerprint != preview.PackageFingerprint ||
		installation.Surface != surface {
		return SkillPackageInstallResult{}, apperror.New(apperror.CodeInternal,
			"desktop Skill installation result violated its preview binding")
	}
	return SkillPackageInstallResult{
		ProtocolVersion: SkillPackageInstallProtocolVersion,
		Name:            installation.Name, Version: installation.Version,
		Surface: string(installation.Surface), TrustClass: string(installation.TrustClass),
		ArchiveSHA256:      installation.ArchiveSHA256,
		PackageFingerprint: installation.PackageFingerprint,
		Replayed:           result.Replayed, RecoveredPending: result.RecoveredPending,
		ImportCommandExecution:     installation.ImportCommandExecution,
		ImportNetworkAccess:        installation.ImportNetworkAccess,
		ImportProviderCalls:        installation.ImportProviderCalls,
		ToolCapabilityGrant:        installation.ToolCapabilityGrant,
		RunSelectionAuthorized:     installation.RunSelectionAuthorized,
		ContextInjectionAuthorized: installation.ContextInjectionAuthorized,
		Receipt: operationreceipt.Settled(operationreceipt.KindSkillPackageInstall,
			result.Replayed, false),
	}, nil
}

func (b *DesktopBridge) lifecycleContext() (context.Context, error) {
	ctx := b.contextProvider()
	if ctx == nil {
		return nil, apperror.New(apperror.CodeFailedPrecondition,
			"desktop lifecycle is not ready")
	}
	if err := ctx.Err(); err != nil {
		return nil, apperror.Normalize(err)
	}
	return ctx, nil
}

func validDesktopToken(value string) bool {
	if value != strings.TrimSpace(value) || !utf8.ValidString(value) ||
		len([]byte(value)) < desktopTokenMinBytes || len([]byte(value)) > desktopTokenMaxBytes {
		return false
	}
	for _, current := range value {
		if unicode.IsControl(current) || unicode.IsSpace(current) {
			return false
		}
	}
	return true
}

func validSHA256(value string) bool {
	if len(value) != sha256.Size*2 || strings.ToLower(value) != value {
		return false
	}
	raw, err := hex.DecodeString(value)
	return err == nil && len(raw) == sha256.Size
}

// SkillPackageInventoryRequest selects the historical Registry scope to show.
// Filters stay inert: listing never authorises selection or execution.
type SkillPackageInventoryRequest struct {
	ProtocolVersion string `json:"protocol_version"`
	Surface         string `json:"surface"`
	Profile         string `json:"profile"`
	IncludeRemoved  bool   `json:"include_removed"`
}

type SkillPackageRemovalRequest struct {
	ProtocolVersion string `json:"protocol_version"`
	Skill           string `json:"skill"`
	OperationKey    string `json:"operation_key"`
	ConfirmRemove   bool   `json:"confirm_remove"`
}

func (b *DesktopBridge) ListSkillPackages(
	request SkillPackageInventoryRequest,
) (httpapi.SkillPackageInventoryView, error) {
	empty := httpapi.SkillPackageInventoryView{}
	if b == nil || !b.bootstrap.SkillInstallationEnabled || b.skillRegistry == nil {
		return empty, apperror.New(apperror.CodeNotFound,
			"desktop Skill package Registry is disabled")
	}
	if request.ProtocolVersion != SkillPackageInventoryProtocolVersion {
		return empty, apperror.New(apperror.CodeInvalidArgument,
			"desktop Skill inventory protocol is invalid")
	}
	var surface domain.ExecutionSurface
	if request.Surface != "" {
		parsed, err := domain.ParseExecutionSurface(request.Surface)
		if err != nil {
			return empty, apperror.Normalize(err)
		}
		surface = parsed
	}
	var profile domain.Profile
	if request.Profile != "" {
		parsed, err := domain.ParseProfile(request.Profile)
		if err != nil || parsed != domain.Profile(request.Profile) {
			return empty, apperror.New(apperror.CodeInvalidArgument,
				"installed Skill package Profile is invalid")
		}
		profile = parsed
	}
	ctx, err := b.lifecycleContext()
	if err != nil {
		return empty, err
	}
	inventory, err := b.skillRegistry.Inventory(ctx,
		application.ListInstalledSkillPackagesRequest{Surface: surface,
			Profile: profile, IncludeRemoved: request.IncludeRemoved})
	if err != nil {
		return empty, apperror.Normalize(err)
	}
	return httpapi.ProjectSkillPackageInventory(string(surface), string(profile),
		request.IncludeRemoved, inventory), nil
}

func (b *DesktopBridge) RemoveSkillPackage(
	request SkillPackageRemovalRequest,
) (httpapi.SkillPackageRemovalView, error) {
	empty := httpapi.SkillPackageRemovalView{}
	if b == nil || !b.bootstrap.SkillInstallationEnabled || b.skillRegistry == nil {
		return empty, apperror.New(apperror.CodeNotFound,
			"desktop Skill package Registry is disabled")
	}
	if request.ProtocolVersion != SkillPackageRemovalProtocolVersion ||
		!request.ConfirmRemove {
		return empty, apperror.New(apperror.CodeFailedPrecondition,
			"desktop Skill package removal requires explicit operator confirmation")
	}
	name, version, err := skills.ParseInstalledPackageRef(request.Skill)
	if err != nil {
		return empty, apperror.New(apperror.CodeInvalidArgument,
			"installed Skill package reference is invalid")
	}
	ctx, err := b.lifecycleContext()
	if err != nil {
		return empty, err
	}
	result, err := b.skillRegistry.Remove(ctx, application.RemoveSkillPackageRequest{
		Name: name, Version: version, OperationKey: request.OperationKey,
		RemovedBy: "desktop_operator", ConfirmRemove: true})
	if err != nil {
		return empty, apperror.Normalize(err)
	}
	return httpapi.ProjectSkillPackageRemoval(result), nil
}
