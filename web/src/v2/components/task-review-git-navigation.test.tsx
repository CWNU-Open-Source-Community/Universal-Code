import type { ReactElement } from "react";
import type { RenderOptions } from "@testing-library/react";
import { LocaleProvider } from "../../lib/locale";
import { createRef, useState } from "react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { cleanup, render as renderComponent, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import type { APIClient } from "../../api/client";
import type { ThreadDetailView } from "../../api/types";
import type { ThreadGitPreview, ThreadGitSpec, ThreadGitState } from "../../api/task-delivery";
import { V2RecoveryProvider } from "../recovery-storage";
import { V2TaskReview } from "./task-review";

// The actual review navigation, Git and PR panels remain mounted by their real owner.
// Unrelated task-overview fetching is outside this navigation regression.
vi.mock("./task-overview", () => ({ TaskOverview: () => <p>任务改动总览</p>, taskReviewKey: (id: string) => ["thread", id, "review"] }));

function state(threadID: string, fingerprint = "a".repeat(64)): ThreadGitState {
  return { version: "thread_git.v1", thread_id: threadID, run_id: `${threadID}-run`, session_id: `${threadID}-session`,
    workspace_id: `${threadID}-workspace`, source_workspace_id: `${threadID}-workspace`, repository_root: `D:/fixture/${threadID}`,
    branch: "feature/current", head_oid: "b".repeat(40), binding_fingerprint: fingerprint,
    branches: ["feature/current", "main"], remotes: [], can_execute: true, truncated: false,
    changes: [{ path: "chosen.txt", staging: "unmodified", worktree: "modified" },
      { path: "unrelated.txt", staging: "modified", worktree: "unmodified" }] };
}

afterEach(() => { cleanup(); window.localStorage.clear(); });

it("preserves selections through actual Git/PR tabs, review closure and task changes while dropping the old confirmation", async () => {
  let fingerprint = "a".repeat(64);
  const get = vi.fn(async (path: string) => {
    const match = /^\/threads\/([^/]+)\/git$/u.exec(path);
    if (!match) throw new Error(`Unexpected GET ${path}`);
    return state(match[1], fingerprint);
  });
  const postControl = vi.fn(async (path: string, body: { spec: ThreadGitSpec }) => {
    const match = /^\/threads\/([^/]+)\/git\/preview$/u.exec(path);
    if (!match) throw new Error(`Unexpected write ${path}`);
    return { ...state(match[1], fingerprint), spec: body.spec, diff: "--- a/chosen.txt\n+++ b/chosen.txt\n@@ -1 +1 @@\n-old\n+new",
      preview_fingerprint: "c".repeat(64) } satisfies ThreadGitPreview;
  });
  const client = { baseURL: "/api/v1", hasControl: true, hasGitHubReviewControl: false, get, postControl } as unknown as APIClient;
  const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false, staleTime: 30_000 }, mutations: { retry: false } } });
  function Harness() {
    const [threadID, setThreadID] = useState("task-a");
    const [open, setOpen] = useState(true);
    const run = { id: `${threadID}-run`, status: "running" };
    const detail = { thread: { id: threadID, title: threadID, workspace_id: `${threadID}-workspace` },
      active_run: run, last_run: run, runs: [{ ordinal: 1, run }] } as ThreadDetailView;
    return <><button onClick={() => setOpen(true)}>打开审阅</button>
      <button onClick={() => setThreadID(threadID === "task-a" ? "task-b" : "task-a")}>切换任务</button>
      {open && <V2TaskReview key={threadID} client={client} detail={detail} working={false} onClose={() => setOpen(false)}
        onRequestChange={vi.fn()} returnFocusRef={createRef()} />}</>;
  }
  const view = render(<QueryClientProvider client={queryClient}>
    <V2RecoveryProvider client={client} scopeID="git-review-navigation-fixture"><Harness /></V2RecoveryProvider>
  </QueryClientProvider>);
  const user = userEvent.setup();
  const openGit = async () => {
    await user.click(screen.getByRole("button", { name: "提交与推送" }));
    await waitFor(() => expect(screen.getByRole("button", { name: "刷新仓库" })).toBeEnabled());
  };
  const chosen = () => screen.getByRole("checkbox", { name: /^chosen\.txt/u });
  await openGit();
  await user.click(chosen());
  await user.type(screen.getByRole("textbox", { name: "提交说明" }), "保留本任务的提交说明");
  await user.click(screen.getByRole("button", { name: "预览本次操作" }));
  await screen.findByRole("region", { name: "Git 操作确认" });
  await user.click(screen.getByRole("button", { name: "PR 状态" }));
  fingerprint = "d".repeat(64);
  await openGit();
  expect(chosen()).toBeChecked();
  expect(screen.getByRole("checkbox", { name: /^unrelated\.txt/u })).not.toBeChecked();
  expect(screen.getByRole("textbox", { name: "提交说明" })).toHaveValue("保留本任务的提交说明");
  expect(screen.queryByRole("region", { name: "Git 操作确认" })).not.toBeInTheDocument();
  await user.click(screen.getByRole("button", { name: "关闭任务审阅" }));
  await user.click(screen.getByRole("button", { name: "打开审阅" }));
  await openGit();
  expect(chosen()).toBeChecked();
  await user.click(screen.getByRole("button", { name: "关闭任务审阅" }));
  await user.click(screen.getByRole("button", { name: "切换任务" }));
  await user.click(screen.getByRole("button", { name: "打开审阅" }));
  await openGit();
  expect(chosen()).not.toBeChecked();
  expect(screen.getByRole("textbox", { name: "提交说明" })).toHaveValue("");
  await user.click(screen.getByRole("button", { name: "关闭任务审阅" }));
  await user.click(screen.getByRole("button", { name: "切换任务" }));
  await user.click(screen.getByRole("button", { name: "打开审阅" }));
  await openGit();
  expect(chosen()).toBeChecked();
  expect(screen.getByRole("textbox", { name: "提交说明" })).toHaveValue("保留本任务的提交说明");
  expect(postControl).toHaveBeenCalledTimes(1);
  expect(postControl.mock.calls[0][0]).toBe("/threads/task-a/git/preview");
  expect(get.mock.calls.filter(([path]) => path === "/threads/task-a/git").length).toBeGreaterThanOrEqual(4);
  view.unmount(); queryClient.clear();
});

function render(ui: ReactElement, options?: RenderOptions) {
  localStorage.setItem("prayu.locale.v1", "zh-CN");
  const Wrapper = options?.wrapper;
  return renderComponent(ui, {...options, wrapper: ({children}) => <LocaleProvider>{Wrapper ? <Wrapper>{children}</Wrapper> : children}</LocaleProvider>});
}
