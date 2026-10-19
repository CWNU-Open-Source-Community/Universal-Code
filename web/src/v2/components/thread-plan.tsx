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

const observationText = (value: ThreadPlanObservation) => {
  if (value.state === "not_received") return "服务端尚未找到原请求。可以继续核对，或主动重试原操作。";
  if (value.state === "prepared") return "计划选择或工作方式已保存，但执行消息尚未确认接收。继续时会使用原请求核对并完成剩余步骤。";
  if (value.state === "received") return "执行消息已被当前对话接收。实际进度请看对话中的工作状态。";
  if (value.state === "failed") return `本次执行未完成${value.turn_request?.error_code ? `（${value.turn_request.error_code}）` : ""}。已保存的记录保留，可以在当前对话继续补充要求。`;
  if (value.state === "rejected") return "原请求未被接受。请查看当前计划和要求后重新操作。";
  if (value.action === "enter_deliver") return "已切换为直接执行。草稿仍保留，发送消息后才会开始处理。";
  return value.action === "enter_plan" ? "已切换为先规划。发送需求后会在当前对话形成计划。" : "计划已确认，本次执行回合已结束。请在对话中查看结果和待处理事项。";
};
const settled = (value: ThreadPlanObservation) => ["received", "completed", "failed", "rejected"].includes(value.state);
const planErrorText = (failure: unknown) => {
  if (!(failure instanceof APIRequestError)) return failure instanceof Error ? failure.message : "计划操作结果尚未确认，请核对原请求。";
  const descriptions: Record<string, string> = {
    CONFLICT: "计划或任务状态已经变化，请核对原请求并重新读取最新计划。",
    FAILED_PRECONDITION: "当前状态暂不允许更改计划。请检查对话中是否还有执行、批准或待处理操作。",
    NOT_FOUND: "当前连接未提供这个计划操作，或对应记录已经不可用。",
    FORBIDDEN: "当前权限不能更改这个任务的计划。",
    UNAUTHORIZED: "连接认证已失效，请重新连接后核对原请求。",
    UNAVAILABLE: "暂时无法确认计划操作结果，原请求已保留。",
    INVALID_RESPONSE: "计划操作回包无法核实，原请求已保留。",
  };
  return descriptions[failure.code] ?? `计划操作未确认（${failure.code}），请核对原请求。`;
};

interface Props {
  client: APIClient; threadID: string; runID: string; active: boolean; working: boolean;
  hasUnsentDraft: boolean; onRequestChange: (content: string) => void;
}
export function V2ThreadPlanControl(props: Props) {
  return <ThreadPlanContent key={props.threadID} {...props} />;
}

