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

export function V2AgentBrowser(props: { client: APIClient; runID: string; running: boolean; onDismiss?: () => void }) {
  return <AgentBrowserCard key={JSON.stringify([props.client.baseURL, props.runID])} {...props} />;
}

function stateText(status: AgentBrowserStatus, t: LocaleText): string {
  switch (status.state) {
    case "starting": return t("正在启动浏览器…", "Starting browser…");
    case "ready": return t("浏览器正在运行", "Browser running");
    case "loading": return t("正在加载网页…", "Loading page…");
    case "busy": return t("正在操作网页…", "Interacting with page…");
    case "waiting_user": return t("网页需要人工处理；当前版本暂不支持人工接管", "The page needs attention; manual takeover is not available yet");
    case "failed": return t("浏览器操作未完成", "Browser action incomplete");
    case "closing": return t("正在停止浏览器…", "Stopping browser…");
    case "cleanup_pending": return t("会话已停止，仍在清理临时进程或资料", "Session stopped; cleaning up temporary processes or data");
    case "closed": return t("浏览器已停止", "Browser stopped");
    case "idle": return t("等待 Agent 打开网页", "Waiting for the agent to open a page");
    default: return t("当前环境无法使用 Agent 浏览器", "Agent browser unavailable in this environment");
  }
}

function actionText(action: string | undefined, t: LocaleText): string {
  if (!action) return "";
  const labels: Record<string, string> = { browser_navigate: t("打开网页", "Open page"), browser_snapshot: t("读取页面", "Read page"), browser_click: t("点击页面", "Click page"),
    browser_type: t("输入内容", "Type content"), browser_screenshot: t("截取页面", "Capture page"), browser_scroll: t("滚动页面", "Scroll page"), browser_key: t("按键操作", "Press key") };
  return labels[action] ?? t("浏览器操作", "Browser action");
}

function unavailableText(code: AgentBrowserStatus["unavailable_code"], t: LocaleText): string {
  switch (code) {
    case "platform_unsupported": return t("当前系统尚未提供可用的 Agent 浏览器适配器。你可以自行打开应用页面检查，Agent 不会把它记为已完成浏览器验收。", "No agent browser adapter is available on this system. You can inspect the app manually; the agent will not record that as completed browser verification.");
    case "full_access_required": return t("Agent 浏览器需要当前任务启用完全访问（Full）。启用后，Agent 可打开独立浏览器检查页面。", "The agent browser requires Full access for the current task. Once enabled, the agent can inspect pages in a separate browser.");
    case "run_unavailable": return t("当前任务已暂停、结束或权限已变更，浏览器暂不可用。", "The task paused, ended or changed permissions. Its browser is unavailable.");
    case "session_unavailable": return t("上一浏览器会话尚未完成清理，请稍后重新读取状态。", "The previous browser session is still cleaning up. Refresh its status later.");
    default: return t("当前环境没有可用的 Agent 浏览器运行时，请检查本机浏览器和执行权限配置。", "No agent browser runtime is available. Check the local browser and execution permissions.");
  }
}

function screenshotPath(runID: string, status: AgentBrowserStatus): string {
  const image = status.screenshot!;
  return `/runs/${encodeURIComponent(runID)}/agent-browser/screenshot?${new URLSearchParams({
    session_id: status.session_id!, artifact_locator: image.locator, sha256: image.sha256,
  })}`;
}

