import { useId } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Check, ChevronsRight, ListChecks, LoaderCircle } from "lucide-react";
import { APIRequestError, type APIClient } from "../api/client";
import type { PlanDeliveryStateView, PlanDirectionControlRequestView,
  PlanDeliveryTransitionControlRequestView, RunDetailView, RunLifecycleControlRequestView } from "../api/types";
import { formatNumber, shortID } from "../lib/format";
import { useLocale } from "../lib/locale";
import { canonicalVocabulary } from "../lib/vocabulary";
import { v2QueryKeys } from "../v2/query-keys";
import { StatusBadge } from "./common";
import { PlanDeliveryHandoff, PlanDeliveryWorkItems } from "./plan-delivery-work-items";

type PlanAttempt = { runID: string; threadID?: string; operationKey: string } & (
  { kind: "adopt"; body: PlanDirectionControlRequestView; deliveryOperationKey: string; selectionID?: string } |
  { kind: "direction"; body: PlanDirectionControlRequestView } |
  { kind: "deliver"; selectionID: string; body: PlanDeliveryTransitionControlRequestView } |
  { kind: "pause"; body: RunLifecycleControlRequestView });
interface PlanInteraction { attempt: PlanAttempt; state: "pending" | "unknown" | "rejected"; error?: string; unconfirmed?: boolean }
const planIntentKey = (runID: string) => ["run", runID, "plan-control-intent"] as const;

