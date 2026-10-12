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
    <SpecialistExecutionControl client={client} runID="run-1" agent={agent} />
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
  await user.click(screen.getByRole("button", { name: "Run child agent" }));
  await screen.findByRole("alert");
  const [path, options] = fetch.mock.calls[0];
  const body = JSON.parse(options.body);
  expect(path).toBe("/api/v1/runs/run-1/agents/agent-child/execute");
  expect(body).toEqual({ version: "specialist_tools.v1", operation_key: expect.stringMatching(/^web-specialist-/), workspace_tools: true, confirm_execute: true });
  expect(options.headers).toMatchObject({ Authorization: "Bearer control", "Idempotency-Key": body.operation_key });
  first.unmount();
  fetch.mockResolvedValueOnce(response({ version: "specialist_tools.v1", run_id: "run-1", agent_id: "agent-child",
    attempt_id: "attempt-child", status: "finished", usage: { input_tokens: 12, output_tokens: 8, total_tokens: 20, execution_millis: 50 },
    model_attempts: 2, tool_calls: 1, tool_rounds: 1, replayed: true,
  }));
  mount(client, { ...child, status: "completed" });
  expect(fetch).toHaveBeenCalledTimes(1);
  await user.click(screen.getByRole("button", { name: "Retry and read execution result" }));
  expect(await screen.findByText("Original execution result")).toBeInTheDocument();
  expect(fetch.mock.calls[1][1].body).toBe(options.body);
  expect(screen.getByText("1 tool calls · 1 rounds")).toBeInTheDocument();
  expect(screen.queryByRole("button", { name: "Run child agent" })).not.toBeInTheDocument();

  fetch.mockResolvedValueOnce(response({ version: "specialist_tools.v1", rounds: [{ round: 1, model_attempt: 1,
    calls: [{ call_id: "read-1", name: "workspace_read", status: "completed", result_json: "File evidence from the child" }],
  }] }));
  await user.click(screen.getByText("Child tool receipts"));
  expect(await screen.findByText("workspace_read")).toBeInTheDocument();
  expect(fetch.mock.calls[2][0]).toBe("/api/v1/runs/run-1/agents/agent-child/attempts/attempt-child/tool-receipts");
  expect(fetch.mock.calls[2][1].headers.Authorization).toBe("Bearer read");
});

it("does not offer execution without model control", () => {
  const client = new APIClient("read", "/api/v1", "control", { modelControlEnabled: false });
  mount(client);
  expect(screen.queryByRole("button", { name: "Run child agent" })).not.toBeInTheDocument();
});
