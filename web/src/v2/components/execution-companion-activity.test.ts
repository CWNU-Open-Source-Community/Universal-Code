import { describe, expect, it } from "vitest";
import type { AgentNodeView, PublicModelStreamSnapshot, RunView, ThreadDetailView, ThreadExecutionView,
  ThreadTranscriptItemView } from "../../api/types";
import { projectExecutionCompanions } from "./execution-companion-activity";

const time = "2026-11-26T00:00:00Z";
const run: RunView = { id: "run", mission_id: "mission", session_id: "session", status: "running",
  config: { interactive: true, model_route: "code" }, budget: { max_turns: 3 }, created_at: time, updated_at: time };
const detail = { thread: { id: "thread", status: "active", active_run_id: "run", last_run_id: "run" },
  active_run: run, last_run: run, runs: [] } as unknown as ThreadDetailView;
const execution: ThreadExecutionView = { version: "thread_execution.v1", thread_id: "thread",
  state: "running", execution_id: "execution", queued_messages: 0, capability_grant: false };
const live: PublicModelStreamSnapshot = { version: "model_public_stream.v3", revision: 1, provisional: true,
  content_kind: "reply", event_sequence: 1, items: [], updated_at: time, text: "", message_complete: false,
  call: { attempt_id: "root-attempt", session_id: "session", run_id: "run", provider: "gateway", model: "gpt-5",
    model_attempt: 1, transport_attempt: 1, max_attempts: 1, tool_round: 0, protocol_repair: 0,
    stream_chunks: 1, stream_bytes: 2, started_at: time, cancel_requested: false } };
function call(sequence: number, patch: Partial<ThreadTranscriptItemView> = {}): ThreadTranscriptItemView {
  return { id: String(sequence), canonical_id: String(sequence), sequence, version: "thread_transcript.v1",
    run_id: "run", run_ordinal: 1, source: "harness", kind: "model_call", activity_type: "checkpoint",
    stage: "started", status: "running", created_at: time, title: "call", attempt_id: "root-attempt",
    model_attempt: 1, durable: true, provisional: false, verifiable: true, instruction_authorized: false, ...patch };
}
function node(id: string, role: AgentNodeView["role"]): AgentNodeView {
  return { id, role, status: "running", active_attempt_id: id + "-attempt", active_attempt_live: role === "specialist",
    session_id: id + "-session", parent_id: role === "specialist" ? "root" : undefined, profile: "code",
    skills: [], depth: role === "specialist" ? 1 : 0, child_limit: 0, turn_limit: 2, turns_used: 1,
    token_limit: 3000, tokens_used: 10, version: 1, created_at: time, updated_at: time };
}
const root = node("root", "root");
const graph = { protocol_version: "agent_graph.v1" as const, run_id: "run", root_agent_id: "root", nodes: [root] };
const base = { threadID: "thread", detail, execution, graph, durableItems: [] };

