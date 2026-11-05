import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act, render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import type { APIClient } from "../../api/client";
import type { components } from "../../api/schema";
import { downloadTextFile } from "../../lib/download";
import { LocaleProvider, useLocale } from "../../lib/locale";
import { UsageSettings } from "./usage-settings";
import { UsageActivity, UsageTrend, usageCalendar } from "./usage-charts";
import { V2Settings } from "./settings";

vi.mock("../../lib/download", () => ({ downloadTextFile: vi.fn() }));

type UsageStatistics = components["schemas"]["UsageStatistics"];
const totals = { calls: 4, usage_reported_calls: 3, cache_usage_reported_calls: 2,
  input_tokens: 1200, output_tokens: 300, total_tokens: 1500, cache_read_tokens: 800, cache_creation_tokens: 100 };
const emptyTotals = { calls: 0, usage_reported_calls: 0, cache_usage_reported_calls: 0,
  input_tokens: 0, output_tokens: 0, total_tokens: 0, cache_read_tokens: 0, cache_creation_tokens: 0 };
const data: UsageStatistics = {
  generated_at: "2026-10-19T04:30:00Z", period: "30d", utc_offset_minutes: 480,
  start_date: "2026-09-20", end_date: "2026-10-19", totals,
  cost: { currency: "USD", known_cost_micros: 125000, priced_calls: 2, unpriced_calls: 1, untracked_calls: 1 },
  unassigned_calls: 1, models: [{ provider: "test-provider", model: "test-model", usage: totals }],
  recent_sessions: [{ thread_id: "thread-a", title: "修复设置布局", last_activity_at: "2026-10-19T04:00:00Z", usage: totals }],
  days: [{ date: "2026-10-19", usage: totals }],
  activity_start_date: "2026-04-21", activity_end_date: "2026-10-19",
  activity_days: [{ date: "2026-10-19", calls: 4, total_tokens: 1500 }],
};

beforeEach(() => localStorage.setItem("prayu.locale.v1", "en-US"));
afterEach(() => { localStorage.removeItem("prayu.locale.v1"); vi.restoreAllMocks(); });

function setup(get = vi.fn().mockResolvedValue(data), integrated = false, threadID = "") {
  const onOpenThread = vi.fn();
  const client = { get } as unknown as APIClient;
  const queries = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  function LocaleSwitch() {
    const { setLocale } = useLocale();
    return <button type="button" onClick={() => setLocale("zh-CN")}>Switch language</button>;
  }
  render(<LocaleProvider><QueryClientProvider client={queries}><LocaleSwitch />{integrated
    ? <V2Settings client={client} section="usage" onOpenThread={onOpenThread} threadID={threadID} workspaces={[]}
      onOpenInspector={vi.fn()} onSelectSection={vi.fn()} />
    : <UsageSettings client={client} threadID={threadID} onOpenThread={onOpenThread} />}
  </QueryClientProvider></LocaleProvider>);
  return { get, onOpenThread };
}

it("mounts Usage in Settings, counts cache inside input, and opens the selected conversation", async () => {
  const { onOpenThread, get } = setup(undefined, true);
  const overview = await screen.findByRole("region", { name: "Usage overview" });
  expect(within(overview).getByText("1,500")).toBeInTheDocument();
  expect(within(overview).getByText("$0.13")).toHaveAttribute("title", "USD 0.125000");
  expect(screen.getByText(/2 \/ 4 calls reported cache details/)).toHaveTextContent("Calls without token usage: 1.");
  expect(screen.getByText("1 calls have no conversation and are included in totals.")).toBeInTheDocument();
  expect(get).toHaveBeenCalledWith("/usage", { period: "30d", utc_offset_minutes: -new Date().getTimezoneOffset() }, expect.any(AbortSignal));
  expect(screen.getByRole("button", { name: "All chats" })).toHaveAttribute("aria-pressed", "true");
  expect(screen.getByRole("button", { name: "Current chat" })).toBeDisabled();
  await userEvent.click(screen.getByRole("button", { name: "Open conversation: 修复设置布局" }));
  expect(onOpenThread).toHaveBeenCalledExactlyOnceWith("thread-a");
});

