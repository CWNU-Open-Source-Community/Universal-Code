import { useLocale, type LocaleText } from "../../lib/locale";
import { Fragment, useEffect, useMemo, useState, type ReactNode } from "react";
import { useQuery } from "@tanstack/react-query";
import { CheckCircle2, ChevronDown, ClipboardList, FilePenLine, LoaderCircle, Search,
  TerminalSquare } from "lucide-react";
import type { APIClient } from "../../api/client";
import type { ThreadActivityArtifactReferenceView, ThreadActivityDetailView as APIThreadActivityDetailView,
  ThreadActivityBoundaryView, ThreadActivityJSONFieldSummaryView,
  ThreadActivityCommandDetailView, ThreadActivityToolDetailView, ThreadActivityTypedDetailView } from "../../api/types";
import { type NarrativeFileChange, type NarrativeEntry } from "../projection/narrative";
import { parseUnifiedDiff } from "../../components/unified-diff";
import { SavedCommandOutput } from "../../components/saved-command-output";
import { StatusLabel } from "../../components/common";
import { MCPContentPreview } from "./mcp-content-preview";

type ActivityEntry = Extract<NarrativeEntry, { kind: "activity" }>;
type ActivityItem = ActivityEntry["items"][number];

export interface ThreadActivityCommandDetail {
  display_command: string;
  cwd?: string;
  status: string;
  exit_code?: number | null;
  duration_ms?: number | null;
  stdout_preview?: string;
  stderr_preview?: string;
  truncated?: boolean;
  artifacts: ThreadActivityArtifactReferenceView[];
  environment_label?: string;
  agent_id: string;
  agent_label?: string;
}

export interface ThreadActivityDetailView {
  version: "thread_activity_detail.v2";
  run_id: string;
  commands: ThreadActivityCommandDetail[];
  typed?: {
    detail: Exclude<ThreadActivityTypedDetailView, { kind: "command" }>;
    agent_id: string;
    agent_label: string;
    duration_ms: number;
    status: string;
  };
}

const failurePattern = /(fail|error|block|deny|cancel|unavailable|ignored|bypass|timed?[_ -]?out|kill|interrupt|失败|错误|阻塞|拒绝|取消|超时|终止|中断)/iu;
const outputDecodingNotice = (t: LocaleText) => t("部分输出字符无法正确解码，当前文本可能不完整。", "Some output characters could not be decoded. The displayed text may be incomplete.");

function isFailedStatus(status: string): boolean {
  return failurePattern.test(status.trim());
}

function isRunningStatus(status: string): boolean {
  return /^(?:pending|prepared|started|starting|queued|running|stopping|in_progress|正在运行|运行中)$/iu.test(status.trim());
}

function itemFailed(item: ActivityItem): boolean {
  const summary = item.summary;
  return isFailedStatus(summary?.status ?? item.status) ||
    (summary?.exit_code !== undefined && summary.exit_code !== 0);
}

function itemRunning(item: ActivityItem): boolean {
  return isRunningStatus(item.summary?.status ?? item.status);
}

function itemSummaryMeta(item: ActivityItem, t: LocaleText): string {
  const summary = item.summary;
  if (!summary) return item.detail;
  const duration = durationLabel(summary.duration_milliseconds, summary.status);
  const count = summary.command_count > 1 ? t(`${summary.command_count} 条命令`, `${summary.command_count} commands`) : "";
  return [count, duration].filter(Boolean).join(" · ");
}

function itemSummaryStatus(item: ActivityItem): ReactNode {
  const summary = item.summary;
  if (!summary) return item.status ? <StatusLabel status={item.status} /> : "";
  if (summary.exit_code !== undefined) return `Exit ${summary.exit_code}`;
  return summary.status ? <StatusLabel status={summary.status} /> : "";
}

function commandEnvironmentLabel(command: Pick<ThreadActivityCommandDetailView, "execution_environment" | "network">, t: LocaleText): string {
  // The requested network setting does not establish isolation on the host.
  const network = command.execution_environment === "Host · Full Access" ? t("宿主网络未隔离", "Host network is not isolated")
    : command.execution_environment === "Workspace Sandbox" && command.network === "disabled" ? t("无网络", "No network")
    : t("网络隔离未确认", "Network isolation unconfirmed");
  return `${command.execution_environment} · ${network}`;
}

