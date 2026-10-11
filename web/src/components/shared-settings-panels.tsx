import { useRef, useState, type ReactNode } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Ban, Cpu, LoaderCircle, PackageSearch, PlugZap, RefreshCw } from "lucide-react";
import type { APIClient } from "../api/client";
import type { CodeIntelQualificationView, CodeIntelServerView, ExtensionMCPServerView,
  ExtensionPluginInstallationView, HealthView } from "../api/types";
import { useLocale } from "../lib/locale";
import { HookDiagnostics, PluginLifecycleControls } from "./plugin-lifecycle";
import { PrayuBrand } from "./prayu-brand";
import { MCPRegistrationForm, MCPReviewControls, PluginImportForm, PluginReviewControls } from "./extension-onboarding";
import { LSPConfigurationCard, LSPConfigurationForm } from "./lsp-onboarding";

export { readDensity, persistDensity, type Density } from "../lib/ui-density";

export function WebSkillInstall({ client }: { client: APIClient }) {
  const { t } = useLocale();
  const inputRef = useRef<HTMLInputElement>(null);
  const operationKey = useRef("");
  const [selected, setSelected] = useState<File | null>(null);
  const [confirmed, setConfirmed] = useState(false);
  const [message, setMessage] = useState("");
  const install = useMutation({
    mutationFn: (file: File) => file.arrayBuffer().then((buffer) => {
      let binary = "";
      const bytes = new Uint8Array(buffer);
      const chunk = 0x8000;
      for (let i = 0; i < bytes.length; i += chunk) {
        binary += String.fromCharCode(...bytes.subarray(i, i + chunk));
      }
      return client.installSkillPackage({
        version: "skill_package_installation.v1", archive_base64: btoa(binary),
        surface: "code", confirm_untrusted: true,
      }, operationKey.current);
    }),
    onSuccess: () => setMessage(t("Skill 包已安装", "Skill package installed")),
  });
  return <div className="settings-web-skill">
    <input accept="application/zip,.zip" aria-label="选择 Skill ZIP 包" disabled={install.isPending}
      hidden ref={inputRef} type="file"
      onChange={(event) => {
        const file = event.target.files?.[0];
        if (file) {
          setSelected(file);
          setConfirmed(false);
          setMessage("");
          install.reset();
          operationKey.current = `web-skill-install-${globalThis.crypto.randomUUID()}`;
        }
        event.currentTarget.value = "";
      }} />
    <button className="settings-action" disabled={install.isPending}
      onClick={() => inputRef.current?.click()} type="button">
      {install.isPending ? <LoaderCircle aria-hidden="true" className="spin" size={15} /> : <PackageSearch aria-hidden="true" size={15} />}
      {t("选择 Skill ZIP 包", "Choose Skill ZIP package")}
    </button>
    {selected && <div className="shared-skill-confirmation">
      <p>{selected.name}</p>
      <label><input checked={confirmed} disabled={install.isPending || install.isSuccess}
        onChange={(event) => setConfirmed(event.target.checked)} type="checkbox" />
        {t("确认将此包以不受信任状态登记到 Code，执行时遵循任务权限与审批", "Register this package as untrusted in Code; execution follows task permissions and approvals")}</label>
      <button className="settings-action" disabled={!client.hasSkillInstallation || !confirmed ||
        install.isPending || install.isSuccess} onClick={() => install.mutate(selected)} type="button">
        {t("安装 Skill 包", "Install Skill package")}</button>
    </div>}
    {message && <span className="projection-placeholder">{message}</span>}
    {install.error && <span className="inline-warning">{install.error instanceof Error ? install.error.message : t("安装失败", "Install failed")}</span>}
  </div>;
}

export function ShortcutSettings() {
  const { t } = useLocale();
  return <section className="settings-page-section">
    <h1>{t("键盘快捷键", "Keyboard shortcuts")}</h1>
    <dl className="shortcut-list">
      <div><dt>{t("Inspector 命令面板", "Inspector command palette")}</dt><dd><kbd>Ctrl</kbd><kbd>K</kbd></dd></div>
      <div><dt>{t("关闭对话框或预览", "Close dialog or preview")}</dt><dd><kbd>Esc</kbd></dd></div>
      <div><dt>{t("选择上一项", "Select previous item")}</dt><dd><kbd>↑</kbd></dd></div>
      <div><dt>{t("选择下一项", "Select next item")}</dt><dd><kbd>↓</kbd></dd></div>
      <div><dt>{t("确认当前操作", "Confirm current action")}</dt><dd><kbd>Enter</kbd></dd></div>
    </dl>
  </section>;
}

