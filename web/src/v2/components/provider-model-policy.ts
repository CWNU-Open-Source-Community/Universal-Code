import type { LocaleText } from "../../lib/locale";

export const providerModelLimit = 128;

export type ProviderModelPolicy = {
  window_tokens: number;
  default_output_tokens: number;
  max_output_tokens: number;
};

const fallbackPolicy: ProviderModelPolicy = {
  window_tokens: 32768, default_output_tokens: 1024, max_output_tokens: 4096,
};

function record(value: unknown): value is Record<string, unknown> {
  return typeof value === "object" && value !== null && !Array.isArray(value);
}

export function policyShape(value: unknown): value is ProviderModelPolicy {
  return record(value) && Object.keys(value).length === 3 &&
    ["window_tokens", "default_output_tokens", "max_output_tokens"].every((key) => typeof value[key] === "number");
}

export function providerModelPolicyError(config: Record<string, unknown>, t: LocaleText = (chinese) => chinese): string | undefined {
  const policies = config.model_context_windows;
  if (policies === undefined) return;
  if (!record(policies) || Object.keys(policies).length === 0 || Object.keys(policies).length > providerModelLimit) {
    return t("高级 JSON 中的 model_context_windows 必须包含 1–128 个模型策略；原内容已保留。", "model_context_windows in advanced JSON must contain 1–128 model policies. Original content is preserved.");
  }
  for (const [model, policy] of Object.entries(policies)) {
    if (!policyShape(policy)) return t(`模型“${model}”的输出策略含缺失或未知字段；请在高级 JSON 中修正，原内容已保留。`, `Output policy for "${model}" has missing or unknown fields. Correct advanced JSON; original content is preserved.`);
    if (!Object.values(policy).every(Number.isSafeInteger)) return t(`模型“${model}”的 token 数必须为整数。`, `Token counts for "${model}" must be integers.`);
    if (policy.window_tokens < 4096 || policy.window_tokens > 2097152) return t(`模型“${model}”的上下文窗口须为 4096–2097152 token。`, `The context window for "${model}" must be 4096–2097152 tokens.`);
    if (policy.default_output_tokens < 1 || policy.max_output_tokens < policy.default_output_tokens) return t(`模型“${model}”的默认输出须至少为 1，且不能大于单次输出上限。`, `Default output for "${model}" must be at least 1 and no greater than the per-request output limit.`);
    if (policy.max_output_tokens > 1000000 || policy.max_output_tokens >= policy.window_tokens - 1024) {
      return t(`模型“${model}”的单次输出上限须不超过 1000000，并小于上下文窗口减去 1024 token 的安全余量。`, `The output limit for "${model}" must be at most 1000000 and below the context window minus a 1024-token safety margin.`);
    }
  }
}

export function providerModelGroups(models: string[], config: Record<string, unknown>): { wireModel: string; aliases: string[] }[] {
  const mapping = record(config.model_mapping) ? config.model_mapping : {};
  const groups = new Map<string, string[]>();
  for (const model of models) {
    const mapped = mapping[model];
    const wireModel = typeof mapped === "string" && mapped.trim() ? mapped : model;
    groups.set(wireModel, [...(groups.get(wireModel) ?? []), model]);
  }
  return [...groups].map(([wireModel, aliases]) => ({ wireModel, aliases }));
}

export function providerPolicyControlsError(config: Record<string, unknown>, t: LocaleText = (chinese) => chinese): string | undefined {
  if (config.model_mapping !== undefined && (!record(config.model_mapping) ||
    Object.values(config.model_mapping).some((value) => typeof value !== "string" || value.trim() === "" || value.trim() !== value))) {
    return t("先修正高级 JSON 中的 model_mapping，再设置实际模型的输出策略。", "Fix model_mapping in advanced JSON before configuring model output policies.");
  }
  if (config.model_context_windows !== undefined && !record(config.model_context_windows)) {
    return t("先修正高级 JSON 中的 model_context_windows；原内容已保留。", "Fix model_context_windows in advanced JSON first. Original content is preserved.");
  }
}

export function withProviderModelPolicy(config: Record<string, unknown>, wireModel: string, policy: ProviderModelPolicy | null): Record<string, unknown> {
  const existing = record(config.model_context_windows) ? config.model_context_windows : {};
  const policies = policy ? { ...existing, [wireModel]: policy } : { ...existing };
  if (!policy) delete policies[wireModel];
  const next = { ...config };
  if (Object.keys(policies).length) next.model_context_windows = policies;
  else delete next.model_context_windows;
  return next;
}

/** Mirrors the runtime's endpoint-bound defaults; these are local budgets, not a live capability probe. */
export function inheritedProviderModelPolicy(endpointURL: string, transport: string, wireModel: string, config: Record<string, unknown>): { policy: ProviderModelPolicy; source: "known" | "fallback"; explicitDefault?: boolean } {
  const fallback = { policy: { ...fallbackPolicy }, source: "fallback" as const };
  let endpoint: URL;
  try { endpoint = new URL(endpointURL); } catch { return fallback; }
  if (endpoint.protocol !== "https:" || endpoint.port !== "" && endpoint.port !== "443" || endpoint.username || endpoint.password || endpoint.search || endpoint.hash) return fallback;
  // Go's runtime preserves dot segments instead of applying the browser URL
  // parser's path normalization. Compare the same decoded original path.
  let path: string;
  try { path = decodeURIComponent(endpointURL.trim().replace(/^[a-z]+:\/\/[^/]*/iu, "")).replace(/\/+$/u, ""); }
  catch { return fallback; }
  if (endpoint.hostname === "api.openai.com" &&
    ((transport === "openai_responses" && ["", "/v1", "/v1/responses"].includes(path)) ||
      (transport === "openai_chat_completions" && ["", "/v1", "/v1/chat/completions"].includes(path))) &&
    ["gpt-6-astra", "gpt-6.1-sol", "gpt-6-luna"].includes(wireModel)) {
    return { policy: { window_tokens: 1050000, default_output_tokens: 16384, max_output_tokens: 128000 }, source: "known", explicitDefault: true };
  }
  if (endpoint.hostname !== "api.deepseek.com" ||
    !["deepseek-flash", "deepseek-v4-flash", "deepseek-v4-flash-vision-exp", "deepseek-v4-pro"].includes(wireModel)) return fallback;
  if (transport === "anthropic_messages") {
    if (!["/anthropic", "/anthropic/v1", "/anthropic/v1/messages"].includes(path)) return fallback;
  } else if (transport !== "openai_chat_completions" || !["", "/v1", "/chat/completions", "/v1/chat/completions"].includes(path)) return fallback;
  const body = record(config.request_body) ? config.request_body : {};
  const thinking = record(body.thinking) ? body.thinking.type : undefined;
  const effort = body.reasoning_effort;
  if (body.thinking !== undefined && thinking !== "enabled" && thinking !== "disabled" ||
    effort !== undefined && !["none", "low", "minimal", "medium", "high", "xhigh", "max"].includes(String(effort))) return fallback;
  const disabled = effort !== undefined ? effort === "none" : thinking === "disabled";
  return { policy: { window_tokens: 1000000, default_output_tokens: disabled ? 8192 : effort === "max" ? 131072 : 65536, max_output_tokens: 393216 }, source: "known" };
}
