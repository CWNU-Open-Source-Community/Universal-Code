import { createRef } from "react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import type { APIClient } from "../../api/client";
import type { GitHubReviewConnectionView, GitHubReviewCredentialView, GitHubReviewProjectionView,
  GitHubReviewWriteReviewResultView, ThreadDetailView } from "../../api/types";
import { standardCodeDeliveryFixture } from "../../test/standard-code-delivery";
import { V2TaskReview } from "./task-review";

vi.mock("../../lib/locale", () => ({ useLocale: () => ({ t: (chinese: string) => chinese }) }));

const sha = "a".repeat(64);
const oid = "b".repeat(40);
const timestamp = "2026-10-07T00:00:00Z";
function detail(): ThreadDetailView {
  const current = { id: "run-current", status: "running" };
  return { thread: { id: "thread-1", title: "审阅工具", workspace_id: "source-current" },
    active_run: current, last_run: current,
    runs: [{ ordinal: 1, run: { id: "run-history", status: "completed" } },
      { ordinal: 2, run: current }] } as ThreadDetailView;
}
function gitProjection(runID: string) {
  return { run_id: runID, workspace_id: `${runID}-workspace`,
    authority: { executable: true, permission_revision: 2,
      scope: { capability_generation: sha, lease_generation: 3 } },
    binding: { head: oid, branch: `${runID}-branch`, detached: false,
      repository_sha256: sha, index_sha256: sha, worktree_sha256: sha,
      status_sha256: sha, stash_sha256: sha, sequence_sha256: sha },
    conflict: { active: false, files: [] }, stashes: [], worktrees: [], operations: [] };
}

function githubConnection(): GitHubReviewConnectionView {
  return { protocol_version: "github-review-connection.v1", id: "connection-1",
    repository: { host: "github.com", owner: "fixture", name: "project",
      full_name: "fixture/project", private: false },
    credential: { name: "fixture-reference", kind: "github_app_device" },
    client_id: "Iv1.fixture", network: { host: "github.com", api_host: "api.github.com",
      oauth_host: "github.com", allowed_log_hosts: [], read_enabled: true, write_enabled: true },
    enabled: true, generation: 1, created_at: timestamp, updated_at: timestamp };
}

function githubCredential(): GitHubReviewCredentialView {
  const connection = githubConnection();
  return { protocol_version: "github-review-api.v1", connection,
    credential: { protocol_version: "github-review-provider.v1", credential: connection.credential,
      configured: true, refreshable: true, store_available: true, store_kind: "test" } };
}

function githubSnapshot(): GitHubReviewProjectionView["snapshots"][number] {
  const connection = githubConnection();
  const text = (value: string) => ({ text: value, original_bytes: value.length, stored_bytes: value.length,
    sha256: sha, redacted: false, truncated: false, untrusted: true });
  return { protocol_version: "github-review-snapshot.v1", id: "snapshot-exact",
    identity: { repository: connection.repository, number: 1, node_id: "PR_fixture_1",
      state: "open", merged: false, draft: false, fork: false, base_ref: "main", base_sha: "c".repeat(40),
      head_ref: "codex/fixture", head_sha: oid, merge_base_sha: "c".repeat(40), updated_at: timestamp },
    capability: { protocol_version: "github-review-capability.v1", generation: sha,
      api_host: "api.github.com", api_version: "2026-03-10", account_login: "fixture-operator",
      installation_id: 1, repository: connection.repository, credential: connection.credential,
      permissions: { pull_requests: "write" }, read: true, reply: true, resolve: true,
      review: true, request_reviewer: true, push: false, logs: false, captured_at: timestamp },
    title: text("Fixture PR"), body: text(""), author: "fixture-operator", requested_reviewers: [],
    files: [], reviews: [], threads: [], loose_comments: [], check_suites: [], check_runs: [],
    jobs: [], artifacts: [], pagination: [], state: "verified", omissions: [], fingerprint: sha,
    rate_limit: { limit: 5000, remaining: 4999, used: 1, reset_at: timestamp }, fetched_at: timestamp };
}

