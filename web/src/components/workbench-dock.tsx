import { useCallback, useEffect, useMemo, useRef, useState, type ReactNode, type RefObject } from "react";
import { useQuery, type UseQueryResult } from "@tanstack/react-query";
import {
  Bot,
  ChevronDown,
  Code2,
  FileDiff,
  FileText,
  Folder,
  FolderOpen,
  Globe2,
  ListTree,
  LoaderCircle,
  PanelBottom,
  PanelRight,
  Plus,
  SquareTerminal,
  X,
} from "lucide-react";
import type { APIClient } from "../api/client";
import type {
  RepositoryStateView,
  RunDetailView,
  SessionDetailView,
  ThreadDetailView,
  WorkItemView,
} from "../api/types";
import {
  closeDesktopUserTerminal,
  desktopErrorMessage,
  listDesktopWorkspaceLaunchers,
  openDesktopWorkspace,
  type DesktopWorkspaceLauncher,
} from "../lib/desktop-bridge";
import { formatCompactDate, shortID } from "../lib/format";
import { useLocale } from "../lib/locale";
import { canonicalVocabulary } from "../lib/vocabulary";
import { usePagedResource } from "../hooks/use-paged-resource";
import { EmptyState, ErrorState, LoadingState, StatusBadge } from "./common";
import { RepositoryDiffPanel } from "./repository-diff-panel";
import { WorkspaceExplorer } from "./workspace-explorer";
import { useFileEditLeave } from "./file-edit-drafts";
import { UserTerminalPanel } from "./user-terminal-panel";

export type WorkbenchResourceKind = "thread" | "run" | "session";
type SidecarTab = "review" | "terminal" | "browser" | "files" | "tasks";
type SidecarGroup = "workspace" | "run" | "reserved";

interface WorkbenchContext {
  goal: string;
  mode: string;
  resourceLabel: string;
  status: string;
  updatedAt: string;
  workspaceID: string;
  runID: string;
}

const sidecarItems: Array<{
  id: SidecarTab;
  label: [string, string];
  group: SidecarGroup;
  shortcut: string;
  icon: typeof FileDiff;
  availability: "ready" | "reserved";
}> = [
  { id: "review", label: ["审阅", "Review"], group: "workspace", shortcut: "Ctrl+Shift+G", icon: FileDiff, availability: "ready" },
  { id: "files", label: ["文件", "Files"], group: "workspace", shortcut: "Ctrl+P", icon: Folder, availability: "ready" },
  { id: "terminal", label: ["终端", "Terminal"], group: "run", shortcut: "", icon: SquareTerminal, availability: "ready" },
  { id: "tasks", label: [...canonicalVocabulary.planItem], group: "run", shortcut: "Ctrl+Alt+S", icon: Bot, availability: "ready" },
  { id: "browser", label: ["浏览器", "Browser"], group: "reserved", shortcut: "", icon: Globe2, availability: "reserved" },
];

const sidecarGroups: Array<{ id: SidecarGroup; label: [string, string] }> = [
  { id: "workspace", label: ["工作区", "Workspace"] },
  { id: "run", label: ["运行", "Run"] },
  { id: "reserved", label: ["即将推出", "Coming soon"] },
];

