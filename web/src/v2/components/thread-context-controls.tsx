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
import { useLocale, type LocaleText } from "../../lib/locale";
import "./thread-context-controls.css";

const pinsKey = (client: APIClient, threadID: string) => ["thread-context-pins", client.baseURL, threadID];
const compactionsKey = (client: APIClient, threadID: string) => ["thread-context-compactions", client.baseURL, threadID];
const unfinishedCompaction = (operation: ThreadCompactionOperation) => ["prepared", "started", "generated"].includes(operation.status);
const compactionNotice = (result: ThreadCompactionResult, t: LocaleText) => result.status === "noop" ? t("当前没有需要压缩的较早消息。", "There are no earlier messages to compact.")
  : t(`已压缩 ${result.removed_messages} 条较早消息，保留 ${result.preserved_messages} 条近期消息。`, `Compacted ${result.removed_messages} earlier messages and kept ${result.preserved_messages} recent messages.`) +
    (result.input_messages_omitted ? t(` 为适配模型窗口，生成摘要时省略了最早的 ${result.input_messages_omitted} 条记录；这些原文仍可回读。`, ` The earliest ${result.input_messages_omitted} records were omitted from summary generation to fit the model window; their original text remains readable.`) : "");
function usePins(client: APIClient, threadID: string, poll = false) {
  return useQuery({ queryKey: pinsKey(client, threadID), retry: false, refetchOnMount: "always",
    refetchInterval: poll ? 3000 : false,
    queryFn: ({ signal }) => readThreadContextPins(client, threadID, signal) });
}
const message = (error: unknown, t: LocaleText) => error instanceof Error ? error.message : t("操作失败，请重试。", "The operation failed. Try again.");

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
  const { t } = useLocale();
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
      setError(message(failure, t));
      if (failure instanceof APIRequestError && failure.status === 409) {
        intent.clear(); setReviewEdit(action === "edit");
        await pins.refetch();
      }
    }
    finally { setPending(false); }
  };
  return <section className="v2-context-pins" aria-labelledby="thread-context-pins-title">
    <h3 id="thread-context-pins-title"><Pin size={16} aria-hidden="true" />{t("本次任务始终保留", "Keep for this task")}</h3>
    <p className="v2-context-help">{t("固定的原文会在压缩后继续提供给 Agent，仅用于当前任务。可随时编辑或取消固定。", "Pinned text stays available to the agent after compaction for this task. You can edit or unpin it at any time.")}</p>
    {pins.isError ? <p role="alert">{message(pins.error, t)} <button type="button" onClick={() => void pins.refetch()}>{t("重新读取固定要求", "Reload pinned requirements")}</button></p>
      : !state ? <p role="status">{t("正在读取固定要求…", "Loading pinned requirements…")}</p> : <>
        {!state.pins.length ? <p>{t("还没有固定要求。", "No pinned requirements yet.")}</p> : <ul>{state.pins.map((pin) => <li key={pin.id}>
          <p className="v2-context-pin-text">{pin.text}</p>
          <div><button type="button" disabled={blocked} onClick={() => { setEditing(pin); if (!reviewEdit) setDraft(pin.text); setReviewEdit(false); setError(""); }}>{t("编辑", "Edit")}</button>
            <button type="button" disabled={blocked} onClick={() => void change("remove", pin)}>{t("取消固定", "Unpin")}</button></div>
        </li>)}</ul>}
        {state.busy && <p role="status">{t("任务正在执行，结束后可更改固定要求。", "The task is running. Edit pinned requirements when it finishes.")}</p>}
      </>}
    <label htmlFor="thread-context-pin-draft">{editing ? t("编辑固定要求", "Edit pinned requirement") : t("添加固定要求", "Add pinned requirement")}</label>
    <textarea id="thread-context-pin-draft" rows={3} value={draft} disabled={pending}
      onChange={(event) => setDraft(event.target.value)} placeholder={t("例如：本次任务不得修改公共 API；验证失败必须如实报告。", "For example: keep the public API unchanged and report any failed checks.")} />
    {reviewEdit && <p role="status">{t("固定要求已有变化。草稿已保留，请核对上方最新内容并重新选择“编辑”。", "Pinned requirements have changed. Your draft is kept. Review the latest text above and select Edit again.")}</p>}
    <div className="v2-context-control-actions"><button type="button" disabled={blocked || reviewEdit || !draft.trim()}
      onClick={() => void change(editing ? "edit" : "add", editing ?? undefined)}>{pending ? t("正在保存…", "Saving…") : editing ? t("保存修改", "Save changes") : t("固定到本次任务", "Pin to this task")}</button>
      {editing && <button type="button" disabled={pending} onClick={() => { setEditing(null); setDraft(""); setReviewEdit(false); }}>{t("取消编辑", "Cancel editing")}</button>}
    </div>
    {error && <p role="alert">{error}</p>}
    {onReply && state?.proposals.some((item) => item.reply_text && item.reply_operation_key) &&
      <details><summary>{t("已确认的问题与答复", "Confirmed questions and replies")}</summary>{state.proposals.filter((item) => item.reply_text && item.reply_operation_key).map((item) =>
        <ResolvedPinReply key={item.id} proposal={item} disabled={blocked} onReply={onReply} />)}</details>}
  </section>;
}