function githubProjection(runID: string, connectionID: string, pullRequest = 0): GitHubReviewProjectionView {
  if (connectionID !== githubConnection().id) throw new Error(`Unknown fixture connection ${connectionID}`);
  const credential = githubCredential();
  const snapshot = githubSnapshot();
  const delivery = standardCodeDeliveryFixture();
  return { protocol_version: "github-review-api.v1", run_id: runID,
    connection: credential.connection, credential: credential.credential,
    // Zero browses the saved snapshots; a selected PR must match its identity.
    snapshots: pullRequest === 0 || pullRequest === snapshot.identity.number ? [snapshot] : [],
    evidence: [], writes: [], standard_code_delivery: { ...delivery,
      binding: { ...delivery.binding, run_id: runID, session_id: `${runID}-session`,
        source_workspace_id: `${runID}-workspace` },
      diff: { ...delivery.diff, changed_count: 0, files: [] }, verifications: [] } };
}

function githubReviewedWrite(runID: string,
  body: Parameters<APIClient["reviewGitHubWrite"]>[1]): GitHubReviewWriteReviewResultView {
  const preview: GitHubReviewWriteReviewResultView["preview"] = {
    protocol_version: body.spec.protocol_version, id: "github-preview-exact", operation: body.spec.operation,
    identity: body.spec.identity, credential: body.spec.credential,
    capability_generation: body.spec.capability_generation, reviewers: body.spec.reviewers,
    body_summary: body.spec.body, body_sha256: sha, review_event: body.spec.review_event,
    target_id: body.spec.target_id, validation_summary: body.spec.validation_summary,
    local_change_summary: body.spec.local_change_summary, approval_fingerprint: sha,
    idempotency_marker: `fixture:${body.operation_key}`, created_at: timestamp,
  };
  return { protocol_version: "github-review-api.v1", preview, replayed: false,
    operation: { protocol_version: "github-review-api.v1", id: "github-write-exact", run_id: runID,
      session_id: `${runID}-session`, workspace_id: `${runID}-workspace`, connection_id: body.connection_id,
      operation_key_sha256: sha, request_fingerprint: sha, approval_fingerprint: preview.approval_fingerprint,
      approval_id: "github-approval-exact", status: "awaiting_approval", preview, created_at: timestamp,
      receipt: { protocol_version: "github-review-write.v1", id: "github-receipt-exact",
        preview_id: preview.id, identity: preview.identity, operation: preview.operation,
        idempotency_marker: preview.idempotency_marker, status: "pending", recovered: false,
        started_at: timestamp, completed_at: timestamp } },
    approval: { ID: "github-approval-exact", RunID: runID, SessionID: `${runID}-session`,
      WorkspaceID: `${runID}-workspace`, ActionClass: "github_write", ToolName: "github-review",
      Status: "pending", Mode: "explicit", Version: 1, ProposalID: "github-write-exact",
      GrantID: "", IdempotencyKey: body.operation_key, RequestFingerprint: preview.approval_fingerprint,
      RequestedBy: "fixture-operator", ReviewedBy: "", DecisionReason: "",
      CreatedAt: timestamp, UpdatedAt: timestamp, DecidedAt: timestamp } };
}

function client() {
  return {
    get: vi.fn().mockRejectedValue(new Error("Overview is outside this fixture")),
    hasBatchDeliveryControl: true, hasBatchDeliveryHostValidation: false,
    hasGitAdvancedControl: true, hasGitHubReviewControl: true,
    hasUIEvidence: false, uiEvidenceUnavailableReason: "ui_evidence_disabled",
    getRunBatchDeliveries: vi.fn().mockResolvedValue({ items: [] }),
    gitAdvancedProjection: vi.fn((runID: string) => Promise.resolve(gitProjection(runID))),
    githubReviewConnections: vi.fn().mockResolvedValue([githubCredential()]),
    githubReviewCredential: vi.fn((connectionID: string) => {
      if (connectionID !== githubConnection().id) return Promise.reject(new Error(`Unknown fixture connection ${connectionID}`));
      return Promise.resolve(githubCredential());
    }),
    githubReviewProjection: vi.fn((runID: string, connectionID: string, pullRequest = 0) =>
      Promise.resolve(githubProjection(runID, connectionID, pullRequest))),
    uiEvidence: vi.fn().mockResolvedValue([]),
    approvalQueue: vi.fn().mockResolvedValue({ items: [] }),
    controlledCommandProposals: vi.fn().mockResolvedValue({ items: [] }),
    hostCommandProposals: vi.fn().mockResolvedValue({ items: [] }),
    reviewGitAdvanced: vi.fn((runID: string) => Promise.resolve({
      run_id: runID, workspace_id: `${runID}-workspace`,
      preview: { id: "preview-exact", operation: "stash_create", summary: "Exact selected Run",
        blocked_reasons: [], files: [], hunks: [], recovery: { required: false, incomplete_reasons: [] } },
      operation: { id: "operation-exact" }, approval: { ID: "approval-exact" },
    })),
    executeGitAdvanced: vi.fn().mockResolvedValue({ receipt: { status: "succeeded" } }),
    reviewGitHubWrite: vi.fn((runID: string, body: Parameters<APIClient["reviewGitHubWrite"]>[1]) =>
      Promise.resolve(githubReviewedWrite(runID, body))),
    executeGitHubWrite: vi.fn().mockResolvedValue({ receipt: { status: "succeeded" } }),
    runCapabilityReadiness: vi.fn().mockRejectedValue(new Error("Readiness is outside this fixture")),
    standardCodeDelivery: vi.fn().mockRejectedValue(new Error("Delivery is outside this fixture")),
  };
}
function renderReview(api: ReturnType<typeof client>, value = detail()) {
  const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false }, mutations: { retry: false } } });
  const node = (next: ThreadDetailView) => <QueryClientProvider client={queryClient}>
    <V2TaskReview client={api as unknown as APIClient} detail={next} working={false}
      returnFocusRef={createRef()} onClose={vi.fn()} onRequestChange={vi.fn()} />
  </QueryClientProvider>;
  return { ...render(node(value)), node };
}
async function chooseTool(user: ReturnType<typeof userEvent.setup>, name: string) {
  await user.click(within(screen.getByRole("group", { name: "选择交付工具" }))
    .getByRole("button", { name }));
}