function AgentBrowserCard({ client, runID, running, onDismiss }: { client: APIClient; runID: string; running: boolean; onDismiss?: () => void }) {
  const { t, locale } = useLocale();
  const queries = useQueryClient();
  const key = agentBrowserQueryKey(client.baseURL, runID);
  const [message, setMessage] = useState("");
  const [image, setImage] = useState<{ binding: string; url: string; blob: Blob }>();
  const [failedImageBinding, setFailedImageBinding] = useState("");
  const [imageRetry, setImageRetry] = useState(0);
  const status = useQuery({ queryKey: key, queryFn: ({ signal }) => readAgentBrowser(client, runID, signal), retry: false,
    refetchOnMount: "always", refetchInterval: (query) => running && !query.state.error ? 3000 : false });
  const current = status.data;
  const noticeState = current && ["starting", "ready", "loading", "busy"].includes(current.state) ? "active" : current?.state;
  const noticeKey = JSON.stringify([status.isError, current?.session_id, noticeState, current?.unavailable_code, current?.failure_code]);
  const [dismissed, setDismissed] = useState(false);
  useEffect(() => { setDismissed(false); }, [noticeKey]);
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

  if (status.isError && agentBrowserAPIAbsent(status.error)) return null;
  if (status.isPending) return null;
  if (dismissed) return null;
  const dismissButton = <button className="v2-agent-browser-dismiss" type="button" aria-label={t("关闭浏览器提示", "Dismiss browser notice")} title={t("关闭浏览器提示", "Dismiss browser notice")}
    onClick={() => { setDismissed(true); onDismiss?.(); }}><X size={18} aria-hidden="true" /></button>;
  if (status.isError) return <section className="v2-composer-card v2-agent-browser" aria-label={t("Agent 浏览器", "Agent browser")}><header><strong>{t("Agent 浏览器", "Agent browser")}</strong>
    <div className="v2-agent-browser-actions"><button type="button" onClick={() => status.refetch()}><RefreshCw size={15} />{t("重新读取", "Reload")}</button>{dismissButton}</div></header>
    <div className="v2-agent-browser-body"><p role="alert">{t("无法读取浏览器状态。自动读取已暂停。", "Could not read browser status. Automatic polling is paused.")}</p></div></section>;
  if (!current) return null;
  const canClose = Boolean(current.session_id) && ["starting", "ready", "loading", "busy", "waiting_user", "failed"].includes(current.state);
  const active = canClose || current.state === "closing" || current.state === "cleanup_pending";
  return <section className="v2-composer-card v2-agent-browser" aria-label={t("Agent 浏览器", "Agent browser")}>
    <header><div className="v2-agent-browser-heading"><strong>{t("Agent 浏览器", "Agent browser")}</strong><span className={`v2-agent-browser-state state-${current.state}`}>{stateText(current, t)}</span></div>
      <div className="v2-agent-browser-actions">{canClose && <button type="button" disabled={close.isPending} onClick={() => close.mutate(current.session_id!)}>{t("停止浏览器", "Stop browser")}</button>}{dismissButton}</div></header>
    <div className="v2-agent-browser-body">
    {!current.available && <p className="v2-agent-browser-note" role="status">{unavailableText(current.unavailable_code, t)}</p>}
    {current.available && !active && <p className="v2-agent-browser-note">{current.headless ? t("页面将在独立的后台浏览器中打开，通过实时画面和截图查看。", "Pages open in a separate background browser and are shown as a live view and screenshots.") : t("页面将在独立浏览器窗口中打开。", "Pages open in a separate browser window.")}</p>}
    {current.headless && active && current.session_id && <AgentBrowserLiveView client={client} runID={runID} sessionID={current.session_id} />}
    {current.headless && active && <p className="v2-agent-browser-note">{t("浏览器在后台运行，上方为实时画面；已验证的截图仍会作为证据保存。需要结束网页操作时可停止浏览器；整个任务可在对话中停止。", "The browser runs in the background with the live view above; verified screenshots are still kept as evidence. Stop the browser to end web actions, or stop the whole task in the conversation.")}</p>}
    {!current.headless && canClose && <p className="v2-agent-browser-note">{t("需要结束网页操作时可停止浏览器；整个任务可在对话中停止。", "Stop the browser to end web actions, or stop the whole task in the conversation.")}</p>}
    {(current.title || current.url) && <div className="v2-agent-browser-page">
      {current.title && <strong>{current.title}</strong>}
      {current.url && <a href={current.url} target="_blank" rel="noreferrer">{current.url}<ExternalLink size={14} /></a>}
    </div>}
    {image?.binding === imageBinding && <BrowserScreenshot key={imageBinding} url={image.url} blob={image.blob}
      name={`agent-browser-${current.screenshot!.sha256.slice(0, 12)}.png`}
      alt={t(`Agent 浏览器页面：${current.title || current.url || "当前页面"}`, `Agent browser page: ${current.title || current.url || "Current page"}`)} />}
    {failedImageBinding && failedImageBinding === imageBinding && <p role="alert">
      {t("无法验证当前页面截图。", "The current screenshot could not be verified.")}{" "}
      <button type="button" onClick={() => setImageRetry((attempt) => attempt + 1)}>{t("重新读取截图", "Reload screenshot")}</button>
    </p>}
    {(current.last_action || current.updated_at || current.failure_code) && <details><summary>{t("最近状态", "Latest status")}</summary>
      {current.last_action && <p>{t("最近动作：", "Last action: ")}{actionText(current.last_action, t)}</p>}
      <p>{t("更新时间：", "Updated: ")}{new Date(current.updated_at).toLocaleTimeString(locale)}</p>
      {current.failure_code === "action_failed" && <p role="alert">{t("最近一次页面操作失败，请让 Agent 重新读取页面后再试。", "The last page action failed. Ask the agent to read the page again before retrying.")}</p>}
      {current.cleanup_pending && <p>{t("浏览器进程或临时资料仍在清理。", "Browser processes or temporary data are still being cleaned up.")}</p>}
    </details>}
    {message && <p role="alert">{message}</p>}
    </div>
  </section>;
}

