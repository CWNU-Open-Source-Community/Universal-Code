import { useId, useRef, useState, type RefObject } from "react";
import { createPortal } from "react-dom";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { ArrowUpRight, Check, Clock3, MessageSquare, Reply, Send, X } from "lucide-react";
import type { APIClient } from "../../api/client";
import { maximumPeerMessageBytes, peerMessageBytes, type ThreadPeerMessage, type ThreadPeerMessageRequest } from "../../api/thread-peer-messages";
import { useModalFocusTrap } from "../../hooks/use-modal-focus-trap";
import { useLocale } from "../../lib/locale";
import { v2QueryKeys } from "../query-keys";
import "./thread-peer-messages.css";

export type PeerReply = (message: ThreadPeerMessage, trigger: HTMLElement) => void;

export function PeerMessageStatus({ status }: { status: ThreadPeerMessage["status"] }) {
  const { t } = useLocale();
  const label = status === "queued" ? t("等待接收", "Queued") : status === "delivered" ? t("已送达模型", "Delivered to model")
    : status === "failed" ? t("投递失败", "Delivery failed") : t("已取消", "Cancelled");
  return <span className={`v2-peer-status is-${status}`}>
    {status === "queued" ? <Clock3 size={12} aria-hidden="true" /> : status === "delivered" ? <Check size={12} aria-hidden="true" /> : null}{label}
  </span>;
}

export function V2PeerMessageCard({ message, threadID, onOpenThread, onReply }: {
  message: ThreadPeerMessage; threadID: string; onOpenThread?: (id: string) => void; onReply?: PeerReply;
}) {
  const { t } = useLocale();
  const incoming = message.target_thread_id === threadID;
  const otherID = incoming ? message.source_thread_id : message.target_thread_id;
  const title = incoming ? message.source_title : message.target_title;
  return <article className="v2-peer-message" aria-label={t("对话间消息", "Message between chats")}>
    <header><MessageSquare size={15} aria-hidden="true" />
      <span>{incoming ? t("来自", "From") : t("发给", "To")}</span>
      <button className="v2-peer-chat-link" disabled={!onOpenThread} onClick={() => onOpenThread?.(otherID)}
        title={title} type="button">{title || otherID}<ArrowUpRight size={13} aria-hidden="true" /></button>
      <PeerMessageStatus status={message.status} />
    </header>
    <p className="v2-peer-content">{message.content}</p>
    {message.error && <p className="v2-peer-error">{message.error}</p>}
    <footer><span>{message.status === "queued" ? t("等待接收；运行中的聊天会在当前轮结束后处理。", "Waiting for the recipient. A running chat receives this after its current turn.")
      : message.status === "delivered" ? t("已加入对方上下文，处理结果以回复为准。", "Added to the other chat’s context. A reply will confirm the outcome.")
        : message.status === "cancelled" ? t("此消息不会继续投递。", "This message will not be delivered.") : t("消息尚未送达。", "The message has not been delivered.")}</span>
      {incoming && onReply && <button onClick={(event) => onReply(message, event.currentTarget)} type="button">
        <Reply size={14} aria-hidden="true" />{t("回复", "Reply")}</button>}
    </footer>
  </article>;
}

