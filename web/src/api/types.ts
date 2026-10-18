import type { components } from "./schema";

export type ProjectActionsView = components["schemas"]["ProjectActionsView"];
export type ProjectActionView = components["schemas"]["ProjectActionView"];
export type ProjectActionResultView = components["schemas"]["ProjectActionResultView"];
export type ProjectActionJobView = components["schemas"]["ProjectActionJobView"];

export type ThreadExecutionView = components["schemas"]["ThreadExecutionState"];

export type ThreadTurnFailureReferenceView = components["schemas"]["ThreadTurnFailureReferenceView"];
export type APIErrorView = components["schemas"]["APIError"];
export type ApprovalDecisionControlRequestView = components["schemas"]["ApprovalDecisionControlRequestView"];
export type ApprovalDecisionControlView = components["schemas"]["ApprovalDecisionControlView"];
export type ApprovalQueueItemView = components["schemas"]["ApprovalQueueItemView"];
export type ApprovalQueueView = components["schemas"]["ApprovalQueueView"];
export type ApprovalPreviewView = components["schemas"]["ApprovalPreviewView"];
export type ControlledCommandProposalView =
  components["schemas"]["ControlledCommandProposalView"];
export type HostCommandProposalView =
  components["schemas"]["HostCommandProposalView"];
export type ApprovalContinuationView = components["schemas"]["ApprovalContinuationResult"];
export type AgentGraphView = components["schemas"]["AgentGraphView"];
export type AgentNodeView = components["schemas"]["AgentNodeView"];
export type ArtifactView = components["schemas"]["ArtifactView"];
export type BrowserSafeWebReadiness = components["schemas"]["BrowserSafeWebReadiness"];
export type DelegationView = components["schemas"]["DelegationView"];
export type EventView = components["schemas"]["EventView"];
export type ExternalSkillProjectionItemView = components["schemas"]["ExternalSkillProjectionItemView"];
export type ExternalSkillProjectionView = components["schemas"]["ExternalSkillProjectionView"];
export type TaskSkillCandidatesView = components["schemas"]["TaskSkillCandidatesView"];
export type TaskSkillCandidateItem = components["schemas"]["TaskSkillCandidateItem"];
export type FanoutPlanView = components["schemas"]["FanoutPlanView"];
export type FanoutExecutionsListView = components["schemas"]["FanoutExecutionsListView"];
export type ChildTaskProposalsListView = components["schemas"]["ChildTaskProposalsListView"];
export type ChildTaskProposalView = components["schemas"]["ChildTaskProposalView"];
export type ChildTaskReviewRequestView = components["schemas"]["ChildTaskReviewRequestView"];
export type ChildTaskAdmitRequestView = components["schemas"]["ChildTaskAdmitRequestView"];
export type BatchDeliveriesListView = components["schemas"]["BatchDeliveriesListView"];
export type BatchDeliverySnapshotView = components["schemas"]["BatchDeliverySnapshotView"];
export type BatchWorkbenchView = components["schemas"]["BatchWorkbenchView"];
export type BatchWorkbenchPrepareRequestView = components["schemas"]["BatchWorkbenchPrepareRequestView"];
export type BatchWorkbenchOwnerRequestView = components["schemas"]["BatchWorkbenchOwnerRequestView"];
export type BatchWorkbenchExecuteRequestView = components["schemas"]["BatchWorkbenchExecuteRequestView"];
export type BatchDeliveryReviewRequestView =
  components["schemas"]["BatchDeliveryReviewRequestView"];
export type BatchDeliveryReviewControlView =
  components["schemas"]["BatchDeliveryReviewControlView"];
export type BatchDeliveryMergeRequestView =
  components["schemas"]["BatchDeliveryMergeRequestView"];
export type BatchDeliveryMergeControlView =
  components["schemas"]["BatchDeliveryMergeControlView"];
export type BatchDeliveryCancelRequestView =
  components["schemas"]["BatchDeliveryCancelRequestView"];
export type BatchDeliveryCancelView = components["schemas"]["BatchDeliveryCancelView"];
export type BatchDeliveryReconcileRequestView =
  components["schemas"]["BatchDeliveryReconcileRequestView"];
export type BatchDeliveryReconcileView =
  components["schemas"]["BatchDeliveryReconcileView"];
