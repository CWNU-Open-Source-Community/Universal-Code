import { APIRequestError } from "../protocol";
import { parseEvidenceAttachment, parseEvidenceInventory } from "../responses/evidence";
import { parseOperationReceiptHistory, parseOperatorActionCenter } from "../responses/operations";
import { boundedIdentity, isRecord, isSHA256, validWorkspaceRelativePath } from "../validation";
import type {
  ArtifactView,
  EvidenceAttachmentRequestView,
  EvidenceAttachmentView,
  EvidenceInventoryView,
  NoteView,
  OperationReceiptHistoryView,
  OperatorActionCenterView,
  WorkItemView,
} from "../../types";
import type { ClientFeatures } from "../capabilities";
import type { APITransport } from "../transport";

export class EvidenceClient {
  constructor(
    private readonly transport: APITransport,
    private readonly features: ClientFeatures,
  ) {}

  async getArtifact(artifactID: string, signal?: AbortSignal): Promise<ArtifactView> {
    const normalized = boundedIdentity(artifactID);
    if (!normalized || normalized !== artifactID) {
      throw new Error("A normalized artifact identity is required");
    }
    const value = await this.transport.get<unknown>(
      `/artifacts/${encodeURIComponent(artifactID)}`, {}, signal,
    );
    if (!isRecord(value) || value.id !== artifactID || !boundedIdentity(value.run_id)) {
      throw new APIRequestError("Artifact response is invalid", "INVALID_RESPONSE", 502);
    }
    return value as unknown as ArtifactView;
  }

  async getNote(noteID: string, signal?: AbortSignal): Promise<NoteView> {
    const normalized = boundedIdentity(noteID);
    if (!normalized || normalized !== noteID) {
      throw new Error("A normalized note identity is required");
    }
    const value = await this.transport.get<unknown>(
      `/notes/${encodeURIComponent(noteID)}`, {}, signal,
    );
    if (!isRecord(value) || value.id !== noteID || !boundedIdentity(value.run_id)) {
      throw new APIRequestError("Note response is invalid", "INVALID_RESPONSE", 502);
    }
    return value as unknown as NoteView;
  }

  async getWorkItem(workItemID: string, signal?: AbortSignal): Promise<WorkItemView> {
    const normalized = boundedIdentity(workItemID);
    if (!normalized || normalized !== workItemID) {
      throw new Error("A normalized Plan item identity is required");
    }
    const value = await this.transport.get<unknown>(
      `/work-items/${encodeURIComponent(workItemID)}`, {}, signal,
    );
    if (!isRecord(value) || value.id !== workItemID || !boundedIdentity(value.run_id)) {
      throw new APIRequestError("Plan item response is invalid", "INVALID_RESPONSE", 502);
    }
    return value as unknown as WorkItemView;
  }

  async operationReceiptHistory(runID = "",
    signal?: AbortSignal): Promise<OperationReceiptHistoryView> {
    if (runID !== "" && (!boundedIdentity(runID) || runID.trim() !== runID)) {
      throw new Error("A normalized Run identity is required");
    }
    return parseOperationReceiptHistory(await this.transport.get<unknown>(
      "/operation-receipts", { run_id: runID || undefined, limit: 100 }, signal,
    ), runID);
  }

  async operatorActionCenter(runID: string,
    signal?: AbortSignal): Promise<OperatorActionCenterView> {
    if (!boundedIdentity(runID) || runID.trim() !== runID) {
      throw new Error("A normalized Run identity is required");
    }
    return parseOperatorActionCenter(await this.transport.get<unknown>(
      `/runs/${encodeURIComponent(runID)}/operator-actions`, {}, signal,
    ), runID);
  }

  async evidenceInventory(runID: string,
    signal?: AbortSignal): Promise<EvidenceInventoryView> {
    if (!boundedIdentity(runID) || runID.trim() !== runID) {
      throw new Error("A normalized Run identity is required");
    }
    return parseEvidenceInventory(await this.transport.get<unknown>(
      `/runs/${encodeURIComponent(runID)}/evidence-attachments`, {}, signal,
    ), runID);
  }

  async attachEvidence(runID: string, body: EvidenceAttachmentRequestView,
    idempotencyKey: string, signal?: AbortSignal): Promise<EvidenceAttachmentView> {
    if (!this.features.hasEvidenceAttachment || !boundedIdentity(runID) ||
      body.version !== "session_evidence_attachment.v1" ||
      body.source_kind !== "workspace_file" || !validWorkspaceRelativePath(body.source_ref) ||
      !isSHA256(body.content_sha256)) {
      throw new Error("Evidence attachment capability and exact Workspace provenance are required");
    }
    return parseEvidenceAttachment(await this.transport.sendControl<unknown>(
      `/runs/${encodeURIComponent(runID)}/evidence-attachments`, body, idempotencyKey, signal,
    ), runID, body);
  }
}
