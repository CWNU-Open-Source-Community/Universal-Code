import type { ReactElement } from "react";
import type { RenderOptions } from "@testing-library/react";
import { LocaleProvider } from "../../lib/locale";
import { QueryClient, QueryClientProvider, useQuery } from "@tanstack/react-query";
import { act, render as renderComponent, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { useState } from "react";
import { APIRequestError, APIClient } from "../../api/client";
import type { RunLifecycleControlView, ThreadExecutionView } from "../../api/types";
import { v2QueryKeys } from "../query-keys";
import { useV2ThreadExecution, V2PausedThreadControl, V2ThreadExecutionControl } from "./thread-execution-control";

function ObservedControl({ client }: { client: APIClient }) {
  const query = useV2ThreadExecution(client, "thread-a");
  return <><output>{query.data?.state ?? "unavailable"}</output>
    <V2ThreadExecutionControl client={client} threadID="thread-a" execution={query.data} /></>;
}

it("only lifts a pause on an explicit click without resubmitting the failed tool or a message", async () => {
  const controlRunLifecycle = vi.fn().mockRejectedValueOnce(new Error("connection interrupted"))
    .mockResolvedValue({});
  const submitThreadTurn = vi.fn();
  const client = { hasRunLifecycle: true, controlRunLifecycle, submitThreadTurn } as unknown as APIClient;
  const queryClient = new QueryClient({ defaultOptions: { mutations: { retry: false } } });
  const view = render(<QueryClientProvider client={queryClient}>
    <V2PausedThreadControl client={client} threadID="thread-a" runID="run-a" />
  </QueryClientProvider>);
  try {
    const user = userEvent.setup();
    const button = screen.getByRole("button", { name: "解除暂停" });
    expect(screen.getByRole("status")).toHaveTextContent("发送消息即可继续，也可以仅解除暂停。");
    expect(button).toHaveAttribute("title", expect.stringContaining("失败的操作需另行发起"));
    expect(controlRunLifecycle).not.toHaveBeenCalled();
    await user.click(button);
    expect(await screen.findByRole("alert")).toHaveTextContent("解除暂停未确认，可重试：connection interrupted");
    expect(controlRunLifecycle).toHaveBeenCalledWith("run-a",
      { version: "run_lifecycle_control.v1", action: "resume" }, expect.stringMatching(/^v2-resume-/));
    await user.click(button);
    await waitFor(() => expect(screen.queryByRole("alert")).not.toBeInTheDocument());
    expect(controlRunLifecycle).toHaveBeenCalledTimes(2);
    expect(controlRunLifecycle.mock.calls[1]).toEqual(controlRunLifecycle.mock.calls[0]);
    await user.click(button);
    await waitFor(() => expect(controlRunLifecycle).toHaveBeenCalledTimes(3));
    expect(controlRunLifecycle.mock.calls[2]![2]).not.toBe(controlRunLifecycle.mock.calls[0]![2]);
    expect(submitThreadTurn).not.toHaveBeenCalled();
  } finally {
    view.unmount();
    queryClient.clear();
  }
});

function deferred<T>() {
  let resolve!: (value: T) => void;
  let reject!: (reason: Error) => void;
  const promise = new Promise<T>((finish, fail) => { resolve = finish; reject = fail; });
  return { promise, resolve, reject };
}

function resumeResult(runID: string): RunLifecycleControlView {
  return { version: "run_lifecycle_control.v1", action: "resume", expected_status: "paused", applied_status: "running",
    run: { id: runID, status: "running" }, event_sequence_start: 7, event_sequence_end: 7, replayed: false,
    execution_started: false, model_called: false, tool_called: false, capability_grant: false } as RunLifecycleControlView;
}

it.each([
  ["another task", "thread-b", "run-b", "success"],
  ["another task", "thread-b", "run-b", "failure"],
  ["another Run in the same task", "thread-a", "run-b", "success"],
  ["another Run in the same task", "thread-a", "run-b", "failure"],
] as const)("isolates a delayed resume after navigating to %s on %s/%s (%s)", async (_scope, nextThreadID, nextRunID, outcome) => {
  const original = deferred<RunLifecycleControlView>();
  const other = deferred<RunLifecycleControlView>();
  const controlRunLifecycle = vi.fn().mockImplementationOnce(() => original.promise)
    .mockImplementationOnce(() => other.promise).mockResolvedValue(resumeResult("run-a"));
  const client = { hasRunLifecycle: true, controlRunLifecycle } as unknown as APIClient;
  const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false }, mutations: { retry: false } } });
  queryClient.setQueryData(v2QueryKeys.thread("thread-a"), { id: "thread-a" });
  if (nextThreadID !== "thread-a") queryClient.setQueryData(v2QueryKeys.thread(nextThreadID), { id: nextThreadID });
  const invalidate = vi.spyOn(queryClient, "invalidateQueries");
  function Control({ threadID, runID }: { threadID: string; runID: string }) {
    const [drafts, setDrafts] = useState<Record<string, string>>({});
    return <><input aria-label="Task draft" value={drafts[threadID] ?? ""}
      onChange={(event) => setDrafts((current) => ({ ...current, [threadID]: event.target.value }))} />
      <V2PausedThreadControl client={client} threadID={threadID} runID={runID} /></>;
  }
  const content = (threadID: string, runID: string) => <QueryClientProvider client={queryClient}>
    <Control threadID={threadID} runID={runID} /></QueryClientProvider>;
  const view = render(content("thread-a", "run-a"));
  const user = userEvent.setup();
  try {
    await user.click(screen.getByRole("button", { name: "解除暂停" }));
    await waitFor(() => expect(controlRunLifecycle).toHaveBeenCalledTimes(1));
    expect(screen.getByRole("button", { name: "正在解除…" })).toBeDisabled();
    view.rerender(content(nextThreadID, nextRunID));
    expect(screen.getByRole("button", { name: "解除暂停" })).toBeEnabled();
    expect(screen.queryByRole("alert")).not.toBeInTheDocument();
    await user.type(screen.getByRole("textbox", { name: "Task draft" }), "New unsent requirement");
    await user.click(screen.getByRole("button", { name: "解除暂停" }));
    await waitFor(() => expect(controlRunLifecycle).toHaveBeenCalledTimes(2));
    expect(controlRunLifecycle.mock.calls[0]![0]).toBe("run-a");
    expect(controlRunLifecycle.mock.calls[1]![0]).toBe(nextRunID);
    expect(controlRunLifecycle.mock.calls[1]![2]).not.toBe(controlRunLifecycle.mock.calls[0]![2]);
    view.rerender(content("thread-a", "run-a"));
    expect(screen.getByRole("button", { name: "正在解除…" })).toBeDisabled();
    view.rerender(content(nextThreadID, nextRunID));
    await act(async () => {
      if (outcome === "success") original.resolve(resumeResult("run-a"));
      else original.reject(new Error("Original resume response lost"));
      await original.promise.catch(() => undefined);
    });
    await waitFor(() => expect(invalidate).toHaveBeenCalledWith({ queryKey: v2QueryKeys.thread("thread-a") }));
    expect(invalidate).toHaveBeenCalledTimes(1);
    expect(screen.getByRole("button", { name: "正在解除…" })).toBeDisabled();
    expect(screen.queryByRole("alert")).not.toBeInTheDocument();
    expect(screen.getByRole("textbox", { name: "Task draft" })).toHaveValue("New unsent requirement");
    if (nextThreadID !== "thread-a") {
      expect(queryClient.getQueryState(v2QueryKeys.thread(nextThreadID))?.isInvalidated).toBe(false);
    }
    view.rerender(content("thread-a", "run-a"));
    await waitFor(() => expect(screen.getByRole("button", { name: "解除暂停" })).toBeEnabled());
    if (outcome === "failure") {
      expect(screen.getByRole("alert")).toHaveTextContent("Original resume response lost");
      await user.click(screen.getByRole("button", { name: "解除暂停" }));
      await waitFor(() => expect(controlRunLifecycle).toHaveBeenCalledTimes(3));
      expect(controlRunLifecycle.mock.calls[2]).toEqual(controlRunLifecycle.mock.calls[0]);
      await waitFor(() => expect(screen.queryByRole("alert")).not.toBeInTheDocument());
    } else expect(screen.queryByRole("alert")).not.toBeInTheDocument();
    view.rerender(content(nextThreadID, nextRunID));
    expect(screen.getByRole("button", { name: "正在解除…" })).toBeDisabled();
    await act(async () => {
      other.reject(new Error("Other resume response lost"));
      await other.promise.catch(() => undefined);
    });
    expect(await screen.findByRole("alert")).toHaveTextContent("Other resume response lost");
    expect(screen.getByRole("textbox", { name: "Task draft" })).toHaveValue("New unsent requirement");
    expect(screen.getByRole("button", { name: "解除暂停" })).toBeEnabled();
  } finally {
    view.unmount();
    queryClient.clear();
  }
});

