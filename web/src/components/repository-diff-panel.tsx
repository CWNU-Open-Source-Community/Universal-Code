import { useQuery } from "@tanstack/react-query";
import { useState } from "react";
import { ChevronRight, FileDiff, Quote, RefreshCw } from "lucide-react";
import type { APIClient } from "../api/client";
import { formatBytes } from "../lib/format";
import { useLocale } from "../lib/locale";
import { parseReviewDiff } from "../lib/review-diff";
import { EmptyState, ErrorState, LoadingState, StatusBadge, StatusLabel } from "./common";
import "./repository-diff-panel.css";

type RepositoryDiffPanelProps = {
  client: APIClient;
  workspaceID: string;
  onRequestChange?: (path: string, baseHead: string) => void;
};

export function RepositoryDiffPanel(props: RepositoryDiffPanelProps) {
  return <RepositoryDiffPages key={props.workspaceID} {...props} />;
}

function RepositoryDiffPatch({ patch }: { patch: string }) {
  const parsed = parseReviewDiff(patch);
  if (!parsed.hunks.length) return <pre>{patch}</pre>;
  return <div className="repository-diff-hunks">
    {parsed.hunks.map((hunk, hunkIndex) => <div className="repository-diff-hunk" key={hunkIndex}>
      {hunk.preamble && <div className="repository-diff-preamble"><code>{hunk.preamble}</code></div>}
      <div className="repository-diff-hunk-heading"><code>{hunk.header}</code></div>
      <div className="repository-diff-lines">
        {hunk.lines.map((line, lineIndex) => <div className={`repository-diff-line is-${line.kind}`} key={lineIndex}>
          <span className="repository-diff-marker" aria-hidden={line.kind === "meta" ? true : undefined}>
            {line.kind === "meta" ? "" : line.text.slice(0, 1)}
          </span>
          <code>{line.kind === "meta" ? line.text : line.text.slice(1) || " "}</code>
        </div>)}
      </div>
    </div>)}
    {parsed.trailing && <div className="repository-diff-preamble"><code>{parsed.trailing}</code></div>}
  </div>;
}

function RepositoryDiffPages({ client, workspaceID, onRequestChange }: RepositoryDiffPanelProps) {
  const { t } = useLocale();
  const [pages, setPages] = useState([""]);
  const after = pages[pages.length - 1];
  const changeStatusLabel = (status: string) => status === "copied" ? t("复制", "copied") :
    <StatusLabel status={status === "conflicted" ? "conflict" : status} />;
  const previewReason = (state: string) => {
    switch (state) {
      case "binary_or_unsupported": return t("二进制或不支持文本预览", "Binary or unsupported text format");
      case "size_limited": return t("文件超出预览上限", "File exceeds preview limit");
      case "computation_limited": return t("计算受限，行数未统计", "Computation limited; line counts unavailable");
      case "linked": return t("链接文件不提供预览", "Linked file preview unavailable");
      case "unavailable": return t("文件内容不可用", "File content unavailable");
      case "text": return t("无文本差异", "No text changes");
      default: return t("暂无差异预览", "Diff preview unavailable");
    }
  };
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
          title={t("工作树变化后，从第一页刷新", "After working-tree changes, refresh from the first page")} type="button">
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
          {query.data.items.map((item) => {
            const heading = <span className="repository-diff-file-heading">
                <code title={item.path}>{item.path}</code>
                <span className="repository-diff-file-meta">
                  {item.staging !== "unmodified" && item.staging !== "untracked" &&
                    <span>{t("暂存区：", "Index: ")}{changeStatusLabel(item.staging)}</span>}
                  {item.worktree !== "unmodified" && item.worktree !== "untracked" &&
                    <span>{t("工作区：", "Working tree: ")}{changeStatusLabel(item.worktree)}</span>}
                  {(item.staging === "untracked" || item.worktree === "untracked") && <StatusLabel status="untracked" />}
                  {item.statistics_complete && <span className="repository-diff-file-counts">{t(`${item.added_lines} 行新增 / ${item.deleted_lines} 行删除`, `${item.added_lines} added / ${item.deleted_lines} deleted`)}</span>}
                  {item.redacted && <span>{item.patch ? t("已脱敏，原文行数未统计", "Redacted; original line counts unavailable") : t("敏感变更已隐藏", "Sensitive changes hidden")}</span>}
                  {!item.patch && !(item.redacted && item.content_state === "text") && <span>{previewReason(item.content_state)}</span>}
                  {item.patch && item.truncated && <span title={item.statistics_complete ? t("行数包含该文件全部变更", "Counts include all changes in this file") : undefined}>{t("预览已截断", "Preview truncated")}</span>}
                </span>
              </span>;
            return <article className={onRequestChange ? "repository-diff-item has-action" : "repository-diff-item"} key={`${workspaceID}:${item.path}`}>
              {item.patch ? <details className="repository-diff-file" open>
                <summary>
                  <ChevronRight aria-hidden="true" className="repository-diff-chevron" size={14} />
                  {heading}
                </summary>
                <RepositoryDiffPatch patch={item.patch} />
              </details> : <div className="repository-diff-file-row">{heading}</div>}
              {onRequestChange && <button className="repository-diff-quote" type="button"
                  aria-label={t(`引用 ${item.path}`, `Quote ${item.path}`)} title={t("引用到对话", "Quote in chat")}
                  onClick={() => onRequestChange(item.path, query.data!.base_head)}>
                  <Quote aria-hidden="true" size={15} /></button>}
            </article>;
          })}
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
