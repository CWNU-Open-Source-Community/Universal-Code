export interface ThreadPeerTarget {
  id: string;
  title: string;
  workspace_id: string;
  status: string;
}

export interface ThreadPeerTargets {
  version: "thread_peer_targets.v1";
  source_thread_id: string;
  workspace_id: string;
  items: ThreadPeerTarget[];
  has_more: boolean;
}

export interface ThreadPeerMessage {
  id: string;
  source_thread_id: string;
  source_title: string;
  target_thread_id: string;
  target_title: string;
  content: string;
  reply_to?: string;
  status: "queued" | "delivered" | "failed" | "cancelled";
  created_at: string;
  delivered_at?: string;
  error?: string;
}

export interface ThreadPeerMessageRequest {
  version: "thread_peer_message.v1";
  target_thread_id: string;
  content: string;
  reply_to?: string;
}

export interface ThreadPeerMessageResult {
  version: "thread_peer_message.v1";
  message: ThreadPeerMessage;
  replayed: boolean;
}

export const maximumPeerMessageBytes = 16_384;
export const peerMessageBytes = (content: string) => new TextEncoder().encode(content).length;
const record = (value: unknown): value is Record<string, unknown> =>
  value !== null && typeof value === "object" && !Array.isArray(value);
const identity = (value: unknown): value is string =>
  typeof value === "string" && value.length > 0 && value.length <= 256 && value.trim() === value && !/[\u0000-\u001f\u007f]/u.test(value);
const text = (value: unknown): value is string => typeof value === "string" && !value.includes("\0");
const date = (value: unknown): value is string => typeof value === "string" && Number.isFinite(Date.parse(value));

export function validThreadPeerMessage(value: unknown): value is ThreadPeerMessage {
  return record(value) && identity(value.id) && identity(value.source_thread_id) && identity(value.target_thread_id) &&
    value.source_thread_id !== value.target_thread_id && text(value.source_title) && text(value.target_title) &&
    text(value.content) && value.content.trim().length > 0 && peerMessageBytes(value.content) <= maximumPeerMessageBytes &&
    ["queued", "delivered", "failed", "cancelled"].includes(String(value.status)) && date(value.created_at) &&
    (value.reply_to === undefined || identity(value.reply_to)) &&
    (value.delivered_at === undefined || date(value.delivered_at)) &&
    (value.error === undefined || text(value.error));
}

export function parseThreadPeerTargets(value: unknown, sourceThreadID: string): ThreadPeerTargets {
  if (!record(value) || value.version !== "thread_peer_targets.v1" || value.source_thread_id !== sourceThreadID ||
    !text(value.workspace_id) || typeof value.has_more !== "boolean" || !Array.isArray(value.items) ||
    value.items.length > 100 || !value.items.every((item) => record(item) && identity(item.id) &&
      item.id !== sourceThreadID && text(item.title) && item.workspace_id === value.workspace_id && item.status === "active") ||
    new Set(value.items.map((item) => item.id)).size !== value.items.length) {
    throw new Error("The available chats could not be confirmed.");
  }
  return value as unknown as ThreadPeerTargets;
}

export function parseThreadPeerMessageResult(value: unknown, sourceThreadID: string,
  request: ThreadPeerMessageRequest): ThreadPeerMessageResult {
  if (!record(value) || value.version !== "thread_peer_message.v1" || typeof value.replayed !== "boolean" ||
    !validThreadPeerMessage(value.message) || value.message.source_thread_id !== sourceThreadID ||
    value.message.target_thread_id !== request.target_thread_id ||
    (value.message.reply_to ?? "") !== (request.reply_to ?? "")) {
    throw new Error("The message delivery could not be confirmed. Retry without changing the message.");
  }
  return value as unknown as ThreadPeerMessageResult;
}
