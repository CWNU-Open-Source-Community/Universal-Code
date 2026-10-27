import { useCallback, useEffect, useLayoutEffect, useRef, useState, type RefObject } from "react";
import type { V2ReplyReading } from "./use-thread-unread";

type ReadingOptions = {
  threadID: string;
  request: number;
  readable: boolean;
  ready: boolean;
  content: unknown;
  scroll: RefObject<HTMLDivElement | null>;
  following: () => boolean;
  refresh: (options: { refreshMutable: boolean; throwOnError: boolean }) => Promise<void>;
  reading?: V2ReplyReading;
};

function atLatest(options: ReadingOptions) {
  const element = options.scroll.current;
  return Boolean(options.reading && options.readable && options.ready && options.following() &&
    document.visibilityState === "visible" && document.hasFocus() && element && element.clientHeight > 0 &&
    !element.closest("[inert]") && element.scrollHeight - element.clientHeight - element.scrollTop <= 48);
}

export function useV2ReplyReading(options: ReadingOptions) {
  const current = useRef(options);
  current.current = options;
  const generation = useRef(0);
  const wasFollowing = useRef(false);
  const [confirmation, setConfirmation] = useState<{ generation: number; threadID: string; ids: string[] } | null>(null);
  const observe = useCallback(() => {
    const value = current.current;
    if (!atLatest(value)) return;
    const element = value.scroll.current!;
    const viewport = element.getBoundingClientRect();
    const ids = [...element.querySelectorAll<HTMLElement>(".v2-assistant-turn:not(.is-provisional)[data-message-ids]")]
      .filter((row) => { const bounds = row.getBoundingClientRect(); return bounds.bottom > viewport.top && bounds.top < viewport.bottom; })
      .flatMap((row) => row.dataset.messageIds?.split(" ").filter(Boolean) ?? []);
    value.reading!.observe(value.threadID, [...new Set(ids)]);
  }, []);
  const enterLatest = useCallback(async () => {
    const token = ++generation.current;
    const value = current.current;
    wasFollowing.current = atLatest(value);
    if (!wasFollowing.current) return;
    observe();
    const stillReading = () => token === generation.current && current.current.threadID === value.threadID && atLatest(current.current);
    try {
      const ids = await value.reading!.prepare(value.threadID);
      if (!ids?.length || !stillReading()) return;
      // refreshMutable also requests a subsequent head read when an older
      // request is already in flight. Its completion cannot acknowledge this
      // captured feed range until that fresh read has succeeded.
      await value.refresh({ refreshMutable: true, throwOnError: true });
      if (stillReading()) setConfirmation({ generation: token, threadID: value.threadID, ids });
    } catch { /* The transcript/feed already exposes failures; retain unread identities. */ }
  }, [observe]);
  useEffect(() => {
    void enterLatest();
    return () => { generation.current++; };
  }, [options.threadID, options.request, options.readable, options.ready, enterLatest]);
  useEffect(() => {
    const focus = () => { void enterLatest(); };
    const blur = () => { generation.current++; wasFollowing.current = false; };
    const visibility = () => document.visibilityState === "visible" ? focus() : blur();
    window.addEventListener("focus", focus);
    window.addEventListener("blur", blur);
    document.addEventListener("visibilitychange", visibility);
    return () => {
      window.removeEventListener("focus", focus);
      window.removeEventListener("blur", blur);
      document.removeEventListener("visibilitychange", visibility);
    };
  }, [enterLatest]);
  useLayoutEffect(() => {
    observe();
    if (!confirmation) return;
    if (confirmation.generation === generation.current && confirmation.threadID === options.threadID && atLatest(options)) {
      options.reading!.confirm(options.threadID, confirmation.ids);
    }
    setConfirmation(null);
  }, [options.content, options.readable, options.ready, options.threadID, confirmation, observe]);
  return useCallback(() => {
    const following = atLatest(current.current);
    if (!following) generation.current++;
    if (following && !wasFollowing.current) void enterLatest();
    wasFollowing.current = following;
    observe();
  }, [enterLatest, observe]);
}
