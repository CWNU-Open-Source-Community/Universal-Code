import { useLocale } from "../../lib/locale";
import { ArrowLeft } from "lucide-react";
import type { APIClient } from "../../api/client";
import { RunWorkspace } from "../../components/run-workspace";
import { SessionWorkspace } from "../../components/session-workspace";
import { ScheduledTasksWorkspace } from "../../components/scheduled-tasks-workspace";
import { WorkbenchFrame } from "../../components/workbench-frame";
import { desktopBridgeAvailable } from "../../lib/desktop-bridge";
import { threadActivityLabel } from "../../lib/thread-activity-label";
import { useV2ThreadExecution } from "./thread-execution-control";
import type { V2SettingsSection } from "./sidebar";
import type { V2RunPane } from "../navigation";
import "./inspector.css";

// Advanced resource pages retain their exact scope and existing controls. They
// use the same application navigation/settings, and never infer a stale Thread.
export function V2InspectorTools({ client, tool, resourceID = "", pane, threadID, onBack, onOpenSettings, onOpenRun }: {
  client: APIClient;
  tool: "run" | "session" | "schedule";
  resourceID?: string;
  pane?: V2RunPane;
  threadID: string;
  onBack: () => void;
  onOpenSettings: (section: V2SettingsSection) => void;
  onOpenRun?: (runID: string) => void;
}) {
  const { t } = useLocale();
  const title = tool === "schedule" ? t("自动化", "Automations") : tool === "session" ? t("会话上下文", "Session context")
    : pane === "context" ? t("续接与记忆", "Continuation and memory") : pane === "checkpoints" ? t("工作区恢复", "Workspace recovery")
      : pane === "ui-evidence" ? t("界面观察证据", "UI observation evidence") : t("运行与工具", "Runs and tools");
  return <section className="v2-inspector-resource">
    <header><button onClick={onBack} type="button"><ArrowLeft aria-hidden="true" size={16} />{t("返回任务观察", "Back to task observation")}</button>
      <strong>{title}</strong></header>
    <div className="v2-resource-scope">
      {tool !== "schedule" && <p>{t("下方工具使用所选执行或会话记录。", "The tools below use the selected run or session record. ")}{threadID ? t("可在“来源与设置”中查看标识，并调整来源对话的任务权限。", "View identifiers and adjust the source conversation's permissions under Source and settings.") : t("此记录尚未绑定对话，请返回任务观察选择来源任务后设置权限。", "This record is not linked to a conversation. Return to task observation and select the source task to set permissions.")}</p>}
      {tool !== "schedule" && threadID && <SourceThreadActivity client={client} threadID={threadID} />}
      <details className="v2-inspector-resource-details"><summary>{t("来源与设置", "Source and settings")}</summary>
        {resourceID && <p>{tool === "schedule" ? t("来源执行：", "Source run: ") : t("当前记录：", "Current record: ")}<code>{resourceID}</code></p>}
        {tool !== "schedule" && threadID && <p>{t("上方显示来源对话的当前活动；下方是所选记录，可能来自较早的执行。", "The source conversation's current activity appears above. The selected record below may come from an earlier run.")}</p>}
        <button onClick={() => onOpenSettings("general")} type="button">{t("设置", "Settings")}</button>
      </details>
    </div>
    <div className="v2-inspector-tool-body">
      {tool === "schedule" ? <ScheduledTasksWorkspace client={client} initialRunID={resourceID} initialThreadID={threadID} />
        : !resourceID ? <p role="alert">{t("地址缺少记录标识，请返回任务观察重新选择。", "The address has no record ID. Return to task observation and select a record.")}</p>
          : <WorkbenchFrame client={client} desktop={desktopBridgeAvailable()} title={title}
            resourceKind={tool} runID={tool === "run" ? resourceID : ""}
            sessionID={tool === "session" ? resourceID : ""}>
            {tool === "run" ? <RunWorkspace client={client} key={`run:${resourceID}:${pane ?? "activity"}`} runID={resourceID}
              initialTab={pane}
              onOpenPlugins={() => onOpenSettings("extensions")} onOpenRun={onOpenRun} />
              : <SessionWorkspace client={client} key={`session:${resourceID}`} sessionID={resourceID}
                onOpenPlugins={() => onOpenSettings("extensions")} />}
          </WorkbenchFrame>}
    </div>
  </section>;
}

function SourceThreadActivity({ client, threadID }: { client: APIClient; threadID: string }) {
  const { t } = useLocale();
  const execution = useV2ThreadExecution(client, threadID);
  const label = threadActivityLabel({ threadID, execution: execution.data,
    readable: client.hasThreadExecutionRead === true, error: execution.isError }, t);
  return <p role="status" aria-label={t("来源任务的 Agent 活动", "Source task agent activity")}>{t(`来源任务的 Agent：${label}。`, `Source task agent: ${label}.`)}
    {execution.isError && <button type="button" onClick={() => void execution.refetch()}>{t("刷新执行状态", "Refresh execution status")}</button>}
  </p>;
}
