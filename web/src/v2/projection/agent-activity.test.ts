import { describe, expect, it } from "vitest";
import type { PublicModelStreamSnapshot, ThreadExecutionView, ThreadTranscriptItemView } from "../../api/types";
import { projectAgentActivity } from "./agent-activity";

const execution: ThreadExecutionView = { version: "thread_execution.v1", thread_id: "thread-a",
  state: "running", execution_id: "execution-a", queued_messages: 0, capability_grant: false };
const snapshot: PublicModelStreamSnapshot = { version: "public_model_stream.v1", revision: 2,
  call: { run_id: "run-a", attempt_id: "attempt-a", started_at: "2026-09-30T01:00:00Z" },
  updated_at: "2026-09-30T01:00:00.500Z",
  content_kind: "reply", message_complete: false, text: "开始处理", items: [], provisional: true,
} as unknown as PublicModelStreamSnapshot;
const base = { threadID: "thread-a", runID: "run-a", runStatus: "running", archived: false,
  execution, executionReadable: true, executionError: false, submitting: false, reconciling: false,
  snapshot: null, streamStatus: "waiting" as const, progressError: false, transcript: [] };
function tool(patch: Partial<ThreadTranscriptItemView> = {}): ThreadTranscriptItemView {
  return { version: "thread_transcript.v1", id: "tool-1", canonical_id: "item-1", durable_call_id: "call-1",
    run_id: "run-a", attempt_id: "attempt-a", run_ordinal: 1, sequence: 10, kind: "tool_call", stage: "running",
    source: "harness", durable: true, provisional: false, activity_type: "search", tool_name: "web_search",
    status: "pending", title: "搜索", created_at: "2026-09-30T01:00:01Z", instruction_authorized: false,
    verifiable: true, ...patch };
}

