import { validBuiltinComputerUseConnection } from "../../computer-use";
import { validMCPConnectionConfig } from "../../mcp-config";
import { APIRequestError } from "../protocol";
import {
  boundedIdentity,
  boundedStringArray,
  boundedText,
  hasExactKeys,
  hasOnlyKeys,
  hasRequiredOnlyKeys,
  isRecord,
  isSHA256,
  safeBoundedCount,
  safePositiveInteger,
  validDate,
  validWorkspaceRelativePath,
} from "../validation";
import type {
  CodeIntelConfigurationTestRequestView,
  CodeIntelConfigurationTestView,
  CodeIntelConfigurationView,
  CodeIntelInventoryView,
  ExtensionInventoryView,
  ExtensionMCPRegistrationRequestView,
  ExtensionMCPRegistrationView,
  ExtensionMCPServerView,
  ExtensionPluginImportRequestView,
  ExtensionPluginImportView,
  ExtensionPluginInstallationView,
  HookDiagnosticsView,
  PluginHistoryView,
  PluginPublisherTrustView,
} from "../../types";

export function containsForbiddenExtensionField(value: unknown): boolean {
  if (Array.isArray(value)) return value.some(containsForbiddenExtensionField);
  if (!isRecord(value)) return false;
  const forbidden = new Set(["credential", "credential_value", "secret", "token",
    "authorization", "arguments", "result", "publisher_public_key", "archive_bytes", "raw"]);
  return Object.entries(value).some(([key, child]) =>
    forbidden.has(key.toLocaleLowerCase()) ||
    (!(value.protocol_version === "mcp-client-server.v1" && ["env", "env_credential_refs", "headers"].includes(key)) &&
      !(key === "prompt_details" && validExtensionPromptDetails(value)) && containsForbiddenExtensionField(child)));
}

export const codeIntelTools = ["code_workspace_symbols", "code_document_symbols",
  "code_definition", "code_references", "code_implementation", "code_hover",
  "code_signature_help", "code_diagnostics", "code_call_hierarchy",
  "code_type_hierarchy"] as const;

export const codeIntelCapabilityKeys = ["workspace_symbols", "document_symbols", "definition",
  "references", "implementation", "hover", "signature_help", "diagnostics",
  "call_hierarchy", "type_hierarchy"] as const;

export const codeIntelHealthStates = ["configured", "starting", "healthy", "unavailable",
  "crashed", "timed_out", "protocol_error", "stopped"];

export function strictCodeIntelIdentity(value: unknown): boolean {
  return boundedIdentity(value) !== "" &&
    !/[\s\u0000-\u001f\u007f]/u.test(String(value));
}

export function containsForbiddenCodeIntelField(value: unknown): boolean {
  if (Array.isArray(value)) return value.some(containsForbiddenCodeIntelField);
  if (!isRecord(value)) return false;
  const forbidden = new Set(["argument", "arguments", "argv", "command", "credential",
    "credentials", "environment", "executable", "home", "password", "raw", "secret",
    "stderr", "stdout", "token"]);
  return Object.entries(value).some(([key, child]) =>
    forbidden.has(key.toLocaleLowerCase()) || containsForbiddenCodeIntelField(child));
}

