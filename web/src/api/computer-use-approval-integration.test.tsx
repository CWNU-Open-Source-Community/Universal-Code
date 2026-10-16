import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { cleanup, render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, describe, expect, it, vi } from "vitest";
import { APIClient } from "./client";
import type { ApprovalPreviewView, ApprovalQueueItemView } from "./types";
import { ApprovalPanel } from "../components/approval-panel";
import { LocaleProvider } from "../lib/locale";
import { V2ApprovalCards } from "../v2/components/approval-cards";

afterEach(() => { cleanup(); vi.unstubAllGlobals(); window.localStorage.clear(); });

function approval() {
  const item: ApprovalQueueItemView = {
    id: "approval-app", proposal_id: "call-app", run_id: "run-app", session_id: "session-app",
    workspace_id: "workspace-app", tool_name: "mcp_tool_call", action_class: "mcp_server_and_tool",
    mode: "per_call", status: "pending", version: 1,
    allowed_actions: ["approve_once", "approve_for_run_app", "deny"],
    process_execution_enabled: false, capability_grant: false,
    created_at: "2026-10-16T00:00:00Z", updated_at: "2026-10-16T00:00:00Z",
  };
  const preview: ApprovalPreviewView = {
    protocol_version: "approval_queue.v1", approval_id: item.id, proposal_id: item.proposal_id,
    run_id: item.run_id, workspace_id: item.workspace_id, tool_name: item.tool_name,
    effect: "computer_use_app", working_directory: "", source_current: true, redacted: false, truncated: false,
    fields: [{ name: "app_id", value: "notepad" }, { name: "app_path", value: "C:\\Windows\\notepad.exe" },
      { name: "window_id", value: "window-notepad" }, { name: "tool_name", value: "type_text" },
      { name: "arguments", value: '{"window_id":"window-notepad","text":"task notes"}' },
      { name: "authorization_scope", value: "当前任务（run-app）" }],
  };
  return { item, preview };
}

function respond(data: unknown, status = 200) {
  return new Response(JSON.stringify({ version: "api.v1", request_id: "computer-use", data }),
    { status, headers: { "Content-Type": "application/json" } });
}

function mount(kind: "v2" | "legacy", options: { external?: boolean; pending?: boolean } = {}) {
  const { item, preview } = approval();
  if (options.external) preview.effect = "mcp_server_and_tool";
  let decided = false;
  let completeDecision: (() => void) | undefined;
  const fetch = vi.fn(async (url: string, init?: RequestInit) => {
    if (String(url).endsWith("/decision")) {
      const request = JSON.parse(String(init?.body));
      if (options.pending) await new Promise<void>((resolve) => { completeDecision = resolve; });
      decided = true;
      return respond({ version: "approval_control.v1", run_id: item.run_id, approval_id: item.id,
        proposal_id: item.proposal_id, tool_name: item.tool_name, action: request.action, status: "approved",
        replayed: false, process_execution_enabled: false, shell_execution_enabled: false,
        docker_execution_enabled: false, workspace_write_applied: false,
        session_grant_created: request.action === "approve_for_run_app", capability_grant: false,
        execution_resumed: false, retry_completed: false, retry_scheduled: false,
        continuation: { state: "completed", replayed: false, model_called: true, tool_called: true } }, 202);
    }
    return respond(String(url).endsWith("/preview") ? preview : {
      protocol_version: "approval_queue.v1", run_id: item.run_id, items: decided ? [] : [item],
      process_execution_enabled: false, session_grant_created: false, capability_grant: false, truncated: false,
    });
  });
  vi.stubGlobal("fetch", fetch);
  const client = new APIClient("read", "/api/v1", "control");
  const emptyPage = { items: [], page: { limit: 100 }, requestID: "history" };
  vi.spyOn(client, "controlledCommandProposals").mockResolvedValue(emptyPage);
  vi.spyOn(client, "hostCommandProposals").mockResolvedValue(emptyPage);
  window.localStorage.setItem("prayu.locale.v1", "zh-CN");
  render(<QueryClientProvider client={new QueryClient({ defaultOptions: {
    queries: { retry: false }, mutations: { retry: false },
  } })}><LocaleProvider>{kind === "v2"
      ? <V2ApprovalCards client={client} runID={item.run_id} threadID="thread-app" />
      : <ApprovalPanel client={client} runID={item.run_id} />}</LocaleProvider></QueryClientProvider>);
  return { fetch, complete: () => completeDecision?.() };
}

