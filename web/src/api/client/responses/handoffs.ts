import { parseStandardCodeDelivery } from "../../standard-code-delivery";
import { APIRequestError } from "../protocol";
import {
  boundedIdentity,
  boundedText,
  hasExactKeys,
  hasRequiredOnlyKeys,
  isRecord,
  isSHA256,
  safeBoundedCount,
  safePositiveInteger,
  validDate,
} from "../validation";
import type { CodeHandoffExportView, CodeHandoffView } from "../../types";

export function parseCodeHandoff(value: unknown, runID: string): CodeHandoffView {
  const keys = ["change_set", "composite_mutation", "durable_sources", "execution_started",
    "generated_at", "mission_id", "mode_revision", "pending_action_count", "pending_actions",
    "pending_actions_truncated", "phase", "plan", "private_bodies_included", "protocol_version",
    "queue", "regenerable", "report_references", "report_references_truncated",
    "resume_authorized", "run_id", "run_status", "session_id", "source_event_sequence",
    "surface", "verification", "verification_coverage", "verification_plans",
    "verification_snapshot_receipt_reviews", "workspace_id"];
  const hasStandardCodeDelivery = isRecord(value) &&
    Object.prototype.hasOwnProperty.call(value, "standard_code_delivery");
  if (hasStandardCodeDelivery) keys.push("standard_code_delivery");
  if (isRecord(value) && Object.prototype.hasOwnProperty.call(value, "host_commands")) keys.push("host_commands");
  if (!hasExactKeys(value, keys) || value.protocol_version !== "code_handoff.v1" ||
    value.run_id !== runID || !boundedIdentity(value.mission_id) || !boundedIdentity(value.session_id) ||
    !boundedIdentity(value.workspace_id) || value.surface !== "code" ||
    !["plan", "deliver"].includes(String(value.phase)) ||
    !["created", "preparing", "running", "waiting_approval", "paused", "completed", "failed",
      "cancelled"].includes(String(value.run_status)) || !safePositiveInteger(value.mode_revision) ||
    !safePositiveInteger(value.source_event_sequence) || !validDate(value.generated_at) ||
    value.regenerable !== true || value.durable_sources !== true ||
    value.private_bodies_included !== false || value.composite_mutation !== false ||
    value.resume_authorized !== false || value.execution_started !== false ||
    !isRecord(value.plan) || !isRecord(value.queue) || !isRecord(value.change_set) ||
    !isRecord(value.verification) || !isRecord(value.verification_plans) ||
    !isRecord(value.verification_coverage) ||
    !isRecord(value.verification_snapshot_receipt_reviews) ||
    !Array.isArray(value.pending_actions) ||
    value.pending_actions.length > 20 || !Array.isArray(value.report_references) ||
    value.report_references.length > 20 || !safeBoundedCount(value.pending_action_count, 100) ||
    typeof value.pending_actions_truncated !== "boolean" ||
    typeof value.report_references_truncated !== "boolean") {
    throw new APIRequestError("Code handoff violated its metadata-only boundary",
      "INVALID_RESPONSE", 502);
  }
  const planKeys = ["blocked_count", "cancelled_count", "completed_count", "direction_count",
    "in_progress_count", "module_count", "pending_count", "proposal_id", "selected_direction",
    "selection_id", "state"];
  const plan = value.plan;
  const planCounts = [plan.pending_count, plan.in_progress_count, plan.blocked_count,
    plan.completed_count, plan.cancelled_count];
  if (!hasRequiredOnlyKeys(plan, planKeys, ["manual_acceptance"]) || !["none", "proposed", "selected"].includes(String(plan.state)) ||
    (plan.manual_acceptance !== undefined && (plan.state !== "selected" ||
      (plan.manual_acceptance !== "required" && plan.manual_acceptance !== "on_demand"))) ||
    typeof plan.proposal_id !== "string" || typeof plan.selection_id !== "string" ||
    !safeBoundedCount(plan.direction_count, 3) || !safeBoundedCount(plan.selected_direction, 3) ||
    !safeBoundedCount(plan.module_count, 8) ||
    planCounts.some((count) => !safeBoundedCount(count, 8)) ||
    planCounts.reduce<number>((total, count) => total + Number(count), 0) !== plan.module_count ||
    (plan.state === "none" && (plan.proposal_id !== "" || plan.selection_id !== "" ||
      plan.direction_count !== 0 || plan.selected_direction !== 0 || plan.module_count !== 0)) ||
    (plan.state === "proposed" && (!boundedIdentity(plan.proposal_id) || plan.selection_id !== "" ||
      plan.direction_count < 1 || plan.selected_direction !== 0 || plan.module_count !== 0)) ||
    (plan.state === "selected" && (!boundedIdentity(plan.proposal_id) ||
      !boundedIdentity(plan.selection_id) || plan.direction_count < 1 ||
      plan.selected_direction < 1 || plan.selected_direction > plan.direction_count))) {
    throw new APIRequestError("Code handoff Plan summary is inconsistent", "INVALID_RESPONSE", 502);
  }
  const queue = value.queue;
  if (!hasExactKeys(queue, ["cancelled", "committed", "pending", "prepared"]) ||
    [queue.pending, queue.prepared, queue.committed, queue.cancelled]
      .some((count) => !safeBoundedCount(count, Number.MAX_SAFE_INTEGER))) {
    throw new APIRequestError("Code handoff queue summary is invalid", "INVALID_RESPONSE", 502);
  }
  const changeSet = value.change_set;
  const changeCounts = [changeSet.proposed, changeSet.approved, changeSet.applied,
    changeSet.denied, changeSet.failed];
  if (!hasExactKeys(changeSet, ["applied", "approved", "denied", "failed", "proposed",
    "returned_count", "total_diff_bytes", "truncated"]) ||
    changeCounts.some((count) => !safeBoundedCount(count, 100)) ||
    !safeBoundedCount(changeSet.returned_count, 100) ||
    changeCounts.reduce<number>((total, count) => total + Number(count), 0) !==
      changeSet.returned_count ||
    !safeBoundedCount(changeSet.total_diff_bytes, 110 * 1024 * 1024) ||
    typeof changeSet.truncated !== "boolean") {
    throw new APIRequestError("Code handoff change-set summary is invalid", "INVALID_RESPONSE", 502);
  }
  const verification = value.verification;
  if (!hasExactKeys(verification, ["fail_count", "pass_count", "references", "returned_count",
    "truncated", "unknown_count"]) || !Array.isArray(verification.references) ||
    verification.references.length > 20 || !safeBoundedCount(verification.returned_count, 100) ||
    !safeBoundedCount(verification.pass_count, 100) || !safeBoundedCount(verification.fail_count, 100) ||
    !safeBoundedCount(verification.unknown_count, 100) ||
    verification.pass_count + verification.fail_count + verification.unknown_count !==
      verification.returned_count || typeof verification.truncated !== "boolean" ||
    verification.references.length !== Math.min(Number(verification.returned_count), 20) ||
    verification.truncated !== (verification.returned_count > 20)) {
    throw new APIRequestError("Code handoff verification summary is invalid", "INVALID_RESPONSE", 502);
  }
  const verificationIDs = new Set<string>();
  for (const reference of verification.references) {
    if (!hasExactKeys(reference, ["id", "outcome", "recorded_at", "redacted"]) ||
      !boundedIdentity(reference.id) || verificationIDs.has(String(reference.id)) ||
      !["pass", "fail", "unknown"].includes(String(reference.outcome)) ||
      typeof reference.redacted !== "boolean" || !validDate(reference.recorded_at)) {
      throw new APIRequestError("Code handoff verification reference is invalid",
        "INVALID_RESPONSE", 502);
    }
    verificationIDs.add(String(reference.id));
  }
  const verificationPlans = value.verification_plans;
  if (!hasExactKeys(verificationPlans, ["references", "returned_count", "truncated"]) ||
    !Array.isArray(verificationPlans.references) || verificationPlans.references.length > 20 ||
    !safeBoundedCount(verificationPlans.returned_count, 50) ||
    typeof verificationPlans.truncated !== "boolean" ||
    verificationPlans.references.length !== Math.min(Number(verificationPlans.returned_count), 20) ||
    verificationPlans.truncated !== (verificationPlans.returned_count > 20)) {
    throw new APIRequestError("Code handoff verification plan summary is invalid",
      "INVALID_RESPONSE", 502);
  }
  const verificationPlanIDs = new Set<string>();
  for (const reference of verificationPlans.references) {
    if (!hasExactKeys(reference, ["created_at", "id", "item_count", "plan_sha256", "redacted"]) ||
      !boundedIdentity(reference.id) || verificationPlanIDs.has(String(reference.id)) ||
      !isSHA256(reference.plan_sha256) || !safeBoundedCount(reference.item_count, 32) ||
      reference.item_count < 1 || typeof reference.redacted !== "boolean" ||
      !validDate(reference.created_at)) {
      throw new APIRequestError("Code handoff verification plan reference is invalid",
        "INVALID_RESPONSE", 502);
    }
    verificationPlanIDs.add(String(reference.id));
  }
  const coverage = value.verification_coverage;
  const coverageKeys = ["associated_evidence_count", "contradictory_item_count", "items",
    "metadata_only", "observed_plan_item_count", "plan_count", "plan_item_count",
    "private_bodies_included", "protocol_version", "read_only", "result_inferred",
    "returned_item_count", "truncated", "unobserved_plan_item_count"];
  if (!hasExactKeys(coverage, coverageKeys) ||
    coverage.protocol_version !== "operator_verification_plan_coverage.v1" ||
    !safeBoundedCount(coverage.plan_count, 50) ||
    !safeBoundedCount(coverage.plan_item_count, 50 * 32) ||
    !safeBoundedCount(coverage.observed_plan_item_count, 50 * 32) ||
    !safeBoundedCount(coverage.unobserved_plan_item_count, 50 * 32) ||
    coverage.observed_plan_item_count + coverage.unobserved_plan_item_count !==
      coverage.plan_item_count ||
    !safeBoundedCount(coverage.associated_evidence_count, 1_000_000_000) ||
    !safeBoundedCount(coverage.contradictory_item_count, 50 * 32) ||
    coverage.contradictory_item_count > coverage.observed_plan_item_count ||
    !safeBoundedCount(coverage.returned_item_count, 100) || !Array.isArray(coverage.items) ||
    coverage.items.length !== coverage.returned_item_count || coverage.items.length > 100 ||
    coverage.returned_item_count !== Math.min(Number(coverage.plan_item_count), 100) ||
    typeof coverage.truncated !== "boolean" ||
    (coverage.plan_item_count > 100 && !coverage.truncated) ||
    coverage.metadata_only !== true || coverage.read_only !== true ||
    coverage.result_inferred !== false || coverage.private_bodies_included !== false) {
    throw new APIRequestError("Code handoff verification coverage widened result authority",
      "INVALID_RESPONSE", 502);
  }
  const coverageItems = new Set<string>();
  let returnedObserved = 0;
  let returnedEvidence = 0;
  let returnedContradictory = 0;
  for (const item of coverage.items) {
    if (!hasExactKeys(item, ["associated_evidence_count", "fail_count", "item_sha256",
      "latest_association_event_sequence", "ordinal", "pass_count", "plan_id", "plan_sha256",
      "unknown_count"]) || !boundedIdentity(item.plan_id) || !isSHA256(item.plan_sha256) ||
      !safePositiveInteger(item.ordinal) || item.ordinal > 32 || !isSHA256(item.item_sha256) ||
      !safeBoundedCount(item.associated_evidence_count, 1_000_000_000) ||
      !safeBoundedCount(item.pass_count, 1_000_000_000) ||
      !safeBoundedCount(item.fail_count, 1_000_000_000) ||
      !safeBoundedCount(item.unknown_count, 1_000_000_000) ||
      item.pass_count + item.fail_count + item.unknown_count !== item.associated_evidence_count ||
      !safeBoundedCount(item.latest_association_event_sequence, Number.MAX_SAFE_INTEGER) ||
      (item.associated_evidence_count === 0) !==
        (item.latest_association_event_sequence === 0)) {
      throw new APIRequestError("Code handoff verification coverage item is inconsistent",
        "INVALID_RESPONSE", 502);
    }
    const key = `${String(item.plan_id)}:${String(item.ordinal)}`;
    if (coverageItems.has(key)) {
      throw new APIRequestError("Code handoff verification coverage duplicated a plan item",
        "INVALID_RESPONSE", 502);
    }
    coverageItems.add(key);
    returnedObserved += item.associated_evidence_count > 0 ? 1 : 0;
    returnedEvidence += Number(item.associated_evidence_count);
    returnedContradictory += item.pass_count > 0 && item.fail_count > 0 ? 1 : 0;
  }
  if (returnedObserved > coverage.observed_plan_item_count ||
    returnedEvidence > coverage.associated_evidence_count ||
    returnedContradictory > coverage.contradictory_item_count ||
    (!coverage.truncated && (returnedObserved !== coverage.observed_plan_item_count ||
      returnedEvidence !== coverage.associated_evidence_count ||
      returnedContradictory !== coverage.contradictory_item_count))) {
    throw new APIRequestError("Code handoff verification coverage totals are inconsistent",
      "INVALID_RESPONSE", 502);
  }
  const reviews = value.verification_snapshot_receipt_reviews;
  const reviewKeys = ["approval", "authority_granted", "content_included", "execution_started",
    "metadata_confirmed_count", "metadata_disputed_count", "metadata_only",
    "operator_identity_included", "private_bodies_included", "protocol_version", "read_only",
    "record_rewritten", "references", "result_accepted", "result_inferred",
    "returned_count", "review_non_authorizing", "snapshot_accepted", "truncated"];
  if (!hasExactKeys(reviews, reviewKeys) || reviews.protocol_version !==
      "operator_verification_plan_item_snapshot_receipt_review_inventory.v1" ||
    !safeBoundedCount(reviews.metadata_confirmed_count, 100) ||
    !safeBoundedCount(reviews.metadata_disputed_count, 100) ||
    !safeBoundedCount(reviews.returned_count, 100) ||
    reviews.metadata_confirmed_count + reviews.metadata_disputed_count !== reviews.returned_count ||
    !Array.isArray(reviews.references) || reviews.references.length > 20 ||
    reviews.references.length !== Math.min(reviews.returned_count, 20) ||
    reviews.truncated !== (reviews.returned_count > 20) || reviews.metadata_only !== true ||
    reviews.read_only !== true || reviews.review_non_authorizing !== true ||
    reviews.content_included !== false || reviews.private_bodies_included !== false ||
    reviews.operator_identity_included !== false || reviews.snapshot_accepted !== false ||
    reviews.result_accepted !== false || reviews.result_inferred !== false ||
    reviews.record_rewritten !== false || reviews.approval !== false ||
    reviews.authority_granted !== false || reviews.execution_started !== false) {
    throw new APIRequestError("Code handoff receipt reviews widened authority",
      "INVALID_RESPONSE", 502);
  }
  const reviewIDs = new Set<string>();
  const reviewedReceiptIDs = new Set<string>();
  let previousReviewSequence: number | null = null;
  let returnedConfirmed = 0;
  let returnedDisputed = 0;
  for (const review of reviews.references) {
    if (!hasExactKeys(review, ["decision", "id", "receipt_content_sha256",
      "receipt_event_sequence", "receipt_id", "review_event_sequence", "reviewed_at"]) ||
      !boundedIdentity(review.id) || reviewIDs.has(String(review.id)) ||
      !boundedIdentity(review.receipt_id) || reviewedReceiptIDs.has(String(review.receipt_id)) ||
      !isSHA256(review.receipt_content_sha256) ||
      !safePositiveInteger(review.receipt_event_sequence) ||
      !safePositiveInteger(review.review_event_sequence) ||
      review.review_event_sequence <= review.receipt_event_sequence ||
      review.review_event_sequence > value.source_event_sequence ||
      (previousReviewSequence !== null && review.review_event_sequence >= previousReviewSequence) ||
      !["metadata_confirmed", "metadata_disputed"].includes(String(review.decision)) ||
      !validDate(review.reviewed_at)) {
      throw new APIRequestError("Code handoff receipt review reference is invalid",
        "INVALID_RESPONSE", 502);
    }
    reviewIDs.add(String(review.id));
    reviewedReceiptIDs.add(String(review.receipt_id));
    previousReviewSequence = review.review_event_sequence;
    returnedConfirmed += review.decision === "metadata_confirmed" ? 1 : 0;
    returnedDisputed += review.decision === "metadata_disputed" ? 1 : 0;
  }
  if (returnedConfirmed > reviews.metadata_confirmed_count ||
    returnedDisputed > reviews.metadata_disputed_count ||
    (!reviews.truncated && (returnedConfirmed !== reviews.metadata_confirmed_count ||
      returnedDisputed !== reviews.metadata_disputed_count))) {
    throw new APIRequestError("Code handoff receipt review totals are inconsistent",
      "INVALID_RESPONSE", 502);
  }
  const actionMapping = {
    steering_pending: ["pending", "queue"], approval_pending: ["pending", "approvals"],
    file_edit_review: ["proposed", "diffs"], file_edit_apply: ["approved", "diffs"],
    wake_due: ["queued", "wake"],
  } as const;
  if (value.pending_actions.length !== Math.min(Number(value.pending_action_count), 20) ||
    value.pending_actions_truncated !== (value.pending_action_count > 20) ||
    (value.report_references_truncated && value.report_references.length !== 20)) {
    throw new APIRequestError("Code handoff reference summary is invalid", "INVALID_RESPONSE", 502);
  }
  const actionIDs = new Set<string>();
  for (const action of value.pending_actions) {
    const hasDueAt = Object.prototype.hasOwnProperty.call(action, "due_at");
    if (!hasExactKeys(action, hasDueAt ?
      ["available_at", "destination", "due_at", "id", "kind", "state"] :
      ["available_at", "destination", "id", "kind", "state"]) ||
      !boundedIdentity(action.id) || !String(action.id).startsWith("action-") ||
      actionIDs.has(String(action.id)) ||
      !Object.prototype.hasOwnProperty.call(actionMapping, String(action.kind)) ||
      !validDate(action.available_at)) {
      throw new APIRequestError("Code handoff action reference is invalid",
        "INVALID_RESPONSE", 502);
    }
    const expected = actionMapping[action.kind as keyof typeof actionMapping];
    if (action.state !== expected[0] || action.destination !== expected[1] ||
      (action.kind === "wake_due" ? !validDate(action.due_at) : hasDueAt)) {
      throw new APIRequestError("Code handoff action reference widened navigation",
        "INVALID_RESPONSE", 502);
    }
    actionIDs.add(String(action.id));
  }
  const reportIDs = new Set<string>();
  for (const report of value.report_references) {
    if (!hasExactKeys(report, ["created_at", "finding_count", "id", "status"]) ||
      !boundedIdentity(report.id) || reportIDs.has(String(report.id)) ||
      report.status !== "generated" || !safeBoundedCount(report.finding_count, 10_000) ||
      !validDate(report.created_at)) {
      throw new APIRequestError("Code handoff report reference is invalid",
        "INVALID_RESPONSE", 502);
    }
    reportIDs.add(String(report.id));
  }
  if (value.host_commands !== undefined) {
    const commands = value.host_commands;
    if (!hasExactKeys(commands, ["items", "truncated"]) || !Array.isArray(commands.items) ||
      commands.items.length > 20 || typeof commands.truncated !== "boolean") {
      throw new APIRequestError("Code handoff host commands are invalid", "INVALID_RESPONSE", 502);
    }
    const ids = new Set<string>();
    for (const item of commands.items) {
      if (!hasRequiredOnlyKeys(item, ["proposal_id", "run_id", "session_id", "workspace_id", "purpose",
        "working_directory", "spec_fingerprint", "created_at"], ["review_id", "review_decision",
        "result_id", "result_status", "source_ref", "content_sha256", "receipt"]) ||
        !boundedIdentity(item.proposal_id) || ids.has(String(item.proposal_id)) || item.run_id !== runID ||
        item.session_id !== value.session_id || item.workspace_id !== value.workspace_id ||
        !boundedText(item.purpose, 4096) || !boundedText(item.working_directory, 4096) ||
        !isSHA256(item.spec_fingerprint) || !validDate(item.created_at) ||
        (item.review_id === undefined ? item.review_decision !== undefined :
          !boundedIdentity(item.review_id) || !["approve", "deny"].includes(String(item.review_decision)))) {
        throw new APIRequestError("Code handoff host command binding is invalid", "INVALID_RESPONSE", 502);
      }
      ids.add(String(item.proposal_id));
      if (item.receipt === undefined) {
        if ([item.result_id, item.result_status, item.source_ref, item.content_sha256].some((field) => field !== undefined)) {
          throw new APIRequestError("Code handoff host result has no receipt", "INVALID_RESPONSE", 502);
        }
        continue;
      }
      const receipt = item.receipt;
      if (!hasExactKeys(receipt, ["request_id", "exit_code", "timed_out", "cancelled", "stdout_truncated",
        "stderr_truncated", "output_limit_exceeded", "tree_reaped", "non_sandboxed", "started_at", "completed_at"]) ||
        item.review_decision !== "approve" || !boundedIdentity(item.result_id) ||
        item.source_ref !== `host-command-proposal:${item.proposal_id}` || !isSHA256(item.content_sha256) ||
        !boundedIdentity(receipt.request_id) || !Number.isSafeInteger(receipt.exit_code) ||
        [receipt.timed_out, receipt.cancelled, receipt.stdout_truncated, receipt.stderr_truncated,
          receipt.output_limit_exceeded].some((field) => typeof field !== "boolean") ||
        (receipt.timed_out && receipt.cancelled) || receipt.tree_reaped !== true || receipt.non_sandboxed !== true ||
        !validDate(receipt.started_at) || !validDate(receipt.completed_at) ||
        Date.parse(receipt.completed_at) < Date.parse(receipt.started_at) ||
        item.result_status !== (receipt.exit_code !== 0 || receipt.timed_out || receipt.cancelled ||
          receipt.output_limit_exceeded ? "failed" : "completed")) {
        throw new APIRequestError("Code handoff host receipt is inconsistent", "INVALID_RESPONSE", 502);
      }
    }
  }
  if (hasStandardCodeDelivery) {
    return { ...value,
      standard_code_delivery: parseStandardCodeDelivery(value.standard_code_delivery, runID),
    } as unknown as CodeHandoffView;
  }
  return value as unknown as CodeHandoffView;
}

