import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act, cleanup, render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { APIRequestError, type APIClient } from "../../api/client";
import type { ThreadContextPins } from "../../api/thread-context-controls";
import { V2ThreadCompactControl, V2ThreadPinPrompt, V2ThreadPinsPanel } from "./thread-context-controls";

const when = "2026-10-17T00:00:00Z";
const proposal = { id: "proposal-1", version: 1, status: "pending" as const,
  text: "整个任务不得修改 billing.go。", question: "后续压缩时是否始终保留这项要求？", run_id: "run-1", created_at: when };
function fixture(pending = false) {
  const state: ThreadContextPins = { version: "thread_context_pins.v1", thread_id: "thread-1", revision: 0,
    busy: false, effective_from: "next_request", pins: [], proposals: pending ? [proposal] : [] };
  const get = vi.fn(async () => state); const postControl = vi.fn(); const reply = vi.fn().mockResolvedValue(undefined);
  const client = { baseURL: "/api/v1", hasControl: true, get, postControl } as unknown as APIClient;
  const queries = new QueryClient({ defaultOptions: { queries: { retry: false, staleTime: Infinity } } });
  const mount = (child: React.ReactNode) => render(<QueryClientProvider client={queries}>{child}</QueryClientProvider>);
  return { state, client, queries, get, postControl, reply, mount };
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
  expect(await screen.findByRole("dialog", { name: "保留这项任务要求？" })).toBeVisible();
  expect(screen.getByText(proposal.text)).toBeVisible(); expect(f.postControl).not.toHaveBeenCalled();
  await user.click(screen.getByRole("radio", { name: /3. 自定义回复/u }));
  expect(screen.getByRole("checkbox")).not.toBeChecked();
  await user.type(screen.getByLabelText("你的回复"), "请先问我再修改。");
  if (pinCustom) await user.click(screen.getByRole("checkbox"));
  await user.click(screen.getByRole("button", { name: "确认并回复" }));
  await waitFor(() => expect(f.reply).toHaveBeenCalledWith("请先问我再修改。", "confirmed-pin-answer-1"));
  expect(f.postControl.mock.calls[0][1]).toMatchObject({ choice: "custom", text: "请先问我再修改。", pin_custom: pinCustom });
  await waitFor(() => expect(screen.queryByRole("dialog")).not.toBeInTheDocument());
});

test("closing the question dismisses it without pinning or sending a reply", async () => {
  const f = fixture(true); const user = userEvent.setup();
  f.postControl.mockResolvedValue({ state: { ...f.state, proposals: [{ ...proposal, status: "dismissed" }] }, replayed: false });
  f.mount(<V2ThreadPinPrompt client={f.client} threadID="thread-1" paused={false} onReply={f.reply} />);
  await screen.findByRole("dialog"); await user.click(screen.getByLabelText("关闭固定要求提问"));
  await waitFor(() => expect(screen.queryByRole("dialog")).not.toBeInTheDocument());
  expect(f.postControl.mock.calls[0][1]).toMatchObject({ choice: "dismiss" }); expect(f.reply).not.toHaveBeenCalled();
});

test("lost resolve response reuses its original revision and operation identity after polling", async () => {
  const f = fixture(true); const user = userEvent.setup();
  f.postControl.mockRejectedValueOnce(new Error("response lost")).mockResolvedValueOnce({
    state: { ...f.state, revision: 1, proposals: [{ ...proposal, status: "resolved" }] }, reply_text: "已固定。", reply_operation_key: "confirmed-pin-answer-1", replayed: true });
  f.mount(<V2ThreadPinPrompt client={f.client} threadID="thread-1" paused={false} onReply={f.reply} />);
  await screen.findByRole("dialog"); await user.click(screen.getByRole("button", { name: "确认并回复" }));
  await screen.findByRole("alert");
  await act(async () => { f.queries.setQueryData(["thread-context-pins", f.client.baseURL, "thread-1"], {
    ...f.state, revision: 1, proposals: [{ ...proposal, status: "resolved" }],
  }); });
  await user.click(screen.getByRole("button", { name: "确认并回复" }));
  await waitFor(() => expect(f.reply).toHaveBeenCalledTimes(1));
  expect(f.postControl.mock.calls[1]).toEqual(f.postControl.mock.calls[0]);
});

test("reply retry does not resolve the proposal again", async () => {
  const f = fixture(true); const user = userEvent.setup();
  f.reply.mockRejectedValueOnce(new Error("reply unavailable")).mockResolvedValueOnce(undefined);
  f.postControl.mockResolvedValue({ state: { ...f.state, proposals: [{ ...proposal, status: "resolved" }] },
    reply_text: "仅当前讨论。", reply_operation_key: "confirmed-pin-answer-1", replayed: false });
  f.mount(<V2ThreadPinPrompt client={f.client} threadID="thread-1" paused={false} onReply={f.reply} />);
  await screen.findByRole("dialog"); await user.click(screen.getByRole("radio", { name: /2. 仅当前讨论/u }));
  await user.click(screen.getByRole("button", { name: "确认并回复" }));
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
  await screen.findByRole("dialog"); await user.click(screen.getByRole("button", { name: "确认并回复" }));
  await screen.findByRole("alert");
  await waitFor(() => expect(screen.getByRole("button", { name: "确认并回复" })).toBeEnabled());
  await user.click(screen.getByRole("button", { name: "确认并回复" }));
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
  await screen.findByRole("alert"); expect(screen.queryByRole("dialog")).not.toBeInTheDocument();
  expect(screen.getByRole("button", { name: "固定到本次任务" })).toBeDisabled(); expect(f.postControl).not.toHaveBeenCalled();
});
