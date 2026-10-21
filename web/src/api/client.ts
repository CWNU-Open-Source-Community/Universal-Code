import {
  applicationServiceIdentity,
  parseThreadApplicationService,
  parseThreadApplicationServiceStop,
  parseThreadApplicationServices,
} from "./application-services";
import { chatGPTAuthProtocol, parseChatGPTAuthStatus, validChatGPTIdentity } from "./chatgpt-auth";
import {
  APIRequestError,
  isErrorEnvelope,
  isSuccessEnvelope,
  normalizeBaseURL,
  parseResponsePage,
} from "./client/protocol";
import { parseThreadActivityArtifact, parseThreadActivityDetail } from "./client/responses/activity";
import {
  parseApprovalDecision,
  parseApprovalQueue,
  parseControlledCommandProposal,
  parseHostCommandProposal,
} from "./client/responses/approvals";
import { parseFullCDPSessionControl } from "./client/responses/browser";
import {
  batchValidationKinds,
  isBatchPath,
  parseBatchDeliveries,
  parseBatchDeliveryCancel,
  parseBatchDeliveryMergeControl,
  parseBatchDeliveryReconcile,
  parseBatchDeliveryReviewControl,
  parseBatchDeliverySnapshot,
  parseBatchWorkbench,
} from "./client/responses/delivery";
import { parseDiagnosticBundle } from "./client/responses/diagnostics";
import { parseEvidenceAttachment, parseEvidenceInventory } from "./client/responses/evidence";
import {
  parseCodeIntelConfiguration,
  parseCodeIntelConfigurationTest,
  parseCodeIntelInventory,
  parseExtensionInventory,
  parseExtensionMCPServer,
  parseExtensionPlugin,
  parseHookDiagnostics,
  parseMCPRegistration,
  parsePluginHistory,
  parsePluginImport,
  parsePluginPublisher,
  pluginCapabilities,
  strictCodeIntelIdentity,
  validCanonicalBase64,
} from "./client/responses/extensions";
import {
  parseFileEditApply,
  parseFileEditChangeSet,
  parseFileEditPreview,
  parseFileEditProposal,
  parseFileEditProposalRecovery,
  parseFileEditProposalSource,
  parseFileEditQueue,
  parseFileEditRevertProposal,
  parseFileEditReview,
} from "./client/responses/file-edits";
import { parseCodeHandoff, parseCodeHandoffExport } from "./client/responses/handoffs";
import {
  parseModelCancellation,
  parsePublicModelStream,
  parsePublicModelStreamPoll,
  parseSpecialistModelCancellation,
} from "./client/responses/model-streams";
import {
  parseAvailableModelRouteCollection,
  parseModelAvailability,
  parseModelHarnessQualification,
  parseModelRouteControl,
  parsePriceSnapshotImport,
  parsePriceSnapshots,
  parseProviderCredentialList,
  parseProviderCredentialStatus,
  parseProviderDefinition,
  parseProviderDefinitionCollection,
  parseProviderDefinitionMutation,
  parseProviderDiagnostic,
  parseProviderSearchReadiness,
  parseSearchDiagnostics,
  parseThreadModelRoute,
  validCustomProviderID,
  validProviderAdvancedConfig,
  validProviderCredentialName,
} from "./client/responses/models";
import { parseOperationReceiptHistory, parseOperatorActionCenter } from "./client/responses/operations";
import {
  normalizeRequestedNetworkAuthority,
  normalizeRequestedWebAccess,
  parseRunNetworkAuthorityControl,
  parseRunWebAccessControl,
} from "./client/responses/permissions";
import {
  parsePlanDeliveryTransition,
  parsePlanDirectionControl,
  parsePlanModeTransition,
  validPlanWorkItemResult,
  validatePlanWorkItemRequest,
} from "./client/responses/plans";
import {
  parseRepositoryCommitComparison,
  parseRepositoryCommitDetail,
  parseRepositoryCommitFilePreview,
  parseRepositoryDiff,
  parseRepositoryFileHistory,
  parseRepositoryHistory,
  parseRepositoryState,
} from "./client/responses/repository";
import {
  parseChildTaskProposal,
  parseChildTaskProposals,
  parseEventPoll,
  parseFanoutExecution,
  parseFanoutExecutions,
  parseRunCreationControl,
  parseRunExecutionControl,
  parseRunLifecycleControl,
  parseStreamFrame,
} from "./client/responses/runs";
import {
  parseEmbeddedAnalyzerExecution,
  parseRunCapabilityReadiness,
  parseRuntimeCapabilities,
  parseSafeWebReadiness,
  parseStandardCodePreset,
} from "./client/responses/runtime";
import {
  parseRunWakeControl,
  parseRunWakeExecution,
  parseRunWakeState,
  parseScheduledJobControl,
  parseScheduledJobDetail,
  parseScheduledJobList,
} from "./client/responses/scheduling";
import {
  parseSessionArchiveControl,
  parseSessionMessageControl,
  parseSessionSteeringCancellation,
  parseThreadApprovalPreference,
  parseThreadCreationControl,
  parseThreadExecution,
  parseThreadLifecycleControl,
  parseThreadMessageControl,
  parseThreadRunRecoveryControl,
  parseThreadView,
} from "./client/responses/sessions";
import {
  assertSkillSelectionChoice,
  assertSkillSelectionReceipt,
  parseSkillCatalogPublisher,
  parseSkillPackageInstall,
  parseSkillPackageInventory,
  parseSkillPackageRemoval,
} from "./client/responses/skills";
import { parseThreadTranscriptItems } from "./client/responses/transcripts";
import { parseUIEvidenceAttempt, parseUIEvidenceBundle } from "./client/responses/ui-evidence";
import {
  parseVerificationAssociation,
  parseVerificationEvidenceInventory,
  parseVerificationEvidenceItem,
  parseVerificationPlan,
  parseVerificationPlanCoverage,
  parseVerificationPlanInventory,
  parseVerificationPlanItemCoverage,
  parseVerificationSnapshotExport,
  parseVerificationSnapshotReceipt,
  parseVerificationSnapshotReceiptInventory,
  parseVerificationSnapshotReceiptReview,
  parseVerificationSnapshotReceiptReviewInventory,
  validVerificationText,
} from "./client/responses/verification";
import {
  normalizeWorkspaceName,
  parseProjectlessDirectory,
  parseWorkspaceExplorer,
  parseWorkspaceProject,
  parseWorkspaceSearch,
} from "./client/responses/workspaces";
import {
  boundedIdentity,
  boundedStringArray,
  boundedText,
  hasExactKeys,
  hasRequiredOnlyKeys,
  isRecord,
  isSHA256,
  safeBoundedCount,
  safePositiveInteger,
  validDate,
  validWorkspaceRelativePath,
} from "./client/validation";
import { parseThreadCommandServicePolicy, validCommandServicePolicyRequest } from "./command-service-policy";
import { maximumFileBytes, validFileAttachment } from "./file-attachments";
import {
  isGitAdvancedExecuteResult,
  isGitAdvancedProjection,
  isGitAdvancedReviewResult,
  validGitAdvancedSpec,
} from "./git-advanced";
import {
  parseGitHubDeviceAuthorization,
  parseGitHubDevicePoll,
  parseGitHubEvidence,
  parseGitHubFetch,
  parseGitHubProjection,
  parseGitHubQualification,
  parseGitHubReviewConfigure,
  parseGitHubReviewConnections,
  parseGitHubReviewCredential,
  parseGitHubWriteExecute,
  parseGitHubWriteReview,
} from "./github-review";
import { acceptedImageTypes, maximumImageBytes, validImageAttachment } from "./image-attachments";
import { validMCPConnectionConfig } from "./mcp-config";
import { parseMCPCredentialStatus, validMCPBearerSecret, validMCPCredentialBinding } from "./mcp-credentials";
import { parseProjectActionResult, parseProjectActions } from "./project-actions";
import {
  normalizePreparationTarget,
  parseProjectPreparation,
  parseProjectPreparationOperation,
  validPreparationTarget,
  validProjectPreparationRequest,
} from "./project-preparation";
import {
  parseSandboxEnvironment,
  sandboxSettingsMatch,
  validSandboxEnvironmentRequest,
} from "./sandbox-environment";
import { consumeSSE } from "./sse";
import { parseStandardCodeDelivery } from "./standard-code-delivery";
import { normalizedTaskBudget, parseTaskConfiguration } from "./task-configuration";
import { parseTaskSkillCandidates } from "./task-skill-candidates";
import { WorkflowClient } from "./workflows";
import type {
  ChatGPTAccountRequestView,
  ChatGPTAuthRequestView,
  ChatGPTAuthStatusView,
  ChatGPTCancelRequestView,
  ChatGPTLoginRequestView,
} from "./chatgpt-auth";
import type { ClientCapabilities } from "./client/capabilities";
import type { QueryValue } from "./client/protocol";
import type { ThreadCommandServicePolicyRequest } from "./command-service-policy";
import type { WorkspaceFileAttachment } from "./file-attachments";
import type { WorkspaceImageAttachment } from "./image-attachments";
import type { ProjectActionSlot } from "./project-actions";
import type {
  ProjectPreparationOperation,
  ProjectPreparationRequest,
  ProjectPreparationView,
} from "./project-preparation";
import type { TaskSkillCandidateScope } from "./task-skill-candidates";
import type { ThreadChangeSummary, ThreadChangeUndo } from "./thread-changes";
import type {
  ApprovalDecisionControlRequestView,
  ApprovalDecisionControlView,
  ApprovalPreviewView,
  ApprovalQueueView,
  ArtifactView,
  AvailableModelRouteCollectionView,
  BatchDeliveriesListView,
  BatchDeliveryCancelRequestView,
  BatchDeliveryCancelView,
  BatchDeliveryMergeControlView,
  BatchDeliveryMergeRequestView,
  BatchDeliveryReconcileRequestView,
  BatchDeliveryReconcileView,
  BatchDeliveryReviewControlView,
  BatchDeliveryReviewRequestView,
  BatchDeliverySnapshotView,
  BatchWorkbenchExecuteRequestView,
  BatchWorkbenchOwnerRequestView,
  BatchWorkbenchPrepareRequestView,
  BatchWorkbenchView,
  BrowserSafeWebReadiness,
  ChildTaskAdmitRequestView,
  ChildTaskProposalView,
  ChildTaskProposalsListView,
  ChildTaskReviewRequestView,
  CodeHandoffExportView,
  CodeHandoffView,
  CodeIntelConfigurationRequestView,
  CodeIntelConfigurationReviewRequestView,
  CodeIntelConfigurationTestRequestView,
  CodeIntelConfigurationTestView,
  CodeIntelConfigurationView,
  CodeIntelInventoryView,
  ControlledCommandProposalView,
  DiagnosticBundleView,
  DockerEnvironmentView,
  DockerSandboxAdmissionRequestView,
  DockerSandboxAdmissionView,
  DockerSandboxCancelRequestView,
  DockerSandboxCancellationView,
  DockerSandboxStartRequestView,
  DockerSandboxStatusView,
  EmbeddedAnalyzerExecutionControlView,
  EmbeddedAnalyzerExecutionRequestView,
  EvidenceAttachmentRequestView,
  EvidenceAttachmentView,
  EvidenceInventoryView,
  ExtensionInventoryView,
  ExtensionMCPRegistrationRequestView,
  ExtensionMCPRegistrationView,
  ExtensionMCPReviewRequestView,
  ExtensionMCPServerView,
  ExtensionPluginImportRequestView,
  ExtensionPluginImportView,
  ExtensionPluginInstallationView,
  ExtensionPluginReviewRequestView,
  ExternalSkillProjectionView,
  FanoutExecutionCancelRequestView,
  FanoutExecutionView,
  FanoutExecutionsListView,
  FileEditApplyRequestView,
  FileEditApplyView,
  FileEditChangeSetView,
  FileEditPreviewView,
  FileEditProposalRecoveryView,
  FileEditProposalRequestView,
  FileEditProposalSourceView,
  FileEditProposalView,
  FileEditQueueView,
  FileEditRevertProposalView,
  FileEditReviewRequestView,
  FileEditReviewView,
  FullCDPSessionCloseRequestView,
  FullCDPSessionControlView,
  FullCDPSessionOpenRequestView,
  GitAdvancedExecuteResultView,
  GitAdvancedProjectionView,
  GitAdvancedReviewResultView,
  GitAdvancedScopeView,
  GitAdvancedSpecView,
  GitHubReviewConfigureResultView,
  GitHubReviewCredentialView,
  GitHubReviewDeviceAuthorizationView,
  GitHubReviewDevicePollResultView,
  GitHubReviewEvidenceResultView,
  GitHubReviewFetchResultView,
  GitHubReviewProjectionView,
  GitHubReviewQualificationResultView,
  GitHubReviewWriteExecuteResultView,
  GitHubReviewWriteReviewResultView,
  GitHubReviewWriteSpecView,
  HealthView,
  HookDiagnosticsView,
  HostCommandProposalView,
  MCPCredentialBindingView,
  MCPCredentialRequestView,
  MCPCredentialStatusView,
  ModelAvailabilityView,
  ModelCancellationRequestView,
  ModelCancellationView,
  ModelHarnessQualificationRequestView,
  ModelHarnessQualificationView,
  ModelRouteControlRequestView,
  NoteView,
  OperationReceiptHistoryView,
  OperatorActionCenterView,
  Page,
  PageResult,
  PlanDeliveryCheckpointControlRequestView,
  PlanDeliveryCheckpointControlView,
  PlanDeliveryTransitionControlRequestView,
  PlanDeliveryTransitionControlView,
  PlanDeliveryWorkItemControlRequestView,
  PlanDeliveryWorkItemControlView,
  PlanDirectionControlRequestView,
  PlanDirectionControlView,
  PlanModeTransitionControlRequestView,
  PlanModeTransitionControlView,
  PluginHistoryView,
  PluginPublisherRevocationRequestView,
  PluginPublisherRevocationView,
  PluginPublisherTrustRequestView,
  PluginPublisherTrustResponseView,
  PluginRollbackRequestView,
  PluginRollbackView,
  PriceSnapshotImportRequestView,
  PriceSnapshotImportView,
  PriceSnapshotListView,
  ProjectlessDirectory,
  ProjectlessWorkspaceView,
  ProviderCredentialListView,
  ProviderCredentialRequestView,
  ProviderCredentialStatusView,
  ProviderDefinitionCollectionView,
  ProviderDefinitionDeleteRequestView,
  ProviderDefinitionMutationView,
  ProviderDefinitionUpsertRequestView,
  ProviderDiagnosticRequestView,
  ProviderDiagnosticView,
  ProviderModelDiscoveryRequestView,
  ProviderModelDiscoveryView,
  ProviderSearchReadinessView,
  PublicModelStreamSnapshot,
  RepositoryCommitComparisonView,
  RepositoryCommitDetailView,
  RepositoryCommitFilePreviewView,
  RepositoryDiffView,
  RepositoryFileHistoryView,
  RepositoryHistoryView,
  RepositoryStateView,
  RunCapabilityReadinessView,
  RunCreationControlRequestView,
  RunCreationControlView,
  RunEventPollView,
  RunEventStreamView,
  RunExecutionControlRequestView,
  RunExecutionControlView,
  RunLifecycleControlRequestView,
  RunLifecycleControlView,
  RunNetworkAuthorityControlRequestView,
  RunNetworkAuthorityControlView,
  RunWakeCancelRequestView,
  RunWakeControlView,
  RunWakeExecutionRequestView,
  RunWakeExecutionView,
  RunWakeScheduleRequestView,
  RunWakeStateView,
  RunWebAccessControlRequestView,
  RunWebAccessControlView,
  RuntimeCapabilitiesView,
  SandboxEnvironmentControlRequestView,
  SandboxEnvironmentView,
  ScheduledJobControlView,
  ScheduledJobCreateRequestView,
  ScheduledJobDetailView,
  ScheduledJobListView,
  ScheduledJobObservationRequestView,
  ScheduledJobTransitionRequestView,
  SearchDiagnosticsRequestView,
  SearchDiagnosticsView,
  SessionArchiveControlRequestView,
  SessionArchiveControlView,
  SessionMessageControlRequestView,
  SessionMessageControlView,
  SessionSteeringCancellationRequestView,
  SessionSteeringCancellationView,
  SkillCatalogPublisherControlRequestView,
  SkillCatalogPublisherView,
  SkillCatalogPublishersView,
  SkillPackageInstallRequestView,
  SkillPackageInstallResult,
  SkillPackageInventoryView,
  SkillPackageRemovalView,
  SkillPackageRemoveRequestView,
  SpecialistModelCancellationView,
  StandardCodeDeliveryRecordRequestView,
  StandardCodeDeliveryRecordResultView,
  StandardCodeDeliveryView,
  StandardCodePresetControlRequestView,
  StandardCodePresetControlView,
  SuccessEnvelope,
  ThreadActivityArtifactView,
  ThreadActivityDetailView,
  ThreadCreationControlRequestView,
  ThreadCreationControlView,
  ThreadExecutionPermissionControlRequestView,
  ThreadExecutionPermissionControlView,
  ThreadLifecycleControlRequestView,
  ThreadLifecycleControlView,
  ThreadMessageControlRequestView,
  ThreadMessageControlView,
  ThreadModelRouteControlRequestView,
  ThreadModelRouteView,
  ThreadRunRecoveryControlRequestView,
  ThreadRunRecoveryControlView,
  ThreadTurnControlRequestView,
  ThreadView,
  UIEvidenceArtifactMetadata,
  UIEvidenceAttempt,
  UIEvidenceBundle,
  UIEvidenceStartView,
  VerificationAssociationControlView,
  VerificationAssociationRequestView,
  VerificationEvidenceControlView,
  VerificationEvidenceInventoryView,
  VerificationEvidenceRequestView,
  VerificationPlanControlView,
  VerificationPlanCoverageInventoryView,
  VerificationPlanInventoryView,
  VerificationPlanItemCoverageDetailView,
  VerificationPlanItemCoveragePage,
  VerificationPlanRequestView,
  VerificationSnapshotExportView,
  VerificationSnapshotReceiptControlView,
  VerificationSnapshotReceiptInventoryView,
  VerificationSnapshotReceiptRequestView,
  VerificationSnapshotReceiptReviewControlView,
  VerificationSnapshotReceiptReviewInventoryView,
  VerificationSnapshotReceiptReviewRequestView,
  WorkItemView,
  WorkspaceExplorerView,
  WorkspaceImportView,
  WorkspaceProjectView,
  WorkspaceSearchView,
} from "./types";

export type { QueryValue } from "./client/protocol";
export type { ClientCapabilities } from "./client/capabilities";
export { APIRequestError } from "./client/protocol";
export { clientCapabilitiesFromRuntime } from "./client/capabilities";
export { parseSkillPackageInventory } from "./client/responses/skills";
export { parseSkillPackageRemoval } from "./client/responses/skills";
export { canonicalWebAccessSettings } from "./client/responses/permissions";
export { canonicalExactNetworkTarget } from "./client/responses/permissions";
export { parsePluginSkillInstall } from "./client/responses/skills";
export { parseProjectlessDirectory } from "./client/responses/workspaces";

export class APIClient {
  readonly baseURL: string;
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

  constructor(
    private readonly token: string,
    baseURL = import.meta.env.VITE_API_BASE_URL || "/api/v1",
    private readonly controlToken = "",
    capabilities: ClientCapabilities = {},
  ) {
    if (token.trim() === "") {
      throw new Error("A read bearer token is required");
    }
    this.baseURL = normalizeBaseURL(baseURL);
    const controlPresent = controlToken.trim() !== "";
    this.hasControl = controlPresent && (capabilities.runControlEnabled ?? true);
    this.hasWorkspaceImport = controlPresent && capabilities.workspaceImportEnabled === true;
    this.hasProjectlessWorkspaces = controlPresent && capabilities.projectlessWorkspacesEnabled === true;
    this.hasExecutionPermissionControl = controlPresent &&
      (capabilities.executionPermissionControlEnabled ?? false);
    this.hasBrowserCDPPermissionControl = controlPresent &&
      (capabilities.browserCDPPermissionControlEnabled ?? false);
    this.hasFullCDPDebug = this.hasBrowserCDPPermissionControl &&
      (capabilities.fullCDPDebugEnabled ?? false);
    this.hasFullCDPSessionControl = this.hasFullCDPDebug &&
      (capabilities.fullCDPSessionControlEnabled ?? false);
    this.hasRunCreation = controlPresent && (capabilities.runCreationEnabled ?? true);
    this.hasStandardCodePreset = controlPresent &&
      (capabilities.standardCodePresetEnabled ?? false);
    this.hasSessionMessages = controlPresent && (capabilities.sessionMessageEnabled ?? true);
    this.hasThreadControl = controlPresent &&
      (capabilities.threadControlEnabled ??
        ((capabilities.runCreationEnabled ?? true) &&
          (capabilities.sessionMessageEnabled ?? true)));
    this.hasSessionSteeringControl = controlPresent &&
      (capabilities.sessionSteeringControlEnabled ?? true);
    this.hasRunLifecycle = controlPresent && (capabilities.runLifecycleEnabled ?? true);
    this.hasRunExecution = controlPresent && (capabilities.runExecutionEnabled ?? true);
    this.hasThreadExecutionRead = capabilities.threadExecutionReadEnabled === true;
    this.hasPlanDelivery = controlPresent && (capabilities.planDeliveryControlEnabled ?? true);
    this.hasApprovalControl = controlPresent && (capabilities.approvalControlEnabled ?? true);
    this.hasModelControl = controlPresent && (capabilities.modelControlEnabled ?? true);
    this.hasProviderDefinitions = this.hasModelControl;
    this.hasProviderCredentials = controlPresent &&
      (capabilities.providerCredentialEnabled ?? false);
    this.hasChatGPTAuth = controlPresent && capabilities.chatGPTAuthEnabled === true;
    this.hasFileEditReview = controlPresent && (capabilities.fileEditReviewEnabled ?? true);
    this.hasFileEditProposals = controlPresent &&
      (capabilities.fileEditProposalEnabled ?? false);
    this.hasFileEditApply = controlPresent && (capabilities.fileEditApplyEnabled ?? true);
    this.hasRunWakeControl = controlPresent && (capabilities.runWakeControlEnabled ?? true);
    this.hasRunWakeExecution = controlPresent && (capabilities.runWakeExecutionEnabled ?? true);
    this.hasRunWakeWorker = controlPresent && (capabilities.runWakeWorkerEnabled ?? false);
    this.hasScheduledJobControl = controlPresent &&
      (capabilities.scheduledJobControlEnabled ?? false);
    this.hasScheduledJobWorker = controlPresent &&
      (capabilities.scheduledJobWorkerEnabled ?? false);
    this.hasSkillInstallation = controlPresent && (capabilities.skillInstallationEnabled ?? true);
    this.hasEvidenceAttachment = controlPresent &&
      (capabilities.evidenceAttachmentEnabled ?? true);
    this.hasVerificationEvidence = controlPresent &&
      (capabilities.verificationEvidenceEnabled ?? false);
    this.hasEmbeddedAnalyzerExecution = controlPresent &&
      (capabilities.embeddedAnalyzerExecutionEnabled ?? false);
    this.hasWorkspaceCheckpointControl = controlPresent &&
      (capabilities.workspaceCheckpointControlEnabled ?? false);
    this.hasGitAdvancedControl = controlPresent &&
      (capabilities.gitAdvancedControlEnabled ?? false) &&
      this.hasExecutionPermissionControl &&
      (capabilities.operatorApprovalEnabled ?? false) &&
      this.hasWorkspaceCheckpointControl;
    this.hasGitHubReviewControl = controlPresent &&
      (capabilities.githubReviewControlEnabled ?? false) &&
      this.hasExecutionPermissionControl &&
      (capabilities.operatorApprovalEnabled ?? false);
    this.hasBatchDeliveryControl = controlPresent &&
      (capabilities.batchDeliveryControlEnabled ?? false);
    this.hasBatchDeliveryHostValidation = this.hasBatchDeliveryControl &&
      this.hasExecutionPermissionControl &&
      (capabilities.operatorApprovalEnabled ?? false) &&
      (capabilities.dangerFullAccessEnabled ?? false) &&
      (capabilities.batchDeliveryHostValidationEnabled ?? false);
    this.hasExtensionControl = controlPresent &&
      (capabilities.extensionControlEnabled ?? true);
    this.hasUIEvidence = controlPresent && (capabilities.uiEvidenceControlEnabled ?? false) &&
      this.hasRunExecution && this.hasBrowserCDPPermissionControl;
    this.uiEvidenceUnavailableReason = !controlPresent ? "missing_control_credential"
      : !(capabilities.uiEvidenceControlEnabled ?? false) ? "ui_evidence_disabled"
        : !this.hasRunExecution ? "run_execution_disabled"
          : !this.hasBrowserCDPPermissionControl ? "browser_cdp_control_disabled" : null;
  }

  async health(signal?: AbortSignal): Promise<HealthView> {
    return this.get<HealthView>("/health", {}, signal);
  }

  async runtimeCapabilities(signal?: AbortSignal): Promise<RuntimeCapabilitiesView> {
    return parseRuntimeCapabilities(await this.get<unknown>("/capabilities", {}, signal));
  }

