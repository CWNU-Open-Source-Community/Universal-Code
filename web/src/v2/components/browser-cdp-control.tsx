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
import { useLocale, type LocaleText } from "../../lib/locale";

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
}, t: LocaleText): string {
  if (!runID) return mode === null ? t("先打开一个任务。", "Open a task first.") : t("当前任务还没有可配置的执行。", "This task has no run to configure yet.");
  if (!allowsFullCDP(mode)) {
    return t("高风险 CDP 需要在当前任务确认并激活完全访问；受限导航、DOM 与截图不受影响。", "High-risk CDP requires confirmed, active Full access for this task. Restricted navigation, DOM access and screenshots remain available.");
  }
  if (!controlEnabled || !fullCDPEnabled) return t("当前运行时未提供完整 CDP 控制。", "This runtime does not provide full CDP control.");
  if (loading) return t("正在读取当前执行的浏览器权限…", "Loading browser permissions for this run…");
  if (failed || !permission) return t("无法读取当前执行的浏览器权限。", "Could not load browser permissions for this run.");
  if (permission.mode === "full_debug") {
    return permission.runtime_gate_available
      ? sessionControlEnabled
        ? t("浏览器控制已开启，可为当前任务打开、查看和关闭独立浏览器。", "Browser control is enabled. Open, inspect and close a separate browser for this task.")
        : t("浏览器控制权限已开启，当前服务尚未提供独立浏览器。请连接支持托管浏览器的服务后使用预览。", "Browser control is permitted, but this service has no managed browser. Connect to a service with managed browser support to use the preview.")
      : t("已选择，但当前运行时尚未授权。", "Selected, but not authorized by this runtime yet.");
  }
  if (!executionRuntimeAvailable) {
    return t("先重新确认并激活当前任务的完全访问权限。", "Reconfirm and activate Full access for this task first.");
  }
  return t("当前关闭；内置隔离浏览器仍可导航、读取 DOM 和截图，重新开启需确认高风险控制。", "Currently off. The isolated browser can still navigate, read the DOM and take screenshots. Re-enabling requires confirmation of high-risk controls.");
}

