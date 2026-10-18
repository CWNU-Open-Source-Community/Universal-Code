import { act, cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { useState, type ReactNode } from "react";
import type { APIClient } from "../api/client";
import type { ThreadExecutionView } from "../api/types";
import { v2QueryKeys } from "./query-keys";
import { useConnectionStore } from "../state/connection";
import { V2InspectorHome } from "./components/inspector-home";
import { V2InspectorTools } from "./components/inspector-tools";

const submitResource = vi.hoisted(() => vi.fn());

function ResourceProbe({ kind, id }: { kind: string; id: string }) {
  const [draft, setDraft] = useState("");
  const [uncertain, setUncertain] = useState(false);
  return <div><output aria-label="Resource identity">{`${kind}:${id}`}</output>
    <output aria-label="Resource operation state">{uncertain ? "uncertain" : "idle"}</output>
    <input aria-label="Resource-scoped draft" value={draft} onChange={(event) => setDraft(event.target.value)} />
    <button type="button" onClick={() => setUncertain(true)}>Mark resource operation uncertain</button>
    <button type="button" disabled={!draft} onClick={() => submitResource(kind, id, draft)}>Submit resource draft</button>
  </div>;
}

vi.mock("../components/run-workspace", () => ({ RunWorkspace: ({ runID, initialTab }: { runID: string; initialTab?: string }) =>
  <><output aria-label="Run entry pane">{initialTab ?? "activity"}</output><ResourceProbe kind="run" id={runID} /></> }));
vi.mock("../components/session-workspace", () => ({ SessionWorkspace: ({ sessionID }: { sessionID: string }) => <ResourceProbe kind="session" id={sessionID} /> }));
vi.mock("../components/workbench-frame", () => ({ WorkbenchFrame: ({ children }: { children: ReactNode }) => <div>{children}</div> }));
vi.mock("../components/scheduled-tasks-workspace", () => ({ ScheduledTasksWorkspace: () => null }));

afterEach(() => { cleanup(); submitResource.mockClear(); useConnectionStore.getState().disconnect(); });

it.each(["context", "checkpoints", "tools", "ui-evidence"] as const)("opens the existing %s pane on the exact Run and refuses a missing identity", (pane) => {
  const props = { client: {} as APIClient, tool: "run" as const, threadID: "", pane,
    onBack: vi.fn(), onOpenSettings: vi.fn() };
  const view = render(<V2InspectorTools {...props} resourceID="run-exact" />);
  expect(screen.getByLabelText("Run entry pane")).toHaveTextContent(pane);
  expect(screen.getByLabelText("Resource identity")).toHaveTextContent("run:run-exact");
  view.rerender(<V2InspectorTools {...props} resourceID="" />);
  expect(screen.getByRole("alert")).toHaveTextContent("地址缺少记录标识");
  expect(screen.queryByLabelText("Resource identity")).not.toBeInTheDocument();
  expect(submitResource).not.toHaveBeenCalled();
});

it.each(["run", "session"] as const)("does not transfer %s draft or uncertain operation state to another resource", (tool) => {
  const client = {} as APIClient;
  const props = { client, tool, threadID: "", onBack: vi.fn(), onOpenSettings: vi.fn() };
  const view = render(<V2InspectorTools {...props} resourceID="resource-a" />);
  fireEvent.change(screen.getByLabelText("Resource-scoped draft"), { target: { value: "Only for resource A" } });
  fireEvent.click(screen.getByRole("button", { name: "Mark resource operation uncertain" }));
  expect(screen.getByLabelText("Resource operation state")).toHaveTextContent("uncertain");
  view.rerender(<V2InspectorTools {...props} resourceID="resource-b" />);
  expect(screen.getByLabelText("Resource identity")).toHaveTextContent(`${tool}:resource-b`);
  expect(screen.getByLabelText("Resource-scoped draft")).toHaveValue("");
  expect(screen.getByLabelText("Resource operation state")).toHaveTextContent("idle");
  expect(screen.getByRole("button", { name: "Submit resource draft" })).toBeDisabled();
  expect(submitResource).not.toHaveBeenCalled();
});

it("keeps direct resource navigation unbound to a previously selected Thread and exposes explicit exits", () => {
  useConnectionStore.getState().selectThread("unrelated-stale-thread");
  const onBack = vi.fn();
  const onOpenSettings = vi.fn();
  render(<V2InspectorTools client={{} as APIClient} tool="run" resourceID="run-history"
    threadID="" onBack={onBack} onOpenSettings={onOpenSettings} />);
  expect(screen.getByText(/此记录尚未绑定对话，请返回任务观察选择来源任务后设置权限/)).toBeInTheDocument();
  expect(screen.queryByText(/unrelated-stale-thread/)).not.toBeInTheDocument();
  expect(screen.getByRole("button", { name: "设置" })).not.toBeVisible();
  fireEvent.click(screen.getByText("来源与设置"));
  expect(screen.getByText("run-history")).toBeVisible();
  fireEvent.click(screen.getByRole("button", { name: "设置" }));
  expect(onOpenSettings).toHaveBeenCalledExactlyOnceWith("general");
  fireEvent.click(screen.getByRole("button", { name: "返回任务观察" }));
  expect(onBack).toHaveBeenCalledOnce();
  expect(submitResource).not.toHaveBeenCalled();
});

it("keeps saved records, scheduled observation and connections directly available without fetching", () => {
  const getPage = vi.fn(), onOpenTool = vi.fn(), onOpenSettings = vi.fn();
  render(<QueryClientProvider client={new QueryClient()}><V2InspectorHome client={{ getPage } as unknown as APIClient}
    onOpenTool={onOpenTool} onOpenSettings={onOpenSettings} /></QueryClientProvider>);
  expect(screen.getByRole("button", { name: "运行记录" })).toBeVisible();
  expect(screen.getByRole("button", { name: "会话记录" })).toBeVisible();
  expect(screen.getByRole("button", { name: "自动化" })).toBeVisible();
  expect(screen.getByRole("button", { name: "连接与环境" })).toBeVisible();
  expect(getPage).not.toHaveBeenCalled();
  fireEvent.click(screen.getByRole("button", { name: "自动化" }));
  expect(onOpenTool).toHaveBeenCalledExactlyOnceWith("schedule");
  fireEvent.click(screen.getByRole("button", { name: "连接与环境" }));
  expect(onOpenSettings).toHaveBeenCalledExactlyOnceWith("connections");
  expect(getPage).not.toHaveBeenCalled();
});

it("keeps source Thread activity separate from a historical resource and does not reuse it after read errors or navigation", async () => {
  const idle = (thread_id: string): ThreadExecutionView => ({ version: "thread_execution.v1", thread_id,
    state: "idle", queued_messages: 0, capability_grant: false });
  let resolveNext!: (state: ThreadExecutionView) => void;
  const threadExecution = vi.fn().mockResolvedValue(idle("task-a"));
  const client = { hasThreadExecutionRead: true, threadExecution } as unknown as APIClient;
  const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  const tree = (threadID: string) => <QueryClientProvider client={queryClient}>
    <V2InspectorTools client={client} tool="run" resourceID="historical-run" threadID={threadID}
      onBack={vi.fn()} onOpenSettings={vi.fn()} />
  </QueryClientProvider>;
  const view = render(tree("task-a"));
  const activity = () => screen.getByRole("status", { name: "来源任务的 Agent 活动" });
  await waitFor(() => expect(activity()).toHaveTextContent("来源任务的 Agent：等待新消息"));
  expect(screen.getByLabelText("Resource identity")).toHaveTextContent("run:historical-run");
  expect(screen.getByText(/下方是所选记录，可能来自较早的执行/)).toBeInTheDocument();
  await act(async () => { queryClient.setQueryData(v2QueryKeys.execution("task-a"), {
    ...idle("task-a"), state: "running", execution_id: "live-execution-a",
  }); });
  await waitFor(() => expect(activity()).toHaveTextContent("来源任务的 Agent：正在工作"));
  threadExecution.mockRejectedValue(new Error("execution read unavailable"));
  await act(async () => { await queryClient.invalidateQueries({ queryKey: v2QueryKeys.execution("task-a") }); });
  await waitFor(() => expect(activity()).toHaveTextContent("来源任务的 Agent：状态读取失败"));
  expect(activity()).not.toHaveTextContent("来源任务的 Agent：正在工作");
  expect(screen.getByRole("button", { name: "刷新执行状态" })).toBeVisible();
  threadExecution.mockImplementation(() => new Promise<ThreadExecutionView>((resolve) => { resolveNext = resolve; }));
  view.rerender(tree("task-b"));
  expect(activity()).toHaveTextContent("来源任务的 Agent：正在同步状态");
  await act(async () => { resolveNext({ ...idle("task-b"), state: "stopping", execution_id: "live-execution-b" }); });
  await waitFor(() => expect(activity()).toHaveTextContent("来源任务的 Agent：正在停止"));
  expect(threadExecution).toHaveBeenLastCalledWith("task-b", expect.any(AbortSignal));
  view.unmount(); queryClient.clear();
});
