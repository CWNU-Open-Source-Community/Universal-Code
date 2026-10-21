import { APIRequestError } from "../protocol";
import {
  boundedIdentity,
  hasExactKeys,
  hasOnlyKeys,
  hasRequiredOnlyKeys,
  isRecord,
  isSHA256,
  safeBoundedCount,
  safePositiveInteger,
  validDate,
} from "../validation";
import type { UIEvidenceArtifactMetadata, UIEvidenceAttempt, UIEvidenceBundle } from "../../types";

export const uiEvidenceStatuses = ["not_run", "running", "passed", "failed", "cancelled",
  "timed_out", "interrupted"] as const;

export const uiEvidenceFailureStages = ["none", "build", "launch", "readiness", "navigation",
  "selector", "assertion", "console", "network", "capture", "cleanup"] as const;

export const uiEvidenceStepKinds = ["navigate", "click", "type", "assert_present", "assert_absent",
  "capture"] as const;

export function validUIEvidenceText(value: unknown, maximum: number, lines = false): value is string {
  return typeof value === "string" && value.length > 0 && value.length <= maximum &&
    value.trim() === value && !value.includes("\0") && (lines || !/[\r\n]/u.test(value));
}

export function validUIEvidenceLoopbackURL(value: unknown): value is string {
  if (typeof value !== "string" || value.length > 4_096 || /[\r\n\t?#]/u.test(value)) return false;
  const match = /^(https?):\/\/(\[[^\]]+\]|[^/:@]+):(\d{1,5})(\/.*)$/u.exec(value);
  if (!match) return false;
  const port = Number(match[3]);
  const host = match[2].replace(/^\[|\]$/gu, "").toLowerCase();
  const ipv4 = /^127(?:\.\d{1,3}){3}$/u.test(host) &&
    host.split(".").every((part) => Number(part) <= 255);
  const ipv6 = host === "::1" || /^::ffff:127(?:\.\d{1,3}){3}$/u.test(host);
  if ((!ipv4 && !ipv6) || port < 1 || port > 65_535) return false;
  try {
    const parsed = new URL(value);
    return parsed.username === "" && parsed.password === "" && parsed.pathname !== "" &&
      parsed.search === "" && parsed.hash === "";
  } catch {
    return false;
  }
}

export function sameUIEvidenceOrigin(left: string, right: string): boolean {
  try {
    const leftURL = new URL(left);
    const rightURL = new URL(right);
    return leftURL.protocol === rightURL.protocol &&
      leftURL.hostname.toLowerCase() === rightURL.hostname.toLowerCase() &&
      leftURL.port === rightURL.port;
  } catch {
    return false;
  }
}

export function validUIEvidenceSource(value: unknown): boolean {
  const required = ["commit", "dirty", "dirty_digest", "index_sha256", "manifest_sha256",
    "repository_kind", "root_fingerprint"];
  if (!hasRequiredOnlyKeys(value, required, ["branch"]) ||
    !["git", "non_git"].includes(String(value.repository_kind)) ||
    typeof value.dirty !== "boolean" || !isSHA256(value.dirty_digest) ||
    !isSHA256(value.root_fingerprint) || !isSHA256(value.index_sha256) ||
    !isSHA256(value.manifest_sha256) ||
    (Object.prototype.hasOwnProperty.call(value, "branch") &&
      !validUIEvidenceText(value.branch, 255))) return false;
  return value.repository_kind === "git"
    ? typeof value.commit === "string" && /^(?:[0-9a-f]{40}|[0-9a-f]{64})$/u.test(value.commit)
    : value.commit === "non-git";
}

export function validUIEvidenceCommandRecipe(value: unknown): boolean {
  const keys = ["canonical_argv", "credentials", "environment_names", "environment_sha256",
    "executable_name", "executable_path_sha256", "executable_sha256", "fingerprint", "network",
    "profile", "protocol_version", "purpose", "timeout_milliseconds", "working_directory"];
  if (!hasExactKeys(value, keys) || value.protocol_version !== "command-runtime.v2" ||
    !["powershell", "bash", "process"].includes(String(value.profile)) ||
    !validUIEvidenceText(value.executable_name, 512) || /[\\/]/u.test(value.executable_name) ||
    !isSHA256(value.executable_path_sha256) || !isSHA256(value.executable_sha256) ||
    !isSHA256(value.environment_sha256) || !isSHA256(value.fingerprint) ||
    !safePositiveInteger(value.timeout_milliseconds) || value.timeout_milliseconds > 1_800_000 ||
    value.network !== "disabled" || value.credentials !== "none" ||
    !validUIEvidenceText(value.working_directory, 4_096) ||
    !validUIEvidenceText(value.purpose, 1_200) || !Array.isArray(value.canonical_argv) ||
    value.canonical_argv.length < 1 || value.canonical_argv.length > 128 ||
    value.canonical_argv.some((entry) => !validUIEvidenceText(entry, 65_536, true)) ||
    !Array.isArray(value.environment_names) || value.environment_names.length > 32 ||
    value.environment_names.some((entry) => !validUIEvidenceText(entry, 128))) return false;
  const names = value.environment_names as string[];
  return names.every((name, index) => index === 0 ||
    name.toLowerCase() > names[index - 1].toLowerCase());
}

export function validUIEvidenceReadiness(value: unknown): boolean {
  if (!hasExactKeys(value, ["expected_status", "interval_milliseconds", "method",
    "timeout_milliseconds", "url"]) || !validUIEvidenceLoopbackURL(value.url) ||
    value.method !== "GET" || !safePositiveInteger(value.timeout_milliseconds) ||
    value.timeout_milliseconds < 100 || value.timeout_milliseconds > 120_000 ||
    !safePositiveInteger(value.interval_milliseconds) || value.interval_milliseconds < 10 ||
    value.interval_milliseconds > 5_000 || !Array.isArray(value.expected_status) ||
    value.expected_status.length < 1 || value.expected_status.length > 16) return false;
  return value.expected_status.every((status, index, statuses) => Number.isInteger(status) &&
    status >= 100 && status <= 599 && (index === 0 || status > statuses[index - 1]));
}

export function validUIEvidenceEnvironment(value: unknown): boolean {
  if (!hasExactKeys(value, ["locale", "reduced_motion", "theme", "viewport"]) ||
    !hasExactKeys(value.viewport, ["dpr", "height", "width"]) ||
    !safePositiveInteger(value.viewport.width) || !safePositiveInteger(value.viewport.height) ||
    value.viewport.width < 320 || value.viewport.width > 7_680 ||
    value.viewport.height < 240 || value.viewport.height > 4_320 ||
    typeof value.viewport.dpr !== "number" || !Number.isFinite(value.viewport.dpr) ||
    value.viewport.dpr < 0.5 || value.viewport.dpr > 4 ||
    value.viewport.width * value.viewport.dpr > 7_680 ||
    value.viewport.height * value.viewport.dpr > 4_320 ||
    !["light", "dark"].includes(String(value.theme)) ||
    typeof value.reduced_motion !== "boolean" || !validUIEvidenceText(value.locale, 64)) return false;
  const parts = value.locale.split("-");
  return parts.length >= 1 && parts.length <= 3 && parts[0].length >= 2 &&
    parts[0].length <= 8 && parts.every((part) => /^[A-Za-z0-9]*$/u.test(part));
}

export function validUIEvidenceFixture(value: unknown): boolean {
  return hasExactKeys(value, ["data_sha256", "deterministic", "name", "page_state", "seed",
    "synthetic"]) && validUIEvidenceText(value.name, 256) &&
    validUIEvidenceText(value.seed, 256) && validUIEvidenceText(value.page_state, 8_192, true) &&
    isSHA256(value.data_sha256) && value.deterministic === true &&
    typeof value.synthetic === "boolean";
}

export function validUIEvidenceSteps(value: unknown): boolean {
  if (!Array.isArray(value) || value.length < 1 || value.length > 128) return false;
  const identifiers = new Set<string>();
  for (const entry of value) {
    if (!hasRequiredOnlyKeys(entry, ["capture_after", "id", "kind"],
      ["input_sha256", "selector"]) || !boundedIdentity(entry.id) ||
      !uiEvidenceStepKinds.includes(String(entry.kind) as typeof uiEvidenceStepKinds[number]) ||
      typeof entry.capture_after !== "boolean") return false;
    const requiresSelector = ["click", "type", "assert_present", "assert_absent"]
      .includes(String(entry.kind));
    const hasSelector = Object.prototype.hasOwnProperty.call(entry, "selector");
    const hasInput = Object.prototype.hasOwnProperty.call(entry, "input_sha256");
    if (requiresSelector !== hasSelector ||
      (hasSelector && !validUIEvidenceText(entry.selector, 2_048)) ||
      ((entry.kind === "type") !== hasInput) || (hasInput && !isSHA256(entry.input_sha256)) ||
      identifiers.has(entry.id as string)) return false;
    identifiers.add(entry.id as string);
  }
  return value[0].kind === "navigate";
}

export function validUIEvidenceBrowser(value: unknown): boolean {
  return hasExactKeys(value, ["driver_protocol", "executable_sha256", "headless", "product",
    "temporary_profile", "version"]) && ["chrome", "edge"].includes(String(value.product)) &&
    validUIEvidenceText(value.version, 128) && isSHA256(value.executable_sha256) &&
    value.driver_protocol === "restricted-cdp-ui-evidence.v1" &&
    typeof value.headless === "boolean" && value.temporary_profile === true;
}

export function parseUIEvidenceAttempt(value: unknown, expectedRunID = ""): UIEvidenceAttempt {
  const required = ["artifact_bytes", "artifact_count", "cleanup", "created_at", "diagnostics",
    "failure_stage", "manifest", "operation_digest", "protocol_version", "request_fingerprint",
    "status", "updated_at", "version"];
  const optional = ["completed_at", "failure_code", "failure_message", "started_at"];
  if (!isRecord(value) || !hasOnlyKeys(value, [...required, ...optional]) ||
    required.some((key) => !Object.prototype.hasOwnProperty.call(value, key)) ||
    value.protocol_version !== "ui-evidence-attempt.v1" ||
    !uiEvidenceStatuses.includes(String(value.status) as typeof uiEvidenceStatuses[number]) ||
    !uiEvidenceFailureStages.includes(String(value.failure_stage) as typeof uiEvidenceFailureStages[number]) ||
    !isSHA256(value.operation_digest) || !isSHA256(value.request_fingerprint) ||
    !safePositiveInteger(value.version) || !safeBoundedCount(value.artifact_count, 10_000) ||
    !safeBoundedCount(value.artifact_bytes, 128 * 1024 * 1024) ||
    !validDate(value.created_at) || !validDate(value.updated_at) ||
    !isRecord(value.manifest) || !isRecord(value.cleanup) || !isRecord(value.diagnostics)) {
    throw new APIRequestError("UI evidence attempt is invalid", "INVALID_RESPONSE", 502);
  }
  const manifest = value.manifest;
  if (!hasExactKeys(manifest, ["attempt_id", "authority", "browser", "capture", "created_at",
    "environment", "failure_policy", "fingerprint", "fixture", "mission_id",
    "protocol_version", "readiness", "route", "run_id", "session_id", "source", "start",
    "steps", "url", "workspace_id"].concat(Object.prototype.hasOwnProperty.call(manifest, "build") ? ["build"] : [])) ||
    manifest.protocol_version !== "ui-evidence.v1" || !boundedIdentity(manifest.attempt_id) ||
    !boundedIdentity(manifest.run_id) || !boundedIdentity(manifest.mission_id) ||
    !boundedIdentity(manifest.session_id) || !boundedIdentity(manifest.workspace_id) ||
    (expectedRunID !== "" && manifest.run_id !== expectedRunID) ||
    !validDate(manifest.created_at) || !isSHA256(manifest.fingerprint) ||
    manifest.fingerprint !== value.request_fingerprint || !validUIEvidenceLoopbackURL(manifest.url) ||
    !validUIEvidenceText(manifest.route, 4_096) || !manifest.route.startsWith("/") ||
    manifest.route.startsWith("//") || new URL(manifest.url).pathname !== manifest.route ||
    !isRecord(manifest.authority) || !isRecord(manifest.browser) ||
    !isRecord(manifest.capture) || !isRecord(manifest.environment) ||
    !isRecord(manifest.failure_policy) || !isRecord(manifest.fixture) ||
    !isRecord(manifest.readiness) || !isRecord(manifest.source) || !isRecord(manifest.start) ||
    !validUIEvidenceSource(manifest.source) || !validUIEvidenceCommandRecipe(manifest.start) ||
    (Object.prototype.hasOwnProperty.call(manifest, "build") &&
      !validUIEvidenceCommandRecipe(manifest.build)) || !validUIEvidenceReadiness(manifest.readiness) ||
    !sameUIEvidenceOrigin(manifest.url, manifest.readiness.url as string) ||
    !validUIEvidenceBrowser(manifest.browser) || !validUIEvidenceEnvironment(manifest.environment) ||
    !validUIEvidenceFixture(manifest.fixture) || !validUIEvidenceSteps(manifest.steps)) {
    throw new APIRequestError("UI evidence manifest is invalid", "INVALID_RESPONSE", 502);
  }
  const capture = manifest.capture as Record<string, unknown>;
  if (!hasExactKeys(manifest.authority, ["credential_access", "network_access",
    "personal_profile", "process_start", "request_mutation", "verification_pass"]) ||
    Object.values(manifest.authority).some((entry) => entry !== false) ||
    !hasExactKeys(manifest.capture, ["accessibility", "console", "dom", "mask_selectors",
      "network", "performance", "screenshot", "video"]) ||
    ["accessibility", "console", "dom", "network", "performance", "screenshot"]
      .some((key) => capture[key] !== true) ||
    !hasExactKeys(manifest.failure_policy, ["fail_on_console_error", "fail_on_http_status",
      "fail_on_page_error", "fail_on_request_error"]) ||
    Object.values(manifest.failure_policy).some((entry) => entry !== true) ||
    manifest.capture.video !== false || !Array.isArray(manifest.capture.mask_selectors) ||
    manifest.capture.mask_selectors.length > 32 ||
    manifest.capture.mask_selectors.some((entry) => !validUIEvidenceText(entry, 2_048)) ||
    new Set(manifest.capture.mask_selectors).size !== manifest.capture.mask_selectors.length) {
    throw new APIRequestError("UI evidence widened its evidence authority", "INVALID_RESPONSE", 502);
  }
  const cleanupKeys = ["application_tree_reaped", "browser_tree_reaped", "network_released",
    "port_released", "profile_removed"];
  const diagnosticKeys = ["allowed_requests", "blocked_requests", "console_errors",
    "console_warnings", "failed_requests", "http_failures", "page_errors"];
  const cleanup = value.cleanup as Record<string, unknown>;
  const diagnostics = value.diagnostics as Record<string, unknown>;
  if (!hasExactKeys(cleanup, cleanupKeys) ||
    cleanupKeys.some((key) => typeof cleanup[key] !== "boolean") ||
    !hasExactKeys(diagnostics, diagnosticKeys) ||
    diagnosticKeys.some((key) => !safeBoundedCount(diagnostics[key], 1_000_000))) {
    throw new APIRequestError("UI evidence cleanup or diagnostics are invalid", "INVALID_RESPONSE", 502);
  }
  const status = String(value.status);
  const hasStarted = Object.prototype.hasOwnProperty.call(value, "started_at");
  const hasCompleted = Object.prototype.hasOwnProperty.call(value, "completed_at");
  const hasFailureCode = Object.prototype.hasOwnProperty.call(value, "failure_code");
  const hasFailureMessage = Object.prototype.hasOwnProperty.call(value, "failure_message");
  const started = hasStarted && typeof value.started_at === "string" && validDate(value.started_at);
  const completed = hasCompleted && typeof value.completed_at === "string" && validDate(value.completed_at);
  const hasExecutionResidue = diagnosticKeys.some((key) => diagnostics[key] !== 0) ||
    cleanupKeys.some((key) => cleanup[key] !== false) || value.artifact_count !== 0 ||
    value.artifact_bytes !== 0;
  const createdAt = Date.parse(String(value.created_at));
  const updatedAt = Date.parse(String(value.updated_at));
  const startedAt = started ? Date.parse(String(value.started_at)) : 0;
  const completedAt = completed ? Date.parse(String(value.completed_at)) : 0;
  if (updatedAt < createdAt || (started && startedAt < createdAt) ||
    (completed && (!started || completedAt < startedAt || updatedAt < completedAt)) ||
    (hasStarted && !started) || (hasCompleted && !completed) ||
    (hasFailureCode && (typeof value.failure_code !== "string" || value.failure_code === "")) ||
    (hasFailureMessage && typeof value.failure_message !== "string") ||
    ((value.artifact_count === 0) !== (value.artifact_bytes === 0)) ||
    (status === "not_run" && (value.version !== 1 || started || completed || hasFailureCode || hasFailureMessage ||
      value.failure_stage !== "none" || hasExecutionResidue)) ||
    (status === "running" && (value.version !== 2 || !started || completed || hasFailureCode || hasFailureMessage ||
      value.failure_stage !== "none" || hasExecutionResidue)) ||
    (status === "passed" && (value.version !== 3 || !started || !completed || value.failure_stage !== "none" ||
      hasFailureCode || hasFailureMessage || value.artifact_count < 1 || value.artifact_bytes < 1 ||
      cleanupKeys.some((key) => cleanup[key] !== true) ||
      ["console_errors", "page_errors", "failed_requests", "http_failures", "blocked_requests"]
        .some((key) => diagnostics[key] !== 0))) ||
    (!["not_run", "running", "passed"].includes(status) &&
      (value.version !== 3 || !started || !completed || value.failure_stage === "none" ||
        typeof value.failure_code !== "string" || value.failure_code === ""))) {
    throw new APIRequestError("UI evidence status is not fail-closed", "INVALID_RESPONSE", 502);
  }
  return value as unknown as UIEvidenceAttempt;
}

export function parseUIEvidenceArtifactMetadata(value: unknown, attempt: UIEvidenceAttempt):
  UIEvidenceArtifactMetadata {
  const required = ["attempt_id", "bytes", "created_at", "fingerprint", "id", "kind", "mime",
    "protocol_version", "redacted", "retention_policy", "run_id", "sha256", "source_commit",
    "step_id", "untrusted", "viewport"];
  const optional = ["height", "width"];
  const attemptStartedAt = attempt.started_at ? Date.parse(attempt.started_at) : Number.NaN;
  const attemptCompletedAt = attempt.completed_at ? Date.parse(attempt.completed_at) : undefined;
  if (!isRecord(value) || !hasOnlyKeys(value, [...required, ...optional]) ||
    required.some((key) => !Object.prototype.hasOwnProperty.call(value, key)) ||
    value.protocol_version !== "ui-evidence-artifact.v1" ||
    value.attempt_id !== attempt.manifest.attempt_id || value.run_id !== attempt.manifest.run_id ||
    !boundedIdentity(value.id) || !boundedIdentity(value.step_id) || !isSHA256(value.sha256) ||
    !isSHA256(value.fingerprint) || value.source_commit !== attempt.manifest.source.commit ||
    !safePositiveInteger(value.bytes) ||
    value.bytes > 32 * 1024 * 1024 || typeof value.mime !== "string" ||
    !["screenshot", "dom", "accessibility", "console", "network", "performance"]
      .includes(String(value.kind)) || typeof value.redacted !== "boolean" ||
    value.retention_policy !== "run_history" || value.untrusted !== true ||
    !validDate(value.created_at) || !Number.isFinite(attemptStartedAt) ||
    Date.parse(String(value.created_at)) < attemptStartedAt ||
    (attemptCompletedAt !== undefined && Date.parse(String(value.created_at)) > attemptCompletedAt) ||
    !hasExactKeys(value.viewport, ["dpr", "height", "width"]) ||
    !safePositiveInteger(value.viewport.width) || !safePositiveInteger(value.viewport.height) ||
    typeof value.viewport.dpr !== "number" || !Number.isFinite(value.viewport.dpr) ||
    value.viewport.width !== attempt.manifest.environment.viewport.width ||
    value.viewport.height !== attempt.manifest.environment.viewport.height ||
    value.viewport.dpr !== attempt.manifest.environment.viewport.dpr ||
    (value.kind === "screenshot" && (value.mime !== "image/png" ||
      !safePositiveInteger(value.width) || !safePositiveInteger(value.height) ||
      value.width > 7_680 || value.height > 4_320 ||
      Math.abs(value.width - value.viewport.width * value.viewport.dpr) > 1 ||
      Math.abs(value.height - value.viewport.height * value.viewport.dpr) > 1)) ||
    (value.kind !== "screenshot" &&
      (Object.prototype.hasOwnProperty.call(value, "width") ||
        Object.prototype.hasOwnProperty.call(value, "height")))) {
    throw new APIRequestError("UI evidence artifact metadata is invalid", "INVALID_RESPONSE", 502);
  }
  return value as unknown as UIEvidenceArtifactMetadata;
}

export function parseUIEvidenceBundle(value: unknown, attemptID: string): UIEvidenceBundle {
  if (!hasExactKeys(value, ["artifacts", "attempt", "steps"]) ||
    !Array.isArray(value.artifacts) || !Array.isArray(value.steps)) {
    throw new APIRequestError("UI evidence bundle is invalid", "INVALID_RESPONSE", 502);
  }
  const attempt = parseUIEvidenceAttempt(value.attempt);
  if (attempt.manifest.attempt_id !== attemptID || value.artifacts.length > 10_000 ||
    value.steps.length > 128) {
    throw new APIRequestError("UI evidence bundle identity is invalid", "INVALID_RESPONSE", 502);
  }
  const artifacts = value.artifacts.map((entry) => parseUIEvidenceArtifactMetadata(entry, attempt));
  const artifactIDs = new Set(artifacts.map((entry) => entry.id));
  const artifactFingerprints = new Set(artifacts.map((entry) => entry.fingerprint));
  const artifactBytes = artifacts.reduce((sum, entry) => sum + entry.bytes, 0);
  const manifestSteps = attempt.manifest.steps;
  const manifestStepIDs = new Set(manifestSteps.map((entry) => entry.id));
  const terminal = !["not_run", "running"].includes(attempt.status);
  if (artifactIDs.size !== artifacts.length || artifactFingerprints.size !== artifacts.length ||
    artifacts.some((entry) => !manifestStepIDs.has(entry.step_id)) ||
    (terminal && (artifacts.length !== attempt.artifact_count ||
      artifactBytes !== attempt.artifact_bytes)) ||
    (attempt.status === "passed" &&
      ["screenshot", "dom", "accessibility", "console", "network", "performance"]
        .some((kind) => !artifacts.some((entry) => entry.kind === kind)))) {
    throw new APIRequestError("UI evidence bundle artifact totals are inconsistent",
      "INVALID_RESPONSE", 502);
  }
  const steps = value.steps.map((entry) => {
    const required = ["attempt_id", "completed_at", "failure_stage", "fingerprint", "kind",
      "protocol_version", "sequence", "started_at", "status", "step_id"];
    const optional = ["message"];
    if (!isRecord(entry) || !hasOnlyKeys(entry, [...required, ...optional]) ||
      required.some((key) => !Object.prototype.hasOwnProperty.call(entry, key)) ||
      entry.protocol_version !== "ui-evidence-step.v1" ||
      entry.attempt_id !== attemptID || !boundedIdentity(entry.step_id) ||
      !safePositiveInteger(entry.sequence) || entry.sequence > 128 ||
      !["navigate", "click", "type", "assert_present", "assert_absent", "capture"]
        .includes(String(entry.kind)) ||
      !["passed", "failed", "cancelled", "timed_out"].includes(String(entry.status)) ||
      !uiEvidenceFailureStages.includes(String(entry.failure_stage) as typeof uiEvidenceFailureStages[number]) ||
      !isSHA256(entry.fingerprint) || !validDate(entry.started_at) || !validDate(entry.completed_at) ||
      !attempt.started_at || Date.parse(String(entry.started_at)) < Date.parse(attempt.started_at) ||
      (attempt.completed_at !== undefined &&
        Date.parse(String(entry.completed_at)) > Date.parse(attempt.completed_at)) ||
      Date.parse(String(entry.completed_at)) < Date.parse(String(entry.started_at)) ||
      (Object.prototype.hasOwnProperty.call(entry, "message") &&
        (typeof entry.message !== "string" || entry.message.length > 2_048)) ||
      ((entry.status === "passed") !== (entry.failure_stage === "none"))) {
      throw new APIRequestError("UI evidence step receipt is invalid", "INVALID_RESPONSE", 502);
    }
    const expected = manifestSteps[Number(entry.sequence) - 1];
    if (!expected || expected.id !== entry.step_id || expected.kind !== entry.kind) {
      throw new APIRequestError("UI evidence step receipt does not match its manifest",
        "INVALID_RESPONSE", 502);
    }
    return entry;
  });
  if (new Set(steps.map((entry) => entry.sequence)).size !== steps.length ||
    new Set(steps.map((entry) => entry.step_id)).size !== steps.length ||
    new Set(steps.map((entry) => entry.fingerprint)).size !== steps.length ||
    (attempt.status === "passed" &&
      (steps.length !== manifestSteps.length || steps.some((entry) => entry.status !== "passed")))) {
    throw new APIRequestError("UI evidence step receipts are inconsistent",
      "INVALID_RESPONSE", 502);
  }
  return { attempt, artifacts, steps } as unknown as UIEvidenceBundle;
}
