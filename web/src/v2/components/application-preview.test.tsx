import type { ReactElement } from "react";
import type { RenderOptions } from "@testing-library/react";
import { LocaleProvider } from "../../lib/locale";
import { act, cleanup, fireEvent, render as renderComponent, screen, waitFor, within } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { createRef } from "react";
import type { APIClient } from "../../api/client";
import type { ThreadApplicationServiceView, ThreadApplicationServiceDetailView } from "../../api/types";
import type { ApplicationPreviewRequest } from "../application-preview-request";
import type { ApplicationPreview } from "../../api/application-preview";
import { V2ApplicationPreview } from "./application-preview";
import { fullCDPSessionQueryKey } from "./browser-cdp-control";

vi.mock("./permission-control", () => ({ V2PermissionControl: () => null }));
const observed: ApplicationPreview = { version: "full_cdp_preview.v1", run_id: "run-preview", session_id: "browser-preview",
  canonical_url: "http://127.0.0.1:18886/", captured_at: "2026-09-11T00:00:00Z",
  image: { media_type: "image/png", bytes: 128, sha256: "a".repeat(64), width: 800, height: 600 },
  page: { snapshot_id: "b".repeat(64), title: "独立项目应用", text: "点击计数 0", accessibility_nodes: 3, truncated: false,
    untrusted_evidence: true, elements: [{ selector: "#counter", tag: "button", name: "增加", disabled: false },
      { selector: "#name", tag: "input", type: "text", name: "称呼", disabled: false },
      { selector: "#hidden", tag: "input", type: "hidden", name: "内部字段", disabled: false }] } };

function fixture(cachedClosedSession = false, overrides: Partial<APIClient> = {}, startRequest?: ApplicationPreviewRequest) {
  let generation = 0;
  const post = vi.fn(async (path: string) => {
    if (path.endsWith("preview-action")) throw new Error("响应中断");
    generation++;
    return { ...observed, page: { ...observed.page, snapshot_id: String(generation).repeat(64) } };
  });
  const client = { hasFullCDPSessionControl: true, hasBrowserCDPPermissionControl: true, hasFullCDPDebug: true,
    get: vi.fn(async () => ({ run: { id: "run-preview", status: "running" }, execution_permission: { mode: "full", runtime_gate_available: true, revision: 1 },
      browser_cdp_permission: { mode: "full_debug", runtime_gate_available: true, revision: 1 } })),
    getFullCDPSession: vi.fn(async () => ({ session: { run_id: "run-preview", session_id: "browser-preview", state: cachedClosedSession ? "closed" : "ready", target_origin: "http://127.0.0.1:18886", browser: { product: "chrome", channel: "stable" }, process_tree_quiescent: true, profile_cleaned: true } })),
    postControl: post, downloadVerifiedImage: vi.fn(async () => new Blob(["png"])),
    closeFullCDPSession: vi.fn(async () => ({ session: { state: "closed", session_id: "browser-preview", process_tree_quiescent: true, profile_cleaned: true } })),
    ...overrides,
  } as unknown as APIClient;
  const queries = new QueryClient({ defaultOptions: { queries: { retry: false, staleTime: 5000 }, mutations: { retry: false } } });
  if (cachedClosedSession) queries.setQueryData(fullCDPSessionQueryKey("run-preview"), {
    session: { run_id: "run-preview", session_id: "browser-preview", state: "ready", target_origin: "http://127.0.0.1:18886" },
  });
  const view = render(<QueryClientProvider client={queries}><V2ApplicationPreview client={client} runID="run-preview" threadID="thread-preview"
    onClose={() => {}} onRequestStart={() => {}} startRequest={startRequest} returnFocusRef={createRef()} /></QueryClientProvider>);
  return { client, post, queries, view };
}
beforeEach(() => {
  vi.stubGlobal("URL", Object.assign(URL, { createObjectURL: vi.fn(() => "blob:verified-preview"), revokeObjectURL: vi.fn() }));
});
afterEach(() => { cleanup(); vi.restoreAllMocks(); vi.unstubAllGlobals(); });

