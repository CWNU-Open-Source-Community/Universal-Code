import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import type { ReactElement } from "react";
import { act, cleanup, fireEvent, render as renderComponent, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, describe, expect, it, vi } from "vitest";

import { APIRequestError, type APIClient } from "../api/client";
import type { ThreadView, WorkspaceView } from "../api/types";
import { useConnectionStore } from "../state/connection";
import { mcpServer } from "../test/extension-onboarding-fixtures";
import { LocaleProvider, useLocale } from "../lib/locale";
import { V2Workbench } from "./app";
import * as desktopBridge from "../lib/desktop-bridge";
import { v2FileReferenceKey } from "./components/file-context";

function render(ui: ReactElement) {
  return renderComponent(<LocaleProvider>{ui}</LocaleProvider>);
}

function LocaleSwitch() {
  const { locale, setLocale } = useLocale();
  return <button onClick={() => setLocale(locale === "zh-CN" ? "en-US" : "zh-CN")} type="button">Switch test language</button>;
}

const branchNavigation = vi.hoisted(() => ({ complete: undefined as ((thread: ThreadView) => void) | undefined }));

vi.mock("./components/conversation", () => ({
  V2Conversation: ({ threadID, view = "conversation", draft, onDraftChange, reviewEntry, onBranchCreated }: {
    threadID: string;
    view?: "conversation" | "inspector";
    draft: string;
    onDraftChange: (value: string) => void;
    reviewEntry?: { threadID: string; runID: string };
    onBranchCreated?: (thread: ThreadView) => void;
  }) => {
    branchNavigation.complete = onBranchCreated;
    return (
    <div data-testid="v2-conversation" data-view={view}>{threadID}
      <textarea aria-label="任务草稿 fixture" value={draft} onChange={(event) => onDraftChange(event.target.value)} />
      {reviewEntry && <output aria-label="Requested GitHub review">{`${reviewEntry.threadID}:${reviewEntry.runID}`}</output>}
    </div>
    );
  },
}));

const workspace: WorkspaceView = {
  id: "workspace-first-turn",
  name: "First turn workspace",
  created_at: "2026-08-29T00:00:00Z",
};

const createdThread: ThreadView = {
  id: "thread-created-from-first-turn",
  title: "First turn",
  workspace_id: workspace.id,
  mission_id: "mission-created-from-first-turn",
  active_run_id: "run-created-from-first-turn",
  last_run_id: "run-created-from-first-turn",
  protocol_version: "thread.v1",
  composer_state: "ready",
  status: "active",
  version: 1,
  created_at: "2026-08-29T00:00:00Z",
  updated_at: "2026-08-29T00:00:00Z",
};

function deferred<T>() {
  let resolve!: (value: T) => void;
  let reject!: (reason?: unknown) => void;
  const promise = new Promise<T>((resolvePromise, rejectPromise) => {
    resolve = resolvePromise;
    reject = rejectPromise;
  });
  return { promise, reject, resolve };
}

const selectableModelCatalog = (provider = "fixture-provider", model = "fixture-model") => ({
  protocol_version: "model_route_catalog.v1",
  generation: 1,
  routes: [{ provider_id: provider, provider_name: provider, model,
	definition_revision: 1, enabled: true, credential_status: "configured",
	qualification_status: "available", harness_ready: true, selectable: true,
	unavailable_reason: "", default_for_routes: ["code"],
	vision_capability: { state: "unknown", source: "unknown" } }],
});

const readyModelCatalog = () => vi.fn().mockResolvedValue(selectableModelCatalog());

afterEach(() => {
  branchNavigation.complete = undefined;
  cleanup();
  useConnectionStore.getState().disconnect();
  vi.restoreAllMocks();
  vi.unstubAllGlobals();
  window.history.replaceState({}, "", "/");
  window.localStorage.clear();
});

function navigationClient() {
  return { hasThreadControl: true,
    getPage: vi.fn(async (path: string) => ({ items: path === "/workspaces" ? [workspace] : [createdThread],
      page: { limit: 100 }, requestID: path })),
    get: vi.fn(() => new Promise(() => undefined)),
    createThread: vi.fn(), submitThreadTurn: vi.fn(), executeRun: vi.fn(), transitionThread: vi.fn(),
  } as unknown as APIClient;
}

function expectNoNavigationWrites(client: APIClient) {
  for (const call of [client.createThread, client.submitThreadTurn, client.executeRun, client.transitionThread]) {
    expect(call).not.toHaveBeenCalled();
  }
}

