import { useCallback, useEffect, useRef, useState, type KeyboardEvent } from "react";
import { ArrowLeft, ArrowRight, Expand, LoaderCircle, Plus, RefreshCw, Shrink, X } from "lucide-react";
import { commandSharedBrowser, getSharedBrowser, openSharedBrowser,
  sharedBrowserNavigationURL, type SharedBrowserCommand, type SharedBrowserOpenRequest, type SharedBrowserState } from "../../lib/desktop-shared-browser";
import { useLocale } from "../../lib/locale";
import { hideUnclaimedSharedBrowser, useSharedBrowserSurface } from "./shared-browser-surface";
import { draftSharedBrowserAdoption, rememberDraftSharedBrowser } from "../shared-browser-session";
import "./shared-browser.css";

export function V2SharedBrowser({ workspaceID, threadID, runID, onFullscreenChange }: {
  workspaceID: string; threadID?: string; runID?: string; onFullscreenChange?: (fullscreen: boolean) => void;
}) {
  const { t } = useLocale();
  const [state, setState] = useState<SharedBrowserState | null>(null);
  const [error, setError] = useState("");
  const [pending, setPending] = useState(false);
  const [retry, setRetry] = useState(0);
  const [address, setAddress] = useState("");
  const addressRef = useRef<HTMLInputElement>(null);
  const current = useRef<SharedBrowserState | null>(null);
  const generation = useRef(0);
  const lastFullscreen = useRef(false);
  const fullscreenChange = useRef(onFullscreenChange);
  fullscreenChange.current = onFullscreenChange;
  useEffect(() => {
    if (!state || state.fullscreen === lastFullscreen.current) return;
    lastFullscreen.current = state.fullscreen;
    fullscreenChange.current?.(state.fullscreen);
  }, [state?.fullscreen]);
  const reportError = useCallback((error: unknown) => setError(error instanceof Error ? error.message : String(error)), []);
  const accept = useCallback((next: SharedBrowserState) => {
    const previous = current.current;
    if (previous && (previous.session_id !== next.session_id || previous.revision > next.revision)) return;
    current.current = next;
    setState(next);
  }, []);
  useEffect(() => {
    const active = ++generation.current;
    let disposed = false;
    let timer: ReturnType<typeof setTimeout> | undefined;
    current.current = null; setState(null); setError(""); setPending(false);
    const scope: SharedBrowserOpenRequest = { workspace_id: workspaceID, ...(threadID ? { thread_id: threadID } : {}), ...(runID ? { run_id: runID } : {}),
      adopt_session_id: draftSharedBrowserAdoption(workspaceID, threadID) };
    const refresh = async (sessionID: string) => {
      try {
        const next = await getSharedBrowser(sessionID);
        if (!disposed) accept(next);
      } catch (error) { if (!disposed) reportError(error); }
      if (!disposed) timer = setTimeout(() => void refresh(sessionID), 450);
    };
    void openSharedBrowser(scope).then((next) => {
      rememberDraftSharedBrowser(next);
      if (disposed || generation.current !== active) {
        // Opening can finish after a workspace switch; that view must stay hidden.
        void hideUnclaimedSharedBrowser(next).catch(reportError);
        return;
      }
      accept(next);
      timer = setTimeout(() => void refresh(next.session_id), 450);
    }).catch((error) => { if (!disposed) reportError(error); });
    return () => { disposed = true; clearTimeout(timer); generation.current += 1; };
  }, [workspaceID, threadID, runID, retry, accept, reportError]);
  const surfaceRef = useSharedBrowserSurface(state, reportError);
  const activeTab = state?.tabs.find((tab) => tab.tab_id === state.active_tab_id);
  useEffect(() => {
    if (document.activeElement !== addressRef.current) setAddress(activeTab?.url ?? "");
  }, [activeTab?.url, activeTab?.tab_id]);
  const command = async (name: SharedBrowserCommand, options: { tab_id?: string; url?: string } = {}) => {
    const session = current.current;
    if (!session || pending) return;
    const active = generation.current;
    setPending(true); setError("");
    try {
      const next = await commandSharedBrowser(session.session_id, name, options);
      if (generation.current === active) accept(next);
    } catch (error) { if (generation.current === active) reportError(error); }
    finally { if (generation.current === active) setPending(false); }
  };
  const canEdit = state?.state === "ready" && !pending;
  const tabKey = (event: KeyboardEvent<HTMLButtonElement>, index: number) => {
    if (!state || !["ArrowLeft", "ArrowRight", "Home", "End"].includes(event.key)) return;
    event.preventDefault();
    const nextIndex = event.key === "Home" ? 0 : event.key === "End" ? state.tabs.length - 1
      : (index + (event.key === "ArrowLeft" ? -1 : 1) + state.tabs.length) % state.tabs.length;
    const target = state.tabs[nextIndex];
    void command("select_tab", { tab_id: target.tab_id });
    event.currentTarget.closest('[role="tablist"]')?.querySelectorAll<HTMLButtonElement>('[role="tab"]')[nextIndex]?.focus();
  };
  return <div className="v2-shared-browser">
    <div className="v2-browser-tabs-row">
      <div className="v2-browser-tabs" role="tablist" aria-label={t("浏览器标签页", "Browser tabs")}>
        {state?.tabs.map((tab, index) => <div className="v2-browser-tab" data-active={tab.tab_id === state.active_tab_id} key={tab.tab_id}>
          <button type="button" role="tab" aria-selected={tab.tab_id === state.active_tab_id} tabIndex={tab.tab_id === state.active_tab_id ? 0 : -1}
            disabled={!canEdit} onClick={() => void command("select_tab", { tab_id: tab.tab_id })} onKeyDown={(event) => tabKey(event, index)} title={tab.title || tab.url}>
            {tab.loading && <LoaderCircle size={12} aria-hidden="true" className="v2-browser-spinner" />}<span>{tab.title || t("新标签页", "New tab")}</span>
          </button><button type="button" disabled={!canEdit} aria-label={t(`关闭 ${tab.title || "标签页"}`, `Close ${tab.title || "tab"}`)}
            onClick={() => void command("close_tab", { tab_id: tab.tab_id })}><X size={12} aria-hidden="true" /></button>
        </div>)}
      </div>
      <button type="button" className="v2-browser-icon" disabled={!canEdit} aria-label={t("新建浏览器标签页", "New browser tab")}
        onClick={() => void command("new_tab", { url: "about:blank" })}><Plus size={15} aria-hidden="true" /></button>
    </div>
    <div className="v2-browser-toolbar">
      <button type="button" className="v2-browser-icon" disabled={!canEdit || !activeTab?.can_go_back} aria-label={t("后退", "Back")}
        onClick={() => void command("back", { tab_id: activeTab?.tab_id })}><ArrowLeft size={15} aria-hidden="true" /></button>
      <button type="button" className="v2-browser-icon" disabled={!canEdit || !activeTab?.can_go_forward} aria-label={t("前进", "Forward")}
        onClick={() => void command("forward", { tab_id: activeTab?.tab_id })}><ArrowRight size={15} aria-hidden="true" /></button>
      <button type="button" className="v2-browser-icon" disabled={!canEdit || !activeTab} aria-label={activeTab?.loading ? t("停止加载", "Stop loading") : t("重新加载", "Reload")}
        onClick={() => void command(activeTab?.loading ? "stop" : "reload", { tab_id: activeTab?.tab_id })}>{activeTab?.loading ? <X size={15} aria-hidden="true" /> : <RefreshCw size={15} aria-hidden="true" />}</button>
      <form className="v2-browser-address" onSubmit={(event) => {
        event.preventDefault();
        const url = sharedBrowserNavigationURL(address);
        if (!url) { setError(t("请输入 http://、https:// 地址或 about:blank。", "Enter an http:// or https:// address, or about:blank.")); return; }
        setAddress(url); addressRef.current?.blur(); void command("navigate", { tab_id: activeTab?.tab_id, url });
      }}><input aria-label={t("浏览器地址", "Browser address")} autoComplete="off" spellCheck={false} disabled={!canEdit || !activeTab}
        ref={addressRef} value={address} onChange={(event) => setAddress(event.target.value)} onFocus={(event) => event.target.select()}
        onKeyDown={(event) => { if (event.key === "Escape") { event.preventDefault(); event.stopPropagation(); setAddress(activeTab?.url ?? ""); event.currentTarget.blur(); } }} /></form>
      <button type="button" className="v2-browser-icon" disabled={!state || pending || state.state !== "ready"}
        aria-label={state?.fullscreen ? t("退出全屏", "Exit fullscreen") : t("全屏浏览器", "Fullscreen browser")}
        onClick={() => void command(state?.fullscreen ? "exit_fullscreen" : "enter_fullscreen")}>{state?.fullscreen ? <Shrink size={15} aria-hidden="true" /> : <Expand size={15} aria-hidden="true" />}</button>
    </div>
    {error && <div className="v2-browser-error" role="alert">{error}<button type="button" onClick={() => current.current ? void getSharedBrowser(current.current.session_id).then(accept).catch(reportError) : setRetry((value) => value + 1)}>{t("重试", "Retry")}</button></div>}
    <div className="v2-browser-native-surface" ref={surfaceRef} role="region" aria-label={t("网页内容", "Web page content")}>
      {!state && !error && <span role="status">{t("正在打开浏览器…", "Opening browser…")}</span>}
      {state?.state === "failed" && <span role="alert">{state.error || t("浏览器启动失败。", "Browser failed to start.")}</span>}
    </div>
  </div>;
}
