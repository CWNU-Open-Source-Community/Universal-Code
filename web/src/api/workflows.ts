import type { components } from "./schema";

export type WorkflowSpec = components["schemas"]["WorkflowSpec"];
export type WorkflowStep = components["schemas"]["WorkflowStep"];
export type WorkflowDefinition = components["schemas"]["WorkflowDefinition"];
export type WorkflowExecution = components["schemas"]["WorkflowExecution"];

interface WorkflowTransport {
  get(path: string, signal?: AbortSignal): Promise<unknown>;
  post(path: string, body: unknown, key: string, signal?: AbortSignal): Promise<unknown>;
}
const identity = (value: string): string => {
  if (!value || value.trim() !== value || value.length > 256 || /[\s/\\\u0000-\u001f]/u.test(value)) {
    throw new Error("A normalized workflow identity is required");
  }
  return encodeURIComponent(value);
};
const record = (value: unknown): value is Record<string, unknown> => !!value && typeof value === "object" && !Array.isArray(value);
function definition(value: unknown): WorkflowDefinition {
  if (!record(value) || typeof value.id !== "string" || typeof value.digest !== "string" ||
    !record(value.spec) || value.spec.version !== "workflow.v1" || !Array.isArray(value.spec.steps)) {
    throw new Error("Invalid workflow definition response");
  }
  return value as WorkflowDefinition;
}
function execution(value: unknown, runID: string, executionID?: string): WorkflowExecution {
  if (!record(value) || typeof value.id !== "string" || value.run_id !== runID ||
    (executionID !== undefined && value.id !== executionID) || typeof value.definition_id !== "string" ||
    !["running", "waiting", "waiting_approval", "paused", "completed", "failed"].includes(String(value.status)) ||
    !Number.isSafeInteger(value.revision) || Number(value.revision) < 1 || !record(value.state) ||
    !record(value.state.outputs) || !Array.isArray(value.state.stack)) {
    throw new Error("Invalid workflow execution response");
  }
  return value as WorkflowExecution;
}
function items(value: unknown): unknown[] {
  if (!record(value) || !Array.isArray(value.items) || value.items.length > 128) {
    throw new Error("Invalid workflow collection response");
  }
  return value.items;
}

/** Saved programs execute only through the server's bounded worker runner. */
export class WorkflowClient {
  constructor(private readonly transport: WorkflowTransport) {}
  async definitions(signal?: AbortSignal): Promise<WorkflowDefinition[]> {
    return items(await this.transport.get("/workflows", signal)).map(definition);
  }
  async definition(id: string, signal?: AbortSignal): Promise<WorkflowDefinition> {
    return definition(await this.transport.get(`/workflows/${identity(id)}`, signal));
  }
  async save(spec: WorkflowSpec, key: string, signal?: AbortSignal): Promise<WorkflowDefinition> {
    if (spec.version !== "workflow.v1") throw new Error("Unsupported workflow version");
    return definition(await this.transport.post("/workflows", spec, key, signal));
  }
  async list(runID: string, signal?: AbortSignal): Promise<WorkflowExecution[]> {
    return items(await this.transport.get(`/runs/${identity(runID)}/workflows`, signal)).map((item) => execution(item, runID));
  }
  async get(runID: string, id: string, signal?: AbortSignal): Promise<WorkflowExecution> {
    return execution(await this.transport.get(this.path(runID, id), signal), runID, id);
  }
  async start(runID: string, definitionID: string, inputs: unknown, key: string, signal?: AbortSignal): Promise<WorkflowExecution> {
    identity(definitionID);
    return execution(await this.transport.post(`/runs/${identity(runID)}/workflows`, {
      version: "workflow_start.v1", definition_id: definitionID, inputs, confirm_start: true,
    }, key, signal), runID);
  }
  async advance(runID: string, id: string, revision: number, key: string, signal?: AbortSignal): Promise<WorkflowExecution> {
    return this.control("advance", runID, id, revision, key, signal);
  }
  async pause(runID: string, id: string, revision: number, key: string, signal?: AbortSignal): Promise<WorkflowExecution> {
    return this.control("pause", runID, id, revision, key, signal);
  }
  async resume(runID: string, id: string, revision: number, key: string, signal?: AbortSignal): Promise<WorkflowExecution> {
    return this.control("resume", runID, id, revision, key, signal);
  }
  async rerun(runID: string, id: string, key: string, signal?: AbortSignal): Promise<WorkflowExecution> {
    return execution(await this.transport.post(`${this.path(runID, id)}/rerun`, {
      version: "workflow_control.v1", revision: 0, confirm_start: true,
    }, key, signal), runID);
  }
  private path(runID: string, id: string): string { return `/runs/${identity(runID)}/workflows/${identity(id)}`; }
  private async control(action: string, runID: string, id: string, revision: number, key: string, signal?: AbortSignal): Promise<WorkflowExecution> {
    if (!Number.isSafeInteger(revision) || revision < 1) throw new Error("The observed workflow revision is required");
    return execution(await this.transport.post(`${this.path(runID, id)}/${action}`, {
      version: "workflow_control.v1", revision,
    }, key, signal), runID, id);
  }
}