  async createProjectlessWorkspace(idempotencyKey: string, signal?: AbortSignal): Promise<ProjectlessWorkspaceView> {
    if (!this.hasProjectlessWorkspaces) throw new Error("Projectless conversations are unavailable");
    const value = await this.sendControl<unknown>("/workspaces/projectless", {
      version: "projectless_workspace.v1",
    }, idempotencyKey, signal);
    if (!hasExactKeys(value, ["version", "workspace", "replayed"]) ||
      value.version !== "projectless_workspace.v1" || typeof value.replayed !== "boolean" ||
      !hasExactKeys(value.workspace, ["id", "name", "created_at"]) ||
      !boundedIdentity(value.workspace.id) || !boundedText(value.workspace.name, 128) ||
      !validDate(value.workspace.created_at)) {
      throw new APIRequestError("Projectless workspace response is invalid", "INVALID_RESPONSE", 502);
    }
    return value as unknown as ProjectlessWorkspaceView;
  }

  async getProjectlessDirectory(signal?: AbortSignal): Promise<ProjectlessDirectory> {
    if (!this.hasProjectlessWorkspaces) throw new Error("Projectless conversations are unavailable");
    return parseProjectlessDirectory(await this.sendControlRequest<unknown>(
      "/settings/projectless-directory", undefined, signal, "", "GET"));
  }

  async setProjectlessDirectory(directoryPath: string, signal?: AbortSignal): Promise<ProjectlessDirectory> {
    if (!this.hasProjectlessWorkspaces || !this.hasWorkspaceImport || !directoryPath.trim()) {
      throw new Error("Choose a directory with workspace import enabled");
    }
    return parseProjectlessDirectory(await this.sendControlRequest<unknown>("/settings/projectless-directory", {
      version: "projectless_directory.v1", directory_path: directoryPath, confirmed: true,
    }, signal, "", "PUT"));
  }

  async getWorkspaceProject(id: string, signal?: AbortSignal): Promise<WorkspaceProjectView> {
    if (!boundedIdentity(id)) throw new Error("A project identity is required");
    return parseWorkspaceProject(await this.sendControlRequest<unknown>(`/workspaces/${encodeURIComponent(id)}/project`, undefined, signal, "", "GET"), id);
  }

  async renameWorkspace(id: string, name: string, signal?: AbortSignal): Promise<WorkspaceProjectView> {
    if (!boundedIdentity(id)) throw new Error("A project identity is required");
    return parseWorkspaceProject(await this.sendControlRequest<unknown>(`/workspaces/${encodeURIComponent(id)}/name`, { name: normalizeWorkspaceName(name) }, signal, "", "PUT"), id);
  }

  async renameThread(id: string, title: string, signal?: AbortSignal): Promise<ThreadView> {
    if (!boundedIdentity(id) || !boundedText(title.trim(), 4096) || /[\p{Cc}]/u.test(title)) {
      throw new Error("A conversation identity and a title of 1–4096 characters without control characters are required");
    }
    return parseThreadView(await this.sendControlRequest<unknown>(`/threads/${encodeURIComponent(id)}/name`, { title: title.trim() }, signal, "", "PUT"), id);
  }

  async pinThread(id: string, pinned: boolean, signal?: AbortSignal): Promise<ThreadView> {
    if (!boundedIdentity(id) || typeof pinned !== "boolean") throw new Error("A conversation identity and pinned state are required");
    return parseThreadView(await this.sendControlRequest<unknown>(`/threads/${encodeURIComponent(id)}/pin`, { pinned }, signal, "", "PUT"), id);
  }

  async importWorkspace(directoryPath: string, signal?: AbortSignal, name?: string): Promise<WorkspaceImportView> {
    if (!this.hasWorkspaceImport) {
      throw new APIRequestError("Workspace import is unavailable for this connection",
        "PERMISSION_DENIED", 403);
    }
    if (typeof directoryPath !== "string" || !directoryPath.trim() ||
      /[\u0000-\u001f\u007f]/u.test(directoryPath) ||
      new TextEncoder().encode(directoryPath).length > 4_096) {
      throw new APIRequestError("A valid project directory is required", "INVALID_ARGUMENT", 400);
    }
    if (name !== undefined) name = normalizeWorkspaceName(name);
    try {
      const value = await this.sendControlRequest<unknown>("/workspaces/import", {
        version: "workspace_import.v1", directory_path: directoryPath, confirmed: true, ...(name !== undefined ? { name } : {}),
      }, signal);
      if (!hasExactKeys(value, ["protocol_version", "workspace",
        "directory_content_modified", "agent_authority_granted"]) ||
        value.protocol_version !== "workspace_import.v1" ||
        value.directory_content_modified !== false || value.agent_authority_granted !== false ||
        !hasExactKeys(value.workspace, ["id", "name", "created_at"]) ||
        typeof value.workspace.id !== "string" ||
        !/^[A-Za-z0-9_.-]{1,256}$/u.test(value.workspace.id) ||
        !boundedText(value.workspace.name, 128) ||
        /[\u0000-\u001f\u007f]/u.test(value.workspace.name) ||
        !validDate(value.workspace.created_at)) {
        throw new APIRequestError("Workspace import response is invalid", "INVALID_RESPONSE", 502);
      }
      return value as unknown as WorkspaceImportView;
    } catch (error) {
      // Transport and server messages may contain the submitted host path.
      // Keep bounded machine status for recovery while discarding raw details.
      if (signal?.aborted || (error instanceof DOMException && error.name === "AbortError")) {
        throw new DOMException("Workspace import was cancelled", "AbortError");
      }
      const code = error instanceof APIRequestError && /^[A-Z_]{1,64}$/u.test(error.code)
        ? error.code : "UNAVAILABLE";
      throw new APIRequestError("Unable to import the project. Check the directory and try again.",
        code, error instanceof APIRequestError ? error.status : 0);
    }
  }

  async runCapabilityReadiness(runID: string,
    signal?: AbortSignal): Promise<RunCapabilityReadinessView> {
    if (!boundedIdentity(runID) || runID.trim() !== runID) {
      throw new Error("A normalized Run identity is required");
    }
    return parseRunCapabilityReadiness(await this.get<unknown>(
      `/runs/${encodeURIComponent(runID)}/capability-readiness`, {}, signal), runID);
  }

  async getThreadCommandServicePolicy(threadID: string, signal?: AbortSignal) {
    if (!applicationServiceIdentity(threadID)) throw new Error("An exact Thread is required for development service settings");
    return parseThreadCommandServicePolicy(await this.get<unknown>(
      `/threads/${encodeURIComponent(threadID)}/command-service-policy`, {}, signal), threadID);
  }

  async setThreadCommandServicePolicy(threadID: string, body: ThreadCommandServicePolicyRequest, signal?: AbortSignal) {
    if (!this.hasExecutionPermissionControl || !applicationServiceIdentity(threadID) || !validCommandServicePolicyRequest(body)) {
      throw new Error("Development service settings require execution permission control access, an exact Thread and its current revision");
    }
    const result = parseThreadCommandServicePolicy(await this.sendControlRequest<unknown>(
      `/threads/${encodeURIComponent(threadID)}/command-service-policy`, body, signal, "", "PUT"), threadID);
    if (result.policy !== body.policy || result.revision < body.expected_revision) {
      throw new APIRequestError("开发服务设置返回的策略或版本与本次保存不符，请重新读取。", "INVALID_RESPONSE", 502);
    }
    return result;
  }

  async listThreadApplicationServices(threadID: string, signal?: AbortSignal, limit = 20) {
    if (!applicationServiceIdentity(threadID) || !Number.isInteger(limit) || limit < 1 || limit > 50) {
      throw new Error("A valid Thread and bounded application service limit are required");
    }
    return parseThreadApplicationServices(await this.get<unknown>(
      `/threads/${encodeURIComponent(threadID)}/application-services`, { limit }, signal), threadID);
  }

  async getThreadApplicationService(threadID: string, jobID: string, signal?: AbortSignal) {
    if (!applicationServiceIdentity(threadID) || !applicationServiceIdentity(jobID)) {
      throw new Error("An exact Thread and application Job are required");
    }
    return parseThreadApplicationService(await this.get<unknown>(
      `/threads/${encodeURIComponent(threadID)}/application-services/${encodeURIComponent(jobID)}`, {}, signal), threadID, jobID);
  }

  async stopThreadApplicationService(threadID: string, runID: string, jobID: string, signal?: AbortSignal) {
    if (!this.hasRunExecution || !applicationServiceIdentity(threadID) || !applicationServiceIdentity(runID) ||
      !applicationServiceIdentity(jobID)) {
      throw new Error("Application service cleanup requires control access and an exact Thread, Run and Job");
    }
    return parseThreadApplicationServiceStop(await this.sendControl<unknown>(
      `/threads/${encodeURIComponent(threadID)}/application-services/${encodeURIComponent(jobID)}/stop`,
      { version: "thread_application_services.v1", expected_run_id: runID },
      `application-stop-${jobID}`, signal), threadID, runID, jobID);
  }

  async getFullCDPSession(runID: string,
    signal?: AbortSignal): Promise<FullCDPSessionControlView> {
    if (!this.hasFullCDPSessionControl || !boundedIdentity(runID) || runID.trim() !== runID) {
      throw new Error("Full CDP production session capability and a normalized Run are required");
    }
    return parseFullCDPSessionControl(await this.get<unknown>(
      `/runs/${encodeURIComponent(runID)}/full-cdp-session`, {}, signal), runID);
  }

  async openFullCDPSession(runID: string, body: FullCDPSessionOpenRequestView,
    idempotencyKey: string, signal?: AbortSignal): Promise<FullCDPSessionControlView> {
    if (!this.hasFullCDPSessionControl || !boundedIdentity(runID) || runID.trim() !== runID ||
      body.version !== "full_cdp_session.v1" || body.confirm_full_cdp !== true ||
      !safePositiveInteger(body.expected_execution_permission_revision) ||
      !safePositiveInteger(body.expected_browser_cdp_permission_revision) ||
      !["chrome", "edge"].includes(body.browser.product) ||
      !["stable", "beta", "dev", "canary"].includes(body.browser.channel) ||
      typeof body.target !== "string" || body.target.trim() !== body.target ||
      body.target.length === 0 || body.target.length > 2_048) {
      throw new Error("A confirmed, revision-bound Full CDP session request is required");
    }
    return parseFullCDPSessionControl(await this.sendControl<unknown>(
      `/runs/${encodeURIComponent(runID)}/full-cdp-session`, body, idempotencyKey, signal), runID);
  }

  async closeFullCDPSession(runID: string, body: FullCDPSessionCloseRequestView,
    idempotencyKey: string, signal?: AbortSignal): Promise<FullCDPSessionControlView> {
    if (!this.hasFullCDPSessionControl || !boundedIdentity(runID) || runID.trim() !== runID ||
      body.version !== "full_cdp_session_close.v1" ||
      !boundedIdentity(body.expected_session_id)) {
      throw new Error("An exact Full CDP session cleanup request is required");
    }
    return parseFullCDPSessionControl(await this.sendControl<unknown>(
      `/runs/${encodeURIComponent(runID)}/full-cdp-session/close`, body,
      idempotencyKey, signal), runID);
  }

  async configureStandardCode(runID: string,
    action: "configure" | "pause_and_configure",
    body: StandardCodePresetControlRequestView, idempotencyKey: string,
    signal?: AbortSignal): Promise<StandardCodePresetControlView> {
    if (!this.hasStandardCodePreset) {
      throw new Error("Standard Code preset capability is required for this operation");
    }
    if (!boundedIdentity(runID) || runID.trim() !== runID ||
      body.version !== "standard_code_preset.v1" ||
      !["auto", "local", "docker", "sbx"].includes(body.backend_intent) ||
      body.workspace_id !== undefined || body.goal !== undefined ||
      body.confirm_workspace_trust !== (body.expected_trust_digest !== undefined) ||
      (body.expected_trust_digest !== undefined && !isSHA256(body.expected_trust_digest))) {
      throw new Error("A valid existing-Run Standard Code preset request is required");
    }
    const suffix = action === "pause_and_configure"
      ? "/standard-code/pause-and-configure" : "/standard-code/preset";
    const result = await this.sendControl<unknown>(
      `/runs/${encodeURIComponent(runID)}${suffix}`, body, idempotencyKey, signal);
    const parsed = parseStandardCodePreset(result, action);
    if (parsed.backend_intent !== body.backend_intent) {
      throw new APIRequestError("Standard Code response changed the requested backend", "INVALID_RESPONSE", 502);
    }
    return parsed;
  }

  async createStandardCode(body: StandardCodePresetControlRequestView,
    idempotencyKey: string, signal?: AbortSignal): Promise<StandardCodePresetControlView> {
    if (!this.hasStandardCodePreset) {
      throw new Error("Standard Code preset capability is required for this operation");
    }
    const workspaceID = boundedIdentity(body.workspace_id);
    const goal = body.goal;
    if (body.version !== "standard_code_preset.v1" ||
      !["auto", "local", "docker", "sbx"].includes(body.backend_intent) ||
      workspaceID !== body.workspace_id || typeof goal !== "string" ||
      goal.trim() !== goal || goal.length === 0 || goal.includes("\0") ||
      new TextEncoder().encode(goal).byteLength > 4096 ||
      body.confirm_workspace_trust !== (body.expected_trust_digest !== undefined) ||
      (body.expected_trust_digest !== undefined && !isSHA256(body.expected_trust_digest))) {
      throw new Error("A normalized Workspace, goal, and trust intent are required for Standard Code");
    }
    const result = parseStandardCodePreset(await this.sendControl<unknown>(
      "/standard-code/preset", body, idempotencyKey, signal), "configure");
    if (result.workspace_id !== workspaceID ||
      result.backend_intent !== body.backend_intent) {
      throw new APIRequestError("Standard Code preset response changed the requested target",
        "INVALID_RESPONSE", 502);
    }
    return result;
  }

  async codeIntelInventory(workspaceID = "", signal?: AbortSignal):
    Promise<CodeIntelInventoryView> {
    if (workspaceID !== "" && (!strictCodeIntelIdentity(workspaceID) ||
      workspaceID.trim() !== workspaceID)) {
      throw new Error("A normalized Workspace identity is required");
    }
    return parseCodeIntelInventory(await this.get<unknown>(
      "/code-intel", { workspace_id: workspaceID || undefined }, signal,
    ));
  }

  async gitAdvancedProjection(runID: string,
    signal?: AbortSignal): Promise<GitAdvancedProjectionView> {
    if (!boundedIdentity(runID) || runID.trim() !== runID) {
      throw new Error("A normalized Run identity is required");
    }
    const value = await this.get<unknown>(
      `/runs/${encodeURIComponent(runID)}/git-advanced`, { limit: 100 }, signal,
    );
    if (!isGitAdvancedProjection(value, runID)) {
      throw new APIRequestError("Git advanced projection is invalid", "INVALID_RESPONSE", 502);
    }
    return value;
  }

  async discoverGitAdvancedHunks(runID: string, spec: GitAdvancedSpecView,
    signal?: AbortSignal): Promise<GitAdvancedReviewResultView> {
    if (!this.hasGitAdvancedControl || !boundedIdentity(runID) || runID.trim() !== runID ||
      !validGitAdvancedSpec(spec) || !["hunk_stage", "hunk_unstage", "hunk_revert"]
        .includes(spec.operation) || (spec.hunk_ids?.length ?? 0) !== 0) {
      throw new Error("Git advanced hunk discovery requires an enabled capability and closed hunk spec");
    }
    const value = await this.sendReadRequest<unknown>(
      `/runs/${encodeURIComponent(runID)}/git-advanced/discover-hunks`, { spec }, signal,
    );
    if (!isGitAdvancedReviewResult(value, runID, true)) {
      throw new APIRequestError("Git advanced hunk discovery is invalid", "INVALID_RESPONSE", 502);
    }
    return value;
  }

  async reviewGitAdvanced(runID: string, body: {
    operation_key: string;
    scope: GitAdvancedScopeView;
    spec: GitAdvancedSpecView;
  }, signal?: AbortSignal): Promise<GitAdvancedReviewResultView> {
    if (!this.hasGitAdvancedControl || !boundedIdentity(runID) || runID.trim() !== runID ||
      !boundedText(body.operation_key, 256) || body.operation_key.length < 16 ||
      !isSHA256(body.scope.capability_generation) || !safePositiveInteger(body.scope.lease_generation) ||
      !validGitAdvancedSpec(body.spec)) {
      throw new Error("Git advanced review requires exact capability, lease, and operation inputs");
    }
    const value = await this.sendControlRequest<unknown>(
      `/runs/${encodeURIComponent(runID)}/git-advanced/review`, body, signal,
    );
    if (!isGitAdvancedReviewResult(value, runID, false)) {
      throw new APIRequestError("Git advanced review is invalid", "INVALID_RESPONSE", 502);
    }
    return value;
  }

  async executeGitAdvanced(runID: string, body: {
    operation_id: string;
    approval_id: string;
    scope: GitAdvancedScopeView;
  }, signal?: AbortSignal): Promise<GitAdvancedExecuteResultView> {
    if (!this.hasGitAdvancedControl || !boundedIdentity(runID) || runID.trim() !== runID ||
      !boundedIdentity(body.operation_id) || !boundedIdentity(body.approval_id) ||
      !isSHA256(body.scope.capability_generation) || !safePositiveInteger(body.scope.lease_generation)) {
      throw new Error("Git advanced execution requires exact approved operation authority");
    }
    const value = await this.sendControlRequest<unknown>(
      `/runs/${encodeURIComponent(runID)}/git-advanced/execute`, body, signal,
    );
    if (!isGitAdvancedExecuteResult(value, body.operation_id)) {
      throw new APIRequestError("Git advanced execution result is invalid", "INVALID_RESPONSE", 502);
    }
    return value;
  }

  async githubReviewConnections(enabledOnly = false,
    signal?: AbortSignal): Promise<GitHubReviewCredentialView[]> {
    return parseGitHubReviewConnections(await this.get<unknown>(
      "/github-review/connections", { enabled_only: enabledOnly ? "true" : undefined }, signal,
    ));
  }

  async githubReviewCredential(connectionID: string,
    signal?: AbortSignal): Promise<GitHubReviewCredentialView> {
    if (!boundedIdentity(connectionID)) throw new Error("A normalized GitHub connection is required");
    return parseGitHubReviewCredential(await this.get<unknown>(
      `/github-review/connections/${encodeURIComponent(connectionID)}`, {}, signal,
    ));
  }

  async configureGitHubReview(body: {
    connection_id?: string;
    repository: { host: "github.com"; owner: string; name: string; full_name: string; private: boolean };
    credential: { name: string; kind: "github_app_device" | "oauth_user" | "fine_grained_pat" };
    client_id?: string;
    allowed_log_hosts: string[];
    write_enabled: boolean;
    enabled: boolean;
    expected_generation: number;
  }, signal?: AbortSignal): Promise<GitHubReviewConfigureResultView> {
    if (!this.hasGitHubReviewControl || !boundedIdentity(body.repository.owner) ||
      !boundedIdentity(body.repository.name) || body.repository.full_name !==
      `${body.repository.owner}/${body.repository.name}` || !boundedIdentity(body.credential.name) ||
      typeof body.write_enabled !== "boolean" || !Number.isSafeInteger(body.expected_generation) ||
      body.expected_generation < 0) {
      throw new Error("A bounded GitHub repository configuration is required");
    }
    return parseGitHubReviewConfigure(await this.sendControlRequest<unknown>(
      "/github-review/connections", body, signal,
    ));
  }

  async beginGitHubReviewDeviceFlow(connectionID: string,
    signal?: AbortSignal): Promise<GitHubReviewDeviceAuthorizationView> {
    if (!this.hasGitHubReviewControl || !boundedIdentity(connectionID)) {
      throw new Error("GitHub review control and a connection are required");
    }
    return parseGitHubDeviceAuthorization(await this.sendControlRequest<unknown>(
      `/github-review/connections/${encodeURIComponent(connectionID)}/device`, {}, signal,
    ));
  }

  async pollGitHubReviewDeviceFlow(connectionID: string, sessionID: string,
    signal?: AbortSignal): Promise<GitHubReviewDevicePollResultView> {
    if (!this.hasGitHubReviewControl || !boundedIdentity(connectionID) ||
      !boundedIdentity(sessionID)) throw new Error("A GitHub Device Flow session is required");
    return parseGitHubDevicePoll(await this.sendControlRequest<unknown>(
      `/github-review/connections/${encodeURIComponent(connectionID)}/device-poll`,
      { session_id: sessionID }, signal,
    ));
  }

  async disconnectGitHubReview(connectionID: string,
    signal?: AbortSignal): Promise<GitHubReviewCredentialView> {
    if (!this.hasGitHubReviewControl || !boundedIdentity(connectionID)) {
      throw new Error("GitHub review control and a connection are required");
    }
    return parseGitHubReviewCredential(await this.sendControlRequest<unknown>(
      `/github-review/connections/${encodeURIComponent(connectionID)}/disconnect`, {}, signal,
    ));
  }

  async qualifyGitHubReview(connectionID: string, pullRequest: number,
    signal?: AbortSignal): Promise<GitHubReviewQualificationResultView> {
    if (!this.hasGitHubReviewControl || !boundedIdentity(connectionID) ||
      !Number.isSafeInteger(pullRequest) || pullRequest < 1) throw new Error("A valid PR is required");
    return parseGitHubQualification(await this.sendControlRequest<unknown>(
      `/github-review/connections/${encodeURIComponent(connectionID)}/qualify`,
      { pull_request: pullRequest }, signal,
    ));
  }

  async fetchGitHubReview(connectionID: string, pullRequest: number,
    signal?: AbortSignal): Promise<GitHubReviewFetchResultView> {
    if (!this.hasGitHubReviewControl || !boundedIdentity(connectionID) ||
      !Number.isSafeInteger(pullRequest) || pullRequest < 1) throw new Error("A valid PR is required");
    return parseGitHubFetch(await this.sendControlRequest<unknown>(
      `/github-review/connections/${encodeURIComponent(connectionID)}/fetch`,
      { pull_request: pullRequest }, signal,
    ));
  }

  async githubReviewProjection(runID: string, connectionID: string, pullRequest = 0,
    signal?: AbortSignal): Promise<GitHubReviewProjectionView> {
    if (!boundedIdentity(runID) || !boundedIdentity(connectionID) ||
      !Number.isSafeInteger(pullRequest) || pullRequest < 0) throw new Error("Invalid GitHub projection scope");
    return parseGitHubProjection(await this.get<unknown>(
      `/runs/${encodeURIComponent(runID)}/github-review`, {
        connection_id: connectionID, pull_request: pullRequest || undefined, limit: 100,
      }, signal,
    ), runID);
  }

  async buildGitHubReviewEvidence(runID: string, snapshotID: string,
    signal?: AbortSignal): Promise<GitHubReviewEvidenceResultView> {
    if (!this.hasGitHubReviewControl || !boundedIdentity(runID) || !boundedIdentity(snapshotID)) {
      throw new Error("A Run and immutable GitHub snapshot are required");
    }
    return parseGitHubEvidence(await this.sendControlRequest<unknown>(
      `/runs/${encodeURIComponent(runID)}/github-review/evidence`,
      { snapshot_id: snapshotID }, signal,
    ));
  }

  async reviewGitHubWrite(runID: string, body: { connection_id: string; snapshot_id: string;
    operation_key: string; spec: GitHubReviewWriteSpecView },
  signal?: AbortSignal): Promise<GitHubReviewWriteReviewResultView> {
    if (!this.hasGitHubReviewControl || !boundedIdentity(runID) ||
      !boundedIdentity(body.connection_id) || !boundedIdentity(body.snapshot_id) ||
      !boundedIdentity(body.operation_key)) throw new Error("An exact GitHub write preview is required");
    return parseGitHubWriteReview(await this.sendControlRequest<unknown>(
      `/runs/${encodeURIComponent(runID)}/github-review/review`, body, signal,
    ));
  }

  async executeGitHubWrite(runID: string, operationID: string, approvalID: string,
    signal?: AbortSignal): Promise<GitHubReviewWriteExecuteResultView> {
    if (!this.hasGitHubReviewControl || !boundedIdentity(runID) ||
      !boundedIdentity(operationID) || !boundedIdentity(approvalID)) {
      throw new Error("An approved GitHub write operation is required");
    }
    return parseGitHubWriteExecute(await this.sendControlRequest<unknown>(
      `/runs/${encodeURIComponent(runID)}/github-review/execute`,
      { operation_id: operationID, approval_id: approvalID }, signal,
    ));
  }

  async extensionInventory(runID = "", signal?: AbortSignal, workspaceID = ""): Promise<ExtensionInventoryView> {
    if (runID !== "" && (!boundedIdentity(runID) || runID.trim() !== runID)) {
      throw new Error("A normalized Run identity is required");
    }
    if (workspaceID !== "" && (!boundedIdentity(workspaceID) || workspaceID.trim() !== workspaceID)) {
      throw new Error("A normalized Workspace identity is required");
    }
    const inventory = parseExtensionInventory(await this.get<unknown>(
      "/extensions", { run_id: runID || undefined, workspace_id: workspaceID || undefined }, signal,
    ));
    if ((runID && inventory.run_id !== runID) || (workspaceID && inventory.workspace_id !== workspaceID) ||
      (runID && inventory.mcp_calls.some((call) => call.run_id !== runID)) ||
      (!runID && inventory.mcp_calls.length > 0) ||
      (workspaceID && inventory.mcp_servers.some((server) => server.workspace_id !== workspaceID))) {
      throw new APIRequestError("Extension inventory scope does not match the request", "INVALID_RESPONSE", 502);
    }
    return inventory;
  }

