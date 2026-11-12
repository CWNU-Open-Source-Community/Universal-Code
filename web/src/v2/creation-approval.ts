import type { APIClient } from "../api/client";
import type { ExecutionApprovalMode } from "./components/approval-mode-contract";

// The journal stores a preference; only a live UI confirmation can activate Full.
export async function applyCreationApproval(client: APIClient, threadID: string,
  mode?: ExecutionApprovalMode, confirmFull = false): Promise<void> {
  if (mode === undefined) return;
  if (mode === "full" && !confirmFull) throw new Error("请先确认完全访问权限，首条消息尚未发送。");
  const result = await client.changeThreadExecutionPermission(threadID, {
    mode, confirm_full: mode === "full", reason: "New conversation approval preference selection",
  }, `thread-creation-approval-${globalThis.crypto.randomUUID()}`);
  if (result.execution_permission.approval_mode !== mode ||
    (mode === "full" && result.execution_permission.full_activation !== "active")) {
    throw new Error("权限尚未生效，首条消息尚未发送。请重新确认权限后重试。");
  }
}
