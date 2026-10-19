import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { cleanup, render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, expect, it, vi } from "vitest";
import type { APIClient } from "../../api/client";
import type { ThreadChangeSummary, ThreadChangeUndo } from "../../api/thread-changes";
import { TurnChangeUndo } from "./turn-change-undo";

afterEach(cleanup);

const summary: ThreadChangeSummary = {
  version: "thread_change_summary.v1", thread_id: "thread-1", run_id: "run-1", source_event_id: "reply-1",
  change_scope: "recorded_file_edits", file_count: 2, additions: 2, deletions: 2,
  counts_available: true, partial: false, reasons: [], files: [],
};
function result(overrides: Partial<ThreadChangeUndo> = {}): ThreadChangeUndo {
  return { version: "thread_change_undo.v1", thread_id: "thread-1", run_id: "run-1", source_event_id: "reply-1",
    state: "available", conflicts: [], restored_paths: [], remaining_paths: ["README.md", "app.ts"], replayed: false, ...overrides };
}
function show(undoThreadChanges: ReturnType<typeof vi.fn>, undo?: ThreadChangeUndo) {
  const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false }, mutations: { retry: false } } });
  const key = ["thread", "thread-1", "turn-changes", "reply-1"];
  queryClient.setQueryData(key, summary);
  render(<QueryClientProvider client={queryClient}>
    <TurnChangeUndo client={{ undoThreadChanges } as unknown as APIClient} summary={{ ...summary, undo }} />
  </QueryClientProvider>);
  return { queryClient, key };
}

it("previews only on demand and requires confirmation before undoing the exact reply", async () => {
  const user = userEvent.setup();
  const api = vi.fn().mockResolvedValueOnce(result()).mockResolvedValueOnce(result({ state: "completed", restored_paths: ["README.md", "app.ts"], remaining_paths: [] }));
  const { queryClient, key } = show(api);
  expect(api).not.toHaveBeenCalled();
  await user.click(screen.getByRole("button", { name: "Undo this turn’s changes" }));
  const confirm = await screen.findByRole("button", { name: "Confirm undo" });
  expect(api).toHaveBeenCalledExactlyOnceWith("thread-1", "run-1", "reply-1", false, expect.any(AbortSignal));
  await user.click(confirm);
  await screen.findByRole("heading", { name: "This turn’s changes were undone" });
  expect(api).toHaveBeenLastCalledWith("thread-1", "run-1", "reply-1", true);
  expect(queryClient.getQueryData<ThreadChangeSummary>(key)?.undo?.state).toBe("completed");
  await user.click(within(screen.getByRole("dialog")).getAllByRole("button", { name: "Close" })[1]);
  const completed = screen.getByRole("button", { name: "This turn’s changes were undone" });
  expect(completed).toHaveAttribute("aria-disabled", "true");
  expect(completed).toHaveFocus();
  await user.click(completed);
  expect(screen.queryByRole("dialog")).not.toBeInTheDocument();
  expect(api).toHaveBeenCalledTimes(2);
});

it("cancels without applying and returns keyboard focus to the card", async () => {
  const user = userEvent.setup();
  const api = vi.fn().mockResolvedValue(result());
  show(api);
  const trigger = screen.getByRole("button", { name: "Undo this turn’s changes" });
  await user.click(trigger);
  await screen.findByRole("button", { name: "Confirm undo" });
  await user.keyboard("{Escape}");
  expect(screen.queryByRole("dialog")).not.toBeInTheDocument();
  expect(trigger).toHaveFocus();
  expect(api.mock.calls.every((call) => call[3] === false)).toBe(true);
});

it("shows the conflicting file and does not offer confirmation for later edits", async () => {
  const user = userEvent.setup();
  const api = vi.fn().mockResolvedValue(result({ state: "conflict", conflicts: [{ path: "app.ts", reason: "files_changed" }] }));
  show(api);
  await user.click(screen.getByRole("button", { name: "Undo this turn’s changes" }));
  await screen.findByText("app.ts");
  expect(screen.getByText("This file changed after this turn. Review its current content first.")).toBeInTheDocument();
  expect(screen.queryByRole("button", { name: "Confirm undo" })).not.toBeInTheDocument();
  expect(api.mock.calls.every((call) => call[3] === false)).toBe(true);
});

it("checks a lost response without applying the undo a second time", async () => {
  const user = userEvent.setup();
  const api = vi.fn().mockResolvedValueOnce(result()).mockRejectedValueOnce(new Error("Connection lost"))
    .mockResolvedValueOnce(result({ state: "completed", restored_paths: ["README.md", "app.ts"], remaining_paths: [], replayed: true }));
  show(api);
  await user.click(screen.getByRole("button", { name: "Undo this turn’s changes" }));
  await user.click(await screen.findByRole("button", { name: "Confirm undo" }));
  await screen.findByRole("alert");
  await user.click(screen.getByRole("button", { name: "Check again" }));
  await screen.findByRole("heading", { name: "This turn’s changes were undone" });
  expect(api.mock.calls.map((call) => call[3])).toEqual([false, true, false]);
});

it("keeps completed undo state after reloading the summary", () => {
  const api = vi.fn();
  show(api, result({ state: "completed", restored_paths: ["README.md", "app.ts"], remaining_paths: [] }));
  expect(screen.getByRole("button", { name: "This turn’s changes were undone" })).toHaveAttribute("aria-disabled", "true");
  expect(api).not.toHaveBeenCalled();
});

it("reports partial progress without claiming the entire turn was undone", async () => {
  const user = userEvent.setup();
  const api = vi.fn().mockResolvedValueOnce(result()).mockResolvedValueOnce(result({ state: "partial",
    restored_paths: ["README.md"], remaining_paths: ["app.ts"], conflicts: [{ path: "app.ts", reason: "restore_failed" }] }));
  show(api);
  await user.click(screen.getByRole("button", { name: "Undo this turn’s changes" }));
  await user.click(await screen.findByRole("button", { name: "Confirm undo" }));
  await screen.findByText("Some files were restored. The remaining files have not been undone.");
  expect(screen.getByText("1 path restored")).toBeInTheDocument();
  expect(screen.getByText("1 path remaining")).toBeInTheDocument();
  expect(screen.queryByRole("heading", { name: "This turn’s changes were undone" })).not.toBeInTheDocument();
  await waitFor(() => expect(screen.getByRole("button", { name: "Check again" })).toBeEnabled());
});
