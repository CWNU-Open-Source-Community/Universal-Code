import { validMCPConnectionConfig } from "../../mcp-config";
import {
  parseMCPCredentialStatus,
  validMCPBearerSecret,
  validMCPCredentialBinding,
} from "../../mcp-credentials";
import { APIRequestError } from "../protocol";
import {
  parseCodeIntelConfiguration,
  parseCodeIntelConfigurationTest,
  parseCodeIntelInventory,
  parseExtensionInventory,
  parseExtensionMCPServer,
  parseExtensionPlugin,
  parseHookDiagnostics,
  parseMCPRegistration,
  parsePluginHistory,
  parsePluginImport,
  parsePluginPublisher,
  pluginCapabilities,
  strictCodeIntelIdentity,
  validCanonicalBase64,
} from "../responses/extensions";
import {
  boundedIdentity,
  boundedStringArray,
  boundedText,
  hasExactKeys,
  isRecord,
  isSHA256,
  safeBoundedCount,
  safePositiveInteger,
  validWorkspaceRelativePath,
} from "../validation";
import type {
  CodeIntelConfigurationRequestView,
  CodeIntelConfigurationReviewRequestView,
  CodeIntelConfigurationTestRequestView,
  CodeIntelConfigurationTestView,
  CodeIntelConfigurationView,
  CodeIntelInventoryView,
  ExtensionInventoryView,
  ExtensionMCPRegistrationRequestView,
  ExtensionMCPRegistrationView,
  ExtensionMCPReviewRequestView,
  ExtensionMCPServerView,
  ExtensionPluginImportRequestView,
  ExtensionPluginImportView,
  ExtensionPluginInstallationView,
  ExtensionPluginReviewRequestView,
  HookDiagnosticsView,
  MCPCredentialBindingView,
  MCPCredentialRequestView,
  MCPCredentialStatusView,
  PluginHistoryView,
  PluginPublisherRevocationRequestView,
  PluginPublisherRevocationView,
  PluginPublisherTrustRequestView,
  PluginPublisherTrustResponseView,
  PluginRollbackRequestView,
  PluginRollbackView,
} from "../../types";
import type { ClientFeatures } from "../capabilities";
import type { APITransport } from "../transport";

export class ExtensionsClient {
  constructor(
    private readonly transport: APITransport,
    private readonly features: ClientFeatures,
  ) {}

  async codeIntelInventory(workspaceID = "", signal?: AbortSignal):
    Promise<CodeIntelInventoryView> {
    if (workspaceID !== "" && (!strictCodeIntelIdentity(workspaceID) ||
      workspaceID.trim() !== workspaceID)) {
      throw new Error("A normalized Workspace identity is required");
    }
    return parseCodeIntelInventory(await this.transport.get<unknown>(
      "/code-intel", { workspace_id: workspaceID || undefined }, signal,
    ));
  }

  async extensionInventory(runID = "", signal?: AbortSignal, workspaceID = ""): Promise<ExtensionInventoryView> {
    if (runID !== "" && (!boundedIdentity(runID) || runID.trim() !== runID)) {
      throw new Error("A normalized Run identity is required");
    }
    if (workspaceID !== "" && (!boundedIdentity(workspaceID) || workspaceID.trim() !== workspaceID)) {
      throw new Error("A normalized Workspace identity is required");
    }
    const inventory = parseExtensionInventory(await this.transport.get<unknown>(
      "/extensions", { run_id: runID || undefined, workspace_id: workspaceID || undefined }, signal,
    ));
    if ((runID && inventory.run_id !== runID) || (workspaceID && inventory.workspace_id !== workspaceID) ||
      (runID && inventory.mcp_calls.some((call) => call.run_id !== runID)) ||
      (!runID && inventory.mcp_calls.length > 0) ||
      (workspaceID && inventory.mcp_servers.some((server) => server.workspace_id !== workspaceID))) {
      throw new APIRequestError("Extension inventory scope does not match the request", "INVALID_RESPONSE", 502);
    }
    return inventory;
  }

