import type { ReactElement } from "react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { cleanup, render as renderComponent, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, describe, expect, it, vi } from "vitest";
import type { APIClient } from "../../api/client";
import type { ThreadActivityDetailView } from "../../api/types";
import { projectThreadNarrative, type NarrativeEntry,
  type ThreadTranscriptActivityItem } from "../projection/narrative";
import { projectThreadActivityDetail, ThreadActivityToolDetailPanel, V2ActivityGroup } from "./activity-detail";
import { LocaleProvider } from "../../lib/locale";

type ActivityEntry = Extract<NarrativeEntry, { kind: "activity" }>;

afterEach(() => cleanup());

function activity(overrides: Partial<ActivityEntry> = {}): ActivityEntry {
  return {
    id: "activity-1",
    kind: "activity",
    activity: "execute",
    title: "运行命令",
    detail: "pnpm test session",
    status: "completed",
    createdAt: "2026-09-02T00:00:00Z",
    runId: "run-1",
    count: 1,
    provisional: false,
    items: [{
      title: "pnpm test session",
      detail: "测试已完成",
      status: "completed",
      provisional: false,
      detailRef: "command-1",
      detailAvailable: true,
    }],
    ...overrides,
  };
}

function renderActivity(client: APIClient, entry = activity()) {
  const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return render(<QueryClientProvider client={queryClient}>
    <V2ActivityGroup client={client} entry={entry} threadID="thread-1" />
  </QueryClientProvider>);
}

const commandDetail = (commands: Array<Record<string, unknown>>) => ({
  kind: "command" as const,
  command: { commands },
});

const boundary = (untrusted = false) => ({ authorization: "policy_checked" as const,
  error_code: "", failure_reason: "", truncated: false, untrusted });

function fileEditActivityDetail(diffAvailable = true) {
  return { kind: "file_edit" as const, file_edit: { operation: "inspect", action: "replace",
    path: "safe-target", destination_path: "", apply_status: "applied", applied: true,
    file_written: true, replayed: false, edit_id: diffAvailable ? "edit-1" : "",
    diff_available: diffAvailable,
    diff: { added_lines: 2, removed_lines: 1, hunks: 1, summary: "修改 safe-target · +2 −1" },
    boundary: boundary() } };
}

function typedDetail(kind: "web_search" | "web_fetch" | "file_read" | "file_edit" |
  "verification" | "mcp" | "browser") {
  switch (kind) {
    case "web_search": return { kind, web_search: { operation: "inspect", query: "safe-target",
      limit: 2, provider: "provider-native", search_policy: "provider_native",
      selection_reason: "configured route", source_count: 1, citeable: true,
      sources: [{ rank: 1, title: "Safe source", url: "https://example.com/reference",
        provider: "provider-native", state: "fetched", citeable: true }], boundary: boundary(true) } };
    case "web_fetch": return { kind, web_fetch: { operation: "inspect",
      url: "https://example.com/reference", state: "fetched", http_status: 200,
      robots: "allowed", robots_policy: "audit_only", redirects: 0, partial: false,
      citeable: true, boundary: boundary(true) } };
    case "file_read": return { kind, file_read: { operation: "inspect", path: "safe-target",
      query: "", pattern: "", start_line: 1, end_line: 2, limit: 0, result_count: 2,
      truncated: false, summary: "返回 2 项结果", boundary: boundary() } };
    case "file_edit": return fileEditActivityDetail();
    case "verification": return { kind, verification: { operation: "inspect",
      tool: "code_diagnostics", path: "safe-target", query: "", position: "",
      direction: "", limit: 10, result_count: 0, truncated: false, summary: "验证已完成",
      boundary: boundary() } };
    case "mcp": return { kind, mcp: { operation: "inspect", server: "safe-target",
      tool: "lookup", arguments: [{ name: "scope", type: "string", summary: "workspace" }],
      result: { type: "object", count: 1, summary: "对象 · 1 个字段（值已隐藏）",
        fields: [{ name: "status", type: "string", summary: "completed" }] },
      boundary: boundary(true) } };
    case "browser": return { kind, browser: { operation: "inspect", action: "browser_snapshot",
      url: "", selector: "#safe-target", input_length: 0, artifact_bytes: 0,
      summary: "浏览器操作已完成", boundary: boundary(true) } };
  }
}

