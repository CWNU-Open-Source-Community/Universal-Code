import { describe, expect, it, vi } from "vitest";
import type { HealthView } from "../api/types";
import { useConnectionStore } from "../state/connection";
import { DraftDocument, type DraftScope } from "./draft-document";
import {
  addWorkspaceFileTab,
  clearWorkspaceNotice,
  closeWorkspaceTab,
  collapseWorkspacePanel,
  defaultWorkspaceWidth,
  focusWorkspaceFiles,
  focusWorkspaceSingletonTab,
  getThreadWorkspaceState,
  maxWorkspaceTabsLimitNotice,
  maxWorkspaceWidth,
  minWorkspaceWidth,
  openWorkspaceFile,
  resetWorkspaceTabsMemoryCache,
  resolveWorkspaceStorageConflict,
  restoreLegacyWorkspaceTabs,
  selectWorkspaceTab,
  setWorkspaceTerminalSession,
  setWorkspaceTerminalShell,
  setWorkspaceWidth,
  toggleWorkspaceFiles,
  toggleWorkspaceFullView,
  toggleWorkspaceSingletonTab,
  updateWorkspaceFileTabPath,
  workspaceTabsChangedEvent,
  workspaceTabsStorageKey,
  workspaceScopeKey,
  workspaceStorageConflictNotice,
  workspaceLegacyTabsNotice,
} from "./workspace-tabs";

