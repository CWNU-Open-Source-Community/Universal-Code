import type { ReactElement } from "react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act, cleanup, fireEvent, render as renderComponent, renderHook, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { APIClient } from "../../api/client";
import type {
  PageResult,
  RunEventStreamView,
  ThreadDetailView,
  ThreadExecutionView,
  ThreadTranscriptItemView,
  WorkspaceView,
} from "../../api/types";
import * as desktopBridge from "../../lib/desktop-bridge";
import { LocaleProvider } from "../../lib/locale";
import { resetWorkspaceTabsMemoryCache } from "../workspace-tabs";
import { useAutoBrowserDock, type BrowserOperationStep } from "../use-auto-browser-dock";
import { V2BrowserCollabBar, V2BrowserCursorHud, V2BrowserStepTrail } from "./browser-collab-bar";
import { V2Conversation } from "./conversation";
import { V2SharedBrowser } from "./shared-browser";

const rpc = vi.hoisted(() => ({ open: vi.fn(), get: vi.fn(), command: vi.fn(), surface: vi.fn() }));
const runStreamState = vi.hoisted(() => ({
  frames: [] as RunEventStreamView[],
  status: "live" as const,
}));

vi.mock("../../lib/desktop-shared-browser", async (original) => ({
  ...(await original<typeof import("../../lib/desktop-shared-browser")>()),
  openSharedBrowser: rpc.open,
  getSharedBrowser: rpc.get,
  commandSharedBrowser: rpc.command,
  setSharedBrowserSurface: rpc.surface,
}));

vi.mock("../../hooks/use-run-event-stream", () => ({
  useRunEventStream: () => ({ error: null, frames: runStreamState.frames, status: runStreamState.status }),
}));

vi.mock("../../hooks/use-public-model-stream", () => ({
  usePublicModelStream: () => ({ error: null, snapshot: null, status: "waiting" }),
}));

function render(ui: ReactElement) {
  window.localStorage.setItem("prayu.locale.v1", "zh-CN");
  return renderComponent(<LocaleProvider>{ui}</LocaleProvider>);
}

function browserTranscriptStep(
  id: string,
  toolName: string,
  title: string,
  detail: string,
  status: string,
  sequence: number,
  runID = "run-1",
  taskID = "task-1",
): ThreadTranscriptItemView {
  return {
    version: "thread_transcript.v1",
    id,
    canonical_id: id,
    run_id: runID,
    run_ordinal: 1,
    sequence,
    activity_type: "execute",
    stage: status === "completed" ? "result" : "running",
    kind: "tool_call",
    source: "harness",
    title,
    detail,
    status,
    tool_name: toolName,
    durable: true,
    provisional: false,
    verifiable: true,
    instruction_authorized: true,
    created_at: new Date(1_700_000_000_000 + sequence * 1000).toISOString(),
    task: {
      id: taskID,
      input_message_id: taskID,
      state: status === "completed" ? "completed" : "running",
    },
  };
}

function pointerEventFrame(
  sequence: number,
  payload: Record<string, unknown>,
  runID = "run-1",
  type = "browser.pointer",
): RunEventStreamView {
  return {
    version: "run-events.v1",
    request_id: `req-${sequence}`,
    cursor: `cursor-${sequence}`,
    run_id: runID,
    sequence,
    event: {
      version: "v1",
      event_id: `ev-${sequence}`,
      mission_id: "mission-1",
      run_id: runID,
      sequence,
      source: "harness",
      type,
      payload,
      created_at: new Date(1_700_000_000_000 + sequence * 1000).toISOString(),
    },
  };
}

