import { useEffect, useRef, useState, type Dispatch, type SetStateAction } from "react";
import { useQueryClient } from "@tanstack/react-query";
import { useV2RecoveryStore, type V2RecoveryStore } from "./recovery-storage";
import { v2FileReferenceKey, type V2FileReference } from "./components/file-context";
import type { V2TurnInput } from "./use-thread-turn";
import { validImageAttachments } from "../api/image-attachments";
import { v2ImageReferenceKey } from "./components/image-input";
import { getV2DraftDocument, readV2Draft, v2DraftScope } from "./draft-context";
import { validV2DraftVersion } from "./draft-version";
import { validFileAttachments, type WorkspaceFileAttachment } from "../api/file-attachments";
import { recoveryAttachmentsKey, v2AttachmentReferenceKey } from "./attachment-keys";
import { validMCPReferences } from "../api/mcp-references";

export const recoveryFilesKey = (workspace: string, thread: string) => `files:${JSON.stringify([workspace, thread])}`;
export const recoveryImagesKey = (workspace: string, thread: string) => `images:${JSON.stringify([workspace, thread])}`;
export const recoveryTurnKey = (input: V2TurnInput) => `turn:${input.operationKey}`;
const identity = (value: unknown): value is string => typeof value === "string" && /^[\w.-]{1,256}$/u.test(value);
export function validRecoveryFiles(value: unknown): value is V2FileReference[] {
  return Array.isArray(value) && value.length <= 4 && value.every((file) => file &&
    identity(file.id) && typeof file.path === "string" && file.path.length > 0 && file.path.length <= 4096 &&
    /^[a-f0-9]{64}$/u.test(file.digest) && typeof file.partial === "boolean" && typeof file.redacted === "boolean");
}
export function validRecoveryTurn(value: unknown): value is V2TurnInput {
  if (!value || typeof value !== "object") return false;
  const input = value as V2TurnInput;
  return identity(input.threadID) && identity(input.workspaceID) && identity(input.operationKey) &&
	(input.deliveryMode === undefined || (input.deliveryMode === "steer" && identity(input.sessionID) &&
		!input.files?.length && !input.images?.length && !input.attachments?.length && !input.mcpReferences?.length)) &&
	(input.deliveryMode === "steer" || input.sessionID === undefined) &&
    typeof input.content === "string" && (input.content.trim().length > 0 || (input.images?.length ?? 0) > 0 || (input.attachments?.length ?? 0) > 0 || (input.mcpReferences?.length ?? 0) > 0) &&
    new TextEncoder().encode(input.content).byteLength <= 16384 &&
    typeof input.createdAt === "string" && Number.isFinite(Date.parse(input.createdAt)) &&
    (input.draft === undefined || (typeof input.draft === "string" && input.draft.length <= 16384 && input.draft.trim() === input.content)) &&
    (input.files === undefined || validRecoveryFiles(input.files)) &&
    (input.images === undefined || validImageAttachments(input.images, input.workspaceID)) &&
    (input.attachments === undefined || validFileAttachments(input.attachments, input.workspaceID)) &&
    (input.mcpReferences === undefined || validMCPReferences(input.mcpReferences)) &&
    (input.draftVersion === undefined || validV2DraftVersion(input.draftVersion, input.workspaceID, input.threadID)) &&
    (input.replacesOperationKey === undefined || identity(input.replacesOperationKey));
}

// Compatibility state for connections without durable scope and text typed
// before a workspace is known. Known scopes use the complete draft document.
export function useV2Drafts(): [Record<string, string>, Dispatch<SetStateAction<Record<string, string>>>] {
  const store = useV2RecoveryStore();
  const [drafts, setState] = useState<Record<string, string>>((): Record<string, string> => {
    const unscoped = store?.read<unknown>("draft:new:", "");
    return typeof unscoped === "string" ? { "new:": unscoped } : {};
  });
  const current = useRef(drafts);
  const update: Dispatch<SetStateAction<Record<string, string>>> = (change) => {
    const next = { ...(typeof change === "function" ? change(current.current) : change) };
    for (const key of Object.keys(next)) {
      if (store && current.current[key] === undefined && key !== "new:") {
        const existing = store.read<unknown>(`draft:${key}`, "");
        if (typeof existing === "string" && existing) next[key] = existing;
      }
      const value = next[key];
      if (current.current[key] !== value) {
        try { store?.write(`draft:${key}`, value); } catch { /* Store exposes the save failure; keep editing locally. */ }
      }
    }
    current.current = next;
    setState(next);
  };
  return [drafts, update];
}

