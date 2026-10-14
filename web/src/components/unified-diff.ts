export type UnifiedDiffLineKind = "add" | "delete" | "context" | "hunk" | "meta";

export type UnifiedDiffLine = {
  kind: UnifiedDiffLineKind;
  marker: string;
  oldLine?: number;
  newLine?: number;
  text: string;
};

export type UnifiedDiff = {
  additions: number;
  deletions: number;
  lines: UnifiedDiffLine[];
};

const hunkPattern = /^@@ -(\d+)(?:,(\d+))? \+(\d+)(?:,(\d+))? @@(.*)$/;

export function parseUnifiedDiff(value: string): UnifiedDiff {
  let additions = 0;
  let deletions = 0;
  let oldLine: number | undefined;
  let newLine: number | undefined;
  let oldRemaining = 0;
  let newRemaining = 0;
  const lines: UnifiedDiffLine[] = [];

  for (const rawLine of value.replace(/\r\n/g, "\n").split("\n")) {
    // Within the hunk ranges, --- and +++ are removed/added source text.
    if (oldRemaining > 0 || newRemaining > 0) {
      if (rawLine.startsWith("-") && oldRemaining > 0) {
        deletions += 1;
        lines.push({ kind: "delete", marker: "-", oldLine, text: rawLine.slice(1) });
        oldLine! += 1;
        oldRemaining -= 1;
        continue;
      }
      if (rawLine.startsWith("+") && newRemaining > 0) {
        additions += 1;
        lines.push({ kind: "add", marker: "+", newLine, text: rawLine.slice(1) });
        newLine! += 1;
        newRemaining -= 1;
        continue;
      }
      if (rawLine.startsWith(" ") && oldRemaining > 0 && newRemaining > 0) {
        lines.push({ kind: "context", marker: "", oldLine, newLine, text: rawLine.slice(1) });
        oldLine! += 1;
        newLine! += 1;
        oldRemaining -= 1;
        newRemaining -= 1;
        continue;
      }
      if (rawLine.startsWith("\\ No newline")) {
        lines.push({ kind: "meta", marker: "", text: rawLine });
        continue;
      }
    }
    oldRemaining = 0;
    newRemaining = 0;
    oldLine = undefined;
    newLine = undefined;
    const hunk = hunkPattern.exec(rawLine);
    if (hunk) {
      oldLine = Number(hunk[1]);
      oldRemaining = Number(hunk[2] ?? 1);
      newLine = Number(hunk[3]);
      newRemaining = Number(hunk[4] ?? 1);
      lines.push({ kind: "hunk", marker: "@@", text: rawLine });
      continue;
    }
    if (rawLine.startsWith("+++") || rawLine.startsWith("---") ||
      rawLine.startsWith("diff ") || rawLine.startsWith("index ") ||
      rawLine.startsWith("\\ No newline")) {
      lines.push({ kind: "meta", marker: "", text: rawLine });
      continue;
    }
    if (rawLine.startsWith("+")) {
      additions += 1;
      lines.push({ kind: "add", marker: "+", newLine, text: rawLine.slice(1) });
      continue;
    }
    if (rawLine.startsWith("-")) {
      deletions += 1;
      lines.push({ kind: "delete", marker: "-", oldLine, text: rawLine.slice(1) });
      continue;
    }
    if (rawLine.startsWith(" ")) {
      lines.push({ kind: "context", marker: "", oldLine, newLine, text: rawLine.slice(1) });
      continue;
    }
    lines.push({ kind: "meta", marker: "", text: rawLine });
  }

  return { additions, deletions, lines };
}