export type PriceSnapshotListView = components["schemas"]["PriceSnapshotListView"];
export type PriceSnapshotImportRequestView = components["schemas"]["PriceSnapshotImportRequestView"];
export type PriceSnapshotImportView = components["schemas"]["PriceSnapshotImportView"];
export type DockerSandboxStatusView = components["schemas"]["DockerSandboxStatusView"];
export type DockerEnvironmentView = components["schemas"]["DockerEnvironmentView"];
export type SandboxEnvironmentSettingsView = components["schemas"]["SandboxEnvironmentSettingsView"];
export type SandboxEnvironmentControlRequestView = components["schemas"]["SandboxEnvironmentControlRequestView"];
export type SandboxEnvironmentView = components["schemas"]["SandboxEnvironmentView"];
export type SandboxEnvironmentBackendView = components["schemas"]["SandboxEnvironmentBackendView"];
export type DockerSandboxAdmissionRequestView = components["schemas"]["DockerSandboxAdmissionRequestView"];
export type DockerSandboxAdmissionView = components["schemas"]["DockerSandboxAdmissionView"];
export type DockerSandboxStartRequestView = components["schemas"]["DockerSandboxStartRequestView"];
export type DockerSandboxCancelRequestView = components["schemas"]["DockerSandboxCancelRequestView"];
export type DockerSandboxCancellationView = components["schemas"]["DockerSandboxCancellationView"];
export type FanoutExecutionView = components["schemas"]["FanoutExecutionView"];
export type FanoutExecutionCancelRequestView =
  components["schemas"]["FanoutExecutionCancelRequestView"];
export type FindingReportSummaryView = components["schemas"]["FindingReportSummaryView"];
export type FindingReportView = components["schemas"]["FindingReportView"];
export type HealthView = components["schemas"]["HealthView"];
export type RuntimeCapabilitiesView = components["schemas"]["RuntimeCapabilitiesView"];
export type WorkspaceImportView = components["schemas"]["WorkspaceImportView"];
export type FullCDPBrowserSelectionView =
  components["schemas"]["FullCDPBrowserSelectionView"];
export type FullCDPSessionOpenRequestView =
  components["schemas"]["FullCDPSessionOpenRequestView"];
export type FullCDPSessionCloseRequestView =
  components["schemas"]["FullCDPSessionCloseRequestView"];
export type FullCDPSessionControlView =
  components["schemas"]["FullCDPSessionControlView"];
export type FullCDPSessionView = components["schemas"]["FullCDPSessionView"];
export type ThreadApplicationServiceView = components["schemas"]["ThreadApplicationServiceView"] & {
  workspace_mode?: "exclusive" | "shared_service";
  tree_reaped?: boolean;
};
export type ThreadApplicationServicesView = Omit<components["schemas"]["ThreadApplicationServicesView"], "services"> & {
  services: ThreadApplicationServiceView[];
};
export type ThreadApplicationServiceDetailView = Omit<components["schemas"]["ThreadApplicationServiceDetailView"], "service"> & {
  service: ThreadApplicationServiceView;
};
export type ThreadApplicationServiceStopView = Omit<components["schemas"]["ThreadApplicationServiceStopView"], "service"> & {
  service: ThreadApplicationServiceView;
};
export type ThreadView = components["schemas"]["ThreadView"];
export type ThreadRunRecoveryView = components["schemas"]["ThreadRunRecoveryView"];
export type ThreadRunRecoveryControlRequestView =
  components["schemas"]["ThreadRunRecoveryControlRequestView"];
export type ThreadRunRecoveryControlView =
  components["schemas"]["ThreadRunRecoveryControlView"];
export type ThreadDetailView = components["schemas"]["ThreadDetailView"];
export type ThreadRunView = components["schemas"]["ThreadRunView"];
export type ThreadMessageView = components["schemas"]["ThreadMessageView"];
export type ThreadTranscriptItemView = components["schemas"]["ThreadTranscriptItemView"];
export type ThreadActivityTypedDetailView =
  components["schemas"]["ThreadActivityTypedDetailView"];
export type ThreadActivityKind = ThreadActivityTypedDetailView["kind"];
export type ThreadActivityBoundaryView = components["schemas"]["ThreadActivityBoundaryView"];
export type ThreadActivityJSONFieldSummaryView =
  components["schemas"]["ThreadActivityJSONFieldSummaryView"];
export type ThreadActivityJSONSummaryView = components["schemas"]["ThreadActivityJSONSummaryView"];
export type ThreadActivityArtifactReferenceView =
  components["schemas"]["ThreadActivityArtifactReferenceView"];
export type ThreadActivityCommandDetailView =
  components["schemas"]["ThreadActivityCommandDetailView"];
