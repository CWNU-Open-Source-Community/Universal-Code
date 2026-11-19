import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { APIRequestError, type APIClient } from "../api/client";
import type { SandboxEnvironmentControlRequestView } from "../api/types";
import { sandboxEnvironmentFixture } from "../test/sandbox-environment";
import { SandboxEnvironmentPanel } from "./sandbox-environment-panel";

const desktop = vi.hoisted(() => ({ enabled: true, restart: vi.fn() }));
vi.mock("../lib/desktop-bridge", () => ({ desktopSandboxRestartEnabled: () => desktop.enabled,
  restartDesktopWithSandboxSettings: desktop.restart, desktopErrorMessage: (error: Error) => error.message }));
vi.mock("../lib/locale", () => ({ useLocale: () => ({ t: (chinese: string) => chinese }) }));

beforeEach(() => { desktop.enabled = true; desktop.restart.mockReset().mockResolvedValue({ status: "cancelled" }); });

function setup(hasControl = true) {
  let value = sandboxEnvironmentFixture();
  const getSandboxEnvironment = vi.fn(async () => value);
  const saveSandboxEnvironment = vi.fn(async (body: SandboxEnvironmentControlRequestView) => {
    value = sandboxEnvironmentFixture(body.settings, {}, body.expected_revision + 1);
    return sandboxEnvironmentFixture(body.settings, {}, body.expected_revision + 1, false);
  });
  const client = { hasControl, getSandboxEnvironment, saveSandboxEnvironment } as unknown as APIClient;
  const queries = new QueryClient({ defaultOptions: { queries: { retry: false }, mutations: { retry: false } } });
  const ui = <QueryClientProvider client={queries}><SandboxEnvironmentPanel client={client} /></QueryClientProvider>;
  return { client, queries, ui, getSandboxEnvironment, saveSandboxEnvironment,
    setValue: (next: typeof value) => { value = next; } };
}

it("shows three observed backends, defaults to Local and saves without claiming activation or installation", async () => {
  const user = userEvent.setup(); const controls = setup(); render(controls.ui);
  await screen.findByText("当前进程已就绪");
  const picker = screen.getByRole("group", { name: "编码环境后端" });
  expect(within(picker).getByRole("button", { name: /^Local/u })).toHaveAttribute("aria-pressed", "true");
  expect(within(picker).getAllByRole("button")).toHaveLength(3);
  expect(screen.getByRole("button", { name: "保存执行环境设置" })).toBeDisabled();
  expect(screen.getByText(/专用 traverse-runtime 命名空间/u)).toHaveTextContent("完整 SHA-256 摘要");
  await user.click(screen.getByRole("checkbox", { name: "启用 Docker Engine" }));
  await user.type(screen.getByLabelText("Docker 固定镜像摘要"), `sha256:${"a".repeat(64)}`);
  await user.click(within(picker).getByRole("button", { name: /^Docker Engine/u }));
  await user.click(screen.getByRole("button", { name: "保存执行环境设置" }));
  await waitFor(() => expect(controls.saveSandboxEnvironment).toHaveBeenCalledOnce());
  expect(controls.saveSandboxEnvironment.mock.calls[0]?.[0]).toEqual({ version: "sandbox_environment.v1", expected_revision: 1,
    settings: { default_backend: "docker", docker_enabled: true, docker_image_digest: `sha256:${"a".repeat(64)}`, sbx_enabled: false, sbx_template: "" } });
  expect(await screen.findByText(/执行环境设置已保存/u)).toBeInTheDocument();
  expect(screen.getAllByText("当前进程待启用")).toHaveLength(2);
  expect(desktop.restart).not.toHaveBeenCalled();
  await user.click(screen.getByRole("button", { name: "重启并读取执行环境设置" }));
  await waitFor(() => expect(desktop.restart).toHaveBeenCalledExactlyOnceWith());
});

it("retains the exact unknown save body across unmount and locks every backend choice until replay succeeds", async () => {
  const user = userEvent.setup(); const controls = setup();
  controls.saveSandboxEnvironment.mockRejectedValueOnce(new Error("response lost"));
  const view = render(controls.ui); await screen.findByText("当前进程已就绪");
  await user.click(screen.getByRole("checkbox", { name: "启用 Docker Sandboxes (sbx)" }));
  await user.click(screen.getByRole("button", { name: /^Docker Sandboxes \(sbx\)/u }));
  await user.click(screen.getByRole("button", { name: "保存执行环境设置" }));
  expect(await screen.findByRole("button", { name: "核对保存结果" })).toBeEnabled();
  const original = controls.saveSandboxEnvironment.mock.calls[0]?.[0];
  view.unmount(); render(controls.ui);
  expect(screen.getByRole("button", { name: /^Local/u })).toBeDisabled();
  expect(screen.getByRole("checkbox", { name: "启用 Docker Engine" })).toBeDisabled();
  await user.click(screen.getByRole("button", { name: "核对保存结果" }));
  await waitFor(() => expect(controls.saveSandboxEnvironment).toHaveBeenCalledTimes(2));
  expect(controls.saveSandboxEnvironment.mock.calls[1]?.[0]).toEqual(original);
  expect(await screen.findByText(/执行环境设置已保存/u)).toBeInTheDocument();
});

