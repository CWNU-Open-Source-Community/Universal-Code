import { QueryClient, QueryClientProvider, type InfiniteData } from "@tanstack/react-query";
import { act, cleanup, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import type { ReactNode } from "react";
import { afterEach, beforeAll, describe, expect, it, vi } from "vitest";

import type { APIClient } from "../api/client";
import type { RunDetailView, ThreadView, WorkspaceView } from "../api/types";
import { LocaleProvider } from "../lib/locale";
import { useConnectionStore } from "../state/connection";
import { V2Workbench } from "./app";
import { v2QueryKeys } from "./query-keys";

// Keep the recovery callback chain real: Workbench -> InspectorTools ->
// RunWorkspace -> ContextContinuityPanel / WorkspaceCheckpointPanel.
vi.mock("../components/workbench-frame", () => ({
  WorkbenchFrame: ({ children }: { children: ReactNode }) => <div>{children}</div>,
}));
vi.mock("./components/conversation", () => ({
  V2Conversation: ({ threadID }: { threadID: string }) => <output>{threadID}</output>,
}));
vi.mock("../hooks/use-run-event-stream", () => ({
  useRunEventStream: () => ({ frames: [], status: "connected", error: "" }),
}));
vi.mock("../hooks/use-public-model-stream", () => ({
  usePublicModelStream: () => ({ snapshot: null, status: "disabled", error: null }),
}));
vi.mock("../components/run-activity-timeline", () => ({
  RunActivityTimeline: ({ activity }: { activity: { run_id: string } }) =>
    <output aria-label="Activity Run">{activity.run_id}</output>,
}));
vi.mock("../components/workspace-explorer", () => ({ WorkspaceExplorer: () => null }));

beforeAll(async () => {
  // This suite exercises real recovery callbacks and route ownership. Await
  // real lazy modules during setup so Vite's first transformation is not part
  // of the first mutation/navigation deadline. First-use loading and errors
  // have separate coverage in app-lazy and app-lazy-error tests.
  await Promise.all([import("./components/inspector-tools"), import("./components/settings")]);
});

const timestamp = "2026-10-09T00:00:00Z";
const sourceRunID = "run-source";
const sourceThreadID = "thread-source";
const sourceHash = `#/threads/${sourceThreadID}/inspector/runs/${sourceRunID}`;
const sourceWorkspace: WorkspaceView = { id: "workspace-source", name: "Source project", created_at: timestamp };
const forkWorkspace: WorkspaceView = { id: "workspace-checkpoint-fork", name: "Recovered project", created_at: timestamp };
const operations = ["fork", "resume", "checkpoint-fork"] as const;
type Operation = (typeof operations)[number];
type Detour = "record" | "source Thread" | "settings";

const queryClients: QueryClient[] = [];
afterEach(() => {
  cleanup();
  queryClients.splice(0).forEach((client) => client.clear());
  useConnectionStore.getState().disconnect();
  vi.restoreAllMocks();
  vi.unstubAllGlobals();
  window.history.replaceState({}, "", "/");
  window.localStorage.clear();
});

function deferred<T>() {
  let resolve!: (value: T) => void;
  let reject!: (reason: unknown) => void;
  const promise = new Promise<T>((resolvePromise, rejectPromise) => {
    resolve = resolvePromise;
    reject = rejectPromise;
  });
  return { promise, resolve, reject };
}

function runDetail(id: string, workspaceID = sourceWorkspace.id): RunDetailView {
  return {
    run: { id, status: "paused", session_id: `session-${id}`, config: { model_route: "fixture" } },
    mission: { id: `mission-${id}`, goal: `Goal for ${id}`, profile: "code", workspace_id: workspaceID },
    mode: { surface: "code", phase: "plan", revision: 1 },
  } as unknown as RunDetailView;
}

function instructions(runID: string) {
  const snapshot = { target_path: "", sources: [], conflicts: [], fingerprint: "a".repeat(64) };
  return { run_id: runID, workspace_id: sourceWorkspace.id, pinned_present: true,
    pinned: { snapshot }, live: snapshot, stale: false, capability_grant: false,
    diff: { requires_confirmation: false, added: [], removed: [], changed: [], order_changed: false } };
}

function continuityTree(runID: string) {
  return { protocol_version: "session_tree.v1", session_id: `session-${runID}`,
    workspace_id: sourceWorkspace.id, capability_grant: false, generated_at: timestamp,
    nodes: [{ id: `continuity-${runID}`, kind: "checkpoint", run_id: runID,
      session_id: `session-${runID}`, title: "Context recovery point", status: "valid",
      warnings: [], derived: false, fingerprint: "b".repeat(64), created_at: timestamp }] };
}

function checkpointTimeline(runID: string) {
  const checkpoint = { id: `checkpoint-${runID}`, protocol_version: "workspace-checkpoint.v1",
    run_id: runID, mission_id: `mission-${runID}`, session_id: `session-${runID}`,
    workspace_id: sourceWorkspace.id, trigger: "command_batch", phase: "after",
    trigger_receipt_id: "receipt-source", root_fingerprint: "a".repeat(64),
    root_path_sha256: "b".repeat(64), base_commit: "c".repeat(40), branch: "codex/source",
    index_sha256: "d".repeat(64), manifest_sha256: "e".repeat(64), recovery_level: "complete",
    incomplete_reasons: [], entry_count: 2, stored_bytes: 24, created_at: timestamp,
    title: "Workspace recovery point" };
  return { protocol_version: "workspace-checkpoint-api.v1", run_id: runID,
    workspace_id: sourceWorkspace.id, current: { run_id: runID,
      current_checkpoint_id: checkpoint.id, last_transaction_id: "", updated_at: timestamp },
    checkpoints: [checkpoint], transactions: [],
    storage_usage: { blob_bytes: 24, blob_count: 2, checkpoint_count: 1 } };
}

function thread(id: string, runID = sourceRunID): ThreadView {
  return { id, title: `Task ${id}`, workspace_id: sourceWorkspace.id, mission_id: `mission-${id}`,
    active_run_id: runID, last_run_id: runID, protocol_version: "thread.v1",
    composer_state: "ready", status: "active", version: 1, created_at: timestamp, updated_at: timestamp };
}

function harness(operation: Operation) {
  window.history.replaceState({ v2NavigationIndex: 0 }, "", sourceHash);
  vi.stubGlobal("confirm", vi.fn(() => true));
  const result = deferred<unknown>();
  const returnedRunID = `run-returned-${operation}`;
  let forkPublished = false;
  let recoveryPublished = false;
  const get = vi.fn(async (path: string) => {
    if (path === "/memories") return [];
    const detailMatch = /^\/runs\/([^/]+)$/u.exec(path);
    if (detailMatch) return runDetail(detailMatch[1], detailMatch[1] === returnedRunID &&
      operation === "checkpoint-fork" ? forkWorkspace.id : sourceWorkspace.id);
    const instructionsMatch = /^\/runs\/([^/]+)\/project-instructions$/u.exec(path);
    if (instructionsMatch) return instructions(instructionsMatch[1]);
    const treeMatch = /^\/sessions\/session-(.+)\/tree$/u.exec(path);
    if (treeMatch) return continuityTree(treeMatch[1]);
    const timelineMatch = /^\/runs\/([^/]+)\/workspace-checkpoints$/u.exec(path);
    if (timelineMatch) return checkpointTimeline(timelineMatch[1]);
    const activityMatch = /^\/runs\/([^/]+)\/activity$/u.exec(path);
    if (activityMatch) return { run_id: activityMatch[1] };
    throw new Error(`Unexpected read: ${path}`);
  });
  const getPage = vi.fn(async (path: string, query?: { pinned?: boolean }, cursor = "") => ({
    items: path === "/workspaces" ? [sourceWorkspace, ...(forkPublished ? [forkWorkspace] : [])]
      : path === "/threads" && !query?.pinned ? cursor ? [thread("thread-other")]
        : [thread(sourceThreadID), ...(recoveryPublished ? [thread(`thread-returned-${operation}`, returnedRunID)] : [])] : [],
    page: { limit: 100, ...(path === "/threads" && !query?.pinned && !cursor ? { next_cursor: "thread-page-2" } : {}) }, requestID: path,
  }));
  const postControl = vi.fn(() => result.promise);
  const client = { hasControl: true, hasThreadControl: true, hasWorkspaceCheckpointControl: true,
    hasThreadExecutionRead: true, hasRunExecution: true,
    get, getPage, postControl,
    threadExecution: vi.fn(async (id: string) => ({ version: "thread_execution.v1", thread_id: id,
      state: "idle", queued_messages: 0, capability_grant: false })),
    createThread: vi.fn(), submitThreadTurn: vi.fn(), executeRun: vi.fn(), transitionThread: vi.fn(),
  } as unknown as APIClient;
  const queryClient = new QueryClient({ defaultOptions: {
    queries: { retry: false, staleTime: Infinity }, mutations: { retry: false },
  } });
  queryClients.push(queryClient);
  const recordKeys = [[...v2QueryKeys.inspectorRecords, "run"], [...v2QueryKeys.inspectorRecords, "session"]];
  const searchKey = v2QueryKeys.threadSearch("active", "recovered search");
  for (const key of [...recordKeys, ["runs"], ["sessions"], searchKey]) {
    queryClient.setQueryData(key, { pages: [{ items: [], page: { limit: 50 } }], pageParams: [""] });
  }
  const activeThreadsKey = v2QueryKeys.threadSearch("active", "");
  queryClient.setQueryData(activeThreadsKey, { pages: [
    { items: [thread(sourceThreadID)], page: { limit: 100, next_cursor: "thread-page-2" }, requestID: "threads-1" },
    { items: [thread("thread-other")], page: { limit: 100 }, requestID: "threads-2" },
  ], pageParams: ["", "thread-page-2"] });
  render(<QueryClientProvider client={queryClient}><LocaleProvider>
    <V2Workbench client={client} />
  </LocaleProvider></QueryClientProvider>);
  const complete = async () => {
    forkPublished = operation === "checkpoint-fork";
    recoveryPublished = true;
    await act(async () => result.resolve({ run: runDetail(returnedRunID).run,
      session: { id: `session-${returnedRunID}` }, workspace: forkWorkspace }));
  };
  return { client, get, getPage, postControl, queryClient, recordKeys, searchKey, activeThreadsKey,
    result, returnedRunID, complete };
}

async function submitRecovery(operation: Operation, user: ReturnType<typeof userEvent.setup>) {
  await screen.findByRole("heading", { name: `Goal for ${sourceRunID}` }, { timeout: 5_000 });
  await user.click(screen.getByRole("button", { name: "高级诊断" }));
  await user.click(screen.getByRole("tab", { name: operation === "checkpoint-fork" ? "工作区检查点" : "上下文" }));
  if (operation === "checkpoint-fork") {
    await screen.findAllByText("Workspace recovery point");
    await user.type(screen.getByRole("textbox", { name: "新 Workspace 名称" }), forkWorkspace.name);
    await user.type(screen.getByRole("textbox", { name: "新 Git 分支" }), "codex/recovered");
    await user.type(screen.getByRole("textbox", { name: "新 Run 目标" }), "Recover workspace");
    await user.click(screen.getByRole("button", { name: "确认 Fork" }));
  } else {
    await screen.findByText("Context recovery point");
    await user.type(screen.getByRole("textbox", { name: "新分支目标" }), "Recover context");
    await user.click(screen.getByRole("button", { name: operation === "fork" ? "Fork" : "Resume" }));
  }
}

function expectRequest(operation: Operation, postControl: ReturnType<typeof vi.fn>) {
  if (operation === "checkpoint-fork") {
    expect(postControl).toHaveBeenCalledExactlyOnceWith(`/runs/${sourceRunID}/workspace-checkpoints/fork`,
      expect.objectContaining({ target_checkpoint_id: `checkpoint-${sourceRunID}`,
        expected_current_checkpoint_id: `checkpoint-${sourceRunID}`, workspace_name: forkWorkspace.name,
        branch: "codex/recovered", goal: "Recover workspace", confirm: true,
        operation_key: expect.stringMatching(/^desktop-workspace-fork-/u) }),
      expect.stringMatching(/^desktop-workspace-fork-/u));
    const [, body, key] = postControl.mock.calls[0] as unknown as [string, { operation_key: string }, string];
    expect(body.operation_key).toBe(key);
  } else {
    expect(postControl).toHaveBeenCalledExactlyOnceWith(`/continuity-nodes/continuity-${sourceRunID}/${operation}`,
      { goal: "Recover context" }, expect.stringMatching(new RegExp(`^web-continuity-${operation}-`, "u")));
  }
}

async function expectRefreshed(h: ReturnType<typeof harness>) {
  await waitFor(() => {
    for (const key of [...h.recordKeys, ["runs"], ["sessions"], h.searchKey]) {
      expect(h.queryClient.getQueryState(key)?.isInvalidated).toBe(true);
    }
    expect(h.getPage.mock.calls.filter(([path]) => path === "/workspaces")).toHaveLength(2);
    expect(h.getPage.mock.calls.filter(([path, query]) => path === "/threads" && !query?.pinned).map(([, , cursor]) => cursor))
      .toEqual(["", "thread-page-2"]);
    expect(h.queryClient.getQueryData<InfiniteData<{ items: ThreadView[] }>>(h.activeThreadsKey)
      ?.pages.flatMap((page) => page.items).some((item) => item.active_run_id === h.returnedRunID)).toBe(true);
  });
}

function expectNoExecution(client: APIClient) {
  for (const write of [client.createThread, client.submitThreadTurn, client.executeRun, client.transitionThread]) {
    expect(write).not.toHaveBeenCalled();
  }
}

async function visitHash(hash: string) {
  await act(async () => {
    window.history.replaceState(window.history.state, "", hash);
    fireEvent(window, new PopStateEvent("popstate"));
  });
}

describe("recovery navigation through the real V2 workbench", () => {
  it.each(operations)("opens the exact returned Run after %s, refreshes record caches, and preserves browser back", async (operation) => {
    const user = userEvent.setup();
    const h = harness(operation);
    await submitRecovery(operation, user);
    await waitFor(() => expect(h.postControl).toHaveBeenCalledOnce());
    expectRequest(operation, h.postControl);
    await h.complete();
    await screen.findByRole("heading", { name: `Goal for ${h.returnedRunID}` });
    expect(window.location.hash).toBe(`#/new/inspector/runs/${h.returnedRunID}`);
    expect(screen.getByLabelText("Activity Run")).toHaveTextContent(h.returnedRunID);
    expect(screen.getByText(/此记录尚未绑定对话，请返回任务观察选择来源任务后设置权限/u)).toBeInTheDocument();
    expect(screen.queryByRole("status", { name: "来源任务的 Agent 活动" })).not.toBeInTheDocument();
    expect(useConnectionStore.getState().selectedThreadID).toBe("");
    expect(h.get).toHaveBeenCalledWith(`/runs/${h.returnedRunID}`, {}, expect.any(AbortSignal));
    await expectRefreshed(h);
    if (operation === "checkpoint-fork") {
      await waitFor(() => expect(h.queryClient.getQueryData<InfiniteData<{ items: WorkspaceView[] }>>(
        v2QueryKeys.workspaces)?.pages.flatMap((page) => page.items)).toContainEqual(forkWorkspace));
    }
    expectNoExecution(h.client);
    await user.click(screen.getByRole("button", { name: "返回" }));
    await waitFor(() => expect(window.location.hash).toBe(sourceHash));
    await screen.findByRole("heading", { name: `Goal for ${sourceRunID}` });
    expect(useConnectionStore.getState().selectedThreadID).toBe(sourceThreadID);
    expect(screen.getByRole("status", { name: "来源任务的 Agent 活动" })).toBeInTheDocument();
    expectNoExecution(h.client);
  });

  it.each(operations.flatMap((operation) => (["record", "source Thread", "settings"] as const)
    .map((detour) => ({ operation, detour }))))(
    "does not let a pending $operation hijack navigation after a $detour round trip to the same URL",
    async ({ operation, detour }: { operation: Operation; detour: Detour }) => {
      const user = userEvent.setup();
      const h = harness(operation);
      await submitRecovery(operation, user);
      await waitFor(() => expect(h.postControl).toHaveBeenCalledOnce());
      const pendingButton = screen.getByRole("button", {
        name: operation === "checkpoint-fork" ? "确认 Fork" : operation === "fork" ? "Fork" : "Resume",
      });
      if (detour === "settings") {
        await user.click(screen.getByText("来源与设置"));
        const details = screen.getByText("来源与设置").closest("details")!;
        await user.click(within(details).getByRole("button", { name: "设置" }));
        await waitFor(() => expect(window.location.hash).toBe(`${sourceHash}/settings/general`));
        await screen.findByRole("heading", { name: "常规", level: 1 });
        await user.click(screen.getByRole("button", { name: "返回应用" }));
        await waitFor(() => expect(window.location.hash).toBe(sourceHash));
      } else {
        await visitHash(detour === "record" ? `#/threads/${sourceThreadID}/inspector/runs/run-other`
          : `#/threads/thread-other/inspector/runs/${sourceRunID}`);
        if (detour === "record") await screen.findByRole("heading", { name: "Goal for run-other" });
        else await waitFor(() => expect(useConnectionStore.getState().selectedThreadID).toBe("thread-other"));
        await visitHash(sourceHash);
      }
      await screen.findByRole("heading", { name: `Goal for ${sourceRunID}` });
      expect(window.location.hash).toBe(sourceHash);
      if (detour === "source Thread") {
        // The same panel stays mounted. The mutation observer alone cannot
        // discard this completion; the source-route identity guard must do it.
        expect(screen.getByRole("button", { name: pendingButton.textContent!.trim() })).toBe(pendingButton);
        expect(pendingButton).toBeDisabled();
      }
      await h.complete();
      await waitFor(() => expect(h.queryClient.getMutationCache().getAll().some(
        (mutation) => mutation.state.status === "success")).toBe(true));
      await expectRefreshed(h);
      expect(window.location.hash).toBe(sourceHash);
      expect(screen.getByRole("heading", { name: `Goal for ${sourceRunID}` })).toBeInTheDocument();
      expect(useConnectionStore.getState().selectedThreadID).toBe(sourceThreadID);
      expect(h.get.mock.calls.some(([path]) => path === `/runs/${h.returnedRunID}`)).toBe(false);
      expectNoExecution(h.client);
    },
  );

  it.each(operations)("keeps the source record visible when %s fails", async (operation) => {
    const user = userEvent.setup();
    const h = harness(operation);
    await submitRecovery(operation, user);
    await waitFor(() => expect(h.postControl).toHaveBeenCalledOnce());
    await act(async () => h.result.reject(new Error("Recovery rejected by backend")));
    await screen.findByText("Recovery rejected by backend");
    expect(window.location.hash).toBe(sourceHash);
    expect(screen.getByRole("heading", { name: `Goal for ${sourceRunID}` })).toBeInTheDocument();
    expect(useConnectionStore.getState().selectedThreadID).toBe(sourceThreadID);
    expect(h.get.mock.calls.some(([path]) => path === `/runs/${h.returnedRunID}`)).toBe(false);
    expectNoExecution(h.client);
  });
});
