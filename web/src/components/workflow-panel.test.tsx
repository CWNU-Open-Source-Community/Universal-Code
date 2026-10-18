import { focusManager, QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act, cleanup, render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { APIClient } from "../api/client";
import { WorkflowPanel, workerWorkflow, workflowExample, isolatedWorkflow } from "./workflow-panel";

function response(data: unknown) {
  return new Response(JSON.stringify({ version: "api.v1", data, request_id: "workflow-test" }), { status: 200, headers: { "Content-Type": "application/json" } });
}
afterEach(() => {
  cleanup();
  focusManager.setFocused(undefined);
  vi.useRealTimers();
  vi.unstubAllGlobals();
});

function execution(status: string, revision = 2) {
  return { id: "flow-1", definition_id: "saved-1", run_id: "run-1", status, revision,
    inputs: {}, state: { stack: [{ block: "", index: 0 }], outputs: {}, iterations: {}, jobs: 0, steps: 0 } };
}

function mount(client = new APIClient("read", "/api/v1", "control"),
  queries = new QueryClient({ defaultOptions: { queries: { retry: false } } }), runStatus = "running") {
  return render(<QueryClientProvider client={queries}>
    <WorkflowPanel client={client} runID="run-1" runStatus={runStatus} />
  </QueryClientProvider>);
}

it("saves an isolated delivery without inventing a task or an approval", async () => {
  const fetch = vi.fn().mockResolvedValue(response({ items: [] }));
  vi.stubGlobal("fetch", fetch);
  const user = userEvent.setup();
  render(<QueryClientProvider client={new QueryClient({ defaultOptions: { queries: { retry: false } } })}>
    <WorkflowPanel client={new APIClient("read", "/api/v1", "control")} runID="run-1" runStatus="running" />
  </QueryClientProvider>);
  await user.click(screen.getByText("Create workflow"));
  await user.type(screen.getByLabelText("Name"), "Deliver in isolation");
  await user.selectOptions(screen.getByLabelText("Workspace mode"), "batch");
  expect(screen.queryByLabelText("Task")).not.toBeInTheDocument();
  expect(screen.getByText(/Use a delivery plan already approved/)).toBeInTheDocument();
  const spec = isolatedWorkflow("Deliver in isolation");
  fetch.mockResolvedValueOnce(response({ id: "saved-isolated", spec, digest: "digest" }));
  await user.click(screen.getByRole("button", { name: "Save definition" }));
  await waitFor(() => expect(screen.getByLabelText("Saved workflow")).toHaveValue("saved-isolated"));
  const posted = fetch.mock.calls.filter(([, init]) => init?.method === "POST");
  expect(posted).toHaveLength(1);
  expect(JSON.parse(posted[0][1].body).steps).toEqual([
    { id: "delivery", kind: "batch", batch_plan: { step: "$input", path: "/plan_id" } },
  ]);
});

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

it("requires an explicit choice to include parent history", async () => {
  vi.stubGlobal("fetch", vi.fn().mockResolvedValue(response({ items: [] })));
  const user = userEvent.setup();
  render(<QueryClientProvider client={new QueryClient({ defaultOptions: { queries: { retry: false } } })}>
    <WorkflowPanel client={new APIClient("read", "/api/v1", "control")} runID="run-1" runStatus="running" />
  </QueryClientProvider>);
  await user.click(screen.getByText("Create workflow"));
  expect(screen.getByLabelText("Include current parent history")).not.toBeChecked();
  expect(workerWorkflow("Read", "Inspect", true, "").steps[0].task?.worker?.history_scope).toBeUndefined();
  await user.click(screen.getByLabelText("Include current parent history"));
  await user.click(screen.getByRole("button", { name: "Edit task and budgets in JSON" }));
  const spec = JSON.parse((screen.getByLabelText("Workflow JSON (copyable)") as HTMLTextAreaElement).value);
  expect(spec.steps[0].task.worker.history_scope).toBe("parent_snapshot");
  expect(workerWorkflow("Read", "Inspect", false, "", true).steps[0].task?.worker).toBeUndefined();
});

it("starts once with explicit consent and observes backend progress without advancing turns", async () => {
  let current: ReturnType<typeof execution> | undefined;
  const spec = workerWorkflow("Review", "Inspect the change", true, "");
  const fetch = vi.fn().mockImplementation(async (path: string, init?: RequestInit) => {
    if (init?.method === "POST") {
      current = execution("running");
      return response(current);
    }
    return response({ items: path.endsWith("/runs/run-1/workflows") ? (current ? [current] : [])
      : [{ id: "saved-1", digest: "digest", spec }] });
  });
  vi.stubGlobal("fetch", fetch);
  const user = userEvent.setup();
  mount();
  await user.selectOptions(screen.getByLabelText("Saved workflow"), await screen.findByRole("option", { name: /Review/ }));
  await user.click(screen.getByRole("button", { name: "Start and run" }));
  await waitFor(() => expect(screen.getByRole("button", { name: "Pause workflow" })).toBeEnabled());
  expect(screen.queryByRole("button", { name: "Advance one turn" })).not.toBeInTheDocument();
  expect(screen.queryByRole("button", { name: "Continue running" })).not.toBeInTheDocument();
  current = execution("completed", 4);
  await waitFor(() => expect(screen.getByText(/checkpoint 4/)).toBeInTheDocument(), { timeout: 3_000 });
  const controls = fetch.mock.calls.filter(([, init]) => init?.method === "POST");
  expect(controls).toHaveLength(1);
  expect(controls[0][0]).toBe("/api/v1/runs/run-1/workflows");
  expect(JSON.parse(controls[0][1].body)).toMatchObject({ definition_id: "saved-1", confirm_start: true });
});

it.each([
  ["waiting", "Continue running", "resume"],
  ["paused", "Resume and run", "resume"],
  ["completed", "Rerun original inputs", "rerun"],
])("submits one %s control request and leaves execution to the app", async (status, label, endpoint) => {
  let current = execution(status);
  const fetch = vi.fn().mockImplementation(async (path: string, init?: RequestInit) => {
    if (init?.method === "POST") {
      current = execution("running", 3);
      return response(current);
    }
    return response({ items: path.endsWith("/runs/run-1/workflows") ? [current] : [] });
  });
  vi.stubGlobal("fetch", fetch);
  const user = userEvent.setup();
  mount();
  await screen.findByRole("button", { name: label });
  expect(fetch.mock.calls.filter(([, init]) => init?.method === "POST")).toHaveLength(0);
  await user.click(screen.getByRole("button", { name: label }));
  await waitFor(() => expect(screen.getByRole("button", { name: "Pause workflow" })).toBeEnabled());
  const controls = fetch.mock.calls.filter(([, init]) => init?.method === "POST");
  expect(controls).toHaveLength(1);
  expect(controls[0][0]).toBe(`/api/v1/runs/run-1/workflows/flow-1/${endpoint}`);
  expect(JSON.parse(controls[0][1].body)).toMatchObject(endpoint === "rerun" ? { confirm_start: true } : { revision: 2 });
});

it("observes saved approval waits across page visits and polls only visible unfinished workflows", async () => {
  vi.useFakeTimers();
  let current = execution("waiting_approval");
  const fetch = vi.fn().mockImplementation(async (path: string) =>
    response({ items: path.endsWith("/runs/run-1/workflows") ? [current] : [] }));
  vi.stubGlobal("fetch", fetch);
  const client = new APIClient("read", "/api/v1", "control");
  const queries = new QueryClient({ defaultOptions: { queries: { retry: false, staleTime: Infinity } } });
  const invalidate = vi.spyOn(queries, "invalidateQueries");
  queries.setQueryData(["run", "run-1", "workflows"], [execution("paused", 1)]);
  const reads = () => fetch.mock.calls.filter(([path]) => path.endsWith("/runs/run-1/workflows")).length;
  const view = mount(client, queries);
  await act(async () => { await vi.advanceTimersByTimeAsync(1); });
  expect(screen.getByText("The child is waiting for tool approval. The app continues automatically after the pending approval is resolved.")).toBeInTheDocument();
  expect(screen.queryByRole("button", { name: "Continue running" })).not.toBeInTheDocument();
  expect(reads()).toBe(1);

  invalidate.mockClear();
  current = execution("running", 3);
  await act(async () => { await vi.advanceTimersByTimeAsync(2_000); });
  expect(screen.getByText(/checkpoint 3/)).toBeInTheDocument();
  expect(reads()).toBe(2);
  expect(invalidate).toHaveBeenCalledExactlyOnceWith({ queryKey: ["run", "run-1", "agent-graph"] }, { cancelRefetch: false });
  await act(async () => { await vi.advanceTimersByTimeAsync(2_000); });
  expect(reads()).toBe(3);
  expect(invalidate).toHaveBeenCalledTimes(1);

  focusManager.setFocused(false);
  current = execution("completed", 4);
  await act(async () => { await vi.advanceTimersByTimeAsync(6_000); });
  expect(reads()).toBe(3);
  await act(async () => { focusManager.setFocused(true); await vi.advanceTimersByTimeAsync(1); });
  expect(screen.getByText(/checkpoint 4/)).toBeInTheDocument();
  expect(reads()).toBe(4);
  await act(async () => { await vi.advanceTimersByTimeAsync(6_000); });
  expect(reads()).toBe(4);

  view.unmount();
  current = execution("waiting", 5);
  const nextVisit = mount(client, queries);
  await act(async () => { await vi.advanceTimersByTimeAsync(1); });
  expect(screen.getByRole("button", { name: "Continue running" })).toBeEnabled();
  expect(reads()).toBe(5);
  current = execution("paused", 6);
  await act(async () => { await vi.advanceTimersByTimeAsync(2_000); });
  expect(screen.getByRole("button", { name: "Resume and run" })).toBeEnabled();
  expect(reads()).toBe(6);
  nextVisit.unmount();
  await act(async () => { await vi.advanceTimersByTimeAsync(6_000); });
  expect(reads()).toBe(6);
  expect(fetch.mock.calls.filter(([, init]) => init?.method === "POST")).toHaveLength(0);
});

it.each([
  [false, "running"],
  [true, "paused"],
])("requires control permission and a running task before continuing (%s, %s)", async (control, runStatus) => {
  vi.stubGlobal("fetch", vi.fn().mockImplementation(async (path: string) =>
    response({ items: path.endsWith("/runs/run-1/workflows") ? [execution("waiting")] : [] })));
  mount(new APIClient("read", "/api/v1", control ? "control" : ""), undefined, runStatus);
  expect(await screen.findByRole("button", { name: "Continue running" })).toBeDisabled();
  expect(screen.getByRole("button", { name: "Rerun original inputs" })).toBeDisabled();
});

it("pauses an executing workflow with its observed revision", async () => {
  let current = execution("running", 7);
  const fetch = vi.fn().mockImplementation(async (path: string, init?: RequestInit) => {
    if (init?.method === "POST") {
      current = execution("paused", 8);
      return response(current);
    }
    return response({ items: path.endsWith("/runs/run-1/workflows") ? [current] : [] });
  });
  vi.stubGlobal("fetch", fetch);
  const user = userEvent.setup();
  mount();
  await user.click(await screen.findByRole("button", { name: "Pause workflow" }));
  await waitFor(() => expect(screen.getByRole("button", { name: "Resume and run" })).toBeEnabled());
  const controls = fetch.mock.calls.filter(([, init]) => init?.method === "POST");
  expect(controls).toHaveLength(1);
  expect(controls[0][0]).toBe("/api/v1/runs/run-1/workflows/flow-1/pause");
  expect(JSON.parse(controls[0][1].body)).toMatchObject({ revision: 7 });
});
