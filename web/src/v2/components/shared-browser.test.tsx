import { act, cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { beforeEach, afterEach, describe, expect, it, vi } from "vitest";
import { LocaleProvider } from "../../lib/locale";
import type { SharedBrowserState, SharedBrowserSurface } from "../../lib/desktop-shared-browser";
import { V2SharedBrowser } from "./shared-browser";

const rpc = vi.hoisted(() => ({ open: vi.fn(), get: vi.fn(), command: vi.fn(), surface: vi.fn() }));
vi.mock("../../lib/desktop-shared-browser", async (original) => ({ ...await original<typeof import("../../lib/desktop-shared-browser")>(),
  openSharedBrowser: rpc.open, getSharedBrowser: rpc.get, commandSharedBrowser: rpc.command, setSharedBrowserSurface: rpc.surface }));
const initial: SharedBrowserState = { protocol_version: "desktop_shared_browser.v1", workspace_id: "workspace-ui", session_id: "session-ui",
  revision: 1, state: "ready", control_owner: "agent", fullscreen: false, active_tab_id: "tab-ui",
  tabs: [{ tab_id: "tab-ui", title: "Example", url: "https://example.com/", loading: false, can_go_back: true, can_go_forward: false }] };
const rect = { x: 400, y: 150, left: 400, top: 150, right: 900, bottom: 750, width: 500, height: 600, toJSON() {} };
const mount = (props: { workspaceID: string; threadID?: string; runID?: string } = { workspaceID: "workspace-ui" }) =>
  render(<LocaleProvider><V2SharedBrowser {...props} /></LocaleProvider>);

beforeEach(() => {
  rpc.open.mockReset().mockResolvedValue(initial);
  rpc.get.mockReset().mockResolvedValue(initial);
  rpc.command.mockReset().mockImplementation(async () => ({ ...initial, revision: 2 }));
  rpc.surface.mockReset().mockImplementation(async (request: SharedBrowserSurface) => ({ ...request }));
  vi.stubGlobal("ResizeObserver", class { observe() {} disconnect() {} unobserve() {} });
  vi.spyOn(HTMLElement.prototype, "getBoundingClientRect").mockReturnValue(rect);
  vi.spyOn(HTMLElement.prototype, "getClientRects").mockReturnValue([rect] as unknown as DOMRectList);
});
afterEach(() => { cleanup(); vi.restoreAllMocks(); vi.unstubAllGlobals(); });

describe("native shared-browser controls", () => {
  it("opens without a Run, uses native commands, and does not gate user controls on Agent ownership", async () => {
    const view = mount();
    const address = await screen.findByRole("textbox", { name: "浏览器地址" });
    await waitFor(() => expect(address).toBeEnabled());
    expect(rpc.open).toHaveBeenCalledWith({ workspace_id: "workspace-ui", adopt_session_id: undefined });
    expect(view.container.querySelector("canvas, iframe")).toBeNull();
    expect(screen.queryByRole("button", { name: /接管|交还/ })).not.toBeInTheDocument();
    fireEvent.change(address, { target: { value: "https://example.org/login" } });
    fireEvent.submit(address.closest("form")!);
    await waitFor(() => expect(rpc.command).toHaveBeenCalledWith("session-ui", "navigate", { tab_id: "tab-ui", url: "https://example.org/login" }));
    await waitFor(() => expect(screen.getByRole("button", { name: "后退" })).toBeEnabled());
    fireEvent.click(screen.getByRole("button", { name: "后退" }));
    await waitFor(() => expect(rpc.command).toHaveBeenCalledWith("session-ui", "back", { tab_id: "tab-ui" }));
    await waitFor(() => expect(screen.getByRole("button", { name: "新建浏览器标签页" })).toBeEnabled());
    fireEvent.click(screen.getByRole("button", { name: "新建浏览器标签页" }));
    await waitFor(() => expect(rpc.command).toHaveBeenCalledWith("session-ui", "new_tab", { url: "about:blank" }));
    await waitFor(() => expect(screen.getByRole("button", { name: "全屏浏览器" })).toBeEnabled());
    fireEvent.click(screen.getByRole("button", { name: "全屏浏览器" }));
    await waitFor(() => expect(rpc.command).toHaveBeenCalledWith("session-ui", "enter_fullscreen", {}));
  });
  it("rejects non-web navigation and preserves an address draft while the host redirects", async () => {
    rpc.get.mockResolvedValue({ ...initial, revision: 3, tabs: [{ ...initial.tabs[0], url: "https://example.com/redirected" }] });
    mount();
    const address = await screen.findByRole("textbox", { name: "浏览器地址" });
    await waitFor(() => expect(address).toHaveValue("https://example.com/"));
    act(() => address.focus());
    fireEvent.change(address, { target: { value: "javascript:alert(1)" } });
    fireEvent.submit(address.closest("form")!);
    expect(await screen.findByRole("alert")).toHaveTextContent("http://");
    expect(rpc.command).not.toHaveBeenCalled();
    await waitFor(() => expect(rpc.get).toHaveBeenCalled());
    expect(address).toHaveValue("javascript:alert(1)");
    fireEvent.keyDown(address, { key: "Escape" });
    expect(address).toHaveValue("https://example.com/redirected");
  });
  it("sends CSS viewport geometry and DPI, hides for menus and on unmount without closing the session", async () => {
    const view = mount();
    await waitFor(() => expect(rpc.surface).toHaveBeenCalledWith(expect.objectContaining({ session_id: "session-ui", visible: true,
      device_scale_factor: window.devicePixelRatio, bounds: { x: 400, y: 150, width: 500, height: 600 } })));
    const menu = document.createElement("div"); menu.setAttribute("role", "menu");
    act(() => document.body.append(menu));
    await waitFor(() => expect(rpc.surface.mock.lastCall?.[0].visible).toBe(false));
    act(() => menu.remove());
    await waitFor(() => expect(rpc.surface.mock.lastCall?.[0].visible).toBe(true));
    const lastVisibleRevision = rpc.surface.mock.lastCall![0].layout_revision;
    view.unmount();
    expect(rpc.surface.mock.lastCall?.[0]).toMatchObject({ visible: false, bounds: { width: 0, height: 0 } });
    expect(rpc.surface.mock.lastCall![0].layout_revision).toBeGreaterThan(lastVisibleRevision);
    expect(rpc.command).not.toHaveBeenCalled();
  });
  it("hides a late Open result after leaving its workspace", async () => {
    let resolve!: (state: SharedBrowserState) => void;
    rpc.open.mockReturnValue(new Promise<SharedBrowserState>((done) => { resolve = done; }));
    const view = mount(); view.unmount();
    await act(async () => resolve(initial));
    expect(rpc.surface).toHaveBeenCalledWith(expect.objectContaining({ session_id: "session-ui", visible: false }));
  });
});
