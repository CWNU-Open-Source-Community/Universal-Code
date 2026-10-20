import type { ReactElement } from "react";
import type { RenderOptions } from "@testing-library/react";
import { LocaleProvider } from "../../lib/locale";
import { render as renderComponent, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import type { APIClient } from "../../api/client";
import { APIRequestError } from "../../api/client";
import type { ProviderSearchReadinessView, SearchDiagnosticsView } from "../../api/types";
import { V2SearchDiagnosticsControl } from "./search-diagnostics-control";

const readiness: ProviderSearchReadinessView = {
  protocol_version: "provider_search_readiness.v1", thread_id: "thread-1", run_id: "run-1",
  mode_revision: 3, model_route: "provider/model", provider: "provider", model: "model",
  search_policy: "provider_native", state: "ready", reason: "search_backend_ready",
  remediation: "none", network_mode: "allowlist", runtime_ready: true,
  capability_grant: false,
};

function diagnostic(overrides: Partial<SearchDiagnosticsView> = {}): SearchDiagnosticsView {
  return {
    protocol_version: "search_diagnostics.v1", thread_id: "thread-1", run_id: "run-1",
    mode_revision: 3, model_route: "provider/model", provider: "provider", model: "model",
    search_policy: "provider_native", backend: "provider", checked_at: "2026-09-21T08:00:00Z",
    state: "succeeded", code: "none", result_count: 3, network_request_attempted: true,
    ...overrides,
  };
}

function clientWith(diagnoseThreadSearch: APIClient["diagnoseThreadSearch"]): APIClient {
  return { hasControl: true, diagnoseThreadSearch } as unknown as APIClient;
}

describe("V2SearchDiagnosticsControl", () => {
  it("guides a busy task to pause or finish before retrying the connection check", async () => {
    const diagnoseThreadSearch = vi.fn().mockRejectedValue(new APIRequestError("Run has an active lease", "CONFLICT", 409));
    render(<V2SearchDiagnosticsControl client={clientWith(diagnoseThreadSearch)} readiness={readiness} />);
    await userEvent.setup().click(screen.getByRole("button", { name: "检查搜索连接" }));
    expect(await screen.findByRole("alert")).toHaveTextContent("暂停任务或等待完成");
    expect(screen.getByRole("button", { name: "重新检查搜索连接" })).toBeEnabled();
    expect(diagnoseThreadSearch).toHaveBeenCalledTimes(1);
  });
  it("runs one explicit bounded probe and shows its exact model, backend, time, and result count", async () => {
    const user = userEvent.setup();
    let resolve!: (value: SearchDiagnosticsView) => void;
    const diagnoseThreadSearch = vi.fn().mockImplementation(() =>
      new Promise<SearchDiagnosticsView>((accept) => { resolve = accept; }));
    render(<V2SearchDiagnosticsControl client={clientWith(diagnoseThreadSearch)}
      readiness={readiness} />);

    expect(screen.getByText("当前 Run 模型：provider / model")).toBeInTheDocument();
    expect(screen.getByText(/不检测专业来源连接器/u)).toBeInTheDocument();
    expect(diagnoseThreadSearch).not.toHaveBeenCalled();

    const button = screen.getByRole("button", { name: "检查搜索连接" });
    await user.click(button);
    expect(button).toBeDisabled();
    expect(diagnoseThreadSearch).toHaveBeenCalledExactlyOnceWith("thread-1", {
      version: "search_diagnostics.v1", confirm: true,
    }, expect.any(AbortSignal));

    resolve(diagnostic());
    expect(await screen.findByText("真实探针通过")).toBeInTheDocument();
    expect(screen.getByText("provider / model")).toBeInTheDocument();
    expect(screen.getByText("3")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "重新检查搜索连接" })).toBeEnabled();
  });

  it("shows rate-limit recovery metadata without opening model settings", async () => {
    const user = userEvent.setup();
    const diagnoseThreadSearch = vi.fn().mockResolvedValue(diagnostic({
      state: "failed", code: "rate_limited", result_count: 0, http_status: 429,
      retry_after: "60", rate_limit_reset: "2026-09-21T08:01:00Z",
    }));
    render(<V2SearchDiagnosticsControl client={clientWith(diagnoseThreadSearch)}
      readiness={readiness} onOpenModelSettings={vi.fn()} />);

    await user.click(screen.getByRole("button", { name: "检查搜索连接" }));

    expect(await screen.findByText(/正在限流/u)).toBeInTheDocument();
    expect(screen.getByText("429")).toBeInTheDocument();
    expect(screen.getByText("60")).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "打开模型设置" })).not.toBeInTheDocument();
  });

  it("offers model settings after an access challenge and never retries automatically", async () => {
    const user = userEvent.setup();
    const onOpenModelSettings = vi.fn();
    const diagnoseThreadSearch = vi.fn().mockResolvedValue(diagnostic({
      state: "failed", code: "access_challenge", result_count: 0, http_status: 403,
    }));
    render(<V2SearchDiagnosticsControl client={clientWith(diagnoseThreadSearch)}
      readiness={readiness} onOpenModelSettings={onOpenModelSettings} />);

    await user.click(screen.getByRole("button", { name: "检查搜索连接" }));
    expect(await screen.findByText(/验证码或访问挑战/u)).toBeInTheDocument();
    expect(diagnoseThreadSearch).toHaveBeenCalledTimes(1);
    await user.click(screen.getByRole("button", { name: "打开模型设置" }));
    expect(onOpenModelSettings).toHaveBeenCalledTimes(1);
    expect(diagnoseThreadSearch).toHaveBeenCalledTimes(1);
  });

  it("aborts an in-flight probe and discards it when the Run binding changes", async () => {
    const signals: AbortSignal[] = [];
    const diagnoseThreadSearch = vi.fn().mockImplementation((_thread, _body, signal: AbortSignal) => {
      signals.push(signal);
      return new Promise<SearchDiagnosticsView>(() => undefined);
    });
    const client = clientWith(diagnoseThreadSearch);
    const user = userEvent.setup();
    const view = render(<V2SearchDiagnosticsControl client={client} readiness={readiness} />);
    await user.click(screen.getByRole("button", { name: "检查搜索连接" }));
    expect(signals[0]?.aborted).toBe(false);

    view.rerender(<V2SearchDiagnosticsControl client={client} readiness={{
      ...readiness, run_id: "run-2", mode_revision: 4,
    }} />);

    await waitFor(() => expect(signals[0]?.aborted).toBe(true));
    expect(screen.getByRole("button", { name: "检查搜索连接" })).toBeEnabled();
    expect(screen.queryByText("真实探针通过")).not.toBeInTheDocument();
  });
});

function render(ui: ReactElement, options?: RenderOptions) {
  localStorage.setItem("prayu.locale.v1", "zh-CN");
  const Wrapper = options?.wrapper;
  return renderComponent(ui, {...options, wrapper: ({children}) => <LocaleProvider>{Wrapper ? <Wrapper>{children}</Wrapper> : children}</LocaleProvider>});
}