  async registerMCPServer(body: ExtensionMCPRegistrationRequestView,
    signal?: AbortSignal): Promise<ExtensionMCPRegistrationView> {
    const descriptor = body.descriptor;
    if (!this.hasExtensionControl || body.version !== "extension-control.v1" ||
      !isRecord(descriptor) || !["mcp-client.v1", "mcp-client.v3"].includes(descriptor.protocol_version) ||
      !validMCPConnectionConfig(descriptor) ||
      !boundedIdentity(descriptor.id) || !boundedIdentity(descriptor.workspace_id) ||
      !boundedText(descriptor.name, 256) || !boundedText(descriptor.target, 4_096) ||
      !["stdio", "streamable_http", "sse", "websocket"].includes(String(descriptor.transport)) ||
      !["workspace", "run"].includes(String(descriptor.scope)) ||
      (descriptor.scope === "run" && !boundedIdentity(descriptor.run_id)) ||
      (descriptor.scope === "workspace" && descriptor.run_id !== undefined && descriptor.run_id !== "") ||
      !boundedStringArray(descriptor.declared_capabilities, 3, 32) ||
      !safePositiveInteger(descriptor.call_timeout_ms) || descriptor.call_timeout_ms < 100 ||
      descriptor.call_timeout_ms > 300_000 || !safePositiveInteger(descriptor.max_result_bytes) ||
      descriptor.max_result_bytes > 131_072) {
      throw new Error("A bounded MCP descriptor and extension control are required");
    }
    return parseMCPRegistration(await this.sendControlRequest<unknown>("/extensions/mcp", body, signal), body);
  }

  async mcpCredentialStatus(binding: MCPCredentialBindingView, signal?: AbortSignal): Promise<MCPCredentialStatusView> {
    if (!validMCPCredentialBinding(binding)) throw new Error("An exact HTTPS MCP credential binding is required");
    const value = await this.get<unknown>(`/extensions/mcp/${encodeURIComponent(binding.server_id)}/credential`, {
      workspace_id: binding.workspace_id, run_id: binding.run_id,
      expected_descriptor_fingerprint: binding.expected_descriptor_fingerprint, target: binding.target, credential_ref: binding.credential_ref,
    }, signal);
    try { return parseMCPCredentialStatus(value, binding); }
    catch { throw new APIRequestError("MCP credential status violated its metadata or binding contract", "INVALID_RESPONSE", 502); }
  }

  async changeMCPCredential(body: MCPCredentialRequestView, signal?: AbortSignal): Promise<MCPCredentialStatusView> {
    if (!this.hasExtensionControl || body.version !== "mcp-credential.v1" || !validMCPCredentialBinding(body.binding) ||
      body.confirm !== true || !isSHA256(body.expected_reference_fingerprint) ||
      (body.action !== "set" && body.action !== "delete") ||
      (body.action === "set" && !validMCPBearerSecret(body.secret ?? "")) || (body.action === "delete" && !!body.secret)) {
      throw new Error("An exact MCP binding, explicit credential action and confirmation are required");
    }
    const value = await this.sendControlRequest<unknown>(`/extensions/mcp/${encodeURIComponent(body.binding.server_id)}/credential`, body, signal);
    try {
      const status = parseMCPCredentialStatus(value, body.binding);
      if (!status.store_available || status.endpoint_conflict || status.configured !== (body.action === "set") ||
        status.reference_fingerprint !== body.expected_reference_fingerprint) throw new Error("Unverified mutation outcome");
      return status;
    }
    catch { throw new APIRequestError("MCP credential change returned invalid presence metadata; refresh before retrying", "INVALID_RESPONSE", 502); }
  }

  async changeMCPOAuth(body: MCPCredentialRequestView, signal?: AbortSignal): Promise<MCPCredentialStatusView> {
    if (!this.hasExtensionControl || body.version !== "mcp-credential.v1" || !validMCPCredentialBinding(body.binding) ||
      !body.confirm || !isSHA256(body.expected_reference_fingerprint) || body.secret ||
      !["oauth-discover", "oauth-start", "oauth-cancel"].includes(body.action)) throw new Error("An exact MCP OAuth binding and confirmation are required");
    if (body.oauth_client_id_mode !== undefined &&
      (body.action !== "oauth-start" || !["auto", "dynamic", "pre_registered", "metadata_url"].includes(body.oauth_client_id_mode))) throw new Error("Invalid OAuth client ID mode");
    if (body.oauth_callback_port !== undefined && (body.action !== "oauth-start" || !Number.isInteger(body.oauth_callback_port) || body.oauth_callback_port < 0 || body.oauth_callback_port > 65535)) throw new Error("Invalid OAuth callback port");
    if (body.oauth_client_secret !== undefined && (body.action !== "oauth-start" || !body.oauth_client_id || !["auto", "pre_registered"].includes(body.oauth_client_id_mode ?? "pre_registered") || !body.oauth_client_secret || body.oauth_client_secret.length > 1024 || /[\r\n\0]/u.test(body.oauth_client_secret))) throw new Error("Invalid OAuth client secret configuration");
    if (body.action === "oauth-start" && body.oauth_client_id_mode === "metadata_url") {
      const url = new URL(body.oauth_client_id ?? "");
      if (url.protocol !== "https:" || !url.hostname || url.username || url.password || url.search || url.hash || url.pathname === "/" ||
        (body.oauth_client_id?.length ?? 0) > 256) throw new Error("A hosted HTTPS client metadata document URL is required");
    }
    const value=await this.sendControlRequest<unknown>(`/extensions/mcp/${encodeURIComponent(body.binding.server_id)}/credential`,body,signal);
    return parseMCPCredentialStatus(value,body.binding);
  }

  async importPluginPackage(body: ExtensionPluginImportRequestView,
    signal?: AbortSignal): Promise<ExtensionPluginImportView> {
    if (!this.hasExtensionControl || body.version !== "extension-control.v1") {
      throw new Error("Plugin import control is required");
    }
    if (body.source) {
      const source = body.source;
      const url = new URL(source.url);
      if (body.archive_base64 || url.protocol !== "https:" || !url.hostname || url.username || url.password ||
        url.search || url.hash || source.url.length > 4096 ||
        (source.kind !== "https" && source.kind !== "git") ||
        (source.kind === "https" && (!isSHA256(body.archive_sha256) || source.commit || source.archive_path)) ||
        (source.kind === "git" && (!/^(?:[a-f0-9]{40}|[a-f0-9]{64})$/u.test(source.commit ?? "") ||
          (body.archive_sha256 !== undefined && !isSHA256(body.archive_sha256)) ||
          (source.archive_path !== undefined && (!/^(?!\/)(?!.*(?:^|\/)\.{1,2}(?:\/|$))[^\\:\0]+\.zip$/u.test(source.archive_path) || source.archive_path.length > 512))))) {
        throw new Error("A credential-free HTTPS URL with an exact SHA-256 or Git commit is required");
      }
    } else if (!isSHA256(body.archive_sha256) || !validCanonicalBase64(body.archive_base64, 4 * 1_024 * 1_024)) {
      throw new Error("A bounded Plugin ZIP and exact SHA-256 are required");
    }
    return parsePluginImport(await this.sendControlRequest<unknown>("/extensions/plugins/import", body, signal), body);
  }

  async stageCodeIntelConfiguration(body: CodeIntelConfigurationRequestView,
    signal?: AbortSignal): Promise<CodeIntelConfigurationView> {
    if (!this.hasExtensionControl || body.version !== "code-intel-configuration.v1" ||
      !strictCodeIntelIdentity(body.server_id) || !strictCodeIntelIdentity(body.workspace_id) ||
      !isSHA256(body.executable_sha256) || !boundedText(body.executable, 4096) ||
      !boundedStringArray(body.arguments, 64, 4096) || !safePositiveInteger(body.request_timeout_ms)) {
      throw new Error("A pinned language-server descriptor and extension control are required");
    }
    const configuration = parseCodeIntelConfiguration(await this.sendControlRequest<unknown>("/code-intel/configurations", body, signal));
    if (configuration.server_id !== body.server_id || configuration.workspace_id !== body.workspace_id ||
      configuration.executable_sha256 !== body.executable_sha256) {
      throw new APIRequestError("Code intelligence configuration binding is invalid", "INVALID_RESPONSE", 502);
    }
    return configuration;
  }

  async reviewCodeIntelConfiguration(serverID: string, body: CodeIntelConfigurationReviewRequestView,
    signal?: AbortSignal): Promise<CodeIntelConfigurationView> {
    if (!this.hasExtensionControl || !strictCodeIntelIdentity(serverID) ||
      body.version !== "code-intel-configuration.v1" || !strictCodeIntelIdentity(body.workspace_id) ||
      !isSHA256(body.expected_descriptor_fingerprint)) throw new Error("An exact language-server review is required");
    const configuration = parseCodeIntelConfiguration(await this.sendControlRequest<unknown>(
      `/code-intel/configurations/${encodeURIComponent(serverID)}/review`, body, signal));
    if (configuration.server_id !== serverID || configuration.workspace_id !== body.workspace_id ||
      configuration.review_state !== "reviewed") {
      throw new APIRequestError("Code intelligence review binding is invalid", "INVALID_RESPONSE", 502);
    }
    return configuration;
  }

  async testCodeIntelConfiguration(serverID: string, body: CodeIntelConfigurationTestRequestView,
    signal?: AbortSignal): Promise<CodeIntelConfigurationTestView> {
    if (!this.hasExtensionControl || !strictCodeIntelIdentity(serverID) ||
      body.version !== "code-intel-configuration.v1" || !strictCodeIntelIdentity(body.workspace_id) ||
      !isSHA256(body.expected_descriptor_fingerprint) || !["code_document_symbols", "code_workspace_symbols"].includes(body.tool) ||
      (body.tool === "code_document_symbols" && (!validWorkspaceRelativePath(body.path) || body.path === ".")) ||
      (body.query !== undefined && body.query !== "" && !boundedText(body.query, 4096))) throw new Error("An exact readonly language-server test is required");
    return parseCodeIntelConfigurationTest(await this.sendControlRequest<unknown>(
      `/code-intel/configurations/${encodeURIComponent(serverID)}/test`, body, signal), serverID, body);
  }

  async reviewMCPServer(serverID: string, body: ExtensionMCPReviewRequestView,
    signal?: AbortSignal): Promise<ExtensionMCPServerView> {
    if (!this.hasExtensionControl || !boundedIdentity(serverID) ||
      body.version !== "extension-control.v1" || !isSHA256(body.expected_descriptor_fingerprint) ||
      (body.expected_capability_fingerprint !== undefined &&
        !isSHA256(body.expected_capability_fingerprint))) {
      throw new Error("An exact MCP review request and extension control are required");
    }
    return parseExtensionMCPServer(await this.sendControlRequest<unknown>(
      `/extensions/mcp/${encodeURIComponent(serverID)}/review`, body, signal,
    ));
  }

  async refreshMCPServer(serverID: string,
    signal?: AbortSignal): Promise<ExtensionMCPServerView> {
    if (!this.hasExtensionControl || !boundedIdentity(serverID)) {
      throw new Error("A normalized MCP server and extension control are required");
    }
    return parseExtensionMCPServer(await this.sendControlRequest<unknown>(
      `/extensions/mcp/${encodeURIComponent(serverID)}/refresh`,
      { version: "extension-control.v1" }, signal,
    ));
  }

  async reviewPluginInstallation(installationID: string,
    body: ExtensionPluginReviewRequestView,
    signal?: AbortSignal): Promise<ExtensionPluginInstallationView> {
    if (!this.hasExtensionControl || !boundedIdentity(installationID) ||
      body.version !== "extension-control.v1" ||
      !isSHA256(body.expected_package_fingerprint) ||
      !safePositiveInteger(body.expected_generation)) {
      throw new Error("An exact Plugin review request and extension control are required");
    }
    return parseExtensionPlugin(await this.sendControlRequest<unknown>(
      `/extensions/plugins/${encodeURIComponent(installationID)}/review`, body, signal,
    ));
  }

  async pluginHistory(installationID: string, signal?: AbortSignal): Promise<PluginHistoryView> {
    if (!boundedIdentity(installationID)) throw new Error("A Plugin installation identity is required");
    return parsePluginHistory(await this.get<unknown>(`/extensions/plugins/${encodeURIComponent(installationID)}/history`, {}, signal), installationID);
  }

  async rollbackPluginInstallation(installationID: string, body: PluginRollbackRequestView, signal?: AbortSignal): Promise<PluginRollbackView> {
    if (!this.hasExtensionControl || !boundedIdentity(installationID) || !boundedIdentity(body.target_installation_id) || body.target_installation_id === installationID ||
      body.version !== "plugin-lifecycle.v1" || !isSHA256(body.expected_current_fingerprint) || !isSHA256(body.expected_target_fingerprint) ||
      !safePositiveInteger(body.expected_current_generation) || !safePositiveInteger(body.expected_target_generation) || typeof body.confirm_untrusted !== "boolean" ||
      !boundedStringArray(body.capabilities, 4, 32) || body.capabilities.length === 0 || new Set(body.capabilities).size !== body.capabilities.length ||
      body.capabilities.some((capability) => !pluginCapabilities.includes(capability))) throw new Error("Exact Plugin version bindings and selected capabilities are required");
    const value = await this.sendControlRequest<unknown>(`/extensions/plugins/${encodeURIComponent(installationID)}/rollback`, body, signal);
    if (!hasExactKeys(value, ["protocol_version", "current", "target"]) || value.protocol_version !== "plugin-lifecycle.v1") throw new APIRequestError("Plugin rollback projection is invalid", "INVALID_RESPONSE", 502);
    const current = parseExtensionPlugin(value.current), target = parseExtensionPlugin(value.target);
    if (current.id !== installationID || target.id !== body.target_installation_id || current.manifest.id !== target.manifest.id ||
      current.protocol_version !== target.protocol_version || (current.snapshot?.surface ?? "") !== (target.snapshot?.surface ?? "") ||
      current.package_fingerprint !== body.expected_current_fingerprint || target.package_fingerprint !== body.expected_target_fingerprint ||
      current.generation !== body.expected_current_generation + 1 || target.generation !== body.expected_target_generation + 1 ||
      current.state !== "rolled_back" || current.enabled_capabilities.length !== 0 || target.state !== "enabled" ||
      [...target.enabled_capabilities].sort().join(",") !== [...body.capabilities].sort().join(",")) throw new APIRequestError("Plugin rollback result differs from its version bindings", "INVALID_RESPONSE", 502);
    return { protocol_version: "plugin-lifecycle.v1", current, target };
  }

  async revokePluginPublisher(installationID: string, body: PluginPublisherRevocationRequestView, signal?: AbortSignal): Promise<PluginPublisherRevocationView> {
    if (!this.hasExtensionControl || !boundedIdentity(installationID) || body.version !== "plugin-lifecycle.v1" || body.confirm !== true ||
      !isSHA256(body.expected_publisher_fingerprint) || !safePositiveInteger(body.expected_publisher_generation)) throw new Error("An exact publisher trust binding and confirmation are required");
    const value = await this.sendControlRequest<unknown>(`/extensions/plugins/${encodeURIComponent(installationID)}/publisher-revocation`, body, signal);
    if (!hasExactKeys(value, ["protocol_version", "installation_id", "publisher"]) || value.protocol_version !== "plugin-lifecycle.v1" || value.installation_id !== installationID) throw new APIRequestError("Publisher revocation projection is invalid", "INVALID_RESPONSE", 502);
    const publisher = parsePluginPublisher(value.publisher);
    if (publisher.fingerprint !== body.expected_publisher_fingerprint || publisher.generation !== body.expected_publisher_generation + 1 || publisher.state !== "revoked") throw new APIRequestError("Publisher revocation binding is invalid", "INVALID_RESPONSE", 502);
    return { ...value, publisher } as PluginPublisherRevocationView;
  }

  async trustPluginPublisher(installationID: string,
    body: PluginPublisherTrustRequestView, signal?: AbortSignal,
  ): Promise<PluginPublisherTrustResponseView> {
    if (!this.hasExtensionControl || !boundedIdentity(installationID) ||
      body.version !== "plugin-lifecycle.v1" || body.confirm !== true ||
      !isSHA256(body.expected_publisher_fingerprint) ||
      !safeBoundedCount(body.expected_publisher_generation, Number.MAX_SAFE_INTEGER)) {
      throw new Error("An exact publisher trust binding and confirmation are required");
    }
    const value = await this.sendControlRequest<unknown>(
      `/extensions/plugins/${encodeURIComponent(installationID)}/publisher-trust`, body, signal);
    if (!hasExactKeys(value, ["protocol_version", "installation_id", "publisher"]) ||
      value.protocol_version !== "plugin-lifecycle.v1" || value.installation_id !== installationID) {
      throw new APIRequestError("Publisher trust projection is invalid", "INVALID_RESPONSE", 502);
    }
    const publisher = parsePluginPublisher(value.publisher);
    if (publisher.fingerprint !== body.expected_publisher_fingerprint ||
      publisher.generation !== body.expected_publisher_generation + 1 ||
      publisher.state !== "trusted") {
      throw new APIRequestError("Publisher trust binding is invalid", "INVALID_RESPONSE", 502);
    }
    return { ...value, publisher } as PluginPublisherTrustResponseView;
  }

  async hookDiagnostics(runID = "", workspaceID = "", signal?: AbortSignal): Promise<HookDiagnosticsView> {
    if ((runID && !boundedIdentity(runID)) || (workspaceID && !boundedIdentity(workspaceID))) throw new Error("An exact Hook diagnostic scope is required");
    return parseHookDiagnostics(await this.get<unknown>("/extensions/hooks", { run_id: runID || undefined, workspace_id: workspaceID || undefined }, signal), runID, workspaceID);
  }

  async safeWebReadiness(product: string, signal?: AbortSignal): Promise<BrowserSafeWebReadiness> {
    return parseSafeWebReadiness(await this.get<unknown>("/browser/safe-web-readiness",
      { product }, signal));
  }

  async uiEvidence(runID: string, signal?: AbortSignal): Promise<UIEvidenceAttempt[]> {
    if (!boundedIdentity(runID) || runID.trim() !== runID) {
      throw new Error("A normalized Run identity is required");
    }
    const value = await this.get<unknown>(
      `/runs/${encodeURIComponent(runID)}/ui-evidence`, { limit: 100 }, signal);
    if (!Array.isArray(value) || value.length > 500) {
      throw new APIRequestError("UI evidence list is invalid", "INVALID_RESPONSE", 502);
    }
    const attempts = value.map((entry) => parseUIEvidenceAttempt(entry, runID));
    if (new Set(attempts.map((entry) => entry.manifest.attempt_id)).size !== attempts.length) {
      throw new APIRequestError("UI evidence list contains duplicate attempts", "INVALID_RESPONSE", 502);
    }
    return attempts;
  }

  async uiEvidenceBundle(attemptID: string, signal?: AbortSignal): Promise<UIEvidenceBundle> {
    if (!boundedIdentity(attemptID) || attemptID.trim() !== attemptID) {
      throw new Error("A normalized UI evidence attempt identity is required");
    }
    return parseUIEvidenceBundle(await this.get<unknown>(
      `/ui-evidence/${encodeURIComponent(attemptID)}`, {}, signal), attemptID);
  }

  async startUIEvidence(runID: string, body: UIEvidenceStartView,
    signal?: AbortSignal): Promise<UIEvidenceAttempt> {
    if (!this.hasUIEvidence || !boundedIdentity(runID) || runID.trim() !== runID ||
      typeof body.operation_key !== "string" || body.operation_key.trim() !== body.operation_key ||
      body.operation_key.length < 1) {
      throw new Error("UI evidence control, a normalized Run, and an operation key are required");
    }
    return parseUIEvidenceAttempt(await this.sendControl<unknown>(
      `/runs/${encodeURIComponent(runID)}/ui-evidence`, body, body.operation_key, signal), runID);
  }

  async cancelUIEvidence(attemptID: string, signal?: AbortSignal): Promise<UIEvidenceAttempt> {
    if (!this.hasUIEvidence || !boundedIdentity(attemptID) || attemptID.trim() !== attemptID) {
      throw new Error("UI evidence control and a normalized attempt identity are required");
    }
    return parseUIEvidenceAttempt(await this.sendControl<unknown>(
      `/ui-evidence/${encodeURIComponent(attemptID)}/cancel`, { confirm: true },
      `ui-evidence-cancel-${attemptID}`, signal));
  }

  async downloadUIEvidenceArtifact(attemptID: string, metadata: UIEvidenceArtifactMetadata,
    signal?: AbortSignal): Promise<Blob> {
    if (!boundedIdentity(attemptID) || attemptID !== metadata.attempt_id ||
      !boundedIdentity(metadata.id) || !isSHA256(metadata.sha256) || metadata.untrusted !== true) {
      throw new Error("Exact untrusted UI evidence artifact metadata is required");
    }
    const response = await fetch(this.url(`/ui-evidence/${encodeURIComponent(attemptID)}/artifacts/` +
      encodeURIComponent(metadata.id)), {
      method: "GET",
      headers: { ...this.headers(), Accept: metadata.mime },
      signal,
      cache: "no-store",
      credentials: "omit",
      referrerPolicy: "no-referrer",
    });
    if (!response.ok) throw await this.responseError(response);
    if (response.headers.get("content-type") !== metadata.mime ||
      response.headers.get("etag") !== `"${metadata.sha256}"` ||
      response.headers.get("x-cyberagent-content-sha256") !== metadata.sha256 ||
      response.headers.get("x-cyberagent-evidence-untrusted") !== "true") {
      throw new APIRequestError("UI evidence artifact headers are invalid", "INVALID_RESPONSE",
        response.status, response.headers.get("x-request-id") || "");
    }
    const declaredLength = response.headers.get("content-length");
    if (declaredLength !== null && Number(declaredLength) !== metadata.bytes) {
      throw new APIRequestError("UI evidence artifact byte count changed", "INVALID_RESPONSE", 502);
    }
    if (!response.body) {
      throw new APIRequestError("UI evidence artifact body is unavailable", "INVALID_RESPONSE", 502);
    }
    const bytes = new Uint8Array(metadata.bytes);
    const reader = response.body.getReader();
    let offset = 0;
    try {
      while (true) {
        const { done, value } = await reader.read();
        if (done) break;
        if (offset + value.byteLength > bytes.byteLength) {
          await reader.cancel();
          throw new APIRequestError("UI evidence artifact exceeded its sealed byte count",
            "INVALID_RESPONSE", 502);
        }
        bytes.set(value, offset);
        offset += value.byteLength;
      }
    } finally {
      reader.releaseLock();
    }
    if (offset !== metadata.bytes) {
      throw new APIRequestError("UI evidence artifact byte count changed", "INVALID_RESPONSE", 502);
    }
    const digest = new Uint8Array(await globalThis.crypto.subtle.digest("SHA-256", bytes));
    const digestHex = [...digest].map((byte) => byte.toString(16).padStart(2, "0")).join("");
    if (digestHex !== metadata.sha256) {
      throw new APIRequestError("UI evidence artifact digest verification failed", "INVALID_RESPONSE", 502);
    }
    return new Blob([bytes], { type: metadata.mime });
  }

  async uploadWorkspaceFile(workspaceID: string, file: File, operationKey: string,
    signal?: AbortSignal): Promise<WorkspaceFileAttachment> {
    if (!boundedIdentity(workspaceID) || file.size > maximumFileBytes) throw new Error("每个文件不能超过 5 MiB。");
    const bytes = new Uint8Array(await file.arrayBuffer());
    let binary = "";
    for (let start = 0; start < bytes.length; start += 8192) binary += String.fromCharCode(...bytes.subarray(start, start + 8192));
    const name = [...file.name.replace(/[\u0000-\u001f\u007f/\\]/gu, "_")].slice(0, 160).join("") || "附件";
    const result = await this.postControl<{ attachment: unknown }>(
      `/workspaces/${encodeURIComponent(workspaceID)}/file-attachments`, {
        version: "workspace_file_upload.v1", name, mime_type: file.type || "application/octet-stream", data_base64: btoa(binary),
      }, operationKey, signal);
    const digest = [...new Uint8Array(await crypto.subtle.digest("SHA-256", bytes))]
      .map((byte) => byte.toString(16).padStart(2, "0")).join("");
    if (!validFileAttachment(result?.attachment) || result.attachment.workspace_id !== workspaceID ||
        result.attachment.sha256 !== digest || result.attachment.byte_size !== bytes.length || result.attachment.name !== name) {
      throw new APIRequestError("文件上传结果与原文件不一致，原请求标识已保留。", "INVALID_RESPONSE", 502);
    }
    return result.attachment;
  }

  async inspectWorkspaceFileUpload(workspaceID: string, operationKey: string, signal?: AbortSignal):
    Promise<{ state: "not_received" | "stored"; attachment?: WorkspaceFileAttachment }> {
    if (!boundedIdentity(workspaceID) || !boundedIdentity(operationKey)) throw new Error("文件上传请求标识无效。");
    const value = (await this.request<unknown>(`/workspaces/${encodeURIComponent(workspaceID)}/file-attachments/request`,
      {}, signal, operationKey)).data;
    if (!isRecord(value) || !["not_received", "stored"].includes(String(value.state)) ||
        (value.state === "stored" && (!validFileAttachment(value.attachment) || value.attachment.workspace_id !== workspaceID)) ||
        (value.state === "not_received" && value.attachment !== undefined)) {
      throw new APIRequestError("文件上传状态无法核对，原请求已保留。", "INVALID_RESPONSE", 502);
    }
    return value as { state: "not_received" | "stored"; attachment?: WorkspaceFileAttachment };
  }

  async downloadWorkspaceFile(metadata: WorkspaceFileAttachment, signal?: AbortSignal): Promise<Blob> {
    if (!validFileAttachment(metadata)) throw new Error("文件引用无效，无法下载。");
    return this.downloadVerifiedAttachment(`/workspaces/${encodeURIComponent(metadata.workspace_id)}/file-attachments/` +
      `${encodeURIComponent(metadata.id)}/content`, { ...metadata, mime_type: "application/octet-stream" }, signal);
  }