export function AboutSettings({ desktop, health }: { desktop: boolean; health: HealthView | null }) {
  const { t } = useLocale();
  return <section className="settings-page-section about-prayu">
    <PrayuBrand className="about-mark" variant="icon" />
    <h1>Universal-Code</h1>
    <p>{t("本地优先的 AI Agent 工作台", "Local-first AI Agent Workbench")}</p>
    <dl className="settings-row-list">
      <div><dt>{t("应用版本", "Application version")}</dt><dd>{health?.app_version ?? "dev"}</dd></div>
      <div><dt>API 协议</dt><dd>{health?.api_version ?? "api.v1"}</dd></div>
      <div><dt>{t("数据库", "Database")}</dt><dd>schema v{health?.schema_version ?? "-"}</dd></div>
      <div><dt>{t("运行界面", "Surface")}</dt><dd>{desktop ? t("桌面端", "Desktop") : t("网页端", "Web")}</dd></div>
    </dl>
  </section>;
}

type ExtensionAction =
  | { kind: "refresh-mcp"; server: ExtensionMCPServerView }
  | { kind: "disable-mcp"; server: ExtensionMCPServerView }
  | { kind: "disable-plugin"; installation: ExtensionPluginInstallationView };

export function ExtensionSettings({ client, selectedRunID, selectedWorkspaceID = "", onOpenTask }: {
  client: APIClient;
  selectedRunID: string;
  selectedWorkspaceID?: string;
  onOpenTask?: (workspaceID?: string) => void;
}) {
  const { t } = useLocale();
  const queryClient = useQueryClient();
  const inventory = useQuery({
    queryKey: ["extensions", selectedRunID, selectedWorkspaceID],
    queryFn: ({ signal }) => selectedWorkspaceID ? client.extensionInventory(selectedRunID, signal, selectedWorkspaceID)
      : client.extensionInventory(selectedRunID, signal),
  });
  const codeIntelWorkspaceID = selectedWorkspaceID || (selectedRunID ? (inventory.data?.workspace_id ?? "") : "");
  const codeIntel = useQuery({
    queryKey: ["code-intel", codeIntelWorkspaceID],
    queryFn: ({ signal }) => client.codeIntelInventory(codeIntelWorkspaceID, signal),
    enabled: selectedRunID === "" || codeIntelWorkspaceID !== "",
  });
  const action = useMutation<unknown, Error, ExtensionAction>({
    mutationFn: (value: ExtensionAction) => {
      if (value.kind === "refresh-mcp") {
        return client.refreshMCPServer(value.server.id);
      }
      if (value.kind === "disable-mcp") {
        return client.reviewMCPServer(value.server.id, {
          version: "extension-control.v1", action: "disable",
          expected_descriptor_fingerprint: value.server.descriptor_fingerprint,
        });
      }
      return client.reviewPluginInstallation(value.installation.id, {
        version: "extension-control.v1", action: "disable",
        expected_package_fingerprint: value.installation.package_fingerprint,
        expected_generation: value.installation.generation,
        confirm_untrusted: false,
      });
    },
    onSuccess: () => queryClient.invalidateQueries({ queryKey: ["extensions"] }),
  });
  const qualificationOnly = codeIntel.data?.qualifications.filter((qualification) =>
    !codeIntel.data.servers.some((server) => server.workspace_id === qualification.workspace_id &&
      server.server_id === qualification.server_id)) ?? [];
  const codeIntelCount = new Set([
    ...(codeIntel.data?.servers ?? []).map((item) => `${item.workspace_id}/${item.server_id}`),
    ...(codeIntel.data?.qualifications ?? []).map((item) => `${item.workspace_id}/${item.server_id}`),
    ...(codeIntel.data?.configurations ?? []).map((item) => `${item.workspace_id}/${item.server_id}`),
  ]).size;
  const configurationWorkspaceID = selectedWorkspaceID || (selectedRunID ? inventory.data?.workspace_id ?? "" : "");
  const onboarding = inventory.data?.onboarding;
  return <section className="settings-page-section extension-settings">
    <header className="extension-heading">
      <div>
        <h1>{t("Code Intel、MCP 与 Plugin", "Code Intel, MCP and Plugins")}</h1>
        <p>{t("选择一种接入方式，按步骤核对来源并启用。每条记录显示当前阶段和下一步；真实查询与工具调用结果在下方或任务中查看。",
          "Choose an integration and follow its review steps. Each record shows its current stage and next action. Inspect query and tool call results below or in your task.")}</p>
      </div>
      <button className="settings-action" disabled={inventory.isFetching || codeIntel.isFetching}
        onClick={() => { void inventory.refetch(); if (!selectedRunID || codeIntelWorkspaceID) void codeIntel.refetch(); }} type="button">
        <RefreshCw aria-hidden="true"
          className={inventory.isFetching || codeIntel.isFetching ? "spin" : ""} size={15} />
        {t("刷新", "Refresh")}
      </button>
    </header>
    {inventory.error && <p className="inline-warning">{inventory.error instanceof Error ?
      inventory.error.message : t("扩展状态读取失败", "Failed to read extension state")}</p>}
    {action.error && <p className="inline-warning">{action.error instanceof Error ?
      action.error.message : t("扩展操作失败", "Extension action failed")}</p>}
    {codeIntel.error && <p className="inline-warning">{codeIntel.error instanceof Error ?
      codeIntel.error.message : t("语言服务器状态读取失败", "Failed to read language-server state")}</p>}
    {(inventory.isLoading || codeIntel.isLoading) && <p role="status">{t("正在读取扩展状态与接入能力…", "Loading extension state and onboarding capabilities…")}</p>}
    <ExtensionCollection title="Code Intel / LSP" count={codeIntelCount}>
      <LSPConfigurationForm capabilityKnown={Boolean(inventory.data)} client={client} enabled={Boolean(client.hasExtensionControl && onboarding?.lsp_configuration)}
        workspaceID={configurationWorkspaceID} />
      {codeIntel.data?.configurations?.map((configuration) => <LSPConfigurationCard client={client}
        enabled={Boolean(client.hasExtensionControl && onboarding?.lsp_configuration)}
        configuration={configuration} key={`${configuration.workspace_id}/${configuration.server_id}/${configuration.descriptor_fingerprint}/${configuration.review_state}`} />)}
      {codeIntel.data?.servers.map((server) => <CodeIntelServerCard
        key={`${server.workspace_id}/${server.server_id}`} server={server}
        qualification={codeIntel.data.qualifications.find((item) =>
          item.workspace_id === server.workspace_id && item.server_id === server.server_id)} />)}
      {qualificationOnly.map((qualification) => <CodeIntelQualificationCard
        key={`${qualification.workspace_id}/${qualification.server_id}/qualification`}
        qualification={qualification} />)}
      {codeIntel.data && codeIntelCount === 0 && !codeIntel.data.configurations?.length &&
        <ExtensionEmpty>{t("准备已安装的语言服务器，展开“配置本地 LSP”登记并审查，再用一个文件测试符号查询。",
          "Prepare an installed language server. Open Configure local LSP to register and review it, then test symbols with one file.")}</ExtensionEmpty>}
    </ExtensionCollection>
    <ExtensionCollection title="MCP Client" count={inventory.data?.mcp_servers.length ?? 0}>
      <MCPRegistrationForm capabilityKnown={Boolean(inventory.data)} client={client} enabled={Boolean(client.hasExtensionControl && onboarding?.mcp_registration)}
        runID={selectedRunID} workspaceID={configurationWorkspaceID} />
      {inventory.data?.mcp_servers.map((server) => <MCPServerCard action={action}
        client={client} key={server.id} onOpenTask={onOpenTask} server={server} credentialCapability={onboarding?.mcp_credentials} />)}
      {inventory.data && inventory.data.mcp_servers.length === 0 &&
        <ExtensionEmpty>{selectedRunID ?
          t("此任务范围尚无 MCP Server。展开“登记 MCP Server”，填写地址后逐步发现和启用工具。", "This task scope has no MCP server yet. Open Register MCP server and enter its address, then discover and enable its tools.") :
          t("此范围尚未登记 MCP Server。先选择工作区并登记，再单独审查。", "No MCP server registered in this scope. Select a workspace, register, then review it.")}</ExtensionEmpty>}
    </ExtensionCollection>
    <ExtensionCollection title={t("MCP 实际调用记录", "Actual MCP calls")} count={inventory.data?.mcp_calls.length ?? 0}>
      <div className="extension-call-list">
        {inventory.data?.mcp_calls.map((call) => <article key={call.id}>
          <strong>{call.server_id} · {call.tool_name}</strong>
          <p>{t("调用状态", "Call status")}: {call.status}{call.error_code ? ` · ${call.error_code}` : ""}</p>
          <p>Run: {call.run_id} · {t("完成时间", "Completed")}: {call.completed_at}</p>
          <p>{t("结果大小", "Result size")}: {call.result_bytes} bytes · {call.truncated ? t("已截断", "Truncated") : t("未截断", "Not truncated")}</p>
          <Fingerprint label={t("本次能力指纹", "Call capability fingerprint")} value={call.capability_fingerprint} />
          <p>{t("在任务消息和证据中查看具体返回内容。此处保留调用状态、时间与能力指纹供核对。", "Read returned content in task messages and evidence. Use the call status, time, and capability fingerprint here to verify the record.")}</p>
        </article>)}
        {inventory.data && inventory.data.mcp_calls.length === 0 && <ExtensionEmpty>{selectedRunID
          ? t("当前任务尚无 MCP 调用记录。启用工具后，使用“生成首次调用任务草稿”在任务中发起调用，再刷新此处查看结果。", "This task has no MCP call records yet. Enable tools, use Prepare first-call task draft to make a call in the task, then refresh here to inspect the result.")
          : t("打开一个任务以查看该执行的 MCP 调用记录。", "Open a task to inspect MCP calls for that execution.")}</ExtensionEmpty>}
      </div>
    </ExtensionCollection>
    <ExtensionCollection title="Plugin" count={inventory.data?.plugins.length ?? 0}>
      <PluginImportForm capabilityKnown={Boolean(inventory.data)} client={client} enabled={Boolean(client.hasExtensionControl && onboarding?.plugin_import)} />
      {inventory.data?.plugins.map((installation) => <PluginCard action={action}
        client={client} installation={installation} key={installation.id} lifecycle={onboarding?.plugin_lifecycle} />)}
      {inventory.data && inventory.data.plugins.length === 0 &&
        <ExtensionEmpty>{t("选择 plugin.v1 ZIP 包开始接入。导入后核对来源与指纹，审查并启用所需能力。", "Choose a plugin.v1 ZIP to get started. After import, inspect its source and fingerprint, then review and enable the capabilities you need.")}</ExtensionEmpty>}
    </ExtensionCollection>
    {onboarding?.hook_diagnostics && <HookDiagnostics client={client} runID={selectedRunID} workspaceID={configurationWorkspaceID}
      key={`${selectedRunID}/${configurationWorkspaceID}`} />}
  </section>;
}

