import { APIClient } from "./client";

const thread = { id: "thread-metadata", protocol_version: "thread.v1", mission_id: "mission-metadata", title: "Renamed chat", status: "active",
  last_run_id: "run-metadata", active_run_id: "run-metadata", version: 3, composer_state: "ready", pinned: true,
  created_at: "2026-10-20T00:00:00Z", updated_at: "2026-10-20T00:00:00Z" };

afterEach(() => vi.unstubAllGlobals());

it("sets the absolute chat name and pin with the control bearer", async () => {
  const fetch = vi.fn().mockImplementation(async () => new Response(JSON.stringify({ version: "api.v1", request_id: "metadata", data: thread }), { status: 200 }));
  vi.stubGlobal("fetch", fetch);
  const client = new APIClient("read-secret", "/api/v1", "control-secret");
  expect(await client.renameThread(thread.id, " Renamed chat ")).toEqual(thread);
  expect(await client.pinThread(thread.id, true)).toEqual(thread);
  expect(fetch.mock.calls.map(([url]) => url)).toEqual(["/api/v1/threads/thread-metadata/name", "/api/v1/threads/thread-metadata/pin"]);
  expect(fetch.mock.calls[0][1]).toMatchObject({ method: "PUT", headers: { Authorization: "Bearer control-secret" } });
  expect(JSON.parse(fetch.mock.calls[0][1].body)).toEqual({ title: "Renamed chat" });
  expect(JSON.parse(fetch.mock.calls[1][1].body)).toEqual({ pinned: true });
  await expect(client.renameThread(thread.id, "bad\nname")).rejects.toThrow();
  expect(fetch).toHaveBeenCalledTimes(2);
});
