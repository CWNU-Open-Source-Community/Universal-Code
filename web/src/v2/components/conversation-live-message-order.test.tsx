import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act, cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import type { APIClient } from "../../api/client";
import type { PublicModelStreamSnapshot, ThreadDetailView, ThreadTranscriptItemView, WorkspaceView } from "../../api/types";
import { LocaleProvider } from "../../lib/locale";
import { V2Conversation } from "./conversation";

const live = vi.hoisted(() => ({ snapshot: null as PublicModelStreamSnapshot | null,
  items: [] as ThreadTranscriptItemView[], queuedIDs: null as string[] | null, refresh: vi.fn(async () => undefined) }));
vi.mock("../../hooks/use-run-event-stream", () => ({ useRunEventStream: () => ({ error: null, frames: [], status: "live" }) }));
vi.mock("../../hooks/use-public-model-stream", () => ({ usePublicModelStream: () => ({ error: null, snapshot: live.snapshot, status: live.snapshot ? "live" : "waiting" }) }));
vi.mock("../use-thread-transcript", () => ({ useV2ThreadTranscript: () => ({ items: live.items,
  refetch: live.refresh, isLoading: false, isError: false, hasNextPage: false, isFetchingNextPage: false }) }));
vi.mock("./permission-control", () => ({ V2PermissionControl: () => null }));
vi.mock("./run-network-authority-control", () => ({ V2RunNetworkAuthorityControl: () => null }));
vi.mock("./model-route-control", () => ({ V2ModelRouteControl: () => null }));
vi.mock("./queued-messages", () => ({ V2QueuedMessages: () => null,
  useV2QueuedMessagesQuery: () => ({ isSuccess: live.queuedIDs !== null,
    data: live.queuedIDs === null ? undefined : { items: live.queuedIDs.map((id) => ({ id })) } }) }));
vi.mock("./composer", () => ({ V2Composer: ({ onSubmit }: { onSubmit: (content: string) => Promise<void> }) =>
  <button type="button" onClick={() => void onSubmit("创建一个HTML页面")}>发送本次请求</button> }));

afterEach(() => { cleanup(); vi.useRealTimers(); live.snapshot = null; live.items = []; live.queuedIDs = null; });

function item(overrides: Partial<ThreadTranscriptItemView>): ThreadTranscriptItemView {
  return { version: "thread_transcript.v1", id: "earlier", canonical_id: "earlier", run_id: "run-order",
    run_ordinal: 1, sequence: 1, source: "model", kind: "model_update", activity_type: "message",
    stage: "result", title: "Assistant", detail: "Earlier task record", durable: true, provisional: false,
    verifiable: true, instruction_authorized: false, created_at: "2026-11-12T10:25:00Z", ...overrides };
}

function snapshot(updatedAt: string): PublicModelStreamSnapshot {
  return { version: "model_public_stream.v3", call: { run_id: "run-order", session_id: "session-order",
    attempt_id: "attempt-order", model_attempt: 1, transport_attempt: 1, max_attempts: 1,
    protocol_repair: 0, tool_round: 0, provider: "fixture", model: "fixture", stream_chunks: 1,
    stream_bytes: 10, cancel_requested: false, started_at: "2026-11-12T10:26:43.200Z" },
    response_id: "response-order", event_sequence: 4, revision: 1, content_kind: "tool_commentary",
    text: "我正在生成页面。", items: [{ id: "message-order", response_id: "response-order", type: "message",
      status: "receiving_content", provisional: true, durable: false }], message_complete: false,
    provisional: true, updated_at: updatedAt };
}

