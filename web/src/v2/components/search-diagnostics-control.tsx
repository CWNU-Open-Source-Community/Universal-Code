import { useEffect, useRef, useState } from "react";
import { CircleAlert, LoaderCircle, RefreshCw, Settings, Wifi } from "lucide-react";
import type { APIClient } from "../../api/client";
import { APIRequestError } from "../../api/client";
import type { ProviderSearchReadinessView, SearchDiagnosticCode,
  SearchDiagnosticsView } from "../../api/types";

function configuredBackend(readiness: ProviderSearchReadinessView): string {
  switch (readiness.search_policy) {
  case "provider_native": return `${readiness.provider || "当前供应商"} Responses 原生搜索`;
  case "web": return "DuckDuckGo 普通网页搜索";
  case "searxng": return "SearXNG";
  case "auto": return "自动选择（Responses 原生搜索优先）";
  default: return "未配置";
  }
}

function backendLabel(value: string): string {
  if (value === "duckduckgo") return "DuckDuckGo";
  if (value === "searxng") return "SearXNG";
  return value;
}

function resultMessage(result: SearchDiagnosticsView): string {
  const messages: Record<SearchDiagnosticCode, string> = {
    none: `搜索后端返回了 ${result.result_count} 条结果。`,
    network: "未能连接搜索后端；请检查网络、代理和服务状态后重新检查。",
    timeout: "搜索后端没有在限定时间内响应；稍后可重新检查。",
    rate_limited: "搜索后端正在限流；请按服务端提示的时间稍后再试。",
    access_challenge: "搜索后端返回了验证码或访问挑战，软件没有尝试绕过。请调整搜索配置。",
    authentication: "搜索后端拒绝了凭据；请检查模型供应商配置。",
    provider_rejected: "供应商拒绝了原生搜索请求；请检查模型、额度与搜索策略。",
    tool_unsupported: "当前模型接口不支持所配置的原生搜索工具。",
    no_usable_results: "搜索后端已响应，但没有返回可用的公开结果。",
    invalid_response: "搜索后端返回了无法验证的响应。",
    not_configured: "当前 Run 没有可用的搜索后端配置。",
    not_authorized: "当前 Run 的网页访问范围未授权所需搜索后端。",
    configuration_changed: "检查期间模型、后端或权限配置发生变化；请重新检查当前配置。",
  };
  return messages[result.code];
}

function needsModelSettings(code: SearchDiagnosticCode): boolean {
  return ["access_challenge", "authentication", "provider_rejected", "tool_unsupported",
    "invalid_response", "not_configured"].includes(code);
}

function formatTime(value: string): string {
  const parsed = new Date(value);
  return Number.isNaN(parsed.valueOf()) ? value : parsed.toLocaleString();
}

function readinessBinding(value: ProviderSearchReadinessView): string {
  return [value.thread_id, value.run_id ?? "", String(value.mode_revision ?? 0),
    value.model_route ?? "", value.provider ?? "", value.model ?? "", value.search_policy ?? ""].join("\u0000");
}

function matchesBinding(result: SearchDiagnosticsView,
  readiness: ProviderSearchReadinessView): boolean {
  return result.thread_id === readiness.thread_id && result.run_id === readiness.run_id &&
    result.mode_revision === readiness.mode_revision && result.model_route === readiness.model_route &&
    result.provider === readiness.provider && result.model === readiness.model &&
    result.search_policy === (readiness.search_policy ?? "");
}

