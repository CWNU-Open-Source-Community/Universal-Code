import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { useState } from "react";
import { APIRequestError, APIClient } from "../../api/client";
import type { ThreadDetailView, ThreadTranscriptItemView, WorkspaceView } from "../../api/types";
import { V2Conversation } from "./conversation";
import { v2FileReferenceKey, type V2FileReference } from "./file-context";

vi.mock("../../hooks/use-run-event-stream", () => ({ useRunEventStream: () => ({ error: null, frames: [] }) }));
vi.mock("../../hooks/use-public-model-stream", () => ({ usePublicModelStream: () => ({ error: null, snapshot: null, status: "waiting" }) }));
vi.mock("./permission-control", () => ({ V2PermissionControl: () => null }));
vi.mock("./run-network-authority-control", () => ({ V2RunNetworkAuthorityControl: () => null }));
vi.mock("./model-route-control", () => ({ V2ModelRouteControl: () => null }));
vi.mock("./agent-browser", () => ({ V2AgentBrowser: () => null }));

const workspaces = [{ id: "workspace-1", name: "Workspace" }] as WorkspaceView[];

function recoveredThread(threadID: string): ThreadDetailView {
  return { thread: { id: threadID, title: `Conversation ${threadID}`, status: "active",
    workspace_id: "workspace-1", composer_state: "ready" },
    last_run: { id: `run-${threadID}`, status: "completed" }, runs: [], mission: {},
  } as unknown as ThreadDetailView;
}

function renderRecovery(submitThreadTurn: ReturnType<typeof vi.fn>, overrides: Partial<APIClient> = {}) {
  const client = { hasThreadControl: true, submitThreadTurn,
    get: vi.fn((path: string) => Promise.resolve(recoveredThread(path.split("/").at(-1)!))),
    getPage: vi.fn().mockResolvedValue({ items: [], page: { limit: 100 }, requestID: "fixture" }),
    ...overrides,
  } as unknown as APIClient;
  const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false, staleTime: Infinity }, mutations: { retry: false } } });
  function Harness() {
    const [threadID, setThreadID] = useState("thread-a");
    const [drafts, setDrafts] = useState<Record<string, string>>({});
    return <QueryClientProvider client={queryClient}>
      <button onClick={() => setThreadID("thread-b")} type="button">Open other task</button>
      <V2Conversation client={client} threadID={threadID} workspaces={workspaces}
        onArchive={vi.fn()} onManageModels={vi.fn()} onOpenInspector={vi.fn()}
        draft={drafts[threadID] ?? ""} onDraftChange={(next, expected) => setDrafts((current) =>
          expected === undefined || current[threadID] === expected ? { ...current, [threadID]: next } : current)} />
    </QueryClientProvider>;
  }
  return { ...render(<Harness />), queryClient };
}

async function submitOriginal(user: ReturnType<typeof userEvent.setup>) {
  const input = await screen.findByRole("textbox", { name: "继续对话" });
  await user.type(input, "Original request");
  await user.click(screen.getByRole("button", { name: "发送消息" }));
  return input;
}

function failedTurn(message = "This turn could not complete") {
  return new APIRequestError(message, "UNAVAILABLE", 503, "failed-turn", undefined, undefined, true);
}

function failureItem(sourceRef: string, sequence: number, detail: string): ThreadTranscriptItemView {
  return { version: "thread_transcript.v1", id: `event-${sequence}`, canonical_id: `event-${sequence}`,
    run_id: "run-thread-a", run_ordinal: 1, sequence, source_ref: sourceRef, activity_type: "message",
    stage: "blocked", kind: "harness_status", source: "harness", title: "本轮执行失败", detail,
    status: "failed", durable: true, provisional: false, verifiable: true,
    instruction_authorized: false, created_at: "2026-09-10T12:00:00Z" };
}