  async uploadWorkspaceImage(workspaceID: string, file: File, operationKey: string,
    signal?: AbortSignal): Promise<WorkspaceImageAttachment> {
    if (!boundedIdentity(workspaceID) || !acceptedImageTypes.includes(file.type) ||
      file.size < 1 || file.size > maximumImageBytes) throw new Error("请选择不超过 5 MiB 的 PNG、JPEG 或 WebP 图片。");
    const bytes = new Uint8Array(await file.arrayBuffer());
    let binary = "";
    for (let start = 0; start < bytes.length; start += 8192) binary += String.fromCharCode(...bytes.subarray(start, start + 8192));
    const result = await this.postControl<{ image: unknown }>(
      `/workspaces/${encodeURIComponent(workspaceID)}/image-attachments`, {
        version: "workspace_image_upload.v1", name: [...file.name.replace(/[\u0000-\u001f\u007f/\\]/gu, "_")].slice(0, 160).join(""), mime_type: file.type,
        data_base64: btoa(binary),
      }, operationKey, signal);
    const digest = [...new Uint8Array(await crypto.subtle.digest("SHA-256", bytes))]
      .map((byte) => byte.toString(16).padStart(2, "0")).join("");
    if (!validImageAttachment(result?.image) || result.image.workspace_id !== workspaceID ||
      result.image.sha256 !== digest || result.image.byte_size !== bytes.length || result.image.mime_type !== file.type) {
      throw new APIRequestError("图片上传结果与原图不一致，请重新选择。", "INVALID_RESPONSE", 502);
    }
    return result.image;
  }

  async downloadWorkspaceImage(metadata: WorkspaceImageAttachment, signal?: AbortSignal): Promise<Blob> {
    if (!validImageAttachment(metadata)) throw new Error("图片引用无效，无法预览。");
    return this.downloadVerifiedImage(`/workspaces/${encodeURIComponent(metadata.workspace_id)}/image-attachments/` +
      `${encodeURIComponent(metadata.id)}/content`, metadata, signal);
  }

  // Both user attachments and live previews are fetched with the read token.
  // Object URLs are created only after the declared size and digest match.
  async downloadVerifiedImage(path: string, metadata: { mime_type: string; byte_size: number; sha256: string },
    signal?: AbortSignal): Promise<Blob> {
    if (!acceptedImageTypes.includes(metadata.mime_type) || !isSHA256(metadata.sha256) ||
      !Number.isSafeInteger(metadata.byte_size) || metadata.byte_size < 1 || metadata.byte_size > 20 * 1024 * 1024) {
      throw new Error("图片元信息无效。");
    }
    return this.downloadVerifiedAttachment(path, metadata, signal);
  }

  private async downloadVerifiedAttachment(path: string, metadata: { mime_type: string; byte_size: number; sha256: string },
    signal?: AbortSignal): Promise<Blob> {
    const response = await fetch(this.url(path), { method: "GET", headers: { ...this.headers(), Accept: metadata.mime_type },
      signal, cache: "no-store", credentials: "omit", referrerPolicy: "no-referrer" });
    if (!response.ok) throw await this.responseError(response);
    if (response.headers.get("content-type") !== metadata.mime_type ||
      response.headers.get("etag") !== `"${metadata.sha256}"` ||
      response.headers.get("x-cyberagent-content-sha256") !== metadata.sha256 ||
      response.headers.get("content-length") !== String(metadata.byte_size) || (!response.body && metadata.byte_size !== 0)) {
      throw new APIRequestError("附件内容已变化或无法验证，请重新读取。", "INVALID_RESPONSE", 502);
    }
    const bytes = new Uint8Array(metadata.byte_size);
    const reader = response.body?.getReader();
    let offset = 0;
    try {
      while (reader) {
        const { done, value } = await reader.read();
        if (done) break;
        if (offset + value.byteLength > bytes.byteLength) { await reader.cancel(); throw new Error("附件大小超出已验证范围。"); }
        bytes.set(value, offset);
        offset += value.byteLength;
      }
    } finally { reader?.releaseLock(); }
    const sha256 = [...new Uint8Array(await crypto.subtle.digest("SHA-256", bytes))]
      .map((byte) => byte.toString(16).padStart(2, "0")).join("");
    if (offset !== bytes.length || sha256 !== metadata.sha256) throw new Error("附件内容验证失败，请重新读取。");
    return new Blob([bytes], { type: metadata.mime_type });
  }

  async modelAvailability(signal?: AbortSignal): Promise<ModelAvailabilityView> {
    const value = await this.get<unknown>("/models", {}, signal);
    return parseModelAvailability(value);
  }

  async getArtifact(artifactID: string, signal?: AbortSignal): Promise<ArtifactView> {
    const normalized = boundedIdentity(artifactID);
    if (!normalized || normalized !== artifactID) {
      throw new Error("A normalized artifact identity is required");
    }
    const value = await this.get<unknown>(
      `/artifacts/${encodeURIComponent(artifactID)}`, {}, signal,
    );
    if (!isRecord(value) || value.id !== artifactID || !boundedIdentity(value.run_id)) {
      throw new APIRequestError("Artifact response is invalid", "INVALID_RESPONSE", 502);
    }
    return value as unknown as ArtifactView;
  }

  async getNote(noteID: string, signal?: AbortSignal): Promise<NoteView> {
    const normalized = boundedIdentity(noteID);
    if (!normalized || normalized !== noteID) {
      throw new Error("A normalized note identity is required");
    }
    const value = await this.get<unknown>(
      `/notes/${encodeURIComponent(noteID)}`, {}, signal,
    );
    if (!isRecord(value) || value.id !== noteID || !boundedIdentity(value.run_id)) {
      throw new APIRequestError("Note response is invalid", "INVALID_RESPONSE", 502);
    }
    return value as unknown as NoteView;
  }

  async getWorkItem(workItemID: string, signal?: AbortSignal): Promise<WorkItemView> {
    const normalized = boundedIdentity(workItemID);
    if (!normalized || normalized !== workItemID) {
      throw new Error("A normalized Plan item identity is required");
    }
    const value = await this.get<unknown>(
      `/work-items/${encodeURIComponent(workItemID)}`, {}, signal,
    );
    if (!isRecord(value) || value.id !== workItemID || !boundedIdentity(value.run_id)) {
      throw new APIRequestError("Plan item response is invalid", "INVALID_RESPONSE", 502);
    }
    return value as unknown as WorkItemView;
  }

  async getRunExternalSkills(runID: string,
    signal?: AbortSignal): Promise<ExternalSkillProjectionView> {
    const normalized = boundedIdentity(runID);
    if (!normalized || normalized !== runID) {
      throw new Error("A normalized Run identity is required");
    }
    const value = await this.get<unknown>(
      `/runs/${encodeURIComponent(runID)}/external-skills`, {}, signal,
    );
    if (!isRecord(value) || value.run_id !== runID ||
      value.protocol_version !== "external_skill_projection.v1") {
      throw new APIRequestError("External skill projection response is invalid",
        "INVALID_RESPONSE", 502);
    }
    return value as unknown as ExternalSkillProjectionView;
  }

  async workspaceExplore(workspaceID: string, path = ".",
    signal?: AbortSignal): Promise<WorkspaceExplorerView> {
    if (!boundedIdentity(workspaceID) || !validWorkspaceRelativePath(path)) {
      throw new Error("A normalized Workspace identity and Go-issued relative path are required");
    }
    return parseWorkspaceExplorer(await this.get<unknown>(
      `/workspaces/${encodeURIComponent(workspaceID)}/explore`, { path }, signal,
    ), workspaceID, path);
  }

  async workspaceSearch(workspaceID: string, query: string,
    signal?: AbortSignal): Promise<WorkspaceSearchView> {
    if (!boundedIdentity(workspaceID) || !boundedText(query, 128) ||
      /[\u0000-\u001f\u007f]/u.test(query)) {
      throw new Error("A normalized Workspace identity and bounded query are required");
    }
    return parseWorkspaceSearch(await this.get<unknown>(
      `/workspaces/${encodeURIComponent(workspaceID)}/search`, { query }, signal,
    ), workspaceID);
  }

  async repositoryState(workspaceID: string,
    signal?: AbortSignal): Promise<RepositoryStateView> {
    if (!boundedIdentity(workspaceID) || workspaceID.trim() !== workspaceID) {
      throw new Error("A normalized Workspace identity is required");
    }
    return parseRepositoryState(await this.get<unknown>(
      `/workspaces/${encodeURIComponent(workspaceID)}/repository-state`, {}, signal,
    ), workspaceID);
  }

  async repositoryDiff(workspaceID: string,
    signal?: AbortSignal): Promise<RepositoryDiffView> {
    if (!boundedIdentity(workspaceID) || workspaceID.trim() !== workspaceID) {
      throw new Error("A normalized Workspace identity is required");
    }
    return parseRepositoryDiff(await this.get<unknown>(
      `/workspaces/${encodeURIComponent(workspaceID)}/repository-diff`, {}, signal,
    ), workspaceID);
  }

  async repositoryHistory(workspaceID: string,
    signal?: AbortSignal): Promise<RepositoryHistoryView> {
    if (!boundedIdentity(workspaceID) || workspaceID.trim() !== workspaceID) {
      throw new Error("A normalized Workspace identity is required");
    }
    return parseRepositoryHistory(await this.get<unknown>(
      `/workspaces/${encodeURIComponent(workspaceID)}/repository-history`, {}, signal,
    ), workspaceID);
  }

  async repositoryCommit(workspaceID: string, objectID: string,
    signal?: AbortSignal): Promise<RepositoryCommitDetailView> {
    if (!boundedIdentity(workspaceID) || workspaceID.trim() !== workspaceID ||
      !/^[0-9a-f]{40}$/u.test(objectID)) {
      throw new Error("A normalized Workspace identity and exact commit object are required");
    }
    return parseRepositoryCommitDetail(await this.get<unknown>(
      `/workspaces/${encodeURIComponent(workspaceID)}/repository-commits/${objectID}`, {}, signal,
    ), workspaceID, objectID);
  }

  async repositoryCommitComparison(workspaceID: string, baseObjectID: string,
    headObjectID: string, signal?: AbortSignal): Promise<RepositoryCommitComparisonView> {
    if (!boundedIdentity(workspaceID) || workspaceID.trim() !== workspaceID ||
      !/^[0-9a-f]{40}$/u.test(baseObjectID) || !/^[0-9a-f]{40}$/u.test(headObjectID)) {
      throw new Error("Normalized Workspace and exact commit identities are required");
    }
    return parseRepositoryCommitComparison(await this.get<unknown>(
      `/workspaces/${encodeURIComponent(workspaceID)}/repository-commit-comparison`,
      { base_object_id: baseObjectID, head_object_id: headObjectID }, signal,
    ), workspaceID, baseObjectID, headObjectID);
  }

  async repositoryFileHistory(workspaceID: string, path: string,
    signal?: AbortSignal): Promise<RepositoryFileHistoryView> {
    if (!boundedIdentity(workspaceID) || workspaceID.trim() !== workspaceID ||
      !validWorkspaceRelativePath(path) || path === ".") {
      throw new Error("A normalized Workspace identity and canonical file path are required");
    }
    return parseRepositoryFileHistory(await this.get<unknown>(
      `/workspaces/${encodeURIComponent(workspaceID)}/repository-file-history`, { path }, signal,
    ), workspaceID, path);
  }

  async repositoryCommitFilePreview(workspaceID: string, objectID: string, path: string,
    signal?: AbortSignal): Promise<RepositoryCommitFilePreviewView> {
    if (!boundedIdentity(workspaceID) || workspaceID.trim() !== workspaceID ||
      !/^[0-9a-f]{40}$/u.test(objectID) || !validWorkspaceRelativePath(path) || path === ".") {
      throw new Error("A normalized Workspace, exact commit object, and canonical file path are required");
    }
    return parseRepositoryCommitFilePreview(await this.get<unknown>(
      `/workspaces/${encodeURIComponent(workspaceID)}/repository-commits/${objectID}/file-preview`,
      { path }, signal,
    ), workspaceID, objectID, path);
  }

  async operationReceiptHistory(runID = "",
    signal?: AbortSignal): Promise<OperationReceiptHistoryView> {
    if (runID !== "" && (!boundedIdentity(runID) || runID.trim() !== runID)) {
      throw new Error("A normalized Run identity is required");
    }
    return parseOperationReceiptHistory(await this.get<unknown>(
      "/operation-receipts", { run_id: runID || undefined, limit: 100 }, signal,
    ), runID);
  }

  async operatorActionCenter(runID: string,
    signal?: AbortSignal): Promise<OperatorActionCenterView> {
    if (!boundedIdentity(runID) || runID.trim() !== runID) {
      throw new Error("A normalized Run identity is required");
    }
    return parseOperatorActionCenter(await this.get<unknown>(
      `/runs/${encodeURIComponent(runID)}/operator-actions`, {}, signal,
    ), runID);
  }

  async evidenceInventory(runID: string,
    signal?: AbortSignal): Promise<EvidenceInventoryView> {
    if (!boundedIdentity(runID) || runID.trim() !== runID) {
      throw new Error("A normalized Run identity is required");
    }
    return parseEvidenceInventory(await this.get<unknown>(
      `/runs/${encodeURIComponent(runID)}/evidence-attachments`, {}, signal,
    ), runID);
  }

  async verificationEvidence(runID: string,
    signal?: AbortSignal): Promise<VerificationEvidenceInventoryView> {
    if (!boundedIdentity(runID) || runID.trim() !== runID) {
      throw new Error("A normalized Run identity is required");
    }
    return parseVerificationEvidenceInventory(await this.get<unknown>(
      `/runs/${encodeURIComponent(runID)}/verification-evidence`, {}, signal,
    ), runID);
  }

  async recordVerificationEvidence(runID: string, body: VerificationEvidenceRequestView,
    idempotencyKey: string, signal?: AbortSignal): Promise<VerificationEvidenceControlView> {
    if (!this.hasVerificationEvidence || !boundedIdentity(runID) ||
      body.version !== "operator_verification_evidence.v1" ||
      !["pass", "fail", "unknown"].includes(body.outcome) ||
      !validVerificationText(body.title, 160, false) ||
      !validVerificationText(body.summary, 2048, true)) {
      throw new Error("Verification evidence capability and a bounded observation are required");
    }
    return parseVerificationEvidenceItem(await this.sendControl<unknown>(
      `/runs/${encodeURIComponent(runID)}/verification-evidence`, body, idempotencyKey, signal,
    ), runID, "", "", true);
  }

  async verificationPlans(runID: string,
    signal?: AbortSignal): Promise<VerificationPlanInventoryView> {
    if (!boundedIdentity(runID) || runID.trim() !== runID) {
      throw new Error("A normalized Run identity is required");
    }
    return parseVerificationPlanInventory(await this.get<unknown>(
      `/runs/${encodeURIComponent(runID)}/verification-plan`, {}, signal,
    ), runID);
  }

  async recordVerificationPlan(runID: string, body: VerificationPlanRequestView,
    idempotencyKey: string, signal?: AbortSignal): Promise<VerificationPlanControlView> {
    if (!this.hasVerificationEvidence || !boundedIdentity(runID) ||
      body.version !== "operator_verification_plan.v1" ||
      !validVerificationText(body.title, 160, false) ||
      !validVerificationText(body.summary, 2048, true) || !Array.isArray(body.items) ||
      body.items.length < 1 || body.items.length > 32 || body.items.some((item) =>
        !validVerificationText(item.title, 160, false) ||
        !validVerificationText(item.expected_observation, 1024, true))) {
      throw new Error("Verification capability and a bounded operator checklist are required");
    }
    return parseVerificationPlan(await this.sendControl<unknown>(
      `/runs/${encodeURIComponent(runID)}/verification-plan`, body,
      idempotencyKey, signal), runID, "", "", true);
  }

  async verificationPlanCoverage(runID: string,
    signal?: AbortSignal): Promise<VerificationPlanCoverageInventoryView> {
    if (!boundedIdentity(runID) || runID.trim() !== runID) {
      throw new Error("A normalized Run identity is required");
    }
    return parseVerificationPlanCoverage(await this.get<unknown>(
      `/runs/${encodeURIComponent(runID)}/verification-plan-coverage`, {}, signal,
    ), runID);
  }

  async verificationPlanItemCoverage(runID: string, planID: string, ordinal: number,
    signal?: AbortSignal): Promise<VerificationPlanItemCoverageDetailView> {
    return (await this.verificationPlanItemCoveragePage(runID, planID, ordinal, "", 50,
      signal)).detail;
  }

  async verificationPlanItemCoveragePage(runID: string, planID: string, ordinal: number,
    cursor = "", limit = 50,
    signal?: AbortSignal): Promise<VerificationPlanItemCoveragePage> {
    if (!boundedIdentity(runID) || runID.trim() !== runID || !boundedIdentity(planID) ||
      planID.trim() !== planID || !safePositiveInteger(ordinal) || ordinal > 32 ||
      !safePositiveInteger(limit) || limit > 100 || typeof cursor !== "string" ||
      cursor.length > 512 || cursor.trim() !== cursor) {
      throw new Error("Normalized Run, plan, and item identities are required");
    }
    const envelope = await this.request<unknown>(
      `/runs/${encodeURIComponent(runID)}/verification-plan-coverage/` +
        `${encodeURIComponent(planID)}/items/${ordinal}`,
      { limit: limit === 50 ? undefined : limit, cursor: cursor || undefined }, signal);
    if (!envelope.page) {
      throw new APIRequestError("Verification coverage page metadata is missing",
        "INVALID_RESPONSE", 502, envelope.request_id);
    }
    const page = parseResponsePage(envelope.page, limit);
    return { detail: parseVerificationPlanItemCoverage(envelope.data, runID, planID, ordinal,
      page, cursor === ""), page, requestID: envelope.request_id };
  }

  async verificationPlanItemSnapshotExport(runID: string, planID: string, ordinal: number,
    format: "json" | "markdown", signal?: AbortSignal): Promise<VerificationSnapshotExportView> {
    if (!boundedIdentity(runID) || runID.trim() !== runID || !boundedIdentity(planID) ||
      planID.trim() !== planID || !safePositiveInteger(ordinal) || ordinal > 32 ||
      !["json", "markdown"].includes(format)) {
      throw new Error("Normalized Run, plan, item, and snapshot format are required");
    }
    return parseVerificationSnapshotExport(await this.get<unknown>(
      `/runs/${encodeURIComponent(runID)}/verification-plan-coverage/` +
        `${encodeURIComponent(planID)}/items/${ordinal}/snapshot-export`, { format }, signal,
    ), runID, planID, ordinal, format);
  }

  async verificationSnapshotReceipts(runID: string,
    signal?: AbortSignal): Promise<VerificationSnapshotReceiptInventoryView> {
    if (!boundedIdentity(runID) || runID.trim() !== runID) {
      throw new Error("A normalized Run identity is required");
    }
    return parseVerificationSnapshotReceiptInventory(await this.get<unknown>(
      `/runs/${encodeURIComponent(runID)}/verification-snapshot-receipts`, {}, signal,
    ), runID);
  }

  async recordVerificationSnapshotReceipt(runID: string,
    body: VerificationSnapshotReceiptRequestView, idempotencyKey: string,
    signal?: AbortSignal): Promise<VerificationSnapshotReceiptControlView> {
    if (!this.hasVerificationEvidence || !boundedIdentity(runID) ||
      body.version !== "operator_verification_plan_item_snapshot_receipt.v1" ||
      !boundedIdentity(body.plan_id) || !safePositiveInteger(body.plan_item_ordinal) ||
      body.plan_item_ordinal > 32 || !["json", "markdown"].includes(body.format) ||
      !safeBoundedCount(body.snapshot_high_water_event_sequence, Number.MAX_SAFE_INTEGER) ||
      !isSHA256(body.content_sha256) || body.confirm_metadata_snapshot !== true) {
      throw new Error("Verification capability and an exact confirmed snapshot digest are required");
    }
    return parseVerificationSnapshotReceipt(await this.sendControl<unknown>(
      `/runs/${encodeURIComponent(runID)}/verification-snapshot-receipts`, body,
      idempotencyKey, signal), runID, "", "", true);
  }

  async verificationSnapshotReceiptReviews(runID: string,
    signal?: AbortSignal): Promise<VerificationSnapshotReceiptReviewInventoryView> {
    if (!boundedIdentity(runID) || runID.trim() !== runID) {
      throw new Error("A normalized Run identity is required");
    }
    return parseVerificationSnapshotReceiptReviewInventory(await this.get<unknown>(
      `/runs/${encodeURIComponent(runID)}/verification-snapshot-receipt-reviews`, {}, signal,
    ), runID);
  }

  async recordVerificationSnapshotReceiptReview(runID: string,
    body: VerificationSnapshotReceiptReviewRequestView, idempotencyKey: string,
    signal?: AbortSignal): Promise<VerificationSnapshotReceiptReviewControlView> {
    if (!this.hasVerificationEvidence || !boundedIdentity(runID) ||
      body.version !== "operator_verification_plan_item_snapshot_receipt_review.v1" ||
      !boundedIdentity(body.receipt_id) || !isSHA256(body.receipt_content_sha256) ||
      !safePositiveInteger(body.receipt_event_sequence) ||
      !["metadata_confirmed", "metadata_disputed"].includes(body.decision) ||
      body.confirm_non_authorizing_review !== true) {
      throw new Error("Verification capability and an exact non-authorizing review are required");
    }
    return parseVerificationSnapshotReceiptReview(await this.sendControl<unknown>(
      `/runs/${encodeURIComponent(runID)}/verification-snapshot-receipt-reviews`, body,
      idempotencyKey, signal), runID, "", "", true);
  }

  async associateVerificationEvidence(runID: string, body: VerificationAssociationRequestView,
    idempotencyKey: string, signal?: AbortSignal): Promise<VerificationAssociationControlView> {
    if (!this.hasVerificationEvidence || !boundedIdentity(runID) ||
      body.version !== "operator_verification_plan_evidence_association.v1" ||
      !boundedIdentity(body.plan_id) || !safePositiveInteger(body.plan_item_ordinal) ||
      body.plan_item_ordinal > 32 || !boundedIdentity(body.evidence_id)) {
      throw new Error("Verification capability and exact plan/evidence identities are required");
    }
    return parseVerificationAssociation(await this.sendControl<unknown>(
      `/runs/${encodeURIComponent(runID)}/verification-plan-associations`, body,
      idempotencyKey, signal), runID, true);
  }

  async codeHandoff(runID: string, signal?: AbortSignal): Promise<CodeHandoffView> {
    if (!boundedIdentity(runID) || runID.trim() !== runID) {
      throw new Error("A normalized Run identity is required");
    }
    return parseCodeHandoff(await this.get<unknown>(
      `/runs/${encodeURIComponent(runID)}/code-handoff`, {}, signal,
    ), runID);
  }

  async standardCodeDelivery(runID: string,
    signal?: AbortSignal): Promise<StandardCodeDeliveryView> {
    if (!boundedIdentity(runID) || runID.trim() !== runID) {
      throw new Error("A normalized Run identity is required");
    }
    return parseStandardCodeDelivery(await this.get<unknown>(
      `/runs/${encodeURIComponent(runID)}/standard-code-delivery`, {}, signal,
    ), runID);
  }

  async recordStandardCodeDelivery(runID: string, body: StandardCodeDeliveryRecordRequestView,
    idempotencyKey: string, signal?: AbortSignal): Promise<StandardCodeDeliveryRecordResultView> {
    if (!this.hasWorkspaceCheckpointControl || !boundedIdentity(runID) ||
      runID.trim() !== runID || typeof body.operation_key !== "string" ||
      body.operation_key.trim() !== body.operation_key || body.operation_key.length === 0 ||
      !Array.isArray(body.verification_job_ids) || !Array.isArray(body.uncovered_items)) {
      throw new Error("Standard Code delivery control and a bounded exact intent are required");
    }
    const value = await this.sendControl<unknown>(
      `/runs/${encodeURIComponent(runID)}/standard-code-delivery`, body, idempotencyKey, signal);
    if (!isRecord(value) || !hasExactKeys(value, ["replayed", "report"]) ||
      typeof value.replayed !== "boolean") {
      throw new APIRequestError("Invalid Standard Code delivery record response",
        "INVALID_RESPONSE", 502);
    }
    return { replayed: value.replayed,
      report: parseStandardCodeDelivery(value.report, runID) };
  }

  async codeHandoffExport(runID: string, format: "json" | "markdown",
    signal?: AbortSignal): Promise<CodeHandoffExportView> {
    if (!boundedIdentity(runID) || runID.trim() !== runID ||
      !["json", "markdown"].includes(format)) {
      throw new Error("A normalized Run identity and supported handoff format are required");
    }
    return parseCodeHandoffExport(await this.get<unknown>(
      `/runs/${encodeURIComponent(runID)}/code-handoff/export`, { format }, signal,
    ), runID, format);
  }

  async attachEvidence(runID: string, body: EvidenceAttachmentRequestView,
    idempotencyKey: string, signal?: AbortSignal): Promise<EvidenceAttachmentView> {
    if (!this.hasEvidenceAttachment || !boundedIdentity(runID) ||
      body.version !== "session_evidence_attachment.v1" ||
      body.source_kind !== "workspace_file" || !validWorkspaceRelativePath(body.source_ref) ||
      !isSHA256(body.content_sha256)) {
      throw new Error("Evidence attachment capability and exact Workspace provenance are required");
    }
    return parseEvidenceAttachment(await this.sendControl<unknown>(
      `/runs/${encodeURIComponent(runID)}/evidence-attachments`, body, idempotencyKey, signal,
    ), runID, body);
  }

