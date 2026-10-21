import { APIRequestError } from "../protocol";
import {
  boundedIdentity,
  boundedText,
  hasExactKeys,
  hasOnlyKeys,
  isRecord,
  safePositiveInteger,
  validDate,
} from "../validation";
import type {
  RunNetworkAuthorityControlRequestView,
  RunNetworkAuthorityControlView,
  RunWebAccessControlRequestView,
  RunWebAccessControlView,
  WebAccessMode,
} from "../../types";

export function parseStrictRunMode(value: unknown): Record<string, unknown> {
  const keys = ["capability_grant", "created_at", "phase", "policy_version", "profile",
    "protocol_version", "reason", "requested_by", "revision", "scope", "surface"];
  if (!hasExactKeys(value, keys) || value.capability_grant !== false ||
    value.protocol_version !== "run_mode.v1" || value.policy_version !== "mode_policy.v1" ||
    !safePositiveInteger(value.revision) || typeof value.surface !== "string" ||
    !["code", "cyber"].includes(value.surface) || typeof value.phase !== "string" ||
    !["plan", "deliver"].includes(value.phase) || typeof value.profile !== "string" ||
    !["code", "review", "learn", "script"].includes(value.profile) ||
    !boundedText(value.requested_by, 256) || !boundedText(value.reason, 1_024) ||
    !validDate(value.created_at) || !isRecord(value.scope) ||
    !hasOnlyKeys(value.scope, ["allowed_targets", "network_mode", "workspace_id", "web_access_mode", "web_allowed_targets"]) ||
    !validWebAccessScope(value.scope) ||
    !Object.prototype.hasOwnProperty.call(value.scope, "network_mode") ||
    (value.scope.workspace_id !== undefined && !boundedIdentity(value.scope.workspace_id))) {
    throw new APIRequestError("Run mode response is invalid", "INVALID_RESPONSE", 502);
  }
  return value;
}

export function parseRunNetworkAuthorityControl(value: unknown, expectedRunID: string,
  request: RunNetworkAuthorityControlRequestView): RunNetworkAuthorityControlView {
  const mode = isRecord(value) && Object.prototype.hasOwnProperty.call(value, "mode")
    ? parseStrictRunMode(value.mode) : null;
  if (!hasExactKeys(value, ["added_targets", "capability_grant", "mode", "replayed",
    "run_id", "version"]) ||
    value.version !== "run_network_authority_control.v1" ||
    value.run_id !== expectedRunID || typeof value.replayed !== "boolean" ||
    value.capability_grant !== true || mode === null ||
    mode.revision !== request.expected_mode_revision + 1 ||
    !isRecord(mode.scope) || mode.scope.network_mode !== "allowlist" ||
    !Array.isArray(value.added_targets) ||
    value.added_targets.some((target) => typeof target !== "string") ||
    !Array.isArray(mode.scope.allowed_targets) ||
    mode.scope.allowed_targets.some((target) => typeof target !== "string")) {
    throw new APIRequestError("Run network authority response is invalid",
      "INVALID_RESPONSE", 502);
  }
  const expected = normalizeRequestedNetworkAuthority({ network_mode: "allowlist",
    allowed_targets: request.add_allowed_targets });
  const rawAdded = value.added_targets as string[];
  const rawAllowed = mode.scope.allowed_targets as string[];
  const added = rawAdded.map((target) => canonicalExactNetworkTarget(target));
  const allowed = rawAllowed.map((target) => canonicalExactNetworkTarget(target));
  if (added.length !== expected.targets.length || added.length > 256 ||
    rawAdded.some((target, index) => target !== added[index]) ||
    rawAllowed.some((target, index) => target !== allowed[index]) ||
    added.some((target, index) => target !== expected.targets[index]) ||
    allowed.length < added.length || allowed.length > 256 ||
    allowed.some((target, index) => index > 0 && allowed[index - 1]! >= target) ||
    added.some((target) => !allowed.includes(target))) {
    throw new APIRequestError("Run network authority response changed its exact target grant",
      "INVALID_RESPONSE", 502);
  }
  return value as unknown as RunNetworkAuthorityControlView;
}

export function parseRunWebAccessControl(value: unknown, runID: string,
  request: RunWebAccessControlRequestView): RunWebAccessControlView {
  const mode = isRecord(value) && Object.hasOwn(value, "mode") ? parseStrictRunMode(value.mode) : null;
  const expected = normalizeRequestedWebAccess(request);
  if (!hasExactKeys(value, ["version", "run_id", "mode", "replayed"]) ||
    value.version !== "run_web_access_control.v1" || value.run_id !== runID ||
    typeof value.replayed !== "boolean" || mode === null || expected === null ||
    mode.revision !== request.expected_mode_revision + 1 || !isRecord(mode.scope) ||
    !scopeMatchesRequestedWebAccess(mode.scope, expected)) {
    throw new APIRequestError("Web access response does not match the requested setting", "INVALID_RESPONSE", 502);
  }
  return value as unknown as RunWebAccessControlView;
}

export type RequestedNetworkAuthority = {
  mode: "disabled" | "allowlist";
  targets: string[];
};