it("shows the exact approval failure once while retaining another identical historical failure", async () => {
  const detail = "审批已保存，后续执行因模型没有返回有效答复而结束。";
  const transcript = [failureItem("older-handoff", 40, detail), failureItem("current-handoff", 42, detail)];
  const getPage = vi.fn().mockResolvedValue({ items: transcript, page: { limit: 100 }, requestID: "failure-notices" });
  renderRecovery(vi.fn(), { getPage, get: vi.fn().mockResolvedValue({ ...recoveredThread("thread-a"),
    recovery: { version: "thread_run_recovery.v1", run_id: "run-thread-a", handoff_operation_id: "current-handoff",
      error_code: "failed_precondition", stop_reason: "failed_precondition", detail,
      quiescent: true, failed_at: "2026-09-10T12:00:00Z" } }) });
  await screen.findByRole("textbox", { name: "继续对话" });
  await waitFor(() => expect(screen.getAllByText(detail)).toHaveLength(2));
  expect(screen.getByText("审批已保存，后续执行已停止")).toBeInTheDocument();
  expect(screen.getAllByText(detail).filter((node) => node.tagName === "LI")).toHaveLength(1);
  expect(screen.queryByRole("button", { name: /解除暂停|重试核对/ })).not.toBeInTheDocument();
});

it("replaces a submission's generic error only after its exact durable notice loads", async () => {
  const explanation = "本轮执行因模型没有返回有效答复而结束。";
  let transcript: ThreadTranscriptItemView[] = [];
  const getPage = vi.fn().mockImplementation(() => Promise.resolve({ items: transcript, page: { limit: 100 }, requestID: "exact-failure" }));
  const submit = vi.fn().mockImplementationOnce(() => {
    transcript = [failureItem("failed-message", 42, explanation)];
    throw new APIRequestError("empty reply", "FAILED_PRECONDITION", 412, "failed-response", undefined, undefined, true,
      { thread_id: "thread-a", run_id: "run-thread-a", message_id: "failed-message", event_sequence: 42 });
  }).mockResolvedValueOnce({ steering: { id: "next-message" } });
  renderRecovery(submit, { getPage });
  const user = userEvent.setup();
  const input = await submitOriginal(user);
  await screen.findByText(explanation);
  await waitFor(() => expect(screen.queryByText(/本轮执行未完成/)).not.toBeInTheDocument());
  expect(screen.getAllByText(explanation)).toHaveLength(1);
  expect(within(input.closest("form")!).queryByRole("alert")).not.toBeInTheDocument();
  await waitFor(() => expect(input).toHaveValue(""));
  expect(screen.getByRole("button", { name: "发送消息" })).toBeDisabled();
  expect(screen.queryByRole("button", { name: "重试核对" })).not.toBeInTheDocument();
  fireEvent.change(input, { target: { value: "Continue with the correction" } });
  await user.click(screen.getByRole("button", { name: "发送消息" }));
  await waitFor(() => expect(input).toHaveValue(""));
  expect(submit.mock.calls[1]![2]).not.toBe(submit.mock.calls[0]![2]);
  expect(screen.getAllByText(explanation)).toHaveLength(1);
});

it.each(["absent", "different-message", "different-sequence"])("keeps the local explanation when a failure reference is %s in the loaded transcript", async (scenario) => {
  const transcript = scenario === "absent" ? [] : [failureItem(scenario === "different-message" ? "older-message" : "failed-message",
    scenario === "different-sequence" ? 41 : 42, "An earlier failure")];
  const submit = vi.fn().mockRejectedValue(new APIRequestError("specific current failure", "FAILED_PRECONDITION", 412,
    "failed-response", undefined, undefined, true,
    { thread_id: "thread-a", run_id: "run-thread-a", message_id: "failed-message", event_sequence: 42 }));
  renderRecovery(submit, { getPage: vi.fn().mockResolvedValue({ items: transcript, page: { limit: 100 }, requestID: "unmatched-failure" }) });
  const input = await submitOriginal(userEvent.setup());
  await screen.findByText(/本轮执行未完成/);
  expect(screen.getByRole("alert")).toHaveTextContent("specific current failure");
  expect(within(input.closest("form")!).queryByRole("alert")).not.toBeInTheDocument();
  await waitFor(() => expect(input).toHaveValue(""));
  expect(submit).toHaveBeenCalledTimes(1);
});

