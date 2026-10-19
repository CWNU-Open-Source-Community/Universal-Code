import { useMemo, useState, type ReactNode } from "react";
import { Archive, ArchiveRestore, ArrowLeft, ChartNoAxesCombined, Box, ChevronDown, Cpu, FolderInput, MessagesSquare, MoreHorizontal, RefreshCw,
  Info, Keyboard, PackageSearch, Palette, PlugZap, Search, Settings, ShieldCheck, SquarePen, X } from "lucide-react";
import type { ThreadSearchResultView, ThreadView, WorkspaceView } from "../../api/types";
import type { APIClient } from "../../api/client";
import { useLocale } from "../../lib/locale";
import { ThreadProjectDialog, threadProjectID } from "./thread-project-dialog";
import { ProjectHeader } from "./project-header";
import "./project-sidebar.css";

export type V2SettingsSection = "general" | "permissions" | "appearance" | "voice" |
  "models" | "plugins" | "browser" | "hooks" | "git" | "environment" |
  "worktrees" | "keyboard" | "inspector" | "archived" | "extensions" | "skills" |
  "advanced-models" | "about" | "shortcuts" | "connections" | "task-configuration" | "configuration" | "usage";

const executionLabels = {
  idle: ["空闲", "Idle"], running: ["执行中", "Running"], stopping: ["正在停止", "Stopping"], stop_failed: ["停止失败", "Stop failed"],
  waiting_approval: ["等待审批", "Awaiting approval"], paused: ["已暂停", "Paused"], completed: ["已完成", "Completed"], failed: ["执行失败", "Failed"],
  cancelled: ["已取消", "Cancelled"], unknown: ["状态未知", "Unknown"],
} as const;

function executionStatus(thread: ThreadView, readFailed: boolean, t: (chinese: string, english: string) => string) {
  const value = thread.execution_state;
  const state = !readFailed && typeof value === "string" && Object.hasOwn(executionLabels, value) ? value : "unknown";
  const [chinese, english] = executionLabels[state];
  const label = t(chinese, english);
  return { state, label,
    description: readFailed ? t("本次列表读取失败，执行状态未知", "The list could not be read; execution status is unknown") : value && state !== "unknown"
      ? label : t("执行状态未知，请刷新列表重试", "Execution status is unknown; refresh the list to try again") };
}

