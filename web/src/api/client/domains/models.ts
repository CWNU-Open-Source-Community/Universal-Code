import { chatGPTAuthProtocol, parseChatGPTAuthStatus, validChatGPTIdentity } from "../../chatgpt-auth";
import { APIRequestError } from "../protocol";
import {
  parseModelCancellation,
  parsePublicModelStream,
  parsePublicModelStreamPoll,
  parseSpecialistModelCancellation,
} from "../responses/model-streams";
import {
  parseAvailableModelRouteCollection,
  parseModelAvailability,
  parseModelHarnessQualification,
  parseModelRouteControl,
  parsePriceSnapshotImport,
  parsePriceSnapshots,
  parseProviderCredentialList,
  parseProviderCredentialStatus,
  parseProviderDefinition,
  parseProviderDefinitionCollection,
  parseProviderDefinitionMutation,
  parseProviderDiagnostic,
  parseProviderSearchReadiness,
  parseSearchDiagnostics,
  parseThreadModelRoute,
  validCustomProviderID,
  validProviderAdvancedConfig,
  validProviderCredentialName,
} from "../responses/models";
import {
  boundedIdentity,
  hasExactKeys,
  isRecord,
  safeBoundedCount,
  safePositiveInteger,
} from "../validation";
import type {
  ChatGPTAccountRequestView,
  ChatGPTAuthRequestView,
  ChatGPTAuthStatusView,
  ChatGPTCancelRequestView,
  ChatGPTLoginRequestView,
} from "../../chatgpt-auth";
import type {
  AvailableModelRouteCollectionView,
  ModelAvailabilityView,
  ModelCancellationRequestView,
  ModelCancellationView,
  ModelHarnessQualificationRequestView,
  ModelHarnessQualificationView,
  ModelRouteControlRequestView,
  PriceSnapshotImportRequestView,
  PriceSnapshotImportView,
  PriceSnapshotListView,
  ProviderCredentialListView,
  ProviderCredentialRequestView,
  ProviderCredentialStatusView,
  ProviderDefinitionCollectionView,
  ProviderDefinitionDeleteRequestView,
  ProviderDefinitionMutationView,
  ProviderDefinitionUpsertRequestView,
  ProviderDiagnosticRequestView,
  ProviderDiagnosticView,
  ProviderModelDiscoveryRequestView,
  ProviderModelDiscoveryView,
  ProviderSearchReadinessView,
  PublicModelStreamSnapshot,
  SearchDiagnosticsRequestView,
  SearchDiagnosticsView,
  SpecialistModelCancellationView,
  ThreadModelRouteControlRequestView,
  ThreadModelRouteView,
} from "../../types";
import type { ClientFeatures } from "../capabilities";
import type { APITransport } from "../transport";

export class ModelsClient {
  constructor(
    private readonly transport: APITransport,
    private readonly features: ClientFeatures,
  ) {}

  async modelAvailability(signal?: AbortSignal): Promise<ModelAvailabilityView> {
    const value = await this.transport.get<unknown>("/models", {}, signal);
    return parseModelAvailability(value);
  }

  async selectModelRoute(route: string, body: ModelRouteControlRequestView,
    signal?: AbortSignal): Promise<ModelAvailabilityView["routes"][number]> {
    if (!this.features.hasModelControl || !boundedIdentity(route) || route.trim() !== route) {
      throw new Error("Model control capability and a normalized route are required");
    }
    const result = await this.transport.sendControlRequest<unknown>(
      `/models/routes/${encodeURIComponent(route)}`, body, signal,
    );
    return parseModelRouteControl(result, route, body);
  }

  async availableModelRoutes(signal?: AbortSignal): Promise<AvailableModelRouteCollectionView> {
    if (!this.features.hasModelControl) {
      throw new Error("Model control capability is required to list selectable routes");
    }
    return parseAvailableModelRouteCollection(await this.transport.get<unknown>(
      "/models/routes/available", {}, signal));
  }

