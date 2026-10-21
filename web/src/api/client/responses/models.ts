import { APIRequestError } from "../protocol";
import {
  boundedIdentity,
  boundedText,
  hasExactKeys,
  hasOnlyKeys,
  hasRequiredOnlyKeys,
  isRecord,
  safeBoundedCount,
  safePositiveInteger,
  validDate,
} from "../validation";
import type {
  AvailableModelRouteCollectionView,
  ModelAvailabilityView,
  ModelHarnessAvailabilityView,
  ModelHarnessQualificationRequestView,
  ModelHarnessQualificationView,
  ModelRouteControlRequestView,
  PriceSnapshotImportView,
  PriceSnapshotListView,
  ProviderCredentialListView,
  ProviderCredentialStatusView,
  ProviderDefinitionCollectionView,
  ProviderDefinitionMutationView,
  ProviderDefinitionView,
  ProviderDiagnosticRequestView,
  ProviderDiagnosticView,
  ProviderSearchReadinessView,
  SearchDiagnosticsView,
  ThreadModelRouteControlRequestView,
  ThreadModelRouteView,
} from "../../types";

export function parseModelAvailability(value: unknown): ModelAvailabilityView {
  if (!hasExactKeys(value, ["generation", "protocol_version", "providers", "routes"]) ||
    value.protocol_version !== "model_availability.v2" || !Array.isArray(value.providers) ||
    !safeBoundedCount(value.generation, Number.MAX_SAFE_INTEGER) || value.generation < 1 ||
    !Array.isArray(value.routes) || value.providers.length > 70 || value.routes.length > 64) {
    throw new APIRequestError("Model availability response is invalid", "INVALID_RESPONSE", 502);
  }
  const providerNames = new Set<string>();
  const availableProviderNames = new Set<string>();
  const harnessReadiness = new Map<string, boolean>();
  for (const provider of value.providers) {
    if (!hasExactKeys(provider, ["configuration_error", "credential_source", "custom",
      "definition_revision", "display_name", "enabled", "harnesses", "kind", "models",
      "name", "native_web_search_capability", "native_web_search_runtime_enabled",
      "network_required", "search_mode", "status", "transport"]) ||
      !boundedText(provider.name, 128) || !boundedText(provider.display_name, 128) ||
      !["local", "anthropic_compatible", "openai_compatible", "ollama"].includes(
        String(provider.kind)) ||
      (provider.status !== "available" && provider.status !== "not_configured" &&
        provider.status !== "invalid_configuration") ||
      (provider.credential_source !== "none" && provider.credential_source !== "environment" &&
        provider.credential_source !== "system" && provider.credential_source !== "chatgpt_oauth") ||
      (provider.credential_source === "chatgpt_oauth" && (provider.custom !== false ||
        !/^chatgpt(?:-[a-f0-9]{32})?$/u.test(String(provider.name)) ||
        provider.kind !== "openai_compatible" || provider.transport !== "openai_responses" ||
        provider.network_required !== true)) ||
      typeof provider.network_required !== "boolean" || typeof provider.custom !== "boolean" ||
      typeof provider.enabled !== "boolean" ||
      !safeBoundedCount(provider.definition_revision, Number.MAX_SAFE_INTEGER) ||
      !["mock", "anthropic_messages", "openai_chat_completions", "openai_responses",
        "ollama_chat"].includes(
        String(provider.transport)) ||
      !["disabled", "auto", "web", "searxng", "provider_native"].includes(
        String(provider.search_mode)) ||
      !["unsupported", "declared_unverified"].includes(
        String(provider.native_web_search_capability)) ||
      provider.native_web_search_runtime_enabled !== false ||
      (provider.custom ? (!validCustomProviderID(provider.name) ||
        provider.definition_revision < 1) : (provider.definition_revision !== 0 ||
        (provider.credential_source !== "chatgpt_oauth" && provider.display_name !== provider.name) || provider.enabled !== true ||
        provider.search_mode !== "disabled" ||
        provider.native_web_search_capability !== "unsupported")) ||
      typeof provider.configuration_error !== "boolean" || !Array.isArray(provider.models) ||
      provider.models.length > 128 || !provider.models.every((model) => boundedText(model, 256)) ||
      !Array.isArray(provider.harnesses) || provider.harnesses.length !== provider.models.length ||
      providerNames.has(provider.name)) {
      throw new APIRequestError("Model Provider availability is invalid", "INVALID_RESPONSE", 502);
    }
    providerNames.add(provider.name);
    if (provider.status === "available") {
      availableProviderNames.add(provider.name);
    }
    const harnessModels = new Set<string>();
    for (const harness of provider.harnesses) {
      const parsed = parseModelHarnessAvailability(harness);
      if (!provider.models.includes(parsed.model) || harnessModels.has(parsed.model)) {
        throw new APIRequestError("Model Harness availability binding is invalid",
          "INVALID_RESPONSE", 502);
      }
      harnessModels.add(parsed.model);
      harnessReadiness.set(`${provider.name}\u0000${parsed.model}`, parsed.root_eligible);
    }
  }
  const routeNames = new Set<string>();
  for (const route of value.routes) {
    if (!hasExactKeys(route, ["available", "harness_ready", "model", "name", "provider"]) ||
      !boundedText(route.name, 128) || !boundedText(route.provider, 128) ||
      !boundedText(route.model, 256) || typeof route.available !== "boolean" ||
      typeof route.harness_ready !== "boolean" ||
      (route.available && !availableProviderNames.has(route.provider)) ||
      (route.harness_ready &&
        harnessReadiness.get(`${route.provider}\u0000${route.model}`) !== true) ||
      routeNames.has(route.name)) {
      throw new APIRequestError("Model route availability is invalid", "INVALID_RESPONSE", 502);
    }
    routeNames.add(route.name);
  }
  return value as unknown as ModelAvailabilityView;
}

