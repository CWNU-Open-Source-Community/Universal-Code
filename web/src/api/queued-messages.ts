import { APIRequestError } from "./client/protocol";
import { type APIClient } from "./client";
import { validFileAttachments, type WorkspaceFileAttachment } from "./file-attachments";
import { validImageAttachments, type WorkspaceImageAttachment } from "./image-attachments";

export interface QueueBinding { threadID: string; runID: string; sessionID: string; workspaceID: string }
export interface QueuedMessage {
  id: string; sequence: number; status: "pending"; prepared: boolean; content: string;
  delivery_mode?: "next_turn" | "steer";
  content_sha256: string; content_redacted: boolean; revision: number; created_at: string; edited_at?: string;
  images: WorkspaceImageAttachment[]; attachments: WorkspaceFileAttachment[]; can_edit: boolean; can_cancel: boolean;
}
export interface QueuedMessages {
  version: "thread_queued_messages.v1"; thread_id: string; run_id: string; session_id: string;
  pending: number; prepared: number; items: QueuedMessage[]; capability_grant: false;
  current_attempt_id?: string; execution_id?: string;
}
export interface QueuePromotionInput extends QueueRevisionInput { expectedAttemptID: string; expectedExecutionID: string }
export interface QueuePromotion {
  rejected?: false;
  version: "session_steering_promotion.v1"; run_id: string; session_id: string; message_id: string;
  receipt: { id: string; replacement_message_id: string; expected_revision: number; content_sha256: string;
    target_attempt_id: string; execution_id: string; cancellation_id: string; created_at: string };
  replayed: boolean; execution_started: false; model_called: false; tool_called: false; capability_grant: false;
}
export interface QueuePromotionRejection {
  version: "session_steering_promotion.v1"; run_id: string; session_id: string; message_id: string;
  receipt: { id: string; expected_revision: number; content_sha256: string; target_attempt_id: string; execution_id: string; created_at: string };
  rejected: true; replayed: boolean; execution_started: false; model_called: false; tool_called: false; capability_grant: false;
}
export type QueuePromotionObservation = { state: "sealed"; promotion: QueuePromotion } | { state: "rejected" } |
  { state: "absent"; message: QueueObservedMessage; executionObserved: boolean; executionID?: string };
