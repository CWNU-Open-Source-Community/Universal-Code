import { lazy, useEffect, useMemo, useRef, useState, type RefObject } from "react";
import { QueryClient, QueryClientProvider, useInfiniteQuery, useMutation, useQueryClient } from "@tanstack/react-query";
import { Folder, SlidersHorizontal } from "lucide-react";
import type { APIClient } from "../api/client";
import type { ProviderDefinitionView, TaskBudgetSettings, ThreadDetailView, ThreadSearchResultView, ThreadView, WorkspaceView } from "../api/types";
import { normalizedTaskBudget } from "../api/task-configuration";
import { useConnectionStore } from "../state/connection";
import { V2Composer, v2ComposerNotSubmitted } from "./components/composer";
import { v2FileReferenceKey, type V2FileReference } from "./components/file-context";
import { V2Conversation } from "./components/conversation";
import { V2ConfirmDialog } from "./components/dialog";
import { V2LazySurface } from "./components/lazy-surface";
import { V2NetworkScopeControl, type V2NetworkMode } from "./components/network-scope-control";
import { V2SettingsSidebar, V2Sidebar, type V2SettingsSection } from "./components/sidebar";
import { V2Titlebar } from "./components/titlebar";
import { useV2Client } from "./client-session";
import { v2QueryKeys } from "./query-keys";
import { registerV2RecoveredTurn, useV2RestoreTurns, useV2ThreadTurn, v2TurnFailed } from "./use-thread-turn";
import { V2WorkspaceStart } from "./components/workspace-start";
import { useV2Navigation, type V2RunPane } from "./navigation";
import { readDensity } from "../lib/ui-density";
import { useLocale } from "../lib/locale";
import { V2RecoveryProvider, useV2PersistentState, useV2PersistenceWarning, useV2RecoveryStore } from "./recovery-storage";
import { useV2Drafts, validRecoveryTurn } from "./recovery-session";
import { readV2Route } from "./navigation";
import { useV2CreationRecovery } from "./recovery-creation";
import type { WorkspaceImageAttachment } from "../api/image-attachments";
import { mcpReferenceIdentities, type MCPDraftReference } from "../api/mcp-references";
import { v2MCPReferenceKey } from "./components/mcp-reference-picker";
import { fileAttachmentIdentities, type WorkspaceFileAttachment } from "../api/file-attachments";
import { v2AttachmentImportKey, v2AttachmentReferenceKey } from "./attachment-keys";
import { v2ImageReferenceKey } from "./components/image-input";
import { assertV2DraftVersion, getV2DraftDocument, readV2Draft, requireV2DraftVersion, useV2DraftDocument, v2DraftScope } from "./draft-context";
import type { V2DraftVersion } from "./draft-version";
import type { TaskReviewToolMemory } from "./components/task-review-tools";
import { V2DraftConflict } from "./components/draft-conflict";
import { FileEditDraftProvider } from "../components/file-edit-drafts";
import { V2PhasePicker, type V2WorkPhase } from "./components/phase-picker";
import { useV2DraftTaskConfiguration } from "./draft-task-configuration";
import { submitSkillChoice, type ProjectSkillChoice } from "../api/project-skills";
import { useV2WebAccessDefaults } from "./web-access-defaults";
import { useProjectlessWorkspace } from "./projectless-workspace";
import { threadProjectID } from "./components/thread-project-dialog";

type CreationAttempt = Map<string, string>;
type NewThreadOptions = { networkMode: V2NetworkMode; allowedTargets: string[];
  modelRoute: { provider: string; model: string } | null };
import "./styles.css";
import "./shared-shell.css";

const V2Settings = lazy(() => import("./components/settings").then((module) => ({
  default: module.V2Settings,
})));
const V2InspectorTools = lazy(() => import("./components/inspector-tools").then((module) => ({
  default: module.V2InspectorTools,
})));
const V2InspectorHome = lazy(() => import("./components/inspector-home").then((module) => ({
  default: module.V2InspectorHome,
})));

