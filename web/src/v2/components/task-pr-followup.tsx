import { useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { APIRequestError, type APIClient } from "../../api/client";
import { createPRFollowup, PR_FOLLOWUP_VERSION, prFollowupTerminal, readPRFollowups, transitionPRFollowup, validPRFollowupCreateRequest,
  type PRFollowup, type PRFollowupAction, type PRFollowupCreateRequest, type PRFollowupList, type PRFollowupStatus } from "../../api/pr-followup";
import { useV2PersistentState, useV2RecoveryStore, type V2RecoveryStore } from "../recovery-storage";
import "./task-pr-followup.css";

interface CreateAttempt { version: "pr-followup-intent.v1"; threadID: string; key: string; body: PRFollowupCreateRequest }
const storageKey = (threadID: string) => `thread:${threadID}:pr-followup-create`;
const queryKey = (threadID: string) => ["thread", threadID, "pr-followups"] as const;
const labels: Record<PRFollowupStatus, string> = { active: "持续跟进中", paused: "已暂停", blocked: "等待处理", ready: "待复核", completed: "已完成", cancelled: "已停止", exhausted: "已结束（期限或次数）" };
const time = (value?: string) => value ? new Date(value).toLocaleString() : "—";
function validAttempt(value: unknown, threadID: string): value is CreateAttempt {
  if (!value || typeof value !== "object" || Array.isArray(value)) return false;
  const item = value as Partial<CreateAttempt>;
  return Object.keys(item).length === 4 && item.version === "pr-followup-intent.v1" && item.threadID === threadID &&
    typeof item.key === "string" && /^task-pr-followup-[\w-]{1,128}$/u.test(item.key) && validPRFollowupCreateRequest(item.body);
}

type Props = { client: APIClient; threadID: string; connectionID: string; pullRequest: number; canStart: boolean };
export function TaskPRFollowup(props: Props) { return <PRFollowupWorkspace key={props.threadID} {...props} />; }

function PRFollowupWorkspace({ client, threadID, connectionID, pullRequest, canStart }: Props) {
  const queries = useQueryClient(), store = useV2RecoveryStore();
  const [saved, setSaved] = useV2PersistentState<unknown>(storageKey(threadID), null);
  const attempt = validAttempt(saved, threadID) ? saved : null;
  const [minutes, setMinutes] = useState("5"), [hours, setHours] = useState("24"), [turns, setTurns] = useState("3");
  const [includeExisting, setIncludeExisting] = useState(false), [localError, setLocalError] = useState("");
  const list = useQuery({ queryKey: queryKey(threadID), queryFn: ({ signal }) => readPRFollowups(client, threadID, signal),
    enabled: client.hasGitHubReviewControl, retry: false, refetchOnMount: "always",
    refetchInterval: (query) => query.state.data?.items.some((item) => !prFollowupTerminal(item)) ? 15_000 : false });
  const unfinished = list.data?.items.find((item) => !prFollowupTerminal(item));
  const current = unfinished ?? list.data?.items[0];
  function updateRecord(value: PRFollowup) {
    queries.setQueryData<PRFollowupList>(queryKey(value.thread_id), (previous) => ({ version: PR_FOLLOWUP_VERSION,
      thread_id: value.thread_id, worker_state: previous?.worker_state ?? "starting", items: [value, ...(previous?.items.filter((item) => item.id !== value.id) ?? [])] }));
  }
  const create = useMutation({ mutationFn: ({ intent, scope }: { intent: CreateAttempt; scope: V2RecoveryStore }) => {
    scope.assertReadable(storageKey(intent.threadID));
    const original = scope.read<unknown>(storageKey(intent.threadID), null);
    if (!validAttempt(original, intent.threadID) || original.key !== intent.key || JSON.stringify(original.body) !== JSON.stringify(intent.body)) throw new Error("原创建请求已变化，请重新核对。");
    return createPRFollowup(client, intent.threadID, intent.body, intent.key);
  }, onSuccess: (value, { intent, scope }) => {
    updateRecord(value);
    const original = scope.read<unknown>(storageKey(intent.threadID), null);
    if (validAttempt(original, intent.threadID) && original.key === intent.key) scope.write(storageKey(intent.threadID), null);
  }, onSettled: (_value, _error, { intent }) => { void queries.invalidateQueries({ queryKey: queryKey(intent.threadID) }); } });
  const transition = useMutation({ mutationFn: ({ value, action }: { value: PRFollowup; action: PRFollowupAction }) => transitionPRFollowup(client, value, action),
    onSuccess: updateRecord, onSettled: (_value, _error, { value }) => { void queries.invalidateQueries({ queryKey: queryKey(value.thread_id) }); } });
  const pending = create.isPending || transition.isPending;
  const valuesValid = Number.isInteger(Number(minutes)) && Number(minutes) >= 1 && Number(minutes) <= 60 &&
    Number.isFinite(Number(hours)) && Number(hours) > 0 && Number(hours) <= 168 && Number.isInteger(Number(turns)) && Number(turns) >= 1 && Number(turns) <= 20;
  const canCreate = canStart && Boolean(store && connectionID && pullRequest > 0) && list.isSuccess && !list.isFetching && !unfinished && !saved && !pending && valuesValid;
  const rejected = create.error instanceof APIRequestError && [400, 401, 403, 404, 409, 412, 422].includes(create.error.status) && create.variables?.intent.key === attempt?.key;
  function begin() {
    if (!canCreate || !store) return;
    try {
      store.assertReadable(storageKey(threadID));
      if (store.read(storageKey(threadID), null)) throw new Error("已有待核对的持续跟进请求。");
      const intent: CreateAttempt = { version: "pr-followup-intent.v1", threadID, key: `task-pr-followup-${crypto.randomUUID()}`,
        body: { version: PR_FOLLOWUP_VERSION, connection_id: connectionID, pull_request: pullRequest, interval_seconds: Number(minutes) * 60,
          deadline_at: new Date(Date.now() + Number(hours) * 3_600_000).toISOString(), max_turns: Number(turns), include_existing_comments: includeExisting } };
      store.write(storageKey(threadID), intent); setSaved(intent); setLocalError("");
      create.mutate({ intent, scope: store });
    } catch (error) { setLocalError(error instanceof Error ? error.message : "无法保存持续跟进请求。"); }
  }
  function resetRejected() {
    if (!rejected || !attempt || !store) return;
    try {
      const original = store.read<unknown>(storageKey(threadID), null);
      if (!validAttempt(original, threadID) || original.key !== attempt.key) throw new Error("原创建请求已变化，请刷新核对。");
      store.write(storageKey(threadID), null); setSaved(null); create.reset(); setLocalError("");
    } catch (error) { setLocalError(error instanceof Error ? error.message : "无法更新原请求。"); }
  }

  return <section className="v2-pr-followup" aria-label="持续跟进 PR">
    <header><h3>持续跟进 PR</h3><button type="button" disabled={list.isFetching || pending} onClick={() => void list.refetch()}>刷新跟进状态</button></header>
    <p>读取新审阅、行内和普通评论及 CI 失败，按本任务当前的 Full / Ask / Auto 权限与预算继续修复和测试；遇到审批时等待你处理。应用运行时检查，关闭后暂停。</p>
    {list.data && list.data.worker_state !== "running" && <p role="status" className="v2-pr-followup-muted">跟进调度当前未运行，启用后的检查将在应用调度恢复后继续。</p>}
    {list.isLoading && <p role="status">正在读取持续跟进状态…</p>}
    {list.isError && <p role="alert">{list.error.message}</p>}
    {current && <div className="v2-pr-followup-record">
      <header><strong>{current.repository} #{current.pull_request}</strong><span>{labels[current.status]}</span></header>
      <dl><div><dt>修复轮次</dt><dd>{current.turns_started} / {current.max_turns}</dd></div>
        <div><dt>下次检查</dt><dd>{current.status === "active" ? time(current.next_check_at) : "—"}</dd></div>
        <div><dt>截止时间</dt><dd>{time(current.deadline_at)}</dd></div></dl>
      {current.last_result && <p>{current.last_result}</p>}{current.last_error && <p role="alert">{current.last_error}</p>}
      {current.status === "blocked" && <p>处理当前任务的审批或阻塞后，可继续跟进。</p>}
      {!prFollowupTerminal(current) && <div className="v2-pr-followup-actions">
        {current.status === "active" ? <button type="button" disabled={pending || list.isFetching} onClick={() => transition.mutate({ value: current, action: "pause" })}>暂停跟进</button>
          : <button type="button" disabled={pending || list.isFetching} onClick={() => transition.mutate({ value: current, action: "resume" })}>继续跟进</button>}
        <button type="button" disabled={pending || list.isFetching} onClick={() => transition.mutate({ value: current, action: "cancel" })}>停止跟进</button>
      </div>}
    </div>}
    {saved !== null && !attempt && <p role="alert">本机持续跟进请求格式异常，原记录已保留，请先核对。</p>}
    {attempt && <div className="v2-pr-followup-recovery"><p>{create.isPending ? "正在确认持续跟进请求…" : "原创建请求尚未确认，重试会核对同一份设置。"}</p>
      <p>PR #{attempt.body.pull_request} · 每 {attempt.body.interval_seconds / 60} 分钟 · 最多 {attempt.body.max_turns} 轮 · 截止 {time(attempt.body.deadline_at)}</p>
      <div className="v2-pr-followup-actions"><button type="button" disabled={!store || pending} onClick={() => { if (store) create.mutate({ intent: attempt, scope: store }); }}>核对原创建请求</button>
        {rejected && <button type="button" disabled={pending} onClick={resetRejected}>重新设置</button>}</div>
    </div>}
    {create.isError && <p role="alert">{create.error.message}</p>}{transition.isError && <p role="alert">{transition.error.message}</p>}{localError && <p role="alert">{localError}</p>}
    {!unfinished && saved === null && <details className="v2-pr-followup-setup"><summary>{current ? "重新开始持续跟进" : "设置持续跟进"}</summary>
      <form onSubmit={(event) => { event.preventDefault(); begin(); }}>
        <div className="v2-pr-followup-fields"><label>检查间隔（分钟）<input required type="number" min={1} max={60} step={1} value={minutes} onChange={(event) => setMinutes(event.target.value)} disabled={pending} /></label>
          <label>持续时间（小时）<input required type="number" min={1} max={168} step={1} value={hours} onChange={(event) => setHours(event.target.value)} disabled={pending} /></label>
          <label>最多修复轮次<input required type="number" min={1} max={20} step={1} value={turns} onChange={(event) => setTurns(event.target.value)} disabled={pending} /></label></div>
        <label className="v2-pr-followup-checkbox"><input type="checkbox" checked={includeExisting} onChange={(event) => setIncludeExisting(event.target.checked)} disabled={pending} />同时处理已有评论</label>
        <p className="v2-pr-followup-muted">当前 CI 失败始终纳入；未勾选时从新评论开始跟进。</p>
        <button type="submit" disabled={!canCreate}>启用持续跟进</button>
        {!canStart && <p className="v2-pr-followup-muted">选择一个开放的 PR 后，可启用持续跟进。</p>}
        {!store && <p role="alert">当前连接未提供本机恢复身份，无法保存创建请求。</p>}
      </form>
    </details>}
  </section>;
}
