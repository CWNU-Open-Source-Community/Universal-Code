import type { ReactElement } from "react";
import type { RenderOptions } from "@testing-library/react";
import { LocaleProvider } from "../../lib/locale";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act, cleanup, render as renderComponent, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { APIRequestError, type APIClient } from "../../api/client";
import type { ThreadCompactionOperation, ThreadCompactionOperations, ThreadContextPins } from "../../api/thread-context-controls";
import { v2QueryKeys } from "../query-keys";
import { V2ThreadCompactControl, V2ThreadPinPrompt, V2ThreadPinsPanel } from "./thread-context-controls";

const when = "2026-10-17T00:00:00Z";
const proposal = { id: "proposal-1", version: 1, status: "pending" as const,
  text: "整个任务不得修改 billing.go。", question: "后续压缩时是否始终保留这项要求？", run_id: "run-1", created_at: when };
function fixture(pending = false) {
  const state: ThreadContextPins = { version: "thread_context_pins.v1", thread_id: "thread-1", revision: 0,
    busy: false, effective_from: "next_request", pins: [], proposals: pending ? [proposal] : [] };
  const operations: ThreadCompactionOperations = { version: "thread_context_compaction_operations.v1", thread_id: "thread-1", operations: [] };
  const get = vi.fn(async (path: string) => path.endsWith("/compactions") ? { ...operations } : state);
  const postControl = vi.fn(); const reply = vi.fn().mockResolvedValue(undefined);
  const client = { baseURL: "/api/v1", hasControl: true, get, postControl } as unknown as APIClient;
  const queries = new QueryClient({ defaultOptions: { queries: { retry: false, staleTime: Infinity } } });
  const mount = (child: React.ReactNode) => render(<QueryClientProvider client={queries}>{child}</QueryClientProvider>);
  return { state, operations, client, queries, get, postControl, reply, mount };
}
afterEach(cleanup);

test("user pins exact text, edits it, and removes it with revision checks", async () => {
  const f = fixture(); const user = userEvent.setup();
  f.postControl.mockImplementation(async (_path, body) => {
    f.state.revision++;
    if (body.action === "remove") f.state.pins = [];
    else f.state.pins = [{ id: "pin-1", text: body.text, version: f.state.revision, content_sha256: "a".repeat(64),
      source_kind: "operator", created_at: when, updated_at: when }];
    return { state: { ...f.state }, replayed: false };
  });
  f.mount(<V2ThreadPinsPanel client={f.client} threadID="thread-1" />);
  await screen.findByText("还没有固定要求。");
  await user.type(screen.getByLabelText("添加固定要求"), proposal.text);
  await user.click(screen.getByRole("button", { name: "固定到本次任务" }));
  expect(await screen.findByText(proposal.text)).toBeVisible();
  expect(f.postControl.mock.calls[0][1]).toEqual({ version: "thread_context_pins.v1", action: "add", expected_revision: 0, text: proposal.text });
  await user.click(screen.getByRole("button", { name: "编辑" }));
  await user.clear(screen.getByLabelText("编辑固定要求"));
  await user.type(screen.getByLabelText("编辑固定要求"), "改为仅检查 billing.go。");
  await user.click(screen.getByRole("button", { name: "保存修改" }));
  expect(await screen.findByText("改为仅检查 billing.go。")).toBeVisible();
  expect(f.postControl.mock.calls[1][1]).toMatchObject({ action: "edit", expected_revision: 1, pin_id: "pin-1", expected_pin_version: 1 });
  await user.click(screen.getByRole("button", { name: "取消固定" }));
  expect(await screen.findByText("还没有固定要求。")).toBeVisible();
});

test.each([false, true])("custom answer pins only when explicitly selected (%s)", async (pinCustom) => {
  const f = fixture(true); const user = userEvent.setup();
  f.postControl.mockImplementation(async () => ({ state: { ...f.state, revision: 1, proposals: [{ ...proposal, status: "resolved" }] },
    reply_text: "请先问我再修改。", reply_operation_key: "confirmed-pin-answer-1", replayed: false }));
  f.mount(<V2ThreadPinPrompt client={f.client} threadID="thread-1" paused={false} onReply={f.reply} />);
  expect(await screen.findByRole("region", { name: proposal.question })).toBeVisible();
  expect(screen.getByText(proposal.text)).toBeVisible(); expect(f.postControl).not.toHaveBeenCalled();
  await user.click(screen.getByRole("textbox", { name: "你的回复" }));
  expect(screen.getByRole("checkbox")).not.toBeChecked();
  await user.type(screen.getByLabelText("你的回复"), "请先问我再修改。");
  if (pinCustom) await user.click(screen.getByRole("checkbox"));
  await user.click(screen.getByRole("button", { name: "发送" }));
  await waitFor(() => expect(f.reply).toHaveBeenCalledWith("请先问我再修改。", "confirmed-pin-answer-1"));
  expect(f.postControl.mock.calls[0][1]).toMatchObject({ choice: "custom", text: "请先问我再修改。", pin_custom: pinCustom });
  await waitFor(() => expect(screen.queryByRole("region", { name: proposal.question })).not.toBeInTheDocument());
});

