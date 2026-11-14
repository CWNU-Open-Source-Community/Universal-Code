import { describe, expect, it, vi } from "vitest";
import { useConnectionStore } from "../state/connection";
import type { SharedBrowserState } from "../lib/desktop-shared-browser";
import { adoptDraftSharedBrowser, bindDraftSharedBrowser, draftSharedBrowserAdoption, rememberDraftSharedBrowser } from "./shared-browser-session";
const open = vi.hoisted(() => vi.fn().mockResolvedValue({}));
vi.mock("../lib/desktop-shared-browser", () => ({ openSharedBrowser: open }));

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
  it("binds the draft session before the first turn and ignores a stale connection handoff", async () => {
    const generation = useConnectionStore.getState().connectionGeneration;
    rememberDraftSharedBrowser({ workspace_id: "before-turn", session_id: "logged-in-session" } as SharedBrowserState);
    await adoptDraftSharedBrowser("before-turn", "created-thread", "created-run", generation);
    expect(open).toHaveBeenCalledWith({ workspace_id: "before-turn", thread_id: "created-thread", run_id: "created-run", adopt_session_id: "logged-in-session" });
    open.mockClear();
    await adoptDraftSharedBrowser("before-turn", "unrelated-thread", "created-run", generation - 1);
    expect(open).not.toHaveBeenCalled();
    expect(draftSharedBrowserAdoption("before-turn", "unrelated-thread")).toBeUndefined();
  });
});
