import { APIClient } from "./client";

const version = "thread_application_services.v1";
const created = "2026-10-06T00:00:00Z";
const service = { thread_id: "thread-1", run_id: "run-1", job_id: "job-1", state: "running",
  created_at: created, can_stop: true, source_call_id: "call-1", source_message_id: "message-1", source_turn: 2 };
const text = "Listening on http://127.0.0.1:3000/\n";
const output = { stdout: text, stderr: "", base_cursor: 0, next_cursor: text.length, end_cursor: text.length,
  dropped: false, available: true };
const candidate = { url: "http://127.0.0.1:3000/", source: "command_output", verified: false };
const detail = () => ({ version, service: { ...service }, output: structuredClone(output), candidate_urls: [{ ...candidate }] });
const client = () => new APIClient("read-token", "/api/v1", "control-token");
const respond = (data: unknown, status = 200) => vi.stubGlobal("fetch", vi.fn().mockImplementation(async () =>
  new Response(JSON.stringify({ version: "api.v1", request_id: "request-1", ...(status === 200 ? { data } : { error: data }) }),
    { status, headers: { "Content-Type": "application/json" } })));
afterEach(() => vi.unstubAllGlobals());

it("reads bounded service metadata with read-only credentials and retains exact request provenance", async () => {
  respond({ version, thread_id: "thread-1", services: [service], has_more: true });
  const readOnly = new APIClient("read-token");
  const result = await readOnly.listThreadApplicationServices("thread-1");
  expect(result.services[0]).toEqual(service);
  expect(result.has_more).toBe(true);
  expect(fetch).toHaveBeenCalledWith(expect.stringContaining("/threads/thread-1/application-services?limit=20"),
    expect.objectContaining({ headers: expect.objectContaining({ Authorization: "Bearer read-token" }) }));
});

it("retains explicit shared-service mode and unconfirmed terminal cleanup", async () => {
  const row = { ...service, workspace_mode: "shared_service", state: "cancelled", can_stop: false, tree_reaped: false };
  respond({ version, thread_id: "thread-1", services: [row], has_more: false });
  expect((await client().listThreadApplicationServices("thread-1")).services[0]).toEqual(row);
});

it("returns separately bounded original output and multiple unverified addresses", async () => {
  const response = detail();
  response.candidate_urls.push({ ...candidate, url: "http://[::1]:3001/alternate" });
  respond(response);
  const controller = new AbortController();
  expect(await client().getThreadApplicationService("thread-1", "job-1", controller.signal)).toEqual(response);
  expect(fetch).toHaveBeenCalledWith(expect.stringContaining("/threads/thread-1/application-services/job-1"),
    expect.objectContaining({ signal: controller.signal }));
});

it.each([
  { ...service, thread_id: "thread-other" },
  { ...service, state: "ready" },
  { ...service, state: "failed", can_stop: true },
  { ...service, source_call_id: undefined },
  { ...service, pid: 123 },
  { ...service, workspace_mode: "service" },
  { ...service, workspace_mode: true },
  { ...service, tree_reaped: "false" },
])("rejects unbound or authority-bearing service metadata: %j", async (row) => {
  respond({ version, thread_id: "thread-1", services: [row], has_more: false });
  await expect(client().listThreadApplicationServices("thread-1")).rejects.toMatchObject({ code: "INVALID_RESPONSE" });
});

it("rejects duplicate Job identities instead of letting selection point at two rows", async () => {
  respond({ version, thread_id: "thread-1", services: [service, { ...service, run_id: "run-other" }], has_more: false });
  await expect(client().listThreadApplicationServices("thread-1")).rejects.toMatchObject({ code: "INVALID_RESPONSE" });
});

it.each(["thread_id", "job_id"])("rejects a detail response from another %s", async (field) => {
  respond({ ...detail(), service: { ...service, [field]: "other-identity" } });
  await expect(client().getThreadApplicationService("thread-1", "job-1")).rejects.toMatchObject({ code: "INVALID_RESPONSE" });
});

