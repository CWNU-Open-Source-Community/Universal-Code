import type { ReactElement } from "react";
import type { RenderOptions } from "@testing-library/react";
import { LocaleProvider } from "../../lib/locale";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { render as renderComponent, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import type { DesktopRuntimeRiskProfile } from "../../lib/desktop-bridge";
import { V2RuntimeCapabilityControl } from "./runtime-capability-control";

const desktopRuntime = vi.hoisted(() => ({
  current: vi.fn((): DesktopRuntimeRiskProfile | null => "safe"),
  enabled: vi.fn(() => true),
  restart: vi.fn(),
}));

vi.mock("../../lib/desktop-bridge", async () => {
  const actual = await vi.importActual<typeof import("../../lib/desktop-bridge")>(
    "../../lib/desktop-bridge");
  return {
    ...actual,
    desktopCurrentRiskProfile: desktopRuntime.current,
    desktopDebugRestartEnabled: desktopRuntime.enabled,
    restartDesktopInDebugMode: desktopRuntime.restart,
  };
});

function renderControl() {
  const queryClient = new QueryClient({ defaultOptions: {
    queries: { retry: false }, mutations: { retry: false },
  } });
  return render(<QueryClientProvider client={queryClient}>
    <V2RuntimeCapabilityControl />
  </QueryClientProvider>);
}

beforeEach(() => {
  desktopRuntime.current.mockReset();
  desktopRuntime.current.mockReturnValue("safe");
  desktopRuntime.enabled.mockReset();
  desktopRuntime.enabled.mockReturnValue(true);
  desktopRuntime.restart.mockReset();
});

describe("V2RuntimeCapabilityControl", () => {
  it("offers only the process-level Debug activation without a selected task", async () => {
    const user = userEvent.setup();
    renderControl();

    expect(screen.getByRole("heading", { name: "调试运行时" })).toBeInTheDocument();
    expect(screen.getByText("标准运行时")).toBeInTheDocument();
    expect(screen.getByText(/在当前任务开启完全访问时，先暂停执行并等待资源释放完成即可/u))
      .toBeInTheDocument();
    const debug = screen.getByRole("button", { name: "启用调试模式并重启" });
    expect(screen.queryByRole("button", { name: /启用完全访问并重启/u })).not.toBeInTheDocument();
    expect(debug).toBeEnabled();

    await user.click(debug);
    const dialog = screen.getByRole("dialog", { name: "要开启调试模式吗？" });
    expect(within(dialog).getByText(/任务已保存的权限会保留/u)).toBeInTheDocument();
    await user.click(within(dialog).getByRole("button", { name: "取消" }));

    expect(screen.queryByRole("dialog")).not.toBeInTheDocument();
    expect(debug).toHaveFocus();
    expect(desktopRuntime.restart).not.toHaveBeenCalled();
  });

  it.each([
    ["safe", "标准运行时", "启用调试模式并重启", false],
    ["debug", "调试已开启", "调试运行时已开启", true],
  ] as const)("shows the %s Debug runtime state", (profile, status,
    debugAction, disabled) => {
    desktopRuntime.current.mockReturnValue(profile);
    renderControl();

    expect(screen.getByText(status, { selector: ".v2-runtime-status" })).toBeInTheDocument();
    const debugButton = screen.getByRole("button", { name: debugAction });
    if (disabled) expect(debugButton).toBeDisabled();
    else expect(debugButton).toBeEnabled();
  });

  it("locks the dialog after the native bridge accepts a debug restart", async () => {
    desktopRuntime.restart.mockResolvedValue({
      protocol_version: "desktop_risk_restart.v1", profile: "debug",
      status: "restarting", restart_required: true, persistent_runtime_grant: false,
      arbitrary_arguments_accepted: false,
    });
    const user = userEvent.setup();
    renderControl();
    await user.click(screen.getByRole("button", { name: "启用调试模式并重启" }));
    const dialog = screen.getByRole("dialog", { name: "要开启调试模式吗？" });
    await user.click(within(dialog).getByRole("button", { name: "启用并重启" }));

    await waitFor(() => expect(desktopRuntime.restart).toHaveBeenCalledWith());
    expect(await within(dialog).findByRole("status")).toHaveTextContent("正在重启…");
    expect(within(dialog).getByRole("button", { name: "取消" })).toBeDisabled();
  });

  it("renders a Wails plain-object error and keeps the activation retryable", async () => {
    desktopRuntime.restart
      .mockRejectedValueOnce({ code: "restart_failed", message: "无法保存启动配置" })
      .mockResolvedValueOnce({
        protocol_version: "desktop_risk_restart.v1", profile: "debug",
        status: "cancelled", restart_required: true, persistent_runtime_grant: false,
        arbitrary_arguments_accepted: false,
      });
    const user = userEvent.setup();
    renderControl();
    const trigger = screen.getByRole("button", { name: "启用调试模式并重启" });
    await user.click(trigger);
    const dialog = screen.getByRole("dialog", { name: "要开启调试模式吗？" });
    const confirm = within(dialog).getByRole("button", { name: "启用并重启" });
    await user.click(confirm);

    expect(await within(dialog).findByRole("alert")).toHaveTextContent("无法保存启动配置");
    expect(confirm).toBeEnabled();
    await user.click(confirm);
    await waitFor(() => expect(desktopRuntime.restart).toHaveBeenCalledTimes(2));
    await waitFor(() => expect(screen.queryByRole("dialog")).not.toBeInTheDocument());
    expect(trigger).toHaveFocus();
  });

  it("does not invent activation controls without a verified desktop bootstrap", () => {
    desktopRuntime.current.mockReturnValue(null);
    desktopRuntime.enabled.mockReturnValue(false);
    renderControl();

    expect(screen.getByText("无法读取", { selector: ".v2-runtime-status" })).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "桌面会话不可用" })).toBeDisabled();
  });
});

function render(ui: ReactElement, options?: RenderOptions) {
  localStorage.setItem("prayu.locale.v1", "zh-CN");
  const Wrapper = options?.wrapper;
  return renderComponent(ui, {...options, wrapper: ({children}) => <LocaleProvider>{Wrapper ? <Wrapper>{children}</Wrapper> : children}</LocaleProvider>});
}
