import { useCallback, useMemo, useSyncExternalStore } from "react";
import type { LocaleText } from "../lib/locale";
import { useConnectionStore } from "../state/connection";

export const workspaceTabsStorageKey = "universal-code.workspace-tabs.v2";
const legacyWorkspaceTabsStorageKey = "universal-code.workspace-tabs.v1";
export const workspaceTabsChangedEvent = "universal-code:workspace-tabs";
const changedEvent = workspaceTabsChangedEvent;

export const defaultWorkspaceWidth = 440;
export const minWorkspaceWidth = 340;
export const maxWorkspaceWidth = 960;
const maxTabsPerThread = 12;

export type WorkspaceFileTab = {
  id: string;
  kind: "file";
  path: string;
  line?: number;
  runID?: string;
};

export type WorkspaceTerminalTab = {
  id: "terminal";
  kind: "terminal";
  shellName?: string;
};

export type WorkspacePreviewTab = {
  id: "preview";
  kind: "preview";
};

export type WorkspaceBrowserTab = { id: "browser"; kind: "browser" };

export type WorkspaceReviewTab = {
  id: "review";
  kind: "review";
};

export type WorkspaceRecordsTab = {
  id: "records";
  kind: "records";
};

export type WorkspaceTab =
  | WorkspaceFileTab
  | WorkspaceTerminalTab
  | WorkspacePreviewTab
  | WorkspaceBrowserTab
  | WorkspaceReviewTab
  | WorkspaceRecordsTab;

export const maxWorkspaceTabsLimitNotice = "已达到 12 个工作区标签上限，请先关闭暂不使用的标签。";
export const workspaceStorageConflictNotice = "另一窗口修改了工作区标签。本窗口的未保存状态已保留，请选择要使用的版本。";
export const workspaceStorageConflictChangedNotice = "另一窗口再次修改了工作区标签，请重新选择要使用的版本。";
export const workspaceLegacyTabsNotice = "发现旧版工作区标签，但无法确认其所属连接。确认属于当前连接后可恢复，恢复会替换当前标签。";
const workspaceStorageFailureNotice = "无法保存工作区标签；当前状态仅保留在本窗口。";

export interface ThreadWorkspaceState {
  tabs: WorkspaceTab[];
  activeTabID: string | null;
  lastFileTabID: string | null;
  open: boolean;
  fullView: boolean;
  width: number;
  terminalSessionID: string;
  hadTerminalSession: boolean;
  terminalShellName?: string;
  nextFileSeq: number;
  notice?: string;
  updatedAt: number;
}

interface PersistedWorkspaceStore {
  version: 1;
  threads: Record<string, ThreadWorkspaceState>;
}

export const clampWorkspaceWidth = (width: number, maximum = maxWorkspaceWidth) =>
  Math.max(minWorkspaceWidth, Math.min(Math.max(minWorkspaceWidth, maximum), Math.round(width)));

const emptyTabs: WorkspaceTab[] = [];

const emptyWorkspaceStateSingleton: ThreadWorkspaceState = Object.freeze({
  tabs: emptyTabs,
  activeTabID: null,
  lastFileTabID: null,
  open: false,
  fullView: false,
  width: defaultWorkspaceWidth,
  terminalSessionID: "",
  hadTerminalSession: false,
  nextFileSeq: 1,
  updatedAt: 0,
});

export function emptyThreadWorkspaceState(): ThreadWorkspaceState {
  return emptyWorkspaceStateSingleton;
}

const memoryFallback = new Map<string, ThreadWorkspaceState>();
const failedBaselines = new Map<string, string>();
const conflictSnapshots = new Map<string, string>();
let cachedRaw: string | null | undefined = undefined;
let storageReadable = true;
let cachedParsed: Record<string, ThreadWorkspaceState> = {};

function workspaceThreadKey(threadID: string): { key: string; persistent: boolean } {
  const connection = useConnectionStore.getState();
  const storeID = connection.health?.data_store_id ?? "";
  const base = connection.apiBaseURL || import.meta.env.VITE_API_BASE_URL || "/api/v1";
  const transientScope = storeID ? null : connection.connectionGeneration;
  return { key: JSON.stringify([base, storeID, transientScope, threadID]), persistent: Boolean(storeID) };
}

