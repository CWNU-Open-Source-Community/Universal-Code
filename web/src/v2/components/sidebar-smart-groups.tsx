import { useEffect, useId, useLayoutEffect, useRef, useState } from "react";
import { createPortal } from "react-dom";
import type { QueryClient } from "@tanstack/react-query";
import {
  AlertCircle, Check, Folder,
  Pin, PinOff, RotateCcw, SlidersHorizontal, Sparkles, SquarePen, Terminal, X,
} from "lucide-react";
import { useLocale, type LocaleText } from "../../lib/locale";
import type {
  ApprovalQueueView,
  ThreadTranscriptItemView, ThreadView, WorkspaceView,
} from "../../api/types";
import { v2QueryKeys } from "../query-keys";
import { executionLabels, executionState, priorityGroups, threadPriority, type Priority } from "./sidebar-priority";

export type SidebarViewMode = "priority" | "project";
export type SidebarPriorityBucket = Priority;
export type SidebarDensity = "comfortable" | "compact";
export type SidebarIdleLimit = 5 | 15 | "all";

const configurablePriorities = priorityGroups.filter((group) => group[0] !== "unknown");

export interface SidebarCustomPrefs {
  defaultLayout: SidebarViewMode;
  visibleGroups: Record<Exclude<Priority, "unknown">, boolean>;
  autoCollapseIdle: boolean;
  showActivitySubtitle: boolean;
  showWorkspaceBadge: boolean;
  density: SidebarDensity;
  idleLimit: SidebarIdleLimit;
}

export const DEFAULT_SIDEBAR_CUSTOM_PREFS: SidebarCustomPrefs = {
  defaultLayout: "project",
  visibleGroups: {
    needs_input: true,
    running: true,
    unread: true,
    idle: true,
  },
  autoCollapseIdle: false,
  showActivitySubtitle: false,
  showWorkspaceBadge: false,
  density: "comfortable",
  idleLimit: "all",
};

const isPlainRecord = (value: unknown): value is Record<string, unknown> =>
  Boolean(value) && typeof value === "object" && !Array.isArray(value);

export function normalizeSidebarViewMode(value: unknown, fallback: SidebarViewMode = "project"): SidebarViewMode {
  if (value === "priority" || value === "project") return value;
  return fallback;
}

export function normalizeSidebarCustomPrefs(value: unknown): SidebarCustomPrefs {
  if (!isPlainRecord(value)) return DEFAULT_SIDEBAR_CUSTOM_PREFS;
  const rawGroups = isPlainRecord(value.visibleGroups) ? value.visibleGroups : {};
  const defaultLayout = normalizeSidebarViewMode(value.defaultLayout);
  const density: SidebarDensity = value.density === "compact" ? "compact" : "comfortable";
  const idleLimit: SidebarIdleLimit = value.idleLimit === 5 || value.idleLimit === 15 || value.idleLimit === "all"
    ? value.idleLimit
    : DEFAULT_SIDEBAR_CUSTOM_PREFS.idleLimit;
  return {
    defaultLayout,
    visibleGroups: {
      needs_input: typeof rawGroups.needs_input === "boolean" ? rawGroups.needs_input : true,
      running: typeof rawGroups.running === "boolean" ? rawGroups.running : true,
      unread: typeof rawGroups.unread === "boolean" ? rawGroups.unread : true,
      idle: typeof rawGroups.idle === "boolean" ? rawGroups.idle : true,
    },
    autoCollapseIdle: typeof value.autoCollapseIdle === "boolean" ? value.autoCollapseIdle : false,
    showActivitySubtitle: value.showActivitySubtitle === true,
    showWorkspaceBadge: value.showWorkspaceBadge === true,
    density,
    idleLimit,
  };
}

type CanonicalExecutionState = keyof typeof executionLabels;

export type ThreadActivityStatusKind =
  | "waiting_input"
  | "running_command"
  | "thinking"
  | "stopping"
  | "failed"
  | "unread"
  | "idle"
  | "unknown";

export type ActivityTone = "amber" | "cyan" | "indigo" | "blue" | "danger" | "neutral";