export function parseModelHarnessAvailability(value: unknown): ModelHarnessAvailabilityView {
  if (!hasExactKeys(value, ["expires_at", "json_strategy", "latest_qualification_status",
    "model", "protocol_version", "qualification_checked_at", "qualification_source",
    "qualification_status", "qualified_at", "root_eligible", "streaming_qualified",
    "strict_json_qualified", "structured_json_eligible", "tool_calls_qualified",
    "tool_results_qualified", "tool_strategy", "transport_protocol"]) ||
    value.protocol_version !== "model_harness.v1" || !boundedText(value.model, 256) ||
    !["mock", "anthropic_messages", "openai_chat_completions", "openai_responses",
      "ollama_chat", "provider_contract"].includes(
      String(value.transport_protocol)) ||
    !["native", "none"].includes(String(value.tool_strategy)) ||
    !["native", "prompt", "none"].includes(String(value.json_strategy)) ||
    !["trusted_builtin", "qualification_required", "verified"].includes(
      String(value.qualification_status)) ||
    !qualificationStatusValid(value.latest_qualification_status) ||
    typeof value.qualification_checked_at !== "string" ||
    typeof value.qualification_source !== "string" ||
    (value.latest_qualification_status === ""
      ? (value.qualification_checked_at !== "" || value.qualification_source !== "")
      : (value.qualification_checked_at !== "" &&
        !validDate(value.qualification_checked_at)) ||
        !["diagnostic", "harness_qualification", "availability"].includes(
          String(value.qualification_source))) ||
    typeof value.tool_calls_qualified !== "boolean" ||
    typeof value.tool_results_qualified !== "boolean" ||
    typeof value.strict_json_qualified !== "boolean" ||
    typeof value.streaming_qualified !== "boolean" ||
    typeof value.root_eligible !== "boolean" ||
    typeof value.structured_json_eligible !== "boolean" ||
    typeof value.qualified_at !== "string" || typeof value.expires_at !== "string" ||
    value.structured_json_eligible !== value.strict_json_qualified ||
    value.root_eligible !== (value.tool_strategy === "native" &&
      value.tool_calls_qualified && value.tool_results_qualified &&
      value.strict_json_qualified && value.streaming_qualified) ||
    (value.qualification_status === "verified"
      ? (!validDate(value.qualified_at) || !validDate(value.expires_at) ||
        Date.parse(value.expires_at) <= Date.parse(value.qualified_at))
      : (value.qualified_at !== "" || value.expires_at !== "")) ||
    (value.tool_strategy === "none" && (value.tool_calls_qualified ||
      value.tool_results_qualified || value.streaming_qualified)) ||
    (value.json_strategy === "none" && value.strict_json_qualified)) {
    throw new APIRequestError("Model Harness availability is invalid", "INVALID_RESPONSE", 502);
  }
  return value as unknown as ModelHarnessAvailabilityView;
}

export const availableRouteCredentialStatuses = ["not_required", "configured", "not_configured",
  "invalid_configuration", "disabled", "unavailable"] as const;

export const availableRouteQualificationStatuses = ["unavailable", "not_configured", "available",
  "protocol_mismatch", "auth_failed", "network_failed", "rate_limit", "capacity",
  "model_unsupported", "response_incomplete", "trusted_builtin", "qualification_required", "verified"] as const;

