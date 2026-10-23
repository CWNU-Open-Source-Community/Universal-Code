import { Children, isValidElement, memo, useEffect, useMemo, useRef, useState } from "react";
import { replaceEqualDeep } from "@tanstack/react-query";
import ReactMarkdown, { defaultUrlTransform, type Components } from "react-markdown";
import remarkGfm from "remark-gfm";
import { Check, Copy, LoaderCircle, Split, Pencil, Quote } from "lucide-react";
import { remarkCjkAutolinks } from "../../components/remark-cjk-autolinks";
import type { APIClient } from "../../api/client";
import type { ThreadSavedMessageView } from "../../api/types";
import type { NarrativeEntry } from "../projection/narrative";
import { useLocale } from "../../lib/locale";
import { V2ImagePreview } from "./image-input";
import { V2FileAttachments } from "./file-input";
import { V2ActivityGroup } from "./activity-detail";
import { rehypeStreamReveal } from "./stream-reveal";
import { V2TurnChanges } from "./turn-changes";
import { V2PeerMessageCard } from "./thread-peer-messages";
import { ChatLinkErrors, ChatResourceContext, ResourceImage, ResourceLink } from "./resource-link";
import { remarkResourceLinks } from "./remark-resource-links";
import { parseProjectFileLink } from "../../lib/chat-resources";
export { parseProjectFileLink, type FileLinkTarget } from "../../lib/chat-resources";

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
  const { t } = useLocale();
  const [copied, setCopied] = useState<"idle" | "success" | "error">("idle");
  const codeText = useMemo(() => extractTextFromNode(children), [children]);
  const codeElement = Children.toArray(children).find((child) =>
    isValidElement<{ className?: string }>(child) && child.type === "code");
  const language = isValidElement<{ className?: string }>(codeElement)
    ? codeElement.props.className?.match(/(?:^|\s)language-(\S+)/u)?.[1] : undefined;
  const [highlighter, setHighlighter] = useState<typeof import("./syntax-highlight").highlightCode>();
  useEffect(() => {
    if (!language || language === "text" || language === "plaintext") return;
    let active = true;
    void import("./syntax-highlight").then((module) => { if (active) setHighlighter(() => module.highlightCode); })
      .catch(() => { /* The original selectable code stays available if the optional chunk cannot load. */ });
    return () => { active = false; };
  }, [language]);
  const highlighted = useMemo(() => language && highlighter?.(codeText, language.toLowerCase()), [codeText, language, highlighter]);

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
      <span className="v2-markdown-code-lang">{!language || language === "text" || language === "plaintext" ? t("文本", "Text") : language}</span>
      <button
        aria-label={copied === "success" ? t("已复制代码", "Code copied") : copied === "error" ? t("复制失败", "Copy failed") : t("复制代码", "Copy code")}
        className={`v2-copy-code-button${copied === "success" ? " is-copied" : copied === "error" ? " is-error" : ""}`}
        onClick={handleCopy}
        type="button"
      >
        {copied === "success" ? <Check size={13} aria-hidden="true" /> : <Copy size={13} aria-hidden="true" />}
        <span>{copied === "success" ? t("已复制", "Copied") : copied === "error" ? t("复制失败", "Copy failed") : t("复制", "Copy")}</span>
      </button>
    </div>
    <pre {...props}>{highlighted === undefined ? children : <code className={`language-${language} hljs`} dangerouslySetInnerHTML={{ __html: highlighted }} />}</pre>
  </div>;
}

type BranchReply = (sourceEventID: string, operationKey: string) => Promise<void>;

const messageTimeFormats = Object.fromEntries((["zh-CN", "en-US"] as const).map((locale) => [locale, {
  short: new Intl.DateTimeFormat(locale, { hour: "2-digit", minute: "2-digit", hourCycle: "h23" }),
  full: new Intl.DateTimeFormat(locale, { dateStyle: "medium", timeStyle: "long" }),
}]));

