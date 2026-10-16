import { useRef, useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Ban, Check, Globe2, LoaderCircle, ShieldAlert } from "lucide-react";
import type { APIClient } from "../../api/client";
import type { ApprovalDecisionControlRequestView, ApprovalPreviewView, ApprovalQueueItemView } from "../../api/types";
import type { FileEditReviewTarget } from "../../components/file-edit-panel";
import { v2QueryKeys } from "../query-keys";

type Action = ApprovalDecisionControlRequestView["action"];
const fieldLabels: Record<string, string> = { command: "命令", executable: "可执行文件",
  arguments: "参数（按顺序）", requested_backend: "提案环境", operation: "操作",
  parameters: "精确参数", summary: "影响", path: "文件", destination_path: "目标文件",
  url: "网址", host: "主机", effect: "操作类型", review_scope: "用途与风险范围", bounded_review: "有界审批说明",
  app_id: "目标应用", app_path: "应用路径", window_id: "目标窗口", tool_name: "操作工具", authorization_scope: "授权范围",
  grant_ttl_seconds: "原定有效秒数", grant_max_uses: "原定命令次数", grant_uses_remaining: "剩余次数", grant_expires_at: "到期时间" };
const effectText: Record<ApprovalPreviewView["effect"], string> = {
  command_process: "本次批准覆盖这批固定命令或本次标准输入，范围限于来源执行的进程。请按宿主文件与网络可能可达的范围评估影响；输入或权限变化后需重新审批。",
  mcp_server_and_tool: "本次批准覆盖此服务的启动、能力发现和当前工具调用。外部影响仍待核实，请核对服务配置与参数；配置、参数或权限变化后需重新审批。",
  computer_use_app: "“本次允许”仅批准当前操作；“本任务允许操作该应用”允许 Agent 在当前任务中继续点击、输入和滚动该应用。任务结束后授权失效。",
  dry_run: "批准后只记录这一次模拟执行，不启动真实进程。拒绝会终止这份提案。",
  record_git_approval: "批准后保存这份 Git 提案的单次授权。Git 操作在后续执行时会再次核对仓库、权限和预览；可在工作记录中查看进度。拒绝会关闭此提案的执行入口。",
  file_review_required: "这里可以拒绝这份编辑。批准、差异审阅和写入请使用任务的「审阅改动」入口。",
  fetch_public_https: "“允许一次”覆盖本次读取；“本对话允许”覆盖此主机在当前对话内的公开 HTTPS 读取。拒绝后，Agent 会收到本次读取的拒绝结果。",
  browser_sensitive_action: "本次批准覆盖当前页面上的这一次操作，Agent 随后继续处理。页面或权限变化后需重新确认操作。",
  unavailable: "此类操作暂不支持在这里批准，请回到对话查看对应的操作入口。",
};

export function V2ApprovalCards({ client, runID, threadID, onReviewFile }: {
  client: APIClient; runID: string; threadID: string;
  onReviewFile?: (target: FileEditReviewTarget, trigger: HTMLButtonElement) => void;
}) {
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
    {query.isLoading && <p className="v2-notice" role="status">正在读取待审批操作…</p>}
    {query.isError && <div className="v2-notice tone-warning" role="alert">
      无法读取待审批操作，请重试后再作决定。
      <button onClick={() => void query.refetch()} type="button">重试审批队列</button>
    </div>}
    {query.isSuccess && query.data.items.length === 0 && <p className="v2-notice" role="status">需要你确认的操作会显示在这里。</p>}
    {Boolean(query.data?.items.length) && <section aria-label="待处理审批" className="v2-approval-stack">
      {!client.hasApprovalControl && <p className="v2-notice" role="status">
        当前连接可以查看审批。需要批准、拒绝或恢复时，请使用具有审批控制权限的连接。
      </p>}
      {query.data?.items.map((item) => <ApprovalCard client={client} item={item} key={item.id}
        onDecided={decided} runID={runID} onReviewFile={onReviewFile} />)}
      {query.data?.truncated && <p role="status">待审批操作较多；处理后会继续显示其余操作。</p>}
    </section>}
  </>;
}

