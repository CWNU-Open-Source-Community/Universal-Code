import { APIRequestError } from "../protocol";
import {
  boundedIdentity,
  hasExactKeys,
  isSHA256,
  safeBoundedCount,
  safePositiveInteger,
  validDate,
} from "../validation";
import type {
  Page,
  VerificationAssociationControlView,
  VerificationEvidenceControlView,
  VerificationEvidenceInventoryView,
  VerificationPlanControlView,
  VerificationPlanCoverageInventoryView,
  VerificationPlanInventoryView,
  VerificationPlanItemCoverageDetailView,
  VerificationSnapshotExportView,
  VerificationSnapshotReceiptControlView,
  VerificationSnapshotReceiptInventoryView,
  VerificationSnapshotReceiptReviewControlView,
  VerificationSnapshotReceiptReviewInventoryView,
  VerificationSnapshotReceiptReviewView,
  VerificationSnapshotReceiptView,
} from "../../types";

export function validVerificationText(value: unknown, maximum: number, multiline: boolean): value is string {
  if (typeof value !== "string" || value === "" || value.trim() !== value ||
    [...value].length > maximum || value.includes("\0")) {
    return false;
  }
  return multiline ? !/[\u0001-\u0008\u000b-\u001f\u007f]/u.test(value) :
    !/[\u0000-\u001f\u007f]/u.test(value);
}

export function parseVerificationEvidenceItem(value: unknown, runID: string,
  sessionID = "", workspaceID = "", control = false): VerificationEvidenceControlView {
  const keys = ["approval", "authority_granted", "command_executed", "id", "immutable",
    "model_assertion", "operator_supplied", "outcome", "protocol_version", "recorded_at",
    "redacted", "run_id", "session_id", "summary", "summary_sha256", "title", "workspace_id"];
  if (control) keys.push("replayed");
  if (!hasExactKeys(value, keys) || value.protocol_version !== "operator_verification_evidence.v1" ||
    value.run_id !== runID || !boundedIdentity(value.id) || !boundedIdentity(value.session_id) ||
    !boundedIdentity(value.workspace_id) || (sessionID !== "" && value.session_id !== sessionID) ||
    (workspaceID !== "" && value.workspace_id !== workspaceID) ||
    !["pass", "fail", "unknown"].includes(String(value.outcome)) ||
    !validVerificationText(value.title, 160, false) ||
    !validVerificationText(value.summary, 2048, true) || !isSHA256(value.summary_sha256) ||
    typeof value.redacted !== "boolean" || !validDate(value.recorded_at) ||
    value.immutable !== true || value.operator_supplied !== true ||
    value.command_executed !== false || value.model_assertion !== false ||
    value.approval !== false || value.authority_granted !== false ||
    (control && typeof value.replayed !== "boolean")) {
    throw new APIRequestError("Verification evidence widened observation authority",
      "INVALID_RESPONSE", 502);
  }
  return value as unknown as VerificationEvidenceControlView;
}

export function parseVerificationEvidenceInventory(value: unknown,
  runID: string): VerificationEvidenceInventoryView {
  if (!hasExactKeys(value, ["fail_count", "items", "pass_count", "protocol_version", "run_id",
    "session_id", "truncated", "unknown_count", "workspace_id"]) ||
    value.protocol_version !== "operator_verification_inventory.v1" || value.run_id !== runID ||
    !boundedIdentity(value.session_id) || !boundedIdentity(value.workspace_id) ||
    !Array.isArray(value.items) || value.items.length > 100 ||
    !safeBoundedCount(value.pass_count, 100) || !safeBoundedCount(value.fail_count, 100) ||
    !safeBoundedCount(value.unknown_count, 100) || typeof value.truncated !== "boolean" ||
    value.pass_count + value.fail_count + value.unknown_count !== value.items.length) {
    throw new APIRequestError("Verification inventory violated its immutable bounded contract",
      "INVALID_RESPONSE", 502);
  }
  const ids = new Set<string>();
  const items = value.items.map((item) => {
    const parsed = parseVerificationEvidenceItem(item, runID, String(value.session_id),
      String(value.workspace_id));
    if (ids.has(parsed.id)) {
      throw new APIRequestError("Verification inventory repeated an immutable identity",
        "INVALID_RESPONSE", 502);
    }
    ids.add(parsed.id);
    return parsed;
  });
  if (value.truncated && items.length !== 100) {
    throw new APIRequestError("Verification inventory truncation is inconsistent",
      "INVALID_RESPONSE", 502);
  }
  return { ...value, items } as unknown as VerificationEvidenceInventoryView;
}

