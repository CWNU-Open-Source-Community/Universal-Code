import { APIRequestError } from "./client/protocol";
import { type APIClient } from "./client";
import type { components } from "./schema";

export const PROJECT_HOOKS_VERSION = "project-hooks.v1";
export type ProjectHooksView = components["schemas"]["ProjectHooksView"];
export type ProjectHookView = components["schemas"]["ProjectHookView"];
export type ProjectHooksReviewRequest = components["schemas"]["ProjectHooksReviewRequest"];
export type ProjectHooksAction = "trust" | "disable";

const record = (value: unknown): value is Record<string, unknown> => Boolean(value && typeof value === "object" && !Array.isArray(value));
const identity = (value: unknown): value is string => typeof value === "string" && /^[\w.-]{1,256}$/u.test(value);
const fingerprint = (value: unknown): value is string => typeof value === "string" && /^[a-f0-9]{64}$/u.test(value);

export function parseProjectHooks(value: unknown, threadID: string): ProjectHooksView {
  const invalid = () => new APIRequestError("项目自动动作响应与当前任务不匹配，请刷新核对。", "INVALID_RESPONSE", 502);
  if (!record(value) || value.version !== PROJECT_HOOKS_VERSION || value.thread_id !== threadID || !identity(value.workspace_id) ||
    !Number.isSafeInteger(value.revision) || Number(value.revision) < 0 || typeof value.trusted !== "boolean" ||
    typeof value.matches_task !== "boolean" || typeof value.message !== "string" || !Array.isArray(value.hooks) || value.hooks.length > 2 ||
    !["not_configured", "needs_review", "trusted", "changed", "disabled"].includes(String(value.state)) ||
    value.trusted !== (value.state === "trusted")) throw invalid();
  if (value.state === "not_configured" ? value.fingerprint !== "" || value.hooks.length !== 0 : !fingerprint(value.fingerprint) || value.hooks.length === 0) throw invalid();
  const seen = new Set<string>();
  for (const hook of value.hooks) {
    if (!record(hook) || typeof hook.event !== "string" || seen.has(hook.event) || typeof hook.target !== "string" || !hook.target.trim() ||
      !(hook.event === "after_edit" && hook.command_id === "format.go" ||
        hook.event === "before_finish" && ["test.go", "test.npm", "test.node", "test.python", "test.pytest", "test.rust"].includes(String(hook.command_id)))) throw invalid();
    seen.add(hook.event);
  }
  return value as unknown as ProjectHooksView;
}

const path = (threadID: string) => `/threads/${encodeURIComponent(threadID)}/project-hooks`;
export async function readProjectHooks(client: APIClient, threadID: string, signal?: AbortSignal): Promise<ProjectHooksView> {
  return parseProjectHooks(await client.get<unknown>(path(threadID), {}, signal), threadID);
}

export async function reviewProjectHooks(client: APIClient, view: ProjectHooksView, action: ProjectHooksAction): Promise<ProjectHooksView> {
  if (!client.hasControl || !["trust", "disable"].includes(action) || !fingerprint(view.fingerprint)) throw new Error("审阅项目自动动作需要控制权限和当前配置。");
  parseProjectHooks(view, view.thread_id);
  const body: ProjectHooksReviewRequest = { version: PROJECT_HOOKS_VERSION, expected_fingerprint: view.fingerprint, expected_revision: view.revision };
  const result = parseProjectHooks(await client.postControl<unknown>(`${path(view.thread_id)}/${action}`, body,
    `project-hooks-${action}-${view.fingerprint}-${view.revision}`), view.thread_id);
  if (result.workspace_id !== view.workspace_id || result.fingerprint !== view.fingerprint || result.revision <= view.revision ||
    result.trusted !== (action === "trust") || result.state !== (action === "trust" ? "trusted" : "disabled")) {
    throw new APIRequestError("返回的项目自动动作审阅结果已变化，请刷新核对。", "INVALID_RESPONSE", 502);
  }
  return result;
}
