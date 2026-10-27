import { useQuery } from "@tanstack/react-query";
import { useState } from "react";
import { ChevronRight, FileDiff, RefreshCw } from "lucide-react";
import type { APIClient } from "../api/client";
import { formatBytes } from "../lib/format";
import { useLocale } from "../lib/locale";
import { EmptyState, ErrorState, LoadingState, StatusBadge } from "./common";
import "./repository-diff-panel.css";

type RepositoryDiffPanelProps = {
  client: APIClient;
  workspaceID: string;
  onRequestChange?: (path: string, baseHead: string) => void;
};

export function RepositoryDiffPanel(props: RepositoryDiffPanelProps) {
  return <RepositoryDiffPages key={props.workspaceID} {...props} />;
}

function RepositoryDiffPages({ client, workspaceID, onRequestChange }: RepositoryDiffPanelProps) {
  const { t } = useLocale();
  const [pages, setPages] = useState([""]);
  const after = pages[pages.length - 1];
  const query = useQuery({
    queryKey: ["workspace", workspaceID, "repository-diff", ...(after ? [after] : [])],
    queryFn: ({ signal }) => client.repositoryDiff(workspaceID, signal, after),
    enabled: Boolean(workspaceID),
    gcTime: 0,
  });
  if (!workspaceID) return null;
  return <section aria-label={t("仓库差异", "Repository diff")} className="repository-diff-panel">
    <header className="projection-heading">
      <div><FileDiff aria-hidden="true" size={17} /><h2>{t("工作树差异", "Working tree diff")}</h2></div>
      <div>
        {query.data?.truncated && <StatusBadge status="truncated" />}
        <button aria-label={t("刷新仓库差异", "Refresh repository diff")} className="icon-button"
          disabled={query.isFetching} onClick={() => {
            if (after) setPages([""]);
            else void query.refetch();
          }}
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
        <span>{t(`本页 ${query.data.returned_count} 个文件`, `${query.data.returned_count} files on this page`)}</span>
        <span>{formatBytes(query.data.total_patch_bytes)}</span>
        {query.data.omitted_count > 0 && <span>{t(`后续还有 ${query.data.omitted_count} 个文件`, `${query.data.omitted_count} more files`)}</span>}
        {query.data.redaction_count > 0 && <span>{t(`${query.data.redaction_count} 项已脱敏`, `${query.data.redaction_count} redactions`)}</span>}
      </div>
      {query.data.items.length === 0 ? <EmptyState>{after ? t("本页没有剩余文件，请刷新查看最新变更", "No remaining files on this page. Refresh to see current changes.") : t("工作树干净", "Working tree is clean")}</EmptyState> :
        <div className="repository-diff-list">
          {query.data.items.map((item) => <article key={`${workspaceID}:${item.path}`}>
            <details className="repository-diff-file" open>
              <summary>
                <ChevronRight aria-hidden="true" className="repository-diff-chevron" size={14} />
                <span className="repository-diff-file-heading">
                  <code title={item.path}>{item.path}</code>
                  <span className="repository-diff-file-counts">{item.statistics_complete ?
                    t(`${item.added_lines} 行新增 / ${item.deleted_lines} 行删除`, `${item.added_lines} added / ${item.deleted_lines} deleted`) :
                    item.redacted ? t("已脱敏，原文行数未统计", "Redacted; original line counts unavailable") :
                      t("行数未统计", "Line counts unavailable")}</span>
                  {item.redacted && <StatusBadge status="redacted" />}
                  <StatusBadge status={item.content_state} label={item.content_state === "computation_limited" ? t("计算受限", "Computation limited") : undefined} />
                </span>
              </summary>
              {onRequestChange && <div className="repository-diff-file-actions">
                <button className="compact-command" type="button"
                  onClick={() => onRequestChange(item.path, query.data!.base_head)}>
                  {t("引用此差异请求修改", "Request changes to this diff")}</button>
              </div>}
              {item.truncated && <p className="repository-diff-notice">
                {item.statistics_complete ? t("仅展示部分差异；上方行数包含该文件全部变更。", "Partial preview; counts above include all changes in this file.") :
                  t("仅展示部分差异。", "Partial diff preview.")}
              </p>}
              {item.patch ? <pre>{item.patch}</pre> :
                <div className="repository-diff-omitted">{
                  item.content_state === "size_limited" ? t("文件超过 4 MiB，未生成文本差异。", "File exceeds 4 MiB; text diff unavailable.") :
                  item.content_state === "computation_limited" ? t("变更过于复杂或行数过多，超出计算预算；未生成完整差异和统计。", "Diff exceeds the computation or line budget; complete diff and counts unavailable.") :
                  item.content_state === "text" ? t("没有可展示的文本差异", "No text differences to display") : item.content_state
                }</div>}
            </details>
          </article>)}
        </div>}
    </>}
    {(pages.length > 1 || query.data?.next_after) && <nav className="repository-diff-pagination" aria-label={t("差异分页", "Diff pages")}>
      <button type="button" className="compact-command" disabled={pages.length === 1 || query.isFetching}
        onClick={() => setPages((current) => current.slice(0, -1))}>{t("上一页", "Previous page")}</button>
      <span>{t(`第 ${pages.length} 页`, `Page ${pages.length}`)}</span>
      <button type="button" className="compact-command" disabled={!query.data?.next_after || query.isFetching}
        onClick={() => { if (query.data?.next_after) setPages((current) => [...current, query.data.next_after]); }}>{t("下一页", "Next page")}</button>
    </nav>}
  </section>;
}
