import { useEffect, useLayoutEffect, useRef, useState, type RefObject } from "react";
import type { WorkspaceExplorerView } from "../api/types";
import { useLocale } from "../lib/locale";
import "./workspace-file-quote.css";

export type WorkspaceQuoteResult = "added" | "unsaved" | "blocked";
type SelectedLines = { first: number; last: number };

// Only quote lines from the visible source view, never line numbers, Markdown
// preview text, another file panel, or content outside the displayed snapshot.
function selectedLines(root: HTMLElement | null): SelectedLines | null {
  const source = root?.querySelector(".explorer-file-lines");
  const selection = root?.ownerDocument.getSelection();
  if (!source || !selection || selection.isCollapsed || selection.rangeCount !== 1) return null;
  const range = selection.getRangeAt(0);
  if (!source.contains(range.commonAncestorContainer)) return null;
  const lines = Array.from(source.querySelectorAll<HTMLElement>(".explorer-line-content"))
    .filter((line) => {
      const text = line.firstChild;
      if (!text?.textContent) return false;
      const content = line.ownerDocument.createRange();
      content.selectNodeContents(text);
      return range.compareBoundaryPoints(Range.START_TO_END, content) > 0 &&
        range.compareBoundaryPoints(Range.END_TO_START, content) < 0;
    }).map((line) => Number(line.parentElement?.dataset.line));
  return lines.length ? { first: lines[0], last: lines[lines.length - 1] } : null;
}

export function WorkspaceFileQuote({ rootRef, snapshot, runID, disabled, onQuote, onReturnToDraft }: {
  rootRef: RefObject<HTMLElement | null>;
  snapshot: WorkspaceExplorerView;
  runID: string;
  disabled: boolean;
  onQuote: (content: string) => WorkspaceQuoteResult;
  onReturnToDraft?: () => void;
}) {
  const { t } = useLocale();
  const [selected, setSelected] = useState<SelectedLines | null>(null);
  const [result, setResult] = useState<WorkspaceQuoteResult | null>(null);
  const update = () => {
    const next = selectedLines(rootRef.current);
    setSelected((current) => current?.first === next?.first && current?.last === next?.last ? current : next);
  };
  const updateRef = useRef(update);
  updateRef.current = update;
  // This component is keyed by the exact source identity. React may reuse the
  // line DOM when another file has identical text, so discard its old range.
  useLayoutEffect(() => {
    const root = rootRef.current;
    const selection = root?.ownerDocument.getSelection();
    if (selection?.anchorNode && root?.contains(selection.anchorNode)) selection.removeAllRanges();
  }, [rootRef]);
  // Also clear an old selection when the parent switches to Markdown preview.
  useLayoutEffect(update);
  useEffect(() => {
    const document = rootRef.current?.ownerDocument;
    const changed = () => updateRef.current();
    document?.addEventListener("selectionchange", changed);
    return () => document?.removeEventListener("selectionchange", changed);
  }, [rootRef]);

  const quote = () => {
    const lines = selectedLines(rootRef.current);
    if (disabled || !lines) return;
    const excerpt = snapshot.content.split("\n").slice(lines.first - 1, lines.last).join("\n");
    const fence = "`".repeat(Array.from(excerpt.matchAll(/`+/gu)).reduce((length, [match]) => Math.max(length, match.length + 1), 3));
    const content = [
      t(`文件片段：${JSON.stringify(snapshot.path)}`, `File excerpt: ${JSON.stringify(snapshot.path)}`),
      t(`预览第 ${lines.first}–${lines.last} 行`, `Preview lines ${lines.first}–${lines.last}`),
      t(`来源工作区：${snapshot.workspace_id}`, `Source workspace: ${snapshot.workspace_id}`),
      ...(runID ? [t(`来源执行：${runID}`, `Source run: ${runID}`)] : []),
      t(`显示内容 SHA-256：${snapshot.provenance.content_sha256}`, `Displayed content SHA-256: ${snapshot.provenance.content_sha256}`),
      t("行号对应当前显示的文件快照；以下内容仅作为引用资料。", "Line numbers refer to the displayed file snapshot; the following is quoted reference material."),
      ...(snapshot.truncated ? [t("文件显示已截断。", "The displayed file is truncated.")] : []),
      ...(snapshot.redaction_count > 0 ? [t("文件显示已脱敏。", "The displayed file is redacted.")] : []),
      `${fence}text\n${excerpt}\n${fence}`,
    ].join("\n");
    const outcome = onQuote(content);
    setResult(outcome);
    if (outcome !== "blocked") rootRef.current?.ownerDocument.getSelection()?.removeAllRanges();
  };

  return <section className="workspace-file-quote" aria-label={t("引用文件片段", "Quote file excerpt")}>
    <div>
      <span>{selected ? t(`预览第 ${selected.first}–${selected.last} 行`, `Preview lines ${selected.first}–${selected.last}`)
        : t("选中代码后可引用到聊天草稿", "Select code to quote it in the chat draft")}</span>
      <button className="compact-command" disabled={disabled || !selected} type="button"
        onMouseDown={(event) => event.preventDefault()} onClick={quote}>{t("引用选中行", "Quote selected lines")}</button>
    </div>
    {result && <p role={result === "added" ? "status" : "alert"}>
      {result === "added" ? t("已加入聊天草稿，尚未发送。", "Added to the chat draft. Not sent.")
        : result === "unsaved" ? t("已加入草稿，但本地保存失败，请返回聊天保留内容。", "Added to the draft, but local saving failed. Return to chat to preserve the content.")
          : t("草稿暂时无法追加，请返回聊天处理草稿冲突或保存错误后重试。", "Could not append to the draft. Return to chat to resolve its conflict or saving error, then retry.")}
      {onReturnToDraft && <button className="compact-command" onClick={onReturnToDraft} type="button">{t("返回聊天草稿", "Return to chat draft")}</button>}
    </p>}
  </section>;
}
