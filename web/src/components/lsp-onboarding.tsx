import { useState } from "react";
import { useMutation, useQueryClient } from "@tanstack/react-query";
import type { APIClient } from "../api/client";
import type { CodeIntelConfigurationView } from "../api/types";
import { formatWorkspacePathLabel } from "../lib/format";
import { useLocale } from "../lib/locale";
import { ExtensionSteps } from "./extension-onboarding";

export function LSPConfigurationForm({ client, enabled, workspaceID, capabilityKnown = true }: {
  client: APIClient; enabled: boolean; workspaceID: string; capabilityKnown?: boolean;
}) {
  const { t } = useLocale();
  const queryClient = useQueryClient();
  const [id, setID] = useState("");
  const [name, setName] = useState("");
  const [executable, setExecutable] = useState("");
  const [sha, setSHA] = useState("");
  const [language, setLanguage] = useState("");
  const [extensions, setExtensions] = useState("");
  const [args, setArgs] = useState("");
  const stage = useMutation({ mutationFn: () => client.stageCodeIntelConfiguration({
    version: "code-intel-configuration.v1", server_id: id.trim(), name: name.trim(), workspace_id: workspaceID,
    executable: executable.trim(), executable_sha256: sha.trim().toLowerCase(),
    languages: [{ id: language.trim(), extensions: extensions.split(",").map((item) => item.trim()).filter(Boolean) }],
    arguments: args.split(/\r?\n/u).filter(Boolean), request_timeout_ms: 30_000,
  }), onSuccess: () => queryClient.invalidateQueries({ queryKey: ["code-intel"] }) });
  const change = (update: () => void) => { update(); stage.reset(); };
  return <details className="extension-onboarding"><summary>{t("配置本地 LSP", "Configure local LSP")}</summary>
    <p>{t("先准备已安装的语言服务器及 SHA-256，登记后核对来源并审查，再执行一次只读查询。待审查配置保存在当前服务进程中；重启后需重新登记。",
      "Prepare an installed language server and its SHA-256. Register it, review its source, then run one readonly query. Pending configurations live in this service process and need registration again after a restart.")}</p>
    {!enabled && <p role="status">{!capabilityKnown ? t("LSP 接入能力尚未确认，请刷新状态。", "LSP onboarding capability is unconfirmed; refresh state.") :
      t("当前服务未开放 LSP 设置管理。请检查控制权限与服务版本；使用显式 LSP 配置文件的服务需通过 CLI 或文件配置，或不指定该文件重启后使用设置管理。已配置服务器的状态仍可读取。",
        "LSP settings management is unavailable. Check control access and service version. Services using an explicit LSP config file require CLI/file configuration, or restart without that file to manage settings here. Existing server state remains readable.")}</p>}
    {!workspaceID && <p role="status">{t("先选择工作区，或打开一个任务。", "Select a workspace or open a task first.")}</p>}
    <form className="extension-form" onSubmit={(event) => { event.preventDefault(); stage.mutate(); }}>
      <fieldset disabled={!enabled || !workspaceID || stage.isPending}>
        <label>{t("LSP Server ID", "LSP server ID")}<input required maxLength={256} value={id} onChange={(event) => change(() => setID(event.target.value))} /></label>
        <label>{t("LSP 显示名称", "LSP display name")}<input required maxLength={256} value={name} onChange={(event) => change(() => setName(event.target.value))} /></label>
        <label>{t("LSP 可执行文件绝对路径", "LSP absolute executable path")}<input required maxLength={4096} value={executable}
          onChange={(event) => change(() => setExecutable(event.target.value))} /></label>
        <label>{t("可执行文件 SHA-256", "Executable SHA-256")}<input required maxLength={64} pattern="[a-fA-F0-9]{64}" value={sha}
          onChange={(event) => change(() => setSHA(event.target.value))} /></label>
        <label>{t("语言 ID", "Language ID")}<input required maxLength={256} placeholder="typescript" value={language}
          onChange={(event) => change(() => setLanguage(event.target.value))} /></label>
        <label>{t("文件后缀（逗号分隔）", "File suffixes (comma separated)")}<input required maxLength={1024} placeholder=".ts,.tsx" value={extensions}
          onChange={(event) => change(() => setExtensions(event.target.value))} /></label>
        <label>{t("LSP 参数（每行一项，可选）", "LSP arguments (one per line, optional)")}<textarea rows={3} maxLength={8192} value={args}
          onChange={(event) => change(() => setArgs(event.target.value))} /></label>
        <p>{t("范围：工作区", "Scope: workspace")} {workspaceID || "—"} · {t("来源：人工配置", "Source: operator configuration")}</p>
        <button className="settings-action" type="submit" disabled={!id.trim() || !name.trim() || !executable.trim() ||
          !/^[a-fA-F0-9]{64}$/u.test(sha.trim()) || !language.trim() || !extensions.trim() || stage.isSuccess}>
          {stage.isPending ? t("正在登记…", "Registering…") : t("登记 LSP 配置", "Register LSP configuration")}</button>
      </fieldset>
    </form>
    {stage.data && <p role="status">{t("LSP 配置已暂存。下一步核对下方来源与指纹，确认后保存审查。", "LSP configuration staged. Next, inspect the source and fingerprint below, then confirm the review.")}</p>}
    {stage.error && <p className="inline-warning" role="alert">{stage.error.message}</p>}
  </details>;
}

