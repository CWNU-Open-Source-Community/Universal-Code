import type { ReactNode } from "react";
import { ArrowLeft } from "lucide-react";
import type { APIClient } from "../../api/client";
import { useLocale } from "../../lib/locale";
import { threadActivityLabel } from "../../lib/thread-activity-label";
import { useV2ThreadExecution } from "./thread-execution-control";
import type { V2SettingsSection } from "./sidebar";
import "./resource-page.css";

export function SourceResourcePage({ client, title, resourceID, threadID, onBack, onOpenSettings, children }: {
  client: APIClient;
  title: string;
  resourceID: string;
  threadID: string;
  onBack: () => void;
  onOpenSettings: (section: V2SettingsSection) => void;
  children: ReactNode;
}) {
  const { t } = useLocale();
  const backLabel = threadID ? t("返回对话", "Back to conversation") : t("返回历史记录", "Back to history");
  return <section className="v2-resource-page">
    <header><button onClick={onBack} type="button"><ArrowLeft aria-hidden="true" size={16} />{backLabel}</button>
      <strong>{title}</strong></header>
    <div className="v2-resource-scope">
      {threadID && <SourceThreadActivity client={client} threadID={threadID} />}
      <details className="v2-resource-details"><summary>{t("来源与设置", "Source and settings")}</summary>
        {resourceID && <p>{t("当前记录：", "Current record: ")}<code>{resourceID}</code></p>}
        {threadID ? <>
          <p>{t("来源对话：", "Source conversation: ")}<code>{threadID}</code></p>
          <p>{t("上方显示来源对话的当前活动；下方是所选记录，可能来自较早的执行。", "The source conversation's current activity appears above. The selected record below may come from an earlier run.")}</p>
        </> : <p>{t("此记录尚未绑定来源对话。", "This record is not linked to a source conversation.")}</p>}
        <button onClick={() => onOpenSettings("general")} type="button">{t("设置", "Settings")}</button>
      </details>
    </div>
    <div className="v2-resource-body">
      {resourceID ? children : <p role="alert">{threadID
        ? t("地址缺少记录标识，请返回对话重新选择。", "The address has no record ID. Return to the conversation and select a record.")
        : t("地址缺少记录标识，请返回历史记录重新选择。", "The address has no record ID. Return to history and select a record.")}</p>}
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