export interface ThreadActiveToolSignal {
  toolName?: string;
  activityType?: string;
  kind?: string;
  command?: string;
  label?: string;
}

export interface ThreadLiveSignals {
  pendingApprovals?: number;
  activeTool?: ThreadActiveToolSignal | null;
  isUnread?: boolean;
  readFailed?: boolean;
}

export interface ThreadActivityDescriptor {
  kind: ThreadActivityStatusKind;
  bucket: SidebarPriorityBucket;
  tone: ActivityTone;
  pulse: boolean;
  label: string;
  detail?: string;
  accessibleDetail?: string;
}

function truncateInline(value: string, maxLength = 34): string {
  const normalized = value.replace(/\s+/gu, " ").trim();
  if (normalized.length <= maxLength) return normalized;
  return `${normalized.slice(0, maxLength - 1)}…`;
}

function describeActiveTool(tool: ThreadActiveToolSignal, t: LocaleText): string | undefined {
  if (tool.command?.trim()) return truncateInline(tool.command);
  if (tool.label?.trim()) return truncateInline(tool.label);
  const name = tool.toolName?.trim() ?? "";
  const activity = tool.activityType?.trim() ?? "";
  if (tool.kind === "browser" || activity === "browser" || /browser|cdp|page|navigate/iu.test(name)) {
    return t("浏览器操作", "Browser action");
  }
  if (activity === "search" || /search|grep|find/iu.test(name)) {
    return t("搜索上下文", "Searching");
  }
  if (activity === "verify" || /test|verify|check|lint/iu.test(name)) {
    return t("运行验证检查", "Running checks");
  }
  if (activity === "edit" || /workspace_(change|apply)|edit|write|patch/iu.test(name)) {
    return name === "workspace_change"
      ? t("准备代码修改", "Preparing changes")
      : t("应用文件修改", "Applying edits");
  }
  if (activity === "read" || /read|open|cat|view/iu.test(name)) {
    return t("读取文件资料", "Reading files");
  }
  if (name) return truncateInline(name, 28);
  return undefined;
}

function formatShortUpdatedAt(updatedAt: string | undefined, locale: string): string | undefined {
  if (!updatedAt) return undefined;
  const parsed = Date.parse(updatedAt);
  if (!Number.isFinite(parsed)) return undefined;
  try {
    return new Intl.DateTimeFormat(locale, { month: "numeric", day: "numeric", hour: "2-digit", minute: "2-digit" })
      .format(new Date(parsed));
  } catch {
    return undefined;
  }
}

export function deriveCanonicalExecutionStatus(
  thread: ThreadView,
  readFailed: boolean,
  t: LocaleText = (chinese) => chinese,
): { state: CanonicalExecutionState; label: string; description: string } {
  const state = executionState(thread, readFailed);
  const [chinese, english] = executionLabels[state];
  const label = t(chinese, english);
  const description = readFailed
    ? t("本次列表读取失败，执行状态未知", "The list could not be read; execution status is unknown")
    : state !== "unknown"
      ? label
      : t("执行状态未知，请刷新列表重试", "Execution status is unknown; refresh the list to try again");
  return { state, label, description };
}

/**
 * Derives both the attention-priority bucket and fine-grained live activity descriptor
 * for a sidebar thread row without requiring the user to open each tab.
 */
