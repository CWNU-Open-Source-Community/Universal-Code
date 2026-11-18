import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act, render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import type { APIClient } from "../api/client";
import type { FileEditPreviewView } from "../api/types";
import { FileEditPanel } from "./file-edit-panel";

describe("FileEditPanel", () => {
  it.each(["failed", "completed", "queued", "not_started"] as const)(
    "separates saved approval from %s continuation and refreshes the actual file outcome", async (state) => {
      const proposed = editFixture("edit-continuation", "proposed");
      const applied = { ...proposed, status: "applied", allowed_actions: [], apply_enabled: false } as const;
      const queue = vi.fn().mockResolvedValueOnce({ items: [proposed], truncated: false, apply_enabled: false })
        .mockResolvedValue({ items: [applied], truncated: false, apply_enabled: false });
      const review = vi.fn().mockResolvedValue({ protocol_version: "file_edit_review.v1", run_id: "run-1",
        action: "approve_intent", edit: { ...proposed, status: "approved", allowed_actions: [] },
        file_written: false, replayed: false, continuation: { state, replayed: false,
          model_called: state === "completed" || state === "failed", tool_called: state === "completed" } });
      const client = { hasFileEditReview: true, hasFileEditApply: true, fileEditQueue: queue,
        fileEditChangeSet: vi.fn().mockResolvedValue(changeSetFor(proposed)), reviewFileEdit: review,
        applyFileEdit: vi.fn() } as unknown as APIClient;
      renderPanel(client);
      const user = userEvent.setup();
      await user.click(await screen.findByRole("button", { name: /README.md/ }));
      await user.click(screen.getByRole("button", { name: "Approve and apply README.md" }));
      await waitFor(() => expect(queue.mock.calls.length).toBeGreaterThanOrEqual(2));
      expect(await screen.findByText({
        failed: "Review saved, but subsequent execution failed. Check the records and continue in this conversation.",
        completed: "Review saved. The subsequent turn has a recorded outcome; see execution records for file changes and command results.",
        queued: "Review saved and continuation queued. See the conversation records for current progress.",
        not_started: "Review saved. Send a new message to continue in this conversation.",
      }[state])).toBeInTheDocument();
      expect(screen.getByRole("button", { name: /README.md.*applied/ })).toBeInTheDocument();
      expect(screen.queryByRole("button", { name: "Approve and apply README.md" })).not.toBeInTheDocument();
      expect(client.applyFileEdit).not.toHaveBeenCalled();
    });
  it("keeps same-path source history read-only beside the current execution target", async () => {
    const historical = { ...editFixture("source-history", "applied"), workspace_id: "workspace-source" };
    const current = { ...editFixture("target-current", "approved"), workspace_id: "workspace-target", apply_enabled: true };
    const summary = { ...changeSetFor(current), workspace_id: current.workspace_id };
    const client = { hasFileEditReview: true, hasFileEditApply: true,
      fileEditQueue: vi.fn().mockResolvedValue({ items: [historical, current], truncated: false, apply_enabled: true }),
      fileEditChangeSet: vi.fn().mockResolvedValue(summary), applyFileEdit: vi.fn(), createFileEditRevertProposal: vi.fn(),
    } as unknown as APIClient;
    renderPanel(client);
    const user = userEvent.setup();
    await user.click(await screen.findByRole("button", { name: /README.md.*Historical directory/ }));
    expect(screen.getByRole("button", { name: "Preview revert of this edit" })).toBeDisabled();
    expect(screen.getByText("workspace-source")).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Apply README.md" })).not.toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: /README.md.*Current execution directory/ }));
    expect(screen.getByRole("button", { name: "Apply README.md" })).toBeEnabled();
    expect(client.applyFileEdit).not.toHaveBeenCalled();
    expect(client.createFileEditRevertProposal).not.toHaveBeenCalled();
  });

  it("keeps historical diffs available when current-target summary fails", async () => {
    const edit = { ...editFixture("history-without-target", "applied"), workspace_id: "workspace-original" };
    const client = { hasFileEditReview: true,
      fileEditQueue: vi.fn().mockResolvedValue({ items: [edit], truncated: false, apply_enabled: false }),
      fileEditChangeSet: vi.fn().mockRejectedValue(new Error("exact target unavailable")),
    } as unknown as APIClient;
    renderPanel(client);
    expect(await screen.findByRole("button", { name: "Retry change summary" })).toBeEnabled();
    await userEvent.setup().click(screen.getByRole("button", { name: /README.md/ }));
    expect(screen.getByText("new")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Preview revert of this edit" })).toBeDisabled();
    expect(screen.getByText("workspace-original")).toBeInTheDocument();
  });

  it("renders the bounded Diff and approves intent without an apply operation", async () => {
    const user = userEvent.setup();
    const edit = {
      id: "edit-1", session_id: "session-1", workspace_id: "workspace-1",
      path: "README.md", operation: "replace", status: "proposed",
      diff: "--- README.md\n+++ README.md\n-old\n+new",
      original_hash: "a".repeat(64), proposed_hash: "b".repeat(64),
      secrets_redacted: true, allowed_actions: ["approve_intent", "deny"],
      created_at: "2026-07-18T00:00:00Z", updated_at: "2026-07-18T00:00:00Z",
      apply_enabled: false,
    } as const;
    const reviewFileEdit = vi.fn().mockResolvedValue({
      protocol_version: "file_edit_review.v1", run_id: "run-1",
      action: "approve_intent", edit: { ...edit, status: "approved", allowed_actions: [] },
      replayed: false, file_written: false,
    });
    const client = {
      hasFileEditReview: true,
      fileEditQueue: vi.fn().mockResolvedValue({
        protocol_version: "file_edit_review.v1", run_id: "run-1", items: [edit],
        truncated: false, apply_enabled: false,
      }),
      fileEditChangeSet: vi.fn().mockResolvedValue(changeSetFor(edit)),
      reviewFileEdit,
    } as unknown as APIClient;

    renderPanel(client);
    await user.click(await screen.findByRole("button", { name: /README\.md/ }));
    expect(await screen.findByText("Apply authority: disabled")).toBeInTheDocument();
    expect(screen.getByRole("complementary", { name: "Review README.md" })).toBeInTheDocument();
    expect(screen.getByText("new")).toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: "Approve and apply README.md" }));
    await waitFor(() => expect(reviewFileEdit).toHaveBeenCalledTimes(1));
    expect(reviewFileEdit.mock.calls[0]?.slice(0, 3)).toEqual([
      "run-1", "edit-1", { version: "file_edit_review.v1", action: "approve_intent" },
    ]);
  });

  it("applies only an approved edit with a memory-only retry key", async () => {
    const user = userEvent.setup();
    const edit = {
      id: "edit-approved", session_id: "session-1", workspace_id: "workspace-1",
      path: "safe.txt", operation: "replace", status: "approved",
      diff: "--- safe.txt\n+++ safe.txt\n+value",
      original_hash: "a".repeat(64), proposed_hash: "b".repeat(64),
      secrets_redacted: false, allowed_actions: [], apply_enabled: true,
      created_at: "2026-07-18T00:00:00Z", updated_at: "2026-07-18T00:00:00Z",
    } as const;
    const applyFileEdit = vi.fn().mockResolvedValue({
      protocol_version: "file_edit_apply.v1", run_id: "run-1", status: "applied",
      edit: { ...edit, status: "applied", apply_enabled: false },
      replayed: false, file_written: true, policy_rechecked: true,
      receipt: { protocol_version: "operation_receipt.v1", kind: "file_edit_apply",
        outcome: "applied", durable: true, replayed: false, retry_safe: true,
        retry_strategy: "same_operation_key", recovery_action: "none",
        cleanup_state: "complete" },
    });
    const client = {
      hasFileEditReview: true, hasFileEditApply: true,
      fileEditQueue: vi.fn().mockResolvedValue({
        protocol_version: "file_edit_review.v1", run_id: "run-1", items: [edit],
        truncated: false, apply_enabled: true,
      }),
      fileEditChangeSet: vi.fn().mockResolvedValue(changeSetFor(edit)),
      reviewFileEdit: vi.fn(), applyFileEdit,
    } as unknown as APIClient;
    renderPanel(client);
    await user.click(await screen.findByRole("button", { name: /safe\.txt/ }));
    await user.click(await screen.findByRole("button", { name: "Apply safe.txt" }));
    await waitFor(() => expect(applyFileEdit).toHaveBeenCalledTimes(1));
    expect(applyFileEdit.mock.calls[0]?.slice(0, 3)).toEqual([
      "run-1", "edit-approved", { version: "file_edit_apply.v1" },
    ]);
    expect(applyFileEdit.mock.calls[0]?.[3]).toMatch(/^web-file-apply-/);
    expect(await screen.findByText("file edit apply")).toBeInTheDocument();
  });

  it("keeps mixed multi-file outcomes visible without a batch mutation", async () => {
    const applied = {
      id: "edit-applied", session_id: "session-1", workspace_id: "workspace-1",
      path: "applied.txt", operation: "create", status: "applied", diff: "+applied",
      original_hash: "missing",
      proposed_hash: "a".repeat(64), secrets_redacted: false, allowed_actions: [],
      apply_enabled: false, created_at: "2026-07-18T00:00:00Z",
      updated_at: "2026-07-18T00:00:00Z",
    } as const;
    const failed = { ...applied, id: "edit-failed", path: "failed.txt", status: "failed",
      diff: "+failed", proposed_hash: "b".repeat(64) } as const;
    const client = {
      hasFileEditReview: true, hasFileEditApply: true,
      fileEditQueue: vi.fn().mockResolvedValue({
        protocol_version: "file_edit_review.v1", run_id: "run-1",
        items: [applied, failed], truncated: false, apply_enabled: true,
      }),
      fileEditChangeSet: vi.fn().mockResolvedValue({
        protocol_version: "file_edit_change_set.v1", run_id: "run-1",
        session_id: "session-1", workspace_id: "workspace-1",
        items: [changeSetItem(applied), changeSetItem(failed)], proposed_count: 0,
        approved_count: 0, applied_count: 1, denied_count: 0, failed_count: 1,
        returned_count: 2, total_diff_bytes: 15, truncated: false,
        review_independent: true, apply_independent: true, atomic_apply: false,
        batch_mutation_supported: false, partial_apply_visible: true,
        diff_content_included: false,
      }),
      reviewFileEdit: vi.fn(), applyFileEdit: vi.fn(),
    } as unknown as APIClient;

    renderPanel(client);
    expect(await screen.findByText("partial")).toBeInTheDocument();
    expect(screen.getByText("applied.txt")).toBeInTheDocument();
    expect(screen.getByText("failed.txt")).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: /apply all/i })).not.toBeInTheDocument();
  });

  it("opens and closes a structured review sidebar from the file index", async () => {
    const user = userEvent.setup();
    const edit = {
      id: "edit-review", session_id: "session-1", workspace_id: "workspace-1",
      path: "src/main.go", operation: "replace", status: "proposed",
      diff: "--- a/src/main.go\n+++ b/src/main.go\n@@ -2 +2 @@\n-old\n+new",
      original_hash: "a".repeat(64), proposed_hash: "b".repeat(64),
      secrets_redacted: false, allowed_actions: ["approve_intent", "deny"],
      apply_enabled: false, created_at: "2026-07-18T00:00:00Z",
      updated_at: "2026-07-18T00:00:00Z",
    } as const;
    const client = {
      hasFileEditReview: true,
      fileEditQueue: vi.fn().mockResolvedValue({
        protocol_version: "file_edit_review.v1", run_id: "run-1", items: [edit],
        truncated: false, apply_enabled: false,
      }),
      fileEditChangeSet: vi.fn().mockResolvedValue(changeSetFor(edit)),
      reviewFileEdit: vi.fn(),
    } as unknown as APIClient;

    renderPanel(client);
    expect(screen.queryByRole("complementary")).not.toBeInTheDocument();
    await user.click(await screen.findByRole("button", { name: /src\/main\.go/ }));
    expect(screen.getByRole("complementary", { name: "Review src/main.go" })).toBeInTheDocument();
    expect(screen.getByText("@@ -2 +2 @@")).toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: "Close review" }));
    expect(screen.queryByRole("complementary")).not.toBeInTheDocument();
  });

  it("opens the inverse diff for review and follows denial, approval, and application states", async () => {
    const source = editFixture("source", "applied");
    let inverse = { ...editFixture("inverse", "proposed"), diff: "--- README.md\n+++ README.md\n-new\n+old" };
    let items = [source];
    const createFileEditRevertProposal = vi.fn(async () => {
      inverse = { ...inverse, status: "proposed", allowed_actions: ["approve_intent", "deny"], apply_enabled: false };
      items = [source, inverse];
      return { edit: inverse, file_written: false };
    });
    const reviewFileEdit = vi.fn(async (_runID: string, _editID: string, body: { action: string }) => {
      inverse = { ...inverse, status: body.action === "approve_intent" ? "approved" : "denied",
        allowed_actions: [], apply_enabled: body.action === "approve_intent" };
      items = [source, inverse];
      return { edit: inverse };
    });
    const applyFileEdit = vi.fn(async () => {
      inverse = { ...inverse, status: "applied", apply_enabled: false };
      items = [source, inverse];
      return { edit: inverse, status: "applied", receipt: {
        protocol_version: "operation_receipt.v1", kind: "file_edit_apply", outcome: "applied",
        durable: true, replayed: false, retry_safe: true, cleanup_state: "complete",
        retry_strategy: "same_operation_key", recovery_action: "none",
      } };
    });
    const client = { hasFileEditReview: true, hasFileEditApply: true, createFileEditRevertProposal, reviewFileEdit,
      applyFileEdit, fileEdit: vi.fn(async () => inverse),
      fileEditQueue: vi.fn(async () => ({ items, apply_enabled: true })),
      fileEditChangeSet: vi.fn().mockResolvedValue(changeSetFor(source)) } as unknown as APIClient;
    const user = userEvent.setup();
    const onRequestRevert = vi.fn();
    renderPanel(client, "running", undefined, { onRequestRevert });
    await user.click(await screen.findByRole("button", { name: /README.md/ }));
    await user.click(screen.getByRole("button", { name: "Preview revert of this edit" }));
    expect(await screen.findByText(/This revert diff is awaiting review/)).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Approve and apply README.md" })).toBeInTheDocument();
    expect(reviewFileEdit).not.toHaveBeenCalled();
    expect(client.applyFileEdit).not.toHaveBeenCalled();
    expect(onRequestRevert).not.toHaveBeenCalled();
    expect(screen.queryByText(/File application is confirmed complete/)).not.toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: /README.md.*applied/ }));
    await user.click(screen.getByRole("button", { name: "View revert proposal" }));
    expect(createFileEditRevertProposal).toHaveBeenCalledTimes(1);
    await user.click(screen.getByRole("button", { name: "Deny README.md" }));
    await waitFor(() => expect(reviewFileEdit).toHaveBeenCalledTimes(1));
    expect(await screen.findByText(/The revert proposal was denied/)).toBeInTheDocument();
    expect(screen.queryByText(/This revert diff is awaiting review/)).not.toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Approve and apply README.md" })).not.toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Apply README.md" })).not.toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: /README.md.*applied/ }));
    await user.click(screen.getByRole("button", { name: "Preview revert of this edit" }));
    await waitFor(() => expect(createFileEditRevertProposal).toHaveBeenCalledTimes(2));
    expect(createFileEditRevertProposal.mock.calls[1]).not.toEqual(createFileEditRevertProposal.mock.calls[0]);
    await user.click(await screen.findByRole("button", { name: "Approve and apply README.md" }));
    expect(await screen.findByText(/The revert proposal is approved/)).toBeInTheDocument();
    expect(applyFileEdit).not.toHaveBeenCalled();
    await user.click(await screen.findByRole("button", { name: "Apply README.md" }));
    expect(await screen.findByText(/The revert changes were applied/)).toBeInTheDocument();
    expect(screen.queryByText(/awaiting review|awaits application/)).not.toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Approve and apply README.md" })).not.toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Apply README.md" })).not.toBeInTheDocument();
    expect(applyFileEdit).toHaveBeenCalledTimes(1);
  });

  it("retains an unknown inverse request across unmount and confirms it even after the task ends", async () => {
    const source = editFixture("source", "applied");
    const inverse = editFixture("inverse", "proposed");
    const createFileEditRevertProposal = vi.fn().mockRejectedValueOnce(new Error("proposal response lost"))
      .mockResolvedValueOnce({ edit: inverse, file_written: false, replayed: true });
    const client = { hasFileEditReview: true, createFileEditRevertProposal,
      fileEditQueue: vi.fn().mockResolvedValue({ items: [source], apply_enabled: false }),
      fileEdit: vi.fn().mockResolvedValue(inverse),
      fileEditChangeSet: vi.fn().mockResolvedValue(changeSetFor(source)) } as unknown as APIClient;
    const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false }, mutations: { retry: false } } });
    const user = userEvent.setup();
    const first = renderPanel(client, "running", queryClient);
    await user.click(await screen.findByRole("button", { name: /README.md/ }));
    await user.click(screen.getByRole("button", { name: "Preview revert of this edit" }));
    await screen.findByText("proposal response lost");
    first.unmount();
    const onRequestRevert = vi.fn();
    renderPanel(client, "completed", queryClient, { onRequestRevert });
    await user.click(await screen.findByRole("button", { name: /README.md.*applied/ }));
    expect(screen.getByRole("button", { name: "Revert this edit in conversation" })).toBeDisabled();
    await user.click(await screen.findByRole("button", { name: "Confirm previous revert proposal" }));
    await waitFor(() => expect(createFileEditRevertProposal).toHaveBeenCalledTimes(2));
    expect(createFileEditRevertProposal.mock.calls[1]).toEqual(createFileEditRevertProposal.mock.calls[0]);
    expect(screen.queryByRole("button", { name: "Confirm previous revert proposal" })).not.toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: /README.md.*applied/ }));
    expect(screen.getByRole("button", { name: "View revert proposal" })).toBeEnabled();
    await user.click(screen.getByRole("button", { name: "View revert proposal" }));
    expect(onRequestRevert).not.toHaveBeenCalled();
    expect(createFileEditRevertProposal).toHaveBeenCalledTimes(2);
  });

  it("does not open a late inverse response over another selected edit", async () => {
    const source = editFixture("source", "applied");
    const other = { ...editFixture("other", "applied"), path: "other.txt" };
    const inverse = editFixture("inverse", "proposed");
    let resolve!: (value: unknown) => void;
    const client = { hasFileEditReview: true,
      createFileEditRevertProposal: vi.fn(() => new Promise((done) => { resolve = done; })),
      fileEditQueue: vi.fn().mockResolvedValue({ items: [source, other], apply_enabled: false }),
      fileEditChangeSet: vi.fn().mockResolvedValue(changeSetFor(source)) } as unknown as APIClient;
    const user = userEvent.setup();
    renderPanel(client);
    await user.click(await screen.findByRole("button", { name: /README.md/ }));
    await user.click(screen.getByRole("button", { name: "Preview revert of this edit" }));
    await user.click(screen.getByRole("button", { name: /other.txt/ }));
    await act(async () => resolve({ edit: inverse, file_written: false }));
    expect(screen.getByRole("complementary", { name: "Review other.txt" })).toBeInTheDocument();
    expect(screen.queryByText(/This revert diff is awaiting review/)).not.toBeInTheDocument();
  });

  it("reads the selected deletion's exact content without letting a late detail replace another selection", async () => {
    const deletion = { ...editFixture("delete", "proposed"), operation: "delete" as const, proposed_hash: "missing",
      diff: "Delete metadata: 64 bytes" };
    const other = { ...editFixture("other", "applied"), path: "other.txt" };
    let resolve!: (value: FileEditPreviewView) => void;
    const exact = { ...deletion, diff: "--- a/README.md\n+++ /dev/null\n@@ -1,2 +0,0 @@\n-first line being removed\n-second line being removed\n" };
    const move = { ...editFixture("move", "applied"), operation: "move" as const, path: "source.txt", destination_path: "target.txt",
      diff: "move source.txt -> target.txt" };
    const fileEdit = vi.fn().mockImplementationOnce(() => new Promise((done) => { resolve = done; })).mockResolvedValue(exact);
    const client = { hasFileEditReview: true, fileEdit,
      fileEditQueue: vi.fn().mockResolvedValue({ items: [deletion, other, move], apply_enabled: false }),
      fileEditChangeSet: vi.fn().mockResolvedValue(changeSetFor(deletion)) } as unknown as APIClient;
    const user = userEvent.setup();
    renderPanel(client);
    const deletionRow = await screen.findByRole("button", { name: /README.md/ });
    expect(deletionRow).toHaveTextContent("Metadata only");
    expect(deletionRow.querySelector(".diff-deletions")).toBeNull();
    expect(screen.getByRole("button", { name: /source.txt/ })).toHaveTextContent("Metadata only");
    expect(screen.getByRole("heading", { name: "Diff review" }).closest("header")?.querySelector(".diff-deletions")).toBeNull();
    await user.click(deletionRow);
    expect(fileEdit).toHaveBeenCalledWith("run-1", "delete", expect.any(AbortSignal));
    expect(screen.queryByRole("button", { name: "Approve and apply README.md" })).not.toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: /other.txt/ }));
    await act(async () => resolve(exact));
    expect(screen.getByRole("complementary", { name: "Review other.txt" })).toBeInTheDocument();
    expect(screen.queryByText("first line being removed")).not.toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: /README.md/ }));
    expect(await screen.findByText("first line being removed")).toBeInTheDocument();
    expect(screen.getByText("second line being removed")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Approve and apply README.md" })).toBeInTheDocument();
    expect(screen.getByRole("button", { name: /^README\.md/ }).querySelector(".diff-deletions")).toHaveTextContent("-2");
    expect(screen.getByRole("button", { name: /^README\.md/ })).not.toHaveTextContent("Metadata only");
    expect(screen.getByRole("heading", { name: "Diff review" }).closest("header")?.querySelector(".diff-deletions")).toBeNull();
  });

  it("confirms the original file application after unmount even when the edit is already applied", async () => {
    const approved = { ...editFixture("approved", "approved"), apply_enabled: true };
    let current = approved;
    const receipt = { protocol_version: "operation_receipt.v1", kind: "file_edit_apply", outcome: "applied",
      durable: true, replayed: true, retry_safe: true, retry_strategy: "same_operation_key", recovery_action: "none", cleanup_state: "complete" };
    let count = 0;
    const applyFileEdit = vi.fn(async () => {
      current = { ...approved, status: "applied", apply_enabled: false };
      if (++count === 1) throw new Error("apply response lost");
      return { edit: current, status: "applied", receipt };
    });
    const client = { hasFileEditApply: true, hasFileEditReview: true, applyFileEdit,
      fileEditQueue: vi.fn(async () => ({ items: [current], apply_enabled: current.apply_enabled })),
      fileEditChangeSet: vi.fn().mockResolvedValue(changeSetFor(approved)) } as unknown as APIClient;
    const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false }, mutations: { retry: false } } });
    const user = userEvent.setup();
    const first = renderPanel(client, "running", queryClient);
    await user.click(await screen.findByRole("button", { name: /README.md/ }));
    await user.click(screen.getByRole("button", { name: "Apply README.md" }));
    await screen.findByText("apply response lost");
    first.unmount();
    renderPanel(client, "completed", queryClient);
    await user.click(await screen.findByRole("button", { name: "Confirm previous file apply" }));
    await waitFor(() => expect(applyFileEdit).toHaveBeenCalledTimes(2));
    expect(applyFileEdit.mock.calls[1]).toEqual(applyFileEdit.mock.calls[0]);
    expect(queryClient.getQueryData(["run", "run-1", "file-apply-attempts"])).toEqual({});
    expect(screen.queryByRole("button", { name: "Confirm previous file apply" })).not.toBeInTheDocument();
    expect(await screen.findByText(/File application is confirmed complete/)).toBeInTheDocument();
  });

  it("clears a confirmed failed application without claiming that the file is unchanged", async () => {
    let edit = { ...editFixture("approved", "approved"), apply_enabled: true };
    const applyFileEdit = vi.fn(async () => {
      edit = { ...edit, status: "failed", apply_enabled: false };
      return { edit, status: "failed", receipt: { protocol_version: "operation_receipt.v1", kind: "file_edit_apply", outcome: "failed",
        durable: true, replayed: true, retry_safe: true, retry_strategy: "same_operation_key", recovery_action: "inspect", cleanup_state: "complete" } };
    });
    const client = { hasFileEditApply: true, applyFileEdit,
      fileEditQueue: vi.fn(async () => ({ items: [edit], apply_enabled: edit.apply_enabled })),
      fileEditChangeSet: vi.fn(async () => changeSetFor(edit)) } as unknown as APIClient;
    const user = userEvent.setup();
    renderPanel(client);
    await user.click(await screen.findByRole("button", { name: /README.md/ }));
    await user.click(screen.getByRole("button", { name: "Apply README.md" }));
    expect(await screen.findByText(/The apply attempt failed/)).toHaveTextContent("the file may have partially changed");
    expect(screen.queryByRole("button", { name: "Confirm previous file apply" })).not.toBeInTheDocument();
    expect(screen.queryByText(/File application is confirmed complete/)).not.toBeInTheDocument();
  });

  it.each(["completed", "paused"])("keeps new inverse proposals closed for a %s execution", async (status) => {
    const edit = editFixture("source", "applied");
    const client = { hasFileEditReview: true, createFileEditRevertProposal: vi.fn(),
      fileEditQueue: vi.fn().mockResolvedValue({ items: [edit], apply_enabled: false }),
      fileEditChangeSet: vi.fn().mockResolvedValue(changeSetFor(edit)) } as unknown as APIClient;
    const user = userEvent.setup();
    renderPanel(client, status);
    await user.click(await screen.findByRole("button", { name: /README.md/ }));
    expect(screen.getByRole("button", { name: "Preview revert of this edit" })).toBeDisabled();
    expect(client.createFileEditRevertProposal).not.toHaveBeenCalled();
  });

  it("displays direct Go apply outcome and renders edit.reason alert on CAS conflict failure", async () => {
    const proposed = editFixture("edit-cas", "proposed");
    const failed = { ...proposed, status: "failed" as const, allowed_actions: [],
      reason: "workspace file changed after review; refusing to apply" };
    const queue = vi.fn().mockResolvedValueOnce({ items: [proposed], truncated: false, apply_enabled: false })
      .mockResolvedValue({ items: [failed], truncated: false, apply_enabled: false });
    const review = vi.fn().mockResolvedValue({ protocol_version: "file_edit_review.v1", run_id: "run-1",
      action: "approve_intent", edit: failed, file_written: false, replayed: false,
      continuation: { state: "completed", replayed: false, model_called: true, tool_called: false, handoff_id: "h-1" } });
    const client = { hasFileEditReview: true, fileEditQueue: queue,
      fileEditChangeSet: vi.fn().mockResolvedValue(changeSetFor(proposed)),
      reviewFileEdit: review } as unknown as APIClient;
    const user = userEvent.setup();
    renderPanel(client, "running", undefined, { onRequestChange: vi.fn() });
    await user.click(await screen.findByRole("button", { name: /README.md/ }));
    expect(screen.getByText("Approve to apply")).toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: "Approve and apply README.md" }));
    const alert = await screen.findByRole("alert");
    expect(alert).toHaveTextContent("workspace file changed after review; refusing to apply");
  });
});