// Bind delayed callbacks to the connection that started them.
export function workspaceScopeKey(threadID: string): string {
  return JSON.stringify([workspaceThreadKey(threadID).key, useConnectionStore.getState().connectionGeneration]);
}

function normalizeFilePath(path: string): string {
  if (!path || path === ".") return ".";
  return path.replace(/^\.\/+/u, "") || ".";
}

function sanitizeTab(raw: unknown): WorkspaceTab | null {
  if (!raw || typeof raw !== "object") return null;
  const candidate = raw as Record<string, unknown>;
  if (candidate.kind === "file" && typeof candidate.id === "string" && candidate.id.startsWith("file:")) {
    const path = typeof candidate.path === "string" ? normalizeFilePath(candidate.path) : ".";
    const line = typeof candidate.line === "number" && Number.isInteger(candidate.line) && candidate.line > 0
      ? candidate.line : undefined;
    const runID = typeof candidate.runID === "string" && candidate.runID ? candidate.runID : undefined;
    return { id: candidate.id, kind: "file", path, ...(line ? { line } : {}), ...(runID ? { runID } : {}) };
  }
  if (candidate.kind === "terminal") {
    const shellName = typeof candidate.shellName === "string" && candidate.shellName ? candidate.shellName : undefined;
    return { id: "terminal", kind: "terminal", ...(shellName ? { shellName } : {}) };
  }
  if (candidate.kind === "browser") return { id: "browser", kind: "browser" };
  if (candidate.kind === "preview") return { id: "preview", kind: "preview" };
  if (candidate.kind === "review") return { id: "review", kind: "review" };
  if (candidate.kind === "records") return { id: "records", kind: "records" };
  return null;
}

function sanitizeThreadState(raw: unknown): ThreadWorkspaceState | null {
  if (!raw || typeof raw !== "object") return null;
  const candidate = raw as Record<string, unknown>;
  const seen = new Set<string>();
  const tabs: WorkspaceTab[] = [];
  if (Array.isArray(candidate.tabs)) {
    for (const item of candidate.tabs) {
      const tab = sanitizeTab(item);
      if (!tab || seen.has(tab.id)) continue;
      seen.add(tab.id);
      tabs.push(tab);
      if (tabs.length >= maxTabsPerThread) break;
    }
  }
  let maxSeq = 0;
  for (const tab of tabs) {
    if (tab.kind === "file") {
      const parsed = Number.parseInt(tab.id.slice("file:".length), 10);
      if (Number.isFinite(parsed) && parsed > maxSeq) maxSeq = parsed;
    }
  }
  const nextFileSeq = typeof candidate.nextFileSeq === "number" && Number.isInteger(candidate.nextFileSeq) &&
    candidate.nextFileSeq > maxSeq ? candidate.nextFileSeq : maxSeq + 1;
  const activeCandidate = typeof candidate.activeTabID === "string" ? candidate.activeTabID : null;
  const activeTabID = activeCandidate && seen.has(activeCandidate)
    ? activeCandidate : (tabs.at(-1)?.id ?? null);
  const lastFileCandidate = typeof candidate.lastFileTabID === "string" ? candidate.lastFileTabID : null;
  const fallbackFileTab = [...tabs].reverse().find((tab) => tab.kind === "file")?.id ?? null;
  const lastFileTabID = lastFileCandidate && tabs.some((tab) => tab.id === lastFileCandidate && tab.kind === "file")
    ? lastFileCandidate : fallbackFileTab;
  const width = typeof candidate.width === "number" && Number.isFinite(candidate.width)
    ? clampWorkspaceWidth(candidate.width) : defaultWorkspaceWidth;
  const terminalSessionID = typeof candidate.terminalSessionID === "string" ? candidate.terminalSessionID : "";
  const hadTerminalSession = Boolean(candidate.hadTerminalSession || terminalSessionID);
  const terminalShellName = typeof candidate.terminalShellName === "string" && candidate.terminalShellName
    ? candidate.terminalShellName : undefined;
  const notice = typeof candidate.notice === "string" && candidate.notice
    ? candidate.notice : undefined;
  const open = Boolean(candidate.open) && activeTabID !== null;
  const fullView = Boolean(candidate.fullView) && tabs.length > 0;
  const updatedAt = typeof candidate.updatedAt === "number" && Number.isFinite(candidate.updatedAt)
    ? candidate.updatedAt : 0;
  return {
    tabs,
    activeTabID,
    lastFileTabID,
    open,
    fullView,
    width,
    terminalSessionID,
    hadTerminalSession,
    ...(terminalShellName ? { terminalShellName } : {}),
    nextFileSeq,
    ...(notice ? { notice } : {}),
    updatedAt,
  };
}

