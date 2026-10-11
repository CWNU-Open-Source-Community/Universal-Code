import { useEffect, useMemo, useRef, useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Check, ChevronDown, Globe2, LoaderCircle, ShieldCheck } from "lucide-react";
import type { APIClient } from "../../api/client";
import type { ProviderSearchReadinessView, RunDetailView } from "../../api/types";
import { v2QueryKeys } from "../query-keys";
import { browserCDPQueryKey } from "./browser-cdp-control";
import { V2ConfirmDialog } from "./dialog";
import { V2SearchDiagnosticsControl } from "./search-diagnostics-control";
import {
  canonicalizeExactNetworkTargets,
  exactNetworkTargetLooksValid,
  parseExactNetworkTargets,
} from "./network-scope-control";

function canExpand(status: RunDetailView["run"]["status"] | undefined): boolean {
  return status === "created" || status === "paused";
}

function readinessLabel(value: ProviderSearchReadinessView | undefined): string {
  if (!value) return "检查搜索…";
  if (value.state === "ready") return "搜索配置就绪";
  if (value.state === "network_disabled") return "搜索未联网";
  if (value.state === "missing_allowlist") return "搜索缺目标";
  if (value.state === "provider_unqualified") return "搜索待验证";
  return "搜索不可用";
}

function readinessDetail(value: ProviderSearchReadinessView | undefined): string {
  if (!value) return "正在核对当前任务的模型连接与网页访问范围。";
  switch (value.reason) {
  case "run_network_disabled":
    return "当前任务没有为网页搜索后端开放出站；供应商原生搜索与直接 URL 抓取分别授权。";
  case "search_endpoint_not_allowlisted":
    return value.required_target
      ? `搜索后端需要明确允许 ${value.required_target}；未列出的主机仍会被拒绝。`
      : "当前白名单没有覆盖搜索后端；请追加后端要求的精确 HTTPS 主机。";
  case "provider_native_qualification_required":
    return "供应商声明支持原生搜索，当前配置还需通过一次实际搜索验证。";
  case "provider_native_qualification_failed":
    switch (value.detail_code) {
    case "transport_unavailable":
      return "供应商原生搜索在连接或超时边界内没有完成；普通对话仍可继续，可重试搜索或配置后备搜索源。";
    case "tool_unsupported":
      return "当前供应商端点不支持已知的 Responses 原生搜索工具类型；普通模型对话仍可继续。";
    case "provider_rejected":
      return "供应商拒绝了原生搜索请求；请检查模型、额度与供应商搜索策略。";
    case "response_invalid":
      return "供应商返回的原生搜索结果不符合可验证协议；该能力已停止发布。";
    default:
      return "原生搜索的有界验证未通过；普通模型对话仍可与搜索能力分开使用。";
    }
  case "no_active_run":
    return "当前尚无可配置搜索权限的执行。发送下一条消息后，可为新执行确认搜索权限。";
  case "model_provider_unavailable":
    return "当前执行使用的模型连接不可用，请到模型设置验证连接。";
  case "provider_search_policy_disabled":
    return "当前供应商明确关闭了搜索策略。";
  case "search_backend_not_configured":
    return "当前选择的搜索后端尚未配置；选择 SearXNG 时需要提供外部搜索服务地址。";
  case "provider_search_configuration_invalid":
    return "当前供应商的搜索声明与传输配置不一致。";
  default:
    return value.search_policy === "provider_native"
      ? `${value.provider || "当前供应商"} 的托管搜索配置与网络授权已就绪，可发起搜索。搜索使用供应商接口；直接读取网页按下方访问范围审批。`
      : `${value.search_policy === "web" ? "普通网页搜索（DuckDuckGo）" : value.search_policy === "searxng" ? "外部搜索服务（SearXNG）" : "网页搜索后端"}的配置与网络授权已就绪；实际搜索结果以本次工具返回为准。`;
  }
}