/** Maps the already strictly parsed Go projection into conversation labels. */
export function projectThreadActivityDetail(
  value: APIThreadActivityDetailView,
  t: LocaleText = (chinese) => chinese,
): ThreadActivityDetailView {
  const tool = value.tools[0];
  const commands = tool.detail.kind === "command" ? tool.detail.command.commands.map((command) => ({
      display_command: command.command,
      cwd: command.working_directory,
      status: command.status,
      ...(command.exit_code !== undefined ? { exit_code: command.exit_code } : {}),
      duration_ms: command.duration_milliseconds,
      stdout_preview: command.stdout_preview,
      stderr_preview: command.stderr_preview,
      truncated: command.truncated,
      artifacts: command.artifacts,
      environment_label: commandEnvironmentLabel(command, t),
      agent_id: tool.agent_id,
      agent_label: tool.agent_label,
    } satisfies ThreadActivityCommandDetail)) : [];
  return { version: value.version, run_id: value.run_id, commands,
    ...(tool.detail.kind !== "command" ? { typed: { detail: tool.detail,
      agent_id: tool.agent_id, agent_label: tool.agent_label,
      duration_ms: tool.duration_milliseconds,
      status: tool.status } } : {}) };
}

/** Shared safe renderer for both the v2 conversation and the Legacy Inspector.
 * It accepts only the Go-owned closed activity union; raw durable payload JSON
 * is deliberately not part of this component's props. */
export function ThreadActivityToolDetailPanel({ activityRef, client, runID, threadID, tool }: {
  activityRef: string;
  client: APIClient;
  runID: string;
  threadID: string;
  tool: ThreadActivityToolDetailView;
}) {
  const { t } = useLocale();
  if (tool.detail.kind !== "command") {
    return <TypedToolDetail client={client} runID={runID} typed={{
      detail: tool.detail,
      agent_id: tool.agent_id,
      agent_label: tool.agent_label,
      duration_ms: tool.duration_milliseconds,
      status: tool.status,
    }} />;
  }
  return <>{tool.detail.command.commands.map((command, index) => <CommandDetail
    activityRef={activityRef}
    client={client}
    command={{
      display_command: command.command,
      cwd: command.working_directory,
      status: command.status,
      ...(command.exit_code !== undefined ? { exit_code: command.exit_code } : {}),
      duration_ms: command.duration_milliseconds,
      stdout_preview: command.stdout_preview,
      stderr_preview: command.stderr_preview,
      truncated: command.truncated,
      artifacts: command.artifacts,
      environment_label: commandEnvironmentLabel(command, t),
      agent_id: tool.agent_id,
      agent_label: tool.agent_label,
    }}
    key={`${command.command}:${index}`}
    threadID={threadID}
  />)}</>;
}

function shortAgentID(value: string): string {
  if (value.length <= 20) return value;
  return `${value.slice(0, 11)}…${value.slice(-6)}`;
}

function AgentIdentity({ id, label }: { id: string; label?: string }) {
  const { t } = useLocale();
  if (id === "unknown") return <>{label || t("历史活动（执行者未知）", "Historical activity (unknown agent)")}</>;
  return <>{label || "Agent"} <code title={id}>{shortAgentID(id)}</code></>;
}

function ActivityIcon({ activity }: { activity: ActivityEntry["activity"] }) {
  if (activity === "plan") return <ClipboardList aria-hidden="true" size={15} />;
  if (activity === "search" || activity === "read") {
    return <Search aria-hidden="true" size={15} />;
  }
  if (activity === "edit") return <FilePenLine aria-hidden="true" size={15} />;
  if (activity === "verify") return <CheckCircle2 aria-hidden="true" size={15} />;
  return <TerminalSquare aria-hidden="true" size={15} />;
}

function durationLabel(durationMS: number | null | undefined, status = ""): string {
  if (durationMS === null || durationMS === undefined) return "";
  // Pending projections have a zero default, not a measured completion time.
  if (durationMS === 0 && isRunningStatus(status)) return "";
  if (durationMS < 1_000) return `${Math.round(durationMS)}ms`;
  if (durationMS < 60_000) return `${(durationMS / 1_000).toFixed(durationMS < 10_000 ? 1 : 0)}s`;
  const minutes = Math.floor(durationMS / 60_000);
  const seconds = Math.round((durationMS % 60_000) / 1_000);
  return `${minutes}m ${seconds}s`;
}