function readRawStorage(): string | null | undefined {
  try {
    return localStorage.getItem(workspaceTabsStorageKey);
  } catch {
    return undefined;
  }
}

function readLegacyThreadState(threadID: string): ThreadWorkspaceState | null {
  try {
    const raw = localStorage.getItem(legacyWorkspaceTabsStorageKey);
    if (!raw) return null;
    const parsed: unknown = JSON.parse(raw);
    if (!parsed || typeof parsed !== "object" || (parsed as { version?: unknown }).version !== 1) return null;
    const threads = (parsed as { threads?: unknown }).threads;
    if (!threads || typeof threads !== "object" || !Object.hasOwn(threads, threadID)) return null;
    return sanitizeThreadState((threads as Record<string, unknown>)[threadID]);
  } catch { return null; }
}

function readAllPersisted(): Record<string, ThreadWorkspaceState> {
  const raw = readRawStorage();
  if (raw === undefined) {
    storageReadable = false;
    return cachedParsed;
  }
  if (raw === cachedRaw) {
    storageReadable = true;
    return cachedParsed;
  }
  if (raw === null) {
    cachedRaw = raw;
    cachedParsed = {};
    storageReadable = true;
    return cachedParsed;
  }
  try {
    const parsed: unknown = JSON.parse(raw);
    if (!parsed || typeof parsed !== "object" || Array.isArray(parsed) || (parsed as { version?: unknown }).version !== 1) {
      throw new Error("invalid workspace storage");
    }
    const threadsRaw = (parsed as { threads?: unknown }).threads;
    if (!threadsRaw || typeof threadsRaw !== "object" || Array.isArray(threadsRaw)) {
      throw new Error("invalid workspace threads");
    }
    const next: Record<string, ThreadWorkspaceState> = {};
    for (const [threadID, value] of Object.entries(threadsRaw as Record<string, unknown>)) {
      if (!threadID) continue;
      const clean = sanitizeThreadState(value);
      if (clean) next[threadID] = clean;
    }
    cachedRaw = raw;
    cachedParsed = next;
    storageReadable = true;
    return cachedParsed;
  } catch {
    // Preserve both the original storage and the last readable in-memory view.
    storageReadable = false;
    return cachedParsed;
  }
}

export function getThreadWorkspaceState(threadID: string): ThreadWorkspaceState {
  if (!threadID) return emptyThreadWorkspaceState();
  const { key, persistent } = workspaceThreadKey(threadID);
  const persisted = persistent ? readAllPersisted()[key] : undefined;
  let fallback = memoryFallback.get(key);
  if (fallback && failedBaselines.has(key)) {
    if (storageReadable && JSON.stringify(persisted ?? null) !== failedBaselines.get(key)) {
      if (!conflictSnapshots.has(key)) conflictSnapshots.set(key, JSON.stringify(persisted ?? null));
      if (fallback.notice !== workspaceStorageConflictNotice && fallback.notice !== workspaceStorageConflictChangedNotice) {
        fallback = { ...fallback, notice: workspaceStorageConflictNotice };
        memoryFallback.set(key, fallback);
      }
    }
    return fallback;
  }
  // The last readable persisted snapshot remains authoritative on read failure,
  // including remote deletion or a writer whose clock is behind this one.
  // Unsaved local writes were handled above.
  if (persistent) return persisted ?? emptyThreadWorkspaceState();
  return fallback ?? emptyThreadWorkspaceState();
}

