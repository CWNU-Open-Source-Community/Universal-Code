import { useLocale } from "../../lib/locale";
import type { APIClient } from "../../api/client";
import { HistoryRecordBrowser } from "./history-record-browser";

export function V2HistoryPage({ client, onOpenRecord }: {
  client: APIClient;
  onOpenRecord: (kind: "run" | "session", id: string) => void;
}) {
  const { t } = useLocale();
  return <main className="v2-history-page v2-page-scroll">
    <div className="v2-page-column">
      <h1>{t("历史记录", "History")}</h1>
      <HistoryRecordBrowser client={client} onOpen={onOpenRecord} />
    </div>
  </main>;
}
