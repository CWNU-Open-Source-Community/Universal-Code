import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import type { APIClient } from "../api/client";
import type { CodeHandoffView } from "../api/types";
import { CodeHandoffHostCommands } from "./code-handoff-host-commands";

type Command = NonNullable<CodeHandoffView["host_commands"]>["items"][number];
function recorded(): Command {
  return { proposal_id: "proposal-1", run_id: "run-1", session_id: "session-1", workspace_id: "workspace-1",
    purpose: "Run the project tests", working_directory: "D:/project", spec_fingerprint: "a".repeat(64),
    created_at: "2026-09-10T00:00:00Z", review_id: "review-1", review_decision: "approve",
    result_id: "result-1", result_status: "completed", source_ref: "host-command-proposal:proposal-1",
    content_sha256: "b".repeat(64), receipt: { request_id: "request-1", exit_code: 0, timed_out: false,
      cancelled: false, stdout_truncated: false, stderr_truncated: false, output_limit_exceeded: false,
      tree_reaped: true, non_sandboxed: true, started_at: "2026-09-10T00:01:00Z", completed_at: "2026-09-10T00:02:00Z" } };
}
function detail(command: Command) {
  return { id: command.proposal_id, run_id: command.run_id, session_id: command.session_id,
    workspace_id: command.workspace_id, spec_fingerprint: command.spec_fingerprint,
    result: { id: command.result_id, source_ref: command.source_ref, content_sha256: command.content_sha256 },
    receipt: command.receipt, untrusted_evidence: "Saved test output: 4 checks completed" };
}
function show(items: Command[], hostCommandProposal = vi.fn()) {
  const client = {  hostCommandProposal } as unknown as APIClient;
  render(<QueryClientProvider client={new QueryClient({ defaultOptions: { queries: { retry: false } } })}>
    <CodeHandoffHostCommands client={client} commands={{ items, truncated: false }} />
  </QueryClientProvider>);
}

it("separates exit facts and unexecuted proposals without inferring current verification", async () => {
  const command = recorded();
  const failed = { ...command, proposal_id: "proposal-2", purpose: "A failing check", result_id: "result-2",
    result_status: "failed", receipt: { ...command.receipt!, exit_code: 7, stderr_truncated: true } };
  const pending: Command = { proposal_id: "proposal-3", run_id: command.run_id, session_id: command.session_id,
    workspace_id: command.workspace_id, purpose: "Not executed", working_directory: command.working_directory,
    spec_fingerprint: command.spec_fingerprint, created_at: command.created_at };
  const read = vi.fn().mockResolvedValue(detail(command));
  show([command, failed, pending], read);
  expect(screen.getByText(/Exit code 0/)).toBeInTheDocument();
  expect(screen.getByText(/Exit code 7/)).toBeInTheDocument();
  expect(screen.getByText(/Output reached the storage limit/)).toBeInTheDocument();
  expect(screen.getByText(/command result needs confirmation/)).toBeInTheDocument();
  expect(screen.queryByText(/^passed$/i)).not.toBeInTheDocument();
  expect(read).not.toHaveBeenCalled();
  await userEvent.setup().click(screen.getAllByRole("button", { name: "Read saved output" })[0]);
  expect(await screen.findByText("Saved test output: 4 checks completed")).toBeInTheDocument();
  expect(read).toHaveBeenCalledWith("run-1", "proposal-1", expect.any(AbortSignal));
});

it("retries only output reading and rejects a different result identity", async () => {
  const command = recorded();
  const read = vi.fn().mockRejectedValueOnce(new Error("saved output temporarily unavailable"))
    .mockResolvedValueOnce({ ...detail(command), run_id: "another-run", untrusted_evidence: "WRONG RUN OUTPUT" })
    .mockResolvedValueOnce(detail(command));
  show([command], read);
  const user = userEvent.setup();
  await user.click(screen.getByRole("button", { name: "Read saved output" }));
  await screen.findByText(/saved output temporarily unavailable/);
  await user.click(screen.getByRole("button", { name: "Retry reading output" }));
  await screen.findByText("Saved output does not match this command receipt.");
  expect(screen.queryByText("WRONG RUN OUTPUT")).not.toBeInTheDocument();
  await user.click(screen.getByRole("button", { name: "Retry reading output" }));
  await screen.findByText("Saved test output: 4 checks completed");
  await waitFor(() => expect(read).toHaveBeenCalledTimes(3));
  expect(read.mock.calls.every(([runID, proposalID]) => runID === "run-1" && proposalID === "proposal-1")).toBe(true);
});

it.each(["UX_Q_HOST_ACTUAL_OUTPUT \uFFFD", "UX_Q_HOST_ACTUAL_OUTPUT 中文"])(
  "preserves the exact saved output and conditionally explains decoding damage: %s", async (output) => {
    const command = { ...recorded(), result_status: "failed",
      receipt: { ...recorded().receipt!, exit_code: 7 } };
    const read = vi.fn().mockResolvedValue({ ...detail(command), untrusted_evidence: output });
    show([command], read);
    await userEvent.setup().click(screen.getByRole("button", { name: "Read saved output" }));
    const saved = await screen.findByText(output);
    expect(saved.tagName).toBe("PRE");
    expect(saved.textContent).toBe(output);
    const notice = screen.queryByText("Some characters could not be decoded correctly; the displayed text may be incomplete.");
    expect(Boolean(notice)).toBe(output.includes("\uFFFD"));
    expect(screen.getByText(/Exit code 7/)).toBeInTheDocument();
    expect(read).toHaveBeenCalledTimes(1);
  });