export function WorkbenchDock({ children, client, desktop, resourceKind, runID, sessionID,
  threadID = "", title }: {
  children: ReactNode;
  client: APIClient;
  desktop: boolean;
  resourceKind: WorkbenchResourceKind;
  runID: string;
  sessionID: string;
  threadID?: string;
  title: string;
}) {
  const { t } = useLocale();
  const [summaryVisible, setSummaryVisible] = useState(false);
  const [bottomVisible, setBottomVisible] = useState(false);
  const [sidecarVisible, setSidecarVisible] = useState(false);
  const [sidecarTab, setSidecarTab] = useState<SidecarTab>("files");
  const leaveFileEdit = useFileEditLeave();
  const [sidecarMenuOpen, setSidecarMenuOpen] = useState(false);
  const [terminalSessionID, setTerminalSessionID] = useState("");
  const updateTerminalSession = useCallback((next: string, expected?: string) => {
    setTerminalSessionID((current) => expected === undefined || current === expected ? next : current);
  }, []);
  const context = useWorkbenchContext(client, resourceKind, runID, sessionID, threadID);
  const effectiveRunID = context.runID || runID;
  const terminalRunRef = useRef(effectiveRunID);
  const sidecarMenuRef = useRef<HTMLDivElement>(null);

  useDismissablePopover(sidecarMenuRef, sidecarMenuOpen, () => setSidecarMenuOpen(false));
  useEffect(() => {
    if (terminalRunRef.current === effectiveRunID) return;
    const previousSession = terminalSessionID;
    terminalRunRef.current = effectiveRunID;
    setTerminalSessionID("");
    if (previousSession) {
      void closeDesktopUserTerminal(previousSession).catch(() => undefined);
    }
  }, [effectiveRunID, terminalSessionID]);
  useEffect(() => {
    const onKeyDown = (event: globalThis.KeyboardEvent) => {
      if (!event.ctrlKey || event.metaKey) return;
      if (event.key.toLowerCase() === "j" && !event.altKey && !event.shiftKey) {
        event.preventDefault();
        setBottomVisible((visible) => !visible);
      } else if (event.key.toLowerCase() === "g" && event.shiftKey && !event.altKey) {
        event.preventDefault();
        leaveFileEdit(() => { setSidecarTab("review"); setSidecarVisible(true); });
      } else if (event.key.toLowerCase() === "p" && !event.altKey && !event.shiftKey) {
        event.preventDefault();
        setSidecarTab("files");
        setSidecarVisible(true);
      } else if (event.key.toLowerCase() === "s" && event.altKey && !event.shiftKey) {
        event.preventDefault();
        leaveFileEdit(() => { setSidecarTab("tasks"); setSidecarVisible(true); });
      }
    };
    window.addEventListener("keydown", onKeyDown);
    return () => window.removeEventListener("keydown", onKeyDown);
  }, [leaveFileEdit]);

  const selectSidecar = (tab: SidecarTab) => {
    if (sidecarItems.find((item) => item.id === tab)?.availability !== "ready") return;
    leaveFileEdit(() => {
      setSidecarTab(tab);
      setSidecarVisible(true);
      setSidecarMenuOpen(false);
    });
  };

  return <main className="main-workspace prayu-conversation-panel">
    <header className="workspace-panel-toolbar">
      <div className="workspace-panel-location">
        <FolderOpen aria-hidden="true" size={16} />
        <strong>{title}</strong>
      </div>
      <div aria-label={t("工作台面板", "Workbench panels")} className="workspace-panel-actions" role="toolbar">
        <WorkspaceOpenMenu desktop={desktop} workspaceID={context.workspaceID} />
        <button aria-label={t("切换摘要", "Toggle summary")} aria-pressed={summaryVisible}
          className={`workspace-panel-icon ${summaryVisible ? "active" : ""}`}
          onClick={() => setSummaryVisible((visible) => !visible)}
          title={t("切换摘要", "Toggle summary")} type="button">
          <ListTree aria-hidden="true" size={16} />
        </button>
        <button aria-label={t("切换底部面板显示", "Toggle bottom panel")} aria-pressed={bottomVisible}
          className={`workspace-panel-icon ${bottomVisible ? "active" : ""}`}
          onClick={() => setBottomVisible((visible) => !visible)}
          title={t("切换底部面板显示 (Ctrl+J)", "Toggle bottom panel (Ctrl+J)")} type="button">
          <PanelBottom aria-hidden="true" size={16} />
        </button>
        <button aria-label={t("显示或隐藏右侧栏", "Show or hide side panel")} aria-pressed={sidecarVisible}
          className={`workspace-panel-icon ${sidecarVisible ? "active" : ""}`}
          onClick={() => leaveFileEdit(() => setSidecarVisible((visible) => !visible))}
          title={t("显示或隐藏右侧栏", "Show or hide side panel")} type="button">
          <PanelRight aria-hidden="true" size={16} />
        </button>
      </div>
    </header>
    <div className={`workbench-dock-layout ${sidecarVisible ? "with-sidecar" : ""}`}>
      <div className="workbench-center-stack">
        <div className={`workbench-center-row ${summaryVisible ? "with-summary" : ""}`}>
          <div className={`workspace-panel-stage workspace-panel-stage-${resourceKind}`}>
            {children}
          </div>
          {summaryVisible && <WorkbenchSummary client={client} context={context} />}
        </div>
        {bottomVisible && <BottomPanel desktop={desktop} runID={effectiveRunID}
          sessionID={terminalSessionID} onSession={updateTerminalSession}
          onClose={() => setBottomVisible(false)} />}
      </div>
      {sidecarVisible && <aside aria-label={t("右侧工具栏", "Side tools")} className="workbench-sidecar">
        <header className="workbench-sidecar-tabs">
          <button className="workbench-sidecar-tab" type="button">
            {sidecarTabIcon(sidecarTab)}<span>{sidecarLabel(sidecarTab, t)}</span>
          </button>
          <div className="workbench-sidecar-add" ref={sidecarMenuRef}>
            <button aria-expanded={sidecarMenuOpen} aria-haspopup="menu"
              aria-label={t("添加右侧工具", "Add side tool")} className="workspace-panel-icon"
              onClick={() => setSidecarMenuOpen((open) => !open)} title={t("添加右侧工具", "Add side tool")}
              type="button"><Plus aria-hidden="true" size={17} /></button>
            {sidecarMenuOpen && <div aria-label={t("右侧工具", "Side tools")} className="workbench-sidecar-menu" role="menu">
              {sidecarGroups.map((group) => <div aria-label={t(...group.label)}
                className="workbench-sidecar-menu-group" key={group.id} role="group">
                <span className="workbench-sidecar-menu-heading">{t(...group.label)}</span>
                {sidecarItems.filter((item) => item.group === group.id).map(({
                  id, label, shortcut, icon: Icon, availability,
                }) => <button aria-checked={sidecarTab === id} disabled={availability === "reserved"}
                  key={id} onClick={() => selectSidecar(id)} role="menuitemradio" type="button">
                  <Icon aria-hidden="true" size={16} /><span>{t(...label)}</span>
                  {availability === "reserved" ?
                    <small>{t("预留", "Reserved")}</small> : shortcut && <kbd>{shortcut}</kbd>}
                </button>)}
              </div>)}
            </div>}
          </div>
          <button aria-label={t("关闭右侧栏", "Close side panel")} className="workspace-panel-icon"
            onClick={() => leaveFileEdit(() => setSidecarVisible(false))} title={t("关闭右侧栏", "Close side panel")} type="button">
            <X aria-hidden="true" size={16} />
          </button>
        </header>
        <div className="workbench-sidecar-body">
          <SidecarContent client={client} context={context} runID={effectiveRunID} tab={sidecarTab} />
        </div>
      </aside>}
    </div>
  </main>;
}

