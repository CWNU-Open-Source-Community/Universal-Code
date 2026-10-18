import { useEffect, useRef, useState, type RefObject } from "react";
import { createPortal } from "react-dom";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { ExternalLink, LoaderCircle, RefreshCw, X } from "lucide-react";
import { APIRequestError, type APIClient } from "../../api/client";
import type { RunDetailView, ThreadApplicationServiceView, ThreadApplicationServiceDetailView } from "../../api/types";
import { localPreviewURL, parseApplicationPreview, type ApplicationPreview, type ApplicationPreviewElement } from "../../api/application-preview";
import { useModalFocusTrap } from "../../hooks/use-modal-focus-trap";
import { browserCDPQueryKey, fullCDPSessionQueryKey } from "./browser-cdp-control";
import { V2PermissionControl } from "./permission-control";
import { ThreadActivityToolDetailPanel } from "./activity-detail";
import { applicationPreviewRequestCopy, applicationServicesQueryKey, type ApplicationPreviewRequest } from "../application-preview-request";
import "./application-preview.css";

export function V2ApplicationPreview({ client, runID, threadID, onClose, returnFocusRef, onRequestStart, startRequest, canRequestStart = true }: {
  client: APIClient; runID: string; threadID: string; onClose: () => void;
  returnFocusRef: RefObject<HTMLElement | null>; onRequestStart: () => void;
  startRequest?: ApplicationPreviewRequest | null; canRequestStart?: boolean;
}) {
  const queries = useQueryClient();
  const closeButton = useRef<HTMLButtonElement>(null);
  const dialog = useModalFocusTrap<HTMLElement>(true, onClose, false, closeButton, { isolateBackground: true, returnFocusRef });
  const [selectedJobID, setSelectedJobID] = useState<string | null>(null);
  const stopping = useRef(false);
  const servicesReadable = typeof client.listThreadApplicationServices === "function";
  const services = useQuery({ queryKey: applicationServicesQueryKey(threadID),
    queryFn: ({ signal }) => client.listThreadApplicationServices(threadID, signal),
    enabled: servicesReadable, retry: false, refetchOnMount: "always", refetchInterval: 1500 });
  const records = services.data?.services ?? [];
  const related = startRequest?.runID && startRequest.messageID
    ? records.filter((service) => service.run_id === startRequest.runID && service.source_message_id === startRequest.messageID) : [];
  const selected = records.find((service) => service.job_id === selectedJobID) ??
    (selectedJobID === null && related.length === 1 ? related[0] : undefined);
  const serviceDetail = useQuery({ queryKey: applicationServiceQueryKey(threadID, selected?.job_id ?? ""),
    queryFn: ({ signal }) => client.getThreadApplicationService(threadID, selected!.job_id, signal),
    enabled: Boolean(selected), retry: false, refetchOnMount: "always", refetchInterval: 1500 });
  const source = serviceDetail.data?.service ?? selected;
  const serviceReadable = Boolean(source && !services.isError && !serviceDetail.isError && serviceDetail.data);
  const stop = useMutation({ retry: false,
    mutationFn: ({ sourceRunID, jobID }: { sourceRunID: string; jobID: string }) =>
      client.stopThreadApplicationService(threadID, sourceRunID, jobID),
    onSuccess: (result, input) => {
      queries.setQueryData<ThreadApplicationServiceDetailView>(applicationServiceQueryKey(threadID, input.jobID),
        (currentDetail) => currentDetail ? { ...currentDetail, service: result.service } : currentDetail);
      void queries.invalidateQueries({ queryKey: applicationServicesQueryKey(threadID) });
      void queries.invalidateQueries({ queryKey: applicationServiceQueryKey(threadID, input.jobID) });
    }, onSettled: () => { stopping.current = false; },
  });
  const stopSelected = () => {
    if (!source || !serviceReadable || !source.can_stop || !client.hasRunExecution || stopping.current) return;
    stopping.current = true;
    stop.mutate({ sourceRunID: source.run_id, jobID: source.job_id });
  };
  const browserRunID = source?.run_id ?? runID;
  return createPortal(<div className="v2-preview-overlay">
    <section className="v2-app-preview" ref={dialog} role="dialog" aria-modal="true" aria-label="应用预览">
      <header><h2>应用预览</h2><button aria-label="收起应用预览" type="button" ref={closeButton} onClick={onClose}><X size={20} /></button></header>
      <section className="v2-preview-services" aria-label="应用后台服务">
        <h3>项目命令与后台服务</h3>
        <p className="v2-preview-help">先把启动要求放回草稿，确认发送后在这里查看服务状态、启动输出和页面预览。</p>
        <button type="button" disabled={!canRequestStart || startRequest?.phase === "submitting" || startRequest?.phase === "unconfirmed"}
          onClick={onRequestStart}>把启动要求放回草稿</button>
        {startRequest && <p role="status">{applicationPreviewRequestCopy(startRequest, !services.isError && related.length > 0)}</p>}
        <details className="v2-preview-identity"><summary>预览来源</summary><p>所属任务 <code>{threadID}</code></p></details>
        {!servicesReadable ? <p role="status">当前连接未提供后台服务状态。</p>
          : services.isLoading ? <p role="status">正在读取后台服务记录…</p>
            : services.isError ? <div role="alert"><p>无法读取后台服务状态，请重试核对。</p>
              <button disabled={services.isFetching} onClick={() => void services.refetch()} type="button">重试服务状态</button></div>
              : records.length === 0 ? <p role="status">尚未发现受管理的后台服务。</p> : null}
        {startRequest && ["accepted", "failed"].includes(startRequest.phase) && related.length === 0 && !services.isLoading && !services.isError &&
          <p role="status">尚未发现本次启动要求关联的后台服务，可回对话查看批准或执行结果。</p>}
        {records.length > 0 && <ul className="v2-preview-service-list">
          {records.map((service) => <li key={service.job_id}><button type="button"
            aria-pressed={source?.job_id === service.job_id} onClick={() => setSelectedJobID(service.job_id)}>
            <span>{service.source_call_id ? "任务命令" : "历史命令"} · Run <code>{service.run_id}</code> · Job <code>{service.job_id}</code></span>
            <span>{serviceStateCopy(service)}{related.some((item) => item.job_id === service.job_id) ? " · 本次启动要求" : ""}</span>
          </button></li>)}</ul>}
        {services.data?.has_more && <p role="status">当前只展示最近一批后台任务，列表尚未完整。</p>}
        {records.length > 1 && !selected && <p role="status">请选择要查看的后台任务，预览与停止操作都以所选服务为范围。</p>}
        {selectedJobID && !selected && <p role="status">所选任务不在当前列表，请重新选择或使用当前执行的手动地址。</p>}
        {records.length > 0 && <button type="button" onClick={() => setSelectedJobID("")}>手动输入当前执行的地址</button>}
        {source && <div className="v2-preview-service-detail">
          <details className="v2-preview-identity"><summary>服务来源与标识</summary>
            <p>来源执行 <code>{source.run_id}</code> · 服务 <code>{source.job_id}</code></p>
            {source.source_message_id && <p>来源消息 <code>{source.source_message_id}</code></p>}
          </details>
          <p role="status">{serviceDetail.isError || services.isError ? "当前服务状态未确认" : serviceStateCopy(source)}
            {source.exit_code !== undefined && ` · 退出码 ${source.exit_code}`}</p>
          {!source.source_message_id && <p className="v2-preview-help">该服务的来源消息待确认，请结合启动输出核对它是否属于本次要求。</p>}
          {source.can_stop && <button disabled={!serviceReadable || stop.isPending || !client.hasRunExecution}
            onClick={stopSelected} type="button">{stop.isPending && stop.variables?.jobID === source.job_id ? "正在请求停止命令…" : "停止此命令"}</button>}
          <p className="v2-preview-help">服务会在收起面板或关闭浏览器后继续运行。需要结束时，点击“停止此命令”，清理范围为所选服务。</p>
          {stop.isError && stop.variables?.jobID === source.job_id && <p role="alert">停止服务未确认完成，请核对状态后重试。
            {stop.error.message}</p>}
          {serviceDetail.isLoading && <p role="status">正在读取启动输出…</p>}
          {serviceDetail.isError && <div role="alert"><p>启动输出与当前状态读取失败。</p>
            <button disabled={serviceDetail.isFetching} onClick={() => void serviceDetail.refetch()} type="button">重试启动输出</button></div>}
          {serviceDetail.data && <ServiceOutput detail={serviceDetail.data} />}
          {source.source_call_id && <ServiceCommandEvidence key={JSON.stringify([threadID, source.run_id, source.job_id, source.source_call_id])}
            client={client} threadID={threadID} source={source} />}
        </div>}
      </section>
      {(!selectedJobID || selected) && <V2PreviewBrowser key={JSON.stringify([browserRunID, source?.job_id ?? "manual"])} client={client} runID={browserRunID} threadID={threadID}
        currentRunID={runID} sourceService={source} sourceReadable={serviceReadable}
        candidates={serviceReadable ? serviceDetail.data?.candidate_urls : undefined} />}
    </section>
  </div>, document.body);
}

