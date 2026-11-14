import { useConnectionStore } from "../state/connection";
import { openSharedBrowser, type SharedBrowserState } from "../lib/desktop-shared-browser";
import { focusWorkspaceSingletonTab, getThreadWorkspaceState, setWorkspaceFullView, setWorkspaceWidth } from "./workspace-tabs";

const drafts = new Map<string, string>();
const adoptions = new Map<string, string>();
const key = (workspaceID: string, threadID = "") => JSON.stringify([useConnectionStore.getState().connectionGeneration, workspaceID, threadID]);

export function rememberDraftSharedBrowser(state: SharedBrowserState) {
  if (!state.thread_id) drafts.set(key(state.workspace_id), state.session_id);
}

// Called only at the successful thread-creation handoff for this exact draft.
export function bindDraftSharedBrowser(workspaceID: string, threadID: string, expectedGeneration = useConnectionStore.getState().connectionGeneration) {
  if (useConnectionStore.getState().connectionGeneration !== expectedGeneration) return;
  const sessionID = drafts.get(key(workspaceID));
  if (sessionID && !adoptions.has(key(workspaceID, threadID))) {
    adoptions.set(key(workspaceID, threadID), sessionID);
    drafts.delete(key(workspaceID));
    const draft = getThreadWorkspaceState(`draft:${workspaceID}`);
    if (draft.open) {
      focusWorkspaceSingletonTab(threadID, "browser");
      setWorkspaceWidth(threadID, draft.width);
      setWorkspaceFullView(threadID, draft.fullView);
    }
  }
}

export async function adoptDraftSharedBrowser(workspaceID: string, threadID: string, runID: string | undefined, expectedGeneration: number) {
  if (useConnectionStore.getState().connectionGeneration !== expectedGeneration) return;
  bindDraftSharedBrowser(workspaceID, threadID, expectedGeneration);
  const sessionID = draftSharedBrowserAdoption(workspaceID, threadID);
  if (sessionID) await openSharedBrowser({ workspace_id: workspaceID, thread_id: threadID,
    ...(runID ? { run_id: runID } : {}), adopt_session_id: sessionID });
}

export function draftSharedBrowserAdoption(workspaceID: string, threadID?: string): string | undefined {
  return threadID ? adoptions.get(key(workspaceID, threadID)) : undefined;
}
