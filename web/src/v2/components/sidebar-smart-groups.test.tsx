import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { cleanup, render, screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { APIClient } from "../../api/client";
import type { ApprovalQueueView, ThreadDetailView, ThreadTranscriptItemView, ThreadView, WorkspaceView } from "../../api/types";
import { LocaleProvider, useLocale } from "../../lib/locale";
import { v2QueryKeys } from "../query-keys";
import { V2RecoveryProvider, resetV2RecoveryStorageMemory } from "../recovery-storage";
import { V2Sidebar } from "./sidebar";
import {
  DEFAULT_SIDEBAR_CUSTOM_PREFS,
  classifyThreadPriorityBucket,
  deriveThreadActivityStatus,
  extractCachedThreadSignals,
  groupThreadsByPriority,
  normalizeSidebarCustomPrefs,
  normalizeSidebarViewMode,
  type ThreadLiveSignals,
} from "./sidebar-smart-groups";

const workspaces: WorkspaceView[] = [
  { id: "ws-alpha", name: "前端工作台", created_at: "2026-11-01T00:00:00Z" },
  { id: "ws-beta", name: "核心引擎", created_at: "2026-11-02T00:00:00Z" },
];

function makeThread(overrides: Partial<ThreadView> & { id: string; title: string }): ThreadView {
  return {
    protocol_version: "thread.v1",
    workspace_id: "ws-alpha",
    project_workspace_id: "ws-alpha",
    mission_id: `mission-${overrides.id}`,
    active_run_id: `run-${overrides.id}`,
    last_run_id: `run-${overrides.id}`,
    status: "active",
    composer_state: "ready",
    execution_state: "idle",
    version: 1,
    created_at: "2026-11-10T08:00:00Z",
    updated_at: "2026-11-10T09:30:00Z",
    ...overrides,
  };
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
  liveSignalsByThread,
  onVisibleChatOrder = vi.fn(),
  onNewInProject = vi.fn(),
  onSelectThread = vi.fn(),
  scopeID = "store-smart-sidebar",
  baseURL = "/api/v1",
  queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } }),
}: {
  threads: ThreadView[];
  unreadThreadIDs?: ReadonlySet<string>;
  liveSignalsByThread?: Record<string, ThreadLiveSignals>;
  onVisibleChatOrder?: (ids: readonly string[]) => void;
  onNewInProject?: (projectID: string) => void;
  onSelectThread?: (threadID: string) => void;
  scopeID?: string;
  baseURL?: string;
  queryClient?: QueryClient;
}) {
  return render(
    <QueryClientProvider client={queryClient}>
      <LocaleProvider>
        <V2RecoveryProvider client={{ baseURL } as Pick<APIClient, "baseURL">} scopeID={scopeID}>
          <LocaleToggle />
          <V2Sidebar
            liveSignalsByThread={liveSignalsByThread}
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
    </QueryClientProvider>,
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

describe("priority bucket classification & ordering", () => {
  it("enforces strict priority precedence needs_input > running > unread > idle", () => {
    const blockedRunningUnread = makeThread({
      id: "t-blocked",
      title: "Blocked while running and unread",
      execution_state: "running",
    });
    expect(
      classifyThreadPriorityBucket(blockedRunningUnread, {
        pendingApprovals: 2,
        activeTool: { toolName: "shell", command: "rm -rf build" },
        isUnread: true,
      }),
    ).toBe("needs_input");

    const activeRunningUnread = makeThread({
      id: "t-running",
      title: "Running with unread",
      execution_state: "running",
    });
    expect(classifyThreadPriorityBucket(activeRunningUnread, { isUnread: true })).toBe("running");

    const idleUnread = makeThread({
      id: "t-unread",
      title: "Completed with unread reply",
      execution_state: "completed",
    });
    expect(classifyThreadPriorityBucket(idleUnread, { isUnread: true })).toBe("unread");

    const idleRead = makeThread({
      id: "t-idle",
      title: "Idle read session",
      execution_state: "idle",
    });
    expect(classifyThreadPriorityBucket(idleRead, { isUnread: false })).toBe("idle");

    const grouped = groupThreadsByPriority(
      [idleRead, idleUnread, activeRunningUnread, blockedRunningUnread],
      (thread) => {
        if (thread.id === "t-blocked") return { pendingApprovals: 1, isUnread: true };
        if (thread.id === "t-running") return { isUnread: true };
        if (thread.id === "t-unread") return { isUnread: true };
        return undefined;
      },
    );

    expect(grouped.groups.map((group) => [group.bucket, group.items.map((item) => item.thread.id)])).toEqual([
      ["needs_input", ["t-blocked"]],
      ["running", ["t-running"]],
      ["unread", ["t-unread"]],
      ["idle", ["t-idle"]],
    ]);
    expect(grouped.attentionCount).toBe(3);
  });

  it("classifies composer waiting_approval, successor_required, paused, and file_edit_review into needs_input", () => {
    expect(
      classifyThreadPriorityBucket(makeThread({ id: "c1", title: "Approval", composer_state: "waiting_approval" })),
    ).toBe("needs_input");
    expect(
      classifyThreadPriorityBucket(makeThread({ id: "c2", title: "Successor", composer_state: "successor_required" })),
    ).toBe("needs_input");
    expect(
      classifyThreadPriorityBucket(makeThread({ id: "c3", title: "Paused", execution_state: "paused" })),
    ).toBe("needs_input");
    expect(
      classifyThreadPriorityBucket(
        makeThread({ id: "c4", title: "Diff review", execution_state: "idle" }),
        { pendingActionKind: "file_edit_review" },
      ),
    ).toBe("needs_input");
  });
});

describe("deriveThreadActivityStatus & QueryClient cache extraction", () => {
  it("distinguishes waiting_input, running_command, thinking, unread, and idle with localized labels", () => {
    const base = makeThread({ id: "t-status", title: "Status probe", execution_state: "running" });

    const waiting = deriveThreadActivityStatus(base, { pendingApprovals: 3 });
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

    const thinking = deriveThreadActivityStatus(base, {
      streamStatus: "live",
      streamSnapshot: { content_kind: "assistant_text", text: "正在分析架构…" },
    });
    expect(thinking).toMatchObject({
      kind: "thinking",
      bucket: "running",
      tone: "indigo",
      pulse: true,
      label: "思考中",
      detail: "生成回复中",
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
      items: [{ id: "approval-1" } as ApprovalQueueView["items"][number]],
    });
    const blockedSignals = extractCachedThreadSignals(queryClient, thread, false, false);
    expect(deriveThreadActivityStatus(thread, blockedSignals)).toMatchObject({
      kind: "waiting_input",
      bucket: "needs_input",
      detail: "1 项待审批",
    });
  });

  it("normalizes malformed persisted preferences safely", () => {
    expect(normalizeSidebarViewMode("project")).toBe("project");
    expect(normalizeSidebarViewMode({ bad: true })).toBe("priority");
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
      showWorkspaceBadge: true,
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
      liveSignalsByThread: {
        "cmd-1": { activeTool: { activityType: "execute", command: "go test ./..." } },
        "wait-1": { pendingApprovals: 2 },
      },
      onVisibleChatOrder,
    });

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
    expect(screen.getByRole("button", { name: "智能分组" })).toHaveAttribute("aria-pressed", "true");

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
    await user.click(screen.getByRole("button", { name: "按项目" }));
    expect(screen.getByRole("button", { name: "按项目" })).toHaveAttribute("aria-pressed", "true");

    // Verify pinned project "核心引擎" is anchored first in Project view
    const projectRegions = screen.getAllByRole("region", { name: /^项目 / }).map((el) => el.getAttribute("aria-label"));
    expect(projectRegions).toEqual(["项目 核心引擎", "项目 前端工作台"]);

    // Unmount and remount to verify persistence of view mode and project pins
    first.unmount();
    renderSidebar({ threads, onNewInProject });
    expect(screen.getByRole("button", { name: "按项目" })).toHaveAttribute("aria-pressed", "true");
    expect(screen.getAllByRole("region", { name: /^项目 / })[0]).toHaveAccessibleName("项目 核心引擎");

    // Unpin via the explicit row-level Pin button on ProjectHeader
    await user.click(screen.getByRole("button", { name: "取消置顶项目 核心引擎" }));
    expect(screen.getAllByRole("region", { name: /^项目 / }).map((el) => el.getAttribute("aria-label"))).toEqual([
      "项目 前端工作台",
      "项目 核心引擎",
    ]);
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
});