export function parseVerificationPlan(value: unknown, runID: string, sessionID = "",
  workspaceID = "", control = false): VerificationPlanControlView {
  const keys = ["approval", "authority_granted", "command_executed", "created_at",
    "guidance_only", "id", "immutable", "item_count", "items", "model_assertion",
    "operator_supplied", "plan_sha256", "protocol_version", "redacted", "result_inferred",
    "run_id", "session_id", "summary", "title", "workspace_id"];
  if (control) keys.push("replayed");
  if (!hasExactKeys(value, keys) || value.protocol_version !== "operator_verification_plan.v1" ||
    value.run_id !== runID || !boundedIdentity(value.id) || !boundedIdentity(value.session_id) ||
    !boundedIdentity(value.workspace_id) || (sessionID !== "" && value.session_id !== sessionID) ||
    (workspaceID !== "" && value.workspace_id !== workspaceID) ||
    !validVerificationText(value.title, 160, false) ||
    !validVerificationText(value.summary, 2048, true) || !isSHA256(value.plan_sha256) ||
    typeof value.redacted !== "boolean" || !validDate(value.created_at) ||
    !Array.isArray(value.items) || value.items.length < 1 || value.items.length > 32 ||
    value.item_count !== value.items.length || value.immutable !== true ||
    value.operator_supplied !== true || value.guidance_only !== true ||
    value.command_executed !== false || value.model_assertion !== false ||
    value.result_inferred !== false || value.approval !== false ||
    value.authority_granted !== false || (control && typeof value.replayed !== "boolean")) {
    throw new APIRequestError("Verification plan widened guidance or result authority",
      "INVALID_RESPONSE", 502);
  }
  let itemRedacted = false;
  const items = value.items.map((item, index) => {
    if (!hasExactKeys(item, ["expected_observation", "item_sha256", "ordinal", "redacted",
      "title"]) || item.ordinal !== index + 1 ||
      !validVerificationText(item.title, 160, false) ||
      !validVerificationText(item.expected_observation, 1024, true) ||
      !isSHA256(item.item_sha256) || typeof item.redacted !== "boolean") {
      throw new APIRequestError("Verification plan item violated its bounded checklist contract",
        "INVALID_RESPONSE", 502);
    }
    itemRedacted = itemRedacted || item.redacted;
    return item;
  });
  if (itemRedacted && !value.redacted) {
    throw new APIRequestError("Verification plan redaction state is inconsistent",
      "INVALID_RESPONSE", 502);
  }
  return { ...value, items } as unknown as VerificationPlanControlView;
}

export function parseVerificationPlanInventory(value: unknown,
  runID: string): VerificationPlanInventoryView {
  if (!hasExactKeys(value, ["items", "protocol_version", "run_id", "session_id", "truncated",
    "workspace_id"]) || value.protocol_version !== "operator_verification_plan_inventory.v1" ||
    value.run_id !== runID || !boundedIdentity(value.session_id) ||
    !boundedIdentity(value.workspace_id) || !Array.isArray(value.items) ||
    value.items.length > 50 || typeof value.truncated !== "boolean") {
    throw new APIRequestError("Verification plan inventory violated its immutable bounded contract",
      "INVALID_RESPONSE", 502);
  }
  const identities = new Set<string>();
  const items = value.items.map((item) => {
    const parsed = parseVerificationPlan(item, runID, String(value.session_id),
      String(value.workspace_id));
    if (identities.has(parsed.id)) {
      throw new APIRequestError("Verification plan inventory repeated an immutable identity",
        "INVALID_RESPONSE", 502);
    }
    identities.add(parsed.id);
    return parsed;
  });
  if (value.truncated && items.length !== 50) {
	throw new APIRequestError("Verification plan inventory truncation is inconsistent",
	  "INVALID_RESPONSE", 502);
  }
  return { ...value, items } as unknown as VerificationPlanInventoryView;
}

export function parseVerificationAssociation(value: unknown, runID: string,
  control = false): VerificationAssociationControlView {
  const keys = ["approval", "associated_at", "association_event_sequence",
    "authority_granted", "command_executed", "evidence_event_sequence", "evidence_id",
    "evidence_outcome", "id", "immutable", "metadata_only", "model_assertion",
    "operator_supplied", "plan_id", "plan_item_ordinal", "plan_item_sha256",
    "protocol_version", "record_rewritten", "result_inferred", "run_id", "session_id",
    "workspace_id"];
  if (control) keys.push("replayed");
  if (!hasExactKeys(value, keys) ||
    value.protocol_version !== "operator_verification_plan_evidence_association.v1" ||
    value.run_id !== runID || !boundedIdentity(value.id) || !boundedIdentity(value.session_id) ||
    !boundedIdentity(value.workspace_id) || !boundedIdentity(value.plan_id) ||
    !safePositiveInteger(value.plan_item_ordinal) || value.plan_item_ordinal > 32 ||
    !isSHA256(value.plan_item_sha256) || !boundedIdentity(value.evidence_id) ||
    !["pass", "fail", "unknown"].includes(String(value.evidence_outcome)) ||
    !safePositiveInteger(value.evidence_event_sequence) ||
    !safePositiveInteger(value.association_event_sequence) ||
    value.association_event_sequence <= value.evidence_event_sequence ||
    !validDate(value.associated_at) || value.immutable !== true ||
    value.operator_supplied !== true || value.metadata_only !== true ||
    value.command_executed !== false || value.model_assertion !== false ||
    value.result_inferred !== false || value.record_rewritten !== false ||
    value.approval !== false || value.authority_granted !== false ||
    (control && typeof value.replayed !== "boolean")) {
    throw new APIRequestError("Verification association widened result or mutation authority",
      "INVALID_RESPONSE", 502);
  }
  return value as unknown as VerificationAssociationControlView;
}

