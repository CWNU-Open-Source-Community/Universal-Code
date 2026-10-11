import { useEffect, useId, useLayoutEffect, useRef, useState, type RefObject } from "react";
import { createPortal } from "react-dom";
import { ChevronUp, CornerUpRight, FileText, ListEnd, MoreHorizontal, Pencil, Text, Trash2, X } from "lucide-react";
import type { APIClient } from "../../api/client";
import type { QueuedMessage } from "../../api/queued-messages";
import { useModalFocusTrap } from "../../hooks/use-modal-focus-trap";
import { V2ImagePreview } from "./image-input";
import { V2FileAttachments } from "./file-input";

export function V2QueuedMessageRow({ client, message, editDisabled, cancelDisabled, detailsID, detailsOpen, promotionUnavailable, onPromote, onDetails, onEdit, onCancel, onCollapse }: {
  client: APIClient; message: QueuedMessage; editDisabled: boolean; cancelDisabled: boolean;
  detailsID: string; detailsOpen: boolean; onDetails: (trigger: HTMLButtonElement | null) => void;
  promotionUnavailable: string; onPromote: () => void;
  onEdit: () => void; onCancel: () => void; onCollapse: () => void;
}) {
  const [menuOpen, setMenuOpen] = useState(false);
  const trigger = useRef<HTMLButtonElement>(null);
  const preview = useRef<HTMLButtonElement>(null);
  const attachments = [message.images.length > 0 ? `${message.images.length} 张图片` : "",
    message.attachments.length === 1 ? message.attachments[0].name : message.attachments.length > 1 ? `${message.attachments.length} 个文件` : ""].filter(Boolean).join(" · ");
  return <li aria-label={`消息 ${message.sequence}`}>
    <div className="v2-queue-row">
      <ListEnd className="v2-queue-row-icon" size={15} aria-hidden="true" />
      {message.images.length > 0 ? <div className="v2-queue-thumbnail" onClickCapture={(event) => {
        const imageButton = event.target instanceof Element ? event.target.closest<HTMLButtonElement>(".v2-image-thumb") : null;
        if (imageButton) { event.preventDefault(); event.stopPropagation(); onDetails(imageButton); }
      }}><V2ImagePreview client={client} images={message.images.slice(0, 1)} /></div>
        : message.attachments.length > 0 && <FileText className="v2-queue-row-icon" size={18} aria-hidden="true" />}
      <button className="v2-queue-preview" type="button" aria-label={`查看消息 ${message.sequence} 全文`}
        ref={preview} aria-haspopup="dialog" aria-expanded={detailsOpen} aria-controls={detailsOpen ? detailsID : undefined}
        title={message.content || attachments} onClick={() => onDetails(preview.current)}>
        <span>{message.content || attachments || "（没有文字）"}</span>
        {message.content && attachments && <small>{attachments}</small>}
      </button>
      {message.delivery_mode === "steer" && <span className="v2-queue-mode">更新当前任务</span>}
      {!message.prepared && message.delivery_mode !== "steer" && <button className="v2-queue-promote" type="button"
        aria-label="引导当前任务" title={promotionUnavailable || "引导当前任务"} disabled={Boolean(promotionUnavailable)}
        onClick={onPromote}><CornerUpRight size={14} aria-hidden="true" /><span>引导</span></button>}
      {message.prepared ? <span className="v2-queue-processing" title="正在处理，无法修改或撤回">处理中</span>
        : <button className="v2-queue-icon" type="button" aria-label="撤回" title={`撤回消息 ${message.sequence}`}
          disabled={cancelDisabled} onClick={onCancel}><Trash2 size={15} aria-hidden="true" /></button>}
      <button className="v2-queue-icon" type="button" ref={trigger} aria-label={`消息 ${message.sequence} 的更多操作`}
        aria-expanded={menuOpen} aria-haspopup="menu" title="更多操作" onClick={() => setMenuOpen((value) => !value)}
        onKeyDown={(event) => {
          if (event.key === "ArrowDown" || event.key === "ArrowUp") { event.preventDefault(); setMenuOpen(true); }
        }}><MoreHorizontal size={17} aria-hidden="true" /></button>
    </div>
    {menuOpen && <QueueMessageMenu trigger={trigger} message={message} editDisabled={editDisabled}
      onClose={() => setMenuOpen(false)} onEdit={onEdit} onDetails={() => onDetails(trigger.current)} onCollapse={onCollapse} />}
  </li>;
}

