import { useMemo, useRef, useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { CircleStop, ExternalLink, LoaderCircle, MonitorCog } from "lucide-react";
import type { APIClient } from "../../api/client";
import type {
  FullCDPSessionView,
  RunBrowserCDPPermissionControlView,
  RunBrowserCDPPermissionView,
  RunDetailView,
  ThreadExecutionPermissionView,
} from "../../api/types";
import { V2ConfirmDialog } from "./dialog";

type PermissionMode = ThreadExecutionPermissionView["mode"];

export const browserCDPQueryKey = (runID: string) =>
  ["v2", "run", runID, "browser-cdp-permission"] as const;

export const fullCDPSessionQueryKey = (runID: string) =>
  ["v2", "run", runID, "full-cdp-session"] as const;

function allowsFullCDP(mode: PermissionMode | null): boolean {
  return mode === "full" || mode === "full_access" || mode === "debug";
}

function statusCopy({ runID, mode, permission, loading, failed, controlEnabled,
  fullCDPEnabled, sessionControlEnabled, executionRuntimeAvailable }: {
  runID: string;
  mode: PermissionMode | null;
  permission?: RunBrowserCDPPermissionView;
  loading: boolean;
  failed: boolean;
  controlEnabled: boolean;
  fullCDPEnabled: boolean;
  sessionControlEnabled: boolean;
  executionRuntimeAvailable: boolean;
}): string {
  if (!runID) return mode === null ? "先打开一个任务。" : "当前任务还没有可配置的执行。";
  if (!allowsFullCDP(mode)) {
    return "高风险 CDP 需要在当前任务确认并激活完全访问；受限导航、DOM 与截图不受影响。";
  }
  if (!controlEnabled || !fullCDPEnabled) return "当前运行时未提供完整 CDP 控制。";
  if (loading) return "正在读取当前执行的浏览器权限…";
  if (failed || !permission) return "无法读取当前执行的浏览器权限。";
  if (permission.mode === "full_debug") {
    return permission.runtime_gate_available
      ? sessionControlEnabled
        ? "浏览器控制已开启，可为当前任务打开、查看和关闭独立浏览器。"
        : "浏览器控制权限已开启，当前服务尚未提供独立浏览器。请连接支持托管浏览器的服务后使用预览。"
      : "已选择，但当前运行时尚未授权。";
  }
  if (!executionRuntimeAvailable) {
    return "先重新确认并激活当前任务的完全访问权限。";
  }
  return "当前关闭；内置隔离浏览器仍可导航、读取 DOM 和截图，重新开启需确认高风险控制。";
}

export function V2BrowserCDPControl({ client, runID, permissionMode,
  executionRuntimeAvailable }: {
  client: APIClient;
  runID: string;
  permissionMode: PermissionMode | null;
  executionRuntimeAvailable: boolean;
}) {
  const queryClient = useQueryClient();
  const [confirmOpen, setConfirmOpen] = useState(false);
  const [sessionConfirmOpen, setSessionConfirmOpen] = useState(false);
  const [target, setTarget] = useState("http://127.0.0.1:3000");
  const [product, setProduct] = useState<"chrome" | "edge">("edge");
  const triggerRef = useRef<HTMLButtonElement>(null);
  const sessionTriggerRef = useRef<HTMLButtonElement>(null);
  const query = useQuery({
    queryKey: browserCDPQueryKey(runID),
    queryFn: ({ signal }) => client.get<RunDetailView>(
      `/runs/${encodeURIComponent(runID)}`, {}, signal),
    enabled: Boolean(runID),
    staleTime: 5_000,
  });
  const mutation = useMutation({
    mutationFn: (mode: RunBrowserCDPPermissionView["mode"]) =>
      client.postControl<RunBrowserCDPPermissionControlView>(
        `/runs/${encodeURIComponent(runID)}/browser-cdp-permission`, {
          mode,
          reason: "v2 current task Full CDP control",
          ...(mode === "full_debug" ? { confirm_full_cdp_debug: true } : {}),
        }, `v2-browser-cdp-${globalThis.crypto.randomUUID()}`),
    onSuccess: (result) => {
      queryClient.setQueryData<RunDetailView>(browserCDPQueryKey(runID), (current) =>
        current ? { ...current, browser_cdp_permission: result.browser_cdp_permission } : current);
      if (!queryClient.getQueryData(browserCDPQueryKey(runID))) {
        void queryClient.invalidateQueries({ queryKey: browserCDPQueryKey(runID) });
      }
      setConfirmOpen(false);
    },
  });
  const sessionQuery = useQuery({
    queryKey: fullCDPSessionQueryKey(runID),
    queryFn: ({ signal }) => client.getFullCDPSession(runID, signal),
    enabled: Boolean(runID) && client.hasFullCDPSessionControl,
    refetchInterval: 1_500,
  });
  const session = sessionQuery.data?.session;
  const sessionMutation = useMutation({
    mutationFn: () => client.openFullCDPSession(runID, {
      version: "full_cdp_session.v1",
      target: target.trim(),
      browser: { product, channel: "stable" },
      expected_execution_permission_revision: query.data?.execution_permission.revision ?? 0,
      expected_browser_cdp_permission_revision: query.data?.browser_cdp_permission.revision ?? 0,
      confirm_full_cdp: true,
      reason: "v2 operator-confirmed task browser session",
    }, `v2-full-cdp-open-${globalThis.crypto.randomUUID()}`),
    onSuccess: (result) => {
      queryClient.setQueryData(fullCDPSessionQueryKey(runID), result);
      setSessionConfirmOpen(false);
    },
  });
  const closeMutation = useMutation({
    mutationFn: (current: FullCDPSessionView) => client.closeFullCDPSession(runID, {
      version: "full_cdp_session_close.v1",
      expected_session_id: current.session_id ?? "",
      reason: "operator_closed",
    }, `v2-full-cdp-close-${globalThis.crypto.randomUUID()}`),
    onSuccess: (result) => queryClient.setQueryData(fullCDPSessionQueryKey(runID), result),
  });
  const eligible = allowsFullCDP(permissionMode);
  const browserPermission = query.data?.browser_cdp_permission;
  const checked = eligible && browserPermission?.mode === "full_debug";
  const canDisable = checked && Boolean(runID) && client.hasBrowserCDPPermissionControl;
  const canEnable = !checked && Boolean(runID) && eligible && executionRuntimeAvailable &&
    client.hasBrowserCDPPermissionControl && client.hasFullCDPDebug;
  const disabled = mutation.isPending || query.isLoading || query.isError ||
    !(canDisable || canEnable);
  const activeSession = session?.state === "starting" || session?.state === "ready" ||
    session?.state === "closing";
  const normalizedTarget = target.trim();
  const targetValid = useMemo(() => {
    try {
      const parsed = new URL(normalizedTarget);
      return (parsed.protocol === "http:" || parsed.protocol === "https:") &&
        (parsed.hostname === "127.0.0.1" ||
          parsed.hostname === "::1" || parsed.hostname === "[::1]");
    } catch {
      return false;
    }
  }, [normalizedTarget]);
  const canOpenSession = checked && session?.runtime_available !== false &&
    !activeSession && targetValid && Boolean(query.data?.execution_permission.revision) &&
    Boolean(browserPermission?.revision) &&
    client.hasFullCDPSessionControl;
  const detail = statusCopy({
    runID,
    mode: permissionMode,
    permission: browserPermission,
    loading: query.isLoading,
    failed: query.isError,
    controlEnabled: client.hasBrowserCDPPermissionControl,
    fullCDPEnabled: client.hasFullCDPDebug,
    sessionControlEnabled: client.hasFullCDPSessionControl,
    executionRuntimeAvailable,
  });
  const toggle = () => {
    mutation.reset();
    if (checked) {
      mutation.mutate("restricted");
      return;
    }
    if (canEnable) setConfirmOpen(true);
  };

  return <div className="v2-browser-cdp-control">
    <span className="v2-browser-cdp-icon"><MonitorCog aria-hidden="true" size={18} /></span>
    <div><strong>内置浏览器 · 完整 CDP</strong><p>{detail}</p></div>
    <button aria-checked={checked} aria-label="完整 CDP 控制" className="v2-cdp-switch"
      disabled={disabled} onClick={toggle} ref={triggerRef} role="switch" type="button">
      <i aria-hidden="true" />
    </button>
    {mutation.isError && <p className="v2-browser-cdp-error" role="alert">
      {mutation.error instanceof Error ? mutation.error.message : "完整 CDP 更新失败"}
    </p>}
    <V2ConfirmDialog busy={mutation.isPending} confirmLabel="开启完整 CDP" danger
      description={`完整 CDP 可以读取 Cookie、捕获和修改网络请求、重放请求并调用任意 CDP 方法。控制范围为 Universal-Code 管理的隔离浏览器，系统浏览器和应用界面保持独立。此权限默认随完全访问或调试模式开启，关闭后重新开启需要确认。${client.hasFullCDPSessionControl
        ? "启用后可在“应用预览”中打开独立浏览器。"
        : "当前连接尚未开放托管浏览器。使用支持此能力的连接后，可在“应用预览”中打开独立浏览器。"}`}
      onCancel={() => {
        mutation.reset();
        setConfirmOpen(false);
      }} onConfirm={() => mutation.mutate("full_debug")} open={confirmOpen}
      returnFocusRef={triggerRef} title="开启完整 CDP 控制？" />
    {checked && client.hasFullCDPSessionControl && <section className="v2-full-cdp-session">
      <header><div><strong>任务专属浏览器会话</strong><span>{sessionQuery.isLoading
        ? "正在读取…" : session?.state === "ready" ? "已就绪" : session?.state === "starting"
          ? "正在启动" : session?.state === "closing" ? "正在清理" : session?.state === "failed"
            ? "启动失败" : "未运行"}</span></div>
        {activeSession && session?.session_id ? <button className="danger" disabled={closeMutation.isPending ||
          session.state === "closing"} onClick={() => closeMutation.mutate(session)} type="button">
          {closeMutation.isPending ? <LoaderCircle aria-hidden="true" className="spin" size={14} />
            : <CircleStop aria-hidden="true" size={14} />}关闭并清理</button> : null}
      </header>
      {session?.state === "ready" ? <div className="v2-full-cdp-live">
        <span><i aria-hidden="true" />隔离 Profile · {session.browser?.product ?? product}</span>
        <code>{session.target_origin}</code>
        {session.expires_at && <small>最迟 {new Date(session.expires_at).toLocaleTimeString()} 自动关闭</small>}
      </div> : <div className="v2-full-cdp-launch">
        <label><span>本地目标</span><input aria-label="完整 CDP 本地目标" disabled={activeSession}
          onChange={(event) => setTarget(event.target.value)} spellCheck={false} value={target} /></label>
        <label><span>浏览器</span><select aria-label="完整 CDP 浏览器" disabled={activeSession}
          onChange={(event) => setProduct(event.target.value as "chrome" | "edge")} value={product}>
          <option value="edge">Microsoft Edge</option><option value="chrome">Google Chrome</option>
        </select></label>
        <button disabled={!canOpenSession || sessionMutation.isPending} onClick={() => {
          sessionMutation.reset();
          setSessionConfirmOpen(true);
        }} ref={sessionTriggerRef} type="button"><ExternalLink aria-hidden="true" size={14} />启动会话</button>
      </div>}
      {!targetValid && !activeSession && <p className="v2-browser-cdp-error">完整 CDP 当前只接受一个明确的本机 HTTP(S) origin。</p>}
      {(sessionMutation.isError || closeMutation.isError || sessionQuery.isError) &&
        <p className="v2-browser-cdp-error" role="alert">{sessionMutation.error instanceof Error
          ? sessionMutation.error.message : closeMutation.error instanceof Error
            ? closeMutation.error.message : "完整 CDP 会话操作失败"}</p>}
      {session?.state === "failed" && <p className="v2-browser-cdp-error">失败代码：{session.failure_code ?? "unknown"}</p>}
      {session?.state === "closed" && <p className="v2-full-cdp-receipt">清理证明：CDP {session.cdp_closed ? "已关闭" : "未确认"} · 进程树 {session.process_tree_quiescent ? "已静止" : "未确认"} · Profile {session.profile_cleaned ? "已删除" : "待清理"}</p>}
    </section>}
    <V2ConfirmDialog busy={sessionMutation.isPending} confirmLabel="启动隔离浏览器" danger
      description={`Universal-Code 将启动一个使用临时 Profile 的独立无头浏览器，并在 5 分钟内向当前任务开放完整 CDP。目标限定为 ${normalizedTarget}；关闭、撤权、任务终止或超时都会回收进程树并删除临时 Profile。`}
      onCancel={() => setSessionConfirmOpen(false)} onConfirm={() => sessionMutation.mutate()}
      open={sessionConfirmOpen} returnFocusRef={sessionTriggerRef} title="启动完整 CDP 会话？" />
  </div>;
}