it.each(["success", "failure"] as const)("keeps the current task's own resume error when an older task settles with %s", async (outcome) => {
  const original = deferred<RunLifecycleControlView>();
  const controlRunLifecycle = vi.fn().mockImplementationOnce(() => original.promise)
    .mockRejectedValueOnce(new Error("Current task response lost"));
  const client = { hasRunLifecycle: true, controlRunLifecycle } as unknown as APIClient;
  const queryClient = new QueryClient({ defaultOptions: { mutations: { retry: false } } });
  const content = (threadID: string, runID: string) => <QueryClientProvider client={queryClient}>
    <V2PausedThreadControl client={client} threadID={threadID} runID={runID} /></QueryClientProvider>;
  const view = render(content("thread-a", "run-a"));
  const user = userEvent.setup();
  try {
    await user.click(screen.getByRole("button", { name: "解除暂停" }));
    await waitFor(() => expect(controlRunLifecycle).toHaveBeenCalledTimes(1));
    view.rerender(content("thread-b", "run-b"));
    await user.click(screen.getByRole("button", { name: "解除暂停" }));
    expect(await screen.findByRole("alert")).toHaveTextContent("Current task response lost");
    await act(async () => {
      if (outcome === "success") original.resolve(resumeResult("run-a"));
      else original.reject(new Error("Older task response lost"));
      await original.promise.catch(() => undefined);
    });
    await waitFor(() => expect(queryClient.getMutationCache().find({
      mutationKey: ["v2", "thread-resume", "thread-a", "run-a"], exact: true,
    })?.state.status).toBe(outcome === "success" ? "success" : "error"));
    expect(screen.getByRole("alert")).toHaveTextContent("Current task response lost");
    expect(screen.getByRole("alert")).not.toHaveTextContent("Older task response lost");
    expect(screen.getByRole("button", { name: "解除暂停" })).toBeEnabled();
  } finally {
    view.unmount();
    queryClient.clear();
  }
});