export function V2PeerMessageDialog({ client, threadID, sourceTitle, reply, onClose, returnFocusRef }: {
  client: APIClient; threadID: string; sourceTitle: string; reply?: ThreadPeerMessage;
  onClose: () => void; returnFocusRef: RefObject<HTMLElement | null>;
}) {
  const { t } = useLocale();
  const queryClient = useQueryClient();
  const [targetID, setTargetID] = useState(reply?.source_thread_id ?? "");
  const [content, setContent] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const intent = useRef<{ key: string; request: ThreadPeerMessageRequest } | null>(null);
  const headingID = useId();
  const selectID = useId();
  const contentID = useId();
  const contentRef = useRef<HTMLTextAreaElement>(null);
  const dialog = useModalFocusTrap<HTMLElement>(true, onClose, busy, reply ? contentRef : undefined,
    { isolateBackground: true, returnFocusRef });
  const targets = useQuery({ queryKey: ["thread", threadID, "peer-targets"],
    queryFn: ({ signal }) => client.threadPeerTargets(threadID, signal), retry: false });
  const target = targets.data?.items.find((item) => item.id === targetID);
  const tooLarge = peerMessageBytes(content.trim()) > maximumPeerMessageBytes;
  const canSend = client.hasThreadControl && Boolean(target) && Boolean(content.trim()) && !tooLarge && !busy;
  const send = async () => {
    if (!canSend) return;
    const request: ThreadPeerMessageRequest = { version: "thread_peer_message.v1", target_thread_id: targetID,
      content: content.trim(), ...(reply ? { reply_to: reply.id } : {}) };
    if (!intent.current || JSON.stringify(intent.current.request) !== JSON.stringify(request)) {
      intent.current = { key: `peer-message-${crypto.randomUUID()}`, request };
    }
    setBusy(true);
    setError("");
    try {
      await client.sendThreadPeerMessage(threadID, request, intent.current.key);
      intent.current = null;
      for (const id of [threadID, targetID]) {
        void queryClient.invalidateQueries({ queryKey: v2QueryKeys.thread(id) });
      }
      void queryClient.invalidateQueries({ queryKey: v2QueryKeys.threads() });
      onClose();
    } catch (cause) {
      setError(cause instanceof Error ? cause.message : t("发送失败，请重试。", "Could not send. Try again."));
    } finally { setBusy(false); }
  };
  return createPortal(<div className="v2-peer-overlay" role="presentation" onMouseDown={(event) => {
    if (event.target === event.currentTarget && !busy) onClose();
  }}><section className="v2-peer-dialog" role="dialog" aria-modal="true" aria-labelledby={headingID} ref={dialog} tabIndex={-1}>
    <header><MessageSquare size={19} aria-hidden="true" /><h2 id={headingID}>{reply ? t("回复其他聊天", "Reply to another chat")
      : t("发送到其他聊天", "Message another chat")}</h2>
      <button aria-label={t("关闭", "Close")} disabled={busy} onClick={onClose} type="button"><X size={18} aria-hidden="true" /></button></header>
    <form id={`${headingID}-form`} className="v2-peer-dialog-body" onSubmit={(event) => { event.preventDefault(); void send(); }}>
      <p className="v2-peer-hint">{t("给同一项目中的其他聊天发送消息。", "Send a message to another chat in this project.")}</p>
      <div className="v2-peer-source"><span>{t("发自", "From")}</span><strong title={sourceTitle}>{sourceTitle}</strong></div>
      <label htmlFor={selectID}>{t("发送给", "To")}</label>
      <select id={selectID} value={targetID} disabled={busy || Boolean(reply)} onChange={(event) => { setTargetID(event.target.value); setError(""); }}>
        <option value="">{targets.isPending ? t("正在加载聊天…", "Loading chats…") : t("选择聊天", "Choose a chat")}</option>
        {targets.data?.items.map((item) => <option key={item.id} value={item.id}>{item.title || item.id}</option>)}
      </select>
      {targets.isError && <p className="v2-peer-error" role="alert">{t("无法加载可接收消息的聊天。", "Could not load available chats.")}
        <button onClick={() => void targets.refetch()} type="button">{t("重试", "Retry")}</button></p>}
      {targets.data && !targets.data.items.length && <p className="v2-peer-hint">{t("此项目中还没有其他可接收消息的聊天。", "There are no other available chats in this project.")}</p>}
      {targets.data?.has_more && <p className="v2-peer-hint">{t("显示最近的 100 条可用聊天。", "Showing the 100 most recent available chats.")}</p>}
      {reply && targets.data && !target && <p className="v2-peer-error" role="alert">{t("来源聊天已不可接收消息。", "The source chat can no longer receive messages.")}</p>}
      {reply && <blockquote className="v2-peer-reply-context">{reply.content}</blockquote>}
      <label htmlFor={contentID}>{t("消息", "Message")}</label>
      <textarea id={contentID} ref={contentRef} value={content} rows={5} disabled={busy}
        placeholder={t("写下要分享的结论、问题或进度…", "Share a finding, question, or progress update…")}
        onChange={(event) => { setContent(event.target.value); setError(""); }} />
      {tooLarge && <p className="v2-peer-error" role="alert">{t("消息超过 16 KB，请缩短后再发送。", "The message exceeds 16 KB. Shorten it before sending.")}</p>}
      <p className="v2-peer-hint">{t("对方会看到来源聊天，并按自己的权限处理。", "The recipient sees the source chat and keeps its own permissions.")}</p>
      {error && <p className="v2-peer-error" role="alert">{error}</p>}
    </form>
    <footer><button onClick={onClose} disabled={busy} type="button">{t("取消", "Cancel")}</button>
      <button className="v2-peer-send" disabled={!canSend} form={`${headingID}-form`} type="submit">
        <Send size={15} aria-hidden="true" />{busy ? t("正在发送…", "Sending…") : t("发送消息", "Send message")}</button></footer>
  </section></div>, document.body);
}
