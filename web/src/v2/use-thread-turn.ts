import { useMutation, useMutationState, useQueryClient } from "@tanstack/react-query";
import { useMemo, useState } from "react";
import { APIRequestError, type APIClient } from "../api/client";
import { v2QueryKeys } from "./query-keys";
import { v2FileReferenceKey, type V2FileReference } from "./components/file-context";
import { useV2RecoveryStore } from "./recovery-storage";
import { recoveryTurnKey, remainingRecoveryAttachments, settleRecoveryTurn, validRecoveryTurn } from "./recovery-session";
import { imageIdentities, type WorkspaceImageAttachment } from "../api/image-attachments";
import { v2ImageReferenceKey } from "./components/image-input";
import type { V2DraftVersion } from "./draft-version";
import { fileAttachmentIdentities, validFileAttachments, type WorkspaceFileAttachment } from "../api/file-attachments";
import { v2AttachmentReferenceKey } from "./attachment-keys";
import { mcpReferenceIdentities, validMCPReferences, type MCPDraftReference } from "../api/mcp-references";
import { v2MCPReferenceKey } from "./components/mcp-reference-picker";

export interface V2TurnInput {
  threadID: string;
  workspaceID: string;
  content: string;
  draft?: string;
  draftVersion?: V2DraftVersion;
  operationKey: string;
  createdAt: string;
  deliveryMode?: "steer";
  sessionID?: string;
  files?: V2FileReference[];
  images?: WorkspaceImageAttachment[];
  attachments?: WorkspaceFileAttachment[];
  mcpReferences?: MCPDraftReference[];
  replacesOperationKey?: string;
}

// Send may first confirm an older request. Its error belongs to that request,
// not to the draft that was visible when the user clicked Send.
export class V2SubmissionError extends Error {
  readonly submission: Pick<V2TurnInput, "threadID" | "workspaceID" | "operationKey">;

  constructor(input: V2TurnInput, readonly reason: unknown) {
    super(reason instanceof Error ? reason.message : "消息发送失败");
    this.name = "V2SubmissionError";
    this.submission = { threadID: input.threadID, workspaceID: input.workspaceID, operationKey: input.operationKey };
  }
}

export const v2TurnWasNotQueued = (error: unknown) =>
  error instanceof APIRequestError && error.messageQueued === false;

export const v2TurnFailed = (error: unknown) =>
  error instanceof APIRequestError && error.turnFailed === true;

export const v2TurnOutcomeKnown = (error: unknown) => v2TurnWasNotQueued(error) || v2TurnFailed(error);

const mutationKey = ["v2", "submit-turn"] as const;

export class V2RecoveredSubmissionError extends Error {
  constructor() { super("上次退出时提交结果尚待确认。请使用“重试核对”查看原提交记录。"); }
}

export function registerV2RecoveredTurn(client: ReturnType<typeof useQueryClient>, input: V2TurnInput) {
  const cache = client.getMutationCache();
  if (cache.findAll({ mutationKey }).some((item) =>
    (item.state.variables as V2TurnInput | undefined)?.operationKey === input.operationKey)) return;
  cache.build(client, { mutationKey, gcTime: Infinity, retry: false }, {
    context: undefined, data: undefined, variables: input, status: "error", isPaused: false,
    failureCount: 0, failureReason: null, error: new V2RecoveredSubmissionError(),
    submittedAt: Date.parse(input.createdAt),
  });
}

export function useV2RestoreTurns() {
  const store = useV2RecoveryStore();
  const client = useQueryClient();
  useState(() => {
    for (const [key, input] of store?.entries<unknown>("turn:") ?? []) {
      if (!validRecoveryTurn(input) || key !== recoveryTurnKey(input)) { store?.reject(key); continue; }
      registerV2RecoveredTurn(client, input);
    }
    return true;
  });
}

export function removeV2Submission(client: ReturnType<typeof useQueryClient>, input: V2TurnInput) {
  const cache = client.getMutationCache();
  for (const previous of cache.findAll({ mutationKey })) {
    const variables = previous.state.variables as V2TurnInput | undefined;
    if (variables?.threadID === input.threadID && variables.operationKey === input.operationKey) cache.remove(previous);
  }
}

