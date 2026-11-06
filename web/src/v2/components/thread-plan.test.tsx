import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act, cleanup, render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { APIRequestError, type APIClient } from "../../api/client";
import { parseThreadPlanObservation, type ThreadPlanAttempt, type ThreadPlanRequest } from "../../api/thread-plan";
import { LocaleProvider } from "../../lib/locale";
import { V2RecoveryProvider, useV2RecoveryStore, type V2RecoveryStore } from "../recovery-storage";
import { V2ThreadPlanControl } from "./thread-plan";

function fixture() {
  const directions = [{ ordinal: 1, title: "整理日志", summary: "只生成汇总文件，保留接口。",
    tradeoffs: ["优先复用现有解析器"], modules: [{ ordinal: 1, title: "汇总", objective: "输出中文汇总", acceptance_criteria: ["不修改原始日志"], dependencies: [] }] }];
  const state: { phase: string; proposal: string | null; directions: typeof directions;
    selection?: { proposal_id: string; direction_ordinal: number } } = { phase: "plan", proposal: "proposal-1", directions };
  const result = (threadID: string, body: ThreadPlanRequest, status = "completed") => ({
    version: "plan_delivery_control.v1", thread_id: threadID, run_id: body.run_id, action: body.action, state: status,
    proposal_id: body.proposal_id, direction: body.direction, manual_acceptance: body.manual_acceptance,
    selection_id: body.proposal_id ? "selection-1" : undefined,
    applied_mode: status === "not_received" || status === "prepared" ? undefined : { protocol_version: "run_mode.v1", phase: body.action === "enter_plan" ? "plan" : "deliver" },
    turn_request: body.action === "confirm" && status !== "not_received" && status !== "prepared"
      ? { thread_id: threadID, run_id: body.run_id, message_id: "message-confirm", state: status, settled: status !== "received" } : undefined,
    execution_started: false, model_called: false, tool_called: false, capability_grant: false,
  });
  const requests = new Map<string, { threadID: string; body: ThreadPlanRequest }>();
  const hooks: { execute?: (body: ThreadPlanRequest) => Promise<unknown>; observe?: () => unknown } = {};
  const get = vi.fn(async (path: string) => ({ run: { id: path.split("/")[2], status: "running" }, mode: { phase: state.phase },
    plan_delivery: { proposal: state.proposal ? { id: state.proposal, directions: state.directions } : undefined, selection: state.selection } }));
  const postControl = vi.fn(async (path: string, body: ThreadPlanRequest, key: string) => {
    const threadID = path.split("/")[2]; requests.set(key, { threadID, body });
    if (hooks.execute) return hooks.execute(body);
    if (body.action !== "confirm") state.phase = body.action === "enter_plan" ? "plan" : "deliver";
    return result(threadID, body);
  });
  const inspectThreadPlanRequest = vi.fn(async (threadID: string, runID: string, action: ThreadPlanRequest["action"], key: string) =>
    hooks.observe ? hooks.observe() : result(threadID, requests.get(key)?.body ?? { version: "plan_delivery_control.v1", action, run_id: runID }, "not_received"));
  const client = { baseURL: "http://localhost/api/v1", hasThreadControl: true, hasPlanDelivery: true, get, postControl, inspectThreadPlanRequest } as unknown as APIClient;
  return { state, hooks, result, client, get, postControl, inspectThreadPlanRequest };
}
function mount(f: ReturnType<typeof fixture>, threadID = "task-a", draft = false) {
  const queries = new QueryClient({ defaultOptions: { queries: { retry: false }, mutations: { retry: false } } });
  let store: V2RecoveryStore;
  function Probe() { store = useV2RecoveryStore()!; return null; }
  const onRequestChange = vi.fn();
  const onSubmit = vi.fn();
  const wrap = (id: string, hasUnsentDraft: boolean) => <LocaleProvider><QueryClientProvider client={queries}><V2RecoveryProvider client={f.client} scopeID="plan-fixture">
    <Probe /><form onSubmit={(event) => { event.preventDefault(); onSubmit(); }}>
      <textarea aria-label="未发送草稿" defaultValue={draft ? "保留这份未发送的修改要求" : ""} />
      <V2ThreadPlanControl client={f.client} threadID={id} runID={`${id}-run`} active working={false} hasUnsentDraft={hasUnsentDraft} onRequestChange={onRequestChange} />
    </form>
  </V2RecoveryProvider></QueryClientProvider></LocaleProvider>;
  const view = render(wrap(threadID, draft));
  return { ...view, queries, getStore: () => store!, onSubmit, onRequestChange, change: (id: string, hasDraft = false) => view.rerender(wrap(id, hasDraft)) };
}
afterEach(() => { cleanup(); localStorage.clear(); });
beforeEach(() => localStorage.setItem("prayu.locale.v1", "zh-CN"));

