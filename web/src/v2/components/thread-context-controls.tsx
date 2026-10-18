import { useRef, useState } from "react";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { MessageCircleQuestion, Pencil, Pin, X } from "lucide-react";
import { APIRequestError, type APIClient } from "../../api/client";
import { parseThreadCompaction, parseThreadPinResolution, readThreadContextPins,
  readThreadCompactionOperations, recoverThreadCompaction,
  type ThreadCompactionOperation, type ThreadCompactionResult,
  type ThreadContextPin, type ThreadContextPinChoice, type ThreadContextPinProposal,
  type ThreadContextPinResolution, type ThreadContextPins } from "../../api/thread-context-controls";
import { v2QueryKeys } from "../query-keys";
import "./thread-context-controls.css";

const pinsKey = (client: APIClient, threadID: string) => ["thread-context-pins", client.baseURL, threadID];
const compactionsKey = (client: APIClient, threadID: string) => ["thread-context-compactions", client.baseURL, threadID];
const unfinishedCompaction = (operation: ThreadCompactionOperation) => ["prepared", "started", "generated"].includes(operation.status);
const compactionNotice = (result: ThreadCompactionResult) => result.status === "noop" ? "当前没有需要压缩的较早消息。"
  : `已压缩 ${result.removed_messages} 条较早消息，保留 ${result.preserved_messages} 条近期消息。` +
    (result.input_messages_omitted ? ` 为适配模型窗口，生成摘要时省略了最早的 ${result.input_messages_omitted} 条记录；这些原文仍可回读。` : "");
function usePins(client: APIClient, threadID: string, poll = false) {
  return useQuery({ queryKey: pinsKey(client, threadID), retry: false, refetchOnMount: "always",
    refetchInterval: poll ? 3000 : false,
    queryFn: ({ signal }) => readThreadContextPins(client, threadID, signal) });
}
const message = (error: unknown) => error instanceof Error ? error.message : "操作失败，请重试。";

// Reuse a request identity when retrying the same intent after a lost response.
function useControlIntent(prefix: string) {
  const intent = useRef<{ identity: string; body: unknown; key: string } | null>(null);
  return {
    prepare(body: Record<string, unknown>) {
      const encoded = JSON.stringify({ ...body, expected_revision: undefined, expected_pin_version: undefined, expected_proposal_version: undefined });
      if (intent.current?.identity !== encoded) intent.current = { identity: encoded, body, key: `${prefix}-${crypto.randomUUID()}` };
      return intent.current;
    },
    clear() { intent.current = null; },
  };
}

