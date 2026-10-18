import { useId, useRef, useState } from "react";
import { useLocale } from "../../lib/locale";

export type UsageDay = { date: string; total_tokens: number; calls: number };

// API dates are calendar days in the displayed UTC offset. UTC arithmetic keeps
// these labels stable even when the browser crosses a daylight-saving boundary.
export function usageCalendar(start: string, end: string, days: UsageDay[]): UsageDay[] {
  const byDate = new Map(days.map((day) => [day.date, day]));
  const result: UsageDay[] = [];
  const date = new Date(`${start}T00:00:00Z`);
  while (date.toISOString().slice(0, 10) <= end) {
    const key = date.toISOString().slice(0, 10);
    result.push(byDate.get(key) ?? { date: key, total_tokens: 0, calls: 0 });
    date.setUTCDate(date.getUTCDate() + 1);
  }
  return result;
}

export function UsageTrend({ days }: { days: UsageDay[] }) {
  const { locale, t } = useLocale();
  const titleID = useId();
  const compact = new Intl.NumberFormat(locale, { notation: "compact", maximumFractionDigits: 1 });
  const byMonth = days.length > 180;
  const points: Array<{ date: string; total: number }> = [];
  for (const day of days) {
    const date = byMonth ? day.date.slice(0, 7) : day.date;
    const previous = points.at(-1);
    if (previous?.date === date) previous.total += day.total_tokens;
    else points.push({ date, total: day.total_tokens });
  }
  const peak = Math.max(0, ...points.map((point) => point.total));
  const width = 720, height = 164, left = 4, right = 4, top = 14, bottom = 140;
  const coordinates = points.map((point, index) => ({ ...point,
    x: points.length === 1 ? (left + width - right) / 2 : left + index / (points.length - 1) * (width - left - right),
    y: bottom - point.total / (peak || 1) * (bottom - top),
  }));
  const line = coordinates.map((point) => `${point.x},${point.y}`).join(" ");
  const dateLabel = (date: string) => byMonth ? date : date.slice(5).replace("-", "/");
  return <section className="v2-usage-trend" aria-labelledby={titleID}>
    <header className="v2-usage-section-heading"><h2 id={titleID}>{t("用量趋势", "Usage over time")}</h2>
      <span>{byMonth ? t("按月", "Monthly") : t("按日", "Daily")}{" / "}{t("峰值", "Peak")} {compact.format(peak)}</span></header>
    <div className="v2-usage-plot"><div className="v2-usage-y-axis" aria-hidden="true">
      {(peak > 0 ? [1, 0.5, 0] : [0]).map((fraction) => <span key={fraction} style={{ top: `${(bottom - fraction * (bottom - top)) / height * 100}%` }}>{compact.format(peak * fraction)}</span>)}
    </div><svg viewBox={`0 0 ${width} ${height}`} preserveAspectRatio="none" role="img" aria-label={t(
      `${byMonth ? "每月" : "每日"} Token 用量，峰值 ${peak.toLocaleString(locale)}。下方可展开明细。`,
      `${byMonth ? "Monthly" : "Daily"} token usage, peak ${peak.toLocaleString(locale)}. Expand the details below for values.`)}>
      {(peak > 0 ? [0, 0.5, 1] : [0]).map((fraction) => <line key={fraction} className="v2-usage-gridline" x1={left} x2={width - right} y1={bottom - fraction * (bottom - top)} y2={bottom - fraction * (bottom - top)} />)}
      {coordinates.length > 1 && <><polygon className="v2-usage-area" points={`${left},${bottom} ${line} ${width - right},${bottom}`} />
        <polyline className="v2-usage-line" points={line} /></>}
      {coordinates.length === 1 && <circle className="v2-usage-point" cx={coordinates[0].x} cy={coordinates[0].y} r={4} />}
    </svg></div>
    {points.length > 0 && <div className="v2-usage-axis-labels"><span>{dateLabel(points[0].date)}</span>
      {points.length > 2 && <span>{dateLabel(points[Math.floor(points.length / 2)].date)}</span>}
      {points.length > 1 && <span>{dateLabel(points[points.length - 1].date)}</span>}</div>}
    <details className="v2-usage-daily-details"><summary>{byMonth ? t("每月明细", "Monthly details") : t("每日明细", "Daily details")}</summary>
      <div tabIndex={0} role="region" aria-label={t("用量明细", "Usage details")}><table>
        <thead><tr><th scope="col">{t("日期", "Date")}</th><th scope="col">Token</th></tr></thead>
        <tbody>{points.map((point) => <tr key={point.date}><th scope="row">{point.date}</th><td>{point.total.toLocaleString(locale)}</td></tr>)}</tbody>
      </table></div>
    </details>
  </section>;
}