export function V2BrowserCDPControl({ client, runID, permissionMode,
  executionRuntimeAvailable }: {
  client: APIClient;
  runID: string;
  permissionMode: PermissionMode | null;
  executionRuntimeAvailable: boolean;
}) {
  const { t, locale } = useLocale();
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
  }, t);
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
    <div><strong>{t("内置浏览器 · 完整 CDP", "Built-in browser · Full CDP")}</strong><p>{detail}</p></div>
    <button aria-checked={checked} aria-label={t("完整 CDP 控制", "Full CDP control")} className="v2-cdp-switch"
      disabled={disabled} onClick={toggle} ref={triggerRef} role="switch" type="button">
      <i aria-hidden="true" />
    </button>
    {mutation.isError && <p className="v2-browser-cdp-error" role="alert">
      {mutation.error instanceof Error ? mutation.error.message : t("完整 CDP 更新失败", "Could not update full CDP")}
    </p>}
    <V2ConfirmDialog busy={mutation.isPending} confirmLabel={t("开启完整 CDP", "Enable full CDP")} danger
      description={`${t("完整 CDP 可以读取 Cookie、捕获和修改网络请求、重放请求并调用任意 CDP 方法。控制范围为 Universal-Code 管理的隔离浏览器，系统浏览器和应用界面保持独立。此权限默认随完全访问或调试模式开启，关闭后重新开启需要确认。", "Full CDP can read cookies, capture, modify and replay network requests, and call any CDP method. It controls an isolated browser managed by Universal-Code; your system browser and the app remain separate. It is enabled by default with full access or debug mode. Re-enabling it after disabling requires confirmation. ")}${client.hasFullCDPSessionControl
        ? t("启用后可在“应用预览”中打开独立浏览器。", "Once enabled, open a separate browser in App preview.")
        : t("当前连接尚未开放托管浏览器。使用支持此能力的连接后，可在“应用预览”中打开独立浏览器。", "This connection has no managed browser. Use a supported connection to open a separate browser in App preview.")}`}
      onCancel={() => {
        mutation.reset();
        setConfirmOpen(false);
      }} onConfirm={() => mutation.mutate("full_debug")} open={confirmOpen}
      returnFocusRef={triggerRef} title={t("开启完整 CDP 控制？", "Enable full CDP control?")} />
    {checked && client.hasFullCDPSessionControl && <section className="v2-full-cdp-session">
      <header><div><strong>{t("任务专属浏览器会话", "Task browser session")}</strong><span>{sessionQuery.isLoading
        ? t("正在读取…", "Loading…") : session?.state === "ready" ? t("已就绪", "Ready") : session?.state === "starting"
          ? t("正在启动", "Starting") : session?.state === "closing" ? t("正在清理", "Cleaning up") : session?.state === "failed"
            ? t("启动失败", "Startup failed") : t("未运行", "Not running")}</span></div>
        {activeSession && session?.session_id ? <button className="danger" disabled={closeMutation.isPending ||
          session.state === "closing"} onClick={() => closeMutation.mutate(session)} type="button">
          {closeMutation.isPending ? <LoaderCircle aria-hidden="true" className="spin" size={14} />
            : <CircleStop aria-hidden="true" size={14} />}{t("关闭并清理", "Close and clean up")}</button> : null}
      </header>
      {session?.state === "ready" ? <div className="v2-full-cdp-live">
        <span><i aria-hidden="true" />{t("隔离 Profile · ", "Isolated profile · ")}{session.browser?.product ?? product}</span>
        <code>{session.target_origin}</code>
        {session.expires_at && <small>{t(`最迟 ${new Date(session.expires_at).toLocaleTimeString(locale)} 自动关闭`, `Closes automatically by ${new Date(session.expires_at).toLocaleTimeString(locale)}`)}</small>}
      </div> : <div className="v2-full-cdp-launch">
        <label><span>{t("本地目标", "Local target")}</span><input aria-label={t("完整 CDP 本地目标", "Full CDP local target")} disabled={activeSession}
          onChange={(event) => setTarget(event.target.value)} spellCheck={false} value={target} /></label>
        <label><span>{t("浏览器", "Browser")}</span><select aria-label={t("完整 CDP 浏览器", "Full CDP browser")} disabled={activeSession}
          onChange={(event) => setProduct(event.target.value as "chrome" | "edge")} value={product}>
          <option value="edge">Microsoft Edge</option><option value="chrome">Google Chrome</option>
        </select></label>
        <button disabled={!canOpenSession || sessionMutation.isPending} onClick={() => {
          sessionMutation.reset();
          setSessionConfirmOpen(true);
        }} ref={sessionTriggerRef} type="button"><ExternalLink aria-hidden="true" size={14} />{t("启动会话", "Start session")}</button>
      </div>}
      {!targetValid && !activeSession && <p className="v2-browser-cdp-error">{t("完整 CDP 当前只接受一个明确的本机 HTTP(S) origin。", "Full CDP currently accepts one explicit local HTTP(S) origin.")}</p>}
      {(sessionMutation.isError || closeMutation.isError || sessionQuery.isError) &&
        <p className="v2-browser-cdp-error" role="alert">{sessionMutation.error instanceof Error
          ? sessionMutation.error.message : closeMutation.error instanceof Error
            ? closeMutation.error.message : t("完整 CDP 会话操作失败", "Full CDP session action failed")}</p>}
      {session?.state === "failed" && <p className="v2-browser-cdp-error">{t("失败代码：", "Failure code: ")}{session.failure_code ?? "unknown"}</p>}
      {session?.state === "closed" && <p className="v2-full-cdp-receipt">{t("清理证明：CDP ", "Cleanup receipt: CDP ")}{session.cdp_closed ? t("已关闭", "Closed") : t("未确认", "Unconfirmed")} {t(" · 进程树 ", " · Process tree ")}{session.process_tree_quiescent ? t("已静止", "Quiescent") : t("未确认", "Unconfirmed")} · Profile {session.profile_cleaned ? t("已删除", "Deleted") : t("待清理", "Cleanup pending")}</p>}
    </section>}
    <V2ConfirmDialog busy={sessionMutation.isPending} confirmLabel={t("启动隔离浏览器", "Start isolated browser")} danger
      description={t(`Universal-Code 将启动一个使用临时 Profile 的独立无头浏览器，并在 5 分钟内向当前任务开放完整 CDP。目标限定为 ${normalizedTarget}；关闭、撤权、任务终止或超时都会回收进程树并删除临时 Profile。`, `Universal-Code will start an isolated headless browser with a temporary profile and allow full CDP for this task for 5 minutes, restricted to ${normalizedTarget}. Closing it, revoking access, ending the task or reaching the timeout stops the process tree and deletes the temporary profile.`)}
      onCancel={() => setSessionConfirmOpen(false)} onConfirm={() => sessionMutation.mutate()}
      open={sessionConfirmOpen} returnFocusRef={sessionTriggerRef} title={t("启动完整 CDP 会话？", "Start full CDP session?")} />
  </div>;
}