test("confirmation stays in the same task and blocks unsent corrections until the refreshed plan is reviewed", async () => {
  const f = fixture(); const view = mount(f, "task-a", true); const user = userEvent.setup();
  await user.click(await screen.findByRole("button", { name: "查看计划" }));
  expect(screen.getByRole("button", { name: "确认计划并执行" })).toBeDisabled();
  expect(f.postControl).not.toHaveBeenCalled();
  await user.click(screen.getByRole("button", { name: "返回对话修改要求" }));
  expect(view.onRequestChange).toHaveBeenCalledWith("");
  f.state.proposal = "proposal-corrected";
  view.change("task-a");
  await act(async () => { await view.queries.invalidateQueries({ queryKey: ["run", "task-a-run"] }); });
  await user.click(screen.getByRole("button", { name: "查看计划" }));
  await user.click(screen.getByRole("button", { name: "确认计划并执行" }));
  await waitFor(() => expect(f.postControl).toHaveBeenCalledTimes(1));
  expect(f.postControl.mock.calls[0]).toEqual(["/threads/task-a/plan", expect.objectContaining({
    run_id: "task-a-run", proposal_id: "proposal-corrected", direction: 1, manual_acceptance: "on_demand", action: "confirm",
    content: expect.stringContaining("保留原目标、后续修正和限制"),
  }), expect.stringMatching(/^thread-plan-/u)]);
});

test("lost response survives remount, only observes its original key and never offers to cancel an absent in-flight request", async () => {
  const f = fixture(); f.hooks.execute = async () => { throw new Error("connection lost"); };
  const view = mount(f); const user = userEvent.setup();
  await user.click(await screen.findByRole("button", { name: "查看计划" }));
  await user.click(screen.getByRole("button", { name: "确认计划并执行" }));
  await screen.findByText("connection lost");
  const key = f.postControl.mock.calls[0][2];
  f.state.proposal = null;
  view.unmount();
  const reopened = mount(f);
  await user.click(await screen.findByRole("button", { name: "计划操作待核对" }));
  await screen.findByText(/服务端尚未找到原请求/u);
  expect(f.postControl).toHaveBeenCalledTimes(1);
  expect(f.inspectThreadPlanRequest).toHaveBeenLastCalledWith("task-a", "task-a-run", "confirm", key, expect.any(AbortSignal));
  expect(screen.queryByRole("button", { name: /取消本次|关闭已拒绝/u })).not.toBeInTheDocument();
  await user.click(screen.getByRole("button", { name: "继续原操作" }));
  await waitFor(() => expect(f.postControl).toHaveBeenCalledTimes(2));
  expect(f.postControl.mock.calls[1]).toEqual(f.postControl.mock.calls[0]);
  expect(reopened.getStore().read<ThreadPlanAttempt | null>("thread:task-a:plan-attempt", null)?.key).toBe(key);
});