function NewConversation({ client, workspaces, workspaceID, projectID, workspaceNotice, onWorkspaceChange, onCreated,
  onTurnSuccess, onManageModels, onOpenTaskConfiguration, phase, onPhaseChange, budget, configurationValid, webAccessValid, projectSkills,
  draft: legacyDraft, onDraftChange: legacyDraftChange, creationAttemptRef, options, onOptionsChange, moreProjects, onImported }: {
  client: APIClient;
  workspaces: WorkspaceView[];
  workspaceID: string;
  projectID?: string;
  workspaceNotice?: React.ReactNode;
  onWorkspaceChange: (workspaceID: string) => void;
  onImported: (workspace: WorkspaceView, currentDraft?: string) => void;
  onCreated: (thread: ThreadView, submittedDraft: string, files: V2FileReference[], images?: WorkspaceImageAttachment[], version?: V2DraftVersion, attachments?: WorkspaceFileAttachment[], shouldOpen?: boolean, mcpReferences?: MCPDraftReference[]) => void;
  onTurnSuccess: (threadID: string, submittedDraft: string) => void;
  onManageModels: (prepareForDraft?: boolean) => void;
  onOpenTaskConfiguration: () => void;
  phase: V2WorkPhase;
  onPhaseChange: (phase: V2WorkPhase) => void;
  budget?: TaskBudgetSettings;
  configurationValid: boolean;
  webAccessValid: boolean;
  projectSkills?: ProjectSkillChoice;
  draft: string;
  onDraftChange: (content: string, expected?: string) => void;
  creationAttemptRef: RefObject<CreationAttempt>;
  options: NewThreadOptions;
  onOptionsChange: (options: NewThreadOptions) => void;
  moreProjects?: React.ReactNode;
}) {
  const { t } = useLocale();
  const queryClient = useQueryClient();
  const turn = useV2ThreadTurn(client);
  const recovery = useV2RecoveryStore();
  const activeRef = useRef(true);
  const workspaceRef = useRef(workspaceID);
  const creationRequestRef = useRef(0);
  if (workspaceRef.current !== workspaceID) {
    workspaceRef.current = workspaceID;
    creationRequestRef.current += 1;
  }
  useEffect(() => {
    activeRef.current = true;
    return () => { activeRef.current = false; };
  }, []);
  const managedDraft = useV2DraftDocument(workspaceID);
  const draft = managedDraft?.state.snapshot.text ?? legacyDraft;
  const onDraftChange = managedDraft?.changeText ?? legacyDraftChange;
  const creationRecovery = useV2CreationRecovery(client, workspaceID, (thread, originalDraft, files, input) => {
    registerV2RecoveredTurn(queryClient, input);
    onCreated(thread, originalDraft, files, input.images, input.draftVersion, input.attachments, true, input.mcpReferences);
  });
  const { networkMode, allowedTargets, modelRoute } = options;
  const [modelCatalogError, setModelCatalogError] = useState("");
  const create = async (content: string, files: V2FileReference[] = [], images: WorkspaceImageAttachment[] = [], draftVersion?: V2DraftVersion, attachments: WorkspaceFileAttachment[] = [], mcpReferences: MCPDraftReference[] = []) => {
    if (!webAccessValid) throw new Error(t("请重新选择网页访问方式，草稿已保留。", "Choose web access again. Your draft has been kept."));
    if (!configurationValid) throw new Error(t("任务配置尚未通过核对，请打开任务配置修正预算或重新读取项目设置。草稿已保留。", "Task configuration needs review. Open task configuration to fix the budget or reload project settings. Your draft has been kept."));
    if (projectSkills?.refs.length && !projectSkills.confirmUntrustedContext) throw new Error(t("请在任务配置中确认所选技能上下文。草稿已保留。", "Confirm the selected skill context in task configuration. Your draft has been kept."));
    normalizedTaskBudget(budget);
    if (phase === "plan" && !client.hasPlanDelivery) throw new Error(t("当前连接未启用计划确认。请检查连接，或选择直接执行。", "This connection does not support plan confirmation. Check the connection or choose direct execution."));
    const submittedDraft = draft;
    setModelCatalogError("");
    const creationWorkspaceID = workspaceID;
    const creationRequest = ++creationRequestRef.current;
    // Leaving this page, changing projects, or starting another request ends
    // this request's right to navigate, but not its original input handoff.
    const requestIsCurrent = () => activeRef.current &&
      workspaceRef.current === creationWorkspaceID && creationRequestRef.current === creationRequest;
    try {
      const catalog = await client.availableModelRoutes();
      if (!requestIsCurrent()) return v2ComposerNotSubmitted;
      const selectable = catalog.routes.filter((route) => route.selectable);
      const selectedReady = !modelRoute || selectable.some((route) =>
        route.provider_id === modelRoute.provider && route.model === modelRoute.model);
      if (!selectable.length || !selectedReady) {
        onManageModels(true);
        return v2ComposerNotSubmitted;
      }
    } catch (error) {
      if (!requestIsCurrent()) return v2ComposerNotSubmitted;
      setModelCatalogError(`${t("无法检查可用模型，草稿已保留。", "Could not check available models. Your draft has been kept.")}${error instanceof Error && error.message
        ? ` ${error.message}` : t(" 请检查连接后重试。", " Check the connection and try again.")}`);
      return v2ComposerNotSubmitted;
    }
    let goal = content || "文件对话";
    if (!content && images.length) goal = "图片对话";
    else if (!content && mcpReferences.length) goal = "MCP 内容";
    const request = {
      version: "thread_creation.v1",
      workspace_id: workspaceID,
      goal,
      profile: "code",
      surface: "code",
      phase,
      network_mode: "disabled",
      web_access_mode: networkMode,
      web_allowed_targets: networkMode === "rules" ? allowedTargets : [],
      ...(budget ? { budget } : {}),
      ...(modelRoute ? { provider: modelRoute.provider, model: modelRoute.model } : {}),
    } as Parameters<APIClient["createThread"]>[0] & { provider?: string; model?: string };
    try {
      if (recovery) {
        if (managedDraft) {
          if (draftVersion) assertV2DraftVersion(managedDraft, draftVersion, { text: submittedDraft, files, images, attachments, mcpReferences });
          else draftVersion = requireV2DraftVersion(managedDraft, { text: submittedDraft, files, images, attachments, mcpReferences });
        }
        const intent = creationRecovery.prepare(request, content, files, submittedDraft, images, draftVersion, attachments, projectSkills, mcpReferences);
        const thread = await creationRecovery.resolve(intent);
        const input = creationRecovery.handoff(intent, thread);
        const submission = turn.mutateAsync(input);
        onCreated(thread, submittedDraft, files, images, input.draftVersion, attachments, requestIsCurrent(), mcpReferences);
        void queryClient.invalidateQueries({ queryKey: v2QueryKeys.threads("active") });
        try { await submission; }
        catch (error) { if (v2TurnFailed(error) && !input.draftVersion) onTurnSuccess(thread.id, submittedDraft); throw error; }
        if (!input.draftVersion) onTurnSuccess(thread.id, submittedDraft);
      } else {
        const fingerprint = JSON.stringify([request, files, images, fileAttachmentIdentities(attachments), projectSkills, mcpReferenceIdentities(mcpReferences)]);
        const operationID = creationAttemptRef.current.get(fingerprint) ?? globalThis.crypto.randomUUID();
        creationAttemptRef.current.set(fingerprint, operationID);
        const result = await client.createThread(request, `v2-thread-create-${operationID}`);
        if (projectSkills?.refs.length) {
          try { await submitSkillChoice(client, result.thread.last_run_id, projectSkills, operationID); }
          catch (error) {
            void queryClient.invalidateQueries({ queryKey: v2QueryKeys.threads("active") });
            throw new Error(`${t(`对话 ${result.thread.id} 已保存，技能选择失败，尚未发送首条消息。`, `Conversation ${result.thread.id} was saved, but skill selection failed and the first message has not been sent.`)}${error instanceof Error ? error.message : t("请重试。", "Please try again.")}`);
          }
        }
        const submission = turn.mutateAsync({ threadID: result.thread.id, workspaceID, content,
          ...(files.length ? { files } : {}),
          ...(images.length ? { images } : {}),
          ...(attachments.length ? { attachments } : {}),
          ...(mcpReferences.length ? { mcpReferences } : {}),
          operationKey: `v2-thread-create-turn-${operationID}`, createdAt: new Date().toISOString() });
        onCreated(result.thread, submittedDraft, files, images, undefined, attachments, requestIsCurrent(), mcpReferences);
        creationAttemptRef.current.delete(fingerprint);
        void queryClient.invalidateQueries({ queryKey: v2QueryKeys.threads("active") });
        try { await submission; }
        catch (error) { if (v2TurnFailed(error)) onTurnSuccess(result.thread.id, submittedDraft); throw error; }
        onTurnSuccess(result.thread.id, submittedDraft);
      }
    } catch (error) {
      // The original key/journal and Thread mutation keep this failure. It
      // must not replace a newer request's error after returning to a project.
      if (requestIsCurrent()) throw error;
      return v2ComposerNotSubmitted;
    }
    if (!requestIsCurrent()) return v2ComposerNotSubmitted;
  };
  return <section className="v2-new-conversation">
    <header><Folder aria-hidden="true" size={17} /><strong>{t("新对话", "New conversation")}</strong>
      <V2WorkspaceStart client={client} onSelect={(workspace) => onImported(workspace, draft)} />{moreProjects}</header>
    <div className="v2-new-conversation-body"><div className="v2-new-copy">
      <span className="v2-orb" aria-hidden="true"><i /><i /></span>
      <h1>{t("接下来要做什么？", "What would you like to do next?")}</h1><p>{workspaceID || client.hasProjectlessWorkspaces ? t("描述目标，随时查看进展、补充消息或停止。", "Describe your goal. Check progress, add messages, or stop at any time.")
        : t("先接入项目。你也可以先写下需求，选择项目后继续。", "Connect a project to get started. You can write your request first and continue after selecting a project.")}</p>
    </div></div>
    {workspaceNotice}
    {creationRecovery.notice}
    {modelCatalogError && <div className="v2-notice" role="alert">{modelCatalogError}</div>}
    {!webAccessValid && <div className="v2-notice" role="alert">{t("保存的网页访问设置需要更新，请在输入框下方重新选择。", "Saved web access settings need updating. Choose again below the input.")}</div>}
    {!configurationValid && <div className="v2-notice" role="alert">{t("任务配置需要修正或重新核对，草稿已保留。", "Task configuration needs correction or review. Your draft has been kept.")}
      <button onClick={onOpenTaskConfiguration} type="button">{t("修正任务配置", "Fix task configuration")}</button></div>}
    <div className="v2-composer-dock">
      {managedDraft && <V2DraftConflict key={workspaceID} client={client} workspaceID={workspaceID} state={managedDraft.state}
        onResolve={(token, ref) => { managedDraft.document.resolve(managedDraft.scope, token, ref); }} />}
      <V2Composer client={client}
      draft={draft} onDraftChange={onDraftChange}
      disabled={!client.hasThreadControl} submitDisabled={!configurationValid || !webAccessValid} onSubmit={create}
      onManageModels={onManageModels} onPendingModelRouteChange={(route) =>
        onOptionsChange({ ...options, modelRoute: route })}
      pendingModelRoute={modelRoute}
      newThreadControls={<><button aria-label={t("任务预算与项目配置", "Task budget and project configuration")} className="v2-composer-chip v2-composer-task-config"
        title={t("任务预算与项目配置", "Task budget and project configuration")}
        onClick={onOpenTaskConfiguration} type="button"><SlidersHorizontal aria-hidden="true" size={14} /><span className="v2-composer-optional-label">{t("任务配置", "Task configuration")}{budget ? t(" · 自定义", " · Custom") : ""}</span></button>
      <V2PhasePicker phase={phase} onChange={onPhaseChange}
        disabled={!client.hasThreadControl} planAvailable={client.hasPlanDelivery} />
      <V2NetworkScopeControl disabled={!client.hasThreadControl}
        mode={networkMode} onChange={(nextMode, nextTargets) => {
          onOptionsChange({ ...options, networkMode: nextMode, allowedTargets: nextTargets });
        }} targets={allowedTargets} /></>}
      onWorkspaceChange={onWorkspaceChange} placeholder={t("输入任务或问题…", "Enter a task or question…")} threadID=""
      workspaceID={workspaceID} projectID={projectID} allowProjectless={client.hasProjectlessWorkspaces} workspaces={workspaces} />
      </div>
  </section>;
}

