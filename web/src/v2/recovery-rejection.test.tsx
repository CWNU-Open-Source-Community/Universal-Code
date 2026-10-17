import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act, cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, beforeEach, expect, it, vi } from "vitest";
import type { APIClient } from "../api/client";
import type { ThreadDetailView, ThreadView, WorkspaceView } from "../api/types";
import { useConnectionStore } from "../state/connection";
import { V2Workbench } from "./app";
import type { ThreadRequestObservation } from "./recovery-api";

vi.mock("../hooks/use-run-event-stream", () => ({ useRunEventStream: () => ({ error: null, frames: [] }) }));
vi.mock("../hooks/use-public-model-stream", () => ({ usePublicModelStream: () => ({ error: null, snapshot: null, status: "waiting" }) }));
vi.mock("./components/permission-control", () => ({ V2PermissionControl: () => null }));
vi.mock("./components/run-network-authority-control", () => ({ V2RunNetworkAuthorityControl: () => null }));
vi.mock("./components/model-route-control", () => ({ V2ModelRouteControl: () => null }));
vi.mock("./components/inspector", () => ({ V2Inspector: () => null }));

const scope = `ds1_${"c".repeat(64)}`;
const workspace = { id: "workspace-rejection", name: "Rejection fixture" } as WorkspaceView;
const thread = { id: "thread-rejection", title: "Original rejected request", status: "active",
  workspace_id: workspace.id, composer_state: "ready", version: 1 } as ThreadView;
const detail = { thread, last_run: { id: "run-rejection", status: "completed" }, runs: [], mission: {} } as unknown as ThreadDetailView;
const original = "Original requirement remains editable";
const rejected: ThreadRequestObservation = { kind: "turn", state: "rejected", settled: true,
  workspace_id: workspace.id, thread_id: thread.id, request_fingerprint: "d".repeat(64) };

function fixture() {
  const submit = vi.fn().mockRejectedValue(new Error("Response lost before rejection was observed"));
  const inspect = vi.fn().mockResolvedValue(rejected);
  const client = { baseURL: "/api/v1", hasThreadControl: true, submitThreadTurn: submit,
    inspectThreadTurnRequest: inspect, createThread: vi.fn(), executeRun: vi.fn(), transitionThread: vi.fn(),
    get: vi.fn().mockResolvedValue(detail),
    getPage: vi.fn(async (path: string) => ({ items: path === "/workspaces" ? [workspace]
      : path === "/threads" ? [thread] : [], page: { limit: 100 }, requestID: path })),
  } as unknown as APIClient;
  return { client, submit, inspect };
}

function mount(client: APIClient) {
  act(() => useConnectionStore.getState().setHealth({ status: "ok", api_version: "api.v1",
    app_version: "fixture", schema_version: 157, data_store_id: scope }));
  const queries = new QueryClient({ defaultOptions: { queries: { retry: false, staleTime: Infinity } } });
  const view = render(<QueryClientProvider client={queries}><V2Workbench client={client} /></QueryClientProvider>);
  return { ...view, queries };
}

function savedTurn(key: string): unknown {
  const address = `v2_recovery.v1:${encodeURIComponent(window.location.origin)}:${encodeURIComponent("/api/v1")}:${encodeURIComponent(scope)}:${encodeURIComponent(`turn:${key}`)}`;
  const raw = window.localStorage.getItem(address);
  return raw === null ? undefined : JSON.parse(raw).value;
}

async function unknownSubmission(f: ReturnType<typeof fixture>) {
  const page = mount(f.client);
  fireEvent.change(await screen.findByRole("textbox", { name: "继续对话" }), { target: { value: original } });
  await userEvent.setup().click(screen.getByRole("button", { name: "发送消息" }));
  // Let the existing immediate transport retry finish. Both calls are one
  // original intent; neither is a recovery inspection or a new operation.
  await screen.findByRole("button", { name: "重试核对" });
  expect(f.submit).toHaveBeenCalledTimes(2);
  expect(f.submit.mock.calls[1]).toEqual(f.submit.mock.calls[0]);
  const originalKey = f.submit.mock.calls[0][2] as string;
  expect(savedTurn(originalKey)).toMatchObject({ content: original, operationKey: originalKey });
  expect(f.inspect).not.toHaveBeenCalled();
  return { page, originalKey };
}

beforeEach(() => {
  window.localStorage.clear();
  window.history.replaceState({}, "", `#/threads/${thread.id}`);
});
afterEach(() => {
  cleanup();
  useConnectionStore.getState().disconnect();
  window.localStorage.clear();
  window.history.replaceState({}, "", "/");
  vi.restoreAllMocks();
});