export function deriveThreadActivityStatus(
  thread: ThreadView,
  signals?: ThreadLiveSignals,
  t: LocaleText = (chinese) => chinese,
  locale = "zh-CN",
): ThreadActivityDescriptor {
  const readFailed = Boolean(signals?.readFailed);
  const canonical = deriveCanonicalExecutionStatus(thread, readFailed, t);
  const bucket = threadPriority(thread, readFailed, signals?.isUnread === true);

  if (readFailed) {
    return {
      kind: "unknown",
      bucket,
      tone: "neutral",
      pulse: false,
      label: canonical.label,
    };
  }

  const pendingApprovals = Math.max(0, signals?.pendingApprovals ?? 0);

  if (bucket === "needs_input") {
    let detail: string | undefined;
    if (pendingApprovals > 0) {
      detail = t(
        `${pendingApprovals} 项待审批`,
        `${pendingApprovals} pending approval${pendingApprovals === 1 ? "" : "s"}`,
      );
    } else if (canonical.state === "stop_failed") {
      detail = t("停止未完成", "Stop failed");
    } else if (canonical.state === "paused") {
      detail = t("已暂停", "Paused");
    } else {
      detail = t("待确认授权", "Awaiting approval");
    }

    const waitingLabel = t("等待回复", "Waiting for input");
    const hasEnrichedWaitingSignal =
      pendingApprovals > 0 ||
      !["waiting_approval", "paused", "stop_failed"].includes(canonical.state);

    return {
      kind: "waiting_input",
      bucket: "needs_input",
      tone: "amber",
      pulse: false,
      label: waitingLabel,
      detail,
      accessibleDetail: hasEnrichedWaitingSignal
        ? (detail ? `${waitingLabel} · ${detail}` : waitingLabel)
        : undefined,
    };
  }

  if (canonical.state === "stopping") {
    const stoppingLabel = t("正在停止", "Stopping");
    return {
      kind: "stopping",
      bucket: "running",
      tone: "amber",
      pulse: true,
      label: stoppingLabel,
    };
  }

  if (bucket === "running") {
    if (signals?.activeTool) {
      const commandLabel = t("执行命令", "Running command");
      const commandDetail = describeActiveTool(signals.activeTool, t);
      return {
        kind: "running_command",
        bucket: "running",
        tone: "cyan",
        pulse: true,
        label: commandLabel,
        detail: commandDetail,
        accessibleDetail: commandDetail ? `${commandLabel} · ${commandDetail}` : commandLabel,
      };
    }

    const thinkingLabel = t("思考中", "Thinking");
    return {
      kind: "thinking",
      bucket: "running",
      tone: "indigo",
      pulse: true,
      label: thinkingLabel,
    };
  }

  if (bucket === "unread") {
    const unreadDetail = canonical.state === "failed"
      ? t("执行失败", "Failed")
      : canonical.state === "completed"
        ? t("已完成", "Completed")
        : formatShortUpdatedAt(thread.updated_at, locale);
    return {
      kind: "unread",
      bucket: "unread",
      tone: "blue",
      pulse: false,
      label: t("未读新进展", "Unread update"),
      detail: unreadDetail,
    };
  }

  if (canonical.state === "failed") {
    const failedLabel = t("执行失败", "Failed");
    return {
      kind: "failed",
      bucket: "idle",
      tone: "danger",
      pulse: false,
      label: failedLabel,
      detail: formatShortUpdatedAt(thread.updated_at, locale),
    };
  }

  if (bucket === "unknown") {
    return {
      kind: "unknown",
      bucket,
      tone: "neutral",
      pulse: false,
      label: canonical.label,
      detail: formatShortUpdatedAt(thread.updated_at, locale),
    };
  }

  const idleDetail = canonical.state === "completed"
    ? t("已完成", "Completed")
    : canonical.state === "cancelled"
      ? t("已取消", "Cancelled")
      : formatShortUpdatedAt(thread.updated_at, locale);

  return {
    kind: "idle",
    bucket: "idle",
    tone: "neutral",
    pulse: false,
    label: t("空闲", "Idle"),
    detail: idleDetail,
  };
}

