import { useEffect, useId, useLayoutEffect, useRef, useState, type ReactNode } from "react";
import { createPortal } from "react-dom";
import type { QueryClient } from "@tanstack/react-query";
import {
  AlertCircle, BellDot, Check, ChevronRight, Clock3, Folder, FolderOpen,
  Pin, PinOff, RotateCcw, SlidersHorizontal, Sparkles, SquarePen, Terminal, X,
} from "lucide-react";
import { useLocale, type LocaleText } from "../../lib/locale";
import type {
  ApprovalQueueView, RunView, ThreadDetailView, ThreadExecutionView,
  ThreadTranscriptItemView, ThreadView, WorkspaceView,
} from "../../api/types";
import { v2QueryKeys } from "../query-keys";

export type SidebarViewMode = "priority" | "project";
export type SidebarPriorityBucket = "needs_input" | "running" | "unread" | "idle";
export type SidebarDensity = "comfortable" | "compact";
export type SidebarIdleLimit = 5 | 15 | "all";

export const PRIORITY_BUCKET_ORDER: readonly SidebarPriorityBucket[] = [
  "needs_input",
  "running",
  "unread",
  "idle",
] as const;

export interface SidebarCustomPrefs {
  defaultLayout: SidebarViewMode;
  visibleGroups: Record<SidebarPriorityBucket, boolean>;
  autoCollapseIdle: boolean;
  showActivitySubtitle: boolean;
  showWorkspaceBadge: boolean;
  density: SidebarDensity;
  idleLimit: SidebarIdleLimit;
}

export const DEFAULT_SIDEBAR_CUSTOM_PREFS: SidebarCustomPrefs = {
  defaultLayout: "priority",
  visibleGroups: {
    needs_input: true,
    running: true,
    unread: true,
    idle: true,
  },
  autoCollapseIdle: false,
  showActivitySubtitle: true,
  showWorkspaceBadge: true,
  density: "comfortable",
  idleLimit: "all",
};

const isPlainRecord = (value: unknown): value is Record<string, unknown> =>
  Boolean(value) && typeof value === "object" && !Array.isArray(value);

export function normalizeSidebarViewMode(value: unknown, fallback: SidebarViewMode = "priority"): SidebarViewMode {
  if (value === "priority" || value === "project") return value;
  return fallback;
}

export function normalizeSidebarCustomPrefs(value: unknown): SidebarCustomPrefs {
  if (!isPlainRecord(value)) return DEFAULT_SIDEBAR_CUSTOM_PREFS;
  const rawGroups = isPlainRecord(value.visibleGroups) ? value.visibleGroups : {};
  const defaultLayout = value.defaultLayout === "project" ? "project" : "priority";
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
    showActivitySubtitle: typeof value.showActivitySubtitle === "boolean" ? value.showActivitySubtitle : true,
    showWorkspaceBadge: typeof value.showWorkspaceBadge === "boolean" ? value.showWorkspaceBadge : true,
    density,
    idleLimit,
  };
}

export const SIDEBAR_EXECUTION_LABELS = {
  idle: ["空闲", "Idle"],
  running: ["执行中", "Running"],
  stopping: ["正在停止", "Stopping"],
  stop_failed: ["停止失败", "Stop failed"],
  waiting_approval: ["等待审批", "Awaiting approval"],
  paused: ["已暂停", "Paused"],
  completed: ["已完成", "Completed"],
  failed: ["执行失败", "Failed"],
  cancelled: ["已取消", "Cancelled"],
  unknown: ["状态未知", "Unknown"],
} as const;

