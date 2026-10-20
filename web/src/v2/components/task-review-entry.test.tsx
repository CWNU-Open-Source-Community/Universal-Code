import type { ReactElement } from "react";
import type { RenderOptions } from "@testing-library/react";
import { LocaleProvider } from "../../lib/locale";
import { createRef, useRef, useState } from "react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act, render as renderComponent, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import type { APIClient } from "../../api/client";
import type { GitHubReviewWriteReviewResultView, ThreadDetailView } from "../../api/types";
import type { TaskReviewToolMemory } from "./task-review-tools";
import { V2TaskReview } from "./task-review";

const retainedCallbacks = vi.hoisted(() => new Map<string, (review: unknown) => void>());
vi.mock("./task-overview", () => ({ TaskOverview: () => <div>Task overview fixture</div> }));

vi.mock("../../components/github-review-panel", () => ({ GitHubReviewPanel: ({ runID, retainedReview, onRetainedReviewChange }: {
  runID: string; retainedReview?: GitHubReviewWriteReviewResultView | null;
  onRetainedReviewChange: (review: GitHubReviewWriteReviewResultView) => void;
}) => {
  retainedCallbacks.set(runID, (review) => onRetainedReviewChange(review as GitHubReviewWriteReviewResultView));
  return <section aria-label="GitHub review fixture"><output aria-label="Exact GitHub Run">{runID}</output>
  <output aria-label="Retained review fixture">{retainedReview ? "retained" : "empty"}</output>
  <button type="button" onClick={() => onRetainedReviewChange({ operation: { run_id: runID } } as unknown as GitHubReviewWriteReviewResultView)}>Retain fixture review</button>
</section>; } }));

it("opens the requested Run's GitHub tool and retains one review across closure, while a Run switch clears it", async () => {
  const client = {} as APIClient;
  const run = { id: "run-current", status: "completed" };
  const detail = { thread: { id: "thread-source", title: "Source task", workspace_id: "workspace-source" },
    last_run: run, runs: [{ ordinal: 1, run: { id: "run-history", status: "completed" } }, { ordinal: 2, run }] } as ThreadDetailView;
  function Harness() {
    const [open, setOpen] = useState(true);
    const [initialTarget, setInitialTarget] = useState(true);
    const memory = useRef<TaskReviewToolMemory>({ generation: 0 });
    return <><button type="button" onClick={() => setOpen(true)}>Reopen source review</button>
      {open && <V2TaskReview client={client} detail={detail} working={false} onClose={() => { setOpen(false); setInitialTarget(false); }}
        onRequestChange={vi.fn()} returnFocusRef={createRef()} toolMemoryRef={memory}
        initialToolTarget={initialTarget ? { runID: "run-history", tool: "github-review" } : undefined} />}</>;
  }
  render(<QueryClientProvider client={new QueryClient({ defaultOptions: { queries: { retry: false } } })}><Harness /></QueryClientProvider>);
  const user = userEvent.setup();
  expect(await screen.findByLabelText("Exact GitHub Run")).toHaveTextContent("run-history");
  await user.click(screen.getByRole("button", { name: "Retain fixture review" }));
  await user.click(screen.getByRole("button", { name: "关闭任务审阅" }));
  await user.click(screen.getByRole("button", { name: "Reopen source review" }));
  await user.click(screen.getByRole("button", { name: "更多交付工具" }));
  expect(screen.getByRole("combobox", { name: "选择审阅的执行记录" })).toHaveValue("run-history");
  await user.click(screen.getByRole("button", { name: /GitHub 审阅/u }));
  expect(await screen.findByLabelText("Retained review fixture")).toHaveTextContent("retained");
  const lateHistoryCallback = retainedCallbacks.get("run-history")!;
  await user.selectOptions(screen.getByRole("combobox", { name: "选择审阅的执行记录" }), "run-current");
  await user.click(screen.getByRole("button", { name: /GitHub 审阅/u }));
  expect(await screen.findByLabelText("Exact GitHub Run")).toHaveTextContent("run-current");
  expect(screen.getByLabelText("Retained review fixture")).toHaveTextContent("empty");
  await act(async () => lateHistoryCallback({ operation: { run_id: "run-history" } }));
  await user.selectOptions(screen.getByRole("combobox", { name: "选择审阅的执行记录" }), "run-history");
  await user.click(screen.getByRole("button", { name: /GitHub 审阅/u }));
  expect(await screen.findByLabelText("Exact GitHub Run")).toHaveTextContent("run-history");
  expect(screen.getByLabelText("Retained review fixture")).toHaveTextContent("empty");
});

it("leaves a missing requested Run explicit and does not mount GitHub tools for another Run", () => {
  const run = { id: "run-current", status: "completed" };
  const detail = { thread: { id: "thread-source", title: "Source task" }, last_run: run,
    runs: [{ ordinal: 1, run }] } as ThreadDetailView;
  render(<QueryClientProvider client={new QueryClient()}><V2TaskReview client={{} as APIClient} detail={detail}
    working={false} onClose={vi.fn()} onRequestChange={vi.fn()} returnFocusRef={createRef()}
    initialToolTarget={{ runID: "run-missing", tool: "github-review" }} /></QueryClientProvider>);
  expect(screen.getByRole("alert")).toHaveTextContent("无法找到目标执行记录 run-missing");
  expect(screen.queryByRole("region", { name: "GitHub review fixture" })).not.toBeInTheDocument();
});

function render(ui: ReactElement, options?: RenderOptions) {
  localStorage.setItem("prayu.locale.v1", "zh-CN");
  const Wrapper = options?.wrapper;
  return renderComponent(ui, {...options, wrapper: ({children}) => <LocaleProvider>{Wrapper ? <Wrapper>{children}</Wrapper> : children}</LocaleProvider>});
}