function byteSizeLabel(size: number): string {
  if (size < 1_024) return `${size} B`;
  if (size < 1_048_576) return `${(size / 1_024).toFixed(size < 10_240 ? 1 : 0)} KB`;
  return `${(size / 1_048_576).toFixed(1)} MB`;
}

function typedKindLabel(kind: ThreadActivityTypedDetailView["kind"], t: LocaleText): string {
  const labels: Record<ThreadActivityTypedDetailView["kind"], string> = {
    command: t("运行命令", "Run command"),
    web_search: t("联网搜索", "Web search"), web_fetch: t("网页读取", "Web read"),
    file_read: t("文件读取", "File read"), file_edit: t("文件修改", "File edit"), verification: t("验证", "Verify"), mcp: "MCP",
    browser: t("浏览器", "Browser"),
  };
  return labels[kind];
}

const fileChangeLabel = (kind: NarrativeFileChange, t: LocaleText) => {
 const labels = {
  "proposed": [
    "已提出修改，等待审阅",
    "Edit proposed; awaiting review"
  ],
  "approved": [
    "修改已批准，尚未应用",
    "Edit approved; not applied"
  ],
  "applied": [
    "修改已应用",
    "Edit applied"
  ],
  "denied": [
    "修改已拒绝",
    "Edit denied"
  ],
  "failed": [
    "文件修改失败",
    "File edit failed"
  ],
  "unknown": [
    "文件修改记录",
    "File edit records"
  ]
} as const;
 return t(labels[kind][0], labels[kind][1]);
};

const maxRenderedDiffLines = 2_000;

function FileEditDiff({ client, editID, runID }: {
  client: APIClient;
  editID: string;
  runID: string;
}) {
  const { t } = useLocale();
  const [open, setOpen] = useState(false);
  const preview = useQuery({
    queryKey: ["v2", "file-edit-diff", runID, editID],
    queryFn: ({ signal }) => client.fileEdit(runID, editID, signal),
    enabled: open,
    retry: false,
    staleTime: Number.POSITIVE_INFINITY,
  });
  const parsed = useMemo(() => preview.data ? parseUnifiedDiff(preview.data.diff) : undefined,
    [preview.data]);
  const visibleLines = parsed?.lines.slice(0, maxRenderedDiffLines) ?? [];
  const displayTruncated = (parsed?.lines.length ?? 0) > maxRenderedDiffLines;

  return <section className="v2-file-edit-diff">
    <button aria-expanded={open} onClick={() => setOpen((value) => !value)} type="button">
      {open ? t("收起 Diff", "Hide diff") : t("查看 Diff", "View diff")}
    </button>
    {open && <div className="v2-file-edit-diff-content">
      {preview.data && <details><summary>{t("查看此编辑的原目录身份", "View the original directory identity for this edit")}</summary><code>{preview.data.workspace_id}</code></details>}
      {preview.isLoading && <p className="v2-activity-detail-state" role="status">
        <LoaderCircle aria-hidden="true" className="spin" size={14} />{t("正在读取文件 Diff…", "Loading file diff…")}</p>}
      {preview.isError && <div className="v2-activity-detail-state is-error" role="alert">
        <span>{t("Diff 加载失败。", "Could not load diff.")}</span>
        <button onClick={() => void preview.refetch()} type="button">{t("重试", "Retry")}</button>
      </div>}
      {preview.data && preview.data.diff === "" && <p className="v2-activity-detail-state">
        {t("该文件修改没有文本 Diff。", "This file edit has no text diff.")}</p>}
      {preview.data && parsed && preview.data.diff !== "" && <>
        <div aria-label={t(`文件 Diff：${preview.data.path}`, `File diff: ${preview.data.path}`)} className="v2-unified-diff" role="table">
          {visibleLines.map((line, index) => <div
            aria-label={`${line.kind} ${line.text}`}
            className={`v2-unified-diff-line is-${line.kind}`}
            key={`${index}-${line.kind}`} role="row">
            <span aria-hidden="true" className="v2-unified-diff-number">{line.oldLine ?? ""}</span>
            <span aria-hidden="true" className="v2-unified-diff-number">{line.newLine ?? ""}</span>
            <span aria-hidden="true" className="v2-unified-diff-marker">{line.marker}</span>
            <code>{line.text || " "}</code>
          </div>)}
        </div>
        <small>{preview.data.secrets_redacted ? t("敏感内容已脱敏", "Sensitive content redacted") : t("未发现需脱敏内容", "No sensitive content found")}
          {displayTruncated
            ? t(` · Diff 共 ${parsed.lines.length} 行，为保持界面流畅仅显示前 ${maxRenderedDiffLines} 行`, ` · Showing the first ${maxRenderedDiffLines} of ${parsed.lines.length} diff lines`)
            : t(` · ${parsed.additions} 行新增，${parsed.deletions} 行删除`, ` · ${parsed.additions} lines added, ${parsed.deletions} removed`)}</small>
      </>}
    </div>}
  </section>;
}

