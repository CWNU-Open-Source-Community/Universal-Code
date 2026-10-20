import type { ReactElement } from "react";
import type { RenderOptions } from "@testing-library/react";
import { LocaleProvider } from "../../lib/locale";
import { createRef, useState, type ReactNode } from "react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { render as renderComponent, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import type { APIClient } from "../../api/client";
import type { FileEditPreviewView, ThreadDetailView } from "../../api/types";
import type { ThreadReview, ThreadReviewChange } from "../../api/task-delivery";
import type { FileEditReviewTarget } from "../../components/file-edit-panel";
import { V2TaskReview } from "./task-review";

const when = "2026-09-29T00:00:00Z";
function detail(): ThreadDetailView {
  return { thread: { id: "task", title: "文件提案", workspace_id: "source" },
    active_run: { id: "run-current", status: "waiting_approval" }, last_run: { id: "run-current", status: "waiting_approval" },
    runs: [{ ordinal: 1, run: { id: "run-history", status: "failed" } },
      { ordinal: 2, run: { id: "run-current", status: "waiting_approval" } }] } as ThreadDetailView;
}
function change(runID: string): ThreadReviewChange {
  return { run_id: runID, session_id: `${runID}-session`, workspace_id: `${runID}-workspace`, edit_id: `${runID}-edit`,
    operation: "create", path: `${runID}.txt`, status: "proposed", original_sha256: "missing", proposed_sha256: "b".repeat(64),
    current_match: "missing", diff: `--- /dev/null\n+++ ${runID}.txt\n@@ -0,0 +1 @@\n+${runID}`,
    diff_truncated: false, redacted: false, updated_at: when };
}
function review(): ThreadReview {
  return { thread_id: "task", thread_version: 1, current_run_id: "run-current", observed_at: when,
    change_scope: "recorded_file_edits", total_runs: 2, runs: [], applied_changes: [],
    unapplied_changes: [change("run-history"), change("run-current")], checks: [], partial: false, reasons: [],
    target: { state: "available", source_workspace_id: "source", workspace_id: "run-current-workspace", kind: "drydock", root_path: "D:/fixture/current" },
    revision: { state: "available", repository_kind: "none", reasons: [] } };
}
function edit(runID: string): FileEditPreviewView {
  const value = change(runID);
  return { id: value.edit_id, path: value.path, workspace_id: value.workspace_id, session_id: value.session_id,
    operation: "create", status: "proposed", allowed_actions: ["approve_intent", "deny"], apply_enabled: false,
    original_hash: "missing", proposed_hash: value.proposed_sha256, diff: value.diff,
    secrets_redacted: false, created_at: when, updated_at: when };
}
function client() {
  return { hasFileEditReview: false, hasFileEditApply: false, get: vi.fn().mockResolvedValue(review()),
    fileEditQueue: vi.fn((runID: string) => Promise.resolve({ items: [edit(runID)], truncated: false, apply_enabled: false })),
    fileEditChangeSet: vi.fn((runID: string) => Promise.resolve({ workspace_id: `${runID}-workspace`, proposed_count: 1,
      approved_count: 0, applied_count: 0, denied_count: 0, failed_count: 0, returned_count: 1, total_diff_bytes: 0 })),
    fileEdit: vi.fn((runID: string) => Promise.resolve(edit(runID))),
    reviewFileEdit: vi.fn(), applyFileEdit: vi.fn(), controlRunLifecycle: vi.fn(),
  };
}
function provider(children: ReactNode) {
  return <QueryClientProvider client={new QueryClient({ defaultOptions: { queries: { retry: false }, mutations: { retry: false } } })}>{children}</QueryClientProvider>;
}

it.each(["run-current", "run-history"])("opens the initial exact %s target with Escape returning to its trigger", async (runID) => {
  const api = client();
  const trigger = createRef<HTMLButtonElement>();
  const initialFileTarget = { runID, editID: `${runID}-edit`, workspaceID: `${runID}-workspace` };
  function Harness() {
    const [open, setOpen] = useState(false);
    return <><button ref={trigger} onClick={() => setOpen(true)}>查看此提案</button>
      {open && <V2TaskReview client={api as unknown as APIClient} detail={detail()} working={false}
        initialFileTarget={initialFileTarget} onClose={() => setOpen(false)} onRequestChange={vi.fn()} returnFocusRef={trigger} />}</>;
  }
  render(provider(<Harness />));
  const user = userEvent.setup();
  await user.click(screen.getByRole("button", { name: "查看此提案" }));
  expect(await screen.findByRole("complementary", { name: `审阅 ${runID}.txt` })).toBeInTheDocument();
  expect(screen.getByRole("combobox", { name: "选择审阅的执行记录" })).toHaveValue(runID);
  expect(api.fileEdit).toHaveBeenCalledWith(runID, `${runID}-edit`, expect.any(AbortSignal));
  expect(api.fileEditQueue.mock.calls.every(([id]) => id === runID)).toBe(true);
  expect(api.reviewFileEdit).not.toHaveBeenCalled(); expect(api.applyFileEdit).not.toHaveBeenCalled();
  expect(api.controlRunLifecycle).not.toHaveBeenCalled();
  await user.keyboard("{Escape}");
  expect(screen.queryByRole("dialog", { name: "审阅任务改动" })).not.toBeInTheDocument();
  expect(trigger.current).toHaveFocus();
});

