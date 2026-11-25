import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act, render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import type { APIClient } from "../api/client";
import type { FileEditPreviewView } from "../api/types";
import { FileEditPanel, type FileEditReviewTarget } from "./file-edit-panel";

function edit(id: string, workspaceID = "workspace-current"): FileEditPreviewView {
  return { id, session_id: `session-${id}`, workspace_id: workspaceID, path: "same.txt",
    operation: "create", status: "proposed", diff: `--- /dev/null\n+++ same.txt\n@@ -0,0 +1 @@\n+${id}`,
    original_hash: "missing", proposed_hash: "b".repeat(64), secrets_redacted: false,
    allowed_actions: ["approve_intent", "deny"], apply_enabled: false,
    created_at: "2026-09-29T00:00:00Z", updated_at: "2026-09-29T00:00:00Z" };
}
function clientFor(fileEdit: ReturnType<typeof vi.fn>, items: FileEditPreviewView[] = [edit("decoy")]) {
  return { hasFileEditReview: true, hasFileEditApply: true, fileEdit,
    fileEditQueue: vi.fn().mockResolvedValue({ items, truncated: false, apply_enabled: true }),
    fileEditChangeSet: vi.fn().mockResolvedValue({ workspace_id: "workspace-current", proposed_count: items.length,
      approved_count: 0, applied_count: 0, denied_count: 0, failed_count: 0, returned_count: items.length, total_diff_bytes: 0 }),
    reviewFileEdit: vi.fn(), applyFileEdit: vi.fn(), createFileEditRevertProposal: vi.fn(),
  };
}
function setup(client: ReturnType<typeof clientFor>, target: FileEditReviewTarget) {
  const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false }, mutations: { retry: false } } });
  const component = (runID: string, initialTarget: FileEditReviewTarget) => <QueryClientProvider client={queryClient}>
    <FileEditPanel client={client as unknown as APIClient} runID={runID} initialTarget={initialTarget} />
  </QueryClientProvider>;
  return { ...render(component(target.runID, target)), component, queryClient };
}
function expectNoMutation(client: ReturnType<typeof clientFor>) {
  expect(client.reviewFileEdit).not.toHaveBeenCalled();
  expect(client.applyFileEdit).not.toHaveBeenCalled();
  expect(client.createFileEditRevertProposal).not.toHaveBeenCalled();
}

it.each(["run-current", "run-history"])("reads the precise target in %s before opening a same-path proposal", async (runID) => {
  const workspaceID = runID === "run-current" ? "workspace-current" : "workspace-history";
  const targetEdit = edit("requested", workspaceID);
  let finish!: (value: FileEditPreviewView) => void;
  const fileEdit = vi.fn(() => new Promise<FileEditPreviewView>((resolve) => { finish = resolve; }));
  const client = clientFor(fileEdit, [edit("decoy"), { ...targetEdit, diff: "+cached-preview" }]);
  setup(client, { runID, editID: targetEdit.id, workspaceID });
  await screen.findByText("Reading the requested file proposal…");
  expect(fileEdit).toHaveBeenCalledWith(runID, "requested", expect.any(AbortSignal));
  expect(screen.queryByRole("complementary")).not.toBeInTheDocument();
  expect(screen.queryByRole("button", { name: "Approve and apply same.txt" })).not.toBeInTheDocument();
  await act(async () => finish(targetEdit));
  const drawer = await screen.findByRole("complementary", { name: "Review same.txt" });
  expect(within(drawer).getByText("requested")).toBeInTheDocument();
  expect(within(drawer).queryByText("cached-preview")).not.toBeInTheDocument();
  expect(screen.getByRole("button", { name: "Close review" })).toHaveFocus();
  if (runID === "run-history") {
    expect(within(drawer).getByText("Original directory record")).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Approve and apply same.txt" })).not.toBeInTheDocument();
  } else {
    expect(screen.getByRole("button", { name: "Approve and apply same.txt" })).toBeEnabled();
  }
  expectNoMutation(client);
  await userEvent.click(screen.getByRole("button", { name: "Close review" }));
  expect(screen.queryByRole("complementary")).not.toBeInTheDocument();
  await waitFor(() => expect(screen.getAllByRole("button", { name: /same.txt.*proposed/ })[1]).toHaveFocus());
});

