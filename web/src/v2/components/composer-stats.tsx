import { useEffect, useId, useRef, useState } from "react";
import { Database, Gauge } from "lucide-react";
import type { ThreadDetailView } from "../../api/types";
import { useLocale } from "../../lib/locale";
import "./composer-stats.css";

type Statistics = NonNullable<ThreadDetailView["statistics"]>;

function compactTokens(value: number): string {
  const unit = value >= 1_000_000 ? 1_000_000 : value >= 1_000 ? 1_000 : 1;
  const scaled = value / unit;
  return `${Number(scaled.toFixed(unit > 1 && scaled < 100 ? 1 : 0))}${unit === 1_000_000 ? "M" : unit === 1_000 ? "K" : ""}`;
}

function cachePercentage(read: number, input: number): string {
  const value = Math.min(100, read / input * 100);
  // A rounded partial hit must not claim the entire prompt was cached.
  for (let digits = 0; digits <= 3; digits++) {
    const rounded = Number(value.toFixed(digits));
    if (rounded < 100 || value === 100) return `${rounded}%`;
  }
  return "<100%";
}

export function V2ComposerStats({ statistics }: { statistics?: Statistics }) {
  const { t, locale } = useLocale();
  const [expanded, setExpanded] = useState<"activity" | "usage" | "context" | null>(null);
  const root = useRef<HTMLDivElement>(null);
  const descriptionID = useId();
  useEffect(() => {
    if (!expanded) return;
    const closeOutside = (event: PointerEvent) => {
      if (event.target instanceof Node && !root.current?.contains(event.target)) setExpanded(null);
    };
    document.addEventListener("pointerdown", closeOutside);
    return () => document.removeEventListener("pointerdown", closeOutside);
  }, [expanded]);
  if (!statistics) return null;
  const stats = statistics;
  const context = stats.context && stats.context.window_tokens > 0 ? stats.context : undefined;
  if (stats.steps === 0 && stats.total_tokens === 0 && !context) return null;

  const speed = stats.decode_millis > 0 && stats.timed_output_tokens > 0
    ? stats.timed_output_tokens * 1_000 / stats.decode_millis : undefined;
  const speedLabel = speed === undefined ? undefined : Number(speed.toFixed(speed < 10 ? 1 : 0));
  const cacheKnown = stats.usage_calls > 0 && stats.cache_reported_calls === stats.usage_calls;
  const cacheLabel = cacheKnown ? cachePercentage(stats.cache_read_tokens, stats.input_tokens) : "—";
  const percent = context ? Math.min(100, Math.round(context.input_tokens / context.window_tokens * 100)) : undefined;
  const format = (value: number) => value.toLocaleString(locale);
  const activityTitle = t(
    `本对话主代理：${stats.turns} 轮，${stats.steps} 个模型步骤。${speedLabel === undefined ? "暂无生成速度记录。" : `平均生成速度 ${speedLabel} tok/s。`}速度为已计时调用的输出 Token ÷ 生成时长，不含首 Token 等待与工具执行。`,
    `Main agent in this conversation: ${stats.turns} turns, ${stats.steps} model steps. ${speedLabel === undefined ? "Generation speed is unavailable." : `Average generation speed: ${speedLabel} tok/s.`} Speed is output tokens / measured generation time, excluding time to first token and tool execution.`,
  );
  const usageTitle = t(
    `本对话主代理累计输入 ${format(stats.input_tokens)} Token（含缓存），输出 ${format(stats.output_tokens)} Token。${cacheKnown ? `缓存命中 ${cacheLabel}。缓存命中率 = 缓存读取 Token ÷ 输入 Token。` : `${stats.cache_reported_calls} / ${stats.usage_calls} 次调用提供了缓存明细，暂不计算命中率。`}`,
    `Main agent totals: ${format(stats.input_tokens)} input tokens (including cache), ${format(stats.output_tokens)} output tokens. ${cacheKnown ? `Cache hit ${cacheLabel}. Cache hit rate = cached input / all input tokens.` : `${stats.cache_reported_calls} / ${stats.usage_calls} calls reported cache usage; hit rate is unavailable.`}`,
  );
  const contextTitle = context ? t(
    `最近一次模型请求${context.estimated ? "估算" : "使用"} ${format(context.input_tokens)} / ${format(context.window_tokens)} Token（应用规划窗口）。`,
    `Latest model request: ${context.estimated ? "estimated " : ""}${format(context.input_tokens)} / ${format(context.window_tokens)} tokens (application context limit).`,
  ) : undefined;

  const description = expanded === "activity" ? activityTitle : expanded === "usage" ? usageTitle : expanded === "context" ? contextTitle : undefined;
  return <div className="v2-composer-stats" role="group" aria-label={t("对话统计", "Conversation statistics")} ref={root}
    onKeyDown={(event) => { if (event.key === "Escape" && expanded) { event.stopPropagation(); setExpanded(null); } }}
    onBlur={(event) => { if (!event.currentTarget.contains(event.relatedTarget)) setExpanded(null); }}>
    {stats.steps > 0 && <button type="button" className="v2-composer-stat" title={activityTitle}
      aria-expanded={expanded === "activity"} aria-describedby={expanded === "activity" ? descriptionID : undefined}
      onClick={() => setExpanded(expanded === "activity" ? null : "activity")}>
      <Gauge size={14} strokeWidth={1.5} aria-hidden="true" />
      <span>{t(`${format(stats.turns)} 轮 ${format(stats.steps)} 步`, `${format(stats.turns)} turns ${format(stats.steps)} steps`)}</span>
      {speedLabel !== undefined && <span className="v2-composer-stat-detail"><span aria-hidden="true">·</span><span>{speedLabel} tok/s</span></span>}
    </button>}
    {stats.total_tokens > 0 && <button type="button" className="v2-composer-stat" title={usageTitle}
      aria-expanded={expanded === "usage"} aria-describedby={expanded === "usage" ? descriptionID : undefined}
      onClick={() => setExpanded(expanded === "usage" ? null : "usage")}>
      <Database size={14} strokeWidth={1.5} aria-hidden="true" />
      <span>{compactTokens(stats.total_tokens)} tok</span>
      {stats.input_tokens > 0 && <span className="v2-composer-stat-detail"><span aria-hidden="true">·</span><span>{t("缓存命中", "Cache hit")} {cacheLabel}</span></span>}
    </button>}
    {percent !== undefined && <button type="button" className="v2-composer-stat" title={contextTitle}
      aria-expanded={expanded === "context"} aria-describedby={expanded === "context" ? descriptionID : undefined}
      onClick={() => setExpanded(expanded === "context" ? null : "context")}>
      <svg className="v2-composer-context-ring" width="14" height="14" viewBox="0 0 14 14" aria-hidden="true">
        <circle className="v2-composer-context-track" cx="7" cy="7" r="5.5" fill="none" stroke="currentColor" strokeWidth="2" />
        {percent > 0 && <circle cx="7" cy="7" r="5.5" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round"
          pathLength="100" strokeDasharray={`${percent} 100`} transform="rotate(-90 7 7)" />}
      </svg>
      <span>{percent}%</span>
    </button>}
    {description && <div className="v2-composer-stat-description" role="note" id={descriptionID}>{description}</div>}
  </div>;
}
