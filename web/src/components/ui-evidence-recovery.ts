import type { UIEvidenceStartView } from "../api/types";
import { recoveryObject as object, recoveryPath, recoveryText as text } from "./verification-recovery";

export type DraftStep = { id: string; kind: UIEvidenceStartView["steps"][number]["step"]["kind"]; selector: string; input: string };
export interface UIEvidenceDraft {
  version: 1; runID: string; serviceID: string; name: string; command: string; directory: string;
  url: string; viewport: "desktop" | "mobile"; theme: "light" | "dark";
  steps: DraftStep[]; advanced: string; prepared: boolean;
}
export interface UIEvidenceIntent {
  version: 1; runID: string; request: UIEvidenceStartView; state: "pending" | "unknown" | "rejected";
}
export const uiEvidenceDraftKey = (runID: string) => `ui-evidence-draft:${JSON.stringify([runID])}`;
export const uiEvidenceIntentKey = (runID: string) => `ui-evidence-intent:${JSON.stringify([runID])}`;
export const emptyUIEvidenceDraft = (runID: string): UIEvidenceDraft => ({ version: 1, runID,
  serviceID: "", name: "", command: "", directory: ".", url: "", viewport: "desktop", theme: "light",
  steps: [], advanced: "", prepared: false });
const kinds = ["navigate", "click", "type", "assert_present", "assert_absent", "capture"];
const integer = (value: unknown, minimum: number, maximum: number) => Number.isSafeInteger(value) && Number(value) >= minimum && Number(value) <= maximum;
const digest = (value: unknown) => typeof value === "string" && /^[a-f0-9]{64}$/u.test(value);
function localURL(value: unknown): URL | null {
  if (!text(value, 4096)) return null;
  try { const url = new URL(value); return ["http:", "https:"].includes(url.protocol) &&
    ["localhost", "127.0.0.1", "[::1]"].includes(url.hostname) && url.port && !url.username && !url.password && !url.search && !url.hash ? url : null; }
  catch { return null; }
}
function command(value: unknown): boolean {
  return object(value) && value.version === "command-runtime.v2" && ["powershell", "bash", "process"].includes(String(value.profile)) &&
    recoveryPath(value.working_directory) && value.network === "disabled" && value.credentials === "none" &&
    integer(value.timeout_milliseconds, 1, 1_800_000) && text(value.purpose, 1200) &&
    Array.isArray(value.environment) && value.environment.length <= 32 && value.environment.every((entry) => object(entry) && text(entry.name, 128) && text(entry.value, 65536)) &&
    ["closed", "pipe"].includes(String(value.stdin_policy)) && typeof value.close_initial_stdin === "boolean" &&
    object(value.output) && integer(value.output.inline_bytes, 0, 1_048_576) && integer(value.output.artifact_bytes, 0, 16_777_216) &&
    (value.profile === "process" ? text(value.executable, 4096) && Array.isArray(value.arguments) && value.arguments.length <= 128 && value.arguments.every((entry) => text(entry, 65536)) : text(value.script, 65536));
}
export function validUIEvidenceRequest(value: unknown): value is UIEvidenceStartView {
  if (!object(value) || new TextEncoder().encode(JSON.stringify(value)).byteLength > 512 * 1024 ||
    !text(value.operation_key, 224) || !value.operation_key.trim() || value.operation_key !== value.operation_key.trim() ||
    !command(value.start) || (value.build !== undefined && !command(value.build)) || !object(value.readiness) || !object(value.browser) ||
    !object(value.environment) || !object(value.environment.viewport) || !object(value.fixture) || !object(value.capture) || !object(value.failure_policy)) return false;
  const url = localURL(value.url); const readiness = localURL(value.readiness.url);
  if (!url || !readiness || url.origin !== readiness.origin || value.route !== url.pathname ||
    value.readiness.method !== "GET" || !integer(value.readiness.timeout_milliseconds, 100, 120_000) || !integer(value.readiness.interval_milliseconds, 10, 5000) ||
    !Array.isArray(value.readiness.expected_status) || value.readiness.expected_status.length < 1 || value.readiness.expected_status.length > 16 || !value.readiness.expected_status.every((entry) => integer(entry, 100, 599)) ||
    !["edge", "chrome"].includes(String(value.browser.product)) || !text(value.browser.channel, 128) ||
    !integer(value.environment.viewport.width, 320, 7680) || !integer(value.environment.viewport.height, 240, 4320) ||
    typeof value.environment.viewport.dpr !== "number" || !Number.isFinite(value.environment.viewport.dpr) || value.environment.viewport.dpr < 0.5 || value.environment.viewport.dpr > 4 ||
    Number(value.environment.viewport.width) * value.environment.viewport.dpr > 7680 || Number(value.environment.viewport.height) * value.environment.viewport.dpr > 4320 ||
    !["light", "dark"].includes(String(value.environment.theme)) || !text(value.environment.locale, 64) || typeof value.environment.reduced_motion !== "boolean" ||
    !text(value.fixture.name, 256) || !text(value.fixture.seed, 256) || !text(value.fixture.page_state, 8192) || !digest(value.fixture.data_sha256) || value.fixture.deterministic !== true || value.fixture.synthetic !== true ||
    ["screenshot", "dom", "accessibility", "console", "network", "performance"].some((key) => value.capture && (value.capture as Record<string, unknown>)[key] !== true) || value.capture.video !== false ||
    !Array.isArray(value.capture.mask_selectors) || value.capture.mask_selectors.length > 32 || !value.capture.mask_selectors.every((entry) => text(entry, 2048)) ||
    ["fail_on_console_error", "fail_on_page_error", "fail_on_request_error", "fail_on_http_status"].some((key) => value.failure_policy && (value.failure_policy as Record<string, unknown>)[key] !== true) ||
    !Array.isArray(value.steps) || value.steps.length < 1 || value.steps.length > 128) return false;
  const ids = new Set<string>();
  return value.steps.every((entry, index) => {
    if (!object(entry) || !object(entry.step) || !text(entry.step.id, 128) || !entry.step.id || ids.has(entry.step.id) ||
      !kinds.includes(String(entry.step.kind)) || typeof entry.step.capture_after !== "boolean" || index === 0 && entry.step.kind !== "navigate") return false;
    ids.add(entry.step.id);
    return (!["click", "type", "assert_present", "assert_absent"].includes(String(entry.step.kind)) || text(entry.step.selector, 2048) && Boolean(entry.step.selector.trim())) &&
      (entry.step.kind !== "type" || text(entry.input, 65536) && digest(entry.step.input_sha256));
  });
}
export function validUIEvidenceDraft(value: unknown, runID: string): value is UIEvidenceDraft {
  if (!(object(value) && value.version === 1 && value.runID === runID && text(value.serviceID, 256) && text(value.name, 256) &&
    text(value.command, 65536) && text(value.directory, 4096) && text(value.url, 4096) &&
    ["desktop", "mobile"].includes(String(value.viewport)) && ["light", "dark"].includes(String(value.theme)) &&
    text(value.advanced, 2 * 1024 * 1024) && typeof value.prepared === "boolean" && Array.isArray(value.steps) && value.steps.length <= 63 &&
    value.steps.every((step) => object(step) && text(step.id, 128) && kinds.slice(1).includes(String(step.kind)) && text(step.selector, 2048) && text(step.input, 65536)))) return false;
  if (value.prepared) {
    try { return validUIEvidenceRequest(JSON.parse(value.advanced as string)); }
    catch { return false; }
  }
  return true;
}
export function validUIEvidenceIntent(value: unknown, runID: string): value is UIEvidenceIntent | null {
  return value === null || object(value) && value.version === 1 && value.runID === runID &&
    ["pending", "unknown", "rejected"].includes(String(value.state)) && validUIEvidenceRequest(value.request);
}
