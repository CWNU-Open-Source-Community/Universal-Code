import { APIRequestError } from "../protocol";
import { validCanonicalBase64 } from "../responses/extensions";
import {
  parseSkillCatalogPublisher,
  parseSkillPackageInstall,
  parseSkillPackageInventory,
  parseSkillPackageRemoval,
} from "../responses/skills";
import { boundedIdentity, boundedText, hasExactKeys, isRecord, isSHA256, validDate } from "../validation";
import type {
  ExternalSkillProjectionView,
  SkillCatalogPublisherControlRequestView,
  SkillCatalogPublisherView,
  SkillCatalogPublishersView,
  SkillPackageInstallRequestView,
  SkillPackageInstallResult,
  SkillPackageInventoryView,
  SkillPackageRemovalView,
  SkillPackageRemoveRequestView,
} from "../../types";
import type { ClientFeatures } from "../capabilities";
import type { QueryValue } from "../protocol";
import type { APITransport } from "../transport";

export class SkillsClient {
  constructor(
    private readonly transport: APITransport,
    private readonly features: ClientFeatures,
  ) {}

  async getRunExternalSkills(runID: string,
    signal?: AbortSignal): Promise<ExternalSkillProjectionView> {
    const normalized = boundedIdentity(runID);
    if (!normalized || normalized !== runID) {
      throw new Error("A normalized Run identity is required");
    }
    const value = await this.transport.get<unknown>(
      `/runs/${encodeURIComponent(runID)}/external-skills`, {}, signal,
    );
    if (!isRecord(value) || value.run_id !== runID ||
      value.protocol_version !== "external_skill_projection.v1") {
      throw new APIRequestError("External skill projection response is invalid",
        "INVALID_RESPONSE", 502);
    }
    return value as unknown as ExternalSkillProjectionView;
  }

  async installSkillPackage(body: SkillPackageInstallRequestView,
    idempotencyKey: string, signal?: AbortSignal): Promise<SkillPackageInstallResult> {
    if (!this.features.hasSkillInstallation || body.confirm_untrusted !== true) {
      throw new Error("Explicit untrusted Skill installation capability is required");
    }
    return parseSkillPackageInstall(await this.transport.sendControl<unknown>(
      "/skills/packages/install", body, idempotencyKey, signal,
    ), body);
  }

  async listSkillCatalogPublishers(signal?: AbortSignal): Promise<SkillCatalogPublishersView> {
    const value = await this.transport.get<unknown>("/skills/catalog/publishers", {}, signal);
    if (!hasExactKeys(value, ["protocol_version", "publishers", "control_enabled"]) ||
      value.protocol_version !== "skill-catalog-control.v1" || typeof value.control_enabled !== "boolean" || !Array.isArray(value.publishers)) {
      throw new APIRequestError("Skill Catalog inventory is invalid", "INVALID_RESPONSE", 502);
    }
    const publishers = value.publishers.map(parseSkillCatalogPublisher);
    if (new Set(publishers.map((item) => item.fingerprint)).size !== publishers.length) {
      throw new APIRequestError("Skill Catalog identities are duplicated", "INVALID_RESPONSE", 502);
    }
    return { protocol_version: "skill-catalog-control.v1", publishers, control_enabled: value.control_enabled };
  }

  async controlSkillCatalogPublisher(body: SkillCatalogPublisherControlRequestView,
    signal?: AbortSignal): Promise<SkillCatalogPublisherView> {
    if (!this.features.hasSkillInstallation || body.version !== "skill-catalog-control.v1" || !body.confirm ||
      !isSHA256(body.fingerprint) || typeof body.expected_updated_at !== "string" ||
      (body.expected_updated_at !== "" && !validDate(body.expected_updated_at)) ||
      !["trust", "revoke"].includes(body.action) ||
      (body.action === "trust" && (!boundedText(body.name, 96) || !validCanonicalBase64(body.public_key, 32))) ||
      (body.action === "revoke" && (!body.expected_updated_at || body.name !== undefined || body.public_key !== undefined || body.team !== undefined))) {
      throw new Error("A confirmed Skill Catalog identity and observed version are required");
    }
    const publisher = parseSkillCatalogPublisher(await this.transport.sendControlRequest<unknown>("/skills/catalog/publishers", body, signal));
    if (publisher.fingerprint !== body.fingerprint || publisher.trust_class !== (body.action === "trust" ? "trusted" : "revoked") ||
      publisher.updated_at === body.expected_updated_at || (body.action === "trust" && publisher.public_key !== body.public_key)) {
      throw new APIRequestError("Skill Catalog change is unverified; refresh before retrying", "INVALID_RESPONSE", 502);
    }
    return publisher;
  }

  async listSkillPackages(filters: { surface?: string; profile?: string;
    includeRemoved?: boolean } = {}, signal?: AbortSignal): Promise<SkillPackageInventoryView> {
    const query: Record<string, QueryValue> = {};
    if (filters.surface !== undefined) {
      const surface = boundedIdentity(filters.surface);
      if (!surface) throw new Error("A bounded Skill inventory surface is required");
      query.surface = surface;
    }
    if (filters.profile !== undefined) {
      const profile = boundedIdentity(filters.profile);
      if (!profile) throw new Error("A bounded Skill inventory Profile is required");
      query.profile = profile;
    }
    if (filters.includeRemoved !== undefined) {
      query.include_removed = filters.includeRemoved;
    }
    return parseSkillPackageInventory(await this.transport.get<unknown>(
      "/skills/packages", query, signal,
    ));
  }

  async removeSkillPackage(body: SkillPackageRemoveRequestView,
    idempotencyKey: string, signal?: AbortSignal): Promise<SkillPackageRemovalView> {
    if (!this.features.hasSkillInstallation || body.confirm_remove !== true ||
      body.protocol_version !== "skill_package_removal_request.v1") {
      throw new Error("Explicit Skill package removal confirmation is required");
    }
    return parseSkillPackageRemoval(await this.transport.sendControl<unknown>(
      "/skills/packages/remove", body, idempotencyKey, signal,
    ), body);
  }
}
