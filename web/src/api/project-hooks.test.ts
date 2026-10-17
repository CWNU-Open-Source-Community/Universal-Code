import { APIClient } from "./client";
import { parseProjectHooks, readProjectHooks, reviewProjectHooks } from "./project-hooks";

const view = { version: "project-hooks.v1", thread_id: "thread-1", workspace_id: "workspace-1", fingerprint: "a".repeat(64), trusted: false,
  revision: 0, matches_task: true, state: "needs_review", message: "请审阅当前配置。",
  hooks: [{ event: "after_edit", command_id: "format.go", target: "已应用的 .go 文件" }, { event: "before_finish", command_id: "test.npm", target: "." }] };
const response = (data: unknown) => new Response(JSON.stringify({ version: "api.v1", request_id: "req-hooks", data }), { headers: { "Content-Type": "application/json" } });

afterEach(() => vi.unstubAllGlobals());

it("reads with the read credential and reviews only the displayed fingerprint and revision using control", async () => {
  const trusted = { ...view, state: "trusted", trusted: true, revision: 1 };
  const fetch = vi.fn().mockResolvedValueOnce(response(view)).mockResolvedValueOnce(response(trusted))
    .mockResolvedValueOnce(response({ ...view, state: "disabled", revision: 2 }));
  vi.stubGlobal("fetch", fetch);
  const client = new APIClient("read", "/api/v1", "control");
  const current = await readProjectHooks(client, "thread-1");
  const saved = await reviewProjectHooks(client, current, "trust");
  await reviewProjectHooks(client, saved, "disable");
  expect(fetch.mock.calls[0][0]).toBe("/api/v1/threads/thread-1/project-hooks");
  expect(fetch.mock.calls[0][1].headers.Authorization).toBe("Bearer read");
  expect(fetch.mock.calls[1][0]).toBe("/api/v1/threads/thread-1/project-hooks/trust");
  expect(fetch.mock.calls[1][1].headers.Authorization).toBe("Bearer control");
  expect(JSON.parse(fetch.mock.calls[1][1].body)).toEqual({ version: "project-hooks.v1", expected_fingerprint: view.fingerprint, expected_revision: 0 });
  expect(JSON.parse(fetch.mock.calls[2][1].body)).toEqual({ version: "project-hooks.v1", expected_fingerprint: view.fingerprint, expected_revision: 1 });
});

it("rejects foreign task identity and commands outside the registered hook actions", () => {
  expect(() => parseProjectHooks(view, "other-task")).toThrow();
  for (const invalid of [
    { ...view, fingerprint: "" }, { ...view, state: "trusted" }, { ...view, revision: -1 },
    { ...view, hooks: [{ event: "after_edit", command_id: "shell", target: "echo hello" }] },
    { ...view, hooks: [{ event: "before_finish", command_id: "format.go", target: "." }] },
    { ...view, hooks: [view.hooks[0], view.hooks[0]] },
  ]) expect(() => parseProjectHooks(invalid, "thread-1")).toThrow();
  expect(parseProjectHooks({ ...view, fingerprint: "", hooks: [], state: "not_configured" }, "thread-1").hooks).toEqual([]);
});

it("requires control and rejects a changed or stale review response", async () => {
  const fetch = vi.fn();
  vi.stubGlobal("fetch", fetch);
  const current = parseProjectHooks(view, "thread-1");
  await expect(reviewProjectHooks(new APIClient("read"), current, "trust")).rejects.toThrow("控制权限");
  expect(fetch).not.toHaveBeenCalled();
  const client = new APIClient("read", "/api/v1", "control");
  for (const invalid of [
    { ...view, fingerprint: "b".repeat(64), trusted: true, state: "trusted", revision: 1 },
    { ...view, workspace_id: "workspace-other", trusted: true, state: "trusted", revision: 1 },
    { ...view, trusted: true, state: "trusted", revision: 0 },
  ]) {
    fetch.mockResolvedValueOnce(response(invalid));
    await expect(reviewProjectHooks(client, current, "trust")).rejects.toMatchObject({ code: "INVALID_RESPONSE" });
  }
});
