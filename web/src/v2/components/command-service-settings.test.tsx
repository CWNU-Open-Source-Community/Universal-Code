import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { APIRequestError, type APIClient } from "../../api/client";
import { commandServicePolicyVersion, type ThreadCommandServicePolicyRequest, type ThreadCommandServicePolicyView } from "../../api/command-service-policy";
import type { ThreadApplicationServicesView, ThreadApplicationServiceView } from "../../api/types";
import { applicationServicesQueryKey } from "../application-preview-request";
import { V2CommandServiceSettings } from "./command-service-settings";

const service = (id: string, override: Partial<ThreadApplicationServiceView> = {}): ThreadApplicationServiceView => ({
  thread_id: "thread-1", run_id: "run-service", job_id: id, workspace_mode: "shared_service",
  state: "running", can_stop: true, tree_reaped: false, created_at: "2026-10-18T00:00:00Z", ...override,
});

function setup(initial: ThreadApplicationServiceView[] = [], threadID = "thread-1", control = true, more = false,
  capabilities: Partial<Pick<APIClient, "hasControl" | "hasExecutionPermissionControl" | "hasRunExecution">> = {}) {
  let saved: ThreadCommandServicePolicyView = { version: commandServicePolicyVersion, thread_id: threadID, policy: "auto", revision: 1 };
  let entries = initial;
  const getThreadCommandServicePolicy = vi.fn(async () => saved);
  const setThreadCommandServicePolicy = vi.fn(async (_thread: string, request: ThreadCommandServicePolicyRequest) => {
    saved = { ...saved, policy: request.policy, revision: saved.revision + (saved.policy === request.policy ? 0 : 1) };
    return saved;
  });
  const listThreadApplicationServices = vi.fn(async (): Promise<ThreadApplicationServicesView> => ({
    version: "thread_application_services.v1", thread_id: threadID, services: entries, has_more: more,
  }));
  const stopThreadApplicationService = vi.fn(async (_thread: string, _run: string, jobID: string) => {
    const stopped: ThreadApplicationServiceView = { ...entries.find((item) => item.job_id === jobID)!, state: "stopping" };
    entries = entries.map((item) => item.job_id === jobID ? stopped : item);
    return { version: "thread_application_services.v1", service: stopped, replayed: false };
  });
  const client = { hasControl: control, hasExecutionPermissionControl: control, hasRunExecution: control, ...capabilities, getThreadCommandServicePolicy,
    setThreadCommandServicePolicy, listThreadApplicationServices, stopThreadApplicationService } as unknown as APIClient;
  const queries = new QueryClient({ defaultOptions: { queries: { retry: false }, mutations: { retry: false } } });
  const tree = (id: string) => <QueryClientProvider client={queries}><V2CommandServiceSettings client={client} threadID={id} /></QueryClientProvider>;
  const view = render(tree(threadID));
  return { ...view, client, queries, getThreadCommandServicePolicy, setThreadCommandServicePolicy,
    listThreadApplicationServices, stopThreadApplicationService, setSaved: (next: ThreadCommandServicePolicyView) => { saved = next; },
    setServices: (next: ThreadApplicationServiceView[]) => { entries = next; },
    refreshServices: () => queries.invalidateQueries({ queryKey: applicationServicesQueryKey(threadID) }),
    switchThread: (id: string) => view.rerender(tree(id)),
  };
}

it("has an ordinary empty state without a current Thread and performs no requests", () => {
  const f = setup([], "");
  expect(screen.getByText("先打开一个任务，再设置开发服务策略。")).toBeVisible();
  expect(f.getThreadCommandServicePolicy).not.toHaveBeenCalled();
  expect(f.listThreadApplicationServices).not.toHaveBeenCalled();
  expect(screen.queryByRole("button", { name: "自动选择" })).not.toBeInTheDocument();
});