describe("workspace-tabs store", () => {
  beforeEach(() => useConnectionStore.setState({ health: { data_store_id: "store-one" } as HealthView }));
  afterEach(() => useConnectionStore.setState({ health: null }));

  it("isolates the same Thread ID across data stores and API connections", () => {
    openWorkspaceFile("shared-id", { path: "old-server.txt" }, "old-run");
    useConnectionStore.setState({ health: { data_store_id: "store-two" } as HealthView });
    expect(getThreadWorkspaceState("shared-id").tabs).toEqual([]);
    openWorkspaceFile("shared-id", { path: "new-server.txt" }, "new-run");
    useConnectionStore.setState({ health: { data_store_id: "store-one" } as HealthView });
    expect(getThreadWorkspaceState("shared-id").tabs).toMatchObject([{ path: "old-server.txt" }]);
    useConnectionStore.setState({ apiBaseURL: "/other-api" });
    expect(getThreadWorkspaceState("shared-id").tabs).toEqual([]);
    useConnectionStore.setState({ apiBaseURL: undefined });
  });

  it("does not reuse unscoped in-memory tabs after reconnecting", () => {
    const noIdentity = { data_store_id: "" } as HealthView;
    useConnectionStore.getState().connect("first", noIdentity);
    openWorkspaceFile("unscoped", { path: "private.ts" });
    expect(getThreadWorkspaceState("unscoped").tabs).toHaveLength(1);
    useConnectionStore.getState().connect("second", noIdentity);
    expect(getThreadWorkspaceState("unscoped").tabs).toEqual([]);
    expect(localStorage.getItem(workspaceTabsStorageKey)).toBeNull();
  });

  it("ignores delayed file and terminal callbacks after the data store changes", () => {
    openWorkspaceFile("switch", { path: "old.ts" });
    const oldScope = workspaceScopeKey("switch");
    useConnectionStore.setState({ health: { data_store_id: "store-two" } as HealthView });
    openWorkspaceFile("switch", { path: "new.ts" });
    updateWorkspaceFileTabPath("switch", "file:1", "stale.ts", oldScope);
    setWorkspaceTerminalSession("switch", "old-session", undefined, oldScope);
    setWorkspaceTerminalShell("switch", "old-shell", oldScope);
    expect(getThreadWorkspaceState("switch")).toMatchObject({
      tabs: [{ path: "new.ts" }], terminalSessionID: "",
    });
    expect(getThreadWorkspaceState("switch").terminalShellName).toBeUndefined();
  });

  it("preserves unscoped legacy tabs and restores them only after explicit choice", () => {
    const legacyKey = "universal-code.workspace-tabs.v1";
    const original = JSON.stringify({ version: 1, threads: { legacy: { tabs: [{ id: "file:1", kind: "file", path: "old.ts" }],
      activeTabID: "file:1", lastFileTabID: "file:1", open: true, updatedAt: 1 } } });
    localStorage.setItem(legacyKey, original);
    try {
      expect(getThreadWorkspaceState("legacy").tabs).toEqual([]);
      openWorkspaceFile("legacy", { path: "new.ts" });
      expect(getThreadWorkspaceState("legacy").notice).toBe(workspaceLegacyTabsNotice);
      restoreLegacyWorkspaceTabs("legacy");
      expect(getThreadWorkspaceState("legacy").tabs).toMatchObject([{ path: "old.ts" }]);
      expect(localStorage.getItem(legacyKey)).toBe(original);
    } finally { localStorage.removeItem(legacyKey); }
  });

  it("does not redirect a whitespace-bearing file path to a different file", () => {
    openWorkspaceFile("exact-path", { path: " file.txt" });
    openWorkspaceFile("exact-path", { path: "file.txt" });
    expect(getThreadWorkspaceState("exact-path").tabs).toMatchObject([{ path: " file.txt" }, { path: "file.txt" }]);
    resetWorkspaceTabsMemoryCache();
    expect(getThreadWorkspaceState("exact-path").tabs).toMatchObject([{ path: " file.txt" }, { path: "file.txt" }]);
  });

  it.each(["local", "remote"] as const)("keeps failed tab writes until the %s conflict is resolved", (choice) => {
    const now = vi.spyOn(Date, "now").mockReturnValue(1000);
    try {
      openWorkspaceFile("conflicted", { path: "first.ts" });
      const fail = vi.spyOn(Storage.prototype, "setItem").mockImplementation(() => { throw new Error("quota"); });
      openWorkspaceFile("conflicted", { path: "local.ts" });
      fail.mockRestore();
      const persisted = JSON.parse(localStorage.getItem(workspaceTabsStorageKey)!);
      const entry = Object.values(persisted.threads)[0] as { updatedAt: number; width: number };
      entry.updatedAt = 1001; entry.width = 700;
      localStorage.setItem(workspaceTabsStorageKey, JSON.stringify(persisted));
      expect(getThreadWorkspaceState("conflicted")).toMatchObject({ notice: workspaceStorageConflictNotice,
        tabs: [{ path: "first.ts" }, { path: "local.ts" }] });
      setWorkspaceWidth("conflicted", 600);
      expect(JSON.parse(localStorage.getItem(workspaceTabsStorageKey)!).threads).toEqual(persisted.threads);
      resolveWorkspaceStorageConflict("conflicted", choice);
      resetWorkspaceTabsMemoryCache();
      expect(getThreadWorkspaceState("conflicted").tabs).toHaveLength(choice === "local" ? 2 : 1);
    } finally { now.mockRestore(); }
  });
  it("preserves malformed workspace storage rather than replacing it with an empty store", () => {
    localStorage.setItem(workspaceTabsStorageKey, "{broken");
    openWorkspaceFile("malformed", { path: "local.ts" });
    expect(localStorage.getItem(workspaceTabsStorageKey)).toBe("{broken");
    expect(getThreadWorkspaceState("malformed").tabs).toMatchObject([{ path: "local.ts" }]);
  });

  it("retains local tabs when storage reads fail, without writing over unseen records", () => {
    openWorkspaceFile("read-failure", { path: "existing.ts" });
    const original = localStorage.getItem(workspaceTabsStorageKey);
    const read = vi.spyOn(Storage.prototype, "getItem").mockImplementation((key) => {
      if (key === workspaceTabsStorageKey) throw new Error("read denied");
      return null;
    });
    const write = vi.spyOn(Storage.prototype, "setItem");
    openWorkspaceFile("read-failure", { path: "local.ts" });
    expect(write).not.toHaveBeenCalled();
    write.mockRestore();
    read.mockRestore();
    expect(localStorage.getItem(workspaceTabsStorageKey)).toBe(original);
    expect(getThreadWorkspaceState("read-failure").tabs).toMatchObject([{ path: "existing.ts" }, { path: "local.ts" }]);
  });

  it("uses the latest saved tabs even when another window's clock is behind", () => {
    openWorkspaceFile("older-clock", { path: "local.ts" });
    const saved = JSON.parse(localStorage.getItem(workspaceTabsStorageKey)!);
    const key = Object.keys(saved.threads).find((item) => JSON.parse(item).at(-1) === "older-clock")!;
    saved.threads[key].tabs[0].path = "remote.ts";
    saved.threads[key].updatedAt = 1;
    localStorage.setItem(workspaceTabsStorageKey, JSON.stringify(saved));
    expect(getThreadWorkspaceState("older-clock").tabs).toMatchObject([{ path: "remote.ts" }]);
    delete saved.threads[key];
    localStorage.setItem(workspaceTabsStorageKey, JSON.stringify(saved));
    expect(getThreadWorkspaceState("older-clock").tabs).toEqual([]);
  });

  it("keeps a single records tab per task and restores it alongside file tabs", () => {
    openWorkspaceFile("records-task", { path: "README.md", line: 4 }, "historical-run");
    focusWorkspaceSingletonTab("records-task", "records");
    focusWorkspaceSingletonTab("records-task", "records");
    resetWorkspaceTabsMemoryCache();
    expect(getThreadWorkspaceState("records-task")).toMatchObject({
      activeTabID: "records", open: true, lastFileTabID: "file:1",
      tabs: [{ id: "file:1", kind: "file", path: "README.md", line: 4, runID: "historical-run" },
        { id: "records", kind: "records" }],
    });
    expect(getThreadWorkspaceState("other-task").tabs).toEqual([]);
    toggleWorkspaceSingletonTab("records-task", "records");
    expect(getThreadWorkspaceState("records-task").open).toBe(false);
    focusWorkspaceFiles("records-task");
    expect(getThreadWorkspaceState("records-task").activeTabID).toBe("file:1");
  });

  it("preserves open file location when switching to terminal and returning via top Files trigger", () => {
    openWorkspaceFile("thread-1", { path: "web/src/main.tsx", line: 28 }, "run-1");
    expect(getThreadWorkspaceState("thread-1")).toMatchObject({
      activeTabID: "file:1",
      lastFileTabID: "file:1",
      open: true,
      tabs: [{ id: "file:1", kind: "file", path: "web/src/main.tsx", line: 28, runID: "run-1" }],
    });

    toggleWorkspaceSingletonTab("thread-1", "terminal");
    expect(getThreadWorkspaceState("thread-1").activeTabID).toBe("terminal");

    toggleWorkspaceFiles("thread-1", "run-1");
    const afterReturn = getThreadWorkspaceState("thread-1");
    expect(afterReturn.open).toBe(true);
    expect(afterReturn.activeTabID).toBe("file:1");
    expect(afterReturn.tabs).toEqual([
      { id: "file:1", kind: "file", path: "web/src/main.tsx", line: 28, runID: "run-1" },
      { id: "terminal", kind: "terminal" },
    ]);
  });

  it("supports multiple simultaneous file tabs and updates navigated paths without resetting siblings", () => {
    openWorkspaceFile("thread-multi", { path: "src/a.ts", line: 10 }, "run-a");
    openWorkspaceFile("thread-multi", { path: "src/b.ts", line: 42 }, "run-a");
    addWorkspaceFileTab("thread-multi", "run-a");

    let state = getThreadWorkspaceState("thread-multi");
    expect(state.tabs.map((tab) => tab.id)).toEqual(["file:1", "file:2", "file:3"]);
    expect(state.activeTabID).toBe("file:3");

    updateWorkspaceFileTabPath("thread-multi", "file:3", "docs/README.md");
    selectWorkspaceTab("thread-multi", "file:1");
    state = getThreadWorkspaceState("thread-multi");
    expect(state.activeTabID).toBe("file:1");
    expect(state.lastFileTabID).toBe("file:1");
    expect(state.tabs).toEqual([
      { id: "file:1", kind: "file", path: "src/a.ts", line: 10, runID: "run-a" },
      { id: "file:2", kind: "file", path: "src/b.ts", line: 42, runID: "run-a" },
      { id: "file:3", kind: "file", path: "docs/README.md", runID: "run-a" },
    ]);
  });

  it("isolates per-chat workspace state and restores from localStorage after memory cache reset", () => {
    openWorkspaceFile("chat-a", { path: "cmd/main.go", line: 12 }, "run-a");
    focusWorkspaceSingletonTab("chat-a", "terminal");
    setWorkspaceTerminalSession("chat-a", "uterm-a");
    setWorkspaceTerminalShell("chat-a", "PowerShell 7");
    setWorkspaceWidth("chat-a", 560);
    toggleWorkspaceFullView("chat-a");

    openWorkspaceFile("chat-b", { path: "README.md" }, "run-b");
    collapseWorkspacePanel("chat-b");

    resetWorkspaceTabsMemoryCache();

    const restoredA = getThreadWorkspaceState("chat-a");
    expect(restoredA).toMatchObject({
      activeTabID: "terminal",
      lastFileTabID: "file:1",
      open: true,
      fullView: true,
      width: 560,
      terminalSessionID: "uterm-a",
      hadTerminalSession: true,
      terminalShellName: "PowerShell 7",
    });
    expect(restoredA.tabs).toEqual([
      { id: "file:1", kind: "file", path: "cmd/main.go", line: 12, runID: "run-a" },
      { id: "terminal", kind: "terminal", shellName: "PowerShell 7" },
    ]);

    const restoredB = getThreadWorkspaceState("chat-b");
    expect(restoredB).toMatchObject({
      activeTabID: "file:1",
      open: false,
      fullView: false,
      width: defaultWorkspaceWidth,
      terminalSessionID: "",
      hadTerminalSession: false,
    });
  });

  it.each([1000, 900])("keeps unsaved workspace changes when storage fails and the clock is %i", (nextTime) => {
    const now = vi.spyOn(Date, "now").mockReturnValue(1000);
    const threadID = `storage-failure-${nextTime}`;
    try {
      openWorkspaceFile(threadID, { path: "src/first.ts" }, "run-1");
      now.mockReturnValue(nextTime);
      const write = vi.spyOn(Storage.prototype, "setItem").mockImplementation(() => { throw new Error("quota"); });
      try {
        openWorkspaceFile(threadID, { path: "src/second.ts" }, "run-1");
        expect(getThreadWorkspaceState(threadID)).toMatchObject({
          activeTabID: "file:2",
          tabs: [{ path: "src/first.ts" }, { path: "src/second.ts" }],
        });
        setWorkspaceWidth(threadID, 560);
        expect(getThreadWorkspaceState(threadID)).toMatchObject({ activeTabID: "file:2", width: 560 });
      } finally {
        write.mockRestore();
      }
      setWorkspaceWidth(threadID, 600);
      resetWorkspaceTabsMemoryCache();
      expect(getThreadWorkspaceState(threadID)).toMatchObject({
        activeTabID: "file:2", width: 600,
        tabs: [{ path: "src/first.ts" }, { path: "src/second.ts" }],
      });
    } finally {
      now.mockRestore();
    }
  });

  it("accepts a persisted update from another window when timestamps tie", () => {
    const now = vi.spyOn(Date, "now").mockReturnValue(1000);
    try {
      const threadID = "storage-external-update";
      openWorkspaceFile(threadID, { path: "src/first.ts" });
      const persisted = JSON.parse(localStorage.getItem(workspaceTabsStorageKey)!);
      const key = Object.keys(persisted.threads).find((value) => JSON.parse(value).at(-1) === threadID)!;
      persisted.threads[key].width = 700;
      localStorage.setItem(workspaceTabsStorageKey, JSON.stringify(persisted));
      expect(getThreadWorkspaceState(threadID).width).toBe(700);
    } finally {
      now.mockRestore();
    }
  });

  it("distinguishes collapsing panel, closing terminal tab, and clearing ended terminal session", () => {
    openWorkspaceFile("thread-term", { path: "src/app.tsx" }, "run-1");
    focusWorkspaceSingletonTab("thread-term", "terminal");
    setWorkspaceTerminalSession("thread-term", "uterm-live");
    setWorkspaceTerminalShell("thread-term", "pwsh");

    // 1. Collapse panel: tabs and terminal session stay intact.
    collapseWorkspacePanel("thread-term");
    expect(getThreadWorkspaceState("thread-term")).toMatchObject({
      open: false,
      activeTabID: "terminal",
      terminalSessionID: "uterm-live",
      hadTerminalSession: true,
    });

    // 2. Reopen and close terminal tab: tab is removed, running session ID stays bound for reattachment.
    focusWorkspaceSingletonTab("thread-term", "terminal");
    closeWorkspaceTab("thread-term", "terminal");
    const afterTabClose = getThreadWorkspaceState("thread-term");
    expect(afterTabClose.tabs.map((tab) => tab.id)).toEqual(["file:1"]);
    expect(afterTabClose.activeTabID).toBe("file:1");
    expect(afterTabClose.terminalSessionID).toBe("uterm-live");
    expect(afterTabClose.hadTerminalSession).toBe(true);

    // 3. Reopening terminal reattaches to uterm-live with remembered shell label.
    focusWorkspaceSingletonTab("thread-term", "terminal");
    expect(getThreadWorkspaceState("thread-term")).toMatchObject({
      activeTabID: "terminal",
      terminalSessionID: "uterm-live",
      tabs: [
        { id: "file:1", kind: "file", path: "src/app.tsx", runID: "run-1" },
        { id: "terminal", kind: "terminal", shellName: "pwsh" },
      ],
    });

    // 4. Explicit stop or app restart clears sessionID while retaining hadTerminalSession for Restart UI.
    setWorkspaceTerminalSession("thread-term", "");
    expect(getThreadWorkspaceState("thread-term")).toMatchObject({
      terminalSessionID: "",
      hadTerminalSession: true,
    });
  });

  it("clamps panel width and sanitizes malformed persisted payloads safely", () => {
    focusWorkspaceFiles("thread-width", "run-1");
    setWorkspaceWidth("thread-width", 120);
    expect(getThreadWorkspaceState("thread-width").width).toBe(minWorkspaceWidth);
    setWorkspaceWidth("thread-width", 2400);
    expect(getThreadWorkspaceState("thread-width").width).toBe(maxWorkspaceWidth);

    localStorage.setItem(workspaceTabsStorageKey, "{broken-json");
    resetWorkspaceTabsMemoryCache();
    expect(getThreadWorkspaceState("thread-width")).toMatchObject({
      tabs: [],
      open: false,
      fullView: false,
      width: defaultWorkspaceWidth,
    });
  });

  it("keeps snapshots referentially stable when setters receive unchanged values", () => {
    focusWorkspaceSingletonTab("thread-stable", "terminal");
    setWorkspaceTerminalSession("thread-stable", "uterm-stable");
    setWorkspaceTerminalShell("thread-stable", "Windows PowerShell 5.1");
    setWorkspaceWidth("thread-stable", 500);

    const before = getThreadWorkspaceState("thread-stable");
    let notifications = 0;
    const onChanged = () => {
      notifications += 1;
    };
    window.addEventListener(workspaceTabsChangedEvent, onChanged);
    try {
      setWorkspaceTerminalSession("thread-stable", "uterm-stable");
      setWorkspaceTerminalShell("thread-stable", "Windows PowerShell 5.1");
      setWorkspaceWidth("thread-stable", 500);
      expect(notifications).toBe(0);
      expect(getThreadWorkspaceState("thread-stable")).toBe(before);

      // Verify that actual changes do fire the notification
      setWorkspaceWidth("thread-stable", 520);
      expect(notifications).toBe(1);
    } finally {
      window.removeEventListener(workspaceTabsChangedEvent, onChanged);
    }
  });

  it("protects file tabs with uncommitted DraftDocument drafts from being evicted", () => {
    for (let i = 1; i <= 12; i++) {
      openWorkspaceFile("thread-real-drafts", { path: `src/file-${i}.ts` }, "run-1");
    }
    const stateBefore = getThreadWorkspaceState("thread-real-drafts");
    expect(stateBefore.tabs.length).toBe(12);

    // Create a real uncommitted draft using DraftDocument
    const scope: DraftScope = {
      key: `file-edit:${JSON.stringify(["run-1", "src/file-1.ts"])}`,
      workspaceID: "ws-1",
    };
    const doc = new DraftDocument(null);
    doc.update(scope, { text: "edited text", files: [], images: [] }, null);
    const read = doc.read(scope);
    expect(read.snapshot.text).toBe("edited text");

    // Attempt to open 13th file
    openWorkspaceFile("thread-real-drafts", { path: "src/file-13.ts" }, "run-1");
    const stateAfter = getThreadWorkspaceState("thread-real-drafts");

    // All 12 files are protected; file-1.ts is never evicted
    expect(stateAfter.tabs.length).toBe(12);
    expect(stateAfter.tabs.some((t) => t.kind === "file" && t.path === "src/file-1.ts")).toBe(true);
    expect(stateAfter.notice).toBe(maxWorkspaceTabsLimitNotice);
  });

  it("prevents state inconsistency when all 12 tabs are protected and handles 13th tab addition gracefully", () => {
    for (let i = 1; i <= 12; i++) {
      openWorkspaceFile("thread-consistency", { path: `src/mod-${i}.ts` }, "run-1");
    }
    const state12 = getThreadWorkspaceState("thread-consistency");
    expect(state12.tabs.length).toBe(12);
    expect(state12.activeTabID).toBe("file:12");

    // Attempting to open 13th file does NOT corrupt activeTabID or drop tabs silently
    openWorkspaceFile("thread-consistency", { path: "src/mod-13.ts" }, "run-1");
    const stateAttempt = getThreadWorkspaceState("thread-consistency");
    expect(stateAttempt.tabs.length).toBe(12);
    expect(stateAttempt.activeTabID).toBe("file:12"); // activeTabID is preserved, not orphaned!
    expect(stateAttempt.tabs.some((t) => t.id === stateAttempt.activeTabID)).toBe(true);
    expect(stateAttempt.notice).toBe(maxWorkspaceTabsLimitNotice);

    // Dismissing notice clears it
    clearWorkspaceNotice("thread-consistency");
    expect(getThreadWorkspaceState("thread-consistency").notice).toBeUndefined();

    // Closing a tab frees a slot and clears notice
    closeWorkspaceTab("thread-consistency", "file:1");
    expect(getThreadWorkspaceState("thread-consistency").tabs.length).toBe(11);
    expect(getThreadWorkspaceState("thread-consistency").tabs.some((t) => t.id === "file:1")).toBe(false);

    // Now opening the 13th file succeeds without exceeding 12 tabs
    openWorkspaceFile("thread-consistency", { path: "src/mod-13.ts" }, "run-1");
    const finalState = getThreadWorkspaceState("thread-consistency");
    expect(finalState.tabs.length).toBe(12);
    expect(finalState.tabs.some((t) => t.kind === "file" && t.path === "src/mod-13.ts")).toBe(true);
    expect(finalState.activeTabID).toBe("file:13");
  });

  it("protects file tabs safely even when localStorage fails and drafts exist purely in memory", () => {
    for (let i = 1; i <= 12; i++) {
      openWorkspaceFile("thread-mem-draft", { path: `src/page-${i}.tsx` }, "run-mem");
    }
    expect(getThreadWorkspaceState("thread-mem-draft").tabs.length).toBe(12);

    // Simulate failing recovery store where writes throw
    const failingStore = {
      read: vi.fn(),
      write: vi.fn().mockImplementation(() => {
        throw new Error("localStorage write failed (e.g. QuotaExceededError)");
      }),
      remove: vi.fn(),
      entries: vi.fn().mockReturnValue([]),
      assertReadable: vi.fn(),
      reject: vi.fn(),
      markUnsaved: vi.fn(),
      unsavedWarning: vi.fn().mockReturnValue("Write failed"),
    };
    const scope: DraftScope = {
      key: `file-edit:${JSON.stringify(["run-mem", "src/page-1.tsx"])}`,
      workspaceID: "ws-mem",
    };
    const doc = new DraftDocument(failingStore as any);
    const updateRes = doc.update(scope, { text: "unsaved memory text", files: [], images: [] }, null);
    expect(updateRes.persisted).toBe(false);
    expect(doc.read(scope).snapshot.text).toBe("unsaved memory text");

    // Attempting to add a new file tab via addWorkspaceFileTab at limit
    addWorkspaceFileTab("thread-mem-draft", "run-mem");
    const state = getThreadWorkspaceState("thread-mem-draft");

    // All file tabs remain intact, page-1.tsx is never evicted
    expect(state.tabs.length).toBe(12);
    expect(state.tabs.some((t) => t.kind === "file" && t.path === "src/page-1.tsx")).toBe(true);
    expect(state.notice).toBe(maxWorkspaceTabsLimitNotice);
  });

  it("replaces clean directory browsing tabs when reaching the 12-tab limit", () => {
    for (let i = 1; i <= 11; i++) {
      openWorkspaceFile("thread-dir-replace", { path: `src/comp-${i}.tsx` });
    }
    addWorkspaceFileTab("thread-dir-replace"); // adds a clean directory tab "."
    const state = getThreadWorkspaceState("thread-dir-replace");
    expect(state.tabs.length).toBe(12);
    expect(state.tabs.some((t) => t.kind === "file" && (!t.path || t.path === "."))).toBe(true);

    // Opening a new real file should safely replace the clean directory tab
    openWorkspaceFile("thread-dir-replace", { path: "src/comp-12.tsx" });
    const nextState = getThreadWorkspaceState("thread-dir-replace");
    expect(nextState.tabs.length).toBe(12);
    expect(nextState.tabs.some((t) => t.kind === "file" && t.path === "src/comp-12.tsx")).toBe(true);
    expect(nextState.notice).toBeUndefined();
  });
});
