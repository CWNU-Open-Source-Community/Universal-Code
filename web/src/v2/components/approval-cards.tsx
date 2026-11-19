import { useLocale, type LocaleText } from "../../lib/locale";
import { useId, useRef, useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { FileDiff, Globe2, LoaderCircle, ShieldAlert } from "lucide-react";
import type { APIClient } from "../../api/client";
import { isRecord } from "../../api/client/validation";
import type { ApprovalDecisionControlRequestView, ApprovalPreviewView, ApprovalQueueItemView } from "../../api/types";
import type { FileEditReviewTarget } from "../../components/file-edit-panel";
import { ComputerUsePreview } from "../../components/computer-use-preview";
import { ApprovalDenialReason, approvalToolLabel, fileApprovalLabel, readApprovalPreview } from "../../components/approval-controls";
import { v2QueryKeys } from "../query-keys";

type Action = ApprovalDecisionControlRequestView["action"];
const fieldLabels = (t: LocaleText): Record<string, string> => ({ command: t("命令", "Command"), executable: t("可执行文件", "Executable"),
  arguments: t("参数（按顺序）", "Arguments (in order)"), requested_backend: t("提案环境", "Proposed environment"), operation: t("操作", "Operation"),
  action: t("操作", "Action"), intent: t("命令请求", "Command request"), server: t("服务", "Server"), tool: t("工具", "Tool"),
  parameters: t("精确参数", "Exact parameters"), summary: t("影响", "Impact"), path: t("文件", "File"), destination_path: t("目标文件", "Destination file"),
  url: t("网址", "URL"), body_bytes: t("请求体大小（字节）", "Request body size (bytes)"), body_sha256: t("请求体 SHA-256", "Request body SHA-256"), expires_at: t("有效期至", "Expires at"),
  host: t("主机", "Host"), effect: t("操作类型", "Action type"), review_scope: t("用途与风险范围", "Purpose and risk scope"), bounded_review: t("有界审批说明", "Bounded approval details"),
  app_id: t("目标应用", "Target app"), app_path: t("应用路径", "App path"), window_id: t("目标窗口", "Target window"), tool_name: t("操作工具", "Tool"), authorization_scope: t("授权范围", "Permission scope"),
  grant_ttl_seconds: t("原定有效秒数", "Original duration in seconds"), grant_max_uses: t("原定命令次数", "Original command limit"), grant_uses_remaining: t("剩余次数", "Uses remaining"), grant_expires_at: t("到期时间", "Expires at") });
const effectText = (t: LocaleText): Record<ApprovalPreviewView["effect"], string> => ({
  command_process: t("仅限当前任务的这批命令或标准输入，可能访问宿主文件和网络。输入或权限变化后需重新批准。", "These commands or standard input are limited to this run and may reach host files and the network. Changed input or permissions need fresh approval."),
  mcp_server_and_tool: t("授权启动或连接此服务、发现能力并调用此工具。外部影响仍待核实，请核对服务配置与参数。", "Allows starting or connecting to this server, discovering capabilities, and calling this tool. Check the configuration and arguments for external effects."),
  computer_use_app: t("按任务授权后，Agent 可继续点击、输入和滚动此应用，直至任务结束。", "A task grant lets the agent click, type and scroll in this app until the task ends."),
  dry_run: t("仅记录模拟执行，不启动进程。", "Records the simulation without starting a process."),
  record_git_approval: t("单次授权。执行时将再次核对仓库、权限和预览。", "One-time authorization. Execution rechecks the repository, permissions and preview."),
  file_review_required: t("在任务的「审阅改动」中查看差异并批准。", "Open Review changes in the task to inspect the diff and approve."),
  fetch_public_https: t("对话授权仅覆盖此主机的公开 HTTPS 读取。", "Conversation access covers public HTTPS reads from this host."),
  browser_sensitive_action: t("仅限当前页面的这一次操作。页面或权限变化后需重新批准。", "Applies to this action on the current page. Changed pages or permissions need fresh approval."),
  unavailable: t("此类操作暂不支持在这里批准，请回到对话查看对应的操作入口。", "This action cannot be approved here. Return to the conversation for its controls."),
});

function ApprovalFields({ fields }: { fields: ApprovalPreviewView["fields"] }) {
  const { t } = useLocale();
  const labels = fieldLabels(t);
  if (!fields.length) return null;
  return <dl className="v2-approval-facts">{fields.map((field, index) => <div key={`${field.name}:${index}`}>
    <dt>{labels[field.name] ?? field.name}</dt><dd>{field.name === "intent" ? <CommandIntent value={field.value} /> : <pre>{field.value}</pre>}</dd>
  </div>)}</dl>;
}

function CommandIntent({ value }: { value: string }) {
  const { t } = useLocale();
  let payload: unknown;
  try { payload = JSON.parse(value); }
  catch { return <pre>{value}</pre>; }
  if (!isRecord(payload) || payload.version !== "command-runtime.v2" ||
    !["run", "start"].includes(String(payload.action)) || !Array.isArray(payload.commands) || !payload.commands.length ||
    !payload.commands.every((command): command is Record<string, unknown> => isRecord(command) &&
      (typeof command.script === "string" || typeof command.executable === "string"))) return <pre>{value}</pre>;
  const labels: Record<string, string> = {
    executable: t("可执行文件", "Executable"), arguments: t("参数", "Arguments"), profile: t("执行环境", "Shell or process"),
    working_directory: t("工作目录", "Working directory"), network: t("网络声明", "Network policy"), credentials: t("凭据权限", "Credentials"),
    environment: t("环境变量", "Environment"), initial_stdin: t("初始输入", "Initial input"), purpose: t("用途", "Purpose"),
    workspace_mode: t("工作区模式", "Workspace mode"), stdin_policy: t("输入模式", "Input mode"),
  };
  return <div className="v2-command-intent">
    <ol>{payload.commands.map((command, index) => <li key={index}>
      {typeof command.script === "string" && <pre>{command.script}</pre>}
      <dl>{Object.entries(labels).map(([key, label]) => {
        const content = command[key];
        if (content === undefined || (Array.isArray(content) && !content.length && key !== "arguments")) return null;
        return <div key={key}><dt>{label}</dt><dd><pre>{typeof content === "string" ? content : JSON.stringify(content)}</pre></dd></div>;
      })}</dl>
    </li>)}</ol>
    {payload.commands.length > 1 && typeof payload.failure_policy === "string" && <p>
      {t("批量命令失败策略", "Batch failure policy")} <code>{payload.failure_policy}</code></p>}
    <details><summary>{t("完整命令请求", "Full command request")}</summary><pre>{value}</pre></details>
  </div>;
}

export function V2ApprovalCards({ client, runID, threadID, onReviewFile }: {
  client: APIClient; runID: string; threadID: string;
  onReviewFile?: (target: FileEditReviewTarget, trigger: HTMLButtonElement) => void;
}) {
  const { t } = useLocale();
  const queryClient = useQueryClient();
  const [notice, setNotice] = useState("");
  const query = useQuery({
    queryKey: v2QueryKeys.approvals(runID),
    queryFn: ({ signal }) => client.approvalQueue(runID, signal),
    enabled: Boolean(runID),
    refetchInterval: 2_000,
  });
  const decided = (message: string) => {
    setNotice(message);
    void queryClient.invalidateQueries({ queryKey: v2QueryKeys.approvals(runID) });
    void queryClient.invalidateQueries({ queryKey: v2QueryKeys.thread(threadID) });
    void queryClient.invalidateQueries({ queryKey: v2QueryKeys.transcript(threadID) });
  };
  return <>
    {notice && <p className="v2-notice" role="status">{notice}</p>}
    {query.isLoading && <p className="v2-notice" role="status">{t("正在读取待审批操作…", "Loading pending approvals…")}</p>}
    {query.isError && <div className="v2-notice tone-warning" role="alert">
      {t("无法读取待审批操作，请重试后再作决定。", "Could not load pending approvals. Retry before deciding.")} {query.error instanceof Error && query.error.message}
      <button onClick={() => void query.refetch()} type="button">{t("重试审批队列", "Retry approval queue")}</button>
    </div>}
    {Boolean(query.data?.items.length) && <section aria-label={t("待处理审批", "Pending approvals")} className="v2-approval-stack">
      {!client.hasApprovalControl && <p className="v2-notice" role="status">
        {t("当前连接可以查看审批。需要批准、拒绝或恢复时，请使用具有审批控制权限的连接。", "This connection can view approvals. Use a connection with approval controls to approve, deny or resume.")}</p>}
      {query.data?.items.map((item) => <ApprovalCard client={client} item={item} key={item.id}
        onDecided={decided} runID={runID} onReviewFile={onReviewFile} />)}
      {query.data?.truncated && <p role="status">{t("待审批操作较多；处理后会继续显示其余操作。", "More approvals are waiting. Remaining actions appear as you process these.")}</p>}
    </section>}
  </>;
}

function ApprovalCard({ client, item, runID, onDecided, onReviewFile }: {
  client: APIClient; item: ApprovalQueueItemView; runID: string;
  onDecided: (message: string) => void;
  onReviewFile?: (target: FileEditReviewTarget, trigger: HTMLButtonElement) => void;
}) {
  const { t } = useLocale();
  const headingID = useId();
  const [reason, setReason] = useState("");
  const [requestedTTL, setGrantTTL] = useState(120);
  const [requestedUses, setGrantUses] = useState(2);
  const operationKeys = useRef(new Map<string, string>());
  const decisionPending = useRef(false);
  const preview = useQuery({
    queryKey: ["v2", "approval-preview", runID, item.id, item.version],
    queryFn: ({ signal }) => readApprovalPreview(client, item, runID, signal, t),
    retry: false,
  });
  const webFetch = item.tool_name === "web_fetch";
  const browserRequest = item.tool_name === "agent_browser_sensitive" && item.proposal_id.startsWith("browser-network-");
  const computerUse = item.tool_name === "mcp_tool_call" && preview.data?.effect === "computer_use_app";
  const fileReview = preview.data?.effect === "file_review_required";
  const fileTool = fileApprovalLabel(item.tool_name, t);
  const fields = preview.data?.fields ?? [];
  const filePath = fileReview ? fields.find(({ name }) => name === "path")?.value : undefined;
  const webHost = webFetch ? fields.find(({ name }) => name === "host")?.value || item.exact_target : undefined;
  const requestDetails = browserRequest ? fields.filter(({ name }) => name === "body_sha256" || name === "expires_at") : [];
  const boundedApproval = client.hasApprovalControl && item.allowed_actions.includes("approve_for_run");
  const grantFields = boundedApproval ? fields.filter(({ name }) => name === "bounded_review" || name.startsWith("grant_")) : [];
  const visibleFields = fields.filter((field) => {
    if (fileReview && (field.name === "path" || (field.name === "operation" && `${field.value}_file` === item.tool_name))) return false;
    if (webFetch && field.name === "host" && field.value === webHost) return false;
    return !requestDetails.includes(field) && !grantFields.includes(field);
  });
  const recovering = item.status === "approved" || item.status === "denied";
  const existingTTL = Number(preview.data?.fields.find((field) => field.name === "grant_ttl_seconds")?.value);
  const existingUses = Number(preview.data?.fields.find((field) => field.name === "grant_max_uses")?.value);
  const existingGrant = Number.isInteger(existingTTL) && existingTTL >= 1 && existingTTL <= 900 &&
    Number.isInteger(existingUses) && existingUses >= 1 && existingUses <= 8;
  const grantTTL = existingGrant ? existingTTL : requestedTTL;
  const grantUses = existingGrant ? existingUses : requestedUses;
  const mutation = useMutation({
    mutationFn: (action: Action) => {
      const denialReason = action === "deny" ? reason.trim() : "";
      const intent = `${item.id}:${action}:${denialReason}:${action === "approve_for_run" ? `${grantTTL}:${grantUses}` : ""}`;
      let key = operationKeys.current.get(intent);
      if (!key) {
        key = `v2-approval-${globalThis.crypto.randomUUID()}`;
        operationKeys.current.set(intent, key);
      }
      return client.decideApproval(runID, item.id, { version: "approval_control.v1", action,
        ...(denialReason ? { reason: denialReason } : {}),
        ...(action === "approve_for_run" ? { grant_ttl_seconds: grantTTL, grant_max_uses: grantUses } : {}) }, key);
    },
    onSuccess: (result, action) => {
      if (browserRequest) {
        onDecided(action === "deny" ? t("已拒绝这次网页请求。", "This web request was denied.") : t("已批准这次网页请求。", "This web request was approved."));
        return;
      }
      const continuation = result.continuation;
      const next = ["agent_browser_sensitive", "mcp_tool_call", "command_runtime"].includes(item.tool_name) ? continuation?.state === "queued"
        ? t("Agent 已接收继续处理的请求。", "The agent received the request to continue.")
        : continuation?.state === "completed" || continuation?.state === "waiting_approval"
          ? t("Agent 已继续处理，可在工作记录中查看结果。", "The agent continued. Check the work log for results.")
          : t("决定已保存，后续执行尚未完成；请查看工作记录后继续。", "The decision is saved, but execution is incomplete. Check the work log before continuing.")
        : webFetch ? result.retry_scheduled
        ? t("后台已安排继续处理，可在工作记录中查看结果。", "Processing is scheduled to continue. Check the work log for results.")
        : t("读取尚未恢复；可以发送消息让 Agent 继续。", "The read has not resumed. Send a message to ask the agent to continue.")
        : preview.data?.effect === "dry_run" && action !== "deny"
          ? t("模拟执行已记录，没有启动真实进程。", "Simulated execution recorded; no real process was started.")
          : action !== "deny" ? t("批准已记录，操作等待执行；可在工作记录中查看进度。", "Approval recorded. The action awaits execution; follow progress in the work log.") : t("此提案已关闭。", "This proposal is closed.");
      onDecided(`${action === "deny" ? t("已拒绝。", "Denied. ") : t("已批准。", "Approved. ")}${next}`);
    },
    onError: () => { void preview.refetch(); },
    onSettled: () => { decisionPending.current = false; },
  });
  const decide = (action: Action) => {
    if (decisionPending.current) return;
    decisionPending.current = true;
    mutation.mutate(action);
  };
  const canApprove = client.hasApprovalControl && preview.isSuccess && !preview.isFetching && preview.data.source_current &&
    !preview.data.truncated && !mutation.isPending;
  const dryRun = preview.data?.effect === "dry_run";
  const title = recovering ? webFetch ? t("恢复上次网页读取", "Resume previous web read") : t("恢复上次审批决定", "Resume previous approval decision")
    : browserRequest ? t("网页请求", "Web request") : webFetch ? t("允许读取这个网站？", "Allow reading this website?")
    : computerUse ? t("允许操作这个应用？", "Allow controlling this app?") : dryRun ? t("批准这次模拟执行？", "Approve this simulated execution?")
    : approvalToolLabel(item.tool_name, t);
  const Icon = webFetch || browserRequest ? Globe2 : fileReview ? FileDiff : ShieldAlert;
  const workingDirectory = preview.data?.working_directory;
  const canDeny = client.hasApprovalControl && item.allowed_actions.includes("deny");
  return <article aria-labelledby={headingID} className="v2-approval-card">
    <header><Icon aria-hidden="true" size={16} />
      <h3 id={headingID}><span>{title}</span>{filePath && <code>{filePath}</code>}{webHost && <code>{webHost}</code>}</h3>
    </header>
    {recovering && <p>{item.status === "approved" ? t("已允许，等待恢复", "Allowed; waiting to resume") : t("已拒绝，等待恢复", "Denied; waiting to resume")}</p>}
    {preview.isLoading && <p role="status">{t("正在读取这次操作的精确预览…", "Loading the exact action preview…")}</p>}
    {preview.isError && <div role="alert"><p>{t("操作预览读取失败，请重新读取后确认批准范围。", "Could not load the action preview. Reload it before confirming the approval scope.")}</p>
      {preview.error instanceof Error && <p>{preview.error.message}</p>}
      <button onClick={() => void preview.refetch()} type="button">{t("重试操作预览", "Retry action preview")}</button></div>}
    {preview.data && <>
      {workingDirectory && (!fileReview || workingDirectory !== ".") && <dl className="v2-approval-facts">
        <div><dt>{t("工作目录", "Working directory")}</dt><dd><code>{workingDirectory}</code></dd></div></dl>}
      {computerUse ? <ComputerUsePreview preview={preview.data} /> : <ApprovalFields fields={visibleFields} />}
      {requestDetails.length > 0 && <details><summary>{t("请求详情", "Request details")}</summary>
        <ApprovalFields fields={requestDetails} /></details>}
      {!browserRequest && !(fileReview && onReviewFile) && <p>{computerUse && !item.allowed_actions.includes("approve_for_run_app")
          ? t("本次允许仅批准当前操作；后续需要授权的操作将再次询问。", "Allow once approves only this action. Later actions requiring permission will ask again.")
          : webFetch && !item.allowed_actions.includes("approve_for_thread") ? t("仅限此网址的一次公开 HTTPS 读取。", "One public HTTPS read of this URL.")
          : effectText(t)[preview.data.effect]}</p>}
      {preview.data.redacted && <p>{t("敏感内容已脱敏，请按可见内容核对操作。", "Sensitive content is redacted. Review the action using the visible information.")}</p>}
      {(!preview.data.source_current || preview.data.truncated) && <p role="alert">
        {preview.data.truncated ? t("预览超过显示上限，需读取完整内容后再批准。", "The preview exceeds the display limit. Read the full content before approving.") : t("操作已变化或审批已处理，请刷新最新状态。", "The action changed or was already decided. Refresh its status.")}
        <button onClick={() => void preview.refetch()} type="button">{t("刷新操作预览", "Refresh action preview")}</button></p>}
    </>}
    {recovering && <p>{t("上次决定已经保存。点击“继续恢复”会沿用原决定和授权范围完成后续处理。", "The previous decision is saved. Resume uses the same decision and permission scope to finish processing.")}</p>}
    {boundedApproval && <details><summary>{t("本次执行的限时、限次审批", "Time and usage limits for this run")}</summary>
      <fieldset aria-label={t("授权限额", "Grant limits")} className="v2-approval-grant" disabled={mutation.isPending}>
      <ApprovalFields fields={grantFields.filter(({ name }) => !existingGrant || (name !== "grant_ttl_seconds" && name !== "grant_max_uses"))} />
      {!grantFields.some(({ name }) => name === "bounded_review") && <p>{t("每条新命令仍需确认。", "Each new command still requires confirmation.")}</p>}
      <label>{t("有效秒数", "Duration in seconds")}<input aria-label={t("有界审批有效秒数", "Bounded approval duration in seconds")} type="number" min={1} max={900} value={grantTTL} disabled={existingGrant}
        onChange={(event) => setGrantTTL(event.target.valueAsNumber)} /></label>
      <label>{t("命令次数", "Command limit")}<input aria-label={t("有界审批命令次数", "Bounded approval command limit")} type="number" min={1} max={8} value={grantUses} disabled={existingGrant}
        onChange={(event) => setGrantUses(event.target.valueAsNumber)} /></label>
      <button className="secondary" type="button" disabled={!canApprove || !Number.isInteger(grantTTL) || grantTTL < 1 || grantTTL > 900 || !Number.isInteger(grantUses) || grantUses < 1 || grantUses > 8}
        onClick={() => decide("approve_for_run")}>{t("确认本条命令并计入有界审批", "Confirm command within bounded approval")}</button>
    </fieldset></details>}
    <footer>
      {onReviewFile && preview.isSuccess && preview.data.effect === "file_review_required" && <button
        className="primary" disabled={preview.isFetching} onClick={(event) => onReviewFile({
          runID, editID: item.proposal_id, workspaceID: item.workspace_id }, event.currentTarget)} type="button">{t("审阅文件提案", "Review file proposal")}</button>}
      {client.hasApprovalControl && !fileTool && !fileReview && item.allowed_actions.includes("approve_once") && <button className="primary" disabled={!canApprove}
        onClick={() => decide("approve_once")} type="button">
        {mutation.isPending && mutation.variables === "approve_once" && <LoaderCircle aria-hidden="true" className="spin" size={14} />}
        {recovering ? t("继续恢复", "Resume") : computerUse ? t("本次允许", "Allow once") : dryRun ? t("批准模拟一次", "Approve simulation once") : webFetch ? t("允许一次", "Allow once") : t("批准一次", "Approve once")}</button>}
      {client.hasApprovalControl && computerUse && item.allowed_actions.includes("approve_for_run_app") && <button className="secondary"
        disabled={!canApprove} onClick={() => decide("approve_for_run_app")} type="button">
        {t("本任务允许操作该应用", "Allow this app for this task")}</button>}
      {client.hasApprovalControl && webFetch && item.allowed_actions.includes("approve_for_thread") && <button className="secondary"
        disabled={!canApprove} onClick={() => decide("approve_for_thread")} type="button">
        {recovering ? t("继续恢复", "Resume") : t("本对话允许", "Allow for this conversation")}</button>}
      {canDeny && <button className="secondary" disabled={mutation.isPending}
        onClick={() => decide("deny")} type="button">{recovering ? t("继续恢复", "Resume") : t("拒绝", "Deny")}</button>}
      {canDeny && !recovering && <ApprovalDenialReason disabled={mutation.isPending} onChange={setReason} value={reason}
        label={browserRequest ? t("网页请求的拒绝原因", "Reason for denying the web request") : t(`${item.tool_name} 的拒绝原因`, `Reason for denying ${item.tool_name}`)} />}
      {preview.data?.workspace_id && <details className="v2-approval-workspace"><summary>{t("工作区", "Workspace")}</summary>
        <code>{preview.data.workspace_id}</code></details>}
    </footer>
    {mutation.isError && <p className="v2-inline-error" role="alert">{t("决定未确认，可以重试。", "The decision is unconfirmed. You can retry. ")}{mutation.error instanceof Error ? mutation.error.message : t("审批失败", "Approval failed")}</p>}
  </article>;
}
