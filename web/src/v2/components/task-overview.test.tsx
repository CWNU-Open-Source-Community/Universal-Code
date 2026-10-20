import type { ReactElement } from "react";
import type { RenderOptions } from "@testing-library/react";
import { LocaleProvider } from "../../lib/locale";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { render as renderComponent, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import type { APIClient } from "../../api/client";
import type { ThreadReview, ThreadReviewChange } from "../../api/task-delivery";
import { TaskOverview, taskReviewKey } from "./task-overview";

const when = "2026-09-11T00:00:00Z";
function change(overrides: Partial<ThreadReviewChange> = {}): ThreadReviewChange {
  return { run_id: "old-run", session_id: "old-session", workspace_id: "physical-workspace", edit_id: "old-edit",
    operation: "replace", path: "src/app.ts", status: "applied", original_sha256: "a".repeat(64), proposed_sha256: "b".repeat(64),
    current_sha256: "c".repeat(64), current_match: "changed", diff: "--- a/src/app.ts\n+++ b/src/app.ts\n@@ -1 +1 @@\n-old\n+new",
    diff_truncated: false, redacted: false, updated_at: when, ...overrides };
}
function review(overrides: Partial<ThreadReview> = {}): ThreadReview {
  return { thread_id: "task", thread_version: 5, current_run_id: "new-run", observed_at: when, change_scope: "recorded_file_edits", total_runs: 2,
    runs: [{ run_id: "new-run", session_id: "new-session", ordinal: 2, source_event_sequence: 20, handoff_url: "/new" },
      { run_id: "old-run", session_id: "old-session", ordinal: 1, source_event_sequence: 10, handoff_url: "/old" }],
    target: { state: "available", source_workspace_id: "source-workspace", workspace_id: "physical-workspace", kind: "drydock", root_path: "D:/fixture/owned" },
    revision: { state: "available", repository_kind: "git", head: "d".repeat(40), branch: "feature", dirty: true, revision_sha256: "e".repeat(64), reasons: [] },
    applied_changes: [change()], unapplied_changes: [change({ run_id: "new-run", session_id: "new-session", edit_id: "pending-edit", path: "pending.txt", status: "approved", current_match: "missing" })],
    checks: [{ run_id: "old-run", id: "receipt", source_kind: "host_command", title: "npm test", outcome: "succeeded", exit_code: 0,
      revision_state: "unbound", reason: "execution_receipt_has_no_workspace_revision_binding", recorded_at: when, handoff_url: "/old" }], partial: false, reasons: [], ...overrides };
}
function setup(value: ThreadReview, queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } })) {
  const get = vi.fn().mockResolvedValue(value);
  const feedback = vi.fn();
  const client = { get } as unknown as APIClient;
  render(<QueryClientProvider client={queryClient}><TaskOverview client={client} threadID="task" onFeedback={feedback} /></QueryClientProvider>);
  return { get, feedback, queryClient };
}

it("shows cross-Run applied history separately and keeps unbound checks from proving current code", async () => {
  const { get, feedback } = setup(review());
  await screen.findByText("owned");
  await userEvent.setup().click(screen.getByText("目录与版本详情"));
  expect(screen.getByText("D:/fixture/owned")).toBeVisible();
  expect(get).toHaveBeenCalledWith("/threads/task/review", {}, expect.any(AbortSignal));
  expect(screen.getByText(/覆盖 2 \/ 2 次执行/)).toBeInTheDocument();
  expect(screen.getByText("未绑定代码版本")).toHaveClass("unbound");
  expect(screen.queryByText("适用于当前版本")).not.toBeInTheDocument();
  expect(screen.getByText("检查记录未绑定代码版本。请对当前代码重新运行检查，再确认结果。")).toBeInTheDocument();
  expect(screen.getByText("此后已有修改")).toBeInTheDocument();
  expect(screen.getByText("当前文件不存在")).toBeInTheDocument();
  const old = screen.getByText("src/app.ts").closest("article")!;
  await userEvent.setup().click(within(old).getByRole("button", { name: "引用文件" }));
  expect(feedback).toHaveBeenCalledWith(expect.stringContaining("来源执行：old-run"));
  expect(feedback).toHaveBeenCalledWith(expect.stringContaining("编辑记录：old-edit"));
  expect(feedback).toHaveBeenCalledWith(expect.stringContaining("当前观测版本：" + "c".repeat(64)));
  expect(feedback).toHaveBeenCalledWith(expect.stringContaining("任务：task"));
  expect(feedback).not.toHaveBeenCalledWith(expect.stringContaining("来源执行：new-run"));
});

