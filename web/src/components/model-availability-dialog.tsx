import { useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Activity, Check, LoaderCircle, Route, ShieldCheck } from "lucide-react";
import type { APIClient } from "../api/client";
import type { ModelHarnessQualificationView, ProviderDiagnosticView } from "../api/types";
import { ErrorState, LoadingState, StatusBadge } from "./common";
import { useLocale } from "../lib/locale";
import { PriceSnapshotsSection } from "./price-snapshots-panel";

export function ModelAvailabilitySettings({ client }: { client: APIClient }) {
  const { t } = useLocale();
  const qualificationStatusLabel = (status: string) => {
    const labels: Record<string, [string, string]> = {
      not_configured: ["未配置", "not configured"],
      available: ["可用", "available"],
      protocol_mismatch: ["协议不兼容", "protocol mismatch"],
      auth_failed: ["身份验证失败", "authentication failed"],
      network_failed: ["网络不可达", "network unreachable"],
      rate_limit: ["达到速率限制", "rate limited"],
      capacity: ["容量不足", "capacity unavailable"],
      model_unsupported: ["模型不支持", "model unsupported"],
      response_incomplete: ["响应未完成", "response incomplete"],
    };
    const label = labels[status];
    return label ? t(label[0], label[1]) : status;
  };
  const failureReasonLabel = (reason: string) => {
    const labels: Record<string, [string, string]> = {
      not_configured: ["未配置", "not configured"],
      authentication: ["身份验证失败", "authentication failed"],
      network: ["网络不可达", "network unreachable"],
      rate_limit: ["达到速率限制", "rate limited"],
      capacity: ["Provider 容量不足", "Provider capacity unavailable"],
      model_not_found: ["模型不存在", "model not found"],
      protocol_incompatible: ["协议不兼容", "protocol incompatible"],
      context_limit: ["输入超出上下文窗口", "input exceeds context window"],
      output_limit: ["输出达到长度上限", "output limit reached"],
      paused: ["模型暂停", "model paused"],
      refusal: ["模型拒绝请求", "model refused the request"],
    };
    const label = labels[reason];
    return label ? t(label[0], label[1]) : reason;
  };
  const queryClient = useQueryClient();
  const [selections, setSelections] = useState<Record<string, string>>({});
  const [diagnostic, setDiagnostic] = useState<ProviderDiagnosticView | null>(null);
  const [qualification, setQualification] = useState<ModelHarnessQualificationView | null>(null);
  const query = useQuery({
    queryKey: ["models", "availability"],
    queryFn: ({ signal }) => client.modelAvailability(signal),
  });
  const refreshModelRoutes = async () => {
    await Promise.all([
      queryClient.invalidateQueries({ queryKey: ["models", "availability"] }),
      queryClient.invalidateQueries({ queryKey: ["v2", "models", "available-routes"] }),
      queryClient.invalidateQueries({ predicate: (candidate) =>
        candidate.queryKey[0] === "v2" && candidate.queryKey[1] === "thread" &&
        candidate.queryKey.at(-1) === "model-route" }),
    ]);
  };
  const routeMutation = useMutation({
    mutationFn: ({ route, reference }: { route: string; reference: string }) => {
      const slash = reference.indexOf("/");
      if (slash <= 0 || slash === reference.length - 1) {
        throw new Error(t("请选择可用的 Provider 模型", "Select an available Provider model"));
      }
      return client.selectModelRoute(route, {
        version: "model_route_control.v1",
        provider: reference.slice(0, slash), model: reference.slice(slash + 1),
      });
    },
    onSuccess: refreshModelRoutes,
  });
  const diagnosticMutation = useMutation({
    mutationFn: ({ provider, model }: { provider: string; model: string }) =>
      client.diagnoseProvider({ version: "provider_diagnostic.v1", provider, model,
        confirm_diagnostic: true }),
    onSuccess: async (result) => {
      setDiagnostic(result);
      await refreshModelRoutes();
    },
  });
  const qualificationMutation = useMutation({
    mutationFn: ({ provider, model }: { provider: string; model: string }) =>
      client.qualifyModelHarness({ version: "model_harness_qualification.v1",
        provider, model, confirm_qualification: true }),
    onSuccess: async (result) => {
      setQualification(result);
      await refreshModelRoutes();
    },
  });
  return (
      <section aria-label={t("高级模型设置", "Advanced model settings")}
        className="model-control-workspace" role="region">
        <div className="desktop-dialog-body model-availability-body">
          {query.isLoading && <LoadingState label={t("加载模型可用性", "Loading model availability")} />}
          {query.isError && <ErrorState error={query.error} />}
          {query.data && (
            <>
              <section className="model-availability-section">
                <h3><Route aria-hidden="true" size={14} />{t("新对话默认模型", "Default model for new conversations")}</h3>
                <div className="model-route-list">
                  {query.data.routes.filter((route) => route.name === "code").map((route) => (
                    <div className="model-route-row" key={route.name}>
                      <strong>{t("默认", "Default")}</strong>
                      {client.hasModelControl ? <select aria-label={t("新对话默认模型", "Default model for new conversations")}
                        onChange={(event) => setSelections((current) => ({ ...current,
                          [route.name]: event.target.value }))}
                        value={selections[route.name] ?? `${route.provider}/${route.model}`}>
                        {query.data.providers.filter((provider) => provider.status === "available")
                          .flatMap((provider) => provider.models.map((model) => (
                            <option key={`${provider.name}/${model}`} value={`${provider.name}/${model}`}>
                              {provider.name}/{model}
                            </option>
                          )))}
                      </select> : <span>{route.provider}/{route.model}</span>}
                      <StatusBadge status={!route.available ? "unavailable"
                        : route.harness_ready ? "harness ready" : "qualification required"} />
                      {client.hasModelControl && <button aria-label={t("保存新对话默认模型", "Save default model for new conversations")}
                        className="icon-button" disabled={routeMutation.isPending}
                        onClick={() => routeMutation.mutate({ route: route.name,
                          reference: selections[route.name] ?? `${route.provider}/${route.model}` })}
                        title={t("持久化路由选择", "Persist route selection")} type="button">
                        {routeMutation.isPending && routeMutation.variables?.route === route.name
                          ? <LoaderCircle aria-hidden="true" className="spin" size={15} />
                          : <Check aria-hidden="true" size={15} />}
                      </button>}
                    </div>
                  ))}
                </div>
                <p>{t("新对话和使用 code 路由的旧任务优先使用此模型。新建任务时，如模型不可用，将选择其他可用模型。", "New chats and existing tasks on the code route prefer this model. New tasks can select another available model if needed.")}</p>
              </section>
              {routeMutation.isError && <div className="inline-warning" role="alert">
                {routeMutation.error instanceof Error
                  ? routeMutation.error.message : t("模型路由选择失败", "Model route selection failed")}
              </div>}
              <details className="model-optional-prices">
                <summary>{t("逐模型诊断", "Per-model diagnostics")}</summary>
                <section className="model-availability-section">
                  <div className="model-provider-list">
                    {query.data.providers.flatMap((provider) =>
                      (provider.models.length > 0 ? provider.models : [""]).map((model) => {
                      const harness = provider.harnesses.find((candidate) => candidate.model === model);
                      const modelReference = `${provider.name}/${model}`;
                      return <div className="model-provider-row" key={modelReference}>
                        <div><strong>{provider.display_name}</strong><small>{provider.kind}</small></div>
                        <span>{model || t("未配置模型", "No configured model")}</span>
                        <span>{harness
                          ? `${harness.transport_protocol} · JSON ${harness.json_strategy}`
                          : provider.credential_source === "chatgpt_oauth" ? t("ChatGPT 账户", "ChatGPT account") : provider.credential_source}</span>
                        <StatusBadge status={provider.status} />
                        {harness && <StatusBadge status={harness.qualification_status} />}
                        {harness?.latest_qualification_status &&
                          <span title={t("最近一次诊断状态", "Latest diagnostic qualification")}>
                            {qualificationStatusLabel(harness.latest_qualification_status)}
                          </span>}
                        {client.hasModelControl &&
                          (provider.status === "available" ||
                            provider.status === "not_configured") &&
                          model && (
                            <>
                              <button aria-label={t(`诊断 ${modelReference}`, `Diagnose ${modelReference}`)} className="icon-button"
                                disabled={diagnosticMutation.isPending ||
                                  qualificationMutation.isPending}
                                onClick={() => diagnosticMutation.mutate({ provider: provider.name,
                                  model })}
                                title={t("运行单次连接诊断", "Run one-call connectivity diagnostic")} type="button">
                                {diagnosticMutation.isPending &&
                                  diagnosticMutation.variables?.provider === provider.name &&
                                  diagnosticMutation.variables.model === model
                                  ? <LoaderCircle aria-hidden="true" className="spin" size={15} />
                                  : <Activity aria-hidden="true" size={15} />}
                              </button>
                              <button aria-label={t(`检查 ${modelReference} 兼容性`, `Check ${modelReference} compatibility`)}
                                className="icon-button"
                                disabled={diagnosticMutation.isPending ||
                                  qualificationMutation.isPending || harness?.root_eligible === true}
                                onClick={() => qualificationMutation.mutate({
                                  provider: provider.name, model,
                                })}
                                title={t("兼容性检查发送两次模型调用，可能产生费用", "Runs two model calls. Charges may apply.")} type="button">
                                {qualificationMutation.isPending &&
                                  qualificationMutation.variables?.provider === provider.name &&
                                  qualificationMutation.variables.model === model
                                  ? <LoaderCircle aria-hidden="true" className="spin" size={15} />
                                  : <ShieldCheck aria-hidden="true" size={15} />}
                              </button>
                            </>
                          )}
                      </div>;
                    }))}
                  </div>
                  {diagnostic && <div className="model-diagnostic-result" role="status">
                    <span>{diagnostic.provider}/{diagnostic.model}</span>
                    <StatusBadge status={diagnostic.status} />
                    {diagnostic.qualification_status &&
                      <span title={t("端点资格状态", "Endpoint qualification status")}>
                        {qualificationStatusLabel(diagnostic.qualification_status)}
                      </span>}
                    <span>{diagnostic.failure_reason !== "none"
                      ? failureReasonLabel(diagnostic.failure_reason)
                      : diagnostic.outcome === "success"
                      ? t("成功", "success")
                      : diagnostic.outcome === "invalid_response"
                        ? t("响应格式不兼容", "invalid response")
                        : diagnostic.outcome}</span>
                    <span>{diagnostic.duration_ms} ms</span>
                  </div>}
                  {diagnosticMutation.isError && <div className="inline-warning" role="alert">
                    {diagnosticMutation.error instanceof Error
                      ? diagnosticMutation.error.message : t("Provider 诊断失败", "Provider diagnostic failed")}
                  </div>}
                  {qualification && <div className="model-diagnostic-result" role="status">
                    <span>{qualification.provider}/{qualification.model}</span>
                    <StatusBadge status={qualification.status} />
                    <span>{qualification.harness.transport_protocol}</span>
                    {qualification.qualification_status &&
                      <span title={t("端点资格状态", "Endpoint qualification status")}>
                        {qualificationStatusLabel(qualification.qualification_status)}
                      </span>}
                    {qualification.failure_reason !== "none" &&
                      <span>{failureReasonLabel(qualification.failure_reason)}</span>}
                    <span>{qualification.model_calls} {t("次模型调用", "model calls")}</span>
                  </div>}
                  {qualificationMutation.isError && <div className="inline-warning" role="alert">
                    {qualificationMutation.error instanceof Error
                      ? qualificationMutation.error.message : t("模型兼容性检查失败", "Model compatibility check failed")}
                  </div>}
                </section>
              </details>
              <details className="model-optional-prices">
                <summary>{t("费用上限所用价格（可选）", "Pricing for spending limits (optional)")}</summary>
                <PriceSnapshotsSection client={client} />
              </details>
            </>
          )}
        </div>
      </section>
  );
}