export function PlanDeliveryPanel({ state, client, detail, threadID }: {
  state: PlanDeliveryStateView;
  client?: APIClient;
  detail?: RunDetailView;
  threadID?: string;
}) {
  const { t } = useLocale();
  const queryClient = useQueryClient();
  const manualDescriptionID = useId();
  const manualPreferenceKey = ["run", detail?.run.id ?? "", "plan-manual-preference"] as const;
  const manualPreference = useQuery<boolean>({ queryKey: manualPreferenceKey, queryFn: () => false,
    enabled: false, initialData: false, gcTime: Infinity });
  const intent = useQuery<PlanInteraction | null>({ queryKey: planIntentKey(detail?.run.id ?? ""),
    queryFn: () => null, enabled: false, initialData: null, gcTime: Infinity });
  const mutation = useMutation({
    mutationFn: async (attempt: PlanAttempt) => {
      if (!client) throw new Error(t("计划控制不可用", "Plan controls are unavailable"));
      if (attempt.kind === "adopt") {
        let selectionID = attempt.selectionID;
        if (!selectionID) {
          const selected = await client.selectPlanDirection(attempt.runID, attempt.body, attempt.operationKey);
          selectionID = selected.selection_id;
          queryClient.setQueryData<PlanInteraction | null>(planIntentKey(attempt.runID), (current) =>
            current?.attempt.operationKey === attempt.operationKey
              ? { attempt: { ...attempt, selectionID }, state: "pending" } : current);
        }
        const result = await client.enterPlanDelivery(attempt.runID, { version: "plan_delivery_control.v1" }, attempt.deliveryOperationKey);
        if (result.selection_id !== selectionID) throw new APIRequestError("Deliver result belongs to a different Plan selection", "INVALID_RESPONSE", 502);
        return result;
      }
      if (attempt.kind === "direction") return client.selectPlanDirection(attempt.runID, attempt.body, attempt.operationKey);
      if (attempt.kind === "deliver") {
        const result = await client.enterPlanDelivery(attempt.runID, attempt.body, attempt.operationKey);
        if (result.selection_id !== attempt.selectionID) throw new APIRequestError("Deliver result belongs to a different Plan selection", "INVALID_RESPONSE", 502);
        return result;
      }
      return client.controlRunLifecycle(attempt.runID, attempt.body, attempt.operationKey);
    },
    onSuccess: (_result, attempt) => {
      queryClient.setQueryData<PlanInteraction | null>(planIntentKey(attempt.runID), (current) =>
        current?.attempt.operationKey === attempt.operationKey ? null : current);
    },
    onError: (error, attempt) => {
      const rejected = error instanceof APIRequestError && error.code !== "INVALID_RESPONSE" &&
        [400, 401, 403, 404, 409, 412, 422].includes(error.status);
      queryClient.setQueryData<PlanInteraction | null>(planIntentKey(attempt.runID), (current) =>
        current?.attempt.operationKey === attempt.operationKey ? { ...current,
          state: rejected && !current.unconfirmed ? "rejected" : "unknown",
          unconfirmed: current.unconfirmed || !rejected, error: error.message } : current);
    },
    onSettled: (_result, _error, attempt) => {
      void queryClient.invalidateQueries({ queryKey: ["run", attempt.runID] });
      if (attempt.threadID) {
        void queryClient.invalidateQueries({ queryKey: v2QueryKeys.thread(attempt.threadID) });
        void queryClient.invalidateQueries({ queryKey: v2QueryKeys.permission(attempt.threadID) });
      }
    },
  });
  const submit = (attempt: PlanAttempt) => {
    const current = queryClient.getQueryData<PlanInteraction | null>(planIntentKey(attempt.runID));
    if (!client || !(attempt.kind === "pause" ? client.hasRunLifecycle : client.hasPlanDelivery) ||
      current?.state === "pending" || (current && current.attempt.operationKey !== attempt.operationKey)) return;
    queryClient.setQueryData<PlanInteraction>(planIntentKey(attempt.runID), { attempt, state: "pending",
      unconfirmed: current?.unconfirmed || current?.state === "unknown" });
    mutation.mutate(attempt);
  };
  const attemptIdentity = () => ({ runID: detail!.run.id, threadID, operationKey: `web-plan-${globalThis.crypto.randomUUID()}` });
  const selected = state.selection?.direction_ordinal;
  const validDirections = Boolean(state.proposal && state.proposal.directions.length >= 1 && state.proposal.directions.length <= 3 &&
    state.proposal.directions.every((direction, index) => direction.ordinal === index + 1));
  const adopted = intent.data?.attempt.kind === "adopt" && Boolean(intent.data.attempt.selectionID);
  const manualRequired = state.selection?.manual_acceptance !== "on_demand";
  const mutable = Boolean(client?.hasPlanDelivery && detail &&
    (detail.run.status === "created" || detail.run.status === "paused") &&
    detail.mode.phase === "plan" && !detail.execution_lease?.active);
  const selecting = Boolean(intent.data);
  const status = !state.proposal ? t("尚无计划方案", "No plan proposal yet") : state.operator_choice_needed
    ? t("需要操作者选择", "Operator choice required")
    : state.phase_change_needed
      ? t("需要进入交付阶段", "Deliver phase required")
      : t("已选择方向", "Direction selected");
  return (
    <section className="detail-section plan-delivery-section">
      <div className="section-heading">
        <h2><ListChecks aria-hidden="true" size={15} />{t("计划 / 交付", "Plan / Delivery")}</h2>
        <StatusBadge status={state.operator_choice_needed ? "pending" : "accepted"} />
      </div>
      <div className="plan-state-line">
        <span>{status}</span>
        {state.selection && state.delivery_gate_enforced && manualRequired && <span>{state.continued_completions?.length ? t("有效人工记录（含沿用）", "Valid manual records (including continued)") :
          t("当前有效人工记录", "Current manual records")} {formatNumber(state.ready_checkpoints)} / {formatNumber(state.required_checkpoints)}</span>
        }
        {state.selection && state.delivery_gate_enforced && !manualRequired && <span>{t("人工说明按需记录；计划项仍须实际完成", "Manual notes on demand; Plan items still require actual completion")}</span>}
        {state.selection && !state.delivery_gate_enforced && <span>{t("此旧版计划沿用原有执行与检查要求，按原验收流程交付。", "This legacy Plan retains its original execution and verification requirements. Use its original acceptance workflow to deliver.")}</span>}
      </div>
      {!state.proposal && <p>{t("暂无待选方案", "No proposals yet")}</p>}
      {detail?.mode.phase === "plan" && detail.run.status === "running" && state.proposal && <div className="plan-delivery-actions">
        <p>{t("选择方向或进入交付前，需暂停当前执行。", "Pause the current execution before selecting a direction or entering Deliver.")}</p>
        {client?.hasRunLifecycle && <button className="command-button" disabled={selecting || Boolean(detail.execution_lease?.active)}
          onClick={() => submit({ ...attemptIdentity(), kind: "pause", body: { version: "run_lifecycle_control.v1", action: "pause" } })} type="button">
          {t("暂停以确认计划", "Pause to review the plan")}</button>}
        {detail.execution_lease?.active && <p>{t("当前执行尚未结束，请先等待或回到对话停止。", "Execution is still active. Wait or return to the conversation to stop it.")}</p>}
      </div>}
      {mutable && state.operator_choice_needed && state.proposal && <div className="plan-manual-option">
        <label><input checked={manualPreference.data} disabled={selecting} type="checkbox" aria-describedby={manualDescriptionID}
          onChange={(event) => queryClient.setQueryData(manualPreferenceKey, event.target.checked)} />
          <span>{t("需要逐项人工验收", "Require manual acceptance for each item")}</span></label>
        <p id={manualDescriptionID}>{t("默认按需记录人工说明；真实检查、文件审批与计划项完成要求保持。", "Manual notes are optional by default; actual checks, file approval, and Plan item completion remain required.")}</p>
      </div>}
      {state.proposal && !validDirections && <p role="alert">{t("方案数量或序号不一致，请刷新计划后再选择。", "The proposal count or ordinals are inconsistent. Refresh the Plan before choosing.")}</p>}
      <div className="plan-direction-list">
        {state.proposal?.directions.map((direction) => (
          <details className={selected === direction.ordinal ? "plan-direction selected" : "plan-direction"}
            key={direction.ordinal} open={selected === direction.ordinal || state.proposal?.directions.length === 1 || undefined}>
            <summary>
              <span className="plan-ordinal">{direction.ordinal}</span>
              <span><strong>{direction.title}</strong><small>{direction.summary}</small></span>
              <span>{t(`${direction.modules.length} 个切片`, `${direction.modules.length} slices`)}</span>
              {selected === direction.ordinal && <StatusBadge status="selected" />}
            </summary>
            <div className="plan-direction-body">
              <div><h3>{t("权衡", "Tradeoffs")}</h3><ul>{direction.tradeoffs.map((item) => <li key={item}>{item}</li>)}</ul></div>
              <div><h3>{t("交付切片", "Delivery slices")}</h3><ol>{direction.modules.map((module) => (
                <li key={module.ordinal}>
                  <strong>{module.title}</strong>
                  <p>{module.objective}</p>
                  <ul>{module.acceptance_criteria.map((criterion) => <li key={criterion}>{criterion}</li>)}</ul>
                  <small>{module.dependencies.length > 0 ? t(`依赖 ${module.dependencies.join(", ")}`, `Depends on ${module.dependencies.join(", ")}`) : t("无依赖", "No dependencies")}</small>
                </li>
              ))}</ol></div>
              {mutable && state.operator_choice_needed && state.proposal && (
                <button className="command-button plan-choice-button" disabled={selecting || !validDirections}
                  onClick={() => submit({ ...attemptIdentity(), kind: "adopt",
                    deliveryOperationKey: `web-plan-deliver-${globalThis.crypto.randomUUID()}`, body: {
                    version: "plan_delivery_control.v1", proposal_id: state.proposal!.id, direction: direction.ordinal,
                    manual_acceptance: manualPreference.data ? "required" : "on_demand" } })} type="button">
                  {intent.data?.state === "pending" && intent.data.attempt.kind === "adopt" && intent.data.attempt.body.direction === direction.ordinal
                    ? <LoaderCircle aria-hidden="true" className="spin" size={15} />
                    : <Check aria-hidden="true" size={15} />}{state.proposal.directions.length === 1
                      ? t("采用方案并开始交付", "Adopt plan and enter Deliver")
                      : t(`采用方案 ${direction.ordinal} 并开始交付`, `Adopt plan ${direction.ordinal} and enter Deliver`)}
                </button>
              )}
            </div>
          </details>
        ))}
      </div>
      {mutable && state.selection && state.phase_change_needed && (
        <div className="plan-delivery-actions">
          <button className="command-button" disabled={selecting}
            onClick={() => submit({ ...attemptIdentity(), kind: "deliver", selectionID: state.selection!.id,
              body: { version: "plan_delivery_control.v1" } })} type="button">
            {intent.data?.state === "pending"
              ? <LoaderCircle aria-hidden="true" className="spin" size={15} />
              : <ChevronsRight aria-hidden="true" size={15} />}{t("进入交付", "Enter Deliver")}
          </button>
        </div>
      )}
      {adopted && <p role="status">{t("方案已采用，正在核对进入交付的结果。", "The plan is adopted. Checking the result of entering Deliver.")}</p>}
      {intent.data?.state === "pending" && <p role="status">{t("计划操作处理中，关闭后可回到此执行查看结果。", "The plan operation is pending; return to this execution after closing to see the result.")}</p>}
      {intent.data?.state === "unknown" && (
        <div className="inline-warning" role="alert">
          <p>{t("计划操作结果尚未确认，请使用原请求确认。", "The plan operation result is unknown. Confirm the original request.")} {intent.data.error}</p>
          <button className="command-button" disabled={!(intent.data.attempt.kind === "pause" ? client?.hasRunLifecycle : client?.hasPlanDelivery)}
            onClick={() => submit(intent.data!.attempt)} type="button">{t("确认上次计划操作", "Confirm previous plan operation")}</button>
        </div>
      )}
      {state.selection && (
        <div className="delivery-checkpoint-list" aria-label={t("交付检查点历史", "Delivery checkpoint history")}>
          <h3>{t("检查点历史", "Checkpoint history")}</h3>
          {state.checkpoints.length === 0 ? <p>{state.continued_completions?.some((entry) => !entry.completion_event_id) ?
            t("当前执行尚无新的人工验收记录；沿用的历史说明可从下方计划项查看。", "This execution has no new manual acceptance records. Read continued historical notes in the Plan items below.") :
            t("没有检查点记录", "No checkpoints recorded")}</p> : state.checkpoints.map((checkpoint) => (
            <div className="delivery-checkpoint-row" key={checkpoint.id}>
              <span>{t("切片", "Slice")} {checkpoint.module_ordinal}/{checkpoint.module_count}</span>
              <code>{shortID(checkpoint.work_item_id)}</code>
              <span>{t("模式", "mode")} r{checkpoint.mode_revision} / {t(...canonicalVocabulary.planItem)} v{checkpoint.work_item_version}</span>
              {checkpoint.full_gate_required && <span>{t("完整门禁", "full gate")}</span>}
              <StatusBadge status={checkpoint.gate_ready ? "ready" : "stale"} />
              {client && detail && <PlanDeliveryHandoff client={client} runID={detail.run.id} noteID={checkpoint.handoff_note_id} />}
            </div>
          ))}
        </div>
      )}
      {intent.data?.state === "rejected" && <div className="inline-warning" role="alert">
        <p>{t("服务拒绝了此步骤，请先核对最新任务状态与权限。", "The service rejected this step. Check the current task state and permissions first.")} {intent.data.error}</p>
        {adopted ? <button disabled={!client?.hasPlanDelivery} className="command-button" type="button"
          onClick={() => submit(intent.data!.attempt)}>{t("重试进入交付", "Retry entering Deliver")}</button>
          : <button className="command-button" type="button" onClick={() => queryClient.setQueryData(planIntentKey(detail!.run.id), null)}>
            {t("返回修改", "Return to editing")}</button>}
      </div>}
      {state.selection && client && detail && detail.mode.phase === "deliver" &&
        <PlanDeliveryWorkItems client={client} detail={detail} state={state} threadID={threadID} />}
    </section>
  );
}
