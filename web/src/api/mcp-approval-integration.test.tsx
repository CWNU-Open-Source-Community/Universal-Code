import type { ReactElement } from "react";
import type { RenderOptions } from "@testing-library/react";
import { LocaleProvider } from "../lib/locale";
import { readFileSync } from "node:fs";
import { join } from "node:path";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { cleanup, render as renderComponent, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, describe, expect, it, vi } from "vitest";
import { APIClient } from "./client";
import { V2ApprovalCards } from "../v2/components/approval-cards";

afterEach(() => { cleanup(); vi.unstubAllGlobals(); });

function evidence(mode: string) {
  if (process.env.UC_MCP_HTTP_EVIDENCE) {
    return JSON.parse(readFileSync(join(process.env.UC_MCP_HTTP_EVIDENCE, `mcp-approval-http-${mode}.json`), "utf8"));
  }
  const identity = { run_id: "run-mcp", approval_id: "approval-mcp", proposal_id: "call-mcp", tool_name: "mcp_tool_call" };
  return {
    queue: { protocol_version: "approval_queue.v1", run_id: identity.run_id,
      process_execution_enabled: false, session_grant_created: false, capability_grant: false, truncated: false,
      items: [{ ...identity, id: identity.approval_id, approval_id: undefined, session_id: "session-mcp", workspace_id: "workspace-mcp",
        action_class: "mcp_server_and_tool", mode: "per_call", status: "pending", version: 1,
        allowed_actions: ["approve_once", "deny"], process_execution_enabled: false, capability_grant: false,
        created_at: "2026-10-02T00:00:00Z", updated_at: "2026-10-02T00:00:00Z" }] },
    preview: { ...identity, protocol_version: "approval_queue.v1", workspace_id: "workspace-mcp", effect: "mcp_server_and_tool",
      working_directory: "", fields: [{ name: "arguments", value: '{"query":"exact review intent"}' }],
      source_current: true, redacted: false, truncated: false },
    decision: { ...identity, version: "approval_control.v1", action: "approve_once", status: "approved", replayed: false,
      process_execution_enabled: false, shell_execution_enabled: false, docker_execution_enabled: false,
      workspace_write_applied: false, session_grant_created: false, capability_grant: false,
      execution_resumed: false, retry_completed: false, retry_scheduled: false,
      continuation: { state: "completed", replayed: false, model_called: true, tool_called: true } },
    peer_tool_calls: 1, model_calls: 2,
  };
}

describe("MCP approval through the strict HTTP client", () => {
  it.each(["ask", "auto"])("renders %s evidence and resumes the exact approved call", async (mode) => {
    const data = evidence(mode);
    expect(data.peer_tool_calls).toBe(1);
    expect(data.model_calls).toBe(2);
    let decided = false;
    const fetch = vi.fn(async (url: unknown, init?: RequestInit) => {
      const path = String(url);
      if (init?.method === "POST") decided = true;
      const body = path.endsWith("/preview") ? data.preview : path.endsWith("/decision") ? data.decision
        : { ...data.queue, items: decided ? [] : data.queue.items };
      return new Response(JSON.stringify({ version: "api.v1", request_id: "mcp-evidence", data: body }),
        { status: path.endsWith("/decision") ? 202 : 200, headers: { "Content-Type": "application/json" } });
    });
    vi.stubGlobal("fetch", fetch);
    render(<QueryClientProvider client={new QueryClient({ defaultOptions: { queries: { retry: false }, mutations: { retry: false } } })}>
      <V2ApprovalCards client={new APIClient("read", "/api/v1", "control")} runID={data.preview.run_id} threadID="thread-mcp" />
    </QueryClientProvider>);
    expect(await screen.findByText(/exact review intent/)).toBeInTheDocument();
    expect(screen.getByText(/外部影响仍待核实，请核对服务配置与参数/)).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "允许本对话" })).not.toBeInTheDocument();
    await userEvent.click(screen.getByRole("button", { name: "批准一次" }));
    expect(await screen.findByText(/Agent 已继续处理/)).toBeInTheDocument();
    await waitFor(() => expect(fetch.mock.calls.filter(([, init]) => init?.method === "POST")).toHaveLength(1));
    const post = fetch.mock.calls.find(([, init]) => init?.method === "POST")!;
    expect(String(post[0])).toBe(`/api/v1/runs/${data.preview.run_id}/approvals/${data.preview.approval_id}/decision`);
    expect(JSON.parse(String(post[1]?.body))).toEqual({ version: "approval_control.v1", action: "approve_once" });
  });

  it("rejects another effect or unrelated operation continuation", async () => {
    const data = evidence("ask");
    vi.stubGlobal("fetch", vi.fn(async () => new Response(JSON.stringify({ version: "api.v1", request_id: "mcp-wrong-effect",
      data: { ...data.preview, effect: "dry_run" } }), { headers: { "Content-Type": "application/json" } })));
    const client = new APIClient("read", "/api/v1", "control");
    await expect(client.approvalPreview(data.preview.run_id, data.preview.approval_id)).rejects.toThrow("different effect");
    vi.stubGlobal("fetch", vi.fn(async () => new Response(JSON.stringify({ version: "api.v1", request_id: "mcp-wrong-tool",
      data: { ...data.decision, tool_name: "shell" } }), { status: 202, headers: { "Content-Type": "application/json" } })));
    await expect(client.decideApproval(data.preview.run_id, data.preview.approval_id,
      { version: "approval_control.v1", action: "approve_once" }, "mcp-wrong-tool-key")).rejects.toThrow("another operation");
  });
});

function render(ui: ReactElement, options?: RenderOptions) {
 localStorage.setItem("prayu.locale.v1", "zh-CN");
 const Wrapper=options?.wrapper;
 return renderComponent(ui, {...options, wrapper: ({children}) => <LocaleProvider>{Wrapper ? <Wrapper>{children}</Wrapper> : children}</LocaleProvider>});
}
