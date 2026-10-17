import type { APIClient } from "./client";
import { createPRFollowup, PR_FOLLOWUP_VERSION, readPRFollowups, validPRFollowupCreateRequest, type PRFollowupCreateRequest } from "./pr-followup";

const body: PRFollowupCreateRequest = { version: PR_FOLLOWUP_VERSION, connection_id: "connection-1", pull_request: 12, interval_seconds: 300,
  deadline_at: "2026-10-18T00:00:00Z", max_turns: 3, include_existing_comments: false };

it("rejects a list returned for another thread", async () => {
  const client = { get: vi.fn().mockResolvedValue({ version: PR_FOLLOWUP_VERSION, thread_id: "other", worker_state: "running", items: [] }) };
  await expect(readPRFollowups(client as unknown as APIClient, "thread-1")).rejects.toThrow(/本任务/);
  expect(client.get).toHaveBeenCalledWith("/threads/thread-1/pr-followups", {}, undefined);
});

it("does not accept an unbound create reply as confirmation", async () => {
  const client = { postControl: vi.fn().mockResolvedValue({ id: "other", thread_id: "other" }) };
  await expect(createPRFollowup(client as unknown as APIClient, "thread-1", body, "original-key")).rejects.toThrow(/当前任务/);
  expect(client.postControl).toHaveBeenCalledWith("/threads/thread-1/pr-followups", body, "original-key");
});

it("validates recovered settings without expiring the original replay body", () => {
  expect(validPRFollowupCreateRequest(body)).toBe(true);
  expect(validPRFollowupCreateRequest({ ...body, interval_seconds: 1 })).toBe(false);
  expect(validPRFollowupCreateRequest({ ...body, max_turns: 21 })).toBe(false);
  expect(validPRFollowupCreateRequest({ ...body, extra: "unverified" })).toBe(false);
});
