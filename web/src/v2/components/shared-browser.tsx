import { useCallback, useEffect, useId, useRef, useState, type KeyboardEvent, type ReactNode } from "react";
import { createPortal } from "react-dom";
import { ArrowLeft, ArrowRight, Expand, Globe, History, LoaderCircle, Plus, RefreshCw, Shrink, X } from "lucide-react";
import { commandSharedBrowser, getSharedBrowser, openSharedBrowser,
  sharedBrowserNavigationURL, type SharedBrowserCommand, type SharedBrowserOpenRequest, type SharedBrowserState } from "../../lib/desktop-shared-browser";
import { useLocale } from "../../lib/locale";
import { hideUnclaimedSharedBrowser, useSharedBrowserSurface } from "./shared-browser-surface";
import { draftSharedBrowserAdoption, rememberDraftSharedBrowser } from "../shared-browser-session";
import "./shared-browser.css";

export interface SharedBrowserTool { id: string; label: string; icon: ReactNode; onOpen: () => void }

export function sharedWorkspaceTabKey(event: KeyboardEvent<HTMLButtonElement>) {
  if (event.altKey || event.ctrlKey || event.metaKey || !["ArrowLeft", "ArrowRight", "Home", "End"].includes(event.key)) return;
  const tabs = Array.from(event.currentTarget.closest('[role="tablist"]')?.querySelectorAll<HTMLButtonElement>('[role="tab"]:not(:disabled)') ?? []);
  if (!tabs.length) return;
  event.preventDefault();
  const index = tabs.indexOf(event.currentTarget);
  const next = event.key === "Home" ? 0 : event.key === "End" ? tabs.length - 1
    : (index + (event.key === "ArrowLeft" ? -1 : 1) + tabs.length) % tabs.length;
  tabs[next]?.click(); tabs[next]?.focus();
}

