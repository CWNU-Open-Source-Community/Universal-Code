import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act, render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { APIRequestError, type APIClient } from "../api/client";
import type { PlanDeliveryStateView, RunDetailView, WorkItemView } from "../api/types";
import { PlanDeliveryWorkItems } from "./plan-delivery-work-items";

function item(runID = "run-a", ordinal = 1): WorkItemView {
  return { id: `${runID}-item-${ordinal}`, run_id: runID, title: `Item ${ordinal}`, status: "in_progress",
    version: 2, acceptance_criteria: ["Actual checks are reviewed"], dependencies: [], priority: "normal",
    created_at: "2026-09-09T00:00:00Z", updated_at: "2026-09-09T00:00:00Z" };
}
function state(runID = "run-a", count = 1): PlanDeliveryStateView {
  return { operator_choice_needed: false, phase_change_needed: false, capability_grant: false,
    delivery_gate_enforced: true, required_checkpoints: count, ready_checkpoints: 0, checkpoints: [],
    selection: { id: `${runID}-selection`, proposal_id: `${runID}-proposal`, direction_ordinal: 1,
      note_id: `${runID}-note`, version: 1, created_at: "2026-09-09T00:00:00Z",
      items: Array.from({ length: count }, (_, index) => ({ ordinal: index + 1, module_ordinal: index + 1,
        work_item_id: item(runID, index + 1).id })) } };
}
const labels = ["Focused verification results", "Diff review", "Security review", "Full functional verification",
  "Failure and recovery checks", "Handoff summary"];
function setup(options: { client?: Partial<APIClient>; count?: number; status?: string } = {}) {
  const client = { hasPlanDelivery: true, getWorkItem: vi.fn(async (id: string) => {
    const value = item(id.startsWith("run-b") ? "run-b" : "run-a", id.endsWith("2") ? 2 : 1);
    return value;
  }), recordPlanDeliveryCheckpoint: vi.fn(async () => ({ current_work_item: item() })),
  controlPlanDeliveryWorkItem: vi.fn(), ...options.client } as unknown as APIClient;
  const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false }, mutations: { retry: false } } });
  const content = (runID = "run-a", plan = state(runID, options.count ?? 1)) => <QueryClientProvider client={queryClient}>
    <PlanDeliveryWorkItems client={client} detail={{ run: { id: runID, status: options.status ?? "paused" },
      mode: { phase: "deliver", revision: 5 } } as RunDetailView} state={plan} threadID={`thread-${runID}`} />
  </QueryClientProvider>;
  const view = render(content());
  return { ...view, client, queryClient, content, user: userEvent.setup() };
}

it("records all final-item evidence without starting tests or completing the item, then requires a current checkpoint", async () => {
  const test = setup();
  await screen.findByLabelText(labels[0]);
  expect(screen.getByRole("button", { name: "Complete this item" })).toBeDisabled();
  expect(screen.getByText(/automated checks retain their own pass or fail results/)).toBeInTheDocument();
  for (const label of labels) await test.user.type(screen.getByLabelText(label), `${label}: failed check; see job-1`);
  await test.user.click(screen.getByRole("button", { name: "Record manual acceptance and handoff" }));
  await waitFor(() => expect(test.client.recordPlanDeliveryCheckpoint).toHaveBeenCalledTimes(1));
  expect(test.client.recordPlanDeliveryCheckpoint).toHaveBeenCalledWith("run-a", "run-a-item-1", {
    version: "plan_delivery_control.v1", expected_work_item_version: 2,
    focused_verification: `${labels[0]}: failed check; see job-1`, diff_audit: `${labels[1]}: failed check; see job-1`,
    security_audit: `${labels[2]}: failed check; see job-1`, functional_verification: `${labels[3]}: failed check; see job-1`,
    robustness_audit: `${labels[4]}: failed check; see job-1`, handoff_summary: `${labels[5]}: failed check; see job-1`,
  }, expect.any(String));
  expect(test.client.controlPlanDeliveryWorkItem).not.toHaveBeenCalled();
  await waitFor(() => expect(screen.getByRole("button", { name: "Record manual acceptance and handoff" })).toBeEnabled());
  const plan = state();
  plan.checkpoints = [{ id: "checkpoint-a", work_item_id: item().id, module_ordinal: 1, module_count: 1,
    mode_revision: 5, work_item_version: 2, full_gate_required: true, gate_ready: true,
    handoff_note_id: "note-a", created_at: "2026-09-09T00:00:00Z" }];
  test.rerender(test.content("run-a", plan));
  expect(screen.getByRole("button", { name: "Complete this item" })).toBeEnabled();
  test.rerender(test.content("run-a", { ...plan, checkpoints: [{ ...plan.checkpoints[0], mode_revision: 4 }] }));
  expect(screen.getByRole("button", { name: "Complete this item" })).toBeDisabled();
});