it.each(["success", "failure"] as const)("retains a resume's %s and retry identity when its view unmounts", async (outcome) => {
  const original = deferred<RunLifecycleControlView>();
  const controlRunLifecycle = vi.fn().mockImplementationOnce(() => original.promise).mockResolvedValue(resumeResult("run-a"));
  const client = { hasRunLifecycle: true, controlRunLifecycle } as unknown as APIClient;
  const queryClient = new QueryClient({ defaultOptions: { mutations: { retry: false } } });
  const invalidate = vi.spyOn(queryClient, "invalidateQueries");
  const content = (threadID: string, runID: string) => <QueryClientProvider client={queryClient}>
    <V2PausedThreadControl client={client} threadID={threadID} runID={runID} /></QueryClientProvider>;
  let view = render(content("thread-a", "run-a"));
  const user = userEvent.setup();
  try {
    await user.click(screen.getByRole("button", { name: "解除暂停" }));
    await waitFor(() => expect(controlRunLifecycle).toHaveBeenCalledTimes(1));
    view.unmount();
    view = render(content("thread-b", "run-b"));
    await act(async () => {
      if (outcome === "success") original.resolve(resumeResult("run-a"));
      else original.reject(new Error("Original resume response lost"));
      await original.promise.catch(() => undefined);
    });
    await waitFor(() => expect(invalidate).toHaveBeenCalledWith({ queryKey: v2QueryKeys.thread("thread-a") }));
    expect(screen.getByRole("button", { name: "解除暂停" })).toBeEnabled();
    expect(screen.queryByRole("alert")).not.toBeInTheDocument();
    view.unmount();
    view = render(content("thread-a", "run-a"));
    expect(screen.getByRole("button", { name: "解除暂停" })).toBeEnabled();
    if (outcome === "failure") {
      expect(screen.getByRole("alert")).toHaveTextContent("Original resume response lost");
      await user.click(screen.getByRole("button", { name: "解除暂停" }));
      await waitFor(() => expect(controlRunLifecycle).toHaveBeenCalledTimes(2));
      expect(controlRunLifecycle.mock.calls[1]).toEqual(controlRunLifecycle.mock.calls[0]);
    } else expect(screen.queryByRole("alert")).not.toBeInTheDocument();
  } finally {
    view.unmount();
    queryClient.clear();
  }
});