// Only use a matching live run to enrich the action text. Priority comes from the thread list.
export function extractCachedThreadSignals(
  queryClient: QueryClient,
  thread: ThreadView,
  isUnread: boolean,
  readFailed: boolean,
): ThreadLiveSignals {
  const signals: ThreadLiveSignals = { isUnread, readFailed };
  const runID = thread.active_run_id;
  if (readFailed || !runID) return signals;

  if (thread.composer_state === "waiting_approval" || thread.execution_state === "waiting_approval") {
    const queue = queryClient.getQueryData<ApprovalQueueView>(v2QueryKeys.approvals(runID));
    if (queue?.run_id === runID) signals.pendingApprovals = queue.items.filter((item) => item.status === "pending").length;
    return signals;
  }
  if (thread.execution_state !== "running") return signals;
  const transcriptData = queryClient.getQueryData<{ pages?: Array<{ items?: ThreadTranscriptItemView[] }> }>(
    v2QueryKeys.transcript(thread.id),
  );
  if (transcriptData?.pages?.length) {
    const items = transcriptData.pages
      .flatMap((page) => page.items ?? [])
      .filter((item) => item.run_id === runID && item.durable && !item.provisional);
    const latestModelSequence = Math.max(
      0,
      ...items.filter((item) => item.kind === "model_call").map((item) => item.sequence),
    );
    const tools = new Map<string, ThreadTranscriptItemView>();
    for (const item of items) {
      if (item.kind !== "tool_call" && item.kind !== "browser" && item.kind !== "approval") continue;
      const identity = item.durable_call_id || item.stream_call_id || item.id;
      if (!identity) continue;
      const previous = tools.get(identity);
      if (!previous || item.sequence >= previous.sequence) tools.set(identity, item);
    }
    const latestTools = [...tools.values()].sort((a, b) => b.sequence - a.sequence);
    const runningItem = latestTools.find(
      (item) => item.stage === "running" && item.sequence > latestModelSequence,
    );
    if (runningItem) {
      signals.activeTool = {
        command: runningItem.activity_summary?.command,
        toolName: runningItem.tool_name,
        activityType: runningItem.activity_type,
        kind: runningItem.kind,
      };
    }
  }

  return signals;
}

export function SidebarActivityGlyph({ activity }: { activity: ThreadActivityDescriptor }) {
  return (
    <span
      aria-hidden="true"
      className={`v2-thread-activity-glyph kind-${activity.kind} tone-${activity.tone}${activity.pulse ? " is-pulsing" : ""}`}
    >
      {activity.kind === "waiting_input" ? (
        <AlertCircle size={13} />
      ) : activity.kind === "running_command" ? (
        <Terminal size={12} />
      ) : activity.kind === "thinking" ? (
        <Sparkles size={12} />
      ) : (
        <i />
      )}
    </span>
  );
}

