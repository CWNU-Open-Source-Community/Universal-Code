import { lazy, useEffect, useMemo, useRef, useState, type RefObject } from "react";
import { QueryClient, QueryClientProvider, useInfiniteQuery, useMutation, useQueryClient } from "@tanstack/react-query";
import { Folder } from "lucide-react";
import type { APIClient } from "../api/client";
import type { ProviderDefinitionView, TaskBudgetSettings, ThreadDetailView, ThreadView, WorkspaceView } from "../api/types";
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
import { V2RecoveryProvider, useV2PersistentState, useV2PersistenceWarning, useV2RecoveryStore } from "./recovery-storage";
import { useV2Drafts } from "./recovery-session";
import { readV2Route } from "./navigation";
import { useV2CreationRecovery } from "./recovery-creation";
import type { WorkspaceImageAttachment } from "../api/image-attachments";
import { fileAttachmentIdentities, type WorkspaceFileAttachment } from "../api/file-attachments";
import { v2AttachmentReferenceKey } from "./attachment-keys";
import { v2ImageReferenceKey } from "./components/image-input";
import { assertV2DraftVersion, getV2DraftDocument, readV2Draft, requireV2DraftVersion, useV2DraftDocument, v2DraftScope } from "./draft-context";
import type { V2DraftVersion } from "./draft-version";
import type { TaskReviewToolMemory } from "./components/task-review-tools";
import { V2DraftConflict } from "./components/draft-conflict";
import { FileEditDraftProvider } from "../components/file-edit-drafts";
import { V2PhasePicker, type V2WorkPhase } from "./components/phase-picker";
import { useV2DraftTaskConfiguration } from "./draft-task-configuration";
import type { ProjectSkillChoice } from "../api/project-skills";
import { useV2WebAccessDefaults } from "./web-access-defaults";

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