export const applicationServiceQueryKey = (threadID: string, jobID: string) =>
  [...applicationServicesQueryKey(threadID), jobID] as const;

function serviceStateCopy(service: ThreadApplicationServiceView): string {
  const { state, can_stop: canStop } = service;
  if (!["prepared", "running", "stopping"].includes(state) &&
    (service.tree_reaped === false || (service.workspace_mode === "shared_service" && service.tree_reaped !== true))) {
    return "任务命令已退出，清理待确认";
  }
  if (state === "running" && !canStop) return "保存的运行记录，当前服务状态未确认";
  switch (state) {
    case "prepared": return "任务命令已登记，等待启动";
    case "running": return "命令进程运行中，页面可用性尚未验证";
    case "stopping": return "正在停止任务命令";
    case "completed": return "任务命令已退出";
    case "failed": return "任务命令启动或执行失败";
    case "timed_out": return "任务命令已超时";
    case "cancelled": return "任务命令已停止";
    case "killed": return "任务命令已终止";
    case "interrupted": return "任务命令已中断";
    default: return "后台服务状态未确认";
  }
}

function ServiceCommandEvidence({ client, source, threadID }: {
  client: APIClient; source: ThreadApplicationServiceView; threadID: string;
}) {
  const [open, setOpen] = useState(false);
  const activityRef = source.source_call_id ?? "";
  const evidence = useQuery({ queryKey: ["v2", "application-service-evidence", threadID, source.run_id, source.job_id, activityRef],
    queryFn: async ({ signal }) => {
      const detail = await client.threadActivityDetail(threadID, activityRef, signal);
      if (detail.run_id !== source.run_id || detail.tools.some((tool) => tool.name !== "command_runtime")) {
        throw new Error("启动工具记录与所选命令的来源不匹配。");
      }
      return detail;
    }, enabled: open && Boolean(activityRef), retry: false });
  return <details className="v2-preview-output" open={open} onToggle={(event) => setOpen(event.currentTarget.open)}>
    <summary>查看启动工具记录与已保存输出</summary>
    <p className="v2-preview-help">以下记录属于此命令的原始工具调用；一次工具调用可能包含多条命令。已保存输出按需读取。</p>
    {evidence.isLoading && <p role="status">正在读取启动工具记录…</p>}
    {evidence.isError && <div role="alert"><p>启动工具记录读取失败。</p>
      <button disabled={evidence.isFetching} onClick={() => void evidence.refetch()} type="button">重试启动工具记录</button></div>}
    {!evidence.isError && evidence.data?.tools.map((tool) => <ThreadActivityToolDetailPanel key={tool.name} client={client}
      activityRef={activityRef} threadID={threadID} runID={source.run_id} tool={tool} />)}
  </details>;
}