test("a late confirmation response cannot clear or display another task's draft or plan operation", async () => {
  const f = fixture(); let release!: (value: unknown) => void;
  f.hooks.execute = () => new Promise((resolve) => { release = resolve; });
  const view = mount(f); const user = userEvent.setup();
  await user.click(await screen.findByRole("button", { name: "查看计划" }));
  await user.click(screen.getByRole("button", { name: "确认计划并执行" }));
  await waitFor(() => expect(f.postControl).toHaveBeenCalledTimes(1));
  view.change("task-b", true);
  await act(async () => { release(f.result("task-a", f.postControl.mock.calls[0][1])); });
  await user.click(await screen.findByRole("button", { name: "查看计划" }));
  expect(screen.getByRole("button", { name: "确认计划并执行" })).toBeDisabled();
  expect(view.onRequestChange).not.toHaveBeenCalled();
  expect(screen.queryByText(/计划已确认，本次执行/u)).not.toBeInTheDocument();
  expect(view.getStore().read("thread:task-b:plan-attempt", null)).toBeNull();
});

test("switching an existing direct conversation to planning preserves the draft and submits only the mode action", async () => {
  const f = fixture(); f.state.phase = "deliver"; f.state.proposal = null;
  const view = mount(f, "task-a", true); const user = userEvent.setup();
  const toggle = await screen.findByRole("button", { name: "计划模式", pressed: false });
  expect(screen.queryByRole("combobox", { name: "工作方式" })).not.toBeInTheDocument();
  expect(screen.queryByRole("button", { name: "直接执行" })).not.toBeInTheDocument();
  expect(screen.queryByRole("button", { name: "计划" })).not.toBeInTheDocument();
  await user.click(toggle);
  await waitFor(() => expect(f.postControl).toHaveBeenCalledTimes(1));
  expect(f.postControl.mock.calls[0][1]).toEqual({ version: "plan_delivery_control.v1", action: "enter_plan", run_id: "task-a-run" });
  await screen.findByRole("button", { name: "计划模式", pressed: true });
  expect(screen.queryByRole("button", { name: "查看计划" })).not.toBeInTheDocument();
  expect(screen.queryByRole("dialog")).not.toBeInTheDocument();
  expect(screen.getByRole("textbox", { name: "未发送草稿" })).toHaveValue("保留这份未发送的修改要求");
  expect(view.onSubmit).not.toHaveBeenCalled();
  expect(view.onRequestChange).not.toHaveBeenCalled();
});

test("known rejection can be dismissed only after a read proves no operation was recorded", async () => {
  const f = fixture(); f.hooks.execute = async () => { throw new APIRequestError("计划已过期", "CONFLICT", 409); };
  const view = mount(f); const user = userEvent.setup();
  await user.click(await screen.findByRole("button", { name: "查看计划" }));
  await user.click(screen.getByRole("button", { name: "确认计划并执行" }));
  await user.click(await screen.findByRole("button", { name: "关闭已拒绝请求" }));
  expect(view.getStore().read("thread:task-a:plan-attempt", null)).toBeNull();
  expect(f.postControl).toHaveBeenCalledTimes(1);
});

test("changing back to direct execution only switches mode and preserves an unsent correction", async () => {
  const f = fixture(); f.state.proposal = null;
  const view = mount(f, "task-a", true); const user = userEvent.setup();
  const toggle = await screen.findByRole("button", { name: "计划模式", pressed: true });
  toggle.focus(); await user.keyboard("{Enter}");
  await waitFor(() => expect(f.postControl).toHaveBeenCalledTimes(1));
  expect(f.postControl.mock.calls[0][1]).toEqual({ version: "plan_delivery_control.v1", action: "enter_deliver", run_id: "task-a-run" });
  await screen.findByRole("button", { name: "计划模式", pressed: false });
  expect(screen.queryByRole("dialog")).not.toBeInTheDocument();
  expect(screen.getByRole("textbox", { name: "未发送草稿" })).toHaveValue("保留这份未发送的修改要求");
  expect(view.onSubmit).not.toHaveBeenCalled();
  expect(view.onRequestChange).not.toHaveBeenCalled();
});

