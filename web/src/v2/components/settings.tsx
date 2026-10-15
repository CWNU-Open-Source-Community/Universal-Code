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

function SettingRow({ title, detail, children }: { title: string; detail: string; children: React.ReactNode }) {
  return <div className="v2-setting-row"><div><strong>{title}</strong><span>{detail}</span></div>
    <div className="v2-setting-value">{children}</div></div>;
}

function ConnectionsSettings({ client, threadID, sourceRunID, onSelect, onOpenGithubReview, onOpenTask }: {
  client: APIClient; threadID: string; sourceRunID?: string; onSelect: (section: V2SettingsSection) => void;
  onOpenGithubReview?: (runID: string) => void; onOpenTask?: (workspaceID?: string) => void;
}) {
  const thread = useQuery({ queryKey: v2QueryKeys.thread(threadID), enabled: Boolean(threadID),
    queryFn: ({ signal }) => client.get<ThreadDetailView>(`/threads/${encodeURIComponent(threadID)}`, {}, signal) });
  const detail = !thread.isError && thread.data?.thread.id === threadID ? thread.data : undefined;
  const run = sourceRunID ? detail?.runs.find((item) => item.run.id === sourceRunID)?.run
    ?? (detail?.active_run?.id === sourceRunID ? detail.active_run : detail?.last_run?.id === sourceRunID ? detail.last_run : undefined)
    : detail?.active_run ?? detail?.last_run;
  return <><h1>连接与环境</h1><p className="v2-settings-lead">连接模型，准备项目需要的工具，然后回到任务开始工作。</p>
    <section className="v2-connection-task" aria-label="当前任务">
      <div><strong>{threadID ? detail?.thread.title || "当前任务" : "从一个任务开始"}</strong>
        <p>{threadID ? "在下面调整连接与选项，完成后继续对话。" : "打开项目，选好模型，在对话中描述你想完成的工作。"}</p></div>
      {onOpenTask && <button onClick={() => onOpenTask()} type="button">
        {threadID ? "返回任务" : "开始任务"}<ArrowRight aria-hidden="true" size={15} /></button>}
    </section>
    <nav className="v2-connection-navigation" aria-label="连接与环境设置">
      <section aria-labelledby="connection-task-preparation"><h2 id="connection-task-preparation">任务准备</h2>
        <button onClick={() => onSelect("models")} type="button"><span><strong>模型连接</strong><small>选择供应商、保存凭据并验证模型。</small></span><ArrowRight aria-hidden="true" size={16} /></button>
        <button onClick={() => onSelect("task-configuration")} type="button"><span><strong>任务预算与项目配置</strong><small>{threadID ? "查看本次执行保存的上限和项目设置。" : "设置新任务的用量上限，预览项目规则。"}</small></span><ArrowRight aria-hidden="true" size={16} /></button>
      </section>
      <section aria-labelledby="connection-task-tools"><h2 id="connection-task-tools">工具与协作</h2>
        <button onClick={() => onSelect("extensions")} type="button"><span><strong>扩展与代码智能</strong><small>添加 MCP、Plugin 和语言服务，按步骤完成接入。</small></span><ArrowRight aria-hidden="true" size={16} /></button>
        <button disabled={!run?.id || !onOpenGithubReview} onClick={() => { if (run?.id) onOpenGithubReview?.(run.id); }} type="button">
          <span><strong>GitHub 连接与审阅</strong><small>{run?.id ? "管理此任务的 GitHub 连接，查看和处理 PR。"
            : !threadID ? "从侧栏打开一个任务，即可管理它的 GitHub 连接。"
              : thread.isPending ? "正在读取任务记录…" : thread.isError ? "任务记录读取失败，请点击下方“重新读取任务”。"
                : "请返回任务，选择一条执行记录后进入 GitHub 审阅。"}</small></span><ArrowRight aria-hidden="true" size={16} /></button>
      </section>
      <section aria-labelledby="connection-task-environment"><h2 id="connection-task-environment">运行设置</h2>
        <button onClick={() => onSelect("environment")} type="button"><span><strong>执行环境设置</strong><small>检测 Local、Docker Engine 与官方 sbx，保存启用选项和固定镜像。</small></span><ArrowRight aria-hidden="true" size={16} /></button>
        <button onClick={() => onSelect("permissions")} type="button"><span><strong>任务权限与执行环境</strong><small>{threadID ? "选择操作确认方式，查看项目访问范围与执行环境。" : "查看当前环境；打开任务后可调整它的操作权限。"}</small></span><ArrowRight aria-hidden="true" size={16} /></button>
        <button onClick={() => onSelect("about")} type="button"><span><strong>应用连接与诊断</strong><small>查看版本和连接状态，处理连接问题。</small></span><ArrowRight aria-hidden="true" size={16} /></button>
      </section>
    </nav>
    {threadID && thread.isError && <button className="v2-setting-link" onClick={() => void thread.refetch()} type="button">重新读取任务</button>}
    {run && <details className="v2-connection-record"><summary>查看任务记录</summary>
      <dl><div><dt>任务 ID</dt><dd>{threadID}</dd></div><div><dt>执行 ID</dt><dd>{run.id}</dd></div></dl></details>}
  </>;
}