function ServiceOutput({ detail }: { detail: ThreadApplicationServiceDetailView }) {
  const output = detail.output;
  return <details className="v2-preview-output"><summary>查看原始启动输出</summary>
    <p className="v2-preview-help">输出来自该后台任务，标准输出和标准错误分别展示保留的原始脱敏正文。输出中的地址尚未验证。</p>
    {!output.available ? <p role="status">当前运行时无法读取此任务的保留输出。</p>
      : !output.stdout && !output.stderr ? <p>尚无启动输出。</p>
        : <><h4>标准输出</h4><pre>{output.stdout || "尚无标准输出。"}</pre>
          <h4>标准错误</h4><pre>{output.stderr || "尚无标准错误。"}</pre></>}
    {(output.dropped || output.base_cursor > 0 || output.truncation_reason) &&
      <p role="status">启动输出未完整保留{output.truncation_reason ? `（${output.truncation_reason}）` : ""}。</p>}
    {output.next_cursor < output.end_cursor && <p role="status">当前展示部分保留输出。</p>}
  </details>;
}

function V2PreviewBrowser({ client, runID, threadID, currentRunID, sourceService, sourceReadable, candidates }: {
  client: APIClient; runID: string; threadID: string; currentRunID: string;
  sourceService?: ThreadApplicationServiceView; sourceReadable: boolean;
  candidates?: ThreadApplicationServiceDetailView["candidate_urls"];
}) {
  const queries = useQueryClient();
  const [target, setTarget] = useState("");
  const [product, setProduct] = useState<"edge" | "chrome">("edge");
  const [preview, setPreview] = useState<ApplicationPreview | null>(null);
  const [url, setURL] = useState("");
  const [error, setError] = useState("");
  const [values, setValues] = useState<Record<string, string>>({});
  const actionInFlight = useRef(false);
  const detail = useQuery({ queryKey: browserCDPQueryKey(runID),
    queryFn: ({ signal }) => client.get<RunDetailView>(`/runs/${encodeURIComponent(runID)}`, {}, signal),
    enabled: client.hasFullCDPSessionControl, refetchInterval: 3000 });
  const session = useQuery({ queryKey: fullCDPSessionQueryKey(runID),
    queryFn: ({ signal }) => client.getFullCDPSession(runID, signal), enabled: client.hasFullCDPSessionControl,
    refetchOnMount: "always", refetchInterval: 1500 });
  const current = session.data?.session;
  const ready = current?.state === "ready";
  const active = ready || current?.state === "starting" || current?.state === "closing";
  const permission = detail.data?.execution_permission;
  const browserPermission = detail.data?.browser_cdp_permission;
  const eligible = permission?.mode === "full" && permission.runtime_gate_available;
  const cdpEnabled = browserPermission?.mode === "full_debug" && browserPermission.runtime_gate_available;
  const runUsable = Boolean(detail.data?.run && ["created", "preparing", "running", "waiting_approval", "paused"].includes(detail.data.run.status));
  const serviceAllowsOpen = !sourceService || (sourceReadable && sourceService.state === "running" && sourceService.can_stop);
  const canOpen = Boolean(!detail.isError && !session.isError && runUsable && serviceAllowsOpen && eligible && cdpEnabled);
  const refresh = useMutation({ retry: false, mutationFn: async () => {
    setError(""); setPreview(null);
    const sessionID = current?.session_id;
    if (!sessionID || !ready || session.isError || detail.isError) throw new Error("预览浏览器尚未就绪或状态未确认。");
    return parseApplicationPreview(await client.postControl(`/runs/${encodeURIComponent(runID)}/full-cdp-session/preview`, {
      version: "full_cdp_preview.v1", expected_session_id: sessionID,
    }, `v2-preview-${crypto.randomUUID()}`), runID, sessionID);
  }, onSuccess: setPreview, onError: (failure) => setError(failure instanceof APIRequestError
    ? "无法读取当前页面，请检查预览是否仍在运行以及浏览器控制权限。" : failure.message) });
  const open = useMutation({ retry: false, mutationFn: () => client.openFullCDPSession(runID, {
    version: "full_cdp_session.v1", target: target.trim(), browser: { product, channel: "stable" },
    expected_execution_permission_revision: permission?.revision ?? 0,
    expected_browser_cdp_permission_revision: browserPermission?.revision ?? 0,
    confirm_full_cdp: true, reason: "operator opened project application preview",
  }, `v2-preview-open-${crypto.randomUUID()}`), onSuccess: (result) => { queries.setQueryData(fullCDPSessionQueryKey(runID), result); setError(""); },
  onError: (failure) => setError(failure.message) });
  const close = useMutation({ retry: false, mutationFn: () => client.closeFullCDPSession(runID, {
    version: "full_cdp_session_close.v1", expected_session_id: current?.session_id ?? "", reason: "operator_closed",
  }, `v2-preview-close-${crypto.randomUUID()}`), onMutate: () => { setPreview(null); setURL(""); },
  onSuccess: (result) => queries.setQueryData(fullCDPSessionQueryKey(runID), result), onError: (failure) => setError(failure.message) });
  const enable = useMutation({ retry: false, mutationFn: () => client.postControl(`/runs/${encodeURIComponent(runID)}/browser-cdp-permission`, {
    mode: "full_debug", confirm_full_cdp_debug: true, reason: "operator enabled project preview browser control",
  }, `v2-preview-enable-${crypto.randomUUID()}`), onSuccess: () => queries.invalidateQueries({ queryKey: browserCDPQueryKey(runID) }),
  onError: (failure) => setError(failure.message) });
  const act = useMutation({ retry: false, mutationFn: async ({ element, value }: { element: ApplicationPreviewElement; value?: string }) => {
    const observed = preview;
    if (!observed || actionInFlight.current) throw new Error("请先刷新当前页面。");
    actionInFlight.current = true;
    setPreview(null); setURL(""); setError("");
    try {
      return parseApplicationPreview(await client.postControl(`/runs/${encodeURIComponent(runID)}/full-cdp-session/preview-action`, {
        version: "full_cdp_preview_action.v1", expected_session_id: observed.session_id,
        expected_snapshot_id: observed.page.snapshot_id, action: value === undefined ? "click" : "type",
        selector: element.selector, ...(value === undefined ? {} : { value }),
      }, `v2-preview-action-${crypto.randomUUID()}`), runID, observed.session_id);
    } finally { actionInFlight.current = false; }
  }, onSuccess: (result) => { setPreview(result); setValues({}); },
  onError: (failure) => setError(`${failure instanceof APIRequestError
    ? "页面操作结果尚待确认，当前页面或授权状态可能已变化。" : failure.message} 请刷新页面预览，核对结果后再发起操作。`) });
  useEffect(() => {
    if (!active) return;
    if (current?.browser?.product) setProduct(current.browser.product);
    const address = preview?.canonical_url ?? current?.target_origin;
    if (address) setTarget(address);
  }, [active, current?.browser?.product, current?.target_origin, preview?.canonical_url]);
  useEffect(() => {
    if (!ready || !preview || preview.session_id !== current?.session_id) { setURL(""); return; }
    const abort = new AbortController(); let objectURL = "";
    setURL("");
    void client.downloadVerifiedImage(`/runs/${encodeURIComponent(runID)}/full-cdp-session/preview-image?` +
      new URLSearchParams({ session_id: preview.session_id, sha256: preview.image.sha256 }), {
        mime_type: preview.image.media_type, byte_size: preview.image.bytes, sha256: preview.image.sha256,
      }, abort.signal).then((blob) => {
        if (abort.signal.aborted) return;
        objectURL = URL.createObjectURL(blob); setURL(objectURL);
      }).catch((failure) => { if (!abort.signal.aborted) { setPreview(null); setError(failure.message); } });
    return () => { abort.abort(); if (objectURL) URL.revokeObjectURL(objectURL); };
  }, [preview, client, runID, current?.session_id, ready]);
  const lastSession = useRef("");
  useEffect(() => {
    if (!session.isFetching && !session.isError && ready && current?.session_id && lastSession.current !== current.session_id) {
      lastSession.current = current.session_id; refresh.mutate();
    }
    if (!ready || session.isError || detail.isError) setPreview(null);
  }, [ready, current?.session_id, session.isFetching, session.isError, detail.isError]);
  const busy = refresh.isPending || open.isPending || close.isPending || act.isPending;
  const visibleElements = preview?.page.elements.filter((element) => element.type !== "hidden") ?? [];
  return <section className="v2-preview-browser" aria-label="预览浏览器控制">
      <h3>浏览器</h3>
      <details className="v2-preview-identity"><summary>浏览器来源</summary><p>来源执行 <code>{runID}</code></p></details>
      {!client.hasFullCDPSessionControl ? <p role="status">当前连接未开放托管浏览器。后台服务状态与日志仍可在上方查看；网页预览需支持托管浏览器的服务及当前任务权限。</p> : <>
        {sourceService && (!runUsable || !serviceAllowsOpen) && <p role="status">
          {detail.isLoading ? "正在核对服务所属执行…" : !sourceReadable ? "服务状态尚未确认，请先重试核对。"
            : sourceService.state !== "running" ? "此后台任务未在运行，可以查看启动输出。"
              : !sourceService.can_stop ? "保存的运行记录，当前服务状态未确认。请回对话核对服务。"
              : "服务所属执行已退出或无法确认，不能打开其预览浏览器。"}</p>}
        {candidates && candidates.length > 0 && <div className="v2-preview-candidates" aria-label="输出中的候选地址">
          <p>从启动输出发现以下地址，尚未验证页面是否可用。请选择要打开的地址。</p>
          {candidates.map((candidate) => <button key={candidate.url} type="button" aria-pressed={target === candidate.url}
            disabled={active || !runUsable || !serviceAllowsOpen || !localPreviewURL(candidate.url)}
            onClick={() => setTarget(candidate.url)} aria-label={`选择地址 ${candidate.url}`}><code>{candidate.url}</code></button>)}
        </div>}
        <div className="v2-preview-address"><label>项目应用地址<input aria-label="项目应用地址" type="url" placeholder="http://127.0.0.1:3000" value={target}
          disabled={active || open.isPending} onChange={(event) => setTarget(event.target.value)} /></label>
          <label>浏览器<select aria-label="预览浏览器" value={product} disabled={active} onChange={(event) => setProduct(event.target.value as typeof product)}>
            <option value="edge">Microsoft Edge</option><option value="chrome">Google Chrome</option></select></label>
          {!active ? <button type="button" disabled={busy || !canOpen || !localPreviewURL(target.trim())} onClick={() => open.mutate()}><ExternalLink size={16} />打开应用</button>
            : <button type="button" disabled={busy || current?.state === "closing"} onClick={() => close.mutate()}>关闭浏览器</button>}
        </div>
        {!active && <p className="v2-preview-help">填写开发服务显示的本机地址。打开后，当前任务可以操作这个独立浏览器；使用临时浏览器资料，关闭或到期后清理。
          </p>}
        {!eligible && <div className="v2-preview-help">打开预览需要先确认完全访问权限。
          {runID === currentRunID ? <V2PermissionControl client={client} threadID={threadID} /> : <span>此服务来自历史执行，请回到对应执行核对权限。</span>}</div>}
        {eligible && !cdpEnabled && <div className="v2-preview-help">浏览器控制目前关闭。启用后，当前任务能读取、操作此独立浏览器及其网络请求。
          {runID === currentRunID ? <button disabled={enable.isPending || !runUsable || !client.hasBrowserCDPPermissionControl || !client.hasFullCDPDebug} type="button" onClick={() => enable.mutate()}>允许任务控制预览浏览器</button>
            : <span>请回到服务所属执行核对浏览器权限。</span>}</div>}
        <div className="v2-preview-status" role="status"><span>{session.isError || detail.isError ? "无法读取浏览器状态" : current?.state === "ready" ? "浏览器已就绪" : current?.state === "starting" ? "正在启动浏览器…"
          : current?.state === "closing" ? "正在清理浏览器…" : current?.state === "failed" ? "浏览器启动失败" : "浏览器未运行"}</span>
          {current?.target_origin && <code>{current.target_origin}</code>}
          {ready && <button type="button" disabled={busy} onClick={() => refresh.mutate()}><RefreshCw size={15} />刷新页面预览</button>}</div>
        {error && <p role="alert" className="v2-composer-error">{error}</p>}
        {current?.state === "failed" && <p role="alert">{current.failure_code || "未能启动，请检查开发服务与浏览器是否可用。"}</p>}
        {ready && !session.isError && !detail.isError && <div className="v2-preview-page">
          {busy && <p role="status"><LoaderCircle size={18} className="spin" />正在读取页面…</p>}
          {url && preview && <><p>{preview.page.title || "应用页面"}<small>观察于 {new Date(preview.captured_at).toLocaleTimeString()}</small></p>
            <img className="v2-preview-capture" src={url} alt={`应用页面：${preview.page.title || preview.canonical_url}`} />
            <details className="v2-preview-interactions"><summary>操作页面控件（{visibleElements.length}）</summary>
              <p>输入会追加到页面输入框的当前光标位置；只发送给本项目页面。操作后重新读取页面，观察过期时请先刷新。</p>
              {visibleElements.map((element) => {
                const editable = (element.tag === "input" && !["button", "submit", "reset", "checkbox", "radio", "file", "password", "hidden"].includes(element.type ?? "text")) || element.tag === "textarea";
                const blocked = element.disabled || ["password", "file", "hidden"].includes(element.type ?? "");
                const label = element.name || element.role || element.tag;
                return <div className="v2-preview-element" key={element.selector}><span>{label}</span>
                  {editable && <input aria-label={`页面输入 ${label}`} disabled={blocked || busy} value={values[element.selector] ?? ""}
                    onChange={(event) => setValues((currentValues) => ({ ...currentValues, [element.selector]: event.target.value }))} />}
                  <button type="button" disabled={blocked || busy || (editable && !(element.selector in values))}
                    onClick={() => act.mutate({ element, ...(editable ? { value: values[element.selector] } : {}) })}>{editable ? "输入到页面" : "点击"}</button></div>;
              })}</details>
            <details><summary>页面文字{preview.page.truncated ? "（部分）" : ""}</summary><pre>{preview.page.text}</pre></details></>}
        </div>}
        {current?.state === "closed" && <p role="status">预览已关闭，浏览器{current.process_tree_quiescent && current.profile_cleaned ? "和临时资料已清理。" : "清理状态尚未完整确认。"}</p>}
      </>}
    </section>;
}
