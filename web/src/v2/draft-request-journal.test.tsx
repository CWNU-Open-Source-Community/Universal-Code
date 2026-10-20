import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act, cleanup, fireEvent, render, renderHook, screen, waitFor } from "@testing-library/react";
import type { PropsWithChildren } from "react";
import { APIRequestError, type APIClient } from "../api/client";
import type { WorkspaceImageAttachment } from "../api/image-attachments";
import type { ThreadCreationControlRequestView, ThreadView } from "../api/types";
import { v2FileReferenceKey, type V2FileReference } from "./components/file-context";
import { v2ImageReferenceKey } from "./components/image-input";
import { validV2DraftVersion, type V2DraftVersion } from "./draft-version";
import { useV2CreationRecovery, type CreationIntent } from "./recovery-creation";
import { recoveryTurnKey } from "./recovery-session";
import { useV2RecoveryStore, V2RecoveryProvider, type V2RecoveryStore } from "./recovery-storage";
import { registerV2RecoveredTurn, useV2ThreadTurn, type V2TurnInput } from "./use-thread-turn";

const workspaceID = "workspace-draft-journal";
const threadID = "thread-draft-journal";
const originalText = "Inspect the original attachments";
const request: ThreadCreationControlRequestView = { version: "thread_creation.v1", workspace_id: workspaceID,
  goal: originalText, profile: "code", surface: "code", phase: "deliver", network_mode: "disabled" };
const version: V2DraftVersion = { scope: { key: `new:${workspaceID}`, workspaceID }, ref: { branchID: "branch-original", seq: 3 } };
const files: V2FileReference[] = [{ id: "file-original", path: "README.md", digest: "a".repeat(64), partial: false, redacted: true }];
const image: WorkspaceImageAttachment = { id: "image-original", workspace_id: workspaceID, sha256: "b".repeat(64),
  mime_type: "image/png", byte_size: 128, width: 64, height: 64, name: "原图.png" };
const thread: ThreadView = { id: threadID, workspace_id: workspaceID, mission_id: "mission-draft-journal",
  active_run_id: "run-draft-journal", last_run_id: "run-draft-journal", protocol_version: "thread.v1", title: originalText,
  status: "active", composer_state: "ready", version: 1, created_at: "2026-09-12T00:00:00Z", updated_at: "2026-09-12T00:00:00Z" };
const absent = { kind: "creation", workspace_id: workspaceID, state: "not_received", settled: false };
const found = { kind: "creation", workspace_id: workspaceID, state: "completed", settled: true,
  thread_id: threadID, run_id: thread.last_run_id, session_id: "session-draft-journal", request_fingerprint: "c".repeat(64) };
const clone = <T,>(value: T): T => JSON.parse(JSON.stringify(value)) as T;

function creationFixture() {
  return { baseURL: "/api/v1", inspectThreadCreationRequest: vi.fn().mockResolvedValue(absent),
    get: vi.fn().mockResolvedValue({ thread }), createThread: vi.fn().mockResolvedValue({ thread }),
    submitThreadTurn: vi.fn(), executeRun: vi.fn() };
}
function mountCreation(client: ReturnType<typeof creationFixture>) {
  let helper!: ReturnType<typeof useV2CreationRecovery>;
  let store!: V2RecoveryStore;
  const onRecovered = vi.fn();
  function Probe() {
    store = useV2RecoveryStore()!;
    helper = useV2CreationRecovery(client as unknown as APIClient, workspaceID, onRecovered);
    return helper.notice;
  }
  const view = render(<V2RecoveryProvider client={client} scopeID="draft-journal-db"><Probe /></V2RecoveryProvider>);
  const prepare = (draftVersion?: V2DraftVersion) => {
    let intent!: CreationIntent;
    act(() => { intent = helper.prepare(request, originalText, files, `  ${originalText}\n`, [image], draftVersion); });
    return intent;
  };
  return { ...view, prepare, helper: () => helper, store: () => store, onRecovered };
}

function turnFixture(recovery = true) {
  const submitThreadTurn = vi.fn().mockResolvedValue({});
  const client = { baseURL: "/api/v1", submitThreadTurn } as unknown as APIClient;
  const queries = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  let store: V2RecoveryStore | null = null;
  const wrapper = ({ children }: PropsWithChildren) => <QueryClientProvider client={queries}>
    <V2RecoveryProvider client={client} scopeID={recovery ? "draft-journal-db" : undefined}>{children}</V2RecoveryProvider>
  </QueryClientProvider>;
  const view = renderHook(() => { store = useV2RecoveryStore(); return useV2ThreadTurn(client); }, { wrapper });
  return { ...view, client, submitThreadTurn, queries, store: () => store };
}
const turnInput = (): V2TurnInput => ({ threadID, workspaceID, operationKey: "original-turn-key",
  createdAt: "2026-09-12T00:00:00Z", content: originalText, draft: `  ${originalText}\n`, files, images: [image],
  draftVersion: { scope: { key: `thread:${threadID}`, workspaceID }, ref: { branchID: "branch-turn-original", seq: 2 } } });