export function V2ThreadPinsPanel({ client, threadID, onReply }: {
  client: APIClient; threadID: string; onReply?: (content: string, operationKey: string) => Promise<void>;
}) {
  const queryClient = useQueryClient();
  const pins = usePins(client, threadID);
  const [draft, setDraft] = useState("");
  const [editing, setEditing] = useState<ThreadContextPin | null>(null);
  const [reviewEdit, setReviewEdit] = useState(false);
  const [pending, setPending] = useState(false);
  const [error, setError] = useState("");
  const intent = useControlIntent("thread-context-pin");
  const state = pins.isError ? undefined : pins.data;
  const blocked = !state || state.busy || pending || !client.hasControl;
  const change = async (action: "add" | "edit" | "remove", pin?: ThreadContextPin) => {
    if (!state || blocked) return;
    const body = { version: "thread_context_pins.v1", action, expected_revision: state.revision,
      ...(pin ? { pin_id: pin.id, expected_pin_version: pin.version } : {}),
      ...(action === "remove" ? {} : { text: draft.trim() }) };
    setPending(true); setError("");
    try {
      const request = intent.prepare(body);
      const result = await client.postControl<unknown>(`/threads/${encodeURIComponent(threadID)}/context/pins`, request.body, request.key);
      queryClient.setQueryData(pinsKey(client, threadID), parseThreadPinResolution(result, threadID).state);
      intent.clear(); setDraft(""); setEditing(null);
    } catch (failure) {
      setError(message(failure));
      if (failure instanceof APIRequestError && failure.status === 409) {
        intent.clear(); setReviewEdit(action === "edit");
        await pins.refetch();
      }
    }
    finally { setPending(false); }
  };
  return <section className="v2-context-pins" aria-labelledby="thread-context-pins-title">
    <h3 id="thread-context-pins-title"><Pin size={16} aria-hidden="true" />本次任务始终保留</h3>
    <p className="v2-context-help">固定的原文会在压缩后继续提供给 Agent，仅用于当前任务。可随时编辑或取消固定。</p>
    {pins.isError ? <p role="alert">{message(pins.error)} <button type="button" onClick={() => void pins.refetch()}>重新读取固定要求</button></p>
      : !state ? <p role="status">正在读取固定要求…</p> : <>
        {!state.pins.length ? <p>还没有固定要求。</p> : <ul>{state.pins.map((pin) => <li key={pin.id}>
          <p className="v2-context-pin-text">{pin.text}</p>
          <div><button type="button" disabled={blocked} onClick={() => { setEditing(pin); if (!reviewEdit) setDraft(pin.text); setReviewEdit(false); setError(""); }}>编辑</button>
            <button type="button" disabled={blocked} onClick={() => void change("remove", pin)}>取消固定</button></div>
        </li>)}</ul>}
        {state.busy && <p role="status">任务正在执行，结束后可更改固定要求。</p>}
      </>}
    <label htmlFor="thread-context-pin-draft">{editing ? "编辑固定要求" : "添加固定要求"}</label>
    <textarea id="thread-context-pin-draft" rows={3} value={draft} disabled={pending}
      onChange={(event) => setDraft(event.target.value)} placeholder="例如：本次任务不得修改公共 API；验证失败必须如实报告。" />
    {reviewEdit && <p role="status">固定要求已有变化。草稿已保留，请核对上方最新内容并重新选择“编辑”。</p>}
    <div className="v2-context-control-actions"><button type="button" disabled={blocked || reviewEdit || !draft.trim()}
      onClick={() => void change(editing ? "edit" : "add", editing ?? undefined)}>{pending ? "正在保存…" : editing ? "保存修改" : "固定到本次任务"}</button>
      {editing && <button type="button" disabled={pending} onClick={() => { setEditing(null); setDraft(""); setReviewEdit(false); }}>取消编辑</button>}
    </div>
    {error && <p role="alert">{error}</p>}
    {onReply && state?.proposals.some((item) => item.reply_text && item.reply_operation_key) &&
      <details><summary>已确认的问题与答复</summary>{state.proposals.filter((item) => item.reply_text && item.reply_operation_key).map((item) =>
        <ResolvedPinReply key={item.id} proposal={item} disabled={blocked} onReply={onReply} />)}</details>}
  </section>;
}

function ResolvedPinReply({ proposal, disabled, onReply }: { proposal: ThreadContextPinProposal; disabled: boolean;
  onReply: (content: string, operationKey: string) => Promise<void> }) {
  const [pending, setPending] = useState(false); const [notice, setNotice] = useState("");
  return <div className="v2-context-resolved-reply"><p>{proposal.reply_text}</p><button type="button" disabled={disabled || pending}
    onClick={() => { setPending(true); setNotice(""); void onReply(proposal.reply_text!, proposal.reply_operation_key!).then(() => setNotice("答复已发送。"))
      .catch((error) => setNotice(message(error))).finally(() => setPending(false)); }}>发送或核对这条答复</button>
    {notice && <p role="status">{notice}</p>}</div>;
}

