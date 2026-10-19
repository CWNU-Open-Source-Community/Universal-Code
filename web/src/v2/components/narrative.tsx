import { Children, isValidElement, memo, useMemo, useState } from "react";
import { replaceEqualDeep } from "@tanstack/react-query";
import ReactMarkdown, { defaultUrlTransform, type Components } from "react-markdown";
import remarkGfm from "remark-gfm";
import { Check, Copy } from "lucide-react";
import { remarkCjkAutolinks } from "../../components/remark-cjk-autolinks";
import type { APIClient } from "../../api/client";
import type { NarrativeEntry } from "../projection/narrative";
import { V2ImagePreview } from "./image-input";
import { V2FileAttachments } from "./file-input";
import { V2ActivityGroup } from "./activity-detail";
import { rehypeStreamReveal } from "./stream-reveal";

export interface FileLinkTarget {
  path: string;
  line?: number;
}

export function parseProjectFileLink(href: string): FileLinkTarget | null {
  if (!href) return null;
  // Ignore protocol-relative external URLs
  if (href.startsWith("//")) return null;
  // Ignore external URI schemes like http:, https:, mailto:, ftp://, etc.
  if (/^(https?|ftp|mailto|javascript|data|tel):/i.test(href)) return null;
  if (/^[a-zA-Z][a-zA-Z0-9+.-]*:\/\//.test(href) && !href.startsWith("file://")) return null;
  if (href.startsWith("#") || href.startsWith("?")) return null;

  let raw = href;
  if (raw.startsWith("file:///")) {
    raw = raw.slice(8);
  } else if (raw.startsWith("file://")) {
    raw = raw.slice(7);
  }

  let line: number | undefined;
  const hashMatch = /#L(\d+)/i.exec(raw);
  if (hashMatch) {
    line = parseInt(hashMatch[1], 10);
    raw = raw.slice(0, raw.indexOf("#"));
  } else {
    const colonMatch = /:(\d+)(?::\d+)?$/.exec(raw);
    if (colonMatch) {
      line = parseInt(colonMatch[1], 10);
      raw = raw.slice(0, colonMatch.index);
    }
  }

  raw = raw.replace(/^(\.\/)+/, "").replace(/^\/+/, "");
  if (!raw || raw === ".") return null;

  return { path: raw, line };
}

function extractTextFromNode(node: React.ReactNode): string {
  if (typeof node === "string") return node;
  if (typeof node === "number") return String(node);
  if (!node) return "";
  if (Array.isArray(node)) return node.map(extractTextFromNode).join("");
  if (typeof node === "object" && "props" in node && (node as any).props) {
    return extractTextFromNode((node as any).props.children);
  }
  return "";
}

function MarkdownCodeBlock({ children, ...props }: React.HTMLAttributes<HTMLPreElement>) {
  const [copied, setCopied] = useState<"idle" | "success" | "error">("idle");
  const codeText = useMemo(() => extractTextFromNode(children), [children]);
  const codeElement = Children.toArray(children).find((child) =>
    isValidElement<{ className?: string }>(child) && child.type === "code");
  const language = isValidElement<{ className?: string }>(codeElement)
    ? codeElement.props.className?.match(/(?:^|\s)language-(\S+)/u)?.[1] : undefined;

  const handleCopy = async () => {
    try {
      if (navigator?.clipboard?.writeText) {
        await navigator.clipboard.writeText(codeText);
        setCopied("success");
      } else {
        setCopied("error");
      }
    } catch {
      setCopied("error");
    }
    setTimeout(() => setCopied("idle"), 2000);
  };

  return <div className="v2-markdown-code-block">
    <div className="v2-markdown-code-header">
      <span className="v2-markdown-code-lang">{!language || language === "text" || language === "plaintext" ? "文本" : language}</span>
      <button
        aria-label={copied === "success" ? "已复制代码" : copied === "error" ? "复制失败" : "复制代码"}
        className={`v2-copy-code-button${copied === "success" ? " is-copied" : copied === "error" ? " is-error" : ""}`}
        onClick={handleCopy}
        type="button"
      >
        {copied === "success" ? <Check size={13} aria-hidden="true" /> : <Copy size={13} aria-hidden="true" />}
        <span>{copied === "success" ? "已复制" : copied === "error" ? "复制失败" : "复制"}</span>
      </button>
    </div>
    <pre {...props}>{children}</pre>
  </div>;
}

function AssistantMessageActions({ text }: { text: string }) {
  const [copied, setCopied] = useState<"idle" | "success" | "error">("idle");
  const handleCopy = async () => {
    try {
      if (navigator?.clipboard?.writeText) {
        await navigator.clipboard.writeText(text);
        setCopied("success");
      } else {
        setCopied("error");
      }
    } catch {
      setCopied("error");
    }
    setTimeout(() => setCopied("idle"), 2000);
  };

  return <div className="v2-assistant-actions">
    <button
      aria-label={copied === "success" ? "已复制回复" : copied === "error" ? "复制失败" : "复制回复"}
      className={`v2-assistant-action-btn${copied === "success" ? " is-copied" : copied === "error" ? " is-error" : ""}`}
      onClick={handleCopy}
      type="button"
    >
      {copied === "success" ? <Check size={13} aria-hidden="true" /> : <Copy size={13} aria-hidden="true" />}
      <span>{copied === "success" ? "已复制" : copied === "error" ? "复制失败" : "复制回复"}</span>
    </button>
  </div>;
}

interface NarrativeRowProps {
  client: APIClient;
  entry: NarrativeEntry;
  threadID: string;
  onOpenFile?: (target: {
    path: string;
    line?: number;
    runID?: string;
    triggerElement?: HTMLElement | null;
  }) => void;
}

const StreamRevealSpan: Components["span"] = ({ node, children, ...props }) =>
  <span key={String(node?.properties.dataStreamStart ?? "")} {...props}>{children}</span>;

function MessageMarkdown({ entry, onOpenFile }: {
  entry: Extract<NarrativeEntry, { kind: "user" | "assistant" }>;
  onOpenFile: NarrativeRowProps["onOpenFile"];
}) {
  const live = entry.kind === "assistant" && entry.provisional;
  const [reveal, setReveal] = useState({ text: entry.text, live, start: undefined as number | undefined });
  if (reveal.text !== entry.text || reveal.live !== live) {
    // Only an append to an already visible live message animates. Opening a
    // thread, correcting text and replacing it with durable output stay immediate.
    setReveal({ text: entry.text, live, start: live && reveal.live &&
      entry.text.length > reveal.text.length && entry.text.startsWith(reveal.text)
      ? reveal.text.length : undefined });
  }
  const runID = "runId" in entry ? entry.runId : undefined;
  const components = useMemo<Components>(() => ({
    a: ({ node: _node, href, children, ...props }) => {
      const fileLink = parseProjectFileLink(href ?? "");
      if (fileLink) {
        return <a
          href={href}
          className="v2-file-link"
          title={`在工作区中打开 ${fileLink.path}${fileLink.line ? ` (第 ${fileLink.line} 行)` : ""}`}
          onClick={(event) => {
            event.preventDefault();
            onOpenFile?.({
              path: fileLink.path,
              line: fileLink.line,
              runID,
              triggerElement: event.currentTarget,
            });
          }}
          {...props}
        >{children}</a>;
      }
      return <a href={href} target="_blank" rel="noreferrer noopener" {...props}>{children}</a>;
    },
    pre: ({ node: _node, children, ...props }) => <MarkdownCodeBlock {...props}>{children}</MarkdownCodeBlock>,
    span: StreamRevealSpan,
  }), [onOpenFile, runID]);
  return <ReactMarkdown
    remarkPlugins={[remarkGfm, remarkCjkAutolinks]}
    rehypePlugins={reveal.start === undefined ? [] : [[rehypeStreamReveal, { start: reveal.start }]]}
    urlTransform={(url) => (parseProjectFileLink(url) ? url : defaultUrlTransform(url))}
    components={components}
  >{entry.text}</ReactMarkdown>;
}

const NarrativeRow = memo(function NarrativeRow({ client, entry, threadID, onOpenFile }: NarrativeRowProps) {
  if (entry.kind === "user" && entry.status === "cancelled" && entry.promotedToMessageID) return <li className="v2-user-turn">
    <details className="v2-promoted-message-history">
      <summary title={entry.text}>排队消息已转为引导</summary>
      <div className="v2-promoted-message-content">{entry.text}<V2ImagePreview client={client} images={entry.images ?? []} />
        <V2FileAttachments client={client} attachments={entry.attachments ?? []} /></div>
    </details></li>;
  if (entry.kind === "user") return <li className="v2-user-turn">
    <div><div className="v2-user-markdown"><MessageMarkdown entry={entry} onOpenFile={onOpenFile} /></div><V2ImagePreview client={client} images={entry.images ?? []} />
      <V2FileAttachments client={client} attachments={entry.attachments ?? []} />{entry.status === "cancelled" &&
      <small className="v2-message-status">已取消，不会继续处理</small>}
      {entry.status === "pending" && <small className="v2-message-status">已接收</small>}
      {entry.deliveryMode === "steer" && entry.status !== "cancelled" && entry.status !== "pending" &&
        <small className="v2-message-status">已加入当前任务</small>}
      {entry.provisional && !entry.status && <small className="v2-message-status">正在发送…</small>}</div></li>;
  if (entry.kind === "assistant") return <li aria-live={entry.provisional ? "polite" : undefined}
    className={`v2-assistant-turn${entry.provisional ? " is-provisional" : ""}`}>
    <MessageMarkdown entry={entry} onOpenFile={onOpenFile} />
    {!entry.provisional && <AssistantMessageActions text={entry.text} />}
  </li>;
  if (entry.kind === "activity") return <li className="v2-activity-turn">
    <V2ActivityGroup client={client} entry={entry} threadID={threadID} /></li>;
  return <li className={`v2-notice tone-${entry.tone}`}>{entry.text}</li>;
}, (previous, next) => previous.client === next.client && previous.threadID === next.threadID &&
  previous.onOpenFile === next.onOpenFile &&
  // Live updates reuse durable entries by reference. A durable refresh can
  // rebuild a group: compare its complete public projection before skipping it.
  replaceEqualDeep(previous.entry, next.entry) === previous.entry);

export const V2Narrative = memo(function V2Narrative({ client, entries, threadID, onOpenFile }: {
  client: APIClient;
  entries: NarrativeEntry[];
  threadID: string;
  onOpenFile?: (target: {
    path: string;
    line?: number;
    runID?: string;
    triggerElement?: HTMLElement | null;
  }) => void;
}) {
  return <ol className="v2-narrative">{entries.map((entry) =>
    <NarrativeRow client={client} entry={entry} key={entry.id} threadID={threadID} onOpenFile={onOpenFile} />)}</ol>;
});
