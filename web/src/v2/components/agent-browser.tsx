import { useEffect, useRef, useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { ExternalLink, Maximize2, RefreshCw, X } from "lucide-react";
import { useLocale, type LocaleText } from "../../lib/locale";
import type { APIClient } from "../../api/client";
import { agentBrowserAPIAbsent, agentBrowserQueryKey, closeAgentBrowser, readAgentBrowser,
  type AgentBrowserScreencastFrame, type AgentBrowserStatus } from "../../api/agent-browser";
import { V2ImageViewer } from "./image-viewer";
import "./image-input.css";
import "./agent-browser.css";

export function V2AgentBrowser(props: { client: APIClient; runID: string; running: boolean; onClose?: () => void }) {
  return <AgentBrowserView key={JSON.stringify([props.client.baseURL, props.runID])} {...props} />;
}

function stateText(status: AgentBrowserStatus, t: LocaleText): string {
  switch (status.state) {
    case "starting": return t("正在启动浏览器…", "Starting browser…");
    case "ready": return t("浏览器正在运行", "Browser running");
    case "loading": return t("正在加载网页…", "Loading page…");
    case "busy": return t("正在操作网页…", "Interacting with page…");
    case "waiting_user": return t("网页需要人工处理", "The page needs attention");
    case "failed": return t("浏览器操作未完成", "Browser action incomplete");
    case "closing": return t("正在停止浏览器…", "Stopping browser…");
    case "cleanup_pending": return t("会话已停止，仍在清理临时进程或资料", "Session stopped; cleaning up temporary processes or data");
    case "closed": return t("浏览器已停止", "Browser stopped");
    case "idle": return t("等待 Agent 打开网页", "Waiting for the agent to open a page");
    default: return t("当前环境无法使用 Agent 浏览器", "Agent browser unavailable in this environment");
  }
}

function unavailableText(code: AgentBrowserStatus["unavailable_code"], t: LocaleText): string {
  switch (code) {
    case "browser_network_off": return t("浏览器联网已关闭", "Browser internet access is off");
    case "platform_unsupported": return t("当前系统暂不支持浏览器。", "The browser is unavailable on this system.");
    case "full_access_required": return t("浏览器当前不可用，请检查任务权限。", "The browser is unavailable. Check task permissions.");
    case "run_unavailable": return t("当前执行的浏览器不可用。", "The browser is unavailable for this run.");
    case "session_unavailable": return t("上一浏览器会话正在清理。", "The previous browser session is being cleaned up.");
    default: return t("浏览器暂不可用。", "The browser is currently unavailable.");
  }
}

function screenshotPath(runID: string, status: AgentBrowserStatus): string {
  const image = status.screenshot!;
  return `/runs/${encodeURIComponent(runID)}/agent-browser/screenshot?${new URLSearchParams({
    session_id: status.session_id!, artifact_locator: image.locator, sha256: image.sha256,
  })}`;
}

function AgentBrowserView({ client, runID, running, onClose }: { client: APIClient; runID: string; running: boolean; onClose?: () => void }) {
  const { t } = useLocale();
  const queries = useQueryClient();
  const key = agentBrowserQueryKey(client.baseURL, runID);
  const [message, setMessage] = useState("");
  const [image, setImage] = useState<{ binding: string; url: string; blob: Blob }>();
  const [failedImageBinding, setFailedImageBinding] = useState("");
  const [imageRetry, setImageRetry] = useState(0);
  const [liveSessionID, setLiveSessionID] = useState<string | null>(null);
  const status = useQuery({ queryKey: key, queryFn: ({ signal }) => readAgentBrowser(client, runID, signal), retry: false,
    refetchOnMount: "always", refetchInterval: (query) => running && !query.state.error ? 3000 : false });
  const current = status.data;
  const close = useMutation({ retry: false, mutationFn: (sessionID: string) => closeAgentBrowser(client, runID, sessionID),
    onSuccess: (next, sessionID) => {
      queries.setQueryData<AgentBrowserStatus>(key, (observed) =>
        observed?.session_id === sessionID && next.session_id === sessionID ? next : observed);
      setMessage("");
    },
    onError: () => { setMessage(t("停止结果尚待确认，正在重新读取状态。请核对最新状态后再决定是否重试停止。", "The stop result is unconfirmed. Refreshing status; check it before retrying the stop request.")); void status.refetch(); } });

  const imageBinding = current?.screenshot && current.session_id
    ? `${runID}\0${current.session_id}\0${current.screenshot.locator}\0${current.screenshot.sha256}` : "";
  useEffect(() => {
    setImage(undefined);
    setFailedImageBinding("");
    if (!current?.screenshot || !current.session_id || !imageBinding) return;
    const abort = new AbortController(); let objectURL = "";
    void client.downloadVerifiedImage(screenshotPath(runID, current), current.screenshot, abort.signal).then((blob) => {
      if (abort.signal.aborted) return;
      objectURL = URL.createObjectURL(blob); setImage({ binding: imageBinding, url: objectURL, blob });
    }).catch(() => { if (!abort.signal.aborted) setFailedImageBinding(imageBinding); });
    return () => { abort.abort(); if (objectURL) URL.revokeObjectURL(objectURL); };
  }, [client, imageBinding, runID, imageRetry]);

  const dismissButton = onClose && <button className="v2-agent-browser-dismiss" type="button" aria-label={t("收起浏览器", "Close browser view")} title={t("收起浏览器", "Close browser view")}
    onClick={onClose}><X size={18} aria-hidden="true" /></button>;
  if (status.isPending || status.isError) return <section className="v2-agent-browser" aria-label={t("浏览器画面", "Browser view")}><header><strong>{t("浏览器", "Browser")}</strong>
    <div className="v2-agent-browser-actions">{status.isError && <button type="button" onClick={() => status.refetch()}><RefreshCw size={15} />{t("重新读取", "Reload")}</button>}{dismissButton}</div></header>
    <div className="v2-agent-browser-body"><p role={status.isError ? "alert" : "status"}>{status.isPending
      ? t("正在读取浏览器…", "Loading browser…") : agentBrowserAPIAbsent(status.error)
        ? t("当前连接不支持浏览器。", "This connection does not support the browser.")
        : t("无法读取浏览器状态。", "Could not read browser status.")}</p></div></section>;
  if (!current) return null;
  const canClose = Boolean(current.session_id) && ["starting", "ready", "loading", "busy", "waiting_user", "failed"].includes(current.state);
  const active = canClose || current.state === "closing" || current.state === "cleanup_pending";
  return <section className="v2-agent-browser" aria-label={t("浏览器画面", "Browser view")}>
    <header><div className="v2-agent-browser-heading"><strong>{t("浏览器", "Browser")}</strong>
      {current.unavailable_code !== "browser_network_off" && <span className={`v2-agent-browser-state state-${current.state}`}>{stateText(current, t)}</span>}</div>
      <div className="v2-agent-browser-actions">{canClose && <button type="button" disabled={close.isPending} onClick={() => close.mutate(current.session_id!)}>{t("停止浏览器", "Stop browser")}</button>}{dismissButton}</div></header>
    <div className="v2-agent-browser-body">
    {!current.available && unavailableText(current.unavailable_code, t) && <p className="v2-agent-browser-note" role="status">{unavailableText(current.unavailable_code, t)}</p>}
<<<<<<< HEAD
    {current.available && !active && <p className="v2-agent-browser-note">{current.headless ? t("页面将在独立的后台浏览器中打开，通过实时画面和截图查看。", "Pages open in a separate background browser and are shown as a live view and screenshots.") : t("页面将在独立浏览器窗口中打开。", "Pages open in a separate browser window.")}</p>}
    {current.available && current.declared_origins && current.declared_origins.length > 0 && <p className="v2-agent-browser-note">{t("已声明的导航来源：", "Declared navigation origins: ")}{current.declared_origins.join(", ")}</p>}
    {current.headless && active && current.session_id && <AgentBrowserLiveView client={client} runID={runID} sessionID={current.session_id} />}
=======
    {current.headless && active && current.session_id && <AgentBrowserLiveView key={current.session_id}
      client={client} runID={runID} sessionID={current.session_id} onLiveChange={setLiveSessionID} />}
>>>>>>> 8ac3e78d (fix(web): show browser frames only after drawing)
    {(current.title || current.url) && <div className="v2-agent-browser-page">
      {current.title && <strong>{current.title}</strong>}
      {current.url && <a href={current.url} target="_blank" rel="noreferrer">{current.url}<ExternalLink size={14} /></a>}
    </div>}
    {image?.binding === imageBinding && <BrowserScreenshot key={imageBinding} url={image.url} blob={image.blob}
      compact={liveSessionID === current.session_id}
      name={`agent-browser-${current.screenshot!.sha256.slice(0, 12)}.png`}
      alt={t(`Agent 浏览器页面：${current.title || current.url || "当前页面"}`, `Agent browser page: ${current.title || current.url || "Current page"}`)} />}
    {failedImageBinding && failedImageBinding === imageBinding && <p role="alert">
      {t("无法验证当前页面截图。", "The current screenshot could not be verified.")}{" "}
      <button type="button" onClick={() => setImageRetry((attempt) => attempt + 1)}>{t("重新读取截图", "Reload screenshot")}</button>
    </p>}
      {current.failure_code === "action_failed" && <p role="alert">{t("最近一次页面操作失败，请让 Agent 重新读取页面后再试。", "The last page action failed. Ask the agent to read the page again before retrying.")}</p>}
      {current.cleanup_pending && <p>{t("浏览器进程或临时资料仍在清理。", "Browser processes or temporary data are still being cleaned up.")}</p>}
    {message && <p role="alert">{message}</p>}
    </div>
  </section>;
}

function BrowserScreenshot({ url, blob, name, alt, compact }: { url: string; blob: Blob; name: string; alt: string; compact: boolean }) {
  const { t } = useLocale();
  const trigger = useRef<HTMLButtonElement>(null);
  const [dimensions, setDimensions] = useState<{ width: number; height: number }>();
  const [failed, setFailed] = useState(false);
  const [viewing, setViewing] = useState(false);
  return <>
    <button ref={trigger} type="button" className={`v2-agent-browser-screenshot${compact ? " is-compact" : ""}`} disabled={!dimensions || failed}
      aria-label={t("查看浏览器截图原图", "View original browser screenshot")} onClick={() => setViewing(true)}>
      <img src={url} alt={alt} hidden={compact} onLoad={({ currentTarget }) => {
        if (currentTarget.naturalWidth > 0 && currentTarget.naturalHeight > 0) {
          setDimensions({ width: currentTarget.naturalWidth, height: currentTarget.naturalHeight });
        }
      }} onError={() => { setFailed(true); setViewing(false); }} />
      <span><Maximize2 size={14} aria-hidden="true" />{t("查看原图", "View original")}</span>
    </button>
    {failed && <p role="alert">{t("无法显示当前页面截图，请让 Agent 重新截图。", "The current screenshot could not be displayed. Ask the agent to capture it again.")}</p>}
    {viewing && dimensions && <V2ImageViewer image={{ name, mime_type: "image/png", ...dimensions }}
      url={url} blob={blob} onClose={() => setViewing(false)} returnFocusRef={trigger} />}
  </>;
}

// The live view is visibility only: it streams the operator-facing picture of
// the exact session, never persists frames, and falls back to the verified
// screenshot chain when the stream is unavailable.
function AgentBrowserLiveView({ client, runID, sessionID, onLiveChange }: {
  client: APIClient; runID: string; sessionID: string; onLiveChange: (sessionID: string | null) => void;
}) {
  const { t } = useLocale();
  const canvasRef = useRef<HTMLCanvasElement>(null);
  const [live, setLive] = useState(false);
  const [failed, setFailed] = useState(false);
  useEffect(() => {
    const controller = new AbortController();
    let cancelled = false;
    let retry = 0;
    let timer: number | undefined;
    onLiveChange(null);
    const draw = (frame: AgentBrowserScreencastFrame) => {
      if (frame.session_id !== sessionID) return;
      const canvas = canvasRef.current;
      if (!canvas || typeof atob !== "function") return;
      const binary = atob(frame.data);
      const bytes = new Uint8Array(binary.length);
      for (let index = 0; index < binary.length; index++) bytes[index] = binary.charCodeAt(index);
      void createImageBitmap(new Blob([bytes], { type: "image/jpeg" })).then((bitmap) => {
        if (cancelled) { bitmap.close(); return; }
        try {
          const context = canvas.getContext("2d");
          if (!context || bitmap.width <= 0 || bitmap.height <= 0) return;
          canvas.width = bitmap.width;
          canvas.height = bitmap.height;
          context.drawImage(bitmap, 0, 0);
          setLive(true); onLiveChange(sessionID);
        } finally { bitmap.close(); }
      }).catch(() => { /* keep the last good frame; the next frame redraws */ });
    };
    const connect = () => {
      if (cancelled) return;
      try {
        void client.streamAgentBrowserScreencast(runID, {
          signal: controller.signal,
          onOpen: () => { retry = 0; setFailed(false); },
          onFrame: draw,
        }).catch(() => {
          if (cancelled || controller.signal.aborted) return;
          setLive(false);
          onLiveChange(null);
          setFailed(true);
          retry = Math.min(retry + 1, 5);
          timer = window.setTimeout(connect, Math.min(1000 * 2 ** retry, 8000));
        });
      } catch {
        if (!cancelled) {
          setLive(false);
          onLiveChange(null);
          setFailed(true);
        }
      }
    };
    connect();
    return () => { cancelled = true; controller.abort(); onLiveChange(null); if (timer !== undefined) window.clearTimeout(timer); };
  }, [client, runID, sessionID, onLiveChange]);
  return <figure className="v2-agent-browser-live">
    <canvas ref={canvasRef} hidden={!live} className={`v2-agent-browser-live-canvas${live ? " is-live" : ""}`}
      role="img" aria-label={t("Agent 浏览器实时画面", "Agent browser live view")} />
    <figcaption>
      {live ? t("实时画面", "Live view")
        : failed ? t("实时画面暂不可用，已回退到截图。", "Live view unavailable; falling back to screenshots.")
        : t("正在连接实时画面…", "Connecting to the live view…")}
    </figcaption>
  </figure>;
}
