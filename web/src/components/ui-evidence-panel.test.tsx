import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { APIRequestError, type APIClient } from "../api/client";
import type { UIEvidenceArtifactMetadata, UIEvidenceAttempt, UIEvidenceStartView } from "../api/types";
import { UIEvidencePanel } from "./ui-evidence-panel";
import { V2RecoveryProvider } from "../v2/recovery-storage";
import { uiEvidenceIntentKey } from "./ui-evidence-recovery";

vi.mock("../lib/locale", () => ({
  useLocale: () => ({ t: (chinese: string) => chinese }),
}));

function attempt(status: "not_run" | "passed", id: string): UIEvidenceAttempt {
  const started = status === "passed" ? { started_at: "2026-08-20T00:00:01Z",
    completed_at: "2026-08-20T00:00:02Z" } : {};
  return {
    protocol_version: "ui-evidence-attempt.v1",
    manifest: {
      protocol_version: "ui-evidence.v1", attempt_id: id, run_id: "run-1",
      mission_id: "mission-1", session_id: "session-1", workspace_id: "workspace-1",
      source: { repository_kind: "git", commit: "a".repeat(40), branch: "codex/test",
        dirty: false, dirty_digest: "b".repeat(64), root_fingerprint: "c".repeat(64),
        index_sha256: "d".repeat(64), manifest_sha256: "e".repeat(64) },
      start: { protocol_version: "command-runtime.v2", profile: "powershell",
        executable_name: "powershell.exe", executable_path_sha256: "1".repeat(64),
        executable_sha256: "2".repeat(64), canonical_argv: ["powershell.exe", "-Command", "npm run dev"],
        working_directory: "web", environment_names: [], environment_sha256: "3".repeat(64),
        timeout_milliseconds: 60_000, network: "disabled", credentials: "none",
        purpose: "UI evidence", fingerprint: "4".repeat(64) },
      readiness: { url: "http://127.0.0.1:4173/", method: "GET",
        expected_status: [200], timeout_milliseconds: 60_000, interval_milliseconds: 250 },
      browser: { product: "edge", version: "140.0.0.0", executable_sha256: "5".repeat(64),
        driver_protocol: "restricted-cdp-ui-evidence.v1", headless: true,
        temporary_profile: true },
      url: "http://127.0.0.1:5178/demo", route: "/demo", environment: {
        viewport: { width: 1440, height: 900, dpr: 1 }, locale: "en-US",
        theme: "light", reduced_motion: false },
      fixture: { name: "fixture", seed: "seed", page_state: "{}",
        data_sha256: "6".repeat(64), deterministic: true, synthetic: true },
      steps: [{ id: "navigate", kind: "navigate", capture_after: true }],
      capture: { screenshot: true, dom: true, accessibility: true, console: true,
        network: true, performance: true, video: false, mask_selectors: [] },
      failure_policy: { fail_on_console_error: true, fail_on_page_error: true,
        fail_on_request_error: true, fail_on_http_status: true },
      authority: { process_start: false, network_access: false, credential_access: false,
        personal_profile: false, request_mutation: false, verification_pass: false },
      created_at: "2026-08-20T00:00:00Z", fingerprint: "7".repeat(64),
    },
    operation_digest: "8".repeat(64), request_fingerprint: "7".repeat(64), status,
    failure_stage: "none", diagnostics: { console_warnings: 0, console_errors: 0,
      page_errors: 0, failed_requests: 0, http_failures: 0, allowed_requests: 1,
      blocked_requests: 0 }, cleanup: { browser_tree_reaped: status === "passed",
      application_tree_reaped: status === "passed", profile_removed: status === "passed",
      network_released: status === "passed", port_released: status === "passed" },
    artifact_count: status === "passed" ? 6 : 0,
    artifact_bytes: status === "passed" ? 1_024 : 0,
    version: status === "passed" ? 3 : 1,
    created_at: "2026-08-20T00:00:00Z", updated_at: "2026-08-20T00:00:02Z", ...started,
  } as UIEvidenceAttempt;
}

