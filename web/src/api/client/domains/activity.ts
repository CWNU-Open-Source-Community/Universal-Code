import { APIRequestError } from "../protocol";
import { parseThreadActivityArtifact, parseThreadActivityDetail } from "../responses/activity";
import { boundedIdentity } from "../validation";
import type { ThreadActivityArtifactView, ThreadActivityDetailView } from "../../types";
import type { ClientFeatures } from "../capabilities";
import type { APITransport } from "../transport";

export class ActivityClient {
  constructor(
    private readonly transport: APITransport,
    private readonly features: ClientFeatures,
  ) {}

  async threadActivityDetail(threadID: string, activityRef: string,
    signal?: AbortSignal): Promise<ThreadActivityDetailView> {
    if (boundedIdentity(threadID) !== threadID || boundedIdentity(activityRef) !== activityRef) {
      throw new Error("Normalized Thread and activity identities are required");
    }
    return parseThreadActivityDetail(await this.transport.get<unknown>(
      `/threads/${encodeURIComponent(threadID)}/activities/${encodeURIComponent(activityRef)}`,
      {}, signal), threadID, activityRef);
  }

  async threadActivityArtifact(threadID: string, activityRef: string, artifactRef: string,
    signal?: AbortSignal): Promise<ThreadActivityArtifactView> {
    if (boundedIdentity(threadID) !== threadID || boundedIdentity(activityRef) !== activityRef ||
      boundedIdentity(artifactRef) !== artifactRef) {
      throw new Error("Normalized Thread activity artifact identities are required");
    }
    const value = parseThreadActivityArtifact(await this.transport.get<unknown>(
      `/threads/${encodeURIComponent(threadID)}/activities/${encodeURIComponent(activityRef)}` +
      `/artifacts/${encodeURIComponent(artifactRef)}`, {}, signal), activityRef, artifactRef);
    const digest = new Uint8Array(await globalThis.crypto.subtle.digest(
      "SHA-256", new TextEncoder().encode(value.content)));
    const digestHex = [...digest].map((byte) => byte.toString(16).padStart(2, "0")).join("");
    if (digestHex !== value.sha256) {
      throw new APIRequestError("Thread activity artifact digest verification failed",
        "INVALID_RESPONSE", 502);
    }
    return value;
  }
}
