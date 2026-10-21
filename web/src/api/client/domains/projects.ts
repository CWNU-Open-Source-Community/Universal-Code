import { parseProjectActionResult, parseProjectActions } from "../../project-actions";
import {
  normalizePreparationTarget,
  parseProjectPreparation,
  parseProjectPreparationOperation,
  validPreparationTarget,
  validProjectPreparationRequest,
} from "../../project-preparation";
import { normalizedTaskBudget, parseTaskConfiguration } from "../../task-configuration";
import { parseTaskSkillCandidates } from "../../task-skill-candidates";
import { APIRequestError } from "../protocol";
import { assertSkillSelectionChoice, assertSkillSelectionReceipt } from "../responses/skills";
import { boundedIdentity } from "../validation";
import type { ProjectActionSlot } from "../../project-actions";
import type {
  ProjectPreparationOperation,
  ProjectPreparationRequest,
  ProjectPreparationView,
} from "../../project-preparation";
import type { TaskSkillCandidateScope } from "../../task-skill-candidates";
import type { ClientFeatures } from "../capabilities";
import type { APITransport } from "../transport";

export class ProjectsClient {
  constructor(
    private readonly transport: APITransport,
    private readonly features: ClientFeatures,
  ) {}

  async listProjectActions(runID: string, target: string | AbortSignal = ".", signal?: AbortSignal): Promise<import("../../types").ProjectActionsView> {
    const directory = typeof target === "string" ? target || "." : ".";
    const requestSignal = typeof target === "string" ? signal : target;
    if (!boundedIdentity(runID) || !validPreparationTarget(directory)) throw new Error("An exact Run and relative project directory are required");
    const normalizedTarget = normalizePreparationTarget(directory);
    return parseProjectActions(await this.transport.get<unknown>(
      "/runs/" + encodeURIComponent(runID) + "/project-actions", normalizedTarget === "." ? {} : { target: normalizedTarget }, requestSignal), runID, normalizedTarget);
  }

  async getProjectPreparation(runID: string, target = ".", signal?: AbortSignal): Promise<ProjectPreparationView> {
    if (!boundedIdentity(runID) || !validPreparationTarget(target)) throw new Error("An exact Run and relative project directory are required");
    const normalizedTarget = normalizePreparationTarget(target);
    return parseProjectPreparation(await this.transport.get<unknown>(
      "/runs/" + encodeURIComponent(runID) + "/project-preparation", { target: normalizedTarget }, signal), runID, normalizedTarget);
  }

  async prepareProject(runID: string, body: ProjectPreparationRequest, operationKey: string): Promise<ProjectPreparationView> {
    if (!this.features.hasControl || !this.features.hasRunExecution) throw new Error("Project preparation requires execution control");
    if (!boundedIdentity(runID) || !boundedIdentity(operationKey) || !validProjectPreparationRequest(body))
      throw new Error("An exact reviewed project preparation request is required");
    const result = parseProjectPreparation(await this.transport.sendControlRequest<unknown>(
      "/runs/" + encodeURIComponent(runID) + "/project-preparation", body, undefined, operationKey), runID, body.target);
    if (!result.latest) throw new APIRequestError("项目准备响应没有执行记录。", "INVALID_RESPONSE", 502);
    return result;
  }

  async getProjectPreparationOperation(runID: string, operationID: string, signal?: AbortSignal): Promise<ProjectPreparationOperation> {
    if (!boundedIdentity(runID) || !boundedIdentity(operationID)) throw new Error("An exact Run and preparation operation are required");
    return parseProjectPreparationOperation(await this.transport.get<unknown>(
      "/runs/" + encodeURIComponent(runID) + "/project-preparation/operations/" + encodeURIComponent(operationID), {}, signal), runID, operationID);
  }

  async cancelProjectPreparation(runID: string, operationID: string, operationKey: string): Promise<ProjectPreparationOperation> {
    if (!this.features.hasControl || !this.features.hasRunExecution) throw new Error("Project preparation requires execution control");
    if (!boundedIdentity(runID) || !boundedIdentity(operationID) || !boundedIdentity(operationKey))
      throw new Error("An exact Run and preparation operation are required");
    return parseProjectPreparationOperation(await this.transport.sendControlRequest<unknown>(
      "/runs/" + encodeURIComponent(runID) + "/project-preparation/operations/" + encodeURIComponent(operationID) + "/cancel",
      { version: "project-preparation.v1" }, undefined, operationKey), runID, operationID);
  }

