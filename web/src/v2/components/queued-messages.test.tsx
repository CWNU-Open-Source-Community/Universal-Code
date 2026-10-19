import { Blob as NodeBlob } from "node:buffer";
import { webcrypto } from "node:crypto";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act, cleanup, render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { APIRequestError, type APIClient } from "../../api/client";
import type { QueueBinding, QueuePromotion, QueuePromotionRejection, QueuedMessage } from "../../api/queued-messages";
import { V2RecoveryProvider } from "../recovery-storage";
import { V2QueuedMessages } from "./queued-messages";

const binding: QueueBinding = { threadID: "thread-a", runID: "run-a", sessionID: "session-a", workspaceID: "workspace-a" };
const time = "2026-09-22T01:00:00Z", sha = "a".repeat(64);
const message = (index: number, patch: Partial<QueuedMessage> = {}): QueuedMessage => ({
  id: `message-${index}`, sequence: index, status: "pending", prepared: false, content: `要求 ${index}`,
  content_sha256: sha, content_redacted: false, revision: 0, created_at: time, images: [], attachments: [], can_edit: true, can_cancel: true, ...patch,
});
function fixture(initial = [message(1), message(2)]) {
  let items = initial, revisionCalls = 0;
  const receipts = new Map<string, unknown>();
  const hooks: { post?: () => Promise<void>; observationError?: boolean; malformed?: boolean; successor?: boolean } = {};
  const postControl = vi.fn(async (_path: string, body: { expected_revision: number; content: string }, key: string) => {
    revisionCalls++; if (hooks.post) await hooks.post();
    const id = _path.split("/").at(-2)!;
    const current = items.find((item) => item.id === id)!;
    const receipt = { version: "session_steering_revision.v1", run_id: binding.runID, session_id: binding.sessionID, message_id: id,
      receipt: { id: `revision-${revisionCalls}`, from_revision: body.expected_revision, to_revision: body.expected_revision + 1,
        old_content_sha256: current.content_sha256, new_content_sha256: "b".repeat(64), created_at: time },
      replayed: false, execution_started: false, model_called: false, tool_called: false, capability_grant: false };
    receipts.set(key, receipt);
    items = items.map((item) => item.id === id ? { ...item, revision: item.revision + 1, content: body.content, content_sha256: "b".repeat(64) } : item);
    return receipt;
  });
  const get = vi.fn(async (path: string) => {
    if (path.includes("/revisions/")) {
      if (hooks.observationError) throw new TypeError("observation unavailable");
      const id = path.split("/").at(-3)!;
      const current = items.find((item) => item.id === id)!;
      const receipt = receipts.get(path.split("/").at(-1)!);
      return { version: "session_steering_revision.v1", session_id: binding.sessionID, message_id: path.split("/").at(-3),
        state: receipt ? "sealed" : "absent", ...(receipt ? { revision: receipt } : { message: { id, run_id: binding.runID,
          session_id: binding.sessionID, revision: current.revision, status: current.status } }), capability_grant: false };
    }
    const other = path.includes("thread-b");
    return { version: "thread_queued_messages.v1", thread_id: hooks.malformed ? "foreign" : other ? "thread-b" : binding.threadID,
      run_id: other ? "run-b" : hooks.successor ? "run-next" : binding.runID,
      session_id: other ? "session-b" : hooks.successor ? "session-next" : binding.sessionID,
      pending: hooks.successor ? 0 : items.filter((item) => !item.prepared).length, prepared: hooks.successor ? 0 : items.filter((item) => item.prepared).length,
      items: hooks.successor ? [] : other ? items.map((item) => ({ ...item, content: `B 的要求 ${item.sequence}` })) : items, capability_grant: false };
  });
  const cancelSessionSteering = vi.fn(async (_session: string, id: string) => {
    items = items.filter((item) => item.id !== id); return { run_id: binding.runID };
  });
  const client = { baseURL: "/api/v1", get, postControl, cancelSessionSteering, hasSessionSteeringControl: true } as unknown as APIClient;
  return { client, get, postControl, cancelSessionSteering, hooks, receipts, setItems: (next: QueuedMessage[]) => { items = next; } };
}
function mount(client: APIClient, current = binding, canPromote = false, running = false) {
  const query = new QueryClient({ defaultOptions: { queries: { retry: false, gcTime: 0 } } });
  const view = (selected = current, promotable = canPromote, active = running) => <V2RecoveryProvider client={client} scopeID="queue-test-store">
    <QueryClientProvider client={query}><V2QueuedMessages client={client} {...selected} running={active} canPromote={promotable} /></QueryClientProvider>
  </V2RecoveryProvider>;
  return { ...render(view()), query, view };
}
function promotionFixture(initial = [message(1), message(2)]) {
  let items = initial;
  const originals = new Map(initial.map((item) => [item.id, { ...item, status: "pending" as "pending" | "committed" | "cancelled" }]));
  const receipts = new Map<string, QueuePromotion>();
  const rejections = new Map<string, QueuePromotionRejection>();
  const hooks = { attemptID: "attempt-a", executionID: "execution-a", executionObserved: true, responseLost: false,
    observationError: false, post: undefined as (() => Promise<void>) | undefined };
  const postControl = vi.fn(async (path: string, body: { expected_revision: number; expected_content_sha256: string;
    expected_attempt_id: string; expected_execution_id: string }, key: string) => {
    if (hooks.post) await hooks.post();
    if (!path.endsWith("/promote")) throw new Error("unexpected mutation");
    const prior = receipts.get(key) ?? rejections.get(key);
    if (prior) return prior;
    const id = path.split("/").at(-2)!, source = originals.get(id)!;
    if (source.status !== "pending" || source.revision !== body.expected_revision) {
      throw new APIRequestError("promotion conflict", "CONFLICT", 409);
    }
    if (!hooks.executionObserved || hooks.attemptID !== body.expected_attempt_id || hooks.executionID !== body.expected_execution_id) {
      const rejection: QueuePromotionRejection = { version: "session_steering_promotion.v1", run_id: binding.runID, session_id: binding.sessionID, message_id: id,
        receipt: { id: `rejected-${key}`, expected_revision: body.expected_revision, content_sha256: body.expected_content_sha256,
          target_attempt_id: body.expected_attempt_id, execution_id: body.expected_execution_id, created_at: time },
        rejected: true, replayed: false, execution_started: false, model_called: false, tool_called: false, capability_grant: false };
      rejections.set(key, rejection);
      if (hooks.responseLost) throw new TypeError("promotion response lost");
      return rejection;
    }
    const receipt: QueuePromotion = { version: "session_steering_promotion.v1", run_id: binding.runID, session_id: binding.sessionID, message_id: id,
      receipt: { id: `promotion-${id}`, replacement_message_id: `steer-${id}`, expected_revision: body.expected_revision,
        content_sha256: body.expected_content_sha256, target_attempt_id: body.expected_attempt_id,
        execution_id: body.expected_execution_id, cancellation_id: `cancel-${id}`, created_at: time },
      replayed: false, execution_started: false, model_called: false, tool_called: false, capability_grant: false };
    receipts.set(key, receipt); originals.set(id, { ...source, status: "cancelled" });
    items = [...items.filter((item) => item.id !== id), message(Math.max(...items.map((item) => item.sequence)) + 1,
      { id: `steer-${id}`, content: source.content, delivery_mode: "steer" })];
    if (hooks.responseLost) throw new TypeError("promotion response lost");
    return receipt;
  });
  const get = vi.fn(async (path: string) => {
    if (path.includes("/promotions/")) {
      if (hooks.observationError) throw new TypeError("promotion observation unavailable");
      const id = path.split("/").at(-3)!, key = path.split("/").at(-1)!;
      const receipt = receipts.get(key), rejection = rejections.get(key);
      const source = originals.get(id)!;
      return { version: "session_steering_promotion.v1", session_id: binding.sessionID, message_id: id,
        state: receipt ? "sealed" : rejection ? "rejected" : "absent", ...(receipt ? { promotion: receipt } : rejection ? { rejection } : { message: { id, run_id: binding.runID,
          session_id: binding.sessionID, revision: source.revision, status: source.status } }),
        execution_observed: hooks.executionObserved, ...(hooks.executionObserved ? { execution_id: hooks.executionID } : {}), capability_grant: false };
    }
    const other = path.includes("thread-b");
    const queue = other ? [message(1, { content: "B 的要求 1" })] : items;
    return { version: "thread_queued_messages.v1", thread_id: other ? "thread-b" : binding.threadID,
      run_id: other ? "run-b" : binding.runID, session_id: other ? "session-b" : binding.sessionID,
      pending: queue.filter((item) => !item.prepared).length, prepared: queue.filter((item) => item.prepared).length,
      items: queue, ...(hooks.executionObserved ? { current_attempt_id: hooks.attemptID, execution_id: hooks.executionID } : {}), capability_grant: false };
  });
  const client = { baseURL: "/api/v1", get, postControl, hasSessionSteeringControl: true,
    downloadWorkspaceImage: vi.fn(async () => new Blob(["png"], { type: "image/png" })) } as unknown as APIClient;
  return { client, get, postControl, hooks, receipts, rejections, claim: (id: string) => {
    const source = originals.get(id)!; originals.set(id, { ...source, status: "committed" });
    items = items.filter((item) => item.id !== id);
  }, revise: (id: string) => {
    const source = originals.get(id)!;
    const revised = { ...source, revision: source.revision + 1, content: "另一窗口修改后的要求", content_sha256: "b".repeat(64) };
    originals.set(id, revised); items = items.map((item) => item.id === id ? { ...item, ...revised, status: "pending" } : item);
  }, complete: () => {
    for (const [id, source] of originals) if (source.status === "pending") originals.set(id, { ...source, status: "committed" });
    items = [];
  } };
}
async function promoteFirst(user: ReturnType<typeof userEvent.setup>) {
  const row = await screen.findByRole("listitem", { name: "消息 1" });
  const action = within(row).getByRole("button", { name: "引导当前任务" });
  expect(action).toBeEnabled();
  await user.click(action);
}
async function editFirst(user: ReturnType<typeof userEvent.setup>) {
  await screen.findByText("待处理 2 条");
  await user.click(screen.getByRole("button", { name: "消息 1 的更多操作" }));
  await user.click(screen.getByRole("menuitem", { name: "编辑消息" }));
  return screen.getByRole("textbox", { name: "编辑消息 1" });
}
beforeEach(() => { localStorage.clear(); vi.stubGlobal("crypto", webcrypto); });
afterEach(() => { cleanup(); vi.restoreAllMocks(); vi.unstubAllGlobals(); });

