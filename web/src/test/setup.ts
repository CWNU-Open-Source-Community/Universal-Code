import "@testing-library/jest-dom/vitest";
import { afterEach, vi } from "vitest";
import type { BrowserStorageRecordResult } from "../lib/browser-storage-record";
import { resetWorkspaceTabsMemoryCache, workspaceTabsStorageKey } from "../v2/workspace-tabs";

// jsdom has no IndexedDB. Unit tests simulate a transaction; the browser journey
// exercises real commits and cross-window notifications.
vi.mock("../lib/browser-storage-record", () => ({
  updateBrowserStorageRecord: vi.fn((name: string, update: (value: string | null) => string | undefined,
    finish: (result: BrowserStorageRecordResult) => void, key?: string) => {
    const storageKey = key === undefined ? name : name + ":" + key;
    let result: BrowserStorageRecordResult;
    try {
      const current = localStorage.getItem(storageKey);
      const next = update(current);
      if (next !== undefined) localStorage.setItem(storageKey, next);
      result = { ok: true, value: next ?? current, written: next !== undefined };
    } catch {
      result = { ok: false };
    }
    finish(result);
  }),
}));

afterEach(() => {
  try {
    localStorage.removeItem(workspaceTabsStorageKey);
  } catch {
    // Tests that stub storage failures keep their stub intact.
  }
  resetWorkspaceTabsMemoryCache();
});