it("requires a fresh observation after an unknown click and never automatically repeats the action", async () => {
  const { post } = fixture();
  await screen.findByRole("img", { name: "应用页面：独立项目应用" });
  expect(screen.getByText("run-preview")).not.toBeVisible();
  fireEvent.click(screen.getByText("浏览器来源"));
  expect(screen.getByText("run-preview")).toBeVisible();
  expect(screen.getByRole("textbox", { name: "项目应用地址" })).toHaveValue(observed.canonical_url);
  expect(screen.getByRole("combobox", { name: "预览浏览器" })).toHaveValue("chrome");
  fireEvent.click(screen.getByText("操作页面控件（2）"));
  expect(screen.queryByText("内部字段")).not.toBeInTheDocument();
  fireEvent.click(screen.getByRole("button", { name: "点击" }));
  await screen.findByRole("alert");
  expect(post.mock.calls.filter(([path]) => path.endsWith("preview-action"))).toHaveLength(1);
  expect(screen.queryByRole("img")).not.toBeInTheDocument();
  expect(screen.queryByRole("button", { name: "点击" })).not.toBeInTheDocument();
  fireEvent.click(screen.getByRole("button", { name: "刷新页面预览" }));
  await screen.findByRole("img", { name: "应用页面：独立项目应用" });
  expect(post.mock.calls.filter(([path]) => path.endsWith("preview-action"))).toHaveLength(1);
});

it("sends typed values only to the exact current session and observed selector", async () => {
  const { post } = fixture();
  await screen.findByRole("img", { name: "应用页面：独立项目应用" });
  fireEvent.click(screen.getByText("操作页面控件（2）"));
  fireEvent.change(screen.getByRole("textbox", { name: "页面输入 称呼" }), { target: { value: "测试用户" } });
  fireEvent.click(screen.getByRole("button", { name: "输入到页面" }));
  await waitFor(() => expect(post).toHaveBeenCalledWith(expect.stringMatching(/preview-action$/), {
    version: "full_cdp_preview_action.v1", expected_session_id: "browser-preview", expected_snapshot_id: "1".repeat(64),
    action: "type", selector: "#name", value: "测试用户",
  }, expect.stringMatching(/^v2-preview-action-/)));
});

it("stops showing the old image when the preview is closed", async () => {
  const { client } = fixture();
  await screen.findByRole("img", { name: "应用页面：独立项目应用" });
  fireEvent.click(screen.getByRole("button", { name: "关闭浏览器" }));
  await waitFor(() => expect(screen.queryByRole("img")).not.toBeInTheDocument());
  expect(client.closeFullCDPSession).toHaveBeenCalledWith("run-preview", expect.objectContaining({ expected_session_id: "browser-preview" }), expect.any(String));
});

it("rechecks a cached ready session before reading a preview that was closed elsewhere", async () => {
  const { post } = fixture(true);
  await screen.findByText("预览已关闭，浏览器和临时资料已清理。");
  expect(post).not.toHaveBeenCalled();
  expect(screen.queryByRole("img")).not.toBeInTheDocument();
  expect(screen.queryByRole("alert")).not.toBeInTheDocument();
});

function service(jobID: string, overrides: Partial<ThreadApplicationServiceView> = {}): ThreadApplicationServiceView {
  return { thread_id: "thread-preview", run_id: "run-service", job_id: jobID, state: "running",
    created_at: "2026-10-05T00:00:00Z", can_stop: true, ...overrides };
}

function serviceDetail(source: ThreadApplicationServiceView): ThreadApplicationServiceDetailView {
  return { version: "thread_application_services.v1", service: source,
    output: { available: true, stdout: "Local: http://127.0.0.1:3000/\n",
      stderr: "Port 3000 was in use. <script>untrusted</script>\n",
      base_cursor: 0, next_cursor: 20, end_cursor: 20, dropped: false },
    candidate_urls: ["http://127.0.0.1:3000/", "http://127.0.0.1:3001/"].map((url) =>
      ({ url, source: "command_output", verified: false })) };
}