test("closing the question dismisses it without pinning or sending a reply", async () => {
  const f = fixture(true); const user = userEvent.setup();
  f.postControl.mockResolvedValue({ state: { ...f.state, proposals: [{ ...proposal, status: "dismissed" }] }, replayed: false });
  f.mount(<V2ThreadPinPrompt client={f.client} threadID="thread-1" paused={false} onReply={f.reply} />);
  await screen.findByRole("region", { name: proposal.question }); await user.click(screen.getByLabelText("关闭固定要求提问"));
  await waitFor(() => expect(screen.queryByRole("region", { name: proposal.question })).not.toBeInTheDocument());
  expect(f.postControl.mock.calls[0][1]).toMatchObject({ choice: "dismiss" }); expect(f.reply).not.toHaveBeenCalled();
});

test("question stays inline, permits normal Tab navigation, and sends the explicitly selected answer", async () => {
  const f = fixture(true); const user = userEvent.setup(); const closed = vi.fn();
  f.postControl.mockResolvedValue({ state: { ...f.state, proposals: [{ ...proposal, status: "resolved" }] },
    reply_text: "仅当前讨论。", reply_operation_key: "confirmed-pin-answer-1", replayed: false });
  const view = f.mount(<><V2ThreadPinPrompt client={f.client} threadID="thread-1" paused={false}
    onReply={f.reply} onClose={closed} /><textarea aria-label="继续对话" /></>);
  const card = await screen.findByRole("region", { name: proposal.question });
  expect(view.container).toContainElement(card);
  expect(card).not.toHaveAttribute("aria-modal");
  expect(screen.getByRole("textbox", { name: "继续对话" }).closest("[inert]")).toBeNull();
  await user.click(screen.getByRole("textbox", { name: "你的回复" }));
  expect(screen.getByRole("button", { name: "发送" })).toBeDisabled();
  await user.type(screen.getByRole("textbox", { name: "你的回复" }), "另一个答复草稿");
  await user.click(screen.getByRole("radio", { name: /仅当前讨论/u }));
  await user.tab();
  expect(screen.getByRole("textbox", { name: "你的回复" })).toHaveFocus();
  expect(screen.getByRole("radio", { name: /仅当前讨论/u })).toBeChecked();
  const send = screen.getByRole("button", { name: "发送" });
  send.focus(); await user.tab();
  expect(screen.getByRole("textbox", { name: "继续对话" })).toHaveFocus();
  await user.click(send);
  await waitFor(() => expect(f.reply).toHaveBeenCalledWith("仅当前讨论。", "confirmed-pin-answer-1"));
  expect(f.postControl.mock.calls[0][1]).toMatchObject({ choice: "discussion_only" });
  expect(f.postControl.mock.calls[0][1]).not.toHaveProperty("text");
  expect(closed).toHaveBeenCalledTimes(1);
});

test("skip dismisses a custom draft without sending or pinning it", async () => {
  const f = fixture(true); const user = userEvent.setup();
  f.postControl.mockResolvedValue({ state: { ...f.state, proposals: [{ ...proposal, status: "dismissed" }] }, replayed: false });
  f.mount(<V2ThreadPinPrompt client={f.client} threadID="thread-1" paused={false} onReply={f.reply} />);
  await screen.findByRole("region", { name: proposal.question });
  await user.type(screen.getByRole("textbox", { name: "你的回复" }), "尚未确认的草稿");
  await user.click(screen.getByRole("button", { name: "跳过" }));
  await waitFor(() => expect(screen.queryByRole("region", { name: proposal.question })).not.toBeInTheDocument());
  expect(f.postControl.mock.calls[0][1]).toMatchObject({ choice: "dismiss" });
  expect(f.postControl.mock.calls[0][1]).not.toHaveProperty("text");
  expect(f.reply).not.toHaveBeenCalled();
});