export type ThreadActivityToolDetailView = components["schemas"]["ThreadActivityToolDetailView"];
export type ThreadActivityMCPContentView = components["schemas"]["ThreadActivityMCPContentView"];
export type ThreadActivityDetailView = components["schemas"]["ThreadActivityDetailView"];
export type ThreadActivityArtifactView = components["schemas"]["ThreadActivityArtifactView"];
export type ThreadExportView = components["schemas"]["ThreadExportView"];
export type ThreadCreationControlRequestView =
  components["schemas"]["ThreadCreationControlRequestView"];
export type ThreadCreationControlView = components["schemas"]["ThreadCreationControlView"];
export type ThreadMessageControlRequestView =
  components["schemas"]["ThreadMessageControlRequestView"];
export type ThreadTurnControlRequestView = components["schemas"]["ThreadTurnControlRequestView"];
export type ThreadMessageControlView = components["schemas"]["ThreadMessageControlView"];
export type ThreadLifecycleControlRequestView =
  components["schemas"]["ThreadLifecycleControlRequestView"];
export type ThreadLifecycleControlView = components["schemas"]["ThreadLifecycleControlView"];
export type ThreadExecutionPermissionControlRequestView =
  components["schemas"]["ThreadExecutionPermissionControlRequestView"];
export type ThreadExecutionPermissionControlView =
  components["schemas"]["ThreadExecutionPermissionControlView"];
export type ThreadExecutionPermissionView =
  components["schemas"]["ThreadExecutionPermissionView"];
export type CapabilityReadinessOptionView =
  components["schemas"]["CapabilityReadinessOptionView"];
export type RunCapabilityReadinessView =
  components["schemas"]["RunCapabilityReadinessView"];
export type StandardCodePresetControlRequestView =
  components["schemas"]["StandardCodePresetControlRequestView"];
export type StandardCodePresetControlView =
  Omit<components["schemas"]["StandardCodePresetControlView"], "sbx_readiness"> & {
    sbx_readiness?: components["schemas"]["StandardCodeBackendReadinessView"];
  };
export type StandardCodeDeliveryView =
  components["schemas"]["StandardCodeDeliveryReportView"];
export type StandardCodeDeliveryOutputSourceView =
  components["schemas"]["StandardCodeDeliveryOutputSourceView"];
export type StandardCodeDeliveryRecordRequestView =
  components["schemas"]["StandardCodeDeliveryRecordView"];
export type StandardCodeDeliveryRecordResultView =
  components["schemas"]["StandardCodeDeliveryRecordResultView"];
export type GitAdvancedAuthorityView = components["schemas"]["GitAdvancedAuthorityView"];
export type GitAdvancedCapabilityView = components["schemas"]["GitAdvancedCapabilitySnapshot"];
export type GitAdvancedConflictView = components["schemas"]["GitAdvancedConflictState"];
export type GitAdvancedExecuteResultView = components["schemas"]["GitAdvancedExecuteResult"];
export type GitAdvancedHunkView = components["schemas"]["GitAdvancedHunk"];
export type GitAdvancedOperationView = components["schemas"]["GitAdvancedOperationView"];
export type GitAdvancedPreviewView = components["schemas"]["GitAdvancedPreview"];
export type GitAdvancedProjectionView = components["schemas"]["GitAdvancedProjection"];
export type GitAdvancedReceiptView = components["schemas"]["GitAdvancedReceipt"];
export type GitAdvancedReviewResultView = components["schemas"]["GitAdvancedReviewResult"];
export type GitAdvancedScopeView = components["schemas"]["GitAdvancedScope"];
export type GitAdvancedSpecView = components["schemas"]["GitAdvancedSpec"];
export type GitHubReviewConnectionView = components["schemas"]["GitHubReviewConnection"];
export type GitHubReviewCredentialView = components["schemas"]["GitHubReviewCredentialView"];
export type GitHubReviewConfigureResultView = components["schemas"]["GitHubReviewConfigureResult"];
export type GitHubReviewDeviceAuthorizationView = components["schemas"]["GitHubReviewDeviceAuthorization"];
export type GitHubReviewDevicePollResultView = components["schemas"]["GitHubReviewDevicePollResult"];
export type GitHubReviewQualificationResultView = components["schemas"]["GitHubReviewQualificationResult"];
export type GitHubReviewFetchResultView = components["schemas"]["GitHubReviewFetchResult"];
export type GitHubReviewEvidenceResultView = components["schemas"]["GitHubReviewEvidenceResult"];
export type GitHubReviewProjectionView = components["schemas"]["GitHubReviewProjection"];
export type GitHubReviewWriteSpecView = components["schemas"]["GitHubReviewWriteSpec"];
export type GitHubReviewWriteReviewResultView = components["schemas"]["GitHubReviewWriteReviewResult"];
export type GitHubReviewWriteExecuteResultView = components["schemas"]["GitHubReviewWriteExecuteResult"];
export type ScheduledJobView = components["schemas"]["ScheduledJob"];
export type ScheduledJobCreateRequestView =
  components["schemas"]["ScheduledJobCreateRequestView"];
