import { getSharedBrowserBridge } from "./desktop-bridge";

export const sharedBrowserProtocol = "desktop_shared_browser.v1";
let layoutRevision = Date.now() * 1_000;
export const nextSharedBrowserLayoutRevision = () => ++layoutRevision;
export interface SharedBrowserScope { workspace_id: string; thread_id?: string; run_id?: string }
export interface SharedBrowserOpenRequest extends SharedBrowserScope { adopt_session_id?: string }
export interface SharedBrowserTab {
  tab_id: string; url: string; title: string; loading: boolean;
  can_go_back: boolean; can_go_forward: boolean;
}
export interface SharedBrowserState extends SharedBrowserScope {
  protocol_version: typeof sharedBrowserProtocol;
  session_id: string; revision: number; state: "starting" | "ready" | "closed" | "failed";
  control_owner: "user" | "agent"; fullscreen: boolean; active_tab_id: string;
  tabs: SharedBrowserTab[]; error?: string;
}
export type SharedBrowserCommand = "new_tab" | "select_tab" | "close_tab" | "navigate" |
  "back" | "forward" | "reload" | "stop" | "focus" | "take_control" | "release_control" |
  "enter_fullscreen" | "exit_fullscreen";
export interface SharedBrowserCommandRequest {
  protocol_version: typeof sharedBrowserProtocol; session_id: string;
  command: SharedBrowserCommand; tab_id?: string; url?: string; operation_key: string;
}
export interface SharedBrowserSurface {
  protocol_version: typeof sharedBrowserProtocol; session_id: string; tab_id: string;
  bounds: { x: number; y: number; width: number; height: number };
  visible: boolean; layout_revision: number;
  device_scale_factor: number;
}
export interface SharedBrowserSurfaceResult {
  protocol_version: typeof sharedBrowserProtocol; session_id: string; layout_revision: number; visible: boolean;
}
export interface SharedBrowserBridge {
  OpenSharedBrowser(request: SharedBrowserOpenRequest & { protocol_version: typeof sharedBrowserProtocol }): Promise<unknown>;
  GetSharedBrowser(request: { protocol_version: typeof sharedBrowserProtocol; session_id: string }): Promise<unknown>;
  SharedBrowserCommand(request: SharedBrowserCommandRequest): Promise<unknown>;
  SetSharedBrowserSurface(request: SharedBrowserSurface): Promise<unknown>;
}

const record = (value: unknown): value is Record<string, unknown> => typeof value === "object" && value !== null;
const id = (value: unknown): value is string => typeof value === "string" && value.length > 0 && value.length <= 256 && !/[\u0000-\u001f]/u.test(value);
const optionalID = (value: unknown) => value === undefined || value === "" || id(value);
const counter = (value: unknown): value is number => Number.isSafeInteger(value) && (value as number) >= 0;

// Only navigations initiated from the address bar are normalized here. Titles and
// URLs reported by the host are display data and never become renderer markup.
export function sharedBrowserNavigationURL(input: string): string | null {
  const value = input.trim();
  if (value === "about:blank") return value;
  try {
    const url = new URL(value);
    return (url.protocol === "http:" || url.protocol === "https:") && !url.username && !url.password ? url.href : null;
  } catch { return null; }
}

export function parseSharedBrowserState(value: unknown, expected: Partial<SharedBrowserScope> & { session_id?: string } = {}): SharedBrowserState {
  if (!record(value) || value.protocol_version !== sharedBrowserProtocol || !id(value.session_id) ||
    !id(value.workspace_id) || !optionalID(value.thread_id) || !optionalID(value.run_id) || !counter(value.revision) ||
    !["starting", "ready", "closed", "failed"].includes(String(value.state)) ||
    !["user", "agent"].includes(String(value.control_owner)) || typeof value.fullscreen !== "boolean" ||
    typeof value.active_tab_id !== "string" || !Array.isArray(value.tabs) || value.tabs.length > 100 ||
    (value.error !== undefined && typeof value.error !== "string")) throw new Error("Invalid shared browser state");
  const seen = new Set<string>();
  for (const tab of value.tabs) {
    if (!record(tab) || !id(tab.tab_id) || seen.has(tab.tab_id) || typeof tab.url !== "string" ||
      typeof tab.title !== "string" || typeof tab.loading !== "boolean" || typeof tab.can_go_back !== "boolean" ||
      typeof tab.can_go_forward !== "boolean") throw new Error("Invalid shared browser tab");
    seen.add(tab.tab_id);
  }
  if ((value.tabs.length > 0 && !seen.has(value.active_tab_id)) || (value.tabs.length === 0 && value.active_tab_id !== "") ||
    Object.entries(expected).some(([key, entry]) => entry !== undefined && (value[key] ?? "") !== entry)) {
    throw new Error("Shared browser scope mismatch");
  }
  return value as unknown as SharedBrowserState;
}

function bridge() {
  const result = getSharedBrowserBridge();
  if (!result) throw new Error("Shared browser is unavailable");
  return result;
}
export async function openSharedBrowser(scope: SharedBrowserOpenRequest) {
  if (!id(scope.workspace_id) || !optionalID(scope.thread_id) || !optionalID(scope.run_id) || !optionalID(scope.adopt_session_id)) throw new Error("Invalid browser scope");
  return parseSharedBrowserState(await bridge().OpenSharedBrowser({ protocol_version: sharedBrowserProtocol, ...scope }),
    { workspace_id: scope.workspace_id, thread_id: scope.thread_id ?? "" });
}
export async function getSharedBrowser(sessionID: string) {
  return parseSharedBrowserState(await bridge().GetSharedBrowser({ protocol_version: sharedBrowserProtocol, session_id: sessionID }), { session_id: sessionID });
}
export async function commandSharedBrowser(sessionID: string, command: SharedBrowserCommand, options: { tab_id?: string; url?: string } = {}) {
  const url = options.url === undefined ? undefined : sharedBrowserNavigationURL(options.url);
  if (url === null) throw new Error("Use an http:// or https:// address, or about:blank");
  return parseSharedBrowserState(await bridge().SharedBrowserCommand({ protocol_version: sharedBrowserProtocol, session_id: sessionID,
    command, ...options, ...(url === undefined ? {} : { url }), operation_key: crypto.randomUUID() }), { session_id: sessionID });
}
export async function setSharedBrowserSurface(request: Omit<SharedBrowserSurface, "protocol_version">): Promise<SharedBrowserSurfaceResult> {
  const value = await bridge().SetSharedBrowserSurface({ protocol_version: sharedBrowserProtocol, ...request });
  if (!record(value) || value.protocol_version !== sharedBrowserProtocol || value.session_id !== request.session_id ||
    value.layout_revision !== request.layout_revision || typeof value.visible !== "boolean") throw new Error("Invalid browser surface result");
  return value as unknown as SharedBrowserSurfaceResult;
}