it.each([original, "A corrected requirement"])("reopens a rejected original without POST, then explicitly sends %s with a new key", async (content) => {
  const f = fixture();
  const { page, originalKey } = await unknownSubmission(f);
  page.unmount();
  page.queries.clear();
  f.submit.mockResolvedValue({ steering: { id: "new-message" } });
  mount(f.client);
  await waitFor(() => expect(f.inspect).toHaveBeenCalledTimes(1));
  await waitFor(() => expect(savedTurn(originalKey)).toBeUndefined());
  expect(f.inspect.mock.calls[0].slice(0, 2)).toEqual([thread.id, originalKey]);
  expect(f.submit).toHaveBeenCalledTimes(2);
  const draft = await screen.findByRole("textbox", { name: "继续对话" });
  expect(draft).toHaveValue(original);
  fireEvent.change(draft, { target: { value: content } });
  await userEvent.setup().click(screen.getByRole("button", { name: "发送消息" }));
  await waitFor(() => expect(f.submit).toHaveBeenCalledTimes(3));
  expect(f.submit.mock.calls[2][0]).toBe(thread.id);
  expect(f.submit.mock.calls[2][1]).toEqual({ version: "thread_message_submission.v1", content });
  expect(f.submit.mock.calls[2][2]).not.toBe(originalKey);
  expect(f.client.createThread).not.toHaveBeenCalled();
  expect(f.client.executeRun).not.toHaveBeenCalled();
});

it.each([original, "Different current draft"])("an explicit send that first discovers rejection sends %s once with a new key", async (content) => {
  const f = fixture();
  const { originalKey } = await unknownSubmission(f);
  let resolve!: (value: ThreadRequestObservation) => void;
  f.inspect.mockImplementation(() => new Promise<ThreadRequestObservation>((accept) => { resolve = accept; }));
  f.submit.mockResolvedValue({ steering: { id: "new-message" } });
  fireEvent.change(screen.getByRole("textbox", { name: "继续对话" }), { target: { value: content } });
  await userEvent.setup().click(screen.getByRole("button", { name: "发送消息" }));
  await waitFor(() => expect(f.inspect).toHaveBeenCalledTimes(1));
  expect(f.submit).toHaveBeenCalledTimes(2);
  expect(savedTurn(originalKey)).toBeDefined();
  await act(async () => resolve(rejected));
  await waitFor(() => expect(f.submit).toHaveBeenCalledTimes(3));
  expect(f.submit.mock.calls[2][1]).toEqual({ version: "thread_message_submission.v1", content });
  expect(f.submit.mock.calls[2][2]).not.toBe(originalKey);
  expect(savedTurn(originalKey)).toBeUndefined();
  await waitFor(() => expect(screen.getByRole("textbox", { name: "继续对话" })).toHaveValue(""));
  expect(f.client.createThread).not.toHaveBeenCalled();
  expect(f.client.executeRun).not.toHaveBeenCalled();
});

it.each([false, true])("retains a whitespace-only newer draft after old confirmation and another full remount (reopened confirmation: %s)", async (reopened) => {
  const f = fixture();
  const { page: originalPage, originalKey } = await unknownSubmission(f);
  expect(savedTurn(originalKey)).toMatchObject({ draft: original, content: original });
  let resolve!: (value: ThreadRequestObservation) => void;
  f.inspect.mockImplementation(() => new Promise<ThreadRequestObservation>((accept) => { resolve = accept; }));
  let page = originalPage;
  if (reopened) {
    page.unmount();
    page.queries.clear();
    page = mount(f.client);
  } else {
    await userEvent.setup().click(screen.getByRole("button", { name: "重试核对" }));
  }
  await waitFor(() => expect(f.inspect).toHaveBeenCalledTimes(1));
  const newerDraft = `${original}\n`;
  fireEvent.change(await screen.findByRole("textbox", { name: "继续对话" }), { target: { value: newerDraft } });
  await act(async () => resolve({ kind: "turn", state: "completed", settled: true,
    workspace_id: workspace.id, thread_id: thread.id, run_id: "run-rejection", session_id: "session-rejection",
    message_id: "original-consumed-message", message_status: "committed", request_fingerprint: "e".repeat(64) }));
  await waitFor(() => expect(savedTurn(originalKey)).toBeUndefined());
  expect(screen.getByRole("textbox", { name: "继续对话" })).toHaveValue(newerDraft);
  expect(f.submit).toHaveBeenCalledTimes(2);
  page.unmount();
  page.queries.clear();
  mount(f.client);
  expect(await screen.findByRole("textbox", { name: "继续对话" })).toHaveValue(newerDraft);
  expect(f.inspect).toHaveBeenCalledTimes(1);
  expect(f.submit).toHaveBeenCalledTimes(2);
});

// Retain textarea coverage through the supported plain-text editing mode.
beforeEach(() => localStorage.setItem("universal-code.composer-plain-text", "true"));
