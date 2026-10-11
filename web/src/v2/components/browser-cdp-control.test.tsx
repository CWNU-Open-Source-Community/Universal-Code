import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import type { APIClient } from "../../api/client";
import type { RunBrowserCDPPermissionView, ThreadExecutionPermissionView } from "../../api/types";
import { V2BrowserCDPControl } from "./browser-cdp-control";

function permission(mode: RunBrowserCDPPermissionView["mode"]): RunBrowserCDPPermissionView {
  const full = mode === "full_debug";
  return {
    arbitrary_method_allowed: full,
    browser_start_authorized: false,
    capability_grant: false,
    cookie_access_allowed: full,
    created_at: "2026-08-30T00:00:00Z",
    dom_snapshot_allowed: true,
    mode,
    navigate_allowed: true,
    operator_confirmed: full,
    policy_version: "browser_cdp_permission_policy.v1",
    protocol_version: "run_browser_cdp_permission.v1",
    request_capture_allowed: full,
    request_mutation_allowed: full,
    request_replay_allowed: full,
    required_gate: full ? "full_cdp_debug" : "browser_cdp_control",
    revision: full ? 2 : 1,
    risk_tier: full ? "high" : "minimal",
    runtime: { control_enabled: true, execution_debug_selected: full, full_debug_enabled: true },
    runtime_authorized: false,
    runtime_gate_available: true,
    screenshot_allowed: true,
    transport_enabled: false,
  };
}

function renderControl({ initial = permission("restricted"), runID = "run-1",
  mode = "full", executionRuntimeAvailable = true, sessionControlEnabled = true }: {
  initial?: RunBrowserCDPPermissionView;
  runID?: string;
  mode?: ThreadExecutionPermissionView["mode"] | null;
  executionRuntimeAvailable?: boolean;
  sessionControlEnabled?: boolean;
} = {}) {
  const get = vi.fn().mockResolvedValue({
    browser_cdp_permission: initial,
    execution_permission: { revision: 4 },
  });
  const postControl = vi.fn().mockImplementation((_path, body: { mode: "restricted" | "full_debug" }) =>
    Promise.resolve({ browser_cdp_permission: permission(body.mode), replayed: false }));
  const getFullCDPSession = vi.fn().mockResolvedValue({ replayed: false, session: {
    version: "full_cdp_session.v1", run_id: runID, state: "none", runtime_available: true,
    cdp_closed: false, process_tree_quiescent: false, profile_released: false,
    profile_cleaned: false,
  } });
  const openFullCDPSession = vi.fn().mockResolvedValue({ replayed: false, session: {
    version: "full_cdp_session.v1", run_id: runID, state: "ready", runtime_available: true,
    session_id: "full-cdp-session-1", target_origin: "http://127.0.0.1:3000",
    browser: { product: "edge", channel: "stable" },
    cdp_closed: false, process_tree_quiescent: false, profile_released: false,
    profile_cleaned: false,
  } });
  const closeFullCDPSession = vi.fn();
  const client = { hasBrowserCDPPermissionControl: true, hasFullCDPDebug: true,
    hasFullCDPSessionControl: sessionControlEnabled,
    get, postControl, getFullCDPSession, openFullCDPSession, closeFullCDPSession,
  } as unknown as APIClient;
  const queryClient = new QueryClient({ defaultOptions: {
    queries: { retry: false }, mutations: { retry: false },
  } });
  render(<QueryClientProvider client={queryClient}>
    <V2BrowserCDPControl client={client} executionRuntimeAvailable={executionRuntimeAvailable}
      permissionMode={mode} runID={runID} />
  </QueryClientProvider>);
  return { get, postControl, getFullCDPSession, openFullCDPSession, closeFullCDPSession };
}

