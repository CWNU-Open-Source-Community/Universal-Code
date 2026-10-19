import { ArrowUpRight, Check, Clock3, MessageSquare } from "lucide-react";
import type { ThreadPeerMessage } from "../../api/thread-peer-messages";
import { useLocale } from "../../lib/locale";
import "./thread-peer-messages.css";

export function PeerMessageStatus({ status }: { status: ThreadPeerMessage["status"] }) {
  const { t } = useLocale();
  const label = status === "queued" ? t("等待接收", "Queued") : status === "delivered" ? t("已送达模型", "Delivered to model")
    : status === "failed" ? t("投递失败", "Delivery failed") : t("已取消", "Cancelled");
  return <span className={`v2-peer-status is-${status}`}>
    {status === "queued" ? <Clock3 size={12} aria-hidden="true" /> : status === "delivered" ? <Check size={12} aria-hidden="true" /> : null}{label}
  </span>;
}

export function V2PeerMessageCard({ message, threadID, onOpenThread }: {
  message: ThreadPeerMessage; threadID: string; onOpenThread?: (id: string) => void;
}) {
  const { t } = useLocale();
  const incoming = message.target_thread_id === threadID;
  const otherID = incoming ? message.source_thread_id : message.target_thread_id;
  const title = incoming ? message.source_title : message.target_title;
  return <article className="v2-peer-message" aria-label={t("模型间消息", "Message between models")}>
    <header><MessageSquare size={15} aria-hidden="true" />
      <span>{incoming ? t("来自", "From") : t("发给", "To")}</span>
      <button className="v2-peer-chat-link" disabled={!onOpenThread} onClick={() => onOpenThread?.(otherID)}
        title={title} type="button">{title || otherID}<ArrowUpRight size={13} aria-hidden="true" /></button>
      <PeerMessageStatus status={message.status} />
    </header>
    <p className="v2-peer-content">{message.content}</p>
    {message.error && <p className="v2-peer-error">{message.error}</p>}
    <footer><span>{message.status === "queued" ? t("等待接收；运行中的聊天会在当前轮结束后处理。", "Waiting for the recipient. A running chat receives this after its current turn.")
      : message.status === "delivered" ? incoming ? t("此聊天已接收消息。", "This chat has received the message.")
        : t("对方已接收，处理结果以回复为准。", "The recipient has received the message. A reply will confirm the outcome.")
        : message.status === "cancelled" ? t("此消息不会继续投递。", "This message will not be delivered.") : t("消息尚未送达。", "The message has not been delivered.")}</span>
    </footer>
  </article>;
}