export function parseCodeIntelInventory(value: unknown): CodeIntelInventoryView {
  if (!isRecord(value) || !hasOnlyKeys(value, ["enabled", "protocol_version", "qualifications", "servers", "configurations"]) ||
    ["enabled", "protocol_version", "qualifications", "servers"].some((key) => !Object.hasOwn(value, key)) ||
    value.protocol_version !== "code-intel-lsp.v1" || typeof value.enabled !== "boolean" ||
    containsForbiddenCodeIntelField(value) || !Array.isArray(value.servers) ||
    value.servers.length > 32 || !Array.isArray(value.qualifications) ||
    value.qualifications.length > 32) {
    throw new APIRequestError("Code intelligence inventory is invalid", "INVALID_RESPONSE", 502);
  }
  if (value.configurations !== undefined) {
    if (!Array.isArray(value.configurations) || value.configurations.length > 64) {
      throw new APIRequestError("Code intelligence configurations are invalid", "INVALID_RESPONSE", 502);
    }
    const identities = new Set<string>();
    for (const item of value.configurations) {
      const configuration = parseCodeIntelConfiguration(item);
      const identity = `${configuration.workspace_id}/${configuration.server_id}/${configuration.review_state}/${configuration.descriptor_fingerprint}`;
      if (identities.has(identity)) throw new APIRequestError("Code intelligence configurations repeat a server", "INVALID_RESPONSE", 502);
      identities.add(identity);
    }
  }
  const serverIdentities = new Set<string>();
  for (const server of value.servers) {
    const required = ["capabilities", "credentials_granted", "descriptor_fingerprint",
      "health", "languages", "model_visible_tools", "network_access_granted",
      "process_owned", "protocol_version", "read_only", "server_id", "server_name",
      "shell_profile_loaded", "source_kind", "source_label", "source_sha256",
      "workspace_id"];
    const optional = ["capability_fingerprint", "generation", "last_error", "qualified_at",
      "server_version"];
    const capabilities = isRecord(server) && isRecord(server.capabilities)
      ? server.capabilities : null;
    if (!isRecord(server) || !hasOnlyKeys(server, [...required, ...optional]) ||
      required.some((key) => !Object.prototype.hasOwnProperty.call(server, key)) ||
      server.protocol_version !== "code-intel-lsp.v1" ||
      !strictCodeIntelIdentity(server.server_id) || !strictCodeIntelIdentity(server.workspace_id) ||
      !boundedText(server.server_name, 256) || server.source_kind !== "operator_config" ||
      !boundedText(server.source_label, 256) || !isSHA256(server.source_sha256) ||
      !isSHA256(server.descriptor_fingerprint) ||
      !codeIntelHealthStates.includes(String(server.health)) ||
      server.process_owned !== true || server.read_only !== true ||
      server.network_access_granted !== false || server.credentials_granted !== false ||
      server.shell_profile_loaded !== false || !Array.isArray(server.languages) ||
      server.languages.length < 1 || server.languages.length > 16 ||
      server.languages.some((language) => !strictCodeIntelIdentity(language)) ||
      new Set(server.languages).size !== server.languages.length ||
      server.languages.join("\u0000") !== [...server.languages].sort().join("\u0000") ||
      capabilities === null || !hasExactKeys(capabilities, [...codeIntelCapabilityKeys]) ||
      codeIntelCapabilityKeys.some((key) => typeof capabilities[key] !== "boolean") ||
      !boundedStringArray(server.model_visible_tools, codeIntelTools.length, 64) ||
      new Set(server.model_visible_tools).size !== server.model_visible_tools.length ||
      server.model_visible_tools.some((tool) => !codeIntelTools.includes(
        tool as typeof codeIntelTools[number])) ||
      (server.capability_fingerprint !== undefined &&
        !isSHA256(server.capability_fingerprint)) ||
      (server.generation !== undefined && !isSHA256(server.generation)) ||
      (server.last_error !== undefined && !boundedText(server.last_error, 2_048)) ||
      (server.server_version !== undefined && !boundedText(server.server_version, 256)) ||
      (server.qualified_at !== undefined && !validDate(server.qualified_at)) ||
      (server.health === "healthy" && (!isSHA256(server.capability_fingerprint) ||
        !isSHA256(server.generation) || !validDate(server.qualified_at)))) {
      throw new APIRequestError("Code intelligence server projection is invalid",
        "INVALID_RESPONSE", 502);
    }
    const expectedTools = codeIntelCapabilityKeys.flatMap((key, index) =>
      capabilities[key] ? [codeIntelTools[index]] : []);
    if (expectedTools.join("\u0000") !== server.model_visible_tools.join("\u0000")) {
      throw new APIRequestError("Code intelligence tools do not match negotiated capabilities",
        "INVALID_RESPONSE", 502);
    }
    const identity = `${String(server.workspace_id)}\u0000${String(server.server_id)}`;
    if (serverIdentities.has(identity)) {
      throw new APIRequestError("Code intelligence inventory repeats a server",
        "INVALID_RESPONSE", 502);
    }
    serverIdentities.add(identity);
  }
  const qualificationIdentities = new Set<string>();
  for (const qualification of value.qualifications) {
    const required = ["credentials_granted", "descriptor_fingerprint", "eligible",
      "executable_hash_matched", "health", "minimal_environment", "network_access_granted",
      "process_owned", "protocol_version", "reviewed", "server_id", "shell_profile_loaded",
      "workspace_id"];
    if (!isRecord(qualification) || !hasOnlyKeys(qualification, [...required, "reason"]) ||
      required.some((key) => !Object.prototype.hasOwnProperty.call(qualification, key)) ||
      qualification.protocol_version !== "code-intel-lsp.v1" ||
      !strictCodeIntelIdentity(qualification.server_id) ||
      !strictCodeIntelIdentity(qualification.workspace_id) ||
      !isSHA256(qualification.descriptor_fingerprint) ||
      !codeIntelHealthStates.includes(String(qualification.health)) ||
      typeof qualification.eligible !== "boolean" ||
      typeof qualification.executable_hash_matched !== "boolean" ||
      typeof qualification.reviewed !== "boolean" || qualification.process_owned !== true ||
      qualification.minimal_environment !== true ||
      qualification.network_access_granted !== false ||
      qualification.credentials_granted !== false ||
      qualification.shell_profile_loaded !== false ||
      (qualification.reason !== undefined && !boundedText(qualification.reason, 2_048)) ||
      (qualification.eligible && (!qualification.executable_hash_matched ||
        !qualification.reviewed || qualification.health !== "configured" ||
        qualification.reason !== undefined))) {
      throw new APIRequestError("Code intelligence qualification is invalid",
        "INVALID_RESPONSE", 502);
    }
    const identity = `${String(qualification.workspace_id)}\u0000${String(qualification.server_id)}`;
    if (qualificationIdentities.has(identity)) {
      throw new APIRequestError("Code intelligence inventory repeats a qualification",
        "INVALID_RESPONSE", 502);
    }
    qualificationIdentities.add(identity);
  }
  return value as unknown as CodeIntelInventoryView;
}

