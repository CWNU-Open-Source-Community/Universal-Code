import { useEffect, useRef, useState } from "react";
import { CircleAlert, LoaderCircle, RefreshCw, Settings, Wifi } from "lucide-react";
import type { APIClient } from "../../api/client";
import { APIRequestError } from "../../api/client";
import { useLocale, type LocaleText, type PrayuLocale } from "../../lib/locale";
import type { ProviderSearchReadinessView, SearchDiagnosticCode,
  SearchDiagnosticsView } from "../../api/types";

function configuredBackend(readiness: ProviderSearchReadinessView, t: LocaleText): string {
  switch (readiness.search_policy) {
  case "provider_native": return t(`${readiness.provider || "当前供应商"} Responses 原生搜索`, `${readiness.provider || "Current provider"} native Responses search`);
  case "web": return t("DuckDuckGo 普通网页搜索", "DuckDuckGo web search");
  case "searxng": return "SearXNG";
  case "auto": return t("自动选择（Responses 原生搜索优先）", "Automatic (prefer native Responses search)");
  default: return t("未配置", "Not configured");
  }
}

function backendLabel(value: string): string {
  if (value === "duckduckgo") return "DuckDuckGo";
  if (value === "searxng") return "SearXNG";
  return value;
}

function resultMessage(result: SearchDiagnosticsView, t: LocaleText): string {
  const messages: Record<SearchDiagnosticCode, string> = {
    none: t(`搜索后端返回了 ${result.result_count} 条结果。`, `The search backend returned ${result.result_count} results.`),
    network: t("未能连接搜索后端；请检查网络、代理和服务状态后重新检查。", "Could not connect to the search backend. Check the network, proxy and service status before retrying."),
    timeout: t("搜索后端没有在限定时间内响应；稍后可重新检查。", "The search backend timed out. Try again later."),
    rate_limited: t("搜索后端正在限流；请按服务端提示的时间稍后再试。", "The search backend is rate-limiting requests. Retry after the time specified by the server."),
    access_challenge: t("搜索后端返回了验证码或访问挑战，软件没有尝试绕过。请调整搜索配置。", "The search backend returned a CAPTCHA or access challenge. No bypass was attempted. Adjust the search configuration."),
    authentication: t("搜索后端拒绝了凭据；请检查模型供应商配置。", "The search backend rejected the credentials. Check the model provider configuration."),
    provider_rejected: t("供应商拒绝了原生搜索请求；请检查模型、额度与搜索策略。", "The provider rejected native search. Check the model, quota and search policy."),
    tool_unsupported: t("当前模型接口不支持所配置的原生搜索工具。", "This model endpoint does not support the configured native search tool."),
    no_usable_results: t("搜索后端已响应，但没有返回可用的公开结果。", "The search backend responded without usable public results."),
    invalid_response: t("搜索后端返回了无法验证的响应。", "The search backend returned a response that could not be verified."),
    not_configured: t("当前 Run 没有可用的搜索后端配置。", "No search backend is configured for this run."),
    not_authorized: t("当前 Run 的网页访问范围未授权所需搜索后端。", "This run's web access does not authorize the required search backend."),
    configuration_changed: t("检查期间模型、后端或权限配置发生变化；请重新检查当前配置。", "The model, backend or permissions changed during the check. Check the current configuration again."),
  };
  return messages[result.code];
}

function needsModelSettings(code: SearchDiagnosticCode): boolean {
  return ["access_challenge", "authentication", "provider_rejected", "tool_unsupported",
    "invalid_response", "not_configured"].includes(code);
}