test("lost resolve response reuses its original revision and operation identity after polling", async () => {
  const f = fixture(true); const user = userEvent.setup();
  f.postControl.mockRejectedValueOnce(new Error("response lost")).mockResolvedValueOnce({
    state: { ...f.state, revision: 1, proposals: [{ ...proposal, status: "resolved" }] }, reply_text: "已固定。", reply_operation_key: "confirmed-pin-answer-1", replayed: true });
  f.mount(<V2ThreadPinPrompt client={f.client} threadID="thread-1" paused={false} onReply={f.reply} />);
  await screen.findByRole("region", { name: proposal.question }); await user.click(screen.getByRole("button", { name: "发送" }));
  await screen.findByRole("alert");
  await act(async () => { f.queries.setQueryData(["thread-context-pins", f.client.baseURL, "thread-1"], {
    ...f.state, revision: 1, proposals: [{ ...proposal, status: "resolved" }],
  }); });
  await user.click(screen.getByRole("button", { name: "发送" }));
  await waitFor(() => expect(f.reply).toHaveBeenCalledTimes(1));
  expect(f.postControl.mock.calls[1]).toEqual(f.postControl.mock.calls[0]);
});

test("reply retry does not resolve the proposal again", async () => {
  const f = fixture(true); const user = userEvent.setup();
  f.reply.mockRejectedValueOnce(new Error("reply unavailable")).mockResolvedValueOnce(undefined);
  f.postControl.mockResolvedValue({ state: { ...f.state, proposals: [{ ...proposal, status: "resolved" }] },
    reply_text: "仅当前讨论。", reply_operation_key: "confirmed-pin-answer-1", replayed: false });
  f.mount(<V2ThreadPinPrompt client={f.client} threadID="thread-1" paused={false} onReply={f.reply} />);
  await screen.findByRole("region", { name: proposal.question }); await user.click(screen.getByRole("radio", { name: /仅当前讨论/u }));
  await user.click(screen.getByRole("button", { name: "发送" }));
  await screen.findByRole("alert"); await user.click(screen.getByRole("button", { name: "重试发送答复" }));
  await waitFor(() => expect(f.reply).toHaveBeenCalledTimes(2));
  expect(f.reply.mock.calls[0]).toEqual(f.reply.mock.calls[1]); expect(f.postControl).toHaveBeenCalledTimes(1);
});

test("known CAS rejection reloads state and allows a newly confirmed answer", async () => {
  const f = fixture(true); const user = userEvent.setup();
  f.postControl.mockImplementationOnce(async () => {
    f.state.revision = 2;
    throw new APIRequestError("context pins revision changed", "CONFLICT", 409);
  }).mockImplementationOnce(async () => ({ state: { ...f.state, proposals: [{ ...proposal, status: "resolved" }] },
    reply_text: "已固定。", reply_operation_key: "confirmed-pin-answer-1", replayed: false }));
  f.mount(<V2ThreadPinPrompt client={f.client} threadID="thread-1" paused={false} onReply={f.reply} />);
  await screen.findByRole("region", { name: proposal.question }); await user.click(screen.getByRole("button", { name: "发送" }));
  await screen.findByRole("alert");
  await waitFor(() => expect(screen.getByRole("button", { name: "发送" })).toBeEnabled());
  await user.click(screen.getByRole("button", { name: "发送" }));
  await waitFor(() => expect(f.reply).toHaveBeenCalledTimes(1));
  expect(f.postControl.mock.calls[1][1].expected_revision).toBe(2);
  expect(f.postControl.mock.calls[1][2]).not.toBe(f.postControl.mock.calls[0][2]);
});

test("busy state prevents compact, then idle compact binds the actual run and focus", async () => {
  const f = fixture(); f.state.busy = true; const user = userEvent.setup();
  f.postControl.mockResolvedValue({ version: "thread_context_compaction.v1", thread_id: "thread-1", run_id: "run-1", session_id: "session-1",
    status: "completed", summary_id: 3, removed_messages: 20, preserved_messages: 4, source_sha256: "b".repeat(64), generated: true, replayed: false });
  f.mount(<V2ThreadCompactControl client={f.client} threadID="thread-1" runID="run-1" />);
  await screen.findByText("任务正在执行，结束后可以压缩。"); expect(screen.getByRole("button", { name: "压缩当前对话" })).toBeDisabled();
  f.state.busy = false;
  await act(async () => { await f.queries.invalidateQueries({ queryKey: ["thread-context-pins"] }); });
  await user.type(screen.getByLabelText("压缩重点（可选）"), "保留未完成验证");
  await user.click(screen.getByRole("button", { name: "压缩当前对话" }));
  expect(await screen.findByText("已压缩 20 条较早消息，保留 4 条近期消息。")).toBeVisible();
  expect(f.postControl.mock.calls[0][1]).toEqual({ version: "thread_context_compaction.v1", expected_run_id: "run-1", focus: "保留未完成验证" });
});