function ResolvedPinReply({ proposal, disabled, onReply }: { proposal: ThreadContextPinProposal; disabled: boolean;
  onReply: (content: string, operationKey: string) => Promise<void> }) {
  const { t } = useLocale();
  const [pending, setPending] = useState(false); const [notice, setNotice] = useState("");
  return <div className="v2-context-resolved-reply"><p>{proposal.reply_text}</p><button type="button" disabled={disabled || pending}
    onClick={() => { setPending(true); setNotice(""); void onReply(proposal.reply_text!, proposal.reply_operation_key!).then(() => setNotice(t("答复已发送。", "Reply sent.")))
      .catch((error) => setNotice(message(error, t))).finally(() => setPending(false)); }}>{t("发送或核对这条答复", "Send or check this reply")}</button>
    {notice && <p role="status">{notice}</p>}</div>;
}

export function V2ThreadCompactControl({ client, threadID, runID }: { client: APIClient; threadID: string; runID: string }) {
  const { t } = useLocale();
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
      setNotice(compactionNotice(result, t));
      await refreshContext();
    } catch (failure) {
      if (attempt !== compactAttempt.current) return;
      setError(message(failure, t));
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
      setNotice(result.result ? compactionNotice(result.result, t) : action === "discard" && result.status === "failed" ? t("已停止采用本次结果。", "This result will no longer be applied.") : t("压缩记录已核对。", "Compaction records checked."));
      await refreshContext();
    } catch (failure) {
      setError(message(failure, t));
      await Promise.all([pins.refetch(), operations.refetch()]);
    } finally { setRecovering(null); }
  };
  return <div className="v2-context-compact-control">
    <label htmlFor="thread-context-compact-focus">{t("压缩重点（可选）", "Compaction focus (optional)")}</label>
    <input id="thread-context-compact-focus" value={focus} disabled={pending} onChange={(event) => setFocus(event.target.value)}
      placeholder={t("例如：保留失败原因与尚未完成的工作", "For example: preserve failure reasons and unfinished work")} />
    <p className="v2-context-help">{t("空闲时压缩当前对话。固定要求与近期用户原文单独保留，历史原文仍可读取。", "Compact this conversation when idle. Pinned requirements and recent user messages are kept separately; original history remains readable.")}</p>
    <button type="button" disabled={blocked || hasUnfinished || !records}
      onClick={() => void compact()}>{pending ? t("正在压缩…", "Compacting…") : t("压缩当前对话", "Compact conversation")}</button>
    {pins.data?.busy && <p role="status">{t("任务正在执行，结束后可以压缩。", "The task is running. Compact it when it finishes.")}</p>}
    {notice && <p role="status">{notice}</p>}{error && <p role="alert">{error}</p>}
    <details className="v2-context-compactions" open={hasUnfinished || operations.isError || undefined}>
      <summary>{t("压缩记录", "Compaction history")}{hasUnfinished ? t("· 有未完成操作", " · Unfinished operation") : records?.length ? `（${records.length}）` : ""}</summary>
      <div className="v2-context-compactions-toolbar"><span>{t("最近的压缩操作", "Recent compactions")}</span>
        <button type="button" disabled={operations.isFetching || Boolean(recovering)}
          onClick={() => void operations.refetch()}>{operations.isFetching ? t("正在核对…", "Checking…") : t("刷新核对记录", "Refresh records")}</button></div>
      {operations.isError ? <p role="alert">{message(operations.error, t)} {t("请刷新核对记录后再压缩。", " Refresh the records before compacting.")}</p>
        : !records ? <p role="status">{t("正在读取压缩记录…", "Loading compaction history…")}</p>
        : !records.length ? <p className="v2-context-help">{t("还没有压缩记录。", "No compaction history yet.")}</p>
        : <ul className="v2-context-compaction-list">{records.map((operation) => <CompactionOperationRow key={operation.operation_id}
          operation={operation} disabled={controlsPending} resumeDisabled={blocked} recovering={recovering === operation.operation_id}
          onRecover={(action) => void recover(operation, action)} />)}</ul>}
    </details>
  </div>;
}

const compactionStatus = (t: LocaleText) => ({ prepared: t("准备压缩", "Preparing compaction"), started: t("正在生成摘要", "Generating summary"), generated: t("摘要已生成，待采用", "Summary generated, awaiting application"),
  completed: t("已完成", "Completed"), noop: t("无需压缩", "No compaction needed"), failed: t("已结束，未采用摘要", "Finished without applying a summary") });
