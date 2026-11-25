import type { ThreadView } from "../../api/types";

export const executionLabels = {
  idle: ["空闲", "Idle"], running: ["执行中", "Running"], stopping: ["正在停止", "Stopping"], stop_failed: ["停止失败", "Stop failed"],
  waiting_approval: ["等待审批", "Awaiting approval"], paused: ["已暂停", "Paused"], completed: ["已完成", "Completed"], failed: ["执行失败", "Failed"],
  cancelled: ["已取消", "Cancelled"], unknown: ["状态未知", "Unknown"],
} as const;

export function executionState(thread: ThreadView, readFailed: boolean) {
  const value = thread.execution_state;
  return !readFailed && typeof value === "string" && Object.hasOwn(executionLabels, value) ? value : "unknown";
}

export const priorityGroups = [
  ["needs_input", "需用户输入", "Needs input"],
  ["running", "运行中", "Running"],
  ["unread", "未读", "Unread"],
  ["idle", "空闲", "Idle"],
  ["unknown", "状态未知", "Unknown"],
] as const;
export type Priority = typeof priorityGroups[number][0];

export function threadPriority(thread: ThreadView, readFailed: boolean, unread: boolean): Priority {
  // The list projects the current turn and pending approvals together, including for unopened chats.
  const state = executionState(thread, readFailed);
  if ((!readFailed && thread.composer_state === "waiting_approval") ||
    state === "waiting_approval" || state === "paused" || state === "stop_failed") return "needs_input";
  if (state === "running" || state === "stopping") return "running";
  if (unread) return "unread";
  return state === "unknown" ? "unknown" : "idle";
}