function panel(client: APIClient, scopeID: string | undefined = "ui-evidence-test-store", runID = "run-1", threadID?: string) {
  return <QueryClientProvider client={new QueryClient({ defaultOptions: {
    mutations: { retry: false }, queries: { retry: false },
  } })}><V2RecoveryProvider client={{ baseURL: client.baseURL || "/api/v1" }} scopeID={scopeID}>
    <UIEvidencePanel client={client} runID={runID} threadID={threadID} />
  </V2RecoveryProvider></QueryClientProvider>;
}
function renderPanel(client: APIClient) { return render(panel(client)); }
beforeEach(() => localStorage.clear());
afterEach(() => vi.restoreAllMocks());

describe("UIEvidencePanel", () => {
  it("previews only a verified screenshot download and releases its object URL", async () => {
    const passed = attempt("passed", "attempt-image");
    const artifact = { id: "artifact-image", attempt_id: "attempt-image", kind: "screenshot", mime: "image/png", bytes: 10,
      sha256: "a".repeat(64), step_id: "navigate", created_at: "2026-08-20T00:00:02Z", retention_policy: "run_history", redacted: false } as UIEvidenceArtifactMetadata;
    const downloadUIEvidenceArtifact = vi.fn().mockRejectedValueOnce(new Error("Artifact content hash mismatch")).mockResolvedValueOnce(new Blob(["png"], { type: "image/png" }));
    const createObjectURL = vi.fn().mockReturnValue("blob:verified-image");
    const revokeObjectURL = vi.fn(); const NativeURL = URL;
    vi.stubGlobal("URL", class extends NativeURL { static createObjectURL = createObjectURL; static revokeObjectURL = revokeObjectURL; });
    try {
      const view = renderPanel({ hasUIEvidence: false, uiEvidence: vi.fn().mockResolvedValue([passed]),
        uiEvidenceBundle: vi.fn().mockResolvedValue({ attempt: passed, artifacts: [artifact], steps: [] }), downloadUIEvidenceArtifact } as unknown as APIClient);
      const user = userEvent.setup(); await user.click(await screen.findByRole("button", { name: "查看截图" }));
      expect(await screen.findByRole("alert")).toHaveTextContent("Artifact content hash mismatch");
      expect(createObjectURL).not.toHaveBeenCalled(); expect(screen.queryByRole("img")).not.toBeInTheDocument();
      await user.click(screen.getByRole("button", { name: "查看截图" }));
      expect(await screen.findByRole("img", { name: "验证截图 artifact-image" })).toHaveAttribute("src", "blob:verified-image");
      expect(downloadUIEvidenceArtifact).toHaveBeenLastCalledWith("attempt-image", artifact);
      view.unmount(); expect(revokeObjectURL).toHaveBeenCalledWith("blob:verified-image");
    } finally { vi.unstubAllGlobals(); }
  });

  async function prepare(user: ReturnType<typeof userEvent.setup>) {
    await user.click(screen.getByText("准备并启动浏览器验证"));
    await user.type(screen.getByLabelText("应用名称"), "测试应用");
    await user.type(screen.getByLabelText("启动命令（PowerShell）"), "node preview-server.js --port 5011");
    await user.type(screen.getByLabelText("本次页面地址"), "http://127.0.0.1:5011/");
    await user.click(screen.getByRole("button", { name: "预览验证清单" }));
    await user.click(screen.getByRole("checkbox"));
  }

  it("retains the exact request after a lost response and remount, and retries only its original key", async () => {
    const created = attempt("not_run", "attempt-recovered");
    const startUIEvidence = vi.fn().mockRejectedValueOnce(new Error("response lost")).mockResolvedValueOnce(created);
    const client = { hasUIEvidence: true, startUIEvidence, uiEvidence: vi.fn().mockResolvedValue([]),
      uiEvidenceBundle: vi.fn().mockResolvedValue({ attempt: created, steps: [], artifacts: [] }) } as unknown as APIClient;
    const content = () => panel(client);
    const view = render(content()); const user = userEvent.setup(); await prepare(user);
    await user.click(screen.getByRole("button", { name: "启动真实浏览器验证" }));
    await screen.findByText(/启动结果待确认/);
    const original = startUIEvidence.mock.calls[0]![1]; view.unmount(); render(content());
    await user.click(screen.getByRole("button", { name: "核对原请求" }));
    await waitFor(() => expect(startUIEvidence).toHaveBeenCalledTimes(2));
    expect(startUIEvidence.mock.calls[1]).toEqual(["run-1", original]);
    await waitFor(() => expect(screen.queryByText(/启动结果待确认/)).not.toBeInTheDocument());
    expect(localStorage.length).toBe(0);
    expect(client.uiEvidenceBundle).toHaveBeenCalledWith("attempt-recovered", expect.any(AbortSignal));
  });

  it("restores a draft with a new provider and query cache, isolates store and Run, and requires fresh review", async () => {
    const startUIEvidence = vi.fn();
    const client = { hasUIEvidence: true, startUIEvidence, uiEvidence: vi.fn().mockResolvedValue([]) } as unknown as APIClient;
    const user = userEvent.setup(); const first = render(panel(client)); await prepare(user);
    const manifest = screen.getByLabelText("本次精确验证清单").textContent;
    first.unmount();
    const otherStore = render(panel(client, "another-store"));
    await user.click(screen.getByText("准备并启动浏览器验证")); expect(screen.getByLabelText("应用名称")).toHaveValue("");
    otherStore.unmount();
    const otherRun = render(panel(client, "ui-evidence-test-store", "run-2"));
    await user.click(screen.getByText("准备并启动浏览器验证")); expect(screen.getByLabelText("应用名称")).toHaveValue("");
    otherRun.unmount(); render(panel(client)); await user.click(screen.getByText("准备并启动浏览器验证"));
    expect(screen.getByLabelText("应用名称")).toHaveValue("测试应用");
    expect(screen.getByLabelText("启动命令（PowerShell）")).toHaveValue("node preview-server.js --port 5011");
    expect(screen.getByLabelText("本次精确验证清单").textContent).toBe(manifest);
    expect(screen.getByRole("checkbox")).not.toBeChecked();
    expect(screen.getByRole("button", { name: "启动真实浏览器验证" })).toBeDisabled();
    expect(startUIEvidence).not.toHaveBeenCalled();
  });

  it("restores an interrupted pending start as unknown and never executes it until the original request is checked", async () => {
    const created = attempt("not_run", "attempt-pending-recovered");
    const startUIEvidence = vi.fn().mockImplementationOnce(() => new Promise(() => {})).mockResolvedValueOnce(created);
    const client = { hasUIEvidence: true, startUIEvidence, uiEvidence: vi.fn().mockResolvedValue([]),
      uiEvidenceBundle: vi.fn().mockResolvedValue({ attempt: created, steps: [], artifacts: [] }) } as unknown as APIClient;
    const user = userEvent.setup(); const first = render(panel(client)); await prepare(user);
    await user.click(screen.getByRole("button", { name: "启动真实浏览器验证" }));
    await waitFor(() => expect(startUIEvidence).toHaveBeenCalledOnce()); const original = startUIEvidence.mock.calls[0];
    const savedKey = Object.keys(localStorage).find((key) => key.endsWith(encodeURIComponent(uiEvidenceIntentKey("run-1"))))!;
    const saved = JSON.parse(localStorage.getItem(savedKey)!); saved.value.state = "pending";
    localStorage.setItem(savedKey, JSON.stringify(saved)); first.unmount();
    const other = render(panel(client, "another-store"));
    expect(screen.queryByRole("button", { name: "核对原请求" })).not.toBeInTheDocument(); expect(startUIEvidence).toHaveBeenCalledOnce(); other.unmount();
    render(panel(client)); expect(screen.getByText(/启动结果待确认/)).toBeInTheDocument(); expect(startUIEvidence).toHaveBeenCalledOnce();
    await user.click(screen.getByRole("button", { name: "核对原请求" }));
    await waitFor(() => expect(startUIEvidence).toHaveBeenCalledTimes(2)); expect(startUIEvidence.mock.calls[1]).toEqual(original);
    await waitFor(() => expect(localStorage.length).toBe(0));
  });

  it("does not execute when persisting the exact start request fails", async () => {
    const startUIEvidence = vi.fn(); const client = { hasUIEvidence: true, startUIEvidence,
      uiEvidence: vi.fn().mockResolvedValue([]) } as unknown as APIClient;
    const user = userEvent.setup(); renderPanel(client); await prepare(user);
    const write = Storage.prototype.setItem;
    vi.spyOn(Storage.prototype, "setItem").mockImplementation(function (this: Storage, key, value) {
      if (key.endsWith(encodeURIComponent(uiEvidenceIntentKey("run-1")))) throw new Error("quota");
      write.call(this, key, value);
    });
    await user.click(screen.getByRole("button", { name: "启动真实浏览器验证" }));
    expect(startUIEvidence).not.toHaveBeenCalled(); expect(await screen.findByRole("alert")).toHaveTextContent("无法保存本机恢复数据");
  });

  it("retains a mismatched recovered Run record and prevents a replacement operation", async () => {
    const startUIEvidence = vi.fn().mockRejectedValue(new Error("response lost"));
    const client = { hasUIEvidence: true, startUIEvidence, uiEvidence: vi.fn().mockResolvedValue([]) } as unknown as APIClient;
    const user = userEvent.setup(); const first = renderPanel(client); await prepare(user);
    await user.click(screen.getByRole("button", { name: "启动真实浏览器验证" })); await screen.findByRole("alert"); first.unmount();
    const key = Object.keys(localStorage).find((entry) => entry.endsWith(encodeURIComponent(uiEvidenceIntentKey("run-1"))))!;
    const stored = JSON.parse(localStorage.getItem(key)!); stored.value.runID = "another-run";
    const corrupt = JSON.stringify(stored); localStorage.setItem(key, corrupt);
    renderPanel(client); await user.click(screen.getByText("准备并启动浏览器验证")); await user.click(screen.getByRole("checkbox"));
    await user.click(screen.getByRole("button", { name: "启动真实浏览器验证" }));
    expect(startUIEvidence).toHaveBeenCalledOnce(); expect(localStorage.getItem(key)).toBe(corrupt);
    expect(screen.getAllByText(/恢复记录格式异常/).length).toBeGreaterThan(0);
  });

  it("shows unavailable persistence without a data-store provider and keeps starting disabled", async () => {
    const startUIEvidence = vi.fn(); const client = { hasUIEvidence: true, startUIEvidence,
      uiEvidence: vi.fn().mockResolvedValue([]) } as unknown as APIClient;
    render(<QueryClientProvider client={new QueryClient()}><UIEvidencePanel client={client} runID="run-1" /></QueryClientProvider>);
    expect(screen.getAllByText(/无法持久保存验证草稿或原请求/).length).toBeGreaterThan(0);
    const user = userEvent.setup(); await prepare(user);
    expect(screen.getByRole("button", { name: "启动真实浏览器验证" })).toBeDisabled(); expect(startUIEvidence).not.toHaveBeenCalled();
  });

  it("invalidates review when launch settings change and rejects remote addresses before mutation", async () => {
    const startUIEvidence = vi.fn(); const user = userEvent.setup();
    renderPanel({ hasUIEvidence: true, startUIEvidence, uiEvidence: vi.fn().mockResolvedValue([]) } as unknown as APIClient);
    await prepare(user); expect(screen.getByRole("button", { name: "启动真实浏览器验证" })).toBeEnabled();
    await user.type(screen.getByLabelText("启动命令（PowerShell）"), " --test");
    expect(screen.getByRole("button", { name: "启动真实浏览器验证" })).toBeDisabled();
    await user.clear(screen.getByLabelText("本次页面地址")); await user.type(screen.getByLabelText("本次页面地址"), "https://example.org/");
    await user.click(screen.getByRole("button", { name: "预览验证清单" }));
    expect(await screen.findByRole("alert")).toHaveTextContent("本机 HTTP 页面地址");
    expect(startUIEvidence).not.toHaveBeenCalled();
  });

  it("limits launch records to the selected execution and binds the service, source call and URL", async () => {
    const source = { job_id: "job-selected", run_id: "run-1", thread_id: "thread-1", source_call_id: "call-selected", state: "running", can_stop: true };
    const listThreadApplicationServices = vi.fn().mockResolvedValue({ services: [source, { ...source, job_id: "job-other", run_id: "run-other" }] });
    const getThreadApplicationService = vi.fn().mockResolvedValue({ service: source, candidate_urls: [{ url: "http://127.0.0.1:5011/" }] });
    const threadActivityDetail = vi.fn().mockResolvedValue({ activity_ref: "call-selected", run_id: "run-1", tools: [{ name: "command_runtime", detail: { kind: "command", command: { commands: [{ command: "node preview.js --port 5011", working_directory: "app" }] } } }] });
    const startUIEvidence = vi.fn().mockResolvedValue(attempt("not_run", "attempt-service"));
    const client = { hasUIEvidence: true, listThreadApplicationServices, getThreadApplicationService, threadActivityDetail, startUIEvidence,
      uiEvidence: vi.fn().mockResolvedValue([]), uiEvidenceBundle: vi.fn().mockResolvedValue({ attempt: attempt("not_run", "attempt-service"), artifacts: [], steps: [] }) } as unknown as APIClient;
    render(panel(client, "ui-evidence-test-store", "run-1", "thread-1"));
    const user = userEvent.setup(); await user.click(screen.getByText("准备并启动浏览器验证"));
    await user.selectOptions(await screen.findByLabelText("应用服务"), "job-selected");
    expect(screen.queryByRole("option", { name: /job-other/ })).not.toBeInTheDocument();
    await waitFor(() => expect(screen.getByRole("button", { name: "使用所选启动记录" })).toBeEnabled());
    await user.click(screen.getByRole("button", { name: "使用所选启动记录" }));
    expect(screen.getByLabelText("启动命令（PowerShell）")).toHaveValue("node preview.js --port 5011");
    expect(screen.getByLabelText("项目内工作目录")).toHaveValue("app");
    await user.click(screen.getByRole("button", { name: "预览验证清单" })); await user.click(screen.getByRole("checkbox"));
    await user.click(screen.getByRole("button", { name: "启动真实浏览器验证" }));
    await waitFor(() => expect(startUIEvidence).toHaveBeenCalledOnce());
    expect(startUIEvidence.mock.calls[0]![1] as UIEvidenceStartView).toMatchObject({ start: { script: "node preview.js --port 5011", working_directory: "app" }, url: "http://127.0.0.1:5011/" });
    expect(threadActivityDetail).toHaveBeenCalledWith("thread-1", "call-selected", expect.any(AbortSignal));
    expect(getThreadApplicationService).toHaveBeenCalledWith("thread-1", "job-selected", expect.any(AbortSignal));
  });
  it("keeps not_run neutral and reserves the success treatment for passed", async () => {
    const notRun = attempt("not_run", "attempt-not-run");
    const passed = attempt("passed", "attempt-passed");
    const client = { hasUIEvidence: false, uiEvidenceUnavailableReason: "ui_evidence_disabled",
      uiEvidence: vi.fn().mockResolvedValue([notRun, passed]),
      uiEvidenceBundle: vi.fn().mockResolvedValue({ attempt: notRun, steps: [], artifacts: [] }),
    } as unknown as APIClient;

    renderPanel(client);

    const notRunBadges = await screen.findAllByText("未运行");
    expect(notRunBadges.length).toBeGreaterThan(0);
    for (const badge of notRunBadges) {
      expect(badge).toHaveClass("status-not-run");
      expect(badge).not.toHaveClass("status-passed");
    }
    expect(screen.getByText("通过")).toHaveClass("status-passed");
    expect(screen.getByText(/截图和下载内容供你核对页面表现/)).toBeInTheDocument();
    expect(client.uiEvidence).toHaveBeenCalledWith("run-1", expect.any(AbortSignal));
    expect(client.uiEvidenceBundle).toHaveBeenCalledWith("attempt-not-run", expect.any(AbortSignal));
    expect(screen.queryByText(/历史证据状态待确认/)).not.toBeInTheDocument();
  });

  it("explains an unavailable backend history endpoint without treating 404 as an empty history", async () => {
    const uiEvidence = vi.fn().mockRejectedValue(new APIRequestError(
      "HTTP API endpoint was not found", "NOT_FOUND", 404));
    const client = { hasUIEvidence: false, uiEvidenceUnavailableReason: "ui_evidence_disabled",
      uiEvidence } as unknown as APIClient;
    const user = userEvent.setup();
    renderPanel(client);
    expect(await screen.findByText(/历史证据状态待确认/)).toHaveTextContent("历史证据状态待确认");
    expect(screen.getByText(/历史证据状态待确认/)).toHaveTextContent("Windows Desktop");
    expect(screen.queryByText("HTTP API endpoint was not found")).not.toBeInTheDocument();
    expect(screen.queryByText("还没有浏览器验证。展开下方启动表单，填写应用启动方式并核对步骤后开始。")).not.toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: "刷新 UI 证据" }));
    await waitFor(() => expect(uiEvidence).toHaveBeenCalledTimes(2));
    expect(uiEvidence).toHaveBeenCalledWith("run-1", expect.any(AbortSignal));
  });

  it.each([
    [false, new APIRequestError("UI evidence service failed", "INTERNAL", 500)],
    [false, new APIRequestError("UI evidence authentication failed", "UNAUTHORIZED", 401)],
    [false, new APIRequestError("Unexpected error code", "OTHER_ERROR", 404)],
    [true, new APIRequestError("Enabled endpoint missing", "NOT_FOUND", 404)],
  ])("keeps other history errors visible with hasUIEvidence=%s", async (enabled, error) => {
    renderPanel({ hasUIEvidence: enabled, uiEvidenceUnavailableReason: enabled ? null : "ui_evidence_disabled",
      uiEvidence: vi.fn().mockRejectedValue(error) } as unknown as APIClient);
    expect(await screen.findByRole("alert")).toHaveTextContent(error.message);
    expect(screen.queryByText(/历史证据状态待确认/)).not.toBeInTheDocument();
    expect(screen.queryByText("还没有浏览器验证。展开下方启动表单，填写应用启动方式并核对步骤后开始。")).not.toBeInTheDocument();
  });

  it("does not keep declaring empty history when a later refresh cannot read the endpoint", async () => {
    const uiEvidence = vi.fn().mockResolvedValueOnce([]).mockRejectedValueOnce(new APIRequestError(
      "HTTP API endpoint was not found", "NOT_FOUND", 404));
    const client = { hasUIEvidence: false, uiEvidenceUnavailableReason: "ui_evidence_disabled",
      uiEvidence } as unknown as APIClient;
    const user = userEvent.setup();
    renderPanel(client);
    await screen.findByText("还没有浏览器验证。请在上方验证设置中保存启用偏好，重启后连接当前服务。");
    await user.click(screen.getByRole("button", { name: "刷新 UI 证据" }));
    await screen.findByText(/历史证据状态待确认/);
    expect(screen.queryByText(/还没有浏览器验证/)).not.toBeInTheDocument();
  });

  it("requires exact-manifest review before starting", async () => {
    const created = attempt("not_run", "attempt-created");
    const startUIEvidence = vi.fn().mockResolvedValue(created);
    const client = { hasUIEvidence: true, uiEvidenceUnavailableReason: null, startUIEvidence,
      uiEvidence: vi.fn().mockResolvedValue([]),
      uiEvidenceBundle: vi.fn().mockResolvedValue({ attempt: created, steps: [], artifacts: [] }),
    } as unknown as APIClient;
    const user = userEvent.setup();
    renderPanel(client);

    await screen.findByText("还没有浏览器验证。展开下方启动表单，填写应用启动方式并核对步骤后开始。");
    await user.click(screen.getByText("准备并启动浏览器验证"));
    await user.type(screen.getByLabelText("应用名称"), "项目预览");
    await user.type(screen.getByLabelText("启动命令（PowerShell）"), "npm run preview -- --port 5178");
    await user.type(screen.getByLabelText("本次页面地址"), "http://127.0.0.1:5178/demo");
    await user.click(screen.getByRole("button", { name: "添加检查步骤" }));
    await user.type(screen.getByLabelText("页面选择器"), "main");
    await user.click(screen.getByRole("button", { name: "预览验证清单" }));
    const startButton = screen.getByRole("button", { name: "启动真实浏览器验证" });
    expect(startButton).toBeDisabled();
    await user.click(screen.getByRole("checkbox"));
    expect(startButton).toBeEnabled();
    await user.click(startButton);

    await waitFor(() => expect(startUIEvidence).toHaveBeenCalledTimes(1));
    expect(startUIEvidence).toHaveBeenCalledWith("run-1", expect.objectContaining({
      url: "http://127.0.0.1:5178/demo", route: "/demo",
      fixture: expect.objectContaining({ deterministic: true, synthetic: true }),
      failure_policy: { fail_on_console_error: true, fail_on_page_error: true,
        fail_on_request_error: true, fail_on_http_status: true },
    }));
  });

  it.each([
    ["missing_control_credential", "先刷新列表以查看历史证据"],
    ["ui_evidence_disabled", "在下方验证设置中启用浏览器验证"],
    ["run_execution_disabled", "恢复浏览器验证所需的任务执行能力"],
    ["browser_cdp_control_disabled", "重新连接当前控制服务"],
    [undefined, "先刷新列表以查看历史记录"],
  ])("explains unavailable UI evidence accurately for %s and keeps launch disabled", async (reason, message) => {
    const startUIEvidence = vi.fn();
    const client = { hasUIEvidence: false, uiEvidenceUnavailableReason: reason,
      uiEvidence: vi.fn().mockResolvedValue([]), startUIEvidence,
    } as unknown as APIClient;
    const user = userEvent.setup();
    const { container } = renderPanel(client);
    expect(screen.getByText(new RegExp(message, "u"))).toBeInTheDocument();
    expect(screen.getByText(/仅在手工启动 Windows Desktop 时/)).toBeInTheDocument();
    expect(container.textContent).toContain("--enable-ui-evidence");
    expect(container.textContent).toContain("--enable-run-execution");
    expect(container.textContent).toContain("--enable-browser-cdp-control");
    expect(container.textContent).not.toContain("当前连接为只读");
    await screen.findByText("还没有浏览器验证。请在上方验证设置中保存启用偏好，重启后连接当前服务。");
    expect(screen.queryByText("还没有浏览器验证。展开下方启动表单，填写应用启动方式并核对步骤后开始。")).not.toBeInTheDocument();
    await user.click(screen.getByText("高级：启动参数与独立 CLI"));
    expect(screen.getByText(/仅在手工启动 Windows Desktop 时/)).toBeVisible();
    await user.click(screen.getByText("准备并启动浏览器验证"));
    expect(screen.getByRole("button", { name: "预览验证清单" })).toBeDisabled();
    expect(screen.getByRole("button", { name: "启动真实浏览器验证" })).toBeDisabled();
    expect(startUIEvidence).not.toHaveBeenCalled();
  });
});
