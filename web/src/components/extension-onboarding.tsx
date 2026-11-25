import { useRef, useState } from "react";
import { useMutation, useQueryClient } from "@tanstack/react-query";
import type { APIClient } from "../api/client";
import type { ExtensionMCPServerView, ExtensionPluginInstallationView } from "../api/types";
import { isBuiltinComputerUseServer } from "../api/computer-use";
import { useLocale } from "../lib/locale";
import { MCPCredentialControls } from "./mcp-credential-controls";
import { MCPEnvironmentCredentialControls } from "./mcp-environment-credentials";
import { parseMCPConfigLines } from "../api/mcp-config";
import "./extension-onboarding.css";

function Failure({ error }: { error: unknown }) {
  const { t } = useLocale();
  return error ? <p className="inline-warning" role="alert">{error instanceof Error ? error.message : t("操作失败。保留当前输入后重试。", "Action failed. Keep your input and retry.")}</p> : null;
}

export function ExtensionSteps({ steps, current }: { steps: string[]; current: number }) {
  const { t } = useLocale();
  return <ol className="extension-steps" aria-label={t("接入步骤", "Setup steps")}>
    {steps.map((step, index) => <li aria-current={index === current ? "step" : undefined}
      className={index < current ? "is-complete" : index === current ? "is-current" : ""} key={step}>
      <span className="extension-step-marker" aria-hidden="true">{index < current ? "✓" : index + 1}</span>
      <span>{step}{index < current && <span className="sr-only">{t("（已完成）", " (complete)")}</span>}</span>
    </li>)}
  </ol>;
}