it("omits final-only evidence for an earlier independent item", async () => {
  const test = setup({ count: 2 });
  await screen.findAllByLabelText(labels[0]);
  const first = within(screen.getByRole("heading", { name: "1. Item 1" }).closest("article")!);
  expect(first.queryByLabelText(labels[3])).not.toBeInTheDocument();
  for (const label of [labels[0], labels[1], labels[2], labels[5]]) await test.user.type(first.getByLabelText(label), "Observed result");
  await test.user.click(first.getByRole("button", { name: "Record manual acceptance and handoff" }));
  await waitFor(() => expect(test.client.recordPlanDeliveryCheckpoint).toHaveBeenCalledTimes(1));
  const body = vi.mocked(test.client.recordPlanDeliveryCheckpoint).mock.calls[0][2];
  expect(body).not.toHaveProperty("functional_verification");
  expect(body).not.toHaveProperty("robustness_audit");
});

it("shows inherited manual completion with its exact original handoff without claiming current automated verification", async () => {
  const oldNote = { id: "original-handoff", run_id: "run-original", content: "Original manual observation; automated check failed." };
  const test = setup({ client: {
    getWorkItem: vi.fn(async () => ({ ...item(), status: "completed" as const, version: 1 })),
    getNote: vi.fn(async () => oldNote as Awaited<ReturnType<APIClient["getNote"]>>),
  } });
  const plan = { ...state(), ready_checkpoints: 1, continued_completions: [{
    work_item_id: item().id, source_run_id: "run-original", source_work_item_id: "original-item",
    checkpoint_id: "original-checkpoint", handoff_note_id: oldNote.id, completed_at: "2026-09-08T22:00:00Z",
  }] };
  test.rerender(test.content("run-a", plan));
  expect(await screen.findByText(/Current automated checks are determined by this execution's verification results/)).toBeInTheDocument();
  expect(screen.queryByRole("button", { name: "Start this item" })).not.toBeInTheDocument();
  expect(screen.queryByRole("button", { name: "Complete this item" })).not.toBeInTheDocument();
  await test.user.click(screen.getByText("Read manual acceptance and handoff"));
  expect(await screen.findByText(oldNote.content)).toBeInTheDocument();
  expect(test.client.getNote).toHaveBeenCalledWith(oldNote.id, expect.any(AbortSignal));
  expect(test.client.recordPlanDeliveryCheckpoint).not.toHaveBeenCalled();
  expect(test.client.controlPlanDeliveryWorkItem).not.toHaveBeenCalled();
});

it("preserves drafts and the exact unknown request across closure, and isolates a late response after navigation", async () => {
  let finish!: (value: unknown) => void;
  const record = vi.fn().mockRejectedValueOnce(new Error("response lost"))
    .mockImplementationOnce(() => new Promise((resolve) => { finish = resolve; }));
  const test = setup({ client: { recordPlanDeliveryCheckpoint: record } });
  await screen.findByLabelText(labels[0]);
  for (const label of labels) await test.user.type(screen.getByLabelText(label), "Task A evidence");
  await test.user.click(screen.getByRole("button", { name: "Record manual acceptance and handoff" }));
  await screen.findByText(/response lost/);
  const original = record.mock.calls[0];
  expect(screen.getByLabelText(labels[0])).toBeDisabled();
  test.rerender(<QueryClientProvider client={test.queryClient}><div>Closed</div></QueryClientProvider>);
  test.rerender(test.content());
  expect(await screen.findByLabelText(labels[0])).toHaveValue("Task A evidence");
  await test.user.click(screen.getByRole("button", { name: "Confirm previous acceptance operation" }));
  await waitFor(() => expect(record).toHaveBeenCalledTimes(2));
  expect(record.mock.calls[1]).toEqual(original);
  test.rerender(test.content("run-b"));
  await screen.findByLabelText(labels[0]);
  await test.user.type(screen.getByLabelText(labels[0]), "Task B unsent evidence");
  await act(async () => finish({ current_work_item: item() }));
  expect(screen.getByLabelText(labels[0])).toHaveValue("Task B unsent evidence");
  expect(test.queryClient.getQueryData(["run", "run-a", "manual-delivery-intent"])).toBeNull();
});

it("lets an explicitly rejected request be edited, and prevents updates while running or read-only", async () => {
  const record = vi.fn().mockRejectedValue(new APIRequestError("Item version changed", "CONFLICT", 409));
  const test = setup({ client: { recordPlanDeliveryCheckpoint: record } });
  await screen.findByLabelText(labels[0]);
  for (const label of labels) await test.user.type(screen.getByLabelText(label), "Preserved evidence");
  await test.user.click(screen.getByRole("button", { name: "Record manual acceptance and handoff" }));
  await test.user.click(await screen.findByRole("button", { name: "Return to editing" }));
  expect(screen.getByLabelText(labels[0])).toBeEnabled();
  expect(screen.getByLabelText(labels[0])).toHaveValue("Preserved evidence");
  test.unmount();
  const running = setup({ status: "running" });
  expect(await screen.findByLabelText(labels[0])).toBeDisabled();
  running.unmount();
  setup({ client: { hasPlanDelivery: false } });
  expect(await screen.findByLabelText(labels[0])).toBeDisabled();
});

it("lets on-demand items complete without fabricated manual evidence and preserves an optional draft", async () => {
  const complete = vi.fn().mockRejectedValue(new APIRequestError("Current check is not passed", "FAILED_PRECONDITION", 412));
  const test = setup({ client: { controlPlanDeliveryWorkItem: complete } });
  const plan = state();
  plan.selection!.manual_acceptance = "on_demand";
  test.rerender(test.content("run-a", plan));
  const button = await screen.findByRole("button", { name: "Complete this item" });
  expect(button).toBeEnabled();
  expect(screen.queryByLabelText(labels[0])).not.toBeInTheDocument();
  expect(screen.getByRole("button", { name: "Add manual acceptance notes (optional)" })).toBeInTheDocument();
  await test.user.click(screen.getByRole("button", { name: "Add manual acceptance notes (optional)" }));
  await test.user.type(screen.getByLabelText(labels[0]), "Observed failure, awaiting correction");
  await test.user.click(screen.getByRole("button", { name: "Hide manual notes" }));
  await test.user.click(button);
  await screen.findByText(/Current check is not passed/);
  expect(complete).toHaveBeenCalledWith("run-a", item().id, "complete",
    { version: "plan_delivery_control.v1", expected_work_item_version: 2 }, expect.any(String));
  expect(test.client.recordPlanDeliveryCheckpoint).not.toHaveBeenCalled();
  expect(test.queryClient.getQueryData<WorkItemView>(["work-item", item().id])?.status).toBe("in_progress");
  await test.user.click(screen.getByRole("button", { name: "Return to editing" }));
  await test.user.click(screen.getByRole("button", { name: "Add manual acceptance notes (optional)" }));
  expect(screen.getByLabelText(labels[0])).toHaveValue("Observed failure, awaiting correction");
});

it("retains dependencies and control gates for on-demand completion", async () => {
  const test = setup({ client: { getWorkItem: vi.fn(async () => ({ ...item(), dependencies: ["missing-dependency"] })) } });
  const plan = state();
  plan.selection!.manual_acceptance = "on_demand";
  test.rerender(test.content("run-a", plan));
  expect(await screen.findByRole("button", { name: "Complete this item" })).toBeDisabled();
  expect(screen.getByText("Complete the dependencies before continuing.")).toBeInTheDocument();
  test.unmount();
  const readOnly = setup({ client: { hasPlanDelivery: false } });
  readOnly.rerender(readOnly.content("run-a", plan));
  expect(await screen.findByRole("button", { name: "Complete this item" })).toBeDisabled();
});

it("shows inherited on-demand completion as an exact event source without requesting an empty handoff note", async () => {
  const getNote = vi.fn();
  const test = setup({ client: { getNote,
    getWorkItem: vi.fn(async () => ({ ...item(), status: "completed" as const, version: 1 })) } });
  const plan = state();
  plan.selection!.manual_acceptance = "on_demand";
  plan.continued_completions = [{ work_item_id: item().id, source_run_id: "original-run", source_work_item_id: "original-item",
    completion_event_id: "original-completion", checkpoint_id: "", handoff_note_id: "", completed_at: "2026-09-09T00:00:00Z" }];
  test.rerender(test.content("run-a", plan));
  expect(await screen.findByText(/Review manual acceptance and this execution/)).toBeInTheDocument();
  await test.user.click(screen.getByText("View completion source"));
  expect(screen.getByText("original-completion")).toBeVisible();
  expect(screen.getByText("original-run")).toBeVisible();
  expect(screen.queryByText("Read manual acceptance and handoff")).not.toBeInTheDocument();
  expect(getNote).not.toHaveBeenCalled();
  expect(test.client.controlPlanDeliveryWorkItem).not.toHaveBeenCalled();
});