export function V2ThreadCompactControl({ client, threadID, runID }: { client: APIClient; threadID: string; runID: string }) {
  const queryClient = useQueryClient(); const pins = usePins(client, threadID);
  const [focus, setFocus] = useState(""); const [pending, setPending] = useState(false);
  const compactAttempt = useRef(0);
  const operations = useQuery({ queryKey: compactionsKey(client, threadID), retry: false, refetchOnMount: "always",
    queryFn: ({ signal }) => readThreadCompactionOperations(client, threadID, signal),
    refetchInterval: (query) => pending || query.state.data?.operations.some(unfinishedCompaction) ? 3000 : false });
  const [recovering, setRecovering] = useState<string | null>(null);
  const [notice, setNotice] = useState(""); const [error, setError] = useState("");
  const intent = useControlIntent("thread-context-compact");
  const records = operations.isError ? undefined : operations.data?.operations;
  const hasUnfinished = Boolean(records?.some(unfinishedCompaction));
  const controlsPending = Boolean(recovering) || !client.hasControl;
  const blocked = pending || controlsPending || pins.isError || !pins.data || pins.data.busy;
  const refreshContext = () => Promise.all([
    queryClient.invalidateQueries({ queryKey: ["thread-context", client.baseURL, threadID] }),
    queryClient.invalidateQueries({ queryKey: pinsKey(client, threadID) }),
    queryClient.invalidateQueries({ queryKey: v2QueryKeys.thread(threadID) }),
    queryClient.invalidateQueries({ queryKey: compactionsKey(client, threadID) }),
  ]);
  const compact = async () => {
    if (blocked || hasUnfinished || !records) return;
    const body = { version: "thread_context_compaction.v1", expected_run_id: runID, focus: focus.trim() };
    const attempt = ++compactAttempt.current;
    setPending(true); setNotice(""); setError("");
    try {
      const request = intent.prepare(body);
      const result = parseThreadCompaction(await client.postControl<unknown>(`/threads/${encodeURIComponent(threadID)}/context/compact`, request.body, request.key), threadID, runID);
      if (attempt !== compactAttempt.current) return;
      intent.clear();
      setNotice(compactionNotice(result));
      await refreshContext();
    } catch (failure) {
      if (attempt !== compactAttempt.current) return;
      setError(message(failure));
      // Source changes end this intent; pending/busy conflicts and unknown responses retain it.
      if (failure instanceof APIRequestError && failure.status === 409 && failure.message.startsWith("thread_context_source_changed:")) intent.clear();
      await Promise.all([pins.refetch(), operations.refetch()]);
    }
    finally { if (attempt === compactAttempt.current) setPending(false); }
  };
  const recover = async (operation: ThreadCompactionOperation, action: "resume" | "discard") => {
    if (controlsPending || (action === "resume" && blocked)) return;
    setRecovering(operation.operation_id); setNotice(""); setError("");
    try {
      const result = await recoverThreadCompaction(client, threadID, operation, action);
      if (action === "discard" && !unfinishedCompaction(result)) { compactAttempt.current++; setPending(false); }
      intent.clear();
      setNotice(result.result ? compactionNotice(result.result) : action === "discard" && result.status === "failed" ? "已停止采用本次结果。" : "压缩记录已核对。");
      await refreshContext();
    } catch (failure) {
      setError(message(failure));
      await Promise.all([pins.refetch(), operations.refetch()]);
    } finally { setRecovering(null); }
  };
  return <div className="v2-context-compact-control">
    <label htmlFor="thread-context-compact-focus">压缩重点（可选）</label>
    <input id="thread-context-compact-focus" value={focus} disabled={pending} onChange={(event) => setFocus(event.target.value)}
      placeholder="例如：保留失败原因与尚未完成的工作" />
    <p className="v2-context-help">空闲时压缩当前对话。固定要求与近期用户原文单独保留，历史原文仍可读取。</p>
    <button type="button" disabled={blocked || hasUnfinished || !records}
      onClick={() => void compact()}>{pending ? "正在压缩…" : "压缩当前对话"}</button>
    {pins.data?.busy && <p role="status">任务正在执行，结束后可以压缩。</p>}
    {notice && <p role="status">{notice}</p>}{error && <p role="alert">{error}</p>}
    <details className="v2-context-compactions" open={hasUnfinished || operations.isError || undefined}>
      <summary>压缩记录{hasUnfinished ? " · 有未完成操作" : records?.length ? `（${records.length}）` : ""}</summary>
      <div className="v2-context-compactions-toolbar"><span>最近的压缩操作</span>
        <button type="button" disabled={operations.isFetching || Boolean(recovering)}
          onClick={() => void operations.refetch()}>{operations.isFetching ? "正在核对…" : "刷新核对记录"}</button></div>
      {operations.isError ? <p role="alert">{message(operations.error)} 请刷新核对记录后再压缩。</p>
        : !records ? <p role="status">正在读取压缩记录…</p>
        : !records.length ? <p className="v2-context-help">还没有压缩记录。</p>
        : <ul className="v2-context-compaction-list">{records.map((operation) => <CompactionOperationRow key={operation.operation_id}
          operation={operation} disabled={controlsPending} resumeDisabled={blocked} recovering={recovering === operation.operation_id}
          onRecover={(action) => void recover(operation, action)} />)}</ul>}
    </details>
  </div>;
}