export function SidebarCustomizeControl({
  viewMode,
  prefs,
  onViewModeChange,
  onPrefsChange,
}: {
  viewMode: SidebarViewMode;
  prefs: SidebarCustomPrefs;
  onViewModeChange: (mode: SidebarViewMode) => void;
  onPrefsChange: (updater: SidebarCustomPrefs | ((current: SidebarCustomPrefs) => SidebarCustomPrefs)) => void;
}) {
  const { t } = useLocale();
  const [open, setOpen] = useState(false);
  const [position, setPosition] = useState({ left: 12, top: 44 });
  const triggerRef = useRef<HTMLButtonElement>(null);
  const dialogRef = useRef<HTMLDivElement>(null);
  const headingID = useId();
  const subtitleToggleID = useId();
  const workspaceBadgeToggleID = useId();
  const autoCollapseToggleID = useId();
  const groupCheckboxBaseID = useId();

  useLayoutEffect(() => {
    if (!open || !triggerRef.current || !dialogRef.current) return;
    const bounds = triggerRef.current.getBoundingClientRect();
    const popup = dialogRef.current.getBoundingClientRect();
    const left = Math.max(8, Math.min(bounds.left, window.innerWidth - popup.width - 8));
    const top = Math.max(8, Math.min(bounds.bottom + 6, window.innerHeight - popup.height - 8));
    setPosition({ left, top });
    if (!dialogRef.current.contains(document.activeElement)) {
      dialogRef.current.focus();
    }
  }, [open]);

  useEffect(() => {
    if (!open) return;
    const handleOutside = (event: Event) => {
      const target = event.target as Node | null;
      if (event.type === "pointerdown" && (triggerRef.current?.contains(target) || dialogRef.current?.contains(target))) {
        return;
      }
      if (event.type === "scroll" && dialogRef.current?.contains(target)) {
        return;
      }
      setOpen(false);
    };
    const handleKeyDown = (event: KeyboardEvent) => {
      if (event.key !== "Escape") return;
      event.preventDefault();
      setOpen(false);
      triggerRef.current?.focus();
    };
    document.addEventListener("pointerdown", handleOutside);
    document.addEventListener("keydown", handleKeyDown);
    window.addEventListener("resize", handleOutside);
    return () => {
      document.removeEventListener("pointerdown", handleOutside);
      document.removeEventListener("keydown", handleKeyDown);
      window.removeEventListener("resize", handleOutside);
    };
  }, [open]);

  const setDefaultLayout = (layout: SidebarViewMode) => {
    onPrefsChange((current) => ({ ...current, defaultLayout: layout }));
    onViewModeChange(layout);
  };

  return (
    <>
      <button
        aria-expanded={open}
        aria-haspopup="dialog"
        aria-label={t("自定义边栏", "Customize sidebar")}
        className="v2-sidebar-customize-trigger"
        onClick={() => setOpen((current) => !current)}
        ref={triggerRef}
        title={t("自定义边栏", "Customize sidebar")}
        type="button"
      >
        <SlidersHorizontal aria-hidden="true" size={15} />
      </button>
      {open && createPortal(
        <div
          aria-labelledby={headingID}
          className="v2-sidebar-customize-popover"
          onBlur={(event) => {
            const next = event.relatedTarget as Node | null;
            if (next && !dialogRef.current?.contains(next) && !triggerRef.current?.contains(next)) {
              setOpen(false);
            }
          }}
          ref={dialogRef}
          role="dialog"
          style={position}
          tabIndex={-1}
        >
          <header className="v2-sidebar-customize-header">
            <h2 id={headingID}>{t("自定义边栏", "Customize sidebar")}</h2>
            <button
              aria-label={t("关闭边栏自定义", "Close sidebar customization")}
              onClick={() => { setOpen(false); triggerRef.current?.focus(); }}
              type="button"
            >
              <X aria-hidden="true" size={15} />
            </button>
          </header>

          <div className="v2-sidebar-customize-section">
            <span className="v2-sidebar-customize-label">{t("默认分组布局", "Default layout")}</span>
            <div aria-label={t("默认分组布局", "Default layout")} className="v2-sidebar-segmented" role="group">
              <button
                aria-pressed={viewMode === "priority"}
                onClick={() => setDefaultLayout("priority")}
                type="button"
              >
                {t("智能优先级分组", "Smart priority")}
              </button>
              <button
                aria-pressed={viewMode === "project"}
                onClick={() => setDefaultLayout("project")}
                type="button"
              >
                {t("按项目分组", "By project")}
              </button>
            </div>
          </div>

          <div className="v2-sidebar-customize-section">
            <span className="v2-sidebar-customize-label">{t("显示密度", "Density")}</span>
            <div aria-label={t("显示密度", "Density")} className="v2-sidebar-segmented" role="group">
              <button
                aria-pressed={prefs.density === "comfortable"}
                onClick={() => onPrefsChange((current) => ({ ...current, density: "comfortable" }))}
                type="button"
              >
                {t("标准", "Comfortable")}
              </button>
              <button
                aria-pressed={prefs.density === "compact"}
                onClick={() => onPrefsChange((current) => ({ ...current, density: "compact" }))}
                type="button"
              >
                {t("紧凑", "Compact")}
              </button>
            </div>
          </div>

          <div className="v2-sidebar-customize-section">
            <span className="v2-sidebar-customize-label">{t("会话状态与标签", "Row details")}</span>
            <div className="v2-sidebar-check">
              <input
                checked={prefs.showActivitySubtitle}
                id={subtitleToggleID}
                onChange={(event) => onPrefsChange((current) => ({
                  ...current,
                  showActivitySubtitle: event.target.checked,
                }))}
                type="checkbox"
              />
              <label htmlFor={subtitleToggleID}>{t("显示智能体实时动作详情", "Show live agent activity")}</label>
            </div>
            <div className="v2-sidebar-check">
              <input
                checked={prefs.showWorkspaceBadge}
                id={workspaceBadgeToggleID}
                onChange={(event) => onPrefsChange((current) => ({
                  ...current,
                  showWorkspaceBadge: event.target.checked,
                }))}
                type="checkbox"
              />
              <label htmlFor={workspaceBadgeToggleID}>{t("显示所属项目标签", "Show project badge")}</label>
            </div>
          </div>

          <div className="v2-sidebar-customize-section">
            <span className="v2-sidebar-customize-label">{t("显示的优先级分组", "Visible priority groups")}</span>
            <div className="v2-sidebar-group-checks">
              {configurablePriorities.map(([bucket, chinese, english]) => {
                const checkboxID = `${groupCheckboxBaseID}-${bucket}`;
                return (
                  <div className="v2-sidebar-check" key={bucket}>
                    <input
                      checked={prefs.visibleGroups[bucket]}
                      id={checkboxID}
                      onChange={(event) => onPrefsChange((current) => ({
                        ...current,
                        visibleGroups: {
                          ...current.visibleGroups,
                          [bucket]: event.target.checked,
                        },
                      }))}
                      type="checkbox"
                    />
                    <label htmlFor={checkboxID}>{t(chinese, english)}</label>
                  </div>
                );
              })}
            </div>
            <div className="v2-sidebar-check">
              <input
                checked={prefs.autoCollapseIdle}
                id={autoCollapseToggleID}
                onChange={(event) => onPrefsChange((current) => ({
                  ...current,
                  autoCollapseIdle: event.target.checked,
                }))}
                type="checkbox"
              />
              <label htmlFor={autoCollapseToggleID}>{t("自动折叠「空闲」分组", "Auto-collapse Idle group")}</label>
            </div>
          </div>

          <div className="v2-sidebar-customize-section">
            <span className="v2-sidebar-customize-label">{t("空闲会话显示数量", "Idle session limit")}</span>
            <div aria-label={t("空闲会话显示数量", "Idle session limit")} className="v2-sidebar-segmented" role="group">
              {([5, 15, "all"] as const).map((limit) => (
                <button
                  aria-pressed={prefs.idleLimit === limit}
                  key={String(limit)}
                  onClick={() => onPrefsChange((current) => ({ ...current, idleLimit: limit }))}
                  type="button"
                >
                  {limit === "all" ? t("全部", "All") : String(limit)}
                </button>
              ))}
            </div>
          </div>

          <footer className="v2-sidebar-customize-footer">
            <button
              className="v2-sidebar-customize-reset"
              onClick={() => {
                onPrefsChange(DEFAULT_SIDEBAR_CUSTOM_PREFS);
                onViewModeChange(DEFAULT_SIDEBAR_CUSTOM_PREFS.defaultLayout);
              }}
              type="button"
            >
              <RotateCcw aria-hidden="true" size={13} />
              <span>{t("恢复默认设置", "Reset defaults")}</span>
            </button>
          </footer>
        </div>,
        document.body,
      )}
    </>
  );
}

