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