export const unavailableRouteReasons = ["", "provider_disabled", "credential_not_configured",
  "invalid_configuration", "provider_unavailable", "harness_qualification_required",
  "not_configured", "protocol_mismatch", "auth_failed", "network_failed", "rate_limit",
  "capacity", "model_unsupported", "response_incomplete"] as const;

export function validVisionCapability(value: unknown): boolean {
  return hasExactKeys(value, ["state", "source"]) &&
    ["supported", "unsupported", "unknown"].includes(String(value.state)) &&
    ["operator_declared", "provider_metadata", "unknown", "adapter_unsupported"].includes(String(value.source));
}

export function parseAvailableModelRoute(value: unknown): AvailableModelRouteCollectionView["routes"][number] {
	const required = ["credential_status", "default_for_routes", "enabled",
		"definition_revision", "harness_ready", "model", "provider_id", "provider_name", "qualification_status",
    "selectable", "unavailable_reason"];
  if (!isRecord(value) || !hasOnlyKeys(value, [...required, "vision_capability"]) ||
    required.some((key) => !Object.prototype.hasOwnProperty.call(value, key)) ||
    (value.vision_capability !== undefined && !validVisionCapability(value.vision_capability)) ||
    !boundedIdentity(value.provider_id) || !boundedText(value.provider_name, 256) ||
		!boundedIdentity(value.model) || !safeBoundedCount(value.definition_revision, Number.MAX_SAFE_INTEGER) ||
		typeof value.enabled !== "boolean" ||
    typeof value.harness_ready !== "boolean" || typeof value.selectable !== "boolean" ||
    !availableRouteCredentialStatuses.includes(value.credential_status as never) ||
    !availableRouteQualificationStatuses.includes(value.qualification_status as never) ||
    !unavailableRouteReasons.includes(value.unavailable_reason as never) ||
    !Array.isArray(value.default_for_routes) || value.default_for_routes.length > 32 ||
    !value.default_for_routes.every((route) => Boolean(boundedIdentity(route)))) {
    throw new APIRequestError("Available model route response is invalid",
      "INVALID_RESPONSE", 502);
  }
  const defaults = value.default_for_routes as string[];
  const selectableQualification = ["available", "trusted_builtin", "verified"]
    .includes(String(value.qualification_status));
  if (new Set(defaults).size !== defaults.length ||
    (value.selectable && (!value.enabled || !value.harness_ready ||
      !["configured", "not_required"].includes(String(value.credential_status)) ||
      value.unavailable_reason !== "" || !selectableQualification)) ||
    (!value.selectable && value.unavailable_reason === "")) {
    throw new APIRequestError("Available model route response violated its eligibility contract",
      "INVALID_RESPONSE", 502);
  }
  return value as unknown as AvailableModelRouteCollectionView["routes"][number];
}

export function parseAvailableModelRouteCollection(value: unknown): AvailableModelRouteCollectionView {
  if (!hasExactKeys(value, ["generation", "protocol_version", "routes"]) ||
    value.protocol_version !== "model_route_catalog.v1" ||
    !safeBoundedCount(value.generation, Number.MAX_SAFE_INTEGER) || !Array.isArray(value.routes) ||
    value.routes.length > 1_024) {
    throw new APIRequestError("Available model route catalog is invalid", "INVALID_RESPONSE", 502);
  }
  const routes = value.routes.map(parseAvailableModelRoute);
  const keys = routes.map((route) => `${route.provider_id}\0${route.model}`);
  if (new Set(keys).size !== keys.length) {
    throw new APIRequestError("Available model route catalog contains duplicate routes",
      "INVALID_RESPONSE", 502);
  }
  return { ...value, routes } as AvailableModelRouteCollectionView;
}