function WorkspaceOpenMenu({ desktop, workspaceID }: { desktop: boolean; workspaceID: string }) {
  const { t } = useLocale();
  const [open, setOpen] = useState(false);
  const [loading, setLoading] = useState(false);
  const [launchers, setLaunchers] = useState<DesktopWorkspaceLauncher[]>([]);
  const [message, setMessage] = useState("");
  const root = useRef<HTMLDivElement>(null);
  useDismissablePopover(root, open, () => setOpen(false));

  const toggle = async () => {
    if (!desktop || !workspaceID) return;
    const next = !open;
    setOpen(next);
    setMessage("");
    if (!next || launchers.length > 0) return;
    setLoading(true);
    try {
      setLaunchers(await listDesktopWorkspaceLaunchers(workspaceID));
    } catch (error) {
      setMessage(desktopErrorMessage(error));
    } finally {
      setLoading(false);
    }
  };
  const launch = async (launcher: DesktopWorkspaceLauncher) => {
    setLoading(true);
    setMessage("");
    try {
      const result = await openDesktopWorkspace(workspaceID, launcher.id);
      setMessage(result.status === "started" ?
        t(`已在 ${launcher.label} 中打开`, `Opened in ${launcher.label}`) :
        t("已取消打开", "Opening cancelled"));
      if (result.status === "started") setOpen(false);
    } catch (error) {
      setMessage(desktopErrorMessage(error));
    } finally {
      setLoading(false);
    }
  };

  return <div className="workspace-open-root" ref={root}>
    <button aria-expanded={open} aria-haspopup="menu" aria-label={t("打开工作区", "Open workspace")}
      className="workspace-open-button" disabled={!desktop || !workspaceID || loading}
      onClick={() => void toggle()} title={desktop ? t("打开工作区", "Open workspace") :
        t("桌面版支持打开工作区", "Open workspace is available in the Desktop app")}
      type="button">
      {loading ? <LoaderCircle aria-hidden="true" className="spin" size={15} /> :
        <FolderOpen aria-hidden="true" size={15} />}
      <ChevronDown aria-hidden="true" size={14} />
    </button>
    {open && <div aria-label={t("打开方式", "Open with")} className="workspace-open-menu" role="menu">
      {launchers.map((launcher) => <button key={launcher.id}
        onClick={() => void launch(launcher)} role="menuitem" type="button">
        {launcherIcon(launcher.kind)}<span>{launcher.label}</span>
      </button>)}
      {!loading && launchers.length === 0 && !message &&
        <span>{t("没有可用的打开方式", "No available application")}</span>}
      {message && <span className="workspace-open-message" role="status">{message}</span>}
    </div>}
    {!open && message && <span className="workspace-open-live" role="status">{message}</span>}
  </div>;
}

