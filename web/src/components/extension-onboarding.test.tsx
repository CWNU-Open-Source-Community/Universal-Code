import { webcrypto } from "node:crypto";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, expect, it, vi } from "vitest";
import { APIClient } from "../api/client";
import type { ExtensionInventoryView } from "../api/types";
import { LocaleProvider } from "../lib/locale";
import { lspConfiguration, lspTest, mcpServer, plugin } from "../test/extension-onboarding-fixtures";
import { ExtensionSettings } from "./shared-settings-panels";
import { MCPRegistrationForm, PluginImportForm } from "./extension-onboarding";
import { V2ExtensionSettings } from "../v2/components/advanced-settings";

afterEach(() => vi.unstubAllGlobals());
function respond(data: unknown) { return new Response(JSON.stringify({ version: "api.v1", request_id: "onboarding", data }),
  { status: 200, headers: { "Content-Type": "application/json" } }); }
function mount(children: React.ReactNode) {
  window.localStorage.setItem("prayu.locale.v1", "zh-CN");
  const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false }, mutations: { retry: false } } });
  return render(<QueryClientProvider client={queryClient}><LocaleProvider>{children}</LocaleProvider></QueryClientProvider>);
}
const emptyLSP = { protocol_version: "code-intel-lsp.v1", enabled: true, qualifications: [], servers: [], configurations: [] };

it.each(["sse", "websocket"])("registers a %s server through the normal API parser", async (transport) => {
  const user = userEvent.setup();
  const target = transport === "websocket" ? "wss://example.invalid/mcp" : "https://example.invalid/events";
  const requests: Record<string, unknown>[] = [];
  vi.stubGlobal("fetch", vi.fn(async (_url: string, init?: RequestInit) => {
    const request = JSON.parse(String(init?.body));
    requests.push(request.descriptor);
    return respond({ protocol_version: "extension-onboarding.v1", replayed: false, next_step: "approve_discovery",
      server: { ...mcpServer(), ...request.descriptor, protocol_version: "mcp-client-server.v1" } });
  }));
  mount(<MCPRegistrationForm client={new APIClient("read", "/api/v1", "control")} enabled workspaceID="project-one" runID="run-one" />);
  await user.selectOptions(screen.getByLabelText("传输方式"), transport);
  await user.type(screen.getByLabelText("Server ID"), "first-mcp");
  await user.type(screen.getByLabelText("显示名称"), "First MCP");
  await user.type(screen.getByLabelText(transport === "websocket" ? "服务器地址（WSS / 本机 WS）" : "服务器地址（HTTPS / 本机 HTTP）"), target);
  await user.type(screen.getByLabelText("固定非敏感 HTTP header（每行 Name: value）"), "X-Project: one");
  await user.type(screen.getByLabelText("凭据名称（认证时填写，登记后输入令牌）"), "server-secret");
  await user.click(screen.getByRole("button", { name: "提交登记" }));
  expect(await screen.findByText(/登记返回状态/)).toHaveTextContent("staged");
  expect(requests).toEqual([expect.objectContaining({ transport, target, headers: { "X-Project": "one" }, credential_ref: "server-secret" })]);
});

it("shows prompt required and optional parameters before enabling discovered capabilities", async () => {
  const current = inventory();
  const server = mcpServer("capabilities_pending");
  server.capabilities = { negotiated: ["prompts", "resources"], tools: [], resources: ["notes://guide"], resource_templates: ["notes://{topic}"], prompts: ["prepare"],
    fingerprint: "b".repeat(64), prompt_details: [{ name: "prepare", arguments: [{ name: "topic", description: "Subject to prepare", required: true }, { name: "style", required: false }] }] };
  current.mcp_servers = [server];
  const fetchMock = vi.fn(async (url: string, _init?: RequestInit) => respond(url.split("?")[0].endsWith("/code-intel") ? { ...emptyLSP, workspace_id: "project-one" } : current));
  vi.stubGlobal("fetch", fetchMock);
  mount(<ExtensionSettings client={new APIClient("read", "/api/v1", "control")} selectedRunID="" selectedWorkspaceID="project-one" />);
  await userEvent.click(await screen.findByText("Prompt: prepare"));
  expect(screen.getByText("topic").closest("li")).toHaveTextContent("必填 · Subject to prepare");
  expect(screen.getByText("style").closest("li")).toHaveTextContent("可选");
  expect(screen.getByText("Resource templates: notes://{topic}")).toBeInTheDocument();
  expect(screen.getByRole("button", { name: "审查并启用能力" })).toBeDisabled();
  expect(fetchMock.mock.calls.every(([, init]) => !init?.method || init.method === "GET")).toBe(true);
});
function inventory(): ExtensionInventoryView { return { protocol_version: "extension-inventory.v1", workspace_id: "project-one",
  mcp_servers: [], mcp_calls: [], plugins: [], onboarding: { mcp_registration: true, plugin_import: true, lsp_configuration: true } }; }