export function parseCodeIntelConfiguration(value: unknown): CodeIntelConfigurationView {
  const required = ["protocol_version", "server_id", "server_name", "workspace_id", "scope", "languages",
    "executable_sha256", "descriptor_fingerprint", "review_state", "source_kind", "source_label", "source_sha256"];
  if (!isRecord(value) || !hasOnlyKeys(value, [...required, "reviewed_by", "reviewed_at"]) ||
    required.some((key) => !Object.hasOwn(value, key)) || containsForbiddenCodeIntelField(value) ||
    value.protocol_version !== "code-intel-configuration.v1" || !strictCodeIntelIdentity(value.server_id) ||
    !strictCodeIntelIdentity(value.workspace_id) || !boundedText(value.server_name, 256) || value.scope !== "workspace" ||
    !isSHA256(value.executable_sha256) || !isSHA256(value.descriptor_fingerprint) || !isSHA256(value.source_sha256) ||
    value.source_kind !== "operator_config" || !boundedText(value.source_label, 256) ||
    !["pending_review", "reviewed"].includes(String(value.review_state)) ||
    !Array.isArray(value.languages) || value.languages.length === 0 || value.languages.length > 16 ||
    value.languages.some((language) => !hasExactKeys(language, ["id", "extensions"]) ||
      !strictCodeIntelIdentity(language.id) || !boundedStringArray(language.extensions, 32, 32) ||
      language.extensions.length === 0 || language.extensions.some((extension) => !/^\.[a-z0-9_.+-]+$/u.test(extension))) ||
    (value.reviewed_by !== undefined && !boundedText(value.reviewed_by, 256)) ||
    (value.reviewed_at !== undefined && !validDate(value.reviewed_at)) ||
    (value.review_state === "reviewed" && (!boundedText(value.reviewed_by, 256) || !validDate(value.reviewed_at)))) {
    throw new APIRequestError("Code intelligence configuration projection is invalid", "INVALID_RESPONSE", 502);
  }
  return value as unknown as CodeIntelConfigurationView;
}