export function parseThreadModelRoute(value: unknown, threadID: string,
  request?: ThreadModelRouteControlRequestView): ThreadModelRouteView {
  const required = ["active_run_unchanged", "applies_to", "model", "protocol_version",
    "provider", "replayed", "source", "thread_id"];
  if (!isRecord(value) || !hasOnlyKeys(value, [...required, "effective_run_id", "vision_capability"]) ||
    (value.vision_capability !== undefined && !validVisionCapability(value.vision_capability)) ||
    required.some((key) => !Object.prototype.hasOwnProperty.call(value, key)) ||
    value.protocol_version !== "thread_model_route.v1" || value.thread_id !== threadID ||
    !boundedIdentity(value.thread_id) || !boundedIdentity(value.provider) ||
    !boundedIdentity(value.model) ||
    !["thread_preference", "default", "active_run"].includes(String(value.source)) ||
    !["next_run", "current_and_next"].includes(String(value.applies_to)) ||
    typeof value.active_run_unchanged !== "boolean" || typeof value.replayed !== "boolean" ||
    (value.effective_run_id !== undefined && !boundedIdentity(value.effective_run_id))) {
    throw new APIRequestError("Thread model route response is invalid", "INVALID_RESPONSE", 502);
  }
  const currentAndNext = value.applies_to === "current_and_next";
  if (currentAndNext !== (value.effective_run_id !== undefined) ||
    (value.active_run_unchanged && currentAndNext) ||
    (value.source === "active_run" && !currentAndNext) ||
    (request?.action === "select" &&
      (value.provider !== request.provider || value.model !== request.model)) ||
    (request?.action === "reset" && value.source === "thread_preference")) {
    throw new APIRequestError("Thread model route response violated its exact binding",
      "INVALID_RESPONSE", 502);
  }
  return value as unknown as ThreadModelRouteView;
}

export const providerSearchStates = ["network_disabled", "missing_allowlist",
  "provider_unqualified", "provider_unavailable", "ready"] as const;

export const providerSearchReasons = ["run_network_disabled", "search_endpoint_not_allowlisted",
  "provider_native_qualification_required", "provider_native_qualification_failed",
  "no_active_run", "model_provider_unavailable", "provider_search_policy_disabled",
  "search_backend_not_configured", "provider_search_configuration_invalid",
  "search_backend_ready"] as const;

export const providerSearchRemediations = ["enable_network_allowlist", "add_required_target",
  "qualify_provider_search", "submit_to_create_successor", "configure_search_provider",
  "enable_provider_search", "repair_provider_configuration", "none"] as const;

export function validSearchTarget(value: unknown): value is string {
  if (typeof value !== "string" || value.length < 1 || value.length > 253 ||
    value !== value.trim() || value !== value.toLowerCase()) return false;
  try {
    const parsed = new URL(`https://${value}`);
    return parsed.hostname === value && parsed.username === "" && parsed.password === "" &&
      parsed.port === "" && parsed.pathname === "/" && parsed.search === "" && parsed.hash === "";
  } catch {
    return false;
  }
}

export function parseProviderSearchReadiness(value: unknown,
  threadID: string): ProviderSearchReadinessView {
  const required = ["capability_grant", "network_mode", "protocol_version", "reason",
    "remediation", "runtime_ready", "state", "thread_id"];
  const optional = ["detail_code", "mode_revision", "model", "model_route", "provider",
    "required_target", "run_id", "search_policy"];
  if (!isRecord(value) || !hasOnlyKeys(value, [...required, ...optional]) ||
    required.some((key) => !Object.prototype.hasOwnProperty.call(value, key)) ||
    value.protocol_version !== "provider_search_readiness.v1" || value.thread_id !== threadID ||
    !boundedIdentity(value.thread_id) ||
    !providerSearchStates.includes(value.state as never) ||
    !providerSearchReasons.includes(value.reason as never) ||
    !providerSearchRemediations.includes(value.remediation as never) ||
    !["", "disabled", "allowlist"].includes(String(value.network_mode)) ||
    typeof value.runtime_ready !== "boolean" || value.capability_grant !== false ||
    (value.run_id !== undefined && !boundedIdentity(value.run_id)) ||
    (value.model_route !== undefined && !boundedText(value.model_route, 512)) ||
    (value.provider !== undefined && !boundedIdentity(value.provider)) ||
    (value.model !== undefined && !boundedIdentity(value.model)) ||
    (value.search_policy !== undefined && !["disabled", "searxng", "provider_native", "auto", "web"]
      .includes(String(value.search_policy))) ||
    (value.detail_code !== undefined && !boundedText(value.detail_code, 256)) ||
    (value.required_target !== undefined && !validSearchTarget(value.required_target)) ||
    (value.mode_revision !== undefined && !safePositiveInteger(value.mode_revision))) {
    throw new APIRequestError("Provider search readiness response is invalid",
      "INVALID_RESPONSE", 502);
  }
  const ready = value.state === "ready";
  const noActiveRun = value.reason === "no_active_run";
  if (value.runtime_ready !== ready || (ready && value.remediation !== "none") ||
    (!ready && value.remediation === "none") ||
    (value.state === "network_disabled" && value.network_mode !== "disabled") ||
    (noActiveRun !== (value.run_id === undefined)) ||
    (!noActiveRun && (value.mode_revision === undefined || value.run_id === undefined))) {
    throw new APIRequestError("Provider search readiness violated its binding",
      "INVALID_RESPONSE", 502);
  }
  return value as unknown as ProviderSearchReadinessView;
}

