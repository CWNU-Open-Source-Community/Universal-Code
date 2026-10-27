import { useQuery } from "@tanstack/react-query";
import { ChevronRight, FileDiff, Quote, RefreshCw } from "lucide-react";
import type { APIClient } from "../api/client";
import { formatBytes } from "../lib/format";
import { useLocale } from "../lib/locale";
import { EmptyState, ErrorState, LoadingState, StatusBadge, StatusLabel } from "./common";
import "./repository-diff-panel.css";

export function RepositoryDiffPanel({ client, workspaceID, onRequestChange }: {
  client: APIClient;
  workspaceID: string;
  onRequestChange?: (path: string, baseHead: string) => void;
}) {
  const { t } = useLocale();
  const changeStatusLabel = (status: string) => status === "copied" ? t("复制", "copied") :
    <StatusLabel status={status === "conflicted" ? "conflict" : status} />;
  const previewReason = (state: string) => {
    switch (state) {
      case "binary_or_unsupported": return t("二进制或不支持文本预览", "Binary or unsupported text format");
      case "size_limited": return t("文件超出预览上限", "File exceeds preview limit");
      case "linked": return t("链接文件不提供预览", "Linked file preview unavailable");
      case "unavailable": return t("文件内容不可用", "File content unavailable");
      case "text": return t("无文本差异", "No text changes");
      default: return t("暂无差异预览", "Diff preview unavailable");
    }
  };
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
          {query.data.items.map((item) => {
            const heading = <span className="repository-diff-file-heading">
                <code title={item.path}>{item.path}</code>
                <span className="repository-diff-file-meta">
                  {item.staging !== "unmodified" && item.staging !== "untracked" &&
                    <span>{t("暂存区：", "Index: ")}{changeStatusLabel(item.staging)}</span>}
                  {item.worktree !== "unmodified" && item.worktree !== "untracked" &&
                    <span>{t("工作区：", "Working tree: ")}{changeStatusLabel(item.worktree)}</span>}
                  {(item.staging === "untracked" || item.worktree === "untracked") && <StatusLabel status="untracked" />}
                  {item.content_state === "text" && <span className="repository-diff-file-counts">{t(`${item.added_lines} 行新增 / ${item.deleted_lines} 行删除`, `${item.added_lines} added / ${item.deleted_lines} deleted`)}</span>}
                  {item.redacted && <StatusBadge status="redacted" />}
                  {!item.patch && <span>{previewReason(item.content_state)}</span>}
                  {item.patch && item.truncated && <span>{t("预览已截断", "Preview truncated")}</span>}
                </span>
              </span>;
            return <article className={onRequestChange ? "repository-diff-item has-action" : "repository-diff-item"} key={`${workspaceID}:${item.path}`}>
              {item.patch ? <details className="repository-diff-file" open>
                <summary>
                  <ChevronRight aria-hidden="true" className="repository-diff-chevron" size={14} />
                  {heading}
                </summary>
                <pre>{item.patch}</pre>
              </details> : <div className="repository-diff-file-row">{heading}</div>}
              {onRequestChange && <button className="repository-diff-quote" type="button"
                  aria-label={t(`引用 ${item.path}`, `Quote ${item.path}`)} title={t("引用到对话", "Quote in chat")}
                  onClick={() => onRequestChange(item.path, query.data!.base_head)}>
                  <Quote aria-hidden="true" size={15} /></button>}
            </article>;
          })}
        </div>}
    </>}
  </section>;
}