function WorkbenchSummary({ client, context }: {
  client: APIClient;
  context: WorkbenchContext;
}) {
  const { t } = useLocale();
  const repository = useQuery({
    queryKey: ["workspace", context.workspaceID, "repository-state"],
    queryFn: ({ signal }) => client.repositoryState(context.workspaceID, signal),
    enabled: Boolean(context.workspaceID),
  });
  return <aside aria-label={t("摘要", "Summary")} className="workbench-summary">
    <header><span>{t("环境信息", "Environment")}</span></header>
    <SummaryRepository query={repository} workspaceID={context.workspaceID} />
    <section>
      <h2>{t("当前 Thread / Run", "Current Thread / Run")}</h2>
      {context.resourceLabel ? <>
        <div className="summary-item-heading"><strong>{context.resourceLabel}</strong>
          {context.status && <StatusBadge status={context.status} />}</div>
        {context.goal && <p>{context.goal}</p>}
        <dl><div><dt>模式</dt><dd>{context.mode || "-"}</dd></div>
          <div><dt>更新</dt><dd>{context.updatedAt ? formatCompactDate(context.updatedAt) : "-"}</dd></div></dl>
      </> : <EmptyState>{t("尚未选择 Thread 或 Run", "No Thread or Run selected")}</EmptyState>}
    </section>
    <section>
      <h2>后台活动</h2>
      <div className="summary-inert-row"><SquareTerminal aria-hidden="true" size={15} />
        <span>没有由工作台启动的后台进程</span></div>
    </section>
  </aside>;
}

function SummaryRepository({ query, workspaceID }: {
  query: UseQueryResult<RepositoryStateView, Error>;
  workspaceID: string;
}) {
  const { t } = useLocale();
  if (!workspaceID) return <section><EmptyState>{t("选择并绑定项目后可查看仓库摘要。", "Select and bind a project to view its repository summary.")}</EmptyState></section>;
  if (query.isLoading) return <section><LoadingState label="加载仓库摘要" /></section>;
  if (query.isError || !query.data) return <section><ErrorState error={query.error} /></section>;
  if (!query.data.available) return <section><p>{t("当前项目可查看文件。需要 Git 操作时，请绑定含 Git 仓库的项目。", "Files in this project are available. Bind a project with a Git repository to use Git operations.")}</p></section>;
  const state = query.data;
  return <section>
    <div className="summary-item-heading"><strong>{state.detached ? "detached" : state.branch || "unborn"}</strong>
      <StatusBadge status={state.clean ? "clean" : "changed"} /></div>
    <dl>
      <div><dt>暂存</dt><dd>{state.staged_count}</dd></div>
      <div><dt>变更</dt><dd>{state.worktree_count}</dd></div>
      <div><dt>未跟踪</dt><dd>{state.untracked_count}</dd></div>
      <div><dt>冲突</dt><dd>{state.conflicted_count}</dd></div>
    </dl>
  </section>;
}