  async registerMCPServer(body: ExtensionMCPRegistrationRequestView,
    signal?: AbortSignal): Promise<ExtensionMCPRegistrationView> {
    const descriptor = body.descriptor;
    if (!this.features.hasExtensionControl || body.version !== "extension-control.v1" ||
      !isRecord(descriptor) || !["mcp-client.v1", "mcp-client.v3"].includes(descriptor.protocol_version) ||
      !validMCPConnectionConfig(descriptor) ||
      !boundedIdentity(descriptor.id) || !boundedIdentity(descriptor.workspace_id) ||
      !boundedText(descriptor.name, 256) || !boundedText(descriptor.target, 4_096) ||
      !["stdio", "streamable_http", "sse", "websocket"].includes(String(descriptor.transport)) ||
      !["workspace", "run"].includes(String(descriptor.scope)) ||
      (descriptor.scope === "run" && !boundedIdentity(descriptor.run_id)) ||
      (descriptor.scope === "workspace" && descriptor.run_id !== undefined && descriptor.run_id !== "") ||
      !boundedStringArray(descriptor.declared_capabilities, 3, 32) ||
      !safePositiveInteger(descriptor.call_timeout_ms) || descriptor.call_timeout_ms < 100 ||
      descriptor.call_timeout_ms > 300_000 || !safePositiveInteger(descriptor.max_result_bytes) ||
      descriptor.max_result_bytes > 131_072) {
      throw new Error("A bounded MCP descriptor and extension control are required");
    }
    return parseMCPRegistration(await this.transport.sendControlRequest<unknown>("/extensions/mcp", body, signal), body);
  }

  async mcpCredentialStatus(binding: MCPCredentialBindingView, signal?: AbortSignal): Promise<MCPCredentialStatusView> {
    if (!validMCPCredentialBinding(binding)) throw new Error("An exact HTTPS MCP credential binding is required");
    const value = await this.transport.get<unknown>(`/extensions/mcp/${encodeURIComponent(binding.server_id)}/credential`, {
      workspace_id: binding.workspace_id, run_id: binding.run_id,
      expected_descriptor_fingerprint: binding.expected_descriptor_fingerprint, target: binding.target, credential_ref: binding.credential_ref,
    }, signal);
    try { return parseMCPCredentialStatus(value, binding); }
    catch { throw new APIRequestError("MCP credential status violated its metadata or binding contract", "INVALID_RESPONSE", 502); }
  }

  async changeMCPCredential(body: MCPCredentialRequestView, signal?: AbortSignal): Promise<MCPCredentialStatusView> {
    if (!this.features.hasExtensionControl || body.version !== "mcp-credential.v1" || !validMCPCredentialBinding(body.binding) ||
      body.confirm !== true || !isSHA256(body.expected_reference_fingerprint) ||
      (body.action !== "set" && body.action !== "delete") ||
      (body.action === "set" && !validMCPBearerSecret(body.secret ?? "")) || (body.action === "delete" && !!body.secret)) {
      throw new Error("An exact MCP binding, explicit credential action and confirmation are required");
    }
    const value = await this.transport.sendControlRequest<unknown>(`/extensions/mcp/${encodeURIComponent(body.binding.server_id)}/credential`, body, signal);
    try {
      const status = parseMCPCredentialStatus(value, body.binding);
      if (!status.store_available || status.endpoint_conflict || status.configured !== (body.action === "set") ||
        status.reference_fingerprint !== body.expected_reference_fingerprint) throw new Error("Unverified mutation outcome");
      return status;
    }
    catch { throw new APIRequestError("MCP credential change returned invalid presence metadata; refresh before retrying", "INVALID_RESPONSE", 502); }
  }

