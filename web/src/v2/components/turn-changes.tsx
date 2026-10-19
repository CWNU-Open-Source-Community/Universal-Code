import { useEffect, useId, useMemo, useRef, useState, type RefObject } from "react";
import { createPortal } from "react-dom";
import { useQuery } from "@tanstack/react-query";
import { ChevronDown, ChevronUp, FileDiff, X } from "lucide-react";
import type { APIClient } from "../../api/client";
import type { ThreadChangeFile, ThreadChangeSummary } from "../../api/thread-changes";
import { parseUnifiedDiff } from "../../components/unified-diff";
import { useModalFocusTrap } from "../../hooks/use-modal-focus-trap";
import { useLocale } from "../../lib/locale";
import { TurnChangeUndo } from "./turn-change-undo";
import "./turn-changes.css";

function ChangeCounts({ value }: { value: Pick<ThreadChangeSummary, "additions" | "deletions" | "counts_available"> }) {
  const { t, locale } = useLocale();
  return value.counts_available ? <span className="v2-change-counts"
    aria-label={t(`新增 ${value.additions} 行，删除 ${value.deletions} 行`, `${value.additions} lines added, ${value.deletions} lines removed`)}>
    <span className="v2-change-added">+{value.additions.toLocaleString(locale)}</span>
    <span className="v2-change-removed">−{value.deletions.toLocaleString(locale)}</span>
  </span> : <span className="v2-change-unavailable">{t("行数不可用", "Counts unavailable")}</span>;
}

function fileLabel(file: ThreadChangeFile) {
  return file.destination_path ? `${file.path} → ${file.destination_path}` : file.path;
}

function ChangeDiffDialog({ summary, initialIndex, onClose, returnFocusRef }: {
  summary: ThreadChangeSummary; initialIndex: number; onClose: () => void;
  returnFocusRef: RefObject<HTMLElement | null>;
}) {
  const { t } = useLocale();
  const [selected, setSelected] = useState(initialIndex);
  const [visibleLines, setVisibleLines] = useState(500);
  const titleID = useId();
  const close = useRef<HTMLButtonElement>(null);
  const dialog = useModalFocusTrap<HTMLElement>(true, onClose, false, close,
    { isolateBackground: true, returnFocusRef });
  const file = summary.files[selected];
  const diff = useMemo(() => parseUnifiedDiff(file.diff), [file.diff]);
  return createPortal(<div className="v2-change-overlay" role="presentation" onMouseDown={(event) => {
    if (event.target === event.currentTarget) onClose();
  }}>
    <section className="v2-change-dialog" role="dialog" aria-modal="true" aria-labelledby={titleID} ref={dialog} tabIndex={-1}>
      <header className="v2-change-dialog-heading">
        <FileDiff size={19} aria-hidden="true" /><h2 id={titleID}>{t("本轮改动", "Changes in this turn")}</h2>
        <ChangeCounts value={summary} />
        <button aria-label={t("关闭改动", "Close changes")} onClick={onClose} ref={close} type="button"><X size={18} aria-hidden="true" /></button>
      </header>
      <div className="v2-change-dialog-body">
        <nav className="v2-change-file-nav" aria-label={t("改动文件", "Changed files")}>
          {summary.files.map((item, index) => <button aria-current={index === selected ? "true" : undefined}
            key={item.edit_ids[0]} onClick={() => { setSelected(index); setVisibleLines(500); }} title={fileLabel(item)} type="button">
            <span>{fileLabel(item)}</span><ChangeCounts value={item} />
          </button>)}
        </nav>
        <section className="v2-change-detail" aria-label={fileLabel(file)} key={selected}>
          <header><strong>{fileLabel(file)}</strong><ChangeCounts value={file} /></header>
          {file.reason && <p className="v2-change-note">{file.reason === "secrets_redacted"
            ? t("此文件含敏感内容，未展示差异及行数。", "This file contains sensitive content. Its diff and line counts are hidden.")
            : file.reason === "non_contiguous_edits"
              ? t("这段编辑记录不连续，无法确定本轮的完整差异。", "These edit records are not continuous, so the full diff for this turn is unavailable.")
              : t("暂时无法计算此文件的差异及行数。", "The diff and line counts for this file could not be calculated.")}</p>}
          {file.diff_truncated && <p className="v2-change-note">{t("差异超过显示上限，未完整展示。", "The diff exceeds the display limit and is not shown in full.")}</p>}
          {file.diff ? <div className="v2-change-diff" tabIndex={0} aria-label={t("代码差异", "Code diff")}>
            {diff.lines.slice(0, visibleLines).map((line, index) => <div className={`v2-change-diff-line is-${line.kind}`} key={index}>
              <span aria-hidden="true">{line.oldLine ?? ""}</span><span aria-hidden="true">{line.newLine ?? ""}</span>
              <code>{line.marker === "+" || line.marker === "-" ? line.marker : " "}{line.text || " "}</code>
            </div>)}
            {diff.lines.length > visibleLines && <button className="v2-change-more-lines" onClick={() => setVisibleLines((count) => count + 500)} type="button">
              {t(`已显示 ${visibleLines} / ${diff.lines.length} 行，继续显示`, `Showing ${visibleLines} / ${diff.lines.length} lines. Show more`)}
            </button>}
          </div> : !file.reason && !file.diff_truncated && <p className="v2-change-note">{t("此改动没有文本差异。", "This change has no text diff.")}</p>}
        </section>
      </div>
      <footer>{t("比较本轮已记录编辑的起始内容与最终内容。", "Compares the initial and final content of edits recorded in this turn.")}</footer>
    </section>
  </div>, document.body);
}

