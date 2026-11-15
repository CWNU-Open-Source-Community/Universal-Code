import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import type { APIClient } from "../../api/client";
import { LocaleProvider } from "../../lib/locale";
import { V2AdvancedPermissionSettings } from "./permission-advanced-settings";

vi.mock("./execution-settings", () => ({ V2ExecutionSettings: () => null }));
vi.mock("./browser-network-control", () => ({ V2BrowserNetworkSettings: () => null }));
vi.mock("./run-network-authority-control", () => ({ V2RunNetworkAuthorityControl: () => null }));
vi.mock("./command-service-settings", () => ({ V2CommandServiceSettings: () => null }));
vi.mock("./runtime-capability-control", () => ({ V2RuntimeCapabilityControl: () => null }));

it("restores current-run CDP revocation in Advanced settings and confirms re-enabling", async () => {
  const browserPermission = (mode: string) => ({ mode, revision: 2, runtime_gate_available: true });
  const getThreadExecutionPermission = vi.fn().mockResolvedValue({ current_run_id: "run-cdp",
    current_run_mode: "full", current_run_synchronized: true,
    execution_permission: { mode: "full", full_activation: "active", runtime_gate_available: true } });
  const get = vi.fn().mockResolvedValue({ browser_cdp_permission: browserPermission("full_debug"),
    execution_permission: { mode: "full", full_activation: "active", runtime_gate_available: true } });
  const postControl = vi.fn().mockImplementation(async (_path, body) => ({ browser_cdp_permission: browserPermission(body.mode) }));
  const client = { hasBrowserCDPPermissionControl: true, hasFullCDPDebug: true,
    getThreadExecutionPermission, get, postControl } as unknown as APIClient;
  render(<LocaleProvider><QueryClientProvider client={new QueryClient({ defaultOptions: { queries: { retry: false } } })}>
    <V2AdvancedPermissionSettings client={client} threadID="thread-cdp" workspaces={[]} onOpenModelSettings={vi.fn()} />
  </QueryClientProvider></LocaleProvider>);
  expect(getThreadExecutionPermission).not.toHaveBeenCalled();
  expect(screen.queryByRole("switch", { name: "完整 CDP 控制" })).not.toBeInTheDocument();
  const user = userEvent.setup();
  await user.click(screen.getByText("高级设置", { selector: "summary" }));
  const toggle = await screen.findByRole("switch", { name: "完整 CDP 控制" });
  await waitFor(() => expect(toggle).toHaveAttribute("aria-checked", "true"));
  await user.click(toggle);
  expect(postControl).toHaveBeenCalledWith("/runs/run-cdp/browser-cdp-permission",
    { mode: "restricted", reason: "v2 current task Full CDP control" }, expect.any(String));
  await waitFor(() => expect(toggle).toHaveAttribute("aria-checked", "false"));
  await user.click(toggle);
  expect(postControl).toHaveBeenCalledTimes(1);
  await user.click(within(screen.getByRole("dialog", { name: "开启完整 CDP 控制？" })).getByRole("button", { name: "开启完整 CDP" }));
  await waitFor(() => expect(postControl).toHaveBeenCalledWith("/runs/run-cdp/browser-cdp-permission",
    { mode: "full_debug", confirm_full_cdp_debug: true, reason: "v2 current task Full CDP control" }, expect.any(String)));
});
