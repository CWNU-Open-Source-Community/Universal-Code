import { AlertTriangle, X } from "lucide-react";
import type { RefObject } from "react";
import { createPortal } from "react-dom";
import { useModalFocusTrap } from "../../hooks/use-modal-focus-trap";
import { useLocale } from "../../lib/locale";

export function V2ConfirmDialog({ open, title, description, confirmLabel, danger = false,
  busy = false, returnFocusRef, onCancel, onConfirm }: {
  open: boolean;
  title: string;
  description: string;
  confirmLabel: string;
  danger?: boolean;
  busy?: boolean;
  returnFocusRef?: RefObject<HTMLElement | null>;
  onCancel: () => void;
  onConfirm: () => void;
}) {
  const { t } = useLocale();
  const ref = useModalFocusTrap<HTMLElement>(open, onCancel, busy, undefined, { returnFocusRef });
  if (!open) return null;
  return createPortal(<div className="v2-overlay" role="presentation" onMouseDown={(event) => {
    if (event.target === event.currentTarget && !busy) onCancel();
  }}>
    <section aria-labelledby="v2-confirm-title" aria-modal="true" className="v2-dialog"
      ref={ref} role="dialog" tabIndex={-1}>
      <header><span className={danger ? "danger" : ""}>
        <AlertTriangle aria-hidden="true" size={17} /></span>
        <h2 id="v2-confirm-title">{title}</h2>
        <button aria-label={t("关闭", "Close")} disabled={busy} onClick={onCancel} type="button">
          <X aria-hidden="true" size={16} />
        </button>
      </header>
      <p>{description}</p>
      <footer><button disabled={busy} onClick={onCancel} type="button">{t("取消", "Cancel")}</button>
        <button className={danger ? "danger" : "primary"} disabled={busy}
          onClick={onConfirm} type="button">{busy ? t("正在处理…", "Processing…") : confirmLabel}</button></footer>
    </section>
  </div>, document.body);
}