export type CanonicalExecutionState = keyof typeof SIDEBAR_EXECUTION_LABELS;

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
  pendingActionKind?: "approval_pending" | "file_edit_review" | "file_edit_apply" | "steering_pending" | "wake_due";
  hasBlockedTool?: boolean;
  run?: Pick<RunView, "id" | "status"> | null;
  runStatus?: RunView["status"];
  execution?: Pick<ThreadExecutionView, "state" | "queued_messages" | "last_turn_interrupted"> | null;
  activeTool?: ThreadActiveToolSignal | null;
  streamSnapshot?: {
    content_kind?: string;
    text?: string;
    message_complete?: boolean;
    provisional?: boolean;
    cancel_requested?: boolean;
  } | null;
  streamStatus?: "idle" | "connecting" | "waiting" | "live" | "finalizing" | "reconnecting" | "stopped" | "error";
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
  executionState: CanonicalExecutionState;
  executionLabel: string;
  executionDescription: string;
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
  const raw = thread.execution_state;
  const state: CanonicalExecutionState = !readFailed && typeof raw === "string" && Object.hasOwn(SIDEBAR_EXECUTION_LABELS, raw)
    ? (raw as CanonicalExecutionState)
    : "unknown";
  const [chinese, english] = SIDEBAR_EXECUTION_LABELS[state];
  const label = t(chinese, english);
  const description = readFailed
    ? t("本次列表读取失败，执行状态未知", "The list could not be read; execution status is unknown")
    : raw && state !== "unknown"
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
  const isUnread = Boolean(signals?.isUnread);

  if (readFailed) {
    return {
      kind: "unknown",
      bucket: isUnread ? "unread" : "idle",
      tone: "neutral",
      pulse: false,
      label: canonical.label,
      executionState: canonical.state,
      executionLabel: canonical.label,
      executionDescription: canonical.description,
    };
  }

  const pendingApprovals = Math.max(0, signals?.pendingApprovals ?? 0);
  const effectiveRunStatus = signals?.runStatus ?? signals?.run?.status;
  const liveExecState = signals?.execution?.state;
  const effectiveExecState = liveExecState ?? canonical.state;

  // `composer_state === "successor_required"` is the backend's normal between-turns
  // state for every active thread without a live run (completed, failed, cancelled…);
  // the user can simply send the next message, so it must not count as needs-input.
  const needsOperatorInput =
    pendingApprovals > 0 ||
    signals?.hasBlockedTool === true ||
    signals?.pendingActionKind === "approval_pending" ||
    signals?.pendingActionKind === "file_edit_review" ||
    effectiveExecState === "waiting_approval" ||
    effectiveExecState === "stop_failed" ||
    canonical.state === "waiting_approval" ||
    canonical.state === "paused" ||
    canonical.state === "stop_failed" ||
    thread.composer_state === "waiting_approval" ||
    effectiveRunStatus === "waiting_approval" ||
    effectiveRunStatus === "paused";

  if (needsOperatorInput) {
    let detail: string | undefined;
    if (pendingApprovals > 0) {
      detail = t(
        `${pendingApprovals} 项待审批`,
        `${pendingApprovals} pending approval${pendingApprovals === 1 ? "" : "s"}`,
      );
    } else if (signals?.pendingActionKind === "file_edit_review") {
      detail = t("待审阅改动", "Changes awaiting review");
    } else if (canonical.state === "stop_failed" || effectiveExecState === "stop_failed") {
      detail = t("停止未完成", "Stop failed");
    } else if (canonical.state === "paused" || effectiveRunStatus === "paused") {
      detail = t("已暂停", "Paused");
    } else {
      detail = t("待确认授权", "Awaiting approval");
    }

    const waitingLabel = t("等待回复", "Waiting for input");
    const hasEnrichedWaitingSignal =
      pendingApprovals > 0 ||
      signals?.pendingActionKind === "file_edit_review" ||
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
      executionState: canonical.state,
      executionLabel: canonical.label,
      executionDescription: canonical.description,
    };
  }

  if (
    effectiveExecState === "stopping" ||
    canonical.state === "stopping" ||
    signals?.streamSnapshot?.cancel_requested === true
  ) {
    const stoppingLabel = t("正在停止", "Stopping");
    return {
      kind: "stopping",
      bucket: "running",
      tone: "amber",
      pulse: true,
      label: stoppingLabel,
      accessibleDetail: canonical.state !== "stopping" ? stoppingLabel : undefined,
      executionState: canonical.state,
      executionLabel: canonical.label,
      executionDescription: canonical.description,
    };
  }

  const confirmedTerminalByRun = effectiveRunStatus !== undefined &&
    ["completed", "failed", "cancelled"].includes(effectiveRunStatus);
  const explicitTurnActivity = !confirmedTerminalByRun && (
    Boolean(signals?.activeTool) ||
    signals?.streamStatus === "live" ||
    signals?.streamStatus === "connecting" ||
    liveExecState === "running"
  );
  const runContainerActive = !confirmedTerminalByRun &&
    liveExecState !== "idle" && (
      effectiveRunStatus === "running" ||
      effectiveRunStatus === "preparing" ||
      canonical.state === "running"
    );
  const isActivelyRunning = explicitTurnActivity || runContainerActive;

  if (isActivelyRunning) {
    const streamKind = signals?.streamSnapshot?.content_kind;
    if (signals?.activeTool || streamKind === "tool_call" || streamKind === "command") {
      const commandLabel = t("执行命令", "Running command");
      const commandDetail = signals?.activeTool ? describeActiveTool(signals.activeTool, t) : undefined;
      return {
        kind: "running_command",
        bucket: "running",
        tone: "cyan",
        pulse: true,
        label: commandLabel,
        detail: commandDetail,
        accessibleDetail: commandDetail ? `${commandLabel} · ${commandDetail}` : commandLabel,
        executionState: canonical.state,
        executionLabel: canonical.label,
        executionDescription: canonical.description,
      };
    }

    let thinkingDetail: string | undefined;
    if (signals?.streamStatus === "live" && signals?.streamSnapshot?.text?.trim() && !signals.streamSnapshot.message_complete) {
      thinkingDetail = streamKind === "tool_commentary"
        ? t("同步进展中", "Updating progress")
        : t("生成回复中", "Streaming reply");
    } else if (effectiveRunStatus === "preparing") {
      thinkingDetail = t("准备环境中", "Preparing run");
    } else if ((signals?.execution?.queued_messages ?? 0) > 0) {
      thinkingDetail = t(`队列 ${signals!.execution!.queued_messages} 条`, `${signals!.execution!.queued_messages} queued`);
    }

    const thinkingLabel = t("思考中", "Thinking");
    return {
      kind: "thinking",
      bucket: "running",
      tone: "indigo",
      pulse: true,
      label: thinkingLabel,
      detail: thinkingDetail,
      accessibleDetail: thinkingDetail
        ? `${thinkingLabel} · ${thinkingDetail}`
        : canonical.state !== "running"
          ? thinkingLabel
          : undefined,
      executionState: canonical.state,
      executionLabel: canonical.label,
      executionDescription: canonical.description,
    };
  }

  if (isUnread) {
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
      executionState: canonical.state,
      executionLabel: canonical.label,
      executionDescription: canonical.description,
    };
  }

  if (canonical.state === "failed" || effectiveRunStatus === "failed") {
    const failedLabel = t("执行失败", "Failed");
    return {
      kind: "failed",
      bucket: "idle",
      tone: "danger",
      pulse: false,
      label: failedLabel,
      detail: formatShortUpdatedAt(thread.updated_at, locale),
      accessibleDetail: canonical.state !== "failed" ? failedLabel : undefined,
      executionState: canonical.state,
      executionLabel: canonical.label,
      executionDescription: canonical.description,
    };
  }

  if (canonical.state === "unknown") {
    return {
      kind: "unknown",
      bucket: "idle",
      tone: "neutral",
      pulse: false,
      label: canonical.label,
      detail: formatShortUpdatedAt(thread.updated_at, locale),
      executionState: canonical.state,
      executionLabel: canonical.label,
      executionDescription: canonical.description,
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
    executionState: canonical.state,
    executionLabel: canonical.label,
    executionDescription: canonical.description,
  };
}

