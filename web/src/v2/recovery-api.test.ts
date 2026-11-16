import { APIClient } from "../api/client";
import { inspectV2CreationRequest, inspectV2SteeringRequest, inspectV2TurnRequest } from "./recovery-api";

const client = () => new APIClient("read-token", "/api/v1", "control-token");
const key = "original-operation-key";
const completed = { kind: "turn", state: "completed", settled: true, workspace_id: "workspace-1",
  thread_id: "thread-1", run_id: "run-original", session_id: "session-original", message_id: "message-1", message_status: "committed", request_fingerprint: "a".repeat(64) };
function respond(data: unknown, status = 200) {
  const fetcher = vi.fn().mockResolvedValue(new Response(JSON.stringify({ version: "api.v1", request_id: "observe-request", data }), { status, headers: { "Content-Type": "application/json" } }));
  vi.stubGlobal("fetch", fetcher);
  return fetcher;
}

it("uses only read GET and the original header key on a fresh client", async () => {
  const fetcher = respond(completed);
  await expect(inspectV2TurnRequest(client(), { threadID: "thread-1", operationKey: key })).resolves.toEqual(completed);
  const [url, options] = fetcher.mock.calls[0];
  expect(url).toBe("/api/v1/threads/thread-1/turn-request");
  expect(options).toMatchObject({ method: "GET", cache: "no-store", credentials: "omit", headers: { Authorization: "Bearer read-token", "Idempotency-Key": key } });
  expect(options.body).toBeUndefined();
  expect(JSON.stringify(options)).not.toContain("control-token");
  expect(fetcher).toHaveBeenCalledTimes(1);
});

it("creation lookup finds the original Thread without sending the saved creation body", async () => {
  const value = { kind: "creation", state: "completed", settled: true, workspace_id: "workspace-1", thread_id: "thread-original", run_id: "run-original", session_id: "session-original", request_fingerprint: "b".repeat(64) };
  const fetcher = respond(value);
  await expect(inspectV2CreationRequest(client(), { workspaceID: "workspace-1", operationKey: key })).resolves.toEqual(value);
  expect(fetcher.mock.calls[0][0]).toBe("/api/v1/threads/creation-request?workspace_id=workspace-1");
  expect(fetcher.mock.calls[0][1].method).toBe("GET");
});

it.each(["not_received", "received"])("does not POST or declare %s settled", async (state) => {
  const fetcher = respond({ kind: "turn", state, settled: false, workspace_id: "workspace-1", thread_id: "thread-1" });
  await expect(inspectV2TurnRequest(client(), { threadID: "thread-1", operationKey: key })).resolves.toMatchObject({ state, settled: false });
  expect(fetcher).toHaveBeenCalledTimes(1);
  expect(fetcher.mock.calls[0][1].method).toBe("GET");
});

it.each(["empty_model_response","full_permission_not_ready"])("returns the exact sealed %s failure without calling the retry endpoint", async (stage) => {
  const value = { ...completed, state: "failed", error_code: "turn_failed", failure_stage: stage,
    turn_failure: { thread_id: "thread-1", run_id: "run-original", message_id: "message-1", event_sequence: 42 } };
  const fetcher = respond(value);
  await expect(inspectV2TurnRequest(client(), { threadID: "thread-1", operationKey: key })).resolves.toEqual(value);
  expect(fetcher).toHaveBeenCalledTimes(1);
});

it.each([{ ...completed, thread_id: "thread-other" }, { ...completed, state: "not_received" },
  { ...completed, message_status: "pending" }, { ...completed, state: "failed" },
  { ...completed, settled: false }, { ...completed, capability_grant: true },
  { ...completed, state: "failed", error_code: "turn_failed", turn_failure: { thread_id: "thread-1", run_id: "wrong-run", message_id: "message-1", event_sequence: 42 } }])(
  "rejects malformed or mismatched observations instead of clearing a pending request", async (value) => {
    respond(value);
    await expect(inspectV2TurnRequest(client(), { threadID: "thread-1", operationKey: key })).rejects.toMatchObject({ code: "INVALID_RESPONSE" });
  });

it("rejects an invalid key before any network request", async () => {
  const fetcher = respond(completed);
  await expect(inspectV2TurnRequest(client(), { threadID: "thread-1", operationKey: "short" })).rejects.toThrow();
  expect(fetcher).not.toHaveBeenCalled();
});

it("confirms current-task steering only for an exact read-only steer receipt", async () => {
  const steeringClient = new APIClient("read-token", "/api/v1", "control-token", { sessionMessageEnabled: true });
  const receipt = { version: "session_message_submission.v1", session_id: "session-original",
    state: "received", message_id: "steer-original", message_status: "pending", delivery_mode: "steer" };
  const fetcher = respond(receipt);
  await expect(inspectV2SteeringRequest(steeringClient,
    { sessionID: "session-original", operationKey: key })).resolves.toMatchObject(receipt);
  expect(fetcher.mock.calls[0][0]).toBe(`/api/v1/sessions/session-original/messages/operations/${key}`);
  expect(fetcher.mock.calls[0][1]).toMatchObject({ method: "GET", headers: { Authorization: "Bearer read-token" } });
  expect(fetcher.mock.calls[0][1].body).toBeUndefined();
  respond({ ...receipt, delivery_mode: "next_turn" });
  await expect(inspectV2SteeringRequest(steeringClient,
    { sessionID: "session-original", operationKey: key })).rejects.toMatchObject({ code: "INVALID_RESPONSE" });
});
