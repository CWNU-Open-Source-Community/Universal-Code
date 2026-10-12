import { useQuery } from "@tanstack/react-query";
import type { APIClient } from "../../api/client";
import type { RunDetailView, TaskBudgetSettings, ThreadDetailView } from "../../api/types";
import { v2QueryKeys } from "../query-keys";
import { TaskConfiguration } from "./task-configuration";
import { useLocale } from "../../lib/locale";
import type { ProjectSkillChoice } from "../../api/project-skills";

export function TaskConfigurationSettings({ client, threadID, sourceRunID, draftWorkspaceID = "", draftBudget,
  onDraftBudgetChange, onDraftValidityChange, draftProjectSkills, onDraftProjectSkillsChange }: {
  client: APIClient;
  threadID: string;
  sourceRunID?: string;
  draftWorkspaceID?: string;
  draftBudget?: TaskBudgetSettings;
  onDraftBudgetChange?: (budget: TaskBudgetSettings | undefined) => void;
  onDraftValidityChange?: (valid: boolean) => void;
  draftProjectSkills?: ProjectSkillChoice;
  onDraftProjectSkillsChange?: (choice: ProjectSkillChoice) => void;
}) {
  const { t } = useLocale();
  const thread = useQuery({ queryKey: v2QueryKeys.thread(threadID), enabled: Boolean(threadID),
    queryFn: async ({ signal }) => {
      const detail = await client.get<ThreadDetailView>(`/threads/${encodeURIComponent(threadID)}`, {}, signal);
      if (detail?.thread.id !== threadID) throw new Error(t("任务配置来源不匹配。", "The task configuration source does not match."));
      return detail;
    } });
  const standaloneRun = useQuery({ queryKey: ["run", sourceRunID], enabled: !threadID && Boolean(sourceRunID),
    queryFn: async ({ signal }) => {
      const detail = await client.get<RunDetailView>(`/runs/${encodeURIComponent(sourceRunID!)}`, {}, signal);
      if (detail?.run.id !== sourceRunID || detail.run.mission_id !== detail.mission.id) throw new Error(t("执行配置来源不匹配。", "The execution configuration source does not match."));
      return detail;
    } });
  const detail = !thread.isError && thread.data?.thread.id === threadID ? thread.data : undefined;
  const selectedRun = sourceRunID ? detail?.runs.find((item) => item.run.id === sourceRunID)?.run
    ?? (detail?.active_run?.id === sourceRunID ? detail.active_run : detail?.last_run?.id === sourceRunID ? detail.last_run : undefined)
    : detail?.active_run ?? detail?.last_run;
  const directRun = !standaloneRun.isError && standaloneRun.data?.run.id === sourceRunID ? standaloneRun.data : undefined;
  const run = threadID ? selectedRun : directRun?.run;
  const pinned = Boolean(threadID || sourceRunID);
  const source = threadID ? thread : standaloneRun;
  const workspaceID = threadID ? detail?.thread.workspace_id ?? "" : directRun?.mission.workspace_id ?? "";
  return <><h1>{t("任务预算与项目配置", "Task budget and project configuration")}</h1><p className="v2-settings-lead">{pinned
    ? t("查看所选执行的预算、项目限制与配置来源。", "View the selected run's budget, project restrictions, and configuration sources.")
    : t("为当前项目的新任务设置预算。设置随草稿保留，发送时会再次核对项目配置。", "Set the budget for new tasks in this project. Settings stay with the draft; project configuration is checked again when you send.")}</p>
    {pinned ? run && workspaceID ? <TaskConfiguration key={`run:${run.id}`} client={client} workspaceID={workspaceID} run={run} />
      : <div className="v2-notice" role={source.isPending ? "status" : "alert"}>
        {source.isPending ? t("正在核对任务与执行配置来源…", "Checking the task and execution source…") : source.isError ? t("无法读取所选执行的配置来源，请重试。", "Could not read the selected execution source. Try again.")
          : t("所选执行或其工作区尚未找到。请返回任务，重新选择执行记录后读取配置。", "The selected execution or workspace was not found. Return to the task, select a run, and read its configuration again.")}
        {!source.isPending && <button onClick={() => void source.refetch()} type="button">{t("重新读取执行配置", "Read execution configuration again")}</button>}
      </div>
      : <TaskConfiguration key={`draft:${draftWorkspaceID}`} client={client} workspaceID={draftWorkspaceID} profile="code"
        budget={draftBudget} onBudgetChange={onDraftBudgetChange} onValidityChange={onDraftValidityChange}
        projectSkills={draftProjectSkills} onProjectSkillsChange={onDraftProjectSkillsChange}
        disabled={!client.hasThreadControl || !draftWorkspaceID} />}
  </>;
}