export function LSPConfigurationCard({ client, enabled, configuration }: {
  client: APIClient; enabled: boolean; configuration: CodeIntelConfigurationView;
}) {
  const { t } = useLocale();
  const queryClient = useQueryClient();
  const [confirmed, setConfirmed] = useState(false);
  const [tool, setTool] = useState("code_document_symbols");
  const [path, setPath] = useState("");
  const [query, setQuery] = useState("");
  const body = { version: "code-intel-configuration.v1", workspace_id: configuration.workspace_id,
    expected_descriptor_fingerprint: configuration.descriptor_fingerprint };
  const review = useMutation({ mutationFn: () => client.reviewCodeIntelConfiguration(configuration.server_id, body),
    onSuccess: () => queryClient.invalidateQueries({ queryKey: ["code-intel"] }) });
  const probe = useMutation({ mutationFn: () => client.testCodeIntelConfiguration(configuration.server_id, {
    ...body, tool, ...(tool === "code_document_symbols" ? { path } : { query: query.trim() }),
  }), onSuccess: () => queryClient.invalidateQueries({ queryKey: ["code-intel"] }) });
  const reviewed = configuration.review_state === "reviewed";
  return <article className="extension-card">
    <header><strong>{configuration.server_name}</strong><span className="extension-state">{configuration.review_state.replaceAll("_", " ")}</span></header>
    <dl className="extension-facts">
      <div><dt>{t("范围", "Scope")}</dt><dd>{configuration.scope} · {configuration.workspace_id}</dd></div>
      <div><dt>{t("来源", "Source")}</dt><dd>{configuration.source_kind} · {configuration.source_label}</dd></div>
      <div><dt>{t("语言", "Languages")}</dt><dd>{configuration.languages.map((item) => `${item.id} (${item.extensions.join(", ")})`).join("; ")}</dd></div>
      <div><dt>{t("人工审查", "Human review")}</dt><dd>{reviewed ? configuration.reviewed_by : t("待审查", "Pending")}</dd></div>
    </dl>
    <label className="extension-fingerprint">{t("描述符指纹", "Descriptor fingerprint")}<code title={configuration.descriptor_fingerprint}>{configuration.descriptor_fingerprint}</code></label>
    <label className="extension-fingerprint">SHA-256<code title={configuration.executable_sha256}>{configuration.executable_sha256}</code></label>
    <div className="extension-review">
      <ExtensionSteps current={reviewed ? 1 : 0} steps={[t("审查配置", "Review configuration"), t("只读查询测试", "Test a readonly query")]} />
      {!enabled && <p role="status">{t("当前连接可查看配置。连接具备 LSP 设置管理权限的服务后，可保存审查并执行测试。", "Inspect the configuration on this connection. Connect with LSP settings control access to save a review and run a test.")}</p>}
      {!reviewed ? <>
        <p>{t("核对来源、语言、可执行文件 SHA-256 与描述符指纹后保存审查。下一步测试会启动此服务器并执行只读查询。", "Review the source, language, executable SHA-256, and descriptor fingerprint. The next test step starts this server and runs a readonly query.")}</p>
        <label><input type="checkbox" checked={confirmed} disabled={review.isPending} onChange={(event) => setConfirmed(event.target.checked)} />
          {t("我已核对当前 LSP 描述符并允许后续只读测试", "I reviewed this LSP descriptor and permit a subsequent readonly test")}</label>
        <button className="settings-action" disabled={!enabled || !confirmed || review.isPending} onClick={() => review.mutate()} type="button">
          {t("审查并保存 LSP 配置", "Review and save LSP configuration")}</button>
      </> : <>
        <p>{t("配置已审查。选择查询类型并填写文件或符号，提交后启动已固定的服务器并执行一次只读查询。", "Configuration reviewed. Select a query type and provide a file or symbol. Submitting starts the pinned server and runs one readonly query.")}</p>
        <label>{t("只读测试", "Readonly test")}<select aria-label={t("LSP 只读测试", "LSP readonly test")} value={tool} disabled={probe.isPending}
          onChange={(event) => { setTool(event.target.value); probe.reset(); }}>
          <option value="code_document_symbols">文件符号 / Document symbols</option><option value="code_workspace_symbols">工作区符号 / Workspace symbols</option>
        </select></label>
        {tool === "code_document_symbols" ? <label>{t("工作区内相对文件路径", "Workspace-relative file path")}<input value={path} disabled={probe.isPending}
          placeholder="src/main.ts" maxLength={4096} onChange={(event) => { setPath(event.target.value); probe.reset(); }} /></label>
          : <label>{t("符号查询（可选）", "Symbol query (optional)")}<input value={query} disabled={probe.isPending} maxLength={4096}
            onChange={(event) => { setQuery(event.target.value); probe.reset(); }} /></label>}
        <button className="settings-action" disabled={!enabled || probe.isPending || (tool === "code_document_symbols" && !path)}
          onClick={() => probe.mutate()} type="button">{probe.isPending ? t("正在执行只读测试…", "Running readonly test…") : t("执行一次 LSP 只读测试", "Run one readonly LSP test")}</button>
      </>}
      {review.error && <p className="inline-warning" role="alert">{review.error.message}</p>}
      {probe.error && <p className="inline-warning" role="alert">{probe.error.message}</p>}
      {probe.data && <section className="extension-probe-results" aria-label={t("LSP 实际查询结果", "Actual LSP query result")}>
        <p role="status">{t("实际只读查询已返回", "Actual readonly query returned")}: {probe.data.result.state} · {probe.data.result.items.length} {t("项", "items")}{probe.data.result.page.truncated ? t("（已截断）", " (truncated)") : ""}</p>
        <p>{t("能力指纹", "Capability fingerprint")}: {probe.data.result.capability_fingerprint}</p>
        <p>{t("查询指纹", "Query fingerprint")}: {probe.data.result.query_fingerprint}</p>
        {probe.data.result.items.length === 0 && <p>{t("本次查询返回 0 个符号。检查文件路径或换一个符号查询后重试。", "This query returned 0 symbols. Check the file path or try another symbol query.")}</p>}
        <ul>{probe.data.result.items.map((item, index) => <li key={index}>{item.name || item.kind}{item.path ? ` · ${formatWorkspacePathLabel(item.path)}` : ""}
          {item.range ? `:${item.range.start.line + 1}:${item.range.start.character + 1}` : ""}</li>)}</ul>
        {probe.data.result.warnings.map((warning, index) => <p className="inline-warning" key={index}>{warning}</p>)}
      </section>}
    </div>
  </article>;
}