export interface QueueRevision {
  version: "session_steering_revision.v1"; run_id: string; session_id: string; message_id: string;
  receipt: { id: string; from_revision: number; to_revision: number; old_content_sha256: string; new_content_sha256: string; created_at: string };
  replayed: boolean; execution_started: false; model_called: false; tool_called: false; capability_grant: false;
}
export interface QueueRevisionInput extends QueueBinding {
  messageID: string; expectedRevision: number; oldSHA256: string; content: string; operationKey: string;
}
export interface QueueObservedMessage { id: string; run_id: string; session_id: string; revision: number; status: "pending" | "committed" | "cancelled" }
export type QueueObservation = { state: "sealed" } | { state: "absent"; message: QueueObservedMessage };
const object = (value: unknown): value is Record<string, unknown> => !!value && typeof value === "object" && !Array.isArray(value);
export const queueIdentity = (value: unknown): value is string => typeof value === "string" && /^[\w.-]{1,256}$/u.test(value);
const digest = (value: unknown): value is string => typeof value === "string" && /^[a-f0-9]{64}$/u.test(value);
const integer = (value: unknown): value is number => Number.isSafeInteger(value) && Number(value) >= 0;
const timestamp = (value: unknown): value is string => typeof value === "string" && Number.isFinite(Date.parse(value));
const invalid = () => new Error("排队消息数据不完整或来源不匹配，请刷新核对；本地编辑稿已保留。");
export async function provesQueueRevisionUnchanged(error: unknown, input: QueueRevisionInput): Promise<boolean> {
  if (!(error instanceof APIRequestError) || error.status !== 400 || error.code !== "INVALID_ARGUMENT") return false;
  const proof = error.revisionUnchanged;
  if (!object(proof) || proof.version !== "queue_revision_unchanged.v1" || proof.run_id !== input.runID ||
    proof.session_id !== input.sessionID || proof.message_id !== input.messageID || proof.expected_revision !== input.expectedRevision ||
    !digest(input.oldSHA256) || proof.normalized_content_sha256 !== input.oldSHA256 || proof.current_content_sha256 !== input.oldSHA256 ||
    !digest(proof.operation_key_sha256) || !digest(proof.request_content_sha256) || proof.execution_started !== false ||
    proof.model_called !== false || proof.tool_called !== false || proof.capability_grant !== false) return false;
  try {
    const hash = async (text: string) => Array.from(new Uint8Array(await crypto.subtle.digest("SHA-256", new TextEncoder().encode(text))),
      (value) => value.toString(16).padStart(2, "0")).join("");
    const [keySHA, contentSHA] = await Promise.all([hash(input.operationKey.trim()), hash(input.content)]);
    return proof.operation_key_sha256 === keySHA && proof.request_content_sha256 === contentSHA;
  } catch { return false; }
}
export function validQueuedMessage(value: unknown, workspaceID: string): value is QueuedMessage {
  return object(value) && queueIdentity(value.id) && integer(value.sequence) && value.sequence > 0 &&
    value.status === "pending" && (value.delivery_mode === undefined || value.delivery_mode === "next_turn" || value.delivery_mode === "steer") &&
    typeof value.prepared === "boolean" && typeof value.content === "string" &&
    new TextEncoder().encode(value.content).byteLength <= 65536 && digest(value.content_sha256) &&
    typeof value.content_redacted === "boolean" && integer(value.revision) && timestamp(value.created_at) &&
    (value.edited_at === undefined || timestamp(value.edited_at)) && typeof value.can_edit === "boolean" &&
    typeof value.can_cancel === "boolean" && (!value.prepared || !value.can_edit && !value.can_cancel) &&
    validImageAttachments(value.images, workspaceID) && validFileAttachments(value.attachments, workspaceID) &&
    (Boolean(workspaceID) || value.images.length === 0 && value.attachments.length === 0);
}
export function parseQueuedMessages(value: unknown, binding: QueueBinding): QueuedMessages {
  if (!object(value) || value.version !== "thread_queued_messages.v1" || value.thread_id !== binding.threadID ||
    value.run_id !== binding.runID || value.session_id !== binding.sessionID || value.capability_grant !== false ||
    !((value.current_attempt_id === undefined && value.execution_id === undefined) ||
      queueIdentity(value.current_attempt_id) && queueIdentity(value.execution_id)) ||
    !integer(value.pending) || !integer(value.prepared) || !Array.isArray(value.items) || value.items.length > 64 ||
    !value.items.every((item) => validQueuedMessage(item, binding.workspaceID)) ||
    new Set(value.items.map((item) => item.id)).size !== value.items.length ||
    value.items.some((item, index, all) => index > 0 && item.sequence <= all[index - 1].sequence) ||
    value.prepared !== value.items.filter((item) => item.prepared).length || value.pending + value.prepared !== value.items.length) throw invalid();
  return value as unknown as QueuedMessages;
}
export async function readQueuedMessages(client: APIClient, binding: QueueBinding, signal?: AbortSignal) {
  return parseQueuedMessages(await client.get<unknown>(`/threads/${encodeURIComponent(binding.threadID)}/queued-messages`, {}, signal), binding);
}
export function parseQueueRevision(value: unknown, input: QueueRevisionInput): QueueRevision {
  if (!object(value) || value.version !== "session_steering_revision.v1" || value.run_id !== input.runID ||
    value.session_id !== input.sessionID || value.message_id !== input.messageID || value.execution_started !== false ||
    value.model_called !== false || value.tool_called !== false || value.capability_grant !== false || typeof value.replayed !== "boolean" ||
    !object(value.receipt) || !queueIdentity(value.receipt.id) || value.receipt.from_revision !== input.expectedRevision ||
    value.receipt.to_revision !== input.expectedRevision + 1 || value.receipt.old_content_sha256 !== input.oldSHA256 ||
    !digest(value.receipt.new_content_sha256) || !timestamp(value.receipt.created_at)) throw invalid();
  return value as unknown as QueueRevision;
}
export async function reviseQueuedMessage(client: APIClient, input: QueueRevisionInput) {
  if (!client.hasSessionSteeringControl) throw new Error("当前连接没有修改排队消息的权限。");
  return parseQueueRevision(await client.postControl<unknown>(
    `/sessions/${encodeURIComponent(input.sessionID)}/messages/${encodeURIComponent(input.messageID)}/revise`,
    { version: "session_steering_revision.v1", expected_revision: input.expectedRevision, content: input.content }, input.operationKey), input);
}
function parseObservedMessage(value: unknown, input: Pick<QueueRevisionInput, "messageID" | "sessionID" | "runID">): QueueObservedMessage {
  if (!object(value) || value.id !== input.messageID || value.session_id !== input.sessionID || value.run_id !== input.runID ||
    !integer(value.revision) || !["pending", "committed", "cancelled"].includes(String(value.status))) throw invalid();
  return value as unknown as QueueObservedMessage;
}
export async function inspectQueueRevision(client: APIClient, input: QueueRevisionInput): Promise<QueueObservation> {
  const value = await client.get<unknown>(`/sessions/${encodeURIComponent(input.sessionID)}/messages/${encodeURIComponent(input.messageID)}/revisions/${encodeURIComponent(input.operationKey)}`);
  if (!object(value) || value.version !== "session_steering_revision.v1" || value.session_id !== input.sessionID ||
    value.message_id !== input.messageID || value.capability_grant !== false) throw invalid();
  if (value.state === "absent" && value.revision === undefined) {
    const message = parseObservedMessage(value.message, input);
    if (message.revision < input.expectedRevision) throw invalid();
    return { state: "absent", message };
  }
  if (value.state !== "sealed") throw invalid();
  parseQueueRevision(value.revision, input);
  return { state: "sealed" };
}
export async function inspectQueueCancellation(client: APIClient, input: Pick<QueueRevisionInput, "messageID" | "sessionID" | "runID" | "operationKey">): Promise<QueueObservation> {
  const value = await client.get<unknown>(`/sessions/${encodeURIComponent(input.sessionID)}/messages/${encodeURIComponent(input.messageID)}/cancellations/${encodeURIComponent(input.operationKey)}`);
  if (!object(value) || value.version !== "session_steering_cancellation.v1" || value.session_id !== input.sessionID ||
    value.message_id !== input.messageID || value.capability_grant !== false) throw invalid();
  if (value.state === "absent" && value.receipt === undefined) return { state: "absent", message: parseObservedMessage(value.message, input) };
  if (value.state !== "sealed" || !object(value.receipt) || value.receipt.run_id !== input.runID || value.receipt.session_id !== input.sessionID ||
    value.receipt.message_id !== input.messageID || value.receipt.kind !== "operator" || !queueIdentity(value.receipt.cancellation_id) || !timestamp(value.receipt.created_at)) throw invalid();
  return { state: "sealed" };
}

