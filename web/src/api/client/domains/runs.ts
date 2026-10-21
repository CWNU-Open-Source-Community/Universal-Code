import { consumeSSE } from "../../sse";
import { normalizedTaskBudget } from "../../task-configuration";
import { APIRequestError } from "../protocol";
import {
  normalizeRequestedNetworkAuthority,
  normalizeRequestedWebAccess,
  parseRunNetworkAuthorityControl,
  parseRunWebAccessControl,
} from "../responses/permissions";
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
} from "../responses/runs";
import { boundedIdentity, boundedText, safePositiveInteger } from "../validation";
import type {
  ChildTaskAdmitRequestView,
  ChildTaskProposalView,
  ChildTaskProposalsListView,
  ChildTaskReviewRequestView,
  FanoutExecutionCancelRequestView,
  FanoutExecutionView,
  FanoutExecutionsListView,
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
  RunWebAccessControlRequestView,
  RunWebAccessControlView,
} from "../../types";
import type { ClientFeatures } from "../capabilities";
import type { APITransport } from "../transport";

export class RunsClient {
  constructor(
    private readonly transport: APITransport,
    private readonly features: ClientFeatures,
  ) {}

  async createRun(body: RunCreationControlRequestView, idempotencyKey: string,
    signal?: AbortSignal): Promise<RunCreationControlView> {
    if (!this.features.hasRunCreation) {
      throw new Error("Run creation capability is required for this operation");
    }
    normalizedTaskBudget(body.budget);
    normalizeRequestedNetworkAuthority(body);
    normalizeRequestedWebAccess(body);
    const result = await this.transport.sendControl<unknown>("/runs", body, idempotencyKey, signal);
    return parseRunCreationControl(result, body);
  }

  async setRunWebAccess(runID: string,
    body: RunWebAccessControlRequestView, idempotencyKey: string,
    signal?: AbortSignal): Promise<RunWebAccessControlView> {
    if (!this.features.hasControl || !boundedIdentity(runID) || runID.trim() !== runID ||
      body.version !== "run_web_access_control.v1" ||
      !safePositiveInteger(body.expected_mode_revision) ||
      (body.reason !== undefined && body.reason !== "" && (!boundedText(body.reason, 1_024) ||
        /[\u0000-\u001f\u007f]/u.test(body.reason)))) {
      throw new Error("请选择当前任务的网页访问方式后重试。");
    }
    const selection = normalizeRequestedWebAccess(body);
    if (selection === null) throw new Error("请选择网页访问方式。");
    const request = { ...body, web_access_mode: selection.mode, web_allowed_targets: selection.targets };
    return parseRunWebAccessControl(await this.transport.sendControl<unknown>(
      `/runs/${encodeURIComponent(runID)}/web-access`, request, idempotencyKey, signal), runID, request);
  }

  async expandRunNetworkAuthority(runID: string,
    body: RunNetworkAuthorityControlRequestView, idempotencyKey: string,
    signal?: AbortSignal): Promise<RunNetworkAuthorityControlView> {
    if (!this.features.hasControl || !boundedIdentity(runID) || runID.trim() !== runID ||
      body.version !== "run_network_authority_control.v1" ||
      !safePositiveInteger(body.expected_mode_revision) ||
      (body.reason !== undefined && body.reason !== "" && (!boundedText(body.reason, 1_024) ||
        /[\u0000-\u001f\u007f]/u.test(body.reason)))) {
      throw new Error("A revision-bound exact Run network authority request is required");
    }
    normalizeRequestedNetworkAuthority({ network_mode: "allowlist",
      allowed_targets: body.add_allowed_targets });
    return parseRunNetworkAuthorityControl(await this.transport.sendControl<unknown>(
      `/runs/${encodeURIComponent(runID)}/network-authority`, body,
      idempotencyKey, signal), runID, body);
  }

  async controlRunLifecycle(runID: string, body: RunLifecycleControlRequestView,
    idempotencyKey: string, signal?: AbortSignal): Promise<RunLifecycleControlView> {
    if (!this.features.hasRunLifecycle) {
      throw new Error("Run lifecycle capability is required for this operation");
    }
    const normalizedRunID = boundedIdentity(runID);
    if (!normalizedRunID || normalizedRunID !== runID) {
      throw new Error("A normalized Run identity is required");
    }
    const result = await this.transport.sendControl<unknown>(
      `/runs/${encodeURIComponent(runID)}/lifecycle`, body, idempotencyKey, signal,
    );
    return parseRunLifecycleControl(result, runID, body);
  }

