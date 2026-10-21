import { APIRequestError } from "../protocol";
import {
  parsePlanDeliveryTransition,
  parsePlanDirectionControl,
  parsePlanModeTransition,
  validPlanWorkItemResult,
  validatePlanWorkItemRequest,
} from "../responses/plans";
import {
  boundedIdentity,
  boundedText,
  hasExactKeys,
  hasRequiredOnlyKeys,
  isRecord,
  safePositiveInteger,
  validDate,
} from "../validation";
import type {
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
} from "../../types";
import type { ClientFeatures } from "../capabilities";
import type { APITransport } from "../transport";

export class PlansClient {
  constructor(
    private readonly transport: APITransport,
    private readonly features: ClientFeatures,
  ) {}

  async selectPlanDirection(runID: string, body: PlanDirectionControlRequestView,
    idempotencyKey: string, signal?: AbortSignal): Promise<PlanDirectionControlView> {
    if (!this.features.hasPlanDelivery) {
      throw new Error("Plan/Delivery control capability is required for this operation");
    }
    if (!boundedIdentity(runID) || runID.trim() !== runID || !safePositiveInteger(body.direction) ||
      body.direction > 3 || !boundedIdentity(body.proposal_id) || body.version !== "plan_delivery_control.v1" ||
      !hasRequiredOnlyKeys(body, ["version", "proposal_id", "direction"], ["manual_acceptance"]) ||
      (body.manual_acceptance !== undefined && body.manual_acceptance !== "required" && body.manual_acceptance !== "on_demand")) {
      throw new Error("A normalized Run, proposal, and direction are required");
    }
    const result = await this.transport.sendControl<unknown>(
      `/runs/${encodeURIComponent(runID)}/plan/direction`, body, idempotencyKey, signal,
    );
    return parsePlanDirectionControl(result, runID, body);
  }

  async enterPlanMode(runID: string, body: PlanModeTransitionControlRequestView,
    idempotencyKey: string, signal?: AbortSignal): Promise<PlanModeTransitionControlView> {
    if (!this.features.hasPlanDelivery) {
      throw new Error("Plan/Delivery control capability is required for this operation");
    }
    if (!boundedIdentity(runID) || runID.trim() !== runID) {
      throw new Error("A normalized Run identity is required");
    }
    const result = await this.transport.sendControl<unknown>(
      `/runs/${encodeURIComponent(runID)}/plan/enter`, body, idempotencyKey, signal,
    );
    return parsePlanModeTransition(result, runID);
  }

  async enterPlanDelivery(runID: string, body: PlanDeliveryTransitionControlRequestView,
    idempotencyKey: string, signal?: AbortSignal): Promise<PlanDeliveryTransitionControlView> {
    if (!this.features.hasPlanDelivery) {
      throw new Error("Plan/Delivery control capability is required for this operation");
    }
    if (!boundedIdentity(runID) || runID.trim() !== runID) {
      throw new Error("A normalized Run identity is required");
    }
    const result = await this.transport.sendControl<unknown>(
      `/runs/${encodeURIComponent(runID)}/plan/deliver`, body, idempotencyKey, signal,
    );
    return parsePlanDeliveryTransition(result, runID);
  }

  async controlPlanDeliveryWorkItem(runID: string, workItemID: string, action: "start" | "complete",
    body: PlanDeliveryWorkItemControlRequestView, idempotencyKey: string,
    signal?: AbortSignal): Promise<PlanDeliveryWorkItemControlView> {
    validatePlanWorkItemRequest(this.features.hasPlanDelivery, runID, workItemID, body);
    if ((action !== "start" && action !== "complete") ||
      !hasExactKeys(body, ["version", "expected_work_item_version"])) {
      throw new Error("An exact Plan item transition is required");
    }
    const value = await this.transport.sendControl<unknown>(
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
    validatePlanWorkItemRequest(this.features.hasPlanDelivery, runID, workItemID, body);
    if (!hasRequiredOnlyKeys(body, ["version", "expected_work_item_version", "focused_verification",
      "diff_audit", "security_audit", "handoff_summary"], ["functional_verification", "robustness_audit"]) ||
      ![body.focused_verification, body.diff_audit, body.security_audit].every((text) => boundedText(text, 1_024)) ||
      !boundedText(body.handoff_summary, 2_048) ||
      [body.functional_verification, body.robustness_audit].some((text) => text !== undefined && !boundedText(text, 1_024))) {
      throw new Error("Bounded manual verification evidence and a handoff summary are required");
    }
    const value = await this.transport.sendControl<unknown>(
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
}