test("cross-thread state never displays a proposal or enables pin mutation", async () => {
  const f = fixture(true); f.state.thread_id = "other-thread";
  f.mount(<><V2ThreadPinPrompt client={f.client} threadID="thread-1" paused={false} onReply={f.reply} />
    <V2ThreadPinsPanel client={f.client} threadID="thread-1" /></>);
  await screen.findByRole("alert"); expect(screen.queryByRole("region", { name: proposal.question })).not.toBeInTheDocument();
  expect(screen.getByRole("button", { name: "固定到本次任务" })).toBeDisabled(); expect(f.postControl).not.toHaveBeenCalled();
});

const compactionResult = { version: "thread_context_compaction.v1" as const, thread_id: "thread-1", run_id: "previous-run", session_id: "session-1",
  status: "completed" as const, summary_id: 3, removed_messages: 20, preserved_messages: 4, source_sha256: "b".repeat(64), generated: true, replayed: false };
const compactionOperation = (status: ThreadCompactionOperation["status"]): ThreadCompactionOperation => ({
  operation_id: "operation-1", run_id: "previous-run", session_id: "session-1", status, source_sha256: "b".repeat(64),
  created_at: when, usage_known: status !== "started", provider: "openai", model: "test-model",
});

test("saved generated summary recovers its own run without submitting a new compact request", async () => {
  const f = fixture(); const user = userEvent.setup();
  f.operations.operations = [compactionOperation("generated")];
  const invalidate = vi.spyOn(f.queries, "invalidateQueries");
  f.postControl.mockImplementation(async () => {
    const completed = { ...compactionOperation("completed"), terminal_at: when, result: compactionResult };
    f.operations.operations = [completed]; return completed;
  });
  f.mount(<V2ThreadCompactControl client={f.client} threadID="thread-1" runID="current-run" />);
  expect(await screen.findByText("摘要已生成，待采用")).toBeVisible();
  expect(screen.getByRole("button", { name: "压缩当前对话" })).toBeDisabled();
  expect(screen.getByText("恢复会校验对话版本并采用已保存的摘要，无需再次请求模型。")).toBeVisible();
  await user.click(screen.getByRole("button", { name: "恢复已生成摘要" }));
  await waitFor(() => expect(invalidate).toHaveBeenCalledWith({ queryKey: v2QueryKeys.thread("thread-1") }));
  expect(f.postControl).toHaveBeenCalledExactlyOnceWith("/threads/thread-1/context/compactions/operation-1/recover",
    { version: "thread_context_compaction_recovery.v1", expected_run_id: "previous-run", action: "resume" }, "thread-context-recover-operation-1-resume");
  expect(invalidate).toHaveBeenCalledWith({ queryKey: ["thread-context", f.client.baseURL, "thread-1"] });
  expect(invalidate).toHaveBeenCalledWith({ queryKey: ["thread-context-pins", f.client.baseURL, "thread-1"] });
  expect(invalidate).toHaveBeenCalledWith({ queryKey: ["thread-context-compactions", f.client.baseURL, "thread-1"] });
  expect(await screen.findByRole("button", { name: "压缩当前对话" })).toBeEnabled();
});