export function V2QueueMessageDetails({ client, snapshot, current, queueKnown, detailsID, trigger, fallbackFocus, onClose }: {
  client: APIClient; snapshot: QueuedMessage; current?: QueuedMessage; queueKnown: boolean; detailsID: string;
  trigger: HTMLElement | null; fallbackFocus: RefObject<HTMLButtonElement | null>; onClose: () => void;
}) {
  const titleID = useId();
  const closeButton = useRef<HTMLButtonElement>(null);
  const lastSeen = useRef(snapshot);
  useEffect(() => { if (current) lastSeen.current = current; }, [current]);
  const message = current ?? lastSeen.current;
  const returnFocus = useRef<HTMLElement | null>(trigger);
  useLayoutEffect(() => {
    // Check after React removes cancelled rows from the DOM.
    returnFocus.current = trigger?.isConnected && !trigger.closest("[hidden]") ? trigger : fallbackFocus.current;
  });
  const modal = useModalFocusTrap<HTMLElement>(true, onClose, false, closeButton,
    { isolateBackground: true, returnFocusRef: returnFocus });
  const composerFallback = useRef<HTMLElement | null>(fallbackFocus.current?.closest(".v2-composer-dock")
    ?.querySelector<HTMLElement>("textarea:not(:disabled), .v2-shared-composer button:not(:disabled)") ?? null);
  useEffect(() => () => {
    // The last queued item can leave while its details are open. Closing the
    // now empty panel returns to the still mounted composer instead of body.
    if (!returnFocus.current?.isConnected && composerFallback.current?.isConnected) composerFallback.current.focus({ preventScroll: true });
  }, []);
  return createPortal(<div className="v2-queue-details-overlay" onMouseDown={(event) => {
    if (event.target === event.currentTarget) onClose();
  }}>
    <section className="v2-queue-details-dialog" id={detailsID} role="dialog" aria-modal="true" aria-labelledby={titleID}
      tabIndex={-1} ref={modal}>
      <header>
        <div><h2 id={titleID}>消息 {message.sequence} 的全文与附件</h2>
          <small>{message.delivery_mode === "steer" ? "更新当前任务" : "下一轮处理"}
            {current?.prepared && " · 正在处理"}</small></div>
        <button type="button" ref={closeButton} aria-label="关闭消息详情" title="关闭（Esc）" onClick={onClose}>
          <X size={19} aria-hidden="true" /></button>
      </header>
      <div className="v2-queue-details-body" tabIndex={0} aria-label="消息全文与附件">
        {!queueKnown && <p role="status">暂时无法确认最新队列状态，下面保留最近读取的内容。</p>}
        {queueKnown && !current && <p role="status">这条消息已离开待处理队列，下面保留最后读取的内容。</p>}
        <pre>{message.content || "（没有文字）"}</pre>
        {message.content_redacted && <p>正文包含已隐藏内容。编辑时请填写完整的新正文。</p>}
        <V2ImagePreview client={client} images={message.images} />
        <V2FileAttachments client={client} attachments={message.attachments} />
      </div>
    </section>
  </div>, trigger?.closest(".v2-shell") ?? fallbackFocus.current?.closest(".v2-shell") ?? document.body);
}

function QueueMessageMenu({ trigger, message, editDisabled, onClose, onEdit, onDetails, onCollapse }: {
  trigger: RefObject<HTMLButtonElement | null>; message: QueuedMessage; editDisabled: boolean;
  onClose: () => void; onEdit: () => void; onDetails: () => void; onCollapse: () => void;
}) {
  const ref = useRef<HTMLDivElement>(null);
  const [position, setPosition] = useState({ top: 0, left: 0 });
  const closeRef = useRef(onClose);
  closeRef.current = onClose;
  useLayoutEffect(() => {
    const anchor = trigger.current?.getBoundingClientRect(), menu = ref.current?.getBoundingClientRect();
    if (!anchor || !menu) return;
    setPosition({ left: Math.max(8, Math.min(anchor.right - menu.width, window.innerWidth - menu.width - 8)),
      top: Math.max(8, anchor.bottom + menu.height + 8 > window.innerHeight ? anchor.top - menu.height - 4 : anchor.bottom + 4) });
  }, [trigger, message.prepared]);
  useLayoutEffect(() => {
    // Polling can remove the focused edit item after the message is claimed.
    const active = document.activeElement;
    if (!ref.current?.contains(active) || active instanceof HTMLButtonElement && active.disabled) {
      ref.current?.querySelector<HTMLButtonElement>("button:not(:disabled)")?.focus({ preventScroll: true });
    }
  }, [message.prepared, editDisabled]);
  useEffect(() => {
    const outside = (event: MouseEvent) => {
      if (!ref.current?.contains(event.target as Node) && !trigger.current?.contains(event.target as Node)) {
        closeRef.current();
      }
    };
    const move = (event: Event) => {
      if (!ref.current?.contains(event.target as Node)) { trigger.current?.focus({ preventScroll: true }); closeRef.current(); }
    };
    window.addEventListener("click", outside);
    window.addEventListener("resize", move);
    window.addEventListener("scroll", move, true);
    return () => {
      window.removeEventListener("click", outside);
      window.removeEventListener("resize", move);
      window.removeEventListener("scroll", move, true);
    };
  }, [trigger]);
  const select = (action: () => void) => { trigger.current?.focus({ preventScroll: true }); onClose(); action(); };
  return createPortal(<div className="v2-queue-menu" role="menu" aria-label={`消息 ${message.sequence} 的操作`} ref={ref} style={position}
    onBlur={(event) => { if (!event.currentTarget.contains(event.relatedTarget as Node | null) && event.relatedTarget !== trigger.current) onClose(); }}
    onKeyDown={(event) => {
      if (event.key === "Escape") { event.preventDefault(); event.stopPropagation(); select(() => undefined); return; }
      if (event.key === "Tab") { event.preventDefault(); select(() => undefined); return; }
      if (!["ArrowDown", "ArrowUp", "Home", "End"].includes(event.key)) return;
      event.preventDefault();
      const items = Array.from(ref.current?.querySelectorAll<HTMLButtonElement>("button:not(:disabled)") ?? []);
      const current = items.indexOf(document.activeElement as HTMLButtonElement);
      const next = event.key === "Home" ? 0 : event.key === "End" ? items.length - 1
        : (current + (event.key === "ArrowDown" ? 1 : -1) + items.length) % items.length;
      items[next]?.focus();
    }}>
    {!message.prepared && <button type="button" role="menuitem" disabled={editDisabled} onClick={() => select(onEdit)}><Pencil size={15} aria-hidden="true" />编辑消息</button>}
    <button type="button" role="menuitem" onClick={() => select(onDetails)}><Text size={15} aria-hidden="true" />查看全文与附件</button>
    <button type="button" role="menuitem" onClick={() => select(onCollapse)}><ChevronUp size={15} aria-hidden="true" />收起待处理消息</button>
  </div>, trigger.current?.closest(".v2-shell") ?? document.body);
}