test("a plan read failure remains reachable without a proposal and can be retried without a write", async () => {
  const f = fixture(); f.state.proposal = null;
  f.get.mockRejectedValueOnce(new Error("offline"));
  mount(f); const user = userEvent.setup();
  await user.click(await screen.findByRole("button", { name: "计划读取失败" }));
  await screen.findByText(/尚无可确认的计划/u);
  await user.keyboard("{Escape}");
  expect(screen.queryByRole("dialog")).not.toBeInTheDocument();
  expect(screen.getByRole("button", { name: "计划模式" })).toHaveFocus();
  expect(f.postControl).not.toHaveBeenCalled();
});

test("a damaged original plan request remains accessible without a proposal and cannot be replaced", async () => {
  const f = fixture(); f.state.proposal = null;
  const view = mount(f); const user = userEvent.setup();
  await screen.findByRole("button", { name: "计划模式" });
  act(() => view.getStore().write("thread:task-a:plan-attempt", { key: "damaged-original" }));
  await user.click(await screen.findByRole("button", { name: "计划操作待核对" }));
  await screen.findByText(/本机计划请求记录无法读取/u);
  expect(screen.queryByRole("button", { name: "继续原操作" })).not.toBeInTheDocument();
  expect(f.postControl).not.toHaveBeenCalled();
  expect(view.getStore().read("thread:task-a:plan-attempt", null)).toEqual({ key: "damaged-original" });
});

test("settled confirmation needs the exact selected plan and accepted message identity", () => {
  const f = fixture(); const attempt: ThreadPlanAttempt = { threadID: "task-a", key: "thread-plan-test", body: {
    version: "plan_delivery_control.v1", action: "confirm", run_id: "task-a-run", proposal_id: "p1", direction: 1, manual_acceptance: "on_demand", content: "执行" } };
  const valid = f.result("task-a", attempt.body);
  expect(parseThreadPlanObservation(valid, attempt).state).toBe("completed");
  expect(() => parseThreadPlanObservation({ ...valid, proposal_id: undefined }, attempt)).toThrow(/来源或格式/u);
  expect(() => parseThreadPlanObservation({ ...valid, turn_request: { ...valid.turn_request, thread_id: "task-b" } }, attempt)).toThrow(/来源或格式/u);
});

test.each(["plan", "deliver"])("reopening a %s plan shows the server-confirmed direction as read-only", async (phase) => {
  const f = fixture(); f.state.phase = phase;
  f.state.directions.push({ ...f.state.directions[0], ordinal: 2, title: "建立日志索引" });
  f.state.selection = { proposal_id: "proposal-1", direction_ordinal: 2 };
  const user = userEvent.setup();
  const view = mount(f);
  await user.click(await screen.findByRole("button", { name: "查看计划" }));
  expect(screen.getByRole("radio", { name: "建立日志索引 已确认" })).toBeChecked();
  await user.click(screen.getByRole("radio", { name: "整理日志" }));
  expect(screen.getByRole("radio", { name: "整理日志" })).not.toBeChecked();
  expect(screen.getByRole("radio", { name: "建立日志索引 已确认" })).toBeDisabled();
  expect(screen.queryByRole("button", { name: "确认计划并执行" })).not.toBeInTheDocument();
  view.unmount();
  mount(f);
  await user.click(await screen.findByRole("button", { name: "查看计划" }));
  expect(screen.getByRole("radio", { name: "建立日志索引 已确认" })).toBeChecked();
  expect(f.postControl).not.toHaveBeenCalled();
});

test.each([
  { name: "another proposal", selection: { proposal_id: "old-proposal", direction_ordinal: 1 } },
  { name: "a missing direction", selection: { proposal_id: "proposal-1", direction_ordinal: 2 } },
  { name: "no saved selection", selection: undefined },
])("a delivered plan does not invent a confirmed choice from $name", async ({ selection }) => {
  const f = fixture(); f.state.phase = "deliver"; f.state.selection = selection;
  mount(f); const user = userEvent.setup();
  await user.click(await screen.findByRole("button", { name: "查看计划" }));
  expect(screen.getByRole("radio", { name: "整理日志" })).not.toBeChecked();
  expect(screen.getByRole("radio", { name: "整理日志" })).toBeDisabled();
  expect(screen.queryByText("已确认")).not.toBeInTheDocument();
  expect(f.postControl).not.toHaveBeenCalled();
});

