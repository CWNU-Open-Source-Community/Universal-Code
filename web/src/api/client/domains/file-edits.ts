import { APIRequestError } from "../protocol";
import {
  parseFileEditApply,
  parseFileEditChangeSet,
  parseFileEditPreview,
  parseFileEditProposal,
  parseFileEditProposalRecovery,
  parseFileEditProposalSource,
  parseFileEditQueue,
  parseFileEditRevertProposal,
  parseFileEditReview,
} from "../responses/file-edits";
import { boundedIdentity, isSHA256, validWorkspaceRelativePath } from "../validation";
import type {
  FileEditApplyRequestView,
  FileEditApplyView,
  FileEditChangeSetView,
  FileEditPreviewView,
  FileEditProposalRecoveryView,
  FileEditProposalRequestView,
  FileEditProposalSourceView,
  FileEditProposalView,
  FileEditQueueView,
  FileEditRevertProposalView,
  FileEditReviewRequestView,
  FileEditReviewView,
} from "../../types";
import type { ClientFeatures } from "../capabilities";
import type { APITransport } from "../transport";

export class FileEditsClient {
  constructor(
    private readonly transport: APITransport,
    private readonly features: ClientFeatures,
  ) {}

  async fileEditQueue(runID: string, signal?: AbortSignal): Promise<FileEditQueueView> {
    if (!boundedIdentity(runID) || runID.trim() !== runID) {
      throw new Error("A normalized Run identity is required");
    }
    return parseFileEditQueue(await this.transport.get<unknown>(
      `/runs/${encodeURIComponent(runID)}/file-edits`, {}, signal,
    ), runID);
  }

  async fileEdit(runID: string, editID: string,
    signal?: AbortSignal): Promise<FileEditPreviewView> {
    if (!boundedIdentity(runID) || runID.trim() !== runID ||
      !boundedIdentity(editID) || editID.trim() !== editID) {
      throw new Error("Normalized Run and file edit identities are required");
    }
    const edit = parseFileEditPreview(await this.transport.get<unknown>(
      `/runs/${encodeURIComponent(runID)}/file-edits/${encodeURIComponent(editID)}`,
      {}, signal,
    ));
    if (edit.id !== editID) {
      throw new APIRequestError("File edit preview returned the wrong identity",
        "INVALID_RESPONSE", 502);
    }
    return edit;
  }

  async fileEditChangeSet(runID: string,
    signal?: AbortSignal): Promise<FileEditChangeSetView> {
    if (!boundedIdentity(runID) || runID.trim() !== runID) {
      throw new Error("A normalized Run identity is required");
    }
    return parseFileEditChangeSet(await this.transport.get<unknown>(
      `/runs/${encodeURIComponent(runID)}/file-edit-change-set`, {}, signal,
    ), runID);
  }

  async issueFileEditProposalSource(runID: string, path: string,
    signal?: AbortSignal): Promise<FileEditProposalSourceView> {
    if (!this.features.hasFileEditProposals || !boundedIdentity(runID) ||
      !validWorkspaceRelativePath(path)) {
      throw new Error("File edit proposal capability and a Go-issued path are required");
    }
    return parseFileEditProposalSource(await this.transport.get<unknown>(
      `/runs/${encodeURIComponent(runID)}/file-edit-proposal-source`, { path }, signal,
    ), runID, path);
  }

  async reissueFileEditProposalSource(runID: string, path: string, expectedSHA256: string,
    signal?: AbortSignal): Promise<FileEditProposalSourceView> {
    if (!this.features.hasFileEditProposals || !boundedIdentity(runID) ||
      !validWorkspaceRelativePath(path) || !isSHA256(expectedSHA256)) {
      throw new Error("An exact previously issued file digest is required");
    }
    return parseFileEditProposalSource(await this.transport.get<unknown>(
      `/runs/${encodeURIComponent(runID)}/file-edit-proposal-source`,
      { path, expected_sha256: expectedSHA256 }, signal,
    ), runID, path);
  }

  async recoverFileEditProposal(runID: string, editID: string,
    signal?: AbortSignal): Promise<FileEditProposalRecoveryView> {
    if (!this.features.hasFileEditProposals || !boundedIdentity(runID) || !boundedIdentity(editID)) {
      throw new Error("File edit recovery requires exact Run and proposal identities");
    }
    return parseFileEditProposalRecovery(await this.transport.get<unknown>(
      `/runs/${encodeURIComponent(runID)}/file-edit-proposal-recovery/${encodeURIComponent(editID)}`,
      {}, signal,
    ), runID, editID);
  }

  async createFileEditProposal(runID: string, body: FileEditProposalRequestView,
    signal?: AbortSignal): Promise<FileEditProposalView> {
    if (!this.features.hasFileEditProposals || !boundedIdentity(runID) ||
      body.version !== "file_edit_proposal.v1" ||
      !/^[A-Za-z0-9_-]{43}$/u.test(body.source_handle) ||
      typeof body.proposed_text !== "string" ||
      new TextEncoder().encode(body.proposed_text).length > 256 * 1024) {
      throw new Error("A bounded Go-issued file edit proposal is required");
    }
    return parseFileEditProposal(await this.transport.sendControlRequest<unknown>(
      `/runs/${encodeURIComponent(runID)}/file-edit-proposals`, body, signal,
    ), runID);
  }

  async reviewFileEdit(runID: string, editID: string, body: FileEditReviewRequestView,
    signal?: AbortSignal): Promise<FileEditReviewView> {
    if (!this.features.hasFileEditReview || !boundedIdentity(runID) || !boundedIdentity(editID)) {
      throw new Error("File edit review capability and normalized identities are required");
    }
    const result = await this.transport.sendControlRequest<unknown>(
      `/runs/${encodeURIComponent(runID)}/file-edits/${encodeURIComponent(editID)}/review`,
      body, signal,
    );
    return parseFileEditReview(result, runID, editID, body);
  }

  async applyFileEdit(runID: string, editID: string, body: FileEditApplyRequestView,
    idempotencyKey: string, signal?: AbortSignal): Promise<FileEditApplyView> {
    if (!this.features.hasFileEditApply || !boundedIdentity(runID) || !boundedIdentity(editID)) {
      throw new Error("File edit apply capability and normalized identities are required");
    }
    const result = await this.transport.sendControl<unknown>(
      `/runs/${encodeURIComponent(runID)}/file-edits/${encodeURIComponent(editID)}/apply`,
      body, idempotencyKey, signal,
    );
    return parseFileEditApply(result, runID, editID);
  }

  async createFileEditRevertProposal(runID: string, sourceEditID: string,
    idempotencyKey: string, signal?: AbortSignal): Promise<FileEditRevertProposalView> {
    if (!this.features.hasFileEditReview || !boundedIdentity(runID) || !boundedIdentity(sourceEditID) ||
      runID.trim() !== runID || sourceEditID.trim() !== sourceEditID) {
      throw new Error("File edit review authority and normalized source identities are required");
    }
    return parseFileEditRevertProposal(await this.transport.sendControl<unknown>(
      `/runs/${encodeURIComponent(runID)}/file-edits/${encodeURIComponent(sourceEditID)}/revert-proposal`,
      { version: "file_edit_proposal.v1" }, idempotencyKey, signal,
    ), runID, sourceEditID);
  }
}
