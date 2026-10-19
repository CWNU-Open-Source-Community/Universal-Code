import { APIClient } from "./client";
import { parseProjectPreparation, parseProjectPreparationOperation, validProjectPreparationRequest } from "./project-preparation";
import { preparationOperation, preparationView } from "../test/project-preparation";

function response(data: unknown) {
  return new Response(JSON.stringify({ version: "api.v1", request_id: "req-preparation", data }),
    { headers: { "Content-Type": "application/json" } });
}
const request = { version: "project-preparation.v1" as const, target: ".", expected_fingerprint: "a".repeat(64),
  allowed_sources: ["https://registry.npmjs.org"], confirm_execution: true as const, run_install_scripts: false };
afterEach(() => vi.unstubAllGlobals());

it("uses read credentials for detection and progress, and submits exact lockfile consent with control and replay identity", async () => {
  const operation = preparationOperation();
  const fetch = vi.fn().mockResolvedValueOnce(response(preparationView())).mockResolvedValueOnce(response(preparationView({ latest: operation })))
    .mockResolvedValueOnce(response(operation)).mockResolvedValueOnce(response(preparationOperation({ state: "cancelled" })));
  vi.stubGlobal("fetch", fetch);
  const client = new APIClient("read", "/api/v1", "control");
  await client.getProjectPreparation("run-1");
  await client.prepareProject("run-1", request, "project-preparation-fixed");
  await client.getProjectPreparationOperation("run-1", "prep-1");
  await client.cancelProjectPreparation("run-1", "prep-1", "project-preparation-cancel-fixed");
  expect(fetch.mock.calls[0][0]).toBe("/api/v1/runs/run-1/project-preparation?target=.");
  expect(fetch.mock.calls[0][1].headers.Authorization).toBe("Bearer read");
  expect(fetch.mock.calls[1][1]).toMatchObject({ method: "POST", headers: {
    Authorization: "Bearer control", "Idempotency-Key": "project-preparation-fixed",
  } });
  expect(JSON.parse(fetch.mock.calls[1][1].body)).toEqual(request);
  expect(fetch.mock.calls[2][0]).toBe("/api/v1/runs/run-1/project-preparation/operations/prep-1");
  expect(fetch.mock.calls[2][1].headers.Authorization).toBe("Bearer read");
  expect(fetch.mock.calls[3][0]).toBe("/api/v1/runs/run-1/project-preparation/operations/prep-1/cancel");
  expect(fetch.mock.calls[3][1].headers["Idempotency-Key"]).toBe("project-preparation-cancel-fixed");
});

it("rejects malformed targets and missing authority before sending, and binds observations to their Run and operation", async () => {
  const fetch = vi.fn();
  vi.stubGlobal("fetch", fetch);
  await expect(new APIClient("read").prepareProject("run-1", request, "fixed")).rejects.toThrow("execution control");
  await expect(new APIClient("read", "/api/v1", "control").getProjectPreparation("run-1", "../other")).rejects.toThrow();
  expect(fetch).not.toHaveBeenCalled();
  expect(validProjectPreparationRequest({ ...request, allowed_sources: ["https://registry.npmjs.org/path"] })).toBe(false);
  expect(validProjectPreparationRequest({ ...request, allowed_sources: [] })).toBe(true);
  expect(() => parseProjectPreparation(preparationView(), "other-run")).toThrow();
  expect(() => parseProjectPreparation(preparationView(), "run-1", "web")).toThrow();
  expect(() => parseProjectPreparationOperation(preparationOperation(), "run-1", "other-op")).toThrow();
  expect(() => parseProjectPreparationOperation(preparationOperation({ completed_packages: 3 }), "run-1")).toThrow();
  expect(() => parseProjectPreparationOperation(preparationOperation({ can_cancel: true }), "run-1")).toThrow();
});

it("treats an admitted response without an operation as an unknown outcome", async () => {
  vi.stubGlobal("fetch", vi.fn().mockResolvedValue(response(preparationView())));
  await expect(new APIClient("read", "/api/v1", "control").prepareProject("run-1", request, "fixed"))
    .rejects.toMatchObject({ code: "INVALID_RESPONSE", status: 502 });
});

it("accepts a shared workspace root but rejects a root outside the run", () => {
  const view = preparationView({ target: "apps/web" });
  view.plan.workspace_root = ".";
  expect(parseProjectPreparation(view, "run-1", "apps/web").plan.workspace_root).toBe(".");
  view.plan.workspace_root = "../outside";
  expect(() => parseProjectPreparation(view, "run-1")).toThrow();
});