it("preserves a newer draft and new file selection when the current input has a sealed failed outcome", async () => {
  let reject!: (reason: unknown) => void;
  const submit = vi.fn().mockImplementationOnce(() => new Promise((_resolve, fail) => { reject = fail; }));
  const { queryClient } = renderRecovery(submit);
  const key = v2FileReferenceKey("workspace-1", "thread-a");
  const original: V2FileReference = { id: "original-file", path: "README.md", digest: "a".repeat(64), partial: false, redacted: false };
  const later: V2FileReference = { ...original, id: "later-file", path: "notes.md" };
  await act(async () => { queryClient.setQueryData(key, [original]); });
  const input = await submitOriginal(userEvent.setup());
  expect(submit.mock.calls[0]![1]).toMatchObject({ files: [{ source_kind: "workspace_file", path: "README.md", expected_sha256: original.digest }] });
  fireEvent.change(input, { target: { value: "A newer unsent draft" } });
  await act(async () => { queryClient.setQueryData(key, [original, later]); });
  await act(async () => reject(new APIRequestError("sealed execution failure", "FAILED_PRECONDITION", 412,
    "failed-response", undefined, undefined, true,
    { thread_id: "thread-a", run_id: "run-thread-a", message_id: "original-message", event_sequence: 42 })));
  await waitFor(() => expect(queryClient.getQueryData(key)).toEqual([later]));
  expect(input).toHaveValue("A newer unsent draft");
  expect(screen.getByRole("alert")).toHaveTextContent("sealed execution failure");
  expect(submit).toHaveBeenCalledTimes(1);
});

it("does not clear a draft for a different Thread's sealed failure reference", async () => {
  const submit = vi.fn().mockRejectedValue(new APIRequestError("wrong Thread outcome", "FAILED_PRECONDITION", 412,
    "failed-response", undefined, undefined, true,
    { thread_id: "thread-other", run_id: "run-other", message_id: "other-message", event_sequence: 42 }));
  renderRecovery(submit);
  const input = await submitOriginal(userEvent.setup());
  await screen.findByText(/本轮执行未完成/);
  expect(input).toHaveValue("Original request");
  expect(submit).toHaveBeenCalledTimes(1);
});

it("does not treat a sealed original-key confirmation as acceptance of a newer request", async () => {
  let rejectOriginal!: (reason: unknown) => void;
  let rejectNew!: (reason: unknown) => void;
  const submit = vi.fn().mockRejectedValueOnce(new Error("original response lost"))
    .mockRejectedValueOnce(new Error("automatic confirmation lost"))
    .mockImplementationOnce(() => new Promise((_resolve, fail) => { rejectOriginal = fail; }))
    .mockImplementationOnce(() => new Promise((_resolve, fail) => { rejectNew = fail; }));
  renderRecovery(submit);
  const user = userEvent.setup();
  const input = await submitOriginal(user);
  await screen.findByRole("button", { name: "重试核对" });
  fireEvent.change(input, { target: { value: "A new requirement" } });
  await user.click(screen.getByRole("button", { name: "发送消息" }));
  await waitFor(() => expect(submit).toHaveBeenCalledTimes(3));
  await act(async () => rejectOriginal(new APIRequestError("original turn sealed", "FAILED_PRECONDITION", 412,
    "original-response", undefined, undefined, true,
    { thread_id: "thread-a", run_id: "run-thread-a", message_id: "original-message", event_sequence: 42 })));
  await waitFor(() => expect(submit).toHaveBeenCalledTimes(4));
  expect(submit.mock.calls[2]).toEqual(submit.mock.calls[0]);
  expect(submit.mock.calls[3]![2]).not.toBe(submit.mock.calls[0]![2]);
  expect(input).toHaveValue("A new requirement");
  await act(async () => rejectNew(failedTurn("new request failure without sealed reference")));
  await screen.findByText("new request failure without sealed reference");
  expect(input).toHaveValue("A new requirement");
});

