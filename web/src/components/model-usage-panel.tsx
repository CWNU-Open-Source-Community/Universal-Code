import type { RunDetailView } from "../api/types";
import { useLocale } from "../lib/locale";
import { formatNumber } from "../lib/format";
import { KeyValue } from "./common";

const usd = (micros: number) => `${(micros / 1_000_000).toFixed(6)} USD`;

export function ModelUsagePanel({ detail }: { detail: RunDetailView }) {
  const { t } = useLocale();
  const tokens = detail.model_usage;
  const money = detail.monetary_usage;
  if (!tokens && !money) return null;
  return <section className="detail-section" aria-label={t("模型用量与费用", "Model usage and cost")}>
    <h2>{t("模型用量与费用", "Model usage and cost")}</h2>
    {tokens && <><p>{t(`已记录 ${tokens.calls} 次调用的用量，包含主代理、子代理及上下文压缩。`, `Usage receipts for ${tokens.calls} calls, including root agents, subagents and context compaction.`)}</p><dl className="detail-grid compact">
      <KeyValue label={t("输入 Token（含缓存）", "Input tokens including cache")} value={formatNumber(tokens.input_tokens)} />
      <KeyValue label={t("输出 Token", "Output tokens")} value={formatNumber(tokens.output_tokens)} />
      <KeyValue label={t("缓存读取 Token", "Cache read tokens")} value={formatNumber(tokens.cache_read_tokens)} />
      <KeyValue label={t("缓存写入 Token", "Cache creation tokens")} value={formatNumber(tokens.cache_creation_tokens)} />
      <KeyValue label={t("5 分钟缓存写入", "5-minute cache creation")} value={formatNumber(tokens.cache_creation_5m_tokens)} />
      <KeyValue label={t("1 小时缓存写入", "1-hour cache creation")} value={formatNumber(tokens.cache_creation_1h_tokens)} />
      {tokens.cache_creation_tokens > tokens.cache_creation_5m_tokens + tokens.cache_creation_1h_tokens &&
        <KeyValue label={t("写入 TTL 未细分", "Creation TTL not reported")} value={formatNumber(tokens.cache_creation_tokens - tokens.cache_creation_5m_tokens - tokens.cache_creation_1h_tokens)} />}
    </dl>{tokens.cache_usage_reported_calls < tokens.calls && <p role="status">{t(
      `${tokens.cache_usage_reported_calls} / ${tokens.calls} 次调用报告了缓存明细；其他调用的缓存量尚未报告。`,
      `${tokens.cache_usage_reported_calls} / ${tokens.calls} calls reported cache details; the other calls did not report cache usage.`)}</p>}</>}
    {money && (money.tracked ? <><dl className="detail-grid compact">
      <KeyValue label={t("本地费用估算", "Local cost estimate")} value={usd(money.settled_micros)} />
      <KeyValue label={t("待结算预留", "Open reservation")} value={usd(money.reserved_micros - money.settled_micros - money.released_micros)} />
      <KeyValue label={t("剩余费用预算", "Remaining cost budget")} value={usd(money.remaining_micros)} />
    </dl>{money.unpriced_calls > 0 && <p className="inline-warning" role="status">{t(
      `${money.unpriced_calls} 次调用尚未精确定价，已保留预留金额作为估算。请核对缓存价格与用量记录。`,
      `${money.unpriced_calls} calls are not fully priced; their reserved estimates are retained. Check cache prices and usage receipts.`)}</p>}
      <p>{t("金额按调用时固定的价格快照估算，供应商账单用于核对实际费用。", "Amounts use the price snapshot pinned at dispatch. Check the provider bill for actual charges.")}</p>
    </> : <p>{t("未启用费用预算，本地费用尚未记录。", "No cost budget was enabled, so local cost is not recorded.")}</p>)}
  </section>;
}