  async executeRun(runID: string, body: RunExecutionControlRequestView,
    idempotencyKey: string, signal?: AbortSignal): Promise<RunExecutionControlView> {
    if (!this.features.hasRunExecution) {
      throw new Error("Run execution capability is required for this operation");
    }
    const normalizedRunID = boundedIdentity(runID);
    if (!normalizedRunID || normalizedRunID !== runID) {
      throw new Error("A normalized Run identity is required");
    }
    const result = await this.transport.sendControl<unknown>(
      `/runs/${encodeURIComponent(runID)}/execute`, body, idempotencyKey, signal,
    );
    return parseRunExecutionControl(result, runID, body);
  }

  async getRunFanoutExecutions(runID: string, planID: string,
    signal?: AbortSignal): Promise<FanoutExecutionsListView> {
    if (!boundedIdentity(runID) || !boundedIdentity(planID)) {
      throw new Error("Normalized Run and Fan-out plan identities are required");
    }
    const result = await this.transport.get<unknown>(
      `/runs/${encodeURIComponent(runID)}/fanout-executions?plan_id=${encodeURIComponent(planID)}`,
      {}, signal,
    );
    return parseFanoutExecutions(result, runID, planID);
  }

  async cancelRunFanoutExecution(runID: string, executionID: string,
    body: FanoutExecutionCancelRequestView, idempotencyKey: string,
    signal?: AbortSignal): Promise<FanoutExecutionView> {
    if (!this.features.hasControl) {
      throw new Error("A control bearer token is required for this operation");
    }
    if (!boundedIdentity(runID) || !boundedIdentity(executionID) ||
      body.version !== "readonly_fanout_cancel.v1" || body.confirm_cancel !== true) {
      throw new Error("Normalized Run/execution identities and explicit confirmation are required");
    }
    const result = await this.transport.sendControl<unknown>(
      `/runs/${encodeURIComponent(runID)}/fanout-executions/${encodeURIComponent(executionID)}/cancel`,
      body, idempotencyKey, signal,
    );
    return parseFanoutExecution(result);
  }

  async getRunChildTaskProposals(runID: string,
    signal?: AbortSignal): Promise<ChildTaskProposalsListView> {
    if (!boundedIdentity(runID)) {
      throw new Error("A normalized Run identity is required");
    }
    const result = await this.transport.get<unknown>(
      `/runs/${encodeURIComponent(runID)}/child-task-proposals`, {}, signal,
    );
    return parseChildTaskProposals(result, runID);
  }

  async reviewRunChildTaskProposal(runID: string, proposalID: string,
    body: ChildTaskReviewRequestView, idempotencyKey: string,
    signal?: AbortSignal): Promise<ChildTaskProposalView> {
    if (!this.features.hasControl) {
      throw new Error("A control bearer token is required for this operation");
    }
    if (!boundedIdentity(runID) || !boundedIdentity(proposalID) ||
      body.version !== "child_task_review.v1" || body.confirm_review !== true ||
      (body.action !== "approve" && body.action !== "deny")) {
      throw new Error("A normalized proposal and explicit confirmed review are required");
    }
    const result = await this.transport.sendControl<unknown>(
      `/runs/${encodeURIComponent(runID)}/child-task-proposals/${encodeURIComponent(proposalID)}/review`,
      body, idempotencyKey, signal,
    );
    return parseChildTaskProposal(result, runID);
  }

  async admitRunChildTaskProposal(runID: string, proposalID: string,
    body: ChildTaskAdmitRequestView, idempotencyKey: string,
    signal?: AbortSignal): Promise<ChildTaskProposalView> {
    if (!this.features.hasControl) {
      throw new Error("A control bearer token is required for this operation");
    }
    if (!boundedIdentity(runID) || !boundedIdentity(proposalID) ||
      body.version !== "child_task_admit.v1" || body.confirm_admit !== true) {
      throw new Error("A normalized proposal and explicit confirmed admission are required");
    }
    const result = await this.transport.sendControl<unknown>(
      `/runs/${encodeURIComponent(runID)}/child-task-proposals/${encodeURIComponent(proposalID)}/admit`,
      body, idempotencyKey, signal,
    );
    return parseChildTaskProposal(result, runID);
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
    const headers = this.transport.headers();
    headers.Accept = "text/event-stream";
    if (options.cursor) {
      headers["Last-Event-ID"] = options.cursor;
    }
    const response = await fetch(this.transport.url(`/runs/${encodeURIComponent(runID)}/events/stream`), {
      method: "GET",
      headers,
      signal: options.signal,
      cache: "no-store",
      credentials: "omit",
      referrerPolicy: "no-referrer",
    });
    if (!response.ok) {
      throw await this.transport.responseError(response);
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
    const envelope = await this.transport.request<unknown>(
      `/runs/${encodeURIComponent(runID)}/events/poll`,
      { cursor: cursor || undefined, limit },
      signal,
    );
    return parseEventPoll(envelope.data, runID, envelope.request_id);
  }
}
