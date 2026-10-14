import { APIClient } from "./client";
import type { RunWebAccessControlRequestView, WebAccessMode } from "./types";

const scope = { workspace_id: "workspace-1", network_mode: "disabled" };
const mode = { protocol_version: "run_mode.v1", policy_version: "mode_policy.v1", revision: 1,
  profile: "code", surface: "code", phase: "deliver", scope, capability_grant: false,
  requested_by: "operator", reason: "initial Run mode", created_at: "2026-10-14T00:00:00Z" };
const request: RunWebAccessControlRequestView = {
  version: "run_web_access_control.v1", expected_mode_revision: 1,
  web_access_mode: "rules", web_allowed_targets: ["docs.example.com"],
};
function receipt(webMode: WebAccessMode = "rules", targets: string[] = ["docs.example.com"]) {
  return { version: "run_web_access_control.v1", run_id: "run-1", replayed: false,
    mode: { ...mode, revision: 2, scope: { ...scope, web_access_mode: webMode, web_allowed_targets: targets } } };
}
function serve(data: unknown) {
  const fetchMock = vi.fn().mockResolvedValue(new Response(JSON.stringify({
    version: "api.v1", request_id: "web-access-response", data,
  }), { status: 202, headers: { "Content-Type": "application/json" } }));
  vi.stubGlobal("fetch", fetchMock);
  return fetchMock;
}
function client() { return new APIClient("read-secret", "/api/v1", "control-secret"); }
afterEach(() => vi.unstubAllGlobals());

it.each(["disabled", "rules", "full"] as const)("sends an explicit %s policy independently of command network scope", async (webMode) => {
  const targets = webMode === "rules" ? ["docs.example.com"] : [];
  const fetchMock = serve(receipt(webMode, targets));
  const result = await client().setRunWebAccess("run-1", { ...request, web_access_mode: webMode,
    web_allowed_targets: webMode === "rules" ? ["HTTPS://Docs.Example.com/", "docs.example.com"] : [] }, "web-access-operation-0001");
  expect(result.mode.scope).toEqual({ ...scope, web_access_mode: webMode, web_allowed_targets: targets });
  const [url, init] = fetchMock.mock.calls[0] as [string, RequestInit];
  expect(url).toBe("/api/v1/runs/run-1/web-access");
  expect(init.headers).toMatchObject({ Authorization: "Bearer control-secret", "Idempotency-Key": "web-access-operation-0001" });
  expect(JSON.parse(init.body as string)).toEqual({ ...request, web_access_mode: webMode, web_allowed_targets: targets });
});

it.each([
  { web_access_mode: "cached" }, { web_access_mode: "indexed" },
  { web_access_mode: "rules", web_allowed_targets: ["*"] },
  { web_access_mode: "rules", web_allowed_targets: ["http://example.com"] },
  { web_access_mode: "rules", web_allowed_targets: ["127.0.0.1"] },
  { web_access_mode: "full", web_allowed_targets: ["example.com"] },
  { expected_mode_revision: 0 },
])("rejects unsupported or malformed policies before POST: %j", async (change) => {
  const fetchMock = serve(receipt());
  await expect(client().setRunWebAccess("run-1", { ...request, ...change } as RunWebAccessControlRequestView,
    "web-access-invalid")).rejects.toThrow();
  expect(fetchMock).not.toHaveBeenCalled();
});

it("requires the control credential", async () => {
  const fetchMock = serve(receipt());
  await expect(new APIClient("read-secret", "/api/v1").setRunWebAccess("run-1", request,
    "web-access-read-only")).rejects.toThrow();
  expect(fetchMock).not.toHaveBeenCalled();
});

it.each([
  { web_access_mode: "full", web_allowed_targets: [] },
  { web_access_mode: "rules", web_allowed_targets: ["docs.example.com", "other.example.com"] },
  { web_access_mode: "rules", web_allowed_targets: ["DOCS.example.com"] },
  { web_access_mode: "rules", web_allowed_targets: ["docs.example.com", "docs.example.com"] },
])("rejects a server policy that differs from the requested rule: %j", async (returned) => {
  const data = receipt();
  serve({ ...data, mode: { ...data.mode, scope: { ...scope, ...returned } } });
  await expect(client().setRunWebAccess("run-1", request, "web-access-mismatch")).rejects.toThrow();
});

it.each(["run", "revision", "protocol", "unknown-field"])("rejects a mismatched %s receipt", async (field) => {
  const data = receipt();
  serve(field === "run" ? { ...data, run_id: "another-run" }
    : field === "revision" ? { ...data, mode: { ...data.mode, revision: 1 } }
      : field === "protocol" ? { ...data, version: "invalid" } : { ...data, extra: true });
  await expect(client().setRunWebAccess("run-1", request, "web-access-bad-receipt")).rejects.toThrow();
});

it.each(["disabled", "rules", "full"] as const)("binds newly created conversations to the requested %s web policy", async (webMode) => {
  const webScope = { ...scope, web_access_mode: webMode, web_allowed_targets: [] };
  const creation = {
    mission: { id: "mission-1", goal: "Read project", workspace_id: "workspace-1", profile: "code", scope: webScope },
    run: { id: "run-1", mission_id: "mission-1", session_id: "session-1", status: "created",
      config: { interactive: true, model_route: "code" }, budget: { max_turns: 100, max_tool_calls: 100 } },
    session: { id: "session-1", workspace_id: "workspace-1", title: "Read project", route: "code", status: "active" },
    mode: { ...mode, scope: webScope }, replayed: false,
    thread: { id: "thread-1", protocol_version: "thread.v1", workspace_id: "workspace-1", mission_id: "mission-1",
      title: "Read project", status: "active", active_run_id: "run-1", last_run_id: "run-1", version: 1,
      composer_state: "ready", created_at: "2026-10-14T00:00:00Z", updated_at: "2026-10-14T00:00:00Z" },
  };
  const body = { version: "thread_creation.v1" as const, goal: "Read project", workspace_id: "workspace-1",
    network_mode: "disabled" as const, web_access_mode: webMode, web_allowed_targets: [] };
  serve(creation);
  expect((await client().createThread(body, "web-create-operation-0001")).mode.scope).toEqual(webScope);
  serve({ ...creation, mode: { ...creation.mode, scope: { ...scope, web_access_mode: webMode === "full" ? "disabled" : "full" } } });
  await expect(client().createThread(body, "web-create-operation-0002")).rejects.toThrow("authority contract");
});