export async function parseCodeHandoffExport(value: unknown, runID: string,
  format: "json" | "markdown"): Promise<CodeHandoffExportView> {
  const keys = ["content", "content_bytes", "content_sha256", "download_only",
    "execution_started", "filename", "format", "generated_at", "mime_type",
    "mutation_supported", "private_bodies", "protocol_version", "read_only",
    "report_acceptance", "resume_authorized", "run_id", "source_event_sequence"];
  if (!hasExactKeys(value, keys) || value.protocol_version !== "code_handoff_export.v1" ||
    value.run_id !== runID || value.format !== format || !validDate(value.generated_at) ||
    !safePositiveInteger(value.source_event_sequence) || typeof value.filename !== "string" ||
    value.filename.length < 1 || value.filename.length > 255 || /[\\/:*?"<>|\u0000-\u001f]/u.test(value.filename) ||
    typeof value.mime_type !== "string" || typeof value.content !== "string" ||
    !safePositiveInteger(value.content_bytes) || value.content_bytes > 256 * 1024 ||
    !isSHA256(value.content_sha256) || value.read_only !== true || value.download_only !== true ||
    value.private_bodies !== false || value.resume_authorized !== false ||
    value.mutation_supported !== false || value.report_acceptance !== false ||
    value.execution_started !== false) {
    throw new APIRequestError("Code handoff export violated its download-only boundary",
      "INVALID_RESPONSE", 502);
  }
  const expectedMIME = format === "json" ? "application/json" : "text/markdown; charset=utf-8";
  const expectedSuffix = format === "json" ? ".json" : ".md";
  const encoded = new TextEncoder().encode(value.content);
  if (value.mime_type !== expectedMIME || !value.filename.endsWith(expectedSuffix) ||
    encoded.length !== value.content_bytes) {
    throw new APIRequestError("Code handoff export metadata does not match its content",
      "INVALID_RESPONSE", 502);
  }
  const digest = new Uint8Array(await globalThis.crypto.subtle.digest("SHA-256", encoded));
  const digestHex = [...digest].map((byte) => byte.toString(16).padStart(2, "0")).join("");
  if (digestHex !== value.content_sha256) {
    throw new APIRequestError("Code handoff export digest verification failed",
      "INVALID_RESPONSE", 502);
  }
  if (format === "json") {
    let document: unknown;
    try {
      document = JSON.parse(value.content);
    } catch {
      throw new APIRequestError("Code handoff JSON export is invalid", "INVALID_RESPONSE", 502);
    }
    if (!isRecord(document) || document.protocol_version !== "code_handoff.v1" ||
      document.run_id !== runID || document.source_event_sequence !== value.source_event_sequence ||
      !isRecord(document.verification_coverage) ||
      document.verification_coverage.protocol_version !==
        "operator_verification_plan_coverage.v1" ||
      document.verification_coverage.result_inferred !== false ||
      !isRecord(document.verification_snapshot_receipt_reviews) ||
      document.verification_snapshot_receipt_reviews.protocol_version !==
        "operator_verification_plan_item_snapshot_receipt_review_inventory.v1" ||
      document.verification_snapshot_receipt_reviews.metadata_only !== true ||
      document.verification_snapshot_receipt_reviews.read_only !== true ||
      document.verification_snapshot_receipt_reviews.review_non_authorizing !== true ||
      document.verification_snapshot_receipt_reviews.content_included !== false ||
      document.verification_snapshot_receipt_reviews.private_bodies_included !== false ||
      document.verification_snapshot_receipt_reviews.operator_identity_included !== false ||
      document.verification_snapshot_receipt_reviews.snapshot_accepted !== false ||
      document.verification_snapshot_receipt_reviews.result_accepted !== false ||
      document.verification_snapshot_receipt_reviews.result_inferred !== false ||
      document.verification_snapshot_receipt_reviews.record_rewritten !== false ||
      document.verification_snapshot_receipt_reviews.approval !== false ||
      document.verification_snapshot_receipt_reviews.authority_granted !== false ||
      document.verification_snapshot_receipt_reviews.execution_started !== false) {
      throw new APIRequestError("Code handoff JSON export escaped its source binding",
        "INVALID_RESPONSE", 502);
    }
  } else if (!value.content.startsWith("# CyberAgent Code Handoff\n") ||
    !value.content.includes(`Source event high-water: \`${value.source_event_sequence}\``) ||
    !value.content.includes("Coverage:") ||
    !value.content.includes("Receipt metadata reviews:")) {
    throw new APIRequestError("Code handoff Markdown export omitted its source binding",
      "INVALID_RESPONSE", 502);
  }
  return value as unknown as CodeHandoffExportView;
}
