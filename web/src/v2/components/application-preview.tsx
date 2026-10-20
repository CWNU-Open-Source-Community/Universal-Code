import { useLocale, type LocaleText } from "../../lib/locale";
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
  const { t } = useLocale();
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
    <section className="v2-app-preview" ref={dialog} role="dialog" aria-modal="true" aria-label={t("应用预览", "App preview")}>
      <header><h2>{t("应用预览", "App preview")}</h2><button aria-label={t("收起应用预览", "Close app preview")} type="button" ref={closeButton} onClick={onClose}><X size={20} /></button></header>
      <section className="v2-preview-services" aria-label={t("应用后台服务", "App background services")}>
        <h3>{t("项目命令与后台服务", "Project commands and background services")}</h3>
        <p className="v2-preview-help">{t("先把启动要求放回草稿，确认发送后在这里查看服务状态、启动输出和页面预览。", "Add the startup request to your draft and send it after review. Then view service status, startup output and page previews here.")}</p>
        <button type="button" disabled={!canRequestStart || startRequest?.phase === "submitting" || startRequest?.phase === "unconfirmed"}
          onClick={onRequestStart}>{t("把启动要求放回草稿", "Add startup request to draft")}</button>
        {startRequest && <p role="status">{applicationPreviewRequestCopy(startRequest, !services.isError && related.length > 0, t)}</p>}
        <details className="v2-preview-identity"><summary>{t("预览来源", "Preview source")}</summary><p>{t("所属任务 ", "Task ")}<code>{threadID}</code></p></details>
        {!servicesReadable ? <p role="status">{t("当前连接未提供后台服务状态。", "Background service status is unavailable on this connection.")}</p>
          : services.isLoading ? <p role="status">{t("正在读取后台服务记录…", "Loading background service records…")}</p>
            : services.isError ? <div role="alert"><p>{t("无法读取后台服务状态，请重试核对。", "Could not read background service status. Retry to check.")}</p>
              <button disabled={services.isFetching} onClick={() => void services.refetch()} type="button">{t("重试服务状态", "Reload service status")}</button></div>
              : records.length === 0 ? <p role="status">{t("尚未发现受管理的后台服务。", "No managed background services found yet.")}</p> : null}
        {startRequest && ["accepted", "failed"].includes(startRequest.phase) && related.length === 0 && !services.isLoading && !services.isError &&
          <p role="status">{t("尚未发现本次启动要求关联的后台服务，可回对话查看批准或执行结果。", "No background service is linked to this startup request yet. Check approvals or results in the conversation.")}</p>}
        {records.length > 0 && <ul className="v2-preview-service-list">
          {records.map((service) => <li key={service.job_id}><button type="button"
            aria-pressed={source?.job_id === service.job_id} onClick={() => setSelectedJobID(service.job_id)}>
            <span>{service.source_call_id ? t("任务命令", "Task command") : t("历史命令", "Past command")} · Run <code>{service.run_id}</code> · Job <code>{service.job_id}</code></span>
            <span>{serviceStateCopy(service, t)}{related.some((item) => item.job_id === service.job_id) ? t(" · 本次启动要求", " · This startup request") : ""}</span>
          </button></li>)}</ul>}
        {services.data?.has_more && <p role="status">{t("当前只展示最近一批后台任务，列表尚未完整。", "Only the latest background tasks are shown. This list is incomplete.")}</p>}
        {records.length > 1 && !selected && <p role="status">{t("请选择要查看的后台任务，预览与停止操作都以所选服务为范围。", "Select a background task. Preview and stop actions apply to that service.")}</p>}
        {selectedJobID && !selected && <p role="status">{t("所选任务不在当前列表，请重新选择或使用当前执行的手动地址。", "The selected task is not listed. Select another or enter an address for the current run manually.")}</p>}
        {records.length > 0 && <button type="button" onClick={() => setSelectedJobID("")}>{t("手动输入当前执行的地址", "Enter an address for the current run")}</button>}
        {source && <div className="v2-preview-service-detail">
          <details className="v2-preview-identity"><summary>{t("服务来源与标识", "Service source and identifiers")}</summary>
            <p>{t("来源执行 ", "Source run ")}<code>{source.run_id}</code> {t(" · 服务 ", " · Service ")}<code>{source.job_id}</code></p>
            {source.source_message_id && <p>{t("来源消息 ", "Source message ")}<code>{source.source_message_id}</code></p>}
          </details>
          <p role="status">{serviceDetail.isError || services.isError ? t("当前服务状态未确认", "Current service status unconfirmed") : serviceStateCopy(source, t)}
            {source.exit_code !== undefined && t(` · 退出码 ${source.exit_code}`, ` · Exit code ${source.exit_code}`)}</p>
          {!source.source_message_id && <p className="v2-preview-help">{t("该服务的来源消息待确认，请结合启动输出核对它是否属于本次要求。", "The service's source message is unconfirmed. Use startup output to check whether it belongs to this request.")}</p>}
          {source.can_stop && <button disabled={!serviceReadable || stop.isPending || !client.hasRunExecution}
            onClick={stopSelected} type="button">{stop.isPending && stop.variables?.jobID === source.job_id ? t("正在请求停止命令…", "Requesting command stop…") : t("停止此命令", "Stop this command")}</button>}
          <p className="v2-preview-help">{t("服务会在收起面板或关闭浏览器后继续运行。需要结束时，点击“停止此命令”，清理范围为所选服务。", "The service keeps running after closing this panel or browser. Select Stop this command to stop and clean up the selected service.")}</p>
          {stop.isError && stop.variables?.jobID === source.job_id && <p role="alert">{t("停止服务未确认完成，请核对状态后重试。", "Service stop is unconfirmed. Check status before retrying.")}{stop.error.message}</p>}
          {serviceDetail.isLoading && <p role="status">{t("正在读取启动输出…", "Loading startup output…")}</p>}
          {serviceDetail.isError && <div role="alert"><p>{t("启动输出与当前状态读取失败。", "Could not load startup output and current status.")}</p>
            <button disabled={serviceDetail.isFetching} onClick={() => void serviceDetail.refetch()} type="button">{t("重试启动输出", "Reload startup output")}</button></div>}
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

function serviceStateCopy(service: ThreadApplicationServiceView, t: LocaleText): string {
  const { state, can_stop: canStop } = service;
  if (!["prepared", "running", "stopping"].includes(state) &&
    (service.tree_reaped === false || (service.workspace_mode === "shared_service" && service.tree_reaped !== true))) {
    return t("任务命令已退出，清理待确认", "Task command exited; cleanup unconfirmed");
  }
  if (state === "running" && !canStop) return t("保存的运行记录，当前服务状态未确认", "Saved run record; current service status unconfirmed");
  switch (state) {
    case "prepared": return t("任务命令已登记，等待启动", "Task command registered; waiting to start");
    case "running": return t("命令进程运行中，页面可用性尚未验证", "Command running; page availability unverified");
    case "stopping": return t("正在停止任务命令", "Stopping task command");
    case "completed": return t("任务命令已退出", "Task command exited");
    case "failed": return t("任务命令启动或执行失败", "Task command failed to start or run");
    case "timed_out": return t("任务命令已超时", "Task command timed out");
    case "cancelled": return t("任务命令已停止", "Task command stopped");
    case "killed": return t("任务命令已终止", "Task command terminated");
    case "interrupted": return t("任务命令已中断", "Task command interrupted");
    default: return t("后台服务状态未确认", "Background service status unconfirmed");
  }
}

function ServiceCommandEvidence({ client, source, threadID }: {
  client: APIClient; source: ThreadApplicationServiceView; threadID: string;
}) {
  const { t } = useLocale();
  const [open, setOpen] = useState(false);
  const activityRef = source.source_call_id ?? "";
  const evidence = useQuery({ queryKey: ["v2", "application-service-evidence", threadID, source.run_id, source.job_id, activityRef],
    queryFn: async ({ signal }) => {
      const detail = await client.threadActivityDetail(threadID, activityRef, signal);
      if (detail.run_id !== source.run_id || detail.tools.some((tool) => tool.name !== "command_runtime")) {
        throw new Error(t("启动工具记录与所选命令的来源不匹配。", "The startup tool record does not match the selected command's source."));
      }
      return detail;
    }, enabled: open && Boolean(activityRef), retry: false });
  return <details className="v2-preview-output" open={open} onToggle={(event) => setOpen(event.currentTarget.open)}>
    <summary>{t("查看启动工具记录与已保存输出", "View startup tool record and saved output")}</summary>
    <p className="v2-preview-help">{t("以下记录属于此命令的原始工具调用；一次工具调用可能包含多条命令。已保存输出按需读取。", "These records belong to the command's original tool call, which may contain multiple commands. Saved output loads on demand.")}</p>
    {evidence.isLoading && <p role="status">{t("正在读取启动工具记录…", "Loading startup tool record…")}</p>}
    {evidence.isError && <div role="alert"><p>{t("启动工具记录读取失败。", "Could not load startup tool record.")}</p>
      <button disabled={evidence.isFetching} onClick={() => void evidence.refetch()} type="button">{t("重试启动工具记录", "Reload startup tool record")}</button></div>}
    {!evidence.isError && evidence.data?.tools.map((tool) => <ThreadActivityToolDetailPanel key={tool.name} client={client}
      activityRef={activityRef} threadID={threadID} runID={source.run_id} tool={tool} />)}
  </details>;
}

function ServiceOutput({ detail }: { detail: ThreadApplicationServiceDetailView }) {
  const { t } = useLocale();
  const output = detail.output;
  return <details className="v2-preview-output"><summary>{t("查看原始启动输出", "View original startup output")}</summary>
    <p className="v2-preview-help">{t("输出来自该后台任务，标准输出和标准错误分别展示保留的原始脱敏正文。输出中的地址尚未验证。", "Output belongs to this background task. Retained, redacted stdout and stderr are shown separately. Addresses in the output are unverified.")}</p>
    {!output.available ? <p role="status">{t("当前运行时无法读取此任务的保留输出。", "The runtime cannot read this task's retained output.")}</p>
      : !output.stdout && !output.stderr ? <p>{t("尚无启动输出。", "No startup output yet.")}</p>
        : <><h4>{t("标准输出", "Standard output")}</h4><pre>{output.stdout || t("尚无标准输出。", "No standard output yet.")}</pre>
          <h4>{t("标准错误", "Standard error")}</h4><pre>{output.stderr || t("尚无标准错误。", "No standard error yet.")}</pre></>}
    {(output.dropped || output.base_cursor > 0 || output.truncation_reason) &&
      <p role="status">{t("启动输出未完整保留", "Startup output was not fully retained")}{output.truncation_reason ? `（${output.truncation_reason}）` : ""}。</p>}
    {output.next_cursor < output.end_cursor && <p role="status">{t("当前展示部分保留输出。", "Showing part of the retained output.")}</p>}
  </details>;
}

function V2PreviewBrowser({ client, runID, threadID, currentRunID, sourceService, sourceReadable, candidates }: {
  client: APIClient; runID: string; threadID: string; currentRunID: string;
  sourceService?: ThreadApplicationServiceView; sourceReadable: boolean;
  candidates?: ThreadApplicationServiceDetailView["candidate_urls"];
}) {
  const { t } = useLocale();
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
    if (!sessionID || !ready || session.isError || detail.isError) throw new Error(t("预览浏览器尚未就绪或状态未确认。", "The preview browser is not ready or its status is unconfirmed."));
    return parseApplicationPreview(await client.postControl(`/runs/${encodeURIComponent(runID)}/full-cdp-session/preview`, {
      version: "full_cdp_preview.v1", expected_session_id: sessionID,
    }, `v2-preview-${crypto.randomUUID()}`), runID, sessionID);
  }, onSuccess: setPreview, onError: (failure) => setError(failure instanceof APIRequestError
    ? t("无法读取当前页面，请检查预览是否仍在运行以及浏览器控制权限。", "Could not read the current page. Check that the preview is running and browser control is authorized.") : failure.message) });
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
    if (!observed || actionInFlight.current) throw new Error(t("请先刷新当前页面。", "Refresh the current page first."));
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
    ? t("页面操作结果尚待确认，当前页面或授权状态可能已变化。", "Page action unconfirmed. The page or authorization may have changed.") : failure.message} ${t("请刷新页面预览，核对结果后再发起操作。", "Refresh the page preview and check the result before acting again.")}`) });
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
  return <section className="v2-preview-browser" aria-label={t("预览浏览器控制", "Preview browser controls")}>
      <h3>{t("浏览器", "Browser")}</h3>
      <details className="v2-preview-identity"><summary>{t("浏览器来源", "Browser source")}</summary><p>{t("来源执行 ", "Source run ")}<code>{runID}</code></p></details>
      {!client.hasFullCDPSessionControl ? <p role="status">{t("当前连接未开放托管浏览器。后台服务状态与日志仍可在上方查看；网页预览需支持托管浏览器的服务及当前任务权限。", "Managed browsers are unavailable on this connection. View service status and logs above. Page previews require a service with managed browsers and appropriate task permissions.")}</p> : <>
        {sourceService && (!runUsable || !serviceAllowsOpen) && <p role="status">
          {detail.isLoading ? t("正在核对服务所属执行…", "Checking the service's source run…") : !sourceReadable ? t("服务状态尚未确认，请先重试核对。", "Service status is unconfirmed. Retry the check first.")
            : sourceService.state !== "running" ? t("此后台任务未在运行，可以查看启动输出。", "This background task is not running. View its startup output.")
              : !sourceService.can_stop ? t("保存的运行记录，当前服务状态未确认。请回对话核对服务。", "This is a saved run record. Current service status is unconfirmed; check it in the conversation.")
              : t("服务所属执行已退出或无法确认，不能打开其预览浏览器。", "The source run has exited or is unconfirmed. Its preview browser cannot be opened.")}</p>}
        {candidates && candidates.length > 0 && <div className="v2-preview-candidates" aria-label={t("输出中的候选地址", "Candidate addresses from output")}>
          <p>{t("从启动输出发现以下地址，尚未验证页面是否可用。请选择要打开的地址。", "These addresses were found in startup output. Page availability is unverified. Choose an address to open.")}</p>
          {candidates.map((candidate) => <button key={candidate.url} type="button" aria-pressed={target === candidate.url}
            disabled={active || !runUsable || !serviceAllowsOpen || !localPreviewURL(candidate.url)}
            onClick={() => setTarget(candidate.url)} aria-label={t(`选择地址 ${candidate.url}`, `Select address ${candidate.url}`)}><code>{candidate.url}</code></button>)}
        </div>}
        <div className="v2-preview-address"><label>{t("项目应用地址", "Project app address")}<input aria-label={t("项目应用地址", "Project app address")} type="url" placeholder="http://127.0.0.1:3000" value={target}
          disabled={active || open.isPending} onChange={(event) => setTarget(event.target.value)} /></label>
          <label>{t("浏览器", "Browser")}<select aria-label={t("预览浏览器", "Preview browser")} value={product} disabled={active} onChange={(event) => setProduct(event.target.value as typeof product)}>
            <option value="edge">Microsoft Edge</option><option value="chrome">Google Chrome</option></select></label>
          {!active ? <button type="button" disabled={busy || !canOpen || !localPreviewURL(target.trim())} onClick={() => open.mutate()}><ExternalLink size={16} />{t("打开应用", "Open app")}</button>
            : <button type="button" disabled={busy || current?.state === "closing"} onClick={() => close.mutate()}>{t("关闭浏览器", "Close browser")}</button>}
        </div>
        {!active && <p className="v2-preview-help">{t("填写开发服务显示的本机地址。打开后，当前任务可以操作这个独立浏览器；使用临时浏览器资料，关闭或到期后清理。", "Enter the local address shown by the development service. This task can control the isolated browser once opened. Temporary browser data is cleaned up on close or expiry.")}</p>}
        {!eligible && <div className="v2-preview-help">{t("打开预览需要先确认完全访问权限。", "Confirm full access before opening the preview.")}{runID === currentRunID ? <V2PermissionControl client={client} threadID={threadID} /> : <span>{t("此服务来自历史执行，请回到对应执行核对权限。", "This service belongs to a past run. Check permissions in that run.")}</span>}</div>}
        {eligible && !cdpEnabled && <div className="v2-preview-help">{t("浏览器控制目前关闭。启用后，当前任务能读取、操作此独立浏览器及其网络请求。", "Browser control is disabled. Enable it to let this task read and control the isolated browser and its network requests.")}{runID === currentRunID ? <button disabled={enable.isPending || !runUsable || !client.hasBrowserCDPPermissionControl || !client.hasFullCDPDebug} type="button" onClick={() => enable.mutate()}>{t("允许任务控制预览浏览器", "Allow this task to control the preview browser")}</button>
            : <span>{t("请回到服务所属执行核对浏览器权限。", "Check browser permissions in the service's source run.")}</span>}</div>}
        <div className="v2-preview-status" role="status"><span>{session.isError || detail.isError ? t("无法读取浏览器状态", "Could not read browser status") : current?.state === "ready" ? t("浏览器已就绪", "Browser ready") : current?.state === "starting" ? t("正在启动浏览器…", "Starting browser…")
          : current?.state === "closing" ? t("正在清理浏览器…", "Cleaning up browser…") : current?.state === "failed" ? t("浏览器启动失败", "Browser failed to start") : t("浏览器未运行", "Browser not running")}</span>
          {current?.target_origin && <code>{current.target_origin}</code>}
          {ready && <button type="button" disabled={busy} onClick={() => refresh.mutate()}><RefreshCw size={15} />{t("刷新页面预览", "Refresh page preview")}</button>}</div>
        {error && <p role="alert" className="v2-composer-error">{error}</p>}
        {current?.state === "failed" && <p role="alert">{current.failure_code || t("未能启动，请检查开发服务与浏览器是否可用。", "Could not start. Check development service and browser availability.")}</p>}
        {ready && !session.isError && !detail.isError && <div className="v2-preview-page">
          {busy && <p role="status"><LoaderCircle size={18} className="spin" />{t("正在读取页面…", "Loading page…")}</p>}
          {url && preview && <><p>{preview.page.title || t("应用页面", "App page")}<small>{t("观察于 ", "Observed at ")}{new Date(preview.captured_at).toLocaleTimeString(t("zh-CN", "en-US"))}</small></p>
            <img className="v2-preview-capture" src={url} alt={t(`应用页面：${preview.page.title || preview.canonical_url}`, `App page: ${preview.page.title || preview.canonical_url}`)} />
            <details className="v2-preview-interactions"><summary>{t("操作页面控件（", "Page controls (")}{visibleElements.length}）</summary>
              <p>{t("输入会追加到页面输入框的当前光标位置；只发送给本项目页面。操作后重新读取页面，观察过期时请先刷新。", "Input is inserted at the page field's current cursor and sent only to this project page. The page is read again after each action. Refresh expired observations first.")}</p>
              {visibleElements.map((element) => {
                const editable = (element.tag === "input" && !["button", "submit", "reset", "checkbox", "radio", "file", "password", "hidden"].includes(element.type ?? "text")) || element.tag === "textarea";
                const blocked = element.disabled || ["password", "file", "hidden"].includes(element.type ?? "");
                const label = element.name || element.role || element.tag;
                return <div className="v2-preview-element" key={element.selector}><span>{label}</span>
                  {editable && <input aria-label={t(`页面输入 ${label}`, `Page input ${label}`)} disabled={blocked || busy} value={values[element.selector] ?? ""}
                    onChange={(event) => setValues((currentValues) => ({ ...currentValues, [element.selector]: event.target.value }))} />}
                  <button type="button" disabled={blocked || busy || (editable && !(element.selector in values))}
                    onClick={() => act.mutate({ element, ...(editable ? { value: values[element.selector] } : {}) })}>{editable ? t("输入到页面", "Type into page") : t("点击", "Click")}</button></div>;
              })}</details>
            <details><summary>{t("页面文字", "Page text")}{preview.page.truncated ? t("（部分）", " (partial)") : ""}</summary><pre>{preview.page.text}</pre></details></>}
        </div>}
        {current?.state === "closed" && <p role="status">{t("预览已关闭，浏览器", "Preview closed. Browser ")}{current.process_tree_quiescent && current.profile_cleaned ? t("和临时资料已清理。", " and temporary data cleaned up.") : t("清理状态尚未完整确认。", "Cleanup is not fully confirmed.")}</p>}
      </>}
    </section>;
}
