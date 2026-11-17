import { act, cleanup, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import type { ComponentProps } from "react";
import type { APIClient } from "../../api/client";
import type { WorkspaceView } from "../../api/types";
import { LocaleProvider, useLocale } from "../../lib/locale";
import { V2RecoveryProvider } from "../recovery-storage";
import { V2Composer, type V2ComposerDispatchContext } from "./composer";

afterEach(() => {
  cleanup();
  localStorage.clear();
});

const workspaces: WorkspaceView[] = [
  { id: "ws-alpha", name: "Universal Workbench", created_at: "2026-11-17T00:00:00Z" },
];

function createIntegratedClient(overrides: Partial<APIClient> = {}): APIClient {
  return {
    baseURL: "/api/v1",
    hasControl: true,
    hasThreadControl: true,
    hasPlanDelivery: true,
    hasEvidenceAttachment: true,
    hasExtensionControl: true,
    hasStandardCodePreset: true,
    repositoryState: vi.fn().mockResolvedValue({
      available: true,
      branch: "feat/workbench",
      head_commit: "8870a401",
      detached: false,
      clean: false,
      staged_count: 1,
      worktree_count: 2,
      untracked_count: 0,
    }),
    repositoryHistory: vi.fn().mockResolvedValue({
      available: true,
      branches: [
        { name: "feat/workbench", head: "8870a401", current: true },
        { name: "main", head: "7760b300", current: false },
        { name: "release/v2", head: "6650c200", current: false },
      ],
      commits: [],
    }),
    getWorkspaceProject: vi.fn().mockResolvedValue({
      workspace: workspaces[0],
      directory_path: "/repo/universal-code",
      trust_status: "trusted",
    }),
    getSandboxEnvironment: vi.fn().mockResolvedValue({
      backends: [
        { backend: "sbx", ready: true },
        { backend: "docker", ready: false },
      ],
    }),
    getThreadExecutionPermission: vi.fn().mockResolvedValue({
      thread_id: "thread-1",
      execution_permission: { approval_mode: "auto", revision: 2 },
    }),
    listSkillPackages: vi.fn().mockResolvedValue({
      items: [
        { id: "skill-1", name: "modern-web" },
        { id: "skill-2", name: "go-test" },
      ],
    }),
    extensionInventory: vi.fn().mockResolvedValue({
      mcp_servers: [{ id: "mcp-git", name: "Git MCP" }],
      plugins: [],
    }),
    configureStandardCode: vi.fn().mockResolvedValue({
      backend_intent: "sbx",
    }),
    workspaceExplore: vi.fn().mockResolvedValue({
      root: ".",
      entries: [],
    }),
    ...overrides,
  } as unknown as APIClient;
}

function renderIntegratedComposer(
  props: Partial<ComponentProps<typeof V2Composer>> = {},
  clientOverrides: Partial<APIClient> = {},
) {
  localStorage.setItem("prayu.locale.v1", "zh-CN");
  localStorage.setItem("universal-code.composer-plain-text", "true");
  const client = props.client ?? createIntegratedClient(clientOverrides);
  const onSubmit = props.onSubmit ?? vi.fn(async () => undefined);
  const onIntegratedDispatch = props.onIntegratedDispatch ?? vi.fn();
  const queryClient = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  });

  const view = render(
    <LocaleProvider>
      <QueryClientProvider client={queryClient}>
        <V2RecoveryProvider client={{ baseURL: "/api/v1" }} scopeID="test-integrated-scope">
          <V2Composer
            client={client}
            onIntegratedDispatch={onIntegratedDispatch}
            onSubmit={onSubmit}
            onWorkspaceChange={vi.fn()}
            threadID=""
            workspaceID="ws-alpha"
            workspaces={workspaces}
            {...props}
          />
        </V2RecoveryProvider>
      </QueryClientProvider>
    </LocaleProvider>,
  );

  return { ...view, client, onSubmit, onIntegratedDispatch, queryClient };
}