export function useV2RecoveryFiles() {
  const store = useV2RecoveryStore();
  const client = useQueryClient();
  const [initialized] = useState(() => {
    for (const [key, value] of [...(store?.entries<unknown>("files:") ?? []), ...(store?.entries<unknown>("images:") ?? []),
      ...(store?.entries<unknown>("attachments:") ?? [])]) {
      try {
        const images = key.startsWith("images:");
        const attachments = key.startsWith("attachments:");
        const scope: unknown = JSON.parse(key.slice(key.indexOf(":") + 1));
        if (!Array.isArray(scope) || scope.length !== 2 || !scope.every((id) => typeof id === "string") ||
          !(images ? validImageAttachments(value, scope[0]) : attachments ? validFileAttachments(value, scope[0]) : validRecoveryFiles(value))) { store?.reject(key); continue; }
        const queryKey = images ? v2ImageReferenceKey(scope[0], scope[1]) : attachments ?
          v2AttachmentReferenceKey(scope[0], scope[1]) : v2FileReferenceKey(scope[0], scope[1]);
        if (client.getQueryData(queryKey) === undefined) client.setQueryData(queryKey, value);
      } catch { store?.reject(key); }
    }
    return true;
  });
  useEffect(() => {
    if (!store || !initialized) return;
    return client.getQueryCache().subscribe(({ query }) => {
      const [version, kind, workspace, thread] = query.queryKey;
      if (version !== "v2" || !["draft-files", "draft-images", "draft-attachments"].includes(String(kind)) || typeof workspace !== "string" || typeof thread !== "string" ||
          !(kind === "draft-images" ? validImageAttachments(query.state.data, workspace) : kind === "draft-attachments" ?
            validFileAttachments(query.state.data, workspace) : validRecoveryFiles(query.state.data))) return;
      try { store.write(kind === "draft-images" ? recoveryImagesKey(workspace, thread) : kind === "draft-attachments" ?
        recoveryAttachmentsKey(workspace, thread) : recoveryFilesKey(workspace, thread), query.state.data); } catch { /* Exposed by the shared save notice. */ }
    });
  }, [store, client, initialized]);
}

export function remainingRecoveryAttachments(current: WorkspaceFileAttachment[], submitted: WorkspaceFileAttachment[] = []): WorkspaceFileAttachment[] {
  return current.filter((file) => !submitted.some((sent) => sent.id === file.id && sent.workspace_id === file.workspace_id &&
    sent.sha256 === file.sha256 && sent.byte_size === file.byte_size));
}

// Clear only the content and reference identities admitted by this request.
// Remove the journal entry last: a crash during cleanup is reconciled by GET.
export function settleRecoveryTurn(store: V2RecoveryStore | null, input: V2TurnInput, accepted: boolean) {
  if (!store) return;
  if (accepted && input.draftVersion) {
    const state = getV2DraftDocument(store).settle(input.draftVersion.scope, input.draftVersion.ref);
    if (state.error || !state.persisted) throw new Error(state.error ?? "提交结果已确认，但草稿清理尚未保存。原请求标识已保留。");
  } else if (accepted) {
    const document = getV2DraftDocument(store);
    const scope = v2DraftScope(input.workspaceID, input.threadID);
    const migrated = readV2Draft(document, store, scope);
    const draftKey = `draft:thread:${input.threadID}`;
    const draft = store.read<unknown>(draftKey, "");
    const filesKey = recoveryFilesKey(input.workspaceID, input.threadID);
    const files = store.read<unknown>(filesKey, []);
    const imagesKey = recoveryImagesKey(input.workspaceID, input.threadID);
    const images = store.read<unknown>(imagesKey, []);
    const attachmentsKey = recoveryAttachmentsKey(input.workspaceID, input.threadID);
    const attachments = store.read<unknown>(attachmentsKey, []);
    if (typeof draft !== "string") store.reject(draftKey);
    if (!validRecoveryFiles(files)) store.reject(filesKey);
    if (!validImageAttachments(images, input.workspaceID)) store.reject(imagesKey);
    if (!validFileAttachments(attachments, input.workspaceID)) store.reject(attachmentsKey);
    store.assertReadable();
    if (draft === (input.draft ?? input.content)) store.write(draftKey, "");
    if (validRecoveryFiles(files)) store.write(filesKey, files.filter(({ id }) => !input.files?.some((file) => file.id === id)));
    if (validImageAttachments(images, input.workspaceID)) store.write(imagesKey, images.filter(({ id }) => !input.images?.some((image) => image.id === id)));
    if (validFileAttachments(attachments, input.workspaceID)) store.write(attachmentsKey, remainingRecoveryAttachments(attachments, input.attachments));
    // Pre-version journals cannot prove ownership of an edited document. Only
    // consume an untouched migration whose complete snapshot is still exact.
    if (!migrated.ref && migrated.snapshot.text === (input.draft ?? input.content) &&
        JSON.stringify(migrated.snapshot.files) === JSON.stringify(input.files ?? []) &&
        JSON.stringify(migrated.snapshot.images) === JSON.stringify(input.images ?? []) &&
        JSON.stringify(migrated.snapshot.attachments ?? []) === JSON.stringify(input.attachments ?? []) &&
        JSON.stringify(migrated.snapshot.mcpReferences ?? []) === JSON.stringify(input.mcpReferences ?? [])) {
      const state = document.update(scope, { text: "", files: [], images: [], ...(input.attachments?.length ? { attachments: [] } : {}), ...(input.mcpReferences?.length ? { mcpReferences: [] } : {}) }, null);
      if (!state.persisted || state.error) throw new Error(state.error ?? "原草稿清理尚未保存。");
    }
  }
  store.remove(recoveryTurnKey(input));
}