export function MCPRegistrationForm({ client, enabled, workspaceID, runID, capabilityKnown = true }: {
  client: APIClient; enabled: boolean; workspaceID: string; runID: string; capabilityKnown?: boolean;
}) {
  const { t } = useLocale();
  const queryClient = useQueryClient();
  const [id, setID] = useState("");
  const [name, setName] = useState("");
  const [transport, setTransport] = useState("streamable_http");
  const [target, setTarget] = useState("");
  const [args, setArgs] = useState("");
  const [credential, setCredential] = useState("");
  const [cwd, setCwd] = useState("");
  const [environment, setEnvironment] = useState("");
  const [environmentCredentials, setEnvironmentCredentials] = useState("");
  const [headers, setHeaders] = useState("");
  const [scope, setScope] = useState("workspace");
  const [capabilities, setCapabilities] = useState<string[]>(["tools"]);
  const registration = useMutation({
    mutationFn: () => client.registerMCPServer({ version: "extension-control.v1", descriptor: {
      protocol_version: "mcp-client.v3", id: id.trim(), name: name.trim(), transport,
      target: target.trim(), workspace_id: workspaceID, scope,
      ...(scope === "run" ? { run_id: runID } : {}),
      ...(transport === "stdio" && args ? { arguments: args.split(/\r?\n/u).filter(Boolean) } : {}),
      ...(transport === "stdio" && cwd.trim() ? { cwd: cwd.trim() } : {}),
      ...(transport === "stdio" && environment.trim() ? { env: parseMCPConfigLines(environment, "=", t("环境变量需每行填写唯一的 NAME=value。", "Use one unique NAME=value environment entry per line.")) } : {}),
      ...(transport === "stdio" && environmentCredentials.trim() ? { env_credential_refs: parseMCPConfigLines(environmentCredentials, "=", t("凭据引用需每行填写唯一的 NAME=凭据名称。", "Use one unique NAME=credential-name reference per line.")) } : {}),
      ...(transport !== "stdio" && headers.trim() ? { headers: parseMCPConfigLines(headers, ":", t("HTTP header 需每行填写唯一的 Name: value。", "Use one unique Name: value HTTP header per line.")) } : {}),
      ...(transport !== "stdio" && credential.trim() ? { credential_ref: credential.trim() } : {}),
      declared_capabilities: capabilities, call_timeout_ms: 30_000, max_result_bytes: 65_536,
    } }),
    onSuccess: () => queryClient.invalidateQueries({ queryKey: ["extensions"] }),
  });
  const change = (update: () => void) => { update(); registration.reset(); };
  return <details className="extension-onboarding"><summary>{t("登记 MCP Server", "Register MCP server")}</summary>
    {!enabled && <p role="status">{!capabilityKnown ? t("MCP 接入能力尚未确认，请刷新状态。", "MCP onboarding capability is unconfirmed; refresh state.") :
      t("当前服务未开放 MCP 登记或当前连接没有控制权限。请检查服务版本与控制连接。", "MCP registration is unavailable. Check the service version and control connection.")}</p>}
    {!workspaceID && <p role="status">{t("先选择工作区，或打开一个任务。", "Select a workspace or open a task first.")}</p>}
    <form className="extension-form" onSubmit={(event) => { event.preventDefault(); registration.mutate(); }}>
      <fieldset disabled={!enabled || !workspaceID || registration.isPending}>
        <label>{t("Server ID", "Server ID")}<input required maxLength={256} value={id} onChange={(event) => change(() => setID(event.target.value))} /></label>
        <label>{t("显示名称", "Display name")}<input required maxLength={256} value={name} onChange={(event) => change(() => setName(event.target.value))} /></label>
        <label>{t("传输方式", "Transport")}<select value={transport} onChange={(event) => change(() => setTransport(event.target.value))}>
          <option value="streamable_http">HTTP / Streamable HTTP</option><option value="stdio">本地程序 / stdio</option>
          <option value="sse">Legacy SSE</option><option value="websocket">WebSocket</option>
        </select></label>
        <label>{transport === "stdio" ? t("可执行文件绝对路径", "Absolute executable path") : transport === "websocket" ? t("服务器地址（WSS / 本机 WS）", "Server URL (WSS / local WS)") : t("服务器地址（HTTPS / 本机 HTTP）", "Server URL (HTTPS / local HTTP)")}
          <input required maxLength={4096} type={transport === "stdio" ? "text" : "url"} value={target}
            placeholder={transport === "stdio" ? "C:\\tools\\mcp-server.exe" : transport === "websocket" ? "wss://example.com/mcp" : "https://example.com/mcp"}
            onChange={(event) => change(() => setTarget(event.target.value))} /></label>
        <label>{t("登记范围", "Registration scope")}<select value={scope} onChange={(event) => change(() => setScope(event.target.value))}>
          <option value="workspace">工作区 / Workspace</option><option disabled={!runID} value="run">当前任务执行 / Current Run</option>
        </select></label>
        <p>{t("工作区", "Workspace")}: {workspaceID || "—"}{scope === "run" ? ` · Run: ${runID}` : ""}</p>
        <div className="extension-checks" role="group" aria-label={t("声明能力", "Declared capabilities")}>
          {["tools", "resources", "prompts"].map((capability) => <label key={capability}><input type="checkbox"
            checked={capabilities.includes(capability)} onChange={(event) => change(() => setCapabilities((current) =>
              event.target.checked ? [...current, capability] : current.filter((item) => item !== capability)))} />{capability}</label>)}
        </div>
        {transport === "stdio" ? <><label>{t("程序参数（每行一项，可选）", "Arguments (one per line, optional)")}
          <textarea rows={3} maxLength={8192} value={args} onChange={(event) => change(() => setArgs(event.target.value))} /></label>
          <label>{t("工作目录（现存绝对路径，可选）", "Working directory (existing absolute path, optional)")}
            <input maxLength={4096} value={cwd} onChange={(event) => change(() => setCwd(event.target.value))} /></label>
          <label>{t("非敏感环境变量（每行 NAME=value）", "Non-sensitive environment (NAME=value per line)")}
            <textarea rows={3} maxLength={16384} value={environment} placeholder="NODE_ENV=production" onChange={(event) => change(() => setEnvironment(event.target.value))} /></label>
          <label>{t("环境凭据引用（每行 NAME=凭据名称）", "Environment credential references (NAME=credential-name per line)")}
            <textarea rows={3} maxLength={4096} value={environmentCredentials} placeholder="API_KEY=mcp-example" onChange={(event) => change(() => setEnvironmentCredentials(event.target.value))} /></label>
          <p>{t("程序继承最小系统环境和以上显式值。秘密请填写凭据名称，登记后保存到系统凭据库。", "The program receives a minimal system environment plus these explicit values. Reference secrets by credential name, then store them after registration.")}</p>
          </> : <><label>{t("固定非敏感 HTTP header（每行 Name: value）", "Fixed non-sensitive HTTP headers (Name: value per line)")}
            <textarea rows={3} maxLength={16384} value={headers} placeholder="X-Project: example" onChange={(event) => change(() => setHeaders(event.target.value))} /></label>
          <label>{t("凭据名称（认证时填写，登记后输入令牌）", "Credential name (for authentication; enter the token after registration)")}
            <input maxLength={64} value={credential} placeholder="mcp-example" onChange={(event) => change(() => setCredential(event.target.value))} /></label>
          <p>{t("HTTP / WS 明文地址限本机 loopback。认证与协议 header 由服务管理。", "Plain HTTP / WS is limited to local loopback. Authentication and protocol headers are managed by the service.")}</p></>}
        <button className="settings-action" disabled={!id.trim() || !name.trim() || !target.trim() ||
          capabilities.length === 0 || registration.isSuccess} type="submit">
          {registration.isPending ? t("正在登记…", "Registering…") : t("提交登记", "Register descriptor")}</button>
      </fieldset>
    </form>
    {registration.data && <p role="status">{t("登记返回状态", "Registration returned state")}: {registration.data.server.state} ·
      {t("下一步：按下方服务器的当前阶段继续。实际调用结果会记录在任务中。", "Next: follow the current server stage below. Actual call results are recorded in your task.")}</p>}
    <Failure error={registration.error} />
  </details>;
}