describe("V2Workbench inspector navigation", () => {
  it("offers the new branch without replacing a chat selected while branching", async () => {
    const otherThread = { ...createdThread, id: "thread-other", title: "Other conversation" };
    const branched = { ...createdThread, id: "thread-branch", title: "Branched conversation" };
    window.history.replaceState({}, "", `#/threads/${createdThread.id}`);
    const client = navigationClient();
    vi.mocked(client.getPage).mockImplementation(async (path: string) => ({
      items: path === "/workspaces" ? [workspace] : [createdThread, otherThread],
      page: { limit: 100 }, requestID: path,
    }) as never);
    render(<QueryClientProvider client={new QueryClient({ defaultOptions: { queries: { retry: false } } })}>
      <V2Workbench client={client} /></QueryClientProvider>);
    const user = userEvent.setup();
    await screen.findByTestId("v2-conversation");
    const completeSourceBranch = branchNavigation.complete!;
    await user.click(await screen.findByRole("button", { name: "Other conversation" }));
    await user.type(screen.getByRole("textbox", { name: "任务草稿 fixture" }), "Keep this draft");
    act(() => completeSourceBranch(branched));
    expect(window.location.hash).toBe(`#/threads/${otherThread.id}`);
    expect(screen.getByRole("textbox", { name: "任务草稿 fixture" })).toHaveValue("Keep this draft");
    await user.click(screen.getByRole("button", { name: "打开已创建的对话" }));
    expect(window.location.hash).toBe(`#/threads/${branched.id}`);
    expect(screen.getByRole("textbox", { name: "任务草稿 fixture" })).toHaveValue("");
    expectNoNavigationWrites(client);
  });

  it("opens the source task's GitHub delivery tool from connections while retaining the task draft", async () => {
    // Exercise navigation after the settings chunk is ready, independent of CI module-load time.
    await import("./components/settings");
    window.history.replaceState({}, "", `#/threads/${createdThread.id}`);
    const client = navigationClient();
    vi.mocked(client.get).mockResolvedValue({ thread: createdThread,
      active_run: { id: "run-current" }, last_run: { id: "run-current" },
      runs: [{ ordinal: 1, run: { id: "run-current" } }] });
    render(<QueryClientProvider client={new QueryClient({ defaultOptions: { queries: { retry: false } } })}>
      <V2Workbench client={client} /></QueryClientProvider>);
    const user = userEvent.setup();
    await user.type(await screen.findByRole("textbox", { name: "任务草稿 fixture" }), "等待连接设置后继续的草稿");
    await user.click(screen.getByRole("button", { name: "连接与环境" }));
    const githubEntry = await screen.findByRole("button", { name: /GitHub 连接与审阅/u });
    await waitFor(() => expect(githubEntry).toBeEnabled());
    await user.click(githubEntry);
    expect(await screen.findByLabelText("Requested GitHub review")).toHaveTextContent(`${createdThread.id}:run-current`);
    expect(window.location.hash).toBe(`#/threads/${createdThread.id}`);
    expect(screen.getByRole("textbox", { name: "任务草稿 fixture" })).toHaveValue("等待连接设置后继续的草稿");
    expectNoNavigationWrites(client);
  });
  it("closes the narrow navigation drawer after new-task and settings navigation while retaining the draft", async () => {
    vi.stubGlobal("matchMedia", vi.fn((query: string) => ({
      matches: query === "(max-width: 760px)", media: query,
      addEventListener: vi.fn(), removeEventListener: vi.fn(),
      addListener: vi.fn(), removeListener: vi.fn(), dispatchEvent: vi.fn(), onchange: null,
    })));
    window.history.replaceState({}, "", `#/threads/${createdThread.id}`);
    const client = { hasThreadControl: true, getPage: vi.fn(async (path: string) => ({
      items: path === "/workspaces" ? [workspace] : [createdThread],
      page: { limit: 100 }, requestID: path,
    })) } as unknown as APIClient;
    const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    render(<QueryClientProvider client={queryClient}><V2Workbench client={client} /></QueryClientProvider>);
    const user = userEvent.setup();
    await screen.findByTestId("v2-conversation");
    const expectDrawerClosed = () => {
      expect(screen.queryByRole("button", { name: "关闭侧栏" })).not.toBeInTheDocument();
      expect(screen.getByRole("button", { name: "显示侧栏" })).toHaveAttribute("aria-pressed", "false");
    };
    await user.click(screen.getByRole("button", { name: "显示侧栏" }));
    await user.click(within(screen.getByRole("navigation", { name: "对话导航" })).getByRole("button", { name: "新对话" }));
    expectDrawerClosed();
    await user.click(screen.getByRole("button", { name: "接入项目" }));
    expect(screen.getByRole("dialog", { name: "接入已有项目" })).toBeInTheDocument();
    await user.keyboard("{Escape}");
    await user.type(screen.getByRole("textbox", { name: "开始新对话" }), "窄窗口中的项目需求");
    await user.click(screen.getByRole("button", { name: "显示侧栏" }));
    await user.click(screen.getByRole("button", { name: "接入模型" }));
    expect(await screen.findByRole("heading", { name: "模型" })).toBeInTheDocument();
    expectDrawerClosed();
    await user.click(screen.getByRole("button", { name: "显示侧栏" }));
    await user.click(screen.getByRole("button", { name: "常规" }));
    expect(screen.getByRole("heading", { name: "常规", level: 1 })).toBeInTheDocument();
    expectDrawerClosed();
    await user.click(screen.getByRole("button", { name: "显示侧栏" }));
    await user.click(screen.getByRole("button", { name: "返回应用" }));
    expect(await screen.findByRole("textbox", { name: "开始新对话" })).toHaveValue("窄窗口中的项目需求");
    expectDrawerClosed();
    await user.click(screen.getByRole("button", { name: "显示侧栏" }));
    await user.click(screen.getByRole("button", { name: "设置" }));
    expect(screen.getByRole("heading", { name: "常规", level: 1 })).toBeInTheDocument();
    expectDrawerClosed();
    await user.click(screen.getByRole("button", { name: "显示侧栏" }));
    await user.click(screen.getByRole("button", { name: "返回" }));
    expect(await screen.findByRole("textbox", { name: "开始新对话" })).toHaveValue("窄窗口中的项目需求");
    expectDrawerClosed();
  });

  it("loads past 100 Threads and preserves an older URL selection through failed paging and refresh", async () => {
    const user = userEvent.setup();
    const first = Array.from({ length: 100 }, (_, index) => ({ ...createdThread,
      id: `history-${index}`, title: `History ${index}` }));
    const oldest = { ...createdThread, id: "history-oldest", title: "Oldest task" };
    let failNext = true;
    const getPage = vi.fn(async (path: string, _query: unknown, cursor: string) => {
      if (path === "/workspaces") return { items: [workspace], page: { limit: 100 }, requestID: path };
      if (cursor && failNext) { failNext = false; throw new Error("historical page unavailable"); }
      return { items: cursor ? [oldest] : first, page: { limit: 100, next_cursor: cursor ? "" : "older-cursor" }, requestID: path };
    });
    window.history.replaceState({}, "", "#/threads/history-oldest");
    const client = { getPage, get: vi.fn() } as unknown as APIClient;
    const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    render(<QueryClientProvider client={queryClient}><V2Workbench client={client} /></QueryClientProvider>);
    expect(await screen.findByTestId("v2-conversation")).toHaveTextContent(oldest.id);
    await user.click(await screen.findByRole("button", { name: "加载更早对话" }));
    await screen.findByRole("button", { name: "重试加载对话" });
    expect(screen.getByTestId("v2-conversation")).toHaveTextContent(oldest.id);
    expect(screen.getByRole("button", { name: "History 0" })).toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: "重试加载对话" }));
    await user.click(await screen.findByRole("button", { name: "Oldest task" }));
    expect(getPage).toHaveBeenCalledWith("/threads", { limit: 100, status: "active" }, "older-cursor", expect.any(AbortSignal));
    await user.click(screen.getByRole("button", { name: "刷新对话列表" }));
    await waitFor(() => expect(queryClient.isFetching()).toBe(0));
    expect(screen.getByTestId("v2-conversation")).toHaveTextContent(oldest.id);
    expect(window.location.hash).toBe("#/threads/history-oldest");
    await user.click(screen.getByRole("button", { name: "History 0" }));
    expect(window.location.hash).toBe("#/threads/history-0");
    await act(async () => { window.history.back(); });
    await waitFor(() => expect(screen.getByTestId("v2-conversation")).toHaveTextContent(oldest.id));
  });
  it("retains the same Thread draft and references across both views and shared settings without writes", async () => {
    window.history.replaceState({}, "", `#/threads/${createdThread.id}`);
    const client = navigationClient();
    const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false }, mutations: { retry: false } } });
    const files = [{ id: "view-file", path: "README.md", digest: "a".repeat(64), partial: false, redacted: false }];
    queryClient.setQueryData(v2FileReferenceKey(workspace.id, createdThread.id), files);
    render(<QueryClientProvider client={queryClient}><V2Workbench client={client} /></QueryClientProvider>);
    const user = userEvent.setup();
    await user.type(await screen.findByRole("textbox", { name: "任务草稿 fixture" }), "尚未发送的任务要求");
    await user.click(screen.getByRole("button", { name: "观察与记录" }));
    expect(screen.getByTestId("v2-conversation")).toHaveAttribute("data-view", "inspector");
    expect(window.location.hash).toBe(`#/threads/${createdThread.id}/inspector`);
    expect(screen.getByRole("textbox", { name: "任务草稿 fixture" })).toHaveValue("尚未发送的任务要求");
    expect(screen.queryByRole("dialog", { name: "观察与记录" })).not.toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: "设置" }));
    expect(screen.getByRole("heading", { name: "常规", level: 1 })).toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: "模型" }));
    expect(window.location.hash).toBe(`#/threads/${createdThread.id}/inspector/settings/models`);
    await user.click(screen.getByRole("button", { name: "返回应用" }));
    expect(await screen.findByTestId("v2-conversation")).toHaveAttribute("data-view", "inspector");
    await user.click(screen.getByRole("button", { name: "观察与记录" }));
    expect(screen.getByTestId("v2-conversation")).toHaveAttribute("data-view", "conversation");
    expect(screen.getByRole("textbox", { name: "任务草稿 fixture" })).toHaveValue("尚未发送的任务要求");
    expect(queryClient.getQueryData(v2FileReferenceKey(workspace.id, createdThread.id))).toEqual(files);
    expect(window.location.hash).toBe(`#/threads/${createdThread.id}`);
    expectNoNavigationWrites(client);
  });

  it.each([`#/threads/${createdThread.id}/inspector`, "#/new/inspector"])(
    "returns to its explicit Inspector source after direct loading settings at %s", async (hash) => {
      window.history.replaceState({}, "", `${hash}/settings/appearance`);
      const historyBack = vi.spyOn(window.history, "back");
      const client = navigationClient();
      const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } });
      render(<QueryClientProvider client={queryClient}><V2Workbench client={client} /></QueryClientProvider>);
      const user = userEvent.setup();
      expect(await screen.findByRole("heading", { name: "外观", level: 1 })).toBeInTheDocument();
      await user.click(screen.getByRole("button", { name: "返回应用" }));
      expect(historyBack).not.toHaveBeenCalled();
      expect(window.location.hash).toBe(hash);
      expect(screen.getByRole("button", { name: "观察与记录" })).toHaveAttribute("aria-pressed", "true");
      await user.click(screen.getByRole("button", { name: "观察与记录" }));
      expect(window.location.hash).toBe(hash.replace("/inspector", ""));
      expect(screen.getByRole("button", { name: "观察与记录" })).toHaveAttribute("aria-pressed", "false");
      expectNoNavigationWrites(client);
    },
  );

  it("retains project selection, new-task draft and references before a Thread exists", async () => {
    window.history.replaceState({}, "", "#/new");
    const client = navigationClient();
    const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    const files = [{ id: "new-view-file", path: "README.md", digest: "b".repeat(64), partial: false, redacted: false }];
    queryClient.setQueryData(v2FileReferenceKey(workspace.id, ""), files);
    render(<QueryClientProvider client={queryClient}><V2Workbench client={client} /></QueryClientProvider>);
    const user = userEvent.setup();
    await user.type(await screen.findByRole("textbox", { name: "开始新对话" }), "没有创建任务前的需求");
    await waitFor(() => expect(screen.getByRole("combobox", { name: "选择工作区" })).toHaveValue(workspace.id));
    await user.click(screen.getByRole("button", { name: "观察与记录" }));
    expect(window.location.hash).toBe("#/new/inspector");
    await user.click(screen.getByRole("button", { name: "设置" }));
    await user.click(screen.getByRole("button", { name: "返回应用" }));
    expect(window.location.hash).toBe("#/new/inspector");
    await user.click(screen.getByRole("button", { name: "观察与记录" }));
    expect(await screen.findByRole("textbox", { name: "开始新对话" })).toHaveValue("没有创建任务前的需求");
    expect(screen.getByRole("combobox", { name: "选择工作区" })).toHaveValue(workspace.id);
    expect(queryClient.getQueryData(v2FileReferenceKey(workspace.id, ""))).toEqual(files);
    expectNoNavigationWrites(client);
  });

  it.each([
    { threadID: "", source: "inspector", button: "打开新任务输入区" },
    { threadID: createdThread.id, source: "inspector", button: "打开任务输入区" },
    { threadID: createdThread.id, source: "inspector/runs/historical-run", button: "打开任务输入区" },
    { threadID: createdThread.id, source: "inspector/sessions/historical-session", button: "打开任务输入区" },
  ])("opens the composer from $source extension settings while retaining draft and identity ($threadID)",
    async ({ threadID, source, button }) => {
      const base = threadID ? `#/threads/${threadID}` : "#/new";
      window.history.replaceState({}, "", base);
      const client = Object.assign(navigationClient(), {
        get: vi.fn().mockResolvedValue({ thread: createdThread, active_run: { id: createdThread.active_run_id } }),
        extensionInventory: vi.fn().mockResolvedValue({ protocol_version: "extension-inventory.v1",
          workspace_id: workspace.id, run_id: threadID ? createdThread.active_run_id : "",
          mcp_servers: [{ ...mcpServer("enabled"), workspace_id: workspace.id }], mcp_calls: [], plugins: [] }),
        codeIntelInventory: vi.fn().mockResolvedValue({ protocol_version: "code-intel-lsp.v1",
          enabled: true, qualifications: [], servers: [], configurations: [] }),
      });
      const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } });
      const files = [{ id: "extension-return-file", path: "README.md", digest: "c".repeat(64), partial: false, redacted: false }];
      queryClient.setQueryData(v2FileReferenceKey(workspace.id, threadID), files);
      render(<QueryClientProvider client={queryClient}><V2Workbench client={client} /></QueryClientProvider>);
      const user = userEvent.setup();
      const inputName = threadID ? "任务草稿 fixture" : "开始新对话";
      await user.type(await screen.findByRole("textbox", { name: inputName }), "接入扩展前未发送的需求");
      await act(async () => {
        window.history.replaceState({}, "", `${base}/${source}/settings/extensions`);
        window.dispatchEvent(new PopStateEvent("popstate"));
      });
      if (!threadID) await user.selectOptions(await screen.findByRole("combobox", { name: "接入工作区" }), workspace.id);
      await user.click(await screen.findByRole("button", { name: button }));
      expect(window.location.hash).toBe(base);
      expect(await screen.findByRole("textbox", { name: inputName })).toHaveValue("接入扩展前未发送的需求");
      if (threadID) expect(screen.getByTestId("v2-conversation")).toHaveAttribute("data-view", "conversation");
      else expect(screen.getByRole("combobox", { name: "选择工作区" })).toHaveValue(workspace.id);
      expect(queryClient.getQueryData(v2FileReferenceKey(workspace.id, threadID))).toEqual(files);
      expectNoNavigationWrites(client);
    });
});

