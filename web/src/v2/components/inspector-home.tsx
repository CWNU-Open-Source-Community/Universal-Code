import { useLocale } from "../../lib/locale";
import { Microscope } from "lucide-react";
import type { APIClient } from "../../api/client";
import { InspectorRecordBrowser } from "./inspector-record-browser";
import "./inspector.css";

export function V2InspectorHome({ client, onOpenTool }: {
  client: APIClient;
  onOpenTool: (tool: "run" | "session" | "schedule", id?: string) => void;
}) {
  const { t } = useLocale();
  return <main className="v2-inspector-home">
    <Microscope aria-hidden="true" size={28} /><h1>{t("历史与诊断", "History and diagnostics")}</h1>
    <InspectorRecordBrowser client={client} onOpen={onOpenTool} />
  </main>;
}
