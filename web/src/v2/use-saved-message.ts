import { useInfiniteQuery, useQuery } from "@tanstack/react-query";
import { useMemo } from "react";
import type { APIClient } from "../api/client";
import type { ThreadSavedMessageView, ThreadTranscriptItemView } from "../api/types";
import { v2QueryKeys } from "./query-keys";

// Keep a search window separate from the moving head: an unread interval must
// never be represented as a contiguous page of the latest conversation.
export function useV2SavedMessage(client: APIClient, threadID: string, messageID: string) {
  const queryKey = [...v2QueryKeys.transcript(threadID), "message", messageID];
  const message = useQuery({ queryKey: [...queryKey, "location"], enabled: Boolean(messageID),
    queryFn: ({ signal }) => client.get<ThreadSavedMessageView>(
      `/threads/${encodeURIComponent(threadID)}/messages/${encodeURIComponent(messageID)}`, {}, signal) });
  const history = useInfiniteQuery({ queryKey, enabled: Boolean(messageID && message.data),
    initialPageParam: message.data?.transcript_cursor ?? "",
    queryFn: ({ signal, pageParam }) => client.getPage<ThreadTranscriptItemView>(
      `/threads/${encodeURIComponent(threadID)}/transcript`, { limit: 100 }, pageParam, signal),
    getNextPageParam: (page) => page.page.next_cursor || undefined });
  const items = useMemo(() => {
    const unique = new Map(history.data?.pages.slice().reverse().flatMap((page) => page.items)
      .map((item) => [item.id, item]));
    return [...unique.values()].map((item) => item.id === messageID && message.data
      ? { ...item, detail: message.data.content } : item);
  }, [history.data, messageID, message.data]);
  return { ...history, items, isLoading: message.isLoading || history.isLoading,
    isError: message.isError || history.isError, error: message.error ?? history.error,
    refetch: () => message.isError ? message.refetch() : history.refetch() };
}