function BottomPanel({ desktop, runID, sessionID, onSession, onClose }: {
  desktop: boolean;
  runID: string;
  sessionID: string;
  onSession: (sessionID: string, expectedSessionID?: string) => void;
  onClose: () => void;
}) {
  const { t } = useLocale();
  return <section aria-label="底部面板" className="workbench-bottom-panel">
    <header><button className="active" type="button"><SquareTerminal aria-hidden="true" size={14} />终端</button>
      <button aria-label="关闭底部面板" className="workspace-panel-icon" onClick={onClose}
        title="关闭底部面板" type="button"><X aria-hidden="true" size={15} /></button></header>
    {desktop ? <UserTerminalPanel runID={runID} sessionID={sessionID}
      onSession={onSession} /> : <div className="workbench-terminal-empty">
      <SquareTerminal aria-hidden="true" size={22} />
      <strong>{t("在桌面应用中使用终端", "Use the terminal in the desktop app")}</strong>
      <span>{t("打开 Desktop 并核对当前执行权限后，可启动本机 Shell。", "Open Desktop and review the current execution permissions to start a local shell.")}</span>
    </div>}
  </section>;
}

function SidecarContent({ client, context, runID, tab }: {
  client: APIClient;
  context: WorkbenchContext;
  runID: string;
  tab: SidecarTab;
}) {
  const { t } = useLocale();
  if (tab === "review") {
    return context.workspaceID ? <RepositoryDiffPanel client={client} workspaceID={context.workspaceID} /> :
      <EmptyState>{t("选择并绑定项目后可审阅差异。", "Select and bind a project to review its diff.")}</EmptyState>;
  }
  if (tab === "files") {
    return <WorkspaceExplorer client={client} runID={runID} workspaceID={context.workspaceID} />;
  }
  if (tab === "tasks") {
    return <SideTaskPanel client={client} runID={runID} />;
  }
  if (tab === "browser") {
    return <div className="workbench-tool-empty"><Globe2 aria-hidden="true" size={29} />
      <strong>{t("浏览器工具准备中", "Browser tools in preparation")}</strong><span>{t("当前可在验证页查看已保存的浏览器证据。", "Inspect saved browser evidence in Verify.")}</span></div>;
  }
  return <div className="workbench-tool-empty"><SquareTerminal aria-hidden="true" size={29} />
    <strong>{t("在底部面板使用终端", "Use the terminal in the bottom panel")}</strong><span>{t("打开 Desktop 的底部面板，核对权限后启动。", "Open the bottom panel in Desktop, review permissions, then start.")}</span></div>;
}

function SideTaskPanel({ client, runID }: { client: APIClient; runID: string }) {
  const { t } = useLocale();
  const query = usePagedResource<WorkItemView>(client, ["run", runID, "sidecar-work-items"],
    `/runs/${encodeURIComponent(runID)}/work-items`, { limit: 50 }, Boolean(runID));
  const items = useMemo(() => query.data?.pages.flatMap((page) => page.items) ?? [], [query.data]);
  if (!runID) return <EmptyState>当前未选择 Run</EmptyState>;
  if (query.isLoading) return <LoadingState label={t("加载计划项", "Loading Plan items")} />;
  if (query.isError) return <ErrorState error={query.error} />;
  if (items.length === 0) return <EmptyState>{t("暂无计划项", "No Plan items")}</EmptyState>;
  return <div className="workbench-side-task-list">
    {items.map((item) => <article key={item.id}>
      <div><Bot aria-hidden="true" size={15} /><strong>{item.title}</strong></div>
      <StatusBadge status={item.status} />
      {item.owner && <small>{item.owner}</small>}
    </article>)}
  </div>;
}

