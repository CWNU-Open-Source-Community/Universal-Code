import { APIRequestError } from "../protocol";
import {
  parseApprovalDecision,
  parseApprovalQueue,
  parseControlledCommandProposal,
  parseHostCommandProposal,
} from "../responses/approvals";
import { boundedIdentity, boundedText, hasExactKeys, safePositiveInteger } from "../validation";
import type {
  ApprovalDecisionControlRequestView,
  ApprovalDecisionControlView,
  ApprovalPreviewView,
  ApprovalQueueView,
  ControlledCommandProposalView,
  HostCommandProposalView,
  PageResult,
} from "../../types";
import type { ClientFeatures } from "../capabilities";
import type { APITransport } from "../transport";

export class ApprovalsClient {
  constructor(
    private readonly transport: APITransport,
    private readonly features: ClientFeatures,
  ) {}

  async approvalPreview(runID: string, approvalID: string,
    signal?: AbortSignal): Promise<ApprovalPreviewView> {
    if (!boundedIdentity(runID) || !boundedIdentity(approvalID)) {
      throw new Error("A Run and exact approval identity are required");
    }
    const value = await this.transport.get<unknown>(
      `/runs/${encodeURIComponent(runID)}/approvals/${encodeURIComponent(approvalID)}/preview`, {}, signal);
    if (!hasExactKeys(value, ["protocol_version", "run_id", "approval_id", "proposal_id",
      "tool_name", "workspace_id", "effect", "working_directory", "fields", "source_current",
      "redacted", "truncated"]) || value.protocol_version !== "approval_queue.v1" ||
      value.run_id !== runID || value.approval_id !== approvalID ||
      !boundedIdentity(value.proposal_id) || !boundedText(value.tool_name, 128) ||
      (value.workspace_id !== "" && !boundedIdentity(value.workspace_id)) ||
      !["dry_run", "record_git_approval", "file_review_required", "fetch_public_https", "browser_sensitive_action", "mcp_server_and_tool", "computer_use_app", "command_process", "unavailable"]
        .includes(String(value.effect)) || typeof value.working_directory !== "string" ||
      !["", "."].includes(value.working_directory) ||
      typeof value.source_current !== "boolean" || typeof value.redacted !== "boolean" ||
      typeof value.truncated !== "boolean" || (value.truncated && value.source_current) ||
      (value.effect === "unavailable" && value.source_current) ||
      !Array.isArray(value.fields) || value.fields.length > 8 ||
      !value.fields.every((field) => hasExactKeys(field, ["name", "value"]) &&
        boundedText(field.name, 64) && typeof field.value === "string" &&
        [...field.value].length <= 8192 && !field.value.includes("\u0000"))) {
      throw new APIRequestError("Approval preview violated its exact binding", "INVALID_RESPONSE", 502);
    }
    const effects: Record<string, string> = { shell: "dry_run", script_process: "dry_run",
      "git.advanced": "record_git_approval", replace_file: "file_review_required",
      create_file: "file_review_required", move_file: "file_review_required", delete_file: "file_review_required",
      web_fetch: "fetch_public_https", agent_browser_sensitive: "browser_sensitive_action", mcp_tool_call: "mcp_server_and_tool", command_runtime: "command_process" };
    if (value.effect !== (effects[value.tool_name] ?? "unavailable") &&
      !(value.tool_name === "mcp_tool_call" && value.effect === "computer_use_app")) {
      throw new APIRequestError("Approval preview returned a different effect", "INVALID_RESPONSE", 502);
    }
    return value as unknown as ApprovalPreviewView;
  }

  async approvalQueue(runID: string, signal?: AbortSignal): Promise<ApprovalQueueView> {
    if (!boundedIdentity(runID) || runID.trim() !== runID) {
      throw new Error("A normalized Run identity is required");
    }
    const value = await this.transport.get<unknown>(
      `/runs/${encodeURIComponent(runID)}/approvals`, {}, signal,
    );
    return parseApprovalQueue(value, runID);
  }

  async controlledCommandProposals(runID: string,
    signal?: AbortSignal): Promise<PageResult<ControlledCommandProposalView>> {
    if (!boundedIdentity(runID) || runID.trim() !== runID) {
      throw new Error("Controlled command proposal capability and normalized Run are required");
    }
    const page = await this.transport.getPage<unknown>(
      `/runs/${encodeURIComponent(runID)}/command-proposals`, { limit: 100 }, "", signal,
    );
    return {
      ...page,
      items: page.items.map((item) => parseControlledCommandProposal(item, runID)),
    };
  }

