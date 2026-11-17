import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { cleanup, render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { APIClient } from "../../api/client";
import type {
  ExtensionMCPServerView,
  ExtensionPluginInstallationView,
  SkillPackageItemView,
} from "../../api/types";
import { LocaleProvider } from "../../lib/locale";
import {
  classifyExtensionCatalog,
  clearDismissedRecommendationsForTests,
  countToolsByRole,
  evaluateProactiveToolRecommendations,
  requestPluginRoleFilter,
} from "../plugin-recommendations";
import { V2ExtensionSettings } from "./advanced-settings";
import { V2PluginRecommendationBanner } from "./plugin-recommendation-banner";

function sampleMCPServer(
  overrides: Partial<ExtensionMCPServerView> & { id: string; name: string },
): ExtensionMCPServerView {
  const state = overrides.state ?? "enabled";
  return {
    protocol_version: "mcp-client-server.v1",
    workspace_id: "ws-1",
    scope: "workspace",
    transport: "stdio",
    target: "npx @playwright/mcp",
    state,
    health: state === "enabled" ? "healthy" : "unknown",
    generation: 1,
    declared_capabilities: ["tools"],
    descriptor_fingerprint: "f".repeat(64),
    capabilities: {
      negotiated: state === "staged" ? [] : ["tools"],
      fingerprint: "c".repeat(64),
      tools: state === "staged" ? [] : ["browser_navigate", "browser_screenshot"],
      resources: [],
      prompts: [],
    },
    source: { kind: "manual", uri: "stdio://playwright", version: "1.0.0" },
    created_at: "2026-11-18T00:00:00Z",
    updated_at: "2026-11-18T00:00:00Z",
    ...overrides,
  };
}

function samplePlugin(
  overrides: Partial<ExtensionPluginInstallationView> & {
    id: string;
    name: string;
    description: string;
  },
): ExtensionPluginInstallationView {
  return {
    id: overrides.id,
    protocol_version: "plugin.v1",
    state: overrides.state ?? "enabled",
    generation: 1,
    archive_sha256: "a".repeat(64),
    package_fingerprint: "b".repeat(64),
    signature_present: true,
    signature_valid: true,
    staged_by: "operator",
    enabled_capabilities: overrides.enabled_capabilities ?? ["security_scan"],
    manifest: {
      id: overrides.id,
      name: overrides.name,
      description: overrides.description,
      publisher: "Universal Labs",
      version: "1.2.0",
      capabilities: ["security_scan"],
    },
    source: { kind: "upload", uri: "file:///security.zip", version: "1.2.0" },
    created_at: "2026-11-18T00:00:00Z",
    updated_at: "2026-11-18T00:00:00Z",
  };
}

describe("Role-based plugin filtering & proactive in-chat recommendations", () => {
  beforeEach(() => {
    window.localStorage.setItem("prayu.locale.v1", "zh-CN");
    clearDismissedRecommendationsForTests();
  });

  afterEach(() => {
    cleanup();
    clearDismissedRecommendationsForTests();
    window.localStorage.clear();
  });

  it("classifies plugins, MCP servers, LSPs, and skills into role buckets accurately", () => {
    const catalog = classifyExtensionCatalog({
      mcpServers: [
        sampleMCPServer({ id: "playwright", name: "Playwright Browser MCP" }),
        sampleMCPServer({
          id: "pg-mcp",
          name: "PostgreSQL Schema Inspector",
          target: "postgres://localhost/db",
          source: { kind: "manual", uri: "postgres://localhost/db", version: "1.0.0" },
          capabilities: {
            negotiated: ["tools"],
            fingerprint: "d".repeat(64),
            tools: ["query_sql"],
            resources: [],
            prompts: [],
          },
        }),
      ],
      plugins: [
        samplePlugin({
          id: "sec-guard",
          name: "OWASP Security Auditor",
          description: "Vulnerability and secret leak review plugin",
        }),
      ],
      skills: [
        {
          skill: "figma.design-tokens@1.0.0",
          surface: "code",
          surfaces: ["code"],
          profiles: ["frontend", "ui"],
          phases: ["deliver"],
          roles: ["root"],
          tool_dependencies: ["read_file"],
          status: "installed",
          operator_confirmed: true,
        } as unknown as SkillPackageItemView,
      ],
    });

    const counts = countToolsByRole(catalog);
    expect(counts.all).toBe(4);
    expect(counts.qa_browser).toBe(1);
    expect(counts.backend_data).toBe(1);
    expect(counts.review_security).toBe(1);
    expect(counts.frontend_ui).toBe(1);
  });

  it("renders one-click Role Filter Pills on Plugins page, filters tools, and shows empty-state presets", async () => {
    const user = userEvent.setup();
    const client = {
      hasSkillInstallation: true,
      extensionInventory: vi.fn().mockResolvedValue({
        mcp_servers: [
          sampleMCPServer({ id: "playwright", name: "Playwright Browser QA" }),
          sampleMCPServer({
            id: "context7",
            name: "Context7 Official Docs",
            target: "https://mcp.context7.com/mcp",
            source: { kind: "manual", uri: "https://mcp.context7.com/mcp", version: "1.0.0" },
            capabilities: {
              negotiated: ["tools"],
              fingerprint: "e".repeat(64),
              tools: ["search_docs"],
              resources: [],
              prompts: [],
            },
          }),
        ],
        mcp_calls: [],
        plugins: [],
        workspace_id: "",
      }),
      codeIntelInventory: vi.fn().mockResolvedValue({ servers: [], qualifications: [] }),
      listSkillPackages: vi.fn().mockResolvedValue({
        protocol_version: "skill_package_inventory.v1",
        items: [],
      }),
    } as unknown as APIClient;

    const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    render(
      <QueryClientProvider client={queryClient}>
        <LocaleProvider>
          <V2ExtensionSettings client={client} threadID="" workspaces={[]} />
        </LocaleProvider>
      </QueryClientProvider>,
    );

    const roleGroup = screen.getByRole("group", { name: "工具角色分类" });
    for (const label of [
      "全部",
      "前端与设计",
      "后端与数据",
      "测试与浏览器",
      "代码审查与安全",
      "文档与检索",
      "运维与自动化",
    ]) {
      expect(within(roleGroup).getByRole("button", { name: new RegExp(label) })).toBeVisible();
    }

    const matchedGrid = () => screen.getByLabelText("匹配的角色工具列表");
    await waitFor(() =>
      expect(within(matchedGrid()).getByText("Playwright Browser QA")).toBeVisible(),
    );
    expect(within(matchedGrid()).getByText("Context7 Official Docs")).toBeVisible();

    // Filter by "测试与浏览器" -> only Playwright remains in role directory
    await user.click(within(roleGroup).getByRole("button", { name: /测试与浏览器/ }));
    expect(within(roleGroup).getByRole("button", { name: /测试与浏览器/ })).toHaveAttribute(
      "aria-pressed",
      "true",
    );
    expect(within(matchedGrid()).getByText("Playwright Browser QA")).toBeVisible();
    expect(within(matchedGrid()).queryByText("Context7 Official Docs")).not.toBeInTheDocument();

    // Filter by "后端与数据" (0 tools) -> shows role empty-state guidance with quick template preset
    await user.click(within(roleGroup).getByRole("button", { name: /后端与数据/ }));
    expect(screen.getByText(/当前「后端与数据」角色暂无已接入工具/)).toBeVisible();
    expect(screen.getByText("PostgreSQL / SQLite 数据结构 MCP")).toBeVisible();
  });

  it("matches contextual conversation intents, suppresses already-enabled tools, persists per-thread dismissal, and jumps to the exact role on Plugins page", async () => {
    const user = userEvent.setup();

    // 1. Unit verification: already-enabled Browser MCP does NOT nag, whereas disabled/missing does
    const enabledBrowserCatalog = classifyExtensionCatalog({
      mcpServers: [sampleMCPServer({ id: "pw", name: "Playwright MCP", state: "enabled" })],
    });
    expect(
      evaluateProactiveToolRecommendations({
        threadID: "thread-enabled",
        missionGoal: "请用浏览器打开登录页完成端到端截图测试",
        catalog: enabledBrowserCatalog,
      }),
    ).toEqual([]);

    const disabledBrowserCatalog = classifyExtensionCatalog({
      mcpServers: [sampleMCPServer({ id: "pw", name: "Playwright MCP", state: "disabled" })],
    });
    const disabledRecs = evaluateProactiveToolRecommendations({
      threadID: "thread-disabled",
      missionGoal: "请用浏览器打开登录页完成端到端截图测试",
      catalog: disabledBrowserCatalog,
    });
    expect(disabledRecs[0]).toMatchObject({
      role: "qa_browser",
      status: "disabled_installed",
      matchedToolName: "Playwright MCP",
    });

    // 2. Component verification: proactive banner render, jump-to-role handoff, and per-thread dismiss persistence
    const onConfigureRole = vi.fn((role) => requestPluginRoleFilter(role));
    const onApplyDraftHint = vi.fn();
    const client = {
      extensionInventory: vi.fn().mockResolvedValue({
        mcp_servers: [sampleMCPServer({ id: "pw", name: "Playwright MCP", state: "staged" })],
        mcp_calls: [],
        plugins: [],
        workspace_id: "ws-1",
      }),
      codeIntelInventory: vi.fn().mockResolvedValue({ servers: [], qualifications: [] }),
    } as unknown as APIClient;

    const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    const view = render(
      <QueryClientProvider client={queryClient}>
        <LocaleProvider>
          <V2PluginRecommendationBanner
            client={client}
            onApplyDraftHint={onApplyDraftHint}
            onConfigureRole={onConfigureRole}
            recentMessagesOverride={["我们需要测试网页弹窗交互并截图验证无障碍焦点"]}
            thread={{
              id: "thread-qa-1",
              title: "登录弹窗交互验收",
              workspace_id: "ws-1",
              active_run_id: "run-qa-1",
              last_run_id: "run-qa-1",
            }}
            threadID="thread-qa-1"
          />
        </LocaleProvider>
      </QueryClientProvider>,
    );

    const banner = await screen.findByRole("complementary", { name: "智能体工具推荐" });
    expect(within(banner).getByText("测试与浏览器")).toBeVisible();
    expect(within(banner).getAllByText(/Playwright MCP/)[0]).toBeVisible();

    // Click "前往启用插件" -> triggers jump-to-role handoff for qa_browser
    await user.click(within(banner).getByRole("button", { name: "前往启用插件" }));
    expect(onConfigureRole).toHaveBeenCalledWith("qa_browser");

    // Mounting V2ExtensionSettings consumes the handoff and pre-selects "测试与浏览器"
    const settingsRender = render(
      <QueryClientProvider client={queryClient}>
        <LocaleProvider>
          <V2ExtensionSettings client={client} threadID="" workspaces={[]} />
        </LocaleProvider>
      </QueryClientProvider>,
    );
    const settingsRoleGroup = within(settingsRender.container).getByRole("group", {
      name: "工具角色分类",
    });
    expect(
      within(settingsRoleGroup).getByRole("button", { name: /测试与浏览器/ }),
    ).toHaveAttribute("aria-pressed", "true");
    settingsRender.unmount();

    // Dismiss recommendation on thread-qa-1 -> disappears immediately and stays dismissed across remounts
    await user.click(within(banner).getByRole("button", { name: "忽略此工具推荐" }));
    expect(
      screen.queryByRole("complementary", { name: "智能体工具推荐" }),
    ).not.toBeInTheDocument();

    view.unmount();
    const remounted = render(
      <QueryClientProvider client={queryClient}>
        <LocaleProvider>
          <V2PluginRecommendationBanner
            client={client}
            onConfigureRole={onConfigureRole}
            recentMessagesOverride={["我们需要测试网页弹窗交互并截图验证无障碍焦点"]}
            threadID="thread-qa-1"
          />
        </LocaleProvider>
      </QueryClientProvider>,
    );
    expect(
      screen.queryByRole("complementary", { name: "智能体工具推荐" }),
    ).not.toBeInTheDocument();
    remounted.unmount();

    // Different thread ("thread-sql-2") still receives contextual SQL/Backend recommendation!
    render(
      <QueryClientProvider client={queryClient}>
        <LocaleProvider>
          <V2PluginRecommendationBanner
            client={client}
            onConfigureRole={onConfigureRole}
            recentMessagesOverride={["请帮我分析 PostgreSQL 慢查询并检查数据库表结构"]}
            threadID="thread-sql-2"
          />
        </LocaleProvider>
      </QueryClientProvider>,
    );
    const sqlBanner = await screen.findByRole("complementary", { name: "智能体工具推荐" });
    expect(within(sqlBanner).getByText("后端与数据")).toBeVisible();
  });
});