it("keeps a freshly published save binding when a stale field change fires before notification", async () => {
  const user = userEvent.setup(); const controls = setup(); render(controls.ui);
  await screen.findByText("当前进程已就绪");
  await user.click(screen.getByRole("checkbox", { name: "启用 Docker Engine" }));
  await user.click(screen.getByRole("button", { name: "保存执行环境设置" }));
  await screen.findByText(/执行环境设置已保存/u);
  const checkbox = screen.getByRole("checkbox", { name: "启用 Docker Sandboxes (sbx)" });
  const key = ["sandbox", "environment-save-intent"];
  const pending = { ...controls.queries.getQueryData<Record<string, unknown>>(key)!, state: "pending" };
  act(() => { controls.queries.setQueryData(key, pending); fireEvent.click(checkbox); });
  expect(controls.queries.getQueryData(key)).toEqual(pending);
  expect(checkbox).not.toBeChecked();
  expect(controls.saveSandboxEnvironment).toHaveBeenCalledOnce();
});

it("requires an explicit reread after revision conflict and uses the returned current revision", async () => {
  const user = userEvent.setup(); const controls = setup();
  controls.saveSandboxEnvironment.mockRejectedValueOnce(new APIRequestError("settings changed", "CONFLICT", 409));
  render(controls.ui); await screen.findByText("当前进程已就绪");
  await user.click(screen.getByRole("checkbox", { name: "启用 Docker Engine" }));
  await user.click(screen.getByRole("button", { name: "保存执行环境设置" }));
  expect(await screen.findByRole("button", { name: "重新读取配置" })).toBeInTheDocument();
  controls.setValue(sandboxEnvironmentFixture({}, {}, 4));
  await user.click(screen.getByRole("button", { name: "重新读取配置" }));
  await waitFor(() => expect(screen.getByRole("checkbox", { name: "启用 Docker Engine" })).toBeEnabled());
  await user.click(screen.getByRole("checkbox", { name: "启用 Docker Engine" }));
  await user.click(screen.getByRole("button", { name: "保存执行环境设置" }));
  await waitFor(() => expect(controls.saveSandboxEnvironment).toHaveBeenCalledTimes(2));
  expect(controls.saveSandboxEnvironment.mock.calls[1]?.[0].expected_revision).toBe(4);
});

it.each([400, 412])("lets a definitive rejected save (%s) be corrected without losing unknown-request recovery", async (status) => {
  const user = userEvent.setup(); const controls = setup();
  controls.saveSandboxEnvironment.mockRejectedValueOnce(new APIRequestError("request rejected", "INVALID_ARGUMENT", status));
  render(controls.ui); await screen.findByText("当前进程已就绪");
  await user.click(screen.getByRole("checkbox", { name: "启用 Docker Engine" }));
  await user.click(screen.getByRole("button", { name: "保存执行环境设置" }));
  expect(await screen.findByText(/设置未保存，请核对配置和连接权限后重试/u)).toBeInTheDocument();
  expect(screen.queryByRole("button", { name: "核对保存结果" })).not.toBeInTheDocument();
  expect(screen.getByRole("checkbox", { name: "启用 Docker Sandboxes (sbx)" })).toBeEnabled();
  await user.click(screen.getByRole("checkbox", { name: "启用 Docker Sandboxes (sbx)" }));
  await user.click(screen.getByRole("button", { name: "保存执行环境设置" }));
  await waitFor(() => expect(controls.saveSandboxEnvironment).toHaveBeenCalledTimes(2));
  expect(controls.saveSandboxEnvironment.mock.calls[1]?.[0].settings.sbx_enabled).toBe(true);
});

it("lets a read-only connection detect environment while keeping save and enable controls disabled", async () => {
  desktop.enabled = false; const controls = setup(false); const user = userEvent.setup(); render(controls.ui);
  await screen.findByText("当前进程已就绪");
  expect(screen.getByRole("checkbox", { name: "启用 Docker Engine" })).toBeDisabled();
  await user.click(screen.getByRole("button", { name: "检测环境" }));
  await waitFor(() => expect(controls.getSandboxEnvironment).toHaveBeenCalledTimes(2));
  expect(controls.saveSandboxEnvironment).not.toHaveBeenCalled(); expect(desktop.restart).not.toHaveBeenCalled();
});