it("reads the complete durable queue and changes one exact message without reordering", async () => {
  const f = fixture(Array.from({ length: 25 }, (_, index) => message(index + 1, index === 0 ? { prepared: true, can_edit: false, can_cancel: false } : {})));
  mount(f.client); const user = userEvent.setup();
  await screen.findByText("待处理 24 条 · 正在处理 1 条");
  // The compact preview keeps the prepared message immutable.
  expect(screen.getByText("处理中")).toHaveAttribute("title", "正在处理，无法修改或撤回");
  expect(within(screen.getByRole("listitem", { name: "消息 1" })).queryByRole("button", { name: "引导当前任务" })).not.toBeInTheDocument();
  expect(screen.getAllByRole("button", { name: "撤回" })).toHaveLength(24);
  await user.click(screen.getByRole("button", { name: "消息 1 的更多操作" }));
  expect(screen.queryByRole("menuitem", { name: "编辑消息" })).not.toBeInTheDocument();
  await user.keyboard("{Escape}");
  await user.click(screen.getByRole("button", { name: "消息 25 的更多操作" }));
  await user.click(screen.getByRole("menuitem", { name: "编辑消息" }));
  const input = screen.getByRole("textbox", { name: "编辑消息 25" });
  await user.clear(input); await user.type(input, "最后一条修改后的要求"); await user.click(screen.getByRole("button", { name: "保存修改" }));
  await screen.findByText("修改已保存。");
  expect(f.postControl).toHaveBeenCalledWith("/sessions/session-a/messages/message-25/revise",
    { version: "session_steering_revision.v1", expected_revision: 0, content: "最后一条修改后的要求" }, expect.any(String));
  expect(screen.queryByRole("textbox")).not.toBeInTheDocument();
  await user.click(screen.getAllByRole("button", { name: "撤回" })[0]);
  await screen.findByText("消息已撤回。");
  await screen.findByText("待处理 23 条 · 正在处理 1 条");
});

