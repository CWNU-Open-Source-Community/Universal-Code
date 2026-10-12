import type { APIClient } from "./client";
import type { components } from "./schema";

export type SpecialistExecutionView = components["schemas"]["SpecialistExecutionView"];
export type SpecialistContinuationView = components["schemas"]["SpecialistContinuationView"];
export type SpecialistToolReceiptsView = components["schemas"]["SpecialistToolReceiptsView"];

export async function executeSpecialist(client: APIClient, runID: string, agentID: string,
  operationKey: string): Promise<SpecialistExecutionView> {
  if (!client.hasModelControl) throw new Error("Model control is required to run a child agent");
  const result = await client.postControl<SpecialistExecutionView>(
    `/runs/${encodeURIComponent(runID)}/agents/${encodeURIComponent(agentID)}/execute`,
    { version: "specialist_tools.v1", operation_key: operationKey, workspace_tools: true, confirm_execute: true },
    operationKey,
  );
  if (result.version !== "specialist_tools.v1" || result.run_id !== runID || result.agent_id !== agentID || !result.attempt_id) {
    throw new Error("Child execution receipt does not match the requested agent");
  }
  return result;
}

export async function continueSpecialist(client: APIClient, runID: string, agentID: string,
  operationKey: string): Promise<SpecialistContinuationView> {
  if (!client.hasModelControl) throw new Error("Model control is required to run a child agent");
  const result = await client.postControl<SpecialistContinuationView>(
    `/runs/${encodeURIComponent(runID)}/agents/${encodeURIComponent(agentID)}/continue`,
    { version: "specialist_continuation.v1", operation_key: operationKey, confirm_execute: true }, operationKey,
  );
  if (result.version !== "specialist_continuation.v1" || result.run_id !== runID || result.agent_id !== agentID ||
    !result.schedule_id || !result.thread_id || !Array.isArray(result.attempt_ids)) {
    throw new Error("Child continuation receipt does not match the requested agent");
  }
  return result;
}

export function getSpecialistToolReceipts(client: APIClient, runID: string, agentID: string,
  attemptID: string, signal?: AbortSignal): Promise<SpecialistToolReceiptsView> {
  return client.get(`/runs/${encodeURIComponent(runID)}/agents/${encodeURIComponent(agentID)}/attempts/${encodeURIComponent(attemptID)}/tool-receipts`, {}, signal);
}