  async selectModelRoute(route: string, body: ModelRouteControlRequestView,
    signal?: AbortSignal): Promise<ModelAvailabilityView["routes"][number]> {
    if (!this.hasModelControl || !boundedIdentity(route) || route.trim() !== route) {
      throw new Error("Model control capability and a normalized route are required");
    }
    const result = await this.sendControlRequest<unknown>(
      `/models/routes/${encodeURIComponent(route)}`, body, signal,
    );
    return parseModelRouteControl(result, route, body);
  }

  async availableModelRoutes(signal?: AbortSignal): Promise<AvailableModelRouteCollectionView> {
    if (!this.hasModelControl) {
      throw new Error("Model control capability is required to list selectable routes");
    }
    return parseAvailableModelRouteCollection(await this.get<unknown>(
      "/models/routes/available", {}, signal));
  }

  async threadModelRoute(threadID: string, signal?: AbortSignal): Promise<ThreadModelRouteView> {
    if (!this.hasModelControl || !boundedIdentity(threadID) || threadID.trim() !== threadID) {
      throw new Error("Model control capability and a normalized Thread are required");
    }
    return parseThreadModelRoute(await this.get<unknown>(
      `/threads/${encodeURIComponent(threadID)}/model-route`, {}, signal), threadID);
  }

  async providerSearchReadiness(threadID: string,
    signal?: AbortSignal): Promise<ProviderSearchReadinessView> {
    if (!boundedIdentity(threadID) || threadID.trim() !== threadID) {
      throw new Error("A normalized Thread is required to read search readiness");
    }
    return parseProviderSearchReadiness(await this.get<unknown>(
      `/threads/${encodeURIComponent(threadID)}/search-readiness`, {}, signal), threadID);
  }

  async diagnoseThreadSearch(threadID: string, body: SearchDiagnosticsRequestView,
    signal?: AbortSignal): Promise<SearchDiagnosticsView> {
    if (!this.hasControl || !boundedIdentity(threadID) || threadID.trim() !== threadID ||
      body.version !== "search_diagnostics.v1" || body.confirm !== true) {
      throw new Error("Explicit confirmation and a normalized Thread are required for search diagnostics");
    }
    return parseSearchDiagnostics(await this.sendControlRequest<unknown>(
      `/threads/${encodeURIComponent(threadID)}/search-diagnostics`, body, signal), threadID);
  }

  async selectThreadModelRoute(threadID: string, body: ThreadModelRouteControlRequestView,
    signal?: AbortSignal): Promise<ThreadModelRouteView> {
    const select = body.action === "select";
    const reset = body.action === "reset";
    if (!this.hasModelControl || !boundedIdentity(threadID) || threadID.trim() !== threadID ||
      body.version !== "thread_model_route_control.v1" || (!select && !reset) ||
      !boundedIdentity(body.operation_key) || !boundedIdentity(body.requested_by) ||
      (select && (!boundedIdentity(body.provider) || !boundedIdentity(body.model))) ||
      (reset && (body.provider !== undefined || body.model !== undefined))) {
      throw new Error("A normalized, exact Thread model route intent is required");
    }
    const result = await this.sendControlRequest<unknown>(
      `/threads/${encodeURIComponent(threadID)}/model-route`, body, signal, "", "PUT");
    return parseThreadModelRoute(result, threadID, body);
  }

  async diagnoseProvider(body: ProviderDiagnosticRequestView,
    signal?: AbortSignal): Promise<ProviderDiagnosticView> {
    if (!this.hasModelControl || body.confirm_diagnostic !== true) {
      throw new Error("Explicit Provider diagnostic confirmation is required");
    }
    const result = await this.sendControlRequest<unknown>("/models/diagnostics", body, signal);
    return parseProviderDiagnostic(result, body);
  }

  async qualifyModelHarness(body: ModelHarnessQualificationRequestView,
    signal?: AbortSignal): Promise<ModelHarnessQualificationView> {
    if (!this.hasModelControl || body.confirm_qualification !== true) {
      throw new Error("Explicit model Harness qualification confirmation is required");
    }
    const result = await this.sendControlRequest<unknown>(
      "/models/harness-qualifications", body, signal,
    );
    return parseModelHarnessQualification(result, body);
  }

  async chatGPTAuthStatus(signal?: AbortSignal): Promise<ChatGPTAuthStatusView> {
    if (!this.hasChatGPTAuth) {
      throw new APIRequestError("ChatGPT 账户登录当前未启用。", "PERMISSION_DENIED", 403);
    }
    return this.parseChatGPTStatus(await this.get<unknown>("/models/chatgpt", {}, signal));
  }

  async startChatGPTLogin(body: ChatGPTLoginRequestView, signal?: AbortSignal): Promise<ChatGPTAuthStatusView> {
    return this.chatGPTAuthAction("login", body, signal);
  }

  async cancelChatGPTLogin(body: ChatGPTCancelRequestView, signal?: AbortSignal): Promise<ChatGPTAuthStatusView> {
    return this.chatGPTAuthAction("cancel", body, signal);
  }

  async logoutChatGPTAccount(body: ChatGPTAccountRequestView, signal?: AbortSignal): Promise<ChatGPTAuthStatusView> {
    return this.chatGPTAuthAction("logout", body, signal);
  }

  async refreshChatGPTModels(body: ChatGPTAccountRequestView, signal?: AbortSignal): Promise<ChatGPTAuthStatusView> {
    return this.chatGPTAuthAction("models", body, signal);
  }

  async openChatGPTUsage(body: ChatGPTAuthRequestView, signal?: AbortSignal): Promise<ChatGPTAuthStatusView> {
    return this.chatGPTAuthAction("usage", body, signal);
  }

  private parseChatGPTStatus(value: unknown): ChatGPTAuthStatusView {
    try { return parseChatGPTAuthStatus(value); }
    catch { throw new APIRequestError("ChatGPT 账户状态无效，请重新读取。", "INVALID_RESPONSE", 502); }
  }

  private async chatGPTAuthAction(action: "login" | "cancel" | "logout" | "models" | "usage",
    body: ChatGPTAuthRequestView, signal?: AbortSignal): Promise<ChatGPTAuthStatusView> {
    if (!this.hasChatGPTAuth) {
      throw new APIRequestError("ChatGPT 账户登录当前未启用。", "PERMISSION_DENIED", 403);
    }
    const request = body as unknown;
    const identity = action === "cancel" ? "attempt_id" : "account_id";
    const requiresIdentity = action === "cancel" || action === "logout" || action === "models";
    const allowed = action === "usage" ? ["version", "confirm"] : ["version", "confirm", identity];
    if (!isRecord(request) || request.version !== chatGPTAuthProtocol || request.confirm !== true ||
      Object.keys(request).some((key) => !allowed.includes(key)) ||
      (requiresIdentity && !validChatGPTIdentity(request[identity])) ||
      (action === "login" && request.account_id !== undefined && !validChatGPTIdentity(request.account_id))) {
      throw new APIRequestError("ChatGPT 账户操作需要有效身份与明确确认。", "INVALID_ARGUMENT", 400);
    }
    return this.parseChatGPTStatus(await this.sendControlRequest<unknown>(`/models/chatgpt/${action}`, body, signal));
  }

  async providerDefinitions(signal?: AbortSignal): Promise<ProviderDefinitionCollectionView> {
    if (!this.hasProviderDefinitions) {
      throw new Error("Custom Provider definition capability is required");
    }
    return parseProviderDefinitionCollection(await this.get<unknown>(
      "/models/provider-definitions", {}, signal));
  }

  async discoverProviderModels(body: ProviderModelDiscoveryRequestView,
    signal?: AbortSignal): Promise<ProviderModelDiscoveryView> {
    if (!this.hasProviderDefinitions || body.version !== "provider_model_discovery.v1" ||
      body.confirm_discovery !== true || !validCustomProviderID(body.provider_id) ||
      typeof body.endpoint_url !== "string" || !body.endpoint_url || body.endpoint_url.length > 2048 ||
      !["openai_chat_completions", "openai_responses", "anthropic_messages"].includes(body.transport) ||
      !validProviderAdvancedConfig(body.advanced_config) ||
      (body.secret !== undefined && (typeof body.secret !== "string" || body.secret.length < 8 ||
        body.secret.length > 2560 || /\s/.test(body.secret))) ||
      (body.expected_definition_revision !== undefined && !safePositiveInteger(body.expected_definition_revision))) {
      throw new Error("Confirmed model discovery capability and a valid draft are required");
    }
    const value = await this.sendControlRequest<unknown>("/models/model-discovery", body, signal);
    const safeText = (text: unknown, limit: number): text is string => typeof text === "string" &&
      text.length <= limit && text.trim() === text && !/[\x00-\x1f\x7f]/.test(text) &&
      (!body.secret || !text.includes(body.secret));
    if (!hasExactKeys(value, ["version", "source", "models", "truncated"]) ||
      value.version !== "provider_model_discovery.v1" || value.source !== "provider_api" ||
      typeof value.truncated !== "boolean" || !Array.isArray(value.models) || value.models.length > 512 ||
      value.models.some((model: unknown) => !isRecord(model) ||
        Object.keys(model).some((key) => !["id", "display_name", "input_token_limit", "output_token_limit"].includes(key)) ||
        !boundedIdentity(model.id) || !safeText(model.id, 256) ||
        (model.display_name !== undefined && !safeText(model.display_name, 256)) ||
        (model.input_token_limit !== undefined && (!safePositiveInteger(model.input_token_limit) || Number(model.input_token_limit) > 2_097_152)) ||
        (model.output_token_limit !== undefined && (!safePositiveInteger(model.output_token_limit) || Number(model.output_token_limit) > 1_000_000))) ||
      new Set(value.models.map((m: { id: unknown }) => m.id)).size !== value.models.length) {
      throw new APIRequestError("Provider model discovery returned an invalid catalog", "INVALID_RESPONSE", 502);
    }
    return value as unknown as ProviderModelDiscoveryView;
  }

  async upsertProviderDefinition(provider: string, body: ProviderDefinitionUpsertRequestView,
    signal?: AbortSignal): Promise<ProviderDefinitionMutationView> {
    if (!this.hasProviderDefinitions || !validCustomProviderID(provider) ||
      body.version !== "provider_definition_control.v1" || body.confirm !== true ||
      body.definition.id !== provider) {
      throw new Error("Confirmed custom Provider definition capability is required");
    }
    parseProviderDefinition(body.definition);
    return parseProviderDefinitionMutation(await this.sendControlRequest<unknown>(
      `/models/provider-definitions/${encodeURIComponent(provider)}`, body, signal,
    ), provider, false);
  }

  async deleteProviderDefinition(provider: string, body: ProviderDefinitionDeleteRequestView,
    signal?: AbortSignal): Promise<ProviderDefinitionMutationView> {
    if (!this.hasProviderDefinitions || !validCustomProviderID(provider) ||
      body.version !== "provider_definition_control.v1" || body.confirm !== true ||
      !safeBoundedCount(body.expected_collection_revision, Number.MAX_SAFE_INTEGER) ||
      !safePositiveInteger(body.expected_definition_revision)) {
      throw new Error("Confirmed custom Provider deletion capability is required");
    }
    return parseProviderDefinitionMutation(await this.sendControlRequest<unknown>(
      `/models/provider-definitions/${encodeURIComponent(provider)}/delete`, body, signal,
    ), provider, true);
  }

  async providerCredentialStatuses(signal?: AbortSignal): Promise<ProviderCredentialListView> {
    if (!this.hasProviderCredentials) {
      throw new Error("Provider credential capability is required");
    }
    return parseProviderCredentialList(await this.get<unknown>("/models/credentials", {}, signal));
  }

  async changeProviderCredential(provider: string, body: ProviderCredentialRequestView,
    signal?: AbortSignal): Promise<ProviderCredentialStatusView> {
    if (!this.hasProviderCredentials ||
      !validProviderCredentialName(provider) ||
      body.version !== "provider_credential.v1" || body.confirm !== true ||
      (body.action === "set" ? typeof body.secret !== "string" || body.secret.length < 8 :
        body.action !== "delete" || body.secret !== "")) {
      throw new Error("Confirmed Provider credential capability is required");
    }
    const result = await this.sendControlRequest<unknown>(
      `/models/credentials/${encodeURIComponent(provider)}`, body, signal,
    );
    const status = parseProviderCredentialStatus(result, provider);
    const applied = status.registry_reloaded && !status.restart_required &&
      status.registry_generation > 0;
    const deferred = !status.registry_reloaded && status.restart_required;
    if (status.configured !== (body.action === "set") || (!applied && !deferred)) {
      throw new APIRequestError("Provider credential change returned the wrong status",
        "INVALID_RESPONSE", 502);
    }
    return status;
  }

  async fileEditQueue(runID: string, signal?: AbortSignal): Promise<FileEditQueueView> {
    if (!boundedIdentity(runID) || runID.trim() !== runID) {
      throw new Error("A normalized Run identity is required");
    }
    return parseFileEditQueue(await this.get<unknown>(
      `/runs/${encodeURIComponent(runID)}/file-edits`, {}, signal,
    ), runID);
  }

  async fileEdit(runID: string, editID: string,
    signal?: AbortSignal): Promise<FileEditPreviewView> {
    if (!boundedIdentity(runID) || runID.trim() !== runID ||
      !boundedIdentity(editID) || editID.trim() !== editID) {
      throw new Error("Normalized Run and file edit identities are required");
    }
    const edit = parseFileEditPreview(await this.get<unknown>(
      `/runs/${encodeURIComponent(runID)}/file-edits/${encodeURIComponent(editID)}`,
      {}, signal,
    ));
    if (edit.id !== editID) {
      throw new APIRequestError("File edit preview returned the wrong identity",
        "INVALID_RESPONSE", 502);
    }
    return edit;
  }

  async fileEditChangeSet(runID: string,
    signal?: AbortSignal): Promise<FileEditChangeSetView> {
    if (!boundedIdentity(runID) || runID.trim() !== runID) {
      throw new Error("A normalized Run identity is required");
    }
    return parseFileEditChangeSet(await this.get<unknown>(
      `/runs/${encodeURIComponent(runID)}/file-edit-change-set`, {}, signal,
    ), runID);
  }

  async issueFileEditProposalSource(runID: string, path: string,
    signal?: AbortSignal): Promise<FileEditProposalSourceView> {
    if (!this.hasFileEditProposals || !boundedIdentity(runID) ||
      !validWorkspaceRelativePath(path)) {
      throw new Error("File edit proposal capability and a Go-issued path are required");
    }
    return parseFileEditProposalSource(await this.get<unknown>(
      `/runs/${encodeURIComponent(runID)}/file-edit-proposal-source`, { path }, signal,
    ), runID, path);
  }

  async reissueFileEditProposalSource(runID: string, path: string, expectedSHA256: string,
    signal?: AbortSignal): Promise<FileEditProposalSourceView> {
    if (!this.hasFileEditProposals || !boundedIdentity(runID) ||
      !validWorkspaceRelativePath(path) || !isSHA256(expectedSHA256)) {
      throw new Error("An exact previously issued file digest is required");
    }
    return parseFileEditProposalSource(await this.get<unknown>(
      `/runs/${encodeURIComponent(runID)}/file-edit-proposal-source`,
      { path, expected_sha256: expectedSHA256 }, signal,
    ), runID, path);
  }

  async recoverFileEditProposal(runID: string, editID: string,
    signal?: AbortSignal): Promise<FileEditProposalRecoveryView> {
    if (!this.hasFileEditProposals || !boundedIdentity(runID) || !boundedIdentity(editID)) {
      throw new Error("File edit recovery requires exact Run and proposal identities");
    }
    return parseFileEditProposalRecovery(await this.get<unknown>(
      `/runs/${encodeURIComponent(runID)}/file-edit-proposal-recovery/${encodeURIComponent(editID)}`,
      {}, signal,
    ), runID, editID);
  }

  async createFileEditProposal(runID: string, body: FileEditProposalRequestView,
    signal?: AbortSignal): Promise<FileEditProposalView> {
    if (!this.hasFileEditProposals || !boundedIdentity(runID) ||
      body.version !== "file_edit_proposal.v1" ||
      !/^[A-Za-z0-9_-]{43}$/u.test(body.source_handle) ||
      typeof body.proposed_text !== "string" ||
      new TextEncoder().encode(body.proposed_text).length > 256 * 1024) {
      throw new Error("A bounded Go-issued file edit proposal is required");
    }
    return parseFileEditProposal(await this.sendControlRequest<unknown>(
      `/runs/${encodeURIComponent(runID)}/file-edit-proposals`, body, signal,
    ), runID);
  }

  async reviewFileEdit(runID: string, editID: string, body: FileEditReviewRequestView,
    signal?: AbortSignal): Promise<FileEditReviewView> {
    if (!this.hasFileEditReview || !boundedIdentity(runID) || !boundedIdentity(editID)) {
      throw new Error("File edit review capability and normalized identities are required");
    }
    const result = await this.sendControlRequest<unknown>(
      `/runs/${encodeURIComponent(runID)}/file-edits/${encodeURIComponent(editID)}/review`,
      body, signal,
    );
    return parseFileEditReview(result, runID, editID, body);
  }

  async applyFileEdit(runID: string, editID: string, body: FileEditApplyRequestView,
    idempotencyKey: string, signal?: AbortSignal): Promise<FileEditApplyView> {
    if (!this.hasFileEditApply || !boundedIdentity(runID) || !boundedIdentity(editID)) {
      throw new Error("File edit apply capability and normalized identities are required");
    }
    const result = await this.sendControl<unknown>(
      `/runs/${encodeURIComponent(runID)}/file-edits/${encodeURIComponent(editID)}/apply`,
      body, idempotencyKey, signal,
    );
    return parseFileEditApply(result, runID, editID);
  }

  async runWakeState(runID: string, signal?: AbortSignal): Promise<RunWakeStateView> {
    if (!boundedIdentity(runID) || runID.trim() !== runID) {
      throw new Error("A normalized Run identity is required");
    }
    return parseRunWakeState(await this.get<unknown>(
      `/runs/${encodeURIComponent(runID)}/wake-intent`, {}, signal,
    ), runID);
  }

  async scheduleRunWake(runID: string, body: RunWakeScheduleRequestView,
    idempotencyKey: string, signal?: AbortSignal): Promise<RunWakeControlView> {
    if (!this.hasRunWakeControl) {
      throw new Error("Run wake control capability is required");
    }
    return parseRunWakeControl(await this.sendControl<unknown>(
      `/runs/${encodeURIComponent(runID)}/wake-intent`, body, idempotencyKey, signal,
    ), runID, "schedule");
  }

  async cancelRunWake(runID: string, body: RunWakeCancelRequestView,
    idempotencyKey: string, signal?: AbortSignal): Promise<RunWakeControlView> {
    if (!this.hasRunWakeControl) {
      throw new Error("Run wake control capability is required");
    }
    return parseRunWakeControl(await this.sendControl<unknown>(
      `/runs/${encodeURIComponent(runID)}/wake-intent/cancel`, body, idempotencyKey, signal,
    ), runID, "cancel");
  }

  async consumeRunWake(runID: string, body: RunWakeExecutionRequestView,
    signal?: AbortSignal): Promise<RunWakeExecutionView> {
    if (!this.hasRunWakeExecution || !boundedIdentity(runID) || runID.trim() !== runID) {
      throw new Error("Foreground Run wake capability and a normalized Run are required");
    }
    return parseRunWakeExecution(await this.sendControlRequest<unknown>(
      `/runs/${encodeURIComponent(runID)}/wake-intent/consume`, body, signal,
    ), runID);
  }

  async listScheduledJobs(runID = "", limit = 50,
    signal?: AbortSignal, cursor = ""): Promise<ScheduledJobListView & { page: Page }> {
    if ((runID !== "" && boundedIdentity(runID) !== runID) ||
      !Number.isSafeInteger(limit) || limit < 1 || limit > 100) {
      throw new Error("A normalized Run identity and a schedule limit between 1 and 100 are required");
    }
    const envelope = await this.request<unknown>("/scheduled-jobs", {
      run_id: runID || undefined, limit, cursor: cursor || undefined,
    }, signal);
    return { ...parseScheduledJobList(envelope.data), page: parseResponsePage(envelope.page, limit) };
  }

  async getScheduledJob(jobID: string, signal?: AbortSignal): Promise<ScheduledJobDetailView> {
    if (boundedIdentity(jobID) !== jobID) {
      throw new Error("A normalized scheduled job identity is required");
    }
    return parseScheduledJobDetail(await this.get<unknown>(
      `/scheduled-jobs/${encodeURIComponent(jobID)}`,
      { round_limit: 20, notification_limit: 20 }, signal,
    ), jobID);
  }

  async createScheduledJob(runID: string, body: ScheduledJobCreateRequestView,
    idempotencyKey: string, signal?: AbortSignal): Promise<ScheduledJobControlView> {
    if (!this.hasScheduledJobControl || boundedIdentity(runID) !== runID ||
      body.version !== "scheduled-job.v1") {
      throw new Error("Scheduled job control and a normalized Run identity are required");
    }
    return parseScheduledJobControl(await this.sendControl<unknown>(
      `/runs/${encodeURIComponent(runID)}/scheduled-jobs`, body, idempotencyKey, signal,
    ), runID, "", "create");
  }

  async transitionScheduledJob(runID: string, jobID: string,
    action: "pause" | "resume" | "cancel", body: ScheduledJobTransitionRequestView,
    idempotencyKey: string, signal?: AbortSignal): Promise<ScheduledJobControlView> {
    if (!this.hasScheduledJobControl || boundedIdentity(runID) !== runID ||
      boundedIdentity(jobID) !== jobID || body.version !== "scheduled-job-control.v1" ||
      !Number.isSafeInteger(body.expected_revision) || body.expected_revision < 1) {
      throw new Error("Scheduled job control, normalized identities, and a revision are required");
    }
    return parseScheduledJobControl(await this.sendControl<unknown>(
      `/runs/${encodeURIComponent(runID)}/scheduled-jobs/${encodeURIComponent(jobID)}/${action}`,
      body, idempotencyKey, signal,
    ), runID, jobID, action);
  }

  async enableScheduledJobObservation(runID: string, jobID: string,
    body: ScheduledJobObservationRequestView, idempotencyKey: string,
    signal?: AbortSignal): Promise<ScheduledJobControlView> {
    if (!this.hasScheduledJobControl || boundedIdentity(runID) !== runID ||
      boundedIdentity(jobID) !== jobID || body.version !== "scheduled-job-control.v1" ||
      body.observation_consent_version !== 1 || !safePositiveInteger(body.expected_revision)) {
      throw new Error("Scheduled observation requires exact identities, a revision, and consent");
    }
    const result = parseScheduledJobControl(await this.sendControl<unknown>(
      `/runs/${encodeURIComponent(runID)}/scheduled-jobs/${encodeURIComponent(jobID)}/enable-observation`,
      body, idempotencyKey, signal,
    ), runID, jobID, "enable-observation");
    if (result.job.observation_consent_version !== 1) {
      throw new APIRequestError("Scheduled observation consent was not confirmed", "INVALID_RESPONSE", 502);
    }
    return result;
  }

  async diagnosticBundle(runID: string, signal?: AbortSignal): Promise<DiagnosticBundleView> {
    if (boundedIdentity(runID) !== runID) {
      throw new Error("A normalized Run identity is required for diagnostic export");
    }
    return parseDiagnosticBundle(await this.get<unknown>("/diagnostic-bundle", {
      run_id: runID, limit: 100,
    }, signal), runID);
  }

  async installSkillPackage(body: SkillPackageInstallRequestView,
    idempotencyKey: string, signal?: AbortSignal): Promise<SkillPackageInstallResult> {
    if (!this.hasSkillInstallation || body.confirm_untrusted !== true) {
      throw new Error("Explicit untrusted Skill installation capability is required");
    }
    return parseSkillPackageInstall(await this.sendControl<unknown>(
      "/skills/packages/install", body, idempotencyKey, signal,
    ), body);
  }

  async listSkillCatalogPublishers(signal?: AbortSignal): Promise<SkillCatalogPublishersView> {
    const value = await this.get<unknown>("/skills/catalog/publishers", {}, signal);
    if (!hasExactKeys(value, ["protocol_version", "publishers", "control_enabled"]) ||
      value.protocol_version !== "skill-catalog-control.v1" || typeof value.control_enabled !== "boolean" || !Array.isArray(value.publishers)) {
      throw new APIRequestError("Skill Catalog inventory is invalid", "INVALID_RESPONSE", 502);
    }
    const publishers = value.publishers.map(parseSkillCatalogPublisher);
    if (new Set(publishers.map((item) => item.fingerprint)).size !== publishers.length) {
      throw new APIRequestError("Skill Catalog identities are duplicated", "INVALID_RESPONSE", 502);
    }
    return { protocol_version: "skill-catalog-control.v1", publishers, control_enabled: value.control_enabled };
  }

