import { StrictMode } from "react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { cleanup, render, screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { readFileSync } from "node:fs";
import { resolve } from "node:path";
import type { APIClient } from "../../api/client";
import type { ApprovalQueueView, ThreadDetailView, ThreadExecutionView, ThreadTranscriptItemView, ThreadView, WorkspaceView } from "../../api/types";
import { LocaleProvider, useLocale } from "../../lib/locale";
import { v2QueryKeys } from "../query-keys";
import { V2RecoveryProvider, resetV2RecoveryStorageMemory } from "../recovery-storage";
import { V2Sidebar } from "./sidebar";
import {
  DEFAULT_SIDEBAR_CUSTOM_PREFS,
  deriveThreadActivityStatus,
  extractCachedThreadSignals,
  normalizeSidebarCustomPrefs,
  normalizeSidebarViewMode,
} from "./sidebar-smart-groups";

const workspaces: WorkspaceView[] = [
  { id: "ws-alpha", name: "前端工作台", created_at: "2026-11-01T00:00:00Z" },
  { id: "ws-beta", name: "核心引擎", created_at: "2026-11-02T00:00:00Z" },
];

function makeThread(overrides: Partial<ThreadView> & { id: string; title: string }): ThreadView {
  const ended = ["completed", "failed", "cancelled"].includes(overrides.execution_state ?? "");
  return {
    protocol_version: "thread.v1",
    workspace_id: "ws-alpha",
    project_workspace_id: "ws-alpha",
    mission_id: `mission-${overrides.id}`,
    active_run_id: ended ? undefined : `run-${overrides.id}`,
    last_run_id: overrides.active_run_id ?? `run-${overrides.id}`,
    status: "active",
    composer_state: ended ? "successor_required" : overrides.execution_state === "waiting_approval" ? "waiting_approval" : "ready",
    execution_state: "idle",
    version: 1,
    created_at: "2026-11-10T08:00:00Z",
    updated_at: "2026-11-10T09:30:00Z",
    ...overrides,
  };
}

async function showProjectView(user: ReturnType<typeof userEvent.setup>) {
  await user.click(screen.getByLabelText("分组选项"));
  await user.click(screen.getByRole("button", { name: "按项目分组" }));
}

async function showRowDetails(user: ReturnType<typeof userEvent.setup>) {
  await user.click(screen.getByRole("button", { name: "自定义边栏" }));
  const dialog = screen.getByRole("dialog", { name: "自定义边栏" });
  await user.click(within(dialog).getByLabelText("显示智能体实时动作详情"));
  await user.click(within(dialog).getByLabelText("显示所属项目标签"));
  await user.keyboard("{Escape}");
}

function activeActionCache(commandThreadID: string, waitingThreadID: string, command: string) {
  const client = new QueryClient();
  const runID = `run-${commandThreadID}`;
  const item: ThreadTranscriptItemView = {
    version: "thread_transcript.v1", id: "call", canonical_id: "call", durable_call_id: "call", run_id: runID,
    run_ordinal: 1, sequence: 3, created_at: "2026-11-10T09:30:00Z", stage: "running", kind: "tool_call",
    title: "Command", tool_name: "command_runtime", activity_type: "execute", source: "harness", durable: true,
    provisional: false, instruction_authorized: false, verifiable: true,
    activity_summary: { version: "thread_activity_summary.v1", activity_ref: "activity", command, status: "running",
      command_count: 1, duration_milliseconds: 0 },
  };
  client.setQueryData(v2QueryKeys.transcript(commandThreadID), { pages: [{ items: [item] }], pageParams: [""] });
  client.setQueryData(v2QueryKeys.approvals(`run-${waitingThreadID}`), {
    run_id: `run-${waitingThreadID}`, items: [{ id: "first", status: "pending" }, { id: "second", status: "pending" }],
  });
  return client;
}

function LocaleToggle() {
  const { locale, setLocale } = useLocale();
  return (
    <button onClick={() => setLocale(locale === "zh-CN" ? "en-US" : "zh-CN")} type="button">
      Toggle locale
    </button>
  );
}