function writeThreadWorkspaceState(threadID: string, updater: (current: ThreadWorkspaceState) => ThreadWorkspaceState) {
  if (!threadID) return;
  const { key, persistent } = workspaceThreadKey(threadID);
  const current = getThreadWorkspaceState(threadID);
  const startingSnapshot = persistent ? JSON.stringify(cachedParsed[key] ?? null) : "";
  const candidate = updater(current);
  if (candidate === current) return;
  // A failed storage write leaves both the persisted snapshot and this in-memory
  // fallback. Advance past the snapshot we read even within the same millisecond
  // or after a clock adjustment, so the new state remains authoritative.
  const updatedAt = Math.max(Date.now(), current.updatedAt + 1);
  let updated = sanitizeThreadState({ ...candidate, updatedAt }) ?? emptyThreadWorkspaceState();
  const persistedThreads = persistent ? readAllPersisted() : {};
  const persisted = persistedThreads[key];
  if (persistent && !persisted && !memoryFallback.has(key) && readLegacyThreadState(threadID) && !updated.notice) {
    updated = { ...updated, notice: workspaceLegacyTabsNotice };
  }
  if (persistent && !storageReadable) {
    if (!failedBaselines.has(key)) failedBaselines.set(key, JSON.stringify(persisted ?? null));
    memoryFallback.set(key, { ...updated, notice: workspaceStorageFailureNotice });
    window.dispatchEvent(new Event(changedEvent));
    return;
  }
  if (persistent && !failedBaselines.has(key) && JSON.stringify(persisted ?? null) !== startingSnapshot) {
    failedBaselines.set(key, startingSnapshot);
    conflictSnapshots.set(key, JSON.stringify(persisted ?? null));
    memoryFallback.set(key, { ...updated, notice: workspaceStorageConflictNotice });
    window.dispatchEvent(new Event(changedEvent));
    return;
  }
  if (!persistent || (failedBaselines.has(key) && JSON.stringify(persisted ?? null) !== failedBaselines.get(key))) {
    updated = { ...updated, notice: persistent ? workspaceStorageConflictNotice : workspaceStorageFailureNotice };
    memoryFallback.set(key, updated);
    window.dispatchEvent(new Event(changedEvent));
    return;
  }
  if (updated.notice === workspaceStorageFailureNotice || updated.notice === workspaceStorageConflictNotice ||
      updated.notice === workspaceStorageConflictChangedNotice) {
    updated = { ...updated, notice: undefined };
  }
  memoryFallback.set(key, updated);
  const payload: PersistedWorkspaceStore = {
    version: 1,
    threads: { ...persistedThreads, [key]: updated },
  };
  try {
    const encoded = JSON.stringify(payload);
    localStorage.setItem(workspaceTabsStorageKey, encoded);
    cachedRaw = encoded;
    cachedParsed = payload.threads;
    failedBaselines.delete(key);
    conflictSnapshots.delete(key);
  } catch {
    if (!failedBaselines.has(key)) failedBaselines.set(key, JSON.stringify(persisted ?? null));
    memoryFallback.set(key, { ...updated, notice: workspaceStorageFailureNotice });
  }
  window.dispatchEvent(new Event(changedEvent));
}

function subscribeWorkspaceTabs(notify: () => void) {
  const onStorage = (event: StorageEvent) => {
    if (!event.key || event.key === workspaceTabsStorageKey) {
      cachedRaw = undefined;
      notify();
    }
  };
  window.addEventListener(changedEvent, notify);
  window.addEventListener("storage", onStorage);
  return () => {
    window.removeEventListener(changedEvent, notify);
    window.removeEventListener("storage", onStorage);
  };
}

export function resetWorkspaceTabsMemoryCache() {
  memoryFallback.clear();
  failedBaselines.clear();
  conflictSnapshots.clear();
  cachedRaw = undefined;
  cachedParsed = {};
  storageReadable = true;
}

