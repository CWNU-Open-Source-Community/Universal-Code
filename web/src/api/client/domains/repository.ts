import {
  isGitAdvancedExecuteResult,
  isGitAdvancedProjection,
  isGitAdvancedReviewResult,
  validGitAdvancedSpec,
} from "../../git-advanced";
import {
  parseGitHubDeviceAuthorization,
  parseGitHubDevicePoll,
  parseGitHubEvidence,
  parseGitHubFetch,
  parseGitHubProjection,
  parseGitHubQualification,
  parseGitHubReviewConfigure,
  parseGitHubReviewConnections,
  parseGitHubReviewCredential,
  parseGitHubWriteExecute,
  parseGitHubWriteReview,
} from "../../github-review";
import { APIRequestError } from "../protocol";
import {
  parseRepositoryCommitComparison,
  parseRepositoryCommitDetail,
  parseRepositoryCommitFilePreview,
  parseRepositoryDiff,
  parseRepositoryFileHistory,
  parseRepositoryHistory,
  parseRepositoryState,
} from "../responses/repository";
import {
  boundedIdentity,
  boundedText,
  isSHA256,
  safePositiveInteger,
  validWorkspaceRelativePath,
} from "../validation";
import type {
  GitAdvancedExecuteResultView,
  GitAdvancedProjectionView,
  GitAdvancedReviewResultView,
  GitAdvancedScopeView,
  GitAdvancedSpecView,
  GitHubReviewConfigureResultView,
  GitHubReviewCredentialView,
  GitHubReviewDeviceAuthorizationView,
  GitHubReviewDevicePollResultView,
  GitHubReviewEvidenceResultView,
  GitHubReviewFetchResultView,
  GitHubReviewProjectionView,
  GitHubReviewQualificationResultView,
  GitHubReviewWriteExecuteResultView,
  GitHubReviewWriteReviewResultView,
  GitHubReviewWriteSpecView,
  RepositoryCommitComparisonView,
  RepositoryCommitDetailView,
  RepositoryCommitFilePreviewView,
  RepositoryDiffView,
  RepositoryFileHistoryView,
  RepositoryHistoryView,
  RepositoryStateView,
} from "../../types";
import type { ClientFeatures } from "../capabilities";
import type { APITransport } from "../transport";

export class RepositoryClient {
  constructor(
    private readonly transport: APITransport,
    private readonly features: ClientFeatures,
  ) {}

  async gitAdvancedProjection(runID: string,
    signal?: AbortSignal): Promise<GitAdvancedProjectionView> {
    if (!boundedIdentity(runID) || runID.trim() !== runID) {
      throw new Error("A normalized Run identity is required");
    }
    const value = await this.transport.get<unknown>(
      `/runs/${encodeURIComponent(runID)}/git-advanced`, { limit: 100 }, signal,
    );
    if (!isGitAdvancedProjection(value, runID)) {
      throw new APIRequestError("Git advanced projection is invalid", "INVALID_RESPONSE", 502);
    }
    return value;
  }

  async discoverGitAdvancedHunks(runID: string, spec: GitAdvancedSpecView,
    signal?: AbortSignal): Promise<GitAdvancedReviewResultView> {
    if (!this.features.hasGitAdvancedControl || !boundedIdentity(runID) || runID.trim() !== runID ||
      !validGitAdvancedSpec(spec) || !["hunk_stage", "hunk_unstage", "hunk_revert"]
        .includes(spec.operation) || (spec.hunk_ids?.length ?? 0) !== 0) {
      throw new Error("Git advanced hunk discovery requires an enabled capability and closed hunk spec");
    }
    const value = await this.transport.sendReadRequest<unknown>(
      `/runs/${encodeURIComponent(runID)}/git-advanced/discover-hunks`, { spec }, signal,
    );
    if (!isGitAdvancedReviewResult(value, runID, true)) {
      throw new APIRequestError("Git advanced hunk discovery is invalid", "INVALID_RESPONSE", 502);
    }
    return value;
  }