it("preserves later typing and isolates a late save across task switches", async () => {
  const f = fixture(); let release!: () => void;
  f.hooks.post = () => new Promise<void>((resolve) => { release = resolve; });
  const ui = mount(f.client); const user = userEvent.setup(); const input = await editFirst(user);
  await user.clear(input); await user.type(input, "保存这一版"); await user.click(screen.getByRole("button", { name: "保存修改" }));
  await user.type(input, "，这是后来写的");
  ui.rerender(ui.view({ ...binding, threadID: "thread-b", runID: "run-b", sessionID: "session-b" }));
  await screen.findAllByText("B 的要求 1");
  await act(async () => { release(); });
  expect(screen.queryByText("修改已保存。")).not.toBeInTheDocument();
  expect(screen.queryByRole("textbox")).not.toBeInTheDocument();
  ui.rerender(ui.view());
  expect(await screen.findByRole("textbox", { name: "编辑消息 1" })).toHaveValue("保存这一版，这是后来写的");
  expect(screen.getByRole("button", { name: "保存修改" })).toBeDisabled();
  expect(f.postControl).toHaveBeenCalledTimes(1);
});

it("keeps an unknown save through reload and confirms only the original receipt without posting", async () => {
  const f = fixture(); const actual = f.postControl.getMockImplementation()!;
  f.postControl.mockImplementation(async (...args) => { await actual(...args); throw new TypeError("connection lost"); });
  const ui = mount(f.client); const user = userEvent.setup(); const input = await editFirst(user);
  await user.clear(input); await user.type(input, "响应丢失但实际保存"); await user.click(screen.getByRole("button", { name: "保存修改" }));
  await screen.findByText(/保存结果待确认。connection lost/u);
  await user.click(screen.getByRole("button", { name: "待处理 2 条" }));
  expect(screen.queryByRole("button", { name: "消息 1 的更多操作" })).not.toBeInTheDocument();
  expect(input).toHaveValue("响应丢失但实际保存");
  expect(screen.getByRole("button", { name: "核对保存结果" })).toBeEnabled();
  expect(f.postControl).toHaveBeenCalledTimes(1);
  ui.unmount(); mount(f.client);
  await user.click(await screen.findByRole("button", { name: "核对保存结果" }));
  await screen.findByText("修改已保存。");
  expect(f.postControl).toHaveBeenCalledTimes(1);
  expect(screen.queryByRole("textbox")).not.toBeInTheDocument();
  expect(f.get.mock.calls.some(([path]) => path.includes("/revisions/"))).toBe(true);
});

it("retains rejected edits and requires explicit adoption of the latest revision", async () => {
  const f = fixture();
  f.postControl.mockImplementation(async () => {
    f.setItems([message(1, { revision: 1, content: "另一窗口版本", content_sha256: "b".repeat(64) }), message(2)]);
    throw new APIRequestError("revision conflict", "CONFLICT", 409);
  });
  mount(f.client); const user = userEvent.setup(); const input = await editFirst(user);
  await user.clear(input); await user.type(input, "保留我的版本"); await user.click(screen.getByRole("button", { name: "保存修改" }));
  await screen.findByText(/原消息版本已变化或已处理/u);
  expect(input).toHaveValue("保留我的版本");
  await user.click(screen.getByRole("button", { name: "载入最新正文，替换编辑稿" }));
  expect(screen.getByRole("textbox", { name: "编辑消息 1" })).toHaveValue("另一窗口版本");
  expect(f.postControl).toHaveBeenCalledTimes(1);
});

it("does not turn an absent receipt into rejection or silently retry a newer draft", async () => {
  const f = fixture(); const actual = f.postControl.getMockImplementation()!;
  f.postControl.mockRejectedValueOnce(new TypeError("connection lost")).mockImplementation(actual);
  mount(f.client); const user = userEvent.setup(); const input = await editFirst(user);
  await user.clear(input); await user.type(input, "原保存"); await user.click(screen.getByRole("button", { name: "保存修改" }));
  await screen.findByText(/保存结果待确认。connection lost/u);
  await user.type(input, "以及后来输入");
  await user.click(screen.getByRole("button", { name: "核对保存结果" }));
  await screen.findByText(/尚未查到这次操作的最终记录/u);
  expect(f.postControl).toHaveBeenCalledTimes(1);
  expect(screen.getByRole("button", { name: "保存修改" })).toBeDisabled();
  await user.click(screen.getByRole("button", { name: "重试原保存" }));
  await screen.findByText("修改已保存。");
  expect(f.postControl.mock.calls[1]).toEqual(f.postControl.mock.calls[0]);
  expect(screen.getByRole("textbox", { name: "编辑消息 1" })).toHaveValue("原保存以及后来输入");
});

it("keeps an empty attachment-only edit visible and allows explicit cancellation", async () => {
  const file = { id: "file-a", workspace_id: binding.workspaceID, sha256: sha, name: "notes.txt", mime_type: "text/plain",
    byte_size: 8, readability: "text" as const, text_bytes: 8, redacted: false };
  const f = fixture([message(1, { attachments: [file] }), message(2)]);
  mount(f.client); const user = userEvent.setup(); const input = await editFirst(user);
  await user.clear(input);
  expect(input).toHaveValue(""); expect(screen.getByRole("button", { name: "保存修改" })).toBeEnabled();
  await user.click(screen.getByRole("button", { name: "取消编辑" }));
  expect(screen.queryByRole("textbox")).not.toBeInTheDocument();
  expect(f.postControl).not.toHaveBeenCalled();
});

it("keeps old-run drafts and unresolved operations reachable when the thread starts a successor run", async () => {
  const f = fixture(); const actual = f.postControl.getMockImplementation()!;
  f.postControl.mockImplementation(async (...args) => { await actual(...args); throw new TypeError("connection lost"); });
  const ui = mount(f.client); const user = userEvent.setup(); const input = await editFirst(user);
  await user.clear(input); await user.type(input, "原执行的编辑稿"); await user.click(screen.getByRole("button", { name: "保存修改" }));
  await screen.findByText(/保存结果待确认。connection lost/u);
  f.hooks.successor = true;
  ui.rerender(ui.view({ ...binding, runID: "run-next", sessionID: "session-next" }));
  expect(await screen.findByRole("textbox", { name: "编辑消息 1" })).toHaveValue("原执行的编辑稿");
  expect(screen.getByText(/上轮编辑稿/u)).toBeInTheDocument();
  await user.click(screen.getByRole("button", { name: "核对保存结果" }));
  await waitFor(() => expect(screen.queryByRole("region", { name: "待处理消息" })).not.toBeInTheDocument());
  expect(f.get.mock.calls.some(([path]) => path.startsWith("/sessions/session-a/messages/message-1/revisions/"))).toBe(true);
  expect(f.postControl).toHaveBeenCalledTimes(1);
});

