import { useState, type ReactNode } from "react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import type { APIClient } from "../api/client";
import type { FileEditProposalSourceView } from "../api/types";
import { V2RecoveryProvider } from "../v2/recovery-storage";
import { useV2Navigation } from "../v2/navigation";
import { FileProposalEditor } from "./file-proposal-editor";
import { WorkspaceExplorer } from "./workspace-explorer";
import { FileEditDraftProvider, useFileEditLeave } from "./file-edit-drafts";

vi.mock("@monaco-editor/react", () => ({
  default: ({ value, onChange }: { value: string; onChange: (value: string) => void }) =>
    <textarea aria-label="File editor" onChange={(event) => onChange(event.target.value)} value={value} />,
  DiffEditor: ({ original, modified }: { original: string; modified: string }) =>
    <pre aria-label="File diff">{original}{modified}</pre>,
}));
vi.mock("../lib/monaco-local", () => ({ configureLocalMonaco: vi.fn() }));

const source: FileEditProposalSourceView = {
  protocol_version: "file_edit_proposal.v1", run_id: "run-1", workspace_id: "workspace-1",
  path: "a.txt", content: "original A", content_sha256: "a".repeat(64), source_handle: "B".repeat(43),
  expires_at: "2099-01-01T00:00:00Z", editable: true, file_write: false,
};
const secondSource = { ...source, path: "b.txt", content: "original B", content_sha256: "b".repeat(64) };
const clients: QueryClient[] = [];
afterEach(() => {
  clients.splice(0).forEach((client) => client.clear());
  window.localStorage.clear();
  window.history.replaceState({}, "", "/");
});
function queries() {
  const query = new QueryClient({ defaultOptions: { queries: { retry: false }, mutations: { retry: false } } });
  clients.push(query); return query;
}
function fakeClient() {
  return { baseURL: "http://localhost:8765/api/v1", createFileEditProposal: vi.fn().mockResolvedValue({
    edit: { id: "edit-1" }, approval_required: true, file_written: false,
  }) } as unknown as APIClient;
}
function providers(client: APIClient, children: ReactNode, query = queries()) {
  return <QueryClientProvider client={query}><V2RecoveryProvider client={client} scopeID="store-1">
    <FileEditDraftProvider>{children}</FileEditDraftProvider>
  </V2RecoveryProvider></QueryClientProvider>;
}
function EditorHarness({ client }: { client: APIClient }) {
  const [file, setFile] = useState(source);
  const [open, setOpen] = useState(true);
  const leave = useFileEditLeave();
  const navigation = useV2Navigation();
  return <>
    <button onClick={() => leave(() => setFile(file.path === source.path ? secondSource : source))}>Switch file</button>
    <button onClick={() => setOpen(true)}>Reopen editor</button>
    <button onClick={() => navigation.navigate({ kind: "new" })}>New conversation</button>
    <a href="#/threads/another-thread">Other conversation link</a>
    <output aria-label="Route">{navigation.route.kind}</output>
    {open && <FileProposalEditor client={client} runID={file.run_id} source={file} onClose={() => setOpen(false)} />}
  </>;
}

it("keeps each file draft across switches and a fresh app mount without persisting source handles", async () => {
  const client = fakeClient(); const user = userEvent.setup();
  const mounted = render(providers(client, <EditorHarness client={client} />));
  fireEvent.change(await screen.findByLabelText("File editor"), { target: { value: "draft A" } });
  await user.click(screen.getByText("Switch file"));
  expect(screen.getByRole("dialog")).toHaveTextContent("a.txt");
  await user.click(screen.getByText("Keep draft"));
  expect(await screen.findByLabelText("File editor")).toHaveValue("original B");
  fireEvent.change(screen.getByLabelText("File editor"), { target: { value: "draft B" } });
  await user.click(screen.getByText("Switch file"));
  await user.click(screen.getByText("Keep draft"));
  expect(await screen.findByLabelText("File editor")).toHaveValue("draft A");
  const unload = new Event("beforeunload", { cancelable: true });
  window.dispatchEvent(unload);
  expect(unload.defaultPrevented).toBe(true);
  const saved = Object.values(window.localStorage).join("");
  expect(saved).toContain("draft A"); expect(saved).toContain("draft B");
  expect(saved).not.toContain(source.source_handle);
  mounted.unmount();
  render(providers(client, <EditorHarness client={client} />));
  expect(await screen.findByLabelText("File editor")).toHaveValue("draft A");
  expect(client.createFileEditProposal).not.toHaveBeenCalled();
});

