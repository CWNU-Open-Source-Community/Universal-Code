import type { ReactElement } from "react";
import type { RenderOptions } from "@testing-library/react";
import { LocaleProvider } from "../../lib/locale";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { render as renderComponent, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { APIClient } from "../../api/client";
import type { ApprovalPreviewView, ApprovalQueueItemView } from "../../api/types";
import { V2ApprovalCards } from "./approval-cards";

afterEach(() => vi.unstubAllGlobals());

function pending(overrides: Partial<ApprovalQueueItemView> = {}): ApprovalQueueItemView {
  return {
    action_class: "public_https_fetch",
    allowed_actions: ["approve_once", "approve_for_thread", "deny"],
    canonical_url: "https://arxiv.org/abs/2608.13637",
    capability_grant: false,
    created_at: "2026-09-02T00:00:00Z",
    exact_target: "arxiv.org",
    id: "approval-web-fetch-1",
    mode: "per_call",
    process_execution_enabled: false,
    proposal_id: "web-fetch-authorization-1",
    run_id: "run-1",
    session_id: "session-1",
    status: "pending",
    tool_name: "web_fetch",
    updated_at: "2026-09-02T00:00:00Z",
    version: 1,
    workspace_id: "",
    ...overrides,
  };
}

function renderCards(item: ApprovalQueueItemView, previewOverrides = {}, onReviewFile = vi.fn()) {
  const approvalQueue = vi.fn().mockResolvedValue({
    protocol_version: "approval_queue.v1", run_id: "run-1", items: [item],
    truncated: false, process_execution_enabled: false,
    session_grant_created: false, capability_grant: false,
  });
  const decideApproval = vi.fn().mockResolvedValue({
    version: "approval_control.v1", run_id: "run-1", approval_id: item.id,
    proposal_id: item.proposal_id, tool_name: item.tool_name,
    action: "approve_for_thread", status: "approved", replayed: false,
    process_execution_enabled: false, shell_execution_enabled: false,
    docker_execution_enabled: false, workspace_write_applied: false,
    session_grant_created: false, capability_grant: false,
    execution_resumed: true, retry_completed: true,
  });
  const approvalPreview = vi.fn().mockResolvedValue({
    protocol_version: "approval_queue.v1", run_id: "run-1", approval_id: item.id,
    proposal_id: item.proposal_id, tool_name: item.tool_name, workspace_id: item.workspace_id,
    effect: item.tool_name === "web_fetch" ? "fetch_public_https" : "dry_run",
    working_directory: item.tool_name === "web_fetch" ? "" : ".",
    fields: item.tool_name === "web_fetch"
      ? [{ name: "url", value: item.canonical_url }]
      : [{ name: "command", value: "echo inspection-only" }],
    source_current: true, redacted: false, truncated: false, ...previewOverrides,
  });
  const client = { hasApprovalControl: true, approvalQueue, decideApproval, approvalPreview,
  } as unknown as APIClient;
  const queryClient = new QueryClient({ defaultOptions: {
    queries: { retry: false }, mutations: { retry: false },
  } });
  render(<QueryClientProvider client={queryClient}>
    <V2ApprovalCards client={client} runID="run-1" threadID="thread-1" onReviewFile={onReviewFile} />
  </QueryClientProvider>);
  return { decideApproval, approvalPreview, onReviewFile };
}

function renderReadOnlyCards(item: ApprovalQueueItemView | null = pending(),
  previewOverrides: Partial<ApprovalPreviewView> = {}, failure: "queue" | "preview" | null = null) {
  const queue = { protocol_version: "approval_queue.v1", run_id: "run-1", items: item ? [item] : [],
    truncated: false, process_execution_enabled: false, session_grant_created: false, capability_grant: false };
  const preview = item && { protocol_version: "approval_queue.v1", run_id: "run-1", approval_id: item.id,
    proposal_id: item.proposal_id, tool_name: item.tool_name, workspace_id: item.workspace_id,
    effect: item.tool_name === "command_runtime" ? "command_process" : "fetch_public_https",
    working_directory: "", fields: [{ name: "summary", value: "Read-only exact proposal [REDACTED]" }],
    source_current: true, redacted: true, truncated: false, ...previewOverrides };
  const fetchMock = vi.fn(async (url: string, init?: RequestInit) => {
    const isPreview = String(url).endsWith("/preview");
    if (init?.method !== "GET") throw new Error("Unexpected approval mutation");
    const failed = failure === (isPreview ? "preview" : "queue");
    return new Response(JSON.stringify(failed
      ? { version: "api.v1", request_id: "readonly-failure", error: { code: "UNAVAILABLE", message: "Read unavailable" } }
      : { version: "api.v1", request_id: "readonly-approval", data: isPreview ? preview : queue }),
    { status: failed ? 503 : 200, headers: { "Content-Type": "application/json" } });
  });
  vi.stubGlobal("fetch", fetchMock);
  const client = new APIClient("test-read", "/api/v1");
  const decideApproval = vi.spyOn(client, "decideApproval");
  const view = render(<QueryClientProvider client={new QueryClient({ defaultOptions: { queries: { retry: false } } })}>
    <V2ApprovalCards client={client} runID="run-1" threadID="thread-1" />
  </QueryClientProvider>);
  return { ...view, fetchMock, decideApproval, restoreReads: () => { failure = null; } };
}

describe("V2ApprovalCards", () => {
  it.each([
    pending(),
    pending({ tool_name: "command_runtime", action_class: "command_process",
      allowed_actions: ["approve_once", "approve_for_run", "deny"], canonical_url: undefined, exact_target: undefined }),
    pending({ status: "approved", allowed_actions: ["approve_for_thread"], version: 2 }),
  ])("shows the exact proposal for a read-only $tool_name/$status connection without decision controls", async (item) => {
    const { fetchMock, decideApproval } = renderReadOnlyCards(item);
    expect(await screen.findByText("Read-only exact proposal [REDACTED]")).toBeVisible();
    expect(screen.getByRole("region", { name: "待处理审批" })).toBeVisible();
    expect(screen.getByText(/当前连接可以查看审批/)).toBeVisible();
    expect(screen.getByText("敏感内容已脱敏，请按可见内容核对操作。")).toBeVisible();
    expect(screen.queryByRole("button")).not.toBeInTheDocument();
    expect(screen.queryByRole("textbox")).not.toBeInTheDocument();
    expect(screen.queryByRole("spinbutton")).not.toBeInTheDocument();
    expect(fetchMock.mock.calls.map(([url]) => url)).toEqual([
      "/api/v1/runs/run-1/approvals", `/api/v1/runs/run-1/approvals/${item.id}/preview`,
    ]);
    expect(fetchMock.mock.calls.every(([, init]) => init?.method === "GET" &&
      new Headers(init.headers).get("Authorization") === "Bearer test-read")).toBe(true);
    expect(decideApproval).not.toHaveBeenCalled();
  });

  it("leaves no placeholder after successfully reading an empty queue", async () => {
    const { container, fetchMock } = renderReadOnlyCards(null);
    expect(screen.getByText("正在读取待审批操作…")).toBeVisible();
    await waitFor(() => expect(container).toBeEmptyDOMElement());
    expect(fetchMock).toHaveBeenCalledTimes(1);
  });

  it("reports a failed queue read and retries with the read token", async () => {
    const { fetchMock, decideApproval, restoreReads } = renderReadOnlyCards(pending(), {}, "queue");
    expect(await screen.findByRole("alert")).toHaveTextContent("无法读取待审批操作");
    restoreReads();
    await userEvent.click(screen.getByRole("button", { name: "重试审批队列" }));
    expect(await screen.findByText("Read-only exact proposal [REDACTED]")).toBeVisible();
    expect(fetchMock.mock.calls.filter(([url]) => url.endsWith("/approvals"))).toHaveLength(2);
    expect(decideApproval).not.toHaveBeenCalled();
  });

  it("retains the queue and retries a failed exact preview read without submitting a decision", async () => {
    const { decideApproval, restoreReads } = renderReadOnlyCards(pending(), {}, "preview");
    expect(await screen.findByText("操作预览读取失败，请重新读取后确认批准范围。")).toBeVisible();
    expect(screen.getByText("arxiv.org")).toBeVisible();
    restoreReads();
    await userEvent.click(screen.getByRole("button", { name: "重试操作预览" }));
    expect(await screen.findByText("Read-only exact proposal [REDACTED]")).toBeVisible();
    expect(decideApproval).not.toHaveBeenCalled();
  });

  it.each([false, true])("shows a stale read-only preview with truncated=%s without offering a decision", async (truncated) => {
    const { decideApproval } = renderReadOnlyCards(pending(), { source_current: false, truncated });
    expect(await screen.findByText(truncated ? "预览超过显示上限，需读取完整内容后再批准。" : "操作已变化或审批已处理，请刷新最新状态。")).toBeVisible();
    expect(screen.getByRole("button", { name: "刷新操作预览" })).toBeEnabled();
    expect(screen.queryByRole("button", { name: /允许一次|本对话允许|拒绝/ })).not.toBeInTheDocument();
    expect(decideApproval).not.toHaveBeenCalled();
  });

  it("reviews each command with explicit bounded Run limits and preserves retry intent", async () => {
    const item = pending({tool_name:"command_runtime",action_class:"command_process",allowed_actions:["approve_once","approve_for_run","deny"],canonical_url:undefined,exact_target:undefined});
    const {decideApproval}=renderCards(item,{effect:"command_process",fields:[{name:"review_scope",value:"local verification"}]});
    const button=await screen.findByRole("button",{name:"确认本条命令并计入有界审批"});
    await userEvent.clear(screen.getByLabelText("有界审批命令次数"));
    await userEvent.type(screen.getByLabelText("有界审批命令次数"),"9");
    expect(button).toBeDisabled();
    await userEvent.clear(screen.getByLabelText("有界审批命令次数"));
    await userEvent.type(screen.getByLabelText("有界审批命令次数"),"3");
    await userEvent.click(button);
    await waitFor(()=>expect(decideApproval).toHaveBeenCalledTimes(1));
    expect(decideApproval.mock.calls[0]?.[2]).toEqual({version:"approval_control.v1",action:"approve_for_run",grant_ttl_seconds:120,grant_max_uses:3});
  });

  it("keeps the original limits for a matching active Run scope", async () => {
    const item=pending({tool_name:"command_runtime",action_class:"command_process",allowed_actions:["approve_once","approve_for_run","deny"],canonical_url:undefined,exact_target:undefined});
    const {decideApproval}=renderCards(item,{effect:"command_process",fields:[{name:"grant_ttl_seconds",value:"300"},{name:"grant_max_uses",value:"4"},{name:"grant_uses_remaining",value:"2"},{name:"grant_expires_at",value:"2026-10-03T16:00:00Z"}]});
    const button=await screen.findByRole("button",{name:"确认本条命令并计入有界审批"});
    await waitFor(()=>expect(screen.getByLabelText("有界审批有效秒数")).toHaveValue(300));
    expect(screen.getByLabelText("有界审批有效秒数")).toBeDisabled();
    expect(screen.getByLabelText("有界审批命令次数")).toHaveValue(4);
    expect(screen.getByLabelText("有界审批命令次数")).toBeDisabled();
    await userEvent.click(button);
    expect(decideApproval.mock.calls[0]?.[2]).toEqual({version:"approval_control.v1",action:"approve_for_run",grant_ttl_seconds:300,grant_max_uses:4});
  });
  it("navigates to the same file proposal from the keyboard without deciding approval", async () => {
    const item = pending({ tool_name: "create_file", action_class: "workspace_write", proposal_id: "exact-edit",
      workspace_id: "original-workspace", allowed_actions: ["deny"], canonical_url: undefined, exact_target: undefined });
    const { onReviewFile, decideApproval } = renderCards(item, { effect: "file_review_required", source_current: false,
      fields: [{ name: "path", value: "same.txt" }] });
    const button = await screen.findByRole("button", { name: "审阅文件提案" });
    button.focus();
    await userEvent.keyboard("{Enter}");
    expect(onReviewFile).toHaveBeenCalledWith({ runID: "run-1", editID: "exact-edit", workspaceID: "original-workspace" }, button);
    expect(screen.queryByRole("button", { name: "批准一次" })).not.toBeInTheDocument();
    expect(decideApproval).not.toHaveBeenCalled();
  });

  it.each([{ run_id: "different-run" }, { approval_id: "different-approval" }, { proposal_id: "different-edit" },
    { workspace_id: "different-workspace" }])("does not navigate from a mismatched file preview %j", async (mismatch) => {
    const item = pending({ tool_name: "create_file", action_class: "workspace_write", proposal_id: "exact-edit",
      workspace_id: "original-workspace", allowed_actions: [], canonical_url: undefined, exact_target: undefined });
    const { onReviewFile, decideApproval } = renderCards(item, { effect: "file_review_required", ...mismatch });
    await screen.findByText("操作预览读取失败，请重新读取后确认批准范围。");
    expect(screen.queryByRole("button", { name: "审阅文件提案" })).not.toBeInTheDocument();
    expect(onReviewFile).not.toHaveBeenCalled(); expect(decideApproval).not.toHaveBeenCalled();
  });

  it("approves a browser action through the strict client and accepts same-checkpoint continuation", async () => {
    const item = pending({ tool_name: "agent_browser_sensitive", action_class: "browser_external_write", mode: "per_call",
      allowed_actions: ["approve_once", "deny"], canonical_url: undefined, exact_target: undefined });
    const preview = { protocol_version: "approval_queue.v1", run_id: "run-1", approval_id: item.id,
      proposal_id: item.proposal_id, tool_name: item.tool_name, workspace_id: item.workspace_id,
      effect: "browser_sensitive_action", working_directory: "", fields: [{ name: "summary", value: "Publish this draft once" }],
      source_current: true, redacted: false, truncated: false };
    const decision = { version: "approval_control.v1", run_id: "run-1", approval_id: item.id, proposal_id: item.proposal_id,
      tool_name: item.tool_name, action: "approve_once", status: "approved", replayed: false,
      process_execution_enabled: false, shell_execution_enabled: false, docker_execution_enabled: false,
      workspace_write_applied: false, session_grant_created: false, capability_grant: false,
      execution_resumed: false, retry_completed: false, retry_scheduled: false,
      continuation: { state: "completed", replayed: false, model_called: true, tool_called: true } };
    let decided = false;
    const fetchMock = vi.fn(async (url: string, init?: RequestInit) => {
      const path = String(url);
      if (init?.method === "POST") decided = true;
      const queue = { protocol_version: "approval_queue.v1", run_id: "run-1", items: decided ? [] : [item],
        truncated: false, process_execution_enabled: false, session_grant_created: false, capability_grant: false };
      return new Response(JSON.stringify({ version: "api.v1", request_id: "browser-decision", data:
        path.endsWith("/decision") ? decision : path.endsWith("/preview") ? preview : queue }),
      { status: path.endsWith("/decision") ? 202 : 200, headers: { "Content-Type": "application/json" } });
    });
    vi.stubGlobal("fetch", fetchMock);
    const client = new APIClient("test-read", "/api/v1", "test-control");
    render(<QueryClientProvider client={new QueryClient({ defaultOptions: { queries: { retry: false }, mutations: { retry: false } } })}>
      <V2ApprovalCards client={client} runID="run-1" threadID="thread-1" />
    </QueryClientProvider>);
    expect(await screen.findByText("Publish this draft once")).toBeInTheDocument();
    await userEvent.click(screen.getByRole("button", { name: "批准一次" }));
    expect(await screen.findByText(/Agent 已继续处理/)).toBeInTheDocument();
    expect(fetchMock.mock.calls.filter(([, init]) => init?.method === "POST")).toHaveLength(1);
  });
  it("renders a real create-file preview through the strict client and keeps approval in diff review", async () => {
    const item = pending({ id: "approval-20260910072057-6583f54ed917",
      proposal_id: "edit-99e4b9e0fc83e37efdda6745e8835851", workspace_id: "workspace-1",
      tool_name: "create_file", action_class: "workspace_write", allowed_actions: [],
      canonical_url: undefined, exact_target: undefined });
    const preview = { protocol_version: "approval_queue.v1", run_id: "run-1", approval_id: item.id,
      proposal_id: item.proposal_id, tool_name: "create_file", workspace_id: item.workspace_id,
      effect: "file_review_required", working_directory: ".", fields: [
        { name: "operation", value: "create" }, { name: "path", value: "package.json" }],
      source_current: true, redacted: false, truncated: false };
    const queue = { protocol_version: "approval_queue.v1", run_id: "run-1", items: [item],
      truncated: false, process_execution_enabled: false, session_grant_created: false, capability_grant: false };
    const fetchMock = vi.fn((url: string) => Promise.resolve(new Response(JSON.stringify({ version: "api.v1",
      request_id: "preview-create", data: String(url).endsWith("/preview") ? preview : queue }),
      { status: 200, headers: { "Content-Type": "application/json" } })));
    vi.stubGlobal("fetch", fetchMock);
    const client = new APIClient("test-read", "/api/v1", "test-control");
    render(<QueryClientProvider client={new QueryClient({ defaultOptions: { queries: { retry: false } } })}>
      <V2ApprovalCards client={client} runID="run-1" threadID="thread-1" />
    </QueryClientProvider>);
    expect(await screen.findByText("package.json")).toBeInTheDocument();
    expect(screen.getByText(/批准、差异审阅和写入请使用任务的「审阅改动」入口/)).toBeInTheDocument();
    expect(screen.queryByText("操作预览读取失败，请重新读取后确认批准范围。")).not.toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "批准一次" })).not.toBeInTheDocument();
    expect(fetchMock.mock.calls.every(([url]) => String(url).includes("/runs/run-1/approvals"))).toBe(true);
  });

  it("offers one-time, conversation, and deny choices for a new public HTTPS host", async () => {
    const user = userEvent.setup();
    const { decideApproval } = renderCards(pending());

    expect(await screen.findByText("允许读取这个网站？")).toBeInTheDocument();
    expect(screen.getByText("arxiv.org")).toBeInTheDocument();
    expect(await screen.findByText("https://arxiv.org/abs/2608.13637")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "允许一次" })).toBeEnabled();
    expect(screen.getByRole("button", { name: "拒绝" })).toBeEnabled();

    await user.click(screen.getByRole("button", { name: "本对话允许" }));
    await waitFor(() => expect(decideApproval).toHaveBeenCalledWith(
      "run-1", "approval-web-fetch-1", {
        version: "approval_control.v1", action: "approve_for_thread",
      }, expect.stringMatching(/^v2-approval-/u),
    ));
  });

  it("does not offer a conversation-wide grant for ordinary approvals", async () => {
    renderCards(pending({
      action_class: "shell", allowed_actions: ["approve_once", "deny"],
      canonical_url: undefined, exact_target: undefined, tool_name: "shell",
    }));

    expect(await screen.findByText("批准这次模拟执行？")).toBeInTheDocument();
    expect(screen.getByText("echo inspection-only")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "批准模拟一次" })).toBeEnabled();
    expect(screen.queryByRole("button", { name: "本对话允许" })).not.toBeInTheDocument();
  });

  it("blocks stale previews while keeping denial available", async () => {
    const { decideApproval } = renderCards(pending(), { source_current: false });
    expect(await screen.findByText("操作已变化或审批已处理，请刷新最新状态。")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "允许一次" })).toBeDisabled();
    expect(screen.getByRole("button", { name: "本对话允许" })).toBeDisabled();
    await userEvent.setup().click(screen.getByRole("button", { name: "拒绝" }));
    await waitFor(() => expect(decideApproval).toHaveBeenCalledWith("run-1", pending().id,
      { version: "approval_control.v1", action: "deny" }, expect.any(String)));
  });

  it("requires a matching proposal preview and offers retry after a loading error", async () => {
    const { approvalPreview } = renderCards(pending(), { proposal_id: "another-proposal" });
    expect(await screen.findByText("操作预览读取失败，请重新读取后确认批准范围。")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "允许一次" })).toBeDisabled();
    await userEvent.setup().click(screen.getByRole("button", { name: "重试操作预览" }));
    await waitFor(() => expect(approvalPreview).toHaveBeenCalledTimes(2));
  });

  it("replays a persisted web decision without offering a different choice", async () => {
    const user = userEvent.setup();
    const recovered = { ...pending(), allowed_actions: ["approve_for_thread"],
      status: "approved" } as unknown as ApprovalQueueItemView;
    const { decideApproval } = renderCards(recovered);

    expect(await screen.findByText("恢复上次网页读取")).toBeInTheDocument();
    expect(screen.getByText("已允许，等待恢复")).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "拒绝" })).not.toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "允许一次" })).not.toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: "继续恢复" }));
    await waitFor(() => expect(decideApproval).toHaveBeenCalledWith(
      "run-1", "approval-web-fetch-1", {
        version: "approval_control.v1", action: "approve_for_thread",
      }, expect.stringMatching(/^v2-approval-/u),
    ));
  });
});

function render(ui: ReactElement, options?: RenderOptions) {
 window.localStorage.setItem("prayu.locale.v1", "zh-CN");
 const Wrapper=options?.wrapper;
 return renderComponent(ui, {...options, wrapper: ({children}) => <LocaleProvider>{Wrapper ? <Wrapper>{children}</Wrapper> : children}</LocaleProvider>});
}