describe("V2ComposerIntegratedBar", () => {
  it("loads current Git branch, switches existing branch, creates custom branch, and resets cleanly", async () => {
    const user = userEvent.setup();
    const { client } = renderIntegratedComposer();

    const branchTrigger = await screen.findByRole("button", {
      name: "切换 Git 分支: feat/workbench",
    });
    expect(branchTrigger).toBeInTheDocument();
    expect(client.repositoryState).toHaveBeenCalledWith("ws-alpha", expect.any(AbortSignal));

    await user.click(branchTrigger);
    const popover = await screen.findByRole("dialog", { name: "切换 Git 分支" });
    expect(within(popover).getByText("3 处本地改动")).toBeInTheDocument();

    const releaseOption = await within(popover).findByRole("button", { name: /release\/v2/ });
    await user.click(releaseOption);

    const switchedTrigger = screen.getByRole("button", {
      name: "切换 Git 分支: release/v2",
    });
    expect(within(switchedTrigger).getByText("待切换")).toBeInTheDocument();

    // Create a brand-new custom branch via the search input
    await user.click(switchedTrigger);
    const searchInput = screen.getByRole("searchbox", { name: "搜索或输入分支名称" });
    await user.type(searchInput, "feat/custom-agent-flow");
    await user.click(
      screen.getByRole("button", { name: '新建并切换分支 "feat/custom-agent-flow"' }),
    );

    const createdTrigger = screen.getByRole("button", {
      name: "切换 Git 分支: feat/custom-agent-flow",
    });
    expect(within(createdTrigger).getByText("新建")).toBeInTheDocument();

    // Reset back to repository branch
    await user.click(createdTrigger);
    await user.click(
      screen.getByRole("button", { name: "恢复跟随仓库分支 (feat/workbench)" }),
    );
    expect(
      screen.getByRole("button", { name: "切换 Git 分支: feat/workbench" }),
    ).toBeInTheDocument();
  });

  it("falls back gracefully when workspace Git state is offline or uninitialized", async () => {
    const user = userEvent.setup();
    renderIntegratedComposer(
      {},
      {
        repositoryState: vi.fn().mockResolvedValue({
          available: false,
          branch: "",
          head_commit: "",
          detached: false,
          clean: true,
          staged_count: 0,
          worktree_count: 0,
          untracked_count: 0,
        }),
      },
    );

    const branchTrigger = screen.getByRole("button", { name: "切换 Git 分支: main" });
    await user.click(branchTrigger);
    const popover = screen.getByRole("dialog", { name: "切换 Git 分支" });
    expect(
      within(popover).getByText("离线或未初始化 Git · 发送前预置分支"),
    ).toBeInTheDocument();

    const searchInput = within(popover).getByRole("searchbox", {
      name: "搜索或输入分支名称",
    });
    await user.type(searchInput, "feat/offline-branch{Enter}");
    expect(
      screen.getByRole("button", { name: "切换 Git 分支: feat/offline-branch" }),
    ).toBeInTheDocument();
  });

  it("toggles isolated Git Worktree, previews/customizes worktree slug, and persists across remounts", async () => {
    const user = userEvent.setup();
    const firstMount = renderIntegratedComposer();

    const worktreeBtn = screen.getByRole("button", { name: "开启独立工作树" });
    expect(worktreeBtn).toHaveAttribute("aria-pressed", "false");

    const editor = screen.getByRole("textbox", { name: "开始新对话" });
    await user.type(editor, "Refactor parser pipeline");

    await user.click(worktreeBtn);
    expect(worktreeBtn).toHaveAttribute("aria-pressed", "true");

    const configBtn = screen.getByRole("button", { name: "配置独立工作树" });
    expect(configBtn).toHaveTextContent("wt-refactor-parser-pipeline");

    await user.click(configBtn);
    const wtDialog = screen.getByRole("dialog", { name: "独立工作树设置" });
    const nameInput = within(wtDialog).getByRole("textbox", { name: "工作树名称" });
    await user.type(nameInput, "parser-v2");
    expect(within(wtDialog).getByText("parser-v2")).toBeInTheDocument();
    expect(within(wtDialog).getByText("worktree/parser-v2")).toBeInTheDocument();

    firstMount.unmount();

    // Remount under same workspace + recovery scope: worktree setting persists
    renderIntegratedComposer();
    expect(screen.getByRole("button", { name: "开启独立工作树" })).toHaveAttribute(
      "aria-pressed",
      "true",
    );
    expect(screen.getByRole("button", { name: "配置独立工作树" })).toHaveTextContent(
      "parser-v2",
    );
  });

  it("consolidates workspace root, approval policy, runtime backend, phase, editor mode, and tools in unified popover", async () => {
    const user = userEvent.setup();
    const onPhaseChange = vi.fn();
    const onOpenTaskConfiguration = vi.fn();
    const onStop = vi.fn(() => true);

    renderIntegratedComposer({
      onPhaseChange,
      onOpenTaskConfiguration,
      onStop,
      phase: "deliver",
      approvalMode: "auto",
    });

    const settingsBtn = screen.getByRole("button", { name: "项目设置与工具" });
    await user.click(settingsBtn);

    const dialog = await screen.findByRole("dialog", { name: "项目设置与工具" });
    expect(await within(dialog).findByText("/repo/universal-code")).toBeInTheDocument();
    expect(within(dialog).getByText("帮我批准")).toBeInTheDocument();
    expect(await within(dialog).findByText("已安装技能 2")).toBeInTheDocument();
    expect(await within(dialog).findByText("MCP 服务 1")).toBeInTheDocument();

    // Switch cloud backend directly inside unified popover
    await user.click(within(dialog).getByRole("button", { name: "Docker" }));
    expect(screen.getByRole("button", { name: "云端环境" })).toHaveAttribute(
      "aria-pressed",
      "true",
    );
    expect(within(dialog).getByText(/云端沙盒 \(docker/)).toBeInTheDocument();

    // Switch task phase inside unified popover
    await user.click(within(dialog).getByRole("button", { name: "计划模式" }));
    expect(onPhaseChange).toHaveBeenCalledWith("plan");

    // Escape closes the unified popover and restores focus without triggering onStop
    await user.keyboard("{Escape}");
    expect(screen.queryByRole("dialog", { name: "项目设置与工具" })).not.toBeInTheDocument();
    expect(settingsBtn).toHaveFocus();
    expect(onStop).not.toHaveBeenCalled();

    // Reopen and trigger task budget configuration
    await user.click(settingsBtn);
    const reopened = await screen.findByRole("dialog", { name: "项目设置与工具" });
    await user.click(within(reopened).getByRole("button", { name: "任务预算配置" }));
    expect(onOpenTaskConfiguration).toHaveBeenCalledOnce();
  });

  it("keeps default onSubmit payload untouched, and dispatches structured context + cloud preset when overrides are enabled", async () => {
    const user = userEvent.setup();
    const onSubmit = vi.fn<ComponentProps<typeof V2Composer>["onSubmit"]>(async () => undefined);
    const onIntegratedDispatch = vi.fn<(ctx: V2ComposerDispatchContext) => void>();
    const { client } = renderIntegratedComposer({
      threadID: "thread-active",
      runID: "run-active-1",
      onSubmit,
      onIntegratedDispatch,
    });

    await screen.findByRole("button", { name: "切换 Git 分支: feat/workbench" });

    // 1. Default state: no branch override, worktree off, environment = local
    const editor = screen.getByRole("textbox", { name: "继续对话" });
    await user.type(editor, "先检查现有单元测试");
    await user.click(screen.getByRole("button", { name: "发送消息" }));

    await waitFor(() => expect(onSubmit).toHaveBeenCalledTimes(1));
    expect(onSubmit.mock.calls[0]?.[0]).toBe("先检查现有单元测试");
    expect(onIntegratedDispatch).not.toHaveBeenCalled();
    expect(client.configureStandardCode).not.toHaveBeenCalled();

    // 2. Enable isolated Worktree + Cloud environment and submit
    await user.click(screen.getByRole("button", { name: "开启独立工作树" }));
    await user.click(screen.getByRole("button", { name: "云端环境" }));

    fireEvent.change(editor, { target: { value: "在云端隔离工作树执行全量重构" } });
    await user.click(screen.getByRole("button", { name: "发送消息" }));

    await waitFor(() => expect(onSubmit).toHaveBeenCalledTimes(2));
    expect(onIntegratedDispatch).toHaveBeenCalledOnce();
    const dispatched = onIntegratedDispatch.mock.calls[0]?.[0];
    expect(dispatched).toEqual(
      expect.objectContaining({
        environment: "cloud",
        sandboxBackend: "sbx",
        hasOverrides: true,
        worktree: expect.objectContaining({
          enabled: true,
          name: "wt-isolated",
        }),
      }),
    );
    expect(client.configureStandardCode).toHaveBeenCalledWith(
      "run-active-1",
      "configure",
      {
        version: "standard_code_preset.v1",
        backend_intent: "sbx",
        confirm_workspace_trust: false,
      },
      expect.stringMatching(/^v2-composer-env-/),
    );
    expect(onSubmit.mock.calls[1]?.[0]).toContain("[执行环境配置 · 运行环境: 云端沙盒 (sbx)");
    expect(onSubmit.mock.calls[1]?.[0]).toContain("独立工作树: wt-isolated");
    expect(onSubmit.mock.calls[1]?.[0]).toContain("在云端隔离工作树执行全量重构");
  });

  it("supports keyboard arrow roving navigation and live locale switching", async () => {
    function LocaleToggle() {
      const { locale, setLocale } = useLocale();
      return (
        <button
          onClick={() => setLocale(locale === "zh-CN" ? "en-US" : "zh-CN")}
          type="button"
        >
          toggle-locale
        </button>
      );
    }

    localStorage.setItem("prayu.locale.v1", "zh-CN");
    localStorage.setItem("universal-code.composer-plain-text", "true");
    const client = createIntegratedClient();
    render(
      <LocaleProvider>
        <QueryClientProvider
          client={new QueryClient({ defaultOptions: { queries: { retry: false } } })}
        >
          <LocaleToggle />
          <V2Composer
            client={client}
            onSubmit={vi.fn(async () => undefined)}
            onWorkspaceChange={vi.fn()}
            threadID=""
            workspaceID="ws-alpha"
            workspaces={workspaces}
          />
        </QueryClientProvider>
      </LocaleProvider>,
    );

    const branchBtn = await screen.findByRole("button", {
      name: "切换 Git 分支: feat/workbench",
    });
    act(() => branchBtn.focus());
    fireEvent.keyDown(branchBtn, { key: "ArrowRight" });
    expect(screen.getByRole("button", { name: "开启独立工作树" })).toHaveFocus();

    await userEvent.setup().click(screen.getByRole("button", { name: "toggle-locale" }));
    expect(
      screen.getByRole("button", { name: "Switch Git branch: feat/workbench" }),
    ).toBeInTheDocument();
    expect(
      screen.getByRole("button", { name: "Enable isolated worktree" }),
    ).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Local" })).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Cloud" })).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Side Chat" })).toBeInTheDocument();
  });
});