function NewConversation({ client, workspaces, workspaceID, onWorkspaceChange, onCreated,
  onTurnSuccess, onManageModels, onOpenTaskConfiguration, budget, configurationValid, webAccessValid, projectSkills,
  draft: legacyDraft, onDraftChange: legacyDraftChange, creationAttemptRef, options, onOptionsChange, moreProjects, onImported }: {
  client: APIClient;
  workspaces: WorkspaceView[];
  workspaceID: string;
  onWorkspaceChange: (workspaceID: string) => void;
  onImported: (workspace: WorkspaceView, currentDraft?: string) => void;
  onCreated: (thread: ThreadView, submittedDraft: string, files: V2FileReference[], images?: WorkspaceImageAttachment[], version?: V2DraftVersion, attachments?: WorkspaceFileAttachment[], shouldOpen?: boolean) => void;
  onTurnSuccess: (threadID: string, submittedDraft: string) => void;
  onManageModels: (prepareForDraft?: boolean) => void;
  onOpenTaskConfiguration: () => void;
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
    onCreated(thread, originalDraft, files, input.images, input.draftVersion, input.attachments);
  });
  const { networkMode, allowedTargets, modelRoute } = options;
  const [modelCatalogError, setModelCatalogError] = useState("");
  const [savedPhase, setSavedPhase] = useV2PersistentState<unknown>(`new-thread-phase:${workspaceID}`, "deliver");
  const phase: V2WorkPhase = savedPhase === "plan" ? "plan" : "deliver";
  const create = async (content: string, files: V2FileReference[] = [], images: WorkspaceImageAttachment[] = [], draftVersion?: V2DraftVersion, attachments: WorkspaceFileAttachment[] = []) => {
    if (!webAccessValid) throw new Error("请重新选择网页访问方式，草稿已保留。");
    if (!configurationValid) throw new Error("任务配置尚未通过核对，请打开任务配置修正预算或重新读取项目设置。草稿已保留。");
    if (projectSkills?.refs.length && !projectSkills.confirmUntrustedContext) throw new Error("请在任务配置中确认所选技能上下文。草稿已保留。");
    normalizedTaskBudget(budget);
    if (phase === "plan" && !client.hasPlanDelivery) throw new Error("当前连接未启用计划确认。请检查连接，或选择直接执行。");
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
      setModelCatalogError(`无法检查可用模型，草稿已保留。${error instanceof Error && error.message
        ? ` ${error.message}` : " 请检查连接后重试。"}`);
      return v2ComposerNotSubmitted;
    }
    const request = {
      version: "thread_creation.v1",
      workspace_id: workspaceID,
      goal: content || (images.length ? "图片对话" : "文件对话"),
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
          if (draftVersion) assertV2DraftVersion(managedDraft, draftVersion, { text: submittedDraft, files, images, attachments });
          else draftVersion = requireV2DraftVersion(managedDraft, { text: submittedDraft, files, images, attachments });
        }
        const intent = creationRecovery.prepare(request, content, files, submittedDraft, images, draftVersion, attachments, projectSkills);
        const thread = await creationRecovery.resolve(intent);
        const input = creationRecovery.handoff(intent, thread);
        const submission = turn.mutateAsync(input);
        onCreated(thread, submittedDraft, files, images, input.draftVersion, attachments, requestIsCurrent());
        void queryClient.invalidateQueries({ queryKey: v2QueryKeys.threads("active") });
        try { await submission; }
        catch (error) { if (v2TurnFailed(error) && !input.draftVersion) onTurnSuccess(thread.id, submittedDraft); throw error; }
        if (!input.draftVersion) onTurnSuccess(thread.id, submittedDraft);
      } else {
        const fingerprint = JSON.stringify([request, files, images, fileAttachmentIdentities(attachments), projectSkills]);
        const operationID = creationAttemptRef.current.get(fingerprint) ?? globalThis.crypto.randomUUID();
        creationAttemptRef.current.set(fingerprint, operationID);
        const result = await client.createThread(request, `v2-thread-create-${operationID}`);
        if (projectSkills?.refs.length) {
          try { await client.selectProjectSkills(result.thread.last_run_id, projectSkills, `v2-project-skills-${operationID}`); }
          catch (error) {
            void queryClient.invalidateQueries({ queryKey: v2QueryKeys.threads("active") });
            throw new Error(`对话 ${result.thread.id} 已保存，技能选择失败，尚未发送首条消息。${error instanceof Error ? error.message : "请重试。"}`);
          }
        }
        const submission = turn.mutateAsync({ threadID: result.thread.id, workspaceID, content,
          ...(files.length ? { files } : {}),
          ...(images.length ? { images } : {}),
          ...(attachments.length ? { attachments } : {}),
          operationKey: `v2-thread-create-turn-${operationID}`, createdAt: new Date().toISOString() });
        onCreated(result.thread, submittedDraft, files, images, undefined, attachments, requestIsCurrent());
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
    <header><Folder aria-hidden="true" size={17} /><strong>新对话</strong>
      <V2WorkspaceStart client={client} onSelect={(workspace) => onImported(workspace, draft)} />{moreProjects}</header>
    <div className="v2-new-conversation-body"><div className="v2-new-copy">
      <span className="v2-orb" aria-hidden="true"><i /><i /></span>
      <h1>接下来要做什么？</h1><p>{workspaceID ? "描述目标，随时查看进展、补充消息或停止。"
        : "先接入项目。你也可以先写下需求，选择项目后继续。"}</p>
    </div></div>
    {creationRecovery.notice}
    {modelCatalogError && <div className="v2-notice" role="alert">{modelCatalogError}</div>}
    {!webAccessValid && <div className="v2-notice" role="alert">保存的网页访问设置需要更新，请在输入框下方重新选择。</div>}
    {!configurationValid && <div className="v2-notice" role="alert">任务配置需要修正或重新核对，草稿已保留。
      <button onClick={onOpenTaskConfiguration} type="button">修正任务配置</button></div>}
    <div className="v2-composer-dock">
      {managedDraft && <V2DraftConflict key={workspaceID} client={client} workspaceID={workspaceID} state={managedDraft.state}
        onResolve={(token, ref) => { managedDraft.document.resolve(managedDraft.scope, token, ref); }} />}
      <V2Composer client={client}
      draft={draft} onDraftChange={onDraftChange}
      disabled={!client.hasThreadControl} submitDisabled={!configurationValid || !webAccessValid} onSubmit={create}
      onManageModels={onManageModels} onPendingModelRouteChange={(route) =>
        onOptionsChange({ ...options, modelRoute: route })}
      pendingModelRoute={modelRoute}
      newThreadControls={<><button aria-label="任务预算与项目配置" className="v2-composer-chip"
        onClick={onOpenTaskConfiguration} type="button">任务配置{budget ? " · 自定义" : ""}</button>
      <V2PhasePicker phase={phase} onChange={setSavedPhase}
        disabled={!client.hasThreadControl} planAvailable={client.hasPlanDelivery} />
      <V2NetworkScopeControl disabled={!client.hasThreadControl}
        mode={networkMode} onChange={(nextMode, nextTargets) => {
          onOptionsChange({ ...options, networkMode: nextMode, allowedTargets: nextTargets });
        }} targets={allowedTargets} /></>}
      onWorkspaceChange={onWorkspaceChange} placeholder="输入任务或问题…" threadID=""
      workspaceID={workspaceID} workspaces={workspaces} />
      <small className="v2-composer-caption">{phase === "plan"
        ? "先分析并准备计划，确认后开始执行。可随时补充要求。"
        : "直接处理需求，操作仍遵循任务权限。可随时补充要求。"}</small></div>
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
  const [sidebarVisible, setSidebarVisible] = useState(() => !window.matchMedia?.("(max-width: 760px)").matches);
  const [searchOpen, setSearchOpen] = useState(false);
  const [threadSearch, setThreadSearch] = useState("");
  const [appliedThreadSearch, setAppliedThreadSearch] = useState("");
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
  const workspaceID = typeof storedWorkspace === "string" ? storedWorkspace : "";
  const draftTaskConfiguration = useV2DraftTaskConfiguration(workspaceID);
  // An explicitly imported existing project may be outside the first list page.
  // Retain its pathless receipt so selection survives list refetches.
  const [importedWorkspaces, setImportedWorkspaces] = useState<WorkspaceView[]>([]);
  const [backgroundCreations, setBackgroundCreations] = useState<ThreadView[]>([]);
  const [drafts, setDrafts] = useV2Drafts();
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
    queryKey: v2QueryKeys.threadSearch("active", appliedThreadSearch),
    queryFn: ({ signal, pageParam }) => client.getPage<ThreadView>("/threads",
      { limit: 100, status: "active", ...(appliedThreadSearch ? { q: appliedThreadSearch } : {}) }, pageParam, signal),
    initialPageParam: "", getNextPageParam: (last) => last.page.next_cursor || undefined,
    enabled: !searchPending && !searchTooLong,
    // Infinite queries refetch every cached page. Poll only a single page;
    // history still refreshes explicitly and through the existing invalidations.
    refetchInterval: (query) => sidebarVisible && surface === "conversation" && !searchPending &&
      (query.state.data?.pages.length ?? 1) === 1 ? 15_000 : false,
  });
  const workspaces = useMemo(() => [...new Map([...importedWorkspaces,
    ...(workspacesQuery.data?.pages.flatMap(({ items }) => items) ?? [])]
    .map((workspace) => [workspace.id, workspace])).values()], [workspacesQuery.data, importedWorkspaces]);
  const threads = useMemo(() => [...new Map(threadsQuery.data?.pages.flatMap(({ items }) =>
    items).map((thread) => [thread.id, thread])).values()], [threadsQuery.data]);
  useEffect(() => {
    if (!workspaceID && workspaces[0]) {
      const id = workspaces[0].id;
      setDrafts((current) => ({ ...current, [`new:${id}`]: current[`new:${id}`] ?? current["new:"] ?? "" }));
      setWorkspaceID(id);
    }
  }, [workspaceID, workspaces]);
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
  const closeNavigationSidebar = () => {
    if (window.matchMedia?.("(max-width: 760px)").matches) setSidebarVisible(false);
  };
  const openConversation = (threadID: string) => {
    setBackgroundCreations((current) => current.filter((thread) => thread.id !== threadID));
    updateModelSetupToken("");
    navigate({ kind: "thread", threadID, ...(route.view ? { view: route.view } : {}) });
    closeNavigationSidebar();
  };
  const startNew = () => {
    updateModelSetupToken("");
    const currentWorkspace = queryClient.getQueryData<ThreadDetailView>(
      v2QueryKeys.thread(selectedThreadID))?.thread.workspace_id ??
      threads.find(({ id }) => id === selectedThreadID)?.workspace_id;
    if (currentWorkspace) selectWorkspace(currentWorkspace);
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
  const completeModelSetup = (token: string, definition: ProviderDefinitionView) => {
    const setup = modelSetupContextRef.current;
    const currentRoute = routeRef.current;
    if (!token || token !== setup?.token || !setup.entered || workspaceID !== setup.workspaceID ||
      currentRoute.section !== "models" || currentRoute.kind !== "new") return;
    setNewThreadModelRoute({ provider: definition.id, model: definition.default_model });
    updateModelSetupToken("");
    navigate({ ...currentRoute, section: undefined }, true);
    requestAnimationFrame(() => requestAnimationFrame(() =>
      document.querySelector<HTMLTextAreaElement>(".v2-new-conversation .v2-composer textarea")?.focus()));
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
      onToggleSidebar={() => setSidebarVisible((visible) => !visible)} sidebarVisible={sidebarVisible} />
    {sidebarVisible && <button aria-label="关闭侧栏" className="v2-sidebar-backdrop"
      onClick={() => setSidebarVisible(false)} type="button" />}
    <div className="v2-shell-body">
      {sidebarVisible && (surface === "settings" ? <V2SettingsSidebar onBack={returnFromSettings}
        onSelect={setSettingsSection} section={settingsSection} /> : <V2Sidebar
          onArchive={setArchiveCandidate} onNewConversation={startNew} onOpenModels={() => openModels(newConversation)}
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
          onSearchChange={(value) => { setThreadSearch(value); if (!value) setSearchComposing(false); }}
          onSearchCompositionChange={setSearchComposing}
          hasMore={threadsQuery.hasNextPage} loading={threadsQuery.isLoading}
          truncated={threadsQuery.data?.pages.at(-1)?.page.truncated === true}
          loadingMore={threadsQuery.isFetchingNextPage} loadFailed={threadsQuery.isError}
          loadMoreFailed={threadsQuery.isFetchNextPageError}
          refreshing={threadsQuery.isFetching && !threadsQuery.isLoading && !threadsQuery.isFetchingNextPage}
          onRetry={() => void (threadsQuery.isFetchNextPageError ? threadsQuery.fetchNextPage() : threadsQuery.refetch())}
          onLoadMore={() => void threadsQuery.fetchNextPage()} onRefresh={() => void threadsQuery.refetch()}
          selectedThreadID={newConversation ? "" : selectedThreadID} threads={threads} workspaces={workspaces} />)}
      <div className="v2-product-surface">
        {route.kind === "invalid" ? <div className="v2-notice" role="alert">任务地址无法识别。
          <button onClick={startNew} type="button">打开新对话</button></div>
          : surface === "settings" ? <V2LazySurface resetKey="settings" loadingText="正在加载设置…" errorLabel="设置">
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
          draftWorkspaceID={workspaceID} draftBudget={draftTaskConfiguration.budget}
          onDraftBudgetChange={draftTaskConfiguration.onBudgetChange}
          onDraftValidityChange={draftTaskConfiguration.onValidityChange}
          draftProjectSkills={draftTaskConfiguration.projectSkills} onDraftProjectSkillsChange={draftTaskConfiguration.onProjectSkillsChange}
          prepareModelForDraft={Boolean(modelSetupToken)} modelSetupToken={modelSetupToken}
          onModelReady={completeModelSetup} /></V2LazySurface> : route.tool
          ? <V2LazySurface resetKey="inspector-tools"
            loadingText="正在加载检查工具…" errorLabel="检查工具">
            <V2InspectorTools client={client} tool={route.tool} resourceID={route.resourceID}
            threadID={selectedThreadID} pane={route.pane} onBack={openInspector} onOpenSettings={setSettingsSection}
            onOpenRun={openCreatedRun} /></V2LazySurface>
          : view === "inspector" && !selectedThreadID
          ? <V2LazySurface resetKey="inspector-home" loadingText="正在加载 Inspector…" errorLabel="Inspector">
            <V2InspectorHome client={client} onOpenTool={openTool} onOpenSettings={setSettingsSection} /></V2LazySurface>
          : newConversation || !selectedThreadID
          ? <NewConversation client={client} draft={drafts[draftKey] ?? ""} onDraftChange={updateDraft}
            creationAttemptRef={creationAttemptRef}
            options={newThreadOptions} onOptionsChange={setNewThreadOptions}
            webAccessValid={webAccessDefaults.valid}
            budget={draftTaskConfiguration.budget} configurationValid={draftTaskConfiguration.valid} projectSkills={draftTaskConfiguration.projectSkills}
            onOpenTaskConfiguration={() => setSettingsSection("task-configuration")}
            onCreated={(thread, submittedDraft, files, images = [], version, attachments = [], shouldOpen = true) => {
              const source = `new:${thread.workspace_id}`;
              const target = `thread:${thread.id}`;
              // Move the submitted version into the durable Thread's draft.
              // Anything typed or selected during creation stays in its project.
              if (!version) {
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
              type="button">加载更多项目</button>}
            workspaceID={workspaceID} workspaces={workspaces} />
          : <V2Conversation client={client} onArchive={setArchiveCandidate}
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
            threadID={selectedThreadID} workspaces={workspaces} />}
      </div>
    </div>
    {backgroundCreations.length > 0 && <div className="v2-toast" role="status">
      {backgroundCreations.map((thread) => <div key={thread.id}>
        对话已创建：{thread.title}
        <button onClick={() => openConversation(thread.id)} type="button">打开已创建的对话</button>
        <button aria-label={`关闭对话创建提示：${thread.title}`} onClick={() =>
          setBackgroundCreations((current) => current.filter((item) => item.id !== thread.id))} type="button">关闭</button>
      </div>)}
    </div>}
    {(workspacesQuery.isError || threadsQuery.isError) && <div className="v2-toast" role="alert">
      项目或对话列表加载失败，已有内容会保留。
      <button onClick={() => { void workspacesQuery.refetch(); void threadsQuery.refetch(); }} type="button">重试加载</button>
    </div>}
    {persistenceWarning && <div className="v2-toast" role={recovery ? "alert" : "status"}>{persistenceWarning}</div>}
    <V2ConfirmDialog busy={archiveMutation.isPending} confirmLabel="归档" description="此对话会从侧栏隐藏，但消息、执行记录和审计证据都会保留。你可以随时从设置中恢复。"
      onCancel={() => setArchiveCandidate(null)} onConfirm={() => archiveCandidate && archiveMutation.mutate(archiveCandidate)}
      open={Boolean(archiveCandidate)} title="归档这个对话？" />
    {archiveMutation.isError && <div className="v2-toast" role="alert">{archiveMutation.error instanceof Error
      ? archiveMutation.error.message : "归档失败"}</div>}
  </div>;
}

export function V2WorkbenchEntry() {
  const client = useV2Client();
  return <V2Workbench client={client} />;
}
