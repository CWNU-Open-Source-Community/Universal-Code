import { Fragment, useId, useMemo, useRef, useState, type Ref } from "react";
import { WrapText } from "lucide-react";
import type { ReviewCommentResult } from "../review-comment-draft";
import "./review-diff-comment.css";
import { useLocale, type LocaleText } from "../../lib/locale";
import { parseReviewDiff, type ReviewDiffHunk, type ReviewDiffLine } from "../../lib/review-diff";

function fileContext(hunk: ReviewDiffHunk, t: LocaleText): string {
  return t(`旧文件路径：${hunk.oldPath ?? t("未提供", "Not provided")}\n新文件路径：${hunk.newPath ?? t("未提供", "Not provided")}`, `Old file path: ${hunk.oldPath ?? t("未提供", "Not provided")}\nNew file path: ${hunk.newPath ?? t("未提供", "Not provided")}`);
}

function lineContext(source: string, hunk: ReviewDiffHunk, line: ReviewDiffLine, t: LocaleText): string {
  const number = line.newLine ?? line.oldLine;
  return t(`${source}\n${fileContext(hunk, t)}\n差异片段：${hunk.header}\n${line.kind === "remove" ? "旧文件" : "新文件"}第 ${number} 行：\n${line.text}\n行号只对应上述差异版本，请先核对当前内容。`, `${source}\n${fileContext(hunk, t)}\nDiff hunk: ${hunk.header}\n${line.kind === "remove" ? "Old file" : "New file"}, line ${number}:\n${line.text}\nLine numbers refer only to this diff revision. Check current content first.`);
}

interface CommentTarget { patch: string; hunk: number; line: number; context: string; label: string; text: string }

export function DiffWrapButton({ wrapLines, onToggle, buttonRef }: { wrapLines: boolean; onToggle: () => void; buttonRef?: Ref<HTMLButtonElement> }) {
  const { t } = useLocale();
  return <button ref={buttonRef} className="v2-diff-wrap-toggle" type="button" aria-pressed={wrapLines}
    aria-label={t("长行换行", "Wrap long lines")} title={t("长行换行", "Wrap long lines")}
    onClick={onToggle}><WrapText size={16} aria-hidden="true" /></button>;
}

