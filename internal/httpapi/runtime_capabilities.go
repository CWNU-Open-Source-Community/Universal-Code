package httpapi

import (
	"net/http"

	"cyberagent-workbench/internal/apperror"
	"cyberagent-workbench/internal/application"
	"cyberagent-workbench/internal/scheduler"
)

const RuntimeCapabilitiesProtocolVersion = "runtime_capabilities.v1"

type RunWakeWorkerHealthSource interface {
	Health() application.RunWakeWorkerHealth
}

type ScheduledJobWorkerHealthSource interface {
	Health() scheduler.WorkerHealth
}

type ScheduledJobWorkerSelectionSource interface {
	SelectionScope() string
}

type RunWakeWorkerHealthView struct {
	ProtocolVersion        string `json:"protocol_version"`
	Enabled                bool   `json:"enabled"`
	State                  string `json:"state"`
	Active                 bool   `json:"active"`
	PollIntervalMillis     int64  `json:"poll_interval_ms"`
	Concurrency            int    `json:"concurrency"`
	MaxSteps               int    `json:"max_steps"`
	RuntimeEnableSupported bool   `json:"runtime_enable_supported"`
	PersistentService      bool   `json:"persistent_service"`
}

type ScheduledJobWorkerHealthView struct {
	SelectionScope         string `json:"selection_scope,omitempty"`
	ProtocolVersion        string `json:"protocol_version"`
	Enabled                bool   `json:"enabled"`
	State                  string `json:"state"`
	Active                 bool   `json:"active"`
	PollIntervalMillis     int64  `json:"poll_interval_ms"`
	Concurrency            int    `json:"concurrency"`
	RuntimeEnableSupported bool   `json:"runtime_enable_supported"`
	PersistentService      bool   `json:"persistent_service"`
	AuthorityEscalation    bool   `json:"authority_escalation"`
}

type CommandRuntimeAdapterView struct {
	Kind             string `json:"kind"`
	Backend          string `json:"backend"`
	BackendIdentity  string `json:"backend_identity"`
	IsolationGrade   string `json:"isolation_grade"`
	NetworkPolicy    string `json:"network_policy"`
	CredentialPolicy string `json:"credential_policy"`
	Ready            bool   `json:"ready"`
}