function CodeIntelQualificationCard({ qualification }: {
  qualification: CodeIntelQualificationView;
}) {
  const { t } = useLocale();
  return <article className="extension-card code-intel-card">
    <header><div><Cpu aria-hidden="true" size={17} />
      <div><strong>{qualification.server_id}</strong>
        <span>{qualification.workspace_id}</span></div></div>
      <ExtensionState state={qualification.health} /></header>
    <dl className="extension-facts">
      <div><dt>{t("资格", "Qualification")}</dt><dd>{qualification.eligible ?
        t("已通过", "Eligible") : t("未通过", "Ineligible")}</dd></div>
      <div><dt>{t("人工审查", "Human review")}</dt><dd>{qualification.reviewed ?
        t("已完成", "Reviewed") : t("未完成", "Pending")}</dd></div>
      <div><dt>{t("可执行文件哈希", "Executable hash")}</dt>
        <dd>{qualification.executable_hash_matched ?
          t("匹配", "Matched") : t("不匹配", "Mismatch")}</dd></div>
      <div><dt>{t("最小环境", "Minimal environment")}</dt>
        <dd>{qualification.minimal_environment ? t("是", "Yes") : t("否", "No")}</dd></div>
    </dl>
    <Fingerprint label={t("描述符指纹", "Descriptor fingerprint")}
      value={qualification.descriptor_fingerprint} />
    {qualification.reason && <p className="inline-warning code-intel-error">
      {qualification.reason}</p>}
  </article>;
}