export function parseCodeIntelConfigurationTest(value: unknown, serverID: string,
  request: CodeIntelConfigurationTestRequestView): CodeIntelConfigurationTestView {
  if (!hasExactKeys(value, ["protocol_version", "configuration", "server", "result"]) ||
    value.protocol_version !== "code-intel-configuration.v1" || containsForbiddenCodeIntelField(value)) {
    throw new APIRequestError("Code intelligence test response is invalid", "INVALID_RESPONSE", 502);
  }
  const configuration = parseCodeIntelConfiguration(value.configuration);
  const server = parseCodeIntelInventory({ enabled: true, protocol_version: "code-intel-lsp.v1", servers: [value.server], qualifications: [] }).servers[0];
  const result = value.result;
  const required = ["protocol_version", "tool", "state", "evidence_level", "workspace_id", "server_id",
    "server_generation", "capability_fingerprint", "query_fingerprint", "items", "page", "warnings"];
  const invalid = () => new APIRequestError("Code intelligence test evidence is invalid", "INVALID_RESPONSE", 502);
  if (!isRecord(result) || !hasOnlyKeys(result, [...required, "document_path", "document_sha256"]) ||
    required.some((key) => !Object.hasOwn(result, key)) || result.protocol_version !== "code-intel-lsp.v1" ||
    result.tool !== request.tool || !["current", "partial"].includes(String(result.state)) ||
    result.evidence_level !== "semantic_language_server" || result.workspace_id !== request.workspace_id ||
    result.server_id !== serverID || configuration.server_id !== serverID || server.server_id !== serverID ||
    configuration.workspace_id !== request.workspace_id || server.workspace_id !== request.workspace_id ||
    configuration.review_state !== "reviewed" || configuration.descriptor_fingerprint !== request.expected_descriptor_fingerprint ||
    server.descriptor_fingerprint !== configuration.descriptor_fingerprint ||
    result.server_generation !== server.generation || result.capability_fingerprint !== server.capability_fingerprint ||
    !isSHA256(result.server_generation) || !isSHA256(result.capability_fingerprint) || !isSHA256(result.query_fingerprint) ||
    !boundedStringArray(result.warnings, 32, 2048) || !Array.isArray(result.items) || result.items.length > 200 ||
    !hasExactKeys(result.page, ["limit", "returned", "total", "truncated"]) ||
    !safePositiveInteger(result.page.limit) || result.page.limit > 200 || result.page.returned !== result.items.length ||
    !safeBoundedCount(result.page.total, 200) || result.page.total < result.items.length || typeof result.page.truncated !== "boolean" ||
    (result.state === "current" && (result.warnings.length !== 0 || result.page.truncated)) ||
    (request.tool === "code_document_symbols" && (result.document_path !== request.path || !isSHA256(result.document_sha256))) ||
    (request.tool === "code_workspace_symbols" && (result.document_path !== undefined || result.document_sha256 !== undefined))) throw invalid();
  for (const item of result.items) {
    if (!isRecord(item) || !hasOnlyKeys(item, ["kind", "name", "path", "range"]) || !boundedText(item.kind, 128) ||
      (item.name !== undefined && !boundedText(item.name, 4096)) ||
      (item.path !== undefined && (!validWorkspaceRelativePath(item.path) || item.path === "."))) throw invalid();
    if (item.range !== undefined) {
      if (!hasExactKeys(item.range, ["start", "end"]) ||
        ![item.range.start, item.range.end].every((position) => hasExactKeys(position, ["line", "character"]) &&
          safeBoundedCount(position.line, 10_000_000) && safeBoundedCount(position.character, 10_000_000))) throw invalid();
      const start = item.range.start as { line: number; character: number };
      const end = item.range.end as { line: number; character: number };
      if (end.line < start.line || (end.line === start.line && end.character < start.character)) throw invalid();
    }
  }
  return { protocol_version: "code-intel-configuration.v1", configuration, server, result } as CodeIntelConfigurationTestView;
}

