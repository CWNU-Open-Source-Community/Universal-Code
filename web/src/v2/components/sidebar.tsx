import { useMemo, useState } from "react";
import { Archive, ArchiveRestore, ArrowLeft, Box, Cpu, Folder, MessagesSquare, RefreshCw,
  Info, Keyboard, PackageSearch, Palette, PlugZap, Search, Settings, ShieldCheck, SquarePen, X } from "lucide-react";
import type { ThreadView, WorkspaceView } from "../../api/types";

export type V2SettingsSection = "general" | "permissions" | "appearance" | "voice" |
  "models" | "plugins" | "browser" | "hooks" | "git" | "environment" |
  "worktrees" | "keyboard" | "inspector" | "archived" | "extensions" | "skills" |
  "advanced-models" | "about" | "shortcuts" | "connections" | "task-configuration";

const executionLabels = {
  idle: "空闲", running: "执行中", stopping: "正在停止", stop_failed: "停止失败",
  waiting_approval: "等待审批", paused: "已暂停", completed: "已完成", failed: "执行失败",
  cancelled: "已取消", unknown: "状态未知",
} as const;

function executionStatus(thread: ThreadView, readFailed: boolean) {
  const value = thread.execution_state;
  const state = !readFailed && typeof value === "string" && Object.hasOwn(executionLabels, value) ? value : "unknown";
  return { state, label: executionLabels[state],
    description: readFailed ? "本次列表读取失败，执行状态未知" : value && state !== "unknown"
      ? executionLabels[state] : "执行状态未知，请刷新列表重试" };
}