  async runProjectAction(runID: string, slot: ProjectActionSlot, target: string, operationKey: string): Promise<import("../../types").ProjectActionResultView> {
    if (!this.features.hasControl || !this.features.hasRunExecution) throw new Error("Project actions require execution control");
    if (!boundedIdentity(runID) || !boundedIdentity(operationKey) || !["test", "format"].includes(slot) ||
      target.length > 4096 || /[\u0000-\u001f\u007f]/u.test(target)) throw new Error("Invalid project action");
    return parseProjectActionResult(await this.transport.sendControlRequest<unknown>(
      "/runs/" + encodeURIComponent(runID) + "/project-actions/" + slot,
      { version: "project-actions.v1", target, confirm_execution: true }, undefined, operationKey), runID, slot);
  }

  async getProjectActionJob(runID: string, jobID: string, signal?: AbortSignal): Promise<import("../../types").ProjectActionResultView> {
    if (!boundedIdentity(runID) || !boundedIdentity(jobID)) throw new Error("An exact Run and Job are required");
    return parseProjectActionResult(await this.transport.get<unknown>(
      "/runs/" + encodeURIComponent(runID) + "/project-actions/jobs/" + encodeURIComponent(jobID), {}, signal), runID, undefined, jobID);
  }

  async listTaskSkillCandidates(scope: TaskSkillCandidateScope,
    signal?: AbortSignal): Promise<import("../../types").TaskSkillCandidatesView> {
    if (!boundedIdentity(scope.workspace_id) || scope.workspace_id.trim() !== scope.workspace_id ||
      !["code", "cyber"].includes(scope.surface) || !["plan", "deliver"].includes(scope.phase) ||
      !["code", "learn", "review", "script"].includes(scope.profile)) {
      throw new Error("An exact workspace and task mode are required");
    }
    return parseTaskSkillCandidates(await this.transport.get<unknown>("/task-configuration/skills", scope, signal), scope);
  }

  async previewTaskConfiguration(body: import("../../types").TaskConfigurationRequest,
    signal?: AbortSignal): Promise<import("../../types").TaskConfigurationView> {
    if (!boundedIdentity(body.workspace_id) || body.workspace_id.trim() !== body.workspace_id ||
      (body.profile !== undefined && !["code", "learn", "review", "script"].includes(body.profile))) throw new Error("A normalized workspace and profile are required");
    normalizedTaskBudget(body.budget);
    return parseTaskConfiguration(await this.transport.sendReadRequest<unknown>("/task-configuration/preview", body, signal), body.workspace_id, body.profile ?? "code");
  }

  async getRunTaskConfiguration(runID: string, signal?: AbortSignal): Promise<import("../../types").TaskConfigurationView> {
    if (!boundedIdentity(runID) || runID.trim() !== runID) throw new Error("A normalized Run identity is required");
    return parseTaskConfiguration(await this.transport.get<unknown>(`/runs/${encodeURIComponent(runID)}/task-configuration`, {}, signal), undefined, undefined, "snapshot");
  }

  async selectProjectSkills(runID: string, choice: import("../../project-skills").ProjectSkillChoice, idempotencyKey: string, signal?: AbortSignal): Promise<void> {
    if (!this.features.hasThreadControl) throw new Error("Exact project Skill references and task control are required");
    assertSkillSelectionChoice(runID, choice, "project");
    const value = await this.transport.sendControl<unknown>(`/runs/${encodeURIComponent(runID)}/project-skills/select`,
      { refs: choice.refs, confirm_untrusted_context: choice.confirmUntrustedContext }, idempotencyKey, signal);
    assertSkillSelectionReceipt(value, runID, choice.refs, "Project");
  }

  async selectInstalledSkills(runID: string, choice: import("../../project-skills").ProjectSkillChoice, idempotencyKey: string, signal?: AbortSignal): Promise<void> {
    if (!this.features.hasThreadControl) throw new Error("Exact installed Skill references and task control are required");
    assertSkillSelectionChoice(runID, choice, "installed");
    const value = await this.transport.sendControl<unknown>(`/runs/${encodeURIComponent(runID)}/external-skills/select`,
      { refs: choice.refs, confirm_untrusted_context: choice.confirmUntrustedContext }, idempotencyKey, signal);
    assertSkillSelectionReceipt(value, runID, choice.refs, "Installed");
  }
}
