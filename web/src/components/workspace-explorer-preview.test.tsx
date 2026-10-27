import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import type { APIClient } from "../api/client";
import type { WorkspaceExplorerView } from "../api/types";
import { V2RecoveryProvider } from "../v2/recovery-storage";
import { FileEditDraftProvider } from "./file-edit-drafts";
import { WorkspaceExplorer } from "./workspace-explorer";

vi.mock("@monaco-editor/react", () => ({
  default: ({ value, onChange }: { value: string; onChange: (value: string) => void }) =>
    <textarea aria-label="Monaco proposal editor" onChange={(event) => onChange(event.target.value)} value={value} />,
}));
vi.mock("../lib/monaco-local", () => ({ configureLocalMonaco: vi.fn() }));

describe("WorkspaceExplorer Markdown preview", () => {
  it("switches the returned content between source and safe formatted Markdown", async () => {
    const snapshot = markdownSnapshot([
      "# Project notes", "", "**Ready**", "", "- Review changes", "",
      "[web](https://example.com/docs) [relative](./notes.md) [local](file:///secret) [unsafe](javascript:alert(1))",
      "", "![tracking](https://example.com/pixel.png)", "<script>alert(1)</script>",
      "", "SESSION_SECRET=[REDACTED:secret]",
    ].join("\n"));
    const client = previewClient(snapshot);
    const user = userEvent.setup();
    const view = renderExplorer(client);

    expect(await screen.findByText("# Project notes")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Source", pressed: true })).toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: "Preview" }));
    const preview = screen.getByRole("region", { name: "Markdown preview" });
    expect(within(preview).getByRole("heading", { name: "Project notes" })).toBeInTheDocument();
    expect(within(preview).getByText("Ready").tagName).toBe("STRONG");
    expect(within(preview).getByRole("listitem")).toHaveTextContent("Review changes");
    expect(within(preview).getByRole("link", { name: "web" })).toHaveAttribute("target", "_blank");
    expect(within(preview).getByRole("link", { name: "web" })).toHaveAttribute("rel", "noreferrer noopener");
    for (const label of ["relative", "local", "unsafe"]) {
      expect(within(preview).getByText(label).tagName).toBe("SPAN");
    }
    expect(preview.querySelector("script, img")).not.toBeInTheDocument();
    expect(preview).toHaveTextContent("SESSION_SECRET=[REDACTED:secret]");
    expect(screen.getByText("1 redacted")).toBeInTheDocument();
    expect(view.container.querySelector(".explorer-file-lines")).not.toBeInTheDocument();

    await user.click(screen.getByRole("button", { name: "Source" }));
    expect(screen.getByText("# Project notes")).toBeInTheDocument();
    expect(screen.queryByRole("region", { name: "Markdown preview" })).not.toBeInTheDocument();
    expect(client.workspaceExplore).toHaveBeenCalledTimes(1);
  });

  it.each(["README.MD", "notes.markdown", "guide.mdown", "notes.txt"])("offers preview only for Markdown paths: %s", async (path) => {
    const client = previewClient({ ...markdownSnapshot(), path });
    renderExplorer(client, { initialPath: path });
    await screen.findByText("# Project notes");
    expect(Boolean(screen.queryByRole("button", { name: "Preview" }))).toBe(path !== "notes.txt");
  });

  it("returns to source for search results and later citation lines", async () => {
    const client = previewClient(markdownSnapshot());
    const user = userEvent.setup();
    const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    const explorer = (initialLine: number) => <QueryClientProvider client={queryClient}>
      <WorkspaceExplorer client={client} workspaceID="workspace-1" initialPath="README.md" initialLine={initialLine} />
    </QueryClientProvider>;
    const view = render(explorer(1));
    await user.click(await screen.findByRole("button", { name: "Preview" }));
    expect(screen.queryByText("Positioned at line 1")).not.toBeInTheDocument();
    await user.type(screen.getByLabelText("Search Workspace evidence"), "matched");
    await user.click(screen.getByRole("button", { name: "Search Workspace" }));
    await user.click(await screen.findByRole("button", { name: /README.md.*line 2/ }));
    expect(await screen.findByText("Positioned at line 2")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Source", pressed: true })).toBeInTheDocument();
    expect(view.container.querySelector('[data-line="2"]')).toHaveClass("is-highlighted");

    await user.click(screen.getByRole("button", { name: "Preview" }));
    view.rerender(explorer(3));
    expect(await screen.findByText("Positioned at line 3")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Source", pressed: true })).toBeInTheDocument();
  });

  it("references the same snapshot and retains truncation and provenance in preview", async () => {
    const snapshot = { ...markdownSnapshot(), truncated: true, total_bytes: 1024 };
    const onSelectReference = vi.fn();
    const client = previewClient(snapshot);
    const user = userEvent.setup();
    renderExplorer(client, { onSelectReference });
    await user.click(await screen.findByRole("button", { name: "Reference this file" }));
    await user.click(screen.getByRole("button", { name: "Preview" }));
    await user.click(screen.getByRole("button", { name: "Reference this file" }));
    expect(onSelectReference.mock.calls).toEqual([[snapshot], [snapshot]]);
    expect(screen.getByText("truncated")).toBeInTheDocument();
    expect(screen.getByText("workspace_file / File source record")).toBeInTheDocument();
    expect(screen.getByText("b".repeat(12))).toBeInTheDocument();
    expect(client.workspaceExplore).toHaveBeenCalledTimes(1);
  });

  it("keeps an editing draft across returning to preview and switching views", async () => {
    const snapshot = { ...markdownSnapshot(), redaction_count: 0 };
    const client = Object.assign(previewClient(snapshot), {
      hasFileEditProposals: true,
      issueFileEditProposalSource: vi.fn().mockResolvedValue({
        protocol_version: "file_edit_proposal.v1", run_id: "run-1", workspace_id: "workspace-1",
        path: snapshot.path, content: snapshot.content, content_sha256: snapshot.provenance.content_sha256,
        source_handle: "B".repeat(43), expires_at: "2099-07-18T00:05:00Z", editable: true, file_write: false,
      }),
      createFileEditProposal: vi.fn(),
    });
    const user = userEvent.setup();
    renderExplorer(client, { runID: "run-1" });
    await user.click(await screen.findByRole("button", { name: "Preview" }));
    await user.click(screen.getByRole("button", { name: "Edit proposal" }));
    const editor = await screen.findByLabelText("Monaco proposal editor");
    expect(screen.queryByRole("group", { name: "File view" })).not.toBeInTheDocument();
    await user.clear(editor);
    await user.type(editor, "# Retained draft");
    await user.click(screen.getByRole("button", { name: "Close file proposal editor" }));
    await user.click(screen.getByRole("button", { name: "Keep draft" }));
    expect(screen.getByRole("region", { name: "Markdown preview" })).toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: "Source" }));
    await user.click(screen.getByRole("button", { name: "Preview" }));
    await user.click(screen.getByRole("button", { name: "Edit proposal" }));
    await waitFor(() => expect(screen.getByLabelText("Monaco proposal editor")).toHaveValue("# Retained draft"));
    expect(client.createFileEditProposal).not.toHaveBeenCalled();
  });
});