it("fetches only after explicit pinned-source submission and retains the pin on retry", async () => {
  const user = userEvent.setup();
  const importPluginPackage = vi.fn().mockRejectedValueOnce(new Error("fetch unavailable"))
    .mockResolvedValueOnce({ installation: plugin(), replayed: false });
  mount(<PluginImportForm client={{ importPluginPackage } as unknown as APIClient} enabled sourceEnabled />);
  await user.click(screen.getByText("导入 Plugin ZIP"));
  await user.selectOptions(screen.getByRole("combobox", { name: "导入来源" }), "git");
  await user.type(screen.getByRole("textbox", { name: "Git 仓库 HTTPS 地址" }), "https://example.invalid/repo");
  await user.type(screen.getByRole("textbox", { name: /完整 Git commit/ }), "a".repeat(40));
  expect(importPluginPackage).not.toHaveBeenCalled();
  await user.click(screen.getByRole("button", { name: "获取并暂存 Plugin" }));
  await screen.findByRole("alert");
  await user.click(screen.getByRole("button", { name: "获取并暂存 Plugin" }));
  await screen.findByRole("status");
  expect(importPluginPackage).toHaveBeenCalledTimes(2);
  expect(importPluginPackage.mock.calls[0]).toEqual(importPluginPackage.mock.calls[1]);
  expect(importPluginPackage.mock.calls[0][0]).toEqual({ version: "extension-control.v1", source: {
    kind: "git", url: "https://example.invalid/repo", commit: "a".repeat(40), archive_path: "plugin.zip",
  } });
});

it("keeps remote sources disabled for a service that only advertises ZIP import", async () => {
  mount(<PluginImportForm client={{} as APIClient} enabled />);
  await userEvent.click(screen.getByText("导入 Plugin ZIP"));
  expect(screen.getByRole("option", { name: "HTTPS URL + SHA-256" })).toBeDisabled();
  expect(screen.getByRole("option", { name: "Git + commit" })).toBeDisabled();
  expect(screen.getByRole("button", { name: "选择 Plugin ZIP" })).toBeEnabled();
});

