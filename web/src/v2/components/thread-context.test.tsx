import type { ReactElement } from "react";
import type { RenderOptions } from "@testing-library/react";
import { LocaleProvider, useLocale, type PrayuLocale } from "../../lib/locale";
import { createRef } from "react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act, cleanup, render as renderComponent, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import type { APIClient } from "../../api/client";
import type { ThreadDetailView } from "../../api/types";
import { V2ThreadContext, type RunContextSummary } from "./thread-context";

const sha = "a".repeat(64);
const when = "2026-09-12T00:00:00Z";
function detail(threadID = "task-a", workspaceID = "workspace-a"): ThreadDetailView {
  const run = { id: `${threadID}-run`, session_id: `${threadID}-session`, status: "running" };
  return { thread: { id: threadID, title: `标题 ${threadID}`, workspace_id: workspaceID },
    active_run: run, last_run: run, runs: [{ ordinal: 1, run }] } as ThreadDetailView;
}
function summary(threadID = "task-a"): RunContextSummary {
  return { run_id: `${threadID}-run`, thread_id: threadID, session_id: `${threadID}-session`, workspace_id: "workspace-a", capability_grant: false,
    current_summary: { id: 7, previous_summary_id: 6, protocol_version: "handoff_memory.v1", content: JSON.stringify({ version: "handoff_memory.v1",
      records_omitted: 2, records: [{ content: "保留最初目标：生成日志汇总，不能改接口。", source_ref: "operator-1" }] }), content_sha256: sha,
      content_redacted: false, content_truncated: false, compacted_message_count: 22, source_message_count: 26,
      preserved_message_count: 4, created_at: when },
    inherited_context: { source_run_id: "earlier-run", source_session_id: "earlier-session", fingerprint: "b".repeat(64), summary_id: 3,
      summary_content: "后续纠正：中文示例必须保留。", summary_content_sha256: "c".repeat(64), content_redacted: false, content_truncated: false,
      recent_message_count: 4, memories: [{ id: "user-preference", scope: "user", scope_id: "local-user", version: 2, content_sha256: sha }] } };
}
function instructions(threadID = "task-a") {
  return { run_id: `${threadID}-run`, workspace_id: "workspace-a", capability_grant: false, stale: true, pinned_present: true,
    pinned: { run_id: `${threadID}-run`, snapshot: { sources: [{ ordinal: 1, path: "AGENTS.md", kind: "agents_md", scope: ".", content: "do not expose this raw file",
      content_sha256: sha, loaded_at: when, why_effective: "项目根目录指令", redacted: false }] } } };
}
function evidence(threadID = "task-a") {
  return { protocol_version: "session_evidence_inventory.v1", run_id: `${threadID}-run`, truncated: true,
    items: [{ attachment_id: "attachment-1", run_id: `${threadID}-run`, session_id: `${threadID}-session`, workspace_id: "workspace-a",
      source_kind: "workspace_file", source_ref: "README.md", content_sha256: sha, instruction_authorized: false, attached_at: when }] };
}
function fixture() {
  const hooks: { summary?: (threadID: string) => unknown; instructions?: (threadID: string) => unknown; evidence?: (threadID: string) => unknown } = {};
  const get = vi.fn(async (path: string) => {
    const match = /^\/runs\/(.+)-run\/(context-summary|project-instructions)$/u.exec(path);
    if (!match) throw new Error(`Unexpected GET ${path}`);
    return match[2] === "context-summary" ? hooks.summary?.(match[1]) ?? summary(match[1]) : hooks.instructions?.(match[1]) ?? instructions(match[1]);
  });
  const evidenceInventory = vi.fn(async (runID: string) => hooks.evidence?.(runID.slice(0, -4)) ?? evidence(runID.slice(0, -4)));
  const postControl = vi.fn();
  const client = { baseURL: "/api/v1", get, evidenceInventory, postControl } as unknown as APIClient;
  return { client, get, evidenceInventory, postControl, hooks };
}
function mount(f: ReturnType<typeof fixture>, initial = detail()) {
  const queries = new QueryClient({ defaultOptions: { queries: { retry: false, staleTime: Infinity } } });
  const requestChange = vi.fn(); const close = vi.fn();
  const wrap = (value: ThreadDetailView) => <QueryClientProvider client={queries}><V2ThreadContext client={f.client} threadID={value.thread.id}
    detail={value} onRequestChange={requestChange} onClose={close} /></QueryClientProvider>;
  const view = render(wrap(initial));
  return { ...view, requestChange, close, rerenderDetail: (value: ThreadDetailView) => view.rerender(wrap(value)) };
}
afterEach(cleanup);