function FontLicenseControl() {
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
          throw new Error("许可文本格式无效");
        }
        setLicense(content);
      })
      .catch((reason: unknown) => {
        if (controller.signal.aborted) return;
        setError(reason instanceof Error ? reason.message : "无法读取许可文本");
      });
    return () => controller.abort();
  }, [license, open]);

  return <>
    <button className="v2-setting-link" onClick={() => setOpen(true)} ref={triggerRef}
      type="button">查看许可</button>
    {open && createPortal(<div className="v2-overlay" onMouseDown={(event) => {
      if (event.target === event.currentTarget) close();
    }} role="presentation">
      <section aria-labelledby="v2-font-license-title" aria-modal="true"
        className="v2-dialog v2-license-dialog" ref={dialogRef} role="dialog" tabIndex={-1}>
        <header><span><BookOpen aria-hidden="true" size={17} /></span>
          <h2 id="v2-font-license-title">HarmonyOS Sans Fonts 许可</h2>
          <button aria-label="关闭许可" onClick={close} type="button"><X aria-hidden="true" size={16} /></button>
        </header>
        <div aria-live="polite" className="v2-license-content">
          {!license && !error && <p>正在读取随软件发布的许可文本…</p>}
          {error && <p role="alert">无法读取许可文本：{error}</p>}
          {license && <pre>{license}</pre>}
        </div>
        <footer><button onClick={close} type="button">关闭</button></footer>
      </section>
    </div>, document.body)}
  </>;
}

function GeneralSettings({ threadID, workspaces, onPermissions }: {
  threadID: string;
  workspaces: WorkspaceView[];
  onPermissions: () => void;
}) {
  const { locale, setLocale } = useLocale();
  return <>
    <h1>常规</h1>
    <p className="v2-settings-lead">调整应用偏好。项目访问范围和执行环境可在「当前任务权限」中设置。</p>
    <section className="v2-settings-section"><h2>常规</h2>
      <div className="v2-settings-card">
        <SettingRow detail="新对话中选择项目；桌面应用可直接打开本机文件夹" title="项目">
          <span>{workspaces.length} 个已加载项目</span>
        </SettingRow>
        <SettingRow detail="主界面使用简体中文。下方语言选项用于已提供双语内容的高级面板。" title="语言">
          <div className="v2-setting-segmented" role="group" aria-label="高级面板语言">
            <button aria-pressed={locale === "zh-CN"} onClick={() => setLocale("zh-CN")} type="button">中文</button>
            <button aria-pressed={locale === "en-US"} onClick={() => setLocale("en-US")} type="button">English（部分）</button>
          </div>
        </SettingRow>
        <SettingRow detail={threadID ? "为当前打开的任务选择操作确认方式和访问范围。" : "先打开一个对话，再查看它的任务权限。"} title="当前任务权限">
          <button className="v2-setting-link" aria-label="管理当前任务权限" disabled={!threadID}
            onClick={onPermissions} type="button">管理</button>
        </SettingRow>
        <SettingRow detail="中文界面使用 HarmonyOS Sans Fonts；完整许可文本随软件发布。" title="第三方字体">
          <FontLicenseControl />
        </SettingRow>
      </div>
    </section>
  </>;
}

