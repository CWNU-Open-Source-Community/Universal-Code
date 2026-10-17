import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act, cleanup, render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, describe, expect, it, vi } from "vitest";
import type { APIClient } from "../../api/client";
import type { RunDetailView, ThreadExecutionPermissionControlView } from "../../api/types";
import { LocaleProvider, type PrayuLocale } from "../../lib/locale";
import { v2QueryKeys } from "../query-keys";
import { browserCDPQueryKey } from "./browser-cdp-control";
import { V2Composer } from "./composer";
import { V2PermissionControl } from "./permission-control";
import { V2RunNetworkAuthorityControl } from "./run-network-authority-control";

afterEach(() => { cleanup(); window.localStorage.removeItem("prayu.locale.v1"); });

function control(threadID: string): ThreadExecutionPermissionControlView {
  return {
    execution_permission: {
      thread_id: threadID, protocol_version: "thread_execution_permission.v2", revision: 1,
      mode: "ask", approval_mode: "ask", full_activation: "inactive", approval_policy: "per_operation",
      command_scope: "per_operation", filesystem_scope: "per_operation", network_scope: "per_operation",
      persistent_terminal: false, background_process: false, agent_terminal_input: false, risk_tier: "minimal",
      required_gate: "operation_authority", policy_version: "execution_permission_policy.v2",
      operator_confirmed: false, runtime_gate_available: true,
      runtime: { workspace_sandbox_enabled: true, operator_approval_enabled: true,
        danger_full_access_enabled: true,  },
      capability_matrix: { workspace_read: true, workspace_write: true, sandboxed_command_runtime: true,
        unsandboxed_host_process: false, network_access: false, credential_access: false,
        user_home_access: false, persistent_user_terminal: false, persistent_agent_terminal: false,
        full_cdp: false, out_of_scope_policy: "exact_once_required" },
      created_at: "2026-10-02T00:00:00Z", process_enabled: false, execution_authorized: false,
      capability_grant: false, applies_to_current_run: true, applies_to_future_successor_runs: true,
    },
    current_run_id: "run-" + threadID, current_run_effect: "applied",
    current_run_mode: "ask", current_run_synchronized: true, replayed: false,
  };
}

function runDetail(runID: string): RunDetailView {
  // These are the display/read fields used by the real network and CDP panels.
  return { run: { id: runID, status: "paused" },
    execution_permission: control(runID).execution_permission,
    mode: { revision: 1, scope: { network_mode: "disabled", allowed_targets: [] } },
  } as unknown as RunDetailView;
}

function install(variant: "menu" | "settings" | "network-menu" = "menu", locale: PrayuLocale = "zh-CN") {
  window.localStorage.setItem("prayu.locale.v1", locale);
  const queries = new QueryClient({ defaultOptions: { queries: { retry: false }, mutations: { retry: false } } });
  const get = vi.fn(async (path: string) => runDetail(path.slice("/runs/".length)));
  const providerSearchReadiness = vi.fn(async () => ({
    state: "provider_unqualified", reason: "provider_native_qualification_required",
    remediation: "qualify_provider_search", search_policy: "provider_native", provider: "Fixture",
  }));
  const setRunWebAccess = vi.fn(async (runID: string, request: { web_allowed_targets: string[] }) => ({
    mode: { ...runDetail(runID).mode, revision: 2, scope: { network_mode: "disabled", allowed_targets: [], web_access_mode: "rules", web_allowed_targets: request.web_allowed_targets } },
    replayed: false,
  }));
  const changeThreadExecutionPermission = vi.fn();
  const onSubmit = vi.fn(async () => {});
  const client = { hasControl: true, hasThreadControl: true, hasExecutionPermissionControl: true,
    getThreadExecutionPermission: vi.fn(async (threadID: string) => control(threadID)),
    get, providerSearchReadiness, setRunWebAccess, changeThreadExecutionPermission,
  } as unknown as APIClient;
  const tree = (threadID: string) => <LocaleProvider><QueryClientProvider client={queries}>
    {variant === "menu" ? <V2Composer client={client} threadID={threadID} workspaceID="" workspaces={[]}
      onWorkspaceChange={() => {}} onSubmit={onSubmit} />
      : variant === "network-menu" ? <V2RunNetworkAuthorityControl client={client} threadID={threadID}
        runID={"run-" + threadID} variant="menu" />
      : <V2PermissionControl client={client} threadID={threadID} variant="settings" />}
    <button type="button">Outside</button>
  </QueryClientProvider></LocaleProvider>;
  const view = render(tree("thread-a"));
  return { ...view, queries, get, providerSearchReadiness, setRunWebAccess, changeThreadExecutionPermission,
    onSubmit, changeThread: () => view.rerender(tree("thread-b")) };
}

