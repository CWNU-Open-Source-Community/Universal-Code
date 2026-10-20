import { useLocale, type LocaleText } from "../../lib/locale";
export interface ReviewDiffLine { text: string; oldLine?: number; newLine?: number; kind: "context" | "add" | "remove" | "meta" }
export interface ReviewDiffHunk { header: string; lines: ReviewDiffLine[]; oldPath?: string; newPath?: string; preamble: string }

export function parseReviewDiff(patch: string): { preamble: string; hunks: ReviewDiffHunk[]; trailing: string } {
  let outside: string[] = [];
  const hunks: ReviewDiffHunk[] = [];
  let hunk: ReviewDiffHunk | undefined, oldLine = 0, newLine = 0, oldRemaining = 0, newRemaining = 0;
  let oldPath: string | undefined, newPath: string | undefined;
  for (const text of patch.replace(/\r\n/gu, "\n").split("\n")) {
    // Inside the declared ranges, ---/+++ are ordinary removed/added source lines.
    if (hunk && (oldRemaining > 0 || newRemaining > 0)) {
      if (text.startsWith("-") && oldRemaining > 0) {
        hunk.lines.push({ text, kind: "remove", oldLine: oldLine++ }); oldRemaining--; continue;
      }
      if (text.startsWith("+") && newRemaining > 0) {
        hunk.lines.push({ text, kind: "add", newLine: newLine++ }); newRemaining--; continue;
      }
      if (text.startsWith(" ") && oldRemaining > 0 && newRemaining > 0) {
        hunk.lines.push({ text, kind: "context", oldLine: oldLine++, newLine: newLine++ }); oldRemaining--; newRemaining--; continue;
      }
    }
    if (hunk && outside.length === 0 && text.startsWith("\\ No newline at end of file")) {
      hunk.lines.push({ text, kind: "meta" }); continue;
    }
    // Completed or interrupted hunks cannot lend line numbers to later file metadata.
    oldRemaining = 0; newRemaining = 0;
    const match = /^@@ -(\d+)(?:,(\d+))? \+(\d+)(?:,(\d+))? @@(?:.*)$/u.exec(text);
    if (match) {
      const oldStart = Number(match[1]), oldCount = Number(match[2] ?? 1);
      const newStart = Number(match[3]), newCount = Number(match[4] ?? 1);
      if ([oldStart, oldCount, newStart, newCount, oldStart + oldCount, newStart + newCount].every(Number.isSafeInteger) &&
        (oldStart > 0 || oldCount === 0) && (newStart > 0 || newCount === 0)) {
        oldLine = oldStart; newLine = newStart; oldRemaining = oldCount; newRemaining = newCount;
        hunk = { header: text, lines: [], oldPath, newPath, preamble: outside.join("\n") };
        outside = []; hunks.push(hunk); continue;
      }
    }
    if (text.startsWith("diff ")) { oldPath = undefined; newPath = undefined; }
    else if (text.startsWith("--- ")) { oldPath = text.slice(4) || undefined; newPath = undefined; }
    else if (text.startsWith("+++ ")) {
      if (!outside.at(-1)?.startsWith("--- ")) oldPath = undefined;
      newPath = text.slice(4) || undefined;
    }
    outside.push(text);
  }
  return { preamble: hunks[0]?.preamble ?? outside.join("\n"), hunks, trailing: hunks.length ? outside.join("\n") : "" };
}

function fileContext(hunk: ReviewDiffHunk, t: LocaleText): string {
  return t(`旧文件路径：${hunk.oldPath ?? t("未提供", "Not provided")}\n新文件路径：${hunk.newPath ?? t("未提供", "Not provided")}`, `Old file path: ${hunk.oldPath ?? t("未提供", "Not provided")}\nNew file path: ${hunk.newPath ?? t("未提供", "Not provided")}`);
}

export function ReviewDiff({ patch, source, onFeedback }: {
  patch: string; source: string; onFeedback?: (context: string) => void;
}) {
  const { t } = useLocale();
  const parsed = parseReviewDiff(patch);
  if (!parsed.hunks.length) return <pre className="v2-delivery-patch">{patch}</pre>;
  return <div className="v2-delivery-diff">
    {parsed.hunks.map((hunk, ordinal) => <section key={ordinal} aria-label={t(`差异片段 ${ordinal + 1}`, `Diff hunk ${ordinal + 1}`)}>
      {hunk.preamble && <pre>{hunk.preamble}</pre>}
      <header><code>{hunk.header}</code>{onFeedback && <button type="button" onClick={() => onFeedback(
        t(`${source}\n${fileContext(hunk, t)}\n差异片段：${hunk.header}\n${hunk.lines.slice(0, 60).map((line) => line.text).join("\n")}${hunk.lines.length > 60 ? t("\n（摘录前60行）", "\n(first 60 lines)") : ""}`, `${source}\n${fileContext(hunk, t)}\nDiff hunk: ${hunk.header}\n${hunk.lines.slice(0, 60).map((line) => line.text).join("\n")}${hunk.lines.length > 60 ? t("\n（摘录前60行）", "\n(first 60 lines)") : ""}`))}>{t("引用此片段", "Quote this excerpt")}</button>}</header>
      <div className="v2-delivery-diff-lines">{hunk.lines.map((line, index) => <div className={`v2-diff-line ${line.kind}`} key={index}>
        {onFeedback && line.kind !== "meta" ? <button type="button" className="v2-diff-line-ref"
          aria-label={t(`引用${line.kind === "remove" ? t("旧", "Old") : t("新", "New")}第 ${line.newLine ?? line.oldLine} 行`, `Quote ${line.kind === "remove" ? "old" : "new"} line ${line.newLine ?? line.oldLine}`)}
          onClick={() => onFeedback(t(`${source}\n${fileContext(hunk, t)}\n差异片段：${hunk.header}\n${line.kind === "remove" ? t("旧文件", "Old file") : t("新文件", "New file")}第 ${line.newLine ?? line.oldLine} 行：\n${line.text}\n行号只对应上述差异版本，请先核对当前内容。`, `${source}\n${fileContext(hunk, t)}\nDiff hunk: ${hunk.header}\n${line.kind === "remove" ? "Old file" : "New file"}, line ${line.newLine ?? line.oldLine}:\n${line.text}\nLine numbers refer only to this diff revision. Check current content first.`))}>
          <span>{line.oldLine ?? ""}</span><span>{line.newLine ?? ""}</span></button> : <span className="v2-diff-line-ref" />}
        <code>{line.text || " "}</code>
      </div>)}</div>
    </section>)}
    {parsed.trailing && <pre>{parsed.trailing}</pre>}
  </div>;
}