describe("V2BrowserCDPControl", () => {
  it("turns an included Full CDP grant off immediately without confirmation", async () => {
    const user = userEvent.setup();
    const controls = renderControl({ initial: permission("full_debug") });
    const toggle = await screen.findByRole("switch", { name: "完整 CDP 控制" });
    await waitFor(() => expect(toggle).toHaveAttribute("aria-checked", "true"));
    expect(screen.getByText(/可为当前任务打开、查看和关闭独立浏览器/u)).toBeInTheDocument();

    await user.click(toggle);

    expect(screen.queryByRole("dialog")).not.toBeInTheDocument();
    await waitFor(() => expect(controls.postControl).toHaveBeenCalledWith(
      "/runs/run-1/browser-cdp-permission",
      { mode: "restricted", reason: "v2 current task Full CDP control" },
      expect.stringMatching(/^v2-browser-cdp-/u),
    ));
    await waitFor(() => expect(toggle).toHaveAttribute("aria-checked", "false"));
  });

  it("requires a risk confirmation before turning Full CDP back on", async () => {
    const user = userEvent.setup();
    const controls = renderControl();
    const toggle = await screen.findByRole("switch", { name: "完整 CDP 控制" });

    await user.click(toggle);

    expect(controls.postControl).not.toHaveBeenCalled();
    const dialog = screen.getByRole("dialog", { name: "开启完整 CDP 控制？" });
    expect(within(dialog).getByText(/读取 Cookie、捕获和修改网络请求/u)).toBeInTheDocument();
    expect(within(dialog).getByText(/控制范围为 Universal-Code 管理的隔离浏览器/u)).toBeInTheDocument();
    expect(within(dialog).getByText(/启用后可在“应用预览”中打开独立浏览器/u)).toBeInTheDocument();
    await user.click(within(dialog).getByRole("button", { name: "开启完整 CDP" }));

    await waitFor(() => expect(controls.postControl).toHaveBeenCalledWith(
      "/runs/run-1/browser-cdp-permission",
      { mode: "full_debug", reason: "v2 current task Full CDP control",
        confirm_full_cdp_debug: true },
      expect.stringMatching(/^v2-browser-cdp-/u),
    ));
    await waitFor(() => expect(toggle).toHaveAttribute("aria-checked", "true"));
  });

  it("cannot be enabled below Full Access", async () => {
    renderControl({ mode: "ask" });

    const toggle = await screen.findByRole("switch", { name: "完整 CDP 控制" });
    expect(toggle).toBeDisabled();
    expect(toggle).toHaveAttribute("aria-checked", "false");
    expect(screen.getByText(/高风险 CDP 需要在当前任务确认并激活完全访问/u)).toBeInTheDocument();
    expect(screen.getByText(/受限导航、DOM 与截图不受影响/u)).toBeInTheDocument();
  });

  it("explains missing managed-browser support while preserving permission activation", async () => {
    const user = userEvent.setup();
    const controls = renderControl({ sessionControlEnabled: false });
    await user.click(await screen.findByRole("switch", { name: "完整 CDP 控制" }));
    const dialog = screen.getByRole("dialog", { name: "开启完整 CDP 控制？" });
    expect(within(dialog).getByText(/当前连接尚未开放托管浏览器/u)).toBeVisible();
    expect(within(dialog).queryByText(/启用后可在“应用预览”中打开独立浏览器/u)).not.toBeInTheDocument();
    await user.click(within(dialog).getByRole("button", { name: "开启完整 CDP" }));
    await waitFor(() => expect(controls.postControl).toHaveBeenCalledTimes(1));
    expect(screen.queryByRole("button", { name: /启动会话/u })).not.toBeInTheDocument();
    expect(controls.getFullCDPSession).not.toHaveBeenCalled();
    expect(controls.openFullCDPSession).not.toHaveBeenCalled();
  });

  it("opens a revision-bound managed browser only after per-session confirmation", async () => {
    const user = userEvent.setup();
    const controls = renderControl({ initial: permission("full_debug") });
    const launch = await screen.findByRole("button", { name: /启动会话/u });

    await user.click(launch);
    expect(controls.openFullCDPSession).not.toHaveBeenCalled();
    const dialog = screen.getByRole("dialog", { name: "启动完整 CDP 会话？" });
    expect(within(dialog).getByText(/临时 Profile/u)).toBeInTheDocument();
    await user.click(within(dialog).getByRole("button", { name: "启动隔离浏览器" }));

    await waitFor(() => expect(controls.openFullCDPSession).toHaveBeenCalledWith("run-1", {
      version: "full_cdp_session.v1",
      target: "http://127.0.0.1:3000",
      browser: { product: "edge", channel: "stable" },
      expected_execution_permission_revision: 4,
      expected_browser_cdp_permission_revision: 2,
      confirm_full_cdp: true,
      reason: "v2 operator-confirmed task browser session",
    }, expect.stringMatching(/^v2-full-cdp-open-/u)));
    expect(await screen.findByText("已就绪")).toBeInTheDocument();
  });

  it("requires the current task live authority to enable but still permits downgrade", async () => {
    const user = userEvent.setup();
    const unavailable = renderControl({ executionRuntimeAvailable: false });
    const disabledToggle = await screen.findByRole("switch", { name: "完整 CDP 控制" });
    await screen.findByText("先重新确认并激活当前任务的完全访问权限。");
    expect(disabledToggle).toBeDisabled();
    expect(unavailable.postControl).not.toHaveBeenCalled();

    const controls = renderControl({
      executionRuntimeAvailable: false,
      initial: permission("full_debug"),
      runID: "run-2",
    });
    const toggles = await screen.findAllByRole("switch", { name: "完整 CDP 控制" });
    await user.click(toggles[1]!);
    await waitFor(() => expect(controls.postControl).toHaveBeenCalledWith(
      "/runs/run-2/browser-cdp-permission",
      { mode: "restricted", reason: "v2 current task Full CDP control" },
      expect.stringMatching(/^v2-browser-cdp-/u),
    ));
  });

  it("stays visible but unavailable without a current Run", () => {
    const controls = renderControl({ runID: "", mode: null });

    expect(screen.getByRole("switch", { name: "完整 CDP 控制" })).toBeDisabled();
    expect(screen.getByText("先打开一个任务。")).toBeInTheDocument();
    expect(controls.get).not.toHaveBeenCalled();
  });
});