it("registers MCP only on submit, reviews each pinned stage, and reads actual call metadata", async () => {
  const user = userEvent.setup();
  const current = inventory();
  current.run_id = "run-one";
  const mutations: { path: string; body: Record<string, unknown> }[] = [];
  const fetchMock = vi.fn(async (url: string, init?: RequestInit) => {
    const path = url.split("?")[0];
    if (init?.method !== "POST") return respond(path.endsWith("/code-intel") ? emptyLSP : current);
    const body = JSON.parse(String(init.body));
    mutations.push({ path, body });
    if (path.endsWith("/extensions/mcp")) {
      current.mcp_servers = [mcpServer()];
      return respond({ protocol_version: "extension-onboarding.v1", replayed: false, next_step: "approve_discovery", server: current.mcp_servers[0] });
    }
    if (path.endsWith("/refresh")) current.mcp_servers = [mcpServer("capabilities_pending")];
    else current.mcp_servers = [mcpServer(body.action === "approve_discovery" ? "discovery_approved" : "enabled")];
    return respond(current.mcp_servers[0]);
  });
  vi.stubGlobal("fetch", fetchMock);
  const openTask = vi.fn();
  mount(<ExtensionSettings client={new APIClient("read", "/api/v1", "control")} selectedRunID="run-one" onOpenTask={openTask} />);
  await screen.findByText(/当前任务尚无 MCP 调用记录/);
  await user.click(screen.getByText("登记 MCP Server"));
  await user.type(screen.getByRole("textbox", { name: "Server ID" }), "first-mcp");
  await user.type(screen.getByRole("textbox", { name: "显示名称" }), "First MCP");
  await user.type(screen.getByRole("textbox", { name: "服务器地址（HTTPS / 本机 HTTP）" }), "https://example.invalid/mcp");
  expect(mutations).toEqual([]);
  await user.click(screen.getByRole("button", { name: "提交登记" }));
  const discovery = await screen.findByRole("button", { name: "批准能力发现" });
  expect(discovery).toBeDisabled();
  expect(screen.getByText("核对登记").closest("li")).toHaveAttribute("aria-current", "step");
  expect(mutations).toHaveLength(1);
  expect(mutations[0].body).toMatchObject({ descriptor: { workspace_id: "project-one", scope: "workspace" } });
  await user.click(screen.getByRole("checkbox", { name: "我已核对描述符，允许连接并发现能力" }));
  await user.click(discovery);
  await screen.findByText(/发现审查已通过/);
  expect(screen.getByText("批准并发现").closest("li")).toHaveAttribute("aria-current", "step");
  expect(mutations).toHaveLength(2);
  expect(mutations[1].body).toMatchObject({ action: "approve_discovery", expected_descriptor_fingerprint: "a".repeat(64) });
  await user.click(screen.getByRole("button", { name: "重新发现" }));
  const enable = await screen.findByRole("button", { name: "审查并启用能力" });
  expect(enable).toBeDisabled();
  expect(screen.getByText("审查并启用").closest("li")).toHaveAttribute("aria-current", "step");
  expect(screen.getByText("lookup")).toBeInTheDocument();
  await user.click(screen.getByRole("checkbox", { name: "我已核对当前能力指纹与所有发现列表" }));
  await user.click(enable);
  await user.click(await screen.findByRole("button", { name: "生成首次调用任务草稿" }));
  expect(screen.getByText("任务中调用").closest("li")).toHaveAttribute("aria-current", "step");
  expect((screen.getByRole("textbox", { name: "调用任务草稿（复制到任务中发送）" }) as HTMLTextAreaElement).value).toContain("first-mcp");
  await user.click(screen.getByRole("button", { name: "打开任务输入区" }));
  expect(openTask).toHaveBeenCalledTimes(1);
  expect(openTask).toHaveBeenCalledWith("project-one");
  expect(mutations).toHaveLength(4);
  current.mcp_calls.push({ id: "actual-call", run_id: "run-one", workspace_id: "project-one", server_id: "first-mcp", tool_name: "lookup",
    capability_fingerprint: "b".repeat(64), arguments_sha256: "c".repeat(64), status: "completed", result_bytes: 65, truncated: false,
    started_at: "2026-10-07T01:00:00Z", completed_at: "2026-10-07T01:00:01Z" });
  await user.click(screen.getByRole("button", { name: "刷新" }));
  expect(await screen.findByText("调用状态: completed")).toBeInTheDocument();
  expect(screen.getByText(/在任务消息和证据中查看具体返回内容/)).toBeInTheDocument();
});

