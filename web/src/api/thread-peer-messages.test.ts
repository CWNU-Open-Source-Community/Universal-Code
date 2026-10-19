import { afterEach, expect, it, vi } from "vitest";
import { APIClient } from "./client";
import { parseThreadPeerMessageResult, parseThreadPeerTargets, peerMessageBytes, type ThreadPeerMessageRequest } from "./thread-peer-messages";
import { peerMessageFixture } from "../test/thread-peer-message";

afterEach(() => vi.unstubAllGlobals());

it("binds the delivery receipt to the source, destination and reply while accepting server redaction", () => {
  const message = peerMessageFixture({ reply_to: "previous-message" });
  const request: ThreadPeerMessageRequest = { version: "thread_peer_message.v1", target_thread_id: message.target_thread_id,
    content: message.content, reply_to: message.reply_to };
  const result = { version: request.version, message, replayed: false };
  expect(parseThreadPeerMessageResult(result, "thread-a", request).message.status).toBe("queued");
  expect(parseThreadPeerMessageResult({ ...result, message: { ...message, content: "token=[REDACTED]" } },
    "thread-a", request).message.content).toBe("token=[REDACTED]");
  for (const patch of [{ source_thread_id: "elsewhere" }, { target_thread_id: "elsewhere" },
    { reply_to: "different" }, { status: "completed" }]) {
    expect(() => parseThreadPeerMessageResult({ ...result, message: { ...message, ...patch } }, "thread-a", request)).toThrow();
  }
});

it("rejects unavailable or out-of-project targets and duplicate identities", () => {
  const target = { id: "thread-b", title: "Frontend", workspace_id: "workspace-1", status: "active" };
  const result = { version: "thread_peer_targets.v1", source_thread_id: "thread-a", workspace_id: "workspace-1", items: [target], has_more: false };
  expect(parseThreadPeerTargets(result, "thread-a").items).toHaveLength(1);
  for (const patch of [{ id: "thread-a" }, { workspace_id: "workspace-2" }, { status: "archived" }]) {
    expect(() => parseThreadPeerTargets({ ...result, items: [{ ...target, ...patch }] }, "thread-a")).toThrow();
  }
  expect(() => parseThreadPeerTargets({ ...result, items: [target, target] }, "thread-a")).toThrow();
  expect(peerMessageBytes("中文")).toBe(6);
});

it("accepts peer transcript provenance while rejecting a peer message represented as operator authority", async () => {
  const item = { version: "thread_transcript.v1", id: "event-1", canonical_id: "event-1", run_id: "run-a",
    run_ordinal: 1, sequence: 1, activity_type: "message", stage: "result", source: "peer", kind: "peer_message",
    title: "Message from another chat", instruction_authorized: false, verifiable: true, durable: true, provisional: false,
    created_at: "2026-10-19T09:00:00Z", peer_message: peerMessageFixture() };
  const fetcher = vi.fn();
  vi.stubGlobal("fetch", fetcher);
  const client = new APIClient("read-token");
  for (const patch of [{}, { instruction_authorized: true }, { source: "operator", kind: "operator_input" }]) {
    fetcher.mockResolvedValueOnce(new Response(JSON.stringify({ version: "api.v1", request_id: "request-1",
      data: [{ ...item, ...patch }], page: { limit: 100 } }), { status: 200 }));
    const result = client.getPage("/threads/thread-b/transcript", {});
    if (Object.keys(patch).length) await expect(result).rejects.toThrow();
    else expect((await result).items).toEqual([item]);
  }
});
