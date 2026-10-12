import { Fragment, useCallback, useEffect, useId, useMemo, useRef, useState, type KeyboardEvent } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import {
  Activity,
  Ban,
  Boxes,
  BookOpenCheck,
  ClipboardList,
  ClipboardCheck,
  Container,
  Database,
  FileArchive,
  FileCheck2,
  FileDiff,
  FolderOpen,
  Gauge,
  GitBranch,
  History,
  ListChecks,
  ListOrdered,
  LoaderCircle,
  MessageSquareText,
  Network,
  Pause,
  Paperclip,
  Play,
  Radio,
  ScanSearch,
  Server,
  ShieldAlert,
  ShieldCheck,
  ShieldOff,
  StickyNote,
  Terminal,
  UserCheck,
  Bug,
  ChevronsRight,
  View,
  Wrench,
} from "lucide-react";
import type { APIClient } from "../api/client";
import type {
  ArtifactView,
  EventView,
  GitAdvancedReviewResultView,
  GitHubReviewWriteReviewResultView,
  NoteView,
  MessageView,
  ModelCancellationRequestView,
  ModelCancellationView,
  OperatorSteeringQueueView,
  RunActivityView,
  RunDetailView,
  RunExecutionProfileControlView,
  RunExecutionProfileView,
  RunExecutionPermissionControlView,
  RunExecutionPermissionView,
  RunExecutionControlView,
  RunLifecycleControlRequestView,
  RunLifecycleControlView,
  SupervisorToolRoundView,
  WorkItemView,
} from "../api/types";
import { usePagedResource } from "../hooks/use-paged-resource";
import { useRunDetailEventRefresh } from "../hooks/use-run-detail-event-refresh";
import { useRunEventStream } from "../hooks/use-run-event-stream";
import { usePublicModelStream } from "../hooks/use-public-model-stream";
import { formatBytes, formatDate, formatNumber, shortID } from "../lib/format";
import { useLocale } from "../lib/locale";
import { canonicalVocabulary, diagnosticVocabulary } from "../lib/vocabulary";
import { readRunNavigationMode, subscribeRunNavigationMode } from "../lib/run-navigation";
import { EmptyState, ErrorState, KeyValue, LoadMoreButton, LoadingState, StatusBadge } from "./common";
import { LifecycleStatusBadge } from "./lifecycle-status";
import { ApprovalPanel } from "./approval-panel";
import { ArtifactDetail } from "./artifact-detail";
import { PlanDeliveryPanel } from "./plan-delivery-panel";
export { PlanDeliveryPanel } from "./plan-delivery-panel";
import { CommandPalette, type CommandPaletteCommand } from "./command-palette";
import { CodeJourney } from "./code-journey";
import { CodeHandoffPanel } from "./code-handoff-panel";
import { EvidenceInventory } from "./evidence-inventory";
import { FileEditPanel } from "./file-edit-panel";
import { OperationReceiptHistory } from "./operation-receipt-history";
import { OperatorActionCenter } from "./operator-action-center";
import { RunWakePanel } from "./run-wake-panel";
import { RepositoryStatePanel } from "./repository-state-panel";
import { RepositoryDiffPanel } from "./repository-diff-panel";
import { RepositoryHistoryPanel } from "./repository-history-panel";
import { GitAdvancedPanel } from "./git-advanced-panel";
import { GitHubReviewPanel } from "./github-review-panel";
import { VerificationEvidence } from "./verification-evidence";
import { VerificationPlan } from "./verification-plan";
import type { ReceiptReviewNavigationTarget } from "./receipt-review-navigation";
import { WorkspaceExplorer } from "./workspace-explorer";
import { SessionComposer, type SessionComposerStatus } from "./session-composer";
import { AgentGraphPanel, BatchDeliveriesPanel, ChildTasksPanel, DelegationsPanel, ExternalSkillsSection, FanoutPanel, FindingsPanel } from "./run-projections";
import { RunActivityTimeline } from "./run-activity-timeline";
import { EmbeddedAnalyzerPanel } from "./embedded-analyzer-panel";
import { DockerSandboxPanel } from "./docker-sandbox-panel";
import { ContextContinuityPanel } from "./context-continuity-panel";
import { WorkspaceCheckpointPanel } from "./workspace-checkpoint-panel";
import { UIEvidencePanel } from "./ui-evidence-panel";
import { ThreadActivityToolDetailPanel } from "../v2/components/activity-detail";
import { StandardCodeDeliveryPanel } from "./standard-code-delivery-panel";
import "./inspector-workspace-navigation.css";
import { ModelUsagePanel } from "./model-usage-panel";

export type RunTab = "activity" | "overview" | "journey" | "actions" | "approvals" | "diffs" | "repository" | "files" | "evidence" | "verify" | "delivery" | "handoff" |
  "receipts" | "agents" | "delegations" | "fanout" | "findings" | "events" | "work" |
  "context" | "checkpoints" | "notes" | "artifacts" | "tools" | "analyzer" | "child-tasks" | "sandbox" | "ui-evidence";

const tabs: Array<{ id: RunTab; label: [string, string]; icon: typeof Activity }> = [
  { id: "activity", label: ["活动", "Activity"], icon: MessageSquareText },
  { id: "overview", label: ["概览", "Overview"], icon: Gauge },
  { id: "journey", label: ["代码历程", "Journey"], icon: ListOrdered },
  { id: "actions", label: ["待办操作", "Actions"], icon: ListChecks },
  { id: "approvals", label: ["审批", "Approvals"], icon: ShieldCheck },
  { id: "diffs", label: ["差异", "Diffs"], icon: FileDiff },
  { id: "repository", label: ["代码仓库", "Repository"], icon: GitBranch },
  { id: "files", label: ["文件", "Files"], icon: FolderOpen },
  { id: "evidence", label: ["证据", "Evidence"], icon: Paperclip },
  { id: "verify", label: ["验证", "Verify"], icon: ClipboardCheck },
  { id: "delivery", label: ["交付", "Delivery"], icon: FileCheck2 },
  { id: "ui-evidence", label: ["UI 证据", "UI evidence"], icon: View },
  { id: "handoff", label: ["交接", "Handoff"], icon: BookOpenCheck },
  { id: "receipts", label: ["操作收据", "Receipts"], icon: History },
  { id: "checkpoints", label: ["工作区检查点", "Checkpoints"], icon: History },
  { id: "agents", label: ["子智能体", "Agents"], icon: GitBranch },
  { id: "delegations", label: ["委派", "Delegations"], icon: Network },
  { id: "fanout", label: ["并发派发", "Fan-out"], icon: ScanSearch },
  { id: "child-tasks", label: ["子任务与交付", "Child tasks & delivery"], icon: Network },
  { id: "findings", label: ["发现", "Findings"], icon: ShieldAlert },
  { id: "events", label: ["事件", "Events"], icon: Activity },
  { id: "work", label: [...canonicalVocabulary.planItem], icon: ClipboardList },
  { id: "context", label: ["上下文", "Context"], icon: Database },
  { id: "notes", label: ["运行笔记", "Run notes"], icon: StickyNote },
  { id: "artifacts", label: ["产物", "Artifacts"], icon: FileArchive },
  { id: "tools", label: ["工具", "Tools"], icon: Wrench },
  { id: "analyzer", label: ["分析器", "Analyzer"], icon: Bug },
  { id: "sandbox", label: ["沙箱", "Sandbox"], icon: Server },
];