it("does not request or poll an unavailable execution route even with a control token", async () => {
  vi.useFakeTimers();
  const client = new APIClient("read", "/api/v1", "control", {
    runExecutionEnabled: true, threadExecutionReadEnabled: false,
  });
  const read = vi.spyOn(client, "threadExecution").mockRejectedValue(new Error("route unavailable"));
  const queryClient = new QueryClient();
  const view = render(<QueryClientProvider client={queryClient}><ObservedControl client={client} /></QueryClientProvider>);
  try {
    await act(async () => { await vi.advanceTimersByTimeAsync(1_800); });
    expect(read).not.toHaveBeenCalled();
    expect(screen.queryByRole("button", { name: "停止当前执行" })).not.toBeInTheDocument();
  } finally {
    view.unmount();
    queryClient.clear();
    vi.useRealTimers();
  }
});

it("keeps observing a supported execution route with only the read token and cannot stop it", async () => {
  const client = new APIClient("read", "/api/v1", "", {
    runExecutionEnabled: true, threadExecutionReadEnabled: true, threadControlEnabled: true,
  });
  const read = vi.spyOn(client, "threadExecution").mockResolvedValueOnce(execution("thread-a"))
    .mockResolvedValue(execution("thread-a", "idle"));
  const stop = vi.spyOn(client, "interruptThread");
  const queryClient = new QueryClient();
  const view = render(<QueryClientProvider client={queryClient}><ObservedControl client={client} /></QueryClientProvider>);
  try {
    await screen.findByText("running");
    expect(screen.queryByRole("button", { name: "停止当前执行" })).not.toBeInTheDocument();
    await screen.findByText("idle");
    expect(read).toHaveBeenCalledTimes(2);
    expect(stop).not.toHaveBeenCalled();
  } finally {
    view.unmount();
    queryClient.clear();
  }
});

