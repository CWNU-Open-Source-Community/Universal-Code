import { APIRequestError } from "../protocol";
import {
  boundedIdentity,
  hasExactKeys,
  hasRequiredOnlyKeys,
  isRecord,
  safeBoundedCount,
  safePositiveInteger,
} from "../validation";
import type {
  PlanDeliveryTransitionControlView,
  PlanDeliveryWorkItemControlRequestView,
  PlanDirectionControlRequestView,
  PlanDirectionControlView,
  PlanModeTransitionControlView,
  WorkItemView,
} from "../../types";

export function parsePlanDirectionControl(value: unknown, expectedRunID: string,
  request: PlanDirectionControlRequestView): PlanDirectionControlView {
  if (!hasRequiredOnlyKeys(value, ["capability_grant", "direction", "execution_started", "model_called",
    "note_id", "phase_changed", "proposal_id", "replayed", "run_id", "selection_id",
    "tool_called", "version", "work_item_count"], ["manual_acceptance"]) ||
    value.version !== "plan_delivery_control.v1" || value.run_id !== expectedRunID ||
    value.proposal_id !== request.proposal_id || value.direction !== request.direction ||
    (value.manual_acceptance !== undefined && value.manual_acceptance !== "required" && value.manual_acceptance !== "on_demand") ||
    (value.manual_acceptance ?? "required") !== (request.manual_acceptance ?? "required") ||
    !boundedIdentity(value.run_id) || !boundedIdentity(value.proposal_id) ||
    !boundedIdentity(value.selection_id) || !boundedIdentity(value.note_id) ||
    !safeBoundedCount(value.work_item_count, 32) || value.work_item_count < 1 ||
    typeof value.replayed !== "boolean" || value.phase_changed !== false ||
    value.execution_started !== false || value.model_called !== false ||
    value.tool_called !== false || value.capability_grant !== false) {
    throw new APIRequestError("Plan direction response violated its closed authority contract",
      "INVALID_RESPONSE", 502);
  }
  return value as unknown as PlanDirectionControlView;
}

export function parsePlanModeTransition(value: unknown,
  expectedRunID: string): PlanModeTransitionControlView {
  if (!hasExactKeys(value, ["applied_mode", "capability_grant", "current_mode",
    "execution_started", "model_called", "replayed", "run_id", "tool_called", "version"]) ||
    value.version !== "plan_delivery_control.v1" || value.run_id !== expectedRunID ||
    !boundedIdentity(value.run_id) || !isRecord(value.applied_mode) ||
    !isRecord(value.current_mode) || value.applied_mode.phase !== "plan" ||
    value.current_mode.phase !== "plan" || value.applied_mode.capability_grant !== false ||
    value.current_mode.capability_grant !== false || typeof value.replayed !== "boolean" ||
    value.execution_started !== false || value.model_called !== false ||
    value.tool_called !== false || value.capability_grant !== false) {
    throw new APIRequestError("Plan mode response violated its closed authority contract",
      "INVALID_RESPONSE", 502);
  }
  return value as unknown as PlanModeTransitionControlView;
}

export function validatePlanWorkItemRequest(enabled: boolean, runID: string, itemID: string,
  body: PlanDeliveryWorkItemControlRequestView) {
  if (!enabled || boundedIdentity(runID) !== runID || boundedIdentity(itemID) !== itemID ||
    body.version !== "plan_delivery_control.v1" || !safePositiveInteger(body.expected_work_item_version)) {
    throw new Error("Plan controls and exact Run, item, and version identities are required");
  }
}

export function validPlanWorkItemResult(value: unknown, runID: string, itemID: string):
  value is Record<string, unknown> & { current_work_item: WorkItemView } {
  if (!isRecord(value) || value.version !== "plan_delivery_control.v1" || value.run_id !== runID ||
    typeof value.replayed !== "boolean" || value.execution_started !== false || value.model_called !== false ||
    value.tool_called !== false || value.capability_grant !== false || !isRecord(value.current_work_item)) return false;
  const item = value.current_work_item;
  return item.id === itemID && item.run_id === runID && safePositiveInteger(item.version) &&
    typeof item.title === "string" && Array.isArray(item.acceptance_criteria) &&
    item.acceptance_criteria.every((entry) => typeof entry === "string") && Array.isArray(item.dependencies) &&
    item.dependencies.every((entry) => typeof entry === "string") &&
    typeof item.status === "string" && ["pending", "in_progress", "blocked", "completed", "cancelled"].includes(item.status);
}

export function parsePlanDeliveryTransition(value: unknown,
  expectedRunID: string): PlanDeliveryTransitionControlView {
  if (!hasExactKeys(value, ["applied_mode", "capability_grant", "current_mode",
    "execution_started", "model_called", "replayed", "run_id", "selection_id", "tool_called",
    "version"]) || value.version !== "plan_delivery_control.v1" ||
    value.run_id !== expectedRunID || !boundedIdentity(value.run_id) ||
    !boundedIdentity(value.selection_id) || !isRecord(value.applied_mode) ||
    !isRecord(value.current_mode) || value.applied_mode.phase !== "deliver" ||
    value.current_mode.phase !== "deliver" || value.applied_mode.capability_grant !== false ||
    value.current_mode.capability_grant !== false || typeof value.replayed !== "boolean" ||
    value.execution_started !== false || value.model_called !== false ||
    value.tool_called !== false || value.capability_grant !== false) {
    throw new APIRequestError("Plan delivery response violated its closed authority contract",
      "INVALID_RESPONSE", 502);
  }
  return value as unknown as PlanDeliveryTransitionControlView;
}
