import { APIRequestError, isErrorEnvelope, isSuccessEnvelope } from "./protocol";
import { parseThreadTranscriptItems } from "./responses/transcripts";
import type { PageResult, SuccessEnvelope } from "../types";
import type { ClientFeatures } from "./capabilities";
import type { QueryValue } from "./protocol";

// APIClient validates the read bearer and same-origin base before constructing this transport.
export class APITransport {
  constructor(
    private readonly token: string,
    readonly baseURL: string,
    private readonly controlToken: string,
    private readonly features: ClientFeatures,
  ) {}

  async get<T>(path: string, query: Record<string, QueryValue> = {}, signal?: AbortSignal): Promise<T> {
    const envelope = await this.request<T>(path, query, signal);
    return envelope.data;
  }

  async getPage<T>(
    path: string,
    query: Record<string, QueryValue> = {},
    cursor = "",
    signal?: AbortSignal,
  ): Promise<PageResult<T>> {
    const envelope = await this.request<T[]>(path, { ...query, cursor: cursor || undefined }, signal);
    if (!envelope.page || !Array.isArray(envelope.data)) {
      throw new APIRequestError("API collection response omitted pagination metadata", "INVALID_RESPONSE", 502,
        envelope.request_id);
    }
    const items = /^\/threads\/[^/]+\/transcript$/u.test(path)
      ? parseThreadTranscriptItems(envelope.data as unknown[]) as T[]
      : envelope.data;
    return { items, page: envelope.page, requestID: envelope.request_id };
  }

  async postControl<T>(
    path: string,
    body: unknown,
    idempotencyKey: string,
    signal?: AbortSignal,
  ): Promise<T> {
    if (!this.features.hasControl) {
      throw new Error("A control bearer token is required for this operation");
    }
    return this.sendControl<T>(path, body, idempotencyKey, signal);
  }

  async patchControl<T>(path: string, body: unknown, signal?: AbortSignal): Promise<T> {
    if (!this.features.hasControl) {
      throw new Error("A control bearer token is required for this operation");
    }
    return this.sendControlRequest<T>(path, body, signal, "", "PATCH");
  }

  async deleteControl<T>(path: string, body: unknown, signal?: AbortSignal): Promise<T> {
    if (!this.features.hasControl) {
      throw new Error("A control bearer token is required for this operation");
    }
    return this.sendControlRequest<T>(path, body, signal, "", "DELETE");
  }

  async sendControl<T>(path: string, body: unknown, idempotencyKey: string,
    signal?: AbortSignal): Promise<T> {
    if (idempotencyKey.trim() !== idempotencyKey || idempotencyKey.length < 16) {
      throw new Error("A normalized idempotency key is required");
    }
    return this.sendControlRequest<T>(path, body, signal, idempotencyKey);
  }

  async sendReadRequest<T>(path: string, body: unknown,
    signal?: AbortSignal): Promise<T> {
    const response = await fetch(this.url(path), {
      method: "POST",
      headers: { ...this.headers(), "Content-Type": "application/json" },
      body: JSON.stringify(body),
      signal,
      cache: "no-store",
      credentials: "omit",
      referrerPolicy: "no-referrer",
    });
    const payload = await this.readJSON(response);
    if (!response.ok) {
      if (isErrorEnvelope(payload)) {
        throw new APIRequestError(payload.error.message, payload.error.code, response.status,
          payload.request_id);
      }
      throw new APIRequestError("Universal Code read request failed", "INVALID_RESPONSE", response.status,
        response.headers.get("x-request-id") || "");
    }
    if (!isSuccessEnvelope<T>(payload)) {
      throw new APIRequestError("Universal Code API returned an invalid read envelope", "INVALID_RESPONSE",
        response.status, response.headers.get("x-request-id") || "");
    }
    return payload.data;
  }