describe("execution office participants", () => {
  it.each([
    ["anthropic/claude-sonnet-5", "claude"], ["deepseek-ai/DeepSeek-R1", "deepseek"],
    ["gemini-3.7-flash", "gemini"], ["gpt-5.4-codex", "gpt"], ["codex-mini-latest", "gpt"],
    ["cohere/command-r", "unknown"], ["deepseek/my-local-model", "unknown"],
  ])("uses the actual model %s through a gateway", (model, family) => {
    const [agent] = projectExecutionCompanions({ ...base, liveStatus: "live",
      liveSnapshot: { ...live, call: { ...live.call, provider: "official-google-gemini", model } } });
    expect(agent).toMatchObject({ model, family, role: "root", state: "working" });
  });

  it("keeps two specialists of the same family separate and follows each active attempt", () => {
    const left = node("left", "specialist"), right = node("right", "specialist");
    const records = [call(1, { model_name: "claude-haiku-4" }),
      call(2, { agent_id: "right", attempt_id: "previous", model_name: "gemini-2.5-pro" }),
      call(3, { agent_id: "left", attempt_id: "left-attempt", model_name: "deepseek-chat" }),
      call(4, { agent_id: "right", attempt_id: "right-attempt", model_name: "deepseek-reasoner" }),
      call(5, { agent_id: "right", attempt_id: "right-attempt", status: "cancelling" })];
    const team = projectExecutionCompanions({ ...base, graph: { ...graph, nodes: [root, left, right] }, durableItems: records });
    expect(team.map(({ id, family, model, state }) => [id, family, model, state])).toEqual([
      ["root", "claude", "claude-haiku-4", "working"], ["left", "deepseek", "deepseek-chat", "working"],
      ["right", "deepseek", "deepseek-reasoner", "stopping"],
    ]);
    const released = projectExecutionCompanions({ ...base, execution: { ...execution, state: "idle" },
      graph: { ...graph, nodes: [root, { ...left, active_attempt_live: undefined }, right] }, durableItems: records });
    expect(released.map(({ state }) => state)).toEqual(["idle", "unknown", "stopping"]);
    const delegated = projectExecutionCompanions({ ...base, graph: { ...graph, nodes: [
      { ...root, status: "completed", active_attempt_id: undefined }, left,
    ] }, durableItems: records.slice(0, 3) });
    expect(delegated[0]).toMatchObject({ state: "completed", family: "claude" });
    expect(delegated.find((agent) => agent.id === "left")?.state).toBe("working");
  });

  it("does not turn an old running record, peer message or a gateway name into a working model", () => {
    const latest = { ...run, config: { ...run.config, model_route: "deepseek/code-helper" } };
    const candidate = { ...base, detail: { ...detail, active_run: latest }, execution: { ...execution, state: "idle" as const },
      liveStatus: "live" as const, liveSnapshot: live, durableItems: [
        call(1, { model_name: "gemini-2.5-pro", run_id: "previous" }),
        call(2, { kind: "peer_message", source: "peer", agent_id: "other", model_name: "claude-opus" }),
        call(3, { model: "gpt", attempt_id: undefined }),
      ] };
    expect(projectExecutionCompanions(candidate)).toEqual([
      { id: "root", role: "root", family: "unknown", model: "code-helper", state: "idle" },
    ]);
    const configured = { ...run, config: { ...run.config, model_route: "gateway/claude-sonnet-5" } };
    expect(projectExecutionCompanions({ ...base, detail: { ...detail, active_run: configured } })[0])
      .toMatchObject({ family: "unknown", model: "", state: "working" });
    const current = projectExecutionCompanions({ ...base, graph: undefined, liveSnapshot: live, liveStatus: "finalizing",
      durableItems: [call(1, { model_name: "gemini-2.5-pro", task: { id: "old", input_message_id: "old-input", state: "completed" } })] });
    expect(current[0]).toMatchObject({ family: "unknown", model: "" });
  });

  it.each([["waiting_approval", "waiting"], ["paused", "paused"], ["completed", "completed"],
    ["failed", "failed"], ["cancelled", "cancelled"]] as const)("stops live work when the run is %s", (status, state) => {
    const [agent] = projectExecutionCompanions({ ...base, detail: { ...detail, active_run: { ...run, status } },
      liveStatus: "live", liveSnapshot: live });
    expect(agent.state).toBe(state);
  });

  it("honors current stop and interrupted status, and discards reads owned by another chat", () => {
    const cancelled = { ...live, call: { ...live.call, cancel_requested: true } };
    expect(projectExecutionCompanions({ ...base, liveStatus: "live", liveSnapshot: cancelled })[0].state).toBe("stopping");
    expect(projectExecutionCompanions({ ...base, execution: { ...execution, state: "idle", last_turn_interrupted: true },
      liveStatus: "live", liveSnapshot: cancelled })[0].state).toBe("cancelled");
    expect(projectExecutionCompanions({ ...base, execution: { ...execution, thread_id: "another" } })[0].state).toBe("unknown");
    expect(projectExecutionCompanions({ ...base, threadID: "another" })).toEqual([]);
  });

  it("drops the old model and its cancellation when a newer call is already recorded", () => {
    const [agent] = projectExecutionCompanions({ ...base, liveStatus: "live",
      liveSnapshot: { ...live, call: { ...live.call, cancel_requested: true } },
      durableItems: [call(5, { model_name: "gemini-2.5-pro", model_attempt: 2 })] });
    expect(agent).toMatchObject({ family: "gemini", model: "gemini-2.5-pro", state: "working" });
  });

  it("uses only the selected historical run's records and never animates an old attempt", () => {
    const older: RunView = { ...run, id: "older", status: "completed", config: { ...run.config, model_route: "gateway/gpt-5" } };
    const previous = { ...base, selectedRunID: "older", detail: { ...detail, runs: [{ run: older, ordinal: 1, created_at: time }] },
      liveStatus: "live" as const, liveSnapshot: live, graph: { ...graph, nodes: [root, node("current-child", "specialist")] } };
    expect(projectExecutionCompanions(previous)).toEqual([
      { id: "older", role: "root", model: "", family: "unknown", state: "completed" },
    ]);
    const team = projectExecutionCompanions({ ...previous, durableItems: [
      call(1, { run_id: "older", model: "gateway / gemini-2.5-flash" }),
      call(2, { run_id: "older", agent_id: "old-child", attempt_id: "old-attempt", model_name: "gpt-5" }),
    ] });
    expect(team.map(({ id, family, state }) => [id, family, state])).toEqual([
      ["older", "gemini", "completed"], ["old-child", "gpt", "unknown"],
    ]);
  });
});