type RuntimeCapabilitiesView struct {
	ProtocolVersion                    string                       `json:"protocol_version"`
	RunControlEnabled                  bool                         `json:"run_control_enabled"`
	ExecutionPermissionControlEnabled  bool                         `json:"execution_permission_control_enabled"`
	WorkspaceSandboxEnabled            bool                         `json:"workspace_sandbox_enabled"`
	OperatorApprovalEnabled            bool                         `json:"operator_approval_enabled"`
	DangerFullAccessEnabled            bool                         `json:"danger_full_access_enabled"`
	CommandRuntimeEnabled              bool                         `json:"command_runtime_enabled"`
	CommandRuntimeProtocolAvailable    bool                         `json:"command_runtime_protocol_available"`
	CommandRuntimeAdapterInstalled     bool                         `json:"command_runtime_adapter_installed"`
	CommandRuntimeAdapterReady         bool                         `json:"command_runtime_adapter_ready"`
	CommandRuntimeAdapters             []CommandRuntimeAdapterView  `json:"command_runtime_adapters"`
	BrowserCDPPermissionControlEnabled bool                         `json:"browser_cdp_permission_control_enabled"`
	FullCDPDebugEnabled                bool                         `json:"full_cdp_debug_enabled"`
	FullCDPSessionControlEnabled       bool                         `json:"full_cdp_session_control_enabled"`
	RunCreationEnabled                 bool                         `json:"run_creation_enabled"`
	WorkspaceImportEnabled             bool                         `json:"workspace_import_enabled"`
	StandardCodePresetEnabled          bool                         `json:"standard_code_preset_enabled"`
	SessionMessageEnabled              bool                         `json:"session_message_enabled"`
	ThreadControlEnabled               bool                         `json:"thread_control_enabled"`
	SessionSteeringControlEnabled      bool                         `json:"session_steering_control_enabled"`
	RunLifecycleEnabled                bool                         `json:"run_lifecycle_enabled"`
	RunExecutionEnabled                bool                         `json:"run_execution_enabled"`
	ThreadExecutionReadEnabled         *bool                        `json:"thread_execution_read_enabled,omitempty"`
	PlanDeliveryControlEnabled         bool                         `json:"plan_delivery_control_enabled"`
	ApprovalControlEnabled             bool                         `json:"approval_control_enabled"`
	ModelControlEnabled                bool                         `json:"model_control_enabled"`
	ProviderCredentialEnabled          bool                         `json:"provider_credential_enabled"`
	ChatGPTAuthEnabled                 bool                         `json:"chatgpt_auth_enabled"`
	FileEditReviewEnabled              bool                         `json:"file_edit_review_enabled"`
	FileEditProposalEnabled            bool                         `json:"file_edit_proposal_enabled"`
	FileEditApplyEnabled               bool                         `json:"file_edit_apply_enabled"`
	RunWakeControlEnabled              bool                         `json:"run_wake_control_enabled"`
	RunWakeExecutionEnabled            bool                         `json:"run_wake_execution_enabled"`
	RunWakeWorkerEnabled               bool                         `json:"run_wake_worker_enabled"`
	ScheduledJobControlEnabled         bool                         `json:"scheduled_job_control_enabled"`
	ScheduledJobWorkerEnabled          bool                         `json:"scheduled_job_worker_enabled"`
	SkillInstallationEnabled           bool                         `json:"skill_installation_enabled"`
	EvidenceAttachmentEnabled          bool                         `json:"evidence_attachment_enabled"`
	VerificationEvidenceEnabled        bool                         `json:"verification_evidence_enabled"`
	EmbeddedAnalyzerExecutionEnabled   bool                         `json:"embedded_analyzer_execution_enabled"`
	WorkspaceCheckpointControlEnabled  bool                         `json:"workspace_checkpoint_control_enabled"`
	GitAdvancedControlEnabled          bool                         `json:"git_advanced_control_enabled"`
	GitHubReviewControlEnabled         bool                         `json:"github_review_control_enabled"`
	BatchDeliveryControlEnabled        bool                         `json:"batch_delivery_control_enabled"`
	BatchDeliveryHostValidationEnabled bool                         `json:"batch_delivery_host_validation_enabled"`
	UIEvidenceControlEnabled           bool                         `json:"ui_evidence_control_enabled"`
	ProcessExecutionEnabled            bool                         `json:"process_execution_enabled"`
	ShellExecutionEnabled              bool                         `json:"shell_execution_enabled"`
	DockerExecutionEnabled             bool                         `json:"docker_execution_enabled"`
	AgentCodeToolsEnabled              bool                         `json:"agent_code_tools_enabled"`
	CodeIntelEnabled                   bool                         `json:"code_intel_enabled"`
	WakeWorker                         RunWakeWorkerHealthView      `json:"wake_worker"`
	ScheduledJobWorker                 ScheduledJobWorkerHealthView `json:"scheduled_job_worker"`
}