export function parseExtensionInventory(value: unknown, hiddenLocalSource = false): ExtensionInventoryView {
  if (!isRecord(value) || value.protocol_version !== "extension-inventory.v1" ||
    containsForbiddenExtensionField(value) ||
    !Array.isArray(value.mcp_servers) || value.mcp_servers.length > 64 ||
    !Array.isArray(value.mcp_calls) || value.mcp_calls.length > 200 ||
    !Array.isArray(value.plugins) || value.plugins.length > 1_000 ||
    (value.run_id !== undefined && !boundedIdentity(value.run_id)) ||
    (value.workspace_id !== undefined && !boundedIdentity(value.workspace_id))) {
    throw new APIRequestError("Extension inventory response is invalid", "INVALID_RESPONSE", 502);
  }
  if (value.onboarding !== undefined && (!hasExactKeys(value.onboarding,
    ["mcp_registration", "plugin_import", "lsp_configuration", ...["mcp_credentials", "plugin_lifecycle", "hook_diagnostics", "plugin_source_import", "native_skill_directory_import"].filter((key) => isRecord(value.onboarding) && value.onboarding[key] !== undefined)]) ||
    Object.values(value.onboarding).some((flag) => typeof flag !== "boolean"))) {
    throw new APIRequestError("Extension onboarding capabilities are invalid", "INVALID_RESPONSE", 502);
  }
  for (const item of value.mcp_servers) {
    if (!isRecord(item) || item.protocol_version !== "mcp-client-server.v1" ||
      !boundedIdentity(item.id) || !boundedText(item.name, 256) ||
      !["stdio", "streamable_http", "sse", "websocket"].includes(String(item.transport)) ||
      !validExtensionMCPTarget(item) || !boundedIdentity(item.workspace_id) ||
      !isSHA256(item.descriptor_fingerprint) || !safePositiveInteger(item.generation) ||
      !validDate(item.created_at) || !validDate(item.updated_at) ||
      !boundedStringArray(item.declared_capabilities, 3, 32) ||
      !isRecord(item.capabilities) ||
      !boundedStringArray(item.capabilities.negotiated, 3, 32) ||
      !boundedStringArray(item.capabilities.tools, 256, 256) ||
      !boundedStringArray(item.capabilities.resources, 256) ||
      (item.capabilities.resource_templates !== undefined && !boundedStringArray(item.capabilities.resource_templates, 256, 4096)) ||
      !boundedStringArray(item.capabilities.prompts, 128, 256) ||
      (item.capabilities.prompt_details !== undefined && !validExtensionPromptDetails(item.capabilities)) ||
      !isRecord(item.source) || !boundedText(item.source.kind, 32) ||
      !boundedText(item.source.uri, 4_096)) {
      throw new APIRequestError("MCP server projection is invalid", "INVALID_RESPONSE", 502);
    }
  }
  for (const item of value.mcp_calls) {
    if (!isRecord(item) || !boundedIdentity(item.id) || !boundedIdentity(item.run_id) ||
      !boundedIdentity(item.workspace_id) || !boundedIdentity(item.server_id) ||
      !boundedText(item.tool_name, 256) || !isSHA256(item.capability_fingerprint) ||
      !isSHA256(item.arguments_sha256) || !safeBoundedCount(item.result_bytes, 128 * 1_024) ||
      !["completed", "denied", "failed", "cancelled", "timed_out"].includes(String(item.status)) ||
      (item.error_code !== undefined && !boundedText(item.error_code, 128)) ||
      typeof item.truncated !== "boolean" || !validDate(item.started_at) ||
      !validDate(item.completed_at) || Date.parse(item.completed_at) < Date.parse(item.started_at)) {
      throw new APIRequestError("MCP call audit projection is invalid", "INVALID_RESPONSE", 502);
    }
  }
  for (const item of value.plugins) {
    if (!isRecord(item) || !["plugin-installation.v1", "plugin-installation.v2"].includes(String(item.protocol_version)) ||
      !boundedIdentity(item.id) || !isSHA256(item.archive_sha256) ||
      !isSHA256(item.package_fingerprint) || !safePositiveInteger(item.generation) ||
      typeof item.signature_present !== "boolean" || typeof item.signature_valid !== "boolean" ||
      !boundedStringArray(item.enabled_capabilities, 4, 32) ||
      !boundedIdentity(item.staged_by) ||
      !validDate(item.created_at) || !validDate(item.updated_at) ||
      !isRecord(item.manifest) || !boundedIdentity(item.manifest.id) ||
      !boundedText(item.manifest.name, 256) ||
      (item.protocol_version === "plugin-installation.v1" && !boundedText(item.manifest.publisher, 256)) ||
      !boundedStringArray(item.manifest.capabilities, 4, 32) ||
      !isRecord(item.source) || !boundedText(item.source.kind, 32) ||
      !(boundedText(item.source.uri, 4_096) ||
        (hiddenLocalSource && item.source.kind === "local_directory" && item.source.uri === ""))) {
      throw new APIRequestError("Plugin installation projection is invalid", "INVALID_RESPONSE", 502);
    }
    if (item.protocol_version === "plugin-installation.v2" &&
      (!isRecord(item.snapshot) || !["agent-skills", "agent-plugins", "traverse-skill"].includes(String(item.snapshot.format)) ||
        !isSHA256(item.snapshot.revision) || item.snapshot.revision !== item.archive_sha256 ||
        !["code", "cyber"].includes(String(item.snapshot.surface)) ||
        (item.manifest.version !== "" && !boundedText(item.manifest.version, 256)) || item.manifest.publisher !== "" ||
        item.signature_present !== false || item.signature_valid !== false)) {
      throw new APIRequestError("Portable plugin installation projection is invalid", "INVALID_RESPONSE", 502);
    }
  }
  const inventory = value as unknown as ExtensionInventoryView;
  return inventory.onboarding ? { ...inventory, onboarding: { ...inventory.onboarding,
    plugin_source_import: inventory.onboarding.plugin_source_import ?? false,
    native_skill_directory_import: inventory.onboarding.native_skill_directory_import ?? false,
  } } : inventory;
}

