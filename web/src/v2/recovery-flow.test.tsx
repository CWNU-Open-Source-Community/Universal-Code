import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act, cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, beforeEach, expect, it, vi } from "vitest";
import type { APIClient } from "../api/client";
import type { ThreadDetailView, ThreadView, WorkspaceView } from "../api/types";
import { useConnectionStore } from "../state/connection";
import { LocaleProvider } from "../lib/locale";
import { V2Workbench } from "./app";
import type { V2FileReference } from "./components/file-context";
import type { ThreadRequestObservation } from "./recovery-api";
import { recoveryFilesKey } from "./recovery-session";
import type { DraftSnapshot } from "./draft-document";

vi.mock("../hooks/use-run-event-stream", () => ({ useRunEventStream: () => ({ error: null, frames: [] }) }));
vi.mock("../hooks/use-public-model-stream", () => ({ usePublicModelStream: () => ({ error: null, snapshot: null, status: "waiting" }) }));
vi.mock("./components/permission-control", () => ({ V2PermissionControl: () => null }));
vi.mock("./components/run-network-authority-control", () => ({ V2RunNetworkAuthorityControl: () => null }));
vi.mock("./components/model-route-control", () => ({ V2ModelRouteControl: () => null }));
vi.mock("./components/inspector", () => ({ V2Inspector: () => <section aria-label="Inspector records fixture" /> }));

const scopeA = `ds1_${"a".repeat(64)}`;
const scopeB = `ds1_${"b".repeat(64)}`;
const workspace = { id: "workspace-recovery", name: "Recovery project" } as WorkspaceView;
const thread = { id: "thread-recovery", title: "Recovery task", status: "active", workspace_id: workspace.id,
  composer_state: "ready", version: 1 } as ThreadView;
const detail = { thread, last_run: { id: "run-recovery", status: "completed" }, runs: [], mission: {} } as unknown as ThreadDetailView;
const digests = { "README.md": "a".repeat(64), "later.md": "b".repeat(64) };
const fileRecordKey = recoveryFilesKey(workspace.id, thread.id);

function deferred<T>() {
  let resolve!: (value: T) => void;
  const promise = new Promise<T>((accept) => { resolve = accept; });
  return { promise, resolve };
}

function fixture() {
  const pending = deferred<unknown>();
  const submit = vi.fn().mockImplementation(() => pending.promise);
  const inspect = vi.fn().mockRejectedValue(new Error("read-only status unavailable"));
  const workspaceExplore = vi.fn(async (_workspace: string, path: string) => ({
    protocol_version: "workspace_explorer.v1", workspace_id: workspace.id, path,
    kind: path === "." ? "directory" : "file", content: path === "." ? "" : "public file reference",
    entries: path === "." ? Object.keys(digests).map((name) => ({ name, path: name,
      kind: "file", size_bytes: 21, readable: true })) : [], redaction_count: 0, truncated: false,
    returned_bytes: 21, total_bytes: 21,
    provenance: { source_kind: "workspace_file", source_ref: path,
      content_sha256: digests[path as keyof typeof digests] ?? "c".repeat(64), instruction_authorized: false },
  }));
  const client = { baseURL: "/api/v1", hasThreadControl: true, hasEvidenceAttachment: true,
    submitThreadTurn: submit, inspectThreadTurnRequest: inspect, workspaceExplore,
    createThread: vi.fn(), executeRun: vi.fn(), transitionThread: vi.fn(), attachEvidence: vi.fn(),
    get: vi.fn().mockResolvedValue(detail),
    getPage: vi.fn(async (path: string) => ({ items: path === "/workspaces" ? [workspace]
      : path === "/threads" ? [thread] : [], page: { limit: 100 }, requestID: path })),
  } as unknown as APIClient;
  return { client, submit, inspect, pending };
}

function selectScope(scope: string) {
  act(() => useConnectionStore.getState().setHealth({ status: "ok", api_version: "api.v1",
    app_version: "fixture", schema_version: 157, data_store_id: scope }));
}

function mount(client: APIClient, scope = scopeA, parent?: QueryClient) {
  selectScope(scope);
  const queries = parent ?? new QueryClient({ defaultOptions: {
    queries: { retry: false, staleTime: Infinity }, mutations: { retry: false },
  } });
  const view = render(<QueryClientProvider client={queries}><LocaleProvider><V2Workbench client={client} /></LocaleProvider></QueryClientProvider>);
  return { ...view, queries };
}