function servicesFixture(initial: ThreadApplicationServiceView[], options?: {
  overrides?: Partial<APIClient>; startRequest?: ApplicationPreviewRequest; runStatus?: string;
}) {
  let records = initial;
  const listThreadApplicationServices = vi.fn(async () => ({ version: "thread_application_services.v1",
    thread_id: "thread-preview", services: records, has_more: false }));
  const getThreadApplicationService = vi.fn(async (_threadID: string, jobID: string) =>
    serviceDetail(records.find((record) => record.job_id === jobID)!));
  const stopThreadApplicationService = vi.fn(async (_threadID: string, runID: string, jobID: string) => {
    records = records.map((record) => record.run_id === runID && record.job_id === jobID
      ? { ...record, state: "cancelled", can_stop: false } : record);
    return { version: "thread_application_services.v1", service: records.find((record) => record.job_id === jobID)!, replayed: false };
  });
  const openFullCDPSession = vi.fn(async (runID: string) => ({ session: {
    run_id: runID, state: "starting", session_id: "source-browser", target_origin: "http://127.0.0.1:3001",
  } }));
  const result = fixture(false, {
    hasControl: true, hasRunExecution: true, listThreadApplicationServices, getThreadApplicationService, stopThreadApplicationService,
    get: vi.fn(async (path: string) => ({ run: { id: path.split("/").at(-1), status: options?.runStatus ?? "running" },
      execution_permission: { mode: "full", runtime_gate_available: true, revision: 4 },
      browser_cdp_permission: { mode: "full_debug", runtime_gate_available: true, revision: 7 } })) as APIClient["get"],
    getFullCDPSession: vi.fn(async () => ({ session: null })), openFullCDPSession,
    ...options?.overrides,
  } as unknown as Partial<APIClient>, options?.startRequest);
  return { ...result, getThreadApplicationService, listThreadApplicationServices, stopThreadApplicationService, openFullCDPSession };
}

it("requires an explicit address choice and opens the existing managed browser for the service's source Run", async () => {
  const { getThreadApplicationService, openFullCDPSession } = servicesFixture([service("job-a"), service("job-b")]);
  await screen.findByRole("button", { name: /Job job-a/ });
  expect(getThreadApplicationService).not.toHaveBeenCalled();
  fireEvent.click(screen.getByRole("button", { name: /Job job-a/ }));
  const secondAddress = await screen.findByRole("button", { name: "选择地址 http://127.0.0.1:3001/" });
  expect(screen.getByRole("textbox", { name: "项目应用地址" })).toHaveValue("");
  expect(screen.getByRole("button", { name: "打开应用" })).toBeDisabled();
  fireEvent.click(screen.getByText("查看原始启动输出"));
  expect(screen.getByText(/Port 3000 was in use/)).toHaveTextContent("<script>untrusted</script>");
  expect(document.querySelector("script")).toBeNull();
  fireEvent.click(secondAddress);
  await waitFor(() => expect(screen.getByRole("button", { name: "打开应用" })).toBeEnabled());
  fireEvent.click(screen.getByRole("button", { name: "打开应用" }));
  await waitFor(() => expect(openFullCDPSession).toHaveBeenCalledWith("run-service", expect.objectContaining({
    target: "http://127.0.0.1:3001/", expected_execution_permission_revision: 4,
    expected_browser_cdp_permission_revision: 7,
  }), expect.any(String)));
});