export function V2TurnChanges({ client, threadID, runID, sourceEventID }: {
  client: APIClient; threadID: string; runID: string; sourceEventID: string;
}) {
  const { t } = useLocale();
  const container = useRef<HTMLDivElement>(null);
  const returnFocusRef = useRef<HTMLElement | null>(null);
  const [visible, setVisible] = useState(false);
  const [expanded, setExpanded] = useState(false);
  const [selected, setSelected] = useState<number | null>(null);
  const listID = useId();
  // Historical replies load their summaries as they approach the viewport.
  useEffect(() => {
    if (!container.current) return;
    if (typeof IntersectionObserver === "undefined") { setVisible(true); return; }
    const observer = new IntersectionObserver((entries) => {
      if (entries.some((entry) => entry.isIntersecting)) { setVisible(true); observer.disconnect(); }
    }, { rootMargin: "300px" });
    observer.observe(container.current);
    return () => observer.disconnect();
  }, []);
  const query = useQuery({ queryKey: ["thread", threadID, "turn-changes", sourceEventID],
    queryFn: ({ signal }) => client.threadChanges(threadID, runID, sourceEventID, signal),
    enabled: visible, staleTime: Infinity, retry: false });
  const summary = query.data;
  const open = (index: number, trigger: HTMLElement) => { returnFocusRef.current = trigger; setSelected(index); };

  if (summary && !summary.files.length && !summary.partial) return null;
  return <div ref={container} className="v2-turn-changes-slot">
    {query.isError && <div className="v2-change-load-error" role="status">
      <span>{t("本轮改动加载失败", "Could not load this turn’s changes")}</span>
      <button disabled={query.isFetching} onClick={() => void query.refetch()} type="button">{t("重试", "Retry")}</button>
    </div>}
    {summary && <section className="v2-turn-changes" aria-label={t("本轮改动", "Changes in this turn")}>
      <header className="v2-turn-changes-heading" title={t("本轮已记录的文件编辑", "File edits recorded in this turn")}>
        <span className="v2-turn-changes-icon"><FileDiff size={21} aria-hidden="true" /></span>
        <div><h3>{summary.files.length ? t(`已编辑 ${summary.file_count} 个文件`, `${summary.file_count} ${summary.file_count === 1 ? "file" : "files"} edited`)
          : t("本轮改动", "Changes in this turn")}</h3><ChangeCounts value={summary} /></div>
        {summary.files.length > 0 && <span className="v2-turn-changes-actions">
          {client.hasWorkspaceCheckpointControl && typeof client.undoThreadChanges === "function" && <TurnChangeUndo client={client} summary={summary} />}
          <button className="v2-turn-changes-review" onClick={(event) => open(0, event.currentTarget)} type="button">{t("查看变更", "View changes")}</button>
        </span>}
      </header>
      <ul id={listID} className="v2-turn-changes-files">
        {(expanded ? summary.files : summary.files.slice(0, 3)).map((file, index) => <li key={file.edit_ids[0]}>
          <button title={fileLabel(file)} onClick={(event) => open(index, event.currentTarget)} type="button">
            <span className="v2-turn-changes-path">{fileLabel(file)}</span><ChangeCounts value={file} />
          </button>
        </li>)}
      </ul>
      {summary.files.length > 3 && <button className="v2-turn-changes-expand" aria-expanded={expanded} aria-controls={listID}
        onClick={() => setExpanded((value) => !value)} type="button">
        {expanded ? t("收起文件", "Show fewer files") : t(`再显示 ${summary.files.length - 3} 个文件`, `Show ${summary.files.length - 3} more files`)}
        {expanded ? <ChevronUp size={15} aria-hidden="true" /> : <ChevronDown size={15} aria-hidden="true" />}
      </button>}
      {summary.partial && <p className="v2-turn-changes-scope">{t("部分改动详情不可用", "Some change details are unavailable")}</p>}
    </section>}
    {summary && selected !== null && <ChangeDiffDialog summary={summary} initialIndex={selected}
      onClose={() => setSelected(null)} returnFocusRef={returnFocusRef} />}
  </div>;
}