it("preserves the Plugin file and exact upload on failure, then requires separate untrusted review and enable", async () => {
  const user = userEvent.setup();
  vi.stubGlobal("crypto", webcrypto);
  const current = inventory();
  const uploads: unknown[] = [];
  const reviews: Record<string, unknown>[] = [];
  vi.stubGlobal("fetch", vi.fn(async (url: string, init?: RequestInit) => {
    const path = url.split("?")[0];
    if (init?.method !== "POST") return respond(path.endsWith("/code-intel") ? emptyLSP : current);
    const body = JSON.parse(String(init.body));
    if (path.endsWith("/import")) {
      uploads.push(body);
      if (uploads.length === 1) throw new Error("response unavailable");
      current.plugins = [{ ...plugin(), archive_sha256: body.archive_sha256 }];
      return respond({ protocol_version: "extension-onboarding.v1", replayed: false, next_step: "approve", installation: current.plugins[0] });
    }
    reviews.push(body);
    current.plugins = [{ ...current.plugins[0], state: body.action === "approve" ? "approved" : "enabled", generation: current.plugins[0].generation + 1,
      enabled_capabilities: body.capabilities ?? [] }];
    return respond(current.plugins[0]);
  }));
  mount(<ExtensionSettings client={new APIClient("read", "/api/v1", "control")} selectedRunID="" selectedWorkspaceID="project-one" />);
  await screen.findByText(/此范围尚未登记 MCP Server/);
  await user.click(screen.getByText("导入 Plugin ZIP"));
  const file = new File(["zip"], "first-plugin.zip", { type: "application/zip" });
  Object.defineProperty(file, "arrayBuffer", { value: async () => new TextEncoder().encode("zip").buffer });
  fireEvent.change(screen.getByLabelText("选择 Plugin ZIP"), { target: { files: [file] } });
  expect(uploads).toEqual([]);
  const stage = screen.getByRole("button", { name: "暂存 Plugin 包" });
  expect(stage).toBeEnabled();
  expect(screen.queryByRole("checkbox", { name: /确认将此文件作为不受信任的 Plugin 暂存/ })).not.toBeInTheDocument();
  await user.click(stage);
  await screen.findByRole("alert");
  expect(screen.getByText("first-plugin.zip · 3 bytes")).toBeInTheDocument();
  expect(stage).toBeEnabled();
  await user.click(stage);
  expect(await screen.findByRole("button", { name: "审查 Plugin 包" })).toBeDisabled();
  expect(uploads[0]).toEqual(uploads[1]);
  expect(reviews).toEqual([]);
  await user.click(screen.getByRole("checkbox", { name: /我已核对包指纹/ }));
  await user.click(screen.getByRole("button", { name: "审查 Plugin 包" }));
  const enable = await screen.findByRole("button", { name: "启用所选能力" });
  expect(enable).toBeDisabled();
  await user.click(within(screen.getByRole("group", { name: "启用 Plugin 能力" })).getByRole("checkbox", { name: "hooks" }));
  await user.click(screen.getByRole("checkbox", { name: /我已核对包指纹/ }));
  await user.click(enable);
  await screen.findByText(/已启用的贡献可由任务加载/);
  expect(reviews).toEqual([
    expect.objectContaining({ action: "approve", confirm_untrusted: true, expected_generation: 1 }),
    expect.objectContaining({ action: "enable", confirm_untrusted: true, capabilities: ["hooks"], expected_generation: 2 }),
  ]);
});

it("stages and reviews LSP explicitly before an actual readonly test, keeping failed query input", async () => {
  const user = userEvent.setup();
  let configurations = [] as ReturnType<typeof lspConfiguration>[];
  let probes = 0;
  const calls: string[] = [];
  vi.stubGlobal("fetch", vi.fn(async (url: string, init?: RequestInit) => {
    const path = url.split("?")[0];
    if (init?.method !== "POST") return respond(path.endsWith("/code-intel") ? { ...emptyLSP, configurations } : inventory());
    calls.push(path);
    if (path.endsWith("/configurations")) { configurations = [lspConfiguration()]; return respond(configurations[0]); }
    if (path.endsWith("/review")) { configurations = [lspConfiguration(true)]; return respond(configurations[0]); }
    if (++probes === 1) throw new Error("server unavailable");
    return respond(lspTest());
  }));
  mount(<ExtensionSettings client={new APIClient("read", "/api/v1", "control")} selectedRunID="" selectedWorkspaceID="project-one" />);
  await user.click(await screen.findByText("配置本地 LSP"));
  await waitFor(() => expect(screen.getByRole("textbox", { name: "LSP Server ID" })).toBeEnabled());
  await user.type(screen.getByRole("textbox", { name: "LSP Server ID" }), "first-lsp");
  await user.type(screen.getByRole("textbox", { name: "LSP 显示名称" }), "First LSP");
  await user.type(screen.getByRole("textbox", { name: "LSP 可执行文件绝对路径" }), "C:\\tools\\server.exe");
  await user.type(screen.getByRole("textbox", { name: "可执行文件 SHA-256" }), "a".repeat(64));
  await user.type(screen.getByRole("textbox", { name: "语言 ID" }), "typescript");
  await user.type(screen.getByRole("textbox", { name: "文件后缀（逗号分隔）" }), ".ts");
  expect(calls).toEqual([]);
  await user.click(screen.getByRole("button", { name: "登记 LSP 配置" }));
  const review = await screen.findByRole("button", { name: "审查并保存 LSP 配置" });
  expect(review).toBeDisabled();
  expect(screen.queryByRole("button", { name: "执行一次 LSP 只读测试" })).not.toBeInTheDocument();
  await user.click(screen.getByRole("checkbox", { name: /我已核对当前 LSP 描述符/ }));
  await user.click(review);
  const input = await screen.findByRole("textbox", { name: "工作区内相对文件路径" });
  await user.type(input, "src/main.ts");
  expect(calls).toHaveLength(2);
  const probe = screen.getByRole("button", { name: "执行一次 LSP 只读测试" });
  await user.click(probe);
  await screen.findByText("server unavailable");
  expect(input).toHaveValue("src/main.ts");
  await user.click(probe);
  const result = await screen.findByRole("region", { name: "LSP 实际查询结果" });
  expect(within(result).getByText(/实际只读查询已返回/)).toBeInTheDocument();
  expect(within(result).getByText("main · src/main.ts:1:1")).toBeInTheDocument();
});

