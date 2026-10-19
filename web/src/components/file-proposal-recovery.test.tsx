import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act, fireEvent, render, screen } from "@testing-library/react";
import type { APIClient } from "../api/client";
import { FileProposalRecovery } from "./file-proposal-recovery";
import { applyPrayuTheme } from "../lib/appearance";

vi.mock("@monaco-editor/react", () => ({
  DiffEditor: ({ original, modified, theme }: { original: string; modified: string; theme: string }) =>
    <pre aria-label="Recovered proposal diff" data-theme={theme}>{original}{modified}</pre>,
}));
vi.mock("../lib/monaco-local", () => ({ configureLocalMonaco: vi.fn() }));

it("renders a durable pending proposal as read-only stale review context", async () => {
  const recoverFileEditProposal = vi.fn().mockResolvedValue({
    protocol_version: "file_edit_proposal_recovery.v1", run_id: "run-1",
    workspace_id: "workspace-1", edit_id: "edit-1", path: "README.md",
    original_content: "before\n", proposed_content: "after\n",
    original_sha256: "a".repeat(64), proposed_sha256: "b".repeat(64),
    current_content_sha256: "c".repeat(64), status: "proposed", stale: true,
    review_required: true, editable: false, file_write: false,
  });
  const client = { recoverFileEditProposal } as unknown as APIClient;
  render(<QueryClientProvider client={new QueryClient()}>
    <FileProposalRecovery client={client} editID="edit-1" onClose={vi.fn()} runID="run-1" />
  </QueryClientProvider>);
  const editor = await screen.findByLabelText("Recovered proposal diff");
  expect(editor).toHaveTextContent("before");
  await act(async () => { applyPrayuTheme("light", false); });
  expect(editor).toHaveAttribute("data-theme", "vs");
  await act(async () => { applyPrayuTheme("dark", false); });
  expect(editor).toHaveAttribute("data-theme", "vs-dark");
  expect(screen.getByText("Workspace source changed")).toBeInTheDocument();
  expect(screen.queryByRole("button", { name: /apply|approve|create proposal/i }))
    .not.toBeInTheDocument();
  expect(recoverFileEditProposal).toHaveBeenCalledWith("run-1", "edit-1", expect.any(AbortSignal));
});

it("keeps a close control available when recovery fails", async () => {
  const onClose = vi.fn();
  const client = {
    recoverFileEditProposal: vi.fn().mockRejectedValue(new Error("unavailable")),
  } as unknown as APIClient;
  const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  render(<QueryClientProvider client={queryClient}>
    <FileProposalRecovery client={client} editID="edit-1" onClose={onClose} runID="run-1" />
  </QueryClientProvider>);
  expect(await screen.findByRole("alert")).toHaveTextContent("unavailable");
  fireEvent.click(screen.getByRole("button", { name: "Close recovered proposal" }));
  expect(onClose).toHaveBeenCalledOnce();
});
