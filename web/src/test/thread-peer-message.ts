import type { ThreadPeerMessage } from "../api/thread-peer-messages";

export const peerMessageFixture = (patch: Partial<ThreadPeerMessage> = {}): ThreadPeerMessage => ({
  id: "peer-message-1", source_thread_id: "thread-a", source_title: "API migration",
  target_thread_id: "thread-b", target_title: "Frontend integration",
  content: "The new response includes the workspace ID.", status: "queued", created_at: "2026-10-19T09:00:00Z", ...patch,
});