export type ScheduledJobTransitionRequestView =
  components["schemas"]["ScheduledJobTransitionRequestView"];
export type ScheduledJobObservationRequestView =
  components["schemas"]["ScheduledJobObservationRequestView"];
export type ScheduledJobControlView = components["schemas"]["ScheduledJobControlView"];
export type ScheduledJobListView = components["schemas"]["ScheduledJobListView"];
export type ScheduledJobDetailView = components["schemas"]["ScheduledJobDetailView"];
export type DoctorSnapshotView = components["schemas"]["DoctorSnapshot"];
export type DebugQueryResultView = components["schemas"]["DebugQueryResult"];
export type DiagnosticBundleView = components["schemas"]["DiagnosticBundle"];
export type ExtensionInventoryView = components["schemas"]["ExtensionInventoryView"];
export type ExtensionMCPRegistrationRequestView = components["schemas"]["ExtensionMCPRegistrationRequestView"];
export type ExtensionMCPRegistrationView = components["schemas"]["ExtensionMCPRegistrationView"];
export type ExtensionPluginImportRequestView = components["schemas"]["ExtensionPluginImportRequestView"];
export type ExtensionPluginImportView = components["schemas"]["ExtensionPluginImportView"];
export type ExtensionMCPCallAuditView = components["schemas"]["ExtensionMCPCallAuditView"];
export type CodeIntelConfigurationRequestView = components["schemas"]["CodeIntelConfigurationRequestView"];
export type CodeIntelConfigurationView = components["schemas"]["CodeIntelConfigurationView"];
export type CodeIntelConfigurationReviewRequestView = components["schemas"]["CodeIntelConfigurationReviewRequestView"];
export type CodeIntelConfigurationTestRequestView = components["schemas"]["CodeIntelConfigurationTestRequestView"];
export type CodeIntelConfigurationTestView = components["schemas"]["CodeIntelConfigurationTestView"];
export type ExtensionMCPServerView = components["schemas"]["ExtensionMCPServerView"];
export type MCPCredentialBindingView = components["schemas"]["MCPCredentialBindingView"];
export type MCPCredentialStatusView = components["schemas"]["MCPCredentialStatusView"];
export type MCPCredentialRequestView = components["schemas"]["MCPCredentialRequestView"];
export type ExtensionMCPReviewRequestView =
  components["schemas"]["ExtensionMCPReviewRequestView"];
export type ExtensionRefreshRequestView = components["schemas"]["ExtensionRefreshRequestView"];
export type ExtensionPluginInstallationView =
  components["schemas"]["ExtensionPluginInstallationView"];
export type ExtensionPluginReviewRequestView =
  components["schemas"]["ExtensionPluginReviewRequestView"];
export type PluginHistoryView = components["schemas"]["PluginHistoryView"];
export type PluginRollbackRequestView = components["schemas"]["PluginRollbackRequestView"];
export type PluginRollbackView = components["schemas"]["PluginRollbackView"];
export type PluginPublisherRevocationRequestView = components["schemas"]["PluginPublisherRevocationRequestView"];
export type PluginPublisherRevocationView = components["schemas"]["PluginPublisherRevocationView"];
export type PluginPublisherTrustView = components["schemas"]["PluginPublisherTrustView"];
export type PluginPublisherTrustRequestView = components["schemas"]["PluginPublisherTrustRequestView"];
export type PluginPublisherTrustResponseView = components["schemas"]["PluginPublisherTrustResponseView"];
export type HookDiagnosticsView = components["schemas"]["HookDiagnosticsView"];
export type CodeIntelInventoryView = components["schemas"]["CodeIntelInventoryView"];
export type CodeIntelServerView = components["schemas"]["CodeIntelServerView"];
export type CodeIntelQualificationView =
  components["schemas"]["CodeIntelQualificationView"];
export type UIEvidenceArtifactMetadata = components["schemas"]["UIEvidenceArtifactMetadata"];
export type UIEvidenceAttempt = components["schemas"]["UIEvidenceAttempt"];
export type UIEvidenceBundle = components["schemas"]["UIEvidenceBundle"];
export type UIEvidenceStartView = components["schemas"]["uiEvidenceStartView"];
export type EmbeddedAnalyzerExecutionRequestView =
  components["schemas"]["EmbeddedAnalyzerExecutionRequestView"];