export function PluginImportForm({ client, enabled, sourceEnabled = false, capabilityKnown = true }: {
  client: APIClient; enabled: boolean; sourceEnabled?: boolean; capabilityKnown?: boolean;
}) {
  const { t } = useLocale();
  const queryClient = useQueryClient();
  const input = useRef<HTMLInputElement>(null);
  const [file, setFile] = useState<File | null>(null);
  const [source, setSource] = useState<"upload" | "https" | "git">("upload");
  const [url, setURL] = useState("");
  const [pin, setPin] = useState("");
  const [archivePath, setArchivePath] = useState("plugin.zip");
  const imported = useMutation({
    mutationFn: async () => {
      if (source !== "upload" && !sourceEnabled) throw new Error(t("当前服务未开放 URL / Git 导入。", "URL / Git import is unavailable on this service."));
      if (source !== "upload") return client.importPluginPackage({ version: "extension-control.v1",
        ...(source === "https" ? { archive_sha256: pin.trim() } : {}),
        source: { kind: source, url: url.trim(), ...(source === "git" ? { commit: pin.trim(), archive_path: archivePath.trim() } : {}) },
      });
      if (!file || file.size === 0 || file.size > 4 * 1024 * 1024) throw new Error(t("请选择 1 字节至 4 MiB 的 Plugin ZIP 包。", "Choose a Plugin ZIP between 1 byte and 4 MiB."));
      const buffer = await file.arrayBuffer();
      const digest = await crypto.subtle.digest("SHA-256", buffer);
      const archive_sha256 = Array.from(new Uint8Array(digest), (byte) => byte.toString(16).padStart(2, "0")).join("");
      let binary = "";
      const bytes = new Uint8Array(buffer);
      for (let offset = 0; offset < bytes.length; offset += 0x8000) binary += String.fromCharCode(...bytes.subarray(offset, offset + 0x8000));
      return client.importPluginPackage({ version: "extension-control.v1", archive_sha256, archive_base64: btoa(binary) });
    },
    onSuccess: () => queryClient.invalidateQueries({ queryKey: ["extensions"] }),
  });
  const change = (update: () => void) => { update(); imported.reset(); };
  return <details className="extension-onboarding"><summary>{t("导入 Plugin ZIP", "Import Plugin ZIP")}</summary>
    <p>{t("支持 plugin.v1 ZIP（最大 4 MiB）、固定 HTTPS 地址或 Git 提交。", "Accepts a plugin.v1 ZIP up to 4 MiB, a pinned HTTPS URL, or a Git commit.")}</p>
    {!enabled && <p role="status">{!capabilityKnown ? t("Plugin 接入能力尚未确认，请刷新状态。", "Plugin onboarding capability is unconfirmed; refresh state.") :
      t("当前服务未开放 Plugin 导入或当前连接没有控制权限。请检查服务版本与控制连接。", "Plugin import is unavailable. Check the service version and control connection.")}</p>}
    <label>{t("导入来源", "Import source")}<select value={source} disabled={!enabled || imported.isPending}
      onChange={(event) => change(() => { setSource(event.target.value as "upload" | "https" | "git"); setPin(""); })}>
      <option value="upload">{t("本地 ZIP", "Local ZIP")}</option><option disabled={!sourceEnabled} value="https">HTTPS URL + SHA-256</option><option disabled={!sourceEnabled} value="git">Git + commit</option>
    </select></label>
    {enabled && !sourceEnabled && <p>{t("当前服务支持本地 ZIP；URL / Git 导入需要服务端开放此能力。", "This service supports local ZIP imports. URL / Git imports require server support.")}</p>}
    {source === "upload" ? <><input hidden ref={input} type="file" accept="application/zip,.zip" aria-label={t("选择 Plugin ZIP", "Choose Plugin ZIP")}
      disabled={!enabled || imported.isPending} onChange={(event) => {
        const selected = event.target.files?.[0];
        if (selected) { setFile(selected); imported.reset(); }
        event.currentTarget.value = "";
      }} />
    <button className="settings-action" disabled={!enabled || imported.isPending} onClick={() => input.current?.click()} type="button">
      {t("选择 Plugin ZIP", "Choose Plugin ZIP")}</button>
    {file && <div className="extension-review">
      <p>{file.name} · {file.size} bytes</p>
      <button className="settings-action" disabled={!enabled || imported.isPending || imported.isSuccess}
        onClick={() => imported.mutate()} type="button">{imported.isPending ? t("正在导入…", "Importing…") : t("暂存 Plugin 包", "Stage Plugin package")}</button>
    </div>}</> : <form className="extension-form" onSubmit={(event) => { event.preventDefault(); imported.mutate(); }}>
      <fieldset disabled={!enabled || !sourceEnabled || imported.isPending}>
        <label>{source === "git" ? t("Git 仓库 HTTPS 地址", "Git repository HTTPS URL") : t("ZIP 的 HTTPS 地址", "ZIP HTTPS URL")}
          <input required type="url" maxLength={4096} value={url} onChange={(event) => change(() => setURL(event.target.value))} /></label>
        <label>{source === "git" ? t("完整 Git commit（40 或 64 位）", "Full Git commit (40 or 64 characters)") : "SHA-256"}
          <input required value={pin} maxLength={64} pattern={source === "git" ? "(?:[a-f0-9]{40}|[a-f0-9]{64})" : "[a-f0-9]{64}"}
            onChange={(event) => change(() => setPin(event.target.value))} /></label>
        {source === "git" && <label>{t("仓库内 ZIP 路径", "ZIP path in repository")}
          <input required value={archivePath} maxLength={512} onChange={(event) => change(() => setArchivePath(event.target.value))} /></label>}
        <p>{t("点击获取即允许读取此固定来源。地址不能包含账号、密码、查询参数或重定向。", "Fetch authorizes reading this pinned source. URLs cannot contain credentials, query parameters, or redirects.")}</p>
        <button className="settings-action" disabled={!url.trim() || !pin.trim() || imported.isSuccess} type="submit">
          {imported.isPending ? t("正在获取…", "Fetching…") : t("获取并暂存 Plugin", "Fetch and stage Plugin")}</button>
      </fieldset>
    </form>}
    {imported.data && <p role="status">{t("导入返回状态", "Import returned state")}: {imported.data.installation.state} ·
      {t("下一步：核对下方包来源、能力和指纹，按当前阶段继续审查或启用。", "Next: inspect the package source, capabilities, and fingerprint below, then follow its current review or enable stage.")}</p>}
    <Failure error={imported.error} />
  </details>;
}