  async changeMCPOAuth(body: MCPCredentialRequestView, signal?: AbortSignal): Promise<MCPCredentialStatusView> {
    if (!this.features.hasExtensionControl || body.version !== "mcp-credential.v1" || !validMCPCredentialBinding(body.binding) ||
      !body.confirm || !isSHA256(body.expected_reference_fingerprint) || body.secret ||
      !["oauth-discover", "oauth-start", "oauth-cancel"].includes(body.action)) throw new Error("An exact MCP OAuth binding and confirmation are required");
    if (body.oauth_client_id_mode !== undefined &&
      (body.action !== "oauth-start" || !["auto", "dynamic", "pre_registered", "metadata_url"].includes(body.oauth_client_id_mode))) throw new Error("Invalid OAuth client ID mode");
    if (body.oauth_callback_port !== undefined && (body.action !== "oauth-start" || !Number.isInteger(body.oauth_callback_port) || body.oauth_callback_port < 0 || body.oauth_callback_port > 65535)) throw new Error("Invalid OAuth callback port");
    if (body.oauth_client_secret !== undefined && (body.action !== "oauth-start" || !body.oauth_client_id || !["auto", "pre_registered"].includes(body.oauth_client_id_mode ?? "pre_registered") || !body.oauth_client_secret || body.oauth_client_secret.length > 1024 || /[\r\n\0]/u.test(body.oauth_client_secret))) throw new Error("Invalid OAuth client secret configuration");
    if (body.action === "oauth-start" && body.oauth_client_id_mode === "metadata_url") {
      const url = new URL(body.oauth_client_id ?? "");
      if (url.protocol !== "https:" || !url.hostname || url.username || url.password || url.search || url.hash || url.pathname === "/" ||
        (body.oauth_client_id?.length ?? 0) > 256) throw new Error("A hosted HTTPS client metadata document URL is required");
    }
    const value=await this.transport.sendControlRequest<unknown>(`/extensions/mcp/${encodeURIComponent(body.binding.server_id)}/credential`,body,signal);
    return parseMCPCredentialStatus(value,body.binding);
  }

  async importPluginPackage(body: ExtensionPluginImportRequestView,
    signal?: AbortSignal): Promise<ExtensionPluginImportView> {
    if (!this.features.hasExtensionControl || body.version !== "extension-control.v1") {
      throw new Error("Plugin import control is required");
    }
    if (body.source) {
      const source = body.source;
      const url = new URL(source.url);
      if (body.archive_base64 || url.protocol !== "https:" || !url.hostname || url.username || url.password ||
        url.search || url.hash || source.url.length > 4096 ||
        (source.kind !== "https" && source.kind !== "git") ||
        (source.kind === "https" && (!isSHA256(body.archive_sha256) || source.commit || source.archive_path)) ||
        (source.kind === "git" && (!/^(?:[a-f0-9]{40}|[a-f0-9]{64})$/u.test(source.commit ?? "") ||
          (body.archive_sha256 !== undefined && !isSHA256(body.archive_sha256)) ||
          (source.archive_path !== undefined && (!/^(?!\/)(?!.*(?:^|\/)\.{1,2}(?:\/|$))[^\\:\0]+\.zip$/u.test(source.archive_path) || source.archive_path.length > 512))))) {
        throw new Error("A credential-free HTTPS URL with an exact SHA-256 or Git commit is required");
      }
    } else if (!isSHA256(body.archive_sha256) || !validCanonicalBase64(body.archive_base64, 4 * 1_024 * 1_024)) {
      throw new Error("A bounded Plugin ZIP and exact SHA-256 are required");
    }
    return parsePluginImport(await this.transport.sendControlRequest<unknown>("/extensions/plugins/import", body, signal), body);
  }

