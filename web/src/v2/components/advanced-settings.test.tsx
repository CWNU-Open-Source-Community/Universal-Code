import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import type { APIClient } from "../../api/client";
import { LocaleProvider } from "../../lib/locale";
import { V2Settings } from "./settings";
import type { V2SettingsSection } from "./sidebar";
import { useConnectionStore } from "../../state/connection";

function mount(client: Partial<APIClient>, section: V2SettingsSection, threadID = "", desktop = false) {
  window.localStorage.setItem("prayu.locale.v1", "zh-CN");
  const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false }, mutations: { retry: false } } });
  const view = (currentSection: V2SettingsSection) => <QueryClientProvider client={queryClient}><LocaleProvider>
    <V2Settings client={{ extensionInventory: vi.fn().mockResolvedValue(emptyExtensions), ...client } as APIClient} desktop={desktop} onOpenInspector={vi.fn()}
      onSelectSection={vi.fn()} section={currentSection} threadID={threadID} workspaces={[]} />
  </LocaleProvider></QueryClientProvider>;
  const rendered = render(view(section));
  return { ...rendered, queryClient,
    rerenderSection: (next: V2SettingsSection) => rendered.rerender(view(next)) };
}

const emptyExtensions = { mcp_servers: [], mcp_calls: [], plugins: [], workspace_id: "workspace-one" };
const emptyCodeIntel = { servers: [], qualifications: [] };