export function V2Sidebar({ threads, workspaces, selectedThreadID, searchOpen, onSearchOpen,
  onNewConversation, onOpenModels, onSelectThread, onOpenSettings, onArchive,
  onOpenInspector, inspectorActive = false, onOpenConnections,
  search = "", appliedSearch = "", searchPending = false, searchTooLong = false, onSearchChange, onSearchCompositionChange,
  searchResults = [], includeArchived = false, onIncludeArchivedChange, onSelectSearchResult,
  projectAction, onNewInProject, onEditProject, client, onMoveThread, movingThreadID = "", moveError = "",
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
  onOpenSettings: () => void;
  onArchive: (thread: ThreadView) => void;
  onOpenInspector?: () => void;
  onOpenConnections?: () => void;
  inspectorActive?: boolean;
  search?: string; appliedSearch?: string; searchPending?: boolean; searchTooLong?: boolean;
  onSearchChange?: (value: string) => void;
  onSearchCompositionChange?: (composing: boolean) => void;
  searchResults?: ThreadSearchResultView[];
  includeArchived?: boolean;
  onIncludeArchivedChange?: (value: boolean) => void;
  onSelectSearchResult?: (result: ThreadSearchResultView) => void;
  projectAction?: ReactNode;
  onNewInProject?: (projectID: string) => void;
  onEditProject?: (workspace: WorkspaceView) => void;
  client?: APIClient;
  onMoveThread?: (thread: ThreadView, projectID: string) => void;
  movingThreadID?: string;
  moveError?: string;
  hasMore?: boolean; truncated?: boolean; loading?: boolean; loadingMore?: boolean; loadFailed?: boolean;
  loadMoreFailed?: boolean; refreshing?: boolean;
  onLoadMore?: () => void; onRefresh?: () => void; onRetry?: () => void;
}) {
  const { t, locale } = useLocale();
  const searching = Boolean(appliedSearch);
  const [draggingThreadID, setDraggingThreadID] = useState("");
  const [dropProjectID, setDropProjectID] = useState<string | null>(null);
  const [projectCandidate, setProjectCandidate] = useState<ThreadView | null>(null);
  const [projectsExpanded, setProjectsExpanded] = useState(true);
  const [collapsedProjects, setCollapsedProjects] = useState<Set<string>>(() => new Set());
  const resultCount = searching ? searchResults.length : threads.length;
  const workspaceNames = useMemo(() => new Map(workspaces.map((workspace) =>
    [workspace.id, workspace.name])), [workspaces]);
  const grouped = useMemo(() => {
    const result = new Map<string, ThreadView[]>();
    for (const thread of threads) {
      const id = threadProjectID(thread);
      const group = result.get(id);
      if (group) group.push(thread);
      else result.set(id, [thread]);
    }
    if (onNewInProject) for (const workspace of workspaces) {
      if (!result.has(workspace.id)) result.set(workspace.id, []);
    }
    if (draggingThreadID && !result.has("")) result.set("", []);
    const order = new Map(workspaces.map((workspace, index) => [workspace.id, index]));
    return [...result.entries()].sort(([a], [b]) => a === "" ? 1 : b === "" ? -1
      : (order.get(a) ?? order.size) - (order.get(b) ?? order.size));
  }, [threads, workspaces, onNewInProject, draggingThreadID]);
  const duplicateNames = useMemo(() => {
    const counts = new Map<string, number>();
    for (const { name } of workspaces) counts.set(name, (counts.get(name) ?? 0) + 1);
    return new Set([...counts].filter(([, count]) => count > 1).map(([name]) => name));
  }, [workspaces]);

  return <aside className="v2-sidebar">
    <div className="v2-sidebar-brand"><strong>Universal-Code</strong>
      {onRefresh && <button aria-label={t("刷新对话列表", "Refresh conversations")} disabled={searchPending || loading || refreshing || loadingMore}
        onClick={onRefresh} type="button"><RefreshCw aria-hidden="true" size={15} /></button>}
      <button aria-label={t("搜索", "Search")} aria-expanded={searchOpen} onClick={() => onSearchOpen(!searchOpen)} type="button">
        {searchOpen ? <X aria-hidden="true" size={16} /> : <Search aria-hidden="true" size={16} />}
      </button></div>
    <nav aria-label={t("对话导航", "Conversation navigation")} className="v2-sidebar-actions">
      <button onClick={onNewConversation} type="button"><SquarePen aria-hidden="true" size={16} />
        <span>{t("新对话", "New conversation")}</span></button>
      <button onClick={onOpenModels} type="button"><Cpu aria-hidden="true" size={16} />
        <span>{t("接入模型", "Connect models")}</span></button>
    </nav>
    {moveError && <p className="v2-history-scope" role="alert">{moveError}</p>}
    {searchOpen && <div className="v2-sidebar-search"><Search aria-hidden="true" size={15} />
      <input aria-label={t("搜索对话", "Search conversations")} aria-invalid={searchTooLong || undefined}
        aria-describedby={`v2-thread-search-scope${searchTooLong ? " v2-thread-search-error" : ""}`} autoFocus
        onChange={(event) => onSearchChange?.(event.target.value)}
        onCompositionStart={() => onSearchCompositionChange?.(true)}
        onCompositionEnd={(event) => {
          onSearchChange?.(event.currentTarget.value);
          onSearchCompositionChange?.(false);
        }} placeholder={t("搜索标题与消息正文…", "Search titles and message text…")} type="search" value={search} />
      {search && <button aria-label={t("清除对话搜索", "Clear conversation search")} onClick={() => onSearchChange?.("")} type="button">
        <X aria-hidden="true" size={14} /></button>}</div>}
    {searchOpen && <><p className="v2-history-scope" id="v2-thread-search-scope">{t("搜索全部已保存的对话标题与用户、助手正文。英文字母忽略大小写，其他文字按原文匹配。",
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
        {searching ? t("没有匹配的标题或消息", "No matching titles or messages") : t("暂无对话", "No conversations yet")}</div>}
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
      {!searchOpen && <div className="v2-project-section-heading">
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
      {!searchPending && !searching && grouped.filter(([id]) => !id || projectsExpanded || searchOpen).map(([workspaceID, workspaceThreads]) => {
        const name = workspaceID ? workspaceNames.get(workspaceID) ?? t("工作区", "Workspace") : t("本地聊天", "Local chats");
        const shortID = workspaceID.length > 16 ? `…${workspaceID.slice(-6)}` : workspaceID;
        const label = workspaceID && (duplicateNames.has(name) || !workspaceNames.has(workspaceID))
          ? `${name} · ${shortID}` : name;
        const draggedThread = threads.find((thread) => thread.id === draggingThreadID);
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
          onToggle={() => setCollapsedProjects((previous) => { const next = new Set(previous); if (next.has(workspaceID)) next.delete(workspaceID); else next.add(workspaceID); return next; })}
          onNewChat={onNewInProject ? () => onNewInProject(workspaceID) : undefined} onEdit={onEditProject} />
          : <header className="v2-local-heading"><span>{label}</span>
          {onNewInProject && <button className="v2-project-new-chat" aria-label={t(`在 ${label} 中新建聊天`, `New chat in ${label}`)}
            onClick={() => onNewInProject(workspaceID)} type="button"><SquarePen aria-hidden="true" size={14} /></button>}
        </header>}
        {expanded && workspaceThreads.length === 0 && <p className="v2-project-empty">{draggingThreadID ? t("拖到这里", "Drop here") : t("暂无聊天", "No chats yet")}</p>}
        {expanded && workspaceThreads.map((thread) => {
          const execution = executionStatus(thread, loadFailed, t);
          const canMove = Boolean(onMoveThread && !movingThreadID && thread.project_assignment_version &&
            thread.status === "active" && ["idle", "completed", "failed", "cancelled"].includes(execution.state));
          const statusID = `v2-thread-state-${thread.id}`;
          return <div className={`v2-thread-row-shell${selectedThreadID === thread.id
          ? " is-selected" : ""}`} key={thread.id}>
          <button aria-current={selectedThreadID === thread.id ? "page" : undefined} aria-describedby={statusID}
            className={`v2-thread-row${onMoveThread ? " has-project-control" : ""}`} onClick={() => onSelectThread(thread.id)} type="button"
            draggable={canMove} onDragStart={(event) => {
              if (!canMove) { event.preventDefault(); return; }
              event.dataTransfer.setData("application/x-universal-code-thread", thread.id);
              event.dataTransfer.effectAllowed = "move"; setDraggingThreadID(thread.id); setProjectsExpanded(true);
            }} onDragEnd={() => { setDraggingThreadID(""); setDropProjectID(null); }}>
            <span className="v2-thread-title">{thread.title}</span>
            <span aria-hidden="true" className={`v2-thread-state state-${execution.state}`} title={execution.description}>
              <i /><span className="v2-thread-state-description">{execution.label}</span></span>
          </button>
          <span className="v2-thread-state-description" id={statusID}>{execution.description}</span>
          {onMoveThread && <button aria-label={t(`移动 ${thread.title} 到项目`, `Move ${thread.title} to project`)}
            className="v2-thread-more v2-thread-move" disabled={!canMove} title={canMove ? t("移动到项目，也可拖动聊天", "Move to a project, or drag the chat")
              : t("当前任务暂不可移动，请等待执行或待处理消息结束", "Wait for execution and pending messages to finish before moving")}
            onClick={() => setProjectCandidate(thread)} type="button"><FolderInput aria-hidden="true" size={15} /></button>}
          <button aria-label={t(`归档 ${thread.title}`, `Archive ${thread.title}`)} className="v2-thread-more"
            onClick={() => onArchive(thread)} title={t("归档此对话", "Archive this conversation")} type="button">
            <Archive aria-hidden="true" size={15} />
          </button>
        </div>; })}
      </section>; })}
      {!searchPending && loadFailed && <p className="v2-history-scope" role="alert">{threads.length ? t("对话列表加载失败，已有记录仍可打开。", "The conversation list could not be loaded. Existing records can still be opened.") : t("对话列表加载失败，请重试读取。", "The conversation list could not be loaded. Please try again.")}
        <button disabled={refreshing || loadingMore} onClick={onRetry ?? (loadMoreFailed ? onLoadMore : onRefresh)} type="button">{t("重试加载对话", "Retry loading conversations")}</button></p>}
      {!searchPending && hasMore && <button className="v2-load-history" disabled={loadingMore || refreshing || loading}
        onClick={onLoadMore} type="button">{loadingMore ? searching ? t("正在加载更多匹配记录…", "Loading more matches…") : t("正在加载更早对话…", "Loading earlier conversations…")
          : searching ? t("加载更多匹配记录", "Load more matches") : t("加载更早对话", "Load earlier conversations")}</button>}
    </div>
    <div className="v2-sidebar-footer">
      {onOpenInspector && <button aria-pressed={inspectorActive} onClick={onOpenInspector} type="button">
        <Box aria-hidden="true" size={16} />{t("观察与记录", "Observation and records")}</button>}
      {onOpenConnections && <button onClick={onOpenConnections} type="button">
        <PlugZap aria-hidden="true" size={16} />{t("连接与环境", "Connections and environment")}</button>}
      <button onClick={onOpenSettings} type="button"><Settings aria-hidden="true" size={16} />{t("设置", "Settings")}</button>
    </div>
    {projectCandidate && onMoveThread && <ThreadProjectDialog thread={projectCandidate} workspaces={workspaces}
      onClose={() => setProjectCandidate(null)} onMove={onMoveThread} />}
  </aside>;
}

const settingsGroups: Array<{ label: [string, string]; items: Array<{
  id: V2SettingsSection; label: [string, string]; icon: typeof Settings;
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
    { id: "environment", label: ["执行环境", "Execution environment"], icon: Box },
  ] },
  { label: ["任务与观察", "Tasks and observation"], items: [
    { id: "task-configuration", label: ["任务预算与项目配置", "Task budget and project configuration"], icon: Settings },
    { id: "permissions", label: ["当前任务权限", "Current task permissions"], icon: ShieldCheck },
    { id: "inspector", label: ["观察视图偏好", "Inspector preferences"], icon: Box },
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
        const items = group.items.filter((item) => !normalized || t(...item.label).toLocaleLowerCase().includes(normalized));
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
