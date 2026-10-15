import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { expect, it, vi } from "vitest";
import { APIClient } from "../api/client";
import { LocaleProvider } from "../lib/locale";
import { hookDiagnostics, pluginHistory } from "../test/plugin-lifecycle-fixtures";
import { PluginLifecycleControls, HookDiagnostics } from "./plugin-lifecycle";
import { ExtensionSettings } from "./shared-settings-panels";

function mount(children: React.ReactNode) {
  window.localStorage.setItem("prayu.locale.v1", "zh-CN");
  const cache = new QueryClient({ defaultOptions: { queries: { retry: false }, mutations: { retry: false } } });
  return render(<QueryClientProvider client={cache}><LocaleProvider>{children}</LocaleProvider></QueryClientProvider>);
}
async function chooseRollback(user: ReturnType<typeof userEvent.setup>) {
  await user.click(screen.getByText("版本与发布者"));
  await user.selectOptions(await screen.findByRole("combobox", { name: "回退到" }), "plugin-old");
  await user.click(within(screen.getByRole("group", { name: "回退版本的能力" })).getByRole("checkbox", { name: "hooks" }));
  await user.click(screen.getByRole("checkbox", { name: "我已核对两个版本和所选能力，允许加载所选版本" }));
}

it("mounts lifecycle and read-only Hook diagnostics from actual backend capabilities in the production settings surface", async () => {
  const history = pluginHistory();
  const pluginHistoryRead = vi.fn().mockResolvedValue(history), hookRead = vi.fn().mockResolvedValue(hookDiagnostics());
  const client = { hasExtensionControl: false, pluginHistory: pluginHistoryRead, hookDiagnostics: hookRead,
    extensionInventory: vi.fn().mockResolvedValue({ protocol_version: "extension-inventory.v1", run_id: "run-one", workspace_id: "project-one", mcp_servers: [], mcp_calls: [], plugins: [history.installations[0]],
      onboarding: { mcp_registration: false, plugin_import: false, lsp_configuration: false, plugin_lifecycle: true, hook_diagnostics: true } }),
    codeIntelInventory: vi.fn().mockResolvedValue({ protocol_version: "code-intel-lsp.v1", enabled: true, qualifications: [], servers: [], configurations: [] }) } as unknown as APIClient;
  const user = userEvent.setup();
  mount(<ExtensionSettings client={client} selectedRunID="run-one" />);
  await screen.findByText("Hooks 声明与触发记录");
  expect(pluginHistoryRead).not.toHaveBeenCalled(); expect(hookRead).not.toHaveBeenCalled();
  await user.click(screen.getByText("版本与发布者"));
  await screen.findByRole("list", { name: "安装版本记录" });
  expect(await screen.findByRole("combobox", { name: "回退到" })).toBeDisabled();
  await user.click(screen.getByText("Hooks 声明与触发记录"));
  await screen.findByText("历史记录缺少决定");
  expect(hookRead).toHaveBeenCalledWith("run-one", "project-one", expect.any(AbortSignal));
  expect(screen.getByText("已拒绝操作")).toBeInTheDocument();
  await user.click(screen.getByRole("button", { name: "刷新 Hooks 记录" }));
  await waitFor(() => expect(hookRead).toHaveBeenCalledTimes(2));
});

it("pins both versions, clears confirmation on capability edits, and holds reads while rollback is pending", async () => {
  const user = userEvent.setup();
  let resolve!: (value: unknown) => void;
  const rollback = vi.fn().mockImplementation(() => new Promise((done) => { resolve = done; }));
  const historyRead = vi.fn().mockResolvedValue(pluginHistory());
  mount(<PluginLifecycleControls client={{ hasExtensionControl: true, pluginHistory: historyRead, rollbackPluginInstallation: rollback } as unknown as APIClient} installationID="plugin-current" />);
  await chooseRollback(user);
  const button = screen.getByRole("button", { name: "回退并启用所选能力" });
  const capability = within(screen.getByRole("group", { name: "回退版本的能力" })).getByRole("checkbox", { name: "hooks" });
  await user.click(capability); await user.click(capability);
  expect(button).toBeDisabled();
  await user.click(screen.getByRole("checkbox", { name: "我已核对两个版本和所选能力，允许加载所选版本" }));
  await user.click(button);
  expect(rollback).toHaveBeenCalledWith("plugin-current", { version: "plugin-lifecycle.v1", target_installation_id: "plugin-old", expected_current_fingerprint: "c".repeat(64), expected_current_generation: 4,
    expected_target_fingerprint: "d".repeat(64), expected_target_generation: 5, capabilities: ["hooks"], confirm_untrusted: true });
  expect(screen.getByRole("button", { name: "刷新版本记录" })).toBeDisabled();
  expect(screen.getByRole("combobox", { name: "回退到" })).toBeDisabled();
  resolve({}); await waitFor(() => expect(historyRead).toHaveBeenCalledTimes(2));
});