function stored<T>(key: string, scope = scopeA): T | undefined {
  const base = `v2_recovery.v1:${encodeURIComponent(window.location.origin)}:${encodeURIComponent("/api/v1")}:${encodeURIComponent(scope)}:`;
  if (key.startsWith("draft:") || key.startsWith("files:") || key.startsWith("images:")) {
    const draftKey = key.startsWith("draft:") ? key.slice(6) : `thread:${thread.id}`;
    const prefix = base + encodeURIComponent(`draft-document:${JSON.stringify([draftKey, workspace.id])}:`);
    const branches = Object.keys(localStorage).filter((address) => address.startsWith(prefix)).map((address) =>
      JSON.parse(localStorage.getItem(address)!).value as { branchID: string; seq: number; bases: Record<string, number>; snapshot: DraftSnapshot });
    const heads = branches.filter((branch) => !branches.some((other) => other.branchID !== branch.branchID && other.bases[branch.branchID] >= branch.seq));
    if (heads.length) {
      expect(heads).toHaveLength(1); // These are sequential recovery journeys.
      return heads[0].snapshot[key.startsWith("draft:") ? "text" : key.startsWith("files:") ? "files" : "images"] as T;
    }
  }
  const address = base + encodeURIComponent(key);
  const raw = window.localStorage.getItem(address);
  return raw === null ? undefined : JSON.parse(raw).value as T;
}

async function chooseFile(user: ReturnType<typeof userEvent.setup>, name: keyof typeof digests) {
  await user.click(screen.getByRole("button", { name: "添加附件" }));
  await user.click(screen.getByRole("menuitem", { name: "引用项目文件" }));
  await user.click(await screen.findByRole("button", { name: new RegExp(name.replaceAll(".", "\\.")) }));
  await user.click(await screen.findByRole("button", { name: /^(引用此文件|Reference this file)$/u }));
  expect(screen.getByRole("button", { name: `移除引用 ${name}` })).toBeInTheDocument();
}

function completed(): ThreadRequestObservation {
  return { kind: "turn", state: "completed", settled: true, workspace_id: workspace.id,
    thread_id: thread.id, run_id: "run-recovery", session_id: "session-recovery", message_id: "message-original",
    message_status: "committed", request_fingerprint: "d".repeat(64) };
}

async function sendAndClose(f: ReturnType<typeof fixture>, content = "Original exact request") {
  const user = userEvent.setup();
  const page = mount(f.client);
  fireEvent.change(await screen.findByRole("textbox", { name: "继续对话" }), { target: { value: content } });
  await chooseFile(user, "README.md");
  const originalFiles = stored<V2FileReference[]>(fileRecordKey)!;
  expect(originalFiles).toEqual([{ id: expect.any(String), path: "README.md", digest: digests["README.md"], partial: false, redacted: false }]);
  await user.click(screen.getByRole("button", { name: "发送消息" }));
  await waitFor(() => expect(f.submit).toHaveBeenCalledTimes(1));
  const originalCall = f.submit.mock.calls[0];
  expect(stored(`turn:${originalCall[2]}`)).toMatchObject({ content, operationKey: originalCall[2], files: originalFiles });
  page.unmount();
  page.queries.clear();
  return { originalCall, originalFiles };
}

beforeEach(() => {
  window.localStorage.clear();
  window.history.replaceState({}, "", `#/threads/${thread.id}`);
});
afterEach(() => {
  cleanup();
  useConnectionStore.getState().disconnect();
  window.history.replaceState({}, "", "/");
  window.localStorage.clear();
  vi.restoreAllMocks();
});

it("restores an unsent draft and exact selected file identities after a full Workbench remount", async () => {
  const f = fixture();
  const user = userEvent.setup();
  const first = mount(f.client);
  fireEvent.change(await screen.findByRole("textbox", { name: "继续对话" }), { target: { value: "未发送的中文草稿 🧭" } });
  await chooseFile(user, "README.md");
  const references = stored<V2FileReference[]>(fileRecordKey);
  expect(references?.[0].id).toMatch(/^[0-9a-f-]{36}$/u);
  first.unmount();
  first.queries.clear();
  const second = mount(f.client);
  expect(second.queries).not.toBe(first.queries);
  expect(await screen.findByRole("textbox", { name: "继续对话" })).toHaveValue("未发送的中文草稿 🧭");
  expect(screen.getByRole("button", { name: "移除引用 README.md" }).closest("span"))
    .toHaveAttribute("title", `README.md · ${digests["README.md"]}`);
  expect(stored(fileRecordKey)).toEqual(references);
  expect(f.submit).not.toHaveBeenCalled();
  expect(f.inspect).not.toHaveBeenCalled();
  expect(f.client.attachEvidence).not.toHaveBeenCalled();
});

