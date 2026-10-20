import { useId, useRef } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { APIRequestError, type APIClient } from "../../api/client";
import { commandServicePolicyVersion, type CommandServicePolicy, type ThreadCommandServicePolicyView } from "../../api/command-service-policy";
import type { ThreadApplicationServicesView, ThreadApplicationServiceView } from "../../api/types";
import { applicationServicesQueryKey } from "../application-preview-request";

import { useLocale, type LocaleText } from "../../lib/locale";

export const commandServicePolicyQueryKey = (threadID: string) =>
  ["v2", "thread", threadID, "command-service-policy"] as const;

const activeStates = new Set(["prepared", "running", "stopping"]);
const policyCopy = (t: LocaleText) => ({
  auto: t("Agent 只为开发或预览服务启用并行编辑；其他命令保持独占。", "The agent enables concurrent editing only for development or preview services. Other commands remain exclusive."),
  exclusive: t("阻止新的共享服务，并停止此任务已有共享服务。", "Block new shared services and stop existing shared services for this task."),
});

function serviceStateCopy(service: ThreadApplicationServiceView, t: LocaleText): string {
  if (!activeStates.has(service.state) && service.tree_reaped !== true) return t("已退出，清理待确认", "Exited; cleanup unconfirmed");
  if (service.state === "stopping") return t("正在停止", "Stopping");
  if (!service.can_stop) return t("当前状态待确认", "Current status unconfirmed");
  return service.state === "prepared" ? t("等待启动", "Waiting to start") : t("运行中", "Running");
}

export function V2CommandServiceSettings(props: { client: APIClient; threadID: string }) {
  return <CommandServiceSettings key={props.threadID} {...props} />;
}

