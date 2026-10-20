import { useLocale, type LocaleText } from "../../lib/locale";
import { useEffect, useId, useMemo, useReducer, useRef, useState } from "react";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { ChevronDown, ChevronUp, RefreshCw } from "lucide-react";
import { APIRequestError, type APIClient } from "../../api/client";
import { inspectQueueCancellation, inspectQueuePromotion, inspectQueueRevision, promoteQueuedMessage, provesQueueRevisionUnchanged,
  readQueuedMessages, reviseQueuedMessage, type QueueBinding, type QueuePromotionInput, type QueuedMessage } from "../../api/queued-messages";
import { getV2DraftDocument } from "../draft-context";
import type { DraftDocument } from "../draft-document";
import { useV2RecoveryStore, type V2RecoveryStore } from "../recovery-storage";
import { closeQueueEdit, queueEditVisible, reopenQueueEdit, queueEditKey, threadQueueEditPrefix, queueEditScope, threadQueueOperationPrefix, readQueueEdits, readQueueOperations,
  saveQueueOperation, settleQueueOperation, type QueueEdit, type QueueOperation } from "../queue-recovery";
import { v2QueryKeys } from "../query-keys";
import { V2DraftConflict } from "./draft-conflict";
import { V2QueuedMessageRow, V2QueueMessageDetails } from "./queued-message-row";
import "./queued-messages.css";

const explanation = (error: unknown, t: LocaleText) => error instanceof Error ? error.message : t("操作暂未完成，内容已保留。", "The operation is not complete. Your content is kept.");
const knownRejection = (error: unknown) => error instanceof APIRequestError && [400, 401, 403, 404, 409, 412, 413, 422].includes(error.status);
const empty = (text = "") => ({ text, files: [], images: [] });
const promotionInput = (operation: QueueOperation, t: LocaleText): QueuePromotionInput => {
  if (!operation.expectedAttemptID || !operation.expectedExecutionID) throw new Error(t("原引导操作的执行身份不完整，原记录已保留。", "The original guidance request has an incomplete execution identity. Its record is kept."));
  return { ...operation, expectedAttemptID: operation.expectedAttemptID, expectedExecutionID: operation.expectedExecutionID };
};

export function useV2QueuedMessagesQuery(client: APIClient, binding: QueueBinding | null, running: boolean) {
  const { t } = useLocale();
  return useQuery({ queryKey: [...v2QueryKeys.thread(binding?.threadID ?? ""), "queued-messages", client.baseURL,
    binding?.runID ?? "", binding?.sessionID ?? "", binding?.workspaceID ?? ""],
    queryFn: ({ signal }) => {
      if (!binding) throw new Error(t("尚未确认队列所属的任务。", "The queue's task has not been confirmed."));
      return readQueuedMessages(client, binding, signal);
    }, enabled: Boolean(binding), retry: false,
    refetchInterval: (current) => running && !current.state.error ? 2000 : false });
}

