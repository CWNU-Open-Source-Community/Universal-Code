import type { RuntimeCapabilitiesView } from "../types";

export interface ClientCapabilities {
  runControlEnabled?: boolean;
  workspaceImportEnabled?: boolean;
  projectlessWorkspacesEnabled?: boolean;
  executionPermissionControlEnabled?: boolean;
  workspaceSandboxEnabled?: boolean;
  browserCDPPermissionControlEnabled?: boolean;
  fullCDPDebugEnabled?: boolean;
  fullCDPSessionControlEnabled?: boolean;
  operatorApprovalEnabled?: boolean;
  dangerFullAccessEnabled?: boolean;
  commandRuntimeEnabled?: boolean;
  commandRuntimeProtocolAvailable?: boolean;
  commandRuntimeAdapterInstalled?: boolean;
  commandRuntimeAdapterReady?: boolean;
  runCreationEnabled?: boolean;
  standardCodePresetEnabled?: boolean;
  sessionMessageEnabled?: boolean;
  threadControlEnabled?: boolean;
  sessionSteeringControlEnabled?: boolean;
  runLifecycleEnabled?: boolean;
  runExecutionEnabled?: boolean;
  threadExecutionReadEnabled?: boolean;
  planDeliveryControlEnabled?: boolean;
  approvalControlEnabled?: boolean;
  modelControlEnabled?: boolean;
  providerCredentialEnabled?: boolean;
  chatGPTAuthEnabled?: boolean;
  fileEditReviewEnabled?: boolean;
  fileEditProposalEnabled?: boolean;
  fileEditApplyEnabled?: boolean;
  runWakeControlEnabled?: boolean;
  runWakeExecutionEnabled?: boolean;
  runWakeWorkerEnabled?: boolean;
  scheduledJobControlEnabled?: boolean;
  scheduledJobWorkerEnabled?: boolean;
  skillInstallationEnabled?: boolean;
  evidenceAttachmentEnabled?: boolean;
  verificationEvidenceEnabled?: boolean;
  uiEvidenceControlEnabled?: boolean;
  embeddedAnalyzerExecutionEnabled?: boolean;
  workspaceCheckpointControlEnabled?: boolean;
  gitAdvancedControlEnabled?: boolean;
  githubReviewControlEnabled?: boolean;
  batchDeliveryControlEnabled?: boolean;
  batchDeliveryHostValidationEnabled?: boolean;
  extensionControlEnabled?: boolean;
  dockerExecutionEnabled?: boolean;
  agentCodeToolsEnabled?: boolean;
  codeIntelEnabled?: boolean;
}

