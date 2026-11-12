import { describe, expect, it } from "vitest";
import { nextSharedBrowserLayoutRevision, parseSharedBrowserState, sharedBrowserNavigationURL, sharedBrowserProtocol } from "./desktop-shared-browser";

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
});
