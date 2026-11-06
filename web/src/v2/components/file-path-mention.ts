import { MarkdownManager } from "@tiptap/markdown";
import { validWorkspaceRelativePath } from "../../api/client/validation";

export interface FilePathMention {
  from: number;
  to: number;
  parent: string;
  prefix: string;
}

// This is a path token, not a Markdown extension or an attachment parser.
// Only an explicit selection can add a file reference to the draft.
export function filePathMention(text: string, caret: number): FilePathMention | null {
  const before = text.slice(0, caret);
  const matches = [...before.matchAll(/(?:^|\s)@/gu)];
  const match = matches.at(-1);
  if (!match) return null;
  const from = match.index! + match[0].length - 1;
  const source = before.slice(from + 1);
  let path = source;
  let to = caret;
  if (source.startsWith('"')) {
    // Completing a prefix inside an existing path would duplicate its suffix.
    if (text[caret] && text[caret] !== '"') return null;
    // JSON string escaping also preserves quotes in Unix file names.
    try { path = JSON.parse(`${source}"`) as string; } catch { return null; }
    if (text[caret] === '"') to++;
  } else if (/[\s"\\]/u.test(source) || (text[caret] && !/\s/u.test(text[caret]))) return null;
  if (Array.from(path).length > 512 || /[\\:\u0000-\u001f\u007f]/u.test(path)) return null;
  const slash = path.lastIndexOf("/");
  const parent = slash < 0 ? "." : path.slice(0, slash);
  const prefix = path.slice(slash + 1);
  if (!validWorkspaceRelativePath(parent) || (slash >= 0 && parent === ".")) return null;
  return { from, to, parent, prefix };
}

let markdown: MarkdownManager | undefined;
export function plainFilePathMention(text: string, caret: number): FilePathMention | null {
  const mention = filePathMention(text, caret);
  if (!mention) return null;
  // Reuse the editor's Markdown lexer for fenced/indented code, inline code,
  // links and HTML, including nested lists. The marker locates this caret only.
  let marker = "UCFILEPATHCARET";
  while (text.includes(marker)) marker += "X";
  const markedText = text.slice(0, mention.from) + marker + text.slice(mention.from);
  markdown ??= new MarkdownManager();
  let protectedText = false;
  markdown.instance.walkTokens(markdown.instance.lexer(markedText), (token) => {
    if (!token.raw?.includes(marker)) return;
    if (["code", "codespan", "link", "image", "html"].includes(token.type) ||
      // An unfinished inline-code delimiter is still literal in the lexer.
      (token.type === "text" && token.raw.slice(0, token.raw.indexOf(marker)).includes("`"))) protectedText = true;
  });
  return protectedText ? null : mention;
}

export function completedFilePath(path: string, directory: boolean): { text: string; caret: number } {
  const value = path + (directory ? "/" : "");
  const quoted = /[\s"]/u.test(value);
  const text = `@${quoted ? JSON.stringify(value) : value}${directory ? "" : " "}`;
  return { text, caret: text.length - (directory && quoted ? 1 : 0) };
}
