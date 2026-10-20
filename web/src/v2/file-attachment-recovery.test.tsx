import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act, cleanup, fireEvent, render, renderHook, screen, waitFor } from "@testing-library/react";
import type { PropsWithChildren } from "react";
import type { APIClient } from "../api/client";
import type { WorkspaceFileAttachment } from "../api/file-attachments";
import type { ThreadCreationControlRequestView, ThreadView } from "../api/types";
import { recoveryAttachmentsKey, v2AttachmentReferenceKey } from "./attachment-keys";
import { getV2DraftDocument, v2DraftScope } from "./draft-context";
import { inspectV2TurnRequest } from "./recovery-api";
import { useV2CreationRecovery, type CreationIntent } from "./recovery-creation";
import { recoveryTurnKey, settleRecoveryTurn, useV2RecoveryFiles, validRecoveryTurn } from "./recovery-session";
import { V2RecoveryProvider, useV2RecoveryStore, type V2RecoveryStore } from "./recovery-storage";
import { useV2RestoreTurns, useV2ThreadSubmissions, useV2ThreadTurn, type V2TurnInput } from "./use-thread-turn";

const workspaceID = "workspace-file-pipeline";
const threadID = "thread-file-pipeline";
const file: WorkspaceFileAttachment = { id: "uploaded-original", workspace_id: workspaceID, name: "需求.txt",
  mime_type: "text/plain", sha256: "a".repeat(64), byte_size: 32, readability: "text", text_sha256: "b".repeat(64),
  text_bytes: 32, redacted: false };
const later: WorkspaceFileAttachment = { id: "uploaded-later", workspace_id: workspaceID, name: "未解析.pdf", sha256: "c".repeat(64),
  mime_type: "application/pdf", byte_size: 32, readability: "stored_only", redacted: false, text_bytes: 0, reason: "格式暂不支持解析" };
const request: ThreadCreationControlRequestView = { version: "thread_creation.v1", workspace_id: workspaceID,
  goal: "文件对话", profile: "code", surface: "code", phase: "deliver", network_mode: "disabled" };
const thread: ThreadView = { id: threadID, workspace_id: workspaceID, mission_id: "mission-file-pipeline",
  active_run_id: "run-file-pipeline", last_run_id: "run-file-pipeline", protocol_version: "thread.v1", title: request.goal,
  status: "active", composer_state: "ready", version: 1, created_at: "2026-09-12T00:00:00Z", updated_at: "2026-09-12T00:00:00Z" };
const input = (): V2TurnInput => ({ threadID, workspaceID, content: "", draft: "", attachments: [file],
  operationKey: "original-file-turn", createdAt: "2026-09-12T00:00:00Z" });
const fixture = () => ({ baseURL: "/api/v1", createThread: vi.fn().mockResolvedValue({ thread }),
  get: vi.fn().mockResolvedValue({ thread }), submitThreadTurn: vi.fn().mockResolvedValue({}),
  inspectThreadCreationRequest: vi.fn().mockResolvedValue({ kind: "creation", state: "not_received", settled: false, workspace_id: workspaceID }),
  inspectThreadTurnRequest: vi.fn().mockResolvedValue({ kind: "turn", state: "not_received", settled: false, workspace_id: workspaceID, thread_id: threadID }) });

function creation(client: ReturnType<typeof fixture>) {
  let helper!: ReturnType<typeof useV2CreationRecovery>;
  let store!: V2RecoveryStore;
  const recovered = vi.fn();
  function Probe() {
    store = useV2RecoveryStore()!;
    helper = useV2CreationRecovery(client as unknown as APIClient, workspaceID, recovered);
    return helper.notice;
  }
  const page = render(<V2RecoveryProvider client={client} scopeID="attachment-pipeline"><Probe /></V2RecoveryProvider>);
  return { ...page, helper: () => helper, store: () => store, recovered };
}

function turns(client: ReturnType<typeof fixture>, restore = false) {
  const queries = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  let store!: V2RecoveryStore;
  const wrapper = ({ children }: PropsWithChildren) => <QueryClientProvider client={queries}>
    <V2RecoveryProvider client={client} scopeID="attachment-pipeline">{children}</V2RecoveryProvider>
  </QueryClientProvider>;
  function useProbe() {
    store = useV2RecoveryStore()!;
    useV2RecoveryFiles();
    useV2RestoreTurns();
    return { turn: useV2ThreadTurn(client as unknown as APIClient), submissions: useV2ThreadSubmissions(threadID) };
  }
  const page = renderHook(useProbe, { wrapper });
  // The flag documents tests which deliberately start with a persisted journal.
  if (!restore) expect(client.submitThreadTurn).not.toHaveBeenCalled();
  return { ...page, queries, store: () => store };
}

beforeEach(() => localStorage.clear());
afterEach(() => { cleanup(); vi.restoreAllMocks(); });