it("does not treat an unauthorized retry as proof that the original unknown save was rejected", async () => {
  const f = fixture(); const actual = f.postControl.getMockImplementation()!;
  f.postControl.mockImplementationOnce(async (...args) => { await actual(...args); throw new TypeError("connection lost"); })
    .mockRejectedValueOnce(new APIRequestError("token expired", "POLICY_DENIED", 401));
  mount(f.client); const user = userEvent.setup(); const input = await editFirst(user);
  await user.clear(input); await user.type(input, "此前已经保存的正文"); await user.click(screen.getByRole("button", { name: "保存修改" }));
  await screen.findByText(/保存结果待确认。connection lost/u);
  f.hooks.observationError = true;
  await user.click(screen.getByRole("button", { name: "重试原保存" }));
  await screen.findByText(/保存结果待确认。token expired/u);
  expect(screen.getByRole("button", { name: "核对保存结果" })).toBeEnabled();
  expect(screen.queryByText(/操作未接受/u)).not.toBeInTheDocument();
  f.hooks.observationError = false;
  await user.click(screen.getByRole("button", { name: "核对保存结果" }));
  await screen.findByText("修改已保存。");
});

it("rejects a queue from a different thread instead of exposing mutation buttons", async () => {
  const f = fixture(); f.hooks.malformed = true; mount(f.client);
  await screen.findByRole("alert");
  const panel = within(screen.getByRole("region", { name: "待处理消息" }));
  expect(panel.queryByRole("button", { name: /更多操作/u })).not.toBeInTheDocument();
  expect(panel.queryByRole("button", { name: "撤回" })).not.toBeInTheDocument();
  expect(f.postControl).not.toHaveBeenCalled();
  expect(f.cancelSessionSteering).not.toHaveBeenCalled();
});

it("supports keyboard menu dismissal and folds only the preview without cancelling queued input", async () => {
  const f = fixture(); mount(f.client); const user = userEvent.setup();
  const trigger = await screen.findByRole("button", { name: "消息 1 的更多操作" });
  await user.click(trigger);
  expect(screen.getByRole("menuitem", { name: "编辑消息" })).toHaveFocus();
  await user.keyboard("{End}");
  expect(screen.getByRole("menuitem", { name: "收起待处理消息" })).toHaveFocus();
  await user.keyboard("{Escape}");
  expect(trigger).toHaveFocus();
  expect(screen.queryByRole("menu")).not.toBeInTheDocument();
  await user.click(trigger);
  await user.click(screen.getByRole("menuitem", { name: "收起待处理消息" }));
  const toggle = screen.getByRole("button", { name: "待处理 2 条" });
  expect(toggle).toHaveFocus();
  expect(toggle).toHaveAttribute("aria-expanded", "false");
  expect(screen.queryByRole("button", { name: "撤回" })).not.toBeInTheDocument();
  await user.click(toggle);
  expect(screen.getAllByRole("button", { name: "撤回" })).toHaveLength(2);
  expect(f.cancelSessionSteering).not.toHaveBeenCalled();
  expect(f.postControl).not.toHaveBeenCalled();
});

it("removes editing from an open menu when the message becomes prepared", async () => {
  const f = fixture(); const ui = mount(f.client); const user = userEvent.setup();
  await user.click(await screen.findByRole("button", { name: "消息 1 的更多操作" }));
  expect(screen.getByRole("menuitem", { name: "编辑消息" })).toBeEnabled();
  f.setItems([message(1, { prepared: true, can_edit: false, can_cancel: false }), message(2)]);
  await act(async () => { await ui.query.invalidateQueries(); });
  await screen.findByText("处理中");
  expect(screen.queryByRole("menuitem", { name: "编辑消息" })).not.toBeInTheDocument();
  expect(screen.getByRole("menuitem", { name: "查看全文与附件" })).toHaveFocus();
  await user.keyboard("{Escape}");
  expect(screen.getByRole("button", { name: "消息 1 的更多操作" })).toHaveFocus();
  expect(screen.queryByRole("menu")).not.toBeInTheDocument();
  expect(screen.getAllByRole("button", { name: "撤回" })).toHaveLength(1);
  expect(f.postControl).not.toHaveBeenCalled();
});

it("opens full text and attachments outside the compact list with a modal focus loop and Escape return", async () => {
  const content = Array.from({ length: 60 }, (_, index) => `长正文第 ${index + 1} 行`).join("\n");
  const file = { id: "file-a", workspace_id: binding.workspaceID, sha256: sha, name: "notes.txt", mime_type: "text/plain",
    byte_size: 8, readability: "text" as const, text_bytes: 8, redacted: false };
  const f = fixture([message(1, { content, attachments: [file] }), message(2)]);
  f.client.downloadWorkspaceFile = vi.fn(async () => new NodeBlob(["raw text"], { type: "text/plain" }) as unknown as Blob);
  const ui = mount(f.client); const user = userEvent.setup();
  const preview = await screen.findByRole("button", { name: "查看消息 1 全文" });
  await user.click(preview);
  const dialog = screen.getByRole("dialog", { name: "消息 1 的全文与附件" });
  expect(ui.container.querySelector("ol")?.contains(dialog)).toBe(false);
  expect(dialog.querySelector("pre")?.textContent).toBe(content);
  expect(within(dialog).getByRole("button", { name: "Download file notes.txt" })).toBeInTheDocument();
  const close = within(dialog).getByRole("button", { name: "关闭消息详情" });
  expect(close).toHaveFocus();
  await user.keyboard("{Shift>}{Tab}{/Shift}");
  expect(within(dialog).getByRole("button", { name: "Download file notes.txt" })).toHaveFocus();
  await user.keyboard("{Tab}");
  expect(close).toHaveFocus();
  const filePreview = within(dialog).getByRole("button", { name: "Preview file notes.txt" });
  await user.click(filePreview);
  const textDialog = screen.getByRole("dialog", { name: "notes.txt" });
  expect(await within(textDialog).findByText("raw text")).toBeInTheDocument();
  expect(within(textDialog).queryByRole("button", { name: "Restore to message" })).not.toBeInTheDocument();
  await user.keyboard("{Escape}");
  expect(screen.queryByRole("dialog", { name: "notes.txt" })).not.toBeInTheDocument();
  expect(filePreview).toHaveFocus();
  await user.keyboard("{Escape}");
  expect(screen.queryByRole("dialog")).not.toBeInTheDocument();
  expect(preview).toHaveFocus();
  expect(f.postControl).not.toHaveBeenCalled();
  expect(f.cancelSessionSteering).not.toHaveBeenCalled();
});

