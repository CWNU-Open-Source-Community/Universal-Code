import { act, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import type { APIClient } from "../../api/client";
import { APIRequestError } from "../../api/client";
import { LocaleProvider } from "../../lib/locale";
import { ProjectActions } from "./project-actions";

const actions = (runID = "run-1") => ({
  protocol_version: "project-actions.v1", run_id: runID, backend: "docker", capability_grant: false,
  actions: [
    { slot: "test", command_id: "test.npm", configured: true, available: true, target_kind: "directory", reason: "" },
    { slot: "format", command_id: "format.go", configured: true, available: true, target_kind: "go_file", reason: "" },
  ],
});
const result = (runID = "run-1") => ({
  protocol_version: "project-actions.v1", run_id: runID, slot: "test", command_id: "test.npm", backend: "docker", replayed: false,
  job: { id: "job-" + runID, state: "failed", exit_code: 1, stdout: "1 test failed", stderr: "expected true", can_stop: false, stdout_truncated: false, stderr_truncated: false },
});
function setup() {
  const client = { baseURL: "/api/v1", hasControl: true, hasRunExecution: true,
    listProjectActions: vi.fn().mockImplementation((runID: string) => Promise.resolve(actions(runID))),
    runProjectAction: vi.fn().mockResolvedValue(result()),
    getProjectActionJob: vi.fn().mockImplementation((runID: string) => Promise.resolve(result(runID))),
    stopThreadApplicationService: vi.fn().mockResolvedValue({}),
  };
  const queries = new QueryClient({ defaultOptions: { queries: { retry: false }, mutations: { retry: false } } });
  const feedback = vi.fn();
  const ui = (runID = "run-1") => <LocaleProvider><QueryClientProvider client={queries}>
    <ProjectActions client={client as unknown as APIClient} runID={runID} threadID="thread-1" onFeedback={feedback} />
  </QueryClientProvider></LocaleProvider>;
  return { client, queries, ui, feedback };
}
beforeEach(() => localStorage.removeItem("prayu.locale.v1"));

it("runs the chosen test directory, displays the real failure and offers it back to conversation", async () => {
  const { client, ui, feedback } = setup();
  render(ui());
  const button = await screen.findByRole("button", { name: "运行测试" });
  expect(screen.getByText(/执行环境：Docker/)).toBeInTheDocument();
  expect(screen.getByRole("button", { name: "格式化文件" })).toBeDisabled();
  fireEvent.change(screen.getByLabelText(/测试目录/), { target: { value: "web" } });
  fireEvent.click(button);
  await screen.findByText("expected true");
  expect(client.runProjectAction).toHaveBeenCalledWith("run-1", "test", "web", expect.stringMatching(/^project-action-/));
  expect(screen.getByText("退出码：1")).toBeInTheDocument();
  fireEvent.click(screen.getByRole("button", { name: "将失败结果放回对话" }));
  expect(feedback).toHaveBeenCalledWith(expect.stringContaining("Job: job-run-1"));
  expect(feedback).toHaveBeenCalledWith(expect.stringContaining("expected true"));
});

it("keeps an unknown outcome on the same request key and does not automatically rerun", async () => {
  const { client, ui } = setup();
  client.runProjectAction.mockRejectedValueOnce(new TypeError("Network disconnected"));
  render(ui());
  fireEvent.click(await screen.findByRole("button", { name: "运行测试" }));
  await screen.findByRole("button", { name: "确认上次运行" });
  expect(client.runProjectAction).toHaveBeenCalledTimes(1);
  expect(screen.getByRole("button", { name: "运行测试" })).toBeDisabled();
  const key = client.runProjectAction.mock.calls[0][3];
  fireEvent.click(screen.getByRole("button", { name: "确认上次运行" }));
  await screen.findByText("expected true");
  expect(client.runProjectAction).toHaveBeenLastCalledWith("run-1", "test", "", key);
});

it("allows correcting a rejected request and respects unavailable actions and read-only connections", async () => {
  const { client, ui } = setup();
  client.runProjectAction.mockRejectedValueOnce(new APIRequestError("Directory excluded", "POLICY_DENIED", 403));
  const { rerender } = render(ui());
  fireEvent.click(await screen.findByRole("button", { name: "运行测试" }));
  await screen.findByText("Directory excluded");
  await waitFor(() => expect(screen.getByRole("button", { name: "运行测试" })).toBeEnabled());
  client.hasControl = false;
  client.hasRunExecution = false;
  rerender(ui());
  expect(screen.getByRole("button", { name: "运行测试" })).toBeDisabled();
  expect(client.runProjectAction).toHaveBeenCalledTimes(1);
});

it("keeps a late execution response with its original Run after switching runs", async () => {
  const { client, ui } = setup();
  let finish!: (value: ReturnType<typeof result>) => void;
  client.runProjectAction.mockImplementationOnce(() => new Promise((resolve) => { finish = resolve; }));
  const { rerender } = render(ui());
  fireEvent.click(await screen.findByRole("button", { name: "运行测试" }));
  await waitFor(() => expect(client.runProjectAction).toHaveBeenCalledTimes(1));
  rerender(ui("run-2"));
  await screen.findByRole("button", { name: "运行测试" });
  await act(async () => finish(result("run-1")));
  expect(screen.queryByText("job-run-1")).not.toBeInTheDocument();
  expect(screen.queryByText("expected true")).not.toBeInTheDocument();
});

it("reads an active Job and stops only that Job", async () => {
  const { client, ui } = setup();
  const running = { ...result(), job: { ...result().job, state: "running", can_stop: true, exit_code: undefined } };
  client.runProjectAction.mockResolvedValue(running);
  client.getProjectActionJob.mockResolvedValue(running);
  render(ui());
  fireEvent.click(await screen.findByRole("button", { name: "运行测试" }));
  fireEvent.click(await screen.findByRole("button", { name: "停止运行" }));
  await waitFor(() => expect(client.stopThreadApplicationService).toHaveBeenCalledWith("thread-1", "run-1", "job-run-1"));
  expect(client.getProjectActionJob).toHaveBeenCalledWith("run-1", "job-run-1", expect.any(AbortSignal));
});
