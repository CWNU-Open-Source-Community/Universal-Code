import type { ReactNode } from "react";
import { LocaleProvider } from "../../lib/locale";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { fireEvent, render as renderComponent, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import type { APIClient } from "../../api/client";
import type { WorkspaceFileAttachment } from "../../api/file-attachments";
import type { WorkspaceImageAttachment } from "../../api/image-attachments";
import type { ThreadExecutionPermissionControlView,
  ThreadExecutionPermissionView } from "../../api/types";
import { v2AttachmentReferenceKey } from "../attachment-keys";
import { V2Composer } from "./composer";
import { v2ImageReferenceKey } from "./image-input";
import { V2PermissionControl } from "./permission-control";

afterEach(() => window.localStorage.removeItem("prayu.locale.v1"));

function permission(overrides: Partial<ThreadExecutionPermissionView> = {}):
ThreadExecutionPermissionView {
  return {
    thread_id: "thread-1", protocol_version: "thread_execution_permission.v1",
    revision: 1, mode: "ask", approval_mode: "ask", full_activation: "inactive", approval_policy: "fixed_templates",
    command_scope: "fixed_templates", filesystem_scope: "workspace_guarded",
    network_scope: "disabled", persistent_terminal: false, background_process: false,
    agent_terminal_input: false, risk_tier: "minimal",
    required_gate: "conservative_control", policy_version: "execution_permission_policy.v1",
    operator_confirmed: false, runtime_gate_available: true,
    runtime: { workspace_sandbox_enabled: true, operator_approval_enabled: true,
      danger_full_access_enabled: true,  },
    capability_matrix: { workspace_read: true, workspace_write: true,
      sandboxed_command_runtime: false, unsandboxed_host_process: false,
      network_access: false, credential_access: false, user_home_access: false,
      persistent_user_terminal: false, persistent_agent_terminal: false,
      full_cdp: false, out_of_scope_policy: "denied" },
    created_at: "2026-08-29T00:00:00Z", process_enabled: false,
    execution_authorized: false, capability_grant: false,
    applies_to_current_run: true, applies_to_future_successor_runs: true,
    ...overrides,
  };
}

function control(executionPermission = permission()): ThreadExecutionPermissionControlView {
  return { execution_permission: executionPermission, current_run_id: "run-1",
    current_run_effect: "applied", current_run_mode: executionPermission.mode,
    current_run_synchronized: true, replayed: false };
}

describe("Composer permission integration", () => {
  it("reads webpage and search status only when expanded inside permissions, without granting access", async () => {
    const user = userEvent.setup();
    const get = vi.fn().mockResolvedValue({ run: { status: "paused" },
      mode: { scope: { network_mode: "disabled", allowed_targets: [] } }, execution_permission: { mode: "conservative" } });
    const providerSearchReadiness = vi.fn().mockResolvedValue({ state: "provider_unqualified",
      reason: "provider_native_qualification_required", remediation: "qualify_provider_search" });
    const changeThreadExecutionPermission = vi.fn(); const setRunWebAccess = vi.fn();
    const client = { getThreadExecutionPermission: vi.fn().mockResolvedValue(control()),
      hasExecutionPermissionControl: true, hasControl: true, get, providerSearchReadiness,
      changeThreadExecutionPermission, setRunWebAccess } as unknown as APIClient;
    const queries = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    render(<QueryClientProvider client={queries}><V2PermissionControl client={client} threadID="thread-1" /></QueryClientProvider>);
    await user.click(await screen.findByRole("button", { name: "请求批准" }));
    expect(get).not.toHaveBeenCalled(); expect(providerSearchReadiness).not.toHaveBeenCalled();
    await user.click(screen.getByRole("button", { name: "网页访问与搜索" }));
    expect(await screen.findByText("网页搜索 · 搜索待验证")).toBeInTheDocument();
    expect(screen.getByRole("combobox", { name: "当前任务网页搜索模式" })).toBeInTheDocument();
    expect(get).toHaveBeenCalledExactlyOnceWith("/runs/run-1", {}, expect.any(AbortSignal));
    expect(providerSearchReadiness).toHaveBeenCalledExactlyOnceWith("thread-1", expect.any(AbortSignal));
    expect(changeThreadExecutionPermission).not.toHaveBeenCalled();
    expect(setRunWebAccess).not.toHaveBeenCalled();
    await user.click(screen.getByRole("button", { name: "网页访问与搜索" }));
    await waitFor(() => expect(screen.queryByRole("combobox", { name: "当前任务网页搜索模式" })).not.toBeInTheDocument());
  });

  it.each(["click", "Enter"] as const)("keeps a real Composer draft and its attachments through cancellation and %s permission confirmation", async (activation) => {
    const user = userEvent.setup();
    const draft = "原回复未确认期间新写的要求，恢复后不要清掉。";
    const workspaceID = "workspace-1";
    const image: WorkspaceImageAttachment = { id: "draft-image", workspace_id: workspaceID,
      name: "未发送.png", sha256: "a".repeat(64), byte_size: 64, mime_type: "image/png", width: 4, height: 4 };
    const files: WorkspaceFileAttachment[] = Array.from({ length: 4 }, (_, index) => ({
      id: `draft-file-${index}`, workspace_id: workspaceID, name: `未发送-${index}.txt`,
      sha256: String(index + 1).repeat(64), byte_size: 12, mime_type: "text/plain",
      readability: "text", text_bytes: 12, redacted: false,
    }));
    const queries = new QueryClient({ defaultOptions: { queries: { retry: false }, mutations: { retry: false } } });
    queries.setQueryData(v2ImageReferenceKey(workspaceID, "thread-1"), [image]);
    queries.setQueryData(v2AttachmentReferenceKey(workspaceID, "thread-1"), files);
    const changed = control(permission({ mode: "full", approval_mode: "full", full_activation: "active", revision: 2, operator_confirmed: true }));
    const changeThreadExecutionPermission = vi.fn().mockResolvedValue(changed);
    const client = { baseURL: "/api/v1", hasThreadControl: true, hasModelControl: true,
      hasExecutionPermissionControl: true,
      getThreadExecutionPermission: vi.fn().mockResolvedValue(control()), changeThreadExecutionPermission,
      threadModelRoute: vi.fn().mockResolvedValue({ vision_capability: { state: "supported", source: "operator_declared" } }),
      downloadWorkspaceImage: vi.fn().mockResolvedValue(new Blob(["verified image"], { type: "image/png" })),
    } as unknown as APIClient;
    const onSubmit = vi.fn(async (..._args: unknown[]) => {});
    render(<QueryClientProvider client={queries}><V2Composer client={client} threadID="thread-1"
      workspaceID={workspaceID} workspaces={[]} onWorkspaceChange={() => {}} onSubmit={onSubmit} />
    </QueryClientProvider>);
    const textarea = screen.getByRole("textbox", { name: "继续对话" });
    fireEvent.change(textarea, { target: { value: draft } });
    await waitFor(() => expect(screen.getByRole("button", { name: "发送消息" })).toBeEnabled());
    const openConfirmation = async () => {
      await user.click(await screen.findByRole("button", { name: "请求批准" }));
      await user.click(within(screen.getByRole("group", { name: "执行权限档位" }))
        .getByRole("menuitemradio", { name: "完全访问权限" }));
      return screen.getByRole("dialog", { name: "启用完全访问权限？" });
    };
    const cancelled = await openConfirmation();
    await user.click(within(cancelled).getByRole("button", { name: "取消" }));
    expect(changeThreadExecutionPermission).not.toHaveBeenCalled();
    expect(onSubmit).not.toHaveBeenCalled();
    expect(textarea).toHaveValue(draft);

    const dialog = await openConfirmation();
    const confirm = within(dialog).getByRole("button", { name: "确认启用" });
    if (activation === "click") await user.click(confirm);
    else { confirm.focus(); await user.keyboard("{Enter}"); }
    await waitFor(() => expect(screen.queryByRole("dialog")).not.toBeInTheDocument());
    expect(changeThreadExecutionPermission).toHaveBeenCalledExactlyOnceWith("thread-1", {
      mode: "full", reason: "Thread approval preference selection", confirm_full: true,
    }, expect.stringMatching(/^thread-approval-preference-/u));
    expect(onSubmit).not.toHaveBeenCalled();
    expect(textarea).toHaveValue(draft);
    expect(queries.getQueryData(v2ImageReferenceKey(workspaceID, "thread-1"))).toEqual([image]);
    expect(queries.getQueryData(v2AttachmentReferenceKey(workspaceID, "thread-1"))).toEqual(files);
    expect(screen.getByRole("button", { name: "移除图片 未发送.png" })).toBeEnabled();
    for (const file of files) expect(screen.getByRole("button", { name: `移除文件 ${file.name}` })).toBeEnabled();

    await user.click(screen.getByRole("button", { name: "发送消息" }));
    await waitFor(() => expect(onSubmit).toHaveBeenCalledExactlyOnceWith(draft, [], [image], undefined, files));
    await waitFor(() => expect(textarea).toHaveValue(""));
  });

  it("searches project files inside the real portal without sending the draft, then permits a Composer keyboard send", async () => {
    const user = userEvent.setup(); const onSubmit = vi.fn(async () => {});
    const queries = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    const workspaceSearch = vi.fn().mockResolvedValue({ query: "README", results: [], truncated: false });
    const client = { baseURL: "/api/v1", hasThreadControl: true, hasEvidenceAttachment: true,
      workspaceExplore: vi.fn().mockResolvedValue({ protocol_version: "workspace_explorer.v1",
        workspace_id: "workspace-1", path: ".", kind: "directory", content: "", entries: [],
        redaction_count: 0, truncated: false, returned_bytes: 0, total_bytes: 0,
        provenance: { source_kind: "workspace_file", source_ref: ".", content_sha256: "b".repeat(64), instruction_authorized: false } }),
      workspaceSearch,
    } as unknown as APIClient;
    render(<QueryClientProvider client={queries}><V2Composer client={client} threadID="thread-1"
      workspaceID="workspace-1" workspaces={[]} onWorkspaceChange={() => {}} onSubmit={onSubmit} />
    </QueryClientProvider>);
    const textarea = screen.getByRole("textbox", { name: "继续对话" });
    fireEvent.change(textarea, { target: { value: "搜索文件不能代我发送" } });
    await user.click(screen.getByRole("button", { name: "添加附件" }));
    await user.click(screen.getByRole("menuitem", { name: /引用项目文件/u }));
    const search = await screen.findByRole("searchbox", { name: "搜索工作区证据" });
    await user.type(search, "README{Enter}");
    await waitFor(() => expect(workspaceSearch).toHaveBeenCalledExactlyOnceWith("workspace-1", "README", expect.any(AbortSignal)));
    expect(onSubmit).not.toHaveBeenCalled();
    expect(textarea).toHaveValue("搜索文件不能代我发送");
    await user.click(screen.getByRole("button", { name: "关闭文件选择" }));
    await user.click(textarea); await user.keyboard("{Enter}");
    await waitFor(() => expect(onSubmit).toHaveBeenCalledExactlyOnceWith("搜索文件不能代我发送"));
  });

});

// Match the production locale boundary instead of the isolated English default.
function render(ui: ReactNode) {
  window.localStorage.setItem("prayu.locale.v1", "zh-CN");
  return renderComponent(ui, { wrapper: LocaleProvider });
}

// Retain textarea coverage through the supported plain-text editing mode.
beforeEach(() => localStorage.setItem("universal-code.composer-plain-text", "true"));