// Mutation lifetime belongs to the QueryClient, so changing views does not hide
// an in-flight submission or its failure. Execution remains owned by Go.
export function useV2ThreadTurn(client: APIClient) {
  const queryClient = useQueryClient();
  const recovery = useV2RecoveryStore();
  return useMutation({
    mutationKey,
    // Drafts survive view changes for this page's lifetime. Their unresolved
    // operation identities must survive equally long, including first turns.
    gcTime: Infinity,
    // A lost response is reconciled with the exact original intent once. The
    // server owns deduplication; a known failed turn is never executed again.
    retry: (failureCount, error) => failureCount < 1 && !v2TurnOutcomeKnown(error),
    retryDelay: 400,
    mutationFn: async (input: V2TurnInput) => {
      if (input.attachments !== undefined && !validFileAttachments(input.attachments, input.workspaceID)) {
        throw new Error("文件附件的项目或保存身份无效，未发送消息。");
      }
      if (input.mcpReferences !== undefined && !validMCPReferences(input.mcpReferences)) throw new Error("MCP 引用身份无效，请重新选择。");
      if (recovery) {
        for (const [key, previous] of recovery.entries<unknown>("turn:")) {
          if (!validRecoveryTurn(previous) || key !== recoveryTurnKey(previous)) throw new Error("原提交记录内容异常，未发送新消息。原数据已保留。");
        }
        recovery.entries("creation:");
        recovery.assertReadable();
        if (!validRecoveryTurn(input)) throw new Error("提交内容无法可靠保存，请检查原消息与文件引用。");
        const previous = recovery.read<V2TurnInput | null>(recoveryTurnKey(input), null);
        if (previous && JSON.stringify(previous) !== JSON.stringify(input)) throw new Error("原提交标识已经绑定其他内容，未发送消息。");
        // Persist before touching the network, including the first Thread turn.
        recovery.write(recoveryTurnKey(input), input);
      }
      if (input.deliveryMode === "steer") {
        if (!input.sessionID || input.files?.length || input.images?.length || input.attachments?.length || input.mcpReferences?.length) {
          throw new Error("更新当前任务仅支持文字；附件和引用仍保留在草稿中。");
        }
        return client.submitSessionMessage(input.sessionID, {
          version: "session_message_submission.v1", content: input.content,
          delivery_mode: "steer",
        }, input.operationKey);
      }
      return client.submitThreadTurn(input.threadID, {
        version: "thread_message_submission.v1", content: input.content,
        ...(input.files?.length ? { files: input.files.map((file) => ({
          source_kind: "workspace_file" as const, path: file.path, expected_sha256: file.digest,
        })) } : {}),
        ...(input.images?.length ? { images: imageIdentities(input.images) } : {}),
        ...(input.attachments?.length ? { attachments: fileAttachmentIdentities(input.attachments) } : {}),
        ...(input.mcpReferences?.length ? { mcp_references: mcpReferenceIdentities(input.mcpReferences) } : {}),
      }, input.operationKey);
    },
    onSuccess: (_data, input) => {
      try { settleRecoveryTurn(recovery, input, true); } catch { /* Keep the journal until a later read confirms it. */ }
      // Versioned drafts are consumed by their exact branch/ref in settlement.
      // Keep identity-based cache cleanup only for legacy journal entries.
      if (!input.draftVersion) {
        queryClient.setQueryData<MCPDraftReference[]>(v2MCPReferenceKey(input.workspaceID, input.threadID),
          (current) => current?.filter(({ id }) => !input.mcpReferences?.some((item) => item.id === id)));
        queryClient.setQueryData<V2FileReference[]>(v2FileReferenceKey(input.workspaceID, input.threadID),
          (current) => current?.filter(({ id }) => !input.files?.some((file) => file.id === id)));
        queryClient.setQueryData<WorkspaceImageAttachment[]>(v2ImageReferenceKey(input.workspaceID, input.threadID),
          (current) => current?.filter(({ id }) => !input.images?.some((image) => image.id === id)));
        queryClient.setQueryData<WorkspaceFileAttachment[]>(v2AttachmentReferenceKey(input.workspaceID, input.threadID),
          (current) => current && remainingRecoveryAttachments(current, input.attachments));
      }
      // A terminal server rejection cannot later enqueue this intent. Only an
      // explicit correction chain may replace it; transport failures stay unknown.
      const cache = queryClient.getMutationCache();
      for (const previous of cache.findAll({ mutationKey })) {
        const variables = previous.state.variables as V2TurnInput | undefined;
        if (previous.state.status === "error" && variables?.threadID === input.threadID &&
          variables.operationKey === input.operationKey) cache.remove(previous);
      }
      let replaced = input.replacesOperationKey;
      const visited = new Set<string>();
      while (replaced && !visited.has(replaced)) {
        visited.add(replaced);
        const failures = cache.findAll({ mutationKey }).filter((previous) => {
          const variables = previous.state.variables as V2TurnInput | undefined;
          return variables?.threadID === input.threadID && variables.operationKey === replaced;
        });
        if (!v2TurnOutcomeKnown(failures.at(-1)?.state.error)) break;
        replaced = (failures.at(-1)?.state.variables as V2TurnInput | undefined)?.replacesOperationKey;
        failures.forEach((previous) => cache.remove(previous));
      }
    },
    onError: (error, input) => {
      if (v2TurnOutcomeKnown(error)) {
        try { settleRecoveryTurn(recovery, input, v2TurnFailed(error)); } catch { /* Original identity stays recoverable. */ }
        if (v2TurnFailed(error) && !input.draftVersion) {
          queryClient.setQueryData<MCPDraftReference[]>(v2MCPReferenceKey(input.workspaceID, input.threadID),
            (current) => current?.filter(({ id }) => !input.mcpReferences?.some((item) => item.id === id)));
          queryClient.setQueryData<V2FileReference[]>(v2FileReferenceKey(input.workspaceID, input.threadID),
            (current) => current?.filter(({ id }) => !input.files?.some((file) => file.id === id)));
          queryClient.setQueryData<WorkspaceImageAttachment[]>(v2ImageReferenceKey(input.workspaceID, input.threadID),
            (current) => current?.filter(({ id }) => !input.images?.some((image) => image.id === id)));
          queryClient.setQueryData<WorkspaceFileAttachment[]>(v2AttachmentReferenceKey(input.workspaceID, input.threadID),
            (current) => current && remainingRecoveryAttachments(current, input.attachments));
        }
      }
    },
    onSettled: async (_data, error, input) => {
      await Promise.allSettled([
        queryClient.invalidateQueries({ queryKey: v2QueryKeys.threads("active") }),
        queryClient.invalidateQueries({ queryKey: v2QueryKeys.thread(input.threadID) }),
        queryClient.invalidateQueries({ queryKey: v2QueryKeys.transcript(input.threadID) }),
      ]);
      if (!error) {
        const cache = queryClient.getMutationCache();
        for (const completed of cache.findAll({ mutationKey })) {
          if (completed.state.variables === input) cache.remove(completed);
        }
      }
    },
  });
}

export function useV2ThreadSubmissions(threadID: string) {
  const states = useMutationState({
    filters: { mutationKey, predicate: (mutation) => {
      const input = mutation.state.variables as V2TurnInput | undefined;
      return input?.threadID === threadID;
    } },
    select: (mutation) => ({
      input: mutation.state.variables as V2TurnInput,
      pending: mutation.state.status === "pending",
      error: mutation.state.error,
      failureReason: mutation.state.failureReason,
      status: mutation.state.status,
    }),
  });
  return useMemo(() => [...new Map(states.map((state) => [state.input.operationKey, {
    ...state,
    reconciling: state.pending && (Boolean(state.failureReason) || states.some((previous) =>
      previous.input.operationKey === state.input.operationKey && previous.error &&
      !v2TurnOutcomeKnown(previous.error))),
  }])).values()].filter((state) => state.status === "pending" || state.status === "error"), [states]);
}
