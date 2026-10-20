import { useEffect, useMemo, useRef, useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Check, ChevronDown, Globe2, LoaderCircle, ShieldCheck } from "lucide-react";
import type { APIClient } from "../../api/client";
import type { ProviderSearchReadinessView, RunDetailView, WebAccessMode } from "../../api/types";
import { useLocale, type LocaleText } from "../../lib/locale";
import { v2QueryKeys } from "../query-keys";
import { browserCDPQueryKey } from "./browser-cdp-control";
import { V2ConfirmDialog } from "./dialog";
import { V2SearchDiagnosticsControl } from "./search-diagnostics-control";
import { V2WebAccessModeSelect, webAccessModeDetail, webAccessModeLabel } from "./web-access-mode";
import {
  canonicalizeExactNetworkTargets,
  exactNetworkTargetLooksValid,
  parseExactNetworkTargets,
} from "./network-scope-control";

function canExpand(status: RunDetailView["run"]["status"] | undefined): boolean {
  return status === "created" || status === "paused";
}

function readinessLabel(value: ProviderSearchReadinessView | undefined, t: LocaleText): string {
  if (!value) return t("检查搜索…", "Checking search…");
  if (value.state === "ready") return t("搜索配置就绪", "Search configured");
  if (value.state === "network_disabled") return t("搜索未联网", "Search offline");
  if (value.state === "missing_allowlist") return t("搜索缺目标", "Search target missing");
  if (value.state === "provider_unqualified") return t("搜索待验证", "Search unverified");
  return t("搜索不可用", "Search unavailable");
}

function readinessDetail(value: ProviderSearchReadinessView | undefined, t: LocaleText): string {
  if (!value) return t("正在核对当前任务的模型连接与网页访问范围。", "Checking this task's model connection and web access scope.");
  switch (value.reason) {
  case "run_network_disabled":
    return t("当前任务已禁止网页搜索。选择规则搜索或完全访问后，可使用已配置的搜索服务。", "Web search is disabled for this task. Choose Rule-based search or Full access to use configured search services.");
  case "search_endpoint_not_allowlisted":
    return value.required_target
      ? t(`搜索后端需要明确允许 ${value.required_target}；未列出的主机仍会被拒绝。`, `The search backend needs explicit access to ${value.required_target}; unlisted hosts remain blocked.`)
      : t("当前白名单没有覆盖搜索后端；请追加后端要求的精确 HTTPS 主机。", "The allowlist does not cover the search backend. Add its exact required HTTPS host.");
  case "provider_native_qualification_required":
    return t("供应商声明支持原生搜索，当前配置还需通过一次实际搜索验证。", "The provider declares native search support. Verify this configuration with an actual search.");
  case "provider_native_qualification_failed":
    switch (value.detail_code) {
    case "transport_unavailable":
      return t("供应商原生搜索在连接或超时边界内没有完成；普通对话仍可继续，可重试搜索或配置后备搜索源。", "Native search did not finish within connection or timeout limits. Conversation remains available. Retry search or configure a fallback source.");
    case "tool_unsupported":
      return t("当前供应商端点不支持已知的 Responses 原生搜索工具类型；普通模型对话仍可继续。", "This provider endpoint does not support the known Responses native search tool. Ordinary model conversation remains available.");
    case "provider_rejected":
      return t("供应商拒绝了原生搜索请求；请检查模型、额度与供应商搜索策略。", "The provider rejected native search. Check the model, quota and provider search policy.");
    case "response_invalid":
      return t("供应商返回的原生搜索结果不符合可验证协议；该能力已停止发布。", "Native search returned an unverifiable response. This capability is no longer advertised.");
    default:
      return t("原生搜索的有界验证未通过；普通模型对话仍可与搜索能力分开使用。", "Bounded native search verification failed. Ordinary model conversation remains available independently.");
    }
  case "no_active_run":
    return t("当前尚无可配置搜索权限的执行。发送下一条消息后，可为新执行确认搜索权限。", "There is no run to configure search permissions for yet. Send a message, then confirm permissions for the new run.");
  case "model_provider_unavailable":
    return t("当前执行使用的模型连接不可用，请到模型设置验证连接。", "This run's model connection is unavailable. Verify it in model settings.");
  case "provider_search_policy_disabled":
    return t("当前供应商明确关闭了搜索策略。", "The provider explicitly disabled its search policy.");
  case "search_backend_not_configured":
    return t("当前选择的搜索后端尚未配置；选择 SearXNG 时需要提供外部搜索服务地址。", "The selected search backend is not configured. SearXNG requires an external search service URL.");
  case "provider_search_configuration_invalid":
    return t("当前供应商的搜索声明与传输配置不一致。", "The provider's search declaration does not match its transport configuration.");
  default:
    return value.search_policy === "provider_native"
      ? t(`${value.provider || "当前供应商"} 的托管搜索配置与网络授权已就绪，可发起搜索。搜索使用供应商接口；直接读取网页按下方访问范围审批。`, `${value.provider || "The current provider"} is configured and authorized for hosted search through its API. Direct page reads follow the access rules below.`)
      : t(`${value.search_policy === "web" ? "普通网页搜索（DuckDuckGo）" : value.search_policy === "searxng" ? "外部搜索服务（SearXNG）" : "网页搜索后端"}的配置与网络授权已就绪；实际搜索结果以本次工具返回为准。`, `${value.search_policy === "web" ? "Web search (DuckDuckGo)" : value.search_policy === "searxng" ? "External search service (SearXNG)" : "The web search backend"} is configured and authorized. Results depend on the actual search response.`);
  }
}