  async reviewGitAdvanced(runID: string, body: {
    operation_key: string;
    scope: GitAdvancedScopeView;
    spec: GitAdvancedSpecView;
  }, signal?: AbortSignal): Promise<GitAdvancedReviewResultView> {
    if (!this.features.hasGitAdvancedControl || !boundedIdentity(runID) || runID.trim() !== runID ||
      !boundedText(body.operation_key, 256) || body.operation_key.length < 16 ||
      !isSHA256(body.scope.capability_generation) || !safePositiveInteger(body.scope.lease_generation) ||
      !validGitAdvancedSpec(body.spec)) {
      throw new Error("Git advanced review requires exact capability, lease, and operation inputs");
    }
    const value = await this.transport.sendControlRequest<unknown>(
      `/runs/${encodeURIComponent(runID)}/git-advanced/review`, body, signal,
    );
    if (!isGitAdvancedReviewResult(value, runID, false)) {
      throw new APIRequestError("Git advanced review is invalid", "INVALID_RESPONSE", 502);
    }
    return value;
  }

  async executeGitAdvanced(runID: string, body: {
    operation_id: string;
    approval_id: string;
    scope: GitAdvancedScopeView;
  }, signal?: AbortSignal): Promise<GitAdvancedExecuteResultView> {
    if (!this.features.hasGitAdvancedControl || !boundedIdentity(runID) || runID.trim() !== runID ||
      !boundedIdentity(body.operation_id) || !boundedIdentity(body.approval_id) ||
      !isSHA256(body.scope.capability_generation) || !safePositiveInteger(body.scope.lease_generation)) {
      throw new Error("Git advanced execution requires exact approved operation authority");
    }
    const value = await this.transport.sendControlRequest<unknown>(
      `/runs/${encodeURIComponent(runID)}/git-advanced/execute`, body, signal,
    );
    if (!isGitAdvancedExecuteResult(value, body.operation_id)) {
      throw new APIRequestError("Git advanced execution result is invalid", "INVALID_RESPONSE", 502);
    }
    return value;
  }

  async githubReviewConnections(enabledOnly = false,
    signal?: AbortSignal): Promise<GitHubReviewCredentialView[]> {
    return parseGitHubReviewConnections(await this.transport.get<unknown>(
      "/github-review/connections", { enabled_only: enabledOnly ? "true" : undefined }, signal,
    ));
  }

  async githubReviewCredential(connectionID: string,
    signal?: AbortSignal): Promise<GitHubReviewCredentialView> {
    if (!boundedIdentity(connectionID)) throw new Error("A normalized GitHub connection is required");
    return parseGitHubReviewCredential(await this.transport.get<unknown>(
      `/github-review/connections/${encodeURIComponent(connectionID)}`, {}, signal,
    ));
  }

  async configureGitHubReview(body: {
    connection_id?: string;
    repository: { host: "github.com"; owner: string; name: string; full_name: string; private: boolean };
    credential: { name: string; kind: "github_app_device" | "oauth_user" | "fine_grained_pat" };
    client_id?: string;
    allowed_log_hosts: string[];
    write_enabled: boolean;
    enabled: boolean;
    expected_generation: number;
  }, signal?: AbortSignal): Promise<GitHubReviewConfigureResultView> {
    if (!this.features.hasGitHubReviewControl || !boundedIdentity(body.repository.owner) ||
      !boundedIdentity(body.repository.name) || body.repository.full_name !==
      `${body.repository.owner}/${body.repository.name}` || !boundedIdentity(body.credential.name) ||
      typeof body.write_enabled !== "boolean" || !Number.isSafeInteger(body.expected_generation) ||
      body.expected_generation < 0) {
      throw new Error("A bounded GitHub repository configuration is required");
    }
    return parseGitHubReviewConfigure(await this.transport.sendControlRequest<unknown>(
      "/github-review/connections", body, signal,
    ));
  }

  async beginGitHubReviewDeviceFlow(connectionID: string,
    signal?: AbortSignal): Promise<GitHubReviewDeviceAuthorizationView> {
    if (!this.features.hasGitHubReviewControl || !boundedIdentity(connectionID)) {
      throw new Error("GitHub review control and a connection are required");
    }
    return parseGitHubDeviceAuthorization(await this.transport.sendControlRequest<unknown>(
      `/github-review/connections/${encodeURIComponent(connectionID)}/device`, {}, signal,
    ));
  }

  async pollGitHubReviewDeviceFlow(connectionID: string, sessionID: string,
    signal?: AbortSignal): Promise<GitHubReviewDevicePollResultView> {
    if (!this.features.hasGitHubReviewControl || !boundedIdentity(connectionID) ||
      !boundedIdentity(sessionID)) throw new Error("A GitHub Device Flow session is required");
    return parseGitHubDevicePoll(await this.transport.sendControlRequest<unknown>(
      `/github-review/connections/${encodeURIComponent(connectionID)}/device-poll`,
      { session_id: sessionID }, signal,
    ));
  }

