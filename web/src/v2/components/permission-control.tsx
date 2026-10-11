import { useCallback, useEffect, useId, useRef, useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { ChevronDown, Globe2, X } from "lucide-react";
import type { APIClient } from "../../api/client";
import type { ThreadExecutionPermissionControlView } from "../../api/types";
import { useLocale } from "../../lib/locale";
import { v2QueryKeys } from "../query-keys";
import { V2ApprovalModeControl } from "./approval-mode-control";
import type { ApprovalModeSelectionRequest } from "./approval-mode-contract";
import { browserCDPQueryKey, V2BrowserCDPControl } from "./browser-cdp-control";
import { V2RunNetworkAuthorityControl } from "./run-network-authority-control";

function effectCopy(result: ThreadExecutionPermissionControlView, t: ReturnType<typeof useLocale>["t"]): string {
  if (result.current_run_effect === "deferred") return t("当前执行保持不变，选择用于下一次执行",
    "The current run is unchanged; this preference applies to the next run.");
  if (result.execution_permission.approval_mode === "full" && result.execution_permission.full_activation !== "active") {
    return t("完全访问偏好已保存，当前会话尚未激活。请在任务权限中查看可用状态",
      "Your Full access preference is saved and awaiting activation in this session. Check availability in task permissions.");
  }
  if (result.current_run_effect === "paused_and_applied") return t("已安全暂停当前执行并应用",
    "The current run was safely paused and the preference applied.");
  if (result.current_run_effect === "applied" || result.current_run_synchronized) return t("已应用到当前和后续执行",
    "Applied to the current and future runs.");
  return t("已用于此对话的后续执行", "Applied to future runs in this conversation.");
}

type PermissionControlProps = {
  client: APIClient;
  threadID: string;
  variant?: "menu" | "settings";
  onOpenModelSettings?: () => void;
};

export function V2PermissionControl(props: PermissionControlProps) {
  // Target changes discard confirmation/pending/error UI. An already submitted
  // mutation keeps its original instance and can only update that Thread's cache.
  return <ThreadPermissionControl key={props.threadID} {...props} />;
}

function ThreadPermissionControl({ client, threadID, variant = "menu", onOpenModelSettings }: PermissionControlProps) {
  const { t } = useLocale();
  const queryClient = useQueryClient();
  const [networkOpen, setNetworkOpen] = useState(false);
  const networkRef = useRef<HTMLDivElement>(null);
  const networkTriggerRef = useRef<HTMLButtonElement>(null);
  const networkPanelRef = useRef<HTMLElement>(null);
  const networkConfirmationOpenRef = useRef(false);
  const onNetworkConfirmationOpenChange = useCallback((open: boolean) => {
    networkConfirmationOpenRef.current = open;
  }, []);
  const networkID = useId();
  useEffect(() => {
    if (!networkOpen || variant !== "menu") return;
    networkPanelRef.current?.focus();
    const outside = (event: MouseEvent) => {
      if (!networkConfirmationOpenRef.current && !networkRef.current?.contains(event.target as Node)) {
        setNetworkOpen(false);
      }
    };
    const escape = (event: KeyboardEvent) => {
      if (event.key !== "Escape" || event.defaultPrevented || networkConfirmationOpenRef.current) return;
      event.preventDefault(); setNetworkOpen(false); networkTriggerRef.current?.focus();
    };
    window.addEventListener("click", outside);
    window.addEventListener("keydown", escape);
    return () => {
      window.removeEventListener("click", outside);
      window.removeEventListener("keydown", escape);
    };
  }, [networkOpen, variant]);
  const query = useQuery({
    queryKey: v2QueryKeys.permission(threadID),
    queryFn: ({ signal }) => client.getThreadExecutionPermission(threadID, signal),
    enabled: Boolean(threadID), staleTime: 10_000,
  });
  const mutation = useMutation({
    mutationFn: (request: ApprovalModeSelectionRequest) => client.changeThreadExecutionPermission(threadID, {
      mode: request.mode, confirm_full: request.confirmFull, reason: "Thread approval preference selection",
    }, `thread-approval-preference-${globalThis.crypto.randomUUID()}`),
    onSuccess: (result) => {
      queryClient.setQueryData(v2QueryKeys.permission(threadID), result);
      void queryClient.invalidateQueries({ queryKey: v2QueryKeys.thread(threadID) });
      if (result.current_run_id) {
        void queryClient.invalidateQueries({ queryKey: ["run", result.current_run_id] });
        void queryClient.invalidateQueries({ queryKey: browserCDPQueryKey(result.current_run_id) });
      }
    },
  });
  if (!threadID) return <p className="v2-settings-empty">{t("先从侧栏打开一个对话。", "Open a conversation from the sidebar first.")}</p>;
  if (query.isPending) return <span role="status">{t("正在读取权限…", "Reading permissions…")}</span>;
  if (query.isError || !query.data) return <p role="alert">{t("无法读取权限设置", "Unable to read permission settings")}</p>;
  const permission = query.data.execution_permission;
  const network = networkOpen && <V2RunNetworkAuthorityControl key={`${threadID}:${query.data.current_run_id ?? ""}`}
    client={client} runID={query.data.current_run_id ?? ""} threadID={threadID}
    onOpenModelSettings={onOpenModelSettings} onConfirmationOpenChange={onNetworkConfirmationOpenChange} />;
  return <div className={`v2-permission-host is-${variant}`}
    style={variant === "menu" ? { display: "flex", alignItems: "center", flexWrap: "wrap", gap: 8, minWidth: 0, maxWidth: "100%" } : undefined}>
    <V2ApprovalModeControl mode={permission.approval_mode} fullActivation={permission.full_activation}
      fullUnavailableReason={permission.full_unavailable_reason} pending={mutation.isPending}
      disabled={!client.hasExecutionPermissionControl} variant={variant}
      error={mutation.isError ? mutation.error instanceof Error ? mutation.error.message
        : t("权限更新失败", "Failed to update permissions") : undefined}
      onRequestChange={(request) => { mutation.reset(); mutation.mutate(request); }} />
    {variant === "settings" && <>
      <p role="status">{effectCopy(query.data, t)}</p>
      <V2BrowserCDPControl client={client} permissionMode={permission.mode}
        executionRuntimeAvailable={permission.full_activation === "active"}
        runID={query.data.current_run_id ?? ""} />
    </>}
    {variant === "menu" ? <div className="v2-network-scope-control" ref={networkRef}>
      <button aria-label={t("网页访问与搜索", "Web access and search")}
        aria-controls={networkOpen ? networkID : undefined} aria-expanded={networkOpen} aria-haspopup="dialog"
        className="v2-composer-chip" onClick={() => {
          if (networkConfirmationOpenRef.current) return;
          setNetworkOpen((value) => !value);
        }} ref={networkTriggerRef} type="button">
        <Globe2 aria-hidden="true" size={14} />{t("网页访问与搜索", "Web & search")}
        <ChevronDown aria-hidden="true" size={13} />
      </button>
      {networkOpen && <section aria-label={t("网页访问与搜索", "Web access and search")}
        className="v2-network-popover" id={networkID} ref={networkPanelRef} role="dialog" tabIndex={-1}>
        <header><strong>{t("网页访问与搜索", "Web access and search")}</strong>
          <button aria-label={t("关闭网页访问与搜索", "Close web access and search")}
            className="v2-composer-icon" onClick={() => {
              if (networkConfirmationOpenRef.current) return;
              setNetworkOpen(false); networkTriggerRef.current?.focus();
            }} type="button"><X aria-hidden="true" size={14} /></button></header>
        <div className="v2-permission-network">{network}</div>
      </section>}
    </div> : <details className="v2-permission-network" open={networkOpen}
      onToggle={(event) => setNetworkOpen(event.currentTarget.open)}>
      <summary>{t("网页访问与搜索", "Web access and search")}</summary>
      {network}
    </details>}
  </div>;
}
