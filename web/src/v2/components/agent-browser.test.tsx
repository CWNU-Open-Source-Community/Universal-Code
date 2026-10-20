import { act, cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { APIRequestError, type APIClient } from "../../api/client";
import { V2AgentBrowser } from "./agent-browser";
import { agentBrowserQueryKey } from "../../api/agent-browser";

const status = (runID = "run-browser", sessionID = "agent-browser-one") => ({
  version: "agent_browser_status.v1", run_id: runID, session_id: sessionID, generation: 1,
  state: "ready", available: true, can_start: true, product: "edge", headless: true,
  url: "https://example.com/report", title: `Report ${runID}`, document_epoch: 2,
  last_action: "browser_navigate", updated_at: "2026-09-22T10:00:00Z",
  screenshot: { locator: `agent-browser/${sessionID}/capture.png`, sha256: "a".repeat(64), byte_size: 3, mime_type: "image/png" },
  cleanup_pending: false, tree_reaped: false, profile_removed: false,
});

function draw(client: APIClient, runID = "run-browser", onDismiss?: () => void) {
  const queries = new QueryClient({ defaultOptions: { queries: { retry: false, gcTime: 0 }, mutations: { retry: false } } });
  const view = render(<QueryClientProvider client={queries}><V2AgentBrowser client={client} runID={runID} running={false} onDismiss={onDismiss} /></QueryClientProvider>);
  return { ...view, queries };
}

beforeEach(() => vi.stubGlobal("URL", Object.assign(URL, {
  createObjectURL: vi.fn(() => "blob:agent-browser"), revokeObjectURL: vi.fn(),
})));
afterEach(() => { cleanup(); vi.restoreAllMocks(); vi.unstubAllGlobals(); });

it("uses a read-only GET and renders only a verified screenshot", async () => {
  const get = vi.fn(async () => status());
  const postControl = vi.fn();
  const downloadVerifiedImage = vi.fn(async () => new Blob(["png"], { type: "image/png" }));
  const client = { baseURL: "/api/v1", get, postControl, downloadVerifiedImage } as unknown as APIClient;
  draw(client);
  expect(await screen.findByText("Report run-browser")).toBeInTheDocument();
  await screen.findByRole("img", { name: "Agent 浏览器页面：Report run-browser" });
  expect(get).toHaveBeenCalledWith("/runs/run-browser/agent-browser", {}, expect.any(AbortSignal));
  expect(postControl).not.toHaveBeenCalled();
  expect(downloadVerifiedImage).toHaveBeenCalledWith(expect.stringContaining("session_id=agent-browser-one"),
    status().screenshot, expect.any(AbortSignal));
  expect(screen.getByText(/浏览器在后台运行/)).toBeInTheDocument();
  expect(screen.queryByRole("button", { name: /打开/ })).not.toBeInTheDocument();
});

it("closes the exact observed session once and distinguishes the Run", async () => {
  const get = vi.fn(async () => status());
  const closed = { ...status(), state: "closed", screenshot: undefined, tree_reaped: true, profile_removed: true };
  const postControl = vi.fn(async () => closed);
  const client = { baseURL: "/api/v1", get, postControl,
    downloadVerifiedImage: vi.fn(async () => new Blob(["png"])) } as unknown as APIClient;
  draw(client);
  fireEvent.click(await screen.findByRole("button", { name: "停止浏览器" }));
  await screen.findByText("浏览器已停止");
  expect(postControl).toHaveBeenCalledTimes(1);
  expect(postControl).toHaveBeenCalledWith("/runs/run-browser/agent-browser/close", {
    version: "agent_browser_close.v1", session_id: "agent-browser-one",
  }, "agent-browser-close-agent-browser-one");
  expect(screen.queryByRole("button", { name: "停止浏览器" })).not.toBeInTheDocument();
});

it("does not show a late status or image after switching Runs", async () => {
  let resolveOld!: (value: unknown) => void;
  const old = new Promise((resolve) => { resolveOld = resolve; });
  const get = vi.fn((path: string) => path.includes("run-old") ? old : Promise.resolve(status("run-new", "agent-browser-new")));
  const downloadVerifiedImage = vi.fn(async (_path: string, _metadata: unknown, _signal?: AbortSignal) => new Blob(["png"]));
  const client = { baseURL: "/api/v1", get, postControl: vi.fn(), downloadVerifiedImage } as unknown as APIClient;
  const page = draw(client, "run-old");
  page.rerender(<QueryClientProvider client={page.queries}><V2AgentBrowser client={client} runID="run-new" running={false} /></QueryClientProvider>);
  expect(await screen.findByText("Report run-new")).toBeInTheDocument();
  resolveOld(status("run-old", "agent-browser-old"));
  await Promise.resolve();
  expect(screen.queryByText("Report run-old")).not.toBeInTheDocument();
  expect(downloadVerifiedImage.mock.calls.every(([path]) => String(path).includes("agent-browser-new"))).toBe(true);
});

it("never retries a failed close and refreshes status with GET", async () => {
  const get = vi.fn(async () => status());
  const postControl = vi.fn(async () => { throw new Error("connection interrupted"); });
  const client = { baseURL: "/api/v1", get, postControl,
    downloadVerifiedImage: vi.fn(async () => new Blob(["png"])) } as unknown as APIClient;
  draw(client);
  fireEvent.click(await screen.findByRole("button", { name: "停止浏览器" }));
  await screen.findByRole("alert");
  await waitFor(() => expect(get.mock.calls.length).toBeGreaterThanOrEqual(2));
  expect(postControl).toHaveBeenCalledTimes(1);
  expect(screen.getByText(/请核对最新状态后再决定是否重试停止/)).toBeInTheDocument();
});

it("keeps a newer same-Run session when an old close finishes", async () => {
  let finish!: (value: unknown) => void;
  const postControl = vi.fn(() => new Promise((resolve) => { finish = resolve; }));
  const client = { baseURL: "/api/v1", get: vi.fn(async () => status()), postControl,
    downloadVerifiedImage: vi.fn(async () => new Blob(["png"])) } as unknown as APIClient;
  const page = draw(client);
  fireEvent.click(await screen.findByRole("button", { name: "停止浏览器" }));
  await waitFor(() => expect(postControl).toHaveBeenCalledTimes(1));
  await act(async () => { page.queries.setQueryData(agentBrowserQueryKey(client.baseURL, "run-browser"),
    { ...status("run-browser", "agent-browser-new"), title: "New document", state: "loading", screenshot: undefined }); });
  await screen.findByText("New document");
  expect(screen.queryByRole("img")).not.toBeInTheDocument();
  await act(async () => { finish({ ...status(), state: "closed", screenshot: undefined }); });
  expect(screen.getByText("New document")).toBeInTheDocument();
  expect(screen.getByText("正在加载网页…")).toBeInTheDocument();
  expect(screen.getByRole("button", { name: "停止浏览器" })).toBeEnabled();
});

it("renders a failed browser with a stop control", async () => {
  const client = { baseURL: "/api/v1", get: vi.fn(async () => ({ ...status(), state: "failed", screenshot: undefined })),
    postControl: vi.fn(), downloadVerifiedImage: vi.fn() } as unknown as APIClient;
  draw(client);
  expect(await screen.findByText("浏览器操作未完成")).toBeInTheDocument();
  expect(screen.getByRole("button", { name: "停止浏览器" })).toBeEnabled();
});

it.each([
  ["platform_unsupported", /当前系统尚未提供可用的 Agent 浏览器适配器/],
  ["full_access_required", /完全访问（Full）/],
  ["runtime_unavailable", /没有可用的 Agent 浏览器运行时/],
])("shows the actual unavailable state: %s", async (code, message) => {
  const client = { baseURL: "/api/v1", get: vi.fn(async () => ({ ...status(), available: false, can_start: false,
    session_id: undefined, screenshot: undefined, state: "unavailable", unavailable_code: code })),
    postControl: vi.fn(), downloadVerifiedImage: vi.fn() } as unknown as APIClient;
  draw(client);
  expect(await screen.findByText(message)).toBeInTheDocument();
  expect(screen.queryByRole("button", { name: "停止浏览器" })).not.toBeInTheDocument();
  expect(client.postControl).not.toHaveBeenCalled();
});

it("keeps a dismissed notice hidden across refreshes and shows a new browser session", async () => {
  const unavailable = { ...status(), available: false, can_start: false, session_id: undefined,
    screenshot: undefined, state: "unavailable", unavailable_code: "full_access_required" };
  const get = vi.fn(async () => unavailable as unknown);
  const client = { baseURL: "/api/v1", get, postControl: vi.fn(), downloadVerifiedImage: vi.fn() } as unknown as APIClient;
  const onDismiss = vi.fn();
  const view = draw(client, "run-browser", onDismiss);
  fireEvent.click(await screen.findByRole("button", { name: "关闭浏览器提示" }));
  expect(screen.queryByRole("region", { name: "Agent 浏览器" })).not.toBeInTheDocument();
  expect(onDismiss).toHaveBeenCalledTimes(1);
  get.mockResolvedValue({ ...unavailable, updated_at: "2026-10-19T10:00:00Z" });
  await act(async () => { await view.queries.refetchQueries({ queryKey: agentBrowserQueryKey(client.baseURL, "run-browser") }); });
  expect(screen.queryByRole("region", { name: "Agent 浏览器" })).not.toBeInTheDocument();
  get.mockResolvedValue({ ...status(), screenshot: undefined });
  await act(async () => { await view.queries.refetchQueries({ queryKey: agentBrowserQueryKey(client.baseURL, "run-browser") }); });
  expect(await screen.findByText("浏览器正在运行")).toBeInTheDocument();
  expect(client.postControl).not.toHaveBeenCalled();
});

it.each([
  [{ state: "waiting_user" }, "网页需要人工处理"],
  [{ state: "ready", failure_code: "action_failed" }, "最近一次页面操作失败，请让 Agent 重新读取页面后再试。"],
])("dismisses routine activity without stopping it and resurfaces for %o", async (attention, text) => {
  const client = { baseURL: "/api/v1", get: vi.fn(async () => ({ ...status(), screenshot: undefined })),
    postControl: vi.fn(), downloadVerifiedImage: vi.fn() } as unknown as APIClient;
  const view = draw(client);
  fireEvent.click(await screen.findByRole("button", { name: "关闭浏览器提示" }));
  await act(async () => { view.queries.setQueryData(agentBrowserQueryKey(client.baseURL, "run-browser"),
    { ...status(), state: "loading", screenshot: undefined }); });
  expect(screen.queryByRole("region", { name: "Agent 浏览器" })).not.toBeInTheDocument();
  await act(async () => { view.queries.setQueryData(agentBrowserQueryKey(client.baseURL, "run-browser"),
    { ...status(), ...attention, screenshot: undefined }); });
  expect(await screen.findByText(text)).toBeInTheDocument();
  expect(screen.getByRole("button", { name: "停止浏览器" })).toBeEnabled();
  expect(client.postControl).not.toHaveBeenCalled();
});

it("allows dismissing a status error and resets dismissal when changing Runs", async () => {
  const client = { baseURL: "/api/v1", get: vi.fn(async () => { throw new Error("offline"); }),
    postControl: vi.fn(), downloadVerifiedImage: vi.fn() } as unknown as APIClient;
  const view = draw(client);
  fireEvent.click(await screen.findByRole("button", { name: "关闭浏览器提示" }));
  expect(screen.queryByRole("alert")).not.toBeInTheDocument();
  view.rerender(<QueryClientProvider client={view.queries}><V2AgentBrowser client={client} runID="run-new" running={false} /></QueryClientProvider>);
  expect(await screen.findByRole("alert")).toHaveTextContent("无法读取浏览器状态");
  expect(screen.getByRole("button", { name: "重新读取" })).toBeEnabled();
});

it("hides an older server without the status route", async () => {
  const client = { baseURL: "/api/v1", get: vi.fn(async () => { throw new APIRequestError("missing", "NOT_FOUND", 404); }),
    postControl: vi.fn(), downloadVerifiedImage: vi.fn() } as unknown as APIClient;
  const page = draw(client);
  await waitFor(() => expect(client.get).toHaveBeenCalled());
  expect(page.container).toBeEmptyDOMElement();
});
