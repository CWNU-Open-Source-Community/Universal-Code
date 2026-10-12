import { act, cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import type { APIClient } from "../api/client";
import type { ThreadCreationControlRequestView, ThreadView } from "../api/types";
import type { V2FileReference } from "./components/file-context";
import { useV2CreationRecovery } from "./recovery-creation";
import type { CreationIntent } from "./recovery-creation";
import { V2RecoveryProvider, useV2RecoveryStore } from "./recovery-storage";
import type { V2RecoveryStore } from "./recovery-storage";
import { recoveryTurnKey } from "./recovery-session";

const workspaceID = "workspace-recovery";
const request: ThreadCreationControlRequestView = { version: "thread_creation.v1", workspace_id: workspaceID,
  goal: "Read README without writing", profile: "code", surface: "code", phase: "deliver", network_mode: "disabled" };
const files: V2FileReference[] = [{ id: "file-readme", path: "README.md", digest: "a".repeat(64), partial: false, redacted: true }];
const thread: ThreadView = { id: "thread-original", workspace_id: workspaceID, mission_id: "mission-original",
  active_run_id: "run-original", last_run_id: "run-original", protocol_version: "thread.v1", title: request.goal,
  status: "active", composer_state: "ready", version: 1, created_at: "2026-09-11T00:00:00Z", updated_at: "2026-09-11T00:00:00Z" };
const absent = { kind: "creation", workspace_id: workspaceID, state: "not_received", settled: false };
const found = { kind: "creation", workspace_id: workspaceID, state: "completed", settled: true,
  thread_id: thread.id, run_id: "run-original", session_id: "session-original", request_fingerprint: "b".repeat(64) };

function fixture() {
  return { baseURL: "/api/v1", inspectThreadCreationRequest: vi.fn().mockResolvedValue(absent),
    get: vi.fn().mockResolvedValue({ thread }), createThread: vi.fn().mockResolvedValue({ thread }),
    selectProjectSkills: vi.fn().mockResolvedValue(undefined),
    submitThreadTurn: vi.fn(), executeRun: vi.fn() };
}
function mount(client: ReturnType<typeof fixture>, selectedWorkspace = workspaceID, onRecovered = vi.fn()) {
  let helper!: ReturnType<typeof useV2CreationRecovery>;
  let store!: V2RecoveryStore;
  function Probe() {
    store = useV2RecoveryStore()!;
    helper = useV2CreationRecovery(client as unknown as APIClient, selectedWorkspace, onRecovered);
    return helper.notice;
  }
  const view = render(<V2RecoveryProvider client={client} scopeID="creation-db"><Probe /></V2RecoveryProvider>);
  return { ...view, helper: () => helper, store: () => store, onRecovered };
}
function prepare(view: ReturnType<typeof mount>, body = request, refs = files, draft = body.goal) {
  let intent!: CreationIntent;
  act(() => { intent = view.helper().prepare(body, body.goal, refs, draft); });
  return intent;
}

beforeEach(() => window.localStorage.clear());
afterEach(() => { cleanup(); vi.restoreAllMocks(); });

describe("first-message creation recovery", () => {
  it("selects exact project Skills before handoff and retains the created task on selection failure", async () => {
    const client = fixture();
    client.selectProjectSkills.mockRejectedValueOnce(new Error("exact package is unavailable"));
    const first = mount(client);
    let intent!: CreationIntent;
    const choice = { refs: ["code@1.0.0"], confirmUntrustedContext: true };
    act(() => { intent = first.helper().prepare(request, request.goal, files, request.goal, undefined, undefined, undefined, choice); });
    await act(async () => { await expect(first.helper().resolve(intent)).rejects.toThrow("exact package is unavailable"); });
    expect(() => first.helper().handoff(intent, thread)).toThrow("原创建请求无法可靠核对");
    expect(first.store().read(`creation:${intent.operationID}`, null)).toEqual(intent);
    expect(client.submitThreadTurn).not.toHaveBeenCalled();
    expect(client.selectProjectSkills).toHaveBeenLastCalledWith(thread.last_run_id, choice, `v2-project-skills-${intent.operationID}`);
    first.unmount();
    client.inspectThreadCreationRequest.mockResolvedValue(found);
    const reopened = mount(client);
    await screen.findByRole("button", { name: "打开原对话" });
    // A read-only inspection cannot bypass the failed explicit selection.
    expect(() => reopened.helper().handoff(intent, thread)).toThrow("原创建请求无法可靠核对");
    fireEvent.click(screen.getByRole("button", { name: "打开原对话" }));
    await waitFor(() => expect(reopened.onRecovered).toHaveBeenCalledTimes(1));
    expect(client.selectProjectSkills).toHaveBeenCalledTimes(2);
    expect(client.createThread).toHaveBeenCalledTimes(1);
    expect(reopened.store().read(`creation:${intent.operationID}`, null)).toBeNull();
  });
  it("retries an unknown creation with the original key and budget after reopening, even when the draft budget changes", async () => {
    const client = fixture();
    client.createThread.mockRejectedValueOnce(new Error("response lost"));
    const originalRequest = { ...request, budget: { max_turns: 20, max_tokens: 1_000 } };
    const first = mount(client);
    const original = prepare(first, originalRequest);
    await act(async () => { await expect(first.helper().resolve(original)).rejects.toThrow("response lost"); });
    first.unmount();
    const reopened = mount(client);
    const resumed = prepare(reopened, { ...request, budget: { max_tokens: 1_000, max_turns: 20 } });
    expect(resumed.operationID).toBe(original.operationID);
    const edited = prepare(reopened, { ...request, budget: { max_turns: 10, max_tokens: 500 } });
    expect(edited.operationID).not.toBe(original.operationID);
    await act(async () => {
      await expect(reopened.helper().resolve({ ...original, request: edited.request })).rejects.toThrow("原创建请求无法可靠核对");
      await reopened.helper().resolve(resumed);
    });
    expect(client.createThread.mock.calls).toEqual([
      [originalRequest, `v2-thread-create-${original.operationID}`],
      [originalRequest, `v2-thread-create-${original.operationID}`],
    ]);
    expect(reopened.store().read(`creation:${original.operationID}`, null)).toEqual(original);
  });

  it("rejects corrupt persisted numeric budgets without issuing a creation request", async () => {
    const client = fixture();
    const view = mount(client);
    const intent = prepare(view, { ...request, budget: { max_tokens: 1_000 } });
    act(() => view.store().write(`creation:${intent.operationID}`, { ...intent, request: { ...intent.request, budget: { max_tokens: null } } }));
    await act(async () => { await expect(view.helper().resolve(intent)).rejects.toThrow("原创建请求无法可靠核对"); });
    expect(client.createThread).not.toHaveBeenCalled();
  });
  it("recovers a lost create response by GET after reopening and opens the now-running original Thread", async () => {
    const client = fixture();
    client.createThread.mockRejectedValueOnce(new Error("response lost"));
    const first = mount(client);
    const intent = prepare(first, request, files, `  ${request.goal}  `);
    await act(async () => { await expect(first.helper().resolve(intent)).rejects.toThrow("response lost"); });
    expect(client.createThread).toHaveBeenCalledWith(request, `v2-thread-create-${intent.operationID}`);
    first.unmount();
    client.inspectThreadCreationRequest.mockResolvedValue(found);
    // The old create response parser expects the initial ready Run; recovery
    // instead reads this valid current Thread, including its changed lifecycle.
    client.get.mockResolvedValue({ thread: { ...thread, composer_state: "waiting_approval" } });
    const second = mount(client);
    await screen.findByRole("button", { name: "打开原对话" });
    expect(client.createThread).toHaveBeenCalledTimes(1);
    expect(client.submitThreadTurn).not.toHaveBeenCalled();
    expect(client.executeRun).not.toHaveBeenCalled();
    fireEvent.click(screen.getByRole("button", { name: "打开原对话" }));
    expect(second.onRecovered).toHaveBeenCalledWith(expect.objectContaining({ id: thread.id }), `  ${request.goal}  `, files,
      expect.objectContaining({ threadID: thread.id, content: request.goal, draft: `  ${request.goal}  `,
        operationKey: `v2-thread-create-turn-${intent.operationID}`, createdAt: intent.createdAt }));
    const input = second.onRecovered.mock.calls[0][3];
    expect(second.store().read(recoveryTurnKey(input), null)).toEqual(input);
    expect(input.content).toBe(request.goal);
    expect(input.draft).toBe(`  ${request.goal}  `);
    expect(second.store().entries("creation:")).toEqual([]);
    expect(client.submitThreadTurn).not.toHaveBeenCalled();
  });

  it("keeps successful creation recoverable if the app exits before preparing the first turn", async () => {
    const client = fixture();
    const first = mount(client);
    const intent = prepare(first);
    await act(async () => { expect(await first.helper().resolve(intent)).toEqual(thread); });
    expect(first.store().entries("turn:")).toEqual([]);
    expect(first.store().entries("creation:")).toHaveLength(1);
    first.unmount();
    client.inspectThreadCreationRequest.mockResolvedValue(found);
    const second = mount(client);
    await screen.findByRole("button", { name: "打开原对话" });
    const resumed = prepare(second);
    expect(resumed).toEqual(intent);
    await act(async () => { expect(await second.helper().resolve(resumed)).toEqual(thread); });
    expect(client.createThread).toHaveBeenCalledTimes(1);
    expect(client.submitThreadTurn).not.toHaveBeenCalled();
  });

  it("writes the exact first-turn journal before removing creation and safely retains both after a failed removal", async () => {
    const client = fixture();
    const first = mount(client);
    const intent = prepare(first);
    await act(async () => { await first.helper().resolve(intent); });
    const remove = vi.spyOn(Storage.prototype, "removeItem").mockImplementation(() => { throw new Error("cannot remove"); });
    act(() => expect(() => first.helper().handoff(intent, thread)).toThrow("无法移除"));
    const savedInput = first.store().entries("turn:")[0][1];
    expect(savedInput).toMatchObject({ operationKey: `v2-thread-create-turn-${intent.operationID}`, createdAt: intent.createdAt });
    expect(first.store().entries("creation:")).toHaveLength(1);
    remove.mockRestore();
    first.unmount();
    client.inspectThreadCreationRequest.mockResolvedValue(found);
    const second = mount(client);
    await screen.findByRole("button", { name: "打开原对话" });
    fireEvent.click(screen.getByRole("button", { name: "打开原对话" }));
    expect(second.onRecovered.mock.calls[0][3]).toEqual(savedInput);
    expect(second.store().entries("turn:")).toHaveLength(1);
    expect(second.store().entries("creation:")).toEqual([]);
    expect(client.createThread).toHaveBeenCalledTimes(1);
    expect(client.submitThreadTurn).not.toHaveBeenCalled();
  });

  it("performs read-only not-found observations on mount and explicit refresh, with no automatic create or submit", async () => {
    const client = fixture();
    const first = mount(client);
    const intent = prepare(first);
    first.unmount();
    const second = mount(client);
    await screen.findByText(/本次查询尚未找到创建记录。可点击“重新核对”/);
    fireEvent.click(screen.getByRole("button", { name: "重新核对" }));
    await waitFor(() => expect(client.inspectThreadCreationRequest).toHaveBeenCalledTimes(2));
    expect(client.inspectThreadCreationRequest).toHaveBeenLastCalledWith(workspaceID, `v2-thread-create-${intent.operationID}`, expect.any(AbortSignal));
    expect(client.createThread).not.toHaveBeenCalled();
    expect(client.submitThreadTurn).not.toHaveBeenCalled();
    expect(second.store().entries("creation:")).toHaveLength(1);
  });

  it("persists before network and leaves creation intact if first-turn storage fails", async () => {
    const client = fixture();
    const view = mount(client);
    const setItem = vi.spyOn(Storage.prototype, "setItem").mockImplementation(() => { throw new Error("quota"); });
    act(() => expect(() => view.helper().prepare(request, request.goal, files, request.goal)).toThrow("无法保存"));
    expect(client.inspectThreadCreationRequest).not.toHaveBeenCalled();
    expect(client.createThread).not.toHaveBeenCalled();
    setItem.mockRestore();
    const intent = prepare(view);
    await act(async () => { await view.helper().resolve(intent); });
    vi.spyOn(Storage.prototype, "setItem").mockImplementation(() => { throw new Error("quota"); });
    act(() => expect(() => view.helper().handoff(intent, thread)).toThrow("无法保存"));
    expect(view.store().entries("creation:")).toHaveLength(1);
    expect(view.store().entries("turn:")).toEqual([]);
    expect(client.submitThreadTurn).not.toHaveBeenCalled();
  });

  it("reuses a complete unchanged intent but binds changed text, references, and network body to new keys", () => {
    const view = mount(fixture());
    const original = prepare(view);
    const reordered = { provider: undefined, model: undefined, workspace_id: request.workspace_id, version: request.version, phase: request.phase,
      surface: request.surface, profile: request.profile, goal: request.goal, network_mode: request.network_mode };
    expect(prepare(view, reordered).operationID).toBe(original.operationID);
    const edited = prepare(view, { ...request, goal: "Read another file" });
    const changedFile = prepare(view, request, [{ ...files[0], digest: "c".repeat(64) }]);
    const changedNetwork = prepare(view, { ...request, network_mode: "allowlist", allowed_targets: ["example.com"] });
    expect(new Set([original, edited, changedFile, changedNetwork].map((intent) => intent.operationID)).size).toBe(4);
    expect(view.store().read(`creation:${original.operationID}`, null)).toEqual(original);
  });

  it("preserves a supported model tag and exact network body while rejecting wildcard authority before creation", () => {
    const client = fixture();
    const view = mount(client);
    const body = { ...request, provider: "local", model: "qwen3:latest", network_mode: "allowlist" as const, allowed_targets: ["https://example.com"] };
    expect(prepare(view, body).request).toEqual(body);
    act(() => expect(() => view.helper().prepare({ ...body, allowed_targets: ["*"] }, body.goal, files, body.goal)).toThrow());
    expect(client.createThread).not.toHaveBeenCalled();
  });

  it("refuses malformed business records and altered in-memory intent bodies without deleting evidence or POSTing", async () => {
    const client = fixture();
    const view = mount(client);
    const intent = prepare(view);
    act(() => view.store().write(`creation:${intent.operationID}`, { ...intent, content: "tampered without matching goal" }));
    act(() => expect(() => view.helper().prepare(request, request.goal, files, request.goal)).toThrow("原创建请求无法可靠核对"));
    await act(async () => { await expect(view.helper().resolve(intent)).rejects.toThrow("原创建请求无法可靠核对"); });
    expect(client.createThread).not.toHaveBeenCalled();
    expect(view.store().entries("creation:")).toHaveLength(1);
    view.unmount();
    mount(client);
    await screen.findByRole("alert");
    expect(client.inspectThreadCreationRequest).not.toHaveBeenCalled();
  });

  it("refuses a stored first-turn payload with the same key but different content", async () => {
    const client = fixture();
    const view = mount(client);
    const intent = prepare(view);
    await act(async () => { await view.helper().resolve(intent); });
    const old = { threadID: thread.id, workspaceID, operationKey: `v2-thread-create-turn-${intent.operationID}`,
      content: "Other admitted content", createdAt: intent.createdAt, files };
    view.store().write(recoveryTurnKey(old), old);
    act(() => expect(() => view.helper().handoff(intent, thread)).toThrow("原创建请求无法可靠核对"));
    expect(view.store().read(recoveryTurnKey(old), null)).toEqual(old);
    expect(view.store().entries("creation:")).toHaveLength(1);
    expect(client.submitThreadTurn).not.toHaveBeenCalled();
  });

  it("does not use a valid but edited in-memory body under an already saved operation identity", async () => {
    const client = fixture();
    const view = mount(client);
    const intent = prepare(view);
    const changed = { ...intent, request: { ...intent.request, goal: "Other goal" }, content: "Other goal", submittedDraft: "Other goal" };
    await act(async () => { await expect(view.helper().resolve(changed)).rejects.toThrow("原创建请求无法可靠核对"); });
    expect(client.inspectThreadCreationRequest).not.toHaveBeenCalled();
    expect(client.createThread).not.toHaveBeenCalled();
    expect(view.store().read(`creation:${intent.operationID}`, null)).toEqual(intent);
  });

  it("rejects a cross-workspace lookup result and does not replay create", async () => {
    const client = fixture();
    const first = mount(client);
    const intent = prepare(first);
    first.unmount();
    client.inspectThreadCreationRequest.mockResolvedValue(found);
    client.get.mockResolvedValue({ thread: { ...thread, workspace_id: "other-workspace" } });
    const second = mount(client);
    await screen.findByText(/暂时无法核对/);
    await act(async () => { await expect(second.helper().resolve(intent)).rejects.toThrow("原创建请求无法可靠核对"); });
    expect(client.createThread).not.toHaveBeenCalled();
    expect(client.submitThreadTurn).not.toHaveBeenCalled();
    expect(screen.queryByRole("button", { name: "打开原对话" })).not.toBeInTheDocument();
  });
});
