import { APIClient } from "./client";
import { commandServicePolicyVersion, type ThreadCommandServicePolicyRequest } from "./command-service-policy";

const policy = { version: commandServicePolicyVersion, thread_id: "thread-1", policy: "auto", revision: 1 };
const request: ThreadCommandServicePolicyRequest = { version: commandServicePolicyVersion, policy: "exclusive", expected_revision: 1 };
const client = () => new APIClient("read-token", "/api/v1", "control-token", { executionPermissionControlEnabled: true });
const respond = (data: unknown, status = 200) => vi.stubGlobal("fetch", vi.fn().mockResolvedValue(
  new Response(JSON.stringify({ version: "api.v1", request_id: "request-1", ...(status === 200 ? { data } : { error: data }) }),
    { status, headers: { "Content-Type": "application/json" } })));

afterEach(() => vi.unstubAllGlobals());

it("reads the exact Thread policy with the read credential", async () => {
  respond(policy);
  const signal = new AbortController().signal;
  await expect(new APIClient("read-token").getThreadCommandServicePolicy("thread-1", signal)).resolves.toEqual(policy);
  expect(fetch).toHaveBeenCalledExactlyOnceWith(expect.stringContaining("/threads/thread-1/command-service-policy"),
    expect.objectContaining({ signal, headers: expect.objectContaining({ Authorization: "Bearer read-token" }) }));
});

it.each([1, 2])("accepts the server's revision %s for an unchanged save or a successful update", async (revision) => {
  respond({ ...policy, policy: "exclusive", revision });
  expect((await client().setThreadCommandServicePolicy("thread-1", request)).revision).toBe(revision);
  expect(fetch).toHaveBeenCalledExactlyOnceWith(expect.stringContaining("/threads/thread-1/command-service-policy"),
    expect.objectContaining({ method: "PUT", body: JSON.stringify(request),
      headers: expect.objectContaining({ Authorization: "Bearer control-token" }) }));
});

it.each([
  { ...policy, thread_id: "thread-other" }, { ...policy, policy: "enabled" },
  { ...policy, revision: 0 }, { ...policy, revision: 1.5 }, { ...policy, enabled: true },
])("rejects an invalid or unrelated saved preference: %j", async (invalid) => {
  respond(invalid);
  await expect(client().getThreadCommandServicePolicy("thread-1")).rejects.toMatchObject({ code: "INVALID_RESPONSE" });
});

it("does not accept a save receipt for another policy", async () => {
  respond(policy);
  await expect(client().setThreadCommandServicePolicy("thread-1", request)).rejects.toMatchObject({ code: "INVALID_RESPONSE" });
  expect(fetch).toHaveBeenCalledOnce();
});

it("requires control and a current positive revision before saving", async () => {
  respond(policy);
  await expect(new APIClient("read-token").setThreadCommandServicePolicy("thread-1", request)).rejects.toThrow("control access");
  await expect(client().setThreadCommandServicePolicy("thread-1", { ...request, expected_revision: 0 })).rejects.toThrow("current revision");
  expect(fetch).not.toHaveBeenCalled();
});

it("uses execution permission control independently of general Run control", async () => {
  respond({ ...policy, policy: "exclusive", revision: 2 });
  const independent = new APIClient("read-token", "/api/v1", "control-token", {
    runControlEnabled: false, runExecutionEnabled: false, executionPermissionControlEnabled: true,
  });
  expect(independent.hasControl).toBe(false);
  await expect(independent.setThreadCommandServicePolicy("thread-1", request)).resolves.toMatchObject({ policy: "exclusive" });
  expect(fetch).toHaveBeenCalledOnce();
});

it("does not save with only general Run control or a missing permission capability", async () => {
  respond(policy);
  for (const capabilities of [{ runControlEnabled: true, executionPermissionControlEnabled: false }, {}]) {
    const disabled = new APIClient("read-token", "/api/v1", "control-token", capabilities);
    await expect(disabled.setThreadCommandServicePolicy("thread-1", request)).rejects.toThrow("execution permission control");
  }
  expect(fetch).not.toHaveBeenCalled();
});

it("returns revision conflicts without automatically replaying the write", async () => {
  respond({ code: "CONFLICT", message: "Policy revision changed" }, 409);
  await expect(client().setThreadCommandServicePolicy("thread-1", request)).rejects.toMatchObject({ code: "CONFLICT", status: 409 });
  expect(fetch).toHaveBeenCalledOnce();
});
