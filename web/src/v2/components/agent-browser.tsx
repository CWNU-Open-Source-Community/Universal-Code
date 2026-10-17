import { useEffect, useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { ExternalLink, RefreshCw } from "lucide-react";
import type { APIClient } from "../../api/client";
import { agentBrowserAPIAbsent, agentBrowserQueryKey, closeAgentBrowser, readAgentBrowser, type AgentBrowserStatus } from "../../api/agent-browser";
import "./agent-browser.css";

export function V2AgentBrowser(props: { client: APIClient; runID: string; running: boolean }) {
  return <AgentBrowserCard key={JSON.stringify([props.client.baseURL, props.runID])} {...props} />;
}

function stateText(status: AgentBrowserStatus): string {
  switch (status.state) {
    case "starting": return "正在启动浏览器…";
    case "ready": return "浏览器正在运行";
    case "loading": return "正在加载网页…";
    case "busy": return "正在操作网页…";
    case "waiting_user": return "网页需要人工处理";
    case "failed": return "浏览器操作未完成";
    case "closing": return "正在停止浏览器…";
    case "cleanup_pending": return "浏览器已停止，仍在清理临时资料";
    case "closed": return "浏览器已停止";
    case "idle": return "等待 Agent 打开网页";
    default: return "当前环境无法使用 Agent 浏览器";
  }
}

function actionText(action?: string): string {
  if (!action) return "";
  const labels: Record<string, string> = { browser_navigate: "打开网页", browser_snapshot: "读取页面", browser_click: "点击页面",
    browser_type: "输入内容", browser_screenshot: "截取页面", browser_scroll: "滚动页面", browser_key: "按键操作" };
  return labels[action] ?? "浏览器操作";
}

function unavailableText(code: AgentBrowserStatus["unavailable_code"]): string {
  switch (code) {
    case "platform_unsupported": return "当前 macOS / Linux 版本尚不支持 Agent 浏览器复验。你可以自行打开应用页面检查，Agent 不会把它记为已完成浏览器验收。";
    case "full_access_required": return "Agent 浏览器需要当前任务启用完全访问（Full）。启用后，Agent 可打开独立浏览器检查页面。";
    case "run_unavailable": return "当前任务已暂停、结束或权限已变更，浏览器暂不可用。";
    case "session_unavailable": return "上一浏览器会话尚未完成清理，请稍后重新读取状态。";
    default: return "当前环境没有可用的 Agent 浏览器运行时，请检查本机浏览器和执行权限配置。";
  }
}

function screenshotPath(runID: string, status: AgentBrowserStatus): string {
  const image = status.screenshot!;
  return `/runs/${encodeURIComponent(runID)}/agent-browser/screenshot?${new URLSearchParams({
    session_id: status.session_id!, artifact_locator: image.locator, sha256: image.sha256,
  })}`;
}

function AgentBrowserCard({ client, runID, running }: { client: APIClient; runID: string; running: boolean }) {
  const queries = useQueryClient();
  const key = agentBrowserQueryKey(client.baseURL, runID);
  const [message, setMessage] = useState("");
  const [image, setImage] = useState<{ binding: string; url: string }>();
  const status = useQuery({ queryKey: key, queryFn: ({ signal }) => readAgentBrowser(client, runID, signal), retry: false,
    refetchOnMount: "always", refetchInterval: (query) => running && !query.state.error ? 3000 : false });
  const current = status.data;
  const close = useMutation({ retry: false, mutationFn: (sessionID: string) => closeAgentBrowser(client, runID, sessionID),
    onSuccess: (next, sessionID) => {
      queries.setQueryData<AgentBrowserStatus>(key, (observed) =>
        observed?.session_id === sessionID && next.session_id === sessionID ? next : observed);
      setMessage("");
    },
    onError: () => { setMessage("停止结果尚待确认，正在重新读取状态。请核对最新状态后再决定是否重试停止。"); void status.refetch(); } });

  const imageBinding = current?.screenshot && current.session_id
    ? `${runID}\0${current.session_id}\0${current.screenshot.locator}\0${current.screenshot.sha256}` : "";
  useEffect(() => {
    setImage(undefined);
    if (!current?.screenshot || !current.session_id || !imageBinding) return;
    const abort = new AbortController(); let objectURL = "";
    void client.downloadVerifiedImage(screenshotPath(runID, current), current.screenshot, abort.signal).then((blob) => {
      if (abort.signal.aborted) return;
      objectURL = URL.createObjectURL(blob); setImage({ binding: imageBinding, url: objectURL });
    }).catch(() => { if (!abort.signal.aborted) setMessage("无法验证当前页面截图，请重新读取状态。"); });
    return () => { abort.abort(); if (objectURL) URL.revokeObjectURL(objectURL); };
  }, [client, imageBinding, runID]);

  if (status.isError && agentBrowserAPIAbsent(status.error)) return null;
  if (status.isPending) return null;
  if (status.isError) return <section className="v2-agent-browser" aria-label="Agent 浏览器"><header><strong>Agent 浏览器</strong>
    <button type="button" onClick={() => status.refetch()}><RefreshCw size={15} />重新读取</button></header>
    <p role="alert">无法读取浏览器状态。自动读取已暂停。</p></section>;
  if (!current) return null;
  const canClose = Boolean(current.session_id) && ["starting", "ready", "loading", "busy", "waiting_user", "failed"].includes(current.state);
  const active = canClose || current.state === "closing" || current.state === "cleanup_pending";
  return <section className="v2-agent-browser" aria-label="Agent 浏览器">
    <header><div><strong>Agent 浏览器</strong><span className={`v2-agent-browser-state state-${current.state}`}>{stateText(current)}</span></div>
      {canClose && <button type="button" disabled={close.isPending} onClick={() => close.mutate(current.session_id!)}>停止浏览器</button>}</header>
    {!current.available && <p className="v2-agent-browser-note" role="status">{unavailableText(current.unavailable_code)}</p>}
    {current.available && !active && <p className="v2-agent-browser-note">{current.headless ? "页面将在独立的后台浏览器中打开，通过截图查看。" : "页面将在独立浏览器窗口中打开。"}</p>}
    {current.headless && active && <p className="v2-agent-browser-note">浏览器在后台运行，当前页面以已验证的截图展示。需要结束网页操作时可停止浏览器；整个任务可在对话中停止。</p>}
    {!current.headless && canClose && <p className="v2-agent-browser-note">需要结束网页操作时可停止浏览器；整个任务可在对话中停止。</p>}
    {(current.title || current.url) && <div className="v2-agent-browser-page">
      {current.title && <strong>{current.title}</strong>}
      {current.url && <a href={current.url} target="_blank" rel="noreferrer">{current.url}<ExternalLink size={14} /></a>}
    </div>}
    {image?.binding === imageBinding && <img src={image.url} alt={`Agent 浏览器页面：${current.title || current.url || "当前页面"}`} />}
    {(current.last_action || current.updated_at || current.failure_code) && <details><summary>最近状态</summary>
      {current.last_action && <p>最近动作：{actionText(current.last_action)}</p>}
      <p>更新时间：{new Date(current.updated_at).toLocaleTimeString()}</p>
      {current.failure_code === "action_failed" && <p role="alert">最近一次页面操作失败，请让 Agent 重新读取页面后再试。</p>}
      {current.cleanup_pending && <p>浏览器进程或临时资料仍在清理。</p>}
    </details>}
    {message && <p role="alert">{message}</p>}
  </section>;
}
