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
import type { ApprovalDecisionControlView, ApprovalPreviewView, ApprovalQueueView } from "./types";
import { V2ApprovalCards } from "../v2/components/approval-cards";

afterEach(() => { cleanup(); vi.unstubAllGlobals(); });

type CommandApprovalEvidence = {
  queue: ApprovalQueueView; preview: ApprovalPreviewView; decision: ApprovalDecisionControlView;
  process_dispatches: number; model_calls: number;
};

function evidence(mode: string): CommandApprovalEvidence {
  if (process.env.UC_COMMAND_HTTP_EVIDENCE) {
    return JSON.parse(readFileSync(join(process.env.UC_COMMAND_HTTP_EVIDENCE, `command-approval-http-${mode}.json`), "utf8"));
  }
  const identity = { run_id: "run-command", approval_id: "approval-command", proposal_id: "call-command", tool_name: "command_runtime" };
  const { approval_id, ...proposal } = identity;
  return {
    queue: { protocol_version: "approval_queue.v1", run_id: identity.run_id,
      process_execution_enabled: false, session_grant_created: false, capability_grant: false, truncated: false,
      items: [{ ...proposal, id: approval_id, session_id: "session-command", workspace_id: "workspace-command",
        action_class: "command_process", mode: "per_call", status: "pending", version: 1,
        allowed_actions: ["approve_once", "deny"], process_execution_enabled: false, capability_grant: false,
        created_at: "2026-10-02T00:00:00Z", updated_at: "2026-10-02T00:00:00Z" }] },
    preview: { ...identity, protocol_version: "approval_queue.v1", workspace_id: "workspace-command", effect: "command_process",
      working_directory: "", fields: [{ name: "arguments", value: '{"query":"exact review intent"}' }],
      source_current: true, redacted: false, truncated: false },
    decision: { ...identity, version: "approval_control.v1", action: "approve_once", status: "approved", replayed: false,
      process_execution_enabled: false, shell_execution_enabled: false, docker_execution_enabled: false,
      workspace_write_applied: false, session_grant_created: false, capability_grant: false,
      execution_resumed: false, retry_completed: false, retry_scheduled: false,
      continuation: { state: "completed", replayed: false, model_called: true, tool_called: true } },
    process_dispatches: 1, model_calls: 2,
  };
}

function activeGrantEvidence() {
  const data = evidence("ask");
  if (data.preview.fields.some(({ name }) => name === "grant_expires_at")) return data;
  const reviewScope = { risk_kinds: ["other_high_risk"], other_risk_reason: "Review the local command" };
  const intent = { version: "command-runtime.v2", action: "run", failure_policy: "fail_fast", max_bytes: 4096, review_scope: reviewScope,
    commands: [{ version: "command-runtime.v2", profile: "powershell", script: "Write-Output 'next command'", working_directory: ".",
      environment: [], stdin_policy: "closed", close_initial_stdin: true, timeout_milliseconds: 1000,
      output: { inline_bytes: 4096, artifact_bytes: 4096 }, network: "disabled", credentials: "none", purpose: "Review the local command" }] };
  const expires = new Date(Date.now() + 211_000).toISOString();
  data.queue.items[0].allowed_actions = ["approve_once", "approve_for_run", "deny"];
  data.preview.fields = [
    { name: "action", value: "run" }, { name: "intent", value: JSON.stringify(intent) }, { name: "review_scope", value: JSON.stringify(reviewScope) },
    { name: "bounded_review", value: "Each new command requires explicit review." }, { name: "grant_ttl_seconds", value: "211" },
    { name: "grant_max_uses", value: "3" }, { name: "grant_uses_remaining", value: "3" }, { name: "grant_expires_at", value: expires },
    { name: "summary", value: "Approve this command only." },
  ];
  data.decision = { ...data.decision, action: "approve_for_run", session_grant_created: false,
    bounded_grant: { id: "existing-grant", scope_fingerprint: "a".repeat(64), ttl_seconds: 211, max_uses: 3,
      uses_remaining: 2, use_ordinal: 1, expires_at: expires, each_command_requires_review: true } };
  return data;
}

function renderCommandApproval(data: CommandApprovalEvidence) {
  let decided = false;
  const fetch = vi.fn(async (url: unknown, init?: RequestInit) => {
    const path = String(url);
    if (init?.method === "POST") decided = true;
    const body = path.endsWith("/preview") ? data.preview : path.endsWith("/decision") ? data.decision
      : { ...data.queue, items: decided ? [] : data.queue.items };
    return new Response(JSON.stringify({ version: "api.v1", request_id: "command-evidence", data: body }),
      { status: path.endsWith("/decision") ? 202 : 200, headers: { "Content-Type": "application/json" } });
  });
  vi.stubGlobal("fetch", fetch);
  render(<QueryClientProvider client={new QueryClient({ defaultOptions: { queries: { retry: false }, mutations: { retry: false } } })}>
    <V2ApprovalCards client={new APIClient("read", "/api/v1", "control")} runID={data.preview.run_id} threadID="thread-command" />
  </QueryClientProvider>);
  return fetch;
}

