import { useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { LoaderCircle, Play } from "lucide-react";
import type { APIClient } from "../api/client";
import type { AgentNodeView } from "../api/types";
import { continueSpecialist, getSpecialistToolReceipts, revokeSpecialistReadTools } from "../api/specialist-execution";
import { v2QueryKeys } from "../v2/query-keys";
import { useLocale } from "../lib/locale";
import { formatNumber } from "../lib/format";
import { ErrorState, LoadingState, StatusBadge } from "./common";

function readPendingKey(storageKey: string): string | null {
  try {
    const key = sessionStorage.getItem(storageKey);
    return key && /^web-specialist-continuation-[a-f0-9-]{36}$/u.test(key) ? key : null;
  } catch { return null; }
}

function retainPendingKey(storageKey: string, key: string | null) {
  try {
    if (key) sessionStorage.setItem(storageKey, key);
    else sessionStorage.removeItem(storageKey);
  } catch { /* The mounted card still retains the operation key for a retry. */ }
}

export function SpecialistExecutionControl({ client, runID, agent, runStatus }: {
  client: APIClient; runID: string; agent: AgentNodeView; runStatus?: string;
}) {
  const { t } = useLocale();
  const cache = useQueryClient();
  const storageKey = `universal-code.specialist-continuation.v1:${client.baseURL}:${runID}:${agent.id}`;
  const [pendingKey, setPendingKey] = useState(() => readPendingKey(storageKey));
  const [showReceipts, setShowReceipts] = useState(false);
  const [selectedAttempt, setSelectedAttempt] = useState<string>();
  const execute = useMutation({
    mutationKey: ["run", runID, "specialist-execute"],
    mutationFn: () => {
      const key = pendingKey ?? `web-specialist-continuation-${globalThis.crypto.randomUUID()}`;
      setPendingKey(key);
      retainPendingKey(storageKey, key);
      return continueSpecialist(client, runID, agent.id, key);
    },
    onSuccess: (result) => {
      if (result.stop_reason !== "waiting_approval" && result.summary_status !== "queued" && result.summary_status !== "failed") {
        setPendingKey(null);
        retainPendingKey(storageKey, null);
      }
      void cache.invalidateQueries({ queryKey: ["thread", result.thread_id] });
      void cache.invalidateQueries({ queryKey: v2QueryKeys.thread(result.thread_id) });
      void cache.invalidateQueries({ queryKey: v2QueryKeys.threads("active") });
    },
    onSettled: () => {
      void cache.invalidateQueries({ queryKey: ["run", runID] });
    },
  });
  const revoke = useMutation({
    mutationFn: () => revokeSpecialistReadTools(client, runID, agent.id),
    onSuccess: () => { void cache.invalidateQueries({ queryKey: ["run", runID] }); },
  });
  const readPaths = revoke.data?.allowed_read_paths ?? (revoke.isSuccess ? [] : agent.allowed_read_paths ?? []);
  const attemptID = selectedAttempt ?? execute.data?.attempt_id ?? agent.active_attempt_id ?? agent.completion?.attempt_id;
  const receipts = useQuery({
    queryKey: ["run", runID, "specialist-receipts", agent.id, attemptID],
    queryFn: ({ signal }) => getSpecialistToolReceipts(client, runID, agent.id, attemptID!, signal),
    enabled: showReceipts && Boolean(attemptID),
    refetchInterval: showReceipts && agent.status === "running" ? 3000 : false,
  });
  const canExecute = client.hasControl && client.hasModelControl && (agent.status === "ready" || Boolean(pendingKey));
  const runBlocked = !pendingKey && runStatus !== "running";
  return <div className="agent-execution-control">
    <p>{agent.worker ? t("执行型子代理", "Execution worker") : readPaths.length > 0 ? t("只读工具子代理", "Read-only tool child") : t("无工具子代理", "No-tool child")}</p>
    {agent.worker && <p>{t("委派工具：", "Delegated tools: ")}{agent.worker.tools == null ? t("继承主代理", "Inherit parent tools") : agent.worker.tools.length ? agent.worker.tools.join(", ") : t("无工具", "No tools")}</p>}
    {agent.worker && <p>{agent.worker.history_scope === "parent_snapshot" ? t("历史范围：自身记录、交付材料与创建时的父会话历史", "History: own records, delivered material and parent history captured at creation") : t("历史范围：自身记录与明确交付的材料", "History: own records and explicitly delivered material")}</p>}
    {!agent.worker && readPaths.length > 0 && <>
      <p>{t("允许读取：", "Read scope: ")}{readPaths.join(", ")}</p>
      <p>{t("工具次数：", "Tool calls: ")}{agent.tool_calls_used ?? 0} / {agent.tool_call_limit}</p>
    </>}
    {(agent.worker || readPaths.length > 0) && client.hasControl && client.hasModelControl && <button type="button" disabled={revoke.isPending} onClick={() => revoke.mutate()}>{agent.worker ? t("撤回工具权限", "Revoke tool access") : t("撤回读取权限", "Revoke read access")}</button>}
    {(agent.cost_limit_micros ?? 0) > 0 && <p>{t("费用（含预留）：", "Cost including reservations: ")}{((agent.cost_used_micros ?? 0) / 1_000_000).toFixed(6)} / {((agent.cost_limit_micros ?? 0) / 1_000_000).toFixed(6)} USD</p>}
    {revoke.error && <ErrorState error={revoke.error} />}
    {canExecute && <>
      <p className="projection-placeholder">{agent.worker ? t("子代理将在委派权限和预算内执行；需要批准的工具会等待审批。", "The child runs within delegated permissions and budgets; tools requiring approval will wait for a decision.") : readPaths.length === 0 ? t("子代理将根据已委托材料继续分析，完成后由主会话汇总。", "The child continues analyzing its delegated material; the main conversation summarizes completion.") : t("子代理将在已授权预算内连续读取、搜索任务文件，完成后由主会话自动汇总。修改与验证请使用批量交付。",
        "The child continues reading and searching task files within its approved budget. On completion, the main conversation summarizes its findings automatically. Use batch delivery for edits and validation.")}</p>
      {runBlocked && <p>{runStatus === "paused" ? t("任务已暂停。请先在任务控制中恢复，再运行子代理。", "The task is paused. Resume it in task controls before running the child.")
        : t("等待任务处于运行状态后执行。", "The task must be running before child execution.")}</p>}
      <button className="command-button" disabled={execute.isPending || runBlocked} type="button" onClick={() => execute.mutate()}>
        {execute.isPending ? <LoaderCircle aria-hidden="true" className="spin" size={15} /> : <Play aria-hidden="true" size={15} />}
        {execute.isPending ? t("子代理执行并汇总中…", "Child running and summarizing…") : pendingKey ? t("重试并读取执行结果", "Retry and read execution result") : t("连续运行并汇总", "Run continuously and summarize")}
      </button>
    </>}
    {execute.data && <div className="run-control-result" role="status">
      <StatusBadge status={execute.data.status} />
      <span>{t(`${formatNumber(execute.data.rounds_completed)} 轮子代理执行`, `${formatNumber(execute.data.rounds_completed)} child turns`)}</span>
      <span>{formatNumber(execute.data.total_tokens)} Tokens</span>
      <span>{executionStopLabel(execute.data.stop_reason, execute.data.error_code, t)}</span>
      <span>{summaryLabel(execute.data.summary_status, t)}{execute.data.summary_error_code && ` · ${execute.data.summary_error_code}`}</span>
      <a href={`#/threads/${encodeURIComponent(execute.data.thread_id)}`}>{t("查看主会话", "Open main conversation")}</a>
      {execute.data.replayed && <span>{t("已读取原执行结果", "Original execution result")}</span>}
    </div>}
    {execute.error && <ErrorState error={execute.error} />}
    {attemptID && <details open={showReceipts} onToggle={(event) => setShowReceipts(event.currentTarget.open)}>
      <summary>{t("子代理工具记录", "Child tool receipts")}</summary>
      {execute.data && execute.data.attempt_ids.length > 1 && <label>
        {t("执行轮次", "Child turn")}
        <select aria-label={t("执行轮次", "Child turn")} value={attemptID} onChange={(event) => setSelectedAttempt(event.target.value)}>
          {execute.data.attempt_ids.map((id, index) => <option key={id} value={id}>{t(`第 ${index + 1} 轮`, `Turn ${index + 1}`)}</option>)}
        </select>
      </label>}
      {receipts.isLoading && <LoadingState />}
      {receipts.error && <ErrorState error={receipts.error} />}
      {receipts.data?.rounds.map((round) => <section key={round.round}>
        <strong>{t(`第 ${round.round} 轮`, `Round ${round.round}`)}</strong>
        {round.calls.map((call) => <details key={call.call_id}>
          <summary><code>{call.name}</code> <StatusBadge status={call.status} />{call.error_code && ` · ${call.error_code}`}</summary>
          {call.approval_id && <p>{t("审批记录：", "Approval: ")}<code>{call.approval_id}</code></p>}
          {call.result_json && <pre style={{ whiteSpace: "pre-wrap", overflowWrap: "anywhere", maxHeight: 320, overflow: "auto" }}>{call.result_json}</pre>}
        </details>)}
      </section>)}
      {receipts.data?.rounds.length === 0 && <p>{t("本次执行没有工具调用。", "No tool calls in this attempt.")}</p>}
    </details>}
  </div>;
}

type Translate = (zh: string, en: string) => string;
function executionStopLabel(reason: string, errorCode: string | undefined, t: Translate) {
  switch (reason) {
    case "waiting_approval": return t("等待工具审批；审批后继续执行", "Waiting for tool approval; continue after approval");
    case "all_terminal": return t("子代理已完成", "Child completed");
    case "no_ready_child": return t("等待后续消息", "Waiting for further input");
    case "round_limit": return t("达到连续执行轮数上限", "Continuation round limit reached");
    case "token_budget_exhausted": return t("Token 预算已耗尽", "Token budget exhausted");
    case "execution_budget_exhausted": return t("执行时间预算已耗尽", "Execution budget exhausted");
    case "cancelled": return t("执行已取消", "Execution cancelled");
    case "child_error": return errorCode === "RESOURCE_EXHAUSTED" ? t("子代理预算已耗尽", "Child budget exhausted") : t("子代理执行失败", "Child execution failed");
    default: return reason;
  }
}
function summaryLabel(status: string, t: Translate) {
  switch (status) {
    case "completed": return t("主会话已汇总", "Main conversation summary completed");
    case "queued": return t("汇总等待主会话执行", "Summary queued for the main conversation");
    case "cancelled": return t("汇总已取消", "Summary cancelled");
    case "failed": return t("主会话汇总失败", "Main conversation summary failed");
    default: return t("尚无完成报告可汇总", "No completion report to summarize yet");
  }
}