it("creates a proposal before leaving and removes only that file draft", async () => {
  const client = fakeClient(); const user = userEvent.setup();
  render(providers(client, <EditorHarness client={client} />));
  fireEvent.change(await screen.findByLabelText("File editor"), { target: { value: "proposed A" } });
  await user.click(screen.getByRole("button", { name: "Close file proposal editor" }));
  await user.click(screen.getByText("Save as proposal"));
  await waitFor(() => expect(screen.queryByLabelText("File editor")).not.toBeInTheDocument());
  expect(client.createFileEditProposal).toHaveBeenCalledWith("run-1", {
    version: "file_edit_proposal.v1", source_handle: source.source_handle, proposed_text: "proposed A",
  });
  await user.click(screen.getByText("Reopen editor"));
  expect(await screen.findByLabelText("File editor")).toHaveValue("original A");
});

it("blocks route changes until the user chooses and keeps a failed proposal editable", async () => {
  const client = fakeClient(); const user = userEvent.setup();
  vi.mocked(client.createFileEditProposal).mockRejectedValue(new Error("Proposal rejected"));
  render(providers(client, <EditorHarness client={client} />));
  fireEvent.change(await screen.findByLabelText("File editor"), { target: { value: "retain me" } });
  await user.click(screen.getByText("New conversation"));
  expect(window.location.hash).toBe("");
  await user.click(screen.getByText("Save as proposal"));
  expect(await screen.findByRole("dialog")).toHaveTextContent("Proposal rejected");
  expect(window.location.hash).toBe("");
  await user.click(screen.getByText("Continue editing"));
  expect(screen.getByLabelText("File editor")).toHaveValue("retain me");
  await user.click(screen.getByText("New conversation"));
  await user.click(screen.getByText("Keep draft"));
  await waitFor(() => expect(window.location.hash).toBe("#/new"));
});

it("guards hash links and browser Back while preserving their history entries", async () => {
  const client = fakeClient(); const user = userEvent.setup();
  render(providers(client, <EditorHarness client={client} />));
  fireEvent.change(await screen.findByLabelText("File editor"), { target: { value: "keep across history" } });
  await user.click(screen.getByText("Other conversation link"));
  expect(await screen.findByRole("dialog")).toBeInTheDocument();
  await user.click(screen.getByText("Continue editing"));
  await waitFor(() => expect(window.location.hash).toBe(""));
  expect(screen.getByLabelText("Route")).toHaveTextContent("initial");
  await user.click(screen.getByText("Other conversation link"));
  await screen.findByRole("dialog");
  await user.click(screen.getByText("Keep draft"));
  await waitFor(() => expect(screen.getByLabelText("Route")).toHaveTextContent("thread"));
  window.history.back();
  await screen.findByRole("dialog");
  await user.click(screen.getByText("Continue editing"));
  await waitFor(() => expect(window.location.hash).toBe("#/threads/another-thread"));
  expect(screen.getByLabelText("File editor")).toHaveValue("keep across history");
});

it("discards only after an explicit choice and restores the original on reopen", async () => {
  const client = fakeClient(); const user = userEvent.setup();
  render(providers(client, <EditorHarness client={client} />));
  fireEvent.change(await screen.findByLabelText("File editor"), { target: { value: "discard me" } });
  await user.click(screen.getByRole("button", { name: "Close file proposal editor" }));
  await user.keyboard("{Escape}");
  expect(screen.getByLabelText("File editor")).toHaveValue("discard me");
  await user.click(screen.getByRole("button", { name: "Close file proposal editor" }));
  await user.click(screen.getByText("Discard edits"));
  await user.click(screen.getByText("Reopen editor"));
  expect(await screen.findByLabelText("File editor")).toHaveValue("original A");
  expect(client.createFileEditProposal).not.toHaveBeenCalled();
});

