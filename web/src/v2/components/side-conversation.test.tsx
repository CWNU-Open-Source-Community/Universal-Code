import { cleanup, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import type { ComponentProps } from "react";
import type { APIClient } from "../../api/client";
import type { WorkspaceView } from "../../api/types";
import { LocaleProvider } from "../../lib/locale";
import { useConnectionStore } from "../../state/connection";
import { V2Composer, type V2SideChatAskRequest } from "./composer";
import { v2SideChatStorageKey } from "./side-conversation";

afterEach(() => {
  cleanup();
  localStorage.clear();
  useConnectionStore.setState({ health: null });
});

const workspaces: WorkspaceView[] = [
  { id: "ws-main", name: "Core Engine", created_at: "2026-11-17T00:00:00Z" },
];

function createSideChatClient(overrides: Partial<APIClient> = {}): APIClient {
  return {
    baseURL: "/api/v1",
    hasControl: true,
    hasThreadControl: true,
    hasModelControl: true,
    hasPlanDelivery: true,
    availableModelRoutes: vi.fn().mockResolvedValue({
      generation: 1,
      routes: [
        {
          provider_id: "official-anthropic",
          model: "claude-sonnet-4",
          selectable: true,
          enabled: true,
          credential_status: "configured",
          harness_ready: true,
          qualification_status: "verified",
          unavailable_reason: "",
        },
        {
          provider_id: "official-deepseek",
          model: "deepseek-r1",
          selectable: true,
          enabled: true,
          credential_status: "configured",
          harness_ready: true,
          qualification_status: "verified",
          unavailable_reason: "",
        },
      ],
    }),
    threadModelRoute: vi.fn().mockResolvedValue({
      thread_id: "thread-main-busy",
      provider: "official-anthropic",
      model: "claude-sonnet-4",
      source: "thread",
    }),
    selectThreadModelRoute: vi.fn(),
    createThread: vi.fn().mockResolvedValue({
      version: "thread_creation.v1",
      thread: {
        id: "side-ephemeral-1",
        workspace_id: "ws-main",
        title: "[Side Q&A]",
        status: "active",
        version: 1,
        last_run_id: "run-side-1",
      },
    }),
    submitThreadTurn: vi.fn().mockResolvedValue({
      version: "thread_message_submission.v1",
      thread_id: "side-ephemeral-1",
      run_id: "run-side-1",
      execution_started: false,
      steering: { id: "msg-side-1", status: "accepted" },
    }),
    getPage: vi.fn().mockResolvedValue({
      items: [
        {
          id: "item-side-model-1",
          run_id: "run-side-1",
          run_ordinal: 1,
          sequence: 2,
          canonical_id: "canon-side-1",
          source: "model",
          kind: "assistant_message",
          title: "支线分析结论",
          detail: "建议使用 **读写锁 (`sync.RWMutex`)** 保护并发缓存映射，示例代码：\n```go\nmu.RLock()\ndefer mu.RUnlock()\n```",
          created_at: "2026-11-17T22:30:00Z",
          durable: true,
          provisional: false,
        },
      ],
      page: { next_cursor: "" },
    }),
    get: vi.fn().mockResolvedValue({
      thread: { id: "side-ephemeral-1", version: 2 },
    }),
    transitionThread: vi.fn().mockResolvedValue({
      version: "thread_lifecycle.v1",
    }),
    ...overrides,
  } as unknown as APIClient;
}

function setupSideChat(
  props: Partial<ComponentProps<typeof V2Composer>> = {},
  clientOverrides: Partial<APIClient> = {},
) {
  localStorage.setItem("prayu.locale.v1", "zh-CN");
  localStorage.setItem("universal-code.composer-plain-text", "true");
  useConnectionStore.setState({
    health: {
      api_version: "api.v1",
      app_version: "1.0.0",
      schema_version: 1,
      status: "ok",
      data_store_id: "store-carbon-test",
    },
  });

  const client = props.client ?? createSideChatClient(clientOverrides);
  const onSubmit = props.onSubmit ?? vi.fn(async () => undefined);
  const onStop = props.onStop ?? vi.fn(() => true);
  const onPendingModelRouteChange = props.onPendingModelRouteChange ?? vi.fn();

  const queryClient = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  });

  const view = render(
    <LocaleProvider>
      <QueryClientProvider client={queryClient}>
        <V2Composer
          busy
          client={client}
          onManageModels={vi.fn()}
          onPendingModelRouteChange={onPendingModelRouteChange}
          onStop={onStop}
          onSubmit={onSubmit}
          onWorkspaceChange={vi.fn()}
          runActive
          sideConversationContext={{
            workspaceName: "Core Engine",
            workspacePath: "/srv/core-engine",
            threadTitle: "重构分布式调度器",
            goal: "在不中断现有 Worker 的前提下完成锁粒度优化",
            runStatus: "running",
            stepSummary: "正在分析 scheduler/queue.go",
            turns: [
              { role: "user", text: "请帮我重构分布式调度器的任务队列锁。" },
              { role: "assistant", text: "正在扫描 queue.go 与 worker_pool.go 的锁竞争热点。" },
            ],
            referencedFiles: ["scheduler/queue.go"],
          }}
          threadID="thread-main-busy"
          workspaceID="ws-main"
          workspaces={workspaces}
          {...props}
        />
      </QueryClientProvider>
    </LocaleProvider>,
  );

  return { ...view, client, onSubmit, onStop, onPendingModelRouteChange };
}