function renderSidebar({
  threads,
  unreadThreadIDs,
  onVisibleChatOrder = vi.fn(),
  onNewInProject = vi.fn(),
  onSelectThread = vi.fn(),
  scopeID = "store-smart-sidebar",
  baseURL = "/api/v1",
  apiClient, hasMore = false,
  queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } }),
}: {
  threads: ThreadView[];
  unreadThreadIDs?: ReadonlySet<string>;
  onVisibleChatOrder?: (ids: readonly string[]) => void;
  onNewInProject?: (projectID: string) => void;
  onSelectThread?: (threadID: string) => void;
  scopeID?: string;
  baseURL?: string;
  queryClient?: QueryClient;
  apiClient?: APIClient;
  hasMore?: boolean;
}) {
  const modeKey = `v2_recovery.v1:${encodeURIComponent(window.location.origin)}:${encodeURIComponent(baseURL)}:${scopeID}:sidebar-view-mode`;
  if (!localStorage.getItem(modeKey)) localStorage.setItem(modeKey, JSON.stringify({ version: "v2_recovery.v1", value: "priority" }));
  return render(
    <StrictMode><QueryClientProvider client={queryClient}>
      <LocaleProvider>
        <V2RecoveryProvider client={{ baseURL } as Pick<APIClient, "baseURL">} scopeID={scopeID}>
          <LocaleToggle />
          <V2Sidebar
            client={apiClient} hasMore={hasMore}
            onArchive={vi.fn()}
            onNewConversation={vi.fn()}
            onNewInProject={onNewInProject}
            onOpenModels={vi.fn()}
            onOpenSettings={vi.fn()}
            onSearchOpen={vi.fn()}
            onSelectThread={onSelectThread}
            onVisibleChatOrder={onVisibleChatOrder}
            searchOpen={false}
            selectedThreadID={threads[0]?.id ?? ""}
            threads={threads}
            unreadThreadIDs={unreadThreadIDs}
            workspaces={workspaces}
          />
        </V2RecoveryProvider>
      </LocaleProvider>
    </QueryClientProvider></StrictMode>,
  );
}

beforeEach(() => {
  localStorage.clear();
  resetV2RecoveryStorageMemory();
  localStorage.setItem("prayu.locale.v1", "zh-CN");
  vi.clearAllMocks();
});

afterEach(() => {
  cleanup();
});

