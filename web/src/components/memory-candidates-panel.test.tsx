import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import type { APIClient } from "../api/client";
import type { MemoryCandidateView } from "../api/types";
import { MemoryCandidatesPanel } from "./memory-candidates-panel";

describe("MemoryCandidatesPanel", () => {
  it("requires explicit review and confirms the displayed source version, then refreshes project memory", async () => {
    const user = userEvent.setup();
    const candidate = candidateView();
    const get = vi.fn().mockResolvedValue([candidate]);
    const postControl = vi.fn().mockImplementation(() => {
      get.mockResolvedValue([]);
      return Promise.resolve({ candidate: { ...candidate, status: "confirmed", content: undefined },
        memory_id: "memory-1", replayed: false });
    });
    const onOpenRun = vi.fn();
    const { queryClient } = renderPanel({ hasControl: true, get, postControl } as unknown as APIClient, onOpenRun);
    const invalidate = vi.spyOn(queryClient, "invalidateQueries");
    expect(await screen.findByText("Use the stable API")).toBeVisible();
    expect(postControl).not.toHaveBeenCalled();
    expect(screen.getByText(candidate.content!)).not.toBeVisible();
    await user.click(screen.getByText("Use the stable API"));
    expect(screen.getByText(candidate.content!)).toBeVisible();
    expect(screen.getByText(/Task or plan completion still needs separate verification/)).toBeVisible();
    expect(screen.getByText("note-1")).toBeVisible();
    await user.click(screen.getByRole("button", { name: "source-run" }));
    expect(onOpenRun).toHaveBeenCalledWith("source-run");
    await user.click(screen.getByRole("button", { name: "Confirm and save to project" }));
    expect(postControl).toHaveBeenCalledExactlyOnceWith("/memory-candidates/candidate-1/confirm", {
      workspace_id: "workspace-1", expected_version: 1,
      expected_fingerprint: "a".repeat(64), expected_source_version: 3, confirm: true,
    }, expect.stringMatching(/^web-memory-candidate-/));
    await screen.findByText("No pending experience candidates.");
    expect(invalidate).toHaveBeenCalledWith({ queryKey: ["context-memories", "project", "workspace-1"] });
  });

  it("dismisses without confirming and keeps controls read-only without a control token", async () => {
    const user = userEvent.setup();
    const get = vi.fn().mockResolvedValue([candidateView()]);
    const postControl = vi.fn().mockResolvedValue({ candidate: { ...candidateView(), status: "rejected" }, replayed: false });
    const { rerender, queryClient } = renderPanel({ hasControl: false, get, postControl } as unknown as APIClient);
    await user.click(await screen.findByText("Use the stable API"));
    expect(screen.getByRole("button", { name: "Confirm and save to project" })).toBeDisabled();
    expect(screen.getByRole("button", { name: "Dismiss candidate" })).toBeDisabled();
    expect(postControl).not.toHaveBeenCalled();
    rerender(<QueryClientProvider client={queryClient}><MemoryCandidatesPanel
      client={{ hasControl: true, get, postControl } as unknown as APIClient} workspaceID="workspace-1" />
    </QueryClientProvider>);
    await user.click(screen.getByRole("button", { name: "Dismiss candidate" }));
    expect(postControl).toHaveBeenCalledWith("/memory-candidates/candidate-1/reject", {
      workspace_id: "workspace-1", expected_version: 1,
      expected_fingerprint: "a".repeat(64), expected_source_version: 3,
    }, expect.stringMatching(/^web-memory-candidate-/));
  });

  it("refreshes changed source content after a conflict without carrying over confirmation", async () => {
    const user = userEvent.setup();
    const candidate = candidateView();
    const get = vi.fn().mockResolvedValue([candidate]);
    const postControl = vi.fn().mockImplementation(() => {
      get.mockResolvedValue([{ ...candidate, content: "The corrected conclusion.", fingerprint: "b".repeat(64),
        source: { ...candidate.source, note_version: 4 } }]);
      return Promise.reject(new Error("Source changed. Review it again."));
    });
    renderPanel({ hasControl: true, get, postControl } as unknown as APIClient);
    await user.click(await screen.findByText(candidate.title!));
    await user.click(screen.getByRole("button", { name: "Confirm and save to project" }));
    expect(await screen.findByText("The corrected conclusion.")).toBeVisible();
    expect(within(screen.getByRole("alert")).getByText(/Source changed/)).toBeVisible();
    expect(postControl).toHaveBeenCalledTimes(1);
    expect(screen.queryByText(candidate.content!)).not.toBeInTheDocument();
  });

  it("does not expose resolved or other-workspace content in the pending queue", async () => {
    const get = vi.fn().mockResolvedValue([
      { ...candidateView(), workspace_id: "workspace-other" },
      { ...candidateView(), id: "resolved", status: "confirmed" },
    ]);
    const postControl = vi.fn();
    renderPanel({ hasControl: true, get, postControl } as unknown as APIClient);
    await screen.findByText("No pending experience candidates.");
    expect(screen.queryByText("Use the stable API")).not.toBeInTheDocument();
    expect(postControl).not.toHaveBeenCalled();
    await waitFor(() => expect(get).toHaveBeenCalledWith("/memory-candidates", {
      workspace_id: "workspace-1", include_resolved: false, limit: 50,
    }, expect.any(AbortSignal)));
  });
});

function renderPanel(client: APIClient, onOpenRun?: (runID: string) => void) {
  const queryClient = new QueryClient({ defaultOptions: {
    queries: { retry: false }, mutations: { retry: false },
  } });
  return { queryClient, ...render(<QueryClientProvider client={queryClient}>
    <MemoryCandidatesPanel client={client} workspaceID="workspace-1" onOpenRun={onOpenRun} />
  </QueryClientProvider>) };
}

function candidateView(): MemoryCandidateView {
  return {
    id: "candidate-1", protocol_version: "context_memory_candidate.v1", workspace_id: "workspace-1",
    title: "Use the stable API", content: "The experimental endpoint is unavailable; use the stable API.",
    fingerprint: "a".repeat(64), status: "pending", version: 1,
    source: { run_id: "source-run", note_id: "note-1", note_version: 3, category: "decision",
      references: ["docs/api.md"], evidence_ids: ["tool-call-1"], updated_at: "2026-10-16T00:00:00Z",
      completion_kind: "thread_reply", completion_ref: "event:reply-1", completion_fingerprint: "c".repeat(64) },
    confidence: "operator_review_required", confidence_reason: "Review the original note before reuse.", redacted: false,
    created_at: "2026-10-16T00:01:00Z", updated_at: "2026-10-16T00:01:00Z",
  };
}