const compactionStatus = { prepared: "准备压缩", started: "正在生成摘要", generated: "摘要已生成，待采用",
  completed: "已完成", noop: "无需压缩", failed: "已结束，未采用摘要" };
function CompactionOperationRow({ operation, disabled, resumeDisabled, recovering, onRecover }: {
  operation: ThreadCompactionOperation; disabled: boolean; resumeDisabled: boolean; recovering: boolean; onRecover: (action: "resume" | "discard") => void;
}) {
  const unfinished = unfinishedCompaction(operation);
  return <li className="v2-context-compaction-record">
    <div className="v2-context-compaction-heading"><strong>{compactionStatus[operation.status]}</strong>
      <time dateTime={operation.created_at}>{new Date(operation.created_at).toLocaleString("zh-CN", { hour12: false })}</time></div>
    <p className="v2-context-compaction-meta">{operation.model ? `${operation.provider ? `${operation.provider} / ` : ""}${operation.model}` : "压缩操作"}
      <span title={operation.operation_id}>操作 {operation.operation_id}</span></p>
    {operation.status === "prepared" && <p>请求已准备，可刷新记录核对进度。</p>}
    {operation.status === "started" && <p>模型请求已发出，可能仍在运行。可刷新记录核对进度。</p>}
    {operation.status === "generated" && <p>恢复会校验对话版本并采用已保存的摘要，无需再次请求模型。</p>}
    {operation.result && <p>{compactionNotice(operation.result)}</p>}
    {operation.error_message && <p className="v2-context-compaction-error">{operation.error_message}</p>}
    {!operation.usage_known && <p className="v2-context-help">本次用量尚未确认。</p>}
    {unfinished && <>
      {operation.status === "started" && <p className="v2-context-help">放弃将停止采用本次结果；已发出的模型请求仍可能计费。</p>}
      <div className="v2-context-control-actions">
        {operation.status === "generated" && <button type="button" disabled={resumeDisabled} onClick={() => onRecover("resume")}>{recovering ? "正在核对…" : "恢复已生成摘要"}</button>}
        <button type="button" disabled={disabled} onClick={() => onRecover("discard")}>{recovering ? "正在处理…" : "放弃本次压缩"}</button>
      </div>
    </>}
  </li>;
}

export function V2ThreadPinPrompt({ client, threadID, paused, onReply, onClose }: {
  client: APIClient; threadID: string; paused: boolean;
  onReply: (content: string, operationKey: string) => Promise<void>;
  onClose?: () => void;
}) {
  const pins = usePins(client, threadID, true);
  const [hidden, setHidden] = useState<string[]>([]);
  const proposal = pins.data?.proposals.find((item) => item.status === "pending" && !hidden.includes(item.id));
  // Keep an answer mounted through its own result/refetch and reply.
  const [opened, setOpened] = useState<ThreadContextPinProposal | null>(null);
  const active = opened ?? (!paused && !pins.isError && !pins.data?.busy ? proposal : undefined);
  if (!active || !pins.data || !client.hasControl) return null;
  return <PinPromptCard key={active.id} client={client} threadID={threadID} state={pins.data} proposal={active}
    onInteraction={() => setOpened(active)} onClose={() => { setOpened(null); setHidden((items) => [...items, active.id]); onClose?.(); }} onReply={onReply} />;
}

