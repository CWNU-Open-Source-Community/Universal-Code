import { useLocale, type LocaleText } from "../../lib/locale";
import { useRef, useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Ban, Check, Globe2, LoaderCircle, ShieldAlert } from "lucide-react";
import type { APIClient } from "../../api/client";
import type { ApprovalDecisionControlRequestView, ApprovalPreviewView, ApprovalQueueItemView } from "../../api/types";
import type { FileEditReviewTarget } from "../../components/file-edit-panel";
import { ComputerUsePreview } from "../../components/computer-use-preview";
import { v2QueryKeys } from "../query-keys";

type Action = ApprovalDecisionControlRequestView["action"];
const fieldLabels = (t: LocaleText): Record<string, string> => ({ command: t("命令", "Command"), executable: t("可执行文件", "Executable"),
  arguments: t("参数（按顺序）", "Arguments (in order)"), requested_backend: t("提案环境", "Proposed environment"), operation: t("操作", "Operation "),
  parameters: t("精确参数", "Exact parameters"), summary: t("影响", "Impact"), path: t("文件", "File"), destination_path: t("目标文件", "Destination file"),
  url: t("网址", "URL"), host: t("主机", "Host"), effect: t("操作类型", "Action type"), review_scope: t("用途与风险范围", "Purpose and risk scope"), bounded_review: t("有界审批说明", "Bounded approval details"),
  app_id: t("目标应用", "Target app"), app_path: t("应用路径", "App path"), window_id: t("目标窗口", "Target window"), tool_name: t("操作工具", "Tool"), authorization_scope: t("授权范围", "Permission scope"),
  grant_ttl_seconds: t("原定有效秒数", "Original duration in seconds"), grant_max_uses: t("原定命令次数", "Original command limit"), grant_uses_remaining: t("剩余次数", "Uses remaining"), grant_expires_at: t("到期时间", "Expires at") });
