import type { components } from "./schema";

export type ThreadPeerMessage = components["schemas"]["ThreadPeerMessage"];

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