beforeEach(() => {
  window.localStorage.setItem("prayu.locale.v1", "zh-CN");
  runStreamState.frames = [];
  resetWorkspaceTabsMemoryCache();
  const sharedInitial = {
    protocol_version: "desktop_shared_browser.v1" as const,
    workspace_id: "workspace-1",
    thread_id: "thread-collab",
    run_id: "run-1",
    session_id: "shared-sess-1",
    revision: 1,
    state: "ready" as const,
    control_owner: "agent" as const,
    fullscreen: false,
    active_tab_id: "tab-login",
    agent_tab_id: "tab-login",
    tabs: [
      {
        tab_id: "tab-login",
        title: "登录系统",
        url: "https://example.com/login",
        loading: false,
        can_go_back: false,
        can_go_forward: false,
      },
    ],
  };
  rpc.open.mockReset().mockResolvedValue(sharedInitial);
  rpc.get.mockReset().mockResolvedValue(sharedInitial);
  rpc.command.mockReset().mockImplementation(async (_sessionID: string, cmd: string) => ({
    ...sharedInitial,
    revision: 2,
    control_owner: cmd === "take_control" ? "user" : "agent",
    agent_tab_id: cmd === "take_control" ? undefined : "tab-login",
  }));
  rpc.surface.mockReset().mockImplementation(async (req: Record<string, unknown>) => ({ ...req }));
});

afterEach(() => {
  cleanup();
  resetWorkspaceTabsMemoryCache();
  vi.restoreAllMocks();
  vi.unstubAllGlobals();
});

describe("useAutoBrowserDock", () => {
  it("auto-opens side-by-side on browser_*/form-fill tool activity and ignores non-browser tools", () => {
    const onAutoOpenBrowser = vi.fn();
    const readFileItem: ThreadTranscriptItemView = {
      ...browserTranscriptStep("read-1", "read_file", "读取文件", "src/app.tsx", "running", 1),
      activity_type: "read",
    };

    const { rerender, result } = renderHook(
      (props: { items: ThreadTranscriptItemView[]; frames: RunEventStreamView[] }) =>
        useAutoBrowserDock({
          threadID: "thread-1",
          runID: "run-1",
          runActive: true,
          isBrowserOpen: false,
          transcriptItems: props.items,
          eventFrames: props.frames,
          onAutoOpenBrowser,
        }),
      {
        initialProps: { items: [readFileItem], frames: [] },
        wrapper: LocaleProvider,
      },
    );

    expect(result.current.hasBrowserActivity).toBe(false);
    expect(onAutoOpenBrowser).not.toHaveBeenCalled();

    const navItem = browserTranscriptStep(
      "step-nav",
      "browser_navigate",
      "打开登录页",
      '{"url":"https://example.com/login"}',
      "completed",
      2,
    );
    const fillItem = browserTranscriptStep(
      "step-fill",
      "browser_type",
      "browser_type",
      '{"label":"邮箱表单","value":"alice@example.com","x":480,"y":260}',
      "running",
      3,
    );

    rerender({ items: [readFileItem, navItem, fillItem], frames: [] });

    expect(result.current.hasBrowserActivity).toBe(true);
    expect(result.current.agentOperating).toBe(true);
    expect(onAutoOpenBrowser).toHaveBeenCalledTimes(1);
    expect(result.current.steps.map((s) => ({ index: s.index, label: s.label, status: s.status }))).toEqual([
      { index: 1, label: "打开登录页", status: "completed" },
      { index: 2, label: "填写邮箱表单", status: "running" },
    ]);
    expect(result.current.cursor).toMatchObject({
      x: 480,
      y: 260,
      actionKind: "type",
      ripple: true,
    });
  });

  it("respects manual close override during the current run/task and re-enables on a new browser task", () => {
    const onAutoOpenBrowser = vi.fn();
    const step1 = browserTranscriptStep(
      "call-1",
      "browser_navigate",
      "打开登录页",
      "https://example.com/login",
      "completed",
      1,
      "run-1",
      "task-1",
    );

    const { rerender, result } = renderHook(
      (props: { runID: string; items: ThreadTranscriptItemView[] }) =>
        useAutoBrowserDock({
          threadID: "thread-override",
          runID: props.runID,
          runActive: true,
          isBrowserOpen: false,
          transcriptItems: props.items,
          onAutoOpenBrowser,
        }),
      {
        initialProps: { runID: "run-1", items: [step1] },
        wrapper: LocaleProvider,
      },
    );

    expect(onAutoOpenBrowser).toHaveBeenCalledTimes(1);
    expect(result.current.manualCloseOverride).toBe(false);

    // User manually closes the browser dock during run-1 / task-1
    act(() => {
      result.current.notifyManualClose();
    });
    expect(result.current.manualCloseOverride).toBe(true);

    // Subsequent browser actions in the SAME run & task must NOT fight the user
    const step2 = browserTranscriptStep(
      "call-2",
      "browser_click",
      "点击提交",
      '{"x":640,"y":410}',
      "running",
      2,
      "run-1",
      "task-1",
    );
    rerender({ runID: "run-1", items: [step1, step2] });
    expect(onAutoOpenBrowser).toHaveBeenCalledTimes(1);
    expect(result.current.manualCloseOverride).toBe(true);

    // A new browser task (task-2 or run-2) re-enables auto-opening
    const nextTaskStep = browserTranscriptStep(
      "call-3",
      "browser_navigate",
      "打开结账页",
      "https://example.com/checkout",
      "running",
      3,
      "run-2",
      "task-2",
    );
    rerender({ runID: "run-2", items: [nextTaskStep] });
    expect(result.current.manualCloseOverride).toBe(false);
    expect(onAutoOpenBrowser).toHaveBeenCalledTimes(2);
  });

  it("tracks real-time pointer coordinates from run event frames without flooding the step trail", () => {
    const moveFrame1 = pointerEventFrame(1, {
      action: "browser_move",
      point: { x: 210, y: 180, viewport: { width: 1000, height: 800 } },
    });
    const moveFrame2 = pointerEventFrame(2, {
      action: "browser_move",
      point: { x: 500, y: 400, viewport: { width: 1000, height: 800 } },
    });
    const clickFrame = pointerEventFrame(3, {
      call_id: "click-submit",
      tool_name: "browser_click",
      label: "提交按钮",
      status: "running",
      point: { x: 500, y: 400, viewport: { width: 1000, height: 800 } },
    });

    const { result } = renderHook(
      () =>
        useAutoBrowserDock({
          threadID: "thread-pointer",
          runID: "run-1",
          runActive: true,
          eventFrames: [moveFrame1, moveFrame2, clickFrame],
          onAutoOpenBrowser: vi.fn(),
        }),
      { wrapper: LocaleProvider },
    );

    expect(result.current.steps).toHaveLength(1);
    expect(result.current.steps[0]).toMatchObject({
      index: 1,
      label: "点击提交按钮",
      status: "running",
    });
    expect(result.current.cursor).toMatchObject({
      x: 500,
      y: 400,
      percentX: 50,
      percentY: 50,
      actionKind: "click",
      ripple: true,
    });
  });
});

