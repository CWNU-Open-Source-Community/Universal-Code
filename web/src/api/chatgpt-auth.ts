export const chatGPTAuthProtocol = "chatgpt_auth.v1";
export const chatGPTUsageURL = "https://chatgpt.com/settings/usage";

export interface ChatGPTAccountView {
  id: string;
  label: string;
  email: string;
  provider: string;
  state: "connected" | "signed_out" | "verification_pending" | "reauthorization_required";
  models: Array<{ id: string; name: string }>;
}

export interface ChatGPTLoginView {
  id: string;
  state: "waiting" | "succeeded" | "failed" | "cancelled";
  start_url?: string;
  message?: string;
  account_id?: string;
}

export interface ChatGPTAuthStatusView {
  protocol_version: typeof chatGPTAuthProtocol;
  available: boolean;
  store_kind: string;
  accounts: ChatGPTAccountView[];
  login?: ChatGPTLoginView;
  usage_url: typeof chatGPTUsageURL;
}

export interface ChatGPTAuthRequestView {
  version: typeof chatGPTAuthProtocol;
  confirm: true;
}

export interface ChatGPTAccountRequestView extends ChatGPTAuthRequestView {
  account_id: string;
}

export interface ChatGPTLoginRequestView extends ChatGPTAuthRequestView {
  account_id?: string;
}

export interface ChatGPTCancelRequestView extends ChatGPTAuthRequestView {
  attempt_id: string;
}

const record = (value: unknown): value is Record<string, unknown> =>
  value !== null && typeof value === "object" && !Array.isArray(value);
const text = (value: unknown, max: number, empty = false): value is string =>
  typeof value === "string" && (empty || value.length > 0) && value.length <= max &&
  value.trim() === value && !/[\u0000-\u001f\u007f]/u.test(value);
export const validChatGPTIdentity = (value: unknown): value is string =>
  text(value, 256) && !/\s/u.test(value);
const keys = (value: Record<string, unknown>, required: string[], optional: string[] = []) =>
  required.every((key) => Object.hasOwn(value, key)) &&
  Object.keys(value).every((key) => required.includes(key) || optional.includes(key));

/** The renderer receives only a single-use local launcher ticket, never the OAuth URL. */
export function validChatGPTStartURL(value: unknown): value is string {
  if (typeof value !== "string") return false;
  const match = /^http:\/\/127\.0\.0\.1:([1-9]\d{0,4})\/auth\/start\/[A-Za-z0-9_-]{43}$/u.exec(value);
  return Boolean(match && Number(match[1]) <= 65_535);
}

export function parseChatGPTAuthStatus(value: unknown): ChatGPTAuthStatusView {
  const invalid = () => new Error("ChatGPT 账户状态无效，请重新读取。");
  if (!record(value) || !keys(value, ["protocol_version", "available", "store_kind", "accounts", "usage_url"], ["login"]) ||
    value.protocol_version !== chatGPTAuthProtocol || typeof value.available !== "boolean" ||
    !text(value.store_kind, 128) || value.usage_url !== chatGPTUsageURL ||
    !Array.isArray(value.accounts) || value.accounts.length > 64) throw invalid();
  const accounts = value.accounts.map((account: unknown) => {
    if (!record(account) || !keys(account, ["id", "label", "email", "provider", "state", "models"]) ||
      !validChatGPTIdentity(account.id) || !text(account.label, 256, true) || !text(account.email, 256, true) ||
      !validChatGPTIdentity(account.provider) ||
      (typeof account.state !== "string" || !["connected", "signed_out", "verification_pending", "reauthorization_required"].includes(account.state)) ||
      !Array.isArray(account.models) || account.models.length > 512) throw invalid();
    const models = account.models.map((model: unknown) => {
      if (!record(model) || !keys(model, ["id", "name"]) ||
        !validChatGPTIdentity(model.id) || !text(model.name, 256, true)) throw invalid();
      return { id: model.id, name: model.name };
    });
    if (new Set(models.map((model) => model.id)).size !== models.length) throw invalid();
    return { id: account.id, label: account.label, email: account.email, provider: account.provider,
      state: account.state, models } as ChatGPTAccountView;
  });
  if (new Set(accounts.map((account) => account.id)).size !== accounts.length ||
    new Set(accounts.map((account) => account.provider)).size !== accounts.length) throw invalid();
  let login: ChatGPTLoginView | undefined;
  if (value.login !== undefined) {
    const attempt = value.login;
    if (!record(attempt) || !keys(attempt, ["id", "state"], ["start_url", "message", "account_id"]) ||
      !validChatGPTIdentity(attempt.id) || typeof attempt.state !== "string" || !["waiting", "succeeded", "failed", "cancelled"].includes(attempt.state) ||
      (attempt.message !== undefined && !text(attempt.message, 2048, true)) ||
      (attempt.account_id !== undefined && !validChatGPTIdentity(attempt.account_id)) ||
      (attempt.start_url !== undefined && (attempt.state !== "waiting" || !validChatGPTStartURL(attempt.start_url)))) throw invalid();
    login = { id: attempt.id, state: attempt.state as ChatGPTLoginView["state"],
      ...(attempt.start_url !== undefined ? { start_url: attempt.start_url } : {}),
      ...(attempt.message !== undefined ? { message: attempt.message } : {}),
      ...(attempt.account_id !== undefined ? { account_id: attempt.account_id } : {}) };
  }
  return { protocol_version: chatGPTAuthProtocol, available: value.available, store_kind: value.store_kind,
    accounts, ...(login ? { login } : {}), usage_url: chatGPTUsageURL };
}
