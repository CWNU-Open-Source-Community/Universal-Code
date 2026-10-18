import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { fireEvent, render as renderComponent, screen, waitFor, within } from "@testing-library/react";
import type { ReactNode } from "react";
import userEvent from "@testing-library/user-event";
import type { APIClient } from "../../api/client";
import type { ThreadView } from "../../api/types";
import { V2Settings } from "./settings";
import { LocaleProvider } from "../../lib/locale";
import { sandboxEnvironmentFixture } from "../../test/sandbox-environment";

const render = (ui: ReactNode) => renderComponent(ui, { wrapper: LocaleProvider });

afterEach(() => {
  vi.unstubAllGlobals();
  window.localStorage.removeItem("prayu.locale.v1");
});

it("mounts real environment settings from the production settings section and reads all backends", async () => {
  const getSandboxEnvironment = vi.fn().mockResolvedValue(sandboxEnvironmentFixture());
  const saveSandboxEnvironment = vi.fn();
  render(<QueryClientProvider client={new QueryClient()}><V2Settings client={{ hasControl: true,
    getSandboxEnvironment, saveSandboxEnvironment,
  } as unknown as APIClient} section="environment" threadID="" workspaces={[]} onSelectSection={vi.fn()}
    onOpenInspector={vi.fn()} /></QueryClientProvider>);
  const panel = await screen.findByRole("region", { name: "执行环境设置" });
  const picker = await within(panel).findByRole("group", { name: "编码环境后端" });
  expect(within(picker).getAllByRole("button")).toHaveLength(3);
  expect(getSandboxEnvironment).toHaveBeenCalledOnce();
  expect(saveSandboxEnvironment).not.toHaveBeenCalled();
});

it("routes connection and environment tasks directly without inspecting or changing a resource", async () => {
  const onSelectSection = vi.fn();
  const onOpenTask = vi.fn();
  const get = vi.fn(); const postControl = vi.fn();
  render(<QueryClientProvider client={new QueryClient({ defaultOptions: { queries: { retry: false } } })}>
    <V2Settings client={{ get, postControl } as unknown as APIClient} section="connections"
    threadID="" workspaces={[]} onSelectSection={onSelectSection} onOpenInspector={vi.fn()}
    onOpenTask={onOpenTask} /></QueryClientProvider>);
  const nav = screen.getByRole("navigation", { name: "连接与环境设置" });
  const user = userEvent.setup();
  for (const label of ["模型连接", "任务预算与项目配置", "扩展与代码智能", "执行环境设置", "任务权限与执行环境", "应用连接与诊断"]) {
    await user.click(within(nav).getByRole("button", { name: new RegExp(label) }));
  }
  expect(onSelectSection.mock.calls).toEqual([["models"], ["task-configuration"], ["extensions"], ["environment"], ["permissions"], ["about"]]);
  await user.click(screen.getByRole("button", { name: "开始任务" }));
  expect(onOpenTask).toHaveBeenCalledExactlyOnceWith();
  expect(get).not.toHaveBeenCalled(); expect(postControl).not.toHaveBeenCalled();
  expect(within(nav).getByRole("button", { name: /GitHub 连接与审阅/ })).toBeDisabled();
});

it("opens GitHub tools for the explicit historical Run and never substitutes an unavailable Run", async () => {
  const get = vi.fn().mockResolvedValue({ thread: { id: "thread-current" },
    active_run: { id: "run-current" }, last_run: { id: "run-current" },
    runs: [{ run: { id: "run-history" } }, { run: { id: "run-current" } }] });
  const onOpenGithubReview = vi.fn();
  const client = { get } as unknown as APIClient;
  const queries = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  const draw = (sourceRunID: string) => <QueryClientProvider client={queries}><V2Settings client={client} section="connections"
    threadID="thread-current" sourceRunID={sourceRunID} workspaces={[]} onSelectSection={vi.fn()}
    onOpenInspector={vi.fn()} onOpenGithubReview={onOpenGithubReview} /></QueryClientProvider>;
  const view = render(draw("run-history"));
  const entry = screen.getByRole("button", { name: /GitHub 连接与审阅/u });
  await waitFor(() => expect(entry).toBeEnabled());
  expect(screen.getByText("run-history")).not.toBeVisible();
  await userEvent.click(screen.getByText("查看任务记录"));
  expect(screen.getByText("run-history")).toBeVisible();
  await userEvent.click(entry);
  expect(onOpenGithubReview).toHaveBeenCalledExactlyOnceWith("run-history");
  view.rerender(draw("run-missing"));
  expect(screen.getByRole("button", { name: /GitHub 连接与审阅/ })).toBeDisabled();
  expect(screen.getByText(/选择一条执行记录后进入 GitHub 审阅/)).toBeInTheDocument();
  expect(onOpenGithubReview).toHaveBeenCalledTimes(1);
});

