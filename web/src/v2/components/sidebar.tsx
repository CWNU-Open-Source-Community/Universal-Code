import { useLayoutEffect, useMemo, useState, type ReactNode } from "react";
import { useInfiniteQuery } from "@tanstack/react-query";
import { ArchiveRestore, ArrowLeft, ChartNoAxesCombined, Box, CalendarClock, ChevronDown, Cpu, FolderInput, MessagesSquare, MoreHorizontal, Pin, RefreshCw,
  Info, Keyboard, PackageSearch, Palette, PlugZap, Search, Settings, ShieldCheck, SquarePen, X } from "lucide-react";
import type { ThreadSearchResultView, ThreadView, WorkspaceView } from "../../api/types";
import type { APIClient } from "../../api/client";
import { useLocale } from "../../lib/locale";
import { ThreadProjectDialog, threadProjectID } from "./thread-project-dialog";
import { ProjectHeader } from "./project-header";
import { ThreadActions } from "./thread-actions";
import { executionLabels, executionState, priorityGroups, threadPriority, type Priority } from "./sidebar-priority";
import { useV2PersistentState } from "../recovery-storage";
import { shortcutBinding, shortcutLabel, useChatPreferences } from "../chat-preferences";
import "./project-sidebar.css";

function projectPinIDs(value: unknown): string[] {
  return Array.isArray(value) ? value.filter((id): id is string => typeof id === "string" && id.length > 0) : [];
}

export type V2SettingsSection = "general" | "permissions" | "appearance" | "voice" |
  "models" | "plugins" | "browser" | "hooks" | "git" | "environment" |
  "worktrees" | "keyboard" | "records" | "archived" | "extensions" | "skills" |
  "advanced-models" | "about" | "shortcuts" | "connections" | "task-configuration" | "configuration" | "usage";