export function validExtensionPromptDetails(capabilities: Record<string, unknown>): boolean {
  const details = capabilities.prompt_details;
  const prompts = capabilities.prompts;
  if (!Array.isArray(details) || details.length > 128 || !Array.isArray(prompts) || details.length !== prompts.length) return false;
  const seen = new Set<string>();
  return details.every((prompt) => {
    if (!isRecord(prompt) || !hasExactKeys(prompt, ["name", "arguments"]) || !boundedText(prompt.name, 256) || seen.has(prompt.name) ||
      !prompts.includes(prompt.name) || !Array.isArray(prompt.arguments) || prompt.arguments.length > 128) return false;
    seen.add(prompt.name);
    const argumentsSeen = new Set<string>();
    return prompt.arguments.every((argument) => {
      if (!isRecord(argument) || !hasExactKeys(argument, ["name", "required", ...(argument.description !== undefined ? ["description"] : [])]) ||
        !boundedText(argument.name, 256) || argumentsSeen.has(argument.name) || typeof argument.required !== "boolean" ||
        argument.description !== undefined && argument.description !== "" && !boundedText(argument.description, 16384)) return false;
      argumentsSeen.add(argument.name);
      return true;
    });
  });
}

export function parseExtensionMCPServer(value: unknown): ExtensionMCPServerView {
  return parseExtensionInventory({ protocol_version: "extension-inventory.v1",
    mcp_servers: [value], mcp_calls: [], plugins: [] }).mcp_servers[0];
}

export function validExtensionMCPTarget(item: Record<string, unknown>): boolean {
  if ((isRecord(item.source) && item.source.kind === "builtin") ||
    (typeof item.target === "string" && item.target.startsWith("builtin:"))) {
    return validBuiltinComputerUseConnection(item);
  }
  if (item.native_source === undefined) return boundedText(item.target, 4_096) &&
    validMCPConnectionConfig(item as { transport: string });
  const source = item.native_source;
  return item.target === "" && (item.credential_ref === undefined || item.credential_ref === "") &&
    isRecord(source) && boundedIdentity(source.installation_id) !== "" &&
    boundedIdentity(source.package_id) !== "" && boundedIdentity(source.component_id) !== "" &&
    isSHA256(source.revision) && safePositiveInteger(source.installation_generation) &&
    (source.surface === "code" || source.surface === "cyber");
}

export function parseExtensionPlugin(value: unknown): ExtensionPluginInstallationView {
  return parseExtensionInventory({ protocol_version: "extension-inventory.v1",
    mcp_servers: [], mcp_calls: [], plugins: [value] }).plugins[0];
}

export const pluginStates = ["staged", "approved", "enabled", "disabled", "rolled_back", "revoked", "quarantined"];

export const pluginCapabilities = ["skills", "mcp", "ui", "hooks"];

export const hookEvents = ["pre_tool", "post_tool", "run_started", "run_completed", "session_opened", "session_closed", "compaction", "subagent", "checkpoint"];

export const hookActions = ["deny", "annotate", "narrow", "record"];

export function parsePluginPublisher(value: unknown): PluginPublisherTrustView {
  if (!hasExactKeys(value, ["fingerprint", "publisher", "state", "generation", "reviewed_at"]) ||
    !isSHA256(value.fingerprint) || !boundedText(value.publisher, 256) ||
    !["trusted", "revoked"].includes(String(value.state)) || !safePositiveInteger(value.generation) || !validDate(value.reviewed_at)) {
    throw new APIRequestError("Plugin publisher projection is invalid", "INVALID_RESPONSE", 502);
  }
  return value as PluginPublisherTrustView;
}