function CompactionOperationRow({ operation, disabled, resumeDisabled, recovering, onRecover }: {
  operation: ThreadCompactionOperation; disabled: boolean; resumeDisabled: boolean; recovering: boolean; onRecover: (action: "resume" | "discard") => void;
}) {
  const { t, locale } = useLocale();
  const unfinished = unfinishedCompaction(operation);
  return <li className="v2-context-compaction-record">
    <div className="v2-context-compaction-heading"><strong>{compactionStatus(t)[operation.status]}</strong>
      <time dateTime={operation.created_at}>{new Date(operation.created_at).toLocaleString(locale, { hour12: false })}</time></div>
    <p className="v2-context-compaction-meta">{operation.model ? `${operation.provider ? `${operation.provider} / ` : ""}${operation.model}` : t("压缩操作", "Compaction")}
      <span title={operation.operation_id}>{t("操作", "Operation ")}{operation.operation_id}</span></p>
    {operation.status === "prepared" && <p>{t("请求已准备，可刷新记录核对进度。", "The request is prepared. Refresh to check progress.")}</p>}
    {operation.status === "started" && <p>{t("模型请求已发出，可能仍在运行。可刷新记录核对进度。", "The model request was sent and may still be running. Refresh to check progress.")}</p>}
    {operation.status === "generated" && <p>{t("恢复会校验对话版本并采用已保存的摘要，无需再次请求模型。", "Resume checks the conversation version and applies the saved summary without another model request.")}</p>}
    {operation.result && <p>{compactionNotice(operation.result, t)}</p>}
    {operation.error_message && <p className="v2-context-compaction-error">{operation.error_message}</p>}
    {!operation.usage_known && <p className="v2-context-help">{t("本次用量尚未确认。", "Usage for this operation is not confirmed yet.")}</p>}
    {unfinished && <>
      {operation.status === "started" && <p className="v2-context-help">{t("放弃将停止采用本次结果；已发出的模型请求仍可能计费。", "Discarding prevents this result from being applied. A model request already sent may still incur charges.")}</p>}
      <div className="v2-context-control-actions">
        {operation.status === "generated" && <button type="button" disabled={resumeDisabled} onClick={() => onRecover("resume")}>{recovering ? t("正在核对…", "Checking…") : t("恢复已生成摘要", "Resume with saved summary")}</button>}
        <button type="button" disabled={disabled} onClick={() => onRecover("discard")}>{recovering ? t("正在处理…", "Processing…") : t("放弃本次压缩", "Discard this compaction")}</button>
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
  const { t } = useLocale();
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
      setError(message(failure, t));
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
  return <section className="v2-composer-card v2-context-pin-card" aria-labelledby="thread-pin-question">
    <header><MessageCircleQuestion size={18} aria-hidden="true" /><span>{t("问题", "Question")}</span>
      <button type="button" aria-label={t("关闭固定要求提问", "Close pinned-requirement question")} disabled={pending} onClick={close}><X size={18} /></button></header>
    <div className="v2-context-pin-body">
    <h2 id="thread-pin-question">{proposal.question || t("是否让这段要求在本次任务压缩后继续保留？", "Keep this requirement available after this task is compacted?")}</h2><blockquote>{proposal.text}</blockquote>
    <fieldset disabled={pending || Boolean(resolved)} aria-label={t("选择处理方式", "Choose how to proceed")}>
      <label className="v2-context-pin-option"><input type="radio" name="pin-choice" value="pin_task"
        checked={choice === "pin_task"} onChange={() => choose("pin_task")} />
        <span className="v2-context-pin-number">1</span><span><strong>{t("本次任务始终保留", "Keep for this task")}</strong></span></label>
      <label className="v2-context-pin-option"><input type="radio" name="pin-choice" value="discussion_only"
        checked={choice === "discussion_only"} onChange={() => choose("discussion_only")} />
        <span className="v2-context-pin-number">2</span><span><strong>{t("仅当前讨论", "Only this discussion")}</strong></span></label>
    </fieldset>
    {resolved && error && <p>{t("选择已保存，答复尚待核对。重试会沿用同一条答复。", "Your choice is saved; the reply still needs checking. Retrying reuses the same reply.")}</p>}
    {error && <p role="alert">{error}</p>}
    </div>
    <footer>
      <div className={`v2-context-pin-custom${choice === "custom" ? " is-active" : ""}`}>
        <label className="v2-context-pin-reply"><Pencil size={18} aria-hidden="true" />
          <textarea aria-label={t("你的回复", "Your reply")} rows={1} placeholder={t("或自行撰写回复", "Or write your own reply")} value={custom} disabled={pending || Boolean(resolved)}
            onPointerDown={() => { if (!pending && !resolved) choose("custom"); }}
            onChange={(event) => { choose("custom"); setCustom(event.target.value); }} /></label>
        {choice === "custom" && <label className="v2-context-pin-custom-toggle"><input type="checkbox" checked={pinCustom}
          disabled={pending || Boolean(resolved)} onChange={(event) => setPinCustom(event.target.checked)} />{t("将这段回复固定到本次任务", "Pin this reply to the task")}</label>}
      </div>
      <div className="v2-context-pin-actions"><button type="button" disabled={pending} onClick={close}>{t("跳过", "Skip")}</button>
      <button type="button" className="primary" disabled={pending || (!resolved && choice === "custom" && !custom.trim())}
        onClick={() => void resolve(choice)}>{pending ? t("正在提交…", "Submitting…") : resolved ? t("重试发送答复", "Retry reply") : t("发送", "Send")}</button></div></footer>
  </section>;
}
