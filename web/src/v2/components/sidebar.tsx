import { useEffect, useLayoutEffect, useMemo, useReducer, useRef, useState, type ReactNode } from "react";
import { useInfiniteQuery, useQueryClient } from "@tanstack/react-query";
import { ArchiveRestore, ArrowLeft, ChartNoAxesCombined, Box, CalendarClock, ChevronDown, Cpu, FolderInput, MessagesSquare, MoreHorizontal, Pin, RefreshCw,
  Info, Keyboard, PackageSearch, Palette, PlugZap, Search, Settings, ShieldCheck, SquarePen, X } from "lucide-react";
import type { ThreadSearchResultView, ThreadView, WorkspaceView } from "../../api/types";
import type { APIClient } from "../../api/client";
import { useLocale } from "../../lib/locale";
import { ThreadProjectDialog, threadProjectID } from "./thread-project-dialog";
import { ProjectHeader } from "./project-header";
import { ThreadActions } from "./thread-actions";
import {
  DEFAULT_SIDEBAR_CUSTOM_PREFS,
  PinnedProjectsQuickBar,
  SidebarActivityGlyph,
  SidebarCustomizeControl,
  SidebarModeBar,
  SmartPriorityGroups,
  deriveThreadActivityStatus,
  extractCachedThreadSignals,
  groupThreadsByPriority,
  normalizeSidebarCustomPrefs,
  normalizeSidebarViewMode,
  type ProjectSummaryCounts,
  type SidebarCustomPrefs,
  type SidebarPriorityBucket,
  type SidebarViewMode,
  type ThreadActivityDescriptor,
  type ThreadLiveSignals,
} from "./sidebar-smart-groups";
import { useV2PersistentState } from "../recovery-storage";
import { v2QueryKeys } from "../query-keys";
import "./project-sidebar.css";

function projectPinIDs(value: unknown): string[] {
  return Array.isArray(value) ? value.filter((id): id is string => typeof id === "string" && id.length > 0) : [];
}

export type V2SettingsSection = "general" | "permissions" | "appearance" | "voice" |
  "models" | "plugins" | "browser" | "hooks" | "git" | "environment" |
  "worktrees" | "keyboard" | "records" | "archived" | "extensions" | "skills" |
  "advanced-models" | "about" | "shortcuts" | "connections" | "task-configuration" | "configuration" | "usage";

function lookupThreadSignal(
  source: ReadonlyMap<string, ThreadLiveSignals> | Record<string, ThreadLiveSignals> | undefined,
  threadID: string,
): ThreadLiveSignals | undefined {
  if (!source) return undefined;
  if (source instanceof Map) return source.get(threadID);
  return Object.hasOwn(source as Record<string, ThreadLiveSignals>, threadID)
    ? (source as Record<string, ThreadLiveSignals>)[threadID]
    : undefined;
}

