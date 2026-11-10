import { APIClient } from "./client";
import { parseBrowserNetwork, readBrowserNetwork, saveBrowserNetwork, type BrowserNetworkRequest } from "./browser-network";

const target = { kind: "runs", id: "run-1" } as const;
const preference = { workspace_id: "workspace-1", revision: 2, browser_network_access: "internet", browser_origins: [] };
const view = { version: "browser_network_access.v1", run_id: "run-1", ...preference, project_preference: preference };
const request: BrowserNetworkRequest = { version: "browser_network_access.v1", expected_revision: 1,
  browser_network_access: "internet", browser_origins: [], remember_for_project: true };

function serve(data: unknown) {
  const fetchMock = vi.fn().mockResolvedValue(new Response(JSON.stringify({ version: "api.v1", request_id: "browser-network", data }),
    { headers: { "Content-Type": "application/json" } }));
  vi.stubGlobal("fetch", fetchMock); return fetchMock;
}
afterEach(() => vi.unstubAllGlobals());

it("reads the browser policy with the read credential", async () => {
  const fetchMock = serve(view);
  expect(await readBrowserNetwork(new APIClient("read", "/api/v1", "control"), target)).toEqual(view);
  expect(fetchMock.mock.calls[0]).toEqual(["/api/v1/runs/run-1/browser-network", expect.objectContaining({
    method: "GET", headers: expect.objectContaining({ Authorization: "Bearer read" }),
  })]);
});

it.each(["runs", "workspaces"] as const)("updates %s with PUT, revision and an idempotency key", async (kind) => {
  const destination = { kind, id: kind === "runs" ? "run-1" : "workspace-1" };
  const fetchMock = serve({ ...view, replayed: false });
  const body = { ...request, ...(kind === "workspaces" ? { remember_for_project: undefined } : {}) };
  await saveBrowserNetwork(new APIClient("read", "/api/v1", "control"), destination, body, "browser-network-operation");
  const [url, init] = fetchMock.mock.calls[0] as [string, RequestInit];
  expect(url).toBe(`/${"api/v1"}/${kind}/${destination.id}/browser-network`);
  expect(init.method).toBe("PUT");
  expect(init.headers).toMatchObject({ Authorization: "Bearer control", "Idempotency-Key": "browser-network-operation" });
  expect(JSON.parse(String(init.body))).toEqual(JSON.parse(JSON.stringify(body)));
});

it("rejects writes without a control credential before sending a request", async () => {
  const fetchMock = serve(view);
  await expect(saveBrowserNetwork(new APIClient("read"), target, request, "browser-network-readonly")).rejects.toThrow("control bearer");
  expect(fetchMock).not.toHaveBeenCalled();
});

it.each([{ run_id: "other" }, { browser_network_access: "full" }, { revision: -1 },
  { project_preference: { ...preference, workspace_id: "other" } }])("rejects an unrelated or malformed policy %j", (change) => {
  expect(() => parseBrowserNetwork({ ...view, ...change }, target)).toThrow("response is invalid");
});

it("rejects an update receipt that did not apply the requested mode", async () => {
  serve({ ...view, browser_network_access: "off", replayed: false });
  await expect(saveBrowserNetwork(new APIClient("read", "/api/v1", "control"), target, request,
    "browser-network-mismatch")).rejects.toThrow("update response is invalid");
});