export const searchDiagnosticCodes = ["none", "network", "rate_limited", "access_challenge",
  "authentication", "provider_rejected", "tool_unsupported", "no_usable_results",
  "invalid_response", "not_configured", "not_authorized", "timeout",
  "configuration_changed"] as const;

export function parseSearchDiagnostics(value: unknown, threadID: string): SearchDiagnosticsView {
  const required = ["backend", "checked_at", "code", "mode_revision", "model", "model_route",
    "network_request_attempted", "protocol_version", "provider", "result_count", "run_id",
    "search_policy", "state", "thread_id"];
  const optional = ["http_status", "rate_limit_reset", "required_target", "retry_after"];
  if (!isRecord(value) || !hasOnlyKeys(value, [...required, ...optional]) ||
    required.some((key) => !Object.prototype.hasOwnProperty.call(value, key)) ||
    value.protocol_version !== "search_diagnostics.v1" || value.thread_id !== threadID ||
    !boundedIdentity(value.thread_id) || !boundedIdentity(value.run_id) ||
    !safePositiveInteger(value.mode_revision) || !boundedText(value.model_route, 512) ||
    !boundedIdentity(value.provider) || !boundedIdentity(value.model) ||
    !["", "disabled", "auto", "web", "searxng", "provider_native"]
      .includes(String(value.search_policy)) || (value.backend !== "" && !boundedText(value.backend, 256)) ||
    !validDate(value.checked_at) || !["succeeded", "failed"].includes(String(value.state)) ||
    !searchDiagnosticCodes.includes(value.code as never) ||
    !safeBoundedCount(value.result_count, 10) || typeof value.network_request_attempted !== "boolean" ||
    (value.retry_after !== undefined && !boundedText(value.retry_after, 128)) ||
    (value.rate_limit_reset !== undefined && !boundedText(value.rate_limit_reset, 128)) ||
    (value.required_target !== undefined && !validSearchTarget(value.required_target)) ||
    (value.http_status !== undefined && (!Number.isSafeInteger(value.http_status) ||
      Number(value.http_status) < 100 || Number(value.http_status) > 599))) {
    throw new APIRequestError("Search diagnostics response is invalid", "INVALID_RESPONSE", 502);
  }
  const succeeded = value.state === "succeeded";
  if (succeeded !== (value.code === "none") ||
    (succeeded && (value.result_count === 0 || value.network_request_attempted !== true ||
      value.backend === "" || value.search_policy === "")) ||
    (!succeeded && value.result_count !== 0) ||
    ((value.code === "rate_limited" || value.code === "access_challenge") &&
      value.network_request_attempted !== true) ||
    (value.code !== "rate_limited" &&
      (value.retry_after !== undefined || value.rate_limit_reset !== undefined))) {
    throw new APIRequestError("Search diagnostics response violated its outcome binding",
      "INVALID_RESPONSE", 502);
  }
  return value as unknown as SearchDiagnosticsView;
}

export function parseModelRouteControl(value: unknown, route: string,
  request: ModelRouteControlRequestView): ModelAvailabilityView["routes"][number] {
  if (!hasExactKeys(value, ["available", "harness_ready", "model", "name", "provider"]) ||
    value.name !== route || value.provider !== request.provider || value.model !== request.model ||
    value.available !== true || typeof value.harness_ready !== "boolean") {
    throw new APIRequestError("Model route response violated its exact binding",
      "INVALID_RESPONSE", 502);
  }
  return value as unknown as ModelAvailabilityView["routes"][number];
}