it("opens thumbnail attachments in persistent details and returns through a nested image preview", async () => {
  vi.stubGlobal("URL", Object.assign(URL, { createObjectURL: vi.fn(() => "blob:verified-image"), revokeObjectURL: vi.fn() }));
  const image = { id: "image-a", workspace_id: binding.workspaceID, sha256: sha,
    mime_type: "image/png" as const, byte_size: 128, width: 64, height: 64, name: "布局.png" };
  const f = fixture([message(1, { images: [image] }), message(2)]);
  f.client.downloadWorkspaceImage = vi.fn(async () => new Blob(["png"], { type: "image/png" }));
  mount(f.client); const user = userEvent.setup();
  const thumbnail = await screen.findByRole("button", { name: "预览图片 布局.png" });
  await waitFor(() => expect(thumbnail).toBeEnabled());
  thumbnail.focus(); await user.keyboard("{Enter}");
  const details = screen.getByRole("dialog", { name: "消息 1 的全文与附件" });
  const imageButton = within(details).getByRole("button", { name: "预览图片 布局.png" });
  await waitFor(() => expect(imageButton).toBeEnabled());
  await user.click(imageButton);
  expect(screen.getByRole("dialog", { name: "图片预览 布局.png" })).toBeInTheDocument();
  await user.keyboard("{Escape}");
  expect(screen.queryByRole("dialog", { name: "图片预览 布局.png" })).not.toBeInTheDocument();
  expect(imageButton).toHaveFocus();
  await user.keyboard("{Escape}");
  expect(screen.queryByRole("dialog")).not.toBeInTheDocument();
  expect(thumbnail).toHaveFocus();
});

it("keeps details and focus through preparation, refreshed text, failed reads and external cancellation", async () => {
  const f = fixture(); const ui = mount(f.client); const user = userEvent.setup();
  const trigger = await screen.findByRole("button", { name: "消息 1 的更多操作" });
  await user.click(trigger); await user.click(screen.getByRole("menuitem", { name: "查看全文与附件" }));
  const dialog = screen.getByRole("dialog", { name: "消息 1 的全文与附件" });
  const body = within(dialog).getByLabelText("消息全文与附件");
  body.focus();
  f.setItems([message(1, { content: "刷新后的正文", revision: 1, prepared: true, can_edit: false, can_cancel: false }), message(2)]);
  await act(async () => { await ui.query.invalidateQueries(); });
  expect(await within(dialog).findByText("刷新后的正文")).toBeInTheDocument();
  expect(within(dialog).getByText(/正在处理/u)).toBeInTheDocument();
  expect(body).toHaveFocus();
  f.hooks.malformed = true;
  await act(async () => { await ui.query.invalidateQueries(); });
  expect(await within(dialog).findByText(/暂时无法确认最新队列状态/u)).toBeInTheDocument();
  expect(body).toHaveFocus();
  f.hooks.malformed = false; f.setItems([message(2)]);
  await act(async () => { await ui.query.invalidateQueries(); });
  expect(await within(dialog).findByText(/已离开待处理队列/u)).toBeInTheDocument();
  expect(within(dialog).getByText("刷新后的正文")).toBeInTheDocument();
  expect(body).toHaveFocus();
  await user.keyboard("{Escape}");
  expect(screen.getByRole("button", { name: "待处理 1 条" })).toHaveFocus();
  expect(f.postControl).not.toHaveBeenCalled();
  expect(f.cancelSessionSteering).not.toHaveBeenCalled();
});

it("returns to the composer when the last queued message leaves during details", async () => {
  const f = fixture([message(1)]), query = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  const user = userEvent.setup();
  render(<V2RecoveryProvider client={f.client} scopeID="queue-details-empty-store"><QueryClientProvider client={query}>
    <div className="v2-composer-dock"><V2QueuedMessages client={f.client} {...binding} running={false} />
      <textarea aria-label="继续对话" /></div>
  </QueryClientProvider></V2RecoveryProvider>);
  await user.click(await screen.findByRole("button", { name: "查看消息 1 全文" }));
  f.setItems([]);
  await act(async () => { await query.invalidateQueries(); });
  await screen.findByText(/已离开待处理队列/u);
  expect(screen.getByRole("dialog", { name: "消息 1 的全文与附件" })).toBeInTheDocument();
  await user.keyboard("{Escape}");
  expect(screen.getByRole("textbox", { name: "继续对话" })).toHaveFocus();
  expect(screen.queryByRole("region", { name: "待处理消息" })).not.toBeInTheDocument();
});

it("persists one immutable promotion before posting and displays the replacement as a current-task correction", async () => {
  const f = promotionFixture(); mount(f.client, binding, true); const user = userEvent.setup();
  f.hooks.post = async () => {
    expect(Array.from({ length: localStorage.length }, (_, index) => decodeURIComponent(localStorage.key(index)!))
      .some((key) => key.includes("queue-operation:"))).toBe(true);
  };
  await promoteFirst(user);
  await screen.findByText("已转为引导。");
  expect(f.postControl).toHaveBeenCalledTimes(1);
  expect(f.postControl).toHaveBeenCalledWith("/sessions/session-a/messages/message-1/promote",
    { version: "session_steering_promotion.v1", expected_revision: 0, expected_content_sha256: sha,
      expected_attempt_id: "attempt-a", expected_execution_id: "execution-a" }, expect.any(String));
  await screen.findByText("更新当前任务");
  expect(screen.queryByRole("button", { name: "消息 1 的更多操作" })).not.toBeInTheDocument();
  expect(screen.getByRole("button", { name: "查看消息 3 全文" })).toHaveTextContent("要求 1");
  expect(within(screen.getByRole("listitem", { name: "消息 3" })).queryByRole("button", { name: "引导当前任务" })).not.toBeInTheDocument();
});