function remediationLabel(value: ProviderSearchReadinessView | undefined): string {
  switch (value?.remediation) {
  case "enable_network_allowlist": return "追加搜索后端主机后即可启用";
  case "add_required_target": return "把所需主机加入当前执行的允许范围";
  case "qualify_provider_search": return "发起一次搜索，验证供应商搜索能力";
  case "submit_to_create_successor": return "发送下一条消息后配置新执行";
  case "configure_search_provider": return "到模型设置检查供应商与搜索后端";
  case "enable_provider_search": return "到模型设置启用搜索策略";
  case "repair_provider_configuration": return "到模型设置核对连接与搜索能力";
  default: return "配置与网络授权允许发起搜索";
  }
}

export function V2RunNetworkAuthorityControl({ client, threadID = "", runID,
  variant = "settings", onOpenModelSettings, onConfirmationOpenChange }: {
  client: APIClient;
  threadID?: string;
  runID: string;
  variant?: "menu" | "settings";
  onOpenModelSettings?: () => void;
  // Keep this instance's host open while its confirmation is portalled to body.
  onConfirmationOpenChange?: (open: boolean) => void;
}) {
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
  const current = networkScope?.network_mode === "allowlist" ? networkScope.allowed_targets ?? [] : [];
  // Historical broad scopes are read as stored facts. An approval preference,
  // including activated Full, does not itself grant this fetch scope.
  const publicHTTPS = current.includes("public_https");
  const rawRequested = useMemo(() => parseExactNetworkTargets(draft), [draft]);
  const invalid = rawRequested.filter((target) => !exactNetworkTargetLooksValid(target));
  const requested = useMemo(() => invalid.length === 0
    ? canonicalizeExactNetworkTargets(rawRequested) : [], [invalid.length, rawRequested]);
  const additions = requested.filter((target) => !current.includes(target));
  const mutable = canExpand(query.data?.run.status);
  const mutation = useMutation({
    mutationFn: () => client.expandRunNetworkAuthority(runID, {
      version: "run_network_authority_control.v1",
      expected_mode_revision: query.data?.mode.revision ?? 0,
      add_allowed_targets: additions,
      reason: "v2 operator-confirmed exact HTTPS targets",
    }, `v2-run-network-authority-${globalThis.crypto.randomUUID()}`),
    onSuccess: (result) => {
      queryClient.setQueryData<RunDetailView>(browserCDPQueryKey(runID), (value) =>
        value ? { ...value, mode: result.mode } : value);
      if (threadID) {
        void queryClient.invalidateQueries({ queryKey: v2QueryKeys.searchReadiness(threadID) });
      }
      setDraft("");
      setConfirmOpen(false);
    },
  });
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
  const status = !runID ? "先打开一个任务。"
    : query.isLoading ? "正在读取当前执行的网页访问范围…"
      : query.isError ? "无法读取网页访问范围。"
        : publicHTTPS
          ? "当前执行保存的网络范围允许匿名访问公网 HTTPS；私网、loopback、元数据地址、DNS 重绑定和非 HTTPS 请求仍会被拒绝。"
        : mutable ? "填写要访问的公网 HTTPS 主机，核对后追加到当前允许范围。"
          : "请在执行开始前，或暂停执行并等待资源释放后追加范围。已结束的执行可返回对话发送新消息。";
  const readiness = readinessQuery.data;
  const canPrefillRequiredTarget = Boolean(readiness?.required_target && mutable &&
    !current.includes(readiness.required_target));

  const body = <>
    <header><span><Globe2 aria-hidden="true" size={18} /></span><div>
      <strong>直接 URL 抓取</strong><p>{status}</p></div>
      <span className="v2-network-count">{query.isError ? "范围未知" : query.isPending ? "读取中"
        : publicHTTPS ? "公网 HTTPS" : current.length === 0 ? "未预先授权主机" : `${current.length} 个主机`}</span>
    </header>
    {threadID && <div className={`v2-search-readiness state-${readiness?.state ?? "loading"}`}
      role="status"><span><strong>{readiness?.search_policy === "provider_native" ? "供应商搜索" : "网页搜索"} · {readinessQuery.isError
        ? "无法检查" : readinessLabel(readiness)}</strong>
        <small>{readinessQuery.isError
          ? "搜索配置状态暂时无法读取。可先核对下方已保存的网页访问范围。"
          : readinessDetail(readiness)}</small></span>
      {!readinessQuery.isError && <em>{remediationLabel(readiness)}</em>}</div>}
    {readiness && <V2SearchDiagnosticsControl client={client} readiness={readiness}
      onOpenModelSettings={onOpenModelSettings} />}
    {query.isSuccess && <p className="v2-network-note">下方为当前执行已允许直接读取的网页范围。范围外的读取需单独审批，供应商搜索使用自己的连接设置。</p>}
    {!publicHTTPS && current.length > 0 && <div aria-label="当前允许的 HTTPS 主机" className="v2-network-targets">
      {current.map((target) => <code key={target}>{target}</code>)}
    </div>}
    {!publicHTTPS && canPrefillRequiredTarget && <button className="v2-network-prefill" onClick={() =>
      setDraft(readiness?.required_target ?? "")} type="button">
      使用所需主机：<code>{readiness?.required_target}</code>
    </button>}
    {!publicHTTPS && <div className="v2-network-expander">
      <label><span>追加 HTTPS 主机</span><textarea aria-label="追加允许的 HTTPS 主机"
        disabled={!runID || query.isLoading || query.isError || !mutable || mutation.isPending}
        onChange={(event) => setDraft(event.target.value)}
        placeholder={"search.example.org\ndocs.example.com"} rows={3} spellCheck={false}
        value={draft} /></label>
      <button disabled={!runID || !mutable || additions.length === 0 || invalid.length > 0 ||
        mutation.isPending || !client.hasControl} onClick={begin} ref={confirmTriggerRef} type="button">
        {mutation.isPending ? <LoaderCircle aria-hidden="true" className="spin" size={14} />
          : <Check aria-hidden="true" size={14} />}审核并追加
      </button>
    </div>}
    {!publicHTTPS && invalid.length > 0 && <p className="v2-network-error" role="alert">
      只接受无路径、查询或通配符的公网 HTTPS 主机：{invalid.join("、")}
    </p>}
    {!publicHTTPS && requested.length > 0 && additions.length === 0 && invalid.length === 0 &&
      <p className="v2-network-note"><ShieldCheck aria-hidden="true" size={14} />这些主机已在当前范围内。</p>}
    {!publicHTTPS && mutation.isError && <p className="v2-network-error" role="alert">
      {mutation.error instanceof Error ? mutation.error.message : "网页访问范围更新失败"}
    </p>}
  </>;

  return <div className={`v2-run-network-control is-${variant}`} ref={shellRef}>
    {variant === "menu" ? <>
      <button aria-expanded={open} aria-haspopup="dialog" aria-label="网页访问状态"
        className="v2-composer-chip" disabled={!runID} onClick={() => {
          if (!confirmOpen) setOpen((value) => !value);
        }}
        ref={menuTriggerRef} type="button">
        {publicHTTPS || current.length > 0 ? <Globe2 aria-hidden="true" size={14} />
          : <ShieldCheck aria-hidden="true" size={14} />}
        {readinessQuery.isError ? query.isError ? "范围未知" : publicHTTPS ? "公网 HTTPS"
          : current.length === 0 ? "URL 待授权" : `网页访问 · ${current.length}`
          : readinessLabel(readiness)}
        <ChevronDown aria-hidden="true" size={13} />
      </button>
      {open && <section aria-label="当前执行网页访问"
        className="v2-run-network-popover v2-run-network-authority"
        role="dialog">{body}</section>}
    </> : <section className="v2-run-network-authority">{body}</section>}
    {confirmOpen && <V2ConfirmDialog busy={mutation.isPending} confirmLabel="允许这些主机" danger
      description={`当前任务及其后续执行将能访问 ${additions.length} 个新增公网 HTTPS 主机。后端仍会拒绝私网、元数据地址、DNS 重绑定、未授权重定向和非 HTTPS 请求；网页内容始终作为不可信证据。`}
      onCancel={() => {
        setConfirmOpen(false);
        // Backdrop mousedown may move focus after the dialog cleanup runs.
        requestAnimationFrame(() => confirmTriggerRef.current?.focus());
      }} onConfirm={() => mutation.mutate()}
      open returnFocusRef={confirmTriggerRef} title="追加网页访问范围？" />}
  </div>;
}