export function parseQueuePromotion(value: unknown, input: QueuePromotionInput): QueuePromotion {
  if (!queueIdentity(input.expectedAttemptID) || !queueIdentity(input.expectedExecutionID) || !digest(input.oldSHA256) ||
    !object(value) || value.rejected !== undefined && value.rejected !== false || value.version !== "session_steering_promotion.v1" || value.run_id !== input.runID ||
    value.session_id !== input.sessionID || value.message_id !== input.messageID || value.execution_started !== false ||
    value.model_called !== false || value.tool_called !== false || value.capability_grant !== false || typeof value.replayed !== "boolean" ||
    !object(value.receipt) || !queueIdentity(value.receipt.id) || !queueIdentity(value.receipt.replacement_message_id) ||
    value.receipt.replacement_message_id === input.messageID || value.receipt.expected_revision !== input.expectedRevision ||
    value.receipt.content_sha256 !== input.oldSHA256 || value.receipt.target_attempt_id !== input.expectedAttemptID ||
    value.receipt.execution_id !== input.expectedExecutionID || !queueIdentity(value.receipt.cancellation_id) || !timestamp(value.receipt.created_at)) throw invalid();
  return value as unknown as QueuePromotion;
}
export async function promoteQueuedMessage(client: APIClient, input: QueuePromotionInput) {
  if (!client.hasSessionSteeringControl) throw new Error("当前连接没有引导当前任务的权限。");
  if (!queueIdentity(input.expectedAttemptID) || !queueIdentity(input.expectedExecutionID) || !digest(input.oldSHA256) || !integer(input.expectedRevision)) throw invalid();
  const value = await client.postControl<unknown>(
    `/sessions/${encodeURIComponent(input.sessionID)}/messages/${encodeURIComponent(input.messageID)}/promote`,
    { version: "session_steering_promotion.v1", expected_revision: input.expectedRevision,
      expected_content_sha256: input.oldSHA256, expected_attempt_id: input.expectedAttemptID,
      expected_execution_id: input.expectedExecutionID }, input.operationKey);
  return object(value) && value.rejected===true ? parseQueuePromotionRejection(value,input) : parseQueuePromotion(value,input);
}
export async function inspectQueuePromotion(client: APIClient, input: QueuePromotionInput): Promise<QueuePromotionObservation> {
  const value = await client.get<unknown>(`/sessions/${encodeURIComponent(input.sessionID)}/messages/${encodeURIComponent(input.messageID)}/promotions/${encodeURIComponent(input.operationKey)}`);
  if (!object(value) || value.version !== "session_steering_promotion.v1" || value.session_id !== input.sessionID ||
    value.message_id !== input.messageID || value.capability_grant !== false || typeof value.execution_observed !== "boolean" ||
    value.execution_id !== undefined && (!value.execution_observed || !queueIdentity(value.execution_id))) throw invalid();
  if (value.state === "sealed" && value.message === undefined && value.rejection===undefined) return { state: "sealed", promotion: parseQueuePromotion(value.promotion, input) };
  if (value.state === "rejected" && value.message===undefined && value.promotion===undefined) { parseQueuePromotionRejection(value.rejection,input); return {state:"rejected"}; }
  if (value.state !== "absent" || value.promotion !== undefined || value.rejection!==undefined) throw invalid();
  const message = parseObservedMessage(value.message, input);
  if (message.revision < input.expectedRevision) throw invalid();
  return { state: "absent", message, executionObserved: value.execution_observed, executionID: value.execution_id as string | undefined };
}

export function parseQueuePromotionRejection(value: unknown, input: QueuePromotionInput): QueuePromotionRejection {
  if (!queueIdentity(input.expectedAttemptID) || !queueIdentity(input.expectedExecutionID) || !digest(input.oldSHA256) || !object(value) ||
    value.version!=="session_steering_promotion.v1" || value.rejected!==true || value.run_id!==input.runID || value.session_id!==input.sessionID ||
    value.message_id!==input.messageID || typeof value.replayed!=="boolean" || value.execution_started!==false || value.model_called!==false ||
    value.tool_called!==false || value.capability_grant!==false || !object(value.receipt) || !queueIdentity(value.receipt.id) ||
    value.receipt.expected_revision!==input.expectedRevision || value.receipt.content_sha256!==input.oldSHA256 || value.receipt.target_attempt_id!==input.expectedAttemptID ||
    value.receipt.execution_id!==input.expectedExecutionID || !timestamp(value.receipt.created_at) || value.receipt.replacement_message_id!==undefined || value.receipt.cancellation_id!==undefined) throw invalid();
  return value as unknown as QueuePromotionRejection;
}
