import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import type { APIClient } from "../api/client";
import { HostCommandProposalPanel } from "./host-command-proposal-panel";

const proposal = {
  id: "host-command-proposal-1",
  protocol_version: "host_command_proposal.v1",
  policy_version: "host_command_policy.v1",
  run_id: "run-1",
  mission_id: "mission-1",
  session_id: "session-1",
  workspace_id: "workspace-1",
  executable_path: "C:\\Program Files\\Go\\bin\\go.exe",
  executable_sha256: "a".repeat(64),
  argv: ["test", "./internal/application"],
  working_directory: "D:\\GitProjects\\Prayu",
  environment_policy: "sanitized_host_environment.v1",
  environment_keys: ["PATH", "SYSTEMROOT"],
  environment_sha256: "b".repeat(64),
  network_intent: "host",
  timeout_milliseconds: 120_000,
  purpose: "Run the focused application tests",
  spec_fingerprint: "c".repeat(64),
  permission_mode: "approval",
  permission_revision: 3,
  operator_review_required: true,
  non_sandboxed: true,
  automatic_retry_allowed: false,
  instruction_authorized: false,
  execution_authorized: false,
  capability_grant: false,
  fingerprint: "d".repeat(64),
  created_at: "2026-08-09T00:00:00Z",
  evidence_instruction_authorized: false,
};

const riskProposal = {
  ...proposal,
  id: "risk-escalation-1",
  protocol_version: "risk_escalation.v1",
  policy_version: "risk_escalation_policy.v1",
  permission_mode: "workspace_access",
  permission_revision: 4,
  state: "waiting_approval",
  supervisor_turn: 2,
  supervisor_tool_call_id: "call-1",
  tool_invocation_id: "invocation-1",
  mode_snapshot_id: "mode-1",
  mode_revision: 2,
  interaction_snapshot_id: "interaction-1",
  interaction_revision: 3,
  execution_profile_snapshot_id: "profile-1",
  execution_profile_revision: 3,
  permission_snapshot_id: "permission-1",
  workspace_root_fingerprint: "e".repeat(64),
  capability_generation: "f".repeat(64),
  scope_fingerprint: "1".repeat(64),
  risk_kinds: ["credential", "host_path", "network", "policy_denial"],
  network_targets: ["proxy.golang.org:443"],
  network_purpose: "Download the declared module",
  credential_kinds: ["system_proxy"],
  host_paths: ["C:\\ProgramData\\go\\cache"],
  policy_code: "workspace_network_denied",
  policy_reason: "Workspace network is denied",
  max_output_bytes: 64 * 1024 * 1024,
  active_process_limit: 32,
  process_memory_bytes: 2 * 1024 * 1024 * 1024,
  approval_id: "approval-1",
  approval_status: "pending",
};