const commonTabs = new Set<RunTab>(["activity", "approvals", "diffs", "repository"]);
const diagnosticGroups: Array<{ label: [string, string]; tabs: RunTab[] }> = [
  { label: ["运行与验证", "Run and verification"], tabs: ["overview", "journey", "actions", "verify", "delivery", "handoff", "receipts", "checkpoints", "ui-evidence", "work"] },
  { label: ["上下文与记录", "Context and records"], tabs: ["files", "evidence", "events", "context", "notes", "artifacts", "tools"] },
  { label: ["专业工具", "Specialist tools"], tabs: ["agents", "delegations", "fanout", "child-tasks", "findings", "analyzer", "sandbox"] },
];

export function RunWorkspaceTabs({ activeTab, ariaLabel, children, items, onSelect, grouped = false, expandDiagnostics = false }: {
  activeTab: RunTab;
  ariaLabel: string;
  children: React.ReactNode;
  items: Array<{ id: RunTab; label: string; icon: typeof Activity }>;
  onSelect: (tab: RunTab) => void;
  grouped?: boolean;
  expandDiagnostics?: boolean;
}) {
  const { t } = useLocale();
  const tabSetID = useId();
  const tabRefs = useRef(new Map<RunTab, HTMLButtonElement>());
  const [expanded, setExpanded] = useState(expandDiagnostics);
  useEffect(() => { setExpanded(expandDiagnostics); }, [expandDiagnostics]);
  useEffect(() => { if (grouped && !commonTabs.has(activeTab)) setExpanded(true); }, [activeTab, grouped]);
  const orderedItems = grouped ? [...items.filter(({ id }) => commonTabs.has(id)),
    ...diagnosticGroups.flatMap((group) => group.tabs.flatMap((id) => items.filter((item) => item.id === id)))] : items;
  const keyboardItems = grouped && !expanded ? orderedItems.filter(({ id }) => commonTabs.has(id)) : orderedItems;
  const activeVisible = keyboardItems.some(({ id }) => id === activeTab);
  const tabID = (id: RunTab) => `${tabSetID}-tab-${id}`;
  const panelID = (id: RunTab) => `${tabSetID}-panel-${id}`;
  const handleKeyDown = (event: KeyboardEvent<HTMLButtonElement>, current: RunTab) => {
    const currentIndex = keyboardItems.findIndex(({ id }) => id === current);
    if (currentIndex < 0 || keyboardItems.length === 0) return;
    let nextIndex: number;
    switch (event.key) {
      case "ArrowRight":
        nextIndex = (currentIndex + 1) % keyboardItems.length;
        break;
      case "ArrowLeft":
        nextIndex = (currentIndex - 1 + keyboardItems.length) % keyboardItems.length;
        break;
      case "Home":
        nextIndex = 0;
        break;
      case "End":
        nextIndex = keyboardItems.length - 1;
        break;
      default:
        return;
    }
    event.preventDefault();
    const next = keyboardItems[nextIndex];
    onSelect(next.id);
    tabRefs.current.get(next.id)?.focus();
  };

  const renderTab = ({ id, label, icon: Icon }: (typeof items)[number]) => (
        <button aria-controls={panelID(id)} aria-selected={activeTab === id}
          className={activeTab === id ? "active" : ""} id={tabID(id)} key={id}
          onClick={() => onSelect(id)} onKeyDown={(event) => handleKeyDown(event, id)}
          ref={(node) => {
            if (node) tabRefs.current.set(id, node);
            else tabRefs.current.delete(id);
          }} role="tab" tabIndex={activeTab === id || (!activeVisible && keyboardItems[0]?.id === id) ? 0 : -1} type="button">
          <Icon aria-hidden="true" size={15} />{label}
        </button>
      );
  return <>
    <nav aria-label={ariaLabel} aria-orientation="horizontal" className={`workspace-tabs${grouped ? " inspector-workspace-navigation" : ""}`} role="tablist">
      {grouped ? <>
        <div className="inspector-workspace-common">{items.filter(({ id }) => commonTabs.has(id)).map(renderTab)}</div>
        <button type="button" className="inspector-workspace-expand" aria-expanded={expanded} aria-controls={`${tabSetID}-diagnostics`}
          onClick={() => setExpanded((value) => !value)}>{t("高级诊断", "Advanced diagnostics")}{!commonTabs.has(activeTab) ? ` · ${items.find(({ id }) => id === activeTab)?.label ?? ""}` : ""}</button>
        <div className="inspector-workspace-groups" id={`${tabSetID}-diagnostics`} hidden={!expanded}>
          {diagnosticGroups.map((group) => <div key={group.label[1]} role="group" aria-label={t(...group.label)}>
            <span>{t(...group.label)}</span>
            {group.tabs.flatMap((id) => items.filter((item) => item.id === id)).map(renderTab)}
          </div>)}
        </div>
      </> : items.map(renderTab)}
    </nav>
    {items.map(({ id }) => (
      <div aria-labelledby={tabID(id)} className="workspace-content" hidden={activeTab !== id}
        id={panelID(id)} key={id} role="tabpanel" tabIndex={activeTab === id ? 0 : -1}>
        {activeTab === id ? children : null}
      </div>
    ))}
  </>;
}