function CodeIntelServerCard({ qualification, server }: {
  qualification?: CodeIntelQualificationView;
  server: CodeIntelServerView;
}) {
  const { t } = useLocale();
  const enabledCapabilities = Object.values(server.capabilities)
    .filter((enabled) => enabled).length;
  return <article className="extension-card code-intel-card">
    <header><div><Cpu aria-hidden="true" size={17} />
      <div><strong>{server.server_name}</strong>
        <span>{server.workspace_id} · {server.server_id}</span></div></div>
      <ExtensionState state={server.health} /></header>
    <dl className="extension-facts">
      <div><dt>{t("语言", "Languages")}</dt><dd>{server.languages.join(", ")}</dd></div>
      <div><dt>{t("能力", "Capabilities")}</dt><dd>{enabledCapabilities} / 10</dd></div>
      <div><dt>{t("来源", "Source")}</dt><dd>{server.source_kind}</dd></div>
      <div><dt>{t("版本", "Version")}</dt><dd>{server.server_version || "—"}</dd></div>
      <div><dt>{t("代次", "Generation")}</dt>
        <dd>{server.generation ? server.generation.slice(0, 12) : "—"}</dd></div>
      <div><dt>{t("模型工具", "Model tools")}</dt>
        <dd>{server.model_visible_tools.length}</dd></div>
      <div><dt>{t("资格", "Qualification")}</dt><dd>{qualification ?
        (qualification.eligible ? t("已通过", "Eligible") : t("未通过", "Ineligible")) :
        t("未检查", "Not checked")}</dd></div>
      {qualification && <div><dt>{t("审查与哈希", "Review and hash")}</dt>
        <dd>{qualification.reviewed && qualification.executable_hash_matched ?
          t("已固定", "Pinned") : t("不完整", "Incomplete")}</dd></div>}
    </dl>
    <p className="extension-target" title={server.source_label}>{server.source_label}</p>
    <Fingerprint label={t("能力指纹", "Capability fingerprint")}
      value={server.capability_fingerprint || server.descriptor_fingerprint} />
    {server.model_visible_tools.length > 0 && <p className="code-intel-tools"
      title={server.model_visible_tools.join(", ")}>{server.model_visible_tools.join(", ")}</p>}
    {server.last_error && <p className="inline-warning code-intel-error">{server.last_error}</p>}
    {qualification?.reason && <p className="inline-warning code-intel-error">
      {qualification.reason}</p>}
  </article>;
}

