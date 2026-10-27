import "@testing-library/jest-dom/vitest";
import { afterEach } from "vitest";
import { resetWorkspaceTabsMemoryCache, workspaceTabsStorageKey } from "../v2/workspace-tabs";

afterEach(() => {
  try {
    localStorage.removeItem(workspaceTabsStorageKey);
  } catch {
    // Tests that stub storage failures keep their stub intact.
  }
  resetWorkspaceTabsMemoryCache();
});
