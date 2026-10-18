import { cleanup, render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import type { ThreadDetailView } from "../../api/types";
import { LocaleProvider } from "../../lib/locale";
import { V2ComposerStats } from "./composer-stats";

const statistics = (): NonNullable<ThreadDetailView["statistics"]> => ({
  turns: 2, steps: 56, input_tokens: 2_000_000, output_tokens: 200_000, total_tokens: 2_200_000,
  cache_read_tokens: 1_940_000, cache_reported_calls: 56, usage_calls: 56,
  timed_output_tokens: 24_500, decode_millis: 100_000,
  context: { input_tokens: 6_000, window_tokens: 100_000, estimated: true, window_source: "provider_config" },
});

afterEach(() => { cleanup(); localStorage.clear(); });

function show(value?: NonNullable<ThreadDetailView["statistics"]>) {
  return render(<LocaleProvider><V2ComposerStats statistics={value} /></LocaleProvider>);
}

it("shows recorded conversation totals, weighted speed, cache ratio and context occupancy", () => {
  show(statistics());
  expect(screen.getByText("2 轮 56 步")).toBeInTheDocument();
  expect(screen.getByText("245 tok/s")).toBeInTheDocument();
  expect(screen.getByText("2.2M tok")).toBeInTheDocument();
  expect(screen.getByText("缓存命中 97%")).toBeInTheDocument();
  expect(screen.getByText("6%")).toBeInTheDocument();
  expect(screen.getByTitle(/最近一次模型请求估算/)).toHaveTextContent("6%");
});

it("keeps historical unknown timing and missing cache detail distinct from measured zero", () => {
  const value = { ...statistics(), decode_millis: 0, timed_output_tokens: 0, cache_reported_calls: 55, context: undefined };
  const view = show(value);
  expect(screen.queryByText(/tok\/s/)).not.toBeInTheDocument();
  expect(screen.getByText("缓存命中 —")).toBeInTheDocument();
  expect(screen.getByTitle(/55 \/ 56 次调用/)).toBeInTheDocument();
  expect(view.container.querySelector(".v2-composer-context-ring")).toBeNull();
  view.rerender(<LocaleProvider><V2ComposerStats statistics={{ ...value, cache_read_tokens: 0, cache_reported_calls: 56,
    context: { input_tokens: 0, window_tokens: 100_000, estimated: true, window_source: "provider_config" } }} /></LocaleProvider>);
  expect(screen.getByText("缓存命中 0%")).toBeInTheDocument();
  expect(screen.getByText("0%")).toBeInTheDocument();
});

it("does not round a partial cache hit to 100% and bounds the context ring", () => {
  show({ ...statistics(), cache_read_tokens: 1_999_200,
    context: { input_tokens: 120_000, window_tokens: 100_000, estimated: true, window_source: "provider_config" } });
  expect(screen.getByText("缓存命中 99.96%")).toBeInTheDocument();
  expect(screen.getByText("100%")).toBeInTheDocument();
});

it("hides statistics before any activity, including unsupported older servers", () => {
  const view = show();
  expect(screen.queryByRole("group")).not.toBeInTheDocument();
  view.rerender(<LocaleProvider><V2ComposerStats statistics={{ turns: 0, steps: 0, input_tokens: 0, output_tokens: 0,
    total_tokens: 0, cache_read_tokens: 0, cache_reported_calls: 0, usage_calls: 0, timed_output_tokens: 0, decode_millis: 0 }} /></LocaleProvider>);
  expect(screen.queryByRole("group")).not.toBeInTheDocument();
});

it("uses the selected interface language", () => {
  localStorage.setItem("prayu.locale.v1", "en-US");
  show(statistics());
  expect(screen.getByRole("group", { name: "Conversation statistics" })).toBeInTheDocument();
  expect(screen.getByText("2 turns 56 steps")).toBeInTheDocument();
  expect(screen.getByText("Cache hit 97%")).toBeInTheDocument();
});

it("opens metric explanations with the keyboard and closes them on Escape or outside click", async () => {
  const user = userEvent.setup();
  show(statistics());
  await user.tab();
  await user.keyboard("{Enter}");
  expect(screen.getByRole("note")).toHaveTextContent("不含首 Token 等待与工具执行");
  await user.keyboard("{Escape}");
  expect(screen.queryByRole("note")).not.toBeInTheDocument();
  await user.tab();
  await user.tab();
  await user.keyboard("{Enter}");
  expect(screen.getByRole("note")).toHaveTextContent("最近一次模型请求估算");
  await user.click(document.body);
  expect(screen.queryByRole("note")).not.toBeInTheDocument();
});
