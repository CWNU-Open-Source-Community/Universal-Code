import { useConnectionStore } from "../state/connection";
import type { SharedBrowserState } from "../lib/desktop-shared-browser";

const drafts = new Map<string, string>();
const adoptions = new Map<string, string>();
const key = (workspaceID: string, threadID = "") => JSON.stringify([useConnectionStore.getState().connectionGeneration, workspaceID, threadID]);

export function rememberDraftSharedBrowser(state: SharedBrowserState) {
  if (!state.thread_id) drafts.set(key(state.workspace_id), state.session_id);
}

// Called only at the successful thread-creation handoff for this exact draft.
export function bindDraftSharedBrowser(workspaceID: string, threadID: string) {
  const sessionID = drafts.get(key(workspaceID));
  if (sessionID && !adoptions.has(key(workspaceID, threadID))) {
    adoptions.set(key(workspaceID, threadID), sessionID);
    drafts.delete(key(workspaceID));
  }
}

export function draftSharedBrowserAdoption(workspaceID: string, threadID?: string): string | undefined {
  return threadID ? adoptions.get(key(workspaceID, threadID)) : undefined;
}
