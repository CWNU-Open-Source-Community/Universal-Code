import type { APIClient } from "../api/client";
import type { ThreadSavedMessageView, ThreadTranscriptItemView } from "../api/types";
import type { LocaleText } from "../lib/locale";
import { projectThreadNarrative, type NarrativeEntry } from "./projection/narrative";

export type ExportMessage = Extract<NarrativeEntry, { kind: "user" | "assistant" }>;
export type ThreadExportFormat = "markdown" | "text";

// Read independently of the visible transcript window: older pages and full
// saved bodies must all succeed before the caller receives anything to save.
export async function readThreadExport(client: Pick<APIClient, "get" | "getPage">, threadID: string,
  signal: AbortSignal, t: LocaleText): Promise<ExportMessage[]> {
  const path = `/threads/${encodeURIComponent(threadID)}`;
  const items = new Map<string, ThreadTranscriptItemView>();
  const cursors = new Set<string>();
  let cursor = "";
  do {
    signal.throwIfAborted();
    if (cursors.has(cursor)) throw new Error(t("聊天历史分页重复，请重新导出。", "Conversation history repeated a page. Try exporting again."));
    cursors.add(cursor);
    const page = await client.getPage<ThreadTranscriptItemView>(`${path}/transcript`, { limit: 100 }, cursor, signal);
    if (page.page.truncated) throw new Error(t("聊天历史未完整读取，请重新导出。", "Conversation history could not be read in full. Try exporting again."));
    for (const item of page.items) {
      if (item.durable && !item.provisional && item.activity_type === "message" &&
        (item.source === "operator" && item.kind === "operator_input" || item.source === "model" && item.kind === "model_update")) {
        if (!items.has(item.id)) items.set(item.id, item);
      }
    }
    cursor = page.page.next_cursor ?? "";
  } while (cursor);

  const ordered = [...items.values()].sort((left, right) => left.run_ordinal - right.run_ordinal ||
    left.sequence - right.sequence || (left.position ?? 0) - (right.position ?? 0));
  // Use the conversation's existing visibility rules for synthetic inputs and
  // internal model updates, then resolve every contributing original message.
  const visible = new Set(projectThreadNarrative(ordered).flatMap((entry) =>
    entry.kind === "user" ? [entry.id] : entry.kind === "assistant" ? entry.messageIDs ?? [entry.id] : []));
  const messages = ordered.filter((item) => visible.has(item.id));
  const complete: ThreadTranscriptItemView[] = [];
  for (let offset = 0; offset < messages.length; offset += 4) {
    signal.throwIfAborted();
    complete.push(...await Promise.all(messages.slice(offset, offset + 4).map(async (item) => {
      const saved = await client.get<ThreadSavedMessageView>(`${path}/messages/${encodeURIComponent(item.id)}`, {}, signal);
      if (saved.message_id !== item.id || typeof saved.content !== "string") {
        throw new Error(t("完整消息身份不匹配，请重新导出。", "A saved message did not match the conversation. Try exporting again."));
      }
      return { ...item, detail: saved.content };
    })));
  }
  signal.throwIfAborted();
  return projectThreadNarrative(complete).filter((entry): entry is ExportMessage =>
    entry.kind === "user" || entry.kind === "assistant");
}

export function formatThreadExport(title: string, messages: ExportMessage[], format: ThreadExportFormat, t: LocaleText): string {
  const heading = title.replace(/[\r\n]+/gu, " ").trim() || t("对话", "Conversation");
  const sections = messages.map((message) => {
    const role = message.kind === "user" ? t("用户", "User") : t("助手", "Assistant");
    const status = message.kind === "user" && message.status === "cancelled" ? t("（已撤回）", " (withdrawn)")
      : message.kind === "user" && message.status === "pending" ? t("（待处理）", " (pending)") : "";
    const attachments = message.kind === "user" ? [
      ...(message.images ?? []).map((image) => image.name || t("图片", "Image")),
      ...(message.attachments ?? []).map((file) => file.name),
    ] : [];
    const files = attachments.length ? `\n\n${t("附件：", "Attachments: ")}${attachments.join(", ")}` : "";
    return `${format === "markdown" ? "## " : ""}${role}${status}\n\n${message.text}${files}`;
  });
  return `${format === "markdown" ? "# " : ""}${heading}\n\n${sections.join("\n\n---\n\n")}\n`;
}

export function threadExportFilename(title: string, format: ThreadExportFormat): string {
  const name = title.replace(/[\\/:*?"<>|\u0000-\u001f]/gu, "_").trim().replace(/[. ]+$/u, "").slice(0, 100);
  return `conversation-${name || "export"}.${format === "markdown" ? "md" : "txt"}`;
}