beforeEach(() => localStorage.clear());
afterEach(() => { cleanup(); vi.restoreAllMocks(); });

it("validates the exact draft reference shape and permits only the creation or destination Thread scope", () => {
  expect(validV2DraftVersion(version, workspaceID)).toBe(true);
  expect(validV2DraftVersion(version, workspaceID, threadID)).toBe(true);
  const current = turnInput().draftVersion!;
  expect(validV2DraftVersion(current, workspaceID, threadID)).toBe(true);
  expect(validV2DraftVersion(current, workspaceID)).toBe(false);
  expect(validV2DraftVersion(current, workspaceID, "other-thread")).toBe(false);
  const malformed = [null, [], { ...version, extra: true }, { ...version, scope: { ...version.scope, extra: true } },
    { ...version, scope: { ...version.scope, workspaceID: "other-workspace" } },
    { ...version, scope: { ...version.scope, key: "new:other-workspace" } },
    { ...version, ref: { ...version.ref, extra: true } }, { ...version, ref: { branchID: "invalid/id", seq: 1 } },
    ...[0, -1, 1.5, "2", Number.MAX_SAFE_INTEGER + 1].map((seq) => ({ ...version, ref: { ...version.ref, seq } }))];
  for (const value of malformed) expect(validV2DraftVersion(value, workspaceID, threadID)).toBe(false);
});

it("keeps the original creation key and cleanup reference when later draft versions return to the same submitted body", () => {
  const client = creationFixture();
  const view = mountCreation(client);
  const mutable = clone(version);
  const original = view.prepare(mutable);
  expect(view.prepare({ ref: { seq: 3, branchID: version.ref.branchID }, scope: { workspaceID, key: version.scope.key } }).operationID)
    .toBe(original.operationID);
  const next = view.prepare({ ...version, ref: { ...version.ref, seq: 4 } });
  const other = view.prepare({ ...version, ref: { branchID: "other-branch", seq: 3 } });
  const legacy = view.prepare();
  for (const candidate of [next, other, legacy]) {
    expect(candidate.operationID).toBe(original.operationID);
    expect(candidate.draftVersion).toEqual(version);
  }
  mutable.ref.seq = 99;
  expect(original.draftVersion).toEqual(version);
  expect(view.store().read(`creation:${original.operationID}`, null)).toEqual(original);
  expect(client.createThread).not.toHaveBeenCalled();
});

it("recovers a lost creation by GET and hands off the original new-dialogue reference without putting it in the API body", async () => {
  const client = creationFixture();
  client.createThread.mockRejectedValueOnce(new Error("lost creation response"));
  const first = mountCreation(client);
  const intent = first.prepare(version);
  await act(async () => { await expect(first.helper().resolve(intent)).rejects.toThrow("lost creation response"); });
  expect(client.createThread).toHaveBeenCalledWith(request, `v2-thread-create-${intent.operationID}`);
  first.unmount();
  client.inspectThreadCreationRequest.mockResolvedValue(found);
  const reopened = mountCreation(client);
  // A new local sequence must first observe the old operation, not reserve a
  // second creation key merely because the editor's cleanup identity advanced.
  const samePayload = reopened.prepare({ ...version, ref: { ...version.ref, seq: 4 } });
  expect(samePayload.operationID).toBe(intent.operationID);
  expect(samePayload.draftVersion).toEqual(version);
  await act(async () => { expect(await reopened.helper().resolve(samePayload)).toEqual(thread); });
  expect(client.createThread).toHaveBeenCalledTimes(1);
  fireEvent.click(await screen.findByRole("button", { name: "打开原对话" }));
  const input = reopened.onRecovered.mock.calls[0][3] as V2TurnInput;
  expect(input).toMatchObject({ threadID, workspaceID, draftVersion: version, files, images: [image],
    operationKey: `v2-thread-create-turn-${intent.operationID}`, draft: intent.submittedDraft });
  expect(reopened.store().read(recoveryTurnKey(input), null)).toEqual(input);
  expect(reopened.store().entries("creation:")).toEqual([]);
  expect(client.createThread).toHaveBeenCalledTimes(1);
  expect(client.submitThreadTurn).not.toHaveBeenCalled();
  const queries = new QueryClient();
  registerV2RecoveredTurn(queries, input);
  expect(queries.getMutationCache().getAll()[0].state.variables).toEqual(input);
  expect(client.submitThreadTurn).not.toHaveBeenCalled();
  queries.clear();
});