export function parseModelHarnessQualification(value: unknown,
  request: ModelHarnessQualificationRequestView): ModelHarnessQualificationView {
  if (!hasExactKeys(value, ["duration_ms", "failure_reason", "harness", "model", "model_calls",
    "network_request_attempted", "outcome", "protocol_version", "provider",
    "qualification_status", "response_content_returned", "retryable", "status",
    "synthetic_tool_calls", "tool_executed"]) ||
    value.protocol_version !== "model_harness_qualification.v1" ||
    value.provider !== request.provider || value.model !== request.model ||
    !["qualified", "incompatible", "unreachable"].includes(String(value.status)) ||
    !qualificationStatusValid(value.qualification_status) ||
    !providerFailureReasonValid(value.failure_reason) ||
    !providerOutcomeValid(value.outcome) || typeof value.retryable !== "boolean" ||
    typeof value.network_request_attempted !== "boolean" ||
    !safeBoundedCount(value.model_calls, 2) ||
    !safeBoundedCount(value.synthetic_tool_calls, 16) ||
    value.tool_executed !== false || value.response_content_returned !== false ||
    !safeBoundedCount(value.duration_ms, 60_000) ||
    !qualificationResultSemanticsValid(value.status, value.outcome, value.failure_reason,
      value.retryable, value.network_request_attempted, value.model_calls)) {
    throw new APIRequestError("Model Harness qualification response is invalid",
      "INVALID_RESPONSE", 502);
  }
  const harness = parseModelHarnessAvailability(value.harness);
  if (harness.model !== request.model ||
    (value.status === "qualified" && !harness.root_eligible)) {
    throw new APIRequestError("Model Harness qualification binding is invalid",
      "INVALID_RESPONSE", 502);
  }
  return value as unknown as ModelHarnessQualificationView;
}

export function parseProviderDiagnostic(value: unknown, request: ProviderDiagnosticRequestView): ProviderDiagnosticView {
  if (!hasExactKeys(value, ["duration_ms", "failure_reason", "model", "model_called",
    "network_request_attempted", "outcome", "protocol_version", "provider",
    "qualification_status", "response_content_returned", "retryable", "status",
    "tool_called"]) ||
    value.protocol_version !== "provider_diagnostic.v1" || value.provider !== request.provider ||
    value.model !== request.model || (value.status !== "reachable" && value.status !== "unreachable") ||
    !qualificationStatusValid(value.qualification_status) ||
    !providerFailureReasonValid(value.failure_reason) ||
    !providerOutcomeValid(value.outcome) || typeof value.retryable !== "boolean" ||
    typeof value.network_request_attempted !== "boolean" || typeof value.model_called !== "boolean" ||
    value.tool_called !== false || value.response_content_returned !== false ||
    !safeBoundedCount(value.duration_ms, 60_000) ||
    !diagnosticResultSemanticsValid(value.status, value.outcome, value.failure_reason,
      value.retryable, value.network_request_attempted, value.model_called ? 1 : 0)) {
    throw new APIRequestError("Provider diagnostic response violated its content-free contract",
      "INVALID_RESPONSE", 502);
  }
  return value as unknown as ProviderDiagnosticView;
}

export function providerFailureReasonValid(value: unknown): boolean {
  return typeof value === "string" &&
    ["none", "not_configured", "authentication", "network", "rate_limit", "capacity",
      "model_not_found", "protocol_incompatible", "context_limit", "output_limit", "paused", "refusal"].includes(value);
}

// qualificationStatusValid accepts the closed per-endpoint qualification
// taxonomy. The empty string means the model was never diagnosed: the field
// is omitted server-side until a diagnostic or Harness qualification observes
// the endpoint.
export function qualificationStatusValid(value: unknown): boolean {
  return typeof value === "string" &&
    ["", "not_configured", "available", "protocol_mismatch", "auth_failed",
      "network_failed", "rate_limit", "capacity", "model_unsupported", "response_incomplete"].includes(value);
}

export function providerOutcomeValid(value: unknown): boolean {
  return typeof value === "string" &&
    ["success", "retryable", "rate_limited", "invalid_response", "cancelled",
      "permanent"].includes(value);
}

export function qualificationResultSemanticsValid(status: unknown, outcome: unknown, reason: unknown,
  retryable: unknown, networkAttempted: unknown, modelCalls: unknown): boolean {
  if (status === "qualified") {
    return outcome === "success" && reason === "none" && retryable === false &&
      (modelCalls === 0 || modelCalls === 2);
  }
  if (reason === "not_configured") {
    return status === "unreachable" && outcome === "permanent" && retryable === false &&
      networkAttempted === false && modelCalls === 0;
  }
  if (status === "incompatible") {
    return outcome === "invalid_response" && reason === "protocol_incompatible" &&
      retryable === false;
  }
  if (status !== "unreachable" || reason === "none" || outcome === "success" ||
    outcome === "invalid_response") {
    return false;
  }
  return retryable === (outcome === "retryable" || outcome === "rate_limited");
}

export function diagnosticResultSemanticsValid(status: unknown, outcome: unknown, reason: unknown,
  retryable: unknown, networkAttempted: unknown, modelCalls: unknown): boolean {
  if (status === "reachable") {
    return outcome === "success" && reason === "none" && retryable === false && modelCalls === 1;
  }
  if (reason === "not_configured") {
    return outcome === "permanent" && retryable === false && networkAttempted === false &&
      modelCalls === 0;
  }
  if (reason === "none" || outcome === "success" || modelCalls !== 1) {
    return false;
  }
  return retryable === (outcome === "retryable" || outcome === "rate_limited");
}