export function ReviewDiff({ patch, source, onFeedback, onAddComment, wrapLines: controlledWrapLines }: {
  patch: string; source: string; onFeedback?: (context: string) => void;
  onAddComment?: (context: string) => ReviewCommentResult; wrapLines?: boolean;
}) {
  const { t } = useLocale();
  const [localWrapLines, setLocalWrapLines] = useState(false);
  const wrapLines = controlledWrapLines ?? localWrapLines;
  const [comment, setComment] = useState<CommentTarget | null>(null);
  const [text, setText] = useState("");
  const [blocked, setBlocked] = useState(false);
  const trigger = useRef<HTMLButtonElement | null>(null);
  const toolbar = useRef<HTMLButtonElement | null>(null);
  const container = useRef<HTMLDivElement | null>(null);
  const inputID = useId();
  const parsed = useMemo(() => parseReviewDiff(patch), [patch]);
  const closeComment = () => {
    setComment(null); setText(""); setBlocked(false);
    requestAnimationFrame(() => (trigger.current?.isConnected ? trigger.current : toolbar.current ?? container.current)?.focus());
  };
  const addComment = () => {
    if (!comment || !text.trim() || !onAddComment) return;
    const result = onAddComment(t(`审阅意见：\n${comment.context}\n要求：${text}`, `Review comment:\n${comment.context}\nRequest: ${text}`));
    if (result === "blocked") { setBlocked(true); return; }
    closeComment();
  };
  const editor = comment && <div className="v2-diff-comment" onKeyDownCapture={(event) => {
    if (event.nativeEvent.isComposing || event.nativeEvent.keyCode === 229) {
      // Let the IME cancel its candidate without the modal's native Escape handler closing review.
      if (event.key === "Escape") event.stopPropagation();
      return;
    }
    if (event.key === "Escape") { event.preventDefault(); event.stopPropagation(); closeComment(); }
    if (event.key === "Enter" && (event.ctrlKey || event.metaKey)) {
      event.preventDefault(); event.stopPropagation(); addComment();
    }
  }}>
    {comment.patch !== patch && <p role="status">{t("差异已更新；这条意见仍引用开始输入时的版本。", "The diff changed; this comment still refers to the revision you started with.")}</p>}
    <label htmlFor={inputID}>{comment.label}</label>
    {comment.patch !== patch && <pre>{comment.text}</pre>}
    <textarea id={inputID} autoFocus rows={3} maxLength={4096} value={text}
      onChange={(event) => setText(event.target.value)} placeholder={t("描述希望如何修改这一行…", "Describe the change you want on this line…")} />
    {blocked && <p role="alert">{t("未能加入草稿。请返回聊天草稿检查版本冲突或保存问题；此处输入仍保留。", "Could not add this comment. Check the chat draft for conflicts or storage issues; your input is still here.")}</p>}
    <div><button type="button" disabled={!text.trim() || !onAddComment} onClick={addComment}>{t("加入聊天草稿", "Add to chat draft")}</button>
      <button type="button" onClick={closeComment}>{t("取消", "Cancel")}</button>
      <span>{t("Ctrl / ⌘ + Enter 加入；返回聊天后发送", "Ctrl / ⌘ + Enter to add; send from chat")}</span></div>
  </div>;
  if (!parsed.hunks.length) return <>{editor}<pre className="v2-delivery-patch">{patch}</pre></>;
  return <div ref={container} tabIndex={-1} className={`v2-delivery-diff${wrapLines ? " is-wrapped" : ""}`}>
    {comment && comment.patch !== patch && editor}
    {parsed.hunks.map((hunk, ordinal) => <section key={ordinal} aria-label={t(`差异片段 ${ordinal + 1}`, `Diff hunk ${ordinal + 1}`)}>
      {hunk.preamble && <pre>{hunk.preamble}</pre>}
      <header><code>{hunk.header}</code><div className="v2-diff-hunk-actions">
        {ordinal === 0 && controlledWrapLines === undefined && <DiffWrapButton wrapLines={wrapLines}
          buttonRef={toolbar} onToggle={() => setLocalWrapLines((value) => !value)} />}
        {onFeedback && <button type="button" onClick={() => onFeedback(
        t(`${source}\n${fileContext(hunk, t)}\n差异片段：${hunk.header}\n${hunk.lines.slice(0, 60).map((line) => line.text).join("\n")}${hunk.lines.length > 60 ? t("\n（摘录前60行）", "\n(first 60 lines)") : ""}`, `${source}\n${fileContext(hunk, t)}\nDiff hunk: ${hunk.header}\n${hunk.lines.slice(0, 60).map((line) => line.text).join("\n")}${hunk.lines.length > 60 ? t("\n（摘录前60行）", "\n(first 60 lines)") : ""}`))}>{t("引用此片段", "Quote this excerpt")}</button>}</div></header>
      <div className="v2-delivery-diff-lines">{hunk.lines.map((line, index) => <Fragment key={index}>
        <div className={`v2-diff-line ${line.kind}`}>
          {onFeedback && line.kind !== "meta" ? <button type="button" className="v2-diff-line-ref"
            aria-label={t(`引用${line.kind === "remove" ? "旧" : "新"}第 ${line.newLine ?? line.oldLine} 行`, `Quote ${line.kind === "remove" ? "old" : "new"} line ${line.newLine ?? line.oldLine}`)}
            onClick={() => onFeedback(lineContext(source, hunk, line, t))}>
            <span>{line.oldLine ?? ""}</span><span>{line.newLine ?? ""}</span></button> : <span className="v2-diff-line-ref" />}
          {onAddComment && line.kind !== "meta" && <button type="button" className="v2-diff-comment-trigger" disabled={comment !== null}
            aria-label={t(`评论${line.kind === "remove" ? "旧" : "新"}第 ${line.newLine ?? line.oldLine} 行`, `Comment on ${line.kind === "remove" ? "old" : "new"} line ${line.newLine ?? line.oldLine}`)}
            onClick={(event) => {
              trigger.current = event.currentTarget; setText(""); setBlocked(false);
              setComment({ patch, hunk: ordinal, line: index, context: lineContext(source, hunk, line, t), text: line.text,
                label: t(`${line.kind === "remove" ? "旧" : "新"}第 ${line.newLine ?? line.oldLine} 行的意见`, `Comment on ${line.kind === "remove" ? "old" : "new"} line ${line.newLine ?? line.oldLine}`) });
            }}>+</button>}
          <code>{line.text || " "}</code>
        </div>
        {comment?.patch === patch && comment.hunk === ordinal && comment.line === index && editor}
      </Fragment>)}</div>
    </section>)}
    {parsed.trailing && <pre>{parsed.trailing}</pre>}
  </div>;
}