export type EmbeddedAnalyzerExecutionControlView =
  components["schemas"]["EmbeddedAnalyzerExecutionControlView"];
export type RunWakeWorkerHealthView = components["schemas"]["RunWakeWorkerHealthView"];
export type MessageView = components["schemas"]["MessageView"];
export type ModelAvailabilityView = components["schemas"]["ModelAvailabilityView"];
export type ModelHarnessAvailabilityView = components["schemas"]["ModelHarnessAvailabilityView"];
export type ModelHarnessQualificationRequestView = components["schemas"]["ModelHarnessQualificationRequestView"];
export type AvailableModelRouteCollectionView =
  components["schemas"]["AvailableModelRouteCollectionView"];
export type AvailableModelRouteView = components["schemas"]["AvailableModelRouteView"];
export type ProviderFailureReason =
  components["schemas"]["ProviderDiagnosticView"]["failure_reason"];
export type ModelHarnessQualificationView = components["schemas"]["ModelHarnessQualificationView"];
export type ModelRouteControlRequestView = components["schemas"]["ModelRouteControlRequestView"];
export type ThreadModelRouteControlRequestView =
  components["schemas"]["ThreadModelRouteControlRequestView"];
export type ThreadModelRouteView = components["schemas"]["ThreadModelRouteView"];
export type ProviderSearchReadinessView =
  components["schemas"]["ProviderSearchReadinessView"];
export type SearchDiagnosticCode = "none" | "network" | "rate_limited" |
  "access_challenge" | "authentication" | "provider_rejected" |
  "tool_unsupported" | "no_usable_results" | "invalid_response" |
  "not_configured" | "not_authorized" | "timeout" | "configuration_changed";
export type SearchDiagnosticsRequestView = {
  version: "search_diagnostics.v1";
  confirm: true;
};
export type SearchDiagnosticsView = {
  protocol_version: "search_diagnostics.v1";
  thread_id: string;
  run_id: string;
  mode_revision: number;
  model_route: string;
  provider: string;
  model: string;
  search_policy: "disabled" | "auto" | "web" | "searxng" | "provider_native";
  backend: string;
  checked_at: string;
  state: "succeeded" | "failed";
  code: SearchDiagnosticCode;
  result_count: number;
  network_request_attempted: boolean;
  retry_after?: string;
  rate_limit_reset?: string;
  required_target?: string;
  http_status?: number;
};
export type OperationReceiptView = components["schemas"]["OperationReceiptView"];
export type OperationReceiptHistoryView = components["schemas"]["OperationReceiptHistoryView"];
export type EvidenceAttachmentRequestView = components["schemas"]["EvidenceAttachmentRequestView"];
export type EvidenceAttachmentView = components["schemas"]["EvidenceAttachmentView"];
export type EvidenceInventoryView = components["schemas"]["EvidenceInventoryView"];
export type OperatorActionCenterView = components["schemas"]["OperatorActionCenterView"];
export type OperatorActionItemView = components["schemas"]["OperatorActionItemView"];
export type ProviderDiagnosticRequestView = components["schemas"]["ProviderDiagnosticRequestView"];
export type ProviderDiagnosticView = components["schemas"]["ProviderDiagnosticView"];
export type ProviderCredentialListView = components["schemas"]["ProviderCredentialListView"];
export type ProviderCredentialRequestView = components["schemas"]["ProviderCredentialRequestView"];
export type ProviderCredentialStatusView = components["schemas"]["ProviderCredentialStatusView"];
export type ProviderDefinitionView = components["schemas"]["ProviderDefinition"];
export interface ProviderModelDiscoveryRequestView {
  version: "provider_model_discovery.v1";
  provider_id: string;
  endpoint_url: string;
  transport: "openai_chat_completions" | "openai_responses" | "anthropic_messages";
  advanced_config: Record<string, unknown>;
  secret?: string;
  expected_definition_revision?: number;
  confirm_discovery: true;
}
export interface DiscoveredProviderModelView {
  id: string;
  display_name?: string;
  input_token_limit?: number;
  output_token_limit?: number;
}
export interface ProviderModelDiscoveryView {
  version: "provider_model_discovery.v1";
  source: "provider_api";
  models: DiscoveredProviderModelView[];
  truncated: boolean;
}
export type ProviderDefinitionCollectionView =
  components["schemas"]["ProviderDefinitionCollectionView"];
export type ProviderDefinitionUpsertRequestView =
  components["schemas"]["ProviderDefinitionUpsertRequestView"];
export type ProviderDefinitionDeleteRequestView =
  components["schemas"]["ProviderDefinitionDeleteRequestView"];