export function parseProviderCredentialStatus(value: unknown,
  expectedProvider = ""): ProviderCredentialStatusView {
  if (!hasExactKeys(value, ["configured", "plaintext_returned", "protocol_version",
    "provider", "registry_generation", "registry_reloaded", "restart_required",
    "store_available", "store_kind"]) ||
    value.protocol_version !== "provider_credential.v1" ||
    !validProviderCredentialName(value.provider) ||
    (expectedProvider !== "" && value.provider !== expectedProvider) ||
    typeof value.configured !== "boolean" || typeof value.store_available !== "boolean" ||
    !boundedText(value.store_kind, 128) || value.plaintext_returned !== false ||
    typeof value.restart_required !== "boolean" || typeof value.registry_reloaded !== "boolean" ||
    !safeBoundedCount(value.registry_generation, Number.MAX_SAFE_INTEGER) ||
    (value.registry_reloaded && (value.restart_required || value.registry_generation < 1))) {
    throw new APIRequestError("Provider credential status violated its plaintext-free contract",
      "INVALID_RESPONSE", 502);
  }
  return value as unknown as ProviderCredentialStatusView;
}

export function parseProviderCredentialList(value: unknown): ProviderCredentialListView {
  if (!hasExactKeys(value, ["items", "protocol_version"]) ||
    value.protocol_version !== "provider_credential.v1" || !Array.isArray(value.items) ||
    value.items.length < 4 || value.items.length > 68) {
    throw new APIRequestError("Provider credential list is invalid", "INVALID_RESPONSE", 502);
  }
  const items = value.items.map((item) => parseProviderCredentialStatus(item));
  if (new Set(items.map((item) => item.provider)).size !== items.length ||
    items.some((item) => item.restart_required || item.registry_reloaded) ||
    new Set(items.map((item) => item.registry_generation)).size !== 1) {
    throw new APIRequestError("Provider credential list widened status authority",
      "INVALID_RESPONSE", 502);
  }
  return { ...value, items } as unknown as ProviderCredentialListView;
}

export const customProviderDefinitionKeys = ["advanced_config", "default_model", "display_name",
  "enabled", "endpoint_url", "id", "models", "native_web_search_capability", "note",
  "revision", "search_mode", "transport", "version", "website_url"];

export function validProviderCredentialName(value: unknown): value is string {
  return typeof value === "string" && value.length > 0 && value.length <= 64 &&
    /^[\p{L}\p{N}_-]+$/u.test(value);
}

export function validCustomProviderID(value: unknown): value is string {
  return typeof value === "string" && value.length > 0 && value.length <= 64 &&
    /^[a-z][a-z0-9_-]*$/u.test(value) &&
    !["anthropic", "deepseek", "mimo", "mock", "ollama", "openai"].includes(value);
}

export function validProviderAdvancedConfig(value: unknown): boolean {
  if (!isRecord(value)) return false;
  try {
    if (new TextEncoder().encode(JSON.stringify(value)).length > 65_536) return false;
  } catch {
    return false;
  }
  let nodes = 0;
  const visit = (current: unknown, depth: number): boolean => {
    nodes += 1;
    if (nodes > 2_048 || depth > 16) return false;
    if (current === null || typeof current === "boolean" || typeof current === "number" ||
      typeof current === "string") return true;
    if (Array.isArray(current)) return current.every((child) => visit(child, depth + 1));
    return isRecord(current) && Object.entries(current).every(([key, child]) =>
      key.length > 0 && key.length <= 128 && visit(child, depth + 1));
  };
  return visit(value, 1);
}