it("refuses changed or corrupt creation references under an original key before observing or creating", async () => {
  const client = creationFixture();
  const first = mountCreation(client);
  const intent = first.prepare(version);
  const altered = { ...intent, draftVersion: { ...version, ref: { ...version.ref, seq: 4 } } };
  await act(async () => { await expect(first.helper().resolve(altered)).rejects.toThrow("无法可靠核对"); });
  expect(client.inspectThreadCreationRequest).not.toHaveBeenCalled();
  const corrupt = { ...intent, draftVersion: { ...version, ref: { ...version.ref, seq: 0 } } };
  first.store().write(`creation:${intent.operationID}`, corrupt);
  first.unmount();
  const reopened = mountCreation(client);
  expect(await screen.findByRole("alert")).toHaveTextContent("无法可靠核对");
  expect(reopened.store().read(`creation:${intent.operationID}`, null)).toEqual(corrupt);
  expect(client.inspectThreadCreationRequest).not.toHaveBeenCalled();
  expect(client.createThread).not.toHaveBeenCalled();
});

it("writes the original version ahead of the network and leaves attachment cache cleanup to exact draft settlement", async () => {
  const view = turnFixture();
  const input = turnInput();
  let finish!: () => void;
  view.submitThreadTurn.mockImplementation(() => {
    expect(view.store()!.read(recoveryTurnKey(input), null)).toEqual(input);
    return new Promise<void>((resolve) => { finish = resolve; });
  });
  const laterFile = { ...files[0], id: "file-later", path: "later.md" };
  const laterImage = { ...image, id: "image-later", sha256: "d".repeat(64) };
  let pending!: Promise<unknown>;
  act(() => { pending = view.result.current.mutateAsync(input); });
  await waitFor(() => expect(view.submitThreadTurn).toHaveBeenCalledTimes(1));
  view.queries.setQueryData(v2FileReferenceKey(workspaceID, threadID), [...files, laterFile]);
  view.queries.setQueryData(v2ImageReferenceKey(workspaceID, threadID), [image, laterImage]);
  await act(async () => { finish(); await pending; });
  expect(view.submitThreadTurn).toHaveBeenCalledWith(threadID, { version: "thread_message_submission.v1", content: originalText,
    files: [{ source_kind: "workspace_file", path: files[0].path, expected_sha256: files[0].digest }],
    images: [{ id: image.id, sha256: image.sha256 }] }, input.operationKey);
  // The selected branch can reuse the original attachment identities. The old
  // request must not filter them merely because its own version was accepted.
  expect(view.queries.getQueryData(v2FileReferenceKey(workspaceID, threadID))).toEqual([...files, laterFile]);
  expect(view.queries.getQueryData(v2ImageReferenceKey(workspaceID, threadID))).toEqual([image, laterImage]);
  view.queries.clear();
});

it.each([true, false])("retains versioned attachments after a sealed turn failure while preserving legacy cleanup (versioned: %s)", async (versioned) => {
  const view = turnFixture(false);
  const input = turnInput();
  if (!versioned) delete input.draftVersion;
  view.queries.setQueryData(v2FileReferenceKey(workspaceID, threadID), files);
  view.queries.setQueryData(v2ImageReferenceKey(workspaceID, threadID), [image]);
  view.submitThreadTurn.mockRejectedValue(new APIRequestError("recorded failure", "FAILED_PRECONDITION", 412, "failed", undefined, undefined, true));
  await act(async () => { await expect(view.result.current.mutateAsync(input)).rejects.toThrow("recorded failure"); });
  expect(view.queries.getQueryData(v2FileReferenceKey(workspaceID, threadID))).toEqual(versioned ? files : []);
  expect(view.queries.getQueryData(v2ImageReferenceKey(workspaceID, threadID))).toEqual(versioned ? [image] : []);
  expect(view.submitThreadTurn).toHaveBeenCalledTimes(1);
  view.queries.clear();
});

it("keeps the original key, payload and draft reference through transport retry despite a changed current draft", async () => {
  const view = turnFixture();
  const input = turnInput();
  view.submitThreadTurn.mockImplementationOnce(async () => {
    view.store()!.write(`draft:thread:${threadID}`, "Newer text must not govern the original request");
    throw new Error("response lost");
  });
  await act(async () => { await view.result.current.mutateAsync(input); });
  expect(view.submitThreadTurn).toHaveBeenCalledTimes(2);
  expect(view.submitThreadTurn.mock.calls[1]).toEqual(view.submitThreadTurn.mock.calls[0]);
  expect(view.store()!.read(`draft:thread:${threadID}`, "")).toBe("Newer text must not govern the original request");
  expect(input.draftVersion).toEqual(turnInput().draftVersion);
  view.queries.clear();
});

it("does not rebind an already saved turn key to the same text from another draft version", async () => {
  const view = turnFixture();
  const input = turnInput();
  view.store()!.write(recoveryTurnKey(input), input);
  const altered = { ...input, draftVersion: { ...input.draftVersion!, ref: { ...input.draftVersion!.ref, seq: 3 } } };
  await act(async () => { await expect(view.result.current.mutateAsync(altered)).rejects.toThrow("原提交标识已经绑定其他内容"); });
  expect(view.store()!.read(recoveryTurnKey(input), null)).toEqual(input);
  expect(view.submitThreadTurn).not.toHaveBeenCalled();
  view.queries.clear();
});

vi.mock("../lib/locale", () => ({ useLocale: () => ({ t: (chinese: string) => chinese, locale: "zh-CN" }) }));
