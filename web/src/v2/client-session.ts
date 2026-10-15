import { useMemo } from "react";
import { useShallow } from "zustand/react/shallow";
import { APIClient, type ClientCapabilities } from "../api/client";
import { useConnectionStore } from "../state/connection";

type ConnectionSnapshot = ReturnType<typeof useConnectionStore.getState>;
type ClientConnection = Pick<ConnectionSnapshot, "apiBaseURL" | "token" | "controlToken"> &
  ClientCapabilities;

function capabilities(state: ClientCapabilities): ClientCapabilities {
  return {
    runControlEnabled: state.runControlEnabled,
    workspaceImportEnabled: state.workspaceImportEnabled,
    executionPermissionControlEnabled: state.executionPermissionControlEnabled,
    workspaceSandboxEnabled: state.workspaceSandboxEnabled,
    browserCDPPermissionControlEnabled: state.browserCDPPermissionControlEnabled,
    fullCDPDebugEnabled: state.fullCDPDebugEnabled,
    fullCDPSessionControlEnabled: state.fullCDPSessionControlEnabled,
    operatorApprovalEnabled: state.operatorApprovalEnabled,
    dangerFullAccessEnabled: state.dangerFullAccessEnabled,

    commandRuntimeEnabled: state.commandRuntimeEnabled,
    commandRuntimeProtocolAvailable: state.commandRuntimeProtocolAvailable,
    commandRuntimeAdapterInstalled: state.commandRuntimeAdapterInstalled,
    commandRuntimeAdapterReady: state.commandRuntimeAdapterReady,
    runCreationEnabled: state.runCreationEnabled,
    standardCodePresetEnabled: state.standardCodePresetEnabled,
    sessionMessageEnabled: state.sessionMessageEnabled,
    threadControlEnabled: state.threadControlEnabled,
    sessionSteeringControlEnabled: state.sessionSteeringControlEnabled,
    runLifecycleEnabled: state.runLifecycleEnabled,
    runExecutionEnabled: state.runExecutionEnabled,
    threadExecutionReadEnabled: state.threadExecutionReadEnabled,
    planDeliveryControlEnabled: state.planDeliveryControlEnabled,
    approvalControlEnabled: state.approvalControlEnabled,


    modelControlEnabled: state.modelControlEnabled,
    providerCredentialEnabled: state.providerCredentialEnabled,
    chatGPTAuthEnabled: state.chatGPTAuthEnabled,
    fileEditReviewEnabled: state.fileEditReviewEnabled,
    fileEditProposalEnabled: state.fileEditProposalEnabled,
    fileEditApplyEnabled: state.fileEditApplyEnabled,
    runWakeControlEnabled: state.runWakeControlEnabled,
    runWakeExecutionEnabled: state.runWakeExecutionEnabled,
    runWakeWorkerEnabled: state.runWakeWorkerEnabled,
    scheduledJobControlEnabled: state.scheduledJobControlEnabled,
    scheduledJobWorkerEnabled: state.scheduledJobWorkerEnabled,
    skillInstallationEnabled: state.skillInstallationEnabled,
    evidenceAttachmentEnabled: state.evidenceAttachmentEnabled,
    verificationEvidenceEnabled: state.verificationEvidenceEnabled,
    uiEvidenceControlEnabled: state.uiEvidenceControlEnabled,
    embeddedAnalyzerExecutionEnabled: state.embeddedAnalyzerExecutionEnabled,
    workspaceCheckpointControlEnabled: state.workspaceCheckpointControlEnabled,
    gitAdvancedControlEnabled: state.gitAdvancedControlEnabled,
    githubReviewControlEnabled: state.githubReviewControlEnabled,
    batchDeliveryControlEnabled: state.batchDeliveryControlEnabled,
    batchDeliveryHostValidationEnabled: state.batchDeliveryHostValidationEnabled,
    dockerExecutionEnabled: state.dockerExecutionEnabled,
    agentCodeToolsEnabled: state.agentCodeToolsEnabled,
    codeIntelEnabled: state.codeIntelEnabled,
  };
}

export function createV2Client(state: ClientConnection): APIClient {
  return new APIClient(state.token, state.apiBaseURL, state.controlToken, capabilities(state));
}

function clientConnection(state: ConnectionSnapshot): ClientConnection {
  return {
    apiBaseURL: state.apiBaseURL,
    token: state.token,
    controlToken: state.controlToken,
    ...capabilities(state),
  };
}

export function useV2Client(): APIClient {
  const connection = useConnectionStore(useShallow(clientConnection));
  return useMemo(() => createV2Client(connection), [connection]);
}