it.each(["missing", "wrong-id", "wrong-workspace"])("does not substitute another same-path edit when the target is %s", async (failure) => {
  const result = edit(failure === "wrong-id" ? "another-id" : "requested",
    failure === "wrong-workspace" ? "another-workspace" : "workspace-current");
  const fileEdit = failure === "missing" ? vi.fn().mockRejectedValue(new Error("file edit not found")) : vi.fn().mockResolvedValue(result);
  const client = clientFor(fileEdit, [edit("decoy"), edit("requested")]);
  setup(client, { runID: "run-current", editID: "requested", workspaceID: "workspace-current" });
  expect(await screen.findByText(/The requested file proposal could not be confirmed and has not been opened/)).toBeInTheDocument();
  expect(screen.queryByRole("complementary")).not.toBeInTheDocument();
  expect(screen.queryByRole("button", { name: "Approve and apply same.txt" })).not.toBeInTheDocument();
  await userEvent.click(screen.getByRole("button", { name: "Retry proposal" }));
  await waitFor(() => expect(fileEdit).toHaveBeenCalledTimes(2));
  expect(fileEdit.mock.calls.every(([runID, editID]) => runID === "run-current" && editID === "requested")).toBe(true);
  expectNoMutation(client);
});

it("opens a precise record omitted from the queue without calling an empty list complete", async () => {
  const client = clientFor(vi.fn().mockResolvedValue(edit("omitted")), []);
  setup(client, { runID: "run-current", editID: "omitted", workspaceID: "workspace-current" });
  expect(await screen.findByRole("complementary", { name: "Review same.txt" })).toBeInTheDocument();
  expect(screen.queryByText("No file edit proposals")).not.toBeInTheDocument();
  expectNoMutation(client);
});

it("keeps a late target read in its original run when another run is opened", async () => {
  let finishOld!: (value: FileEditPreviewView) => void;
  const fileEdit = vi.fn((runID: string, _editID: string) => runID === "run-old"
    ? new Promise<FileEditPreviewView>((resolve) => { finishOld = resolve; }) : Promise.resolve(edit("new-target")));
  const client = clientFor(fileEdit);
  const view = setup(client, { runID: "run-old", editID: "old-target", workspaceID: "workspace-current" });
  await waitFor(() => expect(fileEdit).toHaveBeenCalledWith("run-old", "old-target", expect.any(AbortSignal)));
  view.rerender(view.component("run-new", { runID: "run-new", editID: "new-target", workspaceID: "workspace-current" }));
  const drawer = await screen.findByRole("complementary", { name: "Review same.txt" });
  expect(within(drawer).getByText("new-target")).toBeInTheDocument();
  await act(async () => finishOld(edit("old-target")));
  expect(within(drawer).queryByText("old-target")).not.toBeInTheDocument();
  expect(fileEdit.mock.calls.map(([runID, editID]) => [runID, editID])).toEqual([["run-old", "old-target"], ["run-new", "new-target"]]);
  expectNoMutation(client);
});

it("does not read a target through a different run or expose authority on a read-only connection", async () => {
  const client = clientFor(vi.fn().mockResolvedValue({ ...edit("requested"), apply_enabled: true }));
  client.hasFileEditReview = false; client.hasFileEditApply = false;
  const target = { runID: "run-old", editID: "requested", workspaceID: "workspace-current" };
  const view = setup(client, target);
  expect(await screen.findByRole("complementary", { name: "Review same.txt" })).toBeInTheDocument();
  expect(screen.queryByRole("button", { name: "Approve and apply same.txt" })).not.toBeInTheDocument();
  expect(screen.queryByRole("button", { name: "Apply same.txt" })).not.toBeInTheDocument();
  view.rerender(view.component("run-new", target));
  expect(await screen.findByText(/belongs to another execution. Open it from that execution/)).toBeInTheDocument();
  expect(screen.queryByRole("complementary")).not.toBeInTheDocument();
  expect(client.fileEdit).toHaveBeenCalledTimes(1);
  expectNoMutation(client);
});