export function V2Sidebar({ threads, workspaces, selectedThreadID, searchOpen, onSearchOpen,
  onNewConversation, onOpenModels, onSelectThread, onVisibleChatOrder, onOpenSettings, onArchive,
  onOpenHistory, historyActive = false, onOpenConnections, onOpenAutomations, automationsActive = false,
  search = "", appliedSearch = "", searchPending = false, searchTooLong = false, onSearchChange, onSearchCompositionChange,
  searchResults = [], includeArchived = false, onIncludeArchivedChange, onSelectSearchResult,
  searchCurrentThread = false, onSearchCurrentThreadChange,
  projectAction, onNewInProject, onEditProject, client, onMoveThread, movingThreadID = "", moveError = "",
  executionFilter = "", onExecutionFilterChange,
  unreadThreadIDs, onReadStateChange,
  liveSignalsByThread,
  defaultViewMode,
  visible = true,
  hasMore = false, truncated = false, loading = false, loadingMore = false, loadFailed = false,
  loadMoreFailed = false, refreshing = false, onLoadMore, onRefresh, onRetry }: {
  threads: ThreadView[];
  workspaces: WorkspaceView[];
  selectedThreadID: string;
  searchOpen: boolean;
  onSearchOpen: (open: boolean) => void;
  onNewConversation: () => void;
  onOpenModels: () => void;
  onSelectThread: (threadID: string) => void;
  onVisibleChatOrder?: (threadIDs: readonly string[]) => void;
  onOpenSettings: () => void;
  onArchive: (thread: ThreadView) => void;
  onOpenHistory?: () => void;
  onOpenConnections?: () => void;
  historyActive?: boolean;
  onOpenAutomations?: () => void;
  automationsActive?: boolean;
  search?: string; appliedSearch?: string; searchPending?: boolean; searchTooLong?: boolean;
  onSearchChange?: (value: string) => void;
  onSearchCompositionChange?: (composing: boolean) => void;
  searchResults?: ThreadSearchResultView[];
  includeArchived?: boolean;
  onIncludeArchivedChange?: (value: boolean) => void;
  onSelectSearchResult?: (result: ThreadSearchResultView) => void;
  searchCurrentThread?: boolean;
  onSearchCurrentThreadChange?: (value: boolean) => void;
  projectAction?: ReactNode;
  onNewInProject?: (projectID: string) => void;
  onEditProject?: (workspace: WorkspaceView) => void;
  client?: APIClient;
  onMoveThread?: (thread: ThreadView, projectID: string) => void;
  movingThreadID?: string;
  moveError?: string;
  executionFilter?: "" | "running" | "waiting_approval";
  onExecutionFilterChange?: (value: "" | "running" | "waiting_approval") => void;
  unreadThreadIDs?: ReadonlySet<string>;
  onReadStateChange?: (threadID: string, read: boolean) => void;
  liveSignalsByThread?: ReadonlyMap<string, ThreadLiveSignals> | Record<string, ThreadLiveSignals>;
  defaultViewMode?: SidebarViewMode;
  visible?: boolean;
  hasMore?: boolean; truncated?: boolean; loading?: boolean; loadingMore?: boolean; loadFailed?: boolean;
  loadMoreFailed?: boolean; refreshing?: boolean;
  onLoadMore?: () => void; onRefresh?: () => void; onRetry?: () => void;
}) {
  const { t, locale } = useLocale();
  const queryClient = useQueryClient();
  const [cacheVersion, bumpCacheVersion] = useReducer((count: number) => count + 1, 0);
  useEffect(() => {
    let active = true;
    let queued = false;
    const unsubscribe = queryClient.getQueryCache().subscribe((event) => {
      if (event.type !== "updated") return;
      const key = event.query.queryKey;
      if (key[0] === "v2" && (key[1] === "thread" || key[1] === "run") && !queued) {
        queued = true;
        queueMicrotask(() => {
          queued = false;
          if (active) bumpCacheVersion();
        });
      }
    });
    return () => { active = false; unsubscribe(); };
  }, [queryClient]);

  const searching = Boolean(appliedSearch);
  const [draggingThreadID, setDraggingThreadID] = useState("");
  const [dropProjectID, setDropProjectID] = useState<string | null>(null);
  const [projectCandidate, setProjectCandidate] = useState<ThreadView | null>(null);
  const [projectsExpanded, setProjectsExpanded] = useState(true);
  const [collapsedProjects, setCollapsedProjects] = useState<Set<string>>(() => new Set());
  const [activeProjectFilter, setActiveProjectFilter] = useState<string | null>(null);

  const [rawCustomPrefs, setRawCustomPrefs] = useV2PersistentState<unknown>(
    "sidebar-custom-prefs",
    DEFAULT_SIDEBAR_CUSTOM_PREFS,
  );
  const customPrefs = useMemo(() => normalizeSidebarCustomPrefs(rawCustomPrefs), [rawCustomPrefs]);
  const [collapsedBuckets, setCollapsedBuckets] = useState<Record<SidebarPriorityBucket, boolean>>(() => ({
    needs_input: false,
    running: false,
    unread: false,
    idle: customPrefs.autoCollapseIdle,
  }));
  const [idleShowAll, setIdleShowAll] = useState(false);
  useEffect(() => {
    setCollapsedBuckets((current) => (
      current.idle === customPrefs.autoCollapseIdle ? current : { ...current, idle: customPrefs.autoCollapseIdle }
    ));
  }, [customPrefs.autoCollapseIdle]);
  useEffect(() => {
    setIdleShowAll(false);
  }, [customPrefs.idleLimit]);
  const updateCustomPrefs = (
    updater: SidebarCustomPrefs | ((current: SidebarCustomPrefs) => SidebarCustomPrefs),
  ) => {
    setRawCustomPrefs((previous: unknown) => {
      const current = normalizeSidebarCustomPrefs(previous);
      return typeof updater === "function" ? updater(current) : updater;
    });
  };

  const initialMode: SidebarViewMode = defaultViewMode ?? "priority";
  const [rawViewMode, setRawViewMode] = useV2PersistentState<unknown>("sidebar-view-mode", initialMode);
  const viewMode = normalizeSidebarViewMode(rawViewMode, defaultViewMode ?? customPrefs.defaultLayout);
  const setViewMode = (mode: SidebarViewMode) => setRawViewMode(mode);

  const [savedProjectPins, setProjectPins] = useV2PersistentState<unknown>("pinned-projects", []);
  const pinnedProjects = useMemo(() => new Set(projectPinIDs(savedProjectPins)), [savedProjectPins]);
  const toggleProjectPin = (id: string) => {
    setProjectPins((previous: unknown) => {
      const ids = projectPinIDs(previous);
      const unpinning = ids.includes(id);
      if (unpinning) {
        setActiveProjectFilter((current) => (current === id ? null : current));
      }
      return unpinning ? ids.filter((value) => value !== id) : [...ids, id];
    });
  };

  useEffect(() => {
    if (
      activeProjectFilter !== null &&
      (!workspaces.some(({ id }) => id === activeProjectFilter) || !pinnedProjects.has(activeProjectFilter))
    ) {
      setActiveProjectFilter(null);
    }
  }, [activeProjectFilter, pinnedProjects, workspaces]);

  const pinsQuery = useInfiniteQuery({
    queryKey: ["v2", "threads", "active", "pinned", executionFilter],
    queryFn: ({ signal, pageParam }) => client!.getPage<ThreadView>("/threads", {
      status: "active", pinned: true, limit: 100, ...(executionFilter ? { execution_state: executionFilter } : {}),
    }, pageParam, signal),
    initialPageParam: "", getNextPageParam: (last) => last.page.next_cursor || undefined,
    enabled: Boolean(client) && !searchOpen && visible,
    refetchInterval: (query) => visible && !searchOpen && (query.state.data?.pages.length ?? 1) === 1 ? 15_000 : false,
  });
  const pinnedThreads = useMemo(() => [...new Map((pinsQuery.data?.pages.flatMap(({ items }) => items.filter((thread) => thread.pinned)) ?? threads.filter((thread) => thread.pinned))
    .map((thread) => [thread.id, thread])).values()], [pinsQuery.data, threads]);
  const pinnedIDs = useMemo(() => new Set(pinnedThreads.map(({ id }) => id)), [pinnedThreads]);
  const resultCount = searching ? searchResults.length : threads.filter((thread) => !thread.pinned).length + pinnedThreads.length;
  const workspaceNames = useMemo(() => new Map(workspaces.map((workspace) =>
    [workspace.id, workspace.name])), [workspaces]);
  const duplicateNames = useMemo(() => {
    const counts = new Map<string, number>();
    for (const { name } of workspaces) counts.set(name, (counts.get(name) ?? 0) + 1);
    return new Set([...counts].filter(([, count]) => count > 1).map(([name]) => name));
  }, [workspaces]);

  const resolveWorkspaceLabel = (workspaceID: string) => {
    const name = workspaceID ? workspaceNames.get(workspaceID) ?? t("工作区", "Workspace") : t("本地聊天", "Local chats");
    const shortID = workspaceID.length > 16 ? `…${workspaceID.slice(-6)}` : workspaceID;
    return workspaceID && (duplicateNames.has(name) || !workspaceNames.has(workspaceID))
      ? `${name} · ${shortID}`
      : name;
  };

  const resolveThreadSignals = (thread: ThreadView): ThreadLiveSignals => {
    const readFailed = loadFailed || Boolean(thread.pinned && pinsQuery.isError);
    const unread = unreadThreadIDs?.has(thread.id) === true;
    const explicit = lookupThreadSignal(liveSignalsByThread, thread.id);
    return extractCachedThreadSignals(queryClient, thread, unread, readFailed, explicit);
  };

  const allKnownThreads = useMemo(() => {
    const map = new Map<string, ThreadView>();
    for (const thread of pinnedThreads) map.set(thread.id, thread);
    for (const thread of threads) if (!map.has(thread.id)) map.set(thread.id, thread);
    return [...map.values()];
  }, [pinnedThreads, threads]);

  const observedRunIDsRef = useRef<Map<string, string>>(new Map());
  useEffect(() => {
    const seen = observedRunIDsRef.current;
    const currentIDs = new Set<string>();
    for (const thread of allKnownThreads) {
      currentIDs.add(thread.id);
      const runID = thread.active_run_id || thread.last_run_id || "";
      const previousRunID = seen.get(thread.id);
      if (previousRunID !== undefined && previousRunID !== runID) {
        const execKey = v2QueryKeys.execution(thread.id);
        const existing = queryClient.getQueryCache().find({ queryKey: execKey });
        if (existing) {
          if (existing.getObserversCount() > 0) {
            void queryClient.invalidateQueries({ queryKey: execKey });
          } else {
            queryClient.removeQueries({ queryKey: execKey, exact: true });
          }
        }
      }
      seen.set(thread.id, runID);
    }
    for (const id of [...seen.keys()]) {
      if (!currentIDs.has(id)) seen.delete(id);
    }
  }, [allKnownThreads, queryClient]);

  const projectCounts = useMemo(() => {
    void cacheVersion;
    const counts = new Map<string, ProjectSummaryCounts>();
    for (const thread of allKnownThreads) {
      const workspaceID = threadProjectID(thread);
      const activity = deriveThreadActivityStatus(thread, resolveThreadSignals(thread), t, locale);
      const entry = counts.get(workspaceID) ?? { total: 0, needsInput: 0, running: 0, unread: 0 };
      entry.total += 1;
      if (activity.bucket === "needs_input") entry.needsInput += 1;
      else if (activity.bucket === "running") entry.running += 1;
      else if (activity.bucket === "unread") entry.unread += 1;
      counts.set(workspaceID, entry);
    }
    return counts;
  }, [allKnownThreads, cacheVersion, liveSignalsByThread, loadFailed, locale, pinsQuery.isError, t, unreadThreadIDs]);

  const unpinnedFilteredThreads = useMemo(() => {
    return threads.filter((thread) => {
      if (thread.pinned || pinnedIDs.has(thread.id)) return false;
      if (activeProjectFilter !== null && !searchOpen && threadProjectID(thread) !== activeProjectFilter) return false;
      return true;
    });
  }, [threads, pinnedIDs, activeProjectFilter, searchOpen]);

  const visiblePinnedThreads = useMemo(() => {
    if (activeProjectFilter === null || searchOpen) return pinnedThreads;
    return pinnedThreads.filter((thread) => threadProjectID(thread) === activeProjectFilter);
  }, [pinnedThreads, activeProjectFilter, searchOpen]);

  const priorityGrouping = useMemo(() => {
    void cacheVersion;
    return groupThreadsByPriority(unpinnedFilteredThreads, resolveThreadSignals, t, locale);
  }, [unpinnedFilteredThreads, cacheVersion, liveSignalsByThread, loadFailed, locale, pinsQuery.isError, t, unreadThreadIDs]);

  const grouped = useMemo(() => {
    const result = new Map<string, ThreadView[]>();
    for (const thread of threads) {
      if (thread.pinned || pinnedIDs.has(thread.id)) continue;
      const id = threadProjectID(thread);
      if (activeProjectFilter !== null && !searchOpen && id !== activeProjectFilter) continue;
      const group = result.get(id);
      if (group) group.push(thread);
      else result.set(id, [thread]);
    }
    if (onNewInProject && !executionFilter && activeProjectFilter === null) for (const workspace of workspaces) {
      if (!result.has(workspace.id)) result.set(workspace.id, []);
    }
    if (activeProjectFilter !== null && !searchOpen && !result.has(activeProjectFilter)) {
      result.set(activeProjectFilter, []);
    }
    if (draggingThreadID && !result.has("") && activeProjectFilter === null) result.set("", []);
    const order = new Map(workspaces.map((workspace, index) => [workspace.id, index]));
    return [...result.entries()].sort(([a], [b]) => a === "" ? 1 : b === "" ? -1
      : Number(pinnedProjects.has(b)) - Number(pinnedProjects.has(a)) || (order.get(a) ?? order.size) - (order.get(b) ?? order.size));
  }, [threads, workspaces, onNewInProject, draggingThreadID, executionFilter, pinnedIDs, pinnedProjects, activeProjectFilter, searchOpen]);

  // Collect the rows actually rendered below, including pin and collapse order.
  // Publish after commit so abandoned renders cannot change keyboard navigation.
  const visibleChatOrder: string[] = [];
  useLayoutEffect(() => { onVisibleChatOrder?.(searchOpen ? [] : visibleChatOrder); });

  const showPriorityView = !searchOpen && viewMode === "priority";

  const renderThread = (thread: ThreadView, precomputedActivity?: ThreadActivityDescriptor) => {
    visibleChatOrder.push(thread.id);
    const activity = precomputedActivity ?? deriveThreadActivityStatus(thread, resolveThreadSignals(thread), t, locale);
    const canMove = Boolean(onMoveThread && !movingThreadID && thread.project_assignment_version &&
      thread.status === "active" && ["idle", "completed", "failed", "cancelled"].includes(activity.executionState));
    const statusID = `v2-thread-state-${thread.id}`;
    const activityID = `v2-thread-activity-${thread.id}`;
    const unread = unreadThreadIDs?.has(thread.id) === true;
    const workspaceID = threadProjectID(thread);
    const workspaceBadgeLabel = resolveWorkspaceLabel(workspaceID);
    const showWorkspaceBadge = !searchOpen && customPrefs.showWorkspaceBadge && (showPriorityView || Boolean(thread.pinned));
    const showActivitySubtitle = !searchOpen && customPrefs.showActivitySubtitle;
    const showSubline = showActivitySubtitle || showWorkspaceBadge;
    const accessibleActivityText = showActivitySubtitle ? activity.accessibleDetail : undefined;
    const describedBy = [
      statusID,
      accessibleActivityText ? activityID : "",
      unread ? `unread-${thread.id}` : "",
    ].filter(Boolean).join(" ");

    return <div className={`v2-thread-row-shell${selectedThreadID === thread.id ? " is-selected" : ""}`} key={thread.id}>
      <button aria-current={selectedThreadID === thread.id ? "page" : undefined} aria-describedby={describedBy}
        className={`v2-thread-row${onMoveThread ? " has-project-control" : ""}${showSubline ? " has-subline" : ""}`}
        data-activity-kind={activity.kind}
        data-priority-bucket={activity.bucket}
        onClick={() => onSelectThread(thread.id)} type="button"
        draggable={canMove} onDragStart={(event) => {
          if (!canMove) { event.preventDefault(); return; }
          event.dataTransfer.setData("application/x-universal-code-thread", thread.id);
          event.dataTransfer.effectAllowed = "move"; setDraggingThreadID(thread.id); setProjectsExpanded(true);
        }} onDragEnd={() => { setDraggingThreadID(""); setDropProjectID(null); }}>
        <SidebarActivityGlyph activity={activity} />
        <span className="v2-thread-main">
          <span className="v2-thread-top">
            <span className="v2-thread-title">{thread.title}</span>
            {unread && <span className="v2-thread-unread" aria-hidden="true" />}
            {!showActivitySubtitle && (
              <span aria-hidden="true" className={`v2-thread-state state-${activity.executionState}`} title={activity.executionDescription}>
                <i />
              </span>
            )}
            <span aria-hidden="true" className="v2-thread-state-description">{activity.executionLabel}</span>
          </span>
          {showSubline && (
            <span aria-hidden="true" className="v2-thread-subline">
              {showActivitySubtitle && (
                <span
                  className={`v2-thread-activity-pill tone-${activity.tone}${activity.pulse ? " is-pulsing" : ""}`}
                  id={accessibleActivityText ? activityID : undefined}
                >
                  {activity.label}{activity.detail ? ` · ${activity.detail}` : ""}
                </span>
              )}
              {showWorkspaceBadge && workspaceBadgeLabel && (
                <span className="v2-thread-project-badge" title={workspaceBadgeLabel}>
                  {workspaceBadgeLabel}
                </span>
              )}
            </span>
          )}
        </span>
      </button>
      <span className="v2-thread-state-description" id={statusID}>{activity.executionDescription}</span>
      {unread && <span className="v2-thread-state-description" id={`unread-${thread.id}`}>{t("未读", "Unread")}</span>}
      {onMoveThread && <button aria-label={t(`移动 ${thread.title} 到项目`, `Move ${thread.title} to project`)}
        className="v2-thread-more v2-thread-move" disabled={!canMove} title={canMove ? t("移动到项目，也可拖动聊天", "Move to a project, or drag the chat")
          : t("当前任务暂不可移动，请等待执行或待处理消息结束", "Wait for execution and pending messages to finish before moving")}
        onClick={() => setProjectCandidate(thread)} type="button"><FolderInput aria-hidden="true" size={15} /></button>}
      <ThreadActions thread={thread} client={client} onArchive={onArchive} unread={unread} onReadStateChange={onReadStateChange} />
    </div>;
  };

  return <aside className={`v2-sidebar${customPrefs.density === "compact" ? " is-compact" : ""}`} data-sidebar-density={customPrefs.density}>
    <div className="v2-sidebar-brand"><strong>Universal-Code</strong>
      {onRefresh && <button aria-label={t("刷新对话列表", "Refresh conversations")} disabled={searchPending || loading || refreshing || loadingMore}
        onClick={() => { onRefresh(); if (client && !searchOpen) void pinsQuery.refetch(); }} type="button"><RefreshCw aria-hidden="true" size={15} /></button>}
      <SidebarCustomizeControl
        onPrefsChange={updateCustomPrefs}
        onViewModeChange={setViewMode}
        prefs={customPrefs}
        viewMode={viewMode}
      />
      <button aria-label={t("搜索", "Search")} aria-expanded={searchOpen} onClick={() => onSearchOpen(!searchOpen)} type="button">
        {searchOpen ? <X aria-hidden="true" size={16} /> : <Search aria-hidden="true" size={16} />}
      </button></div>
    <nav aria-label={t("对话导航", "Conversation navigation")} className="v2-sidebar-actions">
      <button onClick={onNewConversation} type="button"><SquarePen aria-hidden="true" size={16} />
        <span>{t("新对话", "New conversation")}</span></button>
      <button onClick={onOpenModels} type="button"><Cpu aria-hidden="true" size={16} />
        <span>{t("接入模型", "Connect models")}</span></button>
    </nav>
    {!searchOpen && (
      <SidebarModeBar
        attentionCount={priorityGrouping.attentionCount}
        onViewModeChange={setViewMode}
        projectAction={projectAction}
        viewMode={viewMode}
      />
    )}
    {!searchOpen && onExecutionFilterChange && <div className="v2-chat-filters" role="group" aria-label={t("按执行状态筛选", "Filter by execution status")}>
      {(["", "running", "waiting_approval"] as const).map((value) => <button type="button" key={value} aria-pressed={executionFilter === value}
        onClick={() => onExecutionFilterChange(value)}>{value === "" ? t("全部", "All") : value === "running" ? t("运行中", "Running") : t("待审批", "Awaiting approval")}</button>)}
    </div>}
    {moveError && <p className="v2-history-scope" role="alert">{moveError}</p>}
    {searchOpen && <div className="v2-sidebar-search"><Search aria-hidden="true" size={15} />
      <input aria-label={t("搜索对话", "Search conversations")} aria-invalid={searchTooLong || undefined}
        aria-describedby={`v2-thread-search-scope${searchTooLong ? " v2-thread-search-error" : ""}`} autoFocus
        onChange={(event) => onSearchChange?.(event.target.value)}
        onCompositionStart={() => onSearchCompositionChange?.(true)}
        onCompositionEnd={(event) => {
          onSearchChange?.(event.currentTarget.value);
          onSearchCompositionChange?.(false);
        }} placeholder={searchCurrentThread ? t("搜索当前聊天的消息…", "Search messages in this chat…")
          : t("搜索标题与消息正文…", "Search titles and message text…")} type="search" value={search} />
      {search && <button aria-label={t("清除对话搜索", "Clear conversation search")} onClick={() => onSearchChange?.("")} type="button">
        <X aria-hidden="true" size={14} /></button>}</div>}
    {searchOpen && <>{onSearchCurrentThreadChange && <div className="v2-search-scope" role="group" aria-label={t("搜索范围", "Search scope")}>
      <button type="button" aria-pressed={!searchCurrentThread} onClick={() => onSearchCurrentThreadChange(false)}>{t("所有聊天", "All chats")}</button>
      <button type="button" aria-pressed={searchCurrentThread} disabled={!selectedThreadID}
        onClick={() => onSearchCurrentThreadChange(true)}>{t("当前聊天", "Current chat")}</button>
    </div>}<p className="v2-history-scope" id="v2-thread-search-scope">{searchCurrentThread
      ? t("搜索当前聊天全部已保存的用户、助手正文，包括尚未加载的历史。英文字母忽略大小写，其他文字按原文匹配。",
        "Search all saved user or assistant text in this chat, including history not loaded yet. English letters are case-insensitive; other text matches exactly.")
      : t("搜索全部已保存的对话标题与用户、助手正文。英文字母忽略大小写，其他文字按原文匹配。",
        "Search all saved conversation titles and user or assistant text. English letters are case-insensitive; other text matches exactly.")}</p>
      <label className="v2-search-archived"><input type="checkbox" checked={includeArchived}
        onChange={(event) => onIncludeArchivedChange?.(event.target.checked)} />{t("包含已归档对话", "Include archived conversations")}</label></>}
    {searchTooLong && <p className="v2-history-scope" id="v2-thread-search-error" role="alert">{t("搜索最多支持 256 个字符，请缩短搜索词。", "Searches support up to 256 characters. Shorten your search.")}</p>}
    <div aria-busy={!searchTooLong && (searchPending || loading || refreshing || loadingMore)} className="v2-thread-scroll">
      <p className={searchOpen || loading || loadFailed || truncated || hasMore ? "v2-history-scope" : "v2-thread-state-description"} role="status" aria-label={t("对话列表状态", "Conversation list status")} aria-live="polite" aria-atomic="true">
        {searchTooLong ? t("请缩短搜索词后继续。", "Shorten your search to continue.") : searchPending ? t("正在准备搜索…", "Preparing search…") : loading ? (searching ? t("正在搜索对话…", "Searching conversations…") : t("正在加载对话…", "Loading conversations…"))
          : loadFailed ? (loadMoreFailed ? t("更多记录读取失败，当前列表尚未加载完毕。", "More records could not be loaded. The list is incomplete.") : t("对话列表读取失败，当前执行状态未知。", "The conversation list could not be read. Execution status is unknown."))
          : refreshing ? t("正在刷新对话列表…", "Refreshing conversations…")
          : `${searching ? t("搜索结果：", "Search results: ") : ""}${searching ? t(`已加载 ${resultCount} 条匹配记录；`, `Loaded ${resultCount} ${resultCount === 1 ? "match" : "matches"}; `) : t(`已加载 ${resultCount} 条对话；`, `Loaded ${resultCount} ${resultCount === 1 ? "conversation" : "conversations"}; `)}${truncated
            ? searching ? t("结果达到读取上限，请缩小搜索范围。", "The result limit was reached. Narrow your search.") : t("结果达到读取上限，请用搜索查找更早记录。", "The result limit was reached. Use search to find earlier records.")
            : hasMore ? searching ? t("还有更多匹配结果。", "More matches are available.") : t("还有更早记录。", "Earlier records are available.") : searching ? t("全部匹配结果已加载。", "All matches are loaded.") : t("当前列表已加载完毕。", "The current list is fully loaded.")}`}</p>
      {!searchPending && !loading && !loadFailed && !truncated && resultCount === 0 && (searching || workspaces.length === 0 && !projectAction) && <div className="v2-sidebar-empty"><MessagesSquare aria-hidden="true" size={16} />
        {searching ? searchCurrentThread ? t("当前聊天没有匹配的消息", "No matching messages in this chat")
          : t("没有匹配的标题或消息", "No matching titles or messages") : t("暂无对话", "No conversations yet")}</div>}
      {!searchPending && searching && <div className="v2-message-search-results">{searchResults.map((result) =>
        <button className="v2-message-search-result" key={result.id} type="button" aria-label={result.thread_title}
          aria-describedby={`search-result-${result.id}`}
          onClick={() => onSelectSearchResult?.(result)}>
          <strong>{result.thread_title}</strong>
          <span className="v2-message-search-meta"><span>{result.thread_status === "archived" ? `${t("已归档", "Archived")} · ` : ""}
            {result.role === "title" ? t("标题", "Title") : result.role === "user" ? t("用户", "User") : t("助手", "Assistant")}</span>
            <time dateTime={result.created_at}>{new Date(result.created_at).toLocaleString(locale)}</time></span>
          <span className="v2-message-search-snippet" id={`search-result-${result.id}`}>{result.snippet}</span>
        </button>)}</div>}
      {!searchOpen && (
        <PinnedProjectsQuickBar
          activeProjectFilter={activeProjectFilter}
          onNewInProject={onNewInProject}
          onSelectProjectFilter={setActiveProjectFilter}
          onTogglePin={toggleProjectPin}
          pinnedProjects={pinnedProjects}
          projectCounts={projectCounts}
          resolveWorkspaceLabel={resolveWorkspaceLabel}
          showManagePicker={showPriorityView}
          workspaces={workspaces}
        />
      )}
      {!searchOpen && Boolean(visiblePinnedThreads.length || pinsQuery.isError || pinsQuery.hasNextPage) && <section className="v2-chat-pins" aria-label={t("置顶聊天", "Pinned chats")}>
        {visiblePinnedThreads.length > 0 && <><h2><Pin aria-hidden="true" size={13} />{t("置顶", "Pinned")}</h2>{visiblePinnedThreads.map((thread) => renderThread(thread))}</>}
        {pinsQuery.isError && <p className="v2-history-scope" role="alert">{t("置顶聊天读取失败。", "Could not load pinned chats.")}<button type="button" onClick={() => void pinsQuery.refetch()}>{t("重试", "Retry")}</button></p>}
        {pinsQuery.hasNextPage && <button className="v2-load-history" type="button" disabled={pinsQuery.isFetching} onClick={() => void pinsQuery.fetchNextPage()}>{t("加载更多置顶聊天", "Load more pinned chats")}</button>}
      </section>}
      {!searchOpen && executionFilter && !loading && !loadFailed && !hasMore && resultCount === 0 && <p className="v2-sidebar-empty">{executionFilter === "running"
        ? t("暂无运行中的聊天", "No running chats") : t("暂无待审批的聊天", "No chats awaiting approval")}</p>}
      {showPriorityView && !searchPending && !searching && (
        <>
          <SmartPriorityGroups
            collapsedBuckets={collapsedBuckets}
            groups={priorityGrouping.groups}
            idleShowAll={idleShowAll}
            onOpenCustomize={() => updateCustomPrefs((current) => ({
              ...current,
              visibleGroups: { needs_input: true, running: true, unread: true, idle: true },
            }))}
            onToggleBucket={(bucket) => setCollapsedBuckets((current) => ({ ...current, [bucket]: !current[bucket] }))}
            onToggleIdleShowAll={() => setIdleShowAll((current) => !current)}
            prefs={customPrefs}
            renderThread={renderThread}
          />
          {activeProjectFilter !== null && unpinnedFilteredThreads.length === 0 && visiblePinnedThreads.length === 0 && !loading && (
            <p className="v2-project-empty">{t("所选置顶项目暂无会话", "No chats in the selected pinned project")}</p>
          )}
        </>
      )}
      {!showPriorityView && !searchOpen && <div className="v2-project-section-heading">
        <button className="v2-project-section-toggle" aria-expanded={projectsExpanded} onClick={() => setProjectsExpanded(!projectsExpanded)} type="button">
          {t("项目", "Projects")}<ChevronDown aria-hidden="true" className={projectsExpanded ? "" : "is-collapsed"} size={14} /></button>
        <details className="v2-project-section-menu" onBlur={(event) => {
          if (!event.currentTarget.contains(event.relatedTarget as Node | null)) event.currentTarget.open = false;
        }} onKeyDown={(event) => { if (event.key === "Escape") { event.currentTarget.open = false; event.currentTarget.querySelector("summary")?.focus(); } }}>
          <summary aria-label={t("项目选项", "Project options")}><MoreHorizontal aria-hidden="true" size={17} /></summary>
          <div>{[true, false].map((expand) => <button key={String(expand)} type="button" onClick={(event) => {
            setProjectsExpanded(true); setCollapsedProjects(expand ? new Set() : new Set(workspaces.map(({ id }) => id)));
            event.currentTarget.closest("details")!.open = false;
          }}>{expand ? t("展开所有项目", "Expand all projects") : t("折叠所有项目", "Collapse all projects")}</button>)}</div>
        </details>{projectAction}
      </div>}
      {!showPriorityView && !searchPending && !searching && grouped.filter(([id]) => !id || projectsExpanded || searchOpen).map(([workspaceID, workspaceThreads]) => {
        const label = resolveWorkspaceLabel(workspaceID);
        const draggedThread = [...threads, ...pinnedThreads].find((thread) => thread.id === draggingThreadID);
        const acceptsDrop = Boolean(onMoveThread && draggedThread && !movingThreadID && threadProjectID(draggedThread) !== workspaceID);
        const workspace = workspaces.find(({ id }) => id === workspaceID) ?? { id: workspaceID, name: label, created_at: "" };
        const expanded = !collapsedProjects.has(workspaceID);
        const summary = projectCounts.get(workspaceID);
        return <section aria-label={t(`项目 ${label}`, `Project ${label}`)} className={`v2-thread-group${workspaceID ? "" : " is-local"}${dropProjectID === workspaceID ? " is-drop-target" : ""}`} key={workspaceID}
          onDragOver={(event) => { if (acceptsDrop) { event.preventDefault(); event.dataTransfer.dropEffect = "move"; setDropProjectID(workspaceID); } }}
          onDragLeave={(event) => { if (!event.currentTarget.contains(event.relatedTarget as Node | null)) setDropProjectID(null); }}
          onDrop={(event) => {
            if (!acceptsDrop || !draggedThread || event.dataTransfer.getData("application/x-universal-code-thread") !== draggedThread.id) return;
            event.preventDefault(); setDropProjectID(null); setDraggingThreadID(""); onMoveThread?.(draggedThread, workspaceID);
          }}>
        {workspaceID ? <ProjectHeader workspace={workspace} label={label} client={client} expanded={expanded}
          pinned={pinnedProjects.has(workspaceID)} onTogglePin={() => toggleProjectPin(workspaceID)}
          activitySummary={summary}
          onToggle={() => setCollapsedProjects((previous) => { const next = new Set(previous); if (next.has(workspaceID)) next.delete(workspaceID); else next.add(workspaceID); return next; })}
          onNewChat={onNewInProject ? () => onNewInProject(workspaceID) : undefined} onEdit={onEditProject} />
          : <header className="v2-local-heading"><span>{label}</span>
          {onNewInProject && <button className="v2-project-new-chat" aria-label={t(`在 ${label} 中新建聊天`, `New chat in ${label}`)}
            onClick={() => onNewInProject(workspaceID)} type="button"><SquarePen aria-hidden="true" size={14} /></button>}
        </header>}
        {expanded && workspaceThreads.length === 0 && <p className="v2-project-empty">{draggingThreadID ? t("拖到这里", "Drop here") : t("暂无聊天", "No chats yet")}</p>}
        {expanded && workspaceThreads.map((thread) => renderThread(thread))}
      </section>; })}
      {!searchPending && loadFailed && <p className="v2-history-scope v2-history-error" role="alert">{threads.length ? t("对话列表加载失败，已有记录仍可打开。", "The conversation list could not be loaded. Existing records can still be opened.") : t("对话列表加载失败，请重试读取。", "The conversation list could not be loaded. Please try again.")}
        <button className="v2-retry-action" disabled={refreshing || loadingMore} onClick={onRetry ?? (loadMoreFailed ? onLoadMore : onRefresh)} type="button"><RefreshCw aria-hidden="true" size={14} />{t("重试加载对话", "Retry loading conversations")}</button></p>}
      {!searchPending && hasMore && <button className="v2-load-history" disabled={loadingMore || refreshing || loading}
        onClick={onLoadMore} type="button">{loadingMore ? searching ? t("正在加载更多匹配记录…", "Loading more matches…") : t("正在加载更早对话…", "Loading earlier conversations…")
          : searching ? t("加载更多匹配记录", "Load more matches") : t("加载更早对话", "Load earlier conversations")}</button>}
    </div>
    <div className="v2-sidebar-footer">
      {onOpenAutomations && <button aria-pressed={automationsActive} onClick={onOpenAutomations} type="button">
        <CalendarClock aria-hidden="true" size={16} />{t("自动化", "Automations")}</button>}
      {onOpenHistory && <button aria-pressed={historyActive} onClick={onOpenHistory} type="button">
        <Box aria-hidden="true" size={16} />{t("历史记录", "History")}</button>}
      {onOpenConnections && <button onClick={onOpenConnections} type="button">
        <PlugZap aria-hidden="true" size={16} />{t("连接与环境", "Connections and environment")}</button>}
      <button onClick={onOpenSettings} type="button"><Settings aria-hidden="true" size={16} />{t("设置", "Settings")}</button>
    </div>
    {projectCandidate && onMoveThread && <ThreadProjectDialog thread={projectCandidate} workspaces={workspaces}
      onClose={() => setProjectCandidate(null)} onMove={onMoveThread} />}
  </aside>;
}