export function RunWorkspace({ client, runID, onOpenPlugins, onOpenRun, initialTab = "activity" }: {
  client: APIClient;
  runID: string;
  onOpenPlugins?: () => void;
  onOpenRun?: (runID: string) => void;
  initialTab?: RunTab;
}) {
  const { t } = useLocale();
  const [tab, setTab] = useState<RunTab>(initialTab);
  const [inputOpen, setInputOpen] = useState(false);
  const [inputStatus, setInputStatus] = useState<SessionComposerStatus>({ pending: false, error: null });
  const showInputStatus = useCallback((status: SessionComposerStatus) => {
    setInputStatus(status);
    if (status.error) setInputOpen(true);
  }, []);
  const [navigationMode, setNavigationMode] = useState(readRunNavigationMode);
  const [fileTarget, setFileTarget] = useState<{ runID: string; path: string; workspaceID?: string }>({ runID, path: "." });
  const [receiptReviewTarget, setReceiptReviewTarget] =
    useState<ReceiptReviewNavigationTarget | null>(null);
  const [gitAdvancedReview, setGitAdvancedReview] =
    useState<GitAdvancedReviewResultView | null>(null);
  const [gitHubReview, setGitHubReview] =
    useState<GitHubReviewWriteReviewResultView | null>(null);
  const queryClient = useQueryClient();
  const detailQuery = useQuery({
    queryKey: ["run", runID],
    queryFn: ({ signal }) => client.get<RunDetailView>(`/runs/${encodeURIComponent(runID)}`, {}, signal),
    enabled: Boolean(runID),
  });
  const boundSessionID = detailQuery.data?.run.session_id ?? "";
  const contextMessagesQuery = usePagedResource<MessageView>(client,
    ["session", boundSessionID, "messages"],
    `/sessions/${encodeURIComponent(boundSessionID)}/messages`,
    { limit: 100, include_compacted: true }, Boolean(boundSessionID));
  const eventsQuery = usePagedResource<EventView>(client, ["run", runID, "events"],
    `/runs/${encodeURIComponent(runID)}/events`, { limit: 100 }, Boolean(runID) && tab === "events");
  const workQuery = usePagedResource<WorkItemView>(client, ["run", runID, "work"],
    `/runs/${encodeURIComponent(runID)}/work-items`, { limit: 100 }, Boolean(runID) && tab === "work");
  const notesQuery = usePagedResource<NoteView>(client, ["run", runID, "notes"],
    `/runs/${encodeURIComponent(runID)}/notes`, { limit: 100 }, Boolean(runID) && tab === "notes");
  const artifactsQuery = usePagedResource<ArtifactView>(client, ["run", runID, "artifacts"],
    `/runs/${encodeURIComponent(runID)}/artifacts`, { limit: 100 }, Boolean(runID) && tab === "artifacts");
  const toolsQuery = usePagedResource<SupervisorToolRoundView>(client, ["run", runID, "tools"],
    `/runs/${encodeURIComponent(runID)}/tool-rounds`, { limit: 100 }, Boolean(runID) && tab === "tools");
  const stream = useRunEventStream(client, runID);
  const publicModelStream = usePublicModelStream(client, runID, Boolean(runID &&
    client.hasRunExecution && detailQuery.data &&
    ["created", "running", "paused"].includes(detailQuery.data.run.status)));
  const activityQuery = useQuery({
    queryKey: ["run", runID, "activity"],
    queryFn: ({ signal }) => client.get<RunActivityView>(
      `/runs/${encodeURIComponent(runID)}/activity`, { limit: 100 }, signal),
    enabled: Boolean(runID) && tab === "activity",
  });
  const latestStreamFrame = stream.frames.at(-1);
  useRunDetailEventRefresh(runID, latestStreamFrame);
  const journeyHandoffQuery = useQuery({
    queryKey: ["run", runID, "code-handoff"],
    queryFn: ({ signal }) => client.codeHandoff(runID, signal),
    enabled: Boolean(runID) && tab === "journey" && detailQuery.data?.mode.surface === "code",
  });

  useEffect(() => {
    setReceiptReviewTarget(null);
    setGitAdvancedReview(null);
    setGitHubReview(null);
  }, [runID]);

  useEffect(() => subscribeRunNavigationMode(setNavigationMode), []);

  useEffect(() => {
    if (tab !== "verify") setReceiptReviewTarget(null);
  }, [tab]);

  useEffect(() => {
    if (tab !== "activity" || !latestStreamFrame ||
      latestStreamFrame.event.type === "model.delta" ||
      latestStreamFrame.sequence <= (activityQuery.data?.through_sequence ?? 0)) {
      return;
    }
    const delays = [0, 250, 800];
    const timers = delays.map((delay) => window.setTimeout(() => {
      void queryClient.invalidateQueries({ queryKey: ["run", runID, "activity"] });
    }, delay));
    return () => timers.forEach((timer) => window.clearTimeout(timer));
  }, [
    activityQuery.data?.through_sequence,
    latestStreamFrame,
    queryClient,
    runID,
    tab,
  ]);

  const openReceiptReview = (target: ReceiptReviewNavigationTarget) => {
    setReceiptReviewTarget({ ...target });
    setTab("verify");
  };

  const events = useMemo(() => {
    const bySequence = new Map<number, EventView>();
    for (const page of eventsQuery.data?.pages ?? []) {
      for (const event of page.items) {
        bySequence.set(event.sequence, event);
      }
    }
    for (const frame of stream.frames) {
      bySequence.set(frame.event.sequence, frame.event);
    }
    return [...bySequence.values()].sort((left, right) => right.sequence - left.sequence);
  }, [eventsQuery.data, stream.frames]);
  const work = useMemo(() => workQuery.data?.pages.flatMap((page) => page.items) ?? [], [workQuery.data]);
  const notes = useMemo(() => notesQuery.data?.pages.flatMap((page) => page.items) ?? [], [notesQuery.data]);
  const artifacts = useMemo(() => artifactsQuery.data?.pages.flatMap((page) => page.items) ?? [], [artifactsQuery.data]);
  const rounds = useMemo(() => toolsQuery.data?.pages.flatMap((page) => page.items) ?? [], [toolsQuery.data]);
  const contextTokens = useMemo(() => (contextMessagesQuery.data?.pages
    .flatMap((page) => page.items) ?? []).filter((message) => !message.compacted)
    .reduce((total, message) => total + message.token_estimate, 0), [contextMessagesQuery.data]);
  const visibleTabs = useMemo(() => tabs.filter(({ id }) => {
    if (id === "analyzer" && !client.hasEmbeddedAnalyzerExecution) return false;
    return detailQuery.data?.mode.surface !== "cyber" ||
      !["journey", "verify", "delivery", "handoff"].includes(id);
  }), [client, detailQuery.data?.mode.surface]);

  useEffect(() => {
    if (!visibleTabs.some((item) => item.id === tab)) setTab("activity");
  }, [tab, visibleTabs]);
  const commands = useMemo<CommandPaletteCommand[]>(() => [
    ...visibleTabs.map(({ id, label }) => ({ id: `view-${id}`, label: t(`打开${label[0]}`, `Open ${label[1]}`),
      group: t("导航", "Navigate"), keywords: [id, "run"], run: () => setTab(id) })),
    { id: "refresh-run", label: t("刷新 Run 数据", "Refresh Run data"), group: t("数据", "Data"),
      keywords: ["reload", "sync"], run: () => {
        void queryClient.invalidateQueries({ queryKey: ["run", runID] });
      } },
  ], [queryClient, runID, t, visibleTabs]);

  if (!runID) {
    return <EmptyWorkspace icon={<Boxes aria-hidden="true" size={24} />} title={t("选择一个 Run", "Select a Run")} />;
  }
  if (detailQuery.isLoading) {
    return <LoadingState label={t("加载 Run", "Loading Run")} />;
  }
  if (detailQuery.isError || !detailQuery.data) {
    return <ErrorState error={detailQuery.error} />;
  }
  const detail = detailQuery.data;

  return (
    <div className="workspace-view">
      <header className="workspace-header">
        <div>
          <div className="workspace-kicker">Run {shortID(detail.run.id)}</div>
          <h1>{detail.mission.goal}</h1>
          <div className="header-meta">
            <span>{t("执行记录状态：", "Execution record state: ")}<LifecycleStatusBadge status={detail.run.status} /></span>
            <span>{detail.mission.profile}</span>
            <span>{detail.run.config.model_route}</span>
          </div>
        </div>
        <div className="workspace-header-actions">
          <CommandPalette commands={commands} />
          <div className={`stream-indicator stream-${stream.status}`} title={stream.error || stream.status}>
            <Radio aria-hidden="true" size={15} />
            {stream.status}
          </div>
        </div>
      </header>
      <RunWorkspaceTabs activeTab={tab} ariaLabel={t("Run 视图", "Run views")} grouped expandDiagnostics={navigationMode === "diagnostic"}
        items={visibleTabs.map(({ id, label, icon }) => ({ id, label: t(...label), icon }))}
        onSelect={setTab}>
        {tab === "activity" && (
          activityQuery.isLoading ? <LoadingState label={t("加载活动", "Loading activity")} /> :
            activityQuery.isError || !activityQuery.data ?
              <ErrorState error={activityQuery.error} /> :
              <RunActivityTimeline activity={activityQuery.data}
                liveCommentary={publicModelStream.snapshot}
                liveStatus={publicModelStream.status} streamError={stream.error || publicModelStream.error} />
        )}
        {tab === "overview" && <RunOverview client={client} detail={detail} />}
        {tab === "journey" && detail.mode.surface === "code" &&
          <CodeJourney detail={detail}
            receiptReviewFacts={journeyHandoffQuery.data?.verification_snapshot_receipt_reviews}
            receiptReviewFactsState={journeyHandoffQuery.isError ? "unavailable" :
              journeyHandoffQuery.isLoading ? "loading" : "ready"}
            onNavigate={setTab} onOpenReceiptReview={openReceiptReview} />}
        {tab === "actions" && <OperatorActionCenter client={client} runID={runID}
          onNavigate={(destination) => setTab(destination === "approvals" ? "approvals" :
            destination === "diffs" ? "diffs" : "overview")} />}
        {tab === "approvals" && <ApprovalPanel client={client} runID={runID} />}
        {tab === "diffs" && <FileEditPanel client={client} runID={runID} runStatus={detail.run.status} />}
        {tab === "repository" && <div className="projection-stack">
          <div className="inspector-repository-heading"><h2>{t("当前运行的仓库工具", "Repository tools for this Run")}</h2>
            <p>{t("操作范围：本次执行绑定的仓库。任务提交和 PR 请从对话的审阅入口进入；此处可使用独立执行的高级工具。", "Scope: the repository bound to this execution. Use conversation review for task commits and PRs; advanced tools for independent executions are available here.")}</p></div>
          <RepositoryStatePanel client={client} workspaceID={detail.mission.workspace_id ?? ""} />
          <details className="inspector-workspace-section" open={Boolean(gitAdvancedReview)}><summary>{t("Git 操作（高级）", "Git operations (advanced)")}</summary>
          <GitAdvancedPanel client={client} onOpenApprovals={() => setTab("approvals")}
            onRetainedReviewChange={setGitAdvancedReview} retainedReview={gitAdvancedReview}
            runID={runID} />
          </details>
          <details className="inspector-workspace-section" open={Boolean(gitHubReview)}><summary>{t("GitHub 操作（高级）", "GitHub operations (advanced)")}</summary>
          <GitHubReviewPanel client={client} onOpenApprovals={() => setTab("approvals")}
            onOpenDelivery={() => setTab("delivery")}
            onRetainedReviewChange={setGitHubReview} retainedReview={gitHubReview}
            runID={runID} />
          </details>
          <details className="inspector-workspace-section"><summary>{t("仓库历史", "Repository history")}</summary>
          <RepositoryHistoryPanel client={client} workspaceID={detail.mission.workspace_id ?? ""} />
          </details>
          <details className="inspector-workspace-section"><summary>{t("当前目录差异", "Current directory diff")}</summary>
          <RepositoryDiffPanel client={client} workspaceID={detail.mission.workspace_id ?? ""} />
          </details>
        </div>}
        {tab === "files" && <WorkspaceExplorer client={client}
          initialPath={fileTarget.runID === runID ? fileTarget.path : "."}
          key={`${detail.mission.workspace_id ?? "unbound"}:${fileTarget.runID === runID ? fileTarget.path : "."}`}
          runID={runID} workspaceID={(fileTarget.runID === runID ? fileTarget.workspaceID : undefined) ?? detail.mission.workspace_id ?? ""} />}
        {tab === "evidence" && <EvidenceInventory client={client} runID={runID}
          onOpenSource={(sourceRef) => {
            setFileTarget({ runID, path: sourceRef });
            setTab("files");
          }} />}
        {tab === "verify" && detail.mode.surface === "code" &&
          <div className="projection-stack"><VerificationPlan client={client} runID={runID}
            receiptReviewTarget={receiptReviewTarget ?? undefined} />
            <VerificationEvidence client={client} runID={runID} /></div>}
        {tab === "delivery" && detail.mode.surface === "code" &&
          <StandardCodeDeliveryPanel client={client} runID={runID}
            onOpenCheckpoints={() => setTab("checkpoints")}
            onOpenFile={(path, workspaceID) => {
              setFileTarget({ runID, path, workspaceID });
              setTab("files");
            }} />}
        {tab === "ui-evidence" && <UIEvidencePanel client={client} runID={runID} />}
        {tab === "handoff" && detail.mode.surface === "code" &&
          <CodeHandoffPanel client={client} runID={runID}
            onOpenDelivery={() => setTab("delivery")}
            onOpenReceiptReview={openReceiptReview} />}
        {tab === "receipts" && <OperationReceiptHistory client={client} runID={runID} />}
        {tab === "checkpoints" && <WorkspaceCheckpointPanel client={client} runID={runID}
          runStatus={detail.run.status} onOpenRun={onOpenRun} />}
        {tab === "agents" && <AgentGraphPanel client={client} runID={runID} />}
        {tab === "delegations" && <DelegationsPanel client={client} runID={runID} />}
        {tab === "fanout" && <FanoutPanel client={client} runID={runID} />}
        {tab === "child-tasks" && <div className="projection-stack">
          <ChildTasksPanel client={client} runID={runID} />
          <BatchDeliveriesPanel client={client} runID={runID} />
        </div>}
        {tab === "sandbox" && <DockerSandboxPanel client={client} runID={runID} />}
        {tab === "findings" && <FindingsPanel client={client} runID={runID} />}
        {tab === "events" && (
          <CollectionState query={eventsQuery} empty="暂无事件">
            {stream.error && <div className="inline-warning">SSE: {stream.error}</div>}
            <EventList events={events} />
            <LoadMoreButton hasNextPage={Boolean(eventsQuery.hasNextPage)} isFetching={eventsQuery.isFetchingNextPage} onClick={() => void eventsQuery.fetchNextPage()} />
          </CollectionState>
        )}
        {tab === "work" && (
          <CollectionState query={workQuery} empty={t("暂无计划项", "No Plan items")}>
            <WorkTable client={client} items={work} />
            <LoadMoreButton hasNextPage={Boolean(workQuery.hasNextPage)} isFetching={workQuery.isFetchingNextPage} onClick={() => void workQuery.fetchNextPage()} />
          </CollectionState>
        )}
        {tab === "context" && <ContextContinuityPanel client={client} runID={runID}
          sessionID={detail.run.session_id ?? ""} workspaceID={detail.mission.workspace_id ?? ""}
          onOpenRun={onOpenRun} />}
        {tab === "notes" && (
          <CollectionState query={notesQuery} empty="暂无记忆">
            <NoteList client={client} notes={notes} />
            <LoadMoreButton hasNextPage={Boolean(notesQuery.hasNextPage)} isFetching={notesQuery.isFetchingNextPage} onClick={() => void notesQuery.fetchNextPage()} />
          </CollectionState>
        )}
        {tab === "artifacts" && (
          <CollectionState query={artifactsQuery} empty="暂无产物">
            <ArtifactTable artifacts={artifacts} client={client} />
            <LoadMoreButton hasNextPage={Boolean(artifactsQuery.hasNextPage)} isFetching={artifactsQuery.isFetchingNextPage} onClick={() => void artifactsQuery.fetchNextPage()} />
          </CollectionState>
        )}
        {tab === "tools" && (
          <CollectionState query={toolsQuery} empty="暂无工具轮次">
            <ToolRounds client={client} rounds={rounds} />
            <LoadMoreButton hasNextPage={Boolean(toolsQuery.hasNextPage)} isFetching={toolsQuery.isFetchingNextPage} onClick={() => void toolsQuery.fetchNextPage()} />
          </CollectionState>
        )}
        {tab === "analyzer" && client.hasEmbeddedAnalyzerExecution &&
          <EmbeddedAnalyzerPanel client={client} runID={runID} />}
      </RunWorkspaceTabs>
      {detail.run.session_id && client.hasSessionMessages && <details className="inspector-workspace-input" open={inputOpen}
        onToggle={(event) => setInputOpen(event.currentTarget.open)}><summary><span>{t("向此运行补充输入（高级）", "Add input to this Run (advanced)")}</span>
          <span role="status">{inputStatus.pending ? t(" · 输入请求处理中", " · Input in progress") : inputStatus.error ? t(" · 输入未完成，请查看原因", " · Input needs attention") : ""}</span></summary>
        <p>{t("输入目标：本次执行。发送时会核对执行状态与权限；继续整个任务请返回对话。", "Input target: this execution. Sending checks its state and permissions. Return to the conversation to continue the whole task.")}</p>
        <SessionComposer client={client} onStatusChange={showInputStatus}
        contextPartial={Boolean(contextMessagesQuery.hasNextPage)} contextTokens={contextTokens}
        onOpenPlugins={onOpenPlugins} run={detail.run} sessionID={detail.run.session_id}
        phase={detail.mode.phase} publicModelStream={publicModelStream}
        workspaceID={detail.mission.workspace_id ?? ""} /></details>}
    </div>
  );
}