function SafeJSONFieldList({ label, values }: {
  label: string;
  values: ThreadActivityJSONFieldSummaryView[];
}) {
  if (values.length === 0) return null;
  return <section className="v2-tool-facts-list" aria-label={label}>
    <strong>{label}</strong><dl>{values.map((field) => <div key={field.name}>
      <dt>{field.name}</dt><dd><code>{field.summary || "—"}</code><small>{field.type}</small></dd>
    </div>)}</dl>
  </section>;
}

function BoundaryFacts({ boundary }: {
  boundary: ThreadActivityBoundaryView;
}) {
  const { t } = useLocale();
  return <>
    <dt>{t("授权", "Authorization")}</dt><dd>{boundary.authorization === "policy_checked" ? t("已通过策略检查", "Policy check passed") :
      boundary.authorization === "pending" ? t("等待授权", "Awaiting authorization") : t("已拒绝", "Denied")}</dd>
    {boundary.error_code && <><dt>{t("错误", "Error")}</dt><dd><code>{boundary.error_code}</code></dd></>}
    {boundary.failure_reason && <><dt>{t("原因", "Reason")}</dt><dd>{boundary.failure_reason}</dd></>}
    {boundary.truncated && <><dt>{t("内容", "Content")}</dt><dd>{t("已截断", "Truncated")}</dd></>}
  </>;
}

function TypedToolDetail({ client, runID, typed }: {
  client: APIClient;
  runID: string;
  typed: NonNullable<ThreadActivityDetailView["typed"]>;
}) {
  const { t } = useLocale();
  const detail = typed.detail;
  const duration = durationLabel(typed.duration_ms, typed.status);
  const { boundary, operation } = typedDetailCommon(detail);
  return <article className="v2-command-detail v2-tool-facts">
    <header><strong>{typedKindLabel(detail.kind, t)} · {operation}</strong>
      <span className={isFailedStatus(typed.status) ? "is-failed" : ""}><StatusLabel status={typed.status} /></span>
    </header>
    <dl>
      <dt>Agent</dt><dd><AgentIdentity id={typed.agent_id} label={typed.agent_label} /></dd>
      <BoundaryFacts boundary={boundary} />
      {duration && <><dt>{t("耗时", "Duration")}</dt><dd>{duration}</dd></>}
    </dl>
    <TypedDetailBody client={client} detail={detail} runID={runID} status={typed.status} />
    {boundary.untrusted && <p className="v2-untrusted-output">
      {t("结果来自外部来源，作为参考资料展示。操作授权以任务要求和审批为准。", "Results from external sources are shown as references. Task instructions and approvals determine action permissions.")}</p>}
  </article>;
}

function typedDetailCommon(detail: Exclude<ThreadActivityTypedDetailView, { kind: "command" }>): {
  boundary: ThreadActivityBoundaryView;
  operation: string;
} {
  switch (detail.kind) {
    case "web_search": return detail.web_search;
    case "web_fetch": return detail.web_fetch;
    case "file_read": return detail.file_read;
    case "file_edit": return detail.file_edit;
    case "mcp": return detail.mcp;
    case "verification": return detail.verification;
    case "browser": return detail.browser;
  }
}