  async disconnectGitHubReview(connectionID: string,
    signal?: AbortSignal): Promise<GitHubReviewCredentialView> {
    if (!this.features.hasGitHubReviewControl || !boundedIdentity(connectionID)) {
      throw new Error("GitHub review control and a connection are required");
    }
    return parseGitHubReviewCredential(await this.transport.sendControlRequest<unknown>(
      `/github-review/connections/${encodeURIComponent(connectionID)}/disconnect`, {}, signal,
    ));
  }

  async qualifyGitHubReview(connectionID: string, pullRequest: number,
    signal?: AbortSignal): Promise<GitHubReviewQualificationResultView> {
    if (!this.features.hasGitHubReviewControl || !boundedIdentity(connectionID) ||
      !Number.isSafeInteger(pullRequest) || pullRequest < 1) throw new Error("A valid PR is required");
    return parseGitHubQualification(await this.transport.sendControlRequest<unknown>(
      `/github-review/connections/${encodeURIComponent(connectionID)}/qualify`,
      { pull_request: pullRequest }, signal,
    ));
  }

  async fetchGitHubReview(connectionID: string, pullRequest: number,
    signal?: AbortSignal): Promise<GitHubReviewFetchResultView> {
    if (!this.features.hasGitHubReviewControl || !boundedIdentity(connectionID) ||
      !Number.isSafeInteger(pullRequest) || pullRequest < 1) throw new Error("A valid PR is required");
    return parseGitHubFetch(await this.transport.sendControlRequest<unknown>(
      `/github-review/connections/${encodeURIComponent(connectionID)}/fetch`,
      { pull_request: pullRequest }, signal,
    ));
  }

  async githubReviewProjection(runID: string, connectionID: string, pullRequest = 0,
    signal?: AbortSignal): Promise<GitHubReviewProjectionView> {
    if (!boundedIdentity(runID) || !boundedIdentity(connectionID) ||
      !Number.isSafeInteger(pullRequest) || pullRequest < 0) throw new Error("Invalid GitHub projection scope");
    return parseGitHubProjection(await this.transport.get<unknown>(
      `/runs/${encodeURIComponent(runID)}/github-review`, {
        connection_id: connectionID, pull_request: pullRequest || undefined, limit: 100,
      }, signal,
    ), runID);
  }

  async buildGitHubReviewEvidence(runID: string, snapshotID: string,
    signal?: AbortSignal): Promise<GitHubReviewEvidenceResultView> {
    if (!this.features.hasGitHubReviewControl || !boundedIdentity(runID) || !boundedIdentity(snapshotID)) {
      throw new Error("A Run and immutable GitHub snapshot are required");
    }
    return parseGitHubEvidence(await this.transport.sendControlRequest<unknown>(
      `/runs/${encodeURIComponent(runID)}/github-review/evidence`,
      { snapshot_id: snapshotID }, signal,
    ));
  }

  async reviewGitHubWrite(runID: string, body: { connection_id: string; snapshot_id: string;
    operation_key: string; spec: GitHubReviewWriteSpecView },
  signal?: AbortSignal): Promise<GitHubReviewWriteReviewResultView> {
    if (!this.features.hasGitHubReviewControl || !boundedIdentity(runID) ||
      !boundedIdentity(body.connection_id) || !boundedIdentity(body.snapshot_id) ||
      !boundedIdentity(body.operation_key)) throw new Error("An exact GitHub write preview is required");
    return parseGitHubWriteReview(await this.transport.sendControlRequest<unknown>(
      `/runs/${encodeURIComponent(runID)}/github-review/review`, body, signal,
    ));
  }

  async executeGitHubWrite(runID: string, operationID: string, approvalID: string,
    signal?: AbortSignal): Promise<GitHubReviewWriteExecuteResultView> {
    if (!this.features.hasGitHubReviewControl || !boundedIdentity(runID) ||
      !boundedIdentity(operationID) || !boundedIdentity(approvalID)) {
      throw new Error("An approved GitHub write operation is required");
    }
    return parseGitHubWriteExecute(await this.transport.sendControlRequest<unknown>(
      `/runs/${encodeURIComponent(runID)}/github-review/execute`,
      { operation_id: operationID, approval_id: approvalID }, signal,
    ));
  }

