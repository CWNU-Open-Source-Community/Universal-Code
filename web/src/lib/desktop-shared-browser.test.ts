import { describe, expect, it, vi } from "vitest";
import { nextSharedBrowserLayoutRevision, parseSharedBrowserState, setSharedBrowserSurface, sharedBrowserNavigationURL, sharedBrowserProtocol } from "./desktop-shared-browser";
const nativeSurface = vi.hoisted(() => vi.fn());
vi.mock("./desktop-bridge", () => ({ getSharedBrowserBridge: () => ({ SetSharedBrowserSurface: nativeSurface }) }));

const state = { protocol_version: sharedBrowserProtocol, session_id: "session-one", workspace_id: "workspace-one", thread_id: "thread-one",
  revision: 2, state: "ready", control_owner: "user", fullscreen: false, active_tab_id: "tab-one",
  tabs: [{ tab_id: "tab-one", url: "https://example.com/", title: "Example", loading: false, can_go_back: true, can_go_forward: false }] };

describe("shared browser bridge data", () => {
  it("accepts only explicit HTTP(S) or about:blank navigation", () => {
    expect(sharedBrowserNavigationURL(" https://example.com ")).toBe("https://example.com/");
    expect(sharedBrowserNavigationURL("http://localhost:8080/path")).toBe("http://localhost:8080/path");
    expect(sharedBrowserNavigationURL("about:blank")).toBe("about:blank");
    for (const value of ["javascript:alert(1)", "file:///C:/secret", "data:text/html,x", "//example.com", "example.com", "https://user:password@example.com/", "about:config"]) {
      expect(sharedBrowserNavigationURL(value)).toBeNull();
    }
  });
  it("rejects a different session/scope, invalid tab selection and duplicate tabs", () => {
    expect(parseSharedBrowserState(state, { session_id: "session-one", workspace_id: "workspace-one" })).toEqual(state);
    for (const value of [{ ...state, active_tab_id: "missing" }, { ...state, tabs: [...state.tabs, ...state.tabs] },
      { ...state, revision: -1 }, { ...state, tabs: [{ ...state.tabs[0], loading: "yes" }] }]) expect(() => parseSharedBrowserState(value)).toThrow();
    expect(() => parseSharedBrowserState(state, { session_id: "another" })).toThrow("scope mismatch");
    expect(() => parseSharedBrowserState(state, { thread_id: "" })).toThrow("scope mismatch");
  });
  it("keeps layout revisions increasing across mounts", () => {
    const first = nextSharedBrowserLayoutRevision();
    expect(nextSharedBrowserLayoutRevision()).toBeGreaterThan(first);
  });
  it("accepts host history and rejects non-web, credential-bearing or invalid records", () => {
    const entry = { url: "https://example.com/visited", title: "Visited", visited_at: "2026-11-12T05:00:00Z" };
    expect(parseSharedBrowserState({ ...state, history: [entry] }).history).toEqual([entry]);
    for (const history of [[{ ...entry, url: "about:blank" }], [{ ...entry, url: "https://user:secret@example.com/" }],
      [{ ...entry, visited_at: "not-a-date" }], Array.from({ length: 51 }, () => entry)]) {
      expect(() => parseSharedBrowserState({ ...state, history })).toThrow("history");
    }
  });
  it("accepts a newer applied surface revision after requests arrive out of order", async () => {
    const request = { session_id: "session-one", tab_id: "tab-one", layout_revision: 10, device_scale_factor: 1.5,
      visible: false, bounds: { x: 0, y: 0, width: 0, height: 0 } };
    nativeSurface.mockResolvedValue({ protocol_version: sharedBrowserProtocol, session_id: request.session_id, layout_revision: 11, visible: true });
    await expect(setSharedBrowserSurface(request)).resolves.toMatchObject({ layout_revision: 11, visible: true });
    nativeSurface.mockResolvedValue({ protocol_version: sharedBrowserProtocol, session_id: request.session_id, layout_revision: 9, visible: false });
    await expect(setSharedBrowserSurface(request)).rejects.toThrow("surface result");
  });
});