it("explains incomplete attribution and a non-Git directory without claiming no changes", async () => {
  setup(review({ partial: true, reasons: ["older_runs_omitted"], total_runs: 53, applied_changes: [], unapplied_changes: [], checks: [],
    revision: { state: "available", repository_kind: "none", reasons: [] } }));
  await screen.findByText("尚未使用 Git");
  expect(screen.getByText(/请在执行记录和提交页面核对其余改动与检查/)).toBeInTheDocument();
  expect(screen.getByText(/尚无已记录的应用编辑。工作目录的其他改动可在提交页面核对/)).toBeInTheDocument();
  expect(screen.getByText(/命令、手动编辑等产生的其他变化，请在提交页面核对/)).toBeInTheDocument();
  expect(screen.getByText(/覆盖 2 \/ 53 次执行/)).toBeInTheDocument();
});

it("labels cached results as unconfirmed after a failed refresh", async () => {
  const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false, staleTime: 5000 } } });
  const cached = review();
  cached.checks[0] = { ...cached.checks[0], revision_state: "current", recorded_revision_sha256: "e".repeat(64) };
  queryClient.setQueryData(taskReviewKey("task"), cached);
  const get = vi.fn().mockRejectedValue(new Error("review unavailable"));
  render(<QueryClientProvider client={queryClient}><TaskOverview client={{ get } as unknown as APIClient} threadID="task" onFeedback={vi.fn()} /></QueryClientProvider>);
  await screen.findByText("以下为上次读取的结果，当前状态尚未确认。");
  await waitFor(() => expect(get).toHaveBeenCalledOnce());
  expect(screen.getByText("src/app.ts")).toBeInTheDocument();
  expect(screen.queryByText("适用于当前版本")).not.toBeInTheDocument();
});

it("does not describe an unapplied proposal as a later modification or an applied edit", async () => {
  setup(review({ applied_changes: [], unapplied_changes: [change({ path: "changed-proposal.txt", status: "proposed", current_match: "changed" }),
    change({ edit_id: "same-content", path: "same-proposal.txt", status: "approved", current_match: "matches" })] }));
  await screen.findByText("当前内容尚不同于提案");
  expect(screen.getByText("当前内容与提案相同，仍以应用记录为准")).toBeInTheDocument();
  expect(screen.queryByText("此后已有修改")).not.toBeInTheDocument();
  expect(screen.getByText(/尚无已记录的应用编辑/)).toBeInTheDocument();
});

it("surfaces delivery actions and distinguishes current run checks from historical checks", async () => {
  const onGit = vi.fn();
  const onPR = vi.fn();
  const onChecks = vi.fn();
  const rev = review({
    checks: [
      { run_id: "new-run", id: "check-1", source_kind: "test", title: "Unit Tests", outcome: "passed", reason: "passed",
        revision_state: "current", recorded_at: when, handoff_url: "/new" },
      { run_id: "old-run", id: "check-2", source_kind: "lint", title: "ESLint", outcome: "failed", reason: "failed",
        revision_state: "stale", recorded_at: when, handoff_url: "/old" },
    ],
  });
  const client = { get: vi.fn().mockResolvedValue(rev) } as unknown as APIClient;
  const user = userEvent.setup();
  render(
    <QueryClientProvider client={new QueryClient({ defaultOptions: { queries: { retry: false } } })}>
      <TaskOverview client={client} threadID="task" onFeedback={vi.fn()} onGit={onGit} onPullRequest={onPR} onChecks={onChecks} />
    </QueryClientProvider>
  );

  expect(await screen.findByText("Unit Tests")).toBeInTheDocument();
  expect(screen.getByText("当前执行")).toBeInTheDocument();
  expect(screen.getByText("历史执行")).toBeInTheDocument();

  await user.click(screen.getByRole("button", { name: /PR 状态与交付/ }));
  expect(onPR).toHaveBeenCalledOnce();

  await user.click(screen.getByRole("button", { name: /完整检查与环境/ }));
  expect(onChecks).toHaveBeenCalledOnce();

  await user.click(screen.getByRole("button", { name: /选择文件并提交/ }));
  expect(onGit).toHaveBeenCalledOnce();
});

function render(ui: ReactElement, options?: RenderOptions) {
  localStorage.setItem("prayu.locale.v1", "zh-CN");
  const Wrapper = options?.wrapper;
  return renderComponent(ui, {...options, wrapper: ({children}) => <LocaleProvider>{Wrapper ? <Wrapper>{children}</Wrapper> : children}</LocaleProvider>});
}
