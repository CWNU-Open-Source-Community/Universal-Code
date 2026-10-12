import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { APIClient } from "../api/client";
import type { AgentNodeView } from "../api/types";
import { SpecialistExecutionControl } from "./specialist-execution";

const child: AgentNodeView = {
  id: "agent-child", parent_id: "agent-root", session_id: "session-child", role: "specialist",
  profile: "review", skills: [], status: "ready", depth: 1, child_limit: 0,
  turn_limit: 4, token_limit: 2000, turns_used: 0, tokens_used: 0, version: 1,
  created_at: "2026-10-11T00:00:00Z", updated_at: "2026-10-11T00:00:00Z",
};

function mount(client: APIClient, agent = child) {
  return render(<QueryClientProvider client={new QueryClient({ defaultOptions: { queries: { retry: false } } })}>
    <SpecialistExecutionControl client={client} runID="run-1" agent={agent} runStatus="running" />
  </QueryClientProvider>);
}
function response(data: unknown) {
  return new Response(JSON.stringify({ version: "api.v1", data, request_id: "request-child" }), { status: 200, headers: { "Content-Type": "application/json" } });
}
afterEach(() => { vi.unstubAllGlobals(); sessionStorage.clear(); });

it("retains the original execution across remount after a lost response and shows durable tool receipts", async () => {
  const fetch = vi.fn().mockRejectedValueOnce(new TypeError("Connection lost"));
  vi.stubGlobal("fetch", fetch);
  const client = new APIClient("read", "/api/v1", "control");
  const user = userEvent.setup();
  const first = mount(client);
  await user.click(screen.getByRole("button", { name: "Run continuously and summarize" }));
  await screen.findByRole("alert");
  const [path, options] = fetch.mock.calls[0];
  const body = JSON.parse(options.body);
  expect(path).toBe("/api/v1/runs/run-1/agents/agent-child/continue");
  expect(body).toEqual({ version: "specialist_continuation.v1", operation_key: expect.stringMatching(/^web-specialist-continuation-/), confirm_execute: true });
  expect(options.headers).toMatchObject({ Authorization: "Bearer control", "Idempotency-Key": body.operation_key });
  first.unmount();
  fetch.mockResolvedValueOnce(response({ version: "specialist_continuation.v1", run_id: "run-1", agent_id: "agent-child", thread_id: "thread-main", schedule_id: "schedule-child",
    attempt_id: "attempt-child", attempt_ids: ["attempt-first", "attempt-child"], status: "completed", total_tokens: 20, execution_millis: 50,
    rounds_completed: 2, turns_started: 2, stop_reason: "all_terminal", summary_status: "completed", replayed: true,
  }));
  mount(client, { ...child, status: "completed" });
  expect(fetch).toHaveBeenCalledTimes(1);
  await user.click(screen.getByRole("button", { name: "Retry and read execution result" }));
  expect(await screen.findByText("Original execution result")).toBeInTheDocument();
  expect(fetch.mock.calls[1][1].body).toBe(options.body);
  expect(screen.getByText("2 child turns")).toBeInTheDocument();
  expect(screen.getByText("Main conversation summary completed")).toBeInTheDocument();
  expect(screen.getByRole("link", { name: "Open main conversation" })).toHaveAttribute("href", "#/threads/thread-main");
  expect(screen.queryByRole("button", { name: "Run continuously and summarize" })).not.toBeInTheDocument();

  fetch.mockResolvedValueOnce(response({ version: "specialist_tools.v1", rounds: [{ round: 1, model_attempt: 1,
    calls: [{ call_id: "read-1", name: "workspace_read", status: "completed", result_json: "File evidence from the child" }],
  }] }));
  await user.click(screen.getByText("Child tool receipts"));
  expect(await screen.findByText("workspace_read")).toBeInTheDocument();
  expect(fetch.mock.calls[2][0]).toBe("/api/v1/runs/run-1/agents/agent-child/attempts/attempt-child/tool-receipts");
  expect(fetch.mock.calls[2][1].headers.Authorization).toBe("Bearer read");

  fetch.mockResolvedValueOnce(response({ version: "specialist_tools.v1", rounds: [] }));
  await user.selectOptions(screen.getByRole("combobox", { name: "Child turn" }), "attempt-first");
  await screen.findByText("No tool calls in this attempt.");
  expect(fetch.mock.calls[3][0]).toBe("/api/v1/runs/run-1/agents/agent-child/attempts/attempt-first/tool-receipts");
});

it("does not offer execution without model control", () => {
  const client = new APIClient("read", "/api/v1", "control", { modelControlEnabled: false });
  mount(client);
  expect(screen.queryByRole("button", { name: "Run continuously and summarize" })).not.toBeInTheDocument();
});

it.each(["queued", "failed"])("refreshes the main conversation and retains a %s summary for a later retry", async (status) => {
  const receipt = { version: "specialist_continuation.v1", run_id: "run-1", agent_id: "agent-child", thread_id: "thread-main", schedule_id: "schedule-child",
    attempt_id: "attempt-child", attempt_ids: ["attempt-child"], status: "completed", total_tokens: 20, execution_millis: 50,
    rounds_completed: 1, turns_started: 1, stop_reason: "all_terminal", summary_status: status, replayed: false };
  const fetch = vi.fn().mockResolvedValueOnce(response(receipt));
  vi.stubGlobal("fetch", fetch);
  const queries = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  const invalidate = vi.spyOn(queries, "invalidateQueries");
  render(<QueryClientProvider client={queries}><SpecialistExecutionControl client={new APIClient("read", "/api/v1", "control")} runID="run-1" agent={child} runStatus="running" /></QueryClientProvider>);
  const user = userEvent.setup();
  await user.click(screen.getByRole("button", { name: "Run continuously and summarize" }));
  await screen.findByText(status === "queued" ? "Summary queued for the main conversation" : "Main conversation summary failed");
  expect(invalidate).toHaveBeenCalledWith({ queryKey: ["v2", "thread", "thread-main"] });
  fetch.mockResolvedValueOnce(response({ ...receipt, replayed: true, summary_status: "completed" }));
  await user.click(screen.getByRole("button", { name: "Retry and read execution result" }));
  await screen.findByText("Main conversation summary completed");
  expect(fetch.mock.calls[1][1].body).toBe(fetch.mock.calls[0][1].body);
});

it("requires explicit task resume for a paused Run", () => {
  render(<QueryClientProvider client={new QueryClient()}><SpecialistExecutionControl client={new APIClient("read", "/api/v1", "control")} runID="run-1" agent={child} runStatus="paused" /></QueryClientProvider>);
  expect(screen.getByText("The task is paused. Resume it in task controls before running the child.")).toBeInTheDocument();
  expect(screen.getByRole("button", { name: "Run continuously and summarize" })).toBeDisabled();
});