function BrowserScreenshot({ url, blob, name, alt }: { url: string; blob: Blob; name: string; alt: string }) {  const { t } = useLocale();
  const trigger = useRef<HTMLButtonElement>(null);
  const [dimensions, setDimensions] = useState<{ width: number; height: number }>();
  const [failed, setFailed] = useState(false);
  const [viewing, setViewing] = useState(false);
  return <>
    <button ref={trigger} type="button" className="v2-agent-browser-screenshot" disabled={!dimensions || failed}
      aria-label={t("查看浏览器截图原图", "View original browser screenshot")} onClick={() => setViewing(true)}>
      <img src={url} alt={alt} onLoad={({ currentTarget }) => {
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
function AgentBrowserLiveView({ client, runID, sessionID }: { client: APIClient; runID: string; sessionID: string }) {
  const { t } = useLocale();
  const canvasRef = useRef<HTMLCanvasElement>(null);
  const [live, setLive] = useState(false);
  const [failed, setFailed] = useState(false);
  useEffect(() => {
    const controller = new AbortController();
    let cancelled = false;
    let retry = 0;
    let timer: number | undefined;
    const draw = (frame: AgentBrowserScreencastFrame) => {
      if (frame.session_id !== sessionID) return;
      setLive(true);
      const canvas = canvasRef.current;
      if (!canvas || typeof atob !== "function") return;
      const binary = atob(frame.data);
      const bytes = new Uint8Array(binary.length);
      for (let index = 0; index < binary.length; index++) bytes[index] = binary.charCodeAt(index);
      void createImageBitmap(new Blob([bytes], { type: "image/jpeg" })).then((bitmap) => {
        if (cancelled) { bitmap.close(); return; }
        canvas.width = bitmap.width;
        canvas.height = bitmap.height;
        const context = canvas.getContext("2d");
        if (context) context.drawImage(bitmap, 0, 0);
        bitmap.close();
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
          setFailed(true);
          retry = Math.min(retry + 1, 5);
          timer = window.setTimeout(connect, Math.min(1000 * 2 ** retry, 8000));
        });
      } catch {
        if (!cancelled) {
          setLive(false);
          setFailed(true);
        }
      }
    };
    connect();
    return () => { cancelled = true; controller.abort(); if (timer !== undefined) window.clearTimeout(timer); };
  }, [client, runID, sessionID]);
  return <figure className="v2-agent-browser-live">
    <canvas ref={canvasRef} className={`v2-agent-browser-live-canvas${live ? " is-live" : ""}`}
      role="img" aria-label={t("Agent 浏览器实时画面", "Agent browser live view")} />
    <figcaption>
      {live ? t("实时画面（直播流不保存、不作为证据）", "Live view (streamed only, never stored as evidence)")
        : failed ? t("实时画面暂不可用，已回退到截图。", "Live view unavailable; falling back to screenshots.")
        : t("正在连接实时画面…", "Connecting to the live view…")}
    </figcaption>
  </figure>;
}
