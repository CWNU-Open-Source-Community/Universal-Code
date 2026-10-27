import { useLocale, type LocaleText } from "../../lib/locale";
import { useEffect, useRef, useState } from "react";
import { createPortal } from "react-dom";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { ListChecks, X } from "lucide-react";
import { APIRequestError, type APIClient } from "../../api/client";
import type { RunDetailView } from "../../api/types";
import { executeThreadPlan, observeThreadPlan, validThreadPlanAttempt, type ThreadPlanAttempt, type ThreadPlanObservation, type ThreadPlanRequest } from "../../api/thread-plan";
import { useModalFocusTrap } from "../../hooks/use-modal-focus-trap";
import { useV2PersistentState, useV2RecoveryStore } from "../recovery-storage";
import { v2QueryKeys } from "../query-keys";
import { V2PhasePicker } from "./phase-picker";
import "./thread-plan.css";

const observationText = (value: ThreadPlanObservation, t: LocaleText) => {
  if (value.state === "not_received") return t("服务端尚未找到原请求。可以继续核对，或主动重试原操作。", "The server has not found the original request. Check again or retry it.");
  if (value.state === "prepared") return t("计划选择或工作方式已保存，但执行消息尚未确认接收。继续时会使用原请求核对并完成剩余步骤。", "The plan or work mode is saved, but the execution message is not confirmed. Continue to check the original request and finish the remaining steps.");
  if (value.state === "received") return t("执行消息已被当前对话接收。实际进度请看对话中的工作状态。", "The conversation received the execution message. Follow progress in the conversation.");
  if (value.state === "failed") return t(`本次执行未完成${value.turn_request?.error_code ? `（${value.turn_request.error_code}）` : ""}。已保存的记录保留，可以在当前对话继续补充要求。`, `This execution did not finish${value.turn_request?.error_code ? ` (${value.turn_request.error_code})` : ""}. Saved records are kept; continue with further instructions in this conversation.`);
  if (value.state === "rejected") return t("原请求未被接受。请查看当前计划和要求后重新操作。", "The original request was rejected. Review the current plan and requirements before trying again.");
  if (value.action === "enter_deliver") return t("已切换为直接执行。草稿仍保留，发送消息后才会开始处理。", "Switched to direct execution. Your draft is kept; work starts when you send a message.");
  return value.action === "enter_plan" ? t("已切换为先规划。发送需求后会在当前对话形成计划。", "Switched to planning. Send your requirements to create a plan in this conversation.") : t("计划已确认，本次执行回合已结束。请在对话中查看结果和待处理事项。", "The plan is confirmed and this execution turn has ended. Check the conversation for results and remaining work.");
};
const settled = (value: ThreadPlanObservation) => ["received", "completed", "failed", "rejected"].includes(value.state);
const planErrorText = (failure: unknown, t: LocaleText) => {
  if (!(failure instanceof APIRequestError)) return failure instanceof Error ? failure.message : t("计划操作结果尚未确认，请核对原请求。", "The plan operation is unconfirmed. Check the original request.");
  const descriptions: Record<string, string> = {
    CONFLICT: t("计划或任务状态已经变化，请核对原请求并重新读取最新计划。", "The plan or task changed. Check the original request and reload the latest plan."),
    FAILED_PRECONDITION: t("当前状态暂不允许更改计划。请检查对话中是否还有执行、批准或待处理操作。", "The plan cannot be changed now. Check for running work, approvals or pending operations."),
    NOT_FOUND: t("当前连接未提供这个计划操作，或对应记录已经不可用。", "This connection does not support this plan operation, or its record is unavailable."),
    FORBIDDEN: t("当前权限不能更改这个任务的计划。", "Current permissions do not allow changing this task's plan."),
    UNAUTHORIZED: t("连接认证已失效，请重新连接后核对原请求。", "Authentication expired. Reconnect and check the original request."),
    UNAVAILABLE: t("暂时无法确认计划操作结果，原请求已保留。", "The plan result cannot be confirmed yet. The original request is kept."),
    INVALID_RESPONSE: t("计划操作回包无法核实，原请求已保留。", "The plan response could not be verified. The original request is kept."),
  };
  return descriptions[failure.code] ?? t(`计划操作未确认（${failure.code}），请核对原请求。`, `Plan operation unconfirmed (${failure.code}). Check the original request.`);
};