function TypedDetailBody({ client, detail, runID, status }: {
  client: APIClient;
  detail: Exclude<ThreadActivityTypedDetailView, { kind: "command" }>;
  runID: string;
  status: string;
}) {
  const { t } = useLocale();
  switch (detail.kind) {
    case "web_search": {
      const value = detail.web_search;
      return <><dl>
        <dt>{t("查询", "Query")}</dt><dd><code>{value.query}</code></dd>
        {value.provider && <><dt>{t("提供商", "Provider")}</dt><dd>{value.provider}</dd></>}
        {value.search_policy && <><dt>{t("搜索策略", "Search policy")}</dt><dd>{value.search_policy}</dd></>}
        {value.selection_reason && <><dt>{t("路由原因", "Routing reason")}</dt><dd>{value.selection_reason}</dd></>}
        <dt>{t("结果", "Result")}</dt><dd>{isFailedStatus(status) && value.source_count === 0 ? t("未取得搜索结果", "No search results retrieved")
          : t(`${value.source_count} 个来源 · ${value.citeable ? "可引用" : "待验证"}`, `${value.source_count} sources · ${value.citeable ? "Citeable" : "Unverified"}`)}</dd>
      </dl>{value.sources.length > 0 && <section className="v2-tool-facts-list" aria-label={t("搜索来源", "Search sources")}>
        <strong>{t("搜索来源", "Search sources")}</strong><ol>{value.sources.map((source) => <li key={`${source.rank}:${source.url}`}>
          <a href={source.url} rel="noreferrer" target="_blank">{source.title || source.url}</a>
          <small>{source.provider || t("未知提供商", "Unknown provider")} · {source.citeable ? t("可引用", "Citeable") : t("待验证", "Unverified")}</small>
        </li>)}</ol>
      </section>}</>;
    }
    case "web_fetch": {
      const value = detail.web_fetch;
      return <dl>
        <dt>URL</dt><dd>{value.url.startsWith("https://")
          ? <a href={value.url} rel="noreferrer" target="_blank">{value.url}</a>
          : <code>{value.url}</code>}</dd>
        <dt>{t("抓取状态", "Fetch status")}</dt><dd><StatusLabel status={value.state || "unknown"} />
          {value.http_status ? ` · HTTP ${value.http_status}` : ""}</dd>
        <dt>Robots</dt><dd>{value.robots || t("未记录", "Not recorded")}
          {value.robots_policy ? ` · ${value.robots_policy}` : ""}</dd>
        <dt>{t("重定向", "Redirects")}</dt><dd>{value.redirects}</dd>
        <dt>{t("证据", "Evidence")}</dt><dd>{value.citeable ? t("可引用", "Citeable") : t("不可引用", "Not citeable")}
          {value.partial ? t("· 部分内容", " · Partial content") : ""}</dd>
      </dl>;
    }
    case "file_read": {
      const value = detail.file_read;
      return <dl>
        {value.path && <><dt>{t("路径", "Path")}</dt><dd><code>{value.path}</code></dd></>}
        {value.query && <><dt>{t("查询", "Query")}</dt><dd><code>{value.query}</code></dd></>}
        {value.pattern && <><dt>{t("范围", "Scope")}</dt><dd><code>{value.pattern}</code></dd></>}
        {value.start_line > 0 && <><dt>{t("行范围", "Line range")}</dt><dd>{value.start_line}–{value.end_line}</dd></>}
        <dt>{t("结果", "Result")}</dt><dd>{value.summary}</dd>
      </dl>;
    }
    case "file_edit": {
      const value = detail.file_edit;
      return <><dl>
        <dt>{t("操作", "Operation ")}</dt><dd>{value.action || t("修改", "Edit")}</dd>
        {value.path && <><dt>{t("文件", "File")}</dt><dd><code>{value.path}</code></dd></>}
        {value.destination_path && <><dt>{t("目标", "Target")}</dt><dd><code>{value.destination_path}</code></dd></>}
        <dt>Diff</dt><dd>{value.diff.summary ||
          t(`+${value.diff.added_lines} −${value.diff.removed_lines} · ${value.diff.hunks} 个区块`, `+${value.diff.added_lines} −${value.diff.removed_lines} · ${value.diff.hunks} hunks`)}</dd>
        <dt>{t("应用结果", "Apply result")}</dt><dd>{value.apply_status || (value.applied ? t("已应用", "Applied") : t("尚未应用", "Not applied"))}
          {value.replayed ? t("· 幂等重放", " · Replayed") : ""}</dd>
      </dl>{value.diff_available
        ? <FileEditDiff client={client} editID={value.edit_id} runID={runID} />
        : <p className="v2-activity-detail-state">{t("本次活动没有可展示的 Diff。", "No diff is available for this activity.")}</p>}</>;
    }
    case "mcp": {
      const value = detail.mcp;
      return <><dl>
        <dt>{t("服务器", "Server")}</dt><dd><code>{value.server}</code></dd>
        <dt>{t("工具", "Tool")}</dt><dd><code>{value.tool}</code></dd>
        <dt>{t("结果摘要", "Result summary")}</dt><dd>{value.result.summary}</dd>
      </dl>
      <SafeJSONFieldList label={t("参数", "Arguments")} values={value.arguments} />
      <SafeJSONFieldList label={t("结果字段", "Result fields")} values={value.result.fields} />
      <MCPContentPreview contents={value.contents} omitted={value.omitted_contents} truncated={value.content_truncated} /></>;
    }
    case "verification": {
      const value = detail.verification;
      return <dl>
        <dt>{t("工具", "Tool")}</dt><dd><code>{value.tool}</code></dd>
        {value.path && <><dt>{t("文件", "File")}</dt><dd><code>{value.path}</code></dd></>}
        {value.query && <><dt>{t("查询", "Query")}</dt><dd><code>{value.query}</code></dd></>}
        {value.position && <><dt>{t("位置", "Position")}</dt><dd>{value.position}</dd></>}
        <dt>{t("结果", "Result")}</dt><dd>{value.summary}</dd>
      </dl>;
    }
    case "browser": {
      const value = detail.browser;
      return <dl>
        <dt>{t("动作", "Action")}</dt><dd><code>{value.action}</code></dd>
        {value.url && <><dt>URL</dt><dd><code>{value.url}</code></dd></>}
        {value.selector && <><dt>{t("选择器", "Selector")}</dt><dd><code>{value.selector}</code></dd></>}
        {value.input_length > 0 && <><dt>{t("输入", "Input")}</dt><dd>{t(`${value.input_length} 个字符（内容不显示）`, `${value.input_length} characters (content hidden)`)}</dd></>}
        {value.artifact_bytes > 0 && <><dt>{t("产物", "Artifact")}</dt><dd>{byteSizeLabel(value.artifact_bytes)}</dd></>}
        <dt>{t("结果", "Result")}</dt><dd>{value.summary}</dd>
      </dl>;
    }
  }
}

