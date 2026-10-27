import { useLocale, type LocaleText } from "../../lib/locale";
import { useEffect, useMemo, useRef, useState } from "react";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import {
  Activity, ArrowLeft, Check, ChevronRight, CircleAlert, KeyRound, LoaderCircle,
  Plus, Save, Server, ShieldCheck, Trash2,
} from "lucide-react";
import type { APIClient } from "../../api/client";
import type {
  ModelHarnessQualificationView,
  ProviderDiagnosticView,
  ProviderDefinitionCollectionView,
  ProviderDefinitionMutationView,
  ProviderDefinitionView,
  DiscoveredProviderModelView,
} from "../../api/types";
import { V2ConfirmDialog } from "./dialog";
import {
  inheritedProviderModelPolicy, policyShape, providerModelGroups, providerModelLimit,
  providerModelPolicyError, providerPolicyControlsError, withProviderModelPolicy,
  type ProviderModelPolicy,
} from "./provider-model-policy";

const definitionQueryKey = ["v2", "provider-definitions"] as const;
const credentialQueryKey = ["v2", "provider-credentials"] as const;
const reservedProviderIDs = new Set([
  "anthropic", "deepseek", "mimo", "mock", "ollama", "openai",
]);

type ProviderDraft = {
  existing: ProviderDefinitionView | null;
  id: string;
  displayName: string;
  note: string;
  websiteURL: string;
  endpointURL: string;
  transport: "openai_chat_completions" | "openai_responses" | "anthropic_messages";
  models: string;
  defaultModel: string;
  enabled: boolean;
  searchMode: "disabled" | "auto" | "web" | "searxng" | "provider_native";
  nativeSearchDeclared: boolean;
  advancedJSON: string;
  apiKey: string;
  syncCredentialReference: boolean;
};

export type V2ProviderDraftPreset = {
  id: string;
  displayName: string;
  note: string;
  websiteURL: string;
  endpointURL: string;
  transport: ProviderDraft["transport"];
  models: readonly string[];
  defaultModel: string;
  searchMode: ProviderDraft["searchMode"];
  nativeSearchDeclared: boolean;
  advancedConfig: Record<string, unknown>;
};

type V2ProviderSettingsProps = {
  client: APIClient;
  initialPreset?: V2ProviderDraftPreset;
  onExit?: () => void;
  onSaved?: (definition: ProviderDefinitionView) => void;
  prepareForDraft?: boolean;
  onReady?: (definition: Pick<ProviderDefinitionView, "id" | "default_model">) => void;
};

type SecretMigration = {
  definition: ProviderDefinitionView;
  sanitizedJSON: string;
};

type SecretScan = {
  secret: string;
  sanitized: unknown;
  count: number;
};

function blankDraft(): ProviderDraft {
  return {
    existing: null,
    id: "",
    displayName: "",
    note: "",
    websiteURL: "",
    endpointURL: "",
    transport: "openai_chat_completions",
    models: "",
    defaultModel: "",
    enabled: true,
    searchMode: "auto",
    nativeSearchDeclared: false,
    advancedJSON: "{}",
    apiKey: "",
    syncCredentialReference: true,
  };
}

function draftFromDefinition(definition: ProviderDefinitionView): ProviderDraft {
  return {
    existing: definition,
    id: definition.id,
    displayName: definition.display_name,
    note: definition.note,
    websiteURL: definition.website_url,
    endpointURL: definition.endpoint_url,
    transport: definition.transport as ProviderDraft["transport"],
    models: definition.models.join("\n"),
    defaultModel: definition.default_model,
    enabled: definition.enabled,
    searchMode: definition.search_mode as ProviderDraft["searchMode"],
    nativeSearchDeclared: definition.native_web_search_capability === "declared_unverified",
    advancedJSON: JSON.stringify(definition.advanced_config, null, 2),
    apiKey: "",
    syncCredentialReference: true,
  };
}

function draftFromPreset(preset: V2ProviderDraftPreset): ProviderDraft {
  const nativeUnsupported = knownNativeSearchUnsupported(preset.endpointURL);
  return {
    existing: null,
    id: preset.id,
    displayName: preset.displayName,
    note: preset.note,
    websiteURL: preset.websiteURL,
    endpointURL: preset.endpointURL,
    transport: preset.transport,
    models: preset.models.join("\n"),
    defaultModel: preset.defaultModel,
    enabled: true,
    searchMode: nativeUnsupported && preset.searchMode === "provider_native" ? "auto" : preset.searchMode,
    nativeSearchDeclared: !nativeUnsupported && preset.nativeSearchDeclared,
    advancedJSON: JSON.stringify(preset.advancedConfig, null, 2),
    apiKey: "",
    syncCredentialReference: true,
  };
}

function normalizeModels(value: string): string[] {
  return [...new Set(value.split(/[\n,]/u).map((model) => model.trim()).filter(Boolean))];
}

function knownNativeSearchUnsupported(endpoint: string): boolean {
  try {
    return new URL(endpoint).hostname.toLowerCase().replace(/\.$/u, "") === "api.deepseek.com";
  } catch {
    return false;
  }
}

function knownVisionDeclaration(value: unknown): value is { vision: string } {
  return isRecord(value) && Object.keys(value).length === 1 &&
    typeof value.vision === "string" && ["supported", "unsupported", "unknown"].includes(value.vision);
}

function draftWithModels(t: LocaleText, draft: ProviderDraft, models: string): ProviderDraft {
  const next = { ...draft, models };
  const advanced = parseAdvancedJSON(t, draft.advancedJSON).value;
  if (!advanced || !isRecord(advanced.model_capabilities)) return next;
  const selected = new Set(normalizeModels(models));
  const capabilities = { ...advanced.model_capabilities };
  let changed = false;
  for (const model of normalizeModels(draft.models)) {
    // Removing a model also removes its exact, understood declaration. A new
    // model never inherits vision support from a renamed identifier. Handwritten
    // unknown values stay intact for the operator to inspect in advanced JSON.
    if (!selected.has(model) && knownVisionDeclaration(capabilities[model])) {
      delete capabilities[model];
      changed = true;
    }
  }
  return changed ? { ...next, advancedJSON: JSON.stringify({ ...advanced, model_capabilities: capabilities }, null, 2) } : next;
}

function modelCapabilitiesError(t: LocaleText, config: Record<string, unknown>, models: string[]): string | undefined {
  if (config.model_capabilities === undefined) return undefined;
  if (!isRecord(config.model_capabilities)) return t("高级 JSON 中的 model_capabilities 必须是对象；原内容已保留，请手动修正。", "model_capabilities in advanced JSON must be an object. Your content is preserved; correct it manually.");
  for (const [model, value] of Object.entries(config.model_capabilities)) {
    if (!models.includes(model)) return t(`图片能力仍引用模型列表以外的“${model}”；原内容已保留，请在高级 JSON 中核对修正。`, `Image capabilities reference "${model}" outside the model list. Content is preserved; correct it in advanced JSON.`);
    if (!knownVisionDeclaration(value)) return t(`模型“${model}”的图片能力配置含无效或未知字段；原内容已保留，请在高级 JSON 中核对修正。`, `Image capabilities for "${model}" contain invalid or unknown fields. Content is preserved; correct it in advanced JSON.`);
  }
  return undefined;
}

function validHTTPSURL(value: string, optional = false): boolean {
  if (optional && value === "") return true;
  try {
    const parsed = new URL(value);
    return parsed.protocol === "https:" && parsed.username === "" && parsed.password === "";
  } catch {
    return false;
  }
}

