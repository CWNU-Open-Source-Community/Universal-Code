import { useQuery } from "@tanstack/react-query";
import { ChevronRight, FileDiff, RefreshCw } from "lucide-react";
import type { APIClient } from "../api/client";
import { formatBytes } from "../lib/format";
import { useLocale } from "../lib/locale";
import { EmptyState, ErrorState, LoadingState, StatusBadge } from "./common";
import "./repository-diff-panel.css";

export function RepositoryDiffPanel({ client, workspaceID, onRequestChange }: {
  client: APIClient;
  workspaceID: string;
  onRequestChange?: (path: string, baseHead: string) => void;
}) {
  const { t } = useLocale();
  const query = useQuery({
    queryKey: ["workspace", workspaceID, "repository-diff"],
    queryFn: ({ signal }) => client.repositoryDiff(workspaceID, signal),
    enabled: Boolean(workspaceID),
  });
  if (!workspaceID) return null;
  return <section aria-label={t("仓库差异", "Repository diff")} className="repository-diff-panel">
    <header className="projection-heading">
      <div><FileDiff aria-hidden="true" size={17} /><h2>{t("工作树差异", "Working tree diff")}</h2></div>
      <div>
        {query.data?.truncated && <StatusBadge status="truncated" />}
        <button aria-label={t("刷新仓库差异", "Refresh repository diff")} className="icon-button"
          disabled={query.isFetching} onClick={() => void query.refetch()}
          title={t("刷新", "Refresh")} type="button">
          <RefreshCw aria-hidden="true" className={query.isFetching ? "spin" : ""} size={15} />
        </button>
      </div>
    </header>
    {query.isLoading && <LoadingState label={t("正在加载仓库差异", "Loading repository diff")} />}
    {query.isError && <ErrorState error={query.error} />}
    {query.data && !query.data.available &&
      <EmptyState>{t("已登记工作区根目录中没有 Git 仓库", "No Git repository at the registered Workspace root")}</EmptyState>}
    {query.data?.available && <>
      <div className="repository-diff-summary">
        <span>{t(`${query.data.returned_count} 个文件`, `${query.data.returned_count} files`)}</span>
        <span>{formatBytes(query.data.total_patch_bytes)}</span>
        {query.data.omitted_count > 0 && <span>{t(`${query.data.omitted_count} 项已省略`, `${query.data.omitted_count} omitted`)}</span>}
        {query.data.redaction_count > 0 && <span>{t(`${query.data.redaction_count} 项已脱敏`, `${query.data.redaction_count} redactions`)}</span>}
      </div>
      {query.data.items.length === 0 ? <EmptyState>{t("工作树干净", "Working tree is clean")}</EmptyState> :
        <div className="repository-diff-list">
          {query.data.items.map((item) => <article key={`${workspaceID}:${item.path}`}>
            <details className="repository-diff-file" open>
              <summary>
                <ChevronRight aria-hidden="true" className="repository-diff-chevron" size={14} />
                <span className="repository-diff-file-heading">
                  <code title={item.path}>{item.path}</code>
                  <span className="repository-diff-file-counts">{t(`${item.added_lines} 行新增 / ${item.deleted_lines} 行删除`, `${item.added_lines} added / ${item.deleted_lines} deleted`)}</span>
                  {item.redacted && <StatusBadge status="redacted" />}
                  <StatusBadge status={item.content_state} />
                </span>
              </summary>
              {onRequestChange && <div className="repository-diff-file-actions">
                <button className="compact-command" type="button"
                  onClick={() => onRequestChange(item.path, query.data!.base_head)}>
                  {t("引用此差异请求修改", "Request changes to this diff")}</button>
              </div>}
              {item.patch ? <pre>{item.patch}</pre> :
                <div className="repository-diff-omitted">{item.content_state}</div>}
            </details>
          </article>)}
        </div>}
    </>}
  </section>;
}