export function V2SearchDiagnosticsControl({ client, readiness, onOpenModelSettings }: {
  client: APIClient;
  readiness: ProviderSearchReadinessView;
  onOpenModelSettings?: () => void;
}) {
  const [busy, setBusy] = useState(false);
  const [result, setResult] = useState<SearchDiagnosticsView | null>(null);
  const [error, setError] = useState("");
  const controllerRef = useRef<AbortController | null>(null);
  const generationRef = useRef(0);
  const mountedRef = useRef(true);
  const binding = readinessBinding(readiness);
  const threadID = readiness.thread_id ?? "";

  useEffect(() => {
    mountedRef.current = true;
    return () => {
      mountedRef.current = false;
      generationRef.current += 1;
      controllerRef.current?.abort();
    };
  }, []);

  useEffect(() => {
    generationRef.current += 1;
    controllerRef.current?.abort();
    controllerRef.current = null;
    setBusy(false);
    setResult(null);
    setError("");
  }, [binding]);

  const check = async () => {
    if (busy || !threadID || !readiness.run_id || !client.hasControl) return;
    const generation = ++generationRef.current;
    const controller = new AbortController();
    controllerRef.current?.abort();
    controllerRef.current = controller;
    setBusy(true);
    setResult(null);
    setError("");
    try {
      const next = await client.diagnoseThreadSearch(threadID, {
        version: "search_diagnostics.v1", confirm: true,
      }, controller.signal);
      if (!mountedRef.current || generation !== generationRef.current || controller.signal.aborted) return;
      if (!matchesBinding(next, readiness)) {
        setError("检查结果不再匹配当前 Run、模型或权限版本，请重新检查。");
        return;
      }
      setResult(next);
    } catch (reason) {
      if (!mountedRef.current || generation !== generationRef.current || controller.signal.aborted) return;
      setError(reason instanceof APIRequestError && reason.code === "CONFLICT"
        ? "当前任务或连接检查正在进行。暂停任务或等待完成后，再检查搜索连接。"
        : reason instanceof Error ? reason.message : "搜索连接检查失败");
    } finally {
      if (mountedRef.current && generation === generationRef.current) {
        controllerRef.current = null;
        setBusy(false);
      }
    }
  };

  const canCheck = Boolean(threadID && readiness.run_id && client.hasControl);
  return <section aria-label="搜索连接" className={`v2-search-readiness state-${
    result?.state === "succeeded" ? "ready" : result ? "provider_unavailable" : readiness.state}`}>
    <span><strong><Wifi aria-hidden="true" size={13} />搜索连接</strong>
      <small>当前 Run 模型：{readiness.provider && readiness.model
        ? `${readiness.provider} / ${readiness.model}` : readiness.model_route || "未解析"}</small>
      <small>搜索配置：{configuredBackend(readiness)}</small>
      <small>检查会使用当前模型对应的通用搜索后端执行一次有界搜索，可能产生供应商搜索费用；不检测专业来源连接器。</small>
    </span>
    <button className="compact-command" disabled={!canCheck || busy} onClick={() => void check()} type="button">
      {busy ? <LoaderCircle aria-hidden="true" className="spin" size={13} />
        : <RefreshCw aria-hidden="true" size={13} />}
      {busy ? "正在检查…" : result || error ? "重新检查搜索连接" : "检查搜索连接"}
    </button>
    {!canCheck && <em>{readiness.run_id
      ? "当前连接没有执行搜索诊断的控制权限。" : "当前没有可检查的活动 Run。"}</em>}
    {result && <article aria-label="搜索连接检查结果" role="status">
      <strong>{result.state === "succeeded" ? "真实探针通过" : "真实探针未通过"}</strong>
      <p>{resultMessage(result)}</p>
      <dl>
        <div><dt>实际模型</dt><dd>{result.provider} / {result.model}</dd></div>
        <div><dt>实际后端</dt><dd>{backendLabel(result.backend) || "未选择"}</dd></div>
        <div><dt>检查时间</dt><dd>{formatTime(result.checked_at)}</dd></div>
        <div><dt>结果数量</dt><dd>{result.result_count}</dd></div>
        {result.http_status !== undefined && <div><dt>HTTP</dt><dd>{result.http_status}</dd></div>}
        {result.retry_after && <div><dt>Retry-After</dt><dd>{result.retry_after}</dd></div>}
        {result.rate_limit_reset && <div><dt>限流重置</dt><dd>{result.rate_limit_reset}</dd></div>}
      </dl>
      {result.required_target && <p>需要允许的主机：<code>{result.required_target}</code></p>}
      {needsModelSettings(result.code) && onOpenModelSettings && <button className="compact-command" onClick={onOpenModelSettings}
        type="button"><Settings aria-hidden="true" size={13} />打开模型设置</button>}
    </article>}
    {error && <em role="alert"><CircleAlert aria-hidden="true" size={13} />{error}</em>}
  </section>;
}
