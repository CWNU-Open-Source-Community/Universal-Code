import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import type { APIClient } from "../api/client";
import type { ScheduledJobCreateRequestView, ScheduledJobView, ThreadView } from "../api/types";
import { ScheduledTasksWorkspace } from "./scheduled-tasks-workspace";

describe("ScheduledTasksWorkspace", () => {
  it("creates only a bounded read-only schedule and uses a memory-only operation key", async () => {
    const user = userEvent.setup();
    const createScheduledJob = vi.fn().mockResolvedValue({
      protocol_version: "scheduled-job-control.v1", action: "create", replayed: false,
      execution_started: false, authority_bypass: false, job: scheduledJob(),
    });
    renderWorkspace({
      hasScheduledJobControl: true, hasScheduledJobWorker: true,
      listScheduledJobs: vi.fn().mockResolvedValue({ protocol_version: "scheduled-job.v1", items: [] }),
      createScheduledJob, transitionScheduledJob: vi.fn(),
      diagnosticBundle: vi.fn(),
    });

    await screen.findByText("No time schedules yet. Choose a task and trigger time to begin.");
    await user.click(screen.getByRole("button", { name: "Read-only observation" }));
    await user.click(screen.getByRole("button", { name: "Create and observe" }));
    await waitFor(() => expect(createScheduledJob).toHaveBeenCalledTimes(1));
    expect(createScheduledJob.mock.calls[0]?.[0]).toBe("run-1");
    expect(createScheduledJob.mock.calls[0]?.[1]).toMatchObject({
      version: "scheduled-job.v1", execution_mode: "read_only", confirm_repair: false,
      max_model_calls: 0, stop_on_target_terminal: true,
      observation_consent_version: 1,
      retry: { max_attempts: 3, initial_backoff_seconds: 5, max_backoff_seconds: 60 },
    });
    const body = createScheduledJob.mock.calls[0]?.[1] as ScheduledJobCreateRequestView;
    const secondsUntilDeadline = Math.floor(
      (new Date(body.deadline_at).getTime() - Date.now()) / 1_000);
    expect(Math.abs(body.max_elapsed_seconds - secondsUntilDeadline)).toBeLessThan(3);
    expect(createScheduledJob.mock.calls[0]?.[2]).toMatch(/^web-scheduled-job-create-/);
  });

  it("pauses by exact revision and keeps diagnostics as a redacted export", async () => {
    const user = userEvent.setup();
    const job = scheduledJob();
    const transitionScheduledJob = vi.fn().mockResolvedValue({
      protocol_version: "scheduled-job-control.v1", action: "pause", replayed: false,
      execution_started: false, authority_bypass: false, job: { ...job, status: "paused" },
    });
    const diagnosticBundle = vi.fn().mockResolvedValue({ protocol_version: "diagnostic-bundle.v1" });
    vi.stubGlobal("URL", { ...URL, createObjectURL: vi.fn().mockReturnValue("blob:test"),
      revokeObjectURL: vi.fn() });
    const click = vi.spyOn(HTMLAnchorElement.prototype, "click").mockImplementation(() => undefined);
    renderWorkspace({
      hasScheduledJobControl: true, hasScheduledJobWorker: false,
      listScheduledJobs: vi.fn().mockResolvedValue({ protocol_version: "scheduled-job.v1", items: [job] }),
      getScheduledJob: vi.fn().mockResolvedValue({ protocol_version: "scheduled-job.v1",
        snapshot: { job, rounds: [], notifications: [] } }),
      createScheduledJob: vi.fn(), transitionScheduledJob, diagnosticBundle,
    });

    await user.click(await screen.findByRole("button", { name: /scheduled-job-1/i }));
    await user.click(await screen.findByRole("button", { name: "Pause" }));
    await waitFor(() => expect(transitionScheduledJob).toHaveBeenCalledWith(
      "run-1", "scheduled-job-1", "pause",
      { version: "scheduled-job-control.v1", expected_revision: 4 }, expect.any(String)));
    await user.click(await screen.findByRole("button", { name: "Export diagnostics" }));
    await waitFor(() => expect(diagnosticBundle).toHaveBeenCalledWith("run-1"));
    expect(click).toHaveBeenCalled();
    click.mockRestore();
    vi.unstubAllGlobals();
  });

  it("reuses the exact create request after a lost response and starts a new intent only after editing", async () => {
    const user = userEvent.setup();
    const createScheduledJob = vi.fn().mockRejectedValue(new Error("Connection lost; outcome unknown"));
    renderWorkspace({ hasScheduledJobControl: true, hasScheduledJobWorker: true,
      listScheduledJobs: vi.fn().mockResolvedValue({ protocol_version: "scheduled-job.v1", items: [] }),
      createScheduledJob,
    });
    await user.click(screen.getByRole("button", { name: "Read-only observation" }));
    const submit = screen.getByRole("button", { name: "Create and observe" });
    await user.click(submit);
    await screen.findByText("Connection lost; outcome unknown");
    const now = vi.spyOn(Date, "now").mockReturnValue(Date.now() + 10_000);
    await user.click(submit);
    await waitFor(() => expect(createScheduledJob).toHaveBeenCalledTimes(2));
    expect(createScheduledJob.mock.calls[1]).toEqual(createScheduledJob.mock.calls[0]);
    await user.selectOptions(screen.getByLabelText("Notifications"), "silent");
    await user.click(submit);
    await waitFor(() => expect(createScheduledJob).toHaveBeenCalledTimes(3));
    expect(createScheduledJob.mock.calls[2]?.[2]).not.toBe(createScheduledJob.mock.calls[0]?.[2]);
    expect(createScheduledJob.mock.calls[2]?.[1]).toMatchObject({ notification: "silent" });
    now.mockRestore();
  });

  it("confirms one legacy paused schedule without resuming it or claiming a stopped worker is running", async () => {
    const user = userEvent.setup();
    const job = { ...scheduledJob(), status: "paused" as const, observation_consent_version: 0 };
    const enabledJob = { ...job, observation_consent_version: 1 };
    const enableScheduledJobObservation = vi.fn().mockRejectedValueOnce(new Error("Observation response lost"))
      .mockResolvedValue({ job: enabledJob });
    const transitionScheduledJob = vi.fn();
    renderWorkspace({
      hasScheduledJobControl: true, hasScheduledJobWorker: true,
      runtimeCapabilities: vi.fn().mockResolvedValue({ scheduled_job_worker: { enabled: true, state: "stopped" } }),
      listScheduledJobs: vi.fn().mockResolvedValue({ protocol_version: "scheduled-job.v1", items: [job] }),
      getScheduledJob: vi.fn().mockResolvedValue({ protocol_version: "scheduled-job.v1",
        snapshot: { job, rounds: [], notifications: [] } }),
      enableScheduledJobObservation, transitionScheduledJob,
    });
    await screen.findByText(/Observer is stopped or shutting down/);
    expect(screen.queryByText(/Observer is running/)).not.toBeInTheDocument();
    await user.click(await screen.findByRole("button", { name: /scheduled-job-1/i }));
    await user.click(await screen.findByRole("button", { name: "Confirm automatic desktop observation" }));
    await screen.findByText("Observation response lost");
    await user.click(await screen.findByRole("button", { name: "Confirm automatic desktop observation" }));
    await waitFor(() => expect(enableScheduledJobObservation).toHaveBeenCalledTimes(2));
    expect(enableScheduledJobObservation.mock.calls[1]).toEqual(enableScheduledJobObservation.mock.calls[0]);
    expect(enableScheduledJobObservation).toHaveBeenCalledWith(
      "run-1", "scheduled-job-1", { version: "scheduled-job-control.v1", expected_revision: 4,
        observation_consent_version: 1 }, expect.stringMatching(/^web-scheduled-job-enable-observation-/));
    expect(transitionScheduledJob).not.toHaveBeenCalled();
    expect(screen.getByRole("button", { name: "Resume" })).toBeInTheDocument();
  });

  it("identifies an explicitly launched all-job worker without claiming a legacy job is waiting", async () => {
    const job = { ...scheduledJob(), observation_consent_version: 0 };
    renderWorkspace({
      hasScheduledJobControl: true, hasScheduledJobWorker: true,
      runtimeCapabilities: vi.fn().mockResolvedValue({ scheduled_job_worker: {
        enabled: true, state: "running", selection_scope: "all_jobs",
      } }),
      listScheduledJobs: vi.fn().mockResolvedValue({ protocol_version: "scheduled-job.v1", items: [job] }),
    });
    await screen.findByText(/Explicit scheduling of all jobs, including legacy jobs/);
    expect(screen.queryByText("Desktop observation needs confirmation")).not.toBeInTheDocument();
  });

  it("selects an existing task, shows its directory and permission, and creates bounded execution", async () => {
    const user = userEvent.setup();
    const createScheduledJob = vi.fn().mockResolvedValue({ job: scheduledJob() });
    renderWorkspace({ getPage: vi.fn().mockResolvedValue({ items: [threadData()], page: {} }), createScheduledJob });
    await screen.findByRole("option", { name: "Review the automation change" });
    await user.selectOptions(screen.getByLabelText("Existing task"), "thread-1");
    await screen.findByText("D:/projects/example");
    expect(await screen.findByText("ASK")).toBeVisible();
    await user.type(screen.getByLabelText("Instructions"), "Check review feedback and run relevant tests");
    await user.click(screen.getByRole("button", { name: "Create automation" }));
    await waitFor(() => expect(createScheduledJob).toHaveBeenCalledWith("run-1", expect.objectContaining({
      execution_mode: "automatic", execution_consent_version: 1, observation_consent_version: 0,
      execution_target: { kind: "thread", thread_id: "thread-1", prompt: "Check review feedback and run relevant tests" },
      stop_on_target_terminal: false, max_model_calls: 0,
    }), expect.stringMatching(/^web-scheduled-job-create-/)));
  });

  it("selects a saved workflow and rejects non-object inputs before creating it", async () => {
    const user = userEvent.setup();
    const createScheduledJob = vi.fn().mockResolvedValue({ job: scheduledJob() });
    const definitions = vi.fn().mockResolvedValue([{ id: "workflow-1", spec: { name: "Inspect and repair" } }]);
    renderWorkspace({ getPage: vi.fn().mockResolvedValue({ items: [threadData()], page: {} }),
      workflows: { definitions } as unknown as APIClient["workflows"], createScheduledJob });
    await screen.findByRole("option", { name: "Review the automation change" });
    await user.selectOptions(screen.getByLabelText("Existing task"), "thread-1");
    await user.click(screen.getByRole("button", { name: "Scheduled workflow" }));
    await screen.findByRole("option", { name: "Inspect and repair" });
    await user.selectOptions(screen.getByLabelText("Saved workflow"), "workflow-1");
    const inputs = screen.getByLabelText("Workflow inputs (JSON object)");
    await user.clear(inputs);
    await user.type(inputs, "[[]");
    await user.click(screen.getByRole("button", { name: "Create automation" }));
    await screen.findByText("Workflow inputs must be a JSON object");
    expect(createScheduledJob).not.toHaveBeenCalled();
    await user.clear(inputs);
    await user.type(inputs, '{{"request":"Review the changes"}');
    await user.click(screen.getByRole("button", { name: "Create automation" }));
    await waitFor(() => expect(createScheduledJob).toHaveBeenCalledWith("run-1", expect.objectContaining({
      execution_mode: "automatic", execution_target: { kind: "workflow", workflow_definition_id: "workflow-1", inputs: { request: "Review the changes" } },
      execution_consent_version: 1, stop_on_target_terminal: false,
    }), expect.any(String)));
  });

  it("keeps execution unavailable on an observation-only host", async () => {
    const user = userEvent.setup();
    const createScheduledJob = vi.fn();
    renderWorkspace({ hasRunExecution: false, hasModelControl: false,
      getPage: vi.fn().mockResolvedValue({ items: [threadData()], page: {} }), createScheduledJob });
    await screen.findByRole("option", { name: "Review the automation change" });
    await user.selectOptions(screen.getByLabelText("Existing task"), "thread-1");
    await user.type(screen.getByLabelText("Instructions"), "Check the task");
    expect(screen.getByRole("button", { name: "Create automation" })).toBeDisabled();
    expect(screen.getByText(/Model execution control is unavailable/)).toBeVisible();
    await user.click(screen.getByRole("button", { name: "Read-only observation" }));
    expect(screen.getByRole("button", { name: "Create and observe" })).toBeEnabled();
    expect(createScheduledJob).not.toHaveBeenCalled();
  });

  it("shows execution outcomes and applies pause, resume, and cancellation by current revision", async () => {
    const user = userEvent.setup();
    let job: ScheduledJobView = { ...scheduledJob(), observation_consent_version: 0, execution_consent_version: 1,
      spec: { ...scheduledJob().spec, execution_mode: "automatic", stop_on_target_terminal: false,
        execution_target: { kind: "thread", thread_id: "thread-1", prompt: "Check changes" } } };
    const transitionScheduledJob = vi.fn().mockImplementation(async (_run, _id, action: string) => {
      job = { ...job, revision: job.revision + 1, status: action === "pause" ? "paused" : action === "resume" ? "active" : "cancelled" };
      return { job };
    });
    renderWorkspace({ listScheduledJobs: vi.fn().mockImplementation(async () => ({ items: [job] })),
      getScheduledJob: vi.fn().mockImplementation(async () => ({ snapshot: { job, rounds: [], notifications: [{
        id: "notice-1", kind: "change", summary: "Execution is waiting for tool approval",
      }], executions: [{ job_id: job.id, occurrence_at: "2026-10-18T10:00:00Z", state: "waiting_approval",
        run_id: "run-1", result: "Approve the pending tool request in the task" }] } })), transitionScheduledJob });
    await user.click(await screen.findByRole("button", { name: /scheduled-job-1/ }));
    expect(await screen.findByText("Waiting for approval")).toBeVisible();
    expect(screen.getByRole("link", { name: "Open execution task" })).toHaveAttribute("href", "#/threads/thread-1");
    await user.click(screen.getByRole("button", { name: "Pause" }));
    await user.click(await screen.findByRole("button", { name: "Resume" }));
    await user.click(await screen.findByRole("button", { name: "Cancel" }));
    await waitFor(() => expect(transitionScheduledJob).toHaveBeenCalledTimes(3));
    expect(transitionScheduledJob.mock.calls.map((call) => [call[2], call[3].expected_revision])).toEqual([["pause", 4], ["resume", 5], ["cancel", 6]]);
  });
});

