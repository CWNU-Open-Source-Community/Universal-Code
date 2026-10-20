import type { ReactElement } from "react";
import type { RenderOptions } from "@testing-library/react";
import { LocaleProvider } from "../../lib/locale";
import { cleanup, render as renderComponent, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import type { ThreadRunRecoveryView } from "../../api/types";
import { ContextDiagnosticsPanel, validContextDiagnostics, type ContextDiagnostic } from "./context-diagnostics";

const receipt = (phase: string, sequence = 1): ContextDiagnostic => ({ sequence, phase,
  occurred_at: "2026-09-22T01:00:00Z", attempt_id: "attempt-1", source_sha256: "a".repeat(64),
  ...(phase === "summary_saved" ? { summary_id: 8, generated: false, removed_messages: 12, fallback_code: "generation_provider_failure" } : { model_attempt: 1 }) });

afterEach(cleanup);

test("received result and an interrupted start never claim a saved summary or live generation", () => {
  const view = render(<ContextDiagnosticsPanel runID="run-1" runStatus="running" value={{ records: [receipt("generation_received")], truncated: false }} />);
  expect(screen.getByText("最近记录：已收到生成结果")).toBeVisible();
  expect(screen.getByText(/尚不能据此确认摘要已保存/u)).toBeVisible();
  expect(screen.queryByText("压缩摘要已保存")).not.toBeInTheDocument();
  view.rerender(<ContextDiagnosticsPanel runID="run-1" runStatus="paused" value={{ records: [receipt("generation_started")], truncated: false }} />);
  expect(screen.getByText(/当前执行未在运行，最近显示的是生成开始记录/u)).toBeVisible();
});

test("saved fallback shows actionable reason and preserves source identity in expandable history", async () => {
  render(<ContextDiagnosticsPanel runID="run-1" runStatus="running" value={{ records: [receipt("summary_saved", 3), receipt("generation_failed", 2)], truncated: true }} />);
  expect(screen.getByText("已采用规则摘要回退：模型服务调用失败。")).toBeVisible();
  await userEvent.click(screen.getByText("查看压缩过程（最近 2 条）"));
  expect(screen.getByText(/本次归纳 12 条消息/u)).toBeVisible();
  expect(screen.getByText(/较早记录仍保留/u)).toBeVisible();
  await userEvent.click(screen.getAllByText("记录来源")[0]);
  expect(screen.getAllByText("a".repeat(64))[0]).toBeVisible();
});

test("recovery remains scoped to this run and distinguishes cleanup from continuing", () => {
  const recovery = { run_id: "run-1", quiescent: false, detail: "上轮工具结果尚未确认。" } as ThreadRunRecoveryView;
  const view = render(<ContextDiagnosticsPanel runID="run-1" runStatus="paused" recovery={recovery} value={{ records: [], truncated: false }} />);
  expect(screen.getByText("上轮正在释放资源")).toBeVisible();
  expect(screen.getByText("上轮工具结果尚未确认。")).toBeVisible();
  view.rerender(<ContextDiagnosticsPanel runID="run-2" runStatus="running" recovery={recovery} />);
  expect(screen.queryByText("上轮工具结果尚未确认。")).not.toBeInTheDocument();
  expect(screen.getByText("当前执行没有待恢复提示。")).toBeVisible();
  expect(screen.getByText(/当前服务未提供压缩过程记录/u)).toBeVisible();
});

test("diagnostic parser rejects malformed identity, invalid phases and contradictory receipts", () => {
  expect(validContextDiagnostics({ records: [receipt("summary_saved", 3), receipt("generation_received", 2)], truncated: false })).toBe(true);
  for (const changed of [
    { ...receipt("generation_started"), phase: "toString" },
    { ...receipt("generation_started"), source_sha256: "foreign" },
    { ...receipt("generation_started"), occurred_at: "unknown" },
    { ...receipt("generation_received"), summary_id: 7 },
    { ...receipt("summary_saved"), generated: undefined },
    { ...receipt("summary_saved"), removed_messages: 0 },
    { ...receipt("summary_saved"), fallback_code: "raw provider error" },
  ]) expect(validContextDiagnostics({ records: [changed], truncated: false })).toBe(false);
  expect(validContextDiagnostics({ records: [receipt("generation_started"), receipt("generation_received")], truncated: false })).toBe(false);
});

function render(ui: ReactElement, options?: RenderOptions) {
  window.localStorage.setItem("prayu.locale.v1", "zh-CN");
  const Wrapper = options?.wrapper;
  return renderComponent(ui, { ...options, wrapper: ({ children }) => <LocaleProvider>
    {Wrapper ? <Wrapper>{children}</Wrapper> : children}
  </LocaleProvider> });
}
