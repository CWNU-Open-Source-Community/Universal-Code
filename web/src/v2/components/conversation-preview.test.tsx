import type { ReactElement } from "react";
import type { RenderOptions } from "@testing-library/react";
import { LocaleProvider } from "../../lib/locale";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act, cleanup, fireEvent, render as renderComponent, screen, waitFor, within } from "@testing-library/react";
import { useState } from "react";
import { APIRequestError, type APIClient } from "../../api/client";
import type { ThreadDetailView, ThreadMessageControlView, WorkspaceView } from "../../api/types";
import { APPLICATION_PREVIEW_START_REQUEST, applicationPreviewRequestKey, type ApplicationPreviewRequest } from "../application-preview-request";
import { V2Conversation } from "./conversation";

vi.mock("../../hooks/use-run-event-stream", () => ({ useRunEventStream: () => ({ error: null, frames: [] }) }));
vi.mock("../../hooks/use-public-model-stream", () => ({ usePublicModelStream: () => ({ error: null, snapshot: null, status: "waiting" }) }));
vi.mock("./permission-control", () => ({ V2PermissionControl: () => null }));
vi.mock("./run-network-authority-control", () => ({ V2RunNetworkAuthorityControl: () => null }));
vi.mock("./model-route-control", () => ({ V2ModelRouteControl: () => null }));
vi.mock("./agent-browser", () => ({ V2AgentBrowser: () => null }));

afterEach(cleanup);

function detail(threadID: string): ThreadDetailView {
  return { thread: { id: threadID, title: `Conversation ${threadID}`, status: "active",
    workspace_id: "workspace-1", composer_state: "ready" },
    last_run: { id: `run-${threadID}`, status: "completed" }, runs: [], mission: {},
  } as unknown as ThreadDetailView;
}

function fixture(submitThreadTurn = vi.fn<APIClient["submitThreadTurn"]>().mockResolvedValue({
  run_id: "run-started", steering: { id: "startup-message" },
} as ThreadMessageControlView)) {
  const client = { hasThreadControl: true, submitThreadTurn,
    get: vi.fn((path: string) => Promise.resolve(detail(path.split("/").at(-1)!))),
    getPage: vi.fn().mockResolvedValue({ items: [], page: { limit: 100 }, requestID: "fixture" }),
  } as unknown as APIClient;
  const queries = new QueryClient({ defaultOptions: { queries: { retry: false, staleTime: Infinity }, mutations: { retry: false } } });
  function Harness() {
    const [threadID, setThreadID] = useState("thread-a");
    const [drafts, setDrafts] = useState<Record<string, string>>({ "thread-a": "保留我的已有要求" });
    return <QueryClientProvider client={queries}>
      <button onClick={() => setThreadID((current) => current === "thread-a" ? "thread-b" : "thread-a")} type="button">切换任务</button>
      <V2Conversation client={client} threadID={threadID} workspaces={[{ id: "workspace-1", name: "Workspace" }] as WorkspaceView[]}
        onArchive={vi.fn()} onManageModels={vi.fn()} onOpenInspector={vi.fn()}
        draft={drafts[threadID] ?? ""} onDraftChange={(next, expected) => setDrafts((current) =>
          expected === undefined || current[threadID] === expected ? { ...current, [threadID]: next } : current)} />
    </QueryClientProvider>;
  }
  render(<Harness />);
  return { queries, submitThreadTurn };
}

async function requestStart() {
  fireEvent.click(await screen.findByRole("button", { name: "应用预览" }));
  fireEvent.click(await screen.findByRole("button", { name: "把启动要求放回草稿" }));
  return screen.findByRole("textbox", { name: "继续对话" });
}

it("returns the complete startup requirement to the existing draft without submitting or claiming startup", async () => {
  const { submitThreadTurn, queries } = fixture();
  const input = await requestStart();
  expect(input).toHaveValue(`保留我的已有要求\n\n${APPLICATION_PREVIEW_START_REQUEST}`);
  expect(submitThreadTurn).not.toHaveBeenCalled();
  expect(screen.getByText("启动要求已放回草稿，请确认后发送。")).toBeInTheDocument();
  expect(queries.getQueryData<ApplicationPreviewRequest>(applicationPreviewRequestKey("thread-a"))?.phase).toBe("draft");
  fireEvent.click(screen.getByRole("button", { name: "查看应用服务" }));
  fireEvent.click(await screen.findByRole("button", { name: "把启动要求放回草稿" }));
  expect(input).toHaveValue(`保留我的已有要求\n\n${APPLICATION_PREVIEW_START_REQUEST}`);
  expect(submitThreadTurn).not.toHaveBeenCalled();
});

