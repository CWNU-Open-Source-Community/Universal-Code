export const terminalSettingsProtocol = "desktop_terminal_settings.v1";
export const terminalSettingsChanged = "uc-terminal-settings-changed";
export interface TerminalSettings {
  agent_control: "disabled" | "manual" | "full";
  manual_ttl_seconds: number;
  default_shell: "auto" | "pwsh" | "powershell" | "cmd" | "bash";
}
export interface TerminalShell {
  id: "pwsh" | "powershell" | "cmd" | "bash";
  name: string; path: string; version: string; available: boolean;
}
export interface TerminalSettingsView {
  protocol_version: typeof terminalSettingsProtocol;
  revision: number;
  settings: TerminalSettings;
  shells: TerminalShell[];
  can_save: boolean;
}
export interface TerminalSettingsRequest {
  protocol_version: typeof terminalSettingsProtocol;
  expected_revision: number;
  settings: TerminalSettings;
}
const record = (value: unknown): value is Record<string, unknown> => typeof value === "object" && value !== null && !Array.isArray(value);
const text = (value: unknown, limit: number): value is string => typeof value === "string" && value.length <= limit && !value.includes("\0");
export function validTerminalShell(value: unknown): value is TerminalShell {
  return record(value) && ["pwsh", "powershell", "cmd", "bash"].includes(String(value.id)) && text(value.name, 80) &&
    text(value.path, 8192) && text(value.version, 80) && typeof value.available === "boolean";
}
function validSettings(value: unknown): value is TerminalSettings {
  return record(value) && ["disabled", "manual", "full"].includes(String(value.agent_control)) &&
    ["auto", "pwsh", "powershell", "cmd", "bash"].includes(String(value.default_shell)) &&
    Number.isInteger(value.manual_ttl_seconds) && Number(value.manual_ttl_seconds) >= 15 && Number(value.manual_ttl_seconds) <= 900;
}
function parseView(value: unknown): TerminalSettingsView {
  if (!record(value) || value.protocol_version !== terminalSettingsProtocol || !Number.isSafeInteger(value.revision) || Number(value.revision) < 1 ||
      !validSettings(value.settings) || !Array.isArray(value.shells) || value.shells.length > 8 || !value.shells.every(validTerminalShell) || typeof value.can_save !== "boolean") {
    throw new Error("Terminal settings response is invalid");
  }
  return value as unknown as TerminalSettingsView;
}
export function desktopTerminalSettingsAvailable() {
  const bridge = window.go?.desktop?.DesktopBridge;
  return typeof bridge?.Bootstrap === "function" && typeof bridge.GetTerminalSettings === "function" && typeof bridge.ConfirmTerminalSettings === "function";
}
export async function getDesktopTerminalSettings(): Promise<TerminalSettingsView> {
  const get = window.go?.desktop?.DesktopBridge?.GetTerminalSettings;
  if (!get) throw new Error("Terminal settings require the desktop app");
  return parseView(await get());
}
export async function saveDesktopTerminalSettings(revision: number, settings: TerminalSettings) {
  const save = window.go?.desktop?.DesktopBridge?.ConfirmTerminalSettings;
  if (!save || !validSettings(settings) || !Number.isSafeInteger(revision) || revision < 1) throw new Error("Terminal settings request is invalid");
  const result = await save({ protocol_version: terminalSettingsProtocol, expected_revision: revision, settings });
  if (!record(result) || result.protocol_version !== terminalSettingsProtocol || !["saved", "cancelled"].includes(String(result.status)) || typeof result.replayed !== "boolean") throw new Error("Terminal settings result is invalid");
  const view = parseView(result.view);
  if (result.status === "saved") window.dispatchEvent(new Event(terminalSettingsChanged));
  return { status: result.status as "saved" | "cancelled", view };
}
