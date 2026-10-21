import { parseStandardCodeDelivery } from "../../standard-code-delivery";
import { APIRequestError } from "../protocol";
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
} from "../responses/delivery";
import { parseCodeHandoff, parseCodeHandoffExport } from "../responses/handoffs";
import { boundedIdentity, boundedText, hasExactKeys, isRecord, safePositiveInteger } from "../validation";
import type {
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
  CodeHandoffExportView,
  CodeHandoffView,
  StandardCodeDeliveryRecordRequestView,
  StandardCodeDeliveryRecordResultView,
  StandardCodeDeliveryView,
} from "../../types";
import type { ClientFeatures } from "../capabilities";
import type { APITransport } from "../transport";

export class DeliveryClient {
  constructor(
    private readonly transport: APITransport,
    private readonly features: ClientFeatures,
  ) {}

  async codeHandoff(runID: string, signal?: AbortSignal): Promise<CodeHandoffView> {
    if (!boundedIdentity(runID) || runID.trim() !== runID) {
      throw new Error("A normalized Run identity is required");
    }
    return parseCodeHandoff(await this.transport.get<unknown>(
      `/runs/${encodeURIComponent(runID)}/code-handoff`, {}, signal,
    ), runID);
  }

  async standardCodeDelivery(runID: string,
    signal?: AbortSignal): Promise<StandardCodeDeliveryView> {
    if (!boundedIdentity(runID) || runID.trim() !== runID) {
      throw new Error("A normalized Run identity is required");
    }
    return parseStandardCodeDelivery(await this.transport.get<unknown>(
      `/runs/${encodeURIComponent(runID)}/standard-code-delivery`, {}, signal,
    ), runID);
  }

  async recordStandardCodeDelivery(runID: string, body: StandardCodeDeliveryRecordRequestView,
    idempotencyKey: string, signal?: AbortSignal): Promise<StandardCodeDeliveryRecordResultView> {
    if (!this.features.hasWorkspaceCheckpointControl || !boundedIdentity(runID) ||
      runID.trim() !== runID || typeof body.operation_key !== "string" ||
      body.operation_key.trim() !== body.operation_key || body.operation_key.length === 0 ||
      !Array.isArray(body.verification_job_ids) || !Array.isArray(body.uncovered_items)) {
      throw new Error("Standard Code delivery control and a bounded exact intent are required");
    }
    const value = await this.transport.sendControl<unknown>(
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
    return parseCodeHandoffExport(await this.transport.get<unknown>(
      `/runs/${encodeURIComponent(runID)}/code-handoff/export`, { format }, signal,
    ), runID, format);
  }

  async getRunBatchDeliveries(runID: string,
    signal?: AbortSignal): Promise<BatchDeliveriesListView> {
    if (!boundedIdentity(runID)) {
      throw new Error("A normalized Run identity is required");
    }
    return parseBatchDeliveries(await this.transport.get<unknown>(
      `/runs/${encodeURIComponent(runID)}/batch-deliveries`, {}, signal,
    ), runID);
  }

  async getRunBatchDelivery(runID: string, planID: string,
    signal?: AbortSignal): Promise<BatchDeliverySnapshotView> {
    if (!boundedIdentity(runID) || !boundedIdentity(planID)) {
      throw new Error("Normalized Run and batch delivery identities are required");
    }
    return parseBatchDeliverySnapshot(await this.transport.get<unknown>(
      `/runs/${encodeURIComponent(runID)}/batch-deliveries/${encodeURIComponent(planID)}`,
      {}, signal,
    ), runID, planID);
  }

  async getBatchWorkbench(runID: string, planID: string, signal?: AbortSignal): Promise<BatchWorkbenchView> {
    if (!boundedIdentity(runID) || !boundedIdentity(planID)) throw new Error("Normalized Run and batch identities are required");
    return parseBatchWorkbench(await this.transport.get<unknown>(
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
    const value = await this.transport.sendControl<unknown>(`/runs/${encodeURIComponent(runID)}/batch-deliveries/prepare-workbench`, body, idempotencyKey, signal);
    return parseBatchWorkbench(value, runID);
  }

  async recoverBatchWorkbenchOwner(runID: string, planID: string, ordinal: number,
    body: BatchWorkbenchOwnerRequestView, idempotencyKey: string, signal?: AbortSignal): Promise<BatchWorkbenchView> {
    if (new TextEncoder().encode(idempotencyKey).byteLength > 224) throw new Error("Batch workbench operation key is too long");
    this.requireBatchDeliveryControl(runID, planID, ordinal);
    if (body.version !== "batch-delivery-workbench.v1" || !body.confirm || !safePositiveInteger(body.expected_generation) ||
      body.expected_generation >= 8 || typeof body.retry !== "boolean") throw new Error("Confirmed current owner generation is required");
    return parseBatchWorkbench(await this.transport.sendControl<unknown>(
      `/runs/${encodeURIComponent(runID)}/batch-deliveries/${encodeURIComponent(planID)}/children/${ordinal}/workbench-owner`,
      body, idempotencyKey, signal), runID, planID);
  }

  async executeBatchWorkbenchChild(runID: string, planID: string, ordinal: number,
    body: BatchWorkbenchExecuteRequestView, idempotencyKey: string, signal?: AbortSignal): Promise<BatchWorkbenchView> {
    if (new TextEncoder().encode(idempotencyKey).byteLength > 224) throw new Error("Batch workbench operation key is too long");
    this.requireBatchDeliveryControl(runID, planID, ordinal);
    if (body.version !== "batch-delivery-workbench.v1" || !body.confirm || !safePositiveInteger(body.expected_generation) ||
      body.expected_generation > 8) throw new Error("Confirmed current child generation is required");
    return parseBatchWorkbench(await this.transport.sendControl<unknown>(
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
    const value = await this.transport.sendControl<unknown>(
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
    const value = await this.transport.sendControl<unknown>(
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
    const value = await this.transport.sendControl<unknown>(
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
    const value = await this.transport.sendControlRequest<unknown>(
      `/runs/${encodeURIComponent(runID)}/batch-deliveries/${encodeURIComponent(planID)}/reconcile`,
      body, signal,
    );
    return parseBatchDeliveryReconcile(value, planID);
  }

  private requireBatchDeliveryControl(runID: string, planID: string, ordinal = 1): void {
    if (!this.features.hasBatchDeliveryControl) {
      throw new Error("Batch delivery control capability is required for this operation");
    }
    if (!boundedIdentity(runID) || !boundedIdentity(planID) ||
      !safePositiveInteger(ordinal) || ordinal > 2) {
      throw new Error("Normalized Run, batch delivery, and child identities are required");
    }
  }
}