export function UsageActivity({ days }: { days: UsageDay[] }) {
  const { locale, t } = useLocale();
  const titleID = useId();
  const [selectedDate, setSelectedDate] = useState("");
  const tooltipID = useId();
  const [tooltipDate, setTooltipDate] = useState("");
  const cells = useRef<Array<HTMLButtonElement | null>>([]);
  const selected = Math.max(0, selectedDate ? days.findIndex((day) => day.date === selectedDate) : days.length - 1);
  const current = days[selected];
  const activeDays = days.filter((day) => day.calls > 0).length;
  const peak = Math.max(1, ...days.map((day) => day.total_tokens));
  const leading = days.length ? (new Date(`${days[0].date}T00:00:00Z`).getUTCDay() + 6) % 7 : 0;
  const columns = Math.ceil((leading + days.length) / 7);
  const tooltipIndex = days.findIndex((day) => day.date === tooltipDate);
  const tooltipDay = days[tooltipIndex];
  const tooltipCell = leading + tooltipIndex;
  const showDate = (date: string) => { setSelectedDate(date); setTooltipDate(date); };
  const months: Array<{ column: number; label: string }> = [];
  days.forEach((day, index) => {
    const column = Math.floor((leading + index) / 7);
    if ((index === 0 && Number(day.date.slice(-2)) <= 7 || day.date.endsWith("-01")) && column < columns - 1) {
      months.push({ column, label: new Date(`${day.date}T00:00:00Z`).toLocaleDateString(locale, { month: "short", timeZone: "UTC" }) });
    }
  });
  return <section className="v2-usage-activity" aria-labelledby={titleID}>
    <header className="v2-usage-section-heading"><h2 id={titleID}>{t("活动", "Activity")}</h2>
      <span>{t(`近 26 周，活跃 ${activeDays} 天`, `Last 26 weeks, ${activeDays} active days`)}</span></header>
    <div className="v2-usage-heatmap-months" aria-hidden="true">{months.map((month) =>
      <span key={month.column} style={{ left: `${month.column / columns * 100}%` }}>{month.label}</span>)}</div>
    <div className="v2-usage-heatmap" role="group" aria-label={t("每日活动，使用方向键选择日期", "Daily activity; use arrow keys to select a date")}
      style={{ gridTemplateColumns: `repeat(${columns}, minmax(0, 1fr))` }}>
      {Array.from({ length: leading }, (_, index) => <span key={`pad-${index}`} />)}
      {days.map((day, index) => {
        const level = day.total_tokens > 0 ? Math.max(1, Math.ceil(day.total_tokens / peak * 4)) : day.calls > 0 ? 1 : 0;
        const label = t(`${day.date}：${day.total_tokens.toLocaleString(locale)} Token，${day.calls} 次调用`,
          `${day.date}: ${day.total_tokens.toLocaleString(locale)} tokens, ${day.calls} calls`);
        return <button type="button" key={day.date} data-level={level} aria-label={label}
          data-active={day.date === tooltipDate || undefined} aria-describedby={day.date === tooltipDate ? tooltipID : undefined}
          tabIndex={index === selected ? 0 : -1} ref={(node) => { cells.current[index] = node; }}
          onFocus={() => showDate(day.date)} onMouseEnter={() => showDate(day.date)} onClick={() => showDate(day.date)}
          onBlur={() => setTooltipDate("")} onMouseLeave={(event) => {
            if (document.activeElement !== event.currentTarget) setTooltipDate("");
          }}
          onKeyDown={(event) => {
            if (event.key === "Escape") { event.preventDefault(); setTooltipDate(""); return; }
            const offsets: Record<string, number> = { ArrowLeft: -7, ArrowRight: 7, ArrowUp: -1, ArrowDown: 1, Home: -index, End: days.length - 1 - index };
            if (!(event.key in offsets)) return;
            event.preventDefault();
            const next = Math.max(0, Math.min(days.length - 1, index + offsets[event.key]));
            cells.current[next]?.focus();
          }} />;
      })}
      {tooltipDay && <div id={tooltipID} role="tooltip" className="v2-usage-tooltip" style={{
        left: `clamp(70px, calc((100% + var(--usage-heatmap-gap)) * ${(Math.floor(tooltipCell / 7) + .5) / columns} - var(--usage-heatmap-gap) / 2), calc(100% - 70px))`,
        top: `calc((100% + var(--usage-heatmap-gap)) * ${(tooltipCell % 7) / 7})`,
      }}><div><strong>{new Intl.NumberFormat("en-US", { notation: "compact", maximumFractionDigits: 1 }).format(tooltipDay.total_tokens)}</strong> tokens</div>
        <time dateTime={tooltipDay.date}>{tooltipDay.date}</time></div>}
    </div>
    <footer className="v2-usage-activity-caption"><span>{current ? `${current.date} / ${current.total_tokens.toLocaleString(locale)} Token` : ""}</span>
      <span className="v2-usage-legend" aria-label={t("色深表示 Token 用量", "Darker cells indicate more tokens")}>{t("少", "Less")}
        {[0, 1, 2, 3, 4].map((level) => <i key={level} data-level={level} />)}{t("多", "More")}</span></footer>
  </section>;
}
