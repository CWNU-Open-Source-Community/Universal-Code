import { useEffect, useRef, useState, type CSSProperties, type ReactNode } from "react";
import { Globe, Maximize2, Minimize2, PanelRightClose } from "lucide-react";
import { desktopSharedBrowserEnabled } from "../../lib/desktop-bridge";
import { useLocale } from "../../lib/locale";
import { clampWorkspaceWidth, collapseWorkspacePanel, defaultWorkspaceWidth, focusWorkspaceSingletonTab,
  minWorkspaceWidth, setWorkspaceWidth, setWorkspaceFullView, toggleWorkspaceFullView, useThreadWorkspaceState } from "../workspace-tabs";
import { V2SharedBrowser, type SharedBrowserTool } from "./shared-browser";
import "./workspace-dock.css";

export function V2DraftBrowserWorkspace({ workspaceID, children, tools }: {
  workspaceID: string; children: (trigger: ReactNode) => ReactNode; tools?: SharedBrowserTool[];
}) {
  const { t } = useLocale();
  const scope = `draft:${workspaceID}`;
  const state = useThreadWorkspaceState(scope);
  const stage = useRef<HTMLDivElement>(null);
  const trigger = useRef<HTMLButtonElement>(null);
  const beforeFullscreen = useRef<boolean | null>(null);
  const [compact, setCompact] = useState(false);
  const [tabTarget, setTabTarget] = useState<HTMLDivElement | null>(null);
  const [liveWidth, setLiveWidth] = useState<number | null>(null);
  const drag = useRef<{ pointerID: number; startX: number; startWidth: number } | null>(null);
  const available = desktopSharedBrowserEnabled() && Boolean(workspaceID);
  const open = available && state.open && state.activeTabID === "browser";
  const fullView = open && (compact || state.fullView);
  useEffect(() => {
    if (!stage.current) return;
    const observer = new ResizeObserver(() => setCompact((stage.current?.clientWidth ?? 0) < 760));
    observer.observe(stage.current);
    return () => observer.disconnect();
  }, [available]);
  useEffect(() => {
    const cancel = () => { drag.current = null; setLiveWidth(null); };
    window.addEventListener("blur", cancel);
    window.addEventListener("resize", cancel);
    return () => { window.removeEventListener("blur", cancel); window.removeEventListener("resize", cancel); };
  }, []);
  useEffect(() => { drag.current = null; setLiveWidth(null); }, [workspaceID, fullView]);
  if (!available) return children(null);
  const close = () => { collapseWorkspacePanel(scope); requestAnimationFrame(() => trigger.current?.focus()); };
  const maximum = () => Math.max(minWorkspaceWidth, (stage.current?.clientWidth ?? window.innerWidth) - 340);
  const resize = (x: number) => clampWorkspaceWidth((drag.current?.startWidth ?? state.width) + (drag.current?.startX ?? x) - x, maximum());
  return <div className={`v2-conversation-stage v2-draft-browser-stage${fullView ? " is-full-view" : ""}`} ref={stage}>
    <div className="v2-conversation-main-column" aria-hidden={fullView || undefined} inert={fullView}>
      {children(<button type="button" ref={trigger} className="v2-draft-browser-trigger" aria-pressed={open} onClick={() => open ? close() : focusWorkspaceSingletonTab(scope, "browser")}>
        <Globe aria-hidden="true" size={16} />{t("浏览器", "Browser")}</button>)}
    </div>
    <div className={`v2-workbench-slot${open ? " is-open" : ""}`} data-resizing={liveWidth !== null || undefined}
      style={{ "--v2-workbench-width": `${liveWidth ?? state.width}px` } as CSSProperties}>
      {open && <section className={`v2-workspace-dock is-docked${fullView ? " is-full-view" : ""}`} role="complementary" aria-label={t("浏览器", "Browser")}>
        {!fullView && <div className="v2-workspace-resize-handle" role="separator" aria-label={t("调整浏览器宽度", "Resize browser")}
          aria-orientation="vertical" aria-valuemin={minWorkspaceWidth} aria-valuemax={maximum()} aria-valuenow={state.width} tabIndex={0}
          onPointerDown={(event) => {
            if (event.button !== 0) return;
            event.preventDefault(); event.currentTarget.focus();
            drag.current = { pointerID: event.pointerId, startX: event.clientX, startWidth: clampWorkspaceWidth(state.width, maximum()) };
            event.currentTarget.setPointerCapture(event.pointerId); setLiveWidth(drag.current.startWidth);
          }} onPointerMove={(event) => { if (drag.current?.pointerID === event.pointerId) setLiveWidth(resize(event.clientX)); }}
          onPointerUp={(event) => {
            if (drag.current?.pointerID !== event.pointerId) return;
            setWorkspaceWidth(scope, resize(event.clientX)); drag.current = null; setLiveWidth(null);
            event.currentTarget.releasePointerCapture(event.pointerId);
          }} onPointerCancel={() => { drag.current = null; setLiveWidth(null); }}
          onLostPointerCapture={() => { drag.current = null; setLiveWidth(null); }}
          onKeyDown={(event) => {
            const next = event.key === "ArrowLeft" ? state.width + 16 : event.key === "ArrowRight" ? state.width - 16
              : event.key === "Home" ? minWorkspaceWidth : event.key === "End" ? maximum() : event.key === "Enter" ? defaultWorkspaceWidth : undefined;
            if (next !== undefined) { event.preventDefault(); setWorkspaceWidth(scope, clampWorkspaceWidth(next, maximum())); }
          }} />}
        <header className="v2-workspace-tabbar">
          <div className="v2-workspace-tabs-track"><div className="v2-workspace-tablist" role="tablist" aria-label={t("工作区标签", "Workspace tabs")} ref={setTabTarget} /></div>
          <div className="v2-workspace-window-actions">
            {!compact && <button type="button" className="v2-workspace-icon-btn" aria-label={state.fullView ? t("退出完整视图", "Exit full view") : t("进入完整视图", "Enter full view")}
              onClick={() => toggleWorkspaceFullView(scope)}>{state.fullView ? <Minimize2 size={15} aria-hidden="true" /> : <Maximize2 size={15} aria-hidden="true" />}</button>}
            <button type="button" className="v2-workspace-icon-btn" aria-label={t("收起浏览器", "Collapse browser")} onClick={close}><PanelRightClose size={16} aria-hidden="true" /></button>
          </div>
        </header>
        <div className="v2-workspace-dock-body"><V2SharedBrowser key={workspaceID} workspaceID={workspaceID} tabBarTarget={tabTarget} tools={tools} onFullscreenChange={(fullscreen) => {
          if (fullscreen) { beforeFullscreen.current = state.fullView; setWorkspaceFullView(scope, true); }
          else if (beforeFullscreen.current !== null) { setWorkspaceFullView(scope, beforeFullscreen.current); beforeFullscreen.current = null; }
        }} /></div>
      </section>}
    </div>
  </div>;
}