export type ProviderDefinitionMutationView =
  components["schemas"]["ProviderDefinitionMutationView"];
export type FileEditProposalRequestView = components["schemas"]["FileEditProposalRequestView"];
export type FileEditProposalRecoveryView = components["schemas"]["FileEditProposalRecoveryView"];
export type FileEditProposalSourceView = components["schemas"]["FileEditProposalSourceView"];
export type FileEditProposalView = components["schemas"]["FileEditProposalView"];
export type FileEditRevertProposalView = components["schemas"]["FileEditRevertProposalView"];
export type FileEditPreviewView = components["schemas"]["FileEditPreviewView"];
export type FileEditApplyRequestView = components["schemas"]["FileEditApplyRequestView"];
export type FileEditApplyView = components["schemas"]["FileEditApplyView"];
export type FileEditQueueView = components["schemas"]["FileEditQueueView"];
export type FileEditChangeSetView = components["schemas"]["FileEditChangeSetView"];
export type FileEditReviewRequestView = components["schemas"]["FileEditReviewRequestView"];
export type FileEditReviewView = components["schemas"]["FileEditReviewView"];
export type RunWakeCancelRequestView = components["schemas"]["RunWakeCancelRequestView"];
export type RunWakeControlView = components["schemas"]["RunWakeControlView"];
export type RunWakeScheduleRequestView = components["schemas"]["RunWakeScheduleRequestView"];
export type RunWakeStateView = components["schemas"]["RunWakeStateView"];
export type RunWakeExecutionRequestView = components["schemas"]["RunWakeExecutionRequestView"];
export type RunWakeExecutionView = components["schemas"]["RunWakeExecutionView"];
export type SkillPackageInstallRequestView = components["schemas"]["SkillPackageInstallRequestView"];
export type SkillCatalogPublishersView = components["schemas"]["SkillCatalogPublishersView"];
export type SkillCatalogPublisherView = components["schemas"]["SkillCatalogPublisherView"];
export type SkillCatalogPublisherControlRequestView = components["schemas"]["SkillCatalogPublisherControlRequestView"];
export type SkillPackageInstallView = components["schemas"]["SkillPackageInstallView"];
export type PluginSkillInstallView = components["schemas"]["PluginSkillInstallView"];
export type SkillPackageInstallResult = SkillPackageInstallView | PluginSkillInstallView;
export type SkillPackageInventoryView = components["schemas"]["SkillPackageInventoryView"];
export type SkillPackageItemView = components["schemas"]["SkillPackageItemView"];
export type SkillPackageRemovalSummaryView = components["schemas"]["SkillPackageRemovalSummaryView"];
export type SkillPackageRemoveRequestView = components["schemas"]["SkillPackageRemoveRequestView"];
export type SkillPackageRemovalView = components["schemas"]["SkillPackageRemovalView"];
export type NoteView = components["schemas"]["NoteView"];
export type OperatorSteeringQueueView = components["schemas"]["OperatorSteeringQueueView"];
export type Page = components["schemas"]["Page"];
export type PlanManualAcceptance = "required" | "on_demand";
// Older servers omit the selection policy; absence retains their required manual acceptance.
export type PlanDeliveryStateView = Omit<components["schemas"]["PlanDeliveryStateView"], "selection" | "continued_completions"> & {
  continued_completions?: Array<NonNullable<components["schemas"]["PlanDeliveryStateView"]["continued_completions"]>[number] & { completion_event_id?: string }>;
  selection?: Omit<NonNullable<components["schemas"]["PlanDeliveryStateView"]["selection"]>, "manual_acceptance"> & {
    manual_acceptance?: PlanManualAcceptance;
  };
};
export type PlanModeTransitionControlRequestView = components["schemas"]["PlanModeTransitionControlRequestView"];
export type PlanModeTransitionControlView = components["schemas"]["PlanModeTransitionControlView"];
export type PlanDeliveryTransitionControlRequestView = components["schemas"]["PlanDeliveryTransitionControlRequestView"];
export type PlanDeliveryTransitionControlView = components["schemas"]["PlanDeliveryTransitionControlView"];
export type PlanDeliveryWorkItemControlRequestView = components["schemas"]["PlanDeliveryWorkItemControlRequestView"];
export type PlanDeliveryWorkItemControlView = components["schemas"]["PlanDeliveryWorkItemControlView"];
export type PlanDeliveryCheckpointControlRequestView = components["schemas"]["PlanDeliveryCheckpointControlRequestView"];
export type PlanDeliveryCheckpointControlView = components["schemas"]["PlanDeliveryCheckpointControlView"];
export type PlanDirectionControlRequestView = components["schemas"]["PlanDirectionControlRequestView"] & { manual_acceptance?: PlanManualAcceptance };
export type PlanDirectionControlView = Omit<components["schemas"]["PlanDirectionControlView"], "manual_acceptance"> & { manual_acceptance?: PlanManualAcceptance };
export type RunDetailView = Omit<components["schemas"]["RunDetailView"], "plan_delivery"> & { plan_delivery?: PlanDeliveryStateView };
export type RunActivityItemView = components["schemas"]["RunActivityItemView"];
export type RunActivityView = components["schemas"]["RunActivityView"];
export type RunEventPollView = components["schemas"]["RunEventPollView"];
export type RunEventStreamView = components["schemas"]["RunEventStreamView"];
export type RunModeView = components["schemas"]["RunModeView"];
export type RunNetworkAuthorityControlRequestView =
  components["schemas"]["RunNetworkAuthorityControlRequestView"];