function ExtensionCollection({ title, count, children }: {
  title: string; count: number; children: ReactNode;
}) {
  return <section className="extension-collection">
    <header><h2>{title}</h2><span>{count}</span></header>
    <div className="extension-card-list">{children}</div>
  </section>;
}

function MCPServerCard({ action, client, server, onOpenTask, credentialCapability }: {
  action: { isPending: boolean; mutate: (value: ExtensionAction) => void };
  client: APIClient;
  server: ExtensionMCPServerView;
  onOpenTask?: (workspaceID?: string) => void;
  credentialCapability?: boolean;
}) {
  const { t } = useLocale();
  const refreshable = ["discovery_approved", "capabilities_pending", "enabled",
    "quarantined"].includes(server.state);
  const disableable = !["disabled", "revoked"].includes(server.state);
  const target = server.native_source
    ? `${t("插件组件", "Plugin component")}: ${server.native_source.component_id}` : server.target;
  return <article className="extension-card">
    <header><div><PlugZap aria-hidden="true" size={17} />
      <div><strong>{server.name}</strong><span>{server.id}</span></div></div>
      <ExtensionState state={server.state} /></header>
    <dl className="extension-facts">
      <div><dt>{t("传输", "Transport")}</dt><dd>{server.transport}</dd></div>
      <div><dt>{t("健康", "Health")}</dt><dd>{server.health}</dd></div>
      <div><dt>{t("范围", "Scope")}</dt><dd>{server.scope}</dd></div>
      <div><dt>{t("工具", "Tools")}</dt><dd>{server.capabilities.tools.length}</dd></div>
      <div><dt>{t("凭据引用", "Credential ref")}</dt><dd>{server.credential_ref || "—"}</dd></div>
      <div><dt>{t("来源", "Source")}</dt><dd>{server.source.kind}</dd></div>
    </dl>
    <p className="extension-target" title={target}>{target}</p>
    <p className="extension-target" title={server.source.uri}>{server.source.uri}</p>
    <p>{t("所属工作区", "Workspace")}: {server.workspace_id}{server.run_id ? ` · Run: ${server.run_id}` : ""}</p>
    <Fingerprint label={t("描述符指纹", "Descriptor fingerprint")} value={server.descriptor_fingerprint} />
    <Fingerprint label={t("能力指纹", "Capability fingerprint")}
      value={server.capabilities.fingerprint || ""} />
    <MCPReviewControls client={client} key={`${server.state}/${server.descriptor_fingerprint}/${server.capabilities.fingerprint}`}
      onOpenTask={onOpenTask} server={server} credentialCapability={credentialCapability} />
    <div className="extension-actions">
      <button className="settings-action" disabled={!client.hasExtensionControl ||
        !refreshable || action.isPending}
        onClick={() => action.mutate({ kind: "refresh-mcp", server })} type="button">
        <RefreshCw aria-hidden="true" size={14} />{t("重新发现", "Rediscover")}
      </button>
      <button className="settings-action danger" disabled={!client.hasExtensionControl ||
        !disableable || action.isPending}
        onClick={() => action.mutate({ kind: "disable-mcp", server })} type="button">
        <Ban aria-hidden="true" size={14} />{t("立即关闭", "Disable now")}
      </button>
    </div>
  </article>;
}