it("accepts a prepared input awaiting tool approval without duplicating its draft or submission", async () => {
  const thread = { id: "thread-a", protocol_version: "thread.v1", title: "Read documentation",
    mission_id: "mission-a", workspace_id: "workspace-1", status: "active",
    active_run_id: "run-thread-a", last_run_id: "run-thread-a", version: 1,
    composer_state: "waiting_approval", created_at: "2026-09-10T04:01:45Z",
    updated_at: "2026-09-10T04:01:45Z" };
  const response = { version: "thread_message_submission.v1", thread,
    run_id: "run-thread-a", session_id: "session-a", successor_created: false,
    steering: { id: "steering-a", sequence: 1, status: "pending", prepared: true,
      created_at: "2026-09-10T04:01:45Z" }, replayed: false,
    execution_started: true, model_called: true, tool_called: true, capability_grant: false };
  const fetchMock = vi.fn().mockResolvedValue(new Response(JSON.stringify({
    version: "api.v1", request_id: "accepted-prepared-input", data: response,
  }), { status: 202, headers: { "Content-Type": "application/json" } }));
  vi.stubGlobal("fetch", fetchMock);
  const transport = new APIClient("read-secret", "/api/v1", "control-secret");
  const submit = vi.fn(transport.submitThreadTurn.bind(transport));
  const waiting = { ...recoveredThread("thread-a"), thread,
    active_run: { id: "run-thread-a", status: "waiting_approval" } } as unknown as ThreadDetailView;
  renderRecovery(submit, { get: vi.fn().mockResolvedValue(waiting) });
  const input = await submitOriginal(userEvent.setup());
  await waitFor(() => expect(input).toHaveValue(""));
  expect(submit).toHaveBeenCalledTimes(1);
  expect(fetchMock).toHaveBeenCalledTimes(1);
  expect(screen.queryByText(/暂时无法确认这条消息的结果/)).not.toBeInTheDocument();
  expect(screen.queryByRole("button", { name: "重试核对" })).not.toBeInTheDocument();
});

it("continues a paused task by sending a message without a separate lifecycle action", async () => {
  const submit = vi.fn().mockResolvedValue({ steering: { id: "next-message", status: "committed" } });
  const controlRunLifecycle = vi.fn();
  const paused = { ...recoveredThread("thread-a"), active_run: { id: "run-thread-a", status: "paused" } };
  renderRecovery(submit, { hasRunLifecycle: true, controlRunLifecycle,
    get: vi.fn().mockResolvedValue(paused) });
  const user = userEvent.setup();
  const input = await screen.findByRole("textbox", { name: "继续对话" });
  expect(screen.getByText("已暂停", { exact: true })).toBeInTheDocument();
  expect(screen.getByText("发送消息即可继续，也可以仅解除暂停。")).toBeInTheDocument();
  await user.type(input, "Continue with this requirement");
  expect(screen.getByRole("button", { name: "发送消息" })).toBeEnabled();
  await user.click(screen.getByRole("button", { name: "发送消息" }));
  await waitFor(() => expect(input).toHaveValue(""));
  expect(submit).toHaveBeenCalledTimes(1);
  expect(controlRunLifecycle).not.toHaveBeenCalled();
});

it.each(["stopping", "stop_failed"] as const)("keeps a draft editable while %s prevents a new submission", async (state) => {
  const submit = vi.fn();
  renderRecovery(submit, { hasThreadExecutionRead: true,
    threadExecution: vi.fn().mockResolvedValue({ version: "thread_execution.v1", thread_id: "thread-a",
      state, execution_id: "execution-a", queued_messages: 1, capability_grant: false }) });
  const user = userEvent.setup();
  const input = await screen.findByRole("textbox", { name: "继续对话" });
  await user.type(input, "Next requirement");
  await screen.findByText(state === "stopping"
    ? "正在停止执行。可以继续编辑，停止完成后再发送。"
    : "停止尚未完成。请重试停止，确认后再发送；已受理的要求会保留。");
  expect(input).toHaveValue("Next requirement");
  expect(screen.getByRole("button", { name: "发送消息" })).toBeDisabled();
  expect(submit).not.toHaveBeenCalled();
});

