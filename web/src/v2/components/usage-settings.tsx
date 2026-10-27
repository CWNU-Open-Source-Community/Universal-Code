import { useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { ArrowUpRight, RefreshCw } from "lucide-react";
import type { APIClient } from "../../api/client";
import type { components } from "../../api/schema";
import { useLocale } from "../../lib/locale";
import { v2QueryKeys } from "../query-keys";
import { UsageActivity, UsageTrend, usageCalendar } from "./usage-charts";
import "./usage-settings.css";

type Period = "today" | "30d" | "all";
type UsageStatistics = components["schemas"]["UsageStatistics"];

export function UsageSettings({ client, threadID = "", onOpenThread }: { client: APIClient; threadID?: string; onOpenThread?: (id: string) => void }) {
  const { locale, t } = useLocale();
  const [period, setPeriod] = useState<Period>("30d");
  const [currentChat, setCurrentChat] = useState(false);
  const scopedThreadID = currentChat ? threadID : "";
  const utcOffsetMinutes = -new Date().getTimezoneOffset();
  const query = useQuery({
    queryKey: v2QueryKeys.usage(period, utcOffsetMinutes, scopedThreadID),
    queryFn: ({ signal }) => client.get<UsageStatistics>("/usage", {
      period, utc_offset_minutes: utcOffsetMinutes, ...(scopedThreadID ? { thread_id: scopedThreadID } : {}),
    }, signal),
    staleTime: 30_000,
    refetchInterval: 60_000,
  });
  const data = query.data;
  const number = (value: number) => value.toLocaleString(locale);
  const compact = new Intl.NumberFormat(locale, { notation: "compact", maximumFractionDigits: 1 });
  const timezone = `UTC${utcOffsetMinutes >= 0 ? "+" : "−"}${String(Math.floor(Math.abs(utcOffsetMinutes) / 60)).padStart(2, "0")}:${String(Math.abs(utcOffsetMinutes) % 60).padStart(2, "0")}`;
  const daily = data ? usageCalendar(data.start_date, data.end_date,
    data.days.map((day) => ({ date: day.date, ...day.usage }))) : [];
  const activity = data ? usageCalendar(data.activity_start_date, data.activity_end_date, data.activity_days) : [];
  const cost = data?.cost;
  const costAmount = (cost?.known_cost_micros ?? 0) / 1_000_000;
  const knownCost = cost && cost.priced_calls > 0 ? new Intl.NumberFormat(locale, {
    style: "currency", currency: "USD", minimumFractionDigits: 2, maximumFractionDigits: costAmount >= 0.01 ? 2 : 6,
  }).format(costAmount) : "—";

  return <div className="v2-usage">
    <header className="v2-usage-heading"><div><h1>{t("用量统计", "Usage")}</h1>
      <p>{scopedThreadID
        ? t("当前聊天所有轮次的模型调用与本地费用", "Model usage and local costs in the current chat")
        : t("Universal-Code 的模型调用与本地费用", "Model usage and local costs in Universal-Code")}</p></div>
      <div className="v2-usage-controls"><div className="v2-setting-segmented" role="group" aria-label={t("统计聊天范围", "Usage scope")}>
        <button type="button" aria-pressed={!scopedThreadID} onClick={() => setCurrentChat(false)}>{t("所有聊天", "All chats")}</button>
        <button type="button" aria-pressed={!!scopedThreadID} disabled={!threadID} onClick={() => setCurrentChat(true)}>{t("当前聊天", "Current chat")}</button>
      </div></div>
      <div className="v2-usage-controls"><div className="v2-setting-segmented" role="group" aria-label={t("统计时间范围", "Usage period")}>
        {([ ["today", t("今日", "Today")], ["30d", t("近 30 天", "30 days")], ["all", t("全部", "All time")] ] as const).map(([value, label]) =>
          <button key={value} type="button" aria-pressed={period === value} onClick={() => setPeriod(value)}>{label}</button>)}
      </div><button className="v2-usage-refresh" type="button" aria-label={t("刷新用量", "Refresh usage")}
        title={t("刷新用量", "Refresh usage")} disabled={query.isFetching} onClick={() => void query.refetch()}><RefreshCw aria-hidden="true" size={16} /></button></div>
    </header>
    {query.isPending && <p className="v2-usage-message" role="status">{t("正在读取模型用量…", "Loading model usage…")}</p>}
    {query.isError && <div className="v2-usage-message" role="alert"><p>{data
      ? t("刷新失败，当前显示上次读取的用量。", "Refresh failed. Showing the last loaded usage.")
      : t("无法读取用量统计。请检查连接后重试。", "Usage could not be loaded. Check the connection and try again.")}</p>
      <button className="v2-setting-link" type="button" onClick={() => void query.refetch()}>{t("重试", "Retry")}</button></div>}
    {data && <>
      <section className="v2-usage-overview" aria-label={t("用量总览", "Usage overview")}>
        <div className="v2-usage-total"><span>{t("已记录 Token", "Recorded tokens")}</span>
          <strong>{number(data.totals.total_tokens)}</strong>
          <p>{t(`${number(data.totals.calls)} 次模型调用`, `${number(data.totals.calls)} model calls`)}<span>{data.start_date} — {data.end_date}</span></p>
        </div>
        <div className="v2-usage-cost"><span>{t("已记录费用估算", "Recorded cost estimate")}</span><strong title={cost && cost.priced_calls > 0 ? `USD ${costAmount.toFixed(6)}` : undefined}>{knownCost}</strong>
          <p>{cost && cost.priced_calls > 0
            ? t(`${number(cost.priced_calls)} 次已定价结算 / USD`, `${number(cost.priced_calls)} priced settlements / USD`)
            : t("尚无已定价的结算记录", "No priced settlements yet")}</p>
          {cost && cost.unpriced_calls > 0 && <p>{t(`${number(cost.unpriced_calls)} 次结算缺少完整计价`, `Settlements with incomplete pricing: ${number(cost.unpriced_calls)}`)}</p>}
          {cost && cost.untracked_calls > 0 && <p>{t(`${number(cost.untracked_calls)} 次调用未记录费用`, `Calls without a recorded cost: ${number(cost.untracked_calls)}`)}</p>}
        </div>
      </section>
      <dl className="v2-usage-breakdown">
        <div><dt>{t("输入", "Input")}</dt><dd>{number(data.totals.input_tokens)}</dd></div>
        <div><dt>{t("输出", "Output")}</dt><dd>{number(data.totals.output_tokens)}</dd></div>
        <div><dt>{t("缓存读取", "Cache read")}</dt><dd>{data.totals.cache_usage_reported_calls > 0 ? number(data.totals.cache_read_tokens) : "—"}</dd></div>
        <div><dt>{t("缓存写入", "Cache write")}</dt><dd>{data.totals.cache_usage_reported_calls > 0 ? number(data.totals.cache_creation_tokens) : "—"}</dd></div>
      </dl>
      <p className="v2-usage-coverage">{t("缓存已包含在输入中。", "Cache tokens are included in input.")}{" "}
        {t(`${number(data.totals.cache_usage_reported_calls)} / ${number(data.totals.calls)} 次调用返回缓存明细。`,
          `${number(data.totals.cache_usage_reported_calls)} / ${number(data.totals.calls)} calls reported cache details.`)}
        {data.totals.usage_reported_calls < data.totals.calls && <> {t(
          `另有 ${number(data.totals.calls - data.totals.usage_reported_calls)} 次调用未返回 Token 用量。`,
          `Calls without token usage: ${number(data.totals.calls - data.totals.usage_reported_calls)}.`)}</>}
      </p>
      {data.totals.calls === 0 && <p className="v2-usage-empty-period" role="status">{t(
        "这段时间还没有模型调用。开始一次对话后，用量会显示在这里。", "No model calls in this period. Start a conversation to see usage here.")}</p>}
      <UsageTrend days={daily} />
      <div className="v2-usage-lists">
        <section aria-labelledby="usage-models-title"><header className="v2-usage-section-heading"><h2 id="usage-models-title">{t("模型", "Models")}</h2>
          <span>{t("Token 占比", "Token share")}</span></header>
          {!data.models.length && <p className="v2-usage-empty">{t("暂无模型用量记录", "No model usage yet")}</p>}
          <ul className="v2-usage-models">{data.models.map((model) => {
            const name = model.model || t("未记录模型", "Unrecorded model");
            const share = data.totals.total_tokens > 0 ? model.usage.total_tokens / data.totals.total_tokens * 100 : 0;
            return <li key={JSON.stringify([model.provider, model.model])}>
              <div className="v2-usage-model-heading"><strong title={name}>{name}</strong>
                <span title={`${number(model.usage.total_tokens)} Token`}>{compact.format(model.usage.total_tokens)}</span><span>{share.toFixed(1)}%</span></div>
              <div className="v2-usage-model-track" aria-hidden="true"><i style={{ width: `${share}%` }} /></div>
              <p><span>{model.provider || t("未记录供应商", "Unrecorded provider")}</span><span>{t(`${number(model.usage.calls)} 次调用`, `${number(model.usage.calls)} calls`)}</span></p>
            </li>;
          })}</ul>
        </section>
        <section aria-labelledby="usage-sessions-title"><header className="v2-usage-section-heading"><h2 id="usage-sessions-title">{t("最近会话", "Recent conversations")}</h2>
          <span>{t("期间用量", "Period usage")}</span></header>
          {!data.recent_sessions.length && <p className="v2-usage-empty">{t("暂无对话用量记录", "No conversation usage yet")}</p>}
          <ul className="v2-usage-sessions">{data.recent_sessions.map((session) => <li key={session.thread_id}>
            <div>{onOpenThread ? <button type="button" title={session.title}
              aria-label={t(`打开对话：${session.title}`, `Open conversation: ${session.title}`)}
              onClick={() => onOpenThread(session.thread_id)}><span>{session.title}</span><ArrowUpRight aria-hidden="true" size={14} /></button>
              : <strong title={session.title}>{session.title}</strong>}
              <span title={`${number(session.usage.total_tokens)} Token`}>{compact.format(session.usage.total_tokens)}</span></div>
            <p><time dateTime={session.last_activity_at}>{new Date(session.last_activity_at).toLocaleString(locale, { month: "short", day: "numeric", hour: "2-digit", minute: "2-digit" })}</time>
              <span>{t(`${number(session.usage.calls)} 次调用`, `${number(session.usage.calls)} calls`)}</span></p>
          </li>)}</ul>
          {data.unassigned_calls > 0 && <p className="v2-usage-empty">{t(
            `${number(data.unassigned_calls)} 次历史调用未关联对话，已计入总量。`,
            `${number(data.unassigned_calls)} calls have no conversation and are included in totals.`)}</p>}
        </section>
      </div>
      <UsageActivity days={activity} />
      <footer className="v2-usage-footer"><span>{timezone} / {t("按此偏移统计日期", "Dates use this UTC offset")}</span>
        <span>{t("更新于", "Updated")} {new Date(data.generated_at).toLocaleTimeString(locale, { hour: "2-digit", minute: "2-digit" })}</span></footer>
      <details className="v2-usage-method"><summary>{t("统计说明", "How usage is counted")}</summary><p>{t(
        "包含 Universal-Code 主代理、子代理、并行调用和上下文压缩的已完成调用。失败但已经产生用量的调用也会统计；Token 以供应商返回的用量为准。",
        "Includes completed calls from Universal-Code’s main agent, subagents, parallel calls, and context compaction. Failed calls with usage are included. Token counts come from provider receipts.")}</p><p>{t(
        "Token 按调用完成时间统计；费用按结算时间统计，仅累加已有完整价格记录的本地估算。尚未计价或尚未结算的调用不包含在金额内，实际费用以供应商账单为准。",
        "Tokens are grouped by call completion time. Costs use settlement time and include only local estimates with complete pricing. Unpriced and unsettled calls are excluded from the amount; provider billing is authoritative.")}</p></details>
    </>}
  </div>;
}
