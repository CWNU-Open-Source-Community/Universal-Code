import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act, cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, expect, it, vi } from "vitest";
import type { APIClient } from "../api/client";
import type { ThreadDetailView, ThreadView, WorkspaceView } from "../api/types";
import { useConnectionStore } from "../state/connection";
import { LocaleProvider } from "../lib/locale";
import { V2Workbench } from "./app";
import { v2FileReferenceKey, type V2FileReference } from "./components/file-context";

vi.mock("../hooks/use-run-event-stream", () => ({ useRunEventStream: () => ({ error: null, frames: [] }) }));
vi.mock("../hooks/use-public-model-stream", () => ({ usePublicModelStream: () => ({ error: null, snapshot: null, status: "waiting" }) }));
vi.mock("./components/permission-control", () => ({ V2PermissionControl: () => null }));
vi.mock("./components/run-network-authority-control", () => ({ V2RunNetworkAuthorityControl: () => null }));
vi.mock("./components/model-route-control", () => ({ V2ModelRouteControl: () => null }));
// Inspector rendering has its own coverage. The shell, Conversation, Composer,
// query-backed submission lifetime and original-key confirmation stay real here.
vi.mock("./components/inspector", () => ({ V2Inspector: () => <section aria-label="Inspector records fixture" /> }));

afterEach(() => {
  cleanup();
  useConnectionStore.getState().disconnect();
  window.history.replaceState({}, "", "/");
  vi.restoreAllMocks();
});

it("preserves an unknown submission across Inspector/settings and confirms only the original key before sending new intent", async () => {
  const workspace = { id: "workspace-shell", name: "Shell project" } as WorkspaceView;
  const thread = { id: "thread-shell", title: "Shell task", status: "active", workspace_id: workspace.id,
    composer_state: "ready", version: 1 } as ThreadView;
  const detail = { thread, last_run: { id: "run-shell", status: "completed" }, runs: [], mission: {} } as unknown as ThreadDetailView;
  const original: V2FileReference = { id: "original-file", path: "README.md", digest: "a".repeat(64), partial: false, redacted: false };
  const later: V2FileReference = { ...original, id: "later-file", path: "later.md", digest: "b".repeat(64) };
  const submit = vi.fn().mockRejectedValueOnce(new Error("response lost"))
    .mockRejectedValueOnce(new Error("confirmation response lost"))
    .mockResolvedValue({ steering: { id: "confirmed-message" } });
  const client = { hasThreadControl: true, hasEvidenceAttachment: true, submitThreadTurn: submit,
    createThread: vi.fn(), executeRun: vi.fn(), transitionThread: vi.fn(),
    get: vi.fn().mockResolvedValue(detail),
    getPage: vi.fn(async (path: string) => ({ items: path === "/workspaces" ? [workspace]
      : path === "/threads" ? [thread] : [], page: { limit: 100 }, requestID: path })),
  } as unknown as APIClient;
  const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false, staleTime: Infinity }, mutations: { retry: false } } });
  const key = v2FileReferenceKey(workspace.id, thread.id);
  queryClient.setQueryData(key, [original]);
  window.history.replaceState({}, "", `#/threads/${thread.id}`);
  render(<QueryClientProvider client={queryClient}><LocaleProvider><V2Workbench client={client} /></LocaleProvider></QueryClientProvider>);
  const user = userEvent.setup();
  await user.type(await screen.findByRole("textbox", { name: "继续对话" }), "Original request");
  await user.click(screen.getByRole("button", { name: "发送消息" }));
  await screen.findByRole("button", { name: "重试核对" });
  expect(submit).toHaveBeenCalledTimes(2); // Existing bounded automatic confirmation, same intent.
  expect(submit.mock.calls[1]).toEqual(submit.mock.calls[0]);
  const originalCall = submit.mock.calls[0];
  await user.click(screen.getByRole("button", { name: "观察与记录" }));
  expect(await screen.findByRole("region", { name: "Inspector records fixture" })).toBeInTheDocument();
  expect(screen.getByRole("button", { name: "重试核对" })).toBeInTheDocument();
  await user.click(screen.getByRole("button", { name: "设置" }));
  await user.click(screen.getByRole("button", { name: "返回应用" }));
  expect(window.location.hash).toBe(`#/threads/${thread.id}/inspector`);
  expect(screen.getByRole("button", { name: "重试核对" })).toBeInTheDocument();
  await user.click(screen.getByRole("button", { name: "观察与记录" }));
  const draft = await screen.findByRole("textbox", { name: "继续对话" });
  expect(draft).toHaveValue("Original request");
  expect(queryClient.getQueryData(key)).toEqual([original]);
  expect(submit).toHaveBeenCalledTimes(2);
  fireEvent.change(draft, { target: { value: "A newer requirement" } });
  act(() => { queryClient.setQueryData(key, [original, later]); });
  await user.click(screen.getByRole("button", { name: "重试核对" }));
  await waitFor(() => expect(screen.queryByRole("button", { name: "重试核对" })).not.toBeInTheDocument());
  expect(submit).toHaveBeenCalledTimes(3);
  expect(submit.mock.calls[2]).toEqual(originalCall);
  expect(draft).toHaveValue("A newer requirement");
  expect(queryClient.getQueryData(key)).toEqual([later]);
  await user.click(screen.getByRole("button", { name: "发送消息" }));
  await waitFor(() => expect(draft).toHaveValue(""));
  expect(submit).toHaveBeenCalledTimes(4);
  expect(submit.mock.calls[3][2]).not.toBe(originalCall[2]);
  expect(submit.mock.calls[3][1]).toMatchObject({ content: "A newer requirement",
    files: [{ source_kind: "workspace_file", path: "later.md", expected_sha256: later.digest }] });
  expect(client.createThread).not.toHaveBeenCalled();
  expect(client.executeRun).not.toHaveBeenCalled();
  expect(client.transitionThread).not.toHaveBeenCalled();
});

// Retain textarea coverage through the supported plain-text editing mode.
beforeEach(() => {
  localStorage.setItem("prayu.locale.v1", "zh-CN");
  localStorage.setItem("universal-code.composer-plain-text", "true");
});
