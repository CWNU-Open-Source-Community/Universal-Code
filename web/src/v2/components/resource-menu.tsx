import { useLayoutEffect, useRef, useState, type ReactNode } from "react";
import { createPortal } from "react-dom";
import { Copy, Download, File, FolderOpen, SquareArrowOutUpRight } from "lucide-react";
import { resourceAction, type ChatResourceRef } from "../../lib/chat-resources";
import "./resource-links.css";

export interface ResourceMenuItem { label: string; icon?: ReactNode; action: () => void | Promise<unknown>; divider?: boolean; disabled?: boolean }
export interface MenuPosition { x: number; y: number; trigger: HTMLElement }
export function fileMenuItems(reference: ChatResourceRef, open: () => void | Promise<unknown>): ResourceMenuItem[] {
  return [
    { label: "打开文件", icon: <File />, action: open },
    { label: "在资源管理器中显示", icon: <FolderOpen />, action: () => resourceAction(reference, "reveal") },
    { label: "打开方式…", icon: <SquareArrowOutUpRight />, action: () => resourceAction(reference, "open_with") },
    { label: "另存为…", icon: <Download />, divider: true, action: () => resourceAction(reference, "save_as") },
    { label: "复制文件", icon: <Copy />, action: () => resourceAction(reference, "copy_file") },
    ...(reference.path ? [{ label: "复制路径", action: () => resourceAction(reference, "copy_path") }] : []),
    { label: "复制文件内容", action: () => resourceAction(reference, "copy_content") },
  ];
}

export function ResourceMenu({ position, items, onClose, onResult }: {
  position: MenuPosition; items: ResourceMenuItem[]; onClose: () => void; onResult: (message: string, error?: boolean) => void;
}) {
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
  return createPortal(<div className="v2-resource-menu" role="menu" aria-label="资源操作" ref={menu} style={{ left: point.x, top: point.y }}
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
          if (item.label.startsWith("复制")) onResult(`已${item.label}`);
          else if (value === "done" && item.label.startsWith("另存")) onResult("已保存文件");
        }).catch((error) => onResult(error instanceof Error ? error.message : "操作失败，请重试。", true)); }
        catch (error) { onResult(error instanceof Error ? error.message : "操作失败，请重试。", true); }
      }}><span className="v2-resource-menu-icon" aria-hidden="true">{item.icon}</span>{item.label}</button>)}
  </div>, position.trigger.closest(".v2-shell") ?? document.body);
}