export function parseVerificationPlanCoverage(value: unknown,
  runID: string): VerificationPlanCoverageInventoryView {
  const keys = ["approval", "associated_evidence_count", "associations",
    "associations_truncated", "authority_granted", "command_executed", "metadata_only",
    "model_assertion", "observed_plan_item_count", "plan_count", "plan_item_count", "plans",
    "plans_truncated", "protocol_version", "read_only", "record_rewritten",
    "result_inferred", "run_id", "session_id", "workspace_id"];
  if (!hasExactKeys(value, keys) ||
    value.protocol_version !== "operator_verification_plan_coverage.v1" ||
    value.run_id !== runID || !boundedIdentity(value.session_id) ||
    !boundedIdentity(value.workspace_id) || !Array.isArray(value.plans) ||
    value.plans.length > 50 || value.plan_count !== value.plans.length ||
    !safeBoundedCount(value.plan_item_count, 1600) ||
    !safeBoundedCount(value.observed_plan_item_count, 1600) ||
    value.observed_plan_item_count > value.plan_item_count ||
    !safeBoundedCount(value.associated_evidence_count, 1_000_000_000) ||
    !Array.isArray(value.associations) || value.associations.length > 100 ||
    typeof value.plans_truncated !== "boolean" ||
    typeof value.associations_truncated !== "boolean" || value.metadata_only !== true ||
    value.read_only !== true || value.result_inferred !== false ||
    value.command_executed !== false || value.model_assertion !== false ||
    value.record_rewritten !== false || value.approval !== false ||
    value.authority_granted !== false) {
    throw new APIRequestError("Verification coverage widened metadata-only authority",
      "INVALID_RESPONSE", 502);
  }
  const planIDs = new Set<string>();
  const planItems = new Map<string, Map<number, string>>();
  let itemTotal = 0;
  let observedTotal = 0;
  let associationTotal = 0;
  const plans = value.plans.map((plan) => {
    if (!hasExactKeys(plan, ["associated_evidence_count", "item_count", "items",
      "observed_item_count", "plan_id", "plan_sha256"]) ||
      !boundedIdentity(plan.plan_id) || planIDs.has(String(plan.plan_id)) ||
      !isSHA256(plan.plan_sha256) || !Array.isArray(plan.items) ||
      plan.items.length < 1 || plan.items.length > 32 || plan.item_count !== plan.items.length ||
      !safeBoundedCount(plan.observed_item_count, 32) ||
      !safeBoundedCount(plan.associated_evidence_count, 1_000_000_000)) {
      throw new APIRequestError("Verification coverage plan metadata is invalid",
        "INVALID_RESPONSE", 502);
    }
    const ordinals = new Map<number, string>();
    let observed = 0;
    let associated = 0;
    const items = plan.items.map((item, index) => {
      if (!hasExactKeys(item, ["associated_evidence_count", "fail_count", "item_sha256",
        "latest_association_event_sequence", "ordinal", "pass_count", "unknown_count"]) ||
        item.ordinal !== index + 1 || !isSHA256(item.item_sha256) ||
        !safeBoundedCount(item.associated_evidence_count, 1_000_000_000) ||
        !safeBoundedCount(item.pass_count, 1_000_000_000) ||
        !safeBoundedCount(item.fail_count, 1_000_000_000) ||
        !safeBoundedCount(item.unknown_count, 1_000_000_000) ||
        item.pass_count + item.fail_count + item.unknown_count !==
          item.associated_evidence_count ||
        !safeBoundedCount(item.latest_association_event_sequence, Number.MAX_SAFE_INTEGER) ||
        ((item.associated_evidence_count === 0) !==
          (item.latest_association_event_sequence === 0))) {
        throw new APIRequestError("Verification coverage item inferred a non-explicit result",
          "INVALID_RESPONSE", 502);
      }
      if (item.associated_evidence_count > 0) observed += 1;
      associated += Number(item.associated_evidence_count);
      ordinals.set(Number(item.ordinal), String(item.item_sha256));
      return item;
    });
    if (plan.observed_item_count !== observed || plan.associated_evidence_count !== associated) {
      throw new APIRequestError("Verification coverage plan counts are inconsistent",
        "INVALID_RESPONSE", 502);
    }
    planIDs.add(String(plan.plan_id));
    planItems.set(String(plan.plan_id), ordinals);
    itemTotal += Number(plan.item_count);
    observedTotal += observed;
    associationTotal += associated;
    return { ...plan, items };
  });
  const associationIDs = new Set<string>();
  const evidenceIDs = new Set<string>();
  const associations = value.associations.map((association) => {
    if (!hasExactKeys(association, ["associated_at", "association_event_sequence",
      "evidence_event_sequence", "evidence_id", "evidence_outcome", "id", "plan_id",
      "plan_item_ordinal", "plan_item_sha256"]) || !boundedIdentity(association.id) ||
      associationIDs.has(String(association.id)) || !boundedIdentity(association.plan_id) ||
      !safePositiveInteger(association.plan_item_ordinal) || association.plan_item_ordinal > 32 ||
      !isSHA256(association.plan_item_sha256) || !boundedIdentity(association.evidence_id) ||
      evidenceIDs.has(String(association.evidence_id)) ||
      !["pass", "fail", "unknown"].includes(String(association.evidence_outcome)) ||
      !safePositiveInteger(association.evidence_event_sequence) ||
      !safePositiveInteger(association.association_event_sequence) ||
      association.association_event_sequence <= association.evidence_event_sequence ||
      !validDate(association.associated_at)) {
      throw new APIRequestError("Verification coverage association metadata is invalid",
        "INVALID_RESPONSE", 502);
    }
    const knownItems = planItems.get(String(association.plan_id));
    if (knownItems && knownItems.get(Number(association.plan_item_ordinal)) !==
      association.plan_item_sha256) {
      throw new APIRequestError("Verification association escaped its exact plan item",
        "INVALID_RESPONSE", 502);
    }
    associationIDs.add(String(association.id));
    evidenceIDs.add(String(association.evidence_id));
    return association;
  });
  if (itemTotal !== value.plan_item_count || observedTotal !== value.observed_plan_item_count ||
    associationTotal !== value.associated_evidence_count ||
    (value.plans_truncated && plans.length !== 50) ||
    (value.associations_truncated && associations.length !== 100)) {
    throw new APIRequestError("Verification coverage aggregate is inconsistent",
      "INVALID_RESPONSE", 502);
  }
  return { ...value, plans, associations } as unknown as VerificationPlanCoverageInventoryView;
}

