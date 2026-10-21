import {
  boundedIdentity,
  boundedText,
  hasExactKeys,
  hasOnlyKeys,
  isRecord,
  safePositiveInteger,
} from "./validation";
import type { ErrorEnvelope, Page, SuccessEnvelope, ThreadTurnFailureReferenceView } from "../types";

export type QueryValue = boolean | number | string | undefined;

export class APIRequestError extends Error {
  constructor(
    message: string,
    readonly code: string,
    readonly status: number,
    readonly requestID = "",
    readonly messageQueued?: false,
    readonly operationKeyInvalidated?: true,
    readonly turnFailed?: true,
    readonly turnFailure?: ThreadTurnFailureReferenceView,
    readonly revisionUnchanged?: unknown,
  ) {
    super(message);
    this.name = "APIRequestError";
  }
}

export function normalizeBaseURL(baseURL: string): string {
  const resolved = new URL(baseURL, window.location.origin);
  if (resolved.origin !== window.location.origin) {
    throw new Error("Universal-Code API must use the current browser origin");
  }
  const path = resolved.pathname.replace(/\/+$/, "");
  if (path !== "/api/v1") {
    throw new Error("Universal-Code API base path must be /api/v1");
  }
  return path;
}

export function isErrorEnvelope(value: unknown): value is ErrorEnvelope {
  if (typeof value !== "object" || value === null) {
    return false;
  }
  const candidate = value as Partial<ErrorEnvelope>;
  return candidate.version === "api.v1" && typeof candidate.request_id === "string" &&
    typeof candidate.error?.code === "string" && typeof candidate.error.message === "string" &&
    (!Object.prototype.hasOwnProperty.call(candidate.error, "message_queued") || candidate.error.message_queued === false) &&
    (!Object.prototype.hasOwnProperty.call(candidate.error, "operation_key_invalidated") || candidate.error.operation_key_invalidated === true) &&
    (!Object.prototype.hasOwnProperty.call(candidate.error, "turn_failed") || candidate.error.turn_failed === true) &&
    !(candidate.error.turn_failed === true && candidate.error.message_queued === false) &&
    (!Object.prototype.hasOwnProperty.call(candidate.error, "turn_failure") ||
      candidate.error.turn_failed === true && validThreadFailureReference(candidate.error.turn_failure));
}

export function validThreadFailureReference(value: unknown): value is ThreadTurnFailureReferenceView {
  return hasExactKeys(value, ["thread_id", "run_id", "message_id", "event_sequence"]) &&
    [value.thread_id, value.run_id, value.message_id].every((id) =>
      typeof id === "string" && Boolean(boundedIdentity(id)) && boundedIdentity(id) === id) &&
    typeof value.event_sequence === "number" && Number.isSafeInteger(value.event_sequence) && value.event_sequence > 0;
}

export function isSuccessEnvelope<T>(value: unknown): value is SuccessEnvelope<T> {
  if (typeof value !== "object" || value === null) {
    return false;
  }
  const candidate = value as Partial<SuccessEnvelope<T>>;
  return candidate.version === "api.v1" && typeof candidate.request_id === "string" &&
    Object.prototype.hasOwnProperty.call(candidate, "data");
}

export function parseResponsePage(value: unknown, expectedLimit: number): Page {
  if (!isRecord(value) || !hasOnlyKeys(value, ["limit", "next_cursor", "truncated"]) ||
    !safePositiveInteger(value.limit) || value.limit > 100 || value.limit !== expectedLimit ||
    (value.next_cursor !== undefined && (!boundedText(value.next_cursor, 512) ||
      value.next_cursor.trim() !== value.next_cursor)) ||
    (value.truncated !== undefined && typeof value.truncated !== "boolean") ||
    (Boolean(value.next_cursor) && value.truncated === true)) {
    throw new APIRequestError("API pagination metadata is invalid", "INVALID_RESPONSE", 502);
  }
  return value as unknown as Page;
}