function remediationLabel(value: ProviderSearchReadinessView | undefined, t: LocaleText): string {
  switch (value?.remediation) {
  case "enable_network_allowlist": return t("选择规则搜索或完全访问以启用网页搜索", "Choose Rule-based search or Full access to enable web search");
  case "add_required_target": return t("把所需主机加入当前执行的允许范围", "Add the required host to this run's allowed scope");
  case "qualify_provider_search": return t("发起一次搜索，验证供应商搜索能力", "Run a search to verify provider search support");
  case "submit_to_create_successor": return t("发送下一条消息后配置新执行", "Send a message, then configure the new run");
  case "configure_search_provider": return t("到模型设置检查供应商与搜索后端", "Check the provider and search backend in model settings");
  case "enable_provider_search": return t("到模型设置启用搜索策略", "Enable the search policy in model settings");
  case "repair_provider_configuration": return t("到模型设置核对连接与搜索能力", "Check connection and search support in model settings");
  default: return t("配置与网络授权允许发起搜索", "Configuration and network permissions allow search");
  }
}

export function V2RunNetworkAuthorityControl({ client, threadID = "", runID,
  variant = "settings", onOpenModelSettings, onConfirmationOpenChange, onWebAccessModeChange }: {
  client: APIClient;
  threadID?: string;
  runID: string;
  variant?: "menu" | "settings";
  onOpenModelSettings?: () => void;
  // Keep this instance's host open while its confirmation is portalled to body.
  onConfirmationOpenChange?: (open: boolean) => void;
  onWebAccessModeChange?: (mode: WebAccessMode) => void;
}) {
  const { t } = useLocale();
  const queryClient = useQueryClient();
  const [draft, setDraft] = useState("");
  const [confirmOpen, setConfirmOpen] = useState(false);
  useEffect(() => {
    onConfirmationOpenChange?.(confirmOpen);
    return () => onConfirmationOpenChange?.(false);
  }, [confirmOpen, onConfirmationOpenChange]);
  const [open, setOpen] = useState(false);
  const shellRef = useRef<HTMLDivElement>(null);
  const menuTriggerRef = useRef<HTMLButtonElement>(null);
  const menuPanelRef = useRef<HTMLElement>(null);
  const confirmTriggerRef = useRef<HTMLButtonElement>(null);
  const query = useQuery({
    queryKey: browserCDPQueryKey(runID),
    queryFn: ({ signal }) => client.get<RunDetailView>(
      `/runs/${encodeURIComponent(runID)}`, {}, signal),
    enabled: Boolean(runID),
    staleTime: 5_000,
  });
  const readinessQuery = useQuery({
    queryKey: v2QueryKeys.searchReadiness(threadID),
    queryFn: ({ signal }) => client.providerSearchReadiness(threadID, signal),
    enabled: Boolean(threadID),
    staleTime: 5_000,
    // Qualification and a real hosted-search request can change the in-memory
    // readiness cache while a Turn is running. Polling this read-only local
    // projection prevents the Composer from continuing to show the stale
    // pre-flight "待验证" state after a successful probe or a failed request.
    refetchInterval: 5_000,
  });
  const networkScope = query.data?.mode.scope;
  const legacyTargets = networkScope?.network_mode === "allowlist" ? networkScope.allowed_targets ?? [] : [];
  const webMode: WebAccessMode = networkScope?.web_access_mode
    ?? (legacyTargets.includes("public_https") ? "full" : "rules");
  const current = webMode === "rules" ? networkScope?.web_allowed_targets
    ?? legacyTargets.filter((target) => target !== "public_https") : [];
  const publicHTTPS = webMode === "full";
  useEffect(() => {
    if (query.isSuccess) onWebAccessModeChange?.(webMode);
  }, [query.isSuccess, webMode, onWebAccessModeChange]);
  const rawRequested = useMemo(() => parseExactNetworkTargets(draft), [draft]);
  const invalid = rawRequested.filter((target) => !exactNetworkTargetLooksValid(target));
  const requested = useMemo(() => invalid.length === 0
    ? canonicalizeExactNetworkTargets(rawRequested) : [], [invalid.length, rawRequested]);
  const additions = requested.filter((target) => !current.includes(target));
  const mutable = canExpand(query.data?.run.status);
  const mutation = useMutation({
    mutationFn: (selection: { mode: WebAccessMode; targets: string[] }) => client.setRunWebAccess(runID, {
      version: "run_web_access_control.v1",
      expected_mode_revision: query.data?.mode.revision ?? 0,
      web_access_mode: selection.mode,
      web_allowed_targets: selection.mode === "rules" ? selection.targets : [],
      reason: "v2 operator-selected web access",
    }, `v2-run-web-access-${globalThis.crypto.randomUUID()}`),
    onSuccess: (result) => {
      queryClient.setQueryData<RunDetailView>(browserCDPQueryKey(runID), (value) =>
        value ? { ...value, mode: result.mode } : value);
      if (threadID) {
        void queryClient.invalidateQueries({ queryKey: v2QueryKeys.searchReadiness(threadID) });
        void queryClient.invalidateQueries({ queryKey: v2QueryKeys.thread(threadID) });
      }
      void queryClient.invalidateQueries({ queryKey: ["run", runID] });
      setDraft("");
      setConfirmOpen(false);
    },
  });
  useEffect(() => {
    if (open) menuPanelRef.current?.focus();
  }, [open]);
  useEffect(() => {
    if (!open || confirmOpen) return;
    const closeOutside = (event: MouseEvent) => {
      if (!shellRef.current?.contains(event.target as Node)) {
        setOpen(false);
      }
    };
    const closeOnEscape = (event: KeyboardEvent) => {
      if (event.key === "Escape") {
        event.preventDefault();
        setOpen(false);
        requestAnimationFrame(() => menuTriggerRef.current?.focus());
      }
    };
    window.addEventListener("click", closeOutside);
    window.addEventListener("keydown", closeOnEscape);
    return () => {
      window.removeEventListener("click", closeOutside);
      window.removeEventListener("keydown", closeOnEscape);
    };
  }, [open, confirmOpen]);
  const begin = () => {
    mutation.reset();
    if (mutable && additions.length > 0 && invalid.length === 0) setConfirmOpen(true);
  };
  const status = !runID ? t("先打开一个任务。", "Open a task first.")
    : query.isLoading ? t("正在读取当前执行的网页访问范围…", "Loading this run's web access scope…")
      : query.isError ? t("无法读取网页访问范围。", "Could not load web access scope.")
        : mutable ? t("修改会应用到当前任务及其后续执行。", "Changes apply to this task and its subsequent runs.")
          : t("暂停执行并等待资源释放后可修改；已结束的执行可发送新消息继续。", "Pause the run and wait for resources to be released before changing this. Send a message to continue a finished run.");
  const readiness = readinessQuery.data;
  const canPrefillRequiredTarget = Boolean(webMode === "rules" && readiness?.required_target && mutable &&
    !current.includes(readiness.required_target));

  const body = <>
    <header><span><Globe2 aria-hidden="true" size={18} /></span><div>
      <strong>{t("当前任务网页访问", "Current task web access")}</strong><p>{status}</p></div>
      <span className="v2-network-count">{query.isError ? t("范围未知", "Scope unknown") : query.isPending ? t("读取中", "Loading")
        : webAccessModeLabel(webMode, t)}</span>
    </header>
    <div className="v2-web-access-mode-row"><label>{t("网页搜索", "Web search")}</label>
      <V2WebAccessModeSelect mode={webMode} label={t("当前任务网页搜索模式", "Current task web search mode")}
        disabled={!runID || !query.isSuccess || !mutable || mutation.isPending || !client.hasControl}
        onChange={(next) => { mutation.reset(); mutation.mutate({ mode: next, targets: next === "rules" ? current : [] }); }} />
    </div>
    {query.isSuccess && <p className="v2-network-note">{webAccessModeDetail(webMode, t)}</p>}
    {threadID && webMode !== "disabled" && <div className={`v2-search-readiness state-${readiness?.state ?? "loading"}`}
      role="status"><span><strong>{readiness?.search_policy === "provider_native" ? t("供应商搜索", "Provider search") : t("网页搜索", "Web search")} · {readinessQuery.isError
        ? t("无法检查", "Could not check") : readinessLabel(readiness, t)}</strong>
        <small>{readinessQuery.isError
          ? t("搜索配置状态暂时无法读取。可先核对下方已保存的网页访问范围。", "Search configuration is temporarily unavailable. Review the saved web access scope below.")
          : readinessDetail(readiness, t)}</small></span>
      {!readinessQuery.isError && <em>{remediationLabel(readiness, t)}</em>}</div>}
    {readiness && webMode !== "disabled" && <V2SearchDiagnosticsControl client={client} readiness={readiness}
      onOpenModelSettings={onOpenModelSettings} />}
    {webMode === "rules" && query.isSuccess && <p className="v2-network-note">{t("下方域名允许直接读取网页。范围外的读取按请求批准。", "Pages on the domains below can be read directly. Reads outside this scope require approval.")}</p>}
    {webMode === "rules" && current.length > 0 && <div aria-label={t("当前允许的 HTTPS 主机", "Currently allowed HTTPS hosts")} className="v2-network-targets">
      {current.map((target) => <code key={target}>{target}</code>)}
    </div>}
    {canPrefillRequiredTarget && <button className="v2-network-prefill" onClick={() =>
      setDraft(readiness?.required_target ?? "")} type="button">
      {t("使用所需主机：", "Use required host: ")}<code>{readiness?.required_target}</code>
    </button>}
    {webMode === "rules" && <div className="v2-network-expander">
      <label><span>{t("追加 HTTPS 主机", "Add HTTPS hosts")}</span><textarea aria-label={t("追加允许的 HTTPS 主机", "Add allowed HTTPS hosts")}
        disabled={!runID || query.isLoading || query.isError || !mutable || mutation.isPending}
        onChange={(event) => setDraft(event.target.value)}
        placeholder={"search.example.org\ndocs.example.com"} rows={3} spellCheck={false}
        value={draft} /></label>
      <button disabled={!runID || !mutable || additions.length === 0 || invalid.length > 0 ||
        mutation.isPending || !client.hasControl} onClick={begin} ref={confirmTriggerRef} type="button">
        {mutation.isPending ? <LoaderCircle aria-hidden="true" className="spin" size={14} />
          : <Check aria-hidden="true" size={14} />}{t("审核并追加", "Review and add")}</button>
    </div>}
    {webMode === "rules" && invalid.length > 0 && <p className="v2-network-error" role="alert">
      {t("只接受无路径、查询或通配符的公网 HTTPS 主机：", "Only public HTTPS hosts without paths, queries or wildcards are accepted: ")}{invalid.join(t("、", ", "))}
    </p>}
    {webMode === "rules" && requested.length > 0 && additions.length === 0 && invalid.length === 0 &&
      <p className="v2-network-note"><ShieldCheck aria-hidden="true" size={14} />{t("这些主机已在当前范围内。", "These hosts are already in the current scope.")}</p>}
    {mutation.isError && <p className="v2-network-error" role="alert">
      {mutation.error instanceof Error ? mutation.error.message : t("网页访问范围更新失败", "Could not update web access scope")}
    </p>}
  </>;

  return <div className={`v2-run-network-control is-${variant}`} ref={shellRef}>
    {variant === "menu" ? <>
      <button aria-expanded={open} aria-haspopup="dialog" aria-label={t("网页访问状态", "Web access status")}
        className="v2-composer-chip" disabled={!runID} onClick={() => {
          if (!confirmOpen) setOpen((value) => !value);
        }}
        ref={menuTriggerRef} type="button">
        {publicHTTPS || current.length > 0 ? <Globe2 aria-hidden="true" size={14} />
          : <ShieldCheck aria-hidden="true" size={14} />}
        {query.isError ? t("范围未知", "Scope unknown") : query.isPending ? t("读取中", "Loading") : webAccessModeLabel(webMode, t)}
        <ChevronDown aria-hidden="true" size={13} />
      </button>
      {open && <section aria-label={t("当前执行网页访问", "Current run web access")}
        className="v2-run-network-popover v2-run-network-authority"
        ref={menuPanelRef} role="dialog" tabIndex={-1}>{body}</section>}
    </> : <section className="v2-run-network-authority">{body}</section>}
    {confirmOpen && <V2ConfirmDialog busy={mutation.isPending} confirmLabel={t("允许这些主机", "Allow these hosts")} danger
      description={t(`当前任务及其后续执行将允许直接读取 ${additions.length} 个新增公网 HTTPS 域名，其他网页读取仍按请求批准。`, `Allow this task and its subsequent runs to read ${additions.length} additional public HTTPS hosts directly. Other page reads still require approval.`)}
      onCancel={() => {
        setConfirmOpen(false);
        // Backdrop mousedown may move focus after the dialog cleanup runs.
        requestAnimationFrame(() => confirmTriggerRef.current?.focus());
      }} onConfirm={() => mutation.mutate({ mode: "rules", targets: canonicalizeExactNetworkTargets([...current, ...additions]) })}
      open returnFocusRef={confirmTriggerRef} title={t("追加网页访问范围？", "Expand web access scope?")} />}
  </div>;
}