export function V2SharedBrowser({ workspaceID, threadID, runID, onFullscreenChange, tabBarTarget, active = true, onActivate, tools = [] }: {
  workspaceID: string; threadID?: string; runID?: string; onFullscreenChange?: (fullscreen: boolean) => void;
  tabBarTarget?: HTMLElement | null; active?: boolean; onActivate?: () => void; tools?: SharedBrowserTool[];
}) {
  const { t } = useLocale();
  const panelID = useId();
  const [state, setState] = useState<SharedBrowserState | null>(null);
  const [error, setError] = useState("");
  const [pending, setPending] = useState(false);
  const [retry, setRetry] = useState(0);
  const [address, setAddress] = useState("");
  const addressRef = useRef<HTMLInputElement>(null);
  const current = useRef<SharedBrowserState | null>(null);
  const generation = useRef(0);
  const lastFullscreen = useRef(false);
  const fullscreenExitRequested = useRef(false);
  const fullscreenChange = useRef(onFullscreenChange);
  fullscreenChange.current = onFullscreenChange;
  useEffect(() => {
    if (!state || state.fullscreen === lastFullscreen.current) return;
    lastFullscreen.current = state.fullscreen;
    fullscreenChange.current?.(state.fullscreen);
  }, [state?.fullscreen]);
  useEffect(() => () => {
    const session = current.current;
    if (!session?.fullscreen) return;
    if (lastFullscreen.current) { lastFullscreen.current = false; fullscreenChange.current?.(false); }
    if (!fullscreenExitRequested.current) void commandSharedBrowser(session.session_id, "exit_fullscreen")
      .catch((error) => console.error("Could not exit shared browser fullscreen", error));
  }, []);
  const reportError = useCallback((error: unknown) => setError(error instanceof Error ? error.message : String(error)), []);
  const accept = useCallback((next: SharedBrowserState) => {
    const previous = current.current;
    if (previous && (previous.session_id !== next.session_id || previous.revision > next.revision)) return;
    current.current = next; setState(next);
    if (!next.fullscreen) fullscreenExitRequested.current = false;
  }, []);
  useEffect(() => {
    const session = current.current;
    if (active || !session?.fullscreen || fullscreenExitRequested.current) return;
    fullscreenExitRequested.current = true;
    void commandSharedBrowser(session.session_id, "exit_fullscreen").then(accept).catch(reportError);
  }, [active, state?.fullscreen, accept, reportError]);
  useEffect(() => {
    const activeGeneration = ++generation.current;
    let disposed = false;
    let timer: ReturnType<typeof setTimeout> | undefined;
    current.current = null; setState(null); setError(""); setPending(false);
    const scope: SharedBrowserOpenRequest = { workspace_id: workspaceID, ...(threadID ? { thread_id: threadID } : {}), ...(runID ? { run_id: runID } : {}),
      adopt_session_id: draftSharedBrowserAdoption(workspaceID, threadID) };
    const refresh = async (sessionID: string) => {
      try { const next = await getSharedBrowser(sessionID); if (!disposed) accept(next); }
      catch (error) { if (!disposed) reportError(error); }
      if (!disposed) timer = setTimeout(() => void refresh(sessionID), 450);
    };
    void openSharedBrowser(scope).then((next) => {
      rememberDraftSharedBrowser(next);
      if (disposed || generation.current !== activeGeneration) {
        void hideUnclaimedSharedBrowser(next).catch(reportError); return;
      }
      accept(next); timer = setTimeout(() => void refresh(next.session_id), 450);
    }).catch((error) => { if (!disposed) reportError(error); });
    return () => { disposed = true; clearTimeout(timer); generation.current += 1; };
  }, [workspaceID, threadID, runID, retry, accept, reportError]);
  const activeTab = state?.tabs.find((tab) => tab.tab_id === state.active_tab_id);
  const isStartPage = !activeTab || activeTab.url === "about:blank";
  const surfaceRef = useSharedBrowserSurface(state, reportError, active && !isStartPage);
  const tabDOMID = (tabID: string) => `${panelID}-tab-${encodeURIComponent(tabID)}`;
  useEffect(() => {
    if (document.activeElement !== addressRef.current) setAddress(activeTab?.url === "about:blank" ? "" : activeTab?.url ?? "");
  }, [activeTab?.url, activeTab?.tab_id]);
  useEffect(() => {
    if (active && isStartPage && state?.state === "ready") addressRef.current?.focus();
  }, [active, activeTab?.tab_id, isStartPage, state?.state]);
  const command = async (name: SharedBrowserCommand, options: { tab_id?: string; url?: string } = {}) => {
    const session = current.current;
    if (!session || pending) return;
    const activeGeneration = generation.current;
    setPending(true); setError("");
    try { const next = await commandSharedBrowser(session.session_id, name, options); if (generation.current === activeGeneration) accept(next); }
    catch (error) { if (generation.current === activeGeneration) reportError(error); }
    finally { if (generation.current === activeGeneration) setPending(false); }
  };
  const canEdit = state?.state === "ready" && !pending;
  const navigate = (url: string) => {
    onActivate?.(); setAddress(url); addressRef.current?.blur();
    void command(activeTab ? "navigate" : "new_tab", { ...(activeTab ? { tab_id: activeTab.tab_id } : {}), url });
  };
  const tabStrip = <>
    {state?.tabs.map((tab) => {
      const selected = active && tab.tab_id === state.active_tab_id;
      const title = tab.url === "about:blank" ? t("新标签页", "New tab") : tab.title || tab.url;
      return <div className="v2-workspace-tab-item v2-browser-tab" data-active={selected} key={tab.tab_id}>
        <button type="button" className="v2-workspace-tab-trigger" role="tab" data-native-tab-id={tab.tab_id} id={tabDOMID(tab.tab_id)} aria-controls={panelID}
          aria-selected={selected} tabIndex={selected ? 0 : -1} disabled={!canEdit} title={title}
          onClick={() => { onActivate?.(); if (tab.tab_id !== state.active_tab_id) void command("select_tab", { tab_id: tab.tab_id }); }} onKeyDown={sharedWorkspaceTabKey}>
          {tab.loading ? <LoaderCircle size={14} aria-hidden="true" className="v2-browser-spinner" /> : <Globe size={14} aria-hidden="true" />}
          <span className="v2-workspace-tab-title">{title}</span>
        </button><button type="button" className="v2-workspace-tab-close" disabled={!canEdit} aria-label={t(`关闭 ${title}`, `Close ${title}`)}
          onClick={() => void command("close_tab", { tab_id: tab.tab_id })}><X size={13} aria-hidden="true" /></button>
      </div>;
    })}
    <button type="button" className="v2-workspace-icon-btn v2-browser-new-tab" disabled={!canEdit} aria-label={t("新建浏览器标签页", "New browser tab")}
      onClick={() => { onActivate?.(); void command("new_tab", { url: "about:blank" }); }}><Plus size={17} aria-hidden="true" /></button>
  </>;
  const history = state?.history?.filter((entry, index, entries) => entries.findIndex((item) => item.url === entry.url) === index).slice(0, 8) ?? [];
  return <div className="v2-shared-browser" role="tabpanel" id={panelID} aria-labelledby={activeTab ? tabDOMID(activeTab.tab_id) : undefined}
    onKeyDownCapture={(event) => {
      if ((event.ctrlKey || event.metaKey) && event.key.toLowerCase() === "l") { event.preventDefault(); addressRef.current?.focus(); addressRef.current?.select(); }
    }}>
    {tabBarTarget === undefined ? <div className="v2-workspace-tabbar"><div className="v2-workspace-tablist" role="tablist" aria-label={t("浏览器标签页", "Browser tabs")}>{tabStrip}</div></div>
      : tabBarTarget ? createPortal(tabStrip, tabBarTarget) : null}
    <div className="v2-browser-toolbar">
      <div className="v2-browser-navigation" role="group" aria-label={t("网页导航", "Page navigation")}>
        <button type="button" className="v2-browser-icon" disabled={!canEdit || !activeTab?.can_go_back} aria-label={t("后退", "Back")}
          onClick={() => void command("back", { tab_id: activeTab?.tab_id })}><ArrowLeft size={17} aria-hidden="true" /></button>
        <button type="button" className="v2-browser-icon" disabled={!canEdit || !activeTab?.can_go_forward} aria-label={t("前进", "Forward")}
          onClick={() => void command("forward", { tab_id: activeTab?.tab_id })}><ArrowRight size={17} aria-hidden="true" /></button>
        <button type="button" className="v2-browser-icon" disabled={!canEdit || !activeTab} aria-label={activeTab?.loading ? t("停止加载", "Stop loading") : t("重新加载", "Reload")}
          onClick={() => void command(activeTab?.loading ? "stop" : "reload", { tab_id: activeTab?.tab_id })}>{activeTab?.loading ? <X size={17} aria-hidden="true" /> : <RefreshCw size={16} aria-hidden="true" />}</button>
      </div>
      <form className="v2-browser-address" onSubmit={(event) => {
        event.preventDefault();
        const url = sharedBrowserNavigationURL(address);
        if (!url) { setError(t("请输入 http://、https:// 地址或 about:blank。", "Enter an http:// or https:// address, or about:blank.")); return; }
        navigate(url);
      }}><input aria-label={t("浏览器地址", "Browser address")} placeholder={t("输入网址", "Enter a URL")} autoComplete="off" spellCheck={false} disabled={!canEdit}
        ref={addressRef} value={address} onChange={(event) => setAddress(event.target.value)} onFocus={(event) => event.target.select()}
        onKeyDown={(event) => { if (event.key === "Escape") { event.preventDefault(); event.stopPropagation(); setAddress(isStartPage ? "" : activeTab?.url ?? ""); event.currentTarget.blur(); } }} /></form>
      <button type="button" className="v2-browser-icon" disabled={!state || pending || state.state !== "ready"}
        aria-label={state?.fullscreen ? t("退出全屏", "Exit fullscreen") : t("全屏浏览器", "Fullscreen browser")}
        onClick={() => void command(state?.fullscreen ? "exit_fullscreen" : "enter_fullscreen")}>{state?.fullscreen ? <Shrink size={17} aria-hidden="true" /> : <Expand size={17} aria-hidden="true" />}</button>
    </div>
    {error && <div className="v2-browser-error" role="alert">{error}<button type="button" onClick={() => current.current ? void getSharedBrowser(current.current.session_id).then(accept).catch(reportError) : setRetry((value) => value + 1)}>{t("重试", "Retry")}</button></div>}
    <div className={`v2-browser-native-surface${isStartPage ? " is-start-page" : ""}`} ref={surfaceRef} role="region" aria-label={t("网页内容", "Web page content")}>
      {!state && !error && <span role="status">{t("正在打开浏览器…", "Opening browser…")}</span>}
      {state?.state === "failed" && <span role="alert">{state.error || t("浏览器启动失败。", "Browser failed to start.")}</span>}
      {state?.state === "ready" && isStartPage && <section className="v2-browser-start" aria-label={t("新标签页", "New tab")}>
        {tools.length > 0 && <section className="v2-browser-tools-section"><h2>{t("工具", "Tools")}</h2>
          <div className="v2-browser-tools">{tools.map((tool) => <button type="button" key={tool.id} onClick={tool.onOpen}>{tool.icon}<span>{tool.label}</span></button>)}</div>
        </section>}
        {history.length > 0 && <section className="v2-browser-recent" aria-label={t("最近访问", "Recently visited")}>
          <h2><History size={14} aria-hidden="true" />{t("最近", "Recent")}</h2>
          {history.map((entry) => <button type="button" key={entry.url} disabled={!canEdit} onClick={() => navigate(entry.url)} title={entry.url}>
            <Globe size={15} aria-hidden="true" /><span>{entry.title || new URL(entry.url).hostname}</span><small>{new URL(entry.url).hostname}</small>
          </button>)}
        </section>}
      </section>}
    </div>
  </div>;
}
