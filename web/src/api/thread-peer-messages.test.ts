import { afterEach, expect, it, vi } from "vitest";
import { APIClient } from "./client";
import { peerMessageFixture } from "../test/thread-peer-message";

afterEach(() => vi.unstubAllGlobals());

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
