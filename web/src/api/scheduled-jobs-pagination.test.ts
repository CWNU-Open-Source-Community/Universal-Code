import { APIClient } from "./client";

afterEach(() => vi.unstubAllGlobals());

it("keeps the existing signal argument and forwards an opaque scheduled-job cursor", async () => {
  const fetchMock = vi.fn().mockImplementation(async () => new Response(JSON.stringify({
    version: "api.v1", request_id: "scheduled-page-request",
    data: { protocol_version: "scheduled-job.v1", items: [] },
    page: { limit: 100, next_cursor: "opaque-next-page" },
  }), { status: 200, headers: { "Content-Type": "application/json" } }));
  vi.stubGlobal("fetch", fetchMock);
  const client = new APIClient("read-secret"), abort = new AbortController();
  const first = await client.listScheduledJobs("run-selected", 100, abort.signal);
  expect(first).toMatchObject({ items: [], page: { limit: 100, next_cursor: "opaque-next-page" } });
  await client.listScheduledJobs("run-selected", 100, abort.signal, first.page.next_cursor);
  const firstURL = new URL(fetchMock.mock.calls[0]![0] as string, "http://localhost");
  const nextURL = new URL(fetchMock.mock.calls[1]![0] as string, "http://localhost");
  expect(firstURL.searchParams.has("cursor")).toBe(false);
  expect(nextURL.searchParams.get("run_id")).toBe("run-selected");
  expect(nextURL.searchParams.get("limit")).toBe("100");
  expect(nextURL.searchParams.get("cursor")).toBe("opaque-next-page");
  expect(fetchMock.mock.calls[1]![1]).toMatchObject({ signal: abort.signal });
});

it.each([undefined, { limit: 99 }, { limit: 100, next_cursor: [] },
  { limit: 100, next_cursor: "opaque-next", truncated: true }])("rejects unusable scheduled-job page metadata: %j", async (page) => {
  vi.stubGlobal("fetch", vi.fn().mockResolvedValue(new Response(JSON.stringify({
    version: "api.v1", request_id: "invalid-scheduled-page",
    data: { protocol_version: "scheduled-job.v1", items: [] }, ...(page ? { page } : {}),
  }), { status: 200, headers: { "Content-Type": "application/json" } })));
  await expect(new APIClient("read-secret").listScheduledJobs("", 100)).rejects.toMatchObject({ code: "INVALID_RESPONSE" });
});
