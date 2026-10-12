import { render, screen } from "@testing-library/react";
import type { RunDetailView } from "../api/types";
import { ModelUsagePanel } from "./model-usage-panel";

const detail = (): RunDetailView => ({
  model_usage: { input_tokens: 100, output_tokens: 10, total_tokens: 110, cache_read_tokens: 40,
    cache_creation_tokens: 20, cache_creation_5m_tokens: 0, cache_creation_1h_tokens: 0,
    cache_usage_reported_calls: 1, calls: 2 },
  monetary_usage: { tracked: true, currency: "USD", cap_micros: 1_000_000, reserved_micros: 1000,
    settled_micros: 115, released_micros: 885, remaining_micros: 999885, unpriced_calls: 0 },
} as RunDetailView);

describe("ModelUsagePanel", () => {
  it("shows cached input as subsets and distinguishes unreported cache details and TTL", () => {
    render(<ModelUsagePanel detail={detail()} />);
    expect(screen.getByText("Input tokens including cache")).toBeInTheDocument();
    expect(screen.getByText("Creation TTL not reported")).toBeInTheDocument();
    expect(screen.getByText(/1 \/ 2 calls reported cache details/)).toBeInTheDocument();
    expect(screen.getByText("0.000115 USD")).toBeInTheDocument();
    expect(screen.getByText(/Amounts use the price snapshot pinned at dispatch/)).toBeInTheDocument();
  });
  it("explains unpriced estimates without claiming provider billing or missing prices are free", () => {
    const value = detail(); value.monetary_usage!.unpriced_calls = 1;
    render(<ModelUsagePanel detail={value} />);
    expect(screen.getByText(/1 calls are not fully priced; their reserved estimates are retained/)).toBeInTheDocument();
  });
  it("keeps historical details without new projections renderable", () => {
    const { container } = render(<ModelUsagePanel detail={{} as RunDetailView} />);
    expect(container).toBeEmptyDOMElement();
  });
});