function archivedThread(id: string, title: string, version: number): ThreadView {
  return {
    id, protocol_version: "thread.v1", workspace_id: `workspace-${id}`,
    mission_id: `mission-${id}`, title, status: "archived", last_run_id: `run-${id}`,
    version, composer_state: "unavailable", archived_at: "2026-08-29T02:00:00Z",
    created_at: "2026-08-29T00:00:00Z", updated_at: "2026-08-29T02:00:00Z",
  };
}

function renderArchived(threads: ThreadView[]) {
  const getPage = vi.fn().mockResolvedValue({ items: threads, page: { limit: 100 },
    requestID: "request-v2-archived" });
  const transitionThread = vi.fn().mockResolvedValue({});
  const client = { hasThreadControl: true, getPage,
    transitionThread } as unknown as APIClient;
  const queryClient = new QueryClient({ defaultOptions: {
    queries: { retry: false }, mutations: { retry: false },
  } });
  render(<QueryClientProvider client={queryClient}>
    <V2Settings client={client} onOpenInspector={vi.fn()} onSelectSection={vi.fn()}
      section="archived" threadID="thread-current" workspaces={[]} />
  </QueryClientProvider>);
  return { getPage, transitionThread };
}

function renderModels(configuredProviders: string[] = []) {
  const providerDefinitions = vi.fn().mockResolvedValue({
    version: "provider_definition_collection.v1",
    revision: 0,
    providers: [],
  });
  const providerCredentialStatuses = vi.fn().mockResolvedValue({
    protocol_version: "provider_credential.v1",
    items: configuredProviders.map((provider) => ({
      protocol_version: "provider_credential.v1",
      provider,
      configured: true,
      plaintext_returned: false,
      registry_generation: 1,
      registry_reloaded: false,
      restart_required: false,
      store_available: true,
      store_kind: "windows_credential_manager",
    })),
  });
  const client = {
    hasProviderDefinitions: true,
    hasProviderCredentials: true,
    providerDefinitions,
    providerCredentialStatuses,
  } as unknown as APIClient;
  const queryClient = new QueryClient({ defaultOptions: {
    queries: { retry: false }, mutations: { retry: false },
  } });
  render(<QueryClientProvider client={queryClient}>
    <V2Settings client={client} onOpenInspector={vi.fn()} onSelectSection={vi.fn()}
      section="models" threadID="thread-current" workspaces={[]} />
  </QueryClientProvider>);
  return { providerDefinitions, providerCredentialStatuses };
}

