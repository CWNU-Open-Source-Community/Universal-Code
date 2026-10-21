import type { ReactElement } from "react";
import type { RenderOptions } from "@testing-library/react";
import { LocaleProvider, useLocale } from "../../lib/locale";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act, cleanup, fireEvent, render as renderComponent, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import type { ReactNode } from "react";
import { APIRequestError, type APIClient } from "../../api/client";
import type { V2FileReference } from "./file-context";
import type { PageResult, ThreadDetailView, ThreadExecutionView, ThreadTranscriptItemView, WorkspaceView } from "../../api/types";
import type { ThreadReview } from "../../api/task-delivery";
import { v2QueryKeys } from "../query-keys";
import { V2RecoveryProvider } from "../recovery-storage";
import * as desktopBridge from "../../lib/desktop-bridge";
import { V2Conversation, V2FileDrawer, V2TerminalDrawer, parseProjectFileLink } from "./conversation";

const composerFiles = vi.hoisted(() => ({ current: undefined as V2FileReference[] | undefined }));
const composerStop = vi.hoisted(() => ({ current: undefined as (() => boolean) | undefined }));
const streams = vi.hoisted(() => ({ events: "live", model: "waiting" }));

vi.mock("../../hooks/use-run-event-stream", () => ({
  useRunEventStream: () => ({ error: null, frames: [], status: streams.events }),
}));

vi.mock("../../hooks/use-public-model-stream", () => ({
  usePublicModelStream: () => ({ error: null, snapshot: null, status: streams.model }),
}));

vi.mock("../projection/narrative", () => ({
  projectThreadNarrative: () => [],
  prepareThreadNarrative: (items: ThreadTranscriptItemView[]) => ({ entries: items.map((item) => ({
    id: item.id,
    kind: item.source === "operator" ? "user" : "assistant",
    text: item.detail ?? item.title,
    createdAt: item.created_at,
    status: item.status,
    deliveryMode: item.delivery_mode,
    promotedToMessageID: item.promoted_to_message_id,
    promotedFromMessageID: item.promoted_from_message_id,
    runId: item.run_id,
    branchSourceID: item.durable && !item.provisional ? item.id : undefined,
  })) }),
  projectLiveThreadNarrative: (prepared: { entries: unknown[] }) => prepared.entries,
}));

vi.mock("./composer", () => ({
  V2Composer: ({ disabled, onSubmit, onStop, threadID, fileReferenceUnavailableReason }: {
    disabled: boolean;
    onSubmit: (content: string, files?: V2FileReference[]) => Promise<void>;
    onStop?: () => boolean;
    threadID: string;
    fileReferenceUnavailableReason?: string;
  }) => { composerStop.current = onStop; return <button data-file-reference-unavailable={fileReferenceUnavailableReason}
    disabled={disabled} onClick={() => void onSubmit(`pending-${threadID}`, composerFiles.current).catch(() => undefined)} type="button">
    发送 {threadID}
  </button>; },
}));

vi.mock("../../components/user-terminal-panel", () => ({
  UserTerminalPanel: ({ runID, sessionID, onSession }: {
    runID: string;
    sessionID: string;
    onSession: (sessionID: string) => void;
  }) => <div data-testid="user-terminal-panel" data-run-id={runID} data-session-id={sessionID}>
    <button onClick={() => onSession("sess-created")} type="button">Mock Start Session</button>
  </div>,
}));

afterEach(() => { cleanup(); composerFiles.current = undefined; composerStop.current = undefined; streams.events = "live"; streams.model = "waiting"; });

const workspaces = [{ id: "workspace-1", name: "Workspace 1" }] as WorkspaceView[];

function detail(threadID: string): ThreadDetailView {
  return {
    thread: {
      id: threadID,
      title: `Title ${threadID}`,
      status: "active",
      workspace_id: "workspace-1",
      composer_state: "ready",
    },
    last_run: { id: `run-${threadID}`, status: "completed" },
    runs: [],
    mission: {},
  } as unknown as ThreadDetailView;
}

function transcriptItem(id: string, detailText: string, sequence: number): ThreadTranscriptItemView {
  return {
    activity_type: "message",
    canonical_id: `canonical-${id}`,
    created_at: new Date(sequence * 1_000).toISOString(),
    detail: detailText,
    durable: true,
    id,
    instruction_authorized: false,
    kind: "model_update",
    provisional: false,
    run_id: "run-thread-a",
    run_ordinal: 1,
    sequence,
    source: "model",
    stage: "result",
    title: detailText,
    verifiable: true,
    version: "thread_transcript.v1",
  };
}

function page(items: ThreadTranscriptItemView[], nextCursor = ""): PageResult<ThreadTranscriptItemView> {
  return {
    items,
    page: { limit: 100, ...(nextCursor ? { next_cursor: nextCursor } : {}) },
    requestID: "request-1",
  };
}

function deferred<T>() {
  let resolve!: (value: T) => void;
  let reject!: (reason?: unknown) => void;
  const promise = new Promise<T>((resolvePromise, rejectPromise) => {
    resolve = resolvePromise;
    reject = rejectPromise;
  });
  return { promise, reject, resolve };
}

function fileDrawerReview(): ThreadReview {
  return {
    thread_id: "thread-a", thread_version: 2, current_run_id: "run-current", total_runs: 2,
    observed_at: "2026-10-07T01:00:00Z", change_scope: "recorded_file_edits", partial: false, reasons: [],
    target: { state: "available", kind: "drydock", source_workspace_id: "source-workspace", workspace_id: "current-workspace" },
    revision: { state: "available", repository_kind: "none", reasons: [] },
    runs: [
      { run_id: "run-current", session_id: "session-current", ordinal: 2, source_event_sequence: 2,
        workspace_id: "current-workspace", handoff_url: "/api/v1/runs/run-current/code-handoff" },
      { run_id: "run-old", session_id: "session-old", ordinal: 1, source_event_sequence: 1,
        handoff_url: "/api/v1/runs/run-old/code-handoff" },
    ],
    applied_changes: [], unapplied_changes: [], checks: [],
  };
}

function renderFileDrawer(client: APIClient, runID = "run-old") {
  const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return render(<QueryClientProvider client={queryClient}>
    <V2FileDrawer client={client} threadID="thread-a" workspaceID="source-workspace" runID={runID}
      initialPath="src/index.ts" onClose={vi.fn()} returnFocusRef={{ current: document.createElement("button") }} />
  </QueryClientProvider>);
}

function renderConversation(client: APIClient, initialThreadID = "thread-a", extras?: ReactNode,
  draftProps?: { draft?: string; onDraftChange?: (content: string, expected?: string) => void;
    messageID?: string; onExitMessage?: () => void;
    onBranchCreated?: (thread: ThreadDetailView["thread"]) => void }) {
  const queryClient = new QueryClient({ defaultOptions: {
    queries: { retry: false, staleTime: Number.POSITIVE_INFINITY },
  } });
  const props = (threadID: string) => <QueryClientProvider client={queryClient}>
    {extras}
    <V2Conversation client={client} onArchive={vi.fn()} onManageModels={vi.fn()}
      onOpenInspector={vi.fn()} threadID={threadID} workspaces={workspaces} {...draftProps} />
  </QueryClientProvider>;
  const view = render(props(initialThreadID));
  return { ...view, queryClient,
    rerenderThread: (threadID: string) => view.rerender(props(threadID)) };
}

function baseClient(overrides: Partial<APIClient> = {}): APIClient {
  return {
    get: vi.fn((path: string) => {
      if (path.endsWith("/agent-browser")) return Promise.reject(new APIRequestError("Route unavailable", "NOT_FOUND", 404));
      const match = path.match(/^\/threads\/([^/]+)$/u);
      return Promise.resolve(detail(decodeURIComponent(match?.[1] ?? "missing")));
    }),
    getPage: vi.fn(() => Promise.resolve(page([]))),
    approvalQueue: vi.fn(() => Promise.resolve({
      protocol_version: "approval_queue.v1", run_id: "run-thread-a", items: [], truncated: false,
      process_execution_enabled: false, session_grant_created: false, capability_grant: false,
    })),
    workspaceExplore: vi.fn(() => Promise.resolve({
      protocol_version: "workspace_explorer.v1",
      workspace_id: "workspace-1",
      path: ".",
      kind: "directory",
      entries: [
        { name: "src", path: "src", kind: "directory" },
        { name: "index.ts", path: "src/index.ts", kind: "file", size_bytes: 120, readable: true },
      ],
      content: "",
      total_bytes: 0,
      returned_bytes: 0,
      truncated: false,
      redaction_count: 0,
      root_path_exposed: false,
      provenance: {
        version: "context_provenance.v1",
        source_kind: "workspace_listing",
        source_ref: ".",
        content_sha256: "a".repeat(64),
        instruction_authorized: false,
      },
    })),
    hasThreadControl: true,
    submitThreadTurn: vi.fn(() => Promise.resolve({ steering: { id: "steering-1" } })),
    ...overrides,
  } as unknown as APIClient;
}

function queuedTranscript(id: string, sourceRef: string, patch: Partial<ThreadTranscriptItemView> = {}) {
  return { ...transcriptItem(id, "同样的排队要求", 1), source: "operator" as const, kind: "operator_input" as const,
    source_ref: sourceRef, status: "pending", ...patch };
}
function queueSnapshot(runID = "run-thread-a", sessionID = "sess-thread-a") {
  return { version: "thread_queued_messages.v1", thread_id: "thread-a", run_id: runID, session_id: sessionID,
    pending: 1, prepared: 0, capability_grant: false, items: [{ id: "queued-one", sequence: 1, status: "pending",
      prepared: false, content: "同样的排队要求", content_sha256: "a".repeat(64), content_redacted: false,
      revision: 0, created_at: "2026-09-29T01:00:00Z", images: [], attachments: [], can_edit: false, can_cancel: false }] };
}