export function parsePluginHistory(value: unknown, installationID: string): PluginHistoryView {
  if (!hasExactKeys(value, ["protocol_version", "installation_id", "package_id", "installations", "publisher_installation_ids", "total_versions", "total_publisher_installations",
    ...(isRecord(value) && value.publisher !== undefined ? ["publisher"] : [])]) || value.protocol_version !== "plugin-lifecycle.v1" ||
    value.installation_id !== installationID || !boundedIdentity(value.package_id) ||
    !Array.isArray(value.installations) || value.installations.length === 0 || value.installations.length > 1000 ||
    !safeBoundedCount(value.total_versions, Number.MAX_SAFE_INTEGER) || value.total_versions < value.installations.length ||
    !safeBoundedCount(value.total_publisher_installations, Number.MAX_SAFE_INTEGER) ||
    !boundedStringArray(value.publisher_installation_ids, 1000, 256) || !value.publisher_installation_ids.every(boundedIdentity) ||
    new Set(value.publisher_installation_ids).size !== value.publisher_installation_ids.length || value.total_publisher_installations < value.publisher_installation_ids.length) {
    throw new APIRequestError("Plugin version history is invalid", "INVALID_RESPONSE", 502);
  }
  const installations = value.installations.map(parseExtensionPlugin);
  const current = installations.find((item) => item.id === installationID);
  if (!current || new Set(installations.map((item) => item.id)).size !== installations.length ||
    installations.some((item) => item.manifest.id !== value.package_id || !pluginStates.includes(item.state) ||
      item.protocol_version !== current.protocol_version || (item.snapshot?.surface ?? "") !== (current.snapshot?.surface ?? ""))) {
    throw new APIRequestError("Plugin version history binding is invalid", "INVALID_RESPONSE", 502);
  }
  const publisher = value.publisher === undefined ? undefined : parsePluginPublisher(value.publisher);
  if (publisher && (!current.signature_valid || publisher.fingerprint !== current.publisher_fingerprint ||
    (publisher.state === "trusted" && publisher.publisher !== current.manifest.publisher))) {
    throw new APIRequestError("Plugin publisher binding is invalid", "INVALID_RESPONSE", 502);
  }
  return { ...value, installations, ...(publisher ? { publisher } : {}) } as PluginHistoryView;
}

export function parseHookDiagnostics(value: unknown, runID: string, workspaceID: string): HookDiagnosticsView {
  if (!isRecord(value) || !hasOnlyKeys(value, ["protocol_version", "run_id", "workspace_id", "declarations", "omitted_declarations", "observations"]) ||
    value.protocol_version !== "hook-diagnostics.v1" || !Array.isArray(value.declarations) || value.declarations.length > 1000 ||
    !Array.isArray(value.observations) || value.observations.length > 200 || !safeBoundedCount(value.omitted_declarations, 64000) ||
    (value.run_id !== undefined && !boundedIdentity(value.run_id)) || (value.workspace_id !== undefined && !boundedIdentity(value.workspace_id)) ||
    (runID && (value.run_id !== runID || !boundedIdentity(value.workspace_id))) || (!runID && value.run_id !== undefined) ||
    (workspaceID && value.workspace_id !== workspaceID) || (!runID && !workspaceID && value.workspace_id !== undefined)) {
    throw new APIRequestError("Hook diagnostic scope is invalid", "INVALID_RESPONSE", 502);
  }
  for (const item of value.declarations) {
    if (!hasExactKeys(item, ["installation_id", "plugin_id", "package_fingerprint", "installation_state", "active", "scope", "hook_id", "event", "action", "failure_policy", "timeout_ms", "tool_names", "remove_fields"]) ||
      !boundedIdentity(item.installation_id) || !boundedIdentity(item.plugin_id) || !isSHA256(item.package_fingerprint) ||
      !pluginStates.includes(String(item.installation_state)) || typeof item.active !== "boolean" || (item.active && item.installation_state !== "enabled") ||
      item.scope !== "local" || !boundedIdentity(item.hook_id) || !hookEvents.includes(String(item.event)) || !hookActions.includes(String(item.action)) ||
      !["continue", "deny"].includes(String(item.failure_policy)) || !safePositiveInteger(item.timeout_ms) || item.timeout_ms > 2000 ||
      !boundedStringArray(item.tool_names, 64, 256) || !boundedStringArray(item.remove_fields, 32, 256)) {
      throw new APIRequestError("Hook declaration projection is invalid", "INVALID_RESPONSE", 502);
    }
  }
  for (const item of value.observations) {
    if (!isRecord(item) || !hasOnlyKeys(item, ["id", "plugin_id", "hook_id", "package_fingerprint", "event", "action", "run_id", "workspace_id", "tool_name", "outcome", "decision", "created_at"]) ||
      !boundedIdentity(item.id) || !boundedIdentity(item.plugin_id) || !boundedIdentity(item.hook_id) || !hookEvents.includes(String(item.event)) ||
      !["completed", "failed_closed", "failed_continue"].includes(String(item.outcome)) || !["rejected", "continued", "unknown"].includes(String(item.decision)) ||
      !validDate(item.created_at) || (item.run_id !== undefined && !boundedIdentity(item.run_id)) || (item.workspace_id !== undefined && !boundedIdentity(item.workspace_id)) ||
      (item.tool_name !== undefined && !boundedText(item.tool_name, 256)) || (runID && item.run_id !== runID) ||
      ((runID || workspaceID) && item.workspace_id !== value.workspace_id) ||
      (item.package_fingerprint !== undefined && !isSHA256(item.package_fingerprint)) ||
      (item.action !== undefined && (!hookActions.includes(String(item.action)) || !isSHA256(item.package_fingerprint))) ||
      (item.action !== undefined && item.decision !== (item.outcome === "failed_closed" || item.action === "deny" && item.outcome === "completed" ? "rejected" : "continued")) ||
      (item.action === undefined && item.decision !== (item.outcome === "failed_closed" ? "rejected" : "unknown"))) {
      throw new APIRequestError("Hook observation projection is invalid", "INVALID_RESPONSE", 502);
    }
  }
  return value as unknown as HookDiagnosticsView;
}

