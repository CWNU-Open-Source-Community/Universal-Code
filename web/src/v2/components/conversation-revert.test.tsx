import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { useState } from "react";
import type { APIClient } from "../../api/client";
import type { FileEditPreviewView, ThreadDetailView, WorkspaceView } from "../../api/types";
import { V2Conversation } from "./conversation";

vi.mock("../../hooks/use-run-event-stream", () => ({ useRunEventStream: () => ({ error: null, frames: [] }) }));
vi.mock("../../hooks/use-public-model-stream", () => ({ usePublicModelStream: () => ({ error: null, snapshot: null, status: "waiting" }) }));
vi.mock("./permission-control", () => ({ V2PermissionControl: () => null }));
vi.mock("./run-network-authority-control", () => ({ V2RunNetworkAuthorityControl: () => null }));
vi.mock("./model-route-control", () => ({ V2ModelRouteControl: () => null }));

const edit: FileEditPreviewView = {
  id: "edit-original", session_id: "session-original", workspace_id: "workspace-physical",
  path: "review.txt", operation: "replace", status: "applied",
  original_hash: "a".repeat(64), proposed_hash: "b".repeat(64),
  diff: "--- review.txt\n+++ review.txt\n-original private body\n+changed private body",
  allowed_actions: [], apply_enabled: false, secrets_redacted: false,
  created_at: "2026-09-09T00:00:00Z", updated_at: "2026-09-09T00:00:00Z",
};

function renderRevertConversation(history: boolean, unavailable?: "archived" | "readonly") {
  const current = { id: "run-current", status: history ? "running" : "paused" };
  const detail = { thread: { id: "thread-a", title: "Review task", workspace_id: "workspace-source",
    status: unavailable === "archived" ? "archived" : "active", composer_state: "ready" },
    active_run: current, last_run: current, mission: {}, runs: [
      ...(history ? [{ ordinal: 1, run: { id: "run-history", status: "completed" } }] : []),
      { ordinal: history ? 2 : 1, run: current },
    ],
  } as unknown as ThreadDetailView;
  const submitThreadTurn = vi.fn();
  const client = { hasThreadControl: unavailable !== "readonly", hasSessionMessages: true,
    hasFileEditReview: true, submitThreadTurn, controlRunLifecycle: vi.fn(), createFileEditRevertProposal: vi.fn(), applyFileEdit: vi.fn(),
    get: vi.fn().mockResolvedValue(detail), getPage: vi.fn().mockResolvedValue({ items: [], page: { limit: 100 } }),
    fileEditQueue: vi.fn().mockResolvedValue({ items: [edit], apply_enabled: false, truncated: false }),
    fileEditChangeSet: vi.fn().mockResolvedValue({ workspace_id: edit.workspace_id, applied_count: 1,
      returned_count: 1, proposed_count: 0, approved_count: 0, denied_count: 0, failed_count: 0, total_diff_bytes: 70 }),
    repositoryDiff: vi.fn().mockResolvedValue({ available: false }),
  } as unknown as APIClient;
  const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false, staleTime: Infinity }, mutations: { retry: false } } });
  function Harness() {
    const [draft, setDraft] = useState("Preserve my existing request.");
    return <QueryClientProvider client={queryClient}><V2Conversation client={client} threadID="thread-a"
      workspaces={[{ id: "workspace-source", name: "Source" }] as WorkspaceView[]}
      draft={draft} onDraftChange={setDraft} onArchive={vi.fn()} onManageModels={vi.fn()} onOpenInspector={vi.fn()} />
    </QueryClientProvider>;
  }
  render(<Harness />);
  return client;
}

async function openEditHistory(user: ReturnType<typeof userEvent.setup>) {
  await user.click(await screen.findByRole("button", { name: "审阅改动" }));
  await user.click(await screen.findByRole("button", { name: "记录与恢复" }));
  await user.click(await screen.findByRole("button", { name: "编辑明细" }));
}

it.each([false, true])("drafts an exact revert from %s historical selection without sending or resuming", async (history) => {
  const client = renderRevertConversation(history);
  const user = userEvent.setup();
  const input = await screen.findByRole("textbox", { name: "继续对话" });
  await openEditHistory(user);
  if (history) await user.selectOptions(screen.getByRole("combobox", { name: "选择审阅的执行记录" }), "run-history");
  await waitFor(() => expect(client.fileEditQueue).toHaveBeenCalledWith(history ? "run-history" : "run-current", expect.any(AbortSignal)));
  await user.click(await screen.findByRole("button", { name: /review.txt.*applied/ }));
  await user.click(screen.getByRole("button", { name: "Close review" }));
  expect(input).toHaveValue("Preserve my existing request.");
  expect(client.createFileEditRevertProposal).not.toHaveBeenCalled();
  await user.click(screen.getByRole("button", { name: /review.txt.*applied/ }));
  await user.click(screen.getByRole("button", { name: "Revert this edit in conversation" }));
  await waitFor(() => expect(screen.queryByRole("dialog", { name: "审阅任务改动" })).not.toBeInTheDocument());
  await waitFor(() => expect(input).toHaveFocus());
  const content = (input as HTMLTextAreaElement).value;
  expect(content).toMatch(/^Preserve my existing request\.\n\n/);
  expect(content).toContain(`请撤销 ${edit.path} 的这次已应用编辑。`);
  expect(content).toContain("等我批准后再应用；保留此后用户修改");
  expect(content).toContain(`来源执行：${history ? "run-history" : "run-current"}`);
  expect(content).toContain(`编辑记录：${edit.id}`);
  expect(content).toContain(`来源目录：${edit.workspace_id}`);
  expect(content).toContain(`预期当前版本：${edit.proposed_hash}`);
  expect(content).not.toMatch(/workspace_change|propose_revert|agent-code-tools|请求参数|\{|\}/);
  expect(content).not.toContain(edit.original_hash);
  expect(content).not.toContain("private body");
  fireEvent.change(input, { target: { value: `${content}\nAdditional user instruction` } });
  expect(input).toHaveValue(`${content}\nAdditional user instruction`);
  expect(client.submitThreadTurn).not.toHaveBeenCalled();
  expect(client.controlRunLifecycle).not.toHaveBeenCalled();
  expect(client.createFileEditRevertProposal).not.toHaveBeenCalled();
  expect(client.applyFileEdit).not.toHaveBeenCalled();
});

it.each(["archived", "readonly"] as const)("explains why %s cannot initiate a revert conversation", async (unavailable) => {
  const client = renderRevertConversation(false, unavailable);
  const user = userEvent.setup();
  await openEditHistory(user);
  await user.click(await screen.findByRole("button", { name: /review.txt.*applied/ }));
  expect(screen.getByRole("button", { name: "Revert this edit in conversation" })).toBeDisabled();
  expect(screen.getByText(unavailable === "archived" ? "此对话已归档，取消归档后才能发送撤销要求。"
    : "当前连接不能发送对话控制请求，无法发起撤销。")).toBeInTheDocument();
  expect(client.submitThreadTurn).not.toHaveBeenCalled();
});

// Retain textarea coverage through the supported plain-text editing mode.
beforeEach(() => localStorage.setItem("universal-code.composer-plain-text", "true"));