describe("V2ActivityGroup", () => {
  it.each([
    ["Host · Full Access", "宿主网络未隔离"],
    ["Workspace Sandbox", "无网络"],
    ["Legacy execution boundary", "网络隔离未确认"],
  ] as const)("describes the trusted %s network boundary consistently in both detail readers", (environment, networkLabel) => {
    const response: ThreadActivityDetailView = { version: "thread_activity_detail.v2", activity_ref: "command-network",
      run_id: "run-1", tools: [{ name: "command_runtime", label: "运行命令", agent_id: "agent-root", agent_role: "root",
        agent_label: "Root Agent", status: "completed", started_at: "2026-09-12T05:00:00Z",
        completed_at: "2026-09-12T05:00:00.125Z", duration_milliseconds: 125,
        detail: { kind: "command", command: { commands: [{ command: "Read the uploaded ZIP", working_directory: ".",
          execution_environment: environment, network: "disabled", status: "completed", exit_code: 0,
          duration_milliseconds: 125, stdout_preview: "原件SHA已保存", stderr_preview: "", truncated: false, artifacts: [] }] } },
      }] };
    const expected = `${environment} · ${networkLabel}`;
    expect(projectThreadActivityDetail(response).commands[0].environment_label).toBe(expected);
    render(<ThreadActivityToolDetailPanel activityRef="command-network" runID="run-1" threadID="thread-1"
      client={{} as APIClient} tool={response.tools[0]} />);
    expect(screen.getByText(expected)).toBeInTheDocument();
    expect(screen.getByText("原件SHA已保存")).toBeInTheDocument();
    if (environment !== "Workspace Sandbox") expect(screen.queryByText(/无网络/u)).not.toBeInTheDocument();
  });

  it("keeps the entire long command and output available when the summary is visually constrained", async () => {
    const command = `$p=Join-Path $env:TRAVERSE_ATTACHMENTS_DIR '${"attachment-0123456789abcdef/".repeat(24)}content.zip'; Get-FileHash -LiteralPath $p`;
    const stdout = `原始输出\n${"0123456789abcdef".repeat(40)}\n中文结果仍完整。`;
    const client = { threadActivityDetail: vi.fn().mockResolvedValue({
      version: "thread_activity_detail.v2", activity_ref: "command-long", run_id: "run-1",
      tools: [{ name: "command_runtime", label: "运行命令", agent_id: "agent-root", agent_role: "root",
        agent_label: "Root Agent", status: "completed", duration_milliseconds: 25,
        detail: commandDetail([{ command, working_directory: ".", status: "completed", exit_code: 0,
          duration_milliseconds: 25, stdout_preview: stdout, stderr_preview: "", truncated: false,
          artifacts: [], execution_environment: "Host", network: "disabled" }]) }],
    }) } as unknown as APIClient;
    const view = renderActivity(client, activity({ items: [{ ...activity().items[0], title: "长命令",
      detailRef: "command-long", summary: { version: "thread_activity_summary.v1", activity_ref: "command-long",
        command, status: "completed", exit_code: 0, duration_milliseconds: 25, command_count: 1 } }] }));
    expect(command.length).toBeGreaterThan(500);
    expect(view.container.querySelector(".v2-activity > summary small")).toHaveTextContent(command);
    const user = userEvent.setup();
    await user.click(screen.getByText("运行"));
    await user.click(screen.getByLabelText("查看长命令的执行详情"));
    await waitFor(() => expect(view.container.querySelector(".v2-command-input code")?.textContent).toBe(command));
    expect(view.container.querySelector(".v2-command-detail > header > strong")).toHaveTextContent("Shell");
    expect(view.container.querySelector('section[aria-label="标准输出"] pre code')?.textContent).toBe(stdout);
  });

  it.each([
    { status: "pending", duration: 0, exitCode: undefined, label: "待处理" },
    { status: "failed", duration: 125, exitCode: undefined, label: "失败" },
    { status: "completed", duration: 0, exitCode: 0, label: "Exit 0" },
  ] as const)("renders the structured $status summary without object coercion or an invented pending duration", async ({ status, duration, exitCode, label }) => {
    localStorage.setItem("prayu.locale.v1", "zh-CN");
    const callID = "toolu_695f966522275d3775352cde";
    const runID = "run-20260912044054-4bd2d4524e1b";
    const activityRef = `command-${callID}`;
    const items: ThreadTranscriptActivityItem[] = [{
      version: "thread_transcript.v1", id: "event-136", canonical_id: callID,
      run_id: runID, run_ordinal: 1, sequence: 136, kind: "tool_call", source: "harness",
      activity_type: "execute", stage: status === "pending" ? "started" : "result",
      title: "运行命令", detail: "command_runtime", tool_name: "command_runtime", status,
      durable_call_id: callID, stream_item_id: "item_fd4d91b0003f25312b1a9d18",
      stream_call_id: "call_d4b8e0893a3ff9634c26899b", verifiable: true,
      instruction_authorized: false, provisional: false, durable: true,
      created_at: "2026-09-12T04:55:23.6526771Z", activity_detail_ref: activityRef, detail_available: true,
      activity_summary: { version: "thread_activity_summary.v1", activity_ref: activityRef,
        command: "command_runtime", status, duration_milliseconds: duration, command_count: 1,
        ...(exitCode === undefined ? {} : { exit_code: exitCode }) },
    }];
    const entry = projectThreadNarrative(items)[0];
    expect(entry.kind).toBe("activity");
    const client = { threadActivityDetail: vi.fn().mockResolvedValue({
      version: "thread_activity_detail.v2", activity_ref: activityRef, run_id: runID,
      tools: [{ name: "command_runtime", label: "运行命令", agent_id: "agent-root", agent_role: "root",
        agent_label: "Root Agent", status, duration_milliseconds: duration,
        detail: commandDetail([{ command: "command_runtime", working_directory: ".", status,
          ...(exitCode === undefined ? {} : { exit_code: exitCode }), duration_milliseconds: duration,
          stdout_preview: "", stderr_preview: status === "failed" ? "真实工具失败原因" : "",
          truncated: false, artifacts: [], execution_environment: "Host", network: "disabled" }]) }],
    }) } as unknown as APIClient;
    const queries = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    const view = render(<LocaleProvider><QueryClientProvider client={queries}>
      <V2ActivityGroup client={client} entry={entry as ActivityEntry} threadID="thread-1" />
    </QueryClientProvider></LocaleProvider>);
    const summary = view.container.querySelector(".v2-activity > summary")!;
    expect(summary).toHaveTextContent(`command_runtime · ${label}`);
    expect(summary).not.toHaveTextContent("[object Object]");
    if (status === "pending") {
      expect(summary).not.toHaveTextContent("0ms");
      expect(summary).not.toHaveTextContent(/已完成|通过|Exit/u);
      const user = userEvent.setup();
      await user.click(screen.getByText("运行"));
      await user.click(screen.getByLabelText("查看command_runtime的执行详情"));
      await waitFor(() => expect(view.container.querySelector(".v2-command-detail")).toBeInTheDocument());
      expect(view.container).not.toHaveTextContent("0ms");
      expect(view.container).not.toHaveTextContent(/Exit 0|已完成/u);
    } else {
      expect(summary).toHaveTextContent(`${duration}ms`);
      if (status === "failed") expect(await screen.findByText("真实工具失败原因")).toBeInTheDocument();
    }
    view.unmount(); queries.clear();
  });

  it("localizes a failed command status while preserving its actual output and exit code", async () => {
    localStorage.setItem("prayu.locale.v1", "zh-CN");
    const client = { threadActivityDetail: vi.fn().mockResolvedValue({
      version: "thread_activity_detail.v2", run_id: "run-1", tools: [{
        tool_name: "command_runtime", agent_id: "agent-1", agent_label: "Root Agent", agent_role: "root",
        status: "failed", duration_milliseconds: 99,
        detail: commandDetail([{ command: "actual check", status: "failed", exit_code: 7,
          duration_milliseconds: 99, stdout_preview: "original completed text", stderr_preview: "", artifacts: [],
          execution_environment: "Host", network: "disabled" }]),
      }],
    }) } as unknown as APIClient;
    const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    const view = render(<LocaleProvider><QueryClientProvider client={queryClient}>
      <V2ActivityGroup client={client} threadID="thread-1" entry={activity({ status: "failed",
        items: [{ ...activity().items[0], status: "failed" }] })} />
    </QueryClientProvider></LocaleProvider>);
    expect(await screen.findByText("original completed text")).toBeInTheDocument();
    expect(view.container.querySelector(".v2-command-detail header .is-failed")).toHaveTextContent("失败");
    expect(screen.getByText("Exit 7 · 99ms")).toBeInTheDocument();
    expect(screen.queryByText("通过")).not.toBeInTheDocument();
  });

  it("keeps proposal, approval and application distinct without counting a completed tool as another file", () => {
    // Matches the durable Go projection: the file event and tool result have
    // different canonical IDs, with no shared edit ID in the public transcript.
    const base = { version: "thread_transcript.v1", run_id: "run-1", run_ordinal: 1,
      activity_type: "edit", source: "harness", verifiable: true,
      instruction_authorized: false, provisional: false, durable: true,
      created_at: "2026-09-08T16:03:17Z" } as const;
    const client = {} as APIClient;
    for (const [status, expected] of [
      ["pending", "已提出修改，等待审阅"],
      ["approved", "修改已批准，尚未应用"],
      ["completed", "修改已应用"],
    ] as const) {
      const transcript: ThreadTranscriptActivityItem[] = [
        { ...base, id: "file-event", canonical_id: "file-event", sequence: 1,
          kind: "file_change", stage: status === "pending" ? "started" : "result",
          title: "文件修改", status },
        { ...base, id: "tool-result", canonical_id: "tool-call", sequence: 2,
          kind: "tool_call", stage: "result", status: "completed",
          tool_name: "workspace_change", title: "工具结果已记录", detail: "workspace_change" },
      ];
      const entries = projectThreadNarrative(transcript);
      expect(entries).toHaveLength(1);
      const entry = entries[0] as ActivityEntry;
      expect(entry.items).toHaveLength(2); // Both original audit records remain accessible.
      const rendered = renderActivity(client, entry);
      const summary = rendered.container.querySelector(".v2-activity > summary");
      expect(summary).toHaveTextContent(expected);
      expect(summary).not.toHaveTextContent(/修改了|2 项|workspace_change/u);
      if (status !== "completed") expect(summary).not.toHaveTextContent("修改已应用");
      rendered.unmount();
    }
  });

  it("loads a sanitized command projection only when its activity item is expanded", async () => {
    const threadActivityDetail = vi.fn(() => Promise.resolve({
      version: "thread_activity_detail.v2",
      activity_ref: "command-1",
      run_id: "run-1",
      tools: [{
        name: "command_runtime", label: "运行命令", agent_id: "agent-root", agent_role: "root" as const,
        agent_label: "Root Agent",
        status: "completed", started_at: "2026-09-02T00:00:00Z",
        completed_at: "2026-09-02T00:00:02.400Z", duration_milliseconds: 2_400,
        detail: commandDetail([{
          command: "pnpm test session", working_directory: "packages/core",
          execution_environment: "Workspace Sandbox", network: "disabled",
           status: "completed", exit_code: 0, duration_milliseconds: 2_400,
           stdout_preview: "✓ 42 tests passed", stderr_preview: "", truncated: false, artifacts: [],
        }]),
      }],
    }));
    const client = { threadActivityDetail } as unknown as APIClient;
    const user = userEvent.setup();
    renderActivity(client);

    expect(threadActivityDetail).not.toHaveBeenCalled();
    await user.click(screen.getByText("运行"));
    expect(threadActivityDetail).not.toHaveBeenCalled();
    await user.click(screen.getByLabelText("查看pnpm test session的执行详情"));

    expect(await screen.findByText("✓ 42 tests passed")).toBeInTheDocument();
    expect(threadActivityDetail).toHaveBeenCalledTimes(1);
    expect(threadActivityDetail).toHaveBeenCalledWith(
      "thread-1", "command-1", expect.any(AbortSignal));
    expect(screen.getByText("packages/core")).not.toBeVisible();
    await user.click(screen.getByText("执行信息"));
    expect(screen.getByText("packages/core")).toBeVisible();
    expect(screen.getByText("Root Agent")).toBeInTheDocument();
    expect(screen.getByTitle("agent-root")).toHaveTextContent("agent-root");
    expect(screen.getByText("Workspace Sandbox · 无网络")).toBeInTheDocument();
    expect(screen.getByText("Exit 0 · 2.4s")).toBeInTheDocument();
    expect(screen.queryByText("部分输出字符无法正确解码，当前文本可能不完整。")).not.toBeInTheDocument();
  });

  it.each(["stdout_preview", "stderr_preview"] as const)(
    "keeps failed command %s visible and flags replacement characters without retrying", async (stream) => {
      const output = "PSEtwLog: 初始化失败 \uFFFD\uFFFD";
      const threadActivityDetail = vi.fn().mockResolvedValue({
        version: "thread_activity_detail.v2", activity_ref: "command-1", run_id: "run-1",
        tools: [{ name: "command_runtime", label: "运行命令", agent_id: "agent-root",
          agent_role: "root", agent_label: "Root Agent", status: "failed",
          started_at: "2026-09-02T00:00:00Z", completed_at: "2026-09-02T00:00:00.099Z",
          duration_milliseconds: 99,
          detail: commandDetail([{ command: "powershell -NoProfile -File check.ps1",
            working_directory: ".", execution_environment: "Workspace Sandbox", network: "disabled",
            status: "failed", exit_code: 4294901760, duration_milliseconds: 99,
            stdout_preview: "", stderr_preview: "", [stream]: output, truncated: false, artifacts: [] }]),
        }],
      });
      const view = renderActivity({ threadActivityDetail } as unknown as APIClient,
        activity({ status: "failed", items: [{ ...activity().items[0], status: "failed" }] }));

      expect(await screen.findByText(output)).toBeInTheDocument();
      expect(screen.getByText("部分输出字符无法正确解码，当前文本可能不完整。")).toBeInTheDocument();
      expect(view.container.querySelector(".v2-command-detail header .is-failed")).toHaveTextContent("失败");
      expect(screen.getByText("Exit 4294901760 · 99ms")).toBeInTheDocument();
      expect(screen.queryByRole("button", { name: "重试" })).not.toBeInTheDocument();
      expect(threadActivityDetail).toHaveBeenCalledTimes(1);
    });

  it.each([
    { name: "decoded startup output", exitCode: 4294901760, notice: true,
      output: "Windows PowerShell 被终止，返回以下错误: \n “System.Management.Automation.Tracing.PSEtwLog”的类型初始值设定项引发异常。\n" },
    { name: "preserved historical startup output", exitCode: 4294901760, notice: true,
      output: "Windows PowerShell \ufffd\ufffd\ufffd~bk\ufffd\u050f\ufffdV\ufffdNN\ufffd\ufffd\ufffd: \n  System.Management.Automation.Tracing.PSEtwLog \ufffdv{|\ufffdWR\ufffdY<P\ufffd\ufffd\ufffd[y\ufffd_\ufffdS_8^0\n" },
    { name: "ordinary test failure", exitCode: 4294901760, notice: false,
      output: "Assertion failed: expected 2, received 1" },
    { name: "startup text with a different exit", exitCode: 1, notice: false,
      output: "Windows PowerShell System.Management.Automation.Tracing.PSEtwLog test fixture" },
  ])("explains only the known startup failure signature: $name", async ({ exitCode, notice, output }) => {
    const threadActivityDetail = vi.fn().mockResolvedValue({
      version: "thread_activity_detail.v2", activity_ref: "command-1", run_id: "run-1",
      tools: [{ name: "command_runtime", label: "运行命令", agent_id: "agent-root",
        agent_role: "root", agent_label: "Root Agent", status: "failed",
        started_at: "2026-09-02T00:00:00Z", duration_milliseconds: 99,
        detail: commandDetail([{ command: "& ./check.ps1", working_directory: ".",
          execution_environment: "Workspace Sandbox", network: "disabled", status: "failed",
          exit_code: exitCode, duration_milliseconds: 99, stdout_preview: "",
          stderr_preview: output, truncated: false, artifacts: [] }]),
      }],
    });
    const view = renderActivity({ threadActivityDetail } as unknown as APIClient,
      activity({ status: "failed", items: [{ ...activity().items[0], status: "failed" }] }));
    await waitFor(() => expect(view.container.querySelector("[aria-label='标准错误'] code")?.textContent).toBe(output));
    const explanation = screen.queryByText("输出提示 PowerShell 初始化失败，当前检查未完成。若使用 Windows 隔离工作区，请安装 PowerShell 7，并在宿主环境中将 CYBERAGENT_POWERSHELL_PATH 设置为 pwsh.exe 的绝对路径，重启应用后重试。完整错误仍保留在下方。");
    if (notice) expect(explanation).toBeInTheDocument();
    else expect(explanation).not.toBeInTheDocument();
    expect(view.container.querySelector(".v2-command-detail header .is-failed")).toHaveTextContent("失败");
    expect(screen.getByText(`Exit ${exitCode} · 99ms`)).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "重试" })).not.toBeInTheDocument();
    expect(threadActivityDetail).toHaveBeenCalledTimes(1);
  });

  it("preserves exact Specialist identity and labels historical unknown execution", async () => {
    const specialistID = "agent-specialist-analysis-1234567890";
    const specialist = projectThreadActivityDetail({
      version: "thread_activity_detail.v2", activity_ref: "command-specialist", run_id: "run-1",
      tools: [{ name: "command_runtime", label: "运行命令", agent_id: specialistID,
        agent_role: "specialist", agent_label: "Review Agent", status: "completed",
        started_at: "2026-09-03T00:00:00Z", duration_milliseconds: 1,
        detail: commandDetail([{ command: "go test ./...", working_directory: ".",
          execution_environment: "Workspace Sandbox", network: "disabled", status: "completed",
          exit_code: 0, duration_milliseconds: 1, stdout_preview: "", stderr_preview: "",
          truncated: false, artifacts: [] }]),
      }],
    } as never);
    expect(specialist.commands[0]?.agent_id).toBe(specialistID);

    const threadActivityDetail = vi.fn(() => Promise.resolve({
      version: "thread_activity_detail.v2" as const, activity_ref: "command-1", run_id: "run-1",
      tools: [{ name: "command_runtime", label: "运行命令", agent_id: "unknown",
        agent_role: "unknown" as const, agent_label: "历史活动（执行者未知）", status: "completed",
        started_at: "2026-09-03T00:00:00Z", duration_milliseconds: 1,
        detail: commandDetail([{ command: "go test ./...", working_directory: ".",
          execution_environment: "Workspace Sandbox", network: "disabled", status: "completed",
          exit_code: 0, duration_milliseconds: 1, stdout_preview: "", stderr_preview: "",
          truncated: false, artifacts: [] }]),
      }],
    }));
    const client = { threadActivityDetail } as unknown as APIClient;
    const user = userEvent.setup();
    renderActivity(client);
    await user.click(screen.getByText("运行"));
    await user.click(screen.getByLabelText("查看pnpm test session的执行详情"));
    expect(await screen.findByText("历史活动（执行者未知）")).toBeInTheDocument();
    expect(screen.queryByTitle("unknown")).not.toBeInTheDocument();
  });

  it("opens failed activities by default and offers an inline retry after a detail error", async () => {
    const threadActivityDetail = vi.fn()
      .mockRejectedValueOnce(new Error("sensitive backend failure"))
      .mockResolvedValueOnce({
        version: "thread_activity_detail.v2",
        activity_ref: "command-1", run_id: "run-1", tools: [{
          name: "command_runtime", label: "运行命令", agent_id: "agent-root",
          agent_role: "root" as const, agent_label: "Root Agent",
          status: "failed", started_at: "2026-09-02T00:00:00Z",
          completed_at: "2026-09-02T00:00:00.099Z", duration_milliseconds: 99,
          detail: commandDetail([{ command: "pnpm test session", working_directory: ".",
             execution_environment: "Workspace Sandbox", network: "disabled",
             status: "failed", exit_code: 2, duration_milliseconds: 99,
             stdout_preview: "", stderr_preview: "two tests failed", truncated: true, artifacts: [] }]),
        }],
      });
    const client = { threadActivityDetail } as unknown as APIClient;
    const user = userEvent.setup();
    const view = renderActivity(client, activity({
      status: "failed",
      items: [{ ...activity().items[0], status: "failed", detail: "命令执行失败" }],
    }));

    await screen.findByRole("alert");
    expect(view.container.querySelector(".v2-activity")).toHaveAttribute("open");
    expect(view.container.querySelector(".v2-activity-item details")).toHaveAttribute("open");
    expect(screen.queryByText("sensitive backend failure")).not.toBeInTheDocument();

    await user.click(screen.getByRole("button", { name: "重试" }));
    expect(await screen.findByText("two tests failed")).toBeInTheDocument();
    expect(screen.getByText("Exit 2 · 99ms")).toBeInTheDocument();
    expect(screen.getByText(/输出过长，当前仅显示已脱敏预览/u)).toBeInTheDocument();
    expect(threadActivityDetail).toHaveBeenCalledTimes(2);
  });

  it("uses the command summary for collapsed facts and expands a real Job failure", async () => {
    const threadActivityDetail = vi.fn(() => Promise.resolve({
      version: "thread_activity_detail.v2", activity_ref: "command-1", run_id: "run-1",
      tools: [{ name: "command_runtime", label: "运行命令", agent_id: "agent-root",
        agent_role: "root" as const, agent_label: "Root Agent",
        status: "completed", started_at: "2026-09-02T00:00:00Z",
        completed_at: "2026-09-02T00:00:00.125Z", duration_milliseconds: 125,
        detail: commandDetail([{ command: "pnpm test session", working_directory: ".",
           execution_environment: "Workspace Sandbox", network: "disabled",
           status: "timed_out", exit_code: 124, duration_milliseconds: 125,
           stdout_preview: "", stderr_preview: "deadline exceeded", truncated: false, artifacts: [] }]),
      }],
    }));
    const client = { threadActivityDetail } as unknown as APIClient;
    const failedEntry = activity({ status: "completed", items: [{ ...activity().items[0],
      status: "completed", detail: "工具批次完成", summary: {
        version: "thread_activity_summary.v1", activity_ref: "command-1",
        command: "pnpm test session", status: "timed_out", exit_code: 124,
        duration_milliseconds: 125, command_count: 1,
      } }],
    });
    const view = renderActivity(client, failedEntry);

    expect(view.container.querySelector(".v2-activity")).toHaveAttribute("open");
    expect(view.container.querySelector(".v2-activity-item details")).toHaveAttribute("open");
    expect(screen.getByText("Exit 124")).toBeInTheDocument();
    expect(screen.getByText("125ms")).toBeInTheDocument();
    expect(await screen.findByText("deadline exceeded")).toBeInTheDocument();
  });

  it("opens a previously collapsed activity when its summary becomes failed", async () => {
    const threadActivityDetail = vi.fn(() => Promise.resolve({
      version: "thread_activity_detail.v2", activity_ref: "command-1", run_id: "run-1",
      tools: [{ name: "command_runtime", label: "运行命令", agent_id: "agent-root",
        agent_role: "root" as const, agent_label: "Root Agent",
        status: "completed", started_at: "2026-09-02T00:00:00Z",
        completed_at: "2026-09-02T00:00:00.050Z", duration_milliseconds: 50,
        detail: commandDetail([{ command: "pnpm test session", working_directory: ".",
           execution_environment: "Workspace Sandbox", network: "disabled",
           status: "killed", duration_milliseconds: 50, stdout_preview: "",
           stderr_preview: "process killed", truncated: false, artifacts: [] }]),
      }],
    }));
    const client = { threadActivityDetail } as unknown as APIClient;
    const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    const initial = activity({ items: [{ ...activity().items[0], summary: {
      version: "thread_activity_summary.v1", activity_ref: "command-1",
      command: "pnpm test session", status: "completed", exit_code: 0,
      duration_milliseconds: 50, command_count: 1,
    } }] });
    const view = render(<QueryClientProvider client={queryClient}>
      <V2ActivityGroup client={client} entry={initial} threadID="thread-1" />
    </QueryClientProvider>);
    expect(view.container.querySelector(".v2-activity")).not.toHaveAttribute("open");

    const failed = activity({ items: [{ ...activity().items[0], summary: {
      version: "thread_activity_summary.v1", activity_ref: "command-1",
      command: "pnpm test session", status: "killed",
      duration_milliseconds: 50, command_count: 1,
    } }] });
    view.rerender(<QueryClientProvider client={queryClient}>
      <V2ActivityGroup client={client} entry={failed} threadID="thread-1" />
    </QueryClientProvider>);

    await waitFor(() => expect(view.container.querySelector(".v2-activity"))
      .toHaveAttribute("open"));
    await waitFor(() => expect(view.container.querySelector(".v2-activity-item details"))
      .toHaveAttribute("open"));
    expect(await screen.findByText("process killed")).toBeInTheDocument();
  });

  it("polls an expanded pending command until its live tail becomes terminal", async () => {
    const response = (status: "running" | "completed", stdout: string) => ({
      version: "thread_activity_detail.v2" as const, activity_ref: "command-1",
      run_id: "run-1", tools: [{ name: "command_runtime", label: "运行命令",
        agent_id: "agent-root", agent_role: "root" as const, agent_label: "Root Agent",
        status: status === "running" ? "pending" : "completed",
        started_at: "2026-09-02T00:00:00Z", duration_milliseconds: 500,
        detail: commandDetail([{ command: "pnpm test session", working_directory: ".",
          execution_environment: "Workspace Sandbox", network: "disabled",
           status, ...(status === "completed" ? { exit_code: 0 } : {}),
           duration_milliseconds: 500, stdout_preview: stdout, stderr_preview: "",
           truncated: false, artifacts: [] }]),
      }],
    });
    const threadActivityDetail = vi.fn()
      .mockResolvedValueOnce(response("running", "12 tests completed"))
      .mockResolvedValue(response("completed", "42 tests passed"));
    const client = { threadActivityDetail } as unknown as APIClient;
    const user = userEvent.setup();
    renderActivity(client, activity({ items: [{ ...activity().items[0], status: "pending",
      summary: { version: "thread_activity_summary.v1", activity_ref: "command-1",
        command: "pnpm test session", status: "running", duration_milliseconds: 500,
        command_count: 1 } }] }));

    await user.click(screen.getByText("运行"));
    await user.click(screen.getByLabelText("查看pnpm test session的执行详情"));
    expect(await screen.findByText("12 tests completed")).toBeInTheDocument();
    await waitFor(() => expect(threadActivityDetail.mock.calls.length).toBeGreaterThanOrEqual(2),
      { timeout: 2_500 });
    expect(await screen.findByText("42 tests passed")).toBeInTheDocument();
  });

  it("loads a complete sanitized output artifact only after the user asks for it", async () => {
    const threadActivityDetail = vi.fn(() => Promise.resolve({
      version: "thread_activity_detail.v2" as const, activity_ref: "command-1", run_id: "run-1",
      tools: [{ name: "command_runtime", label: "运行命令", agent_id: "agent-root",
        agent_role: "root" as const, agent_label: "Root Agent",
        status: "completed", started_at: "2026-09-02T00:00:00Z",
        completed_at: "2026-09-02T00:00:01Z", duration_milliseconds: 1_000,
        detail: commandDetail([{ command: "pnpm test", working_directory: ".",
          execution_environment: "Workspace Sandbox" as const, network: "disabled" as const,
          status: "completed", exit_code: 0, duration_milliseconds: 1_000,
          stdout_preview: "42 tests…", stderr_preview: "", truncated: true,
          artifacts: [{ artifact_ref: "artifact-stdout-1", stream: "stdout" as const,
            mime: "text/plain; charset=utf-8" as const, size_bytes: 32, truncated: false }],
        }]),
      }],
    }));
    const threadActivityArtifact = vi.fn(() => Promise.resolve({
      version: "thread_activity_artifact.v1" as const, activity_ref: "command-1",
      artifact_ref: "artifact-stdout-1", stream: "stdout" as const,
      mime: "text/plain; charset=utf-8" as const, content: "Full output with a damaged character: \uFFFD",
      sha256: "a".repeat(64), size_bytes: 32, redacted: true, truncated: false,
      untrusted: true as const, instruction_authorized: false as const,
    }));
    const client = { threadActivityArtifact, threadActivityDetail } as unknown as APIClient;
    const user = userEvent.setup();
    renderActivity(client);

    await user.click(screen.getByText("运行"));
    await user.click(screen.getByLabelText("查看pnpm test session的执行详情"));
    expect(await screen.findByText("查看已保存标准输出")).toBeInTheDocument();
    expect(screen.queryByText("部分输出字符无法正确解码，当前文本可能不完整。")).not.toBeInTheDocument();
    expect(threadActivityArtifact).not.toHaveBeenCalled();
    await user.click(screen.getByText("查看已保存标准输出"));

    expect(await screen.findByText("Full output with a damaged character: \uFFFD")).toBeInTheDocument();
    expect(screen.getByText("部分输出字符无法正确解码，当前文本可能不完整。")).toBeInTheDocument();
    expect(screen.getByText(/以下是工具返回的参考数据/u)).toBeInTheDocument();
    expect(threadActivityArtifact).toHaveBeenCalledWith(
      "thread-1", "command-1", "artifact-stdout-1", expect.any(AbortSignal));
  });

  it.each([
    ["en-US", "Read saved stdout", "Saved output could not be loaded.", "Retry output", "Hide stdout", "Showing saved content"],
    ["zh-CN", "查看已保存标准输出", "已保存输出加载失败。", "重试输出", "收起标准输出", "展示已保存内容"],
  ] as const)("uses %s for saved output controls, errors and retry results", async (locale, readLabel, errorLabel, retryLabel, hideLabel, savedLabel) => {
    localStorage.setItem("prayu.locale.v1", locale);
    const threadActivityArtifact = vi.fn().mockRejectedValueOnce(new Error("Temporary read failure"))
      .mockResolvedValueOnce({
        stream: "stdout", content: "Original saved output", size_bytes: 21, redacted: true, truncated: false,
      });
    const client = { threadActivityArtifact } as unknown as APIClient;
    const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    const tool: ThreadActivityDetailView["tools"][number] = {
      name: "command_runtime", label: "Run command", agent_id: "agent-root", agent_role: "root",
      agent_label: "Root Agent", status: "completed", started_at: "2026-09-02T00:00:00Z",
      completed_at: "2026-09-02T00:00:01Z", duration_milliseconds: 1_000,
      detail: { kind: "command", command: { commands: [{
        command: "check", working_directory: ".", execution_environment: "Host · Full Access", network: "disabled",
        status: "completed", exit_code: 0, duration_milliseconds: 1_000,
        stdout_preview: "Preview", stderr_preview: "", truncated: true,
        artifacts: [{ artifact_ref: "artifact-stdout-1", stream: "stdout", mime: "text/plain; charset=utf-8",
          size_bytes: 21, truncated: false }],
      }] } },
    };
    const user = userEvent.setup();
    renderComponent(<LocaleProvider><QueryClientProvider client={queryClient}>
      <ThreadActivityToolDetailPanel activityRef="command-1" client={client} runID="run-1"
        threadID="thread-1" tool={tool} />
    </QueryClientProvider></LocaleProvider>);

    expect(threadActivityArtifact).not.toHaveBeenCalled();
    await user.click(screen.getByRole("button", { name: new RegExp(`^${readLabel}`, "u") }));
    expect(await screen.findByRole("alert")).toHaveTextContent(errorLabel);
    await user.click(screen.getByRole("button", { name: retryLabel }));
    expect(await screen.findByText("Original saved output")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: new RegExp(`^${hideLabel}`, "u") })).toBeInTheDocument();
    expect(screen.getByText(new RegExp(savedLabel, "u"))).toBeInTheDocument();
    expect(screen.queryByRole("alert")).not.toBeInTheDocument();
    expect(threadActivityArtifact).toHaveBeenCalledTimes(2);
  });

  it("loads an exact redacted file Diff only after the secondary disclosure is opened", async () => {
    const threadActivityDetail = vi.fn(() => Promise.resolve({
      version: "thread_activity_detail.v2" as const, activity_ref: "detail-file-edit",
      run_id: "run-1", tools: [{ name: "workspace_apply", label: "文件修改",
        agent_id: "agent-root", agent_role: "root" as const, agent_label: "Root Agent",
        status: "completed", started_at: "2026-09-02T00:00:00Z",
        completed_at: "2026-09-02T00:00:00.020Z", duration_milliseconds: 20,
        detail: fileEditActivityDetail(),
      }],
    }));
    const fileEdit = vi.fn(() => Promise.resolve({
      id: "edit-1", session_id: "session-1", workspace_id: "workspace-1",
      path: "safe-target", operation: "replace" as const, status: "applied" as const,
      diff: "--- a/safe-target\n+++ b/safe-target\n@@ -1 +1 @@\n-const answer = 41;\n+const answer = 42;\n",
      original_hash: "a".repeat(64), proposed_hash: "b".repeat(64),
      secrets_redacted: true, allowed_actions: [], apply_enabled: false,
      created_at: "2026-09-02T00:00:00Z", updated_at: "2026-09-02T00:00:00Z",
    }));
    const client = { fileEdit, threadActivityDetail } as unknown as APIClient;
    const user = userEvent.setup();
    renderActivity(client, activity({ activity: "edit", items: [{ title: "文件修改",
      detail: "safe summary", status: "completed", provisional: false,
      detailRef: "detail-file-edit", detailAvailable: true }] }));

    await user.click(screen.getByText("文件修改记录"));
    await user.click(screen.getByLabelText("查看文件修改的执行详情"));
    const disclosure = await screen.findByRole("button", { name: "查看 Diff" });
    expect(fileEdit).not.toHaveBeenCalled();

    await user.click(disclosure);
    expect(await screen.findByText("const answer = 42;")).toBeInTheDocument();
    expect(screen.getByRole("table", { name: "文件 Diff：safe-target" })).toBeInTheDocument();
    expect(screen.getByText(/敏感内容已脱敏 · 1 行新增，1 行删除/u)).toBeInTheDocument();
    expect(fileEdit).toHaveBeenCalledWith("run-1", "edit-1", expect.any(AbortSignal));
  });

  it("explains when a file activity has no retrievable Diff without issuing a request", async () => {
    const threadActivityDetail = vi.fn(() => Promise.resolve({
      version: "thread_activity_detail.v2" as const, activity_ref: "detail-file-edit",
      run_id: "run-1", tools: [{ name: "workspace_apply", label: "文件修改",
        agent_id: "unknown", agent_role: "unknown" as const,
        agent_label: "历史活动（执行者未知）", status: "completed",
        started_at: "2026-09-02T00:00:00Z", duration_milliseconds: 20,
        detail: fileEditActivityDetail(false),
      }],
    }));
    const fileEdit = vi.fn();
    const client = { fileEdit, threadActivityDetail } as unknown as APIClient;
    const user = userEvent.setup();
    renderActivity(client, activity({ activity: "edit", items: [{ title: "文件修改",
      detail: "safe summary", status: "completed", provisional: false,
      detailRef: "detail-file-edit", detailAvailable: true }] }));

    await user.click(screen.getByText("文件修改记录"));
    await user.click(screen.getByLabelText("查看文件修改的执行详情"));
    expect(await screen.findByText("本次活动没有可展示的 Diff。")).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "查看 Diff" })).not.toBeInTheDocument();
    expect(fileEdit).not.toHaveBeenCalled();
  });

  it("keeps a failed lazy Diff request inside the activity and offers retry", async () => {
    const threadActivityDetail = vi.fn(() => Promise.resolve({
      version: "thread_activity_detail.v2" as const, activity_ref: "detail-file-edit",
      run_id: "run-1", tools: [{ name: "workspace_apply", label: "文件修改",
        agent_id: "agent-root", agent_role: "root" as const, agent_label: "Root Agent",
        status: "completed", started_at: "2026-09-02T00:00:00Z",
        duration_milliseconds: 20, detail: fileEditActivityDetail(),
      }],
    }));
    const fileEdit = vi.fn().mockRejectedValue(new Error("private storage failure"));
    const client = { fileEdit, threadActivityDetail } as unknown as APIClient;
    const user = userEvent.setup();
    renderActivity(client, activity({ activity: "edit", items: [{ title: "文件修改",
      detail: "safe summary", status: "completed", provisional: false,
      detailRef: "detail-file-edit", detailAvailable: true }] }));

    await user.click(screen.getByText("文件修改记录"));
    await user.click(screen.getByLabelText("查看文件修改的执行详情"));
    await user.click(await screen.findByRole("button", { name: "查看 Diff" }));
    expect(await screen.findByText("Diff 加载失败。")).toBeInTheDocument();
    expect(screen.queryByText("private storage failure")).not.toBeInTheDocument();
    expect(screen.getByRole("button", { name: "重试" })).toBeInTheDocument();
  });

  it.each([
    ["web_search", "联网搜索", "web_search", "safe-target"],
    ["web_fetch", "网页读取", "web_fetch", "https://example.com/reference"],
    ["file_read", "文件读取", "workspace_read", "safe-target"],
    ["file_edit", "文件修改", "workspace_apply", "safe-target"],
    ["verification", "验证", "code_diagnostics", "safe-target"],
    ["mcp", "MCP", "mcp_tool_call", "workspace"],
    ["browser", "浏览器", "browser_snapshot", "#safe-target"],
  ] as const)("renders %s as a typed safe branch instead of raw JSON",
  async (kind, label, toolName, expected) => {
    const threadActivityDetail = vi.fn(() => Promise.resolve({
      version: "thread_activity_detail.v2" as const, activity_ref: `detail-${kind}`,
      run_id: "run-1", tools: [{ name: toolName, label, agent_id: "agent-root",
        agent_role: "root" as const, agent_label: "Root Agent",
        status: "completed", started_at: "2026-09-02T00:00:00Z",
        completed_at: "2026-09-02T00:00:00.020Z", duration_milliseconds: 20,
        detail: typedDetail(kind),
      }],
    }));
    const client = { threadActivityDetail } as unknown as APIClient;
    const user = userEvent.setup();
    renderActivity(client, activity({ items: [{ title: label, detail: "safe summary",
      status: "completed", provisional: false, detailRef: `detail-${kind}`,
      detailAvailable: true }] }));

    await user.click(screen.getByText("运行"));
    await user.click(screen.getByLabelText(`查看${label}的执行详情`));
    expect(await screen.findByText(`${label} · inspect`)).toBeInTheDocument();
    expect(screen.getByText(expected)).toBeInTheDocument();
    expect(screen.queryByText(/payload_json/u)).not.toBeInTheDocument();
  });

  it.each(["failed", "completed"] as const)("describes a %s search with no sources using its actual outcome", async (status) => {
    const detail = typedDetail("web_search");
    if (detail.kind !== "web_search") throw new Error("expected search fixture");
    detail.web_search.source_count = 0;
    detail.web_search.sources = [];
    detail.web_search.citeable = false;
    detail.web_search.boundary.error_code = status === "failed" ? "UNAVAILABLE" : "";
    detail.web_search.boundary.failure_reason = status === "failed"
      ? "供应商原生搜索返回了无效响应。" : "";
    const threadActivityDetail = vi.fn().mockResolvedValue({
      version: "thread_activity_detail.v2", activity_ref: "search-no-sources", run_id: "run-1",
      tools: [{ name: "web_search", label: "联网搜索", agent_id: "agent-root", agent_role: "root",
        agent_label: "Root Agent", status, duration_milliseconds: 20, detail }],
    });
    renderActivity({ threadActivityDetail } as unknown as APIClient, activity({
      activity: "read", status, items: [{ title: "联网搜索", detail: "safe summary", status,
        provisional: false, detailRef: "search-no-sources", detailAvailable: true }],
    }));
    if (status === "completed") {
      const user = userEvent.setup();
      await user.click(screen.getByText("读取"));
      await user.click(screen.getByLabelText("查看联网搜索的执行详情"));
    }
    await screen.findByText("联网搜索 · inspect");
    if (status === "failed") {
      expect(screen.getByText("未取得搜索结果")).toBeInTheDocument();
      expect(screen.getByText("UNAVAILABLE")).toBeInTheDocument();
      expect(screen.getByText("供应商原生搜索返回了无效响应。")).toBeInTheDocument();
      expect(screen.queryByText(/待验证/)).not.toBeInTheDocument();
    } else {
      expect(screen.getByText("0 个来源 · 待验证")).toBeInTheDocument();
      expect(screen.queryByText("未取得搜索结果")).not.toBeInTheDocument();
    }
    expect(threadActivityDetail).toHaveBeenCalledTimes(1);
  });

  it("expands existing typed web evidence without making a detail request", async () => {
    const threadActivityDetail = vi.fn();
    const client = { threadActivityDetail } as unknown as APIClient;
    const user = userEvent.setup();
    renderActivity(client, activity({ activity: "read", items: [{ title: "网页已抓取",
      detail: "已创建快照", status: "completed", provisional: false,
      detailAvailable: false, webEvidence: {
        version: "web_evidence_presentation.v1", source_id: "source-1", snapshot_id: "snapshot-1",
        url: "https://example.com/reference", title: "Example reference", state: "fetched",
        fetched_at: "2026-09-02T00:00:00Z", stale_at: "2026-09-03T00:00:00Z",
        digest: "a".repeat(64), partial: false, stale: false, citeable: true,
        untrusted: true, instruction_authorized: false,
      } }],
    }));

    await user.click(screen.getByText("读取"));
    await user.click(screen.getByLabelText("查看网页已抓取的执行详情"));
    expect(screen.getByText("https://example.com/reference")).toBeInTheDocument();
    expect(screen.getByText("可引用")).toBeInTheDocument();
    expect(screen.getByText(/网页内容作为参考资料展示/u)).toHaveTextContent("操作授权以任务要求和审批为准");
    expect(threadActivityDetail).not.toHaveBeenCalled();
  });

  it("expands individual calls independently inside a tool group and reuses their loaded details", async () => {
    const threadActivityDetail = vi.fn((_threadID: string, activityRef: string) => Promise.resolve({
      version: "thread_activity_detail.v2", activity_ref: activityRef, run_id: "run-1",
      tools: [{ name: "command_runtime", label: "运行命令", status: "completed",
        agent_id: "agent-root", agent_role: "root", agent_label: "Root Agent",
        detail: commandDetail([{ command: activityRef, working_directory: ".",
          status: "completed", exit_code: 0, duration_milliseconds: 25,
          stdout_preview: `output: ${activityRef}`, stderr_preview: "", artifacts: [],
          execution_environment: "Workspace Sandbox", network: "disabled", truncated: false }]) }],
    }));
    const client = { threadActivityDetail } as unknown as APIClient;
    const user = userEvent.setup();
    renderActivity(client, activity({ count: 2, items: ["first", "second"].map((name) => ({
      title: name, detail: "", status: "completed", provisional: false,
      detailRef: name, detailAvailable: true,
    })) }));

    await user.click(screen.getByText("运行了 2 项"));
    expect(threadActivityDetail).not.toHaveBeenCalled();
    await user.click(screen.getByLabelText("查看first的执行详情"));
    expect(await screen.findByText("output: first")).toBeVisible();
    expect(screen.queryByText("output: second")).not.toBeInTheDocument();
    expect(threadActivityDetail).toHaveBeenCalledTimes(1);
    expect(threadActivityDetail).toHaveBeenLastCalledWith("thread-1", "first", expect.any(AbortSignal));

    await user.click(screen.getByLabelText("查看second的执行详情"));
    expect(await screen.findByText("output: second")).toBeVisible();
    await user.click(screen.getByLabelText("收起first的执行详情"));
    expect(screen.getByText("output: first")).not.toBeVisible();
    expect(screen.getByText("output: second")).toBeVisible();
    await user.click(screen.getByLabelText("查看first的执行详情"));
    expect(screen.getByText("output: first")).toBeVisible();
    expect(threadActivityDetail).toHaveBeenCalledTimes(2);
  });

  it("keeps old transcript activities readable without requesting unavailable details", async () => {
    const threadActivityDetail = vi.fn();
    const client = { threadActivityDetail } as unknown as APIClient;
    const user = userEvent.setup();
    renderActivity(client, activity({
      items: [{
        title: "旧工具活动",
        detail: "仅保留摘要",
        status: "completed",
        provisional: false,
        detailAvailable: false,
      }],
    }));

    await user.click(screen.getByText("运行"));
    expect(screen.getByText("旧工具活动")).toBeInTheDocument();
    expect(screen.getByText("仅保留摘要")).toBeInTheDocument();
    expect(screen.queryByLabelText(/旧工具活动的执行详情/u)).not.toBeInTheDocument();
    await waitFor(() => expect(threadActivityDetail).not.toHaveBeenCalled());
  });
});

function render(ui: ReactElement) {
 localStorage.setItem("prayu.locale.v1", "zh-CN");
 return renderComponent(<LocaleProvider>{ui}</LocaleProvider>);
}