export function validCanonicalBase64(value: unknown, maximumBytes: number): value is string {
  if (typeof value !== "string" || value.length === 0 ||
    value.length > Math.ceil(maximumBytes / 3) * 4) return false;
  try {
    const decoded = atob(value);
    // A grouped-repeat regex can exhaust the browser's stack on valid uploads.
    // The round-trip also rejects whitespace, missing padding and trailing bits.
    return decoded.length <= maximumBytes && btoa(decoded) === value;
  } catch { return false; }
}

export function parseMCPRegistration(value: unknown,
  request: ExtensionMCPRegistrationRequestView): ExtensionMCPRegistrationView {
  if (!hasExactKeys(value, ["protocol_version", "server", "replayed", "next_step"]) ||
    value.protocol_version !== "extension-onboarding.v1" || typeof value.replayed !== "boolean" ||
    !["approve_discovery", "refresh", "enable_capabilities", "request_execution", "inspect_state"].includes(String(value.next_step))) {
    throw new APIRequestError("MCP registration response is invalid", "INVALID_RESPONSE", 502);
  }
  const server = parseExtensionMCPServer(value.server);
  if (server.id !== request.descriptor.id || server.workspace_id !== request.descriptor.workspace_id ||
    server.transport !== request.descriptor.transport || server.target !== request.descriptor.target ||
    server.scope !== request.descriptor.scope || (server.run_id ?? "") !== (request.descriptor.run_id ?? "") ||
    (!value.replayed && (server.state !== "staged" || server.generation !== 1))) {
    throw new APIRequestError("MCP registration binding is invalid", "INVALID_RESPONSE", 502);
  }
  return { ...value, server } as ExtensionMCPRegistrationView;
}

export function parsePluginImport(value: unknown,
  request: ExtensionPluginImportRequestView): ExtensionPluginImportView {
  if (!hasRequiredOnlyKeys(value, ["protocol_version", "installation", "replayed", "next_step"], ["acquisition"]) ||
    value.protocol_version !== "extension-onboarding.v1" || typeof value.replayed !== "boolean" ||
    !["approve", "enable", "select_contributions", "inspect_state"].includes(String(value.next_step))) {
    throw new APIRequestError("Plugin import response is invalid", "INVALID_RESPONSE", 502);
  }
  const installation = parseExtensionPlugin(value.installation);
  const acquisition = value.acquisition;
  if (request.source) {
    const source = request.source;
    if (!hasExactKeys(acquisition, ["kind", "url", "sha256", ...(source.kind === "git" ? ["commit", "archive_path"] : [])]) ||
      acquisition.kind !== source.kind || acquisition.url !== source.url ||
      !isSHA256(acquisition.sha256) || acquisition.sha256 !== installation.archive_sha256 ||
      (source.kind === "git" && (acquisition.commit !== source.commit ||
        acquisition.archive_path !== (source.archive_path ?? "plugin.zip")))) {
      throw new APIRequestError("Plugin acquisition binding is invalid", "INVALID_RESPONSE", 502);
    }
  } else if (acquisition !== undefined) {
    throw new APIRequestError("Uploaded Plugin response includes an unexpected acquisition", "INVALID_RESPONSE", 502);
  }
  if ((request.archive_sha256 !== undefined && installation.archive_sha256 !== request.archive_sha256) ||
    (!value.replayed && request.source && (installation.source.kind !== request.source.kind ||
      installation.source.uri !== request.source.url || (installation.source.commit ?? "") !== (request.source.commit ?? ""))) ||
    (!value.replayed && (installation.state !== "staged" || installation.generation !== 1 ||
      installation.enabled_capabilities.length !== 0))) {
    throw new APIRequestError("Plugin import binding is invalid", "INVALID_RESPONSE", 502);
  }
  return { ...value, installation } as ExtensionPluginImportView;
}