describe("V2 archived settings", () => {
  it("pages older archives without losing the current title filter", async () => {
    const user = userEvent.setup();
    const getPage = vi.fn(async (_path: string, _query: unknown, cursor: string) => ({
      items: cursor ? [archivedThread("older", "Older matching task", 2)] : Array.from({ length: 100 }, (_, index) =>
        archivedThread(`newer-${index}`, `Recent ${index}`, 1)),
      page: { limit: 100, next_cursor: cursor ? "" : "archive-cursor" }, requestID: "archive-page",
    }));
    render(<QueryClientProvider client={new QueryClient()}><V2Settings
      client={{ getPage } as unknown as APIClient} onOpenInspector={vi.fn()} onSelectSection={vi.fn()}
      section="archived" threadID="" workspaces={[]} /></QueryClientProvider>);
    await screen.findByText("Recent 0");
    await user.type(screen.getByRole("searchbox", { name: "搜索已归档的聊天" }), "matching");
    expect(screen.getByText("已加载的标题中没有匹配项")).toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: "加载更早归档" }));
    expect(await screen.findByText("Older matching task")).toBeInTheDocument();
    expect(screen.getByRole("searchbox", { name: "搜索已归档的聊天" })).toHaveValue("matching");
    expect(screen.queryByRole("button", { name: "加载更早归档" })).not.toBeInTheDocument();
  });
  it("loads only archived chats and filters locally without mutating lifecycle state", async () => {
    const controls = renderArchived([
      archivedThread("alpha", "Alpha investigation", 7),
      archivedThread("beta", "Beta delivery", 11),
    ]);
    expect(await screen.findByText("Alpha investigation")).toBeInTheDocument();
    expect(screen.getByText("Beta delivery")).toBeInTheDocument();
    expect(controls.getPage).toHaveBeenCalledWith("/threads",
      { limit: 100, status: "archived" }, "", expect.any(AbortSignal));

    fireEvent.change(screen.getByRole("searchbox", { name: "搜索已归档的聊天" }),
      { target: { value: "beta" } });
    expect(screen.queryByText("Alpha investigation")).not.toBeInTheDocument();
    expect(screen.getByText("Beta delivery")).toBeInTheDocument();
    expect(controls.transitionThread).not.toHaveBeenCalled();
  });

  it("restores with the exact Thread version", async () => {
    const user = userEvent.setup();
    const thread = archivedThread("restore", "Restore me", 7);
    const controls = renderArchived([thread]);
    const article = (await screen.findByText(thread.title)).closest("article");
    expect(article).not.toBeNull();
    await user.click(within(article!).getByRole("button", { name: "取消归档" }));

    await waitFor(() => expect(controls.transitionThread).toHaveBeenCalledWith(
      "restore", "restore", { version: "thread_lifecycle.v1", expected_version: 7 },
      expect.stringMatching(/^v2-archived-restore-/u),
    ));
  });

  it("does not delete until the destructive confirmation is accepted", async () => {
    const user = userEvent.setup();
    const thread = archivedThread("delete", "Delete me", 11);
    const controls = renderArchived([thread]);
    await user.click(await screen.findByRole("button", { name: "删除 Delete me" }));

    expect(controls.transitionThread).not.toHaveBeenCalled();
    const dialog = screen.getByRole("dialog", { name: "删除已归档的聊天" });
    expect(within(dialog).getByText(/底层审计记录仍按项目保留策略保存/u)).toBeInTheDocument();
    await user.click(within(dialog).getByRole("button", { name: "删除" }));

    await waitFor(() => expect(controls.transitionThread).toHaveBeenCalledWith(
      "delete", "delete", { version: "thread_lifecycle.v1", expected_version: 11 },
      expect.stringMatching(/^v2-archived-delete-/u),
    ));
  });
});

describe("V2 general permission summary", () => {
  it("switches the settings language immediately, across sections, and after remounting", async () => {
    const user = userEvent.setup();
    const draw = (section: "general" | "appearance") => <V2Settings client={{} as APIClient}
      onOpenInspector={vi.fn()} onSelectSection={vi.fn()} section={section} threadID=""
      workspaces={[]} />;
    const view = render(draw("general"));
    expect(screen.getByRole("heading", { name: "常规", level: 1 })).toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: /^English/ }));
    expect(screen.getByRole("heading", { name: "General", level: 1 })).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "English" })).toHaveAttribute("aria-pressed", "true");
    expect(screen.getByRole("button", { name: "Manage current task permissions" })).toBeDisabled();
    expect(screen.getByRole("button", { name: "View license" })).toBeInTheDocument();
    expect(document.documentElement.lang).toBe("en-US");
    expect(window.localStorage.getItem("prayu.locale.v1")).toBe("en-US");

    view.rerender(draw("appearance"));
    expect(screen.getByRole("heading", { name: "Appearance", level: 1 })).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Dark" })).toBeInTheDocument();
    view.unmount();

    render(draw("general"));
    expect(screen.getByRole("heading", { name: "General", level: 1 })).toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: "中文" }));
    expect(screen.getByRole("heading", { name: "常规", level: 1 })).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "查看许可" })).toBeInTheDocument();
    expect(document.documentElement.lang).toBe("zh-CN");
    expect(window.localStorage.getItem("prayu.locale.v1")).toBe("zh-CN");
  });

  it("uses neutral management actions instead of hard-coded enabled switches", async () => {
    const user = userEvent.setup();
    const fetchMock = vi.fn().mockResolvedValue(new Response(
      "HarmonyOS Sans Fonts License Agreement\nTHIS HARMONYOS SANS FONTS LICENSE AGREEMENT",
      { headers: { "Content-Type": "text/plain; charset=utf-8" } },
    ));
    vi.stubGlobal("fetch", fetchMock);
    const onSelectSection = vi.fn();
    const queryClient = new QueryClient();
    render(<QueryClientProvider client={queryClient}>
      <V2Settings client={{} as APIClient} onOpenInspector={vi.fn()}
        onSelectSection={onSelectSection} section="general" threadID="thread-current"
        workspaces={[]} />
    </QueryClientProvider>);

    const defaultPermissions = screen.getByRole("button", { name: "管理当前任务权限" });
    expect(defaultPermissions).not.toHaveAttribute("aria-pressed");
    expect(screen.queryByRole("button", { name: "管理完整访问权限" })).not.toBeInTheDocument();
    expect(screen.getByText("选择界面语言，立即生效并保存在本机。")).toBeInTheDocument();
    const licenseButton = screen.getByRole("button", { name: "查看许可" });
    await user.click(licenseButton);
    const dialog = screen.getByRole("dialog", { name: "HarmonyOS Sans Fonts 许可" });
    expect(await within(dialog).findByText(/THIS HARMONYOS SANS FONTS LICENSE AGREEMENT/u))
      .toBeInTheDocument();
    expect(fetchMock).toHaveBeenCalledWith("/licenses/HarmonyOS-Sans.txt", {
      cache: "no-store", signal: expect.any(AbortSignal),
    });
    await user.click(within(dialog).getByRole("button", { name: "关闭" }));
    expect(screen.queryByRole("dialog", { name: "HarmonyOS Sans Fonts 许可" })).not.toBeInTheDocument();
    expect(licenseButton).toHaveFocus();
    await user.click(defaultPermissions);
    expect(onSelectSection).toHaveBeenCalledWith("permissions");
  });
});

