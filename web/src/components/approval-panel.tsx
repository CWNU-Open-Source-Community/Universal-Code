import { Fragment, useRef, useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { ShieldCheck } from "lucide-react";
import type { APIClient } from "../api/client";
import type {
  ApprovalDecisionControlRequestView,
  ApprovalQueueItemView,
} from "../api/types";
import { formatDate } from "../lib/format";
import { EmptyState, ErrorState, LoadingState, StatusBadge } from "./common";
import { ControlledCommandProposalPanel } from "./controlled-command-proposal-panel";
import { HostCommandProposalPanel } from "./host-command-proposal-panel";
import { useLocale } from "../lib/locale";
import { ComputerUsePreview } from "./computer-use-preview";
import { ApprovalDenialReason, approvalToolLabel, fileApprovalLabel, readApprovalPreview } from "./approval-controls";
import type { FileEditReviewTarget } from "./file-edit-panel";
import "./approval-panel.css";

type ApprovalAction = ApprovalDecisionControlRequestView["action"];

export function ApprovalPanel({ client, runID, threadID = "", onReviewFile }: {
  client: APIClient;
  runID: string;
  threadID?: string;
  onReviewFile?: (target: FileEditReviewTarget) => void;
}) {
  const { t } = useLocale();
  const queryClient = useQueryClient();
  const [reasons, setReasons] = useState<Record<string, string>>({});
  const operationKeys = useRef(new Map<string, string>());
  const decisionPending = useRef(false);
  const query = useQuery({
    queryKey: ["run", runID, "approvals"],
    queryFn: ({ signal }) => client.approvalQueue(runID, signal),
    enabled: runID !== "",
  });
  const mutation = useMutation({
    mutationFn: ({ item, action, reason, intent }: {
      item: ApprovalQueueItemView;
      action: ApprovalAction;
      reason: string;
      intent: string;
    }) => {
      let operationKey = operationKeys.current.get(intent);
      if (!operationKey) {
        operationKey = `web-approval-${globalThis.crypto.randomUUID()}`;
        operationKeys.current.set(intent, operationKey);
      }
      const body: ApprovalDecisionControlRequestView = {
        version: "approval_control.v1",
        action,
        ...(action === "deny" && reason ? { reason } : {}),
      };
      return client.decideApproval(runID, item.id, body, operationKey);
    },
    onSuccess: (_result, variables) => {
      operationKeys.current.delete(variables.intent);
      void queryClient.invalidateQueries({ queryKey: ["run", runID, "approvals"] });
      void queryClient.invalidateQueries({ queryKey: ["run", runID] });
      void queryClient.invalidateQueries({ queryKey: ["run", runID, "events"] });
      if (threadID) {
        void queryClient.invalidateQueries({ queryKey: ["thread", threadID] });
        void queryClient.invalidateQueries({ queryKey: ["thread", threadID, "transcript"] });
      }
    },
    onSettled: () => { decisionPending.current = false; },
  });
  if (query.isLoading) {
    return <LoadingState label={t("正在加载审批", "Loading approvals")} />;
  }
  if (query.isError || !query.data) {
    return <ErrorState error={query.error} />;
  }
  const decide = (item: ApprovalQueueItemView, action: ApprovalAction) => {
    if (decisionPending.current) return;
    decisionPending.current = true;
    const reason = action === "deny" ? (reasons[item.id] ?? "").trim() : "";
    mutation.mutate({ item, action, reason,
      intent: `${item.id}:${action}:${reason}` });
  };
  return <>
    <section className="approval-queue approval-decision-queue" aria-label={t("待处理审批", "Pending approvals")}>
      <header className="approval-queue-header">
        <div><ShieldCheck aria-hidden="true" size={16} /><strong>{t("待处理审批", "Pending approvals")}</strong></div>
        <span>{query.data.items.length}{query.data.truncated ? "+" : ""}</span>
      </header>
      {query.data.items.length === 0 ? <EmptyState>{t("没有待处理审批", "No pending approvals")}</EmptyState> :
        <div className="approval-list">{query.data.items.map((item) =>
          <ApprovalDecisionRow key={item.id} client={client} item={item} runID={runID} onReviewFile={onReviewFile}
            busy={mutation.isPending} reason={reasons[item.id] ?? ""} onDecide={(action) => decide(item, action)}
            onReasonChange={(value) => setReasons((current) => ({ ...current, [item.id]: value }))} />)}</div>}
      {mutation.isError && <div className="inline-warning" role="alert">
        {mutation.error instanceof Error ? mutation.error.message : t("审批操作失败", "Approval decision failed")}
      </div>}
      <details className="approval-boundaries"><summary>{t("授权边界", "Permission boundaries")}</summary>
        <div className="approval-boundary-line">
          <span>{t("进程执行：关闭", "Process execution: off")}</span><span>{t("Run 内 Session 授权：无", "Run-local Session grant: none")}</span><span>{t("能力授权：无", "Capability grant: none")}</span>
        </div>
      </details>
    </section>
    <ControlledCommandProposalPanel client={client} runID={runID} />
    <HostCommandProposalPanel client={client} runID={runID} />
  </>;
}

function ApprovalDecisionRow({ client, item, runID, busy, reason, onReasonChange, onDecide, onReviewFile }: {
  client: APIClient; item: ApprovalQueueItemView; runID: string; busy: boolean; reason: string;
  onReasonChange: (value: string) => void; onDecide: (action: ApprovalAction) => void;
  onReviewFile?: (target: FileEditReviewTarget) => void;
}) {
  const { t } = useLocale();
  const fileTool = fileApprovalLabel(item.tool_name, t);
  const needsPreview = Boolean(fileTool) || item.tool_name === "mcp_tool_call";
  const preview = useQuery({
    queryKey: ["run", runID, "approval-preview", item.id, item.version],
    queryFn: ({ signal }) => readApprovalPreview(client, item, runID, signal, t),
    enabled: needsPreview,
    retry: false,
  });
  const computerUse = preview.data?.effect === "computer_use_app";
  const fileReview = fileTool && preview.data?.effect === "file_review_required";
  const fields = preview.data?.fields ?? [];
  const target = fileReview ? fields.find(({ name }) => name === "path")?.value :
    needsPreview ? undefined : item.canonical_url || item.exact_target || item.proposal_id;
  const otherFields = fields.filter((field) => !fileReview || !(field.name === "path" ||
    (field.name === "operation" && `${field.value}_file` === item.tool_name)));
  const canApprove = !busy && (!needsPreview || (preview.isSuccess && !preview.isFetching && preview.data.source_current && !preview.data.truncated));
  const canDeny = client.hasApprovalControl && item.allowed_actions.includes("deny");
  return <article className="approval-row">
    <div className="approval-decision-heading">
      <strong>{computerUse ? t("允许操作这个应用？", "Allow control of this app?") : approvalToolLabel(item.tool_name, t)}</strong>
      {target && <code>{target}</code>}{item.status !== "pending" && <StatusBadge status={item.status} />}
    </div>
    {preview.isLoading && <p role="status">{t("正在读取操作预览…", "Reading the action preview…")}</p>}
    {preview.isError && <div role="alert"><p>{t("操作预览读取失败，请重新读取后确认批准范围。", "The operation preview could not be read. Retry to confirm the approval scope.")}</p>
      {preview.error instanceof Error && <p>{preview.error.message}</p>}
      <button className="command-button" onClick={() => void preview.refetch()} type="button">{t("重试操作预览", "Retry operation preview")}</button></div>}
    {computerUse && preview.data ? <>
      <ComputerUsePreview preview={preview.data} />
      <p>{item.allowed_actions.includes("approve_for_run_app")
        ? t("按任务授权后，Agent 可继续点击、输入和滚动此应用，直至任务结束。", "A task grant lets the agent click, type and scroll in this app until the task ends.")
        : t("本次允许仅批准当前操作；后续需要授权的操作将再次询问。", "Allow once approves only this operation. Later operations that need authorization will ask again.")}</p>
    </> : otherFields.length > 0 && <dl className="host-command-envelope">
      {otherFields.map((field, index) => <Fragment key={`${field.name}:${index}`}><dt>{field.name === "destination_path" ? t("目标文件", "Destination file") : field.name}</dt><dd><code>{field.value}</code></dd></Fragment>)}
    </dl>}
    {fileReview && preview.data?.working_directory && preview.data.working_directory !== "." && <p>{t("工作目录", "Working directory")} <code>{preview.data.working_directory}</code></p>}
    {preview.data && (!preview.data.source_current || preview.data.truncated) && <p role="alert">{t("操作已变化或预览不完整，请刷新后再批准。", "The operation changed or its preview is incomplete. Refresh before approving.")}
      <button className="command-button" disabled={busy} onClick={() => void preview.refetch()} type="button">{t("刷新操作预览", "Refresh action preview")}</button></p>}
    {preview.data?.redacted && <p>{t("敏感内容已脱敏，请按可见内容核对操作。", "Sensitive content is redacted. Review the action using the visible information.")}</p>}
    <div className="approval-actions">
      {fileReview && onReviewFile && <button className="command-button" disabled={!preview.isSuccess || preview.isFetching} type="button"
        onClick={() => onReviewFile({ runID, editID: item.proposal_id, workspaceID: item.workspace_id })}>{t("审阅文件提案", "Review file proposal")}</button>}
      {client.hasApprovalControl && !fileTool && item.allowed_actions.includes("approve_once") && <button className="command-button" disabled={!canApprove}
        onClick={() => onDecide("approve_once")} type="button">{computerUse ? t("本次允许", "Allow once") : t("仅批准一次", "Approve once")}</button>}
      {client.hasApprovalControl && computerUse && item.allowed_actions.includes("approve_for_run_app") && <button className="command-button" disabled={!canApprove}
        onClick={() => onDecide("approve_for_run_app")} type="button">{t("本任务允许操作该应用", "Allow this app for this task")}</button>}
      {canDeny && <button className="command-button" disabled={busy}
        onClick={() => onDecide("deny")} type="button">{t("拒绝", "Deny")}</button>}
      {canDeny && <ApprovalDenialReason label={t(`${item.tool_name} 的拒绝原因`, `Denial reason for ${item.tool_name}`)}
        disabled={busy} onChange={onReasonChange} value={reason} />}
      <details className="approval-metadata"><summary>{t("审批详情", "Approval details")}</summary>
        <dl><dt>{t("提案", "Proposal")}</dt><dd>{item.proposal_id}</dd>
          {item.workspace_id && <><dt>{t("工作区", "Workspace")}</dt><dd>{item.workspace_id}</dd></>}
          <dt>{t("权限", "Permission")}</dt><dd>{item.action_class} / {item.mode}</dd>
          <dt>{t("请求时间", "Requested")}</dt><dd><time dateTime={item.created_at}>{formatDate(item.created_at)}</time></dd></dl>
      </details>
    </div>
  </article>;
}