  async controlSkillCatalogPublisher(body: SkillCatalogPublisherControlRequestView,
    signal?: AbortSignal): Promise<SkillCatalogPublisherView> {
    if (!this.hasSkillInstallation || body.version !== "skill-catalog-control.v1" || !body.confirm ||
      !isSHA256(body.fingerprint) || typeof body.expected_updated_at !== "string" ||
      (body.expected_updated_at !== "" && !validDate(body.expected_updated_at)) ||
      !["trust", "revoke"].includes(body.action) ||
      (body.action === "trust" && (!boundedText(body.name, 96) || !validCanonicalBase64(body.public_key, 32))) ||
      (body.action === "revoke" && (!body.expected_updated_at || body.name !== undefined || body.public_key !== undefined || body.team !== undefined))) {
      throw new Error("A confirmed Skill Catalog identity and observed version are required");
    }
    const publisher = parseSkillCatalogPublisher(await this.sendControlRequest<unknown>("/skills/catalog/publishers", body, signal));
    if (publisher.fingerprint !== body.fingerprint || publisher.trust_class !== (body.action === "trust" ? "trusted" : "revoked") ||
      publisher.updated_at === body.expected_updated_at || (body.action === "trust" && publisher.public_key !== body.public_key)) {
      throw new APIRequestError("Skill Catalog change is unverified; refresh before retrying", "INVALID_RESPONSE", 502);
    }
    return publisher;
  }

  async listSkillPackages(filters: { surface?: string; profile?: string;
    includeRemoved?: boolean } = {}, signal?: AbortSignal): Promise<SkillPackageInventoryView> {
    const query: Record<string, QueryValue> = {};
    if (filters.surface !== undefined) {
      const surface = boundedIdentity(filters.surface);
      if (!surface) throw new Error("A bounded Skill inventory surface is required");
      query.surface = surface;
    }
    if (filters.profile !== undefined) {
      const profile = boundedIdentity(filters.profile);
      if (!profile) throw new Error("A bounded Skill inventory Profile is required");
      query.profile = profile;
    }
    if (filters.includeRemoved !== undefined) {
      query.include_removed = filters.includeRemoved;
    }
    return parseSkillPackageInventory(await this.get<unknown>(
      "/skills/packages", query, signal,
    ));
  }

  async removeSkillPackage(body: SkillPackageRemoveRequestView,
    idempotencyKey: string, signal?: AbortSignal): Promise<SkillPackageRemovalView> {
    if (!this.hasSkillInstallation || body.confirm_remove !== true ||
      body.protocol_version !== "skill_package_removal_request.v1") {
      throw new Error("Explicit Skill package removal confirmation is required");
    }
    return parseSkillPackageRemoval(await this.sendControl<unknown>(
      "/skills/packages/remove", body, idempotencyKey, signal,
    ), body);
  }

  async executeEmbeddedAnalyzer(runID: string,
    body: EmbeddedAnalyzerExecutionRequestView, signal?: AbortSignal,
  ): Promise<EmbeddedAnalyzerExecutionControlView> {
    if (!this.hasEmbeddedAnalyzerExecution) {
      throw new Error("Embedded analyzer execution capability is required");
    }
    const normalizedRunID = boundedIdentity(runID);
    if (!normalizedRunID || normalizedRunID !== runID ||
      body.version !== "embedded_analyzer_operator_request.v1" ||
      body.confirmation !== "RUN-EMBEDDED-ANALYZER" ||
      ((body.text ?? "") === "") === ((body.file ?? "").trim() === "")) {
      throw new Error("A normalized Run and one explicitly confirmed analyzer input are required");
    }
    const result = await this.sendControlRequest<unknown>(
      `/runs/${encodeURIComponent(runID)}/analyzer-executions`, body, signal,
    );
    return parseEmbeddedAnalyzerExecution(result, runID);
  }

  async get<T>(path: string, query: Record<string, QueryValue> = {}, signal?: AbortSignal): Promise<T> {
    const envelope = await this.request<T>(path, query, signal);
    return envelope.data;
  }

  async getPage<T>(
    path: string,
    query: Record<string, QueryValue> = {},
    cursor = "",
    signal?: AbortSignal,
  ): Promise<PageResult<T>> {
    const envelope = await this.request<T[]>(path, { ...query, cursor: cursor || undefined }, signal);
    if (!envelope.page || !Array.isArray(envelope.data)) {
      throw new APIRequestError("API collection response omitted pagination metadata", "INVALID_RESPONSE", 502,
        envelope.request_id);
    }
    const items = /^\/threads\/[^/]+\/transcript$/u.test(path)
      ? parseThreadTranscriptItems(envelope.data as unknown[]) as T[]
      : envelope.data;
    return { items, page: envelope.page, requestID: envelope.request_id };
  }

  async respondMCPInput(runID: string, inputID: string,
    body: { version: "mcp-input.v1"; action: "accept" | "decline" | "cancel"; content?: Record<string, unknown> }): Promise<{ accepted: boolean }> {
    if (!this.hasExtensionControl || !boundedIdentity(runID) || !boundedIdentity(inputID) ||
      body.version !== "mcp-input.v1" || !["accept", "decline", "cancel"].includes(body.action)) {
      throw new Error("An active MCP request and extension control capability are required");
    }
    // The live broker consumes an answer once. It is not a durable command and
    // must not carry a replay key or be retried automatically.
    return this.sendControlRequest(`/runs/${encodeURIComponent(runID)}/mcp-inputs/${encodeURIComponent(inputID)}`, body);
  }

  async postControl<T>(
    path: string,
    body: unknown,
    idempotencyKey: string,
    signal?: AbortSignal,
  ): Promise<T> {
    if (!this.hasControl) {
      throw new Error("A control bearer token is required for this operation");
    }
    return this.sendControl<T>(path, body, idempotencyKey, signal);
  }

  async patchControl<T>(path: string, body: unknown, signal?: AbortSignal): Promise<T> {
    if (!this.hasControl) {
      throw new Error("A control bearer token is required for this operation");
    }
    return this.sendControlRequest<T>(path, body, signal, "", "PATCH");
  }

  async deleteControl<T>(path: string, body: unknown, signal?: AbortSignal): Promise<T> {
    if (!this.hasControl) {
      throw new Error("A control bearer token is required for this operation");
    }
    return this.sendControlRequest<T>(path, body, signal, "", "DELETE");
  }

  async listProjectActions(runID: string, target: string | AbortSignal = ".", signal?: AbortSignal): Promise<import("./types").ProjectActionsView> {
    const directory = typeof target === "string" ? target || "." : ".";
    const requestSignal = typeof target === "string" ? signal : target;
    if (!boundedIdentity(runID) || !validPreparationTarget(directory)) throw new Error("An exact Run and relative project directory are required");
    const normalizedTarget = normalizePreparationTarget(directory);
    return parseProjectActions(await this.get<unknown>(
      "/runs/" + encodeURIComponent(runID) + "/project-actions", normalizedTarget === "." ? {} : { target: normalizedTarget }, requestSignal), runID, normalizedTarget);
  }

  async getProjectPreparation(runID: string, target = ".", signal?: AbortSignal): Promise<ProjectPreparationView> {
    if (!boundedIdentity(runID) || !validPreparationTarget(target)) throw new Error("An exact Run and relative project directory are required");
    const normalizedTarget = normalizePreparationTarget(target);
    return parseProjectPreparation(await this.get<unknown>(
      "/runs/" + encodeURIComponent(runID) + "/project-preparation", { target: normalizedTarget }, signal), runID, normalizedTarget);
  }

  async prepareProject(runID: string, body: ProjectPreparationRequest, operationKey: string): Promise<ProjectPreparationView> {
    if (!this.hasControl || !this.hasRunExecution) throw new Error("Project preparation requires execution control");
    if (!boundedIdentity(runID) || !boundedIdentity(operationKey) || !validProjectPreparationRequest(body))
      throw new Error("An exact reviewed project preparation request is required");
    const result = parseProjectPreparation(await this.sendControlRequest<unknown>(
      "/runs/" + encodeURIComponent(runID) + "/project-preparation", body, undefined, operationKey), runID, body.target);
    if (!result.latest) throw new APIRequestError("项目准备响应没有执行记录。", "INVALID_RESPONSE", 502);
    return result;
  }

  async getProjectPreparationOperation(runID: string, operationID: string, signal?: AbortSignal): Promise<ProjectPreparationOperation> {
    if (!boundedIdentity(runID) || !boundedIdentity(operationID)) throw new Error("An exact Run and preparation operation are required");
    return parseProjectPreparationOperation(await this.get<unknown>(
      "/runs/" + encodeURIComponent(runID) + "/project-preparation/operations/" + encodeURIComponent(operationID), {}, signal), runID, operationID);
  }

  async cancelProjectPreparation(runID: string, operationID: string, operationKey: string): Promise<ProjectPreparationOperation> {
    if (!this.hasControl || !this.hasRunExecution) throw new Error("Project preparation requires execution control");
    if (!boundedIdentity(runID) || !boundedIdentity(operationID) || !boundedIdentity(operationKey))
      throw new Error("An exact Run and preparation operation are required");
    return parseProjectPreparationOperation(await this.sendControlRequest<unknown>(
      "/runs/" + encodeURIComponent(runID) + "/project-preparation/operations/" + encodeURIComponent(operationID) + "/cancel",
      { version: "project-preparation.v1" }, undefined, operationKey), runID, operationID);
  }

  async runProjectAction(runID: string, slot: ProjectActionSlot, target: string, operationKey: string): Promise<import("./types").ProjectActionResultView> {
    if (!this.hasControl || !this.hasRunExecution) throw new Error("Project actions require execution control");
    if (!boundedIdentity(runID) || !boundedIdentity(operationKey) || !["test", "format"].includes(slot) ||
      target.length > 4096 || /[\u0000-\u001f\u007f]/u.test(target)) throw new Error("Invalid project action");
    return parseProjectActionResult(await this.sendControlRequest<unknown>(
      "/runs/" + encodeURIComponent(runID) + "/project-actions/" + slot,
      { version: "project-actions.v1", target, confirm_execution: true }, undefined, operationKey), runID, slot);
  }

  async getProjectActionJob(runID: string, jobID: string, signal?: AbortSignal): Promise<import("./types").ProjectActionResultView> {
    if (!boundedIdentity(runID) || !boundedIdentity(jobID)) throw new Error("An exact Run and Job are required");
    return parseProjectActionResult(await this.get<unknown>(
      "/runs/" + encodeURIComponent(runID) + "/project-actions/jobs/" + encodeURIComponent(jobID), {}, signal), runID, undefined, jobID);
  }

  async listTaskSkillCandidates(scope: TaskSkillCandidateScope,
    signal?: AbortSignal): Promise<import("./types").TaskSkillCandidatesView> {
    if (!boundedIdentity(scope.workspace_id) || scope.workspace_id.trim() !== scope.workspace_id ||
      !["code", "cyber"].includes(scope.surface) || !["plan", "deliver"].includes(scope.phase) ||
      !["code", "learn", "review", "script"].includes(scope.profile)) {
      throw new Error("An exact workspace and task mode are required");
    }
    return parseTaskSkillCandidates(await this.get<unknown>("/task-configuration/skills", scope, signal), scope);
  }

  async previewTaskConfiguration(body: import("./types").TaskConfigurationRequest,
    signal?: AbortSignal): Promise<import("./types").TaskConfigurationView> {
    if (!boundedIdentity(body.workspace_id) || body.workspace_id.trim() !== body.workspace_id ||
      (body.profile !== undefined && !["code", "learn", "review", "script"].includes(body.profile))) throw new Error("A normalized workspace and profile are required");
    normalizedTaskBudget(body.budget);
    return parseTaskConfiguration(await this.sendReadRequest<unknown>("/task-configuration/preview", body, signal), body.workspace_id, body.profile ?? "code");
  }

  async getRunTaskConfiguration(runID: string, signal?: AbortSignal): Promise<import("./types").TaskConfigurationView> {
    if (!boundedIdentity(runID) || runID.trim() !== runID) throw new Error("A normalized Run identity is required");
    return parseTaskConfiguration(await this.get<unknown>(`/runs/${encodeURIComponent(runID)}/task-configuration`, {}, signal), undefined, undefined, "snapshot");
  }

  async selectProjectSkills(runID: string, choice: import("./project-skills").ProjectSkillChoice, idempotencyKey: string, signal?: AbortSignal): Promise<void> {
    if (!this.hasThreadControl) throw new Error("Exact project Skill references and task control are required");
    assertSkillSelectionChoice(runID, choice, "project");
    const value = await this.sendControl<unknown>(`/runs/${encodeURIComponent(runID)}/project-skills/select`,
      { refs: choice.refs, confirm_untrusted_context: choice.confirmUntrustedContext }, idempotencyKey, signal);
    assertSkillSelectionReceipt(value, runID, choice.refs, "Project");
  }

  // selectInstalledSkills names already installed Skills for one exact Run without
  // the pinned-project admission; every other server-side rule is the same.
  async selectInstalledSkills(runID: string, choice: import("./project-skills").ProjectSkillChoice, idempotencyKey: string, signal?: AbortSignal): Promise<void> {
    if (!this.hasThreadControl) throw new Error("Exact installed Skill references and task control are required");
    assertSkillSelectionChoice(runID, choice, "installed");
    const value = await this.sendControl<unknown>(`/runs/${encodeURIComponent(runID)}/external-skills/select`,
      { refs: choice.refs, confirm_untrusted_context: choice.confirmUntrustedContext }, idempotencyKey, signal);
    assertSkillSelectionReceipt(value, runID, choice.refs, "Installed");
  }

  async createRun(body: RunCreationControlRequestView, idempotencyKey: string,
    signal?: AbortSignal): Promise<RunCreationControlView> {
    if (!this.hasRunCreation) {
      throw new Error("Run creation capability is required for this operation");
    }
    normalizedTaskBudget(body.budget);
    normalizeRequestedNetworkAuthority(body);
    normalizeRequestedWebAccess(body);
    const result = await this.sendControl<unknown>("/runs", body, idempotencyKey, signal);
    return parseRunCreationControl(result, body);
  }

  async createThread(body: ThreadCreationControlRequestView, idempotencyKey: string,
    signal?: AbortSignal): Promise<ThreadCreationControlView> {
    const routePairPresent = body.provider !== undefined && body.model !== undefined;
    const routePairAbsent = body.provider === undefined && body.model === undefined;
    if (!this.hasThreadControl || !this.hasRunCreation || !this.hasSessionMessages ||
      body.version !== "thread_creation.v1" || (!routePairPresent && !routePairAbsent) ||
      (routePairPresent && (!boundedIdentity(body.provider) || !boundedIdentity(body.model) ||
        body.provider!.includes("/") || body.model!.includes("/")))) {
      throw new Error("Thread creation capability and a normalized Provider/model pair are required");
    }
    normalizedTaskBudget(body.budget);
    normalizeRequestedNetworkAuthority(body);
    normalizeRequestedWebAccess(body);
    return parseThreadCreationControl(await this.sendControl<unknown>("/threads", body,
      idempotencyKey, signal), body);
  }

  async submitThreadMessage(threadID: string, body: ThreadMessageControlRequestView,
    idempotencyKey: string, signal?: AbortSignal): Promise<ThreadMessageControlView> {
    if (!this.hasThreadControl || !this.hasSessionMessages || !boundedIdentity(threadID) ||
      threadID.trim() !== threadID || body.version !== "thread_message_submission.v1") {
      throw new Error("Thread message capability and a normalized Thread are required");
    }
    return parseThreadMessageControl(await this.sendControl<unknown>(
      `/threads/${encodeURIComponent(threadID)}/messages`, body, idempotencyKey, signal,
    ), threadID);
  }

  async threadActivityDetail(threadID: string, activityRef: string,
    signal?: AbortSignal): Promise<ThreadActivityDetailView> {
    if (boundedIdentity(threadID) !== threadID || boundedIdentity(activityRef) !== activityRef) {
      throw new Error("Normalized Thread and activity identities are required");
    }
    return parseThreadActivityDetail(await this.get<unknown>(
      `/threads/${encodeURIComponent(threadID)}/activities/${encodeURIComponent(activityRef)}`,
      {}, signal), threadID, activityRef);
  }

  async threadActivityArtifact(threadID: string, activityRef: string, artifactRef: string,
    signal?: AbortSignal): Promise<ThreadActivityArtifactView> {
    if (boundedIdentity(threadID) !== threadID || boundedIdentity(activityRef) !== activityRef ||
      boundedIdentity(artifactRef) !== artifactRef) {
      throw new Error("Normalized Thread activity artifact identities are required");
    }
    const value = parseThreadActivityArtifact(await this.get<unknown>(
      `/threads/${encodeURIComponent(threadID)}/activities/${encodeURIComponent(activityRef)}` +
      `/artifacts/${encodeURIComponent(artifactRef)}`, {}, signal), activityRef, artifactRef);
    const digest = new Uint8Array(await globalThis.crypto.subtle.digest(
      "SHA-256", new TextEncoder().encode(value.content)));
    const digestHex = [...digest].map((byte) => byte.toString(16).padStart(2, "0")).join("");
    if (digestHex !== value.sha256) {
      throw new APIRequestError("Thread activity artifact digest verification failed",
        "INVALID_RESPONSE", 502);
    }
    return value;
  }

  async setRunWebAccess(runID: string,
    body: RunWebAccessControlRequestView, idempotencyKey: string,
    signal?: AbortSignal): Promise<RunWebAccessControlView> {
    if (!this.hasControl || !boundedIdentity(runID) || runID.trim() !== runID ||
      body.version !== "run_web_access_control.v1" ||
      !safePositiveInteger(body.expected_mode_revision) ||
      (body.reason !== undefined && body.reason !== "" && (!boundedText(body.reason, 1_024) ||
        /[\u0000-\u001f\u007f]/u.test(body.reason)))) {
      throw new Error("请选择当前任务的网页访问方式后重试。");
    }
    const selection = normalizeRequestedWebAccess(body);
    if (selection === null) throw new Error("请选择网页访问方式。");
    const request = { ...body, web_access_mode: selection.mode, web_allowed_targets: selection.targets };
    return parseRunWebAccessControl(await this.sendControl<unknown>(
      `/runs/${encodeURIComponent(runID)}/web-access`, request, idempotencyKey, signal), runID, request);
  }

  async expandRunNetworkAuthority(runID: string,
    body: RunNetworkAuthorityControlRequestView, idempotencyKey: string,
    signal?: AbortSignal): Promise<RunNetworkAuthorityControlView> {
    if (!this.hasControl || !boundedIdentity(runID) || runID.trim() !== runID ||
      body.version !== "run_network_authority_control.v1" ||
      !safePositiveInteger(body.expected_mode_revision) ||
      (body.reason !== undefined && body.reason !== "" && (!boundedText(body.reason, 1_024) ||
        /[\u0000-\u001f\u007f]/u.test(body.reason)))) {
      throw new Error("A revision-bound exact Run network authority request is required");
    }
    normalizeRequestedNetworkAuthority({ network_mode: "allowlist",
      allowed_targets: body.add_allowed_targets });
    return parseRunNetworkAuthorityControl(await this.sendControl<unknown>(
      `/runs/${encodeURIComponent(runID)}/network-authority`, body,
      idempotencyKey, signal), runID, body);
  }

  async threadExecution(threadID: string, signal?: AbortSignal): Promise<import("./types").ThreadExecutionView> {
    return parseThreadExecution(await this.get<unknown>(
      `/threads/${encodeURIComponent(threadID)}/execution`, {}, signal), threadID);
  }

  async threadChanges(threadID: string, runID: string, sourceEventID: string,
    signal?: AbortSignal): Promise<ThreadChangeSummary> {
    const value = await this.get<ThreadChangeSummary>(`/threads/${encodeURIComponent(threadID)}/changes`,
      { source_event_id: sourceEventID }, signal);
    if (value.version !== "thread_change_summary.v1" || value.thread_id !== threadID ||
      value.run_id !== runID || value.source_event_id !== sourceEventID) {
      throw new Error("The change summary does not match this reply.");
    }
    return value;
  }

  async undoThreadChanges(threadID: string, runID: string, sourceEventID: string,
    confirm: boolean, signal?: AbortSignal): Promise<ThreadChangeUndo> {
    if (!this.hasWorkspaceCheckpointControl || !boundedIdentity(threadID) ||
      !boundedIdentity(runID) || !boundedIdentity(sourceEventID)) {
      throw new Error("Workspace restore authority and an exact reply are required.");
    }
    const value = await this.sendControl<ThreadChangeUndo>(`/threads/${encodeURIComponent(threadID)}/changes/undo`,
      { version: "thread_change_undo.v1", source_event_id: sourceEventID, confirm },
      `thread-change-undo-${sourceEventID}`, signal);
    if (value.version !== "thread_change_undo.v1" || value.thread_id !== threadID ||
      value.run_id !== runID || value.source_event_id !== sourceEventID ||
      !["available", "unavailable", "conflict", "applying", "completed", "partial"].includes(value.state) ||
      !Array.isArray(value.conflicts) || !Array.isArray(value.restored_paths) || !Array.isArray(value.remaining_paths)) {
      throw new Error("The undo result does not match this reply.");
    }
    return value;
  }

  async inspectThreadCreationRequest(workspaceID: string, operationKey: string,
    signal?: AbortSignal): Promise<unknown> {
    if (!boundedIdentity(workspaceID) || workspaceID.trim() !== workspaceID) {
      throw new Error("A normalized workspace is required");
    }
    return (await this.request<unknown>("/threads/creation-request", { workspace_id: workspaceID },
      signal, operationKey)).data;
  }

  async inspectThreadTurnRequest(threadID: string, operationKey: string,
    signal?: AbortSignal): Promise<unknown> {
    if (!boundedIdentity(threadID) || threadID.trim() !== threadID) {
      throw new Error("A normalized Thread is required");
    }
    return (await this.request<unknown>(`/threads/${encodeURIComponent(threadID)}/turn-request`, {},
      signal, operationKey)).data;
  }

  async inspectThreadPlanRequest(threadID: string, runID: string, action: "enter_plan" | "enter_deliver" | "confirm",
    operationKey: string, signal?: AbortSignal): Promise<unknown> {
    if (!boundedIdentity(threadID) || threadID.trim() !== threadID ||
      !boundedIdentity(runID) || runID.trim() !== runID) throw new Error("A normalized Thread and Run are required");
    return (await this.request<unknown>(`/threads/${encodeURIComponent(threadID)}/plan`,
      { run_id: runID, action }, signal, operationKey)).data;
  }

  async createFileEditRevertProposal(runID: string, sourceEditID: string,
    idempotencyKey: string, signal?: AbortSignal): Promise<FileEditRevertProposalView> {
    if (!this.hasFileEditReview || !boundedIdentity(runID) || !boundedIdentity(sourceEditID) ||
      runID.trim() !== runID || sourceEditID.trim() !== sourceEditID) {
      throw new Error("File edit review authority and normalized source identities are required");
    }
    return parseFileEditRevertProposal(await this.sendControl<unknown>(
      `/runs/${encodeURIComponent(runID)}/file-edits/${encodeURIComponent(sourceEditID)}/revert-proposal`,
      { version: "file_edit_proposal.v1" }, idempotencyKey, signal,
    ), runID, sourceEditID);
  }

  async interruptThread(threadID: string, executionID: string, idempotencyKey: string,
    signal?: AbortSignal): Promise<import("./types").ThreadExecutionView> {
    if (!this.hasThreadControl || !this.hasRunExecution || !boundedIdentity(threadID) ||
      !boundedIdentity(executionID)) throw new Error("A current controllable Thread execution is required");
    return parseThreadExecution(await this.sendControl<unknown>(
      `/threads/${encodeURIComponent(threadID)}/interrupt`, {
        version: "thread_execution.v1", execution_id: executionID,
      }, idempotencyKey, signal), threadID);
  }

  async submitThreadTurn(threadID: string, body: ThreadTurnControlRequestView,
    idempotencyKey: string, signal?: AbortSignal): Promise<ThreadMessageControlView> {
    if (!this.hasThreadControl || !this.hasSessionMessages || !boundedIdentity(threadID) ||
      threadID.trim() !== threadID || body.version !== "thread_message_submission.v1") {
      throw new Error("Thread turn capability and a normalized Thread are required");
    }
    if (body.files !== undefined && (!this.hasEvidenceAttachment || !Array.isArray(body.files) ||
      body.files.length > 4 || new Set(body.files.map((file) => file?.path)).size !== body.files.length ||
      body.files.some((file) => !hasExactKeys(file, ["source_kind", "path", "expected_sha256"]) ||
        file.source_kind !== "workspace_file" || file.path === "." || !validWorkspaceRelativePath(file.path) ||
        !isSHA256(file.expected_sha256)))) {
      throw new Error("最多引用 4 个不同的项目文件；请重新选择有效文件后重试。");
    }
    if (body.attachments !== undefined && (!Array.isArray(body.attachments) || body.attachments.length > 4 ||
        new Set(body.attachments.map((file) => file?.id)).size !== body.attachments.length ||
        body.attachments.some((file) => !hasExactKeys(file, ["id", "workspace_id", "sha256", "byte_size"]) ||
          !boundedIdentity(file.id) || !boundedIdentity(file.workspace_id) || !isSHA256(file.sha256) ||
          !Number.isSafeInteger(file.byte_size) || file.byte_size < 0 || file.byte_size > maximumFileBytes))) {
      throw new Error("文件附件身份无效，请重新核对已保存的文件。");
    }
    try {
      return parseThreadMessageControl(await this.sendControl<unknown>(
        `/threads/${encodeURIComponent(threadID)}/turns`, body, idempotencyKey, signal,
      ), threadID, true);
    } catch (error) {
      if (error instanceof APIRequestError && error.turnFailure && error.turnFailure.thread_id !== threadID) {
        throw new APIRequestError("Thread failure response belongs to another conversation", "INVALID_RESPONSE", 502);
      }
      throw error;
    }
  }

