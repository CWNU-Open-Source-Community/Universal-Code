import { describe, expect, it } from "vitest";
import { useConnectionStore } from "../state/connection";
import type { SharedBrowserState } from "../lib/desktop-shared-browser";
import { bindDraftSharedBrowser, draftSharedBrowserAdoption, rememberDraftSharedBrowser } from "./shared-browser-session";

describe("draft browser handoff", () => {
  it("adopts only the explicitly created thread in the same workspace and connection", () => {
    const workspace = "draft-browser-handoff";
    rememberDraftSharedBrowser({ workspace_id: workspace, session_id: "draft-session" } as SharedBrowserState);
    expect(draftSharedBrowserAdoption(workspace, "thread-one")).toBeUndefined();
    bindDraftSharedBrowser(workspace, "thread-one");
    expect(draftSharedBrowserAdoption(workspace, "thread-one")).toBe("draft-session");
    expect(draftSharedBrowserAdoption(workspace, "thread-two")).toBeUndefined();
    expect(draftSharedBrowserAdoption("another-workspace", "thread-one")).toBeUndefined();
    const generation = useConnectionStore.getState().connectionGeneration;
    useConnectionStore.setState({ connectionGeneration: generation + 1 });
    expect(draftSharedBrowserAdoption(workspace, "thread-one")).toBeUndefined();
    useConnectionStore.setState({ connectionGeneration: generation });
  });
});