export type RunNetworkAuthorityControlView =
  components["schemas"]["RunNetworkAuthorityControlView"];
export type WebAccessMode = "disabled" | "rules" | "full";
export type RunWebAccessControlRequestView =
  components["schemas"]["RunWebAccessControlRequestView"];
export type RunWebAccessControlView =
  components["schemas"]["RunWebAccessControlView"];
export type RunExecutionProfileControlView = components["schemas"]["RunExecutionProfileControlView"];
export type RunExecutionProfileView = components["schemas"]["RunExecutionProfileView"];
export type RunExecutionPermissionControlView = components["schemas"]["RunExecutionPermissionControlView"];
export type RunExecutionPermissionView = components["schemas"]["RunExecutionPermissionView"];
export type RunBrowserCDPPermissionControlView =
  components["schemas"]["RunBrowserCDPPermissionControlView"];
export type RunBrowserCDPPermissionView = components["schemas"]["RunBrowserCDPPermissionView"];
export type RunExecutionInteractionControlRequestView = components["schemas"]["RunExecutionInteractionControlRequestView"];
export type RunExecutionInteractionControlView = components["schemas"]["RunExecutionInteractionControlView"];
export type RunExecutionInteractionView = components["schemas"]["RunExecutionInteractionView"];
export type RunCreationControlRequestView = components["schemas"]["RunCreationControlRequestView"];
export type RunCreationControlView = components["schemas"]["RunCreationControlView"];
export type RunLifecycleControlRequestView = components["schemas"]["RunLifecycleControlRequestView"];
export type RunLifecycleControlView = components["schemas"]["RunLifecycleControlView"];
export type RunExecutionControlRequestView = components["schemas"]["RunExecutionControlRequestView"];
export type RunExecutionControlView = components["schemas"]["RunExecutionControlView"];
export type ModelCancellationRequestView = components["schemas"]["ModelCancellationRequestView"];
export type ModelCancellationView = components["schemas"]["ModelCancellationView"];
export type PublicModelStreamSnapshot = components["schemas"]["PublicModelStreamSnapshot"];
export type SpecialistModelCancellationView = components["schemas"]["SpecialistModelCancellationView"];
export type SessionMessageControlRequestView = components["schemas"]["SessionMessageControlRequestView"];
export type SessionMessageControlView = components["schemas"]["SessionMessageControlView"];
export type SessionArchiveControlRequestView = components["schemas"]["SessionArchiveControlRequestView"];
export type SessionArchiveControlView = components["schemas"]["SessionArchiveControlView"];
export type SessionSteeringCancellationRequestView = components["schemas"]["SessionSteeringCancellationRequestView"];
export type SessionSteeringCancellationView = components["schemas"]["SessionSteeringCancellationView"];
export type RunView = components["schemas"]["RunView"];
export type SessionDetailView = components["schemas"]["SessionDetailView"];
export type SessionView = components["schemas"]["SessionView"];
export type SupervisorToolRoundView = components["schemas"]["SupervisorToolRoundView"];
export type WorkItemView = components["schemas"]["WorkItemView"];
export type WorkspaceView = components["schemas"]["WorkspaceView"];
export type WorkspaceExplorerView = components["schemas"]["WorkspaceExplorerView"];
export type WorkspaceSearchView = components["schemas"]["WorkspaceSearchView"];
export type WorkspaceCheckpointView = components["schemas"]["Checkpoint"];
export type WorkspaceCheckpointTimelineView = components["schemas"]["WorkspaceCheckpointTimeline"];
export type WorkspaceCheckpointRestoreView = components["schemas"]["WorkspaceRestoreResult"];
export type WorkspaceCheckpointForkView = components["schemas"]["workspaceCheckpointForkResultView"];
export type WorkspaceCheckpointChangeView = components["schemas"]["Change"];
export type WorkspaceCheckpointConflictView = components["schemas"]["Conflict"];
export type RepositoryStateView = components["schemas"]["RepositoryStateView"];
export type RepositoryDiffView = components["schemas"]["RepositoryDiffView"];
export type RepositoryHistoryView = components["schemas"]["RepositoryHistoryView"];
export type RepositoryFileHistoryView = components["schemas"]["RepositoryFileHistoryView"];
export type RepositoryCommitDetailView = components["schemas"]["RepositoryCommitDetailView"];
export type RepositoryCommitComparisonView = components["schemas"]["RepositoryCommitComparisonView"];
export type RepositoryCommitFilePreviewView = components["schemas"]["RepositoryCommitFilePreviewView"];
export type VerificationEvidenceRequestView = components["schemas"]["VerificationEvidenceRequestView"];
export type VerificationEvidenceControlView = components["schemas"]["VerificationEvidenceControlView"];
export type VerificationEvidenceInventoryView = components["schemas"]["VerificationEvidenceInventoryView"];
export type VerificationPlanRequestView = components["schemas"]["VerificationPlanRequestView"];
export type VerificationPlanControlView = components["schemas"]["VerificationPlanControlView"];
export type VerificationPlanInventoryView = components["schemas"]["VerificationPlanInventoryView"];
export type VerificationAssociationRequestView = components["schemas"]["VerificationAssociationRequestView"];
export type VerificationAssociationControlView = components["schemas"]["VerificationAssociationControlView"];
export type VerificationPlanCoverageInventoryView = components["schemas"]["VerificationPlanCoverageInventoryView"];
export type VerificationPlanItemCoverageDetailView = components["schemas"]["VerificationPlanItemCoverageDetailView"];
export type VerificationSnapshotExportView = components["schemas"]["VerificationSnapshotExportView"];
export type VerificationSnapshotReceiptRequestView = components["schemas"]["VerificationSnapshotReceiptRequestView"];
export type VerificationSnapshotReceiptView = components["schemas"]["VerificationSnapshotReceiptView"];
export type VerificationSnapshotReceiptControlView = components["schemas"]["VerificationSnapshotReceiptControlView"];
export type VerificationSnapshotReceiptInventoryView = components["schemas"]["VerificationSnapshotReceiptInventoryView"];
export type VerificationSnapshotReceiptReviewRequestView = components["schemas"]["VerificationSnapshotReceiptReviewRequestView"];
export type VerificationSnapshotReceiptReviewView = components["schemas"]["VerificationSnapshotReceiptReviewView"];
export type VerificationSnapshotReceiptReviewControlView = components["schemas"]["VerificationSnapshotReceiptReviewControlView"];
export type VerificationSnapshotReceiptReviewInventoryView = components["schemas"]["VerificationSnapshotReceiptReviewInventoryView"];
export type CodeHandoffView = components["schemas"]["CodeHandoffView"];
export type CodeHandoffExportView = components["schemas"]["CodeHandoffExportView"];
export type ContextMemoryView = components["schemas"]["Memory"];
export type MemoryCandidateView = components["schemas"]["MemoryCandidate"];
export type MemoryCandidateReviewView = components["schemas"]["MemoryCandidateReviewResult"];
export type ContextMemoryExportView = components["schemas"]["ContextMemoryExport"];
export type ContinuityNodeView = components["schemas"]["ContinuityNode"];
export type ContinuityBranchView = components["schemas"]["continuityBranchView"];
export type ProjectInstructionStateView = components["schemas"]["ProjectInstructionState"];
export type SessionTreeView = components["schemas"]["SessionTree"];
export type SessionTreeNodeView = components["schemas"]["SessionTreeNode"];

export interface SuccessEnvelope<T> {
  version: "api.v1";
  request_id: string;
  data: T;
  page?: Page;
}

export interface ErrorEnvelope {
  version: "api.v1";
  request_id: string;
  error: APIErrorView;
}

export interface PageResult<T> {
  items: T[];
  page: Page;
  requestID: string;
}

export interface VerificationPlanItemCoveragePage {
  detail: VerificationPlanItemCoverageDetailView;
  page: Page;
  requestID: string;
}

export type TaskBudgetSettings = components["schemas"]["TaskBudgetSettings"];
export type TaskConfigurationRequest = components["schemas"]["TaskConfigurationRequest"];
export type TaskConfigurationView = components["schemas"]["TaskConfigurationView"];
