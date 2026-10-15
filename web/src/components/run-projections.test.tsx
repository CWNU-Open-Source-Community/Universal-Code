import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import type { APIClient } from "../api/client";
import type { AgentGraphView, BatchDeliverySnapshotView, ExternalSkillProjectionView, FindingReportView } from "../api/types";
import { AgentGraphPanel, BatchDeliveriesPanel, DelegationsPanel, ExternalSkillsPanel, FanoutPanel, FindingsPanel } from "./run-projections";
import { batchWorkbenchFixture } from "../test/batch-workbench-fixture";

function provider(children: React.ReactNode) {
  return <QueryClientProvider client={new QueryClient({ defaultOptions: { queries: { retry: false } } })}>{children}</QueryClientProvider>;
}

describe("Run projection panels", () => {
  it.each([false, true])("keeps batch host validation separate from batch control when enabled=%s", async (enabled) => {
    const client = { hasBatchDeliveryControl: true, hasBatchDeliveryHostValidation: enabled,
      getRunBatchDeliveries: vi.fn().mockResolvedValue({ items: [] }),
    } as unknown as APIClient;
    const { container } = render(provider(<BatchDeliveriesPanel client={client} runID="run-1" />));
    await screen.findByText("No batch delivery plans yet. Describe independently deliverable subtasks in the conversation to prepare one.");
    expect(container.textContent).toContain("selected execution");
    expect(container.textContent).toContain("current Full process activation");
    if (enabled) {
      expect(container.textContent).toContain("selected Local, Docker, or SBX environment");
      expect(container.textContent).toContain("refuses execution when it is not ready");
    } else {
      expect(container.textContent).toContain("--enable-batch-validation-execution");
      expect(container.textContent).toContain("--enable-permission-control");
      expect(container.textContent).toContain("--enable-danger-full-access");
      expect(container.textContent).toContain("control credential");
      expect(container.textContent).toContain("operator approval");
    }
    expect(screen.queryByRole("button", { name: "Merge in dependency order" })).not.toBeInTheDocument();
  });

  it("lets an aborted plan retry pending cleanup without reopening merge", async () => {
    const current = batchWorkbenchFixture("ready_for_review");
    current.snapshot.plan.status = "aborted";
    const cancel = vi.fn().mockResolvedValue({});
    const client = { hasBatchDeliveryControl: true,
      getRunBatchDeliveries: vi.fn().mockResolvedValue({ items: [current.snapshot.plan] }),
      getRunBatchDelivery: vi.fn().mockResolvedValue(current.snapshot),
      cancelRunBatchDelivery: cancel,
    } as unknown as APIClient;
    const user = userEvent.setup();
    render(provider(<BatchDeliveriesPanel client={client} runID="run-1" />));
    await user.click(await screen.findByRole("button", { name: "Retry cancellation cleanup" }));
    expect(cancel).toHaveBeenCalledWith("run-1", current.snapshot.plan.id,
      expect.objectContaining({ version: "batch_delivery_cancel.v1", confirm: true }), expect.stringMatching(/^web-batch-cancel-/));
    expect(screen.queryByRole("button", { name: "Merge in dependency order" })).not.toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Reconcile state" })).not.toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Accept" })).not.toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Request changes" })).not.toBeInTheDocument();
  });

  it("explains the independent Desktop batch control switch while preserving read access", async () => {
    const getRunBatchDeliveries = vi.fn().mockResolvedValue({ items: [] });
    const client = { hasBatchDeliveryControl: false, hasBatchDeliveryHostValidation: false,
      getRunBatchDeliveries } as unknown as APIClient;
    render(provider(<BatchDeliveriesPanel client={client} runID="run-1" />));
    expect(await screen.findByText(/--enable-batch-delivery-control/)).toBeInTheDocument();
    expect(getRunBatchDeliveries).toHaveBeenCalledWith("run-1", expect.any(AbortSignal));
    expect(screen.queryByRole("button", { name: "Accept" })).not.toBeInTheDocument();
  });

  it("renders bounded external Skill metadata without a mutation control", () => {
    const projection: ExternalSkillProjectionView = {
      protocol_version: "external_skill_projection.v1",
      run_id: "run-1",
      mode_revision: 2,
      surface: "code",
      profile: "review",
      token_budget: 1024,
      token_upper_bound: 180,
      item_count: 1,
      operator_confirmed: true,
      context_delivery_authorized: true,
      tool_capability_grant: false,
      root_delivery: { prepared: 2, committed: 1 },
      specialist_delivery: { prepared: 1, committed: 1 },
      items: [{ ordinal: 1, name: "safe-review", version: "1.0.0",
        token_upper_bound: 180, trust_class: "operator_installed_untrusted",
        declared_tool_count: 2, specialist_eligible: true }],
      created_at: "2026-07-17T00:00:00Z",
    };
    const { container } = render(<ExternalSkillsPanel projection={projection} />);
    expect(screen.getByText("safe-review@1.0.0")).toBeInTheDocument();
    expect(screen.getByText("operator_installed_untrusted")).toBeInTheDocument();
    expect(screen.getByText("1 / 2")).toBeInTheDocument();
    expect(screen.getByText("Confirmed")).toBeInTheDocument();
    expect(screen.getByText("Authorized")).toBeInTheDocument();
    expect(screen.getByText("Closed")).toBeInTheDocument();
    expect(container.querySelector("button")).toBeNull();
    expect(container.textContent).not.toContain("SKILL.md");
    expect(container.textContent).not.toContain("sha256/");
  });

  it("renders the bounded Agent graph and completion summary", async () => {
    const graph: AgentGraphView = {
      protocol_version: "agent_graph.v1",
      run_id: "run-1",
      root_agent_id: "agent-root",
      nodes: [{ id: "agent-root", session_id: "session-root", role: "root", profile: "review",
        skills: ["model.chat"], status: "running", depth: 0, child_limit: 2,
        turn_limit: 8, token_limit: 2000, turns_used: 2, tokens_used: 320,
        version: 2, created_at: "2026-07-13T00:00:00Z", updated_at: "2026-07-13T00:01:00Z" },
      { id: "agent-child", parent_id: "agent-root", session_id: "session-child", role: "specialist",
        profile: "review", skills: ["note_create"], status: "completed", depth: 1, child_limit: 0,
        turn_limit: 2, token_limit: 300, turns_used: 1, tokens_used: 90, version: 3,
        created_at: "2026-07-13T00:00:10Z", updated_at: "2026-07-13T00:01:20Z",
        completion: { id: "completion-1", attempt_id: "attempt-1", outcome: "succeeded",
          summary: "Reviewed the parser boundary", work_item_ids: [], note_ids: [],
          created_at: "2026-07-13T00:01:20Z" } }],
    };
    const client = { get: vi.fn(async (path: string) => path === "/runs/run-1"
      ? { run: { status: "running" } } : graph) } as unknown as APIClient;
    render(provider(<AgentGraphPanel client={client} runID="run-1" />));
    expect(await screen.findByText("Reviewed the parser boundary")).toBeInTheDocument();
    expect(screen.getByText("agent-root")).toBeInTheDocument();
    expect(screen.getByText("agent-child")).toBeInTheDocument();
  });

  it("shows narrowed batch child authority and requires an explicit independent-review attestation", async () => {
    const base = "a".repeat(40);
    const plan = {
      id: "batch-1", run_id: "run-1", proposal_id: "proposal-1",
      root_agent_id: "agent-root", workspace_id: "workspace-1", status: "reviewing",
      spec: { version: "batch-delivery.v1", tasks: [{ ordinal: 1,
        ownership_hints: [{ path: "internal/parser", kind: "directory" }],
        dependency_ordinals: [], budget: { turn_limit: 2, token_limit: 1_024,
          timeout_millis: 120_000 },
        validations: [{ id: "diff", kind: "git_diff_check", scope: "." }],
        expected_artifacts: [],
      }], contract: { require_clean: true, require_independent_review: true,
        require_all_validations: true, max_changed_files: 32, max_diff_bytes: 1_048_576 } },
      base_commit: base, source_branch: "main", created_by: "operator",
      created_at: "2026-08-20T00:00:00Z", updated_at: "2026-08-20T00:01:00Z",
    } as const;
    const snapshot = {
      protocol_version: "batch-delivery.v1", plan,
      children: [{ workspace: { plan_id: "batch-1", ordinal: 1,
        agent_id: "agent-child", generation: 1, status: "ready_for_review",
        branch: "codex/batch/task-1", base_commit: base, head_commit: "b".repeat(40),
        tool_profile: { version: "batch-delivery-tools.v1", workspace_list: true,
          workspace_read: true, workspace_search: true, workspace_change: true,
          workspace_apply: true, git_status: true, git_diff: true, git_commit: true,
          workspace_delete: false, shell: false, process: false, network: false,
          credentials: false, debug_terminal: false, approvals: false,
          spawn_children: false }, lease_expires_at: "2026-08-20T01:00:00Z",
        last_heartbeat_at: "2026-08-20T00:01:00Z",
        created_at: "2026-08-20T00:00:00Z", updated_at: "2026-08-20T00:01:00Z" },
        mailbox: [],
        receipt: { id: "receipt-1", ordinal: 1, generation: 1,
          protocol_version: "batch-delivery-receipt.v1", base_commit: base,
          head_commit: "b".repeat(40), diff_sha256: "c".repeat(64),
          call_chain_sha256: "d".repeat(64), diff_bytes: 128,
          diff_stat: "1 file changed, 1 insertion(+)", changed_files: ["internal/parser/a.go"],
          test_receipts: [{ requirement_id: "diff", kind: "git_diff_check", scope: ".",
            exit_code: 0, output_sha256: "e".repeat(64), duration_millis: 10,
            completed_at: "2026-08-20T00:01:00Z" }], evidence_refs: [], limitations: [],
          created_at: "2026-08-20T00:01:00Z" },
      }], merge_steps: [],
    } as unknown as BatchDeliverySnapshotView;
    const client = { hasBatchDeliveryControl: true,
      getRunBatchDeliveries: vi.fn().mockResolvedValue({
        protocol_version: "batch-deliveries-list.v1", items: [plan],
      }),
      getRunBatchDelivery: vi.fn().mockResolvedValue(snapshot),
    } as unknown as APIClient;
    const user = userEvent.setup();
    const { container } = render(provider(<BatchDeliveriesPanel client={client} runID="run-1" />));

    expect(await screen.findByText(/Execution scope: the project tools above/)).toBeInTheDocument();
    const accept = screen.getByRole("button", { name: "Accept" });
    expect(accept).toBeDisabled();
    await user.type(screen.getByLabelText("Independent review summary"), "Reviewed exact diff");
    expect(accept).toBeDisabled();
    await user.click(screen.getByLabelText(/independently reviewed the full merge-base diff/));
    expect(accept).toBeEnabled();
    expect(container.textContent).not.toContain("owner_token");
    expect(container.textContent).not.toContain("worktree_root");
  });

  it("renders delegation, Fan-out, and Finding projections from read-only pages", async () => {
    const detail: FindingReportView = {
      report: { id: "report-1", run_id: "run-1", source_kind: "readonly_fanout_execution",
        source_id: "execution-1", status: "generated", title: "Security review",
        finding_count: 1, evidence_count: 1,
        severity: { info: 0, low: 0, medium: 0, high: 1, critical: 0 },
        created_at: "2026-07-13T00:02:00Z" },
      findings: [{ id: "finding-1", ordinal: 1, status: "validated", severity: "high",
        category: "path", title: "Boundary issue", detail: "A bounded Finding detail",
        relative_path: "src/main.go", line_start: 12, line_end: 12, confidence: 92,
        evidence: [], artifact_evidence: [], remediation_evidence: [],
        lifecycle: { status: "validated", validation_evidence_count: 1, remediation_evidence_count: 0 } }],
    };
    const getPage = vi.fn().mockImplementation((path: string) => {
      if (path.endsWith("/delegations")) return Promise.resolve({ items: [{ id: "delegation-1", run_id: "run-1",
        root_agent_id: "agent-root", status: "proposed", requested_by: "run_supervisor",
        created_at: "2026-07-13T00:00:00Z", assignments: [{ ordinal: 1, title: "Parser review",
          goal: "Inspect parser boundaries", skills: ["model.chat"], turn_limit: 2, token_limit: 300 }] }],
        page: { limit: 50 }, requestID: "req-delegations" });
      if (path.endsWith("/fanout-plans")) return Promise.resolve({ items: [{ id: "fanout-1", run_id: "run-1",
        workspace_id: "ws-1", scope_path: ".", goal: "Audit source modules", protocol_version: "readonly_fanout.v1",
        requested_tier: "4", effective_parallelism: 4, status: "planned", file_count: 12,
        total_bytes: 4096, excluded_count: 3, shard_count: 4, requested_by: "operator",
        created_at: "2026-07-13T00:01:00Z" }], page: { limit: 50 }, requestID: "req-fanout" });
      return Promise.resolve({ items: [detail.report], page: { limit: 50 }, requestID: "req-reports" });
    });
    const client = { getPage, get: vi.fn().mockResolvedValue(detail) } as unknown as APIClient;
    render(provider(<><DelegationsPanel client={client} runID="run-1" /><FanoutPanel client={client} runID="run-1" /><FindingsPanel client={client} runID="run-1" /></>));
    expect(await screen.findByText("Inspect parser boundaries")).toBeInTheDocument();
    expect(await screen.findByText("Audit source modules")).toBeInTheDocument();
    expect(await screen.findByText("Boundary issue")).toBeInTheDocument();
    expect(screen.getByText("A bounded Finding detail")).toBeInTheDocument();
  });
});