export function parseVerificationPlanItemCoverage(value: unknown, runID: string, planID: string,
  ordinal: number, page: Page, firstPage: boolean): VerificationPlanItemCoverageDetailView {
  const keys = ["approval", "associated_evidence_count", "associations",
    "associations_truncated", "authority_granted", "command_executed", "fail_count",
    "latest_association_event_sequence", "metadata_only", "model_assertion",
    "operator_identity_included", "pass_count", "plan_id", "plan_item_ordinal",
    "plan_item_sha256", "plan_sha256", "private_evidence_bodies_included",
    "private_plan_body_included", "protocol_version", "read_only", "record_rewritten",
    "result_inferred", "run_id", "session_id", "unknown_count", "workspace_id"];
  if (!hasExactKeys(value, keys) ||
    value.protocol_version !== "operator_verification_plan_item_coverage.v1" ||
    value.run_id !== runID || value.plan_id !== planID || value.plan_item_ordinal !== ordinal ||
    !boundedIdentity(value.session_id) || !boundedIdentity(value.workspace_id) ||
    !isSHA256(value.plan_sha256) || !isSHA256(value.plan_item_sha256) ||
    !safeBoundedCount(value.associated_evidence_count, 1_000_000_000) ||
    !safeBoundedCount(value.pass_count, 1_000_000_000) ||
    !safeBoundedCount(value.fail_count, 1_000_000_000) ||
    !safeBoundedCount(value.unknown_count, 1_000_000_000) ||
    value.pass_count + value.fail_count + value.unknown_count !==
      value.associated_evidence_count ||
    !safeBoundedCount(value.latest_association_event_sequence, Number.MAX_SAFE_INTEGER) ||
    ((value.associated_evidence_count === 0) !==
      (value.latest_association_event_sequence === 0)) ||
    !Array.isArray(value.associations) || value.associations.length > page.limit ||
    typeof value.associations_truncated !== "boolean" || value.metadata_only !== true ||
    value.read_only !== true || value.private_plan_body_included !== false ||
    value.private_evidence_bodies_included !== false ||
    value.operator_identity_included !== false || value.result_inferred !== false ||
    value.command_executed !== false || value.model_assertion !== false ||
    value.record_rewritten !== false || value.approval !== false ||
    value.authority_granted !== false) {
    throw new APIRequestError("Verification item coverage widened its read-only boundary",
      "INVALID_RESPONSE", 502);
  }
  const associationIDs = new Set<string>();
  const evidenceIDs = new Set<string>();
  let previousSequence = Number.MAX_SAFE_INTEGER;
  let returnedPass = 0;
  let returnedFail = 0;
  let returnedUnknown = 0;
  const associations = value.associations.map((association, index) => {
    if (!hasExactKeys(association, ["associated_at", "association_event_sequence",
      "evidence_event_sequence", "evidence_id", "evidence_outcome", "id", "plan_id",
      "plan_item_ordinal", "plan_item_sha256"]) || !boundedIdentity(association.id) ||
      associationIDs.has(String(association.id)) || association.plan_id !== planID ||
      association.plan_item_ordinal !== ordinal ||
      association.plan_item_sha256 !== value.plan_item_sha256 ||
      !boundedIdentity(association.evidence_id) || evidenceIDs.has(String(association.evidence_id)) ||
      !["pass", "fail", "unknown"].includes(String(association.evidence_outcome)) ||
      !safePositiveInteger(association.evidence_event_sequence) ||
      !safePositiveInteger(association.association_event_sequence) ||
      association.association_event_sequence <= association.evidence_event_sequence ||
      association.association_event_sequence > Number(value.latest_association_event_sequence) ||
      (index > 0 && association.association_event_sequence >= previousSequence) ||
      !validDate(association.associated_at)) {
      throw new APIRequestError("Verification item association escaped its exact binding",
        "INVALID_RESPONSE", 502);
    }
    associationIDs.add(String(association.id));
    evidenceIDs.add(String(association.evidence_id));
    previousSequence = Number(association.association_event_sequence);
    if (association.evidence_outcome === "pass") returnedPass += 1;
    if (association.evidence_outcome === "fail") returnedFail += 1;
    if (association.evidence_outcome === "unknown") returnedUnknown += 1;
    return association;
  });
  const pageHasMore = Boolean(page.next_cursor) || page.truncated === true;
  if ((firstPage && associations.length > 0 &&
      associations[0]?.association_event_sequence !== value.latest_association_event_sequence) ||
    value.associations_truncated !== pageHasMore ||
    (value.associations_truncated && page.truncated !== true &&
      associations.length !== page.limit) ||
    (page.truncated === true && associations.length > page.limit) ||
    (firstPage && value.associations_truncated !==
      (associations.length < Number(value.associated_evidence_count))) ||
    (firstPage && !value.associations_truncated &&
      (associations.length !== value.associated_evidence_count ||
      returnedPass !== value.pass_count || returnedFail !== value.fail_count ||
      returnedUnknown !== value.unknown_count)) ||
    returnedPass > value.pass_count || returnedFail > value.fail_count ||
    returnedUnknown > value.unknown_count) {
    throw new APIRequestError("Verification item coverage counts are inconsistent",
      "INVALID_RESPONSE", 502);
  }
  return { ...value, associations } as unknown as VerificationPlanItemCoverageDetailView;
}