test("switching language updates context controls while preserving draft and original content", async () => {
  localStorage.setItem("prayu.locale.v1", "zh-CN");
  const f = fixture(); const user = userEvent.setup();
  const queries = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  let setLanguage: (locale: PrayuLocale) => void = () => undefined;
  function LanguageControl() { setLanguage = useLocale().setLocale; return null; }
  renderComponent(<LocaleProvider><LanguageControl /><QueryClientProvider client={queries}>
    <V2ThreadContext client={f.client} threadID="task-a" detail={detail()} onClose={vi.fn()} onRequestChange={vi.fn()} />
  </QueryClientProvider></LocaleProvider>);
  await screen.findByText("保留最初目标：生成日志汇总，不能改接口。");
  await user.type(screen.getByLabelText("需要继续保留的目标、限制或纠正"), "这段用户草稿不能被翻译");
  act(() => setLanguage("en-US"));
  expect(screen.getByRole("dialog", { name: "Task context" })).toBeVisible();
  expect(screen.getByRole("heading", { name: "Keep for this task" })).toBeVisible();
  expect(screen.getByRole("button", { name: "Compact conversation" })).toBeVisible();
  expect(screen.getByLabelText("Goals, constraints or corrections to retain")).toHaveValue("这段用户草稿不能被翻译");
  expect(screen.getByText("保留最初目标：生成日志汇总，不能改接口。")).toBeVisible();
  expect(screen.queryByRole("heading", { name: "摘要与压缩" })).not.toBeInTheDocument();
  act(() => setLanguage("zh-CN"));
  expect(screen.getByLabelText("需要继续保留的目标、限制或纠正")).toHaveValue("这段用户草稿不能被翻译");
});

test("ordinary context drawer reads diagnostic receipts from the bound summary endpoint without control writes", async () => {
  const f = fixture();
  f.hooks.summary = () => ({ ...summary(), diagnostics: { truncated: false, records: [{ sequence: 12,
    phase: "summary_saved", occurred_at: when, attempt_id: "attempt-a", source_sha256: sha,
    summary_id: 7, generated: false, removed_messages: 18, fallback_code: "generation_provider_failure" }] } });
  mount(f);
  expect(await screen.findByRole("heading", { name: "压缩与恢复进展" })).toBeVisible();
  expect(screen.getByText("已采用规则摘要回退：模型服务调用失败。")).toBeVisible();
  expect(f.get).toHaveBeenCalledWith("/runs/task-a-run/context-summary", {}, expect.any(AbortSignal));
  expect(f.postControl).not.toHaveBeenCalled();
});

test("malformed diagnostics are not displayed from a cached or cross-bound summary", async () => {
  const f = fixture();
  f.hooks.summary = () => ({ ...summary(), diagnostics: { records: [{ phase: "summary_saved" }], truncated: false } });
  mount(f);
  expect(await screen.findByText(/摘要读取失败，当前内容未确认/u)).toBeVisible();
  expect(screen.queryByRole("heading", { name: "压缩与恢复进展" })).not.toBeInTheDocument();
});

test.each([false, true])("distinguishes generated handoff from original excerpts (inherited: %s)", async (inherited) => {
  const f = fixture();
  f.hooks.summary = () => {
    const value = summary();
    const envelope = JSON.parse(value.current_summary!.content);
    envelope.generated = { version: "generated_handoff.v1", text: "继续修复日志输出；先前检查失败，尚需验证。",
      text_sha256: sha, input_fingerprint: sha, instruction_authorized: false, text_excerpted: true,
      source_refs: [{ source_id: "message:1", content_sha256: sha }], source_refs_omitted: 3,
      receipt: { run_id: "earlier-run", attempt_id: "attempt-1", model_attempt: 1, completion_sequence: 77,
        provider: "fixture", model: "fixture-model", source_sha256: sha } };
    if (inherited) {
      value.inherited_context!.summary_content = JSON.stringify({ version: "thread_summary_window.v1", lossy: true,
        instruction_authorized: false, sources: [{ source_id: "summary:3", part: "content", content_sha256: sha }], projection: envelope });
      delete value.current_summary;
    } else value.current_summary!.content = JSON.stringify(envelope);
    return value;
  };
  const view = mount(f); const user = userEvent.setup();
  expect(await screen.findByRole("heading", { name: "模型生成的摘要" })).toBeVisible();
  expect(screen.getByText("继续修复日志输出；先前检查失败，尚需验证。")).toBeVisible();
  expect(screen.getByRole("heading", { name: "原文摘录" })).toBeVisible();
  expect(screen.getByText("保留最初目标：生成日志汇总，不能改接口。")).toBeVisible();
  expect(screen.getByText(/这里保留了生成说明的部分内容/u)).toBeVisible();
  await user.type(screen.getByLabelText("需要继续保留的目标、限制或纠正"), "保留先前失败记录，重新验证。");
  await user.click(screen.getByRole("button", { name: "补充到对话输入" }));
  expect(view.requestChange).toHaveBeenCalledWith(expect.stringContaining("保留先前失败记录，重新验证。"));
  expect(f.postControl).not.toHaveBeenCalled();
});