function RunOverview({ client, detail }: { client: APIClient; detail: RunDetailView }) {
  const { t } = useLocale();
  const checkpoint = detail.checkpoint;
  const usage = detail.tool_usage;
  const percent = usage.limit > 0 ? Math.min(100, Math.round((usage.consumed / usage.limit) * 100)) : 0;
  const steering = detail.operator_steering;
  return (
    <div className="overview-layout">
      <section className="metric-strip" aria-label="Run 指标">
        <div><span>{t("下一轮", "Next turn")}</span><strong>{checkpoint?.next_turn ?? 0}</strong></div>
        <div><span>{t("累计令牌", "Total tokens")}</span><strong>{formatNumber(checkpoint?.total_tokens)}</strong></div>
        <div><span>{t("工具调用", "Tool calls")}</span><strong>{formatNumber(usage.consumed)} / {formatNumber(usage.limit)}</strong></div>
        <div><span>{t("执行耗时", "Execution")}</span><strong>{formatNumber(checkpoint?.execution_millis)} ms</strong></div>
      </section>
      <ModelUsagePanel detail={detail} />
      <section className="detail-section">
        <h2>目标与范围</h2>
        <dl className="detail-grid">
          <KeyValue label={t(...diagnosticVocabulary.mission)} value={detail.mission.id} />
          <KeyValue label={t("工作区", "Workspace")} value={detail.mission.workspace_id} />
          <KeyValue label={t("工作模式", "Surface")} value={detail.mode.surface} />
          <KeyValue label={t("执行阶段", "Execution phase")} value={detail.mode.phase} />
          <KeyValue label={t("模式修订", "Mode revision")} value={formatNumber(detail.mode.revision)} />
          <KeyValue label={t("网络", "Network")} value={detail.mission.scope.network_mode} />
          <KeyValue label={t("允许目标", "Allowed targets")} value={detail.mission.scope.allowed_targets?.join(", ")} />
          <KeyValue label={t("交互式", "Interactive")} value={detail.run.config.interactive ? t("是", "yes") : t("否", "no")} />
          <KeyValue label={t("创建时间", "Created")} value={formatDate(detail.run.created_at)} />
        </dl>
      </section>
      <RunControlPanel client={client} detail={detail} />
      <ActiveCallCancelPanel client={client} detail={detail} />
      <RunWakePanel client={client} detail={detail} />
      <ExecutionBoundarySummary detail={detail} />
      <AgentCodeToolsPanel detail={detail} />
      <section className="detail-section">
        <h2>执行状态</h2>
        <dl className="detail-grid">
          <KeyValue label="Supervisor phase" value={checkpoint?.phase} />
          <KeyValue label="Attempt" value={checkpoint?.attempt_id} />
          <KeyValue label="Repair phase" value={checkpoint?.repair_phase} />
          <KeyValue label="Last error" value={checkpoint?.last_error} />
          <KeyValue label="Lease owner" value={detail.execution_lease?.owner_id} />
          <KeyValue label="Lease" value={detail.execution_lease ? <StatusBadge status={detail.execution_lease.active ? "active" : detail.execution_lease.status} /> : "-"} />
        </dl>
      </section>
      <section className="detail-section">
        <div className="section-heading"><h2>工具预算</h2><span>{percent}%</span></div>
        <progress aria-label="工具预算使用率" className="budget-track" max={100} value={percent}>{percent}%</progress>
        <dl className="detail-grid compact">
          <KeyValue label="Consumed" value={formatNumber(usage.consumed)} />
          <KeyValue label="Remaining" value={formatNumber(usage.remaining)} />
          <KeyValue label="Exhausted" value={formatDate(usage.exhausted_at)} />
        </dl>
      </section>
      <OperatorSteeringPanel state={steering} />
      {detail.plan_delivery && <PlanDeliveryPanel client={client} detail={detail}
        state={detail.plan_delivery} />}
      {detail.external_skills && <ExternalSkillsSection client={client} initial={detail.external_skills} runID={detail.run.id} />}
    </div>
  );
}