export interface ProjectSummaryCounts {
  total: number;
  needsInput: number;
  running: number;
  unread: number;
}

export function PinnedProjectsQuickBar({
  workspaces,
  pinnedProjects,
  activeProjectFilter,
  projectCounts,
  resolveWorkspaceLabel,
  onSelectProjectFilter,
  onTogglePin,
  onNewInProject,
  showManagePicker = false,
}: {
  workspaces: readonly WorkspaceView[];
  pinnedProjects: ReadonlySet<string>;
  activeProjectFilter: string | null;
  projectCounts: ReadonlyMap<string, ProjectSummaryCounts>;
  resolveWorkspaceLabel: (workspaceID: string) => string;
  onSelectProjectFilter: (workspaceID: string | null) => void;
  onTogglePin: (workspaceID: string) => void;
  onNewInProject?: (workspaceID: string) => void;
  showManagePicker?: boolean;
}) {
  const { t } = useLocale();
  const pinnedList = workspaces.filter((workspace) => pinnedProjects.has(workspace.id));
  if (pinnedList.length === 0 && (!showManagePicker || workspaces.length === 0)) {
    return null;
  }

  return (
    <section aria-label={t("置顶项目", "Pinned projects")} className="v2-pinned-projects-bar">
      <div className="v2-pinned-projects-header">
        <span className="v2-pinned-projects-heading">
          <Pin aria-hidden="true" size={12} />
          <span>{t("置顶项目", "Pinned projects")}</span>
        </span>
        {activeProjectFilter !== null && (
          <button
            className="v2-pinned-filter-clear"
            onClick={() => onSelectProjectFilter(null)}
            type="button"
          >
            {t("清除筛选", "Clear filter")}
          </button>
        )}
        {workspaces.length > 0 && (
          <details
            className="v2-pinned-projects-picker"
            onBlur={(event) => {
              if (!event.currentTarget.contains(event.relatedTarget as Node | null)) {
                event.currentTarget.open = false;
              }
            }}
            onKeyDown={(event) => {
              if (event.key === "Escape") {
                event.currentTarget.open = false;
                event.currentTarget.querySelector("summary")?.focus();
              }
            }}
          >
            <summary aria-label={t("管理置顶项目", "Manage pinned projects")}>
              <Folder aria-hidden="true" size={13} />
              <span>{pinnedList.length === 0 ? t("置顶常用项目", "Pin projects") : t("管理", "Manage")}</span>
            </summary>
            <div className="v2-pinned-projects-menu">
              {workspaces.map((workspace) => {
                const label = resolveWorkspaceLabel(workspace.id);
                const isPinned = pinnedProjects.has(workspace.id);
                return (
                  <button
                    aria-pressed={isPinned}
                    key={workspace.id}
                    onClick={() => onTogglePin(workspace.id)}
                    type="button"
                  >
                    <Pin aria-hidden="true" fill={isPinned ? "currentColor" : "none"} size={13} />
                    <span>{label}</span>
                    {isPinned && <Check aria-hidden="true" size={13} />}
                  </button>
                );
              })}
            </div>
          </details>
        )}
      </div>

      {pinnedList.length > 0 && (
        <div className="v2-pinned-projects-chips">
          {pinnedList.map((workspace) => {
            const label = resolveWorkspaceLabel(workspace.id);
            const active = activeProjectFilter === workspace.id;
            const counts = projectCounts.get(workspace.id);
            const tone: ActivityTone = (counts?.needsInput ?? 0) > 0
              ? "amber"
              : (counts?.running ?? 0) > 0
                ? "indigo"
                : (counts?.unread ?? 0) > 0
                  ? "blue"
                  : "neutral";
            return (
              <div className={`v2-pinned-project-chip${active ? " is-active" : ""}`} key={workspace.id}>
                <button
                  aria-label={t(`筛选项目 ${label}`, `Filter project ${label}`)}
                  aria-pressed={active}
                  className="v2-pinned-project-filter"
                  onClick={() => onSelectProjectFilter(active ? null : workspace.id)}
                  type="button"
                >
                  <i aria-hidden="true" className={`v2-pinned-project-dot tone-${tone}${(counts?.running ?? 0) > 0 ? " is-pulsing" : ""}`} />
                  <span className="v2-pinned-project-name">{label}</span>
                  {(counts?.total ?? 0) > 0 && (
                    <em aria-hidden="true" className="v2-pinned-project-count">{counts!.total}</em>
                  )}
                </button>
                {onNewInProject && (
                  <button
                    aria-label={t(`在置顶项目 ${label} 中新建聊天`, `New chat in pinned project ${label}`)}
                    className="v2-pinned-project-action"
                    onClick={() => onNewInProject(workspace.id)}
                    title={t(`在 ${label} 中新建聊天`, `New chat in ${label}`)}
                    type="button"
                  >
                    <SquarePen aria-hidden="true" size={12} />
                  </button>
                )}
                <button
                  aria-label={t(`取消置顶栏中的 ${label}`, `Unpin ${label} from quick bar`)}
                  className="v2-pinned-project-action"
                  onClick={() => {
                    if (active) onSelectProjectFilter(null);
                    onTogglePin(workspace.id);
                  }}
                  title={t("取消置顶项目", "Unpin project")}
                  type="button"
                >
                  <PinOff aria-hidden="true" size={12} />
                </button>
              </div>
            );
          })}
        </div>
      )}
    </section>
  );
}
