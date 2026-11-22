import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useState } from "react";
import { LoaderCircle, Tag } from "lucide-react";
import type { APIClient } from "../api/client";
import { ErrorState, LoadingState } from "./common";
import { useLocale } from "../lib/locale";

export function PriceSnapshotsSection({ client }: { client: APIClient }) {
  const { t } = useLocale();
  const queryClient = useQueryClient();
  const [document, setDocument] = useState("");
  const query = useQuery({
    queryKey: ["models", "prices"],
    queryFn: ({ signal }) => client.listPriceSnapshots(signal),
  });
  const importMutation = useMutation({
    mutationFn: () => client.importPriceSnapshot({ version: "price_snapshot.v1", document },
      `web-price-import-${globalThis.crypto.randomUUID()}`),
    onSuccess: () => {
      setDocument("");
      void queryClient.invalidateQueries({ queryKey: ["models", "prices"] });
    },
  });
  return (
    <section className="model-availability-section">
      <h3><Tag aria-hidden="true" size={14} />{t("价格快照", "Price snapshots")}</h3>
      <p>{t("启用美元费用上限时，导入价格文档以估算任务预算。实际费用可在供应商账单中查看。", "Import a pricing document to estimate task budgets when using a USD spending limit. View actual charges in your provider account.")}</p>
      {query.isLoading && <LoadingState label={t("加载价格快照", "Loading price snapshots")} />}
      {query.isError && <ErrorState error={query.error} />}
      {query.data && (
        <div className="provider-credential-list">
          {query.data.items.length === 0 && <div className="projection-placeholder">{client.hasControl
            ? t("尚无价格快照。", "No price snapshots yet.")
            : t("尚无价格快照。连接具备控制权限的服务后可导入。", "No price snapshots yet. Connect with control access to import one.")}</div>}
          {query.data.items.map((item) => (
            <div className="provider-credential-row" key={item.id}>
              <div><strong>{item.id}</strong><small>{item.source} · {item.currency} · {item.entry_count} {t("条", "entries")}</small></div>
              <code>{item.fingerprint.slice(0, 16)}</code>
            </div>
          ))}
        </div>
      )}
      {client.hasControl && (
        <div className="run-execution-control">
          <textarea aria-label={t("价格文档", "price_snapshot.v1 document")} onChange={(event) => setDocument(event.target.value)}
            placeholder='{"protocol_version":"price_snapshot.v1","id":"...","source":"operator_import","currency":"USD","imported_by":"operator","valid_from":"...","valid_until":"...","entries":[...]}'
            rows={6} value={document} />
          <button className="command-button" disabled={importMutation.isPending || !document}
            onClick={() => importMutation.mutate()} type="button">
            {importMutation.isPending ? <LoaderCircle aria-hidden="true" className="spin" size={15} /> : <Tag aria-hidden="true" size={15} />}
            {t("导入价格快照", "Import price snapshot")}
          </button>
          {importMutation.error && <div className="inline-warning" role="alert">
            {importMutation.error instanceof Error ? importMutation.error.message : t("价格导入失败", "Price import failed")}
          </div>}
        </div>
      )}
    </section>
  );
}