it("reopens an unknown original turn using only read-only inspection, including a failed inspection retry", async () => {
  const f = fixture();
  const { originalCall } = await sendAndClose(f);
  mount(f.client);
  await waitFor(() => expect(f.inspect).toHaveBeenCalledTimes(1));
  expect(f.inspect.mock.calls[0].slice(0, 2)).toEqual([thread.id, originalCall[2]]);
  expect(await screen.findByRole("textbox", { name: "继续对话" })).toHaveValue("Original exact request");
  await userEvent.setup().click(await screen.findByRole("button", { name: "重试核对" }));
  await waitFor(() => expect(f.inspect).toHaveBeenCalledTimes(2));
  expect(f.inspect.mock.calls[1].slice(0, 2)).toEqual([thread.id, originalCall[2]]);
  expect(f.submit).toHaveBeenCalledTimes(1);
  expect(stored(`turn:${originalCall[2]}`)).toBeDefined();
  expect(f.client.createThread).not.toHaveBeenCalled();
  expect(f.client.executeRun).not.toHaveBeenCalled();
  expect(f.client.transitionThread).not.toHaveBeenCalled();
});

it.each([false, true])("settled original inspection retires only admitted draft/files (newer draft: %s)", async (newer) => {
  const f = fixture();
  const { originalCall, originalFiles } = await sendAndClose(f);
  const observation = deferred<ThreadRequestObservation>();
  f.inspect.mockImplementation(() => observation.promise);
  const page = mount(f.client);
  const draft = await screen.findByRole("textbox", { name: "继续对话" });
  await waitFor(() => expect(f.inspect).toHaveBeenCalledTimes(1));
  let later: V2FileReference | undefined;
  if (newer) {
    fireEvent.change(draft, { target: { value: "A newer requirement must survive" } });
    await chooseFile(userEvent.setup(), "later.md");
    later = stored<V2FileReference[]>(fileRecordKey)?.find(({ path }) => path === "later.md");
    expect(later?.id).not.toBe(originalFiles[0].id);
  }
  await act(async () => observation.resolve(completed()));
  await waitFor(() => expect(screen.queryByRole("button", { name: "重试核对" })).not.toBeInTheDocument());
  expect(draft).toHaveValue(newer ? "A newer requirement must survive" : "");
  if (newer) expect(screen.getByRole("button", { name: "移除引用 README.md" })).toBeInTheDocument();
  else expect(screen.queryByRole("button", { name: "移除引用 README.md" })).not.toBeInTheDocument();
  if (newer) expect(screen.getByRole("button", { name: "移除引用 later.md" })).toBeInTheDocument();
  expect(stored(fileRecordKey)).toEqual(newer ? [...originalFiles, later] : []);
  expect(stored(`turn:${originalCall[2]}`)).toBeUndefined();
  expect(f.submit).toHaveBeenCalledTimes(1);
  page.unmount();
  page.queries.clear();
  mount(f.client);
  expect(await screen.findByRole("textbox", { name: "继续对话" }))
    .toHaveValue(newer ? "A newer requirement must survive" : "");
  expect(f.inspect).toHaveBeenCalledTimes(1);
  expect(f.submit).toHaveBeenCalledTimes(1);
});

it("sends a not-received original request only after explicit action, preserving its old key and body", async () => {
  const f = fixture();
  const { originalCall } = await sendAndClose(f);
  f.inspect.mockResolvedValue({ kind: "turn", state: "not_received", settled: false,
    workspace_id: workspace.id, thread_id: thread.id } satisfies ThreadRequestObservation);
  f.submit.mockResolvedValue({ steering: { id: "message-original" } });
  mount(f.client);
  const button = await screen.findByRole("button", { name: "继续提交原消息" });
  expect(f.submit).toHaveBeenCalledTimes(1);
  fireEvent.change(screen.getByRole("textbox", { name: "继续对话" }), { target: { value: "New text is not the old request" } });
  await userEvent.setup().click(button);
  await waitFor(() => expect(f.submit).toHaveBeenCalledTimes(2));
  expect(f.submit.mock.calls[1]).toEqual(originalCall);
  await waitFor(() => expect(stored(`turn:${originalCall[2]}`)).toBeUndefined());
  expect(screen.getByRole("textbox", { name: "继续对话" })).toHaveValue("New text is not the old request");
});

