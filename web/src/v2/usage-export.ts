import type { components } from "../api/schema";

type UsageStatistics = components["schemas"]["UsageStatistics"];

// One row per fact kind over a single column schema, so spreadsheet filters and
// pandas-style reads both work. Cost facts exist only at the totals level (the
// usage ledger prices globally), so per-day/model/session cost cells stay empty
// instead of pretending per-row pricing exists.
const usageCsvHeader = [
  "kind", "date", "provider", "model", "thread_id", "title", "last_activity_at",
  "input_tokens", "output_tokens", "cache_read_tokens", "cache_creation_tokens",
  "total_tokens", "calls", "usage_reported_calls", "unassigned_calls",
  "cost_currency", "known_cost_usd", "priced_calls", "unpriced_calls", "untracked_calls",
] as const;

function csvField(value: string | number): string {
  const text = String(value);
  return /[",\r\n]/u.test(text) ? `"${text.replace(/"/gu, '""')}"` : text;
}

function csvRow(values: readonly (string | number)[]): string {
  return values.map(csvField).join(",");
}

function tokenColumns(usage: UsageStatistics["totals"]): (string | number)[] {
  return [usage.input_tokens, usage.output_tokens, usage.cache_read_tokens, usage.cache_creation_tokens,
    usage.total_tokens, usage.calls, usage.usage_reported_calls];
}

const emptyCost = (cost: UsageStatistics["cost"]): (string | number)[] =>
  [cost.currency, (cost.known_cost_micros / 1_000_000).toFixed(6), cost.priced_calls, cost.unpriced_calls, cost.untracked_calls];

// unassigned_calls plus the five cost columns, which only the totals row fills.
const noFacts = ["", "", "", "", "", ""] as const;

// The exported CSV mirrors the loaded usage projection exactly; it adds no
// derived numbers beyond formatting known_cost_micros into USD decimals.
export function formatUsageCsv(data: UsageStatistics): string {
  const rows: string[] = [csvRow(usageCsvHeader)];
  rows.push(csvRow(["totals", "", "", "", "", "", "", ...tokenColumns(data.totals), data.unassigned_calls, ...emptyCost(data.cost)]));
  for (const day of data.days) {
    rows.push(csvRow(["day", day.date, "", "", "", "", "", ...tokenColumns(day.usage), ...noFacts]));
  }
  for (const model of data.models) {
    rows.push(csvRow(["model", "", model.provider, model.model, "", "", "", ...tokenColumns(model.usage), ...noFacts]));
  }
  for (const session of data.recent_sessions) {
    rows.push(csvRow(["session", "", "", "", session.thread_id, session.title, session.last_activity_at,
      ...tokenColumns(session.usage), ...noFacts]));
  }
  return `${rows.join("\r\n")}\r\n`;
}

export function usageCsvFilename(data: UsageStatistics): string {
  return `universal-code-usage-${data.period}-${data.start_date}_${data.end_date}.csv`;
}
