import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import type { APIClient } from "../../api/client";
import type { ProviderSearchReadinessView, RunDetailView } from "../../api/types";
import { V2RunNetworkAuthorityControl } from "./run-network-authority-control";
import { browserCDPQueryKey } from "./browser-cdp-control";

function renderControl(status: "created" | "paused" | "running" = "paused",
  variant: "menu" | "settings" = "settings",
  readiness: Partial<ProviderSearchReadinessView> = {},
  permissionMode: RunDetailView["execution_permission"]["mode"] = "ask",
  onOpenModelSettings = vi.fn(),
  networkScope: RunDetailView["mode"]["scope"] = { network_mode: "allowlist", allowed_targets: ["search.example.org"],
    web_access_mode: "rules", web_allowed_targets: ["search.example.org"] }) {
  const mode = {
    protocol_version: "run_mode.v1", policy_version: "mode_policy.v1",
    revision: 3, capability_grant: false, phase: "deliver", profile: "code",
    surface: "code", reason: "test", requested_by: "test",
    created_at: "2026-08-31T00:00:00Z",
    scope: networkScope,
  };
  const get = vi.fn().mockResolvedValue({ run: { status }, mode,
    execution_permission: { mode: permissionMode } });
  const setRunWebAccess = vi.fn().mockImplementation(async (_runID, request) => ({
    version: "run_web_access_control.v1", run_id: "run-1", replayed: false,
    mode: { ...mode, revision: 4, scope: { ...networkScope,
      web_access_mode: request.web_access_mode, web_allowed_targets: request.web_allowed_targets,
    } },
  }));
  const providerSearchReadiness = vi.fn().mockResolvedValue({
    protocol_version: "provider_search_readiness.v1", thread_id: "thread-1", run_id: "run-1",
    model_route: "provider/model", provider: "provider", model: "model",
    search_policy: "provider_native", state: "ready", reason: "search_backend_ready",
    remediation: "none", required_target: "search.example.org", network_mode: "allowlist",
    mode_revision: 3, runtime_ready: true, capability_grant: false, ...readiness,
  });
  const diagnoseThreadSearch = vi.fn().mockResolvedValue({
    protocol_version: "search_diagnostics.v1", thread_id: "thread-1", run_id: "run-1",
    mode_revision: 3, model_route: "provider/model", provider: "provider", model: "model",
    search_policy: "provider_native", backend: "provider", checked_at: "2026-09-21T08:00:00Z",
    state: "failed", code: "access_challenge", result_count: 0,
    network_request_attempted: true, http_status: 403,
  });
  const client = { hasControl: true, get, setRunWebAccess,
    providerSearchReadiness, diagnoseThreadSearch } as unknown as APIClient;
  const queryClient = new QueryClient({ defaultOptions: {
    queries: { retry: false }, mutations: { retry: false },
  } });
  render(<QueryClientProvider client={queryClient}>
    <V2RunNetworkAuthorityControl client={client} runID="run-1" threadID="thread-1"
      variant={variant} onOpenModelSettings={onOpenModelSettings} />
  </QueryClientProvider>);
  return { diagnoseThreadSearch, setRunWebAccess, onOpenModelSettings, get, queryClient };
}

