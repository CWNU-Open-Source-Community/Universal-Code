import { useLocale } from "../../lib/locale";
import { History } from "lucide-react";
import type { APIClient } from "../../api/client";
import { HistoryRecordBrowser } from "./history-record-browser";
import "./history-page.css";

export function V2HistoryPage({ client, onOpenRecord }: {
  client: APIClient;
  onOpenRecord: (kind: "run" | "session", id: string) => void;
}) {
  const { t } = useLocale();
  return <main className="v2-history-page">
    <History aria-hidden="true" size={28} /><h1>{t("历史记录", "History")}</h1>
    <HistoryRecordBrowser client={client} onOpen={onOpenRecord} />
  </main>;
}