function renderWorkspace(client: Partial<APIClient>) {
  const queryClient = new QueryClient({ defaultOptions: {
    queries: { retry: false }, mutations: { retry: false },
  } });
  return render(<QueryClientProvider client={queryClient}>
    <ScheduledTasksWorkspace client={{
      hasScheduledJobControl: true, hasRunExecution: true, hasModelControl: true,
      getPage: vi.fn().mockResolvedValue({ items: [], page: {} }),
      get: vi.fn().mockImplementation(async (path: string) => path.endsWith("/review")
        ? { thread_id: "thread-1", target: { state: "available", root_path: "D:/projects/example" },
          revision: { state: "available", repository_kind: "none" }, applied_changes: [], unapplied_changes: [], checks: [], reasons: [], observed_at: "2026-10-18T09:00:00Z" }
        : { thread: threadData(), last_run: { id: "run-1", status: "running" } }),
      getThreadExecutionPermission: vi.fn().mockResolvedValue({ execution_permission: { approval_mode: "ask" } }),
      listScheduledJobs: vi.fn().mockResolvedValue({ protocol_version: "scheduled-job.v1", items: [] }),
      getScheduledJob: vi.fn().mockResolvedValue({ snapshot: { job: scheduledJob(), notifications: [], rounds: [], executions: [] } }),
      runtimeCapabilities: vi.fn().mockResolvedValue({ scheduled_job_worker: {
        enabled: Boolean(client.hasScheduledJobWorker), state: client.hasScheduledJobWorker ? "running" : "disabled",
        selection_scope: "confirmed_read_only",
      } }), ...client,
    } as APIClient} initialRunID="run-1" />
  </QueryClientProvider>);
}

