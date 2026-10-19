import { useEffect, useState } from "react";
import type { APIClient } from "../api/client";
import type { WorkspaceView } from "../api/types";
import { useV2PersistentState, useV2RecoveryStore } from "./recovery-storage";

interface DraftWorkspace { operationID: string; workspace?: WorkspaceView }
const slotKey = "projectless-draft-workspace";
const validSlot = (value: unknown): value is DraftWorkspace => {
  if (!value || typeof value !== "object" || !("operationID" in value) ||
    typeof value.operationID !== "string" || !/^[\w-]{16,128}$/u.test(value.operationID)) return false;
  if (!("workspace" in value)) return true;
  const workspace = value.workspace;
  return !!workspace && typeof workspace === "object" && "id" in workspace &&
    typeof workspace.id === "string" && /^[\w.-]{1,256}$/u.test(workspace.id) &&
    "name" in workspace && typeof workspace.name === "string" &&
    "created_at" in workspace && typeof workspace.created_at === "string";
};

// Reserve before uploading attachments; retries and reloads use the same key.
export function useProjectlessWorkspace(client: APIClient, active: boolean) {
  const store = useV2RecoveryStore();
  const [saved, setSaved] = useV2PersistentState<unknown>(slotKey, null);
  const slot = validSlot(saved) ? saved : null;
  const invalid = saved !== null && !slot;
  const [error, setError] = useState("");
  const [attempt, setAttempt] = useState(0);
  useEffect(() => {
    if (!client.hasProjectlessWorkspaces || !active || invalid || slot?.workspace) return;
    if (!slot) {
      setSaved({ operationID: crypto.randomUUID() });
      return;
    }
    const controller = new AbortController();
    setError("");
    void (async () => {
      if (store) {
        store.assertReadable(slotKey);
        const stored = store.read<unknown>(slotKey, null);
        if (!validSlot(stored) || stored.operationID !== slot.operationID) {
          throw new Error("工作目录请求尚未保存，请保留当前页面后重试。");
        }
      }
      const result = await client.createProjectlessWorkspace(`v2-local-workspace-${slot.operationID}`, controller.signal);
      if (!controller.signal.aborted) setSaved((current: unknown) =>
        validSlot(current) && current.operationID === slot.operationID ? { ...current, workspace: result.workspace } : current);
    })().catch((reason: unknown) => {
      if (!controller.signal.aborted) setError(reason instanceof Error ? reason.message : "无法准备聊天工作目录。");
    });
    return () => controller.abort();
  }, [client, active, invalid, slot?.operationID, slot?.workspace?.id, store, attempt, setSaved]);
  return {
    workspaceID: slot?.workspace?.id ?? "",
    pending: client.hasProjectlessWorkspaces && active && !slot?.workspace && !error && !invalid,
    error: invalid ? "保存的工作目录请求无法读取，原记录已保留。" : error,
    retry: () => setAttempt((value) => value + 1),
    consume: (workspaceID: string) => setSaved((current: unknown) =>
      validSlot(current) && current.workspace?.id === workspaceID ? null : current),
  };
}