export async function parseVerificationSnapshotExport(value: unknown, runID: string, planID: string,
  ordinal: number, format: "json" | "markdown"): Promise<VerificationSnapshotExportView> {
  const keys = ["approval", "associated_evidence_count", "associations_truncated",
    "authority_granted", "command_executed", "content", "content_bytes", "content_sha256",
    "download_only", "execution_started", "fail_count", "filename", "format", "metadata_only",
    "mime_type", "model_assertion", "mutation_supported", "operator_identity_included",
    "pass_count", "plan_id", "plan_item_ordinal", "plan_item_sha256", "plan_sha256",
    "private_evidence_bodies_included", "private_plan_body_included", "protocol_version",
    "read_only", "record_rewritten", "result_inferred", "returned_association_count", "run_id",
    "session_id", "snapshot_high_water_event_sequence", "snapshot_protocol_version",
    "unknown_count", "workspace_id"];
  if (!hasExactKeys(value, keys) ||
    value.protocol_version !== "operator_verification_plan_item_snapshot_export.v1" ||
    value.snapshot_protocol_version !== "operator_verification_plan_item_snapshot.v1" ||
    value.run_id !== runID || value.plan_id !== planID || value.plan_item_ordinal !== ordinal ||
    value.format !== format || !boundedIdentity(value.session_id) ||
    !boundedIdentity(value.workspace_id) || !isSHA256(value.plan_sha256) ||
    !isSHA256(value.plan_item_sha256) ||
    !safeBoundedCount(value.associated_evidence_count, 1_000_000_000) ||
    !safeBoundedCount(value.pass_count, 1_000_000_000) ||
    !safeBoundedCount(value.fail_count, 1_000_000_000) ||
    !safeBoundedCount(value.unknown_count, 1_000_000_000) ||
    value.pass_count + value.fail_count + value.unknown_count !==
      value.associated_evidence_count ||
    !safeBoundedCount(value.snapshot_high_water_event_sequence, Number.MAX_SAFE_INTEGER) ||
    ((value.associated_evidence_count === 0) !==
      (value.snapshot_high_water_event_sequence === 0)) ||
    !safeBoundedCount(value.returned_association_count, 100) ||
    value.returned_association_count !== Math.min(Number(value.associated_evidence_count), 100) ||
    value.associations_truncated !== (value.associated_evidence_count > 100) ||
    typeof value.filename !== "string" || value.filename.length < 1 || value.filename.length > 255 ||
    /[\\/:*?"<>|\u0000-\u001f]/u.test(value.filename) || typeof value.mime_type !== "string" ||
    typeof value.content !== "string" || !safePositiveInteger(value.content_bytes) ||
    value.content_bytes > 256 * 1024 || !isSHA256(value.content_sha256) ||
    value.metadata_only !== true || value.read_only !== true || value.download_only !== true ||
    value.private_plan_body_included !== false ||
    value.private_evidence_bodies_included !== false ||
    value.operator_identity_included !== false || value.result_inferred !== false ||
    value.command_executed !== false || value.model_assertion !== false ||
    value.record_rewritten !== false || value.approval !== false ||
    value.authority_granted !== false || value.mutation_supported !== false ||
    value.execution_started !== false) {
    throw new APIRequestError("Verification snapshot export widened its read-only boundary",
      "INVALID_RESPONSE", 502);
  }
  const expectedMIME = format === "json" ? "application/json" : "text/markdown; charset=utf-8";
  const expectedSuffix = format === "json" ? ".json" : ".md";
  const encoded = new TextEncoder().encode(value.content);
  if (value.mime_type !== expectedMIME || !value.filename.endsWith(expectedSuffix) ||
    encoded.length !== value.content_bytes) {
    throw new APIRequestError("Verification snapshot metadata does not match its content",
      "INVALID_RESPONSE", 502);
  }
  const digest = new Uint8Array(await globalThis.crypto.subtle.digest("SHA-256", encoded));
  const digestHex = [...digest].map((byte) => byte.toString(16).padStart(2, "0")).join("");
  if (digestHex !== value.content_sha256) {
    throw new APIRequestError("Verification snapshot digest verification failed",
      "INVALID_RESPONSE", 502);
  }
  if (format === "json") {
    let document: unknown;
    try {
      document = JSON.parse(value.content);
    } catch {
      throw new APIRequestError("Verification snapshot JSON is invalid", "INVALID_RESPONSE", 502);
    }
    const documentKeys = ["approval", "associated_evidence_count", "associations",
      "associations_truncated", "authority_granted", "command_executed", "execution_started",
      "fail_count", "metadata_only", "model_assertion", "mutation_supported",
      "operator_identity_included", "pass_count", "plan_id", "plan_item_ordinal",
      "plan_item_sha256", "plan_sha256", "private_evidence_bodies_included",
      "private_plan_body_included", "protocol_version", "read_only", "record_rewritten",
      "result_inferred", "returned_association_count", "run_id", "session_id",
      "snapshot_high_water_event_sequence", "unknown_count", "workspace_id"];
    if (!hasExactKeys(document, documentKeys) ||
      document.protocol_version !== "operator_verification_plan_item_snapshot.v1" ||
      document.run_id !== runID || document.session_id !== value.session_id ||
      document.workspace_id !== value.workspace_id || document.plan_id !== planID ||
      document.plan_sha256 !== value.plan_sha256 || document.plan_item_ordinal !== ordinal ||
      document.plan_item_sha256 !== value.plan_item_sha256 ||
      document.snapshot_high_water_event_sequence !== value.snapshot_high_water_event_sequence ||
      document.associated_evidence_count !== value.associated_evidence_count ||
      document.pass_count !== value.pass_count || document.fail_count !== value.fail_count ||
      document.unknown_count !== value.unknown_count ||
      document.returned_association_count !== value.returned_association_count ||
      document.associations_truncated !== value.associations_truncated ||
      !Array.isArray(document.associations) ||
      document.associations.length !== value.returned_association_count ||
      document.metadata_only !== true || document.read_only !== true ||
      document.private_plan_body_included !== false ||
      document.private_evidence_bodies_included !== false ||
      document.operator_identity_included !== false || document.result_inferred !== false ||
      document.command_executed !== false || document.model_assertion !== false ||
      document.record_rewritten !== false || document.approval !== false ||
      document.authority_granted !== false || document.mutation_supported !== false ||
      document.execution_started !== false) {
      throw new APIRequestError("Verification snapshot JSON escaped its exact source binding",
        "INVALID_RESPONSE", 502);
    }
    const associationIDs = new Set<string>();
    const evidenceIDs = new Set<string>();
    let previousSequence = Number.MAX_SAFE_INTEGER;
    let returnedPass = 0;
    let returnedFail = 0;
    let returnedUnknown = 0;
    for (const [index, association] of document.associations.entries()) {
      if (!hasExactKeys(association, ["associated_at", "association_event_sequence",
        "evidence_event_sequence", "evidence_id", "evidence_outcome", "id", "plan_id",
        "plan_item_ordinal", "plan_item_sha256"]) || !boundedIdentity(association.id) ||
        associationIDs.has(String(association.id)) || !boundedIdentity(association.evidence_id) ||
        evidenceIDs.has(String(association.evidence_id)) || association.plan_id !== planID ||
        association.plan_item_ordinal !== ordinal ||
        association.plan_item_sha256 !== value.plan_item_sha256 ||
        !["pass", "fail", "unknown"].includes(String(association.evidence_outcome)) ||
        !safePositiveInteger(association.evidence_event_sequence) ||
        !safePositiveInteger(association.association_event_sequence) ||
        association.association_event_sequence <= association.evidence_event_sequence ||
        association.association_event_sequence > value.snapshot_high_water_event_sequence ||
        (index > 0 && association.association_event_sequence >= previousSequence) ||
        (index === 0 && association.association_event_sequence !==
          value.snapshot_high_water_event_sequence) || !validDate(association.associated_at)) {
        throw new APIRequestError("Verification snapshot association escaped its exact binding",
          "INVALID_RESPONSE", 502);
      }
      associationIDs.add(String(association.id));
      evidenceIDs.add(String(association.evidence_id));
      previousSequence = Number(association.association_event_sequence);
      if (association.evidence_outcome === "pass") returnedPass += 1;
      if (association.evidence_outcome === "fail") returnedFail += 1;
      if (association.evidence_outcome === "unknown") returnedUnknown += 1;
    }
    if (returnedPass > value.pass_count || returnedFail > value.fail_count ||
      returnedUnknown > value.unknown_count || (!value.associations_truncated &&
        (returnedPass !== value.pass_count || returnedFail !== value.fail_count ||
          returnedUnknown !== value.unknown_count))) {
      throw new APIRequestError("Verification snapshot outcome counts are inconsistent",
        "INVALID_RESPONSE", 502);
    }
  } else if (!value.content.startsWith("# CyberAgent Verification Snapshot\n") ||
    !value.content.includes(`Run: \`${runID}\``) ||
    !value.content.includes(`Plan: \`${planID}\``) ||
    !value.content.includes(`Item: \`${ordinal}\``) ||
    !value.content.includes(`Snapshot event high-water: \`${value.snapshot_high_water_event_sequence}\``) ||
    !value.content.includes("This is a read-only metadata snapshot.")) {
    throw new APIRequestError("Verification snapshot Markdown omitted its exact source binding",
      "INVALID_RESPONSE", 502);
  }
  return value as unknown as VerificationSnapshotExportView;
}

export function parseVerificationSnapshotReceipt(value: unknown, runID: string,
  sessionID = "", workspaceID = "", control = false): VerificationSnapshotReceiptControlView {
  const keys = ["approval", "associated_evidence_count", "associations_truncated",
    "authority_granted", "content_bytes", "content_included", "content_sha256",
    "execution_started", "fail_count", "format", "id", "immutable", "metadata_only",
    "operator_identity_included", "operator_recorded", "pass_count", "plan_id",
    "plan_item_ordinal", "plan_item_sha256", "plan_sha256", "private_bodies_included",
    "protocol_version", "read_only", "receipt_event_sequence", "record_rewritten",
    "recorded_at", "result_accepted", "result_inferred", "returned_association_count",
    "run_id", "session_id", "snapshot_accepted", "snapshot_high_water_event_sequence",
    "unknown_count", "workspace_id"];
  if (control) keys.push("replayed");
  if (!hasExactKeys(value, keys) ||
    value.protocol_version !== "operator_verification_plan_item_snapshot_receipt.v1" ||
    value.run_id !== runID || !boundedIdentity(value.id) || !boundedIdentity(value.session_id) ||
    !boundedIdentity(value.workspace_id) || (sessionID !== "" && value.session_id !== sessionID) ||
    (workspaceID !== "" && value.workspace_id !== workspaceID) ||
    !boundedIdentity(value.plan_id) || !isSHA256(value.plan_sha256) ||
    !safePositiveInteger(value.plan_item_ordinal) || value.plan_item_ordinal > 32 ||
    !isSHA256(value.plan_item_sha256) || !["json", "markdown"].includes(String(value.format)) ||
    !safeBoundedCount(value.snapshot_high_water_event_sequence, Number.MAX_SAFE_INTEGER) ||
    !safeBoundedCount(value.associated_evidence_count, 1_000_000_000) ||
    !safeBoundedCount(value.pass_count, 1_000_000_000) ||
    !safeBoundedCount(value.fail_count, 1_000_000_000) ||
    !safeBoundedCount(value.unknown_count, 1_000_000_000) ||
    value.pass_count + value.fail_count + value.unknown_count !==
      value.associated_evidence_count ||
    (value.associated_evidence_count === 0) !==
      (value.snapshot_high_water_event_sequence === 0) ||
    !safeBoundedCount(value.returned_association_count, 100) ||
    value.returned_association_count !== Math.min(Number(value.associated_evidence_count), 100) ||
    value.associations_truncated !== (value.associated_evidence_count > 100) ||
    !isSHA256(value.content_sha256) || !safePositiveInteger(value.content_bytes) ||
    value.content_bytes > 256 * 1024 || !safePositiveInteger(value.receipt_event_sequence) ||
    value.receipt_event_sequence <= value.snapshot_high_water_event_sequence ||
    !validDate(value.recorded_at) || value.immutable !== true ||
    value.operator_recorded !== true || value.metadata_only !== true || value.read_only !== true ||
    value.content_included !== false || value.private_bodies_included !== false ||
    value.operator_identity_included !== false || value.snapshot_accepted !== false ||
    value.result_accepted !== false || value.result_inferred !== false ||
    value.record_rewritten !== false || value.approval !== false ||
    value.authority_granted !== false || value.execution_started !== false ||
    (control && typeof value.replayed !== "boolean")) {
    throw new APIRequestError("Verification snapshot receipt widened acceptance or authority",
      "INVALID_RESPONSE", 502);
  }
  return value as unknown as VerificationSnapshotReceiptControlView;
}

export function parseVerificationSnapshotReceiptInventory(value: unknown,
  runID: string): VerificationSnapshotReceiptInventoryView {
  const keys = ["approval", "authority_granted", "execution_started", "items",
    "metadata_only", "protocol_version", "read_only", "record_rewritten", "result_accepted",
    "result_inferred", "run_id", "session_id", "snapshot_accepted", "truncated",
    "workspace_id"];
  if (!hasExactKeys(value, keys) ||
    value.protocol_version !== "operator_verification_plan_item_snapshot_receipt_inventory.v1" ||
    value.run_id !== runID || !boundedIdentity(value.session_id) ||
    !boundedIdentity(value.workspace_id) || !Array.isArray(value.items) ||
    value.items.length > 100 || typeof value.truncated !== "boolean" ||
    value.metadata_only !== true || value.read_only !== true ||
    value.snapshot_accepted !== false || value.result_accepted !== false ||
    value.result_inferred !== false || value.record_rewritten !== false ||
    value.approval !== false || value.authority_granted !== false ||
    value.execution_started !== false) {
    throw new APIRequestError("Verification snapshot receipt history widened its read-only boundary",
      "INVALID_RESPONSE", 502);
  }
  const identities = new Set<string>();
  let previousSequence = Number.MAX_SAFE_INTEGER;
  const items = value.items.map((item) => {
    const parsed = parseVerificationSnapshotReceipt(item, runID, String(value.session_id),
      String(value.workspace_id));
    if (identities.has(parsed.id) || parsed.receipt_event_sequence >= previousSequence) {
      throw new APIRequestError("Verification snapshot receipt history is duplicated or unordered",
        "INVALID_RESPONSE", 502);
    }
    identities.add(parsed.id);
    previousSequence = parsed.receipt_event_sequence;
    return parsed as unknown as VerificationSnapshotReceiptView;
  });
  if (value.truncated && items.length !== 100) {
    throw new APIRequestError("Verification snapshot receipt history truncation is inconsistent",
      "INVALID_RESPONSE", 502);
  }
  return { ...value, items } as unknown as VerificationSnapshotReceiptInventoryView;
}

export function parseVerificationSnapshotReceiptReview(value: unknown, runID: string,
  sessionID = "", workspaceID = "", control = false): VerificationSnapshotReceiptReviewControlView {
  const keys = ["approval", "authority_granted", "content_included", "decision",
    "execution_started", "id", "immutable", "metadata_only", "operator_identity_included",
    "operator_reviewed", "private_bodies_included", "protocol_version", "read_only",
    "receipt_content_sha256", "receipt_event_sequence", "receipt_id", "record_rewritten",
    "result_accepted", "result_inferred", "review_event_sequence", "review_non_authorizing",
    "reviewed_at", "run_id", "session_id", "snapshot_accepted", "workspace_id"];
  if (control) keys.push("replayed");
  if (!hasExactKeys(value, keys) ||
    value.protocol_version !== "operator_verification_plan_item_snapshot_receipt_review.v1" ||
    value.run_id !== runID || !boundedIdentity(value.id) || !boundedIdentity(value.session_id) ||
    !boundedIdentity(value.workspace_id) || !boundedIdentity(value.receipt_id) ||
    (sessionID !== "" && value.session_id !== sessionID) ||
    (workspaceID !== "" && value.workspace_id !== workspaceID) ||
    !isSHA256(value.receipt_content_sha256) ||
    !safePositiveInteger(value.receipt_event_sequence) ||
    !safePositiveInteger(value.review_event_sequence) ||
    value.review_event_sequence <= value.receipt_event_sequence ||
    !["metadata_confirmed", "metadata_disputed"].includes(String(value.decision)) ||
    !validDate(value.reviewed_at) || value.immutable !== true ||
    value.operator_reviewed !== true || value.metadata_only !== true || value.read_only !== true ||
    value.review_non_authorizing !== true || value.content_included !== false ||
    value.private_bodies_included !== false || value.operator_identity_included !== false ||
    value.snapshot_accepted !== false || value.result_accepted !== false ||
    value.result_inferred !== false || value.record_rewritten !== false ||
    value.approval !== false || value.authority_granted !== false ||
    value.execution_started !== false || (control && typeof value.replayed !== "boolean")) {
    throw new APIRequestError("Verification snapshot receipt review widened acceptance or authority",
      "INVALID_RESPONSE", 502);
  }
  return value as unknown as VerificationSnapshotReceiptReviewControlView;
}

export function parseVerificationSnapshotReceiptReviewInventory(value: unknown,
  runID: string): VerificationSnapshotReceiptReviewInventoryView {
  const keys = ["approval", "authority_granted", "execution_started", "items",
    "metadata_only", "protocol_version", "read_only", "record_rewritten",
    "result_accepted", "result_inferred", "review_non_authorizing", "run_id", "session_id",
    "snapshot_accepted", "truncated", "workspace_id"];
  if (!hasExactKeys(value, keys) ||
    value.protocol_version !==
      "operator_verification_plan_item_snapshot_receipt_review_inventory.v1" ||
    value.run_id !== runID || !boundedIdentity(value.session_id) ||
    !boundedIdentity(value.workspace_id) || !Array.isArray(value.items) ||
    value.items.length > 100 || typeof value.truncated !== "boolean" ||
    value.metadata_only !== true || value.read_only !== true ||
    value.review_non_authorizing !== true || value.snapshot_accepted !== false ||
    value.result_accepted !== false || value.result_inferred !== false ||
    value.record_rewritten !== false || value.approval !== false ||
    value.authority_granted !== false || value.execution_started !== false) {
    throw new APIRequestError(
      "Verification snapshot receipt review history widened its non-authorizing boundary",
      "INVALID_RESPONSE", 502);
  }
  const identities = new Set<string>();
  const receiptIDs = new Set<string>();
  let previousSequence = Number.MAX_SAFE_INTEGER;
  const items = value.items.map((item) => {
    const parsed = parseVerificationSnapshotReceiptReview(item, runID, String(value.session_id),
      String(value.workspace_id));
    if (identities.has(parsed.id) || receiptIDs.has(parsed.receipt_id) ||
      parsed.review_event_sequence >= previousSequence) {
      throw new APIRequestError(
        "Verification snapshot receipt review history is duplicated or unordered",
        "INVALID_RESPONSE", 502);
    }
    identities.add(parsed.id);
    receiptIDs.add(parsed.receipt_id);
    previousSequence = parsed.review_event_sequence;
    return parsed as unknown as VerificationSnapshotReceiptReviewView;
  });
  if (value.truncated && items.length !== 100) {
    throw new APIRequestError("Verification snapshot receipt review truncation is inconsistent",
      "INVALID_RESPONSE", 502);
  }
  return { ...value, items } as unknown as VerificationSnapshotReceiptReviewInventoryView;
}
