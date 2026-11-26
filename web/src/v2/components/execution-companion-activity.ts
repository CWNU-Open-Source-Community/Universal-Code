import type { AgentGraphView, AgentNodeView, PublicModelStreamSnapshot, RunView, ThreadDetailView,
  ThreadExecutionView, ThreadTranscriptItemView } from "../../api/types";
import type { PublicModelStreamStatus } from "../../hooks/use-public-model-stream";

export interface ExecutionCompanion {
  id: string;
  role: AgentNodeView["role"];
  family: "gpt" | "claude" | "deepseek" | "gemini" | "unknown";
  model: string;
  state: "idle" | "working" | "waiting" | "stopping" | "completed" | "failed" | "cancelled" | "paused" | "unknown";
}

interface CompanionInput {
  threadID: string;
  detail: ThreadDetailView;
  selectedRunID?: string;
  execution?: ThreadExecutionView;
  liveSnapshot?: PublicModelStreamSnapshot | null;
  liveStatus?: PublicModelStreamStatus;
  durableItems: readonly ThreadTranscriptItemView[];
  graph?: AgentGraphView;
}

function familyOf(model: string): ExecutionCompanion["family"] {
  const name = model.split("/").at(-1) ?? "";
  const family = /^(claude|deepseek|gemini)(?:[-.:_]|$)/i.exec(name)?.[1].toLowerCase();
  if (family === "claude" || family === "deepseek" || family === "gemini") return family;
  if (/^(?:gpt|chatgpt|codex|o[134])(?:[-.:_]|$)/i.test(name)) return "gpt";
  return "unknown";
}

function recordedModel(item: ThreadTranscriptItemView): string {
  if (item.model_name) return item.model_name;
  // Older servers sent "provider / model". A bare legacy label can be just the provider.
  const label = item.model ?? "";
  const separator = label.indexOf(" / ");
  return separator < 0 ? "" : label.slice(separator + 3);
}

function lastCall(records: ThreadTranscriptItemView[] = [], attemptID?: string, runningTaskOnly = false) {
  let latest: ThreadTranscriptItemView | undefined;
  let model = "";
  for (const item of records) {
    if ((attemptID && item.attempt_id !== attemptID) || (runningTaskOnly && item.task?.state !== "running")) continue;
    const name = recordedModel(item);
    // A cancellation can omit the name. Only its own model call may supply it.
    if (name || latest?.attempt_id !== item.attempt_id || latest?.model_attempt !== item.model_attempt) model = name;
    latest = item;
  }
  return { latest, model };
}

type CompanionState = ExecutionCompanion["state"];
function runState(run: RunView): CompanionState | undefined {
  switch (run.status) {
    case "waiting_approval": return "waiting";
    case "paused": case "completed": case "failed": case "cancelled": return run.status;
  }
}

function rootState(execution?: ThreadExecutionView, cancelRequested = false): CompanionState {
  switch (execution?.state) {
    case "running": return cancelRequested ? "stopping" : "working";
    case "stopping": return "stopping";
    case "idle": return execution.last_turn_interrupted ? "cancelled" : "idle";
    default: return "unknown";
  }
}

function compareTimestamps(left: string, right: string) {
  const millis = Date.parse(left) - Date.parse(right);
  if (millis !== 0) return millis;
  // Go can emit multiple events within a millisecond. Date.parse discards those digits.
  const fraction = (value: string) => Number((/\.(\d+)/.exec(value)?.[1] ?? "").padEnd(9, "0"));
  return fraction(left) - fraction(right);
}

function currentAttempt(node?: AgentNodeView, latest?: ThreadTranscriptItemView) {
  if (!node?.active_attempt_id) return undefined;
  if (latest?.attempt_id && latest.attempt_id !== node.active_attempt_id &&
    compareTimestamps(latest.created_at, node.updated_at) > 0) return latest.attempt_id;
  return node.active_attempt_id;
}

function superseded(call: PublicModelStreamSnapshot["call"], node?: AgentNodeView, latest?: ThreadTranscriptItemView) {
  if (node?.active_attempt_id && node.active_attempt_id !== call.attempt_id && compareTimestamps(node.updated_at, call.started_at) >= 0) return true;
  if (!latest) return false;
  if (latest.attempt_id !== call.attempt_id) return compareTimestamps(latest.created_at, call.started_at) >= 0;
  return (latest.model_attempt ?? 0) > call.model_attempt || latest.model_attempt === call.model_attempt &&
    ["completed", "failed", "cancelled"].includes(latest.status ?? "");
}