function AgentCodeToolsPanel({ detail }: { detail: RunDetailView }) {
  const { t } = useLocale();
  const snapshot = detail.agent_code_tools;
  const available = snapshot.tools.filter((tool) => tool.available).length;
  return <section className="detail-section">
    <div className="section-heading">
      <h2>{t("模型工作区工具", "Model workspace tools")}</h2>
      <span>{available}/{snapshot.tools.length} · {shortID(snapshot.generation)}</span>
    </div>
    <dl className="detail-grid compact">
      <KeyValue label={t("协议", "Protocol")} value={snapshot.protocol_version} />
      <KeyValue label={t("作用域", "Scope")}
        value={`${snapshot.surface}/${snapshot.phase}/${snapshot.role}/${snapshot.profile}`} />
    </dl>
    <div className="steering-list" aria-label={t("模型工作区工具能力", "Model workspace tool capabilities")}>
      {snapshot.tools.map((tool) => <div className="steering-row" key={tool.name}>
        <code>{tool.name}</code>
        <StatusBadge status={tool.available ? "available" : "unavailable"} />
        <span>{tool.source} · {tool.available
          ? `${tool.class} · ${tool.approval}`
          : tool.refusal_reason ?? t("当前模式不可用", "Unavailable in the current mode")}</span>
      </div>)}
    </div>
  </section>;
}