function AppearanceSettings() {
  const [theme, setTheme] = useState<PrayuTheme>(() => readPrayuTheme());
  const select = (next: PrayuTheme) => { setTheme(next); applyPrayuTheme(next); };
  return <><h1>外观</h1><section className="v2-settings-section"><h2>主题与材质</h2>
    <div className="v2-settings-card v2-appearance-card">
      <div className="v2-theme-options" role="group" aria-label="应用主题">
        {(["light", "dark", "glass"] as const).map((option) => <button aria-pressed={theme === option}
          key={option} onClick={() => select(option)} type="button">
          <span aria-hidden="true" className={`v2-theme-preview theme-${option}`} data-prayu-theme={option}><i /><i /></span>
          <strong>{option === "light" ? "浅色" : option === "dark" ? "深色" : "雾透"}</strong>
        </button>)}</div>
      <p>雾透让窗口背景透出柔和的灰蓝层次；深色使用深灰玻璃材质。开启系统的「降低透明度」后，界面会使用清晰的实色背景。</p>
    </div></section></>;
}

function ModelSettingsPage({ client, initialAdvancedOpen = false, prepareForDraft = false,
  setupToken = "", onModelReady }: {
  client: APIClient; initialAdvancedOpen?: boolean; prepareForDraft?: boolean;
  setupToken?: string;
  onModelReady?: (token: string, definition: Pick<ProviderDefinitionView, "id" | "default_model">) => void;
}) {
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
        高级模型设置：默认模型、能力诊断与费用预算
      </button>
      {advancedOpen && <><p>新对话可在输入区直接选模型；这里管理未单独选择时的默认值。
        价格表只用于启用金额上限的任务。</p>
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
  return <><h1>已归档的聊天</h1><p className="v2-settings-lead">在这里查看归档对话及其记录。选择「取消归档」可将对话放回侧栏。</p>
    <label className="v2-archive-search"><Search aria-hidden="true" size={15} />
      <input aria-label="搜索已归档的聊天" onChange={(event) => setSearch(event.target.value)}
        placeholder="搜索已归档的聊天" type="search" value={search} /></label>
    <p className="v2-history-scope">按标题搜索当前已加载的 {loaded.length} 条归档对话。
      {query.isLoading ? "正在读取列表。" : query.isError ? "本次加载未完成，请重试。"
        : query.hasNextPage ? "可以继续加载更早记录。" : "当前列表已加载完毕。"}</p>
    <div className="v2-archive-list">
      {query.isLoading && <p>正在加载…</p>}
      {query.isError && <p role="alert">无法读取归档列表。<button onClick={() => void (query.isFetchNextPageError
        ? query.fetchNextPage() : query.refetch())} type="button">重试归档列表</button></p>}
      {!query.isLoading && !query.isError && threads.length === 0 && <div className="v2-archive-empty">
        {normalized ? "已加载的标题中没有匹配项" : "暂无已归档的聊天"}</div>}
      {threads.map((thread) => <article key={thread.id}><div><strong>{onOpenThread
        ? <button className="link-button" aria-label={`打开 ${thread.title}`} onClick={() => onOpenThread(thread.id)}
          type="button">{thread.title}</button> : thread.title}</strong>
        <span>{thread.archived_at ? new Date(thread.archived_at).toLocaleString() : "已归档"}</span></div>
        <button disabled={!client.hasThreadControl || transition.isPending}
          onClick={() => transition.mutate({ thread, action: "restore" })} type="button">
          <ArchiveRestore aria-hidden="true" size={15} />取消归档</button>
        <button aria-label={`删除 ${thread.title}`} className="danger"
          disabled={!client.hasThreadControl || transition.isPending}
          onClick={() => setDeleteCandidate(thread)} type="button"><Trash2 aria-hidden="true" size={15} />删除</button>
      </article>)}</div>
    {query.hasNextPage && <button className="v2-load-history" disabled={query.isFetchingNextPage}
      onClick={() => void query.fetchNextPage()} type="button">
      {query.isFetchingNextPage ? "正在加载…" : "加载更早归档"}</button>}
    {transition.isError && <p className="v2-inline-error" role="alert">{transition.error instanceof Error
      ? transition.error.message : "更新归档状态失败"}</p>}
    <V2ConfirmDialog busy={transition.isPending} confirmLabel="删除" danger
      description="此对话会从产品列表中删除；底层审计记录仍按项目保留策略保存。"
      onCancel={() => setDeleteCandidate(null)} onConfirm={() => deleteCandidate &&
        transition.mutate({ thread: deleteCandidate, action: "delete" })}
      open={Boolean(deleteCandidate)} title="删除已归档的聊天" />
  </>;
}

function PlaceholderSettings({ section, onOpenLegacy }: {
  section: V2SettingsSection;
  onOpenLegacy: (returnFocus: HTMLElement) => void;
}) {
  const titles: Partial<Record<V2SettingsSection, string>> = {
    voice: "语音", keyboard: "键盘快捷键", plugins: "插件",
    browser: "浏览器", hooks: "钩子", git: "Git", environment: "环境", worktrees: "Worktrees",
  };
  return <><h1>{titles[section] ?? "设置"}</h1><section className="v2-settings-section">
    <div className="v2-settings-card v2-settings-placeholder"><Monitor aria-hidden="true" size={22} />
      <strong>打开{titles[section] ?? "高级"}工具</strong>
      <p>进入 Inspector，查看详细设置和运行记录。</p>
      <button onClick={(event) => onOpenLegacy(event.currentTarget)}
        type="button">在 Inspector 中打开</button></div>
  </section></>;
}

export function V2Settings({ client, section, threadID, workspaces, onSelectSection,
  onOpenInspector, onOpenThread, prepareModelForDraft = false, modelSetupToken = "",
  onModelReady, onOpenTask, sourceRunID, onOpenGithubReview, draftWorkspaceID, draftBudget,
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
  return <main className="v2-settings-main"><div className="v2-settings-toolbar" />
    <div className="v2-settings-scroll"><div className="v2-settings-content">
      {section === "general" && <GeneralSettings onPermissions={() => onSelectSection("permissions")}
        threadID={threadID} workspaces={workspaces} />}
      {section === "configuration" && <V2WebAccessSettings mode={defaultWebAccessMode} targets={defaultWebAccessTargets}
        onChange={onDefaultWebAccessChange} hasThread={Boolean(threadID)}
        onOpenPermissions={() => onSelectSection("permissions")} onOpenEnvironment={() => onSelectSection("environment")} />}
      {section === "permissions" && <><h1>权限</h1><p className="v2-settings-lead">
        为当前任务选择操作确认方式。运行中提高权限会从下一次执行生效；降低权限会收紧相应操作范围。
      </p>
        <section className="v2-settings-section"><h2>任务权限</h2>
          <V2PermissionControl client={client} threadID={threadID} variant="settings"
            onOpenModelSettings={() => onSelectSection("models")} />
        </section>
        <V2ExecutionSettings client={client} threadID={threadID} workspaces={workspaces} />
        <section className="v2-settings-section"><V2RuntimeCapabilityControl /></section></>}
      {section === "appearance" && <AppearanceSettings />}
      {section === "environment" && <><h1>执行环境设置</h1><p className="v2-settings-lead">检测用户安装，保存下次启动配置，再回到任务选择编码环境。</p>
        <Suspense fallback={<p role="status">正在加载执行环境设置…</p>}><SandboxEnvironmentPanel client={client} /></Suspense></>}
      {section === "connections" && <ConnectionsSettings client={client} threadID={threadID} sourceRunID={sourceRunID}
        onOpenGithubReview={onOpenGithubReview} onOpenTask={onOpenTask} onSelect={onSelectSection} />}
      {section === "task-configuration" && <Suspense fallback={<p role="status">正在加载任务配置…</p>}>
        <TaskConfigurationSettings client={client} threadID={threadID} sourceRunID={sourceRunID} draftWorkspaceID={draftWorkspaceID}
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
        <ShortcutSettings /><p>使用方向键与 Enter 操作当前菜单或对话框；在输入框中按 Enter 发送、Shift+Enter 换行。</p>
      </div>}
      {section === "inspector" && <V2InspectorPreferences onOpenInspector={onOpenInspector} />}
      {!(["general", "permissions", "appearance", "archived", "models", "inspector", "extensions", "plugins",
        "skills", "advanced-models", "about", "shortcuts", "keyboard", "connections", "task-configuration", "environment", "configuration"] as V2SettingsSection[])
        .includes(section) && <PlaceholderSettings onOpenLegacy={onOpenInspector} section={section} />}
    </div></div>
  </main>;
}
