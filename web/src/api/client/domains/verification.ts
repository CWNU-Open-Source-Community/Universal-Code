import { APIRequestError, parseResponsePage } from "../protocol";
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
} from "../responses/verification";
import { boundedIdentity, isSHA256, safeBoundedCount, safePositiveInteger } from "../validation";
import type {
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
} from "../../types";
import type { ClientFeatures } from "../capabilities";
import type { APITransport } from "../transport";

export class VerificationClient {
  constructor(
    private readonly transport: APITransport,
    private readonly features: ClientFeatures,
  ) {}

  async verificationEvidence(runID: string,
    signal?: AbortSignal): Promise<VerificationEvidenceInventoryView> {
    if (!boundedIdentity(runID) || runID.trim() !== runID) {
      throw new Error("A normalized Run identity is required");
    }
    return parseVerificationEvidenceInventory(await this.transport.get<unknown>(
      `/runs/${encodeURIComponent(runID)}/verification-evidence`, {}, signal,
    ), runID);
  }

  async recordVerificationEvidence(runID: string, body: VerificationEvidenceRequestView,
    idempotencyKey: string, signal?: AbortSignal): Promise<VerificationEvidenceControlView> {
    if (!this.features.hasVerificationEvidence || !boundedIdentity(runID) ||
      body.version !== "operator_verification_evidence.v1" ||
      !["pass", "fail", "unknown"].includes(body.outcome) ||
      !validVerificationText(body.title, 160, false) ||
      !validVerificationText(body.summary, 2048, true)) {
      throw new Error("Verification evidence capability and a bounded observation are required");
    }
    return parseVerificationEvidenceItem(await this.transport.sendControl<unknown>(
      `/runs/${encodeURIComponent(runID)}/verification-evidence`, body, idempotencyKey, signal,
    ), runID, "", "", true);
  }

  async verificationPlans(runID: string,
    signal?: AbortSignal): Promise<VerificationPlanInventoryView> {
    if (!boundedIdentity(runID) || runID.trim() !== runID) {
      throw new Error("A normalized Run identity is required");
    }
    return parseVerificationPlanInventory(await this.transport.get<unknown>(
      `/runs/${encodeURIComponent(runID)}/verification-plan`, {}, signal,
    ), runID);
  }

  async recordVerificationPlan(runID: string, body: VerificationPlanRequestView,
    idempotencyKey: string, signal?: AbortSignal): Promise<VerificationPlanControlView> {
    if (!this.features.hasVerificationEvidence || !boundedIdentity(runID) ||
      body.version !== "operator_verification_plan.v1" ||
      !validVerificationText(body.title, 160, false) ||
      !validVerificationText(body.summary, 2048, true) || !Array.isArray(body.items) ||
      body.items.length < 1 || body.items.length > 32 || body.items.some((item) =>
        !validVerificationText(item.title, 160, false) ||
        !validVerificationText(item.expected_observation, 1024, true))) {
      throw new Error("Verification capability and a bounded operator checklist are required");
    }
    return parseVerificationPlan(await this.transport.sendControl<unknown>(
      `/runs/${encodeURIComponent(runID)}/verification-plan`, body,
      idempotencyKey, signal), runID, "", "", true);
  }

