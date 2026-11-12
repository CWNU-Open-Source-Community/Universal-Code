import { act, cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { useState } from "react";
import { beforeEach, afterEach, describe, expect, it, vi } from "vitest";
import { LocaleProvider } from "../../lib/locale";
import type { SharedBrowserState, SharedBrowserSurface } from "../../lib/desktop-shared-browser";
import { V2SharedBrowser } from "./shared-browser";
import { V2DraftBrowserWorkspace } from "./draft-browser-workspace";
import * as desktopBridge from "../../lib/desktop-bridge";
import { V2WorkspaceDock } from "./workspace-dock";
import { focusWorkspaceSingletonTab, useThreadWorkspaceState } from "../workspace-tabs";
import type { APIClient } from "../../api/client";
import type { ThreadDetailView } from "../../api/types";

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
  it("shows actual tools and host history on the new-tab page while hiding the native surface", async () => {
    const tool = vi.fn();
    rpc.open.mockResolvedValue({ ...initial, tabs: [{ ...initial.tabs[0], title: "", url: "about:blank" }],
      history: [{ url: "https://example.com/actual-visit", title: "已访问报告", visited_at: "2026-11-12T05:00:00Z" }] });
    render(<LocaleProvider><V2SharedBrowser workspaceID="workspace-ui" tools={[{ id: "files", label: "文件", icon: null, onOpen: tool }]} /></LocaleProvider>);
    expect(await screen.findByRole("region", { name: "新标签页" })).toBeVisible();
    expect(screen.getByRole("textbox", { name: "浏览器地址" })).toHaveValue("");
    fireEvent.click(screen.getByRole("button", { name: "文件" }));
    expect(tool).toHaveBeenCalledOnce();
    await waitFor(() => expect(rpc.surface).toHaveBeenCalledWith(expect.objectContaining({ visible: false })));
    expect(rpc.surface.mock.calls.every(([request]) => request.visible === false)).toBe(true);
    fireEvent.click(screen.getByRole("button", { name: /已访问报告/ }));
    await waitFor(() => expect(rpc.command).toHaveBeenCalledWith("session-ui", "navigate", { tab_id: "tab-ui", url: "https://example.com/actual-visit" }));
    await waitFor(() => expect(rpc.surface.mock.lastCall?.[0].visible).toBe(true));
  });
  it("renders one draft workspace tab bar and restores split after OS fullscreen", async () => {
    vi.spyOn(desktopBridge, "desktopSharedBrowserEnabled").mockReturnValue(true);
    let revision = 1;
    rpc.command.mockImplementation(async (_session, name) => ({ ...initial, revision: ++revision, fullscreen: name === "enter_fullscreen" }));
    const view = render(<LocaleProvider><V2DraftBrowserWorkspace workspaceID="workspace-ui">{trigger => <section><header>{trigger}</header><p>草稿</p></section>}</V2DraftBrowserWorkspace></LocaleProvider>);
    fireEvent.click(screen.getByRole("button", { name: "浏览器" }));
    expect(await screen.findByRole("tab", { name: "Example" })).toBeVisible();
    expect(screen.getAllByRole("tablist")).toHaveLength(1);
    expect(screen.queryByRole("tab", { name: "浏览器" })).not.toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: "全屏浏览器" }));
    await waitFor(() => expect(view.container.querySelector(".v2-conversation-stage")).toHaveClass("is-full-view"));
    fireEvent.click(screen.getByRole("button", { name: "退出全屏" }));
    await waitFor(() => expect(view.container.querySelector(".v2-conversation-stage")).not.toHaveClass("is-full-view"));
  });
  it.each(["draft", "conversation"])("keeps %s divider dragging outside page input and restores the native surface afterward", async (kind) => {
    vi.spyOn(desktopBridge, "desktopSharedBrowserEnabled").mockReturnValue(true);
    const workspaceID = `resize-${kind}`, scope = kind === "draft" ? `draft:${workspaceID}` : `resize-thread-${kind}`;
    rpc.open.mockResolvedValue({ ...initial, workspace_id: workspaceID });
    rpc.get.mockResolvedValue({ ...initial, workspace_id: workspaceID });
    focusWorkspaceSingletonTab(scope, "browser");
    function Dock() {
      const state = useThreadWorkspaceState(scope);
      const [resizing, setResizing] = useState(false);
      return <div className="v2-conversation-stage"><div className="v2-workbench-slot is-open" data-resizing={resizing || undefined}>
        <V2WorkspaceDock client={{} as APIClient} threadID={scope} detail={{ thread: { workspace_id: workspaceID } } as ThreadDetailView}
          currentRunID="run-ui" workspaceName="工作区" working={false} docked panelVisible state={state}
          fullViewShortcutLabel="" onToggleFullView={() => {}} onCollapse={() => {}} returnFocusRef={{ current: null }}
          canQuote={false} onReturnToDraft={() => {}} onRequestChange={() => {}} canRequestPreviewStart={false} onRequestPreviewStart={() => {}}
          onResizeStateChange={(active) => setResizing(active)} />
      </div></div>;
    }
    const view = render(<LocaleProvider>{kind === "draft"
      ? <V2DraftBrowserWorkspace workspaceID={workspaceID}>{trigger => <section>{trigger}</section>}</V2DraftBrowserWorkspace>
      : <Dock />}</LocaleProvider>);
    Object.defineProperty(view.container.querySelector(".v2-conversation-stage"), "clientWidth", { configurable: true, value: 1280 });
    await waitFor(() => expect(rpc.surface.mock.lastCall?.[0].visible).toBe(true));
    const handle = screen.getByRole("separator");
    let captured = false;
    const release = vi.fn(() => { captured = false; });
    Object.assign(handle, { setPointerCapture: vi.fn(() => { captured = true; }), hasPointerCapture: () => captured, releasePointerCapture: release });
    const pointer = (type: string, x: number) => {
      const event = new MouseEvent(type, { bubbles: true, button: 0, clientX: x });
      Object.defineProperty(event, "pointerId", { value: 7 });
      fireEvent(handle, event);
    };
    pointer("pointerdown", 700);
    pointer("pointermove", 620);
    await waitFor(() => expect(rpc.surface.mock.lastCall?.[0].visible).toBe(false));
    expect(view.container.querySelector(".v2-workbench-slot")).toHaveAttribute("data-resizing", "true");
    expect(rpc.command).not.toHaveBeenCalled();
    pointer("pointerup", 620);
    await waitFor(() => expect(handle).toHaveAttribute("aria-valuenow", "520"));
    await waitFor(() => expect(rpc.surface.mock.lastCall?.[0].visible).toBe(true));
    expect(release).toHaveBeenCalledWith(7);
    expect(view.container.querySelector(".v2-workbench-slot")).not.toHaveAttribute("data-resizing");
  });
  it("does not exit OS fullscreen for a modal but exits when the browser container unmounts", async () => {
    const changed = vi.fn();
    rpc.open.mockResolvedValue({ ...initial, fullscreen: true });
    const view = render(<LocaleProvider><V2SharedBrowser workspaceID="workspace-ui" onFullscreenChange={changed} /></LocaleProvider>);
    await waitFor(() => expect(changed).toHaveBeenCalledWith(true));
    const menu = document.createElement("div"); menu.setAttribute("role", "menu");
    act(() => document.body.append(menu));
    await waitFor(() => expect(rpc.surface.mock.lastCall?.[0].visible).toBe(false));
    expect(rpc.command).not.toHaveBeenCalled();
    act(() => menu.remove());
    view.unmount();
    expect(rpc.command).toHaveBeenCalledWith("session-ui", "exit_fullscreen");
    expect(changed).toHaveBeenLastCalledWith(false);
  });
  it("keeps native pages alongside other workspace tabs in the same dock tab list", async () => {
    vi.spyOn(desktopBridge, "desktopSharedBrowserEnabled").mockReturnValue(true);
    const pages = { ...initial, tabs: [...initial.tabs, { ...initial.tabs[0], tab_id: "tab-two", title: "Second" }] };
    rpc.open.mockResolvedValue(pages); rpc.get.mockResolvedValue(pages);
    let revision = 1;
    rpc.command.mockImplementation(async (_session, _command, options) => ({ ...pages, revision: ++revision, active_tab_id: options?.tab_id ?? pages.active_tab_id }));
    const threadID = "unified-dock-test";
    focusWorkspaceSingletonTab(threadID, "browser");
    focusWorkspaceSingletonTab(threadID, "records");
    focusWorkspaceSingletonTab(threadID, "browser");
    function Dock() {
      const state = useThreadWorkspaceState(threadID);
      return <V2WorkspaceDock client={{} as APIClient} threadID={threadID} detail={{ thread: { workspace_id: "workspace-ui" } } as ThreadDetailView}
        currentRunID="run-ui" workspaceName="工作区" working={false} docked panelVisible state={state}
        fullViewShortcutLabel="" onToggleFullView={() => {}} onCollapse={() => {}} returnFocusRef={{ current: null }}
        canQuote={false} onReturnToDraft={() => {}} onRequestChange={() => {}} canRequestPreviewStart={false} onRequestPreviewStart={() => {}}
        recordsContent={<p>真实记录面板</p>} />;
    }
    render(<LocaleProvider><Dock /></LocaleProvider>);
    const nativeTab = await screen.findByRole("tab", { name: "Example" });
    expect(screen.getAllByRole("tablist")).toHaveLength(1);
    expect(screen.queryByRole("tab", { name: "浏览器" })).not.toBeInTheDocument();
    const secondTab = screen.getByRole("tab", { name: "Second" });
    fireEvent.keyDown(nativeTab, { key: "ArrowRight" });
    await waitFor(() => expect(secondTab).toHaveAttribute("aria-selected", "true"));
    expect(secondTab).toHaveFocus();
    fireEvent.keyDown(secondTab, { key: "End" });
    expect(await screen.findByText("真实记录面板")).toBeVisible();
    const recordsTab = screen.getByRole("tab", { name: "执行记录" });
    expect(recordsTab).toHaveFocus();
    expect(nativeTab).toBeVisible();
    expect(nativeTab).toHaveAttribute("aria-selected", "false");
    await waitFor(() => expect(rpc.surface.mock.lastCall?.[0].visible).toBe(false));
    fireEvent.keyDown(recordsTab, { key: "ArrowLeft" });
    await waitFor(() => expect(rpc.surface.mock.lastCall?.[0].visible).toBe(true));
    expect(secondTab).toHaveFocus();
    fireEvent.keyDown(secondTab, { key: "Home" });
    await waitFor(() => expect(nativeTab).toHaveAttribute("aria-selected", "true"));
    expect(nativeTab).toHaveFocus();
    expect(nativeTab).toHaveAttribute("aria-selected", "true");
    expect(rpc.open).toHaveBeenCalledOnce();
  });
});
