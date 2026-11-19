import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import type { APIClient } from "../api/client";
import { EvidenceInventory } from "./evidence-inventory";

describe("EvidenceInventory", () => {
  it("shows metadata only and opens the Go-issued source reference", async () => {
    const digest = "c".repeat(64);
    const evidenceInventory = vi.fn().mockResolvedValue({
      protocol_version: "session_evidence_inventory.v1", run_id: "run-1", truncated: false,
      items: [{ attachment_id: "evidence-1", run_id: "run-1", session_id: "session-1",
        workspace_id: "workspace-1", source_kind: "workspace_file", source_ref: "README.md",
        content_sha256: digest, instruction_authorized: false,
        attached_at: "2026-07-19T11:00:00Z" }],
    });
    const client = { evidenceInventory } as unknown as APIClient;
    const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    const onOpenSource = vi.fn();
    const user = userEvent.setup();
    render(<QueryClientProvider client={queryClient}>
      <EvidenceInventory client={client} onOpenSource={onOpenSource} runID="run-1" />
    </QueryClientProvider>);

    expect(await screen.findByText("README.md")).toBeInTheDocument();
    expect(screen.getByText(digest)).toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: "Open current file README.md" }));
    expect(onOpenSource).toHaveBeenCalledWith("README.md");
    expect(screen.queryByText(/session message|attached by|operation key/i)).not.toBeInTheDocument();
  });
});
