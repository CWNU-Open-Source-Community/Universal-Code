import { useEffect, useId, useLayoutEffect, useRef, useState } from "react";
import { createPortal } from "react-dom";
import { useMutation, useQueryClient } from "@tanstack/react-query";
import { Archive, MoreHorizontal, Pencil, Pin, PinOff, X } from "lucide-react";
import type { APIClient } from "../../api/client";
import type { ThreadView } from "../../api/types";
import { useModalFocusTrap } from "../../hooks/use-modal-focus-trap";
import { useLocale } from "../../lib/locale";
import { v2QueryKeys } from "../query-keys";
import "./chat-sidebar.css";

export function ThreadActions({ thread, client, onArchive }: {
  thread: ThreadView; client?: APIClient; onArchive: (thread: ThreadView) => void;
}) {
  const { t } = useLocale();
  const queryClient = useQueryClient();
  const trigger = useRef<HTMLButtonElement>(null);
  const menu = useRef<HTMLDivElement>(null);
  const [open, setOpen] = useState(false);
  const [renaming, setRenaming] = useState(false);
  const [position, setPosition] = useState({ left: 0, top: 0 });
  const close = () => { setOpen(false); trigger.current?.focus(); };
  const mutation = useMutation({
    mutationFn: (change: { title: string } | { pinned: boolean }) => "title" in change
      ? client!.renameThread(thread.id, change.title) : client!.pinThread(thread.id, change.pinned),
    onSuccess: () => {
      setRenaming(false);
      trigger.current?.focus();
      void queryClient.invalidateQueries({ queryKey: ["v2", "threads"] });
      void queryClient.invalidateQueries({ queryKey: v2QueryKeys.thread(thread.id) });
    },
  });
  useLayoutEffect(() => {
    if (!open || !trigger.current || !menu.current) return;
    const bounds = trigger.current.getBoundingClientRect();
    const popup = menu.current.getBoundingClientRect();
    setPosition({ left: Math.max(8, Math.min(bounds.left, window.innerWidth - popup.width - 8)),
      top: Math.max(8, Math.min(bounds.bottom + 4, window.innerHeight - popup.height - 8)) });
    menu.current.querySelector("button")?.focus();
  }, [open]);
  useEffect(() => {
    if (!open) return;
    const outside = (event: Event) => {
      if (event.type === "pointerdown" && (trigger.current?.contains(event.target as Node) || menu.current?.contains(event.target as Node))) return;
      if (event.type === "scroll" && menu.current?.contains(event.target as Node)) return;
      setOpen(false);
    };
    document.addEventListener("pointerdown", outside);
    window.addEventListener("resize", outside);
    window.addEventListener("scroll", outside, true);
    return () => {
      document.removeEventListener("pointerdown", outside);
      window.removeEventListener("resize", outside);
      window.removeEventListener("scroll", outside, true);
    };
  }, [open]);
  return <>
    <button className="v2-thread-more" ref={trigger} type="button" aria-label={t(`${thread.title} 聊天选项`, `${thread.title} chat options`)}
      aria-haspopup="menu" aria-expanded={open} disabled={mutation.isPending} onClick={() => { mutation.reset(); setOpen(!open); }}>
      <MoreHorizontal aria-hidden="true" size={16} /></button>
    {open && createPortal(<div className="v2-thread-menu v2-chat-actions" ref={menu} role="menu" aria-label={t("聊天选项", "Chat options")} style={position}
      onBlur={(event) => { if (!event.currentTarget.contains(event.relatedTarget as Node | null)) setOpen(false); }}
      onKeyDown={(event) => {
        if (event.key === "Escape") { event.preventDefault(); close(); }
        if (["ArrowDown", "ArrowUp", "Home", "End"].includes(event.key)) {
          event.preventDefault();
          const buttons = [...event.currentTarget.querySelectorAll<HTMLButtonElement>("button")];
          const current = buttons.indexOf(document.activeElement as HTMLButtonElement);
          const next = event.key === "Home" ? 0 : event.key === "End" ? buttons.length - 1
            : (current + (event.key === "ArrowDown" ? 1 : -1) + buttons.length) % buttons.length;
          buttons[next]?.focus();
        }
      }}>
      {client?.hasControl && <>
        <button role="menuitem" type="button" onClick={() => { close(); mutation.mutate({ pinned: !thread.pinned }); }}>
          {thread.pinned ? <PinOff aria-hidden="true" size={15} /> : <Pin aria-hidden="true" size={15} />}
          {thread.pinned ? t("取消置顶", "Unpin") : t("置顶聊天", "Pin chat")}</button>
        <button role="menuitem" type="button" onClick={() => { close(); setRenaming(true); }}><Pencil aria-hidden="true" size={15} />{t("重命名", "Rename")}</button>
      </>}
      <button role="menuitem" type="button" aria-label={t(`归档 ${thread.title}`, `Archive ${thread.title}`)} onClick={() => { close(); onArchive(thread); }}>
        <Archive aria-hidden="true" size={15} />{t("归档聊天", "Archive chat")}</button>
    </div>, document.body)}
    {mutation.isError && !renaming && <p className="v2-chat-action-error" role="alert">{mutation.error.message}</p>}
    {renaming && <ThreadRenameDialog thread={thread} pending={mutation.isPending} error={mutation.isError ? mutation.error.message : ""}
      onSave={(title) => mutation.mutate({ title })} onClose={() => { setRenaming(false); mutation.reset(); trigger.current?.focus(); }} />}
  </>;
}

function ThreadRenameDialog({ thread, pending, error, onSave, onClose }: {
  thread: ThreadView; pending: boolean; error: string; onSave: (title: string) => void; onClose: () => void;
}) {
  const { t } = useLocale();
  const [title, setTitle] = useState(thread.title);
  const titleID = useId();
  const input = useRef<HTMLInputElement>(null);
  const dialog = useModalFocusTrap<HTMLElement>(true, () => { if (!pending) onClose(); }, false, input, { isolateBackground: true });
  useEffect(() => { input.current?.select(); }, []);
  return createPortal(<div className="v2-overlay" role="presentation" onMouseDown={(event) => { if (event.target === event.currentTarget && !pending) onClose(); }}>
    <section className="v2-dialog v2-chat-rename" role="dialog" aria-modal="true" aria-labelledby={titleID} ref={dialog} tabIndex={-1}>
      <header><Pencil aria-hidden="true" size={18} /><h2 id={titleID}>{t("重命名聊天", "Rename chat")}</h2>
        <button type="button" disabled={pending} aria-label={t("关闭重命名", "Close rename")} onClick={onClose}><X aria-hidden="true" size={17} /></button></header>
      <form onSubmit={(event) => { event.preventDefault(); if (title.trim() && !pending) onSave(title); }}>
        <label className="v2-chat-rename-field">{t("聊天名称", "Chat name")}<input ref={input} value={title} maxLength={4096} disabled={pending}
          onChange={(event) => setTitle(event.target.value)} required /></label>
        {error && <p className="v2-chat-action-error" role="alert">{error}</p>}
        <footer><button type="button" disabled={pending} onClick={onClose}>{t("取消", "Cancel")}</button>
          <button type="submit" className="primary" disabled={pending || !title.trim()}>{pending ? t("正在保存…", "Saving…") : t("保存", "Save")}</button></footer>
      </form>
    </section>
  </div>, document.body);
}