export function V2Sidebar({ threads, workspaces, selectedThreadID, searchOpen, onSearchOpen,
  onNewConversation, onOpenModels, onSelectThread, onOpenSettings, onArchive,
  onOpenInspector, inspectorActive = false, onOpenConnections,
  search = "", appliedSearch = "", searchPending = false, searchTooLong = false, onSearchChange, onSearchCompositionChange,
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
  hasMore?: boolean; truncated?: boolean; loading?: boolean; loadingMore?: boolean; loadFailed?: boolean;
  loadMoreFailed?: boolean; refreshing?: boolean;
  onLoadMore?: () => void; onRefresh?: () => void; onRetry?: () => void;
}) {
  const searching = Boolean(appliedSearch);
  const workspaceNames = useMemo(() => new Map(workspaces.map((workspace) =>
    [workspace.id, workspace.name])), [workspaces]);
  const grouped = useMemo(() => {
    const result = new Map<string, ThreadView[]>();
    for (const thread of threads) {
      const id = thread.workspace_id ?? "";
      const group = result.get(id);
      if (group) group.push(thread);
      else result.set(id, [thread]);
    }
    return [...result.entries()];
  }, [threads]);
  const duplicateNames = useMemo(() => {
    const counts = new Map<string, number>();
    for (const { name } of workspaces) counts.set(name, (counts.get(name) ?? 0) + 1);
    return new Set([...counts].filter(([, count]) => count > 1).map(([name]) => name));
  }, [workspaces]);

  return <aside className="v2-sidebar">
    <div className="v2-sidebar-brand"><strong>Universal-Code</strong>
      {onRefresh && <button aria-label="刷新对话列表" disabled={searchPending || loading || refreshing || loadingMore}
        onClick={onRefresh} type="button"><RefreshCw aria-hidden="true" size={15} /></button>}
      <button aria-label="搜索" aria-expanded={searchOpen} onClick={() => onSearchOpen(!searchOpen)} type="button">
        {searchOpen ? <X aria-hidden="true" size={16} /> : <Search aria-hidden="true" size={16} />}
      </button></div>
    <nav aria-label="对话导航" className="v2-sidebar-actions">
      <button onClick={onNewConversation} type="button"><SquarePen aria-hidden="true" size={16} />
        <span>新对话</span></button>
      <button onClick={onOpenModels} type="button"><Cpu aria-hidden="true" size={16} />
        <span>接入模型</span></button>
    </nav>
    {searchOpen && <div className="v2-sidebar-search"><Search aria-hidden="true" size={15} />
      <input aria-label="搜索对话" aria-invalid={searchTooLong || undefined}
        aria-describedby={`v2-thread-search-scope${searchTooLong ? " v2-thread-search-error" : ""}`} autoFocus
        onChange={(event) => onSearchChange?.(event.target.value)}
        onCompositionStart={() => onSearchCompositionChange?.(true)}
        onCompositionEnd={(event) => {
          onSearchChange?.(event.currentTarget.value);
          onSearchCompositionChange?.(false);
        }} placeholder="搜索全部未归档对话标题…" type="search" value={search} />
      {search && <button aria-label="清除对话搜索" onClick={() => onSearchChange?.("")} type="button">
        <X aria-hidden="true" size={14} /></button>}</div>}
    {searchOpen && <p className="v2-history-scope" id="v2-thread-search-scope">搜索全部未归档对话标题，不含消息正文。
      英文字母忽略大小写，其他文字按原文匹配。归档对话在设置中查看。</p>}
    {searchTooLong && <p className="v2-history-scope" id="v2-thread-search-error" role="alert">标题搜索最多支持 256 个字符，请缩短搜索词。</p>}
    <div aria-busy={!searchTooLong && (searchPending || loading || refreshing || loadingMore)} className="v2-thread-scroll">
      <p className="v2-history-scope" role="status" aria-label="对话列表状态" aria-live="polite" aria-atomic="true">
        {searchTooLong ? "请缩短搜索词后继续。" : searchPending ? "正在准备标题搜索…" : loading ? (searching ? "正在搜索对话…" : "正在加载对话…")
          : loadFailed ? (loadMoreFailed ? "更多记录读取失败，当前列表尚未加载完毕。" : "对话列表读取失败，当前执行状态未知。")
          : refreshing ? "正在刷新对话列表…"
          : `${searching ? "搜索结果：" : ""}已加载 ${threads.length} 条${searching ? "匹配对话" : "对话"}；${truncated
            ? searching ? "结果达到读取上限，请缩小标题搜索范围。" : "结果达到读取上限，请用标题搜索查找更早记录。"
            : hasMore ? searching ? "还有更多匹配结果。" : "还有更早记录。" : searching ? "全部匹配结果已加载。" : "当前列表已加载完毕。"}`}</p>
      {!searchPending && !loading && !loadFailed && !truncated && grouped.length === 0 && <div className="v2-sidebar-empty"><MessagesSquare aria-hidden="true" size={16} />
        {searching ? "没有匹配的未归档对话标题" : "暂无对话"}</div>}
      {!searchPending && grouped.map(([workspaceID, workspaceThreads]) => {
        const name = workspaceID ? workspaceNames.get(workspaceID) ?? "工作区" : "本地任务";
        const shortID = workspaceID.length > 16 ? `…${workspaceID.slice(-6)}` : workspaceID;
        const label = workspaceID && (duplicateNames.has(name) || !workspaceNames.has(workspaceID))
          ? `${name} · ${shortID}` : name;
        return <section aria-label={`项目 ${label}`} className="v2-thread-group" key={workspaceID}>
        <header title={workspaceID || name}><Folder aria-hidden="true" size={15} /><span>{label}</span></header>
        {workspaceThreads.map((thread) => {
          const execution = executionStatus(thread, loadFailed);
          const statusID = `v2-thread-state-${thread.id}`;
          return <div className={`v2-thread-row-shell${selectedThreadID === thread.id
          ? " is-selected" : ""}`} key={thread.id}>
          <button aria-current={selectedThreadID === thread.id ? "page" : undefined} aria-describedby={statusID}
            className="v2-thread-row" onClick={() => onSelectThread(thread.id)} type="button">
            <span className="v2-thread-title">{thread.title}</span>
            <span aria-hidden="true" className={`v2-thread-state state-${execution.state}`} title={execution.description}>
              <i />{execution.label}</span>
          </button>
          <span className="v2-thread-state-description" id={statusID}>{execution.description}</span>
          <button aria-label={`归档 ${thread.title}`} className="v2-thread-more"
            onClick={() => onArchive(thread)} title="归档此对话" type="button">
            <Archive aria-hidden="true" size={15} />
          </button>
        </div>; })}
      </section>; })}
      {!searchPending && loadFailed && <p className="v2-history-scope" role="alert">对话列表加载失败{threads.length ? "，已有记录仍可打开。" : "，请重试读取。"}
        <button disabled={refreshing || loadingMore} onClick={onRetry ?? (loadMoreFailed ? onLoadMore : onRefresh)} type="button">重试加载对话</button></p>}
      {!searchPending && hasMore && <button className="v2-load-history" disabled={loadingMore || refreshing || loading}
        onClick={onLoadMore} type="button">{loadingMore ? searching ? "正在加载更多匹配对话…" : "正在加载更早对话…"
          : searching ? "加载更多匹配对话" : "加载更早对话"}</button>}
    </div>
    <div className="v2-sidebar-footer">
      {onOpenInspector && <button aria-pressed={inspectorActive} onClick={onOpenInspector} type="button">
        <Box aria-hidden="true" size={16} />观察与记录</button>}
      {onOpenConnections && <button onClick={onOpenConnections} type="button">
        <PlugZap aria-hidden="true" size={16} />连接与环境</button>}
      <button onClick={onOpenSettings} type="button"><Settings aria-hidden="true" size={16} />设置</button>
    </div>
  </aside>;
}