it("takes each default overview proposal straight to its own run and edit without a mutation", async () => {
  const api = client();
  render(provider(<V2TaskReview client={api as unknown as APIClient} detail={detail()} working={false}
    onClose={vi.fn()} onRequestChange={vi.fn()} returnFocusRef={createRef()} />));
  const user = userEvent.setup();
  await screen.findByText("当前执行的提案");
  const actions = screen.getAllByRole("button", { name: "查看并审阅" });
  expect(within(actions[0].closest("article")!).getByText("run-current.txt")).toBeInTheDocument();
  await user.click(actions[0]);
  expect(await screen.findByRole("complementary", { name: "审阅 run-current.txt" })).toBeInTheDocument();
  await user.click(screen.getByRole("button", { name: "返回任务改动" }));
  const historical = (await screen.findByText("run-history.txt")).closest("article")!;
  await user.click(within(historical).getByRole("button", { name: "查看并审阅" }));
  expect(await screen.findByRole("complementary", { name: "审阅 run-history.txt" })).toBeInTheDocument();
  expect(screen.getByRole("combobox", { name: "选择审阅的执行记录" })).toHaveValue("run-history");
  expect(api.fileEdit.mock.calls.map(([runID]) => runID)).toEqual(["run-current", "run-history"]);
  expect(api.reviewFileEdit).not.toHaveBeenCalled(); expect(api.applyFileEdit).not.toHaveBeenCalled();
  expect(api.controlRunLifecycle).not.toHaveBeenCalled();
});

it("keeps a missing or removed target run explicit instead of opening the current run", async () => {
  const api = client();
  const initialFileTarget: FileEditReviewTarget = { runID: "run-missing", editID: "missing-edit", workspaceID: "missing-workspace" };
  render(provider(<V2TaskReview client={api as unknown as APIClient} detail={detail()} working={false}
    initialFileTarget={initialFileTarget} onClose={vi.fn()} onRequestChange={vi.fn()} returnFocusRef={createRef()} />));
  expect(await screen.findByText(/无法找到目标执行记录 run-missing/)).toBeInTheDocument();
  expect(screen.getByRole("combobox", { name: "选择审阅的执行记录" })).toHaveValue("run-missing");
  expect(api.fileEditQueue).not.toHaveBeenCalled(); expect(api.fileEdit).not.toHaveBeenCalled();
  await userEvent.click(screen.getByRole("button", { name: "编辑明细" }));
  expect(screen.getByText(/无法找到目标执行记录 run-missing/)).toBeInTheDocument();
  expect(api.fileEditQueue).not.toHaveBeenCalled();
  await userEvent.click(screen.getByRole("button", { name: "检查与交付" }));
  expect(screen.getByText(/无法找到目标执行记录 run-missing/)).toBeInTheDocument();
  expect(api.get).not.toHaveBeenCalled();
  await userEvent.click(screen.getByRole("button", { name: "编辑明细" }));
  await userEvent.selectOptions(screen.getByRole("combobox", { name: "选择审阅的执行记录" }), "run-current");
  await waitFor(() => expect(api.fileEditQueue).toHaveBeenCalledWith("run-current", expect.any(AbortSignal)));
  expect(screen.queryByRole("complementary")).not.toBeInTheDocument();
  expect(api.fileEdit).not.toHaveBeenCalled();
  expect(api.reviewFileEdit).not.toHaveBeenCalled(); expect(api.applyFileEdit).not.toHaveBeenCalled();
});

it("does not replace an opened historical target after its run disappears from refreshed thread detail", async () => {
  const api = client();
  const target = { runID: "run-history", editID: "run-history-edit", workspaceID: "run-history-workspace" };
  const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  const component = (value: ThreadDetailView) => <QueryClientProvider client={queryClient}>
    <V2TaskReview client={api as unknown as APIClient} detail={value} working={false}
      initialFileTarget={target} onClose={vi.fn()} onRequestChange={vi.fn()} returnFocusRef={createRef()} />
  </QueryClientProvider>;
  const view = render(component(detail()));
  await screen.findByRole("complementary", { name: "审阅 run-history.txt" });
  const refreshed = detail(); refreshed.runs = refreshed.runs.filter(({ run }) => run.id !== "run-history");
  view.rerender(component(refreshed));
  expect(screen.getByText(/无法找到目标执行记录 run-history/)).toBeInTheDocument();
  expect(screen.queryByRole("complementary")).not.toBeInTheDocument();
  expect(api.fileEditQueue.mock.calls.map(([runID]) => runID)).toEqual(["run-history"]);
  expect(api.fileEdit.mock.calls.map(([runID]) => runID)).toEqual(["run-history"]);
  expect(api.reviewFileEdit).not.toHaveBeenCalled(); expect(api.applyFileEdit).not.toHaveBeenCalled();
});

function render(ui: ReactElement, options?: RenderOptions) {
  localStorage.setItem("prayu.locale.v1", "zh-CN");
  const Wrapper = options?.wrapper;
  return renderComponent(ui, {...options, wrapper: ({children}) => <LocaleProvider>{Wrapper ? <Wrapper>{children}</Wrapper> : children}</LocaleProvider>});
}