  async stageCodeIntelConfiguration(body: CodeIntelConfigurationRequestView,
    signal?: AbortSignal): Promise<CodeIntelConfigurationView> {
    if (!this.features.hasExtensionControl || body.version !== "code-intel-configuration.v1" ||
      !strictCodeIntelIdentity(body.server_id) || !strictCodeIntelIdentity(body.workspace_id) ||
      !isSHA256(body.executable_sha256) || !boundedText(body.executable, 4096) ||
      !boundedStringArray(body.arguments, 64, 4096) || !safePositiveInteger(body.request_timeout_ms)) {
      throw new Error("A pinned language-server descriptor and extension control are required");
    }
    const configuration = parseCodeIntelConfiguration(await this.transport.sendControlRequest<unknown>("/code-intel/configurations", body, signal));
    if (configuration.server_id !== body.server_id || configuration.workspace_id !== body.workspace_id ||
      configuration.executable_sha256 !== body.executable_sha256) {
      throw new APIRequestError("Code intelligence configuration binding is invalid", "INVALID_RESPONSE", 502);
    }
    return configuration;
  }

  async reviewCodeIntelConfiguration(serverID: string, body: CodeIntelConfigurationReviewRequestView,
    signal?: AbortSignal): Promise<CodeIntelConfigurationView> {
    if (!this.features.hasExtensionControl || !strictCodeIntelIdentity(serverID) ||
      body.version !== "code-intel-configuration.v1" || !strictCodeIntelIdentity(body.workspace_id) ||
      !isSHA256(body.expected_descriptor_fingerprint)) throw new Error("An exact language-server review is required");
    const configuration = parseCodeIntelConfiguration(await this.transport.sendControlRequest<unknown>(
      `/code-intel/configurations/${encodeURIComponent(serverID)}/review`, body, signal));
    if (configuration.server_id !== serverID || configuration.workspace_id !== body.workspace_id ||
      configuration.review_state !== "reviewed") {
      throw new APIRequestError("Code intelligence review binding is invalid", "INVALID_RESPONSE", 502);
    }
    return configuration;
  }

  async testCodeIntelConfiguration(serverID: string, body: CodeIntelConfigurationTestRequestView,
    signal?: AbortSignal): Promise<CodeIntelConfigurationTestView> {
    if (!this.features.hasExtensionControl || !strictCodeIntelIdentity(serverID) ||
      body.version !== "code-intel-configuration.v1" || !strictCodeIntelIdentity(body.workspace_id) ||
      !isSHA256(body.expected_descriptor_fingerprint) || !["code_document_symbols", "code_workspace_symbols"].includes(body.tool) ||
      (body.tool === "code_document_symbols" && (!validWorkspaceRelativePath(body.path) || body.path === ".")) ||
      (body.query !== undefined && body.query !== "" && !boundedText(body.query, 4096))) throw new Error("An exact readonly language-server test is required");
    return parseCodeIntelConfigurationTest(await this.transport.sendControlRequest<unknown>(
      `/code-intel/configurations/${encodeURIComponent(serverID)}/test`, body, signal), serverID, body);
  }

  async reviewMCPServer(serverID: string, body: ExtensionMCPReviewRequestView,
    signal?: AbortSignal): Promise<ExtensionMCPServerView> {
    if (!this.features.hasExtensionControl || !boundedIdentity(serverID) ||
      body.version !== "extension-control.v1" || !isSHA256(body.expected_descriptor_fingerprint) ||
      (body.expected_capability_fingerprint !== undefined &&
        !isSHA256(body.expected_capability_fingerprint))) {
      throw new Error("An exact MCP review request and extension control are required");
    }
    return parseExtensionMCPServer(await this.transport.sendControlRequest<unknown>(
      `/extensions/mcp/${encodeURIComponent(serverID)}/review`, body, signal,
    ));
  }