describe("deriveThreadActivityStatus & QueryClient cache extraction", () => {
  it("distinguishes waiting_input, running_command, thinking, unread, and idle with localized labels", () => {
    const base = makeThread({ id: "t-status", title: "Status probe", execution_state: "running" });

    const waiting = deriveThreadActivityStatus({ ...base, composer_state: "waiting_approval" }, { pendingApprovals: 3 });
    expect(waiting).toMatchObject({
      kind: "waiting_input",
      bucket: "needs_input",
      tone: "amber",
      pulse: false,
      label: "等待回复",
      detail: "3 项待审批",
    });

    const command = deriveThreadActivityStatus(base, {
      activeTool: { activityType: "execute", command: "npx vitest run src/v2/sidebar" },
    });
    expect(command).toMatchObject({
      kind: "running_command",
      bucket: "running",
      tone: "cyan",
      pulse: true,
      label: "执行命令",
      detail: "npx vitest run src/v2/sidebar",
    });

    const browserTool = deriveThreadActivityStatus(base, {
      activeTool: { kind: "browser", toolName: "cdp_click" },
    });
    expect(browserTool).toMatchObject({
      kind: "running_command",
      detail: "浏览器操作",
    });

    const thinking = deriveThreadActivityStatus(base);
    expect(thinking).toMatchObject({
      kind: "thinking",
      bucket: "running",
      tone: "indigo",
      pulse: true,
      label: "思考中",
    });

    const unread = deriveThreadActivityStatus(
      makeThread({ id: "t-done", title: "Done", execution_state: "completed" }),
      { isUnread: true },
    );
    expect(unread).toMatchObject({
      kind: "unread",
      bucket: "unread",
      tone: "blue",
      pulse: false,
      label: "未读新进展",
      detail: "已完成",
    });

    const idle = deriveThreadActivityStatus(makeThread({ id: "t-idle", title: "Idle", execution_state: "idle" }));
    expect(idle).toMatchObject({
      kind: "idle",
      bucket: "idle",
      tone: "neutral",
      pulse: false,
      label: "空闲",
    });
  });

  it("extracts running tool calls and pending approvals from QueryClient cache without network calls", () => {
    const queryClient = new QueryClient();
    const thread = makeThread({ id: "cached-thread", title: "Cached thread", active_run_id: "run-live", execution_state: "running" });

    queryClient.setQueryData<ThreadDetailView>(v2QueryKeys.thread(thread.id), {
      thread,
      active_run: { id: "run-live", status: "running" } as ThreadDetailView["active_run"],
    } as ThreadDetailView);

    queryClient.setQueryData(v2QueryKeys.transcript(thread.id), {
      pages: [{
        items: [
          {
            id: "m1",
            run_id: "run-live",
            sequence: 1,
            kind: "model_call",
            stage: "result",
            durable: true,
            provisional: false,
          } as unknown as ThreadTranscriptItemView,
          {
            id: "t1",
            run_id: "run-live",
            sequence: 2,
            kind: "tool_call",
            durable_call_id: "call-1",
            tool_name: "run_verify",
            activity_type: "verify",
            stage: "running",
            durable: true,
            provisional: false,
          } as unknown as ThreadTranscriptItemView,
        ],
      }],
    });

    const runningSignals = extractCachedThreadSignals(queryClient, thread, false, false);
    expect(deriveThreadActivityStatus(thread, runningSignals)).toMatchObject({
      kind: "running_command",
      bucket: "running",
      detail: "运行验证检查",
    });

    queryClient.setQueryData<ApprovalQueueView>(v2QueryKeys.approvals("run-live"), {
      protocol_version: "approval_queue.v1",
      run_id: "run-live",
      truncated: false,
      capability_grant: false,
      process_execution_enabled: true,
      session_grant_created: false,
      items: [{ id: "approval-1", status: "pending" } as ApprovalQueueView["items"][number]],
    });
    const waiting: ThreadView = { ...thread, composer_state: "waiting_approval" };
    const blockedSignals = extractCachedThreadSignals(queryClient, waiting, false, false);
    expect(deriveThreadActivityStatus(waiting, blockedSignals)).toMatchObject({
      kind: "waiting_input",
      bucket: "needs_input",
      detail: "1 项待审批",
    });
  });

  it("normalizes malformed persisted preferences safely", () => {
    expect(normalizeSidebarViewMode("project")).toBe("project");
    expect(normalizeSidebarViewMode({ bad: true })).toBe("project");
    expect(normalizeSidebarCustomPrefs(null)).toEqual(DEFAULT_SIDEBAR_CUSTOM_PREFS);
    expect(
      normalizeSidebarCustomPrefs({
        defaultLayout: "project",
        density: "compact",
        idleLimit: 5,
        autoCollapseIdle: true,
        showActivitySubtitle: false,
        visibleGroups: { idle: false, running: "invalid" },
      }),
    ).toEqual({
      defaultLayout: "project",
      density: "compact",
      idleLimit: 5,
      autoCollapseIdle: true,
      showActivitySubtitle: false,
      showWorkspaceBadge: false,
      visibleGroups: { needs_input: true, running: true, unread: true, idle: false },
    });
  });
});

