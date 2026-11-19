import { useId, useState } from "react";
import { useMutation, useQueries, useQuery, useQueryClient } from "@tanstack/react-query";
import { APIRequestError, type APIClient } from "../api/client";
import type { PlanDeliveryCheckpointControlRequestView, PlanDeliveryStateView,
  PlanDeliveryWorkItemControlRequestView, PlanDeliveryCheckpointControlView,
  PlanDeliveryWorkItemControlView, RunDetailView } from "../api/types";
import { useLocale } from "../lib/locale";
import { v2QueryKeys } from "../v2/query-keys";
import { StatusBadge } from "./common";

const evidenceFields = [
  ["focused_verification", "针对本项的验证结果", "Focused verification results"],
  ["diff_audit", "改动审阅", "Diff review"],
  ["security_audit", "安全审阅", "Security review"],
  ["functional_verification", "完整功能验证", "Full functional verification"],
  ["robustness_audit", "异常与恢复检查", "Failure and recovery checks"],
  ["handoff_summary", "交接说明", "Handoff summary"],
] as const;
type EvidenceField = typeof evidenceFields[number][0];
type Draft = Record<EvidenceField, string>;
const emptyDraft: Draft = { focused_verification: "", diff_audit: "", security_audit: "",
  functional_verification: "", robustness_audit: "", handoff_summary: "" };
type Attempt = { runID: string; itemID: string; threadID?: string; operationKey: string } & (
  { action: "start" | "complete"; body: PlanDeliveryWorkItemControlRequestView } |
  { action: "checkpoint"; body: PlanDeliveryCheckpointControlRequestView });
type Interaction = { attempt: Attempt; state: "pending" | "unknown" | "rejected"; error?: string; unconfirmed?: boolean };
const intentKey = (runID: string) => ["run", runID, "manual-delivery-intent"] as const;
const draftsKey = (runID: string) => ["run", runID, "manual-delivery-drafts"] as const;