it("lets a known failed turn continue through the ordinary composer with a fresh key", async () => {
  const submit = vi.fn().mockRejectedValueOnce(failedTurn()).mockResolvedValueOnce({ steering: { id: "next-message" } });
  renderRecovery(submit);
  const user = userEvent.setup();
  const input = await submitOriginal(user);
  await screen.findByText(/本轮执行未完成/);
  expect(submit).toHaveBeenCalledTimes(1);
  expect(screen.queryByRole("button", { name: "重试核对" })).not.toBeInTheDocument();
  fireEvent.change(input, { target: { value: "Continue with this new requirement" } });
  await user.click(screen.getByRole("button", { name: "发送消息" }));
  await waitFor(() => expect(input).toHaveValue(""));
  expect(submit).toHaveBeenCalledTimes(2);
  expect(submit.mock.calls[1]![1]).toMatchObject({ content: "Continue with this new requirement" });
  expect(submit.mock.calls[1]![2]).not.toBe(submit.mock.calls[0]![2]);
  expect(screen.queryByText(/本轮执行未完成/)).not.toBeInTheDocument();
  expect(within(input.closest("form")!).queryByRole("alert")).not.toBeInTheDocument();
});

it("automatically confirms a lost response with its original key and preserves an edited draft on the same page", async () => {
  let confirm!: (result: unknown) => void;
  const submit = vi.fn().mockRejectedValueOnce(new TypeError("Failed to fetch"))
    .mockImplementationOnce(() => new Promise((resolve) => { confirm = resolve; }));
  renderRecovery(submit);
  const user = userEvent.setup();
  const input = await submitOriginal(user);
  await screen.findByText(/正在核对上次提交/);
  fireEvent.change(input, { target: { value: "A newer draft" } });
  expect(screen.getByRole("button", { name: "发送消息" })).toBeDisabled();
  await waitFor(() => expect(submit).toHaveBeenCalledTimes(2));
  await act(async () => confirm({ steering: { id: "original-message" }, replayed: true }));
  await waitFor(() => expect(screen.queryByText(/正在核对上次提交/)).not.toBeInTheDocument());
  expect(submit.mock.calls[1]).toEqual(submit.mock.calls[0]);
  expect(screen.getByRole("textbox", { name: "继续对话" })).toBe(input);
  expect(input).toHaveValue("A newer draft");
  expect(within(input.closest("form")!).queryByRole("alert")).not.toBeInTheDocument();
});

it("automatically resolves an unknown response to a known failure without forcing a recovery action", async () => {
  const submit = vi.fn().mockRejectedValueOnce(new Error("response lost"))
    .mockRejectedValueOnce(failedTurn()).mockResolvedValueOnce({ steering: { id: "next-message" } });
  renderRecovery(submit);
  const user = userEvent.setup();
  const input = await submitOriginal(user);
  await screen.findByText(/本轮执行未完成/);
  expect(submit.mock.calls[1]).toEqual(submit.mock.calls[0]);
  fireEvent.change(input, { target: { value: "Continue" } });
  await user.click(screen.getByRole("button", { name: "发送消息" }));
  await waitFor(() => expect(input).toHaveValue(""));
  expect(submit).toHaveBeenCalledTimes(3);
  expect(submit.mock.calls[2]![2]).not.toBe(submit.mock.calls[0]![2]);
});