describe("V2 model provider catalog", () => {
  it("opens an official preset as an editable provider draft and restores catalog focus", async () => {
    const user = userEvent.setup();
    const controls = renderModels(["official-openai"]);

    const openAI = await screen.findByRole("button", {
      name: /OpenAI，gpt-6\.1-sol，已保存 API Key/u,
    });
    await user.click(openAI);

    expect(await screen.findByRole("heading", { name: "添加供应商" })).toBeInTheDocument();
    expect(screen.getByLabelText("API Key")).toBeEnabled();
    expect(screen.queryByLabelText("请求地址")).not.toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: "高级设置：自定义连接、模型与搜索" }));
    expect(screen.getByLabelText("供应商 ID")).toHaveValue("official-openai");
    expect(screen.getByLabelText("显示名称")).toHaveValue("OpenAI 官方 API");
    expect(screen.getByLabelText("请求地址")).toHaveValue("https://api.openai.com/v1/responses");
    expect(screen.getByLabelText("协议")).toHaveValue("openai_responses");
    expect(screen.getByLabelText("默认模型")).toHaveValue("gpt-6.1-sol");
    expect(screen.getByRole("textbox", { name: "高级 JSON" })).toBeEnabled();
    expect(controls.providerDefinitions).toHaveBeenCalledOnce();
    expect(controls.providerCredentialStatuses).toHaveBeenCalled();

    await user.click(screen.getByRole("button", { name: "返回模型目录" }));
    expect(await screen.findByRole("heading", { name: "模型" })).toBeInTheDocument();
    await waitFor(() => expect(screen.getByRole("button", {
      name: /OpenAI，gpt-6\.1-sol，已保存 API Key/u,
    })).toHaveFocus());
  });

});