export function V2QueuedMessages(props: QueueBinding & { client: APIClient; running: boolean; canPromote?: boolean }) {
  // A late response belongs to the captured task and cannot replace another editor.
  return <QueuePanel key={JSON.stringify([props.client.baseURL, props.threadID, props.runID, props.sessionID, props.workspaceID])} {...props} />;
}
function QueuePanel({ client, running, canPromote = false, ...binding }: QueueBinding & { client: APIClient; running: boolean; canPromote?: boolean }) {
  const { t } = useLocale();
  const queryClient = useQueryClient();
  const store = useV2RecoveryStore();
  const document = useMemo(() => store ? getV2DraftDocument(store) : null, [store]);
  const [, refresh] = useReducer((count: number) => count + 1, 0);
  const [noticeState, setNoticeState] = useState({ text: "", successful: false });
  const notice = noticeState.text;
  const setNotice = (text: string, successful = false) => setNoticeState({ text, successful });
  const [busy, setBusy] = useState<string[]>([]);
  const [collapsed, setCollapsed] = useState(false);
  const [details, setDetails] = useState<{ message: QueuedMessage; trigger: HTMLElement | null } | null>(null);
  const detailsID = useId();
  const listID = useId();
  const toggleRef = useRef<HTMLButtonElement>(null);
  const query = useV2QueuedMessagesQuery(client, binding, running);
  const bindingKey = JSON.stringify(binding);
  useEffect(() => {
    const disconnect = [threadQueueEditPrefix(binding), threadQueueOperationPrefix(binding), "queue-result:", "queue-editor-closed:"].map((prefix) => store?.subscribePrefix?.(prefix, refresh));
    return () => disconnect.forEach((stop) => stop?.());
  }, [store, bindingKey]);
  let edits: QueueEdit[] = [], operations: QueueOperation[] = [], recoveryError = "";
  try {
    if (store && document) {
      edits = readQueueEdits(store, binding, true).filter((edit) => {
        const state = document.read(queueEditScope(edit));
        return queueEditVisible(store, edit, state);
      });
      operations = readQueueOperations(store, binding, true);
    }
  } catch (error) { recoveryError = explanation(error, t); }
  const completedEmpty = query.isSuccess && !query.isFetching && query.data.items.length === 0 && !running &&
    !edits.length && !operations.length && !busy.length && !recoveryError;
  useEffect(() => {
    if (completedEmpty) setNoticeState((current) => current.successful ? { text: "", successful: false } : current);
  }, [completedEmpty, noticeState.successful]);
  const invalidate = async () => { await queryClient.invalidateQueries({ queryKey: v2QueryKeys.thread(binding.threadID) }); };
  const startEdit = (message: QueuedMessage) => {
    try {
      if (!store || !document) throw new Error(t("当前连接无法保存编辑稿，请先恢复本机存储连接。", "This connection cannot save edits. Restore local storage first."));
      const edit: QueueEdit = { ...binding, version: "queue_edit.v1", message };
      const key = queueEditKey(edit), scope = queueEditScope(edit);
      store.assertReadable(key);
      if (store.read<unknown>(key, null) === null) store.write(key, edit);
      const state = document.read(scope);
      if (!state.ref || !queueEditVisible(store, edit, state)) document.update(scope, empty(message.content), state.ref);
      reopenQueueEdit(store, edit);
      refresh(); setNotice("");
    } catch (error) { setNotice(explanation(error, t)); }
  };
  const finish = async (operation: QueueOperation) => {
    if (!store || !document) return;
    if (operation.kind === "revise" && operation.draftRef) {
      const edit = readQueueEdits(store, operation).find((candidate) => candidate.message.id === operation.messageID && candidate.message.revision === operation.expectedRevision);
      if (!edit) throw new Error(t("修改已保存，但原编辑稿记录暂不可读，请核对后再关闭。", "Changes are saved, but the original edit record is unavailable. Check it before closing."));
      closeQueueEdit(store, document, edit, operation.draftRef);
    }
    settleQueueOperation(store, operation, "applied"); refresh();
    setNotice(operation.kind === "revise" ? t("修改已保存。", "Changes saved.") : operation.kind === "promote"
      ? t("已转为引导。", "Sent as guidance.") : t("消息已撤回。", "Message withdrawn."), true);
    await invalidate();
  };
  const finishRejectedPromotion = async (operation: QueueOperation) => {
    if (!store) return;
    settleQueueOperation(store, operation, "obsolete"); refresh();
    setNotice(t("这次引导未生效，原消息保留，可重新引导。", "The guidance was not applied. The original message is kept and can be sent as guidance again."));
    await invalidate();
  };
  const observeOperation = async (operation: QueueOperation): Promise<boolean> => {
    if (!store) return false;
    if (operation.kind === "promote") {
      const result = await inspectQueuePromotion(client, promotionInput(operation, t));
      if (result.state === "sealed") { await finish(operation); return true; }
      if (result.state === "rejected") { await finishRejectedPromotion(operation); return true; }
      // Local execution telemetry cannot prove whether another API instance
      // will still commit the captured request. Only durable source changes do.
      if (result.message.status !== "pending" || result.message.revision !== operation.expectedRevision) {
        settleQueueOperation(store, operation, "obsolete"); refresh();
        setNotice(t("原消息已变化或已处理，原引导请求已失效。历史记录和本地编辑稿已保留，请核对最新消息。", "The message changed or was processed, so the original guidance request expired. History and local edits are kept. Check the latest message."));
        await invalidate(); return true;
      }
      return false;
    }
    const result = operation.kind === "revise" ? await inspectQueueRevision(client, operation) : await inspectQueueCancellation(client, operation);
    if (result.state === "sealed") { await finish(operation); return true; }
    const obsolete = result.message.status !== "pending" || operation.kind === "revise" && result.message.revision > operation.expectedRevision;
    if (obsolete) {
      settleQueueOperation(store, operation, "obsolete"); refresh();
      setNotice(operation.kind === "revise" ? t("原消息版本已变化或已处理，原修改请求已失效。编辑稿已保留，请核对最新消息。", "The message changed or was processed, so the original revision request expired. Your edits are kept. Check the latest message.") : t("消息已被撤回或已交给模型，原撤回请求已失效。请查看最新队列状态。", "The message was withdrawn or delivered to the model. The original withdrawal request expired; check the latest queue."));
      await invalidate(); return true;
    }
    return false;
  };
  const perform = async (operation: QueueOperation, observe = false) => {
    if (!store || busy.includes(operation.messageID)) return;
    setBusy((current) => [...current, operation.messageID]); setNotice("");
    let resolved = false;
    try {
      if (observe) {
        if (!await observeOperation(operation)) setNotice(operation.kind === "promote"
          ? t("尚未查到这次引导的最终记录。原操作已保留，可以继续核对或重试原引导。", "No final guidance record was found. The original request is kept; check again or retry it.")
          : t("尚未查到这次操作的最终记录。编辑稿已保留，可以继续核对或重试原操作。", "No final operation record was found. Your edits are kept; check again or retry the original request."));
        return;
      }
      if (operation.kind === "revise") {
        await reviseQueuedMessage(client, operation);
      } else if (operation.kind === "promote") {
        const result = await promoteQueuedMessage(client, promotionInput(operation, t));
        if (result.rejected === true) { resolved = true; await finishRejectedPromotion(operation); return; }
      } else {
        const receipt = await client.cancelSessionSteering(operation.sessionID, operation.messageID,
          { version: "session_steering_cancellation.v1", reason: t("用户从待处理消息列表撤回", "Withdrawn by the user from pending messages") }, operation.operationKey);
        if (receipt.run_id !== operation.runID) throw new Error(t("撤回结果的任务来源不同，请核对原消息。", "The withdrawal result belongs to a different task. Check the original message."));
      }
      resolved = true; await finish(operation);
    } catch (error) {
      if (!resolved && !observe && operation.kind === "revise" && await provesQueueRevisionUnchanged(error, operation)) {
        try {
          settleQueueOperation(store, operation, "unchanged"); refresh();
          setNotice(t("规范化后的正文与当前消息相同，未产生修订。编辑稿已保留，可继续修改。", "The normalized text matches the current message, so no revision was created. Your draft is kept for further edits."));
        } catch (failure) { setNotice(t("结果记录暂未保存，原操作和编辑稿已保留。", "The result could not be saved. The original operation and edits are kept. ") + explanation(failure, t)); }
        return;
      }
      if (!resolved && !observe && knownRejection(error)) {
        // An HTTP refusal only describes this transport attempt. Another
        // window may already have committed the same immutable operation.
        try { if (await observeOperation(operation)) return; } catch { /* Keep the original journal and draft. */ }
      }
      setNotice(`${operation.kind === "revise" ? t("保存", "Save") : operation.kind === "promote" ? t("引导", "Guide") : t("撤回", "Withdraw")}${t("结果待确认。", " result awaiting confirmation. ")}${explanation(error, t)}`);
    } finally { setBusy((current) => current.filter((id) => id !== operation.messageID)); refresh(); }
  };
  const submit = (operation: QueueOperation) => {
    try {
      if (!store) throw new Error(t("当前连接无法可靠保存操作记录。", "This connection cannot reliably save operation records."));
      if (readQueueOperations(store, binding, true).some((pending) => pending.messageID === operation.messageID)) throw new Error(t("请先确认这条消息的上次操作结果。", "Check the previous operation on this message first."));
      saveQueueOperation(store, operation); refresh(); void perform(operation);
    } catch (error) { setNotice(explanation(error, t)); }
  };
  const items = query.data?.items ?? [];
  const recoveredIDs = new Set(edits.map((edit) => edit.message.id));
  const promotionUnavailable = (message: QueuedMessage): string => {
    if (message.images.length || message.attachments.length) return t("当前只支持文字引导；图片与附件保留在下一轮消息中。", "Only text can be sent as guidance. Images and attachments stay queued for the next turn.");
    if (message.delivery_mode === "steer") return t("这条消息已经用于更新当前任务。", "This message already updated the current task.");
    if (message.prepared) return t("消息正在处理，无法转为引导。", "This message is being processed and cannot be sent as guidance.");
    if (!message.content.trim()) return t("当前只支持将文字消息转为引导。", "Only text messages can currently be sent as guidance.");
    if (!client.hasSessionSteeringControl || !message.can_edit || !message.can_cancel) return t("当前连接或消息状态不允许引导。", "This connection or message state does not allow guidance.");
    if (!store || recoveryError) return t("本机操作记录暂不可用，请先恢复存储连接。", "Local operation records are unavailable. Restore the storage connection first.");
    if (recoveredIDs.has(message.id)) return t("请先保存或取消这条消息的本地编辑。", "Save or cancel local edits to this message first.");
    if (busy.includes(message.id) || operations.some((operation) => operation.messageID === message.id)) return t("请先核对这条消息的上次操作结果。", "Check the previous operation on this message first.");
    if (!query.isSuccess || !query.data.current_attempt_id || !query.data.execution_id) return t("尚未确认当前执行身份，请刷新队列后重试。", "The current execution identity is unconfirmed. Refresh the queue and retry.");
    if (!canPromote) return t("当前执行暂时无法接受引导，请刷新状态后重试。", "This run cannot accept guidance right now. Refresh its status and retry.");
    return "";
  };
  const startPromotion = (message: QueuedMessage) => {
    const reason = promotionUnavailable(message);
    if (reason) { setNotice(reason); return; }
    submit({ ...binding, version: "queue_operation.v1", kind: "promote", operationKey: crypto.randomUUID(),
      messageID: message.id, expectedRevision: message.revision, oldSHA256: message.content_sha256, content: "",
      expectedAttemptID: query.data!.current_attempt_id, expectedExecutionID: query.data!.execution_id });
  };
  if (!query.isError && !items.length && !edits.length && !operations.length && !notice && !recoveryError && !details) return null;
  return <section className="v2-queued-messages" aria-label={t("待处理消息", "Pending messages")}>
    <header>
      <button className="v2-queue-toggle" type="button" ref={toggleRef} aria-expanded={!collapsed} aria-controls={listID}
        title={collapsed ? t("展开待处理消息", "Expand pending messages") : t("收起待处理消息", "Collapse pending messages")} onClick={() => setCollapsed((value) => !value)}>
        <span>{t(`待处理 ${query.data?.pending ?? "—"} 条`, `Pending ${query.data?.pending ?? "—"} messages`)}{Boolean(query.data?.prepared) && t(` · 正在处理 ${query.data?.prepared} 条`, ` · Processing ${query.data?.prepared}`)}</span>
        {collapsed ? <ChevronDown size={14} aria-hidden="true" /> : <ChevronUp size={14} aria-hidden="true" />}
      </button>
      <button className="v2-queue-icon" type="button" aria-label={t("刷新列表", "Refresh list")} title={t("刷新列表", "Refresh list")}
        onClick={() => void query.refetch()} disabled={query.isFetching}><RefreshCw size={13} aria-hidden="true" /></button>
    </header>
    {query.isError && <p role="alert">{t("暂时无法读取完整队列。已有消息仍可在对话记录中查看。", "The full queue is unavailable. Existing messages remain in conversation history.")}{explanation(query.error, t)}</p>}
    {recoveryError && <p role="alert">{recoveryError}</p>}
    {notice && <p role="status">{notice}</p>}
    <ol id={listID} hidden={collapsed}>{!collapsed && items.map((message) => <V2QueuedMessageRow key={message.id}
      client={client} message={message} detailsID={detailsID} detailsOpen={details?.message.id === message.id}
      onDetails={(trigger) => setDetails({ message, trigger })}
      promotionUnavailable={promotionUnavailable(message)} onPromote={() => startPromotion(message)}
      editDisabled={!message.can_edit || !client.hasSessionSteeringControl || !store || !!recoveryError ||
        recoveredIDs.has(message.id) || busy.includes(message.id) || operations.some((operation) => operation.messageID === message.id)}
      cancelDisabled={!message.can_cancel || !client.hasSessionSteeringControl || !store || !!recoveryError ||
        busy.includes(message.id) || operations.some((operation) => operation.messageID === message.id)}
      onEdit={() => startEdit(message)} onCollapse={() => { setCollapsed(true); toggleRef.current?.focus(); }}
      onCancel={() => submit({ ...binding,
          version: "queue_operation.v1", kind: "cancel", operationKey: crypto.randomUUID(), messageID: message.id,
          expectedRevision: message.revision, oldSHA256: message.content_sha256, content: "" })} />)}</ol>
    {/* Recovery controls stay reachable even when the queue preview is collapsed. */}
    {document && store && edits.map((edit) => <QueueEditor key={queueEditKey(edit)} edit={edit} document={document} store={store}
      client={client} current={edit.runID === binding.runID && edit.sessionID === binding.sessionID ? items.find((message) => message.id === edit.message.id) : undefined} refresh={refresh}
      previousRun={edit.runID !== binding.runID || edit.sessionID !== binding.sessionID}
      queueKnown={query.isSuccess} locked={!!recoveryError || !query.isSuccess || operations.some((operation) => operation.messageID === edit.message.id) || busy.includes(edit.message.id)}
      submit={submit} startEdit={startEdit} />)}
    {operations.map((operation) => <div className="v2-queued-message-pending" key={operation.operationKey}>
      <p>{operation.runID !== binding.runID && t("上轮操作 ·", "Previous operation · ")}{operation.kind === "revise" ? t("修改保存结果待确认", "Saved edit awaiting confirmation") : operation.kind === "promote" ? t("引导结果待确认", "Guidance awaiting confirmation") : t("撤回结果待确认", "Withdrawal awaiting confirmation")} {t("· 消息", " · Message ")}{operation.messageID}</p>
      <button type="button" disabled={busy.includes(operation.messageID)} onClick={() => void perform(operation, true)}>{operation.kind === "revise" ? t("核对保存结果", "Check saved edit") : operation.kind === "promote" ? t("核对引导结果", "Check guidance") : t("核对撤回结果", "Check withdrawal")}</button>
      <button type="button" disabled={busy.includes(operation.messageID) || !client.hasSessionSteeringControl}
        onClick={() => void perform(operation)}>{operation.kind === "revise" ? t("重试原保存", "Retry original save") : operation.kind === "promote" ? t("重试原引导", "Retry original guidance") : t("继续撤回", "Continue withdrawal")}</button>
    </div>)}
    {details && <V2QueueMessageDetails client={client} snapshot={details.message} detailsID={detailsID}
      current={query.isSuccess ? items.find((message) => message.id === details.message.id) : undefined} queueKnown={query.isSuccess}
      trigger={details.trigger} fallbackFocus={toggleRef} onClose={() => setDetails(null)} />}
  </section>;
}