export function resolveWorkspaceStorageConflict(threadID: string, choice: "local" | "remote") {
  const { key, persistent } = workspaceThreadKey(threadID);
  if (!persistent || !failedBaselines.has(key)) return;
  const remote = readAllPersisted()[key];
  if (!storageReadable) return;
  if (choice === "remote") {
    failedBaselines.delete(key);
    conflictSnapshots.delete(key);
    memoryFallback.delete(key);
    window.dispatchEvent(new Event(changedEvent));
    return;
  }
  const latestSnapshot = JSON.stringify(remote ?? null);
  if (conflictSnapshots.get(key) !== latestSnapshot) {
    conflictSnapshots.set(key, latestSnapshot);
    const local = memoryFallback.get(key);
    if (local) memoryFallback.set(key, { ...local, notice: workspaceStorageConflictChangedNotice });
    window.dispatchEvent(new Event(changedEvent));
    return;
  }
  // The explicit choice authorizes replacing the last observed snapshot.
  failedBaselines.set(key, latestSnapshot);
  writeThreadWorkspaceState(threadID, (state) => ({ ...state, notice: undefined }));
}

export function restoreLegacyWorkspaceTabs(threadID: string) {
  const legacy = readLegacyThreadState(threadID);
  if (!legacy) return;
  writeThreadWorkspaceState(threadID, () => ({ ...legacy, notice: undefined }));
}

export function hasLegacyWorkspaceTabs(threadID: string): boolean {
  return Boolean(readLegacyThreadState(threadID));
}

export function promptLegacyWorkspaceTabs(threadID: string) {
  if (!hasLegacyWorkspaceTabs(threadID)) return;
  writeThreadWorkspaceState(threadID, (state) => state.notice === workspaceLegacyTabsNotice
    ? state : { ...state, notice: workspaceLegacyTabsNotice });
}

function tryAddTab(
  tabs: WorkspaceTab[],
  created: WorkspaceTab,
): { tabs: WorkspaceTab[]; accepted: boolean } {
  if (tabs.length < maxTabsPerThread) {
    return { tabs: [...tabs, created], accepted: true };
  }
  // At limit: only clean directory browsing tabs (!path or path === ".") may be evicted/replaced.
  // Real file editor tabs carry active user editor state/drafts and are NEVER silently closed.
  const removableIndex = tabs.findIndex(
    (tab) => tab.kind === "file" && (!tab.path || tab.path === ".")
  );
  if (removableIndex === -1) {
    return { tabs, accepted: false };
  }
  const next = [...tabs];
  next.splice(removableIndex, 1, created);
  return { tabs: next, accepted: true };
}

export function toggleWorkspaceFiles(threadID: string, currentRunID?: string) {
  writeThreadWorkspaceState(threadID, (state) => {
    const active = state.tabs.find((tab) => tab.id === state.activeTabID);
    if (state.open && active?.kind === "file") {
      return { ...state, open: false };
    }
    return activateOrCreateFileTab(state, currentRunID);
  });
}

export function focusWorkspaceFiles(threadID: string, currentRunID?: string) {
  writeThreadWorkspaceState(threadID, (state) => activateOrCreateFileTab(state, currentRunID));
}

function activateOrCreateFileTab(state: ThreadWorkspaceState, currentRunID?: string): ThreadWorkspaceState {
  const remembered = state.lastFileTabID
    ? state.tabs.find((tab) => tab.id === state.lastFileTabID && tab.kind === "file")
    : undefined;
  const targetFile = remembered ?? [...state.tabs].reverse().find((tab) => tab.kind === "file");
  if (targetFile) {
    return {
      ...state,
      activeTabID: targetFile.id,
      lastFileTabID: targetFile.id,
      open: true,
    };
  }
  const id = `file:${state.nextFileSeq}`;
  const created: WorkspaceFileTab = {
    id,
    kind: "file",
    path: ".",
    ...(currentRunID ? { runID: currentRunID } : {}),
  };
  const result = tryAddTab(state.tabs, created);
  if (!result.accepted) {
    return {
      ...state,
      notice: maxWorkspaceTabsLimitNotice,
      open: true,
    };
  }
  return {
    ...state,
    tabs: result.tabs,
    activeTabID: id,
    lastFileTabID: id,
    open: true,
    notice: undefined,
    nextFileSeq: state.nextFileSeq + 1,
  };
}