  async threadModelRoute(threadID: string, signal?: AbortSignal): Promise<ThreadModelRouteView> {
    if (!this.features.hasModelControl || !boundedIdentity(threadID) || threadID.trim() !== threadID) {
      throw new Error("Model control capability and a normalized Thread are required");
    }
    return parseThreadModelRoute(await this.transport.get<unknown>(
      `/threads/${encodeURIComponent(threadID)}/model-route`, {}, signal), threadID);
  }

  async providerSearchReadiness(threadID: string,
    signal?: AbortSignal): Promise<ProviderSearchReadinessView> {
    if (!boundedIdentity(threadID) || threadID.trim() !== threadID) {
      throw new Error("A normalized Thread is required to read search readiness");
    }
    return parseProviderSearchReadiness(await this.transport.get<unknown>(
      `/threads/${encodeURIComponent(threadID)}/search-readiness`, {}, signal), threadID);
  }

  async diagnoseThreadSearch(threadID: string, body: SearchDiagnosticsRequestView,
    signal?: AbortSignal): Promise<SearchDiagnosticsView> {
    if (!this.features.hasControl || !boundedIdentity(threadID) || threadID.trim() !== threadID ||
      body.version !== "search_diagnostics.v1" || body.confirm !== true) {
      throw new Error("Explicit confirmation and a normalized Thread are required for search diagnostics");
    }
    return parseSearchDiagnostics(await this.transport.sendControlRequest<unknown>(
      `/threads/${encodeURIComponent(threadID)}/search-diagnostics`, body, signal), threadID);
  }

  async selectThreadModelRoute(threadID: string, body: ThreadModelRouteControlRequestView,
    signal?: AbortSignal): Promise<ThreadModelRouteView> {
    const select = body.action === "select";
    const reset = body.action === "reset";
    if (!this.features.hasModelControl || !boundedIdentity(threadID) || threadID.trim() !== threadID ||
      body.version !== "thread_model_route_control.v1" || (!select && !reset) ||
      !boundedIdentity(body.operation_key) || !boundedIdentity(body.requested_by) ||
      (select && (!boundedIdentity(body.provider) || !boundedIdentity(body.model))) ||
      (reset && (body.provider !== undefined || body.model !== undefined))) {
      throw new Error("A normalized, exact Thread model route intent is required");
    }
    const result = await this.transport.sendControlRequest<unknown>(
      `/threads/${encodeURIComponent(threadID)}/model-route`, body, signal, "", "PUT");
    return parseThreadModelRoute(result, threadID, body);
  }

  async diagnoseProvider(body: ProviderDiagnosticRequestView,
    signal?: AbortSignal): Promise<ProviderDiagnosticView> {
    if (!this.features.hasModelControl || body.confirm_diagnostic !== true) {
      throw new Error("Explicit Provider diagnostic confirmation is required");
    }
    const result = await this.transport.sendControlRequest<unknown>("/models/diagnostics", body, signal);
    return parseProviderDiagnostic(result, body);
  }

  async qualifyModelHarness(body: ModelHarnessQualificationRequestView,
    signal?: AbortSignal): Promise<ModelHarnessQualificationView> {
    if (!this.features.hasModelControl || body.confirm_qualification !== true) {
      throw new Error("Explicit model Harness qualification confirmation is required");
    }
    const result = await this.transport.sendControlRequest<unknown>(
      "/models/harness-qualifications", body, signal,
    );
    return parseModelHarnessQualification(result, body);
  }

  async chatGPTAuthStatus(signal?: AbortSignal): Promise<ChatGPTAuthStatusView> {
    if (!this.features.hasChatGPTAuth) {
      throw new APIRequestError("ChatGPT 账户登录当前未启用。", "PERMISSION_DENIED", 403);
    }
    return this.parseChatGPTStatus(await this.transport.get<unknown>("/models/chatgpt", {}, signal));
  }

  async startChatGPTLogin(body: ChatGPTLoginRequestView, signal?: AbortSignal): Promise<ChatGPTAuthStatusView> {
    return this.chatGPTAuthAction("login", body, signal);
  }