it("isolates a different data-store scope even with the previous parent QueryClient and Thread ID", async () => {
  const f = fixture();
  const first = mount(f.client);
  const user = userEvent.setup();
  fireEvent.change(await screen.findByRole("textbox", { name: "继续对话" }), { target: { value: "Scope A private draft" } });
  await chooseFile(user, "README.md");
  await user.click(screen.getByRole("button", { name: "发送消息" }));
  await waitFor(() => expect(f.submit).toHaveBeenCalledTimes(1));
  const key = f.submit.mock.calls[0][2];
  const scopeARecord = stored(`turn:${key}`);
  selectScope(scopeB); // Same mounted Workbench and parent cache: identity is the boundary.
  await waitFor(() => expect(screen.getByRole("textbox", { name: "继续对话" })).toHaveValue(""));
  expect(screen.queryByRole("button", { name: "移除引用 README.md" })).not.toBeInTheDocument();
  expect(screen.queryByRole("button", { name: "重试核对" })).not.toBeInTheDocument();
  expect(f.inspect).not.toHaveBeenCalled();
  expect(f.submit).toHaveBeenCalledTimes(1);
  expect(stored(`turn:${key}`, scopeB)).toBeUndefined();
  fireEvent.change(screen.getByRole("textbox", { name: "继续对话" }), { target: { value: "Scope B separate draft" } });
  first.unmount();
  mount(f.client, scopeA, first.queries);
  expect(await screen.findByRole("textbox", { name: "继续对话" })).toHaveValue("Scope A private draft");
  await waitFor(() => expect(f.inspect).toHaveBeenCalledTimes(1));
  expect(f.inspect.mock.calls[0].slice(0, 2)).toEqual([thread.id, key]);
  expect(stored(`turn:${key}`)).toEqual(scopeARecord);
  expect(stored(`draft:thread:${thread.id}`, scopeB)).toBe("Scope B separate draft");
  expect(f.submit).toHaveBeenCalledTimes(1);
});

it.each([
  { kind: "files", key: fileRecordKey,
    value: [{ id: "saved-reference", path: "README.md", digest: "invalid-digest", partial: false, redacted: false }] },
  { kind: "draft", key: `draft:thread:${thread.id}`, value: { text: "Preserve this invalid saved draft" } },
])("preserves an invalid $kind business value while allowing local edits and blocking submission", async ({ kind, key, value }) => {
  const f = fixture();
  const user = userEvent.setup();
  const address = `v2_recovery.v1:${encodeURIComponent(window.location.origin)}:${encodeURIComponent("/api/v1")}:${encodeURIComponent(scopeA)}:${encodeURIComponent(key)}`;
  // The envelope is valid: rejection must come from the real recovery hooks'
  // business validation, rather than the storage JSON/envelope decoder.
  const original = JSON.stringify({ version: "v2_recovery.v1", value });
  window.localStorage.setItem(address, original);
  mount(f.client);
  const draft = await screen.findByRole("textbox", { name: "继续对话" });
  const warning = "本机恢复记录格式异常；原记录已保留，当前修改无法覆盖它。";
  await waitFor(() => expect(screen.getAllByRole("alert").some((node) => node.textContent === warning)).toBe(true));
  expect(draft).toHaveValue("");
  expect(screen.queryByRole("button", { name: "移除引用 README.md" })).not.toBeInTheDocument();
  expect(window.localStorage.getItem(address)).toBe(original);

  fireEvent.change(draft, { target: { value: "A new requirement remains editable in this window" } });
  if (kind === "files") await chooseFile(user, "later.md");
  expect(window.localStorage.getItem(address)).toBe(original);
  await user.click(screen.getByRole("button", { name: "发送消息" }));
  // A known local persistence failure never becomes an unknown network turn.
  expect(screen.getByRole("button", { name: "发送消息" })).toBeDisabled();
  expect(screen.queryByRole("button", { name: "重试核对" })).not.toBeInTheDocument();
  expect(f.submit).not.toHaveBeenCalled();
  expect(f.inspect).not.toHaveBeenCalled();
  expect(f.client.attachEvidence).not.toHaveBeenCalled();
  expect(window.localStorage.getItem(address)).toBe(original);
  expect(draft).toHaveValue("A new requirement remains editable in this window");
  expect(draft).toBeEnabled();

  fireEvent.change(draft, { target: { value: "Further local editing after the blocked send" } });
  expect(draft).toHaveValue("Further local editing after the blocked send");
  expect(screen.getAllByRole("alert").some((node) => node.textContent === warning)).toBe(true);
  expect(window.localStorage.getItem(address)).toBe(original);
  if (kind === "files") expect(screen.getByRole("button", { name: "移除引用 later.md" })).toBeInTheDocument();
  expect(f.submit).not.toHaveBeenCalled();
});

// Retain textarea coverage through the supported plain-text editing mode.
beforeEach(() => {
  localStorage.setItem("prayu.locale.v1", "zh-CN");
  localStorage.setItem("universal-code.composer-plain-text", "true");
});