function markdownSnapshot(content = "# Project notes\nmatched text\nthird line\n"): WorkspaceExplorerView {
  return {
    protocol_version: "workspace_explorer.v1", workspace_id: "workspace-1", path: "README.md",
    kind: "file", entries: [], content, total_bytes: content.length, returned_bytes: content.length,
    truncated: false, redaction_count: 1, root_path_exposed: false,
    provenance: { version: "context_provenance.v1", source_kind: "workspace_file", source_ref: "README.md",
      content_sha256: "b".repeat(64), instruction_authorized: false },
  };
}

function previewClient(snapshot: WorkspaceExplorerView) {
  return Object.assign({} as APIClient, {
    baseURL: "http://localhost:8765/api/v1",
    workspaceExplore: vi.fn().mockResolvedValue(snapshot),
    workspaceSearch: vi.fn().mockResolvedValue({ results: [{ path: "README.md", line: 2,
      match_kind: "content", snippet: "matched text", content_truncated: false,
      provenance: snapshot.provenance }], truncated: false }),
  });
}

function renderExplorer(client: APIClient, props: Partial<Parameters<typeof WorkspaceExplorer>[0]> = {}) {
  const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return render(<QueryClientProvider client={queryClient}><V2RecoveryProvider client={client} scopeID="preview-tests"><FileEditDraftProvider>
    <WorkspaceExplorer client={client} workspaceID="workspace-1" initialPath="README.md" {...props} />
  </FileEditDraftProvider></V2RecoveryProvider></QueryClientProvider>);
}