describe("V2Conversation", () => {
  it("opens export from the conversation menu with read access and closes it when changing chats", async () => {
    const ui = renderConversation(baseClient({ hasThreadControl: false }));
    const trigger = await screen.findByRole("button", { name: "对话操作" });
    fireEvent.click(trigger);
    fireEvent.click(screen.getByRole("menuitem", { name: "导出对话" }));
    expect(await screen.findByRole("dialog", { name: "导出对话" })).toBeVisible();
    ui.rerenderThread("thread-b");
    await waitFor(() => expect(screen.queryByRole("dialog", { name: "导出对话" })).not.toBeInTheDocument());
    ui.rerenderThread("thread-a");
    await screen.findByText("Title thread-a");
    expect(screen.queryByRole("dialog", { name: "导出对话" })).not.toBeInTheDocument();
  });

  it("loads and focuses the complete saved message window in an archived chat without submitting or restoring it", async () => {
    const content = "An old full answer beyond the transcript preview.";
    const getPage = vi.fn((_path: string, _query: unknown, cursor = "") => Promise.resolve(cursor
      ? page([transcriptItem("old-message", "truncated preview", 1)])
      : page([transcriptItem("latest-message", "Latest unrelated answer", 120)])));
    const exit = vi.fn();
    const client = baseClient({ getPage,
      get: vi.fn((path: string) => Promise.resolve(path.endsWith("/messages/old-message")
        ? { message_id: "old-message", content, transcript_cursor: "target-window" }
        : { ...detail("thread-a"), last_run: { ...detail("thread-a").last_run, session_id: "session-archived" },
          thread: { ...detail("thread-a").thread, status: "archived", composer_state: "archived" } })),
      transitionThread: vi.fn(), executeRun: vi.fn(),
    } as unknown as Partial<APIClient>);
    const ui = renderConversation(client, "thread-a", undefined, { messageID: "old-message", onExitMessage: exit });
    const message = await screen.findByText(content);
    const row = message.closest("[data-message-ids]")!;
    expect(row).toHaveClass("is-search-target");
    expect(row).toHaveFocus();
    expect(screen.queryByText("Latest unrelated answer")).not.toBeInTheDocument();
    expect(screen.queryByText("truncated preview")).not.toBeInTheDocument();
    expect(getPage).toHaveBeenCalledWith("/threads/thread-a/transcript", { limit: 100 }, "target-window", expect.any(AbortSignal));
    expect(screen.getByText(/此对话已归档，仍可查看记录/)).toBeInTheDocument();
    expect(vi.mocked(client.get).mock.calls.some(([path]) => path.endsWith("/queued-messages"))).toBe(false);
    expect(screen.queryByText(/暂时无法读取完整队列/)).not.toBeInTheDocument();
    for (const method of [client.submitThreadTurn, client.transitionThread, client.executeRun]) expect(method).not.toHaveBeenCalled();
    const returnButton = screen.getByRole("button", { name: "返回最新消息" });
    returnButton.focus();
    const scroller = ui.container.querySelector<HTMLDivElement>(".v2-conversation-scroll")!;
    Object.defineProperties(scroller, { scrollHeight: { value: 2000, configurable: true }, clientHeight: { value: 500, configurable: true } });
    scroller.scrollTop = 77;
    fireEvent.scroll(scroller);
    act(() => ui.queryClient.setQueryData([...v2QueryKeys.transcript("thread-a"), "message", "old-message"], {
      pages: [page([transcriptItem("earlier-message", "Earlier context added", 0), transcriptItem("old-message", "truncated preview", 1)])],
      pageParams: ["target-window"],
    }));
    await screen.findByText("Earlier context added");
    expect(returnButton).toHaveFocus();
    expect(scroller.scrollTop).toBe(77);
    expect(row).toHaveClass("is-search-target");
    await userEvent.click(screen.getByRole("button", { name: "返回最新消息" }));
    expect(ui.queryClient.getQueryData(["v2", "reading", "thread-a"])).toEqual({ top: 0, following: true });
    expect(exit).toHaveBeenCalledOnce();
    ui.queryClient.clear();
  });
  it.each(["accepted", "rejected", "switched"])("returns from a searched message only after an accepted send in the same view (%s)", async (outcome) => {
    const response = deferred<{ steering: { id: string } }>();
    const submitThreadTurn = vi.fn(() => response.promise);
    const exit = vi.fn();
    const client = baseClient({ submitThreadTurn,
      get: vi.fn((path: string) => Promise.resolve(path.endsWith("/messages/old-message")
        ? { message_id: "old-message", content: "Saved message", transcript_cursor: "target-window" }
        : detail(path.includes("thread-b") ? "thread-b" : "thread-a"))),
      getPage: vi.fn(() => Promise.resolve(page([transcriptItem("old-message", "Saved message", 1)]))),
    } as unknown as Partial<APIClient>);
    const ui = renderConversation(client, "thread-a", undefined, { messageID: "old-message", onExitMessage: exit });
    await screen.findByText("Saved message");
    await userEvent.click(screen.getByRole("button", { name: "发送 thread-a" }));
    expect(exit).not.toHaveBeenCalled();
    if (outcome === "switched") {
      ui.rerenderThread("thread-b");
      await screen.findByText("Title thread-b");
    }
    await act(async () => {
      if (outcome === "rejected") response.reject(new APIRequestError("Rejected", "CONFLICT", 409, "confirmed", false));
      else response.resolve({ steering: { id: "new-message" } });
    });
    if (outcome === "accepted") {
      await waitFor(() => expect(exit).toHaveBeenCalledOnce());
      expect(ui.queryClient.getQueryData(["v2", "reading", "thread-a"])).toEqual({ top: 0, following: true });
    } else expect(exit).not.toHaveBeenCalled();
    ui.queryClient.clear();
  });
  it("keeps one composer when switching between cached source and branch chats", async () => {
    const statistics = { turns: 1, steps: 2, input_tokens: 48, output_tokens: 16, total_tokens: 64,
      cache_read_tokens: 0, cache_reported_calls: 0, usage_calls: 2, timed_output_tokens: 0, decode_millis: 0 };
    const view = renderConversation(baseClient());
    await screen.findByRole("button", { name: "发送 thread-a" });
    act(() => {
      view.queryClient.setQueryData(v2QueryKeys.thread("thread-a"), { ...detail("thread-a"), statistics });
      view.queryClient.setQueryData(v2QueryKeys.thread("thread-branch"), { ...detail("thread-branch"), statistics });
    });
    view.rerenderThread("thread-branch");
    await screen.findByRole("button", { name: "发送 thread-branch" });
    expect(screen.queryByRole("button", { name: "发送 thread-a" })).not.toBeInTheDocument();
    view.rerenderThread("thread-a");
    expect(screen.getAllByRole("button", { name: /^发送 thread-/u })).toHaveLength(1);
    expect(screen.getByRole("button", { name: "发送 thread-a" })).toBeInTheDocument();
  });

  it("synchronizes durable records after reconnect, model finalization and the Run becoming terminal", async () => {
    const active = { ...detail("thread-a"), active_run: { id: "run-thread-a", status: "running" } };
    let items = [transcriptItem("first", "First", 1)];
    const getPage = vi.fn(() => Promise.resolve(page(items)));
    const client = baseClient({ get: vi.fn(() => Promise.resolve(active)), getPage } as Partial<APIClient>);
    const ui = renderConversation(client);
    await screen.findByText("First");
    streams.events = "reconnecting";
    ui.rerenderThread("thread-a");
    items = [...items, transcriptItem("reconnected", "Recovered after disconnect", 2)];
    streams.events = "live";
    ui.rerenderThread("thread-a");
    await screen.findByText("Recovered after disconnect");
    streams.model = "finalizing";
    ui.rerenderThread("thread-a");
    streams.model = "reconnecting";
    ui.rerenderThread("thread-a");
    items = [...items, transcriptItem("settled", "Final commentary persisted", 3)];
    streams.model = "waiting";
    ui.rerenderThread("thread-a");
    await screen.findByText("Final commentary persisted");
    items = [...items, transcriptItem("done", "Terminal work persisted", 4)];
    await act(async () => { ui.queryClient.setQueryData(v2QueryKeys.thread("thread-a"), detail("thread-a")); });
    await screen.findByText("Terminal work persisted");
    expect(getPage).toHaveBeenCalledTimes(4);
  });

  it.each([false, true])("holds the reading anchor until older history arrives during live output (retry: %s)", async (retry) => {
    const older = deferred<PageResult<ThreadTranscriptItemView>>();
    const middle = transcriptItem("middle-anchor", "Middle anchor", 2);
    let failed = false;
    const getPage = vi.fn((_path: string, _query: unknown, cursor: string) => {
      if (cursor === "first-history") return Promise.resolve(page([middle], "older-anchor"));
      if (cursor === "older-anchor") {
        if (retry && !failed) { failed = true; return Promise.reject(new Error("Older page unavailable")); }
        return older.promise;
      }
      return Promise.resolve(retry ? page([transcriptItem("latest-anchor", "Latest anchor", 3)], "first-history")
        : page([middle], "older-anchor"));
    });
    const ui = renderConversation(baseClient({ getPage } as Partial<APIClient>));
    if (retry) {
      await userEvent.setup().click(await screen.findByRole("button", { name: "加载更早记录" }));
      await screen.findByText("Middle anchor");
      await userEvent.setup().click(screen.getByRole("button", { name: "加载更早记录" }));
      await screen.findByText("更早的工作记录加载失败，请重试。");
    }
    await screen.findByText("Middle anchor");
    const scroller = ui.container.querySelector<HTMLDivElement>(".v2-conversation-scroll")!;
    Object.defineProperties(scroller, { scrollHeight: { value: 2_000, configurable: true },
      clientHeight: { value: 500, configurable: true } });
    scroller.scrollTop = 180;
    fireEvent.scroll(scroller);
    const row = ui.container.querySelector<HTMLElement>(".v2-narrative > li")!;
    let rowTop = 20;
    vi.spyOn(row, "getBoundingClientRect").mockImplementation(() =>
      ({ top: rowTop, bottom: rowTop + 50, height: 50 }) as DOMRect);
    await userEvent.setup().click(screen.getByRole("button", { name: "加载更早记录" }));
    await act(async () => { ui.queryClient.setQueryData(v2QueryKeys.transcript("thread-a"), {
      pages: [page([middle, transcriptItem("live-tail", "New output", 4)], retry ? "first-history" : "older-anchor")], pageParams: [""],
    }); });
    await screen.findByText("New output");
    expect(scroller.scrollTop).toBe(180);
    // The older page adds 400px above the reader; output below is irrelevant.
    rowTop += 400;
    Object.defineProperty(scroller, "scrollHeight", { value: 2_700, configurable: true });
    await act(async () => { older.resolve(page([transcriptItem("old", "Older message", 1)])); await older.promise; });
    await screen.findByText("Older message");
    expect(scroller.scrollTop).toBe(580);
    ui.rerenderThread("thread-b");
    await screen.findByText("Title thread-b");
    ui.rerenderThread("thread-a");
    await screen.findByText("Title thread-a");
    expect(scroller.scrollTop).toBe(580);
  });

  it("enables queue promotion only when the observed running execution matches the validated queue owner", async () => {
    let executionID = "execution-other";
    const execution = () => ({ version: "thread_execution.v1", thread_id: "thread-a", run_id: "run-thread-a",
      state: "running", execution_id: executionID, queued_messages: 1, capability_grant: false });
    const client = baseClient({ baseURL: "/api/v1", hasThreadExecutionRead: true, hasSessionSteeringControl: true,
      threadExecution: vi.fn(() => Promise.resolve(execution())),
      get: vi.fn((path: string) => Promise.resolve(path.endsWith("/queued-messages")
        ? { ...queueSnapshot(), current_attempt_id: "attempt-a", execution_id: "execution-a",
          items: queueSnapshot().items.map((item) => ({ ...item, can_edit: true, can_cancel: true })) }
        : { ...detail("thread-a"), active_run: { id: "run-thread-a", session_id: "sess-thread-a", status: "running" } })),
    } as unknown as Partial<APIClient>);
    const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false, staleTime: Number.POSITIVE_INFINITY } } });
    render(<V2RecoveryProvider client={client} scopeID="conversation-queue-owner-test"><QueryClientProvider client={queryClient}>
      <V2Conversation client={client} onArchive={vi.fn()} onManageModels={vi.fn()} onOpenInspector={vi.fn()}
        threadID="thread-a" workspaces={workspaces} />
    </QueryClientProvider></V2RecoveryProvider>);
    const action = await screen.findByRole("button", { name: "引导当前任务" });
    expect(action).toBeDisabled();
    expect(action).toHaveAttribute("title", "当前执行暂时无法接受引导，请刷新状态后重试。");
    executionID = "execution-a";
    await act(async () => { queryClient.setQueryData(v2QueryKeys.execution("thread-a"), execution()); });
    await waitFor(() => expect(screen.getByRole("button", { name: "引导当前任务" })).toBeEnabled());
  });

  it("compacts a promoted source while keeping its original text expandable and the correction fully visible", async () => {
    const client = baseClient({ getPage: vi.fn(() => Promise.resolve(page([
      queuedTranscript("promoted-source", "queued-one", { status: "cancelled", promoted_to_message_id: "replacement-steer" }),
      queuedTranscript("cancelled-source", "cancelled-one", { status: "cancelled" }),
      queuedTranscript("steer-replacement", "replacement-steer", { status: "committed", delivery_mode: "steer",
        promoted_from_message_id: "queued-one" }),
    ]))) } as unknown as Partial<APIClient>);
    const ui = renderConversation(client);
    const summary = await screen.findByText("排队消息已转为引导");
    expect(summary).toHaveAttribute("title", "同样的排队要求");
    const history = summary.closest("details")!;
    expect(history).not.toHaveAttribute("open");
    expect(screen.getByText("已取消，不会继续处理")).toBeInTheDocument();
    expect(screen.getByText("已加入当前任务")).toBeInTheDocument();
    const user = userEvent.setup();
    await user.click(summary);
    expect(history).toHaveAttribute("open");
    expect(within(history).getByText("同样的排队要求")).toBeInTheDocument();
    expect(within(ui.container.querySelector(".v2-narrative")!).getAllByText("同样的排队要求")).toHaveLength(3);
  });

  it("suppresses only the exact durable pending queue identity while retaining equal text and history", async () => {
    const items = [queuedTranscript("queued-event", "queued-one"), queuedTranscript("another-event", "another-message"),
      queuedTranscript("previous-run-event", "queued-one", { run_id: "run-previous" }),
      queuedTranscript("committed-event", "queued-one", { status: "committed" }),
      queuedTranscript("cancelled-event", "queued-one", { status: "cancelled" }),
      queuedTranscript("unconfirmed-event", "queued-one", { durable: false, provisional: true })];
    const client = baseClient({ get: vi.fn((path: string) => Promise.resolve(path.endsWith("/queued-messages")
      ? queueSnapshot() : { ...detail("thread-a"), active_run: { id: "run-thread-a", session_id: "sess-thread-a", status: "running" } })),
      getPage: vi.fn(() => Promise.resolve(page(items))) } as unknown as Partial<APIClient>);
    const ui = renderConversation(client);
    await screen.findByRole("button", { name: "查看消息 1 全文" });
    await waitFor(() => expect(within(ui.container.querySelector(".v2-narrative")!).getAllByText("同样的排队要求")).toHaveLength(5));
    expect(ui.queryClient.getQueryData<{ pages: PageResult<ThreadTranscriptItemView>[] }>(v2QueryKeys.transcript("thread-a"))
      ?.pages[0].items).toEqual(items);
  });

  it("keeps an unconfirmed pending bubble and restores it when a complete queue refresh fails", async () => {
    const pendingQueue = deferred<unknown>(); let fail = false, first = true;
    const client = baseClient({ get: vi.fn((path: string) => {
      if (path.endsWith("/queued-messages")) {
        if (fail) return Promise.reject(new TypeError("queue unavailable"));
        if (first) { first = false; return pendingQueue.promise; }
        return Promise.resolve(queueSnapshot());
      }
      return Promise.resolve({ ...detail("thread-a"), active_run: { id: "run-thread-a", session_id: "sess-thread-a", status: "running" } });
    }), getPage: vi.fn(() => Promise.resolve(page([queuedTranscript("queued-event", "queued-one")]))) } as unknown as Partial<APIClient>);
    const ui = renderConversation(client);
    expect(await screen.findByText("同样的排队要求")).toBeInTheDocument();
    await act(async () => { pendingQueue.resolve(queueSnapshot()); });
    await screen.findByRole("button", { name: "查看消息 1 全文" });
    await waitFor(() => expect(within(ui.container.querySelector(".v2-narrative")!).queryByText("同样的排队要求")).not.toBeInTheDocument());
    fail = true;
    await act(async () => { await ui.queryClient.invalidateQueries({ queryKey: [...v2QueryKeys.thread("thread-a"), "queued-messages"] }); });
    expect(await within(ui.container.querySelector(".v2-narrative")!).findByText("同样的排队要求")).toBeInTheDocument();
    expect(screen.getByText(/暂时无法读取完整队列/u)).toBeInTheDocument();
  });

  it("does not suppress a pending bubble using a malformed queue or a successor Run's reused ID", async () => {
    let successor = false;
    const client = baseClient({ get: vi.fn((path: string) => Promise.resolve(path.endsWith("/queued-messages")
      ? successor ? queueSnapshot("run-next", "sess-next") : { ...queueSnapshot(), pending: 2 }
      : { ...detail("thread-a"), active_run: { id: "run-thread-a", session_id: "sess-thread-a", status: "running" } })),
      getPage: vi.fn(() => Promise.resolve(page([queuedTranscript("queued-event", "queued-one")]))) } as unknown as Partial<APIClient>);
    const ui = renderConversation(client);
    await screen.findByText(/暂时无法读取完整队列/u);
    expect(within(ui.container.querySelector(".v2-narrative")!).getByText("同样的排队要求")).toBeInTheDocument();
    successor = true;
    await act(async () => { ui.queryClient.setQueryData(v2QueryKeys.thread("thread-a"),
      { ...detail("thread-a"), active_run: { id: "run-next", session_id: "sess-next", status: "running" } }); });
    await screen.findByRole("button", { name: "查看消息 1 全文" });
    expect(within(ui.container.querySelector(".v2-narrative")!).getByText("同样的排队要求")).toBeInTheDocument();
  });

  it("queues ordinary sends during an active run without a delivery-mode selector", async () => {
    const submitSessionMessage = vi.fn();
    const submitThreadTurn = vi.fn();
    const client = baseClient({
      get: vi.fn(() => Promise.resolve({ ...detail("thread-a"),
        active_run: { id: "run-thread-a", session_id: "sess-thread-a", status: "running" } })),
      submitSessionMessage, submitThreadTurn,
    } as Partial<APIClient>);
    renderConversation(client);
    const user = userEvent.setup();
    await user.click(await screen.findByRole("button", { name: "发送 thread-a" }));
    expect(screen.queryByRole("combobox", { name: "发送方式" })).not.toBeInTheDocument();
    await waitFor(() => expect(submitThreadTurn).toHaveBeenCalledWith("thread-a",
      expect.objectContaining({ content: "pending-thread-a" }), expect.any(String)));
    expect(submitSessionMessage).not.toHaveBeenCalled();
  });

  it("sends the same draft normally when the task ends before sending", async () => {
    const execution: ThreadExecutionView = { version: "thread_execution.v1", thread_id: "thread-a",
      state: "running", execution_id: "execution-a", queued_messages: 0, capability_grant: false };
    const submitSessionMessage = vi.fn();
    const submitThreadTurn = vi.fn();
    const view = renderConversation(baseClient({ hasThreadExecutionRead: true,
      get: vi.fn(() => Promise.resolve({ ...detail("thread-a"),
        active_run: { id: "run-thread-a", session_id: "sess-thread-a", status: "running" } })),
      threadExecution: vi.fn(() => Promise.resolve(execution)), submitSessionMessage, submitThreadTurn,
    } as Partial<APIClient>));
    const user = userEvent.setup();
    await screen.findByRole("button", { name: "发送 thread-a" });
    await act(async () => { view.queryClient.setQueryData(v2QueryKeys.execution("thread-a"),
      { ...execution, state: "idle", execution_id: undefined }); });
    expect(screen.queryByRole("combobox", { name: "发送方式" })).not.toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: "发送 thread-a" }));
    await waitFor(() => expect(submitThreadTurn).toHaveBeenCalledWith("thread-a",
      expect.objectContaining({ content: "pending-thread-a" }), expect.any(String)));
    expect(submitSessionMessage).not.toHaveBeenCalled();
  });
  it("explains unavailable execution observation while retaining readable work history", async () => {
    const threadExecution = vi.fn();
    renderConversation(baseClient({ hasThreadExecutionRead: false, threadExecution,
      getPage: async <T,>() => page([transcriptItem("history", "Persisted work remains readable", 1)]) as PageResult<T>,
    }));
    expect(await screen.findByText("当前连接不提供 Agent 活动状态。对话与工作记录仍可查看。")).toBeInTheDocument();
    expect(await screen.findByText("Persisted work remains readable")).toBeInTheDocument();
    expect(threadExecution).not.toHaveBeenCalled();
    expect(screen.queryByRole("button", { name: "刷新执行状态" })).not.toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "停止当前执行" })).not.toBeInTheDocument();
  });

  it("replaces a terminal file rejection with a new turn after references are removed", async () => {
    const submitThreadTurn = vi.fn().mockRejectedValueOnce(new APIRequestError("File changed", "CONFLICT", 409, "rejected", false))
      .mockResolvedValue({ steering: { id: "steering-corrected" } });
    composerFiles.current = [{ id: "reference-1", path: "README.md", digest: "a".repeat(64), partial: false, redacted: false }];
    const view = renderConversation(baseClient({ submitThreadTurn }));
    const user = userEvent.setup();
    await user.click(await screen.findByRole("button", { name: "发送 thread-a" }));
    expect(await screen.findByRole("alert")).toHaveTextContent("这条消息未入队");
    composerFiles.current = undefined;
    await user.click(screen.getByRole("button", { name: "发送 thread-a" }));
    await waitFor(() => expect(screen.queryByRole("alert")).not.toBeInTheDocument());
    expect(submitThreadTurn).toHaveBeenCalledTimes(2);
    expect(submitThreadTurn.mock.calls[0]![1]).toMatchObject({ files: [
      { source_kind: "workspace_file", path: "README.md", expected_sha256: "a".repeat(64) }] });
    expect(submitThreadTurn.mock.calls[1]![1]).toEqual({ version: "thread_message_submission.v1", content: "pending-thread-a" });
    expect(submitThreadTurn.mock.calls[1]![2]).not.toBe(submitThreadTurn.mock.calls[0]![2]);
    expect(view.container.querySelector(".v2-user-turn")).not.toBeInTheDocument();
  });

  it("rechecks an unknown turn with the original key and files before allowing a correction", async () => {
    const submitThreadTurn = vi.fn().mockRejectedValueOnce(new Error("response lost"))
      .mockRejectedValueOnce(new APIRequestError("File intent rejected", "CONFLICT", 409, "confirmed", false))
      .mockResolvedValue({ steering: { id: "steering-corrected" } });
    composerFiles.current = [{ id: "reference-old", path: "README.md", digest: "a".repeat(64), partial: false, redacted: false }];
    renderConversation(baseClient({ submitThreadTurn }));
    const user = userEvent.setup();
    await user.click(await screen.findByRole("button", { name: "发送 thread-a" }));
    await waitFor(() => expect(screen.getByRole("alert")).toHaveTextContent("这条消息未入队"));
    expect(submitThreadTurn).toHaveBeenCalledTimes(2);
    expect(submitThreadTurn.mock.calls[1]).toEqual(submitThreadTurn.mock.calls[0]);
    composerFiles.current = [{ ...composerFiles.current[0]!, id: "reference-new", digest: "b".repeat(64) }];
    await user.click(screen.getByRole("button", { name: "发送 thread-a" }));
    await waitFor(() => expect(screen.queryByRole("alert")).not.toBeInTheDocument());
    expect(submitThreadTurn).toHaveBeenCalledTimes(3);
    expect(submitThreadTurn.mock.calls[2]![2]).not.toBe(submitThreadTurn.mock.calls[1]![2]);
    expect(submitThreadTurn.mock.calls[2]![1]).toMatchObject({ files: [{ expected_sha256: "b".repeat(64) }] });
  });

  it("allows references for a successor and explains the boundary only while execution owns the task", async () => {
    const restoredDetail = { ...detail("thread-a"), thread: { ...detail("thread-a").thread,
      composer_state: "successor_required" }, last_run: { id: "run-thread-a", status: "cancelled" } } as ThreadDetailView;
    const view = renderConversation(baseClient({
      get: async <T,>() => restoredDetail as T,
    }));
    const composer = await screen.findByRole("button", { name: "发送 thread-a" });
    expect(composer).not.toHaveAttribute("data-file-reference-unavailable");
    await act(async () => view.queryClient.setQueryData(v2QueryKeys.thread("thread-a"), {
      ...restoredDetail, thread: { ...restoredDetail.thread, composer_state: "ready" },
      active_run: { id: "successor-run", status: "running" },
    }));
    await waitFor(() => expect(composer).toHaveAttribute("data-file-reference-unavailable", expect.stringContaining("活动状态尚未确认")));
    await act(async () => view.queryClient.setQueryData(v2QueryKeys.thread("thread-a"), restoredDetail));
    await waitFor(() => expect(composer).not.toHaveAttribute("data-file-reference-unavailable"));
  });

  it("does not claim cached activity in file guidance after a failed status read", async () => {
    const execution: ThreadExecutionView = { version: "thread_execution.v1", thread_id: "thread-a",
      state: "running", queued_messages: 0, capability_grant: false };
    const threadExecution = vi.fn().mockResolvedValue(execution);
    const view = renderConversation(baseClient({ hasThreadExecutionRead: true, threadExecution }));
    await screen.findByText("正在工作");
    const composer = screen.getByRole("button", { name: "发送 thread-a" });
    expect(composer).toHaveAttribute("data-file-reference-unavailable", expect.stringContaining("项目内文件引用需等执行结束"));
    threadExecution.mockRejectedValue(new Error("execution read failed"));
    await act(async () => { await view.queryClient.invalidateQueries({ queryKey: v2QueryKeys.execution("thread-a") }); });
    expect(await screen.findByText("状态读取失败")).toBeInTheDocument();
    expect(composer).toHaveAttribute("data-file-reference-unavailable", expect.stringContaining("活动状态尚未确认"));
    expect(composer).toBeEnabled();
    expect(screen.queryByText("正在工作")).not.toBeInTheDocument();
    threadExecution.mockResolvedValue({ ...execution, state: "idle" });
    await userEvent.setup().click(screen.getByRole("button", { name: "刷新执行状态" }));
    // An idle task shows no status pill instead of a persistent "waiting" label.
    await waitFor(() => expect(screen.queryByText("状态读取失败")).not.toBeInTheDocument());
    expect(screen.queryByText("等待新消息")).not.toBeInTheDocument();
    expect(composer).not.toHaveAttribute("data-file-reference-unavailable");
  });

  it("restores actual execution control on reopen and waits for confirmed stopping", async () => {
    let execution: ThreadExecutionView = { version: "thread_execution.v1", thread_id: "thread-a",
      execution_id: "thread-execution-a", state: "running", queued_messages: 0, capability_grant: false };
    const interruptThread = vi.fn(async () => { execution = { ...execution, state: "stopping" }; return execution; });
    const client = baseClient({ hasRunExecution: true, hasThreadExecutionRead: true,
      threadExecution: vi.fn(async () => execution), interruptThread });
    const view = renderConversation(client);
    const user = userEvent.setup();
    await user.click(await screen.findByRole("button", { name: "停止当前执行" }));
    expect(interruptThread).toHaveBeenCalledWith("thread-a", "thread-execution-a", expect.any(String));
    expect(await screen.findByRole("button", { name: "正在停止" })).toBeDisabled();
    await act(async () => {
      execution = { version: "thread_execution.v1", thread_id: "thread-a", state: "idle",
        queued_messages: 0, capability_grant: false };
      view.queryClient.setQueryData(v2QueryKeys.execution("thread-a"), execution);
    });
    await waitFor(() => expect(screen.queryByRole("button", { name: "正在停止" })).not.toBeInTheDocument());
    expect(screen.queryByText("正在工作")).not.toBeInTheDocument();
  });

  it("routes the composer stop action through the existing authorized button and pending state", async () => {
    const execution: ThreadExecutionView = { version: "thread_execution.v1", thread_id: "thread-a",
      execution_id: "execution-a", state: "running", queued_messages: 0, capability_grant: false };
    const pending = deferred<ThreadExecutionView>();
    const interruptThread = vi.fn(() => pending.promise);
    const threadExecution = vi.fn().mockResolvedValue(execution);
    const view = renderConversation(baseClient({ hasRunExecution: true, hasThreadExecutionRead: true,
      threadExecution, interruptThread }));
    const button = await screen.findByRole("button", { name: "停止当前执行" });
    expect(button).toHaveAttribute("aria-keyshortcuts", "Escape");
    expect(button).toHaveAttribute("title", expect.stringContaining("输入框内 Esc"));
    act(() => expect(composerStop.current?.()).toBe(true));
    await waitFor(() => expect(interruptThread).toHaveBeenCalledWith("thread-a", "execution-a", "v2-thread-stop-execution-a"));
    await waitFor(() => expect(screen.getByRole("button", { name: "正在停止" })).toBeDisabled());
    act(() => expect(composerStop.current?.()).toBe(false));
    threadExecution.mockResolvedValue({ ...execution, state: "stopping" });
    await act(async () => pending.resolve({ ...execution, state: "stopping" }));
    act(() => expect(composerStop.current?.()).toBe(false));
    threadExecution.mockResolvedValue({ ...execution, state: "idle" });
    await act(async () => view.queryClient.invalidateQueries({ queryKey: v2QueryKeys.execution("thread-a") }));
    await waitFor(() => expect(screen.queryByRole("button", { name: "正在停止" })).not.toBeInTheDocument());
    act(() => expect(composerStop.current?.()).toBe(false));
    expect(interruptThread).toHaveBeenCalledOnce();
  });

  it("does not offer a composer stop action when execution permission is missing", async () => {
    const interruptThread = vi.fn();
    renderConversation(baseClient({ hasRunExecution: false, hasThreadExecutionRead: true, interruptThread,
      threadExecution: vi.fn().mockResolvedValue({ version: "thread_execution.v1", thread_id: "thread-a",
        execution_id: "execution-a", state: "running", queued_messages: 0, capability_grant: false }) }));
    await screen.findByText("正在工作");
    act(() => expect(composerStop.current?.()).toBe(false));
    expect(interruptThread).not.toHaveBeenCalled();
  });

  it("keeps the reader's scroll position when new content arrives and restores it on return", async () => {
    const view = renderConversation(baseClient());
    await screen.findByText("Title thread-a");
    await waitFor(() => expect(view.queryClient.getQueryState(v2QueryKeys.transcript("thread-a"))?.status).toBe("success"));
    const scroller = view.container.querySelector<HTMLDivElement>(".v2-conversation-scroll")!;
    Object.defineProperties(scroller, { scrollHeight: { value: 2000, configurable: true },
      clientHeight: { value: 500, configurable: true } });
    scroller.scrollTop = 180;
    fireEvent.scroll(scroller);
    await act(async () => view.queryClient.setQueryData(v2QueryKeys.transcript("thread-a"), {
      pages: [page([transcriptItem("new", "new content", 1)])], pageParams: [""],
    }));
    expect(await screen.findByText("new content")).toBeInTheDocument();
    expect(scroller.scrollTop).toBe(180);
    expect(await screen.findByRole("button", { name: "有新内容 · 回到最新" })).toBeInTheDocument();
    view.rerenderThread("thread-b");
    await screen.findByText("Title thread-b");
    view.rerenderThread("thread-a");
    await screen.findByText("Title thread-a");
    expect(view.container.querySelector<HTMLDivElement>(".v2-conversation-scroll")!.scrollTop).toBe(180);
  });

  it("follows viewport and card size changes without moving a reader looking at history", async () => {
    const resize = new Map<Element, () => void>();
    vi.stubGlobal("ResizeObserver", class {
      constructor(private callback: ResizeObserverCallback) {}
      observe(element: Element) { resize.set(element, () => this.callback([], this as unknown as ResizeObserver)); }
      disconnect() {}
    });
    try {
      const view = renderConversation(baseClient());
      await screen.findByText("Title thread-a");
      await waitFor(() => expect(view.queryClient.getQueryState(v2QueryKeys.transcript("thread-a"))?.status).toBe("success"));
      const scroller = view.container.querySelector<HTMLDivElement>(".v2-conversation-scroll")!;
      const content = scroller.firstElementChild!;
      let height = 2000, viewport = 500, top = 1500;
      Object.defineProperties(scroller, {
        scrollHeight: { get: () => height, configurable: true },
        clientHeight: { get: () => viewport, configurable: true },
        scrollTop: { get: () => top, set: (value: number) => { top = Math.min(value, height - viewport); }, configurable: true },
      });
      fireEvent.scroll(scroller);
      expect(resize.has(scroller)).toBe(true); expect(resize.has(content)).toBe(true);
      await act(async () => { viewport = 350; resize.get(scroller)!(); });
      expect(scroller.scrollTop).toBe(1650);
      await act(async () => { height = 2200; resize.get(content)!(); });
      expect(scroller.scrollTop).toBe(1850);
      // Switching editors briefly clamps scrollTop before delivering a resize.
      viewport = 320; scroller.scrollTop = 1600; fireEvent.scroll(scroller);
      expect(scroller.scrollTop).toBe(1880);
      scroller.scrollTop = 180; fireEvent.scroll(scroller);
      await act(async () => { viewport = 300; height = 2400; resize.get(scroller)!(); resize.get(content)!(); });
      expect(scroller.scrollTop).toBe(180);
      expect(view.queryClient.getQueryData(["v2", "reading", "thread-a"])).toEqual({ top: 180, following: false });
    } finally { cleanup(); vi.unstubAllGlobals(); }
  });

  it("shows the current Run's pending approval and exact preview in a read-only conversation", async () => {
    const item = { id: "approval-readonly", proposal_id: "proposal-readonly", run_id: "run-thread-a",
      workspace_id: "", tool_name: "web_fetch", canonical_url: "https://example.org/public",
      exact_target: "example.org", status: "pending", allowed_actions: ["approve_once", "approve_for_thread", "deny"], version: 1 };
    const approvalQueue = vi.fn().mockResolvedValue({ items: [item], truncated: false });
    const approvalPreview = vi.fn().mockResolvedValue({ run_id: item.run_id, approval_id: item.id,
      proposal_id: item.proposal_id, tool_name: item.tool_name, workspace_id: item.workspace_id,
      effect: "fetch_public_https", fields: [{ name: "url", value: item.canonical_url }],
      source_current: true, redacted: false, truncated: false });
    const decideApproval = vi.fn();
    renderConversation(baseClient({ hasApprovalControl: false, hasThreadControl: false,
      get: async <T,>() => ({ ...detail("thread-a"),
        active_run: { id: "run-thread-a", status: "waiting_approval" } }) as T,
      approvalQueue, approvalPreview, decideApproval,
      controlledCommandProposals: vi.fn().mockResolvedValue({ items: [], page: { limit: 100 }, requestID: "fixture" }),
      hostCommandProposals: vi.fn().mockResolvedValue({ items: [], page: { limit: 100 }, requestID: "fixture" }),
    }));
    expect(await screen.findByText(item.canonical_url)).toBeVisible();
    expect(screen.getByText(/当前连接可以查看审批/)).toBeVisible();
    expect(approvalQueue).toHaveBeenCalledWith(item.run_id, expect.any(AbortSignal));
    expect(approvalPreview).toHaveBeenCalledWith(item.run_id, item.id, expect.any(AbortSignal));
    expect(screen.queryByRole("button", { name: /允许一次|本对话允许|拒绝/ })).not.toBeInTheDocument();
    expect(decideApproval).not.toHaveBeenCalled();
  });

  it("keeps a decided web approval recoverable while the Run is already running", async () => {
    const runningDetail = { ...detail("thread-a"),
      active_run: { id: "run-thread-a", status: "running" } } as ThreadDetailView;
    const client = baseClient({
      hasApprovalControl: true,
      get: vi.fn(() => Promise.resolve(runningDetail)),
      approvalQueue: vi.fn(() => Promise.resolve({
        protocol_version: "approval_queue.v1", run_id: "run-thread-a", truncated: false,
        process_execution_enabled: false, session_grant_created: false,
        capability_grant: false, items: [{
          id: "approval-web-recovery", proposal_id: "web-fetch-authorization-recovery",
          run_id: "run-thread-a", session_id: "session-thread-a", workspace_id: "",
          tool_name: "web_fetch", action_class: "public_https_fetch", mode: "per_call",
          status: "approved", allowed_actions: ["approve_once"],
          canonical_url: "https://arxiv.org/abs/2608.13637", exact_target: "arxiv.org",
          version: 2, created_at: "2026-09-02T00:00:00Z",
          updated_at: "2026-09-02T00:00:01Z", process_execution_enabled: false,
          capability_grant: false,
        }],
      })),
      decideApproval: vi.fn(),
    } as Partial<APIClient>);

    renderConversation(client);

    expect(await screen.findByText("恢复上次网页读取")).toBeInTheDocument();
    expect(screen.getByText("已允许，等待恢复")).toBeInTheDocument();
  });

  it("keeps a failed Thread sendable without exposing manual Run controls", async () => {
    const failedDetail = { ...detail("thread-a"), recovery: {
      version: "thread_run_recovery.v1",
      run_id: "run-thread-a",
      handoff_operation_id: "handoff-thread-a",
      error_code: "failed_precondition",
      stop_reason: "failed_precondition",
      detail: "上一次执行已经停止，下一条消息会自动继续。",
      quiescent: true,
      failed_at: "2026-09-01T00:00:00Z",
    } } as ThreadDetailView;
    const client = baseClient({
      get: vi.fn(() => Promise.resolve(failedDetail)),
    } as Partial<APIClient>);

    renderConversation(client);

    expect(await screen.findByRole("button", { name: "发送 thread-a" })).toBeInTheDocument();
    expect(screen.queryByText("可继续")).not.toBeInTheDocument();
    expect(screen.getByRole("button", { name: "发送 thread-a" })).toBeEnabled();
    expect(screen.queryByRole("button", { name: /结束旧 Run/u })).not.toBeInTheDocument();
  });

  it("describes an unconfirmed stop without claiming to cancel accepted input", async () => {
    const current = detail("thread-a");
    current.last_run.session_id = "session-thread-a";
    current.active_run = { ...current.last_run, status: "running" };
    renderConversation(baseClient({ hasThreadExecutionRead: true,
      threadExecution: vi.fn(async () => ({ state: "stop_failed", queued_messages: 0 } as ThreadExecutionView)),
      get: vi.fn(async (path: string) => path.endsWith("/queued-messages") ? {
        version: "thread_queued_messages.v1", thread_id: "thread-a", run_id: "run-thread-a", session_id: "session-thread-a",
        pending: 1, prepared: 0, capability_grant: false, items: [{ id: "message-pending", sequence: 1, status: "pending", prepared: false,
          content: "停止后仍保留的要求", content_sha256: "a".repeat(64), content_redacted: false, revision: 0,
          created_at: "2026-09-22T01:00:00Z", images: [], attachments: [], can_edit: false, can_cancel: false }],
      } : current) as APIClient["get"],
    }));
    expect(await screen.findByText("停止尚未完成。请重试停止，确认后再发送；已受理的要求会保留。")).toBeInTheDocument();
    expect(await screen.findByText("待处理 1 条")).toBeInTheDocument();
    expect(screen.queryByText(/排队消息的取消/u)).not.toBeInTheDocument();
  });

  it("retains the exact failure reason in conversation details", async () => {
    const reason = new APIRequestError("Recorded provider failure at model call call-17", "UNAVAILABLE", 503,
      "request-failed", undefined, undefined, true);
    renderConversation(baseClient({ submitThreadTurn: vi.fn().mockRejectedValue(reason) }));
    fireEvent.click(await screen.findByRole("button", { name: "发送 thread-a" }));
    const originalReason = await screen.findByText(reason.message);
    expect(originalReason.closest("details")).toHaveTextContent("查看原因");
    expect(screen.queryByRole("button", { name: "重试核对" })).not.toBeInTheDocument();
  });

  it("isolates optimistic sends and their async completion by Thread", async () => {
    const submission = deferred<Awaited<ReturnType<APIClient["submitThreadTurn"]>>>();
    const submitThreadTurn = vi.fn(() => submission.promise);
    const client = baseClient({ submitThreadTurn } as Partial<APIClient>);
    const user = userEvent.setup();
    const view = renderConversation(client);

    await screen.findByText("Title thread-a");
    await user.click(screen.getByRole("button", { name: "发送 thread-a" }));
    expect(screen.getByText("pending-thread-a")).toBeInTheDocument();
    expect(submitThreadTurn).toHaveBeenCalledWith("thread-a", expect.anything(), expect.any(String));

    view.rerenderThread("thread-b");
    await screen.findByText("Title thread-b");
    expect(screen.queryByText("pending-thread-a")).not.toBeInTheDocument();

    await act(async () => {
      submission.resolve({ steering: { id: "steering-a" } } as Awaited<
        ReturnType<APIClient["submitThreadTurn"]>>);
      await submission.promise;
    });
    expect(screen.queryByText("pending-thread-a")).not.toBeInTheDocument();
    expect(screen.getByRole("button", { name: "发送 thread-b" })).toBeInTheDocument();
  });

  it("distinguishes an in-flight submission from Agent execution and consumes the completed Thread projection", async () => {
    const submission = deferred<Awaited<ReturnType<APIClient["submitThreadTurn"]>>>();
    const client = baseClient({
      get: vi.fn(() => Promise.resolve({ ...detail("thread-a"),
        active_run: { id: "run-thread-a", status: "running" },
        statistics: { turns: 2, steps: 56, input_tokens: 1000, output_tokens: 100, total_tokens: 1100,
          cache_read_tokens: 0, cache_reported_calls: 0, usage_calls: 56,
          timed_output_tokens: 0, decode_millis: 0 } })),
      submitThreadTurn: vi.fn(() => submission.promise),
    } as Partial<APIClient>);
    const user = userEvent.setup();
    const view = renderConversation(client);

    await screen.findByText("Title thread-a");
    expect(screen.queryByText("正在工作")).not.toBeInTheDocument();

    await user.click(screen.getByRole("button", { name: "发送 thread-a" }));
    expect(await screen.findByText("正在发送消息")).toBeInTheDocument();
    expect(screen.queryByText("正在工作")).not.toBeInTheDocument();
    expect(screen.getByRole("group", { name: "对话统计" })).toHaveTextContent("2 轮 56 步");
    expect(screen.queryByText(/停止会取消/u)).not.toBeInTheDocument();

    const completed = detail("thread-a").thread;
    await act(async () => {
      submission.resolve({ steering: { id: "steering-a" }, thread: completed } as Awaited<
        ReturnType<APIClient["submitThreadTurn"]>>);
      await submission.promise;
    });
    await waitFor(() => expect(screen.queryByText("正在发送消息")).not.toBeInTheDocument());
    expect(view.queryClient.getQueryData<ThreadDetailView>(v2QueryKeys.thread("thread-a"))?.thread)
      .toEqual(completed);
  });

  it("keeps the next-message chip after the matching submission resolves", async () => {
    const submission = deferred<Awaited<ReturnType<APIClient["submitThreadTurn"]>>>();
    const execution: ThreadExecutionView = { version: "thread_execution.v1", thread_id: "thread-a",
      state: "running", queued_messages: 0, capability_grant: false };
    const onDraftChange = vi.fn();
    const user = userEvent.setup();
    renderConversation(baseClient({ hasThreadExecutionRead: true,
      threadExecution: vi.fn(async () => execution), submitThreadTurn: vi.fn(() => submission.promise) }),
      "thread-a", undefined, { draft: "pending-thread-a", onDraftChange });

    await screen.findByText("Title thread-a");
    expect(screen.queryByRole("button", { name: "编写下一条" })).not.toBeInTheDocument();

    await user.click(screen.getByRole("button", { name: "发送 thread-a" }));
    expect(await screen.findByRole("button", { name: "编写下一条" })).toBeInTheDocument();

    await act(async () => {
      submission.resolve({ steering: { id: "steering-a" } } as Awaited<
        ReturnType<APIClient["submitThreadTurn"]>>);
      await submission.promise;
    });
    // The settled mutation leaves the cache, but the leftover draft still
    // matches the accepted payload, so the chip stays available.
    const chip = await screen.findByRole("button", { name: "编写下一条" });

    await user.click(chip);
    expect(onDraftChange).toHaveBeenCalledWith("");
  });

  it("does not offer another Thread's unsent draft for clearing after a late confirmation", async () => {
    const submission = deferred<Awaited<ReturnType<APIClient["submitThreadTurn"]>>>();
    const onDraftChange = vi.fn();
    const client = baseClient({ hasThreadExecutionRead: true,
      threadExecution: vi.fn(async (threadID: string) => ({ version: "thread_execution.v1", thread_id: threadID,
        state: "running", queued_messages: 0, capability_grant: false } as ThreadExecutionView)),
      submitThreadTurn: vi.fn(() => submission.promise) });
    const view = renderConversation(client, "thread-a", undefined,
      { draft: "pending-thread-a", onDraftChange });
    const user = userEvent.setup();
    await user.click(await screen.findByRole("button", { name: "发送 thread-a" }));

    view.rerenderThread("thread-b");
    await screen.findByText("Title thread-b");
    expect(screen.queryByRole("button", { name: "编写下一条" })).not.toBeInTheDocument();
    await act(async () => {
      submission.resolve({ steering: { id: "steering-a" } } as Awaited<
        ReturnType<APIClient["submitThreadTurn"]>>);
      await submission.promise;
    });
    expect(screen.queryByRole("button", { name: "编写下一条" })).not.toBeInTheDocument();
    expect(onDraftChange).not.toHaveBeenCalled();

    // The same text remains a confirmed submission in A, where it was sent.
    view.rerenderThread("thread-a");
    expect(await screen.findByRole("button", { name: "编写下一条" })).toBeInTheDocument();
  });

  it("offers the next-message chip when the draft repeats the latest sent transcript message", async () => {
    const execution: ThreadExecutionView = { version: "thread_execution.v1", thread_id: "thread-a",
      state: "running", queued_messages: 0, capability_grant: false };
    const operatorItem: ThreadTranscriptItemView = { ...transcriptItem("operator-1", "已经发送的内容", 1),
      source: "operator" };
    const onDraftChange = vi.fn();
    renderConversation(baseClient({ hasThreadExecutionRead: true,
      threadExecution: vi.fn(async () => execution),
      getPage: vi.fn(() => Promise.resolve(page([operatorItem]))) } as Partial<APIClient>),
      "thread-a", undefined, { draft: "已经发送的内容", onDraftChange });

    const chip = await screen.findByRole("button", { name: "编写下一条" });
    await userEvent.setup().click(chip);
    expect(onDraftChange).toHaveBeenCalledWith("");
  });

  it("retains the paused state and sends a normal continuation after a run pauses", async () => {
    const execution: ThreadExecutionView = { version: "thread_execution.v1", thread_id: "thread-a",
      state: "running", execution_id: "execution-a", queued_messages: 0, capability_grant: false };
    const pausedDetail: ThreadDetailView = { ...detail("thread-a"),
      active_run: { id: "run-thread-a", session_id: "sess-thread-a", status: "paused" } } as ThreadDetailView;
    const submitThreadTurn = vi.fn();
    const submitSessionMessage = vi.fn();
    const view = renderConversation(baseClient({ hasThreadExecutionRead: true, submitThreadTurn, submitSessionMessage,
      get: vi.fn(() => Promise.resolve({ ...detail("thread-a"),
        active_run: { id: "run-thread-a", session_id: "sess-thread-a", status: "running" } })),
      threadExecution: vi.fn(() => Promise.resolve(execution)),
    } as Partial<APIClient>));
    const user = userEvent.setup();
    await screen.findByRole("button", { name: "发送 thread-a" });
    await act(async () => {
      view.queryClient.setQueryData(v2QueryKeys.thread("thread-a"), pausedDetail);
      view.queryClient.setQueryData(v2QueryKeys.execution("thread-a"),
        { ...execution, state: "idle", execution_id: undefined });
    });
    expect(screen.getByText("发送消息即可继续，也可以仅解除暂停。")).toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: "发送 thread-a" }));
    await waitFor(() => expect(submitThreadTurn).toHaveBeenCalledWith("thread-a",
      expect.objectContaining({ content: "pending-thread-a" }), expect.any(String)));
    expect(submitSessionMessage).not.toHaveBeenCalled();
  });

  it("shows confirmed Agent activity and stopping while the submission response is still pending", async () => {
    const submission = deferred<Awaited<ReturnType<APIClient["submitThreadTurn"]>>>();
    const execution: ThreadExecutionView = { version: "thread_execution.v1", thread_id: "thread-a",
      state: "idle", queued_messages: 0, capability_grant: false };
    const view = renderConversation(baseClient({ hasThreadExecutionRead: true,
      threadExecution: vi.fn(async () => execution), submitThreadTurn: vi.fn(() => submission.promise) }));
    const sendButton = await screen.findByRole("button", { name: "发送 thread-a" });
    await waitFor(() => expect(screen.queryByText("正在同步状态")).not.toBeInTheDocument());
    expect(screen.queryByText("等待新消息")).not.toBeInTheDocument();
    await userEvent.setup().click(sendButton);
    expect(await screen.findByText("正在发送消息")).toBeInTheDocument();
    await act(async () => { view.queryClient.setQueryData(v2QueryKeys.execution("thread-a"),
      { ...execution, state: "running", execution_id: "accepted-execution" }); });
    expect(await screen.findByText("正在工作")).toBeInTheDocument();
    expect(screen.queryByText("正在发送消息")).not.toBeInTheDocument();
    await act(async () => { view.queryClient.setQueryData(v2QueryKeys.execution("thread-a"),
      { ...execution, state: "stopping", execution_id: "accepted-execution" }); });
    expect(await screen.findByText("正在停止")).toBeInTheDocument();
    await act(async () => { submission.resolve({ steering: { id: "steering-a" } } as Awaited<
      ReturnType<APIClient["submitThreadTurn"]>>); await submission.promise; });
  });

  it("pages toward older transcript records without reversing or duplicating the timeline", async () => {
    const middle = transcriptItem("middle", "middle message", 2);
    const getPage = vi.fn((_path: string, _query: unknown, cursor: string) => Promise.resolve(
      cursor === "older-cursor"
        ? page([transcriptItem("oldest", "oldest message", 1), middle])
        : page([middle, transcriptItem("newest", "newest message", 3)], "older-cursor"),
    ));
    const client = baseClient({ getPage } as Partial<APIClient>);
    const user = userEvent.setup();
    const view = renderConversation(client);

    await screen.findByText("newest message");
    expect(Array.from(view.container.querySelectorAll(".v2-assistant-turn p"))
      .map((element) => element.textContent)).toEqual(["middle message", "newest message"]);

    await user.click(screen.getByRole("button", { name: "加载更早记录" }));
    await screen.findByText("oldest message");
    expect(getPage).toHaveBeenCalledWith(expect.stringContaining("/thread-a/transcript"),
      { limit: 100 }, "older-cursor", expect.any(AbortSignal));
    expect(Array.from(view.container.querySelectorAll(".v2-assistant-turn p"))
      .map((element) => element.textContent)).toEqual([
      "oldest message", "middle message", "newest message",
    ]);
    expect(screen.queryByRole("button", { name: "加载更早记录" })).not.toBeInTheDocument();
  });

  it("closes the title menu with Escape or an outside click and restores focus predictably", async () => {
    const onArchive = vi.fn();
    const client = baseClient();
    const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    const user = userEvent.setup();
    render(<><button type="button">Outside</button><QueryClientProvider client={queryClient}>
      <V2Conversation client={client} onArchive={onArchive} onManageModels={vi.fn()}
        onOpenInspector={vi.fn()} threadID="thread-a" workspaces={workspaces} />
    </QueryClientProvider></>);
    await screen.findByText("Title thread-a");
    const trigger = screen.getByRole("button", { name: "对话操作" });

    await user.click(trigger);
    expect(screen.getByRole("menuitem", { name: "归档对话" })).toHaveFocus();
    await user.keyboard("{Escape}");
    expect(screen.queryByRole("menu")).not.toBeInTheDocument();
    expect(trigger).toHaveFocus();

    await user.click(trigger);
    await user.click(screen.getByRole("button", { name: "Outside" }));
    expect(screen.queryByRole("menu")).not.toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Outside" })).toHaveFocus();

    await user.click(trigger);
    await user.click(screen.getByRole("menuitem", { name: "归档对话" }));
    expect(onArchive).toHaveBeenCalledTimes(1);
    expect(trigger).toHaveFocus();
    await waitFor(() => expect(screen.queryByRole("menu")).not.toBeInTheDocument());
  });

  it("parses project file links and ignores external schemes or anchors", () => {
    expect(parseProjectFileLink("src/index.ts:42")).toEqual({ path: "src/index.ts", line: 42 });
    expect(parseProjectFileLink("README.md:42")).toEqual({ path: "README.md", line: 42 });
    expect(parseProjectFileLink("file:///d:/GitProjects/app.ts#L18")).toEqual({ path: "d:/GitProjects/app.ts", line: 18 });
    expect(parseProjectFileLink("./web/src/main.tsx")).toEqual({ path: "web/src/main.tsx", line: undefined });
    expect(parseProjectFileLink("https://github.com/foo/bar")).toBeNull();
    expect(parseProjectFileLink("//example.com/foo.ts")).toBeNull();
    expect(parseProjectFileLink("#anchor")).toBeNull();
    expect(parseProjectFileLink("")).toBeNull();
  });

  it("opens the workspace file drawer from the header and closes with focus restored", async () => {
    const client = baseClient();
    const user = userEvent.setup();
    renderConversation(client);

    await screen.findByText("Title thread-a");
    const fileTrigger = screen.getByRole("button", { name: "工作区文件" });
    await user.click(fileTrigger);

    expect(await screen.findByRole("dialog", { name: "工作区文件" })).toBeInTheDocument();
    expect(screen.getByText("run-thread-a")).not.toBeVisible();
    await user.click(screen.getByText("执行信息"));
    expect(screen.getByText("run-thread-a")).toBeVisible();

    const closeBtn = screen.getByRole("button", { name: "关闭文件面板" });
    await user.click(closeBtn);

    await waitFor(() => {
      expect(screen.queryByRole("dialog", { name: "工作区文件" })).not.toBeInTheDocument();
    });
    expect(fileTrigger).toHaveFocus();
  });

  it("collapses workbench panels in stages and restores the selected file without taking focus", async () => {
    let width = 1140;
    const resizes = new Set<() => void>();
    const clientWidth = vi.spyOn(HTMLElement.prototype, "clientWidth", "get")
      .mockImplementation(function (this: HTMLElement) {
        return this.classList.contains("v2-shell") ? width : this.classList.contains("v2-conversation") ? width - 299 : 0;
      });
    vi.stubGlobal("ResizeObserver", class {
      private resize: () => void;
      constructor(callback: ResizeObserverCallback) { this.resize = () => callback([], this as unknown as ResizeObserver); resizes.add(this.resize); }
      observe() {}
      disconnect() { resizes.delete(this.resize); }
    });
    try {
      const user = userEvent.setup();
      const client = baseClient({
        fileEditChangeSet: vi.fn().mockResolvedValue({ workspace_id: "workspace-1" }),
        getPage: vi.fn(() => Promise.resolve(page([
          transcriptItem("layout-link", "查看 [src/index.ts](src/index.ts)", 1),
        ]))) as unknown as APIClient["getPage"],
      });
      const directory = await client.workspaceExplore("workspace-1", ".");
      vi.mocked(client.workspaceExplore).mockImplementation(async (_workspaceID, path = ".") => path === "." ? directory : {
        ...directory, path, kind: "file", entries: [], content: "export const value = 1;",
      });
      const view = renderConversation(client);
      view.container.classList.add("v2-shell", "has-sidebar");
      const trigger = await screen.findByRole("button", { name: "工作区文件" });
      const sourceLink = await screen.findByRole("link", { name: "index.ts" });
      expect(sourceLink).toHaveAttribute("title", "src/index.ts");
      await user.click(sourceLink);
      const dock = await screen.findByRole("complementary", { name: "工作区文件" });
      expect(dock.closest(".v2-conversation-stage")).not.toBeNull();
      expect(dock).not.toHaveAttribute("aria-modal");
      within(dock).getByRole("button", { name: "关闭文件面板" }).focus();
      await user.tab({ shift: true });
      expect(screen.getByRole("button", { name: "发送 thread-a" })).toHaveFocus();
      expect(await within(dock).findByText("export const value = 1;")).toBeInTheDocument();
      within(dock).getByRole("button", { name: "关闭文件面板" }).focus();
      await act(async () => { width = 960; resizes.forEach((resize) => resize()); });
      expect(screen.queryByRole("complementary", { name: "工作区文件" })).not.toBeInTheDocument();
      expect(screen.queryByRole("dialog", { name: "工作区文件" })).not.toBeInTheDocument();
      expect(dock.closest(".v2-workbench-slot")).toHaveAttribute("inert");
      expect(trigger).toHaveFocus();
      screen.getByRole("button", { name: "发送 thread-a" }).focus();
      await act(async () => { width = 961; resizes.forEach((resize) => resize()); });
      expect(screen.getByRole("complementary", { name: "工作区文件" })).toBe(dock);
      expect(within(dock).getByText("export const value = 1;")).toBeInTheDocument();
      expect(screen.getByRole("button", { name: "发送 thread-a" })).toHaveFocus();
      await act(async () => {
        view.container.classList.remove("has-sidebar"); width = 800; resizes.forEach((resize) => resize());
      });
      expect(screen.getByRole("complementary", { name: "工作区文件" })).toBe(dock);
      await act(async () => { width = 720; resizes.forEach((resize) => resize()); });
      expect(screen.queryByRole("complementary", { name: "工作区文件" })).not.toBeInTheDocument();
      await user.click(trigger);
      const modal = screen.getByRole("dialog", { name: "工作区文件" });
      expect(modal).toHaveAttribute("aria-modal", "true");
      expect(modal.parentElement?.parentElement).toBe(document.body);
      expect(document.querySelector(".v2-conversation")?.closest("[inert]")).not.toBeNull();
      expect(await within(modal).findByText("export const value = 1;")).toBeInTheDocument();
      await user.keyboard("{Escape}");
      expect(screen.queryByRole("dialog", { name: "工作区文件" })).not.toBeInTheDocument();
      expect(trigger).toHaveFocus();
      await act(async () => { width = 1140; resizes.forEach((resize) => resize()); });
      expect(screen.queryByRole("complementary", { name: "工作区文件" })).not.toBeInTheDocument();
    } finally {
      cleanup(); clientWidth.mockRestore(); vi.unstubAllGlobals();
    }
  });

  it("intercepts project file links in assistant messages and opens the workspace file drawer", async () => {
    const linkItem = transcriptItem("item-with-link", "请查看 [src/index.ts:42](src/index.ts:42)", 1);
    const client = baseClient({
      getPage: vi.fn(() => Promise.resolve(page([linkItem]))) as unknown as APIClient["getPage"],
    });
    const user = userEvent.setup();
    renderConversation(client);

    const fileLink = await screen.findByRole("link", { name: "index.ts (line 42)" });
    expect(fileLink).toHaveAttribute("title", "src/index.ts (line 42)");
    expect(fileLink).toHaveClass("v2-file-link");

    await user.click(fileLink);
    expect(await screen.findByRole("dialog", { name: "工作区文件" })).toBeInTheDocument();
  });

  it("renders README.md:42 markdown links as project file links instead of clearing them", async () => {
    const linkItem = transcriptItem("item-readme", "详情参见 [README.md:42](README.md:42)", 1);
    const client = baseClient({
      getPage: vi.fn(() => Promise.resolve(page([linkItem]))) as unknown as APIClient["getPage"],
    });
    const user = userEvent.setup();
    renderConversation(client);

    const fileLink = await screen.findByRole("link", { name: "README.md (line 42)" });
    expect(fileLink).toHaveClass("v2-file-link");

    await user.click(fileLink);
    expect(await screen.findByRole("dialog", { name: "工作区文件" })).toBeInTheDocument();
  });

  it("resolves the originating Run's workspace before reading files in V2FileDrawer", async () => {
    const client = baseClient({
      fileEditChangeSet: vi.fn(() => Promise.resolve({
        protocol_version: "file_edit_change_set.v1",
        workspace_id: "drydock-run-123",
        proposed_count: 1,
        approved_count: 0,
        applied_count: 0,
        denied_count: 0,
        failed_count: 0,
        returned_count: 1,
        total_diff_bytes: 0,
      })) as unknown as APIClient["fileEditChangeSet"],
    });

    const trigger = { current: document.createElement("button") };
    const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    render(
      <QueryClientProvider client={queryClient}>
        <V2FileDrawer
          client={client}
          threadID="thread-a"
          workspaceID="source-workspace"
          runID="run-drydock-1"
          onClose={vi.fn()}
          returnFocusRef={trigger}
        />
      </QueryClientProvider>
    );

    expect(await screen.findByRole("dialog", { name: "工作区文件" })).toBeInTheDocument();
    await waitFor(() => {
      expect(client.workspaceExplore).toHaveBeenCalledWith("drydock-run-123", ".", expect.anything());
    });
  });

  it("does not open the current or source workspace when the historical Run's target is unavailable", async () => {
    const client = baseClient({
      fileEditChangeSet: vi.fn().mockRejectedValue(new APIRequestError("Run target unavailable", "FAILED_PRECONDITION", 412)),
      get: vi.fn().mockResolvedValue(fileDrawerReview()),
    });
    renderFileDrawer(client);

    await waitFor(() => expect(screen.queryByText("正在确认工作区…")).not.toBeInTheDocument());
    expect(client.workspaceExplore).not.toHaveBeenCalled();
    const alert = await screen.findByRole("alert");
    expect(alert).toHaveTextContent("无法确认此执行的工作区");
    expect(client.get).toHaveBeenCalledWith("/threads/thread-a/review", {}, expect.any(AbortSignal));
    const executionInfo = screen.getByText("执行信息");
    expect(executionInfo.closest("details")).not.toHaveAttribute("open");
    await userEvent.setup().click(executionInfo);
    expect(executionInfo.closest("details")).toHaveAttribute("open");
    expect(screen.getByText("run-old")).toBeVisible();
  });

  it("opens a historical Run's exact review workspace when its change set is unavailable", async () => {
    const review = fileDrawerReview();
    review.runs[1].workspace_id = "old-workspace";
    const client = baseClient({
      fileEditChangeSet: vi.fn().mockRejectedValue(new APIRequestError("Change set unavailable", "NOT_FOUND", 404)),
      get: vi.fn().mockResolvedValue(review),
    });
    renderFileDrawer(client);

    await waitFor(() => expect(client.workspaceExplore).toHaveBeenCalledWith("old-workspace", "src/index.ts", expect.any(AbortSignal)));
    expect(client.workspaceExplore).toHaveBeenCalledTimes(1);
  });

  it("keeps a failed workspace lookup closed until retry confirms the originating Run's workspace", async () => {
    const review = fileDrawerReview();
    review.runs[1].workspace_id = "old-workspace";
    const get = vi.fn().mockRejectedValueOnce(new Error("Review disconnected")).mockResolvedValue(review);
    const client = baseClient({ fileEditChangeSet: vi.fn().mockRejectedValue(new Error("Change set disconnected")), get });
    renderFileDrawer(client);

    expect(await screen.findByRole("alert")).toHaveTextContent("无法确认此执行的工作区");
    expect(client.workspaceExplore).not.toHaveBeenCalled();
    await userEvent.setup().click(screen.getByRole("button", { name: "重试工作区确认" }));
    await waitFor(() => expect(client.workspaceExplore).toHaveBeenCalledWith("old-workspace", "src/index.ts", expect.any(AbortSignal)));
    expect(client.workspaceExplore).toHaveBeenCalledTimes(1);
  });

  it("copies assistant message and code blocks to the clipboard with visual feedback", async () => {
    const writeText = vi.spyOn(navigator.clipboard, "writeText").mockResolvedValue(undefined);
    const codeItem = transcriptItem("item-with-code", "这是一段分析：\n\n```typescript\nconst sum = (a: number, b: number) => a + b;\n```", 1);
    const client = baseClient({
      getPage: vi.fn(() => Promise.resolve(page([codeItem]))) as unknown as APIClient["getPage"],
    });
    const user = userEvent.setup();
    renderConversation(client);

    const copyMessageBtn = await screen.findByRole("button", { name: "复制回复" });
    await user.click(copyMessageBtn);
    expect(writeText).toHaveBeenCalledWith(expect.stringContaining("这是一段分析："));
    expect(await screen.findByRole("button", { name: "已复制回复" })).toBeInTheDocument();

    const copyCodeBtn = screen.getByRole("button", { name: "复制代码" });
    await user.click(copyCodeBtn);
    expect(writeText).toHaveBeenCalledWith(expect.stringContaining("const sum = (a: number, b: number) => a + b;"));
    expect(await screen.findByRole("button", { name: "已复制代码" })).toBeInTheDocument();
  });

  it("creates a new chat from the selected reply and hands off navigation without executing it", async () => {
    const branched = detail("thread-branch").thread;
    const onBranchCreated = vi.fn();
    const postControl = vi.fn().mockResolvedValue({ version: "thread_branch.v1", thread: branched, source_event_id: "selected-reply" });
    const client = baseClient({ postControl, getPage: vi.fn().mockResolvedValue(page([
      transcriptItem("selected-reply", "Earlier answer", 1), transcriptItem("later-reply", "Later answer", 2),
    ])) });
    const user = userEvent.setup();
    renderConversation(client, "thread-a", undefined, { onBranchCreated });
    const actions = await screen.findAllByRole("button", { name: "分支到新聊天" });
    await user.click(actions[0]);
    expect(postControl).toHaveBeenCalledExactlyOnceWith("/threads/thread-a/branches",
      { version: "thread_branch.v1", source_event_id: "selected-reply" }, expect.stringMatching(/^thread-branch-/u));
    expect(onBranchCreated).toHaveBeenCalledExactlyOnceWith(branched);
    expect(client.submitThreadTurn).not.toHaveBeenCalled();
  });

  it("retains the source chat when branching fails and retries with the original request key", async () => {
    const onBranchCreated = vi.fn();
    const postControl = vi.fn().mockRejectedValueOnce(new Error("Branch response lost"))
      .mockResolvedValue({ version: "thread_branch.v1", thread: detail("new-branch").thread, source_event_id: "selected-reply" });
    const client = baseClient({ postControl, getPage: vi.fn().mockResolvedValue(page([
      transcriptItem("selected-reply", "Keep this answer", 1),
    ])) });
    const user = userEvent.setup();
    renderConversation(client, "thread-a", undefined, { onBranchCreated });
    await user.click(await screen.findByRole("button", { name: "分支到新聊天" }));
    expect(screen.getByRole("alert")).toHaveTextContent("Branch response lost");
    expect(screen.getByText("Keep this answer")).toBeInTheDocument();
    expect(onBranchCreated).not.toHaveBeenCalled();
    await user.click(screen.getByRole("button", { name: "分支到新聊天" }));
    expect(postControl.mock.calls[1]).toEqual(postControl.mock.calls[0]);
    expect(onBranchCreated).toHaveBeenCalledTimes(1);
  });

  it("opens desktop terminal drawer from header and collapses without destroying session", async () => {
    const client = baseClient();
    const user = userEvent.setup();
    renderConversation(client);

    await screen.findByText("Title thread-a");
    const termTrigger = screen.getByRole("button", { name: "终端" });
    await user.click(termTrigger);

    expect(await screen.findByRole("dialog", { name: "任务终端" })).toBeInTheDocument();
    expect(screen.getByText("当前运行环境未启用桌面终端。仅在 Universal-Code 桌面端运行时支持本机 Debug 终端。")).toBeInTheDocument();

    const collapseBtn = screen.getByRole("button", { name: "收起终端" });
    await user.click(collapseBtn);

    await waitFor(() => {
      expect(screen.queryByRole("dialog", { name: "任务终端" })).not.toBeInTheDocument();
    });
    expect(termTrigger).toHaveFocus();
  });

  it("protects terminal termination against errors and surfaces error notice", async () => {
    vi.spyOn(desktopBridge, "desktopUserTerminalEnabled").mockReturnValue(true);
    const closeSpy = vi.spyOn(desktopBridge, "closeDesktopUserTerminal").mockRejectedValue(new Error("Process busy"));
    const onSession = vi.fn();
    const trigger = { current: document.createElement("button") };
    const user = userEvent.setup();

    render(
      <V2TerminalDrawer
        runID="run-term"
        sessionID="sess-active"
        threadTitle="Thread Term"
        workspaceName="Workspace Term"
        onSession={onSession}
        onClose={vi.fn()}
        returnFocusRef={trigger}
      />
    );

    const termBtn = screen.getByRole("button", { name: "终止终端进程" });
    await user.click(termBtn);

    expect(closeSpy).toHaveBeenCalledWith("sess-active");
    expect(onSession).not.toHaveBeenCalledWith("");
    expect(await screen.findByRole("alert")).toHaveTextContent("终止终端失败：Process busy");
  });

  it("does not clear a replacement session if earlier termination settled late", async () => {
    vi.spyOn(desktopBridge, "desktopUserTerminalEnabled").mockReturnValue(true);
    let resolveClose!: () => void;
    vi.spyOn(desktopBridge, "closeDesktopUserTerminal").mockImplementation(
      () => new Promise((resolve) => { resolveClose = resolve; })
    );
    const onSession = vi.fn();
    const trigger = { current: document.createElement("button") };
    const user = userEvent.setup();

    const { rerender } = render(
      <V2TerminalDrawer
        runID="run-term"
        sessionID="sess-old"
        threadTitle="Thread Term"
        workspaceName="Workspace Term"
        onSession={onSession}
        onClose={vi.fn()}
        returnFocusRef={trigger}
      />
    );

    const termBtn = screen.getByRole("button", { name: "终止终端进程" });
    await user.click(termBtn);

    rerender(
      <V2TerminalDrawer
        runID="run-term"
        sessionID="sess-new"
        threadTitle="Thread Term"
        workspaceName="Workspace Term"
        onSession={onSession}
        onClose={vi.fn()}
        returnFocusRef={trigger}
      />
    );

    resolveClose();
    await waitFor(() => {
      expect(onSession).not.toHaveBeenCalledWith("");
    });
  });

  it("keeps terminal session bindings across conversation-page unmounts", async () => {
    vi.spyOn(desktopBridge, "desktopUserTerminalEnabled").mockReturnValue(true);
    const client = baseClient();
    const user = userEvent.setup();

    const view = renderConversation(client, "thread-persist");
    await screen.findByText("Title thread-persist");

    const termTrigger = screen.getByRole("button", { name: "终端" });
    await user.click(termTrigger);

    expect(await screen.findByRole("dialog", { name: "任务终端" })).toBeInTheDocument();
    const startBtn = screen.getByRole("button", { name: "Mock Start Session" });
    await user.click(startBtn);

    view.unmount();

    renderConversation(client, "thread-persist");
    await screen.findByText("Title thread-persist");

    const newTermTrigger = screen.getByRole("button", { name: "终端" });
    await user.click(newTermTrigger);

    expect(await screen.findByRole("dialog", { name: "任务终端" })).toBeInTheDocument();
    const panel = screen.getByTestId("user-terminal-panel");
    expect(panel).toHaveAttribute("data-session-id", "sess-created");
  });
});