export function clientCapabilitiesFromRuntime(value: RuntimeCapabilitiesView): ClientCapabilities {
  return {
    runControlEnabled: value.run_control_enabled,
    workspaceImportEnabled: value.workspace_import_enabled === true,
    projectlessWorkspacesEnabled: value.projectless_workspaces_enabled === true,
    executionPermissionControlEnabled: value.execution_permission_control_enabled,
    workspaceSandboxEnabled: value.workspace_sandbox_enabled,
    browserCDPPermissionControlEnabled: value.browser_cdp_permission_control_enabled,
    fullCDPDebugEnabled: value.full_cdp_debug_enabled,
    fullCDPSessionControlEnabled: value.full_cdp_session_control_enabled,
    operatorApprovalEnabled: value.operator_approval_enabled,
    dangerFullAccessEnabled: value.danger_full_access_enabled,

    commandRuntimeEnabled: value.command_runtime_enabled,
    commandRuntimeProtocolAvailable: value.command_runtime_protocol_available,
    commandRuntimeAdapterInstalled: value.command_runtime_adapter_installed,
    commandRuntimeAdapterReady: value.command_runtime_adapter_ready,
    runCreationEnabled: value.run_creation_enabled,
    standardCodePresetEnabled: value.standard_code_preset_enabled,
    sessionMessageEnabled: value.session_message_enabled,
    threadControlEnabled: value.thread_control_enabled,
    sessionSteeringControlEnabled: value.session_steering_control_enabled,
    runLifecycleEnabled: value.run_lifecycle_enabled,
    runExecutionEnabled: value.run_execution_enabled,
    threadExecutionReadEnabled: value.thread_execution_read_enabled === true,
    planDeliveryControlEnabled: value.plan_delivery_control_enabled,
    approvalControlEnabled: value.approval_control_enabled,


    modelControlEnabled: value.model_control_enabled,
    providerCredentialEnabled: value.provider_credential_enabled,
    chatGPTAuthEnabled: value.chatgpt_auth_enabled === true,
    fileEditReviewEnabled: value.file_edit_review_enabled,
    fileEditProposalEnabled: value.file_edit_proposal_enabled,
    fileEditApplyEnabled: value.file_edit_apply_enabled,
    runWakeControlEnabled: value.run_wake_control_enabled,
    runWakeExecutionEnabled: value.run_wake_execution_enabled,
    runWakeWorkerEnabled: value.run_wake_worker_enabled,
    scheduledJobControlEnabled: value.scheduled_job_control_enabled,
    scheduledJobWorkerEnabled: value.scheduled_job_worker_enabled,
    skillInstallationEnabled: value.skill_installation_enabled,
    evidenceAttachmentEnabled: value.evidence_attachment_enabled,
    verificationEvidenceEnabled: value.verification_evidence_enabled,
    embeddedAnalyzerExecutionEnabled: value.embedded_analyzer_execution_enabled,
    workspaceCheckpointControlEnabled: value.workspace_checkpoint_control_enabled,
    gitAdvancedControlEnabled: value.git_advanced_control_enabled,
    githubReviewControlEnabled: value.github_review_control_enabled,
    batchDeliveryControlEnabled: value.batch_delivery_control_enabled,
    batchDeliveryHostValidationEnabled: value.batch_delivery_host_validation_enabled,
    uiEvidenceControlEnabled: value.ui_evidence_control_enabled,
    dockerExecutionEnabled: value.docker_execution_enabled,
    agentCodeToolsEnabled: value.agent_code_tools_enabled,
    codeIntelEnabled: value.code_intel_enabled,
  };
}

// Feature decisions are computed once by APIClient and shared by its domain clients.
export interface ClientFeatures {
  readonly hasControl: boolean;
  readonly hasWorkspaceImport: boolean;
  readonly hasProjectlessWorkspaces: boolean;
  readonly hasExecutionPermissionControl: boolean;
  readonly hasBrowserCDPPermissionControl: boolean;
  readonly hasFullCDPDebug: boolean;
  readonly hasFullCDPSessionControl: boolean;
  readonly hasRunCreation: boolean;
  readonly hasStandardCodePreset: boolean;
  readonly hasSessionMessages: boolean;
  readonly hasThreadControl: boolean;
  readonly hasSessionSteeringControl: boolean;
  readonly hasRunLifecycle: boolean;
  readonly hasRunExecution: boolean;
  readonly hasThreadExecutionRead: boolean;
  readonly hasPlanDelivery: boolean;
  readonly hasApprovalControl: boolean;
  readonly hasModelControl: boolean;
  readonly hasProviderDefinitions: boolean;
  readonly hasProviderCredentials: boolean;
  readonly hasChatGPTAuth: boolean;
  readonly hasFileEditReview: boolean;
  readonly hasFileEditProposals: boolean;
  readonly hasFileEditApply: boolean;
  readonly hasRunWakeControl: boolean;
  readonly hasRunWakeExecution: boolean;
  readonly hasRunWakeWorker: boolean;
  readonly hasScheduledJobControl: boolean;
  readonly hasScheduledJobWorker: boolean;
  readonly hasSkillInstallation: boolean;
  readonly hasEvidenceAttachment: boolean;
  readonly hasVerificationEvidence: boolean;
  readonly hasEmbeddedAnalyzerExecution: boolean;
  readonly hasWorkspaceCheckpointControl: boolean;
  readonly hasGitAdvancedControl: boolean;
  readonly hasGitHubReviewControl: boolean;
  readonly hasBatchDeliveryControl: boolean;
  readonly hasBatchDeliveryHostValidation: boolean;
  readonly hasExtensionControl: boolean;
  readonly hasUIEvidence: boolean;
  readonly uiEvidenceUnavailableReason: "missing_control_credential" | "ui_evidence_disabled" |
    "run_execution_disabled" | "browser_cdp_control_disabled" | null;
}