  async verificationPlanCoverage(runID: string,
    signal?: AbortSignal): Promise<VerificationPlanCoverageInventoryView> {
    if (!boundedIdentity(runID) || runID.trim() !== runID) {
      throw new Error("A normalized Run identity is required");
    }
    return parseVerificationPlanCoverage(await this.transport.get<unknown>(
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
    const envelope = await this.transport.request<unknown>(
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
    return parseVerificationSnapshotExport(await this.transport.get<unknown>(
      `/runs/${encodeURIComponent(runID)}/verification-plan-coverage/` +
        `${encodeURIComponent(planID)}/items/${ordinal}/snapshot-export`, { format }, signal,
    ), runID, planID, ordinal, format);
  }

  async verificationSnapshotReceipts(runID: string,
    signal?: AbortSignal): Promise<VerificationSnapshotReceiptInventoryView> {
    if (!boundedIdentity(runID) || runID.trim() !== runID) {
      throw new Error("A normalized Run identity is required");
    }
    return parseVerificationSnapshotReceiptInventory(await this.transport.get<unknown>(
      `/runs/${encodeURIComponent(runID)}/verification-snapshot-receipts`, {}, signal,
    ), runID);
  }

  async recordVerificationSnapshotReceipt(runID: string,
    body: VerificationSnapshotReceiptRequestView, idempotencyKey: string,
    signal?: AbortSignal): Promise<VerificationSnapshotReceiptControlView> {
    if (!this.features.hasVerificationEvidence || !boundedIdentity(runID) ||
      body.version !== "operator_verification_plan_item_snapshot_receipt.v1" ||
      !boundedIdentity(body.plan_id) || !safePositiveInteger(body.plan_item_ordinal) ||
      body.plan_item_ordinal > 32 || !["json", "markdown"].includes(body.format) ||
      !safeBoundedCount(body.snapshot_high_water_event_sequence, Number.MAX_SAFE_INTEGER) ||
      !isSHA256(body.content_sha256) || body.confirm_metadata_snapshot !== true) {
      throw new Error("Verification capability and an exact confirmed snapshot digest are required");
    }
    return parseVerificationSnapshotReceipt(await this.transport.sendControl<unknown>(
      `/runs/${encodeURIComponent(runID)}/verification-snapshot-receipts`, body,
      idempotencyKey, signal), runID, "", "", true);
  }

  async verificationSnapshotReceiptReviews(runID: string,
    signal?: AbortSignal): Promise<VerificationSnapshotReceiptReviewInventoryView> {
    if (!boundedIdentity(runID) || runID.trim() !== runID) {
      throw new Error("A normalized Run identity is required");
    }
    return parseVerificationSnapshotReceiptReviewInventory(await this.transport.get<unknown>(
      `/runs/${encodeURIComponent(runID)}/verification-snapshot-receipt-reviews`, {}, signal,
    ), runID);
  }

  async recordVerificationSnapshotReceiptReview(runID: string,
    body: VerificationSnapshotReceiptReviewRequestView, idempotencyKey: string,
    signal?: AbortSignal): Promise<VerificationSnapshotReceiptReviewControlView> {
    if (!this.features.hasVerificationEvidence || !boundedIdentity(runID) ||
      body.version !== "operator_verification_plan_item_snapshot_receipt_review.v1" ||
      !boundedIdentity(body.receipt_id) || !isSHA256(body.receipt_content_sha256) ||
      !safePositiveInteger(body.receipt_event_sequence) ||
      !["metadata_confirmed", "metadata_disputed"].includes(body.decision) ||
      body.confirm_non_authorizing_review !== true) {
      throw new Error("Verification capability and an exact non-authorizing review are required");
    }
    return parseVerificationSnapshotReceiptReview(await this.transport.sendControl<unknown>(
      `/runs/${encodeURIComponent(runID)}/verification-snapshot-receipt-reviews`, body,
      idempotencyKey, signal), runID, "", "", true);
  }

  async associateVerificationEvidence(runID: string, body: VerificationAssociationRequestView,
    idempotencyKey: string, signal?: AbortSignal): Promise<VerificationAssociationControlView> {
    if (!this.features.hasVerificationEvidence || !boundedIdentity(runID) ||
      body.version !== "operator_verification_plan_evidence_association.v1" ||
      !boundedIdentity(body.plan_id) || !safePositiveInteger(body.plan_item_ordinal) ||
      body.plan_item_ordinal > 32 || !boundedIdentity(body.evidence_id)) {
      throw new Error("Verification capability and exact plan/evidence identities are required");
    }
    return parseVerificationAssociation(await this.transport.sendControl<unknown>(
      `/runs/${encodeURIComponent(runID)}/verification-plan-associations`, body,
      idempotencyKey, signal), runID, true);
  }
}