it("removes a successful empty queue after execution finishes without retaining a waiting notice", async () => {
  const f = promotionFixture(); const ui = mount(f.client, binding, true, true); const user = userEvent.setup();
  await promoteFirst(user); await screen.findByText("已转为引导。");
  f.complete();
  await act(async () => { await ui.query.invalidateQueries(); });
  await screen.findByText("待处理 0 条");
  expect(screen.getByText("已转为引导。")).toBeInTheDocument();
  ui.rerender(ui.view(binding, true, false));
  await waitFor(() => expect(screen.queryByRole("region", { name: "待处理消息" })).not.toBeInTheDocument());
  expect(screen.queryByText("已转为引导。")).not.toBeInTheDocument();
});

it("keeps an unknown promotion reachable when execution ends with a complete empty queue", async () => {
  const f = promotionFixture(); f.postControl.mockRejectedValueOnce(new TypeError("connection lost"));
  const ui = mount(f.client, binding, true); const user = userEvent.setup();
  await promoteFirst(user); await screen.findByText(/引导结果待确认。connection lost/u);
  f.complete();
  await act(async () => { await ui.query.invalidateQueries(); });
  await screen.findByText("待处理 0 条");
  expect(screen.getByRole("button", { name: "核对引导结果" })).toBeEnabled();
  expect(screen.getByRole("button", { name: "重试原引导" })).toBeEnabled();
  expect(screen.getByText(/引导结果待确认。connection lost/u)).toBeInTheDocument();
  expect(f.postControl).toHaveBeenCalledTimes(1);
});

it("retains a lost promotion response through collapse and reload and confirms only its original receipt", async () => {
  const f = promotionFixture(); f.hooks.responseLost = true;
  const ui = mount(f.client, binding, true); const user = userEvent.setup();
  await promoteFirst(user); await screen.findByText(/引导结果待确认。promotion response lost/u);
  await user.click(screen.getByRole("button", { name: "待处理 2 条" }));
  expect(screen.getByRole("button", { name: "核对引导结果" })).toBeEnabled();
  ui.unmount(); mount(f.client, binding, true);
  await screen.findByRole("button", { name: "核对引导结果" });
  expect(f.postControl).toHaveBeenCalledTimes(1);
  await user.click(screen.getByRole("button", { name: "核对引导结果" }));
  await screen.findByText("已转为引导。");
  expect(f.postControl).toHaveBeenCalledTimes(1);
  expect(f.get.mock.calls.some(([path]) => path.includes(`/promotions/${f.postControl.mock.calls[0][2]}`))).toBe(true);
  expect(screen.queryByRole("button", { name: "核对引导结果" })).not.toBeInTheDocument();
});

it("keeps an absent promotion unknown when execution is unobserved and retries the captured intent only after a manual click", async () => {
  const f = promotionFixture(); f.postControl.mockRejectedValueOnce(new TypeError("connection lost"));
  const ui = mount(f.client, binding, true); const user = userEvent.setup();
  await promoteFirst(user); await screen.findByText(/引导结果待确认。connection lost/u);
  const captured = f.postControl.mock.calls[0];
  f.hooks.executionObserved = false;
  await act(async () => { await ui.query.invalidateQueries(); });
  await user.click(screen.getByRole("button", { name: "核对引导结果" }));
  await screen.findByText(/尚未查到这次引导的最终记录/u);
  expect(screen.getByRole("button", { name: "核对引导结果" })).toBeEnabled();
  expect(f.postControl).toHaveBeenCalledTimes(1);
  ui.unmount(); mount(f.client, binding, true);
  await screen.findByRole("button", { name: "重试原引导" });
  expect(f.postControl).toHaveBeenCalledTimes(1);
  f.hooks.executionObserved = true;
  await user.click(screen.getByRole("button", { name: "重试原引导" }));
  await screen.findByText("已转为引导。");
  expect(f.postControl.mock.calls[1]).toEqual(captured);
});

it("keeps a different local execution unknown until a sealed manual refusal permits a fresh promotion intent", async () => {
  const f = promotionFixture(); f.postControl.mockRejectedValueOnce(new TypeError("connection lost"));
  mount(f.client, binding, true); const user = userEvent.setup();
  await promoteFirst(user); await screen.findByText(/引导结果待确认。connection lost/u);
  const captured = f.postControl.mock.calls[0];
  f.hooks.executionID = "execution-restarted";
  await user.click(screen.getByRole("button", { name: "核对引导结果" }));
  await screen.findByText(/尚未查到这次引导的最终记录/u);
  expect(f.postControl).toHaveBeenCalledTimes(1);
  expect(screen.getByRole("button", { name: "重试原引导" })).toBeEnabled();
  expect(screen.getByRole("button", { name: "查看消息 1 全文" })).toHaveTextContent("要求 1");
  await user.click(screen.getByRole("button", { name: "重试原引导" }));
  await screen.findByText("这次引导未生效，原消息保留，可重新引导。");
  expect(f.postControl.mock.calls[1]).toEqual(captured);
  expect(screen.queryByRole("button", { name: "重试原引导" })).not.toBeInTheDocument();
  expect(screen.queryByText("更新当前任务")).not.toBeInTheDocument();
  expect(screen.getByRole("button", { name: "查看消息 1 全文" })).toHaveTextContent("要求 1");
  await promoteFirst(user);
  await screen.findByText("已转为引导。");
  expect(f.postControl.mock.calls[2][1]).toEqual({ ...captured[1], expected_execution_id: "execution-restarted" });
  expect(f.postControl.mock.calls[2][2]).not.toBe(captured[2]);
});

it("recovers a lost sealed rejection after reload without posting or removing the original message", async () => {
  const f = promotionFixture(); f.hooks.responseLost = true;
  f.hooks.post = async () => { f.hooks.executionObserved = false; };
  const ui = mount(f.client, binding, true); const user = userEvent.setup();
  await promoteFirst(user); await screen.findByText(/引导结果待确认。promotion response lost/u);
  ui.unmount(); mount(f.client, binding, true);
  await user.click(await screen.findByRole("button", { name: "核对引导结果" }));
  await screen.findByText("这次引导未生效，原消息保留，可重新引导。");
  expect(f.postControl).toHaveBeenCalledTimes(1);
  expect(screen.queryByRole("button", { name: "核对引导结果" })).not.toBeInTheDocument();
  expect(screen.queryByText("更新当前任务")).not.toBeInTheDocument();
  expect(screen.getByRole("button", { name: "查看消息 1 全文" })).toHaveTextContent("要求 1");
});