describe("V2Workbench model navigation", () => {
  it("opens the dedicated Models settings section from the conversation sidebar", async () => {
    const getPage = vi.fn(async (path: string) => ({
      items: path === "/workspaces" ? [workspace] : [createdThread],
      page: { limit: 100 },
      requestID: `${path}-request`,
    }));
    const client = {
      hasThreadControl: true,
      getPage,
      providerDefinitions: vi.fn().mockResolvedValue({ providers: [] }),
      providerCredentialStatuses: vi.fn().mockResolvedValue({ items: [] }),
    } as unknown as APIClient;
    const queryClient = new QueryClient({
      defaultOptions: { queries: { retry: false }, mutations: { retry: false } },
    });
    render(<QueryClientProvider client={queryClient}>
      <V2Workbench client={client} />
    </QueryClientProvider>);

    const user = userEvent.setup();
    await screen.findByRole("button", { name: "接入模型" });
    await user.click(screen.getByRole("button", { name: "接入模型" }));

    const models = screen.getByRole("button", { name: "模型" });
    expect(models).toHaveAttribute("aria-current", "page");
    expect(screen.getByRole("navigation", { name: "设置分类" })).toContainElement(models);
  });

  it("returns a qualified first model to the original draft and uses it for the first real turn", async () => {
    let savedDefinition: any;
    let qualified = false;
    const providerDefinitions = vi.fn(async () => ({
      version: "provider_definition_collection.v1", revision: savedDefinition ? 1 : 0,
      providers: savedDefinition ? [savedDefinition] : [],
    }));
    const upsertProviderDefinition = vi.fn(async (_id, body) => {
      savedDefinition = { ...body.definition, revision: 1 };
      return { protocol_version: "provider_definition_control.v1", registry_reloaded: true,
        registry_generation: 2, definition: savedDefinition,
        collection: { version: "provider_definition_collection.v1", revision: 1,
          providers: [savedDefinition] } };
    });
    const availableModelRoutes = vi.fn(async () => ({
      protocol_version: "model_route_catalog.v1", generation: 2,
      routes: qualified && savedDefinition ? [{ provider_id: savedDefinition.id,
        provider_name: savedDefinition.display_name, model: savedDefinition.default_model,
		definition_revision: savedDefinition.revision,
        enabled: true, credential_status: "configured", qualification_status: "available",
        harness_ready: true, selectable: true, unavailable_reason: "", default_for_routes: [],
		vision_capability: { state: "unknown", source: "unknown" } }] : [{
		provider_id: "official-openai", provider_name: "OpenAI", model: "gpt-5",
		definition_revision: 0, enabled: true, credential_status: "not_configured",
		qualification_status: "not_configured", harness_ready: false, selectable: false,
		unavailable_reason: "credential_not_configured", default_for_routes: [],
		vision_capability: { state: "unknown", source: "unknown" },
	  }],
    }));
    const qualifyModelHarness = vi.fn(async (body) => {
      qualified = true;
      return { protocol_version: "model_harness_qualification.v1", provider: body.provider,
        model: body.model, status: "qualified", outcome: "success", failure_reason: "none",
        retryable: false, network_request_attempted: true, model_calls: 2,
        synthetic_tool_calls: 1, tool_executed: false, response_content_returned: false,
        duration_ms: 20, qualification_status: "available",
        harness: { protocol_version: "model_harness.v1", model: body.model,
          transport_protocol: "openai_responses", tool_strategy: "native", json_strategy: "native",
          qualification_status: "verified", latest_qualification_status: "available",
          qualification_checked_at: "2026-09-20T00:00:00Z", qualification_source: "harness_qualification",
          tool_calls_qualified: true, tool_results_qualified: true, strict_json_qualified: true,
          streaming_qualified: true, root_eligible: true, structured_json_eligible: true,
          qualified_at: "2026-09-20T00:00:00Z", expires_at: "2026-09-27T00:00:00Z" } };
    });
    const createThread = vi.fn().mockResolvedValue({ thread: createdThread });
    const submitThreadTurn = vi.fn().mockResolvedValue({ accepted: true });
    const client = { hasThreadControl: true, hasModelControl: true,
      hasProviderDefinitions: true, hasProviderCredentials: true,
      getPage: vi.fn(async (path: string) => ({ items: path === "/workspaces" ? [workspace] : [],
        page: { limit: 100 }, requestID: path })), availableModelRoutes,
      providerDefinitions, providerCredentialStatuses: vi.fn().mockResolvedValue({
        protocol_version: "provider_credential.v1", items: [] }),
      upsertProviderDefinition, changeProviderCredential: vi.fn().mockResolvedValue({
        protocol_version: "provider_credential.v1", provider: "official-openai", configured: true,
        store_available: true, store_kind: "windows_credential_manager", plaintext_returned: false,
        restart_required: false, registry_reloaded: true, registry_generation: 2 }),
      qualifyModelHarness, createThread, submitThreadTurn,
    } as unknown as APIClient;
    render(<QueryClientProvider client={new QueryClient({ defaultOptions: {
      queries: { retry: false }, mutations: { retry: false },
    } })}><V2Workbench client={client} /></QueryClientProvider>);
    const user = userEvent.setup();
    const composer = await screen.findByRole("textbox", { name: "开始新对话" });
    await user.type(composer, "使用刚配置的真实模型处理这份草稿");
	await user.click(screen.getByRole("button", { name: "接入模型" }));
    await user.click(await screen.findByRole("button", { name: /OpenAI/u }));
    await user.type(screen.getByLabelText("API Key"), "first-model-key-123456");
    await user.click(screen.getByRole("button", { name: "保存并检查" }));

    const restored = await screen.findByRole("textbox", { name: "开始新对话" });
    expect(restored).toHaveValue("使用刚配置的真实模型处理这份草稿");
    await waitFor(() => expect(restored).toHaveFocus());
    expect(screen.getByRole("button", { name: /模型路由，当前/u })).toHaveAccessibleName(
      expect.stringContaining(savedDefinition.default_model),
    );
    expect(createThread).not.toHaveBeenCalled();
    await user.click(screen.getByRole("button", { name: "发送消息" }));
    await waitFor(() => expect(createThread).toHaveBeenCalledTimes(1));
    expect(createThread.mock.calls[0][0]).toEqual(expect.objectContaining({
      provider: savedDefinition.id, model: savedDefinition.default_model,
    }));
    expect(qualifyModelHarness).toHaveBeenCalledTimes(1);
  });

  it("does not let a finished first-model check reclaim navigation after the user leaves", async () => {
    let savedDefinition: any;
    let qualified = false;
    const qualification = deferred<any>();
    const providerDefinitions = vi.fn(async () => ({
      version: "provider_definition_collection.v1", revision: savedDefinition ? 1 : 0,
      providers: savedDefinition ? [savedDefinition] : [],
    }));
    const upsertProviderDefinition = vi.fn(async (_id, body) => {
      savedDefinition = { ...body.definition, revision: 1 };
      return { protocol_version: "provider_definition_control.v1", registry_reloaded: true,
        registry_generation: 2, definition: savedDefinition,
        collection: { version: "provider_definition_collection.v1", revision: 1,
          providers: [savedDefinition] } };
    });
    const availableModelRoutes = vi.fn(async () => ({
      protocol_version: "model_route_catalog.v1", generation: 2,
      routes: qualified && savedDefinition ? [{ provider_id: savedDefinition.id,
        provider_name: savedDefinition.display_name, model: savedDefinition.default_model,
        definition_revision: savedDefinition.revision,
        enabled: true, credential_status: "configured", qualification_status: "available",
        harness_ready: true, selectable: true, unavailable_reason: "", default_for_routes: [],
        vision_capability: { state: "unknown", source: "unknown" } }] : [{
        provider_id: "official-openai", provider_name: "OpenAI", model: "gpt-5",
        definition_revision: 0, enabled: true, credential_status: "not_configured",
        qualification_status: "not_configured", harness_ready: false, selectable: false,
        unavailable_reason: "credential_not_configured", default_for_routes: [],
        vision_capability: { state: "unknown", source: "unknown" },
      }],
    }));
    const qualifyModelHarness = vi.fn(async () => {
      const result = await qualification.promise;
      qualified = true;
      return result;
    });
    const createThread = vi.fn().mockResolvedValue({ thread: createdThread });
    const client = { hasThreadControl: true, hasModelControl: true,
      hasProviderDefinitions: true, hasProviderCredentials: true,
      getPage: vi.fn(async (path: string) => ({ items: path === "/workspaces" ? [workspace] : [],
        page: { limit: 100 }, requestID: path })), availableModelRoutes,
      providerDefinitions, providerCredentialStatuses: vi.fn().mockResolvedValue({
        protocol_version: "provider_credential.v1", items: [] }),
      upsertProviderDefinition, changeProviderCredential: vi.fn().mockResolvedValue({
        protocol_version: "provider_credential.v1", provider: "official-openai", configured: true,
        store_available: true, store_kind: "windows_credential_manager", plaintext_returned: false,
        restart_required: false, registry_reloaded: true, registry_generation: 2 }),
      qualifyModelHarness, createThread,
    } as unknown as APIClient;
    render(<QueryClientProvider client={new QueryClient({ defaultOptions: {
      queries: { retry: false }, mutations: { retry: false },
    } })}><V2Workbench client={client} /></QueryClientProvider>);
    const user = userEvent.setup();
    const composer = await screen.findByRole("textbox", { name: "开始新对话" });
    await user.type(composer, "离开设置后继续保留的草稿");
    await user.click(screen.getByRole("button", { name: /模型路由，当前/u }));
    await user.click(within(screen.getByRole("menu", { name: "模型与响应设置" }))
      .getAllByRole("menuitem")[0]!);
    await user.click(await screen.findByRole("menuitem", { name: "添加模型供应商" }));
    await user.click(await screen.findByRole("button", { name: /OpenAI/u }));
    await user.type(screen.getByLabelText("API Key"), "first-model-key-123456");
    await user.click(screen.getByRole("button", { name: "保存并检查" }));
    await waitFor(() => expect(qualifyModelHarness).toHaveBeenCalledTimes(1));

    await user.click(screen.getByRole("button", { name: "返回应用" }));
    await user.click(screen.getByRole("button", { name: "新对话" }));
    const restored = await screen.findByRole("textbox", { name: "开始新对话" });
    expect(restored).toHaveValue("离开设置后继续保留的草稿");
    await act(async () => qualification.resolve({
      protocol_version: "model_harness_qualification.v1", provider: "official-openai",
      model: "gpt-5", status: "qualified", outcome: "success", failure_reason: "none",
      retryable: false, network_request_attempted: true, model_calls: 2,
      synthetic_tool_calls: 1, tool_executed: false, response_content_returned: false,
      duration_ms: 20, qualification_status: "available",
      harness: { protocol_version: "model_harness.v1", model: "gpt-5",
        transport_protocol: "openai_responses", tool_strategy: "native", json_strategy: "native",
        qualification_status: "verified", latest_qualification_status: "available",
        qualification_checked_at: "2026-09-20T00:00:00Z", qualification_source: "harness_qualification",
        tool_calls_qualified: true, tool_results_qualified: true, strict_json_qualified: true,
        streaming_qualified: true, root_eligible: true, structured_json_eligible: true,
        qualified_at: "2026-09-20T00:00:00Z", expires_at: "2026-09-27T00:00:00Z" },
    }));
    await waitFor(() => expect(screen.getByRole("textbox", { name: "开始新对话" }))
      .toHaveValue("离开设置后继续保留的草稿"));
    expect(screen.getByRole("button", { name: /模型路由，当前/u }))
      .not.toHaveAccessibleName(expect.stringContaining("gpt-5"));
    expect(createThread).not.toHaveBeenCalled();
  });
});

