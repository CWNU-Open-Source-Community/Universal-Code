import type { LocaleText } from "../../lib/locale";
import type { PublicModelStreamSnapshot, ThreadExecutionView, ThreadTranscriptItemView } from "../../api/types";
import type { PublicModelStreamStatus } from "../../hooks/use-public-model-stream";

export type AgentOrbMode = "waiting" | "searching" | "executing" | "composing";
export interface AgentActivity {
  label: string;
  mode?: AgentOrbMode;
  attention?: boolean;
}

interface ActivityInput {
  threadID: string;
  runID?: string;
  runStatus?: string;
  archived: boolean;
  execution?: ThreadExecutionView;
  executionReadable: boolean;
  executionError: boolean;
  submitting: boolean;
  reconciling: boolean;
  snapshot: PublicModelStreamSnapshot | null;
  streamStatus: PublicModelStreamStatus;
  progressError: boolean;
  transcript: ThreadTranscriptItemView[];
}

/** Presentation only. Never infer an execution from an open Run or generated tool arguments. */
export function projectAgentActivity(input: ActivityInput, t: LocaleText = (chinese) => chinese): AgentActivity | null {
  if (input.archived) return null;
  const execution = input.execution;
  if (input.executionError) return { label: t("状态读取失败", "Could not read activity status"), attention: true };
  if (execution && execution.thread_id !== input.threadID) return { label: t("活动状态未知", "Activity status unknown") };
  if (execution?.state === "stopping") return { label: t("正在停止", "Stopping") };
  if (execution?.state === "stop_failed") return { label: t("停止未完成", "Stopping did not finish"), attention: true };
  if (input.runStatus === "waiting_approval") return { label: t("等待批准", "Awaiting approval") };
  if (input.runStatus === "paused") return { label: t("已暂停", "Paused") };
  if (execution?.state !== "running") {
    if (input.reconciling) return { label: t("正在核对提交", "Checking submission") };
    if (input.submitting) return { label: t("正在发送消息", "Sending message") };
    if (!input.executionReadable) return { label: t("当前连接不支持读取 Agent 状态", "This connection cannot read Agent status") };
    if (!execution) return { label: t("正在同步状态", "Syncing status") };
    if (input.runStatus === "failed") return { label: t("本轮未完成", "This turn did not finish"), attention: true };
    // Confirmed idle wins over a cached live snapshot, including after cancellation.
    return null;
  }
  if (input.runID && ["completed", "failed", "cancelled", "archived"].includes(input.runStatus ?? "")) {
    return input.runStatus === "failed" ? { label: t("本轮未完成", "This turn did not finish"), attention: true } : null;
  }
  if (input.progressError) return { label: t("实时进度暂不可用", "Live progress is unavailable"), attention: true };

  const snapshot = input.snapshot?.call.run_id === input.runID ? input.snapshot : null;
  if (snapshot?.call.cancel_requested) return { label: t("正在停止", "Stopping") };
  const items = input.transcript.filter((item) => item.run_id === input.runID && item.durable && !item.provisional);
  // Durable sequence retains sub-millisecond ordering; Date.parse does not.
  const modelCalls = items.filter((item) => item.kind === "model_call");
  const latestModelSequence = Math.max(0, ...modelCalls.map((item) => item.sequence));
  const latestModelAt = Math.max(0, ...modelCalls.map((item) => Date.parse(item.created_at) || 0));
  const snapshotStartedAt = snapshot ? Date.parse(snapshot.call.started_at) : 0;
  const tools = new Map<string, ThreadTranscriptItemView>();
  for (const item of items) {
    if (item.kind !== "tool_call") continue;
    // Legacy event IDs differ at start/result and cannot establish tool pairing.
    const identity = item.durable_call_id || item.stream_call_id;
    if (!identity) continue;
    const previous = tools.get(identity);
    if (!previous || item.sequence >= previous.sequence) tools.set(identity, item);
  }
  const runningTool = [...tools.values()].filter((item) => item.stage === "running" &&
    item.sequence > latestModelSequence && (!snapshot || Date.parse(item.created_at) > snapshotStartedAt) &&
    (!snapshot || !item.attempt_id || item.attempt_id === snapshot.call.attempt_id))
    .sort((a, b) => b.sequence - a.sequence)[0];
  if (runningTool) {
    if (runningTool.activity_type === "search") return { mode: "searching", label: t("正在搜索", "Searching") };
    const label = runningTool.tool_name === "workspace_change" ? t("正在准备修改", "Preparing changes")
      : runningTool.tool_name === "workspace_apply" ? t("正在应用修改", "Applying changes")
        : runningTool.activity_type === "read" ? t("正在读取资料", "Reading")
          : runningTool.activity_type === "verify" ? t("正在运行检查", "Running checks") : t("正在执行工具", "Running tool");
    return { mode: "executing", label };
  }
  if (snapshot && Date.parse(snapshot.updated_at) > latestModelAt &&
      input.streamStatus === "live" && !snapshot.message_complete && snapshot.text.trim() &&
      !snapshot.items.some((item) => item.type === "message" && ["completed", "failed", "cancelled"].includes(item.status))) {
    return { mode: "composing", label: snapshot.content_kind === "tool_commentary" ? t("正在更新进展", "Updating progress") : t("正在回复", "Responding") };
  }
  return { mode: "waiting", label: t("正在工作", "Working") };
}
