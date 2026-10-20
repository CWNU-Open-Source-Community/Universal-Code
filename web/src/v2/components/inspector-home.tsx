import { useLocale } from "../../lib/locale";
import { CalendarClock, Microscope, Settings } from "lucide-react";
import type { APIClient } from "../../api/client";
import type { V2SettingsSection } from "./sidebar";
import { InspectorRecordBrowser } from "./inspector-record-browser";
import "./inspector.css";

export function V2InspectorHome({ client, onOpenTool, onOpenSettings }: {
  client: APIClient;
  onOpenTool: (tool: "run" | "session" | "schedule", id?: string) => void;
  onOpenSettings: (section: V2SettingsSection) => void;
}) {
  const { t } = useLocale();
  return <main className="v2-inspector-home">
    <Microscope aria-hidden="true" size={28} /><h1>{t("观察与记录", "Observe and records")}</h1>
    <p>{t("从侧栏选择一个对话查看执行记录，或打开下方的精确运行与会话。", "Select a conversation in the sidebar to view execution records, or open a specific run or session below.")}</p>
    <p>{t("返回对话后可以继续编辑已保留的草稿。", "Return to the conversation to continue editing your saved draft.")}</p>
    <InspectorRecordBrowser client={client} onOpen={onOpenTool} />
    <nav className="v2-inspector-home-utilities" aria-label={t("观察与连接", "Observation and connections")}><button onClick={() => onOpenTool("schedule")} type="button">
      <CalendarClock aria-hidden="true" size={16} />{t("自动化", "Automations")}</button>
      <button onClick={() => onOpenSettings("connections")} type="button">
        <Settings aria-hidden="true" size={16} />{t("连接与环境", "Connections and environment")}</button></nav>
  </main>;
}