it("checks the original unknown intent before sending an edited requirement and does not duplicate an accepted original", async () => {
  let confirm!: (result: unknown) => void;
  const submit = vi.fn().mockRejectedValueOnce(new Error("first response lost"))
    .mockRejectedValueOnce(new Error("automatic confirmation lost"))
    .mockImplementationOnce(() => new Promise((resolve) => { confirm = resolve; }))
    .mockResolvedValueOnce({ steering: { id: "new-message" } });
  renderRecovery(submit);
  const user = userEvent.setup();
  const input = await submitOriginal(user);
  await screen.findByRole("button", { name: "重试核对" });
  fireEvent.change(input, { target: { value: "Changed requirement" } });
  await user.click(screen.getByRole("button", { name: "发送消息" }));
  await waitFor(() => expect(submit).toHaveBeenCalledTimes(3));
  expect(submit.mock.calls[2]).toEqual(submit.mock.calls[0]);
  expect(submit.mock.calls.every((call) => call[1].content === "Original request")).toBe(true);
  fireEvent.change(input, { target: { value: "A still newer draft" } });
  await act(async () => confirm({ steering: { id: "original-message" }, replayed: true }));
  await waitFor(() => expect(submit).toHaveBeenCalledTimes(4));
  expect(submit.mock.calls[3]![1]).toMatchObject({ content: "Changed requirement" });
  expect(submit.mock.calls[3]![2]).not.toBe(submit.mock.calls[0]![2]);
  expect(input).toHaveValue("A still newer draft");
});

it("retains the edited request without sending it when automatic confirmation remains unknown", async () => {
  const submit = vi.fn().mockRejectedValue(new Error("connection unavailable"));
  renderRecovery(submit);
  const user = userEvent.setup();
  const input = await submitOriginal(user);
  await screen.findByRole("button", { name: "重试核对" });
  expect(submit).toHaveBeenCalledTimes(2);
  fireEvent.change(input, { target: { value: "New unsent draft" } });
  await user.click(screen.getByRole("button", { name: "发送消息" }));
  await waitFor(() => expect(screen.getByRole("button", { name: "重试核对" })).toBeEnabled());
  expect(submit).toHaveBeenCalledTimes(4);
  expect(submit.mock.calls.every((call) => JSON.stringify(call) === JSON.stringify(submit.mock.calls[0]))).toBe(true);
  expect(input).toHaveValue("New unsent draft");
  expect(screen.getByRole("alert")).toHaveTextContent("connection unavailable");
  expect(within(input.closest("form")!).queryByRole("alert")).not.toBeInTheDocument();
});

it("clears a stale Composer error after repeated original-key confirmation eventually succeeds", async () => {
  const first = new APIRequestError("generic precondition response", "FAILED_PRECONDITION", 412, "old-server");
  const submit = vi.fn().mockRejectedValueOnce(first).mockRejectedValueOnce(new Error("automatic recheck lost"))
    .mockRejectedValueOnce(new Error("manual recheck lost"))
    .mockResolvedValueOnce({ steering: { id: "original-message" }, replayed: true });
  renderRecovery(submit);
  const user = userEvent.setup();
  const input = await submitOriginal(user);
  const form = input.closest("form")!;
  await screen.findByRole("button", { name: "重试核对" });
  expect(screen.getByRole("alert")).toHaveTextContent("automatic recheck lost");
  expect(within(form).queryByRole("alert")).not.toBeInTheDocument();
  await user.click(screen.getByRole("button", { name: "重试核对" }));
  await waitFor(() => expect(submit).toHaveBeenCalledTimes(4));
  await waitFor(() => expect(within(form).queryByRole("alert")).not.toBeInTheDocument());
  expect(screen.queryByRole("button", { name: "重试核对" })).not.toBeInTheDocument();
  expect(screen.getByRole("textbox", { name: "继续对话" })).toBe(input);
  expect(submit).toHaveBeenCalledTimes(4);
  expect(submit.mock.calls.every((call) => JSON.stringify(call) === JSON.stringify(submit.mock.calls[0]))).toBe(true);
});