export function RunControlPanel({ client, detail, threadID = "" }: {
  client: APIClient;
  detail: RunDetailView;
  threadID?: string;
}) {
  const { t } = useLocale();
  const queryClient = useQueryClient();
  const [maxSteps, setMaxSteps] = useState(1);
  const [lastExecution, setLastExecution] = useState<RunExecutionControlView | null>(null);
  const operationKeys = useRef(new Map<string, string>());
  const operationKey = (kind: string) => {
    const existing = operationKeys.current.get(kind);
    if (existing) {
      return existing;
    }
    const created = `web-run-${kind}-${globalThis.crypto.randomUUID()}`;
    operationKeys.current.set(kind, created);
    return created;
  };
  const lifecycle = useMutation({
    mutationFn: (action: RunLifecycleControlRequestView["action"]) =>
      client.controlRunLifecycle(detail.run.id, {
        version: "run_lifecycle_control.v1", action,
      }, operationKey(`lifecycle-${action}`)),
    onSuccess: (result: RunLifecycleControlView, action) => {
      operationKeys.current.delete(`lifecycle-${action}`);
      queryClient.setQueryData<RunDetailView>(["run", detail.run.id], (current) => current
        ? { ...current, run: result.run }
        : current);
      void queryClient.invalidateQueries({ queryKey: ["run", detail.run.id] });
      void queryClient.invalidateQueries({ queryKey: ["run", detail.run.id, "events"] });
      if (threadID) {
        void queryClient.invalidateQueries({ queryKey: ["thread", threadID] });
        void queryClient.invalidateQueries({ queryKey: ["thread", threadID, "transcript"] });
      }
    },
  });
  const execution = useMutation({
    mutationFn: () => client.executeRun(detail.run.id, {
      version: "run_execution_handoff.v1", max_steps: maxSteps,
    }, operationKey(`execute-${maxSteps}`)),
    onSuccess: (result) => {
      operationKeys.current.delete(`execute-${result.max_steps}`);
      setLastExecution(result);
      void queryClient.invalidateQueries({ queryKey: ["run", detail.run.id] });
      void queryClient.invalidateQueries({ queryKey: ["run", detail.run.id, "events"] });
      if (threadID) {
        void queryClient.invalidateQueries({ queryKey: ["thread", threadID] });
        void queryClient.invalidateQueries({ queryKey: ["thread", threadID, "transcript"] });
      }
    },
  });
  if (!client.hasRunLifecycle && !client.hasRunExecution) {
    return null;
  }
  const activeLease = Boolean(detail.execution_lease?.active);
  const queued = detail.operator_steering.pending + detail.operator_steering.prepared;
  const lifecycleAction: RunLifecycleControlRequestView["action"] | null =
    detail.run.status === "created" ? "start" :
      detail.run.status === "running" ? "pause" :
        detail.run.status === "paused" ? "resume" : null;
  const lifecycleDisabled = lifecycle.isPending || execution.isPending || activeLease ||
    lifecycleAction === null;
  const executionDisabled = execution.isPending || lifecycle.isPending || activeLease ||
    detail.run.status !== "running" || queued === 0;
  const LifecycleIcon = lifecycleAction === "pause" ? Pause : Play;
  const error = lifecycle.error ?? execution.error;
  return (
    <section className="detail-section run-control-section">
      <div className="section-heading">
        <h2><Play aria-hidden="true" size={15} />{t("Run 控制", "Run control")}</h2>
        {activeLease ? <StatusBadge status="busy" label={t("执行访问被占用", "Execution access in use")} /> : <LifecycleStatusBadge status={detail.run.status} />}
      </div>
      <div className="run-control-row">
        {client.hasRunLifecycle && lifecycleAction && (
          <button className="command-button" disabled={lifecycleDisabled}
            onClick={() => lifecycle.mutate(lifecycleAction)} type="button">
            {lifecycle.isPending
              ? <LoaderCircle aria-hidden="true" className="spin" size={16} />
              : <LifecycleIcon aria-hidden="true" size={16} />}
            {lifecycleAction === "start" ? t("启动", "Start") : lifecycleAction === "pause" ? t("暂停", "Pause") : t("恢复", "Resume")}
          </button>
        )}
        {client.hasRunExecution && (
          <div className="run-execution-control">
            <label htmlFor={`run-max-steps-${detail.run.id}`}>{t("步数", "Steps")}</label>
            <input id={`run-max-steps-${detail.run.id}`} max={8} min={1}
              onChange={(event) => setMaxSteps(Math.max(1, Math.min(8,
                Number.parseInt(event.target.value, 10) || 1)))} type="number" value={maxSteps} />
            <button className="command-button" disabled={executionDisabled}
              onClick={() => execution.mutate()} type="button">
              {execution.isPending
                ? <LoaderCircle aria-hidden="true" className="spin" size={16} />
                : <ChevronsRight aria-hidden="true" size={16} />}
              {t("执行队列", "Run queue")}
            </button>
          </div>
        )}
      </div>
      {lastExecution && (
        <div className="run-control-result" role="status">
          <StatusBadge status={lastExecution.status} />
          <span>{lastExecution.stop_reason}</span>
          <span>{t(`${lastExecution.steps_completed}/${lastExecution.selected_count} 步`, `${lastExecution.steps_completed}/${lastExecution.selected_count} steps`)}</span>
        </div>
      )}
      {error && <div className="inline-warning" role="alert">
        {error instanceof Error ? error.message : t("Run 控制失败", "Run control failed")}
      </div>}
    </section>
  );
}