describe("Visual Cursor HUD, Operation Step Trail & Bottom Takeover Bar", () => {
  const sampleSteps: BrowserOperationStep[] = [
    { id: "s1", index: 1, actionKind: "navigate", toolName: "browser_navigate", label: "打开登录页", status: "completed" },
    { id: "s2", index: 2, actionKind: "type", toolName: "browser_type", label: "填写邮箱表单", status: "completed" },
    { id: "s3", index: 3, actionKind: "click", toolName: "browser_click", label: "点击提交", status: "running" },
  ];

  it("renders live cursor coordinates, click ripple, and ordered operation steps", () => {
    render(
      <div>
        <V2BrowserStepTrail steps={sampleSteps} />
        <div style={{ position: "relative", width: 600, height: 400 }}>
          <V2BrowserCursorHud
            cursor={{
              x: 384,
              y: 256,
              percentX: 48,
              percentY: 42,
              viewportWidth: 800,
              viewportHeight: 600,
              actionKind: "click",
              targetLabel: "提交",
              ripple: true,
              sequenceKey: "seq-click-1",
            }}
          />
        </div>
      </div>,
    );

    expect(screen.getByText("步骤 1: 打开登录页")).toBeInTheDocument();
    expect(screen.getByText("步骤 2: 填写邮箱表单")).toBeInTheDocument();
    expect(screen.getByText("步骤 3: 点击提交")).toBeInTheDocument();

    const items = screen.getAllByRole("listitem");
    expect(items[0]).toHaveAttribute("data-step-status", "completed");
    expect(items[1]).toHaveAttribute("data-step-status", "completed");
    expect(items[2]).toHaveAttribute("data-step-status", "running");

    const hud = screen.getByTestId("v2-browser-cursor-hud");
    expect(hud).toHaveTextContent("点击 · 提交");
    expect(hud).toHaveTextContent("(384, 256)");
    expect(screen.getByTestId("v2-browser-cursor-ripple")).toBeInTheDocument();
  });

  it("cancels the active run and enters Manual Takeover when clicking Stop & Take Over, then resumes", async () => {
    const user = userEvent.setup();
    const onCancelRun = vi.fn().mockResolvedValue(undefined);
    const onResumeAgent = vi.fn();

    function CollabHarness() {
      const collab = useAutoBrowserDock({
        threadID: "thread-takeover",
        runID: "run-takeover",
        runActive: true,
        transcriptItems: [
          browserTranscriptStep("s1", "browser_navigate", "打开登录页", "https://example.com/login", "completed", 1, "run-takeover"),
          browserTranscriptStep("s2", "browser_type", "填写邮箱表单", '{"x":410,"y":290}', "running", 2, "run-takeover"),
        ],
        onAutoOpenBrowser: vi.fn(),
        onCancelRun,
        onResumeAgent,
      });

      return (
        <div>
          <V2BrowserCursorHud cursor={collab.cursor} manualTakeover={collab.manualTakeover} />
          <V2BrowserCollabBar
            agentOperating={collab.agentOperating}
            manualTakeover={collab.manualTakeover}
            steps={collab.steps}
            onStopAndTakeOver={collab.triggerStopAndTakeOver}
            onResumeAgent={collab.resumeWithAgent}
          />
        </div>
      );
    }

    render(<CollabHarness />);

    expect(screen.getByRole("status")).toHaveTextContent("智能体正在操作网页 · 步骤 2: 填写邮箱表单");
    expect(screen.getByTestId("v2-browser-cursor-hud")).toBeInTheDocument();

    await user.click(screen.getByRole("button", { name: "停止并人工接管" }));

    expect(onCancelRun).toHaveBeenCalledOnce();
    expect(screen.getByRole("status")).toHaveTextContent("已由人工接管 · 您可直接操作页面");
    expect(screen.queryByTestId("v2-browser-cursor-hud")).not.toBeInTheDocument();

    const resumeBtn = screen.getByRole("button", { name: "交还智能体继续" });
    await user.click(resumeBtn);
    expect(onResumeAgent).toHaveBeenCalledOnce();
    expect(screen.getByRole("button", { name: "停止并人工接管" })).toBeInTheDocument();
  });

  it("releases agent ownership on V2SharedBrowser when Stop & Take Over is clicked", async () => {
    vi.stubGlobal("ResizeObserver", class { observe() {} disconnect() {} unobserve() {} });
    const rect = { x: 400, y: 150, left: 400, top: 150, right: 900, bottom: 750, width: 500, height: 600, toJSON() {} };
    vi.spyOn(HTMLElement.prototype, "getBoundingClientRect").mockReturnValue(rect);
    vi.spyOn(HTMLElement.prototype, "getClientRects").mockReturnValue([rect] as unknown as DOMRectList);

    const onCancelRun = vi.fn().mockResolvedValue(undefined);
    function SharedHarness() {
      const collab = useAutoBrowserDock({
        threadID: "thread-collab",
        runID: "run-1",
        runActive: true,
        transcriptItems: [
          browserTranscriptStep("s1", "browser_navigate", "打开登录页", "https://example.com/login", "completed", 1),
          browserTranscriptStep("s2", "browser_click", "点击提交", '{"x":512,"y":320}', "running", 2),
        ],
        onAutoOpenBrowser: vi.fn(),
        onCancelRun,
      });
      return <V2SharedBrowser workspaceID="workspace-1" threadID="thread-collab" runID="run-1" collaboration={collab} />;
    }

    render(<SharedHarness />);
    expect(await screen.findByRole("tab", { name: "登录系统" })).toBeInTheDocument();
    expect(screen.getByText("步骤 1: 打开登录页")).toBeInTheDocument();
    expect(screen.getByText("步骤 2: 点击提交")).toBeInTheDocument();
    expect(screen.getByTestId("v2-browser-cursor-hud")).toHaveTextContent("(512, 320)");

    fireEvent.click(screen.getByRole("button", { name: "停止并人工接管" }));
    await waitFor(() => expect(onCancelRun).toHaveBeenCalledOnce());
    expect(rpc.command).toHaveBeenCalledWith("shared-sess-1", "take_control", {});
    expect(await screen.findByText("已由人工接管 · 您可直接操作页面")).toBeInTheDocument();
  });

  it("auto-opens the browser dock side-by-side inside V2Conversation when browser activity starts and interrupts run on takeover", async () => {
    vi.spyOn(desktopBridge, "desktopSharedBrowserEnabled").mockReturnValue(true);
    vi.spyOn(HTMLElement.prototype, "clientWidth", "get").mockImplementation(function (this: HTMLElement) {
      return this.classList.contains("v2-shell") ? 1280 : 980;
    });
    vi.stubGlobal("ResizeObserver", class { observe() {} disconnect() {} unobserve() {} });
    const rect = { x: 520, y: 120, left: 520, top: 120, right: 1120, bottom: 720, width: 600, height: 600, toJSON() {} };
    vi.spyOn(HTMLElement.prototype, "getBoundingClientRect").mockReturnValue(rect);
    vi.spyOn(HTMLElement.prototype, "getClientRects").mockReturnValue([rect] as unknown as DOMRectList);

    runStreamState.frames = [
      pointerEventFrame(1, {
        call_id: "c1",
        tool_name: "browser_navigate",
        title: "打开登录页",
        url: "https://example.com/login",
        status: "completed",
      }),
      pointerEventFrame(2, {
        call_id: "c2",
        tool_name: "browser_type",
        label: "邮箱表单",
        status: "running",
        point: { x: 460, y: 310, viewport: { width: 1000, height: 700 } },
      }),
    ];

    const interruptThread = vi.fn().mockResolvedValue({
      version: "thread_execution.v1",
      thread_id: "thread-collab",
      state: "stopping",
      queued_messages: 0,
      capability_grant: false,
    } satisfies ThreadExecutionView);

    const detail: ThreadDetailView = {
      thread: {
        id: "thread-collab",
        title: "表单填写测试",
        status: "active",
        workspace_id: "workspace-1",
        composer_state: "ready",
      },
      active_run: { id: "run-1", status: "running" },
      last_run: { id: "run-1", status: "running" },
      runs: [],
      mission: {},
    } as unknown as ThreadDetailView;

    const client = {
      baseURL: "/api/v1",
      hasControl: true,
      hasThreadControl: true,
      hasRunExecution: true,
      hasThreadExecutionRead: true,
      get: vi.fn().mockResolvedValue(detail),
      getPage: vi.fn().mockResolvedValue({
        items: [],
        page: { limit: 100 },
        requestID: "req-1",
      } satisfies PageResult<ThreadTranscriptItemView>),
      threadExecution: vi.fn().mockResolvedValue({
        version: "thread_execution.v1",
        thread_id: "thread-collab",
        execution_id: "exec-run-1",
        state: "running",
        queued_messages: 0,
        capability_grant: false,
      } satisfies ThreadExecutionView),
      interruptThread,
      availableModelRoutes: vi.fn().mockResolvedValue({ items: [] }),
    } as unknown as APIClient;

    const queryClient = new QueryClient({
      defaultOptions: { queries: { retry: false }, mutations: { retry: false } },
    });

    const view = render(
      <div className="v2-shell has-sidebar">
        <QueryClientProvider client={queryClient}>
          <V2Conversation
            client={client}
            threadID="thread-collab"
            workspaces={[{ id: "workspace-1", name: "Workspace 1" } as WorkspaceView]}
            onArchive={vi.fn()}
            onManageModels={vi.fn()}
          />
        </QueryClientProvider>
      </div>,
    );

    const dock = await screen.findByRole("complementary", { name: "浏览器" });
    expect(view.container.querySelector(".v2-workbench-slot")).toHaveClass("is-open");
    expect(within(dock).getByText("步骤 1: 打开登录页")).toBeInTheDocument();
    expect(within(dock).getByText("步骤 2: 填写邮箱表单")).toBeInTheDocument();
    expect(within(dock).getByTestId("v2-browser-cursor-hud")).toHaveTextContent("(460, 310)");

    await userEvent.setup().click(within(dock).getByRole("button", { name: "停止并人工接管" }));
    await waitFor(() => expect(interruptThread).toHaveBeenCalledWith(
      "thread-collab",
      "exec-run-1",
      expect.stringContaining("exec-run-1"),
    ));
    expect(within(dock).getByText("已由人工接管 · 您可直接操作页面")).toBeInTheDocument();
  });
});