export function classifyThreadPriorityBucket(
  thread: ThreadView,
  signals?: ThreadLiveSignals,
  t?: LocaleText,
): SidebarPriorityBucket {
  return deriveThreadActivityStatus(thread, signals, t).bucket;
}

export interface GroupedPriorityThread {
  thread: ThreadView;
  activity: ThreadActivityDescriptor;
}

export interface PriorityBucketGroup {
  bucket: SidebarPriorityBucket;
  items: GroupedPriorityThread[];
}

export function groupThreadsByPriority(
  threads: readonly ThreadView[],
  resolveSignals?: (thread: ThreadView) => ThreadLiveSignals | undefined,
  t: LocaleText = (chinese) => chinese,
  locale = "zh-CN",
): {
  groups: PriorityBucketGroup[];
  counts: Record<SidebarPriorityBucket, number>;
  attentionCount: number;
} {
  const buckets: Record<SidebarPriorityBucket, GroupedPriorityThread[]> = {
    needs_input: [],
    running: [],
    unread: [],
    idle: [],
  };
  for (const thread of threads) {
    const activity = deriveThreadActivityStatus(thread, resolveSignals?.(thread), t, locale);
    buckets[activity.bucket].push({ thread, activity });
  }
  const counts: Record<SidebarPriorityBucket, number> = {
    needs_input: buckets.needs_input.length,
    running: buckets.running.length,
    unread: buckets.unread.length,
    idle: buckets.idle.length,
  };
  return {
    groups: PRIORITY_BUCKET_ORDER.map((bucket) => ({ bucket, items: buckets[bucket] })),
    counts,
    attentionCount: counts.needs_input + counts.running + counts.unread,
  };
}