async function openNetwork(user: ReturnType<typeof userEvent.setup>, name = "网页访问与搜索") {
  const trigger = await screen.findByRole("button", { name });
  await user.click(trigger);
  const dialog = screen.getByRole("dialog", { name });
  await waitFor(() => expect(within(dialog).getByRole("combobox", { name: "当前任务网页搜索模式" })).toBeEnabled());
  return { trigger, dialog };
}

describe("Composer network layout and interaction", () => {
  it("loads network facts only in the existing popover, without a composer details block or a write", async () => {
    const user = userEvent.setup(); const fixture = install();
    const draft = screen.getByRole("textbox", { name: "继续对话" });
    await user.type(draft, "Keep this unsent draft");
    await user.click(await screen.findByRole("button", { name: "请求批准" }));
    expect(screen.getByRole("menu", { name: "选择执行权限" })).toBeVisible();
    expect(fixture.get).not.toHaveBeenCalled(); expect(fixture.providerSearchReadiness).not.toHaveBeenCalled();
    const { trigger, dialog } = await openNetwork(user);
    expect(trigger).toHaveAttribute("type", "button");
    expect(trigger).toHaveAttribute("aria-controls", dialog.id);
    expect(dialog).toHaveClass("v2-network-popover");
    expect(dialog).toHaveFocus();
    expect(fixture.container.querySelector(".v2-composer details")).toBeNull();
    expect(screen.queryByRole("menu")).not.toBeInTheDocument();
    expect(fixture.get).toHaveBeenCalledExactlyOnceWith("/runs/run-thread-a", {}, expect.any(AbortSignal));
    expect(fixture.providerSearchReadiness).toHaveBeenCalledExactlyOnceWith("thread-a", expect.any(AbortSignal));
    expect(fixture.setRunWebAccess).not.toHaveBeenCalled();
    expect(fixture.changeThreadExecutionPermission).not.toHaveBeenCalled();
    expect(fixture.onSubmit).not.toHaveBeenCalled();
    expect(draft).toHaveValue("Keep this unsent draft");
  });

  it.each(["Escape", "close", "outside"] as const)("dismisses by %s without submitting or losing the draft", async (action) => {
    const user = userEvent.setup(); const fixture = install();
    const draft = screen.getByRole("textbox", { name: "继续对话" });
    await user.type(draft, "Retain draft");
    const { trigger, dialog } = await openNetwork(user);
    if (action === "Escape") await user.keyboard("{Escape}");
    else if (action === "close") await user.click(within(dialog).getByRole("button", { name: "关闭网页访问与搜索" }));
    else await user.click(screen.getByRole("button", { name: "Outside" }));
    expect(screen.queryByRole("dialog", { name: "网页访问与搜索" })).not.toBeInTheDocument();
    expect(action === "outside" ? screen.getByRole("button", { name: "Outside" }) : trigger).toHaveFocus();
    expect(draft).toHaveValue("Retain draft");
    expect(fixture.setRunWebAccess).not.toHaveBeenCalled();
    expect(fixture.onSubmit).not.toHaveBeenCalled();
  });

  it("opens from the keyboard and localizes the menu host and close control", async () => {
    const user = userEvent.setup(); const fixture = install("menu", "en-US");
    const trigger = await screen.findByRole("button", { name: "Web access and search" });
    trigger.focus(); await user.keyboard("{Enter}");
    const dialog = screen.getByRole("dialog", { name: "Web access and search" });
    expect(dialog).toHaveFocus();
    expect(within(dialog).getByRole("button", { name: "Close web access and search" })).toBeVisible();
    await user.keyboard("{Escape}");
    expect(trigger).toHaveFocus(); expect(fixture.onSubmit).not.toHaveBeenCalled();
  });

  it("discards the open panel and its draft when the target Thread changes", async () => {
    const user = userEvent.setup(); const fixture = install();
    await openNetwork(user);
    await user.type(screen.getByRole("textbox", { name: "追加允许的 HTTPS 主机" }), "docs.example.org");
    fixture.changeThread();
    const trigger = await screen.findByRole("button", { name: "网页访问与搜索" });
    expect(trigger).toHaveAttribute("aria-expanded", "false");
    expect(screen.queryByRole("dialog", { name: "网页访问与搜索" })).not.toBeInTheDocument();
    await user.click(trigger);
    expect(await screen.findByRole("textbox", { name: "追加允许的 HTTPS 主机" })).toHaveValue("");
    await waitFor(() => expect(fixture.get).toHaveBeenCalledWith("/runs/run-thread-b", {}, expect.any(AbortSignal)));
    expect(fixture.setRunWebAccess).not.toHaveBeenCalled();
  });

  it("cancels an inner network confirmation before closing the outer popover", async () => {
    const user = userEvent.setup(); const fixture = install();
    const { trigger } = await openNetwork(user);
    await user.type(screen.getByRole("textbox", { name: "追加允许的 HTTPS 主机" }), "docs.example.org");
    await user.click(screen.getByRole("button", { name: "审核并追加" }));
    expect(screen.getByRole("dialog", { name: "追加网页访问范围？" })).toBeVisible();
    expect(screen.getByRole("dialog", { name: "追加网页访问范围？" }).parentElement?.parentElement).toBe(document.body);
    await user.keyboard("{Escape}");
    expect(screen.queryByRole("dialog", { name: "追加网页访问范围？" })).not.toBeInTheDocument();
    expect(screen.getByRole("dialog", { name: "网页访问与搜索" })).toBeVisible();
    expect(fixture.setRunWebAccess).not.toHaveBeenCalled();
    await user.keyboard("{Escape}");
    expect(screen.queryByRole("dialog")).not.toBeInTheDocument(); expect(trigger).toHaveFocus();
  });

  it.each(["cancel", "close", "backdrop"] as const)("cancels the body portal by %s while keeping the network draft", async (action) => {
    const user = userEvent.setup(); const fixture = install();
    const { dialog } = await openNetwork(user);
    const input = screen.getByRole("textbox", { name: "追加允许的 HTTPS 主机" });
    await user.type(input, "docs.example.org");
    const review = screen.getByRole("button", { name: "审核并追加" });
    await user.click(review);
    const confirmation = screen.getByRole("dialog", { name: "追加网页访问范围？" });
    expect(confirmation.parentElement?.parentElement).toBe(document.body);
    expect(dialog).not.toContainElement(confirmation);
    if (action === "backdrop") await user.click(confirmation.parentElement!);
    else await user.click(within(confirmation).getByRole("button", { name: action === "cancel" ? "取消" : "关闭" }));
    expect(screen.queryByRole("dialog", { name: "追加网页访问范围？" })).not.toBeInTheDocument();
    expect(dialog).toBeVisible(); expect(input).toHaveValue("docs.example.org");
    await waitFor(() => expect(review).toHaveFocus());
    expect(fixture.setRunWebAccess).not.toHaveBeenCalled();
    expect(fixture.onSubmit).not.toHaveBeenCalled();
  });

  it.each([false, true])("removes the portal on Thread switch with pending=%s and keeps the original request target", async (pending) => {
    const user = userEvent.setup(); const fixture = install();
    let release: (() => void) | undefined;
    fixture.setRunWebAccess.mockImplementation(async (runID, request) => {
      await new Promise<void>((resolve) => { release = resolve; });
      return { mode: { ...runDetail(runID).mode, revision: 2,
        scope: { network_mode: "disabled", allowed_targets: [], web_access_mode: "rules", web_allowed_targets: request.web_allowed_targets } }, replayed: false };
    });
    await openNetwork(user);
    await user.type(screen.getByRole("textbox", { name: "追加允许的 HTTPS 主机" }), "docs.example.org");
    await user.click(screen.getByRole("button", { name: "审核并追加" }));
    if (pending) await user.click(within(screen.getByRole("dialog", { name: "追加网页访问范围？" }))
      .getByRole("button", { name: "允许这些主机" }));
    fixture.changeThread();
    expect(screen.queryByRole("dialog", { name: "追加网页访问范围？" })).not.toBeInTheDocument();
    await openNetwork(user);
    expect(screen.getByRole("textbox", { name: "追加允许的 HTTPS 主机" })).toHaveValue("");
    if (pending) {
      expect(fixture.setRunWebAccess.mock.calls[0]?.[0]).toBe("run-thread-a");
      await act(async () => { release?.(); });
      expect(fixture.queries.getQueryData<RunDetailView>(browserCDPQueryKey("run-thread-b"))?.mode.scope.allowed_targets).toEqual([]);
    }
    expect(fixture.setRunWebAccess).toHaveBeenCalledTimes(pending ? 1 : 0);
    await user.keyboard("{Escape}");
    expect(screen.queryByRole("dialog")).not.toBeInTheDocument();
    expect(fixture.onSubmit).not.toHaveBeenCalled();
  });

  it("clears the portal guard when the same Thread changes its current Run", async () => {
    const user = userEvent.setup(); const fixture = install();
    await openNetwork(user);
    await user.type(screen.getByRole("textbox", { name: "追加允许的 HTTPS 主机" }), "docs.example.org");
    await user.click(screen.getByRole("button", { name: "审核并追加" }));
    act(() => { fixture.queries.setQueryData(v2QueryKeys.permission("thread-a"), {
      ...control("thread-a"), current_run_id: "run-next",
    }); });
    await waitFor(() => expect(screen.queryByRole("dialog", { name: "追加网页访问范围？" })).not.toBeInTheDocument());
    await waitFor(() => expect(fixture.get).toHaveBeenCalledWith("/runs/run-next", {}, expect.any(AbortSignal)));
    expect(screen.getByRole("textbox", { name: "追加允许的 HTTPS 主机" })).toHaveValue("");
    await user.keyboard("{Escape}");
    expect(screen.queryByRole("dialog")).not.toBeInTheDocument();
    expect(fixture.setRunWebAccess).not.toHaveBeenCalled();
  });

  it("keeps the native network menu open while cancelling its body portal", async () => {
    const user = userEvent.setup(); const fixture = install("network-menu");
    await user.click(screen.getByRole("button", { name: "网页访问状态" }));
    const menu = screen.getByRole("dialog", { name: "当前执行网页访问" });
    await user.type(await within(menu).findByRole("textbox", { name: "追加允许的 HTTPS 主机" }), "docs.example.org");
    const review = within(menu).getByRole("button", { name: "审核并追加" });
    await user.click(review);
    const confirmation = screen.getByRole("dialog", { name: "追加网页访问范围？" });
    expect(confirmation.parentElement?.parentElement).toBe(document.body);
    await user.click(within(confirmation).getByRole("button", { name: "取消" }));
    expect(menu).toBeVisible(); expect(review).toHaveFocus();
    expect(fixture.setRunWebAccess).not.toHaveBeenCalled();
  });

  it("retains a pending inner confirmation and the exact network request", async () => {
    const user = userEvent.setup(); const fixture = install();
    let release: (() => void) | undefined;
    fixture.setRunWebAccess.mockImplementation(async (runID, request) => {
      await new Promise<void>((resolve) => { release = resolve; });
      return { mode: { ...runDetail(runID).mode, revision: 2,
        scope: { network_mode: "disabled", allowed_targets: [], web_access_mode: "rules", web_allowed_targets: request.web_allowed_targets } }, replayed: false };
    });
    await openNetwork(user);
    await user.type(screen.getByRole("textbox", { name: "追加允许的 HTTPS 主机" }), "docs.example.org");
    await user.click(screen.getByRole("button", { name: "审核并追加" }));
    await user.click(within(screen.getByRole("dialog", { name: "追加网页访问范围？" })).getByRole("button", { name: "允许这些主机" }));
    await waitFor(() => expect(fixture.setRunWebAccess).toHaveBeenCalledExactlyOnceWith("run-thread-a", {
      version: "run_web_access_control.v1", expected_mode_revision: 1,
      web_allowed_targets: ["docs.example.org"], web_access_mode: "rules", reason: "v2 operator-selected web access",
    }, expect.stringMatching(/^v2-run-web-access-/u)));
    await user.keyboard("{Escape}");
    expect(screen.getByRole("dialog", { name: "网页访问与搜索" })).toBeVisible();
    expect(screen.getByRole("dialog", { name: "追加网页访问范围？" })).toBeVisible();
    await user.click(screen.getByRole("button", { name: "网页访问与搜索" }));
    expect(screen.getByRole("dialog", { name: "追加网页访问范围？" })).toBeVisible();
    await user.click(screen.getByRole("button", { name: "关闭网页访问与搜索" }));
    await user.click(screen.getByRole("button", { name: "Outside" }));
    expect(screen.getByRole("dialog", { name: "网页访问与搜索" })).toBeVisible();
    expect(screen.getByRole("dialog", { name: "追加网页访问范围？" })).toBeVisible();
    await act(async () => { release?.(); });
    await waitFor(() => expect(screen.queryByRole("dialog", { name: "追加网页访问范围？" })).not.toBeInTheDocument());
    expect(fixture.onSubmit).not.toHaveBeenCalled();
  });

  it("keeps settings expansion in the settings page and localizes its status", async () => {
    const user = userEvent.setup(); const fixture = install("settings", "en-US");
    expect(await screen.findByText("Applied to the current and future runs.")).toBeVisible();
    const summary = screen.getByText("Web access and search");
    expect(summary.tagName).toBe("SUMMARY");
    await user.click(summary);
    expect(await screen.findByText("当前任务网页访问")).toBeVisible();
    expect(summary.closest("details")).toHaveAttribute("open");
    expect(fixture.container.querySelector(".v2-network-popover")).toBeNull();
    expect(fixture.setRunWebAccess).not.toHaveBeenCalled();
  });
});

// Retain textarea coverage through the supported plain-text editing mode.
beforeEach(() => localStorage.setItem("universal-code.composer-plain-text", "true"));
