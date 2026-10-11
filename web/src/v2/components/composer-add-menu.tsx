import { useEffect, useId, useRef, useState, type ReactNode, type RefObject } from "react";
import { Plus } from "lucide-react";

export interface ComposerAddAction {
  id: string;
  label: string;
  detail: string;
  icon: ReactNode;
  disabled?: boolean;
  onSelect: () => void;
}

export function V2ComposerAddMenu({ actions, disabled, triggerRef }: {
  actions: ComposerAddAction[];
  disabled: boolean;
  triggerRef: RefObject<HTMLButtonElement | null>;
}) {
  const [open, setOpen] = useState(false);
  const shellRef = useRef<HTMLDivElement>(null);
  const menuRef = useRef<HTMLDivElement>(null);
  const focusLastRef = useRef(false);
  const tabbingRef = useRef(false);
  const id = useId();
  const close = (restoreFocus = false) => {
    if (restoreFocus) triggerRef.current?.focus();
    setOpen(false);
  };
  useEffect(() => { if (disabled) setOpen(false); }, [disabled]);
  useEffect(() => {
    if (!open) return;
    tabbingRef.current = false;
    const items = Array.from(menuRef.current?.querySelectorAll<HTMLButtonElement>('button:not(:disabled)') ?? []);
    (items[focusLastRef.current ? items.length - 1 : 0] ?? menuRef.current)?.focus();
    const outside = (event: MouseEvent) => {
      if (!shellRef.current?.contains(event.target as Node)) {
        setOpen(false);
      }
    };
    window.addEventListener("click", outside);
    return () => window.removeEventListener("click", outside);
  }, [open]);
  if (actions.length === 0) return null;
  return <div className="v2-composer-add" ref={shellRef}>
    <button aria-label="添加附件" aria-expanded={open} aria-haspopup="menu" aria-controls={open ? id : undefined}
      className="v2-composer-icon v2-composer-add-trigger" disabled={disabled} ref={triggerRef}
      title="添加图片、文件或项目引用" type="button" onClick={() => { focusLastRef.current = false; setOpen((value) => !value); }}
      onKeyDown={(event) => {
        if (event.key === "ArrowDown" || event.key === "ArrowUp") {
          event.preventDefault(); focusLastRef.current = event.key === "ArrowUp"; setOpen(true);
        }
      }}><Plus aria-hidden="true" size={20} /></button>
    {open && <div aria-label="添加内容" className="v2-composer-add-menu" id={id} ref={menuRef} role="menu" tabIndex={-1}
      onBlur={(event) => {
        if (!event.currentTarget.contains(event.relatedTarget as Node | null) &&
          (event.relatedTarget !== triggerRef.current || tabbingRef.current)) setOpen(false);
      }}
      onKeyDown={(event) => {
        if (event.key === "Tab") { tabbingRef.current = true; return; }
        if (event.key === "Escape") { event.preventDefault(); event.stopPropagation(); close(true); return; }
        if (!["ArrowDown", "ArrowUp", "Home", "End"].includes(event.key)) return;
        event.preventDefault(); event.stopPropagation();
        const items = Array.from(menuRef.current?.querySelectorAll<HTMLButtonElement>('button:not(:disabled)') ?? []);
        const current = items.indexOf(document.activeElement as HTMLButtonElement);
        const next = event.key === "Home" ? 0 : event.key === "End" ? items.length - 1
          : (current + (event.key === "ArrowDown" ? 1 : -1) + items.length) % items.length;
        items[next]?.focus();
      }}>
      <div className="v2-composer-add-heading" role="presentation">添加到对话</div>
      {actions.map((action) => <button aria-label={action.label} aria-describedby={`${id}-${action.id}`}
        disabled={action.disabled} key={action.id} role="menuitem" tabIndex={-1} type="button"
        onClick={() => { close(true); action.onSelect(); }}>
        {action.icon}<span><strong>{action.label}</strong><small id={`${id}-${action.id}`}>{action.detail}</small></span>
      </button>)}
    </div>}
  </div>;
}