export function MCPReviewControls({ client, server, onOpenTask, credentialCapability }: {
  client: APIClient; server: ExtensionMCPServerView; onOpenTask?: (workspaceID?: string) => void; credentialCapability?: boolean;
}) {
  const { t } = useLocale();
  const queryClient = useQueryClient();
  const [confirmed, setConfirmed] = useState(false);
  const [draftOpen, setDraftOpen] = useState(false);
  const builtin = isBuiltinComputerUseServer(server);
  const action = server.state === "capabilities_pending" ? "enable_capabilities" : "approve_discovery";
  const reviewable = ["staged", "disabled", "quarantined", "capabilities_pending"].includes(server.state);
  const review = useMutation({ mutationFn: () => client.reviewMCPServer(server.id, {
    version: "extension-control.v1", action, expected_descriptor_fingerprint: server.descriptor_fingerprint,
    ...(action === "enable_capabilities" ? { expected_capability_fingerprint: server.capabilities.fingerprint } : {}),
  }), onSuccess: () => queryClient.invalidateQueries({ queryKey: ["extensions"] }) });
  const firstTool = server.capabilities.tools[0] ?? t("（选择工具）", "(choose a tool)");
  const draft = t(`请在当前任务中通过现有 MCP 权限与审批路径调用 Server ${server.id} 的工具 ${firstTool}，使用该工具需要的明确参数。请报告实际调用结果、调用状态以及任务中的证据。`,
    `In this task, call tool ${firstTool} on MCP server ${server.id} through the existing task permissions and approval flow, with explicit arguments required by the tool. Report the actual result, call status, and evidence in the task.`);
  return <div className="extension-review">
    {builtin && <p>{t("Windows Computer Use 随宿主自动登记，操作时沿用当前任务的 Agent 权限与审批。", "Windows Computer Use is registered automatically by the host. App control follows the current task's Agent permissions and approvals.")}</p>}
    {!builtin && server.state !== "revoked" && <ExtensionSteps current={server.state === "enabled" ? 3 : server.state === "capabilities_pending" ? 2 : server.state === "discovery_approved" ? 1 : 0}
      steps={[t("核对登记", "Review registration"), t("批准并发现", "Approve and discover"), t("审查并启用", "Review and enable"), t("任务中调用", "Call in a task")]} />}
    {!builtin && (server.transport === "stdio" ? <MCPEnvironmentCredentialControls client={client} server={server} capability={credentialCapability} /> :
      <MCPCredentialControls client={client} server={server} capability={credentialCapability} />)}
    {server.cwd && <p>{t("工作目录", "Working directory")}: {server.cwd}</p>}
    {server.env && Object.keys(server.env).length > 0 && <p>{t("显式非敏感环境", "Explicit non-sensitive environment")}: {Object.entries(server.env).map(([name, value]) => `${name}=${value}`).join(" · ")}</p>}
    {server.headers && Object.keys(server.headers).length > 0 && <p>{t("固定 HTTP header", "Fixed HTTP headers")}: {Object.entries(server.headers).map(([name, value]) => `${name}: ${value}`).join(" · ")}</p>}
    {!client.hasExtensionControl && <p role="status">{t("当前为只读连接。审查或启用需要控制权限。", "Read-only connection. Review and enablement require control access.")}</p>}
    {server.state === "enabled" && <p>{t("调用工具仍需当前任务授权。", "Tool calls still require permission from the task.")}</p>}
    {server.state === "revoked" && <p>{t("重新接入需要登记并审查新的服务器描述符。", "To reconnect, register and review a new server descriptor.")}</p>}
    {server.health_message && <p className="inline-warning">{server.health_message}</p>}
    <p>{t("已发现能力", "Discovered capabilities")}: {server.capabilities.negotiated.join(", ") || "—"}</p>
    {server.state === "enabled" && server.capabilities.tools.length + server.capabilities.resources.length + server.capabilities.prompts.length + (server.capabilities.resource_templates?.length ?? 0) === 0 && <p>{t("未发现工具、资源、资源模板或 Prompt，请重新发现并检查服务器发布的能力。",
      "No tools, resources, resource templates, or prompts were discovered. Rediscover and check the server's published capabilities.")}</p>}
    {server.capabilities.tools.length > 0 && <p className="extension-tool-list">{server.capabilities.tools.join(", ")}</p>}
    {server.capabilities.resources.length > 0 && <p className="extension-tool-list">Resources: {server.capabilities.resources.join(", ")}</p>}
    {(server.capabilities.resource_templates?.length ?? 0) > 0 && <p className="extension-tool-list">Resource templates: {server.capabilities.resource_templates?.join(", ")}</p>}
    {server.capabilities.prompts.length > 0 && <p className="extension-tool-list">Prompts: {server.capabilities.prompts.join(", ")}</p>}
    {server.capabilities.prompt_details?.map((prompt) => <details className="extension-tool-list" key={prompt.name}>
      <summary>Prompt: {prompt.name}</summary>
      {prompt.arguments.length === 0 ? <p>{t("此 Prompt 无参数。", "This prompt has no arguments.")}</p> : <ul>{prompt.arguments.map((argument) =>
        <li key={argument.name}><code>{argument.name}</code> · {argument.required ? t("必填", "Required") : t("可选", "Optional")}
          {argument.description ? ` · ${argument.description}` : ""}</li>)}</ul>}
    </details>)}
    {reviewable && <>
      <label><input checked={confirmed} disabled={review.isPending} onChange={(event) => setConfirmed(event.target.checked)} type="checkbox" />
        {action === "enable_capabilities" ? t("我已核对当前能力指纹与所有发现列表", "I reviewed this capability fingerprint and all discovered lists") : t("我已核对描述符，允许连接并发现能力", "I reviewed this descriptor and permit connection for discovery")}</label>
      <button className="settings-action" disabled={!client.hasExtensionControl || !confirmed || review.isPending}
        onClick={() => review.mutate()} type="button">{action === "enable_capabilities" ? t("审查并启用能力", "Review and enable capabilities") : t("批准能力发现", "Approve discovery")}</button>
    </>}
    {server.state === "enabled" && server.capabilities.tools.length > 0 && <>
      <button className="settings-action" onClick={() => setDraftOpen((value) => !value)} type="button">{t("生成首次调用任务草稿", "Prepare first-call task draft")}</button>
      {draftOpen && <label>{t("调用任务草稿（复制到任务中发送）", "Task draft (copy and send in your task)")}<textarea readOnly rows={4} value={draft} onFocus={(event) => event.currentTarget.select()} /></label>}
      {onOpenTask && <button className="settings-action" onClick={() => onOpenTask(server.workspace_id)} type="button">{t("打开任务输入区", "Open task composer")}</button>}
    </>}
    <Failure error={review.error} />
  </div>;
}

