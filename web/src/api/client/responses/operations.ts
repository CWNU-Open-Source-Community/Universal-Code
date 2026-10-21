import { APIRequestError } from "../protocol";
import { boundedIdentity, hasExactKeys, hasOnlyKeys, isRecord, validDate } from "../validation";
import type {
  OperationReceiptHistoryView,
  OperationReceiptView,
  OperatorActionCenterView,
} from "../../types";

export function parseOperationReceipt(value: unknown, kind: OperationReceiptView["kind"],
  outcome: OperationReceiptView["outcome"], replayed: boolean): OperationReceiptView {
  if (!hasExactKeys(value, ["cleanup_state", "durable", "kind", "outcome", "protocol_version",
    "recovery_action", "replayed", "retry_safe", "retry_strategy"]) ||
    value.protocol_version !== "operation_receipt.v1" || value.kind !== kind ||
    value.outcome !== outcome || value.durable !== true || value.replayed !== replayed ||
    value.retry_safe !== true) {
    throw new APIRequestError("Operation receipt violated its durable recovery contract",
      "INVALID_RESPONSE", 502);
  }
  const apply = kind === "file_edit_apply";
  if ((apply && value.retry_strategy !== "same_operation_key") ||
    (kind === "run_wake_consume" && value.retry_strategy !== "same_wake_generation") ||
    (kind === "skill_package_install" && value.retry_strategy !== "same_operation_key") ||
    (kind === "skill_package_remove" && value.retry_strategy !== "same_operation_key") ||
    (apply && !["complete", "pending_review"].includes(String(value.cleanup_state))) ||
    (!apply && value.cleanup_state !== "not_applicable") ||
    (value.cleanup_state === "pending_review" &&
      value.recovery_action !== "retry_after_cleanup_grace") ||
    (value.cleanup_state !== "pending_review" && value.recovery_action !== "none")) {
    throw new APIRequestError("Operation receipt widened recovery authority",
      "INVALID_RESPONSE", 502);
  }
  return value as unknown as OperationReceiptView;
}

export function parseOperatorActionCenter(value: unknown, runID: string): OperatorActionCenterView {
  if (!hasExactKeys(value, ["generated_at", "items", "protocol_version", "run_id",
    "truncated"]) || value.protocol_version !== "operator_action_center.v1" ||
    value.run_id !== runID || !validDate(value.generated_at) || !Array.isArray(value.items) ||
    value.items.length > 100 || typeof value.truncated !== "boolean") {
    throw new APIRequestError("Operator action center response is invalid", "INVALID_RESPONSE", 502);
  }
  const mapping = {
    steering_pending: ["pending", "queue"],
    approval_pending: ["pending", "approvals"],
    file_edit_review: ["proposed", "diffs"],
    file_edit_apply: ["approved", "diffs"],
    wake_due: ["queued", "wake"],
  } as const;
  const identities = new Set<string>();
  const generatedAt = Date.parse(value.generated_at);
  const items = value.items.map((item) => {
    if (!isRecord(item) || !hasOnlyKeys(item, ["available_at", "destination", "due_at", "id",
      "kind", "state"]) || !boundedIdentity(item.id) || !String(item.id).startsWith("action-") ||
      identities.has(String(item.id)) || !validDate(item.available_at) ||
      !Object.prototype.hasOwnProperty.call(mapping, String(item.kind))) {
      throw new APIRequestError("Operator action item exposed invalid metadata",
        "INVALID_RESPONSE", 502);
    }
    const expected = mapping[item.kind as keyof typeof mapping];
    const dueAt = item.due_at;
    if (item.state !== expected[0] || item.destination !== expected[1] ||
      (item.kind === "wake_due"
        ? !validDate(dueAt) || Date.parse(dueAt) > generatedAt
        : dueAt !== undefined)) {
      throw new APIRequestError("Operator action item widened its closed navigation contract",
        "INVALID_RESPONSE", 502);
    }
    identities.add(String(item.id));
    return item;
  });
  return { ...value, items } as unknown as OperatorActionCenterView;
}

export function parseOperationReceiptHistory(value: unknown,
  expectedRunID: string): OperationReceiptHistoryView {
  if (!hasExactKeys(value, ["items", "protocol_version", "truncated"]) ||
    value.protocol_version !== "operation_receipt_history.v1" || !Array.isArray(value.items) ||
    value.items.length > 100 || typeof value.truncated !== "boolean") {
    throw new APIRequestError("Operation receipt history response is invalid",
      "INVALID_RESPONSE", 502);
  }
  const identities = new Set<string>();
  const items = value.items.map((item) => {
    if (!isRecord(item) || !hasOnlyKeys(item, ["completed_at", "id", "receipt", "run_id", "scope"]) ||
      !boundedIdentity(item.id) || identities.has(String(item.id)) || !validDate(item.completed_at) ||
      (item.scope !== "run" && item.scope !== "skill_registry") ||
      (item.scope === "run" && (!boundedIdentity(item.run_id) ||
        (expectedRunID !== "" && item.run_id !== expectedRunID))) ||
      (item.scope === "skill_registry" && item.run_id !== undefined)) {
      throw new APIRequestError("Operation receipt history item exposed invalid scope metadata",
        "INVALID_RESPONSE", 502);
    }
    const receiptValue = item.receipt;
    if (!isRecord(receiptValue) || typeof receiptValue.kind !== "string" ||
      typeof receiptValue.outcome !== "string") {
      throw new APIRequestError("Operation receipt history item omitted its durable receipt",
        "INVALID_RESPONSE", 502);
    }
    const validTerminalOutcome =
      (receiptValue.kind === "file_edit_apply" &&
        (receiptValue.outcome === "applied" || receiptValue.outcome === "failed")) ||
      (receiptValue.kind === "run_wake_consume" &&
        (receiptValue.outcome === "completed" || receiptValue.outcome === "failed")) ||
      (receiptValue.kind === "skill_package_install" && receiptValue.outcome === "installed");
    if (!validTerminalOutcome) {
      throw new APIRequestError("Operation receipt history contains an unsupported terminal result",
        "INVALID_RESPONSE", 502);
    }
    const receipt = parseOperationReceipt(receiptValue,
      receiptValue.kind as OperationReceiptView["kind"],
      receiptValue.outcome as OperationReceiptView["outcome"], false);
    if ((item.scope === "skill_registry") !== (receipt.kind === "skill_package_install")) {
      throw new APIRequestError("Operation receipt history scope and kind diverged",
        "INVALID_RESPONSE", 502);
    }
    identities.add(String(item.id));
    return { ...item, receipt };
  });
  return { ...value, items } as unknown as OperationReceiptHistoryView;
}
