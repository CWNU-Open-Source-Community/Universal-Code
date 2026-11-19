import { useQuery } from "@tanstack/react-query";
import { Eye, FileCheck2, RefreshCw, ShieldCheck } from "lucide-react";
import type { APIClient } from "../api/client";
import { formatDate } from "../lib/format";
import { useLocale } from "../lib/locale";
import { EmptyState, ErrorState, LoadingState, StatusBadge } from "./common";

export function EvidenceInventory({ client, runID, onOpenSource }: {
  client: APIClient;
  runID: string;
  onOpenSource: (sourceRef: string) => void;
}) {
  const { t } = useLocale();
  const query = useQuery({
    queryKey: ["run", runID, "evidence-inventory"],
    queryFn: ({ signal }) => client.evidenceInventory(runID, signal),
    enabled: Boolean(runID),
  });

  return <section className="evidence-inventory" aria-label={t("已附加证据清单", "Attached evidence inventory")}>
    <header className="operator-list-header">
      <div><FileCheck2 aria-hidden="true" size={16} /><h2>{t("已附加证据", "Attached evidence")}</h2></div>
      <div>
        {query.data?.truncated && <StatusBadge status="truncated" />}
        <button aria-label={t("刷新已附加证据", "Refresh attached evidence")} className="icon-button"
          disabled={query.isFetching} onClick={() => void query.refetch()}
          title={t("刷新证据", "Refresh evidence")} type="button">
          <RefreshCw aria-hidden="true" className={query.isFetching ? "spin" : ""} size={15} />
        </button>
      </div>
    </header>
    {query.isLoading && <LoadingState label={t("正在加载已附加证据", "Loading attached evidence")} />}
    {query.isError && <ErrorState error={query.error} />}
    {query.data?.items.length === 0 && <EmptyState>{t("暂无附件证据", "No attached evidence")}</EmptyState>}
    {query.data && query.data.items.length > 0 && <div className="evidence-inventory-list">
      {query.data.items.map((item) => <div key={item.attachment_id}>
        <ShieldCheck aria-hidden="true" size={16} />
        <span><strong>{item.source_ref}</strong><code>{item.content_sha256}</code>{item.instruction_authorized && <small>{t("含授权指令", "instruction authorized")}</small>}</span>
        <time dateTime={item.attached_at}>{formatDate(item.attached_at)}</time>
        <button aria-label={t(`打开 ${item.source_ref}`, `Open ${item.source_ref}`)} className="icon-button"
          onClick={() => onOpenSource(item.source_ref)} title={t("打开来源", "Open source")} type="button">
          <Eye aria-hidden="true" size={15} />
        </button>
      </div>)}
    </div>}
  </section>;
}