function QueueEditor({ edit, current, client, document, store, locked, queueKnown, previousRun, refresh, submit, startEdit }: {
  edit: QueueEdit; current?: QueuedMessage; client: APIClient; document: DraftDocument; store: V2RecoveryStore;
  locked: boolean; queueKnown: boolean; previousRun: boolean; refresh: () => void; submit: (operation: QueueOperation) => void; startEdit: (message: QueuedMessage) => void;
}) {
  const { t } = useLocale();
  const scope = useMemo(() => queueEditScope(edit), [edit.threadID, edit.runID, edit.sessionID, edit.message.id, edit.message.revision]);
  useEffect(() => document.subscribe(scope, refresh), [document, scope, refresh]);
  const state = document.read(scope);
  const [error, setError] = useState("");
  const stale = current?.revision !== edit.message.revision;
  const bytes = new TextEncoder().encode(state.snapshot.text.trim()).byteLength;
  const eligible = !!current?.can_edit && client.hasSessionSteeringControl && !stale && !locked && !state.conflict && !state.error && state.persisted &&
    state.snapshot.text.trim() !== edit.message.content && bytes <= 16384 && (bytes > 0 || edit.message.images.length > 0 || edit.message.attachments.length > 0);
  const save = () => {
    try {
      if (!eligible) return;
      const latest = document.read(scope);
      if (latest.conflict || latest.error || !latest.ref || latest.ref.branchID !== state.ref?.branchID || latest.ref.seq !== state.ref?.seq) throw new Error(t("编辑稿已变化，请核对后再保存。", "Your edit has changed. Check it before saving."));
      submit({ ...edit, version: "queue_operation.v1", kind: "revise", messageID: edit.message.id, expectedRevision: edit.message.revision,
        oldSHA256: edit.message.content_sha256, content: latest.snapshot.text.trim(), draftRef: latest.ref, operationKey: crypto.randomUUID() });
    } catch (failure) { setError(explanation(failure, t)); }
  };
  return <div className="v2-queue-editor">
    {previousRun && <p>{t("上轮编辑稿 · 原执行", "Previous edit · Original run ")}{edit.runID}</p>}
    <label>{t(`编辑消息 ${edit.message.sequence}`, `Edit message ${edit.message.sequence}`)}<textarea aria-label={t(`编辑消息 ${edit.message.sequence}`, `Edit message ${edit.message.sequence}`)} value={state.snapshot.text} rows={4}
      onChange={(event) => { document.update(scope, { text: event.target.value }, state.ref); refresh(); }} /></label>
    <V2DraftConflict state={state} client={client} workspaceID={edit.workspaceID} onResolve={(token, ref) => { document.resolve(scope, token, ref); refresh(); }} />
    {(!queueKnown || !current || current.prepared || stale) && <p role="status">{!queueKnown ? t("尚未确认最新队列状态。", "The latest queue state is unconfirmed.") : !current ? t("这条消息已离开待处理队列。", "This message left the pending queue.") : current.prepared ? t("这条消息正在处理。", "This message is being processed.") : t("这条消息已在其他窗口更新。", "This message was updated in another window.")}{t("本地编辑稿仍保留，可选中文字复制。", "Your local edit is kept. Select text to copy it.")}</p>}
    {stale && current && <details><summary>{t("查看当前排队正文", "View current queued text")}</summary><pre>{current.content}</pre></details>}
    {bytes > 16384 && <p role="alert">{t("正文超过 16 KiB，请缩短后保存。", "The text exceeds 16 KiB. Shorten it before saving.")}</p>}
    {error && <p role="alert">{error}</p>}
    <div className="v2-queued-message-actions"><button type="button" disabled={!eligible} onClick={save}>{t("保存修改", "Save changes")}</button>
      <button type="button" disabled={locked || state.conflict || !state.ref} onClick={() => {
        try { if (state.ref) closeQueueEdit(store, document, edit, state.ref); refresh(); } catch (failure) { setError(explanation(failure, t)); }
      }}>{t("取消编辑", "Cancel editing")}</button>
      {stale && current?.can_edit && <button type="button" disabled={locked || state.conflict || !state.ref} onClick={() => {
        try { if (state.ref) closeQueueEdit(store, document, edit, state.ref); startEdit(current); refresh(); } catch (failure) { setError(explanation(failure, t)); }
      }}>{t("载入最新正文，替换编辑稿", "Replace draft with latest text")}</button>}
    </div>
  </div>;
}
