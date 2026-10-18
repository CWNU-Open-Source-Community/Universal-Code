import { useId, useRef } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { APIRequestError, type APIClient } from "../../api/client";
import { commandServicePolicyVersion, type CommandServicePolicy, type ThreadCommandServicePolicyView } from "../../api/command-service-policy";
import type { ThreadApplicationServicesView, ThreadApplicationServiceView } from "../../api/types";
import { applicationServicesQueryKey } from "../application-preview-request";

export const commandServicePolicyQueryKey = (threadID: string) =>
  ["v2", "thread", threadID, "command-service-policy"] as const;

const activeStates = new Set(["prepared", "running", "stopping"]);
const policyCopy = {
  auto: "Agent 只为开发或预览服务启用并行编辑；其他命令保持独占。",
  exclusive: "阻止新的共享服务，并停止此任务已有共享服务。",
};

function serviceStateCopy(service: ThreadApplicationServiceView): string {
  if (!activeStates.has(service.state) && service.tree_reaped !== true) return "已退出，清理待确认";
  if (service.state === "stopping") return "正在停止";
  if (!service.can_stop) return "当前状态待确认";
  return service.state === "prepared" ? "等待启动" : "运行中";
}

export function V2CommandServiceSettings(props: { client: APIClient; threadID: string }) {
  return <CommandServiceSettings key={props.threadID} {...props} />;
}

function CommandServiceSettings({ client, threadID }: { client: APIClient; threadID: string }) {
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
  const status = !canReadServices || services.isError ? "无法读取"
    : !readable ? "正在读取…"
      : unknown ? "无法读取"
        : awaitingCleanup ? "正在停止"
          : runningCount > 0 ? `${services.data?.has_more ? "至少 " : ""}${runningCount} 个服务运行中`
            : shared.some((service) => service.state === "prepared") ? "正在启动"
              : services.data?.has_more ? "无法读取完整状态" : "未启用";

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

  return <section className="v2-settings-section v2-command-service-settings" aria-label="开发服务">
    <h2>开发服务</h2>
    {!threadID ? <p className="v2-settings-empty">先打开一个任务，再设置开发服务策略。</p> : <div className="v2-settings-card">
      <div className="v2-setting-row">
        <div><strong>使用策略</strong><span id={detailID}>{current ? policyCopy[current.policy]
          : policy.isPending && canReadPolicy ? "正在读取此任务的使用策略…" : "无法读取此任务的使用策略。"}</span></div>
        <div className="v2-setting-value"><div className="v2-setting-segmented" role="group" aria-label="开发服务使用策略" aria-describedby={detailID}>
          <button aria-pressed={current?.policy === "auto"} disabled={!current || !client.hasExecutionPermissionControl || save.isPending}
            onClick={() => select("auto")} type="button">自动选择</button>
          <button aria-pressed={current?.policy === "exclusive"} disabled={!current || !client.hasExecutionPermissionControl || save.isPending}
            onClick={() => select("exclusive")} type="button">始终独占</button>
        </div></div>
      </div>
      <div className="v2-setting-row">
        <div><strong>运行状态</strong><span>仅显示允许并行编辑的开发服务；关闭设置页后仍会运行。</span></div>
        <div className="v2-setting-value"><span className="v2-command-service-status" role="status">{status}</span>
          <button type="button" aria-label="刷新开发服务状态" disabled={(!canReadPolicy && !canReadServices) || policy.isFetching || services.isFetching}
            onClick={refresh}>刷新</button></div>
      </div>
      {shared.length > 0 && <ul className="v2-command-service-list" aria-label="并行开发服务">
        {shared.map((service) => <li className="v2-setting-row" key={service.job_id}>
          <div><strong title={service.job_id}>开发服务 · {service.job_id.slice(-8)}</strong>
            <span>{!readable ? "当前状态待确认" : serviceStateCopy(service)}{service.source_turn ? ` · 第 ${service.source_turn} 轮` : ""}</span></div>
          <div className="v2-setting-value"><button aria-label={`停止服务 ${service.job_id}`} type="button"
            disabled={!readable || !service.can_stop || !client.hasRunExecution || stop.isPending || service.state === "stopping"}
            onClick={() => {
              if (stopping.current || !readable || !service.can_stop || !client.hasRunExecution) return;
              stopping.current = true;
              stop.mutate(service);
            }}>{stop.isPending && stop.variables?.job_id === service.job_id ? "请求中…" : "停止"}</button></div>
        </li>)}
      </ul>}
      <div className="v2-command-service-note">
        <p>仅作用于当前任务及其后续执行。命令继续遵循当前任务权限与沙箱设置。</p>
        {current?.policy !== "exclusive" && <p>选择“始终独占”会停止此任务已有共享服务。</p>}
        {services.data?.has_more && <p>当前列表未完整，运行数量仅统计已显示的服务。</p>}
        {!client.hasExecutionPermissionControl && !client.hasRunExecution && <p>当前连接只可查看设置与服务状态。</p>}
        {!client.hasExecutionPermissionControl && client.hasRunExecution && <p>当前连接无法更改开发服务策略。</p>}
        {client.hasExecutionPermissionControl && !client.hasRunExecution && <p>当前连接无法直接停止服务。</p>}
        {save.isPending && <p role="status">正在保存使用策略…</p>}
        {save.isError && <p role="alert">{save.error instanceof APIRequestError && save.error.status === 409
          ? "设置已被更新，请核对当前选择后重试。" : "保存结果未确认，请刷新设置后核对。"}</p>}
        {stop.isError && <p role="alert">停止结果尚未确认，请刷新状态后重试。</p>}
      </div>
    </div>}
  </section>;
}
