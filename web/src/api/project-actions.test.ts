import { APIClient } from "./client";
import { parseProjectActions, parseProjectActionResult } from "./project-actions";

const actions = {
  protocol_version: "project-actions.v1", run_id: "run-1", target: ".", backend: "docker", capability_grant: false,
  actions: [
    { slot: "test", command_id: "test.npm", configured: true, available: true, target_kind: "directory", reason: "" },
    { slot: "format", command_id: "format.go", configured: true, available: false, target_kind: "go_file", reason: "Read-only project" },
  ],
};
const result = {
  protocol_version: "project-actions.v1", run_id: "run-1", slot: "test", command_id: "test.npm",
  backend: "docker", replayed: false,
  job: { id: "job-1", state: "running", can_stop: true, stdout: "", stderr: "", stdout_truncated: false, stderr_truncated: false },
};
function response(data: unknown) {
  return new Response(JSON.stringify({ version: "api.v1", request_id: "req-actions", data }),
    { headers: { "Content-Type": "application/json" } });
}
afterEach(() => vi.unstubAllGlobals());

it("reads actions and Jobs with the read credential, and starts an exact preset using control and idempotency", async () => {
  const fetch = vi.fn().mockResolvedValueOnce(response(actions)).mockResolvedValueOnce(response(result)).mockResolvedValueOnce(response(result));
  vi.stubGlobal("fetch", fetch);
  const client = new APIClient("read", "/api/v1", "control");
  await client.listProjectActions("run-1");
  await client.runProjectAction("run-1", "test", "web", "project-action-fixed");
  await client.getProjectActionJob("run-1", "job-1");
  expect(fetch.mock.calls[0][1].headers.Authorization).toBe("Bearer read");
  expect(fetch.mock.calls[0][0]).toBe("/api/v1/runs/run-1/project-actions");
  expect(fetch.mock.calls[1][0]).toBe("/api/v1/runs/run-1/project-actions/test");
  expect(fetch.mock.calls[1][1]).toMatchObject({ method: "POST", headers: {
    Authorization: "Bearer control", "Idempotency-Key": "project-action-fixed",
  } });
  expect(JSON.parse(fetch.mock.calls[1][1].body)).toEqual({ version: "project-actions.v1", target: "web", confirm_execution: true });
  expect(fetch.mock.calls[2][0]).toBe("/api/v1/runs/run-1/project-actions/jobs/job-1");
  expect(fetch.mock.calls[2][1].headers.Authorization).toBe("Bearer read");
});

it("rejects execution without control and binds responses to the requested Run and Job", async () => {
  const fetch = vi.fn();
  vi.stubGlobal("fetch", fetch);
  await expect(new APIClient("read").runProjectAction("run-1", "test", "", "project-action-fixed")).rejects.toThrow("execution control");
  expect(fetch).not.toHaveBeenCalled();
  expect(() => parseProjectActions(actions, "other-run")).toThrow();
  expect(() => parseProjectActions(actions, "run-1", "web")).toThrow();
  expect(parseProjectActions({ ...actions, target: undefined }, "run-1").target).toBe(".");
  expect(() => parseProjectActions({ ...actions, target: undefined }, "run-1", "web")).toThrow();
  expect(() => parseProjectActionResult(result, "run-1", "format")).toThrow();
  expect(() => parseProjectActionResult(result, "run-1", undefined, "other-job")).toThrow();
  expect(parseProjectActionResult({ ...result, job: { ...result.job, state: "failed", can_stop: false, exit_code: 1, stderr: "test failed" } }, "run-1").job.exit_code).toBe(1);
});

it("normalizes a requested test directory and rejects an observation from another directory", async () => {
  const fetch = vi.fn().mockResolvedValueOnce(response({ ...actions, target: "web" })).mockResolvedValueOnce(response(actions));
  vi.stubGlobal("fetch", fetch);
  const client = new APIClient("read");
  await client.listProjectActions("run-1", "./web/");
  expect(fetch.mock.calls[0][0]).toBe("/api/v1/runs/run-1/project-actions?target=web");
  await expect(client.listProjectActions("run-1", "web")).rejects.toMatchObject({ code: "INVALID_RESPONSE" });
  await expect(client.listProjectActions("run-1", "../other")).rejects.toThrow();
  expect(fetch).toHaveBeenCalledTimes(2);
});

it("keeps the legacy root request and signal compatible with a root response without target", async () => {
  const fetch = vi.fn().mockResolvedValue(response({ ...actions, target: undefined }));
  vi.stubGlobal("fetch", fetch);
  const signal = new AbortController().signal;
  expect((await new APIClient("read").listProjectActions("run-1", signal)).target).toBe(".");
  expect(fetch.mock.calls[0][0]).toBe("/api/v1/runs/run-1/project-actions");
  expect(fetch.mock.calls[0][1].signal).toBe(signal);
});

it("accepts a detected runnable test with no pinned action while requiring its command identity", () => {
  const detected = { ...actions, actions: actions.actions.map((action) => action.slot === "test" ? { ...action, configured: false } : action) };
  expect(parseProjectActions(detected, "run-1").actions[0]).toMatchObject({ configured: false, available: true, command_id: "test.npm" });
  expect(() => parseProjectActions({ ...detected, actions: detected.actions.map((action) => ({ ...action, command_id: "" })) }, "run-1")).toThrow();
});