it("keeps saved auto policy separate from observed shared services and ordinary commands", async () => {
  const f = setup([service("exclusive-job", { workspace_mode: "exclusive" })]);
  expect(await screen.findByText("未启用")).toBeVisible();
  expect(screen.getByRole("button", { name: "自动选择" })).toHaveAttribute("aria-pressed", "true");
  expect(screen.queryByRole("button", { name: "停止服务 exclusive-job" })).not.toBeInTheDocument();
  f.setServices([service("shared-job"), service("exclusive-job", { workspace_mode: "exclusive" })]);
  await f.refreshServices();
  expect(await screen.findByText("1 个服务运行中")).toBeVisible();
  expect(screen.getByRole("button", { name: "停止服务 shared-job" })).toBeEnabled();
  expect(f.setThreadCommandServicePolicy).not.toHaveBeenCalled();
  expect(screen.getByRole("button", { name: "自动选择" })).toHaveAttribute("aria-pressed", "true");
});

it("changes policy only after the save receipt and keeps running state until actual stop evidence", async () => {
  const f = setup([service("shared-job")]);
  await screen.findByText("1 个服务运行中");
  let finish!: (value: ThreadCommandServicePolicyView) => void;
  f.setThreadCommandServicePolicy.mockImplementationOnce(() => new Promise((resolve) => { finish = resolve; }));
  await userEvent.click(screen.getByRole("button", { name: "始终独占" }));
  expect(f.setThreadCommandServicePolicy).toHaveBeenCalledExactlyOnceWith("thread-1", {
    version: commandServicePolicyVersion, policy: "exclusive", expected_revision: 1,
  });
  expect(screen.getByRole("button", { name: "自动选择" })).toHaveAttribute("aria-pressed", "true");
  expect(screen.getByText("1 个服务运行中")).toBeVisible();
  const saved: ThreadCommandServicePolicyView = { version: commandServicePolicyVersion, thread_id: "thread-1", policy: "exclusive", revision: 2 };
  f.setSaved(saved); finish(saved);
  await waitFor(() => expect(screen.getByRole("button", { name: "始终独占" })).toHaveAttribute("aria-pressed", "true"));
  expect(screen.getByText("1 个服务运行中")).toBeVisible();
  expect(f.stopThreadApplicationService).not.toHaveBeenCalled();
});

it("refreshes a revision conflict and waits for another explicit choice before retrying", async () => {
  const f = setup();
  await screen.findByText("未启用");
  f.setThreadCommandServicePolicy.mockImplementationOnce(async () => {
    f.setSaved({ version: commandServicePolicyVersion, thread_id: "thread-1", policy: "exclusive", revision: 3 });
    throw new APIRequestError("changed", "CONFLICT", 409);
  });
  await userEvent.click(screen.getByRole("button", { name: "始终独占" }));
  expect(await screen.findByText("设置已被更新，请核对当前选择后重试。")).toBeVisible();
  await waitFor(() => expect(screen.getByRole("button", { name: "自动选择" })).toBeEnabled());
  expect(f.setThreadCommandServicePolicy).toHaveBeenCalledTimes(1);
  expect(screen.getByRole("button", { name: "始终独占" })).toHaveAttribute("aria-pressed", "true");
  await userEvent.click(screen.getByRole("button", { name: "自动选择" }));
  await waitFor(() => expect(f.setThreadCommandServicePolicy).toHaveBeenLastCalledWith("thread-1", {
    version: commandServicePolicyVersion, policy: "auto", expected_revision: 3,
  }));
});

it("stops the selected service and keeps terminal but unreaped cleanup visible", async () => {
  const source = service("shared-job", { run_id: "old-run" });
  const f = setup([source]);
  await screen.findByText("1 个服务运行中");
  await userEvent.click(screen.getByRole("button", { name: "停止服务 shared-job" }));
  await waitFor(() => expect(f.stopThreadApplicationService).toHaveBeenCalledExactlyOnceWith("thread-1", "old-run", "shared-job"));
  expect((await screen.findAllByText("正在停止")).length).toBeGreaterThan(0);
  f.setServices([{ ...source, state: "cancelled", can_stop: false, tree_reaped: false }]);
  await f.refreshServices();
  expect(await screen.findByText("已退出，清理待确认")).toBeVisible();
  expect(screen.queryByText("未启用")).not.toBeInTheDocument();
  expect(screen.getByRole("button", { name: "停止服务 shared-job" })).toBeDisabled();
  f.setServices([{ ...source, state: "cancelled", can_stop: false, tree_reaped: true }]);
  await f.refreshServices();
  expect(await screen.findByText("未启用")).toBeVisible();
  expect(screen.queryByRole("button", { name: "停止服务 shared-job" })).not.toBeInTheDocument();
  expect(f.setThreadCommandServicePolicy).not.toHaveBeenCalled();
});

