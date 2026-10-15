import { create } from "zustand";
import type { ClientCapabilities } from "../api/client";
import type { HealthView } from "../api/types";

type ResourceKind = "thread" | "run" | "session";

interface ConnectionState {
  health: HealthView | null;
  apiBaseURL: string | undefined;
  resourceKind: ResourceKind;
  selectedRunID: string;
  selectedSessionID: string;
  selectedThreadID: string;
  token: string;
  controlToken: string;
  runControlEnabled: boolean;
  workspaceImportEnabled: boolean;
  executionPermissionControlEnabled: boolean;
  workspaceSandboxEnabled: boolean;
  browserCDPPermissionControlEnabled: boolean;
  fullCDPDebugEnabled: boolean;
  fullCDPSessionControlEnabled: boolean;
  operatorApprovalEnabled: boolean;
  dangerFullAccessEnabled: boolean;
  commandRuntimeEnabled: boolean;
  commandRuntimeProtocolAvailable: boolean;
  commandRuntimeAdapterInstalled: boolean;
  commandRuntimeAdapterReady: boolean;
  runCreationEnabled: boolean;
  standardCodePresetEnabled: boolean;
  sessionMessageEnabled: boolean;
  threadControlEnabled: boolean;
  sessionSteeringControlEnabled: boolean;
  runLifecycleEnabled: boolean;
  runExecutionEnabled: boolean;
  threadExecutionReadEnabled: boolean;
  planDeliveryControlEnabled: boolean;
  approvalControlEnabled: boolean;
  modelControlEnabled: boolean;
  providerCredentialEnabled: boolean;
  chatGPTAuthEnabled: boolean;
  fileEditReviewEnabled: boolean;
  fileEditProposalEnabled: boolean;
  fileEditApplyEnabled: boolean;
  runWakeControlEnabled: boolean;
  runWakeExecutionEnabled: boolean;
  runWakeWorkerEnabled: boolean;
  scheduledJobControlEnabled: boolean;
  scheduledJobWorkerEnabled: boolean;
  skillInstallationEnabled: boolean;
  evidenceAttachmentEnabled: boolean;
  verificationEvidenceEnabled: boolean;
  uiEvidenceControlEnabled: boolean;
  embeddedAnalyzerExecutionEnabled: boolean;
  gitAdvancedControlEnabled: boolean;
  githubReviewControlEnabled: boolean;
  workspaceCheckpointControlEnabled: boolean;
  batchDeliveryControlEnabled: boolean;
  batchDeliveryHostValidationEnabled: boolean;
  dockerExecutionEnabled: boolean;
  agentCodeToolsEnabled: boolean;
  codeIntelEnabled: boolean;
  connect: (token: string, health: HealthView, controlToken?: string,
    capabilities?: ClientCapabilities, apiBaseURL?: string) => void;
  disconnect: () => void;
  selectRun: (runID: string) => void;
  selectSession: (sessionID: string) => void;
  selectThread: (threadID: string) => void;
  setHealth: (health: HealthView) => void;
  setResourceKind: (kind: ResourceKind) => void;
}

const initialSelection = {
  resourceKind: "thread" as const,
  selectedThreadID: "",
  selectedRunID: "",
  selectedSessionID: "",
};

