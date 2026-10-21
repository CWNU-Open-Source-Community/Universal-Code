import { validSkillSelectionRef } from "../../project-skills";
import { APIRequestError } from "../protocol";
import {
  boundedIdentity,
  boundedStringArray,
  boundedText,
  hasExactKeys,
  hasOnlyKeys,
  hasRequiredOnlyKeys,
  isRecord,
  isSHA256,
  validDate,
} from "../validation";
import { parseExtensionInventory, validCanonicalBase64 } from "./extensions";
import { parseOperationReceipt } from "./operations";
import type {
  PluginSkillInstallView,
  SkillCatalogPublisherView,
  SkillPackageInstallRequestView,
  SkillPackageInstallResult,
  SkillPackageInstallView,
  SkillPackageInventoryView,
  SkillPackageRemovalView,
} from "../../types";

export function parseSkillPackageInstall(value: unknown,
  request: SkillPackageInstallRequestView): SkillPackageInstallResult {
  if (isRecord(value) && value.protocol_version === "plugin-installation.v2") {
    return parsePluginSkillInstall(value, request.surface);
  }
  const legacyKeys = ["archive_sha256", "context_injection_authorized",
    "import_command_execution", "import_network_access", "import_provider_calls", "name",
    "package_fingerprint", "protocol_version", "receipt", "recovered_pending", "replayed",
    "run_selection_authorized", "surface", "tool_capability_grant", "trust_class", "version"];
  const modeKeys = ["profiles", "surfaces", "phases", "roles", "user_invocable", "model_invocable", "explicit_only"];
  if ((!hasExactKeys(value, legacyKeys) && !hasExactKeys(value, [...legacyKeys, ...modeKeys])) ||
    value.protocol_version !== "skill_package_installation.v1" ||
    value.surface !== request.surface || value.trust_class !== "operator_installed_untrusted" ||
    !boundedText(value.name, 128) || !boundedText(value.version, 64) ||
    !isSHA256(value.archive_sha256) || !isSHA256(value.package_fingerprint) ||
    typeof value.replayed !== "boolean" || typeof value.recovered_pending !== "boolean" ||
    value.import_command_execution !== false || value.import_network_access !== false ||
    value.import_provider_calls !== false || value.tool_capability_grant !== false ||
    value.run_selection_authorized !== false || value.context_injection_authorized !== false) {
    throw new APIRequestError("Skill package installation widened inert Registry authority",
      "INVALID_RESPONSE", 502);
  }
  if ("profiles" in value && (!boundedStringArray(value.profiles, 8, 64) ||
    !boundedStringArray(value.surfaces, 2, 64) || !boundedStringArray(value.phases, 4, 64) ||
    !boundedStringArray(value.roles, 4, 64) || typeof value.user_invocable !== "boolean" ||
    typeof value.model_invocable !== "boolean" || typeof value.explicit_only !== "boolean")) {
    throw new APIRequestError("Legacy Skill recovery metadata is invalid", "INVALID_RESPONSE", 502);
  }
  const receipt = parseOperationReceipt(value.receipt, "skill_package_install", "installed",
    value.replayed);
  return { ...value, receipt } as unknown as SkillPackageInstallView;
}

export const skillPackageItemKeys = ["archive_bytes", "archive_sha256", "completed_at",
  "content_body_exposed", "content_bytes", "content_sha256", "content_token_upper_bound",
  "context_injection_authorized", "created_at",
  "entry_count", "explicit_only", "import_command_execution", "import_network_access",
  "import_provider_calls", "installed_by", "model_invocable", "object_verified",
  "operator_confirmed", "package_fingerprint", "phases", "profiles", "risk_codes", "roles",
  "run_selection_authorized", "skill", "status", "surface", "surfaces",
  "tool_capability_grant", "tool_dependencies", "trust_class", "uncompressed_bytes",
  "user_invocable"];

export const skillPackageRemovalKeys = ["context_injection_authorized", "future_selection_enabled",
  "historical_recovery_preserved", "package_object_retained", "removed_at", "removed_by",
  "run_selection_authorized", "tool_capability_grant"];

// Both Skill selection routes take the identical request DTO and return the
// identical response DTO, so one guard covers either admission.
export function assertSkillSelectionChoice(runID: string,
  choice: import("../../project-skills").ProjectSkillChoice, label: string,
): void {
  if (!boundedIdentity(runID) || runID.trim() !== runID || typeof choice.confirmUntrustedContext !== "boolean" ||
    choice.refs.length === 0 || choice.refs.length > 16 || new Set(choice.refs).size !== choice.refs.length ||
    choice.refs.some((ref) => !validSkillSelectionRef(ref))) {
    throw new Error(`Exact ${label} Skill references and task control are required`);
  }
}

export function assertSkillSelectionReceipt(value: unknown, runID: string, refs: string[],
  label: string,
): void {
  if (!isRecord(value)) throw new Error(`${label} Skill selection receipt is invalid`);
  if (!hasOnlyKeys(value, ["run_id", "refs", "builtin_selection_id", "external_selection_id",
    "replayed", "capability_grant"]) || value.run_id !== runID || value.capability_grant !== false ||
    typeof value.replayed !== "boolean" || !Array.isArray(value.refs) ||
    value.refs.length !== refs.length || value.refs.some((ref) => typeof ref !== "string" || !refs.includes(ref)) ||
    new Set(value.refs).size !== value.refs.length ||
    (!value.builtin_selection_id && !value.external_selection_id) ||
    [value.builtin_selection_id, value.external_selection_id]
      .some((id) => id !== undefined && !boundedIdentity(id))) {
    throw new Error(`${label} Skill selection receipt does not match the request`);
  }
}