it("associates the startup request only with a matching source Run and message", async () => {
  const { getThreadApplicationService } = servicesFixture([
    service("job-unrelated", { source_message_id: "old-message" }),
    service("job-related", { source_message_id: "startup-message" }),
    service("job-other-run", { run_id: "other-run", source_message_id: "startup-message" }),
  ], { startRequest: { request: "Start", phase: "accepted", runID: "run-service", messageID: "startup-message" } });
  await screen.findByRole("button", { name: "选择地址 http://127.0.0.1:3001/" });
  expect(getThreadApplicationService).toHaveBeenCalledWith("thread-preview", "job-related", expect.any(AbortSignal));
  expect(getThreadApplicationService.mock.calls.every(([, jobID]) => jobID === "job-related")).toBe(true);
  expect(screen.getByRole("button", { name: /Job job-related/ })).toHaveAttribute("aria-pressed", "true");
  expect(screen.getByRole("button", { name: /Job job-unrelated/ })).toHaveAttribute("aria-pressed", "false");
});

it("keeps browser readiness independent from a failed service and prevents opening its output addresses", async () => {
  const source = service("job-failed", { state: "failed", exit_code: 1, can_stop: false });
  const { openFullCDPSession } = servicesFixture([source], { overrides: {
    getFullCDPSession: vi.fn(async (runID: string) => ({ session: { state: "ready", run_id: runID,
      session_id: "browser-preview", target_origin: "http://127.0.0.1:18886" } })) as unknown as APIClient["getFullCDPSession"],
  } });
  fireEvent.click(await screen.findByRole("button", { name: /Job job-failed/ }));
  const address = await screen.findByRole("button", { name: "选择地址 http://127.0.0.1:3001/" });
  expect(address).toBeDisabled();
  expect(screen.getByText("任务命令启动或执行失败 · 退出码 1")).toBeInTheDocument();
  expect(screen.getByText("浏览器已就绪")).toBeInTheDocument();
  expect(screen.queryByRole("button", { name: "停止此命令" })).not.toBeInTheDocument();
  expect(openFullCDPSession).not.toHaveBeenCalled();
});

it("does not borrow the current Run's browser authority for a terminal source Run", async () => {
  const { openFullCDPSession } = servicesFixture([service("job-old")], { runStatus: "completed" });
  fireEvent.click(await screen.findByRole("button", { name: /Job job-old/ }));
  const address = await screen.findByRole("button", { name: "选择地址 http://127.0.0.1:3001/" });
  expect(address).toBeDisabled();
  expect(screen.getByRole("button", { name: "打开应用" })).toBeDisabled();
  expect(screen.getByText("服务所属执行已退出或无法确认，不能打开其预览浏览器。")).toBeInTheDocument();
  expect(openFullCDPSession).not.toHaveBeenCalled();
});

it("stops only the selected service while closing the browser leaves both service Jobs alone", async () => {
  const { client, stopThreadApplicationService } = servicesFixture([service("job-a"), service("job-b")]);
  fireEvent.click(await screen.findByRole("button", { name: /Job job-b/ }));
  fireEvent.click(await screen.findByRole("button", { name: "选择地址 http://127.0.0.1:3001/" }));
  await waitFor(() => expect(screen.getByRole("button", { name: "打开应用" })).toBeEnabled());
  fireEvent.click(screen.getByRole("button", { name: "打开应用" }));
  fireEvent.click(await screen.findByRole("button", { name: "关闭浏览器" }));
  await waitFor(() => expect(client.closeFullCDPSession).toHaveBeenCalledWith("run-service",
    expect.objectContaining({ expected_session_id: "source-browser" }), expect.any(String)));
  expect(stopThreadApplicationService).not.toHaveBeenCalled();
  fireEvent.click(screen.getByRole("button", { name: "停止此命令" }));
  await waitFor(() => expect(stopThreadApplicationService).toHaveBeenCalledExactlyOnceWith("thread-preview", "run-service", "job-b"));
  await screen.findByText("任务命令已停止", { selector: "p[role='status']" });
  expect(screen.getByRole("button", { name: /Job job-a/ })).toHaveTextContent("命令进程运行中");
});