it("requires an explicit read after a lost write response, retaining the write state when that read fails", async () => {
  const user = userEvent.setup();
  const historyRead = vi.fn().mockResolvedValueOnce(pluginHistory()).mockRejectedValueOnce(new Error("read disconnected")).mockResolvedValue(pluginHistory());
  const rollback = vi.fn().mockRejectedValue(new Error("response lost"));
  mount(<PluginLifecycleControls client={{ hasExtensionControl: true, pluginHistory: historyRead, rollbackPluginInstallation: rollback } as unknown as APIClient} installationID="plugin-current" />);
  await chooseRollback(user); await user.click(screen.getByRole("button", { name: "回退并启用所选能力" }));
  await screen.findByText(/操作结果需要核对/);
  expect(screen.getByRole("button", { name: "回退并启用所选能力" })).toBeDisabled();
  await user.click(screen.getByRole("button", { name: "核对最新版本状态" }));
  await screen.findByText(/版本记录读取失败/);
  expect(screen.getByRole("button", { name: "回退并启用所选能力" })).toBeDisabled();
  expect(rollback).toHaveBeenCalledTimes(1);
  await user.click(screen.getByRole("button", { name: "核对最新版本状态" }));
  await waitFor(() => expect(screen.queryByText(/操作结果需要核对/)).not.toBeInTheDocument());
  expect(screen.getByRole("checkbox", { name: "我已核对两个版本和所选能力，允许加载所选版本" })).not.toBeChecked();
  expect(rollback).toHaveBeenCalledTimes(1);
});

it("discloses publisher-wide revocation and submits its exact trust generation after confirmation", async () => {
  const user = userEvent.setup(); const revoke = vi.fn().mockResolvedValue({});
  mount(<PluginLifecycleControls client={{ hasExtensionControl: true, pluginHistory: vi.fn().mockResolvedValue(pluginHistory()), revokePluginPublisher: revoke } as unknown as APIClient} installationID="plugin-current" />);
  await user.click(screen.getByText("版本与发布者"));
  const button = await screen.findByRole("button", { name: "撤销发布者信任" }); expect(button).toBeDisabled();
  expect(screen.getByText(/跨包生效/)).toBeInTheDocument();
  await user.click(screen.getByRole("checkbox", { name: "我已核对发布者和影响范围，撤销其信任与安装权限" }));
  await user.click(button);
  expect(revoke).toHaveBeenCalledWith("plugin-current", { version: "plugin-lifecycle.v1", expected_publisher_fingerprint: "f".repeat(64), expected_publisher_generation: 3, confirm: true });
});

it("reads the selected Hook scope again after the production scope changes", async () => {
  const user = userEvent.setup(); const read = vi.fn().mockResolvedValue(hookDiagnostics());
  const client = { hookDiagnostics: read } as unknown as APIClient;
  const rendered = mount(<HookDiagnostics client={client} runID="run-one" workspaceID="project-one" />);
  await user.click(screen.getByText("Hooks 声明与触发记录")); await screen.findByText("已拒绝操作");
  rendered.rerender(<QueryClientProvider client={new QueryClient({ defaultOptions: { queries: { retry: false } } })}><LocaleProvider><HookDiagnostics client={client} runID="run-two" workspaceID="project-two" /></LocaleProvider></QueryClientProvider>);
  await waitFor(() => expect(read).toHaveBeenCalledWith("run-two", "project-two", expect.any(AbortSignal)));
});

it("offers re-trust for a revoked publisher and binds the reviewed generation", async () => {
  const user = userEvent.setup(); const trust = vi.fn().mockResolvedValue({});
  const history = pluginHistory(); history.publisher!.state = "revoked";
  mount(<PluginLifecycleControls client={{ hasExtensionControl: true, pluginHistory: vi.fn().mockResolvedValue(history), trustPluginPublisher: trust } as unknown as APIClient} installationID="plugin-current" />);
  await user.click(screen.getByText("版本与发布者"));
  const button = await screen.findByRole("button", { name: "信任该发布者" });
  expect(button).toBeDisabled();
  expect(screen.getByText(/无法通过审查/)).toBeInTheDocument();
  expect(screen.queryByRole("button", { name: "撤销发布者信任" })).not.toBeInTheDocument();
  await user.click(screen.getByRole("checkbox", { name: "我已核对发布者公钥指纹，确认建立信任记录" }));
  await user.click(button);
  expect(trust).toHaveBeenCalledWith("plugin-current", { version: "plugin-lifecycle.v1",
    expected_publisher_fingerprint: "f".repeat(64), expected_publisher_generation: 3, confirm: true });
});

it("offers a first trust record for a signed publisher without one", async () => {
  const user = userEvent.setup(); const trust = vi.fn().mockResolvedValue({});
  const history = pluginHistory(); delete history.publisher;
  mount(<PluginLifecycleControls client={{ hasExtensionControl: true, pluginHistory: vi.fn().mockResolvedValue(history), trustPluginPublisher: trust } as unknown as APIClient} installationID="plugin-current" />);
  await user.click(screen.getByText("版本与发布者"));
  const button = await screen.findByRole("button", { name: "信任该发布者" });
  expect(screen.getByText(/尚无信任记录/)).toBeInTheDocument();
  await user.click(screen.getByRole("checkbox", { name: "我已核对发布者公钥指纹，确认建立信任记录" }));
  await user.click(button);
  expect(trust).toHaveBeenCalledWith("plugin-current", { version: "plugin-lifecycle.v1",
    expected_publisher_fingerprint: "f".repeat(64), expected_publisher_generation: 0, confirm: true });
});
