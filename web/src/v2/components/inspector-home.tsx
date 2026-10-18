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
  return <main className="v2-inspector-home">
    <Microscope aria-hidden="true" size={28} /><h1>观察与记录</h1>
    <p>从侧栏选择一个对话查看执行记录，或打开下方的精确运行与会话。</p>
    <p>返回对话后可以继续编辑已保留的草稿。</p>
    <InspectorRecordBrowser client={client} onOpen={onOpenTool} />
    <nav className="v2-inspector-home-utilities" aria-label="观察与连接"><button onClick={() => onOpenTool("schedule")} type="button">
      <CalendarClock aria-hidden="true" size={16} />自动化</button>
      <button onClick={() => onOpenSettings("connections")} type="button">
        <Settings aria-hidden="true" size={16} />连接与环境</button></nav>
  </main>;
}
