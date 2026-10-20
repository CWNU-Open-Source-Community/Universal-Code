import { useQuery } from "@tanstack/react-query";
import type { APIClient } from "../../api/client";
import type { RunDetailView, ThreadDetailView, WorkspaceView } from "../../api/types";
import { ExecutionInteractionPanel, ExecutionProfilePanel, StandardCodeReadinessPanel } from "../../components/run-permission-settings";
import { useLocale } from "../../lib/locale";
import { v2QueryKeys } from "../query-keys";

export function V2ExecutionSettings({ client, threadID, workspaces }: {
  client: APIClient;
  threadID: string;
  workspaces: WorkspaceView[];
}) {
  const { t } = useLocale();
  const thread = useQuery({
    queryKey: v2QueryKeys.thread(threadID),
    queryFn: ({ signal }) => client.get<ThreadDetailView>(
      `/threads/${encodeURIComponent(threadID)}`, {}, signal),
    enabled: Boolean(threadID),
  });
  const currentRun = thread.data?.active_run ?? thread.data?.last_run;
  const runID = currentRun?.id ?? "";
  const detail = useQuery({
    queryKey: ["run", runID],
    queryFn: ({ signal }) => client.get<RunDetailView>(`/runs/${encodeURIComponent(runID)}`, {}, signal),
    enabled: Boolean(runID),
  });
  const readiness = useQuery({
    queryKey: ["run", runID, "capability-readiness"],
    queryFn: ({ signal }) => client.runCapabilityReadiness(runID, signal),
    enabled: Boolean(runID),
  });
  const retry = () => {
    void thread.refetch();
    if (runID) {
      void detail.refetch();
      void readiness.refetch();
    }
  };
  const project = workspaces.find(({ id }) => id === thread.data?.thread.workspace_id);
  return <section aria-label={t("当前任务执行环境", "Task execution environment")} className="v2-settings-section v2-execution-settings">
    <h2>{t("当前任务执行环境", "Task execution environment")}</h2>
    {!threadID ? <p className="v2-settings-empty">{t("先打开一个对话，再选择执行环境。", "Open a conversation to choose an execution environment.")}</p>
      : thread.isError || detail.isError || readiness.isError
        ? <p role="alert">{t("无法读取当前任务的执行环境。", "Could not read this task's execution environment.")}<button onClick={retry} type="button">{t("重试执行环境", "Reload execution environment")}</button></p>
        : thread.isPending || detail.isPending || readiness.isPending || !detail.data || !readiness.data
          ? <p role="status">{t("正在读取执行环境…", "Loading execution environment…")}</p>
          : <>
            <div className="v2-settings-card v2-execution-help">
              <strong>{t("项目：", "Project: ")}{project?.name ?? t("当前对话的项目", "Current conversation's project")}</strong>
              <p>{detail.data.run.standard_code_preset_configured
                ? t("当前任务使用隔离工作区，文件编辑与沙箱命令都在该目录中进行。交付时可在“审阅改动”中核对并处理改动。", "This task uses an isolated workspace for file edits and sandboxed commands. Review and handle changes in Review changes when delivering.")
                : t("文件编辑使用已接入的项目目录。本地执行可用于创建工程和运行检查，具体命令的工作目录会显示在操作预览中。", "File edits use the connected project directory. Local execution can create projects and run checks. Each command's working directory appears in its preview.")}</p>
              <p>{t("先选择执行环境，再确认信任项目并启用 Code。运行期间需要更改时，先返回对话停止当前执行。", "Choose an execution environment, confirm project trust and enable Code. To change it while running, return to the conversation and stop execution first.")}</p>
              <details><summary>{t("权限与运行条件", "Permissions and execution requirements")}</summary><p>{t("审批偏好决定操作如何获批。隔离工作区、调试运行时、网络范围与浏览器能力分别配置；需要批准时，请核对具体操作、目录和用途。", "Approval preferences determine how actions are approved. Configure the isolated workspace, debug runtime, network scope and browser capabilities separately. When approval is requested, review the action, directory and purpose.")}</p></details>
            </div>
            <StandardCodeReadinessPanel client={client} detail={detail.data} readiness={readiness.data}
              threadID={threadID} key={`preset-${runID}`} />
            <details><summary>{t("查看单项执行与项目信任设置", "View execution and project trust settings")}</summary>
              <ExecutionProfilePanel client={client} detail={detail.data} readiness={readiness.data} key={`profile-${runID}`} />
              <ExecutionInteractionPanel client={client} detail={detail.data} readiness={readiness.data} key={`interaction-${runID}`} />
            </details>
          </>}
  </section>;
}