it("passes the current chat through Settings and keeps period and scope independent", async () => {
  const { get } = setup(undefined, true, "thread-a");
  await screen.findByRole("region", { name: "Usage overview" });
  await userEvent.click(screen.getByRole("button", { name: "Current chat" }));
  await waitFor(() => expect(get).toHaveBeenLastCalledWith("/usage", {
    period: "30d", utc_offset_minutes: -new Date().getTimezoneOffset(), thread_id: "thread-a",
  }, expect.any(AbortSignal)));
  await userEvent.click(screen.getByRole("button", { name: "All time" }));
  await waitFor(() => expect(get).toHaveBeenLastCalledWith("/usage", {
    period: "all", utc_offset_minutes: -new Date().getTimezoneOffset(), thread_id: "thread-a",
  }, expect.any(AbortSignal)));
  await userEvent.click(screen.getByRole("button", { name: "All chats" }));
  await waitFor(() => expect(get).toHaveBeenLastCalledWith("/usage", {
    period: "all", utc_offset_minutes: -new Date().getTimezoneOffset(),
  }, expect.any(AbortSignal)));
  expect(screen.getByRole("button", { name: "All time" })).toHaveAttribute("aria-pressed", "true");
});

it("isolates in-flight chat responses and returns to global usage when no chat is open", async () => {
  let resolveA!: (value: UsageStatistics) => void;
  let resolveB!: (value: UsageStatistics) => void;
  const pendingA = new Promise<UsageStatistics>((resolve) => { resolveA = resolve; });
  const pendingB = new Promise<UsageStatistics>((resolve) => { resolveB = resolve; });
  const get = vi.fn((_path: string, params: { thread_id?: string }, _signal?: AbortSignal) =>
    params.thread_id === "thread-a" ? pendingA : params.thread_id === "thread-b" ? pendingB : Promise.resolve(data));
  const client = { get } as unknown as APIClient;
  const queries = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  const view = (threadID: string) => <LocaleProvider><QueryClientProvider client={queries}>
    <UsageSettings client={client} threadID={threadID} />
  </QueryClientProvider></LocaleProvider>;
  const rendered = render(view("thread-a"));
  await screen.findByRole("region", { name: "Usage overview" });
  await userEvent.click(screen.getByRole("button", { name: "Current chat" }));
  await waitFor(() => expect(get).toHaveBeenCalledTimes(2));
  expect(screen.queryByRole("region", { name: "Usage overview" })).not.toBeInTheDocument();
  rendered.rerender(view("thread-b"));
  await waitFor(() => expect(get).toHaveBeenCalledTimes(3));
  expect(get.mock.calls[1][2]?.aborted).toBe(true);
  await act(async () => resolveB({ ...data, totals: { ...totals, total_tokens: 2400 } }));
  expect(within(await screen.findByRole("region", { name: "Usage overview" })).getByText("2,400")).toBeInTheDocument();
  await act(async () => resolveA({ ...data, totals: { ...totals, total_tokens: 1200 } }));
  expect(within(screen.getByRole("region", { name: "Usage overview" })).getByText("2,400")).toBeInTheDocument();
  expect(within(screen.getByRole("region", { name: "Usage overview" })).queryByText("1,200")).not.toBeInTheDocument();
  rendered.rerender(view(""));
  expect(screen.getByRole("button", { name: "Current chat" })).toBeDisabled();
  expect(screen.getByRole("button", { name: "All chats" })).toHaveAttribute("aria-pressed", "true");
  expect(within(await screen.findByRole("region", { name: "Usage overview" })).getByText("1,500")).toBeInTheDocument();
});