function ActiveCallCancelPanel({ client, detail }: {
  client: APIClient;
  detail: RunDetailView;
}) {
  const { t } = useLocale();
  const queryClient = useQueryClient();
  const attemptID = detail.checkpoint?.attempt_id ?? "";
  const [modelAttempt, setModelAttempt] = useState(1);
  const [reason, setReason] = useState("");
  const [lastResult, setLastResult] = useState<ModelCancellationView | null>(null);
  const operationKey = useRef<string | null>(null);
  const cancel = useMutation({
    mutationFn: () => {
      const key = operationKey.current ??
        (operationKey.current = `web-run-cancel-call-${globalThis.crypto.randomUUID()}`);
      const trimmed = reason.trim();
      const body: ModelCancellationRequestView = trimmed.length > 0
        ? { attempt_id: attemptID, model_attempt: modelAttempt, reason: trimmed }
        : { attempt_id: attemptID, model_attempt: modelAttempt };
      return client.cancelModelCall(detail.run.id, body, key);
    },
    onSuccess: (result) => {
      operationKey.current = null;
      setLastResult(result);
      void queryClient.invalidateQueries({ queryKey: ["run", detail.run.id] });
      void queryClient.invalidateQueries({ queryKey: ["run", detail.run.id, "events"] });
    },
  });
  if (!client.hasControl || !attemptID) {
    return null;
  }
  return (
    <section className="detail-section run-control-section">
      <div className="section-heading">
        <h2><Ban aria-hidden="true" size={15} />{t("取消模型调用", "Cancel model call")}</h2>
        {detail.execution_lease?.active ? <StatusBadge status="busy" label={t("执行访问被占用", "Execution access in use")} /> : <LifecycleStatusBadge status={detail.run.status} />}
      </div>
      <p className="run-cancel-hint">{t(`按尝试标识请求取消模型调用（尝试 ${shortID(attemptID)}）；是否仍在执行由服务端核验。`, `Request cancellation by attempt (${shortID(attemptID)}); the server verifies whether the call is still executing.`)}</p>
      <div className="run-control-row">
        <div className="run-execution-control">
          <label htmlFor={`run-cancel-attempt-${detail.run.id}`}>{t("模型尝试", "Model attempt")}</label>
          <input id={`run-cancel-attempt-${detail.run.id}`} min={1} type="number"
            onChange={(event) => setModelAttempt(Math.max(1,
              Number.parseInt(event.target.value, 10) || 1))} value={modelAttempt} />
        </div>
        <input aria-label={t("取消原因（可选）", "Cancellation reason (optional)")} className="run-cancel-reason" maxLength={1024}
          onChange={(event) => setReason(event.target.value)}
          placeholder={t("原因（可选）", "Reason (optional)")} type="text" value={reason} />
        <button className="command-button" disabled={cancel.isPending}
          onClick={() => cancel.mutate()} type="button">
          {cancel.isPending
            ? <LoaderCircle aria-hidden="true" className="spin" size={16} />
            : <Ban aria-hidden="true" size={16} />}
          {t("取消调用", "Cancel call")}
        </button>
      </div>
      {lastResult && (
        <div className="run-control-result" role="status">
          <StatusBadge status={lastResult.status} />
          <span>{t("尝试", "attempt")} {lastResult.model_attempt}</span>
          {lastResult.replayed && <span>{t("已重放", "replayed")}</span>}
        </div>
      )}
      {cancel.error && <div className="inline-warning" role="alert">
        {cancel.error instanceof Error ? cancel.error.message : t("取消模型调用失败", "Model call cancellation failed")}
      </div>}
    </section>
  );
}

function ExecutionBoundarySummary({ detail }: { detail: RunDetailView }) {
  const { t } = useLocale();
  return <section className="detail-section execution-boundary-summary">
    <div className="section-heading">
      <h2><ShieldCheck aria-hidden="true" size={15} />{t("权限摘要", "Permission summary")}</h2>
      <span>{t("设置 > 权限", "Settings > Permissions")}</span>
    </div>
    <dl className="detail-grid compact">
      <KeyValue label={t("权限", "Permission")} value={detail.execution_permission.mode} />
      <KeyValue label={t("交互", "Interaction")} value={detail.execution_interaction.mode} />
      <KeyValue label={t("环境", "Environment")} value={detail.execution_profile.profile} />
      <KeyValue label={t("工作区信任", "Workspace trust")}
        value={detail.execution_interaction.workspace_trust} />
      <KeyValue label={t("运行时授权", "Runtime authority")} value={t("禁用", "disabled")} />
      <KeyValue label={t("Agent 输入", "Agent input")} value={t("默认禁用", "disabled by default")} />
    </dl>
  </section>;
}

export function OperatorSteeringPanel({ state }: { state: OperatorSteeringQueueView }) {
  const { t } = useLocale();
  return (
    <section className="detail-section steering-section">
      <div className="section-heading">
        <h2><ListOrdered aria-hidden="true" size={15} />{t("操作者引导", "Operator steering")}</h2>
        <StatusBadge status={state.pending + state.prepared > 0 ? "pending" : "idle"} />
      </div>
      <div className="steering-state-line">
        <span>{t("已排队", "Queued")} {formatNumber(state.pending)}</span>
        <span>{t("已准备", "Prepared")} {formatNumber(state.prepared)}</span>
        <span>{t("已提交", "Committed")} {formatNumber(state.committed)}</span>
        <span>{t("已取消", "Cancelled")} {formatNumber(state.cancelled)}</span>
      </div>
      <div className="steering-list" aria-label={t("操作者引导元数据", "Operator steering metadata")}>
        {state.messages.length === 0 ? <p>{t("没有操作者引导记录", "No operator guidance recorded")}</p> :
          state.messages.map((message) => (
            <div className="steering-row" key={message.id}>
              <span>#{message.sequence}</span>
              <code>{shortID(message.id)}</code>
              <StatusBadge status={message.status} />
              <time dateTime={message.created_at}>{formatDate(message.created_at)}</time>
            </div>
          ))}
      </div>
    </section>
  );
}

function EventList({ events }: { events: EventView[] }) {
  const { t } = useLocale();
  if (events.length === 0) {
    return <EmptyState>暂无事件</EmptyState>;
  }
  return (
    <div className="event-list">
      {events.map((event) => (
        <details className="event-row" key={event.event_id}>
          <summary>
            <span className="event-sequence">#{event.sequence}</span>
            <strong>{event.type}</strong>
            <span>{event.source}</span>
            <time dateTime={event.created_at}>{formatDate(event.created_at)}</time>
          </summary>
          <div>
            <label>{t("脱敏事件载荷", "Redacted event payload")}</label>
            <pre>{JSON.stringify(event.payload, null, 2)}</pre>
          </div>
        </details>
      ))}
    </div>
  );
}

function WorkTable({ client, items }: { client: APIClient; items: WorkItemView[] }) {
  const { t } = useLocale();
  const [expanded, setExpanded] = useState<string | null>(null);
  if (items.length === 0) {
    return <EmptyState>{t("暂无计划项", "No Plan items")}</EmptyState>;
  }
  return (
    <div className="table-scroll"><table><thead><tr><th>{t(...canonicalVocabulary.planItem)}</th><th>{t("状态", "Status")}</th><th>{t("优先级", "Priority")}</th><th>{t("负责人", "Owner")}</th><th>{t("版本", "Version")}</th><th aria-label={t("详情", "Details")} /></tr></thead><tbody>
      {items.map((item) => (
        <Fragment key={item.id}>
          <tr>
            <td><strong>{item.title}</strong>{item.description && <small>{item.description}</small>}</td>
            <td><StatusBadge status={item.status} /></td>
            <td>{item.priority}</td>
            <td>{item.owner_agent_id || item.owner || "-"}</td>
            <td>v{item.version}</td>
            <td><button aria-expanded={expanded === item.id} className="row-detail-toggle" onClick={() => setExpanded(expanded === item.id ? null : item.id)} type="button">{expanded === item.id ? t("收起", "Collapse") : t("详情", "Details")}</button></td>
          </tr>
          {expanded === item.id && <tr className="detail-row"><td colSpan={6}><WorkItemDetail client={client} id={item.id} /></td></tr>}
        </Fragment>
      ))}
    </tbody></table></div>
  );
}

