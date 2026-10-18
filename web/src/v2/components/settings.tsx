import { lazy, Suspense, useEffect, useMemo, useRef, useState } from "react";
import { createPortal } from "react-dom";
import { useInfiniteQuery, useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { ArchiveRestore, ArrowRight, BookOpen, Monitor, Search, Trash2, X } from "lucide-react";
import type { APIClient } from "../../api/client";
import type { ProviderDefinitionView, TaskBudgetSettings, ThreadDetailView, ThreadView, WebAccessMode, WorkspaceView } from "../../api/types";
import { applyPrayuTheme, readPrayuTheme, type PrayuTheme } from "../../lib/appearance";
import { useModalFocusTrap } from "../../hooks/use-modal-focus-trap";
import { v2QueryKeys } from "../query-keys";
import type { V2SettingsSection } from "./sidebar";
import { V2ConfirmDialog } from "./dialog";
import { V2ModelSettings, type V2ModelProviderPreset } from "./model-settings";
import {
  v2ModelProviderPresets,
  type V2ConfiguredModelProviderPreset,
} from "./model-provider-presets";
import { V2PermissionControl } from "./permission-control";
import { V2ProviderSettings } from "./provider-settings";
import { V2RuntimeCapabilityControl } from "./runtime-capability-control";
import { V2ExecutionSettings } from "./execution-settings";
import { V2CommandServiceSettings } from "./command-service-settings";
import { V2WebAccessSettings } from "./web-access-settings";
import { desktopBridgeAvailable } from "../../lib/desktop-bridge";
import { useLocale } from "../../lib/locale";
import type { ProjectSkillChoice } from "../../api/project-skills";
import { ShortcutSettings } from "../../components/shared-settings-panels";
import { ModelAvailabilitySettings } from "../../components/model-availability-dialog";
import { V2AboutSettings, V2ExtensionSettings, V2InspectorPreferences, V2SkillSettings } from "./advanced-settings";
import "./settings.css";

const TaskConfigurationSettings = lazy(() => import("./task-configuration-settings").then((module) => ({ default: module.TaskConfigurationSettings })));
const SandboxEnvironmentPanel = lazy(() => import("../../components/sandbox-environment-panel").then((module) => ({ default: module.SandboxEnvironmentPanel })));
const VerificationSettingsPanel = lazy(() => import("../../components/verification-settings-panel").then((module) => ({ default: module.VerificationSettingsPanel })));

function SettingRow({ title, detail, children }: { title: string; detail: string; children: React.ReactNode }) {
  return <div className="v2-setting-row"><div><strong>{title}</strong><span>{detail}</span></div>
    <div className="v2-setting-value">{children}</div></div>;
}

function ConnectionsSettings({ client, threadID, sourceRunID, onSelect, onOpenGithubReview, onOpenTask }: {
  client: APIClient; threadID: string; sourceRunID?: string; onSelect: (section: V2SettingsSection) => void;
  onOpenGithubReview?: (runID: string) => void; onOpenTask?: (workspaceID?: string) => void;
}) {
  const { t } = useLocale();
  const thread = useQuery({ queryKey: v2QueryKeys.thread(threadID), enabled: Boolean(threadID),
    queryFn: ({ signal }) => client.get<ThreadDetailView>(`/threads/${encodeURIComponent(threadID)}`, {}, signal) });
  const detail = !thread.isError && thread.data?.thread.id === threadID ? thread.data : undefined;
  const run = sourceRunID ? detail?.runs.find((item) => item.run.id === sourceRunID)?.run
    ?? (detail?.active_run?.id === sourceRunID ? detail.active_run : detail?.last_run?.id === sourceRunID ? detail.last_run : undefined)
    : detail?.active_run ?? detail?.last_run;
  return <><h1>{t("连接与环境", "Connections & environment")}</h1><p className="v2-settings-lead">{t("连接模型，准备项目需要的工具，然后回到任务开始工作。", "Connect a model, prepare your project tools, then return to your task.")}</p>
    <section className="v2-connection-task" aria-label={t("当前任务", "Current task")}>
      <div><strong>{threadID ? detail?.thread.title || t("当前任务", "Current task") : t("从一个任务开始", "Start with a task")}</strong>
        <p>{threadID ? t("在下面调整连接与选项，完成后继续对话。", "Adjust connections and options below, then continue the conversation.") : t("打开项目，选好模型，在对话中描述你想完成的工作。", "Open a project, choose a model, and describe what you want to do.")}</p></div>
      {onOpenTask && <button onClick={() => onOpenTask()} type="button">
        {threadID ? t("返回任务", "Return to task") : t("开始任务", "Start a task")}<ArrowRight aria-hidden="true" size={15} /></button>}
    </section>
    <nav className="v2-connection-navigation" aria-label={t("连接与环境设置", "Connection and environment settings")}>
      <section aria-labelledby="connection-task-preparation"><h2 id="connection-task-preparation">{t("任务准备", "Task preparation")}</h2>
        <button onClick={() => onSelect("models")} type="button"><span><strong>{t("模型连接", "Model connections")}</strong><small>{t("选择供应商、保存凭据并验证模型。", "Choose a provider, save credentials, and verify the model.")}</small></span><ArrowRight aria-hidden="true" size={16} /></button>
        <button onClick={() => onSelect("task-configuration")} type="button"><span><strong>{t("任务预算与项目配置", "Task budget & project configuration")}</strong><small>{threadID ? t("查看本次执行保存的上限和项目设置。", "View limits and project settings saved for this run.") : t("设置新任务的用量上限，预览项目规则。", "Set usage limits for new tasks and preview project rules.")}</small></span><ArrowRight aria-hidden="true" size={16} /></button>
      </section>
      <section aria-labelledby="connection-task-tools"><h2 id="connection-task-tools">{t("工具与协作", "Tools & collaboration")}</h2>
        <button onClick={() => onSelect("extensions")} type="button"><span><strong>{t("扩展与代码智能", "Extensions & code intelligence")}</strong><small>{t("添加 MCP、Plugin 和语言服务，按步骤完成接入。", "Set up MCP servers, plugins, and language services.")}</small></span><ArrowRight aria-hidden="true" size={16} /></button>
        <button disabled={!run?.id || !onOpenGithubReview} onClick={() => { if (run?.id) onOpenGithubReview?.(run.id); }} type="button">
          <span><strong>{t("GitHub 连接与审阅", "GitHub connections & review")}</strong><small>{run?.id ? t("管理此任务的 GitHub 连接，查看和处理 PR。", "Manage GitHub connections and pull requests for this task.")
            : !threadID ? t("从侧栏打开一个任务，即可管理它的 GitHub 连接。", "Open a task from the sidebar to manage its GitHub connection.")
              : thread.isPending ? t("正在读取任务记录…", "Loading task records…") : thread.isError ? t("任务记录读取失败，请点击下方“重新读取任务”。", "Task records could not be loaded. Select Reload task below.")
                : t("请返回任务，选择一条执行记录后进入 GitHub 审阅。", "Return to the task and select a run to open GitHub review.")}</small></span><ArrowRight aria-hidden="true" size={16} /></button>
      </section>
      <section aria-labelledby="connection-task-environment"><h2 id="connection-task-environment">{t("运行设置", "Runtime settings")}</h2>
        <button onClick={() => onSelect("environment")} type="button"><span><strong>{t("执行环境设置", "Execution environment settings")}</strong><small>{t("检测 Local、Docker Engine 与官方 sbx，保存启用选项和固定镜像。", "Detect Local, Docker Engine, and official sbx; save enabled options and pinned images.")}</small></span><ArrowRight aria-hidden="true" size={16} /></button>
        <button onClick={() => onSelect("permissions")} type="button"><span><strong>{t("任务权限与执行环境", "Task permissions & environment")}</strong><small>{threadID ? t("选择操作确认方式，查看项目访问范围与执行环境。", "Choose approval behavior and view project access and the execution environment.") : t("查看当前环境；打开任务后可调整它的操作权限。", "View the current environment. Open a task to adjust its permissions.")}</small></span><ArrowRight aria-hidden="true" size={16} /></button>
        <button onClick={() => onSelect("about")} type="button"><span><strong>{t("应用连接与诊断", "App connection & diagnostics")}</strong><small>{t("查看版本和连接状态，处理连接问题。", "View version and connection status and troubleshoot connection issues.")}</small></span><ArrowRight aria-hidden="true" size={16} /></button>
      </section>
    </nav>
    {threadID && thread.isError && <button className="v2-setting-link" onClick={() => void thread.refetch()} type="button">{t("重新读取任务", "Reload task")}</button>}
    {run && <details className="v2-connection-record"><summary>{t("查看任务记录", "View task records")}</summary>
      <dl><div><dt>{t("任务 ID", "Task ID")}</dt><dd>{threadID}</dd></div><div><dt>{t("执行 ID", "Run ID")}</dt><dd>{run.id}</dd></div></dl></details>}
  </>;
}

function FontLicenseControl() {
  const { t } = useLocale();
  const [open, setOpen] = useState(false);
  const [license, setLicense] = useState("");
  const [error, setError] = useState("");
  const triggerRef = useRef<HTMLButtonElement>(null);
  const close = () => setOpen(false);
  const dialogRef = useModalFocusTrap<HTMLElement>(open, close, false, undefined, {
    isolateBackground: true,
    returnFocusRef: triggerRef,
  });

  useEffect(() => {
    if (!open || license) return;
    const controller = new AbortController();
    setError("");
    void fetch("/licenses/HarmonyOS-Sans.txt", { cache: "no-store", signal: controller.signal })
      .then(async (response) => {
        if (!response.ok) throw new Error(`HTTP ${response.status}`);
        const content = await response.text();
        if (!content.includes("HarmonyOS Sans Fonts License Agreement")) {
          throw new Error("Invalid license text");
        }
        setLicense(content);
      })
      .catch((reason: unknown) => {
        if (controller.signal.aborted) return;
        setError(reason instanceof Error ? reason.message : "Unable to read license text");
      });
    return () => controller.abort();
  }, [license, open]);

  return <>
    <button className="v2-setting-link" onClick={() => setOpen(true)} ref={triggerRef}
      type="button">{t("查看许可", "View license")}</button>
    {open && createPortal(<div className="v2-overlay" onMouseDown={(event) => {
      if (event.target === event.currentTarget) close();
    }} role="presentation">
      <section aria-labelledby="v2-font-license-title" aria-modal="true"
        className="v2-dialog v2-license-dialog" ref={dialogRef} role="dialog" tabIndex={-1}>
        <header><span><BookOpen aria-hidden="true" size={17} /></span>
          <h2 id="v2-font-license-title">{t("HarmonyOS Sans Fonts 许可", "HarmonyOS Sans Fonts license")}</h2>
          <button aria-label={t("关闭许可", "Close license")} onClick={close} type="button"><X aria-hidden="true" size={16} /></button>
        </header>
        <div aria-live="polite" className="v2-license-content">
          {!license && !error && <p>{t("正在读取随软件发布的许可文本…", "Loading the license bundled with the app…")}</p>}
          {error && <p role="alert">{t("无法读取许可文本：", "Unable to read license text: ")}{error}</p>}
          {license && <pre>{license}</pre>}
        </div>
        <footer><button onClick={close} type="button">{t("关闭", "Close")}</button></footer>
      </section>
    </div>, document.body)}
  </>;
}

function GeneralSettings({ threadID, workspaces, onPermissions }: {
  threadID: string;
  workspaces: WorkspaceView[];
  onPermissions: () => void;
}) {
  const { locale, setLocale, t } = useLocale();
  return <>
    <h1>{t("常规", "General")}</h1>
    <p className="v2-settings-lead">{t("调整应用偏好。项目访问范围和执行环境可在「当前任务权限」中设置。", "Adjust app preferences. Manage project access and execution environments in Current task permissions.")}</p>
    <section className="v2-settings-section"><h2>{t("常规", "General")}</h2>
      <div className="v2-settings-card">
        <SettingRow detail={t("新对话中选择项目；桌面应用可直接打开本机文件夹", "Choose a project in a new conversation. The desktop app can open local folders directly.")} title={t("项目", "Projects")}>
          <span>{t(`${workspaces.length} 个已加载项目`, `Loaded projects: ${workspaces.length}`)}</span>
        </SettingRow>
        <SettingRow detail={t("选择界面语言，立即生效并保存在本机。", "Choose the interface language. Changes apply immediately and are saved on this device.")} title={t("语言", "Language")}>
          <div className="v2-setting-segmented" role="group" aria-label={t("界面语言", "Interface language")}>
            <button aria-pressed={locale === "zh-CN"} onClick={() => setLocale("zh-CN")} type="button">中文</button>
            <button aria-pressed={locale === "en-US"} onClick={() => setLocale("en-US")} type="button">English</button>
          </div>
        </SettingRow>
        <SettingRow detail={threadID ? t("为当前打开的任务选择操作确认方式和访问范围。", "Choose approval behavior and access scope for the current task.") : t("先打开一个对话，再查看它的任务权限。", "Open a conversation to view its task permissions.")} title={t("当前任务权限", "Current task permissions")}>
          <button className="v2-setting-link" aria-label={t("管理当前任务权限", "Manage current task permissions")} disabled={!threadID}
            onClick={onPermissions} type="button">{t("管理", "Manage")}</button>
        </SettingRow>
        <SettingRow detail={t("中文界面使用 HarmonyOS Sans Fonts；完整许可文本随软件发布。", "Chinese text uses HarmonyOS Sans Fonts. The full license is bundled with the app.")} title={t("第三方字体", "Third-party fonts")}>
          <FontLicenseControl />
        </SettingRow>
      </div>
    </section>
  </>;
}

function AppearanceSettings() {
  const { t } = useLocale();
  const [theme, setTheme] = useState<PrayuTheme>(() => readPrayuTheme());
  const select = (next: PrayuTheme) => { setTheme(next); applyPrayuTheme(next); };
  return <><h1>{t("外观", "Appearance")}</h1><section className="v2-settings-section"><h2>{t("主题与材质", "Theme & materials")}</h2>
    <div className="v2-settings-card v2-appearance-card">
      <div className="v2-theme-options" role="group" aria-label={t("应用主题", "App theme")}>
        {(["light", "dark", "glass"] as const).map((option) => <button aria-pressed={theme === option}
          key={option} onClick={() => select(option)} type="button">
          <span aria-hidden="true" className={`v2-theme-preview theme-${option}`} data-prayu-theme={option}><i /><i /></span>
          <strong>{option === "light" ? t("浅色", "Light") : option === "dark" ? t("深色", "Dark") : t("雾透", "Mist")}</strong>
        </button>)}</div>
      <p>{t("雾透让窗口背景透出柔和的灰蓝层次；深色使用深灰玻璃材质。开启系统的「降低透明度」后，界面会使用清晰的实色背景。", "Mist adds soft blue-gray translucency; Dark uses dark gray glass. Enable Reduce transparency in your system settings to use solid backgrounds.")}</p>
    </div></section></>;
}

function ModelSettingsPage({ client, initialAdvancedOpen = false, prepareForDraft = false,
  setupToken = "", onModelReady }: {
  client: APIClient; initialAdvancedOpen?: boolean; prepareForDraft?: boolean;
  setupToken?: string;
  onModelReady?: (token: string, definition: Pick<ProviderDefinitionView, "id" | "default_model">) => void;
}) {
  const { t } = useLocale();
  const [selected, setSelected] = useState<V2ConfiguredModelProviderPreset | null>(null);
  const [advancedOpen, setAdvancedOpen] = useState(initialAdvancedOpen);
  useEffect(() => { if (initialAdvancedOpen) setAdvancedOpen(true); }, [initialAdvancedOpen]);
  const selectedIDRef = useRef("");
  const credentials = useQuery({
    queryKey: ["v2", "provider-credentials"],
    queryFn: ({ signal }) => client.providerCredentialStatuses(signal),
    enabled: Boolean(client.hasProviderCredentials),
  });
  const configuredProviderIDs = useMemo(() => new Set(
    (credentials.data?.items ?? []).filter((status) => status.configured)
      .map((status) => status.provider),
  ), [credentials.data?.items]);
  const presets = useMemo<V2ConfiguredModelProviderPreset[]>(() =>
    v2ModelProviderPresets.map((preset) => ({ ...preset,
      credentialConfigured: configuredProviderIDs.has(preset.id) })),
  [configuredProviderIDs]);

  const restoreCatalogFocus = () => {
    const selectedID = selectedIDRef.current;
    setSelected(null);
    requestAnimationFrame(() => document.querySelector<HTMLButtonElement>(
      `[data-model-provider-id="${selectedID}"]`,
    )?.focus());
  };
  const selectPreset = (catalogPreset: V2ModelProviderPreset) => {
    const preset = presets.find((candidate) => candidate.id === catalogPreset.id);
    if (!preset) return;
    selectedIDRef.current = preset.id;
    setSelected(preset);
  };

  const advancedPanel = <section className="v2-model-advanced">
      <button aria-expanded={advancedOpen} className="v2-model-advanced-toggle"
        onClick={() => setAdvancedOpen((open) => !open)} type="button">
        {t("高级模型设置：默认模型、能力诊断与费用预算", "Advanced model settings: defaults, capabilities & cost budgets")}
      </button>
      {advancedOpen && <><p>{t("新对话可在输入区直接选模型；这里管理未单独选择时的默认值。价格表只用于启用金额上限的任务。", "Choose a model directly in a new conversation. Set defaults here for tasks without a specific selection. Pricing is only used for tasks with a cost limit.")}</p>
        <ModelAvailabilitySettings client={client} /></>}
    </section>;

  if (selected) {
    return <>{initialAdvancedOpen && advancedPanel}
      <V2ProviderSettings client={client} initialPreset={selected.draft}
        onExit={restoreCatalogFocus} prepareForDraft={prepareForDraft}
        onReady={(definition) => onModelReady?.(setupToken, definition)} /></>;
  }

  return <>
    <V2ModelSettings client={client} onSelectPreset={selectPreset} presets={presets}
      prepareForDraft={prepareForDraft} onReady={(source) => onModelReady?.(setupToken, source)} />
    {advancedPanel}
  </>;
}

function ArchivedSettings({ client, onOpenThread }: { client: APIClient; onOpenThread?: (id: string) => void }) {
  const { locale, t } = useLocale();
  const queryClient = useQueryClient();
  const [search, setSearch] = useState("");
  const [deleteCandidate, setDeleteCandidate] = useState<ThreadView | null>(null);
  const query = useInfiniteQuery({
    queryKey: v2QueryKeys.threads("archived"),
    queryFn: ({ signal, pageParam }) => client.getPage<ThreadView>("/threads",
      { limit: 100, status: "archived" }, pageParam, signal),
    initialPageParam: "", getNextPageParam: (last) => last.page.next_cursor || undefined,
  });
  const transition = useMutation({
    mutationFn: ({ thread, action }: { thread: ThreadView; action: "restore" | "delete" }) =>
      client.transitionThread(thread.id, action, {
        version: "thread_lifecycle.v1", expected_version: thread.version,
      }, `v2-archived-${action}-${globalThis.crypto.randomUUID()}`),
    onSuccess: () => {
      setDeleteCandidate(null);
      void queryClient.invalidateQueries({ queryKey: v2QueryKeys.threads("archived") });
      void queryClient.invalidateQueries({ queryKey: v2QueryKeys.threads("active") });
    },
  });
  const normalized = search.trim().toLocaleLowerCase();
  const loaded = useMemo(() => [...new Map(query.data?.pages.flatMap(({ items }) => items)
    .map((thread) => [thread.id, thread])).values()], [query.data]);
  const threads = useMemo(() => loaded.filter((thread) => !normalized ||
    thread.title.toLocaleLowerCase().includes(normalized)), [normalized, loaded]);
  return <><h1>{t("已归档的聊天", "Archived chats")}</h1><p className="v2-settings-lead">{t("在这里查看归档对话及其记录。选择「取消归档」可将对话放回侧栏。", "View archived conversations and their records. Unarchive a conversation to return it to the sidebar.")}</p>
    <label className="v2-archive-search"><Search aria-hidden="true" size={15} />
      <input aria-label={t("搜索已归档的聊天", "Search archived chats")} onChange={(event) => setSearch(event.target.value)}
        placeholder={t("搜索已归档的聊天", "Search archived chats")} type="search" value={search} /></label>
    <p className="v2-history-scope">{t(`按标题搜索当前已加载的 ${loaded.length} 条归档对话。`, `Search the ${loaded.length} loaded archived conversations by title. `)}
      {query.isLoading ? t("正在读取列表。", "Loading the list.") : query.isError ? t("本次加载未完成，请重试。", "Loading did not complete. Please retry.")
        : query.hasNextPage ? t("可以继续加载更早记录。", "More records are available.") : t("当前列表已加载完毕。", "All records have been loaded.")}</p>
    <div className="v2-archive-list">
      {query.isLoading && <p>{t("正在加载…", "Loading…")}</p>}
      {query.isError && <p role="alert">{t("无法读取归档列表。", "Unable to load archived chats.")}<button onClick={() => void (query.isFetchNextPageError
        ? query.fetchNextPage() : query.refetch())} type="button">{t("重试归档列表", "Retry archived chats")}</button></p>}
      {!query.isLoading && !query.isError && threads.length === 0 && <div className="v2-archive-empty">
        {normalized ? t("已加载的标题中没有匹配项", "No matching titles in the loaded chats") : t("暂无已归档的聊天", "No archived chats yet")}</div>}
      {threads.map((thread) => <article key={thread.id}><div><strong>{onOpenThread
        ? <button className="link-button" aria-label={t(`打开 ${thread.title}`, `Open ${thread.title}`)} onClick={() => onOpenThread(thread.id)}
          type="button">{thread.title}</button> : thread.title}</strong>
        <span>{thread.archived_at ? new Date(thread.archived_at).toLocaleString(locale) : t("已归档", "Archived")}</span></div>
        <button disabled={!client.hasThreadControl || transition.isPending}
          onClick={() => transition.mutate({ thread, action: "restore" })} type="button">
          <ArchiveRestore aria-hidden="true" size={15} />{t("取消归档", "Unarchive")}</button>
        <button aria-label={t(`删除 ${thread.title}`, `Delete ${thread.title}`)} className="danger"
          disabled={!client.hasThreadControl || transition.isPending}
          onClick={() => setDeleteCandidate(thread)} type="button"><Trash2 aria-hidden="true" size={15} />{t("删除", "Delete")}</button>
      </article>)}</div>
    {query.hasNextPage && <button className="v2-load-history" disabled={query.isFetchingNextPage}
      onClick={() => void query.fetchNextPage()} type="button">
      {query.isFetchingNextPage ? t("正在加载…", "Loading…") : t("加载更早归档", "Load older archives")}</button>}
    {transition.isError && <p className="v2-inline-error" role="alert">{transition.error instanceof Error
      ? transition.error.message : t("更新归档状态失败", "Unable to update archive status")}</p>}
    <V2ConfirmDialog busy={transition.isPending} confirmLabel={t("删除", "Delete")} danger
      description={t("此对话会从产品列表中删除；底层审计记录仍按项目保留策略保存。", "This conversation will be removed from the app list. Audit records are retained under the project's retention policy.")}
      onCancel={() => setDeleteCandidate(null)} onConfirm={() => deleteCandidate &&
        transition.mutate({ thread: deleteCandidate, action: "delete" })}
      open={Boolean(deleteCandidate)} title={t("删除已归档的聊天", "Delete archived chat")} />
  </>;
}

function PlaceholderSettings({ section, onOpenLegacy }: {
  section: V2SettingsSection;
  onOpenLegacy: (returnFocus: HTMLElement) => void;
}) {
  const { t } = useLocale();
  const titles: Partial<Record<V2SettingsSection, string>> = {
    voice: t("语音", "Voice"), keyboard: t("键盘快捷键", "Keyboard shortcuts"), plugins: t("插件", "Plugins"),
    browser: t("浏览器", "Browser"), hooks: t("钩子", "Hooks"), git: "Git", environment: t("环境", "Environment"), worktrees: "Worktrees",
  };
  return <><h1>{titles[section] ?? t("设置", "Settings")}</h1><section className="v2-settings-section">
    <div className="v2-settings-card v2-settings-placeholder"><Monitor aria-hidden="true" size={22} />
      <strong>{t(`打开${titles[section] ?? "高级"}工具`, `Open ${titles[section] ?? "advanced"} tools`)}</strong>
      <p>{t("进入 Inspector，查看详细设置和运行记录。", "Open Inspector to view detailed settings and run records.")}</p>
      <button onClick={(event) => onOpenLegacy(event.currentTarget)}
        type="button">{t("在 Inspector 中打开", "Open in Inspector")}</button></div>
  </section></>;
}

export function V2Settings({ client, section, threadID, workspaces, onSelectSection,
  onOpenInspector, onOpenThread, prepareModelForDraft = false, modelSetupToken = "",
  onModelReady, onOpenTask, sourceRunID, onOpenGithubReview, draftWorkspaceID, draftPhase, draftBudget,
  onDraftBudgetChange, onDraftValidityChange, draftProjectSkills, onDraftProjectSkillsChange,
  defaultWebAccessMode = "rules", defaultWebAccessTargets = [], onDefaultWebAccessChange,
  desktop = desktopBridgeAvailable() }: {
  client: APIClient;
  section: V2SettingsSection;
  threadID: string;
  workspaces: WorkspaceView[];
  onSelectSection: (section: V2SettingsSection) => void;
  onOpenThread?: (id: string) => void;
  onOpenTask?: (workspaceID?: string) => void;
  sourceRunID?: string;
  onOpenGithubReview?: (runID: string) => void;
  draftWorkspaceID?: string;
  draftPhase?: "plan" | "deliver";
  draftBudget?: TaskBudgetSettings;
  onDraftBudgetChange?: (budget: TaskBudgetSettings | undefined) => void;
  onDraftValidityChange?: (valid: boolean) => void;
  draftProjectSkills?: ProjectSkillChoice;
  onDraftProjectSkillsChange?: (choice: ProjectSkillChoice) => void;
  defaultWebAccessMode?: WebAccessMode;
  defaultWebAccessTargets?: string[];
  onDefaultWebAccessChange?: (mode: WebAccessMode, targets: string[]) => void;
  onOpenInspector: (returnFocus?: HTMLElement | null) => void;
  prepareModelForDraft?: boolean;
  modelSetupToken?: string;
  onModelReady?: (token: string, definition: Pick<ProviderDefinitionView, "id" | "default_model">) => void;
  desktop?: boolean;
}) {
  const { t } = useLocale();
  return <main className="v2-settings-main"><div className="v2-settings-toolbar" />
    <div className="v2-settings-scroll"><div className="v2-settings-content">
      {section === "general" && <GeneralSettings onPermissions={() => onSelectSection("permissions")}
        threadID={threadID} workspaces={workspaces} />}
      {section === "configuration" && <V2WebAccessSettings mode={defaultWebAccessMode} targets={defaultWebAccessTargets}
        onChange={onDefaultWebAccessChange} hasThread={Boolean(threadID)}
        onOpenPermissions={() => onSelectSection("permissions")} onOpenEnvironment={() => onSelectSection("environment")} />}
      {section === "permissions" && <><h1>{t("权限", "Permissions")}</h1><p className="v2-settings-lead">
        {t("为当前任务选择操作确认方式。运行中提高权限会从下一次执行生效；降低权限会收紧相应操作范围。", "Choose approval behavior for the current task. Permission increases during a run apply to the next run; reductions restrict the corresponding actions.")}
      </p>
        <section className="v2-settings-section"><h2>{t("任务权限", "Task permissions")}</h2>
          <V2PermissionControl client={client} threadID={threadID} variant="settings"
            onOpenModelSettings={() => onSelectSection("models")} />
        </section>
        <V2CommandServiceSettings client={client} threadID={threadID} />
        <V2ExecutionSettings client={client} threadID={threadID} workspaces={workspaces} />
        <section className="v2-settings-section"><V2RuntimeCapabilityControl /></section></>}
      {section === "appearance" && <AppearanceSettings />}
      {section === "environment" && <><h1>{t("执行环境设置", "Execution environment settings")}</h1><p className="v2-settings-lead">{t("检测用户安装，保存下次启动配置，再回到任务选择编码环境。", "Detect installed environments, save settings for the next startup, then return to your task to choose a coding environment.")}</p>
        <Suspense fallback={<p role="status">{t("正在加载执行环境设置…", "Loading execution environment settings…")}</p>}><SandboxEnvironmentPanel client={client} />
          <VerificationSettingsPanel client={client} /></Suspense></>}
      {section === "connections" && <ConnectionsSettings client={client} threadID={threadID} sourceRunID={sourceRunID}
        onOpenGithubReview={onOpenGithubReview} onOpenTask={onOpenTask} onSelect={onSelectSection} />}
      {section === "task-configuration" && <Suspense fallback={<p role="status">{t("正在加载任务配置…", "Loading task configuration…")}</p>}>
        <TaskConfigurationSettings client={client} threadID={threadID} sourceRunID={sourceRunID} draftWorkspaceID={draftWorkspaceID} draftPhase={draftPhase}
          draftBudget={draftBudget} onDraftBudgetChange={onDraftBudgetChange} onDraftValidityChange={onDraftValidityChange}
          draftProjectSkills={draftProjectSkills} onDraftProjectSkillsChange={onDraftProjectSkillsChange} />
      </Suspense>}
      {section === "archived" && <ArchivedSettings client={client} onOpenThread={onOpenThread} />}
      {(section === "models" || section === "advanced-models") &&
        <ModelSettingsPage client={client} initialAdvancedOpen={section === "advanced-models"}
          prepareForDraft={prepareModelForDraft} setupToken={modelSetupToken}
          onModelReady={onModelReady} />}
      {(section === "extensions" || section === "plugins") && <V2ExtensionSettings client={client} onOpenTask={onOpenTask}
        threadID={threadID} workspaces={workspaces} />}
      {section === "skills" && <V2SkillSettings client={client} desktop={desktop} />}
      {section === "about" && <V2AboutSettings client={client} desktop={desktop} />}
      {(section === "shortcuts" || section === "keyboard") && <div className="v2-shared-settings">
        <ShortcutSettings /><p>{t("使用方向键与 Enter 操作当前菜单或对话框；输入普通段落时按 Enter 发送，列表和代码块中按 Enter 续行。Ctrl+Enter 发送，Shift+Enter 换行。", "Use arrow keys and Enter in menus and dialogs. Enter sends regular paragraphs and adds a line in lists and code blocks. Ctrl+Enter sends; Shift+Enter adds a line.")}</p>
      </div>}
      {section === "inspector" && <V2InspectorPreferences onOpenInspector={onOpenInspector} />}
      {!(["general", "permissions", "appearance", "archived", "models", "inspector", "extensions", "plugins",
        "skills", "advanced-models", "about", "shortcuts", "keyboard", "connections", "task-configuration", "environment", "configuration"] as V2SettingsSection[])
        .includes(section) && <PlaceholderSettings onOpenLegacy={onOpenInspector} section={section} />}
    </div></div>
  </main>;
}