function formatTime(value: string, locale: PrayuLocale): string {
  const parsed = new Date(value);
  return Number.isNaN(parsed.valueOf()) ? value : parsed.toLocaleString(locale);
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
  const { t, locale } = useLocale();
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
        setError(t("检查结果不再匹配当前 Run、模型或权限版本，请重新检查。", "The result no longer matches the current run, model or permission revision. Check again."));
        return;
      }
      setResult(next);
    } catch (reason) {
      if (!mountedRef.current || generation !== generationRef.current || controller.signal.aborted) return;
      setError(reason instanceof APIRequestError && reason.code === "CONFLICT"
        ? t("当前任务或连接检查正在进行。暂停任务或等待完成后，再检查搜索连接。", "A task or connection check is running. Pause the task or wait for it to finish before checking search.")
        : reason instanceof Error ? reason.message : t("搜索连接检查失败", "Search connection check failed"));
    } finally {
      if (mountedRef.current && generation === generationRef.current) {
        controllerRef.current = null;
        setBusy(false);
      }
    }
  };

  const canCheck = Boolean(threadID && readiness.run_id && client.hasControl);
  return <section aria-label={t("搜索连接", "Search connection")} className={`v2-search-readiness state-${
    result?.state === "succeeded" ? "ready" : result ? "provider_unavailable" : readiness.state}`}>
    <span><strong><Wifi aria-hidden="true" size={13} />{t("搜索连接", "Search connection")}</strong>
      <small>{t("当前 Run 模型：", "Current run model: ")}{readiness.provider && readiness.model
        ? `${readiness.provider} / ${readiness.model}` : readiness.model_route || t("未解析", "Unresolved")}</small>
      <small>{t("搜索配置：", "Search configuration: ")}{configuredBackend(readiness, t)}</small>
      <small>{t("检查会使用当前模型对应的通用搜索后端执行一次有界搜索，可能产生供应商搜索费用；不检测专业来源连接器。", "This check runs a bounded search using the current model's general search backend. Provider search fees may apply. Specialized source connectors are not checked.")}</small>
    </span>
    <button className="compact-command" disabled={!canCheck || busy} onClick={() => void check()} type="button">
      {busy ? <LoaderCircle aria-hidden="true" className="spin" size={13} />
        : <RefreshCw aria-hidden="true" size={13} />}
      {busy ? t("正在检查…", "Checking…") : result || error ? t("重新检查搜索连接", "Recheck search connection") : t("检查搜索连接", "Check search connection")}
    </button>
    {!canCheck && <em>{readiness.run_id
      ? t("当前连接没有执行搜索诊断的控制权限。", "This connection cannot run search diagnostics.") : t("当前没有可检查的活动 Run。", "There is no active run to check.")}</em>}
    {result && <article aria-label={t("搜索连接检查结果", "Search connection results")} role="status">
      <strong>{result.state === "succeeded" ? t("真实探针通过", "Live check passed") : t("真实探针未通过", "Live check failed")}</strong>
      <p>{resultMessage(result, t)}</p>
      <dl>
        <div><dt>{t("实际模型", "Actual model")}</dt><dd>{result.provider} / {result.model}</dd></div>
        <div><dt>{t("实际后端", "Actual backend")}</dt><dd>{backendLabel(result.backend) || t("未选择", "Not selected")}</dd></div>
        <div><dt>{t("检查时间", "Checked at")}</dt><dd>{formatTime(result.checked_at, locale)}</dd></div>
        <div><dt>{t("结果数量", "Results")}</dt><dd>{result.result_count}</dd></div>
        {result.http_status !== undefined && <div><dt>HTTP</dt><dd>{result.http_status}</dd></div>}
        {result.retry_after && <div><dt>Retry-After</dt><dd>{result.retry_after}</dd></div>}
        {result.rate_limit_reset && <div><dt>{t("限流重置", "Rate limit reset")}</dt><dd>{result.rate_limit_reset}</dd></div>}
      </dl>
      {result.required_target && <p>{t("需要允许的主机：", "Host access required: ")}<code>{result.required_target}</code></p>}
      {needsModelSettings(result.code) && onOpenModelSettings && <button className="compact-command" onClick={onOpenModelSettings}
        type="button"><Settings aria-hidden="true" size={13} />{t("打开模型设置", "Open model settings")}</button>}
    </article>}
    {error && <em role="alert"><CircleAlert aria-hidden="true" size={13} />{error}</em>}
  </section>;
}
