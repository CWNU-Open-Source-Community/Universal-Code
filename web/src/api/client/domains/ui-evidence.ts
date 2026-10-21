import { APIRequestError } from "../protocol";
import { parseUIEvidenceAttempt, parseUIEvidenceBundle } from "../responses/ui-evidence";
import { boundedIdentity, isSHA256 } from "../validation";
import type {
  UIEvidenceArtifactMetadata,
  UIEvidenceAttempt,
  UIEvidenceBundle,
  UIEvidenceStartView,
} from "../../types";
import type { ClientFeatures } from "../capabilities";
import type { APITransport } from "../transport";

export class UIEvidenceClient {
  constructor(
    private readonly transport: APITransport,
    private readonly features: ClientFeatures,
  ) {}

  async uiEvidence(runID: string, signal?: AbortSignal): Promise<UIEvidenceAttempt[]> {
    if (!boundedIdentity(runID) || runID.trim() !== runID) {
      throw new Error("A normalized Run identity is required");
    }
    const value = await this.transport.get<unknown>(
      `/runs/${encodeURIComponent(runID)}/ui-evidence`, { limit: 100 }, signal);
    if (!Array.isArray(value) || value.length > 500) {
      throw new APIRequestError("UI evidence list is invalid", "INVALID_RESPONSE", 502);
    }
    const attempts = value.map((entry) => parseUIEvidenceAttempt(entry, runID));
    if (new Set(attempts.map((entry) => entry.manifest.attempt_id)).size !== attempts.length) {
      throw new APIRequestError("UI evidence list contains duplicate attempts", "INVALID_RESPONSE", 502);
    }
    return attempts;
  }

  async uiEvidenceBundle(attemptID: string, signal?: AbortSignal): Promise<UIEvidenceBundle> {
    if (!boundedIdentity(attemptID) || attemptID.trim() !== attemptID) {
      throw new Error("A normalized UI evidence attempt identity is required");
    }
    return parseUIEvidenceBundle(await this.transport.get<unknown>(
      `/ui-evidence/${encodeURIComponent(attemptID)}`, {}, signal), attemptID);
  }

  async startUIEvidence(runID: string, body: UIEvidenceStartView,
    signal?: AbortSignal): Promise<UIEvidenceAttempt> {
    if (!this.features.hasUIEvidence || !boundedIdentity(runID) || runID.trim() !== runID ||
      typeof body.operation_key !== "string" || body.operation_key.trim() !== body.operation_key ||
      body.operation_key.length < 1) {
      throw new Error("UI evidence control, a normalized Run, and an operation key are required");
    }
    return parseUIEvidenceAttempt(await this.transport.sendControl<unknown>(
      `/runs/${encodeURIComponent(runID)}/ui-evidence`, body, body.operation_key, signal), runID);
  }

  async cancelUIEvidence(attemptID: string, signal?: AbortSignal): Promise<UIEvidenceAttempt> {
    if (!this.features.hasUIEvidence || !boundedIdentity(attemptID) || attemptID.trim() !== attemptID) {
      throw new Error("UI evidence control and a normalized attempt identity are required");
    }
    return parseUIEvidenceAttempt(await this.transport.sendControl<unknown>(
      `/ui-evidence/${encodeURIComponent(attemptID)}/cancel`, { confirm: true },
      `ui-evidence-cancel-${attemptID}`, signal));
  }

  async downloadUIEvidenceArtifact(attemptID: string, metadata: UIEvidenceArtifactMetadata,
    signal?: AbortSignal): Promise<Blob> {
    if (!boundedIdentity(attemptID) || attemptID !== metadata.attempt_id ||
      !boundedIdentity(metadata.id) || !isSHA256(metadata.sha256) || metadata.untrusted !== true) {
      throw new Error("Exact untrusted UI evidence artifact metadata is required");
    }
    const response = await fetch(this.transport.url(`/ui-evidence/${encodeURIComponent(attemptID)}/artifacts/` +
      encodeURIComponent(metadata.id)), {
      method: "GET",
      headers: { ...this.transport.headers(), Accept: metadata.mime },
      signal,
      cache: "no-store",
      credentials: "omit",
      referrerPolicy: "no-referrer",
    });
    if (!response.ok) throw await this.transport.responseError(response);
    if (response.headers.get("content-type") !== metadata.mime ||
      response.headers.get("etag") !== `"${metadata.sha256}"` ||
      response.headers.get("x-cyberagent-content-sha256") !== metadata.sha256 ||
      response.headers.get("x-cyberagent-evidence-untrusted") !== "true") {
      throw new APIRequestError("UI evidence artifact headers are invalid", "INVALID_RESPONSE",
        response.status, response.headers.get("x-request-id") || "");
    }
    const declaredLength = response.headers.get("content-length");
    if (declaredLength !== null && Number(declaredLength) !== metadata.bytes) {
      throw new APIRequestError("UI evidence artifact byte count changed", "INVALID_RESPONSE", 502);
    }
    if (!response.body) {
      throw new APIRequestError("UI evidence artifact body is unavailable", "INVALID_RESPONSE", 502);
    }
    const bytes = new Uint8Array(metadata.bytes);
    const reader = response.body.getReader();
    let offset = 0;
    try {
      while (true) {
        const { done, value } = await reader.read();
        if (done) break;
        if (offset + value.byteLength > bytes.byteLength) {
          await reader.cancel();
          throw new APIRequestError("UI evidence artifact exceeded its sealed byte count",
            "INVALID_RESPONSE", 502);
        }
        bytes.set(value, offset);
        offset += value.byteLength;
      }
    } finally {
      reader.releaseLock();
    }
    if (offset !== metadata.bytes) {
      throw new APIRequestError("UI evidence artifact byte count changed", "INVALID_RESPONSE", 502);
    }
    const digest = new Uint8Array(await globalThis.crypto.subtle.digest("SHA-256", bytes));
    const digestHex = [...digest].map((byte) => byte.toString(16).padStart(2, "0")).join("");
    if (digestHex !== metadata.sha256) {
      throw new APIRequestError("UI evidence artifact digest verification failed", "INVALID_RESPONSE", 502);
    }
    return new Blob([bytes], { type: metadata.mime });
  }
}