  async refreshMCPServer(serverID: string,
    signal?: AbortSignal): Promise<ExtensionMCPServerView> {
    if (!this.features.hasExtensionControl || !boundedIdentity(serverID)) {
      throw new Error("A normalized MCP server and extension control are required");
    }
    return parseExtensionMCPServer(await this.transport.sendControlRequest<unknown>(
      `/extensions/mcp/${encodeURIComponent(serverID)}/refresh`,
      { version: "extension-control.v1" }, signal,
    ));
  }

  async reviewPluginInstallation(installationID: string,
    body: ExtensionPluginReviewRequestView,
    signal?: AbortSignal): Promise<ExtensionPluginInstallationView> {
    if (!this.features.hasExtensionControl || !boundedIdentity(installationID) ||
      body.version !== "extension-control.v1" ||
      !isSHA256(body.expected_package_fingerprint) ||
      !safePositiveInteger(body.expected_generation)) {
      throw new Error("An exact Plugin review request and extension control are required");
    }
    return parseExtensionPlugin(await this.transport.sendControlRequest<unknown>(
      `/extensions/plugins/${encodeURIComponent(installationID)}/review`, body, signal,
    ));
  }

  async pluginHistory(installationID: string, signal?: AbortSignal): Promise<PluginHistoryView> {
    if (!boundedIdentity(installationID)) throw new Error("A Plugin installation identity is required");
    return parsePluginHistory(await this.transport.get<unknown>(`/extensions/plugins/${encodeURIComponent(installationID)}/history`, {}, signal), installationID);
  }

  async rollbackPluginInstallation(installationID: string, body: PluginRollbackRequestView, signal?: AbortSignal): Promise<PluginRollbackView> {
    if (!this.features.hasExtensionControl || !boundedIdentity(installationID) || !boundedIdentity(body.target_installation_id) || body.target_installation_id === installationID ||
      body.version !== "plugin-lifecycle.v1" || !isSHA256(body.expected_current_fingerprint) || !isSHA256(body.expected_target_fingerprint) ||
      !safePositiveInteger(body.expected_current_generation) || !safePositiveInteger(body.expected_target_generation) || typeof body.confirm_untrusted !== "boolean" ||
      !boundedStringArray(body.capabilities, 4, 32) || body.capabilities.length === 0 || new Set(body.capabilities).size !== body.capabilities.length ||
      body.capabilities.some((capability) => !pluginCapabilities.includes(capability))) throw new Error("Exact Plugin version bindings and selected capabilities are required");
    const value = await this.transport.sendControlRequest<unknown>(`/extensions/plugins/${encodeURIComponent(installationID)}/rollback`, body, signal);
    if (!hasExactKeys(value, ["protocol_version", "current", "target"]) || value.protocol_version !== "plugin-lifecycle.v1") throw new APIRequestError("Plugin rollback projection is invalid", "INVALID_RESPONSE", 502);
    const current = parseExtensionPlugin(value.current), target = parseExtensionPlugin(value.target);
    if (current.id !== installationID || target.id !== body.target_installation_id || current.manifest.id !== target.manifest.id ||
      current.protocol_version !== target.protocol_version || (current.snapshot?.surface ?? "") !== (target.snapshot?.surface ?? "") ||
      current.package_fingerprint !== body.expected_current_fingerprint || target.package_fingerprint !== body.expected_target_fingerprint ||
      current.generation !== body.expected_current_generation + 1 || target.generation !== body.expected_target_generation + 1 ||
      current.state !== "rolled_back" || current.enabled_capabilities.length !== 0 || target.state !== "enabled" ||
      [...target.enabled_capabilities].sort().join(",") !== [...body.capabilities].sort().join(",")) throw new APIRequestError("Plugin rollback result differs from its version bindings", "INVALID_RESPONSE", 502);
    return { protocol_version: "plugin-lifecycle.v1", current, target };
  }