it("retains an old draft on changed source and requires review before using its fresh handle", async () => {
  const client = fakeClient(); const user = userEvent.setup();
  const first = render(providers(client, <EditorHarness client={client} />));
  fireEvent.change(await screen.findByLabelText("File editor"), { target: { value: "old draft" } });
  first.unmount();
  const changed = { ...source, content: "upstream change", content_sha256: "c".repeat(64), source_handle: "C".repeat(43) };
  render(providers(client, <FileProposalEditor client={client} runID="run-1" source={changed} onClose={vi.fn()} />));
  expect(await screen.findByLabelText("File editor")).toHaveValue("old draft");
  expect(screen.getByText("Create proposal")).toBeDisabled();
  await user.click(screen.getByText("Review diff"));
  expect(screen.getByLabelText("File diff")).toHaveTextContent("upstream changeold draft");
  await user.click(screen.getByText("Use current version after review"));
  await user.click(screen.getByText("Create proposal"));
  expect(client.createFileEditProposal).toHaveBeenCalledWith("run-1", {
    version: "file_edit_proposal.v1", source_handle: changed.source_handle, proposed_text: "old draft",
  });
});

it("does not leave or lose the in-memory draft when local persistence fails", async () => {
  const client = fakeClient(); const user = userEvent.setup(); const query = queries();
  const mounted = render(providers(client, <EditorHarness client={client} />, query));
  const editor = await screen.findByLabelText("File editor");
  vi.spyOn(Storage.prototype, "setItem").mockImplementation(() => { throw new Error("disk full"); });
  fireEvent.change(editor, { target: { value: "only in memory" } });
  await user.click(screen.getByRole("button", { name: "Close file proposal editor" }));
  await user.click(screen.getByText("Keep draft"));
  expect(screen.getByRole("dialog")).toHaveTextContent("无法保存本机恢复数据");
  await user.click(screen.getByText("Continue editing"));
  expect(screen.getByLabelText("File editor")).toHaveValue("only in memory");
  mounted.unmount();
  render(providers(client, <EditorHarness client={client} />, query));
  expect(await screen.findByLabelText("File editor")).toHaveValue("only in memory");
});

it("protects the actual explorer parent-directory action and restores each reopened file", async () => {
  const client = fakeClient(); const user = userEvent.setup();
  Object.assign(client, {
    hasFileEditProposals: true,
    issueFileEditProposalSource: vi.fn(async (_runID, path) => path === source.path ? source : secondSource),
    workspaceExplore: vi.fn(async (_workspaceID, path) => ({
      protocol_version: "workspace_explorer.v1", workspace_id: "workspace-1", path,
      kind: path === "." ? "directory" : "file", content: path === source.path ? source.content : secondSource.content,
      entries: [source, secondSource].map((file) => ({ path: file.path, name: file.path, kind: "file", readable: true, size_bytes: 10 })),
      total_bytes: 10, returned_bytes: 10, truncated: false, redaction_count: 0, root_path_exposed: false,
      provenance: { source_kind: "workspace_file", content_sha256: source.content_sha256 },
    })),
  });
  render(providers(client, <WorkspaceExplorer client={client} workspaceID="workspace-1" runID="run-1" />));
  for (const file of [source, secondSource]) {
    await user.click(await screen.findByRole("button", { name: new RegExp(file.path) }));
    await user.click(await screen.findByText("Edit proposal"));
    fireEvent.change(await screen.findByLabelText("File editor"), { target: { value: `draft ${file.path}` } });
    await user.click(screen.getByRole("button", { name: "Open parent directory" }));
    expect(screen.getByRole("dialog")).toHaveTextContent(file.path);
    await user.click(screen.getByText("Keep draft"));
  }
  await user.click(await screen.findByRole("button", { name: /a.txt/ }));
  await user.click(await screen.findByText("Edit proposal"));
  expect(await screen.findByLabelText("File editor")).toHaveValue("draft a.txt");
});