export function parseSkillPackageInventory(value: unknown): SkillPackageInventoryView {
  if (!hasExactKeys(value, ["capability_grant", "include_removed", "items", "profile",
    "protocol_version", "returned_count", "surface", "unverified_count"]) ||
    value.protocol_version !== "skill_package_inventory.v1" ||
    value.capability_grant !== false || typeof value.include_removed !== "boolean" ||
    typeof value.surface !== "string" || typeof value.profile !== "string" ||
    !Array.isArray(value.items) || value.items.length !== value.returned_count ||
    !Number.isInteger(value.unverified_count) ||
    (value.unverified_count as number) < 0 ||
    (value.unverified_count as number) > value.items.length) {
    throw new APIRequestError("Skill package inventory response is invalid",
      "INVALID_RESPONSE", 502);
  }
  for (const item of value.items) {
    if ((!hasExactKeys(item, skillPackageItemKeys) &&
      !hasExactKeys(item, [...skillPackageItemKeys, "removal"])) ||
      typeof item.skill !== "string" ||
      (item.status !== "installed" && item.status !== "removed") ||
      typeof item.object_verified !== "boolean" ||
      item.content_body_exposed !== false ||
      (item.status === "removed" &&
        (!isRecord(item.removal) || !hasExactKeys(item.removal, skillPackageRemovalKeys) ||
          item.removal.future_selection_enabled !== false ||
          item.removal.package_object_retained !== true)) ||
      (item.status === "installed" && item.removal !== undefined)) {
      throw new APIRequestError("Installed Skill package projection is invalid",
        "INVALID_RESPONSE", 502);
    }
  }
  return value as unknown as SkillPackageInventoryView;
}

export function parseSkillPackageRemoval(value: unknown,
  request: { skill: string },
): SkillPackageRemovalView {
  if (!hasExactKeys(value, ["capability_grant", "context_injection_authorized",
    "future_selection_enabled", "historical_recovery_preserved", "package_object_retained",
    "protocol_version", "receipt", "removed_at", "removed_by", "replayed",
    "run_selection_authorized", "skill", "status", "surface", "tool_capability_grant"]) ||
    value.protocol_version !== "skill_package_removal_control.v1" ||
    value.capability_grant !== false || value.status !== "removed" ||
    typeof value.replayed !== "boolean" ||
    value.skill !== request.skill || value.future_selection_enabled !== false ||
    value.run_selection_authorized !== false ||
    value.context_injection_authorized !== false || value.tool_capability_grant !== false ||
    value.package_object_retained !== true || value.historical_recovery_preserved !== true) {
    throw new APIRequestError("Skill package removal widened inert Registry authority",
      "INVALID_RESPONSE", 502);
  }
  const receipt = parseOperationReceipt(value.receipt, "skill_package_remove", "removed",
    value.replayed);
  return { ...value, receipt } as unknown as SkillPackageRemovalView;
}

export function parseSkillCatalogPublisher(value: unknown): SkillCatalogPublisherView {
  if (!isRecord(value) || !hasRequiredOnlyKeys(value,
    ["fingerprint", "name", "team", "public_key", "trust_class", "updated_at", "created_at"], ["trusted_at", "revoked_at"]) ||
    !isSHA256(value.fingerprint) || !boundedText(value.name, 96) || typeof value.team !== "string" || value.team.length > 256 ||
    !validCanonicalBase64(value.public_key, 32) || atob(String(value.public_key)).length !== 32 ||
    !["trusted", "revoked"].includes(String(value.trust_class)) || !validDate(value.updated_at) || !validDate(value.created_at) ||
    (value.trusted_at !== undefined && !validDate(value.trusted_at)) || (value.revoked_at !== undefined && !validDate(value.revoked_at))) {
    throw new APIRequestError("Skill Catalog publisher response is invalid", "INVALID_RESPONSE", 502);
  }
  return value as SkillCatalogPublisherView;
}

// Shared HTTP/Desktop decoder for the actual Plugin lifecycle response.
export function parsePluginSkillInstall(value: unknown, surface: string,
  hiddenLocalSource = false): PluginSkillInstallView {
  if (!hasExactKeys(value, ["protocol_version", "installation", "replayed"]) ||
    value.protocol_version !== "plugin-installation.v2" || typeof value.replayed !== "boolean") {
    throw new APIRequestError("Plugin import response is invalid", "INVALID_RESPONSE", 502);
  }
  const installation = parseExtensionInventory({ protocol_version: "extension-inventory.v1",
    mcp_servers: [], mcp_calls: [], plugins: [value.installation] }, hiddenLocalSource).plugins[0];
  if (installation.protocol_version !== "plugin-installation.v2" || installation.snapshot?.surface !== surface ||
    !["staged", "approved", "enabled", "disabled", "revoked", "rolled_back", "quarantined"].includes(installation.state) ||
    (!value.replayed && (installation.state !== "staged" || installation.generation !== 1 ||
      installation.enabled_capabilities.length !== 0))) {
    throw new APIRequestError("Plugin import lifecycle binding is invalid", "INVALID_RESPONSE", 502);
  }
  return { protocol_version: "plugin-installation.v2", installation, replayed: value.replayed };
}