function CommandServiceSettings({ client, threadID }: { client: APIClient; threadID: string }) {
  const { t } = useLocale();
  const queries = useQueryClient();
  const detailID = useId();
  const changing = useRef(false);
  const stopping = useRef(false);
  const canReadPolicy = typeof client.getThreadCommandServicePolicy === "function";
  const canReadServices = typeof client.listThreadApplicationServices === "function";
  const policyKey = commandServicePolicyQueryKey(threadID);
  const servicesKey = applicationServicesQueryKey(threadID);
  const policy = useQuery({ queryKey: policyKey,
    queryFn: ({ signal }) => client.getThreadCommandServicePolicy(threadID, signal),
    enabled: Boolean(threadID) && canReadPolicy, retry: false, refetchOnMount: "always", refetchInterval: 5000 });
  const services = useQuery({ queryKey: servicesKey,
    queryFn: ({ signal }) => client.listThreadApplicationServices(threadID, signal),
    enabled: Boolean(threadID) && canReadServices, retry: false, refetchOnMount: "always", refetchInterval: 1500 });
  const current = !policy.isError && policy.data?.thread_id === threadID ? policy.data : undefined;
  const records = services.data?.thread_id === threadID ? services.data.services : [];
  const shared = records.filter((service) => service.workspace_mode === "shared_service" &&
    (activeStates.has(service.state) || service.tree_reaped !== true));
  const readable = canReadServices && !services.isError && services.data?.thread_id === threadID;
  const runningCount = shared.filter((service) => service.state === "running" && service.can_stop).length;
  const awaitingCleanup = shared.some((service) => service.state === "stopping" || !activeStates.has(service.state));
  const unknown = shared.some((service) => service.state === "running" && !service.can_stop);
  const status = !canReadServices || services.isError ? t("无法读取", "Unavailable")
    : !readable ? t("正在读取…", "Loading…")
      : unknown ? t("无法读取", "Unavailable")
        : awaitingCleanup ? t("正在停止", "Stopping")
          : runningCount > 0 ? t(`${services.data?.has_more ? "至少 " : ""}${runningCount} 个服务运行中`, `${services.data?.has_more ? "At least " : ""}${runningCount} services running`)
            : shared.some((service) => service.state === "prepared") ? t("正在启动", "Starting")
              : services.data?.has_more ? t("无法读取完整状态", "Full status unavailable") : t("未启用", "Not enabled");

  const save = useMutation({ retry: false,
    mutationFn: ({ selected, revision }: { selected: CommandServicePolicy; revision: number }) =>
      client.setThreadCommandServicePolicy(threadID, {
        version: commandServicePolicyVersion, policy: selected, expected_revision: revision,
      }),
    onMutate: () => queries.cancelQueries({ queryKey: policyKey }),
    onSuccess: async (result) => {
      await queries.cancelQueries({ queryKey: policyKey });
      queries.setQueryData<ThreadCommandServicePolicyView>(policyKey, result);
    },
    onSettled: async () => {
      await Promise.all([
        queries.invalidateQueries({ queryKey: policyKey }),
        queries.invalidateQueries({ queryKey: servicesKey }),
      ]);
      changing.current = false;
    },
  });
  const stop = useMutation({ retry: false,
    mutationFn: (service: ThreadApplicationServiceView) =>
      client.stopThreadApplicationService(threadID, service.run_id, service.job_id),
    onSuccess: async (result) => {
      await queries.cancelQueries({ queryKey: servicesKey });
      queries.setQueryData<ThreadApplicationServicesView>(servicesKey, (previous) => previous ? {
        ...previous, services: previous.services.map((service) => service.job_id === result.service.job_id &&
          service.run_id === result.service.run_id ? result.service : service),
      } : previous);
    },
    onSettled: async () => {
      await queries.invalidateQueries({ queryKey: servicesKey });
      stopping.current = false;
    },
  });
  const select = (selected: CommandServicePolicy) => {
    if (!current || !client.hasExecutionPermissionControl || changing.current || selected === current.policy) return;
    changing.current = true;
    save.mutate({ selected, revision: current.revision });
  };
  const refresh = () => {
    if (canReadPolicy) void policy.refetch();
    if (canReadServices) void services.refetch();
  };

  return <section className="v2-settings-section v2-command-service-settings" aria-label={t("开发服务", "Development services")}>
    <h2>{t("开发服务", "Development services")}</h2>
    {!threadID ? <p className="v2-settings-empty">{t("先打开一个任务，再设置开发服务策略。", "Open a task to configure development service policy.")}</p> : <div className="v2-settings-card">
      <div className="v2-setting-row">
        <div><strong>{t("使用策略", "Usage policy")}</strong><span id={detailID}>{current ? policyCopy(t)[current.policy]
          : policy.isPending && canReadPolicy ? t("正在读取此任务的使用策略…", "Loading this task's usage policy…") : t("无法读取此任务的使用策略。", "Could not read this task's usage policy.")}</span></div>
        <div className="v2-setting-value"><div className="v2-setting-segmented" role="group" aria-label={t("开发服务使用策略", "Development service usage policy")} aria-describedby={detailID}>
          <button aria-pressed={current?.policy === "auto"} disabled={!current || !client.hasExecutionPermissionControl || save.isPending}
            onClick={() => select("auto")} type="button">{t("自动选择", "Automatic")}</button>
          <button aria-pressed={current?.policy === "exclusive"} disabled={!current || !client.hasExecutionPermissionControl || save.isPending}
            onClick={() => select("exclusive")} type="button">{t("始终独占", "Always exclusive")}</button>
        </div></div>
      </div>
      <div className="v2-setting-row">
        <div><strong>{t("运行状态", "Run status")}</strong><span>{t("仅显示允许并行编辑的开发服务；关闭设置页后仍会运行。", "Only development services allowing concurrent editing are shown. They keep running after settings close.")}</span></div>
        <div className="v2-setting-value"><span className="v2-command-service-status" role="status">{status}</span>
          <button type="button" aria-label={t("刷新开发服务状态", "Refresh development service status")} disabled={(!canReadPolicy && !canReadServices) || policy.isFetching || services.isFetching}
            onClick={refresh}>{t("刷新", "Refresh")}</button></div>
      </div>
      {shared.length > 0 && <ul className="v2-command-service-list" aria-label={t("并行开发服务", "Concurrent development services")}>
        {shared.map((service) => <li className="v2-setting-row" key={service.job_id}>
          <div><strong title={service.job_id}>{t("开发服务 · ", "Development service · ")}{service.job_id.slice(-8)}</strong>
            <span>{!readable ? t("当前状态待确认", "Current status unconfirmed") : serviceStateCopy(service, t)}{service.source_turn ? t(` · 第 ${service.source_turn} 轮`, ` · Turn ${service.source_turn}`) : ""}</span></div>
          <div className="v2-setting-value"><button aria-label={t(`停止服务 ${service.job_id}`, `Stop service ${service.job_id}`)} type="button"
            disabled={!readable || !service.can_stop || !client.hasRunExecution || stop.isPending || service.state === "stopping"}
            onClick={() => {
              if (stopping.current || !readable || !service.can_stop || !client.hasRunExecution) return;
              stopping.current = true;
              stop.mutate(service);
            }}>{stop.isPending && stop.variables?.job_id === service.job_id ? t("请求中…", "Requesting…") : t("停止", "Stop")}</button></div>
        </li>)}
      </ul>}
      <div className="v2-command-service-note">
        <p>{t("仅作用于当前任务及其后续执行。命令继续遵循当前任务权限与沙箱设置。", "Applies to this task and subsequent runs. Commands continue to follow this task's permissions and sandbox settings.")}</p>
        {current?.policy !== "exclusive" && <p>{t("选择“始终独占”会停止此任务已有共享服务。", "Selecting Always exclusive stops existing shared services for this task.")}</p>}
        {services.data?.has_more && <p>{t("当前列表未完整，运行数量仅统计已显示的服务。", "This list is incomplete. The running count includes only the services shown.")}</p>}
        {!client.hasExecutionPermissionControl && !client.hasRunExecution && <p>{t("当前连接只可查看设置与服务状态。", "This connection can only view settings and service status.")}</p>}
        {!client.hasExecutionPermissionControl && client.hasRunExecution && <p>{t("当前连接无法更改开发服务策略。", "This connection cannot change development service policy.")}</p>}
        {client.hasExecutionPermissionControl && !client.hasRunExecution && <p>{t("当前连接无法直接停止服务。", "This connection cannot stop services directly.")}</p>}
        {save.isPending && <p role="status">{t("正在保存使用策略…", "Saving usage policy…")}</p>}
        {save.isError && <p role="alert">{save.error instanceof APIRequestError && save.error.status === 409
          ? t("设置已被更新，请核对当前选择后重试。", "Settings have changed. Review the current selection and try again.") : t("保存结果未确认，请刷新设置后核对。", "Save result unconfirmed. Refresh settings to check.")}</p>}
        {stop.isError && <p role="alert">{t("停止结果尚未确认，请刷新状态后重试。", "Stop result unconfirmed. Refresh status before retrying.")}</p>}
      </div>
    </div>}
  </section>;
}