function CommandDetail({ activityRef, client, command, threadID }: {
  activityRef: string;
  client: APIClient;
  command: ThreadActivityCommandDetail;
  threadID: string;
}) {
  const { locale, t } = useLocale();
  const duration = durationLabel(command.duration_ms, command.status);
  // This display-only projection has no executable/adapter identity. The
  // observed output can suggest a startup problem, never prove no side effects.
  const powerShellStartupError = command.status === "failed" && command.exit_code === 0xffff0000 &&
    command.stderr_preview?.startsWith("Windows PowerShell ") &&
    command.stderr_preview.includes("System.Management.Automation.Tracing.PSEtwLog");
  return <article className="v2-command-detail v2-shell-detail">
    <header>
      <strong>Shell</strong>
      <span className={isFailedStatus(command.status) ||
        (command.exit_code !== undefined && command.exit_code !== null && command.exit_code !== 0)
        ? "is-failed" : ""}><StatusLabel status={command.status} /></span>
    </header>
    <pre className="v2-command-input" aria-label={t("命令", "Command")}><code>{command.display_command}</code></pre>
    {powerShellStartupError && <p className="v2-command-truncated">
      {t("输出提示 PowerShell 初始化失败，当前检查未完成。若使用 Windows 隔离工作区，请安装 PowerShell 7，并在宿主环境中将 CYBERAGENT_POWERSHELL_PATH 设置为 pwsh.exe 的绝对路径，重启应用后重试。完整错误仍保留在下方。", "The output indicates PowerShell initialization failed, so this check is incomplete. For a Windows isolated workspace, install PowerShell 7 and set CYBERAGENT_POWERSHELL_PATH in the host environment to the absolute path of pwsh.exe. Restart the app and retry. The full error is kept below.")}</p>}
    {command.stdout_preview && <section aria-label={t("标准输出", "Standard output")}>
      <strong>stdout</strong><pre><code>{command.stdout_preview}</code></pre>
    </section>}
    {command.stderr_preview && <section aria-label={t("标准错误", "Standard error")}>
      <strong>stderr</strong><pre><code>{command.stderr_preview}</code></pre>
    </section>}
    <details className="v2-command-metadata">
      <summary><ChevronDown aria-hidden="true" size={12} /><span>{t("执行信息", "Execution details")}</span>
        <small>{command.exit_code !== undefined && command.exit_code !== null
          ? `Exit ${command.exit_code}` : <StatusLabel status={command.status} />}
          {duration ? ` · ${duration}` : ""}</small>
      </summary>
      <dl>
        {command.cwd && <><dt>{t("目录", "Directory")}</dt><dd><code>{command.cwd}</code></dd></>}
        {(command.agent_label || command.agent_id) && <><dt>Agent</dt><dd>
          <AgentIdentity id={command.agent_id} label={command.agent_label} />
        </dd></>}
        {command.environment_label && <><dt>{t("环境", "Environment")}</dt><dd>{command.environment_label}</dd></>}
      </dl>
    </details>
    {(command.stdout_preview?.includes("\uFFFD") || command.stderr_preview?.includes("\uFFFD")) &&
      <p className="v2-command-truncated">{outputDecodingNotice(t)}</p>}
    {command.truncated && <p className="v2-command-truncated">
      {t("输出过长，当前仅显示已脱敏预览。", "The output is too long. Only a redacted preview is shown.")}</p>}
    {command.artifacts.map((reference) => <SavedCommandOutput activityRef={activityRef}
      artifactRef={reference.artifact_ref} client={client} key={reference.artifact_ref}
      locale={locale} reference={reference} threadID={threadID} />)}
  </article>;
}