it("fetches the selected period and translates the entire dashboard without changing user titles", async () => {
  const { get } = setup();
  await screen.findByRole("region", { name: "Usage overview" });
  await userEvent.click(screen.getByRole("button", { name: "Today" }));
  await waitFor(() => expect(get).toHaveBeenLastCalledWith("/usage", expect.objectContaining({ period: "today" }), expect.any(AbortSignal)));
  await userEvent.click(screen.getByRole("button", { name: "All time" }));
  await waitFor(() => expect(get).toHaveBeenLastCalledWith("/usage", expect.objectContaining({ period: "all" }), expect.any(AbortSignal)));
  await userEvent.click(screen.getByRole("button", { name: "Switch language" }));
  expect(screen.getByRole("heading", { name: "用量统计" })).toBeInTheDocument();
  expect(screen.getByRole("button", { name: "全部" })).toHaveAttribute("aria-pressed", "true");
  expect(screen.getByRole("button", { name: "打开对话：修复设置布局" })).toBeInTheDocument();
  expect(screen.getByText("已记录费用估算")).toBeInTheDocument();
  expect(screen.queryByText("Recorded cost estimate")).not.toBeInTheDocument();
});

it("distinguishes missing usage/pricing from a reported zero cost", async () => {
  setup(vi.fn().mockResolvedValue({ ...data, totals: { ...totals, cache_usage_reported_calls: 0 },
    cost: { ...data.cost, priced_calls: 0, known_cost_micros: 0 } }));
  const overview = await screen.findByRole("region", { name: "Usage overview" });
  expect(within(overview).getByText("—")).toBeInTheDocument();
  expect(within(overview).queryByText("$0.00")).not.toBeInTheDocument();
  expect(screen.getByText("Cache read").parentElement).toHaveTextContent("—");
  expect(screen.getByText("Cache write").parentElement).toHaveTextContent("—");
  expect(screen.getByText("No priced settlements yet")).toBeInTheDocument();
});

it("shows a useful empty state while preserving an explicitly recorded zero cost", async () => {
  setup(vi.fn().mockResolvedValue({ ...data, totals: emptyTotals, models: [], recent_sessions: [], days: [], activity_days: [],
    cost: { ...data.cost, priced_calls: 1, known_cost_micros: 0, unpriced_calls: 0, untracked_calls: 0 } }));
  await screen.findByRole("region", { name: "Usage overview" });
  expect(screen.getByRole("status")).toHaveTextContent("No model calls in this period.");
  expect(screen.getByText("$0.00")).toBeInTheDocument();
  expect(screen.getByText("No model usage yet")).toBeInTheDocument();
  expect(screen.getByText("No conversation usage yet")).toBeInTheDocument();
});