function MessageTime({ createdAt }: { createdAt: string }) {
  const { locale, t } = useLocale();
  if (!createdAt) return null;
  const date = new Date(createdAt);
  if (!Number.isFinite(date.getTime())) return null;
  const formats = messageTimeFormats[locale];
  const full = formats.full.format(date);
  return <time className="v2-message-time" dateTime={date.toISOString()} title={full}
    aria-label={t("消息时间：", "Message time: ") + full}>{formats.short.format(date)}</time>;
}

function AssistantMessageActions({ text, createdAt, branchSourceID, onBranch, user = false, onCompose, loadText }: {
  text: string;
  createdAt: string;
  branchSourceID?: string;
  onBranch?: BranchReply;
  user?: boolean;
  onCompose?: (text: string) => void;
  loadText?: () => Promise<string>;
}) {
  const { t } = useLocale();
  const [copied, setCopied] = useState<"idle" | "success" | "error">("idle");
  const [branching, setBranching] = useState(false);
  const [branchError, setBranchError] = useState("");
  const [loading, setLoading] = useState(false);
  const mounted = useRef(true);
  useEffect(() => { mounted.current = true; return () => { mounted.current = false; }; }, []);
  const branchIntent = useRef<{ sourceEventID: string; key: string } | null>(null);
  const handleCopy = async () => {
    if (loading) return;
    setLoading(true); setBranchError("");
    try {
      if (navigator?.clipboard?.writeText) {
        const fullText = loadText ? await loadText() : text;
        if (!mounted.current) return;
        await navigator.clipboard.writeText(fullText);
        setCopied("success");
      } else {
        setCopied("error");
      }
    } catch (error) {
      setCopied("error");
      if (loadText) setBranchError(error instanceof Error ? error.message : t("无法读取完整消息，请重试。", "Could not read the full message. Try again."));
    } finally {
      setLoading(false);
    }
    setTimeout(() => setCopied("idle"), 2000);
  };
  const compose = async (quote: boolean, selected?: string) => {
    if (!onCompose || loading) return;
    setLoading(true); setBranchError("");
    try {
      const content = selected ?? (loadText ? await loadText() : text);
      if (mounted.current) onCompose(quote ? content.split("\n").map((line) => `> ${line}`).join("\n") + "\n\n" : content);
    } catch (error) {
      setBranchError(error instanceof Error ? error.message : t("无法读取完整消息，请重试。", "Could not read the full message. Try again."));
    } finally { setLoading(false); }
  };
  const handleBranch = async () => {
    if (!onBranch || !branchSourceID || branching) return;
    if (branchIntent.current?.sourceEventID !== branchSourceID) {
      branchIntent.current = { sourceEventID: branchSourceID, key: `thread-branch-${crypto.randomUUID()}` };
    }
    setBranching(true);
    setBranchError("");
    try {
      await onBranch(branchSourceID, branchIntent.current.key);
      branchIntent.current = null;
    } catch (error) {
      setBranchError(error instanceof Error ? error.message : t("无法创建新聊天，请重试。", "Could not create a new chat. Try again."));
    } finally {
      setBranching(false);
    }
  };
  const copyLabel = copied === "success" ? user ? t("已复制消息", "Message copied") : t("已复制回复", "Response copied")
    : copied === "error" ? t("复制失败", "Copy failed") : user ? t("复制消息", "Copy message") : t("复制回复", "Copy response");
  const branchLabel = branching ? t("正在创建新聊天…", "Creating new chat…") : t("分支到新聊天", "Branch into new chat");

  return <><div className={`v2-assistant-actions${user ? " v2-user-actions" : ""}`}>
    <button
      aria-label={copyLabel}
      className={`v2-assistant-action-btn${copied === "success" ? " is-copied" : copied === "error" ? " is-error" : ""}`}
      onClick={handleCopy}
      disabled={!text || loading}
      type="button"
    >
      {copied === "success" ? <Check size={16} aria-hidden="true" /> : <Copy size={16} aria-hidden="true" />}
      <span className="v2-assistant-action-tooltip" aria-hidden="true">{copyLabel}</span>
    </button>
    {user && onCompose && <button className="v2-assistant-action-btn" type="button" disabled={!text || loading}
      aria-label={t("回填文字到输入框", "Reuse text in composer")} onClick={() => void compose(false)}>
      <Pencil size={16} aria-hidden="true" /><span className="v2-assistant-action-tooltip" aria-hidden="true">{t("回填文字到输入框", "Reuse text in composer")}</span>
    </button>}
    {onCompose && <button className="v2-assistant-action-btn" type="button" disabled={!text || loading}
      aria-label={t("引用消息", "Quote message")} onMouseDown={(event) => event.preventDefault()}
      onClick={(event) => {
        const selection = window.getSelection();
        const row = event.currentTarget.closest("li");
        const quote = selection && !selection.isCollapsed && selection.anchorNode && selection.focusNode &&
          row?.contains(selection.anchorNode) && row.contains(selection.focusNode) ? selection.toString() : undefined;
        void compose(true, quote);
      }}><Quote size={16} aria-hidden="true" /><span className="v2-assistant-action-tooltip" aria-hidden="true">{t("引用选中文字或整条消息", "Quote selected text or the whole message")}</span></button>}
    <button aria-label={branchLabel} aria-busy={branching || undefined}
      className="v2-assistant-action-btn" disabled={!branchSourceID || !onBranch || branching}
      onClick={() => void handleBranch()} type="button">
      {branching ? <LoaderCircle size={16} className="spin" aria-hidden="true" /> : <Split size={16} aria-hidden="true" />}
      <span className="v2-assistant-action-tooltip" aria-hidden="true">{branchLabel}</span>
    </button>
    <MessageTime createdAt={createdAt} />
  </div>{branchError && <p className="v2-assistant-action-error" role="alert">{branchError}</p>}</>;
}