it("loads advanced tools only after selection and binds every panel to the explicitly chosen historical Run", async () => {
  const api = client();
  const user = userEvent.setup();
  renderReview(api);
  expect(api.getRunBatchDeliveries).not.toHaveBeenCalled();
  expect(api.gitAdvancedProjection).not.toHaveBeenCalled();
  expect(api.githubReviewConnections).not.toHaveBeenCalled();
  expect(api.uiEvidence).not.toHaveBeenCalled();
  await user.click(screen.getByRole("button", { name: "更多交付工具" }));
  await user.selectOptions(screen.getByRole("combobox", { name: "选择审阅的执行记录" }), "run-history");
  expect(api.getRunBatchDeliveries).not.toHaveBeenCalled();
  expect(api.gitAdvancedProjection).not.toHaveBeenCalled();
  expect(api.githubReviewConnections).not.toHaveBeenCalled();
  expect(api.uiEvidence).not.toHaveBeenCalled();

  await chooseTool(user, "批量交付");
  await screen.findByText("暂无批量交付计划");
  expect(api.getRunBatchDeliveries).toHaveBeenCalledWith("run-history", expect.any(AbortSignal));
  expect(api.gitAdvancedProjection).not.toHaveBeenCalled();
  await chooseTool(user, "高级 Git");
  await screen.findByText("run-history-branch");
  expect(api.gitAdvancedProjection).toHaveBeenCalledWith("run-history", expect.any(AbortSignal));
  expect(api.githubReviewConnections).not.toHaveBeenCalled();
  await chooseTool(user, "GitHub 审阅");
  await waitFor(() => expect(api.githubReviewProjection).toHaveBeenCalledWith(
    "run-history", "connection-1", 0, expect.any(AbortSignal)));
  expect(api.uiEvidence).not.toHaveBeenCalled();
  await chooseTool(user, "UI 取证");
  await screen.findByText(/还没有浏览器验证/u);
  expect(api.uiEvidence).toHaveBeenCalledWith("run-history", expect.any(AbortSignal));
  await user.selectOptions(screen.getByRole("combobox", { name: "选择审阅的执行记录" }), "run-current");
  expect(within(screen.getByRole("group", { name: "选择交付工具" })).queryByRole("button", { pressed: true })).toBeNull();
  expect(api.uiEvidence).toHaveBeenCalledTimes(1);
  await chooseTool(user, "UI 取证");
  await waitFor(() => expect(api.uiEvidence).toHaveBeenCalledWith("run-current", expect.any(AbortSignal)));
});