function WebEvidenceDetail({ item }: { item: ActivityItem }) {
  const { t } = useLocale();
  const evidence = item.webEvidence;
  if (!evidence) return null;
  return <article className="v2-command-detail v2-tool-facts">
    <header><strong>{item.title}</strong>
      <span className={isFailedStatus(item.status) ? "is-failed" : ""}><StatusLabel status={item.status} /></span>
    </header>
    <dl>
      {evidence.title && <><dt>{t("来源", "Source")}</dt><dd>{evidence.title}</dd></>}
      <dt>URL</dt><dd><code>{evidence.url}</code></dd>
      <dt>{t("证据", "Evidence")}</dt><dd>{evidence.citeable ? t("可引用", "Citeable") : t("不可引用", "Not citeable")}
        {evidence.partial ? t("· 部分内容", " · Partial content") : ""}{evidence.stale ? t("· 已过期", " · Expired") : ""}</dd>
    </dl>
    <p className="v2-untrusted-output">{t("网页内容作为参考资料展示。操作授权以任务要求和审批为准。", "Web content is shown as reference material. Task instructions and approvals determine action permissions.")}</p>
  </article>;
}

export function ActivityItemDetail({ client, item, threadID, initialOpen = false, expectedRunID, activity }: {
  client: APIClient;
  item: ActivityItem;
  threadID: string;
  initialOpen?: boolean;
  expectedRunID?: string;
  activity?: ActivityEntry["activity"];
}) {
  const { t } = useLocale();
  const failed = itemFailed(item);
  const [open, setOpen] = useState(initialOpen || failed);
  useEffect(() => {
    if (failed) setOpen(true);
  }, [failed]);
  const detailRef = item.detailAvailable ? item.detailRef : undefined;
  const fallbackAvailable = Boolean(item.webEvidence);
  const query = useQuery({
    queryKey: ["v2", "thread-activity-detail", threadID, detailRef, ...(expectedRunID ? [expectedRunID] : [])],
    queryFn: async ({ signal }) => {
      const detail = await client.threadActivityDetail(threadID, detailRef ?? "", signal);
      if (expectedRunID && detail.run_id !== expectedRunID) throw new Error("Activity detail Run identity does not match");
      return detail;
    },
    select: (detail) => projectThreadActivityDetail(detail, t),
    enabled: open && Boolean(detailRef),
    retry: false,
    refetchInterval: (activityQuery) => (activityQuery.state.data
      ? activityQuery.state.data.tools.some((tool) => isRunningStatus(tool.status) || (tool.detail.kind === "command" && tool.detail.command.commands.some((command) => isRunningStatus(command.status))))
      : itemRunning(item)) ? 1_000 : false,
    staleTime: Number.POSITIVE_INFINITY,
  });

  if (!detailRef && !fallbackAvailable) return <li className={`v2-activity-item${failed ? " is-failed" : ""}`}>
    {activity && <ActivityIcon activity={activity} />}
    <span className="v2-activity-item-label" title={item.title}>{item.title}</span>
    {item.detail && item.detail !== item.title && <small title={item.detail}>{item.detail}</small>}
    {item.status && <em><StatusLabel status={item.status} /></em>}
  </li>;

  return <li className={`v2-activity-item has-detail${failed ? " is-failed" : ""}`}>
    <details onToggle={(event) => setOpen(event.currentTarget.open)} open={open}>
      <summary aria-label={t(`${open ? "收起" : "查看"}${item.title}的执行详情`, `${open ? "Collapse" : "View"} execution details for ${item.title}`)}>
        {activity && <ActivityIcon activity={activity} />}
        <span className="v2-activity-item-label" title={item.summary?.command ?? item.title}>{item.summary?.command ?? item.title}</span>
        {itemSummaryMeta(item, t) && <small>{itemSummaryMeta(item, t)}</small>}
        {itemSummaryStatus(item) && <em>{itemSummaryStatus(item)}</em>}
        <ChevronDown aria-hidden="true" size={13} />
      </summary>
      <div className="v2-activity-detail-panel">
        {detailRef && query.isLoading && <p className="v2-activity-detail-state" role="status">
          <LoaderCircle aria-hidden="true" className="spin" size={14} />{t("正在加载执行详情…", "Loading execution details…")}</p>}
        {detailRef && query.isError && <div className="v2-activity-detail-state is-error" role="alert">
          <span>{t("执行详情加载失败。", "Could not load execution details.")}</span>
          <button onClick={() => void query.refetch()} type="button">{t("重试", "Retry")}</button>
        </div>}
        {query.data && query.data.commands.length === 0 && !query.data.typed && <p className="v2-activity-detail-state">
          {t("此活动没有可展示的命令详情。", "No command details are available for this activity.")}</p>}
        {query.data?.typed && <TypedToolDetail client={client} runID={query.data.run_id}
          typed={query.data.typed} />}
        {query.data?.commands.map((command, index) => <CommandDetail activityRef={detailRef ?? ""}
          client={client} command={command} key={`${command.display_command}:${index}`}
          threadID={threadID} />)}
        {!detailRef && <WebEvidenceDetail item={item} />}
      </div>
    </details>
  </li>;
}