export const useConnectionStore = create<ConnectionState>((set) => ({
  ...initialSelection,
  health: null,
  apiBaseURL: undefined,
  token: "",
  controlToken: "",
  runControlEnabled: false,
  workspaceImportEnabled: false,
  executionPermissionControlEnabled: false,
  workspaceSandboxEnabled: false,
  browserCDPPermissionControlEnabled: false,
  fullCDPDebugEnabled: false,
  fullCDPSessionControlEnabled: false,
  operatorApprovalEnabled: false,
  dangerFullAccessEnabled: false,

  commandRuntimeEnabled: false,
  commandRuntimeProtocolAvailable: false,
  commandRuntimeAdapterInstalled: false,
  commandRuntimeAdapterReady: false,
  runCreationEnabled: false,
  standardCodePresetEnabled: false,
  sessionMessageEnabled: false,
  threadControlEnabled: false,
  sessionSteeringControlEnabled: false,
  runLifecycleEnabled: false,
  runExecutionEnabled: false,
  threadExecutionReadEnabled: false,
  planDeliveryControlEnabled: false,
  approvalControlEnabled: false,


  modelControlEnabled: false,
  providerCredentialEnabled: false,
  chatGPTAuthEnabled: false,
  fileEditReviewEnabled: false,
  fileEditProposalEnabled: false,
  fileEditApplyEnabled: false,
  runWakeControlEnabled: false,
  runWakeExecutionEnabled: false,
  runWakeWorkerEnabled: false,
  scheduledJobControlEnabled: false,
  scheduledJobWorkerEnabled: false,
  skillInstallationEnabled: false,
  evidenceAttachmentEnabled: false,
  verificationEvidenceEnabled: false,
  uiEvidenceControlEnabled: false,
  embeddedAnalyzerExecutionEnabled: false,
  gitAdvancedControlEnabled: false,
  githubReviewControlEnabled: false,
  workspaceCheckpointControlEnabled: false,
  batchDeliveryControlEnabled: false,
  batchDeliveryHostValidationEnabled: false,
  dockerExecutionEnabled: false,
  agentCodeToolsEnabled: false,
  codeIntelEnabled: false,
  connect: (token, health, controlToken = "", capabilities = {}, apiBaseURL) => {
    const present = controlToken.trim() !== "";
    set({ token, health, controlToken, apiBaseURL,
      runControlEnabled: present && (capabilities.runControlEnabled ?? true),
      workspaceImportEnabled: present && capabilities.workspaceImportEnabled === true,
      executionPermissionControlEnabled: present &&
        (capabilities.executionPermissionControlEnabled ?? false),
      workspaceSandboxEnabled: present &&
        (capabilities.executionPermissionControlEnabled ?? false) &&
        (capabilities.workspaceSandboxEnabled ?? false),
      browserCDPPermissionControlEnabled: present &&
        (capabilities.browserCDPPermissionControlEnabled ?? false),
      fullCDPDebugEnabled: present &&
        (capabilities.browserCDPPermissionControlEnabled ?? false) &&
        (capabilities.fullCDPDebugEnabled ?? false),
      fullCDPSessionControlEnabled: present &&
        (capabilities.fullCDPDebugEnabled ?? false) &&
        (capabilities.fullCDPSessionControlEnabled ?? false),
      operatorApprovalEnabled: present && (capabilities.operatorApprovalEnabled ?? false),
      dangerFullAccessEnabled: present && (capabilities.dangerFullAccessEnabled ?? false),

      commandRuntimeEnabled: present && (capabilities.commandRuntimeEnabled ?? false),
      commandRuntimeProtocolAvailable: present &&
        (capabilities.commandRuntimeProtocolAvailable ?? false),
      commandRuntimeAdapterInstalled: present &&
        (capabilities.commandRuntimeAdapterInstalled ?? false),
      commandRuntimeAdapterReady: present &&
        (capabilities.commandRuntimeAdapterReady ?? false),
      runCreationEnabled: present && (capabilities.runCreationEnabled ?? true),
      standardCodePresetEnabled: present &&
        (capabilities.standardCodePresetEnabled ?? false),
      sessionMessageEnabled: present && (capabilities.sessionMessageEnabled ?? true),
      threadControlEnabled: present && (capabilities.threadControlEnabled ??
        ((capabilities.runCreationEnabled ?? true) &&
          (capabilities.sessionMessageEnabled ?? true))),
      sessionSteeringControlEnabled: present &&
        (capabilities.sessionSteeringControlEnabled ?? true),
      runLifecycleEnabled: present && (capabilities.runLifecycleEnabled ?? true),
      runExecutionEnabled: present && (capabilities.runExecutionEnabled ?? true),
      threadExecutionReadEnabled: capabilities.threadExecutionReadEnabled === true,
      planDeliveryControlEnabled: present &&
        (capabilities.planDeliveryControlEnabled ?? true),
      approvalControlEnabled: present && (capabilities.approvalControlEnabled ?? true),


	  modelControlEnabled: present && (capabilities.modelControlEnabled ?? true),
	  providerCredentialEnabled: present && (capabilities.providerCredentialEnabled ?? false),
      chatGPTAuthEnabled: present && capabilities.chatGPTAuthEnabled === true,
	  fileEditReviewEnabled: present && (capabilities.fileEditReviewEnabled ?? true),
	  fileEditProposalEnabled: present && (capabilities.fileEditProposalEnabled ?? false),
	  fileEditApplyEnabled: present && (capabilities.fileEditApplyEnabled ?? true),
	  runWakeControlEnabled: present && (capabilities.runWakeControlEnabled ?? true),
	  runWakeExecutionEnabled: present && (capabilities.runWakeExecutionEnabled ?? true),
	  runWakeWorkerEnabled: present && (capabilities.runWakeWorkerEnabled ?? false),
	  scheduledJobControlEnabled: present &&
	    (capabilities.scheduledJobControlEnabled ?? false),
	  scheduledJobWorkerEnabled: present &&
	    (capabilities.scheduledJobWorkerEnabled ?? false),
	  skillInstallationEnabled: present && (capabilities.skillInstallationEnabled ?? true),
	  evidenceAttachmentEnabled: present && (capabilities.evidenceAttachmentEnabled ?? true),
	  verificationEvidenceEnabled: present &&
	    (capabilities.verificationEvidenceEnabled ?? false),
	  uiEvidenceControlEnabled: present &&
	    (capabilities.uiEvidenceControlEnabled ?? false),
	  embeddedAnalyzerExecutionEnabled: present &&
	    (capabilities.embeddedAnalyzerExecutionEnabled ?? false),
	  gitAdvancedControlEnabled: present &&
	    (capabilities.gitAdvancedControlEnabled ?? false),
	  githubReviewControlEnabled: present &&
	    (capabilities.githubReviewControlEnabled ?? false),
	  workspaceCheckpointControlEnabled: present &&
	    (capabilities.workspaceCheckpointControlEnabled ?? false),
	  batchDeliveryControlEnabled: present && capabilities.batchDeliveryControlEnabled === true,
	  batchDeliveryHostValidationEnabled: present &&
	    capabilities.batchDeliveryHostValidationEnabled === true,
	  dockerExecutionEnabled: present &&
	    (capabilities.dockerExecutionEnabled ?? false),
	  agentCodeToolsEnabled: present &&
	    (capabilities.agentCodeToolsEnabled ?? false),
	  codeIntelEnabled: capabilities.codeIntelEnabled ?? false,
    });
  },
  disconnect: () => set({ token: "", controlToken: "", health: null, apiBaseURL: undefined,
    runControlEnabled: false, runCreationEnabled: false,
    workspaceImportEnabled: false,
    standardCodePresetEnabled: false, sessionMessageEnabled: false,
    threadControlEnabled: false,
    executionPermissionControlEnabled: false, workspaceSandboxEnabled: false,
    operatorApprovalEnabled: false,
    browserCDPPermissionControlEnabled: false, fullCDPDebugEnabled: false,
    fullCDPSessionControlEnabled: false,
    dangerFullAccessEnabled: false,
    commandRuntimeEnabled: false, commandRuntimeProtocolAvailable: false,
    commandRuntimeAdapterInstalled: false, commandRuntimeAdapterReady: false,
    sessionSteeringControlEnabled: false,
    runLifecycleEnabled: false, runExecutionEnabled: false,
    threadExecutionReadEnabled: false,
	planDeliveryControlEnabled: false, approvalControlEnabled: false,


	modelControlEnabled: false, providerCredentialEnabled: false, chatGPTAuthEnabled: false,
	fileEditReviewEnabled: false, fileEditProposalEnabled: false, fileEditApplyEnabled: false,
	runWakeControlEnabled: false, runWakeExecutionEnabled: false, runWakeWorkerEnabled: false,
	scheduledJobControlEnabled: false, scheduledJobWorkerEnabled: false,
	skillInstallationEnabled: false,
	evidenceAttachmentEnabled: false,
	verificationEvidenceEnabled: false,
	uiEvidenceControlEnabled: false,
	embeddedAnalyzerExecutionEnabled: false,
	gitAdvancedControlEnabled: false,
	githubReviewControlEnabled: false,
	workspaceCheckpointControlEnabled: false,
	batchDeliveryControlEnabled: false,
	batchDeliveryHostValidationEnabled: false,
	dockerExecutionEnabled: false,
	agentCodeToolsEnabled: false,
	codeIntelEnabled: false,
    ...initialSelection }),
  selectRun: (selectedRunID) => set({ selectedRunID, resourceKind: "run" }),
  selectSession: (selectedSessionID) => set({ selectedSessionID, resourceKind: "session" }),
  selectThread: (selectedThreadID) => set({ selectedThreadID, resourceKind: "thread" }),
  setHealth: (health) => set({ health }),
  setResourceKind: (resourceKind) => set({ resourceKind }),
}));
