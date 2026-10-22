import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act, fireEvent, render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import type { APIClient } from "../api/client";
import type { WorkspaceExplorerView } from "../api/types";
import { WorkspaceExplorer } from "./workspace-explorer";
import type { WorkspaceQuoteResult } from "./workspace-file-quote";

const content = "before\n  selected start\n\n```inside the file\nselected end\nafter";
function snapshot(path = "src/example.ts", digest = "a".repeat(64)): WorkspaceExplorerView {
  return { protocol_version: "workspace_explorer.v1", workspace_id: "actual-worktree", path, kind: "file", entries: [],
    content, returned_bytes: content.length, total_bytes: 500, truncated: true, redaction_count: 1, root_path_exposed: false,
    provenance: { version: "context_provenance.v1", source_kind: "workspace_file", source_ref: path,
      content_sha256: digest, instruction_authorized: false } };
}
function setup(outcome: WorkspaceQuoteResult = "added") {
  const client = { workspaceExplore: vi.fn().mockResolvedValue(snapshot()) } as unknown as APIClient;
  const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false, staleTime: Infinity } } });
  const quote = vi.fn((_content: string) => outcome);
  const returned = vi.fn();
  const component = (runID = "actual-run", path = "src/example.ts") => <QueryClientProvider client={queryClient}>
    <p>Outside the file</p>
    <WorkspaceExplorer client={client} workspaceID="actual-worktree" runID={runID} initialPath={path}
      onQuote={quote} onReturnToDraft={returned} />
  </QueryClientProvider>;
  return { ...render(component()), client, queryClient, quote, returned, component };
}
function select(container: HTMLElement, first: number, firstOffset: number, last: number, lastOffset: number) {
  const start = container.querySelector(`[data-line="${first}"] .explorer-line-content`)!.firstChild!;
  const end = container.querySelector(`[data-line="${last}"] .explorer-line-content`)!.firstChild!;
  act(() => {
    const range = document.createRange(); range.setStart(start, firstOffset); range.setEnd(end, lastOffset);
    const selection = document.getSelection()!; selection.removeAllRanges(); selection.addRange(range);
    document.dispatchEvent(new Event("selectionchange"));
  });
}
afterEach(() => { document.getSelection()?.removeAllRanges(); });

it("quotes the full selected source lines with exact provenance, blank lines, safe fences, and no send", async () => {
  const { container, quote, returned, client } = setup();
  const user = userEvent.setup();
  const button = await screen.findByRole("button", { name: "Quote selected lines" });
  expect(button).toBeDisabled();
  select(container, 2, 4, 5, 3);
  expect(screen.getByText("Preview lines 2–5")).toBeVisible();
  await user.click(button);
  expect(quote).toHaveBeenCalledOnce();
  const excerpt = quote.mock.calls[0][0];
  for (const part of ['File excerpt: "src/example.ts"', "Preview lines 2–5", "Source workspace: actual-worktree", "Source run: actual-run",
    "a".repeat(64), "truncated", "redacted", "````text\n  selected start\n\n```inside the file\nselected end\n````"]) {
    expect(excerpt).toContain(part);
  }
  expect(excerpt).not.toContain("before"); expect(excerpt).not.toContain("after");
  expect(screen.getByRole("status")).toHaveTextContent("Not sent");
  fireEvent.click(button); // A second click cannot duplicate an already consumed selection.
  expect(quote).toHaveBeenCalledOnce();
  expect(client.workspaceExplore).toHaveBeenCalledTimes(1);
  await user.click(screen.getByRole("button", { name: "Return to chat draft" }));
  expect(returned).toHaveBeenCalledOnce();
});

it("excludes an end boundary at the next line's column zero and rejects selections crossing outside the source", async () => {
  const { container, quote } = setup();
  const button = await screen.findByRole("button", { name: "Quote selected lines" });
  select(container, 2, 0, 5, 0);
  fireEvent.click(button);
  expect(quote.mock.calls[0][0]).toContain("Preview lines 2–4");
  expect(quote.mock.calls[0][0]).not.toContain("selected end");
  act(() => {
    const range = document.createRange();
    range.setStart(screen.getByText("Outside the file").firstChild!, 1);
    range.setEnd(container.querySelector('[data-line="2"] .explorer-line-content')!.firstChild!, 3);
    document.getSelection()!.addRange(range);
    document.dispatchEvent(new Event("selectionchange"));
  });
  expect(button).toBeDisabled();
});

it.each(["file", "run", "snapshot"] as const)("discards the old range when the %s identity changes, even if the displayed text is identical", async (change) => {
  const { container, component, client, queryClient, rerender, quote } = setup();
  await screen.findByRole("button", { name: "Quote selected lines" });
  select(container, 2, 0, 5, 3);
  if (change === "file") {
    vi.mocked(client.workspaceExplore).mockResolvedValue(snapshot("src/other.ts"));
    rerender(component("actual-run", "src/other.ts"));
    await waitFor(() => expect(client.workspaceExplore).toHaveBeenLastCalledWith("actual-worktree", "src/other.ts", expect.any(AbortSignal)));
  } else if (change === "run") rerender(component("other-run"));
  else act(() => { queryClient.setQueryData(["workspace", "actual-worktree", "explore", "src/example.ts"], snapshot(undefined, "b".repeat(64))); });
  await waitFor(() => expect(screen.getByRole("button", { name: "Quote selected lines" })).toBeDisabled());
  expect(quote).not.toHaveBeenCalled();
});

it.each(["unsaved", "blocked"] as const)("reports a %s draft without claiming persistence or silently losing the excerpt", async (outcome) => {
  const { container, quote } = setup(outcome);
  const button = await screen.findByRole("button", { name: "Quote selected lines" });
  select(container, 2, 0, 2, 5);
  fireEvent.click(button);
  expect(screen.getByRole("alert")).toHaveTextContent(outcome === "unsaved" ? "local saving failed" : "Could not append");
  fireEvent.click(button);
  expect(quote).toHaveBeenCalledTimes(outcome === "unsaved" ? 1 : 2);
});
