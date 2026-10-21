import { APIRequestError } from "./client/protocol";
import { type APIClient } from "./client";
import type { components } from "./schema";

export type ThreadPlanRequest = components["schemas"]["ThreadPlanControlRequestView"];
export type ThreadPlanObservation = Omit<components["schemas"]["ThreadPlanControlView"], "capability_grant"> & { capability_grant: false };
export interface ThreadPlanAttempt { threadID: string; key: string; body: ThreadPlanRequest; responseRejected?: true }
const record = (value: unknown): value is Record<string, unknown> => Boolean(value) && typeof value === "object" && !Array.isArray(value);
const identity = (value: unknown): value is string => typeof value === "string" && value.length > 0 && value.length <= 512 && value.trim() === value;
export function validThreadPlanAttempt(value: unknown, threadID: string): value is ThreadPlanAttempt {
  if (!record(value) || value.threadID !== threadID || typeof value.key !== "string" || !/^thread-plan-[\w-]+$/u.test(value.key) ||
    value.responseRejected !== undefined && value.responseRejected !== true ||
    !record(value.body) || value.body.version !== "plan_delivery_control.v1" || !identity(value.body.run_id)) return false;
  const body = value.body;
  if (body.action === "enter_plan" || body.action === "enter_deliver") return ["proposal_id", "direction", "manual_acceptance", "content"].every((key) => body[key] === undefined);
  return body.action === "confirm" && identity(body.proposal_id) && Number.isSafeInteger(body.direction) && Number(body.direction) > 0 &&
    ["required", "on_demand"].includes(String(body.manual_acceptance)) && typeof body.content === "string" && body.content.trim().length > 0 &&
    new TextEncoder().encode(body.content).length <= 16 * 1024;
}
export function parseThreadPlanObservation(value: unknown, attempt: ThreadPlanAttempt): ThreadPlanObservation {
  const invalid = () => new APIRequestError("计划操作结果的来源或格式不匹配，请核对原请求。", "INVALID_RESPONSE", 502);
  if (!record(value) || value.version !== "plan_delivery_control.v1" || value.thread_id !== attempt.threadID || value.run_id !== attempt.body.run_id ||
    value.action !== attempt.body.action || !["not_received", "prepared", "received", "completed", "failed", "rejected"].includes(String(value.state)) ||
    value.capability_grant !== false || ["execution_started", "model_called", "tool_called"].some((key) => typeof value[key] !== "boolean")) throw invalid();
  if (value.proposal_id && value.proposal_id !== attempt.body.proposal_id || value.direction && value.direction !== attempt.body.direction ||
    value.manual_acceptance && value.manual_acceptance !== attempt.body.manual_acceptance) throw invalid();
  for (const key of ["applied_mode", "current_mode"]) if (value[key] !== undefined) {
    const mode = value[key];
    if (!record(mode) || mode.protocol_version !== "run_mode.v1" || !["plan", "deliver"].includes(String(mode.phase))) throw invalid();
  }
  if (["received", "completed", "failed"].includes(String(value.state)) &&
    (!record(value.applied_mode) || value.applied_mode.phase !== (attempt.body.action === "enter_plan" ? "plan" : "deliver"))) throw invalid();
  if (value.turn_request !== undefined) {
    const turn = value.turn_request;
    if (!record(turn) || typeof turn.state !== "string" || typeof turn.settled !== "boolean" ||
      turn.thread_id && turn.thread_id !== attempt.threadID || turn.run_id && turn.run_id !== attempt.body.run_id) throw invalid();
  }
  if (attempt.body.action === "confirm" && ["prepared", "received", "completed", "failed"].includes(String(value.state)) &&
    (value.proposal_id !== attempt.body.proposal_id || value.direction !== attempt.body.direction ||
      value.manual_acceptance !== attempt.body.manual_acceptance || !identity(value.selection_id))) throw invalid();
  if (attempt.body.action === "confirm" && ["received", "completed", "failed"].includes(String(value.state)) &&
    (!record(value.turn_request) || !identity(value.turn_request.message_id) || value.turn_request.thread_id !== attempt.threadID || value.turn_request.run_id !== attempt.body.run_id)) throw invalid();
  return value as unknown as ThreadPlanObservation;
}
export async function executeThreadPlan(client: APIClient, attempt: ThreadPlanAttempt): Promise<ThreadPlanObservation> {
  return parseThreadPlanObservation(await client.postControl<unknown>(`/threads/${encodeURIComponent(attempt.threadID)}/plan`, attempt.body, attempt.key), attempt);
}
export async function observeThreadPlan(client: APIClient, attempt: ThreadPlanAttempt, signal?: AbortSignal): Promise<ThreadPlanObservation> {
  return parseThreadPlanObservation(await client.inspectThreadPlanRequest(attempt.threadID, attempt.body.run_id, attempt.body.action, attempt.key, signal), attempt);
}
