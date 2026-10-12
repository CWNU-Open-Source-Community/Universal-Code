import { useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { LoaderCircle, Play } from "lucide-react";
import type { APIClient } from "../api/client";
import type { AgentNodeView } from "../api/types";
import { executeSpecialist, getSpecialistToolReceipts } from "../api/specialist-execution";
import { useLocale } from "../lib/locale";
import { formatNumber } from "../lib/format";
import { ErrorState, LoadingState, StatusBadge } from "./common";

function readPendingKey(storageKey: string): string | null {
  try {
    const key = sessionStorage.getItem(storageKey);
    return key && /^web-specialist-[a-f0-9-]{36}$/u.test(key) ? key : null;
  } catch { return null; }
}

function retainPendingKey(storageKey: string, key: string | null) {
  try {
    if (key) sessionStorage.setItem(storageKey, key);
    else sessionStorage.removeItem(storageKey);
  } catch { /* The mounted card still retains the operation key for a retry. */ }
}

export function SpecialistExecutionControl({ client, runID, agent }: {
  client: APIClient; runID: string; agent: AgentNodeView;
}) {
  const { t } = useLocale();
  const cache = useQueryClient();
  const storageKey = `universal-code.specialist.v1:${client.baseURL}:${runID}:${agent.id}`;
  const [pendingKey, setPendingKey] = useState(() => readPendingKey(storageKey));
  const [showReceipts, setShowReceipts] = useState(false);
  const execute = useMutation({
    mutationKey: ["run", runID, "specialist-execute"],
    mutationFn: () => {
      const key = pendingKey ?? `web-specialist-${globalThis.crypto.randomUUID()}`;
      setPendingKey(key);
      retainPendingKey(storageKey, key);
      return executeSpecialist(client, runID, agent.id, key);
    },
    onSuccess: () => {
      setPendingKey(null);
      retainPendingKey(storageKey, null);
    },
    onSettled: () => {
      void cache.invalidateQueries({ queryKey: ["run", runID, "agent-graph"] });
      void cache.invalidateQueries({ queryKey: ["run", runID, "events"] });
    },
  });
  const attemptID = execute.data?.attempt_id ?? agent.active_attempt_id ?? agent.completion?.attempt_id;
  const receipts = useQuery({
    queryKey: ["run", runID, "specialist-receipts", agent.id, attemptID],
    queryFn: ({ signal }) => getSpecialistToolReceipts(client, runID, agent.id, attemptID!, signal),
    enabled: showReceipts && Boolean(attemptID),
    refetchInterval: showReceipts && agent.status === "running" ? 3000 : false,
  });
  const canExecute = client.hasControl && client.hasModelControl && (agent.status === "ready" || Boolean(pendingKey));
  return <div className="agent-execution-control">
    {canExecute && <>
      <p className="projection-placeholder">{t("子代理可独立读取、搜索当前任务文件并提交结论。修改与验证请使用批量交付。",
        "The child can independently read and search this task's files and report findings. Use batch delivery for edits and validation.")}</p>
      <button className="command-button" disabled={execute.isPending} type="button" onClick={() => execute.mutate()}>
        {execute.isPending ? <LoaderCircle aria-hidden="true" className="spin" size={15} /> : <Play aria-hidden="true" size={15} />}
        {execute.isPending ? t("子代理执行中…", "Child running…") : pendingKey ? t("重试并读取执行结果", "Retry and read execution result") : t("运行子代理", "Run child agent")}
      </button>
    </>}
    {execute.data && <div className="run-control-result" role="status">
      <StatusBadge status={execute.data.status} />
      <span>{t(`${formatNumber(execute.data.tool_calls)} 次工具调用 · ${formatNumber(execute.data.tool_rounds)} 轮`,
        `${formatNumber(execute.data.tool_calls)} tool calls · ${formatNumber(execute.data.tool_rounds)} rounds`)}</span>
      <span>{formatNumber(execute.data.usage.total_tokens)} Tokens</span>
      {execute.data.replayed && <span>{t("已读取原执行结果", "Original execution result")}</span>}
    </div>}
    {execute.error && <ErrorState error={execute.error} />}
    {attemptID && <details open={showReceipts} onToggle={(event) => setShowReceipts(event.currentTarget.open)}>
      <summary>{t("子代理工具记录", "Child tool receipts")}</summary>
      {receipts.isLoading && <LoadingState />}
      {receipts.error && <ErrorState error={receipts.error} />}
      {receipts.data?.rounds.map((round) => <section key={round.round}>
        <strong>{t(`第 ${round.round} 轮`, `Round ${round.round}`)}</strong>
        {round.calls.map((call) => <details key={call.call_id}>
          <summary><code>{call.name}</code> <StatusBadge status={call.status} />{call.error_code && ` · ${call.error_code}`}</summary>
          {call.result_json && <pre style={{ whiteSpace: "pre-wrap", overflowWrap: "anywhere", maxHeight: 320, overflow: "auto" }}>{call.result_json}</pre>}
        </details>)}
      </section>)}
      {receipts.data?.rounds.length === 0 && <p>{t("本次执行没有工具调用。", "No tool calls in this attempt.")}</p>}
    </details>}
  </div>;
}
