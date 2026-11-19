import { useRef, useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { BellOff, BellRing, LoaderCircle, Play } from "lucide-react";
import type { APIClient } from "../api/client";
import type { OperationReceiptView, RunDetailView, RunWakeStateView } from "../api/types";
import { formatDate, formatNumber, shortID } from "../lib/format";
import { useLocale } from "../lib/locale";
import { ErrorState, KeyValue, LoadingState, StatusBadge } from "./common";
import { OperationReceipt } from "./operation-receipt";

export function RunWakePanel({ client, detail }: {
  client: APIClient;
  detail: RunDetailView;
}) {
  const { t } = useLocale();
  const runID = detail.run.id;
  const queryClient = useQueryClient();
  const [receipt, setReceipt] = useState<OperationReceiptView | null>(null);
  const operationKeys = useRef(new Map<string, string>());
  const keyFor = (action: string) => {
    const existing = operationKeys.current.get(action);
    if (existing) return existing;
    const created = `web-wake-${action}-${globalThis.crypto.randomUUID()}`;
    operationKeys.current.set(action, created);
    return created;
  };
  const query = useQuery({
    queryKey: ["run", runID, "wake-intent"],
    queryFn: ({ signal }) => client.runWakeState(runID, signal),
    enabled: client.hasRunWakeControl || client.hasRunWakeExecution,
  });
  const runtime = useQuery({
    queryKey: ["runtime-capabilities"],
    queryFn: ({ signal }) => client.runtimeCapabilities(signal),
    enabled: client.hasRunWakeWorker,
    refetchInterval: 2_000,
  });
  const schedule = useMutation({
    mutationFn: () => client.scheduleRunWake(runID, {
      version: "run_wake_control.v1", max_attempts: 3, initial_delay_seconds: 0,
      base_backoff_seconds: 5, max_backoff_seconds: 60, max_elapsed_seconds: 300,
    }, keyFor("schedule")),
    onSuccess: (result) => {
      operationKeys.current.delete("schedule");
      queryClient.setQueryData<RunWakeStateView>(["run", runID, "wake-intent"], {
        protocol_version: "run_wake_intent.v1", run_id: runID, found: true,
        intent: result.intent,
      });
      void queryClient.invalidateQueries({ queryKey: ["run", runID, "events"] });
    },
  });
  const cancel = useMutation({
    mutationFn: () => client.cancelRunWake(runID, { version: "run_wake_control.v1" },
      keyFor("cancel")),
    onSuccess: (result) => {
      operationKeys.current.delete("cancel");
      queryClient.setQueryData<RunWakeStateView>(["run", runID, "wake-intent"], {
        protocol_version: "run_wake_intent.v1", run_id: runID, found: true,
        intent: result.intent,
      });
      void queryClient.invalidateQueries({ queryKey: ["run", runID, "events"] });
    },
  });
  const consume = useMutation({
    mutationFn: () => client.consumeRunWake(runID, {
      version: "run_wake_consumer.v1", max_steps: 1,
    }),
    onSuccess: (result) => {
      setReceipt(result.receipt);
      queryClient.setQueryData<RunWakeStateView>(["run", runID, "wake-intent"], {
        protocol_version: "run_wake_intent.v1", run_id: runID, found: true,
        intent: result.intent,
      });
      void queryClient.invalidateQueries({ queryKey: ["run", runID] });
      void queryClient.invalidateQueries({ queryKey: ["run", runID, "events"] });
    },
    onError: () => {
      void queryClient.invalidateQueries({ queryKey: ["run", runID, "wake-intent"] });
    },
  });
  if (!client.hasRunWakeControl && !client.hasRunWakeExecution) return null;
  if (query.isLoading) return <LoadingState label={t("正在加载继续处理安排", "Loading continuation schedule")} />;
  if (query.isError || !query.data) return <ErrorState error={query.error} />;
  const intent = query.data.intent;
  const active = intent?.status === "queued" || intent?.status === "leased";
  const due = intent?.status === "leased" ||
    (intent?.status === "queued" && Date.parse(intent.next_wake_at) <= Date.now());
  const queuedWork = detail.operator_steering.pending;
  const canSchedule = !active && detail.run.status === "running" && queuedWork > 0 &&
    !detail.execution_lease?.active && !schedule.isPending && !cancel.isPending &&
    !consume.isPending && client.hasRunWakeControl;
  const canCancel = Boolean(active) && !schedule.isPending && !cancel.isPending &&
    !consume.isPending && client.hasRunWakeControl;
  const canConsume = Boolean(due) && detail.run.status === "running" &&
    !detail.execution_lease?.active && !schedule.isPending && !cancel.isPending &&
    !consume.isPending && client.hasRunWakeExecution;
  const error = runtime.error ?? consume.error ?? schedule.error ?? cancel.error;
  const worker = runtime.data?.wake_worker;
  return <section className="detail-section run-wake-section">
    <div className="section-heading">
      <h2><BellRing aria-hidden="true" size={15} />{t("继续排队工作", "Continue queued work")}</h2>
      <StatusBadge status={intent?.status ?? "idle"} />
    </div>
    {!active && <p>{queuedWork > 0 ? t(`待处理输入 ${queuedWork} 条`, `${queuedWork} queued inputs`) : t("暂无待处理输入", "No queued input")}</p>}
    <dl className="detail-grid compact">
      {intent && <>
        <KeyValue label={t("意图", "Intent")} value={shortID(intent.id)} />
        <KeyValue label={t("尝试次数", "Attempts")} value={`${formatNumber(intent.attempt_count)} / ${formatNumber(intent.max_attempts)}`} />
        <KeyValue label={t("下次唤醒", "Next wake")} value={formatDate(intent.next_wake_at)} />
        <KeyValue label={t("截止时间", "Deadline")} value={formatDate(intent.deadline_at)} />
      </>}
      <KeyValue label={t("执行", "Execution")} value={client.hasRunWakeWorker ? t("有界 Worker（1 × 1 步）", "bounded worker (1 x 1 step)") :
        client.hasRunWakeExecution ? t("前台", "foreground") : t("禁用", "disabled")} />
      <KeyValue label="Worker" value={worker?.enabled ?
        `${worker.state}${worker.active ? t(" / 活跃", " / active") : ""}` : t("禁用", "disabled")} />
    </dl>
    <div className="run-control-row">
      <button className="command-button" disabled={!canSchedule}
        onClick={() => schedule.mutate()} type="button">
        {schedule.isPending ? <LoaderCircle aria-hidden="true" className="spin" size={15} />
          : <BellRing aria-hidden="true" size={15} />}{t("安排", "Schedule")}
      </button>
      {active && <button className="command-button secondary" disabled={!canCancel}
        onClick={() => cancel.mutate()} type="button">
        {cancel.isPending ? <LoaderCircle aria-hidden="true" className="spin" size={15} />
          : <BellOff aria-hidden="true" size={15} />}{t("取消", "Cancel")}
      </button>}
      {active && client.hasRunWakeExecution &&
        <button className="command-button" disabled={!canConsume}
          onClick={() => consume.mutate()} type="button">
          {consume.isPending ? <LoaderCircle aria-hidden="true" className="spin" size={15} />
            : <Play aria-hidden="true" size={15} />}{t("执行唤醒", "Consume")}
        </button>}
    </div>
    {receipt && <OperationReceipt receipt={receipt} />}
    {error && <div className="inline-warning" role="alert">
      {error instanceof Error ? error.message : t("Run 唤醒控制失败", "Run wake control failed")}
    </div>}
  </section>;
}