/**
 * Passively reads cached execution, run, approval, and transcript state from QueryClient
 * without triggering extra background network requests.
 */
export function extractCachedThreadSignals(
  queryClient: QueryClient,
  thread: ThreadView,
  isUnread: boolean,
  readFailed: boolean,
  extra?: ThreadLiveSignals,
): ThreadLiveSignals {
  const listedRunID = thread.active_run_id || thread.last_run_id || "";
  const detail = queryClient.getQueryData<ThreadDetailView>(v2QueryKeys.thread(thread.id));
  const detailCandidateRun = detail?.active_run ?? detail?.last_run ?? null;
  const cachedRunMismatch = Boolean(detailCandidateRun && listedRunID && detailCandidateRun.id !== listedRunID);
  const activeRun = detailCandidateRun && !cachedRunMismatch ? detailCandidateRun : null;
  const resolvedRun = extra?.run !== undefined ? extra.run : activeRun;
  const runID = extra?.run !== undefined ? (extra.run?.id || "") : (listedRunID || activeRun?.id || "");

  const executionQuery = queryClient.getQueryCache().find({ queryKey: v2QueryKeys.execution(thread.id) });
  const rawExecution = queryClient.getQueryData<ThreadExecutionView>(v2QueryKeys.execution(thread.id));
  const threadUpdatedAtMs = Date.parse(thread.updated_at);
  const executionOlderThanThread =
    Number.isFinite(threadUpdatedAtMs) &&
    executionQuery !== undefined &&
    threadUpdatedAtMs > executionQuery.state.dataUpdatedAt;
  const staleIdleExecution =
    rawExecution?.state === "idle" &&
    (cachedRunMismatch ||
      executionOlderThanThread ||
      executionQuery?.state.isInvalidated === true ||
      thread.execution_state === "running" ||
      ((executionQuery?.getObserversCount() ?? 0) === 0 &&
        !["idle", "completed", "cancelled"].includes(thread.execution_state ?? "")));
  const execution = staleIdleExecution ? null : rawExecution;

  // In Universal-Code, an open multi-turn Thread keeps `active_run.status === "running"`
  // between turns while `thread.execution_state` / `execution.state` is `"idle"`.
  // Only propagate cached `activeRun.status === "running"` when the thread is not idle between turns.
  const cachedIdleBetweenTurns =
    extra?.run === undefined &&
    extra?.runStatus === undefined &&
    activeRun?.status === "running" &&
    (execution?.state === "idle" ||
      (execution?.state !== "running" &&
        ["idle", "completed", "cancelled"].includes(thread.execution_state ?? "")));
  const effectiveCachedRunStatus = cachedIdleBetweenTurns ? undefined : resolvedRun?.status;

  const approvals = runID ? queryClient.getQueryData<ApprovalQueueView>(v2QueryKeys.approvals(runID)) : undefined;
  const transcriptData = queryClient.getQueryData<{ pages?: Array<{ items?: ThreadTranscriptItemView[] }> }>(
    v2QueryKeys.transcript(thread.id),
  );

  let activeTool: ThreadActiveToolSignal | null = null;
  let hasBlockedTool = false;
  if (runID && transcriptData?.pages?.length) {
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
    hasBlockedTool = latestTools.some((item) => item.stage === "blocked");
    const runningItem = latestTools.find(
      (item) => item.stage === "running" && item.sequence > latestModelSequence,
    );
    if (runningItem) {
      activeTool = {
        toolName: runningItem.tool_name,
        activityType: runningItem.activity_type,
        kind: runningItem.kind,
      };
    }
  }

  return {
    pendingApprovals: extra?.pendingApprovals ?? approvals?.items.length,
    pendingActionKind: extra?.pendingActionKind,
    hasBlockedTool: extra?.hasBlockedTool ?? (hasBlockedTool || undefined),
    run: cachedIdleBetweenTurns ? null : resolvedRun,
    runStatus: extra?.runStatus ?? effectiveCachedRunStatus,
    execution: extra?.execution !== undefined ? extra.execution : (execution ?? null),
    activeTool: extra?.activeTool !== undefined ? extra.activeTool : activeTool,
    streamSnapshot: extra?.streamSnapshot,
    streamStatus: extra?.streamStatus,
    isUnread: extra?.isUnread ?? isUnread,
    readFailed: extra?.readFailed ?? readFailed,
  };
}