describe("V2Workbench first turn", () => {

  it.each([
    { outcome: "an empty catalog", result: { ...selectableModelCatalog(), routes: [] } },
    { outcome: "a selectable catalog", result: selectableModelCatalog("late-provider", "late-model") },
  ])("ignores $outcome returned for a different workspace", async ({ result }) => {
    window.history.replaceState({}, "", "#/new");
    const otherWorkspace = { ...workspace, id: "workspace-after-catalog", name: "Later workspace" };
    const catalog = deferred<any>();
    const createThread = vi.fn();
    const client = { hasThreadControl: true, hasWorkspaceImport: true,
      getPage: vi.fn(async (path: string) => ({
        items: path === "/workspaces" ? [workspace] : [],
        page: { limit: 100 }, requestID: path,
      })),
      importWorkspace: vi.fn().mockResolvedValue({ protocol_version: "workspace_import.v1",
        workspace: otherWorkspace, directory_content_modified: false, agent_authority_granted: false }),
      availableModelRoutes: vi.fn(() => catalog.promise), createThread,
    } as unknown as APIClient;
    render(<QueryClientProvider client={new QueryClient({ defaultOptions: {
      queries: { retry: false }, mutations: { retry: false },
    } })}><V2Workbench client={client} /></QueryClientProvider>);
    const user = userEvent.setup();
    await user.type(await screen.findByRole("textbox", { name: "开始新对话" }), "A项目等待目录的草稿");
    await user.click(screen.getByRole("button", { name: "发送消息" }));
    await user.click(screen.getByRole("button", { name: "接入项目" }));
    const dialog = screen.getByRole("dialog", { name: "接入已有项目" });
    await user.type(within(dialog).getByRole("textbox", { name: "项目文件夹路径" }), "D:\\later-workspace");
    await user.click(within(dialog).getByRole("button", { name: "接入此目录" }));
    await waitFor(() => expect(screen.getByRole("combobox", { name: "选择工作区" }))
      .toHaveValue(otherWorkspace.id));
    await act(async () => catalog.resolve(result));
    await waitFor(() => expect(window.location.hash).toBe("#/new"));
    expect(createThread).not.toHaveBeenCalled();
  });

  it("invalidates a pending catalog when returning through another workspace", async () => {
    window.history.replaceState({}, "", "#/new");
    const otherWorkspace = { ...workspace, id: "workspace-catalog-round-trip", name: "Round trip workspace" };
    const catalog = deferred<any>();
    const createThread = vi.fn().mockResolvedValue({ thread: createdThread });
    const client = { hasThreadControl: true, hasWorkspaceImport: true,
      getPage: vi.fn(async (path: string) => ({ items: path === "/workspaces" ? [workspace] : [],
        page: { limit: 100 }, requestID: path })),
      importWorkspace: vi.fn().mockResolvedValue({ protocol_version: "workspace_import.v1",
        workspace: otherWorkspace, directory_content_modified: false, agent_authority_granted: false }),
      availableModelRoutes: vi.fn(() => catalog.promise), createThread,
      submitThreadTurn: vi.fn().mockResolvedValue({ accepted: true }),
    } as unknown as APIClient;
    render(<QueryClientProvider client={new QueryClient({ defaultOptions: {
      queries: { retry: false }, mutations: { retry: false },
    } })}><V2Workbench client={client} /></QueryClientProvider>);
    const user = userEvent.setup();
    await user.type(await screen.findByRole("textbox", { name: "开始新对话" }), "A项目往返后仍未提交的草稿");
    await user.click(screen.getByRole("button", { name: "发送消息" }));
    await user.click(screen.getByRole("button", { name: "接入项目" }));
    const dialog = screen.getByRole("dialog", { name: "接入已有项目" });
    await user.type(within(dialog).getByRole("textbox", { name: "项目文件夹路径" }), "D:\\round-trip-workspace");
    await user.click(within(dialog).getByRole("button", { name: "接入此目录" }));
    await waitFor(() => expect(screen.getByRole("combobox", { name: "选择工作区" }))
      .toHaveValue(otherWorkspace.id));
    await user.selectOptions(screen.getByRole("combobox", { name: "选择工作区" }), workspace.id);
    expect(screen.getByRole("combobox", { name: "选择工作区" })).toHaveValue(workspace.id);
    await act(async () => catalog.resolve(selectableModelCatalog("late-provider", "late-model")));
    expect(window.location.hash).toBe("#/new");
    expect(createThread).not.toHaveBeenCalled();
  });

	it("opens model setup before creating when the fresh catalog has no selectable route", async () => {
	  window.history.replaceState({}, "", "#/new");
	  const createThread = vi.fn();
	  const availableModelRoutes = vi.fn().mockResolvedValue({
		protocol_version: "model_route_catalog.v1", generation: 3,
		routes: [{ ...selectableModelCatalog().routes[0], selectable: false,
		  credential_status: "not_configured", qualification_status: "not_configured",
		  harness_ready: false, unavailable_reason: "credential_not_configured" }],
	  });
	  const client = { hasThreadControl: true, hasModelControl: true,
		hasProviderDefinitions: true, hasProviderCredentials: true,
		getPage: vi.fn(async (path: string) => ({ items: path === "/workspaces" ? [workspace] : [],
		  page: { limit: 100 }, requestID: path })),
		availableModelRoutes, createThread,
		providerDefinitions: vi.fn().mockResolvedValue({ version: "provider_definition_collection.v1", revision: 0, providers: [] }),
		providerCredentialStatuses: vi.fn().mockResolvedValue({ protocol_version: "provider_credential.v1", items: [] }),
	  } as unknown as APIClient;
	  render(<QueryClientProvider client={new QueryClient({ defaultOptions: { queries: { retry: false }, mutations: { retry: false } } })}>
		<V2Workbench client={client} />
	  </QueryClientProvider>);
	  const user = userEvent.setup();
	  await user.type(await screen.findByRole("textbox", { name: "开始新对话" }), "先配置模型再发送这份草稿");
	  await user.click(screen.getByRole("button", { name: "发送消息" }));
	  await waitFor(() => expect(window.location.hash).toBe("#/new/settings/models"));
	  expect(createThread).not.toHaveBeenCalled();
	  await user.click(screen.getByRole("button", { name: "返回应用" }));
	  expect(await screen.findByRole("textbox", { name: "开始新对话" })).toHaveValue("先配置模型再发送这份草稿");
	});

  it("does not create or inspect a recovery request before model setup", async () => {
    window.history.replaceState({}, "", "#/new");
    const scope = `ds1_${"f".repeat(64)}`;
    const baseURL = "/api/v1";
    const storagePrefix = `v2_recovery.v1:${encodeURIComponent(window.location.origin)}:${encodeURIComponent(baseURL)}:${encodeURIComponent(scope)}:`;
    Object.keys(localStorage).filter((key) => key.startsWith(storagePrefix))
      .forEach((key) => localStorage.removeItem(key));
    act(() => useConnectionStore.getState().setHealth({ status: "ok", api_version: "api.v1",
      app_version: "fixture", schema_version: 166, data_store_id: scope }));
    const inspectThreadCreationRequest = vi.fn();
    const createThread = vi.fn();
    const submitThreadTurn = vi.fn();
    const client = { baseURL, hasThreadControl: true, hasModelControl: true,
      hasProviderDefinitions: true, hasProviderCredentials: true,
      getPage: vi.fn(async (path: string) => ({ items: path === "/workspaces" ? [workspace] : [],
        page: { limit: 100 }, requestID: path })),
      availableModelRoutes: vi.fn().mockResolvedValue({ ...selectableModelCatalog(), routes: [] }),
      inspectThreadCreationRequest, createThread, submitThreadTurn,
      providerDefinitions: vi.fn().mockResolvedValue({ version: "provider_definition_collection.v1", revision: 0, providers: [] }),
      providerCredentialStatuses: vi.fn().mockResolvedValue({ protocol_version: "provider_credential.v1", items: [] }),
    } as unknown as APIClient;
    render(<QueryClientProvider client={new QueryClient({ defaultOptions: {
      queries: { retry: false }, mutations: { retry: false },
    } })}><V2Workbench client={client} /></QueryClientProvider>);
    const user = userEvent.setup();
    await user.type(await screen.findByRole("textbox", { name: "开始新对话" }), "没有模型时不要写创建恢复记录");
    await user.click(screen.getByRole("button", { name: "发送消息" }));
    await waitFor(() => expect(window.location.hash).toBe("#/new/settings/models"));
    expect(inspectThreadCreationRequest).not.toHaveBeenCalled();
    expect(createThread).not.toHaveBeenCalled();
    expect(submitThreadTurn).not.toHaveBeenCalled();
    const creationStoragePrefix = storagePrefix + encodeURIComponent("creation:");
    expect(Object.keys(localStorage).filter((key) => key.startsWith(creationStoragePrefix))).toEqual([]);
  });

	it("keeps the first draft visible when the fresh model catalog cannot be read", async () => {
	  window.history.replaceState({}, "", "#/new");
	  const createThread = vi.fn();
	  const availableModelRoutes = vi.fn().mockRejectedValue(new Error("catalog connection failed"));
	  const client = { hasThreadControl: true,
		getPage: vi.fn(async (path: string) => ({ items: path === "/workspaces" ? [workspace] : [],
		  page: { limit: 100 }, requestID: path })),
		availableModelRoutes, createThread,
	  } as unknown as APIClient;
	  render(<QueryClientProvider client={new QueryClient({ defaultOptions: { queries: { retry: false }, mutations: { retry: false } } })}>
		<V2Workbench client={client} />
	  </QueryClientProvider>);
	  const user = userEvent.setup();
	  const composer = await screen.findByRole("textbox", { name: "开始新对话" });
	  await user.type(composer, "目录读取失败也保留原任务");
	  await user.click(screen.getByRole("button", { name: "发送消息" }));
	  expect(await screen.findByRole("alert")).toHaveTextContent("无法检查可用模型，草稿已保留");
	  expect(screen.getByRole("textbox", { name: "开始新对话" })).toHaveValue("目录读取失败也保留原任务");
	  expect(window.location.hash).toBe("#/new");
	  expect(createThread).not.toHaveBeenCalled();
	});

	it("does not let a late model preflight reclaim navigation after leaving the new conversation", async () => {
	  window.history.replaceState({}, "", "#/new");
	  const catalog = deferred<any>();
	  const createThread = vi.fn();
	  const client = { hasThreadControl: true,
		getPage: vi.fn(async (path: string) => ({ items: path === "/workspaces" ? [workspace] : [],
		  page: { limit: 100 }, requestID: path })),
		availableModelRoutes: vi.fn(() => catalog.promise), createThread,
	  } as unknown as APIClient;
	  render(<QueryClientProvider client={new QueryClient({ defaultOptions: { queries: { retry: false }, mutations: { retry: false } } })}>
		<V2Workbench client={client} />
	  </QueryClientProvider>);
	  const user = userEvent.setup();
	  await user.type(await screen.findByRole("textbox", { name: "开始新对话" }), "离开后也保留的首条草稿");
	  await user.click(screen.getByRole("button", { name: "发送消息" }));
	  await user.click(screen.getByRole("button", { name: "观察与记录" }));
	  expect(window.location.hash).toBe("#/new/inspector");
	  await act(async () => catalog.resolve({ ...selectableModelCatalog(), routes: [] }));
	  await waitFor(() => expect(window.location.hash).toBe("#/new/inspector"));
	  expect(createThread).not.toHaveBeenCalled();
	  await user.click(screen.getByRole("button", { name: "观察与记录" }));
	  expect(await screen.findByRole("textbox", { name: "开始新对话" })).toHaveValue("离开后也保留的首条草稿");
	});

	it("rejects a stale selected route before creation and preserves the exact draft", async () => {
	  window.history.replaceState({}, "", "#/new");
	  const stale = selectableModelCatalog("old-provider", "old-model");
	  const availableModelRoutes = vi.fn()
		.mockResolvedValueOnce(stale)
		.mockResolvedValueOnce({ ...stale, generation: 2, routes: [
		  { ...stale.routes[0], selectable: false, enabled: false,
			unavailable_reason: "definition_disabled" },
		  ...selectableModelCatalog("new-provider", "new-model").routes,
		] });
	  const createThread = vi.fn();
	  const client = { hasThreadControl: true, hasModelControl: true,
		hasProviderDefinitions: true, hasProviderCredentials: true,
		getPage: vi.fn(async (path: string) => ({ items: path === "/workspaces" ? [workspace] : [],
		  page: { limit: 100 }, requestID: path })), availableModelRoutes, createThread,
		providerDefinitions: vi.fn().mockResolvedValue({ version: "provider_definition_collection.v1", revision: 0, providers: [] }),
		providerCredentialStatuses: vi.fn().mockResolvedValue({ protocol_version: "provider_credential.v1", items: [] }),
	  } as unknown as APIClient;
	  render(<QueryClientProvider client={new QueryClient({ defaultOptions: { queries: { retry: false }, mutations: { retry: false } } })}>
		<V2Workbench client={client} />
	  </QueryClientProvider>);
	  const user = userEvent.setup();
	  const composer = await screen.findByRole("textbox", { name: "开始新对话" });
	  await user.click(screen.getByRole("button", { name: /模型路由，当前/u }));
	  await user.click(screen.getByRole("menuitem", { name: /^模型/ }));
	  await user.click(await screen.findByRole("menuitemradio", { name: /old-model/ }));
	  await user.type(composer, "旧模型失效时保留的任务");
	  await user.click(screen.getByRole("button", { name: "发送消息" }));
	  await waitFor(() => expect(window.location.hash).toBe("#/new/settings/models"));
	  expect(createThread).not.toHaveBeenCalled();
	  await user.click(screen.getByRole("button", { name: "返回应用" }));
	  expect(await screen.findByRole("textbox", { name: "开始新对话" })).toHaveValue("旧模型失效时保留的任务");
	});

  it("keeps the import receipt and current draft when a reimported project is outside the first page", async () => {
    const imported = { ...workspace, id: "workspace-older-import", name: "Older project" };
    const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    const client = { hasThreadControl: true, hasWorkspaceImport: true,
      importWorkspace: vi.fn().mockResolvedValue({ protocol_version: "workspace_import.v1",
        workspace: imported, directory_content_modified: false, agent_authority_granted: false }),
      getPage: vi.fn(async (path: string) => ({ items: path === "/workspaces" ? [workspace] : [],
        page: { limit: 100 }, requestID: path })) } as unknown as APIClient;
    render(<QueryClientProvider client={queryClient}><V2Workbench client={client} /></QueryClientProvider>);
    const user = userEvent.setup();
    await user.type(await screen.findByRole("textbox", { name: "开始新对话" }), "在新项目中继续这份需求");
    await user.click(screen.getByRole("button", { name: "接入项目" }));
    const dialog = screen.getByRole("dialog", { name: "接入已有项目" });
    await user.type(within(dialog).getByRole("textbox", { name: "项目文件夹路径" }), "D:\\older project");
    await user.click(within(dialog).getByRole("button", { name: "接入此目录" }));
    await waitFor(() => expect(screen.queryByRole("dialog")).not.toBeInTheDocument());
    await waitFor(() => expect(queryClient.isFetching()).toBe(0));
    expect(screen.getByRole("combobox", { name: "选择工作区" })).toHaveValue(imported.id);
    expect(screen.getByRole("textbox", { name: "开始新对话" })).toHaveValue("在新项目中继续这份需求");
    expect(client.importWorkspace).toHaveBeenCalledWith("D:\\older project");
    expect(screen.getByRole("button", { name: "接入项目" })).toHaveFocus();
  });

  it("preserves a new-task draft across the model settings round trip", async () => {
    const client = { hasThreadControl: true, getPage: vi.fn(async (path: string) => ({
      items: path === "/workspaces" ? [workspace] : [], page: { limit: 100 }, requestID: path,
    })) } as unknown as APIClient;
    render(<QueryClientProvider client={new QueryClient({ defaultOptions: { queries: { retry: false } } })}>
      <V2Workbench client={client} />
    </QueryClientProvider>);
    const user = userEvent.setup();
    await user.type(await screen.findByRole("textbox", { name: "开始新对话" }), "保留配置前的需求");
    await user.click(screen.getByRole("button", { name: "接入模型" }));
    await user.click(screen.getByRole("button", { name: "返回应用" }));
    expect(screen.getByRole("textbox", { name: "开始新对话" })).toHaveValue("保留配置前的需求");
  });

  it("imports through the native picker and preserves input when the picker is cancelled", async () => {
    let imported = false;
    vi.spyOn(desktopBridge, "desktopWorkspaceImportEnabled").mockReturnValue(true);
    const picker = vi.spyOn(desktopBridge, "importDesktopWorkspace")
      .mockResolvedValueOnce(null).mockImplementationOnce(async () => { imported = true; return workspace; });
    const client = { hasThreadControl: true, getPage: vi.fn(async (path: string) => ({
      items: path === "/workspaces" && imported ? [workspace] : [],
      page: { limit: 100 }, requestID: path,
    })) } as unknown as APIClient;
    render(<QueryClientProvider client={new QueryClient({ defaultOptions: { queries: { retry: false } } })}>
      <V2Workbench client={client} />
    </QueryClientProvider>);
    const user = userEvent.setup();
    const composer = await screen.findByRole("textbox", { name: "开始新对话" });
    await user.type(composer, "在选择项目之前写下的需求");
    expect(screen.getByRole("button", { name: "发送消息" })).toBeDisabled();
    await user.click(screen.getByRole("button", { name: "打开项目" }));
    expect(composer).toHaveValue("在选择项目之前写下的需求");
    await user.click(screen.getByRole("button", { name: "打开项目" }));
    await waitFor(() => expect(screen.getByRole("combobox", { name: "选择工作区" })).toHaveValue(workspace.id));
    expect(composer).toHaveValue("在选择项目之前写下的需求");
    expect(screen.getByRole("button", { name: "发送消息" })).toBeEnabled();
    expect(picker.mock.calls.every((args) => args.length === 0)).toBe(true);
  });

  it.each(["deliver", "plan"] as const)("opens the durable thread during the first %s turn with its selected phase", async (phase) => {
    const operationID = "00000000-0000-4000-8000-000000000042";
    const content = "请从这句首条消息直接开始执行。";
    let threads: ThreadView[] = [];
    const submission = deferred<Awaited<ReturnType<APIClient["submitThreadTurn"]>>>();

    vi.stubGlobal("crypto", {
      randomUUID: vi.fn(() => operationID),
    });

    const getPage = vi.fn(async (path: string) => {
      if (path === "/workspaces") {
        return {
          items: [workspace],
          page: { limit: 100 },
          requestID: "workspaces-request",
        };
      }
      if (path === "/threads") {
        return {
          items: threads,
          page: { limit: 100 },
          requestID: "threads-request",
        };
      }
      throw new Error(`Unexpected page request: ${path}`);
    });
    const createThread = vi.fn(async (
      _body: Parameters<APIClient["createThread"]>[0],
      _idempotencyKey: string,
    ): Promise<Awaited<ReturnType<APIClient["createThread"]>>> => {
      threads = [createdThread];
      return { thread: createdThread } as Awaited<ReturnType<APIClient["createThread"]>>;
    });
    const submitThreadTurn = vi.fn((
      _threadID: string,
      _body: Parameters<APIClient["submitThreadTurn"]>[1],
      _idempotencyKey: string,
    ) => submission.promise);
    const availableModelRoutes = vi.fn().mockResolvedValue({
      protocol_version: "model_route_catalog.v1",
      generation: 1,
      routes: [{ provider_id: "official-deepseek", provider_name: "DeepSeek",
        model: "deepseek-v4-flash", enabled: true, credential_status: "configured",
        qualification_status: "qualified", harness_ready: true, selectable: true,
        unavailable_reason: "", default_for_routes: ["code"] }],
    });
    const client = {
      hasThreadControl: true,
      hasModelControl: true,
      hasPlanDelivery: true,
      getPage,
      createThread,
      submitThreadTurn,
      availableModelRoutes,
    } as unknown as APIClient;
    const queryClient = new QueryClient({
      defaultOptions: {
        queries: { retry: false },
        mutations: { retry: false },
      },
    });

    render(
      <QueryClientProvider client={queryClient}>
        <V2Workbench client={client} />
      </QueryClientProvider>,
    );

    const composer = await screen.findByRole("textbox", { name: "开始新对话" });
    await waitFor(() => expect(composer).toBeEnabled());

    const user = userEvent.setup();
    expect(screen.getByRole("button", { name: "计划模式" })).toHaveAttribute("aria-pressed", "false");
    if (phase === "plan") await user.click(screen.getByRole("button", { name: "计划模式" }));
    expect(screen.getByRole("button", { name: "计划模式" })).toHaveAttribute("aria-pressed", String(phase === "plan"));
    expect(createThread).not.toHaveBeenCalled();
    expect(submitThreadTurn).not.toHaveBeenCalled();
    await user.click(screen.getByRole("button", { name: "模型路由，当前 默认 · 模型；推理强度：随模型" }));
    await user.click(screen.getByRole("menuitem", { name: /^模型/ }));
    await user.click(await screen.findByRole("menuitemradio", { name: /deepseek-v4-flash/ }));
    await user.type(composer, content);
    await waitFor(() => expect(screen.getByRole("button", { name: "发送消息" })).toBeEnabled());
    await user.click(screen.getByRole("button", { name: "发送消息" }));

    await waitFor(() => expect(submitThreadTurn).toHaveBeenCalledTimes(1));
    expect(screen.queryByRole("textbox", { name: "开始新对话" })).not.toBeInTheDocument();
    expect(await screen.findByTestId("v2-conversation")).toHaveTextContent(createdThread.id);
    expect(createThread).toHaveBeenCalledTimes(1);
    expect(createThread.mock.invocationCallOrder[0]).toBeLessThan(
      submitThreadTurn.mock.invocationCallOrder[0],
    );

    const [createBody, createKey] = createThread.mock.calls[0]!;
    const [turnThreadID, turnBody, turnKey] = submitThreadTurn.mock.calls[0]!;

    expect(createBody).toMatchObject({
      phase,
      workspace_id: workspace.id,
      goal: content,
      network_mode: "disabled",
      provider: "official-deepseek",
      model: "deepseek-v4-flash",
    });
    expect(turnThreadID).toBe(createdThread.id);
    expect(turnBody).toEqual({
      version: "thread_message_submission.v1",
      content,
    });
    expect(turnBody.content).toBe(createBody.goal);

    expect(createKey).toBe(`v2-thread-create-${operationID}`);
    expect(turnKey).toBe(`v2-thread-create-turn-${operationID}`);
    expect(turnKey).not.toBe(createKey);
    expect(turnKey).toBe(createKey.replace("v2-thread-create-", "v2-thread-create-turn-"));

    await act(async () => {
      submission.resolve({ accepted: true } as unknown as Awaited<
        ReturnType<APIClient["submitThreadTurn"]>>);
      await submission.promise;
    });

    expect(await screen.findByTestId("v2-conversation")).toHaveTextContent(createdThread.id);
    expect(screen.queryByRole("textbox", { name: "开始新对话" })).not.toBeInTheDocument();
  });

  it("opens the already-created thread when its first turn fails", async () => {
    const operationID = "00000000-0000-4000-8000-000000000043";
    const content = "即使首轮执行失败，也继续留在这个对话。";
    const submission = deferred<Awaited<ReturnType<APIClient["submitThreadTurn"]>>>();
    let threads: ThreadView[] = [];

    vi.stubGlobal("crypto", { randomUUID: vi.fn(() => operationID) });
    const getPage = vi.fn(async (path: string) => ({
      items: path === "/workspaces" ? [workspace] : threads,
      page: { limit: 100 },
      requestID: `${path}-request`,
    }));
    const createThread = vi.fn(async () => {
      threads = [createdThread];
      return { thread: createdThread } as Awaited<ReturnType<APIClient["createThread"]>>;
    });
    const submitThreadTurn = vi.fn(() => submission.promise);
    const client = {
      hasThreadControl: true,
      hasEvidenceAttachment: true,
      getPage,
      createThread,
      submitThreadTurn,
	  availableModelRoutes: readyModelCatalog(),
    } as unknown as APIClient;
    const queryClient = new QueryClient({
      defaultOptions: { queries: { retry: false }, mutations: { retry: false } },
    });
    const files = [{ id: "first-file", path: "README.md", digest: "a".repeat(64), partial: false, redacted: false }];
    queryClient.setQueryData(v2FileReferenceKey(workspace.id, ""), files);

    render(<QueryClientProvider client={queryClient}>
      <V2Workbench client={client} />
    </QueryClientProvider>);

    const composer = await screen.findByRole("textbox", { name: "开始新对话" });
    await waitFor(() => expect(composer).toBeEnabled());
    const user = userEvent.setup();
    await user.type(composer, content);
    await user.click(screen.getByRole("button", { name: "发送消息" }));

    await waitFor(() => expect(submitThreadTurn).toHaveBeenCalledTimes(1));
    expect(await screen.findByTestId("v2-conversation")).toHaveTextContent(createdThread.id);
    await act(async () => {
      submission.reject(new APIRequestError("first turn failed at its durable boundary", "UNAVAILABLE", 503,
        "failed-turn", undefined, undefined, true));
      await Promise.resolve();
    });

    expect(await screen.findByTestId("v2-conversation")).toHaveTextContent(createdThread.id);
    expect(createThread).toHaveBeenCalledTimes(1);
    expect(submitThreadTurn).toHaveBeenCalledTimes(1);
    expect(screen.queryByRole("textbox", { name: "开始新对话" })).not.toBeInTheDocument();
    // A known execution failure has already admitted this exact first input.
    // Its original payload remains in the turn record, not as an unsent draft.
    expect(screen.getByRole("textbox", { name: "任务草稿 fixture" })).toHaveValue("");
    expect(queryClient.getQueryData(v2FileReferenceKey(workspace.id, createdThread.id))).toEqual([]);
    expect(queryClient.getQueryData(v2FileReferenceKey(workspace.id, ""))).toEqual([]);
    expect(submitThreadTurn).toHaveBeenCalledWith(createdThread.id, {
      version: "thread_message_submission.v1", content,
      files: [{ source_kind: "workspace_file", path: "README.md", expected_sha256: files[0]!.digest }],
    }, `v2-thread-create-turn-${operationID}`);
  });

  it("hands off only the submitted draft and files when creation finishes after project navigation", async () => {
    const otherWorkspace = { ...workspace, id: "workspace-other", name: "Other project" };
    const creation = deferred<Awaited<ReturnType<APIClient["createThread"]>>>();
    const submission = deferred<Awaited<ReturnType<APIClient["submitThreadTurn"]>>>();
    const files = [{ id: "submitted-file", path: "README.md", digest: "a".repeat(64), partial: false, redacted: false }];
    const lateFile = { ...files[0]!, id: "late-file", path: "notes.md" };
    const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false }, mutations: { retry: false } } });
    queryClient.setQueryData(v2FileReferenceKey(workspace.id, ""), files);
    const client = { hasThreadControl: true, hasEvidenceAttachment: true,
      getPage: vi.fn(async (path: string) => ({ items: path === "/workspaces" ? [workspace, otherWorkspace] : [],
        page: { limit: 100 }, requestID: path })),
      createThread: vi.fn(() => creation.promise), submitThreadTurn: vi.fn(() => submission.promise),
	  availableModelRoutes: readyModelCatalog(),
    } as unknown as APIClient;
    render(<QueryClientProvider client={queryClient}><V2Workbench client={client} /></QueryClientProvider>);
    const user = userEvent.setup();
    const input = await screen.findByRole("textbox", { name: "开始新对话" });
    await user.type(input, "首条提交");
    await user.click(screen.getByRole("button", { name: "发送消息" }));
    fireEvent.change(input, { target: { value: "创建期间的新草稿" } });
    act(() => queryClient.setQueryData(v2FileReferenceKey(workspace.id, ""), [...files, lateFile]));
    await user.click(screen.getByRole("button", { name: "接入模型" }));
    await user.click(screen.getByRole("button", { name: "返回应用" }));
    await user.selectOptions(screen.getByRole("combobox", { name: "选择工作区" }), otherWorkspace.id);
    await user.type(screen.getByRole("textbox", { name: "开始新对话" }), "另一项目的独立草稿");
    await act(async () => { creation.resolve({ thread: createdThread } as Awaited<ReturnType<APIClient["createThread"]>>); });
    expect(window.location.hash).toBe("#/new");
    expect(screen.getByRole("textbox", { name: "开始新对话" })).toHaveValue("另一项目的独立草稿");
    expect(screen.getByRole("button", { name: "发送消息" })).toBeEnabled();
    await user.click(await screen.findByRole("button", { name: "打开已创建的对话" }));
    expect(await screen.findByRole("textbox", { name: "任务草稿 fixture" })).toHaveValue("首条提交");
    expect(queryClient.getQueryData(v2FileReferenceKey(workspace.id, createdThread.id))).toEqual(files);
    expect(queryClient.getQueryData(v2FileReferenceKey(workspace.id, ""))).toEqual([lateFile]);
    fireEvent.change(screen.getByRole("textbox", { name: "任务草稿 fixture" }), { target: { value: "任务内追加的草稿" } });
    await user.click(screen.getByRole("button", { name: "新对话" }));
    await user.selectOptions(screen.getByRole("combobox", { name: "选择工作区" }), workspace.id);
    expect(screen.getByRole("textbox", { name: "开始新对话" })).toHaveValue("创建期间的新草稿");
    await user.selectOptions(screen.getByRole("combobox", { name: "选择工作区" }), otherWorkspace.id);
    expect(screen.getByRole("textbox", { name: "开始新对话" })).toHaveValue("另一项目的独立草稿");
    await act(async () => { submission.resolve({ accepted: true } as unknown as Awaited<ReturnType<APIClient["submitThreadTurn"]>>); });
    expect(screen.getByRole("textbox", { name: "开始新对话" })).toHaveValue("另一项目的独立草稿");
    expect(queryClient.getQueryData(v2FileReferenceKey(workspace.id, createdThread.id))).toEqual([]);
    act(() => { window.history.pushState({}, "", `#/threads/${createdThread.id}`); window.dispatchEvent(new PopStateEvent("popstate")); });
    expect(await screen.findByRole("textbox", { name: "任务草稿 fixture" })).toHaveValue("任务内追加的草稿");
    expect(client.createThread).toHaveBeenCalledTimes(1);
  });

  it("reuses the creation idempotency key when a creation response is retried", async () => {
    const operationID = "00000000-0000-4000-8000-000000000044";
    const content = "响应丢失后不要创建第二个 Thread。";
    let threads: ThreadView[] = [];

    const randomUUID = vi.fn(() => operationID);
    vi.stubGlobal("crypto", { randomUUID });
    const getPage = vi.fn(async (path: string) => ({
      items: path === "/workspaces" ? [workspace] : threads,
      page: { limit: 100 },
      requestID: `${path}-request`,
    }));
    const createThread = vi.fn()
      .mockRejectedValueOnce(new Error("creation response was lost"))
      .mockImplementationOnce(async () => {
        threads = [createdThread];
        return { thread: createdThread } as Awaited<ReturnType<APIClient["createThread"]>>;
      });
    const submitThreadTurn = vi.fn().mockResolvedValue(
      { accepted: true } as unknown as Awaited<ReturnType<APIClient["submitThreadTurn"]>>,
    );
    const client = {
      hasThreadControl: true,
      getPage,
      createThread,
      submitThreadTurn,
	  availableModelRoutes: readyModelCatalog(),
    } as unknown as APIClient;
    const queryClient = new QueryClient({
      defaultOptions: { queries: { retry: false }, mutations: { retry: false } },
    });

    render(<QueryClientProvider client={queryClient}>
      <V2Workbench client={client} />
    </QueryClientProvider>);

    const composer = await screen.findByRole("textbox", { name: "开始新对话" });
    await waitFor(() => expect(composer).toBeEnabled());
    const user = userEvent.setup();
    await user.type(composer, content);
    await user.click(screen.getByRole("button", { name: "发送消息" }));
    expect(await screen.findByRole("alert")).toHaveTextContent("creation response was lost");

    await user.click(screen.getByRole("button", { name: "发送消息" }));
    expect(await screen.findByTestId("v2-conversation")).toHaveTextContent(createdThread.id);

    expect(createThread).toHaveBeenCalledTimes(2);
    expect(createThread.mock.calls[0]?.[1]).toBe(`v2-thread-create-${operationID}`);
    expect(createThread.mock.calls[1]?.[1]).toBe(`v2-thread-create-${operationID}`);
    expect(randomUUID).toHaveBeenCalledTimes(1);
    expect(submitThreadTurn).toHaveBeenCalledTimes(1);
  });

  it("keeps each project's unknown creation key when another project's response arrives late", async () => {
    const otherWorkspace = { ...workspace, id: "workspace-second-create", name: "Second project" };
    const otherThread = { ...createdThread, id: "thread-second-create", workspace_id: otherWorkspace.id };
    const firstCreation = deferred<Awaited<ReturnType<APIClient["createThread"]>>>();
    const secondCreation = deferred<Awaited<ReturnType<APIClient["createThread"]>>>();
    const createThread = vi.fn().mockImplementationOnce(() => firstCreation.promise)
      .mockImplementationOnce(() => secondCreation.promise).mockResolvedValueOnce({ thread: otherThread });
    const client = { hasThreadControl: true, createThread,
      getPage: vi.fn(async (path: string) => ({ items: path === "/workspaces" ? [workspace, otherWorkspace] : [],
        page: { limit: 100 }, requestID: path })),
      submitThreadTurn: vi.fn().mockResolvedValue({ accepted: true }),
	  availableModelRoutes: readyModelCatalog(),
    } as unknown as APIClient;
    const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false }, mutations: { retry: false } } });
    render(<QueryClientProvider client={queryClient}><V2Workbench client={client} /></QueryClientProvider>);
    const user = userEvent.setup();
    await user.type(await screen.findByRole("textbox", { name: "开始新对话" }), "A项目请求");
    await user.click(screen.getByRole("button", { name: "发送消息" }));
    await user.click(screen.getByRole("button", { name: "接入模型" }));
    await user.click(screen.getByRole("button", { name: "返回应用" }));
    await user.selectOptions(screen.getByRole("combobox", { name: "选择工作区" }), otherWorkspace.id);
    await user.type(screen.getByRole("textbox", { name: "开始新对话" }), "B项目请求");
    await user.click(screen.getByRole("button", { name: "发送消息" }));
    await waitFor(() => expect(createThread).toHaveBeenCalledTimes(2));
    await act(async () => { secondCreation.reject(new Error("B creation response lost")); });
    expect(await screen.findByRole("alert")).toHaveTextContent("B creation response lost");
    await act(async () => { firstCreation.resolve({ thread: createdThread } as Awaited<ReturnType<APIClient["createThread"]>>); });
    expect(window.location.hash).toBe("#/new");
    expect(screen.getByRole("alert")).toHaveTextContent("B creation response lost");
    expect(screen.getByRole("button", { name: "打开已创建的对话" })).toBeInTheDocument();
    expect(screen.getByRole("textbox", { name: "开始新对话" })).toHaveValue("B项目请求");
    await user.click(screen.getByRole("button", { name: "发送消息" }));
    expect(await screen.findByTestId("v2-conversation")).toHaveTextContent(otherThread.id);
    expect(createThread).toHaveBeenCalledTimes(3);
    expect(createThread.mock.calls[1]).toEqual(createThread.mock.calls[2]);
    expect(createThread.mock.calls[0]![1]).not.toBe(createThread.mock.calls[1]![1]);
    expect(client.submitThreadTurn).toHaveBeenCalledTimes(2);
  });

  it.each(["created", "creation failed", "turn failed"] as const)(
    "keeps task B and its draft when task A finishes in the background: %s", async (outcome) => {
      window.history.replaceState({}, "", "#/new");
      const otherThread = { ...createdThread, id: "thread-existing-b", title: "Existing B" };
      const creation = deferred<Awaited<ReturnType<APIClient["createThread"]>>>();
      const submitThreadTurn = vi.fn().mockImplementation(async (_threadID: string) => {
        if (outcome === "turn failed") throw new APIRequestError("CONFLICT", "A execution failed", 409,
          "failed-turn", undefined, undefined, true);
        return { accepted: true };
      });
      const client = { hasThreadControl: true,
        getPage: vi.fn(async (path: string) => ({ items: path === "/workspaces" ? [workspace] : [otherThread],
          page: { limit: 100 }, requestID: path })),
        createThread: vi.fn(() => creation.promise), submitThreadTurn,
        availableModelRoutes: readyModelCatalog(),
      } as unknown as APIClient;
      const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false }, mutations: { retry: false } } });
      render(<QueryClientProvider client={queryClient}><V2Workbench client={client} /></QueryClientProvider>);
      const user = userEvent.setup();
      await user.type(await screen.findByRole("textbox", { name: "开始新对话" }), "A 的首条消息");
      await user.click(screen.getByRole("button", { name: "发送消息" }));
      await waitFor(() => expect(client.createThread).toHaveBeenCalledTimes(1));
      await user.click(screen.getByRole("button", { name: "Existing B" }));
      await user.type(screen.getByRole("textbox", { name: "任务草稿 fixture" }), "B 的独立草稿");
      await act(async () => {
        if (outcome === "creation failed") creation.reject(new Error("A creation response lost"));
        else creation.resolve({ thread: createdThread } as Awaited<ReturnType<APIClient["createThread"]>>);
      });
      await waitFor(() => expect(queryClient.isMutating()).toBe(0));
      expect(window.location.hash).toBe(`#/threads/${otherThread.id}`);
      expect(screen.getByTestId("v2-conversation")).toHaveTextContent(otherThread.id);
      expect(screen.getByRole("textbox", { name: "任务草稿 fixture" })).toHaveValue("B 的独立草稿");
      expect(screen.queryByRole("alert")).not.toBeInTheDocument();
      if (outcome === "creation failed") {
        expect(submitThreadTurn).not.toHaveBeenCalled();
        expect(screen.queryByRole("button", { name: "打开已创建的对话" })).not.toBeInTheDocument();
        await user.click(screen.getByRole("button", { name: "新对话" }));
        expect(screen.getByRole("textbox", { name: "开始新对话" })).toHaveValue("A 的首条消息");
      } else {
        expect(submitThreadTurn).toHaveBeenCalledTimes(1);
        expect(submitThreadTurn.mock.calls[0]?.[0]).toBe(createdThread.id);
        await user.click(screen.getByRole("button", { name: "打开已创建的对话" }));
        expect(window.location.hash).toBe(`#/threads/${createdThread.id}`);
        expect(screen.getByRole("textbox", { name: "任务草稿 fixture" })).toHaveValue("");
        expect(screen.queryByRole("button", { name: "打开已创建的对话" })).not.toBeInTheDocument();
        const mutations = queryClient.getMutationCache().findAll({ mutationKey: ["v2", "submit-turn"] });
        if (outcome === "turn failed") {
          expect(mutations).toHaveLength(1);
          expect(mutations[0]?.state).toMatchObject({ variables: { threadID: createdThread.id }, status: "error" });
        } else expect(mutations).toHaveLength(0);
      }
    });

  it("offers an explicit open after leaving and returning to the same new-task page", async () => {
    const creation = deferred<Awaited<ReturnType<APIClient["createThread"]>>>();
    const client = { hasThreadControl: true,
      getPage: vi.fn(async (path: string) => ({ items: path === "/workspaces" ? [workspace] : [],
        page: { limit: 100 }, requestID: path })),
      createThread: vi.fn(() => creation.promise), submitThreadTurn: vi.fn().mockResolvedValue({ accepted: true }),
      availableModelRoutes: readyModelCatalog(),
    } as unknown as APIClient;
    const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false }, mutations: { retry: false } } });
    render(<QueryClientProvider client={queryClient}><V2Workbench client={client} /></QueryClientProvider>);
    const user = userEvent.setup();
    await user.type(await screen.findByRole("textbox", { name: "开始新对话" }), "原创建请求");
    await user.click(screen.getByRole("button", { name: "发送消息" }));
    await waitFor(() => expect(client.createThread).toHaveBeenCalledTimes(1));
    await user.click(screen.getByRole("button", { name: "接入模型" }));
    await user.click(screen.getByRole("button", { name: "返回应用" }));
    fireEvent.change(screen.getByRole("textbox", { name: "开始新对话" }), { target: { value: "返回后正在写的新需求" } });
    await act(async () => { creation.resolve({ thread: createdThread } as Awaited<ReturnType<APIClient["createThread"]>>); });
    expect(window.location.hash).toBe("#/new");
    expect(screen.getByRole("textbox", { name: "开始新对话" })).toHaveValue("返回后正在写的新需求");
    await user.click(screen.getByRole("button", { name: "打开已创建的对话" }));
    expect(screen.getByTestId("v2-conversation")).toHaveTextContent(createdThread.id);
  });

  it.each(["created", "response lost"] as const)(
    "retains the durable creation handoff after navigation: %s", async (outcome) => {
      window.history.replaceState({}, "", "#/new");
      useConnectionStore.getState().setHealth({ status: "ok", api_version: "api.v1",
        app_version: "fixture", schema_version: 157, data_store_id: `ds1_${"c".repeat(64)}` });
      const otherThread = { ...createdThread, id: "thread-durable-b", title: "Durable B" };
      const creation = deferred<Awaited<ReturnType<APIClient["createThread"]>>>();
      const submission = deferred<Awaited<ReturnType<APIClient["submitThreadTurn"]>>>();
      const inspectThreadCreationRequest = vi.fn().mockResolvedValue({ kind: "creation",
        workspace_id: workspace.id, state: "not_received", settled: false });
      const client = { baseURL: "/api/v1", hasThreadControl: true,
        getPage: vi.fn(async (path: string) => ({ items: path === "/workspaces" ? [workspace] : [otherThread],
          page: { limit: 100 }, requestID: path })),
        get: vi.fn().mockResolvedValue({ thread: createdThread }),
        createThread: vi.fn(() => creation.promise), submitThreadTurn: vi.fn(() => submission.promise),
        inspectThreadCreationRequest, availableModelRoutes: readyModelCatalog(),
      } as unknown as APIClient;
      render(<QueryClientProvider client={new QueryClient({ defaultOptions: { queries: { retry: false } } })}>
        <V2Workbench client={client} />
      </QueryClientProvider>);
      const savedValues = () => Object.entries(window.localStorage)
        .filter(([key]) => key.startsWith("v2_recovery.v1:"))
        .map(([, value]: [string, string]) => JSON.parse(value).value);
      const user = userEvent.setup();
      await user.type(await screen.findByRole("textbox", { name: "开始新对话" }), "持久保存的 A 请求");
      await user.click(screen.getByRole("button", { name: "发送消息" }));
      await waitFor(() => expect(client.createThread).toHaveBeenCalledTimes(1));
      const intent = savedValues().find((value) => value?.version === "v2_creation_recovery.v1");
      expect(intent).toMatchObject({ content: "持久保存的 A 请求", request: { workspace_id: workspace.id } });
      await user.click(screen.getByRole("button", { name: "Durable B" }));
      await user.type(screen.getByRole("textbox", { name: "任务草稿 fixture" }), "B 的当前草稿");
      await act(async () => {
        if (outcome === "response lost") creation.reject(new Error("creation response lost"));
        else creation.resolve({ thread: createdThread } as Awaited<ReturnType<APIClient["createThread"]>>);
      });
      expect(window.location.hash).toBe(`#/threads/${otherThread.id}`);
      expect(screen.getByRole("textbox", { name: "任务草稿 fixture" })).toHaveValue("B 的当前草稿");
      expect(screen.queryByRole("alert")).not.toBeInTheDocument();
      if (outcome === "created") {
        await waitFor(() => expect(client.submitThreadTurn).toHaveBeenCalledTimes(1));
        expect(savedValues().find((value) => value?.operationKey === `v2-thread-create-turn-${intent.operationID}`))
          .toMatchObject({ threadID: createdThread.id, workspaceID: workspace.id, content: intent.content });
        expect(savedValues().filter((value) => value?.version === "v2_creation_recovery.v1")).toEqual([]);
        await act(async () => { submission.resolve({ accepted: true } as unknown as Awaited<ReturnType<APIClient["submitThreadTurn"]>>); });
        await waitFor(() => expect(savedValues().find((value) => value?.operationKey === `v2-thread-create-turn-${intent.operationID}`)).toBeUndefined());
        expect(screen.getByRole("textbox", { name: "任务草稿 fixture" })).toHaveValue("B 的当前草稿");
        await user.click(screen.getByRole("button", { name: "打开已创建的对话" }));
      } else {
        expect(client.submitThreadTurn).not.toHaveBeenCalled();
        inspectThreadCreationRequest.mockResolvedValue({ kind: "creation", workspace_id: workspace.id,
          state: "completed", settled: true, thread_id: createdThread.id, run_id: createdThread.last_run_id,
          session_id: "session-created", request_fingerprint: "d".repeat(64) });
        await user.click(screen.getByRole("button", { name: "新对话" }));
        expect(screen.getByRole("textbox", { name: "开始新对话" })).toHaveValue("持久保存的 A 请求");
        await user.click(await screen.findByRole("button", { name: "打开原对话" }));
        expect(client.submitThreadTurn).not.toHaveBeenCalled();
      }
      expect(window.location.hash).toBe(`#/threads/${createdThread.id}`);
      expect(client.createThread).toHaveBeenCalledTimes(1);
    });

  it.each(["creation failed", "turn failed"] as const)(
    "preserves a newer creation error after switching projects and returning: %s", async (outcome) => {
      const otherWorkspace = { ...workspace, id: "workspace-round-trip", name: "Other project" };
      const creation = deferred<Awaited<ReturnType<APIClient["createThread"]>>>();
      const createThread = vi.fn().mockImplementationOnce(() => creation.promise)
        .mockRejectedValueOnce(new Error("new request failed"));
      const client = { hasThreadControl: true, createThread,
        getPage: vi.fn(async (path: string) => ({ items: path === "/workspaces" ? [workspace, otherWorkspace] : [],
          page: { limit: 100 }, requestID: path })),
        submitThreadTurn: vi.fn().mockRejectedValue(new APIRequestError("old turn failed", "CONFLICT", 409,
          "failed-turn", undefined, undefined, true)), availableModelRoutes: readyModelCatalog(),
      } as unknown as APIClient;
      const queries = new QueryClient({ defaultOptions: { queries: { retry: false }, mutations: { retry: false } } });
      render(<QueryClientProvider client={queries}><V2Workbench client={client} /></QueryClientProvider>);
      const user = userEvent.setup();
      await user.type(await screen.findByRole("textbox", { name: "开始新对话" }), "旧创建请求");
      await user.click(screen.getByRole("button", { name: "发送消息" }));
      await waitFor(() => expect(createThread).toHaveBeenCalledTimes(1));
      await user.selectOptions(screen.getByRole("combobox", { name: "选择工作区" }), otherWorkspace.id);
      await user.selectOptions(screen.getByRole("combobox", { name: "选择工作区" }), workspace.id);
      fireEvent.change(screen.getByRole("textbox", { name: "开始新对话" }), { target: { value: "新创建请求" } });
      await user.click(screen.getByRole("button", { name: "发送消息" }));
      expect(await screen.findByRole("alert")).toHaveTextContent("new request failed");
      await act(async () => {
        if (outcome === "creation failed") creation.reject(new Error("old creation failed"));
        else creation.resolve({ thread: createdThread } as Awaited<ReturnType<APIClient["createThread"]>>);
      });
      await waitFor(() => expect(queries.isMutating()).toBe(0));
      expect(screen.getByRole("alert")).toHaveTextContent("new request failed");
      expect(screen.getByRole("textbox", { name: "开始新对话" })).toHaveValue("新创建请求");
      expect(screen.getByRole("button", { name: "发送消息" })).toBeEnabled();
      expect(window.location.hash).toBe("#/new");
    });
});

