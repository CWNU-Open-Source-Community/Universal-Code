import { useEffect, useMemo, useRef, useState } from "react";
import type { APIClient } from "../api/client";
import type { ThreadSavedMessageView, ThreadTranscriptItemView } from "../api/types";
import { localUIMessage } from "../lib/local-ui-message";
import { useLocale, type LocaleText } from "../lib/locale";
import { projectThreadNarrative } from "./projection/narrative";

export type HistoryDirection = "older" | "newer";

// The visible conversation may contain only its newest page. Read the public
// index separately, then fetch only the full body the user chooses to recall.
export async function readPromptHistory(client: Pick<APIClient, "getPage">, threadID: string,
  signal: AbortSignal, t: LocaleText): Promise<string[]> {
  const items = new Map<string, ThreadTranscriptItemView>();
  const cursors = new Set<string>();
  let cursor = "";
  do {
    signal.throwIfAborted();
    if (cursors.has(cursor)) throw new Error(t("输入历史分页重复，请重试。", "Prompt history repeated a page. Try again."));
    cursors.add(cursor);
    const page = await client.getPage<ThreadTranscriptItemView>(`/threads/${encodeURIComponent(threadID)}/transcript`, { limit: 100 }, cursor, signal);
    if (page.page.truncated) throw new Error(t("输入历史未完整读取，请重试。", "Prompt history could not be read in full. Try again."));
    for (const item of page.items) {
      if (item.durable && !item.provisional && item.activity_type === "message" && item.source === "operator" &&
        item.kind === "operator_input" && item.status !== "pending" && item.status !== "cancelled" && item.detail?.trim()) {
        if (!items.has(item.id)) items.set(item.id, item);
      }
    }
    cursor = page.page.next_cursor ?? "";
  } while (cursor);
  signal.throwIfAborted();
  return projectThreadNarrative([...items.values()]).flatMap((entry) => entry.kind === "user" ? [entry.id] : []).reverse();
}

interface HistorySession {
  abort: AbortController;
  original: string;
  expected: string;
  ids?: string[];
  bodies: Map<string, string>;
  index: number;
  loading: boolean;
}

export function usePromptHistory({ client, threadID, scope, value, blocked, apply, atBoundary }: {
  client: APIClient; threadID: string; scope: string; value: string; blocked: boolean;
  apply: (next: string, expected: string) => void;
  atBoundary: (direction: HistoryDirection) => boolean;
}) {
  const { t } = useLocale();
  const owner = useMemo(() => ({ current: null as HistorySession | null }), [client, threadID, scope]);
  const latest = useRef({ owner, value, blocked, apply, atBoundary });
  latest.current = { owner, value, blocked, apply, atBoundary };
  const [notice, setNotice] = useState<{ owner: typeof owner; text: string; error?: boolean } | null>(null);
  const cancel = () => { owner.current?.abort.abort(); owner.current = null; setNotice(null); };
  useEffect(() => () => { owner.current?.abort.abort(); owner.current = null; }, [owner]);
  useEffect(() => {
    if (owner.current && (blocked || value !== owner.current.expected)) cancel();
  }, [owner, blocked, value]);

  const navigate = (direction: HistoryDirection): boolean => {
    if (!threadID || blocked || !atBoundary(direction)) return false;
    let session = owner.current;
    if (session && value !== session.expected) { cancel(); session = null; }
    if (!session) {
      if (direction !== "older" || value !== "") return false;
      session = { abort: new AbortController(), original: value, expected: value, index: -1, loading: false, bodies: new Map() };
      owner.current = session;
    }
    const target = session.index + (direction === "older" ? 1 : -1);
    if (direction === "newer" && (target < 0 || session.loading)) {
      apply(session.original, session.expected); cancel(); return true;
    }
    if (session.loading) return true;
    session.loading = true;
    setNotice({ owner, text: t("正在读取历史输入…", "Reading prompt history…") });
    const current = session;
    const stillCurrent = () => owner.current === current && !current.abort.signal.aborted &&
      latest.current.owner === owner && !latest.current.blocked && latest.current.value === current.expected && latest.current.atBoundary(direction);
    void (async () => {
      current.ids ??= await readPromptHistory(client, threadID, current.abort.signal, t);
      if (!stillCurrent()) { if (owner.current === current) cancel(); return; }
      const id = current.ids[target];
      if (!id) {
        setNotice({ owner, text: current.ids.length ? t("已到最早的输入。", "You have reached the oldest prompt.") : t("暂无可回填的历史输入。", "No previous prompts to recall.") });
        return;
      }
      let text = current.bodies.get(id);
      if (text === undefined) {
        const saved = await client.get<ThreadSavedMessageView>(`/threads/${encodeURIComponent(threadID)}/messages/${encodeURIComponent(id)}`, {}, current.abort.signal);
        if (saved.message_id !== id || typeof saved.content !== "string" || !saved.content.trim()) {
          throw new Error(t("历史输入原文不可用，请重试。", "The original prompt is unavailable. Try again."));
        }
        text = saved.content; current.bodies.set(id, text);
      }
      if (!stillCurrent()) { if (owner.current === current) cancel(); return; }
      latest.current.apply(text, current.expected);
      current.expected = text; current.index = target;
      setNotice({ owner, text: t(`历史输入 ${target + 1} / ${current.ids.length}（仅文字）· 开头 ↑ 更早，末尾 ↓ 返回`,
        `Prompt history ${target + 1} / ${current.ids.length} (text only) · ↑ at start for older, ↓ at end to return`) });
    })().catch((failure: unknown) => {
      if (!stillCurrent()) { if (owner.current === current) cancel(); return; }
      setNotice({ owner, error: true, text: t("历史输入未回填：", "Prompt was not recalled: ") +
        (failure instanceof Error ? localUIMessage(failure.message, t) : t("读取失败，请重试。", "Read failed. Try again.")) });
    }).finally(() => { current.loading = false; });
    return true;
  };
  return { navigate, cancel, notice: notice?.owner === owner ? notice : null };
}