describe("shared advanced settings", () => {
  beforeEach(() => window.localStorage.clear());
  afterEach(() => useConnectionStore.getState().disconnect());

  it("resolves the current Thread before reading its extension scope", async () => {
    const get = vi.fn().mockResolvedValue({ thread: { id: "thread-one", title: "Current task" },
      active_run: { id: "run-one" }, last_run: { id: "old-run" } });
    const extensionInventory = vi.fn().mockResolvedValue(emptyExtensions);
    const codeIntelInventory = vi.fn().mockResolvedValue(emptyCodeIntel);
    mount({ get, extensionInventory, codeIntelInventory }, "extensions", "thread-one");
    await waitFor(() => expect(codeIntelInventory).toHaveBeenCalledWith("workspace-one", expect.any(AbortSignal)));
    expect(get).toHaveBeenCalledWith("/threads/thread-one", {}, expect.any(AbortSignal));
    expect(extensionInventory).toHaveBeenCalledWith("run-one", expect.any(AbortSignal));
    expect(extensionInventory).not.toHaveBeenCalledWith("", expect.anything());
    expect(screen.getByText(/关闭会停用对应扩展或安装/)).toBeInTheDocument();
  });

  it("does not silently fall back to global extension scope when the selected Thread fails to load", async () => {
    const extensionInventory = vi.fn();
    const codeIntelInventory = vi.fn();
    mount({ get: vi.fn().mockRejectedValue(new Error("not found")), extensionInventory, codeIntelInventory },
      "extensions", "missing-thread");
    expect(await screen.findByRole("alert")).toHaveTextContent("无法确定当前任务的扩展范围");
    expect(extensionInventory).not.toHaveBeenCalled();
    expect(codeIntelInventory).not.toHaveBeenCalled();
  });

  it("keeps the old plugins address mapped to the real global inventory when no task is selected", async () => {
    const extensionInventory = vi.fn().mockResolvedValue(emptyExtensions);
    const codeIntelInventory = vi.fn().mockResolvedValue(emptyCodeIntel);
    mount({ extensionInventory, codeIntelInventory }, "plugins");
    await waitFor(() => expect(extensionInventory).toHaveBeenCalledWith("", expect.any(AbortSignal)));
    expect(codeIntelInventory).toHaveBeenCalledWith("", expect.any(AbortSignal));
    expect(screen.getByText(/选择接入工作区以添加服务器/)).toBeInTheDocument();
  });

  it("requires explicit untrusted registration before web installation and keeps its key on retry", async () => {
    const user = userEvent.setup();
    const installSkillPackage = vi.fn().mockRejectedValueOnce(new Error("response unavailable"))
      .mockResolvedValueOnce({});
    mount({ hasSkillInstallation: true, installSkillPackage }, "skills");
    const file = new File(["zip"], "review.zip", { type: "application/zip" });
    Object.defineProperty(file, "arrayBuffer", { value: async () => new TextEncoder().encode("zip").buffer });
    fireEvent.change(screen.getByLabelText("选择 Skill ZIP 包"), { target: { files: [file] } });
    expect(installSkillPackage).not.toHaveBeenCalled();
    const install = screen.getByRole("button", { name: "安装 Skill 包" });
    expect(install).toBeDisabled();
    await user.click(screen.getByRole("checkbox", { name: /确认将此包以不受信任状态登记到 Code/ }));
    await user.click(install);
    await screen.findByText("response unavailable");
    await user.click(install);
    await screen.findByText("Skill 包已安装");
    expect(installSkillPackage).toHaveBeenCalledTimes(2);
    expect(installSkillPackage.mock.calls[0]).toEqual(installSkillPackage.mock.calls[1]);
    expect(installSkillPackage.mock.calls[0][0]).toEqual({ version: "skill_package_installation.v1",
      archive_base64: btoa("zip"), surface: "code", confirm_untrusted: true });
    expect(install).toBeDisabled();
  });

  it("keeps read-only installation closed while the desktop preview remains available", async () => {
    const user = userEvent.setup();
    const installSkillPackage = vi.fn();
    mount({ hasSkillInstallation: false, installSkillPackage }, "skills", "", true);
    expect(screen.getByText(/当前连接可预览包结构/)).toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: "预览 Skill 包" }));
    expect(screen.getByRole("dialog", { name: "Skill 包预览" })).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "安装" })).not.toBeInTheDocument();
    expect(installSkillPackage).not.toHaveBeenCalled();
  });

  it("lists registered Skill packages and gates removal behind explicit confirmation", async () => {
    const user = userEvent.setup();
    const item = { skill: "review.helper@1.0.0", surface: "code", profiles: ["review"],
      surfaces: ["code"], phases: ["deliver"], roles: ["root"], user_invocable: true,
      model_invocable: false, explicit_only: true, tool_dependencies: ["read_file"],
      content_sha256: "a".repeat(64), content_bytes: 24, content_token_upper_bound: 24,
      archive_sha256: "b".repeat(64), package_fingerprint: "c".repeat(64), archive_bytes: 512,
      uncompressed_bytes: 256, entry_count: 2, trust_class: "operator_installed_untrusted",
      risk_codes: [], status: "installed", object_verified: false, operator_confirmed: true,
      import_command_execution: false, import_network_access: false,
      import_provider_calls: false, run_selection_authorized: false,
      context_injection_authorized: false, tool_capability_grant: false,
      content_body_exposed: false, installed_by: "operator",
      created_at: "2026-10-15T00:00:00Z", completed_at: "2026-10-15T00:00:01Z" };
    const inventory = { protocol_version: "skill_package_inventory.v1", capability_grant: false,
      surface: "", profile: "", include_removed: false, items: [item], returned_count: 1,
      unverified_count: 1 };
    const listSkillPackages = vi.fn().mockResolvedValue(inventory);
    const removeSkillPackage = vi.fn().mockResolvedValue({});
    mount({ hasSkillInstallation: true, listSkillPackages, removeSkillPackage }, "skills");
    expect(await screen.findByText("review.helper@1.0.0")).toBeInTheDocument();
    expect(listSkillPackages).toHaveBeenCalledWith({ includeRemoved: false }, expect.any(AbortSignal));
    expect(screen.getByText(/包内容暂不可读/)).toBeInTheDocument();
    const remove = screen.getByRole("button", { name: "移除登记" });
    expect(remove).toBeDisabled();
    await user.click(screen.getByRole("checkbox", { name: /确认移除此技能包/ }));
    await user.click(remove);
    await screen.findByText(/已移除登记/);
    expect(removeSkillPackage).toHaveBeenCalledTimes(1);
    expect(removeSkillPackage.mock.calls[0][0]).toEqual({
      protocol_version: "skill_package_removal_request.v1", skill: "review.helper@1.0.0",
      confirm_remove: true,
    });
    expect(removeSkillPackage.mock.calls[0][1]).toMatch(/^skill-package-remove-/u);
  });

  it("keeps the Skill registry closed on a read-only web connection", async () => {
    const listSkillPackages = vi.fn();
    mount({ hasSkillInstallation: false, listSkillPackages }, "skills");
    expect(await screen.findByText(/连接具备 Skill 管理权限的服务后/)).toBeInTheDocument();
    expect(listSkillPackages).not.toHaveBeenCalled();
  });

  it("keeps one conversation default and optional prices under Model advanced settings", async () => {
    const user = userEvent.setup();
    const providerCredentialStatuses = vi.fn().mockResolvedValue({ items: [] });
    const selectModelRoute = vi.fn().mockResolvedValue({});
    const importPriceSnapshot = vi.fn().mockResolvedValue({});
    const { queryClient } = mount({ hasModelControl: true, hasControl: true, hasProviderCredentials: true,
      providerCredentialStatuses, selectModelRoute, importPriceSnapshot,
      modelAvailability: vi.fn().mockResolvedValue({ providers: [{ name: "sample", status: "available",
        models: ["one", "two"], harnesses: [] }], routes: [
        { name: "code", provider: "sample", model: "one", available: true, harness_ready: true },
        { name: "review", provider: "sample", model: "one", available: true, harness_ready: true },
      ] }), listPriceSnapshots: vi.fn().mockResolvedValue({ items: [] }) }, "models");
    const invalidate = vi.spyOn(queryClient, "invalidateQueries");
    expect(screen.queryByRole("textbox", { name: "价格文档" })).not.toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: /高级模型设置/ }));
    await user.selectOptions(await screen.findByRole("combobox", { name: "新对话默认模型" }), "sample/two");
    expect(screen.queryByRole("combobox", { name: "review 模型路由" })).not.toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: "保存新对话默认模型" }));
    await waitFor(() => expect(selectModelRoute).toHaveBeenCalledWith("code", {
      version: "model_route_control.v1", provider: "sample", model: "two" }));
    await waitFor(() => expect(invalidate).toHaveBeenCalledWith({
      queryKey: ["v2", "models", "available-routes"],
    }));
    await user.click(screen.getByText("费用上限所用价格（可选）"));
    fireEvent.change(screen.getByRole("textbox", { name: "价格文档" }), { target: { value: '{"source":"review"}' } });
    await user.click(screen.getByRole("button", { name: "导入价格快照" }));
    await waitFor(() => expect(importPriceSnapshot).toHaveBeenCalledWith({ version: "price_snapshot.v1",
      document: '{"source":"review"}' }, expect.any(String)));
    expect(providerCredentialStatuses).toHaveBeenCalledTimes(1);
    expect(screen.queryByRole("heading", { name: "系统凭证" })).not.toBeInTheDocument();
  });

  it("opens the advanced panel from an old settings address", async () => {
    mount({ modelAvailability: vi.fn().mockResolvedValue({ providers: [], routes: [] }),
      listPriceSnapshots: vi.fn().mockResolvedValue({ items: [] }) }, "advanced-models");
    expect(screen.getByRole("heading", { name: "模型", level: 1 })).toBeInTheDocument();
    expect(screen.getByRole("button", { name: /高级模型设置/ })).toHaveAttribute("aria-expanded", "true");
    expect(await screen.findByRole("region", { name: "高级模型设置" })).toBeInTheDocument();
  });

  it("shows the old advanced address while preserving an unsaved provider draft", async () => {
    const user = userEvent.setup();
    const { rerenderSection } = mount({ hasProviderDefinitions: true, hasModelControl: true,
      providerDefinitions: vi.fn().mockResolvedValue({ providers: [] }),
      modelAvailability: vi.fn().mockResolvedValue({ providers: [], routes: [] }),
      listPriceSnapshots: vi.fn().mockResolvedValue({ items: [] }) }, "models");
    await user.click(screen.getByRole("button", { name: /DeepSeek/ }));
    await user.click(await screen.findByRole("button", { name: "高级设置：自定义连接、模型与搜索" }));
    const displayName = await screen.findByRole("textbox", { name: "显示名称" });
    await user.clear(displayName);
    await user.type(displayName, "尚未保存的名称");
    rerenderSection("advanced-models");
    await waitFor(() => expect(screen.getByRole("button", { name: /高级模型设置/ }))
      .toHaveAttribute("aria-expanded", "true"));
    expect(screen.getByRole("textbox", { name: "显示名称" })).toHaveValue("尚未保存的名称");
    expect(await screen.findByRole("region", { name: "高级模型设置" })).toBeInTheDocument();
  });

  it("uses existing local Inspector preferences without making a service request", async () => {
    const user = userEvent.setup();
    mount({}, "inspector");
    await user.click(screen.getByRole("button", { name: "紧凑" }));
    await user.click(screen.getByRole("button", { name: "完整诊断" }));
    expect(window.localStorage.getItem("prayu.ui-density")).toBe("compact");
    expect(document.documentElement.dataset.prayuDensity).toBe("compact");
    expect(window.localStorage.getItem("prayu.run-navigation.v1")).toBe("diagnostic");
  });

  it("reports unknown versions on health failure instead of displaying a fabricated development version", async () => {
    const safeWebReadiness = vi.fn();
    mount({ health: vi.fn().mockRejectedValue(new Error("offline")), safeWebReadiness }, "about");
    expect(await screen.findByRole("alert")).toHaveTextContent("版本未知");
    expect(screen.queryByText("dev")).not.toBeInTheDocument();
    expect(safeWebReadiness).not.toHaveBeenCalled();
  });

  it("clears only the web interface connection and its cache without sending a task stop", async () => {
    const user = userEvent.setup();
    useConnectionStore.setState({ token: "test-read-connection", controlToken: "test-control-connection" });
    const interruptThread = vi.fn();
    const controlRunLifecycle = vi.fn();
    const { queryClient } = mount({ health: vi.fn().mockResolvedValue({ status: "ok", app_version: "test",
      api_version: "api.v1", schema_version: 157 }), interruptThread, controlRunLifecycle }, "about");
    queryClient.setQueryData(["test-connection-cache"], { id: "saved" });
    await screen.findByText("test");
    expect(screen.getByText(/任务和已受理操作会继续在服务中运行/)).toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: "断开连接" }));
    expect(queryClient.getQueryData(["test-connection-cache"])).toBeUndefined();
    expect(useConnectionStore.getState().token).toBe("");
    expect(useConnectionStore.getState().controlToken).toBe("");
    expect(interruptThread).not.toHaveBeenCalled();
    expect(controlRunLifecycle).not.toHaveBeenCalled();
  });

  it("does not offer web disconnection in the native settings surface", async () => {
    mount({ health: vi.fn().mockResolvedValue({ status: "ok", app_version: "native-test",
      api_version: "api.v1", schema_version: 157 }) }, "about", "", true);
    await screen.findByText("native-test");
    expect(screen.queryByRole("button", { name: "断开连接" })).not.toBeInTheDocument();
  });
});
