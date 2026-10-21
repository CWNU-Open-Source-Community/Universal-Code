import { APIRequestError, parseResponsePage } from "../protocol";
import {
  parseRunWakeControl,
  parseRunWakeExecution,
  parseRunWakeState,
  parseScheduledJobControl,
  parseScheduledJobDetail,
  parseScheduledJobList,
} from "../responses/scheduling";
import { boundedIdentity, safePositiveInteger } from "../validation";
import type {
  Page,
  RunWakeCancelRequestView,
  RunWakeControlView,
  RunWakeExecutionRequestView,
  RunWakeExecutionView,
  RunWakeScheduleRequestView,
  RunWakeStateView,
  ScheduledJobControlView,
  ScheduledJobCreateRequestView,
  ScheduledJobDetailView,
  ScheduledJobListView,
  ScheduledJobObservationRequestView,
  ScheduledJobTransitionRequestView,
} from "../../types";
import type { ClientFeatures } from "../capabilities";
import type { APITransport } from "../transport";

export class SchedulingClient {
  constructor(
    private readonly transport: APITransport,
    private readonly features: ClientFeatures,
  ) {}

  async runWakeState(runID: string, signal?: AbortSignal): Promise<RunWakeStateView> {
    if (!boundedIdentity(runID) || runID.trim() !== runID) {
      throw new Error("A normalized Run identity is required");
    }
    return parseRunWakeState(await this.transport.get<unknown>(
      `/runs/${encodeURIComponent(runID)}/wake-intent`, {}, signal,
    ), runID);
  }

  async scheduleRunWake(runID: string, body: RunWakeScheduleRequestView,
    idempotencyKey: string, signal?: AbortSignal): Promise<RunWakeControlView> {
    if (!this.features.hasRunWakeControl) {
      throw new Error("Run wake control capability is required");
    }
    return parseRunWakeControl(await this.transport.sendControl<unknown>(
      `/runs/${encodeURIComponent(runID)}/wake-intent`, body, idempotencyKey, signal,
    ), runID, "schedule");
  }

  async cancelRunWake(runID: string, body: RunWakeCancelRequestView,
    idempotencyKey: string, signal?: AbortSignal): Promise<RunWakeControlView> {
    if (!this.features.hasRunWakeControl) {
      throw new Error("Run wake control capability is required");
    }
    return parseRunWakeControl(await this.transport.sendControl<unknown>(
      `/runs/${encodeURIComponent(runID)}/wake-intent/cancel`, body, idempotencyKey, signal,
    ), runID, "cancel");
  }

  async consumeRunWake(runID: string, body: RunWakeExecutionRequestView,
    signal?: AbortSignal): Promise<RunWakeExecutionView> {
    if (!this.features.hasRunWakeExecution || !boundedIdentity(runID) || runID.trim() !== runID) {
      throw new Error("Foreground Run wake capability and a normalized Run are required");
    }
    return parseRunWakeExecution(await this.transport.sendControlRequest<unknown>(
      `/runs/${encodeURIComponent(runID)}/wake-intent/consume`, body, signal,
    ), runID);
  }

  async listScheduledJobs(runID = "", limit = 50,
    signal?: AbortSignal, cursor = ""): Promise<ScheduledJobListView & { page: Page }> {
    if ((runID !== "" && boundedIdentity(runID) !== runID) ||
      !Number.isSafeInteger(limit) || limit < 1 || limit > 100) {
      throw new Error("A normalized Run identity and a schedule limit between 1 and 100 are required");
    }
    const envelope = await this.transport.request<unknown>("/scheduled-jobs", {
      run_id: runID || undefined, limit, cursor: cursor || undefined,
    }, signal);
    return { ...parseScheduledJobList(envelope.data), page: parseResponsePage(envelope.page, limit) };
  }

  async getScheduledJob(jobID: string, signal?: AbortSignal): Promise<ScheduledJobDetailView> {
    if (boundedIdentity(jobID) !== jobID) {
      throw new Error("A normalized scheduled job identity is required");
    }
    return parseScheduledJobDetail(await this.transport.get<unknown>(
      `/scheduled-jobs/${encodeURIComponent(jobID)}`,
      { round_limit: 20, notification_limit: 20 }, signal,
    ), jobID);
  }

  async createScheduledJob(runID: string, body: ScheduledJobCreateRequestView,
    idempotencyKey: string, signal?: AbortSignal): Promise<ScheduledJobControlView> {
    if (!this.features.hasScheduledJobControl || boundedIdentity(runID) !== runID ||
      body.version !== "scheduled-job.v1") {
      throw new Error("Scheduled job control and a normalized Run identity are required");
    }
    return parseScheduledJobControl(await this.transport.sendControl<unknown>(
      `/runs/${encodeURIComponent(runID)}/scheduled-jobs`, body, idempotencyKey, signal,
    ), runID, "", "create");
  }

  async transitionScheduledJob(runID: string, jobID: string,
    action: "pause" | "resume" | "cancel", body: ScheduledJobTransitionRequestView,
    idempotencyKey: string, signal?: AbortSignal): Promise<ScheduledJobControlView> {
    if (!this.features.hasScheduledJobControl || boundedIdentity(runID) !== runID ||
      boundedIdentity(jobID) !== jobID || body.version !== "scheduled-job-control.v1" ||
      !Number.isSafeInteger(body.expected_revision) || body.expected_revision < 1) {
      throw new Error("Scheduled job control, normalized identities, and a revision are required");
    }
    return parseScheduledJobControl(await this.transport.sendControl<unknown>(
      `/runs/${encodeURIComponent(runID)}/scheduled-jobs/${encodeURIComponent(jobID)}/${action}`,
      body, idempotencyKey, signal,
    ), runID, jobID, action);
  }

  async enableScheduledJobObservation(runID: string, jobID: string,
    body: ScheduledJobObservationRequestView, idempotencyKey: string,
    signal?: AbortSignal): Promise<ScheduledJobControlView> {
    if (!this.features.hasScheduledJobControl || boundedIdentity(runID) !== runID ||
      boundedIdentity(jobID) !== jobID || body.version !== "scheduled-job-control.v1" ||
      body.observation_consent_version !== 1 || !safePositiveInteger(body.expected_revision)) {
      throw new Error("Scheduled observation requires exact identities, a revision, and consent");
    }
    const result = parseScheduledJobControl(await this.transport.sendControl<unknown>(
      `/runs/${encodeURIComponent(runID)}/scheduled-jobs/${encodeURIComponent(jobID)}/enable-observation`,
      body, idempotencyKey, signal,
    ), runID, jobID, "enable-observation");
    if (result.job.observation_consent_version !== 1) {
      throw new APIRequestError("Scheduled observation consent was not confirmed", "INVALID_RESPONSE", 502);
    }
    return result;
  }
}
