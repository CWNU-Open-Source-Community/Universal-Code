import { APIRequestError } from "../protocol";
import {
  boundedIdentity,
  hasExactKeys,
  isSHA256,
  safePositiveInteger,
  validDate,
  validWorkspaceRelativePath,
} from "../validation";
import type {
  EvidenceAttachmentRequestView,
  EvidenceAttachmentView,
  EvidenceInventoryView,
} from "../../types";

export function parseEvidenceAttachment(value: unknown, runID: string,
  request: EvidenceAttachmentRequestView): EvidenceAttachmentView {
  if (!hasExactKeys(value, ["attachment_id", "capability_grant", "content_sha256",
    "execution_started", "instruction_authorized", "model_called", "protocol_version",
    "replayed", "run_id", "session_id", "session_message_id", "source_kind", "source_ref",
    "tool_called", "workspace_id"]) ||
    value.protocol_version !== "session_evidence_attachment.v1" || value.run_id !== runID ||
    value.source_kind !== "workspace_file" || value.source_kind !== request.source_kind ||
    value.source_ref !== request.source_ref || value.content_sha256 !== request.content_sha256 ||
    !boundedIdentity(value.attachment_id) || !boundedIdentity(value.session_id) ||
    !boundedIdentity(value.workspace_id) || !safePositiveInteger(value.session_message_id) ||
    value.instruction_authorized !== false || typeof value.replayed !== "boolean" ||
    value.execution_started !== false || value.model_called !== false ||
    value.tool_called !== false || value.capability_grant !== false) {
    throw new APIRequestError("Evidence attachment response widened document authority",
      "INVALID_RESPONSE", 502);
  }
  return value as unknown as EvidenceAttachmentView;
}

export function parseEvidenceInventory(value: unknown, runID: string): EvidenceInventoryView {
  if (!hasExactKeys(value, ["items", "protocol_version", "run_id", "truncated"]) ||
    value.protocol_version !== "session_evidence_inventory.v1" || value.run_id !== runID ||
    !Array.isArray(value.items) || value.items.length > 100 ||
    typeof value.truncated !== "boolean") {
    throw new APIRequestError("Evidence inventory response violated its metadata-only contract",
      "INVALID_RESPONSE", 502);
  }
  const identities = new Set<string>();
  const items = value.items.map((item) => {
    if (!hasExactKeys(item, ["attached_at", "attachment_id", "content_sha256",
      "instruction_authorized", "run_id", "session_id", "source_kind", "source_ref",
      "workspace_id"]) || !boundedIdentity(item.attachment_id) ||
      identities.has(String(item.attachment_id)) || item.run_id !== runID ||
      !boundedIdentity(item.session_id) || !boundedIdentity(item.workspace_id) ||
      item.source_kind !== "workspace_file" || !validWorkspaceRelativePath(item.source_ref) ||
      !isSHA256(item.content_sha256) || item.instruction_authorized !== false ||
      !validDate(item.attached_at)) {
      throw new APIRequestError("Evidence inventory item widened document or renderer authority",
        "INVALID_RESPONSE", 502);
    }
    identities.add(String(item.attachment_id));
    return item;
  });
  return { ...value, items } as unknown as EvidenceInventoryView;
}