  async cancelChatGPTLogin(body: ChatGPTCancelRequestView, signal?: AbortSignal): Promise<ChatGPTAuthStatusView> {
    return this.chatGPTAuthAction("cancel", body, signal);
  }

  async logoutChatGPTAccount(body: ChatGPTAccountRequestView, signal?: AbortSignal): Promise<ChatGPTAuthStatusView> {
    return this.chatGPTAuthAction("logout", body, signal);
  }

  async refreshChatGPTModels(body: ChatGPTAccountRequestView, signal?: AbortSignal): Promise<ChatGPTAuthStatusView> {
    return this.chatGPTAuthAction("models", body, signal);
  }

  async openChatGPTUsage(body: ChatGPTAuthRequestView, signal?: AbortSignal): Promise<ChatGPTAuthStatusView> {
    return this.chatGPTAuthAction("usage", body, signal);
  }

  private parseChatGPTStatus(value: unknown): ChatGPTAuthStatusView {
    try { return parseChatGPTAuthStatus(value); }
    catch { throw new APIRequestError("ChatGPT 账户状态无效，请重新读取。", "INVALID_RESPONSE", 502); }
  }

  private async chatGPTAuthAction(action: "login" | "cancel" | "logout" | "models" | "usage",
    body: ChatGPTAuthRequestView, signal?: AbortSignal): Promise<ChatGPTAuthStatusView> {
    if (!this.features.hasChatGPTAuth) {
      throw new APIRequestError("ChatGPT 账户登录当前未启用。", "PERMISSION_DENIED", 403);
    }
    const request = body as unknown;
    const identity = action === "cancel" ? "attempt_id" : "account_id";
    const requiresIdentity = action === "cancel" || action === "logout" || action === "models";
    const allowed = action === "usage" ? ["version", "confirm"] : ["version", "confirm", identity];
    if (!isRecord(request) || request.version !== chatGPTAuthProtocol || request.confirm !== true ||
      Object.keys(request).some((key) => !allowed.includes(key)) ||
      (requiresIdentity && !validChatGPTIdentity(request[identity])) ||
      (action === "login" && request.account_id !== undefined && !validChatGPTIdentity(request.account_id))) {
      throw new APIRequestError("ChatGPT 账户操作需要有效身份与明确确认。", "INVALID_ARGUMENT", 400);
    }
    return this.parseChatGPTStatus(await this.transport.sendControlRequest<unknown>(`/models/chatgpt/${action}`, body, signal));
  }

  async providerDefinitions(signal?: AbortSignal): Promise<ProviderDefinitionCollectionView> {
    if (!this.features.hasProviderDefinitions) {
      throw new Error("Custom Provider definition capability is required");
    }
    return parseProviderDefinitionCollection(await this.transport.get<unknown>(
      "/models/provider-definitions", {}, signal));
  }

