import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act, cleanup, render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, describe, expect, it, vi } from "vitest";
import type { APIClient } from "../../api/client";
import type { RunDetailView, ThreadExecutionPermissionControlView } from "../../api/types";
import { LocaleProvider } from "../../lib/locale";
import { capabilityReadinessFixture } from "../../test/capability-readiness";
import { v2QueryKeys } from "../query-keys";
import { browserCDPQueryKey } from "./browser-cdp-control";
import { V2Composer } from "./composer";
import { V2Settings } from "./settings";

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
      runtime: { workspace_sandbox_enabled: true, operator_approval_enabled: true, danger_full_access_enabled: true },
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
  return { run: { id: runID, status: "paused" }, mission: { workspace_id: "workspace-1" },
    execution_permission: control(runID).execution_permission,
    execution_profile: { profile: "preview", backend: "noop", risk_tier: "minimal",
      approval_policy: "none", required_gate: "none", revision: 1 },
    execution_interaction: { mode: "preview", workspace_trust: "untrusted", command_form: "none",
      execution_profile: "preview", required_gate: "none", revision: 1 },
    mode: { revision: 1, scope: { network_mode: "disabled", allowed_targets: [], web_access_mode: "disabled" } },
  } as unknown as RunDetailView;
}

function install(variant: "composer" | "settings" = "settings") {
  window.localStorage.setItem("prayu.locale.v1", "zh-CN");
  const queries = new QueryClient({ defaultOptions: { queries: { retry: false }, mutations: { retry: false } } });
  const get = vi.fn(async (path: string) => {
    if (path.startsWith("/threads/")) return { thread: { id: path.split("/").at(-1), workspace_id: "workspace-1" },
      active_run: { id: "run-" + path.split("/").at(-1) } };
    if (path.endsWith("/browser-network")) return { version: "browser_network_access.v1", workspace_id: "workspace-1",
      run_id: path.split("/")[2], revision: 1, browser_network_access: "off", browser_origins: [] };
    return runDetail(path.slice("/runs/".length));
  });
  const providerSearchReadiness = vi.fn(async () => ({
    state: "network_disabled", reason: "run_network_disabled", remediation: "enable_network_allowlist",
  }));
  const setRunWebAccess = vi.fn(async (runID: string, request: { web_allowed_targets: string[] }) => ({
    mode: { ...runDetail(runID).mode, revision: 2, scope: { network_mode: "disabled", allowed_targets: [], web_access_mode: "rules", web_allowed_targets: request.web_allowed_targets } },
    replayed: false,
  }));
  const changeThreadExecutionPermission = vi.fn(); const putControl = vi.fn();
  const onSubmit = vi.fn(async () => {});
  const client = { baseURL: "/api/v1", hasControl: true, hasThreadControl: true, hasExecutionPermissionControl: true,
    getThreadExecutionPermission: vi.fn(async (threadID: string) => control(threadID)),
    runCapabilityReadiness: vi.fn(async (runID: string) => capabilityReadinessFixture(runID)),
    get, providerSearchReadiness, setRunWebAccess, changeThreadExecutionPermission, putControl,
  } as unknown as APIClient;
  const tree = (threadID: string) => <LocaleProvider><QueryClientProvider client={queries}>
    {variant === "composer" ? <V2Composer client={client} threadID={threadID} workspaceID="" workspaces={[]}
      onWorkspaceChange={() => {}} onSubmit={onSubmit} />
      : <V2Settings client={client} threadID={threadID} workspaces={[]} section="permissions"
        onOpenHistory={vi.fn()} onSelectSection={vi.fn()} />}
  </QueryClientProvider></LocaleProvider>;
  const view = render(tree("thread-a"));
  return { ...view, queries, get, providerSearchReadiness, setRunWebAccess, changeThreadExecutionPermission,
    putControl, onSubmit, changeThread: () => view.rerender(tree("thread-b")) };
}