  async controlledCommandProposal(runID: string, proposalID: string,
    signal?: AbortSignal): Promise<ControlledCommandProposalView> {
    if (!boundedIdentity(runID) || runID.trim() !== runID ||
      !boundedIdentity(proposalID) || proposalID.trim() !== proposalID) {
      throw new Error("Controlled command proposal capability and normalized identities are required");
    }
    return parseControlledCommandProposal(await this.transport.get<unknown>(
      `/runs/${encodeURIComponent(runID)}/command-proposals/${encodeURIComponent(proposalID)}`,
      {}, signal,
    ), runID, proposalID);
  }

  async hostCommandProposals(runID: string,
    signal?: AbortSignal): Promise<PageResult<HostCommandProposalView>> {
    if (!boundedIdentity(runID) || runID.trim() !== runID) {
      throw new Error("Host command proposal capability and normalized Run are required");
    }
    const page = await this.transport.getPage<unknown>(
      `/runs/${encodeURIComponent(runID)}/host-command-proposals`, { limit: 100 }, "", signal,
    );
    return {
      ...page,
      items: page.items.map((item) => parseHostCommandProposal(item, runID)),
    };
  }

  async hostCommandProposal(runID: string, proposalID: string,
    signal?: AbortSignal): Promise<HostCommandProposalView> {
    if (!boundedIdentity(runID) || runID.trim() !== runID ||
      !boundedIdentity(proposalID) || proposalID.trim() !== proposalID) {
      throw new Error("Host command proposal capability and normalized identities are required");
    }
    return parseHostCommandProposal(await this.transport.get<unknown>(
      `/runs/${encodeURIComponent(runID)}/host-command-proposals/${encodeURIComponent(proposalID)}`,
      {}, signal,
    ), runID, proposalID);
  }

  async resumeHostCommandProposal(runID: string, proposalID: string,
    signal?: AbortSignal): Promise<HostCommandProposalView> {
    if (!this.features.hasRunExecution ||
      !boundedIdentity(runID) || runID.trim() !== runID ||
      !boundedIdentity(proposalID) || proposalID.trim() !== proposalID) {
      throw new Error("Historical command continuation requires current control and exact identities");
    }
    const result = await this.transport.sendControlRequest<unknown>(
      `/runs/${encodeURIComponent(runID)}/host-command-proposals/${encodeURIComponent(proposalID)}/resume`,
      undefined, signal,
    );
    return parseHostCommandProposal(result, runID, proposalID);
  }

  async decideApproval(runID: string, approvalID: string,
    body: ApprovalDecisionControlRequestView, idempotencyKey: string,
    signal?: AbortSignal): Promise<ApprovalDecisionControlView> {
    if (!this.features.hasApprovalControl) {
      throw new Error("Approval control capability is required for this operation");
    }
    if (!boundedIdentity(runID) || runID.trim() !== runID ||
      !boundedIdentity(approvalID) || approvalID.trim() !== approvalID ||
      body.version !== "approval_control.v1" ||
      !["approve_once", "approve_for_thread", "approve_for_run", "approve_for_run_app", "deny"].includes(body.action) ||
      (body.action !== "deny" && body.action !== "approve_for_run" && body.reason !== undefined) ||
      (body.reason !== undefined && (!boundedText(body.reason, 2_048) ||
        body.reason.trim() !== body.reason || /[\u0000-\u001f\u007f]/u.test(body.reason)))) {
      throw new Error("Normalized Run and approval identities are required");
    }
    if (body.action === "approve_for_run" ?
      !safePositiveInteger(body.grant_ttl_seconds) || body.grant_ttl_seconds > 900 || !safePositiveInteger(body.grant_max_uses) || body.grant_max_uses > 8
      : body.grant_ttl_seconds !== undefined || body.grant_max_uses !== undefined) {
      throw new Error("Bounded Run approval requires explicit limits: 1..900 seconds and 1..8 uses");
    }
    const result = await this.transport.sendControl<unknown>(
      `/runs/${encodeURIComponent(runID)}/approvals/${encodeURIComponent(approvalID)}/decision`,
      body, idempotencyKey, signal,
    );
    return parseApprovalDecision(result, runID, approvalID, body);
  }
}