export function addWorkspaceFileTab(threadID: string, currentRunID?: string) {
  writeThreadWorkspaceState(threadID, (state) => {
    const id = `file:${state.nextFileSeq}`;
    const created: WorkspaceFileTab = {
      id,
      kind: "file",
      path: ".",
      ...(currentRunID ? { runID: currentRunID } : {}),
    };
    const result = tryAddTab(state.tabs, created);
    if (!result.accepted) {
      return {
        ...state,
        notice: maxWorkspaceTabsLimitNotice,
        open: true,
      };
    }
    return {
      ...state,
      tabs: result.tabs,
      activeTabID: id,
      lastFileTabID: id,
      open: true,
      notice: undefined,
      nextFileSeq: state.nextFileSeq + 1,
    };
  });
}

export function openWorkspaceFile(
  threadID: string,
  target: { path: string; line?: number; runID?: string },
  currentRunID?: string,
) {
  writeThreadWorkspaceState(threadID, (state) => {
    const normalizedPath = normalizeFilePath(target.path);
    const effectiveRunID = target.runID || currentRunID;
    const existing = state.tabs.find((tab): tab is WorkspaceFileTab =>
      tab.kind === "file" &&
      tab.path === normalizedPath &&
      (tab.runID || currentRunID || "") === (effectiveRunID || ""));
    if (existing) {
      const tabs = state.tabs.map((tab) => tab.id === existing.id
        ? { ...existing, line: target.line, ...(effectiveRunID ? { runID: effectiveRunID } : {}) }
        : tab);
      return {
        ...state,
        tabs,
        activeTabID: existing.id,
        lastFileTabID: existing.id,
        open: true,
        notice: undefined,
      };
    }
    const id = `file:${state.nextFileSeq}`;
    const created: WorkspaceFileTab = {
      id,
      kind: "file",
      path: normalizedPath,
      ...(target.line ? { line: target.line } : {}),
      ...(effectiveRunID ? { runID: effectiveRunID } : {}),
    };
    const result = tryAddTab(state.tabs, created);
    if (!result.accepted) {
      return {
        ...state,
        notice: maxWorkspaceTabsLimitNotice,
        open: true,
      };
    }
    return {
      ...state,
      tabs: result.tabs,
      activeTabID: id,
      lastFileTabID: id,
      open: true,
      notice: undefined,
      nextFileSeq: state.nextFileSeq + 1,
    };
  });
}

export function updateWorkspaceFileTabPath(threadID: string, tabID: string, nextPath: string, expectedScope?: string) {
  if (expectedScope !== undefined && workspaceScopeKey(threadID) !== expectedScope) return;
  writeThreadWorkspaceState(threadID, (state) => {
    const normalized = normalizeFilePath(nextPath);
    let changed = false;
    const tabs = state.tabs.map((tab) => {
      if (tab.id !== tabID || tab.kind !== "file") return tab;
      if (tab.path === normalized && tab.line === undefined) return tab;
      changed = true;
      const { line: _removedLine, ...rest } = tab;
      return { ...rest, path: normalized };
    });
    return changed ? { ...state, tabs, lastFileTabID: tabID } : state;
  });
}

function ensureSingletonTab(
  state: ThreadWorkspaceState,
  kind: "terminal" | "preview" | "browser" | "review" | "records",
): { tabs: WorkspaceTab[]; activeTabID: string; notice?: string } {
  if (state.tabs.some((tab) => tab.id === kind)) {
    return { tabs: state.tabs, activeTabID: kind };
  }
  const added: WorkspaceTab = kind === "terminal"
    ? { id: "terminal", kind: "terminal", ...(state.terminalShellName ? { shellName: state.terminalShellName } : {}) }
    : kind === "preview"
      ? { id: "preview", kind: "preview" }
      : kind === "browser" ? { id: "browser", kind: "browser" }
      : kind === "review" ? { id: "review", kind: "review" } : { id: "records", kind: "records" };
  const result = tryAddTab(state.tabs, added);
  if (!result.accepted) {
    return {
      tabs: state.tabs,
      activeTabID: state.activeTabID ?? state.tabs[0]?.id ?? kind,
      notice: maxWorkspaceTabsLimitNotice,
    };
  }
  return { tabs: result.tabs, activeTabID: kind };
}