it("retains the original promotion journal when a rejection receipt belongs to a different execution", async () => {
  const f = promotionFixture(); f.hooks.responseLost = true;
  f.hooks.post = async () => { f.hooks.executionObserved = false; };
  mount(f.client, binding, true); const user = userEvent.setup();
  await promoteFirst(user); await screen.findByText(/引导结果待确认。promotion response lost/u);
  const key = f.postControl.mock.calls[0][2], rejection = f.rejections.get(key)!;
  f.rejections.set(key, { ...rejection, receipt: { ...rejection.receipt, execution_id: "foreign-execution" } });
  await user.click(screen.getByRole("button", { name: "核对引导结果" }));
  await screen.findByText(/引导结果待确认。排队消息数据不完整或来源不匹配/u);
  expect(screen.getByRole("button", { name: "重试原引导" })).toBeEnabled();
  expect(screen.queryByText("这次引导未生效，原消息保留，可重新引导。")).not.toBeInTheDocument();
  expect(f.postControl).toHaveBeenCalledTimes(1);
  f.rejections.set(key, rejection);
  await user.click(screen.getByRole("button", { name: "核对引导结果" }));
  await screen.findByText("这次引导未生效，原消息保留，可重新引导。");
});

it("ends an absent promotion when the source is claimed without treating missing execution telemetry as proof", async () => {
  const f = promotionFixture(); f.postControl.mockRejectedValueOnce(new TypeError("connection lost"));
  mount(f.client, binding, true); const user = userEvent.setup();
  await promoteFirst(user); await screen.findByText(/引导结果待确认。connection lost/u);
  f.claim("message-1"); f.hooks.executionObserved = false;
  await user.click(screen.getByRole("button", { name: "核对引导结果" }));
  await screen.findByText(/原消息已变化或已处理，原引导请求已失效/u);
  expect(f.postControl).toHaveBeenCalledTimes(1);
  expect(screen.queryByRole("button", { name: "核对引导结果" })).not.toBeInTheDocument();
});

it("ends an absent original-version promotion only after observing a durable source revision change", async () => {
  const f = promotionFixture(); f.postControl.mockRejectedValueOnce(new TypeError("connection lost"));
  mount(f.client, binding, true); const user = userEvent.setup();
  await promoteFirst(user); await screen.findByText(/引导结果待确认。connection lost/u);
  f.revise("message-1");
  await user.click(screen.getByRole("button", { name: "核对引导结果" }));
  await screen.findByText(/原消息已变化或已处理，原引导请求已失效/u);
  expect(f.postControl).toHaveBeenCalledTimes(1);
  expect(screen.queryByRole("button", { name: "核对引导结果" })).not.toBeInTheDocument();
  expect(screen.getByRole("button", { name: "查看消息 1 全文" })).toHaveTextContent("另一窗口修改后的要求");
});

it.each([false, true])("keeps a late promotion result and its recovery bound to the original Thread, lost response=%s", async (responseLost) => {
  const f = promotionFixture(); let release!: () => void;
  f.hooks.responseLost = responseLost;
  f.hooks.post = () => new Promise<void>((resolve) => { release = resolve; });
  const ui = mount(f.client, binding, true); const user = userEvent.setup();
  await promoteFirst(user);
  await waitFor(() => expect(f.postControl).toHaveBeenCalledTimes(1));
  ui.rerender(ui.view({ ...binding, threadID: "thread-b", runID: "run-b", sessionID: "session-b" }));
  await screen.findByText("B 的要求 1");
  expect(screen.queryByRole("button", { name: "核对引导结果" })).not.toBeInTheDocument();
  await act(async () => { release(); });
  expect(screen.queryByText("已转为引导。")).not.toBeInTheDocument();
  expect(screen.getByText("B 的要求 1")).toBeInTheDocument();
  ui.rerender(ui.view());
  await screen.findByText("更新当前任务");
  if (responseLost) {
    await user.click(await screen.findByRole("button", { name: "核对引导结果" }));
    await screen.findByText("已转为引导。");
  }
  expect(screen.queryByRole("button", { name: "核对引导结果" })).not.toBeInTheDocument();
  expect(f.postControl).toHaveBeenCalledTimes(1);
});

it.each(["file", "image"] as const)("disables promotion for a %s attachment with a visible reason", async (kind) => {
  const file = { id: "file-a", workspace_id: binding.workspaceID, sha256: sha, name: "notes.txt", mime_type: "text/plain",
    byte_size: 8, readability: "text" as const, text_bytes: 8, redacted: false };
  const image = { id: "image-a", workspace_id: binding.workspaceID, sha256: sha, mime_type: "image/png" as const,
    byte_size: 128, width: 64, height: 64, name: "布局.png" };
  const f = promotionFixture([message(1, kind === "file" ? { attachments: [file] } : { images: [image] })]);
  mount(f.client, binding, true); const user = userEvent.setup();
  const action = await screen.findByRole("button", { name: "引导当前任务" });
  expect(action).toBeDisabled();
  expect(action).toHaveAttribute("title", "当前只支持文字引导；图片与附件保留在下一轮消息中。");
  expect(action).toHaveAccessibleDescription("当前只支持文字引导；图片与附件保留在下一轮消息中。");
  await user.click(action);
  expect(f.postControl).not.toHaveBeenCalled();
});

it("does not enable a promotion from owner IDs when the current execution is not runnable", async () => {
  const f = promotionFixture(); mount(f.client, binding, false);
  const action = within(await screen.findByRole("listitem", { name: "消息 1" })).getByRole("button", { name: "引导当前任务" });
  expect(action).toBeDisabled();
  expect(action).toHaveAttribute("title", "当前执行暂时无法接受引导，请刷新状态后重试。");
  expect(f.postControl).not.toHaveBeenCalled();
});

it("does not post a promotion if its original journal cannot be persisted", async () => {
  const f = promotionFixture(); mount(f.client, binding, true); const user = userEvent.setup();
  const original = Storage.prototype.setItem;
  vi.spyOn(Storage.prototype, "setItem").mockImplementation(function (this: Storage, key, value) {
    if (decodeURIComponent(key).includes("queue-operation:")) throw new DOMException("quota full", "QuotaExceededError");
    original.call(this, key, value);
  });
  await promoteFirst(user);
  await screen.findByText(/无法保存本机恢复数据/u);
  expect(f.postControl).not.toHaveBeenCalled();
  expect(screen.getByRole("button", { name: "查看消息 1 全文" })).toHaveTextContent("要求 1");
});

