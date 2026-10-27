import { desktopTerminalSettingsAvailable, getDesktopTerminalSettings, saveDesktopTerminalSettings } from "./desktop-terminal-settings";

const view = { protocol_version: "desktop_terminal_settings.v1", revision: 1,
  settings: { agent_control: "manual", manual_ttl_seconds: 300, default_shell: "auto" }, shells: [], can_save: true };
afterEach(() => vi.unstubAllGlobals());

it("validates native preferences and emits refresh only after a confirmed save", async () => {
  const get = vi.fn().mockResolvedValue(view);
  const save = vi.fn().mockResolvedValue({ protocol_version: view.protocol_version, status: "cancelled", replayed: false, view });
  vi.stubGlobal("go", { desktop: { DesktopBridge: { Bootstrap: vi.fn(), GetTerminalSettings: get, ConfirmTerminalSettings: save } } });
  const changed = vi.fn(); window.addEventListener("uc-terminal-settings-changed", changed);
  try {
    expect(desktopTerminalSettingsAvailable()).toBe(true);
    expect(await getDesktopTerminalSettings()).toEqual(view);
    const settings = { agent_control: "full" as const, manual_ttl_seconds: 60, default_shell: "cmd" as const };
    await saveDesktopTerminalSettings(1, settings);
    expect(changed).not.toHaveBeenCalled();
    save.mockResolvedValue({ protocol_version: view.protocol_version, status: "saved", replayed: false, view: { ...view, settings, revision: 2 } });
    await saveDesktopTerminalSettings(1, settings);
    expect(changed).toHaveBeenCalledTimes(1);
    expect(save).toHaveBeenLastCalledWith({ protocol_version: view.protocol_version, expected_revision: 1, settings });
    get.mockResolvedValue({ ...view, settings: { ...settings, default_shell: "C:\\untrusted.exe" } });
    await expect(getDesktopTerminalSettings()).rejects.toThrow("invalid");
    await expect(saveDesktopTerminalSettings(1, { ...settings, manual_ttl_seconds: 0 })).rejects.toThrow("invalid");
    expect(save).toHaveBeenCalledTimes(2);
  } finally { window.removeEventListener("uc-terminal-settings-changed", changed); }
});