it("switches the mounted task navigation and open conversation menu while preserving transcript text", async () => {
  function LanguageControl() {
    const { locale, setLocale } = useLocale();
    return <select aria-label="Test language" value={locale}
      onChange={(event) => setLocale(event.target.value as "zh-CN" | "en-US")}>
      <option value="zh-CN">中文</option><option value="en-US">English</option>
    </select>;
  }
  const client = baseClient({ getPage: vi.fn(() => Promise.resolve(page([
    transcriptItem("user-content", "用户消息和工具输出保持原文", 1),
  ]))) } as Partial<APIClient>);
  const ui = renderConversation(client, "thread-a", <LanguageControl />);
  await screen.findByText("用户消息和工具输出保持原文");
  const navigation = screen.getByRole("navigation", { name: "任务工作区" });
  await userEvent.setup().click(screen.getByRole("button", { name: "对话操作" }));
  expect(screen.getByRole("menuitem", { name: "归档对话" })).toBeInTheDocument();

  fireEvent.change(screen.getByRole("combobox", { name: "Test language" }), { target: { value: "en-US" } });
  expect(screen.getByRole("navigation", { name: "Task workspace" })).toBe(navigation);
  expect(within(navigation).getByRole("button", { name: "Conversation" })).toBeInTheDocument();
  expect(within(navigation).getByRole("button", { name: "Workspace files" })).toHaveTextContent("Files");
  expect(within(navigation).getByRole("button", { name: "Terminal" })).toBeInTheDocument();
  expect(within(navigation).getByRole("button", { name: "App preview" })).toBeInTheDocument();
  expect(within(navigation).getByRole("button", { name: "Review changes" })).toHaveTextContent("Changes and delivery");
  expect(within(navigation).getByRole("button", { name: "View context" })).toHaveTextContent("Context and recovery");
  expect(screen.getByRole("menuitem", { name: "Archive conversation" })).toBeInTheDocument();
  expect(screen.getByRole("menuitem", { name: "Open Inspector" })).toBeInTheDocument();
  expect(screen.getByText("用户消息和工具输出保持原文")).toBeInTheDocument();
  expect(screen.getByText("Title thread-a")).toBeInTheDocument();

  fireEvent.change(screen.getByRole("combobox", { name: "Test language" }), { target: { value: "zh-CN" } });
  expect(screen.getByRole("navigation", { name: "任务工作区" })).toBe(navigation);
  expect(screen.getByRole("menuitem", { name: "归档对话" })).toBeInTheDocument();
  ui.queryClient.clear();
});

function render(ui: ReactElement, options?: RenderOptions) {
  window.localStorage.setItem("prayu.locale.v1", "zh-CN");
  return renderComponent(ui, { wrapper: LocaleProvider, ...options });
}