it("switches the new conversation and shell language without changing user project names or thread titles", async () => {
  window.history.replaceState({}, "", "#/new");
  const project = { ...workspace, name: "我的新对话项目" };
  const thread = { ...createdThread, title: "新对话：排查连接" };
  const client = Object.assign(navigationClient(), {
    getPage: vi.fn(async (path: string) => ({ items: path === "/workspaces" ? [project] : [thread],
      page: { limit: 100 }, requestID: path })),
  });
  render(<QueryClientProvider client={new QueryClient({ defaultOptions: { queries: { retry: false } } })}>
    <LocaleSwitch /><V2Workbench client={client} /></QueryClientProvider>);
  const user = userEvent.setup();
  await screen.findByRole("region", { name: "项目 我的新对话项目" });
  const input = screen.getByRole("textbox", { name: "开始新对话" });
  await user.type(input, "保持这段新对话需求");
  expect(screen.getByRole("heading", { name: "接下来要做什么？" })).toBeInTheDocument();
  expect(screen.getByRole("button", { name: "隐藏侧栏" })).toBeInTheDocument();

  await user.click(screen.getByRole("button", { name: "Switch test language" }));
  expect(screen.getByRole("heading", { name: "What would you like to do next?" })).toBeInTheDocument();
  expect(screen.queryByRole("heading", { name: "接下来要做什么？" })).not.toBeInTheDocument();
  expect(screen.getByRole("navigation", { name: "Conversation navigation" })).toBeInTheDocument();
  expect(screen.getByRole("button", { name: "Hide sidebar" })).toBeInTheDocument();
  expect(screen.getByRole("button", { name: "Task budget and project configuration" })).toHaveTextContent("Task configuration");
  expect(screen.getByRole("button", { name: "Connect project" })).toBeInTheDocument();
  expect(screen.getByRole("button", { name: "Plan mode" })).toBeInTheDocument();
  expect(screen.getByRole("button", { name: "Rule-based search" })).toBeInTheDocument();
  expect(input).toHaveAttribute("placeholder", "Enter a task or question…");
  expect(input).toHaveValue("保持这段新对话需求");
  const group = screen.getByRole("region", { name: "Project 我的新对话项目" });
  expect(within(group).getByText("我的新对话项目")).toBeInTheDocument();
  expect(within(group).getByRole("button", { name: "新对话：排查连接" })).toBeInTheDocument();
  expect(screen.getByRole("button", { name: "Archive 新对话：排查连接" })).toBeInTheDocument();
  expect(window.localStorage.getItem("prayu.locale.v1")).toBe("en-US");

  await user.click(screen.getByRole("button", { name: "Switch test language" }));
  expect(screen.getByRole("heading", { name: "接下来要做什么？" })).toBeInTheDocument();
  expect(screen.getByRole("navigation", { name: "对话导航" })).toBeInTheDocument();
  expect(input).toHaveAttribute("placeholder", "输入任务或问题…");
  expect(input).toHaveValue("保持这段新对话需求");
  expect(screen.getByRole("button", { name: "新对话：排查连接" })).toBeInTheDocument();
  expectNoNavigationWrites(client);
});

// Retain textarea coverage through the supported plain-text editing mode.
beforeEach(() => {
  localStorage.setItem("prayu.locale.v1", "zh-CN");
  localStorage.setItem("universal-code.composer-plain-text", "true");
});