describe("Command approval through the strict HTTP client", () => {
  it.each(["ask", "auto", "full"])("renders %s evidence and resumes the exact approved call", async (mode) => {
    const data = evidence(mode);
    expect(data.process_dispatches).toBe(1);
    expect(data.model_calls).toBe(2);
    const fetch = renderCommandApproval(data);
    const allow = await screen.findByRole("button", { name: "允许一次" });
    await waitFor(() => expect(allow).toBeEnabled());
    await userEvent.click(screen.getByRole("button", { name: "审批选项" }));
    await userEvent.click(screen.getByRole("menuitem", { name: "请求详情" }));
    expect(screen.getAllByText(/exact review intent/)[0]).toBeVisible();
    expect(screen.getByText(/可能访问宿主文件和网络/)).toBeVisible();
    expect(screen.queryByRole("button", { name: "允许本对话" })).not.toBeInTheDocument();
    await userEvent.click(allow);
    expect(await screen.findByText(/Agent 已继续处理/)).toBeInTheDocument();
    await waitFor(() => expect(fetch.mock.calls.filter(([, init]) => init?.method === "POST")).toHaveLength(1));
    const post = fetch.mock.calls.find(([, init]) => init?.method === "POST")!;
    expect(String(post[0])).toBe(`/api/v1/runs/${data.preview.run_id}/approvals/${data.preview.approval_id}/decision`);
    expect(JSON.parse(String(post[1]?.body))).toEqual({ version: "approval_control.v1", action: "approve_once" });
  });

  it("confirms the next command within an existing grant using its original limits", async () => {
    const data = activeGrantEvidence();
    const fetch = renderCommandApproval(data);
    const user = userEvent.setup();
    await user.click(await screen.findByRole("button", { name: "审批选项" }));
    const limits = screen.getByRole("menuitem", { name: "本次执行的限时、限次审批" });
    await waitFor(() => expect(limits).toBeEnabled());
    await user.click(limits);
    const duration = screen.getByRole("spinbutton", { name: "有界审批有效秒数" });
    const uses = screen.getByRole("spinbutton", { name: "有界审批命令次数" });
    expect(duration).toHaveValue(211);
    expect(uses).toHaveValue(3);
    expect(duration).toBeDisabled();
    expect(uses).toBeDisabled();
    expect(fetch.mock.calls.some(([, init]) => init?.method === "POST")).toBe(false);
    await user.click(screen.getByRole("button", { name: "确认本条命令并计入有界审批" }));
    expect(await screen.findByText(/Agent 已继续处理/)).toBeVisible();
    const posts = fetch.mock.calls.filter(([, init]) => init?.method === "POST");
    expect(posts).toHaveLength(1);
    expect(String(posts[0][0])).toBe(`/api/v1/runs/${data.preview.run_id}/approvals/${data.preview.approval_id}/decision`);
    expect(JSON.parse(String(posts[0][1]?.body))).toEqual({ version: "approval_control.v1", action: "approve_for_run", grant_ttl_seconds: 211, grant_max_uses: 3 });
  });

  it("only accepts the extra preview field for the complete current command grant", async () => {
    const { preview } = activeGrantEvidence();
    const unexpected = { name: "extra", value: "unexpected" };
    const invalid = [
      { ...preview, fields: [...preview.fields, unexpected] },
      { ...preview, tool_name: "mcp_tool_call", effect: "mcp_server_and_tool" },
      { ...preview, fields: [...preview.fields.slice(0, -1), unexpected] },
    ];
    const client = new APIClient("read", "/api/v1", "control");
    for (const data of invalid) {
      vi.stubGlobal("fetch", vi.fn(async () => new Response(JSON.stringify({ version: "api.v1", request_id: "extra-preview-field", data }),
        { headers: { "Content-Type": "application/json" } })));
      await expect(client.approvalPreview(preview.run_id, preview.approval_id)).rejects.toThrow(/approval preview/i);
    }
  });

  it("rejects another effect or unrelated operation continuation", async () => {
    const data = evidence("ask");
    vi.stubGlobal("fetch", vi.fn(async () => new Response(JSON.stringify({ version: "api.v1", request_id: "command-wrong-effect",
      data: { ...data.preview, effect: "dry_run" } }), { headers: { "Content-Type": "application/json" } })));
    const client = new APIClient("read", "/api/v1", "control");
    await expect(client.approvalPreview(data.preview.run_id, data.preview.approval_id)).rejects.toThrow("different effect");
    vi.stubGlobal("fetch", vi.fn(async () => new Response(JSON.stringify({ version: "api.v1", request_id: "command-wrong-tool",
      data: { ...data.decision, tool_name: "shell" } }), { status: 202, headers: { "Content-Type": "application/json" } })));
    await expect(client.decideApproval(data.preview.run_id, data.preview.approval_id,
      { version: "approval_control.v1", action: "approve_once" }, "command-wrong-tool-key")).rejects.toThrow("another operation");
  });
});

function render(ui: ReactElement, options?: RenderOptions) {
 localStorage.setItem("prayu.locale.v1", "zh-CN");
 const Wrapper=options?.wrapper;
 return renderComponent(ui, {...options, wrapper: ({children}) => <LocaleProvider>{Wrapper ? <Wrapper>{children}</Wrapper> : children}</LocaleProvider>});
}
