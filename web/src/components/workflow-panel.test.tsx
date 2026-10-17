import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { APIClient } from "../api/client";
import { WorkflowPanel, workerWorkflow, workflowExample } from "./workflow-panel";

function response(data: unknown) {
  return new Response(JSON.stringify({ version: "api.v1", data, request_id: "workflow-test" }), { status: 200, headers: { "Content-Type": "application/json" } });
}
afterEach(() => vi.unstubAllGlobals());

it("defaults to inherited worker tools and preserves a read-only choice", async () => {
  const fetch = vi.fn().mockResolvedValue(response({ items: [] }));
  vi.stubGlobal("fetch", fetch);
  const user = userEvent.setup();
  render(<QueryClientProvider client={new QueryClient({ defaultOptions: { queries: { retry: false } } })}>
    <WorkflowPanel client={new APIClient("read", "/api/v1", "control")} runID="run-1" runStatus="running" />
  </QueryClientProvider>);
  await user.click(screen.getByText("Create workflow"));
  expect(screen.getByLabelText("Execution worker")).toBeChecked();
  await user.type(screen.getByLabelText("Name"), "Review");
  await user.type(screen.getByLabelText("Task"), "Inspect the change");
  const spec = workerWorkflow("Review", "Inspect the change", true, "");
  fetch.mockResolvedValueOnce(response({ id: "saved-1", digest: "digest", spec, created_at: "2026-10-17T00:00:00Z" }));
  await user.click(screen.getByRole("button", { name: "Save definition" }));
  await waitFor(() => expect(screen.getByLabelText("Saved workflow")).toHaveValue("saved-1"));
  const call = fetch.mock.calls.find(([, init]) => init?.method === "POST");
  expect(JSON.parse(call![1].body).steps[0].task.worker).toEqual({ tools: null });
  await user.click(screen.getByLabelText("Execution worker"));
  expect(screen.getByText("Read-only mode: allow reading the current workspace.")).toBeInTheDocument();
  expect(workerWorkflow("Read", "Inspect", false, "").steps[0].task?.worker).toBeUndefined();
  expect(workerWorkflow("Narrow", "Inspect", true, "workspace_read, workspace_search").steps[0].task?.worker?.tools)
    .toEqual(["workspace_read", "workspace_search"]);
  expect(workflowExample().steps[1].then?.[0]).toMatchObject({ kind: "repeat", max_iterations: 3 });
});

it("resumes a saved approval wait only on user action and stops dispatching when approval is still needed", async () => {
  let current = { id: "flow-1", definition_id: "saved-1", run_id: "run-1", status: "waiting_approval", revision: 2,
    inputs: {}, state: { stack: [{ block: "", index: 0 }], outputs: {}, iterations: {}, jobs: 0, steps: 0 } };
  const fetch = vi.fn().mockImplementation(async (path: string, init?: RequestInit) => {
    if (init?.method === "POST") {
      current = { ...current, revision: 3 };
      return response(current);
    }
    return response({ items: path.endsWith("/runs/run-1/workflows") ? [current] : [] });
  });
  vi.stubGlobal("fetch", fetch);
  const user = userEvent.setup();
  render(<QueryClientProvider client={new QueryClient({ defaultOptions: { queries: { retry: false } } })}>
    <WorkflowPanel client={new APIClient("read", "/api/v1", "control")} runID="run-1" runStatus="running" />
  </QueryClientProvider>);
  expect(await screen.findByText("The child is waiting for tool approval. Resolve the pending approval in this task, then continue.")).toBeInTheDocument();
  expect(fetch.mock.calls.filter(([, init]) => init?.method === "POST")).toHaveLength(0);
  await user.click(screen.getByRole("button", { name: "Continue running" }));
  await waitFor(() => expect(screen.getByRole("button", { name: "Continue running" })).toBeEnabled());
  const controls = fetch.mock.calls.filter(([, init]) => init?.method === "POST");
  expect(controls).toHaveLength(1);
  expect(JSON.parse(controls[0][1].body)).toMatchObject({ revision: 2 });
});