interface Props {
  client: APIClient; threadID: string; runID: string; active: boolean; working: boolean;
  hasUnsentDraft: boolean; onRequestChange: (content: string) => void;
}
export function V2ThreadPlanControl(props: Props) {
  return <ThreadPlanContent key={props.threadID} {...props} />;
}

function ThreadPlanContent({ client, threadID, runID, active, working, hasUnsentDraft, onRequestChange }: Props) {
  const { t } = useLocale();
  const queryClient = useQueryClient();
  const store = useV2RecoveryStore();
  const storageKey = `thread:${threadID}:plan-attempt`;
  const [saved, setSaved] = useV2PersistentState<unknown>(storageKey, null);
  const attempt = validThreadPlanAttempt(saved, threadID) ? saved : null;
  const damaged = saved !== null && !attempt;
  const [open, setOpen] = useState(false);
  const [choice, setChoice] = useState<{ proposalID: string; direction: number } | null>(null);
  const [error, setError] = useState("");
  const [result, setResult] = useState<ThreadPlanObservation | null>(null);
  const closeButton = useRef<HTMLButtonElement>(null);
  const trigger = useRef<HTMLButtonElement>(null);
  const modeTrigger = useRef<HTMLButtonElement>(null);
  // A successful refresh can remove an error-only plan entry while the dialog is open.
  const returnFocus = { get current() { return trigger.current ?? modeTrigger.current; } };
  const dialog = useModalFocusTrap<HTMLElement>(open, () => setOpen(false), false, closeButton, { isolateBackground: true, returnFocusRef: returnFocus });
  const alive = useRef(true);
  useEffect(() => { alive.current = true; return () => { alive.current = false; }; }, []);
  const available = client.hasThreadControl && client.hasPlanDelivery;
  const detail = useQuery({ queryKey: ["run", runID, "conversation-plan"], enabled: available && Boolean(runID), retry: false,
    queryFn: async ({ signal }) => {
      const value = await client.get<RunDetailView>(`/runs/${encodeURIComponent(runID)}`, {}, signal);
      if (value.run.id !== runID || !["plan", "deliver"].includes(value.mode.phase)) throw new Error(t("工作方式的来源不匹配，请重新读取。", "The work mode belongs to a different source. Reload it."));
      return value;
    }, refetchOnMount: "always", refetchInterval: working || open ? 1500 : 5000 });
  const observed = useQuery({ queryKey: ["thread", threadID, "plan-request", attempt?.key], enabled: Boolean(attempt), retry: false,
    queryFn: ({ signal }) => observeThreadPlan(client, attempt!, signal), refetchOnMount: "always" });
  const refresh = () => {
    void queryClient.invalidateQueries({ queryKey: v2QueryKeys.thread(threadID) });
    void queryClient.invalidateQueries({ queryKey: ["run", runID] });
    void queryClient.invalidateQueries({ queryKey: v2QueryKeys.threads() });
  };
  const clear = (original: ThreadPlanAttempt) => {
    if (!store) {
      if (alive.current) setSaved((current: unknown) => validThreadPlanAttempt(current, threadID) && current.key === original.key ? null : current);
      return;
    }
    const stored = store?.read<unknown>(storageKey, null) ?? saved;
    if (!validThreadPlanAttempt(stored, threadID) || stored.key !== original.key) return;
    store?.write(storageKey, null);
    if (alive.current) setSaved(null);
  };
  useEffect(() => {
    if (!attempt || !observed.data || !settled(observed.data)) return;
    setResult(observed.data);
    try { clear(attempt); setError(""); } catch { setError(t("结果已核对，但本机记录尚未结清；原请求已保留。", "The result was checked, but the local record is unsettled. The original request is kept.")); }
    refresh();
  }, [observed.data, attempt?.key]);
  const mutation = useMutation({ mutationFn: async (original: ThreadPlanAttempt) => {
    store?.assertReadable(storageKey);
    const current = store?.read<unknown>(storageKey, null);
    if (current && (!validThreadPlanAttempt(current, threadID) || current.key !== original.key)) throw new Error(t("另一个计划操作尚待核对，请先查看原请求。", "Another plan operation awaits confirmation. Check the original request first."));
    store?.write(storageKey, original);
    if (alive.current) { setSaved(original); setError(""); setResult(null); }
    return executeThreadPlan(client, original);
  }, onSuccess: (value, original) => {
    queryClient.setQueryData(["thread", threadID, "plan-request", original.key], value);
    if (settled(value)) {
      try { clear(original); } catch { if (alive.current) setError(t("结果已核对，但本机记录尚未结清；原请求已保留。", "The result was checked, but the local record is unsettled. The original request is kept.")); }
    }
    if (alive.current) { setResult(value); if (value.action === "confirm" && settled(value) && value.state !== "rejected") setOpen(false); }
    refresh();
  }, onError: (failure, original) => {
    if (failure instanceof APIRequestError && [400, 401, 403, 404, 409, 422].includes(failure.status)) {
      const current = store?.read<unknown>(storageKey, null);
      if (!store || validThreadPlanAttempt(current, threadID) && current.key === original.key) {
        const rejected = { ...original, responseRejected: true as const };
        try { store?.write(storageKey, rejected); if (alive.current) setSaved(rejected); } catch { /* Keep the original identity if storage is unavailable. */ }
      }
    }
    if (alive.current) setError(planErrorText(failure, t));
    void queryClient.invalidateQueries({ queryKey: ["thread", threadID, "plan-request"] });
    refresh();
  } });
  const phase = detail.data?.mode.phase;
  const proposal = detail.data?.plan_delivery?.proposal;
  const selection = detail.data?.plan_delivery?.selection;
  const confirmed = selection?.proposal_id === proposal?.id
    ? proposal?.directions.find((direction) => direction.ordinal === selection?.direction_ordinal) : undefined;
  const latest = choice?.proposalID === proposal?.id ? proposal?.directions.find((item) => item.ordinal === choice?.direction) : undefined;
  const ready = available && active && !working && !mutation.isPending && !attempt && !damaged && !detail.isError && !detail.isPending;
  const show = () => {
    setOpen(true);
    if (phase === "plan" && !confirmed && proposal?.directions.length === 1) setChoice({ proposalID: proposal.id, direction: proposal.directions[0].ordinal });
    else if (choice?.proposalID !== proposal?.id) setChoice(null);
    void detail.refetch();
  };
  const start = (body: ThreadPlanRequest) => {
    if (!ready) return;
    mutation.mutate({ threadID, key: `thread-plan-${crypto.randomUUID()}`, body });
  };
  const confirmContent = latest ? t(`请按已确认的计划“${latest.title}”继续执行，保留原目标、后续修正和限制。需要调整计划时先说明原因；执行沿用当前操作权限。`, `Continue with the confirmed plan “${latest.title}”, preserving the original goal, subsequent changes and constraints. Explain any plan changes first; use the current permissions.`) : "";
  const shownObservation = attempt ? observed.data : result;
  const needsAttention = Boolean(attempt || damaged || error || detail.isError || result?.state === "failed" || result?.state === "rejected");
  const hasPlanEntry = Boolean(proposal || needsAttention || open);
  if (!available && !attempt && !damaged) return null;
  return <>
    <div className="v2-thread-plan-controls">
      {phase && <V2PhasePicker phase={phase} disabled={!ready} buttonRef={modeTrigger} onChange={(next) => {
        start({ version: "plan_delivery_control.v1", action: next === "plan" ? "enter_plan" : "enter_deliver", run_id: runID });
      }} />}
      {hasPlanEntry && <button className={`v2-plan-trigger${needsAttention ? " has-attention" : ""}`} ref={trigger} type="button" onClick={show} aria-haspopup="dialog" aria-expanded={open}>
        <ListChecks size={14} aria-hidden="true" /><span className="v2-composer-optional-label">{attempt || damaged ? t("计划操作待核对", "Plan operation awaiting confirmation") : detail.isError ? t("计划读取失败", "Could not load plan") : needsAttention ? t("计划操作未完成", "Plan operation incomplete") : t("查看计划", "View plan")}</span>
      </button>}
    </div>
    {open && createPortal(<div className="v2-plan-overlay" onMouseDown={(event) => { if (event.target === event.currentTarget) setOpen(false); }}>
      <section className="v2-thread-plan" ref={dialog} role="dialog" aria-modal="true" aria-labelledby="v2-plan-title">
        <header><h2 id="v2-plan-title">{phase === "plan" && !confirmed ? t("确认计划", "Confirm plan") : t("任务计划", "Task plan")}</h2>
          <button type="button" ref={closeButton} aria-label={t("关闭计划", "Close plan")} onClick={() => setOpen(false)}><X size={18} /></button></header>
        <p className="v2-plan-help">{t("先规划时先分析和形成方案。确认后在同一对话执行；后续修改要求仍可直接发消息。", "Planning analyzes the task and proposes an approach. Confirm to execute in this conversation; send further messages to change requirements.")}</p>
        {detail.isPending && <p role="status">{t("正在读取计划…", "Loading plan…")}</p>}
        {detail.isError && <p role="alert">{t("计划读取失败，请重新读取后确认方案。", "Could not load the plan. Reload it before confirming an approach.")}<button type="button" onClick={() => void detail.refetch()}>{t("重新读取", "Reload")}</button></p>}
        {damaged && <p role="alert">{t("本机计划请求记录无法读取，已保留原记录。请先处理本机存储问题；普通对话仍可继续。", "The local plan request cannot be read. Its record is kept. Resolve the storage issue; ordinary conversation remains available.")}</p>}
        {error && <p role="alert">{error}</p>}
        {attempt && <section className="v2-plan-request" aria-label={t("原计划请求", "Original plan request")}>
          <strong>{t("原计划操作待核对", "Original plan operation awaiting confirmation")}</strong>
          <p>{shownObservation ? observationText(shownObservation, t) : observed.isError ? t("暂时无法确认原请求结果，原内容和请求标识已保留。", "The original result is unconfirmed. Its content and request ID are kept.") : t("正在核对原请求…", "Checking original request…")}</p>
          {attempt.body.content && <blockquote>{attempt.body.content}</blockquote>}
          <div className="v2-plan-actions"><button type="button" disabled={observed.isFetching || mutation.isPending} onClick={() => void observed.refetch()}>{t("核对原请求", "Check original request")}</button>
            {shownObservation && ["not_received", "prepared"].includes(shownObservation.state) && <button type="button" disabled={working || mutation.isPending || !available || attempt.body.run_id !== runID || (attempt.body.action === "confirm" && hasUnsentDraft)}
              onClick={() => mutation.mutate(attempt)}>{t("继续原操作", "Continue original operation")}</button>}
            {shownObservation?.state === "not_received" && attempt.responseRejected && <button type="button" disabled={mutation.isPending} onClick={() => { try { clear(attempt); setResult(null); setError(""); } catch { setError(t("本机原请求暂时无法结清。", "The original local request cannot be settled yet.")); } }}>{t("关闭已拒绝请求", "Close rejected request")}</button>}
          </div>
        </section>}
        {!attempt && shownObservation && <p role="status">{observationText(shownObservation, t)}</p>}
        {!detail.isError && proposal && <div className="v2-plan-directions">
          {proposal.directions.map((direction) => <section key={`${proposal.id}:${direction.ordinal}`}
            className={confirmed?.ordinal === direction.ordinal ? "is-confirmed" : undefined}>
            <label className="v2-plan-choice"><input type="radio" name="plan-direction" checked={confirmed
              ? confirmed.ordinal === direction.ordinal : phase === "plan" && choice?.proposalID === proposal.id && choice.direction === direction.ordinal}
              disabled={Boolean(attempt) || phase !== "plan" || Boolean(confirmed)} onChange={() => setChoice({ proposalID: proposal.id, direction: direction.ordinal })} />
              <strong>{direction.title}</strong>{" "}{confirmed?.ordinal === direction.ordinal && <span className="v2-plan-confirmed">{t("已确认", "Confirmed")}</span>}</label>
            <p>{direction.summary}</p>
            {direction.tradeoffs.length > 0 && <ul>{direction.tradeoffs.map((text, index) => <li key={index}>{text}</li>)}</ul>}
            <details><summary>{t(`查看步骤与验收标准（${direction.modules.length} 项）`, `View steps and acceptance criteria (${direction.modules.length})`)}</summary>
              <ol>{direction.modules.map((module) => <li key={module.ordinal}><strong>{module.title}</strong><p>{module.objective}</p>
                <ul>{module.acceptance_criteria.map((criterion, index) => <li key={index}>{criterion}</li>)}</ul>
                {module.dependencies.length > 0 && <small>{t("依赖步骤：", "Required steps: ")}{module.dependencies.join(t("、", ", "))}</small>}</li>)}</ol>
            </details>
          </section>)}
        </div>}
        {!detail.isPending && !detail.isError && !proposal && <p>{phase === "plan" ? t("尚无可确认的计划。请在对话中提出需求或补充要求，等待计划形成。", "No plan is ready to confirm. Send your requirements in the conversation and wait for a plan.") : t("当前采用默认处理方式。需要先讨论方案时，可开启输入框旁的“计划模式”。", "The default work mode is active. Enable Plan mode beside the composer to discuss an approach first.")}</p>}
        {working && <p className="v2-plan-help">{t("Agent 仍在处理消息。可以直接在对话中补充要求，待本轮处理结束后确认最新计划。", "The agent is still processing messages. Add requirements in the conversation and confirm the latest plan after this turn ends.")}</p>}
        {phase === "plan" && proposal && !confirmed && <>
          {hasUnsentDraft && <p role="status">{t("输入框中还有未发送内容。请先发送修改要求并等待计划更新，再确认执行。", "The composer contains an unsent draft. Send your changes and wait for the plan to update before confirming.")}</p>}
          {!latest && <p className="v2-plan-help">{t("请选择当前方案；计划更新后需要重新核对。", "Select an approach. Review it again if the plan changes.")}</p>}
          {latest && <details><summary>{t("确认时发送的消息", "Message sent on confirmation")}</summary><p>{confirmContent}</p></details>}
          <div className="v2-plan-actions"><button type="button" onClick={() => { setOpen(false); onRequestChange(""); }}>{t("返回对话修改要求", "Return to conversation to change requirements")}</button>
            <button type="button" className="v2-plan-confirm" disabled={!ready || !latest || hasUnsentDraft} onClick={() => {
              if (!latest || !proposal || hasUnsentDraft) return;
              start({ version: "plan_delivery_control.v1", action: "confirm", run_id: runID, proposal_id: proposal.id,
                direction: latest.ordinal, manual_acceptance: "on_demand", content: confirmContent });
            }}>{mutation.isPending ? t("正在确认…", "Confirming…") : t("确认计划并执行", "Confirm plan and execute")}</button></div>
        </>}
      </section>
    </div>, document.body)}
  </>;
}