export function V2ActivityGroup({ client, entry, threadID }: {
  client: APIClient;
  entry: ActivityEntry;
  threadID: string;
}) {
  const { t } = useLocale();
  const labels = { search: t("搜索", "Search"), read: t("读取", "Read"), edit: t("修改", "Edit"), execute: t("运行", "Run"), verify: t("验证", "Verify"), plan: t("规划", "Plan") };
  const latestItem = entry.items.at(-1);
  const fileChanges = entry.items.flatMap((item) => item.fileChange ? [item.fileChange] : []);
  const distinctChanges = new Set(fileChanges);
  // Tool completion confirms a request was handled, not that a file was written.
  // The transcript exposes event IDs, not a shared edit ID: retain the audit
  // leaves without presenting their count as a number of changed files.
  const label = entry.activity === "plan" ? t("规划", "Plan") : entry.activity === "edit"
    ? distinctChanges.size === 1 ? fileChangeLabel(fileChanges[0], t) : t("文件修改记录", "File edit records")
    : entry.count > 1 ? t(`${labels[entry.activity]}了 ${entry.count} 项`, `${labels[entry.activity]} · ${entry.count} items`) : labels[entry.activity];
  const detail = entry.activity === "edit"
    ? fileChanges.length > 1 ? fileChangeLabel(fileChanges.at(-1)!, t) : t("展开查看请求与审阅记录", "Expand to view requests and review records")
    : latestItem?.summary ? [latestItem.summary.command,
    itemSummaryStatus(latestItem), itemSummaryMeta(latestItem, t)].filter(Boolean).map((part, index) =>
      <Fragment key={index}>{index > 0 && " · "}{part}</Fragment>) :
    entry.detail || entry.title;
  const failed = entry.items.some(itemFailed);
  const [open, setOpen] = useState(failed);
  useEffect(() => {
    if (failed) setOpen(true);
  }, [failed]);
  return <details className={`v2-activity activity-${entry.activity}${entry.provisional
    ? " is-provisional" : ""}${failed ? " is-failed" : ""}`}
    onToggle={(event) => setOpen(event.currentTarget.open)} open={open}>
    <summary><ActivityIcon activity={entry.activity} />
      <span>{label}</span>
      <small>{detail}</small><ChevronDown aria-hidden="true" size={14} /></summary>
    <ul>{entry.items.map((item, index) => <ActivityItemDetail client={client} item={item} activity={entry.activity}
      key={`${entry.id}:${item.detailRef ?? index}`} threadID={threadID} />)}</ul>
  </details>;
}