it("reopens a lost attachment-only creation using GET and preserves the original file in its first-turn journal", async () => {
  const client = fixture();
  client.createThread.mockRejectedValueOnce(new Error("creation response lost"));
  const first = creation(client);
  let intent!: CreationIntent;
  act(() => { intent = first.helper().prepare(request, "", [], "", undefined, undefined, [file]); });
  await act(async () => { await expect(first.helper().resolve(intent)).rejects.toThrow("response lost"); });
  first.unmount();
  client.inspectThreadCreationRequest.mockResolvedValue({ kind: "creation", state: "completed", settled: true,
    workspace_id: workspaceID, thread_id: threadID, run_id: thread.last_run_id, session_id: "session-file-pipeline", request_fingerprint: "d".repeat(64) });
  const reopened = creation(client);
  fireEvent.click(await screen.findByRole("button", { name: "打开原对话" }));
  const saved = reopened.recovered.mock.calls[0][3] as V2TurnInput;
  expect(saved).toMatchObject({ content: "", attachments: [file], operationKey: `v2-thread-create-turn-${intent.operationID}` });
  expect(validRecoveryTurn(saved)).toBe(true);
  expect(reopened.store().read(recoveryTurnKey(saved), null)).toEqual(saved);
  expect(client.inspectThreadCreationRequest).toHaveBeenLastCalledWith(workspaceID, `v2-thread-create-${intent.operationID}`, expect.any(AbortSignal));
  expect(client.createThread).toHaveBeenCalledTimes(1);
  expect(client.submitThreadTurn).not.toHaveBeenCalled();
});

it("binds changed attachment hashes and sizes to distinct creation intents and refuses a foreign workspace", async () => {
  const client = fixture();
  const view = creation(client);
  const prepare = (attachment: WorkspaceFileAttachment) => {
    let intent!: CreationIntent;
    act(() => { intent = view.helper().prepare(request, "", [], "", undefined, undefined, [attachment]); });
    return intent;
  };
  const original = prepare(file);
  expect(prepare({ ...file }).operationID).toBe(original.operationID);
  const changed = prepare({ ...file, sha256: "e".repeat(64) });
  const changedSize = prepare({ ...file, byte_size: 33 });
  expect(new Set([original, changed, changedSize].map(({ operationID }) => operationID)).size).toBe(3);
  await act(async () => { await expect(view.helper().resolve({ ...original, attachments: [later] })).rejects.toThrow("无法可靠核对"); });
  expect(() => prepare({ ...file, workspace_id: "other-workspace" })).toThrow("无法可靠核对");
  expect(view.store().read(`creation:${original.operationID}`, null)).toEqual(original);
  expect(client.inspectThreadCreationRequest).not.toHaveBeenCalled();
  expect(client.createThread).not.toHaveBeenCalled();
});

it("hands off mixed text, project references, images and uploads into one exact first-turn submission", async () => {
  const client = fixture();
  const page = creation(client);
  const image = { id: "image-mixed", workspace_id: workspaceID, sha256: "f".repeat(64),
    mime_type: "image/png" as const, byte_size: 100, width: 40, height: 20, name: "截图.png" };
  const refs = [{ id: "project-reference", path: "README.md", digest: "e".repeat(64), partial: false, redacted: true }];
  let intent!: CreationIntent;
  act(() => { intent = page.helper().prepare({ ...request, goal: "图片对话" }, "", refs, "", [image], undefined, [file, later]); });
  await act(async () => { await page.helper().resolve(intent); });
  let sent!: V2TurnInput;
  act(() => { sent = page.helper().handoff(intent, thread); });
  expect(sent).toMatchObject({ content: "", files: refs, images: [image], attachments: [file, later] });
  page.unmount();
  const next = turns(client);
  await act(async () => { await next.result.current.turn.mutateAsync(sent); });
  expect(client.submitThreadTurn).toHaveBeenCalledTimes(1);
  expect(client.submitThreadTurn).toHaveBeenCalledWith(threadID, { version: "thread_message_submission.v1", content: "",
    files: [{ source_kind: "workspace_file", path: refs[0].path, expected_sha256: refs[0].digest }],
    images: [{ id: image.id, sha256: image.sha256 }],
    attachments: [file, later].map(({ id, workspace_id, sha256, byte_size }) => ({ id, workspace_id, sha256, byte_size })),
  }, sent.operationKey);
  next.queries.clear();
});

it("persists before sending exact attachment identities and clears only the accepted attachment after later additions", async () => {
  const client = fixture();
  const view = turns(client);
  const sent = input();
  let complete!: () => void;
  client.submitThreadTurn.mockImplementation(() => {
    expect(view.store().read(recoveryTurnKey(sent), null)).toEqual(sent);
    return new Promise<void>((resolve) => { complete = resolve; });
  });
  let pending!: Promise<unknown>;
  act(() => { pending = view.result.current.turn.mutateAsync(sent); });
  await waitFor(() => expect(client.submitThreadTurn).toHaveBeenCalledTimes(1));
  act(() => view.queries.setQueryData(v2AttachmentReferenceKey(workspaceID, threadID), [file, later]));
  view.store().write(`draft:thread:${threadID}`, "后来补充的要求");
  await act(async () => { complete(); await pending; });
  expect(client.submitThreadTurn).toHaveBeenCalledWith(threadID, { version: "thread_message_submission.v1", content: "",
    attachments: [{ id: file.id, workspace_id: workspaceID, sha256: file.sha256, byte_size: file.byte_size }] }, sent.operationKey);
  expect(view.queries.getQueryData(v2AttachmentReferenceKey(workspaceID, threadID))).toEqual([later]);
  expect(view.store().read(recoveryAttachmentsKey(workspaceID, threadID), [])).toEqual([later]);
  expect(view.store().read(`draft:thread:${threadID}`, "")).toBe("后来补充的要求");
  expect(view.store().read(recoveryTurnKey(sent), null)).toBeNull();
  view.queries.clear();
});