function editFixture(id: string, status: FileEditPreviewView["status"]): FileEditPreviewView {
  return { id, session_id: "session-1", workspace_id: "workspace-1", path: "README.md", operation: "replace", status,
    diff: "--- README.md\n+++ README.md\n-old\n+new", original_hash: "a".repeat(64), proposed_hash: "b".repeat(64),
    secrets_redacted: false, allowed_actions: status === "proposed" ? ["approve_intent", "deny"] : [], apply_enabled: false,
    created_at: "2026-07-18T00:00:00Z", updated_at: "2026-07-18T00:00:00Z" };
}

function changeSetFor(edit: {
  id: string; path: string; operation: string; status: string; diff: string;
  secrets_redacted: boolean;
  allowed_actions: readonly string[]; apply_enabled: boolean; updated_at: string;
}) {
  const item = changeSetItem(edit);
  return {
    protocol_version: "file_edit_change_set.v1", run_id: "run-1", session_id: "session-1",
    workspace_id: "workspace-1", items: [item], proposed_count: edit.status === "proposed" ? 1 : 0,
    approved_count: edit.status === "approved" ? 1 : 0,
    applied_count: edit.status === "applied" ? 1 : 0, denied_count: edit.status === "denied" ? 1 : 0,
    failed_count: edit.status === "failed" ? 1 : 0, returned_count: 1,
    total_diff_bytes: new TextEncoder().encode(edit.diff).length, truncated: false,
    review_independent: true, apply_independent: true, atomic_apply: false,
    batch_mutation_supported: false, partial_apply_visible: true, diff_content_included: false,
  };
}

function changeSetItem(edit: {
  id: string; path: string; operation: string; status: string; diff: string;
  secrets_redacted: boolean;
  allowed_actions: readonly string[]; apply_enabled: boolean; updated_at: string;
}) {
  return { id: edit.id, path: edit.path, operation: edit.operation, status: edit.status,
    diff_bytes: new TextEncoder().encode(edit.diff).length,
    secrets_redacted: edit.secrets_redacted, allowed_actions: [...edit.allowed_actions],
    apply_enabled: edit.apply_enabled, updated_at: edit.updated_at };
}

function renderPanel(client: APIClient, runStatus = "running", queryClient = new QueryClient({ defaultOptions: {
    queries: { retry: false }, mutations: { retry: false },
  } }), props: { onRequestRevert?: (edit: FileEditPreviewView) => void; onRequestChange?: () => void } = {}) {
  return render(<QueryClientProvider client={queryClient}>
    <FileEditPanel client={client} runID="run-1" runStatus={runStatus} {...props} />
  </QueryClientProvider>);
}