function threadData(): ThreadView {
  return { id: "thread-1", title: "Review the automation change", last_run_id: "run-1", mission_id: "mission-1",
    protocol_version: "thread.v1", status: "active", composer_state: "ready", version: 1,
    created_at: "2026-10-18T09:00:00Z", updated_at: "2026-10-18T09:00:00Z" };
}

function scheduledJob(): ScheduledJobView {
  return {
    id: "scheduled-job-1", owner_run_id: "run-1", owner_root_agent_id: "agent-root",
    status: "active", revision: 4, active_lease_generation: 0, rounds_completed: 0,
    consecutive_unchanged: 0, model_calls: 0, last_event_sequence: 0,
    observation_consent_version: 1, execution_consent_version: 0,
    next_wake_at: "2026-08-20T11:00:00Z", created_by: "test",
    created_at: "2026-08-20T10:00:00Z", updated_at: "2026-08-20T10:00:00Z",
    spec: { version: "scheduled-job.v1", target_run_id: "run-1", execution_mode: "read_only",
      schedule: { kind: "once", timezone: "UTC", anchor_at: "2026-08-20T11:00:00Z",
        misfire_policy: "run_once" }, deadline_at: "2026-08-20T12:00:00Z",
      stop_on_target_terminal: true, max_rounds: 1, max_model_calls: 0,
      max_elapsed_seconds: 3600, retry: { max_attempts: 3, initial_backoff_seconds: 5,
        max_backoff_seconds: 60 }, notification: "on_change" },
  };
}