it("preserves the exact Git review through same-Run approval navigation and clears it on Run changes", async () => {
  const api = client();
  const user = userEvent.setup();
  renderReview(api);
  await user.click(screen.getByRole("button", { name: "更多交付工具" }));
  await user.selectOptions(screen.getByRole("combobox", { name: "选择审阅的执行记录" }), "run-history");
  await chooseTool(user, "高级 Git");
  await user.click(await screen.findByRole("button", { name: "请求创建审批" }));
  await screen.findByText("approval-exact");
  expect(api.reviewGitAdvanced).toHaveBeenCalledWith("run-history", expect.objectContaining({
    scope: { capability_generation: sha, lease_generation: 3 },
    spec: expect.objectContaining({ operation: "stash_create" }),
  }));
  expect(api.executeGitAdvanced).not.toHaveBeenCalled();
  await user.click(screen.getByRole("button", { name: "打开审批" }));
  await screen.findByText("没有待处理审批");
  expect(api.approvalQueue).toHaveBeenCalledWith("run-history", expect.any(AbortSignal));
  await user.click(screen.getByRole("button", { name: "返回所选工具" }));
  await user.click(await screen.findByRole("button", { name: "执行已审批操作" }));
  await waitFor(() => expect(api.executeGitAdvanced).toHaveBeenCalledWith("run-history", {
    operation_id: "operation-exact", approval_id: "approval-exact",
    scope: { capability_generation: sha, lease_generation: 3 },
  }));
  await user.click(screen.getByRole("button", { name: "请求创建审批" }));
  await screen.findByText("approval-exact");
  await user.selectOptions(screen.getByRole("combobox", { name: "选择审阅的执行记录" }), "run-current");
  await chooseTool(user, "高级 Git");
  await screen.findByText("run-current-branch");
  expect(screen.queryByText("approval-exact")).not.toBeInTheDocument();
  expect(screen.queryByRole("button", { name: "执行已审批操作" })).not.toBeInTheDocument();
  expect(api.executeGitAdvanced).toHaveBeenCalledTimes(1);
});

it("refuses to substitute the current Run when the selected historical Run disappears", async () => {
  const api = client();
  const user = userEvent.setup();
  const view = renderReview(api);
  await user.click(screen.getByRole("button", { name: "更多交付工具" }));
  await user.selectOptions(screen.getByRole("combobox", { name: "选择审阅的执行记录" }), "run-history");
  await chooseTool(user, "批量交付");
  await screen.findByText("暂无批量交付计划");
  const changed = detail();
  changed.runs = changed.runs.filter(({ run }) => run.id !== "run-history");
  view.rerender(view.node(changed));
  expect(screen.getByRole("alert")).toHaveTextContent("无法找到目标执行记录 run-history");
  expect(screen.queryByRole("group", { name: "选择交付工具" })).not.toBeInTheDocument();
  expect(api.getRunBatchDeliveries).toHaveBeenCalledTimes(1);
  expect(api.getRunBatchDeliveries.mock.calls[0][0]).toBe("run-history");
});

it("retains a GitHub write through approval and delivery pages without rebinding it to the current Run", async () => {
  const api = client();
  const user = userEvent.setup();
  renderReview(api);
  await user.click(screen.getByRole("button", { name: "更多交付工具" }));
  await user.selectOptions(screen.getByRole("combobox", { name: "选择审阅的执行记录" }), "run-history");
  await chooseTool(user, "GitHub 审阅");
  await user.click(await screen.findByRole("button", { name: "生成精确预览" }));
  await screen.findByRole("button", { name: "执行已批准操作" });
  expect(api.reviewGitHubWrite).toHaveBeenCalledWith("run-history", expect.objectContaining({
    connection_id: "connection-1", snapshot_id: "snapshot-exact",
    spec: expect.objectContaining({ capability_generation: sha }),
  }));
  expect(api.executeGitHubWrite).not.toHaveBeenCalled();
  await user.click(screen.getByRole("button", { name: "打开审批" }));
  await screen.findByText("没有待处理审批");
  expect(api.approvalQueue).toHaveBeenCalledWith("run-history", expect.any(AbortSignal));
  await user.click(screen.getByRole("button", { name: "返回所选工具" }));
  await user.click(await screen.findByRole("button", { name: "打开交付页" }));
  expect(screen.getByRole("combobox", { name: "选择审阅的执行记录" })).toHaveValue("run-history");
  await user.click(screen.getByRole("button", { name: "更多交付工具" }));
  await chooseTool(user, "GitHub 审阅");
  await user.click(await screen.findByRole("button", { name: "执行已批准操作" }));
  await waitFor(() => expect(api.executeGitHubWrite).toHaveBeenCalledWith(
    "run-history", "github-write-exact", "github-approval-exact"));
});
