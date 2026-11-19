import { useLocale } from "../../lib/locale";
import { useMemo, useState } from "react";
import { useInfiniteQuery } from "@tanstack/react-query";
import { ChevronRight, ListChecks, MessagesSquare, Search } from "lucide-react";
import type { APIClient } from "../../api/client";
import type { RunView, SessionView } from "../../api/types";
import { LifecycleStatusLabel } from "../../components/lifecycle-status";
import { formatDate } from "../../lib/format";
import { v2QueryKeys } from "../query-keys";
import "./secondary-page.css";
import "./history-page.css";

type RecordKind = "run" | "session";

export function HistoryRecordBrowser({ client, onOpen }: {
  client: APIClient;
  onOpen: (kind: RecordKind, id: string) => void;
}) {
  const { t } = useLocale();
  const [kind, setKind] = useState<RecordKind | null>(null);
  return <section className="v2-record-browser" aria-label={t("全部运行与会话", "All runs and sessions")}>
    <div className={kind ? "v2-page-segments" : "v2-history-collections"} role="group" aria-label={t("记录类型", "Record type")}>
      <button aria-pressed={kind === "run"} onClick={() => setKind("run")} type="button">
        <ListChecks aria-hidden="true" size={16} />{t("运行记录", "Run records")}{!kind && <ChevronRight aria-hidden="true" size={14} />}
      </button>
      <button aria-pressed={kind === "session"} onClick={() => setKind("session")} type="button">
        <MessagesSquare aria-hidden="true" size={16} />{t("会话记录", "Session records")}{!kind && <ChevronRight aria-hidden="true" size={14} />}
      </button>
    </div>
    {kind && <RecordList client={client} key={kind} kind={kind} onOpen={onOpen} />}
  </section>;
}

function RecordList({ client, kind, onOpen }: {
  client: APIClient;
  kind: RecordKind;
  onOpen: (kind: RecordKind, id: string) => void;
}) {
  const { t } = useLocale();
  const [search, setSearch] = useState("");
  const query = useInfiniteQuery({
    queryKey: [...v2QueryKeys.historyRecords, kind],
    queryFn: ({ signal, pageParam }) => client.getPage<RunView | SessionView>(
      kind === "run" ? "/runs" : "/sessions", { limit: 50 }, pageParam, signal),
    initialPageParam: "", getNextPageParam: (page) => page.page.next_cursor || undefined,
    retry: false,
  });
  const loaded = useMemo(() => [...new Map((query.data?.pages ?? []).flatMap((page) => page.items)
    .map((record) => [record.id, record])).values()], [query.data]);
  const normalized = search.trim().toLocaleLowerCase();
  const visible = loaded.filter((record) => !normalized ||
    `${record.id} ${"title" in record ? record.title : ""}`.toLocaleLowerCase().includes(normalized));
  const label = kind === "run" ? t("运行记录", "Run records") : t("会话记录", "Session records");
  return <>
    <label className="v2-record-search"><Search aria-hidden="true" size={16} />
      <input aria-label={t(`搜索${label}`, `Search ${label}`)} onChange={(event) => setSearch(event.target.value)} type="search"
        placeholder={kind === "run" ? t("搜索已加载的运行 ID", "Search loaded run IDs") : t("搜索已加载的会话标题或 ID", "Search loaded session titles or IDs")} value={search} />
    </label>
    {query.isPending && <p role="status">{t(`正在读取${label}…`, `Loading ${label.toLowerCase()}…`)}</p>}
    {query.isError && <p role="alert">{t(`${label}加载失败`, `Could not load ${label.toLowerCase()}`)}
      <button onClick={() => void (query.isFetchNextPageError ? query.fetchNextPage() : query.refetch())}
        type="button">{t(`重试${label}`, `Retry ${label.toLowerCase()}`)}</button></p>}
    {!query.isPending && !query.isError && visible.length === 0 && <p role="status">
      {normalized ? t("已加载记录中没有匹配项", "No matches in loaded records") : t(`暂无${label}`, `No ${label.toLowerCase()} yet`)}</p>}
    <ul className="v2-record-list">{visible.map((record) => <li key={record.id}>
      <button aria-label={t(`打开${label} ${record.id}`, `Open ${label.toLowerCase()} ${record.id}`)} onClick={() => onOpen(kind, record.id)} type="button">
        <span className="v2-record-name"><strong>{"title" in record && record.title ? record.title : record.id}</strong>
          {"title" in record && record.title && <code>{record.id}</code>}</span>
        <span aria-label={kind === "run" ? t("执行记录状态", "Run record status") : t("上下文记录状态", "Context record status")} className={`v2-record-status${record.status === "failed" ? " is-failed" : record.status === "waiting_approval" ? " is-pending" : ""}`}>
          <LifecycleStatusLabel status={record.status} kind={kind} /></span>
        <time dateTime={record.updated_at}>{formatDate(record.updated_at)}</time>
      </button>
    </li>)}</ul>
    {query.hasNextPage && !query.isFetchNextPageError && <button className="v2-record-load-more" disabled={query.isFetchingNextPage}
      onClick={() => void query.fetchNextPage()} type="button">
      {query.isFetchingNextPage ? t("正在加载更早记录…", "Loading earlier records…") : t(`加载更早${label}`, `Load earlier ${label.toLowerCase()}`)}</button>}
  </>;
}