test("started operation can be discarded while a task is busy and retries preserve recovery identity", async () => {
  const f = fixture(); f.state.busy = true; const user = userEvent.setup();
  f.operations.operations = [compactionOperation("started")];
  f.postControl.mockRejectedValueOnce(new Error("response lost")).mockImplementationOnce(async () => {
    const discarded = { ...compactionOperation("failed"), usage_known: false, error_message: "operator discarded", terminal_at: when };
    f.operations.operations = [discarded]; return discarded;
  });
  f.mount(<V2ThreadCompactControl client={f.client} threadID="thread-1" runID="current-run" />);
  expect(await screen.findByText("模型请求已发出，可能仍在运行。可刷新记录核对进度。")).toBeVisible();
  expect(screen.getByText("放弃将停止采用本次结果；已发出的模型请求仍可能计费。")).toBeVisible();
  expect(screen.getByText("本次用量尚未确认。")).toBeVisible();
  expect(screen.queryByRole("button", { name: "恢复已生成摘要" })).not.toBeInTheDocument();
  await user.click(screen.getByRole("button", { name: "放弃本次压缩" }));
  await screen.findByRole("alert");
  await user.click(screen.getByRole("button", { name: "放弃本次压缩" }));
  expect(await screen.findByText("已停止采用本次结果。")).toBeVisible();
  expect(f.postControl.mock.calls[1]).toEqual(f.postControl.mock.calls[0]);
  expect(f.postControl.mock.calls[0][1]).toEqual({ version: "thread_context_compaction_recovery.v1", expected_run_id: "previous-run", action: "discard" });
  expect(screen.getByRole("button", { name: "压缩当前对话" })).toBeDisabled();
});

test("compact conflict reloads a pending operation and exposes recovery instead of another model request", async () => {
  const f = fixture(); const user = userEvent.setup();
  f.postControl.mockImplementationOnce(async () => {
    f.operations.operations = [compactionOperation("generated")];
    throw new APIRequestError("compaction pending", "CONFLICT", 409);
  });
  f.mount(<V2ThreadCompactControl client={f.client} threadID="thread-1" runID="current-run" />);
  const compact = screen.getByRole("button", { name: "压缩当前对话" });
  await waitFor(() => expect(compact).toBeEnabled()); await user.click(compact);
  expect(await screen.findByRole("button", { name: "恢复已生成摘要" })).toBeEnabled();
  expect(compact).toBeDisabled(); await user.click(compact);
  expect(f.postControl).toHaveBeenCalledTimes(1);
});

test.each([
  new APIRequestError("thread_context_operation_pending: request is unresolved", "CONFLICT", 409),
  new APIRequestError("thread_context_busy: another operation is pending", "CONFLICT", 409),
  new Error("response lost"),
])("compact retry retains its request identity for an uncertain outcome: %s", async (failure) => {
  const f = fixture(); const user = userEvent.setup();
  f.postControl.mockRejectedValueOnce(failure)
    .mockResolvedValueOnce({ ...compactionResult, run_id: "current-run", replayed: true });
  f.mount(<V2ThreadCompactControl client={f.client} threadID="thread-1" runID="current-run" />);
  const compact = screen.getByRole("button", { name: "压缩当前对话" });
  await waitFor(() => expect(compact).toBeEnabled()); await user.click(compact);
  await screen.findByRole("alert");
  await waitFor(() => expect(compact).toBeEnabled()); await user.click(compact);
  expect(await screen.findByText("已压缩 20 条较早消息，保留 4 条近期消息。")).toBeVisible();
  expect(f.postControl.mock.calls[1]).toEqual(f.postControl.mock.calls[0]);
});

test("source-changed terminal failure permits a fresh compaction identity", async () => {
  const f = fixture(); const user = userEvent.setup();
  f.postControl.mockImplementationOnce(async () => {
    f.operations.operations = [{ ...compactionOperation("failed"), run_id: "current-run", terminal_at: when,
      error_message: "thread_context_source_changed: history changed while summarizing" }];
    throw new APIRequestError("thread_context_source_changed: history changed while summarizing", "CONFLICT", 409);
  }).mockResolvedValueOnce({ ...compactionResult, run_id: "current-run" });
  f.mount(<V2ThreadCompactControl client={f.client} threadID="thread-1" runID="current-run" />);
  const compact = screen.getByRole("button", { name: "压缩当前对话" });
  await waitFor(() => expect(compact).toBeEnabled()); await user.click(compact);
  await screen.findByRole("alert");
  await waitFor(() => expect(compact).toBeEnabled()); await user.click(compact);
  expect(await screen.findByText("已压缩 20 条较早消息，保留 4 条近期消息。")).toBeVisible();
  expect(f.postControl.mock.calls[1][0]).toBe(f.postControl.mock.calls[0][0]);
  expect(f.postControl.mock.calls[1][1]).toEqual(f.postControl.mock.calls[0][1]);
  expect(f.postControl.mock.calls[1][2]).not.toBe(f.postControl.mock.calls[0][2]);
});

