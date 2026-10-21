import {
  applicationServiceIdentity,
  parseThreadApplicationService,
  parseThreadApplicationServiceStop,
  parseThreadApplicationServices,
} from "../../application-services";
import {
  parseThreadCommandServicePolicy,
  validCommandServicePolicyRequest,
} from "../../command-service-policy";
import { maximumFileBytes } from "../../file-attachments";
import { normalizedTaskBudget } from "../../task-configuration";
import { APIRequestError } from "../protocol";
import { normalizeRequestedNetworkAuthority, normalizeRequestedWebAccess } from "../responses/permissions";
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
} from "../responses/sessions";
import {
  boundedIdentity,
  boundedText,
  hasExactKeys,
  isSHA256,
  safePositiveInteger,
  validWorkspaceRelativePath,
} from "../validation";
import type { ThreadCommandServicePolicyRequest } from "../../command-service-policy";
import type { ThreadChangeSummary, ThreadChangeUndo } from "../../thread-changes";
import type {
  SessionArchiveControlRequestView,
  SessionArchiveControlView,
  SessionMessageControlRequestView,
  SessionMessageControlView,
  SessionSteeringCancellationRequestView,
  SessionSteeringCancellationView,
  ThreadCreationControlRequestView,
  ThreadCreationControlView,
  ThreadExecutionPermissionControlRequestView,
  ThreadExecutionPermissionControlView,
  ThreadLifecycleControlRequestView,
  ThreadLifecycleControlView,
  ThreadMessageControlRequestView,
  ThreadMessageControlView,
  ThreadRunRecoveryControlRequestView,
  ThreadRunRecoveryControlView,
  ThreadTurnControlRequestView,
  ThreadView,
} from "../../types";
import type { ClientFeatures } from "../capabilities";
import type { APITransport } from "../transport";

export class SessionsClient {
  constructor(
    private readonly transport: APITransport,
    private readonly features: ClientFeatures,
  ) {}

  async renameThread(id: string, title: string, signal?: AbortSignal): Promise<ThreadView> {
    if (!boundedIdentity(id) || !boundedText(title.trim(), 4096) || /[\p{Cc}]/u.test(title)) {
      throw new Error("A conversation identity and a title of 1–4096 characters without control characters are required");
    }
    return parseThreadView(await this.transport.sendControlRequest<unknown>(`/threads/${encodeURIComponent(id)}/name`, { title: title.trim() }, signal, "", "PUT"), id);
  }

  async pinThread(id: string, pinned: boolean, signal?: AbortSignal): Promise<ThreadView> {
    if (!boundedIdentity(id) || typeof pinned !== "boolean") throw new Error("A conversation identity and pinned state are required");
    return parseThreadView(await this.transport.sendControlRequest<unknown>(`/threads/${encodeURIComponent(id)}/pin`, { pinned }, signal, "", "PUT"), id);
  }

  async getThreadCommandServicePolicy(threadID: string, signal?: AbortSignal) {
    if (!applicationServiceIdentity(threadID)) throw new Error("An exact Thread is required for development service settings");
    return parseThreadCommandServicePolicy(await this.transport.get<unknown>(
      `/threads/${encodeURIComponent(threadID)}/command-service-policy`, {}, signal), threadID);
  }

