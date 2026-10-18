import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import type { APIClient } from "../api/client";
import type { ScheduledJobView } from "../api/types";
import { ScheduledTasksWorkspace } from "./scheduled-tasks-workspace";

it.each([false, true])("loads and controls the old active schedule beyond 100 records (retry=%s)", async (retry) => {
  const user = userEvent.setup();
  const jobs = Array.from({ length: 105 }, (_, index) => scheduledJob(index));
  let failNextPage = retry;
  const listScheduledJobs = vi.fn().mockImplementation(async (_run, _limit, _signal, cursor) => {
    if (cursor && failNextPage) {
      failNextPage = false;
      throw new Error("Older schedules unavailable");
    }
    return { protocol_version: "scheduled-job.v1", items: cursor ? jobs.slice(100) : jobs.slice(0, 100),
      page: { limit: 100, ...(cursor ? {} : { next_cursor: "opaque-older-schedules" }) } };
  });
  const transitionScheduledJob = vi.fn().mockImplementation(async (_run, id, action) => {
    const index = jobs.findIndex((job) => job.id === id);
    jobs[index] = { ...jobs[index], revision: jobs[index]!.revision + 1,
      status: action === "pause" ? "paused" : "cancelled" } as ScheduledJobView;
    return { job: jobs[index] };
  });
  const client = {
    hasScheduledJobControl: true, hasScheduledJobWorker: false,
    getPage: vi.fn().mockResolvedValue({ items: [], page: { limit: 100 } }),
    runtimeCapabilities: vi.fn().mockResolvedValue({ scheduled_job_worker: { enabled: false, state: "disabled" } }),
    listScheduledJobs, transitionScheduledJob,
    getScheduledJob: vi.fn().mockImplementation(async (id) => ({ snapshot: {
      job: jobs.find((job) => job.id === id), rounds: [], notifications: [], executions: [],
    } })),
  } as unknown as APIClient;
  const queries = new QueryClient({ defaultOptions: { queries: { retry: false }, mutations: { retry: false } } });
  render(<QueryClientProvider client={queries}><ScheduledTasksWorkspace client={client} /></QueryClientProvider>);
  expect(await screen.findByRole("button", { name: /scheduled-job-000/ })).toBeVisible();
  expect(screen.queryByRole("button", { name: /scheduled-job-104/ })).not.toBeInTheDocument();
  await user.click(screen.getByRole("button", { name: "Load more schedules" }));
  if (retry) {
    await screen.findByText("Older schedules unavailable");
    await user.click(screen.getByRole("button", { name: "Retry loading more schedules" }));
  }
  await user.click(await screen.findByRole("button", { name: /scheduled-job-104/ }));
  expect(listScheduledJobs).toHaveBeenCalledWith("", 100, expect.any(AbortSignal), "opaque-older-schedules");
  expect(screen.queryByRole("button", { name: "Load more schedules" })).not.toBeInTheDocument();
  await user.click(await screen.findByRole("button", { name: "Pause" }));
  await waitFor(() => expect(transitionScheduledJob).toHaveBeenCalledWith("run-older", "scheduled-job-104", "pause",
    { version: "scheduled-job-control.v1", expected_revision: 1 }, expect.any(String)));
  await screen.findByRole("button", { name: "Resume" });
  await user.click(screen.getByRole("button", { name: "Cancel" }));
  await waitFor(() => expect(transitionScheduledJob).toHaveBeenCalledWith("run-older", "scheduled-job-104", "cancel",
    { version: "scheduled-job-control.v1", expected_revision: 2 }, expect.any(String)));
});

function scheduledJob(index: number): ScheduledJobView {
  return {
    id: "scheduled-job-" + index.toString().padStart(3, "0"), owner_run_id: "run-older", owner_root_agent_id: "root-older",
    status: "active", revision: 1, active_lease_generation: 0, rounds_completed: 0, consecutive_unchanged: 0,
    model_calls: 0, last_event_sequence: 0, observation_consent_version: 1, execution_consent_version: 0,
    created_by: "test", created_at: "2026-10-18T09:00:00Z", updated_at: "2026-10-18T09:00:00Z",
    spec: { version: "scheduled-job.v1", target_run_id: "run-older", execution_mode: "read_only",
      schedule: { kind: "once", timezone: "UTC", anchor_at: "2026-10-18T11:00:00Z", misfire_policy: "run_once" },
      deadline_at: "2026-10-18T12:00:00Z", stop_on_target_terminal: true, max_rounds: 1, max_model_calls: 0,
      max_elapsed_seconds: 3600, retry: { max_attempts: 2, initial_backoff_seconds: 1, max_backoff_seconds: 10 },
      notification: "on_change" },
  };
}
