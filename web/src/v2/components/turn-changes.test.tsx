import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { cleanup, render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, expect, it, vi } from "vitest";
import { APIClient } from "../../api/client";
import type { ThreadChangeSummary } from "../../api/thread-changes";
import { V2Narrative } from "./narrative";

afterEach(() => cleanup());

function changes(overrides: Partial<ThreadChangeSummary> = {}): ThreadChangeSummary {
  return { version: "thread_change_summary.v1", thread_id: "thread-1", run_id: "run-1", source_event_id: "first-reply",
    change_scope: "recorded_file_edits", file_count: 5, additions: 5, deletions: 5, counts_available: true, partial: false, reasons: [],
    files: ["web/src/chat.tsx", "web/src/theme.css", "README.md", "long/path/with-many-directories/messages.ts", "tests/chat.test.ts"].map((path, index) => ({
      workspace_id: "workspace-1", path, operation: "replace", additions: 1, deletions: 1, counts_available: true,
      diff: `--- a/${path}\n+++ b/${path}\n@@ -1 +1 @@\n-old ${index}\n+new ${index}\n`, diff_truncated: false, edit_ids: [`edit-${index}`],
    })), ...overrides };
}

function show(get: ReturnType<typeof vi.fn>) {
  const client = { get, threadChanges: APIClient.prototype.threadChanges } as unknown as APIClient;
  const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return render(<QueryClientProvider client={queryClient}>
    <V2Narrative client={client} threadID="thread-1" entries={[{ id: "first-reply", branchSourceID: "last-reply", changeSourceID: "first-reply",
      kind: "assistant", runId: "run-1", text: "Implemented the changes.", createdAt: "", provisional: false }]} />
  </QueryClientProvider>);
}

it("shows three files, expands the rest and opens the selected net diff with keyboard focus restored", async () => {
  const user = userEvent.setup();
  const get = vi.fn().mockResolvedValue(changes());
  show(get);
  const summary = await screen.findByRole("region", { name: "Changes in this turn" });
  expect(get).toHaveBeenCalledWith("/threads/thread-1/changes", { source_event_id: "first-reply" }, expect.any(AbortSignal));
  expect(within(summary).getAllByRole("listitem")).toHaveLength(3);
  expect(within(summary).getByText("5 files edited")).toBeInTheDocument();
  await user.click(within(summary).getByRole("button", { name: "Show 2 more files" }));
  expect(within(summary).getAllByRole("listitem")).toHaveLength(5);
  const file = within(summary).getByRole("button", { name: /tests\/chat.test.ts/u });
  await user.click(file);
  const dialog = screen.getByRole("dialog", { name: "Changes in this turn" });
  expect(within(dialog).getByText("+new 4")).toBeInTheDocument();
  expect(within(dialog).queryByText("+new 0")).not.toBeInTheDocument();
  await user.click(within(dialog).getByRole("button", { name: /web\/src\/chat.tsx/u }));
  expect(within(dialog).getByText("+new 0")).toBeInTheDocument();
  await user.keyboard("{Escape}");
  expect(screen.queryByRole("dialog")).not.toBeInTheDocument();
  expect(file).toHaveFocus();
  await user.click(within(summary).getByRole("button", { name: "Show fewer files" }));
  expect(within(summary).getAllByRole("listitem")).toHaveLength(3);
});

it("hides the card for a reply with no recorded net changes", async () => {
  const get = vi.fn().mockResolvedValue(changes({ files: [], file_count: 0, additions: 0, deletions: 0 }));
  const view = show(get);
  await waitFor(() => expect(view.container.querySelector(".v2-turn-changes-slot")).toBeNull());
  expect(screen.queryByRole("region", { name: "Changes in this turn" })).not.toBeInTheDocument();
  expect(get).toHaveBeenCalledOnce();
});

it("keeps unavailable counts distinct from zero and explains hidden sensitive diffs", async () => {
  const user = userEvent.setup();
  const summary = changes();
  summary.files[0] = { ...summary.files[0], counts_available: false, diff: "", reason: "secrets_redacted" };
  summary.counts_available = false;
  summary.partial = true;
  show(vi.fn().mockResolvedValue(summary));
  const card = await screen.findByRole("region", { name: "Changes in this turn" });
  expect(within(card).getAllByText("Counts unavailable")).toHaveLength(2);
  expect(within(card).getByText("Some change details are unavailable")).toBeInTheDocument();
  await user.click(within(card).getByRole("button", { name: "View changes" }));
  expect(screen.getByText("This file contains sensitive content. Its diff and line counts are hidden.")).toBeInTheDocument();
  expect(screen.queryByLabelText("Code diff")).not.toBeInTheDocument();
});

it("rejects another reply's summary and retries without replacing the source identity", async () => {
  const user = userEvent.setup();
  const get = vi.fn().mockResolvedValueOnce(changes({ run_id: "another-run" })).mockResolvedValue(changes());
  show(get);
  await screen.findByText("Could not load this turn’s changes");
  expect(screen.queryByText("5 files edited")).not.toBeInTheDocument();
  await user.click(screen.getByRole("button", { name: "Retry" }));
  await screen.findByText("5 files edited");
  expect(get.mock.calls[1][1]).toEqual({ source_event_id: "first-reply" });
});