describe("HostCommandProposalPanel", () => {
  afterEach(() => {
    vi.unstubAllGlobals();
  });

  it("reads retired proposals without approval or grant controls", async () => {
    const client={hostCommandProposals:vi.fn().mockResolvedValue({items:[riskProposal]})} as unknown as APIClient;
    renderPanel(client);
    expect(await screen.findByText(proposal.purpose)).toBeInTheDocument();
    expect(screen.queryByRole("checkbox")).not.toBeInTheDocument();
    expect(screen.queryByRole("button",{name:/approve|deny|grant|continue/i})).not.toBeInTheDocument();
    expect(screen.getByText("proxy.golang.org:443")).toBeInTheDocument();
  });


  it("keeps unknown Host execution visible without a continuation action", async () => {
    const client={hasRunExecution:true,hostCommandProposals:vi.fn().mockResolvedValue({items:[{...proposal,review:approvedReview(),uncertain:true}]})} as unknown as APIClient;
    renderPanel(client,"thread-1");
    expect(await screen.findByText("Approved; the execution result needs confirmation. Refresh command records to check the state before proceeding.")).toBeInTheDocument();
    expect(screen.queryByRole("button",{name:"Continue from saved outcome"})).not.toBeInTheDocument();
  });

  it.each([{ exit: 7, cancelled: false }, { exit: 0, cancelled: true }])(
    "retains an exact failed command receipt and reads saved output in the conversation: $exit/$cancelled", async ({ exit, cancelled }) => {
      const recorded = recordedProposal(exit, cancelled);
      const detail = vi.fn().mockResolvedValue({ ...recorded,
        untrusted_evidence: "UNTRUSTED HOST COMMAND RESULT\nstderr_begin\nactual command output\nstderr_end" });
      const review = vi.fn();
      const client = {
        hostCommandProposals: vi.fn().mockResolvedValue({ items: [recorded], page: { limit: 100 } }),
        hostCommandProposal: detail, resumeHostCommandProposal: review } as unknown as APIClient;
      renderPanel(client, "thread-1");
      expect(await screen.findByText(`Execution result recorded, exit code ${exit}`)).toBeInTheDocument();
      if (cancelled) expect(screen.getByText("The command was cancelled.")).toBeInTheDocument();
      expect(detail).not.toHaveBeenCalled();
      await userEvent.setup().click(screen.getByRole("button", { name: "Read saved output" }));
      expect(await screen.findByText(/actual command output/)).toBeInTheDocument();
      expect(detail).toHaveBeenCalledWith("run-1", proposal.id, expect.any(AbortSignal));
      expect(review).not.toHaveBeenCalled();
    });

  it("resumes only a saved outcome and retains continuation failure after refresh", async () => {
    const denied={...proposal,review:{...approvedReview(),decision:"deny",single_use_execution_authorized:false}};
    const queue=vi.fn().mockResolvedValue({items:[denied]});
    const resume=vi.fn().mockResolvedValue({...denied,continuation:{state:"failed",replayed:false,model_called:true,tool_called:false,error_code:"FAILED_PRECONDITION"}});
    const client={hasRunExecution:true,hostCommandProposals:queue,resumeHostCommandProposal:resume} as unknown as APIClient;
    renderPanel(client,"thread-1");
    await userEvent.setup().click(await screen.findByRole("button",{name:"Continue from saved outcome"}));
    expect(resume).toHaveBeenCalledWith("run-1",proposal.id);
    await waitFor(()=>expect(queue.mock.calls.length).toBeGreaterThanOrEqual(2));
    expect(screen.getByText("Review saved, but subsequent execution failed. Check the records and continue in this conversation.")).toBeInTheDocument();
    expect(screen.getByText("Proposal denied; no execution result.")).toBeInTheDocument();
  });

  it("does not display saved output from a different execution binding", async () => {
    const recorded = recordedProposal(7, false);
    const detail = vi.fn().mockResolvedValue({ ...recorded, session_id: "other-session",
      untrusted_evidence: "wrong execution output" });
    const client = {
      hostCommandProposals: vi.fn().mockResolvedValue({ items: [recorded], page: { limit: 100 } }),
      hostCommandProposal: detail } as unknown as APIClient;
    renderPanel(client, "thread-1");
    await userEvent.setup().click(await screen.findByRole("button", { name: "Read saved output" }));
    expect(await screen.findByText("Saved output does not match this command receipt.")).toBeInTheDocument();
    expect(screen.queryByText("wrong execution output")).not.toBeInTheDocument();
  });

  it.each(["UX_Q_HOST_ACTUAL_OUTPUT \uFFFD", "UX_Q_HOST_ACTUAL_OUTPUT 中文"])(
    "explains damaged saved Host text without altering it or retrying execution: %s", async (output) => {
      const recorded = recordedProposal(7, false);
      const detail = vi.fn().mockResolvedValue({ ...recorded, untrusted_evidence: output });
      const review = vi.fn();
      const client = {
        hostCommandProposals: vi.fn().mockResolvedValue({ items: [recorded], page: { limit: 100 } }),
        hostCommandProposal: detail, resumeHostCommandProposal: review } as unknown as APIClient;
      renderPanel(client, "thread-1");
      await userEvent.setup().click(await screen.findByRole("button", { name: "Read saved output" }));
      const saved = await screen.findByText(output);
      expect(saved.tagName).toBe("PRE");
      expect(saved.textContent).toBe(output);
      expect(Boolean(screen.queryByText("Some characters could not be decoded correctly; the displayed text may be incomplete.")))
        .toBe(output.includes("\uFFFD"));
      expect(screen.getByText("Execution result recorded, exit code 7")).toBeInTheDocument();
      expect(detail).toHaveBeenCalledTimes(1);
      expect(review).not.toHaveBeenCalled();
    });
});

function approvedReview() {
  return { id: "review-1", decision: "approve", reviewed_by: "http_control_operator", reason: "Exact command approved",
    single_use_execution_authorized: true, capability_grant: false, created_at: "2026-08-09T00:01:00Z" };
}

function recordedProposal(exit: number, cancelled: boolean) {
  return { ...proposal, review: approvedReview(),
    result: { id: "result-1", status: "failed", content_sha256: "e".repeat(64) },
    receipt: { request_id: "host-exec-1", exit_code: exit, cancelled, timed_out: false,
      stdout_truncated: false, stderr_truncated: false, output_limit_exceeded: false } };
}

function renderPanel(client: APIClient, threadID = "", compact = false) {
  const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false },
    mutations: { retry: false } } });
  return render(<QueryClientProvider client={queryClient}>
    <HostCommandProposalPanel client={client} runID="run-1" threadID={threadID} compact={compact} />
  </QueryClientProvider>);
}

it("keeps uncertain approvals and failed continuations actionable in the execution records", async () => {
  const settled = { ...recordedProposal(7, false), id: "settled", purpose: "Settled command" };
  const uncertain = { ...proposal, id: "uncertain", purpose: "Uncertain command", review: approvedReview() };
  const failed = { ...recordedProposal(0, false), id: "failed-continuation", purpose: "Failed continuation",
    continuation: { state: "failed", replayed: false, model_called: true, tool_called: false } };
  const review = vi.fn();
  const client = {  resumeHostCommandProposal: review,
    hostCommandProposals: vi.fn().mockResolvedValue({ items: [settled, uncertain, failed], page: { limit: 100 } }) } as unknown as APIClient;
  renderPanel(client, "thread-1", true);
  expect(await screen.findByText("Uncertain command")).toBeInTheDocument();
  expect(screen.getByText("Failed continuation")).toBeInTheDocument();
  expect(screen.queryByText("Settled command")).not.toBeInTheDocument();
  expect(screen.getByText("Review saved, but subsequent execution failed. Check the records and continue in this conversation.")).toBeInTheDocument();
  expect(review).not.toHaveBeenCalled();
});
