import { WorkflowClient } from "./workflows";

const execution = { id: "flow-1", definition_id: "def-1", run_id: "run-1", status: "running", revision: 3,
  state: { stack: [{ block: "", index: 0 }], outputs: {}, iterations: {}, jobs: 0, steps: 0 } };

it("uses explicit launch consent and revision-fenced advance", async () => {
  const post = vi.fn().mockResolvedValue(execution);
  const client = new WorkflowClient({ get: vi.fn(), post });
  await client.start("run-1", "def-1", { task: "inspect" }, "start-key");
  expect(post).toHaveBeenLastCalledWith("/runs/run-1/workflows", {
    version: "workflow_start.v1", definition_id: "def-1", inputs: { task: "inspect" }, confirm_start: true,
  }, "start-key", undefined);
  await client.advance("run-1", "flow-1", 3, "advance-key");
  expect(post).toHaveBeenLastCalledWith("/runs/run-1/workflows/flow-1/advance", {
    version: "workflow_control.v1", revision: 3,
  }, "advance-key", undefined);
  await expect(client.advance("run-1", "flow-1", 0, "invalid")).rejects.toThrow("revision");
  expect(post).toHaveBeenCalledTimes(2);
});

it("rejects foreign executions and accepts durable approval waiting", async () => {
  const get = vi.fn().mockResolvedValueOnce({ ...execution, run_id: "another-run" })
    .mockResolvedValueOnce({ ...execution, status: "waiting_approval" });
  const client = new WorkflowClient({ get, post: vi.fn() });
  await expect(client.get("run-1", "flow-1")).rejects.toThrow("Invalid workflow execution");
  expect((await client.get("run-1", "flow-1")).status).toBe("waiting_approval");
  await expect(client.get("run-1", "../other")).rejects.toThrow("identity");
  expect(get).toHaveBeenCalledTimes(2);
});

it("retries failed items with explicit consent, the observed revision and the original key", async () => {
  const post = vi.fn().mockResolvedValue(execution);
  const client = new WorkflowClient({ get: vi.fn(), post });
  await client.retryFailed("run-1", "flow-1", 3, "retry-failed-key");
  expect(post).toHaveBeenLastCalledWith("/runs/run-1/workflows/flow-1/retry_failed", {
    version: "workflow_control.v1", revision: 3, confirm_start: true,
  }, "retry-failed-key", undefined);
  await expect(client.retryFailed("run-1", "flow-1", 0, "invalid")).rejects.toThrow("revision");
  expect(post).toHaveBeenCalledTimes(1);
  post.mockResolvedValueOnce({ ...execution, id: "another-workflow" });
  await expect(client.retryFailed("run-1", "flow-1", 3, "retry-failed-key")).rejects.toThrow("Invalid workflow execution");
});
