import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { cleanup, render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { APIClient } from "../../api/client";
import type { ScheduledJobDetailView, ScheduledJobView, ThreadView } from "../../api/types";
import { LocaleProvider } from "../../lib/locale";
import { V2AutomationsPage } from "./automations-page";

function makeThread(id: string, title: string, runID = `run-${id}`): ThreadView {
  return {
    id,
    title,
    workspace_id: "ws-main",
    mission_id: `mission-${id}`,
    active_run_id: runID,
    last_run_id: runID,
    protocol_version: "thread.v1",
    composer_state: "ready",
    status: "active",
    version: 1,
    created_at: "2026-11-18T00:00:00Z",
    updated_at: "2026-11-18T00:00:00Z",
  };
}

function makeScheduledJob(overrides: Partial<ScheduledJobView> = {}): ScheduledJobView {
  const id = overrides.id ?? "job-daily-check";
  const ownerRunID = overrides.owner_run_id ?? "run-thread-alpha";
  return {
    id,
    owner_run_id: ownerRunID,
    owner_root_agent_id: "agent-root",
    created_by: "operator",
    status: "active",
    revision: 1,
    rounds_completed: 3,
    consecutive_unchanged: 0,
    model_calls: 3,
    last_event_sequence: 10,
    active_lease_generation: 0,
    observation_consent_version: 0,
    execution_consent_version: 1,
    next_wake_at: "2026-11-19T01:00:00Z",
    last_result: "All 42 unit tests passed",
    created_at: "2026-11-17T00:00:00Z",
    updated_at: "2026-11-18T01:00:00Z",
    spec: {
      version: "scheduled-job.v1",
      target_run_id: ownerRunID,
      execution_mode: "automatic",
      execution_target: {
        kind: "thread",
        thread_id: "thread-alpha",
        prompt: "## 每日巡检\n运行回归测试并汇总结果",
      },
      schedule: {
        kind: "periodic",
        timezone: "Asia/Shanghai",
        anchor_at: "2026-11-18T01:00:00Z",
        interval_seconds: 86_400,
        misfire_policy: "run_once",
      },
      deadline_at: "2026-12-18T01:00:00Z",
      max_rounds: 30,
      max_model_calls: 0,
      max_elapsed_seconds: 30 * 86_400,
      stop_on_target_terminal: false,
      notification: "on_change",
      retry: {
        max_attempts: 3,
        initial_backoff_seconds: 5,
        max_backoff_seconds: 60,
      },
    },
    ...overrides,
  };
}

function mountStudio(
  client: Partial<APIClient>,
  props: { initialRunID?: string; initialThreadID?: string } = {},
) {
  const queryClient = new QueryClient({
    defaultOptions: { queries: { retry: false }, mutations: { retry: false } },
  });
  return render(
    <QueryClientProvider client={queryClient}>
      <LocaleProvider>
        <V2AutomationsPage client={client as APIClient} {...props} />
      </LocaleProvider>
    </QueryClientProvider>,
  );
}

describe("V2AutomationsPage", () => {
  beforeEach(() => {
    window.localStorage.setItem("prayu.locale.v1", "zh-CN");
  });

  afterEach(() => {
    cleanup();
    window.localStorage.clear();
  });

  it("renders the V2 Automations studio, preset chips, and live next-3-triggers preview without Cron", async () => {
    const user = userEvent.setup();
    const thread = makeThread("thread-alpha", "主干构建巡检会话");
    const activeJob = makeScheduledJob();

    const listScheduledJobs = vi.fn().mockResolvedValue({
      protocol_version: "scheduled-job.v1",
      items: [activeJob],
      page: { limit: 100 },
    });
    const getScheduledJob = vi.fn().mockResolvedValue({
      protocol_version: "scheduled-job.v1",
      snapshot: {
        job: activeJob,
        executions: [],
        rounds: [],
        notifications: [],
      },
    } satisfies ScheduledJobDetailView);

    mountStudio(
      {
        hasScheduledJobControl: true,
        hasThreadControl: true,
        listScheduledJobs,
        getScheduledJob,
        getPage: vi.fn().mockResolvedValue({ items: [thread], page: { limit: 100 }, requestID: "req" }),
        get: vi.fn().mockResolvedValue({ thread, active_run: { id: thread.active_run_id }, last_run: { id: thread.last_run_id }, runs: [] }),
      },
      { initialThreadID: "thread-alpha" },
    );

    expect(screen.getByRole("heading", { name: "自动化", level: 1 })).toBeVisible();
    expect(screen.getByRole("button", { name: "刷新自动化" })).toBeVisible();

    // Verify all 4 natural-language preset chips are available
    for (const label of ["每小时巡检", "每天早报 09:00", "每日构建检查 18:00", "每周一汇总 10:00"]) {
      expect(screen.getByRole("button", { name: label })).toBeVisible();
    }

    // Clicking a preset updates the live preview and renders 3 next trigger timestamps
    await user.click(screen.getByRole("button", { name: "每周一汇总 10:00" }));
    expect(screen.getByText(/每周一 10:00 执行/)).toBeVisible();
    const triggerList = screen.getByRole("list", { name: "未来 3 次触发时间预览" });
    expect(within(triggerList).getAllByRole("listitem")).toHaveLength(3);

    // Existing scheduled job appears in the list with Open Thread link
    expect(await screen.findByRole("heading", { name: "主干构建巡检会话", level: 2 })).toBeVisible();
    expect(screen.getByRole("link", { name: "查看关联会话" })).toHaveAttribute(
      "href",
      "#/threads/thread-alpha",
    );
  });

  it("opens the Full-Screen Editor, previews Markdown templates, and creates a scheduled automation via apiClient", async () => {
    const user = userEvent.setup();
    const thread = makeThread("thread-beta", "后端接口巡检");
    const createdJob = makeScheduledJob({
      id: "job-created-studio",
      owner_run_id: "run-thread-beta",
    });
    const createScheduledJob = vi.fn().mockResolvedValue({
      protocol_version: "scheduled-job-control.v1",
      action: "create",
      replayed: false,
      execution_started: false,
      authority_bypass: false,
      job: createdJob,
    });

    mountStudio({
      hasScheduledJobControl: true,
      hasThreadControl: true,
      hasExecutionPermissionControl: true,
      listScheduledJobs: vi.fn().mockResolvedValue({
        protocol_version: "scheduled-job.v1",
        items: [],
        page: { limit: 100 },
      }),
      getPage: vi.fn(async (path: string) => ({
        items: path === "/threads" ? [thread] : [{ id: "ws-main", name: "Core Workspace", created_at: "2026-11-18T00:00:00Z" }],
        page: { limit: 100 },
        requestID: path,
      }) as never),
      get: vi.fn().mockResolvedValue({
        thread,
        active_run: { id: "run-thread-beta" },
        last_run: { id: "run-thread-beta" },
        runs: [],
      }),
      availableModelRoutes: vi.fn().mockResolvedValue({
        protocol_version: "model_route_catalog.v1",
        generation: 1,
        routes: [
          {
            provider_id: "openai",
            provider_name: "OpenAI",
            model: "gpt-5",
            selectable: true,
          },
        ],
      }),
      createScheduledJob,
    });

    await screen.findByRole("option", { name: "后端接口巡检" });

    // Open Fullscreen Studio
    await user.click(screen.getByRole("button", { name: "展开全屏编辑器" }));
    const dialog = screen.getByRole("dialog", { name: "全屏自动化编排工作台" });
    expect(dialog).toBeVisible();

    // Apply reusable template & verify live Markdown preview toggle
    await user.click(within(dialog).getByRole("button", { name: "构建与测试巡检" }));
    await user.click(within(dialog).getByRole("button", { name: "实时预览" }));
    expect(
      within(within(dialog).getByLabelText("Markdown 预览内容")).getByRole("heading", {
        name: "每日构建与回归检查",
      }),
    ).toBeVisible();

    // Switch back to Write mode and customize natural-language schedule
    await user.click(within(dialog).getByRole("button", { name: "编写 Markdown" }));
    const scheduleInput = within(dialog).getByLabelText("自然语言定时规则（免 Cron）");
    await user.clear(scheduleInput);
    await user.type(scheduleInput, "每 30 分钟");
    expect(within(dialog).getAllByText(/每 30 分钟执行一次/)[0]).toBeVisible();

    // Bind existing thread and submit
    await user.selectOptions(within(dialog).getByLabelText("绑定已有会话"), "thread-beta");
    await user.click(within(dialog).getByRole("button", { name: "保存并启用自动化" }));

    await waitFor(() => expect(createScheduledJob).toHaveBeenCalledTimes(1));
    const [calledRunID, calledBody, calledKey] = createScheduledJob.mock.calls[0]!;
    expect(calledRunID).toBe("run-thread-beta");
    expect(calledBody).toMatchObject({
      version: "scheduled-job.v1",
      execution_mode: "automatic",
      execution_consent_version: 1,
      schedule: {
        kind: "periodic",
        interval_seconds: 1800,
        misfire_policy: "run_once",
      },
      execution_target: {
        kind: "thread",
        thread_id: "thread-beta",
      },
    });
    expect(calledKey).toMatch(/^v2-scheduled-job-create-/u);
    await waitFor(() => expect(screen.queryByRole("dialog")).not.toBeInTheDocument());
  });

  it("filters Historical Run Records by status and supports Run Now, Pause, and Resume actions", async () => {
    const user = userEvent.setup();
    const thread = makeThread("thread-alpha", "夜间全量回归");
    const activeJob = makeScheduledJob({
      id: "job-active",
      status: "active",
      rounds_completed: 5,
    });
    const pausedJob = makeScheduledJob({
      id: "job-paused",
      owner_run_id: "run-paused",
      status: "paused",
      rounds_completed: 2,
      spec: {
        ...makeScheduledJob().spec,
        target_run_id: "run-paused",
        execution_target: {
          kind: "thread",
          thread_id: "thread-alpha",
          prompt: "暂停中的巡检计划",
        },
      },
    });

    const getScheduledJob = vi.fn(async (jobID: string): Promise<ScheduledJobDetailView> => {
      if (jobID === "job-active") {
        return {
          protocol_version: "scheduled-job.v1",
          snapshot: {
            job: activeJob,
            executions: [
              {
                job_id: "job-active",
                occurrence_at: "2026-11-18T02:00:00Z",
                created_at: "2026-11-18T02:00:00Z",
                updated_at: "2026-11-18T02:02:00Z",
                state: "completed",
                run_id: "run-exec-ok",
                result: "回归套件全部通过",
              },
              {
                job_id: "job-active",
                occurrence_at: "2026-11-18T03:00:00Z",
                created_at: "2026-11-18T03:00:00Z",
                updated_at: "2026-11-18T03:01:00Z",
                state: "failed",
                run_id: "run-exec-fail",
                error_code: "E2E_ASSERTION_TIMEOUT",
              },
            ],
            rounds: [],
            notifications: [],
          },
        };
      }
      return {
        protocol_version: "scheduled-job.v1",
        snapshot: {
          job: pausedJob,
          executions: [],
          rounds: [],
          notifications: [],
        },
      };
    });

    const submitThreadTurn = vi.fn().mockResolvedValue({ accepted: true });
    const transitionScheduledJob = vi.fn().mockImplementation(
      async (_runID: string, jobID: string, action: "pause" | "resume") => ({
        protocol_version: "scheduled-job-control.v1",
        action,
        replayed: false,
        execution_started: false,
        authority_bypass: false,
        job: { ...(jobID === "job-active" ? activeJob : pausedJob), status: action === "pause" ? "paused" : "active" },
      }),
    );

    mountStudio({
      hasScheduledJobControl: true,
      hasThreadControl: true,
      listScheduledJobs: vi.fn().mockResolvedValue({
        protocol_version: "scheduled-job.v1",
        items: [activeJob, pausedJob],
        page: { limit: 100 },
      }),
      getScheduledJob,
      getPage: vi.fn().mockResolvedValue({ items: [thread], page: { limit: 100 }, requestID: "req" }),
      submitThreadTurn,
      transitionScheduledJob,
    });

    // Switch to Historical Run Records tab
    const historyTab = await screen.findByRole("button", { name: /历史运行记录/ });
    await user.click(historyTab);

    const historySection = screen.getByRole("region", { name: "历史运行记录面板" });
    await waitFor(() =>
      expect(within(historySection).getByText("E2E_ASSERTION_TIMEOUT")).toBeVisible(),
    );
    expect(within(historySection).getByText("回归套件全部通过")).toBeVisible();

    // Filter by "失败" (Failed)
    await user.click(within(historySection).getByRole("button", { name: /失败 \(1\)/ }));
    expect(within(historySection).getByText("E2E_ASSERTION_TIMEOUT")).toBeVisible();
    expect(within(historySection).queryByText("回归套件全部通过")).not.toBeInTheDocument();

    // Trigger manual "立即执行一次"
    await user.click(within(historySection).getByRole("button", { name: "立即执行一次" }));
    await waitFor(() =>
      expect(submitThreadTurn).toHaveBeenCalledWith(
        "thread-alpha",
        expect.objectContaining({ version: "thread_message_submission.v1" }),
        expect.stringMatching(/^v2-scheduled-job-run-now-turn-/u),
      ),
    );

    // Filter by "已暂停" (Paused) and click Resume
    await user.click(within(historySection).getByRole("button", { name: /已暂停 \(1\)/ }));
    await user.click(within(historySection).getByRole("button", { name: "恢复" }));
    await waitFor(() =>
      expect(transitionScheduledJob).toHaveBeenCalledWith(
        "run-paused",
        "job-paused",
        "resume",
        { version: "scheduled-job-control.v1", expected_revision: 1 },
        expect.stringMatching(/^v2-scheduled-job-resume-/u),
      ),
    );
  });
});