export function V2Workbench({ client }: { client: APIClient }) {
  const scopeID = useConnectionStore((state) => state.health?.data_store_id);
  const parentQueries = useQueryClient();
  const queries = useMemo(() => scopeID ? new QueryClient({ defaultOptions: parentQueries.getDefaultOptions() })
    : parentQueries, [scopeID, client.baseURL, parentQueries]);
  return <QueryClientProvider client={queries}><V2RecoveryProvider key={JSON.stringify([scopeID, client.baseURL])} client={client} scopeID={scopeID}>
    <FileEditDraftProvider><V2WorkbenchContent client={client} /></FileEditDraftProvider>
  </V2RecoveryProvider></QueryClientProvider>;
}

function V2WorkbenchContent({ client }: { client: APIClient }) {
  const { t } = useLocale();
  useV2RestoreTurns();
  const recovery = useV2RecoveryStore();
  const persistenceWarning = useV2PersistenceWarning();
  useEffect(() => {
    document.documentElement.dataset.prayuDensity = readDensity();
  }, []);
  const queryClient = useQueryClient();
  const previousThreadID = useConnectionStore((state) => state.selectedThreadID);
  const selectThread = useConnectionStore((state) => state.selectThread);
  const navigation = useV2Navigation();
  const { route, navigate } = navigation;
  const routeRef = useRef(route);
  routeRef.current = route;
  const selectedThreadID = route.threadID ?? "";
  const reviewMemoryRef = useRef<TaskReviewToolMemory>({ generation: 0 });
  const [reviewEntry, setReviewEntry] = useState<{ threadID: string; runID: string; requestID: string }>();
  useEffect(() => {
    reviewMemoryRef.current = { generation: reviewMemoryRef.current.generation + 1 };
    setReviewEntry(undefined);
  }, [client, selectedThreadID]);
  useEffect(() => {
    if (route.tool === "run" && reviewMemoryRef.current.runID && reviewMemoryRef.current.runID !== route.resourceID)
      reviewMemoryRef.current = { generation: reviewMemoryRef.current.generation + 1 };
  }, [route.tool, route.resourceID]);
  const surface = route.section ? "settings" : "conversation";
  const view = route.view ?? "conversation";
  const settingsSection = route.section ?? "general";
  const newConversation = route.kind !== "thread";
  const [narrowLayout, setNarrowLayout] = useState(() => Boolean(window.matchMedia?.("(max-width: 720px)").matches));
  const [desktopSidebarVisible, setDesktopSidebarVisible] = useState(true);
  const [narrowSidebarVisible, setNarrowSidebarVisible] = useState(false);
  const sidebarVisible = narrowLayout ? narrowSidebarVisible : desktopSidebarVisible;
  const sidebarSlot = useRef<HTMLDivElement>(null);
  const sidebarToggle = useRef<HTMLButtonElement>(null);
  const setSidebarVisible = (visible: boolean) => {
    if (!visible) sidebarToggle.current?.focus();
    if (narrowLayout) setNarrowSidebarVisible(visible);
    else setDesktopSidebarVisible(visible);
  };
  useEffect(() => {
    const media = window.matchMedia?.("(max-width: 720px)");
    if (!media) return;
    const onChange = (event: MediaQueryListEvent) => {
      if (sidebarSlot.current?.contains(document.activeElement)) sidebarToggle.current?.focus();
      setNarrowLayout(event.matches);
      setNarrowSidebarVisible(false);
    };
    if (typeof media.addEventListener === "function") {
      media.addEventListener("change", onChange);
      return () => media.removeEventListener("change", onChange);
    }
    media.addListener?.(onChange);
    return () => media.removeListener?.(onChange);
  }, []);
  const [searchOpen, setSearchOpen] = useState(false);
  const [threadSearch, setThreadSearch] = useState("");
  const [appliedThreadSearch, setAppliedThreadSearch] = useState("");
  const [includeArchivedSearch, setIncludeArchivedSearch] = useState(false);
  const [searchComposing, setSearchComposing] = useState(false);
  const normalizedThreadSearch = threadSearch.trim();
  const searchTooLong = Array.from(normalizedThreadSearch).length > 256;
  const searchPending = searchComposing || normalizedThreadSearch !== appliedThreadSearch;
  useEffect(() => {
    if (searchComposing || searchTooLong) return;
    if (!normalizedThreadSearch) { setAppliedThreadSearch(""); return; }
    const timer = window.setTimeout(() => setAppliedThreadSearch(normalizedThreadSearch), 250);
    return () => window.clearTimeout(timer);
  }, [normalizedThreadSearch, searchComposing, searchTooLong]);
  const [storedWorkspace, setWorkspaceID] = useV2PersistentState<unknown>("workspace", "");
  const projectID = typeof storedWorkspace === "string" ? storedWorkspace : "";
  const localWorkspace = useProjectlessWorkspace(client, route.kind === "new" && !route.section && !route.tool && !projectID);
  const workspaceID = projectID || (client.hasProjectlessWorkspaces ? localWorkspace.workspaceID : "");
  const [savedPhase, setSavedPhase] = useV2PersistentState<unknown>(`new-thread-phase:${workspaceID}`, "deliver");
  const phase: V2WorkPhase = savedPhase === "plan" ? "plan" : "deliver";
  const draftTaskConfiguration = useV2DraftTaskConfiguration(workspaceID);
  // An explicitly imported existing project may be outside the first list page.
  // Retain its pathless receipt so selection survives list refetches.
  const [importedWorkspaces, setImportedWorkspaces] = useState<WorkspaceView[]>([]);
  const [backgroundCreations, setBackgroundCreations] = useState<ThreadView[]>([]);
  const [drafts, setDrafts] = useV2Drafts();
  useEffect(() => {
    if (projectID || !localWorkspace.workspaceID || !drafts["new:"]) return;
    const text = drafts["new:"];
    if (recovery) {
      const document = getV2DraftDocument(recovery);
      const scope = v2DraftScope(localWorkspace.workspaceID);
      const target = readV2Draft(document, recovery, scope);
      if (target.ref || target.snapshot.text || target.error || target.conflict) return;
      const copied = document.update(scope, { text }, null);
      if (!copied.persisted || copied.error) return;
    }
    setDrafts((current) => ({ ...current,
      [`new:${localWorkspace.workspaceID}`]: current[`new:${localWorkspace.workspaceID}`] ?? text,
      "new:": current["new:"] === text ? "" : current["new:"] }));
  }, [projectID, localWorkspace.workspaceID, drafts, recovery, setDrafts]);
  const creationAttemptRef = useRef<CreationAttempt>(new Map());
  const webAccessDefaults = useV2WebAccessDefaults();
  const [newThreadModelRoute, setNewThreadModelRoute] = useState<NewThreadOptions["modelRoute"]>(null);
  const newThreadOptions: NewThreadOptions = { networkMode: webAccessDefaults.mode,
    allowedTargets: webAccessDefaults.targets, modelRoute: newThreadModelRoute };
  const setNewThreadOptions = (next: NewThreadOptions) => {
    if (next.networkMode !== newThreadOptions.networkMode ||
      next.allowedTargets !== newThreadOptions.allowedTargets) {
      webAccessDefaults.onChange(next.networkMode, next.allowedTargets);
    }
    setNewThreadModelRoute(next.modelRoute);
  };
  const [modelSetupToken, setModelSetupToken] = useState("");
  const modelSetupContextRef = useRef<{ token: string; workspaceID: string; entered: boolean } | null>(null);
  const updateModelSetupToken = (value: string, setupWorkspaceID = "") => {
    modelSetupContextRef.current = value ? { token: value, workspaceID: setupWorkspaceID, entered: false } : null;
    setModelSetupToken(value);
  };
  const draftKey = newConversation || !selectedThreadID ? `new:${workspaceID}` : `thread:${selectedThreadID}`;
  const updateDraft = (content: string, expected?: string) => setDrafts((current) =>
    expected !== undefined && (current[draftKey] ?? "") !== expected ? current
      : { ...current, [draftKey]: content });
  const selectWorkspace = (id: string) => {
    if (!workspaceID) setDrafts((current) => ({ ...current,
      [`new:${id}`]: current[`new:${id}`] ?? current["new:"] ?? "" }));
    setWorkspaceID(id);
  };
  const [archiveCandidate, setArchiveCandidate] = useState<ThreadView | null>(null);
  const workspacesQuery = useInfiniteQuery({
    queryKey: v2QueryKeys.workspaces,
    queryFn: ({ signal, pageParam }) => client.getPage<WorkspaceView>("/workspaces", { limit: 100 }, pageParam, signal),
    initialPageParam: "", getNextPageParam: (last) => last.page.next_cursor || undefined,
    staleTime: 30_000,
  });
  const threadsQuery = useInfiniteQuery({
    queryKey: v2QueryKeys.threadSearch("active", ""),
    queryFn: ({ signal, pageParam }) => client.getPage<ThreadView>("/threads",
      { limit: 100, status: "active" }, pageParam, signal),
    initialPageParam: "", getNextPageParam: (last) => last.page.next_cursor || undefined,
    enabled: !appliedThreadSearch && !searchPending && !searchTooLong,
    // Infinite queries refetch every cached page. Poll only a single page;
    // history still refreshes explicitly and through the existing invalidations.
    refetchInterval: (query) => !appliedThreadSearch && sidebarVisible && surface === "conversation" && !searchPending &&
      (query.state.data?.pages.length ?? 1) === 1 ? 15_000 : false,
  });
  const messageSearchQuery = useInfiniteQuery({
    queryKey: ["v2", "threads", "active", "messages", { q: appliedThreadSearch, include_archived: includeArchivedSearch }],
    queryFn: ({ signal, pageParam }) => client.getPage<ThreadSearchResultView>("/threads/search",
      { q: appliedThreadSearch, include_archived: includeArchivedSearch, limit: 50 }, pageParam, signal),
    initialPageParam: "", getNextPageParam: (last) => last.page.next_cursor || undefined,
    enabled: Boolean(appliedThreadSearch) && !searchPending && !searchTooLong,
    refetchInterval: (query) => sidebarVisible && surface === "conversation" && !searchPending &&
      (query.state.data?.pages.length ?? 1) === 1 ? 15_000 : false,
  });
  const sidebarQuery = appliedThreadSearch ? messageSearchQuery : threadsQuery;
  const searchResults = useMemo(() => [...new Map(messageSearchQuery.data?.pages.flatMap(({ items }) => items)
    .map((item) => [item.id, item])).values()], [messageSearchQuery.data]);
  const workspaces = useMemo(() => [...new Map([...importedWorkspaces,
    ...(workspacesQuery.data?.pages.flatMap(({ items }) => items) ?? [])]
    .map((workspace) => [workspace.id, workspace])).values()], [workspacesQuery.data, importedWorkspaces]);
  const threads = useMemo(() => [...new Map(threadsQuery.data?.pages.flatMap(({ items }) =>
    items).map((thread) => [thread.id, thread])).values()], [threadsQuery.data]);
  useEffect(() => {
    if (!client.hasProjectlessWorkspaces && !workspaceID && workspaces[0]) {
      const id = workspaces[0].id;
      setDrafts((current) => ({ ...current, [`new:${id}`]: current[`new:${id}`] ?? current["new:"] ?? "" }));
      setWorkspaceID(id);
    }
  }, [client.hasProjectlessWorkspaces, workspaceID, workspaces]);
  useEffect(() => {
    if (route.kind !== "initial" || !threadsQuery.isSuccess) return;
    const saved = recovery?.read<unknown>("route", "");
    if (typeof saved === "string" && saved) {
      const restored = readV2Route(saved);
      if (restored.kind === "thread" || restored.kind === "new") { navigate(restored, true); return; }
    }
    const id = previousThreadID || threads[0]?.id;
    navigate(id ? { kind: "thread", threadID: id } : { kind: "new" }, true);
  }, [route.kind, previousThreadID, threads, threadsQuery.isSuccess, navigate, recovery]);
  useEffect(() => {
    const setup = modelSetupContextRef.current;
    if (!setup) return;
    if (route.kind === "new" && route.section === "models" && workspaceID === setup.workspaceID) {
      setup.entered = true;
      return;
    }
    if (setup.entered) updateModelSetupToken("");
  }, [route, workspaceID]);
  useEffect(() => {
    if (route.kind === "initial" || route.kind === "invalid") return;
    try { recovery?.write("route", window.location.hash); } catch { /* Visible save notice. */ }
  }, [route, recovery]);
  useEffect(() => {
    if (route.kind !== "initial") selectThread(selectedThreadID);
  }, [route.kind, selectedThreadID, selectThread]);
  const archiveMutation = useMutation({
    mutationFn: (thread: ThreadView) => client.transitionThread(thread.id, "archive", {
      version: "thread_lifecycle.v1", expected_version: thread.version,
    }, `v2-thread-archive-${globalThis.crypto.randomUUID()}`),
    onSuccess: (result) => {
      const fallback = threads.find(({ id }) => id !== result.thread.id);
      if (selectedThreadID === result.thread.id) {
        navigate(fallback ? { kind: "thread", threadID: fallback.id } : { kind: "new" }, true);
      }
      setArchiveCandidate(null);
      void queryClient.invalidateQueries({ queryKey: v2QueryKeys.threads("active") });
      void queryClient.invalidateQueries({ queryKey: v2QueryKeys.threads("archived") });
    },
  });
  const projectMoveKeys = useRef(new Map<string, string>());
  const moveMutation = useMutation({
    mutationFn: async ({ thread, destination }: { thread: ThreadView; destination: string }) => {
      const changingWorkspace = Boolean(destination && destination !== thread.workspace_id);
      const sourceScope = v2DraftScope(thread.workspace_id ?? "", thread.id);
      const targetScope = v2DraftScope(destination || sourceScope.workspaceID, thread.id);
      const document = recovery ? getV2DraftDocument(recovery) : null;
      const source = document && recovery ? readV2Draft(document, recovery, sourceScope) : null;
      const target = document ? document.read(targetScope) : null;
      if (recovery) {
        recovery.assertReadable("turn:");
        if (recovery.entries<unknown>("turn:").some(([, input]) => validRecoveryTurn(input) && input.threadID === thread.id))
          throw new Error(t("消息还在确认中，请等待发送结果后移动。", "Wait for the pending message to be confirmed before moving."));
      }
      if (changingWorkspace) {
        if (queryClient.getQueryData<{ pending: () => boolean }>(v2AttachmentImportKey(sourceScope.workspaceID, thread.id))?.pending() ||
          recovery?.entries(`file-upload:${JSON.stringify([sourceScope.workspaceID, thread.id])}:`).length ||
          recovery?.entries(`native-clipboard:${JSON.stringify([sourceScope.workspaceID, thread.id])}:`).length)
          throw new Error(t("此聊天的附件还在保存或待核对，请完成后再移动。", "Finish saving or checking this chat's attachments before moving."));
        if (source?.error || source?.conflict || target?.error || target?.conflict)
          throw new Error(t("请先处理此对话的草稿冲突，再移动项目。", "Resolve this conversation's draft conflict before moving."));
        const references = source ? [source.snapshot.files, source.snapshot.images,
          source.snapshot.attachments ?? [], source.snapshot.mcpReferences ?? []] : [
          queryClient.getQueryData<unknown[]>(v2FileReferenceKey(sourceScope.workspaceID, thread.id)) ?? [],
          queryClient.getQueryData<unknown[]>(v2ImageReferenceKey(sourceScope.workspaceID, thread.id)) ?? [],
          queryClient.getQueryData<unknown[]>(v2AttachmentReferenceKey(sourceScope.workspaceID, thread.id)) ?? [],
          queryClient.getQueryData<unknown[]>(v2MCPReferenceKey(sourceScope.workspaceID, thread.id)) ?? [],
        ];
        if (references.some((items) => items.length))
          throw new Error(t("草稿中还有原项目的附件或引用，请先发送或移除后移动。", "Send or remove the draft's attachments and references before moving."));
        if (target && (target.snapshot.text || target.snapshot.files.length || target.snapshot.images.length ||
          target.snapshot.attachments?.length || target.snapshot.mcpReferences?.length))
          throw new Error(t("此对话在目标目录还有未发送草稿，请先处理后再移动。", "This chat has an unsent draft in the destination folder. Resolve it before moving."));
      }
      const fingerprint = JSON.stringify([thread.id, destination, thread.project_assignment_version]);
      const key = projectMoveKeys.current.get(fingerprint) ?? `v2-project-move-${crypto.randomUUID()}`;
      projectMoveKeys.current.set(fingerprint, key);
      const result = await client.moveThreadToProject(thread.id, destination, thread.project_assignment_version!, key);
      if (changingWorkspace && document && recovery) {
        const latest = readV2Draft(document, recovery, sourceScope);
        if (!latest.error) {
          const copied = document.update(targetScope, { text: latest.snapshot.text }, target?.ref ?? null);
          if (copied.persisted && !copied.error && !copied.conflict && !latest.conflict && latest.ref)
            document.update(sourceScope, { text: "" }, latest.ref);
        }
      }
      projectMoveKeys.current.delete(fingerprint);
      return result;
    },
    onSuccess: ({ thread }) => {
      queryClient.setQueryData<ThreadDetailView>(v2QueryKeys.thread(thread.id), (current) => current ? { ...current, thread } : current);
      void queryClient.invalidateQueries({ queryKey: v2QueryKeys.thread(thread.id) });
      void queryClient.invalidateQueries({ queryKey: v2QueryKeys.threads("active") });
    },
    onError: () => { void queryClient.invalidateQueries({ queryKey: v2QueryKeys.threads("active") }); },
  });
  const closeNavigationSidebar = () => {
    if (window.matchMedia?.("(max-width: 720px)").matches) setSidebarVisible(false);
  };
  const openConversation = (threadID: string) => {
    setBackgroundCreations((current) => current.filter((thread) => thread.id !== threadID));
    updateModelSetupToken("");
    navigate({ kind: "thread", threadID, ...(route.view ? { view: route.view } : {}) });
    closeNavigationSidebar();
  };
  const startNew = (destination?: string) => {
    updateModelSetupToken("");
    const currentThread = queryClient.getQueryData<ThreadDetailView>(
      v2QueryKeys.thread(selectedThreadID))?.thread ?? threads.find(({ id }) => id === selectedThreadID);
    selectWorkspace(destination ?? (client.hasProjectlessWorkspaces ? "" : currentThread ? threadProjectID(currentThread) : projectID));
    navigate({ kind: "new" });
    closeNavigationSidebar();
  };
  const setSettingsSection = (section: V2SettingsSection) => {
    if (section !== "models" && section !== "advanced-models") updateModelSetupToken("");
    navigate({ ...route, kind: selectedThreadID ? "thread" : "new",
      ...(selectedThreadID ? { threadID: selectedThreadID } : {}), section }, Boolean(route.section));
    closeNavigationSidebar();
  };
  const goBack = () => { updateModelSetupToken(""); navigation.back(); closeNavigationSidebar(); };
  const returnFromSettings = () => {
    updateModelSetupToken("");
    navigate({ ...route, section: undefined }, true);
    closeNavigationSidebar();
  };
  const changeView = (next: "conversation" | "inspector") => {
    updateModelSetupToken("");
    navigate({ kind: selectedThreadID ? "thread" : "new",
      ...(selectedThreadID ? { threadID: selectedThreadID } : {}),
      ...(next === "inspector" ? { view: "inspector" } : {}) });
    closeNavigationSidebar();
  };
  const openSettings = () => setSettingsSection("general");
  const openModels = (prepareForDraft = false) => {
    updateModelSetupToken(prepareForDraft && newConversation ? globalThis.crypto.randomUUID() : "",
      workspaceID);
    setSettingsSection("models");
  };
  const completeModelSetup = (token: string, definition: Pick<ProviderDefinitionView, "id" | "default_model">) => {
    const setup = modelSetupContextRef.current;
    const currentRoute = routeRef.current;
    if (!token || token !== setup?.token || !setup.entered || workspaceID !== setup.workspaceID ||
      currentRoute.section !== "models" || currentRoute.kind !== "new") return;
    setNewThreadModelRoute({ provider: definition.id, model: definition.default_model });
    updateModelSetupToken("");
    navigate({ ...currentRoute, section: undefined }, true);
    requestAnimationFrame(() => requestAnimationFrame(() =>
      document.querySelector<HTMLElement>('.v2-new-conversation .v2-composer :is(textarea, [contenteditable="true"][role="textbox"])')?.focus()));
  };
  const openInspector = () => changeView("inspector");
  const openTool = (tool: "run" | "session" | "schedule", resourceID?: string, pane?: V2RunPane) => {
    if (tool === "run" && reviewMemoryRef.current.runID !== resourceID)
      reviewMemoryRef.current = { generation: reviewMemoryRef.current.generation + 1 };
    navigate({ kind: selectedThreadID ? "thread" : "new", ...(selectedThreadID ? { threadID: selectedThreadID } : {}),
      view: "inspector", tool, ...(resourceID ? { resourceID } : {}), ...(tool === "run" && pane ? { pane } : {}),
    });
  };
  const openCreatedRun = (runID: string) => {
    // The mutation retains the callback from its source route. A later source
    // selection must not be replaced by the completion of that older request.
    if (routeRef.current !== route) return;
    // Fork/Resume returns a Run, not a Thread association. Open that exact
    // record without carrying the old Thread's permissions into its scope.
    navigate({ kind: "new", view: "inspector", tool: "run", resourceID: runID });
  };
  const openGithubReview = (runID: string) => {
    if (!selectedThreadID || routeRef.current !== route) return;
    setReviewEntry({ threadID: selectedThreadID, runID, requestID: globalThis.crypto.randomUUID() });
    navigate({ kind: "thread", threadID: selectedThreadID });
    closeNavigationSidebar();
  };

  return <div className={`v2-shell${sidebarVisible ? " has-sidebar" : " no-sidebar"}`}>
    <V2Titlebar canGoBack={navigation.canGoBack} onBack={surface === "settings" ? returnFromSettings : goBack}
      onToggleSidebar={() => setSidebarVisible(!sidebarVisible)} sidebarVisible={sidebarVisible} sidebarToggleRef={sidebarToggle} />
    <button aria-label={t("关闭侧栏", "Close sidebar")} className="v2-sidebar-backdrop" aria-hidden={!sidebarVisible} inert={!sidebarVisible}
      onClick={() => setSidebarVisible(false)} type="button" />
    <div className="v2-shell-body">
      <div className="v2-sidebar-slot" ref={sidebarSlot} aria-hidden={!sidebarVisible} inert={!sidebarVisible}>
      {surface === "settings" ? <V2SettingsSidebar onBack={returnFromSettings}
        onSelect={setSettingsSection} section={settingsSection} /> : <V2Sidebar
          onArchive={setArchiveCandidate} onNewConversation={() => startNew()} onOpenModels={() => openModels(newConversation)}
          onNewInProject={(id) => startNew(id)}
          onMoveThread={client.hasThreadControl ? (thread, destination) => moveMutation.mutate({ thread, destination }) : undefined}
          movingThreadID={moveMutation.isPending ? moveMutation.variables.thread.id : undefined}
          moveError={moveMutation.isError ? moveMutation.error.message : undefined}
          projectAction={<V2WorkspaceStart client={client} variant="sidebar" onSelect={(workspace) => {
            setImportedWorkspaces((current) => [...current.filter(({ id }) => id !== workspace.id), workspace]);
            startNew(workspace.id);
          }} />}
          onOpenSettings={openSettings}
          onOpenConnections={() => setSettingsSection("connections")}
          onOpenInspector={() => changeView(view === "inspector" ? "conversation" : "inspector")}
          inspectorActive={view === "inspector"}
          onSearchOpen={(open) => {
            setSearchOpen(open);
            if (!open) { setThreadSearch(""); setSearchComposing(false); }
          }} onSelectThread={openConversation} searchOpen={searchOpen}
          search={threadSearch} appliedSearch={appliedThreadSearch} searchPending={searchPending}
          searchTooLong={searchTooLong}
          searchResults={searchResults} includeArchived={includeArchivedSearch} onIncludeArchivedChange={setIncludeArchivedSearch}
          onSelectSearchResult={(result) => {
            navigate({ kind: "thread", threadID: result.thread_id, ...(result.message_id ? { messageID: result.message_id } : {}) });
            closeNavigationSidebar();
          }}
          onSearchChange={(value) => { setThreadSearch(value); if (!value) setSearchComposing(false); }}
          onSearchCompositionChange={setSearchComposing}
          hasMore={sidebarQuery.hasNextPage} loading={sidebarQuery.isLoading}
          truncated={sidebarQuery.data?.pages.at(-1)?.page.truncated === true}
          loadingMore={sidebarQuery.isFetchingNextPage} loadFailed={sidebarQuery.isError}
          loadMoreFailed={sidebarQuery.isFetchNextPageError}
          refreshing={sidebarQuery.isFetching && !sidebarQuery.isLoading && !sidebarQuery.isFetchingNextPage}
          onRetry={() => void (sidebarQuery.isFetchNextPageError ? sidebarQuery.fetchNextPage() : sidebarQuery.refetch())}
          onLoadMore={() => void sidebarQuery.fetchNextPage()} onRefresh={() => void sidebarQuery.refetch()}
          selectedThreadID={newConversation ? "" : selectedThreadID} threads={threads} workspaces={workspaces} />}
      </div>
      <div className="v2-product-surface" inert={moveMutation.isPending && moveMutation.variables.thread.id === selectedThreadID}
        aria-busy={moveMutation.isPending && moveMutation.variables.thread.id === selectedThreadID}>
        {route.kind === "invalid" ? <div className="v2-notice" role="alert">{t("任务地址无法识别。", "The task address is not recognized.")}
          <button onClick={() => startNew()} type="button">{t("打开新对话", "Open new conversation")}</button></div>
          : surface === "settings" ? <V2LazySurface resetKey="settings" loadingText={t("正在加载设置…", "Loading settings…")} errorLabel={t("设置", "Settings")}>
          <V2Settings client={client} onOpenInspector={openInspector}
          onSelectSection={setSettingsSection} onOpenThread={openConversation} onOpenTask={(workspaceID) => {
            if (!selectedThreadID && workspaceID) selectWorkspace(workspaceID);
            changeView("conversation");
          }} section={settingsSection}
          threadID={selectedThreadID} workspaces={workspaces}
          defaultWebAccessMode={webAccessDefaults.mode} defaultWebAccessTargets={webAccessDefaults.targets}
          onDefaultWebAccessChange={webAccessDefaults.onChange}
          sourceRunID={route.tool === "run" ? route.resourceID : reviewMemoryRef.current.client === client &&
            reviewMemoryRef.current.threadID === selectedThreadID ? reviewMemoryRef.current.runID : undefined}
          onOpenGithubReview={openGithubReview}
          draftWorkspaceID={workspaceID} draftPhase={phase} draftBudget={draftTaskConfiguration.budget}
          onDraftBudgetChange={draftTaskConfiguration.onBudgetChange}
          onDraftValidityChange={draftTaskConfiguration.onValidityChange}
          draftProjectSkills={draftTaskConfiguration.projectSkills} onDraftProjectSkillsChange={draftTaskConfiguration.onProjectSkillsChange}
          prepareModelForDraft={Boolean(modelSetupToken)} modelSetupToken={modelSetupToken}
          onModelReady={completeModelSetup} /></V2LazySurface> : route.tool
          ? <V2LazySurface resetKey="inspector-tools"
            loadingText={t("正在加载检查工具…", "Loading inspection tools…")} errorLabel={t("检查工具", "Inspection tools")}>
            <V2InspectorTools client={client} tool={route.tool} resourceID={route.resourceID}
            threadID={selectedThreadID} pane={route.pane} onBack={openInspector} onOpenSettings={setSettingsSection}
            onOpenRun={openCreatedRun} /></V2LazySurface>
          : view === "inspector" && !selectedThreadID
          ? <V2LazySurface resetKey="inspector-home" loadingText={t("正在加载 Inspector…", "Loading Inspector…")} errorLabel="Inspector">
            <V2InspectorHome client={client} onOpenTool={openTool} onOpenSettings={setSettingsSection} /></V2LazySurface>
          : newConversation || !selectedThreadID
          ? <NewConversation client={client} draft={drafts[draftKey] ?? ""} onDraftChange={updateDraft}
            projectID={projectID}
            workspaceNotice={!projectID && client.hasProjectlessWorkspaces && <>
              {localWorkspace.pending && <p className="v2-notice" role="status">{t("正在准备聊天工作目录…", "Preparing the chat folder…")}</p>}
              {localWorkspace.error && <div className="v2-notice" role="alert">{localWorkspace.error}
                <button onClick={localWorkspace.retry} type="button">{t("重试", "Retry")}</button></div>}
            </>}
            phase={phase} onPhaseChange={setSavedPhase}
            creationAttemptRef={creationAttemptRef}
            options={newThreadOptions} onOptionsChange={setNewThreadOptions}
            webAccessValid={webAccessDefaults.valid}
            budget={draftTaskConfiguration.budget} configurationValid={draftTaskConfiguration.valid} projectSkills={draftTaskConfiguration.projectSkills}
            onOpenTaskConfiguration={() => setSettingsSection("task-configuration")}
            onCreated={(thread, submittedDraft, files, images = [], version, attachments = [], shouldOpen = true, mcpReferences = []) => {
              const source = `new:${thread.workspace_id}`;
              const target = `thread:${thread.id}`;
              if (thread.project_workspace_id === "") {
                // Preserve edits made while the first message was being created.
                if (recovery && version) {
                  const document = getV2DraftDocument(recovery);
                  const original = readV2Draft(document, recovery, version.scope);
                  if (original.ref?.branchID !== version.ref.branchID || original.ref?.seq !== version.ref.seq) {
                    const targetScope = v2DraftScope(thread.workspace_id ?? "", thread.id);
                    const targetDraft = readV2Draft(document, recovery, targetScope);
                    const occupied = targetDraft.snapshot.text || targetDraft.snapshot.files.length || targetDraft.snapshot.images.length ||
                      targetDraft.snapshot.attachments?.length || targetDraft.snapshot.mcpReferences?.length;
                    document.update(targetScope, original.snapshot, occupied ? null : targetDraft.ref);
                  }
                }
                localWorkspace.consume(thread.workspace_id ?? "");
              }
              // Move the submitted version into the durable Thread's draft.
              // Anything typed or selected during creation stays in its project.
              if (!version) {
              queryClient.setQueryData<MCPDraftReference[]>(v2MCPReferenceKey(thread.workspace_id ?? "", thread.id),
                (current) => [...(current ?? []), ...mcpReferences.filter((item) => !current?.some(({ id }) => id === item.id))]);
              queryClient.setQueryData<MCPDraftReference[]>(v2MCPReferenceKey(thread.workspace_id ?? "", ""),
                (current) => current?.filter(({ id }) => !mcpReferences.some((item) => item.id === id)));
              setDrafts((current) => ({ ...current,
                [source]: current[source] === submittedDraft ? "" : current[source] ?? "",
                [target]: current[target] ?? submittedDraft }));
              queryClient.setQueryData<V2FileReference[]>(v2FileReferenceKey(thread.workspace_id ?? "", thread.id),
                (current) => [...(current ?? []), ...files.filter((file) =>
                  !current?.some(({ path }) => path === file.path))]);
              queryClient.setQueryData<V2FileReference[]>(v2FileReferenceKey(thread.workspace_id ?? "", ""),
                (current) => current?.filter(({ id }) => !files.some((file) => file.id === id)));
              queryClient.setQueryData<WorkspaceImageAttachment[]>(v2ImageReferenceKey(thread.workspace_id ?? "", thread.id),
                (current) => [...(current ?? []), ...images.filter((image) => !current?.some(({ id }) => id === image.id))]);
              queryClient.setQueryData<WorkspaceImageAttachment[]>(v2ImageReferenceKey(thread.workspace_id ?? "", ""),
                (current) => current?.filter(({ id }) => !images.some((image) => image.id === id)));
              queryClient.setQueryData<WorkspaceFileAttachment[]>(v2AttachmentReferenceKey(thread.workspace_id ?? "", thread.id),
                (current) => [...(current ?? []), ...attachments.filter((file) => !current?.some(({ id }) => id === file.id))]);
              queryClient.setQueryData<WorkspaceFileAttachment[]>(v2AttachmentReferenceKey(thread.workspace_id ?? "", ""),
                (current) => current?.filter(({ id }) => !attachments.some((file) => file.id === id)));
              }
              // The request can outlive its original new-task page. Hand off
              // its input above, but let the user keep their current task.
              if (shouldOpen) openConversation(thread.id);
              else setBackgroundCreations((current) => [...current.filter((item) => item.id !== thread.id), thread]);
              void queryClient.invalidateQueries({ queryKey: v2QueryKeys.threads("active") });
            }} onTurnSuccess={(threadID, submittedDraft) => setDrafts((current) =>
              current[`thread:${threadID}`] === submittedDraft
                ? { ...current, [`thread:${threadID}`]: "" } : current)}
            onManageModels={openModels} onWorkspaceChange={selectWorkspace}
            onImported={(workspace, currentDraft) => {
              setImportedWorkspaces((current) => [...current.filter(({ id }) => id !== workspace.id), workspace]);
              if (recovery) {
                const document = getV2DraftDocument(recovery);
                const scope = v2DraftScope(workspace.id);
                const target = readV2Draft(document, recovery, scope);
                if (!target.ref && !target.snapshot.text && !target.snapshot.files.length && !target.snapshot.images.length && !target.snapshot.attachments?.length)
                  document.update(scope, { text: currentDraft ?? "" }, null);
              } else setDrafts((current) => ({ ...current,
                [`new:${workspace.id}`]: current[`new:${workspace.id}`] ?? currentDraft ?? current[draftKey] ?? "" }));
              selectWorkspace(workspace.id);
            }}
            moreProjects={workspacesQuery.hasNextPage && <button className="v2-composer-chip"
              disabled={workspacesQuery.isFetchingNextPage} onClick={() => void workspacesQuery.fetchNextPage()}
              type="button">{t("加载更多项目", "Load more projects")}</button>}
            workspaceID={workspaceID} workspaces={workspaces} />
          : <V2Conversation client={client} onArchive={setArchiveCandidate}
            onOpenThread={openConversation}
            onBranchCreated={(thread) => {
              if (routeRef.current === route) openConversation(thread.id);
              else setBackgroundCreations((current) => [...current.filter(({ id }) => id !== thread.id), thread]);
            }}
            onOpenWorktree={(workspace) => {
              setImportedWorkspaces((current) => [...current.filter(({ id }) => id !== workspace.id), workspace]);
              selectWorkspace(workspace.id);
              setNewThreadModelRoute(null);
              navigate({ kind: "new" });
              closeNavigationSidebar();
            }}
            view={view} onOpenTool={openTool}
            reviewMemoryRef={reviewMemoryRef} reviewEntry={reviewEntry}
            onReviewEntryHandled={(requestID) => setReviewEntry((current) => current?.requestID === requestID ? undefined : current)}
            onOpenInspectorHome={() => navigate({ kind: "new", view: "inspector" })}
            onExitInspector={() => changeView("conversation")}
            draft={drafts[draftKey] ?? ""} onDraftChange={updateDraft}
            onManageModels={() => openModels(false)} onOpenInspector={openInspector}
            messageID={route.messageID} onExitMessage={() => navigate({ kind: "thread", threadID: selectedThreadID })}
            threadID={selectedThreadID} workspaces={workspaces} />}
      </div>
    </div>
    {backgroundCreations.length > 0 && <div className="v2-toast" role="status">
      {backgroundCreations.map((thread) => <div key={thread.id}>
        {t("对话已创建：", "Conversation created: ")}{thread.title}
        <button onClick={() => openConversation(thread.id)} type="button">{t("打开已创建的对话", "Open created conversation")}</button>
        <button aria-label={t(`关闭对话创建提示：${thread.title}`, `Dismiss conversation creation notice: ${thread.title}`)} onClick={() =>
          setBackgroundCreations((current) => current.filter((item) => item.id !== thread.id))} type="button">{t("关闭", "Close")}</button>
      </div>)}
    </div>}
    {(workspacesQuery.isError || threadsQuery.isError) && <div className="v2-toast" role="alert">
      {t("项目或对话列表加载失败，已有内容会保留。", "Projects or conversations could not be loaded. Existing content has been kept.")}
      <button onClick={() => { void workspacesQuery.refetch(); void threadsQuery.refetch(); }} type="button">{t("重试加载", "Retry loading")}</button>
    </div>}
    {persistenceWarning && <div className="v2-toast" role={recovery ? "alert" : "status"}>{persistenceWarning}</div>}
    <V2ConfirmDialog busy={archiveMutation.isPending} confirmLabel={t("归档", "Archive")} description={t("此对话会从侧栏隐藏，但消息、执行记录和审计证据都会保留。你可以随时从设置中恢复。", "This conversation will be hidden from the sidebar. Messages, execution records, and audit evidence will be kept. You can restore it in Settings at any time.")}
      onCancel={() => setArchiveCandidate(null)} onConfirm={() => archiveCandidate && archiveMutation.mutate(archiveCandidate)}
      open={Boolean(archiveCandidate)} title={t("归档这个对话？", "Archive this conversation?")} />
    {archiveMutation.isError && <div className="v2-toast" role="alert">{archiveMutation.error instanceof Error
      ? archiveMutation.error.message : t("归档失败", "Archive failed")}</div>}
  </div>;
}

export function V2WorkbenchEntry() {
  const client = useV2Client();
  return <V2Workbench client={client} />;
}
