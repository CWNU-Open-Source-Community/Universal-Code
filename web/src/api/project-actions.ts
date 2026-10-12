import { APIRequestError } from "./client";
import type { ProjectActionResultView, ProjectActionsView } from "./types";

export type ProjectActionSlot = "test" | "format";
const states = new Set(["prepared", "preparing", "running", "stopping", "completed", "failed", "timed_out", "cancelled", "killed", "interrupted"]);
const record = (value: unknown): value is Record<string, unknown> =>
  value !== null && typeof value === "object" && !Array.isArray(value);
const slot = (value: unknown): value is ProjectActionSlot => value === "test" || value === "format";
const backend = (value: unknown) => value === null || typeof value === "string";
function invalid(): never {
  throw new APIRequestError("项目动作响应与当前执行不匹配。", "INVALID_RESPONSE", 502);
}

export function parseProjectActions(value: unknown, runID: string): ProjectActionsView {
  if (!record(value) || value.protocol_version !== "project-actions.v1" || value.run_id !== runID ||
    value.capability_grant !== false || !backend(value.backend) || !Array.isArray(value.actions) ||
    value.actions.length !== 2 || new Set(value.actions.map((item) => record(item) ? item.slot : null)).size !== 2 ||
    value.actions.some((item) => !record(item) || !slot(item.slot) || typeof item.command_id !== "string" ||
      typeof item.configured !== "boolean" || typeof item.available !== "boolean" ||
      (item.available && !item.configured) || typeof item.reason !== "string" ||
      !["directory", "go_file"].includes(String(item.target_kind)))) invalid();
  return value as unknown as ProjectActionsView;
}

export function parseProjectActionResult(value: unknown, runID: string, expectedSlot?: ProjectActionSlot, jobID?: string): ProjectActionResultView {
  if (!record(value) || value.protocol_version !== "project-actions.v1" || value.run_id !== runID ||
    !slot(value.slot) || (expectedSlot !== undefined && value.slot !== expectedSlot) ||
    typeof value.command_id !== "string" || !backend(value.backend) || typeof value.replayed !== "boolean" ||
    !record(value.job) || typeof value.job.id !== "string" || !value.job.id ||
    (jobID !== undefined && value.job.id !== jobID) || !states.has(String(value.job.state)) ||
    typeof value.job.stdout !== "string" || typeof value.job.stderr !== "string" ||
    typeof value.job.stdout_truncated !== "boolean" || typeof value.job.stderr_truncated !== "boolean" ||
    typeof value.job.can_stop !== "boolean" ||
    (value.job.exit_code !== undefined && !Number.isSafeInteger(value.job.exit_code))) invalid();
  return value as unknown as ProjectActionResultView;
}

export function projectActionIsRunning(state: string): boolean {
  return ["prepared", "preparing", "running", "stopping"].includes(state);
}
