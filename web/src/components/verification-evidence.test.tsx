import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act, render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import type { APIClient } from "../api/client";
import { VerificationEvidence } from "./verification-evidence";

const emptyPlans = () => ({
  protocol_version: "operator_verification_plan_inventory.v1", run_id: "run-1",
  session_id: "session-1", workspace_id: "workspace-1", items: [], truncated: false,
});

const emptyCoverage = () => ({
  protocol_version: "operator_verification_plan_coverage.v1", run_id: "run-1",
  session_id: "session-1", workspace_id: "workspace-1", plans: [], plan_count: 0,
  plan_item_count: 0, observed_plan_item_count: 0, associated_evidence_count: 0,
  associations: [], plans_truncated: false, associations_truncated: false,
  metadata_only: true, read_only: true, result_inferred: false, command_executed: false,
  model_assertion: false, record_rewritten: false, approval: false, authority_granted: false,
});

describe("VerificationEvidence", () => {
  it("records one operator observation through the distinct capability", async () => {
    const verificationEvidence = vi.fn().mockResolvedValue({
      protocol_version: "operator_verification_inventory.v1", run_id: "run-1",
      session_id: "session-1", workspace_id: "workspace-1", items: [],
      pass_count: 0, fail_count: 0, unknown_count: 0, truncated: false,
    });
    const verificationPlans = vi.fn().mockResolvedValue(emptyPlans());
    const verificationPlanCoverage = vi.fn().mockResolvedValue(emptyCoverage());
    const recordVerificationEvidence = vi.fn().mockResolvedValue({ id: "verification-1" });
    const client = { hasVerificationEvidence: true, verificationEvidence,
      verificationPlans, verificationPlanCoverage,
      recordVerificationEvidence } as unknown as APIClient;
    const queryClient = new QueryClient({ defaultOptions: {
      queries: { retry: false }, mutations: { retry: false },
    } });
    const user = userEvent.setup();
    render(<QueryClientProvider client={queryClient}>
      <VerificationEvidence client={client} runID="run-1" />
    </QueryClientProvider>);
    await screen.findByText("No verification evidence recorded");
    await user.selectOptions(screen.getByLabelText("Verification result"), "pass");
    await user.type(screen.getByLabelText("Title"), "Focused tests");
    await user.type(screen.getByLabelText("Summary"), "Go and React suites passed");
    await user.click(screen.getByRole("button", { name: "Record" }));
    await waitFor(() => expect(recordVerificationEvidence).toHaveBeenCalledTimes(1));
    expect(recordVerificationEvidence).toHaveBeenCalledWith("run-1", {
      version: "operator_verification_evidence.v1", outcome: "pass",
      title: "Focused tests", summary: "Go and React suites passed",
    }, expect.stringMatching(/^web-verification-/u));
    await waitFor(() => expect(screen.getByLabelText("Title")).toHaveValue(""));
    expect(screen.getByLabelText("Summary")).toHaveValue("");
  });

  it("locks pending evidence fields and preserves the draft when saving fails", async () => {
    let rejectSave!: (reason: Error) => void;
    const recordVerificationEvidence = vi.fn(() => new Promise((_, reject) => { rejectSave = reject; }));
    const client = {
      hasVerificationEvidence: true,
      verificationEvidence: async () => ({ items: [], pass_count: 0, fail_count: 0, unknown_count: 0 }),
      verificationPlans: async () => emptyPlans(),
      verificationPlanCoverage: async () => emptyCoverage(),
      recordVerificationEvidence,
    } as unknown as APIClient;
    const queryClient = new QueryClient({ defaultOptions: {
      queries: { retry: false }, mutations: { retry: false },
    } });
    const user = userEvent.setup();
    render(<QueryClientProvider client={queryClient}>
      <VerificationEvidence client={client} runID="run-1" />
    </QueryClientProvider>);
    await user.selectOptions(screen.getByLabelText("Verification result"), "pass");
    await user.type(screen.getByLabelText("Title"), "First result");
    await user.type(screen.getByLabelText("Summary"), "First evidence");
    await user.click(screen.getByRole("button", { name: "Record" }));
    await waitFor(() => expect(recordVerificationEvidence).toHaveBeenCalledTimes(1));
    expect(screen.getByLabelText("Verification result")).toBeDisabled();
    expect(screen.getByLabelText("Title")).toBeDisabled();
    expect(screen.getByLabelText("Summary")).toBeDisabled();
    await user.type(screen.getByLabelText("Title"), "Second result");
    await user.type(screen.getByLabelText("Summary"), "Second evidence");
    await act(async () => { rejectSave(new Error("Save failed")); });
    await waitFor(() => expect(screen.getByLabelText("Title")).toBeEnabled());
    expect(screen.getByLabelText("Title")).toHaveValue("First result");
    expect(screen.getByLabelText("Summary")).toHaveValue("First evidence");
    expect(screen.getByLabelText("Verification result")).toHaveValue("pass");
    queryClient.clear();
  });

  it("associates one immutable observation with one explicit plan item", async () => {
    const verificationEvidence = vi.fn().mockResolvedValue({
      protocol_version: "operator_verification_inventory.v1", run_id: "run-1",
      session_id: "session-1", workspace_id: "workspace-1", pass_count: 1,
      fail_count: 0, unknown_count: 0, truncated: false,
      items: [{ id: "evidence-1", outcome: "pass", title: "Focused tests",
        summary: "Observed a passing suite", recorded_at: "2026-07-20T01:02:00Z" }],
    });
    const verificationPlans = vi.fn().mockResolvedValue({
      ...emptyPlans(), items: [{ id: "plan-1", title: "Release checks",
        summary: "Operator guidance", created_at: "2026-07-20T01:00:00Z",
        items: [{ ordinal: 1, title: "Focused tests",
          expected_observation: "Observe an explicit result" }] }],
    });
    const verificationPlanCoverage = vi.fn().mockResolvedValue({
      ...emptyCoverage(), plan_count: 1, plan_item_count: 1,
      plans: [{ plan_id: "plan-1", plan_sha256: "a".repeat(64), item_count: 1,
        observed_item_count: 0, associated_evidence_count: 0,
        items: [{ ordinal: 1, item_sha256: "b".repeat(64), associated_evidence_count: 0,
          pass_count: 0, fail_count: 0, unknown_count: 0,
          latest_association_event_sequence: 0 }] }],
    });
    const associateVerificationEvidence = vi.fn().mockResolvedValue({ id: "association-1" });
    const client = { hasVerificationEvidence: true, verificationEvidence, verificationPlans,
      verificationPlanCoverage, recordVerificationEvidence: vi.fn(),
      associateVerificationEvidence } as unknown as APIClient;
    const user = userEvent.setup();
    const queryClient = new QueryClient();
    const { rerender } = render(<QueryClientProvider client={queryClient}>
      <VerificationEvidence client={client} runID="run-1" />
    </QueryClientProvider>);
    await screen.findByText("Observed a passing suite");
    await user.selectOptions(screen.getByLabelText("Plan item for Focused tests"),
      JSON.stringify(["plan-1", 1]));
    await user.click(screen.getByRole("button", { name: "Associate Focused tests" }));
    await waitFor(() => expect(associateVerificationEvidence).toHaveBeenCalledTimes(1));
    expect(associateVerificationEvidence).toHaveBeenCalledWith("run-1", {
      version: "operator_verification_plan_evidence_association.v1", plan_id: "plan-1",
      plan_item_ordinal: 1, evidence_id: "evidence-1",
    }, expect.stringMatching(/^web-verification-association-/u));
    const readOnlyClient = { ...client, hasVerificationEvidence: false } as APIClient;
    rerender(<QueryClientProvider client={queryClient}>
      <VerificationEvidence client={readOnlyClient} runID="run-1" />
    </QueryClientProvider>);
    expect(screen.queryByRole("button", { name: "Associate Focused tests" })).not.toBeInTheDocument();
  });
});
