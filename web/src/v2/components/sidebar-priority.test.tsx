import type { ComponentProps } from "react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { render, screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import type { ThreadExecutionView, ThreadTranscriptItemView, ThreadView } from "../../api/types";
import { LocaleProvider } from "../../lib/locale";
import { v2QueryKeys } from "../query-keys";
import { V2RecoveryProvider } from "../recovery-storage";
import { V2Sidebar } from "./sidebar";

const when = "2026-11-20T12:00:00Z";
function chat(id: string, state: NonNullable<ThreadView["execution_state"]>): ThreadView {
  const ended = state === "completed" || state === "failed" || state === "cancelled";
  return {
    protocol_version: "thread.v1", id, title: id, mission_id: `mission-${id}`, workspace_id: "desk", status: "active",
    active_run_id: ended ? undefined : `run-${id}`, last_run_id: `run-${id}`, version: 1,
    composer_state: ended ? "successor_required" : state === "waiting_approval" ? "waiting_approval" : "ready",
    execution_state: state, created_at: when, updated_at: when,
  };
}

function sidebar(threads: ThreadView[], props: Partial<ComponentProps<typeof V2Sidebar>>, client: QueryClient) {
  return <QueryClientProvider client={client}><LocaleProvider>
    <V2RecoveryProvider client={{ baseURL: "/api/v1" }} scopeID="priority-test">
      <V2Sidebar workspaces={[{ id: "desk", name: "Desk", created_at: when }]} threads={threads}
        selectedThreadID="" searchOpen={false} onSearchOpen={vi.fn()} onNewConversation={vi.fn()} onOpenModels={vi.fn()}
        onSelectThread={vi.fn()} onOpenSettings={vi.fn()} onArchive={vi.fn()} {...props} />
    </V2RecoveryProvider></LocaleProvider></QueryClientProvider>;
}

async function enablePriority(user: ReturnType<typeof userEvent.setup>) {
  await user.click(screen.getByLabelText("项目选项"));
  await user.click(screen.getByRole("button", { name: "按优先级分组" }));
}

function group(name: string) { return within(screen.getByRole("region", { name })); }

beforeEach(() => { localStorage.clear(); localStorage.setItem("prayu.locale.v1", "zh-CN"); });

it("groups list rows in priority order, keeps pins first and restores the selected view", async () => {
  const waiting = { ...chat("approval", "running"), composer_state: "waiting_approval" as const };
  const running = chat("working", "running");
  const done = chat("finished", "completed");
  const idle = chat("between turns", "idle");
  const pinned = { ...chat("pinned", "idle"), pinned: true };
  const rows = [idle, done, pinned, running, waiting];
  const onVisibleChatOrder = vi.fn();
  const props = { unreadThreadIDs: new Set([waiting.id, running.id, done.id]), onVisibleChatOrder };
  const client = new QueryClient();
  const user = userEvent.setup();
  const view = render(sidebar(rows, props, client));
  expect(screen.getByRole("region", { name: "项目 Desk" })).toBeVisible();
  await enablePriority(user);
  expect(group("需用户输入").getByRole("button", { name: waiting.title })).toBeVisible();
  expect(group("运行中").getByRole("button", { name: running.title })).toBeVisible();
  expect(group("未读").getByRole("button", { name: done.title })).toHaveAccessibleDescription("已完成 未读");
  expect(group("空闲").getByRole("button", { name: idle.title })).toBeVisible();
  expect(onVisibleChatOrder).toHaveBeenLastCalledWith([pinned.id, waiting.id, running.id, done.id, idle.id]);
  await user.click(group("运行中").getByRole("button", { expanded: true }));
  expect(onVisibleChatOrder).toHaveBeenLastCalledWith([pinned.id, waiting.id, done.id, idle.id]);
  view.rerender(sidebar(rows, { ...props, searchOpen: true }, client));
  expect(screen.queryByRole("region", { name: "空闲" })).toBeNull();
  expect(onVisibleChatOrder).toHaveBeenLastCalledWith([]);
  view.unmount();
  render(sidebar(rows, props, client));
  expect(group("运行中").getByRole("button", { name: running.title })).toBeVisible();
  await user.click(screen.getByLabelText("分组选项"));
  await user.click(screen.getByRole("button", { name: "按项目分组" }));
  expect(group("项目 Desk").getByRole("button", { name: done.title })).toBeVisible();
});

it("uses a refreshed thread list when a past tool was blocked or a previous turn is cached as running", async () => {
  const done = chat("prior blocked tool", "completed");
  const open = chat("open run", "running");
  const client = new QueryClient();
  const blocked: ThreadTranscriptItemView = {
    id: "call", canonical_id: "call", durable_call_id: "call", run_id: done.last_run_id, run_ordinal: 1,
    sequence: 2, stage: "blocked", kind: "tool_call", title: "Command denied", activity_type: "execute",
    source: "harness", durable: true, provisional: false, instruction_authorized: false, verifiable: true,
    version: "thread_transcript.v1", created_at: when,
  };
  client.setQueryData(v2QueryKeys.transcript(done.id), { pages: [{ items: [blocked] }], pageParams: [""] });
  client.setQueryData<ThreadExecutionView>(v2QueryKeys.execution(open.id), {
    version: "thread_execution.v1", thread_id: open.id, execution_id: "previous-turn", state: "running",
    capability_grant: false, queued_messages: 0,
  }, { updatedAt: Date.parse(when) - 1000 });
  const props = { unreadThreadIDs: new Set([done.id]) };
  const view = render(sidebar([done, open], props, client));
  await enablePriority(userEvent.setup());
  expect(group("未读").getByRole("button", { name: done.title })).toBeVisible();
  expect(group("运行中").getByRole("button", { name: open.title })).toBeVisible();
  view.rerender(sidebar([done, { ...open, execution_state: "idle" }], props, client));
  expect(group("空闲").getByRole("button", { name: open.title })).toBeVisible();
  expect(screen.queryByRole("region", { name: "运行中" })).toBeNull();
  expect(screen.queryByRole("region", { name: "需用户输入" })).toBeNull();
});

it("keeps unread markers on read failure and shows the load-more action for an incomplete list", async () => {
  const client = new QueryClient();
  const waiting = chat("last known approval", "waiting_approval");
  const unseen = chat("unread chat", "idle");
  const view = render(sidebar([waiting, unseen], { loadFailed: true, unreadThreadIDs: new Set([unseen.id]) }, client));
  await enablePriority(userEvent.setup());
  expect(group("状态未知").getByRole("button", { name: waiting.title })).toBeVisible();
  expect(group("未读").getByRole("button", { name: unseen.title })).toBeVisible();
  expect(screen.queryByRole("region", { name: "需用户输入" })).toBeNull();
  const onLoadMore = vi.fn();
  view.rerender(sidebar([], { hasMore: true, onLoadMore }, client));
  expect(screen.queryByText("暂无对话")).toBeNull();
  await userEvent.click(screen.getByRole("button", { name: "加载更早对话" }));
  expect(onLoadMore).toHaveBeenCalledOnce();
  view.rerender(sidebar([], {}, client));
  expect(screen.getByText("暂无对话")).toBeVisible();
});