const settingsGroups: Array<{ label: [string, string]; items: Array<{
  id: V2SettingsSection; label: [string, string]; icon: typeof Settings; keywords?: string;
}> }> = [
  { label: ["应用", "Application"], items: [
    { id: "general", label: ["常规", "General"], icon: Settings },
    { id: "configuration", label: ["配置", "Configuration"], icon: Settings },
    { id: "appearance", label: ["外观", "Appearance"], icon: Palette },
    { id: "usage", label: ["用量统计", "Usage"], icon: ChartNoAxesCombined },
    { id: "shortcuts", label: ["快捷键", "Shortcuts"], icon: Keyboard },
    { id: "about", label: ["关于", "About"], icon: Info },
  ] },
  { label: ["连接与环境", "Connections and environment"], items: [
    { id: "connections", label: ["连接与环境", "Connections and environment"], icon: PlugZap },
    { id: "models", label: ["模型", "Models"], icon: Cpu },
    { id: "extensions", label: ["扩展与代码智能", "Extensions and code intelligence"], icon: PlugZap },
    { id: "skills", label: ["Skill 包", "Skill packages"], icon: PackageSearch },
    { id: "environment", label: ["执行环境", "Execution environment"], icon: Box, keywords: "终端 terminal shell powershell pwsh cmd 授权" },
  ] },
  { label: ["任务与记录", "Tasks and records"], items: [
    { id: "task-configuration", label: ["任务预算与项目配置", "Task budget and project configuration"], icon: Settings },
    { id: "permissions", label: ["当前任务权限", "Current task permissions"], icon: ShieldCheck },
    { id: "records", label: ["记录显示", "Record preferences"], icon: Box },
  ] },
];