it("does not automatically repeat an unconfirmed stop request", async () => {
  let rejectStop!: (failure: Error) => void;
  const pendingStop = new Promise<never>((_resolve, reject) => { rejectStop = reject; });
  const stop = vi.fn(() => pendingStop);
  servicesFixture([service("job-a")], { overrides: { stopThreadApplicationService: stop } });
  fireEvent.click(await screen.findByRole("button", { name: /Job job-a/ }));
  await screen.findByRole("button", { name: "选择地址 http://127.0.0.1:3001/" });
  const button = screen.getByRole("button", { name: "停止此命令" });
  fireEvent.click(button); fireEvent.click(button);
  await waitFor(() => expect(stop).toHaveBeenCalledTimes(1));
  rejectStop(new Error("响应中断"));
  await screen.findByText(/停止服务未确认完成/);
  expect(stop).toHaveBeenCalledTimes(1);
  expect(screen.queryByText("任务命令已停止")).not.toBeInTheDocument();
});

it.each([
  { hasControl: false, hasRunExecution: true },
  { hasControl: true, hasRunExecution: false },
])("uses execution capability for service cleanup: %j", async (capabilities) => {
  const { stopThreadApplicationService } = servicesFixture([service("job-a")], { overrides: capabilities });
  fireEvent.click(await screen.findByRole("button", { name: /Job job-a/ }));
  await screen.findByRole("button", { name: "选择地址 http://127.0.0.1:3001/" });
  const button = screen.getByRole("button", { name: "停止此命令" });
  if (capabilities.hasRunExecution) {
    expect(button).toBeEnabled();
    fireEvent.click(button);
    await waitFor(() => expect(stopThreadApplicationService).toHaveBeenCalledExactlyOnceWith("thread-preview", "run-service", "job-a"));
  } else {
    expect(button).toBeDisabled();
    fireEvent.click(button);
    expect(stopThreadApplicationService).not.toHaveBeenCalled();
  }
});

it("keeps an exited shared service pending cleanup until the process tree is reaped", async () => {
  servicesFixture([service("job-cleanup", { workspace_mode: "shared_service", state: "cancelled", can_stop: false, tree_reaped: false })]);
  const entry = await screen.findByRole("button", { name: /Job job-cleanup/ });
  expect(entry).toHaveTextContent("任务命令已退出，清理待确认");
  fireEvent.click(entry);
  expect(await screen.findByText("任务命令已退出，清理待确认", { selector: "p[role='status']" })).toBeVisible();
  expect(screen.queryByText("任务命令已停止")).not.toBeInTheDocument();
});

it("keeps a cold running record unconfirmed and prevents using its saved address", async () => {
  servicesFixture([service("job-cold", { can_stop: false })]);
  fireEvent.click(await screen.findByRole("button", { name: /Job job-cold/ }));
  const address = await screen.findByRole("button", { name: "选择地址 http://127.0.0.1:3001/" });
  expect(address).toBeDisabled();
  expect(screen.getByRole("button", { name: "打开应用" })).toBeDisabled();
  expect(screen.queryByRole("button", { name: "停止此命令" })).not.toBeInTheDocument();
  expect(screen.getByText("保存的运行记录，当前服务状态未确认", { selector: "p" })).toBeInTheDocument();
});