function PluginCard({ action, client, installation, lifecycle }: {
  action: { isPending: boolean; mutate: (value: ExtensionAction) => void };
  client: APIClient;
  installation: ExtensionPluginInstallationView;
  lifecycle?: boolean;
}) {
  const { t } = useLocale();
  const disableable = !["disabled", "revoked", "rolled_back"].includes(installation.state);
  return <article className="extension-card">
    <header><div><PackageSearch aria-hidden="true" size={17} />
      <div><strong>{installation.manifest.name}</strong>
        <span>{[installation.manifest.publisher, installation.manifest.version ?
          `v${installation.manifest.version}` : ""].filter(Boolean).join(" · ") ||
          t("未提供作者或版本", "No author or version provided")}</span></div></div>
      <ExtensionState state={installation.state} /></header>
    <dl className="extension-facts">
      <div><dt>{t("签名", "Signature")}</dt><dd>{installation.signature_valid ?
        t("有效", "Valid") : installation.signature_present ? t("无效", "Invalid") :
          t("未签名", "Unsigned")}</dd></div>
      <div><dt>{t("来源", "Source")}</dt><dd>{installation.source.kind}</dd></div>
      <div><dt>{t("已启用", "Enabled")}</dt>
        <dd>{installation.enabled_capabilities.join(", ") || "—"}</dd></div>
      <div><dt>{t("代次", "Generation")}</dt><dd>{installation.generation}</dd></div>
    </dl>
    <p className="extension-target" title={installation.source.uri}>{installation.source.uri}</p>
    <Fingerprint label={t("包指纹", "Package fingerprint")}
      value={installation.package_fingerprint} />
    <p>{t("范围", "Scope")}: {t("本机安装；任务按已启用贡献加载", "Local installation; tasks load enabled contributions")}</p>
    <PluginReviewControls client={client} installation={installation} key={`${installation.state}/${installation.generation}`} />
    {lifecycle && <PluginLifecycleControls client={client} installationID={installation.id} />}
    <div className="extension-actions">
      <button className="settings-action danger" disabled={!client.hasExtensionControl ||
        !disableable || action.isPending}
        onClick={() => action.mutate({ kind: "disable-plugin", installation })} type="button">
        <Ban aria-hidden="true" size={14} />{t("立即关闭", "Disable now")}
      </button>
    </div>
  </article>;
}

function ExtensionState({ state }: { state: string }) {
  const { t } = useLocale();
  const labels: Record<string, [string, string]> = {
    staged: ["待审查", "Awaiting review"], discovery_approved: ["待发现能力", "Ready for discovery"],
    capabilities_pending: ["待启用能力", "Awaiting enablement"], approved: ["已审查", "Reviewed"],
    enabled: ["已启用", "Enabled"], disabled: ["已停用", "Disabled"], revoked: ["已撤销", "Revoked"],
    quarantined: ["需重新审查", "Review needed"], rolled_back: ["已回退", "Rolled back"],
    ready: ["已就绪", "Ready"], healthy: ["运行正常", "Healthy"], starting: ["正在启动", "Starting"],
    stopped: ["已停止", "Stopped"], degraded: ["需检查", "Needs attention"], failed: ["运行失败", "Failed"],
  };
  const label = labels[state];
  return <span className={`extension-state state-${state}`}>{label ? t(label[0], label[1]) : state.replaceAll("_", " ")}</span>;
}

function Fingerprint({ label, value }: { label: string; value: string }) {
  return <div className="extension-fingerprint"><span>{label}</span>
    <code title={value}>{value ? `${value.slice(0, 12)}…${value.slice(-8)}` : "—"}</code></div>;
}

function ExtensionEmpty({ children }: { children: ReactNode }) {
  return <p className="extension-empty">{children}</p>;
}
