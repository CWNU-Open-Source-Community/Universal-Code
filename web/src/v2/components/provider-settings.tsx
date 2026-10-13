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
  onReady?: (definition: ProviderDefinitionView) => void;
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

function draftWithModels(draft: ProviderDraft, models: string): ProviderDraft {
  const next = { ...draft, models };
  const advanced = parseAdvancedJSON(draft.advancedJSON).value;
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

function modelCapabilitiesError(config: Record<string, unknown>, models: string[]): string | undefined {
  if (config.model_capabilities === undefined) return undefined;
  if (!isRecord(config.model_capabilities)) return "高级 JSON 中的 model_capabilities 必须是对象；原内容已保留，请手动修正。";
  for (const [model, value] of Object.entries(config.model_capabilities)) {
    if (!models.includes(model)) return `图片能力仍引用模型列表以外的“${model}”；原内容已保留，请在高级 JSON 中核对修正。`;
    if (!knownVisionDeclaration(value)) return `模型“${model}”的图片能力配置含无效或未知字段；原内容已保留，请在高级 JSON 中核对修正。`;
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

function rejectDuplicateJSONKeys(source: string): void {
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
        if (keys.has(key)) throw new Error(`高级 JSON 包含重复字段“${key}”。`);
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

function parseAdvancedJSON(source: string): { value?: Record<string, unknown>; error?: string } {
  let parsed: unknown;
  try {
    parsed = JSON.parse(source);
    rejectDuplicateJSONKeys(source);
  } catch (reason) {
    const message = reason instanceof Error && reason.message.startsWith("高级 JSON")
      ? reason.message : "高级 JSON 不是有效的 JSON。";
    return { error: message };
  }
  if (!isRecord(parsed)) return { error: "高级 JSON 的顶层必须是对象。" };
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

function withSyncedCredentialReference(value: unknown, draft: ProviderDraft): {
  value?: Record<string, unknown>;
  error?: string;
} {
  if (!isRecord(value)) return { error: "高级 JSON 的顶层必须是对象。" };
  if (hasOwnedCredentialReference(value, draft.id.trim())) return { value };
  const currentHeaders = value.request_headers;
  if (currentHeaders !== undefined && !isRecord(currentHeaders)) {
    return { error: "要同步凭据引用，request_headers 必须是 JSON 对象。" };
  }
  const headerName = draft.transport === "anthropic_messages" ? "x-api-key" : "Authorization";
  const headers = { ...(currentHeaders ?? {}) } as Record<string, unknown>;
  const conflictingHeader = Object.keys(headers).find((key) => key.toLowerCase() === headerName.toLowerCase());
  if (conflictingHeader) {
    return { error: `高级 JSON 已定义 ${conflictingHeader}；请改为 $credential 引用，或关闭“同步凭据引用”。` };
  }
  headers[headerName] = draft.transport === "anthropic_messages"
    ? { $credential: draft.id.trim() }
    : { $credential: draft.id.trim(), template: "Bearer ${secret}" };
  return { value: { ...value, request_headers: headers } };
}

function definitionFromDraft(draft: ProviderDraft): { definition?: ProviderDefinitionView; error?: string } {
  const id = draft.id.trim();
  if (!/^[a-z][a-z0-9_-]{0,63}$/u.test(id) || reservedProviderIDs.has(id)) {
    return { error: "供应商 ID 需以小写字母开头，只能包含小写字母、数字、_ 或 -，且不能使用内置名称。" };
  }
  if (draft.displayName.trim() === "") return { error: "请填写供应商名称。" };
  if (!validProviderEndpointURL(draft.endpointURL.trim())) return { error: "请求地址必须使用 HTTPS；仅本机回环地址可使用 HTTP。地址不能包含凭据、查询参数或片段。" };
  if (!validHTTPSURL(draft.websiteURL.trim(), true)) return { error: "官网链接必须是 HTTPS URL。" };
  const models = normalizeModels(draft.models);
  if (models.length === 0) return { error: "请至少填写一个模型名称。" };
  if (models.length > providerModelLimit) return { error: "每个供应商最多保存 128 个模型；请调整模型列表后再保存。" };
  const defaultModel = draft.defaultModel.trim() || models[0];
  if (!models.includes(defaultModel)) return { error: "默认模型必须存在于模型列表中。" };
  if (draft.searchMode === "provider_native" && !draft.nativeSearchDeclared) {
    return { error: "选择原生搜索前，请明确声明该供应商提供此能力。此声明仍需后续资格验证。" };
  }
  const advanced = parseAdvancedJSON(draft.advancedJSON);
  if (!advanced.value) return { error: advanced.error };
  const advancedConfig = advanced.value;
  const capabilitiesError = modelCapabilitiesError(advancedConfig, models);
  if (capabilitiesError) return { error: capabilitiesError };
  const policyError = providerModelPolicyError(advancedConfig);
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

function searchModeLabel(mode: string): string {
  return ({ disabled: "关闭", auto: "自动选择", web: "普通网页搜索（DuckDuckGo）", searxng: "SearXNG", provider_native: "供应商原生" })[mode]
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

function harnessStatusLabel(status: string): string {
  return ({ reachable: "连接正常", unreachable: "连接失败", qualified: "验证通过",
    incompatible: "协议不兼容", available: "可用", not_configured: "尚未配置",
    protocol_mismatch: "协议不匹配", auth_failed: "API Key 验证失败",
    network_failed: "网络连接失败", rate_limit: "供应商限流", capacity: "供应商额度或容量不足",
    model_unsupported: "模型不受支持", response_incomplete: "响应未完成" } as Record<string, string>)[status] ?? status;
}

function harnessFailureLabel(reason: string): string {
  return ({ none: "无", not_configured: "尚未配置", authentication: "API Key 验证失败",
    network: "网络连接失败", rate_limit: "供应商限流", capacity: "供应商额度或容量不足，请检查账单或服务状态",
    model_not_found: "模型不存在", protocol_incompatible: "协议不兼容", context_limit: "输入超出上下文窗口", output_limit: "输出达到长度上限", paused: "模型暂停", refusal: "模型拒绝请求" } as Record<string, string>)[reason]
    ?? reason;
}

function timestampLabel(value: string): string {
  if (!value) return "未记录";
  const parsed = new Date(value);
  return Number.isNaN(parsed.valueOf()) ? value : parsed.toLocaleString();
}

export function V2ProviderSettings({ client, initialPreset, onExit, onSaved,
  prepareForDraft = false, onReady }: V2ProviderSettingsProps) {
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
      setDiscoveryError(`合并后共有 ${models.length} 个模型，超过 128 个上限。请减少勾选，或先从模型列表移除不需要的项；当前列表已保留。`);
      return false;
    }
    setDraft((current) => current ? draftWithModels(current, models.join("\n")) : current);
    setDiscoveryError(""); setError(""); setNotice("");
    setHarnessError(""); setDiagnostic(null); setQualification(null); setPreparedDefinition(null);
    return true;
  };

  const discoverModels = async () => {
    if (!draft || discoveryAbortRef.current || typeof client.discoverProviderModels !== "function") return;
    const advanced = parseAdvancedJSON(draft.advancedJSON);
    if (!advanced.value || !validProviderEndpointURL(draft.endpointURL.trim())) {
      setDiscoveryError(advanced.error ?? "请先填写有效的请求地址，再获取模型。");
      return;
    }
    const existing = draft.existing;
    const useStoredKey = !draft.apiKey && credentialByProvider.get(draft.id)?.configured;
    if (useStoredKey && (!existing || draft.endpointURL.trim() !== existing.endpoint_url || draft.transport !== existing.transport)) {
      setDiscoveryError("请求地址或协议已变更。请填写此地址的 API Key 后获取模型，或先保存新的连接配置。");
      return;
    }
    if (scanPlaintextSecrets(advanced.value, draft.id)?.count) {
      setDiscoveryError("高级 JSON 含明文密钥。请先移到 API Key 输入框，并修正凭据引用，再获取模型。");
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
      setDiscoveryError(reason instanceof Error ? reason.message : "无法获取模型列表。当前模型与配置已保留，可手动添加。");
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
        setHarnessError(`连接诊断未通过：${harnessFailureLabel(diagnosticResult.failure_reason)}。未继续执行 Harness 合成验证。`);
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
      setHarnessError(reason instanceof Error ? reason.message : "Harness 验证失败。请检查供应商配置与网络权限。");
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
    setNotice(`已保存 ${definition.display_name}，正在检查 ${definition.default_model}…`);
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
        setHarnessError(`模型检查未通过：${harnessFailureLabel(result.failure_reason)}。配置和草稿都已保留；修正配置后保存，或直接重试检查。`);
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
        setHarnessError("模型检查已通过，正在等待可用模型目录确认。配置和草稿已保留；请点击“重新检查”后继续使用这个模型。");
        return;
      }
      setNotice(`${definition.display_name} / ${definition.default_model} 已可用，正在返回原草稿。`);
      onReady?.(definition);
    } catch (reason) {
      if (!mountedRef.current || operationVersion !== harnessOperationVersionRef.current) return;
      setHarnessError(reason instanceof Error ? reason.message : "模型检查失败。配置和草稿已保留，请重试。");
    } finally {
      if (mountedRef.current && operationVersion === harnessOperationVersionRef.current) {
        harnessOperationRef.current = false;
        setHarnessBusy(false);
      }
    }
  };

  const persist = async (definition: ProviderDefinitionView, oneTimeSecret = "") => {
    if (!definitions.data) {
      setError("供应商定义尚未加载完成。");
      return;
    }
    if (oneTimeSecret && !client.hasProviderCredentials) {
      setError("系统凭据存储当前不可用，未保存任何明文密钥。");
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
            setError("供应商定义已保存，但 API Key 未写入系统凭据。请检查系统凭据管理器，在此重新输入密钥并再次保存。已保存的定义与高级 JSON 可继续编辑。");
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
      setNotice(`已保存 ${definition.display_name}`);
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
      setError(reason instanceof Error ? reason.message : "保存供应商失败。");
    } finally {
      setBusy(false);
    }
  };

  const save = () => {
    if (!draft || busy || harnessBusy) return;
    invalidateDiscovery();
    const parsed = definitionFromDraft(draft);
    if (!parsed.definition) {
      setAdvancedOpen(true);
      setError(parsed.error ?? "供应商配置无效。");
      return;
    }
    const scan = scanPlaintextSecrets(parsed.definition.advanced_config, parsed.definition.id);
    if (scan?.count && scan.count > 1) {
      setError("高级 JSON 中检测到多个不同的明文密钥。请分别写入系统凭据后，再用 $credential 引用替换。未保存配置。");
      return;
    }
    if (scan?.count === 1) {
      if (draft.apiKey && draft.apiKey !== scan.secret) {
        setError("API Key 输入框与高级 JSON 中的明文密钥不同。请只保留一个值，避免把错误凭据写入系统凭据库。");
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
      const synced = withSyncedCredentialReference(definition.advanced_config, draft);
      if (!synced.value) {
        setError(synced.error ?? "无法把凭据引用同步到高级 JSON。");
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
      setError("迁移密钥已失效，请重新输入。未保存配置。");
      return;
    }
    void persist(definition, secret);
  };

  const insertCredentialReference = () => {
    if (!draft) return;
    const providerID = draft.id.trim();
    if (!/^[a-z][a-z0-9_-]{0,63}$/u.test(providerID)) {
      setError("请先填写有效的供应商 ID，再插入凭据引用。");
      return;
    }
    try {
      const parsed = parseAdvancedJSON(draft.advancedJSON);
      if (!parsed.value) {
        setError(parsed.error ?? "请先修复高级 JSON，再插入凭据引用。");
        return;
      }
      const value = parsed.value;
      const synced = withSyncedCredentialReference(value, draft);
      if (!synced.value) {
        setError(synced.error ?? "无法插入凭据引用。");
        return;
      }
      update("advancedJSON", JSON.stringify(synced.value, null, 2));
      setNotice("已把当前供应商的 $credential 引用插入 request_headers。运行时通过该引用读取系统凭据。");
    } catch {
      setError("请先修复高级 JSON，再插入凭据引用。");
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
      setNotice("系统凭据已移除。供应商定义未改变。");
    } catch (reason) {
      setError(reason instanceof Error ? reason.message : "移除系统凭据失败。");
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
      setError(reason instanceof Error ? reason.message : "删除供应商失败。");
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
    return <><h1>自定义配置</h1><p className="v2-settings-lead">当前桌面后端未启用供应商定义控制。</p>
      <section className="v2-settings-card v2-provider-empty"><Server aria-hidden="true" size={24} />
        <strong>自定义供应商不可用</strong><p>连接具备控制权限与模型控制能力的服务后，可添加供应商并使用系统凭据库保存密钥。</p>
      </section></>;
  }

  if (initialPreset && !draft && initializedPresetIDRef.current !== initialPreset.id) {
    return <><div className="v2-provider-editor-heading">
      {onExit && <button aria-label="返回模型目录" onClick={onExit} type="button">
        <ArrowLeft aria-hidden="true" size={17} /></button>}
      <div><h1>连接 {initialPreset.displayName}</h1>
      <p>正在读取已有供应商定义与系统凭据状态。</p></div></div>
      {definitions.isError
        ? <section className="v2-settings-card v2-provider-empty" role="alert">
          <CircleAlert aria-hidden="true" size={22} /><strong>无法读取供应商</strong>
          <p>{definitions.error instanceof Error ? definitions.error.message : "请求失败"}</p>
          <button onClick={() => void definitions.refetch()} type="button">重试</button></section>
        : <section className="v2-settings-card v2-provider-empty"><p>正在读取供应商…</p></section>}
    </>;
  }

  if (!draft) {
    const providers = definitions.data?.providers ?? [];
    return <><div className="v2-provider-heading"><div><h1>自定义配置</h1>
      <p>在模型模块中连接兼容供应商；定义与系统凭据相互分离。</p></div>
      <button className="primary" disabled={definitions.isLoading || definitions.isError}
        onClick={(event) => openEditor(blankDraft(), event.currentTarget)} ref={addButtonRef} type="button">
        <Plus aria-hidden="true" size={15} />添加供应商</button></div>
      {notice && <p aria-live="polite" className="v2-provider-notice"><Check aria-hidden="true" size={15} />{notice}</p>}
      {definitions.isLoading && <div className="v2-settings-card v2-provider-empty"><p>正在读取供应商…</p></div>}
      {definitions.isError && <div className="v2-settings-card v2-provider-empty" role="alert">
        <CircleAlert aria-hidden="true" size={22} /><strong>无法读取供应商</strong>
        <p>{definitions.error instanceof Error ? definitions.error.message : "请求失败"}</p>
        <button onClick={() => void definitions.refetch()} type="button">重试</button></div>}
      {!definitions.isLoading && !definitions.isError && <section aria-label="自定义供应商" className="v2-provider-list">
        {providers.length === 0 && <div className="v2-settings-card v2-provider-empty"><Server aria-hidden="true" size={24} />
          <strong>还没有自定义供应商</strong><p>点击“添加供应商”，填写服务地址和模型，保存密钥后进行连接验证。API Key 保存在系统凭据库。</p></div>}
        {providers.map((provider) => {
          const credential = credentialByProvider.get(provider.id);
          return <button className="v2-provider-row" key={provider.id}
            onClick={(event) => openEditor(draftFromDefinition(provider), event.currentTarget)} type="button">
            <span className="v2-provider-mark"><Server aria-hidden="true" size={18} /></span>
            <span className="v2-provider-row-copy"><strong>{provider.display_name}</strong>
              <small>{provider.id} · {provider.default_model}</small></span>
            <span className="v2-provider-tags"><i className={provider.enabled ? "is-ready" : ""}>
              {provider.enabled ? "已启用" : "已停用"}</i>
              <i>{credential?.configured ? "密钥已存储" : permitsKeylessLocalProvider(provider)
                ? "本机 · 可不填密钥" : "无系统密钥"}</i>
              <i>{searchModeLabel(provider.search_mode)}</i></span>
            <ChevronRight aria-hidden="true" size={16} /></button>;
        })}
      </section>}
    </>;
  }

  const configuredCredential = credentialByProvider.get(draft.id)?.configured ?? false;
  const modelOptions = normalizeModels(draft.models);
  const quickSetup = Boolean(initialPreset);
  const showAdvanced = !quickSetup || advancedOpen;
  const modelField = <label>默认模型<select disabled={modelOptions.length === 0}
    onChange={(event) => update("defaultModel", event.target.value)}
    value={modelOptions.includes(draft.defaultModel) ? draft.defaultModel : ""}>
    <option value="">{modelOptions.length ? "选择默认模型" : "先填写模型列表"}</option>
    {modelOptions.map((model) => <option key={model} value={model}>{model}</option>)}</select></label>;
  const credentialField = <div className="v2-provider-credential-row"><label>API Key
    <input aria-describedby="provider-key-help" autoComplete="new-password"
      disabled={!client.hasProviderCredentials} onChange={(event) => update("apiKey", event.target.value)}
      placeholder={configuredCredential ? "留空以保留现有密钥" : "粘贴提供商的 API Key"}
      ref={quickSetup ? firstFieldRef : undefined} type="password" value={draft.apiKey} /></label>
    {configuredCredential && <button className="secondary" disabled={busy || !client.hasProviderCredentials}
      onClick={() => void removeCredential()} type="button"><KeyRound aria-hidden="true" size={14} />移除密钥</button>}</div>;
  const syncCredentialField = <label className="v2-provider-check v2-provider-sync"><input checked={draft.syncCredentialReference}
    onChange={(event) => update("syncCredentialReference", event.target.checked)} type="checkbox" />
    <span><strong>把凭据引用同步到高级 JSON</strong>
      <small>输入新 API Key 时，默认写入对应协议的 request_headers。这里只保存 $credential 引用；你仍可自由移动、改模板或关闭同步。</small></span></label>;
  const parsedDraftDefinition = definitionFromDraft(draft).definition;
  const parsedCapabilities = parseAdvancedJSON(draft.advancedJSON).value;
  const modelCapabilities = isRecord(parsedCapabilities?.model_capabilities) ? parsedCapabilities.model_capabilities : {};
  const capabilitiesError = parsedCapabilities ? modelCapabilitiesError(parsedCapabilities, modelOptions) : undefined;
  const policyControlsError = parsedCapabilities ? providerPolicyControlsError(parsedCapabilities) : undefined;
  const policyError = parsedCapabilities ? providerModelPolicyError(parsedCapabilities) : undefined;
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
  const harnessBlocker = !draft.existing ? "先保存供应商配置后才能验证。"
    : !savedConfiguration ? "当前更改尚未保存；请先保存，再验证已持久化的精确配置。"
      : !configuredCredential && !keylessLocal ? "请先把 API Key 保存到系统凭据管理器；只有未引用凭据的本机模型可不填密钥。"
        : !harnessControlAvailable ? "当前桌面启动未开放模型诊断与 Harness 验证。"
          : !draft.enabled ? "供应商已停用；启用并保存后才能验证。" : "";
  return <><div className="v2-provider-editor-heading">
    <button aria-label={initialPreset && onExit ? "返回模型目录" : "返回供应商列表"}
	  disabled={busy || harnessBusy} onClick={closeEditor} type="button">
      <ArrowLeft aria-hidden="true" size={17} /></button>
    <div><h1>{draft.existing ? "编辑供应商" : "添加供应商"}</h1>
      <p>{quickSetup ? "填入 API Key，确认模型并保存，然后完成下方模型检查。自建服务可展开高级设置。"
        : "填写服务地址和模型；密钥将单独保存到系统凭据管理器。"}</p></div></div>
    <form className="v2-provider-form" onSubmit={(event) => { event.preventDefault(); save(); }}>
      <fieldset className="v2-provider-operation-lock" disabled={harnessBusy}>
      {quickSetup && <section className="v2-settings-card v2-provider-fields v2-provider-quick" aria-labelledby="provider-quick-title">
        <header><div><h2 id="provider-quick-title">{draft.displayName}</h2>
          <p>提供商已选定；其他连接参数使用当前配置。</p></div>
          <span className={configuredCredential ? "v2-provider-status is-ready" : "v2-provider-status"}>
            {configuredCredential ? "密钥已存储" : "等待配置密钥"}</span></header>
        {credentialField}
        <p className="v2-provider-help" id="provider-key-help">{client.hasProviderCredentials
          ? "密钥保存在系统凭据管理器，保存后输入框会清空。已有密钥可留空保留；远端认证请通过下方连接验证检查。"
          : "使用支持系统凭据存储的桌面版本后，可在此保存密钥。"}</p>
        <div className="v2-provider-grid">{modelField}</div>
      </section>}
      {quickSetup && <button aria-expanded={advancedOpen} aria-controls="provider-advanced-connection"
        className="v2-model-advanced-toggle" onClick={() => setAdvancedOpen((open) => !open)} type="button">
        {advancedOpen ? "收起高级设置" : "高级设置：自定义连接、模型与搜索"}
      </button>}
      {showAdvanced && <section className="v2-settings-card v2-provider-fields" id="provider-advanced-connection" aria-labelledby="provider-basics-title">
        <header><div><h2 id="provider-basics-title">连接</h2><p>请求发送到你填写的模型地址；外部服务须使用 HTTPS，本机回环地址可使用 HTTP。</p></div>
          <label className="v2-provider-enabled"><input checked={draft.enabled}
            onChange={(event) => update("enabled", event.target.checked)} type="checkbox" />
            <span aria-hidden="true"><i /></span>启用</label></header>
        <div className="v2-provider-grid">
          <label>供应商 ID<input autoComplete="off" disabled={Boolean(draft.existing)}
            maxLength={64} onChange={(event) => update("id", event.target.value)}
            placeholder="例如 acme-ai" ref={!quickSetup ? firstFieldRef : undefined} required spellCheck={false}
            value={draft.id} /></label>
          <label>显示名称<input autoComplete="organization" maxLength={128}
            onChange={(event) => update("displayName", event.target.value)}
            placeholder="例如 Acme AI" ref={!quickSetup && draft.existing ? firstFieldRef : undefined}
            required value={draft.displayName} /></label>
          <label className="is-wide">备注<input maxLength={2048}
            onChange={(event) => update("note", event.target.value)}
            placeholder="可选，例如团队账号" value={draft.note} /></label>
          <label className="is-wide">官网链接<input inputMode="url"
            onChange={(event) => update("websiteURL", event.target.value)}
            placeholder="https://example.com（可选）" type="url" value={draft.websiteURL} /></label>
          <label className="is-wide">请求地址<input inputMode="url"
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
          <label>协议<select onChange={(event) => {
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
        <header><div><h2 id="provider-models-title">可用模型</h2><p>使用当前 API Key 从服务获取模型，或手动补充。获取后选择要加入的模型，保存时生效。</p></div>
          <button className="secondary" disabled={busy || discoveryBusy || typeof client.discoverProviderModels !== "function"}
            onClick={() => void discoverModels()} type="button">
            {discoveryBusy && <LoaderCircle aria-hidden="true" className="spin" size={14} />}
            {discoveryBusy ? "正在获取模型…" : "获取模型列表"}</button></header>
        <div className="v2-provider-grid">
          <div className="v2-provider-model-add is-wide"><label>手动添加模型<input autoComplete="off"
            onChange={(event) => setManualModel(event.target.value)} placeholder="输入完整模型名称" spellCheck={false} value={manualModel}
            onKeyDown={(event) => {
              if (event.key === "Enter") {
                event.preventDefault();
                if (manualModel.trim() && mergeModels(normalizeModels(manualModel))) setManualModel("");
              }
            }} /></label><button disabled={!manualModel.trim() || busy}
            onClick={() => { if (mergeModels(normalizeModels(manualModel))) setManualModel(""); }} type="button">
            <Plus aria-hidden="true" size={14} />添加模型</button></div>
          <label className="is-wide">模型列表<textarea aria-describedby="provider-models-help"
            aria-label="模型列表"
            onChange={(event) => {
              const models = event.target.value;
              setDraft((current) => current ? draftWithModels(current, models) : current);
              setError(""); setNotice(""); setHarnessError(""); setDiagnostic(null); setQualification(null); setPreparedDefinition(null);
            }}
            placeholder={"model-a\nmodel-b"} rows={3} spellCheck={false} value={draft.models} />
            <small id="provider-models-help">每行或逗号分隔，最多 128 个（当前 {modelOptions.length} 个）。名称按 model_mapping 映射后发送；移除模型会清除对应图片声明，新名称需重新确认。</small></label>
          {typeof client.discoverProviderModels !== "function" && <p className="v2-provider-field-help is-wide">当前服务提供手动添加：填写完整模型名称，保存后进行连接验证。</p>}
          {discoveryError && <p className="v2-inline-error is-wide" role="alert">{discoveryError}</p>}
          {discoveredModels && <div aria-label="获取到的模型" className="v2-provider-discovery is-wide">
            <p aria-live="polite">服务返回 {discoveredModels.length} 个模型。勾选后加入当前列表，保存后继续完成连接与 Harness 验证。</p>
            {discoveryTruncated && <p>服务模型列表较长，本次结果未完整返回。可从已返回结果中选择，也可手动添加完整模型名。</p>}
            {discoveredModels.length === 0 && <p>服务没有返回模型。当前列表已保留，可手动添加模型名。</p>}
            <div className="v2-provider-discovery-options">{discoveredModels.map((model) => {
              const added = modelOptions.includes(model.id);
              return <label className="v2-provider-check" key={model.id}><input type="checkbox"
                aria-label={`选择模型 ${model.id}`} checked={added || selectedDiscoveredModels.includes(model.id)} disabled={added}
                onChange={(event) => setSelectedDiscoveredModels((current) => event.target.checked ? [...current, model.id] : current.filter((id) => id !== model.id))} />
                <span><strong>{model.id}{added ? " · 已添加" : ""}</strong>
                  {model.display_name && model.display_name !== model.id && <small>{model.display_name}</small>}
                  {(model.input_token_limit !== undefined || model.output_token_limit !== undefined) && <small>服务返回的限制：
                    {model.input_token_limit !== undefined && `输入 ${model.input_token_limit.toLocaleString()} token`}
                    {model.input_token_limit !== undefined && model.output_token_limit !== undefined && " · "}
                    {model.output_token_limit !== undefined && `输出 ${model.output_token_limit.toLocaleString()} token`}。未自动写入本地策略。</small>}
                </span></label>;
            })}</div>
            {discoveredModels.length > 0 && <button disabled={!selectedDiscoveredModels.some((model) => !modelOptions.includes(model))}
              onClick={() => { if (mergeModels(selectedDiscoveredModels)) setSelectedDiscoveredModels([]); }} type="button">添加所选模型</button>}
          </div>}
        </div>
      </section>}

      {showAdvanced && <section className="v2-settings-card v2-provider-fields" aria-labelledby="provider-output-title">
        <header><div><h2 id="provider-output-title">模型输出限制</h2>
          <p>每个模型可继承默认策略，或自定义默认输出与单次上限。输出 Token 预算包含推理内容和可见文字，分配方式由模型服务决定。</p></div></header>
        <div className="v2-provider-grid">
          {!modelOptions.length && <p className="v2-provider-field-help is-wide">先填写模型列表。</p>}
          {!parsedCapabilities && <p className="v2-provider-field-help is-wide">先修正下方高级 JSON，再设置输出限制。</p>}
          {policyControlsError && <p className="v2-provider-field-help is-wide" role="alert">{policyControlsError}</p>}
          {policyError && <p className="v2-provider-field-help is-wide" role="alert">{policyError}</p>}
          {policyGroups.map(({ wireModel, aliases }) => {
            const inherited = inheritedProviderModelPolicy(draft.endpointURL, draft.transport, wireModel, parsedCapabilities ?? {});
            const custom = Object.hasOwn(modelPolicies, wireModel);
            const rawPolicy = modelPolicies[wireModel];
            const invalidShape = custom && !policyShape(rawPolicy);
            const policy = custom && policyShape(rawPolicy) ? rawPolicy : inherited.policy;
            const disabled = !parsedCapabilities || Boolean(policyControlsError) || invalidShape;
            const source = custom ? "用户覆盖" : inherited.source === "known" ? "继承默认 · 已知模型的本地规划策略" : "继承默认 · 本地保守回退（服务端实际限制未知）";
            return <article aria-label={`${wireModel} 输出策略`} className="v2-provider-policy is-wide" key={wireModel}>
              <div className="v2-provider-policy-heading"><strong>{aliases.join(" / ")}</strong><span>{source}</span></div>
              {(aliases.length > 1 || aliases[0] !== wireModel) && <p>实际发送模型：{wireModel}。映射到此模型的名称共享以下设置。</p>}
              {!custom && inherited.source === "fallback" && <p>这里设置本地预算；服务端实际限制请向供应商确认。带工具的请求会按此上限预留输出空间。</p>}
              {!custom && inherited.source === "known" && <p>{inherited.explicitDefault
                ? "默认输出 16,384 token 是应用发送给服务的单次请求限制；128,000 token 上限来自官方模型资料，可在此自定义。"
                : "容量依据已知模型资料；默认输出是应用的本地预算，可按任务需要调整。"}</p>}
              <label className="v2-provider-check"><input aria-label={`${wireModel} 自定义输出限制`} type="checkbox" checked={custom} disabled={disabled}
                onChange={(event) => changePolicy(wireModel, event.target.checked ? inherited.policy : null)} />自定义此模型的输出限制</label>
              <div className="v2-provider-policy-fields">{([
                ["default_output_tokens", "默认输出 token", 1, 1000000],
                ["max_output_tokens", "单次输出上限 token", 1, 1000000],
                ["window_tokens", "上下文窗口 token", 4096, 2097152],
              ] as const).map(([field, label, min, max]) => <label key={field}>{label}<input type="number" inputMode="numeric"
                aria-label={`${wireModel} ${label}`} min={min} max={max} step={1} disabled={disabled || !custom} value={policy[field] || ""}
                onChange={(event) => {
                  const value = Number(event.target.value);
                  changePolicy(wireModel, { ...policy, [field]: Number.isFinite(value) ? value : 0 });
                }} /></label>)}</div>
              <p>默认输出 ≤ 单次上限；单次上限须小于上下文窗口减去 1024 token，且不超过当前适配器的 1,000,000 token。</p>
            </article>;
          })}
        </div>
      </section>}

      {showAdvanced && <section className="v2-settings-card v2-provider-fields" aria-labelledby="provider-images-title">
        <header><div><h2 id="provider-images-title">图片输入</h2><p>按供应商说明选择图片能力声明，再在任务中用图片检查实际效果。原始图片会发送到上方的请求地址，请确认该端点适合接收这些内容。</p></div></header>
        <div className="v2-provider-grid">{modelOptions.map((model) => {
          const capability = modelCapabilities[model];
          const state = isRecord(capability) && ["supported", "unsupported", "unknown"].includes(String(capability.vision)) ? String(capability.vision) : "unknown";
          return <label key={model}>{model}<select aria-label={`${model} 图片输入`} disabled={!parsedCapabilities || Boolean(capabilitiesError)}
            value={state} onChange={(event) => {
              if (!parsedCapabilities || capabilitiesError) return;
              update("advancedJSON", JSON.stringify({ ...parsedCapabilities,
                model_capabilities: { ...modelCapabilities, [model]: { vision: event.target.value } } }, null, 2));
            }}><option value="unknown">尚未确认</option><option value="supported">支持图片</option><option value="unsupported">不支持图片</option></select></label>;
        })}
        {!modelOptions.length && <p className="v2-provider-field-help is-wide">先填写模型列表。</p>}
        {!parsedCapabilities && <p className="v2-provider-field-help is-wide">先修正下方高级 JSON，再设置图片能力。</p>}
        {capabilitiesError && <p className="v2-provider-field-help is-wide">{capabilitiesError}</p>}
        </div>
      </section>}

      {showAdvanced && <section className="v2-settings-card v2-provider-fields" aria-labelledby="provider-search-title">
        <header><div><h2 id="provider-search-title">网页搜索</h2>
          <p>先确认供应商提供哪种搜索能力，再选择搜索方式。原生搜索会在首次真实调用前完成有界资格验证。</p></div></header>
        <div className="v2-provider-grid">
          <label className="is-wide">搜索策略<select aria-label="搜索策略" onChange={(event) => {
            const mode = event.target.value as ProviderDraft["searchMode"];
            setDraft((current) => current ? { ...current, searchMode: mode,
              nativeSearchDeclared: mode === "provider_native" || current.nativeSearchDeclared } : current);
            setError("");
            setNotice("");
            setHarnessError("");
            setDiagnostic(null);
            setQualification(null);
          }} value={draft.searchMode}>
            <option value="disabled">关闭</option><option value="auto">自动选择</option>
            <option value="web">普通网页搜索（DuckDuckGo）</option>
            <option value="searxng">SearXNG</option>
            <option disabled={knownNativeSearchUnsupported(draft.endpointURL)} value="provider_native">供应商原生</option>
          </select><small>{draft.searchMode === "web"
            ? "搜索查询会发送到 DuckDuckGo，可直接用于当前模型。搜索与网页读取遵循当前任务的网页访问范围。"
            : draft.searchMode === "auto"
              ? "自动优先使用已声明并验证的供应商原生搜索，随后可回退到已配置的 SearXNG。要使用 DuckDuckGo，请选择“普通网页搜索”。"
              : draft.searchMode === "searxng"
                ? "先在 Desktop 启动配置中填写 SearXNG 地址。查询使用该地址，并遵循当前任务的网页访问范围。"
                : draft.searchMode === "disabled"
                  ? "此供应商的网页搜索已关闭。任务的网页访问范围可在任务设置中管理。"
                  : "选择供应商原生时会绑定当前模型、端点、定义 revision 与系统凭据代际。"}</small></label>
          {knownNativeSearchUnsupported(draft.endpointURL) && <p className="is-wide">
            {draft.existing && draft.searchMode === "provider_native"
              ? "此官方 DeepSeek 旧配置仍选择了原生搜索，需要更换搜索方式。请选择“普通网页搜索”，或配置 SearXNG 后选择“自动选择”。"
              : "此官方 DeepSeek 地址可使用“普通网页搜索”。也可配置 SearXNG 后选择“自动选择”。"}
          </p>}
          <label className="v2-provider-check is-wide"><input aria-label="声明供应商具备原生 Web Search"
            checked={draft.nativeSearchDeclared}
            disabled={knownNativeSearchUnsupported(draft.endpointURL)}
            onChange={(event) => update("nativeSearchDeclared", event.target.checked)} type="checkbox" />
            <span><strong>声明供应商具备原生 Web Search</strong>
              <small>勾选后会登记原生搜索能力声明。首次真实搜索需完成有界验证，可能产生 API 调用费用；搜索使用当前供应商端点的授权范围，网页抓取遵循任务权限。</small></span></label>
        </div>
      </section>}

      {!quickSetup && <section className="v2-settings-card v2-provider-fields" aria-labelledby="provider-credential-title">
        <header><div><h2 id="provider-credential-title">系统凭据</h2>
          <p>密钥保存在操作系统凭据管理器。此页读取存储状态，通过连接验证检查远端认证。</p></div>
          <span className={configuredCredential ? "v2-provider-status is-ready" : "v2-provider-status"}>
            {configuredCredential ? "已存储" : "未存储"}</span></header>
        {credentialField}
        {syncCredentialField}
        <p id="provider-key-help" className="v2-provider-help">{keylessLocal
          ? "本机模型未引用凭据，可留空并进行连接验证；若服务要求认证，请填写其真实 API Key。"
          : <>保存后输入框会立即清空。高级 JSON 应使用 <code>$credential</code> 引用。</>}</p>
      </section>}

      <section className="v2-settings-card v2-provider-fields v2-provider-harness"
        aria-labelledby="provider-harness-title">
        <header><div><h2 id="provider-harness-title">{prepareForDraft ? "首次模型检查" : "连接与 Harness 验证"}</h2>
          <p>{prepareForDraft
            ? "保存后自动检查工具调用、结果回传与结构化 JSON；通过后返回原草稿并选中这个模型。检查通常会产生两次模型调用。"
            : "先做一次最小连接诊断；通过后再验证工具调用、结果回传与结构化 JSON 合同。"}</p></div>
          {!prepareForDraft && <button className="secondary" disabled={Boolean(harnessBlocker) || busy || harnessBusy}
            onClick={() => setHarnessConfirmOpen(true)} ref={harnessButtonRef} type="button">
            {harnessBusy ? <LoaderCircle aria-hidden="true" className="spin" size={14} />
              : <ShieldCheck aria-hidden="true" size={14} />}
            {harnessBusy ? "正在验证…" : "测试并验证 Harness"}</button>}
          {prepareForDraft && preparedDefinition && harnessError && <button className="secondary"
            disabled={busy || harnessBusy} onClick={() => void prepareSavedProvider(preparedDefinition)}
            ref={harnessButtonRef} type="button"><ShieldCheck aria-hidden="true" size={14} />重新检查</button>}</header>
        {!prepareForDraft && harnessBlocker && <p className="v2-provider-help">{harnessBlocker}</p>}
        {!harnessBlocker && !diagnostic && !qualification && !harnessBusy &&
          <p className="v2-provider-help">验证绑定当前供应商、默认模型、端点、定义 revision 与系统凭据代际；任一项变化后需重新验证。</p>}
        {harnessBusy && <p aria-live="polite" className="v2-provider-harness-progress">
          <Activity aria-hidden="true" size={14} />正在执行有界供应商调用，请勿关闭应用…</p>}
        {diagnostic && <article aria-label="连接诊断结果" className="v2-provider-harness-result" role="status">
          <div><strong>连接诊断</strong><span className={diagnostic.status === "reachable" ? "is-ready" : "is-error"}>
            {harnessStatusLabel(diagnostic.status)}</span></div>
          <dl><div><dt>模型</dt><dd>{diagnostic.provider} / {diagnostic.model}</dd></div>
            <div><dt>结果</dt><dd>{diagnostic.failure_reason === "none"
              ? harnessStatusLabel(diagnostic.qualification_status) : harnessFailureLabel(diagnostic.failure_reason)}</dd></div>
            <div><dt>耗时</dt><dd>{diagnostic.duration_ms} ms</dd></div></dl>
        </article>}
        {qualification && <article aria-label="Harness 验证结果" className="v2-provider-harness-result" role="status">
          <div><strong>Harness 验证</strong><span className={qualification.status === "qualified" ? "is-ready" : "is-error"}>
            {harnessStatusLabel(qualification.status)}</span></div>
          <dl><div><dt>资格状态</dt><dd>{harnessStatusLabel(qualification.qualification_status)}</dd></div>
            <div><dt>模型调用</dt><dd>{qualification.model_calls} 次</dd></div>
            <div><dt>验证时间</dt><dd>{timestampLabel(qualification.harness.qualified_at)}</dd></div>
            <div><dt>有效期至</dt><dd>{timestampLabel(qualification.harness.expires_at)}</dd></div></dl>
        </article>}
        {harnessError && <p className="v2-inline-error v2-provider-harness-error" role="alert">
          <CircleAlert aria-hidden="true" size={15} />{harnessError}</p>}
      </section>

      {showAdvanced && <section className="v2-settings-card v2-provider-fields" aria-labelledby="provider-json-title">
        <header><div><h2 id="provider-json-title">高级 JSON</h2>
          <p>在这里编辑 request_headers、request_body 与 model_mapping；其他扩展会原样保留。Harness 核心字段由运行时管理。</p></div>
          <button className="secondary" onClick={insertCredentialReference} type="button">
            <KeyRound aria-hidden="true" size={14} />插入凭据引用</button></header>
        <label className="v2-provider-json">
          <textarea aria-label="高级 JSON" onChange={(event) => update("advancedJSON", event.target.value)}
            spellCheck={false} value={draft.advancedJSON} /></label>
        {quickSetup && syncCredentialField}
        <p className="v2-provider-help">示例：<code>{'{"request_headers":{"Authorization":{"$credential":"' + (draft.id || "provider-id") + '","template":"Bearer ${secret}"}}}'}</code>。如粘贴一个明文密钥，保存时会先要求二次确认并迁移。</p>
      </section>}

      {error && <p className="v2-inline-error v2-provider-error" role="alert">
        <CircleAlert aria-hidden="true" size={16} />{error}</p>}
      {notice && <p aria-live="polite" className="v2-provider-notice"><Check aria-hidden="true" size={15} />{notice}</p>}
      <footer className="v2-provider-actions">
        {draft.existing && <button className="danger-quiet" disabled={busy}
          onClick={() => setDeleteOpen(true)} ref={deleteButtonRef} type="button">
          <Trash2 aria-hidden="true" size={15} />删除供应商</button>}
        <span />
        <button disabled={busy} onClick={closeEditor} type="button">取消</button>
        <button className="primary" disabled={busy} ref={saveButtonRef} type="submit">
          <Save aria-hidden="true" size={15} />{busy ? "正在保存…" : prepareForDraft ? "保存并检查" : "保存"}</button>
      </footer>
      </fieldset>
    </form>

    <V2ConfirmDialog busy={busy} confirmLabel="删除" danger
      description={configuredCredential
        ? "确认供应商版本与路由引用允许删除后，将一并移除供应商定义和系统凭据。仍被路由引用或版本已更新时，原凭据会保留。"
        : "供应商定义将从可用模型列表中移除。仍有路由引用它时，请先更换这些路由的模型，再重试删除。"}
      onCancel={() => setDeleteOpen(false)} onConfirm={() => void removeDefinition()}
      open={deleteOpen} returnFocusRef={deleteButtonRef} title={`删除 ${draft.displayName || draft.id}`} />
    <V2ConfirmDialog busy={busy} confirmLabel="迁移并保存" danger
      description="高级 JSON 中检测到一个明文密钥。确认后，密钥会移入操作系统凭据管理器，JSON 中对应位置会替换为当前供应商的 $credential 引用。"
      onCancel={() => { migrationSecretRef.current = ""; setMigration(null); }}
      onConfirm={confirmMigration} open={Boolean(migration)} returnFocusRef={saveButtonRef}
      title="迁移明文密钥？" />
    <V2ConfirmDialog busy={harnessBusy} confirmLabel="开始验证"
      description={`将对 ${draft.displayName || draft.id} 的默认模型 ${draft.defaultModel || "未选择"} 发起一次最小连接诊断；诊断通过后会继续执行 Harness 合成验证，当前协议通常还需要两次模型调用。供应商可能按这些调用计费，且请求会发送到已保存的端点。`}
      onCancel={() => setHarnessConfirmOpen(false)} onConfirm={() => void verifyHarness()}
      open={harnessConfirmOpen} returnFocusRef={harnessButtonRef}
      title="测试并验证 Harness？" />
  </>;
}
import "./settings.css";