export function V2SettingsSidebar({ section, onBack, onSelect }: {
  section: V2SettingsSection;
  onBack: () => void;
  onSelect: (section: V2SettingsSection) => void;
}) {
  const { t } = useLocale();
  const [search, setSearch] = useState("");
  const normalized = search.trim().toLocaleLowerCase();
  return <aside className="v2-sidebar v2-settings-sidebar">
    <button className="v2-settings-back" onClick={onBack} type="button">
      <ArrowLeft aria-hidden="true" size={16} />{t("返回应用", "Back to app")}</button>
    <label className="v2-settings-search"><Search aria-hidden="true" size={15} />
      <input aria-label={t("搜索设置", "Search settings")} onChange={(event) => setSearch(event.target.value)}
        placeholder={t("搜索设置…", "Search settings…")} type="search" value={search} /></label>
    <nav aria-label={t("设置分类", "Settings categories")} className="v2-settings-nav">
      {settingsGroups.map((group) => {
        const items = group.items.filter((item) => !normalized || `${t(...item.label)} ${item.keywords ?? ""}`.toLocaleLowerCase().includes(normalized));
        if (!items.length) return null;
        return <section key={group.label[0]}><h2>{t(...group.label)}</h2>{items.map(({ id, label, icon: Icon }) => {
          const active = section === id || section === "advanced-models" && id === "models" ||
            section === "plugins" && id === "extensions" || section === "keyboard" && id === "shortcuts";
          return <button aria-current={active ? "page" : undefined}
            className={active ? "is-active" : ""} key={id} onClick={() => onSelect(id)}
            type="button"><Icon aria-hidden="true" size={16} />{t(...label)}</button>;
        })}</section>;
      })}
    </nav>
    <div className="v2-settings-archive"><span>{t("已归档", "Archived")}</span>
      <button aria-current={section === "archived" ? "page" : undefined}
        className={section === "archived" ? "is-active" : ""}
        onClick={() => onSelect("archived")} type="button">
        <ArchiveRestore aria-hidden="true" size={16} />{t("已归档的聊天", "Archived chats")}</button></div>
  </aside>;
}
