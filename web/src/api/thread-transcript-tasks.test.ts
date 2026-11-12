import { afterEach, expect, it, vi } from "vitest";
import { APIClient } from "./client";
import type { ThreadTranscriptItemView } from "./types";
import { mergeThreadTranscriptItems } from "../components/thread-transcript";

afterEach(() => vi.unstubAllGlobals());

const item = {
  version: "thread_transcript.v1", id: "evt-final", canonical_id: "evt-final",
  run_id: "run-task", run_ordinal: 1, sequence: 400, activity_type: "message",
  stage: "result", kind: "model_update", source: "model", title: "Universal-Code",
  detail: "Finished", verifiable: false, instruction_authorized: false,
  provisional: false, durable: true, created_at: "2026-11-12T05:38:52.1174978Z",
};
const task = {
  id: "steer-input", input_message_id: "steer-input", state: "completed",
  started_at: "2026-11-12T05:37:54.2191794Z", finished_at: "2026-11-12T05:38:52.1174978Z",
  final_message_id: "evt-final", change_source_event_id: "evt-final",
};

function readTranscript(taskValue?: unknown) {
  const value = taskValue === undefined ? item : { ...item, task: taskValue };
  vi.stubGlobal("fetch", vi.fn().mockResolvedValue(new Response(JSON.stringify({
    version: "api.v1", request_id: "request-task", data: [value], page: { limit: 100 },
  }), { status: 200, headers: { "Content-Type": "application/json" } })));
  return new APIClient("read-token").getPage<ThreadTranscriptItemView>("/threads/thread-task/transcript", { limit: 100 });
}

it.each([
  task,
  { id: task.id, input_message_id: task.input_message_id, state: "running" },
  { id: task.id, input_message_id: task.input_message_id, state: "interrupted", started_at: task.started_at,
    finished_at: task.finished_at, change_source_event_id: "evt-interrupted-handoff" },
  { id: task.id, input_message_id: task.input_message_id, state: "failed", change_source_event_id: "evt-failed-handoff" },
])("preserves a $state task from the HTTP response into the durable transcript projection", async (metadata) => {
  const result = await readTranscript(metadata);
  expect(result.items[0].task).toEqual(metadata);
  const projected = mergeThreadTranscriptItems(result.items, [], null, "stopped");
  expect(projected[0].task).toEqual(metadata);
  if (metadata.state === "completed") expect(projected[0].task?.final_message_id).toBe(projected[0].id);
});

it("keeps transcript responses without task metadata compatible", async () => {
  const result = await readTranscript();
  expect(result.items).toEqual([item]);
  expect(mergeThreadTranscriptItems(result.items, [], null, "stopped")[0].task).toBeUndefined();
});

it.each([
  null, [], {},
  { input_message_id: task.input_message_id, state: "running" },
  { id: task.id, state: "running" },
  { id: task.id, input_message_id: task.input_message_id },
  { ...task, id: "" },
  { ...task, id: "x".repeat(257) },
  { ...task, input_message_id: "wrong\0identity" },
  { ...task, input_message_id: "x".repeat(257) },
  { ...task, state: "waiting" },
  { ...task, state: ["running"] },
  { ...task, final_message_id: "" },
  { ...task, final_message_id: "x".repeat(257) },
  { ...task, change_source_event_id: 123 },
  { ...task, change_source_event_id: "x".repeat(257) },
  { ...task, started_at: "invalid-date" },
  { ...task, finished_at: null },
  { ...task, raw_payload: "unexpected" },
])("rejects malformed nested task metadata %#", async (metadata) => {
  await expect(readTranscript(metadata)).rejects.toMatchObject({ code: "INVALID_RESPONSE", status: 502 });
});