it("stops automatic polling if a previously advertised execution route returns 404", async () => {
  vi.useFakeTimers();
  const client = new APIClient("read", "/api/v1", "", { threadExecutionReadEnabled: true });
  const read = vi.spyOn(client, "threadExecution").mockRejectedValue(new APIRequestError("Not found", "NOT_FOUND", 404));
  const queryClient = new QueryClient();
  const view = render(<QueryClientProvider client={queryClient}><ObservedControl client={client} /></QueryClientProvider>);
  try {
    await act(async () => { await vi.advanceTimersByTimeAsync(1_800); });
    expect(read).toHaveBeenCalledTimes(1);
    expect(queryClient.getQueryState(v2QueryKeys.execution("thread-a"))?.status).toBe("error");
    expect(screen.queryByRole("button", { name: "停止当前执行" })).not.toBeInTheDocument();
  } finally {
    view.unmount();
    queryClient.clear();
    vi.useRealTimers();
  }
});

function execution(threadID: string, state: "running" | "idle" = "running") {
  return { version: "thread_execution.v1", thread_id: threadID,
    execution_id: state === "running" ? `execution-${threadID}` : undefined,
    state, queued_messages: 0, capability_grant: false, last_turn_interrupted: state === "idle" } as ThreadExecutionView;
}

it.each(["success", "failure"] as const)("keeps a delayed stop %s bound to its submitted task after navigation", async (outcome) => {
  let finish!: (value: ThreadExecutionView) => void;
  let fail!: (reason: Error) => void;
  const pending = new Promise<ThreadExecutionView>((resolve, reject) => { finish = resolve; fail = reject; });
  const interruptThread = vi.fn(() => pending);
  const client = { hasThreadControl: true, hasRunExecution: true, interruptThread } as unknown as APIClient;
  const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false }, mutations: { retry: false } } });
  const stateA = execution("thread-a");
  const stateB = execution("thread-b");
  queryClient.setQueryData(v2QueryKeys.execution("thread-a"), stateA);
  queryClient.setQueryData(v2QueryKeys.execution("thread-b"), stateB);
  function Control({ threadID }: { threadID: string }) {
    const query = useQuery<ThreadExecutionView>({ queryKey: v2QueryKeys.execution(threadID),
      queryFn: async () => execution(threadID), enabled: false });
    return <V2ThreadExecutionControl client={client} threadID={threadID} execution={query.data} />;
  }
  const content = (threadID: string) => <QueryClientProvider client={queryClient}><Control threadID={threadID} /></QueryClientProvider>;
  const view = render(content("thread-a"));
  await userEvent.setup().click(screen.getByRole("button", { name: "停止当前执行" }));
  await waitFor(() => expect(interruptThread).toHaveBeenCalledWith("thread-a", "execution-thread-a", "v2-thread-stop-execution-thread-a"));
  view.rerender(content("thread-b"));
  await waitFor(() => expect(screen.getByRole("button", { name: "停止当前执行" })).toBeEnabled());
  await act(async () => {
    if (outcome === "success") finish(execution("thread-a", "idle"));
    else fail(new Error("Task A connection interrupted"));
    await pending.catch(() => undefined);
  });
  await waitFor(() => expect(queryClient.getQueryState(v2QueryKeys.execution("thread-a"))?.isInvalidated).toBe(true));
  expect(queryClient.getQueryData(v2QueryKeys.execution("thread-b"))).toEqual(stateB);
  expect(queryClient.getQueryState(v2QueryKeys.execution("thread-b"))?.isInvalidated).toBe(false);
  expect(queryClient.getQueryData(v2QueryKeys.execution("thread-a"))).toEqual(outcome === "success" ? execution("thread-a", "idle") : stateA);
  expect(screen.getByRole("button", { name: "停止当前执行" })).toBeEnabled();
  expect(screen.queryByRole("alert")).not.toBeInTheDocument();
  view.unmount();
  queryClient.clear();
});

function render(ui: ReactElement, options?: RenderOptions) {
  localStorage.setItem("prayu.locale.v1", "zh-CN");
  const Wrapper = options?.wrapper;
  return renderComponent(ui, {...options, wrapper: ({children}) => <LocaleProvider>{Wrapper ? <Wrapper>{children}</Wrapper> : children}</LocaleProvider>});
}