describe("V2 permission settings hierarchy", () => {
  it("keeps Debug visible but leaves task permissions unavailable without a selected Thread", () => {
    window.localStorage.setItem("prayu.locale.v1", "zh-CN");
    const getThreadExecutionPermission = vi.fn();
    const get = vi.fn();
    const changeThreadExecutionPermission = vi.fn();
    const postControl = vi.fn();
    const client = { hasExecutionPermissionControl: true, get,
      getThreadExecutionPermission, changeThreadExecutionPermission, postControl } as unknown as APIClient;
    const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    render(<LocaleProvider><QueryClientProvider client={queryClient}>
      <V2Settings client={client} onOpenInspector={vi.fn()} onSelectSection={vi.fn()}
        section="permissions" threadID="" workspaces={[]} />
    </QueryClientProvider></LocaleProvider>);

    expect(screen.getByRole("heading", { name: "调试运行时" })).toBeInTheDocument();
    expect(screen.getByRole("heading", { name: "任务权限" })).toBeInTheDocument();
    const headings = screen.getAllByRole("heading", { level: 2 }).map((heading) => heading.textContent);
    expect(headings.indexOf("开发服务")).toBe(headings.indexOf("任务权限") + 1);
    expect(headings.indexOf("当前任务执行环境")).toBe(headings.indexOf("开发服务") + 1);
    expect(screen.getByText("先从侧栏打开一个对话。")).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: /启用完全访问/u })).not.toBeInTheDocument();
    const debugGroup = screen.getByRole("group", { name: "调试运行时能力" });
    expect(within(debugGroup).getByText("调试模式")).toBeInTheDocument();
    expect(within(debugGroup).getByRole("button")).toBeVisible();
    // CDP belongs to the selected task. No Thread means no task permission host.
    // The CDP component's no-current-Run disabled state is covered separately.
    expect(screen.queryByRole("switch", { name: "完整 CDP 控制" })).not.toBeInTheDocument();
    expect(screen.queryByRole("group", { name: "执行权限档位" })).not.toBeInTheDocument();
    expect(within(debugGroup).getByRole("button")).toBeDisabled();
    expect(screen.getByText("当前页面没有可验证的桌面运行时能力信息。")).toBeVisible();
    expect(screen.getByText(/在当前任务开启完全访问时，先暂停执行并等待资源释放完成即可/u))
      .toBeInTheDocument();
    expect(getThreadExecutionPermission).not.toHaveBeenCalled();
    expect(get).not.toHaveBeenCalled();
    expect(changeThreadExecutionPermission).not.toHaveBeenCalled();
    expect(postControl).not.toHaveBeenCalled();
  });
});

describe("V2 appearance and Catppuccin Latte glass controls", () => {
  afterEach(() => {
    window.localStorage.removeItem("prayu.theme");
    window.localStorage.removeItem("prayu.glass");
  });

  it("reveals Catppuccin Latte glass tuning controls when glass theme is selected and syncs CSS variables", async () => {
    window.localStorage.setItem("prayu.theme", "dark");
    const user = userEvent.setup();
    render(<QueryClientProvider client={new QueryClient()}>
      <V2Settings client={{} as APIClient} onOpenInspector={vi.fn()} onSelectSection={vi.fn()}
        section="appearance" threadID="" workspaces={[]} />
    </QueryClientProvider>);

    expect(screen.queryByRole("region", { name: "雾透玻璃参数" })).not.toBeInTheDocument();

    const themeGroup = screen.getByRole("group", { name: "应用主题" });
    await user.click(within(themeGroup).getByRole("button", { name: "雾透" }));

    const glassRegion = screen.getByRole("region", { name: "雾透玻璃参数" });
    expect(glassRegion).toBeVisible();
    expect(document.documentElement.dataset.prayuTheme).toBe("glass");
    expect(document.documentElement.dataset.prayuGlassMode).toBe("mica");

    const resetButton = within(glassRegion).getByRole("button", { name: "还原默认值" });
    expect(resetButton).toBeDisabled();

    await user.click(within(glassRegion).getByRole("button", { name: "兼容模式" }));
    expect(document.documentElement.dataset.prayuGlassMode).toBe("compat");
    expect(resetButton).toBeEnabled();

    await user.click(within(glassRegion).getByRole("button", { name: "磨砂" }));
    expect(document.documentElement.style.getPropertyValue("--prayu-glass-blur")).toBe("12px");
    expect(document.documentElement.style.getPropertyValue("--prayu-glass-frost")).toBe("0.9");

    fireEvent.change(within(glassRegion).getByRole("slider", { name: "背景亮度" }), {
      target: { value: "80" },
    });
    expect(document.documentElement.style.getPropertyValue("--prayu-glass-brightness-white")).toBe("0.6");

    const glowSwitch = within(glassRegion).getByRole("switch", { name: "氛围光斑" });
    await user.click(glowSwitch);
    expect(glowSwitch).toHaveAttribute("aria-checked", "true");
    expect(document.documentElement.style.getPropertyValue("--prayu-glass-glow")).toBe("1");

    await user.click(resetButton);
    expect(document.documentElement.dataset.prayuGlassMode).toBe("mica");
    expect(document.documentElement.style.getPropertyValue("--prayu-glass-blur")).toBe("5px");
    expect(document.documentElement.style.getPropertyValue("--prayu-glass-frost")).toBe("0.4");
    expect(document.documentElement.style.getPropertyValue("--prayu-glass-brightness-white")).toBe("0");
    expect(document.documentElement.style.getPropertyValue("--prayu-glass-glow")).toBe("0");
    expect(resetButton).toBeDisabled();
  });
});