test("a replacement proposal requires a fresh choice instead of inheriting the saved confirmation", async () => {
  const f = fixture(); f.state.phase = "deliver";
  f.state.directions.push({ ...f.state.directions[0], ordinal: 2, title: "建立日志索引" });
  f.state.selection = { proposal_id: "proposal-1", direction_ordinal: 2 };
  const view = mount(f); const user = userEvent.setup();
  await user.click(await screen.findByRole("button", { name: "查看计划" }));
  f.state.phase = "plan"; f.state.proposal = "proposal-2";
  await act(async () => { await view.queries.invalidateQueries({ queryKey: ["run", "task-a-run"] }); });
  await screen.findByRole("dialog", { name: "确认计划" });
  expect(screen.queryByText("已确认")).not.toBeInTheDocument();
  expect(screen.getByRole("radio", { name: "建立日志索引" })).not.toBeChecked();
  expect(screen.getByRole("button", { name: "确认计划并执行" })).toBeDisabled();
  await user.click(screen.getByRole("radio", { name: "整理日志" }));
  await user.click(screen.getByRole("button", { name: "确认计划并执行" }));
  await waitFor(() => expect(f.postControl).toHaveBeenCalledTimes(1));
  expect(f.postControl.mock.calls[0][1]).toMatchObject({ proposal_id: "proposal-2", direction: 1 });
});

test("a refreshed server confirmation replaces an unsaved local choice", async () => {
  const f = fixture(); f.state.directions.push({ ...f.state.directions[0], ordinal: 2, title: "建立日志索引" });
  const view = mount(f); const user = userEvent.setup();
  await user.click(await screen.findByRole("button", { name: "查看计划" }));
  await user.click(screen.getByRole("radio", { name: "整理日志" }));
  f.state.phase = "deliver"; f.state.selection = { proposal_id: "proposal-1", direction_ordinal: 2 };
  await act(async () => { await view.queries.invalidateQueries({ queryKey: ["run", "task-a-run"] }); });
  await screen.findByRole("radio", { name: "建立日志索引 已确认" });
  expect(screen.getByRole("radio", { name: "整理日志" })).not.toBeChecked();
  expect(screen.getByRole("radio", { name: "建立日志索引 已确认" })).toBeChecked();
  expect(f.postControl).not.toHaveBeenCalled();
});

test("a saved selection still allows continuing its prepared original request", async () => {
  const f = fixture(); f.state.selection = { proposal_id: "proposal-1", direction_ordinal: 1 };
  const attempt: ThreadPlanAttempt = { threadID: "task-a", key: "thread-plan-prepared", body: {
    version: "plan_delivery_control.v1", action: "confirm", run_id: "task-a-run", proposal_id: "proposal-1",
    direction: 1, manual_acceptance: "on_demand", content: "继续原计划" } };
  f.hooks.observe = () => f.result("task-a", attempt.body, "prepared");
  const view = mount(f); const user = userEvent.setup();
  await screen.findByRole("button", { name: "查看计划" });
  act(() => view.getStore().write("thread:task-a:plan-attempt", attempt));
  await user.click(await screen.findByRole("button", { name: "计划操作待核对" }));
  expect(screen.getByRole("radio", { name: "整理日志 已确认" })).toBeChecked();
  expect(screen.queryByRole("button", { name: "确认计划并执行" })).not.toBeInTheDocument();
  await user.click(await screen.findByRole("button", { name: "继续原操作" }));
  await waitFor(() => expect(f.postControl).toHaveBeenCalledTimes(1));
  expect(f.postControl.mock.calls[0]).toEqual(["/threads/task-a/plan", attempt.body, attempt.key]);
});