  async recoverThreadRun(threadID: string, body: ThreadRunRecoveryControlRequestView,
    idempotencyKey: string, signal?: AbortSignal): Promise<ThreadRunRecoveryControlView> {
    if (!this.hasThreadControl || !this.hasSessionMessages || !boundedIdentity(threadID) ||
      threadID.trim() !== threadID || body.version !== "thread_run_recovery.v1" ||
      !boundedIdentity(body.run_id) || !boundedIdentity(body.handoff_operation_id)) {
      throw new Error("Thread recovery capability and exact failed boundary identities are required");
    }
    return parseThreadRunRecoveryControl(await this.sendControl<unknown>(
      `/threads/${encodeURIComponent(threadID)}/recovery`, body, idempotencyKey, signal,
    ), threadID, body);
  }

  async getThreadExecutionPermission(threadID: string,
    signal?: AbortSignal): Promise<ThreadExecutionPermissionControlView> {
    if (!boundedIdentity(threadID) || threadID.trim() !== threadID) {
      throw new Error("A normalized Thread is required");
    }
    return parseThreadApprovalPreference(await this.get<unknown>(
      `/threads/${encodeURIComponent(threadID)}/execution-permission`, {}, signal), threadID);
  }

  async changeThreadExecutionPermission(threadID: string,
    body: ThreadExecutionPermissionControlRequestView, idempotencyKey: string,
    signal?: AbortSignal): Promise<ThreadExecutionPermissionControlView> {
    if (!this.hasExecutionPermissionControl || !boundedIdentity(threadID) ||
      threadID.trim() !== threadID) {
      throw new Error("Thread execution permission capability is required");
    }
    if (!["ask", "auto", "full"].includes(body.mode) ||
      body.confirm_full !== (body.mode === "full") ||
      Object.keys(body).some((key) => !["mode", "confirm_full", "reason"].includes(key))) {
      throw new Error("An exact ask/auto/full selection and explicit Full confirmation are required");
    }
    return parseThreadApprovalPreference(await this.sendControl<unknown>(
      `/threads/${encodeURIComponent(threadID)}/execution-permission`, body,
      idempotencyKey, signal), threadID);
  }

  async moveThreadToProject(threadID: string, projectWorkspaceID: string, expectedVersion: number,
    idempotencyKey: string, signal?: AbortSignal): Promise<{ thread: ThreadView; replayed: boolean }> {
    if (!this.hasThreadControl || !boundedIdentity(threadID) ||
      (projectWorkspaceID !== "" && !boundedIdentity(projectWorkspaceID)) || !safePositiveInteger(expectedVersion)) {
      throw new Error("A conversation, project and current project assignment version are required");
    }
    const result = await this.sendControl<unknown>(`/threads/${encodeURIComponent(threadID)}/project`, {
      version: "thread_project_move.v1", project_workspace_id: projectWorkspaceID,
      expected_version: expectedVersion,
    }, idempotencyKey, signal);
    if (!hasExactKeys(result, ["version", "thread", "replayed"]) ||
      result.version !== "thread_project_move.v1" || typeof result.replayed !== "boolean") {
      throw new APIRequestError("Project move response is invalid", "INVALID_RESPONSE", 502);
    }
    const thread = parseThreadView(result.thread, threadID);
    if ((!result.replayed && thread.project_workspace_id !== projectWorkspaceID) ||
      !safePositiveInteger(thread.project_assignment_version) || thread.project_assignment_version <= expectedVersion) {
      throw new APIRequestError("Project move response does not match the requested destination", "INVALID_RESPONSE", 502);
    }
    return { thread, replayed: result.replayed };
  }

  async transitionThread(threadID: string, action: "archive" | "restore" | "delete",
    body: ThreadLifecycleControlRequestView, idempotencyKey: string,
    signal?: AbortSignal): Promise<ThreadLifecycleControlView> {
    if (!this.hasThreadControl || !this.hasSessionMessages ||
      !boundedIdentity(threadID) || threadID.trim() !== threadID ||
      body.version !== "thread_lifecycle.v1" || !Number.isSafeInteger(body.expected_version) ||
      body.expected_version < 1) {
      throw new Error("Thread lifecycle capability and a current Thread version are required");
    }
    return parseThreadLifecycleControl(await this.sendControl<unknown>(
      `/threads/${encodeURIComponent(threadID)}/${action}`, body, idempotencyKey, signal,
    ), threadID, action, body.expected_version);
  }

  async submitSessionMessage(sessionID: string, body: SessionMessageControlRequestView,
    idempotencyKey: string, signal?: AbortSignal): Promise<SessionMessageControlView> {
    if (!this.hasSessionMessages) {
      throw new Error("Run messaging capability is required for this operation");
    }
    const normalizedSessionID = boundedIdentity(sessionID);
    if (!normalizedSessionID || normalizedSessionID !== sessionID) {
      throw new Error("A normalized Run-local Session identity is required");
    }
    const result = await this.sendControl<unknown>(
      `/sessions/${encodeURIComponent(sessionID)}/messages`, body, idempotencyKey, signal,
    );
    return parseSessionMessageControl(result, sessionID);
  }

  async inspectSessionMessageOperation(sessionID: string, operationKey: string,
    signal?: AbortSignal): Promise<unknown> {
    if (!this.hasSessionMessages || !boundedIdentity(sessionID) || sessionID.trim() !== sessionID ||
      operationKey.length < 16 || operationKey.length > 256 || /[\s\u0000-\u001f\u007f]/u.test(operationKey)) {
      throw new Error("A Run-local Session and normalized original operation key are required");
    }
    return (await this.request<unknown>(
      `/sessions/${encodeURIComponent(sessionID)}/messages/operations/${encodeURIComponent(operationKey)}`,
      {}, signal)).data;
  }

  async archiveSession(sessionID: string, body: SessionArchiveControlRequestView,
    signal?: AbortSignal): Promise<SessionArchiveControlView> {
    if (!this.hasSessionMessages || !boundedIdentity(sessionID) || sessionID.trim() !== sessionID ||
      body.version !== "session_archive.v1" || body.confirm !== true) {
      throw new Error("Confirmed Run-local Session archive capability and identity are required");
    }
    return parseSessionArchiveControl(await this.sendControlRequest<unknown>(
      `/sessions/${encodeURIComponent(sessionID)}/archive`, body, signal,
    ), sessionID);
  }

  async cancelSessionSteering(sessionID: string, messageID: string,
    body: SessionSteeringCancellationRequestView, idempotencyKey: string,
    signal?: AbortSignal): Promise<SessionSteeringCancellationView> {
    if (!this.hasSessionSteeringControl) {
      throw new Error("Run-local Session steering cancellation capability is required for this operation");
    }
    const normalizedSessionID = boundedIdentity(sessionID);
    const normalizedMessageID = boundedIdentity(messageID);
    if (!normalizedSessionID || normalizedSessionID !== sessionID ||
      !normalizedMessageID || normalizedMessageID !== messageID) {
      throw new Error("Normalized Run-local Session and steering identities are required");
    }
    const result = await this.sendControl<unknown>(
      `/sessions/${encodeURIComponent(sessionID)}/messages/${encodeURIComponent(messageID)}/cancel`,
      body, idempotencyKey, signal,
    );
    return parseSessionSteeringCancellation(result, sessionID, messageID);
  }

  async controlRunLifecycle(runID: string, body: RunLifecycleControlRequestView,
    idempotencyKey: string, signal?: AbortSignal): Promise<RunLifecycleControlView> {
    if (!this.hasRunLifecycle) {
      throw new Error("Run lifecycle capability is required for this operation");
    }
    const normalizedRunID = boundedIdentity(runID);
    if (!normalizedRunID || normalizedRunID !== runID) {
      throw new Error("A normalized Run identity is required");
    }
    const result = await this.sendControl<unknown>(
      `/runs/${encodeURIComponent(runID)}/lifecycle`, body, idempotencyKey, signal,
    );
    return parseRunLifecycleControl(result, runID, body);
  }

  async executeRun(runID: string, body: RunExecutionControlRequestView,
    idempotencyKey: string, signal?: AbortSignal): Promise<RunExecutionControlView> {
    if (!this.hasRunExecution) {
      throw new Error("Run execution capability is required for this operation");
    }
    const normalizedRunID = boundedIdentity(runID);
    if (!normalizedRunID || normalizedRunID !== runID) {
      throw new Error("A normalized Run identity is required");
    }
    const result = await this.sendControl<unknown>(
      `/runs/${encodeURIComponent(runID)}/execute`, body, idempotencyKey, signal,
    );
    return parseRunExecutionControl(result, runID, body);
  }

  async getPublicModelStream(runID: string,
    signal?: AbortSignal): Promise<PublicModelStreamSnapshot> {
    const normalizedRunID = boundedIdentity(runID);
    if (!normalizedRunID || normalizedRunID !== runID) {
      throw new Error("A normalized Run identity is required");
    }
    const result = await this.get<unknown>(
      `/runs/${encodeURIComponent(runID)}/active-call`, {}, signal,
    );
    return parsePublicModelStream(result, runID);
  }

  async pollPublicModelStream(runID: string,
    signal?: AbortSignal): Promise<PublicModelStreamSnapshot | null> {
    const normalizedRunID = boundedIdentity(runID);
    if (!normalizedRunID || normalizedRunID !== runID) {
      throw new Error("A normalized Run identity is required");
    }
    const result = await this.get<unknown>(
      `/runs/${encodeURIComponent(runID)}/active-call/poll`, {}, signal,
    );
    return parsePublicModelStreamPoll(result, runID);
  }

  async cancelModelCall(runID: string, body: ModelCancellationRequestView,
    idempotencyKey: string, signal?: AbortSignal): Promise<ModelCancellationView> {
    if (!this.hasControl) {
      throw new Error("A control bearer token is required for this operation");
    }
    const normalizedRunID = boundedIdentity(runID);
    if (!normalizedRunID || normalizedRunID !== runID) {
      throw new Error("A normalized Run identity is required");
    }
    if (!boundedIdentity(body.attempt_id) || !safePositiveInteger(body.model_attempt)) {
      throw new Error("A bound attempt identity and model attempt are required");
    }
    const result = await this.sendControl<unknown>(
      `/runs/${encodeURIComponent(runID)}/active-call/cancel`, body, idempotencyKey, signal,
    );
    return parseModelCancellation(result, runID, body);
  }

  async cancelSpecialistModelCall(runID: string, agentID: string,
    body: ModelCancellationRequestView, idempotencyKey: string,
    signal?: AbortSignal): Promise<SpecialistModelCancellationView> {
    if (!this.hasControl) {
      throw new Error("A control bearer token is required for this operation");
    }
    const normalizedRunID = boundedIdentity(runID);
    const normalizedAgentID = boundedIdentity(agentID);
    if (!normalizedRunID || normalizedRunID !== runID ||
      !normalizedAgentID || normalizedAgentID !== agentID) {
      throw new Error("Normalized Run and Agent identities are required");
    }
    if (!boundedIdentity(body.attempt_id) || !safePositiveInteger(body.model_attempt)) {
      throw new Error("A bound attempt identity and model attempt are required");
    }
    const result = await this.sendControl<unknown>(
      `/runs/${encodeURIComponent(runID)}/agents/${encodeURIComponent(agentID)}/active-call/cancel`,
      body, idempotencyKey, signal,
    );
    return parseSpecialistModelCancellation(result, runID, agentID, body);
  }

  async getRunFanoutExecutions(runID: string, planID: string,
    signal?: AbortSignal): Promise<FanoutExecutionsListView> {
    if (!boundedIdentity(runID) || !boundedIdentity(planID)) {
      throw new Error("Normalized Run and Fan-out plan identities are required");
    }
    const result = await this.get<unknown>(
      `/runs/${encodeURIComponent(runID)}/fanout-executions?plan_id=${encodeURIComponent(planID)}`,
      {}, signal,
    );
    return parseFanoutExecutions(result, runID, planID);
  }

  async cancelRunFanoutExecution(runID: string, executionID: string,
    body: FanoutExecutionCancelRequestView, idempotencyKey: string,
    signal?: AbortSignal): Promise<FanoutExecutionView> {
    if (!this.hasControl) {
      throw new Error("A control bearer token is required for this operation");
    }
    if (!boundedIdentity(runID) || !boundedIdentity(executionID) ||
      body.version !== "readonly_fanout_cancel.v1" || body.confirm_cancel !== true) {
      throw new Error("Normalized Run/execution identities and explicit confirmation are required");
    }
    const result = await this.sendControl<unknown>(
      `/runs/${encodeURIComponent(runID)}/fanout-executions/${encodeURIComponent(executionID)}/cancel`,
      body, idempotencyKey, signal,
    );
    return parseFanoutExecution(result);
  }
  get workflows(): WorkflowClient {
    return new WorkflowClient({
      get: (path, signal) => this.get<unknown>(path, {}, signal),
      post: (path, body, key, signal) => this.sendControl<unknown>(path, body, key, signal),
    });
  }

  async getRunChildTaskProposals(runID: string,
    signal?: AbortSignal): Promise<ChildTaskProposalsListView> {
    if (!boundedIdentity(runID)) {
      throw new Error("A normalized Run identity is required");
    }
    const result = await this.get<unknown>(
      `/runs/${encodeURIComponent(runID)}/child-task-proposals`, {}, signal,
    );
    return parseChildTaskProposals(result, runID);
  }

  async reviewRunChildTaskProposal(runID: string, proposalID: string,
    body: ChildTaskReviewRequestView, idempotencyKey: string,
    signal?: AbortSignal): Promise<ChildTaskProposalView> {
    if (!this.hasControl) {
      throw new Error("A control bearer token is required for this operation");
    }
    if (!boundedIdentity(runID) || !boundedIdentity(proposalID) ||
      body.version !== "child_task_review.v1" || body.confirm_review !== true ||
      (body.action !== "approve" && body.action !== "deny")) {
      throw new Error("A normalized proposal and explicit confirmed review are required");
    }
    const result = await this.sendControl<unknown>(
      `/runs/${encodeURIComponent(runID)}/child-task-proposals/${encodeURIComponent(proposalID)}/review`,
      body, idempotencyKey, signal,
    );
    return parseChildTaskProposal(result, runID);
  }

  async admitRunChildTaskProposal(runID: string, proposalID: string,
    body: ChildTaskAdmitRequestView, idempotencyKey: string,
    signal?: AbortSignal): Promise<ChildTaskProposalView> {
    if (!this.hasControl) {
      throw new Error("A control bearer token is required for this operation");
    }
    if (!boundedIdentity(runID) || !boundedIdentity(proposalID) ||
      body.version !== "child_task_admit.v1" || body.confirm_admit !== true) {
      throw new Error("A normalized proposal and explicit confirmed admission are required");
    }
    const result = await this.sendControl<unknown>(
      `/runs/${encodeURIComponent(runID)}/child-task-proposals/${encodeURIComponent(proposalID)}/admit`,
      body, idempotencyKey, signal,
    );
    return parseChildTaskProposal(result, runID);
  }

  async getRunBatchDeliveries(runID: string,
    signal?: AbortSignal): Promise<BatchDeliveriesListView> {
    if (!boundedIdentity(runID)) {
      throw new Error("A normalized Run identity is required");
    }
    return parseBatchDeliveries(await this.get<unknown>(
      `/runs/${encodeURIComponent(runID)}/batch-deliveries`, {}, signal,
    ), runID);
  }

  async getRunBatchDelivery(runID: string, planID: string,
    signal?: AbortSignal): Promise<BatchDeliverySnapshotView> {
    if (!boundedIdentity(runID) || !boundedIdentity(planID)) {
      throw new Error("Normalized Run and batch delivery identities are required");
    }
    return parseBatchDeliverySnapshot(await this.get<unknown>(
      `/runs/${encodeURIComponent(runID)}/batch-deliveries/${encodeURIComponent(planID)}`,
      {}, signal,
    ), runID, planID);
  }

  async getBatchWorkbench(runID: string, planID: string, signal?: AbortSignal): Promise<BatchWorkbenchView> {
    if (!boundedIdentity(runID) || !boundedIdentity(planID)) throw new Error("Normalized Run and batch identities are required");
    return parseBatchWorkbench(await this.get<unknown>(
      `/runs/${encodeURIComponent(runID)}/batch-deliveries/${encodeURIComponent(planID)}/workbench`, {}, signal), runID, planID);
  }

  async prepareBatchWorkbench(runID: string, body: BatchWorkbenchPrepareRequestView,
    idempotencyKey: string, signal?: AbortSignal): Promise<BatchWorkbenchView> {
    if (new TextEncoder().encode(idempotencyKey).byteLength > 224) throw new Error("Batch workbench operation key is too long");
    this.requireBatchDeliveryControl(runID, body.proposal_id);
    if (body.version !== "batch-delivery-workbench.v1" || !body.confirm || !Array.isArray(body.tasks) ||
      body.tasks.length < 1 || body.tasks.length > 2 || body.tasks.some((task, index) =>
        task.ordinal !== index + 1 || !Array.isArray(task.ownership_hints) || task.ownership_hints.length < 1 ||
        task.ownership_hints.length > 32 || task.ownership_hints.some((hint) => !isBatchPath(hint.path) ||
          (hint.kind !== "file" && hint.kind !== "directory")) || !Array.isArray(task.validations) ||
        task.validations.length < 1 || task.validations.length > 16 || !task.validations.some((test) => test.kind === "git_diff_check") ||
        task.validations.some((test) => !boundedIdentity(test.id) || !batchValidationKinds.includes(test.kind) || !isBatchPath(test.scope)))) {
      throw new Error("Confirmed ownership and validations for the admitted task set are required");
    }
    const value = await this.sendControl<unknown>(`/runs/${encodeURIComponent(runID)}/batch-deliveries/prepare-workbench`, body, idempotencyKey, signal);
    return parseBatchWorkbench(value, runID);
  }

  async recoverBatchWorkbenchOwner(runID: string, planID: string, ordinal: number,
    body: BatchWorkbenchOwnerRequestView, idempotencyKey: string, signal?: AbortSignal): Promise<BatchWorkbenchView> {
    if (new TextEncoder().encode(idempotencyKey).byteLength > 224) throw new Error("Batch workbench operation key is too long");
    this.requireBatchDeliveryControl(runID, planID, ordinal);
    if (body.version !== "batch-delivery-workbench.v1" || !body.confirm || !safePositiveInteger(body.expected_generation) ||
      body.expected_generation >= 8 || typeof body.retry !== "boolean") throw new Error("Confirmed current owner generation is required");
    return parseBatchWorkbench(await this.sendControl<unknown>(
      `/runs/${encodeURIComponent(runID)}/batch-deliveries/${encodeURIComponent(planID)}/children/${ordinal}/workbench-owner`,
      body, idempotencyKey, signal), runID, planID);
  }

  async executeBatchWorkbenchChild(runID: string, planID: string, ordinal: number,
    body: BatchWorkbenchExecuteRequestView, idempotencyKey: string, signal?: AbortSignal): Promise<BatchWorkbenchView> {
    if (new TextEncoder().encode(idempotencyKey).byteLength > 224) throw new Error("Batch workbench operation key is too long");
    this.requireBatchDeliveryControl(runID, planID, ordinal);
    if (body.version !== "batch-delivery-workbench.v1" || !body.confirm || !safePositiveInteger(body.expected_generation) ||
      body.expected_generation > 8) throw new Error("Confirmed current child generation is required");
    return parseBatchWorkbench(await this.sendControl<unknown>(
      `/runs/${encodeURIComponent(runID)}/batch-deliveries/${encodeURIComponent(planID)}/children/${ordinal}/workbench-execute`,
      body, idempotencyKey, signal), runID, planID);
  }

  async reviewRunBatchDeliveryChild(runID: string, planID: string, ordinal: number,
    body: BatchDeliveryReviewRequestView, idempotencyKey: string,
    signal?: AbortSignal): Promise<BatchDeliveryReviewControlView> {
    this.requireBatchDeliveryControl(runID, planID, ordinal);
    if (body.version !== "batch_delivery_review_control.v1" ||
      !safePositiveInteger(body.generation) || !boundedText(body.reviewer, 256) ||
      !boundedText(body.summary, 4096) ||
      (body.verdict !== "accepted" && body.verdict !== "changes_requested") ||
      !body.full_diff_reviewed || !body.call_chain_reviewed || !body.tests_reviewed) {
      throw new Error("A complete independent batch delivery review is required");
    }
    const value = await this.sendControl<unknown>(
      `/runs/${encodeURIComponent(runID)}/batch-deliveries/${encodeURIComponent(planID)}` +
        `/children/${ordinal}/review`, body, idempotencyKey, signal,
    );
    return parseBatchDeliveryReviewControl(value, planID, ordinal);
  }

  async mergeRunBatchDelivery(runID: string, planID: string,
    body: BatchDeliveryMergeRequestView, idempotencyKey: string,
    signal?: AbortSignal): Promise<BatchDeliveryMergeControlView> {
    this.requireBatchDeliveryControl(runID, planID);
    if (body.version !== "batch_delivery_merge.v1" || body.confirm !== true ||
      !Array.isArray(body.ordered_ordinals) || body.ordered_ordinals.length < 1 ||
      body.ordered_ordinals.length > 2 ||
      body.ordered_ordinals.some((ordinal) => !safePositiveInteger(ordinal))) {
      throw new Error("A bounded confirmed batch delivery merge order is required");
    }
    const value = await this.sendControl<unknown>(
      `/runs/${encodeURIComponent(runID)}/batch-deliveries/${encodeURIComponent(planID)}/merge`,
      body, idempotencyKey, signal,
    );
    return parseBatchDeliveryMergeControl(value, planID);
  }

  async cancelRunBatchDelivery(runID: string, planID: string,
    body: BatchDeliveryCancelRequestView, idempotencyKey: string,
    signal?: AbortSignal): Promise<BatchDeliveryCancelView> {
    this.requireBatchDeliveryControl(runID, planID);
    if (body.version !== "batch_delivery_cancel.v1" || body.confirm !== true ||
      !boundedText(body.reason, 4096)) {
      throw new Error("A bounded confirmed batch delivery cancellation is required");
    }
    const value = await this.sendControl<unknown>(
      `/runs/${encodeURIComponent(runID)}/batch-deliveries/${encodeURIComponent(planID)}/cancel`,
      body, idempotencyKey, signal,
    );
    return parseBatchDeliveryCancel(value, runID, planID);
  }

  async reconcileRunBatchDelivery(runID: string, planID: string,
    body: BatchDeliveryReconcileRequestView,
    signal?: AbortSignal): Promise<BatchDeliveryReconcileView> {
    this.requireBatchDeliveryControl(runID, planID);
    if (body.version !== "batch_delivery_reconcile.v1" || body.confirm !== true) {
      throw new Error("Explicit batch delivery reconciliation confirmation is required");
    }
    const value = await this.sendControlRequest<unknown>(
      `/runs/${encodeURIComponent(runID)}/batch-deliveries/${encodeURIComponent(planID)}/reconcile`,
      body, signal,
    );
    return parseBatchDeliveryReconcile(value, planID);
  }

  private requireBatchDeliveryControl(runID: string, planID: string, ordinal = 1): void {
    if (!this.hasBatchDeliveryControl) {
      throw new Error("Batch delivery control capability is required for this operation");
    }
    if (!boundedIdentity(runID) || !boundedIdentity(planID) ||
      !safePositiveInteger(ordinal) || ordinal > 2) {
      throw new Error("Normalized Run, batch delivery, and child identities are required");
    }
  }
  async listPriceSnapshots(signal?: AbortSignal): Promise<PriceSnapshotListView> {
    const result = await this.get<unknown>("/models/prices", {}, signal);
    return parsePriceSnapshots(result);
  }

  async importPriceSnapshot(body: PriceSnapshotImportRequestView, idempotencyKey: string,
    signal?: AbortSignal): Promise<PriceSnapshotImportView> {
    if (!this.hasControl) {
      throw new Error("A control bearer token is required for this operation");
    }
    if (body.version !== "price_snapshot.v1" || !body.document ||
      body.document.length > 64 * 1024) {
      throw new Error("A bounded price_snapshot.v1 document is required");
    }
    const result = await this.sendControl<unknown>("/models/prices", body, idempotencyKey, signal);
    return parsePriceSnapshotImport(result);
  }

  async getSandboxEnvironment(signal?: AbortSignal): Promise<SandboxEnvironmentView> {
    const value = await this.get<unknown>("/sandbox/environment", {}, signal);
    try { return parseSandboxEnvironment(value); }
    catch { throw new APIRequestError("Sandbox environment response is invalid", "INVALID_RESPONSE", 502); }
  }

  async saveSandboxEnvironment(body: SandboxEnvironmentControlRequestView, signal?: AbortSignal): Promise<SandboxEnvironmentView> {
    if (!this.hasControl || !validSandboxEnvironmentRequest(body)) throw new Error("Complete sandbox settings and their current revision are required");
    const value = await this.sendControlRequest<unknown>("/sandbox/environment", body, signal, "", "PUT");
    let result: SandboxEnvironmentView;
    try { result = parseSandboxEnvironment(value); }
    catch { throw new APIRequestError("Sandbox environment response is invalid", "INVALID_RESPONSE", 502); }
    if (!sandboxSettingsMatch(result.settings, body.settings) || result.revision !== body.expected_revision + 1) {
      throw new APIRequestError("Sandbox settings receipt changed the saved request", "INVALID_RESPONSE", 502);
    }
    return result;
  }