  async setThreadCommandServicePolicy(threadID: string, body: ThreadCommandServicePolicyRequest, signal?: AbortSignal) {
    if (!this.features.hasExecutionPermissionControl || !applicationServiceIdentity(threadID) || !validCommandServicePolicyRequest(body)) {
      throw new Error("Development service settings require execution permission control access, an exact Thread and its current revision");
    }
    const result = parseThreadCommandServicePolicy(await this.transport.sendControlRequest<unknown>(
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
    return parseThreadApplicationServices(await this.transport.get<unknown>(
      `/threads/${encodeURIComponent(threadID)}/application-services`, { limit }, signal), threadID);
  }

  async getThreadApplicationService(threadID: string, jobID: string, signal?: AbortSignal) {
    if (!applicationServiceIdentity(threadID) || !applicationServiceIdentity(jobID)) {
      throw new Error("An exact Thread and application Job are required");
    }
    return parseThreadApplicationService(await this.transport.get<unknown>(
      `/threads/${encodeURIComponent(threadID)}/application-services/${encodeURIComponent(jobID)}`, {}, signal), threadID, jobID);
  }

  async stopThreadApplicationService(threadID: string, runID: string, jobID: string, signal?: AbortSignal) {
    if (!this.features.hasRunExecution || !applicationServiceIdentity(threadID) || !applicationServiceIdentity(runID) ||
      !applicationServiceIdentity(jobID)) {
      throw new Error("Application service cleanup requires control access and an exact Thread, Run and Job");
    }
    return parseThreadApplicationServiceStop(await this.transport.sendControl<unknown>(
      `/threads/${encodeURIComponent(threadID)}/application-services/${encodeURIComponent(jobID)}/stop`,
      { version: "thread_application_services.v1", expected_run_id: runID },
      `application-stop-${jobID}`, signal), threadID, runID, jobID);
  }

  async createThread(body: ThreadCreationControlRequestView, idempotencyKey: string,
    signal?: AbortSignal): Promise<ThreadCreationControlView> {
    const routePairPresent = body.provider !== undefined && body.model !== undefined;
    const routePairAbsent = body.provider === undefined && body.model === undefined;
    if (!this.features.hasThreadControl || !this.features.hasRunCreation || !this.features.hasSessionMessages ||
      body.version !== "thread_creation.v1" || (!routePairPresent && !routePairAbsent) ||
      (routePairPresent && (!boundedIdentity(body.provider) || !boundedIdentity(body.model) ||
        body.provider!.includes("/") || body.model!.includes("/")))) {
      throw new Error("Thread creation capability and a normalized Provider/model pair are required");
    }
    normalizedTaskBudget(body.budget);
    normalizeRequestedNetworkAuthority(body);
    normalizeRequestedWebAccess(body);
    return parseThreadCreationControl(await this.transport.sendControl<unknown>("/threads", body,
      idempotencyKey, signal), body);
  }

  async submitThreadMessage(threadID: string, body: ThreadMessageControlRequestView,
    idempotencyKey: string, signal?: AbortSignal): Promise<ThreadMessageControlView> {
    if (!this.features.hasThreadControl || !this.features.hasSessionMessages || !boundedIdentity(threadID) ||
      threadID.trim() !== threadID || body.version !== "thread_message_submission.v1") {
      throw new Error("Thread message capability and a normalized Thread are required");
    }
    return parseThreadMessageControl(await this.transport.sendControl<unknown>(
      `/threads/${encodeURIComponent(threadID)}/messages`, body, idempotencyKey, signal,
    ), threadID);
  }

  async threadExecution(threadID: string, signal?: AbortSignal): Promise<import("../../types").ThreadExecutionView> {
    return parseThreadExecution(await this.transport.get<unknown>(
      `/threads/${encodeURIComponent(threadID)}/execution`, {}, signal), threadID);
  }

  async threadChanges(threadID: string, runID: string, sourceEventID: string,
    signal?: AbortSignal): Promise<ThreadChangeSummary> {
    const value = await this.transport.get<ThreadChangeSummary>(`/threads/${encodeURIComponent(threadID)}/changes`,
      { source_event_id: sourceEventID }, signal);
    if (value.version !== "thread_change_summary.v1" || value.thread_id !== threadID ||
      value.run_id !== runID || value.source_event_id !== sourceEventID) {
      throw new Error("The change summary does not match this reply.");
    }
    return value;
  }

  async undoThreadChanges(threadID: string, runID: string, sourceEventID: string,
    confirm: boolean, signal?: AbortSignal): Promise<ThreadChangeUndo> {
    if (!this.features.hasWorkspaceCheckpointControl || !boundedIdentity(threadID) ||
      !boundedIdentity(runID) || !boundedIdentity(sourceEventID)) {
      throw new Error("Workspace restore authority and an exact reply are required.");
    }
    const value = await this.transport.sendControl<ThreadChangeUndo>(`/threads/${encodeURIComponent(threadID)}/changes/undo`,
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
    return (await this.transport.request<unknown>("/threads/creation-request", { workspace_id: workspaceID },
      signal, operationKey)).data;
  }

  async inspectThreadTurnRequest(threadID: string, operationKey: string,
    signal?: AbortSignal): Promise<unknown> {
    if (!boundedIdentity(threadID) || threadID.trim() !== threadID) {
      throw new Error("A normalized Thread is required");
    }
    return (await this.transport.request<unknown>(`/threads/${encodeURIComponent(threadID)}/turn-request`, {},
      signal, operationKey)).data;
  }

  async inspectThreadPlanRequest(threadID: string, runID: string, action: "enter_plan" | "enter_deliver" | "confirm",
    operationKey: string, signal?: AbortSignal): Promise<unknown> {
    if (!boundedIdentity(threadID) || threadID.trim() !== threadID ||
      !boundedIdentity(runID) || runID.trim() !== runID) throw new Error("A normalized Thread and Run are required");
    return (await this.transport.request<unknown>(`/threads/${encodeURIComponent(threadID)}/plan`,
      { run_id: runID, action }, signal, operationKey)).data;
  }

  async interruptThread(threadID: string, executionID: string, idempotencyKey: string,
    signal?: AbortSignal): Promise<import("../../types").ThreadExecutionView> {
    if (!this.features.hasThreadControl || !this.features.hasRunExecution || !boundedIdentity(threadID) ||
      !boundedIdentity(executionID)) throw new Error("A current controllable Thread execution is required");
    return parseThreadExecution(await this.transport.sendControl<unknown>(
      `/threads/${encodeURIComponent(threadID)}/interrupt`, {
        version: "thread_execution.v1", execution_id: executionID,
      }, idempotencyKey, signal), threadID);
  }

  async submitThreadTurn(threadID: string, body: ThreadTurnControlRequestView,
    idempotencyKey: string, signal?: AbortSignal): Promise<ThreadMessageControlView> {
    if (!this.features.hasThreadControl || !this.features.hasSessionMessages || !boundedIdentity(threadID) ||
      threadID.trim() !== threadID || body.version !== "thread_message_submission.v1") {
      throw new Error("Thread turn capability and a normalized Thread are required");
    }
    if (body.files !== undefined && (!this.features.hasEvidenceAttachment || !Array.isArray(body.files) ||
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
      return parseThreadMessageControl(await this.transport.sendControl<unknown>(
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
    if (!this.features.hasThreadControl || !this.features.hasSessionMessages || !boundedIdentity(threadID) ||
      threadID.trim() !== threadID || body.version !== "thread_run_recovery.v1" ||
      !boundedIdentity(body.run_id) || !boundedIdentity(body.handoff_operation_id)) {
      throw new Error("Thread recovery capability and exact failed boundary identities are required");
    }
    return parseThreadRunRecoveryControl(await this.transport.sendControl<unknown>(
      `/threads/${encodeURIComponent(threadID)}/recovery`, body, idempotencyKey, signal,
    ), threadID, body);
  }

  async getThreadExecutionPermission(threadID: string,
    signal?: AbortSignal): Promise<ThreadExecutionPermissionControlView> {
    if (!boundedIdentity(threadID) || threadID.trim() !== threadID) {
      throw new Error("A normalized Thread is required");
    }
    return parseThreadApprovalPreference(await this.transport.get<unknown>(
      `/threads/${encodeURIComponent(threadID)}/execution-permission`, {}, signal), threadID);
  }

  async changeThreadExecutionPermission(threadID: string,
    body: ThreadExecutionPermissionControlRequestView, idempotencyKey: string,
    signal?: AbortSignal): Promise<ThreadExecutionPermissionControlView> {
    if (!this.features.hasExecutionPermissionControl || !boundedIdentity(threadID) ||
      threadID.trim() !== threadID) {
      throw new Error("Thread execution permission capability is required");
    }
    if (!["ask", "auto", "full"].includes(body.mode) ||
      body.confirm_full !== (body.mode === "full") ||
      Object.keys(body).some((key) => !["mode", "confirm_full", "reason"].includes(key))) {
      throw new Error("An exact ask/auto/full selection and explicit Full confirmation are required");
    }
    return parseThreadApprovalPreference(await this.transport.sendControl<unknown>(
      `/threads/${encodeURIComponent(threadID)}/execution-permission`, body,
      idempotencyKey, signal), threadID);
  }

  async moveThreadToProject(threadID: string, projectWorkspaceID: string, expectedVersion: number,
    idempotencyKey: string, signal?: AbortSignal): Promise<{ thread: ThreadView; replayed: boolean }> {
    if (!this.features.hasThreadControl || !boundedIdentity(threadID) ||
      (projectWorkspaceID !== "" && !boundedIdentity(projectWorkspaceID)) || !safePositiveInteger(expectedVersion)) {
      throw new Error("A conversation, project and current project assignment version are required");
    }
    const result = await this.transport.sendControl<unknown>(`/threads/${encodeURIComponent(threadID)}/project`, {
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
    if (!this.features.hasThreadControl || !this.features.hasSessionMessages ||
      !boundedIdentity(threadID) || threadID.trim() !== threadID ||
      body.version !== "thread_lifecycle.v1" || !Number.isSafeInteger(body.expected_version) ||
      body.expected_version < 1) {
      throw new Error("Thread lifecycle capability and a current Thread version are required");
    }
    return parseThreadLifecycleControl(await this.transport.sendControl<unknown>(
      `/threads/${encodeURIComponent(threadID)}/${action}`, body, idempotencyKey, signal,
    ), threadID, action, body.expected_version);
  }

  async submitSessionMessage(sessionID: string, body: SessionMessageControlRequestView,
    idempotencyKey: string, signal?: AbortSignal): Promise<SessionMessageControlView> {
    if (!this.features.hasSessionMessages) {
      throw new Error("Run messaging capability is required for this operation");
    }
    const normalizedSessionID = boundedIdentity(sessionID);
    if (!normalizedSessionID || normalizedSessionID !== sessionID) {
      throw new Error("A normalized Run-local Session identity is required");
    }
    const result = await this.transport.sendControl<unknown>(
      `/sessions/${encodeURIComponent(sessionID)}/messages`, body, idempotencyKey, signal,
    );
    return parseSessionMessageControl(result, sessionID);
  }

  async inspectSessionMessageOperation(sessionID: string, operationKey: string,
    signal?: AbortSignal): Promise<unknown> {
    if (!this.features.hasSessionMessages || !boundedIdentity(sessionID) || sessionID.trim() !== sessionID ||
      operationKey.length < 16 || operationKey.length > 256 || /[\s\u0000-\u001f\u007f]/u.test(operationKey)) {
      throw new Error("A Run-local Session and normalized original operation key are required");
    }
    return (await this.transport.request<unknown>(
      `/sessions/${encodeURIComponent(sessionID)}/messages/operations/${encodeURIComponent(operationKey)}`,
      {}, signal)).data;
  }

  async archiveSession(sessionID: string, body: SessionArchiveControlRequestView,
    signal?: AbortSignal): Promise<SessionArchiveControlView> {
    if (!this.features.hasSessionMessages || !boundedIdentity(sessionID) || sessionID.trim() !== sessionID ||
      body.version !== "session_archive.v1" || body.confirm !== true) {
      throw new Error("Confirmed Run-local Session archive capability and identity are required");
    }
    return parseSessionArchiveControl(await this.transport.sendControlRequest<unknown>(
      `/sessions/${encodeURIComponent(sessionID)}/archive`, body, signal,
    ), sessionID);
  }

  async cancelSessionSteering(sessionID: string, messageID: string,
    body: SessionSteeringCancellationRequestView, idempotencyKey: string,
    signal?: AbortSignal): Promise<SessionSteeringCancellationView> {
    if (!this.features.hasSessionSteeringControl) {
      throw new Error("Run-local Session steering cancellation capability is required for this operation");
    }
    const normalizedSessionID = boundedIdentity(sessionID);
    const normalizedMessageID = boundedIdentity(messageID);
    if (!normalizedSessionID || normalizedSessionID !== sessionID ||
      !normalizedMessageID || normalizedMessageID !== messageID) {
      throw new Error("Normalized Run-local Session and steering identities are required");
    }
    const result = await this.transport.sendControl<unknown>(
      `/sessions/${encodeURIComponent(sessionID)}/messages/${encodeURIComponent(messageID)}/cancel`,
      body, idempotencyKey, signal,
    );
    return parseSessionSteeringCancellation(result, sessionID, messageID);
  }
}