export function toggleWorkspaceSingletonTab(threadID: string, kind: "terminal" | "preview" | "browser" | "review" | "records") {
  writeThreadWorkspaceState(threadID, (state) => {
    if (state.open && state.activeTabID === kind) {
      return { ...state, open: false };
    }
    const ensured = ensureSingletonTab(state, kind);
    return {
      ...state,
      tabs: ensured.tabs,
      activeTabID: ensured.activeTabID,
      open: true,
      ...(ensured.notice ? { notice: ensured.notice } : { notice: undefined }),
    };
  });
}

export function focusWorkspaceSingletonTab(threadID: string, kind: "terminal" | "preview" | "browser" | "review" | "records") {
  writeThreadWorkspaceState(threadID, (state) => {
    const ensured = ensureSingletonTab(state, kind);
    return {
      ...state,
      tabs: ensured.tabs,
      activeTabID: ensured.activeTabID,
      open: true,
      ...(ensured.notice ? { notice: ensured.notice } : { notice: undefined }),
    };
  });
}

export function selectWorkspaceTab(threadID: string, tabID: string) {
  writeThreadWorkspaceState(threadID, (state) => {
    const target = state.tabs.find((tab) => tab.id === tabID);
    if (!target) return state;
    return {
      ...state,
      activeTabID: target.id,
      lastFileTabID: target.kind === "file" ? target.id : state.lastFileTabID,
      open: true,
    };
  });
}

export function closeWorkspaceTab(threadID: string, tabID: string) {
  writeThreadWorkspaceState(threadID, (state) => {
    const index = state.tabs.findIndex((tab) => tab.id === tabID);
    if (index === -1) return state;
    const nextTabs = state.tabs.filter((tab) => tab.id !== tabID);
    const nextActive = state.activeTabID === tabID
      ? (nextTabs[index]?.id ?? nextTabs[index - 1]?.id ?? null)
      : state.activeTabID;
    const nextActiveTab = nextTabs.find((tab) => tab.id === nextActive);
    const remainingLastFile = state.lastFileTabID === tabID
      ? ([...nextTabs].reverse().find((tab) => tab.kind === "file")?.id ?? null)
      : state.lastFileTabID;
    return {
      ...state,
      tabs: nextTabs,
      activeTabID: nextActive,
      lastFileTabID: nextActiveTab?.kind === "file" ? nextActiveTab.id : remainingLastFile,
      open: nextTabs.length > 0 ? state.open : false,
      fullView: nextTabs.length > 0 ? state.fullView : false,
      notice: undefined,
    };
  });
}

export function clearWorkspaceNotice(threadID: string) {
  writeThreadWorkspaceState(threadID, (state) => (state.notice ? { ...state, notice: undefined } : state));
}

export function collapseWorkspacePanel(threadID: string) {
  writeThreadWorkspaceState(threadID, (state) => (state.open ? { ...state, open: false } : state));
}

export function toggleWorkspaceFullView(threadID: string) {
  writeThreadWorkspaceState(threadID, (state) => (state.tabs.length > 0
    ? { ...state, open: true, fullView: !state.fullView }
    : state));
}

export function setWorkspaceFullView(threadID: string, fullView: boolean) {
  writeThreadWorkspaceState(threadID, (state) => (state.fullView === fullView ? state : { ...state, fullView }));
}

export function setWorkspaceWidth(threadID: string, width: number) {
  const clamped = clampWorkspaceWidth(width);
  writeThreadWorkspaceState(threadID, (state) => (state.width === clamped ? state : { ...state, width: clamped }));
}

