import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act, render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import type { APIClient } from "../api/client";
import type { FileEditProposalSourceView } from "../api/types";
import { FileProposalEditor } from "./file-proposal-editor";
import { applyPrayuTheme } from "../lib/appearance";

vi.mock("@monaco-editor/react", () => ({
  default: ({ value, onChange, theme }: { value: string; onChange: (value: string) => void; theme: string }) =>
    <textarea aria-label="Monaco proposal editor" data-theme={theme} onChange={(event) => onChange(event.target.value)}
      value={value} />,
  DiffEditor: ({ original, modified }: { original: string; modified: string }) =>
    <pre aria-label="Monaco diff preview">{original}{modified}</pre>,
}));

vi.mock("../lib/monaco-local", () => ({ configureLocalMonaco: vi.fn() }));

describe("FileProposalEditor", () => {
  it("submits only the opaque source handle and creates a pending proposal", async () => {
    const source: FileEditProposalSourceView = {
      protocol_version: "file_edit_proposal.v1", run_id: "run-1",
      workspace_id: "workspace-1", path: "README.md", content: "before\n",
      content_sha256: "a".repeat(64), source_handle: "B".repeat(43),
      expires_at: "2099-07-18T00:05:00Z", editable: true, file_write: false,
    };
    const createFileEditProposal = vi.fn().mockResolvedValue({
      protocol_version: "file_edit_proposal.v1", run_id: "run-1", replayed: false,
      approval_required: true, file_written: false,
      edit: { id: "edit-1", status: "proposed", path: "README.md" },
    });
    const client = { createFileEditProposal } as unknown as APIClient;
    const user = userEvent.setup();
    render(<QueryClientProvider client={new QueryClient()}>
      <FileProposalEditor client={client} onClose={vi.fn()} runID="run-1" source={source} />
    </QueryClientProvider>);
    const editor = await screen.findByLabelText("Monaco proposal editor");
    await act(async () => { applyPrayuTheme("light", false); });
    expect(editor).toHaveAttribute("data-theme", "vs");
    await act(async () => { applyPrayuTheme("dark", false); });
    expect(editor).toHaveAttribute("data-theme", "vs-dark");
    await user.clear(editor);
    await user.type(editor, "after");
    await user.click(screen.getByRole("button", { name: "Create proposal" }));
    await waitFor(() => expect(createFileEditProposal).toHaveBeenCalledWith("run-1", {
      version: "file_edit_proposal.v1", source_handle: source.source_handle,
      proposed_text: "after",
    }));
    expect(await screen.findByText(/Pending review/)).toBeInTheDocument();
    expect(createFileEditProposal.mock.calls[0]?.[1]).not.toHaveProperty("path");
  });

  it("rotates an expired Go source without sending or discarding the renderer draft", async () => {
    const source: FileEditProposalSourceView = {
      protocol_version: "file_edit_proposal.v1", run_id: "run-1",
      workspace_id: "workspace-1", path: "README.md", content: "before\n",
      content_sha256: "a".repeat(64), source_handle: "B".repeat(43),
      expires_at: "2020-01-01T00:00:00Z", editable: true, file_write: false,
    };
    const renewed = { ...source, source_handle: "C".repeat(43),
      expires_at: "2099-07-18T00:05:00Z" };
    const reissueFileEditProposalSource = vi.fn().mockResolvedValue(renewed);
    const createFileEditProposal = vi.fn().mockResolvedValue({
      protocol_version: "file_edit_proposal.v1", run_id: "run-1", replayed: false,
      approval_required: true, file_written: false,
      edit: { id: "edit-2", status: "proposed", path: "README.md" },
    });
    const client = { createFileEditProposal,
      reissueFileEditProposalSource } as unknown as APIClient;
    const user = userEvent.setup();
    render(<QueryClientProvider client={new QueryClient()}>
      <FileProposalEditor client={client} onClose={vi.fn()} runID="run-1" source={source} />
    </QueryClientProvider>);
    const editor = await screen.findByLabelText("Monaco proposal editor");
    await user.clear(editor);
    await user.type(editor, "retained draft");
    await user.click(screen.getByRole("button", { name: "Refresh source" }));
    await waitFor(() => expect(reissueFileEditProposalSource)
      .toHaveBeenCalledWith("run-1", "README.md", "a".repeat(64)));
    await user.click(screen.getByRole("button", { name: "Create proposal" }));
    await waitFor(() => expect(createFileEditProposal).toHaveBeenCalledWith("run-1", {
      version: "file_edit_proposal.v1", source_handle: renewed.source_handle,
      proposed_text: "retained draft",
    }));
  });
});