export function priorityBucketLabel(bucket: SidebarPriorityBucket, t: LocaleText): string {
  switch (bucket) {
    case "needs_input":
      return t("需用户输入", "Needs input");
    case "running":
      return t("运行中", "Running");
    case "unread":
      return t("未读", "Unread");
    case "idle":
      return t("空闲", "Idle");
  }
}

export function priorityBucketTone(bucket: SidebarPriorityBucket): ActivityTone {
  switch (bucket) {
    case "needs_input":
      return "amber";
    case "running":
      return "indigo";
    case "unread":
      return "blue";
    case "idle":
      return "neutral";
  }
}

function PriorityBucketIcon({ bucket }: { bucket: SidebarPriorityBucket }) {
  switch (bucket) {
    case "needs_input":
      return <AlertCircle aria-hidden="true" className="v2-priority-bucket-icon tone-amber" size={14} />;
    case "running":
      return <Sparkles aria-hidden="true" className="v2-priority-bucket-icon tone-indigo" size={14} />;
    case "unread":
      return <BellDot aria-hidden="true" className="v2-priority-bucket-icon tone-blue" size={14} />;
    case "idle":
      return <Clock3 aria-hidden="true" className="v2-priority-bucket-icon tone-neutral" size={14} />;
  }
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

export function SidebarModeBar({
  viewMode,
  attentionCount = 0,
  onViewModeChange,
  projectAction,
}: {
  viewMode: SidebarViewMode;
  attentionCount?: number;
  onViewModeChange: (mode: SidebarViewMode) => void;
  projectAction?: ReactNode;
}) {
  const { t } = useLocale();
  return (
    <div className="v2-sidebar-mode-bar">
      <div
        aria-label={t("会话分组方式", "Conversation grouping mode")}
        className="v2-sidebar-mode-switch"
        role="group"
      >
        <button
          aria-pressed={viewMode === "priority"}
          onClick={() => onViewModeChange("priority")}
          type="button"
        >
          <Sparkles aria-hidden="true" size={13} />
          <span>{t("智能分组", "Smart priority")}</span>
          {attentionCount > 0 && (
            <em aria-hidden="true" className="v2-mode-attention-count">
              {attentionCount}
            </em>
          )}
        </button>
        <button
          aria-pressed={viewMode === "project"}
          onClick={() => onViewModeChange("project")}
          type="button"
        >
          <FolderOpen aria-hidden="true" size={13} />
          <span>{t("按项目", "By project")}</span>
        </button>
      </div>
      {viewMode === "priority" && projectAction}
    </div>
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
              {PRIORITY_BUCKET_ORDER.map((bucket) => {
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
                    <label htmlFor={checkboxID}>{priorityBucketLabel(bucket, t)}</label>
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

export function SmartPriorityGroups({
  groups,
  prefs,
  collapsedBuckets,
  onToggleBucket,
  idleShowAll,
  onToggleIdleShowAll,
  renderThread,
  onOpenCustomize,
}: {
  groups: readonly PriorityBucketGroup[];
  prefs: SidebarCustomPrefs;
  collapsedBuckets: Record<SidebarPriorityBucket, boolean>;
  onToggleBucket: (bucket: SidebarPriorityBucket) => void;
  idleShowAll: boolean;
  onToggleIdleShowAll: () => void;
  renderThread: (thread: ThreadView, activity: ThreadActivityDescriptor) => ReactNode;
  onOpenCustomize?: () => void;
}) {
  const { t } = useLocale();
  const basePanelID = useId();

  let hiddenThreadCount = 0;
  const visibleNonEmptyGroups = groups.filter(({ bucket, items }) => {
    if (!prefs.visibleGroups[bucket]) {
      hiddenThreadCount += items.length;
      return false;
    }
    return items.length > 0;
  });

  return (
    <div className="v2-priority-groups">
      {visibleNonEmptyGroups.map(({ bucket, items }) => {
        const label = priorityBucketLabel(bucket, t);
        const tone = priorityBucketTone(bucket);
        const expanded = !collapsedBuckets[bucket];
        const panelID = `${basePanelID}-${bucket}`;
        const cappedLimit = bucket === "idle" && prefs.idleLimit !== "all" ? prefs.idleLimit : null;
        const displayedItems = cappedLimit !== null && !idleShowAll && items.length > cappedLimit
          ? items.slice(0, cappedLimit)
          : items;
        const remainingCount = items.length - displayedItems.length;

        return (
          <section
            aria-label={label}
            className={`v2-priority-group bucket-${bucket} tone-${tone}`}
            key={bucket}
          >
            <h2 className="v2-priority-heading">
              <button
                aria-controls={panelID}
                aria-expanded={expanded}
                className="v2-priority-toggle"
                onClick={() => onToggleBucket(bucket)}
                type="button"
              >
                <ChevronRight
                  aria-hidden="true"
                  className={`v2-priority-chevron${expanded ? " is-expanded" : ""}`}
                  size={14}
                />
                <PriorityBucketIcon bucket={bucket} />
                <span className="v2-priority-title">{label}</span>{" "}
                <span className={`v2-priority-count tone-${tone}`}>{items.length}</span>
              </button>
            </h2>
            {expanded && (
              <div className="v2-priority-list" id={panelID}>
                {displayedItems.map(({ thread, activity }) => renderThread(thread, activity))}
                {bucket === "idle" && cappedLimit !== null && items.length > cappedLimit && (
                  <button
                    className="v2-priority-more"
                    onClick={onToggleIdleShowAll}
                    type="button"
                  >
                    {remainingCount > 0
                      ? t(`显示其余 ${remainingCount} 条空闲会话`, `Show ${remainingCount} more idle chats`)
                      : t(`仅显示前 ${cappedLimit} 条`, `Show first ${cappedLimit} only`)}
                  </button>
                )}
              </div>
            )}
          </section>
        );
      })}
      {hiddenThreadCount > 0 && (
        <p className="v2-priority-hidden-hint" role="status">
          <span>
            {t(
              `已按边栏设置隐藏 ${hiddenThreadCount} 条会话`,
              `${hiddenThreadCount} ${hiddenThreadCount === 1 ? "chat" : "chats"} hidden by sidebar settings`,
            )}
          </span>
          {onOpenCustomize && (
            <button onClick={onOpenCustomize} type="button">
              {t("调整", "Adjust")}
            </button>
          )}
        </p>
      )}
    </div>
  );
}