test("failed operation read blocks new compaction until an explicit successful refresh", async () => {
  const f = fixture(); const user = userEvent.setup(); f.operations.thread_id = "other-thread";
  f.mount(<V2ThreadCompactControl client={f.client} threadID="thread-1" runID="current-run" />);
  await user.click(screen.getByText("压缩记录"));
  expect(await screen.findByRole("alert")).toHaveTextContent("请刷新核对记录后再压缩。");
  expect(screen.getByRole("button", { name: "压缩当前对话" })).toBeDisabled();
  f.operations.thread_id = "thread-1";
  await user.click(screen.getByRole("button", { name: "刷新核对记录" }));
  await waitFor(() => expect(screen.getByRole("button", { name: "压缩当前对话" })).toBeEnabled());
  expect(f.postControl).not.toHaveBeenCalled();
});

test("in-flight compact can be checked and discarded without a late response replacing its outcome", async () => {
  const f = fixture(); const user = userEvent.setup();
  let rejectCompact!: (error: Error) => void;
  f.postControl.mockImplementationOnce(() => new Promise((_resolve, reject) => { rejectCompact = reject; }))
    .mockImplementationOnce(async () => {
      const discarded = { ...compactionOperation("failed"), terminal_at: when, usage_known: false };
      f.operations.operations = [discarded]; return discarded;
    });
  f.mount(<V2ThreadCompactControl client={f.client} threadID="thread-1" runID="current-run" />);
  await waitFor(() => expect(screen.getByRole("button", { name: "压缩当前对话" })).toBeEnabled());
  await user.click(screen.getByRole("button", { name: "压缩当前对话" }));
  expect(screen.getByRole("button", { name: "正在压缩…" })).toBeDisabled();
  await user.click(screen.getByText("压缩记录"));
  f.operations.operations = [compactionOperation("started")];
  await user.click(screen.getByRole("button", { name: "刷新核对记录" }));
  expect(await screen.findByRole("button", { name: "放弃本次压缩" })).toBeEnabled();
  await user.click(screen.getByRole("button", { name: "放弃本次压缩" }));
  expect(await screen.findByText("已停止采用本次结果。")).toBeVisible();
  await waitFor(() => expect(screen.getByRole("button", { name: "压缩当前对话" })).toBeEnabled());
  await act(async () => { rejectCompact(new APIRequestError("late discarded response", "CONFLICT", 409)); });
  expect(screen.queryByRole("alert")).not.toBeInTheDocument();
  expect(screen.getByText("已停止采用本次结果。")).toBeVisible();
  expect(f.postControl).toHaveBeenCalledTimes(2);
});

test.each(["completed", "noop"] as const)("discard racing an already %s compaction reports its saved result", async (status) => {
  const f = fixture(); const user = userEvent.setup();
  let rejectCompact!: (error: Error) => void;
  f.postControl.mockImplementationOnce(() => new Promise((_resolve, reject) => { rejectCompact = reject; }))
    .mockImplementationOnce(async () => {
      const committed = { ...compactionOperation(status), terminal_at: when, result: { ...compactionResult, status } };
      f.operations.operations = [committed]; return committed;
    });
  f.mount(<V2ThreadCompactControl client={f.client} threadID="thread-1" runID="current-run" />);
  await waitFor(() => expect(screen.getByRole("button", { name: "压缩当前对话" })).toBeEnabled());
  await user.click(screen.getByRole("button", { name: "压缩当前对话" }));
  await user.click(screen.getByText("压缩记录"));
  f.operations.operations = [compactionOperation("started")];
  await user.click(screen.getByRole("button", { name: "刷新核对记录" }));
  await user.click(await screen.findByRole("button", { name: "放弃本次压缩" }));
  const notice = status === "completed" ? "已压缩 20 条较早消息，保留 4 条近期消息。" : "当前没有需要压缩的较早消息。";
  await waitFor(() => expect(screen.getByRole("status")).toHaveTextContent(notice));
  expect(screen.queryByText("已停止采用本次结果。")).not.toBeInTheDocument();
  await act(async () => { rejectCompact(new APIRequestError("late original response", "CONFLICT", 409)); });
  expect(screen.queryByRole("alert")).not.toBeInTheDocument();
  expect(screen.getByRole("status")).toHaveTextContent(notice);
  expect(f.postControl).toHaveBeenCalledTimes(2);
});

function render(ui: ReactElement, options?: RenderOptions) {
  window.localStorage.setItem("prayu.locale.v1", "zh-CN");
  const Wrapper = options?.wrapper;
  return renderComponent(ui, { ...options, wrapper: ({ children }) => <LocaleProvider>
    {Wrapper ? <Wrapper>{children}</Wrapper> : children}
  </LocaleProvider> });
}