interface NarrativeRowProps {
  client: APIClient;
  entry: NarrativeEntry;
  threadID: string;
  onBranch?: BranchReply;
  onCompose?: (text: string) => void;
  onOpenThread?: (id: string) => void;
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
    a: ({ href, children }) => <ResourceLink href={href} runID={runID} onOpenFile={onOpenFile}>{children}</ResourceLink>,
    img: ({ src, alt }) => <ResourceImage src={typeof src === "string" ? src : undefined} alt={alt} runID={runID} onOpenFile={onOpenFile} />,
    pre: ({ node: _node, children, ...props }) => <MarkdownCodeBlock {...props}>{children}</MarkdownCodeBlock>,
    span: StreamRevealSpan,
  }), [onOpenFile, runID]);
  return <ReactMarkdown
    remarkPlugins={[remarkGfm, remarkCjkAutolinks, remarkResourceLinks]}
    rehypePlugins={reveal.start === undefined ? [] : [[rehypeStreamReveal, { start: reveal.start }]]}
    urlTransform={(url) => (parseProjectFileLink(url) ? url : defaultUrlTransform(url))}
    components={components}
  >{entry.text}</ReactMarkdown>;
}

const NarrativeRow = memo(function NarrativeRow({ client, entry, threadID, onOpenFile, onBranch, onOpenThread, onCompose }: NarrativeRowProps) {
  const { t } = useLocale();
  if (entry.kind === "peer") return <li className="v2-peer-turn"><V2PeerMessageCard message={entry.message}
    threadID={threadID} onOpenThread={onOpenThread} /></li>;
  if (entry.kind === "user" && entry.status === "cancelled" && entry.promotedToMessageID) return <li className="v2-user-turn" data-message-ids={entry.id} tabIndex={-1}>
    <details className="v2-promoted-message-history">
      <summary title={entry.text}>{t("排队消息已转为引导", "Queued message sent as guidance")}</summary>
      <div className="v2-promoted-message-content">{entry.text}<V2ImagePreview client={client} images={entry.images ?? []} />
        <V2FileAttachments client={client} attachments={entry.attachments ?? []} /></div>
    </details></li>;
  if (entry.kind === "user") return <li className="v2-user-turn" data-message-ids={entry.id} tabIndex={-1}>
    <div><div className="v2-user-markdown"><MessageMarkdown entry={entry} onOpenFile={onOpenFile} /></div><V2ImagePreview client={client} images={entry.images ?? []} />
      <V2FileAttachments client={client} attachments={entry.attachments ?? []} />{entry.status === "cancelled" &&
      <small className="v2-message-status">{t("已取消，不会继续处理", "Cancelled; will not be processed")}</small>}
      {entry.status === "pending" && <small className="v2-message-status">{t("已接收", "Received")}</small>}
      {entry.deliveryMode === "steer" && entry.status !== "cancelled" && entry.status !== "pending" &&
        <small className="v2-message-status">{t("已加入当前任务", "Added to current task")}</small>}
      {entry.provisional && !entry.status && <small className="v2-message-status">{t("正在发送…", "Sending…")}</small>}</div>
    {!entry.provisional && entry.status !== "pending" && <AssistantMessageActions user text={entry.text} createdAt={entry.createdAt}
      branchSourceID={entry.branchSourceID} onBranch={onBranch} onCompose={onCompose} loadText={async () => {
        const saved = await client.get<ThreadSavedMessageView>(`/threads/${encodeURIComponent(threadID)}/messages/${encodeURIComponent(entry.id)}`);
        if (saved.message_id !== entry.id || typeof saved.content !== "string") throw new Error(t("完整消息身份不匹配，请重新读取。", "The saved message identity did not match. Reload and try again."));
        return saved.content;
      }} />}</li>;
  if (entry.kind === "assistant") return <li aria-live={entry.provisional ? "polite" : undefined}
    data-message-ids={(entry.messageIDs ?? [entry.id]).join(" ")} tabIndex={-1}
    className={`v2-assistant-turn${entry.provisional ? " is-provisional" : ""}`}>
    <MessageMarkdown entry={entry} onOpenFile={onOpenFile} />
    {!entry.provisional && entry.changeSourceID && entry.runId && typeof client.threadChanges === "function" &&
      <V2TurnChanges client={client} threadID={threadID} runID={entry.runId} sourceEventID={entry.changeSourceID} />}
    {!entry.provisional && <AssistantMessageActions text={entry.text} createdAt={entry.createdAt} branchSourceID={entry.branchSourceID} onBranch={onBranch} onCompose={onCompose} />}
  </li>;
  if (entry.kind === "activity") return <li className="v2-activity-turn">
    <V2ActivityGroup client={client} entry={entry} threadID={threadID} /></li>;
  return <li className={`v2-notice tone-${entry.tone}`}>{entry.text}</li>;
}, (previous, next) => previous.client === next.client && previous.threadID === next.threadID &&
  previous.onOpenFile === next.onOpenFile &&
  previous.onBranch === next.onBranch &&
  previous.onCompose === next.onCompose &&
  previous.onOpenThread === next.onOpenThread &&
  // Live updates reuse durable entries by reference. A durable refresh can
  // rebuild a group: compare its complete public projection before skipping it.
  replaceEqualDeep(previous.entry, next.entry) === previous.entry);

export const V2Narrative = memo(function V2Narrative({ client, entries, threadID, workspaceID = "", onOpenFile, onBranch, onOpenThread, onCompose }: {
  client: APIClient;
  entries: NarrativeEntry[];
  threadID: string;
  workspaceID?: string;
  onBranch?: BranchReply;
  onCompose?: (text: string) => void;
  onOpenThread?: (id: string) => void;
  onOpenFile?: (target: {
    path: string;
    line?: number;
    runID?: string;
    triggerElement?: HTMLElement | null;
  }) => void;
}) {
  const resources = useMemo(() => ({ workspaceID }), [workspaceID]);
  return <ChatResourceContext.Provider value={resources}><ChatLinkErrors /><ol className="v2-narrative">{entries.map((entry) =>
    <NarrativeRow client={client} entry={entry} key={entry.id} threadID={threadID} onOpenFile={onOpenFile} onBranch={onBranch}
      onOpenThread={onOpenThread} onCompose={onCompose} />)}</ol></ChatResourceContext.Provider>;
});