function executionStatus(thread: ThreadView, readFailed: boolean, t: (chinese: string, english: string) => string) {
  const state = executionState(thread, readFailed);
  const [chinese, english] = executionLabels[state];
  const label = t(chinese, english);
  return { state, label,
    description: readFailed ? t("本次列表读取失败，执行状态未知", "The list could not be read; execution status is unknown") : state !== "unknown"
      ? label : t("执行状态未知，请刷新列表重试", "Execution status is unknown; refresh the list to try again") };
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
  visible?: boolean;
  hasMore?: boolean; truncated?: boolean; loading?: boolean; loadingMore?: boolean; loadFailed?: boolean;
  loadMoreFailed?: boolean; refreshing?: boolean;
  onLoadMore?: () => void; onRefresh?: () => void; onRetry?: () => void;
}) {
  const { t, locale } = useLocale();
  const preferences = useChatPreferences();
  const newChatKeys = shortcutLabel(shortcutBinding(preferences, "newChat"));
  const searching = Boolean(appliedSearch);
  const [draggingThreadID, setDraggingThreadID] = useState("");
  const [dropProjectID, setDropProjectID] = useState<string | null>(null);
  const [projectCandidate, setProjectCandidate] = useState<ThreadView | null>(null);
  const [groupsExpanded, setGroupsExpanded] = useState(true);
  const [collapsedProjects, setCollapsedProjects] = useState<Set<string>>(() => new Set());
  const [viewMode, setViewMode] = useV2PersistentState("sidebar-view-mode", "project");
  const showPriority = !searchOpen && viewMode === "priority";
  const [collapsedPriorities, setCollapsedPriorities] = useState<Partial<Record<Priority, boolean>>>({});
  const [savedProjectPins, setProjectPins] = useV2PersistentState<unknown>("pinned-projects", []);
  const pinnedProjects = useMemo(() => new Set(projectPinIDs(savedProjectPins)), [savedProjectPins]);
  const toggleProjectPin = (id: string) => setProjectPins((previous: unknown) => {
    const ids = projectPinIDs(previous);
    return ids.includes(id) ? ids.filter((value) => value !== id) : [...ids, id];
  });
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
  const unpinnedThreads = useMemo(() => threads.filter((thread) => !thread.pinned && !pinnedIDs.has(thread.id)), [threads, pinnedIDs]);
  const resultCount = searching ? searchResults.length : unpinnedThreads.length + pinnedThreads.length;
  const workspaceNames = useMemo(() => new Map(workspaces.map((workspace) =>
    [workspace.id, workspace.name])), [workspaces]);
  const grouped = useMemo(() => {
    const result = new Map<string, ThreadView[]>();
    if (showPriority) return [];
    for (const thread of unpinnedThreads) {
      const id = threadProjectID(thread);
      const group = result.get(id);
      if (group) group.push(thread);
      else result.set(id, [thread]);
    }
    if (onNewInProject && !executionFilter) for (const workspace of workspaces) {
      if (!result.has(workspace.id)) result.set(workspace.id, []);
    }
    if (draggingThreadID && !result.has("")) result.set("", []);
    const order = new Map(workspaces.map((workspace, index) => [workspace.id, index]));
    return [...result.entries()].sort(([a], [b]) => a === "" ? 1 : b === "" ? -1
      : Number(pinnedProjects.has(b)) - Number(pinnedProjects.has(a)) || (order.get(a) ?? order.size) - (order.get(b) ?? order.size));
  }, [unpinnedThreads, workspaces, onNewInProject, draggingThreadID, executionFilter, pinnedProjects, showPriority]);
  const byPriority = useMemo(() => {
    const result: Record<Priority, ThreadView[]> = { needs_input: [], running: [], unread: [], idle: [], unknown: [] };
    if (showPriority) for (const thread of unpinnedThreads) {
      result[threadPriority(thread, loadFailed, unreadThreadIDs?.has(thread.id) === true)].push(thread);
    }
    return result;
  }, [showPriority, unpinnedThreads, loadFailed, unreadThreadIDs]);
  const showEmptyList = showPriority
    ? !executionFilter && !hasMore && (!client || (pinsQuery.isSuccess && !pinsQuery.hasNextPage))
    : workspaces.length === 0 && !projectAction;
  const duplicateNames = useMemo(() => {
    const counts = new Map<string, number>();
    for (const { name } of workspaces) counts.set(name, (counts.get(name) ?? 0) + 1);
    return new Set([...counts].filter(([, count]) => count > 1).map(([name]) => name));
  }, [workspaces]);

  // Collect the rows actually rendered below, including pin and collapse order.
  // Publish after commit so abandoned renders cannot change keyboard navigation.
  const visibleChatOrder: string[] = [];
  useLayoutEffect(() => { onVisibleChatOrder?.(searchOpen ? [] : visibleChatOrder); });
  const renderThread = (thread: ThreadView) => {
    visibleChatOrder.push(thread.id);
    const execution = executionStatus(thread, loadFailed || Boolean(thread.pinned && pinsQuery.isError), t);
    const canMove = Boolean(onMoveThread && !movingThreadID && thread.project_assignment_version &&
      thread.status === "active" && ["idle", "completed", "failed", "cancelled"].includes(execution.state));
    const canDrag = canMove && !showPriority;
    const statusID = `v2-thread-state-${thread.id}`;
    const unread = unreadThreadIDs?.has(thread.id) === true;
    return <div className={`v2-thread-row-shell${selectedThreadID === thread.id ? " is-selected" : ""}`} key={thread.id}>
      <button aria-current={selectedThreadID === thread.id ? "page" : undefined} aria-describedby={`${statusID}${unread ? ` unread-${thread.id}` : ""}`}
        className={`v2-thread-row${onMoveThread ? " has-project-control" : ""}`} onClick={() => onSelectThread(thread.id)} type="button"
        draggable={canDrag} onDragStart={(event) => {
          if (!canDrag) { event.preventDefault(); return; }
          event.dataTransfer.setData("application/x-universal-code-thread", thread.id);
          event.dataTransfer.effectAllowed = "move"; setDraggingThreadID(thread.id); setGroupsExpanded(true);
        }} onDragEnd={() => { setDraggingThreadID(""); setDropProjectID(null); }}>
        <span className="v2-thread-title">{thread.title}</span>
        {unread && <span className="v2-thread-unread" aria-hidden="true" />}
        <span aria-hidden="true" className={`v2-thread-state state-${execution.state}`} title={execution.description}>
          <i /><span className="v2-thread-state-description">{execution.label}</span></span>
      </button>
      <span className="v2-thread-state-description" id={statusID}>{execution.description}</span>
      {unread && <span className="v2-thread-state-description" id={`unread-${thread.id}`}>{t("未读", "Unread")}</span>}
      {onMoveThread && <button aria-label={t(`移动 ${thread.title} 到项目`, `Move ${thread.title} to project`)}
        className="v2-thread-more v2-thread-move" disabled={!canMove} title={canDrag ? t("移动到项目，也可拖动聊天", "Move to a project, or drag the chat")
          : canMove ? t("移动到项目", "Move to a project")
          : t("当前任务暂不可移动，请等待执行或待处理消息结束", "Wait for execution and pending messages to finish before moving")}
        onClick={() => setProjectCandidate(thread)} type="button"><FolderInput aria-hidden="true" size={15} /></button>}
      <ThreadActions thread={thread} client={client} onArchive={onArchive} unread={unread} onReadStateChange={onReadStateChange} />
    </div>;
  };

  return <aside className="v2-sidebar">
    <div className="v2-sidebar-brand"><strong>Universal-Code</strong>
      {onRefresh && <button aria-label={t("刷新对话列表", "Refresh conversations")} disabled={searchPending || loading || refreshing || loadingMore}
        onClick={() => { onRefresh(); if (client && !searchOpen) void pinsQuery.refetch(); }} type="button"><RefreshCw aria-hidden="true" size={15} /></button>}
      <button aria-label={t("搜索", "Search")} aria-expanded={searchOpen} onClick={() => onSearchOpen(!searchOpen)} type="button">
        {searchOpen ? <X aria-hidden="true" size={16} /> : <Search aria-hidden="true" size={16} />}
      </button></div>
    <nav aria-label={t("对话导航", "Conversation navigation")} className="v2-sidebar-actions">
      <button onClick={onNewConversation} type="button"><SquarePen aria-hidden="true" size={16} />
        <span>{t("新对话", "New conversation")}</span>{newChatKeys && <kbd aria-hidden="true">{newChatKeys}</kbd>}</button>
      <button onClick={onOpenModels} type="button"><Cpu aria-hidden="true" size={16} />
        <span>{t("接入模型", "Connect models")}</span></button>
    </nav>
    {!searchOpen && onExecutionFilterChange && <div className="v2-chat-filters" role="group" aria-label={t("按执行状态筛选", "Filter by execution status")}>
      {(["", "running", "waiting_approval"] as const).map((value) => <button type="button" key={value} aria-pressed={executionFilter === value}
        onClick={() => onExecutionFilterChange(value)}>{value === "" ? t("全部", "All") : value === "running" ? t("运行中", "Running") : t("待审批", "Awaiting approval")}</button>)}
    </div>}
    {moveError && <p className="v2-history-scope" role="alert">{moveError}</p>}
    {searchOpen && <div className="v2-sidebar-search"><Search aria-hidden="true" size={15} />
      <input aria-label={t("搜索对话", "Search conversations")} aria-invalid={searchTooLong || undefined}
        aria-describedby={searchTooLong ? "v2-thread-search-error" : undefined} autoFocus
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
    </div>}
      <label className="v2-search-archived"><input type="checkbox" checked={includeArchived}
        onChange={(event) => onIncludeArchivedChange?.(event.target.checked)} />{t("包含已归档对话", "Include archived conversations")}</label></>}
    {searchTooLong && <p className="v2-history-scope" id="v2-thread-search-error" role="alert">{t("搜索最多支持 256 个字符，请缩短搜索词。", "Searches support up to 256 characters. Shorten your search.")}</p>}
    <div aria-busy={!searchTooLong && (searchPending || loading || refreshing || loadingMore)} className="v2-thread-scroll">
      <p className={!searchTooLong && (searching && resultCount > 0 || loading || loadFailed || truncated) ? "v2-history-scope" : "v2-thread-state-description"} role="status" aria-label={t("对话列表状态", "Conversation list status")} aria-live="polite" aria-atomic="true">
        {searchTooLong ? "" : searchPending ? t("正在准备搜索…", "Preparing search…") : loading ? (searching ? t("正在搜索对话…", "Searching conversations…") : t("正在加载对话…", "Loading conversations…"))
          : loadFailed ? (loadMoreFailed ? t("更多记录读取失败", "Could not load more records") : t("对话列表读取失败，当前执行状态未知。", "The conversation list could not be read. Execution status is unknown."))
          : refreshing ? t("正在刷新对话列表…", "Refreshing conversations…")
          : truncated ? (searching ? t("结果达到读取上限，请缩小搜索范围。", "Result limit reached. Narrow your search.")
            : t("结果达到读取上限，可搜索更早记录。", "List limit reached. Search for earlier records."))
          : searching ? t(`${resultCount} 条匹配记录`, `${resultCount} ${resultCount === 1 ? "match" : "matches"}`)
          : t(`${resultCount} 条对话`, `${resultCount} ${resultCount === 1 ? "conversation" : "conversations"}`)}</p>
      {!searchPending && !loading && !loadFailed && !truncated && resultCount === 0 && (searching || showEmptyList) && <div className="v2-sidebar-empty"><MessagesSquare aria-hidden="true" size={16} />
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
      {!searchOpen && Boolean(pinnedThreads.length || pinsQuery.isError || pinsQuery.hasNextPage) && <section className="v2-chat-pins" aria-label={t("置顶聊天", "Pinned chats")}>
        {pinnedThreads.length > 0 && <><h2><Pin aria-hidden="true" size={13} />{t("置顶", "Pinned")}</h2>{pinnedThreads.map(renderThread)}</>}
        {pinsQuery.isError && <p className="v2-history-scope" role="alert">{t("置顶聊天读取失败。", "Could not load pinned chats.")}<button type="button" onClick={() => void pinsQuery.refetch()}>{t("重试", "Retry")}</button></p>}
        {pinsQuery.hasNextPage && <button className="v2-load-history" type="button" disabled={pinsQuery.isFetching} onClick={() => void pinsQuery.fetchNextPage()}>{t("加载更多置顶聊天", "Load more pinned chats")}</button>}
      </section>}
      {!searchOpen && executionFilter && !loading && !loadFailed && !hasMore && resultCount === 0 && <p className="v2-sidebar-empty">{executionFilter === "running"
        ? t("暂无运行中的聊天", "No running chats") : t("暂无待审批的聊天", "No chats awaiting approval")}</p>}
      {!searchOpen && <div className="v2-project-section-heading">
        <button className="v2-project-section-toggle" aria-expanded={groupsExpanded} onClick={() => setGroupsExpanded(!groupsExpanded)} type="button">
          {showPriority ? t("优先级", "Priority") : t("项目", "Projects")}
          <ChevronDown aria-hidden="true" className={groupsExpanded ? "" : "is-collapsed"} size={14} /></button>
        <details className="v2-project-section-menu" onBlur={(event) => {
          if (!event.currentTarget.contains(event.relatedTarget as Node | null)) event.currentTarget.open = false;
        }} onKeyDown={(event) => { if (event.key === "Escape" && event.currentTarget.open) {
          event.preventDefault();
          event.currentTarget.open = false;
          event.currentTarget.querySelector("summary")?.focus();
        } }}>
          <summary aria-label={showPriority ? t("分组选项", "Grouping options") : t("项目选项", "Project options")}><MoreHorizontal aria-hidden="true" size={17} /></summary>
          <div><button type="button" onClick={(event) => {
            setViewMode(showPriority ? "project" : "priority");
            setGroupsExpanded(true);
            const menu = event.currentTarget.closest("details")!;
            menu.open = false;
            menu.querySelector("summary")?.focus();
          }}>{showPriority ? t("按项目分组", "Group by project") : t("按优先级分组", "Group by priority")}</button>
          {!showPriority && [true, false].map((expand) => <button key={String(expand)} type="button" onClick={(event) => {
            setGroupsExpanded(true); setCollapsedProjects(expand ? new Set() : new Set(workspaces.map(({ id }) => id)));
            event.currentTarget.closest("details")!.open = false;
          }}>{expand ? t("展开所有项目", "Expand all projects") : t("折叠所有项目", "Collapse all projects")}</button>)}</div>
        </details>{projectAction}
      </div>}
      {showPriority && groupsExpanded && !searchPending && !searching && priorityGroups.map(([key, chinese, english]) => {
        const members = byPriority[key];
        if (!members.length) return null;
        const label = t(chinese, english);
        const expanded = !collapsedPriorities[key];
        return <section aria-label={label} className="v2-thread-group" key={key}>
          <header className="v2-project-row"><button type="button" className="v2-project-title" aria-expanded={expanded}
            onClick={() => setCollapsedPriorities((previous) => ({ ...previous, [key]: !previous[key] }))}>
            <ChevronDown aria-hidden="true" className="v2-priority-chevron" size={15} />
            {label}<span className="v2-priority-count">{members.length}</span>
          </button></header>
          {expanded && members.map(renderThread)}
        </section>;
      })}
      {!searchPending && !searching && grouped.filter(([id]) => !id || groupsExpanded || searchOpen).map(([workspaceID, workspaceThreads]) => {
        const name = workspaceID ? workspaceNames.get(workspaceID) ?? t("工作区", "Workspace") : t("本地聊天", "Local chats");
        const shortID = workspaceID.length > 16 ? `…${workspaceID.slice(-6)}` : workspaceID;
        const label = workspaceID && (duplicateNames.has(name) || !workspaceNames.has(workspaceID))
          ? `${name} · ${shortID}` : name;
        const draggedThread = [...threads, ...pinnedThreads].find((thread) => thread.id === draggingThreadID);
        const acceptsDrop = Boolean(onMoveThread && draggedThread && !movingThreadID && threadProjectID(draggedThread) !== workspaceID);
        const workspace = workspaces.find(({ id }) => id === workspaceID) ?? { id: workspaceID, name: label, created_at: "" };
        const expanded = !collapsedProjects.has(workspaceID);
        return <section aria-label={t(`项目 ${label}`, `Project ${label}`)} className={`v2-thread-group${workspaceID ? "" : " is-local"}${dropProjectID === workspaceID ? " is-drop-target" : ""}`} key={workspaceID}
          onDragOver={(event) => { if (acceptsDrop) { event.preventDefault(); event.dataTransfer.dropEffect = "move"; setDropProjectID(workspaceID); } }}
          onDragLeave={(event) => { if (!event.currentTarget.contains(event.relatedTarget as Node | null)) setDropProjectID(null); }}
          onDrop={(event) => {
            if (!acceptsDrop || !draggedThread || event.dataTransfer.getData("application/x-universal-code-thread") !== draggedThread.id) return;
            event.preventDefault(); setDropProjectID(null); setDraggingThreadID(""); onMoveThread?.(draggedThread, workspaceID);
          }}>
        {workspaceID ? <ProjectHeader workspace={workspace} label={label} client={client} expanded={expanded}
          pinned={pinnedProjects.has(workspaceID)} onTogglePin={() => toggleProjectPin(workspaceID)}
          onToggle={() => setCollapsedProjects((previous) => { const next = new Set(previous); if (next.has(workspaceID)) next.delete(workspaceID); else next.add(workspaceID); return next; })}
          onNewChat={onNewInProject ? () => onNewInProject(workspaceID) : undefined} onEdit={onEditProject} />
          : <header className="v2-local-heading"><span>{label}</span>
          {onNewInProject && <button className="v2-project-new-chat" aria-label={t(`在 ${label} 中新建聊天`, `New chat in ${label}`)}
            onClick={() => onNewInProject(workspaceID)} type="button"><SquarePen aria-hidden="true" size={14} /></button>}
        </header>}
        {expanded && workspaceThreads.length === 0 && <p className="v2-project-empty">{draggingThreadID ? t("拖到这里", "Drop here") : t("暂无聊天", "No chats yet")}</p>}
        {expanded && workspaceThreads.map(renderThread)}
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
  const groups = settingsGroups.map(({ label, items }) => ({
    label,
    items: items.filter((item) => !normalized || `${t(...item.label)} ${item.keywords ?? ""}`.toLocaleLowerCase().includes(normalized)),
  })).filter(({ items }) => items.length);
  return <aside className="v2-sidebar v2-settings-sidebar">
    <button className="v2-settings-back" onClick={onBack} type="button">
      <ArrowLeft aria-hidden="true" size={16} />{t("返回应用", "Back to app")}</button>
    <label className="v2-settings-search"><Search aria-hidden="true" size={15} />
      <input aria-label={t("搜索设置", "Search settings")} onChange={(event) => setSearch(event.target.value)}
        placeholder={t("搜索设置…", "Search settings…")} type="search" value={search} /></label>
    <nav aria-label={t("设置分类", "Settings categories")} className="v2-settings-nav">
      {groups.length === 0 && <p className="v2-settings-no-results" role="status">{t("没有匹配的设置", "No matching settings")}</p>}
      {groups.map((group) =>
        <section key={group.label[0]}><h2>{t(...group.label)}</h2>{group.items.map(({ id, label, icon: Icon }) => {
          const active = section === id || section === "advanced-models" && id === "models" ||
            section === "plugins" && id === "extensions" || section === "keyboard" && id === "shortcuts";
          return <button aria-current={active ? "page" : undefined}
            className={active ? "is-active" : ""} key={id} onClick={() => onSelect(id)}
            type="button"><Icon aria-hidden="true" size={16} />{t(...label)}</button>;
        })}</section>)}
    </nav>
    <div className="v2-settings-archive">
      <button aria-current={section === "archived" ? "page" : undefined}
        className={section === "archived" ? "is-active" : ""}
        onClick={() => onSelect("archived")} type="button">
        <ArchiveRestore aria-hidden="true" size={16} />{t("已归档的聊天", "Archived chats")}</button></div>
  </aside>;
}