  async getDockerEnvironment(signal?: AbortSignal): Promise<DockerEnvironmentView> {
    const value = await this.get<unknown>("/sandbox/docker/environment", {}, signal);
    if (!hasRequiredOnlyKeys(value, ["protocol_version", "feature_enabled", "image_configured", "restart_required"],
      ["image_digest", "readiness"]) || value.protocol_version !== "docker_environment.v1" ||
      typeof value.feature_enabled !== "boolean" || typeof value.image_configured !== "boolean" ||
      typeof value.restart_required !== "boolean" ||
      (value.image_configured ? typeof value.image_digest !== "string" || !/^sha256:[a-f0-9]{64}$/u.test(value.image_digest) : value.image_digest !== undefined) ||
      (value.readiness !== undefined && (!hasRequiredOnlyKeys(value.readiness,
        ["protocol_version", "status", "ready", "feature_enabled", "reason_code", "remediation_code", "checked_at", "expires_at", "endpoint_class", "endpoint_fingerprint", "daemon_reachable", "image_inspected", "image_profile_safe", "readiness_fingerprint"], ["network_mode"]) ||
        value.readiness.protocol_version !== "sandbox.readiness.v1" ||
        !["ready", "disabled", "unavailable"].includes(String(value.readiness.status)) ||
        value.readiness.ready !== (value.readiness.status === "ready") ||
        value.readiness.feature_enabled !== value.feature_enabled ||
        !validDate(value.readiness.checked_at) || !validDate(value.readiness.expires_at) ||
        Date.parse(String(value.readiness.expires_at)) - Date.parse(String(value.readiness.checked_at)) !== 30_000 ||
        !["local_unix", "local_npipe"].includes(String(value.readiness.endpoint_class)) ||
        !isSHA256(value.readiness.endpoint_fingerprint) || !isSHA256(value.readiness.readiness_fingerprint) ||
        (value.readiness.network_mode !== undefined && value.readiness.network_mode !== "disabled") ||
        (value.readiness.ready && (!value.image_configured || value.readiness.daemon_reachable !== true || value.readiness.image_inspected !== true || value.readiness.image_profile_safe !== true || value.readiness.network_mode !== "disabled")) ||
        ["daemon_reachable", "image_inspected", "image_profile_safe"].some((key) => typeof (value.readiness as Record<string, unknown>)[key] !== "boolean") ||
        typeof value.readiness.reason_code !== "string" || typeof value.readiness.remediation_code !== "string"))) {
      throw new APIRequestError("Docker environment response is invalid", "INVALID_RESPONSE", 502);
    }
    return value as unknown as DockerEnvironmentView;
  }

  async getDockerSandboxStatus(admissionID: string,
    signal?: AbortSignal): Promise<DockerSandboxStatusView> {
    const result = await this.get<unknown>("/sandbox/docker/status", { admission_id: admissionID }, signal);
    if (!isRecord(result) || !boundedIdentity(String(result.admission_id))) {
      throw new APIRequestError("Docker sandbox status is invalid", "INVALID_RESPONSE", 502);
    }
    return result as unknown as DockerSandboxStatusView;
  }

  async admitDockerSandbox(body: DockerSandboxAdmissionRequestView, idempotencyKey: string,
    signal?: AbortSignal): Promise<DockerSandboxAdmissionView> {
    if (!this.hasControl) {
      throw new Error("A control bearer token is required for this operation");
    }
    if (!boundedIdentity(body.plan_id) || !boundedIdentity(body.requested_by) ||
      !isRecord(body.manifest)) {
      throw new Error("A plan identity, requester, and manifest object are required");
    }
    const result = await this.sendControl<unknown>("/sandbox/docker/admissions", body, idempotencyKey, signal);
    if (!isRecord(result) || typeof result.allowed !== "boolean") {
      throw new APIRequestError("Docker sandbox admission is invalid", "INVALID_RESPONSE", 502);
    }
    return result as unknown as DockerSandboxAdmissionView;
  }

  async startDockerSandbox(body: DockerSandboxStartRequestView, idempotencyKey: string,
    signal?: AbortSignal): Promise<DockerSandboxStatusView> {
    if (!this.hasControl) {
      throw new Error("A control bearer token is required for this operation");
    }
    if (!boundedIdentity(body.admission_id) || !boundedIdentity(body.requested_by)) {
      throw new Error("An admission identity and requester are required");
    }
    const result = await this.sendControl<unknown>("/sandbox/docker/starts", body, idempotencyKey, signal);
    return result as unknown as DockerSandboxStatusView;
  }

  async cancelDockerSandbox(body: DockerSandboxCancelRequestView, idempotencyKey: string,
    signal?: AbortSignal): Promise<DockerSandboxCancellationView> {
    if (!this.hasControl) {
      throw new Error("A control bearer token is required for this operation");
    }
    if (!boundedIdentity(body.admission_id) || !boundedIdentity(body.requested_by)) {
      throw new Error("An admission identity and requester are required");
    }
    const result = await this.sendControl<unknown>("/sandbox/docker/cancellations", body, idempotencyKey, signal);
    return result as unknown as DockerSandboxCancellationView;
  }
  async selectPlanDirection(runID: string, body: PlanDirectionControlRequestView,
    idempotencyKey: string, signal?: AbortSignal): Promise<PlanDirectionControlView> {
    if (!this.hasPlanDelivery) {
      throw new Error("Plan/Delivery control capability is required for this operation");
    }
    if (!boundedIdentity(runID) || runID.trim() !== runID || !safePositiveInteger(body.direction) ||
      body.direction > 3 || !boundedIdentity(body.proposal_id) || body.version !== "plan_delivery_control.v1" ||
      !hasRequiredOnlyKeys(body, ["version", "proposal_id", "direction"], ["manual_acceptance"]) ||
      (body.manual_acceptance !== undefined && body.manual_acceptance !== "required" && body.manual_acceptance !== "on_demand")) {
      throw new Error("A normalized Run, proposal, and direction are required");
    }
    const result = await this.sendControl<unknown>(
      `/runs/${encodeURIComponent(runID)}/plan/direction`, body, idempotencyKey, signal,
    );
    return parsePlanDirectionControl(result, runID, body);
  }

  async enterPlanMode(runID: string, body: PlanModeTransitionControlRequestView,
    idempotencyKey: string, signal?: AbortSignal): Promise<PlanModeTransitionControlView> {
    if (!this.hasPlanDelivery) {
      throw new Error("Plan/Delivery control capability is required for this operation");
    }
    if (!boundedIdentity(runID) || runID.trim() !== runID) {
      throw new Error("A normalized Run identity is required");
    }
    const result = await this.sendControl<unknown>(
      `/runs/${encodeURIComponent(runID)}/plan/enter`, body, idempotencyKey, signal,
    );
    return parsePlanModeTransition(result, runID);
  }

  async enterPlanDelivery(runID: string, body: PlanDeliveryTransitionControlRequestView,
    idempotencyKey: string, signal?: AbortSignal): Promise<PlanDeliveryTransitionControlView> {
    if (!this.hasPlanDelivery) {
      throw new Error("Plan/Delivery control capability is required for this operation");
    }
    if (!boundedIdentity(runID) || runID.trim() !== runID) {
      throw new Error("A normalized Run identity is required");
    }
    const result = await this.sendControl<unknown>(
      `/runs/${encodeURIComponent(runID)}/plan/deliver`, body, idempotencyKey, signal,
    );
    return parsePlanDeliveryTransition(result, runID);
  }

  async controlPlanDeliveryWorkItem(runID: string, workItemID: string, action: "start" | "complete",
    body: PlanDeliveryWorkItemControlRequestView, idempotencyKey: string,
    signal?: AbortSignal): Promise<PlanDeliveryWorkItemControlView> {
    validatePlanWorkItemRequest(this.hasPlanDelivery, runID, workItemID, body);
    if ((action !== "start" && action !== "complete") ||
      !hasExactKeys(body, ["version", "expected_work_item_version"])) {
      throw new Error("An exact Plan item transition is required");
    }
    const value = await this.sendControl<unknown>(
      `/runs/${encodeURIComponent(runID)}/plan/work-items/${encodeURIComponent(workItemID)}/${action}`,
      body, idempotencyKey, signal);
    if (!validPlanWorkItemResult(value, runID, workItemID) ||
      !hasExactKeys(value, ["version", "run_id", "work_item_id", "applied_status", "applied_version",
        "current_work_item", "replayed", "execution_started", "model_called", "tool_called", "capability_grant"]) ||
      value.work_item_id !== workItemID || value.applied_status !== (action === "start" ? "in_progress" : "completed") ||
      value.applied_version !== body.expected_work_item_version + 1 ||
      value.current_work_item.version < value.applied_version) {
      throw new APIRequestError("Plan item result does not match the submitted operation", "INVALID_RESPONSE", 502);
    }
    return value as unknown as PlanDeliveryWorkItemControlView;
  }

  async recordPlanDeliveryCheckpoint(runID: string, workItemID: string,
    body: PlanDeliveryCheckpointControlRequestView, idempotencyKey: string,
    signal?: AbortSignal): Promise<PlanDeliveryCheckpointControlView> {
    validatePlanWorkItemRequest(this.hasPlanDelivery, runID, workItemID, body);
    if (!hasRequiredOnlyKeys(body, ["version", "expected_work_item_version", "focused_verification",
      "diff_audit", "security_audit", "handoff_summary"], ["functional_verification", "robustness_audit"]) ||
      ![body.focused_verification, body.diff_audit, body.security_audit].every((text) => boundedText(text, 1_024)) ||
      !boundedText(body.handoff_summary, 2_048) ||
      [body.functional_verification, body.robustness_audit].some((text) => text !== undefined && !boundedText(text, 1_024))) {
      throw new Error("Bounded manual verification evidence and a handoff summary are required");
    }
    const value = await this.sendControl<unknown>(
      `/runs/${encodeURIComponent(runID)}/plan/work-items/${encodeURIComponent(workItemID)}/checkpoint`,
      body, idempotencyKey, signal);
    if (!validPlanWorkItemResult(value, runID, workItemID) ||
      !hasExactKeys(value, ["version", "run_id", "checkpoint", "note", "current_work_item", "replayed",
        "execution_started", "model_called", "tool_called", "capability_grant"]) ||
      !isRecord(value.checkpoint) || !boundedIdentity(value.checkpoint.id) ||
      value.checkpoint.work_item_id !== workItemID ||
      value.checkpoint.work_item_version !== body.expected_work_item_version ||
      typeof value.checkpoint.gate_ready !== "boolean" || typeof value.checkpoint.full_gate_required !== "boolean" ||
      !safePositiveInteger(value.checkpoint.mode_revision) || !safePositiveInteger(value.checkpoint.module_ordinal) ||
      !safePositiveInteger(value.checkpoint.module_count) ||
      value.checkpoint.module_ordinal > value.checkpoint.module_count || !validDate(value.checkpoint.created_at) ||
      !isRecord(value.note) || !boundedIdentity(value.note.id) || value.note.run_id !== runID ||
      value.note.id !== value.checkpoint.handoff_note_id || typeof value.note.content !== "string") {
      throw new APIRequestError("Manual checkpoint result does not match the submitted operation", "INVALID_RESPONSE", 502);
    }
    return value as unknown as PlanDeliveryCheckpointControlView;
  }

  async approvalPreview(runID: string, approvalID: string,
    signal?: AbortSignal): Promise<ApprovalPreviewView> {
    if (!boundedIdentity(runID) || !boundedIdentity(approvalID)) {
      throw new Error("A Run and exact approval identity are required");
    }
    const value = await this.get<unknown>(
      `/runs/${encodeURIComponent(runID)}/approvals/${encodeURIComponent(approvalID)}/preview`, {}, signal);
    if (!hasExactKeys(value, ["protocol_version", "run_id", "approval_id", "proposal_id",
      "tool_name", "workspace_id", "effect", "working_directory", "fields", "source_current",
      "redacted", "truncated"]) || value.protocol_version !== "approval_queue.v1" ||
      value.run_id !== runID || value.approval_id !== approvalID ||
      !boundedIdentity(value.proposal_id) || !boundedText(value.tool_name, 128) ||
      (value.workspace_id !== "" && !boundedIdentity(value.workspace_id)) ||
      !["dry_run", "record_git_approval", "file_review_required", "fetch_public_https", "browser_sensitive_action", "mcp_server_and_tool", "computer_use_app", "command_process", "unavailable"]
        .includes(String(value.effect)) || typeof value.working_directory !== "string" ||
      !["", "."].includes(value.working_directory) ||
      typeof value.source_current !== "boolean" || typeof value.redacted !== "boolean" ||
      typeof value.truncated !== "boolean" || (value.truncated && value.source_current) ||
      (value.effect === "unavailable" && value.source_current) ||
      !Array.isArray(value.fields) || value.fields.length > 8 ||
      !value.fields.every((field) => hasExactKeys(field, ["name", "value"]) &&
        boundedText(field.name, 64) && typeof field.value === "string" &&
        [...field.value].length <= 8192 && !field.value.includes("\u0000"))) {
      throw new APIRequestError("Approval preview violated its exact binding", "INVALID_RESPONSE", 502);
    }
    const effects: Record<string, string> = { shell: "dry_run", script_process: "dry_run",
      "git.advanced": "record_git_approval", replace_file: "file_review_required",
      create_file: "file_review_required", move_file: "file_review_required", delete_file: "file_review_required",
      web_fetch: "fetch_public_https", agent_browser_sensitive: "browser_sensitive_action", mcp_tool_call: "mcp_server_and_tool", command_runtime: "command_process" };
    if (value.effect !== (effects[value.tool_name] ?? "unavailable") &&
      !(value.tool_name === "mcp_tool_call" && value.effect === "computer_use_app")) {
      throw new APIRequestError("Approval preview returned a different effect", "INVALID_RESPONSE", 502);
    }
    return value as unknown as ApprovalPreviewView;
  }

  async approvalQueue(runID: string, signal?: AbortSignal): Promise<ApprovalQueueView> {
    if (!boundedIdentity(runID) || runID.trim() !== runID) {
      throw new Error("A normalized Run identity is required");
    }
    const value = await this.get<unknown>(
      `/runs/${encodeURIComponent(runID)}/approvals`, {}, signal,
    );
    return parseApprovalQueue(value, runID);
  }

  async controlledCommandProposals(runID: string,
    signal?: AbortSignal): Promise<PageResult<ControlledCommandProposalView>> {
    if (!boundedIdentity(runID) || runID.trim() !== runID) {
      throw new Error("Controlled command proposal capability and normalized Run are required");
    }
    const page = await this.getPage<unknown>(
      `/runs/${encodeURIComponent(runID)}/command-proposals`, { limit: 100 }, "", signal,
    );
    return {
      ...page,
      items: page.items.map((item) => parseControlledCommandProposal(item, runID)),
    };
  }

  async controlledCommandProposal(runID: string, proposalID: string,
    signal?: AbortSignal): Promise<ControlledCommandProposalView> {
    if (!boundedIdentity(runID) || runID.trim() !== runID ||
      !boundedIdentity(proposalID) || proposalID.trim() !== proposalID) {
      throw new Error("Controlled command proposal capability and normalized identities are required");
    }
    return parseControlledCommandProposal(await this.get<unknown>(
      `/runs/${encodeURIComponent(runID)}/command-proposals/${encodeURIComponent(proposalID)}`,
      {}, signal,
    ), runID, proposalID);
  }

  async hostCommandProposals(runID: string,
    signal?: AbortSignal): Promise<PageResult<HostCommandProposalView>> {
    if (!boundedIdentity(runID) || runID.trim() !== runID) {
      throw new Error("Host command proposal capability and normalized Run are required");
    }
    const page = await this.getPage<unknown>(
      `/runs/${encodeURIComponent(runID)}/host-command-proposals`, { limit: 100 }, "", signal,
    );
    return {
      ...page,
      items: page.items.map((item) => parseHostCommandProposal(item, runID)),
    };
  }

  async hostCommandProposal(runID: string, proposalID: string,
    signal?: AbortSignal): Promise<HostCommandProposalView> {
    if (!boundedIdentity(runID) || runID.trim() !== runID ||
      !boundedIdentity(proposalID) || proposalID.trim() !== proposalID) {
      throw new Error("Host command proposal capability and normalized identities are required");
    }
    return parseHostCommandProposal(await this.get<unknown>(
      `/runs/${encodeURIComponent(runID)}/host-command-proposals/${encodeURIComponent(proposalID)}`,
      {}, signal,
    ), runID, proposalID);
  }

  async resumeHostCommandProposal(runID: string, proposalID: string,
    signal?: AbortSignal): Promise<HostCommandProposalView> {
    if (!this.hasRunExecution ||
      !boundedIdentity(runID) || runID.trim() !== runID ||
      !boundedIdentity(proposalID) || proposalID.trim() !== proposalID) {
      throw new Error("Historical command continuation requires current control and exact identities");
    }
    const result = await this.sendControlRequest<unknown>(
      `/runs/${encodeURIComponent(runID)}/host-command-proposals/${encodeURIComponent(proposalID)}/resume`,
      undefined, signal,
    );
    return parseHostCommandProposal(result, runID, proposalID);
  }

  async decideApproval(runID: string, approvalID: string,
    body: ApprovalDecisionControlRequestView, idempotencyKey: string,
    signal?: AbortSignal): Promise<ApprovalDecisionControlView> {
    if (!this.hasApprovalControl) {
      throw new Error("Approval control capability is required for this operation");
    }
    if (!boundedIdentity(runID) || runID.trim() !== runID ||
      !boundedIdentity(approvalID) || approvalID.trim() !== approvalID ||
      body.version !== "approval_control.v1" ||
      !["approve_once", "approve_for_thread", "approve_for_run", "approve_for_run_app", "deny"].includes(body.action) ||
      (body.action !== "deny" && body.action !== "approve_for_run" && body.reason !== undefined) ||
      (body.reason !== undefined && (!boundedText(body.reason, 2_048) ||
        body.reason.trim() !== body.reason || /[\u0000-\u001f\u007f]/u.test(body.reason)))) {
      throw new Error("Normalized Run and approval identities are required");
    }
    if (body.action === "approve_for_run" ?
      !safePositiveInteger(body.grant_ttl_seconds) || body.grant_ttl_seconds > 900 || !safePositiveInteger(body.grant_max_uses) || body.grant_max_uses > 8
      : body.grant_ttl_seconds !== undefined || body.grant_max_uses !== undefined) {
      throw new Error("Bounded Run approval requires explicit limits: 1..900 seconds and 1..8 uses");
    }
    const result = await this.sendControl<unknown>(
      `/runs/${encodeURIComponent(runID)}/approvals/${encodeURIComponent(approvalID)}/decision`,
      body, idempotencyKey, signal,
    );
    return parseApprovalDecision(result, runID, approvalID, body);
  }

  private async sendControl<T>(path: string, body: unknown, idempotencyKey: string,
    signal?: AbortSignal): Promise<T> {
    if (idempotencyKey.trim() !== idempotencyKey || idempotencyKey.length < 16) {
      throw new Error("A normalized idempotency key is required");
    }
    return this.sendControlRequest<T>(path, body, signal, idempotencyKey);
  }

  private async sendReadRequest<T>(path: string, body: unknown,
    signal?: AbortSignal): Promise<T> {
    const response = await fetch(this.url(path), {
      method: "POST",
      headers: { ...this.headers(), "Content-Type": "application/json" },
      body: JSON.stringify(body),
      signal,
      cache: "no-store",
      credentials: "omit",
      referrerPolicy: "no-referrer",
    });
    const payload = await this.readJSON(response);
    if (!response.ok) {
      if (isErrorEnvelope(payload)) {
        throw new APIRequestError(payload.error.message, payload.error.code, response.status,
          payload.request_id);
      }
      throw new APIRequestError("Universal Code read request failed", "INVALID_RESPONSE", response.status,
        response.headers.get("x-request-id") || "");
    }
    if (!isSuccessEnvelope<T>(payload)) {
      throw new APIRequestError("Universal Code API returned an invalid read envelope", "INVALID_RESPONSE",
        response.status, response.headers.get("x-request-id") || "");
    }
    return payload.data;
  }

  private async sendControlRequest<T>(path: string, body: unknown, signal?: AbortSignal,
    idempotencyKey = "", method: "GET" | "POST" | "PUT" | "PATCH" | "DELETE" = "POST"): Promise<T> {
    const headers: Record<string, string> = {
      Accept: "application/json",
      Authorization: `Bearer ${this.controlToken}`,
      "Content-Type": "application/json",
    };
    if (idempotencyKey) {
      headers["Idempotency-Key"] = idempotencyKey;
    }
    const response = await fetch(this.url(path), {
      method,
      headers,
      body: method === "GET" ? undefined : JSON.stringify(body),
      signal,
      cache: "no-store",
      credentials: "omit",
      referrerPolicy: "no-referrer",
    });
    const payload = await this.readJSON(response);
    if (!response.ok) {
      if (isErrorEnvelope(payload)) {
        throw new APIRequestError(payload.error.message, payload.error.code, response.status, payload.request_id,
          payload.error.message_queued === false ? false : undefined,
          payload.error.operation_key_invalidated === true ? true : undefined,
          payload.error.turn_failed === true ? true : undefined, payload.error.turn_failure,
          payload.error.revision_unchanged);
      }
      throw new APIRequestError("Universal Code control request failed", "INVALID_RESPONSE", response.status,
        response.headers.get("x-request-id") || "");
    }
    if (!isSuccessEnvelope<T>(payload)) {
      throw new APIRequestError("Universal Code API returned an invalid control envelope", "INVALID_RESPONSE",
        response.status, response.headers.get("x-request-id") || "");
    }
    return payload.data;
  }

  async streamRunEvents(
    runID: string,
    options: {
      cursor?: string;
      signal: AbortSignal;
      onOpen?: () => void;
      onFrame: (frame: RunEventStreamView) => void;
    },
  ): Promise<void> {
    const headers = this.headers();
    headers.Accept = "text/event-stream";
    if (options.cursor) {
      headers["Last-Event-ID"] = options.cursor;
    }
    const response = await fetch(this.url(`/runs/${encodeURIComponent(runID)}/events/stream`), {
      method: "GET",
      headers,
      signal: options.signal,
      cache: "no-store",
      credentials: "omit",
      referrerPolicy: "no-referrer",
    });
    if (!response.ok) {
      throw await this.responseError(response);
    }
    if (!response.headers.get("content-type")?.toLowerCase().startsWith("text/event-stream") || !response.body) {
      throw new APIRequestError("API returned an invalid event stream", "INVALID_RESPONSE", response.status,
        response.headers.get("x-request-id") || "");
    }
    if (!options.signal.aborted) options.onOpen?.();
    await consumeSSE(response.body, (message) => {
      if (message.event !== "run.event") {
        return;
      }
      const frame = parseStreamFrame(JSON.parse(message.data) as unknown, runID);
      if (message.id === "" || message.id !== frame.cursor) {
        throw new Error("SSE id does not match the frame cursor");
      }
      options.onFrame(frame);
    });
  }

  async pollRunEvents(
    runID: string,
    cursor = "",
    limit = 100,
    signal?: AbortSignal,
  ): Promise<RunEventPollView> {
    if (!Number.isSafeInteger(limit) || limit <= 0 || limit > 100) {
      throw new Error("Event poll limit must be between 1 and 100");
    }
    const envelope = await this.request<unknown>(
      `/runs/${encodeURIComponent(runID)}/events/poll`,
      { cursor: cursor || undefined, limit },
      signal,
    );
    return parseEventPoll(envelope.data, runID, envelope.request_id);
  }

  private async request<T>(
    path: string,
    query: Record<string, QueryValue>,
    signal?: AbortSignal,
    observationKey?: string,
  ): Promise<SuccessEnvelope<T>> {
    if (observationKey !== undefined && (observationKey.length < 16 || observationKey.length > 256 ||
      /[\s\u0000-\u001f\u007f]/u.test(observationKey))) {
      throw new Error("A normalized original operation key is required");
    }
    const response = await fetch(this.url(path, query), {
      method: "GET",
      headers: { ...this.headers(), ...(observationKey === undefined ? {} : { "Idempotency-Key": observationKey }) },
      signal,
      cache: "no-store",
      credentials: "omit",
      referrerPolicy: "no-referrer",
    });
    const payload = await this.readJSON(response);
    if (!response.ok) {
      if (isErrorEnvelope(payload)) {
        throw new APIRequestError(payload.error.message, payload.error.code, response.status, payload.request_id);
      }
      throw new APIRequestError("Universal Code API request failed", "INVALID_RESPONSE", response.status,
        response.headers.get("x-request-id") || "");
    }
    if (!isSuccessEnvelope<T>(payload)) {
      throw new APIRequestError("Universal Code API returned an invalid envelope", "INVALID_RESPONSE", response.status,
        response.headers.get("x-request-id") || "");
    }
    return payload;
  }

  private async responseError(response: Response): Promise<APIRequestError> {
    const payload = await this.readJSON(response);
    if (isErrorEnvelope(payload)) {
      return new APIRequestError(payload.error.message, payload.error.code, response.status, payload.request_id);
    }
    return new APIRequestError("Universal Code API request failed", "INVALID_RESPONSE", response.status,
      response.headers.get("x-request-id") || "");
  }

  private async readJSON(response: Response): Promise<unknown> {
    try {
      return await response.json() as unknown;
    } catch {
      return null;
    }
  }

  private headers(): Record<string, string> {
    return {
      Accept: "application/json",
      Authorization: `Bearer ${this.token}`,
    };
  }

  private url(path: string, query: Record<string, QueryValue> = {}): string {
    if (!path.startsWith("/") || path.startsWith("//")) {
      throw new Error("API path must be relative to the configured base path");
    }
    const url = new URL(`${this.baseURL}${path}`, window.location.origin);
    if (!url.pathname.startsWith(`${this.baseURL}/`)) {
      throw new Error("API path escaped the configured base path");
    }
    for (const [key, value] of Object.entries(query)) {
      if (value !== undefined && value !== "") {
        url.searchParams.set(key, String(value));
      }
    }
    return `${url.pathname}${url.search}`;
  }
}
