import { APIRequestError } from "./client";
import type { ThreadApplicationServiceDetailView, ThreadApplicationServicesView,
  ThreadApplicationServiceStopView, ThreadApplicationServiceView } from "./types";

const version = "thread_application_services.v1";
const states = new Set(["prepared", "running", "stopping", "completed", "failed", "timed_out", "cancelled", "killed", "interrupted"]);
const activeStates = new Set(["prepared", "running", "stopping"]);
const record = (value: unknown): value is Record<string, unknown> =>
  value !== null && typeof value === "object" && !Array.isArray(value);
const keys = (value: unknown, required: string[], optional: string[] = []): value is Record<string, unknown> =>
  record(value) && required.every((key) => Object.hasOwn(value, key)) &&
  Object.keys(value).every((key) => required.includes(key) || optional.includes(key));
export const applicationServiceIdentity = (value: unknown): value is string =>
  typeof value === "string" && value.length > 0 && value.length <= 256 && value.trim() === value && !/[\u0000-\u001f\u007f/\\]/u.test(value);
const integer = (value: unknown): value is number => typeof value === "number" && Number.isSafeInteger(value);
const cursor = (value: unknown): value is number => integer(value) && value >= 0;
const date = (value: unknown) => typeof value === "string" && Number.isFinite(Date.parse(value));
function invalid(): never { throw new APIRequestError("应用服务结果无法验证，请重新读取任务状态。", "INVALID_RESPONSE", 502); }

function service(value: unknown, threadID: string, jobID?: string, runID?: string): ThreadApplicationServiceView {
  if (!keys(value, ["thread_id", "run_id", "job_id", "state", "created_at", "can_stop"],
    ["exit_code", "started_at", "completed_at", "source_call_id", "source_message_id", "source_turn", "workspace_mode", "tree_reaped"]) ||
    value.thread_id !== threadID || !applicationServiceIdentity(value.run_id) || !applicationServiceIdentity(value.job_id) ||
    (jobID !== undefined && value.job_id !== jobID) || (runID !== undefined && value.run_id !== runID) ||
    typeof value.state !== "string" || !states.has(value.state) || !date(value.created_at) ||
    typeof value.can_stop !== "boolean" || (value.can_stop && !activeStates.has(value.state)) ||
    (value.workspace_mode !== undefined && value.workspace_mode !== "exclusive" && value.workspace_mode !== "shared_service") ||
    (value.tree_reaped !== undefined && typeof value.tree_reaped !== "boolean") ||
    (value.exit_code !== undefined && !integer(value.exit_code)) ||
    (value.started_at !== undefined && !date(value.started_at)) ||
    (value.completed_at !== undefined && !date(value.completed_at)) ||
    (value.source_call_id !== undefined && !applicationServiceIdentity(value.source_call_id)) ||
    (value.source_message_id !== undefined && (!applicationServiceIdentity(value.source_message_id) || !value.source_call_id)) ||
    (value.source_turn !== undefined && (!integer(value.source_turn) || value.source_turn <= 0 || !value.source_call_id))) invalid();
  return value as ThreadApplicationServiceView;
}

export function parseThreadApplicationServices(value: unknown, threadID: string): ThreadApplicationServicesView {
  if (!keys(value, ["version", "thread_id", "services", "has_more"]) || value.version !== version ||
    value.thread_id !== threadID || typeof value.has_more !== "boolean" || !Array.isArray(value.services) || value.services.length > 50) invalid();
  const services = value.services.map((item) => service(item, threadID));
  if (new Set(services.map((item) => item.job_id)).size !== services.length) invalid();
  return { ...value, services } as ThreadApplicationServicesView;
}

function candidateURL(value: unknown): boolean {
  if (typeof value !== "string" || value.length > 2048 || value.trim() !== value || /[\u0000-\u0020\u007f\\]/u.test(value) ||
    !/^https?:\/\/(?:127\.0\.0\.1|\[::1\])(?::\d{1,5})?(?:\/|$)/u.test(value)) return false;
  try {
    const url = new URL(value);
    return (url.protocol === "http:" || url.protocol === "https:") && ["127.0.0.1", "[::1]"].includes(url.hostname) &&
      !url.username && !url.password && !url.search && !url.hash;
  } catch { return false; }
}

export function parseThreadApplicationService(value: unknown, threadID: string, jobID: string): ThreadApplicationServiceDetailView {
  if (!keys(value, ["version", "service", "output", "candidate_urls"]) || value.version !== version) invalid();
  service(value.service, threadID, jobID);
  const output = value.output;
  if (!keys(output, ["stdout", "stderr", "base_cursor", "next_cursor", "end_cursor", "dropped", "available"], ["truncation_reason"]) ||
    !cursor(output.base_cursor) || !cursor(output.next_cursor) || !cursor(output.end_cursor) ||
    output.base_cursor > output.next_cursor || output.next_cursor > output.end_cursor || typeof output.dropped !== "boolean" ||
    typeof output.available !== "boolean" || typeof output.stdout !== "string" || typeof output.stderr !== "string" ||
    (output.truncation_reason !== undefined && (typeof output.truncation_reason !== "string" || output.truncation_reason.length > 256))) invalid();
  const encoder = new TextEncoder();
  if (encoder.encode(output.stdout).length + encoder.encode(output.stderr).length > 65536) invalid();
  if (!Array.isArray(value.candidate_urls) || value.candidate_urls.length > 8 ||
    value.candidate_urls.some((candidate) => !keys(candidate, ["url", "source", "verified"]) ||
      !candidateURL(candidate.url) || candidate.source !== "command_output" || candidate.verified !== false) ||
    new Set(value.candidate_urls.map((candidate) => candidate.url)).size !== value.candidate_urls.length ||
    (!output.available && (output.stdout.length > 0 || output.stderr.length > 0 || value.candidate_urls.length > 0))) invalid();
  return value as ThreadApplicationServiceDetailView;
}

export function parseThreadApplicationServiceStop(value: unknown, threadID: string, runID: string, jobID: string): ThreadApplicationServiceStopView {
  if (!keys(value, ["version", "service", "replayed"]) || value.version !== version || typeof value.replayed !== "boolean") invalid();
  const stopped = service(value.service, threadID, jobID, runID);
  if (stopped.state === "prepared" || stopped.state === "running") invalid();
  return value as ThreadApplicationServiceStopView;
}