it("retains an unknown file-only payload across a new QueryClient and only GETs the original key when inspected", async () => {
  const client = fixture();
  const first = turns(client);
  const sent = input();
  client.submitThreadTurn.mockRejectedValue(new Error("response lost"));
  await act(async () => { await expect(first.result.current.turn.mutateAsync(sent)).rejects.toThrow("response lost"); });
  expect(client.submitThreadTurn).toHaveBeenCalledTimes(2);
  expect(client.submitThreadTurn.mock.calls[0]).toEqual(client.submitThreadTurn.mock.calls[1]);
  first.unmount(); first.queries.clear();
  const reopened = turns(client, true);
  expect(reopened.result.current.submissions[0].input).toEqual(sent);
  const observed = await inspectV2TurnRequest(client as unknown as APIClient, reopened.result.current.submissions[0].input);
  expect(observed.state).toBe("not_received");
  expect(client.inspectThreadTurnRequest).toHaveBeenCalledWith(threadID, sent.operationKey, undefined);
  expect(client.submitThreadTurn).toHaveBeenCalledTimes(2);
  expect(reopened.store().read(recoveryTurnKey(sent), null)).toEqual(sent);
  await act(async () => { await expect(reopened.result.current.turn.mutateAsync({ ...sent, attachments: [later] })).rejects.toThrow("已经绑定其他内容"); });
  expect(client.submitThreadTurn).toHaveBeenCalledTimes(2);
  reopened.queries.clear();
});

it("does not clear a versioned draft edited to another attachment even when its text is unchanged", () => {
  const client = fixture();
  const view = turns(client);
  const document = getV2DraftDocument(view.store());
  const scope = v2DraftScope(workspaceID, threadID);
  const original = document.update(scope, { text: "", files: [], images: [], attachments: [file] }, null);
  const sent = { ...input(), draftVersion: { scope, ref: original.ref! } };
  view.store().write(recoveryTurnKey(sent), sent);
  const edited = document.update(scope, { attachments: [later] }, original.ref);
  settleRecoveryTurn(view.store(), sent, true);
  expect(document.read(scope).ref).toEqual(edited.ref);
  expect(document.read(scope).snapshot.attachments).toEqual([later]);
  expect(view.store().read(recoveryTurnKey(sent), null)).toBeNull();
  view.unmount(); view.queries.clear();
  const reopened = turns(client);
  expect(getV2DraftDocument(reopened.store()).read(scope).snapshot.attachments).toEqual([later]);
  reopened.queries.clear();
});

it.each(["sha256", "byte_size"] as const)("legacy settlement preserves an attachment whose %s changed under the old ID", (field) => {
  const client = fixture();
  const view = turns(client);
  const sent = input();
  const replacement = { ...file, [field]: field === "sha256" ? "e".repeat(64) : file.byte_size + 1 };
  view.store().write(recoveryTurnKey(sent), sent);
  view.store().write(recoveryAttachmentsKey(workspaceID, threadID), [replacement]);
  settleRecoveryTurn(view.store(), sent, true);
  expect(view.store().read(recoveryAttachmentsKey(workspaceID, threadID), [])).toEqual([replacement]);
  expect(view.store().read(recoveryTurnKey(sent), null)).toBeNull();
  expect(client.submitThreadTurn).not.toHaveBeenCalled();
  view.queries.clear();
});

it("preserves corrupt attachment state and the original journal instead of clearing or submitting it", async () => {
  const client = fixture();
  const view = turns(client);
  const sent = input();
  const invalid = [{ ...file, workspace_id: "other-workspace" }];
  expect(validRecoveryTurn({ ...sent, attachments: invalid })).toBe(false);
  view.store().write(recoveryTurnKey(sent), sent);
  view.store().write(recoveryAttachmentsKey(workspaceID, threadID), invalid);
  expect(() => settleRecoveryTurn(view.store(), sent, true)).toThrow();
  expect(view.store().read(recoveryTurnKey(sent), null)).toEqual(sent);
  expect(view.store().read(recoveryAttachmentsKey(workspaceID, threadID), [])).toEqual(invalid);
  await act(async () => { await expect(view.result.current.turn.mutateAsync({ ...sent, attachments: invalid })).rejects.toThrow("保存身份无效"); });
  expect(client.submitThreadTurn).not.toHaveBeenCalled();
  view.queries.clear();
});

vi.mock("../lib/locale", () => ({ useLocale: () => ({ t: (chinese: string) => chinese, locale: "zh-CN" }) }));