async function openAdvanced(user: ReturnType<typeof userEvent.setup>) {
  const summary = screen.getByText("高级设置", { selector: ".v2-permission-advanced > summary" });
  await user.click(summary);
  await waitFor(() => expect(screen.getByRole("combobox", { name: "当前任务网页搜索模式" })).toBeEnabled());
  return summary;
}

async function useRules(user: ReturnType<typeof userEvent.setup>) {
  await user.selectOptions(screen.getByRole("combobox", { name: "当前任务网页搜索模式" }), "rules");
  await waitFor(() => expect(screen.getByRole("textbox", { name: "追加允许的 HTTPS 主机" })).toBeEnabled());
}

describe("Unified daily permissions and advanced restrictions", () => {
  it("keeps a real Composer draft with only three approval choices and no network setup", async () => {
    const user = userEvent.setup(); const fixture = install("composer");
    const draft = screen.getByRole("textbox", { name: "继续对话" });
    await user.type(draft, "Keep this unsent draft");
    await user.click(await screen.findByRole("button", { name: "请求批准" }));
    const options = within(screen.getByRole("group", { name: "执行权限档位" })).getAllByRole("menuitemradio");
    expect(options.map((option) => option.getAttribute("aria-label"))).toEqual(["请求批准", "帮我批准", "完全访问权限"]);
    expect(screen.queryByRole("button", { name: "网页访问与搜索" })).not.toBeInTheDocument();
    expect(screen.queryByRole("switch")).not.toBeInTheDocument();
    expect(screen.queryByRole("combobox")).not.toBeInTheDocument();
    await user.click(within(screen.getByRole("menu")).getByRole("menuitemradio", { name: "完全访问权限" }));
    const dialog = screen.getByRole("dialog", { name: "启用完全访问权限？" });
    expect(within(dialog).getByText(/项目文件、执行本机命令和访问公共网络/u)).toBeVisible();
    await user.click(within(dialog).getByRole("button", { name: "取消" }));
    expect(draft).toHaveValue("Keep this unsent draft");
    expect(fixture.get).not.toHaveBeenCalled(); expect(fixture.providerSearchReadiness).not.toHaveBeenCalled();
    expect(fixture.setRunWebAccess).not.toHaveBeenCalled(); expect(fixture.putControl).not.toHaveBeenCalled();
    expect(fixture.changeThreadExecutionPermission).not.toHaveBeenCalled(); expect(fixture.onSubmit).not.toHaveBeenCalled();
  });

  it("loads explicit preview, web-off and browser-off settings only after opening Advanced", async () => {
    const user = userEvent.setup(); const fixture = install();
    await screen.findByRole("button", { name: "请求批准" });
    expect(fixture.get).not.toHaveBeenCalled(); expect(fixture.providerSearchReadiness).not.toHaveBeenCalled();
    expect(screen.queryByRole("switch")).not.toBeInTheDocument();
    await openAdvanced(user);
    expect(screen.getByRole("combobox", { name: "当前任务网页搜索模式" })).toHaveValue("disabled");
    const environment = screen.getByRole("group", { name: "Run 执行环境" });
    expect(within(environment).getByRole("button", { name: /预览/u })).toHaveAttribute("aria-pressed", "true");
    expect(within(environment).getByRole("button", { name: /Docker Engine/u })).toBeInTheDocument();
    expect(await screen.findByRole("switch", { name: "允许联网" })).toHaveAttribute("aria-checked", "false");
    expect(screen.queryByRole("switch", { name: "完整 CDP 控制" })).not.toBeInTheDocument();
    expect(fixture.providerSearchReadiness).toHaveBeenCalledWith("thread-a", expect.any(AbortSignal));
    expect(fixture.setRunWebAccess).not.toHaveBeenCalled(); expect(fixture.putControl).not.toHaveBeenCalled();
    expect(fixture.changeThreadExecutionPermission).not.toHaveBeenCalled();
  });

  it.each(["cancel", "close", "backdrop"] as const)("cancels an advanced confirmation by %s without discarding its draft", async (action) => {
    const user = userEvent.setup(); const fixture = install();
    await openAdvanced(user); await useRules(user);
    fixture.setRunWebAccess.mockClear();
    const input = screen.getByRole("textbox", { name: "追加允许的 HTTPS 主机" });
    await user.type(input, "docs.example.org");
    const review = screen.getByRole("button", { name: "审核并追加" });
    await user.click(review);
    const confirmation = screen.getByRole("dialog", { name: "追加网页访问范围？" });
    expect(confirmation.parentElement?.parentElement).toBe(document.body);
    if (action === "backdrop") await user.click(confirmation.parentElement!);
    else await user.click(within(confirmation).getByRole("button", { name: action === "cancel" ? "取消" : "关闭" }));
    expect(screen.queryByRole("dialog")).not.toBeInTheDocument();
    expect(input).toHaveValue("docs.example.org");
    await waitFor(() => expect(review).toHaveFocus());
    expect(fixture.setRunWebAccess).not.toHaveBeenCalled();
  });

  it("keeps a pending write on its original Thread while the new settings start collapsed", async () => {
    const user = userEvent.setup(); const fixture = install();
    await openAdvanced(user); await useRules(user);
    fixture.setRunWebAccess.mockClear();
    let release: (() => void) | undefined;
    fixture.setRunWebAccess.mockImplementation(async (runID, request) => {
      await new Promise<void>((resolve) => { release = resolve; });
      return { mode: { ...runDetail(runID).mode, revision: 3,
        scope: { network_mode: "disabled", allowed_targets: [], web_access_mode: "rules", web_allowed_targets: request.web_allowed_targets } }, replayed: false };
    });
    await user.type(screen.getByRole("textbox", { name: "追加允许的 HTTPS 主机" }), "docs.example.org");
    await user.click(screen.getByRole("button", { name: "审核并追加" }));
    await user.click(within(screen.getByRole("dialog", { name: "追加网页访问范围？" })).getByRole("button", { name: "允许这些主机" }));
    expect(fixture.setRunWebAccess).toHaveBeenCalledExactlyOnceWith("run-thread-a", {
      version: "run_web_access_control.v1", expected_mode_revision: 2,
      web_allowed_targets: ["docs.example.org"], web_access_mode: "rules", reason: "v2 operator-selected web access",
    }, expect.stringMatching(/^v2-run-web-access-/u));
    fixture.changeThread();
    expect(screen.queryByRole("dialog")).not.toBeInTheDocument();
    expect(fixture.container.querySelector(".v2-permission-advanced")).not.toHaveAttribute("open");
    await act(async () => { release?.(); });
    expect(fixture.queries.getQueryData<RunDetailView>(browserCDPQueryKey("run-thread-b"))).toBeUndefined();
    expect(fixture.onSubmit).not.toHaveBeenCalled();
  });

  it("discards a confirmation when the same Thread receives a new current Run", async () => {
    const user = userEvent.setup(); const fixture = install();
    await openAdvanced(user); await useRules(user);
    await user.type(screen.getByRole("textbox", { name: "追加允许的 HTTPS 主机" }), "docs.example.org");
    await user.click(screen.getByRole("button", { name: "审核并追加" }));
    act(() => { fixture.queries.setQueryData(v2QueryKeys.permission("thread-a"), {
      ...control("thread-a"), current_run_id: "run-next",
    }); });
    await waitFor(() => expect(screen.queryByRole("dialog")).not.toBeInTheDocument());
    await waitFor(() => expect(fixture.get).toHaveBeenCalledWith("/runs/run-next", {}, expect.any(AbortSignal)));
    expect(screen.getByRole("combobox", { name: "当前任务网页搜索模式" })).toHaveValue("disabled");
  });
});

beforeEach(() => localStorage.setItem("universal-code.composer-plain-text", "true"));