function ApprovalCard({ client, item, runID, onDecided, onReviewFile }: {
  client: APIClient; item: ApprovalQueueItemView; runID: string;
  onDecided: (message: string) => void;
  onReviewFile?: (target: FileEditReviewTarget, trigger: HTMLButtonElement) => void;
}) {
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
        value.workspace_id !== item.workspace_id) throw new Error("审批预览与当前操作不一致，请刷新。");
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
        ? "Agent 已接收继续处理的请求。"
        : continuation?.state === "completed" || continuation?.state === "waiting_approval"
          ? "Agent 已继续处理，可在工作记录中查看结果。"
          : "决定已保存，后续执行尚未完成；请查看工作记录后继续。"
        : webFetch ? result.retry_scheduled
        ? "后台已安排继续处理，可在工作记录中查看结果。"
        : "读取尚未恢复；可以发送消息让 Agent 继续。"
        : preview.data?.effect === "dry_run" && action !== "deny"
          ? "模拟执行已记录，没有启动真实进程。"
          : action !== "deny" ? "批准已记录，操作等待执行；可在工作记录中查看进度。" : "此提案已关闭。";
      onDecided(`${action === "deny" ? "已拒绝。" : "已批准。"}${next}`);
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
      <div><strong>{recovering ? webFetch ? "恢复上次网页读取" : "恢复上次审批决定" : webFetch ? "允许读取这个网站？"
        : computerUse ? "允许操作这个应用？" : dryRun ? "批准这次模拟执行？" : "需要你的批准"}</strong>
        <small>{recovering ? item.status === "approved" ? "已允许，等待恢复" : "已拒绝，等待恢复"
          : webFetch ? item.exact_target : item.tool_name}</small></div></header>
    {preview.isLoading && <p role="status">正在读取这次操作的精确预览…</p>}
    {preview.isError && <div role="alert"><p>操作预览读取失败，请重新读取后确认批准范围。</p>
      <button onClick={() => void preview.refetch()} type="button">重试操作预览</button></div>}
    {preview.data && <>
      <dl className="v2-approval-facts">
        {preview.data.working_directory && <div><dt>工作目录</dt><dd>提案绑定目录内 <code>{preview.data.working_directory}</code></dd></div>}
        {preview.data.fields.map((field, index) => <div key={`${field.name}:${index}`}>
          <dt>{fieldLabels[field.name] ?? field.name}</dt><dd><pre>{field.value}</pre></dd>
        </div>)}
      </dl>
      {preview.data.workspace_id && <details><summary>查看操作目录身份</summary><code>{preview.data.workspace_id}</code></details>}
      <p>{preview.data.effect === "file_review_required" && onReviewFile
        ? "先查看这份编辑的差异，再决定是否批准和应用。" : effectText[preview.data.effect]}</p>
      {preview.data.redacted && <p>敏感内容已脱敏，请按可见内容核对操作。</p>}
      {(!preview.data.source_current || preview.data.truncated) && <p role="alert">
        {preview.data.truncated ? "预览超过显示上限，需读取完整内容后再批准。" : "操作已变化或审批已处理，请刷新最新状态。"}
        <button onClick={() => void preview.refetch()} type="button">刷新操作预览</button></p>}
    </>}
    {recovering && <p>上次决定已经保存。点击“继续恢复”会沿用原决定和授权范围完成后续处理。</p>}
    {client.hasApprovalControl && !recovering && item.allowed_actions.includes("deny") && <input
      aria-label={`${item.tool_name} 的拒绝原因`} disabled={mutation.isPending} maxLength={2048}
      onChange={(event) => setReason(event.target.value)} placeholder="拒绝原因（可选）" value={reason} />}
    {client.hasApprovalControl && item.allowed_actions.includes("approve_for_run") && <fieldset disabled={mutation.isPending}>
      <legend>本次执行的限时、限次审批</legend>
      <p>每条新命令确认后计入此用途与风险范围。已有授权沿用原次数与到期时间。</p>
      <label>有效秒数<input aria-label="有界审批有效秒数" type="number" min={1} max={900} value={grantTTL} disabled={existingGrant}
        onChange={(event) => setGrantTTL(event.target.valueAsNumber)} /></label>
      <label>命令次数<input aria-label="有界审批命令次数" type="number" min={1} max={8} value={grantUses} disabled={existingGrant}
        onChange={(event) => setGrantUses(event.target.valueAsNumber)} /></label>
      <button className="primary" type="button" disabled={!canApprove || !Number.isInteger(grantTTL) || grantTTL < 1 || grantTTL > 900 || !Number.isInteger(grantUses) || grantUses < 1 || grantUses > 8}
        onClick={() => decide("approve_for_run")}>确认本条命令并计入有界审批</button>
    </fieldset>}
    <footer>
      {onReviewFile && preview.isSuccess && preview.data.effect === "file_review_required" && <button
        className="primary" disabled={preview.isFetching} onClick={(event) => onReviewFile({
          runID, editID: item.proposal_id, workspaceID: item.workspace_id }, event.currentTarget)} type="button">审阅文件提案</button>}
      {client.hasApprovalControl && item.allowed_actions.includes("deny") && <button className="secondary" disabled={mutation.isPending}
        onClick={() => decide("deny")} type="button"><Ban aria-hidden="true" size={15} />
        {recovering ? "继续恢复" : "拒绝"}</button>}
      {client.hasApprovalControl && item.allowed_actions.includes("approve_once") && <button className="primary" disabled={!canApprove}
        onClick={() => decide("approve_once")} type="button">
        {mutation.isPending ? <LoaderCircle className="spin" size={15} /> : <Check aria-hidden="true" size={15} />}
        {recovering ? "继续恢复" : computerUse ? "本次允许" : dryRun ? "批准模拟一次" : webFetch ? "允许一次" : "批准一次"}</button>}
      {client.hasApprovalControl && computerUse && item.allowed_actions.includes("approve_for_run_app") && <button className="primary"
        disabled={!canApprove} onClick={() => decide("approve_for_run_app")} type="button">
        <Check aria-hidden="true" size={15} />本任务允许操作该应用</button>}
      {client.hasApprovalControl && webFetch && item.allowed_actions.includes("approve_for_thread") && <button className="primary"
        disabled={!canApprove} onClick={() => decide("approve_for_thread")} type="button">
        <Check aria-hidden="true" size={15} />{recovering ? "继续恢复" : "本对话允许"}</button>}
    </footer>
    {mutation.isError && <p className="v2-inline-error" role="alert">决定未确认，可以重试。
      {mutation.error instanceof Error ? mutation.error.message : "审批失败"}</p>}
  </article>;
}