it("disables onboarding when the backend capability object is absent even with a control token", async () => {
  const user = userEvent.setup();
  const readOnly = inventory();
  delete readOnly.onboarding;
  vi.stubGlobal("fetch", vi.fn(async (url: string) => respond(url.includes("/code-intel") ? emptyLSP : readOnly)));
  mount(<ExtensionSettings client={new APIClient("read", "/api/v1", "control")} selectedRunID="" selectedWorkspaceID="project-one" />);
  await user.click(await screen.findByText("登记 MCP Server"));
  expect(screen.getByRole("textbox", { name: "Server ID" })).toBeDisabled();
  await user.click(screen.getByText("导入 Plugin ZIP"));
  expect(screen.getByRole("button", { name: "选择 Plugin ZIP" })).toBeDisabled();
  await user.click(screen.getByText("配置本地 LSP"));
  expect(screen.getByRole("textbox", { name: "LSP Server ID" })).toBeDisabled();
  expect(screen.getByText(/使用显式 LSP 配置文件/)).toBeInTheDocument();
});

it("allows a first workspace without a Run and isolates inputs when the selected workspace changes", async () => {
  const user = userEvent.setup();
  const extensionInventory = vi.fn().mockResolvedValue(inventory());
  const client = { extensionInventory, codeIntelInventory: vi.fn().mockResolvedValue(emptyLSP), hasExtensionControl: true } as unknown as APIClient;
  mount(<V2ExtensionSettings client={client} threadID="" workspaces={[{ id: "project-one", name: "One", created_at: "2026-10-07T01:00:00Z" },
    { id: "project-two", name: "Two", created_at: "2026-10-07T01:00:00Z" }]} />);
  await user.selectOptions(screen.getByRole("combobox", { name: "接入工作区" }), "project-one");
  await waitFor(() => expect(extensionInventory).toHaveBeenCalledWith("", expect.any(AbortSignal), "project-one"));
  await user.click(screen.getByText("登记 MCP Server"));
  await user.type(screen.getByRole("textbox", { name: "Server ID" }), "unsaved");
  await user.selectOptions(screen.getByRole("combobox", { name: "接入工作区" }), "project-two");
  await user.click(screen.getByText("登记 MCP Server"));
  expect(screen.getByRole("textbox", { name: "Server ID" })).toHaveValue("");
  expect(extensionInventory).toHaveBeenCalledWith("", expect.any(AbortSignal), "project-two");
});

it("keeps manual refresh within an unresolved Run scope instead of reading global LSP state", async () => {
  const user = userEvent.setup();
  const extensionInventory = vi.fn().mockRejectedValue(new Error("scope unavailable"));
  const codeIntelInventory = vi.fn();
  mount(<ExtensionSettings client={{ extensionInventory, codeIntelInventory } as unknown as APIClient} selectedRunID="run-one" />);
  await screen.findByText("scope unavailable");
  expect(screen.queryByText("正在读取扩展状态与接入能力…")).not.toBeInTheDocument();
  await user.click(screen.getByRole("button", { name: "刷新" }));
  await waitFor(() => expect(extensionInventory).toHaveBeenCalledTimes(2));
  expect(codeIntelInventory).not.toHaveBeenCalled();
});
