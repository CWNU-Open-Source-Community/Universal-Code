import type { LocaleText } from "./locale";
import type { ThreadExecutionView } from "../api/types";

/** Current Thread activity only; a Run's open lifecycle is not execution evidence. */
export function threadActivityLabel({ threadID, execution, readable, error = false }: {
  threadID: string;
  execution?: ThreadExecutionView;
  readable: boolean;
  error?: boolean;
}, t: LocaleText = (chinese) => chinese): string {
  if (!readable) return t("活动状态未提供", "Activity status unavailable");
  if (error) return t("状态读取失败", "Could not load activity status");
  if (execution && execution.thread_id !== threadID) return t("活动状态未知", "Activity status unknown");
  if (!execution) return t("正在同步状态", "Syncing status");
  switch (execution.state) {
    case "running": return t("正在工作", "Working");
    case "stopping": return t("正在停止", "Stopping");
    case "stop_failed": return t("停止未完成", "Stop incomplete");
    case "idle": return t("等待新消息", "Waiting for a new message");
    default: return t("活动状态未知", "Activity status unknown");
  }
}