export function setWorkspaceTerminalSession(threadID: string, sessionID: string, expectedSessionID?: string, expectedScope?: string) {
  if (expectedScope !== undefined && workspaceScopeKey(threadID) !== expectedScope) return;
  writeThreadWorkspaceState(threadID, (state) => {
    if (expectedSessionID !== undefined && state.terminalSessionID !== expectedSessionID) return state;
    const nextHad = Boolean(state.hadTerminalSession || sessionID);
    if (state.terminalSessionID === sessionID && state.hadTerminalSession === nextHad) return state;
    return {
      ...state,
      terminalSessionID: sessionID,
      hadTerminalSession: nextHad,
    };
  });
}

export function setWorkspaceTerminalShell(threadID: string, shellName: string, expectedScope?: string) {
  if (!shellName || (expectedScope !== undefined && workspaceScopeKey(threadID) !== expectedScope)) return;
  writeThreadWorkspaceState(threadID, (state) => {
    if (state.terminalShellName === shellName &&
      state.tabs.every((tab) => tab.kind !== "terminal" || tab.shellName === shellName)) {
      return state;
    }
    return {
      ...state,
      terminalShellName: shellName,
      tabs: state.tabs.map((tab) => tab.kind === "terminal" ? { ...tab, shellName } : tab),
    };
  });
}

export function workspaceTabTitle(tab: WorkspaceTab, t: LocaleText): string {
  switch (tab.kind) {
    case "file": {
      if (!tab.path || tab.path === ".") return t("文件", "Files");
      const trimmed = tab.path.replace(/\/+$/u, "");
      const base = trimmed.split("/").at(-1) || trimmed;
      return tab.line ? `${base}:${tab.line}` : base;
    }
    case "terminal":
      return tab.shellName || t("终端", "Terminal");
    case "browser":
      return t("浏览器", "Browser");
    case "preview":
      return t("应用预览", "App preview");
    case "review":
      return t("改动与交付", "Changes");
    case "records":
      return t("执行记录", "Execution records");
  }
}

export function workspaceTabTooltip(tab: WorkspaceTab, workspaceName: string, t: LocaleText): string {
  switch (tab.kind) {
    case "file":
      if (!tab.path || tab.path === ".") return t("工作区文件目录", "Workspace directory");
      return tab.line ? `${tab.path} (line ${tab.line})` : tab.path;
    case "terminal":
      return tab.shellName
        ? `${t("终端", "Terminal")} · ${tab.shellName} (${workspaceName})`
        : `${t("终端", "Terminal")} · ${workspaceName}`;
    case "browser":
      return t("浏览器", "Browser");
    case "preview":
      return t("应用预览", "App preview");
    case "review":
      return t("改动与交付", "Changes and delivery");
    case "records":
      return t("执行记录", "Execution records");
  }
}

export function workspaceSurfaceAriaLabel(kind: WorkspaceTab["kind"], t: LocaleText): string {
  switch (kind) {
    case "file":
      return t("工作区文件", "Workspace files");
    case "terminal":
      return t("任务终端", "Task terminal");
    case "browser":
      return t("浏览器", "Browser");
    case "preview":
      return t("应用预览", "App preview");
    case "review":
      return t("审阅任务改动", "Review task changes");
    case "records":
      return t("执行记录", "Execution records");
  }
}

export function workspaceCollapseAriaLabel(kind: WorkspaceTab["kind"], t: LocaleText): string {
  switch (kind) {
    case "file":
      return t("关闭文件面板", "Close file panel");
    case "terminal":
      return t("收起终端", "Collapse terminal");
    case "browser":
      return t("浏览器", "Browser");
    case "preview":
      return t("收起应用预览", "Close app preview");
    case "review":
      return t("关闭任务审阅", "Close task review");
    case "records":
      return t("收起执行记录", "Close execution records");
  }
}

export function useThreadWorkspaceState(threadID: string): ThreadWorkspaceState {
  const storeID = useConnectionStore((state) => state.health?.data_store_id ?? "");
  const baseURL = useConnectionStore((state) => state.apiBaseURL ?? "");
  const generation = useConnectionStore((state) => state.connectionGeneration);
  const getSnapshot = useCallback(() => getThreadWorkspaceState(threadID), [threadID, storeID, baseURL, generation]);
  const state = useSyncExternalStore(subscribeWorkspaceTabs, getSnapshot, emptyThreadWorkspaceState);
  return useMemo(() => state, [state]);
}