  async discoverProviderModels(body: ProviderModelDiscoveryRequestView,
    signal?: AbortSignal): Promise<ProviderModelDiscoveryView> {
    if (!this.features.hasProviderDefinitions || body.version !== "provider_model_discovery.v1" ||
      body.confirm_discovery !== true || !validCustomProviderID(body.provider_id) ||
      typeof body.endpoint_url !== "string" || !body.endpoint_url || body.endpoint_url.length > 2048 ||
      !["openai_chat_completions", "openai_responses", "anthropic_messages"].includes(body.transport) ||
      !validProviderAdvancedConfig(body.advanced_config) ||
      (body.secret !== undefined && (typeof body.secret !== "string" || body.secret.length < 8 ||
        body.secret.length > 2560 || /\s/.test(body.secret))) ||
      (body.expected_definition_revision !== undefined && !safePositiveInteger(body.expected_definition_revision))) {
      throw new Error("Confirmed model discovery capability and a valid draft are required");
    }
    const value = await this.transport.sendControlRequest<unknown>("/models/model-discovery", body, signal);
    const safeText = (text: unknown, limit: number): text is string => typeof text === "string" &&
      text.length <= limit && text.trim() === text && !/[\x00-\x1f\x7f]/.test(text) &&
      (!body.secret || !text.includes(body.secret));
    if (!hasExactKeys(value, ["version", "source", "models", "truncated"]) ||
      value.version !== "provider_model_discovery.v1" || value.source !== "provider_api" ||
      typeof value.truncated !== "boolean" || !Array.isArray(value.models) || value.models.length > 512 ||
      value.models.some((model: unknown) => !isRecord(model) ||
        Object.keys(model).some((key) => !["id", "display_name", "input_token_limit", "output_token_limit"].includes(key)) ||
        !boundedIdentity(model.id) || !safeText(model.id, 256) ||
        (model.display_name !== undefined && !safeText(model.display_name, 256)) ||
        (model.input_token_limit !== undefined && (!safePositiveInteger(model.input_token_limit) || Number(model.input_token_limit) > 2_097_152)) ||
        (model.output_token_limit !== undefined && (!safePositiveInteger(model.output_token_limit) || Number(model.output_token_limit) > 1_000_000))) ||
      new Set(value.models.map((m: { id: unknown }) => m.id)).size !== value.models.length) {
      throw new APIRequestError("Provider model discovery returned an invalid catalog", "INVALID_RESPONSE", 502);
    }
    return value as unknown as ProviderModelDiscoveryView;
  }

  async upsertProviderDefinition(provider: string, body: ProviderDefinitionUpsertRequestView,
    signal?: AbortSignal): Promise<ProviderDefinitionMutationView> {
    if (!this.features.hasProviderDefinitions || !validCustomProviderID(provider) ||
      body.version !== "provider_definition_control.v1" || body.confirm !== true ||
      body.definition.id !== provider) {
      throw new Error("Confirmed custom Provider definition capability is required");
    }
    parseProviderDefinition(body.definition);
    return parseProviderDefinitionMutation(await this.transport.sendControlRequest<unknown>(
      `/models/provider-definitions/${encodeURIComponent(provider)}`, body, signal,
    ), provider, false);
  }

  async deleteProviderDefinition(provider: string, body: ProviderDefinitionDeleteRequestView,
    signal?: AbortSignal): Promise<ProviderDefinitionMutationView> {
    if (!this.features.hasProviderDefinitions || !validCustomProviderID(provider) ||
      body.version !== "provider_definition_control.v1" || body.confirm !== true ||
      !safeBoundedCount(body.expected_collection_revision, Number.MAX_SAFE_INTEGER) ||
      !safePositiveInteger(body.expected_definition_revision)) {
      throw new Error("Confirmed custom Provider deletion capability is required");
    }
    return parseProviderDefinitionMutation(await this.transport.sendControlRequest<unknown>(
      `/models/provider-definitions/${encodeURIComponent(provider)}/delete`, body, signal,
    ), provider, true);
  }

  async providerCredentialStatuses(signal?: AbortSignal): Promise<ProviderCredentialListView> {
    if (!this.features.hasProviderCredentials) {
      throw new Error("Provider credential capability is required");
    }
    return parseProviderCredentialList(await this.transport.get<unknown>("/models/credentials", {}, signal));
  }

  async changeProviderCredential(provider: string, body: ProviderCredentialRequestView,
    signal?: AbortSignal): Promise<ProviderCredentialStatusView> {
    if (!this.features.hasProviderCredentials ||
      !validProviderCredentialName(provider) ||
      body.version !== "provider_credential.v1" || body.confirm !== true ||
      (body.action === "set" ? typeof body.secret !== "string" || body.secret.length < 8 :
        body.action !== "delete" || body.secret !== "")) {
      throw new Error("Confirmed Provider credential capability is required");
    }
    const result = await this.transport.sendControlRequest<unknown>(
      `/models/credentials/${encodeURIComponent(provider)}`, body, signal,
    );
    const status = parseProviderCredentialStatus(result, provider);
    const applied = status.registry_reloaded && !status.restart_required &&
      status.registry_generation > 0;
    const deferred = !status.registry_reloaded && status.restart_required;
    if (status.configured !== (body.action === "set") || (!applied && !deferred)) {
      throw new APIRequestError("Provider credential change returned the wrong status",
        "INVALID_RESPONSE", 502);
    }
    return status;
  }