function ThreadPlanContent({ client, threadID, runID, active, working, hasUnsentDraft, onRequestChange }: Props) {
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
      if (value.run.id !== runID || !["plan", "deliver"].includes(value.mode.phase)) throw new Error("工作方式的来源不匹配，请重新读取。");
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
    try { clear(attempt); setError(""); } catch { setError("结果已核对，但本机记录尚未结清；原请求已保留。"); }
    refresh();
  }, [observed.data, attempt?.key]);
  const mutation = useMutation({ mutationFn: async (original: ThreadPlanAttempt) => {
    store?.assertReadable(storageKey);
    const current = store?.read<unknown>(storageKey, null);
    if (current && (!validThreadPlanAttempt(current, threadID) || current.key !== original.key)) throw new Error("另一个计划操作尚待核对，请先查看原请求。");
    store?.write(storageKey, original);
    if (alive.current) { setSaved(original); setError(""); setResult(null); }
    return executeThreadPlan(client, original);
  }, onSuccess: (value, original) => {
    queryClient.setQueryData(["thread", threadID, "plan-request", original.key], value);
    if (settled(value)) {
      try { clear(original); } catch { if (alive.current) setError("结果已核对，但本机记录尚未结清；原请求已保留。"); }
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
    if (alive.current) setError(planErrorText(failure));
    void queryClient.invalidateQueries({ queryKey: ["thread", threadID, "plan-request"] });
    refresh();
  } });
  const phase = detail.data?.mode.phase;
  const proposal = detail.data?.plan_delivery?.proposal;
  const latest = choice?.proposalID === proposal?.id ? proposal?.directions.find((item) => item.ordinal === choice?.direction) : undefined;
  const ready = available && active && !working && !mutation.isPending && !attempt && !damaged && !detail.isError && !detail.isPending;
  const show = () => {
    setOpen(true);
    if (proposal?.directions.length === 1) setChoice({ proposalID: proposal.id, direction: proposal.directions[0].ordinal });
    else if (choice?.proposalID !== proposal?.id) setChoice(null);
    void detail.refetch();
  };
  const start = (body: ThreadPlanRequest) => {
    if (!ready) return;
    mutation.mutate({ threadID, key: `thread-plan-${crypto.randomUUID()}`, body });
  };
  const confirmContent = latest ? `请按已确认的计划“${latest.title}”继续执行，保留原目标、后续修正和限制。需要调整计划时先说明原因；执行沿用当前操作权限。` : "";
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
        <ListChecks size={14} aria-hidden="true" /><span className="v2-composer-optional-label">{attempt || damaged ? "计划操作待核对" : detail.isError ? "计划读取失败" : needsAttention ? "计划操作未完成" : "查看计划"}</span>
      </button>}
    </div>
    {open && createPortal(<div className="v2-plan-overlay" onMouseDown={(event) => { if (event.target === event.currentTarget) setOpen(false); }}>
      <section className="v2-thread-plan" ref={dialog} role="dialog" aria-modal="true" aria-labelledby="v2-plan-title">
        <header><h2 id="v2-plan-title">{phase === "plan" ? "确认计划" : "任务计划"}</h2>
          <button type="button" ref={closeButton} aria-label="关闭计划" onClick={() => setOpen(false)}><X size={18} /></button></header>
        <p className="v2-plan-help">先规划时先分析和形成方案。确认后在同一对话执行；后续修改要求仍可直接发消息。</p>
        {detail.isPending && <p role="status">正在读取计划…</p>}
        {detail.isError && <p role="alert">计划读取失败，请重新读取后确认方案。<button type="button" onClick={() => void detail.refetch()}>重新读取</button></p>}
        {damaged && <p role="alert">本机计划请求记录无法读取，已保留原记录。请先处理本机存储问题；普通对话仍可继续。</p>}
        {error && <p role="alert">{error}</p>}
        {attempt && <section className="v2-plan-request" aria-label="原计划请求">
          <strong>原计划操作待核对</strong>
          <p>{shownObservation ? observationText(shownObservation) : observed.isError ? "暂时无法确认原请求结果，原内容和请求标识已保留。" : "正在核对原请求…"}</p>
          {attempt.body.content && <blockquote>{attempt.body.content}</blockquote>}
          <div className="v2-plan-actions"><button type="button" disabled={observed.isFetching || mutation.isPending} onClick={() => void observed.refetch()}>核对原请求</button>
            {shownObservation && ["not_received", "prepared"].includes(shownObservation.state) && <button type="button" disabled={working || mutation.isPending || !available || attempt.body.run_id !== runID || (attempt.body.action === "confirm" && hasUnsentDraft)}
              onClick={() => mutation.mutate(attempt)}>继续原操作</button>}
            {shownObservation?.state === "not_received" && attempt.responseRejected && <button type="button" disabled={mutation.isPending} onClick={() => { try { clear(attempt); setResult(null); setError(""); } catch { setError("本机原请求暂时无法结清。"); } }}>关闭已拒绝请求</button>}
          </div>
        </section>}
        {!attempt && shownObservation && <p role="status">{observationText(shownObservation)}</p>}
        {!detail.isError && proposal && <div className="v2-plan-directions">
          {proposal.directions.map((direction) => <section key={`${proposal.id}:${direction.ordinal}`}>
            <label className="v2-plan-choice"><input type="radio" name="plan-direction" checked={choice?.proposalID === proposal.id && choice.direction === direction.ordinal}
              disabled={Boolean(attempt) || phase !== "plan"} onChange={() => setChoice({ proposalID: proposal.id, direction: direction.ordinal })} />
              <strong>{direction.title}</strong></label>
            <p>{direction.summary}</p>
            {direction.tradeoffs.length > 0 && <ul>{direction.tradeoffs.map((text, index) => <li key={index}>{text}</li>)}</ul>}
            <details><summary>查看步骤与验收标准（{direction.modules.length} 项）</summary>
              <ol>{direction.modules.map((module) => <li key={module.ordinal}><strong>{module.title}</strong><p>{module.objective}</p>
                <ul>{module.acceptance_criteria.map((criterion, index) => <li key={index}>{criterion}</li>)}</ul>
                {module.dependencies.length > 0 && <small>依赖步骤：{module.dependencies.join("、")}</small>}</li>)}</ol>
            </details>
          </section>)}
        </div>}
        {!detail.isPending && !detail.isError && !proposal && <p>{phase === "plan" ? "尚无可确认的计划。请在对话中提出需求或补充要求，等待计划形成。" : "当前采用默认处理方式。需要先讨论方案时，可开启输入框旁的“计划模式”。"}</p>}
        {working && <p className="v2-plan-help">Agent 仍在处理消息。可以直接在对话中补充要求，待本轮处理结束后确认最新计划。</p>}
        {phase === "plan" && proposal && <>
          {hasUnsentDraft && <p role="status">输入框中还有未发送内容。请先发送修改要求并等待计划更新，再确认执行。</p>}
          {!latest && <p className="v2-plan-help">请选择当前方案；计划更新后需要重新核对。</p>}
          {latest && <details><summary>确认时发送的消息</summary><p>{confirmContent}</p></details>}
          <div className="v2-plan-actions"><button type="button" onClick={() => { setOpen(false); onRequestChange(""); }}>返回对话修改要求</button>
            <button type="button" className="v2-plan-confirm" disabled={!ready || !latest || hasUnsentDraft} onClick={() => {
              if (!latest || !proposal || hasUnsentDraft) return;
              start({ version: "plan_delivery_control.v1", action: "confirm", run_id: runID, proposal_id: proposal.id,
                direction: latest.ordinal, manual_acceptance: "on_demand", content: confirmContent });
            }}>{mutation.isPending ? "正在确认…" : "确认计划并执行"}</button></div>
        </>}
      </section>
    </div>, document.body)}
  </>;
}
