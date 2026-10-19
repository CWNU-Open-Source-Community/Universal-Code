import { afterEach, expect, it, vi } from "vitest";
import { APIClient } from "./client";

afterEach(() => vi.unstubAllGlobals());

const receipt = { version: "thread_change_undo.v1", thread_id: "thread-1", run_id: "run-1", source_event_id: "reply-1",
  state: "available", conflicts: [], restored_paths: [], remaining_paths: ["README.md"], replayed: false };
function response(data: unknown) {
  return new Response(JSON.stringify({ version: "api.v1", request_id: "test-undo", data }),
    { status: 200, headers: { "Content-Type": "application/json" } });
}

it("uses control authorization and a stable reply key for preview, apply and retry", async () => {
  const fetch = vi.fn().mockImplementation(async () => response(receipt));
  vi.stubGlobal("fetch", fetch);
  const client = new APIClient("read-test", "/api/v1", "control-test", { workspaceCheckpointControlEnabled: true });
  await client.undoThreadChanges("thread-1", "run-1", "reply-1", false);
  await client.undoThreadChanges("thread-1", "run-1", "reply-1", true);
  await client.undoThreadChanges("thread-1", "run-1", "reply-1", true);
  for (const [url, init] of fetch.mock.calls) {
    expect(url).toBe("/api/v1/threads/thread-1/changes/undo");
    expect(init.method).toBe("POST");
    expect(init.headers.Authorization).toBe("Bearer control-test");
    expect(init.headers["Idempotency-Key"]).toBe("thread-change-undo-reply-1");
    expect(JSON.parse(init.body)).toMatchObject({ version: "thread_change_undo.v1", source_event_id: "reply-1" });
  }
  expect(fetch.mock.calls.map(([, init]) => JSON.parse(init.body).confirm)).toEqual([false, true, true]);
});

it("rejects another reply's undo receipt", async () => {
  const fetch = vi.fn().mockResolvedValue(response({ ...receipt, source_event_id: "other-reply", state: "completed" }));
  vi.stubGlobal("fetch", fetch);
  const client = new APIClient("read-test", "/api/v1", "control-test", { workspaceCheckpointControlEnabled: true });
  await expect(client.undoThreadChanges("thread-1", "run-1", "reply-1", true)).rejects.toThrow("does not match this reply");
});

it("does not send undo requests without workspace restore authority", async () => {
  const fetch = vi.fn();
  vi.stubGlobal("fetch", fetch);
  const client = new APIClient("read-test", "/api/v1", "control-test", { workspaceCheckpointControlEnabled: false });
  await expect(client.undoThreadChanges("thread-1", "run-1", "reply-1", true)).rejects.toThrow("restore authority");
  expect(fetch).not.toHaveBeenCalled();
});
