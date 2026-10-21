import { useEffect, useRef, useState, type CSSProperties, type KeyboardEvent, type PointerEvent, type ReactNode, type RefObject } from "react";
import { useLocale } from "../../lib/locale";
import "./sidebar-layout.css";

export const sidebarWidthKey = "universal-code.sidebar-width.v1";
const defaultWidth = 299;
const minimumWidth = 232;
const maximumWidth = 420;
const clampWidth = (width: number, maximum = maximumWidth) => Math.max(minimumWidth, Math.min(maximum, Math.round(width)));

function readWidth() {
  try {
    const value = Number(localStorage.getItem(sidebarWidthKey));
    return Number.isFinite(value) && value >= minimumWidth && value <= maximumWidth ? value : defaultWidth;
  } catch { return defaultWidth; }
}

function saveWidth(width: number) {
  try { localStorage.setItem(sidebarWidthKey, String(width)); }
  catch { /* A display preference must not block the workbench. */ }
}

export function V2SidebarLayout({ sidebar, children, sidebarRef, visible, narrow }: {
  sidebar: ReactNode;
  children: ReactNode;
  sidebarRef: RefObject<HTMLDivElement | null>;
  visible: boolean;
  narrow: boolean;
}) {
  const { t } = useLocale();
  const [preferredWidth, setPreferredWidth] = useState(readWidth);
  const [viewportWidth, setViewportWidth] = useState(() => window.innerWidth);
  const [resizing, setResizing] = useState(false);
  const drag = useRef<{ pointerID: number; element: HTMLDivElement; originX: number; originWidth: number; preferred: number } | null>(null);
  const active = visible && !narrow;
  // Leave room for the conversation. A temporary window squeeze does not replace the saved width.
  const maximum = Math.max(minimumWidth, Math.min(maximumWidth, viewportWidth - 420));
  const width = clampWidth(preferredWidth, maximum);

  const cancelDrag = () => {
    if (!drag.current) return;
    const current = drag.current;
    setPreferredWidth(current.preferred);
    drag.current = null;
    setResizing(false);
    if (current.element.hasPointerCapture(current.pointerID)) current.element.releasePointerCapture(current.pointerID);
  };
  useEffect(() => {
    const onResize = () => { cancelDrag(); setViewportWidth(window.innerWidth); };
    window.addEventListener("resize", onResize);
    window.addEventListener("blur", cancelDrag);
    return () => {
      window.removeEventListener("resize", onResize);
      window.removeEventListener("blur", cancelDrag);
    };
  }, []);
  useEffect(() => { if (!active) cancelDrag(); }, [active]);

  const changeWidth = (next: number) => {
    const value = clampWidth(next, maximum);
    setPreferredWidth(value);
    saveWidth(value);
  };
  const onKeyDown = (event: KeyboardEvent<HTMLDivElement>) => {
    if (event.isDefaultPrevented() || event.nativeEvent.isComposing || event.ctrlKey || event.metaKey || event.altKey) return;
    if (event.key === "Escape" && drag.current) {
      event.preventDefault(); cancelDrag(); return;
    }
    if (drag.current) return;
    const next = event.key === "ArrowLeft" ? width - 12 : event.key === "ArrowRight" ? width + 12
      : event.key === "Home" ? minimumWidth : event.key === "End" ? maximum
        : event.key === "Enter" ? defaultWidth : undefined;
    if (next !== undefined) { event.preventDefault(); changeWidth(next); }
  };
  const onPointerDown = (event: PointerEvent<HTMLDivElement>) => {
    if (event.button !== 0 || drag.current) return;
    event.preventDefault();
    event.currentTarget.focus();
    drag.current = { pointerID: event.pointerId, element: event.currentTarget, originX: event.clientX, originWidth: width, preferred: preferredWidth };
    event.currentTarget.setPointerCapture(event.pointerId);
    setResizing(true);
  };
  const onPointerMove = (event: PointerEvent<HTMLDivElement>) => {
    if (!drag.current || drag.current.pointerID !== event.pointerId) return;
    const next = clampWidth(drag.current.originWidth + event.clientX - drag.current.originX, maximum);
    setPreferredWidth(next);
  };
  const onPointerUp = (event: PointerEvent<HTMLDivElement>) => {
    if (!drag.current || drag.current.pointerID !== event.pointerId) return;
    const next = clampWidth(drag.current.originWidth + event.clientX - drag.current.originX, maximum);
    if (next === drag.current.originWidth) setPreferredWidth(drag.current.preferred);
    else { setPreferredWidth(next); saveWidth(next); }
    drag.current = null;
    setResizing(false);
    if (event.currentTarget.hasPointerCapture(event.pointerId)) event.currentTarget.releasePointerCapture(event.pointerId);
  };

  return <div className="v2-shell-body v2-sidebar-layout" data-resizing={resizing} style={{ "--v2-sidebar-width": `${width}px` } as CSSProperties}>
    <div className="v2-sidebar-slot" id="v2-sidebar-panel" ref={sidebarRef} aria-hidden={!visible} inert={!visible}>
      {sidebar}
      <div role="separator" className="v2-sidebar-resize-handle" tabIndex={active ? 0 : -1} hidden={!active}
        aria-label={t("调整侧栏宽度", "Resize sidebar")} aria-controls="v2-sidebar-panel" aria-orientation="vertical"
        aria-valuemin={minimumWidth} aria-valuemax={maximum} aria-valuenow={width}
        aria-valuetext={t(`${width} 像素`, `${width} pixels`)}
        title={t("拖动调整侧栏宽度；双击或 Enter 恢复默认", "Drag to resize the sidebar; double-click or Enter to reset")}
        onKeyDown={onKeyDown} onDoubleClick={() => changeWidth(defaultWidth)}
        onPointerDown={onPointerDown} onPointerMove={onPointerMove} onPointerUp={onPointerUp}
        onPointerCancel={(event) => { if (drag.current?.pointerID === event.pointerId) cancelDrag(); }}
        onLostPointerCapture={(event) => { if (drag.current?.pointerID === event.pointerId) cancelDrag(); }} />
    </div>
    {children}
  </div>;
}