describe.each(["v2", "legacy"] as const)("%s Computer Use approval", (kind) => {
  it("shows the exact app and task scope, then sends a run-app grant without command limits", async () => {
    const { fetch } = mount(kind);
    expect(await screen.findByText("允许操作这个应用？")).toBeVisible();
    expect(screen.getByText("C:\\Windows\\notepad.exe")).toBeVisible();
    expect(screen.getByText("window-notepad")).toBeVisible();
    expect(screen.getByText("当前任务（run-app）")).toBeVisible();
    expect(screen.getByText('{"window_id":"window-notepad","text":"task notes"}')).toBeVisible();
    expect(screen.getByRole("button", { name: "本次允许" })).toBeEnabled();
    expect(screen.queryByRole("spinbutton")).not.toBeInTheDocument();
    await userEvent.click(screen.getByRole("button", { name: "本任务允许操作该应用" }));
    await waitFor(() => expect(fetch.mock.calls.filter(([, init]) => init?.method === "POST")).toHaveLength(1));
    const post = fetch.mock.calls.find(([, init]) => init?.method === "POST")!;
    expect(post[0]).toBe("/api/v1/runs/run-app/approvals/approval-app/decision");
    expect(new Headers(post[1]?.headers).get("Authorization")).toBe("Bearer control");
    expect(JSON.parse(String(post[1]?.body))).toEqual({ version: "approval_control.v1", action: "approve_for_run_app" });
    expect(screen.queryByRole("alert")).not.toBeInTheDocument();
  });

  it("keeps allow-once limited to the current operation", async () => {
    const { fetch } = mount(kind);
    await userEvent.click(await screen.findByRole("button", { name: "本次允许" }));
    await waitFor(() => expect(fetch.mock.calls.filter(([, init]) => init?.method === "POST")).toHaveLength(1));
    const post = fetch.mock.calls.find(([, init]) => init?.method === "POST")!;
    expect(JSON.parse(String(post[1]?.body))).toEqual({ version: "approval_control.v1", action: "approve_once" });
  });

  it("disables all decisions while submitting and ignores a duplicate click", async () => {
    const { fetch, complete } = mount(kind, { pending: true });
    const grant = await screen.findByRole("button", { name: "本任务允许操作该应用" });
    await userEvent.dblClick(grant);
    expect(grant).toBeDisabled();
    expect(screen.getByRole("button", { name: "本次允许" })).toBeDisabled();
    expect(screen.getByRole("button", { name: "拒绝" })).toBeDisabled();
    expect(screen.getByRole("textbox", { name: "mcp_tool_call 的拒绝原因" })).toBeDisabled();
    expect(fetch.mock.calls.filter(([, init]) => init?.method === "POST")).toHaveLength(1);
    complete();
    await waitFor(() => expect(screen.queryByRole("button", { name: "本任务允许操作该应用" })).not.toBeInTheDocument());
  });

  it("does not offer application authorization for an external MCP preview", async () => {
    const { fetch } = mount(kind, { external: true });
    await screen.findByRole("button", { name: kind === "v2" ? "批准一次" : "仅批准一次" });
    await waitFor(() => expect(fetch.mock.calls.some(([url]) => url.endsWith("/preview"))).toBe(true));
    expect(screen.queryByRole("button", { name: "本任务允许操作该应用" })).not.toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "本次允许" })).not.toBeInTheDocument();
  });
});

it("rejects command limits on an app authorization before sending HTTP", async () => {
  const fetch = vi.fn();
  vi.stubGlobal("fetch", fetch);
  const client = new APIClient("read", "/api/v1", "control");
  await expect(client.decideApproval("run-app", "approval-app", {
    version: "approval_control.v1", action: "approve_for_run_app", grant_ttl_seconds: 120, grant_max_uses: 2,
  }, "computer-use-operation-1")).rejects.toThrow("explicit limits");
  expect(fetch).not.toHaveBeenCalled();
});

it("rejects an app grant response from another tool", async () => {
  vi.stubGlobal("fetch", vi.fn(async () => respond({ version: "approval_control.v1", run_id: "run-app",
    approval_id: "approval-app", proposal_id: "call-app", tool_name: "shell", action: "approve_for_run_app",
    status: "approved", replayed: false, process_execution_enabled: false, shell_execution_enabled: false,
    docker_execution_enabled: false, workspace_write_applied: false, session_grant_created: true,
    capability_grant: false, execution_resumed: false, retry_completed: false, retry_scheduled: false }, 202)));
  await expect(new APIClient("read", "/api/v1", "control").decideApproval("run-app", "approval-app", {
    version: "approval_control.v1", action: "approve_for_run_app",
  }, "computer-use-operation-2")).rejects.toThrow("Application approval decision is invalid");
});