describe("V2Sidebar smart priority view, project pinning, and customization", () => {
  it("groups sessions into 4 priority buckets with live status pills, workspace badges, and collapsible chat order", async () => {
    const user = userEvent.setup();
    const onVisibleChatOrder = vi.fn();
    const threads = [
      makeThread({ id: "idle-1", title: "架构草稿整理", execution_state: "idle", project_workspace_id: "ws-beta" }),
      makeThread({ id: "unread-1", title: "测试报告已生成", execution_state: "completed", project_workspace_id: "ws-alpha" }),
      makeThread({ id: "cmd-1", title: "运行端到端检查", execution_state: "running", project_workspace_id: "ws-beta" }),
      makeThread({ id: "think-1", title: "分析依赖冲突", execution_state: "running", project_workspace_id: "ws-alpha" }),
      makeThread({ id: "wait-1", title: "请求执行数据库迁移", execution_state: "waiting_approval", project_workspace_id: "ws-alpha" }),
    ];

    renderSidebar({
      threads,
      unreadThreadIDs: new Set(["unread-1"]),
      queryClient: activeActionCache("cmd-1", "wait-1", "go test ./..."),
      onVisibleChatOrder,
    });

    await showRowDetails(user);
    const groupRegions = screen
      .getAllByRole("region")
      .map((el) => el.getAttribute("aria-label"))
      .filter((label) => ["需用户输入", "运行中", "未读", "空闲"].includes(label ?? ""));
    expect(groupRegions).toEqual(["需用户输入", "运行中", "未读", "空闲"]);

    expect(onVisibleChatOrder).toHaveBeenLastCalledWith([
      "wait-1",
      "cmd-1",
      "think-1",
      "unread-1",
      "idle-1",
    ]);

    expect(within(screen.getByRole("region", { name: "需用户输入" })).getByText(/等待回复 · 2 项待审批/)).toBeInTheDocument();
    expect(within(screen.getByRole("region", { name: "运行中" })).getByText(/执行命令 · go test \.\/\.\.\./)).toBeInTheDocument();
    expect(within(screen.getByRole("region", { name: "运行中" })).getByText("思考中")).toBeInTheDocument();
    expect(within(screen.getByRole("region", { name: "未读" })).getByText(/未读新进展/)).toBeInTheDocument();
    expect(within(screen.getByRole("region", { name: "空闲" })).getByText("核心引擎")).toBeInTheDocument();

    // Collapse Running bucket and verify keyboard navigation order updates
    await user.click(screen.getByRole("button", { name: "运行中 2" }));
    expect(screen.queryByRole("button", { name: "运行端到端检查" })).not.toBeInTheDocument();
    expect(onVisibleChatOrder).toHaveBeenLastCalledWith(["wait-1", "unread-1", "idle-1"]);

    // Switch language at runtime and verify English translations
    await user.click(screen.getByRole("button", { name: "Toggle locale" }));
    expect(screen.getByRole("region", { name: "Needs input" })).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Running 2" })).toHaveAttribute("aria-expanded", "false");
    expect(screen.getByRole("region", { name: "Unread" })).toBeInTheDocument();
    expect(screen.getByRole("region", { name: "Idle" })).toBeInTheDocument();
  });

  it("switches between Smart Priority and Project views, pins/filters projects from both views, and persists across remount", async () => {
    const user = userEvent.setup();
    const onNewInProject = vi.fn();
    const threads = [
      makeThread({ id: "alpha-chat", title: "界面优化", project_workspace_id: "ws-alpha", execution_state: "idle" }),
      makeThread({ id: "beta-chat", title: "调度器重构", project_workspace_id: "ws-beta", execution_state: "running" }),
    ];

    const first = renderSidebar({ threads, onNewInProject });
    expect(screen.getByRole("button", { name: "优先级" })).toHaveAttribute("aria-expanded", "true");

    // Pin "核心引擎" directly from the top pinned-projects bar in Smart Priority view
    await user.click(screen.getByLabelText("管理置顶项目"));
    await user.click(screen.getByRole("button", { name: "核心引擎" }));

    const pinnedBar = screen.getByRole("region", { name: "置顶项目" });
    const filterBeta = within(pinnedBar).getByRole("button", { name: "筛选项目 核心引擎" });
    expect(filterBeta).toHaveAttribute("aria-pressed", "false");

    // Filter Smart Priority view to only the pinned project
    await user.click(filterBeta);
    expect(filterBeta).toHaveAttribute("aria-pressed", "true");
    expect(screen.getByRole("button", { name: "调度器重构" })).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "界面优化" })).not.toBeInTheDocument();

    // Start a new chat in the pinned project right from the quick bar
    await user.click(within(pinnedBar).getByRole("button", { name: "在置顶项目 核心引擎 中新建聊天" }));
    expect(onNewInProject).toHaveBeenCalledWith("ws-beta");

    // Clear filter and switch to By Project view
    await user.click(within(pinnedBar).getByRole("button", { name: "清除筛选" }));
    await showProjectView(user);
    expect(screen.getByRole("button", { name: "项目" })).toHaveAttribute("aria-expanded", "true");

    // Verify pinned project "核心引擎" is anchored first in Project view
    const projectRegions = screen.getAllByRole("region", { name: /^项目 / }).map((el) => el.getAttribute("aria-label"));
    expect(projectRegions).toEqual(["项目 核心引擎", "项目 前端工作台"]);

    // Unmount and remount to verify persistence of view mode and project pins
    first.unmount();
    renderSidebar({ threads, onNewInProject });
    expect(screen.getByRole("button", { name: "项目" })).toHaveAttribute("aria-expanded", "true");
    expect(screen.getAllByRole("region", { name: /^项目 / })[0]).toHaveAccessibleName("项目 核心引擎");

    // Unpin via the explicit row-level Pin button on ProjectHeader
    await user.click(screen.getByRole("button", { name: "取消置顶项目 核心引擎" }));
    expect(screen.getAllByRole("region", { name: /^项目 / }).map((el) => el.getAttribute("aria-label"))).toEqual([
      "项目 前端工作台",
      "项目 核心引擎",
    ]);
  });

  it("shows the regular history pagination while the selected project has no loaded chats", async () => {
    const user = userEvent.setup();
    renderSidebar({ threads: [], hasMore: true });
    await user.click(screen.getByLabelText("管理置顶项目"));
    await user.click(screen.getByRole("button", { name: "核心引擎" }));
    await user.click(screen.getByRole("button", { name: "筛选项目 核心引擎" }));
    expect(screen.getByText("已加载的记录中暂无该项目的会话。")).toBeVisible();
    expect(screen.getByRole("button", { name: "加载更早对话" })).toBeVisible();
    expect(screen.queryByText("所选置顶项目暂无会话")).not.toBeInTheDocument();
  });

  it("keeps the selected project's empty state provisional until its pinned chat pages are loaded", async () => {
    const user = userEvent.setup();
    const chat = makeThread({ id: "later", title: "稍早的置顶聊天", project_workspace_id: "ws-beta", pinned: true });
    const getPage = vi.fn(async (_path: string, _query: unknown, cursor: string) => ({
      items: cursor ? [chat] : [], page: { next_cursor: cursor ? "" : "next-pinned" },
    }));
    renderSidebar({ threads: [], apiClient: { getPage } as unknown as APIClient });
    await user.click(screen.getByLabelText("管理置顶项目"));
    await user.click(screen.getByRole("button", { name: "核心引擎" }));
    await user.click(screen.getByRole("button", { name: "筛选项目 核心引擎" }));
    expect(screen.queryByText("所选置顶项目暂无会话")).not.toBeInTheDocument();
    expect(screen.getByText("已加载的记录中暂无该项目的会话。")).toBeVisible();
    await showProjectView(user);
    const project = within(screen.getByRole("region", { name: "项目 核心引擎" }));
    expect(project.getByText("已加载的记录中暂无该项目的会话。")).toBeVisible();
    await user.click(screen.getByRole("button", { name: "加载更多置顶聊天" }));
    const pins = within(await screen.findByRole("region", { name: "置顶聊天" }));
    expect(await pins.findByRole("button", { name: chat.title })).toBeVisible();
    expect(project.queryByText("暂无聊天")).not.toBeInTheDocument();
  });

  it("customizes density, subtitle/badge visibility, idle auto-collapse, group visibility, and idle limit with persistence", async () => {
    const user = userEvent.setup();
    const idleThreads = Array.from({ length: 7 }, (_, index) =>
      makeThread({
        id: `idle-${index + 1}`,
        title: `空闲任务 ${index + 1}`,
        execution_state: "idle",
        project_workspace_id: "ws-alpha",
      }),
    );
    const runningThread = makeThread({
      id: "run-1",
      title: "实时编译检查",
      execution_state: "running",
      project_workspace_id: "ws-beta",
    });

    const first = renderSidebar({ threads: [runningThread, ...idleThreads] });
    expect(screen.queryByText("思考中")).not.toBeInTheDocument();
    expect(document.querySelector(".v2-thread-project-badge")).not.toBeInTheDocument();
    await showRowDetails(user);
    const sidebar = document.querySelector(".v2-sidebar")!;
    expect(sidebar).toHaveAttribute("data-sidebar-density", "comfortable");
    expect(screen.getByText("思考中")).toBeInTheDocument();
    expect(screen.getAllByText("前端工作台").length).toBeGreaterThan(0);

    // Open Customize Sidebar popover
    await user.click(screen.getByRole("button", { name: "自定义边栏" }));
    const dialog = screen.getByRole("dialog", { name: "自定义边栏" });

    // Switch to Compact density
    await user.click(within(dialog).getByRole("button", { name: "紧凑" }));
    expect(sidebar).toHaveClass("is-compact");
    expect(sidebar).toHaveAttribute("data-sidebar-density", "compact");

    // Cap idle sessions to 5 and verify clicking outside light-dismisses the popover
    await user.click(within(dialog).getByRole("button", { name: "5" }));
    expect(screen.getByRole("button", { name: "空闲任务 5" })).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "空闲任务 6" })).not.toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: "显示其余 2 条空闲会话" }));
    expect(screen.getByRole("button", { name: "空闲任务 7" })).toBeInTheDocument();
    expect(screen.queryByRole("dialog", { name: "自定义边栏" })).not.toBeInTheDocument();

    // Reopen Customize Sidebar popover to adjust row details and group visibility
    await user.click(screen.getByRole("button", { name: "自定义边栏" }));
    const reopenedDialog = screen.getByRole("dialog", { name: "自定义边栏" });

    // Hide live activity subtitles and project badges
    await user.click(within(reopenedDialog).getByLabelText("显示智能体实时动作详情"));
    await user.click(within(reopenedDialog).getByLabelText("显示所属项目标签"));
    expect(screen.queryByText("思考中")).not.toBeInTheDocument();
    expect(document.querySelector(".v2-thread-project-badge")).not.toBeInTheDocument();

    // Auto-collapse Idle group
    await user.click(within(reopenedDialog).getByLabelText("自动折叠「空闲」分组"));
    expect(screen.getByRole("button", { name: "空闲 7" })).toHaveAttribute("aria-expanded", "false");

    // Hide Idle group completely
    await user.click(within(reopenedDialog).getByLabelText("空闲"));
    expect(screen.queryByRole("region", { name: "空闲" })).not.toBeInTheDocument();
    expect(screen.getByText("已按边栏设置隐藏 7 条会话")).toBeInTheDocument();

    // Dismiss with Escape and verify focus returns to trigger
    await user.keyboard("{Escape}");
    expect(screen.queryByRole("dialog", { name: "自定义边栏" })).not.toBeInTheDocument();
    expect(screen.getByRole("button", { name: "自定义边栏" })).toHaveFocus();

    // Remount and verify all custom preferences persisted
    first.unmount();
    renderSidebar({ threads: [runningThread, ...idleThreads] });
    expect(document.querySelector(".v2-sidebar")).toHaveClass("is-compact");
    expect(screen.queryByText("思考中")).not.toBeInTheDocument();
    expect(screen.queryByRole("region", { name: "空闲" })).not.toBeInTheDocument();
    expect(screen.getByText("已按边栏设置隐藏 7 条会话")).toBeInTheDocument();
  });

  it("ignores stale cached idle execution state and mismatched cached runs when a thread starts a new run", () => {
    const queryClient = new QueryClient();
    const runningThread = makeThread({
      id: "t-stale-cache",
      title: "New running turn",
      active_run_id: "run-B",
      last_run_id: "run-B",
      execution_state: "running",
      updated_at: "2026-11-25T00:00:00Z",
    });

    // Previous turn left an unobserved idle execution state and completed run-A in QueryClient cache
    queryClient.setQueryData<ThreadExecutionView>(v2QueryKeys.execution(runningThread.id), {
      version: "thread_execution.v1",
      thread_id: runningThread.id,
      state: "idle",
      queued_messages: 0,
      capability_grant: false,
    });
    queryClient.setQueryData<ThreadDetailView>(v2QueryKeys.thread(runningThread.id), {
      thread: { ...runningThread, active_run_id: undefined, last_run_id: "run-A", execution_state: "completed", composer_state: "successor_required" },
      last_run: { id: "run-A", status: "completed" } as ThreadDetailView["last_run"],
    } as ThreadDetailView);
    queryClient.setQueryData<ApprovalQueueView>(v2QueryKeys.approvals("run-A"), {
      protocol_version: "approval_queue.v1",
      run_id: "run-A",
      truncated: false,
      capability_grant: false,
      process_execution_enabled: true,
      session_grant_created: false,
      items: [{ id: "stale-approval" } as ApprovalQueueView["items"][number]],
    });

    const signals = extractCachedThreadSignals(queryClient, runningThread, false, false);
    expect(signals.pendingApprovals).toBeUndefined();
    expect(deriveThreadActivityStatus(runningThread, signals).bucket).toBe("running");
  });

  it("clears active project filter when its project is unpinned via header or picker, while preserving unrelated filters", async () => {
    const user = userEvent.setup();
    const threads = [
      makeThread({ id: "alpha-chat", title: "前端任务", project_workspace_id: "ws-alpha", execution_state: "idle" }),
      makeThread({ id: "beta-chat", title: "后端任务", project_workspace_id: "ws-beta", execution_state: "idle" }),
    ];

    renderSidebar({ threads });

    // Pin both projects and filter to ws-beta
    await user.click(screen.getByLabelText("管理置顶项目"));
    await user.click(screen.getByRole("button", { name: "前端工作台" }));
    await user.click(screen.getByRole("button", { name: "核心引擎" }));

    const pinnedBar = screen.getByRole("region", { name: "置顶项目" });
    await user.click(within(pinnedBar).getByRole("button", { name: "筛选项目 核心引擎" }));
    expect(screen.queryByRole("button", { name: "前端任务" })).not.toBeInTheDocument();

    // Unpinning unrelated ws-alpha keeps ws-beta filter active
    await user.click(within(pinnedBar).getByRole("button", { name: "取消置顶栏中的 前端工作台" }));
    expect(screen.queryByRole("button", { name: "前端任务" })).not.toBeInTheDocument();

    // Switch to Project view and unpin active ws-beta via ProjectHeader pin button
    await showProjectView(user);
    expect(screen.queryByRole("region", { name: "项目 前端工作台" })).not.toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: "取消置顶项目 核心引擎" }));

    // Filter clears automatically so other projects reappear
    expect(screen.getByRole("region", { name: "项目 前端工作台" })).toBeInTheDocument();
    expect(screen.getByRole("region", { name: "项目 核心引擎" })).toBeInTheDocument();
  });

  it("exposes accessible activity descriptions on thread rows and collapsed project headers, and uses zero hex literals in CSS", async () => {
    const user = userEvent.setup();
    const threads = [
      makeThread({ id: "t-approvals", title: "审批阻塞任务", project_workspace_id: "ws-alpha", composer_state: "waiting_approval", execution_state: "running" }),
      makeThread({ id: "t-cmd", title: "命令执行任务", project_workspace_id: "ws-alpha", execution_state: "running" }),
      makeThread({ id: "t-unread", title: "未读完成任务", project_workspace_id: "ws-alpha", execution_state: "completed" }),
    ];

    renderSidebar({
      threads,
      unreadThreadIDs: new Set(["t-unread"]),
      queryClient: activeActionCache("t-cmd", "t-approvals", "npm test"),
    });

    await showRowDetails(user);
    expect(screen.getByRole("button", { name: "审批阻塞任务" })).toHaveAccessibleDescription("执行中 等待回复 · 2 项待审批");
    expect(screen.getByRole("button", { name: "命令执行任务" })).toHaveAccessibleDescription("执行中 执行命令 · npm test");

    await showProjectView(user);
    const projectRegion = screen.getByRole("region", { name: "项目 前端工作台" });
    const projectToggle = within(projectRegion).getByRole("button", { name: "前端工作台" });
    await user.click(projectToggle);
    expect(projectToggle).toHaveAttribute("aria-expanded", "false");
    expect(projectToggle).toHaveAccessibleDescription("1 项需用户输入，1 项运行中，1 项未读");

    const css = readFileSync(resolve(import.meta.dirname, "./project-sidebar.css"), "utf8");
    expect(css).not.toMatch(/#[0-9a-fA-F]{3,8}\b/);
  });
});