describe("V2SideConversation", () => {
  it("highlights trigger while main agent is busy and toggles via button or Alt+S without interrupting main run", async () => {
    const user = userEvent.setup();
    const { onStop, onSubmit } = setupSideChat();

    const trigger = screen.getByRole("button", { name: "支线问答" });
    expect(trigger).toHaveClass("is-main-busy");
    expect(within(trigger).getByText("主任务运行中")).toBeInTheDocument();
    expect(trigger).toHaveAttribute("aria-pressed", "false");

    // Open via Alt+S shortcut
    fireEvent.keyDown(window, { key: "s", code: "KeyS", altKey: true });
    const aside = await screen.findByRole("complementary", { name: "并行支线对话" });
    expect(aside).toBeInTheDocument();
    expect(trigger).toHaveAttribute("aria-pressed", "true");
    expect(onStop).not.toHaveBeenCalled();
    expect(onSubmit).not.toHaveBeenCalled();

    // Toggle closed via Alt+S shortcut
    fireEvent.keyDown(window, { key: "s", code: "KeyS", altKey: true });
    expect(
      screen.queryByRole("complementary", { name: "并行支线对话" }),
    ).not.toBeInTheDocument();
    expect(trigger).toHaveAttribute("aria-pressed", "false");
    expect(onStop).not.toHaveBeenCalled();

    // Reopen via button click
    await user.click(trigger);
    expect(
      await screen.findByRole("complementary", { name: "并行支线对话" }),
    ).toBeInTheDocument();
  });

  it("switches side model route independently and toggles shared context snapshot scope", async () => {
    const user = userEvent.setup();
    const askSpy = vi.fn(async (req: V2SideChatAskRequest) => `Answered by ${req.modelRoute.model}`);
    const { client, onPendingModelRouteChange } = setupSideChat({
      onSideChatAsk: askSpy,
    });

    await user.click(screen.getByRole("button", { name: "支线问答" }));
    const panel = await screen.findByRole("complementary", { name: "并行支线对话" });

    // Verify shared context badge & details snapshot
    expect(
      within(panel).getByText("已共享上下文: 2 轮对话 · 不打断主任务"),
    ).toBeInTheDocument();
    expect(within(panel).getByText("/srv/core-engine")).toBeInTheDocument();
    expect(
      within(panel).getByText("在不中断现有 Worker 的前提下完成锁粒度优化"),
    ).toBeInTheDocument();

    // Wait for available model routes to populate and switch to DeepSeek R1
    const modelSelect = within(panel).getByRole("combobox", { name: "支线模型路由" });
    await waitFor(() =>
      expect(within(modelSelect).getByRole("option", { name: "DeepSeek · deepseek-r1" })).toBeInTheDocument(),
    );
    await user.selectOptions(modelSelect, "official-deepseek::deepseek-r1");

    // Main thread model route must NEVER be mutated
    expect(onPendingModelRouteChange).not.toHaveBeenCalled();
    expect(client.selectThreadModelRoute).not.toHaveBeenCalled();

    // Ask first question with Full Recent History scope
    const sideInput = within(panel).getByRole("textbox", { name: "支线问题输入" });
    await user.type(sideInput, "这里用 RWMutex 还是 sync.Map 更合适？");
    await user.click(within(panel).getByRole("button", { name: "发送支线问题" }));

    await waitFor(() => expect(askSpy).toHaveBeenCalledTimes(1));
    expect(askSpy.mock.calls[0]?.[0].modelRoute).toEqual(
      expect.objectContaining({
        provider: "official-deepseek",
        model: "deepseek-r1",
      }),
    );
    expect(askSpy.mock.calls[0]?.[0].contextScope).toBe("full");
    expect(askSpy.mock.calls[0]?.[0].prompt).toContain("正在扫描 queue.go 与 worker_pool.go");

    // Switch scope to Current Goal Only and ask a follow-up multi-turn question
    await user.click(within(panel).getByRole("button", { name: "仅当前目标" }));
    expect(
      within(panel).getByText("已共享上下文: 仅当前目标 · 不打断主任务"),
    ).toBeInTheDocument();

    await user.type(sideInput, "如果读多写少呢？{Enter}");
    await waitFor(() => expect(askSpy).toHaveBeenCalledTimes(2));
    const secondReq = askSpy.mock.calls[1]?.[0];
    expect(secondReq?.contextScope).toBe("goal_only");
    expect(secondReq?.prompt).toContain("在不中断现有 Worker 的前提下完成锁粒度优化");
    expect(secondReq?.prompt).not.toContain("正在扫描 queue.go 与 worker_pool.go");
    // Multi-turn side history is included
    expect(secondReq?.history).toHaveLength(2);
    expect(secondReq?.prompt).toContain("这里用 RWMutex 还是 sync.Map 更合适？");
  });

  it("executes isolated backend side Q&A without polluting main transcript, renders Markdown, inserts into composer, and persists across Escape/remount", async () => {
    const user = userEvent.setup();
    const { client, onSubmit, onStop, unmount } = setupSideChat();

    await user.click(screen.getByRole("button", { name: "支线问答" }));
    const panel = await screen.findByRole("complementary", { name: "并行支线对话" });

    const modelSelect = within(panel).getByRole("combobox", { name: "支线模型路由" });
    await waitFor(() =>
      expect(within(modelSelect).getByRole("option", { name: "DeepSeek · deepseek-r1" })).toBeInTheDocument(),
    );
    await user.selectOptions(modelSelect, "official-deepseek::deepseek-r1");

    const sideInput = within(panel).getByRole("textbox", { name: "支线问题输入" });
    await user.type(sideInput, "给出并发安全读取的最小示例");
    await user.click(within(panel).getByRole("button", { name: "发送支线问题" }));

    // Markdown rendered reply appears inside side panel
    expect(await within(panel).findByText(/读写锁/)).toBeInTheDocument();
    expect(within(panel).getByText(/mu\.RLock\(\)/)).toBeInTheDocument();

    // Isolated ephemeral thread used & archived; main thread untouched!
    expect(client.createThread).toHaveBeenCalledWith(
      expect.objectContaining({
        workspace_id: "ws-main",
        provider: "official-deepseek",
        model: "deepseek-r1",
      }),
      expect.stringMatching(/^v2-side-chat-create-/),
      expect.any(AbortSignal),
    );
    expect(client.submitThreadTurn).toHaveBeenCalledWith(
      "side-ephemeral-1",
      expect.objectContaining({ version: "thread_message_submission.v1" }),
      expect.stringMatching(/^v2-side-chat-turn-/),
      expect.any(AbortSignal),
    );
    expect(client.transitionThread).toHaveBeenCalledWith(
      "side-ephemeral-1",
      "archive",
      { version: "thread_lifecycle.v1", expected_version: 2 },
      expect.stringMatching(/^v2-side-chat-archive-/),
    );
    expect(onSubmit).not.toHaveBeenCalled();
    expect(onStop).not.toHaveBeenCalled();

    // Insert side answer into main composer without auto-submitting
    await user.click(within(panel).getByRole("button", { name: "复制至主输入框" }));
    const mainEditor = screen.getByRole("textbox", { name: "继续对话" });
    expect((mainEditor as HTMLTextAreaElement).value).toContain("mu.RLock()");
    expect(onSubmit).not.toHaveBeenCalled();

    // Scoped persistence under data_store_id
    const expectedStorageKey = v2SideChatStorageKey(
      "store-carbon-test",
      "thread-main-busy",
      "ws-main",
    );
    expect(localStorage.getItem(expectedStorageKey)).toContain("给出并发安全读取的最小示例");

    // Escape minimizes without losing side chat history or stopping main run
    sideInput.focus();
    await user.keyboard("{Escape}");
    expect(
      screen.queryByRole("complementary", { name: "并行支线对话" }),
    ).not.toBeInTheDocument();
    expect(onStop).not.toHaveBeenCalled();

    // Remount and verify persisted side history + Clear ("新建支线") button
    unmount();
    setupSideChat({ busy: false, runActive: false });
    const remountTrigger = screen.getByRole("button", { name: "支线问答" });
    expect(within(remountTrigger).getByText("1")).toBeInTheDocument();

    await user.click(remountTrigger);
    const reopenedPanel = await screen.findByRole("complementary", { name: "并行支线对话" });
    expect(within(reopenedPanel).getByText("给出并发安全读取的最小示例")).toBeInTheDocument();
    expect(within(reopenedPanel).getByText(/mu\.RLock\(\)/)).toBeInTheDocument();

    await user.click(within(reopenedPanel).getByRole("button", { name: "新建支线" }));
    expect(
      within(reopenedPanel).queryByText("给出并发安全读取的最小示例"),
    ).not.toBeInTheDocument();
    expect(localStorage.getItem(expectedStorageKey)).toBeNull();
  });

  it("handles offline preview fallback and surfaces retryable inline errors without affecting main composer", async () => {
    const user = userEvent.setup();
    const offlineClient = { hasThreadControl: false } as unknown as APIClient;
    const { unmount } = setupSideChat({ client: offlineClient });

    await user.click(screen.getByRole("button", { name: "支线问答" }));
    const panel = await screen.findByRole("complementary", { name: "并行支线对话" });

    const sideInput = within(panel).getByRole("textbox", { name: "支线问题输入" });
    await user.type(sideInput, "离线预览下能否查看共享上下文？");
    await user.click(within(panel).getByRole("button", { name: "发送支线问题" }));

    expect(
      await within(panel).findByText(/离线或轻量预览连接/),
    ).toBeInTheDocument();

    unmount();

    // Error handling when ask handler rejects
    setupSideChat({
      onSideChatAsk: vi.fn().mockRejectedValue(new Error("模型网关请求超时")),
    });
    await user.click(screen.getByRole("button", { name: "支线问答" }));
    const errorPanel = await screen.findByRole("complementary", { name: "并行支线对话" });
    await user.type(
      within(errorPanel).getByRole("textbox", { name: "支线问题输入" }),
      "测试网关异常",
    );
    await user.click(within(errorPanel).getByRole("button", { name: "发送支线问题" }));
    expect(await within(errorPanel).findByRole("alert")).toHaveTextContent(
      "模型网关请求超时",
    );
  });
});