export function PlanDeliveryWorkItems({ client, detail, state, threadID }: {
  client: APIClient; detail: RunDetailView; state: PlanDeliveryStateView; threadID?: string;
}) {
  const { t } = useLocale();
  const queryClient = useQueryClient();
  const formID = useId();
  const runID = detail.run.id;
  const manualRequired = state.selection?.manual_acceptance !== "on_demand";
  const [optionalForms, setOptionalForms] = useState<Record<string, boolean>>({});
  const selected = state.selection?.items ?? [];
  const items = useQueries({ queries: selected.map((item) => ({ queryKey: ["work-item", item.work_item_id],
    queryFn: async ({ signal }: { signal: AbortSignal }) => {
      const value = await client.getWorkItem(item.work_item_id, signal);
      if (value.run_id !== runID) throw new Error(t("计划项与当前任务不匹配", "The Plan item belongs to a different execution"));
      return value;
    }, refetchOnMount: "always" as const })) });
  const intent = useQuery<Interaction | null>({ queryKey: intentKey(runID), queryFn: () => null,
    enabled: false, initialData: null, gcTime: Infinity });
  const drafts = useQuery<Record<string, Draft>>({ queryKey: draftsKey(runID), queryFn: () => ({}),
    enabled: false, initialData: {}, gcTime: Infinity });
  const mutation = useMutation({
    mutationFn: async (attempt: Attempt): Promise<PlanDeliveryWorkItemControlView | PlanDeliveryCheckpointControlView> => attempt.action === "checkpoint"
      ? client.recordPlanDeliveryCheckpoint(attempt.runID, attempt.itemID, attempt.body, attempt.operationKey)
      : client.controlPlanDeliveryWorkItem(attempt.runID, attempt.itemID, attempt.action, attempt.body, attempt.operationKey),
    onSuccess: (result, attempt) => {
      queryClient.setQueryData(["work-item", attempt.itemID], result.current_work_item);
      if ("checkpoint" in result) {
        queryClient.setQueryData(["note", result.note.id], result.note);
        queryClient.setQueryData<RunDetailView>(["run", attempt.runID], (current) => {
          if (!current?.plan_delivery || current.run.id !== attempt.runID) return current;
          const checkpoints = [...current.plan_delivery.checkpoints.filter((entry) => entry.id !== result.checkpoint.id), result.checkpoint];
          return { ...current, plan_delivery: { ...current.plan_delivery, checkpoints,
            ready_checkpoints: new Set([...checkpoints.filter((entry) => entry.gate_ready).map((entry) => entry.work_item_id),
              ...(current.plan_delivery.continued_completions ?? []).filter((entry) => !entry.completion_event_id).map((entry) => entry.work_item_id)]).size } };
        });
      }
      queryClient.setQueryData<Interaction | null>(intentKey(attempt.runID), (current) =>
        current?.attempt.operationKey === attempt.operationKey ? null : current);
      // Keep the author's text for review and further edits; a saved statement is not an automated test result.
    },
    onError: (error, attempt) => {
      const rejected = error instanceof APIRequestError && error.code !== "INVALID_RESPONSE" &&
        [400, 401, 403, 404, 409, 412, 422].includes(error.status);
      queryClient.setQueryData<Interaction | null>(intentKey(attempt.runID), (current) =>
        current?.attempt.operationKey === attempt.operationKey
          ? { attempt, state: rejected && !current.unconfirmed ? "rejected" : "unknown",
            unconfirmed: current.unconfirmed || !rejected, error: error.message } : current);
    },
    onSettled: (_result, _error, attempt) => {
      void queryClient.invalidateQueries({ queryKey: ["work-item", attempt.itemID] });
      void queryClient.invalidateQueries({ queryKey: ["run", attempt.runID] });
      if (attempt.threadID) void queryClient.invalidateQueries({ queryKey: v2QueryKeys.thread(attempt.threadID) });
    },
  });
  const submit = (attempt: Attempt) => {
    const current = queryClient.getQueryData<Interaction | null>(intentKey(attempt.runID));
    if (!client.hasPlanDelivery || current?.state === "pending" ||
      (current && current.attempt.operationKey !== attempt.operationKey)) return;
    queryClient.setQueryData<Interaction>(intentKey(attempt.runID), { attempt, state: "pending",
      unconfirmed: current?.unconfirmed || current?.state === "unknown" });
    mutation.mutate(attempt);
  };
  const newAttempt = (itemID: string) => ({ runID, itemID, threadID,
    operationKey: `web-plan-item-${globalThis.crypto.randomUUID()}` });
  const mutable = client.hasPlanDelivery && state.delivery_gate_enforced && detail.run.status === "paused" &&
    detail.mode.phase === "deliver" && !detail.execution_lease?.active;
  const locked = Boolean(intent.data);
  return <section aria-label={t("计划项与验收", "Plan items and acceptance")} className="plan-manual-delivery">
    <h3>{t("计划项与验收", "Plan items and acceptance")}</h3>
    {!mutable && <p>{!client.hasPlanDelivery ? t("当前可查看验收记录。连接计划交付控制后可更新。", "Acceptance records are available for review. Connect plan delivery control to update them.") :
      !state.delivery_gate_enforced ? t("此旧版计划沿用原验收流程，可在这里查看现有记录。", "This legacy Plan uses its original acceptance workflow. Existing records are available here.") :
      t("请在交付阶段暂停任务并等待执行结束后，再更新计划项。", "Pause the task in Deliver and wait for execution to end before updating Plan items.")}</p>}
    {intent.data && <div role={intent.data.state === "pending" ? "status" : "alert"} className="inline-warning">
      <p>{intent.data.state === "pending" ? t("正在保存计划项操作…", "Saving the Plan item operation…") :
        intent.data.state === "rejected" ? t("服务拒绝了此请求，输入已保留。请查看最新状态后调整。", "The request was rejected. Your text is preserved; review the current state before editing.") :
          t("结果尚未确认。请确认原请求，避免重复记录。", "The result is unknown. Confirm the original request to avoid duplicate records.")} {intent.data.error}</p>
      {intent.data.state === "unknown" && <button className="command-button" disabled={!client.hasPlanDelivery}
        onClick={() => submit(intent.data!.attempt)} type="button">{t("确认上次验收操作", "Confirm previous acceptance operation")}</button>}
      {intent.data.state === "rejected" && <button className="command-button"
        onClick={() => queryClient.setQueryData(intentKey(runID), null)} type="button">{t("返回修改", "Return to editing")}</button>}
    </div>}
    {selected.map((selection, index) => {
      const query = items[index];
      const item = query.data;
      if (query.isError || (item && item.run_id !== runID)) return <p key={selection.work_item_id} role="alert">
        {t("计划项读取失败，请重新读取最新状态后继续。", "The Plan item failed to load. Reload its latest state to continue.")}
        <button onClick={() => void query.refetch()} type="button">{t("重试计划项", "Retry Plan item")}</button></p>;
      if (!item) return <p key={selection.work_item_id} role="status">{t("正在读取计划项…", "Loading the Plan item…")}</p>;
      const full = selection.module_ordinal === selected.length;
      const fields = evidenceFields.filter(([key]) => full || (key !== "functional_verification" && key !== "robustness_audit"));
      const draft = drafts.data[item.id] ?? emptyDraft;
      const ready = state.checkpoints.some((checkpoint) => checkpoint.work_item_id === item.id && checkpoint.gate_ready &&
        checkpoint.work_item_version === item.version && checkpoint.mode_revision === detail.mode.revision);
      const dependenciesReady = item.dependencies.every((id) => items.some((entry) => entry.data?.id === id && entry.data.status === "completed"));
      const completionSource = state.continued_completions?.find((source) => source.work_item_id === item.id);
      const body: PlanDeliveryWorkItemControlRequestView = { version: "plan_delivery_control.v1", expected_work_item_version: item.version };
      return <article className="plan-manual-item" key={item.id}>
        <header><h4>{selection.module_ordinal}. {item.title}</h4><StatusBadge status={item.status} /></header>
        {item.status === "completed" && completionSource && <div>
          <p>{completionSource.completion_event_id ? t("已沿用原对话的事项完成进度。人工验收与本次自动检查请分别核对对应记录。", "Item completion is carried forward from this conversation. Review manual acceptance and this execution's automated checks in their respective records.") :
            t("沿用原对话的人工完成记录，当前执行的自动检查以本次验证结果为准。", "Manual completion is continued from this conversation. Current automated checks are determined by this execution's verification results.")}</p>
          {completionSource.completion_event_id ? <details className="plan-completion-source"><summary>{t("查看完成来源", "View completion source")}</summary>
            <dl><dt>{t("来源执行", "Source execution")}</dt><dd><code>{completionSource.source_run_id}</code></dd>
              <dt>{t("来源事项", "Source item")}</dt><dd><code>{completionSource.source_work_item_id}</code></dd>
              <dt>{t("完成记录", "Completion record")}</dt><dd><code>{completionSource.completion_event_id}</code></dd></dl>
          </details> : completionSource.handoff_note_id && <PlanDeliveryHandoff client={client} runID={completionSource.source_run_id} noteID={completionSource.handoff_note_id} />}
        </div>}
        <ul>{item.acceptance_criteria.map((criterion) => <li key={criterion}>{criterion}</li>)}</ul>
        {item.dependencies.length > 0 && <p>{t("依赖：", "Dependencies: ")}{item.dependencies.map((id) =>
          items.find((entry) => entry.data?.id === id)?.data?.title ?? id).join(" · ")}</p>}
        {item.status === "pending" && <button className="command-button" disabled={!mutable || locked || !dependenciesReady}
          onClick={() => submit({ ...newAttempt(item.id), action: "start", body })} type="button">
          {t("开始此项", "Start this item")}</button>}
        {!dependenciesReady && item.status !== "completed" && <p>{t("依赖项完成后可继续。", "Complete the dependencies before continuing.")}</p>}
        {item.status === "in_progress" && <>
          {!ready && !manualRequired && <button className="command-button" type="button"
            aria-expanded={Boolean(optionalForms[`${runID}:${item.id}`])}
            onClick={() => setOptionalForms((current) => ({ ...current, [`${runID}:${item.id}`]: !current[`${runID}:${item.id}`] }))}>
            {optionalForms[`${runID}:${item.id}`] ? t("收起人工说明", "Hide manual notes") : t("补充人工验收说明（可选）", "Add manual acceptance notes (optional)")}</button>}
          {!ready && (manualRequired || optionalForms[`${runID}:${item.id}`]) && <form onSubmit={(event) => {
            event.preventDefault();
            if (!mutable || locked) return;
            const evidence = Object.fromEntries(fields.map(([key]) => [key, draft[key].trim()]));
            if (fields.some(([key]) => !draft[key].trim() || Array.from(draft[key]).length > (key === "handoff_summary" ? 2_048 : 1_024))) return;
            submit({ ...newAttempt(item.id), action: "checkpoint", body: { ...body, ...evidence } as PlanDeliveryCheckpointControlRequestView });
          }}>
            <fieldset disabled={!mutable || locked}>
              <legend>{t("人工验收说明", "Manual acceptance notes")}</legend>
              {full && <p>{t("这是最后一项，还需说明完整功能与异常恢复检查。", "The final item also requires full functional and failure recovery evidence.")}</p>}
              {fields.map(([key, zh, en]) => <label htmlFor={`${formID}-${item.id}-${key}`} key={key}>
                {t(zh, en)}<textarea id={`${formID}-${item.id}-${key}`} maxLength={key === "handoff_summary" ? 2_048 : 1_024}
                  required rows={3} value={draft[key]} onChange={(event) => {
                    const text = event.target.value;
                    queryClient.setQueryData<Record<string, Draft>>(draftsKey(runID), (current) => ({ ...current,
                      [item.id]: { ...emptyDraft, ...current?.[item.id], [key]: text } }));
                  }} />
              </label>)}
              <button className="command-button" type="submit">{t("记录人工验收与交接", "Record manual acceptance and handoff")}</button>
            </fieldset>
          </form>}
          <p>{!manualRequired ? t("确认此项验收标准已实际满足后可完成；当前交付仍由真实检查和版本门禁核验。", "Complete this item after its acceptance criteria are actually met; delivery remains subject to real checks and revision validation.") :
            ready ? t("当前版本的人工验收已保存，可在检查点历史查看。该版本保留原记录；确认验收标准已满足后，完成此项。", "Manual acceptance for this revision is saved in checkpoint history. The revision retains its original record; confirm the acceptance criteria are met, then complete this item.") :
            t("完成此项前，需要当前版本的人工验收记录。", "A manual acceptance record for the current version is required before completion.")}</p>
          <button className="command-button" disabled={!mutable || locked || (manualRequired && !ready) || !dependenciesReady}
            onClick={() => submit({ ...newAttempt(item.id), action: "complete", body })} type="button">{t("完成此项", "Complete this item")}</button>
        </>}
        {item.status === "blocked" && <p>{item.blocked_reason || t("此项处于阻塞状态，请先处理阻塞原因。", "Resolve this item's blocked state before continuing.")}</p>}
      </article>;
    })}
  </section>;
}

export function PlanDeliveryHandoff({ client, runID, noteID }: { client: APIClient; runID: string; noteID: string }) {
  const { t } = useLocale();
  const [open, setOpen] = useState(false);
  const note = useQuery({ queryKey: ["note", noteID], queryFn: ({ signal }) => client.getNote(noteID, signal), enabled: open });
  return <details onToggle={(event) => setOpen(event.currentTarget.open)} className="plan-handoff-note">
    <summary>{t("查看人工验收与交接说明", "Read manual acceptance and handoff")}</summary>
    {open && (note.isError || (note.data && note.data.run_id !== runID) ? <p role="alert">
      {t("交接说明读取失败。", "Handoff could not be loaded.")}<button onClick={() => void note.refetch()} type="button">{t("重试", "Retry")}</button></p> :
      note.data ? <pre>{note.data.content}</pre> : <p role="status">{t("正在读取交接说明…", "Loading handoff…")}</p>)}
  </details>;
}
