import type { APIClient } from "./client";
import type { components } from "./schema";

export const PR_FOLLOWUP_VERSION = "pr-followup.v1";
export type PRFollowupStatus = "active" | "paused" | "blocked" | "ready" | "completed" | "cancelled" | "exhausted";
export type PRFollowup = Omit<components["schemas"]["PRFollowup"], "status"> & { status: PRFollowupStatus };
export type PRFollowupList = Omit<components["schemas"]["PRFollowupList"], "items"> & { items: PRFollowup[] };
export type PRFollowupCreateRequest = components["schemas"]["PRFollowupCreateRequest"];
export type PRFollowupAction = "pause" | "resume" | "cancel";

const record = (value: unknown): value is Record<string, unknown> => Boolean(value && typeof value === "object" && !Array.isArray(value));
const identity = (value: unknown): value is string => typeof value === "string" && /^[\w.-]{1,256}$/u.test(value);
const integer = (value: unknown, min: number, max = Number.MAX_SAFE_INTEGER) => Number.isSafeInteger(value) && Number(value) >= min && Number(value) <= max;
const date = (value: unknown): value is string => typeof value === "string" && Number.isFinite(Date.parse(value));
const statuses: PRFollowupStatus[] = ["active", "paused", "blocked", "ready", "completed", "cancelled", "exhausted"];
export const prFollowupTerminal = (value: PRFollowup) => ["completed", "cancelled", "exhausted"].includes(value.status);

export function validPRFollowupCreateRequest(value: unknown): value is PRFollowupCreateRequest {
  return record(value) && Object.keys(value).length === 7 && value.version === PR_FOLLOWUP_VERSION && identity(value.connection_id) &&
    integer(value.pull_request, 1) && integer(value.interval_seconds, 60, 3600) && date(value.deadline_at) &&
    integer(value.max_turns, 1, 20) && typeof value.include_existing_comments === "boolean";
}

function parsePRFollowup(value: unknown, threadID: string): PRFollowup {
  if (!record(value) || !identity(value.id) || value.thread_id !== threadID || !identity(value.connection_id) ||
    typeof value.repository !== "string" || typeof value.branch !== "string" || !identity(value.workspace_id) ||
    typeof value.root_path_sha256 !== "string" || !/^[a-f0-9]{64}$/u.test(value.root_path_sha256) ||
    !integer(value.pull_request, 1) || !integer(value.interval_seconds, 60, 3600) || !integer(value.max_turns, 1, 20) ||
    !integer(value.turns_started, 0, Number(value.max_turns)) || !integer(value.revision, 1) ||
    !statuses.includes(value.status as PRFollowupStatus) || typeof value.include_existing_comments !== "boolean" ||
    !date(value.deadline_at) || !date(value.next_check_at) || !date(value.created_at) || !date(value.updated_at) ||
    (value.last_checked_at !== undefined && !date(value.last_checked_at)) ||
    [value.last_head_sha, value.last_result, value.last_error].some((item) => item !== undefined && typeof item !== "string")) {
    throw new Error("持续跟进记录与当前任务不一致或不完整，请刷新核对。");
  }
  return value as unknown as PRFollowup;
}

const path = (threadID: string) => `/threads/${encodeURIComponent(threadID)}/pr-followups`;
export async function readPRFollowups(client: APIClient, threadID: string, signal?: AbortSignal): Promise<PRFollowupList> {
  const value = await client.get<unknown>(path(threadID), {}, signal);
  if (!record(value) || value.version !== PR_FOLLOWUP_VERSION || value.thread_id !== threadID ||
    typeof value.worker_state !== "string" || !Array.isArray(value.items)) throw new Error("无法确认本任务的持续跟进状态，请刷新。");
  const items = value.items.map((item) => parsePRFollowup(item, threadID));
  if (items.filter((item) => !prFollowupTerminal(item)).length > 1) throw new Error("本任务存在多份未结束的跟进记录，请先核对。");
  return { version: PR_FOLLOWUP_VERSION, thread_id: threadID, worker_state: value.worker_state, items };
}

export async function createPRFollowup(client: APIClient, threadID: string, body: PRFollowupCreateRequest, key: string): Promise<PRFollowup> {
  const value = parsePRFollowup(await client.postControl<unknown>(path(threadID), body, key), threadID);
  if (value.connection_id !== body.connection_id || value.pull_request !== body.pull_request || value.interval_seconds !== body.interval_seconds ||
    Date.parse(value.deadline_at) !== Date.parse(body.deadline_at) || value.max_turns !== body.max_turns || value.include_existing_comments !== body.include_existing_comments) {
    throw new Error("返回的持续跟进设置与原请求不一致，请保留原请求并核对。");
  }
  return value;
}

export async function transitionPRFollowup(client: APIClient, value: PRFollowup, action: PRFollowupAction): Promise<PRFollowup> {
  const result = parsePRFollowup(await client.postControl<unknown>(`${path(value.thread_id)}/${encodeURIComponent(value.id)}/${action}`,
    { version: PR_FOLLOWUP_VERSION, expected_revision: value.revision }, `pr-followup-${value.id}-${action}-${value.revision}`), value.thread_id);
  if (result.id !== value.id) throw new Error("返回的持续跟进对象不匹配，请刷新。");
  return result;
}