const effectText = (t: LocaleText): Record<ApprovalPreviewView["effect"], string> => ({
  command_process: t("本次批准覆盖这批固定命令或本次标准输入，范围限于来源执行的进程。请按宿主文件与网络可能可达的范围评估影响；输入或权限变化后需重新审批。", "This approval covers these fixed commands or this standard input, limited to processes of the source run. Review the potential impact on host files and network access. Changed input or permissions require approval again."),
  mcp_server_and_tool: t("本次批准覆盖此服务的启动、能力发现和当前工具调用。外部影响仍待核实，请核对服务配置与参数；配置、参数或权限变化后需重新审批。", "This approval covers starting this service, discovering its capabilities and calling the current tool. Check its configuration and arguments for external effects. Changed configuration, arguments or permissions require approval again."),
  computer_use_app: t("“本次允许”仅批准当前操作；“本任务允许操作该应用”允许 Agent 在当前任务中继续点击、输入和滚动该应用。任务结束后授权失效。", "Allow once approves only this action. Allow this app for this task lets the agent continue clicking, typing and scrolling in the app until the task ends."),
  dry_run: t("批准后只记录这一次模拟执行，不启动真实进程。拒绝会终止这份提案。", "Approval records this simulated execution without starting a real process. Denying closes this proposal."),
  record_git_approval: t("批准后保存这份 Git 提案的单次授权。Git 操作在后续执行时会再次核对仓库、权限和预览；可在工作记录中查看进度。拒绝会关闭此提案的执行入口。", "Approval saves one-time authorization for this Git proposal. Execution rechecks the repository, permissions and preview. Follow progress in the work log. Denying prevents execution of this proposal."),
  file_review_required: t("这里可以拒绝这份编辑。批准、差异审阅和写入请使用任务的「审阅改动」入口。", "You can deny this edit here. Use Review changes to inspect, approve and apply it."),
  fetch_public_https: t("“允许一次”覆盖本次读取；“本对话允许”覆盖此主机在当前对话内的公开 HTTPS 读取。拒绝后，Agent 会收到本次读取的拒绝结果。", "Allow once covers this read. Allow for this conversation covers public HTTPS reads from this host in the current conversation. Denying reports the refusal to the agent."),
  browser_sensitive_action: t("本次批准覆盖当前页面上的这一次操作，Agent 随后继续处理。页面或权限变化后需重新确认操作。", "This approval covers this action on the current page, then the agent continues. Changed pages or permissions require approval again."),
  unavailable: t("此类操作暂不支持在这里批准，请回到对话查看对应的操作入口。", "This action cannot be approved here. Return to the conversation for its controls."),
});

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
      {t("无法读取待审批操作，请重试后再作决定。", "Could not load pending approvals. Retry before deciding.")}<button onClick={() => void query.refetch()} type="button">{t("重试审批队列", "Retry approval queue")}</button>
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
  const [reason, setReason] = useState("");
  const [requestedTTL, setGrantTTL] = useState(120);
  const [requestedUses, setGrantUses] = useState(2);
  const operationKeys = useRef(new Map<string, string>());
  const decisionPending = useRef(false);
  const preview = useQuery({
    queryKey: ["v2", "approval-preview", runID, item.id, item.version],
    queryFn: async ({ signal }) => {
      const value = await client.approvalPreview(runID, item.id, signal);
      if (item.run_id !== runID || value.run_id !== runID || value.approval_id !== item.id ||
        value.proposal_id !== item.proposal_id || value.tool_name !== item.tool_name ||
        value.workspace_id !== item.workspace_id) throw new Error(t("审批预览与当前操作不一致，请刷新。", "The approval preview does not match this action. Refresh it."));
      return value;
    },
    retry: false,
  });
  const webFetch = item.tool_name === "web_fetch";
  const computerUse = item.tool_name === "mcp_tool_call" && preview.data?.effect === "computer_use_app";
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
  return <article className="v2-approval-card">
    <header><span>{webFetch ? <Globe2 aria-hidden="true" size={17} />
      : <ShieldAlert aria-hidden="true" size={17} />}</span>
      <div><strong>{recovering ? webFetch ? t("恢复上次网页读取", "Resume previous web read") : t("恢复上次审批决定", "Resume previous approval decision") : webFetch ? t("允许读取这个网站？", "Allow reading this website?")
        : computerUse ? t("允许操作这个应用？", "Allow controlling this app?") : dryRun ? t("批准这次模拟执行？", "Approve this simulated execution?") : t("需要你的批准", "Approval required")}</strong>
        <small>{recovering ? item.status === "approved" ? t("已允许，等待恢复", "Allowed; waiting to resume") : t("已拒绝，等待恢复", "Denied; waiting to resume")
          : webFetch ? item.exact_target : computerUse ? "Computer Use" : item.tool_name}</small></div></header>
    {preview.isLoading && <p role="status">{t("正在读取这次操作的精确预览…", "Loading the exact action preview…")}</p>}
    {preview.isError && <div role="alert"><p>{t("操作预览读取失败，请重新读取后确认批准范围。", "Could not load the action preview. Reload it before confirming the approval scope.")}</p>
      <button onClick={() => void preview.refetch()} type="button">{t("重试操作预览", "Retry action preview")}</button></div>}
    {preview.data && <>
      {computerUse ? <ComputerUsePreview preview={preview.data} /> : <dl className="v2-approval-facts">
        {preview.data.working_directory && <div><dt>{t("工作目录", "Working directory")}</dt><dd>{t("提案绑定目录内", "Within the proposal's bound directory ")}<code>{preview.data.working_directory}</code></dd></div>}
        {preview.data.fields.map((field, index) => <div key={`${field.name}:${index}`}>
          <dt>{fieldLabels(t)[field.name] ?? field.name}</dt><dd><pre>{field.value}</pre></dd>
        </div>)}
      </dl>}
      {preview.data.workspace_id && <details><summary>{t("查看操作目录身份", "View action directory identity")}</summary><code>{preview.data.workspace_id}</code></details>}
      <p>{preview.data.effect === "file_review_required" && onReviewFile
        ? t("先查看这份编辑的差异，再决定是否批准和应用。", "Review this edit's diff before approving and applying it.")
        : computerUse && !item.allowed_actions.includes("approve_for_run_app")
          ? t("本次允许仅批准当前操作；后续需要授权的操作将再次询问。", "Allow once approves only this action. Later actions requiring permission will ask again.") : effectText(t)[preview.data.effect]}</p>
      {preview.data.redacted && <p>{t("敏感内容已脱敏，请按可见内容核对操作。", "Sensitive content is redacted. Review the action using the visible information.")}</p>}
      {(!preview.data.source_current || preview.data.truncated) && <p role="alert">
        {preview.data.truncated ? t("预览超过显示上限，需读取完整内容后再批准。", "The preview exceeds the display limit. Read the full content before approving.") : t("操作已变化或审批已处理，请刷新最新状态。", "The action changed or was already decided. Refresh its status.")}
        <button onClick={() => void preview.refetch()} type="button">{t("刷新操作预览", "Refresh action preview")}</button></p>}
    </>}
    {recovering && <p>{t("上次决定已经保存。点击“继续恢复”会沿用原决定和授权范围完成后续处理。", "The previous decision is saved. Resume uses the same decision and permission scope to finish processing.")}</p>}
    {client.hasApprovalControl && !recovering && item.allowed_actions.includes("deny") && <input
      aria-label={t(`${item.tool_name} 的拒绝原因`, `Reason for denying ${item.tool_name}`)} disabled={mutation.isPending} maxLength={2048}
      onChange={(event) => setReason(event.target.value)} placeholder={t("拒绝原因（可选）", "Reason for denial (optional)")} value={reason} />}
    {client.hasApprovalControl && item.allowed_actions.includes("approve_for_run") && <fieldset disabled={mutation.isPending}>
      <legend>{t("本次执行的限时、限次审批", "Time and usage limits for this run")}</legend>
      <p>{t("每条新命令确认后计入此用途与风险范围。已有授权沿用原次数与到期时间。", "Each confirmed command counts toward this purpose and risk scope. Existing grants retain their original limits and expiry.")}</p>
      <label>{t("有效秒数", "Duration in seconds")}<input aria-label={t("有界审批有效秒数", "Bounded approval duration in seconds")} type="number" min={1} max={900} value={grantTTL} disabled={existingGrant}
        onChange={(event) => setGrantTTL(event.target.valueAsNumber)} /></label>
      <label>{t("命令次数", "Command limit")}<input aria-label={t("有界审批命令次数", "Bounded approval command limit")} type="number" min={1} max={8} value={grantUses} disabled={existingGrant}
        onChange={(event) => setGrantUses(event.target.valueAsNumber)} /></label>
      <button className="primary" type="button" disabled={!canApprove || !Number.isInteger(grantTTL) || grantTTL < 1 || grantTTL > 900 || !Number.isInteger(grantUses) || grantUses < 1 || grantUses > 8}
        onClick={() => decide("approve_for_run")}>{t("确认本条命令并计入有界审批", "Confirm command within bounded approval")}</button>
    </fieldset>}
    <footer>
      {onReviewFile && preview.isSuccess && preview.data.effect === "file_review_required" && <button
        className="primary" disabled={preview.isFetching} onClick={(event) => onReviewFile({
          runID, editID: item.proposal_id, workspaceID: item.workspace_id }, event.currentTarget)} type="button">{t("审阅文件提案", "Review file proposal")}</button>}
      {client.hasApprovalControl && item.allowed_actions.includes("deny") && <button className="secondary" disabled={mutation.isPending}
        onClick={() => decide("deny")} type="button"><Ban aria-hidden="true" size={15} />
        {recovering ? t("继续恢复", "Resume") : t("拒绝", "Deny")}</button>}
      {client.hasApprovalControl && item.allowed_actions.includes("approve_once") && <button className="primary" disabled={!canApprove}
        onClick={() => decide("approve_once")} type="button">
        {mutation.isPending ? <LoaderCircle className="spin" size={15} /> : <Check aria-hidden="true" size={15} />}
        {recovering ? t("继续恢复", "Resume") : computerUse ? t("本次允许", "Allow once") : dryRun ? t("批准模拟一次", "Approve simulation once") : webFetch ? t("允许一次", "Allow once") : t("批准一次", "Approve once")}</button>}
      {client.hasApprovalControl && computerUse && item.allowed_actions.includes("approve_for_run_app") && <button className="primary"
        disabled={!canApprove} onClick={() => decide("approve_for_run_app")} type="button">
        <Check aria-hidden="true" size={15} />{t("本任务允许操作该应用", "Allow this app for this task")}</button>}
      {client.hasApprovalControl && webFetch && item.allowed_actions.includes("approve_for_thread") && <button className="primary"
        disabled={!canApprove} onClick={() => decide("approve_for_thread")} type="button">
        <Check aria-hidden="true" size={15} />{recovering ? t("继续恢复", "Resume") : t("本对话允许", "Allow for this conversation")}</button>}
    </footer>
    {mutation.isError && <p className="v2-inline-error" role="alert">{t("决定未确认，可以重试。", "The decision is unconfirmed. You can retry. ")}{mutation.error instanceof Error ? mutation.error.message : t("审批失败", "Approval failed")}</p>}
  </article>;
}