export function PluginReviewControls({ client, installation }: {
  client: APIClient; installation: ExtensionPluginInstallationView;
}) {
  const { t } = useLocale();
  const queryClient = useQueryClient();
  const [confirmed, setConfirmed] = useState(false);
  const [capabilities, setCapabilities] = useState<string[]>([]);
  const approve = ["staged", "quarantined"].includes(installation.state);
  const enable = ["approved", "disabled"].includes(installation.state);
  const review = useMutation({ mutationFn: () => client.reviewPluginInstallation(installation.id, {
    version: "extension-control.v1", action: approve ? "approve" : "enable",
    expected_package_fingerprint: installation.package_fingerprint, expected_generation: installation.generation,
    confirm_untrusted: confirmed, ...(approve ? {} : { capabilities }),
  }), onSuccess: () => queryClient.invalidateQueries({ queryKey: ["extensions"] }) });
  return <div className="extension-review">
    {(approve || enable || installation.state === "enabled") && <ExtensionSteps current={approve ? 0 : enable ? 1 : 2}
      steps={[t("审查包信息", "Review package"), t("选择并启用能力", "Choose and enable capabilities"), t("任务中使用", "Use in a task")]} />}
    {!client.hasExtensionControl && <p role="status">{t("当前连接为只读。连接具备控制权限的服务后可审查并启用此包。", "This connection is read-only. Connect with control access to review and enable this package.")}</p>}
    {installation.state === "enabled" && installation.enabled_capabilities.includes("mcp_servers") && <p>{t("使用 MCP 前需完成服务器发现与能力审查。", "MCP requires server discovery and capability review before use.")}</p>}
    <p>{t("声明能力", "Declared capabilities")}: {installation.manifest.capabilities.join(", ") || "—"}</p>
    {(approve || enable) && <>
      {enable && <div className="extension-checks" role="group" aria-label={t("启用 Plugin 能力", "Enable Plugin capabilities")}>
        {installation.manifest.capabilities.map((capability) => <label key={capability}><input type="checkbox" disabled={review.isPending}
          checked={capabilities.includes(capability)} onChange={(event) => setCapabilities((current) =>
            event.target.checked ? [...current, capability] : current.filter((item) => item !== capability))} />{capability}</label>)}
      </div>}
      <label><input type="checkbox" checked={confirmed} disabled={review.isPending} onChange={(event) => setConfirmed(event.target.checked)} />
        {t("我已核对包指纹，并明确允许此不受信任包的本次审查或启用", "I reviewed the package fingerprint and explicitly allow this untrusted review or enable action")}</label>
      <button className="settings-action" disabled={!client.hasExtensionControl || !confirmed || review.isPending || (enable && capabilities.length === 0)}
        onClick={() => review.mutate()} type="button">{approve ? t("审查 Plugin 包", "Review Plugin package") : t("启用所选能力", "Enable selected capabilities")}</button>
    </>}
    <Failure error={review.error} />
  </div>;
}