it("attributes an old confirmation error to its original key even when Send was clicked with a new draft", async () => {
  const submit = vi.fn().mockRejectedValueOnce(new Error("original response lost"))
    .mockRejectedValueOnce(new Error("automatic confirmation lost"))
    .mockRejectedValueOnce(new Error("Thread message response violated its continuation contract"))
    .mockRejectedValueOnce(new Error("Thread message response violated its continuation contract"))
    .mockResolvedValueOnce({ steering: { id: "original-message" }, replayed: true })
    .mockRejectedValue(new Error("The new request has a different failure"));
  renderRecovery(submit);
  const user = userEvent.setup();
  const input = await submitOriginal(user);
  const form = input.closest("form")!;
  await screen.findByRole("button", { name: "重试核对" });
  fireEvent.change(input, { target: { value: "New unsent draft" } });
  await user.click(screen.getByRole("button", { name: "发送消息" }));
  await waitFor(() => expect(screen.getByRole("button", { name: "重试核对" })).toBeEnabled());
  expect(submit).toHaveBeenCalledTimes(4);
  expect(screen.getByRole("alert")).toHaveTextContent("Thread message response violated its continuation contract");
  expect(within(form).queryByRole("alert")).not.toBeInTheDocument();
  expect(input).toHaveValue("New unsent draft");

  await user.click(screen.getByRole("button", { name: "重试核对" }));
  await waitFor(() => expect(screen.queryByRole("button", { name: "重试核对" })).not.toBeInTheDocument());
  await waitFor(() => expect(within(form).queryByRole("alert")).not.toBeInTheDocument());
  expect(screen.getByRole("textbox", { name: "继续对话" })).toBe(input);
  expect(input).toHaveValue("New unsent draft");
  expect(submit).toHaveBeenCalledTimes(5);
  expect(submit.mock.calls.every((call) => JSON.stringify(call) === JSON.stringify(submit.mock.calls[0]))).toBe(true);

  await user.click(screen.getByRole("button", { name: "发送消息" }));
  await screen.findByRole("button", { name: "重试核对" });
  expect(submit).toHaveBeenCalledTimes(7);
  expect(submit.mock.calls[5]![2]).not.toBe(submit.mock.calls[0]![2]);
  expect(submit.mock.calls[5]![1]).toMatchObject({ content: "New unsent draft" });
  expect(screen.getByRole("alert")).toHaveTextContent("The new request has a different failure");
  expect(within(form).queryByRole("alert")).not.toBeInTheDocument();
  expect(input).toHaveValue("New unsent draft");
});

it("does not clear another task's error or draft when an automatic confirmation returns late", async () => {
  let confirm!: (result: unknown) => void;
  const submit = vi.fn().mockRejectedValueOnce(new Error("response lost"))
    .mockImplementationOnce(() => new Promise((resolve) => { confirm = resolve; }))
    .mockRejectedValueOnce(failedTurn("Other task failed"));
  renderRecovery(submit);
  const user = userEvent.setup();
  await submitOriginal(user);
  await waitFor(() => expect(submit).toHaveBeenCalledTimes(2));
  await user.click(screen.getByRole("button", { name: "Open other task" }));
  await screen.findByText("Conversation thread-b");
  const otherInput = screen.getByRole("textbox", { name: "继续对话" });
  await user.type(otherInput, "Original request");
  await user.click(screen.getByRole("button", { name: "发送消息" }));
  const otherForm = otherInput.closest("form")!;
  const otherError = await screen.findByRole("alert");
  expect(otherError).toHaveTextContent("本轮执行未完成，失败记录与已完成的修改会保留。可以直接发送“继续”或补充要求。");
  expect(within(otherForm).queryByRole("alert")).not.toBeInTheDocument();
  await act(async () => confirm({ steering: { id: "original-message" }, replayed: true }));
  expect(screen.getByRole("alert")).toBe(otherError);
  expect(otherError).toHaveTextContent("Other task failed");
  expect(otherInput).toHaveValue("Original request");
  expect(submit.mock.calls[0]![0]).toBe("thread-a");
  expect(submit.mock.calls[2]![0]).toBe("thread-b");
});

// Retain textarea coverage through the supported plain-text editing mode.
beforeEach(() => localStorage.setItem("universal-code.composer-plain-text", "true"));