function WorkItemDetail({ client, id }: { client: APIClient; id: string }) {
  const { t } = useLocale();
  const query = useQuery({ queryKey: ["work-item", id], queryFn: ({ signal }) => client.getWorkItem(id, signal) });
  if (query.isLoading) {
    return <LoadingState label={t("加载计划项详情", "Loading Plan item details")} />;
  }
  if (query.isError || !query.data) {
    return <ErrorState error={query.error} />;
  }
  const item = query.data;
  return (
    <dl className="detail-grid">
      <KeyValue label="ID" value={item.id} />
      <KeyValue label="Run" value={item.run_id} />
      <KeyValue label={t("负责 Agent", "Owner agent")} value={item.owner_agent_id} />
      <KeyValue label={t("负责人", "Owner")} value={item.owner} />
      <KeyValue label={t("阻塞原因", "Blocked")} value={item.blocked_reason} />
      <KeyValue label={t("验收标准", "Acceptance")} value={item.acceptance_criteria.join(" · ")} />
      <KeyValue label={t("依赖", "Dependencies")} value={item.dependencies.join(" · ")} />
      <KeyValue label={t("创建时间", "Created")} value={formatDate(item.created_at)} />
      <KeyValue label={t("更新时间", "Updated")} value={formatDate(item.updated_at)} />
      <KeyValue label={t("完成时间", "Completed")} value={item.completed_at ? formatDate(item.completed_at) : "-"} />
    </dl>
  );
}

function NoteList({ client, notes }: { client: APIClient; notes: NoteView[] }) {
  const { t } = useLocale();
  const [expanded, setExpanded] = useState<string | null>(null);
  if (notes.length === 0) {
    return <EmptyState>暂无记忆</EmptyState>;
  }
  return <div className="note-list">{notes.map((note) => (
    <article className="note-item" key={note.id}>
      <header><div><strong>{note.title}</strong><span>{note.category} / {note.visibility}</span></div><StatusBadge status={note.status} /></header>
      <p>{note.content}</p>
      <footer><span>{note.tags.join(" · ") || t("无标签", "untagged")}</span><div><time dateTime={note.updated_at}>{formatDate(note.updated_at)}</time><button aria-expanded={expanded === note.id} className="row-detail-toggle" onClick={() => setExpanded(expanded === note.id ? null : note.id)} type="button">{expanded === note.id ? t("收起", "Collapse") : t("详情", "Details")}</button></div></footer>
      {expanded === note.id && <NoteDetail client={client} id={note.id} />}
    </article>
  ))}</div>;
}

function NoteDetail({ client, id }: { client: APIClient; id: string }) {
  const { t } = useLocale();
  const query = useQuery({ queryKey: ["note", id], queryFn: ({ signal }) => client.getNote(id, signal) });
  if (query.isLoading) {
    return <LoadingState label={t("加载记忆详情", "Loading memory details")} />;
  }
  if (query.isError || !query.data) {
    return <ErrorState error={query.error} />;
  }
  const note = query.data;
  return (
    <dl className="detail-grid">
      <KeyValue label="ID" value={note.id} />
      <KeyValue label="Run" value={note.run_id} />
      <KeyValue label={t("负责 Agent", "Owner agent")} value={note.owner_agent_id} />
      <KeyValue label={t("负责人", "Owner")} value={note.owner} />
      <KeyValue label={t("置顶", "Pinned")} value={note.pinned ? t("是", "yes") : t("否", "no")} />
      <KeyValue label={t("版本", "Version")} value={`v${note.version}`} />
      <KeyValue label={t("标签", "Tags")} value={note.tags.join(" · ")} />
      <KeyValue label={t("来源引用", "Source refs")} value={note.source_refs.join(" · ")} />
      <KeyValue label={t("证据", "Evidence")} value={note.evidence_ids.join(" · ")} />
      <KeyValue label={t("创建时间", "Created")} value={formatDate(note.created_at)} />
      <KeyValue label={t("更新时间", "Updated")} value={formatDate(note.updated_at)} />
      <KeyValue label={t("归档时间", "Archived")} value={note.archived_at ? formatDate(note.archived_at) : "-"} />
    </dl>
  );
}

function ArtifactTable({ artifacts, client }: { artifacts: ArtifactView[]; client: APIClient }) {
  const { t } = useLocale();
  const [expanded, setExpanded] = useState<string | null>(null);
  if (artifacts.length === 0) {
    return <EmptyState>暂无产物</EmptyState>;
  }
  return (
    <div className="table-scroll"><table><thead><tr><th>{t("描述符", "Descriptor")}</th><th>{t("工具 / 流", "Tool / stream")}</th><th>MIME</th><th>{t("大小", "Size")}</th><th>SHA-256</th><th aria-label={t("详情", "Details")} /></tr></thead><tbody>
      {artifacts.map((item) => (
        <Fragment key={item.id}>
          <tr>
            <td><strong>{shortID(item.id)}</strong><small>{item.kind}{item.redacted ? t(" / 已脱敏", " / redacted") : ""}</small></td>
            <td>{item.tool_name}<small>{item.stream}</small></td>
            <td>{item.mime}</td>
            <td>{formatBytes(item.size_bytes)}</td>
            <td><code>{shortID(item.sha256)}</code></td>
            <td><button aria-expanded={expanded === item.id} className="row-detail-toggle" onClick={() => setExpanded(expanded === item.id ? null : item.id)} type="button">{expanded === item.id ? t("收起", "Collapse") : t("详情", "Details")}</button></td>
          </tr>
          {expanded === item.id && <tr className="detail-row"><td colSpan={6}><ArtifactDetail client={client} id={item.id} /></td></tr>}
        </Fragment>
      ))}
    </tbody></table></div>
  );
}

function ToolRounds({ client, rounds }: {
  client: APIClient;
  rounds: SupervisorToolRoundView[];
}) {
  const { t } = useLocale();
  if (rounds.length === 0) {
    return <EmptyState>暂无工具轮次</EmptyState>;
  }
  return <div className="tool-rounds">{rounds.map((round) => (
    <section className="tool-round" key={`${round.attempt_id}-${round.turn}-${round.round}`}>
      <header><strong>{t("回合", "Turn")} {round.turn} / {t("轮次", "Round")} {round.round}</strong><span>{round.attempt_id}</span><time dateTime={round.created_at}>{formatDate(round.created_at)}</time></header>
      {round.calls.map((call) => (
        <details className="tool-call" key={`${call.position}-${call.call_id}`}>
          <summary><span>{call.position}</span><strong>{call.tool_name}</strong><StatusBadge status={call.status} /></summary>
          {call.detail_available && call.detail
            ? <ThreadActivityToolDetailPanel activityRef={call.call_id} client={client}
                runID={round.run_id} threadID={round.thread_id ?? ""} tool={call.detail} />
            : <p className="inline-warning">{t(
                "此历史工具调用没有可安全展示的结构化详情。",
                "No safe structured detail is available for this historical tool call.",
              )}</p>}
        </details>
      ))}
    </section>
  ))}</div>;
}

function CollectionState({ query, empty, children }: { query: { isLoading: boolean; isError: boolean; error: unknown; data?: unknown }; empty: string; children: React.ReactNode }) {
  if (query.isLoading) {
    return <LoadingState />;
  }
  if (query.isError) {
    return <ErrorState error={query.error} />;
  }
  if (!query.data) {
    return <EmptyState>{empty}</EmptyState>;
  }
  return <>{children}</>;
}

function EmptyWorkspace({ icon, title }: { icon: React.ReactNode; title: string }) {
  return <div className="workspace-empty">{icon}<h1>{title}</h1></div>;
}