it("does not claim inactivity from a truncated list", async () => {
  const f = setup([], "thread-1", true, true);
  expect(await screen.findByText("无法读取完整状态")).toBeVisible();
  expect(screen.queryByText("未启用")).not.toBeInTheDocument();
  f.setServices([service("shared-job")]);
  await f.refreshServices();
  expect(await screen.findByText("至少 1 个服务运行中")).toBeVisible();
});

it("marks failed refreshes as unreadable and disables stale stop actions", async () => {
  const f = setup([service("shared-job")]);
  await screen.findByText("1 个服务运行中");
  f.listThreadApplicationServices.mockRejectedValue(new Error("unavailable"));
  await f.refreshServices();
  expect(await screen.findByText("无法读取")).toBeVisible();
  expect(screen.getByText("当前状态待确认")).toBeVisible();
  expect(screen.getByRole("button", { name: "停止服务 shared-job" })).toBeDisabled();
});

it("allows a read-only connection to see policy and services without changing either", async () => {
  const f = setup([service("shared-job")], "thread-1", false);
  await screen.findByText("1 个服务运行中");
  expect(screen.getByRole("button", { name: "始终独占" })).toBeDisabled();
  expect(screen.getByRole("button", { name: "停止服务 shared-job" })).toBeDisabled();
  expect(screen.getByText("当前连接只可查看设置与服务状态。")).toBeVisible();
  expect(f.setThreadCommandServicePolicy).not.toHaveBeenCalled();
  expect(f.stopThreadApplicationService).not.toHaveBeenCalled();
});

it.each([
  { name: "general Run control disabled", hasControl: false, hasExecutionPermissionControl: true, hasRunExecution: true },
  { name: "policy control disabled", hasControl: true, hasExecutionPermissionControl: false, hasRunExecution: true },
  { name: "service execution disabled", hasControl: true, hasExecutionPermissionControl: true, hasRunExecution: false },
])("uses separate capabilities when $name", async (capabilities) => {
  const f = setup([service("shared-job")], "thread-1", true, false, capabilities);
  await screen.findByText("1 个服务运行中");
  const policyButton = screen.getByRole("button", { name: "始终独占" });
  const stopButton = screen.getByRole("button", { name: "停止服务 shared-job" });
  expect(screen.queryByText("当前连接只可查看设置与服务状态。")).not.toBeInTheDocument();
  if (capabilities.hasExecutionPermissionControl) {
    expect(policyButton).toBeEnabled();
    await userEvent.click(policyButton);
    await waitFor(() => expect(f.setThreadCommandServicePolicy).toHaveBeenCalledOnce());
  } else {
    expect(policyButton).toBeDisabled();
    expect(f.setThreadCommandServicePolicy).not.toHaveBeenCalled();
    expect(screen.getByText("当前连接无法更改开发服务策略。")).toBeVisible();
  }
  if (capabilities.hasRunExecution) {
    expect(stopButton).toBeEnabled();
    await userEvent.click(stopButton);
    await waitFor(() => expect(f.stopThreadApplicationService).toHaveBeenCalledExactlyOnceWith("thread-1", "run-service", "shared-job"));
  } else {
    expect(stopButton).toBeDisabled();
    expect(f.stopThreadApplicationService).not.toHaveBeenCalled();
    expect(screen.getByText("当前连接无法直接停止服务。")).toBeVisible();
  }
});

it("does not show a previous Thread's cached policy or services after switching tasks", async () => {
  const f = setup([service("shared-job")]);
  await screen.findByText("1 个服务运行中");
  f.getThreadCommandServicePolicy.mockImplementation(() => new Promise(() => {}));
  f.listThreadApplicationServices.mockImplementation(() => new Promise(() => {}));
  f.switchThread("thread-2");
  expect(screen.queryByRole("button", { name: "停止服务 shared-job" })).not.toBeInTheDocument();
  expect(screen.getByRole("button", { name: "自动选择" })).not.toHaveAttribute("aria-pressed", "true");
  expect(screen.getByRole("button", { name: "始终独占" })).toBeDisabled();
});