const settingsGroups: Array<{ label: string; items: Array<{
  id: V2SettingsSection; label: string; icon: typeof Settings;
}> }> = [
  { label: "应用", items: [
    { id: "general", label: "常规", icon: Settings },
    { id: "appearance", label: "外观", icon: Palette },
    { id: "shortcuts", label: "快捷键", icon: Keyboard },
    { id: "about", label: "关于", icon: Info },
  ] },
  { label: "连接与环境", items: [
    { id: "connections", label: "连接与环境", icon: PlugZap },
    { id: "models", label: "模型", icon: Cpu },
    { id: "extensions", label: "扩展与代码智能", icon: PlugZap },
    { id: "skills", label: "Skill 包", icon: PackageSearch },
    { id: "environment", label: "执行环境", icon: Box },
  ] },
  { label: "任务与观察", items: [
    { id: "task-configuration", label: "任务预算与项目配置", icon: Settings },
    { id: "permissions", label: "当前任务权限", icon: ShieldCheck },
    { id: "inspector", label: "观察视图偏好", icon: Box },
  ] },
];

export function V2SettingsSidebar({ section, onBack, onSelect }: {
  section: V2SettingsSection;
  onBack: () => void;
  onSelect: (section: V2SettingsSection) => void;
}) {
  const [search, setSearch] = useState("");
  const normalized = search.trim().toLocaleLowerCase();
  return <aside className="v2-sidebar v2-settings-sidebar">
    <button className="v2-settings-back" onClick={onBack} type="button">
      <ArrowLeft aria-hidden="true" size={16} />返回应用</button>
    <label className="v2-settings-search"><Search aria-hidden="true" size={15} />
      <input aria-label="搜索设置" onChange={(event) => setSearch(event.target.value)}
        placeholder="搜索设置…" type="search" value={search} /></label>
    <nav aria-label="设置分类" className="v2-settings-nav">
      {settingsGroups.map((group) => {
        const items = group.items.filter((item) => !normalized || item.label.toLocaleLowerCase().includes(normalized));
        if (!items.length) return null;
        return <section key={group.label}><h2>{group.label}</h2>{items.map(({ id, label, icon: Icon }) => {
          const active = section === id || section === "advanced-models" && id === "models" ||
            section === "plugins" && id === "extensions" || section === "keyboard" && id === "shortcuts";
          return <button aria-current={active ? "page" : undefined}
            className={active ? "is-active" : ""} key={id} onClick={() => onSelect(id)}
            type="button"><Icon aria-hidden="true" size={16} />{label}</button>;
        })}</section>;
      })}
    </nav>
    <div className="v2-settings-archive"><span>已归档</span>
      <button aria-current={section === "archived" ? "page" : undefined}
        className={section === "archived" ? "is-active" : ""}
        onClick={() => onSelect("archived")} type="button">
        <ArchiveRestore aria-hidden="true" size={16} />已归档的聊天</button></div>
  </aside>;
}
