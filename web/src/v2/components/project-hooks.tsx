import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import type { APIClient } from "../../api/client";
import { readProjectHooks, reviewProjectHooks, type ProjectHooksAction, type ProjectHooksView } from "../../api/project-hooks";
import { ErrorState } from "../../components/common";
import { useLocale } from "../../lib/locale";
import "./project-hooks.css";

interface ProjectHooksProps { client: APIClient; threadID: string }
const projectHooksKey = (client: APIClient, threadID: string) => ["thread", threadID, "project-hooks", client.baseURL] as const;

export function ProjectHooks(props: ProjectHooksProps) {
  return <ProjectHooksForTask key={props.client.baseURL + ":" + props.threadID} {...props} />;
}

function ProjectHooksForTask({ client, threadID }: ProjectHooksProps) {
  const { t } = useLocale();
  const queries = useQueryClient(), key = projectHooksKey(client, threadID);
  const state = useQuery({ queryKey: key, queryFn: ({ signal }) => readProjectHooks(client, threadID, signal), retry: false, refetchOnMount: "always" });
  const review = useMutation({ mutationFn: ({ view, action }: { view: ProjectHooksView; action: ProjectHooksAction }) => reviewProjectHooks(client, view, action),
    onSuccess: (view) => queries.setQueryData(key, view),
    onSettled: () => { void queries.invalidateQueries({ queryKey: key }); } });
  const view = state.data, busy = state.isFetching || review.isPending;
  const canReview = client.hasControl && !busy && !state.isError && Boolean(view?.fingerprint);
  const labels = { not_configured: t("尚未配置", "Not configured"), needs_review: t("等待首次审阅", "First review required"),
    trusted: t("已信任当前配置", "Current configuration trusted"), changed: t("配置已变化", "Configuration changed"), disabled: t("已停用", "Disabled") };

  return <section className="v2-task-delivery v2-project-hooks" aria-label={t("项目自动动作", "Project automatic actions")}>
    <div className="v2-delivery-heading"><h2>{t("项目自动动作", "Project automatic actions")}</h2>
      <button type="button" disabled={busy} onClick={() => { review.reset(); void state.refetch(); }}>{t("刷新配置", "Refresh configuration")}</button></div>
    <p className="v2-delivery-muted">{t("首次信任下面的具体配置后，后续任务自动沿用；配置变化时重新确认。自动动作继续遵守任务权限和预算，需要审批时等待你处理。",
      "Trust this exact configuration once for later tasks to reuse it. Configuration changes require review again. Automatic actions still follow task permissions and budgets and wait for required approval.")}</p>
    {state.isLoading && <p role="status">{t("正在读取项目自动动作…", "Reading project automatic actions…")}</p>}
    {state.isError && <ErrorState error={state.error} />}
    {view && <>
      <div className="v2-project-hooks-state"><strong role="status">{labels[view.state]}</strong><span>{view.message}</span></div>
      {view.hooks.length > 0 && <ul className="v2-project-hooks-list">{view.hooks.map((hook) => <li key={hook.event}>
        <strong>{hook.event === "after_edit" ? t("编辑后格式化", "Format after edits") : t("结束前测试", "Test before finishing")}</strong>
        <span>{t("注册动作", "Registered action")} <code>{hook.command_id}</code></span>
        <span>{t("目标", "Target")} <code>{hook.target}</code></span>
      </li>)}</ul>}
      {view.hooks.length > 0 && <details><summary>{t("配置标识", "Configuration identity")}</summary>
        <dl><div><dt>{t("配置指纹", "Fingerprint")}</dt><dd><code>{view.fingerprint}</code></dd></div>
          <div><dt>{t("信任版本", "Trust revision")}</dt><dd>{view.revision}</dd></div></dl></details>}
      {!view.matches_task && <p className="v2-delivery-muted">{t("当前任务使用已保存的旧配置。对这份配置的信任将在新任务中生效。", "This task uses its saved older configuration. Trust for this configuration takes effect in new tasks.")}</p>}
      {view.hooks.length > 0 && <div className="v2-delivery-actions">
        {!view.trusted && <button type="button" className="v2-delivery-primary" disabled={!canReview} onClick={() => review.mutate({ view, action: "trust" })}>
          {view.state === "changed" ? t("信任变更后的配置", "Trust changed configuration") : view.state === "disabled" ? t("重新信任当前配置", "Trust current configuration again") : t("信任当前配置", "Trust current configuration")}</button>}
        {(view.trusted || view.state === "changed") && <button type="button" disabled={!canReview} onClick={() => review.mutate({ view, action: "disable" })}>{t("停用自动动作", "Disable automatic actions")}</button>}
      </div>}
    </>}
    {!client.hasControl && <p className="v2-delivery-muted">{t("当前连接仅可查看配置。", "This connection can only view the configuration.")}</p>}
    {review.isError && <ErrorState error={review.error} />}
  </section>;
}