test("shows saved instruction/reference/summary provenance and sends a correction only to the existing draft callback", async () => {
  const f = fixture();
  // Actual project-root snapshot shape: agents with an empty scope.
  f.hooks.instructions = () => {
    const value = instructions();
    value.pinned.snapshot.sources[0].kind = "agents";
    value.pinned.snapshot.sources[0].scope = "";
    return value;
  };
  const view = mount(f); const user = userEvent.setup();
  expect(await screen.findByText("AGENTS.md")).toBeVisible();
  expect(screen.getByText("项目指令 · 作用范围：项目根目录")).toBeVisible();
  expect(await screen.findByText("README.md")).toBeVisible();
  expect(await screen.findByText("保留最初目标：生成日志汇总，不能改接口。")).toBeVisible();
  expect(screen.getByText("后续纠正：中文示例必须保留。")).toBeVisible();
  expect(screen.getByText(/累计归纳 22 条消息/u)).toBeVisible();
  expect(screen.getByText(/2 条条目省略/u)).toBeVisible();
  expect(screen.getByText(/当前只显示部分记录/u)).toBeVisible();
  expect(screen.getByText(/项目指令文件已有变化，此执行沿用创建时固定的版本/u)).toBeVisible();
  expect(screen.queryByText("do not expose this raw file")).not.toBeInTheDocument();
  expect(view.requestChange).not.toHaveBeenCalled();
  await user.type(screen.getByLabelText("需要继续保留的目标、限制或纠正"), "保留中文示例，但改用 JSON 输出。");
  await user.click(screen.getByRole("button", { name: "补充到对话输入" }));
  expect(view.requestChange).toHaveBeenCalledWith(expect.stringContaining(`执行：task-a-run\n已保存摘要：7 / SHA256 ${sha}`));
  expect(view.requestChange).toHaveBeenCalledWith(expect.stringContaining("保留中文示例，但改用 JSON 输出。"));
  expect(f.postControl).not.toHaveBeenCalled();
});

test("read failure hides cached summary and stays distinct from a confirmed absence; GET retry can recover", async () => {
  const f = fixture(); mount(f); const user = userEvent.setup();
  await screen.findByText("保留最初目标：生成日志汇总，不能改接口。");
  f.hooks.summary = () => { throw new Error("test transport unavailable"); };
  await user.click(screen.getByRole("button", { name: "重新读取" }));
  expect(await screen.findByText(/摘要读取失败，当前内容未确认/u)).toBeVisible();
  expect(screen.queryByText("保留最初目标：生成日志汇总，不能改接口。")).not.toBeInTheDocument();
  expect(screen.queryByText(/此会话尚无已保存/u)).not.toBeInTheDocument();
  f.hooks.summary = () => { const value = summary(); delete value.current_summary; return value; };
  await user.click(screen.getByRole("button", { name: "重新读取" }));
  expect(await screen.findByText(/此会话尚无已保存的压缩摘要/u)).toBeVisible();
  expect(screen.getByText("后续纠正：中文示例必须保留。")).toBeVisible();
  expect(f.postControl).not.toHaveBeenCalled();
});

test.each(["summary", "instructions", "evidence"] as const)("rejects a %s response bound to another Run", async (kind) => {
  const f = fixture();
  if (kind === "summary") f.hooks.summary = () => ({ ...summary(), run_id: "foreign-run" });
  if (kind === "instructions") f.hooks.instructions = () => ({ ...instructions(), run_id: "foreign-run" });
  if (kind === "evidence") f.hooks.evidence = () => ({ ...evidence(), items: [{ ...evidence().items[0], session_id: "foreign-session" }] });
  mount(f);
  expect(await screen.findByText(/上下文记录的来源或格式不匹配/u)).toBeVisible();
  expect(screen.queryByText(kind === "summary" ? "保留最初目标：生成日志汇总，不能改接口。" : kind === "instructions" ? "AGENTS.md" : "README.md")).not.toBeInTheDocument();
});