it.each(["https://example.com/", "http://127.0.0.1:3000/?token=secret", "http://user:password@127.0.0.1/",
  "http://127.0.0.1/#secret", "http://2130706433:3000/", "javascript:alert(1)"])("rejects a noncanonical or credential-bearing output address: %s", async (url) => {
  respond({ ...detail(), candidate_urls: [{ ...candidate, url }] });
  await expect(client().getThreadApplicationService("thread-1", "job-1")).rejects.toMatchObject({ code: "INVALID_RESPONSE" });
});

it("does not trust an output URL as verified application readiness", async () => {
  respond({ ...detail(), candidate_urls: [{ ...candidate, verified: true }] });
  await expect(client().getThreadApplicationService("thread-1", "job-1")).rejects.toMatchObject({ code: "INVALID_RESPONSE" });
});

it("rejects malformed output cursors and contradictory unavailable output", async () => {
  for (const invalidOutput of [{ ...output, next_cursor: -1 }, { ...output, end_cursor: 0 },
    { ...output, available: false }, { ...output, base_cursor: text.length + 1 }]) {
    respond({ ...detail(), output: invalidOutput });
    await expect(client().getThreadApplicationService("thread-1", "job-1")).rejects.toMatchObject({ code: "INVALID_RESPONSE" });
  }
});

it("counts UTF-8 bytes when bounding retained command output", async () => {
  const text = "文".repeat(22_000);
  respond({ ...detail(), output: { ...output, next_cursor: 66_000, end_cursor: 66_000, stdout: text } });
  await expect(client().getThreadApplicationService("thread-1", "job-1")).rejects.toMatchObject({ code: "INVALID_RESPONSE" });
});

it("uses exact Thread/Run/Job cleanup with a stable retry key and only the control credential", async () => {
  const stopped = { ...service, state: "cancelled", can_stop: false };
  respond({ version, service: stopped, replayed: false });
  await client().stopThreadApplicationService("thread-1", "run-1", "job-1");
  await client().stopThreadApplicationService("thread-1", "run-1", "job-1");
  for (const [url, request] of vi.mocked(fetch).mock.calls) {
    expect(String(url)).toContain("/threads/thread-1/application-services/job-1/stop");
    expect(request).toMatchObject({ method: "POST", headers: {
      Authorization: "Bearer control-token", "Idempotency-Key": "application-stop-job-1",
    } });
    expect(JSON.parse(String(request?.body))).toEqual({ version, expected_run_id: "run-1" });
  }
  expect(fetch).toHaveBeenCalledTimes(2);
});

it("rejects a stop receipt for a different Run and does not repeat the mutation", async () => {
  respond({ version, service: { ...service, state: "cancelled", can_stop: false, run_id: "run-other" }, replayed: false });
  await expect(client().stopThreadApplicationService("thread-1", "run-1", "job-1")).rejects.toMatchObject({ code: "INVALID_RESPONSE" });
  expect(fetch).toHaveBeenCalledTimes(1);
});

it("preserves an uncertain stop failure without automatically resending", async () => {
  respond({ code: "FAILED_PRECONDITION", message: "Job owner is unavailable" }, 412);
  await expect(client().stopThreadApplicationService("thread-1", "run-1", "job-1")).rejects.toMatchObject({ code: "FAILED_PRECONDITION" });
  expect(fetch).toHaveBeenCalledTimes(1);
});

it("requires control capability before issuing a service stop", async () => {
  respond(null);
  await expect(new APIClient("read-token").stopThreadApplicationService("thread-1", "run-1", "job-1")).rejects.toThrow("control access");
  await expect(new APIClient("read-token", "/api/v1", "control-token", { runExecutionEnabled: false })
    .stopThreadApplicationService("thread-1", "run-1", "job-1")).rejects.toThrow("control access");
  expect(fetch).not.toHaveBeenCalled();
});