it("waits only after the user sends and retains the exact accepted source across Thread changes", async () => {
  let resolve!: (result: ThreadMessageControlView) => void;
  const pending = new Promise<ThreadMessageControlView>((finish) => { resolve = finish; });
  const submit = vi.fn<APIClient["submitThreadTurn"]>(() => pending);
  const { queries } = fixture(submit);
  await requestStart();
  fireEvent.click(screen.getByRole("button", { name: "发送消息" }));
  await screen.findByText("正在提交启动要求，尚未确认服务是否启动。");
  expect(submit).toHaveBeenCalledWith("thread-a", {
    version: "thread_message_submission.v1", content: `保留我的已有要求\n\n${APPLICATION_PREVIEW_START_REQUEST}`,
  }, expect.any(String));
  fireEvent.click(screen.getByRole("button", { name: "切换任务" }));
  await screen.findByText("Conversation thread-b");
  await act(async () => { resolve({ run_id: "run-started", steering: { id: "startup-message" } } as ThreadMessageControlView); await pending; });
  await waitFor(() => expect(queries.getQueryData<ApplicationPreviewRequest>(applicationPreviewRequestKey("thread-a"))).toEqual(
    expect.objectContaining({ phase: "accepted", runID: "run-started", messageID: "startup-message" })));
  expect(screen.queryByText("启动要求已受理，正在等待受管理的后台服务记录。")).not.toBeInTheDocument();
  fireEvent.click(screen.getByRole("button", { name: "切换任务" }));
  await screen.findByText("启动要求已受理，正在等待受管理的后台服务记录。");
  fireEvent.click(screen.getByRole("button", { name: "查看应用服务" }));
  const panel = await screen.findByRole("dialog", { name: "应用预览" });
  expect(within(panel).getByText("尚未发现本次启动要求关联的后台服务，可回对话查看批准或执行结果。")).toBeInTheDocument();
});

it("keeps a rejected startup requirement in the draft and reports that it was not queued", async () => {
  const submit = vi.fn<APIClient["submitThreadTurn"]>().mockRejectedValue(new APIRequestError("当前执行不接受消息", "CONFLICT", 409, "rejected", false));
  fixture(submit);
  const input = await requestStart();
  fireEvent.click(screen.getByRole("button", { name: "发送消息" }));
  await screen.findByText("启动要求未入队，草稿已保留。");
  expect(input).toHaveValue(`保留我的已有要求\n\n${APPLICATION_PREVIEW_START_REQUEST}`);
  expect(submit).toHaveBeenCalledTimes(1);
});

it("keeps an unknown submission unconfirmed with its original key and draft", async () => {
  const submit = vi.fn<APIClient["submitThreadTurn"]>().mockRejectedValue(new Error("响应中断"));
  const { queries } = fixture(submit);
  const input = await requestStart();
  fireEvent.click(screen.getByRole("button", { name: "发送消息" }));
  await screen.findByText("启动要求的提交结果尚未确认，请核对原提交。");
  expect(input).toHaveValue(`保留我的已有要求\n\n${APPLICATION_PREVIEW_START_REQUEST}`);
  const request = queries.getQueryData<ApplicationPreviewRequest>(applicationPreviewRequestKey("thread-a"));
  expect(request).toEqual(expect.objectContaining({ phase: "unconfirmed" }));
  expect(request?.runID).toBeUndefined();
  expect(request?.messageID).toBeUndefined();
  expect(new Set(submit.mock.calls.map(([, , key]) => key))).toEqual(new Set([request?.operationKey]));
  fireEvent.click(screen.getByRole("button", { name: "查看应用服务" }));
  expect(await screen.findByRole("button", { name: "把启动要求放回草稿" })).toBeDisabled();
});

// Retain textarea coverage through the supported plain-text editing mode.
beforeEach(() => localStorage.setItem("universal-code.composer-plain-text", "true"));

function render(ui: ReactElement, options?: RenderOptions) {
  window.localStorage.setItem("prayu.locale.v1", "zh-CN");
  return renderComponent(ui, { wrapper: LocaleProvider, ...options });
}