  async repositoryState(workspaceID: string,
    signal?: AbortSignal): Promise<RepositoryStateView> {
    if (!boundedIdentity(workspaceID) || workspaceID.trim() !== workspaceID) {
      throw new Error("A normalized Workspace identity is required");
    }
    return parseRepositoryState(await this.transport.get<unknown>(
      `/workspaces/${encodeURIComponent(workspaceID)}/repository-state`, {}, signal,
    ), workspaceID);
  }

  async repositoryDiff(workspaceID: string,
    signal?: AbortSignal): Promise<RepositoryDiffView> {
    if (!boundedIdentity(workspaceID) || workspaceID.trim() !== workspaceID) {
      throw new Error("A normalized Workspace identity is required");
    }
    return parseRepositoryDiff(await this.transport.get<unknown>(
      `/workspaces/${encodeURIComponent(workspaceID)}/repository-diff`, {}, signal,
    ), workspaceID);
  }

  async repositoryHistory(workspaceID: string,
    signal?: AbortSignal): Promise<RepositoryHistoryView> {
    if (!boundedIdentity(workspaceID) || workspaceID.trim() !== workspaceID) {
      throw new Error("A normalized Workspace identity is required");
    }
    return parseRepositoryHistory(await this.transport.get<unknown>(
      `/workspaces/${encodeURIComponent(workspaceID)}/repository-history`, {}, signal,
    ), workspaceID);
  }

  async repositoryCommit(workspaceID: string, objectID: string,
    signal?: AbortSignal): Promise<RepositoryCommitDetailView> {
    if (!boundedIdentity(workspaceID) || workspaceID.trim() !== workspaceID ||
      !/^[0-9a-f]{40}$/u.test(objectID)) {
      throw new Error("A normalized Workspace identity and exact commit object are required");
    }
    return parseRepositoryCommitDetail(await this.transport.get<unknown>(
      `/workspaces/${encodeURIComponent(workspaceID)}/repository-commits/${objectID}`, {}, signal,
    ), workspaceID, objectID);
  }

  async repositoryCommitComparison(workspaceID: string, baseObjectID: string,
    headObjectID: string, signal?: AbortSignal): Promise<RepositoryCommitComparisonView> {
    if (!boundedIdentity(workspaceID) || workspaceID.trim() !== workspaceID ||
      !/^[0-9a-f]{40}$/u.test(baseObjectID) || !/^[0-9a-f]{40}$/u.test(headObjectID)) {
      throw new Error("Normalized Workspace and exact commit identities are required");
    }
    return parseRepositoryCommitComparison(await this.transport.get<unknown>(
      `/workspaces/${encodeURIComponent(workspaceID)}/repository-commit-comparison`,
      { base_object_id: baseObjectID, head_object_id: headObjectID }, signal,
    ), workspaceID, baseObjectID, headObjectID);
  }

  async repositoryFileHistory(workspaceID: string, path: string,
    signal?: AbortSignal): Promise<RepositoryFileHistoryView> {
    if (!boundedIdentity(workspaceID) || workspaceID.trim() !== workspaceID ||
      !validWorkspaceRelativePath(path) || path === ".") {
      throw new Error("A normalized Workspace identity and canonical file path are required");
    }
    return parseRepositoryFileHistory(await this.transport.get<unknown>(
      `/workspaces/${encodeURIComponent(workspaceID)}/repository-file-history`, { path }, signal,
    ), workspaceID, path);
  }

  async repositoryCommitFilePreview(workspaceID: string, objectID: string, path: string,
    signal?: AbortSignal): Promise<RepositoryCommitFilePreviewView> {
    if (!boundedIdentity(workspaceID) || workspaceID.trim() !== workspaceID ||
      !/^[0-9a-f]{40}$/u.test(objectID) || !validWorkspaceRelativePath(path) || path === ".") {
      throw new Error("A normalized Workspace, exact commit object, and canonical file path are required");
    }
    return parseRepositoryCommitFilePreview(await this.transport.get<unknown>(
      `/workspaces/${encodeURIComponent(workspaceID)}/repository-commits/${objectID}/file-preview`,
      { path }, signal,
    ), workspaceID, objectID, path);
  }
}