  async getPublicModelStream(runID: string,
    signal?: AbortSignal): Promise<PublicModelStreamSnapshot> {
    const normalizedRunID = boundedIdentity(runID);
    if (!normalizedRunID || normalizedRunID !== runID) {
      throw new Error("A normalized Run identity is required");
    }
    const result = await this.transport.get<unknown>(
      `/runs/${encodeURIComponent(runID)}/active-call`, {}, signal,
    );
    return parsePublicModelStream(result, runID);
  }

  async pollPublicModelStream(runID: string,
    signal?: AbortSignal): Promise<PublicModelStreamSnapshot | null> {
    const normalizedRunID = boundedIdentity(runID);
    if (!normalizedRunID || normalizedRunID !== runID) {
      throw new Error("A normalized Run identity is required");
    }
    const result = await this.transport.get<unknown>(
      `/runs/${encodeURIComponent(runID)}/active-call/poll`, {}, signal,
    );
    return parsePublicModelStreamPoll(result, runID);
  }

  async cancelModelCall(runID: string, body: ModelCancellationRequestView,
    idempotencyKey: string, signal?: AbortSignal): Promise<ModelCancellationView> {
    if (!this.features.hasControl) {
      throw new Error("A control bearer token is required for this operation");
    }
    const normalizedRunID = boundedIdentity(runID);
    if (!normalizedRunID || normalizedRunID !== runID) {
      throw new Error("A normalized Run identity is required");
    }
    if (!boundedIdentity(body.attempt_id) || !safePositiveInteger(body.model_attempt)) {
      throw new Error("A bound attempt identity and model attempt are required");
    }
    const result = await this.transport.sendControl<unknown>(
      `/runs/${encodeURIComponent(runID)}/active-call/cancel`, body, idempotencyKey, signal,
    );
    return parseModelCancellation(result, runID, body);
  }

  async cancelSpecialistModelCall(runID: string, agentID: string,
    body: ModelCancellationRequestView, idempotencyKey: string,
    signal?: AbortSignal): Promise<SpecialistModelCancellationView> {
    if (!this.features.hasControl) {
      throw new Error("A control bearer token is required for this operation");
    }
    const normalizedRunID = boundedIdentity(runID);
    const normalizedAgentID = boundedIdentity(agentID);
    if (!normalizedRunID || normalizedRunID !== runID ||
      !normalizedAgentID || normalizedAgentID !== agentID) {
      throw new Error("Normalized Run and Agent identities are required");
    }
    if (!boundedIdentity(body.attempt_id) || !safePositiveInteger(body.model_attempt)) {
      throw new Error("A bound attempt identity and model attempt are required");
    }
    const result = await this.transport.sendControl<unknown>(
      `/runs/${encodeURIComponent(runID)}/agents/${encodeURIComponent(agentID)}/active-call/cancel`,
      body, idempotencyKey, signal,
    );
    return parseSpecialistModelCancellation(result, runID, agentID, body);
  }

  async listPriceSnapshots(signal?: AbortSignal): Promise<PriceSnapshotListView> {
    const result = await this.transport.get<unknown>("/models/prices", {}, signal);
    return parsePriceSnapshots(result);
  }

  async importPriceSnapshot(body: PriceSnapshotImportRequestView, idempotencyKey: string,
    signal?: AbortSignal): Promise<PriceSnapshotImportView> {
    if (!this.features.hasControl) {
      throw new Error("A control bearer token is required for this operation");
    }
    if (body.version !== "price_snapshot.v1" || !body.document ||
      body.document.length > 64 * 1024) {
      throw new Error("A bounded price_snapshot.v1 document is required");
    }
    const result = await this.transport.sendControl<unknown>("/models/prices", body, idempotencyKey, signal);
    return parsePriceSnapshotImport(result);
  }
}
