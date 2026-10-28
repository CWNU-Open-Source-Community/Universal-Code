import { APIRequestError } from "./client/protocol";
import { type APIClient } from "./client";

export const agentBrowserStatusVersion = "agent_browser_status.v1";
export const agentBrowserCloseVersion = "agent_browser_close.v1";

export interface AgentBrowserScreenshot {
  locator: string;
  sha256: string;
  byte_size: number;
  mime_type: "image/png";
}

export interface AgentBrowserStatus {
  version: typeof agentBrowserStatusVersion;
  run_id: string;
  session_id?: string;
  generation: number;
  state: "unavailable" | "idle" | "starting" | "ready" | "loading" | "busy" | "waiting_user" | "failed" | "closing" | "closed" | "cleanup_pending";
  available: boolean;
  can_start: boolean;
  product?: string;
  headless: boolean;
  url?: string;
  title?: string;
  document_epoch: number;
  last_action?: string;
  updated_at: string;
  screenshot?: AgentBrowserScreenshot;
  failure_code?: "action_failed" | "browser_unavailable";
  unavailable_code?: "platform_unsupported" | "full_access_required" | "runtime_unavailable" | "run_unavailable" | "session_unavailable";
  cleanup_pending: boolean;
  tree_reaped: boolean;
  profile_removed: boolean;
  process_scope?: "no_owned_process_resources" | "darwin_group_and_scoped_children.v1";
  process_scope_verified?: boolean;
}

const states = new Set<AgentBrowserStatus["state"]>(["unavailable", "idle", "starting", "ready", "loading", "busy", "waiting_user", "failed", "closing", "closed", "cleanup_pending"]);
const sha256 = /^[0-9a-f]{64}$/u;
const unavailableCodes = new Set(["platform_unsupported", "full_access_required", "runtime_unavailable", "run_unavailable", "session_unavailable"]);

function record(value: unknown): value is Record<string, unknown> {
  return typeof value === "object" && value !== null && !Array.isArray(value);
}

function identity(value: unknown): value is string {
  return typeof value === "string" && value.length > 0 && value.length <= 256 && value.trim() === value && !value.includes("\0");
}

function optionalText(value: unknown, maximum: number): value is string | undefined {
  return value === undefined || typeof value === "string" && value.length <= maximum && !value.includes("\0");
}

function safePageURL(value: unknown): value is string | undefined {
  if (value === undefined) return true;
  if (typeof value !== "string" || value.length > 8192) return false;
  try {
    const parsed = new URL(value);
    return (parsed.protocol === "http:" || parsed.protocol === "https:") && parsed.host !== "" && parsed.username === "" && parsed.password === "";
  } catch { return false; }
}

function validScreenshot(value: unknown): value is AgentBrowserScreenshot {
  if (!record(value)) return false;
  return identity(value.locator) && typeof value.sha256 === "string" && sha256.test(value.sha256) &&
    Number.isSafeInteger(value.byte_size) && Number(value.byte_size) > 0 && Number(value.byte_size) <= 20 * 1024 * 1024 &&
    value.mime_type === "image/png";
}

export function parseAgentBrowserStatus(value: unknown, runID: string): AgentBrowserStatus {
  if (!record(value) || value.version !== agentBrowserStatusVersion || value.run_id !== runID ||
    !identity(runID) || !(typeof value.state === "string" && states.has(value.state as AgentBrowserStatus["state"])) ||
    typeof value.available !== "boolean" || typeof value.can_start !== "boolean" || typeof value.headless !== "boolean" ||
    !Number.isSafeInteger(value.generation) || Number(value.generation) < 0 ||
    !Number.isSafeInteger(value.document_epoch) || Number(value.document_epoch) < 0 ||
    !optionalText(value.product, 64) || !optionalText(value.title, 1024) || !optionalText(value.last_action, 128) ||
    !safePageURL(value.url) || typeof value.updated_at !== "string" || !Number.isFinite(Date.parse(value.updated_at)) ||
    typeof value.cleanup_pending !== "boolean" || typeof value.tree_reaped !== "boolean" || typeof value.profile_removed !== "boolean" ||
    (value.process_scope !== undefined && value.process_scope !== "no_owned_process_resources" && value.process_scope !== "darwin_group_and_scoped_children.v1") ||
    (value.process_scope_verified !== undefined && typeof value.process_scope_verified !== "boolean") ||
    (value.process_scope === undefined && value.process_scope_verified === true) ||
    (value.process_scope !== undefined && (value.tree_reaped || typeof value.process_scope_verified !== "boolean")) ||
    (value.session_id !== undefined && !identity(value.session_id)) ||
    (value.failure_code !== undefined && value.failure_code !== "action_failed" && value.failure_code !== "browser_unavailable") ||
    (value.unavailable_code !== undefined && (typeof value.unavailable_code !== "string" || !unavailableCodes.has(value.unavailable_code))) ||
    (value.screenshot !== undefined && !validScreenshot(value.screenshot))) {
    throw new Error("Agent 浏览器状态响应无效。");
  }
  const status = value as unknown as AgentBrowserStatus;
  return { ...status, ...(status.screenshot ? { screenshot: { ...status.screenshot } } : {}) };
}

export function agentBrowserQueryKey(baseURL: string, runID: string) {
  return ["agent-browser", baseURL, runID] as const;
}

export async function readAgentBrowser(client: Pick<APIClient, "get">, runID: string, signal?: AbortSignal) {
  return parseAgentBrowserStatus(await client.get<unknown>(`/runs/${encodeURIComponent(runID)}/agent-browser`, {}, signal), runID);
}

export async function closeAgentBrowser(client: Pick<APIClient, "postControl">, runID: string, sessionID: string) {
  if (!identity(sessionID)) throw new Error("Agent 浏览器会话无效。");
  return parseAgentBrowserStatus(await client.postControl<unknown>(`/runs/${encodeURIComponent(runID)}/agent-browser/close`, {
    version: agentBrowserCloseVersion, session_id: sessionID,
  }, `agent-browser-close-${sessionID}`), runID);
}

export function agentBrowserAPIAbsent(error: unknown): boolean {
  return error instanceof APIRequestError && error.status === 404;
}

export const agentBrowserScreencastVersion = "agent_browser_screencast.v1";

const screencastBase64 = /^[A-Za-z0-9+/]+={0,2}$/u;
const maxScreencastFrameData = 700 * 1024;

export interface AgentBrowserScreencastFrame {
  version: typeof agentBrowserScreencastVersion;
  run_id: string;
  session_id: string;
  generation: number;
  media_type: "image/jpeg";
  width: number;
  height: number;
  data: string;
  captured_at: string;
}

export function parseAgentBrowserScreencastFrame(value: unknown, runID: string): AgentBrowserScreencastFrame {
  if (!record(value) || value.version !== agentBrowserScreencastVersion || value.run_id !== runID ||
    !identity(value.session_id) || value.media_type !== "image/jpeg" ||
    typeof value.data !== "string" || value.data.length === 0 || value.data.length > maxScreencastFrameData ||
    !screencastBase64.test(value.data) ||
    !Number.isSafeInteger(value.generation) || Number(value.generation) < 0 ||
    !Number.isSafeInteger(value.width) || Number(value.width) < 0 || Number(value.width) > 4096 ||
    !Number.isSafeInteger(value.height) || Number(value.height) < 0 || Number(value.height) > 4096 ||
    typeof value.captured_at !== "string" || !Number.isFinite(Date.parse(value.captured_at))) {
    throw new Error("Agent 浏览器实时画面帧无效。");
  }
  return value as unknown as AgentBrowserScreencastFrame;
}
