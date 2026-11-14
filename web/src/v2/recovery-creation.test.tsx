import type { ReactElement } from "react";
import type { RenderOptions } from "@testing-library/react";
import { LocaleProvider } from "../lib/locale";
import { act, cleanup, fireEvent, render as renderComponent, screen, waitFor } from "@testing-library/react";
import type { APIClient } from "../api/client";
import type { ThreadCreationControlRequestView, ThreadView } from "../api/types";
import type { V2FileReference } from "./components/file-context";
import { useV2CreationRecovery } from "./recovery-creation";
import type { CreationIntent } from "./recovery-creation";
import { V2RecoveryProvider, useV2RecoveryStore } from "./recovery-storage";
import type { V2RecoveryStore } from "./recovery-storage";
import { recoveryTurnKey } from "./recovery-session";
import type { MCPDraftReference } from "../api/mcp-references";

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
    changeThreadExecutionPermission: vi.fn().mockImplementation(async (_id, body) => ({
      execution_permission: { approval_mode: body.mode, full_activation: body.mode === "full" ? "active" : "inactive" },
    })),
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
  const prepareApproval = (view: ReturnType<typeof mount>, mode: "ask" | "auto" | "full") => {
    let intent!: CreationIntent;
    act(() => { intent = view.helper().prepare(request, request.goal, files, request.goal, [], undefined, [], undefined, [], mode); });
    return intent;
  };

  it("applies the selected permission before handing off the first input, requiring live Full confirmation", async () => {
    const client = fixture();
    const view = mount(client);
    const intent = prepareApproval(view, "full");
    await act(async () => { await expect(view.helper().resolve(intent)).rejects.toThrow("确认完全访问"); });
    expect(client.createThread).not.toHaveBeenCalled();
    expect(client.changeThreadExecutionPermission).not.toHaveBeenCalled();
    let resolved!: ThreadView;
    await act(async () => { resolved = await view.helper().resolve(intent, true); });
    expect(client.changeThreadExecutionPermission).toHaveBeenCalledWith(thread.id, {
      mode: "full", confirm_full: true, reason: "New conversation approval preference selection",
    }, expect.any(String));
    expect(client.createThread.mock.invocationCallOrder[0]).toBeLessThan(client.changeThreadExecutionPermission.mock.invocationCallOrder[0]);
    act(() => { view.helper().handoff(intent, resolved); });
    expect(view.store().entries("creation:")).toHaveLength(0);
    expect(view.store().entries("turn:")).toHaveLength(1);
  });

  it("keeps the draft and creation key when permission activation fails, without handing off a turn", async () => {
    const client = fixture();
    client.changeThreadExecutionPermission.mockRejectedValueOnce(new Error("activation unavailable"));
    const view = mount(client);
    const intent = prepareApproval(view, "full");
    await act(async () => { await expect(view.helper().resolve(intent, true)).rejects.toThrow("activation unavailable"); });
    expect(() => view.helper().handoff(intent, thread)).toThrow();
    expect(view.store().read(`creation:${intent.operationID}`, null)).toEqual(intent);
    expect(view.store().entries("turn:")).toHaveLength(0);
    client.inspectThreadCreationRequest.mockResolvedValue(found);
    const retry = prepareApproval(view, "auto");
    expect(retry.operationID).toBe(intent.operationID);
    await act(async () => { await view.helper().resolve(retry); });
    expect(client.createThread).toHaveBeenCalledTimes(1);
    expect(client.changeThreadExecutionPermission).toHaveBeenLastCalledWith(thread.id, {
      mode: "auto", confirm_full: false, reason: "New conversation approval preference selection",
    }, expect.any(String));
  });

  it("does not restore Full authorization from the creation journal or from read-only inspection", async () => {
    const client = fixture();
    const original = mount(client);
    const intent = prepareApproval(original, "full");
    await act(async () => { await original.helper().resolve(intent, true); });
    original.unmount();
    client.inspectThreadCreationRequest.mockResolvedValue(found);
    client.changeThreadExecutionPermission.mockClear();
    const restored = mount(client);
    const open = await screen.findByRole("button", { name: "打开原对话" });
    expect(client.changeThreadExecutionPermission).not.toHaveBeenCalled();
    expect(() => restored.helper().handoff(intent, thread)).toThrow();
    fireEvent.click(open);
    expect(await screen.findByRole("dialog", { name: "启用完全访问权限？" })).toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: "取消" }));
    expect(client.changeThreadExecutionPermission).not.toHaveBeenCalled();
    expect(restored.onRecovered).not.toHaveBeenCalled();
    fireEvent.click(open);
    fireEvent.click(screen.getByRole("button", { name: "确认并继续" }));
    await waitFor(() => expect(restored.onRecovered).toHaveBeenCalledTimes(1));
    expect(client.changeThreadExecutionPermission).toHaveBeenCalledTimes(1);
    expect(client.createThread).toHaveBeenCalledTimes(1);
  });

  it("does not hand off a first turn when Full is saved but not activated", async () => {
    const client = fixture();
    client.changeThreadExecutionPermission.mockResolvedValueOnce({ execution_permission: { approval_mode: "full", full_activation: "inactive" } });
    const view = mount(client);
    const intent = prepareApproval(view, "full");
    await act(async () => { await expect(view.helper().resolve(intent, true)).rejects.toThrow("权限尚未生效"); });
    expect(() => view.helper().handoff(intent, thread)).toThrow();
    expect(view.store().entries("turn:")).toHaveLength(0);
  });

  it("serializes permission submission and preserves the original handoff after leaving the page", async () => {
    const client = fixture();
    let activate!: (value: any) => void;
    client.changeThreadExecutionPermission.mockImplementation(() => new Promise((resolve) => { activate = resolve; }));
    const view = mount(client);
    const intent = prepareApproval(view, "auto");
    client.inspectThreadCreationRequest.mockResolvedValue(found);
    fireEvent.click(screen.getByRole("button", { name: "重新核对" }));
    fireEvent.click(await screen.findByRole("button", { name: "打开原对话" }));
    await waitFor(() => expect(client.changeThreadExecutionPermission).toHaveBeenCalledTimes(1));
    expect(() => view.helper().prepare(request, request.goal, files, request.goal, [], undefined, [], undefined, [], "ask")).toThrow("正在提交");
    await expect(view.helper().resolve(intent)).rejects.toThrow("正在提交");
    expect(client.changeThreadExecutionPermission).toHaveBeenCalledTimes(1);
    view.unmount();
    const remounted = mount(client);
    await screen.findByRole("button", { name: "打开原对话" });
    expect(screen.getByRole("button", { name: "打开原对话" })).toBeDisabled();
    expect(() => remounted.helper().prepare(request, request.goal, files, request.goal, [], undefined, [], undefined, [], "ask")).toThrow("正在提交");
    await expect(remounted.helper().resolve(intent)).rejects.toThrow("正在提交");
    await act(async () => activate({ execution_permission: { approval_mode: "auto", full_activation: "inactive" } }));
    expect(view.onRecovered).toHaveBeenCalledWith(thread, request.goal, files, expect.objectContaining({ threadID: thread.id }), false);
    expect(remounted.helper().pending).toBe(false);
    expect(screen.queryByRole("button", { name: "打开原对话" })).not.toBeInTheDocument();
  });

  it("leaves a handled current image error to its action row while preserving other scopes and the creation guard", async () => {
    const client = fixture();
    const view = mount(client);
    const intent = prepare(view);
    const current = `image-upload:${JSON.stringify([workspaceID, ""])}:v2-image-upload-current`;
    const other = `image-upload:${JSON.stringify([workspaceID, "other-thread"])}:v2-image-upload-other`;
    act(() => { view.store().write(current, false); view.store().reject(current); });
    fireEvent.click(screen.getByRole("button", { name: "重新核对" }));
    await waitFor(() => expect(screen.queryByRole("button", { name: "重新核对" })).not.toBeInTheDocument());
    expect(screen.queryByRole("alert")).not.toBeInTheDocument();
    expect(() => view.helper().prepare(request, request.goal, files, request.goal)).toThrow("格式异常");
    act(() => { view.store().write(other, false); view.store().reject(other); });
    await screen.findByRole("alert");
    act(() => view.store().remove(current));
    expect(screen.getByRole("alert")).toHaveTextContent("格式异常");
    expect(view.store().read(other, null)).toBe(false);
    expect(view.store().read(`creation:${intent.operationID}`, null)).toEqual(intent);
    expect(() => view.helper().prepare(request, request.goal, files, request.goal)).toThrow("格式异常");
    expect(client.inspectThreadCreationRequest).not.toHaveBeenCalled();
    expect(client.createThread).not.toHaveBeenCalled();
  });

  it("rechecks a failed global load after the last damaged image record is discarded and retains the unknown creation", async () => {
    const client = fixture();
    const view = mount(client);
    const intent = prepare(view);
    const damaged = ["image-upload:damaged-one", "image-upload:damaged-two"];
    act(() => {
      for (const key of damaged) { view.store().write(key, { damaged: true }); view.store().reject(key); }
    });
    fireEvent.click(screen.getByRole("button", { name: "重新核对" }));
    await screen.findByRole("alert");
    expect(client.inspectThreadCreationRequest).not.toHaveBeenCalled();
    act(() => view.store().remove(damaged[0]));
    expect(screen.getByRole("alert")).toHaveTextContent("格式异常");
    expect(view.store().read(damaged[1], null)).toEqual({ damaged: true });
    expect(view.store().read(`creation:${intent.operationID}`, null)).toEqual(intent);
    expect(() => view.helper().prepare(request, request.goal, files, request.goal)).toThrow("格式异常");
    expect(client.inspectThreadCreationRequest).not.toHaveBeenCalled();
    act(() => view.store().remove(damaged[1]));
    await waitFor(() => expect(screen.queryByRole("alert")).not.toBeInTheDocument());
    await screen.findByText(/本次查询尚未找到创建记录/u);
    expect(client.inspectThreadCreationRequest).toHaveBeenCalledTimes(1);
    expect(client.inspectThreadCreationRequest.mock.calls[0].slice(0, 2)).toEqual([workspaceID, `v2-thread-create-${intent.operationID}`]);
    expect(view.store().read(`creation:${intent.operationID}`, null)).toEqual(intent);
    act(() => { view.store().write("draft:one", "keep typing"); view.store().write("draft:two", "more typing"); });
    expect(client.inspectThreadCreationRequest).toHaveBeenCalledTimes(1);
    expect(view.store().read(`creation:${intent.operationID}`, null)).toEqual(intent);
    expect(client.createThread).not.toHaveBeenCalled();
  });

  it("keeps an invalid creation blocked after the damaged image record is discarded", async () => {
    const client = fixture();
    const view = mount(client);
    const intent = prepare(view);
    const altered = { ...intent, content: "does not match the original goal" };
    act(() => {
      view.store().write(`creation:${intent.operationID}`, altered);
      view.store().write("image-upload:damaged", false);
      view.store().reject("image-upload:damaged");
    });
    const globalGuard = vi.spyOn(view.store(), "assertReadable");
    fireEvent.click(screen.getByRole("button", { name: "重新核对" }));
    await screen.findByRole("alert");
    expect(screen.getByRole("alert")).toHaveTextContent("原创建请求无法可靠核对");
    expect(globalGuard).toHaveBeenCalledWith();
    act(() => view.store().remove("image-upload:damaged"));
    await waitFor(() => expect(screen.getByRole("alert")).toHaveTextContent("原创建请求无法可靠核对"));
    expect(view.store().read(`creation:${intent.operationID}`, null)).toEqual(altered);
    expect(() => view.helper().prepare(request, request.goal, files, request.goal)).toThrow("原创建请求无法可靠核对");
    expect(client.inspectThreadCreationRequest).not.toHaveBeenCalled();
    expect(client.createThread).not.toHaveBeenCalled();
  });

  it("does not clear an explicit handoff failure or re-inspect unknown creations on draft writes", async () => {
    const client = fixture();
    client.inspectThreadCreationRequest.mockResolvedValue(found);
    const view = mount(client);
    const intent = prepare(view);
    fireEvent.click(screen.getByRole("button", { name: "重新核对" }));
    await screen.findByRole("button", { name: "打开原对话" });
    const remove = vi.spyOn(Storage.prototype, "removeItem").mockImplementationOnce(() => { throw new Error("denied"); });
    fireEvent.click(screen.getByRole("button", { name: "打开原对话" }));
    await waitFor(() => expect(screen.getByRole("alert")).toHaveTextContent("无法移除"));
    remove.mockRestore();
    act(() => view.store().write("draft:typing", "an unrelated edit"));
    expect(screen.getByRole("alert")).toHaveTextContent("无法移除");
    expect(client.inspectThreadCreationRequest).toHaveBeenCalledTimes(1);
    expect(view.store().read(`creation:${intent.operationID}`, null)).toEqual(intent);
  });

  it("recovers reference-only MCP input with the same selection and message key after a lost create response", async () => {
    const client = fixture();
    client.createThread.mockRejectedValueOnce(new Error("response lost"));
    const selected: MCPDraftReference[] = [{ id: "selected-prompt", label: "review", serverName: "Project MCP",
      reference: { server_id: "project-mcp", capability_fingerprint: "c".repeat(64), method: "prompts/get",
        arguments: { name: "review", arguments: { language: "Go with spaces" } } } }];
    const body = { ...request, goal: "MCP 内容" };
    const first = mount(client);
    let intent!: CreationIntent;
    act(() => { intent = first.helper().prepare(body, "", [], "", undefined, undefined, undefined, undefined, selected); });
    await act(async () => { await expect(first.helper().resolve(intent)).rejects.toThrow("response lost"); });
    first.unmount();
    client.inspectThreadCreationRequest.mockResolvedValue(found);
    const reopened = mount(client);
    await screen.findByRole("button", { name: "打开原对话" });
    fireEvent.click(screen.getByRole("button", { name: "打开原对话" }));
    await waitFor(() => expect(reopened.onRecovered).toHaveBeenCalledTimes(1));
    const input = reopened.onRecovered.mock.calls[0][3];
    expect(input).toMatchObject({ content: "", mcpReferences: selected,
      operationKey: `v2-thread-create-turn-${intent.operationID}` });
    expect(reopened.store().read(recoveryTurnKey(input), null)).toEqual(input);
    expect(client.createThread).toHaveBeenCalledTimes(1);
    expect(client.submitThreadTurn).not.toHaveBeenCalled();
  });

  it("retries a lost creation with its original web policy and distinguishes a changed policy", async () => {
    const client = fixture();
    client.createThread.mockRejectedValueOnce(new Error("response lost"));
    const originalRequest = { ...request, web_access_mode: "rules" as const, web_allowed_targets: ["docs.example.com"] };
    const first = mount(client);
    const original = prepare(first, originalRequest);
    await act(async () => { await expect(first.helper().resolve(original)).rejects.toThrow("response lost"); });
    first.unmount();
    const reopened = mount(client);
    const resumed = prepare(reopened, originalRequest);
    expect(resumed.operationID).toBe(original.operationID);
    const changed = prepare(reopened, { ...request, web_access_mode: "full", web_allowed_targets: [] });
    expect(changed.operationID).not.toBe(original.operationID);
    await act(async () => { await reopened.helper().resolve(resumed); });
    expect(client.createThread.mock.calls).toEqual([
      [originalRequest, `v2-thread-create-${original.operationID}`],
      [originalRequest, `v2-thread-create-${original.operationID}`],
    ]);
  });
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
        operationKey: `v2-thread-create-turn-${intent.operationID}`, createdAt: intent.createdAt }), true);
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

function render(ui: ReactElement, options?: RenderOptions) {
  localStorage.setItem("prayu.locale.v1", "zh-CN");
  const Wrapper = options?.wrapper;
  return renderComponent(ui, {...options, wrapper: ({children}) => <LocaleProvider>{Wrapper ? <Wrapper>{children}</Wrapper> : children}</LocaleProvider>});
}