function PinPromptCard({ client, threadID, state, proposal, onReply, onInteraction, onClose }: {
  client: APIClient; threadID: string; state: ThreadContextPins; proposal: ThreadContextPinProposal;
  onReply: (content: string, operationKey: string) => Promise<void>; onInteraction: () => void; onClose: () => void;
}) {
  const queryClient = useQueryClient(); const [choice, setChoice] = useState<ThreadContextPinChoice>("pin_task");
  const [custom, setCustom] = useState(""); const [pinCustom, setPinCustom] = useState(false);
  const [pending, setPending] = useState(false); const [error, setError] = useState("");
  const [resolved, setResolved] = useState<ThreadContextPinResolution | null>(null);
  const intent = useControlIntent("thread-context-pin-answer");
  const resolve = async (answer: ThreadContextPinChoice) => {
    if (pending) return;
    onInteraction(); setPending(true); setError("");
    try {
      let result = resolved;
      if (!result) {
        const body = { version: "thread_context_pins.v1", expected_revision: state.revision,
          expected_proposal_version: proposal.version, choice: answer,
          ...(answer === "custom" ? { text: custom.trim(), pin_custom: pinCustom } : {}) };
        const request = intent.prepare(body);
        result = parseThreadPinResolution(await client.postControl<unknown>(
          `/threads/${encodeURIComponent(threadID)}/context/pin-proposals/${encodeURIComponent(proposal.id)}/resolve`, request.body, request.key), threadID);
        setResolved(result); queryClient.setQueryData(pinsKey(client, threadID), result.state);
      }
      if (result.reply_text && result.reply_operation_key) await onReply(result.reply_text, result.reply_operation_key);
      intent.clear(); onClose();
    } catch (failure) {
      setError(message(failure));
      if (answer === "dismiss") { onClose(); return; }
      if (!resolved && failure instanceof APIRequestError && failure.status === 409) {
        intent.clear();
        try {
          const latest = await readThreadContextPins(client, threadID);
          queryClient.setQueryData(pinsKey(client, threadID), latest);
          if (latest.proposals.find((item) => item.id === proposal.id)?.status !== "pending") onClose();
        } catch { /* Preserve the original conflict and the user's answer until a successful read. */ }
      }
    }
    finally { setPending(false); }
  };
  const close = () => { if (resolved) onClose(); else void resolve("dismiss"); };
  const choose = (answer: ThreadContextPinChoice) => { onInteraction(); setChoice(answer); };
  return <section className="v2-context-pin-card" aria-labelledby="thread-pin-question">
    <header><MessageCircleQuestion size={18} aria-hidden="true" /><span>问题</span>
      <button type="button" aria-label="关闭固定要求提问" disabled={pending} onClick={close}><X size={18} /></button></header>
    <div className="v2-context-pin-body">
    <h2 id="thread-pin-question">{proposal.question || "是否让这段要求在本次任务压缩后继续保留？"}</h2><blockquote>{proposal.text}</blockquote>
    <fieldset disabled={pending || Boolean(resolved)} aria-label="选择处理方式">
      <label className="v2-context-pin-option"><input type="radio" name="pin-choice" value="pin_task"
        checked={choice === "pin_task"} onChange={() => choose("pin_task")} />
        <span className="v2-context-pin-number">1</span><span><strong>本次任务始终保留</strong></span></label>
      <label className="v2-context-pin-option"><input type="radio" name="pin-choice" value="discussion_only"
        checked={choice === "discussion_only"} onChange={() => choose("discussion_only")} />
        <span className="v2-context-pin-number">2</span><span><strong>仅当前讨论</strong></span></label>
    </fieldset>
    {resolved && error && <p>选择已保存，答复尚待核对。重试会沿用同一条答复。</p>}
    {error && <p role="alert">{error}</p>}
    </div>
    <footer>
      <div className={`v2-context-pin-custom${choice === "custom" ? " is-active" : ""}`}>
        <label className="v2-context-pin-reply"><Pencil size={18} aria-hidden="true" />
          <textarea aria-label="你的回复" rows={1} placeholder="或自行撰写回复" value={custom} disabled={pending || Boolean(resolved)}
            onPointerDown={() => { if (!pending && !resolved) choose("custom"); }}
            onChange={(event) => { choose("custom"); setCustom(event.target.value); }} /></label>
        {choice === "custom" && <label className="v2-context-pin-custom-toggle"><input type="checkbox" checked={pinCustom}
          disabled={pending || Boolean(resolved)} onChange={(event) => setPinCustom(event.target.checked)} />将这段回复固定到本次任务</label>}
      </div>
      <div className="v2-context-pin-actions"><button type="button" disabled={pending} onClick={close}>跳过</button>
      <button type="button" className="primary" disabled={pending || (!resolved && choice === "custom" && !custom.trim())}
        onClick={() => void resolve(choice)}>{pending ? "正在提交…" : resolved ? "重试发送答复" : "发送"}</button></div></footer>
  </section>;
}