  async sendControlRequest<T>(path: string, body: unknown, signal?: AbortSignal,
    idempotencyKey = "", method: "GET" | "POST" | "PUT" | "PATCH" | "DELETE" = "POST"): Promise<T> {
    const headers: Record<string, string> = {
      Accept: "application/json",
      Authorization: `Bearer ${this.controlToken}`,
      "Content-Type": "application/json",
    };
    if (idempotencyKey) {
      headers["Idempotency-Key"] = idempotencyKey;
    }
    const response = await fetch(this.url(path), {
      method,
      headers,
      body: method === "GET" ? undefined : JSON.stringify(body),
      signal,
      cache: "no-store",
      credentials: "omit",
      referrerPolicy: "no-referrer",
    });
    const payload = await this.readJSON(response);
    if (!response.ok) {
      if (isErrorEnvelope(payload)) {
        throw new APIRequestError(payload.error.message, payload.error.code, response.status, payload.request_id,
          payload.error.message_queued === false ? false : undefined,
          payload.error.operation_key_invalidated === true ? true : undefined,
          payload.error.turn_failed === true ? true : undefined, payload.error.turn_failure,
          payload.error.revision_unchanged);
      }
      throw new APIRequestError("Universal Code control request failed", "INVALID_RESPONSE", response.status,
        response.headers.get("x-request-id") || "");
    }
    if (!isSuccessEnvelope<T>(payload)) {
      throw new APIRequestError("Universal Code API returned an invalid control envelope", "INVALID_RESPONSE",
        response.status, response.headers.get("x-request-id") || "");
    }
    return payload.data;
  }

  async request<T>(
    path: string,
    query: Record<string, QueryValue>,
    signal?: AbortSignal,
    observationKey?: string,
  ): Promise<SuccessEnvelope<T>> {
    if (observationKey !== undefined && (observationKey.length < 16 || observationKey.length > 256 ||
      /[\s\u0000-\u001f\u007f]/u.test(observationKey))) {
      throw new Error("A normalized original operation key is required");
    }
    const response = await fetch(this.url(path, query), {
      method: "GET",
      headers: { ...this.headers(), ...(observationKey === undefined ? {} : { "Idempotency-Key": observationKey }) },
      signal,
      cache: "no-store",
      credentials: "omit",
      referrerPolicy: "no-referrer",
    });
    const payload = await this.readJSON(response);
    if (!response.ok) {
      if (isErrorEnvelope(payload)) {
        throw new APIRequestError(payload.error.message, payload.error.code, response.status, payload.request_id);
      }
      throw new APIRequestError("Universal Code API request failed", "INVALID_RESPONSE", response.status,
        response.headers.get("x-request-id") || "");
    }
    if (!isSuccessEnvelope<T>(payload)) {
      throw new APIRequestError("Universal Code API returned an invalid envelope", "INVALID_RESPONSE", response.status,
        response.headers.get("x-request-id") || "");
    }
    return payload;
  }

  async responseError(response: Response): Promise<APIRequestError> {
    const payload = await this.readJSON(response);
    if (isErrorEnvelope(payload)) {
      return new APIRequestError(payload.error.message, payload.error.code, response.status, payload.request_id);
    }
    return new APIRequestError("Universal Code API request failed", "INVALID_RESPONSE", response.status,
      response.headers.get("x-request-id") || "");
  }

  private async readJSON(response: Response): Promise<unknown> {
    try {
      return await response.json() as unknown;
    } catch {
      return null;
    }
  }

  headers(): Record<string, string> {
    return {
      Accept: "application/json",
      Authorization: `Bearer ${this.token}`,
    };
  }

  url(path: string, query: Record<string, QueryValue> = {}): string {
    if (!path.startsWith("/") || path.startsWith("//")) {
      throw new Error("API path must be relative to the configured base path");
    }
    const url = new URL(`${this.baseURL}${path}`, window.location.origin);
    if (!url.pathname.startsWith(`${this.baseURL}/`)) {
      throw new Error("API path escaped the configured base path");
    }
    for (const [key, value] of Object.entries(query)) {
      if (value !== undefined && value !== "") {
        url.searchParams.set(key, String(value));
      }
    }
    return `${url.pathname}${url.search}`;
  }
}