test("task switch does not surface a late response or the previous task's local correction", async () => {
  const f = fixture(); let complete!: (value: RunContextSummary) => void;
  f.hooks.summary = (threadID) => threadID === "task-a" ? new Promise<RunContextSummary>((resolve) => { complete = resolve; }) : summary(threadID);
  const view = mount(f); const user = userEvent.setup();
  await screen.findByText("AGENTS.md");
  await user.type(screen.getByLabelText("需要继续保留的目标、限制或纠正"), "only task A");
  view.rerenderDetail(detail("task-b"));
  await screen.findByText("保留最初目标：生成日志汇总，不能改接口。");
  await act(async () => complete({ ...summary(), current_summary: { ...summary().current_summary!, content: "late old private task text" } }));
  expect(screen.getByLabelText("需要继续保留的目标、限制或纠正")).toHaveValue("");
  expect(screen.queryByText("late old private task text")).not.toBeInTheDocument();
  expect(screen.getByText("标题 task-b")).toBeVisible();
});

test("makes a truncated or redacted public summary explicit without inventing a complete model window", async () => {
  const f = fixture(); f.hooks.summary = () => ({ ...summary(), current_summary: { ...summary().current_summary!, content: "只剩公开片段", content_redacted: true, content_truncated: true } });
  mount(f);
  expect(await screen.findByText("只剩公开片段")).toBeVisible();
  expect(screen.getByText(/当前只展示部分摘要/u)).toBeVisible();
  expect(screen.getByText(/显示文本已脱敏/u)).toBeVisible();
  expect(screen.getByText(/查看此执行保存的项目指令、引用和摘要/u)).toBeVisible();
});

test("shows rolling inherited excerpts as readable history with explicit omissions and no execution action", async () => {
  const f = fixture();
  f.hooks.summary = () => {
    const value = summary();
    value.inherited_context!.summary_id = 0;
    value.inherited_context!.summary_content = JSON.stringify({ version: "thread_summary_window.v1", workspace_id: "workspace-a",
      instruction_authorized: false, lossy: true,
      sources: [{ source_id: "continuity:original", part: "summary", content_sha256: sha }, { source_id: "summary:8", part: "content", content_sha256: sha }],
      projection: { version: "handoff_memory.v1", records_omitted: 3, records: [{ content: "后续要求：保持离线，未完成导出验收。" }, { content: "工具原记录：导出检查失败，尚未重试。" }] } });
    return value;
  };
  mount(f);
  expect(await screen.findByText("后续要求：保持离线，未完成导出验收。")).toBeVisible();
  expect(screen.getByText("工具原记录：导出检查失败，尚未重试。")).toBeVisible();
  expect(screen.getByText(/较早内容仍保留，模型可按来源回读原文/u)).toBeVisible();
  expect(screen.getByText(/3 条条目省略/u)).toBeVisible();
  expect(f.postControl).not.toHaveBeenCalled();
});

test("accepts the existing absent-instruction shape without claiming it is a read failure", async () => {
  const f = fixture(); f.hooks.instructions = () => ({ run_id: "task-a-run", workspace_id: "workspace-a", capability_grant: false,
    stale: false, pinned_present: false, pinned: { run_id: "task-a-run", snapshot: { sources: null } } });
  mount(f);
  expect(await screen.findByText("此执行没有已固定的项目指令快照。")).toBeVisible();
  expect(screen.queryByText(/项目指令读取失败/u)).not.toBeInTheDocument();
});

test("uses a modal close control and Escape, returning focus when unmounted", async () => {
  const f = fixture(); const queries = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  const returnFocus = createRef<HTMLButtonElement>(); const close = vi.fn();
  const view = render(<><button ref={returnFocus}>上下文入口</button><QueryClientProvider client={queries}>
    <V2ThreadContext client={f.client} threadID="task-a" detail={detail()} onClose={close} onRequestChange={vi.fn()} returnFocusRef={returnFocus} />
  </QueryClientProvider></>);
  const modal = screen.getByRole("dialog", { name: "任务上下文" });
  expect(within(modal).getByRole("button", { name: "关闭任务上下文" })).toHaveFocus();
  await userEvent.setup().keyboard("{Escape}");
  expect(close).toHaveBeenCalledOnce();
  view.rerender(<button ref={returnFocus}>上下文入口</button>);
  await waitFor(() => expect(screen.getByRole("button", { name: "上下文入口" })).toHaveFocus());
});

function render(ui: ReactElement, options?: RenderOptions) {
  window.localStorage.setItem("prayu.locale.v1", "zh-CN");
  const Wrapper = options?.wrapper;
  return renderComponent(ui, { ...options, wrapper: ({ children }) => <LocaleProvider>
    {Wrapper ? <Wrapper>{children}</Wrapper> : children}
  </LocaleProvider> });
}