function specialistState(node: AgentNodeView, current: boolean, status: CompanionState | undefined,
  receipt?: ThreadTranscriptItemView): CompanionState {
  switch (node.status) {
    case "completed": case "failed": case "cancelled": return node.status;
  }
  if (!current) return "idle";
  if (status === "waiting" || status === "paused") return status;
  if (status || node.status === "ready") return "idle";
  if (node.status === "waiting") return "waiting";
  if (!node.active_attempt_live || !node.active_attempt_id || (receipt && receipt.attempt_id !== node.active_attempt_id)) return "unknown";
  return receipt?.status === "cancelling" || receipt?.status === "cancelled" ? "stopping" : "working";
}

export function projectExecutionCompanions({ threadID, detail, selectedRunID, execution, liveSnapshot,
  liveStatus, durableItems, graph }: CompanionInput): ExecutionCompanion[] {
  if (detail.thread.id !== threadID) return [];
  const activeID = detail.thread.active_run_id || detail.active_run?.id;
  const runID = selectedRunID || activeID || detail.thread.last_run_id || detail.last_run?.id;
  const run = [detail.active_run, detail.last_run].find((item) => item?.id === runID) ??
    detail.runs?.find((binding) => binding.run.id === runID)?.run;
  if (!run) return [];
  const current = detail.thread.status === "active" && run.id === activeID;
  const status = runState(run);
  const nodes = graph?.run_id === run.id ? graph.nodes : [];
  const root = nodes.find((node) => node.role === "root");
  const children = new Map(nodes.filter((node) => node.role === "specialist").map((node) => [node.id, node]));
  const observed = current && execution?.thread_id === threadID ? execution : undefined;
  const records = new Map<string, ThreadTranscriptItemView[]>();
  // The conversation supplies the durable transcript in run/sequence order, including older pages.
  for (const item of durableItems) {
    if (item.run_id !== run.id || item.kind !== "model_call" || item.source !== "harness" ||
      !item.durable || item.provisional || !item.attempt_id) continue;
    const id = item.agent_id ?? "";
    const owned = records.get(id);
    if (owned) owned.push(item);
    else records.set(id, [item]);
  }

  const rootRecords = records.get("");
  const latestRoot = rootRecords?.at(-1);
  const live = current && !status && liveStatus === "live" && liveSnapshot?.call.run_id === run.id &&
    (!run.session_id || liveSnapshot.call.session_id === run.session_id) && !superseded(liveSnapshot.call, root, latestRoot)
    ? liveSnapshot.call : undefined;
  let primaryState = status ?? (current ? rootState(observed, live?.cancel_requested) : "idle");
  if (current && (primaryState === "working" || primaryState === "idle") && !live && root && root.status !== "running" &&
    (!latestRoot || compareTimestamps(root.updated_at, latestRoot.created_at) >= 0)) {
    primaryState = root.status === "ready" ? "idle" : root.status;
  }
  const executing = primaryState === "working" || primaryState === "stopping";
  const activeAttempt = executing ? currentAttempt(root, latestRoot) : undefined;
  const rootCall = lastCall(rootRecords, activeAttempt, executing && !activeAttempt);
  // A qualified route can identify the idle selection. A running model needs a call of its own.
  const route = current && !status && !executing ? run.config?.model_route ?? "" : "";
  const slash = route.indexOf("/");
  const primaryModel = (observed?.state !== "idle" && live?.model) || rootCall.model || (slash > 0 ? route.slice(slash + 1) : "");
  const result: ExecutionCompanion[] = [{ id: root?.id ?? run.id, role: "root", model: primaryModel,
    family: familyOf(primaryModel), state: primaryState }];
  for (const id of new Set([...children.keys(), ...records.keys()])) {
    if (!id) continue;
    const node = children.get(id);
    const agentRecords = records.get(id);
    const call = lastCall(agentRecords, current && !status ? currentAttempt(node, agentRecords?.at(-1)) : undefined);
    result.push({ id, role: "specialist", model: call.model, family: familyOf(call.model),
      state: node ? specialistState(node, current, status, call.latest) : "unknown" });
  }
  return result;
}
