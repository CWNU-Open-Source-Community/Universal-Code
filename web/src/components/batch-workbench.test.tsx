import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { APIRequestError, type APIClient } from "../api/client";
import { batchProposalFixture, batchWorkbenchFixture } from "../test/batch-workbench-fixture";
import { BatchPreparation, BatchWorkbenchControls } from "./batch-workbench";
import { BatchDeliveriesPanel } from "./run-projections";
import { V2RecoveryProvider } from "../v2/recovery-storage";

function provider(children: React.ReactNode, scopeID: string | undefined = "batch-workbench-store") {
  return <QueryClientProvider client={new QueryClient({ defaultOptions: { queries: { retry: false, gcTime: 0 } } })}>
    <V2RecoveryProvider client={{ baseURL: "http://batch-test" }} scopeID={scopeID}>{children}</V2RecoveryProvider>
  </QueryClientProvider>;
}
beforeEach(() => { sessionStorage.clear(); localStorage.clear(); });
afterEach(() => vi.restoreAllMocks());

describe("Batch workbench", () => {
  it.each(["prepare", "child"])("settles a legacy %s rejected with 409 before adjustment and never imports it again", async (kind) => {
    const suffix = kind === "prepare" ? "prepare" : "batch-1/1";
    const key = `web-batch-${kind === "prepare" ? "prepare" : "execute"}-11111111-1111-4111-8111-111111111111`;
    const original = kind === "prepare" ? { key, body: { version: "batch-delivery-workbench.v1", proposal_id: "proposal-1", confirm: true,
      tasks: [{ ordinal: 1, ownership_hints: [{ path: "internal/parser/a.go", kind: "file" }], validations: [{ id: "diff", kind: "git_diff_check", scope: "." }] }] } }
      : { key, kind: "execute", generation: 1, retry: false };
    const legacyKey = `universal-code.batch-workbench.v1:http://batch-test:run-1:${suffix}`;
    const raw = JSON.stringify(original); sessionStorage.setItem(legacyKey, raw);
    const perform = vi.fn().mockRejectedValue(new APIRequestError("generation changed", "CONFLICT", 409));
    const client = { baseURL: "http://batch-test", hasBatchDeliveryHostValidation: false,
      getRunChildTaskProposals: vi.fn().mockResolvedValue({ items: [batchProposalFixture()] }), getBatchWorkbench: vi.fn().mockResolvedValue(batchWorkbenchFixture()),
      prepareBatchWorkbench: perform, executeBatchWorkbenchChild: perform } as unknown as APIClient;
    const tree = () => provider(kind === "prepare" ? <BatchPreparation client={client} runID="run-1" usedProposalIDs={[]} onReviewProposals={vi.fn()} />
      : <BatchWorkbenchControls client={client} runID="run-1" planID="batch-1" ordinal={1} />);
    const check = kind === "prepare" ? "Check preparation result" : "Check child operation result";
    const adjust = kind === "prepare" ? "Revise preparation request" : "Continue from the current task";
    const user = userEvent.setup(); const first = render(tree());
    await user.click(await screen.findByRole("button", { name: check }));
    await screen.findByRole("button", { name: adjust });
    const journalKey = Array.from({ length: localStorage.length }, (_, index) => localStorage.key(index)!)
      .find((stored) => decodeURIComponent(stored).includes(kind === "prepare" ? "batch-workbench-prepare:" : "batch-workbench-child:"))!;
    const journal = localStorage.getItem(journalKey);
    const originalSet = Storage.prototype.setItem;
    const write = vi.spyOn(Storage.prototype, "setItem").mockImplementation(function (this: Storage, storedKey, value) {
      if (decodeURIComponent(storedKey).includes("batch-workbench-legacy-settled:")) throw new Error("quota");
      originalSet.call(this, storedKey, value);
    });
    await user.click(screen.getByRole("button", { name: adjust }));
    await screen.findAllByText(/无法保存本机恢复数据/u);
    expect(localStorage.getItem(journalKey)).toBe(journal);
    expect(screen.getByRole("button", { name: check })).toBeInTheDocument();
    write.mockRestore();
    await user.click(screen.getByRole("button", { name: adjust }));
    await waitFor(() => expect(screen.queryByRole("button", { name: check })).not.toBeInTheDocument());
    first.unmount(); render(tree());
    if (kind === "prepare") await screen.findByLabelText("Owned paths (one per line)");
    else await screen.findByRole("button", { name: "Start child and return delivery" });
    expect(screen.queryByRole("button", { name: check })).not.toBeInTheDocument();
    expect(localStorage.getItem(journalKey)).toBeNull();
    expect(perform).toHaveBeenCalledOnce(); expect(sessionStorage.getItem(legacyKey)).toBe(raw);
  });

  it("migrates the exact legacy preparation after current Run confirmation without deleting or automatically replaying it", async () => {
    const key = "web-batch-prepare-11111111-1111-4111-8111-111111111111";
    const body = { version: "batch-delivery-workbench.v1", proposal_id: "proposal-1", confirm: true,
      tasks: [{ ordinal: 1, ownership_hints: [{ path: "internal/parser/a.go", kind: "file" }], validations: [{ id: "diff", kind: "git_diff_check", scope: "." }] }] };
    const legacyKey = "universal-code.batch-workbench.v1:http://batch-test:run-1:prepare";
    const raw = JSON.stringify({ key, body }); sessionStorage.setItem(legacyKey, raw);
    const prepare = vi.fn().mockResolvedValue(batchWorkbenchFixture());
    const client = { baseURL: "http://batch-test", hasBatchDeliveryHostValidation: false,
      getRunChildTaskProposals: vi.fn().mockResolvedValue({ items: [batchProposalFixture()] }), prepareBatchWorkbench: prepare } as unknown as APIClient;
    const form = () => provider(<BatchPreparation client={client} runID="run-1" usedProposalIDs={[]} onReviewProposals={vi.fn()} />);
    const user = userEvent.setup(); const first = render(form());
    await screen.findByRole("button", { name: "Check preparation result" }); expect(prepare).not.toHaveBeenCalled();
    expect(sessionStorage.getItem(legacyKey)).toBe(raw); first.unmount();
    const second = render(form()); await user.click(await screen.findByRole("button", { name: "Check preparation result" }));
    await waitFor(() => expect(prepare).toHaveBeenCalledWith("run-1", body, key));
    await waitFor(() => expect(screen.queryByRole("button", { name: "Check preparation result" })).not.toBeInTheDocument()); second.unmount();
    render(form()); await screen.findByLabelText("Owned paths (one per line)");
    expect(screen.queryByRole("button", { name: "Check preparation result" })).not.toBeInTheDocument();
    expect(prepare).toHaveBeenCalledOnce(); expect(sessionStorage.getItem(legacyKey)).toBe(raw);
  });

  it("migrates only the current endpoint/Run/plan/child legacy operation and replays its exact original generation and key", async () => {
    const legacyKey = "universal-code.batch-workbench.v1:http://batch-test:run-1:batch-1/1";
    const original = { key: "web-batch-execute-11111111-1111-4111-8111-111111111111", kind: "execute", generation: 1, retry: false };
    const raw = JSON.stringify(original); sessionStorage.setItem(legacyKey, raw);
    const execute = vi.fn().mockResolvedValue(batchWorkbenchFixture("ready_for_review"));
    const client = { baseURL: "http://batch-test", getBatchWorkbench: vi.fn().mockResolvedValue(batchWorkbenchFixture()), executeBatchWorkbenchChild: execute } as unknown as APIClient;
    const user = userEvent.setup(); render(provider(<BatchWorkbenchControls client={client} runID="run-1" planID="batch-1" ordinal={1} />));
    const check = await screen.findByRole("button", { name: "Check child operation result" }); expect(execute).not.toHaveBeenCalled();
    await user.click(check); await waitFor(() => expect(execute).toHaveBeenCalledWith("run-1", "batch-1", 1,
      { version: "batch-delivery-workbench.v1", confirm: true, expected_generation: 1 }, original.key));
    expect(sessionStorage.getItem(legacyKey)).toBe(raw);
  });

  it("does not import or execute a legacy operation when the current home cannot read that Run and plan", async () => {
    const legacyKey = "universal-code.batch-workbench.v1:http://batch-test:run-1:batch-1/1";
    const raw = JSON.stringify({ key: "web-batch-execute-11111111-1111-4111-8111-111111111111", kind: "execute", generation: 1, retry: false });
    sessionStorage.setItem(legacyKey, raw);
    const client = { baseURL: "http://batch-test", getBatchWorkbench: vi.fn().mockRejectedValue(new Error("Run not found in current home")), executeBatchWorkbenchChild: vi.fn() } as unknown as APIClient;
    render(provider(<BatchWorkbenchControls client={client} runID="run-1" planID="batch-1" ordinal={1} />, "new-home"));
    await screen.findByRole("alert"); expect(client.executeBatchWorkbenchChild).not.toHaveBeenCalled();
    expect(screen.queryByRole("button", { name: "Check child operation result" })).not.toBeInTheDocument();
    expect(localStorage.length).toBe(0); expect(sessionStorage.getItem(legacyKey)).toBe(raw);
  });

  it("blocks new operations when legacy migration cannot be saved and permits only a manual save retry", async () => {
    const legacyKey = "universal-code.batch-workbench.v1:http://batch-test:run-1:batch-1/1";
    const raw = JSON.stringify({ key: "web-batch-execute-11111111-1111-4111-8111-111111111111", kind: "execute", generation: 1, retry: false });
    sessionStorage.setItem(legacyKey, raw);
    const write = vi.spyOn(Storage.prototype, "setItem").mockImplementation(() => { throw new Error("quota"); });
    const execute = vi.fn(); const client = { baseURL: "http://batch-test", getBatchWorkbench: vi.fn().mockResolvedValue(batchWorkbenchFixture()), executeBatchWorkbenchChild: execute } as unknown as APIClient;
    render(provider(<BatchWorkbenchControls client={client} runID="run-1" planID="batch-1" ordinal={1} />));
    await screen.findAllByText(/无法保存本机恢复数据/); expect(execute).not.toHaveBeenCalled();
    expect(screen.queryByRole("button", { name: "Start child and return delivery" })).not.toBeInTheDocument();
    write.mockRestore(); await userEvent.setup().click(screen.getByRole("button", { name: "Retry saving original request" }));
    await screen.findByRole("button", { name: "Check child operation result" }); expect(execute).not.toHaveBeenCalled();
    expect(sessionStorage.getItem(legacyKey)).toBe(raw);
  });

  it("restores unsubmitted owned paths in the same store and Run, while requiring a new confirmation", async () => {
    const client = { baseURL: "http://batch-test", hasBatchDeliveryHostValidation: true,
      getRunChildTaskProposals: vi.fn().mockResolvedValue({ items: [batchProposalFixture()] }), prepareBatchWorkbench: vi.fn() } as unknown as APIClient;
    const form = (scope = "batch-workbench-store", runID = "run-1") => provider(<BatchPreparation client={client} runID={runID}
      usedProposalIDs={[]} onReviewProposals={vi.fn()} />, scope);
    const user = userEvent.setup(); const first = render(form());
    await user.type(await screen.findByLabelText("Owned paths (one per line)"), "internal/parser/a.go");
    await user.click(screen.getByText("Add Go/npm validation")); await user.type(screen.getByLabelText("Go validation scope"), "internal/parser");
    await user.click(screen.getByLabelText(/I reviewed the tasks/)); first.unmount();
    const otherStore = render(form("other-store")); expect(await screen.findByLabelText("Owned paths (one per line)")).toHaveValue(""); otherStore.unmount();
    const otherRun = render(form("batch-workbench-store", "run-2")); expect(await screen.findByLabelText("Owned paths (one per line)")).toHaveValue(""); otherRun.unmount();
    render(form()); expect(await screen.findByLabelText("Owned paths (one per line)")).toHaveValue("internal/parser/a.go");
    expect(screen.getByLabelText("Go validation scope")).toHaveValue("internal/parser");
    expect(screen.getByLabelText(/I reviewed the tasks/)).not.toBeChecked();
    expect(screen.getByRole("button", { name: "Prepare isolated tasks" })).toBeDisabled(); expect(client.prepareBatchWorkbench).not.toHaveBeenCalled();
  });

  it("keeps an interrupted pending child request across a new provider/cache and clears only after its original request succeeds", async () => {
    const execute = vi.fn().mockImplementationOnce(() => new Promise(() => {})).mockResolvedValueOnce(batchWorkbenchFixture("ready_for_review"));
    const client = { baseURL: "http://batch-test", getBatchWorkbench: vi.fn().mockResolvedValue(batchWorkbenchFixture()), executeBatchWorkbenchChild: execute } as unknown as APIClient;
    const controls = (scope = "batch-workbench-store", runID = "run-1") => provider(<BatchWorkbenchControls client={client} runID={runID} planID="batch-1" ordinal={1} />, scope);
    const user = userEvent.setup(); const first = render(controls());
    await user.click(await screen.findByRole("button", { name: "Start child and return delivery" }));
    await waitFor(() => expect(execute).toHaveBeenCalledOnce()); const original = execute.mock.calls[0]; first.unmount();
    const otherStore = render(controls("other-store")); await screen.findByRole("button", { name: "Start child and return delivery" });
    expect(screen.queryByRole("button", { name: "Check child operation result" })).not.toBeInTheDocument(); otherStore.unmount();
    const otherRun = render(controls("batch-workbench-store", "run-2")); await screen.findByRole("button", { name: "Start child and return delivery" });
    expect(screen.queryByRole("button", { name: "Check child operation result" })).not.toBeInTheDocument(); otherRun.unmount();
    render(controls()); const check = await screen.findByRole("button", { name: "Check child operation result" }); expect(execute).toHaveBeenCalledOnce();
    await user.click(check); await waitFor(() => expect(execute).toHaveBeenCalledTimes(2)); expect(execute.mock.calls[1]).toEqual(original);
    await waitFor(() => expect(localStorage.length).toBe(0));
  });

  it("retains an approval-waiting operation and resumes its original identity after remount", async () => {
    const waiting = batchWorkbenchFixture();
    waiting.children[0].waiting_approval = true;
    const execute = vi.fn().mockResolvedValueOnce(waiting).mockResolvedValueOnce(batchWorkbenchFixture("ready_for_review"));
    const getWorkbench = vi.fn().mockResolvedValue(batchWorkbenchFixture());
    const client = { baseURL: "http://batch-test", getBatchWorkbench: getWorkbench, executeBatchWorkbenchChild: execute } as unknown as APIClient;
    const controls = () => provider(<BatchWorkbenchControls client={client} runID="run-1" planID="batch-1" ordinal={1} />);
    const user = userEvent.setup();
    const first = render(controls());
    await user.click(await screen.findByRole("button", { name: "Start child and return delivery" }));
    await screen.findByRole("button", { name: "Continue child after approval" });
    expect(execute).toHaveBeenCalledOnce();
    expect(localStorage.length).toBe(1);
    const original = execute.mock.calls[0];
    first.unmount();
    getWorkbench.mockResolvedValue(waiting);
    render(controls());
    const resume = await screen.findByRole("button", { name: "Continue child after approval" });
    expect(execute).toHaveBeenCalledOnce();
    await user.click(resume);
    await waitFor(() => expect(execute).toHaveBeenCalledTimes(2));
    expect(execute.mock.calls[1]).toEqual(original);
    await waitFor(() => expect(localStorage.length).toBe(0));
  });

  it("prevents child execution when saving its original identity fails", async () => {
    const execute = vi.fn(); const client = { baseURL: "http://batch-test", getBatchWorkbench: vi.fn().mockResolvedValue(batchWorkbenchFixture()), executeBatchWorkbenchChild: execute } as unknown as APIClient;
    render(provider(<BatchWorkbenchControls client={client} runID="run-1" planID="batch-1" ordinal={1} />));
    const start = await screen.findByRole("button", { name: "Start child and return delivery" });
    vi.spyOn(Storage.prototype, "setItem").mockImplementation(() => { throw new Error("quota"); });
    await userEvent.setup().click(start); expect(execute).not.toHaveBeenCalled();
    expect(screen.getAllByText(/无法保存本机恢复数据/).length).toBeGreaterThan(0);
  });

  it("retains the original operation when success cleanup fails and exposes unavailable persistence without an identity", async () => {
    const execute = vi.fn().mockResolvedValue(batchWorkbenchFixture());
    const client = { baseURL: "http://batch-test", getBatchWorkbench: vi.fn().mockResolvedValue(batchWorkbenchFixture()), executeBatchWorkbenchChild: execute } as unknown as APIClient;
    const user = userEvent.setup(); const first = render(provider(<BatchWorkbenchControls client={client} runID="run-1" planID="batch-1" ordinal={1} />));
    const start = await screen.findByRole("button", { name: "Start child and return delivery" });
    vi.spyOn(Storage.prototype, "removeItem").mockImplementation(() => { throw new Error("storage denied"); });
    await user.click(start); await screen.findByRole("button", { name: "Check child operation result" });
    await waitFor(() => expect(screen.getAllByText(/无法移除本机恢复记录/).length).toBeGreaterThan(0));
    expect(screen.queryByRole("button", { name: "Start child and return delivery" })).not.toBeInTheDocument();
    expect(localStorage.length).toBe(1); first.unmount();
    render(<QueryClientProvider client={new QueryClient()}><BatchWorkbenchControls client={client} runID="run-1" planID="batch-1" ordinal={1} /></QueryClientProvider>);
    await user.click(await screen.findByRole("button", { name: "Start child and return delivery" }));
    expect(execute).toHaveBeenCalledOnce(); expect(screen.getAllByText(/无法持久保存验证草稿或原请求/).length).toBeGreaterThan(0);
  });

  it("requires owned paths and confirmation, retains the original preparation after remount, and never executes on preparation", async () => {
    const prepare = vi.fn().mockRejectedValueOnce(new Error("Preparation response interrupted"))
      .mockResolvedValue(batchWorkbenchFixture());
    const execute = vi.fn();
    const client = { baseURL: "http://batch-test", hasBatchDeliveryHostValidation: false,
      getRunChildTaskProposals: vi.fn().mockResolvedValue({ items: [batchProposalFixture()] }),
      prepareBatchWorkbench: prepare, executeBatchWorkbenchChild: execute } as unknown as APIClient;
    const user = userEvent.setup();
    const first = render(provider(<BatchPreparation client={client} runID="run-1" usedProposalIDs={[]} onReviewProposals={vi.fn()} />));
    const start = await screen.findByRole("button", { name: "Prepare isolated tasks" });
    expect(start).toBeDisabled();
    await user.type(screen.getByLabelText("Owned paths (one per line)"), "internal/parser/a.go");
    expect(start).toBeDisabled();
    await user.click(screen.getByLabelText(/I reviewed the tasks/));
    await user.click(start);
    await screen.findByRole("alert");
    expect(prepare).toHaveBeenCalledTimes(1);
    const original = prepare.mock.calls[0];
    expect(original[1]).toEqual({ version: "batch-delivery-workbench.v1", proposal_id: "proposal-1", confirm: true,
      tasks: [{ ordinal: 1, ownership_hints: [{ path: "internal/parser/a.go", kind: "file" }],
        validations: [{ id: "diff", kind: "git_diff_check", scope: "." }] }] });
    first.unmount();
    render(provider(<BatchPreparation client={client} runID="run-1" usedProposalIDs={[]} onReviewProposals={vi.fn()} />));
    expect(screen.queryByLabelText("Owned paths (one per line)")).not.toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: "Check preparation result" }));
    await waitFor(() => expect(prepare).toHaveBeenCalledTimes(2));
    expect(prepare.mock.calls[1]).toEqual(original);
    expect(execute).not.toHaveBeenCalled();
  });

  it("prepares feedback rework for the exact generation and waits for a separate start", async () => {
    const current = batchWorkbenchFixture("changes_requested", 2);
    const next = batchWorkbenchFixture("question", 3);
    const recover = vi.fn().mockResolvedValue(next);
    const execute = vi.fn().mockResolvedValue(batchWorkbenchFixture("ready_for_review", 3));
    const client = { baseURL: "http://batch-test", getBatchWorkbench: vi.fn().mockResolvedValue(current),
      recoverBatchWorkbenchOwner: recover, executeBatchWorkbenchChild: execute } as unknown as APIClient;
    const user = userEvent.setup();
    render(provider(<BatchWorkbenchControls client={client} runID="run-1" planID="batch-1" ordinal={1} />));
    await user.click(await screen.findByRole("button", { name: "Prepare rework from review feedback" }));
    const start = await screen.findByRole("button", { name: "Start child and return delivery" });
    expect(recover).toHaveBeenCalledWith("run-1", "batch-1", 1,
      { version: "batch-delivery-workbench.v1", confirm: true, retry: true, expected_generation: 2 }, expect.stringMatching(/^web-batch-owner-/));
    expect(execute).not.toHaveBeenCalled();
    await user.click(start);
    await waitFor(() => expect(execute).toHaveBeenCalledWith("run-1", "batch-1", 1,
      { version: "batch-delivery-workbench.v1", confirm: true, expected_generation: 3 }, expect.stringMatching(/^web-batch-execute-/)));
  });

  it("retains an interrupted execution identity and makes refresh read only", async () => {
    const current = batchWorkbenchFixture();
    const unresolved = structuredClone(current);
    unresolved.children[0].outcome_unresolved = true;
    const execute = vi.fn().mockRejectedValueOnce(new Error("Worker result interrupted"))
      .mockResolvedValue({ ...unresolved, replayed: true });
    const get = vi.fn().mockResolvedValue(current);
    const client = { baseURL: "http://batch-test", getBatchWorkbench: get, executeBatchWorkbenchChild: execute,
      recoverBatchWorkbenchOwner: vi.fn() } as unknown as APIClient;
    const user = userEvent.setup();
    const first = render(provider(<BatchWorkbenchControls client={client} runID="run-1" planID="batch-1" ordinal={1} />));
    await user.click(await screen.findByRole("button", { name: "Start child and return delivery" }));
    await screen.findByRole("alert");
    const original = execute.mock.calls[0];
    first.unmount();
    get.mockResolvedValue(unresolved);
    render(provider(<BatchWorkbenchControls client={client} runID="run-1" planID="batch-1" ordinal={1} />));
    await user.click(await screen.findByRole("button", { name: "Check child operation result" }));
    await screen.findByRole("button", { name: "Recover child ownership" });
    expect(execute.mock.calls[1]).toEqual(original);
    await user.click(screen.getByRole("button", { name: "Refresh child progress" }));
    expect(execute).toHaveBeenCalledTimes(2);
    expect(client.recoverBatchWorkbenchOwner).not.toHaveBeenCalled();
    expect(screen.queryByRole("button", { name: "Start child and return delivery" })).not.toBeInTheDocument();
  });

  it("blocks execution while a dependency remains unaccepted", async () => {
    const current = batchWorkbenchFixture();
    current.snapshot.plan.spec.tasks[0].dependency_ordinals = [2];
    const client = { baseURL: "http://batch-test", getBatchWorkbench: vi.fn().mockResolvedValue(current),
      executeBatchWorkbenchChild: vi.fn() } as unknown as APIClient;
    render(provider(<BatchWorkbenchControls client={client} runID="run-1" planID="batch-1" ordinal={1} />));
    expect(await screen.findByRole("button", { name: "Start child and return delivery" })).toBeDisabled();
    expect(screen.getByText("Accept the dependency task before starting this task.")).toBeInTheDocument();
    expect(client.executeBatchWorkbenchChild).not.toHaveBeenCalled();
  });

  it("refreshes the feedback workflow after an independent changes-requested review", async () => {
    const current = batchWorkbenchFixture("ready_for_review");
    current.snapshot.children[0].receipt = { id: "receipt-1", ordinal: 1, generation: 1,
      protocol_version: "batch-delivery-receipt.v1", base_commit: "a".repeat(40), head_commit: "b".repeat(40),
      diff_sha256: "c".repeat(64), call_chain_sha256: "d".repeat(64), diff_bytes: 128,
      diff_stat: "1 file changed", changed_files: ["internal/parser/a.go"], test_receipts: [],
      evidence_refs: [], limitations: [], created_at: "2026-10-10T00:00:00Z" };
    const next = structuredClone(current); next.snapshot.children[0].workspace.status = "changes_requested";
    let reviewed = false;
    const client = { baseURL: "http://batch-test", hasBatchDeliveryControl: true, hasBatchDeliveryHostValidation: false,
      getRunBatchDeliveries: vi.fn().mockResolvedValue({ items: [current.snapshot.plan] }),
      getRunBatchDelivery: vi.fn().mockImplementation(() => Promise.resolve(reviewed ? next.snapshot : current.snapshot)),
      getBatchWorkbench: vi.fn().mockImplementation(() => Promise.resolve(reviewed ? next : current)),
      reviewRunBatchDeliveryChild: vi.fn().mockImplementation(() => { reviewed = true; return Promise.resolve({}); }),
    } as unknown as APIClient;
    const user = userEvent.setup();
    render(provider(<BatchDeliveriesPanel client={client} runID="run-1" />));
    await user.type(await screen.findByLabelText("Independent review summary"), "Add the bounded regression example");
    await user.click(screen.getByLabelText(/I independently reviewed/));
    await user.click(screen.getByRole("button", { name: "Request changes" }));
    expect(await screen.findByRole("button", { name: "Prepare rework from review feedback" })).toBeInTheDocument();
    expect(client.reviewRunBatchDeliveryChild).toHaveBeenCalledWith("run-1", "batch-1", 1,
      expect.objectContaining({ generation: 1, summary: "Add the bounded regression example", verdict: "changes_requested" }), expect.any(String));
  });

  it("keeps independent review confirmation bound to one child receipt", async () => {
    const current = batchWorkbenchFixture("ready_for_review");
    current.snapshot.children[0].receipt = { id: "receipt-1", ordinal: 1, generation: 1,
      protocol_version: "batch-delivery-receipt.v1", base_commit: "a".repeat(40), head_commit: "b".repeat(40),
      diff_sha256: "c".repeat(64), call_chain_sha256: "d".repeat(64), diff_bytes: 128,
      diff_stat: "1 file changed", changed_files: ["internal/parser/a.go"], test_receipts: [],
      evidence_refs: [], limitations: [], created_at: "2026-10-10T00:00:00Z" };
    const second = structuredClone(current.snapshot.children[0]);
    second.workspace.ordinal = 2; second.workspace.agent_id = "agent-child-2";
    second.receipt!.id = "receipt-2"; second.receipt!.ordinal = 2;
    current.snapshot.children.push(second);
    const client = { baseURL: "http://batch-test", hasBatchDeliveryControl: true,
      getRunBatchDeliveries: vi.fn().mockResolvedValue({ items: [current.snapshot.plan] }),
      getRunBatchDelivery: vi.fn().mockResolvedValue(current.snapshot),
    } as unknown as APIClient;
    const user = userEvent.setup();
    render(provider(<BatchDeliveriesPanel client={client} runID="run-1" />));
    const summaries = await screen.findAllByLabelText("Independent review summary");
    await user.type(summaries[0], "Independently checked child one");
    await user.click(screen.getAllByLabelText(/I independently reviewed/)[0]);
    const accept = screen.getAllByRole("button", { name: "Accept" });
    expect(accept[0]).toBeEnabled(); expect(accept[1]).toBeDisabled();
  });
});
