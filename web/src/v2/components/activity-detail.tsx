import { Fragment, useEffect, useMemo, useState, type ReactNode } from "react";
import { useQuery } from "@tanstack/react-query";
import { CheckCircle2, ChevronDown, ClipboardList, FilePenLine, LoaderCircle, Search,
  TerminalSquare } from "lucide-react";
import type { APIClient } from "../../api/client";
import type { ThreadActivityArtifactReferenceView, ThreadActivityDetailView as APIThreadActivityDetailView,
  ThreadActivityBoundaryView, ThreadActivityJSONFieldSummaryView,
  ThreadActivityCommandDetailView, ThreadActivityToolDetailView, ThreadActivityTypedDetailView } from "../../api/types";
import { fileChangeLabels, type NarrativeEntry } from "../projection/narrative";
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
const outputDecodingNotice = "部分输出字符无法正确解码，当前文本可能不完整。";

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

function itemSummaryMeta(item: ActivityItem): string {
  const summary = item.summary;
  if (!summary) return item.detail;
  const duration = durationLabel(summary.duration_milliseconds, summary.status);
  const count = summary.command_count > 1 ? `${summary.command_count} 条命令` : "";
  return [count, duration].filter(Boolean).join(" · ");
}

function itemSummaryStatus(item: ActivityItem): ReactNode {
  const summary = item.summary;
  if (!summary) return item.status ? <StatusLabel status={item.status} /> : "";
  if (summary.exit_code !== undefined) return `Exit ${summary.exit_code}`;
  return summary.status ? <StatusLabel status={summary.status} /> : "";
}

function commandEnvironmentLabel(command: Pick<ThreadActivityCommandDetailView, "execution_environment" | "network">): string {
  // The requested network setting does not establish isolation on the host.
  const network = command.execution_environment === "Host · Full Access" ? "宿主网络未隔离"
    : command.execution_environment === "Workspace Sandbox" && command.network === "disabled" ? "无网络"
    : "网络隔离未确认";
  return `${command.execution_environment} · ${network}`;
}