it("exports the loaded usage as CSV with totals, day, model, and session rows", async () => {
  setup();
  await screen.findByRole("region", { name: "Usage overview" });
  await userEvent.click(screen.getByRole("button", { name: "Export CSV" }));
  expect(downloadTextFile).toHaveBeenCalledExactlyOnceWith("universal-code-usage-30d-2026-09-20_2026-10-19.csv",
    "text/csv;charset=utf-8", expect.stringMatching(/^\uFEFF/u));
  const csv = (vi.mocked(downloadTextFile).mock.calls[0][2] as string).slice("\uFEFF".length);
  const rows = csv.replace(/\r\n$/u, "").split("\r\n");
  expect(rows).toHaveLength(5);
  const header = rows[0].split(",");
  expect(header).toHaveLength(21);
  expect(header.slice(0, 8)).toEqual(["kind", "date", "provider", "model", "thread_id", "title", "last_activity_at", "input_tokens"]);
  expect(header.slice(13, 16)).toEqual(["usage_reported_calls", "cache_usage_reported_calls", "unassigned_calls"]);
  for (const row of rows) expect(row.split(",")).toHaveLength(21);
  const totalsRow = rows[1].split(",");
  expect(totalsRow[0]).toBe("totals");
  expect(totalsRow.slice(1, 7)).toEqual(["", "", "", "", "", ""]);
  expect(totalsRow.slice(7, 15)).toEqual(["1200", "300", "800", "100", "1500", "4", "3", "2"]);
  expect(totalsRow.slice(15)).toEqual(["1", "USD", "0.125000", "2", "1", "1"]);
  const dayRow = rows[2].split(",");
  expect(dayRow.slice(0, 2)).toEqual(["day", "2026-10-19"]);
  expect(dayRow.slice(2, 7)).toEqual(["", "", "", "", ""]);
  expect(dayRow.slice(7, 15)).toEqual(["1200", "300", "800", "100", "1500", "4", "3", "2"]);
  expect(dayRow.slice(15)).toEqual(["", "", "", "", "", ""]);
  const modelRow = rows[3].split(",");
  expect(modelRow.slice(0, 7)).toEqual(["model", "", "test-provider", "test-model", "", "", ""]);
  expect(modelRow.slice(7, 15)).toEqual(["1200", "300", "800", "100", "1500", "4", "3", "2"]);
  const sessionRow = rows[4].split(",");
  expect(sessionRow.slice(0, 7)).toEqual(["session", "", "", "", "thread-a", "修复设置布局", "2026-10-19T04:00:00Z"]);
  expect(sessionRow.slice(7, 15)).toEqual(["1200", "300", "800", "100", "1500", "4", "3", "2"]);
});

it("neutralizes spreadsheet formulas hiding in conversation titles and model names", async () => {
  vi.mocked(downloadTextFile).mockClear();
  setup(vi.fn().mockResolvedValue({ ...data,
    models: [{ provider: "=1+1", model: "@SUM(A1)", usage: totals }],
    recent_sessions: [
      { thread_id: "thread-1", title: "=HYPERLINK(\"http://evil\",1)", last_activity_at: "2026-10-19T04:00:00Z", usage: totals },
      { thread_id: "thread-2", title: "-2+3", last_activity_at: "2026-10-19T05:00:00Z", usage: totals },
      { thread_id: "thread-3", title: "\t=cmd|'/C calc'!A0", last_activity_at: "2026-10-19T06:00:00Z", usage: totals },
      { thread_id: "thread-4", title: "= 1+1", last_activity_at: "2026-10-19T07:00:00Z", usage: totals },
      { thread_id: "thread-5", title: "100%= 进度", last_activity_at: "2026-10-19T08:00:00Z", usage: totals }] }));
  await screen.findByRole("region", { name: "Usage overview" });
  await userEvent.click(screen.getByRole("button", { name: "Export CSV" }));
  const csv = vi.mocked(downloadTextFile).mock.calls[0][2] as string;
  // Every trigger-starting cell gains the text-forcing apostrophe, and quoting
  // still applies on top of it when the value carries a comma or a quote.
  expect(csv).toContain('"\'=HYPERLINK(""http://evil"",1)"');
  expect(csv).toContain("'@SUM(A1)");
  expect(csv).toContain("'=1+1");
  expect(csv).toContain("'-2+3");
  expect(csv).toContain("'\t=cmd|'/C calc'!A0");
  expect(csv).toContain("'= 1+1");
  // A trigger that is not at the start is ordinary text and stays untouched.
  expect(csv).toContain("100%= 进度");
  // Numeric cells never take the prefix.
  expect(csv).toContain("1200,300,800,100,1500,4,3,2");
});

it("escapes CSV special characters in conversation titles", async () => {
  vi.mocked(downloadTextFile).mockClear();
  setup(vi.fn().mockResolvedValue({ ...data, recent_sessions: [
    { thread_id: "thread-1", title: '修复, "设置" 布局', last_activity_at: "2026-10-19T04:00:00Z", usage: totals },
    { thread_id: "thread-2", title: "两行\r\n标题", last_activity_at: "2026-10-19T05:00:00Z", usage: totals }] }));
  await screen.findByRole("region", { name: "Usage overview" });
  await userEvent.click(screen.getByRole("button", { name: "Export CSV" }));
  const csv = vi.mocked(downloadTextFile).mock.calls[0][2] as string;
  expect(csv).toContain('"修复, ""设置"" 布局"');
  expect(csv).toContain('"两行\r\n标题"');
});

