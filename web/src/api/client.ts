import { ActivityClient } from "./client/domains/activity";
import { ApprovalsClient } from "./client/domains/approvals";
import { DeliveryClient } from "./client/domains/delivery";
import { EvidenceClient } from "./client/domains/evidence";
import { ExtensionsClient } from "./client/domains/extensions";
import { FileEditsClient } from "./client/domains/file-edits";
import { ModelsClient } from "./client/domains/models";
import { PlansClient } from "./client/domains/plans";
import { ProjectsClient } from "./client/domains/projects";
import { RepositoryClient } from "./client/domains/repository";
import { RunsClient } from "./client/domains/runs";
import { RuntimeClient } from "./client/domains/runtime";
import { SchedulingClient } from "./client/domains/scheduling";
import { SessionsClient } from "./client/domains/sessions";
import { SkillsClient } from "./client/domains/skills";
import { UIEvidenceClient } from "./client/domains/ui-evidence";
import { VerificationClient } from "./client/domains/verification";
import { WorkspacesClient } from "./client/domains/workspaces";
import { APIRequestError, normalizeBaseURL } from "./client/protocol";
import { consumeSSE, type SSEMessage } from "./sse";
import { parseAgentBrowserScreencastFrame, type AgentBrowserScreencastFrame } from "./agent-browser";
import { APITransport } from "./client/transport";
import { WorkflowClient } from "./workflows";
import type {
  ChatGPTAccountRequestView,
  ChatGPTAuthRequestView,
  ChatGPTAuthStatusView,
  ChatGPTCancelRequestView,
  ChatGPTLoginRequestView,
} from "./chatgpt-auth";
import type { ClientCapabilities, ClientFeatures } from "./client/capabilities";
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

