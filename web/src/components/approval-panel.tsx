import { Fragment, useRef, useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Ban, Check, LoaderCircle, ShieldCheck } from "lucide-react";
import type { APIClient } from "../api/client";
import type {
  ApprovalDecisionControlRequestView,
  ApprovalQueueItemView,
} from "../api/types";
import { formatDate, shortID } from "../lib/format";
import { EmptyState, ErrorState, LoadingState, StatusBadge } from "./common";
import { ControlledCommandProposalPanel } from "./controlled-command-proposal-panel";
import { HostCommandProposalPanel } from "./host-command-proposal-panel";
import { useLocale } from "../lib/locale";

type ApprovalAction = ApprovalDecisionControlRequestView["action"];

export function ApprovalPanel({ client, runID, threadID = "" }: {
  client: APIClient;
  runID: string;
  threadID?: string;
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
  return (
    <>
    <section className="approval-queue" aria-label={t("待处理审批", "Pending approvals")}>
      <header className="approval-queue-header">
        <div><ShieldCheck aria-hidden="true" size={16} /><strong>{t("待处理审批", "Pending approvals")}</strong></div>
        <span>{query.data.items.length}{query.data.truncated ? "+" : ""}</span>
      </header>
      <div className="approval-boundary-line">
        <span>{t("进程执行：关闭", "Process execution: off")}</span><span>{t("Run 内 Session 授权：无", "Run-local Session grant: none")}</span><span>{t("能力授权：无", "Capability grant: none")}</span>
      </div>
      {query.data.items.length === 0 ? <EmptyState>{t("没有待处理审批", "No pending approvals")}</EmptyState> : (
        <div className="approval-list">
          {query.data.items.map((item) => {
            const busy = mutation.isPending;
            const canApprove = client.hasApprovalControl &&
              item.allowed_actions.includes("approve_once");
            const canDeny = client.hasApprovalControl && item.allowed_actions.includes("deny");
            if (item.tool_name === "mcp_tool_call" && item.allowed_actions.includes("approve_for_run_app")) {
              return <ComputerUseApprovalRow key={item.id} client={client} item={item} runID={runID}
                busy={busy} reason={reasons[item.id] ?? ""}
                onReasonChange={(value) => setReasons((current) => ({ ...current, [item.id]: value }))}
                onDecide={(action) => decide(item, action)} />;
            }
            return (
              <article className="approval-row" key={item.id}>
                <div className="approval-row-main">
                  <span><strong>{item.tool_name}</strong><small>{item.action_class} / {item.mode}</small></span>
                  <code>{shortID(item.proposal_id)}</code>
                  <StatusBadge status={item.status} />
                  <time dateTime={item.created_at}>{formatDate(item.created_at)}</time>
                </div>
                {(canApprove || canDeny) && (
                  <div className="approval-actions">
                    {canDeny && <input aria-label={t(`${item.tool_name} 的拒绝原因`, `Denial reason for ${item.tool_name}`)}
                      disabled={busy} maxLength={2048}
                      onChange={(event) => setReasons((current) => ({ ...current,
                        [item.id]: event.target.value }))}
                      placeholder={t("拒绝原因", "Denial reason")} value={reasons[item.id] ?? ""} />}
                    {canDeny && <button className="command-button danger" disabled={busy}
                      onClick={() => decide(item, "deny")} type="button">
                      {busy && mutation.variables?.action === "deny"
                        ? <LoaderCircle aria-hidden="true" className="spin" size={15} />
                        : <Ban aria-hidden="true" size={15} />}{t("拒绝", "Deny")}
                    </button>}
                    {canApprove && <button className="command-button" disabled={busy}
                      onClick={() => decide(item, "approve_once")} type="button">
                      {busy && mutation.variables?.action === "approve_once"
                        ? <LoaderCircle aria-hidden="true" className="spin" size={15} />
                        : <Check aria-hidden="true" size={15} />}{t("仅批准一次", "Approve once")}
                    </button>}
                  </div>
                )}
              </article>
            );
          })}
        </div>
      )}
      {mutation.isError && <div className="inline-warning" role="alert">
        {mutation.error instanceof Error ? mutation.error.message : t("审批操作失败", "Approval decision failed")}
      </div>}
    </section>
    <ControlledCommandProposalPanel client={client} runID={runID} />
    <HostCommandProposalPanel client={client} runID={runID} />
    </>
  );
}

function ComputerUseApprovalRow({ client, item, runID, busy, reason, onReasonChange, onDecide }: {
  client: APIClient; item: ApprovalQueueItemView; runID: string; busy: boolean; reason: string;
  onReasonChange: (value: string) => void; onDecide: (action: ApprovalAction) => void;
}) {
  const { t } = useLocale();
  const preview = useQuery({
    queryKey: ["run", runID, "approval-preview", item.id, item.version],
    queryFn: async ({ signal }) => {
      const value = await client.approvalPreview(runID, item.id, signal);
      if (item.run_id !== runID || value.run_id !== runID || value.approval_id !== item.id ||
        value.proposal_id !== item.proposal_id || value.tool_name !== item.tool_name ||
        value.workspace_id !== item.workspace_id) throw new Error(t("审批预览与当前操作不一致，请刷新。", "Approval preview does not match this operation. Refresh it."));
      return value;
    },
    retry: false,
  });
  const computerUse = preview.data?.effect === "computer_use_app";
  const canApprove = client.hasApprovalControl && preview.isSuccess && !preview.isFetching &&
    preview.data.source_current && !preview.data.truncated && !busy;
  const canDeny = client.hasApprovalControl && item.allowed_actions.includes("deny");
  const labels: Record<string, string> = {
    app_id: t("目标应用", "Target app"), app_path: t("应用路径", "Application path"),
    window_id: t("目标窗口", "Target window"), tool_name: t("操作工具", "Tool"),
    authorization_scope: t("授权范围", "Authorization scope"), arguments: t("操作参数", "Arguments"),
  };
  return <article className="approval-row">
    <div className="approval-row-main">
      <span><strong>{computerUse ? t("允许操作这个应用？", "Allow control of this app?") : item.tool_name}</strong>
        <small>{item.action_class} / {item.mode}</small></span>
      <code>{shortID(item.proposal_id)}</code><StatusBadge status={item.status} />
      <time dateTime={item.created_at}>{formatDate(item.created_at)}</time>
    </div>
    {preview.isLoading && <p role="status">{t("正在读取应用操作预览…", "Reading the app operation preview…")}</p>}
    {preview.isError && <div role="alert"><p>{t("操作预览读取失败，请重新读取后确认批准范围。", "The operation preview could not be read. Retry to confirm the approval scope.")}</p>
      <button className="command-button" onClick={() => void preview.refetch()} type="button">{t("重试操作预览", "Retry operation preview")}</button></div>}
    {computerUse && preview.data && <>
      <dl className="host-command-envelope">{preview.data.fields.map((field, index) => <Fragment key={`${field.name}:${index}`}>
        <dt>{labels[field.name] ?? field.name}</dt><dd><code>{field.value}</code></dd>
      </Fragment>)}</dl>
      <p>{t("“本次允许”仅批准当前操作；“本任务允许操作该应用”允许 Agent 在当前任务中继续点击、输入和滚动该应用。任务结束后授权失效。",
        "Allow once approves this operation. Allow this app for this task lets the Agent continue clicking, typing, and scrolling in this app until the task ends.")}</p>
      {(!preview.data.source_current || preview.data.truncated) && <p role="alert">{t("操作已变化或预览不完整，请刷新后再批准。", "The operation changed or its preview is incomplete. Refresh before approving.")}
        <button className="command-button" disabled={busy} onClick={() => void preview.refetch()} type="button">{t("刷新操作预览", "Refresh operation preview")}</button></p>}
    </>}
    {client.hasApprovalControl && <div className="approval-actions">
      {canDeny && <input aria-label={t(`${item.tool_name} 的拒绝原因`, `Denial reason for ${item.tool_name}`)}
        disabled={busy} maxLength={2048} onChange={(event) => onReasonChange(event.target.value)}
        placeholder={t("拒绝原因", "Denial reason")} value={reason} />}
      {canDeny && <button className="command-button danger" disabled={busy}
        onClick={() => onDecide("deny")} type="button"><Ban aria-hidden="true" size={15} />{t("拒绝", "Deny")}</button>}
      {item.allowed_actions.includes("approve_once") && <button className="command-button" disabled={!canApprove}
        onClick={() => onDecide("approve_once")} type="button"><Check aria-hidden="true" size={15} />
        {computerUse ? t("本次允许", "Allow once") : t("仅批准一次", "Approve once")}</button>}
      {computerUse && item.allowed_actions.includes("approve_for_run_app") && <button className="command-button" disabled={!canApprove}
        onClick={() => onDecide("approve_for_run_app")} type="button"><Check aria-hidden="true" size={15} />
        {t("本任务允许操作该应用", "Allow this app for this task")}</button>}
    </div>}
  </article>;
}