  async revokePluginPublisher(installationID: string, body: PluginPublisherRevocationRequestView, signal?: AbortSignal): Promise<PluginPublisherRevocationView> {
    if (!this.features.hasExtensionControl || !boundedIdentity(installationID) || body.version !== "plugin-lifecycle.v1" || body.confirm !== true ||
      !isSHA256(body.expected_publisher_fingerprint) || !safePositiveInteger(body.expected_publisher_generation)) throw new Error("An exact publisher trust binding and confirmation are required");
    const value = await this.transport.sendControlRequest<unknown>(`/extensions/plugins/${encodeURIComponent(installationID)}/publisher-revocation`, body, signal);
    if (!hasExactKeys(value, ["protocol_version", "installation_id", "publisher"]) || value.protocol_version !== "plugin-lifecycle.v1" || value.installation_id !== installationID) throw new APIRequestError("Publisher revocation projection is invalid", "INVALID_RESPONSE", 502);
    const publisher = parsePluginPublisher(value.publisher);
    if (publisher.fingerprint !== body.expected_publisher_fingerprint || publisher.generation !== body.expected_publisher_generation + 1 || publisher.state !== "revoked") throw new APIRequestError("Publisher revocation binding is invalid", "INVALID_RESPONSE", 502);
    return { ...value, publisher } as PluginPublisherRevocationView;
  }

  async trustPluginPublisher(installationID: string,
    body: PluginPublisherTrustRequestView, signal?: AbortSignal,
  ): Promise<PluginPublisherTrustResponseView> {
    if (!this.features.hasExtensionControl || !boundedIdentity(installationID) ||
      body.version !== "plugin-lifecycle.v1" || body.confirm !== true ||
      !isSHA256(body.expected_publisher_fingerprint) ||
      !safeBoundedCount(body.expected_publisher_generation, Number.MAX_SAFE_INTEGER)) {
      throw new Error("An exact publisher trust binding and confirmation are required");
    }
    const value = await this.transport.sendControlRequest<unknown>(
      `/extensions/plugins/${encodeURIComponent(installationID)}/publisher-trust`, body, signal);
    if (!hasExactKeys(value, ["protocol_version", "installation_id", "publisher"]) ||
      value.protocol_version !== "plugin-lifecycle.v1" || value.installation_id !== installationID) {
      throw new APIRequestError("Publisher trust projection is invalid", "INVALID_RESPONSE", 502);
    }
    const publisher = parsePluginPublisher(value.publisher);
    if (publisher.fingerprint !== body.expected_publisher_fingerprint ||
      publisher.generation !== body.expected_publisher_generation + 1 ||
      publisher.state !== "trusted") {
      throw new APIRequestError("Publisher trust binding is invalid", "INVALID_RESPONSE", 502);
    }
    return { ...value, publisher } as PluginPublisherTrustResponseView;
  }

  async hookDiagnostics(runID = "", workspaceID = "", signal?: AbortSignal): Promise<HookDiagnosticsView> {
    if ((runID && !boundedIdentity(runID)) || (workspaceID && !boundedIdentity(workspaceID))) throw new Error("An exact Hook diagnostic scope is required");
    return parseHookDiagnostics(await this.transport.get<unknown>("/extensions/hooks", { run_id: runID || undefined, workspace_id: workspaceID || undefined }, signal), runID, workspaceID);
  }

  async respondMCPInput(runID: string, inputID: string,
    body: { version: "mcp-input.v1"; action: "accept" | "decline" | "cancel"; content?: Record<string, unknown> }): Promise<{ accepted: boolean }> {
    if (!this.features.hasExtensionControl || !boundedIdentity(runID) || !boundedIdentity(inputID) ||
      body.version !== "mcp-input.v1" || !["accept", "decline", "cancel"].includes(body.action)) {
      throw new Error("An active MCP request and extension control capability are required");
    }
    // The live broker consumes an answer once. It is not a durable command and
    // must not carry a replay key or be retried automatically.
    return this.transport.sendControlRequest(`/runs/${encodeURIComponent(runID)}/mcp-inputs/${encodeURIComponent(inputID)}`, body);
  }
}