export function parseProviderDefinition(value: unknown): ProviderDefinitionView {
  if (!hasExactKeys(value, customProviderDefinitionKeys) ||
    value.version !== "provider_definition.v1" || !validCustomProviderID(value.id) ||
    !boundedText(value.display_name, 128) ||
    typeof value.note !== "string" || value.note.length > 2_048 ||
    typeof value.website_url !== "string" || value.website_url.length > 2_048 ||
    typeof value.endpoint_url !== "string" || value.endpoint_url.length === 0 ||
    value.endpoint_url.length > 2_048 || !boundedIdentity(value.default_model) ||
    !Array.isArray(value.models) || value.models.length < 1 || value.models.length > 128 ||
    !value.models.every((model) => Boolean(boundedIdentity(model))) ||
    new Set(value.models).size !== value.models.length ||
    !value.models.includes(value.default_model as string) ||
    !["openai_chat_completions", "openai_responses", "anthropic_messages"]
      .includes(String(value.transport)) ||
    !["disabled", "auto", "web", "searxng", "provider_native"].includes(String(value.search_mode)) ||
    !["unsupported", "declared_unverified"].includes(
      String(value.native_web_search_capability)) ||
    (value.search_mode === "provider_native" &&
      value.native_web_search_capability !== "declared_unverified") ||
    typeof value.enabled !== "boolean" || !safeBoundedCount(value.revision, Number.MAX_SAFE_INTEGER) ||
    !validProviderAdvancedConfig(value.advanced_config)) {
    throw new APIRequestError("Custom Provider definition is invalid", "INVALID_RESPONSE", 502);
  }
  return value as unknown as ProviderDefinitionView;
}

export function parseProviderDefinitionCollection(value: unknown): ProviderDefinitionCollectionView {
  if (!hasExactKeys(value, ["providers", "revision", "version"]) ||
    value.version !== "provider_definition_collection.v1" ||
    !safeBoundedCount(value.revision, Number.MAX_SAFE_INTEGER) ||
    !Array.isArray(value.providers) || value.providers.length > 64) {
    throw new APIRequestError("Custom Provider collection is invalid", "INVALID_RESPONSE", 502);
  }
  const providers = value.providers.map(parseProviderDefinition);
  if (new Set(providers.map((provider) => provider.id)).size !== providers.length ||
    providers.some((provider, index) => index > 0 && providers[index - 1].id >= provider.id)) {
    throw new APIRequestError("Custom Provider collection order is invalid", "INVALID_RESPONSE", 502);
  }
  return { ...value, providers } as ProviderDefinitionCollectionView;
}

export function parseProviderDefinitionMutation(value: unknown,
  expectedProvider: string, remove: boolean): ProviderDefinitionMutationView {
  if (!hasRequiredOnlyKeys(value, ["collection", "protocol_version", "registry_generation",
    "registry_reloaded"], ["definition", "deleted_id"]) ||
    value.protocol_version !== "provider_definition_control.v1" ||
    value.registry_reloaded !== true ||
    !safePositiveInteger(value.registry_generation)) {
    throw new APIRequestError("Custom Provider mutation result is invalid", "INVALID_RESPONSE", 502);
  }
  const collection = parseProviderDefinitionCollection(value.collection);
  if (remove) {
    if (value.deleted_id !== expectedProvider || value.definition !== undefined ||
      collection.providers.some((provider) => provider.id === expectedProvider)) {
      throw new APIRequestError("Custom Provider deletion returned the wrong identity",
        "INVALID_RESPONSE", 502);
    }
    return { ...value, collection } as ProviderDefinitionMutationView;
  }
  const definition = parseProviderDefinition(value.definition);
  if (definition.id !== expectedProvider || value.deleted_id !== undefined ||
    !collection.providers.some((provider) => provider.id === expectedProvider &&
      provider.revision === definition.revision)) {
    throw new APIRequestError("Custom Provider upsert returned the wrong identity",
      "INVALID_RESPONSE", 502);
  }
  return { ...value, collection, definition } as ProviderDefinitionMutationView;
}

export function parsePriceSnapshots(value: unknown): PriceSnapshotListView {
  if (!isRecord(value) || value.protocol_version !== "price_snapshot_list.v1" ||
    !Array.isArray(value.items) || value.items.length > 32) {
    throw new APIRequestError("Price snapshot list is invalid", "INVALID_RESPONSE", 502);
  }
  for (const item of value.items) {
    if (!isRecord(item) || !boundedIdentity(String(item.id)) || !validDate(item.imported_at) ||
      !Array.isArray(item.entries) || item.entries.length > 512) {
      throw new APIRequestError("Price snapshot item is invalid", "INVALID_RESPONSE", 502);
    }
  }
  return value as unknown as PriceSnapshotListView;
}

export function parsePriceSnapshotImport(value: unknown): PriceSnapshotImportView {
  if (!isRecord(value) || value.protocol_version !== "price_snapshot.v1" ||
    !boundedIdentity(String(value.id)) || !boundedIdentity(String(value.fingerprint)) ||
    !safeBoundedCount(value.entry_count, 512) || typeof value.replayed !== "boolean") {
    throw new APIRequestError("Price snapshot import is invalid", "INVALID_RESPONSE", 502);
  }
  return value as unknown as PriceSnapshotImportView;
}