describe("V2RunNetworkAuthorityControl", () => {
  it("shows the exact current Run network scope from the Composer chip", async () => {
    const user = userEvent.setup();
    renderControl("paused", "menu");

    const trigger = screen.getByRole("button", { name: "网页访问状态" });
    await waitFor(() => expect(trigger).toHaveTextContent("规则搜索"));
    await user.click(trigger);

    const popover = screen.getByRole("dialog", { name: "当前执行网页访问" });
    expect(within(popover).getByText("当前任务网页访问")).toBeInTheDocument();
    expect(await within(popover).findByText(/供应商搜索 · 搜索配置就绪/u)).toBeInTheDocument();
    expect(within(popover).getByText("search.example.org")).toBeInTheDocument();
    expect(within(popover).getByText(/搜索使用供应商接口/u)).toBeInTheDocument();
    expect(within(popover).getByText(/当前任务及其后续执行/u)).toBeInTheDocument();
  });

  it.each(["ask", "auto", "full", "full_access", "debug"] as const)(
    "keeps the exact URL scope visible for %s without inferring a broader grant", async (permissionMode) => {
    const user = userEvent.setup();
    const controls = renderControl("paused", "menu", {}, permissionMode);

    const trigger = screen.getByRole("button", { name: "网页访问状态" });
    await waitFor(() => expect(trigger).toHaveTextContent("规则搜索"));
    await user.click(trigger);

    const popover = screen.getByRole("dialog", { name: "当前执行网页访问" });
    expect(within(popover).getByText("search.example.org")).toBeInTheDocument();
    expect(within(popover).getByRole("combobox", { name: "当前任务网页搜索模式" })).toHaveValue("rules");
    expect(within(popover).queryByText("公网 HTTPS")).not.toBeInTheDocument();
    expect(within(popover).getByRole("textbox", { name: "追加允许的 HTTPS 主机" })).toBeEnabled();
    expect(controls.setRunWebAccess).not.toHaveBeenCalled();
  });

  it("reads a saved broad network scope without treating it as a permission preference", async () => {
    const user = userEvent.setup();
    renderControl("running", "menu", {}, "ask", vi.fn(), {
      network_mode: "allowlist", allowed_targets: ["public_https"],
    // An older server projection omits the new fields; retain this fallback test.
    } as RunDetailView["mode"]["scope"]);

    const trigger = screen.getByRole("button", { name: "网页访问状态" });
    await waitFor(() => expect(trigger).toHaveTextContent("完全访问"));
    await user.click(trigger);

    const popover = screen.getByRole("dialog", { name: "当前执行网页访问" });
    expect(within(popover).getByRole("combobox", { name: "当前任务网页搜索模式" })).toHaveValue("full");
    expect(within(popover).getByText(/直接读取公网 HTTPS 网页/u)).toBeInTheDocument();
    expect(within(popover).queryByRole("textbox", { name: "追加允许的 HTTPS 主机" }))
      .not.toBeInTheDocument();
  });

  it("does not claim broad access or a disconnected provider when the URL scope is disabled", async () => {
    const controls = renderControl("paused", "settings", {}, "full", vi.fn(), {
      network_mode: "disabled", allowed_targets: [], web_access_mode: "rules", web_allowed_targets: [],
    });
    expect(await screen.findByRole("combobox", { name: "当前任务网页搜索模式" })).toHaveValue("rules");
    expect(await screen.findByText("供应商搜索 · 搜索配置就绪")).toBeInTheDocument();
    expect(screen.queryByText("公网 HTTPS")).not.toBeInTheDocument();
    expect(screen.getByRole("textbox", { name: "追加允许的 HTTPS 主机" })).toBeEnabled();
    expect(controls.setRunWebAccess).not.toHaveBeenCalled();
  });

  it("explains and prefills the exact missing Provider target", async () => {
    const user = userEvent.setup();
    renderControl("paused", "menu", {
      state: "missing_allowlist", reason: "search_endpoint_not_allowlisted",
      remediation: "add_required_target", required_target: "api.provider.com",
      runtime_ready: false,
    });

    const trigger = screen.getByRole("button", { name: "网页访问状态" });
    await waitFor(() => expect(trigger).toHaveTextContent("规则搜索"));
    await user.click(trigger);
    await user.click(screen.getByRole("button", { name: /使用所需主机/u }));
    expect(screen.getByRole("textbox", { name: "追加允许的 HTTPS 主机" }))
      .toHaveValue("api.provider.com");
  });

  it("points disabled-search remediation to the web mode selector", async () => {
    renderControl("paused", "settings", {
      state: "network_disabled", reason: "run_network_disabled", remediation: "enable_network_allowlist",
    });
    expect(await screen.findByText("选择规则搜索或完全访问以启用网页搜索")).toBeVisible();
    expect(screen.getByText(/选择规则搜索或完全访问后，可使用已配置的搜索服务/u)).toBeVisible();
    expect(screen.queryByText("追加搜索后端主机后即可启用")).not.toBeInTheDocument();
  });

  it("identifies DuckDuckGo configuration readiness without claiming a completed search or changing authority", async () => {
    const user = userEvent.setup();
    const controls = renderControl("running", "menu", {
      search_policy: "web", required_target: "html.duckduckgo.com",
    }, "full");
    const trigger = screen.getByRole("button", { name: "网页访问状态" });
    await waitFor(() => expect(trigger).toHaveTextContent("规则搜索"));
    await user.click(trigger);
    expect(screen.getByText(/普通网页搜索（DuckDuckGo）的配置与网络授权已就绪/u))
      .toHaveTextContent("实际搜索结果以本次工具返回为准");
    expect(screen.getByText("网页搜索 · 搜索配置就绪")).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: /使用所需主机/u })).not.toBeInTheDocument();
    expect(controls.setRunWebAccess).not.toHaveBeenCalled();
  });

  it("distinguishes a native-search transport timeout from model chat readiness", async () => {
    const user = userEvent.setup();
    renderControl("paused", "menu", {
      state: "provider_unavailable", reason: "provider_native_qualification_failed",
      remediation: "qualify_provider_search", detail_code: "transport_unavailable",
      runtime_ready: false,
    });

    const trigger = screen.getByRole("button", { name: "网页访问状态" });
    await waitFor(() => expect(trigger).toHaveTextContent("规则搜索"));
    await user.click(trigger);
    expect(screen.getByText(/连接或超时边界内没有完成/u)).toBeInTheDocument();
    expect(screen.getByText(/普通对话仍可继续/u)).toBeInTheDocument();
  });

  it("integrates the explicit search probe and model-settings recovery action", async () => {
    const user = userEvent.setup();
    const controls = renderControl();
    await screen.findByText("当前 Run 模型：provider / model");

    await user.click(screen.getByRole("button", { name: "检查搜索连接" }));
    expect(await screen.findByText(/验证码或访问挑战/u)).toBeInTheDocument();
    expect(controls.diagnoseThreadSearch).toHaveBeenCalledTimes(1);
    await user.click(screen.getByRole("button", { name: "打开模型设置" }));
    expect(controls.onOpenModelSettings).toHaveBeenCalledTimes(1);
  });

  it("requires a second confirmation and submits an exact revision-bound host grant", async () => {
    const user = userEvent.setup();
    const controls = renderControl();
    const input = await screen.findByRole("textbox", { name: "追加允许的 HTTPS 主机" });
    await waitFor(() => expect(input).toBeEnabled());
    await user.type(input, "HTTPS://Docs.Example.COM:443/\ndocs.example.com");
    expect(input).toHaveValue("HTTPS://Docs.Example.COM:443/\ndocs.example.com");
    const submit = screen.getByRole("button", { name: "审核并追加" });
    await waitFor(() => expect(submit).toBeEnabled());
    await user.click(submit);

    expect(controls.setRunWebAccess).not.toHaveBeenCalled();
    const dialog = screen.getByRole("dialog", { name: "追加网页访问范围？" });
    await user.click(within(dialog).getByRole("button", { name: "允许这些主机" }));

    await waitFor(() => expect(controls.setRunWebAccess).toHaveBeenCalledWith(
      "run-1", {
        version: "run_web_access_control.v1",
        expected_mode_revision: 3,
        web_access_mode: "rules",
        web_allowed_targets: ["docs.example.com", "search.example.org"],
        reason: "v2 operator-selected web access",
      }, expect.stringMatching(/^v2-run-web-access-/u),
    ));
    expect(await screen.findByText("docs.example.com")).toBeInTheDocument();
  });

  it("refuses mutation outside a created or paused safe boundary", async () => {
    renderControl("running");
    const input = await screen.findByRole("textbox", { name: "追加允许的 HTTPS 主机" });
    await waitFor(() => expect(input).toBeDisabled());
    expect(screen.getByRole("combobox", { name: "当前任务网页搜索模式" })).toBeDisabled();
    expect(await screen.findByText(/暂停执行并等待资源释放后可修改/u)).toBeInTheDocument();
  });

  it("disables search controls and webpage rules for an explicit disabled mode", async () => {
    const controls = renderControl("paused", "settings", {}, "full", vi.fn(), {
      network_mode: "allowlist", allowed_targets: ["command.example.org"],
      web_access_mode: "disabled", web_allowed_targets: [],
    });
    await waitFor(() => expect(screen.getByRole("combobox", { name: "当前任务网页搜索模式" })).toHaveValue("disabled"));
    expect(screen.getByText("关闭新的网页搜索与抓取，已保存的网页仍可阅读。")).toBeVisible();
    expect(screen.queryByRole("button", { name: "检查搜索连接" })).not.toBeInTheDocument();
    expect(screen.queryByRole("textbox")).not.toBeInTheDocument();
    expect(controls.setRunWebAccess).not.toHaveBeenCalled();
  });

  it.each(["disabled", "full"] as const)("sets %s through the versioned web endpoint without granting command networking", async (mode) => {
    const controls = renderControl("paused", "settings", {}, "ask", vi.fn(), {
      network_mode: "disabled", allowed_targets: [], web_access_mode: "rules",
      web_allowed_targets: ["docs.example.org"],
    });
    const select = await screen.findByRole("combobox", { name: "当前任务网页搜索模式" });
    await waitFor(() => expect(select).toBeEnabled());
    await userEvent.selectOptions(select, mode);
    await waitFor(() => expect(controls.setRunWebAccess).toHaveBeenCalledExactlyOnceWith("run-1", {
      version: "run_web_access_control.v1", expected_mode_revision: 3,
      web_access_mode: mode, web_allowed_targets: [], reason: "v2 operator-selected web access",
    }, expect.stringMatching(/^v2-run-web-access-/u)));
    await waitFor(() => expect(select).toHaveValue(mode));
    expect(controls.queryClient.getQueryData<RunDetailView>(browserCDPQueryKey("run-1"))?.mode.scope)
      .toMatchObject({ network_mode: "disabled", allowed_targets: [], web_access_mode: mode });
    expect(screen.queryByRole("textbox")).not.toBeInTheDocument();
    expect(screen.queryByRole("dialog")).not.toBeInTheDocument();
  });

  it("leaves the saved mode unchanged when the server rejects a stale update", async () => {
    const controls = renderControl();
    controls.setRunWebAccess.mockRejectedValueOnce(new Error("网页访问设置已更新，请重新读取后重试。"));
    const select = await screen.findByRole("combobox", { name: "当前任务网页搜索模式" });
    await waitFor(() => expect(select).toBeEnabled());
    await userEvent.selectOptions(select, "full");
    expect(await screen.findByRole("alert")).toHaveTextContent("请重新读取后重试");
    expect(select).toHaveValue("rules");
  });

  it("rejects broad and path-bearing targets before confirmation", async () => {
    const user = userEvent.setup();
    renderControl();
    const input = await screen.findByRole("textbox", { name: "追加允许的 HTTPS 主机" });
    await waitFor(() => expect(input).toBeEnabled());
    await user.type(input, "*.example.com\nhttps://example.net/path\nlocalhost\n10.0.0.1");
    expect(input).toHaveValue("*.example.com\nhttps://example.net/path\nlocalhost\n10.0.0.1");
    expect(await screen.findByRole("alert")).toHaveTextContent("只接受无路径、查询或通配符");
    expect(screen.getByRole("button", { name: "审核并追加" })).toBeDisabled();
  });

  it("returns focus to the Composer chip when Escape closes its popover", async () => {
    const user = userEvent.setup();
    renderControl("paused", "menu");
    const trigger = screen.getByRole("button", { name: "网页访问状态" });
    await waitFor(() => expect(trigger).toHaveTextContent("规则搜索"));
    await user.click(trigger);
    expect(screen.getByRole("dialog", { name: "当前执行网页访问" })).toBeInTheDocument();

    await user.keyboard("{Escape}");

    expect(screen.queryByRole("dialog", { name: "当前执行网页访问" }))
      .not.toBeInTheDocument();
    await waitFor(() => expect(trigger).toHaveFocus());
  });
});