describe("Agent activity reflects execution evidence", () => {
  it("distinguishes an unsupported connection, a pending read and a failed read", () => {
    expect(projectAgentActivity({ ...base, execution: undefined, executionReadable: false })?.label)
      .toBe("当前连接不支持读取 Agent 状态");
    expect(projectAgentActivity({ ...base, execution: undefined })?.label).toBe("正在同步状态");
    expect(projectAgentActivity({ ...base, execution: undefined, executionError: true }))
      .toEqual({ label: "状态读取失败", attention: true });
  });
  it("does not treat an open run or cached text as a running worker", () => {
    expect(projectAgentActivity({ ...base, execution: { ...execution, state: "idle" }, snapshot,
      streamStatus: "live" })).toBeNull();
    expect(projectAgentActivity({ ...base, execution: undefined })).toEqual({ label: "正在同步状态" });
  });
  it.each(["waiting_approval", "paused", "completed", "failed", "cancelled"])(
    "%s overrides stale streaming and tool-start data", (runStatus) => {
      const status = projectAgentActivity({ ...base, runStatus, snapshot, streamStatus: "live", transcript: [tool()] });
      expect(status?.mode).toBeUndefined();
    });
  it.each(["stopping", "stop_failed"] as const)("%s stays static", (state) => {
    expect(projectAgentActivity({ ...base, execution: { ...execution, state }, snapshot,
      streamStatus: "live", transcript: [tool()] })?.mode).toBeUndefined();
  });
  it("does not animate after a failed status/progress read or a mismatched thread", () => {
    for (const patch of [{ executionError: true }, { progressError: true },
      { execution: { ...execution, thread_id: "other" } }]) {
      expect(projectAgentActivity({ ...base, snapshot, streamStatus: "live", transcript: [tool()], ...patch })?.mode)
        .toBeUndefined();
    }
  });
  it("requires durable execution-start rather than model-generated arguments", () => {
    const parameters = { ...snapshot, text: "", items: [{ type: "tool_call", status: "in_progress", tool_name: "web_search" }] } as PublicModelStreamSnapshot;
    for (const candidate of [tool({ stage: "arguments_ready" }), tool({ durable: false, provisional: true }),
      tool({ stage: "started" })]) {
      expect(projectAgentActivity({ ...base, snapshot: parameters, streamStatus: "live", transcript: [candidate] })?.mode)
        .toBe("waiting");
    }
    expect(projectAgentActivity({ ...base, snapshot: parameters, streamStatus: "live", transcript: [tool()] }))
      .toEqual({ mode: "searching", label: "正在搜索" });
  });
  it("stops showing a tool after its later result, including superseded/not-dispatched", () => {
    for (const stage of ["result", "blocked"] as const) {
      expect(projectAgentActivity({ ...base, transcript: [tool(), tool({ sequence: 11, stage })] })?.mode).toBe("waiting");
    }
  });
  it("does not reuse another run/attempt or an abandoned tool before a new model request", () => {
    expect(projectAgentActivity({ ...base, transcript: [tool({ run_id: "old-run" })] })?.mode).toBe("waiting");
    expect(projectAgentActivity({ ...base, snapshot: { ...snapshot, text: "" }, streamStatus: "live",
      transcript: [tool({ attempt_id: "old-attempt" })] })?.mode).toBe("waiting");
    expect(projectAgentActivity({ ...base, transcript: [tool(), tool({ id: "model-2", kind: "model_call",
      sequence: 12, created_at: "2026-09-30T01:00:02Z" })] })?.mode).toBe("waiting");
  });
  it("does not guess legacy pairing from event IDs or lose durable ordering below a millisecond", () => {
    const legacy = tool({ durable_call_id: undefined, stream_call_id: undefined, attempt_id: undefined });
    expect(projectAgentActivity({ ...base, transcript: [legacy,
      { ...legacy, canonical_id: "different-result-event", id: "done", sequence: 11, stage: "result" }] })?.mode).toBe("waiting");
    expect(projectAgentActivity({ ...base, transcript: [tool({ attempt_id: undefined, created_at: "2026-09-30T01:00:00.000100Z" }),
      tool({ kind: "model_call", sequence: 11, created_at: "2026-09-30T01:00:00.000900Z" })] })?.mode).toBe("waiting");
  });
  it("composes only current unfinished public text, not completed, failed or foreign snapshots", () => {
    expect(projectAgentActivity({ ...base, snapshot, streamStatus: "live" })?.mode).toBe("composing");
    for (const candidate of [{ ...snapshot, message_complete: true }, { ...snapshot, text: "" },
      { ...snapshot, items: [{ type: "message", status: "failed" }] } as PublicModelStreamSnapshot,
      { ...snapshot, call: { ...snapshot.call, run_id: "old-run" } }]) {
      expect(projectAgentActivity({ ...base, snapshot: candidate, streamStatus: "live" })?.mode).toBe("waiting");
    }
    expect(projectAgentActivity({ ...base, snapshot, streamStatus: "finalizing" })?.mode).toBe("waiting");
    expect(projectAgentActivity({ ...base, snapshot, streamStatus: "live", transcript: [tool({ kind: "model_call",
      sequence: 20, created_at: "2026-09-30T01:00:05Z" })] })?.mode).toBe("waiting");
    expect(projectAgentActivity({ ...base, snapshot: { ...snapshot, call: { ...snapshot.call, cancel_requested: true } },
      streamStatus: "live" })?.mode).toBeUndefined();
  });
  it("describes proposals separately from applying modifications", () => {
    expect(projectAgentActivity({ ...base, transcript: [tool({ activity_type: "edit", tool_name: "workspace_change" })] }))
      .toEqual({ mode: "executing", label: "正在准备修改" });
    expect(projectAgentActivity({ ...base, transcript: [tool({ activity_type: "edit", tool_name: "workspace_apply" })] }))
      .toEqual({ mode: "executing", label: "正在应用修改" });
  });
  it("keeps sending and reconciliation distinct from agent activity", () => {
    const idle = { ...base, execution: { ...execution, state: "idle" as const } };
    expect(projectAgentActivity({ ...idle, submitting: true })).toEqual({ label: "正在发送消息" });
    expect(projectAgentActivity({ ...idle, reconciling: true })).toEqual({ label: "正在核对提交" });
    expect(projectAgentActivity({ ...base, submitting: true })?.mode).toBe("waiting");
    expect(projectAgentActivity({ ...base, archived: true })).toBeNull();
  });
});
