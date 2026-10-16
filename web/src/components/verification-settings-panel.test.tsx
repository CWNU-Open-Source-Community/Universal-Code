import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import type { DesktopVerificationSettingsView } from "../lib/desktop-bridge";
import { VerificationSettingsPanel } from "./verification-settings-panel";

const native = vi.hoisted(() => ({ available: vi.fn(), read: vi.fn(), save: vi.fn(), restart: vi.fn(), warning: "" }));
vi.mock("../lib/desktop-bridge", async () => ({
  ...await vi.importActual<typeof import("../lib/desktop-bridge")>("../lib/desktop-bridge"),
  desktopVerificationSettingsAvailable: native.available, getDesktopVerificationSettings: native.read,
  confirmDesktopVerificationSettings: native.save, restartDesktopWithVerificationSettings: native.restart,
}));
vi.mock("../v2/recovery-storage", async () => ({
  ...await vi.importActual<typeof import("../v2/recovery-storage")>("../v2/recovery-storage"),
  useV2PersistenceWarning: () => native.warning || null,
}));

function view(saved = false, active = false): DesktopVerificationSettingsView {
  return { protocol_version: "desktop_verification_settings.v1", revision: saved ? 2 : 1,
    settings: { ui_evidence_enabled: saved, batch_validation_enabled: false },
    active_settings: { ui_evidence_enabled: active, batch_validation_enabled: false },
    restart_required: saved !== active, can_save: true, can_restart: saved !== active, capability_grant: false,
    ui_evidence: { installed: active, blocked_by: active ? [] : ["startup_gate_closed"],
      remediation: active ? [] : [saved ? "restart_application" : "enable_and_restart"] },
    batch_validation: { installed: false, blocked_by: ["startup_gate_closed"], remediation: ["enable_and_restart"] } };
}
function mount(baseURL = "/api/v1") {
  const queries = new QueryClient({ defaultOptions: { queries: { retry: false }, mutations: { retry: false } } });
  return render(<QueryClientProvider client={queries}><VerificationSettingsPanel client={{ baseURL }} /></QueryClientProvider>);
}
beforeEach(() => {
  native.available.mockReset().mockReturnValue(true);
  native.read.mockReset().mockResolvedValue(view());
  native.save.mockReset(); native.restart.mockReset(); native.warning = "";
});

it("uses native confirmation to save exact choices, then requires a separate restart action", async () => {
  const user = userEvent.setup();
  native.save.mockResolvedValue({ protocol_version: "desktop_verification_settings.v1", status: "saved", replayed: false, view: view(true) });
  native.restart.mockResolvedValue({ status: "cancelled" });
  mount();
  const browser = await screen.findByRole("checkbox", { name: "浏览器验证" });
  expect(screen.getByRole("button", { name: "保存验证设置" })).toBeDisabled();
  await user.click(browser);
  await user.click(screen.getByRole("button", { name: "保存验证设置" }));
  await screen.findByText("验证设置已保存，重启应用后生效。");
  expect(native.save).toHaveBeenCalledWith({ protocol_version: "desktop_verification_settings.v1", expected_revision: 1,
    settings: { ui_evidence_enabled: true, batch_validation_enabled: false } });
  expect(native.restart).not.toHaveBeenCalled();
  expect(screen.getByRole("checkbox", { name: "批量验证" })).not.toBeChecked();
  expect(screen.getAllByText("当前进程：未启用。")).toHaveLength(2);
  await user.click(screen.getByRole("button", { name: "重启以应用验证设置" }));
  await screen.findByText("已取消重启，保存的设置仍保留。");
  expect(browser).toBeChecked();
});

it("does not mark a cancelled native save as enabled or restart the application", async () => {
  native.save.mockResolvedValue({ protocol_version: "desktop_verification_settings.v1", status: "cancelled", replayed: false, view: view() });
  const user = userEvent.setup(); mount();
  await user.click(await screen.findByRole("checkbox", { name: "浏览器验证" }));
  await user.click(screen.getByRole("button", { name: "保存验证设置" }));
  await screen.findByText("已取消保存，启用偏好未更改。");
  expect(screen.queryByRole("button", { name: "重启以应用验证设置" })).not.toBeInTheDocument();
  expect(screen.getByRole("button", { name: "保存验证设置" })).toBeEnabled();
  expect(native.restart).not.toHaveBeenCalled();
});

it("rereads an uncertain save before allowing another write", async () => {
  native.save.mockRejectedValue(new Error("connection closed"));
  const user = userEvent.setup(); mount();
  await user.click(await screen.findByRole("checkbox", { name: "浏览器验证" }));
  await user.click(screen.getByRole("button", { name: "保存验证设置" }));
  await screen.findByText(/保存结果待核对/u);
  expect(screen.getByRole("checkbox", { name: "浏览器验证" })).toBeDisabled();
  native.read.mockResolvedValue(view(true));
  await user.click(screen.getByRole("button", { name: "重新读取验证设置" }));
  await waitFor(() => expect(screen.getByRole("button", { name: "重启以应用验证设置" })).toBeEnabled());
  expect(screen.getByRole("checkbox", { name: "浏览器验证" })).toBeChecked();
  expect(native.save).toHaveBeenCalledTimes(1); expect(native.restart).not.toHaveBeenCalled();
});

it("keeps saved preferences available after restart fails and after a new query cache mounts", async () => {
  native.read.mockResolvedValue(view(true)); native.restart.mockRejectedValueOnce(new Error("child launch failed"));
  const user = userEvent.setup(); const first = mount();
  await user.click(await screen.findByRole("button", { name: "重启以应用验证设置" }));
  await screen.findByText(/重启失败，已保存的设置仍保留/u);
  expect(screen.getByRole("button", { name: "重启以应用验证设置" })).toBeEnabled();
  first.unmount(); mount();
  expect(await screen.findByRole("checkbox", { name: "浏览器验证" })).toBeChecked();
  expect(native.save).not.toHaveBeenCalled();
  expect(native.restart).toHaveBeenCalledTimes(1);
});

it("blocks restart while draft persistence has failed", async () => {
  native.read.mockResolvedValue(view(true)); native.warning = "无法保存草稿";
  mount();
  expect(await screen.findByRole("button", { name: "重启以应用验证设置" })).toBeDisabled();
  expect(screen.getByRole("alert")).toHaveTextContent("无法保存草稿");
  expect(native.restart).not.toHaveBeenCalled();
});

it.each(["/remote-api", "https://remote.example/api/v1"])("does not change native settings from connection %s", (url) => {
  mount(url);
  expect(screen.getByText(/请在支持验证设置的桌面版本/u)).toBeInTheDocument();
  expect(native.read).not.toHaveBeenCalled();
});