func (a *API) runtimeCapabilities(request *http.Request) (any, *Page, error) {
	if err := rejectQuery(request.URL.Query()); err != nil {
		return nil, nil, err
	}
	worker := RunWakeWorkerHealthView{
		ProtocolVersion: application.RunWakeWorkerHealthProtocolVersion,
		Enabled:         false, State: "disabled", Active: false,
		Concurrency:            application.RunWakeWorkerConcurrency,
		MaxSteps:               application.RunWakeWorkerMaxSteps,
		RuntimeEnableSupported: false, PersistentService: false,
	}
	if a.runWakeWorkerEnabled {
		if a.runWakeWorkerHealthSource == nil {
			return nil, nil, apperror.New(apperror.CodeInternal,
				"Run wake worker health source is unavailable")
		}
		health := a.runWakeWorkerHealthSource.Health()
		if health.ProtocolVersion != application.RunWakeWorkerHealthProtocolVersion ||
			!validRunWakeWorkerHealthState(health.State, health.Active) || health.PollIntervalMillis <
			application.MinRunWakeWorkerPollInterval.Milliseconds() ||
			health.PollIntervalMillis > application.MaxRunWakeWorkerPollInterval.Milliseconds() ||
			health.Concurrency != application.RunWakeWorkerConcurrency ||
			health.MaxSteps != application.RunWakeWorkerMaxSteps {
			return nil, nil, apperror.New(apperror.CodeInternal,
				"Run wake worker health violated its bounded contract")
		}
		worker.Enabled = true
		worker.State = string(health.State)
		worker.Active = health.Active
		worker.PollIntervalMillis = health.PollIntervalMillis
	}
	scheduledWorker := ScheduledJobWorkerHealthView{
		ProtocolVersion: scheduler.WorkerHealthProtocolVersion,
		State:           "disabled", Concurrency: scheduler.WorkerConcurrency,
		RuntimeEnableSupported: false, PersistentService: false,
		AuthorityEscalation: false,
	}
	if a.scheduledJobWorkerEnabled {
		if a.scheduledJobWorkerHealthSource == nil {
			return nil, nil, apperror.New(apperror.CodeInternal,
				"scheduled job worker health source is unavailable")
		}
		health := a.scheduledJobWorkerHealthSource.Health()
		scheduledWorker.SelectionScope = "all_jobs"
		if source, ok := a.scheduledJobWorkerHealthSource.(ScheduledJobWorkerSelectionSource); ok {
			scheduledWorker.SelectionScope = source.SelectionScope()
		}
		if scheduledWorker.SelectionScope != "all_jobs" && scheduledWorker.SelectionScope != "confirmed_read_only" {
			return nil, nil, apperror.New(apperror.CodeInternal,
				"scheduled job worker selection scope is invalid")
		}
		if health.ProtocolVersion != scheduler.WorkerHealthProtocolVersion ||
			!validScheduledJobWorkerHealthState(health.State, health.Active) ||
			health.PollIntervalMillis < scheduler.MinPollInterval.Milliseconds() ||
			health.PollIntervalMillis > scheduler.MaxPollInterval.Milliseconds() ||
			health.Concurrency != scheduler.WorkerConcurrency {
			return nil, nil, apperror.New(apperror.CodeInternal,
				"scheduled job worker health violated its bounded contract")
		}
		scheduledWorker.Enabled = true
		scheduledWorker.State = string(health.State)
		scheduledWorker.Active = health.Active
		scheduledWorker.PollIntervalMillis = health.PollIntervalMillis
	}
	commandRuntimeAdapters := make([]CommandRuntimeAdapterView, 0,
		len(a.commandRuntimeAdapters))
	commandRuntimeReady := false
	for _, adapter := range a.commandRuntimeAdapters {
		ready := true
		switch adapter.Backend {
		case application.CommandRuntimeLocalSandboxBackend:
			ready = a.capabilityReadinessRuntime.LocalBackendReady
		case application.CommandRuntimeDockerSandboxBackend:
			ready = a.capabilityReadinessRuntime.DockerBackendReady
		}
		commandRuntimeReady = commandRuntimeReady || ready
		commandRuntimeAdapters = append(commandRuntimeAdapters,
			CommandRuntimeAdapterView{Kind: string(adapter.Kind), Backend: adapter.Backend,
				BackendIdentity:  adapter.BackendIdentity,
				IsolationGrade:   string(adapter.IsolationGrade),
				NetworkPolicy:    string(adapter.NetworkPolicy),
				CredentialPolicy: string(adapter.CredentialPolicy), Ready: ready})
	}
	commandRuntimeEnabled := a.runExecutionEnabled && commandRuntimeReady
	_, threadExecutionControllerPresent := a.threadTurnController.(ThreadExecutionController)
	threadExecutionReadable := a.runExecutionEnabled && threadExecutionControllerPresent
	return RuntimeCapabilitiesView{
		ProtocolVersion:   RuntimeCapabilitiesProtocolVersion,
		RunControlEnabled: a.controlEnabled, RunCreationEnabled: a.runCreationEnabled,
		WorkspaceImportEnabled:             a.workspaceImportEnabled,
		StandardCodePresetEnabled:          a.standardCodePresetEnabled,
		ExecutionPermissionControlEnabled:  a.executionPermissionControlEnabled,
		WorkspaceSandboxEnabled:            a.executionPermissionCapabilities.WorkspaceSandboxEnabled,
		OperatorApprovalEnabled:            a.executionPermissionCapabilities.OperatorApprovalEnabled,
		DangerFullAccessEnabled:            a.executionPermissionCapabilities.DangerFullAccessEnabled,
		CommandRuntimeEnabled:              commandRuntimeEnabled,
		CommandRuntimeProtocolAvailable:    true,
		CommandRuntimeAdapterInstalled:     len(commandRuntimeAdapters) > 0,
		CommandRuntimeAdapterReady:         commandRuntimeReady,
		CommandRuntimeAdapters:             commandRuntimeAdapters,
		BrowserCDPPermissionControlEnabled: a.browserCDPPermissionControlEnabled,
		FullCDPDebugEnabled:                a.browserCDPPermissionCapabilities.FullDebugEnabled,
		FullCDPSessionControlEnabled:       a.fullCDPSessionControlEnabled,
		SessionMessageEnabled:              a.sessionMessageEnabled,
		ThreadControlEnabled:               a.runCreationEnabled && a.sessionMessageEnabled,
		SessionSteeringControlEnabled:      a.sessionSteeringControlEnabled,
		RunLifecycleEnabled:                a.runLifecycleEnabled, RunExecutionEnabled: a.runExecutionEnabled,
		ThreadExecutionReadEnabled:         &threadExecutionReadable,
		PlanDeliveryControlEnabled:         a.planDeliveryControlEnabled,
		ApprovalControlEnabled:             a.approvalControlEnabled,
		ModelControlEnabled:                a.modelControlEnabled,
		ProviderCredentialEnabled:          a.providerCredentialEnabled,
		ChatGPTAuthEnabled:                 a.chatGPTAuthEnabled,
		FileEditReviewEnabled:              a.fileEditReviewEnabled,
		FileEditProposalEnabled:            a.fileEditProposalEnabled,
		FileEditApplyEnabled:               a.fileEditApplyEnabled,
		RunWakeControlEnabled:              a.runWakeControlEnabled,
		RunWakeExecutionEnabled:            a.runWakeExecutionEnabled,
		RunWakeWorkerEnabled:               a.runWakeWorkerEnabled,
		ScheduledJobControlEnabled:         a.scheduledJobControlEnabled,
		ScheduledJobWorkerEnabled:          a.scheduledJobWorkerEnabled,
		SkillInstallationEnabled:           a.skillInstallationEnabled,
		EvidenceAttachmentEnabled:          a.evidenceAttachmentEnabled,
		VerificationEvidenceEnabled:        a.verificationEvidenceEnabled,
		EmbeddedAnalyzerExecutionEnabled:   a.embeddedAnalyzerExecutionEnabled,
		WorkspaceCheckpointControlEnabled:  a.workspaceCheckpointControlEnabled,
		GitAdvancedControlEnabled:          a.gitAdvancedControlEnabled,
		GitHubReviewControlEnabled:         a.githubReviewControlEnabled,
		BatchDeliveryControlEnabled:        a.batchDeliveryControlEnabled,
		BatchDeliveryHostValidationEnabled: a.batchDeliveryHostValidationEnabled,
		UIEvidenceControlEnabled:           a.uiEvidenceControlEnabled,
		ProcessExecutionEnabled:            commandRuntimeEnabled,
		ShellExecutionEnabled:              commandRuntimeEnabled,
		DockerExecutionEnabled:             a.dockerExecutionEnabled, AgentCodeToolsEnabled: true,
		CodeIntelEnabled: a.codeIntelSource != nil,
		WakeWorker:       worker, ScheduledJobWorker: scheduledWorker,
	}, nil, nil
}

func validScheduledJobWorkerHealthState(state scheduler.WorkerState, active bool) bool {
	switch state {
	case scheduler.WorkerReady, scheduler.WorkerStopped:
		return !active
	case scheduler.WorkerRunning, scheduler.WorkerDraining:
		return true
	default:
		return false
	}
}

func validRunWakeWorkerHealthState(state application.RunWakeWorkerState, active bool) bool {
	switch state {
	case application.RunWakeWorkerReady, application.RunWakeWorkerStopped:
		return !active
	case application.RunWakeWorkerRunning, application.RunWakeWorkerDraining:
		return true
	default:
		return false
	}
}
