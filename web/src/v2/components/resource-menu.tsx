import { useLocale, type LocaleText } from "../../lib/locale";
import { useLayoutEffect, useRef, useState, type ReactNode } from "react";
import { createPortal } from "react-dom";
import { Copy, Download, File, FolderOpen, SquareArrowOutUpRight } from "lucide-react";
import { resourceAction, type ChatResourceRef } from "../../lib/chat-resources";
import "./resource-links.css";

export interface ResourceMenuItem { label: string; icon?: ReactNode; action: () => void | Promise<unknown>; divider?: boolean; disabled?: boolean; successMessage?: string }
export interface MenuPosition { x: number; y: number; trigger: HTMLElement }
export function fileMenuItems(reference: ChatResourceRef, open: () => void | Promise<unknown>, t: LocaleText): ResourceMenuItem[] {
  return [
    { label: t("打开文件", "Open file"), icon: <File />, action: open },
    { label: t("在资源管理器中显示", "Show in file manager"), icon: <FolderOpen />, action: () => resourceAction(reference, "reveal") },
    { label: t("打开方式…", "Open with…"), icon: <SquareArrowOutUpRight />, action: () => resourceAction(reference, "open_with") },
    { label: t("另存为…", "Save as…"), successMessage: t("已保存文件", "File saved"), icon: <Download />, divider: true, action: () => resourceAction(reference, "save_as") },
    { label: t("复制文件", "Copy file"), successMessage: t("已复制文件", "File copied"), icon: <Copy />, action: () => resourceAction(reference, "copy_file") },
    ...(reference.path ? [{ label: t("复制路径", "Copy path"), successMessage: t("已复制路径", "Path copied"), action: () => resourceAction(reference, "copy_path") }] : []),
    { label: t("复制文件内容", "Copy file contents"), successMessage: t("已复制文件内容", "File contents copied"), action: () => resourceAction(reference, "copy_content") },
  ];
}

export function ResourceMenu({ position, items, onClose, onResult }: {
  position: MenuPosition; items: ResourceMenuItem[]; onClose: () => void; onResult: (message: string, error?: boolean) => void;
}) {
  const { t } = useLocale();
  const menu = useRef<HTMLDivElement>(null);
  const [point, setPoint] = useState({ x: position.x, y: position.y });
  useLayoutEffect(() => {
    const element = menu.current; if (!element) return;
    const rect = element.getBoundingClientRect();
    setPoint({ x: Math.max(8, Math.min(position.x, window.innerWidth - rect.width - 8)), y: Math.max(8, Math.min(position.y, window.innerHeight - rect.height - 8)) });
    element.querySelector<HTMLButtonElement>("button:not(:disabled)")?.focus();
    const outside = (event: PointerEvent) => { if (!element.contains(event.target as Node)) onClose(); };
    const close = () => onClose();
    document.addEventListener("pointerdown", outside, true);
    window.addEventListener("resize", close);
    return () => { document.removeEventListener("pointerdown", outside, true); window.removeEventListener("resize", close); };
  }, [position, onClose]);
  const close = () => { onClose(); if (position.trigger.isConnected) position.trigger.focus(); };
  return createPortal(<div className="v2-resource-menu" role="menu" aria-label={t("资源操作", "Resource actions")} ref={menu} style={{ left: point.x, top: point.y }}
    onKeyDown={(event) => {
      if (event.key === "Escape" || event.key === "Tab") { event.preventDefault(); event.stopPropagation(); close(); return; }
      if (!["ArrowDown", "ArrowUp", "Home", "End"].includes(event.key)) return;
      event.preventDefault();
      const buttons = Array.from(menu.current?.querySelectorAll<HTMLButtonElement>("button:not(:disabled)") ?? []);
      const index = buttons.indexOf(document.activeElement as HTMLButtonElement);
      buttons[event.key === "Home" ? 0 : event.key === "End" ? buttons.length - 1 : (index + (event.key === "ArrowDown" ? 1 : -1) + buttons.length) % buttons.length]?.focus();
    }}>
    {items.map((item) => <button key={item.label} className={item.divider ? "has-divider" : undefined} type="button" role="menuitem" tabIndex={-1}
      disabled={item.disabled} onClick={() => {
        close();
        // Invoke synchronously so browser clipboard/popup APIs retain activation.
        try { void Promise.resolve(item.action()).then((value) => {
          if (item.successMessage && value !== "cancelled") onResult(item.successMessage);
        }).catch((error) => onResult(error instanceof Error ? error.message : t("操作失败，请重试。", "The operation failed. Try again."), true)); }
        catch (error) { onResult(error instanceof Error ? error.message : t("操作失败，请重试。", "The operation failed. Try again."), true); }
      }}><span className="v2-resource-menu-icon" aria-hidden="true">{item.icon}</span>{item.label}</button>)}
  </div>, position.trigger.closest(".v2-shell") ?? document.body);
}