it("keeps late service and browser responses attached to their original Run after selecting another Job", async () => {
  const first = service("job-a", { run_id: "run-a" });
  const second = service("job-b", { run_id: "run-b" });
  let resolveDetail!: (value: ThreadApplicationServiceDetailView) => void;
  const oldDetail = new Promise<ThreadApplicationServiceDetailView>((resolve) => { resolveDetail = resolve; });
  let resolveSession!: (value: unknown) => void;
  const oldSession = new Promise((resolve) => { resolveSession = resolve; });
  const getFullCDPSession = vi.fn((runID: string) => runID === "run-a" ? oldSession : Promise.resolve({
    session: runID === "run-b" ? { state: "starting", run_id: runID, session_id: "browser-b" } : null,
  }));
  const { client } = servicesFixture([first, second], { overrides: {
    getThreadApplicationService: vi.fn((_threadID, jobID) => jobID === "job-a" ? oldDetail : Promise.resolve(serviceDetail(second))),
    getFullCDPSession,
  } as unknown as Partial<APIClient> });
  fireEvent.click(await screen.findByRole("button", { name: /Job job-a/ }));
  await waitFor(() => expect(getFullCDPSession).toHaveBeenCalledWith("run-a", expect.any(AbortSignal)));
  fireEvent.click(screen.getByRole("button", { name: /Job job-b/ }));
  await screen.findByRole("button", { name: "选择地址 http://127.0.0.1:3001/" });
  await act(async () => {
    resolveDetail({ ...serviceDetail(first), output: { ...serviceDetail(first).output, stdout: "old-run-output" },
      candidate_urls: [{ url: "http://127.0.0.1:8999/", source: "command_output", verified: false }] });
    resolveSession({ session: { state: "ready", run_id: "run-a", session_id: "browser-a", target_origin: "http://127.0.0.1:8999" } });
    await Promise.all([oldDetail, oldSession]);
  });
  const browser = screen.getByRole("region", { name: "预览浏览器控制" });
  expect(within(browser).getByText("run-b")).toBeInTheDocument();
  expect(screen.queryByText("old-run-output")).not.toBeInTheDocument();
  expect(screen.queryByRole("button", { name: "选择地址 http://127.0.0.1:8999/" })).not.toBeInTheDocument();
  fireEvent.click(within(browser).getByRole("button", { name: "关闭浏览器" }));
  await waitFor(() => expect(client.closeFullCDPSession).toHaveBeenCalledWith("run-b",
    expect.objectContaining({ expected_session_id: "browser-b" }), expect.any(String)));
});

it("loads original tool evidence only on demand and rejects another Run's record", async () => {
  const threadActivityDetail = vi.fn().mockResolvedValue({ run_id: "another-run", tools: [] });
  servicesFixture([service("job-a", { source_call_id: "source-call" })], {
    overrides: { threadActivityDetail },
  });
  fireEvent.click(await screen.findByRole("button", { name: /Job job-a/ }));
  const disclosure = await screen.findByText("查看启动工具记录与已保存输出");
  expect(threadActivityDetail).not.toHaveBeenCalled();
  fireEvent.click(disclosure);
  await screen.findByText("启动工具记录读取失败。");
  expect(threadActivityDetail).toHaveBeenCalledWith("thread-preview", "source-call", expect.any(AbortSignal));
});

it("explains incomplete retained output and a bounded service list", async () => {
  const source = service("job-output");
  servicesFixture([source], { overrides: {
    listThreadApplicationServices: vi.fn().mockResolvedValue({ version: "thread_application_services.v1",
      thread_id: "thread-preview", services: [source], has_more: true }),
    getThreadApplicationService: vi.fn().mockResolvedValue({ ...serviceDetail(source), output: {
      ...serviceDetail(source).output, base_cursor: 10, dropped: true, truncation_reason: "retained_tail", next_cursor: 20, end_cursor: 40,
    } }),
  } });
  fireEvent.click(await screen.findByRole("button", { name: /Job job-output/ }));
  await screen.findByText("启动输出未完整保留（retained_tail）。");
  expect(screen.getByText("当前展示部分保留输出。")).toBeInTheDocument();
  expect(screen.getByText("当前只展示最近一批后台任务，列表尚未完整。")).toBeInTheDocument();
});

function render(ui: ReactElement, options?: RenderOptions) {
  localStorage.setItem("prayu.locale.v1", "zh-CN");
  const Wrapper = options?.wrapper;
  return renderComponent(ui, {...options, wrapper: ({children}) => <LocaleProvider>{Wrapper ? <Wrapper>{children}</Wrapper> : children}</LocaleProvider>});
}