function useWorkbenchContext(client: APIClient, resourceKind: WorkbenchResourceKind,
  runID: string, sessionID: string, threadID: string): WorkbenchContext {
  const run = useQuery({
    queryKey: ["run", runID],
    queryFn: ({ signal }) => client.get<RunDetailView>(`/runs/${encodeURIComponent(runID)}`, {}, signal),
    enabled: resourceKind === "run" && Boolean(runID),
  });
  const session = useQuery({
    queryKey: ["session", sessionID],
    queryFn: ({ signal }) => client.get<SessionDetailView>(
      `/sessions/${encodeURIComponent(sessionID)}`, {}, signal),
    enabled: resourceKind === "session" && Boolean(sessionID),
  });
  const thread = useQuery({
    queryKey: ["thread", threadID],
    queryFn: ({ signal }) => client.get<ThreadDetailView>(
      `/threads/${encodeURIComponent(threadID)}`, {}, signal),
    enabled: resourceKind === "thread" && Boolean(threadID),
  });
  if (resourceKind === "run" && run.data) {
    return {
      goal: run.data.mission.goal,
      mode: `${run.data.mode.phase} / ${run.data.mission.profile}`,
      resourceLabel: `Run ${shortID(run.data.run.id)}`,
      status: run.data.run.status,
      updatedAt: run.data.run.updated_at,
      workspaceID: run.data.mission.workspace_id ?? "",
      runID: run.data.run.id,
    };
  }
  if (resourceKind === "session" && session.data) {
    return {
      goal: session.data.session.title,
      mode: session.data.session.route,
      resourceLabel: `Run-local Session ${shortID(session.data.session.id)}`,
      status: session.data.session.status,
      updatedAt: session.data.session.updated_at,
      workspaceID: session.data.session.workspace_id ?? "",
      runID: session.data.run?.id ?? "",
    };
  }
  if (resourceKind === "thread" && thread.data) {
    const currentRun = thread.data.active_run ?? thread.data.last_run;
    return {
      goal: thread.data.mission.goal,
      mode: `Thread / ${thread.data.mission.profile}`,
      resourceLabel: `Thread ${shortID(thread.data.thread.id)}`,
      status: thread.data.thread.status,
      updatedAt: thread.data.thread.updated_at,
      workspaceID: thread.data.thread.workspace_id ?? "",
      runID: currentRun.id,
    };
  }
  return { goal: "", mode: "", resourceLabel: "", status: "", updatedAt: "",
    workspaceID: "", runID: "" };
}

function useDismissablePopover(root: RefObject<HTMLElement | null>, open: boolean,
  close: () => void) {
  useEffect(() => {
    if (!open) return;
    const onPointerDown = (event: globalThis.PointerEvent) => {
      if (event.target instanceof Node && !root.current?.contains(event.target)) close();
    };
    const onKeyDown = (event: globalThis.KeyboardEvent) => {
      if (event.key === "Escape") close();
    };
    document.addEventListener("pointerdown", onPointerDown);
    document.addEventListener("keydown", onKeyDown);
    return () => {
      document.removeEventListener("pointerdown", onPointerDown);
      document.removeEventListener("keydown", onKeyDown);
    };
  }, [close, open, root]);
}

function sidecarLabel(tab: SidecarTab, t: (chinese: string, english: string) => string): string {
  const label = sidecarItems.find((item) => item.id === tab)?.label;
  return label ? t(...label) : t("工具", "Tools");
}

function sidecarTabIcon(tab: SidecarTab): ReactNode {
  const Icon = sidecarItems.find((item) => item.id === tab)?.icon ?? FileText;
  return <Icon aria-hidden="true" size={15} />;
}

function launcherIcon(kind: DesktopWorkspaceLauncher["kind"]): ReactNode {
  if (kind === "folder") return <FolderOpen aria-hidden="true" size={16} />;
  if (kind === "terminal") return <SquareTerminal aria-hidden="true" size={16} />;
  return <Code2 aria-hidden="true" size={16} />;
}
