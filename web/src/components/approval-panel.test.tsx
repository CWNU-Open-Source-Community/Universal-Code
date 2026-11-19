import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import type { APIClient } from "../api/client";
import { ApprovalPanel } from "./approval-panel";

describe("ApprovalPanel", () => {
  it("shows metadata only and sends an explicit approve-once decision", async () => {
    const user = userEvent.setup();
    const decideApproval = vi.fn().mockResolvedValue({
      version: "approval_control.v1", run_id: "run-1", approval_id: "approval-1",
      proposal_id: "proposal-1", tool_name: "shell", action: "approve_once",
      status: "approved", replayed: false, process_execution_enabled: false,
      shell_execution_enabled: false, docker_execution_enabled: false,
      workspace_write_applied: false, session_grant_created: false, capability_grant: false,
    });
    const client = {
      hasApprovalControl: true,
      approvalQueue: vi.fn().mockResolvedValue({
        protocol_version: "approval_queue.v1", run_id: "run-1", truncated: false,
        process_execution_enabled: false, session_grant_created: false, capability_grant: false,
        items: [{ id: "approval-1", proposal_id: "proposal-1", run_id: "run-1",
          session_id: "session-1", workspace_id: "workspace-1", tool_name: "shell",
          action_class: "shell", mode: "per_call", status: "pending",
          allowed_actions: ["approve_once", "deny"], version: 1,
          created_at: "2026-07-18T00:00:00Z", updated_at: "2026-07-18T00:00:00Z",
          process_execution_enabled: false, capability_grant: false }],
      }),
      decideApproval,
    } as unknown as APIClient;
    renderPanel(client);
    expect(await screen.findByText("Simulate command")).toBeVisible();
    expect(screen.queryByText("echo secret command")).not.toBeInTheDocument();
    expect(screen.getByText("Process execution: off")).toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: "Approve once" }));
    await waitFor(() => expect(decideApproval).toHaveBeenCalledTimes(1));
    expect(decideApproval.mock.calls[0]?.[0]).toBe("run-1");
    expect(decideApproval.mock.calls[0]?.[1]).toBe("approval-1");
    expect(decideApproval.mock.calls[0]?.[2]).toEqual({
      version: "approval_control.v1", action: "approve_once",
    });
  });
});

function renderPanel(client: APIClient) {
  const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false },
    mutations: { retry: false } } });
  return render(<QueryClientProvider client={queryClient}>
    <ApprovalPanel client={client} runID="run-1" />
  </QueryClientProvider>);
}