it("keeps a main input before its own live work during a delayed optimistic-to-durable handoff", async () => {
  localStorage.setItem("prayu.locale.v1", "zh-CN");
  vi.useFakeTimers({ toFake: ["Date"] });
  vi.setSystemTime(new Date("2026-11-12T10:26:43.129Z"));
  type Submission = Awaited<ReturnType<APIClient["submitThreadTurn"]>>;
  let resolveSubmission!: (value: Submission) => void;
  const submission = new Promise<Submission>((resolve) => { resolveSubmission = resolve; });
  const detail = { thread: { id: "thread-order", title: "Message order", workspace_id: "workspace-order",
    status: "active", composer_state: "ready" }, active_run: { id: "run-order", session_id: "session-order", status: "running" },
    runs: [], mission: {} } as unknown as ThreadDetailView;
  const client = { hasThreadControl: true, submitThreadTurn: vi.fn(() => submission),
    get: vi.fn(async () => detail), getPage: vi.fn(async () => ({ items: [], page: {} })),
    approvalQueue: vi.fn(async () => ({ items: [] })) } as unknown as APIClient;
  const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false, staleTime: Infinity }, mutations: { retry: false } } });
  const ui = () => <LocaleProvider><QueryClientProvider client={queryClient}><V2Conversation client={client}
    threadID="thread-order" workspaces={[{ id: "workspace-order", name: "Project" }] as WorkspaceView[]}
    onArchive={vi.fn()} onManageModels={vi.fn()} /></QueryClientProvider></LocaleProvider>;
  live.items = [item({})];
  const view = render(ui());
  fireEvent.click(await screen.findByRole("button", { name: "发送本次请求" }));
  await screen.findByText("创建一个HTML页面");
  for (const updatedAt of ["2026-11-12T10:26:44Z", "2026-11-12T10:27:19Z"]) {
    vi.setSystemTime(new Date(updatedAt));
    live.snapshot = snapshot(updatedAt);
    if (updatedAt === "2026-11-12T10:27:19Z") live.snapshot.text = "我正在生成页面并检查布局。";
    view.rerender(ui());
    const progress = await screen.findByText(live.snapshot.text);
    const input = screen.getByText("创建一个HTML页面");
    expect(input.compareDocumentPosition(progress) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy();
    expect(screen.getByText("Earlier task record").compareDocumentPosition(input) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy();
  }
  live.items = [...live.items, item({ id: "saved-input", canonical_id: "input-order", sequence: 2,
    source: "operator", kind: "operator_input", detail: "创建一个HTML页面", status: "committed",
    created_at: "2026-11-12T10:26:43.129Z" })];
  view.rerender(ui());
  await waitFor(() => expect(screen.getAllByText("创建一个HTML页面")).toHaveLength(1));
  expect(screen.getByText("创建一个HTML页面").compareDocumentPosition(screen.getByText("我正在生成页面并检查布局。")) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy();
  await act(async () => {
    resolveSubmission({ steering: { id: "input-order" }, thread: detail.thread } as Submission);
    await submission;
  });
});

it("keeps the running task input in place while its queue record stays pending across tool boundaries", async () => {
  localStorage.setItem("prayu.locale.v1", "zh-CN");
  const inputID = "steer-main-input";
  const task = { id: inputID, input_message_id: inputID, state: "running" as const,
    started_at: "2026-11-12T12:32:28Z" };
  const input = item({ id: "queued-input-event", canonical_id: inputID, sequence: 7, source: "operator",
    source_ref: inputID, kind: "operator_input", detail: "创建一个HTML页面", status: "pending", delivery_mode: "next_turn",
    created_at: "2026-11-12T12:32:27.967Z", task });
  const progress = item({ id: "progress-event", canonical_id: "progress", sequence: 35,
    detail: "我正在读取项目并准备页面。", created_at: "2026-11-12T12:32:30Z", task });
  const tool = item({ id: "read-event", canonical_id: "read", sequence: 40, source: "harness", kind: "tool_call",
    activity_type: "read", tool_name: "workspace_read", title: "读取项目文件", detail: "README.md", status: "completed",
    created_at: "2026-11-12T12:32:31Z", task });
  // A different queued input may have identical text. It has no started task
  // binding and must remain in the queue, without duplicating the main input.
  live.items = [input, progress, tool, { ...input, id: "next-queue-event", canonical_id: "steer-next-input",
    source_ref: "steer-next-input", task: undefined, sequence: 90, created_at: "2026-11-12T12:32:40Z" }];
  live.queuedIDs = [inputID, "steer-next-input"];
  const detail = { thread: { id: "thread-order", title: "Message order", workspace_id: "workspace-order",
    status: "active", composer_state: "ready" }, active_run: { id: "run-order", session_id: "session-order", status: "running" },
    runs: [], mission: {} } as unknown as ThreadDetailView;
  const client = { hasThreadControl: true, get: vi.fn(async () => detail),
    getPage: vi.fn(async () => ({ items: [], page: {} })), approvalQueue: vi.fn(async () => ({ items: [] })) } as unknown as APIClient;
  const queries = new QueryClient({ defaultOptions: { queries: { retry: false, staleTime: Infinity } } });
  const ui = () => <LocaleProvider><QueryClientProvider client={queries}><V2Conversation client={client}
    threadID="thread-order" workspaces={[{ id: "workspace-order", name: "Project" }] as WorkspaceView[]}
    onArchive={vi.fn()} onManageModels={vi.fn()} /></QueryClientProvider></LocaleProvider>;
  const view = render(ui());
  for (const updatedAt of ["2026-11-12T12:32:35Z", "2026-11-12T12:32:44Z"]) {
    live.snapshot = snapshot(updatedAt);
    live.snapshot.call.started_at = "2026-11-12T12:32:34Z";
    live.snapshot.text = "继续检查页面布局。";
    view.rerender(ui());
    const userInput = await screen.findByText("创建一个HTML页面");
    for (const text of [progress.detail!, "继续检查页面布局。"]) {
      expect(userInput.compareDocumentPosition(await screen.findByText(text)) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy();
    }
    expect(screen.getAllByText("创建一个HTML页面")).toHaveLength(1);
  }
  const completed = { ...task, state: "completed" as const, finished_at: "2026-11-12T12:32:45Z", final_message_id: "final-event" };
  live.snapshot = null; live.queuedIDs = [];
  live.items = [{ ...input, status: "committed", task: completed }, { ...progress, task: completed }, { ...tool, task: completed },
    item({ id: "final-event", canonical_id: "final", sequence: 140, detail: "页面已经完成。", created_at: "2026-11-12T12:32:45Z", task: completed })];
  view.rerender(ui());
  expect(await screen.findByText("创建一个HTML页面")).toBeVisible();
  expect(screen.getByText("创建一个HTML页面").compareDocumentPosition(await screen.findByText("页面已经完成。")) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy();
  expect(screen.getAllByText("创建一个HTML页面")).toHaveLength(1);
  expect(screen.getByRole("button", { name: /用时/u })).toHaveAttribute("aria-expanded", "false");
});
