import { APIClient } from "./client";

const client = new APIClient("reader", "/api/v1", "operator", {
  projectlessWorkspacesEnabled: true, workspaceImportEnabled: true,
});
const directory = { version: "projectless_directory.v1", directory_path: "D:/Chats", default_directory_path: "C:/Users/test/Documents/Universal-Code" };
const thread = { id: "chat-one", protocol_version: "thread.v1", workspace_id: "project-two", mission_id: "mission-one",
  title: "Saved conversation", status: "active", composer_state: "ready", version: 2,
  active_run_id: "run-one", last_run_id: "run-one", project_workspace_id: "project-two", project_assignment_version: 2,
  created_at: "2026-10-19T00:00:00Z", updated_at: "2026-10-19T00:01:00Z" };
function response(data: unknown) { return new Response(JSON.stringify({ version: "api.v1", request_id: "folders-test", data }), { status: 200 }); }
afterEach(() => vi.unstubAllGlobals());

it("reads and writes directory settings with operator credentials and the selected path", async () => {
  const fetch = vi.fn().mockResolvedValue(response(directory));
  vi.stubGlobal("fetch", fetch);
  expect(await client.getProjectlessDirectory()).toEqual(directory);
  expect(fetch.mock.calls[0][0]).toBe("/api/v1/settings/projectless-directory");
  expect(fetch.mock.calls[0][1]).toMatchObject({ method: "GET", headers: { Authorization: "Bearer operator" }, body: undefined });
  fetch.mockResolvedValueOnce(response(directory));
  await client.setProjectlessDirectory("D:/Chats");
  expect(fetch.mock.calls[1][1]).toMatchObject({ method: "PUT", headers: { Authorization: "Bearer operator" } });
  expect(JSON.parse(fetch.mock.calls[1][1].body)).toEqual({ version: "projectless_directory.v1", directory_path: "D:/Chats", confirmed: true });
});

it("uses the allocation key for retries and never returns a malformed workspace", async () => {
  const fetch = vi.fn().mockResolvedValueOnce(response({ version: "projectless_workspace.v1", replayed: true,
    workspace: { id: "chat-folder", name: "Chat", created_at: "2026-10-19T00:00:00Z" } }))
    .mockResolvedValueOnce(response({ version: "projectless_workspace.v1", replayed: false, workspace: { id: "../unexpected" } }));
  vi.stubGlobal("fetch", fetch);
  expect((await client.createProjectlessWorkspace("persisted-allocation-key")).workspace.id).toBe("chat-folder");
  expect(fetch.mock.calls[0][1].headers["Idempotency-Key"]).toBe("persisted-allocation-key");
  await expect(client.createProjectlessWorkspace("persisted-allocation-key")).rejects.toThrow("invalid");
});

it("binds fresh moves to their target, but accepts a replay observed after a later move", async () => {
  const fetch = vi.fn().mockResolvedValueOnce(response({ version: "thread_project_move.v1", thread, replayed: false }))
    .mockResolvedValueOnce(response({ version: "thread_project_move.v1", thread: { ...thread, project_workspace_id: "", project_assignment_version: 3 }, replayed: true }))
    .mockResolvedValueOnce(response({ version: "thread_project_move.v1", thread: { ...thread, project_workspace_id: "" }, replayed: false }));
  vi.stubGlobal("fetch", fetch);
  expect((await client.moveThreadToProject(thread.id, "project-two", 1, "original-project-move-key")).thread).toEqual(thread);
  expect(JSON.parse(fetch.mock.calls[0][1].body)).toEqual({ version: "thread_project_move.v1", project_workspace_id: "project-two", expected_version: 1 });
  expect((await client.moveThreadToProject(thread.id, "project-two", 1, "original-project-move-key")).thread.project_workspace_id).toBe("");
  await expect(client.moveThreadToProject(thread.id, "project-two", 1, "original-project-move-key")).rejects.toThrow("destination");
});

it("does not make allocation or directory requests without the explicit capability", async () => {
  const fetch = vi.fn(); vi.stubGlobal("fetch", fetch);
  const oldClient = new APIClient("reader", "/api/v1", "operator");
  await expect(oldClient.createProjectlessWorkspace("persisted-allocation-key")).rejects.toThrow("unavailable");
  await expect(oldClient.getProjectlessDirectory()).rejects.toThrow("unavailable");
  expect(fetch).not.toHaveBeenCalled();
});