it("keeps the CSV export disabled while usage has not loaded", () => {
  setup(vi.fn().mockReturnValue(new Promise(() => {})));
  expect(screen.getByRole("button", { name: "Export CSV" })).toBeDisabled();
});

it("disables the CSV export for an empty period", async () => {
  setup(vi.fn().mockResolvedValue({ ...data, totals: emptyTotals, models: [], recent_sessions: [], days: [],
    cost: { currency: "USD", known_cost_micros: 0, priced_calls: 0, unpriced_calls: 0, untracked_calls: 0 } }));
  await screen.findByRole("status");
  expect(screen.getByRole("button", { name: "Export CSV" })).toBeDisabled();
});

it.each([
  { known_cost_micros: 125000, priced_calls: 1, unpriced_calls: 0, untracked_calls: 0 },
  { known_cost_micros: 0, priced_calls: 1, unpriced_calls: 0, untracked_calls: 0 },
  { known_cost_micros: 0, priced_calls: 0, unpriced_calls: 1, untracked_calls: 0 },
  { known_cost_micros: 0, priced_calls: 0, unpriced_calls: 0, untracked_calls: 1 },
])("exports cost facts without calls in the selected period: %j", async (cost) => {
  vi.mocked(downloadTextFile).mockClear();
  setup(vi.fn().mockResolvedValue({ ...data, totals: emptyTotals, models: [], recent_sessions: [], days: [],
    unassigned_calls: 0, cost: { currency: "USD", ...cost } }));
  await screen.findByRole("region", { name: "Usage overview" });
  expect(screen.getByRole("button", { name: "Export CSV" })).toBeEnabled();
  await userEvent.click(screen.getByRole("button", { name: "Export CSV" }));
  expect(downloadTextFile).toHaveBeenCalledExactlyOnceWith("universal-code-usage-30d-2026-09-20_2026-10-19.csv",
    "text/csv;charset=utf-8", expect.stringMatching(/^\uFEFF/u));
  const csv = vi.mocked(downloadTextFile).mock.calls[0][2] as string;
  const rows = csv.slice(1).trimEnd().split("\r\n");
  expect(rows).toHaveLength(2);
  const total = rows[1].split(",");
  expect(total[12]).toBe("0");
  expect(total.slice(16)).toEqual(["USD", (cost.known_cost_micros / 1_000_000).toFixed(6),
    String(cost.priced_calls), String(cost.unpriced_calls), String(cost.untracked_calls)]);
});

it("keeps the CSV export disabled during a refresh with previously loaded cost facts", async () => {
  const get = vi.fn().mockResolvedValueOnce({ ...data, totals: emptyTotals, models: [], recent_sessions: [], days: [] })
    .mockReturnValue(new Promise(() => {}));
  setup(get);
  await screen.findByRole("region", { name: "Usage overview" });
  expect(screen.getByRole("button", { name: "Export CSV" })).toBeEnabled();
  await userEvent.click(screen.getByRole("button", { name: "Refresh usage" }));
  expect(screen.getByRole("button", { name: "Export CSV" })).toBeDisabled();
});

it("offers retry when loading fails and restores the real data", async () => {
  const get = vi.fn().mockRejectedValueOnce(new Error("offline")).mockResolvedValue(data);
  setup(get);
  expect(await screen.findByRole("alert")).toHaveTextContent("Usage could not be loaded.");
  expect(screen.queryByRole("region", { name: "Usage overview" })).not.toBeInTheDocument();
  await userEvent.click(screen.getByRole("button", { name: "Retry" }));
  expect(await screen.findByRole("region", { name: "Usage overview" })).toHaveTextContent("1,500");
});