export function canonicalWebAccessSettings(mode: unknown, rawTargets: unknown = []): {
  mode: WebAccessMode; targets: string[];
} {
  if (mode !== "disabled" && mode !== "rules" && mode !== "full") {
    throw new Error("请选择已支持的网页访问方式。");
  }
  if (!Array.isArray(rawTargets) || rawTargets.length > 256 ||
    rawTargets.some((target) => typeof target !== "string")) {
    throw new Error("网站规则最多包含 256 个 HTTPS 域名。");
  }
  if (mode !== "rules" && rawTargets.length > 0) {
    throw new Error("网站名单仅用于规则搜索。");
  }
  return { mode, targets: [...new Set(rawTargets.map(canonicalExactNetworkTarget))].sort() };
}

export function normalizeRequestedWebAccess(request: { web_access_mode?: string; web_allowed_targets?: string[] }) {
  if (request.web_access_mode === undefined && request.web_allowed_targets === undefined) return null;
  return canonicalWebAccessSettings(request.web_access_mode, request.web_allowed_targets ?? []);
}

export function validWebAccessScope(scope: Record<string, unknown>): boolean {
  if (scope.web_access_mode === undefined && scope.web_allowed_targets === undefined) return true;
  try {
    const expected = canonicalWebAccessSettings(scope.web_access_mode, scope.web_allowed_targets ?? []);
    return scopeMatchesRequestedWebAccess(scope, expected);
  } catch { return false; }
}

export function scopeMatchesRequestedWebAccess(scope: Record<string, unknown>,
  expected: { mode: WebAccessMode; targets: string[] }): boolean {
  const targets = scope.web_allowed_targets ?? [];
  return scope.web_access_mode === expected.mode && Array.isArray(targets) &&
    targets.length === expected.targets.length && targets.every((target, index) => target === expected.targets[index]);
}

export function normalizeRequestedNetworkAuthority(request: {
  network_mode?: string;
  allowed_targets?: string[];
}): RequestedNetworkAuthority {
  const mode = request.network_mode ?? "disabled";
  const rawTargets = request.allowed_targets ?? [];
  if (mode === "disabled") {
    if (rawTargets.length !== 0) {
      throw new Error("Disabled network authority cannot include allowed targets");
    }
    return { mode, targets: [] };
  }
  if (mode !== "allowlist" || rawTargets.length === 0 || rawTargets.length > 256) {
    throw new Error("Network authority requires a bounded exact allowlist");
  }
  const targets = [...new Set(rawTargets.map(canonicalExactNetworkTarget))].sort();
  if (targets.length === 0) {
    throw new Error("Network authority requires an exact allowed target");
  }
  return { mode, targets };
}

export function canonicalExactNetworkTarget(raw: string): string {
  const target = raw.trim();
  const lower = target.toLowerCase();
  if (target === "" || target.includes("*") || lower === "public_https" ||
    /[\\?#]/u.test(target) || target.includes("@")) {
    throw new Error("Network allowlist targets must identify exact public HTTPS hosts");
  }
  if (lower.includes("://") && !lower.startsWith("https://")) {
    throw new Error("Network allowlist target origins must use HTTPS");
  }
  let parsed: URL;
  try {
    parsed = new URL(lower.includes("://") ? target : `https://${target}`);
  } catch {
    throw new Error("Network allowlist target is invalid");
  }
  if (parsed.protocol !== "https:" || parsed.username !== "" || parsed.password !== "" ||
    (parsed.port !== "" && parsed.port !== "443") || parsed.pathname !== "/" ||
    parsed.search !== "" || parsed.hash !== "") {
    throw new Error("Network allowlist targets must be exact HTTPS origins or hosts");
  }
  const hostname = parsed.hostname.toLowerCase().replace(/^\[|\]$/gu, "")
    .replace(/\.$/u, "");
  if (hostname === "" || hostname === "localhost" ||
    !hostname.includes(".") || hostname.includes(":") ||
    /^\d+(?:\.\d+){3}$/u.test(hostname) ||
    hostname.split(".").some((label) => label === "" || label.length > 63 ||
      label.startsWith("-") || label.endsWith("-") || !/^[a-z0-9-]+$/u.test(label)) ||
    [".localhost", ".local", ".internal", ".intranet", ".corp", ".home", ".lan",
      ".home.arpa", ".localdomain", ".test", ".invalid", ".example"]
      .some((suffix) => hostname.endsWith(suffix))) {
    throw new Error("Network allowlist target must be a public host");
  }
  return hostname;
}

export function scopeMatchesRequestedNetworkAuthority(scope: Record<string, unknown>,
  expected: RequestedNetworkAuthority): boolean {
  if (scope.network_mode !== expected.mode) {
    return false;
  }
  const targets = scope.allowed_targets;
  if (expected.mode === "disabled") {
    return targets === undefined || (Array.isArray(targets) && targets.length === 0);
  }
  return Array.isArray(targets) && targets.length === expected.targets.length &&
    targets.every((target, index) => target === expected.targets[index]);
}