it("keeps a local edit visible and requires it to be handled before promoting the original message", async () => {
  const f = promotionFixture(); mount(f.client, binding, true); const user = userEvent.setup();
  const input = await editFirst(user);
  await user.clear(input); await user.type(input, "尚未保存的本地修改");
  const action = within(screen.getByRole("listitem", { name: "消息 1" })).getByRole("button", { name: "引导当前任务" });
  expect(action).toBeDisabled();
  expect(action).toHaveAttribute("title", "请先保存或取消这条消息的本地编辑。");
  expect(input).toHaveValue("尚未保存的本地修改");
  expect(f.postControl).not.toHaveBeenCalled();
});

it("unlocks a normalized no-op only after an explicit original retry returns exact proof, preserving later edits", async () => {
  const f = fixture();
  f.postControl.mockRejectedValueOnce(new TypeError("connection lost")).mockImplementation(async (_path, body, key) => {
    const hash = async (text: string) => Buffer.from(await webcrypto.subtle.digest("SHA-256", new TextEncoder().encode(text))).toString("hex");
    throw new APIRequestError("unchanged", "INVALID_ARGUMENT", 400, "", undefined, undefined, undefined, undefined,
      { version: "queue_revision_unchanged.v1", run_id: binding.runID, session_id: binding.sessionID, message_id: "message-1", expected_revision: 0,
        operation_key_sha256: await hash(key), request_content_sha256: await hash(body.content), normalized_content_sha256: sha, current_content_sha256: sha,
        execution_started: false, model_called: false, tool_called: false, capability_grant: false });
  });
  mount(f.client); const user = userEvent.setup(); const input = await editFirst(user);
  expect(screen.getByRole("button", { name: "保存修改" })).toBeDisabled();
  await user.clear(input); await user.type(input, "脱敏后等值的文字"); await user.click(screen.getByRole("button", { name: "保存修改" }));
  await screen.findByText(/保存结果待确认。connection lost/u);
  await user.click(screen.getByRole("button", { name: "核对保存结果" }));
  await screen.findByText(/尚未查到这次操作的最终记录/u);
  expect(f.postControl).toHaveBeenCalledTimes(1);
  await user.type(input, "，后来补充");
  await user.click(screen.getByRole("button", { name: "重试原保存" }));
  await screen.findByText(/未产生修订。编辑稿已保留/u);
  expect(input).toHaveValue("脱敏后等值的文字，后来补充");
  expect(screen.getByRole("button", { name: "保存修改" })).toBeEnabled();
  expect(screen.queryByRole("button", { name: "核对保存结果" })).not.toBeInTheDocument();
  expect(f.postControl.mock.calls[1]).toEqual(f.postControl.mock.calls[0]);
});

it("does not let a late refusal in one window erase a same-key commit in another window", async () => {
  const f = fixture(); const actual = f.postControl.getMockImplementation()!;
  let release!: () => void;
  f.postControl.mockImplementationOnce(async () => { await new Promise<void>((resolve) => { release = resolve; }); throw new APIRequestError("token expired", "POLICY_DENIED", 401); })
    .mockImplementationOnce(async (...args) => { await actual(...args); throw new TypeError("response lost"); });
  const first = mount(f.client); const user = userEvent.setup(); const input = await editFirst(user);
  await user.clear(input); await user.type(input, "两个窗口的同一保存"); await user.click(screen.getByRole("button", { name: "保存修改" }));
  const second = mount(f.client);
  await user.click(await within(second.container).findByRole("button", { name: "重试原保存" }));
  await within(second.container).findByText(/保存结果待确认。response lost/u);
  await act(async () => { release(); });
  await within(first.container).findByText("修改已保存。");
  // JSDOM hosts both simulated windows in one document; deliver the storage
  // notifications that a real second document receives from the first.
  await act(async () => {
    for (let index = 0; index < localStorage.length; index++) {
      const key = localStorage.key(index)!;
      if (decodeURIComponent(key).includes("queue-result:") || decodeURIComponent(key).includes("queue-editor-closed:")) {
        window.dispatchEvent(new StorageEvent("storage", { key, newValue: localStorage.getItem(key), storageArea: localStorage }));
      }
    }
  });
  await waitFor(() => expect(within(second.container).queryByRole("button", { name: "核对保存结果" })).not.toBeInTheDocument());
  expect(f.postControl.mock.calls[1]).toEqual(f.postControl.mock.calls[0]);
  expect(screen.queryByRole("textbox")).not.toBeInTheDocument();
});

it("keeps the operation recoverable if persisting a verified unchanged result fails", async () => {
  const f = fixture();
  f.postControl.mockImplementation(async (_path, body, key) => {
    const hash = async (text: string) => Buffer.from(await webcrypto.subtle.digest("SHA-256", new TextEncoder().encode(text))).toString("hex");
    throw new APIRequestError("unchanged", "INVALID_ARGUMENT", 400, "", undefined, undefined, undefined, undefined,
      { version: "queue_revision_unchanged.v1", run_id: binding.runID, session_id: binding.sessionID, message_id: "message-1", expected_revision: 0,
        operation_key_sha256: await hash(key), request_content_sha256: await hash(body.content), normalized_content_sha256: sha, current_content_sha256: sha,
        execution_started: false, model_called: false, tool_called: false, capability_grant: false });
  });
  const original = Storage.prototype.setItem;
  vi.spyOn(Storage.prototype, "setItem").mockImplementation(function (this: Storage, key, value) {
    if (decodeURIComponent(key).includes("queue-result:")) throw new DOMException("quota full", "QuotaExceededError");
    original.call(this, key, value);
  });
  mount(f.client); const user = userEvent.setup(); const input = await editFirst(user);
  await user.clear(input); await user.type(input, "保留编辑稿"); await user.click(screen.getByRole("button", { name: "保存修改" }));
  await screen.findByText(/结果记录暂未保存，原操作和编辑稿已保留/u);
  expect(input).toHaveValue("保留编辑稿");
  expect(screen.getByRole("button", { name: "核对保存结果" })).toBeEnabled();
  expect(f.postControl).toHaveBeenCalledTimes(1);
});

// Retain textarea coverage through the supported plain-text editing mode.
beforeEach(() => localStorage.setItem("universal-code.composer-plain-text", "true"));
