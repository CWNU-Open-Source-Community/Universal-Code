import { act, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import type { APIClient } from "../../api/client";
import { APIRequestError } from "../../api/client";
import { LocaleProvider } from "../../lib/locale";
import { ProjectActions } from "./project-actions";
import { V2RecoveryProvider } from "../recovery-storage";

const actions = (runID = "run-1", target = ".") => ({
  protocol_version: "project-actions.v1", run_id: runID, target, backend: "docker", capability_grant: false,
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
    listProjectActions: vi.fn().mockImplementation((runID: string, target: string) => Promise.resolve(actions(runID, target))),
    runProjectAction: vi.fn().mockResolvedValue(result()),
    getProjectActionJob: vi.fn().mockImplementation((runID: string) => Promise.resolve(result(runID))),
    stopThreadApplicationService: vi.fn().mockResolvedValue({}),
  };
  const queries = new QueryClient({ defaultOptions: { queries: { retry: false }, mutations: { retry: false } } });
  const feedback = vi.fn();
  const ui = (runID = "run-1", testTarget?: string) => <LocaleProvider><QueryClientProvider client={queries}>
    <ProjectActions client={client as unknown as APIClient} runID={runID} threadID="thread-1" onFeedback={feedback} testTarget={testTarget} />
  </QueryClientProvider></LocaleProvider>;
  return { client, queries, ui, feedback };
}
beforeEach(() => localStorage.removeItem("prayu.locale.v1"));

async function readyTests() {
  const button = await screen.findByRole("button", { name: "运行测试" });
  await waitFor(() => expect(button).toBeEnabled());
  return button;
}

it("keeps directory selection available when only a subdirectory contains the detected project", async () => {
  const { client, ui } = setup();
  client.listProjectActions.mockImplementation((runID: string, target?: string) => Promise.resolve({
    ...actions(runID), target: target ?? ".", actions: actions(runID).actions.map((action) => action.slot === "test"
      ? { ...action, configured: false, command_id: target === "web" ? "test.npm" : "", available: target === "web" }
      : action),
  }));
  render(ui());
  expect(await screen.findByRole("button", { name: "运行测试" })).toBeDisabled();
  fireEvent.change(screen.getByLabelText(/测试目录/), { target: { value: "web" } });
  await waitFor(() => expect(screen.getByRole("button", { name: "运行测试" })).toBeEnabled());
  fireEvent.click(screen.getByRole("button", { name: "运行测试" }));
  await waitFor(() => expect(client.runProjectAction).toHaveBeenCalledWith("run-1", "test", "web", expect.any(String)));
});

it("runs the chosen test directory, displays the real failure and offers it back to conversation", async () => {
  const { client, ui, feedback } = setup();
  render(ui());
  const button = await readyTests();
  expect(screen.getByText(/执行环境：Docker/)).toBeInTheDocument();
  expect(screen.getByRole("button", { name: "格式化文件" })).toBeDisabled();
  fireEvent.change(screen.getByLabelText(/测试目录/), { target: { value: "web" } });
  await readyTests();
  fireEvent.click(button);
  await screen.findByText("expected true");
  expect(client.runProjectAction).toHaveBeenCalledWith("run-1", "test", "web", expect.stringMatching(/^project-action-/));
  expect(screen.getByText("退出码：1")).toBeInTheDocument();
  fireEvent.click(screen.getByRole("button", { name: "将失败结果放回对话" }));
  expect(feedback).toHaveBeenCalledWith(expect.stringContaining("Job: job-run-1"));
  expect(feedback).toHaveBeenCalledWith(expect.stringContaining("expected true"));
});

it("runs a detected test without asking the user to configure the pinned action", async () => {
  const { client, ui } = setup();
  client.listProjectActions.mockImplementation((runID: string, target: string) => Promise.resolve({
    ...actions(runID, target), actions: actions().actions.map((action) => action.slot === "test"
      ? { ...action, configured: false } : action) }));
  render(ui());
  expect(await screen.findByText("根据项目文件检测")).toBeInTheDocument();
  fireEvent.change(screen.getByLabelText(/测试目录/), { target: { value: "web" } });
  await readyTests();
  fireEvent.click(screen.getByRole("button", { name: "运行测试" }));
  await screen.findByText("expected true");
  expect(client.runProjectAction).toHaveBeenCalledWith("run-1", "test", "web", expect.stringMatching(/^project-action-/));
});

it("keeps an unknown outcome on the same request key and does not automatically rerun", async () => {
  const { client, ui } = setup();
  client.runProjectAction.mockRejectedValueOnce(new TypeError("Network disconnected"));
  render(ui());
  fireEvent.click(await readyTests());
  await screen.findByRole("button", { name: "确认上次运行" });
  expect(client.runProjectAction).toHaveBeenCalledTimes(1);
  expect(screen.getByRole("button", { name: "运行测试" })).toBeDisabled();
  const key = client.runProjectAction.mock.calls[0][3];
  fireEvent.click(screen.getByRole("button", { name: "确认上次运行" }));
  await screen.findByText("expected true");
  expect(client.runProjectAction).toHaveBeenLastCalledWith("run-1", "test", ".", key);
});

it("allows correcting a rejected request and respects unavailable actions and read-only connections", async () => {
  const { client, ui } = setup();
  client.runProjectAction.mockRejectedValueOnce(new APIRequestError("Directory excluded", "POLICY_DENIED", 403));
  const { rerender } = render(ui());
  fireEvent.click(await readyTests());
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
  fireEvent.click(await readyTests());
  await waitFor(() => expect(client.runProjectAction).toHaveBeenCalledTimes(1));
  rerender(ui("run-2"));
  await readyTests();
  await act(async () => finish(result("run-1")));
  expect(screen.queryByText("job-run-1")).not.toBeInTheDocument();
  expect(screen.queryByText("expected true")).not.toBeInTheDocument();
});

it("reads and stops the active Job, then refreshes actions for the next execution", async () => {
  const { client, ui } = setup();
  const running = { ...result(), job: { ...result().job, state: "running", can_stop: true, exit_code: undefined } };
  let started = false;
  let finished = false;
  client.listProjectActions.mockImplementation(async () => ({
    ...actions(), actions: actions().actions.map((action) => ({ ...action, available: !started || finished })),
  }));
  client.runProjectAction.mockImplementation(async () => { started = true; return running; });
  client.getProjectActionJob.mockImplementation(async () => finished ? result() : running);
  client.stopThreadApplicationService.mockImplementation(async () => { finished = true; return {}; });
  render(ui());
  fireEvent.click(await readyTests());
  await screen.findByRole("button", { name: "停止运行" });
  await waitFor(() => expect(client.listProjectActions).toHaveBeenCalledTimes(2));
  expect(screen.getByRole("button", { name: "运行测试" })).toBeDisabled();
  fireEvent.click(await screen.findByRole("button", { name: "停止运行" }));
  await waitFor(() => expect(client.stopThreadApplicationService).toHaveBeenCalledWith("thread-1", "run-1", "job-run-1"));
  expect(client.getProjectActionJob).toHaveBeenCalledWith("run-1", "job-run-1", expect.any(AbortSignal));
  await waitFor(() => expect(screen.getByRole("button", { name: "运行测试" })).toBeEnabled());
});

it("blocks new execution during directory detection and ignores a late response for the previous directory", async () => {
  const { client, ui } = setup();
  let finishWeb!: (value: ReturnType<typeof actions>) => void;
  client.listProjectActions.mockImplementation((runID: string, target: string) => target === "web"
    ? new Promise((resolve) => { finishWeb = resolve; })
    : Promise.resolve({ ...actions(runID, target), actions: actions().actions.map((action) => ({ ...action, available: target === "." })) }));
  render(ui());
  await readyTests();
  fireEvent.change(screen.getByLabelText(/测试目录/), { target: { value: "web" } });
  expect(screen.getByRole("button", { name: "运行测试" })).toBeDisabled();
  fireEvent.click(screen.getByRole("button", { name: "运行测试" }));
  expect(client.runProjectAction).not.toHaveBeenCalled();
  fireEvent.change(screen.getByLabelText(/测试目录/), { target: { value: "other" } });
  await waitFor(() => expect(client.listProjectActions).toHaveBeenCalledWith("run-1", "other", expect.any(AbortSignal)));
  await act(async () => finishWeb(actions("run-1", "web")));
  expect(screen.getByLabelText(/测试目录/)).toHaveValue("other");
  expect(screen.getByRole("button", { name: "运行测试" })).toBeDisabled();
});

it("retains a running Job and its stop control when the selected test directory changes", async () => {
  const { client, ui } = setup();
  const running = { ...result(), job: { ...result().job, state: "running", can_stop: true, exit_code: undefined } };
  client.runProjectAction.mockResolvedValue(running);
  client.getProjectActionJob.mockResolvedValue(running);
  const { rerender } = render(ui("run-1", "web"));
  fireEvent.click(await readyTests());
  await screen.findByRole("button", { name: "停止运行" });
  rerender(ui("run-1", "other"));
  await waitFor(() => expect(client.listProjectActions).toHaveBeenCalledWith("run-1", "other", expect.any(AbortSignal)));
  fireEvent.click(screen.getByRole("button", { name: "停止运行" }));
  await waitFor(() => expect(client.stopThreadApplicationService).toHaveBeenCalledWith("thread-1", "run-1", "job-run-1"));
  expect(client.getProjectActionJob).toHaveBeenCalledWith("run-1", "job-run-1", expect.any(AbortSignal));
});

it("replays a recovered legacy request with its original empty target after another directory is selected", async () => {
  const { client, queries } = setup();
  const scope = "legacy-project-action";
  const key = `v2_recovery.v1:${encodeURIComponent(window.location.origin)}:${encodeURIComponent(client.baseURL)}:${scope}:${encodeURIComponent("run:run-1:project-action")}`;
  localStorage.setItem(key, JSON.stringify({ version: "v2_recovery.v1", value: {
    runID: "run-1", slot: "test", target: "", key: "project-action-original",
  } }));
  render(<LocaleProvider><QueryClientProvider client={queries}>
    <V2RecoveryProvider client={client} scopeID={scope}>
      <ProjectActions client={client as unknown as APIClient} runID="run-1" threadID="thread-1" testTarget="web" />
    </V2RecoveryProvider>
  </QueryClientProvider></LocaleProvider>);
  fireEvent.click(await screen.findByRole("button", { name: "确认上次运行" }));
  await waitFor(() => expect(client.runProjectAction).toHaveBeenCalledWith("run-1", "test", "", "project-action-original"));
  expect(screen.getByRole("textbox", { name: /测试目录/ })).toHaveValue("web");
});