// Stable entry point for components. Domain clients own operations and response validation;
// every domain shares this instance's transport and capability decisions.
export class APIClient implements ClientFeatures {
  private readonly transport: APITransport;
  private readonly activityClient: ActivityClient;
  private readonly approvalsClient: ApprovalsClient;
  private readonly deliveryClient: DeliveryClient;
  private readonly evidenceClient: EvidenceClient;
  private readonly extensionsClient: ExtensionsClient;
  private readonly fileEditsClient: FileEditsClient;
  private readonly modelsClient: ModelsClient;
  private readonly plansClient: PlansClient;
  private readonly projectsClient: ProjectsClient;
  private readonly repositoryClient: RepositoryClient;
  private readonly runsClient: RunsClient;
  private readonly runtimeClient: RuntimeClient;
  private readonly schedulingClient: SchedulingClient;
  private readonly sessionsClient: SessionsClient;
  private readonly skillsClient: SkillsClient;
  private readonly uiEvidenceClient: UIEvidenceClient;
  private readonly verificationClient: VerificationClient;
  private readonly workspacesClient: WorkspacesClient;

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
    token: string,
    baseURL = import.meta.env.VITE_API_BASE_URL || "/api/v1",
    controlToken = "",
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
    this.transport = new APITransport(token, this.baseURL, controlToken, this);
    this.activityClient = new ActivityClient(this.transport, this);
    this.approvalsClient = new ApprovalsClient(this.transport, this);
    this.deliveryClient = new DeliveryClient(this.transport, this);
    this.evidenceClient = new EvidenceClient(this.transport, this);
    this.extensionsClient = new ExtensionsClient(this.transport, this);
    this.fileEditsClient = new FileEditsClient(this.transport, this);
    this.modelsClient = new ModelsClient(this.transport, this);
    this.plansClient = new PlansClient(this.transport, this);
    this.projectsClient = new ProjectsClient(this.transport, this);
    this.repositoryClient = new RepositoryClient(this.transport, this);
    this.runsClient = new RunsClient(this.transport, this);
    this.runtimeClient = new RuntimeClient(this.transport, this);
    this.schedulingClient = new SchedulingClient(this.transport, this);
    this.sessionsClient = new SessionsClient(this.transport, this);
    this.skillsClient = new SkillsClient(this.transport, this);
    this.uiEvidenceClient = new UIEvidenceClient(this.transport, this);
    this.verificationClient = new VerificationClient(this.transport, this);
    this.workspacesClient = new WorkspacesClient(this.transport, this);
  }

  health(signal?: AbortSignal): Promise<HealthView> {
    return this.runtimeClient.health(signal);
  }

  runtimeCapabilities(signal?: AbortSignal): Promise<RuntimeCapabilitiesView> {
    return this.runtimeClient.runtimeCapabilities(signal);
  }

  createProjectlessWorkspace(idempotencyKey: string, signal?: AbortSignal): Promise<ProjectlessWorkspaceView> {
    return this.workspacesClient.createProjectlessWorkspace(idempotencyKey, signal);
  }

  getProjectlessDirectory(signal?: AbortSignal): Promise<ProjectlessDirectory> {
    return this.workspacesClient.getProjectlessDirectory(signal);
  }

  setProjectlessDirectory(directoryPath: string, signal?: AbortSignal): Promise<ProjectlessDirectory> {
    return this.workspacesClient.setProjectlessDirectory(directoryPath, signal);
  }

  getWorkspaceProject(id: string, signal?: AbortSignal): Promise<WorkspaceProjectView> {
    return this.workspacesClient.getWorkspaceProject(id, signal);
  }

  renameWorkspace(id: string, name: string, signal?: AbortSignal): Promise<WorkspaceProjectView> {
    return this.workspacesClient.renameWorkspace(id, name, signal);
  }

  renameThread(id: string, title: string, signal?: AbortSignal): Promise<ThreadView> {
    return this.sessionsClient.renameThread(id, title, signal);
  }

  pinThread(id: string, pinned: boolean, signal?: AbortSignal): Promise<ThreadView> {
    return this.sessionsClient.pinThread(id, pinned, signal);
  }

  importWorkspace(directoryPath: string, signal?: AbortSignal, name?: string): Promise<WorkspaceImportView> {
    return this.workspacesClient.importWorkspace(directoryPath, signal, name);
  }

  runCapabilityReadiness(runID: string,
    signal?: AbortSignal): Promise<RunCapabilityReadinessView> {
    return this.runtimeClient.runCapabilityReadiness(runID, signal);
  }

  getThreadCommandServicePolicy(threadID: string, signal?: AbortSignal) {
    return this.sessionsClient.getThreadCommandServicePolicy(threadID, signal);
  }

  setThreadCommandServicePolicy(threadID: string, body: ThreadCommandServicePolicyRequest, signal?: AbortSignal) {
    return this.sessionsClient.setThreadCommandServicePolicy(threadID, body, signal);
  }

  listThreadApplicationServices(threadID: string, signal?: AbortSignal, limit = 20) {
    return this.sessionsClient.listThreadApplicationServices(threadID, signal, limit);
  }

  getThreadApplicationService(threadID: string, jobID: string, signal?: AbortSignal) {
    return this.sessionsClient.getThreadApplicationService(threadID, jobID, signal);
  }

  stopThreadApplicationService(threadID: string, runID: string, jobID: string, signal?: AbortSignal) {
    return this.sessionsClient.stopThreadApplicationService(threadID, runID, jobID, signal);
  }

  getFullCDPSession(runID: string,
    signal?: AbortSignal): Promise<FullCDPSessionControlView> {
    return this.runtimeClient.getFullCDPSession(runID, signal);
  }

  openFullCDPSession(runID: string, body: FullCDPSessionOpenRequestView,
    idempotencyKey: string, signal?: AbortSignal): Promise<FullCDPSessionControlView> {
    return this.runtimeClient.openFullCDPSession(runID, body, idempotencyKey, signal);
  }

  closeFullCDPSession(runID: string, body: FullCDPSessionCloseRequestView,
    idempotencyKey: string, signal?: AbortSignal): Promise<FullCDPSessionControlView> {
    return this.runtimeClient.closeFullCDPSession(runID, body, idempotencyKey, signal);
  }

  configureStandardCode(runID: string,
    action: "configure" | "pause_and_configure",
    body: StandardCodePresetControlRequestView, idempotencyKey: string,
    signal?: AbortSignal): Promise<StandardCodePresetControlView> {
    return this.runtimeClient.configureStandardCode(runID, action, body, idempotencyKey, signal);
  }

  createStandardCode(body: StandardCodePresetControlRequestView,
    idempotencyKey: string, signal?: AbortSignal): Promise<StandardCodePresetControlView> {
    return this.runtimeClient.createStandardCode(body, idempotencyKey, signal);
  }

  codeIntelInventory(workspaceID = "", signal?: AbortSignal):
    Promise<CodeIntelInventoryView> {
    return this.extensionsClient.codeIntelInventory(workspaceID, signal);
  }

  gitAdvancedProjection(runID: string,
    signal?: AbortSignal): Promise<GitAdvancedProjectionView> {
    return this.repositoryClient.gitAdvancedProjection(runID, signal);
  }

  discoverGitAdvancedHunks(runID: string, spec: GitAdvancedSpecView,
    signal?: AbortSignal): Promise<GitAdvancedReviewResultView> {
    return this.repositoryClient.discoverGitAdvancedHunks(runID, spec, signal);
  }

  reviewGitAdvanced(runID: string, body: {
    operation_key: string;
    scope: GitAdvancedScopeView;
    spec: GitAdvancedSpecView;
  }, signal?: AbortSignal): Promise<GitAdvancedReviewResultView> {
    return this.repositoryClient.reviewGitAdvanced(runID, body, signal);
  }

  executeGitAdvanced(runID: string, body: {
    operation_id: string;
    approval_id: string;
    scope: GitAdvancedScopeView;
  }, signal?: AbortSignal): Promise<GitAdvancedExecuteResultView> {
    return this.repositoryClient.executeGitAdvanced(runID, body, signal);
  }

  githubReviewConnections(enabledOnly = false,
    signal?: AbortSignal): Promise<GitHubReviewCredentialView[]> {
    return this.repositoryClient.githubReviewConnections(enabledOnly, signal);
  }

  githubReviewCredential(connectionID: string,
    signal?: AbortSignal): Promise<GitHubReviewCredentialView> {
    return this.repositoryClient.githubReviewCredential(connectionID, signal);
  }

  configureGitHubReview(body: {
    connection_id?: string;
    repository: { host: "github.com"; owner: string; name: string; full_name: string; private: boolean };
    credential: { name: string; kind: "github_app_device" | "oauth_user" | "fine_grained_pat" };
    client_id?: string;
    allowed_log_hosts: string[];
    write_enabled: boolean;
    enabled: boolean;
    expected_generation: number;
  }, signal?: AbortSignal): Promise<GitHubReviewConfigureResultView> {
    return this.repositoryClient.configureGitHubReview(body, signal);
  }

  beginGitHubReviewDeviceFlow(connectionID: string,
    signal?: AbortSignal): Promise<GitHubReviewDeviceAuthorizationView> {
    return this.repositoryClient.beginGitHubReviewDeviceFlow(connectionID, signal);
  }

  pollGitHubReviewDeviceFlow(connectionID: string, sessionID: string,
    signal?: AbortSignal): Promise<GitHubReviewDevicePollResultView> {
    return this.repositoryClient.pollGitHubReviewDeviceFlow(connectionID, sessionID, signal);
  }

  disconnectGitHubReview(connectionID: string,
    signal?: AbortSignal): Promise<GitHubReviewCredentialView> {
    return this.repositoryClient.disconnectGitHubReview(connectionID, signal);
  }

  qualifyGitHubReview(connectionID: string, pullRequest: number,
    signal?: AbortSignal): Promise<GitHubReviewQualificationResultView> {
    return this.repositoryClient.qualifyGitHubReview(connectionID, pullRequest, signal);
  }

  fetchGitHubReview(connectionID: string, pullRequest: number,
    signal?: AbortSignal): Promise<GitHubReviewFetchResultView> {
    return this.repositoryClient.fetchGitHubReview(connectionID, pullRequest, signal);
  }

  githubReviewProjection(runID: string, connectionID: string, pullRequest = 0,
    signal?: AbortSignal): Promise<GitHubReviewProjectionView> {
    return this.repositoryClient.githubReviewProjection(runID, connectionID, pullRequest, signal);
  }

  buildGitHubReviewEvidence(runID: string, snapshotID: string,
    signal?: AbortSignal): Promise<GitHubReviewEvidenceResultView> {
    return this.repositoryClient.buildGitHubReviewEvidence(runID, snapshotID, signal);
  }

  reviewGitHubWrite(runID: string, body: { connection_id: string; snapshot_id: string;
    operation_key: string; spec: GitHubReviewWriteSpecView },
  signal?: AbortSignal): Promise<GitHubReviewWriteReviewResultView> {
    return this.repositoryClient.reviewGitHubWrite(runID, body, signal);
  }

  executeGitHubWrite(runID: string, operationID: string, approvalID: string,
    signal?: AbortSignal): Promise<GitHubReviewWriteExecuteResultView> {
    return this.repositoryClient.executeGitHubWrite(runID, operationID, approvalID, signal);
  }

  extensionInventory(runID = "", signal?: AbortSignal, workspaceID = ""): Promise<ExtensionInventoryView> {
    return this.extensionsClient.extensionInventory(runID, signal, workspaceID);
  }

  registerMCPServer(body: ExtensionMCPRegistrationRequestView,
    signal?: AbortSignal): Promise<ExtensionMCPRegistrationView> {
    return this.extensionsClient.registerMCPServer(body, signal);
  }

  mcpCredentialStatus(binding: MCPCredentialBindingView, signal?: AbortSignal): Promise<MCPCredentialStatusView> {
    return this.extensionsClient.mcpCredentialStatus(binding, signal);
  }

  changeMCPCredential(body: MCPCredentialRequestView, signal?: AbortSignal): Promise<MCPCredentialStatusView> {
    return this.extensionsClient.changeMCPCredential(body, signal);
  }

  changeMCPOAuth(body: MCPCredentialRequestView, signal?: AbortSignal): Promise<MCPCredentialStatusView> {
    return this.extensionsClient.changeMCPOAuth(body, signal);
  }

  importPluginPackage(body: ExtensionPluginImportRequestView,
    signal?: AbortSignal): Promise<ExtensionPluginImportView> {
    return this.extensionsClient.importPluginPackage(body, signal);
  }

  stageCodeIntelConfiguration(body: CodeIntelConfigurationRequestView,
    signal?: AbortSignal): Promise<CodeIntelConfigurationView> {
    return this.extensionsClient.stageCodeIntelConfiguration(body, signal);
  }

  reviewCodeIntelConfiguration(serverID: string, body: CodeIntelConfigurationReviewRequestView,
    signal?: AbortSignal): Promise<CodeIntelConfigurationView> {
    return this.extensionsClient.reviewCodeIntelConfiguration(serverID, body, signal);
  }

  testCodeIntelConfiguration(serverID: string, body: CodeIntelConfigurationTestRequestView,
    signal?: AbortSignal): Promise<CodeIntelConfigurationTestView> {
    return this.extensionsClient.testCodeIntelConfiguration(serverID, body, signal);
  }

  reviewMCPServer(serverID: string, body: ExtensionMCPReviewRequestView,
    signal?: AbortSignal): Promise<ExtensionMCPServerView> {
    return this.extensionsClient.reviewMCPServer(serverID, body, signal);
  }

  refreshMCPServer(serverID: string,
    signal?: AbortSignal): Promise<ExtensionMCPServerView> {
    return this.extensionsClient.refreshMCPServer(serverID, signal);
  }

  reviewPluginInstallation(installationID: string,
    body: ExtensionPluginReviewRequestView,
    signal?: AbortSignal): Promise<ExtensionPluginInstallationView> {
    return this.extensionsClient.reviewPluginInstallation(installationID, body, signal);
  }

  pluginHistory(installationID: string, signal?: AbortSignal): Promise<PluginHistoryView> {
    return this.extensionsClient.pluginHistory(installationID, signal);
  }

  rollbackPluginInstallation(installationID: string, body: PluginRollbackRequestView, signal?: AbortSignal): Promise<PluginRollbackView> {
    return this.extensionsClient.rollbackPluginInstallation(installationID, body, signal);
  }

  revokePluginPublisher(installationID: string, body: PluginPublisherRevocationRequestView, signal?: AbortSignal): Promise<PluginPublisherRevocationView> {
    return this.extensionsClient.revokePluginPublisher(installationID, body, signal);
  }

  trustPluginPublisher(installationID: string,
    body: PluginPublisherTrustRequestView, signal?: AbortSignal,
  ): Promise<PluginPublisherTrustResponseView> {
    return this.extensionsClient.trustPluginPublisher(installationID, body, signal);
  }

  hookDiagnostics(runID = "", workspaceID = "", signal?: AbortSignal): Promise<HookDiagnosticsView> {
    return this.extensionsClient.hookDiagnostics(runID, workspaceID, signal);
  }

  safeWebReadiness(product: string, signal?: AbortSignal): Promise<BrowserSafeWebReadiness> {
    return this.runtimeClient.safeWebReadiness(product, signal);
  }

  uiEvidence(runID: string, signal?: AbortSignal): Promise<UIEvidenceAttempt[]> {
    return this.uiEvidenceClient.uiEvidence(runID, signal);
  }

  uiEvidenceBundle(attemptID: string, signal?: AbortSignal): Promise<UIEvidenceBundle> {
    return this.uiEvidenceClient.uiEvidenceBundle(attemptID, signal);
  }

  startUIEvidence(runID: string, body: UIEvidenceStartView,
    signal?: AbortSignal): Promise<UIEvidenceAttempt> {
    return this.uiEvidenceClient.startUIEvidence(runID, body, signal);
  }

  cancelUIEvidence(attemptID: string, signal?: AbortSignal): Promise<UIEvidenceAttempt> {
    return this.uiEvidenceClient.cancelUIEvidence(attemptID, signal);
  }

  downloadUIEvidenceArtifact(attemptID: string, metadata: UIEvidenceArtifactMetadata,
    signal?: AbortSignal): Promise<Blob> {
    return this.uiEvidenceClient.downloadUIEvidenceArtifact(attemptID, metadata, signal);
  }

  uploadWorkspaceFile(workspaceID: string, file: File, operationKey: string,
    signal?: AbortSignal): Promise<WorkspaceFileAttachment> {
    return this.workspacesClient.uploadWorkspaceFile(workspaceID, file, operationKey, signal);
  }

  inspectWorkspaceFileUpload(workspaceID: string, operationKey: string, signal?: AbortSignal):
    Promise<{ state: "not_received" | "stored"; attachment?: WorkspaceFileAttachment }> {
    return this.workspacesClient.inspectWorkspaceFileUpload(workspaceID, operationKey, signal);
  }

  downloadWorkspaceFile(metadata: WorkspaceFileAttachment, signal?: AbortSignal): Promise<Blob> {
    return this.workspacesClient.downloadWorkspaceFile(metadata, signal);
  }

  uploadWorkspaceImage(workspaceID: string, file: File, operationKey: string,
    signal?: AbortSignal): Promise<WorkspaceImageAttachment> {
    return this.workspacesClient.uploadWorkspaceImage(workspaceID, file, operationKey, signal);
  }

  downloadWorkspaceImage(metadata: WorkspaceImageAttachment, signal?: AbortSignal): Promise<Blob> {
    return this.workspacesClient.downloadWorkspaceImage(metadata, signal);
  }

  downloadVerifiedImage(path: string, metadata: { mime_type: string; byte_size: number; sha256: string },
    signal?: AbortSignal): Promise<Blob> {
    return this.workspacesClient.downloadVerifiedImage(path, metadata, signal);
  }

  modelAvailability(signal?: AbortSignal): Promise<ModelAvailabilityView> {
    return this.modelsClient.modelAvailability(signal);
  }

  getArtifact(artifactID: string, signal?: AbortSignal): Promise<ArtifactView> {
    return this.evidenceClient.getArtifact(artifactID, signal);
  }

  getNote(noteID: string, signal?: AbortSignal): Promise<NoteView> {
    return this.evidenceClient.getNote(noteID, signal);
  }

  getWorkItem(workItemID: string, signal?: AbortSignal): Promise<WorkItemView> {
    return this.evidenceClient.getWorkItem(workItemID, signal);
  }

  getRunExternalSkills(runID: string,
    signal?: AbortSignal): Promise<ExternalSkillProjectionView> {
    return this.skillsClient.getRunExternalSkills(runID, signal);
  }

  workspaceExplore(workspaceID: string, path = ".",
    signal?: AbortSignal): Promise<WorkspaceExplorerView> {
    return this.workspacesClient.workspaceExplore(workspaceID, path, signal);
  }

  workspaceSearch(workspaceID: string, query: string,
    signal?: AbortSignal): Promise<WorkspaceSearchView> {
    return this.workspacesClient.workspaceSearch(workspaceID, query, signal);
  }

  repositoryState(workspaceID: string,
    signal?: AbortSignal): Promise<RepositoryStateView> {
    return this.repositoryClient.repositoryState(workspaceID, signal);
  }

  repositoryDiff(workspaceID: string,
    signal?: AbortSignal, after = ""): Promise<RepositoryDiffView> {
    return this.repositoryClient.repositoryDiff(workspaceID, signal, after);
  }

  repositoryHistory(workspaceID: string,
    signal?: AbortSignal): Promise<RepositoryHistoryView> {
    return this.repositoryClient.repositoryHistory(workspaceID, signal);
  }

  repositoryCommit(workspaceID: string, objectID: string,
    signal?: AbortSignal): Promise<RepositoryCommitDetailView> {
    return this.repositoryClient.repositoryCommit(workspaceID, objectID, signal);
  }

  repositoryCommitComparison(workspaceID: string, baseObjectID: string,
    headObjectID: string, signal?: AbortSignal): Promise<RepositoryCommitComparisonView> {
    return this.repositoryClient.repositoryCommitComparison(workspaceID, baseObjectID, headObjectID, signal);
  }

  repositoryFileHistory(workspaceID: string, path: string,
    signal?: AbortSignal): Promise<RepositoryFileHistoryView> {
    return this.repositoryClient.repositoryFileHistory(workspaceID, path, signal);
  }

  repositoryCommitFilePreview(workspaceID: string, objectID: string, path: string,
    signal?: AbortSignal): Promise<RepositoryCommitFilePreviewView> {
    return this.repositoryClient.repositoryCommitFilePreview(workspaceID, objectID, path, signal);
  }

  operationReceiptHistory(runID = "",
    signal?: AbortSignal): Promise<OperationReceiptHistoryView> {
    return this.evidenceClient.operationReceiptHistory(runID, signal);
  }

  operatorActionCenter(runID: string,
    signal?: AbortSignal): Promise<OperatorActionCenterView> {
    return this.evidenceClient.operatorActionCenter(runID, signal);
  }

  evidenceInventory(runID: string,
    signal?: AbortSignal): Promise<EvidenceInventoryView> {
    return this.evidenceClient.evidenceInventory(runID, signal);
  }

  verificationEvidence(runID: string,
    signal?: AbortSignal): Promise<VerificationEvidenceInventoryView> {
    return this.verificationClient.verificationEvidence(runID, signal);
  }

  recordVerificationEvidence(runID: string, body: VerificationEvidenceRequestView,
    idempotencyKey: string, signal?: AbortSignal): Promise<VerificationEvidenceControlView> {
    return this.verificationClient.recordVerificationEvidence(runID, body, idempotencyKey, signal);
  }

  verificationPlans(runID: string,
    signal?: AbortSignal): Promise<VerificationPlanInventoryView> {
    return this.verificationClient.verificationPlans(runID, signal);
  }

  recordVerificationPlan(runID: string, body: VerificationPlanRequestView,
    idempotencyKey: string, signal?: AbortSignal): Promise<VerificationPlanControlView> {
    return this.verificationClient.recordVerificationPlan(runID, body, idempotencyKey, signal);
  }

  verificationPlanCoverage(runID: string,
    signal?: AbortSignal): Promise<VerificationPlanCoverageInventoryView> {
    return this.verificationClient.verificationPlanCoverage(runID, signal);
  }

  verificationPlanItemCoverage(runID: string, planID: string, ordinal: number,
    signal?: AbortSignal): Promise<VerificationPlanItemCoverageDetailView> {
    return this.verificationClient.verificationPlanItemCoverage(runID, planID, ordinal, signal);
  }

  verificationPlanItemCoveragePage(runID: string, planID: string, ordinal: number,
    cursor = "", limit = 50,
    signal?: AbortSignal): Promise<VerificationPlanItemCoveragePage> {
    return this.verificationClient.verificationPlanItemCoveragePage(runID, planID, ordinal, cursor, limit, signal);
  }

  verificationPlanItemSnapshotExport(runID: string, planID: string, ordinal: number,
    format: "json" | "markdown", signal?: AbortSignal): Promise<VerificationSnapshotExportView> {
    return this.verificationClient.verificationPlanItemSnapshotExport(runID, planID, ordinal, format, signal);
  }

  verificationSnapshotReceipts(runID: string,
    signal?: AbortSignal): Promise<VerificationSnapshotReceiptInventoryView> {
    return this.verificationClient.verificationSnapshotReceipts(runID, signal);
  }

  recordVerificationSnapshotReceipt(runID: string,
    body: VerificationSnapshotReceiptRequestView, idempotencyKey: string,
    signal?: AbortSignal): Promise<VerificationSnapshotReceiptControlView> {
    return this.verificationClient.recordVerificationSnapshotReceipt(runID, body, idempotencyKey, signal);
  }

  verificationSnapshotReceiptReviews(runID: string,
    signal?: AbortSignal): Promise<VerificationSnapshotReceiptReviewInventoryView> {
    return this.verificationClient.verificationSnapshotReceiptReviews(runID, signal);
  }

  recordVerificationSnapshotReceiptReview(runID: string,
    body: VerificationSnapshotReceiptReviewRequestView, idempotencyKey: string,
    signal?: AbortSignal): Promise<VerificationSnapshotReceiptReviewControlView> {
    return this.verificationClient.recordVerificationSnapshotReceiptReview(runID, body, idempotencyKey, signal);
  }

  associateVerificationEvidence(runID: string, body: VerificationAssociationRequestView,
    idempotencyKey: string, signal?: AbortSignal): Promise<VerificationAssociationControlView> {
    return this.verificationClient.associateVerificationEvidence(runID, body, idempotencyKey, signal);
  }

  codeHandoff(runID: string, signal?: AbortSignal): Promise<CodeHandoffView> {
    return this.deliveryClient.codeHandoff(runID, signal);
  }

  standardCodeDelivery(runID: string,
    signal?: AbortSignal): Promise<StandardCodeDeliveryView> {
    return this.deliveryClient.standardCodeDelivery(runID, signal);
  }

  recordStandardCodeDelivery(runID: string, body: StandardCodeDeliveryRecordRequestView,
    idempotencyKey: string, signal?: AbortSignal): Promise<StandardCodeDeliveryRecordResultView> {
    return this.deliveryClient.recordStandardCodeDelivery(runID, body, idempotencyKey, signal);
  }

  codeHandoffExport(runID: string, format: "json" | "markdown",
    signal?: AbortSignal): Promise<CodeHandoffExportView> {
    return this.deliveryClient.codeHandoffExport(runID, format, signal);
  }

  attachEvidence(runID: string, body: EvidenceAttachmentRequestView,
    idempotencyKey: string, signal?: AbortSignal): Promise<EvidenceAttachmentView> {
    return this.evidenceClient.attachEvidence(runID, body, idempotencyKey, signal);
  }

  selectModelRoute(route: string, body: ModelRouteControlRequestView,
    signal?: AbortSignal): Promise<ModelAvailabilityView["routes"][number]> {
    return this.modelsClient.selectModelRoute(route, body, signal);
  }

  availableModelRoutes(signal?: AbortSignal): Promise<AvailableModelRouteCollectionView> {
    return this.modelsClient.availableModelRoutes(signal);
  }

  threadModelRoute(threadID: string, signal?: AbortSignal): Promise<ThreadModelRouteView> {
    return this.modelsClient.threadModelRoute(threadID, signal);
  }

  providerSearchReadiness(threadID: string,
    signal?: AbortSignal): Promise<ProviderSearchReadinessView> {
    return this.modelsClient.providerSearchReadiness(threadID, signal);
  }

  diagnoseThreadSearch(threadID: string, body: SearchDiagnosticsRequestView,
    signal?: AbortSignal): Promise<SearchDiagnosticsView> {
    return this.modelsClient.diagnoseThreadSearch(threadID, body, signal);
  }

  selectThreadModelRoute(threadID: string, body: ThreadModelRouteControlRequestView,
    signal?: AbortSignal): Promise<ThreadModelRouteView> {
    return this.modelsClient.selectThreadModelRoute(threadID, body, signal);
  }

  diagnoseProvider(body: ProviderDiagnosticRequestView,
    signal?: AbortSignal): Promise<ProviderDiagnosticView> {
    return this.modelsClient.diagnoseProvider(body, signal);
  }

  qualifyModelHarness(body: ModelHarnessQualificationRequestView,
    signal?: AbortSignal): Promise<ModelHarnessQualificationView> {
    return this.modelsClient.qualifyModelHarness(body, signal);
  }

  chatGPTAuthStatus(signal?: AbortSignal): Promise<ChatGPTAuthStatusView> {
    return this.modelsClient.chatGPTAuthStatus(signal);
  }

  startChatGPTLogin(body: ChatGPTLoginRequestView, signal?: AbortSignal): Promise<ChatGPTAuthStatusView> {
    return this.modelsClient.startChatGPTLogin(body, signal);
  }

  cancelChatGPTLogin(body: ChatGPTCancelRequestView, signal?: AbortSignal): Promise<ChatGPTAuthStatusView> {
    return this.modelsClient.cancelChatGPTLogin(body, signal);
  }

  logoutChatGPTAccount(body: ChatGPTAccountRequestView, signal?: AbortSignal): Promise<ChatGPTAuthStatusView> {
    return this.modelsClient.logoutChatGPTAccount(body, signal);
  }

  refreshChatGPTModels(body: ChatGPTAccountRequestView, signal?: AbortSignal): Promise<ChatGPTAuthStatusView> {
    return this.modelsClient.refreshChatGPTModels(body, signal);
  }

  openChatGPTUsage(body: ChatGPTAuthRequestView, signal?: AbortSignal): Promise<ChatGPTAuthStatusView> {
    return this.modelsClient.openChatGPTUsage(body, signal);
  }

  providerDefinitions(signal?: AbortSignal): Promise<ProviderDefinitionCollectionView> {
    return this.modelsClient.providerDefinitions(signal);
  }

  discoverProviderModels(body: ProviderModelDiscoveryRequestView,
    signal?: AbortSignal): Promise<ProviderModelDiscoveryView> {
    return this.modelsClient.discoverProviderModels(body, signal);
  }

  upsertProviderDefinition(provider: string, body: ProviderDefinitionUpsertRequestView,
    signal?: AbortSignal): Promise<ProviderDefinitionMutationView> {
    return this.modelsClient.upsertProviderDefinition(provider, body, signal);
  }

  deleteProviderDefinition(provider: string, body: ProviderDefinitionDeleteRequestView,
    signal?: AbortSignal): Promise<ProviderDefinitionMutationView> {
    return this.modelsClient.deleteProviderDefinition(provider, body, signal);
  }

  providerCredentialStatuses(signal?: AbortSignal): Promise<ProviderCredentialListView> {
    return this.modelsClient.providerCredentialStatuses(signal);
  }

  changeProviderCredential(provider: string, body: ProviderCredentialRequestView,
    signal?: AbortSignal): Promise<ProviderCredentialStatusView> {
    return this.modelsClient.changeProviderCredential(provider, body, signal);
  }

  fileEditQueue(runID: string, signal?: AbortSignal): Promise<FileEditQueueView> {
    return this.fileEditsClient.fileEditQueue(runID, signal);
  }

  fileEdit(runID: string, editID: string,
    signal?: AbortSignal): Promise<FileEditPreviewView> {
    return this.fileEditsClient.fileEdit(runID, editID, signal);
  }

  fileEditChangeSet(runID: string,
    signal?: AbortSignal): Promise<FileEditChangeSetView> {
    return this.fileEditsClient.fileEditChangeSet(runID, signal);
  }

  issueFileEditProposalSource(runID: string, path: string,
    signal?: AbortSignal): Promise<FileEditProposalSourceView> {
    return this.fileEditsClient.issueFileEditProposalSource(runID, path, signal);
  }

  reissueFileEditProposalSource(runID: string, path: string, expectedSHA256: string,
    signal?: AbortSignal): Promise<FileEditProposalSourceView> {
    return this.fileEditsClient.reissueFileEditProposalSource(runID, path, expectedSHA256, signal);
  }

  recoverFileEditProposal(runID: string, editID: string,
    signal?: AbortSignal): Promise<FileEditProposalRecoveryView> {
    return this.fileEditsClient.recoverFileEditProposal(runID, editID, signal);
  }

  createFileEditProposal(runID: string, body: FileEditProposalRequestView,
    signal?: AbortSignal): Promise<FileEditProposalView> {
    return this.fileEditsClient.createFileEditProposal(runID, body, signal);
  }

  reviewFileEdit(runID: string, editID: string, body: FileEditReviewRequestView,
    signal?: AbortSignal): Promise<FileEditReviewView> {
    return this.fileEditsClient.reviewFileEdit(runID, editID, body, signal);
  }

  applyFileEdit(runID: string, editID: string, body: FileEditApplyRequestView,
    idempotencyKey: string, signal?: AbortSignal): Promise<FileEditApplyView> {
    return this.fileEditsClient.applyFileEdit(runID, editID, body, idempotencyKey, signal);
  }

  runWakeState(runID: string, signal?: AbortSignal): Promise<RunWakeStateView> {
    return this.schedulingClient.runWakeState(runID, signal);
  }

  scheduleRunWake(runID: string, body: RunWakeScheduleRequestView,
    idempotencyKey: string, signal?: AbortSignal): Promise<RunWakeControlView> {
    return this.schedulingClient.scheduleRunWake(runID, body, idempotencyKey, signal);
  }

  cancelRunWake(runID: string, body: RunWakeCancelRequestView,
    idempotencyKey: string, signal?: AbortSignal): Promise<RunWakeControlView> {
    return this.schedulingClient.cancelRunWake(runID, body, idempotencyKey, signal);
  }

  consumeRunWake(runID: string, body: RunWakeExecutionRequestView,
    signal?: AbortSignal): Promise<RunWakeExecutionView> {
    return this.schedulingClient.consumeRunWake(runID, body, signal);
  }

  listScheduledJobs(runID = "", limit = 50,
    signal?: AbortSignal, cursor = ""): Promise<ScheduledJobListView & { page: Page }> {
    return this.schedulingClient.listScheduledJobs(runID, limit, signal, cursor);
  }

  getScheduledJob(jobID: string, signal?: AbortSignal): Promise<ScheduledJobDetailView> {
    return this.schedulingClient.getScheduledJob(jobID, signal);
  }

  createScheduledJob(runID: string, body: ScheduledJobCreateRequestView,
    idempotencyKey: string, signal?: AbortSignal): Promise<ScheduledJobControlView> {
    return this.schedulingClient.createScheduledJob(runID, body, idempotencyKey, signal);
  }

  transitionScheduledJob(runID: string, jobID: string,
    action: "pause" | "resume" | "cancel", body: ScheduledJobTransitionRequestView,
    idempotencyKey: string, signal?: AbortSignal): Promise<ScheduledJobControlView> {
    return this.schedulingClient.transitionScheduledJob(runID, jobID, action, body, idempotencyKey, signal);
  }

  enableScheduledJobObservation(runID: string, jobID: string,
    body: ScheduledJobObservationRequestView, idempotencyKey: string,
    signal?: AbortSignal): Promise<ScheduledJobControlView> {
    return this.schedulingClient.enableScheduledJobObservation(runID, jobID, body, idempotencyKey, signal);
  }

  diagnosticBundle(runID: string, signal?: AbortSignal): Promise<DiagnosticBundleView> {
    return this.runtimeClient.diagnosticBundle(runID, signal);
  }

  installSkillPackage(body: SkillPackageInstallRequestView,
    idempotencyKey: string, signal?: AbortSignal): Promise<SkillPackageInstallResult> {
    return this.skillsClient.installSkillPackage(body, idempotencyKey, signal);
  }

  listSkillCatalogPublishers(signal?: AbortSignal): Promise<SkillCatalogPublishersView> {
    return this.skillsClient.listSkillCatalogPublishers(signal);
  }

  controlSkillCatalogPublisher(body: SkillCatalogPublisherControlRequestView,
    signal?: AbortSignal): Promise<SkillCatalogPublisherView> {
    return this.skillsClient.controlSkillCatalogPublisher(body, signal);
  }

  listSkillPackages(filters: { surface?: string; profile?: string;
    includeRemoved?: boolean } = {}, signal?: AbortSignal): Promise<SkillPackageInventoryView> {
    return this.skillsClient.listSkillPackages(filters, signal);
  }

  removeSkillPackage(body: SkillPackageRemoveRequestView,
    idempotencyKey: string, signal?: AbortSignal): Promise<SkillPackageRemovalView> {
    return this.skillsClient.removeSkillPackage(body, idempotencyKey, signal);
  }

  executeEmbeddedAnalyzer(runID: string,
    body: EmbeddedAnalyzerExecutionRequestView, signal?: AbortSignal,
  ): Promise<EmbeddedAnalyzerExecutionControlView> {
    return this.runtimeClient.executeEmbeddedAnalyzer(runID, body, signal);
  }

  get<T>(path: string, query: Record<string, QueryValue> = {}, signal?: AbortSignal): Promise<T> {
    return this.transport.get<T>(path, query, signal);
  }

  getPage<T>(
    path: string,
    query: Record<string, QueryValue> = {},
    cursor = "",
    signal?: AbortSignal,
  ): Promise<PageResult<T>> {
    return this.transport.getPage<T>(path, query, cursor, signal);
  }

  respondMCPInput(runID: string, inputID: string,
    body: { version: "mcp-input.v1"; action: "accept" | "decline" | "cancel"; content?: Record<string, unknown> }): Promise<{ accepted: boolean }> {
    return this.extensionsClient.respondMCPInput(runID, inputID, body);
  }

  postControl<T>(
    path: string,
    body: unknown,
    idempotencyKey: string,
    signal?: AbortSignal,
  ): Promise<T> {
    return this.transport.postControl<T>(path, body, idempotencyKey, signal);
  }

  patchControl<T>(path: string, body: unknown, signal?: AbortSignal): Promise<T> {
    return this.transport.patchControl<T>(path, body, signal);
  }

  deleteControl<T>(path: string, body: unknown, signal?: AbortSignal): Promise<T> {
    return this.transport.deleteControl<T>(path, body, signal);
  }

  listProjectActions(runID: string, target: string | AbortSignal = ".", signal?: AbortSignal): Promise<import("./types").ProjectActionsView> {
    return this.projectsClient.listProjectActions(runID, target, signal);
  }

  getProjectPreparation(runID: string, target = ".", signal?: AbortSignal): Promise<ProjectPreparationView> {
    return this.projectsClient.getProjectPreparation(runID, target, signal);
  }

  prepareProject(runID: string, body: ProjectPreparationRequest, operationKey: string): Promise<ProjectPreparationView> {
    return this.projectsClient.prepareProject(runID, body, operationKey);
  }

  getProjectPreparationOperation(runID: string, operationID: string, signal?: AbortSignal): Promise<ProjectPreparationOperation> {
    return this.projectsClient.getProjectPreparationOperation(runID, operationID, signal);
  }

  cancelProjectPreparation(runID: string, operationID: string, operationKey: string): Promise<ProjectPreparationOperation> {
    return this.projectsClient.cancelProjectPreparation(runID, operationID, operationKey);
  }

  runProjectAction(runID: string, slot: ProjectActionSlot, target: string, operationKey: string): Promise<import("./types").ProjectActionResultView> {
    return this.projectsClient.runProjectAction(runID, slot, target, operationKey);
  }

  getProjectActionJob(runID: string, jobID: string, signal?: AbortSignal): Promise<import("./types").ProjectActionResultView> {
    return this.projectsClient.getProjectActionJob(runID, jobID, signal);
  }

  listTaskSkillCandidates(scope: TaskSkillCandidateScope,
    signal?: AbortSignal): Promise<import("./types").TaskSkillCandidatesView> {
    return this.projectsClient.listTaskSkillCandidates(scope, signal);
  }

  previewTaskConfiguration(body: import("./types").TaskConfigurationRequest,
    signal?: AbortSignal): Promise<import("./types").TaskConfigurationView> {
    return this.projectsClient.previewTaskConfiguration(body, signal);
  }

  getRunTaskConfiguration(runID: string, signal?: AbortSignal): Promise<import("./types").TaskConfigurationView> {
    return this.projectsClient.getRunTaskConfiguration(runID, signal);
  }

  selectProjectSkills(runID: string, choice: import("./project-skills").ProjectSkillChoice, idempotencyKey: string, signal?: AbortSignal): Promise<void> {
    return this.projectsClient.selectProjectSkills(runID, choice, idempotencyKey, signal);
  }

  selectInstalledSkills(runID: string, choice: import("./project-skills").ProjectSkillChoice, idempotencyKey: string, signal?: AbortSignal): Promise<void> {
    return this.projectsClient.selectInstalledSkills(runID, choice, idempotencyKey, signal);
  }

  createRun(body: RunCreationControlRequestView, idempotencyKey: string,
    signal?: AbortSignal): Promise<RunCreationControlView> {
    return this.runsClient.createRun(body, idempotencyKey, signal);
  }

  createThread(body: ThreadCreationControlRequestView, idempotencyKey: string,
    signal?: AbortSignal): Promise<ThreadCreationControlView> {
    return this.sessionsClient.createThread(body, idempotencyKey, signal);
  }

  submitThreadMessage(threadID: string, body: ThreadMessageControlRequestView,
    idempotencyKey: string, signal?: AbortSignal): Promise<ThreadMessageControlView> {
    return this.sessionsClient.submitThreadMessage(threadID, body, idempotencyKey, signal);
  }

  threadActivityDetail(threadID: string, activityRef: string,
    signal?: AbortSignal): Promise<ThreadActivityDetailView> {
    return this.activityClient.threadActivityDetail(threadID, activityRef, signal);
  }

  threadActivityArtifact(threadID: string, activityRef: string, artifactRef: string,
    signal?: AbortSignal): Promise<ThreadActivityArtifactView> {
    return this.activityClient.threadActivityArtifact(threadID, activityRef, artifactRef, signal);
  }

  setRunWebAccess(runID: string,
    body: RunWebAccessControlRequestView, idempotencyKey: string,
    signal?: AbortSignal): Promise<RunWebAccessControlView> {
    return this.runsClient.setRunWebAccess(runID, body, idempotencyKey, signal);
  }

  expandRunNetworkAuthority(runID: string,
    body: RunNetworkAuthorityControlRequestView, idempotencyKey: string,
    signal?: AbortSignal): Promise<RunNetworkAuthorityControlView> {
    return this.runsClient.expandRunNetworkAuthority(runID, body, idempotencyKey, signal);
  }

  threadExecution(threadID: string, signal?: AbortSignal): Promise<import("./types").ThreadExecutionView> {
    return this.sessionsClient.threadExecution(threadID, signal);
  }

  threadChanges(threadID: string, runID: string, sourceEventID: string,
    signal?: AbortSignal): Promise<ThreadChangeSummary> {
    return this.sessionsClient.threadChanges(threadID, runID, sourceEventID, signal);
  }

  undoThreadChanges(threadID: string, runID: string, sourceEventID: string,
    confirm: boolean, signal?: AbortSignal): Promise<ThreadChangeUndo> {
    return this.sessionsClient.undoThreadChanges(threadID, runID, sourceEventID, confirm, signal);
  }

  inspectThreadCreationRequest(workspaceID: string, operationKey: string,
    signal?: AbortSignal): Promise<unknown> {
    return this.sessionsClient.inspectThreadCreationRequest(workspaceID, operationKey, signal);
  }

  inspectThreadTurnRequest(threadID: string, operationKey: string,
    signal?: AbortSignal): Promise<unknown> {
    return this.sessionsClient.inspectThreadTurnRequest(threadID, operationKey, signal);
  }

  inspectThreadPlanRequest(threadID: string, runID: string, action: "enter_plan" | "enter_deliver" | "confirm",
    operationKey: string, signal?: AbortSignal): Promise<unknown> {
    return this.sessionsClient.inspectThreadPlanRequest(threadID, runID, action, operationKey, signal);
  }

  createFileEditRevertProposal(runID: string, sourceEditID: string,
    idempotencyKey: string, signal?: AbortSignal): Promise<FileEditRevertProposalView> {
    return this.fileEditsClient.createFileEditRevertProposal(runID, sourceEditID, idempotencyKey, signal);
  }

  interruptThread(threadID: string, executionID: string, idempotencyKey: string,
    signal?: AbortSignal): Promise<import("./types").ThreadExecutionView> {
    return this.sessionsClient.interruptThread(threadID, executionID, idempotencyKey, signal);
  }

  submitThreadTurn(threadID: string, body: ThreadTurnControlRequestView,
    idempotencyKey: string, signal?: AbortSignal): Promise<ThreadMessageControlView> {
    return this.sessionsClient.submitThreadTurn(threadID, body, idempotencyKey, signal);
  }

  recoverThreadRun(threadID: string, body: ThreadRunRecoveryControlRequestView,
    idempotencyKey: string, signal?: AbortSignal): Promise<ThreadRunRecoveryControlView> {
    return this.sessionsClient.recoverThreadRun(threadID, body, idempotencyKey, signal);
  }

  getThreadExecutionPermission(threadID: string,
    signal?: AbortSignal): Promise<ThreadExecutionPermissionControlView> {
    return this.sessionsClient.getThreadExecutionPermission(threadID, signal);
  }

  changeThreadExecutionPermission(threadID: string,
    body: ThreadExecutionPermissionControlRequestView, idempotencyKey: string,
    signal?: AbortSignal): Promise<ThreadExecutionPermissionControlView> {
    return this.sessionsClient.changeThreadExecutionPermission(threadID, body, idempotencyKey, signal);
  }

  moveThreadToProject(threadID: string, projectWorkspaceID: string, expectedVersion: number,
    idempotencyKey: string, signal?: AbortSignal): Promise<{ thread: ThreadView; replayed: boolean }> {
    return this.sessionsClient.moveThreadToProject(threadID, projectWorkspaceID, expectedVersion, idempotencyKey, signal);
  }

  transitionThread(threadID: string, action: "archive" | "restore" | "delete",
    body: ThreadLifecycleControlRequestView, idempotencyKey: string,
    signal?: AbortSignal): Promise<ThreadLifecycleControlView> {
    return this.sessionsClient.transitionThread(threadID, action, body, idempotencyKey, signal);
  }

  submitSessionMessage(sessionID: string, body: SessionMessageControlRequestView,
    idempotencyKey: string, signal?: AbortSignal): Promise<SessionMessageControlView> {
    return this.sessionsClient.submitSessionMessage(sessionID, body, idempotencyKey, signal);
  }

  inspectSessionMessageOperation(sessionID: string, operationKey: string,
    signal?: AbortSignal): Promise<unknown> {
    return this.sessionsClient.inspectSessionMessageOperation(sessionID, operationKey, signal);
  }

  archiveSession(sessionID: string, body: SessionArchiveControlRequestView,
    signal?: AbortSignal): Promise<SessionArchiveControlView> {
    return this.sessionsClient.archiveSession(sessionID, body, signal);
  }

  cancelSessionSteering(sessionID: string, messageID: string,
    body: SessionSteeringCancellationRequestView, idempotencyKey: string,
    signal?: AbortSignal): Promise<SessionSteeringCancellationView> {
    return this.sessionsClient.cancelSessionSteering(sessionID, messageID, body, idempotencyKey, signal);
  }

  controlRunLifecycle(runID: string, body: RunLifecycleControlRequestView,
    idempotencyKey: string, signal?: AbortSignal): Promise<RunLifecycleControlView> {
    return this.runsClient.controlRunLifecycle(runID, body, idempotencyKey, signal);
  }

  executeRun(runID: string, body: RunExecutionControlRequestView,
    idempotencyKey: string, signal?: AbortSignal): Promise<RunExecutionControlView> {
    return this.runsClient.executeRun(runID, body, idempotencyKey, signal);
  }

  getPublicModelStream(runID: string,
    signal?: AbortSignal): Promise<PublicModelStreamSnapshot> {
    return this.modelsClient.getPublicModelStream(runID, signal);
  }

  pollPublicModelStream(runID: string,
    signal?: AbortSignal): Promise<PublicModelStreamSnapshot | null> {
    return this.modelsClient.pollPublicModelStream(runID, signal);
  }

  cancelModelCall(runID: string, body: ModelCancellationRequestView,
    idempotencyKey: string, signal?: AbortSignal): Promise<ModelCancellationView> {
    return this.modelsClient.cancelModelCall(runID, body, idempotencyKey, signal);
  }

  cancelSpecialistModelCall(runID: string, agentID: string,
    body: ModelCancellationRequestView, idempotencyKey: string,
    signal?: AbortSignal): Promise<SpecialistModelCancellationView> {
    return this.modelsClient.cancelSpecialistModelCall(runID, agentID, body, idempotencyKey, signal);
  }

  getRunFanoutExecutions(runID: string, planID: string,
    signal?: AbortSignal): Promise<FanoutExecutionsListView> {
    return this.runsClient.getRunFanoutExecutions(runID, planID, signal);
  }

  cancelRunFanoutExecution(runID: string, executionID: string,
    body: FanoutExecutionCancelRequestView, idempotencyKey: string,
    signal?: AbortSignal): Promise<FanoutExecutionView> {
    return this.runsClient.cancelRunFanoutExecution(runID, executionID, body, idempotencyKey, signal);
  }

  get workflows(): WorkflowClient {
    return new WorkflowClient({
      get: (path, signal) => this.get<unknown>(path, {}, signal),
      post: (path, body, key, signal) => this.transport.sendControl<unknown>(path, body, key, signal),
    });
  }

  getRunChildTaskProposals(runID: string,
    signal?: AbortSignal): Promise<ChildTaskProposalsListView> {
    return this.runsClient.getRunChildTaskProposals(runID, signal);
  }

  reviewRunChildTaskProposal(runID: string, proposalID: string,
    body: ChildTaskReviewRequestView, idempotencyKey: string,
    signal?: AbortSignal): Promise<ChildTaskProposalView> {
    return this.runsClient.reviewRunChildTaskProposal(runID, proposalID, body, idempotencyKey, signal);
  }

  admitRunChildTaskProposal(runID: string, proposalID: string,
    body: ChildTaskAdmitRequestView, idempotencyKey: string,
    signal?: AbortSignal): Promise<ChildTaskProposalView> {
    return this.runsClient.admitRunChildTaskProposal(runID, proposalID, body, idempotencyKey, signal);
  }

  getRunBatchDeliveries(runID: string,
    signal?: AbortSignal): Promise<BatchDeliveriesListView> {
    return this.deliveryClient.getRunBatchDeliveries(runID, signal);
  }

  getRunBatchDelivery(runID: string, planID: string,
    signal?: AbortSignal): Promise<BatchDeliverySnapshotView> {
    return this.deliveryClient.getRunBatchDelivery(runID, planID, signal);
  }

  getBatchWorkbench(runID: string, planID: string, signal?: AbortSignal): Promise<BatchWorkbenchView> {
    return this.deliveryClient.getBatchWorkbench(runID, planID, signal);
  }

  prepareBatchWorkbench(runID: string, body: BatchWorkbenchPrepareRequestView,
    idempotencyKey: string, signal?: AbortSignal): Promise<BatchWorkbenchView> {
    return this.deliveryClient.prepareBatchWorkbench(runID, body, idempotencyKey, signal);
  }

  recoverBatchWorkbenchOwner(runID: string, planID: string, ordinal: number,
    body: BatchWorkbenchOwnerRequestView, idempotencyKey: string, signal?: AbortSignal): Promise<BatchWorkbenchView> {
    return this.deliveryClient.recoverBatchWorkbenchOwner(runID, planID, ordinal, body, idempotencyKey, signal);
  }

  executeBatchWorkbenchChild(runID: string, planID: string, ordinal: number,
    body: BatchWorkbenchExecuteRequestView, idempotencyKey: string, signal?: AbortSignal): Promise<BatchWorkbenchView> {
    return this.deliveryClient.executeBatchWorkbenchChild(runID, planID, ordinal, body, idempotencyKey, signal);
  }

  reviewRunBatchDeliveryChild(runID: string, planID: string, ordinal: number,
    body: BatchDeliveryReviewRequestView, idempotencyKey: string,
    signal?: AbortSignal): Promise<BatchDeliveryReviewControlView> {
    return this.deliveryClient.reviewRunBatchDeliveryChild(runID, planID, ordinal, body, idempotencyKey, signal);
  }

  mergeRunBatchDelivery(runID: string, planID: string,
    body: BatchDeliveryMergeRequestView, idempotencyKey: string,
    signal?: AbortSignal): Promise<BatchDeliveryMergeControlView> {
    return this.deliveryClient.mergeRunBatchDelivery(runID, planID, body, idempotencyKey, signal);
  }

  cancelRunBatchDelivery(runID: string, planID: string,
    body: BatchDeliveryCancelRequestView, idempotencyKey: string,
    signal?: AbortSignal): Promise<BatchDeliveryCancelView> {
    return this.deliveryClient.cancelRunBatchDelivery(runID, planID, body, idempotencyKey, signal);
  }

  reconcileRunBatchDelivery(runID: string, planID: string,
    body: BatchDeliveryReconcileRequestView,
    signal?: AbortSignal): Promise<BatchDeliveryReconcileView> {
    return this.deliveryClient.reconcileRunBatchDelivery(runID, planID, body, signal);
  }

  listPriceSnapshots(signal?: AbortSignal): Promise<PriceSnapshotListView> {
    return this.modelsClient.listPriceSnapshots(signal);
  }

  importPriceSnapshot(body: PriceSnapshotImportRequestView, idempotencyKey: string,
    signal?: AbortSignal): Promise<PriceSnapshotImportView> {
    return this.modelsClient.importPriceSnapshot(body, idempotencyKey, signal);
  }

  getSandboxEnvironment(signal?: AbortSignal): Promise<SandboxEnvironmentView> {
    return this.runtimeClient.getSandboxEnvironment(signal);
  }

  saveSandboxEnvironment(body: SandboxEnvironmentControlRequestView, signal?: AbortSignal): Promise<SandboxEnvironmentView> {
    return this.runtimeClient.saveSandboxEnvironment(body, signal);
  }

  getDockerEnvironment(signal?: AbortSignal): Promise<DockerEnvironmentView> {
    return this.runtimeClient.getDockerEnvironment(signal);
  }

  getDockerSandboxStatus(admissionID: string,
    signal?: AbortSignal): Promise<DockerSandboxStatusView> {
    return this.runtimeClient.getDockerSandboxStatus(admissionID, signal);
  }

  admitDockerSandbox(body: DockerSandboxAdmissionRequestView, idempotencyKey: string,
    signal?: AbortSignal): Promise<DockerSandboxAdmissionView> {
    return this.runtimeClient.admitDockerSandbox(body, idempotencyKey, signal);
  }

  startDockerSandbox(body: DockerSandboxStartRequestView, idempotencyKey: string,
    signal?: AbortSignal): Promise<DockerSandboxStatusView> {
    return this.runtimeClient.startDockerSandbox(body, idempotencyKey, signal);
  }

  cancelDockerSandbox(body: DockerSandboxCancelRequestView, idempotencyKey: string,
    signal?: AbortSignal): Promise<DockerSandboxCancellationView> {
    return this.runtimeClient.cancelDockerSandbox(body, idempotencyKey, signal);
  }

  selectPlanDirection(runID: string, body: PlanDirectionControlRequestView,
    idempotencyKey: string, signal?: AbortSignal): Promise<PlanDirectionControlView> {
    return this.plansClient.selectPlanDirection(runID, body, idempotencyKey, signal);
  }

  enterPlanMode(runID: string, body: PlanModeTransitionControlRequestView,
    idempotencyKey: string, signal?: AbortSignal): Promise<PlanModeTransitionControlView> {
    return this.plansClient.enterPlanMode(runID, body, idempotencyKey, signal);
  }

  enterPlanDelivery(runID: string, body: PlanDeliveryTransitionControlRequestView,
    idempotencyKey: string, signal?: AbortSignal): Promise<PlanDeliveryTransitionControlView> {
    return this.plansClient.enterPlanDelivery(runID, body, idempotencyKey, signal);
  }

  controlPlanDeliveryWorkItem(runID: string, workItemID: string, action: "start" | "complete",
    body: PlanDeliveryWorkItemControlRequestView, idempotencyKey: string,
    signal?: AbortSignal): Promise<PlanDeliveryWorkItemControlView> {
    return this.plansClient.controlPlanDeliveryWorkItem(runID, workItemID, action, body, idempotencyKey, signal);
  }

  recordPlanDeliveryCheckpoint(runID: string, workItemID: string,
    body: PlanDeliveryCheckpointControlRequestView, idempotencyKey: string,
    signal?: AbortSignal): Promise<PlanDeliveryCheckpointControlView> {
    return this.plansClient.recordPlanDeliveryCheckpoint(runID, workItemID, body, idempotencyKey, signal);
  }

  approvalPreview(runID: string, approvalID: string,
    signal?: AbortSignal): Promise<ApprovalPreviewView> {
    return this.approvalsClient.approvalPreview(runID, approvalID, signal);
  }

  approvalQueue(runID: string, signal?: AbortSignal): Promise<ApprovalQueueView> {
    return this.approvalsClient.approvalQueue(runID, signal);
  }

  controlledCommandProposals(runID: string,
    signal?: AbortSignal): Promise<PageResult<ControlledCommandProposalView>> {
    return this.approvalsClient.controlledCommandProposals(runID, signal);
  }

  controlledCommandProposal(runID: string, proposalID: string,
    signal?: AbortSignal): Promise<ControlledCommandProposalView> {
    return this.approvalsClient.controlledCommandProposal(runID, proposalID, signal);
  }

  hostCommandProposals(runID: string,
    signal?: AbortSignal): Promise<PageResult<HostCommandProposalView>> {
    return this.approvalsClient.hostCommandProposals(runID, signal);
  }

  hostCommandProposal(runID: string, proposalID: string,
    signal?: AbortSignal): Promise<HostCommandProposalView> {
    return this.approvalsClient.hostCommandProposal(runID, proposalID, signal);
  }

  resumeHostCommandProposal(runID: string, proposalID: string,
    signal?: AbortSignal): Promise<HostCommandProposalView> {
    return this.approvalsClient.resumeHostCommandProposal(runID, proposalID, signal);
  }

  decideApproval(runID: string, approvalID: string,
    body: ApprovalDecisionControlRequestView, idempotencyKey: string,
    signal?: AbortSignal): Promise<ApprovalDecisionControlView> {
    return this.approvalsClient.decideApproval(runID, approvalID, body, idempotencyKey, signal);
  }

  streamRunEvents(
    runID: string,
    options: {
      cursor?: string;
      signal: AbortSignal;
      onOpen?: () => void;
      onFrame: (frame: RunEventStreamView) => void;
    },
  ): Promise<void> {
    return this.runsClient.streamRunEvents(runID, options);
  }

  streamAgentBrowserScreencast(
    runID: string,
    options: {
      signal: AbortSignal;
      onOpen?: () => void;
      onFrame: (frame: AgentBrowserScreencastFrame) => void;
    },
  ): Promise<void> {
    const headers = this.transport.headers();
    headers.Accept = "text/event-stream";
    return this.rawEventStream(`/runs/${encodeURIComponent(runID)}/agent-browser/screencast`, headers,
      options.signal, options.onOpen, (message) => {
        if (message.event === "agent_browser.closed") {
          throw new APIRequestError("Agent browser live view closed", "STREAM_CLOSED", 200, "");
        }
        if (message.event !== "agent_browser.frame") {
          return;
        }
        options.onFrame(parseAgentBrowserScreencastFrame(JSON.parse(message.data) as unknown, runID));
      });
  }

  private async rawEventStream(path: string, headers: Record<string, string>, signal: AbortSignal,
    onOpen: (() => void) | undefined, onMessage: (message: SSEMessage) => Promise<void> | void,
  ): Promise<void> {
    const response = await fetch(this.transport.url(path), {
      method: "GET", headers, signal, cache: "no-store", credentials: "omit", referrerPolicy: "no-referrer",
    });
    if (!response.ok) {
      throw await this.transport.responseError(response);
    }
    if (!response.headers.get("content-type")?.toLowerCase().startsWith("text/event-stream") || !response.body) {
      throw new APIRequestError("API returned an invalid event stream", "INVALID_RESPONSE", response.status,
        response.headers.get("x-request-id") || "");
    }
    if (!signal.aborted) onOpen?.();
    await consumeSSE(response.body, onMessage);
  }

  pollRunEvents(
    runID: string,
    cursor = "",
    limit = 100,
    signal?: AbortSignal,
  ): Promise<RunEventPollView> {
    return this.runsClient.pollRunEvents(runID, cursor, limit, signal);
  }
}