it("fills calendar gaps through DST and exposes heatmap dates with one keyboard tab stop", async () => {
  const days = usageCalendar("2026-10-31", "2026-11-02", [{ date: "2026-11-02", calls: 2, total_tokens: 80 }]);
  expect(days).toEqual([{ date: "2026-10-31", calls: 0, total_tokens: 0 },
    { date: "2026-11-01", calls: 0, total_tokens: 0 }, { date: "2026-11-02", calls: 2, total_tokens: 80 }]);
  render(<LocaleProvider><UsageActivity days={days} /></LocaleProvider>);
  const heatmap = screen.getByRole("group", { name: /Daily activity/ });
  const cells = within(heatmap).getAllByRole("button");
  expect(cells.filter((cell) => cell.tabIndex === 0)).toHaveLength(1);
  await userEvent.tab();
  expect(cells[2]).toHaveFocus();
  await userEvent.keyboard("{ArrowUp}");
  expect(cells[1]).toHaveFocus();
  expect(cells[1]).toHaveAccessibleName("2026-11-01: 0 tokens, 0 calls");
});

it("groups long histories by month without losing sparse daily usage", async () => {
  const days = usageCalendar("2026-04-01", "2026-10-01", [
    { date: "2026-04-01", calls: 1, total_tokens: 10 },
    { date: "2026-04-02", calls: 1, total_tokens: 20 },
    { date: "2026-10-01", calls: 1, total_tokens: 5 },
  ]);
  render(<LocaleProvider><UsageTrend days={days} /></LocaleProvider>);
  expect(screen.getByText("Monthly / Peak 30")).toBeInTheDocument();
  await userEvent.click(screen.getByText("Monthly details"));
  expect(screen.getByRole("row", { name: "2026-04 30" })).toBeVisible();
  expect(screen.getByRole("row", { name: "2026-10 5" })).toBeVisible();
});

it("highlights a heatmap date and shows its tokens on hover, click, and keyboard focus", async () => {
  const user = userEvent.setup();
  render(<LocaleProvider><UsageActivity days={[
    { date: "2026-08-28", calls: 3, total_tokens: 1_400_000_000 },
    { date: "2026-08-29", calls: 1, total_tokens: 80 },
  ]} /><button type="button">Outside heatmap</button></LocaleProvider>);
  const cells = within(screen.getByRole("group", { name: /Daily activity/ })).getAllByRole("button");
  expect(screen.queryByRole("tooltip")).not.toBeInTheDocument();
  await user.hover(cells[0]);
  expect(cells[0]).toHaveAttribute("data-active", "true");
  expect(cells[0]).toHaveAttribute("aria-describedby", screen.getByRole("tooltip").id);
  expect(screen.getByRole("tooltip")).toHaveTextContent("1.4B tokens");
  expect(within(screen.getByRole("tooltip")).getByText("2026-08-28")).toHaveAttribute("dateTime", "2026-08-28");
  await user.unhover(cells[0]);
  expect(screen.queryByRole("tooltip")).not.toBeInTheDocument();
  expect(cells[0]).not.toHaveAttribute("data-active");
  await user.click(cells[0]);
  await user.unhover(cells[0]);
  expect(screen.getByRole("tooltip")).toHaveTextContent("1.4B tokens");
  await user.keyboard("{ArrowDown}");
  expect(cells[1]).toHaveFocus();
  expect(cells[1]).toHaveAttribute("data-active", "true");
  expect(screen.getByRole("tooltip")).toHaveTextContent("80 tokens2026-08-29");
  await user.keyboard("{Escape}");
  expect(screen.queryByRole("tooltip")).not.toBeInTheDocument();
  expect(cells[1]).toHaveFocus();
  await user.click(cells[0]);
  await user.tab();
  expect(screen.getByRole("button", { name: "Outside heatmap" })).toHaveFocus();
  expect(screen.queryByRole("tooltip")).not.toBeInTheDocument();
});