export function validProviderEndpointURL(value: string): boolean {
  if (new TextEncoder().encode(value).length > 2_048 || /[\\\u0000-\u0020\u007f?#]/u.test(value)) {
    return false;
  }
  try {
    const parsed = new URL(value);
    if (!/^https?:\/\//iu.test(value) || parsed.username || parsed.password) return false;
    // URL normalizes shorthand, integer and escaped IPv4 hosts. Inspect the
    // original authority as well, matching Go's literal net.ParseIP boundary.
    const authority = value.slice(value.indexOf("://") + 3).split("/", 1)[0];
    if (authority.includes("@") || authority.endsWith(":") || /[^\x21-\x7e]|%/u.test(authority)) return false;
    if (parsed.protocol === "https:") return true;
    return parsed.protocol === "http:" && literalProviderLoopbackHost(authority, parsed);
  } catch {
    return false;
  }
}

function literalProviderLoopbackHost(authority: string, parsed: URL): boolean {
  const host = authority.startsWith("[")
    ? authority.slice(0, authority.indexOf("]") + 1) : authority.split(":", 1)[0];
  if (host.toLowerCase() === "localhost") return true;
  if (/^127(?:\.(?:0|[1-9]\d{0,2})){3}$/u.test(host)) return parsed.hostname === host;
  return host.startsWith("[") && (parsed.hostname === "[::1]" ||
    /^\[::ffff:7f[0-9a-f]{2}:[0-9a-f]{1,4}\]$/u.test(parsed.hostname));
}

function isRecord(value: unknown): value is Record<string, unknown> {
  return typeof value === "object" && value !== null && !Array.isArray(value);
}

function rejectDuplicateJSONKeys(t: LocaleText, source: string): void {
  let cursor = 0;
  const whitespace = () => {
    while (cursor < source.length && /\s/u.test(source[cursor])) cursor += 1;
  };
  const stringToken = (): string => {
    const start = cursor;
    if (source[cursor] !== '"') throw new Error("expected string");
    cursor += 1;
    while (cursor < source.length) {
      if (source[cursor] === "\\") {
        cursor += 2;
        continue;
      }
      if (source[cursor] === '"') {
        cursor += 1;
        return JSON.parse(source.slice(start, cursor)) as string;
      }
      cursor += 1;
    }
    throw new Error("unterminated string");
  };
  const value = (): void => {
    whitespace();
    if (source[cursor] === "{") {
      cursor += 1;
      whitespace();
      const keys = new Set<string>();
      if (source[cursor] === "}") {
        cursor += 1;
        return;
      }
      while (cursor < source.length) {
        whitespace();
        const key = stringToken();
        if (keys.has(key)) throw new Error(t(`高级 JSON 包含重复字段“${key}”。`, `Advanced JSON contains the duplicate field "${key}".`));
        keys.add(key);
        whitespace();
        if (source[cursor] !== ":") throw new Error("expected colon");
        cursor += 1;
        value();
        whitespace();
        if (source[cursor] === "}") {
          cursor += 1;
          return;
        }
        if (source[cursor] !== ",") throw new Error("expected comma");
        cursor += 1;
      }
      throw new Error("unterminated object");
    }
    if (source[cursor] === "[") {
      cursor += 1;
      whitespace();
      if (source[cursor] === "]") {
        cursor += 1;
        return;
      }
      while (cursor < source.length) {
        value();
        whitespace();
        if (source[cursor] === "]") {
          cursor += 1;
          return;
        }
        if (source[cursor] !== ",") throw new Error("expected comma");
        cursor += 1;
      }
      throw new Error("unterminated array");
    }
    if (source[cursor] === '"') {
      stringToken();
      return;
    }
    const primitive = /(?:true|false|null|-?(?:0|[1-9]\d*)(?:\.\d+)?(?:[eE][+-]?\d+)?)/uy;
    primitive.lastIndex = cursor;
    const match = primitive.exec(source);
    if (!match) throw new Error("invalid value");
    cursor = primitive.lastIndex;
  };
  value();
  whitespace();
  if (cursor !== source.length) throw new Error("trailing data");
}

function parseAdvancedJSON(t: LocaleText, source: string): { value?: Record<string, unknown>; error?: string } {
  let parsed: unknown;
  try {
    parsed = JSON.parse(source);
    rejectDuplicateJSONKeys(t, source);
  } catch (reason) {
    const message = reason instanceof Error && reason.message.startsWith(t("高级 JSON", "Advanced JSON"))
      ? reason.message : t("高级 JSON 不是有效的 JSON。", "Advanced JSON is not valid JSON.");
    return { error: message };
  }
  if (!isRecord(parsed)) return { error: t("高级 JSON 的顶层必须是对象。", "Advanced JSON must have an object at its top level.") };
  return { value: parsed };
}

function sensitiveKey(key: string): boolean {
  if (key === "$credential") return false;
  return /(?:api[-_]?key|authorization|auth[-_]?token|access[-_]?token|password|secret|token|credential)/iu
    .test(key);
}

function secretValue(value: string): { secret: string; template?: string } | null {
  const trimmed = value.trim();
  if (trimmed.length < 8 || trimmed.includes("${") || trimmed.startsWith("$credential")) {
    return null;
  }
  const bearer = /^Bearer\s+(.+)$/iu.exec(trimmed);
  if (bearer?.[1] && bearer[1].length >= 8) {
    return { secret: bearer[1], template: "Bearer ${secret}" };
  }
  return { secret: trimmed };
}

function scanPlaintextSecrets(value: unknown, providerID: string): SecretScan | null {
  const candidates: Array<{ secret: string; template?: string }> = [];
  const collect = (current: unknown): void => {
    if (Array.isArray(current)) {
      current.forEach(collect);
      return;
    }
    if (!isRecord(current)) return;
    for (const [key, child] of Object.entries(current)) {
      if (sensitiveKey(key) && typeof child === "string") {
        const candidate = secretValue(child);
        if (candidate) candidates.push(candidate);
      } else {
        collect(child);
      }
    }
  };
  collect(value);
  if (candidates.length === 0) return null;
  const secrets = new Set(candidates.map((candidate) => candidate.secret));
  if (secrets.size !== 1) return { secret: "", sanitized: value, count: secrets.size };
  const secret = candidates[0].secret;
  const replace = (current: unknown): unknown => {
    if (Array.isArray(current)) return current.map(replace);
    if (!isRecord(current)) return current;
    return Object.fromEntries(Object.entries(current).map(([key, child]) => {
      if (!sensitiveKey(key) || typeof child !== "string") return [key, replace(child)];
      const candidate = secretValue(child);
      if (!candidate || candidate.secret !== secret) return [key, child];
      return [key, candidate.template
        ? { $credential: providerID, template: candidate.template }
        : { $credential: providerID }];
    }));
  };
  return { secret, sanitized: replace(value), count: 1 };
}

function hasOwnedCredentialReference(value: unknown, providerID?: string): boolean {
  if (Array.isArray(value)) return value.some((child) => hasOwnedCredentialReference(child, providerID));
  if (!isRecord(value)) return false;
  if (Object.hasOwn(value, "$credential") && (providerID === undefined || value.$credential === providerID)) return true;
  return Object.values(value).some((child) => hasOwnedCredentialReference(child, providerID));
}

function permitsKeylessLocalProvider(definition: ProviderDefinitionView | undefined): boolean {
  if (!definition || !validProviderEndpointURL(definition.endpoint_url)) return false;
  const authority = definition.endpoint_url.slice(definition.endpoint_url.indexOf("://") + 3).split("/", 1)[0];
  if (!literalProviderLoopbackHost(authority, new URL(definition.endpoint_url))) return false;
  const config = definition.advanced_config;
  return isRecord(config) && !hasOwnedCredentialReference(config.request_headers) &&
    !hasOwnedCredentialReference(config.request_body);
}

function withSyncedCredentialReference(t: LocaleText, value: unknown, draft: ProviderDraft): {
  value?: Record<string, unknown>;
  error?: string;
} {
  if (!isRecord(value)) return { error: t("高级 JSON 的顶层必须是对象。", "Advanced JSON must have an object at its top level.") };
  if (hasOwnedCredentialReference(value, draft.id.trim())) return { value };
  const currentHeaders = value.request_headers;
  if (currentHeaders !== undefined && !isRecord(currentHeaders)) {
    return { error: t("要同步凭据引用，request_headers 必须是 JSON 对象。", "request_headers must be a JSON object to sync credential references.") };
  }
  const headerName = draft.transport === "anthropic_messages" ? "x-api-key" : "Authorization";
  const headers = { ...(currentHeaders ?? {}) } as Record<string, unknown>;
  const conflictingHeader = Object.keys(headers).find((key) => key.toLowerCase() === headerName.toLowerCase());
  if (conflictingHeader) {
    return { error: t(`高级 JSON 已定义 ${conflictingHeader}；请改为 $credential 引用，或关闭“同步凭据引用”。`, `Advanced JSON already defines ${conflictingHeader}. Use a $credential reference or disable credential reference sync.`) };
  }
  headers[headerName] = draft.transport === "anthropic_messages"
    ? { $credential: draft.id.trim() }
    : { $credential: draft.id.trim(), template: "Bearer ${secret}" };
  return { value: { ...value, request_headers: headers } };
}

function definitionFromDraft(t: LocaleText, draft: ProviderDraft): { definition?: ProviderDefinitionView; error?: string } {
  const id = draft.id.trim();
  if (!/^[a-z][a-z0-9_-]{0,63}$/u.test(id) || reservedProviderIDs.has(id)) {
    return { error: t("供应商 ID 需以小写字母开头，只能包含小写字母、数字、_ 或 -，且不能使用内置名称。", "Provider IDs must start with a lowercase letter and contain only lowercase letters, numbers, _ or -. Built-in names are reserved.") };
  }
  if (draft.displayName.trim() === "") return { error: t("请填写供应商名称。", "Enter a provider name.") };
  if (!validProviderEndpointURL(draft.endpointURL.trim())) return { error: t("请求地址必须使用 HTTPS；仅本机回环地址可使用 HTTP。地址不能包含凭据、查询参数或片段。", "Endpoints must use HTTPS; only loopback addresses may use HTTP. Credentials, query parameters and fragments are not allowed in the URL.") };
  if (!validHTTPSURL(draft.websiteURL.trim(), true)) return { error: t("官网链接必须是 HTTPS URL。", "The website URL must use HTTPS.") };
  const models = normalizeModels(draft.models);
  if (models.length === 0) return { error: t("请至少填写一个模型名称。", "Enter at least one model name.") };
  if (models.length > providerModelLimit) return { error: t("每个供应商最多保存 128 个模型；请调整模型列表后再保存。", "A provider can have up to 128 models. Adjust the list before saving.") };
  const defaultModel = draft.defaultModel.trim() || models[0];
  if (!models.includes(defaultModel)) return { error: t("默认模型必须存在于模型列表中。", "The default model must be in the model list.") };
  if (draft.searchMode === "provider_native" && !draft.nativeSearchDeclared) {
    return { error: t("选择原生搜索前，请明确声明该供应商提供此能力。此声明仍需后续资格验证。", "Declare that the provider supports native search before selecting it. The declaration still requires verification.") };
  }
  const advanced = parseAdvancedJSON(t, draft.advancedJSON);
  if (!advanced.value) return { error: advanced.error };
  const advancedConfig = advanced.value;
  const capabilitiesError = modelCapabilitiesError(t, advancedConfig, models);
  if (capabilitiesError) return { error: capabilitiesError };
  const policyError = providerModelPolicyError(advancedConfig, t);
  if (policyError) return { error: policyError };
  return { definition: {
    version: "provider_definition.v1",
    id,
    display_name: draft.displayName.trim(),
    note: draft.note.trim(),
    website_url: draft.websiteURL.trim(),
    endpoint_url: draft.endpointURL.trim(),
    default_model: defaultModel,
    models,
    transport: draft.transport,
    search_mode: draft.searchMode,
    native_web_search_capability: draft.nativeSearchDeclared
      ? "declared_unverified" : "unsupported",
    enabled: draft.enabled,
    revision: draft.existing?.revision ?? 0,
    advanced_config: advancedConfig,
  } };
}

function searchModeLabel(t: LocaleText, mode: string): string {
  return ({ disabled: t("关闭", "Off"), auto: t("自动选择", "Automatic"), web: t("普通网页搜索（DuckDuckGo）", "Web search (DuckDuckGo)"), searxng: "SearXNG", provider_native: t("供应商原生", "Provider native") })[mode]
    ?? mode;
}

function definitionFingerprint(definition: ProviderDefinitionView): string {
  return JSON.stringify({
    id: definition.id,
    display_name: definition.display_name,
    note: definition.note,
    website_url: definition.website_url,
    endpoint_url: definition.endpoint_url,
    default_model: definition.default_model,
    models: definition.models,
    transport: definition.transport,
    search_mode: definition.search_mode,
    native_web_search_capability: definition.native_web_search_capability,
    enabled: definition.enabled,
    advanced_config: definition.advanced_config,
  });
}

function harnessStatusLabel(t: LocaleText, status: string): string {
  return ({ reachable: t("连接正常", "Connected"), unreachable: t("连接失败", "Connection failed"), qualified: t("验证通过", "Verified"),
    incompatible: t("协议不兼容", "Incompatible protocol"), available: t("可用", "Available"), not_configured: t("尚未配置", "Not configured"),
    protocol_mismatch: t("协议不匹配", "Protocol mismatch"), auth_failed: t("API Key 验证失败", "API key authentication failed"),
    network_failed: t("网络连接失败", "Network connection failed"), rate_limit: t("供应商限流", "Provider rate limit"), capacity: t("供应商额度或容量不足", "Provider quota or capacity exhausted"),
    model_unsupported: t("模型不受支持", "Model unsupported"), response_incomplete: t("响应未完成", "Response incomplete") } as Record<string, string>)[status] ?? status;
}

function harnessFailureLabel(t: LocaleText, reason: string): string {
  return ({ none: t("无", "None"), not_configured: t("尚未配置", "Not configured"), authentication: t("API Key 验证失败", "API key authentication failed"),
    network: t("网络连接失败", "Network connection failed"), rate_limit: t("供应商限流", "Provider rate limit"), capacity: t("供应商额度或容量不足，请检查账单或服务状态", "Provider quota or capacity exhausted; check billing or service status"),
    model_not_found: t("模型不存在", "Model not found"), protocol_incompatible: t("协议不兼容", "Incompatible protocol"), context_limit: t("输入超出上下文窗口", "Input exceeds context window"), output_limit: t("输出达到长度上限", "Output length limit reached"), paused: t("模型暂停", "Model paused"), refusal: t("模型拒绝请求", "Model refused the request") } as Record<string, string>)[reason]
    ?? reason;
}

function timestampLabel(t: LocaleText, value: string): string {
  if (!value) return t("未记录", "Not recorded");
  const parsed = new Date(value);
  return Number.isNaN(parsed.valueOf()) ? value : parsed.toLocaleString(t("zh-CN", "en-US"));
}

export function V2ProviderSettings({ client, initialPreset, onExit, onSaved,
  prepareForDraft = false, onReady }: V2ProviderSettingsProps) {
  const { t } = useLocale();
  const queryClient = useQueryClient();
  const [draft, setDraft] = useState<ProviderDraft | null>(null);
  const [error, setError] = useState("");
  const [notice, setNotice] = useState("");
  const [busy, setBusy] = useState(false);
  const [advancedOpen, setAdvancedOpen] = useState(false);
  const [deleteOpen, setDeleteOpen] = useState(false);
  const [migration, setMigration] = useState<SecretMigration | null>(null);
  const [harnessConfirmOpen, setHarnessConfirmOpen] = useState(false);
  const [harnessBusy, setHarnessBusy] = useState(false);
  const [harnessError, setHarnessError] = useState("");
  const [diagnostic, setDiagnostic] = useState<ProviderDiagnosticView | null>(null);
  const [qualification, setQualification] = useState<ModelHarnessQualificationView | null>(null);
  const [preparedDefinition, setPreparedDefinition] = useState<ProviderDefinitionView | null>(null);
  const [manualModel, setManualModel] = useState("");
  const [discoveryBusy, setDiscoveryBusy] = useState(false);
  const [discoveryError, setDiscoveryError] = useState("");
  const [discoveredModels, setDiscoveredModels] = useState<DiscoveredProviderModelView[] | null>(null);
  const [discoveryTruncated, setDiscoveryTruncated] = useState(false);
  const [selectedDiscoveredModels, setSelectedDiscoveredModels] = useState<string[]>([]);
  const discoveryAbortRef = useRef<AbortController | null>(null);
  const discoveryVersionRef = useRef(0);
  const migrationSecretRef = useRef("");
  const addButtonRef = useRef<HTMLButtonElement>(null);
  const deleteButtonRef = useRef<HTMLButtonElement>(null);
  const saveButtonRef = useRef<HTMLButtonElement>(null);
  const harnessButtonRef = useRef<HTMLButtonElement>(null);
  const firstFieldRef = useRef<HTMLInputElement>(null);
  const returnFocusRef = useRef<HTMLElement | null>(null);
  const initializedPresetIDRef = useRef<string | null>(null);
  const harnessOperationRef = useRef(false);
  const harnessOperationVersionRef = useRef(0);
  const mountedRef = useRef(true);

  useEffect(() => {
    mountedRef.current = true;
    return () => {
      mountedRef.current = false;
      harnessOperationVersionRef.current += 1;
      harnessOperationRef.current = false;
      discoveryAbortRef.current?.abort();
      discoveryVersionRef.current += 1;
    };
  }, []);

  const definitions = useQuery({
    queryKey: definitionQueryKey,
    queryFn: ({ signal }) => client.providerDefinitions(signal),
    enabled: Boolean(client.hasProviderDefinitions),
  });
  const credentials = useQuery({
    queryKey: credentialQueryKey,
    queryFn: ({ signal }) => client.providerCredentialStatuses(signal),
    enabled: Boolean(client.hasProviderCredentials),
  });

  const credentialByProvider = useMemo(() => new Map(
    (credentials.data?.items ?? []).map((status) => [status.provider, status]),
  ), [credentials.data?.items]);

  const invalidateDiscovery = () => {
    discoveryAbortRef.current?.abort();
    discoveryAbortRef.current = null;
    discoveryVersionRef.current += 1;
    setDiscoveryBusy(false);
    setDiscoveryError("");
    setDiscoveredModels(null);
    setDiscoveryTruncated(false);
    setSelectedDiscoveredModels([]);
  };

  useEffect(() => {
    if (!initialPreset) {
      initializedPresetIDRef.current = null;
      return;
    }
    if (!definitions.data || initializedPresetIDRef.current === initialPreset.id) return;
    initializedPresetIDRef.current = initialPreset.id;
    invalidateDiscovery();
    setManualModel("");
    const existing = definitions.data.providers.find((provider) => provider.id === initialPreset.id);
    setError("");
    setNotice("");
    setAdvancedOpen(false);
    setDraft(existing ? draftFromDefinition(existing) : draftFromPreset(initialPreset));
  }, [definitions.data, initialPreset]);

  useEffect(() => {
    if (!draft) return;
    const frame = requestAnimationFrame(() => firstFieldRef.current?.focus());
    return () => cancelAnimationFrame(frame);
  }, [draft !== null, draft?.existing?.id]);

  const openEditor = (next: ProviderDraft, trigger: HTMLElement) => {
    invalidateDiscovery();
    setManualModel("");
    returnFocusRef.current = trigger;
    setError("");
    setNotice("");
    setHarnessError("");
    setDiagnostic(null);
    setQualification(null);
    setPreparedDefinition(null);
    setDraft(next);
  };
  const closeEditor = () => {
    invalidateDiscovery();
    const target = returnFocusRef.current;
    harnessOperationVersionRef.current += 1;
    harnessOperationRef.current = false;
    migrationSecretRef.current = "";
    setMigration(null);
    setDeleteOpen(false);
    setHarnessConfirmOpen(false);
    setDraft(null);
    setError("");
    if (initialPreset && onExit) {
      onExit();
      return;
    }
    requestAnimationFrame(() => target?.focus());
  };
  const update = <K extends keyof ProviderDraft>(key: K, value: ProviderDraft[K], preserveDiscovery = false) => {
    if (!preserveDiscovery && ["id", "endpointURL", "transport", "apiKey", "advancedJSON"].includes(key)) invalidateDiscovery();
    setDraft((current) => current ? { ...current, [key]: value } : current);
    setError("");
    setNotice("");
    setHarnessError("");
    setDiagnostic(null);
    setQualification(null);
    setPreparedDefinition(null);
  };

  const mergeModels = (incoming: string[]): boolean => {
    if (!draft) return false;
    const models = [...new Set([...normalizeModels(draft.models), ...incoming])];
    if (models.length > providerModelLimit) {
      setDiscoveryError(t(`合并后共有 ${models.length} 个模型，超过 128 个上限。请减少勾选，或先从模型列表移除不需要的项；当前列表已保留。`, `Adding these would total ${models.length} models, exceeding the 128-model limit. Select fewer or remove existing models first. The current list is preserved.`));
      return false;
    }
    setDraft((current) => current ? draftWithModels(t, current, models.join("\n")) : current);
    setDiscoveryError(""); setError(""); setNotice("");
    setHarnessError(""); setDiagnostic(null); setQualification(null); setPreparedDefinition(null);
    return true;
  };

  const discoverModels = async () => {
    if (!draft || discoveryAbortRef.current || typeof client.discoverProviderModels !== "function") return;
    const advanced = parseAdvancedJSON(t, draft.advancedJSON);
    if (!advanced.value || !validProviderEndpointURL(draft.endpointURL.trim())) {
      setDiscoveryError(advanced.error ?? t("请先填写有效的请求地址，再获取模型。", "Enter a valid endpoint before fetching models."));
      return;
    }
    const existing = draft.existing;
    const useStoredKey = !draft.apiKey && credentialByProvider.get(draft.id)?.configured;
    if (useStoredKey && (!existing || draft.endpointURL.trim() !== existing.endpoint_url || draft.transport !== existing.transport)) {
      setDiscoveryError(t("请求地址或协议已变更。请填写此地址的 API Key 后获取模型，或先保存新的连接配置。", "The endpoint or protocol changed. Enter this endpoint's API key to fetch models, or save the new connection first."));
      return;
    }
    if (scanPlaintextSecrets(advanced.value, draft.id)?.count) {
      setDiscoveryError(t("高级 JSON 含明文密钥。请先移到 API Key 输入框，并修正凭据引用，再获取模型。", "Advanced JSON contains a plaintext key. Move it to the API key field and fix credential references before fetching models."));
      return;
    }
    const controller = new AbortController();
    const version = ++discoveryVersionRef.current;
    discoveryAbortRef.current = controller;
    setDiscoveryBusy(true); setDiscoveryError(""); setDiscoveredModels(null); setSelectedDiscoveredModels([]);
    try {
      const result = await client.discoverProviderModels({
        version: "provider_model_discovery.v1", provider_id: draft.id.trim() || "model-discovery-draft",
        endpoint_url: draft.endpointURL.trim(), transport: draft.transport,
        advanced_config: advanced.value, confirm_discovery: true,
        ...(draft.apiKey ? { secret: draft.apiKey } : useStoredKey && existing ? { expected_definition_revision: existing.revision } : {}),
      }, controller.signal);
      if (!mountedRef.current || controller.signal.aborted || version !== discoveryVersionRef.current) return;
      setDiscoveredModels(result.models);
      setDiscoveryTruncated(result.truncated);
    } catch (reason) {
      if (!mountedRef.current || controller.signal.aborted || version !== discoveryVersionRef.current) return;
      setDiscoveryError(reason instanceof Error ? reason.message : t("无法获取模型列表。当前模型与配置已保留，可手动添加。", "Could not fetch models. Existing models and configuration are preserved; add models manually if needed."));
    } finally {
      if (mountedRef.current && version === discoveryVersionRef.current) {
        discoveryAbortRef.current = null;
        setDiscoveryBusy(false);
      }
    }
  };

  const verifyHarness = async () => {
    if (!draft?.existing || harnessOperationRef.current) return;
    harnessOperationRef.current = true;
    const operationVersion = ++harnessOperationVersionRef.current;
    setHarnessConfirmOpen(false);
    setHarnessBusy(true);
    setHarnessError("");
    setDiagnostic(null);
    setQualification(null);
    const provider = draft.existing.id;
    const model = draft.existing.default_model;
    try {
      const diagnosticResult = await client.diagnoseProvider({
        version: "provider_diagnostic.v1", provider, model, confirm_diagnostic: true,
      });
      if (!mountedRef.current || operationVersion !== harnessOperationVersionRef.current) return;
      setDiagnostic(diagnosticResult);
      if (diagnosticResult.status !== "reachable" || diagnosticResult.failure_reason !== "none") {
        setHarnessError(t(`连接诊断未通过：${harnessFailureLabel(t, diagnosticResult.failure_reason)}。未继续执行 Harness 合成验证。`, `Connection diagnostic failed: ${harnessFailureLabel(t, diagnosticResult.failure_reason)}. Harness verification was not started.`));
        return;
      }
      const qualificationResult = await client.qualifyModelHarness({
        version: "model_harness_qualification.v1", provider, model,
        confirm_qualification: true,
      });
      if (!mountedRef.current || operationVersion !== harnessOperationVersionRef.current) return;
      setQualification(qualificationResult);
      await Promise.all([
        queryClient.invalidateQueries({ queryKey: ["models", "availability"] }),
        queryClient.invalidateQueries({ queryKey: ["v2", "models", "available-routes"] }),
      ]);
    } catch (reason) {
      if (!mountedRef.current || operationVersion !== harnessOperationVersionRef.current) return;
      setHarnessError(reason instanceof Error ? reason.message : t("Harness 验证失败。请检查供应商配置与网络权限。", "Harness verification failed. Check provider configuration and network permissions."));
    } finally {
      if (mountedRef.current && operationVersion === harnessOperationVersionRef.current) {
        harnessOperationRef.current = false;
        setHarnessBusy(false);
      }
    }
  };

  const prepareSavedProvider = async (definition: ProviderDefinitionView) => {
    if (harnessOperationRef.current) return;
    harnessOperationRef.current = true;
    const operationVersion = ++harnessOperationVersionRef.current;
    setHarnessBusy(true);
    setHarnessError("");
    setDiagnostic(null);
    setQualification(null);
    setNotice(t(`已保存 ${definition.display_name}，正在检查 ${definition.default_model}…`, `Saved ${definition.display_name}. Checking ${definition.default_model}…`));
    try {
      const result = await client.qualifyModelHarness({
        version: "model_harness_qualification.v1",
        provider: definition.id,
        model: definition.default_model,
        confirm_qualification: true,
      });
      if (!mountedRef.current || operationVersion !== harnessOperationVersionRef.current) return;
      setQualification(result);
      if (result.status !== "qualified" || result.failure_reason !== "none" ||
        !result.harness.root_eligible) {
        setHarnessError(t(`模型检查未通过：${harnessFailureLabel(t, result.failure_reason)}。配置和草稿都已保留；修正配置后保存，或直接重试检查。`, `Model check failed: ${harnessFailureLabel(t, result.failure_reason)}. Configuration and draft are preserved. Fix and save the configuration, or retry the check.`));
        return;
      }
      await Promise.all([
        queryClient.invalidateQueries({ queryKey: ["models", "availability"] }),
        queryClient.invalidateQueries({ queryKey: ["v2", "models", "available-routes"] }),
      ]);
      if (!mountedRef.current || operationVersion !== harnessOperationVersionRef.current) return;
      const catalog = await client.availableModelRoutes();
      if (!mountedRef.current || operationVersion !== harnessOperationVersionRef.current) return;
      const selected = catalog.routes.find((route) => route.provider_id === definition.id &&
        route.model === definition.default_model);
      if (!selected?.selectable || selected.definition_revision !== definition.revision) {
        setHarnessError(t("模型检查已通过，正在等待可用模型目录确认。配置和草稿已保留；请点击“重新检查”后继续使用这个模型。", "The model check passed; catalog availability is still unconfirmed. Configuration and draft are preserved. Select Recheck before using this model."));
        return;
      }
      setNotice(t(`${definition.display_name} / ${definition.default_model} 已可用，正在返回原草稿。`, `${definition.display_name} / ${definition.default_model} is available. Returning to the original draft.`));
      onReady?.(definition);
    } catch (reason) {
      if (!mountedRef.current || operationVersion !== harnessOperationVersionRef.current) return;
      setHarnessError(reason instanceof Error ? reason.message : t("模型检查失败。配置和草稿已保留，请重试。", "Model check failed. Configuration and draft are preserved; try again."));
    } finally {
      if (mountedRef.current && operationVersion === harnessOperationVersionRef.current) {
        harnessOperationRef.current = false;
        setHarnessBusy(false);
      }
    }
  };

  const persist = async (definition: ProviderDefinitionView, oneTimeSecret = "") => {
    if (!definitions.data) {
      setError(t("供应商定义尚未加载完成。", "Provider definitions are still loading."));
      return;
    }
    if (oneTimeSecret && !client.hasProviderCredentials) {
      setError(t("系统凭据存储当前不可用，未保存任何明文密钥。", "System credential storage is unavailable. No plaintext key was saved."));
      return;
    }
    setBusy(true);
    setError("");
    try {
      const setCredential = () => client.changeProviderCredential(definition.id, {
        version: "provider_credential.v1" as const, action: "set" as const, confirm: true,
        secret: oneTimeSecret,
      });
      const upsert = () => client.upsertProviderDefinition(definition.id, {
        version: "provider_definition_control.v1",
        expected_collection_revision: definitions.data.revision, definition, confirm: true,
      });
      let result: ProviderDefinitionMutationView;
      if (definition.revision === 0) {
        // A first-time credential is not addressable until the definition joins the registry.
        // The definition is already sanitized, so persisting it before the transient secret is safe.
        result = await upsert();
        queryClient.setQueryData<ProviderDefinitionCollectionView>(definitionQueryKey,
          result.collection);
        if (oneTimeSecret) {
          try {
            await setCredential();
          } catch {
            const saved = result.definition ?? { ...definition, revision: 1 };
            setDraft(draftFromDefinition(saved));
            setError(t("供应商定义已保存，但 API Key 未写入系统凭据。请检查系统凭据管理器，在此重新输入密钥并再次保存。已保存的定义与高级 JSON 可继续编辑。", "Provider definition saved, but the API key was not saved to system credentials. Check the credential manager, re-enter the key and save again. The saved definition and advanced JSON remain editable."));
            await queryClient.invalidateQueries({ queryKey: credentialQueryKey });
            return;
          }
        }
      } else {
        if (oneTimeSecret) await setCredential();
        result = await upsert();
      }
      queryClient.setQueryData<ProviderDefinitionCollectionView>(definitionQueryKey,
        result.collection);
      await Promise.all([
        queryClient.invalidateQueries({ queryKey: credentialQueryKey }),
        queryClient.invalidateQueries({ queryKey: ["models", "availability"] }),
        queryClient.invalidateQueries({ queryKey: ["v2", "models", "available-routes"] }),
        queryClient.invalidateQueries({ predicate: (query) => query.queryKey[0] === "v2" && query.queryKey[1] === "thread" && query.queryKey[3] === "model-route" }),
      ]);
      const saved = result.definition ?? definition;
      invalidateDiscovery();
      setNotice(t(`已保存 ${definition.display_name}`, `Saved ${definition.display_name}`));
      onSaved?.(saved);
      if (prepareForDraft) {
        setPreparedDefinition(saved);
        setDraft(draftFromDefinition(saved));
        await prepareSavedProvider(saved);
        return;
      }
      setDraft(null);
      if (onExit) {
        onExit();
        return;
      }
      requestAnimationFrame(() => returnFocusRef.current?.focus());
    } catch (reason) {
      setError(reason instanceof Error ? reason.message : t("保存供应商失败。", "Could not save provider."));
    } finally {
      setBusy(false);
    }
  };

  const save = () => {
    if (!draft || busy || harnessBusy) return;
    invalidateDiscovery();
    const parsed = definitionFromDraft(t, draft);
    if (!parsed.definition) {
      setAdvancedOpen(true);
      setError(parsed.error ?? t("供应商配置无效。", "Invalid provider configuration."));
      return;
    }
    const scan = scanPlaintextSecrets(parsed.definition.advanced_config, parsed.definition.id);
    if (scan?.count && scan.count > 1) {
      setError(t("高级 JSON 中检测到多个不同的明文密钥。请分别写入系统凭据后，再用 $credential 引用替换。未保存配置。", "Advanced JSON contains multiple different plaintext keys. Save each in system credentials and replace it with a $credential reference. Configuration was not saved."));
      return;
    }
    if (scan?.count === 1) {
      if (draft.apiKey && draft.apiKey !== scan.secret) {
        setError(t("API Key 输入框与高级 JSON 中的明文密钥不同。请只保留一个值，避免把错误凭据写入系统凭据库。", "The API key field and advanced JSON contain different keys. Keep one value to avoid saving the wrong credential."));
        return;
      }
      migrationSecretRef.current = scan.secret;
      const sanitizedJSON = JSON.stringify(scan.sanitized, null, 2);
      setMigration({
        definition: { ...parsed.definition, advanced_config: scan.sanitized },
        sanitizedJSON,
      });
      return;
    }
    let definition = parsed.definition;
    if (draft.apiKey && draft.syncCredentialReference) {
      const synced = withSyncedCredentialReference(t, definition.advanced_config, draft);
      if (!synced.value) {
        setError(synced.error ?? t("无法把凭据引用同步到高级 JSON。", "Could not sync the credential reference to advanced JSON."));
        return;
      }
      definition = { ...definition, advanced_config: synced.value };
    }
    const oneTimeSecret = draft.apiKey;
    if (oneTimeSecret) update("apiKey", "");
    void persist(definition, oneTimeSecret);
  };

  const confirmMigration = () => {
    if (!draft || !migration || busy) return;
    const secret = migrationSecretRef.current;
    migrationSecretRef.current = "";
    const sanitized = migration.sanitizedJSON;
    const definition = migration.definition;
    setDraft({ ...draft, advancedJSON: sanitized, apiKey: "" });
    setMigration(null);
    if (!secret) {
      setError(t("迁移密钥已失效，请重新输入。未保存配置。", "The key to migrate has expired. Re-enter it. Configuration was not saved."));
      return;
    }
    void persist(definition, secret);
  };

  const insertCredentialReference = () => {
    if (!draft) return;
    const providerID = draft.id.trim();
    if (!/^[a-z][a-z0-9_-]{0,63}$/u.test(providerID)) {
      setError(t("请先填写有效的供应商 ID，再插入凭据引用。", "Enter a valid provider ID before inserting a credential reference."));
      return;
    }
    try {
      const parsed = parseAdvancedJSON(t, draft.advancedJSON);
      if (!parsed.value) {
        setError(parsed.error ?? t("请先修复高级 JSON，再插入凭据引用。", "Fix advanced JSON before inserting a credential reference."));
        return;
      }
      const value = parsed.value;
      const synced = withSyncedCredentialReference(t, value, draft);
      if (!synced.value) {
        setError(synced.error ?? t("无法插入凭据引用。", "Could not insert credential reference."));
        return;
      }
      update("advancedJSON", JSON.stringify(synced.value, null, 2));
      setNotice(t("已把当前供应商的 $credential 引用插入 request_headers。运行时通过该引用读取系统凭据。", "Inserted this provider's $credential reference into request_headers. The runtime uses it to read system credentials."));
    } catch {
      setError(t("请先修复高级 JSON，再插入凭据引用。", "Fix advanced JSON before inserting a credential reference."));
    }
  };

  const removeCredential = async () => {
    if (!draft || !client.hasProviderCredentials || busy) return;
    setBusy(true);
    setError("");
    try {
      await client.changeProviderCredential(draft.id, {
        version: "provider_credential.v1", action: "delete", confirm: true, secret: "",
      });
      await queryClient.invalidateQueries({ queryKey: credentialQueryKey });
      invalidateDiscovery();
      setNotice(t("系统凭据已移除。供应商定义未改变。", "System credential removed. Provider definition unchanged."));
    } catch (reason) {
      setError(reason instanceof Error ? reason.message : t("移除系统凭据失败。", "Could not remove system credential."));
    } finally {
      setBusy(false);
    }
  };

  const removeDefinition = async () => {
    if (!draft?.existing || !definitions.data || busy) return;
    setDeleteOpen(false);
    setBusy(true);
    setError("");
    try {
      const result = await client.deleteProviderDefinition(draft.id, {
        version: "provider_definition_control.v1",
        expected_collection_revision: definitions.data.revision,
        expected_definition_revision: draft.existing.revision,
        confirm: true,
      });
      queryClient.setQueryData<ProviderDefinitionCollectionView>(definitionQueryKey,
        result.collection);
      await queryClient.invalidateQueries({ queryKey: credentialQueryKey });
      invalidateDiscovery();
      setDraft(null);
      if (initialPreset && onExit) {
        onExit();
        return;
      }
      requestAnimationFrame(() => addButtonRef.current?.focus());
    } catch (reason) {
      setError(reason instanceof Error ? reason.message : t("删除供应商失败。", "Could not delete provider."));
      await Promise.all([
        queryClient.invalidateQueries({ queryKey: definitionQueryKey }),
        queryClient.invalidateQueries({ queryKey: credentialQueryKey }),
      ]);
      invalidateDiscovery();
    } finally {
      setBusy(false);
    }
  };

  if (!client.hasProviderDefinitions) {
    return <><h1>{t("自定义配置", "Custom configuration")}</h1><p className="v2-settings-lead">{t("当前桌面后端未启用供应商定义控制。", "Provider definition controls are disabled in this desktop backend.")}</p>
      <section className="v2-settings-card v2-provider-empty"><Server aria-hidden="true" size={24} />
        <strong>{t("自定义供应商不可用", "Custom providers unavailable")}</strong><p>{t("连接具备控制权限与模型控制能力的服务后，可添加供应商并使用系统凭据库保存密钥。", "Connect to a service with control and model permissions to add providers and save keys in system credentials.")}</p>
      </section></>;
  }

  if (initialPreset && !draft && initializedPresetIDRef.current !== initialPreset.id) {
    return <><div className="v2-provider-editor-heading">
      {onExit && <button aria-label={t("返回模型目录", "Back to model catalog")} onClick={onExit} type="button">
        <ArrowLeft aria-hidden="true" size={17} /></button>}
      <div><h1>{t(`连接 ${initialPreset.displayName}`, `Connect ${initialPreset.displayName}`)}</h1>
      <p>{t("正在读取已有供应商定义与系统凭据状态。", "Loading provider definitions and system credential status.")}</p></div></div>
      {definitions.isError
        ? <section className="v2-settings-card v2-provider-empty" role="alert">
          <CircleAlert aria-hidden="true" size={22} /><strong>{t("无法读取供应商", "Could not load providers")}</strong>
          <p>{definitions.error instanceof Error ? definitions.error.message : t("请求失败", "Request failed")}</p>
          <button onClick={() => void definitions.refetch()} type="button">{t("重试", "Retry")}</button></section>
        : <section className="v2-settings-card v2-provider-empty"><p>{t("正在读取供应商…", "Loading providers…")}</p></section>}
    </>;
  }

  if (!draft) {
    const providers = definitions.data?.providers ?? [];
    return <><div className="v2-provider-heading"><div><h1>{t("自定义配置", "Custom configuration")}</h1>
      <p>{t("在模型模块中连接兼容供应商；定义与系统凭据相互分离。", "Connect compatible providers here. Definitions and system credentials are stored separately.")}</p></div>
      <button className="primary" disabled={definitions.isLoading || definitions.isError}
        onClick={(event) => openEditor(blankDraft(), event.currentTarget)} ref={addButtonRef} type="button">
        <Plus aria-hidden="true" size={15} />{t("添加供应商", "Add provider")}</button></div>
      {notice && <p aria-live="polite" className="v2-provider-notice"><Check aria-hidden="true" size={15} />{notice}</p>}
      {definitions.isLoading && <div className="v2-settings-card v2-provider-empty"><p>{t("正在读取供应商…", "Loading providers…")}</p></div>}
      {definitions.isError && <div className="v2-settings-card v2-provider-empty" role="alert">
        <CircleAlert aria-hidden="true" size={22} /><strong>{t("无法读取供应商", "Could not load providers")}</strong>
        <p>{definitions.error instanceof Error ? definitions.error.message : t("请求失败", "Request failed")}</p>
        <button onClick={() => void definitions.refetch()} type="button">{t("重试", "Retry")}</button></div>}
      {!definitions.isLoading && !definitions.isError && <section aria-label={t("自定义供应商", "Custom providers")} className="v2-provider-list">
        {providers.length === 0 && <div className="v2-settings-card v2-provider-empty"><Server aria-hidden="true" size={24} />
          <strong>{t("还没有自定义供应商", "No custom providers yet")}</strong><p>{t("点击“添加供应商”，填写服务地址和模型，保存密钥后进行连接验证。API Key 保存在系统凭据库。", "Select Add provider, enter the endpoint and models, save the key, then verify the connection. API keys are saved in system credentials.")}</p></div>}
        {providers.map((provider) => {
          const credential = credentialByProvider.get(provider.id);
          return <button className="v2-provider-row" key={provider.id}
            onClick={(event) => openEditor(draftFromDefinition(provider), event.currentTarget)} type="button">
            <span className="v2-provider-mark"><Server aria-hidden="true" size={18} /></span>
            <span className="v2-provider-row-copy"><strong>{provider.display_name}</strong>
              <small>{provider.id} · {provider.default_model}</small></span>
            <span className="v2-provider-tags"><i className={provider.enabled ? "is-ready" : ""}>
              {provider.enabled ? t("已启用", "Enabled") : t("已停用", "Disabled")}</i>
              <i>{credential?.configured ? t("密钥已存储", "Key stored") : permitsKeylessLocalProvider(provider)
                ? t("本机 · 可不填密钥", "Local · Key optional") : t("无系统密钥", "No system key")}</i>
              <i>{searchModeLabel(t, provider.search_mode)}</i></span>
            <ChevronRight aria-hidden="true" size={16} /></button>;
        })}
      </section>}
    </>;
  }

  const configuredCredential = credentialByProvider.get(draft.id)?.configured ?? false;
  const modelOptions = normalizeModels(draft.models);
  const quickSetup = Boolean(initialPreset);
  const showAdvanced = !quickSetup || advancedOpen;
  const modelField = <label>{t("默认模型", "Default model")}<select disabled={modelOptions.length === 0}
    onChange={(event) => update("defaultModel", event.target.value)}
    value={modelOptions.includes(draft.defaultModel) ? draft.defaultModel : ""}>
    <option value="">{modelOptions.length ? t("选择默认模型", "Choose default model") : t("先填写模型列表", "Enter models first")}</option>
    {modelOptions.map((model) => <option key={model} value={model}>{model}</option>)}</select></label>;
  const credentialField = <div className="v2-provider-credential-row"><label>API Key
    <input aria-describedby="provider-key-help" autoComplete="new-password"
      disabled={!client.hasProviderCredentials} onChange={(event) => update("apiKey", event.target.value)}
      placeholder={configuredCredential ? t("留空以保留现有密钥", "Leave blank to keep the existing key") : t("粘贴提供商的 API Key", "Paste the provider's API key")}
      ref={quickSetup ? firstFieldRef : undefined} type="password" value={draft.apiKey} /></label>
    {configuredCredential && <button className="secondary" disabled={busy || !client.hasProviderCredentials}
      onClick={() => void removeCredential()} type="button"><KeyRound aria-hidden="true" size={14} />{t("移除密钥", "Remove key")}</button>}</div>;
  const syncCredentialField = <label className="v2-provider-check v2-provider-sync"><input checked={draft.syncCredentialReference}
    onChange={(event) => update("syncCredentialReference", event.target.checked)} type="checkbox" />
    <span><strong>{t("把凭据引用同步到高级 JSON", "Sync credential reference to advanced JSON")}</strong>
      <small>{t("输入新 API Key 时，默认写入对应协议的 request_headers。这里只保存 $credential 引用；你仍可自由移动、改模板或关闭同步。", "New API keys add a $credential reference to the protocol's request_headers by default. You can move the reference, edit the template or disable sync.")}</small></span></label>;
  const parsedDraftDefinition = definitionFromDraft(t, draft).definition;
  const parsedCapabilities = parseAdvancedJSON(t, draft.advancedJSON).value;
  const modelCapabilities = isRecord(parsedCapabilities?.model_capabilities) ? parsedCapabilities.model_capabilities : {};
  const capabilitiesError = parsedCapabilities ? modelCapabilitiesError(t, parsedCapabilities, modelOptions) : undefined;
  const policyControlsError = parsedCapabilities ? providerPolicyControlsError(parsedCapabilities, t) : undefined;
  const policyError = parsedCapabilities ? providerModelPolicyError(parsedCapabilities, t) : undefined;
  const policyGroups = providerModelGroups(modelOptions, parsedCapabilities ?? {});
  const modelPolicies = isRecord(parsedCapabilities?.model_context_windows) ? parsedCapabilities.model_context_windows : {};
  const changePolicy = (wireModel: string, policy: ProviderModelPolicy | null) => {
    if (!parsedCapabilities || policyControlsError) return;
    // Local planning values do not change the catalog request's endpoint or
    // authentication. Keep returned limits visible while the user edits them.
    update("advancedJSON", JSON.stringify(withProviderModelPolicy(parsedCapabilities, wireModel, policy), null, 2), true);
  };
  const keylessLocal = permitsKeylessLocalProvider(parsedDraftDefinition);
  const savedConfiguration = Boolean(draft.existing && parsedDraftDefinition && !draft.apiKey &&
    definitionFingerprint(parsedDraftDefinition) === definitionFingerprint(draft.existing));
  const harnessControlAvailable = client.hasModelControl &&
    typeof client.diagnoseProvider === "function" && typeof client.qualifyModelHarness === "function";
  const harnessBlocker = !draft.existing ? t("先保存供应商配置后才能验证。", "Save provider configuration before verification.")
    : !savedConfiguration ? t("当前更改尚未保存；请先保存，再验证已持久化的精确配置。", "Changes are unsaved. Save first to verify the exact saved configuration.")
      : !configuredCredential && !keylessLocal ? t("请先把 API Key 保存到系统凭据管理器；只有未引用凭据的本机模型可不填密钥。", "Save the API key to system credentials first. Only local models without credential references can omit a key.")
        : !harnessControlAvailable ? t("当前桌面启动未开放模型诊断与 Harness 验证。", "Model diagnostics and Harness verification are unavailable in this desktop session.")
          : !draft.enabled ? t("供应商已停用；启用并保存后才能验证。", "Provider disabled. Enable and save it before verification.") : "";
  return <><div className="v2-provider-editor-heading">
    <button aria-label={initialPreset && onExit ? t("返回模型目录", "Back to model catalog") : t("返回供应商列表", "Back to providers")}
	  disabled={busy || harnessBusy} onClick={closeEditor} type="button">
      <ArrowLeft aria-hidden="true" size={17} /></button>
    <div><h1>{draft.existing ? t("编辑供应商", "Edit provider") : t("添加供应商", "Add provider")}</h1>
      <p>{quickSetup ? t("填入 API Key，确认模型并保存，然后完成下方模型检查。自建服务可展开高级设置。", "Enter the API key, confirm the model and save, then check the model below. Expand advanced settings for self-hosted services.")
        : t("填写服务地址和模型；密钥将单独保存到系统凭据管理器。", "Enter the endpoint and models. Keys are saved separately in system credentials.")}</p></div></div>
    <form className="v2-provider-form" onSubmit={(event) => { event.preventDefault(); save(); }}>
      <fieldset className="v2-provider-operation-lock" disabled={harnessBusy}>
      {quickSetup && <section className="v2-settings-card v2-provider-fields v2-provider-quick" aria-labelledby="provider-quick-title">
        <header><div><h2 id="provider-quick-title">{draft.displayName}</h2>
          <p>{t("提供商已选定；其他连接参数使用当前配置。", "Provider selected. Other connection parameters use the current configuration.")}</p></div>
          <span className={configuredCredential ? "v2-provider-status is-ready" : "v2-provider-status"}>
            {configuredCredential ? t("密钥已存储", "Key stored") : t("等待配置密钥", "Waiting for an API key")}</span></header>
        {credentialField}
        <p className="v2-provider-help" id="provider-key-help">{client.hasProviderCredentials
          ? t("密钥保存在系统凭据管理器，保存后输入框会清空。已有密钥可留空保留；远端认证请通过下方连接验证检查。", "Keys are saved in system credentials; the field clears after saving. Leave it blank to keep an existing key. Use connection verification below to check remote authentication.")
          : t("使用支持系统凭据存储的桌面版本后，可在此保存密钥。", "Use a desktop version with system credential storage to save keys here.")}</p>
        <div className="v2-provider-grid">{modelField}</div>
      </section>}
      {quickSetup && <button aria-expanded={advancedOpen} aria-controls="provider-advanced-connection"
        className="v2-model-advanced-toggle" onClick={() => setAdvancedOpen((open) => !open)} type="button">
        {advancedOpen ? t("收起高级设置", "Collapse advanced settings") : t("高级设置：自定义连接、模型与搜索", "Advanced: connection, models and search")}
      </button>}
      {showAdvanced && <section className="v2-settings-card v2-provider-fields" id="provider-advanced-connection" aria-labelledby="provider-basics-title">
        <header><div><h2 id="provider-basics-title">{t("连接", "Connection")}</h2><p>{t("请求发送到你填写的模型地址；外部服务须使用 HTTPS，本机回环地址可使用 HTTP。", "Requests go to the model endpoint you enter. External services require HTTPS; loopback addresses may use HTTP.")}</p></div>
          <label className="v2-provider-enabled"><input checked={draft.enabled}
            onChange={(event) => update("enabled", event.target.checked)} type="checkbox" />
            <span aria-hidden="true"><i /></span>{t("启用", "Enable")}</label></header>
        <div className="v2-provider-grid">
          <label>{t("供应商 ID", "Provider ID")}<input autoComplete="off" disabled={Boolean(draft.existing)}
            maxLength={64} onChange={(event) => update("id", event.target.value)}
            placeholder={t("例如 acme-ai", "For example, acme-ai")} ref={!quickSetup ? firstFieldRef : undefined} required spellCheck={false}
            value={draft.id} /></label>
          <label>{t("显示名称", "Display name")}<input autoComplete="organization" maxLength={128}
            onChange={(event) => update("displayName", event.target.value)}
            placeholder={t("例如 Acme AI", "For example, Acme AI")} ref={!quickSetup && draft.existing ? firstFieldRef : undefined}
            required value={draft.displayName} /></label>
          <label className="is-wide">{t("备注", "Notes")}<input maxLength={2048}
            onChange={(event) => update("note", event.target.value)}
            placeholder={t("可选，例如团队账号", "Optional, for example a team account")} value={draft.note} /></label>
          <label className="is-wide">{t("官网链接", "Website")}<input inputMode="url"
            onChange={(event) => update("websiteURL", event.target.value)}
            placeholder={t("https://example.com（可选）", "https://example.com (optional)")} type="url" value={draft.websiteURL} /></label>
          <label className="is-wide">{t("请求地址", "Endpoint")}<input inputMode="url"
            onChange={(event) => {
              invalidateDiscovery();
              const endpointURL = event.target.value;
              setDraft((current) => current ? { ...current, endpointURL,
                ...(knownNativeSearchUnsupported(endpointURL) ? {
                  searchMode: current.searchMode === "provider_native" ? "auto" : current.searchMode,
                  nativeSearchDeclared: false,
                } : {}),
              } : current);
              setError(""); setNotice(""); setHarnessError(""); setDiagnostic(null); setQualification(null); setPreparedDefinition(null);
            }}
            placeholder="https://api.example.com/v1/chat/completions" required type="url"
            value={draft.endpointURL} /></label>
          <label>{t("协议", "Protocol")}<select onChange={(event) => {
            invalidateDiscovery();
            const transport = event.target.value as ProviderDraft["transport"];
            setDraft((current) => current ? {
              ...current,
              transport,
              // Transport compatibility does not declare hosted-search support.
              // Keep an explicit declaration only while its transport can use it.
              ...(transport !== "openai_responses" ? {
                searchMode: current.searchMode === "provider_native" ? "auto" : current.searchMode,
                nativeSearchDeclared: false,
              } : {}),
            } : current);
            setError("");
            setNotice("");
            setHarnessError("");
            setDiagnostic(null);
            setQualification(null);
            setPreparedDefinition(null);
          }} value={draft.transport}>
            <option value="openai_chat_completions">OpenAI Chat Completions</option>
            <option value="openai_responses">OpenAI Responses</option>
            <option value="anthropic_messages">Anthropic Messages</option>
          </select></label>
          {!quickSetup && modelField}
        </div>
      </section>}

      {showAdvanced && <section className="v2-settings-card v2-provider-fields" aria-labelledby="provider-models-title">
        <header><div><h2 id="provider-models-title">{t("可用模型", "Available models")}</h2><p>{t("使用当前 API Key 从服务获取模型，或手动补充。获取后选择要加入的模型，保存时生效。", "Fetch models using the current API key or add them manually. Select models from the results; changes take effect when saved.")}</p></div>
          <button className="secondary" disabled={busy || discoveryBusy || typeof client.discoverProviderModels !== "function"}
            onClick={() => void discoverModels()} type="button">
            {discoveryBusy && <LoaderCircle aria-hidden="true" className="spin" size={14} />}
            {discoveryBusy ? t("正在获取模型…", "Fetching models…") : t("获取模型列表", "Fetch models")}</button></header>
        <div className="v2-provider-grid">
          <div className="v2-provider-model-add is-wide"><label>{t("手动添加模型", "Add model manually")}<input autoComplete="off"
            onChange={(event) => setManualModel(event.target.value)} placeholder={t("输入完整模型名称", "Enter the full model name")} spellCheck={false} value={manualModel}
            onKeyDown={(event) => {
              if (event.key !== "Enter" || event.nativeEvent.isComposing || event.keyCode === 229) return;
              event.preventDefault();
              if (manualModel.trim() && mergeModels(normalizeModels(manualModel))) setManualModel("");
            }} /></label><button disabled={!manualModel.trim() || busy}
            onClick={() => { if (mergeModels(normalizeModels(manualModel))) setManualModel(""); }} type="button">
            <Plus aria-hidden="true" size={14} />{t("添加模型", "Add model")}</button></div>
          <label className="is-wide">{t("模型列表", "Model list")}<textarea aria-describedby="provider-models-help"
            aria-label={t("模型列表", "Model list")}
            onChange={(event) => {
              const models = event.target.value;
              setDraft((current) => current ? draftWithModels(t, current, models) : current);
              setError(""); setNotice(""); setHarnessError(""); setDiagnostic(null); setQualification(null); setPreparedDefinition(null);
            }}
            placeholder={"model-a\nmodel-b"} rows={3} spellCheck={false} value={draft.models} />
            <small id="provider-models-help">{t("每行或逗号分隔，最多 128 个（当前 ", "One per line or comma-separated, up to 128 (currently ")}{modelOptions.length} {t(" 个）。名称按 model_mapping 映射后发送；移除模型会清除对应图片声明，新名称需重新确认。", "). Names are sent after model_mapping is applied. Removing a model clears its image capability declaration; confirm capabilities for new names.")}</small></label>
          {typeof client.discoverProviderModels !== "function" && <p className="v2-provider-field-help is-wide">{t("当前服务提供手动添加：填写完整模型名称，保存后进行连接验证。", "This service supports manual model entry. Enter the full name, save and verify the connection.")}</p>}
          {discoveryError && <p className="v2-inline-error is-wide" role="alert">{discoveryError}</p>}
          {discoveredModels && <div aria-label={t("获取到的模型", "Fetched models")} className="v2-provider-discovery is-wide">
            <p aria-live="polite">{t("服务返回 ", "The service returned ")}{discoveredModels.length} {t(" 个模型。勾选后加入当前列表，保存后继续完成连接与 Harness 验证。", " models. Select models to add, save, then complete connection and Harness verification.")}</p>
            {discoveryTruncated && <p>{t("服务模型列表较长，本次结果未完整返回。可从已返回结果中选择，也可手动添加完整模型名。", "The model list was truncated. Choose from returned results or enter full model names manually.")}</p>}
            {discoveredModels.length === 0 && <p>{t("服务没有返回模型。当前列表已保留，可手动添加模型名。", "No models were returned. The current list is preserved; add names manually if needed.")}</p>}
            <div className="v2-provider-discovery-options">{discoveredModels.map((model) => {
              const added = modelOptions.includes(model.id);
              return <label className="v2-provider-check" key={model.id}><input type="checkbox"
                aria-label={t(`选择模型 ${model.id}`, `Select model ${model.id}`)} checked={added || selectedDiscoveredModels.includes(model.id)} disabled={added}
                onChange={(event) => setSelectedDiscoveredModels((current) => event.target.checked ? [...current, model.id] : current.filter((id) => id !== model.id))} />
                <span><strong>{model.id}{added ? t(" · 已添加", "· Added") : ""}</strong>
                  {model.display_name && model.display_name !== model.id && <small>{model.display_name}</small>}
                  {(model.input_token_limit !== undefined || model.output_token_limit !== undefined) && <small>{t("服务返回的限制：", "Limits reported by the service: ")}{model.input_token_limit !== undefined && t(`输入 ${model.input_token_limit.toLocaleString(t("zh-CN", "en-US"))} token`, `Input ${model.input_token_limit.toLocaleString(t("zh-CN", "en-US"))} tokens`)}
                    {model.input_token_limit !== undefined && model.output_token_limit !== undefined && " · "}
                    {model.output_token_limit !== undefined && t(`输出 ${model.output_token_limit.toLocaleString(t("zh-CN", "en-US"))} token`, `Output ${model.output_token_limit.toLocaleString(t("zh-CN", "en-US"))} tokens`)}{t("。未自动写入本地策略。", ". These are not automatically saved to local policy.")}</small>}
                </span></label>;
            })}</div>
            {discoveredModels.length > 0 && <button disabled={!selectedDiscoveredModels.some((model) => !modelOptions.includes(model))}
              onClick={() => { if (mergeModels(selectedDiscoveredModels)) setSelectedDiscoveredModels([]); }} type="button">{t("添加所选模型", "Add selected models")}</button>}
          </div>}
        </div>
      </section>}

      {showAdvanced && <section className="v2-settings-card v2-provider-fields" aria-labelledby="provider-output-title">
        <header><div><h2 id="provider-output-title">{t("模型输出限制", "Model output limits")}</h2>
          <p>{t("每个模型可继承默认策略，或自定义默认输出与单次上限。输出 Token 预算包含推理内容和可见文字，分配方式由模型服务决定。", "Each model can inherit default policy or use custom output defaults and per-request limits. The output token budget includes reasoning and visible text; the provider decides allocation.")}</p></div></header>
        <div className="v2-provider-grid">
          {!modelOptions.length && <p className="v2-provider-field-help is-wide">{t("先填写模型列表。", "Enter models first.")}</p>}
          {!parsedCapabilities && <p className="v2-provider-field-help is-wide">{t("先修正下方高级 JSON，再设置输出限制。", "Fix advanced JSON below before setting output limits.")}</p>}
          {policyControlsError && <p className="v2-provider-field-help is-wide" role="alert">{policyControlsError}</p>}
          {policyError && <p className="v2-provider-field-help is-wide" role="alert">{policyError}</p>}
          {policyGroups.map(({ wireModel, aliases }) => {
            const inherited = inheritedProviderModelPolicy(draft.endpointURL, draft.transport, wireModel, parsedCapabilities ?? {});
            const custom = Object.hasOwn(modelPolicies, wireModel);
            const rawPolicy = modelPolicies[wireModel];
            const invalidShape = custom && !policyShape(rawPolicy);
            const policy = custom && policyShape(rawPolicy) ? rawPolicy : inherited.policy;
            const disabled = !parsedCapabilities || Boolean(policyControlsError) || invalidShape;
            const source = custom ? t("用户覆盖", "Custom override") : inherited.source === "known" ? t("继承默认 · 已知模型的本地规划策略", "Inherited · Local planning policy for known models") : t("继承默认 · 本地保守回退（服务端实际限制未知）", "Inherited · Conservative local fallback (server limits unknown)");
            return <article aria-label={t(`${wireModel} 输出策略`, `${wireModel} output policy`)} className="v2-provider-policy is-wide" key={wireModel}>
              <div className="v2-provider-policy-heading"><strong>{aliases.join(" / ")}</strong><span>{source}</span></div>
              {(aliases.length > 1 || aliases[0] !== wireModel) && <p>{t("实际发送模型：", "Model sent to the service: ")}{wireModel}{t("。映射到此模型的名称共享以下设置。", ". Names mapped to this model share these settings.")}</p>}
              {!custom && inherited.source === "fallback" && <p>{t("这里设置本地预算；服务端实际限制请向供应商确认。带工具的请求会按此上限预留输出空间。", "These are local budgets; confirm actual server limits with the provider. Tool requests reserve output space using this limit.")}</p>}
              {!custom && inherited.source === "known" && <p>{inherited.explicitDefault
                ? t("默认输出 16,384 token 是应用发送给服务的单次请求限制；128,000 token 上限来自官方模型资料，可在此自定义。", "The default output of 16,384 tokens is this app's per-request limit; the 128,000-token maximum comes from official model documentation. Customize it here.")
                : t("容量依据已知模型资料；默认输出是应用的本地预算，可按任务需要调整。", "Capacity uses known model documentation. Default output is a local app budget and can be adjusted for the task.")}</p>}
              <label className="v2-provider-check"><input aria-label={t(`${wireModel} 自定义输出限制`, `${wireModel} custom output limits`)} type="checkbox" checked={custom} disabled={disabled}
                onChange={(event) => changePolicy(wireModel, event.target.checked ? inherited.policy : null)} />{t("自定义此模型的输出限制", "Customize output limits for this model")}</label>
              <div className="v2-provider-policy-fields">{([
                ["default_output_tokens", t("默认输出 token", "Default output tokens"), 1, 1000000],
                ["max_output_tokens", t("单次输出上限 token", "Maximum output tokens per request"), 1, 1000000],
                ["window_tokens", t("上下文窗口 token", "Context window tokens"), 4096, 2097152],
              ] as const).map(([field, label, min, max]) => <label key={field}>{label}<input type="number" inputMode="numeric"
                aria-label={`${wireModel} ${label}`} min={min} max={max} step={1} disabled={disabled || !custom} value={policy[field] || ""}
                onChange={(event) => {
                  const value = Number(event.target.value);
                  changePolicy(wireModel, { ...policy, [field]: Number.isFinite(value) ? value : 0 });
                }} /></label>)}</div>
              <p>{t("默认输出 ≤ 单次上限；单次上限须小于上下文窗口减去 1024 token，且不超过当前适配器的 1,000,000 token。", "Default output must not exceed the per-request limit. That limit must be below the context window minus 1,024 tokens and no higher than the adapter's 1,000,000-token limit.")}</p>
            </article>;
          })}
        </div>
      </section>}

      {showAdvanced && <section className="v2-settings-card v2-provider-fields" aria-labelledby="provider-images-title">
        <header><div><h2 id="provider-images-title">{t("图片输入", "Image input")}</h2><p>{t("按供应商说明选择图片能力声明，再在任务中用图片检查实际效果。原始图片会发送到上方的请求地址，请确认该端点适合接收这些内容。", "Declare image support based on provider documentation, then test it with an image in a task. Original images are sent to the endpoint above; confirm it is appropriate for the content.")}</p></div></header>
        <div className="v2-provider-grid">{modelOptions.map((model) => {
          const capability = modelCapabilities[model];
          const state = isRecord(capability) && ["supported", "unsupported", "unknown"].includes(String(capability.vision)) ? String(capability.vision) : "unknown";
          return <label key={model}>{model}<select aria-label={t(`${model} 图片输入`, `${model} image input`)} disabled={!parsedCapabilities || Boolean(capabilitiesError)}
            value={state} onChange={(event) => {
              if (!parsedCapabilities || capabilitiesError) return;
              update("advancedJSON", JSON.stringify({ ...parsedCapabilities,
                model_capabilities: { ...modelCapabilities, [model]: { vision: event.target.value } } }, null, 2));
            }}><option value="unknown">{t("尚未确认", "Unconfirmed")}</option><option value="supported">{t("支持图片", "Images supported")}</option><option value="unsupported">{t("不支持图片", "Images unsupported")}</option></select></label>;
        })}
        {!modelOptions.length && <p className="v2-provider-field-help is-wide">{t("先填写模型列表。", "Enter models first.")}</p>}
        {!parsedCapabilities && <p className="v2-provider-field-help is-wide">{t("先修正下方高级 JSON，再设置图片能力。", "Fix advanced JSON below before setting image capabilities.")}</p>}
        {capabilitiesError && <p className="v2-provider-field-help is-wide">{capabilitiesError}</p>}
        </div>
      </section>}

      {showAdvanced && <section className="v2-settings-card v2-provider-fields" aria-labelledby="provider-search-title">
        <header><div><h2 id="provider-search-title">{t("网页搜索", "Web search")}</h2>
          <p>{t("先确认供应商提供哪种搜索能力，再选择搜索方式。原生搜索会在首次真实调用前完成有界资格验证。", "Confirm the provider's search capabilities before choosing a mode. Native search undergoes bounded verification before its first real use.")}</p></div></header>
        <div className="v2-provider-grid">
          <label className="is-wide">{t("搜索策略", "Search policy")}<select aria-label={t("搜索策略", "Search policy")} onChange={(event) => {
            const mode = event.target.value as ProviderDraft["searchMode"];
            setDraft((current) => current ? { ...current, searchMode: mode,
              nativeSearchDeclared: mode === "provider_native" || current.nativeSearchDeclared } : current);
            setError("");
            setNotice("");
            setHarnessError("");
            setDiagnostic(null);
            setQualification(null);
          }} value={draft.searchMode}>
            <option value="disabled">{t("关闭", "Off")}</option><option value="auto">{t("自动选择", "Automatic")}</option>
            <option value="web">{t("普通网页搜索（DuckDuckGo）", "Web search (DuckDuckGo)")}</option>
            <option value="searxng">SearXNG</option>
            <option disabled={knownNativeSearchUnsupported(draft.endpointURL)} value="provider_native">{t("供应商原生", "Provider native")}</option>
          </select><small>{draft.searchMode === "web"
            ? t("搜索查询会发送到 DuckDuckGo，可直接用于当前模型。搜索与网页读取遵循当前任务的网页访问范围。", "Queries go to DuckDuckGo and can be used with this model. Search and page reads follow this task's web access scope.")
            : draft.searchMode === "auto"
              ? t("自动优先使用已声明并验证的供应商原生搜索，随后可回退到已配置的 SearXNG。要使用 DuckDuckGo，请选择“普通网页搜索”。", "Automatic mode prefers declared and verified native search, then may fall back to configured SearXNG. Select Web search to use DuckDuckGo.")
              : draft.searchMode === "searxng"
                ? t("先在 Desktop 启动配置中填写 SearXNG 地址。查询使用该地址，并遵循当前任务的网页访问范围。", "Configure the SearXNG URL in desktop startup settings first. Queries use that URL and follow task web access permissions.")
                : draft.searchMode === "disabled"
                  ? t("此供应商的网页搜索已关闭。任务的网页访问范围可在任务设置中管理。", "Web search is disabled for this provider. Manage the task's web access scope in task settings.")
                  : t("选择供应商原生时会绑定当前模型、端点、定义 revision 与系统凭据代际。", "Native search is bound to the current model, endpoint, definition revision and system credential generation.")}</small></label>
          {knownNativeSearchUnsupported(draft.endpointURL) && <p className="is-wide">
            {draft.existing && draft.searchMode === "provider_native"
              ? t("此官方 DeepSeek 旧配置仍选择了原生搜索，需要更换搜索方式。请选择“普通网页搜索”，或配置 SearXNG 后选择“自动选择”。", "This older official DeepSeek configuration still selects native search. Select Web search, or configure SearXNG and select Automatic.")
              : t("此官方 DeepSeek 地址可使用“普通网页搜索”。也可配置 SearXNG 后选择“自动选择”。", "This official DeepSeek endpoint can use Web search, or Automatic after configuring SearXNG.")}
          </p>}
          <label className="v2-provider-check is-wide"><input aria-label={t("声明供应商具备原生 Web Search", "Declare native Web Search support")}
            checked={draft.nativeSearchDeclared}
            disabled={knownNativeSearchUnsupported(draft.endpointURL)}
            onChange={(event) => update("nativeSearchDeclared", event.target.checked)} type="checkbox" />
            <span><strong>{t("声明供应商具备原生 Web Search", "Declare native Web Search support")}</strong>
              <small>{t("勾选后会登记原生搜索能力声明。首次真实搜索需完成有界验证，可能产生 API 调用费用；搜索使用当前供应商端点的授权范围，网页抓取遵循任务权限。", "Checking this records a native search declaration. The first search requires bounded verification and may incur API fees. Search uses the provider endpoint's authorization; page reads follow task permissions.")}</small></span></label>
        </div>
      </section>}

      {!quickSetup && <section className="v2-settings-card v2-provider-fields" aria-labelledby="provider-credential-title">
        <header><div><h2 id="provider-credential-title">{t("系统凭据", "System credentials")}</h2>
          <p>{t("密钥保存在操作系统凭据管理器。此页读取存储状态，通过连接验证检查远端认证。", "Keys are stored in the operating system's credential manager. This page shows storage status; connection verification checks remote authentication.")}</p></div>
          <span className={configuredCredential ? "v2-provider-status is-ready" : "v2-provider-status"}>
            {configuredCredential ? t("已存储", "Stored") : t("未存储", "Not stored")}</span></header>
        {credentialField}
        {syncCredentialField}
        <p id="provider-key-help" className="v2-provider-help">{keylessLocal
          ? t("本机模型未引用凭据，可留空并进行连接验证；若服务要求认证，请填写其真实 API Key。", "This local model has no credential reference. Leave the key blank to verify, or enter its real API key if the service requires authentication.")
          : <>{t("保存后输入框会立即清空。高级 JSON 应使用 ", "The field clears after saving. Advanced JSON should use a ")}<code>$credential</code> {t(" 引用。", " reference.")}</>}</p>
      </section>}

      <section className="v2-settings-card v2-provider-fields v2-provider-harness"
        aria-labelledby="provider-harness-title">
        <header><div><h2 id="provider-harness-title">{prepareForDraft ? t("首次模型检查", "First model check") : t("连接与 Harness 验证", "Connection and Harness verification")}</h2>
          <p>{prepareForDraft
            ? t("保存后自动检查工具调用、结果回传与结构化 JSON；通过后返回原草稿并选中这个模型。检查通常会产生两次模型调用。", "After saving, automatically check tool calls, result submission and structured JSON. On success, return to the original draft with this model selected. The check usually makes two model calls.")
            : t("先做一次最小连接诊断；通过后再验证工具调用、结果回传与结构化 JSON 合同。", "Run a minimal connection diagnostic, then verify tool calls, result submission and structured JSON.")}</p></div>
          {!prepareForDraft && <button className="secondary" disabled={Boolean(harnessBlocker) || busy || harnessBusy}
            onClick={() => setHarnessConfirmOpen(true)} ref={harnessButtonRef} type="button">
            {harnessBusy ? <LoaderCircle aria-hidden="true" className="spin" size={14} />
              : <ShieldCheck aria-hidden="true" size={14} />}
            {harnessBusy ? t("正在验证…", "Verifying…") : t("测试并验证 Harness", "Test and verify Harness")}</button>}
          {prepareForDraft && preparedDefinition && harnessError && <button className="secondary"
            disabled={busy || harnessBusy} onClick={() => void prepareSavedProvider(preparedDefinition)}
            ref={harnessButtonRef} type="button"><ShieldCheck aria-hidden="true" size={14} />{t("重新检查", "Recheck")}</button>}</header>
        {!prepareForDraft && harnessBlocker && <p className="v2-provider-help">{harnessBlocker}</p>}
        {!harnessBlocker && !diagnostic && !qualification && !harnessBusy &&
          <p className="v2-provider-help">{t("验证绑定当前供应商、默认模型、端点、定义 revision 与系统凭据代际；任一项变化后需重新验证。", "Verification is bound to the provider, default model, endpoint, definition revision and credential generation. Any change requires verification again.")}</p>}
        {harnessBusy && <p aria-live="polite" className="v2-provider-harness-progress">
          <Activity aria-hidden="true" size={14} />{t("正在执行有界供应商调用，请勿关闭应用…", "Running bounded provider calls. Keep the app open…")}</p>}
        {diagnostic && <article aria-label={t("连接诊断结果", "Connection diagnostic results")} className="v2-provider-harness-result" role="status">
          <div><strong>{t("连接诊断", "Connection diagnostic")}</strong><span className={diagnostic.status === "reachable" ? "is-ready" : "is-error"}>
            {harnessStatusLabel(t, diagnostic.status)}</span></div>
          <dl><div><dt>{t("模型", "Models")}</dt><dd>{diagnostic.provider} / {diagnostic.model}</dd></div>
            <div><dt>{t("结果", "Result")}</dt><dd>{diagnostic.failure_reason === "none"
              ? harnessStatusLabel(t, diagnostic.qualification_status) : harnessFailureLabel(t, diagnostic.failure_reason)}</dd></div>
            <div><dt>{t("耗时", "Duration")}</dt><dd>{diagnostic.duration_ms} ms</dd></div></dl>
        </article>}
        {qualification && <article aria-label={t("Harness 验证结果", "Harness verification results")} className="v2-provider-harness-result" role="status">
          <div><strong>{t("Harness 验证", "Harness verification")}</strong><span className={qualification.status === "qualified" ? "is-ready" : "is-error"}>
            {harnessStatusLabel(t, qualification.status)}</span></div>
          <dl><div><dt>{t("资格状态", "Qualification")}</dt><dd>{harnessStatusLabel(t, qualification.qualification_status)}</dd></div>
            <div><dt>{t("模型调用", "Model calls")}</dt><dd>{qualification.model_calls} {t(" 次", " calls")}</dd></div>
            <div><dt>{t("验证时间", "Verified at")}</dt><dd>{timestampLabel(t, qualification.harness.qualified_at)}</dd></div>
            <div><dt>{t("有效期至", "Expires at")}</dt><dd>{timestampLabel(t, qualification.harness.expires_at)}</dd></div></dl>
        </article>}
        {harnessError && <p className="v2-inline-error v2-provider-harness-error" role="alert">
          <CircleAlert aria-hidden="true" size={15} />{harnessError}</p>}
      </section>

      {showAdvanced && <section className="v2-settings-card v2-provider-fields" aria-labelledby="provider-json-title">
        <header><div><h2 id="provider-json-title">{t("高级 JSON", "Advanced JSON")}</h2>
          <p>{t("在这里编辑 request_headers、request_body 与 model_mapping；其他扩展会原样保留。Harness 核心字段由运行时管理。", "Edit request_headers, request_body and model_mapping here. Other extensions are preserved. The runtime manages core Harness fields.")}</p></div>
          <button className="secondary" onClick={insertCredentialReference} type="button">
            <KeyRound aria-hidden="true" size={14} />{t("插入凭据引用", "Insert credential reference")}</button></header>
        <label className="v2-provider-json">
          <textarea aria-label={t("高级 JSON", "Advanced JSON")} onChange={(event) => update("advancedJSON", event.target.value)}
            spellCheck={false} value={draft.advancedJSON} /></label>
        {quickSetup && syncCredentialField}
        <p className="v2-provider-help">{t("示例：", "Example: ")}<code>{'{"request_headers":{"Authorization":{"$credential":"' + (draft.id || "provider-id") + '","template":"Bearer ${secret}"}}}'}</code>{t("。如粘贴一个明文密钥，保存时会先要求二次确认并迁移。", ". Pasting a plaintext key requires confirmation to migrate it when saving.")}</p>
      </section>}

      {error && <p className="v2-inline-error v2-provider-error" role="alert">
        <CircleAlert aria-hidden="true" size={16} />{error}</p>}
      {notice && <p aria-live="polite" className="v2-provider-notice"><Check aria-hidden="true" size={15} />{notice}</p>}
      <footer className="v2-provider-actions">
        {draft.existing && <button className="danger-quiet" disabled={busy}
          onClick={() => setDeleteOpen(true)} ref={deleteButtonRef} type="button">
          <Trash2 aria-hidden="true" size={15} />{t("删除供应商", "Delete provider")}</button>}
        <span />
        <button disabled={busy} onClick={closeEditor} type="button">{t("取消", "Cancel")}</button>
        <button className="primary" disabled={busy} ref={saveButtonRef} type="submit">
          <Save aria-hidden="true" size={15} />{busy ? t("正在保存…", "Saving…") : prepareForDraft ? t("保存并检查", "Save and check") : t("保存", "Save")}</button>
      </footer>
      </fieldset>
    </form>

    <V2ConfirmDialog busy={busy} confirmLabel={t("删除", "Delete")} danger
      description={configuredCredential
        ? t("确认供应商版本与路由引用允许删除后，将一并移除供应商定义和系统凭据。仍被路由引用或版本已更新时，原凭据会保留。", "After checking the provider revision and route references, delete both the definition and system credential. If routes still use it or its revision changed, the credential is preserved.")
        : t("供应商定义将从可用模型列表中移除。仍有路由引用它时，请先更换这些路由的模型，再重试删除。", "The definition will be removed from available models. If routes still reference it, change their models before deleting.")}
      onCancel={() => setDeleteOpen(false)} onConfirm={() => void removeDefinition()}
      open={deleteOpen} returnFocusRef={deleteButtonRef} title={t(`删除 ${draft.displayName || draft.id}`, `Delete ${draft.displayName || draft.id}`)} />
    <V2ConfirmDialog busy={busy} confirmLabel={t("迁移并保存", "Migrate and save")} danger
      description={t("高级 JSON 中检测到一个明文密钥。确认后，密钥会移入操作系统凭据管理器，JSON 中对应位置会替换为当前供应商的 $credential 引用。", "A plaintext key was found in advanced JSON. After confirmation, it moves to the OS credential manager and its JSON position is replaced with this provider's $credential reference.")}
      onCancel={() => { migrationSecretRef.current = ""; setMigration(null); }}
      onConfirm={confirmMigration} open={Boolean(migration)} returnFocusRef={saveButtonRef}
      title={t("迁移明文密钥？", "Migrate plaintext key?")} />
    <V2ConfirmDialog busy={harnessBusy} confirmLabel={t("开始验证", "Start verification")}
      description={t(`将对 ${draft.displayName || draft.id} 的默认模型 ${draft.defaultModel || t("未选择", "Not selected")} 发起一次最小连接诊断；诊断通过后会继续执行 Harness 合成验证，当前协议通常还需要两次模型调用。供应商可能按这些调用计费，且请求会发送到已保存的端点。`, `Run a minimal connection diagnostic for ${draft.displayName || draft.id}, using its default model ${draft.defaultModel || t("未选择", "Not selected")}. On success, Harness verification usually makes two more model calls. Requests go to the saved endpoint and may incur provider charges.`)}
      onCancel={() => setHarnessConfirmOpen(false)} onConfirm={() => void verifyHarness()}
      open={harnessConfirmOpen} returnFocusRef={harnessButtonRef}
      title={t("测试并验证 Harness？", "Test and verify Harness?")} />
  </>;
}
import "./settings.css";