/** Maps the already strictly parsed Go projection into conversation labels. */
export function projectThreadActivityDetail(
  value: APIThreadActivityDetailView,
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
      environment_label: commandEnvironmentLabel(command),
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
      environment_label: commandEnvironmentLabel(command),
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
  if (id === "unknown") return <>{label || "历史活动（执行者未知）"}</>;
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

function typedKindLabel(kind: ThreadActivityTypedDetailView["kind"]): string {
  const labels: Record<ThreadActivityTypedDetailView["kind"], string> = {
    command: "运行命令",
    web_search: "联网搜索", web_fetch: "网页读取",
    file_read: "文件读取", file_edit: "文件修改", verification: "验证", mcp: "MCP",
    browser: "浏览器",
  };
  return labels[kind];
}

const maxRenderedDiffLines = 2_000;

function FileEditDiff({ client, editID, runID }: {
  client: APIClient;
  editID: string;
  runID: string;
}) {
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
      {open ? "收起 Diff" : "查看 Diff"}
    </button>
    {open && <div className="v2-file-edit-diff-content">
      {preview.data && <details><summary>查看此编辑的原目录身份</summary><code>{preview.data.workspace_id}</code></details>}
      {preview.isLoading && <p className="v2-activity-detail-state" role="status">
        <LoaderCircle aria-hidden="true" className="spin" size={14} />正在读取文件 Diff…</p>}
      {preview.isError && <div className="v2-activity-detail-state is-error" role="alert">
        <span>Diff 加载失败。</span>
        <button onClick={() => void preview.refetch()} type="button">重试</button>
      </div>}
      {preview.data && preview.data.diff === "" && <p className="v2-activity-detail-state">
        该文件修改没有文本 Diff。</p>}
      {preview.data && parsed && preview.data.diff !== "" && <>
        <div aria-label={`文件 Diff：${preview.data.path}`} className="v2-unified-diff" role="table">
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
        <small>{preview.data.secrets_redacted ? "敏感内容已脱敏" : "未发现需脱敏内容"}
          {displayTruncated
            ? ` · Diff 共 ${parsed.lines.length} 行，为保持界面流畅仅显示前 ${maxRenderedDiffLines} 行`
            : ` · ${parsed.additions} 行新增，${parsed.deletions} 行删除`}</small>
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
  return <>
    <dt>授权</dt><dd>{boundary.authorization === "policy_checked" ? "已通过策略检查" :
      boundary.authorization === "pending" ? "等待授权" : "已拒绝"}</dd>
    {boundary.error_code && <><dt>错误</dt><dd><code>{boundary.error_code}</code></dd></>}
    {boundary.failure_reason && <><dt>原因</dt><dd>{boundary.failure_reason}</dd></>}
    {boundary.truncated && <><dt>内容</dt><dd>已截断</dd></>}
  </>;
}

function TypedToolDetail({ client, runID, typed }: {
  client: APIClient;
  runID: string;
  typed: NonNullable<ThreadActivityDetailView["typed"]>;
}) {
  const detail = typed.detail;
  const duration = durationLabel(typed.duration_ms, typed.status);
  const { boundary, operation } = typedDetailCommon(detail);
  return <article className="v2-command-detail v2-tool-facts">
    <header><strong>{typedKindLabel(detail.kind)} · {operation}</strong>
      <span className={isFailedStatus(typed.status) ? "is-failed" : ""}><StatusLabel status={typed.status} /></span>
    </header>
    <dl>
      <dt>Agent</dt><dd><AgentIdentity id={typed.agent_id} label={typed.agent_label} /></dd>
      <BoundaryFacts boundary={boundary} />
      {duration && <><dt>耗时</dt><dd>{duration}</dd></>}
    </dl>
    <TypedDetailBody client={client} detail={detail} runID={runID} status={typed.status} />
    {boundary.untrusted && <p className="v2-untrusted-output">
      结果来自外部来源，作为参考资料展示。操作授权以任务要求和审批为准。</p>}
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
  switch (detail.kind) {
    case "web_search": {
      const value = detail.web_search;
      return <><dl>
        <dt>查询</dt><dd><code>{value.query}</code></dd>
        {value.provider && <><dt>提供商</dt><dd>{value.provider}</dd></>}
        {value.search_policy && <><dt>搜索策略</dt><dd>{value.search_policy}</dd></>}
        {value.selection_reason && <><dt>路由原因</dt><dd>{value.selection_reason}</dd></>}
        <dt>结果</dt><dd>{isFailedStatus(status) && value.source_count === 0 ? "未取得搜索结果"
          : `${value.source_count} 个来源 · ${value.citeable ? "可引用" : "待验证"}`}</dd>
      </dl>{value.sources.length > 0 && <section className="v2-tool-facts-list" aria-label="搜索来源">
        <strong>搜索来源</strong><ol>{value.sources.map((source) => <li key={`${source.rank}:${source.url}`}>
          <a href={source.url} rel="noreferrer" target="_blank">{source.title || source.url}</a>
          <small>{source.provider || "未知提供商"} · {source.citeable ? "可引用" : "待验证"}</small>
        </li>)}</ol>
      </section>}</>;
    }
    case "web_fetch": {
      const value = detail.web_fetch;
      return <dl>
        <dt>URL</dt><dd>{value.url.startsWith("https://")
          ? <a href={value.url} rel="noreferrer" target="_blank">{value.url}</a>
          : <code>{value.url}</code>}</dd>
        <dt>抓取状态</dt><dd><StatusLabel status={value.state || "unknown"} />
          {value.http_status ? ` · HTTP ${value.http_status}` : ""}</dd>
        <dt>Robots</dt><dd>{value.robots || "未记录"}
          {value.robots_policy ? ` · ${value.robots_policy}` : ""}</dd>
        <dt>重定向</dt><dd>{value.redirects}</dd>
        <dt>证据</dt><dd>{value.citeable ? "可引用" : "不可引用"}
          {value.partial ? " · 部分内容" : ""}</dd>
      </dl>;
    }
    case "file_read": {
      const value = detail.file_read;
      return <dl>
        {value.path && <><dt>路径</dt><dd><code>{value.path}</code></dd></>}
        {value.query && <><dt>查询</dt><dd><code>{value.query}</code></dd></>}
        {value.pattern && <><dt>范围</dt><dd><code>{value.pattern}</code></dd></>}
        {value.start_line > 0 && <><dt>行范围</dt><dd>{value.start_line}–{value.end_line}</dd></>}
        <dt>结果</dt><dd>{value.summary}</dd>
      </dl>;
    }
    case "file_edit": {
      const value = detail.file_edit;
      return <><dl>
        <dt>操作</dt><dd>{value.action || "修改"}</dd>
        {value.path && <><dt>文件</dt><dd><code>{value.path}</code></dd></>}
        {value.destination_path && <><dt>目标</dt><dd><code>{value.destination_path}</code></dd></>}
        <dt>Diff</dt><dd>{value.diff.summary ||
          `+${value.diff.added_lines} −${value.diff.removed_lines} · ${value.diff.hunks} 个区块`}</dd>
        <dt>应用结果</dt><dd>{value.apply_status || (value.applied ? "已应用" : "尚未应用")}
          {value.replayed ? " · 幂等重放" : ""}</dd>
      </dl>{value.diff_available
        ? <FileEditDiff client={client} editID={value.edit_id} runID={runID} />
        : <p className="v2-activity-detail-state">本次活动没有可展示的 Diff。</p>}</>;
    }
    case "mcp": {
      const value = detail.mcp;
      return <><dl>
        <dt>服务器</dt><dd><code>{value.server}</code></dd>
        <dt>工具</dt><dd><code>{value.tool}</code></dd>
        <dt>结果摘要</dt><dd>{value.result.summary}</dd>
      </dl>
      <SafeJSONFieldList label="参数" values={value.arguments} />
      <SafeJSONFieldList label="结果字段" values={value.result.fields} />
      <MCPContentPreview contents={value.contents} omitted={value.omitted_contents} truncated={value.content_truncated} /></>;
    }
    case "verification": {
      const value = detail.verification;
      return <dl>
        <dt>工具</dt><dd><code>{value.tool}</code></dd>
        {value.path && <><dt>文件</dt><dd><code>{value.path}</code></dd></>}
        {value.query && <><dt>查询</dt><dd><code>{value.query}</code></dd></>}
        {value.position && <><dt>位置</dt><dd>{value.position}</dd></>}
        <dt>结果</dt><dd>{value.summary}</dd>
      </dl>;
    }
    case "browser": {
      const value = detail.browser;
      return <dl>
        <dt>动作</dt><dd><code>{value.action}</code></dd>
        {value.url && <><dt>URL</dt><dd><code>{value.url}</code></dd></>}
        {value.selector && <><dt>选择器</dt><dd><code>{value.selector}</code></dd></>}
        {value.input_length > 0 && <><dt>输入</dt><dd>{value.input_length} 个字符（内容不显示）</dd></>}
        {value.artifact_bytes > 0 && <><dt>产物</dt><dd>{byteSizeLabel(value.artifact_bytes)}</dd></>}
        <dt>结果</dt><dd>{value.summary}</dd>
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
    <pre className="v2-command-input" aria-label="命令"><code>{command.display_command}</code></pre>
    {powerShellStartupError && <p className="v2-command-truncated">
      输出提示 PowerShell 初始化失败，当前检查未完成。若使用 Windows 隔离工作区，请安装 PowerShell 7，并在宿主环境中将 CYBERAGENT_POWERSHELL_PATH 设置为 pwsh.exe 的绝对路径，重启应用后重试。完整错误仍保留在下方。
    </p>}
    {command.stdout_preview && <section aria-label="标准输出">
      <strong>stdout</strong><pre><code>{command.stdout_preview}</code></pre>
    </section>}
    {command.stderr_preview && <section aria-label="标准错误">
      <strong>stderr</strong><pre><code>{command.stderr_preview}</code></pre>
    </section>}
    <details className="v2-command-metadata">
      <summary><ChevronDown aria-hidden="true" size={12} /><span>执行信息</span>
        <small>{command.exit_code !== undefined && command.exit_code !== null
          ? `Exit ${command.exit_code}` : <StatusLabel status={command.status} />}
          {duration ? ` · ${duration}` : ""}</small>
      </summary>
      <dl>
        {command.cwd && <><dt>目录</dt><dd><code>{command.cwd}</code></dd></>}
        {(command.agent_label || command.agent_id) && <><dt>Agent</dt><dd>
          <AgentIdentity id={command.agent_id} label={command.agent_label} />
        </dd></>}
        {command.environment_label && <><dt>环境</dt><dd>{command.environment_label}</dd></>}
      </dl>
    </details>
    {(command.stdout_preview?.includes("\uFFFD") || command.stderr_preview?.includes("\uFFFD")) &&
      <p className="v2-command-truncated">{outputDecodingNotice}</p>}
    {command.truncated && <p className="v2-command-truncated">
      输出过长，当前仅显示已脱敏预览。</p>}
    {command.artifacts.map((reference) => <SavedCommandOutput activityRef={activityRef}
      artifactRef={reference.artifact_ref} client={client} key={reference.artifact_ref}
      reference={reference} threadID={threadID} />)}
  </article>;
}

function WebEvidenceDetail({ item }: { item: ActivityItem }) {
  const evidence = item.webEvidence;
  if (!evidence) return null;
  return <article className="v2-command-detail v2-tool-facts">
    <header><strong>{item.title}</strong>
      <span className={isFailedStatus(item.status) ? "is-failed" : ""}><StatusLabel status={item.status} /></span>
    </header>
    <dl>
      {evidence.title && <><dt>来源</dt><dd>{evidence.title}</dd></>}
      <dt>URL</dt><dd><code>{evidence.url}</code></dd>
      <dt>证据</dt><dd>{evidence.citeable ? "可引用" : "不可引用"}
        {evidence.partial ? " · 部分内容" : ""}{evidence.stale ? " · 已过期" : ""}</dd>
    </dl>
    <p className="v2-untrusted-output">网页内容作为参考资料展示。操作授权以任务要求和审批为准。</p>
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
      return projectThreadActivityDetail(detail);
    },
    enabled: open && Boolean(detailRef),
    retry: false,
    refetchInterval: (activityQuery) => (activityQuery.state.data
      ? activityQuery.state.data.commands.some((command) => isRunningStatus(command.status)) ||
        Boolean(activityQuery.state.data.typed && isRunningStatus(activityQuery.state.data.typed.status))
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
      <summary aria-label={`${open ? "收起" : "查看"}${item.title}的执行详情`}>
        {activity && <ActivityIcon activity={activity} />}
        <span className="v2-activity-item-label" title={item.summary?.command ?? item.title}>{item.summary?.command ?? item.title}</span>
        {itemSummaryMeta(item) && <small>{itemSummaryMeta(item)}</small>}
        {itemSummaryStatus(item) && <em>{itemSummaryStatus(item)}</em>}
        <ChevronDown aria-hidden="true" size={13} />
      </summary>
      <div className="v2-activity-detail-panel">
        {detailRef && query.isLoading && <p className="v2-activity-detail-state" role="status">
          <LoaderCircle aria-hidden="true" className="spin" size={14} />正在加载执行详情…</p>}
        {detailRef && query.isError && <div className="v2-activity-detail-state is-error" role="alert">
          <span>执行详情加载失败。</span>
          <button onClick={() => void query.refetch()} type="button">重试</button>
        </div>}
        {query.data && query.data.commands.length === 0 && !query.data.typed && <p className="v2-activity-detail-state">
          此活动没有可展示的命令详情。</p>}
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
  const labels = { search: "搜索", read: "读取", edit: "修改", execute: "运行", verify: "验证", plan: "规划" };
  const latestItem = entry.items.at(-1);
  const fileChanges = entry.items.flatMap((item) => item.fileChange ? [item.fileChange] : []);
  const distinctChanges = new Set(fileChanges);
  // Tool completion confirms a request was handled, not that a file was written.
  // The transcript exposes event IDs, not a shared edit ID: retain the audit
  // leaves without presenting their count as a number of changed files.
  const label = entry.activity === "plan" ? "规划" : entry.activity === "edit"
    ? distinctChanges.size === 1 ? fileChangeLabels[fileChanges[0]] : "文件修改记录"
    : `${labels[entry.activity]}${entry.count > 1 ? `了 ${entry.count} 项` : ""}`;
  const detail = entry.activity === "edit"
    ? fileChanges.length > 1 ? fileChangeLabels[fileChanges.at(-1)!] : "展开查看请求与审阅记录"
    : latestItem?.summary ? [latestItem.summary.command,
    itemSummaryStatus(latestItem), itemSummaryMeta(latestItem)].filter(Boolean).map((part, index) =>
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
