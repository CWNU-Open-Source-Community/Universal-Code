import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { TerminalSettingsPanel } from "./terminal-settings-panel";
import type { TerminalSettingsView } from "../lib/desktop-terminal-settings";

const native = vi.hoisted(() => ({ read: vi.fn(), save: vi.fn() }));
vi.mock("../lib/desktop-terminal-settings", () => ({ desktopTerminalSettingsAvailable: () => true,
  getDesktopTerminalSettings: native.read, saveDesktopTerminalSettings: native.save }));
const view = (): TerminalSettingsView => ({ protocol_version: "desktop_terminal_settings.v1", revision: 1,
  settings: { agent_control: "manual", manual_ttl_seconds: 300, default_shell: "auto" }, can_save: true,
  shells: [{ id: "cmd", name: "CMD", path: "C:\\Windows\\System32\\cmd.exe", version: "10.0", available: true },
    { id: "pwsh", name: "PowerShell 7", path: "", version: "", available: false }] });
function mount() {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false }, mutations: { retry: false } } });
  render(<QueryClientProvider client={client}><TerminalSettingsPanel /></QueryClientProvider>);
}
beforeEach(() => { native.read.mockReset().mockResolvedValue(view()); native.save.mockReset(); });

it("saves exact mode, duration and shell through native consent", async () => {
  native.save.mockResolvedValue({ status: "saved", view: { ...view(), revision: 2,
    settings: { agent_control: "full", manual_ttl_seconds: 60, default_shell: "cmd" } } });
  mount();
  fireEvent.change(await screen.findByRole("spinbutton"), { target: { value: "60" } });
  fireEvent.change(screen.getByLabelText("Agent access to the terminal"), { target: { value: "full" } });
  expect(screen.queryByRole("spinbutton")).not.toBeInTheDocument();
  fireEvent.change(screen.getByLabelText("Shell for new terminals"), { target: { value: "cmd" } });
  expect(screen.getByText("C:\\Windows\\System32\\cmd.exe")).toBeInTheDocument();
  expect(screen.getByRole("option", { name: "PowerShell 7 · Not installed" })).toBeDisabled();
  fireEvent.click(screen.getByRole("button", { name: "Save terminal settings" }));
  await waitFor(() => expect(native.save).toHaveBeenCalledWith(1, { agent_control: "full", manual_ttl_seconds: 60, default_shell: "cmd" }));
  await screen.findByText(/Access settings apply now/u);
  expect(screen.getByRole("button", { name: "Save terminal settings" })).toBeDisabled();
});

it("keeps the draft after cancellation and reloads after a stale save", async () => {
  native.save.mockResolvedValueOnce({ status: "cancelled", view: view() }).mockRejectedValueOnce(new Error("settings changed"));
  mount();
  fireEvent.change(await screen.findByLabelText("Agent access to the terminal"), { target: { value: "disabled" } });
  fireEvent.click(screen.getByRole("button", { name: "Save terminal settings" }));
  await screen.findByText("Cancelled. Settings are unchanged.");
  expect(screen.getByLabelText("Agent access to the terminal")).toHaveValue("disabled");
  fireEvent.click(screen.getByRole("button", { name: "Save terminal settings" }));
  await screen.findByRole("alert");
  fireEvent.click(screen.getByRole("button", { name: "Reload terminal settings" }));
  await waitFor(() => expect(screen.getByLabelText("Agent access to the terminal")).toHaveValue("manual"));
});
